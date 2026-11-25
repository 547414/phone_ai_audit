package usage

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/klauspost/compress/zstd"
)

const cacheSchemaVersion = 3

type bucket5m struct {
	SlotStartMs int64      `json:"s"`
	EarliestMs  int64      `json:"e"`
	Model       string     `json:"m"`
	Stats       TokenStats `json:"t"`
}

type fileCursor struct {
	Agent        string                `json:"agent"`
	Size         int64                 `json:"size"`
	ModUnixNano  int64                 `json:"mod_ns"`
	Offset       int64                 `json:"offset"`
	Compressed   bool                  `json:"compressed,omitempty"`
	CurrentModel string                `json:"model,omitempty"`
	PrevTotal    TokenStats            `json:"prev_total,omitempty"`
	MaxStepIndex int                   `json:"max_step,omitempty"`
	SeenReqOrder []string              `json:"seen_req_order,omitempty"`
	SeenReq      map[string]TokenStats `json:"seen_req,omitempty"`
}

type claudeStatsSnapshot struct {
	ModUnixNano       int64                 `json:"mod_ns"`
	Size              int64                 `json:"size"`
	FirstSessionDate  string                `json:"first_session_date,omitempty"`
	LastComputedDate  string                `json:"last_computed_date,omitempty"`
	OfficialTotal     int64                 `json:"official_total,omitempty"`
	BackfillByModel   map[string]TokenStats `json:"backfill_by_model,omitempty"`
	OfficialRawModels map[string]TokenStats `json:"official_raw_models,omitempty"`
}

type persistentCache struct {
	Version        int                                       `json:"version"`
	Days           map[string]map[string]map[string]TokenStats `json:"days"`                      // date(YYYY-MM-DD local) -> agent -> model -> TokenStats
	ClaudeRawDays  map[string]map[string]TokenStats          `json:"claude_raw_days,omitempty"` // date(YYYY-MM-DD UTC) -> model -> raw un-deduped TokenStats
	ClaudeStats    *claudeStatsSnapshot                      `json:"claude_stats,omitempty"`
	Recent5m       map[string]map[string]*bucket5m            `json:"recent_5m"`                 // agent -> key(slot|model) -> bucket5m (last 8 days)
	Files          map[string]*fileCursor                     `json:"files"`                     // canonicalPath -> cursor
	LastActive     map[string]int64                           `json:"last_active"`               // agent -> unix ms
	CodexRateLimit *officialQuotaSnapshot                     `json:"codex_rl,omitempty"`
	AgyQuota       *officialQuotaSnapshot                     `json:"agy_quota,omitempty"`
	AgyQuotaExtra  string                                     `json:"agy_quota_extra,omitempty"`
	AgyQuotaModNs  int64                                      `json:"agy_quota_mod_ns,omitempty"`
	AgyQuotaSize   int64                                      `json:"agy_quota_size,omitempty"`
}

type Engine struct {
	opts       Options
	httpClient *http.Client

	mu             sync.Mutex
	cache          *persistentCache
	loaded         bool
	dirty          bool
	lastSummary    *Summary
	lastSummaryAt  time.Time
	lastOAuthAt    time.Time
	claudeLive     *officialQuotaSnapshot
	codexLive      *officialQuotaSnapshot

	oauthMu           sync.Mutex
	keychainCheckedAt time.Time
	keychainMissing   bool
}

func New(opts Options) *Engine {
	home, _ := os.UserHomeDir()
	if opts.ClaudeDir == "" {
		opts.ClaudeDir = filepath.Join(home, ".claude", "projects")
	}
	if opts.ClaudeStatsPath == "" && opts.ClaudeDir != "" {
		opts.ClaudeStatsPath = filepath.Join(filepath.Dir(opts.ClaudeDir), "stats-cache.json")
	}
	if opts.CodexDir == "" {
		opts.CodexDir = filepath.Join(home, ".codex", "sessions")
	}
	if opts.AgyDir == "" {
		opts.AgyDir = filepath.Join(home, ".gemini", "antigravity-cli", "brain")
	}
	if opts.CachePath == "" {
		opts.CachePath = filepath.Join(home, ".config", "ai-audit", "usage-cache.json")
	}
	if opts.AgyQuotaPath == "" && opts.CachePath != "" {
		opts.AgyQuotaPath = filepath.Join(filepath.Dir(opts.CachePath), "agy-quota.json")
	}
	if opts.Hostname == "" {
		opts.Hostname, _ = os.Hostname()
	}
	return &Engine{
		opts:       opts,
		httpClient: buildUsageHTTPClient(opts.ProxyURL),
	}
}

// StartBackground runs a delayed cold-start warmup and a low-frequency 5-minute
// incremental archive pass so historical totals survive upstream log rotation.
func (e *Engine) StartBackground(ctx context.Context) {
	go func() {
		select {
		case <-ctx.Done():
			return
		case <-time.After(2 * time.Second):
		}
		_, _ = e.Snapshot(ctx, false)

		ticker := time.NewTicker(5 * time.Minute)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				_, _ = e.Snapshot(ctx, false)
			}
		}
	}()
}

// Snapshot returns the current usage summary. Unless force is true, results
// computed within the last 5 seconds are served straight from memory.
func (e *Engine) Snapshot(ctx context.Context, force bool) (Summary, error) {
	e.mu.Lock()
	defer e.mu.Unlock()

	now := time.Now()
	if !force && e.lastSummary != nil && now.Sub(e.lastSummaryAt) < 5*time.Second {
		cp := *e.lastSummary
		return cp, nil
	}

	if !e.loaded {
		e.loadCacheLocked()
	}

	scanStart := time.Now()
	stats := e.scanAllLocked(now)
	scanMs := time.Since(scanStart).Milliseconds()

	// Refresh optional live OAuth quotas at most once per 90s (or on force refresh).
	if !e.opts.DisableNetwork && (force || e.lastOAuthAt.IsZero() || now.Sub(e.lastOAuthAt) >= 90*time.Second) {
		e.lastOAuthAt = now
		// Release lock during short network calls? Keep timeout short (<=5s) and run in parallel.
		var wg sync.WaitGroup
		var claudeQ, codexQ *officialQuotaSnapshot
		wg.Add(2)
		go func() {
			defer wg.Done()
			claudeQ = e.fetchClaudeOAuthQuota(ctx)
		}()
		go func() {
			defer wg.Done()
			codexQ = e.fetchCodexLiveQuota(ctx)
		}()
		wg.Wait()
		if claudeQ != nil {
			e.claudeLive = claudeQ
		}
		if codexQ != nil {
			e.codexLive = codexQ
			if e.cache.CodexRateLimit == nil || codexQ.ObservedAt >= e.cache.CodexRateLimit.ObservedAt {
				e.cache.CodexRateLimit = codexQ
				e.dirty = true
			}
		}
	}

	e.pruneRecentLocked(now)
	if e.dirty {
		e.saveCacheLocked()
		e.dirty = false
	}

	summary := e.buildSummaryLocked(now, stats, scanMs)
	e.lastSummary = &summary
	e.lastSummaryAt = now
	return summary, nil
}

