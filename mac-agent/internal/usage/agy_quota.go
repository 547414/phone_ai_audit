package usage

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"
)

const agyQuotaSchemaVersion = 2

const agyDefaultAccountKey = "default"

// AgyQuotaBucket represents one entry in Antigravity CLI's official statusLine JSON `quota` map.
type AgyQuotaBucket struct {
	RemainingFraction *float64 `json:"remaining_fraction,omitempty"`
	RemainingAmount   *float64 `json:"remaining_amount,omitempty"`
	ResetTime         string   `json:"reset_time,omitempty"`
	ResetInSeconds    int64    `json:"reset_in_seconds,omitempty"`
	Disabled          bool     `json:"disabled,omitempty"`
}

// AgyAccountQuota stores the latest official quota snapshot for a single Antigravity account.
type AgyAccountQuota struct {
	Email      string                    `json:"email,omitempty"`
	ObservedAt int64                     `json:"observed_at"`
	PlanTier   string                    `json:"plan_tier,omitempty"`
	ModelID    string                    `json:"model_id,omitempty"`
	ModelName  string                    `json:"model_name,omitempty"`
	Quota      map[string]AgyQuotaBucket `json:"quota,omitempty"`
}

// AgyQuotaFile stores official quota snapshots (per account) and conversation->account mappings
// captured via Antigravity CLI's `statusLine` hook.
type AgyQuotaFile struct {
	Version    int   `json:"version,omitempty"`
	ObservedAt int64 `json:"observed_at"`

	// Legacy v1 top-level fields (only read when Accounts is empty for backward compatibility).
	PlanTier  string                    `json:"plan_tier,omitempty"`
	ModelID   string                    `json:"model_id,omitempty"`
	ModelName string                    `json:"model_name,omitempty"`
	Quota     map[string]AgyQuotaBucket `json:"quota,omitempty"`

	// v2 multi-account fields.
	Accounts      map[string]*AgyAccountQuota `json:"accounts,omitempty"`      // normalized account key -> latest quota
	Conversations map[string]string           `json:"conversations,omitempty"` // conversation_id -> normalized account key
}

// DefaultAgyQuotaPath returns ~/.config/ai-audit/agy-quota.json.
func DefaultAgyQuotaPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config", "ai-audit", "agy-quota.json")
}

func normalizeAgyAccountKey(email string) string {
	e := strings.ToLower(strings.TrimSpace(email))
	if e == "" {
		return agyDefaultAccountKey
	}
	return e
}

func agyAccountDisplayLabel(acctKey, rawEmail string) string {
	if s := strings.TrimSpace(rawEmail); s != "" {
		return s
	}
	if acctKey == "" || acctKey == agyDefaultAccountKey {
		return "默认账号"
	}
	return acctKey
}

func extractAgyConvIDFromPath(p string) string {
	p = strings.TrimSpace(p)
	if p == "" {
		return ""
	}
	parts := strings.Split(filepath.ToSlash(p), "/")
	for i := 0; i < len(parts); i++ {
		if parts[i] == ".system_generated" && i >= 1 && parts[i-1] != "" && parts[i-1] != "." && parts[i-1] != ".." {
			return parts[i-1]
		}
	}
	for i := 0; i+1 < len(parts); i++ {
		if parts[i] == "brain" && parts[i+1] != "" && parts[i+1] != "." && parts[i+1] != ".." {
			return parts[i+1]
		}
	}
	return ""
}

func acquireAgyQuotaLock(outPath string) func() {
	lockPath := outPath + ".lock"
	f, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return func() {}
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		_ = f.Close()
		return func() {}
	}
	return func() {
		_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		_ = f.Close()
	}
}

func agyFloatPtrEqual(a, b *float64) bool {
	if a == nil && b == nil {
		return true
	}
	if a == nil || b == nil {
		return false
	}
	return math.Abs(*a-*b) <= 1e-6
}

