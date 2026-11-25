package adapters

import (
	"encoding/json"
	"strings"
	"testing"

	"phone-ai-audit/mac-agent/internal/proto"
)

func TestClaudePermissionFormatting(t *testing.T) {
	raw := `{"session_id":"s1","cwd":"/tmp/demo","tool_name":"Bash","tool_input":{"command":"git push --force origin main"}}`
	ev := Parse("claude", "PermissionRequest", raw)
	if ev.Kind != ActionApproval {
		t.Fatalf("expected approval, got %s", ev.Kind)
	}
	if ev.Approval.RiskLevel != "high" {
		t.Fatalf("expected high risk for git push --force, got %s", ev.Approval.RiskLevel)
	}
	allowOut := FormatDecisionOutput("claude", proto.Approval{Status: "approved"})
	if !strings.Contains(allowOut, `"behavior":"allow"`) || !strings.Contains(allowOut, `"PermissionRequest"`) {
		t.Fatalf("unexpected allow output: %s", allowOut)
	}
	denyOut := FormatDecisionOutput("claude", proto.Approval{Status: "denied", DecisionReason: "别强推"})
	if !strings.Contains(denyOut, `"behavior":"deny"`) || !strings.Contains(denyOut, "别强推") {
		t.Fatalf("unexpected deny output: %s", denyOut)
	}
	if fb := FormatDecisionOutput("claude", proto.Approval{Status: "fallback"}); fb != "" {
		t.Fatalf("expected empty fallback output for claude, got %q", fb)
	}
}

func TestCodexPermissionNoInterrupt(t *testing.T) {
	raw := `{"session_id":"c1","cwd":"/tmp/demo","tool_name":"shell","tool_input":{"command":"rm -rf build"}}`
	ev := Parse("codex", "PermissionRequest", raw)
	if ev.Kind != ActionApproval || ev.Approval.RiskLevel != "high" {
		t.Fatalf("unexpected codex parsed event: %+v", ev)
	}
	denyOut := FormatDecisionOutput("codex", proto.Approval{Status: "denied", DecisionReason: "no"})
	if strings.Contains(denyOut, `"interrupt"`) {
		t.Fatalf("codex 0.169.0 rejects interrupt:true, must not include interrupt field: %s", denyOut)
	}
}

func TestAgyPreToolUseFilteringAndDecisions(t *testing.T) {
	// Sandboxed run_command -> no approval needed, immediate allow
	safe := `{"conversationId":"g1","cwd":"/tmp/demo","workspacePaths":["/tmp/demo"],"toolCall":{"name":"run_command","args":{"CommandLine":"ls -la","BypassSandbox":false}}}`
	evSafe := Parse("agy", "PreToolUse", safe)
	if evSafe.Kind != ActionNone || !strings.Contains(evSafe.DefaultOut, `"allow"`) {
		t.Fatalf("expected ActionNone for sandboxed command, got %+v", evSafe)
	}

	// BypassSandbox run_command -> high-risk approval
	risky := `{"conversationId":"g1","cwd":"/tmp/demo","workspacePaths":["/tmp/demo"],"toolCall":{"name":"run_command","args":{"CommandLine":"ssh prod-server","BypassSandbox":true,"toolSummary":"SSH to prod"}}}`
	evRisky := Parse("agy", "PreToolUse", risky)
	if evRisky.Kind != ActionApproval || evRisky.Approval.RiskLevel != "high" {
		t.Fatalf("expected high-risk approval for BypassSandbox, got %+v", evRisky)
	}

	var allowObj map[string]any
	_ = json.Unmarshal([]byte(FormatDecisionOutput("agy", proto.Approval{Status: "approved"})), &allowObj)
	if allowObj["decision"] != "auto_approve" {
		t.Fatalf("expected auto_approve for agy, got %v", allowObj)
	}

	var fbObj map[string]any
	_ = json.Unmarshal([]byte(FormatDecisionOutput("agy", proto.Approval{Status: "fallback"})), &fbObj)
	if fbObj["decision"] != "allow" {
		t.Fatalf("expected allow (delegate to local policy) on fallback for agy, got %v", fbObj)
	}
}
