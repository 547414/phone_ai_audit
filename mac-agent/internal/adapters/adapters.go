package adapters

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
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
		bypass := boolArg(args, "BypassSandbox", "bypassSandbox", "bypass_sandbox")
		cmdLine := strArg(args, "CommandLine", "commandLine", "command", "cmd")
		toolSummary := strArg(args, "toolSummary", "ToolSummary")
		rk := risk.Evaluate(toolName, cmdLine, bypass, false)
		needApproval, extraReasons := shouldApproveAgyCommand(cmdLine, bypass, rk.Level, roots)
		if needApproval {
			summary := toolSummary
			if summary == "" {
				summary = oneLine(cmdLine, 140)
			} else if cmdLine != "" {
				summary = summary + ": " + oneLine(cmdLine, 120)
			}
			reasons := mergeUniqueStrings(rk.Reasons, extraReasons)
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
					RiskReasons: reasons,
				},
			}
		}
		return ParsedEvent{Kind: ActionNone, DefaultOut: "{\"decision\":\"allow\"}\n"}
	}

	if toolName == "ask_permission" {
		action := strArg(args, "Action", "action")
		target := strArg(args, "Target", "target")
		reason := strArg(args, "Reason", "reason")
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

	if toolName == "write_file" || toolName == "edit_file" || toolName == "write_to_file" || toolName == "replace_file_content" || toolName == "multi_replace_file_content" || toolName == "delete_file" || toolName == "remove_file" || toolName == "move_file" || toolName == "rename_file" || toolName == "apply_patch" {
		if am, ok := args["ArtifactMetadata"].(map[string]any); ok {
			if rf, _ := am["RequestFeedback"].(bool); rf {
				target := strArg(args, "Path", "TargetFile", "path", "file_path")
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

		target := strArg(args, "Path", "TargetFile", "Destination", "dst", "path", "file_path")
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

	if toolName == "read_file" || toolName == "view_file" {
		target := strArg(args, "Path", "AbsolutePath", "path", "file_path")
		allowedRoots := append([]string(nil), roots...)
		if artDir := strVal(data, "artifactDirectoryPath"); artDir != "" {
			allowedRoots = append(allowedRoots, artDir)
		}
		if home, err := os.UserHomeDir(); err == nil && home != "" {
			allowedRoots = append(allowedRoots, filepath.Join(home, ".gemini"), filepath.Join(home, ".agents"))
		}
		allowedRoots = append(allowedRoots, "/tmp", "/private/tmp")
		if target != "" && (isSensitivePath(target) || isOutsideWorkspace(target, allowedRoots)) {
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
					Summary:     fmt.Sprintf("读取敏感或工作区外文件: %s", displayPath(target)),
					PayloadJSON: prettyJSON(args),
					RiskLevel:   rk.Level,
					RiskReasons: mergeUniqueStrings(rk.Reasons, []string{"读取敏感或工作区外路径"}),
				},
			}
		}
	}

	if isAgyExternalMCPTool(toolName) {
		detail := extractToolDetail(toolName, args)
		summary := formatToolSummary(toolName, detail)
		rk := risk.Evaluate(toolName, detail, false, false)
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
				RiskReasons: mergeUniqueStrings(rk.Reasons, []string{"调用外部 MCP / 扩展工具"}),
			},
		}
	}

	return ParsedEvent{Kind: ActionNone, DefaultOut: "{\"decision\":\"allow\"}\n"}
}

type agyCmdRule struct {
	re     *regexp.Regexp
	reason string
}

