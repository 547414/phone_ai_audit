package fsengine

import (
	"archive/zip"
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/csv"
	"encoding/hex"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"mime"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/xuri/excelize/v2"

	"phone-ai-audit/mac-agent/internal/gitengine"
)

var (
	ErrDisabled   = errors.New("远程文件功能已在控制面板关闭")
	ErrForbidden  = errors.New("路径超出允许访问的根目录或命中敏感红线")
	ErrConflict   = errors.New("文件已被外部修改，为防覆盖已阻止保存")
	ErrNotText    = errors.New("该文件为二进制文件，不支持文本阅读")
)

type ConflictError struct {
	CurrentSHA256 string `json:"current_sha256"`
	CurrentText   string `json:"current_text,omitempty"`
}

func (e *ConflictError) Error() string { return ErrConflict.Error() }

type Entry struct {
	Name        string `json:"name"`
	Path        string `json:"path"`
	IsDir       bool   `json:"is_dir"`
	Size        int64  `json:"size"`
	ModTime     int64  `json:"mod_time"`
	Ext         string `json:"ext,omitempty"`
	Kind        string `json:"kind"` // dir, code, markdown, image, pdf, excel, word, pptx, video, audio, binary
	GitStatus   string `json:"git_status,omitempty"`
	IsSensitive bool   `json:"is_sensitive,omitempty"`
}

type ListResult struct {
	Path       string             `json:"path"`
	ParentPath string             `json:"parent_path,omitempty"`
	Roots      []string           `json:"roots"`
	GitRepo    gitengine.RepoInfo `json:"git_repo"`
	Entries    []Entry            `json:"entries"`
}

type ReadTextResult struct {
	Path        string `json:"path"`
	Name        string `json:"name"`
	Kind        string `json:"kind"`
	Ext         string `json:"ext"`
	Size        int64  `json:"size"`
	ModTime     int64  `json:"mod_time"`
	SHA256      string `json:"sha256"`
	Content     string `json:"content"`
	TotalLines  int    `json:"total_lines"`
	OffsetLine  int    `json:"offset_line"`
	Returned    int    `json:"returned_lines"`
	Editable    bool   `json:"editable"`
	Truncated   bool   `json:"truncated"`
	HasMore     bool   `json:"has_more"`
	IsSensitive bool   `json:"is_sensitive,omitempty"`
}

type WriteTextResult struct {
	Path    string `json:"path"`
	SHA256  string `json:"sha256"`
	Size    int64  `json:"size"`
	ModTime int64  `json:"mod_time"`
}

type SearchHit struct {
	Name    string `json:"name"`
	Path    string `json:"path"`
	RelPath string `json:"rel_path"`
	IsDir   bool   `json:"is_dir,omitempty"`
	Kind    string `json:"kind"`
	Line    int    `json:"line,omitempty"`
	Text    string `json:"text,omitempty"`
}

type PDFMeta struct {
	Path  string `json:"path"`
	Name  string `json:"name"`
	Pages int    `json:"pages"`
	Size  int64  `json:"size"`
}

type ExcelMeta struct {
	Path   string   `json:"path"`
	Name   string   `json:"name"`
	Sheets []string `json:"sheets"`
	SHA256 string   `json:"sha256"`
	IsCSV  bool     `json:"is_csv"`
}

type ExcelSheetData struct {
	Path      string     `json:"path"`
	Sheet     string     `json:"sheet"`
	RowStart  int        `json:"row_start"`
	TotalRows int        `json:"total_rows"`
	TotalCols int        `json:"total_cols"`
	HasMore   bool       `json:"has_more"`
	SHA256    string     `json:"sha256"`
	Rows      [][]string `json:"rows"`
}

type SlideItem struct {
	Index int      `json:"index"`
	Title string   `json:"title"`
	Lines []string `json:"lines"`
}

type PPTXOutline struct {
	Path   string      `json:"path"`
	Name   string      `json:"name"`
	Slides []SlideItem `json:"slides"`
}

type StreamMeta struct {
	StatusCode    int
	ContentType   string
	ContentLength int64
	TotalSize     int64
	ContentRange  string
	AcceptRanges  string
}

type Engine struct {
	mu      sync.RWMutex
	enabled bool
	roots   []string
}

func New(rawRoots []string) *Engine {
	home, _ := os.UserHomeDir()
	if len(rawRoots) == 0 {
		rawRoots = []string{home}
	}
	resolved := make([]string, 0, len(rawRoots))
	for _, r := range rawRoots {
		p := expandHome(r)
		if abs, err := filepath.Abs(p); err == nil {
			p = abs
		}
		if real, err := filepath.EvalSymlinks(p); err == nil {
			p = real
		}
		resolved = append(resolved, filepath.Clean(p))
	}
	return &Engine{
		enabled: true,
		roots:   resolved,
	}
}

func (e *Engine) SetEnabled(v bool) {
	e.mu.Lock()
	e.enabled = v
	e.mu.Unlock()
}

func (e *Engine) Enabled() bool {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.enabled
}

func (e *Engine) Roots() []string {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return append([]string(nil), e.roots...)
}

func (e *Engine) checkEnabled() error {
	if !e.Enabled() {
		return ErrDisabled
	}
	return nil
}

