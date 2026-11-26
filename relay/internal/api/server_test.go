package api_test

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"phone-ai-audit/relay/internal/api"
	"phone-ai-audit/relay/internal/config"
	"phone-ai-audit/relay/internal/hub"
	"phone-ai-audit/relay/internal/model"
	"phone-ai-audit/relay/internal/store"
)

func newTestServer(t *testing.T) (*httptest.Server, config.Config, func()) {
	t.Helper()
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	h := hub.New(st)
	ctx, cancel := context.WithCancel(context.Background())
	h.StartBackground(ctx)

	cfg := config.Config{
		ListenAddr: "127.0.0.1:0",
		DBPath:     ":memory:",
		MacToken:   "mac-secret-token",
		AppToken:   "app-secret-token",
	}
	srv := httptest.NewServer(api.New(cfg, st, h))
	return srv, cfg, func() {
		cancel()
		srv.Close()
		_ = st.Close()
	}
}

func doJSON(t *testing.T, method, url, token string, body any) *http.Response {
	t.Helper()
	var buf bytes.Buffer
	if body != nil {
		if err := json.NewEncoder(&buf).Encode(body); err != nil {
			t.Fatalf("encode json: %v", err)
		}
	}
	req, err := http.NewRequest(method, url, &buf)
	if err != nil {
		t.Fatalf("new req: %v", err)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("http %s %s: %v", method, url, err)
	}
	return resp
}

func wsURL(httpURL, path string) string {
	return "ws" + strings.TrimPrefix(httpURL, "http") + path
}

