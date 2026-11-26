package prompt

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"phone-ai-audit/mac-agent/internal/proto"
)

func TestPromptDecisionAndContextCancellation(t *testing.T) {
	dir := t.TempDir()

	// 1. Mock prompt returning immediate JSON decision
	okBin := filepath.Join(dir, "mock-prompt-ok")
	stdinFile := filepath.Join(dir, "stdin.json")
	script := "#!/bin/sh\ncat >" + stdinFile + "\necho '{\"decision\":\"denied\",\"reason\":\"use safer command\",\"decided_by\":\"mac_local\"}'\n"
	if err := os.WriteFile(okBin, []byte(script), 0o755); err != nil {
		t.Fatalf("write mock script: %v", err)
	}
	t.Setenv("AI_AUDIT_PROMPT_BIN", okBin)

	res, err := Show(context.Background(), proto.Approval{Agent: "claude", Summary: "rm -rf /tmp/x"}, 30, "approve")
	if err != nil {
		t.Fatalf("Show failed: %v", err)
	}
	if res.Decision != "denied" || res.Reason != "use safer command" || res.DecidedBy != "mac_local" {
		t.Fatalf("unexpected result: %+v", res)
	}
	stdinBytes, err := os.ReadFile(stdinFile)
	if err != nil {
		t.Fatalf("read stdin capture: %v", err)
	}
	if string(stdinBytes) == "" || !containsSubstr(string(stdinBytes), `"timeout_action":"approve"`) {
		t.Fatalf("expected timeout_action in prompt stdin, got %s", string(stdinBytes))
	}

	// 2. Mock prompt that sleeps until phone cancels it via ctx
	hangBin := filepath.Join(dir, "mock-prompt-hang")
	hangScript := "#!/bin/sh\ncat >/dev/null\nsleep 30\n"
	if err := os.WriteFile(hangBin, []byte(hangScript), 0o755); err != nil {
		t.Fatalf("write hang script: %v", err)
	}
	t.Setenv("AI_AUDIT_PROMPT_BIN", hangBin)

	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err = Show(ctx, proto.Approval{Agent: "agy", Summary: "echo hi"}, 30, "approve")
	elapsed := time.Since(start)
	if err == nil {
		t.Fatal("expected cancellation error")
	}
	if elapsed > 2*time.Second {
		t.Fatalf("expected prompt process to terminate promptly on cancel, took %s", elapsed)
	}

	// 3. Mock prompt emitting live pause/resume events with remaining_sec before final decision
	pauseBin := filepath.Join(dir, "mock-prompt-pause")
	pauseScript := "#!/bin/sh\ncat >/dev/null\necho '{\"event\":\"pause\",\"remaining_sec\":11}'\nsleep 0.05\necho '{\"event\":\"resume\",\"remaining_sec\":11}'\necho '{\"decision\":\"denied\",\"reason\":\"等等\",\"decided_by\":\"mac_local\"}'\n"
	if err := os.WriteFile(pauseBin, []byte(pauseScript), 0o755); err != nil {
		t.Fatalf("write pause script: %v", err)
	}
	t.Setenv("AI_AUDIT_PROMPT_BIN", pauseBin)

	type holdEv struct {
		paused bool
		rem    int
	}
	var holdEvents []holdEv
	res3, err := ShowWithControl(context.Background(), proto.Approval{Agent: "agy", Summary: "rm -rf /tmp/x"}, 15, "approve", Control{
		OnHold: func(paused bool, remainingSec int) {
			holdEvents = append(holdEvents, holdEv{paused: paused, rem: remainingSec})
		},
	})
	if err != nil {
		t.Fatalf("ShowWithControl failed: %v", err)
	}
	if res3.Decision != "denied" || res3.Reason != "等等" {
		t.Fatalf("unexpected pause result: %+v", res3)
	}
	if len(holdEvents) != 2 || !holdEvents[0].paused || holdEvents[0].rem != 11 || holdEvents[1].paused || holdEvents[1].rem != 11 {
		t.Fatalf("expected [{true 11}, {false 11}] hold events, got %+v", holdEvents)
	}
}

func containsSubstr(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
