package adapters

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"phone-ai-audit/mac-agent/internal/proto"
	"phone-ai-audit/mac-agent/internal/risk"
	"phone-ai-audit/mac-agent/internal/term"
)

type ActionKind string

const (
	ActionNone     ActionKind = "none"
	ActionMessage  ActionKind = "message"
	ActionApproval ActionKind = "approval"
)

type ParsedEvent struct {
	Kind       ActionKind
	Approval   proto.Approval
	Message    proto.Message
	DefaultOut string // stdout emitted when ActionNone or fallback
}

func ResolveTaskTitle(agent, rawPayload string) string {
	home, _ := os.UserHomeDir()
	helper := filepath.Join(home, ".local", "bin", "ai-task-title")
	if _, err := os.Stat(helper); err != nil {
		return ""
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, helper, agent, rawPayload).Output()
	if err != nil {
		return ""
	}
	return oneLine(string(out), 220)
}

func Parse(agent, event, rawPayload string) ParsedEvent {
	var data map[string]any
	if strings.TrimSpace(rawPayload) != "" {
		_ = json.Unmarshal([]byte(rawPayload), &data)
	}
	if data == nil {
		data = map[string]any{}
	}
	tmuxSess := term.CurrentTmuxSession()

	switch agent {
	case "claude":
		return parseClaude(event, rawPayload, data, tmuxSess)
	case "codex":
		return parseCodex(event, rawPayload, data, tmuxSess)
	case "agy":
		return parseAgy(event, rawPayload, data, tmuxSess)
	default:
		return ParsedEvent{Kind: ActionNone}
	}
}

func FormatDecisionOutput(agent string, a proto.Approval) string {
	switch agent {
	case "claude", "codex":
		switch a.Status {
		case "approved":
			b, _ := json.Marshal(map[string]any{
				"hookSpecificOutput": map[string]any{
					"hookEventName": "PermissionRequest",
					"decision": map[string]any{
						"behavior": "allow",
					},
				},
			})
			return string(b) + "\n"
		case "denied", "expired":
			msg := strings.TrimSpace(a.DecisionReason)
			if msg == "" {
				msg = "Rejected by user on mobile."
			}
			// Codex 0.169.0 explicitly rejects interrupt:true, so keep clean allow/deny+message for both.
			b, _ := json.Marshal(map[string]any{
				"hookSpecificOutput": map[string]any{
					"hookEventName": "PermissionRequest",
					"decision": map[string]any{
						"behavior": "deny",
						"message":  msg,
					},
				},
			})
			return string(b) + "\n"
		default:
			// Fallback / canceled: empty stdout lets CLI show native terminal prompt.
			return ""
		}
	case "agy":
		switch a.Status {
		case "approved":
			out := map[string]any{
				"decision": "auto_approve",
				"reason":   "Approved on mobile",
			}
			b, _ := json.Marshal(out)
			return string(b) + "\n"
		case "denied", "expired":
			msg := strings.TrimSpace(a.DecisionReason)
			if msg == "" {
				msg = "Rejected by user on mobile."
			}
			b, _ := json.Marshal(map[string]any{
				"decision": "deny",
				"reason":   msg,
			})
			return string(b) + "\n"
		default:
			// "allow" delegates to AGY's native permission policy/prompt.
			return "{\"decision\":\"allow\"}\n"
		}
	default:
		return ""
	}
}