func expandHome(p string) string {
	p = strings.TrimSpace(p)
	if p == "~" {
		h, _ := os.UserHomeDir()
		return h
	}
	if strings.HasPrefix(p, "~/") {
		h, _ := os.UserHomeDir()
		return filepath.Join(h, p[2:])
	}
	return p
}

func (e *Engine) ResolvePath(reqPath string, allowMissingLeaf bool) (string, error) {
	if err := e.checkEnabled(); err != nil {
		return "", err
	}
	roots := e.Roots()
	if len(roots) == 0 {
		return "", ErrForbidden
	}
	if strings.TrimSpace(reqPath) == "" {
		return roots[0], nil
	}

	p := expandHome(reqPath)
	if !filepath.IsAbs(p) {
		p = filepath.Join(roots[0], p)
	}
	p = filepath.Clean(p)

	var realPath string
	if resolved, err := filepath.EvalSymlinks(p); err == nil {
		realPath = filepath.Clean(resolved)
	} else {
		// Walk up ancestors until an existing directory can be symlink-resolved,
		// so out-of-root paths ALWAYS return ErrForbidden instead of leaking existence.
		cur := p
		var tail []string
		for {
			parent := filepath.Dir(cur)
			tail = append([]string{filepath.Base(cur)}, tail...)
			if parent == cur {
				return "", ErrForbidden
			}
			if parentReal, perr := filepath.EvalSymlinks(parent); perr == nil {
				parts := append([]string{filepath.Clean(parentReal)}, tail...)
				cand := filepath.Clean(filepath.Join(parts...))
				if !isWithinRoots(cand, roots) || isHardBlockedPath(cand) {
					return "", ErrForbidden
				}
				if allowMissingLeaf && os.IsNotExist(err) && len(tail) == 1 {
					realPath = cand
					break
				}
				if allowMissingLeaf && os.IsNotExist(err) {
					// e.g. Mkdir creating nested directories under an allowed root
					realPath = cand
					break
				}
				return "", err
			}
			cur = parent
		}
	}

	if !isWithinRoots(realPath, roots) {
		return "", ErrForbidden
	}
	if isHardBlockedPath(realPath) {
		return "", ErrForbidden
	}
	return realPath, nil
}

func isWithinRoots(target string, roots []string) bool {
	for _, root := range roots {
		if target == root || strings.HasPrefix(target, root+string(os.PathSeparator)) {
			return true
		}
	}
	return false
}

func isHardBlockedPath(absPath string) bool {
	lower := strings.ToLower(absPath)
	base := strings.ToLower(filepath.Base(absPath))
	if strings.Contains(lower, "/.ssh/") || strings.HasSuffix(lower, "/.ssh") ||
		strings.Contains(lower, "/ssh_key/") || strings.HasSuffix(lower, "/ssh_key") {
		return true
	}
	if base == "id_rsa" || base == "id_ed25519" || base == "id_ecdsa" ||
		strings.HasSuffix(base, ".pem") || strings.HasSuffix(base, ".key") ||
		strings.HasSuffix(base, ".p12") || strings.HasSuffix(base, ".pfx") {
		return true
	}
	return false
}

func isSensitiveFile(name string) bool {
	l := strings.ToLower(name)
	return l == ".env" || strings.HasPrefix(l, ".env.") ||
		strings.Contains(l, "secret") || strings.Contains(l, "credential") ||
		strings.Contains(l, "token")
}

func isGitInternalWrite(absPath string) bool {
	norm := filepath.ToSlash(absPath)
	return strings.Contains(norm, "/.git/") || strings.HasSuffix(norm, "/.git")
}

func ClassifyKind(name string, isDir bool) string {
	if isDir {
		return "dir"
	}
	ext := strings.ToLower(filepath.Ext(name))
	switch ext {
	case ".md", ".markdown":
		return "markdown"
	case ".png", ".jpg", ".jpeg", ".webp", ".gif", ".bmp", ".heic", ".svg":
		return "image"
	case ".pdf":
		return "pdf"
	case ".xlsx", ".csv", ".tsv":
		return "excel"
	case ".docx", ".doc", ".rtf":
		return "word"
	case ".pptx":
		return "pptx"
	case ".mp4", ".mov", ".m4v", ".webm", ".mkv":
		return "video"
	case ".mp3", ".m4a", ".wav", ".flac", ".ogg", ".aac":
		return "audio"
	case ".go", ".kt", ".kts", ".java", ".ts", ".tsx", ".js", ".jsx", ".mjs", ".cjs",
		".py", ".rs", ".c", ".h", ".cpp", ".hpp", ".swift", ".sh", ".zsh", ".bash",
		".json", ".yaml", ".yml", ".toml", ".xml", ".html", ".htm", ".css", ".scss",
		".sql", ".txt", ".log", ".ini", ".conf", ".cfg", ".properties", ".gradle",
		".dockerfile", ".gitignore", ".env", ".vue", ".svelte", ".php", ".rb":
		return "code"
	}
	lower := strings.ToLower(name)
	if lower == "dockerfile" || lower == "makefile" || lower == "readme" || strings.HasPrefix(lower, ".env") {
		return "code"
	}
	return "binary"
}

