package agyauth

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"phone-ai-audit/mac-agent/internal/term"
	"phone-ai-audit/mac-agent/internal/usage"
)

const (
	DefaultAgyService          = "gemini"
	DefaultAgyAccount          = "antigravity"
	VaultService               = "phone-ai-audit-agy"
	profilesVersion            = 2
	DefaultRefreshReminderDays = 14
)

// Profile holds non-secret metadata for a saved Antigravity account profile.
// The actual OAuth credential bytes are stored exclusively in macOS Keychain under VaultService.
type Profile struct {
	Name             string `json:"name"`
	Email            string `json:"email,omitempty"`
	Fingerprint      string `json:"fingerprint"` // 16-char SHA-256 prefix of raw Keychain credential bytes
	KeyService       string `json:"key_service,omitempty"`
	KeyAccount       string `json:"key_account,omitempty"`
	SavedAt          int64  `json:"saved_at"`
	LastUsedAt       int64  `json:"last_used_at,omitempty"`
	HasRefreshToken  bool   `json:"has_refresh_token,omitempty"`
	TokenExpiresAt   int64  `json:"token_expires_at,omitempty"`   // unix ms (short-lived access/id token expiry if present)
	RefreshExpiresAt int64  `json:"refresh_expires_at,omitempty"` // unix ms (refresh/session expiry if present in payload)
	CustomTTLMs      int64  `json:"custom_ttl_ms,omitempty"`      // >0 user custom expiry TTL from SavedAt; <0 disabled; 0 auto
	RemindAt         int64  `json:"remind_at,omitempty"`          // computed effective expiry / re-login reminder timestamp (unix ms)
	ExpireSource     string `json:"expire_source,omitempty"`      // "custom_ttl" | "refresh_token" | "access_token" | "refresh_reminder"
}

type profilesFile struct {
	Version  int        `json:"version"`
	Profiles []*Profile `json:"profiles"`
}

// CredentialMeta contains non-secret metadata inspected in memory from raw Keychain bytes.
type CredentialMeta struct {
	Email            string
	HasRefreshToken  bool
	TokenExpiresAt   int64
	RefreshExpiresAt int64
}

// ExpiryState summarizes whether a profile's login is healthy, expiring soon, or expired.
type ExpiryState struct {
	ExpiresAt    int64  // unix ms (0 if disabled / unknown)
	Source       string // "custom_ttl" | "refresh_token" | "access_token" | "refresh_reminder" | ""
	Expired      bool
	ExpiringSoon bool
	HardExpiry   bool // true for explicit access/refresh token expiry or user TTL; false for soft 14d reminder
	Summary      string
	Warning      string
}

// Options configures paths and Keychain backend (primarily for dependency injection in tests).
type Options struct {
	ProfilesPath string
	AgyQuotaPath string
	AgyService   string
	AgyAccount   string
	VaultService string
	Store        KeychainStore
	TrustedApps  []string
	NowFunc      func() time.Time
}

// Manager coordinates saving, listing, switching, expiry tracking, and launching terminals for Antigravity profiles.
type Manager struct {
	profilesPath string
	agyQuotaPath string
	agyService   string
	agyAccount   string
	vaultService string
	store        KeychainStore
	trustedApps  []string
	nowFunc      func() time.Time
}

// DefaultProfilesPath returns ~/.config/ai-audit/agy-profiles.json.
func DefaultProfilesPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config", "ai-audit", "agy-profiles.json")
}

// New constructs a Manager with sensible macOS defaults.
func New(opts Options) *Manager {
	profilesPath := opts.ProfilesPath
	if profilesPath == "" {
		profilesPath = DefaultProfilesPath()
	}
	agyQuotaPath := opts.AgyQuotaPath
	if agyQuotaPath == "" {
		agyQuotaPath = usage.DefaultAgyQuotaPath()
	}
	agySvc := strings.TrimSpace(opts.AgyService)
	if agySvc == "" {
		agySvc = strings.TrimSpace(os.Getenv("AI_AUDIT_AGY_KEYCHAIN_SERVICE"))
	}
	if agySvc == "" {
		agySvc = DefaultAgyService
	}
	agyAcct := strings.TrimSpace(opts.AgyAccount)
	if agyAcct == "" {
		agyAcct = strings.TrimSpace(os.Getenv("AI_AUDIT_AGY_KEYCHAIN_ACCOUNT"))
	}
	if agyAcct == "" {
		agyAcct = DefaultAgyAccount
	}
	vaultSvc := strings.TrimSpace(opts.VaultService)
	if vaultSvc == "" {
		vaultSvc = VaultService
	}
	store := opts.Store
	if store == nil {
		store = &MacOSKeychainStore{}
	}
	trusted := opts.TrustedApps
	if trusted == nil {
		trusted = DiscoverAgyTrustedApps()
	}
	nowFn := opts.NowFunc
	if nowFn == nil {
		nowFn = time.Now
	}
	return &Manager{
		profilesPath: profilesPath,
		agyQuotaPath: agyQuotaPath,
		agyService:   agySvc,
		agyAccount:   agyAcct,
		vaultService: vaultSvc,
		store:        store,
		trustedApps:  trusted,
		nowFunc:      nowFn,
	}
}

func (m *Manager) now() time.Time {
	if m != nil && m.nowFunc != nil {
		return m.nowFunc()
	}
	return time.Now()
}

// ActiveSlotInfo describes the currently active credential inside Antigravity's global Keychain slot.
type ActiveSlotInfo struct {
	Present     bool
	Service     string
	Account     string
	Fingerprint string
	Meta        CredentialMeta
	EmailHint   string
	RawBytes    []byte
}

// ProfileStatus combines saved profile metadata with live active status, expiry state, and official quota info.
type ProfileStatus struct {
	Profile
	Active      bool
	Expiry      ExpiryState
	PlanTier    string
	Quota5h     string
	QuotaWeekly string
}

// RemoteProfileItem is the zero-secret JSON payload sent to mobile clients for account selection.
type RemoteProfileItem struct {
	Name          string `json:"name"`
	Email         string `json:"email,omitempty"`
	Active        bool   `json:"active"`
	PlanTier      string `json:"plan_tier,omitempty"`
	Quota5h       string `json:"quota_5h,omitempty"`
	QuotaWeekly   string `json:"quota_weekly,omitempty"`
	SavedAt       int64  `json:"saved_at,omitempty"`
	LastUsedAt    int64  `json:"last_used_at,omitempty"`
	Expired       bool   `json:"expired,omitempty"`
	ExpiringSoon  bool   `json:"expiring_soon,omitempty"`
	HardExpiry    bool   `json:"hard_expiry,omitempty"`
	ExpireSummary string `json:"expire_summary,omitempty"`
	ExpireWarning string `json:"expire_warning,omitempty"`
}

// RemoteProfilesSnapshot summarizes available Antigravity accounts for mobile terminal creation.
type RemoteProfilesSnapshot struct {
	AgyInstalled  bool                `json:"agy_installed"`
	ActivePresent bool                `json:"active_present"`
	ActiveEmail   string              `json:"active_email,omitempty"`
	ActiveProfile string              `json:"active_profile,omitempty"`
	ActiveUnsaved bool                `json:"active_unsaved,omitempty"`
	DefaultCwd    string              `json:"default_cwd,omitempty"`
	SuggestedCwds []string            `json:"suggested_cwds,omitempty"`
	Profiles      []RemoteProfileItem `json:"profiles"`
}

