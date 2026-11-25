package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	qrcode "github.com/skip2/go-qrcode"

	"phone-ai-audit/mac-agent/internal/config"
	"phone-ai-audit/mac-agent/internal/idle"
	"phone-ai-audit/mac-agent/internal/proto"
	"phone-ai-audit/mac-agent/internal/term"
	"phone-ai-audit/mac-agent/internal/usage"
)

func main() {
	if len(os.Args) < 2 {
		printHelp()
		return
	}
	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintf(os.Stderr, "无法读取配置 (%s): %v\n", config.DefaultPath(), err)
		os.Exit(1)
	}

	switch os.Args[1] {
	case "status":
		cmdStatus(cfg)
	case "mode":
		if len(os.Args) < 3 {
			fmt.Println("用法: ai-audit mode auto|both|phone|notify")
			os.Exit(1)
		}
		patchSettings(cfg, map[string]any{"approval_mode": os.Args[2]})
	case "awake":
		if len(os.Args) < 3 {
			fmt.Println("用法: ai-audit awake on|off")
			os.Exit(1)
		}
		patchSettings(cfg, map[string]any{"mac_awake_enabled": os.Args[2] == "on"})
	case "terminal":
		if len(os.Args) < 3 {
			fmt.Println("用法: ai-audit terminal on|off")
			os.Exit(1)
		}
		patchSettings(cfg, map[string]any{"terminal_enabled": os.Args[2] == "on"})
	case "files":
		if len(os.Args) < 3 {
			fmt.Println("用法: ai-audit files on|off")
			os.Exit(1)
		}
		patchSettings(cfg, map[string]any{"files_enabled": os.Args[2] == "on"})
	case "pair":
		cmdPair(cfg)
	case "usage":
		cmdUsage(cfg)
	case "web":
		cmdWeb(cfg)
	case "test-msg":
		cmdTestMessage(cfg)
	case "test-approve":
		cmdTestApproval(cfg)
	default:
		printHelp()
	}
}

func printHelp() {
	fmt.Println(`Phone AI Audit Mac 控制命令:
  ai-audit status                         查看中转状态、手机在线数、当前模式、tmux 会话
  ai-audit usage                          查看 Claude / Codex / Antigravity 用量与配额重置时间
  ai-audit web                            在浏览器打开本机 Agent 用量看板网页
  ai-audit mode auto|both|phone|notify    切换审批模式 (智能离座 / 双端同步 / 始终手机 / 仅通知)
  ai-audit awake on|off                   开启/关闭 Mac 防休眠 (caffeinate)
  ai-audit terminal on|off                开启/关闭远程交互终端总开关
  ai-audit files on|off                   开启/关闭远程文件查看/编辑与 Git 操作总开关
  ai-audit pair                           打印 Android App 扫码配对二维码与配置信息
  ai-audit test-msg                       发送一条测试消息到手机
  ai-audit test-approve                   发起一次测试远程审批并等待决策`)
}

func client() *http.Client {
	return &http.Client{
		Timeout:   10 * time.Second,
		Transport: &http.Transport{Proxy: nil},
	}
}

func relayReq(cfg config.Config, method, path string, body any) ([]byte, int, error) {
	var r io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		r = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, cfg.ServerURL+path, r)
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("Authorization", "Bearer "+cfg.MacToken)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := client().Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	return data, resp.StatusCode, nil
}

func cmdStatus(cfg config.Config) {
	data, code, err := relayReq(cfg, http.MethodGet, "/api/v1/status", nil)
	if err != nil {
		fmt.Fprintf(os.Stderr, "连接中转服务失败 (%s): %v\n", cfg.ServerURL, err)
		os.Exit(1)
	}
	if code >= 300 {
		fmt.Fprintf(os.Stderr, "中转返回 HTTP %d: %s\n", code, string(data))
		os.Exit(1)
	}
	var pretty bytes.Buffer
	_ = json.Indent(&pretty, data, "", "  ")
	idleSt := idle.Detect()
	fmt.Printf("Relay: %s\nMac 空闲: %ds (锁屏=%v)\n本机 tmux 会话: %d\n%s\n",
		cfg.ServerURL, idleSt.IdleSeconds, idleSt.Locked, len(term.ListTmuxSessions()), pretty.String())
}

func patchSettings(cfg config.Config, patch map[string]any) {
	data, code, err := relayReq(cfg, http.MethodPut, "/api/v1/settings", patch)
	if err != nil || code >= 300 {
		fmt.Fprintf(os.Stderr, "更新设置失败 (code=%d): %v %s\n", code, err, string(data))
		os.Exit(1)
	}
	fmt.Printf("设置已同步: %s\n", strings.TrimSpace(string(data)))
}