func (e *Engine) List(reqPath string, showHidden bool, sortBy string) (ListResult, error) {
	dir, err := e.ResolvePath(reqPath, false)
	if err != nil {
		return ListResult{}, err
	}
	fi, err := os.Stat(dir)
	if err != nil {
		return ListResult{}, err
	}
	if !fi.IsDir() {
		dir = filepath.Dir(dir)
	}

	dirEntries, err := os.ReadDir(dir)
	if err != nil {
		return ListResult{}, err
	}

	repoInfo, gitMap := gitengine.FileStatusMap(dir)

	entries := make([]Entry, 0, len(dirEntries))
	for _, de := range dirEntries {
		name := de.Name()
		if !showHidden && strings.HasPrefix(name, ".") {
			continue
		}
		full := filepath.Join(dir, name)
		if isHardBlockedPath(full) {
			continue
		}
		info, err := de.Info()
		if err != nil {
			continue
		}
		isDir := info.IsDir()
		// Resolve symlink dir target carefully
		if info.Mode()&os.ModeSymlink != 0 {
			if st, serr := os.Stat(full); serr == nil {
				isDir = st.IsDir()
				info = st
			}
		}
		entries = append(entries, Entry{
			Name:        name,
			Path:        full,
			IsDir:       isDir,
			Size:        info.Size(),
			ModTime:     info.ModTime().UnixMilli(),
			Ext:         strings.ToLower(filepath.Ext(name)),
			Kind:        ClassifyKind(name, isDir),
			GitStatus:   gitMap[full],
			IsSensitive: !isDir && isSensitiveFile(name),
		})
	}

	sort.Slice(entries, func(i, j int) bool {
		if entries[i].IsDir != entries[j].IsDir {
			return entries[i].IsDir
		}
		switch sortBy {
		case "mtime":
			if entries[i].ModTime != entries[j].ModTime {
				return entries[i].ModTime > entries[j].ModTime
			}
		case "size":
			if entries[i].Size != entries[j].Size {
				return entries[i].Size > entries[j].Size
			}
		}
		return strings.ToLower(entries[i].Name) < strings.ToLower(entries[j].Name)
	})

	parent := ""
	p := filepath.Dir(dir)
	if p != dir && isWithinRoots(p, e.Roots()) {
		parent = p
	}

	return ListResult{
		Path:       dir,
		ParentPath: parent,
		Roots:      e.Roots(),
		GitRepo:    repoInfo,
		Entries:    entries,
	}, nil
}

func (e *Engine) Search(reqPath, query, mode string) ([]SearchHit, error) {
	dir, err := e.ResolvePath(reqPath, false)
	if err != nil {
		return nil, err
	}
	q := strings.TrimSpace(query)
	if q == "" {
		return []SearchHit{}, nil
	}

	if mode == "content" {
		return e.searchContentRG(dir, q)
	}
	return e.searchByName(dir, q)
}

func (e *Engine) searchByName(root, query string) ([]SearchHit, error) {
	ql := strings.ToLower(query)
	hits := make([]SearchHit, 0, 32)
	skipDirs := map[string]bool{
		".git": true, "node_modules": true, "build": true, ".gradle": true,
		"dist": true, "__pycache__": true, ".venv": true, "target": true,
	}
	_ = filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil || p == root {
			return nil
		}
		name := d.Name()
		if d.IsDir() && skipDirs[name] {
			return filepath.SkipDir
		}
		if isHardBlockedPath(p) {
			return nil
		}
		if strings.Contains(strings.ToLower(name), ql) {
			rel, _ := filepath.Rel(root, p)
			hits = append(hits, SearchHit{
				Name:    name,
				Path:    p,
				RelPath: filepath.ToSlash(rel),
				IsDir:   d.IsDir(),
				Kind:    ClassifyKind(name, d.IsDir()),
			})
			if len(hits) >= 80 {
				return io.EOF
			}
		}
		return nil
	})
	return hits, nil
}

func (e *Engine) searchContentRG(root, query string) ([]SearchHit, error) {
	rgBin := "/opt/homebrew/bin/rg"
	if _, err := os.Stat(rgBin); err != nil {
		if found, lerr := exec.LookPath("rg"); lerr == nil {
			rgBin = found
		} else {
			return nil, errors.New("未找到 ripgrep (rg)")
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, rgBin,
		"--json", "-n", "-i", "--max-count", "4", "--max-filesize", "1M",
		"--glob", "!.git/*", "--glob", "!node_modules/*", "--glob", "!build/*",
		"--", query, root,
	)
	out, _ := cmd.Output()
	hits := make([]SearchHit, 0, 40)
	scanner := bufio.NewScanner(bytes.NewReader(out))
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)
	for scanner.Scan() {
		var ev struct {
			Type string `json:"type"`
			Data struct {
				Path struct {
					Text string `json:"text"`
				} `json:"path"`
				Lines struct {
					Text string `json:"text"`
				} `json:"lines"`
				LineNumber int `json:"line_number"`
			} `json:"data"`
		}
		if json.Unmarshal(scanner.Bytes(), &ev) != nil || ev.Type != "match" {
			continue
		}
		p := ev.Data.Path.Text
		if isHardBlockedPath(p) {
			continue
		}
		rel, _ := filepath.Rel(root, p)
		hits = append(hits, SearchHit{
			Name:    filepath.Base(p),
			Path:    p,
			RelPath: filepath.ToSlash(rel),
			Kind:    ClassifyKind(filepath.Base(p), false),
			Line:    ev.Data.LineNumber,
			Text:    strings.TrimSpace(ev.Data.Lines.Text),
		})
		if len(hits) >= 80 {
			break
		}
	}
	return hits, nil
}