func parseClaude(event, raw string, data map[string]any, tmuxSess string) ParsedEvent {
	cwd := strVal(data, "cwd")
	proj := projectName(cwd)
	title := ResolveTaskTitle("claude", raw)
	sessID := strVal(data, "session_id")

	switch event {
	case "permission", "PermissionRequest":
		toolName := strVal(data, "tool_name")
		toolInput, _ := data["tool_input"].(map[string]any)
		cmdOrPath := extractToolDetail(toolName, toolInput)
		summary := formatToolSummary(toolName, cmdOrPath)
		outside := isOutsideWorkspace(extractPathArg(toolInput), []string{cwd})
		rk := risk.Evaluate(toolName, cmdOrPath, false, outside)
		return ParsedEvent{
			Kind: ActionApproval,
			Approval: proto.Approval{
				Agent:       "claude",
				SessionID:   sessID,
				TmuxSession: tmuxSess,
				TaskTitle:   title,
				ProjectName: proj,
				Cwd:         cwd,
				ToolName:    toolName,
				Summary:     summary,
				PayloadJSON: prettyJSON(toolInput),
				RiskLevel:   rk.Level,
				RiskReasons: rk.Reasons,
			},
		}
	case "attention", "Notification":
		msg := strVal(data, "message")
		if msg == "" {
			msg = strVal(data, "title")
		}
		if msg == "" {
			msg = "Claude Code 需要你处理"
		}
		return ParsedEvent{
			Kind: ActionMessage,
			Message: proto.Message{
				Agent:       "claude",
				EventType:   "attention",
				Priority:    "high",
				Title:       "Claude Code 需要关注",
				Body:        oneLine(msg, 300),
				TaskTitle:   title,
				ProjectName: proj,
				TmuxSession: tmuxSess,
			},
		}
	default:
		return ParsedEvent{
			Kind: ActionMessage,
			Message: proto.Message{
				Agent:       "claude",
				EventType:   "done",
				Priority:    "default",
				Title:       "Claude Code 任务已完成",
				Body:        fallbackStr(title, "本轮任务执行结束"),
				TaskTitle:   title,
				ProjectName: proj,
				TmuxSession: tmuxSess,
			},
		}
	}
}

func parseCodex(event, raw string, data map[string]any, tmuxSess string) ParsedEvent {
	cwd := strVal(data, "cwd")
	proj := projectName(cwd)
	title := ResolveTaskTitle("codex", raw)
	sessID := strVal(data, "session_id")

	switch event {
	case "permission", "PermissionRequest":
		toolName := strVal(data, "tool_name")
		if toolName == "" {
			toolName = strVal(data, "trigger")
		}
		toolInput, _ := data["tool_input"].(map[string]any)
		cmdOrPath := extractToolDetail(toolName, toolInput)
		if cmdOrPath == "" {
			cmdOrPath = oneLine(raw, 200)
		}
		summary := formatToolSummary(toolName, cmdOrPath)
		outside := isOutsideWorkspace(extractPathArg(toolInput), []string{cwd})
		rk := risk.Evaluate(toolName, cmdOrPath, false, outside)
		return ParsedEvent{
			Kind: ActionApproval,
			Approval: proto.Approval{
				Agent:       "codex",
				SessionID:   sessID,
				TmuxSession: tmuxSess,
				TaskTitle:   title,
				ProjectName: proj,
				Cwd:         cwd,
				ToolName:    toolName,
				Summary:     summary,
				PayloadJSON: prettyJSON(data),
				RiskLevel:   rk.Level,
				RiskReasons: rk.Reasons,
			},
		}
	case "attention":
		return ParsedEvent{
			Kind: ActionMessage,
			Message: proto.Message{
				Agent:       "codex",
				EventType:   "attention",
				Priority:    "high",
				Title:       "Codex 需要审批或确认",
				Body:        fallbackStr(title, "Codex 等待你的输入"),
				TaskTitle:   title,
				ProjectName: proj,
				TmuxSession: tmuxSess,
			},
		}
	default:
		// Auto-detect attention vs done if called from legacy notify hook.
		lowered := strings.ToLower(raw)
		evType := "done"
		prio := "default"
		mTitle := "Codex 任务已完成"
		if event == "auto" && (strings.Contains(lowered, "approval") || strings.Contains(lowered, "permission") || strings.Contains(lowered, "request_user_input")) && !strings.Contains(lowered, "turn-complete") {
			evType = "attention"
			prio = "high"
			mTitle = "Codex 需要审批或确认"
		}
		return ParsedEvent{
			Kind: ActionMessage,
			Message: proto.Message{
				Agent:       "codex",
				EventType:   evType,
				Priority:    prio,
				Title:       mTitle,
				Body:        fallbackStr(title, "本轮任务执行结束"),
				TaskTitle:   title,
				ProjectName: proj,
				TmuxSession: tmuxSess,
			},
		}
	}
}

