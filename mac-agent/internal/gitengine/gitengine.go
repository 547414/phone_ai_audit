package gitengine

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

type RepoInfo struct {
	IsRepo     bool   `json:"is_repo"`
	Root       string `json:"root,omitempty"`
	RepoName   string `json:"repo_name,omitempty"`
	Branch     string `json:"branch,omitempty"`
	Remote     string `json:"remote,omitempty"`
	Upstream   string `json:"upstream,omitempty"`
	Ahead      int    `json:"ahead"`
	Behind     int    `json:"behind"`
	DirtyCount int    `json:"dirty_count"`
	Protected  bool   `json:"protected"`
}

type ChangedFile struct {
	Path           string `json:"path"`
	OrigPath       string `json:"orig_path,omitempty"`
	IndexStatus    string `json:"index_status"`
	WorktreeStatus string `json:"worktree_status"`
	StatusCode     string `json:"status_code"` // M, A, D, R, ?, U
	Staged         bool   `json:"staged"`
	Untracked      bool   `json:"untracked"`
	Added          int    `json:"added"`
	Deleted        int    `json:"deleted"`
}

type StatusResult struct {
	Repo  RepoInfo      `json:"repo"`
	Files []ChangedFile `json:"files"`
}

type DiffResult struct {
	File      string `json:"file"`
	Staged    bool   `json:"staged"`
	Untracked bool   `json:"untracked"`
	Binary    bool   `json:"binary"`
	Truncated bool   `json:"truncated"`
	Added     int    `json:"added"`
	Deleted   int    `json:"deleted"`
	Diff      string `json:"diff"`
}

type CommitResult struct {
	SHA     string `json:"sha"`
	Short   string `json:"short_sha"`
	Summary string `json:"summary"`
	Output  string `json:"output"`
}

type PushResult struct {
	Branch    string `json:"branch"`
	Remote    string `json:"remote"`
	Output    string `json:"output"`
	Protected bool   `json:"protected"`
}

type LogEntry struct {
	SHA     string `json:"sha"`
	Short   string `json:"short_sha"`
	Author  string `json:"author"`
	Date    string `json:"date"`
	Subject string `json:"subject"`
}

func runGit(ctx context.Context, dir string, args ...string) (string, string, error) {
	cmd := exec.CommandContext(ctx, "/usr/bin/git", args...)
	cmd.Dir = dir
	env := os.Environ()
	env = append(env,
		"GIT_TERMINAL_PROMPT=0",
		"LC_ALL=en_US.UTF-8",
		"LANG=en_US.UTF-8",
	)
	cmd.Env = env
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	return stdout.String(), stderr.String(), err
}

func IsProtectedBranch(branch string) bool {
	b := strings.ToLower(strings.TrimSpace(branch))
	if b == "main" || b == "master" || b == "production" || b == "prod" {
		return true
	}
	return strings.HasPrefix(b, "release/") || strings.HasPrefix(b, "prod/") || strings.HasPrefix(b, "prod-")
}

func RepoInfoForDir(dir string) RepoInfo {
	ctx, cancel := context.WithTimeout(context.Background(), 2500*time.Millisecond)
	defer cancel()

	out, _, err := runGit(ctx, dir, "rev-parse", "--show-toplevel")
	if err != nil {
		return RepoInfo{IsRepo: false}
	}
	root := strings.TrimSpace(out)
	if root == "" {
		return RepoInfo{IsRepo: false}
	}

	info := RepoInfo{
		IsRepo:   true,
		Root:     root,
		RepoName: filepath.Base(root),
	}

	if br, _, err := runGit(ctx, root, "symbolic-ref", "--short", "HEAD"); err == nil && strings.TrimSpace(br) != "" {
		info.Branch = strings.TrimSpace(br)
	} else if sha, _, err := runGit(ctx, root, "rev-parse", "--short", "HEAD"); err == nil {
		info.Branch = "DETACHED@" + strings.TrimSpace(sha)
	} else {
		info.Branch = "main"
	}
	info.Protected = IsProtectedBranch(info.Branch)

	if up, _, err := runGit(ctx, root, "rev-parse", "--abbrev-ref", "--symbolic-full-name", "@{u}"); err == nil {
		info.Upstream = strings.TrimSpace(up)
		if idx := strings.Index(info.Upstream, "/"); idx > 0 {
			info.Remote = info.Upstream[:idx]
		}
		if cnt, _, err := runGit(ctx, root, "rev-list", "--left-right", "--count", "HEAD...@{u}"); err == nil {
			parts := strings.Fields(cnt)
			if len(parts) == 2 {
				info.Ahead, _ = strconv.Atoi(parts[0])
				info.Behind, _ = strconv.Atoi(parts[1])
			}
		}
	} else if rem, _, err := runGit(ctx, root, "remote"); err == nil {
		lines := strings.Fields(rem)
		if len(lines) > 0 {
			info.Remote = lines[0]
		}
	}

	if por, _, err := runGit(ctx, root, "status", "--porcelain=v1", "-z", "-uall"); err == nil && por != "" {
		entries := parsePorcelainZ(por)
		info.DirtyCount = len(entries)
	}

	return info
}

