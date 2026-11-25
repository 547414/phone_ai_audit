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
	"sync"
	"syscall"
	"time"

	"github.com/gorilla/websocket"

	"phone-ai-audit/mac-agent/internal/awake"
	"phone-ai-audit/mac-agent/internal/config"
	"phone-ai-audit/mac-agent/internal/fsengine"
	"phone-ai-audit/mac-agent/internal/gitengine"
	"phone-ai-audit/mac-agent/internal/idle"
	"phone-ai-audit/mac-agent/internal/proto"
	"phone-ai-audit/mac-agent/internal/term"
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

type Daemon struct {
	cfg     config.Config
	awake   *awake.Controller
	termMgr *term.Manager
	fsMgr   *fsengine.Engine

	mu       sync.Mutex
	wsConn   *websocket.Conn
	wsSend   chan wsOut
	wsDone   chan struct{}
	settings proto.Settings

	ackWaiters      map[string]chan proto.Approval
	decisionWaiters map[string]chan proto.Approval
	fsStreams       map[string]context.CancelFunc
}

func newDaemon(cfg config.Config) *Daemon {
	d := &Daemon{
		cfg:   cfg,
		awake: awake.New(),
		fsMgr: fsengine.New(cfg.FileRoots),
		settings: proto.Settings{
			ApprovalMode:       "auto",
			IdleThresholdSec:   60,
			DefaultTimeoutSec:  180,
			TimeoutAction:      "fallback",
			TerminalEnabled:    true,
			FilesEnabled:       true,
			MacAwakeEnabled:    false,
			HighRiskRequireApp: false,
		},
		ackWaiters:      make(map[string]chan proto.Approval),
		decisionWaiters: make(map[string]chan proto.Approval),
		fsStreams:       make(map[string]context.CancelFunc),
	}
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
		StreamID string          `json:"stream_id"`
		Mode     string          `json:"mode"`
		Target   string          `json:"target"`
		Cols     int             `json:"cols"`
		Rows     int             `json:"rows"`
		Reason   string          `json:"reason"`
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
	case "term.list_req":
		sessions := term.ListTmuxSessions()
		d.sendJSON(map[string]any{
			"type":          "term.list_resp",
			"req_id":        env.ReqID,
			"tmux_sessions": sessions,
		})
	case "term.open":
		if actualCols, actualRows, err := d.termMgr.Open(env.StreamID, env.Mode, env.Target, env.Cols, env.Rows); err != nil {
			d.sendJSON(map[string]any{
				"type":      "term.error",
				"stream_id": env.StreamID,
				"error":     err.Error(),
			})
		} else {
			d.sendJSON(map[string]any{
				"type":      "term.opened",
				"stream_id": env.StreamID,
				"cols":      actualCols,
				"rows":      actualRows,
			})
		}
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

func (d *Daemon) handleRPC(ctx context.Context, reqID, op string, rawParams json.RawMessage) {
	status, data, errMsg := d.dispatchRPC(op, rawParams)
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

func (d *Daemon) dispatchRPC(op string, rawParams json.RawMessage) (int, any, string) {
	switch op {
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
		})
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
		st := d.getSettings()
		if st.ApprovalMode == "notify" {
			d.forwardAttentionFallback(req.Approval, "仅通知模式")
			writeLocalJSON(w, proto.Approval{Status: "fallback", DecisionReason: "仅通知模式"})
			return
		}
		if st.ApprovalMode == "auto" {
			idleSt := idle.Detect()
			if !idleSt.ShouldUsePhone(st.IdleThresholdSec) {
				d.forwardAttentionFallback(req.Approval, "人在电脑前操作，直接走本地确认")
				writeLocalJSON(w, proto.Approval{Status: "fallback", DecisionReason: "人在电脑前"})
				return
			}
		}

		res, err := d.requestRemoteApproval(r.Context(), req.Approval, req.TimeoutSec)
		if err != nil {
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

func (d *Daemon) requestRemoteApproval(ctx context.Context, a proto.Approval, timeoutSec int) (proto.Approval, error) {
	st := d.getSettings()
	if timeoutSec <= 0 {
		timeoutSec = st.DefaultTimeoutSec
	}
	if timeoutSec <= 0 {
		timeoutSec = 180
	}

	reqID := newReqID()
	ackCh := make(chan proto.Approval, 1)
	d.mu.Lock()
	d.ackWaiters[reqID] = ackCh
	d.mu.Unlock()

	if !d.sendJSON(map[string]any{
		"type":        "approval.create",
		"req_id":      reqID,
		"approval":    a,
		"timeout_sec": timeoutSec,
	}) {
		d.mu.Lock()
		delete(d.ackWaiters, reqID)
		d.mu.Unlock()
		return proto.Approval{}, errors.New("tunnel offline")
	}

	var created proto.Approval
	select {
	case c, ok := <-ackCh:
		if !ok {
			return proto.Approval{}, errors.New("tunnel disconnected")
		}
		created = c
	case <-time.After(6 * time.Second):
		d.mu.Lock()
		delete(d.ackWaiters, reqID)
		d.mu.Unlock()
		return proto.Approval{}, errors.New("relay ack timeout")
	case <-ctx.Done():
		return proto.Approval{}, ctx.Err()
	}

	if created.Status != "pending" {
		return created, nil
	}

	decCh := make(chan proto.Approval, 1)
	d.mu.Lock()
	d.decisionWaiters[created.ID] = decCh
	d.mu.Unlock()
	defer func() {
		d.mu.Lock()
		delete(d.decisionWaiters, created.ID)
		d.mu.Unlock()
	}()

	timer := time.NewTimer(time.Duration(timeoutSec+5) * time.Second)
	defer timer.Stop()

	select {
	case dec := <-decCh:
		return dec, nil
	case <-ctx.Done():
		d.sendJSON(map[string]any{
			"type":        "approval.cancel",
			"approval_id": created.ID,
		})
		return proto.Approval{ID: created.ID, Status: "canceled"}, ctx.Err()
	case <-timer.C:
		return proto.Approval{ID: created.ID, Status: "fallback", DecisionReason: "本地等待超时"}, nil
	}
}

func writeLocalJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func main() {
	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("load config: %v", err)
	}
	d := newDaemon(cfg)
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	go d.runTunnel(ctx)
	if err := d.serveLocalIPC(ctx); err != nil {
		log.Fatalf("local IPC error: %v", err)
	}
}