func parseAgy(event, raw string, data map[string]any, tmuxSess string) ParsedEvent {
	cwd := strVal(data, "cwd")
	roots := extractStringList(data["workspacePaths"])
	if len(roots) == 0 && cwd != "" {
		roots = []string{cwd}
	}
	proj := projectName(cwd)
	if proj == "" && len(roots) > 0 {
		proj = projectName(roots[0])
	}
	title := ResolveTaskTitle("agy", raw)
	convID := strVal(data, "conversationId")

	if event == "Stop" || event == "done" {
		reason := strVal(data, "terminationReason")
		errMsg := strings.TrimSpace(strVal(data, "error"))
		fullyIdle, hasIdle := data["fullyIdle"].(bool)

		if reason == "USER_CANCELED" || (hasIdle && !fullyIdle) {
			return ParsedEvent{Kind: ActionNone, DefaultOut: "{\"decision\":\"stop\"}\n"}
		}
		if reason == "ERROR" {
			return ParsedEvent{
				Kind:       ActionMessage,
				DefaultOut: "{\"decision\":\"stop\"}\n",
				Message: proto.Message{
					Agent:       "agy",
					EventType:   "error",
					Priority:    "high",
					Title:       "Antigravity 任务出错",
					Body:        fallbackStr(oneLine(errMsg, 240), "执行过程发生错误"),
					TaskTitle:   title,
					ProjectName: proj,
					TmuxSession: tmuxSess,
				},
			}
		}
		if reason == "MAX_INVOCATIONS" || reason == "MAX_FORCED_INVOCATIONS" || reason == "MAX_TOKEN_BUDGET_EXCEEDED" {
			return ParsedEvent{
				Kind:       ActionMessage,
				DefaultOut: "{\"decision\":\"stop\"}\n",
				Message: proto.Message{
					Agent:       "agy",
					EventType:   "attention",
					Priority:    "high",
					Title:       fmt.Sprintf("Antigravity 达到上限 (%s)", reason),
					Body:        fallbackStr(title, reason),
					TaskTitle:   title,
					ProjectName: proj,
					TmuxSession: tmuxSess,
				},
			}
		}
		return ParsedEvent{
			Kind:       ActionMessage,
			DefaultOut: "{\"decision\":\"stop\"}\n",
			Message: proto.Message{
				Agent:       "agy",
				EventType:   "done",
				Priority:    "default",
				Title:       "Antigravity 任务已完成",
				Body:        fallbackStr(title, "本轮任务执行结束"),
				TaskTitle:   title,
				ProjectName: proj,
				TmuxSession: tmuxSess,
			},
		}
	}

	// PreToolUse
	tc, _ := data["toolCall"].(map[string]any)
	if tc == nil {
		return ParsedEvent{Kind: ActionNone, DefaultOut: "{\"decision\":\"allow\"}\n"}
	}
	toolName := strVal(tc, "name")
	args, _ := tc["args"].(map[string]any)
	if args == nil {
		args = map[string]any{}
	}

	if toolName == "ask_question" {
		qText := extractAgyQuestion(args)
		return ParsedEvent{
			Kind:       ActionMessage,
			DefaultOut: "{\"decision\":\"allow\"}\n",
			Message: proto.Message{
				Agent:       "agy",
				EventType:   "attention",
				Priority:    "high",
				Title:       "Antigravity 等待你回答问题",
				Body:        fallbackStr(qText, "请在终端或桌面端回复选项"),
				TaskTitle:   title,
				ProjectName: proj,
				TmuxSession: tmuxSess,
			},
		}
	}

	if toolName == "run_command" {
		bypass, _ := args["BypassSandbox"].(bool)
		cmdLine := strVal(args, "CommandLine")
		toolSummary := strVal(args, "toolSummary")
		if bypass {
			summary := toolSummary
			if summary == "" {
				summary = oneLine(cmdLine, 140)
			} else if cmdLine != "" {
				summary = summary + ": " + oneLine(cmdLine, 120)
			}
			rk := risk.Evaluate(toolName, cmdLine, true, false)
			return ParsedEvent{
				Kind:       ActionApproval,
				DefaultOut: "{\"decision\":\"allow\"}\n",
				Approval: proto.Approval{
					Agent:       "agy",
					SessionID:   convID,
					TmuxSession: tmuxSess,
					TaskTitle:   title,
					ProjectName: proj,
					Cwd:         cwd,
					ToolName:    toolName,
					Summary:     summary,
					PayloadJSON: prettyJSON(args),
					RiskLevel:   rk.Level,
					RiskReasons: rk.Reasons,
				},
			}
		}
		return ParsedEvent{Kind: ActionNone, DefaultOut: "{\"decision\":\"allow\"}\n"}
	}

	if toolName == "ask_permission" {
		action := strVal(args, "Action")
		target := strVal(args, "Target")
		reason := strVal(args, "Reason")
		summary := fmt.Sprintf("请求权限 %s(%s)", action, target)
		if reason != "" {
			summary += " - " + oneLine(reason, 100)
		}
		rk := risk.Evaluate(toolName, action+" "+target+" "+reason, true, false)
		return ParsedEvent{
			Kind:       ActionApproval,
			DefaultOut: "{\"decision\":\"allow\"}\n",
			Approval: proto.Approval{
				Agent:       "agy",
				SessionID:   convID,
				TmuxSession: tmuxSess,
				TaskTitle:   title,
				ProjectName: proj,
				Cwd:         cwd,
				ToolName:    toolName,
				Summary:     summary,
				PayloadJSON: prettyJSON(args),
				RiskLevel:   rk.Level,
				RiskReasons: rk.Reasons,
			},
		}
	}

	if toolName == "write_file" || toolName == "edit_file" || toolName == "write_to_file" || toolName == "replace_file_content" || toolName == "multi_replace_file_content" {
		if am, ok := args["ArtifactMetadata"].(map[string]any); ok {
			if rf, _ := am["RequestFeedback"].(bool); rf {
				target := strVal(args, "Path")
				if target == "" {
					target = strVal(args, "TargetFile")
				}
				name := filepath.Base(target)
				sum := oneLine(strVal(am, "Summary"), 160)
				body := name
				if sum != "" {
					body = name + " - " + sum
				}
				return ParsedEvent{
					Kind:       ActionMessage,
					DefaultOut: "{\"decision\":\"allow\"}\n",
					Message: proto.Message{
						Agent:       "agy",
						EventType:   "attention",
						Priority:    "high",
						Title:       "Antigravity 待确认方案",
						Body:        body,
						TaskTitle:   title,
						ProjectName: proj,
						TmuxSession: tmuxSess,
					},
				}
			}
		}

		target := strVal(args, "Path")
		if target == "" {
			target = strVal(args, "TargetFile")
		}
		allowedRoots := append([]string(nil), roots...)
		if artDir := strVal(data, "artifactDirectoryPath"); artDir != "" {
			allowedRoots = append(allowedRoots, artDir)
		}
		allowedRoots = append(allowedRoots, "/tmp", "/private/tmp")
		if target != "" && isOutsideWorkspace(target, allowedRoots) {
			rk := risk.Evaluate(toolName, target, false, true)
			return ParsedEvent{
				Kind:       ActionApproval,
				DefaultOut: "{\"decision\":\"allow\"}\n",
				Approval: proto.Approval{
					Agent:       "agy",
					SessionID:   convID,
					TmuxSession: tmuxSess,
					TaskTitle:   title,
					ProjectName: proj,
					Cwd:         cwd,
					ToolName:    toolName,
					Summary:     fmt.Sprintf("修改工作区外文件: %s", displayPath(target)),
					PayloadJSON: prettyJSON(args),
					RiskLevel:   rk.Level,
					RiskReasons: rk.Reasons,
				},
			}
		}
	}

	return ParsedEvent{Kind: ActionNone, DefaultOut: "{\"decision\":\"allow\"}\n"}
}