var agyApprovalCmdRules = []agyCmdRule{
	{regexp.MustCompile(`(?i)\bgit\s+(push|commit|pull|fetch|rebase|merge|cherry-pick|revert|reset|clean|stash|tag\s+-[df]|branch\s+-[dD]|checkout\s+--|restore)\b`), "涉及 Git 提交/推送/拉取或历史变更"},
	{regexp.MustCompile(`(?i)\b(ssh|scp|rsync|sftp|kubectl|helm|terraform|ansible|systemctl|nginx|certbot|launchctl)\b`), "涉及远程主机或系统服务操作"},
	{regexp.MustCompile(`(?i)\bdocker\s+(push|rm|rmi|system\s+prune|compose\s+(up|down|restart)|run|exec|stop|kill)\b`), "涉及 Docker 容器或镜像变更"},
	{regexp.MustCompile(`(?i)\bgh\s+(pr\s+(create|merge|close|edit)|release\s+(create|delete|upload)|issue\s+(create|close)|repo\s+(create|delete))\b`), "涉及 GitHub 远端写入操作"},
	{regexp.MustCompile(`(?i)\b(npm|pnpm|yarn|cargo|gem|poetry|twine)\s+(publish|upload)\b`), "涉及软件包发布上传"},
	{regexp.MustCompile(`(?i)\b(brew|npm|pnpm|yarn|pip|pip3|uv)\s+(install|uninstall|remove|upgrade)\b`), "涉及外部依赖安装或卸载"},
	{regexp.MustCompile(`(?i)\b(curl|wget)\b`), "涉及网络请求（沙箱外可能触发授权）"},
	{regexp.MustCompile(`(?i)\b(killall|pkill|shutdown|reboot|diskutil|chmod|chown)\b`), "涉及系统进程、权限或磁盘操作"},
	{regexp.MustCompile(`(?i)(^|[;&|]\s*)rm\s+`), "涉及文件删除操作"},
}

var absPathInCmdRe = regexp.MustCompile(`(?:^|[\s"'=:])(/(?:Users|home|etc|var|opt|Library|System|Applications)/[^\s"'|;&>)]+|~/[^\s"'|;&>)]*)`)

// shouldApproveAgyCommand decides whether an Antigravity run_command PreToolUse call
// should trigger Phone/Mac dual-end approval even when BypassSandbox is false, because
// AGY's built-in auto/default policy still prompts for git writes, network, external paths,
// or high-risk commands.
func shouldApproveAgyCommand(cmdLine string, bypass bool, riskLevel string, roots []string) (bool, []string) {
	if bypass || riskLevel == "high" {
		return true, nil
	}
	var extra []string
	for _, r := range agyApprovalCmdRules {
		if r.re.MatchString(cmdLine) {
			extra = append(extra, r.reason)
		}
	}
	if len(roots) > 0 {
		allowed := append([]string(nil), roots...)
		allowed = append(allowed, "/tmp", "/private/tmp", "/opt/homebrew", "/usr/local")
		for _, m := range absPathInCmdRe.FindAllStringSubmatch(cmdLine, -1) {
			if len(m) < 2 {
				continue
			}
			p := strings.TrimRight(m[1], ".,;:")
			if isOutsideWorkspace(p, allowed) {
				extra = append(extra, "命令访问工作区外路径")
				break
			}
		}
	}
	return len(extra) > 0, extra
}

func isSensitivePath(target string) bool {
	p := strings.ToLower(normalizePath(target))
	base := strings.ToLower(filepath.Base(p))
	if base == ".env" || strings.HasPrefix(base, ".env.") || strings.HasSuffix(base, ".pem") || strings.HasSuffix(base, ".key") || strings.HasPrefix(base, "id_rsa") || strings.HasPrefix(base, "id_ed25519") {
		return true
	}
	for _, seg := range []string{"/.ssh/", "/.aws/", "/.kube/", "/.gnupg/", "/etc/"} {
		if strings.Contains(p, seg) {
			return true
		}
	}
	return false
}

func isAgyExternalMCPTool(toolName string) bool {
	tl := strings.ToLower(strings.TrimSpace(toolName))
	if tl == "" {
		return false
	}
	return strings.HasPrefix(tl, "mcp_") || strings.HasPrefix(tl, "mcp__")
}

func boolArg(m map[string]any, keys ...string) bool {
	if m == nil {
		return false
	}
	for _, k := range keys {
		if v, ok := m[k].(bool); ok {
			return v
		}
	}
	return false
}

func strArg(m map[string]any, keys ...string) string {
	for _, k := range keys {
		if v := strVal(m, k); v != "" {
			return v
		}
	}
	return ""
}

func mergeUniqueStrings(a, b []string) []string {
	seen := make(map[string]struct{}, len(a)+len(b))
	out := make([]string, 0, len(a)+len(b))
	for _, s := range append(append([]string(nil), a...), b...) {
		if s == "" {
			continue
		}
		if _, ok := seen[s]; ok {
			continue
		}
		seen[s] = struct{}{}
		out = append(out, s)
	}
	return out
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
