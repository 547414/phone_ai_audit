package idle

import (
	"context"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"time"
)

var hidIdleRe = regexp.MustCompile(`"HIDIdleTime"\s*=\s*(\d+)`)

// State describes the current user presence on macOS.
type State struct {
	IdleSeconds int  `json:"idle_seconds"`
	Locked      bool `json:"locked"`
}

func Detect() State {
	ctx, cancel := context.WithTimeout(context.Background(), 800*time.Millisecond)
	defer cancel()

	var st State

	// Read HIDIdleTime (nanoseconds since last keyboard/mouse event).
	if out, err := exec.CommandContext(ctx, "/usr/sbin/ioreg", "-c", "IOHIDSystem", "-d", "4").Output(); err == nil {
		if m := hidIdleRe.FindSubmatch(out); len(m) == 2 {
			if ns, err := strconv.ParseInt(string(m[1]), 10, 64); err == nil && ns >= 0 {
				st.IdleSeconds = int(ns / 1_000_000_000)
			}
		}
	}

	// Check screen lock state via ioreg CGSSessionScreenIsLocked.
	if out, err := exec.CommandContext(ctx, "/usr/sbin/ioreg", "-n", "Root", "-d1").Output(); err == nil {
		s := string(out)
		if strings.Contains(s, `"CGSSessionScreenIsLocked" = Yes`) || strings.Contains(s, `"CGSSessionScreenIsLocked"=Yes`) {
			st.Locked = true
		}
	}
	return st
}

// ShouldUsePhone decides whether auto mode should route approval to the phone.
func (s State) ShouldUsePhone(thresholdSec int) bool {
	if s.Locked {
		return true
	}
	if thresholdSec <= 0 {
		thresholdSec = 60
	}
	return s.IdleSeconds >= thresholdSec
}