func fileSHA256(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func (e *Engine) ReadText(reqPath string, offsetLine, limitLines int, tail bool) (ReadTextResult, error) {
	p, err := e.ResolvePath(reqPath, false)
	if err != nil {
		return ReadTextResult{}, err
	}
	fi, err := os.Stat(p)
	if err != nil {
		return ReadTextResult{}, err
	}
	if fi.IsDir() {
		return ReadTextResult{}, errors.New("目标是一个目录")
	}

	f, err := os.Open(p)
	if err != nil {
		return ReadTextResult{}, err
	}
	defer f.Close()

	// Binary check on first 8KB
	probe := make([]byte, 8192)
	pn, _ := f.Read(probe)
	if bytes.IndexByte(probe[:pn], 0) >= 0 {
		return ReadTextResult{}, ErrNotText
	}
	_, _ = f.Seek(0, io.SeekStart)

	const editableMaxBytes = 350 * 1024
	if limitLines <= 0 || limitLines > 1000 {
		limitLines = 400
	}
	if offsetLine <= 0 {
		offsetLine = 1
	}

	name := filepath.Base(p)
	kind := ClassifyKind(name, false)
	if kind == "binary" {
		kind = "code"
	}

	// Small/normal file: load completely so it can be edited directly with sha256 lock
	if fi.Size() <= editableMaxBytes && offsetLine == 1 && !tail {
		data, err := io.ReadAll(f)
		if err != nil {
			return ReadTextResult{}, err
		}
		sum := sha256.Sum256(data)
		text := string(data)
		totalLines := strings.Count(text, "\n") + 1
		return ReadTextResult{
			Path:        p,
			Name:        name,
			Kind:        kind,
			Ext:         strings.ToLower(filepath.Ext(name)),
			Size:        fi.Size(),
			ModTime:     fi.ModTime().UnixMilli(),
			SHA256:      hex.EncodeToString(sum[:]),
			Content:     text,
			TotalLines:  totalLines,
			OffsetLine:  1,
			Returned:    totalLines,
			Editable:    true,
			Truncated:   false,
			HasMore:     false,
			IsSensitive: isSensitiveFile(name),
		}, nil
	}

	// Large file / paginated or tail read
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 64*1024), 2*1024*1024)
	if tail {
		ring := make([]string, 0, limitLines)
		total := 0
		for scanner.Scan() {
			total++
			if len(ring) < limitLines {
				ring = append(ring, scanner.Text())
			} else {
				copy(ring, ring[1:])
				ring[len(ring)-1] = scanner.Text()
			}
		}
		startLine := total - len(ring) + 1
		if startLine < 1 {
			startLine = 1
		}
		return ReadTextResult{
			Path:        p,
			Name:        name,
			Kind:        kind,
			Ext:         strings.ToLower(filepath.Ext(name)),
			Size:        fi.Size(),
			ModTime:     fi.ModTime().UnixMilli(),
			Content:     strings.Join(ring, "\n"),
			TotalLines:  total,
			OffsetLine:  startLine,
			Returned:    len(ring),
			Editable:    false,
			Truncated:   true,
			HasMore:     false,
			IsSensitive: isSensitiveFile(name),
		}, nil
	}

	lines := make([]string, 0, limitLines)
	total := 0
	for scanner.Scan() {
		total++
		if total >= offsetLine && len(lines) < limitLines {
			lines = append(lines, scanner.Text())
		}
	}
	return ReadTextResult{
		Path:        p,
		Name:        name,
		Kind:        kind,
		Ext:         strings.ToLower(filepath.Ext(name)),
		Size:        fi.Size(),
		ModTime:     fi.ModTime().UnixMilli(),
		Content:     strings.Join(lines, "\n"),
		TotalLines:  total,
		OffsetLine:  offsetLine,
		Returned:    len(lines),
		Editable:    false,
		Truncated:   true,
		HasMore:     offsetLine+len(lines)-1 < total,
		IsSensitive: isSensitiveFile(name),
	}, nil
}

func (e *Engine) WriteText(reqPath, content, expectedSHA string) (WriteTextResult, error) {
	p, err := e.ResolvePath(reqPath, true)
	if err != nil {
		return WriteTextResult{}, err
	}
	if isGitInternalWrite(p) {
		return WriteTextResult{}, ErrForbidden
	}

	if fi, statErr := os.Stat(p); statErr == nil {
		if fi.IsDir() {
			return WriteTextResult{}, errors.New("不能覆盖目录")
		}
		if expectedSHA != "" {
			curSHA, _ := fileSHA256(p)
			if curSHA != "" && curSHA != expectedSHA {
				curBytes, _ := os.ReadFile(p)
				curTxt := ""
				if len(curBytes) <= 350*1024 {
					curTxt = string(curBytes)
				}
				return WriteTextResult{}, &ConflictError{
					CurrentSHA256: curSHA,
					CurrentText:   curTxt,
				}
			}
		}
	}

	// Atomic temp file + rename
	dir := filepath.Dir(p)
	tmp, err := os.CreateTemp(dir, ".ai-audit-tmp-*")
	if err != nil {
		return WriteTextResult{}, err
	}
	tmpName := tmp.Name()
	data := []byte(content)
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
		return WriteTextResult{}, err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
		return WriteTextResult{}, err
	}
	_ = tmp.Close()

	// Preserve mode if file already exists
	if fi, err := os.Stat(p); err == nil {
		_ = os.Chmod(tmpName, fi.Mode())
	} else {
		_ = os.Chmod(tmpName, 0o644)
	}
	if err := os.Rename(tmpName, p); err != nil {
		_ = os.Remove(tmpName)
		return WriteTextResult{}, err
	}

	sum := sha256.Sum256(data)
	fi, _ := os.Stat(p)
	modMs := time.Now().UnixMilli()
	if fi != nil {
		modMs = fi.ModTime().UnixMilli()
	}
	return WriteTextResult{
		Path:    p,
		SHA256:  hex.EncodeToString(sum[:]),
		Size:    int64(len(data)),
		ModTime: modMs,
	}, nil
}