// RemoteLaunchPlan holds the resolved tmux session name, cwd, and shell command for a mobile-triggered agy launch.
type RemoteLaunchPlan struct {
	Profile      *Profile
	AccountLabel string
	SessionName  string
	Cwd          string
	ShellCommand string
	NotifyLabel  string
}

// FindAgyBinary locates the Antigravity CLI binary across PATH and common macOS install directories.
func FindAgyBinary() string {
	if p, err := exec.LookPath("agy"); err == nil && p != "" {
		return p
	}
	home, _ := os.UserHomeDir()
	for _, cand := range []string{
		filepath.Join(home, ".local", "bin", "agy"),
		filepath.Join(home, ".gemini", "antigravity-cli", "bin", "agy"),
		"/opt/homebrew/bin/agy",
		"/usr/local/bin/agy",
	} {
		if fi, err := os.Stat(cand); err == nil && !fi.IsDir() {
			return cand
		}
	}
	return ""
}

// SaveOptions configures optional overrides when saving a profile.
type SaveOptions struct {
	Email       string
	CustomTTLMs *int64 // nil = keep existing (or auto); >0 = custom TTL ms; <0 = disable reminder; 0 = reset to auto
}

// ParseDurationExtended parses human-friendly TTL strings like "7d", "14d", "2w", "1d12h", "24h", "off", "auto".
// Returns (ttlMs, error) where:
//   - ttlMs > 0: explicit custom TTL in milliseconds
//   - ttlMs == -1: explicitly disable expiry reminder ("off" / "none" / "0")
//   - ttlMs == 0: reset to automatic detection ("auto")
func ParseDurationExtended(raw string) (int64, error) {
	s := strings.ToLower(strings.TrimSpace(raw))
	switch s {
	case "", "auto":
		return 0, nil
	case "off", "none", "disable", "disabled", "0":
		return -1, nil
	}
	// Expand shorthand `w` (weeks) and `d` (days) into hours so time.ParseDuration handles combinations like `7d12h`.
	var total time.Duration
	rest := s
	for rest != "" {
		i := 0
		for i < len(rest) && ((rest[i] >= '0' && rest[i] <= '9') || rest[i] == '.') {
			i++
		}
		if i == 0 || i >= len(rest) {
			return 0, fmt.Errorf("无效时长格式 %q（示例: 7d、14d、30d、24h、1d12h、off、auto）", raw)
		}
		num, err := strconv.ParseFloat(rest[:i], 64)
		if err != nil || num <= 0 {
			return 0, fmt.Errorf("无效时长数值 %q", raw)
		}
		j := i
		for j < len(rest) && (rest[j] < '0' || rest[j] > '9') && rest[j] != '.' {
			j++
		}
		unit := rest[i:j]
		rest = rest[j:]
		switch unit {
		case "w", "wk", "week", "weeks":
			total += time.Duration(num * float64(7*24*time.Hour))
		case "d", "day", "days":
			total += time.Duration(num * float64(24*time.Hour))
		case "h", "hr", "hrs", "hour", "hours":
			total += time.Duration(num * float64(time.Hour))
		case "m", "min", "mins", "minute", "minutes":
			total += time.Duration(num * float64(time.Minute))
		case "s", "sec", "secs", "second", "seconds":
			total += time.Duration(num * float64(time.Second))
		default:
			return 0, fmt.Errorf("不支持的时长单位 %q（支持 w/d/h/m/s）", unit)
		}
	}
	if total <= 0 {
		return 0, fmt.Errorf("时长必须大于 0: %q", raw)
	}
	return total.Milliseconds(), nil
}

// ComputeExpiryState evaluates the profile's expiration / re-login reminder state at nowMs.
func ComputeExpiryState(p *Profile, nowMs int64) ExpiryState {
	if p == nil {
		return ExpiryState{}
	}
	var expAt int64
	var src string
	var hard bool
	var warnWindowMs int64 = 24 * 3600 * 1000 // default 24h warning window

	switch {
	case p.CustomTTLMs < 0:
		return ExpiryState{Summary: "提醒已关闭"}
	case p.CustomTTLMs > 0 && p.SavedAt > 0:
		expAt = p.SavedAt + p.CustomTTLMs
		src = "custom_ttl"
		hard = true
		if p.CustomTTLMs <= 6*3600*1000 {
			warnWindowMs = p.CustomTTLMs / 4
			if warnWindowMs < 15*60*1000 {
				warnWindowMs = 15 * 60 * 1000
			}
		}
	case p.RefreshExpiresAt > 0:
		expAt = p.RefreshExpiresAt
		src = "refresh_token"
		hard = true
	case !p.HasRefreshToken && p.TokenExpiresAt > 0:
		expAt = p.TokenExpiresAt
		src = "access_token"
		hard = true
		warnWindowMs = 15 * 60 * 1000
	case p.SavedAt > 0:
		expAt = p.SavedAt + int64(DefaultRefreshReminderDays)*24*3600*1000
		src = "refresh_reminder"
		hard = false
	default:
		return ExpiryState{}
	}

	diffMs := expAt - nowMs
	st := ExpiryState{
		ExpiresAt:  expAt,
		Source:     src,
		HardExpiry: hard,
	}

	clock := formatExpiryClock(expAt, nowMs)
	if diffMs <= 0 {
		st.Expired = true
		ago := formatShortDuration(-diffMs)
		if hard {
			st.Summary = fmt.Sprintf("已过期 %s (%s)", ago, clock)
			st.Warning = fmt.Sprintf("登录凭据已于 %s 过期（已过 %s），请先运行 `ai-audit agy login %s` 重新登录更新", clock, ago, p.Name)
		} else {
			st.Summary = fmt.Sprintf("建议重登（已存超 %d 天）", DefaultRefreshReminderDays)
			st.Warning = fmt.Sprintf("距上次保存凭据已超过 %d 天（%s），建议先运行 `ai-audit agy login %s` 重新登录刷新凭据", DefaultRefreshReminderDays, clock, p.Name)
		}
		return st
	}

	left := formatShortDuration(diffMs)
	if diffMs <= warnWindowMs {
		st.ExpiringSoon = true
		if hard {
			st.Summary = fmt.Sprintf("即将过期 · 剩 %s (%s)", left, clock)
			st.Warning = fmt.Sprintf("登录凭据将于 %s 到期（剩余 %s），建议尽快运行 `ai-audit agy login %s` 重新登录", clock, left, p.Name)
		} else {
			st.Summary = fmt.Sprintf("建议尽快续登 · 剩 %s (%s)", left, clock)
			st.Warning = fmt.Sprintf("凭据将于 %s 达到 %d 天建议续登周期（剩余 %s），建议提前重登更新", clock, DefaultRefreshReminderDays, left)
		}
		return st
	}

	if hard {
		st.Summary = fmt.Sprintf("有效 · 剩 %s (%s到期)", left, clock)
	} else {
		st.Summary = fmt.Sprintf("有效(含Refresh) · %s提醒续登", clock)
	}
	return st
}

func (p *Profile) refreshComputedExpiry(nowMs int64) {
	st := ComputeExpiryState(p, nowMs)
	p.RemindAt = st.ExpiresAt
	p.ExpireSource = st.Source
}