// FileStatusMap returns a map of absolute file/dir path -> compact status code (M, A, D, R, ?, U)
// for badge rendering inside directory listings.
func FileStatusMap(dir string) (RepoInfo, map[string]string) {
	info := RepoInfoForDir(dir)
	res := make(map[string]string)
	if !info.IsRepo {
		return info, res
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2500*time.Millisecond)
	defer cancel()
	por, _, err := runGit(ctx, info.Root, "status", "--porcelain=v1", "-z", "-uall")
	if err != nil || por == "" {
		return info, res
	}
	entries := parsePorcelainZ(por)
	info.DirtyCount = len(entries)
	for _, e := range entries {
		abs := filepath.Join(info.Root, filepath.FromSlash(e.Path))
		res[abs] = e.StatusCode
		// Mark ancestor directories up to repo root as modified so folders show badges too
		p := filepath.Dir(abs)
		for strings.HasPrefix(p, info.Root) && p != info.Root {
			if _, exists := res[p]; !exists {
				res[p] = "M"
			}
			next := filepath.Dir(p)
			if next == p {
				break
			}
			p = next
		}
	}
	return info, res
}

func Status(dir string) (StatusResult, error) {
	info := RepoInfoForDir(dir)
	if !info.IsRepo {
		return StatusResult{}, errors.New("当前目录不在 Git 仓库内")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	por, stderr, err := runGit(ctx, info.Root, "status", "--porcelain=v1", "-z", "-uall")
	if err != nil {
		return StatusResult{}, fmt.Errorf("git status failed: %s", strings.TrimSpace(stderr))
	}
	files := parsePorcelainZ(por)
	info.DirtyCount = len(files)

	// Merge line stats from working tree & staged diffs
	wtStats := parseNumstat(ctx, info.Root, false)
	stStats := parseNumstat(ctx, info.Root, true)
	for i := range files {
		if st, ok := wtStats[files[i].Path]; ok {
			files[i].Added += st[0]
			files[i].Deleted += st[1]
		}
		if st, ok := stStats[files[i].Path]; ok {
			files[i].Added += st[0]
			files[i].Deleted += st[1]
		}
	}

	return StatusResult{Repo: info, Files: files}, nil
}

func Diff(dir, relFile string, staged bool, commitSHA string) (DiffResult, error) {
	info := RepoInfoForDir(dir)
	if !info.IsRepo {
		return DiffResult{}, errors.New("当前目录不在 Git 仓库内")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()

	if commitSHA != "" {
		args := []string{"show", "--no-color", "--patch", "--stat", commitSHA}
		if relFile != "" {
			args = append(args, "--", relFile)
		}
		out, stderr, err := runGit(ctx, info.Root, args...)
		if err != nil {
			return DiffResult{}, fmt.Errorf("git show failed: %s", strings.TrimSpace(stderr))
		}
		return buildDiffResult(relFile, false, false, out), nil
	}

	if relFile == "" {
		args := []string{"diff", "--no-color", "-U3"}
		if staged {
			args = append(args, "--cached")
		}
		out, _, _ := runGit(ctx, info.Root, args...)
		return buildDiffResult("", staged, false, out), nil
	}

	cleanRel := filepath.ToSlash(filepath.Clean(relFile))
	if strings.HasPrefix(cleanRel, "../") || filepath.IsAbs(cleanRel) {
		return DiffResult{}, errors.New("非法文件路径")
	}

	// Check if file is untracked
	por, _, _ := runGit(ctx, info.Root, "status", "--porcelain=v1", "-z", "--", cleanRel)
	isUntracked := strings.HasPrefix(por, "?? ")
	if isUntracked {
		return syntheticUntrackedDiff(info.Root, cleanRel)
	}

	var out string
	if staged {
		out, _, _ = runGit(ctx, info.Root, "diff", "--no-color", "-U3", "--cached", "--", cleanRel)
	} else {
		out, _, _ = runGit(ctx, info.Root, "diff", "--no-color", "-U3", "--", cleanRel)
		if strings.TrimSpace(out) == "" {
			// Fallback to staged diff if file changes are already staged
			out, _, _ = runGit(ctx, info.Root, "diff", "--no-color", "-U3", "--cached", "--", cleanRel)
			if strings.TrimSpace(out) != "" {
				staged = true
			}
		}
	}
	return buildDiffResult(cleanRel, staged, false, out), nil
}

// Commit stages ONLY the selected files (via intent-to-add for untracked + `git commit --only`)
// so unrelated changes or pre-staged files on Mac are NEVER accidentally committed.
func Commit(dir string, files []string, message string) (CommitResult, error) {
	info := RepoInfoForDir(dir)
	if !info.IsRepo {
		return CommitResult{}, errors.New("当前目录不在 Git 仓库内")
	}
	msg := strings.TrimSpace(message)
	if msg == "" {
		return CommitResult{}, errors.New("Commit 说明不能为空")
	}
	if len(files) == 0 {
		return CommitResult{}, errors.New("请至少勾选一个要提交的文件")
	}

	cleanFiles := make([]string, 0, len(files))
	for _, f := range files {
		c := filepath.ToSlash(filepath.Clean(strings.TrimSpace(f)))
		if c == "" || c == "." || strings.HasPrefix(c, "../") || filepath.IsAbs(c) {
			return CommitResult{}, fmt.Errorf("非法路径: %s", f)
		}
		cleanFiles = append(cleanFiles, c)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	// Detect which selected files are untracked so we can mark them with `git add -N`
	st, err := Status(info.Root)
	if err != nil {
		return CommitResult{}, err
	}
	untrackedSet := make(map[string]bool)
	for _, cf := range st.Files {
		if cf.Untracked {
			untrackedSet[cf.Path] = true
		}
	}
	var toIntentAdd []string
	for _, f := range cleanFiles {
		if untrackedSet[f] {
			toIntentAdd = append(toIntentAdd, f)
		}
	}
	if len(toIntentAdd) > 0 {
		addArgs := append([]string{"add", "-N", "--"}, toIntentAdd...)
		if _, stderr, err := runGit(ctx, info.Root, addArgs...); err != nil {
			return CommitResult{}, fmt.Errorf("git add -N 失败: %s", strings.TrimSpace(stderr))
		}
	}

	commitArgs := []string{"commit", "--only", "-m", msg, "--"}
	commitArgs = append(commitArgs, cleanFiles...)
	out, stderr, err := runGit(ctx, info.Root, commitArgs...)
	if err != nil {
		// Roll back intent-to-add on failure so repo stays clean
		if len(toIntentAdd) > 0 {
			resetArgs := append([]string{"reset", "--"}, toIntentAdd...)
			_, _, _ = runGit(ctx, info.Root, resetArgs...)
		}
		errTxt := strings.TrimSpace(stderr)
		if errTxt == "" {
			errTxt = strings.TrimSpace(out)
		}
		return CommitResult{}, fmt.Errorf("git commit 失败: %s", errTxt)
	}

	sha, _, _ := runGit(ctx, info.Root, "rev-parse", "HEAD")
	short, _, _ := runGit(ctx, info.Root, "rev-parse", "--short", "HEAD")
	return CommitResult{
		SHA:     strings.TrimSpace(sha),
		Short:   strings.TrimSpace(short),
		Summary: msg,
		Output:  strings.TrimSpace(out),
	}, nil
}

// Push executes a safe non-force push. Never passes --force.
func Push(dir string) (PushResult, error) {
	info := RepoInfoForDir(dir)
	if !info.IsRepo {
		return PushResult{}, errors.New("当前目录不在 Git 仓库内")
	}
	if strings.HasPrefix(info.Branch, "DETACHED@") {
		return PushResult{}, errors.New("当前处于游离 HEAD 状态，请先在终端切换分支")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()

	var args []string
	remote := info.Remote
	if info.Upstream != "" {
		args = []string{"push"}
	} else {
		if remote == "" {
			remote = "origin"
		}
		args = []string{"push", "-u", remote, info.Branch}
	}

	out, stderr, err := runGit(ctx, info.Root, args...)
	combined := strings.TrimSpace(strings.TrimSpace(out) + "\n" + strings.TrimSpace(stderr))
	if err != nil {
		return PushResult{}, fmt.Errorf("git push 失败: %s", combined)
	}
	return PushResult{
		Branch:    info.Branch,
		Remote:    remote,
		Output:    combined,
		Protected: info.Protected,
	}, nil
}

func Pull(dir string) (string, error) {
	info := RepoInfoForDir(dir)
	if !info.IsRepo {
		return "", errors.New("当前目录不在 Git 仓库内")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	out, stderr, err := runGit(ctx, info.Root, "pull", "--ff-only")
	combined := strings.TrimSpace(strings.TrimSpace(out) + "\n" + strings.TrimSpace(stderr))
	if err != nil {
		return "", fmt.Errorf("git pull --ff-only 失败: %s", combined)
	}
	return combined, nil
}

func Discard(dir, relFile string, moveToTrash func(absPath string) error) error {
	info := RepoInfoForDir(dir)
	if !info.IsRepo {
		return errors.New("当前目录不在 Git 仓库内")
	}
	cleanRel := filepath.ToSlash(filepath.Clean(strings.TrimSpace(relFile)))
	if cleanRel == "" || strings.HasPrefix(cleanRel, "../") || filepath.IsAbs(cleanRel) {
		return errors.New("非法文件路径")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	por, _, _ := runGit(ctx, info.Root, "status", "--porcelain=v1", "-z", "--", cleanRel)
	if strings.HasPrefix(por, "?? ") {
		abs := filepath.Join(info.Root, filepath.FromSlash(cleanRel))
		if moveToTrash != nil {
			return moveToTrash(abs)
		}
		return os.Remove(abs)
	}

	// Unstage if staged, then restore working tree
	_, _, _ = runGit(ctx, info.Root, "restore", "--staged", "--", cleanRel)
	_, stderr, err := runGit(ctx, info.Root, "checkout", "--", cleanRel)
	if err != nil {
		return fmt.Errorf("撤销改动失败: %s", strings.TrimSpace(stderr))
	}
	return nil
}

func Log(dir string, limit int) ([]LogEntry, error) {
	info := RepoInfoForDir(dir)
	if !info.IsRepo {
		return nil, errors.New("当前目录不在 Git 仓库内")
	}
	if limit <= 0 || limit > 50 {
		limit = 20
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	const sep = "\x1f"
	format := strings.Join([]string{"%H", "%h", "%an", "%ad", "%s"}, sep)
	out, _, err := runGit(ctx, info.Root, "log", fmt.Sprintf("-n%d", limit), "--date=format:%m-%d %H:%M", "--format="+format)
	if err != nil {
		return []LogEntry{}, nil
	}
	entries := make([]LogEntry, 0)
	for _, line := range strings.Split(strings.TrimRight(out, "\r\n"), "\n") {
		if line == "" {
			continue
		}
		parts := strings.Split(line, sep)
		if len(parts) < 5 {
			continue
		}
		entries = append(entries, LogEntry{
			SHA:     parts[0],
			Short:   parts[1],
			Author:  parts[2],
			Date:    parts[3],
			Subject: parts[4],
		})
	}
	return entries, nil
}

func parsePorcelainZ(raw string) []ChangedFile {
	tokens := strings.Split(raw, "\x00")
	out := make([]ChangedFile, 0)
	for i := 0; i < len(tokens); i++ {
		tok := tokens[i]
		if len(tok) < 4 {
			continue
		}
		x := tok[0]
		y := tok[1]
		path := tok[3:]
		origPath := ""
		if x == 'R' || x == 'C' || y == 'R' || y == 'C' {
			if i+1 < len(tokens) {
				origPath = tokens[i+1]
				i++
			}
		}
		untracked := x == '?' && y == '?'
		staged := !untracked && x != ' ' && x != '?'
		code := pickStatusCode(x, y)
		out = append(out, ChangedFile{
			Path:           path,
			OrigPath:       origPath,
			IndexStatus:    string(x),
			WorktreeStatus: string(y),
			StatusCode:     code,
			Staged:         staged,
			Untracked:      untracked,
		})
	}
	return out
}

func pickStatusCode(x, y byte) string {
	if x == '?' && y == '?' {
		return "?"
	}
	if x == 'U' || y == 'U' || (x == 'A' && y == 'A') || (x == 'D' && y == 'D') {
		return "U"
	}
	if x == 'R' || y == 'R' {
		return "R"
	}
	if x == 'D' || y == 'D' {
		return "D"
	}
	if x == 'A' || y == 'A' {
		return "A"
	}
	return "M"
}

func parseNumstat(ctx context.Context, root string, cached bool) map[string][2]int {
	args := []string{"diff", "--numstat"}
	if cached {
		args = append(args, "--cached")
	}
	out, _, err := runGit(ctx, root, args...)
	res := make(map[string][2]int)
	if err != nil {
		return res
	}
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		if line == "" {
			continue
		}
		parts := strings.Split(line, "\t")
		if len(parts) < 3 {
			continue
		}
		add, _ := strconv.Atoi(parts[0])
		del, _ := strconv.Atoi(parts[1])
		res[parts[2]] = [2]int{add, del}
	}
	return res
}

func syntheticUntrackedDiff(root, relPath string) (DiffResult, error) {
	abs := filepath.Join(root, filepath.FromSlash(relPath))
	const maxBytes = 256 * 1024
	f, err := os.Open(abs)
	if err != nil {
		return DiffResult{}, err
	}
	defer f.Close()
	buf := make([]byte, maxBytes+1)
	n, _ := f.Read(buf)
	data := buf[:n]
	truncated := n > maxBytes
	if truncated {
		data = data[:maxBytes]
	}
	if bytes.IndexByte(data, 0) >= 0 {
		return DiffResult{File: relPath, Untracked: true, Binary: true, Diff: "Binary file (untracked)"}, nil
	}
	lines := strings.Split(strings.ReplaceAll(string(data), "\r\n", "\n"), "\n")
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("--- /dev/null\n+++ b/%s\n@@ -0,0 +1,%d @@\n", relPath, len(lines)))
	for _, ln := range lines {
		sb.WriteByte('+')
		sb.WriteString(ln)
		sb.WriteByte('\n')
	}
	return DiffResult{
		File:      relPath,
		Untracked: true,
		Truncated: truncated,
		Added:     len(lines),
		Diff:      sb.String(),
	}, nil
}

func buildDiffResult(file string, staged, untracked bool, raw string) DiffResult {
	const maxLen = 300 * 1024
	truncated := len(raw) > maxLen
	if truncated {
		raw = raw[:maxLen] + "\n... (Diff 过大已截断)\n"
	}
	add, del := 0, 0
	for _, ln := range strings.Split(raw, "\n") {
		if strings.HasPrefix(ln, "+") && !strings.HasPrefix(ln, "+++") {
			add++
		} else if strings.HasPrefix(ln, "-") && !strings.HasPrefix(ln, "---") {
			del++
		}
	}
	binary := strings.Contains(raw, "Binary files ")
	return DiffResult{
		File:      file,
		Staged:    staged,
		Untracked: untracked,
		Binary:    binary,
		Truncated: truncated,
		Added:     add,
		Deleted:   del,
		Diff:      raw,
	}
}