func (e *Engine) Mkdir(reqPath string) (string, error) {
	p, err := e.ResolvePath(reqPath, true)
	if err != nil {
		return "", err
	}
	if isGitInternalWrite(p) {
		return "", ErrForbidden
	}
	if err := os.MkdirAll(p, 0o755); err != nil {
		return "", err
	}
	return p, nil
}

func (e *Engine) Touch(reqPath string) (string, error) {
	p, err := e.ResolvePath(reqPath, true)
	if err != nil {
		return "", err
	}
	if isGitInternalWrite(p) {
		return "", ErrForbidden
	}
	if _, err := os.Stat(p); err == nil {
		return "", errors.New("文件已存在")
	}
	if err := os.WriteFile(p, []byte{}, 0o644); err != nil {
		return "", err
	}
	return p, nil
}

func (e *Engine) Rename(oldPath, newName string) (string, error) {
	src, err := e.ResolvePath(oldPath, false)
	if err != nil {
		return "", err
	}
	if isGitInternalWrite(src) {
		return "", ErrForbidden
	}
	cleanName := strings.TrimSpace(newName)
	if cleanName == "" || strings.Contains(cleanName, "/") || cleanName == "." || cleanName == ".." {
		return "", errors.New("非法文件名")
	}
	dst, err := e.ResolvePath(filepath.Join(filepath.Dir(src), cleanName), true)
	if err != nil {
		return "", err
	}
	if _, err := os.Stat(dst); err == nil {
		return "", errors.New("目标名称已存在")
	}
	if err := os.Rename(src, dst); err != nil {
		return "", err
	}
	return dst, nil
}

func (e *Engine) MoveToTrash(reqPath string) error {
	p, err := e.ResolvePath(reqPath, false)
	if err != nil {
		return err
	}
	for _, r := range e.Roots() {
		if p == r {
			return errors.New("不能删除根目录")
		}
	}
	return MoveAbsPathToTrash(p)
}

func MoveAbsPathToTrash(absPath string) error {
	home, _ := os.UserHomeDir()
	trashDir := filepath.Join(home, ".Trash")
	_ = os.MkdirAll(trashDir, 0o700)
	base := filepath.Base(absPath)
	dst := filepath.Join(trashDir, base)
	if _, err := os.Stat(dst); err == nil {
		ext := filepath.Ext(base)
		stem := strings.TrimSuffix(base, ext)
		dst = filepath.Join(trashDir, fmt.Sprintf("%s-%s%s", stem, time.Now().Format("20060102-150405"), ext))
	}
	return os.Rename(absPath, dst)
}

// --- PDF via poppler (pdfinfo / pdftoppm) ---

func findBin(name string) string {
	for _, dir := range []string{"/opt/homebrew/bin", "/usr/local/bin", "/usr/bin"} {
		cand := filepath.Join(dir, name)
		if _, err := os.Stat(cand); err == nil {
			return cand
		}
	}
	if p, err := exec.LookPath(name); err == nil {
		return p
	}
	return ""
}

var pdfPagesRe = regexp.MustCompile(`(?m)^Pages:\s+(\d+)`)

func (e *Engine) PDFMeta(reqPath string) (PDFMeta, error) {
	p, err := e.ResolvePath(reqPath, false)
	if err != nil {
		return PDFMeta{}, err
	}
	fi, err := os.Stat(p)
	if err != nil {
		return PDFMeta{}, err
	}
	pdfinfoBin := findBin("pdfinfo")
	if pdfinfoBin == "" {
		return PDFMeta{}, errors.New("Mac 未安装 poppler (pdfinfo)")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, pdfinfoBin, p).CombinedOutput()
	if err != nil {
		return PDFMeta{}, fmt.Errorf("pdfinfo 失败: %s", strings.TrimSpace(string(out)))
	}
	pages := 1
	if m := pdfPagesRe.FindSubmatch(out); len(m) == 2 {
		if n, err := strconv.Atoi(string(m[1])); err == nil && n > 0 {
			pages = n
		}
	}
	return PDFMeta{
		Path:  p,
		Name:  filepath.Base(p),
		Pages: pages,
		Size:  fi.Size(),
	}, nil
}

// --- Excel / CSV via excelize & encoding/csv ---