func extractAgyQuestion(args map[string]any) string {
	qs, ok := args["questions"].([]any)
	if !ok || len(qs) == 0 {
		return ""
	}
	first, ok := qs[0].(map[string]any)
	if !ok {
		return ""
	}
	q := oneLine(strVal(first, "question"), 160)
	opts := extractStringList(first["options"])
	if len(opts) > 0 {
		return q + "\n选项: " + strings.Join(opts, " / ")
	}
	return q
}

func extractToolDetail(toolName string, input map[string]any) string {
	if input == nil {
		return ""
	}
	for _, k := range []string{"command", "CommandLine", "cmd", "file_path", "path", "Path", "TargetFile", "url", "Url"} {
		if v := strVal(input, k); v != "" {
			return v
		}
	}
	if arr, ok := input["command"].([]any); ok {
		parts := make([]string, 0, len(arr))
		for _, item := range arr {
			if s, ok := item.(string); ok {
				parts = append(parts, s)
			}
		}
		if len(parts) > 0 {
			return strings.Join(parts, " ")
		}
	}
	return oneLine(prettyJSON(input), 180)
}

func extractPathArg(input map[string]any) string {
	if input == nil {
		return ""
	}
	for _, k := range []string{"file_path", "path", "Path", "TargetFile"} {
		if v := strVal(input, k); v != "" {
			return v
		}
	}
	return ""
}

