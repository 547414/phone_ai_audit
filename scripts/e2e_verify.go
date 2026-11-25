package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/gorilla/websocket"
)

type macConfig struct {
	ServerURL string `json:"server_url"`
	MacToken  string `json:"mac_token"`
	AppToken  string `json:"app_token"`
}

func loadConfigPath() string {
	if p := strings.TrimSpace(os.Getenv("AI_AUDIT_CONFIG")); p != "" {
		return p
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config", "ai-audit", "config.json")
}

func main() {
	home, _ := os.UserHomeDir()
	cwd, err := os.Getwd()
	must(err)

	raw, err := os.ReadFile(loadConfigPath())
	must(err)
	var cfg macConfig
	must(json.Unmarshal(raw, &cfg))
	cfg.ServerURL = strings.TrimRight(cfg.ServerURL, "/")

	httpCli := &http.Client{Timeout: 15 * time.Second, Transport: &http.Transport{Proxy: nil}}
	wsDialer := &websocket.Dialer{Proxy: nil, HandshakeTimeout: 12 * time.Second}
	wsBase := "wss://" + strings.TrimPrefix(cfg.ServerURL, "https://")
	if strings.HasPrefix(cfg.ServerURL, "http://") {
		wsBase = "ws://" + strings.TrimPrefix(cfg.ServerURL, "http://")
	}

	// 1. Verify status & mac_online
	statusBytes := apiJSON(httpCli, "GET", cfg.ServerURL+"/api/v1/status", cfg.AppToken, nil)
	var st struct {
		MacOnline bool `json:"mac_online"`
	}
	must(json.Unmarshal(statusBytes, &st))
	if !st.MacOnline {
		panic("mac_online is false")
	}
	fmt.Println("[1/4] Relay & Mac tunnel status OK (mac_online=true)")

	// 2. Connect simulated Android App WS & temporarily set mode=phone so hooks route to phone even while Mac is active
	appConn, _, err := wsDialer.Dial(wsBase+"/api/v1/ws/app", http.Header{"Authorization": []string{"Bearer " + cfg.AppToken}})
	must(err)
	defer appConn.Close()
	_, _, err = appConn.ReadMessage() // init frame
	must(err)

	apiJSON(httpCli, "PUT", cfg.ServerURL+"/api/v1/settings", cfg.AppToken, map[string]any{"approval_mode": "phone"})
	defer apiJSON(httpCli, "PUT", cfg.ServerURL+"/api/v1/settings", cfg.AppToken, map[string]any{"approval_mode": "auto"})
	time.Sleep(300 * time.Millisecond)

	// 2a. Real ai-audit-hook claude PermissionRequest -> phone approves -> verify hook stdout
	claudePayload, _ := json.Marshal(map[string]any{
		"session_id": "e2e-1",
		"cwd":        cwd,
		"tool_name":  "Bash",
		"tool_input": map[string]any{"command": "echo e2e_allow"},
	})
	claudeHookOut := make(chan string, 1)
	go func() {
		cmd := exec.Command(filepath.Join(home, ".local", "bin", "ai-audit-hook"), "claude", "PermissionRequest")
		cmd.Stdin = bytes.NewReader(claudePayload)
		out, _ := cmd.CombinedOutput()
		claudeHookOut <- string(out)
	}()

	approvalID := waitApprovalPush(appConn)
	apiJSON(httpCli, "POST", cfg.ServerURL+"/api/v1/approvals/"+approvalID+"/decide", cfg.AppToken, map[string]any{
		"decision": "approved",
	})
	gotClaude := <-claudeHookOut
	if !strings.Contains(gotClaude, `"behavior":"allow"`) {
		panic("unexpected claude hook output: " + gotClaude)
	}
	fmt.Println("[2/4] Claude PermissionRequest E2E approved OK ->", strings.TrimSpace(gotClaude))

	// 2b. Real ai-audit-hook agy PreToolUse (BypassSandbox) -> phone denies with reason
	agyPayload, _ := json.Marshal(map[string]any{
		"conversationId": "e2e-2",
		"cwd":            cwd,
		"workspacePaths": []string{cwd},
		"toolCall": map[string]any{
			"name": "run_command",
			"args": map[string]any{
				"CommandLine":   "echo dangerous",
				"BypassSandbox": true,
				"toolSummary":   "E2E BypassSandbox test",
			},
		},
	})
	agyHookOut := make(chan string, 1)
	go func() {
		cmd := exec.Command(filepath.Join(home, ".local", "bin", "ai-audit-hook"), "agy", "PreToolUse")
		cmd.Stdin = bytes.NewReader(agyPayload)
		out, _ := cmd.CombinedOutput()
		agyHookOut <- string(out)
	}()

	agyApprovalID := waitApprovalPush(appConn)
	apiJSON(httpCli, "POST", cfg.ServerURL+"/api/v1/approvals/"+agyApprovalID+"/decide", cfg.AppToken, map[string]any{
		"decision": "denied",
		"reason":   "E2E拒绝理由测试",
	})
	gotAgy := <-agyHookOut
	if !strings.Contains(gotAgy, `"decision":"deny"`) || !strings.Contains(gotAgy, "E2E拒绝理由测试") {
		panic("unexpected agy hook output: " + gotAgy)
	}
	fmt.Println("[3/4] AGY PreToolUse E2E denied with reason OK ->", strings.TrimSpace(gotAgy))

	// 4. Real remote PTY terminal over configured relay -> Mac tmux -> stdout back!
	tkBytes := apiJSON(httpCli, "POST", cfg.ServerURL+"/api/v1/terminals/ticket", cfg.AppToken, map[string]any{})
	var tk struct {
		Ticket string `json:"ticket"`
	}
	must(json.Unmarshal(tkBytes, &tk))

	termConn, _, err := wsDialer.Dial(wsBase+"/api/v1/ws/terminal", http.Header{
		"Authorization":     []string{"Bearer " + cfg.AppToken},
		"X-Terminal-Ticket": []string{tk.Ticket},
	})
	must(err)
	defer termConn.Close()

	must(termConn.WriteJSON(map[string]any{
		"type":   "open",
		"mode":   "new_shell",
		"target": "e2e-smoke",
		"cols":   80,
		"rows":   24,
	}))

	time.Sleep(700 * time.Millisecond)
	must(termConn.WriteMessage(websocket.BinaryMessage, []byte("echo PHONE_AUDIT_PTY_OK; exit\r")))

	deadline := time.Now().Add(8 * time.Second)
	var collected strings.Builder
	found := false
	for time.Now().Before(deadline) {
		_ = termConn.SetReadDeadline(time.Now().Add(2 * time.Second))
		mt, data, err := termConn.ReadMessage()
		if err != nil {
			break
		}
		if mt == websocket.BinaryMessage {
			collected.Write(data)
			if strings.Contains(collected.String(), "PHONE_AUDIT_PTY_OK") {
				found = true
				break
			}
		}
	}
	if tmuxBin, err := exec.LookPath("tmux"); err == nil {
		_ = exec.Command(tmuxBin, "kill-session", "-t", "e2e-smoke").Run()
	}
	if !found {
		panic("did not receive PHONE_AUDIT_PTY_OK from remote Mac PTY; got: " + collected.String())
	}
	fmt.Printf("[4/4] Remote Mac PTY terminal E2E over %s OK!\n", wsBase)
}

func waitApprovalPush(conn *websocket.Conn) string {
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		_ = conn.SetReadDeadline(time.Now().Add(3 * time.Second))
		_, raw, err := conn.ReadMessage()
		must(err)
		var env struct {
			Type     string `json:"type"`
			Approval struct {
				ID string `json:"id"`
			} `json:"approval"`
		}
		if json.Unmarshal(raw, &env) == nil && env.Type == "approval.created" && env.Approval.ID != "" {
			return env.Approval.ID
		}
	}
	panic("timed out waiting for approval.created push")
}

func apiJSON(cli *http.Client, method, url, token string, body any) []byte {
	var r io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		r = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, url, r)
	must(err)
	req.Header.Set("Authorization", "Bearer "+token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := cli.Do(req)
	must(err)
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 300 {
		panic(fmt.Sprintf("%s %s -> HTTP %d: %s", method, url, resp.StatusCode, string(b)))
	}
	return b
}

func must(err error) {
	if err != nil {
		panic(err)
	}
}
