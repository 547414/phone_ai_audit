package prompt

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"phone-ai-audit/mac-agent/internal/proto"
)

var ErrUnavailable = errors.New("ai-audit-prompt binary not found")

type Result struct {
	Decision  string `json:"decision"`
	Reason    string `json:"reason"`
	DecidedBy string `json:"decided_by,omitempty"`
}

var (
	slotMu    sync.Mutex
	usedSlots = map[int]bool{}
)

func acquireSlot() int {
	slotMu.Lock()
	defer slotMu.Unlock()
	for i := 0; i < 8; i++ {
		if !usedSlots[i] {
			usedSlots[i] = true
			return i
		}
	}
	return 0
}

func releaseSlot(slot int) {
	slotMu.Lock()
	delete(usedSlots, slot)
	slotMu.Unlock()
}

func FindBinary() string {
	if custom := strings.TrimSpace(os.Getenv("AI_AUDIT_PROMPT_BIN")); custom != "" {
		if fi, err := os.Stat(custom); err == nil && !fi.IsDir() {
			return custom
		}
	}
	if exe, err := os.Executable(); err == nil {
		cand := filepath.Join(filepath.Dir(exe), "ai-audit-prompt")
		if fi, err := os.Stat(cand); err == nil && !fi.IsDir() {
			return cand
		}
	}
	if home, err := os.UserHomeDir(); err == nil {
		cand := filepath.Join(home, ".local", "bin", "ai-audit-prompt")
		if fi, err := os.Stat(cand); err == nil && !fi.IsDir() {
			return cand
		}
	}
	if p, err := exec.LookPath("ai-audit-prompt"); err == nil {
		return p
	}
	return ""
}

// Show launches the native macOS floating approval HUD and waits for a local user decision
// or ctx cancellation (e.g., when the phone approves/denies first).
func Show(ctx context.Context, a proto.Approval, timeoutSec int, timeoutAction string) (Result, error) {
	bin := FindBinary()
	if bin == "" {
		return Result{}, ErrUnavailable
	}

	slot := acquireSlot()
	defer releaseSlot(slot)

	if strings.TrimSpace(timeoutAction) == "" {
		timeoutAction = "approve"
	}

	payload, err := json.Marshal(map[string]any{
		"agent":          a.Agent,
		"project_name":   a.ProjectName,
		"task_title":     a.TaskTitle,
		"tool_name":      a.ToolName,
		"summary":        a.Summary,
		"risk_level":     a.RiskLevel,
		"risk_reasons":   a.RiskReasons,
		"timeout_sec":    timeoutSec,
		"timeout_action": timeoutAction,
		"slot":           slot,
	})
	if err != nil {
		return Result{}, err
	}

	cmd := exec.Command(bin)
	cmd.Stdin = bytes.NewReader(payload)
	var stdout bytes.Buffer
	cmd.Stdout = &stdout

	if err := cmd.Start(); err != nil {
		return Result{}, err
	}

	waitCh := make(chan error, 1)
	go func() {
		waitCh <- cmd.Wait()
	}()

	select {
	case <-ctx.Done():
		if cmd.Process != nil {
			_ = cmd.Process.Signal(syscall.SIGTERM)
			select {
			case <-waitCh:
			case <-time.After(300 * time.Millisecond):
				_ = cmd.Process.Kill()
				<-waitCh
			}
		}
		return Result{}, ctx.Err()
	case err := <-waitCh:
		if err != nil {
			return Result{}, err
		}
	}

	raw := strings.TrimSpace(stdout.String())
	if raw == "" {
		return Result{}, errors.New("prompt exited without decision")
	}
	var res Result
	if err := json.Unmarshal([]byte(raw), &res); err != nil {
		return Result{}, err
	}
	switch res.Decision {
	case "approved", "denied", "fallback":
		return res, nil
	default:
		return Result{}, errors.New("unknown prompt decision: " + res.Decision)
	}
}