func (e *Engine) ExcelMeta(reqPath string) (ExcelMeta, error) {
	p, err := e.ResolvePath(reqPath, false)
	if err != nil {
		return ExcelMeta{}, err
	}
	sha, err := fileSHA256(p)
	if err != nil {
		return ExcelMeta{}, err
	}
	ext := strings.ToLower(filepath.Ext(p))
	if ext == ".csv" || ext == ".tsv" {
		return ExcelMeta{
			Path:   p,
			Name:   filepath.Base(p),
			Sheets: []string{filepath.Base(p)},
			SHA256: sha,
			IsCSV:  true,
		}, nil
	}
	f, err := excelize.OpenFile(p)
	if err != nil {
		return ExcelMeta{}, fmt.Errorf("打开 Excel 失败: %w", err)
	}
	defer f.Close()
	return ExcelMeta{
		Path:   p,
		Name:   filepath.Base(p),
		Sheets: f.GetSheetList(),
		SHA256: sha,
		IsCSV:  false,
	}, nil
}

func (e *Engine) ExcelSheet(reqPath, sheet string, rowStart, limit int) (ExcelSheetData, error) {
	p, err := e.ResolvePath(reqPath, false)
	if err != nil {
		return ExcelSheetData{}, err
	}
	sha, _ := fileSHA256(p)
	if rowStart <= 0 {
		rowStart = 1
	}
	if limit <= 0 || limit > 300 {
		limit = 150
	}

	ext := strings.ToLower(filepath.Ext(p))
	var allRows [][]string
	sheetName := sheet
	if ext == ".csv" || ext == ".tsv" {
		raw, err := os.ReadFile(p)
		if err != nil {
			return ExcelSheetData{}, err
		}
		r := csv.NewReader(bytes.NewReader(raw))
		if ext == ".tsv" {
			r.Comma = '\t'
		}
		r.FieldsPerRecord = -1
		r.LazyQuotes = true
		allRows, err = r.ReadAll()
		if err != nil {
			return ExcelSheetData{}, err
		}
		sheetName = filepath.Base(p)
	} else {
		f, err := excelize.OpenFile(p)
		if err != nil {
			return ExcelSheetData{}, err
		}
		defer f.Close()
		sheets := f.GetSheetList()
		if len(sheets) == 0 {
			return ExcelSheetData{Path: p, SHA256: sha, Rows: [][]string{}}, nil
		}
		if sheetName == "" {
			sheetName = sheets[0]
		}
		allRows, err = f.GetRows(sheetName)
		if err != nil {
			return ExcelSheetData{}, err
		}
	}

	totalRows := len(allRows)
	maxCols := 0
	for _, r := range allRows {
		if len(r) > maxCols {
			maxCols = len(r)
		}
	}
	if maxCols > 50 {
		maxCols = 50
	}

	startIdx := rowStart - 1
	if startIdx > totalRows {
		startIdx = totalRows
	}
	endIdx := startIdx + limit
	if endIdx > totalRows {
		endIdx = totalRows
	}

	pageRows := make([][]string, 0, endIdx-startIdx)
	for _, r := range allRows[startIdx:endIdx] {
		row := make([]string, maxCols)
		for c := 0; c < len(r) && c < maxCols; c++ {
			row[c] = r[c]
		}
		pageRows = append(pageRows, row)
	}

	return ExcelSheetData{
		Path:      p,
		Sheet:     sheetName,
		RowStart:  rowStart,
		TotalRows: totalRows,
		TotalCols: maxCols,
		HasMore:   endIdx < totalRows,
		SHA256:    sha,
		Rows:      pageRows,
	}, nil
}

func (e *Engine) ExcelWriteCell(reqPath, sheet string, row1, col1 int, value, expectedSHA string) (WriteTextResult, error) {
	p, err := e.ResolvePath(reqPath, false)
	if err != nil {
		return WriteTextResult{}, err
	}
	if row1 < 1 || col1 < 1 {
		return WriteTextResult{}, errors.New("无效的单元格坐标")
	}
	if expectedSHA != "" {
		curSHA, _ := fileSHA256(p)
		if curSHA != "" && curSHA != expectedSHA {
			return WriteTextResult{}, &ConflictError{CurrentSHA256: curSHA}
		}
	}

	ext := strings.ToLower(filepath.Ext(p))
	if ext == ".csv" || ext == ".tsv" {
		raw, err := os.ReadFile(p)
		if err != nil {
			return WriteTextResult{}, err
		}
		r := csv.NewReader(bytes.NewReader(raw))
		if ext == ".tsv" {
			r.Comma = '\t'
		}
		r.FieldsPerRecord = -1
		r.LazyQuotes = true
		rows, err := r.ReadAll()
		if err != nil {
			return WriteTextResult{}, err
		}
		for len(rows) < row1 {
			rows = append(rows, []string{})
		}
		for len(rows[row1-1]) < col1 {
			rows[row1-1] = append(rows[row1-1], "")
		}
		rows[row1-1][col1-1] = value
		var buf bytes.Buffer
		w := csv.NewWriter(&buf)
		if ext == ".tsv" {
			w.Comma = '\t'
		}
		_ = w.WriteAll(rows)
		w.Flush()
		return e.WriteText(p, buf.String(), expectedSHA)
	}

	f, err := excelize.OpenFile(p)
	if err != nil {
		return WriteTextResult{}, err
	}
	defer f.Close()
	if sheet == "" {
		sheets := f.GetSheetList()
		if len(sheets) == 0 {
			return WriteTextResult{}, errors.New("Excel 无工作表")
		}
		sheet = sheets[0]
	}
	coord, err := excelize.CoordinatesToCellName(col1, row1)
	if err != nil {
		return WriteTextResult{}, err
	}
	if err := f.SetCellValue(sheet, coord, value); err != nil {
		return WriteTextResult{}, err
	}
	if err := f.Save(); err != nil {
		return WriteTextResult{}, err
	}
	newSHA, _ := fileSHA256(p)
	fi, _ := os.Stat(p)
	return WriteTextResult{
		Path:    p,
		SHA256:  newSHA,
		Size:    fi.Size(),
		ModTime: fi.ModTime().UnixMilli(),
	}, nil
}