func newEmptyCache() *persistentCache {
	return &persistentCache{
		Version:       cacheSchemaVersion,
		Days:          make(map[string]map[string]map[string]TokenStats),
		ClaudeRawDays: make(map[string]map[string]TokenStats),
		Recent5m:      make(map[string]map[string]*bucket5m),
		Files:         make(map[string]*fileCursor),
		LastActive:    make(map[string]int64),
	}
}

func (e *Engine) loadCacheLocked() {
	e.loaded = true
	e.cache = newEmptyCache()
	b, err := os.ReadFile(e.opts.CachePath)
	if err != nil {
		return
	}
	var c persistentCache
	if err := json.Unmarshal(b, &c); err != nil || c.Version != cacheSchemaVersion {
		return
	}
	if c.Days == nil {
		c.Days = make(map[string]map[string]map[string]TokenStats)
	}
	if c.ClaudeRawDays == nil {
		c.ClaudeRawDays = make(map[string]map[string]TokenStats)
	}
	if c.Recent5m == nil {
		c.Recent5m = make(map[string]map[string]*bucket5m)
	}
	if c.Files == nil {
		c.Files = make(map[string]*fileCursor)
	}
	if c.LastActive == nil {
		c.LastActive = make(map[string]int64)
	}
	e.cache = &c
}

func (e *Engine) saveCacheLocked() {
	if e.opts.CachePath == "" {
		return
	}
	if err := os.MkdirAll(filepath.Dir(e.opts.CachePath), 0o700); err != nil {
		return
	}
	b, err := json.Marshal(e.cache)
	if err != nil {
		return
	}
	tmp := e.opts.CachePath + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return
	}
	_ = os.Rename(tmp, e.opts.CachePath)
}

type scanCounters struct {
	filesTotal    int
	filesScanned  int
	filesCached   int
	codexZstFiles int
	agentFiles    map[string]int
	agentScanned  map[string]int
	agentBytes    map[string]int64
	installed     map[string]bool
}

func (e *Engine) scanAllLocked(now time.Time) scanCounters {
	sc := scanCounters{
		agentFiles:   make(map[string]int),
		agentScanned: make(map[string]int),
		agentBytes:   make(map[string]int64),
		installed:    make(map[string]bool),
	}

	e.scanDirLocked("claude", e.opts.ClaudeDir, func(name string) bool {
		return strings.HasSuffix(name, ".jsonl")
	}, &sc)
	e.syncClaudeStatsCacheLocked(sc.agentScanned["claude"] > 0)

	e.scanCodexDirLocked(e.opts.CodexDir, &sc)

	e.scanDirLocked("agy", e.opts.AgyDir, func(name string) bool {
		return name == "transcript.jsonl"
	}, &sc)
	e.syncAgyQuotaLocked()

	return sc
}

func (e *Engine) syncAgyQuotaLocked() {
	if e.opts.AgyQuotaPath == "" {
		return
	}
	fi, err := os.Stat(e.opts.AgyQuotaPath)
	if err != nil || fi.IsDir() {
		return
	}
	modNs := fi.ModTime().UnixNano()
	size := fi.Size()
	if e.cache.AgyQuota != nil && e.cache.AgyQuotaModNs == modNs && e.cache.AgyQuotaSize == size {
		return
	}
	b, err := os.ReadFile(e.opts.AgyQuotaPath)
	if err != nil {
		return
	}
	var qf AgyQuotaFile
	if err := json.Unmarshal(b, &qf); err != nil {
		return
	}
	snap, extra := convertAgyQuotaFile(&qf)
	if snap == nil {
		return
	}
	e.cache.AgyQuota = snap
	e.cache.AgyQuotaExtra = extra
	e.cache.AgyQuotaModNs = modNs
	e.cache.AgyQuotaSize = size
	e.dirty = true
}

func (e *Engine) scanDirLocked(agent, root string, match func(base string) bool, sc *scanCounters) {
	if root == "" {
		return
	}
	info, err := os.Stat(root)
	if err != nil || !info.IsDir() {
		return
	}
	sc.installed[agent] = true

	_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		if !match(d.Name()) {
			return nil
		}
		fi, err := d.Info()
		if err != nil {
			return nil
		}
		size := fi.Size()
		modNs := fi.ModTime().UnixNano()
		sc.filesTotal++
		sc.agentFiles[agent]++
		sc.agentBytes[agent] += size

		cur, ok := e.cache.Files[path]
		if ok && cur.Size == size && cur.ModUnixNano == modNs {
			sc.filesCached++
			return nil
		}

		// File is new or grew.
		if !ok {
			cur = &fileCursor{Agent: agent, MaxStepIndex: -1}
			e.cache.Files[path] = cur
		}
		if size < cur.Offset {
			// File was truncated/rewritten: restart offset, preserve dedup markers where sensible.
			cur.Offset = 0
			cur.PrevTotal = TokenStats{}
			cur.MaxStepIndex = -1
		}

		if e.scanFileLocked(agent, path, cur) {
			sc.filesScanned++
			sc.agentScanned[agent]++
			cur.Size = size
			cur.ModUnixNano = modNs
			e.dirty = true
		}
		return nil
	})
}

