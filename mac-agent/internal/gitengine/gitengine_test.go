package gitengine

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSelectiveBatchCommitOnlyCommitsCheckedFiles(t *testing.T) {
	dir := t.TempDir()
	ctx := context.Background()

	mustGit := func(args ...string) string {
		t.Helper()
		out, errOut, err := runGit(ctx, dir, args...)
		if err != nil {
			t.Fatalf("git %v failed: %v (%s)", args, err, errOut)
		}
		return out
	}

	mustGit("init", "-b", "main")
	mustGit("config", "user.name", "Test User")
	mustGit("config", "user.email", "test@example.com")

	_ = os.WriteFile(filepath.Join(dir, "a.txt"), []byte("a1\n"), 0o644)
	_ = os.WriteFile(filepath.Join(dir, "b.txt"), []byte("b1\n"), 0o644)
	_ = os.WriteFile(filepath.Join(dir, "c.txt"), []byte("c1\n"), 0o644)
	mustGit("add", ".")
	mustGit("commit", "-m", "init")

	// Modify a.txt, b.txt (pre-staged!), and create untracked d.txt
	_ = os.WriteFile(filepath.Join(dir, "a.txt"), []byte("a2\n"), 0o644)
	_ = os.WriteFile(filepath.Join(dir, "b.txt"), []byte("b2-keep-uncommitted\n"), 0o644)
	mustGit("add", "b.txt") // pre-staged on Mac, NOT selected by phone!
	_ = os.WriteFile(filepath.Join(dir, "d.txt"), []byte("new-from-ai\n"), 0o644)

	st, err := Status(dir)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if len(st.Files) != 3 {
		t.Fatalf("expected 3 changed files, got %d: %+v", len(st.Files), st.Files)
	}

	// Verify untracked synthetic diff works
	diffD, err := Diff(dir, "d.txt", false, "")
	if err != nil || !diffD.Untracked || !strings.Contains(diffD.Diff, "+new-from-ai") {
		t.Fatalf("unexpected untracked diff: %+v err=%v", diffD, err)
	}

	// Select ONLY a.txt and d.txt; b.txt must NOT be included!
	res, err := Commit(dir, []string{"a.txt", "d.txt"}, "feat: selective commit")
	if err != nil {
		t.Fatalf("Commit failed: %v", err)
	}
	if res.Short == "" {
		t.Fatalf("expected non-empty commit sha")
	}

	committedFiles := mustGit("show", "--name-only", "--pretty=format:", "HEAD")
	if !strings.Contains(committedFiles, "a.txt") || !strings.Contains(committedFiles, "d.txt") {
		t.Fatalf("expected a.txt and d.txt in commit, got: %s", committedFiles)
	}
	if strings.Contains(committedFiles, "b.txt") {
		t.Fatalf("b.txt was NOT selected and must NOT be committed! got: %s", committedFiles)
	}

	// Verify b.txt is still pending in status
	stAfter, err := Status(dir)
	if err != nil {
		t.Fatalf("Status after: %v", err)
	}
	if len(stAfter.Files) != 1 || stAfter.Files[0].Path != "b.txt" {
		t.Fatalf("expected only b.txt remaining, got %+v", stAfter.Files)
	}
}
