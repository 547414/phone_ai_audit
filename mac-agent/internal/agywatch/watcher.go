package agywatch

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"log"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"phone-ai-audit/mac-agent/internal/proto"
	"phone-ai-audit/mac-agent/internal/risk"
	"phone-ai-audit/mac-agent/internal/term"
)

// DetectedPrompt represents an active Antigravity CLI interactive permission prompt
// captured directly from a live tmux pane when PreToolUse did not intercept it.
type DetectedPrompt struct {
	Summary       string
	Body          string
	Question      string
	SelectedIndex int
	Option1Text   string
	Fingerprint   string
	RiskLevel     string
	RiskReasons   []string
}

type Callbacks struct {
	ListPanes             func() []term.AgyPane
	CapturePane           func(paneID string, extraHistoryLines int) (string, error)
	SendKeys              func(paneID string, keys ...string) error
	IsHookActive          func(sessionName string) bool
	WasRecentHookFallback func(sessionName string) bool
	RequestApproval       func(ctx context.Context, a proto.Approval) (proto.Approval, error)
}

type Watcher struct {
	cb Callbacks

	mu                  sync.Mutex
	activePanes         map[string]bool
	handledFingerprints map[string]string
}

func New(cb Callbacks) *Watcher {
	if cb.ListPanes == nil {
		cb.ListPanes = term.ListAgyPanes
	}
	if cb.CapturePane == nil {
		cb.CapturePane = term.CapturePane
	}
	if cb.SendKeys == nil {
		cb.SendKeys = term.SendPaneKeys
	}
	return &Watcher{
		cb:                  cb,
		activePanes:         make(map[string]bool),
		handledFingerprints: make(map[string]string),
	}
}

func (w *Watcher) Run(ctx context.Context) {
	ticker := time.NewTicker(1200 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			w.scanOnce(ctx)
		}
	}
}

func (w *Watcher) scanOnce(ctx context.Context) {
	panes := w.cb.ListPanes()
	if len(panes) == 0 {
		return
	}

	present := make(map[string]bool, len(panes))
	for _, pane := range panes {
		present[pane.PaneID] = true

		w.mu.Lock()
		busy := w.activePanes[pane.PaneID]
		w.mu.Unlock()
		if busy {
			continue
		}

		vis, err := w.cb.CapturePane(pane.PaneID, 0)
		if err != nil {
			continue
		}
		if !looksLikePromptCandidate(vis) {
			w.clearHandled(pane.PaneID)
			continue
		}

		ext, err := w.cb.CapturePane(pane.PaneID, 80)
		if err != nil {
			ext = vis
		}
		detected, ok := ParsePermissionPrompt(vis, ext)
		if !ok {
			w.clearHandled(pane.PaneID)
			continue
		}

		if w.cb.IsHookActive != nil && w.cb.IsHookActive(pane.SessionName) {
			// PreToolUse hook is currently handling an approval on this session; do not duplicate.
			continue
		}
		if w.cb.WasRecentHookFallback != nil && w.cb.WasRecentHookFallback(pane.SessionName) {
			// User explicitly sent the PreToolUse approval back to Terminal; remember this prompt so we don't re-pop it.
			w.setHandled(pane.PaneID, detected.Fingerprint)
			continue
		}

		w.mu.Lock()
		if w.activePanes[pane.PaneID] || w.handledFingerprints[pane.PaneID] == detected.Fingerprint {
			w.mu.Unlock()
			continue
		}
		w.activePanes[pane.PaneID] = true
		w.handledFingerprints[pane.PaneID] = detected.Fingerprint
		w.mu.Unlock()

		go w.handlePrompt(ctx, pane, detected)
	}

	// Clean up stale pane entries for closed tmux panes.
	w.mu.Lock()
	for pid := range w.handledFingerprints {
		if !present[pid] && !w.activePanes[pid] {
			delete(w.handledFingerprints, pid)
		}
	}
	w.mu.Unlock()
}

func (w *Watcher) clearHandled(paneID string) {
	w.mu.Lock()
	delete(w.handledFingerprints, paneID)
	w.mu.Unlock()
}