func cmdPair(cfg config.Config) {
	if cfg.AppToken == "" {
		fmt.Fprintln(os.Stderr, "本地配置缺少 app_token，请先在 ~/.config/ai-audit/config.json 填入 app_token")
		os.Exit(1)
	}
	payload, _ := json.Marshal(map[string]string{
		"server_url": cfg.ServerURL,
		"app_token":  cfg.AppToken,
	})
	qr, err := qrcode.New(string(payload), qrcode.Medium)
	if err != nil {
		fmt.Fprintf(os.Stderr, "生成二维码失败: %v\n", err)
		os.Exit(1)
	}
	fmt.Println("请打开手机端 Phone AI Audit -> 设置 -> 扫码配置：")
	fmt.Println(qr.ToSmallString(false))
	fmt.Printf("服务器地址: %s\n", cfg.ServerURL)
	fmt.Printf("App Token:  %s\n", cfg.AppToken)
}

func cmdWeb(cfg config.Config) {
	url := cfg.WebURL()
	if url == "" {
		fmt.Fprintln(os.Stderr, "用量网页看板已关闭（可在 config.json 设置 web_addr，如 \"127.0.0.1:9878\"）")
		os.Exit(1)
	}
	fmt.Printf("打开 Agent 用量看板: %s\n", url)
	_ = exec.Command("open", url).Start()
}

func cmdUsage(cfg config.Config) {
	var sum usage.Summary
	fetched := false
	unixCli := &http.Client{
		Timeout: 20 * time.Second,
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				return (&net.Dialer{Timeout: 2 * time.Second}).DialContext(ctx, "unix", cfg.SocketPath)
			},
		},
	}
	if resp, err := unixCli.Get("http://unix/local/usage"); err == nil {
		defer resp.Body.Close()
		if resp.StatusCode == http.StatusOK && json.NewDecoder(resp.Body).Decode(&sum) == nil {
			fetched = true
		}
	}
	if !fetched {
		eng := usage.New(usage.Options{Hostname: cfg.Hostname, WebURL: cfg.WebURL(), ProxyURL: cfg.ProxyURL})
		s, err := eng.Snapshot(context.Background(), true)
		if err != nil {
			fmt.Fprintf(os.Stderr, "统计用量失败: %v\n", err)
			os.Exit(1)
		}
		sum = s
	}

	fmt.Printf("Agent 用量概览 (%s) | 增量扫描 %dms (缓存 %d/%d)\n",
		sum.Hostname, sum.Meta.ScanMs, sum.Meta.FilesCached, sum.Meta.FilesTotal)
	if sum.WebURL != "" {
		fmt.Printf("网页看板: %s\n", sum.WebURL)
	}
	fmt.Printf("合计: 今日 %s | 5h %s | 本周 %s | 本月 %s | 总计 %s\n\n",
		fmtTok(sum.Today.TotalTokens), fmtTok(sum.FiveHour.TotalTokens),
		fmtTok(sum.Week.TotalTokens), fmtTok(sum.Month.TotalTokens), fmtTok(sum.Total.TotalTokens))
	for _, a := range sum.Agents {
		plan := ""
		if a.PlanType != "" {
			plan = " [" + strings.ToUpper(a.PlanType) + "]"
		}
		fmt.Printf("%-14s%s  今日 %-8s  本周 %-8s  本月 %-8s  总计 %-8s\n",
			a.DisplayName, plan,
			fmtTok(a.Today.TotalTokens), fmtTok(a.Week.TotalTokens),
			fmtTok(a.Month.TotalTokens), fmtTok(a.Total.TotalTokens))
		fmt.Printf("  5h 窗口: %-18s  周窗口: %s\n",
			fmtWindowBrief(a.Window5h), fmtWindowBrief(a.WindowWeekly))
		if a.HistoryNote != "" {
			fmt.Printf("  说明: %s\n", a.HistoryNote)
		}
	}
}

func fmtTok(n int64) string {
	switch {
	case n >= 1_000_000_000:
		return fmt.Sprintf("%.2fB", float64(n)/1e9)
	case n >= 1_000_000:
		return fmt.Sprintf("%.2fM", float64(n)/1e6)
	case n >= 1_000:
		return fmt.Sprintf("%.1fK", float64(n)/1e3)
	default:
		return fmt.Sprintf("%d", n)
	}
}

func fmtWindowBrief(w usage.QuotaWindow) string {
	val := fmtTok(w.Tokens.TotalTokens)
	if w.UsedPercent != nil {
		if w.Expired {
			val = "已重置(0%)"
		} else {
			left := 100.0 - *w.UsedPercent
			if left < 0 {
				left = 0
			}
			val = fmt.Sprintf("已用 %.0f%%/剩 %.0f%%", *w.UsedPercent, left)
		}
	}
	if w.ResetsAt <= 0 {
		if w.Source == "local" && w.Tokens.TotalTokens > 0 {
			return val + " (本地统计)"
		}
		return val + " (空闲)"
	}
	t := time.UnixMilli(w.ResetsAt).In(time.Local)
	return fmt.Sprintf("%s (重置 %s)", val, t.Format("01-02 15:04"))
}

