package usage

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

var (
	sysProxyMu        sync.Mutex
	sysProxyCheckedAt time.Time
	sysProxyCached    *url.URL
)

func buildUsageHTTPClient(explicitProxy string) *http.Client {
	var fixedProxy *url.URL
	if p := strings.TrimSpace(explicitProxy); p != "" {
		if !strings.Contains(p, "://") {
			p = "http://" + p
		}
		if u, err := url.Parse(p); err == nil && u.Host != "" {
			fixedProxy = u
		}
	}
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.Proxy = func(req *http.Request) (*url.URL, error) {
		if fixedProxy != nil {
			return fixedProxy, nil
		}
		if u, err := http.ProxyFromEnvironment(req); err == nil && u != nil {
			return u, nil
		}
		return detectMacSystemProxy(), nil
	}
	return &http.Client{
		Timeout:   6 * time.Second,
		Transport: tr,
	}
}

func detectMacSystemProxy() *url.URL {
	sysProxyMu.Lock()
	if !sysProxyCheckedAt.IsZero() && time.Since(sysProxyCheckedAt) < 5*time.Minute {
		u := sysProxyCached
		sysProxyMu.Unlock()
		return u
	}
	sysProxyMu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), 1500*time.Millisecond)
	defer cancel()
	out, err := exec.CommandContext(ctx, "scutil", "--proxy").Output()
	var parsed *url.URL
	if err == nil {
		kv := make(map[string]string)
		sc := bufio.NewScanner(bytes.NewReader(out))
		for sc.Scan() {
			line := strings.TrimSpace(sc.Text())
			k, v, ok := strings.Cut(line, ":")
			if ok {
				kv[strings.TrimSpace(k)] = strings.TrimSpace(v)
			}
		}
		switch {
		case kv["HTTPSEnable"] == "1" && kv["HTTPSProxy"] != "" && kv["HTTPSPort"] != "":
			parsed, _ = url.Parse("http://" + kv["HTTPSProxy"] + ":" + kv["HTTPSPort"])
		case kv["HTTPEnable"] == "1" && kv["HTTPProxy"] != "" && kv["HTTPPort"] != "":
			parsed, _ = url.Parse("http://" + kv["HTTPProxy"] + ":" + kv["HTTPPort"])
		case kv["SOCKSEnable"] == "1" && kv["SOCKSProxy"] != "" && kv["SOCKSPort"] != "":
			parsed, _ = url.Parse("socks5://" + kv["SOCKSProxy"] + ":" + kv["SOCKSPort"])
		}
	}

	sysProxyMu.Lock()
	sysProxyCheckedAt = time.Now()
	sysProxyCached = parsed
	sysProxyMu.Unlock()
	return parsed
}

type officialWindow struct {
	UsedPercent   float64 `json:"used_percent"`
	WindowMinutes int     `json:"window_minutes"`
	ResetsAt      int64   `json:"resets_at"` // unix ms
}

type officialQuotaSnapshot struct {
	PlanType   string          `json:"plan_type,omitempty"`
	Primary    *officialWindow `json:"primary,omitempty"`
	Secondary  *officialWindow `json:"secondary,omitempty"`
	ObservedAt int64           `json:"observed_at"` // unix ms
	SourceNote string          `json:"source_note,omitempty"`
}

func (e *Engine) fetchClaudeOAuthQuota(ctx context.Context) *officialQuotaSnapshot {
	if e.opts.DisableNetwork {
		return nil
	}
	token := e.readClaudeOAuthToken(ctx)
	if token == "" {
		return nil
	}
	reqCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, "https://api.anthropic.com/api/oauth/usage", nil)
	if err != nil {
		return nil
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("anthropic-beta", "oauth-2025-04-20")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "claude-code/2.0")

	resp, err := e.httpClient.Do(req)
	if err != nil {
		return nil
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 128*1024))
	if err != nil {
		return nil
	}

	var raw struct {
		FiveHour *struct {
			Utilization float64 `json:"utilization"`
			ResetsAt    string  `json:"resets_at"`
		} `json:"five_hour"`
		SevenDay *struct {
			Utilization float64 `json:"utilization"`
			ResetsAt    string  `json:"resets_at"`
		} `json:"seven_day"`
	}
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil
	}
	if raw.FiveHour == nil && raw.SevenDay == nil {
		return nil
	}
	snap := &officialQuotaSnapshot{
		ObservedAt: time.Now().UnixMilli(),
		SourceNote: "来自 Claude OAuth 实时配额接口",
	}
	if raw.FiveHour != nil {
		snap.Primary = &officialWindow{
			UsedPercent:   clampPercent(raw.FiveHour.Utilization),
			WindowMinutes: 300,
			ResetsAt:      parseISOToMillis(raw.FiveHour.ResetsAt),
		}
	}
	if raw.SevenDay != nil {
		snap.Secondary = &officialWindow{
			UsedPercent:   clampPercent(raw.SevenDay.Utilization),
			WindowMinutes: 10080,
			ResetsAt:      parseISOToMillis(raw.SevenDay.ResetsAt),
		}
	}
	return snap
}