type codexFileEntry struct {
	path  string
	canon string
	isZst bool
	size  int64
	modNs int64
}

func (e *Engine) scanCodexDirLocked(root string, sc *scanCounters) {
	if root == "" {
		return
	}
	info, err := os.Stat(root)
	if err != nil || !info.IsDir() {
		return
	}
	sc.installed["codex"] = true

	// Deduplicate by canonical path (stripping ".zst") so that when Codex
	// compresses a rollout from .jsonl to .jsonl.zst (or if both briefly coexist),
	// we preserve cur.PrevTotal and never double-count tokens.
	byCanon := make(map[string]codexFileEntry)
	_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		name := d.Name()
		isZst := strings.HasSuffix(name, ".jsonl.zst")
		if !isZst && !strings.HasSuffix(name, ".jsonl") {
			return nil
		}
		fi, err := d.Info()
		if err != nil {
			return nil
		}
		canon := path
		if isZst {
			canon = strings.TrimSuffix(path, ".zst")
		}
		if prev, exists := byCanon[canon]; exists && !prev.isZst && isZst {
			return nil
		}
		byCanon[canon] = codexFileEntry{
			path:  path,
			canon: canon,
			isZst: isZst,
			size:  fi.Size(),
			modNs: fi.ModTime().UnixNano(),
		}
		return nil
	})

	entries := make([]codexFileEntry, 0, len(byCanon))
	for _, ent := range byCanon {
		entries = append(entries, ent)
	}
	sort.Slice(entries, func(i, j int) bool {
		return entries[i].canon < entries[j].canon
	})

	var zstdDec *zstd.Decoder
	defer func() {
		if zstdDec != nil {
			zstdDec.Close()
		}
	}()

	for _, ent := range entries {
		sc.filesTotal++
		sc.agentFiles["codex"]++
		sc.agentBytes["codex"] += ent.size
		if ent.isZst {
			sc.codexZstFiles++
		}

		cur, ok := e.cache.Files[ent.canon]
		if ok && cur.Compressed == ent.isZst && cur.Size == ent.size && cur.ModUnixNano == ent.modNs {
			sc.filesCached++
			continue
		}
		if !ok {
			cur = &fileCursor{Agent: "codex", MaxStepIndex: -1}
			e.cache.Files[ent.canon] = cur
		}

		var scanned bool
		if ent.isZst {
			if zstdDec == nil {
				dec, err := zstd.NewReader(nil, zstd.WithDecoderConcurrency(1))
				if err != nil {
					continue
				}
				zstdDec = dec
			}
			scanned = e.scanCodexZstFileLocked(ent.path, cur, zstdDec)
			if scanned {
				cur.Compressed = true
				cur.Offset = 0
			}
		} else {
			if cur.Compressed {
				cur.Offset = 0
				cur.Compressed = false
			} else if ent.size < cur.Offset {
				cur.Offset = 0
				cur.PrevTotal = TokenStats{}
				cur.MaxStepIndex = -1
			}
			scanned = e.scanFileLocked("codex", ent.path, cur)
			if scanned {
				cur.Compressed = false
			}
		}

		if scanned {
			sc.filesScanned++
			sc.agentScanned["codex"]++
			cur.Size = ent.size
			cur.ModUnixNano = ent.modNs
			e.dirty = true
		}
	}
}

func (e *Engine) scanCodexZstFileLocked(path string, cur *fileCursor, dec *zstd.Decoder) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()

	if err := dec.Reset(f); err != nil {
		return false
	}
	reader := bufio.NewReaderSize(dec, 256*1024)
	for {
		line, err := reader.ReadBytes('\n')
		if len(line) > 0 {
			trimmed := bytes.TrimSpace(line)
			if len(trimmed) > 0 {
				e.parseCodexLineLocked(trimmed, cur)
			}
		}
		if err != nil {
			if !errors.Is(err, io.EOF) {
				return false
			}
			break
		}
	}
	return true
}

func (e *Engine) syncClaudeStatsCacheLocked(anyClaudeScanned bool) {
	if e.opts.ClaudeStatsPath == "" {
		return
	}
	fi, err := os.Stat(e.opts.ClaudeStatsPath)
	if err != nil || fi.IsDir() {
		return
	}
	modNs := fi.ModTime().UnixNano()
	size := fi.Size()

	snap := e.cache.ClaudeStats
	needReload := snap == nil || snap.ModUnixNano != modNs || snap.Size != size
	if !needReload && !anyClaudeScanned {
		return
	}

	if needReload {
		b, err := os.ReadFile(e.opts.ClaudeStatsPath)
		if err != nil {
			return
		}
		var raw struct {
			FirstSessionDate string `json:"firstSessionDate"`
			LastComputedDate string `json:"lastComputedDate"`
			ModelUsage       map[string]struct {
				InputTokens              int64 `json:"inputTokens"`
				OutputTokens             int64 `json:"outputTokens"`
				CacheReadInputTokens     int64 `json:"cacheReadInputTokens"`
				CacheCreationInputTokens int64 `json:"cacheCreationInputTokens"`
			} `json:"modelUsage"`
		}
		if err := json.Unmarshal(b, &raw); err != nil {
			return
		}
		firstDate := strings.TrimSpace(raw.FirstSessionDate)
		if len(firstDate) >= 10 {
			firstDate = firstDate[:10]
		}
		officialModels := make(map[string]TokenStats, len(raw.ModelUsage))
		var officialTotal int64
		for model, u := range raw.ModelUsage {
			m := strings.TrimSpace(model)
			if m == "" || m == "<synthetic>" {
				continue
			}
			st := TokenStats{
				InputTokens:      max64(0, u.InputTokens),
				CacheReadTokens:  max64(0, u.CacheReadInputTokens),
				CacheWriteTokens: max64(0, u.CacheCreationInputTokens),
				OutputTokens:     max64(0, u.OutputTokens),
			}
			st.TotalTokens = st.InputTokens + st.CacheReadTokens + st.CacheWriteTokens + st.OutputTokens
			if st.TotalTokens == 0 {
				continue
			}
			officialModels[m] = st
			officialTotal += st.TotalTokens
		}
		snap = &claudeStatsSnapshot{
			ModUnixNano:       modNs,
			Size:              size,
			FirstSessionDate:  firstDate,
			LastComputedDate:  strings.TrimSpace(raw.LastComputedDate),
			OfficialTotal:     officialTotal,
			OfficialRawModels: officialModels,
		}
		e.cache.ClaudeStats = snap
	}

	if snap == nil || len(snap.OfficialRawModels) == 0 {
		e.dirty = true
		return
	}

	// Match Claude Code's official /stats (stats-cache.json) raw counting directly:
	// only subtract .jsonl raw totals up through stats-cache's lastComputedDate so
	// newer conversations never cause a temporary backfill dip before stats-cache updates.
	knownRaw := make(map[string]TokenStats)
	cutoff := snap.LastComputedDate
	for utcDate, byModel := range e.cache.ClaudeRawDays {
		if cutoff != "" && utcDate > cutoff {
			continue
		}
		for m, st := range byModel {
			cur := knownRaw[m]
			cur.Add(st)
			knownRaw[m] = cur
		}
	}

	backfill := make(map[string]TokenStats)
	for m, off := range snap.OfficialRawModels {
		known := knownRaw[m]
		miss := TokenStats{
			InputTokens:      max64(0, off.InputTokens-known.InputTokens),
			CacheReadTokens:  max64(0, off.CacheReadTokens-known.CacheReadTokens),
			CacheWriteTokens: max64(0, off.CacheWriteTokens-known.CacheWriteTokens),
			OutputTokens:     max64(0, off.OutputTokens-known.OutputTokens),
		}
		miss.TotalTokens = miss.InputTokens + miss.CacheReadTokens + miss.CacheWriteTokens + miss.OutputTokens
		if miss.TotalTokens > 0 {
			backfill[m] = miss
		}
	}
	snap.BackfillByModel = backfill
	e.dirty = true
}

