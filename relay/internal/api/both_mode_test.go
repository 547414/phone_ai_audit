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
