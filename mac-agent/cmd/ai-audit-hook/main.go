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
	"os/signal"
	"strings"
	"syscall"
	"time"

	"phone-ai-audit/mac-agent/internal/adapters"
	"phone-ai-audit/mac-agent/internal/config"
	"phone-ai-audit/mac-agent/internal/proto"
)

func main() {
	if len(os.Args) < 3 {
		os.Exit(0)
	}
	agent := os.Args[1]
	event := os.Args[2]

	rawPayload := ""
	if len(os.Args) > 3 {
		rawPayload = strings.Join(os.Args[3:], " ")
	} else if fi, err := os.Stdin.Stat(); err == nil && (fi.Mode()&os.ModeCharDevice) == 0 {
		b, _ := io.ReadAll(io.LimitReader(os.Stdin, 2*1024*1024))
		rawPayload = string(b)
	}

	parsed := adapters.Parse(agent, event, rawPayload)
	if parsed.Kind == adapters.ActionNone {
		if parsed.DefaultOut != "" {
			_, _ = os.Stdout.WriteString(parsed.DefaultOut)
		}
		return
	}

	cfg, err := config.Load()
	if err != nil {
		if parsed.DefaultOut != "" {
			_, _ = os.Stdout.WriteString(parsed.DefaultOut)
		}
		return
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	if parsed.Kind == adapters.ActionMessage {
		if err := sendMessageViaDaemon(ctx, cfg, parsed.Message); err != nil {
			_ = sendMessageDirect(ctx, cfg, parsed.Message)
		}
		if parsed.DefaultOut != "" {
			_, _ = os.Stdout.WriteString(parsed.DefaultOut)
		}
		return
	}

	// Approval flow
	timeoutSec := 180
	decision, err := requestApprovalViaDaemon(ctx, cfg, parsed.Approval, timeoutSec)
	if err != nil {
		// Daemon might be down; try direct HTTPS create + wait fallback.
		decision, err = requestApprovalDirect(ctx, cfg, parsed.Approval, timeoutSec)
	}
	if err != nil {
		if parsed.DefaultOut != "" {
			_, _ = os.Stdout.WriteString(parsed.DefaultOut)
		}
		return
	}

	out := adapters.FormatDecisionOutput(agent, decision)
	if out != "" {
		_, _ = os.Stdout.WriteString(out)
	}
}

func unixHTTPClient(socketPath string, timeout time.Duration) *http.Client {
	return &http.Client{
		Timeout: timeout,
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				return (&net.Dialer{Timeout: 2 * time.Second}).DialContext(ctx, "unix", socketPath)
			},
		},
	}
}

func directHTTPClient(timeout time.Duration) *http.Client {
	return &http.Client{
		Timeout: timeout,
		Transport: &http.Transport{
			Proxy: nil, // Bypass local proxy to reach the relay server directly.
		},
	}
}

func sendMessageViaDaemon(ctx context.Context, cfg config.Config, m proto.Message) error {
	b, _ := json.Marshal(proto.HookMessageReq{Message: m})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://unix/local/message", bytes.NewReader(b))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := unixHTTPClient(cfg.SocketPath, 4*time.Second).Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return fmt.Errorf("daemon status %d", resp.StatusCode)
	}
	return nil
}

func sendMessageDirect(ctx context.Context, cfg config.Config, m proto.Message) error {
	b, _ := json.Marshal(m)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, cfg.ServerURL+"/api/v1/messages", bytes.NewReader(b))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+cfg.MacToken)
	req.Header.Set("Content-Type", "application/json")
	resp, err := directHTTPClient(6 * time.Second).Do(req)
	if err != nil {
		return err
	}
	_ = resp.Body.Close()
	return nil
}

func requestApprovalViaDaemon(ctx context.Context, cfg config.Config, a proto.Approval, timeoutSec int) (proto.Approval, error) {
	b, _ := json.Marshal(proto.HookApprovalReq{Approval: a, TimeoutSec: timeoutSec})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://unix/local/approval", bytes.NewReader(b))
	if err != nil {
		return proto.Approval{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := unixHTTPClient(cfg.SocketPath, time.Duration(timeoutSec+15)*time.Second).Do(req)
	if err != nil {
		return proto.Approval{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return proto.Approval{}, fmt.Errorf("daemon status %d", resp.StatusCode)
	}
	var out proto.Approval
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return proto.Approval{}, err
	}
	return out, nil
}

func requestApprovalDirect(ctx context.Context, cfg config.Config, a proto.Approval, timeoutSec int) (proto.Approval, error) {
	body := map[string]any{
		"agent":        a.Agent,
		"session_id":   a.SessionID,
		"tmux_session": a.TmuxSession,
		"task_title":   a.TaskTitle,
		"project_name": a.ProjectName,
		"cwd":          a.Cwd,
		"tool_name":    a.ToolName,
		"summary":      a.Summary,
		"payload_json": a.PayloadJSON,
		"risk_level":   a.RiskLevel,
		"risk_reasons": a.RiskReasons,
		"timeout_sec":  timeoutSec,
	}
	b, _ := json.Marshal(body)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, cfg.ServerURL+"/api/v1/approvals", bytes.NewReader(b))
	if err != nil {
		return proto.Approval{}, err
	}
	req.Header.Set("Authorization", "Bearer "+cfg.MacToken)
	req.Header.Set("Content-Type", "application/json")
	resp, err := directHTTPClient(8 * time.Second).Do(req)
	if err != nil {
		return proto.Approval{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return proto.Approval{}, fmt.Errorf("relay create status %d", resp.StatusCode)
	}
	var created proto.Approval
	if err := json.NewDecoder(resp.Body).Decode(&created); err != nil {
		return proto.Approval{}, err
	}
	if created.Status != "pending" {
		return created, nil
	}

	waitURL := fmt.Sprintf("%s/api/v1/approvals/%s/wait?timeout=%d", cfg.ServerURL, created.ID, timeoutSec)
	wreq, err := http.NewRequestWithContext(ctx, http.MethodGet, waitURL, nil)
	if err != nil {
		return proto.Approval{}, err
	}
	wreq.Header.Set("Authorization", "Bearer "+cfg.MacToken)
	wresp, err := directHTTPClient(time.Duration(timeoutSec+15) * time.Second).Do(wreq)
	if err != nil {
		if ctx.Err() != nil {
			cancelCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			creq, _ := http.NewRequestWithContext(cancelCtx, http.MethodPost, cfg.ServerURL+"/api/v1/approvals/"+created.ID+"/cancel", nil)
			if creq != nil {
				creq.Header.Set("Authorization", "Bearer "+cfg.MacToken)
				if cr, cerr := directHTTPClient(3 * time.Second).Do(creq); cerr == nil {
					_ = cr.Body.Close()
				}
			}
		}
		return proto.Approval{}, err
	}
	defer wresp.Body.Close()
	var resolved proto.Approval
	if err := json.NewDecoder(wresp.Body).Decode(&resolved); err != nil {
		return proto.Approval{}, err
	}
	return resolved, nil
}