func (e *Engine) scanFileLocked(agent, path string, cur *fileCursor) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()

	if cur.Offset > 0 {
		if _, err := f.Seek(cur.Offset, io.SeekStart); err != nil {
			cur.Offset = 0
		}
	}

	reader := bufio.NewReaderSize(f, 256*1024)
	pos := cur.Offset

	for {
		line, err := reader.ReadBytes('\n')
		if len(line) > 0 {
			// If we hit EOF without a trailing newline, wait until the writer flushes the full line.
			if errors.Is(err, io.EOF) && line[len(line)-1] != '\n' {
				break
			}
			pos += int64(len(line))
			trimmed := bytes.TrimSpace(line)
			if len(trimmed) > 0 {
				switch agent {
				case "claude":
					e.parseClaudeLineLocked(trimmed, cur)
				case "codex":
					e.parseCodexLineLocked(trimmed, cur)
				case "agy":
					e.parseAgyLineLocked(trimmed, cur)
				}
			}
		}
		if err != nil {
			break
		}
	}

	cur.Offset = pos
	return true
}

func (e *Engine) parseClaudeLineLocked(line []byte, cur *fileCursor) {
	// Fast substring pre-filter avoids JSON unmarshaling 95%+ of lines (tool outputs, user inputs, diffs).
	if !bytes.Contains(line, []byte(`"usage"`)) || !bytes.Contains(line, []byte(`"input_tokens"`)) {
		return
	}
	var entry struct {
		Type              string `json:"type"`
		Timestamp         string `json:"timestamp"`
		RequestID         string `json:"requestId"`
		IsApiErrorMessage bool   `json:"isApiErrorMessage"`
		Message           *struct {
			ID    string `json:"id"`
			Model string `json:"model"`
			Usage *struct {
				InputTokens              int64 `json:"input_tokens"`
				OutputTokens             int64 `json:"output_tokens"`
				CacheCreationInputTokens int64 `json:"cache_creation_input_tokens"`
				CacheReadInputTokens     int64 `json:"cache_read_input_tokens"`
			} `json:"usage"`
		} `json:"message"`
	}
	if err := json.Unmarshal(line, &entry); err != nil {
		return
	}
	if entry.IsApiErrorMessage || entry.Message == nil || entry.Message.Usage == nil {
		return
	}
	model := strings.TrimSpace(entry.Message.Model)
	if model == "" || model == "<synthetic>" {
		return
	}
	u := entry.Message.Usage
	stats := TokenStats{
		InputTokens:      max64(0, u.InputTokens),
		CacheReadTokens:  max64(0, u.CacheReadInputTokens),
		CacheWriteTokens: max64(0, u.CacheCreationInputTokens),
		OutputTokens:     max64(0, u.OutputTokens),
		Requests:         1,
	}
	stats.TotalTokens = stats.InputTokens + stats.CacheReadTokens + stats.CacheWriteTokens + stats.OutputTokens
	if stats.TotalTokens == 0 {
		return
	}

	tsMs := parseISOToMillis(entry.Timestamp)
	if tsMs == 0 {
		return
	}

	// Track raw un-deduplicated line totals (bucketed by UTC date to match Claude's
	// stats-cache.json) so we can cleanly subtract surviving .jsonl coverage and
	// backfill purged history from stats-cache.json without double-counting.
	utcDate := time.UnixMilli(tsMs).UTC().Format("2006-01-02")
	if e.cache.ClaudeRawDays[utcDate] == nil {
		e.cache.ClaudeRawDays[utcDate] = make(map[string]TokenStats)
	}
	rawEx := e.cache.ClaudeRawDays[utcDate][model]
	rawEx.Add(stats)
	e.cache.ClaudeRawDays[utcDate][model] = rawEx

	// Follow Claude Code's official /stats (stats-cache.json) un-deduplicated token counting,
	// while still deduplicating the request counter by requestId / message.id.
	reqKey := strings.TrimSpace(entry.RequestID)
	if reqKey == "" {
		reqKey = strings.TrimSpace(entry.Message.ID)
	}
	if reqKey != "" {
		if cur.SeenReq == nil {
			cur.SeenReq = make(map[string]TokenStats)
		}
		if _, exists := cur.SeenReq[reqKey]; exists {
			stats.Requests = 0
		} else {
			cur.SeenReq[reqKey] = TokenStats{Requests: 1}
			cur.SeenReqOrder = append(cur.SeenReqOrder, reqKey)
			if len(cur.SeenReqOrder) > 64 {
				evict := cur.SeenReqOrder[0]
				cur.SeenReqOrder = cur.SeenReqOrder[1:]
				delete(cur.SeenReq, evict)
			}
		}
	}

	e.recordEventLocked("claude", model, tsMs, stats)
}

