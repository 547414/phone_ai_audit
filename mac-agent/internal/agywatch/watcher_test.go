package agywatch

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"phone-ai-audit/mac-agent/internal/proto"
	"phone-ai-audit/mac-agent/internal/term"
)

func TestParsePermissionPromptMatchesRealAgyCaptures(t *testing.T) {
	// Capture modeled directly after real AGY git commit/push prompt
	cap1 := `
● Ran (cd /tmp/demo-app/web && npx vue-tsc --noEmit)

Command
──────────────────────────────────────────────────────────────────────────
Requesting permission for:
  cd /tmp/demo-app/web && npx vue-tsc --noEmit && git commit -m "feat: test" && git push

Run this command?
> 1. Yes, run command
  2. No, cancel

↑/↓ Navigate · tab Amend · ctrl+g edit/expand command
esc to cancel                                               auto · Gemini 4 Neon · xhigh
phone-3c2ce1 | 0:agy*                                       08:28
`
	d1, ok := ParsePermissionPrompt(cap1, cap1)
	if !ok {
		t.Fatal("expected ParsePermissionPrompt to detect real AGY git command prompt")
	}
	if d1.SelectedIndex != 1 {
		t.Fatalf("expected SelectedIndex=1, got %d", d1.SelectedIndex)
	}
	if !strings.Contains(d1.Body, "npx vue-tsc --noEmit") || !strings.Contains(d1.Body, "git push") {
		t.Fatalf("unexpected extracted body: %q", d1.Body)
	}
	if d1.Question != "Run this command?" {
		t.Fatalf("unexpected question: %q", d1.Question)
	}

	// Capture modeled after AGY npm run build prompt with "always allow" options (where header scrolled above visible area)
	cap2Vis := `
  cd /tmp/demo-app/web && npm run build && cd /tmp/demo-app/mobile && npm run build

> 1. Yes, run command
  2. Yes, and always allow in this conversation for commands that start with 'npm run build'
  3. Yes, and always allow for commands that start with 'npm run build' (Persist to settings)
  4. No, cancel

↑/↓ Navigate · tab Amend · ctrl+g edit/expand command
auto · Gemini 4 Neon · xhigh
`
	cap2Ext := "Requesting permission for:\n" + cap2Vis
	d2, ok := ParsePermissionPrompt(cap2Vis, cap2Ext)
	if !ok {
		t.Fatal("expected ParsePermissionPrompt to detect npm run build always-allow prompt")
	}
	if !strings.Contains(d2.Body, "npm run build") {
		t.Fatalf("expected npm run build in body, got %q", d2.Body)
	}
	if d1.Fingerprint == d2.Fingerprint {
		t.Fatal("distinct prompts must have distinct fingerprints")
	}

	// Negative test 1: completed turn with idle input box MUST NOT trigger
	idleScreen := `
● Ran (cd /tmp/demo-app/web && npm run build)
All builds succeeded.

> 

auto · Gemini 4 Neon · xhigh
`
	if _, ok := ParsePermissionPrompt(idleScreen, idleScreen); ok {
		t.Fatal("idle terminal screen must not trigger permission prompt")
	}

	// Negative test 2: normal ask_question design poll MUST NOT trigger
	designPoll := `
Which database engine should we use for the prototype?
> 1. PostgreSQL (Recommended)
  2. SQLite
  3. DuckDB

↑/↓ Navigate · Enter to select
auto · Gemini 4 Neon · xhigh
`
	if _, ok := ParsePermissionPrompt(designPoll, designPoll); ok {
		t.Fatal("design question poll must not be misclassified as permission prompt")
	}
}

func TestWatcherApprovesAndCancelsOnLocalClear(t *testing.T) {
	promptScreen := `
Command
──────────────────────────
Requesting permission for:
  npm run build

Run this command?
> 1. Yes, run command
  2. No, cancel

↑/↓ Navigate · esc to cancel
auto · Gemini 4 Neon · xhigh
`
	var mu sync.Mutex
	curScreen := promptScreen
	var sentKeys []string
	approvalCount := 0

	w := New(Callbacks{
		ListPanes: func() []term.AgyPane {
			return []term.AgyPane{{SessionName: "phone-1", PaneID: "%1", Cwd: "/tmp/demo", ProjectName: "demo"}}
		},
		CapturePane: func(paneID string, extra int) (string, error) {
			mu.Lock()
			defer mu.Unlock()
			return curScreen, nil
		},
		SendKeys: func(paneID string, keys ...string) error {
			mu.Lock()
			sentKeys = append(sentKeys, keys...)
			curScreen = "● Ran (npm run build)\n> \n"
			mu.Unlock()
			return nil
		},
		RequestApproval: func(ctx context.Context, a proto.Approval) (proto.Approval, error) {
			mu.Lock()
			approvalCount++
			mu.Unlock()
			return proto.Approval{Status: "approved", DecidedBy: "system_timeout"}, nil
		},
	})

	w.scanOnce(context.Background())
	// Wait for handlePrompt goroutine
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		done := len(sentKeys) > 0
		mu.Unlock()
		if done {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	mu.Lock()
	defer mu.Unlock()
	if approvalCount != 1 {
		t.Fatalf("expected 1 approval call, got %d", approvalCount)
	}
	if len(sentKeys) != 1 || sentKeys[0] != "Enter" {
		t.Fatalf("expected Enter sent to tmux pane, got %v", sentKeys)
	}
}
