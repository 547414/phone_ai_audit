package usage

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"sync"
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

func TestAgyMultiAccountQuotaAndTokenAttribution(t *testing.T) {
	tmp := t.TempDir()
	agyRoot := filepath.Join(tmp, "agy")
	agyQuotaPath := filepath.Join(tmp, "agy-quota.json")
	cachePath := filepath.Join(tmp, "usage-cache.json")

	now := time.Now().UTC()
	ts1 := now.Add(-20 * time.Minute).Format(time.RFC3339Nano)
	ts2 := now.Add(-5 * time.Minute).Format(time.RFC3339Nano)

	// Create 4 conversations:
	// - conv-alice (already exists BEFORE statusLine -> tests retroactive backfill without double count)
	// - conv-alice-sub (child subagent of conv-alice -> tests parent->child inheritance)
	// - conv-bob (attributed directly via statusLine)
	// - conv-legacy (never seen by statusLine -> stays unattributed, counted in aggregate total)
	makeTranscript := func(convID, body string) string {
		dir := filepath.Join(agyRoot, convID, ".system_generated", "logs")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		p := filepath.Join(dir, "transcript.jsonl")
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}

	alicePath := makeTranscript("conv-alice",
		`{"step_index":1,"type":"PLANNER_RESPONSE","created_at":"`+ts1+`","model":"gemini-4-neon-xhigh","input_tokens":1000,"cache_read_tokens":2000,"output_tokens":500}`+"\n")
	makeTranscript("conv-alice-sub",
		`{"step_index":1,"type":"PLANNER_RESPONSE","created_at":"`+ts1+`","model":"gemini-4-hydrogen","input_tokens":200,"cache_read_tokens":300,"output_tokens":100}`+"\n")
	// Link conv-alice-sub under conv-alice's subagents directory.
	subDir := filepath.Join(agyRoot, "conv-alice", ".system_generated", "subagents")
	if err := os.MkdirAll(subDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(subDir, "conv-alice-sub.json"), []byte(`{"conversationId":"conv-alice-sub"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	makeTranscript("conv-legacy",
		`{"step_index":1,"type":"PLANNER_RESPONSE","created_at":"`+ts1+`","model":"gemini-4-neon-xhigh","input_tokens":400,"cache_read_tokens":0,"output_tokens":100}`+"\n")

	// Seed legacy v1 agy-quota.json (no email) first, then scan once BEFORE multi-account statusLine arrives.
	legacyV1 := `{"observed_at":` + itoa64(now.Add(-time.Minute).UnixMilli()) + `,"plan_tier":"Google AI Pro","quota":{"gemini-5h":{"remaining_fraction":0.5}}}`
	if err := os.WriteFile(agyQuotaPath, []byte(legacyV1), 0o600); err != nil {
		t.Fatal(err)
	}

	eng := New(Options{
		ClaudeDir:      filepath.Join(tmp, "claude"),
		CodexDir:       filepath.Join(tmp, "codex"),
		AgyDir:         agyRoot,
		AgyQuotaPath:   agyQuotaPath,
		CachePath:      cachePath,
		Hostname:       "test-mac",
		DisableNetwork: true,
	})
	sum0, err := eng.Snapshot(context.Background(), true)
	if err != nil {
		t.Fatalf("Snapshot 0: %v", err)
	}
	// Initial total: alice(3500) + alice-sub(600) + legacy(500) = 4600
	if sum0.Total.TotalTokens != 4600 {
		t.Fatalf("initial total mismatch: got %d, want 4600", sum0.Total.TotalTokens)
	}

	// Now create conv-bob and emit statusLine payloads for BOTH Alice and Bob (different terminals & accounts).
	makeTranscript("conv-bob",
		`{"step_index":1,"type":"PLANNER_RESPONSE","created_at":"`+ts2+`","model":"gemini-4-neon-xhigh","input_tokens":700,"cache_read_tokens":1000,"output_tokens":300}`+"\n")

	aliceReset5h := now.Add(101 * time.Minute).Truncate(time.Second)
	aliceResetWk := now.Add(146 * time.Hour).Truncate(time.Second)
	bobReset5h := now.Add(23 * time.Minute).Truncate(time.Second)
	bobResetWk := now.Add(157 * time.Hour).Truncate(time.Second)

	alicePayload := `{
		"email": "Alice@Example.com",
		"conversation_id": "conv-alice",
		"plan_tier": "Google AI Pro",
		"model": {"id": "Gemini 4 Neon (XHigh)", "display_name": "Gemini 4 Neon (XHigh)"},
		"quota": {
			"gemini-5h": {"remaining_fraction": 0.117, "reset_time": "` + aliceReset5h.Format(time.RFC3339) + `", "reset_in_seconds": 6060},
			"gemini-weekly": {"remaining_fraction": 0.465, "reset_time": "` + aliceResetWk.Format(time.RFC3339) + `", "reset_in_seconds": 525600},
			"3p-5h": {"remaining_fraction": 1.0, "reset_time": "1970-01-01T00:00:00Z"},
			"3p-weekly": {"remaining_fraction": 1.0, "reset_time": "1970-01-01T00:00:00Z"}
		}
	}`
	bobPayload := `{
		"email": "bob@example.com",
		"transcript_path": "` + filepath.ToSlash(filepath.Join(agyRoot, "conv-bob", ".system_generated", "logs", "transcript.jsonl")) + `",
		"plan_tier": "Google AI Ultra",
		"model": {"id": "Gemini 4 Neon (XHigh)", "display_name": "Gemini 4 Neon (XHigh)"},
		"quota": {
			"gemini-5h": {"remaining_fraction": 0.5646, "reset_time": "` + bobReset5h.Format(time.RFC3339) + `", "reset_in_seconds": 1380},
			"gemini-weekly": {"remaining_fraction": 0.8782, "reset_time": "` + bobResetWk.Format(time.RFC3339) + `", "reset_in_seconds": 565200},
			"3p-5h": {"remaining_fraction": 1.0, "reset_time": "1970-01-01T00:00:00Z"},
			"3p-weekly": {"remaining_fraction": 1.0, "reset_time": "1970-01-01T00:00:00Z"}
		}
	}`
	// Ensure distinct mod time for agy-quota.json.
	if err := RecordAgyStatusLine([]byte(alicePayload), agyQuotaPath); err != nil {
		t.Fatalf("RecordAgyStatusLine alice: %v", err)
	}
	if err := RecordAgyStatusLine([]byte(bobPayload), agyQuotaPath); err != nil {
		t.Fatalf("RecordAgyStatusLine bob: %v", err)
	}
	futureTime := time.Now().Add(2 * time.Second)
	_ = os.Chtimes(agyQuotaPath, futureTime, futureTime)

	sum1, err := eng.Snapshot(context.Background(), true)
	if err != nil {
		t.Fatalf("Snapshot 1: %v", err)
	}

	var agy AgentUsage
	for _, a := range sum1.Agents {
		if a.Agent == "agy" {
			agy = a
		}
	}

	// Aggregate total: 4600 + bob(2000) = 6600 (zero double counting from retroactive backfill!)
	if agy.Total.TotalTokens != 6600 {
		t.Fatalf("agy aggregate total mismatch: got %d, want 6600", agy.Total.TotalTokens)
	}
	if len(agy.Accounts) != 2 {
		t.Fatalf("expected 2 accounts (legacy v1 default dropped), got %d: %+v", len(agy.Accounts), agy.Accounts)
	}

	alice := agy.Accounts[0]
	bob := agy.Accounts[1]
	if alice.Account != "alice@example.com" || bob.Account != "bob@example.com" {
		t.Fatalf("unexpected sorted accounts: %q, %q", alice.Account, bob.Account)
	}

	// Verify Alice's quota & reset times did NOT get overwritten by Bob!
	if alice.Window5h.Source != "official" || alice.Window5h.UsedPercent == nil || math.Abs(*alice.Window5h.UsedPercent-88.3) > 0.05 {
		t.Fatalf("alice 5h mismatch: %+v", alice.Window5h)
	}
	if alice.Window5h.ResetsAt != aliceReset5h.UnixMilli() {
		t.Fatalf("alice 5h reset mismatch: got %d want %d", alice.Window5h.ResetsAt, aliceReset5h.UnixMilli())
	}
	if alice.WindowWeekly.UsedPercent == nil || math.Abs(*alice.WindowWeekly.UsedPercent-53.5) > 0.05 {
		t.Fatalf("alice weekly mismatch: %+v", alice.WindowWeekly)
	}
	if alice.WindowWeekly.ResetsAt != aliceResetWk.UnixMilli() {
		t.Fatalf("alice weekly reset mismatch: got %d want %d", alice.WindowWeekly.ResetsAt, aliceResetWk.UnixMilli())
	}
	// Alice tokens: conv-alice (3500) + inherited conv-alice-sub (600) = 4100
	if alice.Total.TotalTokens != 4100 {
		t.Fatalf("alice total tokens mismatch (with subagent inheritance): got %d, want 4100", alice.Total.TotalTokens)
	}

	// Verify Bob's quota & reset times!
	if bob.PlanType != "Google AI Ultra" {
		t.Fatalf("bob plan mismatch: got %q", bob.PlanType)
	}
	if bob.Window5h.UsedPercent == nil || math.Abs(*bob.Window5h.UsedPercent-43.54) > 0.05 {
		t.Fatalf("bob 5h mismatch: %+v", bob.Window5h)
	}
	if bob.Window5h.ResetsAt != bobReset5h.UnixMilli() {
		t.Fatalf("bob 5h reset mismatch: got %d want %d", bob.Window5h.ResetsAt, bobReset5h.UnixMilli())
	}
	if bob.WindowWeekly.UsedPercent == nil || math.Abs(*bob.WindowWeekly.UsedPercent-12.18) > 0.05 {
		t.Fatalf("bob weekly mismatch: %+v", bob.WindowWeekly)
	}
	if bob.WindowWeekly.ResetsAt != bobResetWk.UnixMilli() {
		t.Fatalf("bob weekly reset mismatch: got %d want %d", bob.WindowWeekly.ResetsAt, bobResetWk.UnixMilli())
	}
	if bob.Total.TotalTokens != 2000 {
		t.Fatalf("bob total tokens mismatch: got %d, want 2000", bob.Total.TotalTokens)
	}
	if !strings.Contains(agy.HistoryNote, "1 个历史会话") {
		t.Fatalf("expected unattributed legacy history note, got %q", agy.HistoryNote)
	}

	// Append a new turn to Alice's transcript and reload engine from disk cache.
	f, err := os.OpenFile(alicePath, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = f.WriteString(`{"step_index":2,"type":"PLANNER_RESPONSE","created_at":"` + ts2 + `","model":"gemini-4-neon-xhigh","input_tokens":300,"cache_read_tokens":0,"output_tokens":100}` + "\n")
	_ = f.Close()

	eng2 := New(Options{
		ClaudeDir:      filepath.Join(tmp, "claude"),
		CodexDir:       filepath.Join(tmp, "codex"),
		AgyDir:         agyRoot,
		AgyQuotaPath:   agyQuotaPath,
		CachePath:      cachePath,
		Hostname:       "test-mac",
		DisableNetwork: true,
	})
	sum2, err := eng2.Snapshot(context.Background(), true)
	if err != nil {
		t.Fatalf("Snapshot 2: %v", err)
	}
	for _, a := range sum2.Agents {
		if a.Agent == "agy" {
			if a.Total.TotalTokens != 7000 {
				t.Fatalf("post-append aggregate total mismatch: got %d want 7000", a.Total.TotalTokens)
			}
			if len(a.Accounts) != 2 || a.Accounts[0].Total.TotalTokens != 4500 || a.Accounts[1].Total.TotalTokens != 2000 {
				t.Fatalf("post-append per-account mismatch: %+v", a.Accounts)
			}
		}
	}
}

func TestAgyStatusLineConcurrentWrites(t *testing.T) {
	tmp := t.TempDir()
	outPath := filepath.Join(tmp, "agy-quota.json")
	reset := time.Now().UTC().Add(3 * time.Hour).Truncate(time.Second).Format(time.RFC3339)

	var wg sync.WaitGroup
	errs := make(chan error, 32)
	for i := 0; i < 4; i++ {
		acctIdx := i
		for j := 0; j < 4; j++ {
			step := j
			wg.Add(1)
			go func() {
				defer wg.Done()
				rem := 0.9 - float64(acctIdx)*0.1 - float64(step)*0.01
				payload := fmt.Sprintf(`{"email":"user%d@example.com","conversation_id":"conv-%d","plan_tier":"Pro","quota":{"gemini-5h":{"remaining_fraction":%.4f,"reset_time":"%s"}}}`,
					acctIdx, acctIdx, rem, reset)
				if err := RecordAgyStatusLine([]byte(payload), outPath); err != nil {
					errs <- err
				}
			}()
		}
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatalf("concurrent RecordAgyStatusLine error: %v", err)
	}

	raw, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatalf("read agy-quota.json: %v", err)
	}
	var qf AgyQuotaFile
	if err := json.Unmarshal(raw, &qf); err != nil {
		t.Fatalf("unmarshal agy-quota.json: %v", err)
	}
	if len(qf.Accounts) != 4 {
		t.Fatalf("expected all 4 accounts preserved under concurrent writes, got %d: %+v", len(qf.Accounts), qf.Accounts)
	}
	if len(qf.Conversations) != 4 {
		t.Fatalf("expected all 4 conversation mappings preserved, got %d: %+v", len(qf.Conversations), qf.Conversations)
	}
}

func TestAgyUsageAuthExpiryEnrichment(t *testing.T) {
	tmp := t.TempDir()
	agyQuotaPath := filepath.Join(tmp, "agy-quota.json")
	agyProfilesPath := filepath.Join(tmp, "agy-profiles.json")
	now := time.Now().UTC()
	reset := now.Add(2 * time.Hour).Truncate(time.Second).Format(time.RFC3339)

	// Record 2 accounts via statusLine.
	_ = RecordAgyStatusLine([]byte(`{"email":"alice@example.com","plan_tier":"Pro","quota":{"gemini-5h":{"remaining_fraction":0.8,"reset_time":"`+reset+`"}}}`), agyQuotaPath)
	_ = RecordAgyStatusLine([]byte(`{"email":"bob@example.com","plan_tier":"Pro","quota":{"gemini-5h":{"remaining_fraction":0.6,"reset_time":"`+reset+`"}}}`), agyQuotaPath)

	// Write profiles metadata: Alice expiring in 6h (soon!), Bob already expired 2h ago!
	profilesJSON := fmt.Sprintf(`{
		"version": 2,
		"profiles": [
			{"name":"main","email":"alice@example.com","saved_at":%d,"custom_ttl_ms":%d},
			{"name":"work","email":"bob@example.com","saved_at":%d,"custom_ttl_ms":%d}
		]
	}`,
		now.Add(-18*time.Hour).UnixMilli(), (24 * time.Hour).Milliseconds(),
		now.Add(-26*time.Hour).UnixMilli(), (24 * time.Hour).Milliseconds(),
	)
	if err := os.WriteFile(agyProfilesPath, []byte(profilesJSON), 0o600); err != nil {
		t.Fatal(err)
	}

	eng := New(Options{
		ClaudeDir:       filepath.Join(tmp, "claude"),
		CodexDir:        filepath.Join(tmp, "codex"),
		AgyDir:          filepath.Join(tmp, "agy"),
		AgyQuotaPath:    agyQuotaPath,
		AgyProfilesPath: agyProfilesPath,
		CachePath:       filepath.Join(tmp, "usage-cache.json"),
		Hostname:        "test-mac",
		DisableNetwork:  true,
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
	if len(agy.Accounts) != 2 {
		t.Fatalf("expected 2 accounts, got %d", len(agy.Accounts))
	}
	alice, bob := agy.Accounts[0], agy.Accounts[1]
	if alice.AuthProfile != "main" || !alice.AuthExpiringSoon || alice.AuthExpired {
		t.Fatalf("expected alice expiring soon, got %+v", alice)
	}
	if bob.AuthProfile != "work" || !bob.AuthExpired || !strings.Contains(bob.AuthExpireNote, "ai-audit agy login work") {
		t.Fatalf("expected bob expired with login hint, got %+v", bob)
	}
}