func (e *Engine) parseCodexLineLocked(line []byte, cur *fileCursor) {
	isTurnCtx := bytes.Contains(line, []byte(`"turn_context"`))
	isTokenCount := bytes.Contains(line, []byte(`"token_count"`))
	if !isTurnCtx && !isTokenCount {
		return
	}

	if isTurnCtx {
		var tc struct {
			Type    string `json:"type"`
			Payload struct {
				Model string `json:"model"`
			} `json:"payload"`
		}
		if err := json.Unmarshal(line, &tc); err == nil && tc.Type == "turn_context" {
			if m := strings.TrimSpace(tc.Payload.Model); m != "" {
				cur.CurrentModel = m
			}
		}
		if !isTokenCount {
			return
		}
	}

	var ev struct {
		Timestamp string `json:"timestamp"`
		Payload   struct {
			Type string `json:"type"`
			Info *struct {
				TotalTokenUsage *struct {
					InputTokens           int64 `json:"input_tokens"`
					CachedInputTokens     int64 `json:"cached_input_tokens"`
					CacheWriteInputTokens int64 `json:"cache_write_input_tokens"`
					OutputTokens          int64 `json:"output_tokens"`
					TotalTokens           int64 `json:"total_tokens"`
				} `json:"total_token_usage"`
			} `json:"info"`
			RateLimits *struct {
				PlanType string `json:"plan_type"`
				Primary  *struct {
					UsedPercent   float64 `json:"used_percent"`
					WindowMinutes int     `json:"window_minutes"`
					ResetsAt      int64   `json:"resets_at"`
				} `json:"primary"`
				Secondary *struct {
					UsedPercent   float64 `json:"used_percent"`
					WindowMinutes int     `json:"window_minutes"`
					ResetsAt      int64   `json:"resets_at"`
				} `json:"secondary"`
			} `json:"rate_limits"`
		} `json:"payload"`
	}
	if err := json.Unmarshal(line, &ev); err != nil || ev.Payload.Type != "token_count" {
		return
	}

	tsMs := parseISOToMillis(ev.Timestamp)
	if tsMs == 0 {
		return
	}

	// Update newest official Codex rate_limits snapshot.
	if rl := ev.Payload.RateLimits; rl != nil && (rl.Primary != nil || rl.Secondary != nil) {
		if e.cache.CodexRateLimit == nil || tsMs >= e.cache.CodexRateLimit.ObservedAt {
			snap := &officialQuotaSnapshot{
				PlanType:   rl.PlanType,
				ObservedAt: tsMs,
				SourceNote: "来自 Codex 本地会话 rate_limits 快照",
			}
			if rl.Primary != nil {
				mins := rl.Primary.WindowMinutes
				if mins <= 0 {
					mins = 300
				}
				snap.Primary = &officialWindow{
					UsedPercent:   clampPercent(rl.Primary.UsedPercent),
					WindowMinutes: mins,
					ResetsAt:      unixSecToMillis(rl.Primary.ResetsAt),
				}
			}
			if rl.Secondary != nil {
				mins := rl.Secondary.WindowMinutes
				if mins <= 0 {
					mins = 10080
				}
				snap.Secondary = &officialWindow{
					UsedPercent:   clampPercent(rl.Secondary.UsedPercent),
					WindowMinutes: mins,
					ResetsAt:      unixSecToMillis(rl.Secondary.ResetsAt),
				}
			}
			e.cache.CodexRateLimit = snap
		}
	}

	if ev.Payload.Info == nil || ev.Payload.Info.TotalTokenUsage == nil {
		return
	}
	tu := ev.Payload.Info.TotalTokenUsage
	// Codex input_tokens already includes cached_input_tokens. Normalize cleanly.
	freshIn := max64(0, tu.InputTokens-tu.CachedInputTokens)
	cacheRead := max64(0, tu.CachedInputTokens)
	cacheWrite := max64(0, tu.CacheWriteInputTokens)
	outTok := max64(0, tu.OutputTokens)
	total := freshIn + cacheRead + cacheWrite + outTok
	if total == 0 {
		return
	}

	// Compute positive delta against previous cumulative total in this rollout file.
	prev := cur.PrevTotal
	dIn := max64(0, freshIn-prev.InputTokens)
	dCR := max64(0, cacheRead-prev.CacheReadTokens)
	dCW := max64(0, cacheWrite-prev.CacheWriteTokens)
	dOut := max64(0, outTok-prev.OutputTokens)
	dTot := dIn + dCR + dCW + dOut
	if dTot == 0 {
		return
	}

	cur.PrevTotal = TokenStats{
		InputTokens:      max64(prev.InputTokens, freshIn),
		CacheReadTokens:  max64(prev.CacheReadTokens, cacheRead),
		CacheWriteTokens: max64(prev.CacheWriteTokens, cacheWrite),
		OutputTokens:     max64(prev.OutputTokens, outTok),
		TotalTokens:      max64(prev.TotalTokens, total),
	}

	model := cur.CurrentModel
	if model == "" {
		model = "codex"
	}
	e.recordEventLocked("codex", model, tsMs, TokenStats{
		InputTokens:      dIn,
		CacheReadTokens:  dCR,
		CacheWriteTokens: dCW,
		OutputTokens:     dOut,
		TotalTokens:      dTot,
		Requests:         1,
	})
}