func (m *Manager) readActiveSlot() (*ActiveSlotInfo, error) {
	candidates := [][2]string{
		{m.agyService, m.agyAccount},
	}
	if m.agyService == DefaultAgyService && m.agyAccount == DefaultAgyAccount {
		candidates = append(candidates, [2]string{DefaultAgyService, "agy"})
	}
	nowMs := m.now().UnixMilli()
	var lastErr error
	for _, c := range candidates {
		raw, err := m.store.Get(c[0], c[1])
		if err == nil && len(raw) > 0 {
			meta := inspectCredentialBytes(raw, nowMs)
			return &ActiveSlotInfo{
				Present:     true,
				Service:     c[0],
				Account:     c[1],
				Fingerprint: fingerprintBytes(raw),
				Meta:        meta,
				EmailHint:   meta.Email,
				RawBytes:    raw,
			}, nil
		}
		if err != nil && !errors.Is(err, ErrNotFound) {
			lastErr = err
		}
	}
	if lastErr != nil {
		return nil, lastErr
	}
	return &ActiveSlotInfo{Present: false, Service: m.agyService, Account: m.agyAccount}, nil
}

// SaveCurrent snapshots the currently active Antigravity Keychain credential into the Keychain vault.
func (m *Manager) SaveCurrent(alias, emailOverride string) (*Profile, error) {
	return m.SaveCurrentWithOptions(alias, SaveOptions{Email: emailOverride})
}

// SaveCurrentWithOptions snapshots the active Keychain credential with optional email & TTL overrides.
func (m *Manager) SaveCurrentWithOptions(alias string, opts SaveOptions) (*Profile, error) {
	unlock, err := m.acquireLock()
	if err != nil {
		return nil, err
	}
	defer unlock()
	return m.saveCurrentLocked(alias, opts)
}

func (m *Manager) saveCurrentLocked(alias string, opts SaveOptions) (*Profile, error) {
	active, err := m.readActiveSlot()
	if err != nil {
		return nil, err
	}
	if !active.Present {
		return nil, fmt.Errorf("未在 macOS Keychain 中找到当前已登录的 agy 凭据（%s/%s），请先运行 agy 完成登录", m.agyService, m.agyAccount)
	}

	email := strings.TrimSpace(opts.Email)
	if email == "" {
		email = active.EmailHint
	}
	if email == "" {
		email = m.inferRecentQuotaEmail()
	}

	name := sanitizeProfileName(alias)
	if name == "" && email != "" {
		if at := strings.Index(email, "@"); at > 0 {
			name = sanitizeProfileName(email[:at])
		} else {
			name = sanitizeProfileName(email)
		}
	}
	if name == "" {
		return nil, errors.New("请提供账号别名，例如: ai-audit agy save work --email you@example.com")
	}
	if email == "" {
		if aq := matchQuotaAccount(m.loadAgyQuotas(), "", name); aq != nil && aq.Email != "" {
			email = aq.Email
		}
	}

	if err := m.store.Set(m.vaultService, name, active.RawBytes, nil); err != nil {
		return nil, fmt.Errorf("保存凭据到 Keychain 失败: %w", err)
	}

	pf, err := m.loadProfilesLocked()
	if err != nil {
		return nil, err
	}
	nowMs := m.now().UnixMilli()
	var updated *Profile
	for _, p := range pf.Profiles {
		if strings.EqualFold(p.Name, name) {
			p.Name = name
			if email != "" {
				p.Email = email
			}
			p.Fingerprint = active.Fingerprint
			p.KeyService = active.Service
			p.KeyAccount = active.Account
			p.SavedAt = nowMs
			p.HasRefreshToken = active.Meta.HasRefreshToken
			p.TokenExpiresAt = active.Meta.TokenExpiresAt
			p.RefreshExpiresAt = active.Meta.RefreshExpiresAt
			if opts.CustomTTLMs != nil {
				p.CustomTTLMs = *opts.CustomTTLMs
			}
			p.refreshComputedExpiry(nowMs)
			updated = p
			break
		}
	}
	if updated == nil {
		var customTTL int64
		if opts.CustomTTLMs != nil {
			customTTL = *opts.CustomTTLMs
		}
		updated = &Profile{
			Name:             name,
			Email:            email,
			Fingerprint:      active.Fingerprint,
			KeyService:       active.Service,
			KeyAccount:       active.Account,
			SavedAt:          nowMs,
			HasRefreshToken:  active.Meta.HasRefreshToken,
			TokenExpiresAt:   active.Meta.TokenExpiresAt,
			RefreshExpiresAt: active.Meta.RefreshExpiresAt,
			CustomTTLMs:      customTTL,
		}
		updated.refreshComputedExpiry(nowMs)
		pf.Profiles = append(pf.Profiles, updated)
	}
	sort.Slice(pf.Profiles, func(i, j int) bool {
		return strings.ToLower(pf.Profiles[i].Name) < strings.ToLower(pf.Profiles[j].Name)
	})
	if err := m.saveProfilesLocked(pf); err != nil {
		return nil, err
	}
	cp := *updated
	return &cp, nil
}

// SetTTL updates a saved profile's custom expiration / re-login reminder TTL.
func (m *Manager) SetTTL(query string, ttlMs int64) (*Profile, ExpiryState, error) {
	unlock, err := m.acquireLock()
	if err != nil {
		return nil, ExpiryState{}, err
	}
	defer unlock()

	pf, err := m.loadProfilesLocked()
	if err != nil {
		return nil, ExpiryState{}, err
	}
	target, err := resolveProfile(pf.Profiles, query)
	if err != nil {
		return nil, ExpiryState{}, err
	}
	nowMs := m.now().UnixMilli()
	target.CustomTTLMs = ttlMs
	target.refreshComputedExpiry(nowMs)
	if err := m.saveProfilesLocked(pf); err != nil {
		return nil, ExpiryState{}, err
	}
	cp := *target
	return &cp, ComputeExpiryState(&cp, nowMs), nil
}

// InspectProfile resolves query and returns its metadata + current expiry state without switching Keychain.
func (m *Manager) InspectProfile(query string) (*Profile, ExpiryState, error) {
	pf, err := m.loadProfilesLocked()
	if err != nil {
		return nil, ExpiryState{}, err
	}
	target, err := resolveProfile(pf.Profiles, query)
	if err != nil {
		return nil, ExpiryState{}, err
	}
	cp := *target
	return &cp, ComputeExpiryState(&cp, m.now().UnixMilli()), nil
}

// List returns all saved profiles enriched with live active marker, expiry state, and official quota summary.
func (m *Manager) List() ([]ProfileStatus, *ActiveSlotInfo, error) {
	pf, err := m.loadProfilesLocked()
	if err != nil {
		return nil, nil, err
	}
	active, _ := m.readActiveSlot()
	quotas := m.loadAgyQuotas()
	nowMs := m.now().UnixMilli()

	out := make([]ProfileStatus, 0, len(pf.Profiles))
	for _, p := range pf.Profiles {
		isActive := active != nil && active.Present &&
			(active.Fingerprint == p.Fingerprint ||
				(active.EmailHint != "" && p.Email != "" && strings.EqualFold(active.EmailHint, p.Email)))
		st := ProfileStatus{
			Profile: *p,
			Active:  isActive,
			Expiry:  ComputeExpiryState(p, nowMs),
		}
		if aq := matchQuotaAccount(quotas, p.Email, p.Name); aq != nil {
			st.PlanTier = aq.PlanTier
			if st.Email == "" && aq.Email != "" {
				st.Email = aq.Email
			}
			st.Quota5h, st.QuotaWeekly = formatAccountQuotaBrief(aq)
		}
		out = append(out, st)
	}
	return out, active, nil
}