func formatToolSummary(toolName, detail string) string {
	if toolName == "" {
		return fallbackStr(oneLine(detail, 160), "请求执行操作")
	}
	if detail == "" {
		return toolName
	}
	return fmt.Sprintf("[%s] %s", toolName, oneLine(detail, 150))
}

func isOutsideWorkspace(target string, roots []string) bool {
	if target == "" || len(roots) == 0 {
		return false
	}
	absTarget := normalizePath(target)
	for _, r := range roots {
		if r == "" {
			continue
		}
		absRoot := normalizePath(r)
		if absTarget == absRoot || strings.HasPrefix(absTarget, absRoot+string(os.PathSeparator)) {
			return false
		}
	}
	return true
}

func normalizePath(p string) string {
	if strings.HasPrefix(p, "~/") {
		home, _ := os.UserHomeDir()
		p = filepath.Join(home, p[2:])
	}
	if abs, err := filepath.Abs(p); err == nil {
		p = abs
	}
	if real, err := filepath.EvalSymlinks(p); err == nil {
		return real
	}
	return filepath.Clean(p)
}

func displayPath(p string) string {
	home, _ := os.UserHomeDir()
	if home != "" && strings.HasPrefix(p, home+string(os.PathSeparator)) {
		return "~" + p[len(home):]
	}
	return p
}

func projectName(cwd string) string {
	if cwd == "" {
		return ""
	}
	base := filepath.Base(filepath.Clean(cwd))
	if base == "." || base == "/" {
		return ""
	}
	return base
}

func strVal(m map[string]any, key string) string {
	if m == nil {
		return ""
	}
	if v, ok := m[key].(string); ok {
		return v
	}
	return ""
}

func extractStringList(v any) []string {
	arr, ok := v.([]any)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(arr))
	for _, item := range arr {
		if s, ok := item.(string); ok && s != "" {
			out = append(out, s)
		}
	}
	return out
}

func oneLine(s string, limit int) string {
	cleaned := strings.Join(strings.Fields(s), " ")
	runes := []rune(cleaned)
	if len(runes) > limit {
		return string(runes[:limit]) + "..."
	}
	return cleaned
}

func fallbackStr(s, def string) string {
	if strings.TrimSpace(s) == "" {
		return def
	}
	return s
}

func prettyJSON(v any) string {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return ""
	}
	if len(b) > 8192 {
		return string(b[:8192]) + "\n... (truncated)"
	}
	return string(b)
}