func (e *Engine) parseAgyLineLocked(line []byte, cur *fileCursor) {
	if !bytes.Contains(line, []byte(`"input_tokens"`)) &&
		!bytes.Contains(line, []byte(`"cache_read_tokens"`)) &&
		!bytes.Contains(line, []byte(`"output_tokens"`)) {
		return
	}
	var entry struct {
		StepIndex       int    `json:"step_index"`
		Type            string `json:"type"`
		CreatedAt       string `json:"created_at"`
		Model           string `json:"model"`
		InputTokens     int64  `json:"input_tokens"`
		CacheReadTokens int64  `json:"cache_read_tokens"`
		OutputTokens    int64  `json:"output_tokens"`
	}
	if err := json.Unmarshal(line, &entry); err != nil {
		return
	}
	if entry.Type != "PLANNER_RESPONSE" && entry.Type != "CHECKPOINT" {
		return
	}
	if entry.StepIndex <= cur.MaxStepIndex {
		return
	}
	cur.MaxStepIndex = entry.StepIndex

	model := strings.TrimSpace(entry.Model)
	if model != "" {
		cur.CurrentModel = model
	} else if cur.CurrentModel != "" {
		model = cur.CurrentModel
	} else {
		model = "antigravity"
	}

	inTok := max64(0, entry.InputTokens)
	cacheRead := max64(0, entry.CacheReadTokens)
	outTok := max64(0, entry.OutputTokens)
	total := inTok + cacheRead + outTok
	if total == 0 {
		return
	}
	tsMs := parseISOToMillis(entry.CreatedAt)
	if tsMs == 0 {
		return
	}
	e.recordEventLocked("agy", model, tsMs, TokenStats{
		InputTokens:     inTok,
		CacheReadTokens: cacheRead,
		OutputTokens:    outTok,
		TotalTokens:     total,
		Requests:        1,
	})
}

func (e *Engine) recordEventLocked(agent, model string, tsMs int64, delta TokenStats) {
	if delta.IsZero() {
		return
	}
	t := time.UnixMilli(tsMs).In(time.Local)
	dateKey := t.Format("2006-01-02")

	if e.cache.Days[dateKey] == nil {
		e.cache.Days[dateKey] = make(map[string]map[string]TokenStats)
	}
	if e.cache.Days[dateKey][agent] == nil {
		e.cache.Days[dateKey][agent] = make(map[string]TokenStats)
	}
	existing := e.cache.Days[dateKey][agent][model]
	existing.Add(delta)
	e.cache.Days[dateKey][agent][model] = existing

	if tsMs > e.cache.LastActive[agent] {
		e.cache.LastActive[agent] = tsMs
	}

	// Record into 5-minute rolling bucket if within last 8 days.
	if time.Since(t) <= 8*24*time.Hour {
		slotStart := (tsMs / 300_000) * 300_000
		if e.cache.Recent5m[agent] == nil {
			e.cache.Recent5m[agent] = make(map[string]*bucket5m)
		}
		bKey := itoa64(slotStart) + "|" + model
		b := e.cache.Recent5m[agent][bKey]
		if b == nil {
			b = &bucket5m{
				SlotStartMs: slotStart,
				EarliestMs:  tsMs,
				Model:       model,
			}
			e.cache.Recent5m[agent][bKey] = b
		}
		if tsMs < b.EarliestMs {
			b.EarliestMs = tsMs
		}
		b.Stats.Add(delta)
	}
}

func (e *Engine) pruneRecentLocked(now time.Time) {
	cutoffMs := now.Add(-8 * 24 * time.Hour).UnixMilli()
	for _, buckets := range e.cache.Recent5m {
		for k, b := range buckets {
			if b.SlotStartMs < cutoffMs {
				delete(buckets, k)
				e.dirty = true
			}
		}
	}
}

