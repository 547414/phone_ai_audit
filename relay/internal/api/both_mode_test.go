package api_test

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"phone-ai-audit/relay/internal/model"
)

func TestBothModeAndMacLocalDecision(t *testing.T) {
	srv, cfg, cleanup := newTestServer(t)
	defer cleanup()

	// 1. Update approval_mode to "both" and verify persistence
	resp := doJSON(t, "PUT", srv.URL+"/api/v1/settings", cfg.AppToken, map[string]any{
		"approval_mode": model.ModeBoth,
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 setting mode=both, got %d", resp.StatusCode)
	}
	var updated model.Settings
	_ = json.NewDecoder(resp.Body).Decode(&updated)
	_ = resp.Body.Close()
	if updated.ApprovalMode != model.ModeBoth {
		t.Fatalf("expected approval_mode=both, got %q", updated.ApprovalMode)
	}

	// 2. Connect both Android App WS and Mac WS
	appConn, _, err := websocket.DefaultDialer.Dial(
		wsURL(srv.URL, "/api/v1/ws/app"),
		http.Header{"Authorization": []string{"Bearer " + cfg.AppToken}},
	)
	if err != nil {
		t.Fatalf("dial app ws: %v", err)
	}
	defer appConn.Close()
	_, _, _ = appConn.ReadMessage() // init frame

	macConn, _, err := websocket.DefaultDialer.Dial(
		wsURL(srv.URL, "/api/v1/ws/mac"),
		http.Header{"Authorization": []string{"Bearer " + cfg.MacToken}},
	)
	if err != nil {
		t.Fatalf("dial mac ws: %v", err)
	}
	defer macConn.Close()

	// 3. Mac submits approval.create -> ack should be pending
	if err := macConn.WriteJSON(map[string]any{
		"type":        "approval.create",
		"req_id":      "req-both-1",
		"timeout_sec": 60,
		"approval": map[string]any{
			"agent":   "claude",
			"summary": "[Bash] echo dual-mode",
		},
	}); err != nil {
		t.Fatalf("write approval.create: %v", err)
	}

	var approvalID string
	deadline := time.Now().Add(4 * time.Second)
	for time.Now().Before(deadline) && approvalID == "" {
		_ = macConn.SetReadDeadline(time.Now().Add(2 * time.Second))
		_, raw, err := macConn.ReadMessage()
		if err != nil {
			t.Fatalf("read mac ws: %v", err)
		}
		var env struct {
			Type     string         `json:"type"`
			Approval model.Approval `json:"approval"`
		}
		if json.Unmarshal(raw, &env) == nil && env.Type == "approval.ack" {
			if env.Approval.Status != model.StatusPending {
				t.Fatalf("expected pending approval in both mode, got %s", env.Approval.Status)
			}
			approvalID = env.Approval.ID
		}
	}
	if approvalID == "" {
		t.Fatal("did not receive approval.ack")
	}

	// 4. Mac decides locally via approval.decide -> Android app must receive approval.updated with decided_by=mac_local
	if err := macConn.WriteJSON(map[string]any{
		"type":        "approval.decide",
		"approval_id": approvalID,
		"decision":    model.StatusApproved,
		"reason":      "",
	}); err != nil {
		t.Fatalf("write approval.decide: %v", err)
	}

	gotUpdated := false
	deadline = time.Now().Add(4 * time.Second)
	for time.Now().Before(deadline) && !gotUpdated {
		_ = appConn.SetReadDeadline(time.Now().Add(2 * time.Second))
		_, raw, err := appConn.ReadMessage()
		if err != nil {
			t.Fatalf("read app ws: %v", err)
		}
		var env struct {
			Type     string         `json:"type"`
			Approval model.Approval `json:"approval"`
		}
		if json.Unmarshal(raw, &env) == nil && env.Type == "approval.updated" && env.Approval.ID == approvalID {
			if env.Approval.Status != model.StatusApproved || env.Approval.DecidedBy != "mac_local" {
				t.Fatalf("unexpected approval.updated payload: %+v", env.Approval)
			}
			gotUpdated = true
		}
	}
	if !gotUpdated {
		t.Fatal("android app did not receive approval.updated after Mac local approval")
	}

	// Verify phone cannot overwrite an already-decided approval (idempotent conflict check)
	resp = doJSON(t, "POST", srv.URL+"/api/v1/approvals/"+approvalID+"/decide", cfg.AppToken, map[string]any{
		"decision": model.StatusDenied,
	})
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("expected 409 conflict when phone decides after Mac, got %d", resp.StatusCode)
	}

	// Verify audit message includes Mac source tag
	resp = doJSON(t, "GET", srv.URL+"/api/v1/messages?limit=10", cfg.AppToken, nil)
	defer resp.Body.Close()
	var msgs struct {
		Items []model.Message `json:"items"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&msgs)
	foundAudit := false
	for _, m := range msgs.Items {
		if m.ApprovalID == approvalID && strings.Contains(m.Title, "电脑端") {
			foundAudit = true
			break
		}
	}
	if !foundAudit {
		t.Fatalf("expected approval_result message tagged with 电脑端, got %+v", msgs.Items)
	}
}

func TestTimeoutAutoApproveSyncAndMarkRead(t *testing.T) {
	srv, cfg, cleanup := newTestServer(t)
	defer cleanup()

	// 1. Default settings must be 30s + timeout_action=approve
	resp := doJSON(t, "GET", srv.URL+"/api/v1/settings", cfg.AppToken, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 GET settings, got %d", resp.StatusCode)
	}
	var def model.Settings
	_ = json.NewDecoder(resp.Body).Decode(&def)
	_ = resp.Body.Close()
	if def.DefaultTimeoutSec != 30 || def.TimeoutAction != model.TimeoutApprove {
		t.Fatalf("expected default 30s + approve, got %+v", def)
	}

	// 2. Connect App WS and Mac WS
	appConn, _, err := websocket.DefaultDialer.Dial(
		wsURL(srv.URL, "/api/v1/ws/app"),
		http.Header{"Authorization": []string{"Bearer " + cfg.AppToken}},
	)
	if err != nil {
		t.Fatalf("dial app ws: %v", err)
	}
	defer appConn.Close()
	_, _, _ = appConn.ReadMessage() // init

	macConn, _, err := websocket.DefaultDialer.Dial(
		wsURL(srv.URL, "/api/v1/ws/mac"),
		http.Header{"Authorization": []string{"Bearer " + cfg.MacToken}},
	)
	if err != nil {
		t.Fatalf("dial mac ws: %v", err)
	}
	defer macConn.Close()
	_, _, _ = macConn.ReadMessage() // initial settings.updated

	// 3. Updating custom timeout broadcasts settings.updated to Mac WS
	resp = doJSON(t, "PUT", srv.URL+"/api/v1/settings", cfg.AppToken, map[string]any{
		"default_timeout_sec": 45,
		"timeout_action":      model.TimeoutApprove,
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 PUT settings, got %d", resp.StatusCode)
	}
	_ = resp.Body.Close()

	_ = macConn.SetReadDeadline(time.Now().Add(2 * time.Second))
	_, macSetRaw, err := macConn.ReadMessage()
	if err != nil {
		t.Fatalf("read mac settings.updated: %v", err)
	}
	var setFrame struct {
		Type     string         `json:"type"`
		Settings model.Settings `json:"settings"`
	}
	if err := json.Unmarshal(macSetRaw, &setFrame); err != nil || setFrame.Type != "settings.updated" || setFrame.Settings.DefaultTimeoutSec != 45 || setFrame.Settings.TimeoutAction != model.TimeoutApprove {
		t.Fatalf("unexpected mac settings frame: %s", string(macSetRaw))
	}

	// 4. Create approval with short 1s timeout -> auto-approves on expiry, pushes to both Mac and App, and marks read
	if err := macConn.WriteJSON(map[string]any{
		"type":        "approval.create",
		"req_id":      "req-timeout-1",
		"timeout_sec": 1,
		"approval": map[string]any{
			"agent":   "agy",
			"summary": "[run_command] git commit -m test",
		},
	}); err != nil {
		t.Fatalf("write approval.create: %v", err)
	}

	var approvalID string
	gotMacDecision := false
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) && !gotMacDecision {
		_ = macConn.SetReadDeadline(time.Now().Add(3 * time.Second))
		_, raw, err := macConn.ReadMessage()
		if err != nil {
			t.Fatalf("read mac ws: %v", err)
		}
		var env struct {
			Type     string         `json:"type"`
			Approval model.Approval `json:"approval"`
		}
		if json.Unmarshal(raw, &env) == nil {
			switch env.Type {
			case "approval.ack":
				approvalID = env.Approval.ID
			case "approval.decision":
				if env.Approval.ID == approvalID {
					if env.Approval.Status != model.StatusApproved || env.Approval.DecidedBy != "system_timeout" {
						t.Fatalf("expected timeout auto-approve decision on Mac, got %+v", env.Approval)
					}
					gotMacDecision = true
				}
			}
		}
	}
	if !gotMacDecision {
		t.Fatal("mac did not receive approval.decision on timeout auto-approve")
	}

	// Verify App received approval.updated (clearing notification & pending card) and read message
	gotAppUpdated := false
	gotReadAudit := false
	deadline = time.Now().Add(4 * time.Second)
	for time.Now().Before(deadline) && (!gotAppUpdated || !gotReadAudit) {
		_ = appConn.SetReadDeadline(time.Now().Add(2 * time.Second))
		_, raw, err := appConn.ReadMessage()
		if err != nil {
			t.Fatalf("read app ws: %v", err)
		}
		var env struct {
			Type     string         `json:"type"`
			Approval model.Approval `json:"approval"`
			Message  model.Message  `json:"message"`
		}
		if json.Unmarshal(raw, &env) == nil {
			if env.Type == "approval.updated" && env.Approval.ID == approvalID {
				if env.Approval.Status != model.StatusApproved {
					t.Fatalf("expected approved on app ws, got %+v", env.Approval)
				}
				gotAppUpdated = true
			}
			if env.Type == "message.created" && env.Message.ApprovalID == approvalID {
				if env.Message.ReadAt == 0 {
					t.Fatalf("expected approval_result message to be marked read, got %+v", env.Message)
				}
				if !strings.Contains(env.Message.Title, "超时自动允许") {
					t.Fatalf("expected 超时自动允许 title, got %q", env.Message.Title)
				}
				gotReadAudit = true
			}
		}
	}
	if !gotAppUpdated || !gotReadAudit {
		t.Fatalf("app ws missing expected events: gotAppUpdated=%v gotReadAudit=%v", gotAppUpdated, gotReadAudit)
	}

	// Verify status reports 0 pending and 0 unread
	resp = doJSON(t, "GET", srv.URL+"/api/v1/status", cfg.AppToken, nil)
	var st model.StatusSnapshot
	_ = json.NewDecoder(resp.Body).Decode(&st)
	_ = resp.Body.Close()
	if st.PendingCount != 0 || st.UnreadCount != 0 {
		t.Fatalf("expected pending=0 unread=0 after timeout auto-approve, got pending=%d unread=%d", st.PendingCount, st.UnreadCount)
	}

	// 5. Toggle auto-approve OFF (timeout_action=fallback) -> timeout falls back instead of approving
	resp = doJSON(t, "PUT", srv.URL+"/api/v1/settings", cfg.AppToken, map[string]any{
		"timeout_action": model.TimeoutFallback,
	})
	_ = resp.Body.Close()

	if err := macConn.WriteJSON(map[string]any{
		"type":        "approval.create",
		"req_id":      "req-timeout-2",
		"timeout_sec": 1,
		"approval": map[string]any{
			"agent":   "claude",
			"summary": "[Bash] echo fallback-test",
		},
	}); err != nil {
		t.Fatalf("write approval.create 2: %v", err)
	}
	var id2 string
	gotFallback := false
	deadline = time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) && !gotFallback {
		_ = macConn.SetReadDeadline(time.Now().Add(3 * time.Second))
		_, raw, err := macConn.ReadMessage()
		if err != nil {
			t.Fatalf("read mac ws 2: %v", err)
		}
		var env struct {
			Type     string         `json:"type"`
			Approval model.Approval `json:"approval"`
		}
		if json.Unmarshal(raw, &env) == nil {
			if env.Type == "approval.ack" {
				id2 = env.Approval.ID
			}
			if env.Type == "approval.decision" && env.Approval.ID == id2 {
				if env.Approval.Status != model.StatusFallback {
					t.Fatalf("expected fallback when timeout_action=fallback, got %+v", env.Approval)
				}
				gotFallback = true
			}
		}
	}
	if !gotFallback {
		t.Fatal("mac did not receive fallback decision when auto-approve disabled")
	}
}

func TestBothModeStaysPendingWhenPhoneTemporarilyOffline(t *testing.T) {
	srv, cfg, cleanup := newTestServer(t)
	defer cleanup()

	// Switch to both mode while NO phone client is connected
	resp := doJSON(t, "PUT", srv.URL+"/api/v1/settings", cfg.AppToken, map[string]any{
		"approval_mode": model.ModeBoth,
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 setting mode=both, got %d", resp.StatusCode)
	}
	_ = resp.Body.Close()

	// Create approval with app offline -> MUST stay pending in both mode so Mac HUD & reconnecting phone can handle it
	resp = doJSON(t, "POST", srv.URL+"/api/v1/approvals", cfg.MacToken, map[string]any{
		"agent":       "agy",
		"summary":     "[run_command] npm run build",
		"timeout_sec": 30,
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("expected 201, got %d", resp.StatusCode)
	}
	var created model.Approval
	_ = json.NewDecoder(resp.Body).Decode(&created)
	_ = resp.Body.Close()
	if created.Status != model.StatusPending {
		t.Fatalf("expected pending in both mode even when app temporarily offline, got %s", created.Status)
	}

	// Phone reconnects shortly after -> init frame MUST include the pending approval
	appConn, _, err := websocket.DefaultDialer.Dial(
		wsURL(srv.URL, "/api/v1/ws/app"),
		http.Header{"Authorization": []string{"Bearer " + cfg.AppToken}},
	)
	if err != nil {
		t.Fatalf("dial app ws: %v", err)
	}
	defer appConn.Close()
	_ = appConn.SetReadDeadline(time.Now().Add(2 * time.Second))
	_, initRaw, err := appConn.ReadMessage()
	if err != nil {
		t.Fatalf("read init frame: %v", err)
	}
	if !strings.Contains(string(initRaw), created.ID) {
		t.Fatalf("expected reconnecting app init frame to include pending approval %s, got %s", created.ID, string(initRaw))
	}
}