func agyQuotaMapsEquivalent(a, b map[string]AgyQuotaBucket) bool {
	if len(a) != len(b) {
		return false
	}
	for k, va := range a {
		vb, ok := b[k]
		if !ok {
			return false
		}
		if va.Disabled != vb.Disabled {
			return false
		}
		if !agyFloatPtrEqual(va.RemainingFraction, vb.RemainingFraction) {
			return false
		}
		if !agyFloatPtrEqual(va.RemainingAmount, vb.RemainingAmount) {
			return false
		}
		if strings.TrimSpace(va.ResetTime) != strings.TrimSpace(vb.ResetTime) {
			return false
		}
		// Only compare reset_in_seconds when reset_time is absent; otherwise 1s tick jitter would trigger constant writes.
		if strings.TrimSpace(va.ResetTime) == "" {
			diff := va.ResetInSeconds - vb.ResetInSeconds
			if diff < 0 {
				diff = -diff
			}
			if diff > 5 {
				return false
			}
		}
	}
	return true
}

func hasRealAgyAccounts(accts map[string]*AgyAccountQuota) bool {
	for k := range accts {
		if k != "" && k != agyDefaultAccountKey {
			return true
		}
	}
	return false
}

// RecordAgyStatusLine parses the official Antigravity CLI statusLine stdin JSON payload
// and atomically merges per-account quota snapshots and conversation->account mappings into outPath.
func RecordAgyStatusLine(rawJSON []byte, outPath string) error {
	if len(rawJSON) == 0 || outPath == "" {
		return nil
	}
	var payload struct {
		Email        string `json:"email"`
		AccountEmail string `json:"account_email"`
		UserEmail    string `json:"user_email"`
		Account      *struct {
			Email string `json:"email"`
		} `json:"account"`
		User *struct {
			Email string `json:"email"`
		} `json:"user"`
		ConversationID string `json:"conversation_id"`
		SessionID      string `json:"session_id"`
		TranscriptPath string `json:"transcript_path"`
		PlanTier       string `json:"plan_tier"`
		Model          *struct {
			ID          string `json:"id"`
			DisplayName string `json:"display_name"`
		} `json:"model"`
		Quota map[string]AgyQuotaBucket `json:"quota"`
	}
	if err := json.Unmarshal(rawJSON, &payload); err != nil {
		return err
	}

	rawEmail := strings.TrimSpace(payload.Email)
	if rawEmail == "" {
		rawEmail = strings.TrimSpace(payload.AccountEmail)
	}
	if rawEmail == "" {
		rawEmail = strings.TrimSpace(payload.UserEmail)
	}
	if rawEmail == "" && payload.Account != nil {
		rawEmail = strings.TrimSpace(payload.Account.Email)
	}
	if rawEmail == "" && payload.User != nil {
		rawEmail = strings.TrimSpace(payload.User.Email)
	}
	acctKey := normalizeAgyAccountKey(rawEmail)

	convID := strings.TrimSpace(payload.ConversationID)
	if convID == "" {
		convID = strings.TrimSpace(payload.SessionID)
	}
	if convID == "" {
		convID = extractAgyConvIDFromPath(payload.TranscriptPath)
	}

	hasQuota := len(payload.Quota) > 0
	hasConvMapping := convID != "" && acctKey != agyDefaultAccountKey
	if !hasQuota && !hasConvMapping {
		return nil
	}

	if err := os.MkdirAll(filepath.Dir(outPath), 0o700); err != nil {
		return err
	}

	unlock := acquireAgyQuotaLock(outPath)
	defer unlock()

	var qf AgyQuotaFile
	if prevBytes, err := os.ReadFile(outPath); err == nil {
		_ = json.Unmarshal(prevBytes, &qf)
	}
	if qf.Accounts == nil {
		qf.Accounts = make(map[string]*AgyAccountQuota)
	}
	if qf.Conversations == nil {
		qf.Conversations = make(map[string]string)
	}

	// Migrate legacy v1 snapshot only if we still have no real email-keyed accounts.
	if len(qf.Accounts) == 0 && len(qf.Quota) > 0 && acctKey == agyDefaultAccountKey {
		qf.Accounts[agyDefaultAccountKey] = &AgyAccountQuota{
			ObservedAt: qf.ObservedAt,
			PlanTier:   qf.PlanTier,
			ModelID:    qf.ModelID,
			ModelName:  qf.ModelName,
			Quota:      qf.Quota,
		}
	}

	nowMs := time.Now().UnixMilli()
	convChanged := false
	if hasConvMapping {
		if qf.Conversations[convID] != acctKey {
			qf.Conversations[convID] = acctKey
			convChanged = true
		}
	}

	// If real email-keyed accounts exist, drop/ignore anonymous default placeholders
	// (e.g. emitted briefly during CLI startup before auth finishes, or leftover v1 snapshot).
	if acctKey == agyDefaultAccountKey && hasRealAgyAccounts(qf.Accounts) {
		if !convChanged {
			return nil
		}
	} else if hasQuota {
		if acctKey != agyDefaultAccountKey {
			delete(qf.Accounts, agyDefaultAccountKey)
		}
		next := &AgyAccountQuota{
			Email:      rawEmail,
			ObservedAt: nowMs,
			PlanTier:   strings.TrimSpace(payload.PlanTier),
			Quota:      payload.Quota,
		}
		if payload.Model != nil {
			next.ModelID = strings.TrimSpace(payload.Model.ID)
			next.ModelName = strings.TrimSpace(payload.Model.DisplayName)
		}

		// Avoid frequent disk rewrites when Antigravity fires statusLine every 300ms with unchanged quota.
		if prev := qf.Accounts[acctKey]; prev != nil && !convChanged {
			if nowMs-prev.ObservedAt < 10_000 &&
				prev.Email == next.Email &&
				prev.PlanTier == next.PlanTier &&
				prev.ModelID == next.ModelID &&
				prev.ModelName == next.ModelName &&
				agyQuotaMapsEquivalent(prev.Quota, next.Quota) {
				return nil
			}
		}
		qf.Accounts[acctKey] = next
	} else if !convChanged {
		return nil
	}

	qf.Version = agyQuotaSchemaVersion
	qf.ObservedAt = nowMs
	// Clear legacy v1 top-level fields once written in v2 format.
	qf.PlanTier = ""
	qf.ModelID = ""
	qf.ModelName = ""
	qf.Quota = nil

	b, err := json.Marshal(qf)
	if err != nil {
		return err
	}
	tmpFile, err := os.CreateTemp(filepath.Dir(outPath), ".agy-quota-*.tmp")
	if err != nil {
		return err
	}
	tmpPath := tmpFile.Name()
	if _, err := tmpFile.Write(b); err != nil {
		_ = tmpFile.Close()
		_ = os.Remove(tmpPath)
		return err
	}
	if err := tmpFile.Chmod(0o600); err != nil {
		_ = tmpFile.Close()
		_ = os.Remove(tmpPath)
		return err
	}
	if err := tmpFile.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return err
	}
	if err := os.Rename(tmpPath, outPath); err != nil {
		_ = os.Remove(tmpPath)
		return err
	}
	return nil
}