func (e *Engine) readClaudeOAuthToken(ctx context.Context) string {
	if tok := strings.TrimSpace(os.Getenv("CLAUDE_CODE_OAUTH_TOKEN")); tok != "" {
		return tok
	}
	home, _ := os.UserHomeDir()
	for _, p := range []string{
		filepath.Join(home, ".claude", ".credentials.json"),
		filepath.Join(home, ".claude", "credentials.json"),
	} {
		if b, err := os.ReadFile(p); err == nil {
			if tok := extractClaudeOAuthToken(b); tok != "" {
				return tok
			}
		}
	}
	// Try macOS Keychain at most once per 10 minutes if missing to avoid repeated keychain spawns.
	e.oauthMu.Lock()
	skipKeychain := !e.keychainCheckedAt.IsZero() && e.keychainMissing && time.Since(e.keychainCheckedAt) < 10*time.Minute
	e.oauthMu.Unlock()
	if skipKeychain {
		return ""
	}

	kctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	out, err := exec.CommandContext(kctx, "security", "find-generic-password", "-s", "Claude Code-credentials", "-w").Output()
	e.oauthMu.Lock()
	e.keychainCheckedAt = time.Now()
	if err != nil || len(out) == 0 {
		e.keychainMissing = true
		e.oauthMu.Unlock()
		return ""
	}
	e.keychainMissing = false
	e.oauthMu.Unlock()
	return extractClaudeOAuthToken(out)
}

func extractClaudeOAuthToken(raw []byte) string {
	var parsed struct {
		ClaudeAiOauth *struct {
			AccessToken string `json:"accessToken"`
		} `json:"claudeAiOauth"`
		AccessToken string `json:"access_token"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return ""
	}
	if parsed.ClaudeAiOauth != nil && strings.TrimSpace(parsed.ClaudeAiOauth.AccessToken) != "" {
		return strings.TrimSpace(parsed.ClaudeAiOauth.AccessToken)
	}
	return strings.TrimSpace(parsed.AccessToken)
}

func (e *Engine) fetchCodexLiveQuota(ctx context.Context) *officialQuotaSnapshot {
	if e.opts.DisableNetwork {
		return nil
	}
	home, _ := os.UserHomeDir()
	authPath := filepath.Join(home, ".codex", "auth.json")
	b, err := os.ReadFile(authPath)
	if err != nil {
		return nil
	}
	var auth struct {
		Tokens *struct {
			AccessToken string `json:"access_token"`
			AccountID   string `json:"account_id"`
		} `json:"tokens"`
	}
	if err := json.Unmarshal(b, &auth); err != nil || auth.Tokens == nil || strings.TrimSpace(auth.Tokens.AccessToken) == "" {
		return nil
	}

	reqCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, "https://chatgpt.com/backend-api/wham/usage", nil)
	if err != nil {
		return nil
	}
	req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(auth.Tokens.AccessToken))
	if acc := strings.TrimSpace(auth.Tokens.AccountID); acc != "" {
		req.Header.Set("ChatGPT-Account-Id", acc)
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "codex-cli")

	resp, err := e.httpClient.Do(req)
	if err != nil {
		return nil
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 128*1024))
	if err != nil {
		return nil
	}

	var raw struct {
		PlanType  string `json:"plan_type"`
		RateLimit *struct {
			PrimaryWindow *struct {
				UsedPercent        float64 `json:"used_percent"`
				LimitWindowSeconds int     `json:"limit_window_seconds"`
				ResetAt            int64   `json:"reset_at"`
			} `json:"primary_window"`
			SecondaryWindow *struct {
				UsedPercent        float64 `json:"used_percent"`
				LimitWindowSeconds int     `json:"limit_window_seconds"`
				ResetAt            int64   `json:"reset_at"`
			} `json:"secondary_window"`
		} `json:"rate_limit"`
	}
	if err := json.Unmarshal(body, &raw); err != nil || raw.RateLimit == nil {
		return nil
	}
	snap := &officialQuotaSnapshot{
		PlanType:   raw.PlanType,
		ObservedAt: time.Now().UnixMilli(),
		SourceNote: "来自 Codex 实时配额接口",
	}
	if pw := raw.RateLimit.PrimaryWindow; pw != nil {
		mins := pw.LimitWindowSeconds / 60
		if mins <= 0 {
			mins = 300
		}
		snap.Primary = &officialWindow{
			UsedPercent:   clampPercent(pw.UsedPercent),
			WindowMinutes: mins,
			ResetsAt:      unixSecToMillis(pw.ResetAt),
		}
	}
	if sw := raw.RateLimit.SecondaryWindow; sw != nil {
		mins := sw.LimitWindowSeconds / 60
		if mins <= 0 {
			mins = 10080
		}
		snap.Secondary = &officialWindow{
			UsedPercent:   clampPercent(sw.UsedPercent),
			WindowMinutes: mins,
			ResetsAt:      unixSecToMillis(sw.ResetAt),
		}
	}
	return snap
}

func clampPercent(v float64) float64 {
	if v < 0 {
		return 0
	}
	if v > 100 {
		return 100
	}
	return v
}

func unixSecToMillis(sec int64) int64 {
	if sec <= 0 {
		return 0
	}
	if sec > 1e12 {
		return sec
	}
	return sec * 1000
}

func parseISOToMillis(raw string) int64 {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0
	}
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339} {
		if t, err := time.Parse(layout, raw); err == nil {
			return t.UnixMilli()
		}
	}
	return 0
}
