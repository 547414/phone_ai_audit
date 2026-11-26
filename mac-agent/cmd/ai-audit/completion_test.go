package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadAgyCompletionProfiles(t *testing.T) {
	tmp := t.TempDir()
	p := filepath.Join(tmp, "agy-profiles.json")

	if got := loadAgyCompletionProfiles(p); len(got) != 0 {
		t.Fatalf("expected empty on missing file, got %v", got)
	}

	data := `{
		"version": 2,
		"profiles": [
			{"name": "main", "email": "alice@example.com"},
			{"name": "work", "email": ""}
		]
	}`
	if err := os.WriteFile(p, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
	got := loadAgyCompletionProfiles(p)
	if len(got) != 2 || got[0] != "main\talice@example.com" || got[1] != "work\t已保存 agy 账号" {
		t.Fatalf("unexpected completion profiles: %#v", got)
	}
}

func TestInstallZshCompletionIdempotent(t *testing.T) {
	home := t.TempDir()
	rcPath := filepath.Join(home, ".zshrc")
	if err := os.WriteFile(rcPath, []byte("# existing zshrc\nexport FOO=bar\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	scriptPath, gotRC, err := installZshCompletion(home)
	if err != nil {
		t.Fatalf("installZshCompletion 1: %v", err)
	}
	if gotRC != rcPath {
		t.Fatalf("unexpected rcPath: %s", gotRC)
	}
	if _, err := os.Stat(scriptPath); err != nil {
		t.Fatalf("completion script missing: %v", err)
	}
	if _, err := os.Stat(filepath.Join(home, ".zsh", "completions", "_ai-audit")); err != nil {
		t.Fatalf("_ai-audit missing: %v", err)
	}

	rc1, _ := os.ReadFile(rcPath)
	if !strings.Contains(string(rc1), completionMarkerStart) || !strings.Contains(string(rc1), "completion.zsh") {
		t.Fatalf("zshrc missing completion block: %s", string(rc1))
	}

	// Second install must be strictly idempotent.
	if _, _, err := installZshCompletion(home); err != nil {
		t.Fatalf("installZshCompletion 2: %v", err)
	}
	rc2, _ := os.ReadFile(rcPath)
	if string(rc1) != string(rc2) {
		t.Fatalf("zshrc changed on second idempotent install:\n---1---\n%s\n---2---\n%s", string(rc1), string(rc2))
	}
	if strings.Count(string(rc2), completionMarkerStart) != 1 {
		t.Fatalf("duplicate completion block in zshrc: %s", string(rc2))
	}
}

func TestGeneratedCompletionScriptsSyntax(t *testing.T) {
	tmp := t.TempDir()
	zshFile := filepath.Join(tmp, "comp.zsh")
	bashFile := filepath.Join(tmp, "comp.bash")
	if err := os.WriteFile(zshFile, []byte(generateZshCompletion()), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(bashFile, []byte(generateBashCompletion()), 0o644); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("/bin/zsh", "-n", zshFile).CombinedOutput(); err != nil {
		t.Fatalf("zsh syntax check failed: %v\n%s", err, string(out))
	}
	if out, err := exec.Command("/bin/bash", "-n", bashFile).CombinedOutput(); err != nil {
		t.Fatalf("bash syntax check failed: %v\n%s", err, string(out))
	}
}