func (e *Engine) buildSummaryLocked(now time.Time, sc scanCounters, scanMs int64) Summary {
	loc := time.Local
	nowLocal := now.In(loc)
	todayKey := nowLocal.Format("2006-01-02")
	weekStart := startOfWeekMonday(nowLocal)
	weekStartKey := weekStart.Format("2006-01-02")
	monthPrefix := nowLocal.Format("2006-01-")
	fiveHourCutoff := now.Add(-5 * time.Hour).UnixMilli()
	sevenDayCutoff := now.Add(-7 * 24 * time.Hour).UnixMilli()

	agentsOrder := []struct {
		id   string
		name string
	}{
		{"claude", "Claude Code"},
		{"codex", "OpenAI Codex"},
		{"agy", "Antigravity"},
	}

	agentMap := make(map[string]*AgentUsage, 3)
	modelMaps := make(map[string]map[string]*ModelUsage, 3)
	for _, def := range agentsOrder {
		au := &AgentUsage{
			Agent:        def.id,
			DisplayName:  def.name,
			Installed:    sc.installed[def.id],
			LogFiles:     sc.agentFiles[def.id],
			LogBytes:     sc.agentBytes[def.id],
			LastActiveAt: e.cache.LastActive[def.id],
		}
		agentMap[def.id] = au
		modelMaps[def.id] = make(map[string]*ModelUsage)
	}

	// Build 14-day daily chart lookup.
	dailyOrder := make([]string, 14)
	dailyMap := make(map[string]*DailyPoint, 14)
	for i := 0; i < 14; i++ {
		d := nowLocal.AddDate(0, 0, -(13 - i)).Format("2006-01-02")
		dailyOrder[i] = d
		dailyMap[d] = &DailyPoint{Date: d}
	}

	// Aggregate daily persisted totals.
	earliestByAgent := make(map[string]string, 3)
	for dateKey, byAgent := range e.cache.Days {
		isToday := dateKey == todayKey
		isThisWeek := dateKey >= weekStartKey && dateKey <= todayKey
		isThisMonth := strings.HasPrefix(dateKey, monthPrefix)
		dp := dailyMap[dateKey]

		for agentID, byModel := range byAgent {
			au := agentMap[agentID]
			if au == nil {
				continue
			}
			mm := modelMaps[agentID]
			hasAgentTok := false
			for modelName, st := range byModel {
				if st.TotalTokens > 0 {
					hasAgentTok = true
				}
				au.Total.Add(st)
				if isThisMonth {
					au.Month.Add(st)
				}
				if isThisWeek {
					au.Week.Add(st)
				}
				if isToday {
					au.Today.Add(st)
				}
				if dp != nil {
					dp.Total += st.TotalTokens
					switch agentID {
					case "claude":
						dp.Claude += st.TotalTokens
					case "codex":
						dp.Codex += st.TotalTokens
					case "agy":
						dp.Agy += st.TotalTokens
					}
				}
				mu := mm[modelName]
				if mu == nil {
					mu = &ModelUsage{Model: modelName}
					mm[modelName] = mu
				}
				mu.Total.Add(st)
				if isThisMonth {
					mu.Month.Add(st)
				}
				if isThisWeek {
					mu.Week.Add(st)
				}
				if isToday {
					mu.Today.Add(st)
				}
			}
			if hasAgentTok && (earliestByAgent[agentID] == "" || dateKey < earliestByAgent[agentID]) {
				earliestByAgent[agentID] = dateKey
			}
		}
	}

	// Merge historical Claude Code totals from stats-cache.json for logs already pruned by Claude's 30-day cleanup.
	if cs := e.cache.ClaudeStats; cs != nil && len(cs.BackfillByModel) > 0 {
		au := agentMap["claude"]
		au.Installed = true
		mm := modelMaps["claude"]
		for modelName, st := range cs.BackfillByModel {
			au.Total.Add(st)
			mu := mm[modelName]
			if mu == nil {
				mu = &ModelUsage{Model: modelName}
				mm[modelName] = mu
			}
			mu.Total.Add(st)
		}
		since := cs.FirstSessionDate
		if since == "" {
			since = "历史"
		}
		au.HistoryNote = fmt.Sprintf("按 Claude 官方 /stats 未去重口径统计，含 stats-cache 补全 %s 起已清理日志", since)
	}

	if sc.codexZstFiles > 0 {
		if first := earliestByAgent["codex"]; first != "" {
			agentMap["codex"].HistoryNote = fmt.Sprintf("已含 %d 个 .jsonl.zst 历史归档（%s 起完整本地会话，含子代理）", sc.codexZstFiles, first)
		} else {
			agentMap["codex"].HistoryNote = fmt.Sprintf("已含 %d 个 .jsonl.zst 历史归档", sc.codexZstFiles)
		}
	}

	// Aggregate rolling 5-hour & rolling 7-day tokens.
	rolling5h := make(map[string]TokenStats, 3)
	rolling7d := make(map[string]TokenStats, 3)
	for agentID, buckets := range e.cache.Recent5m {
		for _, b := range buckets {
			if b.SlotStartMs+300_000 >= fiveHourCutoff {
				st := rolling5h[agentID]
				st.Add(b.Stats)
				rolling5h[agentID] = st
			}
			if b.SlotStartMs+300_000 >= sevenDayCutoff {
				st := rolling7d[agentID]
				st.Add(b.Stats)
				rolling7d[agentID] = st
			}
		}
	}

	var grand Summary
	grand.GeneratedAt = now.UnixMilli()
	grand.Timezone = nowLocal.Location().String()
	grand.Hostname = e.opts.Hostname
	grand.WebURL = e.opts.WebURL

	for _, def := range agentsOrder {
		au := agentMap[def.id]
		au.FiveHour = rolling5h[def.id]

		// Sort models by total tokens descending, keep top 10.
		models := make([]ModelUsage, 0, len(modelMaps[def.id]))
		for _, m := range modelMaps[def.id] {
			models = append(models, *m)
		}
		sort.Slice(models, func(i, j int) bool {
			return models[i].Total.TotalTokens > models[j].Total.TotalTokens
		})
		if len(models) > 10 {
			models = models[:10]
		}
		au.Models = models

		// Populate 5h & Weekly quota windows.
		e.populateQuotaWindowsLocked(au, now, rolling7d[def.id])

		grand.Today.Add(au.Today)
		grand.FiveHour.Add(au.FiveHour)
		grand.Week.Add(au.Week)
		grand.Month.Add(au.Month)
		grand.Total.Add(au.Total)
		grand.Agents = append(grand.Agents, *au)
	}

	grand.Daily = make([]DailyPoint, 0, len(dailyOrder))
	for _, d := range dailyOrder {
		grand.Daily = append(grand.Daily, *dailyMap[d])
	}

	var cacheBytes int64
	if fi, err := os.Stat(e.opts.CachePath); err == nil {
		cacheBytes = fi.Size()
	}
	grand.Meta = ScanMeta{
		ScanMs:       scanMs,
		FilesTotal:   sc.filesTotal,
		FilesScanned: sc.filesScanned,
		FilesCached:  sc.filesCached,
		CachePath:    e.opts.CachePath,
		CacheBytes:   cacheBytes,
	}

	return grand
}