func classifyAgyBucket(key string, b AgyQuotaBucket) (family string, kind string) {
	lk := strings.ToLower(strings.TrimSpace(key))
	switch {
	case strings.Contains(lk, "gemini"):
		family = "gemini"
	case strings.HasPrefix(lk, "3p") || strings.Contains(lk, "claude") || strings.Contains(lk, "gpt") || strings.Contains(lk, "partner") || strings.Contains(lk, "third"):
		family = "claude_gpt"
	default:
		family = "default"
	}

	switch {
	case strings.Contains(lk, "week") || strings.Contains(lk, "7d") || strings.Contains(lk, "168h"):
		kind = "weekly"
	case strings.Contains(lk, "5h") || strings.Contains(lk, "five") || strings.Contains(lk, "hour") || strings.Contains(lk, "short"):
		kind = "5h"
	case b.ResetInSeconds > 24*3600:
		kind = "weekly"
	case b.ResetInSeconds > 0 && b.ResetInSeconds <= 12*3600:
		kind = "5h"
	default:
		kind = "other"
	}
	return family, kind
}

func agyFamilyLabel(family string) string {
	switch family {
	case "gemini":
		return "Gemini 组"
	case "claude_gpt":
		return "Claude/GPT 组"
	default:
		return "当前模型组"
	}
}