func (w *Watcher) setHandled(paneID, fp string) {
	w.mu.Lock()
	w.handledFingerprints[paneID] = fp
	w.mu.Unlock()
}

func (w *Watcher) handlePrompt(parentCtx context.Context, pane term.AgyPane, detected DetectedPrompt) {
	defer func() {
		w.mu.Lock()
		delete(w.activePanes, pane.PaneID)
		w.mu.Unlock()
	}()

	log.Printf("agywatch: detected live terminal permission prompt session=%s pane=%s summary=%q",
		pane.SessionName, pane.PaneID, detected.Summary)

	reqCtx, cancel := context.WithCancel(parentCtx)
	defer cancel()

	// Monitor the pane while awaiting decision: if user answers directly in Terminal,
	// cancel the remote approval immediately so both Phone notification & Mac HUD disappear.
	monitorDone := make(chan struct{})
	go func() {
		defer close(monitorDone)
		t := time.NewTicker(600 * time.Millisecond)
		defer t.Stop()
		for {
			select {
			case <-reqCtx.Done():
				return
			case <-t.C:
				vis, err := w.cb.CapturePane(pane.PaneID, 0)
				if err != nil {
					continue
				}
				ext := vis
				if looksLikePromptCandidate(vis) {
					if e, eerr := w.cb.CapturePane(pane.PaneID, 80); eerr == nil {
						ext = e
					}
				}
				cur, ok := ParsePermissionPrompt(vis, ext)
				if !ok || cur.Fingerprint != detected.Fingerprint {
					log.Printf("agywatch: prompt cleared locally in terminal (session=%s pane=%s), canceling remote approval",
						pane.SessionName, pane.PaneID)
					cancel()
					return
				}
			}
		}
	}()

	payloadBytes, _ := json.MarshalIndent(map[string]any{
		"source":       "agy_tmux_watcher",
		"tmux_session": pane.SessionName,
		"pane_id":      pane.PaneID,
		"question":     detected.Question,
		"request":      detected.Body,
	}, "", "  ")

	approval := proto.Approval{
		Agent:       "agy",
		TmuxSession: pane.SessionName,
		TaskTitle:   detected.Question,
		ProjectName: pane.ProjectName,
		Cwd:         pane.Cwd,
		ToolName:    "run_command",
		Summary:     detected.Summary,
		PayloadJSON: string(payloadBytes),
		RiskLevel:   detected.RiskLevel,
		RiskReasons: detected.RiskReasons,
	}

	res, err := w.cb.RequestApproval(reqCtx, approval)
	cancel()
	<-monitorDone
	if err != nil {
		return
	}

	if res.Status != "approved" && res.Status != "denied" && res.Status != "expired" {
		// fallback / canceled: leave Terminal prompt untouched for manual handling.
		return
	}

	// Re-verify the exact same prompt is still on screen before injecting keystrokes.
	vis, err := w.cb.CapturePane(pane.PaneID, 0)
	if err != nil {
		return
	}
	ext := vis
	if e, eerr := w.cb.CapturePane(pane.PaneID, 80); eerr == nil {
		ext = e
	}
	cur, ok := ParsePermissionPrompt(vis, ext)
	if !ok || cur.Fingerprint != detected.Fingerprint {
		log.Printf("agywatch: prompt changed before key injection (session=%s pane=%s), skipping",
			pane.SessionName, pane.PaneID)
		return
	}

	switch res.Status {
	case "approved":
		if cur.SelectedIndex > 1 && cur.SelectedIndex <= 5 {
			upKeys := make([]string, cur.SelectedIndex-1)
			for i := range upKeys {
				upKeys[i] = "Up"
			}
			_ = w.cb.SendKeys(pane.PaneID, upKeys...)
			time.Sleep(80 * time.Millisecond)
		}
		if err := w.cb.SendKeys(pane.PaneID, "Enter"); err != nil {
			log.Printf("agywatch: send Enter failed (pane=%s): %v", pane.PaneID, err)
		} else {
			log.Printf("agywatch: approved live agy prompt via Enter (session=%s pane=%s decided_by=%s)",
				pane.SessionName, pane.PaneID, res.DecidedBy)
		}
	case "denied", "expired":
		if err := w.cb.SendKeys(pane.PaneID, "Escape"); err != nil {
			log.Printf("agywatch: send Escape failed (pane=%s): %v", pane.PaneID, err)
		} else {
			log.Printf("agywatch: denied live agy prompt via Escape (session=%s pane=%s)",
				pane.SessionName, pane.PaneID)
		}
	}
}

