package usage

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/klauspost/compress/zstd"
)

func TestEngineIncrementalDedupeAndQuota(t *testing.T) {
	tmp := t.TempDir()
	claudeDir := filepath.Join(tmp, "claude", "proj1")
	codexDir := filepath.Join(tmp, "codex", "2026", "11", "25")
	agyDir := filepath.Join(tmp, "agy", "conv1", ".system_generated", "logs")
	for _, d := range []string{claudeDir, codexDir, agyDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}

	now := time.Now().UTC()
	ts1 := now.Add(-30 * time.Minute).Format(time.RFC3339Nano)
	ts2 := now.Add(-10 * time.Minute).Format(time.RFC3339Nano)
	reset5h := now.Add(4 * time.Hour).Unix()
	reset7d := now.Add(48 * time.Hour).Unix()

	// Claude file with duplicate streaming requestId + synthetic error line that must be skipped.
	claudeFile := filepath.Join(claudeDir, "s1.jsonl")
	claudeData := "" +
		`{"type":"assistant","timestamp":"` + ts1 + `","requestId":"req-1","message":{"model":"claude-opus-5-5","usage":{"input_tokens":10,"cache_creation_input_tokens":200,"cache_read_input_tokens":300,"output_tokens":50}}}` + "\n" +
		`{"type":"assistant","timestamp":"` + ts1 + `","requestId":"req-1","message":{"model":"claude-opus-5-5","usage":{"input_tokens":10,"cache_creation_input_tokens":200,"cache_read_input_tokens":300,"output_tokens":120}}}` + "\n" +
		`{"type":"assistant","timestamp":"` + ts1 + `","requestId":"req-err","isApiErrorMessage":true,"message":{"model":"<synthetic>","usage":{"input_tokens":0,"output_tokens":0}}}` + "\n"
	if err := os.WriteFile(claudeFile, []byte(claudeData), 0o644); err != nil {
		t.Fatal(err)
	}

	// Codex file with turn_context + two cumulative token_count lines (including rate_limits).
	codexFile := filepath.Join(codexDir, "rollout-1.jsonl")
	codexData := "" +
		`{"timestamp":"` + ts1 + `","type":"turn_context","payload":{"model":"gpt-6.1-sol"}}` + "\n" +
		`{"timestamp":"` + ts1 + `","type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":1000,"cached_input_tokens":600,"cache_write_input_tokens":0,"output_tokens":100,"total_tokens":1100}},"rate_limits":{"plan_type":"plus","primary":{"used_percent":12.5,"window_minutes":300,"resets_at":` + itoa64(reset5h) + `},"secondary":{"used_percent":33.0,"window_minutes":10080,"resets_at":` + itoa64(reset7d) + `}}}}` + "\n" +
		`{"timestamp":"` + ts2 + `","type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":1500,"cached_input_tokens":900,"cache_write_input_tokens":0,"output_tokens":150,"total_tokens":1650}},"rate_limits":{"plan_type":"plus","primary":{"used_percent":18.0,"window_minutes":300,"resets_at":` + itoa64(reset5h) + `},"secondary":{"used_percent":35.0,"window_minutes":10080,"resets_at":` + itoa64(reset7d) + `}}}}` + "\n"
	if err := os.WriteFile(codexFile, []byte(codexData), 0o644); err != nil {
		t.Fatal(err)
	}

	// Antigravity transcript with PLANNER_RESPONSE entries.
	agyFile := filepath.Join(agyDir, "transcript.jsonl")
	agyData := "" +
		`{"step_index":0,"type":"USER_INPUT","created_at":"` + ts1 + `"}` + "\n" +
		`{"step_index":1,"type":"PLANNER_RESPONSE","created_at":"` + ts1 + `","model":"gemini-4-neon","input_tokens":500,"cache_read_tokens":2000,"output_tokens":300}` + "\n"
	if err := os.WriteFile(agyFile, []byte(agyData), 0o644); err != nil {
		t.Fatal(err)
	}

	cachePath := filepath.Join(tmp, "usage-cache.json")
	eng := New(Options{
		ClaudeDir:      filepath.Join(tmp, "claude"),
		CodexDir:       filepath.Join(tmp, "codex"),
		AgyDir:         filepath.Join(tmp, "agy"),
		CachePath:      cachePath,
		Hostname:       "test-mac",
		DisableNetwork: true,
	})

	sum, err := eng.Snapshot(context.Background(), true)
	if err != nil {
		t.Fatalf("Snapshot 1: %v", err)
	}
	if sum.Meta.FilesScanned != 3 {
		t.Fatalf("expected 3 scanned files, got %d", sum.Meta.FilesScanned)
	}

	byAgent := map[string]AgentUsage{}
	for _, a := range sum.Agents {
		byAgent[a.Agent] = a
	}

	// Claude follows official /stats un-deduplicated token sum: (10+200+300+50) + (10+200+300+120) = 560 + 630 = 1190,
	// while still counting 1 distinct requestId.
	if got := byAgent["claude"].Total.TotalTokens; got != 1190 {
		t.Fatalf("claude total mismatch: got %d, want 1190", got)
	}
	if got := byAgent["claude"].Total.Requests; got != 1 {
		t.Fatalf("claude requests mismatch: got %d, want 1", got)
	}

	// Codex: cumulative 1650 (fresh_in 600 + cached 900 + out 150 = 1650).
	if got := byAgent["codex"].Total.TotalTokens; got != 1650 {
		t.Fatalf("codex total mismatch: got %d, want 1650", got)
	}
	if byAgent["codex"].Window5h.Source != "official" || byAgent["codex"].Window5h.UsedPercent == nil || *byAgent["codex"].Window5h.UsedPercent != 18.0 {
		t.Fatalf("codex 5h window mismatch: %+v", byAgent["codex"].Window5h)
	}
	if byAgent["codex"].WindowWeekly.Source != "official" || byAgent["codex"].WindowWeekly.UsedPercent == nil || *byAgent["codex"].WindowWeekly.UsedPercent != 35.0 {
		t.Fatalf("codex weekly window mismatch: %+v", byAgent["codex"].WindowWeekly)
	}

	// Antigravity: 500 + 2000 + 300 = 2800.
	if got := byAgent["agy"].Total.TotalTokens; got != 2800 {
		t.Fatalf("agy total mismatch: got %d, want 2800", got)
	}

	// Grand total: 1190 + 1650 + 2800 = 5640.
	if sum.Total.TotalTokens != 5640 {
		t.Fatalf("grand total mismatch: got %d, want 5640", sum.Total.TotalTokens)
	}

	// Reload from disk cache: all 3 files should hit cache with 0 rescanned.
	eng2 := New(Options{
		ClaudeDir:      filepath.Join(tmp, "claude"),
		CodexDir:       filepath.Join(tmp, "codex"),
		AgyDir:         filepath.Join(tmp, "agy"),
		CachePath:      cachePath,
		Hostname:       "test-mac",
		DisableNetwork: true,
	})
	sum2, err := eng2.Snapshot(context.Background(), true)
	if err != nil {
		t.Fatalf("Snapshot 2: %v", err)
	}
	if sum2.Meta.FilesCached != 3 || sum2.Meta.FilesScanned != 0 {
		t.Fatalf("expected 3 cached / 0 scanned, got %+v", sum2.Meta)
	}
	if sum2.Total.TotalTokens != 5640 {
		t.Fatalf("cached grand total mismatch: got %d, want 5640", sum2.Total.TotalTokens)
	}

	// Append a new step + a CHECKPOINT step (omitting model) to agy transcript and verify incremental append adds only delta.
	f, err := os.OpenFile(agyFile, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = f.WriteString(`{"step_index":2,"type":"PLANNER_RESPONSE","created_at":"` + ts2 + `","model":"gemini-4-neon","input_tokens":100,"cache_read_tokens":0,"output_tokens":20}` + "\n")
	_, _ = f.WriteString(`{"step_index":3,"type":"CHECKPOINT","created_at":"` + ts2 + `","input_tokens":50,"cache_read_tokens":200,"output_tokens":50}` + "\n")
	_ = f.Close()

	sum3, err := eng2.Snapshot(context.Background(), true)
	if err != nil {
		t.Fatalf("Snapshot 3: %v", err)
	}
	if sum3.Total.TotalTokens != 6060 {
		t.Fatalf("incremental grand total mismatch: got %d, want 6060", sum3.Total.TotalTokens)
	}
	if sum3.Meta.FilesScanned != 1 || sum3.Meta.FilesCached != 2 {
		t.Fatalf("expected 1 scanned / 2 cached after append, got %+v", sum3.Meta)
	}
	for _, a := range sum3.Agents {
		if a.Agent == "agy" {
			if len(a.Models) != 1 || a.Models[0].Model != "gemini-4-neon" || a.Models[0].Total.TotalTokens != 3220 {
				t.Fatalf("agy checkpoint model inheritance mismatch: %+v", a.Models)
			}
		}
	}
}

func writeZstFile(t *testing.T, path string, data []byte) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	zw, err := zstd.NewWriter(f)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := zw.Write(data); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestCodexZstArchiveAndTransition(t *testing.T) {
	tmp := t.TempDir()
	codexOldDir := filepath.Join(tmp, "codex", "2026", "04", "01")
	codexNewDir := filepath.Join(tmp, "codex", "2026", "11", "20")
	for _, d := range []string{codexOldDir, codexNewDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}

	oldContent := "" +
		`{"timestamp":"2026-04-01T08:00:00Z","type":"turn_context","payload":{"model":"gpt-5.5"}}` + "\n" +
		`{"timestamp":"2026-04-01T08:01:00Z","type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":5000,"cached_input_tokens":3000,"output_tokens":400,"total_tokens":5400}}}}` + "\n"
	writeZstFile(t, filepath.Join(codexOldDir, "rollout-old.jsonl.zst"), []byte(oldContent))

	livePath := filepath.Join(codexNewDir, "rollout-live.jsonl")
	liveContent1 := "" +
		`{"timestamp":"2026-11-20T08:00:00Z","type":"turn_context","payload":{"model":"gpt-6.1-sol"}}` + "\n" +
		`{"timestamp":"2026-11-20T08:01:00Z","type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":1000,"cached_input_tokens":600,"output_tokens":100,"total_tokens":1100}}}}` + "\n"
	if err := os.WriteFile(livePath, []byte(liveContent1), 0o644); err != nil {
		t.Fatal(err)
	}

	cachePath := filepath.Join(tmp, "usage-cache.json")
	eng := New(Options{
		ClaudeDir:      filepath.Join(tmp, "claude"),
		CodexDir:       filepath.Join(tmp, "codex"),
		AgyDir:         filepath.Join(tmp, "agy"),
		CachePath:      cachePath,
		Hostname:       "test-mac",
		DisableNetwork: true,
	})

	sum1, err := eng.Snapshot(context.Background(), true)
	if err != nil {
		t.Fatal(err)
	}
	// 5400 + 1100 = 6500
	if sum1.Total.TotalTokens != 6500 {
		t.Fatalf("codex zst+jsonl total mismatch: got %d, want 6500", sum1.Total.TotalTokens)
	}
	if sum1.Meta.FilesScanned != 2 {
		t.Fatalf("expected 2 scanned files, got %+v", sum1.Meta)
	}

	// Simulate Codex compressing rollout-live.jsonl -> rollout-live.jsonl.zst with one final turn appended.
	liveContent2 := liveContent1 +
		`{"timestamp":"2026-11-20T08:05:00Z","type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":1400,"cached_input_tokens":900,"output_tokens":150,"total_tokens":1550}}}}` + "\n"
	writeZstFile(t, livePath+".zst", []byte(liveContent2))
	if err := os.Remove(livePath); err != nil {
		t.Fatal(err)
	}

	// Reload engine from disk cache and verify zero double-counting of the initial 1100 tokens!
	eng2 := New(Options{
		ClaudeDir:      filepath.Join(tmp, "claude"),
		CodexDir:       filepath.Join(tmp, "codex"),
		AgyDir:         filepath.Join(tmp, "agy"),
		CachePath:      cachePath,
		Hostname:       "test-mac",
		DisableNetwork: true,
	})
	sum2, err := eng2.Snapshot(context.Background(), true)
	if err != nil {
		t.Fatal(err)
	}
	// 5400 + 1550 = 6950 (not 5400 + 1100 + 1550!)
	if sum2.Total.TotalTokens != 6950 {
		t.Fatalf("codex transition total mismatch: got %d, want 6950", sum2.Total.TotalTokens)
	}
	if sum2.Meta.FilesCached != 1 || sum2.Meta.FilesScanned != 1 {
		t.Fatalf("expected 1 cached / 1 scanned on transition, got %+v", sum2.Meta)
	}
	for _, a := range sum2.Agents {
		if a.Agent == "codex" && !strings.Contains(a.HistoryNote, ".jsonl.zst") {
			t.Fatalf("expected codex history_note to mention .jsonl.zst, got %q", a.HistoryNote)
		}
	}
}

func TestClaudeStatsCacheOfficialBackfill(t *testing.T) {
	tmp := t.TempDir()
	claudeProjects := filepath.Join(tmp, "claude-home", "projects", "p1")
	if err := os.MkdirAll(claudeProjects, 0o755); err != nil {
		t.Fatal(err)
	}
	statsPath := filepath.Join(tmp, "claude-home", "stats-cache.json")

	// Surviving JSONL: 1 request logged twice (official /stats raw sum = 2000).
	jsonlFile := filepath.Join(claudeProjects, "s1.jsonl")
	line1 := `{"type":"assistant","timestamp":"2026-11-18T10:00:00Z","requestId":"req-1","message":{"model":"claude-opus-5-5","usage":{"input_tokens":10,"cache_creation_input_tokens":190,"cache_read_input_tokens":700,"output_tokens":100}}}` + "\n"
	if err := os.WriteFile(jsonlFile, []byte(line1+line1), 0o644); err != nil {
		t.Fatal(err)
	}

	// stats-cache.json includes opus-5-5 raw=6000 (4000 purged) + retired opus-5 raw=10000 (all purged).
	stats1 := `{
		"version": 5,
		"firstSessionDate": "2026-03-11T07:43:49.637Z",
		"lastComputedDate": "2026-11-19",
		"modelUsage": {
			"claude-opus-5-5": {"inputTokens": 60, "outputTokens": 600, "cacheReadInputTokens": 4200, "cacheCreationInputTokens": 1140},
			"claude-opus-5": {"inputTokens": 100, "outputTokens": 900, "cacheReadInputTokens": 8000, "cacheCreationInputTokens": 1000}
		}
	}`
	if err := os.WriteFile(statsPath, []byte(stats1), 0o644); err != nil {
		t.Fatal(err)
	}

	cachePath := filepath.Join(tmp, "usage-cache.json")
	eng := New(Options{
		ClaudeDir:       filepath.Join(tmp, "claude-home", "projects"),
		ClaudeStatsPath: statsPath,
		CodexDir:        filepath.Join(tmp, "codex"),
		AgyDir:          filepath.Join(tmp, "agy"),
		CachePath:       cachePath,
		Hostname:        "test-mac",
		DisableNetwork:  true,
	})

	sum1, err := eng.Snapshot(context.Background(), true)
	if err != nil {
		t.Fatal(err)
	}
	var claude1 AgentUsage
	for _, a := range sum1.Agents {
		if a.Agent == "claude" {
			claude1 = a
		}
	}
	// Surviving raw (2000) + opus-5-5 purged (4000) + opus-5 purged (10000) = 16000 (exact official /stats total).
	if claude1.Total.TotalTokens != 16000 {
		t.Fatalf("claude backfilled total mismatch: got %d, want 16000", claude1.Total.TotalTokens)
	}
	if !strings.Contains(claude1.HistoryNote, "stats-cache") || !strings.Contains(claude1.HistoryNote, "/stats") {
		t.Fatalf("unexpected claude history_note: %q", claude1.HistoryNote)
	}

	// Append a new request AFTER lastComputedDate (2026-11-25) while stats-cache has not refreshed yet (raw=+1000).
	line2 := `{"type":"assistant","timestamp":"2026-11-25T05:00:00Z","requestId":"req-2","message":{"model":"claude-opus-5-5","usage":{"input_tokens":5,"cache_creation_input_tokens":95,"cache_read_input_tokens":350,"output_tokens":50}}}` + "\n"
	f, err := os.OpenFile(jsonlFile, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = f.WriteString(line2 + line2)
	_ = f.Close()

	sum2, err := eng.Snapshot(context.Background(), true)
	if err != nil {
		t.Fatal(err)
	}
	// Must grow by exact +1000 -> 17000 (no dip before stats-cache catches up!).
	if sum2.Total.TotalTokens != 17000 {
		t.Fatalf("claude total after new post-cutoff chat mismatch: got %d, want 17000", sum2.Total.TotalTokens)
	}

	// Now simulate stats-cache updating to include 2026-11-25 (+1000 raw for opus-5-5).
	stats2 := `{
		"version": 5,
		"firstSessionDate": "2026-03-11T07:43:49.637Z",
		"lastComputedDate": "2026-11-25",
		"modelUsage": {
			"claude-opus-5-5": {"inputTokens": 70, "outputTokens": 700, "cacheReadInputTokens": 4900, "cacheCreationInputTokens": 1330},
			"claude-opus-5": {"inputTokens": 100, "outputTokens": 900, "cacheReadInputTokens": 8000, "cacheCreationInputTokens": 1000}
		}
	}`
	if err := os.WriteFile(statsPath, []byte(stats2), 0o644); err != nil {
		t.Fatal(err)
	}
	futureTime := time.Now().Add(2 * time.Second)
	_ = os.Chtimes(statsPath, futureTime, futureTime)

	sum3, err := eng.Snapshot(context.Background(), true)
	if err != nil {
		t.Fatal(err)
	}
	// Must remain 17000 with zero double-counting or drift!
	if sum3.Total.TotalTokens != 17000 {
		t.Fatalf("claude total after stats-cache refresh mismatch: got %d, want 17000", sum3.Total.TotalTokens)
	}
}

func TestAgyOfficialStatusLineQuota(t *testing.T) {
	tmp := t.TempDir()
	agyQuotaPath := filepath.Join(tmp, "agy-quota.json")
	reset5h := time.Now().UTC().Add(4 * time.Hour).Truncate(time.Second)
	resetWeekly := time.Now().UTC().Add(160 * time.Hour).Truncate(time.Second)

	payload := `{
		"plan_tier": "Pro",
		"model": {"id": "gemini-4-neon-xhigh", "display_name": "Gemini 4 Neon (XHigh)"},
		"quota": {
			"gemini-5h": {"remaining_fraction": 0.7262, "reset_time": "` + reset5h.Format(time.RFC3339) + `"},
			"gemini-weekly": {"remaining_fraction": 0.9265, "reset_time": "` + resetWeekly.Format(time.RFC3339) + `"},
			"3p-5h": {"remaining_fraction": 1.0},
			"3p-weekly": {"remaining_fraction": 1.0}
		}
	}`
	if err := RecordAgyStatusLine([]byte(payload), agyQuotaPath); err != nil {
		t.Fatalf("RecordAgyStatusLine: %v", err)
	}

	eng := New(Options{
		ClaudeDir:      filepath.Join(tmp, "claude"),
		CodexDir:       filepath.Join(tmp, "codex"),
		AgyDir:         filepath.Join(tmp, "agy"),
		AgyQuotaPath:   agyQuotaPath,
		CachePath:      filepath.Join(tmp, "usage-cache.json"),
		Hostname:       "test-mac",
		DisableNetwork: true,
	})
	sum, err := eng.Snapshot(context.Background(), true)
	if err != nil {
		t.Fatal(err)
	}
	var agy AgentUsage
	for _, a := range sum.Agents {
		if a.Agent == "agy" {
			agy = a
		}
	}
	if agy.PlanType != "Pro" {
		t.Fatalf("expected agy plan_type Pro, got %q", agy.PlanType)
	}
	if agy.Window5h.Source != "official" || agy.Window5h.UsedPercent == nil || agy.Window5h.ResetsAt != reset5h.UnixMilli() {
		t.Fatalf("unexpected agy 5h window: %+v", agy.Window5h)
	}
	if *agy.Window5h.UsedPercent < 27.3 || *agy.Window5h.UsedPercent > 27.5 {
		t.Fatalf("expected ~27.38%% used, got %.4f", *agy.Window5h.UsedPercent)
	}
	if agy.WindowWeekly.Source != "official" || agy.WindowWeekly.UsedPercent == nil || agy.WindowWeekly.ResetsAt != resetWeekly.UnixMilli() {
		t.Fatalf("unexpected agy weekly window: %+v", agy.WindowWeekly)
	}
	if !strings.Contains(agy.HistoryNote, "Claude/GPT") {
		t.Fatalf("expected secondary quota group in history_note, got %q", agy.HistoryNote)
	}
}
