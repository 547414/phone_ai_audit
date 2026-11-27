package term

import (
	"strings"
	"testing"
)

func envToMap(env []string) map[string]string {
	m := make(map[string]string, len(env))
	for _, kv := range env {
		if k, v, ok := strings.Cut(kv, "="); ok {
			m[k] = v
		}
	}
	return m
}

func TestEnrichedEnvDefaultsTrueColorAndAppleTerminal(t *testing.T) {
	t.Setenv("TERM", "dumb")
	t.Setenv("TMUX", "/tmp/tmux-501/default,123,0")
	t.Setenv("COLORTERM", "")
	t.Setenv("TERM_PROGRAM", "")

	m := envToMap(enrichedEnv())
	if m["TERM"] != "xterm-256color" {
		t.Fatalf("expected TERM=xterm-256color, got %q", m["TERM"])
	}
	if _, hasTmux := m["TMUX"]; hasTmux {
		t.Fatalf("expected TMUX stripped, got %q", m["TMUX"])
	}
	if m["COLORTERM"] != "truecolor" {
		t.Fatalf("expected COLORTERM=truecolor, got %q", m["COLORTERM"])
	}
	if m["TERM_PROGRAM"] != "Apple_Terminal" {
		t.Fatalf("expected TERM_PROGRAM=Apple_Terminal, got %q", m["TERM_PROGRAM"])
	}
}

func TestEnrichedEnvPreservesExistingColorAndTermProgram(t *testing.T) {
	t.Setenv("COLORTERM", "24bit")
	t.Setenv("TERM_PROGRAM", "iTerm.app")

	m := envToMap(enrichedEnv())
	if m["COLORTERM"] != "24bit" {
		t.Fatalf("expected preserved COLORTERM=24bit, got %q", m["COLORTERM"])
	}
	if m["TERM_PROGRAM"] != "iTerm.app" {
		t.Fatalf("expected preserved TERM_PROGRAM=iTerm.app, got %q", m["TERM_PROGRAM"])
	}
}