func agyBucketToWindow(b AgyQuotaBucket, observedAtMs int64, defaultWindowMin int) (*officialWindow, bool) {
	if b.Disabled || b.RemainingFraction == nil {
		return nil, false
	}
	rem := *b.RemainingFraction
	if rem < 0 {
		rem = 0
	}
	if rem > 1 {
		rem = 1
	}
	usedPct := clampPercent((1.0 - rem) * 100.0)
	resetsAt := parseISOToMillis(b.ResetTime)
	if resetsAt == 0 && b.ResetInSeconds > 0 && observedAtMs > 0 {
		resetsAt = observedAtMs + b.ResetInSeconds*1000
	}
	return &officialWindow{
		UsedPercent:   usedPct,
		WindowMinutes: defaultWindowMin,
		ResetsAt:      resetsAt,
	}, true
}

func convertAgyAccountQuota(acctKey string, aq *AgyAccountQuota) (*officialQuotaSnapshot, string) {
	if aq == nil || len(aq.Quota) == 0 {
		return nil, ""
	}
	type familyBuckets struct {
		w5h    *officialWindow
		weekly *officialWindow
	}
	groups := make(map[string]*familyBuckets)
	keys := make([]string, 0, len(aq.Quota))
	for k := range aq.Quota {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	for _, k := range keys {
		b := aq.Quota[k]
		fam, kind := classifyAgyBucket(k, b)
		if kind != "5h" && kind != "weekly" {
			continue
		}
		defMin := 300
		if kind == "weekly" {
			defMin = 10080
		}
		w, ok := agyBucketToWindow(b, aq.ObservedAt, defMin)
		if !ok {
			continue
		}
		g := groups[fam]
		if g == nil {
			g = &familyBuckets{}
			groups[fam] = g
		}
		if kind == "5h" {
			if g.w5h == nil || w.UsedPercent > g.w5h.UsedPercent || (w.UsedPercent == g.w5h.UsedPercent && w.ResetsAt > g.w5h.ResetsAt) {
				g.w5h = w
			}
		} else {
			if g.weekly == nil || w.UsedPercent > g.weekly.UsedPercent || (w.UsedPercent == g.weekly.UsedPercent && w.ResetsAt > g.weekly.ResetsAt) {
				g.weekly = w
			}
		}
	}
	if len(groups) == 0 {
		return nil, ""
	}

	// Pick preferred family matching the currently active Antigravity model (default: gemini).
	modelHint := strings.ToLower(aq.ModelID + " " + aq.ModelName)
	pref := "gemini"
	if strings.Contains(modelHint, "claude") || strings.Contains(modelHint, "gpt") {
		pref = "claude_gpt"
	}
	chosenFam := ""
	if groups[pref] != nil {
		chosenFam = pref
	} else {
		bestScore := -1.0
		for _, fam := range []string{"gemini", "claude_gpt", "default"} {
			g := groups[fam]
			if g == nil {
				continue
			}
			score := 0.0
			if g.w5h != nil && g.w5h.UsedPercent > score {
				score = g.w5h.UsedPercent
			}
			if g.weekly != nil && g.weekly.UsedPercent > score {
				score = g.weekly.UsedPercent
			}
			if score > bestScore {
				bestScore = score
				chosenFam = fam
			}
		}
	}
	chosen := groups[chosenFam]
	if chosen == nil || (chosen.w5h == nil && chosen.weekly == nil) {
		return nil, ""
	}

	sourceNote := fmt.Sprintf("来自 Antigravity 官方 statusLine 实时配额（%s）", agyFamilyLabel(chosenFam))
	if acctKey != "" && acctKey != agyDefaultAccountKey {
		sourceNote = fmt.Sprintf("来自 Antigravity 官方 statusLine 实时配额（%s）", agyFamilyLabel(chosenFam))
	}

	snap := &officialQuotaSnapshot{
		PlanType:   aq.PlanTier,
		Primary:    chosen.w5h,
		Secondary:  chosen.weekly,
		ObservedAt: aq.ObservedAt,
		SourceNote: sourceNote,
	}

	// Summarize any secondary model group (e.g. Claude/GPT group).
	var otherNotes []string
	for _, fam := range []string{"gemini", "claude_gpt", "default"} {
		if fam == chosenFam {
			continue
		}
		g := groups[fam]
		if g == nil {
			continue
		}
		var parts []string
		if g.w5h != nil {
			parts = append(parts, fmt.Sprintf("5h 剩 %.0f%%", clampPercent(100-g.w5h.UsedPercent)))
		}
		if g.weekly != nil {
			parts = append(parts, fmt.Sprintf("周剩 %.0f%%", clampPercent(100-g.weekly.UsedPercent)))
		}
		if len(parts) > 0 {
			otherNotes = append(otherNotes, fmt.Sprintf("%s（%s）", agyFamilyLabel(fam), strings.Join(parts, " · ")))
		}
	}
	extraNote := ""
	if len(otherNotes) > 0 {
		extraNote = "其他配额组：" + strings.Join(otherNotes, "；")
	}
	return snap, extraNote
}

func convertAgyQuotaFile(qf *AgyQuotaFile) (map[string]*cachedAgyAccount, map[string]string) {
	if qf == nil {
		return nil, nil
	}
	convs := make(map[string]string, len(qf.Conversations))
	for cid, acct := range qf.Conversations {
		cid = strings.TrimSpace(cid)
		acct = normalizeAgyAccountKey(acct)
		if cid != "" && acct != agyDefaultAccountKey {
			convs[cid] = acct
		}
	}

	out := make(map[string]*cachedAgyAccount)
	if len(qf.Accounts) > 0 {
		hasReal := hasRealAgyAccounts(qf.Accounts)
		for k, aq := range qf.Accounts {
			if aq == nil {
				continue
			}
			acctKey := normalizeAgyAccountKey(k)
			if acctKey == agyDefaultAccountKey && aq.Email != "" {
				acctKey = normalizeAgyAccountKey(aq.Email)
			}
			if hasReal && acctKey == agyDefaultAccountKey {
				continue
			}
			snap, extra := convertAgyAccountQuota(acctKey, aq)
			if snap == nil {
				continue
			}
			modelName := strings.TrimSpace(aq.ModelName)
			if modelName == "" {
				modelName = strings.TrimSpace(aq.ModelID)
			}
			out[acctKey] = &cachedAgyAccount{
				Account:   acctKey,
				Label:     agyAccountDisplayLabel(acctKey, aq.Email),
				ModelName: modelName,
				Quota:     snap,
				ExtraNote: extra,
			}
		}
		return out, convs
	}

	// Legacy v1 fallback (single un-emailed snapshot).
	if len(qf.Quota) > 0 {
		aq := &AgyAccountQuota{
			ObservedAt: qf.ObservedAt,
			PlanTier:   qf.PlanTier,
			ModelID:    qf.ModelID,
			ModelName:  qf.ModelName,
			Quota:      qf.Quota,
		}
		snap, extra := convertAgyAccountQuota(agyDefaultAccountKey, aq)
		if snap != nil {
			modelName := strings.TrimSpace(aq.ModelName)
			if modelName == "" {
				modelName = strings.TrimSpace(aq.ModelID)
			}
			out[agyDefaultAccountKey] = &cachedAgyAccount{
				Account:   agyDefaultAccountKey,
				Label:     agyAccountDisplayLabel(agyDefaultAccountKey, ""),
				ModelName: modelName,
				Quota:     snap,
				ExtraNote: extra,
			}
		}
	}
	return out, convs
}

// agyProfileMeta mirrors the non-secret metadata stored in ~/.config/ai-audit/agy-profiles.json.
type agyProfileMeta struct {
	Name             string `json:"name"`
	Email            string `json:"email,omitempty"`
	SavedAt          int64  `json:"saved_at"`
	HasRefreshToken  bool   `json:"has_refresh_token,omitempty"`
	TokenExpiresAt   int64  `json:"token_expires_at,omitempty"`
	RefreshExpiresAt int64  `json:"refresh_expires_at,omitempty"`
	CustomTTLMs      int64  `json:"custom_ttl_ms,omitempty"`
}

func loadAgyProfilesMeta(path string) []agyProfileMeta {
	if path == "" {
		return nil
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var raw struct {
		Profiles []agyProfileMeta `json:"profiles"`
	}
	if json.Unmarshal(b, &raw) != nil {
		return nil
	}
	return raw.Profiles
}

func applyAgyProfileAuthStatus(acc *AccountUsage, profiles []agyProfileMeta, nowMs int64) {
	if acc == nil || len(profiles) == 0 {
		return
	}
	var matched *agyProfileMeta
	acctLow := strings.ToLower(strings.TrimSpace(acc.Account))
	for i := range profiles {
		p := &profiles[i]
		if p.Email != "" && strings.ToLower(strings.TrimSpace(p.Email)) == acctLow {
			matched = p
			break
		}
	}
	if matched == nil && acctLow != "" {
		for i := range profiles {
			p := &profiles[i]
			pn := strings.ToLower(strings.TrimSpace(p.Name))
			if pn != "" && (pn == acctLow || strings.HasPrefix(acctLow, pn+"@")) {
				matched = p
				break
			}
		}
	}
	if matched == nil {
		return
	}
	acc.AuthProfile = matched.Name

	if matched.CustomTTLMs < 0 {
		return
	}
	var expAt int64
	var hard bool
	var warnWindowMs int64 = 24 * 3600 * 1000

	switch {
	case matched.CustomTTLMs > 0 && matched.SavedAt > 0:
		expAt = matched.SavedAt + matched.CustomTTLMs
		hard = true
		if matched.CustomTTLMs <= 6*3600*1000 {
			warnWindowMs = matched.CustomTTLMs / 4
			if warnWindowMs < 15*60*1000 {
				warnWindowMs = 15 * 60 * 1000
			}
		}
	case matched.RefreshExpiresAt > 0:
		expAt = matched.RefreshExpiresAt
		hard = true
	case !matched.HasRefreshToken && matched.TokenExpiresAt > 0:
		expAt = matched.TokenExpiresAt
		hard = true
		warnWindowMs = 15 * 60 * 1000
	case matched.SavedAt > 0:
		expAt = matched.SavedAt + 14*24*3600*1000
		hard = false
	default:
		return
	}

	acc.AuthExpiresAt = expAt
	diffMs := expAt - nowMs
	t := time.UnixMilli(expAt).In(time.Local)
	nowLoc := time.UnixMilli(nowMs).In(time.Local)
	clock := t.Format("01-02 15:04")
	if t.Year() == nowLoc.Year() && t.YearDay() == nowLoc.YearDay() {
		clock = t.Format("15:04")
	}

	fmtDur := func(ms int64) string {
		if ms <= 0 {
			return "0m"
		}
		sec := ms / 1000
		d := sec / 86400
		h := (sec % 86400) / 3600
		m := (sec % 3600) / 60
		switch {
		case d > 0:
			return fmt.Sprintf("%d天%dh", d, h)
		case h > 0:
			return fmt.Sprintf("%dh%dm", h, m)
		default:
			if m < 1 {
				m = 1
			}
			return fmt.Sprintf("%dm", m)
		}
	}

	if diffMs <= 0 {
		acc.AuthExpired = true
		if hard {
			acc.AuthExpireNote = fmt.Sprintf("登录已过期（%s），请运行 ai-audit agy login %s 重新登录", clock, matched.Name)
		} else {
			acc.AuthExpireNote = fmt.Sprintf("凭据已存超 14 天，建议运行 ai-audit agy login %s 续登防过期", matched.Name)
		}
		return
	}
	if diffMs <= warnWindowMs {
		acc.AuthExpiringSoon = true
		if hard {
			acc.AuthExpireNote = fmt.Sprintf("登录即将到期（剩 %s · %s），建议先执行 ai-audit agy login %s 重登", fmtDur(diffMs), clock, matched.Name)
		} else {
			acc.AuthExpireNote = fmt.Sprintf("即将到达 14 天续登提醒（剩 %s · %s），建议提前重登更新", fmtDur(diffMs), clock)
		}
		return
	}
	if hard {
		acc.AuthExpireNote = fmt.Sprintf("登录有效 · 剩 %s（%s到期）", fmtDur(diffMs), clock)
	} else {
		acc.AuthExpireNote = fmt.Sprintf("登录有效（%s提醒续登）", clock)
	}
}