func (e *Engine) populateQuotaWindowsLocked(au *AgentUsage, now time.Time, rolling7d TokenStats) {
	nowMs := now.UnixMilli()

	// Default local fallback windows: intentionally leave ResetsAt = 0 instead of
	// fabricating inaccurate local reset countdowns (server-side rolling windows
	// cannot be reconstructed reliably from local logs alone).
	au.Window5h = QuotaWindow{
		Source:        "local",
		ResetsAt:      0,
		WindowMinutes: 300,
		Tokens:        au.FiveHour,
	}
	au.WindowWeekly = QuotaWindow{
		Source:        "local",
		ResetsAt:      0,
		WindowMinutes: 10080,
		Tokens:        au.Week,
	}

	switch au.Agent {
	case "codex":
		// Prefer fresher of live OAuth fetch vs local session snapshot.
		snap := e.cache.CodexRateLimit
		if e.codexLive != nil && (snap == nil || e.codexLive.ObservedAt >= snap.ObservedAt) {
			snap = e.codexLive
		}
		if snap != nil {
			au.PlanType = snap.PlanType
			if snap.Primary != nil {
				pct := snap.Primary.UsedPercent
				expired := snap.Primary.ResetsAt > 0 && nowMs > snap.Primary.ResetsAt
				if expired {
					pct = 0
				}
				note := snap.SourceNote
				if expired {
					note = "上次 5h 窗口已重置（待新会话或实时接口刷新）"
				}
				au.Window5h = QuotaWindow{
					Source:        "official",
					UsedPercent:   &pct,
					ResetsAt:      snap.Primary.ResetsAt,
					WindowMinutes: snap.Primary.WindowMinutes,
					Tokens:        au.FiveHour,
					Expired:       expired,
					ObservedAt:    snap.ObservedAt,
					Note:          note,
				}
			}
			if snap.Secondary != nil {
				pct := snap.Secondary.UsedPercent
				expired := snap.Secondary.ResetsAt > 0 && nowMs > snap.Secondary.ResetsAt
				if expired {
					pct = 0
				}
				note := snap.SourceNote
				if expired {
					note = "上周窗口已重置（待新会话或实时接口刷新）"
				}
				au.WindowWeekly = QuotaWindow{
					Source:        "official",
					UsedPercent:   &pct,
					ResetsAt:      snap.Secondary.ResetsAt,
					WindowMinutes: snap.Secondary.WindowMinutes,
					Tokens:        rolling7d,
					Expired:       expired,
					ObservedAt:    snap.ObservedAt,
					Note:          note,
				}
			}
		} else {
			au.Window5h.Note = "近 5 小时本地统计（无法拉取实时配额接口）"
			au.WindowWeekly.Note = "本周本地统计（周一至今）"
		}

	case "claude":
		if e.claudeLive != nil {
			if e.claudeLive.Primary != nil {
				pct := e.claudeLive.Primary.UsedPercent
				au.Window5h = QuotaWindow{
					Source:        "official",
					UsedPercent:   &pct,
					ResetsAt:      e.claudeLive.Primary.ResetsAt,
					WindowMinutes: 300,
					Tokens:        au.FiveHour,
					Expired:       e.claudeLive.Primary.ResetsAt > 0 && nowMs > e.claudeLive.Primary.ResetsAt,
					ObservedAt:    e.claudeLive.ObservedAt,
					Note:          e.claudeLive.SourceNote,
				}
			}
			if e.claudeLive.Secondary != nil {
				pct := e.claudeLive.Secondary.UsedPercent
				au.WindowWeekly = QuotaWindow{
					Source:        "official",
					UsedPercent:   &pct,
					ResetsAt:      e.claudeLive.Secondary.ResetsAt,
					WindowMinutes: 10080,
					Tokens:        rolling7d,
					Expired:       e.claudeLive.Secondary.ResetsAt > 0 && nowMs > e.claudeLive.Secondary.ResetsAt,
					ObservedAt:    e.claudeLive.ObservedAt,
					Note:          e.claudeLive.SourceNote,
				}
			}
		} else {
			if au.FiveHour.TotalTokens > 0 {
				au.Window5h.Note = "近 5 小时本地消耗统计（未登录 OAuth，无法获取官方重置时间）"
			} else {
				au.Window5h.Note = "近 5 小时无活跃对话（登录后自动同步官方配额）"
			}
			au.WindowWeekly.Note = "本周本地消耗统计（周一至今，登录 OAuth 后同步官方 7 天重置时间）"
		}

	case "agy":
		if snap := e.cache.AgyQuota; snap != nil {
			if snap.PlanType != "" {
				au.PlanType = snap.PlanType
			}
			if e.cache.AgyQuotaExtra != "" {
				au.HistoryNote = e.cache.AgyQuotaExtra
			}
			if snap.Primary != nil {
				pct := snap.Primary.UsedPercent
				expired := snap.Primary.ResetsAt > 0 && nowMs > snap.Primary.ResetsAt
				if expired {
					pct = 0
				}
				note := snap.SourceNote
				if expired {
					note = "上次 5h 窗口已重置（待 agy 新操作刷新）"
				}
				au.Window5h = QuotaWindow{
					Source:        "official",
					UsedPercent:   &pct,
					ResetsAt:      snap.Primary.ResetsAt,
					WindowMinutes: snap.Primary.WindowMinutes,
					Tokens:        au.FiveHour,
					Expired:       expired,
					ObservedAt:    snap.ObservedAt,
					Note:          note,
				}
			} else if au.FiveHour.TotalTokens > 0 {
				au.Window5h.Note = "近 5 小时本地消耗统计（当前 statusLine 未返回 5h 配额桶）"
			} else {
				au.Window5h.Note = "近 5 小时无活跃对话"
			}
			if snap.Secondary != nil {
				pct := snap.Secondary.UsedPercent
				expired := snap.Secondary.ResetsAt > 0 && nowMs > snap.Secondary.ResetsAt
				if expired {
					pct = 0
				}
				note := snap.SourceNote
				if expired {
					note = "上周窗口已重置（待 agy 新操作刷新）"
				}
				au.WindowWeekly = QuotaWindow{
					Source:        "official",
					UsedPercent:   &pct,
					ResetsAt:      snap.Secondary.ResetsAt,
					WindowMinutes: snap.Secondary.WindowMinutes,
					Tokens:        rolling7d,
					Expired:       expired,
					ObservedAt:    snap.ObservedAt,
					Note:          note,
				}
			} else {
				au.WindowWeekly.Note = "本周本地消耗统计（当前 statusLine 未返回周配额桶）"
			}
		} else {
			if au.FiveHour.TotalTokens > 0 {
				au.Window5h.Note = "近 5 小时本地消耗统计（打开 agy 对话后自动通过官方 statusLine 同步配额与重置时间）"
			} else {
				au.Window5h.Note = "近 5 小时无活跃对话（打开 agy 对话后自动通过官方 statusLine 同步配额）"
			}
			au.WindowWeekly.Note = "本周本地消耗统计（打开 agy 对话后自动同步官方 7 天滚动配额与重置时间）"
		}
	}
}

func startOfWeekMonday(t time.Time) time.Time {
	weekday := int(t.Weekday())
	if weekday == 0 {
		weekday = 7
	}
	day := time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, t.Location())
	return day.AddDate(0, 0, -(weekday - 1))
}

func max64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}

func itoa64(v int64) string {
	if v == 0 {
		return "0"
	}
	neg := v < 0
	if neg {
		v = -v
	}
	var buf [24]byte
	i := len(buf)
	for v > 0 {
		i--
		buf[i] = byte('0' + v%10)
		v /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}