// SnapshotForRemote builds a zero-secret profile summary for the mobile app's new-terminal account picker.
func (m *Manager) SnapshotForRemote(defaultCwd string, suggestedCwds []string) (*RemoteProfilesSnapshot, error) {
	list, active, err := m.List()
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(defaultCwd) == "" {
		defaultCwd, _ = os.UserHomeDir()
	}

	items := make([]RemoteProfileItem, 0, len(list))
	activeProfile := ""
	for _, st := range list {
		if st.Active && activeProfile == "" {
			activeProfile = st.Name
		}
		items = append(items, RemoteProfileItem{
			Name:          st.Name,
			Email:         st.Email,
			Active:        st.Active,
			PlanTier:      st.PlanTier,
			Quota5h:       st.Quota5h,
			QuotaWeekly:   st.QuotaWeekly,
			SavedAt:       st.SavedAt,
			LastUsedAt:    st.LastUsedAt,
			Expired:       st.Expiry.Expired,
			ExpiringSoon:  st.Expiry.ExpiringSoon,
			HardExpiry:    st.Expiry.HardExpiry,
			ExpireSummary: st.Expiry.Summary,
			ExpireWarning: st.Expiry.Warning,
		})
	}

	activePresent := active != nil && active.Present
	activeEmail := ""
	if active != nil {
		activeEmail = active.EmailHint
	}

	cleanedCwds := make([]string, 0, len(suggestedCwds))
	seenCwd := make(map[string]bool)
	for _, c := range suggestedCwds {
		c = strings.TrimSpace(c)
		if c == "" || seenCwd[c] {
			continue
		}
		if fi, serr := os.Stat(c); serr == nil && fi.IsDir() {
			seenCwd[c] = true
			cleanedCwds = append(cleanedCwds, c)
		}
	}

	return &RemoteProfilesSnapshot{
		AgyInstalled:  FindAgyBinary() != "",
		ActivePresent: activePresent,
		ActiveEmail:   activeEmail,
		ActiveProfile: activeProfile,
		ActiveUnsaved: activePresent && activeProfile == "",
		DefaultCwd:    defaultCwd,
		SuggestedCwds: cleanedCwds,
		Profiles:      items,
	}, nil
}

// PrepareRemoteLaunch switches the global Keychain slot (when accountQuery != "") under the agyauth flock
// and returns the tmux session name, resolved cwd, and an interactive shell bootstrap command that loads
// the account & proxy environment without directly launching `agy`.
// The caller MUST invoke unlock() after the terminal session is created.
func (m *Manager) PrepareRemoteLaunch(accountQuery, cwd, requestedTarget string) (*RemoteLaunchPlan, func(), error) {
	resolvedCwd, err := resolveLaunchCwd(cwd)
	if err != nil {
		return nil, nil, err
	}

	unlock, err := m.acquireLock()
	if err != nil {
		return nil, nil, err
	}

	var prof *Profile
	alias := "default"
	email := ""
	switched := false
	activePresent := false
	query := strings.TrimSpace(accountQuery)
	if query != "" {
		p, serr := m.switchLocked(query)
		if serr != nil {
			unlock()
			return nil, nil, serr
		}
		prof = p
		alias = p.Name
		email = p.Email
		switched = true
		activePresent = true
	} else {
		// Keep the currently active Keychain login untouched, but derive a friendly alias/email if available.
		if active, aerr := m.readActiveSlot(); aerr == nil && active != nil && active.Present {
			activePresent = true
			email = active.EmailHint
			if pf, lerr := m.loadProfilesLocked(); lerr == nil {
				for _, p := range pf.Profiles {
					if active.Fingerprint == p.Fingerprint ||
						(active.EmailHint != "" && p.Email != "" && strings.EqualFold(active.EmailHint, p.Email)) {
						cp := *p
						prof = &cp
						alias = p.Name
						if p.Email != "" {
							email = p.Email
						}
						break
					}
				}
			}
		}
	}
	if email == "" && prof != nil {
		if aq := matchQuotaAccount(m.loadAgyQuotas(), prof.Email, prof.Name); aq != nil && aq.Email != "" {
			email = aq.Email
		}
	}

	sessName := ""
	if tmuxBin := term.FindTmux(); tmuxBin != "" {
		if reqSan := sanitizeProfileName(requestedTarget); reqSan != "" && !tmuxHasSession(tmuxBin, reqSan) {
			sessName = reqSan
		} else {
			sessName = pickSessionName(tmuxBin, alias, resolvedCwd, false)
		}
	}

	agyBin := FindAgyBinary()
	shellCmd := buildAgyEnvShellCommand(agyBin, alias, email, switched, activePresent)
	notifyLabel := "agy 环境: " + alias
	if sessName != "" {
		notifyLabel = fmt.Sprintf("agy 环境 (%s): %s", alias, sessName)
	}

	return &RemoteLaunchPlan{
		Profile:      prof,
		AccountLabel: alias,
		SessionName:  sessName,
		Cwd:          resolvedCwd,
		ShellCommand: shellCmd,
		NotifyLabel:  notifyLabel,
	}, unlock, nil
}

func resolveLaunchCwd(raw string) (string, error) {
	home, _ := os.UserHomeDir()
	cwd := strings.TrimSpace(raw)
	switch {
	case cwd == "" || cwd == "~":
		cwd = home
	case strings.HasPrefix(cwd, "~/"):
		cwd = filepath.Join(home, cwd[2:])
	}
	if abs, err := filepath.Abs(cwd); err == nil {
		cwd = abs
	}
	fi, err := os.Stat(cwd)
	if err != nil {
		return "", fmt.Errorf("工作目录不存在: %s", cwd)
	}
	if !fi.IsDir() {
		return "", fmt.Errorf("工作目录不是文件夹: %s", cwd)
	}
	return cwd, nil
}

// syncActiveBeforeSwitchLocked checks if the live Keychain slot holds a fresher refreshed token
// for an existing saved profile (matched by email) and silently refreshes that profile's vault backup
// so switching away never loses an updated access/refresh token.
func (m *Manager) syncActiveBeforeSwitchLocked(pf *profilesFile, targetName string, nowMs int64) {
	active, err := m.readActiveSlot()
	if err != nil || !active.Present || len(active.RawBytes) == 0 || active.EmailHint == "" {
		return
	}
	for _, p := range pf.Profiles {
		if p.Email == "" || !strings.EqualFold(p.Email, active.EmailHint) {
			continue
		}
		if p.Name == targetName {
			// If we're switching to the SAME profile that's already active with fresher bytes, refresh backup too.
			if p.Fingerprint != active.Fingerprint {
				if m.store.Set(m.vaultService, p.Name, active.RawBytes, nil) == nil {
					p.Fingerprint = active.Fingerprint
					p.SavedAt = nowMs
					p.HasRefreshToken = active.Meta.HasRefreshToken
					if active.Meta.TokenExpiresAt > 0 {
						p.TokenExpiresAt = active.Meta.TokenExpiresAt
					}
					if active.Meta.RefreshExpiresAt > 0 {
						p.RefreshExpiresAt = active.Meta.RefreshExpiresAt
					}
					p.refreshComputedExpiry(nowMs)
				}
			}
			return
		}
		if p.Fingerprint != active.Fingerprint {
			if m.store.Set(m.vaultService, p.Name, active.RawBytes, nil) == nil {
				p.Fingerprint = active.Fingerprint
				p.SavedAt = nowMs
				p.HasRefreshToken = active.Meta.HasRefreshToken
				if active.Meta.TokenExpiresAt > 0 {
					p.TokenExpiresAt = active.Meta.TokenExpiresAt
				}
				if active.Meta.RefreshExpiresAt > 0 {
					p.RefreshExpiresAt = active.Meta.RefreshExpiresAt
				}
				p.refreshComputedExpiry(nowMs)
			}
		}
		return
	}
}

