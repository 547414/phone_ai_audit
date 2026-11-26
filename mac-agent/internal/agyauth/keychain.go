package agyauth

import (
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// ErrNotFound is returned when a Keychain generic password item does not exist.
var ErrNotFound = errors.New("keychain item not found")

// KeychainStore abstracts macOS Keychain generic-password CRUD so tests never touch the live Keychain.
type KeychainStore interface {
	Get(service, account string) ([]byte, error)
	Set(service, account string, secret []byte, trustedApps []string) error
	Delete(service, account string) error
}

// MacOSKeychainStore interacts with the native macOS Keychain via /usr/bin/security.
// Secrets are written through `security -i` stdin using `-X <hex>` so exact raw bytes are
// preserved without exposing credentials in process arguments (`ps`).
type MacOSKeychainStore struct {
	SecurityBin string
}

func (s *MacOSKeychainStore) bin() string {
	if s != nil && s.SecurityBin != "" {
		return s.SecurityBin
	}
	return "/usr/bin/security"
}

func (s *MacOSKeychainStore) Get(service, account string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	var stdout, stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, s.bin(), "find-generic-password", "-s", service, "-a", account, "-w")
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		errTxt := strings.TrimSpace(stderr.String())
		if isSecurityNotFound(errTxt) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("security find-generic-password (%s/%s): %w (%s)", service, account, err, errTxt)
	}

	raw := strings.TrimRight(stdout.String(), "\r\n")
	if raw == "" {
		return []byte{}, nil
	}

	// macOS `security find-generic-password -w` prints raw UTF-8 for single-line printable strings,
	// or an even-length lowercase hex string when the value contains newlines or non-printable bytes.
	// If the output looks like even-length hex, confirm unambiguously via `-g` (`password: 0x...`).
	if looksLikeLowerHex(raw) {
		isHex, gErr := s.isItemHexEncoded(ctx, service, account)
		if gErr == nil && isHex {
			if decoded, derr := hex.DecodeString(raw); derr == nil {
				return decoded, nil
			}
		}
	}
	return []byte(raw), nil
}

func (s *MacOSKeychainStore) isItemHexEncoded(ctx context.Context, service, account string) (bool, error) {
	var stdout, stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, s.bin(), "find-generic-password", "-s", service, "-a", account, "-g")
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return false, err
	}
	combined := stderr.String() + "\n" + stdout.String()
	for _, line := range strings.Split(combined, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "password: 0x") {
			return true, nil
		}
		if strings.HasPrefix(trimmed, "password: \"") {
			return false, nil
		}
	}
	return false, nil
}

func (s *MacOSKeychainStore) Set(service, account string, secret []byte, trustedApps []string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Build a single `security -i` command line so `-X <hex>` is delivered over stdin instead of argv.
	var parts []string
	parts = append(parts, "add-generic-password", "-U",
		"-s", quoteSecurityArg(service),
		"-a", quoteSecurityArg(account),
		"-X", hex.EncodeToString(secret),
	)
	seenApp := make(map[string]struct{}, len(trustedApps)+1)
	for _, app := range trustedApps {
		app = strings.TrimSpace(app)
		if app == "" {
			continue
		}
		// SecTrustedApplicationCreateFromPath fails with ENOENT if any -T path does not exist on disk.
		if fi, err := os.Stat(app); err != nil || fi.IsDir() {
			continue
		}
		if _, dup := seenApp[app]; dup {
			continue
		}
		seenApp[app] = struct{}{}
		parts = append(parts, "-T", quoteSecurityArg(app))
	}
	line := strings.Join(parts, " ") + "\n"

	var stdout, stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, s.bin(), "-i")
	cmd.Stdin = strings.NewReader(line)
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("security add-generic-password (%s/%s): %w (%s)", service, account, err, strings.TrimSpace(stderr.String()))
	}
	errTxt := strings.TrimSpace(stderr.String())
	if errTxt != "" {
		return fmt.Errorf("security add-generic-password (%s/%s): %s", service, account, errTxt)
	}
	return nil
}

func (s *MacOSKeychainStore) Delete(service, account string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	var stdout, stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, s.bin(), "delete-generic-password", "-s", service, "-a", account)
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		errTxt := strings.TrimSpace(stderr.String())
		if isSecurityNotFound(errTxt) {
			return ErrNotFound
		}
		return fmt.Errorf("security delete-generic-password (%s/%s): %w (%s)", service, account, err, errTxt)
	}
	return nil
}

func isSecurityNotFound(errTxt string) bool {
	low := strings.ToLower(errTxt)
	return strings.Contains(low, "could not be found") ||
		strings.Contains(low, "errsecitemnotfound") ||
		strings.Contains(low, "item could not be found")
}

func looksLikeLowerHex(s string) bool {
	if len(s) < 2 || len(s)%2 != 0 {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
			return false
		}
	}
	return true
}

func quoteSecurityArg(s string) string {
	// `security -i` splits tokens on whitespace and accepts double-quoted strings with backslash escapes.
	escaped := strings.ReplaceAll(s, `\`, `\\`)
	escaped = strings.ReplaceAll(escaped, `"`, `\"`)
	return `"` + escaped + `"`
}

// DiscoverAgyTrustedApps collects existing executable paths that should keep passwordless Keychain access
// when creating a fresh `gemini/antigravity` Keychain item (never using insecure `-A`).
func DiscoverAgyTrustedApps() []string {
	apps := []string{}
	seen := map[string]bool{}
	addIfExists := func(p string) {
		p = strings.TrimSpace(p)
		if p == "" {
			return
		}
		if fi, err := os.Stat(p); err == nil && !fi.IsDir() && !seen[p] {
			seen[p] = true
			apps = append(apps, p)
		}
		if resolved, err := filepath.EvalSymlinks(p); err == nil && resolved != "" {
			if fi, serr := os.Stat(resolved); serr == nil && !fi.IsDir() && !seen[resolved] {
				seen[resolved] = true
				apps = append(apps, resolved)
			}
		}
	}
	addIfExists("/usr/bin/security")
	if p, err := exec.LookPath("agy"); err == nil {
		addIfExists(p)
	}
	home, _ := os.UserHomeDir()
	for _, cand := range []string{
		filepath.Join(home, ".local", "bin", "agy"),
		filepath.Join(home, ".gemini", "antigravity-cli", "bin", "agy"),
		"/opt/homebrew/bin/agy",
		"/usr/local/bin/agy",
	} {
		addIfExists(cand)
	}
	return apps
}
