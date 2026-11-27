package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/gorilla/websocket"

	"phone-ai-audit/mac-agent/internal/agyauth"
	"phone-ai-audit/mac-agent/internal/agywatch"
	"phone-ai-audit/mac-agent/internal/awake"
	"phone-ai-audit/mac-agent/internal/config"
	"phone-ai-audit/mac-agent/internal/fsengine"
	"phone-ai-audit/mac-agent/internal/gitengine"
	"phone-ai-audit/mac-agent/internal/idle"
	"phone-ai-audit/mac-agent/internal/prompt"
	"phone-ai-audit/mac-agent/internal/proto"
	"phone-ai-audit/mac-agent/internal/term"
	"phone-ai-audit/mac-agent/internal/usage"
)

const (
	binFrameStdin   byte = 0x01
	binFrameStdout  byte = 0x02
	binFrameFSChunk byte = 0x03
	binFrameFSEOF   byte = 0x04
)

type wsOut struct {
	mt   int
	data []byte
}

var errTunnelUnavailable = errors.New("tunnel unavailable")

type Daemon struct {
	cfg        config.Config
	awake      *awake.Controller
	termMgr    *term.Manager
	fsMgr      *fsengine.Engine
	usageMgr   *usage.Engine
	agyMgr     *agyauth.Manager
	agyWatcher *agywatch.Watcher

	mu       sync.Mutex
	wsConn   *websocket.Conn
	wsSend   chan wsOut
	wsDone   chan struct{}
	settings proto.Settings

	ackWaiters          map[string]chan proto.Approval
	decisionWaiters     map[string]chan proto.Approval
	expiryWaiters       map[string]chan proto.Approval
	fsStreams           map[string]context.CancelFunc
	activeHookSessions  map[string]int
	recentHookFallbacks map[string]time.Time
}

func newDaemon(cfg config.Config) *Daemon {
	d := &Daemon{
		cfg:   cfg,
		awake: awake.New(),
		fsMgr: fsengine.New(cfg.FileRoots),
		usageMgr: usage.New(usage.Options{
			Hostname: cfg.Hostname,
			WebURL:   cfg.WebURL(),
			ProxyURL: cfg.ProxyURL,
		}),
		agyMgr: agyauth.New(agyauth.Options{}),
		settings: proto.Settings{
			ApprovalMode:       "auto",
			IdleThresholdSec:   60,
			DefaultTimeoutSec:  30,
			TimeoutAction:      "approve",
			TerminalEnabled:    true,
			FilesEnabled:       true,
			MacAwakeEnabled:    false,
			HighRiskRequireApp: false,
		},
		ackWaiters:          make(map[string]chan proto.Approval),
		decisionWaiters:     make(map[string]chan proto.Approval),
		expiryWaiters:       make(map[string]chan proto.Approval),
		fsStreams:           make(map[string]context.CancelFunc),
		activeHookSessions:  make(map[string]int),
		recentHookFallbacks: make(map[string]time.Time),
	}
	d.agyWatcher = agywatch.New(agywatch.Callbacks{
		IsHookActive: func(sessionName string) bool {
			if sessionName == "" {
				return false
			}
			d.mu.Lock()
			defer d.mu.Unlock()
			return d.activeHookSessions[sessionName] > 0
		},
		WasRecentHookFallback: func(sessionName string) bool {
			if sessionName == "" {
				return false
			}
			d.mu.Lock()
			defer d.mu.Unlock()
			ts, ok := d.recentHookFallbacks[sessionName]
			if !ok {
				return false
			}
			if time.Since(ts) > 10*time.Second {
				delete(d.recentHookFallbacks, sessionName)
				return false
			}
			return true
		},
		RequestApproval: func(ctx context.Context, a proto.Approval) (proto.Approval, error) {
			return d.handleApprovalPolicy(ctx, a, 0)
		},
	})
	d.termMgr = term.NewManager(
		func(streamID [16]byte, data []byte) {
			frame := make([]byte, 17+len(data))
			frame[0] = binFrameStdout
			copy(frame[1:17], streamID[:])
			copy(frame[17:], data)
			d.sendWS(websocket.BinaryMessage, frame)
		},
		func(streamIDHex, reason string) {
			d.sendJSON(map[string]any{
				"type":      "term.closed",
				"stream_id": streamIDHex,
				"reason":    reason,
			})
		},
	)
	return d
}