// Switch writes the specified profile's credential from the Keychain vault into Antigravity's active slot.
func (m *Manager) Switch(query string) (*Profile, error) {
	unlock, err := m.acquireLock()
	if err != nil {
		return nil, err
	}
	defer unlock()
	return m.switchLocked(query)
}

func (m *Manager) switchLocked(query string) (*Profile, error) {
	pf, err := m.loadProfilesLocked()
	if err != nil {
		return nil, err
	}
	target, err := resolveProfile(pf.Profiles, query)
	if err != nil {
		return nil, err
	}

	nowMs := m.now().UnixMilli()
	// Silently sync back any refreshed live token for the outgoing profile before overwriting Keychain.
	m.syncActiveBeforeSwitchLocked(pf, target.Name, nowMs)

	secret, err := m.store.Get(m.vaultService, target.Name)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return nil, fmt.Errorf("Keychain 中未找到档案 %q 的凭据，请重新登录后执行 ai-audit agy save %s", target.Name, target.Name)
		}
		return nil, err
	}
	if len(secret) == 0 {
		return nil, fmt.Errorf("档案 %q 保存的凭据为空，请重新保存", target.Name)
	}

	svc := target.KeyService
	if svc == "" {
		svc = m.agyService
	}
	acct := target.KeyAccount
	if acct == "" {
		acct = m.agyAccount
	}

	if err := m.store.Set(svc, acct, secret, m.trustedApps); err != nil {
		return nil, fmt.Errorf("写入全局 agy Keychain (%s/%s) 失败: %w", svc, acct, err)
	}

	target.Fingerprint = fingerprintBytes(secret)
	target.LastUsedAt = nowMs
	target.refreshComputedExpiry(nowMs)
	_ = m.saveProfilesLocked(pf)

	cp := *target
	return &cp, nil
}

// Remove deletes a saved profile from metadata and from the Keychain vault (leaving the active agy login untouched).
func (m *Manager) Remove(query string) (*Profile, error) {
	unlock, err := m.acquireLock()
	if err != nil {
		return nil, err
	}
	defer unlock()

	pf, err := m.loadProfilesLocked()
	if err != nil {
		return nil, err
	}
	target, err := resolveProfile(pf.Profiles, query)
	if err != nil {
		return nil, err
	}
	if err := m.store.Delete(m.vaultService, target.Name); err != nil && !errors.Is(err, ErrNotFound) {
		return nil, err
	}
	kept := make([]*Profile, 0, len(pf.Profiles))
	for _, p := range pf.Profiles {
		if p.Name != target.Name {
			kept = append(kept, p)
		}
	}
	pf.Profiles = kept
	if err := m.saveProfilesLocked(pf); err != nil {
		return nil, err
	}
	cp := *target
	return &cp, nil
}

// HasProfile checks whether query uniquely matches any saved profile (used for `ai-audit agy <alias>` shorthand).
func (m *Manager) HasProfile(query string) bool {
	pf, err := m.loadProfilesLocked()
	if err != nil {
		return false
	}
	_, err = resolveProfile(pf.Profiles, query)
	return err == nil
}

// OpenOptions configures how `ai-audit agy open` launches the new agy terminal.
type OpenOptions struct {
	Cwd        string
	InPlace    bool
	Attach     bool
	HoldLockMs int // Wait briefly under lock after launching agy so back-to-back opens don't race on Keychain read
	AgyArgs    []string
}

// Open switches the global Antigravity Keychain slot to `query` and opens a fresh tmux-backed
// Terminal.app window (or runs in-place) running `agy`.
func (m *Manager) Open(query string, opts OpenOptions) (*Profile, string, error) {
	unlock, err := m.acquireLock()
	if err != nil {
		return nil, "", err
	}
	defer unlock()

	prof, err := m.switchLocked(query)
	if err != nil {
		return nil, "", err
	}

	cwd := strings.TrimSpace(opts.Cwd)
	if cwd == "" {
		cwd, _ = os.Getwd()
	}
	if abs, aerr := filepath.Abs(cwd); aerr == nil {
		cwd = abs
	}

	agyBin := FindAgyBinary()
	if agyBin == "" {
		return prof, "", errors.New("未在 PATH 中找到 agy 命令，请先安装 Antigravity CLI")
	}

	if opts.InPlace {
		unlock()
		return prof, "", runAgyInPlace(agyBin, cwd, opts.AgyArgs)
	}

	tmuxBin := term.FindTmux()
	if tmuxBin == "" {
		if err := openDirectTerminalWindow(agyBin, cwd, opts.AgyArgs); err != nil {
			return prof, "", err
		}
		waitForStartup(opts.HoldLockMs)
		return prof, "", nil
	}

	sessName := pickSessionName(tmuxBin, prof.Name, cwd, opts.Attach)
	existed := tmuxHasSession(tmuxBin, sessName)

	if !existed {
		shellCmd := buildAgyShellCommand(agyBin, opts.AgyArgs)
		args := tmuxBaseArgs()
		args = append(args, "new-session", "-d", "-s", sessName, "-c", cwd, "--", "/bin/zsh", "-lc", shellCmd)
		cmd := exec.Command(tmuxBin, args...)
		if out, err := cmd.CombinedOutput(); err != nil {
			return prof, "", fmt.Errorf("创建 tmux 会话 %s 失败: %w (%s)", sessName, err, strings.TrimSpace(string(out)))
		}
	}

	if err := term.OpenMacTerminalWindow(tmuxBin, sessName); err != nil {
		return prof, sessName, err
	}

	if !existed {
		waitForStartup(opts.HoldLockMs)
	}
	return prof, sessName, nil
}