// --- PPTX outline parser (pure Go zip + XML) ---

var slideNumRe = regexp.MustCompile(`slide(\d+)\.xml$`)

func (e *Engine) PPTXOutline(reqPath string) (PPTXOutline, error) {
	p, err := e.ResolvePath(reqPath, false)
	if err != nil {
		return PPTXOutline{}, err
	}
	zr, err := zip.OpenReader(p)
	if err != nil {
		return PPTXOutline{}, fmt.Errorf("打开 PPTX 失败: %w", err)
	}
	defer zr.Close()

	type slideFile struct {
		num int
		f   *zip.File
	}
	var slides []slideFile
	for _, f := range zr.File {
		if strings.HasPrefix(f.Name, "ppt/slides/slide") && strings.HasSuffix(f.Name, ".xml") {
			if m := slideNumRe.FindStringSubmatch(f.Name); len(m) == 2 {
				n, _ := strconv.Atoi(m[1])
				slides = append(slides, slideFile{num: n, f: f})
			}
		}
	}
	sort.Slice(slides, func(i, j int) bool { return slides[i].num < slides[j].num })

	out := PPTXOutline{
		Path:   p,
		Name:   filepath.Base(p),
		Slides: make([]SlideItem, 0, len(slides)),
	}
	for _, sf := range slides {
		rc, err := sf.f.Open()
		if err != nil {
			continue
		}
		lines := extractPPTXText(rc)
		_ = rc.Close()
		title := fmt.Sprintf("第 %d 页", sf.num)
		body := lines
		if len(lines) > 0 {
			title = lines[0]
			body = lines[1:]
		}
		out.Slides = append(out.Slides, SlideItem{
			Index: sf.num,
			Title: title,
			Lines: body,
		})
	}
	return out, nil
}

func extractPPTXText(r io.Reader) []string {
	dec := xml.NewDecoder(r)
	var lines []string
	var curPara strings.Builder
	inText := false
	for {
		tok, err := dec.Token()
		if err != nil {
			break
		}
		switch t := tok.(type) {
		case xml.StartElement:
			if t.Name.Local == "t" {
				inText = true
			}
		case xml.EndElement:
			if t.Name.Local == "t" {
				inText = false
			} else if t.Name.Local == "p" {
				s := strings.TrimSpace(curPara.String())
				if s != "" {
					lines = append(lines, s)
				}
				curPara.Reset()
			}
		case xml.CharData:
			if inText {
				curPara.Write(t)
			}
		}
	}
	return lines
}

// --- Zero-disk-cache streaming for images, PDF single page, Word HTML, and Range raw/media ---

func (e *Engine) OpenStream(ctx context.Context, reqPath, kind string, page, dpi, maxDim int, rangeHdr string) (io.ReadCloser, StreamMeta, error) {
	p, err := e.ResolvePath(reqPath, false)
	if err != nil {
		return nil, StreamMeta{}, err
	}

	switch kind {
	case "pdf_page":
		return e.openPDFPageStream(ctx, p, page, dpi)
	case "image":
		return e.openImageStream(ctx, p, maxDim)
	case "docx_html":
		return e.openDocxHTMLStream(ctx, p)
	default:
		return e.openRawRangeStream(p, rangeHdr)
	}
}

func (e *Engine) openPDFPageStream(ctx context.Context, absPath string, page, dpi int) (io.ReadCloser, StreamMeta, error) {
	pdftoppm := findBin("pdftoppm")
	if pdftoppm == "" {
		return nil, StreamMeta{}, errors.New("Mac 未安装 poppler (pdftoppm)")
	}
	if page <= 0 {
		page = 1
	}
	if dpi < 72 || dpi > 300 {
		dpi = 160
	}
	tmpDir, err := os.MkdirTemp("", "ai-audit-pdf-*")
	if err != nil {
		return nil, StreamMeta{}, err
	}
	prefix := filepath.Join(tmpDir, "page")
	cctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	cmd := exec.CommandContext(cctx, pdftoppm,
		"-jpeg", "-jpegopt", "quality=85",
		"-r", strconv.Itoa(dpi),
		"-f", strconv.Itoa(page),
		"-l", strconv.Itoa(page),
		"-singlefile",
		absPath, prefix,
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		_ = os.RemoveAll(tmpDir)
		return nil, StreamMeta{}, fmt.Errorf("pdftoppm 渲染失败: %s", strings.TrimSpace(string(out)))
	}
	jpgPath := prefix + ".jpg"
	data, err := os.ReadFile(jpgPath)
	_ = os.RemoveAll(tmpDir)
	if err != nil {
		return nil, StreamMeta{}, err
	}
	return io.NopCloser(bytes.NewReader(data)), StreamMeta{
		StatusCode:    200,
		ContentType:   "image/jpeg",
		ContentLength: int64(len(data)),
		TotalSize:     int64(len(data)),
	}, nil
}

