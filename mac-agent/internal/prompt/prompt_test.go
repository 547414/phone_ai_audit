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
	script := "#!/bin/sh\ncat >/dev/null\necho '{\"decision\":\"denied\",\"reason\":\"use safer command\"}'\n"
	if err := os.WriteFile(okBin, []byte(script), 0o755); err != nil {
		t.Fatalf("write mock script: %v", err)
	}
	t.Setenv("AI_AUDIT_PROMPT_BIN", okBin)

	res, err := Show(context.Background(), proto.Approval{Agent: "claude", Summary: "rm -rf /tmp/x"}, 30)
	if err != nil {
		t.Fatalf("Show failed: %v", err)
	}
	if res.Decision != "denied" || res.Reason != "use safer command" {
		t.Fatalf("unexpected result: %+v", res)
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
	_, err = Show(ctx, proto.Approval{Agent: "agy", Summary: "echo hi"}, 30)
	elapsed := time.Since(start)
	if err == nil {
		t.Fatal("expected cancellation error")
	}
	if elapsed > 2*time.Second {
		t.Fatalf("expected prompt process to terminate promptly on cancel, took %s", elapsed)
	}
}