// LoginNew safely clears the active Keychain slot (refusing if unsaved unless force=true),
// opens a new agy terminal for fresh Google OAuth sign-in, and polls Keychain to auto-save the new profile.
func (m *Manager) LoginNew(alias, cwd string, force bool, customTTLMs *int64, waitTimeout time.Duration, onStatus func(string)) (*Profile, string, error) {
	name := sanitizeProfileName(alias)
	if name == "" {
		return nil, "", errors.New("请指定新账号别名，例如: ai-audit agy login work")
	}

	unlock, err := m.acquireLock()
	if err != nil {
		return nil, "", err
	}

	pf, err := m.loadProfilesLocked()
	if err != nil {
		unlock()
		return nil, "", err
	}
	active, err := m.readActiveSlot()
	if err != nil {
		unlock()
		return nil, "", err
	}

	prevFP := ""
	targetSvc, targetAcct := m.agyService, m.agyAccount
	if active.Present {
		prevFP = active.Fingerprint
		targetSvc = active.Service
		targetAcct = active.Account
		saved := false
		for _, p := range pf.Profiles {
			if p.Fingerprint == active.Fingerprint ||
				(active.EmailHint != "" && p.Email != "" && strings.EqualFold(active.EmailHint, p.Email)) {
				saved = true
				break
			}
		}
		if !saved && !force {
			unlock()
			return nil, "", fmt.Errorf("当前已登录的 agy 账号尚未保存到档案中！请先运行 `ai-audit agy save <当前账号别名>` 备份，或加 `--force` 强制清空后登录新号")
		}
		if err := m.store.Delete(targetSvc, targetAcct); err != nil && !errors.Is(err, ErrNotFound) {
			unlock()
			return nil, "", fmt.Errorf("清空当前 Keychain 登录槽位失败: %w", err)
		}
	}
	unlock()

	agyBin := FindAgyBinary()
	if agyBin == "" {
		return nil, "", errors.New("未在 PATH 中找到 agy 命令，请先安装 Antigravity CLI")
	}
	if strings.TrimSpace(cwd) == "" {
		cwd, _ = os.Getwd()
	}

	sessName := ""
	if tmuxBin := term.FindTmux(); tmuxBin != "" {
		sessName = pickSessionName(tmuxBin, name, cwd, false)
		shellCmd := buildAgyShellCommand(agyBin, nil)
		args := tmuxBaseArgs()
		args = append(args, "new-session", "-d", "-s", sessName, "-c", cwd, "--", "/bin/zsh", "-lc", shellCmd)
		if out, err := exec.Command(tmuxBin, args...).CombinedOutput(); err != nil {
			return nil, "", fmt.Errorf("创建登录终端失败: %w (%s)", err, strings.TrimSpace(string(out)))
		}
		if err := term.OpenMacTerminalWindow(tmuxBin, sessName); err != nil {
			return nil, sessName, err
		}
	} else {
		if err := openDirectTerminalWindow(agyBin, cwd, nil); err != nil {
			return nil, "", err
		}
	}

	if onStatus != nil {
		onStatus(fmt.Sprintf("已弹出新终端（%s），请在浏览器中完成新账号登录；检测到登录成功后将自动保存为档案 %q ...", sessName, name))
	}

	if waitTimeout <= 0 {
		waitTimeout = 3 * time.Minute
	}
	deadline := time.Now().Add(waitTimeout)
	for time.Now().Before(deadline) {
		time.Sleep(1 * time.Second)
		cur, rerr := m.readActiveSlot()
		if rerr == nil && cur.Present && cur.Fingerprint != "" && cur.Fingerprint != prevFP {
			time.Sleep(1200 * time.Millisecond)
			prof, serr := m.SaveCurrentWithOptions(name, SaveOptions{CustomTTLMs: customTTLMs})
			if serr != nil {
				return nil, sessName, serr
			}
			return prof, sessName, nil
		}
	}

	return nil, sessName, fmt.Errorf("等待新账号登录超时（%s）；登录完成后可手动执行 `ai-audit agy save %s` 保存档案", waitTimeout, name)
}

func waitForStartup(holdMs int) {
	if holdMs == 0 {
		holdMs = 1500
	}
	if holdMs > 0 {
		time.Sleep(time.Duration(holdMs) * time.Millisecond)
	}
}

func buildAgyShellCommand(agyBin string, extraArgs []string) string {
	var parts []string
	parts = append(parts, shellQuote(agyBin))
	for _, a := range extraArgs {
		parts = append(parts, shellQuote(a))
	}
	callerProxy := collectCallerProxyEnv(os.Getenv)
	fbHTTP, fbHTTPS, fbAll := "", "", ""
	if !hasActiveProxyEnv(callerProxy) {
		fbHTTP, fbHTTPS, fbAll = detectFallbackProxy()
	}
	proxySetup := buildProxyShellExports(callerProxy, fbHTTP, fbHTTPS, fbAll)
	return fmt.Sprintf(
		`export TERM=xterm-256color; %s; %s; __rc=$?; if [[ $__rc -ne 0 && $__rc -ne 130 ]]; then echo ""; echo "[agy 异常退出 (code=$__rc)，可查看上方报错后输入 exit 关闭]"; exec /bin/zsh -l; fi`,
		proxySetup, strings.Join(parts, " "),
	)
}

func buildAgyEnvBanner(alias, email string, switched, activePresent bool) string {
	accountDesc := strings.TrimSpace(alias)
	em := strings.TrimSpace(email)
	switch {
	case accountDesc != "" && accountDesc != "default" && em != "":
		accountDesc = fmt.Sprintf("%s (%s)", accountDesc, em)
	case (accountDesc == "" || accountDesc == "default") && em != "":
		accountDesc = em
	case accountDesc == "":
		accountDesc = "default"
	}

	if switched {
		return fmt.Sprintf("[agy 账号环境已加载] %s（已切换全局 Keychain，输入 agy 即可启动）", accountDesc)
	}
	if activePresent {
		return fmt.Sprintf("[agy 账号环境已加载] 当前登录: %s（未切换 Keychain，输入 agy 即可启动）", accountDesc)
	}
	return "[agy 环境已加载] 当前未检测到已登录凭据，可输入 agy 完成登录"
}

// buildAgyEnvShellCommand prepares the shell environment for a selected Antigravity account
// (PATH, account markers, proxy variables, status banner) and drops into an interactive login zsh
// WITHOUT directly launching `agy`.
func buildAgyEnvShellCommand(agyBin, alias, email string, switched, activePresent bool) string {
	stmts := []string{`export TERM=xterm-256color`}

	if agyBin != "" {
		if agyDir := strings.TrimSpace(filepath.Dir(agyBin)); agyDir != "" && agyDir != "." {
			stmts = append(stmts, fmt.Sprintf(`case ":$PATH:" in *:%s:*) ;; *) export PATH=%s:"$PATH" ;; esac`, shellQuote(agyDir), shellQuote(agyDir)))
		}
	}
	if alias := strings.TrimSpace(alias); alias != "" {
		stmts = append(stmts, fmt.Sprintf("export AI_AUDIT_AGY_ACCOUNT=%s", shellQuote(alias)))
	}
	if email := strings.TrimSpace(email); email != "" {
		stmts = append(stmts, fmt.Sprintf("export AI_AUDIT_AGY_EMAIL=%s", shellQuote(email)))
	}

	callerProxy := collectCallerProxyEnv(os.Getenv)
	fbHTTP, fbHTTPS, fbAll := "", "", ""
	if !hasActiveProxyEnv(callerProxy) {
		fbHTTP, fbHTTPS, fbAll = detectFallbackProxy()
	}
	if proxySetup := buildInteractiveProxyShellExports(callerProxy, fbHTTP, fbHTTPS, fbAll); proxySetup != "" {
		stmts = append(stmts, proxySetup)
	}

	banner := buildAgyEnvBanner(alias, email, switched, activePresent)
	stmts = append(stmts, fmt.Sprintf("printf '%%s\\n' %s", shellQuote(banner)))
	stmts = append(stmts, `exec /bin/zsh -l`)
	return strings.Join(stmts, "; ")
}