func (e *Engine) openImageStream(ctx context.Context, absPath string, maxDim int) (io.ReadCloser, StreamMeta, error) {
	ext := strings.ToLower(filepath.Ext(absPath))
	// Stream SVG/GIF or original full image directly
	if maxDim <= 0 || ext == ".svg" || ext == ".gif" {
		return e.openRawRangeStream(absPath, "")
	}
	if maxDim > 3200 {
		maxDim = 3200
	}
	tmpFile, err := os.CreateTemp("", "ai-audit-img-*.jpg")
	if err != nil {
		return e.openRawRangeStream(absPath, "")
	}
	tmpPath := tmpFile.Name()
	_ = tmpFile.Close()

	cctx, cancel := context.WithTimeout(ctx, 12*time.Second)
	defer cancel()
	cmd := exec.CommandContext(cctx, "/usr/bin/sips",
		"-Z", strconv.Itoa(maxDim),
		"-s", "format", "jpeg",
		"-s", "formatOptions", "82",
		absPath, "--out", tmpPath,
	)
	if err := cmd.Run(); err != nil {
		_ = os.Remove(tmpPath)
		return e.openRawRangeStream(absPath, "")
	}
	data, err := os.ReadFile(tmpPath)
	_ = os.Remove(tmpPath)
	if err != nil || len(data) == 0 {
		return e.openRawRangeStream(absPath, "")
	}
	return io.NopCloser(bytes.NewReader(data)), StreamMeta{
		StatusCode:    200,
		ContentType:   "image/jpeg",
		ContentLength: int64(len(data)),
		TotalSize:     int64(len(data)),
	}, nil
}

func (e *Engine) openDocxHTMLStream(ctx context.Context, absPath string) (io.ReadCloser, StreamMeta, error) {
	cctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	out, err := exec.CommandContext(cctx, "/usr/bin/textutil", "-convert", "html", "-stdout", absPath).Output()
	if err != nil {
		return nil, StreamMeta{}, fmt.Errorf("textutil 转换失败: %w", err)
	}
	return io.NopCloser(bytes.NewReader(out)), StreamMeta{
		StatusCode:    200,
		ContentType:   "text/html; charset=utf-8",
		ContentLength: int64(len(out)),
		TotalSize:     int64(len(out)),
	}, nil
}

func (e *Engine) openRawRangeStream(absPath, rangeHdr string) (io.ReadCloser, StreamMeta, error) {
	f, err := os.Open(absPath)
	if err != nil {
		return nil, StreamMeta{}, err
	}
	fi, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return nil, StreamMeta{}, err
	}
	total := fi.Size()
	ct := detectContentType(absPath)

	if strings.HasPrefix(rangeHdr, "bytes=") && total > 0 {
		spec := strings.TrimPrefix(rangeHdr, "bytes=")
		if idx := strings.Index(spec, ","); idx >= 0 {
			spec = spec[:idx]
		}
		parts := strings.SplitN(spec, "-", 2)
		if len(parts) == 2 {
			start, err1 := strconv.ParseInt(strings.TrimSpace(parts[0]), 10, 64)
			end := total - 1
			openEnded := strings.TrimSpace(parts[1]) == ""
			if !openEnded {
				if parsedEnd, err2 := strconv.ParseInt(strings.TrimSpace(parts[1]), 10, 64); err2 == nil {
					end = parsedEnd
				}
			}
			if err1 == nil && start >= 0 && start < total {
				const maxOpenRangeChunk = 2 * 1024 * 1024
				if openEnded && end-start+1 > maxOpenRangeChunk {
					end = start + maxOpenRangeChunk - 1
				}
				if end >= total {
					end = total - 1
				}
				if end < start {
					end = start
				}
				length := end - start + 1
				if _, err := f.Seek(start, io.SeekStart); err != nil {
					_ = f.Close()
					return nil, StreamMeta{}, err
				}
				return &limitedReadCloser{r: io.LimitReader(f, length), c: f}, StreamMeta{
					StatusCode:    206,
					ContentType:   ct,
					ContentLength: length,
					TotalSize:     total,
					ContentRange:  fmt.Sprintf("bytes %d-%d/%d", start, end, total),
					AcceptRanges:  "bytes",
				}, nil
			}
		}
	}

	return f, StreamMeta{
		StatusCode:    200,
		ContentType:   ct,
		ContentLength: total,
		TotalSize:     total,
		AcceptRanges:  "bytes",
	}, nil
}

type limitedReadCloser struct {
	r io.Reader
	c io.Closer
}

func (l *limitedReadCloser) Read(p []byte) (int, error) { return l.r.Read(p) }
func (l *limitedReadCloser) Close() error               { return l.c.Close() }

func detectContentType(absPath string) string {
	ext := strings.ToLower(filepath.Ext(absPath))
	switch ext {
	case ".mp4", ".m4v":
		return "video/mp4"
	case ".mov":
		return "video/quicktime"
	case ".webm":
		return "video/webm"
	case ".mp3":
		return "audio/mpeg"
	case ".m4a":
		return "audio/mp4"
	case ".wav":
		return "audio/wav"
	case ".flac":
		return "audio/flac"
	case ".svg":
		return "image/svg+xml"
	case ".webp":
		return "image/webp"
	case ".png":
		return "image/png"
	case ".jpg", ".jpeg":
		return "image/jpeg"
	case ".gif":
		return "image/gif"
	case ".pdf":
		return "application/pdf"
	}
	if ct := mime.TypeByExtension(ext); ct != "" {
		return ct
	}
	return "application/octet-stream"
}
