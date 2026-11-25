package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type cfgFile struct {
	ServerURL string `json:"server_url"`
	AppToken  string `json:"app_token"`
}

func mustLoadCfg() cfgFile {
	path := strings.TrimSpace(os.Getenv("AI_AUDIT_CONFIG"))
	if path == "" {
		home, _ := os.UserHomeDir()
		path = filepath.Join(home, ".config/ai-audit/config.json")
	}
	b, err := os.ReadFile(path)
	if err != nil {
		panic(err)
	}
	var c cfgFile
	if err := json.Unmarshal(b, &c); err != nil {
		panic(err)
	}
	c.ServerURL = strings.TrimRight(c.ServerURL, "/")
	return c
}

func doReq(cli *http.Client, cfg cfgFile, method, path string, body any, extraHdr map[string]string) (int, http.Header, []byte) {
	var r io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		r = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, cfg.ServerURL+path, r)
	if err != nil {
		panic(err)
	}
	req.Header.Set("Authorization", "Bearer "+cfg.AppToken)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, v := range extraHdr {
		req.Header.Set(k, v)
	}
	resp, err := cli.Do(req)
	if err != nil {
		panic(err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, resp.Header, data
}

func must(cond bool, msg string, args ...any) {
	if !cond {
		fmt.Fprintf(os.Stderr, "FAIL: "+msg+"\n", args...)
		os.Exit(1)
	}
}

func main() {
	cfg := mustLoadCfg()
	cli := &http.Client{
		Timeout:   25 * time.Second,
		Transport: &http.Transport{Proxy: nil},
	}

	// Wait up to 8s for Mac daemon to finish reconnecting after install.sh restart
	var online bool
	for i := 0; i < 8; i++ {
		code, _, body := doReq(cli, cfg, "GET", "/api/v1/status", nil, nil)
		if code == 200 {
			var st struct {
				MacOnline bool `json:"mac_online"`
				Settings  struct {
					FilesEnabled bool `json:"files_enabled"`
				} `json:"settings"`
			}
			_ = json.Unmarshal(body, &st)
			if st.MacOnline && st.Settings.FilesEnabled {
				online = true
				break
			}
		}
		time.Sleep(time.Second)
	}
	must(online, "Mac daemon did not come online with files_enabled=true")
	fmt.Println("[PASS] 1. Relay + Mac tunnel online, files_enabled=true")

	// 2. List configured default root + sandbox block on ../../etc/passwd
	code, _, body := doReq(cli, cfg, "GET", "/api/v1/fs/list", nil, nil)
	must(code == 200, "fs/list default root failed: %d %s", code, string(body))
	var listRes struct {
		Path    string `json:"path"`
		Entries []any  `json:"entries"`
	}
	_ = json.Unmarshal(body, &listRes)
	must(listRes.Path != "" && len(listRes.Entries) > 0, "unexpected default root list: %s", listRes.Path)

	code, _, _ = doReq(cli, cfg, "GET", "/api/v1/fs/list?path="+url.QueryEscape("../../etc/passwd"), nil, nil)
	must(code == 403, "expected 403 on path traversal, got %d", code)
	fmt.Printf("[PASS] 2. fs/list default root=%s (%d entries) & traversal blocked (403)\n", listRes.Path, len(listRes.Entries))

	// Determine target project dir (use cwd if inside configured root, else root itself)
	cwd, _ := os.Getwd()
	projDir := listRes.Path
	if cwd != "" && strings.HasPrefix(cwd, listRes.Path) {
		projDir = cwd
	}

	// 3. Content search via ripgrep (rg)
	code, _, body = doReq(cli, cfg, "GET", "/api/v1/fs/search?path="+url.QueryEscape(projDir)+"&q=pdftoppm&mode=content", nil, nil)
	must(code == 200, "fs/search rg failed: %d %s", code, string(body))
	var searchRes struct {
		Items []struct {
			RelPath string `json:"rel_path"`
			Line    int    `json:"line"`
		} `json:"items"`
	}
	_ = json.Unmarshal(body, &searchRes)
	must(len(searchRes.Items) > 0, "expected rg matches for pdftoppm in %s", projDir)
	fmt.Printf("[PASS] 3. fs/search (rg) found %d matches (first: %s:%d)\n", len(searchRes.Items), searchRes.Items[0].RelPath, searchRes.Items[0].Line)

	// 4. Touch -> Read -> Write (200) -> Stale SHA Write (409 Conflict) -> Range 206 -> Cleanup
	tmpFile := filepath.Join(projDir, ".v2_e2e_verify_tmp.txt")
	_ = os.Remove(tmpFile)
	defer os.Remove(tmpFile)

	code, _, body = doReq(cli, cfg, "POST", "/api/v1/fs/touch", map[string]any{"path": tmpFile}, nil)
	must(code == 200, "fs/touch failed: %d %s", code, string(body))

	code, _, body = doReq(cli, cfg, "GET", "/api/v1/fs/read?path="+url.QueryEscape(tmpFile), nil, nil)
	must(code == 200, "fs/read failed: %d %s", code, string(body))
	var rRes struct {
		SHA256 string `json:"sha256"`
	}
	_ = json.Unmarshal(body, &rRes)
	must(rRes.SHA256 != "", "empty sha256")

	content := "Phone AI Audit v2.0.0 live verification payload — zero download streaming!\n"
	code, _, body = doReq(cli, cfg, "PUT", "/api/v1/fs/write", map[string]any{
		"path":            tmpFile,
		"content":         content,
		"expected_sha256": rRes.SHA256,
	}, nil)
	must(code == 200, "fs/write failed: %d %s", code, string(body))

	// Stale sha must return 409 Conflict
	code, _, body = doReq(cli, cfg, "PUT", "/api/v1/fs/write", map[string]any{
		"path":            tmpFile,
		"content":         "should fail",
		"expected_sha256": rRes.SHA256,
	}, nil)
	must(code == 409, "expected 409 conflict on stale sha, got %d %s", code, string(body))

	// HTTP Range 206 Partial Content stream test
	code, hdr, raw := doReq(cli, cfg, "GET", "/api/v1/fs/stream?path="+url.QueryEscape(tmpFile)+"&kind=raw", nil, map[string]string{
		"Range": "bytes=0-13",
	})
	must(code == 206 && string(raw) == "Phone AI Audit", "expected 206 'Phone AI Audit', got %d %q (range=%s)", code, string(raw), hdr.Get("Content-Range"))
	fmt.Printf("[PASS] 4. fs/touch + fs/read + fs/write + 409 lock + HTTP 206 Range (%s)\n", hdr.Get("Content-Range"))

	// 5. PDF meta + pdftoppm single-page JPEG stream
	tmpPDF := filepath.Join(projDir, ".v2_e2e_verify_tmp.pdf")
	_ = os.WriteFile(tmpPDF, minimalPDF(), 0o644)
	defer os.Remove(tmpPDF)

	code, _, body = doReq(cli, cfg, "GET", "/api/v1/fs/pdf/meta?path="+url.QueryEscape(tmpPDF), nil, nil)
	must(code == 200, "fs/pdf/meta failed: %d %s", code, string(body))
	var pdfMeta struct {
		Pages int `json:"pages"`
	}
	_ = json.Unmarshal(body, &pdfMeta)
	must(pdfMeta.Pages == 1, "expected 1 page PDF, got %d", pdfMeta.Pages)

	code, hdr, jpg := doReq(cli, cfg, "GET", "/api/v1/fs/stream?path="+url.QueryEscape(tmpPDF)+"&kind=pdf_page&page=1&dpi=120", nil, nil)
	must(code == 200 && strings.Contains(hdr.Get("Content-Type"), "image/jpeg") && len(jpg) > 500 && jpg[0] == 0xFF && jpg[1] == 0xD8,
		"expected JPEG stream from pdftoppm, got code=%d ct=%s len=%d", code, hdr.Get("Content-Type"), len(jpg))
	fmt.Printf("[PASS] 5. PDF pdfinfo (pages=%d) + pdftoppm JPEG stream (%d bytes)\n", pdfMeta.Pages, len(jpg))

	// 6. Git status / log / diff (use projDir if git repo, else first git repo under default root)
	code, _, body = doReq(cli, cfg, "GET", "/api/v1/git/status?path="+url.QueryEscape(projDir), nil, nil)
	if code == 200 {
		var gs struct {
			Repo struct {
				Branch     string `json:"branch"`
				DirtyCount int    `json:"dirty_count"`
			} `json:"repo"`
		}
		_ = json.Unmarshal(body, &gs)
		codeLog, _, _ := doReq(cli, cfg, "GET", "/api/v1/git/log?path="+url.QueryEscape(projDir)+"&limit=5", nil, nil)
		must(codeLog == 200, "git/log failed: %d", codeLog)
		fmt.Printf("[PASS] 6. Git status & log on %s (branch=%s, dirty=%d)\n", projDir, gs.Repo.Branch, gs.Repo.DirtyCount)
	} else {
		fmt.Printf("[INFO] %s is not a git repo (%s), searching first git repo under %s...\n", projDir, string(body), listRes.Path)
		foundRepo := ""
		_ = filepath.WalkDir(listRes.Path, func(p string, d os.DirEntry, err error) error {
			if err != nil || foundRepo != "" {
				return nil
			}
			if d.IsDir() && d.Name() == ".git" {
				foundRepo = filepath.Dir(p)
				return io.EOF
			}
			return nil
		})
		must(foundRepo != "", "no git repo found under %s", listRes.Path)
		code, _, body = doReq(cli, cfg, "GET", "/api/v1/git/status?path="+url.QueryEscape(foundRepo), nil, nil)
		must(code == 200, "git/status failed on %s: %d %s", foundRepo, code, string(body))
		var gs struct {
			Repo struct {
				RepoName   string `json:"repo_name"`
				Branch     string `json:"branch"`
				DirtyCount int    `json:"dirty_count"`
			} `json:"repo"`
		}
		_ = json.Unmarshal(body, &gs)
		codeLog, _, _ := doReq(cli, cfg, "GET", "/api/v1/git/log?path="+url.QueryEscape(foundRepo)+"&limit=5", nil, nil)
		must(codeLog == 200, "git/log failed: %d", codeLog)
		fmt.Printf("[PASS] 6. Git status & log on %s (branch=%s, dirty=%d)\n", gs.Repo.RepoName, gs.Repo.Branch, gs.Repo.DirtyCount)
	}

	fmt.Println("ALL v2.0.0 LIVE E2E CHECKS PASSED!")
}

func minimalPDF() []byte {
	return []byte(`%PDF-1.4
1 0 obj << /Type /Catalog /Pages 2 0 R >> endobj
2 0 obj << /Type /Pages /Kids [3 0 R] /Count 1 >> endobj
3 0 obj << /Type /Page /Parent 2 0 R /MediaBox [0 0 200 100] /Contents 4 0 R >> endobj
4 0 obj << /Length 28 >> stream
BT /F1 12 Tf 20 50 Td (OK) Tj ET
endstream endobj
xref
0 5
0000000000 65535 f 
0000000009 00000 n 
0000000058 00000 n 
0000000115 00000 n 
0000000198 00000 n 
trailer << /Size 5 /Root 1 0 R >>
startxref
278
%%EOF
`)
}