func runAgyInPlace(agyBin, cwd string, extraArgs []string) error {
	cmd := exec.Command(agyBin, extraArgs...)
	cmd.Dir = cwd
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func openDirectTerminalWindow(agyBin, cwd string, extraArgs []string) error {
	var parts []string
	parts = append(parts, shellQuote(agyBin))
	for _, a := range extraArgs {
		parts = append(parts, shellQuote(a))
	}
	callerProxy := collectCallerProxyEnv(os.Getenv)
	fbHTTP, fbHTTPS, fbAll := "", "", ""
	if !hasActiveProxyEnv(callerProxy) {
		fbHTTP, fbHTTPS, fbAll = detectFallbackProxy()
	}
	proxySetup := buildProxyShellExports(callerProxy, fbHTTP, fbHTTPS, fbAll)
	scriptPath := filepath.Join(os.TempDir(), fmt.Sprintf("ai-audit-agy-%d.command", time.Now().UnixNano()))
	content := fmt.Sprintf("#!/bin/zsh\nrm -f %s\ncd %s || exit 1\nexport TERM=xterm-256color\n%s\nexec %s\n",
		shellQuote(scriptPath), shellQuote(cwd), proxySetup, strings.Join(parts, " "))
	if err := os.WriteFile(scriptPath, []byte(content), 0o700); err != nil {
		return err
	}
	return exec.Command("/usr/bin/open", "-a", "Terminal", scriptPath).Run()
}

func tmuxBaseArgs() []string {
	if sock := term.FindTmuxSocket(); sock != "" {
		return []string{"-S", sock}
	}
	return nil
}

func tmuxHasSession(tmuxBin, name string) bool {
	args := append(tmuxBaseArgs(), "has-session", "-t", name)
	return exec.Command(tmuxBin, args...).Run() == nil
}

func pickSessionName(tmuxBin, alias, cwd string, reuseExisting bool) string {
	dirPart := sanitizeProfileName(filepath.Base(cwd))
	if dirPart == "" {
		dirPart = "home"
	}
	base := fmt.Sprintf("agy-%s-%s", sanitizeProfileName(alias), dirPart)
	if reuseExisting || !tmuxHasSession(tmuxBin, base) {
		return base
	}
	for i := 2; i <= 99; i++ {
		cand := fmt.Sprintf("%s-%d", base, i)
		if !tmuxHasSession(tmuxBin, cand) {
			return cand
		}
	}
	return fmt.Sprintf("%s-%d", base, time.Now().Unix()%10000)
}

func shellQuote(s string) string {
	if s == "" {
		return "''"
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

func sanitizeProfileName(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	var b strings.Builder
	prevSep := false
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '_' || r == '-' || r == '.' {
			b.WriteRune(r)
			prevSep = false
		} else if !prevSep && b.Len() > 0 {
			b.WriteByte('-')
			prevSep = true
		}
	}
	return strings.Trim(b.String(), "-_.")
}

func resolveProfile(profiles []*Profile, query string) (*Profile, error) {
	q := strings.ToLower(strings.TrimSpace(query))
	if q == "" {
		return nil, errors.New("请指定账号别名或邮箱")
	}
	if len(profiles) == 0 {
		return nil, errors.New("当前尚未保存任何 agy 账号档案，请先执行 `ai-audit agy save <别名>`")
	}
	for _, p := range profiles {
		if strings.ToLower(p.Name) == q || (p.Email != "" && strings.ToLower(p.Email) == q) {
			return p, nil
		}
	}
	var matches []*Profile
	for _, p := range profiles {
		if strings.HasPrefix(strings.ToLower(p.Name), q) || (p.Email != "" && strings.HasPrefix(strings.ToLower(p.Email), q)) {
			matches = append(matches, p)
		}
	}
	if len(matches) == 1 {
		return matches[0], nil
	}
	if len(matches) > 1 {
		names := make([]string, 0, len(matches))
		for _, p := range matches {
			names = append(names, p.Name)
		}
		return nil, fmt.Errorf("匹配到多个账号档案（%s），请输入更完整的别名或邮箱", strings.Join(names, ", "))
	}
	return nil, fmt.Errorf("未找到账号档案 %q，可先运行 `ai-audit agy ls` 查看已保存列表", query)
}

func (m *Manager) acquireLock() (func(), error) {
	if err := os.MkdirAll(filepath.Dir(m.profilesPath), 0o700); err != nil {
		return nil, err
	}
	lockPath := m.profilesPath + ".lock"
	f, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		_ = f.Close()
		return nil, err
	}
	released := false
	return func() {
		if released {
			return
		}
		released = true
		_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		_ = f.Close()
	}, nil
}

func (m *Manager) loadProfilesLocked() (*profilesFile, error) {
	pf := &profilesFile{Version: profilesVersion}
	b, err := os.ReadFile(m.profilesPath)
	if err != nil {
		if os.IsNotExist(err) {
			return pf, nil
		}
		return nil, err
	}
	if err := json.Unmarshal(b, pf); err != nil {
		return nil, fmt.Errorf("解析 %s 失败: %w", m.profilesPath, err)
	}
	if pf.Version == 0 {
		pf.Version = profilesVersion
	}
	return pf, nil
}

func (m *Manager) saveProfilesLocked(pf *profilesFile) error {
	if err := os.MkdirAll(filepath.Dir(m.profilesPath), 0o700); err != nil {
		return err
	}
	pf.Version = profilesVersion
	b, err := json.MarshalIndent(pf, "", "  ")
	if err != nil {
		return err
	}
	b = append(b, '\n')
	tmp, err := os.CreateTemp(filepath.Dir(m.profilesPath), ".agy-profiles-*.tmp")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	if _, err := tmp.Write(b); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpPath)
		return err
	}
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpPath)
		return err
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return err
	}
	return os.Rename(tmpPath, m.profilesPath)
}

func fingerprintBytes(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:8])
}

// inspectCredentialBytes best-effort extracts non-secret email & expiry metadata from
// JSON or `go-keyring-base64:` encoded OAuth payloads (including JWT id_token claims).
func inspectCredentialBytes(raw []byte, nowMs int64) CredentialMeta {
	data := []byte(strings.TrimSpace(string(raw)))
	if len(data) == 0 {
		return CredentialMeta{}
	}
	const b64Prefix = "go-keyring-base64:"
	if strings.HasPrefix(string(data), b64Prefix) {
		enc := strings.TrimSpace(strings.TrimPrefix(string(data), b64Prefix))
		if decoded, err := base64.StdEncoding.DecodeString(enc); err == nil {
			data = []byte(strings.TrimSpace(string(decoded)))
		}
	}
	var obj any
	if json.Unmarshal(data, &obj) != nil {
		return CredentialMeta{}
	}
	var meta CredentialMeta
	inspectJSONObj(obj, 0, nowMs, &meta)
	return meta
}