func newReqID() string {
	var b [12]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

func (d *Daemon) sendWS(mt int, data []byte) bool {
	d.mu.Lock()
	ch := d.wsSend
	done := d.wsDone
	d.mu.Unlock()
	if ch == nil || done == nil {
		return false
	}
	select {
	case <-done:
		return false
	case ch <- wsOut{mt: mt, data: data}:
		return true
	default:
		return false
	}
}

func (d *Daemon) sendWSBlocking(ctx context.Context, mt int, data []byte) bool {
	d.mu.Lock()
	ch := d.wsSend
	done := d.wsDone
	d.mu.Unlock()
	if ch == nil || done == nil {
		return false
	}
	timer := time.NewTimer(25 * time.Second)
	defer timer.Stop()
	select {
	case <-done:
		return false
	case <-ctx.Done():
		return false
	case <-timer.C:
		return false
	case ch <- wsOut{mt: mt, data: data}:
		return true
	}
}

func (d *Daemon) sendJSON(v any) bool {
	b, err := json.Marshal(v)
	if err != nil {
		return false
	}
	return d.sendWS(websocket.TextMessage, b)
}

func (d *Daemon) sendJSONBlocking(ctx context.Context, v any) bool {
	b, err := json.Marshal(v)
	if err != nil {
		return false
	}
	return d.sendWSBlocking(ctx, websocket.TextMessage, b)
}

func (d *Daemon) runTunnel(ctx context.Context) {
	dialer := &websocket.Dialer{
		Proxy:            nil, // Always connect directly to the relay server bypassing local proxy.
		HandshakeTimeout: 12 * time.Second,
	}
	wsURL := d.cfg.WSBaseURL() + "/api/v1/ws/mac"
	headers := http.Header{"Authorization": []string{"Bearer " + d.cfg.MacToken}}

	backoff := time.Second
	for {
		if ctx.Err() != nil {
			return
		}
		conn, _, err := dialer.DialContext(ctx, wsURL, headers)
		if err != nil {
			log.Printf("tunnel dial %s failed: %v (retry in %s)", wsURL, err, backoff)
			select {
			case <-ctx.Done():
				return
			case <-time.After(backoff):
			}
			backoff *= 2
			if backoff > 20*time.Second {
				backoff = 20 * time.Second
			}
			continue
		}

		backoff = time.Second
		log.Printf("tunnel connected to %s", wsURL)
		d.serveConn(ctx, conn)
		log.Printf("tunnel disconnected, reconnecting...")
	}
}

func (d *Daemon) serveConn(ctx context.Context, conn *websocket.Conn) {
	sendCh := make(chan wsOut, 512)
	doneCh := make(chan struct{})

	d.mu.Lock()
	d.wsConn = conn
	d.wsSend = sendCh
	d.wsDone = doneCh
	d.mu.Unlock()

	defer func() {
		close(doneCh)
		_ = conn.Close()
		d.termMgr.CloseAll()

		d.mu.Lock()
		if d.wsConn == conn {
			d.wsConn = nil
			d.wsSend = nil
			d.wsDone = nil
		}
		for sid, cancel := range d.fsStreams {
			cancel()
			delete(d.fsStreams, sid)
		}
		// Wake any waiting hooks with fallback so they don't hang on tunnel drop.
		for id, ch := range d.decisionWaiters {
			select {
			case ch <- proto.Approval{ID: id, Status: "fallback", DecisionReason: "Mac 隧道断开，已回退本地"}:
			default:
			}
			delete(d.decisionWaiters, id)
		}
		for reqID, ch := range d.ackWaiters {
			close(ch)
			delete(d.ackWaiters, reqID)
		}
		d.mu.Unlock()
	}()

	conn.SetReadLimit(4 * 1024 * 1024)
	_ = conn.SetReadDeadline(time.Now().Add(70 * time.Second))
	conn.SetPongHandler(func(string) error {
		_ = conn.SetReadDeadline(time.Now().Add(70 * time.Second))
		return nil
	})

	// Writer & heartbeat goroutine
	go func() {
		pingTicker := time.NewTicker(25 * time.Second)
		hbTicker := time.NewTicker(15 * time.Second)
		defer pingTicker.Stop()
		defer hbTicker.Stop()

		// Hello immediately
		_ = d.sendJSON(map[string]any{
			"type":          "mac.hello",
			"hostname":      d.cfg.Hostname,
			"awake":         d.awake.Enabled(),
			"tmux_sessions": term.ListTmuxSessions(),
		})

		for {
			select {
			case <-ctx.Done():
				_ = conn.Close()
				return
			case <-doneCh:
				return
			case f := <-sendCh:
				_ = conn.SetWriteDeadline(time.Now().Add(20 * time.Second))
				if err := conn.WriteMessage(f.mt, f.data); err != nil {
					_ = conn.Close()
					return
				}
			case <-pingTicker.C:
				_ = conn.SetWriteDeadline(time.Now().Add(15 * time.Second))
				if err := conn.WriteMessage(websocket.PingMessage, nil); err != nil {
					_ = conn.Close()
					return
				}
			case <-hbTicker.C:
				_ = d.sendJSON(map[string]any{
					"type":          "mac.heartbeat",
					"hostname":      d.cfg.Hostname,
					"awake":         d.awake.Enabled(),
					"tmux_sessions": term.ListTmuxSessions(),
				})
			}
		}
	}()

	for {
		mt, data, err := conn.ReadMessage()
		if err != nil {
			return
		}
		_ = conn.SetReadDeadline(time.Now().Add(70 * time.Second))
		if mt == websocket.BinaryMessage {
			if len(data) >= 17 && data[0] == binFrameStdin {
				var sid [16]byte
				copy(sid[:], data[1:17])
				d.termMgr.WriteStdin(sid, data[17:])
			}
			continue
		}
		if mt == websocket.TextMessage {
			d.handleRelayText(ctx, data)
		}
	}
}

func (d *Daemon) handleRelayText(ctx context.Context, data []byte) {
	var env struct {
		Type     string          `json:"type"`
		ReqID    string          `json:"req_id"`
		Op       string          `json:"op"`
		Params   json.RawMessage `json:"params"`
		Settings *proto.Settings `json:"settings"`
		Approval *proto.Approval `json:"approval"`
		StreamID   string          `json:"stream_id"`
		Mode       string          `json:"mode"`
		Target     string          `json:"target"`
		AgyAccount string          `json:"agy_account"`
		Cwd        string          `json:"cwd"`
		Cols       int             `json:"cols"`
		Rows       int             `json:"rows"`
		Reason     string          `json:"reason"`
		// FS stream open params
		Path   string `json:"path"`
		Kind   string `json:"kind"`
		Page   int    `json:"page"`
		DPI    int    `json:"dpi"`
		MaxDim int    `json:"max_dim"`
		Range  string `json:"range"`
	}
	if err := json.Unmarshal(data, &env); err != nil {
		return
	}

	switch env.Type {
	case "settings.updated":
		if env.Settings != nil {
			d.applySettings(*env.Settings)
		}
	case "approval.ack":
		if env.Approval != nil && env.ReqID != "" {
			d.mu.Lock()
			ch := d.ackWaiters[env.ReqID]
			delete(d.ackWaiters, env.ReqID)
			d.mu.Unlock()
			if ch != nil {
				ch <- *env.Approval
			}
		}
	case "approval.decision":
		if env.Approval != nil {
			d.mu.Lock()
			ch := d.decisionWaiters[env.Approval.ID]
			delete(d.decisionWaiters, env.Approval.ID)
			d.mu.Unlock()
			if ch != nil {
				ch <- *env.Approval
			}
		}
	case "approval.expiry_updated":
		if env.Approval != nil {
			d.mu.Lock()
			ch := d.expiryWaiters[env.Approval.ID]
			d.mu.Unlock()
			if ch != nil {
				select {
				case ch <- *env.Approval:
				default:
				}
			}
		}
	case "term.list_req":
		sessions := term.ListTmuxSessions()
		d.sendJSON(map[string]any{
			"type":          "term.list_resp",
			"req_id":        env.ReqID,
			"tmux_sessions": sessions,
		})
	case "term.open":
		go d.handleTermOpen(env.StreamID, env.Mode, env.Target, env.AgyAccount, env.Cwd, env.Cols, env.Rows)
	case "term.resize":
		d.termMgr.Resize(env.StreamID, env.Cols, env.Rows)
	case "term.close":
		d.termMgr.Close(env.StreamID, env.Reason, false)
	case "term.kill":
		if env.Target != "" {
			_ = d.termMgr.KillSession(env.Target)
			d.sendJSON(map[string]any{
				"type":          "mac.heartbeat",
				"hostname":      d.cfg.Hostname,
				"awake":         d.awake.Enabled(),
				"tmux_sessions": term.ListTmuxSessions(),
			})
		}
	case "rpc.req":
		go d.handleRPC(ctx, env.ReqID, env.Op, env.Params)
	case "fs.stream_open":
		go d.handleFSStreamOpen(ctx, env.StreamID, env.Path, env.Kind, env.Page, env.DPI, env.MaxDim, env.Range)
	case "fs.stream_close":
		d.mu.Lock()
		if cancel, ok := d.fsStreams[env.StreamID]; ok {
			cancel()
			delete(d.fsStreams, env.StreamID)
		}
		d.mu.Unlock()
	}
}

func (d *Daemon) handleTermOpen(streamID, mode, target, agyAccount, cwd string, cols, rows int) {
	openReq := term.OpenRequest{
		StreamID: streamID,
		Mode:     mode,
		Target:   target,
		Cwd:      cwd,
		Cols:     cols,
		Rows:     rows,
	}

	var releaseAgyLock func()
	if mode == "new_agy" {
		if !d.termMgr.Enabled() {
			d.sendJSON(map[string]any{
				"type":      "term.error",
				"stream_id": streamID,
				"error":     "远程终端已在设置中关闭",
			})
			return
		}
		plan, unlock, err := d.agyMgr.PrepareRemoteLaunch(agyAccount, cwd, target)
		if err != nil {
			d.sendJSON(map[string]any{
				"type":      "term.error",
				"stream_id": streamID,
				"error":     err.Error(),
			})
			return
		}
		releaseAgyLock = unlock
		openReq.Target = plan.SessionName
		openReq.Cwd = plan.Cwd
		openReq.ShellCommand = plan.ShellCommand
		openReq.NotifyLabel = plan.NotifyLabel
	}

	res, err := d.termMgr.OpenWithOptions(openReq)
	if releaseAgyLock != nil {
		releaseAgyLock()
	}
	if err != nil {
		d.sendJSON(map[string]any{
			"type":      "term.error",
			"stream_id": streamID,
			"error":     err.Error(),
		})
		return
	}

	opened := map[string]any{
		"type":      "term.opened",
		"stream_id": streamID,
		"cols":      res.Cols,
		"rows":      res.Rows,
	}
	if res.Target != "" {
		opened["target"] = res.Target
	}
	d.sendJSON(opened)

	if mode != "tmux_attach" {
		go func() {
			time.Sleep(500 * time.Millisecond)
			d.sendJSON(map[string]any{
				"type":          "mac.heartbeat",
				"hostname":      d.cfg.Hostname,
				"awake":         d.awake.Enabled(),
				"tmux_sessions": term.ListTmuxSessions(),
			})
		}()
	}
}

func (d *Daemon) handleRPC(ctx context.Context, reqID, op string, rawParams json.RawMessage) {
	status, data, errMsg := d.dispatchRPC(ctx, op, rawParams)
	resp := map[string]any{
		"type":   "rpc.resp",
		"req_id": reqID,
		"status": status,
	}
	if data != nil {
		resp["data"] = data
	}
	if errMsg != "" {
		resp["error"] = errMsg
	}
	d.sendJSONBlocking(ctx, resp)
}

func errToRPCStatus(err error) (int, any, string) {
	if err == nil {
		return http.StatusOK, nil, ""
	}
	var ce *fsengine.ConflictError
	if errors.As(err, &ce) {
		return http.StatusConflict, ce, ce.Error()
	}
	if errors.Is(err, fsengine.ErrDisabled) || errors.Is(err, fsengine.ErrForbidden) {
		return http.StatusForbidden, nil, err.Error()
	}
	if os.IsNotExist(err) {
		return http.StatusNotFound, nil, "文件或目录不存在"
	}
	return http.StatusBadRequest, nil, err.Error()
}

func (d *Daemon) dispatchRPC(ctx context.Context, op string, rawParams json.RawMessage) (int, any, string) {
	switch op {
	case "usage.summary":
		var p struct {
			Force bool `json:"force"`
		}
		_ = json.Unmarshal(rawParams, &p)
		sum, err := d.usageMgr.Snapshot(ctx, p.Force)
		if err != nil {
			return http.StatusInternalServerError, nil, err.Error()
		}
		return http.StatusOK, sum, ""

	case "agy.profiles":
		if !d.termMgr.Enabled() {
			return http.StatusForbidden, nil, "远程终端已在设置中关闭"
		}
		home, _ := os.UserHomeDir()
		snap, err := d.agyMgr.SnapshotForRemote(home, d.fsMgr.Roots())
		if err != nil {
			return http.StatusInternalServerError, nil, err.Error()
		}
		return http.StatusOK, snap, ""

	case "fs.roots":
		return http.StatusOK, map[string]any{
			"roots":   d.fsMgr.Roots(),
			"enabled": d.fsMgr.Enabled(),
		}, ""

	case "fs.list":
		var p struct {
			Path   string `json:"path"`
			Hidden bool   `json:"hidden"`
			Sort   string `json:"sort"`
		}
		_ = json.Unmarshal(rawParams, &p)
		res, err := d.fsMgr.List(p.Path, p.Hidden, p.Sort)
		if err != nil {
			return errToRPCStatus(err)
		}
		return http.StatusOK, res, ""

	case "fs.search":
		var p struct {
			Path string `json:"path"`
			Q    string `json:"q"`
			Mode string `json:"mode"`
		}
		_ = json.Unmarshal(rawParams, &p)
		hits, err := d.fsMgr.Search(p.Path, p.Q, p.Mode)
		if err != nil {
			return errToRPCStatus(err)
		}
		return http.StatusOK, map[string]any{"items": hits}, ""

	case "fs.read":
		var p struct {
			Path       string `json:"path"`
			OffsetLine int    `json:"offset_line"`
			LimitLines int    `json:"limit_lines"`
			Tail       bool   `json:"tail"`
		}
		_ = json.Unmarshal(rawParams, &p)
		res, err := d.fsMgr.ReadText(p.Path, p.OffsetLine, p.LimitLines, p.Tail)
		if err != nil {
			return errToRPCStatus(err)
		}
		return http.StatusOK, res, ""

	case "fs.write":
		var p struct {
			Path           string `json:"path"`
			Content        string `json:"content"`
			ExpectedSHA256 string `json:"expected_sha256"`
		}
		_ = json.Unmarshal(rawParams, &p)
		res, err := d.fsMgr.WriteText(p.Path, p.Content, p.ExpectedSHA256)
		if err != nil {
			return errToRPCStatus(err)
		}
		return http.StatusOK, res, ""

	case "fs.mkdir":
		var p struct {
			Path string `json:"path"`
		}
		_ = json.Unmarshal(rawParams, &p)
		created, err := d.fsMgr.Mkdir(p.Path)
		if err != nil {
			return errToRPCStatus(err)
		}
		return http.StatusOK, map[string]any{"ok": true, "path": created}, ""

	case "fs.touch":
		var p struct {
			Path string `json:"path"`
		}
		_ = json.Unmarshal(rawParams, &p)
		created, err := d.fsMgr.Touch(p.Path)
		if err != nil {
			return errToRPCStatus(err)
		}
		return http.StatusOK, map[string]any{"ok": true, "path": created}, ""

	case "fs.rename":
		var p struct {
			Path    string `json:"path"`
			NewName string `json:"new_name"`
		}
		_ = json.Unmarshal(rawParams, &p)
		dst, err := d.fsMgr.Rename(p.Path, p.NewName)
		if err != nil {
			return errToRPCStatus(err)
		}
		return http.StatusOK, map[string]any{"ok": true, "path": dst}, ""

	case "fs.trash":
		var p struct {
			Path string `json:"path"`
		}
		_ = json.Unmarshal(rawParams, &p)
		if err := d.fsMgr.MoveToTrash(p.Path); err != nil {
			return errToRPCStatus(err)
		}
		return http.StatusOK, map[string]any{"ok": true}, ""

	case "fs.pdf.meta":
		var p struct {
			Path string `json:"path"`
		}
		_ = json.Unmarshal(rawParams, &p)
		res, err := d.fsMgr.PDFMeta(p.Path)
		if err != nil {
			return errToRPCStatus(err)
		}
		return http.StatusOK, res, ""

	case "fs.excel.meta":
		var p struct {
			Path string `json:"path"`
		}
		_ = json.Unmarshal(rawParams, &p)
		res, err := d.fsMgr.ExcelMeta(p.Path)
		if err != nil {
			return errToRPCStatus(err)
		}
		return http.StatusOK, res, ""

	case "fs.excel.sheet":
		var p struct {
			Path     string `json:"path"`
			Sheet    string `json:"sheet"`
			RowStart int    `json:"row_start"`
			Limit    int    `json:"limit"`
		}
		_ = json.Unmarshal(rawParams, &p)
		res, err := d.fsMgr.ExcelSheet(p.Path, p.Sheet, p.RowStart, p.Limit)
		if err != nil {
			return errToRPCStatus(err)
		}
		return http.StatusOK, res, ""

	case "fs.excel.cell":
		var p struct {
			Path           string `json:"path"`
			Sheet          string `json:"sheet"`
			Row            int    `json:"row"`
			Col            int    `json:"col"`
			Value          string `json:"value"`
			ExpectedSHA256 string `json:"expected_sha256"`
		}
		_ = json.Unmarshal(rawParams, &p)
		res, err := d.fsMgr.ExcelWriteCell(p.Path, p.Sheet, p.Row, p.Col, p.Value, p.ExpectedSHA256)
		if err != nil {
			return errToRPCStatus(err)
		}
		return http.StatusOK, res, ""

	case "fs.pptx.outline":
		var p struct {
			Path string `json:"path"`
		}
		_ = json.Unmarshal(rawParams, &p)
		res, err := d.fsMgr.PPTXOutline(p.Path)
		if err != nil {
			return errToRPCStatus(err)
		}
		return http.StatusOK, res, ""

	case "git.status":
		var p struct {
			Path string `json:"path"`
		}
		_ = json.Unmarshal(rawParams, &p)
		dir, err := d.fsMgr.ResolvePath(p.Path, false)
		if err != nil {
			return errToRPCStatus(err)
		}
		res, err := gitengine.Status(dir)
		if err != nil {
			return http.StatusBadRequest, nil, err.Error()
		}
		return http.StatusOK, res, ""

	case "git.diff":
		var p struct {
			Path   string `json:"path"`
			File   string `json:"file"`
			Staged bool   `json:"staged"`
			Commit string `json:"commit"`
		}
		_ = json.Unmarshal(rawParams, &p)
		dir, err := d.fsMgr.ResolvePath(p.Path, false)
		if err != nil {
			return errToRPCStatus(err)
		}
		res, err := gitengine.Diff(dir, p.File, p.Staged, p.Commit)
		if err != nil {
			return http.StatusBadRequest, nil, err.Error()
		}
		return http.StatusOK, res, ""

	case "git.commit":
		var p struct {
			Path    string   `json:"path"`
			Files   []string `json:"files"`
			Message string   `json:"message"`
		}
		_ = json.Unmarshal(rawParams, &p)
		dir, err := d.fsMgr.ResolvePath(p.Path, false)
		if err != nil {
			return errToRPCStatus(err)
		}
		res, err := gitengine.Commit(dir, p.Files, p.Message)
		if err != nil {
			return http.StatusBadRequest, nil, err.Error()
		}
		return http.StatusOK, res, ""

	case "git.push":
		var p struct {
			Path string `json:"path"`
		}
		_ = json.Unmarshal(rawParams, &p)
		dir, err := d.fsMgr.ResolvePath(p.Path, false)
		if err != nil {
			return errToRPCStatus(err)
		}
		res, err := gitengine.Push(dir)
		if err != nil {
			return http.StatusBadRequest, nil, err.Error()
		}
		return http.StatusOK, res, ""

	case "git.pull":
		var p struct {
			Path string `json:"path"`
		}
		_ = json.Unmarshal(rawParams, &p)
		dir, err := d.fsMgr.ResolvePath(p.Path, false)
		if err != nil {
			return errToRPCStatus(err)
		}
		out, err := gitengine.Pull(dir)
		if err != nil {
			return http.StatusBadRequest, nil, err.Error()
		}
		return http.StatusOK, map[string]any{"ok": true, "output": out}, ""

	case "git.discard":
		var p struct {
			Path string `json:"path"`
			File string `json:"file"`
		}
		_ = json.Unmarshal(rawParams, &p)
		dir, err := d.fsMgr.ResolvePath(p.Path, false)
		if err != nil {
			return errToRPCStatus(err)
		}
		if err := gitengine.Discard(dir, p.File, fsengine.MoveAbsPathToTrash); err != nil {
			return http.StatusBadRequest, nil, err.Error()
		}
		return http.StatusOK, map[string]any{"ok": true}, ""

	case "git.log":
		var p struct {
			Path  string `json:"path"`
			Limit int    `json:"limit"`
		}
		_ = json.Unmarshal(rawParams, &p)
		dir, err := d.fsMgr.ResolvePath(p.Path, false)
		if err != nil {
			return errToRPCStatus(err)
		}
		items, err := gitengine.Log(dir, p.Limit)
		if err != nil {
			return http.StatusBadRequest, nil, err.Error()
		}
		return http.StatusOK, map[string]any{"items": items}, ""

	default:
		return http.StatusNotFound, nil, "unknown rpc op: " + op
	}
}

func (d *Daemon) handleFSStreamOpen(parentCtx context.Context, streamIDHex, reqPath, kind string, page, dpi, maxDim int, rangeHdr string) {
	sidBytes, err := hex.DecodeString(streamIDHex)
	if err != nil || len(sidBytes) != 16 {
		return
	}
	var sid [16]byte
	copy(sid[:], sidBytes)

	sctx, cancel := context.WithCancel(parentCtx)
	d.mu.Lock()
	d.fsStreams[streamIDHex] = cancel
	d.mu.Unlock()
	defer func() {
		cancel()
		d.mu.Lock()
		delete(d.fsStreams, streamIDHex)
		d.mu.Unlock()
	}()

	rc, meta, err := d.fsMgr.OpenStream(sctx, reqPath, kind, page, dpi, maxDim, rangeHdr)
	if err != nil {
		status, _, msg := errToRPCStatus(err)
		d.sendJSONBlocking(sctx, map[string]any{
			"type":        "fs.stream_meta",
			"stream_id":   streamIDHex,
			"status_code": status,
			"error":       msg,
		})
		return
	}
	defer rc.Close()

	if !d.sendJSONBlocking(sctx, map[string]any{
		"type":           "fs.stream_meta",
		"stream_id":      streamIDHex,
		"status_code":    meta.StatusCode,
		"content_type":   meta.ContentType,
		"content_length": meta.ContentLength,
		"total_size":     meta.TotalSize,
		"content_range":  meta.ContentRange,
		"accept_ranges":  meta.AcceptRanges,
	}) {
		return
	}

	buf := make([]byte, 64*1024)
	for {
		if sctx.Err() != nil {
			return
		}
		n, rerr := rc.Read(buf)
		if n > 0 {
			frame := make([]byte, 17+n)
			frame[0] = binFrameFSChunk
			copy(frame[1:17], sid[:])
			copy(frame[17:], buf[:n])
			if !d.sendWSBlocking(sctx, websocket.BinaryMessage, frame) {
				return
			}
		}
		if rerr != nil {
			if !errors.Is(rerr, io.EOF) {
				d.sendJSONBlocking(sctx, map[string]any{
					"type":      "fs.stream_error",
					"stream_id": streamIDHex,
					"error":     rerr.Error(),
				})
				return
			}
			break
		}
	}

	eofFrame := make([]byte, 17)
	eofFrame[0] = binFrameFSEOF
	copy(eofFrame[1:17], sid[:])
	d.sendWSBlocking(sctx, websocket.BinaryMessage, eofFrame)
}

func (d *Daemon) applySettings(cfg proto.Settings) {
	d.mu.Lock()
	d.settings = cfg
	d.mu.Unlock()
	d.termMgr.SetEnabled(cfg.TerminalEnabled)
	d.fsMgr.SetEnabled(cfg.FilesEnabled)
	d.awake.Set(cfg.MacAwakeEnabled)
}

func (d *Daemon) getSettings() proto.Settings {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.settings
}

func (d *Daemon) serveLocalIPC(ctx context.Context) error {
	sock := d.cfg.SocketPath
	if err := os.MkdirAll(filepath.Dir(sock), 0o700); err != nil {
		return err
	}
	_ = os.Remove(sock)
	ln, err := net.Listen("unix", sock)
	if err != nil {
		return err
	}
	_ = os.Chmod(sock, 0o600)

	mux := http.NewServeMux()
	mux.HandleFunc("GET /local/status", func(w http.ResponseWriter, r *http.Request) {
		d.mu.Lock()
		connected := d.wsConn != nil
		st := d.settings
		d.mu.Unlock()
		_ = json.NewEncoder(w).Encode(map[string]any{
			"tunnel_connected": connected,
			"awake":            d.awake.Enabled(),
			"idle":             idle.Detect(),
			"settings":         st,
			"file_roots":       d.fsMgr.Roots(),
			"tmux_sessions":    term.ListTmuxSessions(),
			"web_url":          d.cfg.WebURL(),
		})
	})

	mux.HandleFunc("GET /local/usage", func(w http.ResponseWriter, r *http.Request) {
		force := r.URL.Query().Get("force") == "1"
		sum, err := d.usageMgr.Snapshot(r.Context(), force)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeLocalJSON(w, sum)
	})

	mux.HandleFunc("POST /local/message", func(w http.ResponseWriter, r *http.Request) {
		var req proto.HookMessageReq
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		ok := d.sendJSON(map[string]any{
			"type":    "message.create",
			"message": req.Message,
		})
		if !ok {
			http.Error(w, "tunnel offline", http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	})

	mux.HandleFunc("POST /local/approval", func(w http.ResponseWriter, r *http.Request) {
		var req proto.HookApprovalReq
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		sess := strings.TrimSpace(req.Approval.TmuxSession)
		if sess != "" {
			d.mu.Lock()
			d.activeHookSessions[sess]++
			d.mu.Unlock()
		}
		res, err := d.handleApprovalPolicy(r.Context(), req.Approval, req.TimeoutSec)
		if sess != "" {
			d.mu.Lock()
			if d.activeHookSessions[sess] <= 1 {
				delete(d.activeHookSessions, sess)
			} else {
				d.activeHookSessions[sess]--
			}
			if err == nil && res.Status == "fallback" {
				d.recentHookFallbacks[sess] = time.Now()
			}
			d.mu.Unlock()
		}
		if err != nil {
			if errors.Is(err, errTunnelUnavailable) {
				// Return 503 so ai-audit-hook falls back to direct HTTPS Relay approval instead of silently dropping!
				http.Error(w, err.Error(), http.StatusServiceUnavailable)
				return
			}
			writeLocalJSON(w, proto.Approval{Status: "fallback", DecisionReason: err.Error()})
			return
		}
		writeLocalJSON(w, res)
	})

	srv := &http.Server{Handler: mux}
	go func() {
		<-ctx.Done()
		_ = srv.Close()
		_ = os.Remove(sock)
	}()
	log.Printf("local IPC listening on unix://%s", sock)
	if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) && !errors.Is(err, net.ErrClosed) {
		return err
	}
	return nil
}

func (d *Daemon) forwardAttentionFallback(a proto.Approval, note string) {
	_ = d.sendJSON(map[string]any{
		"type": "message.create",
		"message": proto.Message{
			Agent:       a.Agent,
			EventType:   "attention",
			Priority:    "default",
			Title:       a.Agent + " 本地待授权 (" + note + ")",
			Body:        a.Summary,
			TaskTitle:   a.TaskTitle,
			ProjectName: a.ProjectName,
			TmuxSession: a.TmuxSession,
		},
	})
}

func (d *Daemon) handleApprovalPolicy(ctx context.Context, a proto.Approval, timeoutSec int) (proto.Approval, error) {
	st := d.getSettings()
	log.Printf("approval request: agent=%s tool=%s risk=%s mode=%s summary=%q",
		a.Agent, a.ToolName, a.RiskLevel, st.ApprovalMode, a.Summary)
	if st.ApprovalMode == "notify" {
		d.forwardAttentionFallback(a, "仅通知模式")
		return proto.Approval{Status: "fallback", DecisionReason: "仅通知模式"}, nil
	}
	if st.ApprovalMode == "auto" {
		idleSt := idle.Detect()
		if !idleSt.ShouldUsePhone(st.IdleThresholdSec) {
			d.forwardAttentionFallback(a, "人在电脑前操作，直接走本地确认")
			return proto.Approval{Status: "fallback", DecisionReason: "人在电脑前"}, nil
		}
	}
	allowMacPrompt := st.ApprovalMode == "both"
	return d.requestRemoteApproval(ctx, a, timeoutSec, allowMacPrompt)
}

func (d *Daemon) waitForTunnel(ctx context.Context, maxWait time.Duration) bool {
	deadline := time.Now().Add(maxWait)
	for {
		d.mu.Lock()
		connected := d.wsConn != nil
		d.mu.Unlock()
		if connected {
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
		select {
		case <-ctx.Done():
			return false
		case <-time.After(150 * time.Millisecond):
		}
	}
}

func (d *Daemon) requestRemoteApproval(ctx context.Context, a proto.Approval, timeoutSec int, allowMacPrompt bool) (proto.Approval, error) {
	if !d.waitForTunnel(ctx, 3*time.Second) {
		return proto.Approval{}, errTunnelUnavailable
	}
	st := d.getSettings()
	reqTimeoutSec := timeoutSec // 0 lets Relay apply its authoritative default_timeout_sec setting.

	reqID := newReqID()
	ackCh := make(chan proto.Approval, 1)
	d.mu.Lock()
	d.ackWaiters[reqID] = ackCh
	d.mu.Unlock()

	if !d.sendJSON(map[string]any{
		"type":        "approval.create",
		"req_id":      reqID,
		"approval":    a,
		"timeout_sec": reqTimeoutSec,
	}) {
		d.mu.Lock()
		delete(d.ackWaiters, reqID)
		d.mu.Unlock()
		return proto.Approval{}, errTunnelUnavailable
	}

	var created proto.Approval
	select {
	case c, ok := <-ackCh:
		if !ok {
			return proto.Approval{}, errTunnelUnavailable
		}
		created = c
	case <-time.After(6 * time.Second):
		d.mu.Lock()
		delete(d.ackWaiters, reqID)
		d.mu.Unlock()
		return proto.Approval{}, errTunnelUnavailable
	case <-ctx.Done():
		return proto.Approval{}, ctx.Err()
	}

	if created.Status != "pending" {
		return created, nil
	}

	effectiveTimeoutSec := reqTimeoutSec
	if created.ExpiresAt > created.CreatedAt {
		effectiveTimeoutSec = int((created.ExpiresAt - created.CreatedAt + 999) / 1000)
	}
	if effectiveTimeoutSec <= 0 {
		effectiveTimeoutSec = st.DefaultTimeoutSec
	}
	if effectiveTimeoutSec <= 0 {
		effectiveTimeoutSec = 30
	}

	decCh := make(chan proto.Approval, 1)
	expCh := make(chan proto.Approval, 4)
	d.mu.Lock()
	d.decisionWaiters[created.ID] = decCh
	d.expiryWaiters[created.ID] = expCh
	d.mu.Unlock()
	defer func() {
		d.mu.Lock()
		delete(d.decisionWaiters, created.ID)
		delete(d.expiryWaiters, created.ID)
		d.mu.Unlock()
	}()

	type promptOutcome struct {
		res prompt.Result
		err error
	}
	type localHoldEvent struct {
		paused       bool
		remainingSec int
	}
	var macPromptCh chan promptOutcome
	localHoldCh := make(chan localHoldEvent, 8)
	remoteHoldCh := make(chan bool, 4)
	cancelPrompt := func() {}
	if allowMacPrompt && !idle.Detect().Locked {
		hudSec := effectiveTimeoutSec
		if created.ExpiresAt > 0 {
			remMs := created.ExpiresAt - time.Now().UnixMilli()
			if remMs > 0 {
				hudSec = int((remMs + 999) / 1000)
			} else {
				hudSec = 1
			}
		}
		var pCtx context.Context
		pCtx, cancelPrompt = context.WithCancel(ctx)
		macPromptCh = make(chan promptOutcome, 1)
		timeoutAction := st.TimeoutAction
		go func() {
			r, err := prompt.ShowWithControl(pCtx, a, hudSec, timeoutAction, prompt.Control{
				OnHold: func(paused bool, remainingSec int) {
					select {
					case localHoldCh <- localHoldEvent{paused: paused, remainingSec: remainingSec}:
					default:
					}
				},
				RemoteHoldCh: remoteHoldCh,
			})
			macPromptCh <- promptOutcome{res: r, err: err}
		}()
	}
	defer cancelPrompt()

	timer := time.NewTimer(time.Duration(effectiveTimeoutSec+5) * time.Second)
	defer timer.Stop()
	timerCh := timer.C

	stopTimer := func() {
		if !timer.Stop() {
			select {
			case <-timer.C:
			default:
			}
		}
		timerCh = nil
	}
	resetTimer := func(dur time.Duration) {
		if dur < 6*time.Second {
			dur = 6 * time.Second
		}
		stopTimer()
		timer.Reset(dur)
		timerCh = timer.C
	}

	for {
		select {
		case dec := <-decCh:
			cancelPrompt()
			return dec, nil
		case ev := <-localHoldCh:
			if ev.paused {
				stopTimer()
			} else {
				remSec := ev.remainingSec
				if remSec <= 0 {
					remSec = effectiveTimeoutSec
				}
				resetTimer(time.Duration(remSec+5) * time.Second)
			}
			d.sendJSON(map[string]any{
				"type":        "approval.hold",
				"approval_id": created.ID,
				"paused":      ev.paused,
				"resume_sec":  ev.remainingSec,
			})
		case exp := <-expCh:
			if exp.ExpiresAt == 0 {
				stopTimer()
				select {
				case remoteHoldCh <- true:
				default:
				}
			} else {
				rem := time.Until(time.UnixMilli(exp.ExpiresAt)) + 5*time.Second
				resetTimer(rem)
				select {
				case remoteHoldCh <- false:
				default:
				}
			}
		case macRes := <-macPromptCh:
			if macRes.err != nil {
				macPromptCh = nil
				continue
			}
			decidedBy := macRes.res.DecidedBy
			if decidedBy == "" {
				decidedBy = "mac_local"
			}
			d.sendJSON(map[string]any{
				"type":        "approval.decide",
				"approval_id": created.ID,
				"decision":    macRes.res.Decision,
				"reason":      macRes.res.Reason,
				"decided_by":  decidedBy,
			})
			return proto.Approval{
				ID:             created.ID,
				Status:         macRes.res.Decision,
				DecisionReason: macRes.res.Reason,
				DecidedBy:      decidedBy,
			}, nil
		case <-ctx.Done():
			cancelPrompt()
			d.sendJSON(map[string]any{
				"type":        "approval.cancel",
				"approval_id": created.ID,
			})
			return proto.Approval{ID: created.ID, Status: "canceled"}, ctx.Err()
		case <-timerCh:
			cancelPrompt()
			curSt := d.getSettings()
			switch curSt.TimeoutAction {
			case "approve":
				d.sendJSON(map[string]any{
					"type":        "approval.decide",
					"approval_id": created.ID,
					"decision":    "approved",
					"reason":      "审批超时，系统自动确认允许",
					"decided_by":  "system_timeout",
				})
				return proto.Approval{ID: created.ID, Status: "approved", DecisionReason: "审批超时，系统自动确认允许", DecidedBy: "system_timeout"}, nil
			case "deny":
				return proto.Approval{ID: created.ID, Status: "denied", DecisionReason: "审批超时，系统自动拒绝", DecidedBy: "system_timeout"}, nil
			default:
				return proto.Approval{ID: created.ID, Status: "fallback", DecisionReason: "本地等待超时", DecidedBy: "system_timeout"}, nil
			}
		}
	}
}

func writeLocalJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func (d *Daemon) serveWebDashboard(ctx context.Context) {
	if !d.cfg.WebEnabled() {
		return
	}
	ln, err := net.Listen("tcp", d.cfg.WebAddr)
	if err != nil {
		log.Printf("usage web dashboard listen %s failed: %v", d.cfg.WebAddr, err)
		return
	}
	srv := &http.Server{
		Handler:           d.usageMgr.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}
	go func() {
		<-ctx.Done()
		_ = srv.Close()
	}()
	log.Printf("usage web dashboard listening on %s", d.cfg.WebURL())
	if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) && !errors.Is(err, net.ErrClosed) {
		log.Printf("usage web dashboard stopped: %v", err)
	}
}

func main() {
	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("load config: %v", err)
	}
	d := newDaemon(cfg)
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	d.usageMgr.StartBackground(ctx)
	go d.serveWebDashboard(ctx)
	go d.runTunnel(ctx)
	go d.agyWatcher.Run(ctx)
	if err := d.serveLocalIPC(ctx); err != nil {
		log.Fatalf("local IPC error: %v", err)
	}
}