var (
	selectedOptRe = regexp.MustCompile(`^\s*[>›❯]\s*([1-9])\.\s+(.+)$`)
	plainOptRe    = regexp.MustCompile(`^\s*([1-9])\.\s+(.+)$`)
	approveOpt1Re = regexp.MustCompile(`(?i)^(yes|allow|approve|run|confirm|proceed|accept)\b`)
	rejectOptRe   = regexp.MustCompile(`(?i)\b(no,\s*cancel|no,|deny|reject|cancel)\b`)
	idlePromptRe  = regexp.MustCompile(`^\s*[>›❯]\s*$`)
	statusBarRe   = regexp.MustCompile(`(?i)(\b(auto|default|accept-edits|plan)\s*·|·\s*(gemini|claude|gpt|xhigh|high|medium|low)\b)`)
	tmuxBarRe     = regexp.MustCompile(`\|\s*\d+:\S+\*?\s+\d{2}:\d{2}`)
)

func looksLikePromptCandidate(visibleScreen string) bool {
	lower := strings.ToLower(visibleScreen)
	if !strings.Contains(lower, "1.") {
		return false
	}
	return strings.Contains(lower, "requesting permission") ||
		strings.Contains(lower, "run this command?") ||
		strings.Contains(lower, "yes, run command") ||
		strings.Contains(lower, "always allow") ||
		strings.Contains(lower, "esc to cancel") ||
		strings.Contains(lower, "↑/↓ navigate") ||
		strings.Contains(lower, "allow this") ||
		strings.Contains(lower, "persist to settings")
}

