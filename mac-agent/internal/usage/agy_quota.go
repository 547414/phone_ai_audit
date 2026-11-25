package usage

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"time"
)

// AgyQuotaBucket represents one entry in Antigravity CLI's official statusLine JSON `quota` map.
type AgyQuotaBucket struct {
	RemainingFraction *float64 `json:"remaining_fraction,omitempty"`
	RemainingAmount   *float64 `json:"remaining_amount,omitempty"`
	ResetTime         string   `json:"reset_time,omitempty"`
	ResetInSeconds    int64    `json:"reset_in_seconds,omitempty"`
	Disabled          bool     `json:"disabled,omitempty"`
}

// AgyQuotaFile stores the latest official quota snapshot captured via Antigravity CLI's `statusLine` hook.
type AgyQuotaFile struct {
	ObservedAt int64                     `json:"observed_at"`
	PlanTier   string                    `json:"plan_tier,omitempty"`
	ModelID    string                    `json:"model_id,omitempty"`
	ModelName  string                    `json:"model_name,omitempty"`
	Quota      map[string]AgyQuotaBucket `json:"quota,omitempty"`
}

// DefaultAgyQuotaPath returns ~/.config/ai-audit/agy-quota.json.
func DefaultAgyQuotaPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config", "ai-audit", "agy-quota.json")
}

// RecordAgyStatusLine parses the official Antigravity CLI statusLine stdin JSON payload
// and atomically persists non-empty quota snapshots to outPath.
func RecordAgyStatusLine(rawJSON []byte, outPath string) error {
	if len(rawJSON) == 0 || outPath == "" {
		return nil
	}
	var payload struct {
		PlanTier string `json:"plan_tier"`
		Model    *struct {
			ID          string `json:"id"`
			DisplayName string `json:"display_name"`
		} `json:"model"`
		Quota map[string]AgyQuotaBucket `json:"quota"`
	}
	if err := json.Unmarshal(rawJSON, &payload); err != nil {
		return err
	}
	if len(payload.Quota) == 0 {
		return nil
	}

	nowMs := time.Now().UnixMilli()
	next := AgyQuotaFile{
		ObservedAt: nowMs,
		PlanTier:   strings.TrimSpace(payload.PlanTier),
		Quota:      payload.Quota,
	}
	if payload.Model != nil {
		next.ModelID = strings.TrimSpace(payload.Model.ID)
		next.ModelName = strings.TrimSpace(payload.Model.DisplayName)
	}

	// Avoid frequent disk rewrites when Antigravity fires statusLine every 300ms with identical quota.
	if prevBytes, err := os.ReadFile(outPath); err == nil {
		var prev AgyQuotaFile
		if json.Unmarshal(prevBytes, &prev) == nil {
			if nowMs-prev.ObservedAt < 10_000 &&
				prev.PlanTier == next.PlanTier &&
				prev.ModelID == next.ModelID &&
				reflect.DeepEqual(prev.Quota, next.Quota) {
				return nil
			}
		}
	}

	if err := os.MkdirAll(filepath.Dir(outPath), 0o700); err != nil {
		return err
	}
	b, err := json.Marshal(next)
	if err != nil {
		return err
	}
	tmp := outPath + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, outPath)
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

func convertAgyQuotaFile(qf *AgyQuotaFile) (*officialQuotaSnapshot, string) {
	if qf == nil || len(qf.Quota) == 0 {
		return nil, ""
	}
	type familyBuckets struct {
		w5h    *officialWindow
		weekly *officialWindow
	}
	groups := make(map[string]*familyBuckets)
	keys := make([]string, 0, len(qf.Quota))
	for k := range qf.Quota {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	for _, k := range keys {
		b := qf.Quota[k]
		fam, kind := classifyAgyBucket(k, b)
		if kind != "5h" && kind != "weekly" {
			continue
		}
		defMin := 300
		if kind == "weekly" {
			defMin = 10080
		}
		w, ok := agyBucketToWindow(b, qf.ObservedAt, defMin)
		if !ok {
			continue
		}
		g := groups[fam]
		if g == nil {
			g = &familyBuckets{}
			groups[fam] = g
		}
		if kind == "5h" {
			// If multiple buckets map to same family+kind, keep the most constrained one.
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
	modelHint := strings.ToLower(qf.ModelID + " " + qf.ModelName)
	pref := "gemini"
	if strings.Contains(modelHint, "claude") || strings.Contains(modelHint, "gpt") {
		pref = "claude_gpt"
	}
	chosenFam := ""
	if groups[pref] != nil {
		chosenFam = pref
	} else {
		// Fall back to whichever group has the highest usage (or deterministic order).
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

	snap := &officialQuotaSnapshot{
		PlanType:   qf.PlanTier,
		Primary:    chosen.w5h,
		Secondary:  chosen.weekly,
		ObservedAt: qf.ObservedAt,
		SourceNote: fmt.Sprintf("来自 Antigravity 官方 statusLine 实时配额（%s）", agyFamilyLabel(chosenFam)),
	}

	// Summarize any secondary model group (e.g. Claude/GPT group) for history_note.
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
