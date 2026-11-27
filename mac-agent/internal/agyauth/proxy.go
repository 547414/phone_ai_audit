package agyauth

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"phone-ai-audit/mac-agent/internal/config"
)

var proxyEnvKeys = []string{
	"HTTP_PROXY",
	"HTTPS_PROXY",
	"ALL_PROXY",
	"NO_PROXY",
	"http_proxy",
	"https_proxy",
	"all_proxy",
	"no_proxy",
}

// collectCallerProxyEnv captures proxy environment variables from the current process environment.
func collectCallerProxyEnv(getenv func(string) string) map[string]string {
	if getenv == nil {
		getenv = os.Getenv
	}
	res := make(map[string]string)
	for _, k := range proxyEnvKeys {
		if v := strings.TrimSpace(getenv(k)); v != "" {
			res[k] = v
		}
	}
	return res
}

// detectFallbackProxy resolves a proxy URL from Phone AI Audit config.json or macOS system proxy (`scutil --proxy`).
func detectFallbackProxy() (httpProxy, httpsProxy, allProxy string) {
	if cfg, err := config.Load(); err == nil {
		if p := normalizeProxyURL(cfg.ProxyURL); p != "" {
			return p, p, ""
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 1500*time.Millisecond)
	defer cancel()
	out, err := exec.CommandContext(ctx, "/usr/sbin/scutil", "--proxy").Output()
	if err != nil {
		return "", "", ""
	}
	return parseScutilProxyOutput(out)
}

func parseScutilProxyOutput(out []byte) (httpProxy, httpsProxy, allProxy string) {
	kv := make(map[string]string)
	sc := bufio.NewScanner(bytes.NewReader(out))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		k, v, ok := strings.Cut(line, ":")
		if ok {
			kv[strings.TrimSpace(k)] = strings.TrimSpace(v)
		}
	}
	if kv["HTTPEnable"] == "1" && kv["HTTPProxy"] != "" && kv["HTTPPort"] != "" {
		httpProxy = fmt.Sprintf("http://%s:%s", kv["HTTPProxy"], kv["HTTPPort"])
	}
	if kv["HTTPSEnable"] == "1" && kv["HTTPSProxy"] != "" && kv["HTTPSPort"] != "" {
		httpsProxy = fmt.Sprintf("http://%s:%s", kv["HTTPSProxy"], kv["HTTPSPort"])
	}
	if kv["SOCKSEnable"] == "1" && kv["SOCKSProxy"] != "" && kv["SOCKSPort"] != "" {
		allProxy = fmt.Sprintf("socks5://%s:%s", kv["SOCKSProxy"], kv["SOCKSPort"])
	}
	if httpsProxy == "" && httpProxy != "" {
		httpsProxy = httpProxy
	}
	if httpProxy == "" && httpsProxy != "" {
		httpProxy = httpsProxy
	}
	return httpProxy, httpsProxy, allProxy
}

func normalizeProxyURL(p string) string {
	p = strings.TrimSpace(p)
	if p == "" {
		return ""
	}
	if !strings.Contains(p, "://") {
		p = "http://" + p
	}
	return p
}

func hasActiveProxyEnv(env map[string]string) bool {
	for _, k := range proxyEnvKeys {
		if k == "NO_PROXY" || k == "no_proxy" {
			continue
		}
		if strings.TrimSpace(env[k]) != "" {
			return true
		}
	}
	return false
}

func appendProxyExportStmts(stmts []string, callerEnv map[string]string, fbHTTP, fbHTTPS, fbAll string) []string {
	// Forward caller shell proxy env vars verbatim (in deterministic order).
	hasCallerProxy := false
	for _, k := range proxyEnvKeys {
		if v := strings.TrimSpace(callerEnv[k]); v != "" {
			stmts = append(stmts, fmt.Sprintf("export %s=%s", k, shellQuote(v)))
			if k != "NO_PROXY" && k != "no_proxy" {
				hasCallerProxy = true
			}
		}
	}

	// If caller shell didn't explicitly set a proxy, fall back to config.json or macOS system proxy
	// ONLY when proxy variables are still unset.
	if !hasCallerProxy && (fbHTTP != "" || fbHTTPS != "" || fbAll != "") {
		var fbLines []string
		if fbHTTP != "" {
			fbLines = append(fbLines, fmt.Sprintf("export http_proxy=%s HTTP_PROXY=%s", shellQuote(fbHTTP), shellQuote(fbHTTP)))
		}
		if fbHTTPS != "" {
			fbLines = append(fbLines, fmt.Sprintf("export https_proxy=%s HTTPS_PROXY=%s", shellQuote(fbHTTPS), shellQuote(fbHTTPS)))
		}
		if fbAll != "" {
			fbLines = append(fbLines, fmt.Sprintf("export all_proxy=%s ALL_PROXY=%s", shellQuote(fbAll), shellQuote(fbAll)))
		}
		stmts = append(stmts, fmt.Sprintf(
			`if [[ -z "${https_proxy:-}${HTTPS_PROXY:-}${http_proxy:-}${HTTP_PROXY:-}${all_proxy:-}${ALL_PROXY:-}" ]]; then %s; fi`,
			strings.Join(fbLines, "; "),
		))
	}
	return stmts
}

// buildProxyShellExports generates zsh statements that:
//  1. Source ~/.zshrc (because `zsh -lc` is non-interactive and otherwise skips ~/.zshrc proxy exports),
//  2. Explicitly re-apply any proxy env vars already set in the caller shell (overriding stale tmux server env),
//  3. Fall back to config.json / macOS system proxy only if ~/.zshrc and caller shell left proxy vars unset.
func buildProxyShellExports(callerEnv map[string]string, fbHTTP, fbHTTPS, fbAll string) string {
	stmts := []string{
		// Load user's interactive zsh settings (~/.zshrc) where PATH and proxy exports commonly live.
		`[[ -f "$HOME/.zshrc" ]] && source "$HOME/.zshrc" >/dev/null 2>&1 || true`,
	}
	stmts = appendProxyExportStmts(stmts, callerEnv, fbHTTP, fbHTTPS, fbAll)
	return strings.Join(stmts, "; ")
}

// buildInteractiveProxyShellExports prepares proxy env vars right before `exec /bin/zsh -l`.
// It intentionally skips pre-sourcing ~/.zshrc because the interactive login shell will load ~/.zshrc once.
func buildInteractiveProxyShellExports(callerEnv map[string]string, fbHTTP, fbHTTPS, fbAll string) string {
	stmts := appendProxyExportStmts(nil, callerEnv, fbHTTP, fbHTTPS, fbAll)
	return strings.Join(stmts, "; ")
}