// ParsePermissionPrompt inspects the live visible tmux screen (and optionally extended scrollback)
// and detects whether Antigravity CLI is currently paused at an interactive permission prompt.
func ParsePermissionPrompt(visibleScreen, extendedScreen string) (DetectedPrompt, bool) {
	visLines := normalizeScreenLines(visibleScreen)
	if len(visLines) == 0 {
		return DetectedPrompt{}, false
	}

	// Trim bottom status bar / tmux bar / blank lines to inspect the live interactive tail.
	end := len(visLines) - 1
	for end >= 0 && isIgnorableFooterLine(visLines[end]) {
		end--
	}
	if end < 1 {
		return DetectedPrompt{}, false
	}

	// Inspect bottom window (up to 18 lines above status bar) for an active choice menu.
	scanStart := end - 18
	if scanStart < 0 {
		scanStart = 0
	}

	type optEntry struct {
		lineIdx  int
		num      int
		text     string
		selected bool
	}

	var opts []optEntry
	hasFooterHint := false
	hasRejectCue := false
	lastOptLine := -1

	for i := scanStart; i <= end; i++ {
		ln := strings.TrimSpace(visLines[i])
		if ln == "" {
			continue
		}
		lower := strings.ToLower(ln)
		if strings.Contains(lower, "esc to cancel") || strings.Contains(lower, "↑/↓ navigate") || strings.Contains(lower, "ctrl+g edit/expand") {
			hasFooterHint = true
			if strings.Contains(lower, "esc to cancel") {
				hasRejectCue = true
			}
			continue
		}
		if m := selectedOptRe.FindStringSubmatch(visLines[i]); len(m) == 3 {
			n, _ := strconv.Atoi(m[1])
			opts = append(opts, optEntry{lineIdx: i, num: n, text: strings.TrimSpace(m[2]), selected: true})
			lastOptLine = i
			if rejectOptRe.MatchString(m[2]) {
				hasRejectCue = true
			}
			continue
		}
		if m := plainOptRe.FindStringSubmatch(visLines[i]); len(m) == 3 {
			n, _ := strconv.Atoi(m[1])
			opts = append(opts, optEntry{lineIdx: i, num: n, text: strings.TrimSpace(m[2]), selected: false})
			lastOptLine = i
			if rejectOptRe.MatchString(m[2]) {
				hasRejectCue = true
			}
		}
	}

	if len(opts) < 2 || lastOptLine < 0 {
		return DetectedPrompt{}, false
	}

	// Ensure nothing looking like a fresh idle input prompt or completed step appears after the option list.
	for i := lastOptLine + 1; i <= end; i++ {
		ln := strings.TrimSpace(visLines[i])
		if ln == "" {
			continue
		}
		lower := strings.ToLower(ln)
		if strings.Contains(lower, "esc to cancel") || strings.Contains(lower, "↑/↓ navigate") || strings.Contains(lower, "ctrl+g") || strings.Contains(lower, "tab amend") {
			continue
		}
		if idlePromptRe.MatchString(visLines[i]) || strings.HasPrefix(ln, "• ") || strings.HasPrefix(ln, "● ") {
			return DetectedPrompt{}, false
		}
	}

	// Find option 1 and the currently selected option.
	var opt1 *optEntry
	selectedIdx := 0
	for idx := range opts {
		if opts[idx].num == 1 && opt1 == nil {
			opt1 = &opts[idx]
		}
		if opts[idx].selected {
			selectedIdx = opts[idx].num
		}
	}
	if opt1 == nil || selectedIdx == 0 {
		return DetectedPrompt{}, false
	}
	if !approveOpt1Re.MatchString(opt1.text) {
		return DetectedPrompt{}, false
	}
	if !hasRejectCue && !hasFooterHint {
		return DetectedPrompt{}, false
	}

	// Confirm this is genuinely a PERMISSION prompt (not an `ask_question` design poll).
	tailJoined := strings.ToLower(strings.Join(visLines[scanStart:end+1], "\n"))
	fullVisLower := strings.ToLower(strings.Join(visLines[:end+1], "\n"))
	isPermission := hasPermissionCue(tailJoined) || hasPermissionCue(fullVisLower)
	if !isPermission {
		return DetectedPrompt{}, false
	}

	// Extract full command/request body from extendedScreen (handles long wrapped commands).
	if strings.TrimSpace(extendedScreen) == "" {
		extendedScreen = visibleScreen
	}
	extLines := normalizeScreenLines(extendedScreen)
	question, body := extractPromptDetails(extLines, opt1.text)
	if body == "" {
		body = question
	}
	if body == "" {
		body = opt1.text
	}

	summary := oneLineText(body, 150)
	if question != "" && !strings.EqualFold(question, body) && !strings.HasPrefix(summary, "[") {
		summary = "[AGY 授权] " + summary
	}

	rk := risk.Evaluate("run_command", body, false, false)
	reasons := append([]string(nil), rk.Reasons...)
	reasons = append(reasons, "AGY 终端实时审批捕获")

	fpHash := sha256.Sum256([]byte(strings.ToLower(oneLineText(body, 400)) + "\n" + strings.ToLower(question) + "\n" + strings.ToLower(opt1.text)))
	return DetectedPrompt{
		Summary:       summary,
		Body:          body,
		Question:      question,
		SelectedIndex: selectedIdx,
		Option1Text:   opt1.text,
		Fingerprint:   hex.EncodeToString(fpHash[:16]),
		RiskLevel:     rk.Level,
		RiskReasons:   reasons,
	}, true
}

func hasPermissionCue(lower string) bool {
	cues := []string{
		"requesting permission for",
		"run this command?",
		"execute this command?",
		"yes, run command",
		"yes, allow",
		"allow once",
		"allow this action",
		"allow this tool",
		"allow reading",
		"allow writing",
		"allow editing",
		"always allow in this conversation",
		"always allow for commands",
		"persist to settings",
		"grant permission",
		"permission required",
		"bypass sandbox",
		"ctrl+g edit/expand command",
		"tab amend",
	}
	for _, c := range cues {
		if strings.Contains(lower, c) {
			return true
		}
	}
	return false
}