func cmdTestMessage(cfg config.Config) {
	cwd, _ := os.Getwd()
	data, code, err := relayReq(cfg, http.MethodPost, "/api/v1/messages", map[string]any{
		"agent":        "agy",
		"event_type":   "done",
		"priority":     "high",
		"title":        "Phone AI Audit 联通测试",
		"body":         "如果你在手机端看到这条推送，说明中转通道正常！",
		"task_title":   "测试消息推送",
		"project_name": filepath.Base(cwd),
		"tmux_session": term.CurrentTmuxSession(),
	})
	if err != nil || code >= 300 {
		fmt.Fprintf(os.Stderr, "发送失败 (code=%d): %v %s\n", code, err, string(data))
		os.Exit(1)
	}
	fmt.Println("测试消息已发送:", strings.TrimSpace(string(data)))
}

func cmdTestApproval(cfg config.Config) {
	cwd, _ := os.Getwd()
	approval := proto.Approval{
		Agent:       "claude",
		TmuxSession: term.CurrentTmuxSession(),
		TaskTitle:   "远程审批联通测试",
		ProjectName: filepath.Base(cwd),
		Cwd:         cwd,
		ToolName:    "Bash",
		Summary:     "[Bash] echo 'hello from phone approval'",
		PayloadJSON: `{"command": "echo 'hello from phone approval'"}`,
		RiskLevel:   "medium",
		RiskReasons: []string{"测试审批请求"},
	}

	// Prefer routing through the local daemon so `both` (dual-end) and `auto` modes behave identically to real hooks.
	unixCli := &http.Client{
		Timeout: 105 * time.Second,
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				return (&net.Dialer{Timeout: 2 * time.Second}).DialContext(ctx, "unix", cfg.SocketPath)
			},
		},
	}
	reqBody, _ := json.Marshal(proto.HookApprovalReq{Approval: approval, TimeoutSec: 90})
	if ureq, err := http.NewRequest(http.MethodPost, "http://unix/local/approval", bytes.NewReader(reqBody)); err == nil {
		ureq.Header.Set("Content-Type", "application/json")
		fmt.Println("已通过本机守护进程发起测试审批，等待决策中 (最长 90 秒)...")
		if uresp, err := unixCli.Do(ureq); err == nil {
			defer uresp.Body.Close()
			resBytes, _ := io.ReadAll(uresp.Body)
			if uresp.StatusCode < 300 {
				fmt.Println("收到审批结果:", strings.TrimSpace(string(resBytes)))
				return
			}
		}
	}

	// Fallback to direct relay call if daemon is offline.
	data, code, err := relayReq(cfg, http.MethodPost, "/api/v1/approvals", map[string]any{
		"agent":        approval.Agent,
		"tmux_session": approval.TmuxSession,
		"task_title":   approval.TaskTitle,
		"project_name": approval.ProjectName,
		"cwd":          approval.Cwd,
		"tool_name":    approval.ToolName,
		"summary":      approval.Summary,
		"payload_json": approval.PayloadJSON,
		"risk_level":   approval.RiskLevel,
		"risk_reasons": approval.RiskReasons,
		"timeout_sec":  90,
	})
	if err != nil || code >= 300 {
		fmt.Fprintf(os.Stderr, "创建审批失败 (code=%d): %v %s\n", code, err, string(data))
		os.Exit(1)
	}
	var created struct {
		ID     string `json:"id"`
		Status string `json:"status"`
	}
	_ = json.Unmarshal(data, &created)
	fmt.Printf("已发起审批 id=%s status=%s\n", created.ID, created.Status)
	if created.Status != "pending" {
		fmt.Println("非 pending 状态（可能是手机未在线或仅通知模式）:", string(data))
		return
	}
	fmt.Println("等待手机端审批中 (最长 90 秒)...")
	longCli := &http.Client{Timeout: 100 * time.Second, Transport: &http.Transport{Proxy: nil}}
	req, _ := http.NewRequest(http.MethodGet, cfg.ServerURL+"/api/v1/approvals/"+created.ID+"/wait?timeout=90", nil)
	req.Header.Set("Authorization", "Bearer "+cfg.MacToken)
	resp, err := longCli.Do(req)
	if err != nil {
		fmt.Fprintf(os.Stderr, "等待失败: %v\n", err)
		os.Exit(1)
	}
	defer resp.Body.Close()
	resBytes, _ := io.ReadAll(resp.Body)
	fmt.Println("收到审批结果:", strings.TrimSpace(string(resBytes)))
}