func TestApprovalOfflineFallbackAndOnlineDecide(t *testing.T) {
	srv, cfg, cleanup := newTestServer(t)
	defer cleanup()

	// 1. Auth separation
	resp := doJSON(t, "POST", srv.URL+"/api/v1/approvals", cfg.AppToken, map[string]any{"agent": "claude", "summary": "ls"})
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected 401 for app token on mac route, got %d", resp.StatusCode)
	}
	_ = resp.Body.Close()

	// 2. When phone is offline, CreateApproval immediately falls back
	resp = doJSON(t, "POST", srv.URL+"/api/v1/approvals", cfg.MacToken, map[string]any{
		"agent":   "claude",
		"summary": "run npm test",
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("expected 201, got %d", resp.StatusCode)
	}
	var offlineApproval model.Approval
	_ = json.NewDecoder(resp.Body).Decode(&offlineApproval)
	_ = resp.Body.Close()
	if offlineApproval.Status != model.StatusFallback {
		t.Fatalf("expected fallback when app offline, got %s", offlineApproval.Status)
	}

	// 3. Connect Android App WS
	appHdr := http.Header{"Authorization": []string{"Bearer " + cfg.AppToken}}
	appConn, _, err := websocket.DefaultDialer.Dial(wsURL(srv.URL, "/api/v1/ws/app"), appHdr)
	if err != nil {
		t.Fatalf("dial app ws: %v", err)
	}
	defer appConn.Close()

	// Read init frame
	_, initMsg, err := appConn.ReadMessage()
	if err != nil {
		t.Fatalf("read init: %v", err)
	}
	if !strings.Contains(string(initMsg), `"type":"init"`) {
		t.Fatalf("expected init frame, got %s", string(initMsg))
	}

	// 4. Create approval while phone online -> pending
	resp = doJSON(t, "POST", srv.URL+"/api/v1/approvals", cfg.MacToken, map[string]any{
		"agent":        "codex",
		"summary":      "git push origin main",
		"risk_level":   "high",
		"tmux_session": "codex-demo",
		"timeout_sec":  30,
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("expected 201, got %d", resp.StatusCode)
	}
	var onlineApproval model.Approval
	_ = json.NewDecoder(resp.Body).Decode(&onlineApproval)
	_ = resp.Body.Close()
	if onlineApproval.Status != model.StatusPending {
		t.Fatalf("expected pending, got %s", onlineApproval.Status)
	}

	// Verify app receives approval.created
	_ = appConn.SetReadDeadline(time.Now().Add(2 * time.Second))
	_, pushMsg, err := appConn.ReadMessage()
	if err != nil {
		t.Fatalf("read approval.created: %v", err)
	}
	if !strings.Contains(string(pushMsg), onlineApproval.ID) {
		t.Fatalf("expected approval id in ws push, got %s", string(pushMsg))
	}

	// 5. Start long-poll wait in background, then decide from phone
	waitDone := make(chan model.Approval, 1)
	go func() {
		r := doJSON(t, "GET", srv.URL+"/api/v1/approvals/"+onlineApproval.ID+"/wait?timeout=10", cfg.MacToken, nil)
		defer r.Body.Close()
		var a model.Approval
		_ = json.NewDecoder(r.Body).Decode(&a)
		waitDone <- a
	}()

	time.Sleep(50 * time.Millisecond)
	resp = doJSON(t, "POST", srv.URL+"/api/v1/approvals/"+onlineApproval.ID+"/decide", cfg.AppToken, map[string]any{
		"decision": "denied",
		"reason":   "请先跑单测",
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 decide, got %d", resp.StatusCode)
	}
	_ = resp.Body.Close()

	// Duplicate decide must be rejected with 409
	resp = doJSON(t, "POST", srv.URL+"/api/v1/approvals/"+onlineApproval.ID+"/decide", cfg.AppToken, map[string]any{
		"decision": "approved",
	})
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("expected 409 on duplicate decide, got %d", resp.StatusCode)
	}
	_ = resp.Body.Close()

	select {
	case resolved := <-waitDone:
		if resolved.Status != model.StatusDenied || resolved.DecisionReason != "请先跑单测" {
			t.Fatalf("unexpected resolved approval: %+v", resolved)
		}
	case <-time.After(3 * time.Second):
		t.Fatalf("waiter did not wake up")
	}
}

func TestTerminalRelayBinaryStream(t *testing.T) {
	srv, cfg, cleanup := newTestServer(t)
	defer cleanup()

	// Connect Mac WS
	macHdr := http.Header{"Authorization": []string{"Bearer " + cfg.MacToken}}
	macConn, _, err := websocket.DefaultDialer.Dial(wsURL(srv.URL, "/api/v1/ws/mac"), macHdr)
	if err != nil {
		t.Fatalf("dial mac ws: %v", err)
	}
	defer macConn.Close()
	// Drain initial settings.updated
	_, _, _ = macConn.ReadMessage()

	// Issue terminal ticket
	resp := doJSON(t, "POST", srv.URL+"/api/v1/terminals/ticket", cfg.AppToken, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 ticket, got %d", resp.StatusCode)
	}
	var tk struct {
		Ticket string `json:"ticket"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&tk)
	_ = resp.Body.Close()
	if tk.Ticket == "" {
		t.Fatalf("empty ticket")
	}

	// Dial Terminal WS with ticket
	termHdr := http.Header{
		"Authorization":     []string{"Bearer " + cfg.AppToken},
		"X-Terminal-Ticket": []string{tk.Ticket},
	}
	termConn, _, err := websocket.DefaultDialer.Dial(wsURL(srv.URL, "/api/v1/ws/terminal"), termHdr)
	if err != nil {
		t.Fatalf("dial terminal ws: %v", err)
	}
	defer termConn.Close()

	// Ticket cannot be reused
	if _, r, err := websocket.DefaultDialer.Dial(wsURL(srv.URL, "/api/v1/ws/terminal"), termHdr); err == nil {
		t.Fatalf("expected reused ticket to fail")
	} else if r != nil && r.StatusCode != http.StatusForbidden {
		t.Fatalf("expected 403 on reused ticket, got %d", r.StatusCode)
	}

	// App sends open
	_ = termConn.WriteJSON(map[string]any{"type": "open", "mode": "new_shell", "cols": 80, "rows": 24})

	// Mac receives term.open
	_, openRaw, err := macConn.ReadMessage()
	if err != nil {
		t.Fatalf("mac read term.open: %v", err)
	}
	var openMsg struct {
		Type     string `json:"type"`
		StreamID string `json:"stream_id"`
	}
	_ = json.Unmarshal(openRaw, &openMsg)
	if openMsg.Type != "term.open" || len(openMsg.StreamID) != 32 {
		t.Fatalf("unexpected term.open: %s", string(openRaw))
	}

	// App sends stdin -> Mac receives binary frame with 0x01 + 16B streamID + payload
	_ = termConn.WriteMessage(websocket.BinaryMessage, []byte("echo hi\n"))
	mt, macBin, err := macConn.ReadMessage()
	if err != nil || mt != websocket.BinaryMessage {
		t.Fatalf("mac read bin stdin: mt=%d err=%v", mt, err)
	}
	if len(macBin) < 17 || macBin[0] != hub.BinFrameStdin || string(macBin[17:]) != "echo hi\n" {
		t.Fatalf("unexpected stdin binary frame: %x", macBin)
	}

	// Mac echoes stdout back -> App receives binary payload
	sidBytes, _ := hex.DecodeString(openMsg.StreamID)
	outFrame := append([]byte{hub.BinFrameStdout}, sidBytes...)
	outFrame = append(outFrame, []byte("hi\r\n")...)
	_ = macConn.WriteMessage(websocket.BinaryMessage, outFrame)

	mt, appBin, err := termConn.ReadMessage()
	if err != nil || mt != websocket.BinaryMessage || string(appBin) != "hi\r\n" {
		t.Fatalf("unexpected app stdout: mt=%d data=%q err=%v", mt, string(appBin), err)
	}
}

func TestFSRPCAndBinaryStreaming(t *testing.T) {
	srv, cfg, cleanup := newTestServer(t)
	defer cleanup()

	macHdr := http.Header{"Authorization": []string{"Bearer " + cfg.MacToken}}
	macConn, _, err := websocket.DefaultDialer.Dial(wsURL(srv.URL, "/api/v1/ws/mac"), macHdr)
	if err != nil {
		t.Fatalf("dial mac ws: %v", err)
	}
	defer macConn.Close()
	_, _, _ = macConn.ReadMessage() // drain settings.updated

	// Mock Mac responder for 1 RPC + 1 FS stream
	go func() {
		for {
			_, raw, err := macConn.ReadMessage()
			if err != nil {
				return
			}
			var msg struct {
				Type     string `json:"type"`
				ReqID    string `json:"req_id"`
				Op       string `json:"op"`
				StreamID string `json:"stream_id"`
			}
			_ = json.Unmarshal(raw, &msg)
			switch msg.Type {
			case "rpc.req":
				_ = macConn.WriteJSON(map[string]any{
					"type":   "rpc.resp",
					"req_id": msg.ReqID,
					"status": 200,
					"data":   map[string]any{"op": msg.Op, "ok": true},
				})
			case "fs.stream_open":
				_ = macConn.WriteJSON(map[string]any{
					"type":           "fs.stream_meta",
					"stream_id":      msg.StreamID,
					"status_code":    206,
					"content_type":   "video/mp4",
					"content_length": 11,
					"content_range":  "bytes 0-10/100",
					"accept_ranges":  "bytes",
				})
				sid, _ := hex.DecodeString(msg.StreamID)
				chunk1 := append([]byte{hub.BinFrameFSChunk}, sid...)
				chunk1 = append(chunk1, []byte("hello ")...)
				_ = macConn.WriteMessage(websocket.BinaryMessage, chunk1)
				chunk2 := append([]byte{hub.BinFrameFSChunk}, sid...)
				chunk2 = append(chunk2, []byte("world")...)
				_ = macConn.WriteMessage(websocket.BinaryMessage, chunk2)
				eof := append([]byte{hub.BinFrameFSEOF}, sid...)
				_ = macConn.WriteMessage(websocket.BinaryMessage, eof)
			}
		}
	}()

	// 1. Test RPC roundtrip
	resp := doJSON(t, "GET", srv.URL+"/api/v1/fs/list?path=/tmp", cfg.AppToken, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 fs/list, got %d", resp.StatusCode)
	}
	bodyBytes, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if !strings.Contains(string(bodyBytes), `"op":"fs.list"`) {
		t.Fatalf("unexpected rpc resp: %s", string(bodyBytes))
	}

	// 2. Test zero-disk binary streaming with 206 Partial Content
	req, _ := http.NewRequest("GET", srv.URL+"/api/v1/fs/stream?path=/tmp/a.mp4&kind=raw", nil)
	req.Header.Set("Authorization", "Bearer "+cfg.AppToken)
	req.Header.Set("Range", "bytes=0-10")
	sresp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("fs/stream req: %v", err)
	}
	sbody, _ := io.ReadAll(sresp.Body)
	_ = sresp.Body.Close()
	if sresp.StatusCode != http.StatusPartialContent || string(sbody) != "hello world" || sresp.Header.Get("Content-Range") != "bytes 0-10/100" {
		t.Fatalf("unexpected stream resp: code=%d range=%q body=%q", sresp.StatusCode, sresp.Header.Get("Content-Range"), string(sbody))
	}
}

func TestAgyProfilesRPCAndNewAgyTerminalOpen(t *testing.T) {
	srv, cfg, cleanup := newTestServer(t)
	defer cleanup()

	// Disable FilesEnabled while keeping TerminalEnabled=true: agy-profiles must still succeed!
	resp := doJSON(t, "PUT", srv.URL+"/api/v1/settings", cfg.AppToken, map[string]any{
		"files_enabled":    false,
		"terminal_enabled": true,
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 updating settings, got %d", resp.StatusCode)
	}
	_ = resp.Body.Close()

	macHdr := http.Header{"Authorization": []string{"Bearer " + cfg.MacToken}}
	macConn, _, err := websocket.DefaultDialer.Dial(wsURL(srv.URL, "/api/v1/ws/mac"), macHdr)
	if err != nil {
		t.Fatalf("dial mac ws: %v", err)
	}
	defer macConn.Close()
	_, _, _ = macConn.ReadMessage() // drain initial settings.updated

	openReceived := make(chan map[string]any, 1)
	go func() {
		for {
			_, raw, err := macConn.ReadMessage()
			if err != nil {
				return
			}
			var msg map[string]any
			if json.Unmarshal(raw, &msg) != nil {
				continue
			}
			switch msg["type"] {
			case "rpc.req":
				if msg["op"] == "agy.profiles" {
					_ = macConn.WriteJSON(map[string]any{
						"type":   "rpc.resp",
						"req_id": msg["req_id"],
						"status": 200,
						"data": map[string]any{
							"agy_installed": true,
							"profiles": []map[string]any{
								{"name": "268zhb", "email": "268zhb@gmail.com", "active": true},
								{"name": "crysug55", "email": "crysug55@gmail.com", "active": false},
							},
						},
					})
				}
			case "term.open":
				openReceived <- msg
				_ = macConn.WriteJSON(map[string]any{
					"type":      "term.opened",
					"stream_id": msg["stream_id"],
					"target":    "agy-crysug55-demo",
					"cols":      135,
					"rows":      35,
				})
			}
		}
	}()

	// 1. Fetch agy profiles via app endpoint
	resp = doJSON(t, "GET", srv.URL+"/api/v1/terminals/agy-profiles", cfg.AppToken, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 agy-profiles even when files_enabled=false, got %d", resp.StatusCode)
	}
	bodyBytes, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if !strings.Contains(string(bodyBytes), `"crysug55"`) {
		t.Fatalf("unexpected agy-profiles resp: %s", string(bodyBytes))
	}

	// 2. Open new_agy terminal with specific agy_account & cwd -> Mac receives both, App gets resolved target
	tkResp := doJSON(t, "POST", srv.URL+"/api/v1/terminals/ticket", cfg.AppToken, nil)
	var tk struct {
		Ticket string `json:"ticket"`
	}
	_ = json.NewDecoder(tkResp.Body).Decode(&tk)
	_ = tkResp.Body.Close()

	termHdr := http.Header{
		"Authorization":     []string{"Bearer " + cfg.AppToken},
		"X-Terminal-Ticket": []string{tk.Ticket},
	}
	termConn, _, err := websocket.DefaultDialer.Dial(wsURL(srv.URL, "/api/v1/ws/terminal"), termHdr)
	if err != nil {
		t.Fatalf("dial terminal ws: %v", err)
	}
	defer termConn.Close()

	_ = termConn.WriteJSON(map[string]any{
		"type":        "open",
		"mode":        "new_agy",
		"agy_account": "crysug55",
		"cwd":         "/Users/crysu/demo",
		"cols":        135,
		"rows":        35,
	})

	select {
	case macOpen := <-openReceived:
		if macOpen["mode"] != "new_agy" || macOpen["agy_account"] != "crysug55" || macOpen["cwd"] != "/Users/crysu/demo" {
			t.Fatalf("unexpected term.open forwarded to mac: %+v", macOpen)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for term.open on mac")
	}

	_ = termConn.SetReadDeadline(time.Now().Add(3 * time.Second))
	_, openedRaw, err := termConn.ReadMessage()
	if err != nil {
		t.Fatalf("read opened from relay: %v", err)
	}
	var opened struct {
		Type   string `json:"type"`
		Target string `json:"target"`
		Cols   int    `json:"cols"`
		Rows   int    `json:"rows"`
	}
	if err := json.Unmarshal(openedRaw, &opened); err != nil || opened.Type != "opened" || opened.Target != "agy-crysug55-demo" || opened.Cols != 135 {
		t.Fatalf("unexpected opened frame: %s", string(openedRaw))
	}
}