func extractPromptDetails(extLines []string, opt1Text string) (question string, body string) {
	// Locate option 1 near the end of extLines.
	opt1Idx := -1
	for i := len(extLines) - 1; i >= 0; i-- {
		if m := selectedOptRe.FindStringSubmatch(extLines[i]); len(m) == 3 && m[1] == "1" {
			opt1Idx = i
			break
		}
		if m := plainOptRe.FindStringSubmatch(extLines[i]); len(m) == 3 && m[1] == "1" {
			opt1Idx = i
			break
		}
	}
	if opt1Idx <= 0 {
		return "Antigravity 请求授权", ""
	}

	// Find optional question line directly above option 1 (e.g. "Run this command?").
	bodyEnd := opt1Idx
	for i := opt1Idx - 1; i >= 0 && i >= opt1Idx-3; i-- {
		ln := strings.TrimSpace(extLines[i])
		if ln == "" {
			continue
		}
		if strings.HasSuffix(ln, "?") || strings.HasSuffix(ln, "？") {
			question = ln
			bodyEnd = i
		}
		break
	}
	if question == "" {
		question = "Antigravity 请求授权"
	}

	// Walk upwards from bodyEnd to find "Requesting permission for:" or a section boundary.
	reqHeaderIdx := -1
	boundaryIdx := max(0, bodyEnd-35)
	for i := bodyEnd - 1; i >= boundaryIdx; i-- {
		ln := strings.TrimSpace(extLines[i])
		lower := strings.ToLower(ln)
		if strings.Contains(lower, "requesting permission for") {
			reqHeaderIdx = i
			break
		}
		if isSectionBoundary(ln) {
			boundaryIdx = i + 1
			break
		}
	}

	var collected []string
	start := boundaryIdx
	if reqHeaderIdx >= 0 {
		// Include anything on the same line after "Requesting permission for:" if present.
		raw := strings.TrimSpace(extLines[reqHeaderIdx])
		if idx := strings.Index(strings.ToLower(raw), "requesting permission for:"); idx >= 0 {
			after := strings.TrimSpace(raw[idx+len("requesting permission for:"):])
			if after != "" {
				collected = append(collected, after)
			}
		}
		start = reqHeaderIdx + 1
	}

	for i := start; i < bodyEnd; i++ {
		ln := strings.TrimSpace(extLines[i])
		if ln == "" || isHorizontalRule(ln) || strings.EqualFold(ln, "Command") {
			continue
		}
		collected = append(collected, ln)
	}

	// If no header was found and we captured too many lines, keep only the closest 6 lines above the question.
	if reqHeaderIdx < 0 && len(collected) > 6 {
		collected = collected[len(collected)-6:]
	}
	body = strings.TrimSpace(strings.Join(collected, "\n"))
	return question, body
}

func isSectionBoundary(ln string) bool {
	if ln == "" {
		return false
	}
	if strings.HasPrefix(ln, "• ") || strings.HasPrefix(ln, "● ") || strings.HasPrefix(ln, "○ ") {
		return true
	}
	return isHorizontalRule(ln)
}

func isHorizontalRule(ln string) bool {
	if len(ln) < 8 {
		return false
	}
	for _, r := range ln {
		if r != '─' && r != '━' && r != '-' && r != '=' && r != ' ' {
			return false
		}
	}
	return true
}

func isIgnorableFooterLine(ln string) bool {
	t := strings.TrimSpace(ln)
	if t == "" {
		return true
	}
	if statusBarRe.MatchString(t) || tmuxBarRe.MatchString(t) {
		return true
	}
	return false
}

func normalizeScreenLines(s string) []string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	return strings.Split(s, "\n")
}

func oneLineText(s string, limit int) string {
	cleaned := strings.Join(strings.Fields(s), " ")
	runes := []rune(cleaned)
	if len(runes) > limit {
		return string(runes[:limit]) + "..."
	}
	return cleaned
}