func inspectJSONObj(v any, depth int, nowMs int64, meta *CredentialMeta) {
	if depth > 6 || v == nil {
		return
	}
	switch x := v.(type) {
	case map[string]any:
		for k, val := range x {
			lk := strings.ToLower(strings.TrimSpace(k))
			switch lk {
			case "email", "user_email", "account_email", "useremail", "accountemail":
				if meta.Email == "" {
					if s, ok := val.(string); ok && strings.Contains(s, "@") {
						meta.Email = strings.TrimSpace(s)
					}
				}
			case "refresh_token", "refreshtoken":
				if s, ok := val.(string); ok && strings.TrimSpace(s) != "" {
					meta.HasRefreshToken = true
				}
			case "id_token", "idtoken":
				if s, ok := val.(string); ok {
					em, exp := inspectJWTClaims(s)
					if meta.Email == "" && em != "" {
						meta.Email = em
					}
					if meta.TokenExpiresAt == 0 && exp > 0 {
						meta.TokenExpiresAt = exp
					}
				}
			case "access_token", "accesstoken":
				if s, ok := val.(string); ok {
					em, exp := inspectJWTClaims(s)
					if meta.Email == "" && em != "" {
						meta.Email = em
					}
					if meta.TokenExpiresAt == 0 && exp > 0 {
						meta.TokenExpiresAt = exp
					}
				}
			case "expiry", "expiry_date", "expirydate", "expires_at", "expiresat", "token_expiry", "access_token_expires_at", "exp":
				if ts := parseTimestampAny(val); ts > 0 && (meta.TokenExpiresAt == 0 || ts > meta.TokenExpiresAt) {
					meta.TokenExpiresAt = ts
				}
			case "refresh_token_expires_at", "refreshtokenexpiresat", "refresh_expires_at", "refreshexpiresat", "refresh_token_expiry", "session_expires_at", "session_expiry", "reauth_at":
				if ts := parseTimestampAny(val); ts > 0 && (meta.RefreshExpiresAt == 0 || ts > meta.RefreshExpiresAt) {
					meta.RefreshExpiresAt = ts
				}
			case "refresh_token_expires_in", "refreshtokenexpiresin":
				if sec := parseDurationSecAny(val); sec > 0 && nowMs > 0 {
					ts := nowMs + sec*1000
					if meta.RefreshExpiresAt == 0 || ts > meta.RefreshExpiresAt {
						meta.RefreshExpiresAt = ts
					}
				}
			}
			inspectJSONObj(val, depth+1, nowMs, meta)
		}
	case []any:
		for _, item := range x {
			inspectJSONObj(item, depth+1, nowMs, meta)
		}
	}
}

func inspectJWTClaims(tok string) (email string, expMs int64) {
	parts := strings.Split(strings.TrimSpace(tok), ".")
	if len(parts) != 3 {
		return "", 0
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return "", 0
	}
	var claims struct {
		Email string `json:"email"`
		Exp   int64  `json:"exp"`
	}
	if json.Unmarshal(payload, &claims) != nil {
		return "", 0
	}
	if strings.Contains(claims.Email, "@") {
		email = strings.TrimSpace(claims.Email)
	}
	if claims.Exp > 1e9 {
		expMs = claims.Exp * 1000
	}
	return email, expMs
}

func parseTimestampAny(v any) int64 {
	switch x := v.(type) {
	case float64:
		if x > 1e12 {
			return int64(x)
		}
		if x > 1e9 {
			return int64(x) * 1000
		}
	case int64:
		if x > 1e12 {
			return x
		}
		if x > 1e9 {
			return x * 1000
		}
	case string:
		s := strings.TrimSpace(x)
		if s == "" {
			return 0
		}
		for _, layout := range []string{time.RFC3339Nano, time.RFC3339} {
			if t, err := time.Parse(layout, s); err == nil && t.Unix() > 1e9 {
				return t.UnixMilli()
			}
		}
		if f, err := strconv.ParseFloat(s, 64); err == nil {
			return parseTimestampAny(f)
		}
	}
	return 0
}

func parseDurationSecAny(v any) int64 {
	switch x := v.(type) {
	case float64:
		if x > 0 {
			return int64(x)
		}
	case int64:
		if x > 0 {
			return x
		}
	case string:
		if f, err := strconv.ParseFloat(strings.TrimSpace(x), 64); err == nil && f > 0 {
			return int64(f)
		}
	}
	return 0
}

func formatExpiryClock(ms, nowMs int64) string {
	if ms <= 0 {
		return "--"
	}
	t := time.UnixMilli(ms).In(time.Local)
	now := time.UnixMilli(nowMs).In(time.Local)
	if t.Year() == now.Year() && t.YearDay() == now.YearDay() {
		return t.Format("15:04")
	}
	if t.Year() == now.Year() {
		return t.Format("01-02 15:04")
	}
	return t.Format("2006-01-02 15:04")
}

func formatShortDuration(ms int64) string {
	if ms <= 0 {
		return "0m"
	}
	sec := ms / 1000
	days := sec / 86400
	hrs := (sec % 86400) / 3600
	mins := (sec % 3600) / 60
	switch {
	case days > 0:
		return fmt.Sprintf("%d天%dh", days, hrs)
	case hrs > 0:
		return fmt.Sprintf("%dh%dm", hrs, mins)
	default:
		if mins < 1 {
			mins = 1
		}
		return fmt.Sprintf("%dm", mins)
	}
}

func (m *Manager) loadAgyQuotas() map[string]*usage.AgyAccountQuota {
	if m.agyQuotaPath == "" {
		return nil
	}
	b, err := os.ReadFile(m.agyQuotaPath)
	if err != nil {
		return nil
	}
	var qf usage.AgyQuotaFile
	if json.Unmarshal(b, &qf) != nil {
		return nil
	}
	return qf.Accounts
}

func (m *Manager) inferRecentQuotaEmail() string {
	accts := m.loadAgyQuotas()
	if len(accts) == 0 {
		return ""
	}
	var realEmails []string
	for k, v := range accts {
		if k == "" || k == "default" {
			continue
		}
		em := strings.TrimSpace(v.Email)
		if em == "" {
			em = k
		}
		realEmails = append(realEmails, em)
	}
	if len(realEmails) == 1 {
		return realEmails[0]
	}
	return ""
}

func matchQuotaAccount(accts map[string]*usage.AgyAccountQuota, email, alias string) *usage.AgyAccountQuota {
	if len(accts) == 0 {
		return nil
	}
	if email != "" {
		if aq := accts[strings.ToLower(strings.TrimSpace(email))]; aq != nil {
			return aq
		}
	}
	al := strings.ToLower(strings.TrimSpace(alias))
	if al == "" {
		return nil
	}
	var matched *usage.AgyAccountQuota
	for k, aq := range accts {
		if k == al || strings.HasPrefix(k, al+"@") {
			if matched != nil {
				return nil
			}
			matched = aq
		}
	}
	if matched != nil {
		return matched
	}
	// Fallback: unique prefix match on the email local-part (for short aliases like "leng" -> "lengxxx@gmail.com").
	if len(al) >= 3 {
		for k, aq := range accts {
			if k == "" || k == "default" {
				continue
			}
			if strings.HasPrefix(k, al) {
				if matched != nil {
					return nil
				}
				matched = aq
			}
		}
	}
	return matched
}

func formatAccountQuotaBrief(aq *usage.AgyAccountQuota) (string, string) {
	if aq == nil || len(aq.Quota) == 0 {
		return "", ""
	}
	fmtBucket := func(b usage.AgyQuotaBucket, exists bool) string {
		if !exists || b.Disabled || b.RemainingFraction == nil {
			return ""
		}
		rem := *b.RemainingFraction * 100
		if rem < 0 {
			rem = 0
		}
		if rem > 100 {
			rem = 100
		}
		res := fmt.Sprintf("剩 %.1f%%", rem)
		if rt := strings.TrimSpace(b.ResetTime); rt != "" {
			if t, err := time.Parse(time.RFC3339, rt); err == nil && t.Unix() > 0 {
				if time.Now().After(t) {
					return "已重置"
				}
				loc := t.In(time.Local)
				nowLoc := time.Now().In(time.Local)
				if loc.Year() == nowLoc.Year() && loc.YearDay() == nowLoc.YearDay() {
					res += fmt.Sprintf(" (%s重置)", loc.Format("15:04"))
				} else {
					res += fmt.Sprintf(" (%s重置)", loc.Format("01-02 15:04"))
				}
			}
		}
		return res
	}
	b5h, ok5 := aq.Quota["gemini-5h"]
	bwk, okw := aq.Quota["gemini-weekly"]
	return fmtBucket(b5h, ok5), fmtBucket(bwk, okw)
}
