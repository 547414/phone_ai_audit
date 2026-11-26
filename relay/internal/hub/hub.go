package hub

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"

	"phone-ai-audit/relay/internal/model"
	"phone-ai-audit/relay/internal/store"
)

const (
	BinFrameStdin   byte = 0x01
	BinFrameStdout  byte = 0x02
	BinFrameFSChunk byte = 0x03
	BinFrameFSEOF   byte = 0x04

	wsWriteTimeout  = 20 * time.Second
	wsPingInterval  = 25 * time.Second
	wsPongWait      = 65 * time.Second
	termIdleTimeout = 15 * time.Minute
	ticketTTL       = 45 * time.Second
)

type RPCResult struct {
	Status int             `json:"status"`
	Data   json.RawMessage `json:"data,omitempty"`
	Error  string          `json:"error,omitempty"`
}

type FSStreamMeta struct {
	StatusCode    int    `json:"status_code"`
	ContentType   string `json:"content_type"`
	ContentLength int64  `json:"content_length"`
	TotalSize     int64  `json:"total_size"`
	ContentRange  string `json:"content_range,omitempty"`
	AcceptRanges  string `json:"accept_ranges,omitempty"`
	Error         string `json:"error,omitempty"`
}

type FSChunk struct {
	Data []byte
	EOF  bool
	Err  error
}

type FSStream struct {
	id     [16]byte
	idHex  string
	metaCh chan FSStreamMeta
	chunks chan FSChunk
	done   chan struct{}
	once   sync.Once
	hub    *Hub
}

func (s *FSStream) Chunks() <-chan FSChunk { return s.chunks }
func (s *FSStream) Done() <-chan struct{}  { return s.done }

func (s *FSStream) enqueueChunk(c FSChunk) bool {
	select {
	case <-s.done:
		return false
	case s.chunks <- c:
		return true
	case <-time.After(15 * time.Second):
		return false
	}
}

func (s *FSStream) Close(notifyMac bool) {
	s.once.Do(func() {
		close(s.done)
		s.hub.mu.Lock()
		delete(s.hub.fsStreams, s.id)
		mac := s.hub.mac
		s.hub.mu.Unlock()
		if notifyMac && mac != nil {
			mac.enqueueJSON(map[string]any{
				"type":      "fs.stream_close",
				"stream_id": s.idHex,
			})
		}
	})
}

type wsOutFrame struct {
	msgType int
	data    []byte
}

type wsClient struct {
	id   string
	conn *websocket.Conn
	send chan wsOutFrame
	once sync.Once
	done chan struct{}
}

func newWSClient(id string, conn *websocket.Conn, buf int) *wsClient {
	return &wsClient{
		id:   id,
		conn: conn,
		send: make(chan wsOutFrame, buf),
		done: make(chan struct{}),
	}
}

func (c *wsClient) enqueue(msgType int, data []byte) bool {
	select {
	case <-c.done:
		return false
	default:
	}
	select {
	case c.send <- wsOutFrame{msgType: msgType, data: data}:
		return true
	case <-c.done:
		return false
	default:
		return false
	}
}

func (c *wsClient) enqueueJSON(v any) bool {
	b, err := json.Marshal(v)
	if err != nil {
		return false
	}
	return c.enqueue(websocket.TextMessage, b)
}

func (c *wsClient) close() {
	c.once.Do(func() {
		close(c.done)
		_ = c.conn.Close()
	})
}

func (c *wsClient) writeLoop() {
	ticker := time.NewTicker(wsPingInterval)
	defer func() {
		ticker.Stop()
		c.close()
	}()
	for {
		select {
		case <-c.done:
			return
		case f := <-c.send:
			_ = c.conn.SetWriteDeadline(time.Now().Add(wsWriteTimeout))
			if err := c.conn.WriteMessage(f.msgType, f.data); err != nil {
				return
			}
		case <-ticker.C:
			_ = c.conn.SetWriteDeadline(time.Now().Add(wsWriteTimeout))
			if err := c.conn.WriteMessage(websocket.PingMessage, nil); err != nil {
				return
			}
		}
	}
}

type termTicket struct {
	Token     string
	ExpiresAt time.Time
	ClientIP  string
}

type termStream struct {
	id          [16]byte
	idHex       string
	app         *wsClient
	mode        string
	target      string
	clientIP    string
	lastActive  time.Time
	mu          sync.Mutex
	closed      bool
	auditOpened bool
}

type Hub struct {
	store *store.Store

	mu           sync.RWMutex
	mac          *wsClient
	macHostname  string
	macLastSeen  int64
	tmuxSessions []model.TmuxSession

	apps map[string]*wsClient

	waiters         map[string][]chan model.Approval
	termListWaiters map[string]chan []model.TmuxSession
	rpcWaiters      map[string]chan RPCResult
	fsStreams       map[[16]byte]*FSStream
	tickets         map[string]termTicket
	streams         map[[16]byte]*termStream
}

func New(st *store.Store) *Hub {
	return &Hub{
		store:           st,
		apps:            make(map[string]*wsClient),
		waiters:         make(map[string][]chan model.Approval),
		termListWaiters: make(map[string]chan []model.TmuxSession),
		rpcWaiters:      make(map[string]chan RPCResult),
		fsStreams:       make(map[[16]byte]*FSStream),
		tickets:         make(map[string]termTicket),
		streams:         make(map[[16]byte]*termStream),
	}
}

func NewID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

func newStreamID() ([16]byte, string) {
	var b [16]byte
	_, _ = rand.Read(b[:])
	return b, hex.EncodeToString(b[:])
}

func (h *Hub) StartBackground(ctx context.Context) {
	go func() {
		sweepTicker := time.NewTicker(1 * time.Second)
		pruneTicker := time.NewTicker(6 * time.Hour)
		defer sweepTicker.Stop()
		defer pruneTicker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-sweepTicker.C:
				h.sweepExpiredApprovals(ctx)
				h.sweepIdleTerminalsAndTickets(ctx)
			case <-pruneTicker.C:
				h.store.PruneOldRecords(ctx, 30)
			}
		}
	}()
}

func (h *Hub) sweepExpiredApprovals(ctx context.Context) {
	cfg, err := h.store.GetSettings(ctx)
	if err != nil {
		return
	}
	target := model.StatusFallback
	switch cfg.TimeoutAction {
	case model.TimeoutApprove:
		target = model.StatusApproved
	case model.TimeoutDeny:
		target = model.StatusExpired
	}
	resolved, err := h.store.ExpirePendingApprovals(ctx, time.Now().UnixMilli(), target)
	if err != nil || len(resolved) == 0 {
		return
	}
	for _, a := range resolved {
		h.onApprovalResolved(ctx, a)
	}
}

func (h *Hub) sweepIdleTerminalsAndTickets(ctx context.Context) {
	now := time.Now()
	var idle []*termStream
	h.mu.Lock()
	for k, t := range h.tickets {
		if now.After(t.ExpiresAt) {
			delete(h.tickets, k)
		}
	}
	for _, st := range h.streams {
		st.mu.Lock()
		if !st.closed && now.Sub(st.lastActive) > termIdleTimeout {
			idle = append(idle, st)
		}
		st.mu.Unlock()
	}
	h.mu.Unlock()

	for _, st := range idle {
		h.closeTerminalStream(ctx, st.id, "idle_timeout", true)
	}
}

func (h *Hub) attachNotes(ctx context.Context, sessions []model.TmuxSession) []model.TmuxSession {
	if len(sessions) == 0 {
		return sessions
	}
	notes, err := h.store.GetTerminalNotes(ctx)
	if err != nil || len(notes) == 0 {
		return sessions
	}
	out := make([]model.TmuxSession, len(sessions))
	copy(out, sessions)
	for i := range out {
		if n, ok := notes[out[i].Name]; ok {
			out[i].Note = n
		}
	}
	return out
}

func (h *Hub) Status(ctx context.Context) model.StatusSnapshot {
	cfg, _ := h.store.GetSettings(ctx)
	pending, unread, _ := h.store.Counts(ctx)

	h.mu.RLock()
	macOnline := h.mac != nil
	macHost := h.macHostname
	macSeen := h.macLastSeen
	appCount := len(h.apps)
	sessions := append([]model.TmuxSession(nil), h.tmuxSessions...)
	h.mu.RUnlock()

	return model.StatusSnapshot{
		MacOnline:      macOnline,
		MacHostname:    macHost,
		MacLastSeenAt:  macSeen,
		AppOnlineCount: appCount,
		PendingCount:   pending,
		UnreadCount:    unread,
		Settings:       cfg,
		TmuxSessions:   h.attachNotes(ctx, sessions),
	}
}

func (h *Hub) AppOnlineCount() int {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return len(h.apps)
}

func (h *Hub) MacOnline() bool {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.mac != nil
}

func (h *Hub) CreateApproval(ctx context.Context, req model.Approval, timeoutSec int) (model.Approval, error) {
	cfg, err := h.store.GetSettings(ctx)
	if err != nil {
		return model.Approval{}, err
	}
	if req.ID == "" {
		req.ID = NewID()
	}
	now := time.Now().UnixMilli()
	req.CreatedAt = now
	if timeoutSec <= 0 {
		timeoutSec = cfg.DefaultTimeoutSec
	}
	if timeoutSec > 600 {
		timeoutSec = 600
	}
	req.ExpiresAt = now + int64(timeoutSec)*1000
	if req.RiskLevel == "" {
		req.RiskLevel = "low"
	}

	// Immediate fallback if mode=notify, or if no Android client is connected outside `both` mode.
	// In `both` mode, keep the approval pending so the Mac floating HUD still appears immediately,
	// Android receives it upon brief WS reconnect, and timeout auto-approve remains active.
	if cfg.ApprovalMode == model.ModeNotify || (h.AppOnlineCount() == 0 && cfg.ApprovalMode != model.ModeBoth) {
		req.Status = model.StatusFallback
		req.DecidedAt = now
		if cfg.ApprovalMode == model.ModeNotify {
			req.DecisionReason = "当前为仅通知模式，已回退本地确认"
			req.DecidedBy = "mode_notify"
		} else {
			req.DecisionReason = "手机端未在线，已自动回退本地确认"
			req.DecidedBy = "app_offline"
		}
		if err := h.store.CreateApproval(ctx, req); err != nil {
			return model.Approval{}, err
		}
		_, _ = h.CreateMessage(ctx, model.Message{
			Agent:       req.Agent,
			EventType:   "attention",
			Priority:    "high",
			Title:       fmt.Sprintf("%s 请求授权（本地处理）", agentLabel(req.Agent)),
			Body:        req.Summary,
			TaskTitle:   req.TaskTitle,
			ProjectName: req.ProjectName,
			TmuxSession: req.TmuxSession,
			ApprovalID:  req.ID,
		})
		return req, nil
	}

	req.Status = model.StatusPending
	if err := h.store.CreateApproval(ctx, req); err != nil {
		return model.Approval{}, err
	}
	h.broadcastApps(map[string]any{
		"type":     "approval.created",
		"approval": req,
	})
	return req, nil
}

func (h *Hub) WaitApproval(ctx context.Context, id string, timeout time.Duration) (model.Approval, error) {
	a, err := h.store.GetApproval(ctx, id)
	if err != nil {
		return a, err
	}
	if a.Status != model.StatusPending {
		return a, nil
	}

	ch := make(chan model.Approval, 1)
	h.mu.Lock()
	h.waiters[id] = append(h.waiters[id], ch)
	h.mu.Unlock()

	defer func() {
		h.mu.Lock()
		list := h.waiters[id]
		for i, c := range list {
			if c == ch {
				h.waiters[id] = append(list[:i], list[i+1:]...)
				break
			}
		}
		if len(h.waiters[id]) == 0 {
			delete(h.waiters, id)
		}
		h.mu.Unlock()
	}()

	// Re-check after subscribing to close race window.
	a, err = h.store.GetApproval(ctx, id)
	if err == nil && a.Status != model.StatusPending {
		return a, nil
	}

	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case res := <-ch:
		return res, nil
	case <-ctx.Done():
		return model.Approval{}, ctx.Err()
	case <-timer.C:
		return h.store.GetApproval(ctx, id)
	}
}

func (h *Hub) DecideApproval(ctx context.Context, id, decision, reason, scope, decidedBy string) (model.Approval, error) {
	switch decision {
	case model.StatusApproved, model.StatusDenied, model.StatusFallback, model.StatusCanceled:
	default:
		return model.Approval{}, fmt.Errorf("invalid decision: %s", decision)
	}
	a, err := h.store.ResolveApproval(ctx, id, decision, reason, scope, decidedBy)
	if err != nil {
		return a, err
	}
	h.onApprovalResolved(ctx, a)
	return a, nil
}

func (h *Hub) onApprovalResolved(ctx context.Context, a model.Approval) {
	// Wake HTTP waiters
	h.mu.Lock()
	chans := h.waiters[a.ID]
	delete(h.waiters, a.ID)
	mac := h.mac
	h.mu.Unlock()

	for _, ch := range chans {
		select {
		case ch <- a:
		default:
		}
	}

	// Push to Mac over reverse WS if connected
	if mac != nil {
		mac.enqueueJSON(map[string]any{
			"type":     "approval.decision",
			"approval": a,
		})
	}

	// Notify Android apps so pending cards and notifications clear immediately
	h.broadcastApps(map[string]any{
		"type":     "approval.updated",
		"approval": a,
	})

	// Mark any existing unread messages linked to this approval as read.
	_ = h.store.MarkMessagesReadByApprovalID(ctx, a.ID)

	// Record an audit timeline entry in messages (marked read so resolved/timed-out approvals don't leave unread badges)
	statusLabel := map[string]string{
		model.StatusApproved: "已允许",
		model.StatusDenied:   "已拒绝",
		model.StatusFallback: "已退回电脑",
		model.StatusExpired:  "超时自动拒绝",
		model.StatusCanceled: "Mac 端已取消",
	}[a.Status]
	sourceTag := ""
	if a.Status == model.StatusApproved && a.DecidedBy == "system_timeout" {
		statusLabel = "超时自动允许"
	} else if a.DecidedBy == "mac_local" {
		sourceTag = "（电脑端）"
	}
	body := a.Summary
	if a.DecisionReason != "" {
		body = fmt.Sprintf("%s\n备注：%s", body, a.DecisionReason)
	}
	nowMs := time.Now().UnixMilli()
	_, _ = h.CreateMessage(ctx, model.Message{
		Agent:       a.Agent,
		EventType:   "approval_result",
		Priority:    "default",
		Title:       fmt.Sprintf("%s 审批%s%s", agentLabel(a.Agent), statusLabel, sourceTag),
		Body:        body,
		TaskTitle:   a.TaskTitle,
		ProjectName: a.ProjectName,
		TmuxSession: a.TmuxSession,
		ApprovalID:  a.ID,
		CreatedAt:   nowMs,
		ReadAt:      nowMs,
	})
}

func (h *Hub) CreateMessage(ctx context.Context, m model.Message) (model.Message, error) {
	if m.ID == "" {
		m.ID = NewID()
	}
	if m.CreatedAt == 0 {
		m.CreatedAt = time.Now().UnixMilli()
	}
	if m.Priority == "" {
		m.Priority = "default"
	}
	if err := h.store.CreateMessage(ctx, m); err != nil {
		return m, err
	}
	h.broadcastApps(map[string]any{
		"type":    "message.created",
		"message": m,
	})
	return m, nil
}

func (h *Hub) UpdateSettings(ctx context.Context, cfg model.Settings) (model.Settings, error) {
	saved, err := h.store.SaveSettings(ctx, cfg)
	if err != nil {
		return saved, err
	}
	frame := map[string]any{
		"type":     "settings.updated",
		"settings": saved,
	}
	h.broadcastApps(frame)
	h.mu.RLock()
	mac := h.mac
	h.mu.RUnlock()
	if mac != nil {
		mac.enqueueJSON(frame)
	}
	return saved, nil
}

func (h *Hub) IssueTerminalTicket(ctx context.Context, clientIP string) (string, error) {
	cfg, err := h.store.GetSettings(ctx)
	if err != nil {
		return "", err
	}
	if !cfg.TerminalEnabled {
		return "", errors.New("terminal is disabled in settings")
	}
	if !h.MacOnline() {
		return "", errors.New("mac agent is offline")
	}
	tok := NewID()
	h.mu.Lock()
	h.tickets[tok] = termTicket{
		Token:     tok,
		ExpiresAt: time.Now().Add(ticketTTL),
		ClientIP:  clientIP,
	}
	h.mu.Unlock()
	return tok, nil
}

func (h *Hub) ConsumeTerminalTicket(tok string) (termTicket, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	t, ok := h.tickets[tok]
	if !ok {
		return termTicket{}, false
	}
	delete(h.tickets, tok)
	if time.Now().After(t.ExpiresAt) {
		return termTicket{}, false
	}
	return t, true
}

func (h *Hub) ListTmuxSessions(ctx context.Context) ([]model.TmuxSession, bool) {
	h.mu.RLock()
	mac := h.mac
	cached := append([]model.TmuxSession(nil), h.tmuxSessions...)
	h.mu.RUnlock()
	if mac == nil {
		return nil, false
	}

	reqID := NewID()
	ch := make(chan []model.TmuxSession, 1)
	h.mu.Lock()
	h.termListWaiters[reqID] = ch
	h.mu.Unlock()
	defer func() {
		h.mu.Lock()
		delete(h.termListWaiters, reqID)
		h.mu.Unlock()
	}()

	mac.enqueueJSON(map[string]any{
		"type":   "term.list_req",
		"req_id": reqID,
	})

	timer := time.NewTimer(3 * time.Second)
	defer timer.Stop()
	select {
	case sessions := <-ch:
		return h.attachNotes(ctx, sessions), true
	case <-ctx.Done():
		return h.attachNotes(ctx, cached), true
	case <-timer.C:
		return h.attachNotes(ctx, cached), true
	}
}

func (h *Hub) SetTerminalNote(ctx context.Context, sessionName, note string) error {
	if err := h.store.SetTerminalNote(ctx, sessionName, note); err != nil {
		return err
	}
	h.broadcastApps(map[string]any{
		"type":   "mac.status",
		"status": h.Status(ctx),
	})
	return nil
}

func (h *Hub) KillTmuxSession(ctx context.Context, target string) error {
	h.mu.RLock()
	mac := h.mac
	h.mu.RUnlock()
	if mac == nil {
		return errors.New("mac agent is offline")
	}
	// Close any active phone terminal stream targeting this tmux session
	var toClose [][16]byte
	h.mu.RLock()
	for sid, st := range h.streams {
		st.mu.Lock()
		if st.target == target {
			toClose = append(toClose, sid)
		}
		st.mu.Unlock()
	}
	h.mu.RUnlock()
	for _, sid := range toClose {
		h.closeTerminalStream(ctx, sid, "session_killed", false)
	}

	mac.enqueueJSON(map[string]any{
		"type":   "term.kill",
		"target": target,
	})
	_ = h.store.DeleteTerminalNote(ctx, target)

	// Optimistically remove from cached list and broadcast
	h.mu.Lock()
	filtered := make([]model.TmuxSession, 0, len(h.tmuxSessions))
	for _, s := range h.tmuxSessions {
		if s.Name != target {
			filtered = append(filtered, s)
		}
	}
	h.tmuxSessions = filtered
	h.mu.Unlock()

	h.broadcastApps(map[string]any{
		"type":   "mac.status",
		"status": h.Status(ctx),
	})
	return nil
}

func (h *Hub) ServeAppWS(ctx context.Context, conn *websocket.Conn) {
	id := NewID()
	client := newWSClient(id, conn, 128)
	h.mu.Lock()
	h.apps[id] = client
	h.mu.Unlock()

	defer func() {
		h.mu.Lock()
		delete(h.apps, id)
		h.mu.Unlock()
		client.close()
	}()

	conn.SetReadLimit(64 * 1024)
	_ = conn.SetReadDeadline(time.Now().Add(wsPongWait))
	conn.SetPongHandler(func(string) error {
		_ = conn.SetReadDeadline(time.Now().Add(wsPongWait))
		return nil
	})

	go client.writeLoop()

	// Send initial snapshot
	status := h.Status(ctx)
	pending, _ := h.store.ListApprovals(ctx, model.StatusPending, "", 50)
	client.enqueueJSON(map[string]any{
		"type":      "init",
		"status":    status,
		"approvals": pending,
	})

	for {
		mt, data, err := conn.ReadMessage()
		if err != nil {
			return
		}
		_ = conn.SetReadDeadline(time.Now().Add(wsPongWait))
		if mt != websocket.TextMessage {
			continue
		}
		var env struct {
			Type string `json:"type"`
		}
		if json.Unmarshal(data, &env) != nil {
			continue
		}
		if env.Type == "ping" {
			client.enqueueJSON(map[string]any{"type": "pong", "ts": time.Now().UnixMilli()})
		}
	}
}

func (h *Hub) ServeMacWS(ctx context.Context, conn *websocket.Conn) {
	id := NewID()
	client := newWSClient(id, conn, 512)

	h.mu.Lock()
	prev := h.mac
	h.mac = client
	h.macLastSeen = time.Now().UnixMilli()
	h.mu.Unlock()
	if prev != nil {
		prev.close()
	}

	defer func() {
		var toClose [][16]byte
		var fsToClose []*FSStream
		var pendingRPC []chan RPCResult
		h.mu.Lock()
		if h.mac == client {
			h.mac = nil
			h.tmuxSessions = nil
			for sid := range h.streams {
				toClose = append(toClose, sid)
			}
			for _, fs := range h.fsStreams {
				fsToClose = append(fsToClose, fs)
			}
			for reqID, ch := range h.rpcWaiters {
				pendingRPC = append(pendingRPC, ch)
				delete(h.rpcWaiters, reqID)
			}
		}
		h.mu.Unlock()
		for _, sid := range toClose {
			h.closeTerminalStream(context.Background(), sid, "mac_disconnected", false)
		}
		for _, fs := range fsToClose {
			select {
			case fs.metaCh <- FSStreamMeta{StatusCode: 503, Error: "Mac 守护进程已断开"}:
			default:
			}
			fs.Close(false)
		}
		for _, ch := range pendingRPC {
			select {
			case ch <- RPCResult{Status: 503, Error: "Mac 守护进程已断开"}:
			default:
			}
		}
		client.close()
		h.broadcastApps(map[string]any{
			"type":   "mac.status",
			"status": h.Status(context.Background()),
		})
	}()

	conn.SetReadLimit(4 * 1024 * 1024)
	_ = conn.SetReadDeadline(time.Now().Add(wsPongWait))
	conn.SetPongHandler(func(string) error {
		_ = conn.SetReadDeadline(time.Now().Add(wsPongWait))
		h.mu.Lock()
		h.macLastSeen = time.Now().UnixMilli()
		h.mu.Unlock()
		return nil
	})

	go client.writeLoop()

	if cfg, err := h.store.GetSettings(ctx); err == nil {
		client.enqueueJSON(map[string]any{
			"type":     "settings.updated",
			"settings": cfg,
		})
	}
	h.broadcastApps(map[string]any{
		"type":   "mac.status",
		"status": h.Status(ctx),
	})

	for {
		mt, data, err := conn.ReadMessage()
		if err != nil {
			return
		}
		_ = conn.SetReadDeadline(time.Now().Add(wsPongWait))
		h.mu.Lock()
		h.macLastSeen = time.Now().UnixMilli()
		h.mu.Unlock()

		if mt == websocket.BinaryMessage {
			h.handleMacBinary(data)
			continue
		}
		if mt == websocket.TextMessage {
			h.handleMacText(ctx, client, data)
		}
	}
}

func (h *Hub) handleMacBinary(data []byte) {
	if len(data) < 17 {
		return
	}
	var sid [16]byte
	copy(sid[:], data[1:17])

	switch data[0] {
	case BinFrameStdout:
		payload := make([]byte, len(data)-17)
		copy(payload, data[17:])

		h.mu.RLock()
		st := h.streams[sid]
		h.mu.RUnlock()
		if st == nil {
			return
		}
		st.mu.Lock()
		st.lastActive = time.Now()
		st.mu.Unlock()
		st.app.enqueue(websocket.BinaryMessage, payload)

	case BinFrameFSChunk:
		payload := make([]byte, len(data)-17)
		copy(payload, data[17:])
		h.mu.RLock()
		fs := h.fsStreams[sid]
		h.mu.RUnlock()
		if fs == nil {
			return
		}
		if !fs.enqueueChunk(FSChunk{Data: payload}) {
			fs.Close(true)
		}

	case BinFrameFSEOF:
		h.mu.RLock()
		fs := h.fsStreams[sid]
		h.mu.RUnlock()
		if fs != nil {
			fs.enqueueChunk(FSChunk{EOF: true})
		}
	}
}

func (h *Hub) CallMacRPC(ctx context.Context, op string, params any) RPCResult {
	cfg, err := h.store.GetSettings(ctx)
	if err == nil && !cfg.FilesEnabled {
		return RPCResult{Status: 403, Error: "远程文件与 Git 功能已在控制面板关闭"}
	}
	h.mu.RLock()
	mac := h.mac
	h.mu.RUnlock()
	if mac == nil {
		return RPCResult{Status: 503, Error: "Mac 守护进程未在线"}
	}

	reqID := NewID()
	ch := make(chan RPCResult, 1)
	h.mu.Lock()
	h.rpcWaiters[reqID] = ch
	h.mu.Unlock()
	defer func() {
		h.mu.Lock()
		delete(h.rpcWaiters, reqID)
		h.mu.Unlock()
	}()

	if !mac.enqueueJSON(map[string]any{
		"type":   "rpc.req",
		"req_id": reqID,
		"op":     op,
		"params": params,
	}) {
		return RPCResult{Status: 503, Error: "Mac 隧道繁忙或已断开"}
	}

	timer := time.NewTimer(50 * time.Second)
	defer timer.Stop()
	select {
	case res := <-ch:
		return res
	case <-ctx.Done():
		return RPCResult{Status: 499, Error: "请求已取消"}
	case <-timer.C:
		return RPCResult{Status: 504, Error: "等待 Mac 响应超时"}
	}
}

func (h *Hub) OpenMacFSStream(ctx context.Context, path, kind string, page, dpi, maxDim int, rangeHdr string) (*FSStream, FSStreamMeta, error) {
	cfg, err := h.store.GetSettings(ctx)
	if err == nil && !cfg.FilesEnabled {
		return nil, FSStreamMeta{StatusCode: 403, Error: "远程文件功能已在控制面板关闭"}, errors.New("远程文件功能已在控制面板关闭")
	}
	h.mu.RLock()
	mac := h.mac
	h.mu.RUnlock()
	if mac == nil {
		return nil, FSStreamMeta{StatusCode: 503, Error: "Mac 守护进程未在线"}, errors.New("Mac 守护进程未在线")
	}

	sid, sidHex := newStreamID()
	fs := &FSStream{
		id:     sid,
		idHex:  sidHex,
		metaCh: make(chan FSStreamMeta, 1),
		chunks: make(chan FSChunk, 64),
		done:   make(chan struct{}),
		hub:    h,
	}
	h.mu.Lock()
	h.fsStreams[sid] = fs
	h.mu.Unlock()

	if !mac.enqueueJSON(map[string]any{
		"type":      "fs.stream_open",
		"stream_id": sidHex,
		"path":      path,
		"kind":      kind,
		"page":      page,
		"dpi":       dpi,
		"max_dim":   maxDim,
		"range":     rangeHdr,
	}) {
		fs.Close(false)
		return nil, FSStreamMeta{StatusCode: 503, Error: "Mac 隧道繁忙或已断开"}, errors.New("Mac 隧道繁忙或已断开")
	}

	timer := time.NewTimer(25 * time.Second)
	defer timer.Stop()
	select {
	case meta := <-fs.metaCh:
		if meta.Error != "" || meta.StatusCode >= 400 {
			fs.Close(false)
			if meta.StatusCode == 0 {
				meta.StatusCode = 500
			}
			return nil, meta, errors.New(meta.Error)
		}
		return fs, meta, nil
	case <-ctx.Done():
		fs.Close(true)
		return nil, FSStreamMeta{StatusCode: 499, Error: "请求已取消"}, ctx.Err()
	case <-timer.C:
		fs.Close(true)
		return nil, FSStreamMeta{StatusCode: 504, Error: "等待 Mac 渲染/读取流超时"}, errors.New("等待 Mac 渲染/读取流超时")
	}
}

func (h *Hub) handleMacText(ctx context.Context, mac *wsClient, data []byte) {
	var env struct {
		Type          string              `json:"type"`
		ReqID         string              `json:"req_id"`
		Hostname      string              `json:"hostname"`
		Awake         *bool               `json:"awake"`
		TmuxSessions  []model.TmuxSession `json:"tmux_sessions"`
		Approval      *model.Approval     `json:"approval"`
		TimeoutSec    int                 `json:"timeout_sec"`
		ApprovalID    string              `json:"approval_id"`
		Decision      string              `json:"decision"`
		DecidedBy     string              `json:"decided_by"`
		Message       *model.Message      `json:"message"`
		StreamID      string              `json:"stream_id"`
		Cols          int                 `json:"cols"`
		Rows          int                 `json:"rows"`
		Reason        string              `json:"reason"`
		Error         string              `json:"error"`
		Status        int                 `json:"status"`
		Data          json.RawMessage     `json:"data"`
		StatusCode    int                 `json:"status_code"`
		ContentType   string              `json:"content_type"`
		ContentLength int64               `json:"content_length"`
		TotalSize     int64               `json:"total_size"`
		ContentRange  string              `json:"content_range"`
		AcceptRanges  string              `json:"accept_ranges"`
	}
	if err := json.Unmarshal(data, &env); err != nil {
		log.Printf("mac ws bad json: %v", err)
		return
	}

	switch env.Type {
	case "mac.hello", "mac.heartbeat":
		h.mu.Lock()
		if env.Hostname != "" {
			h.macHostname = env.Hostname
		}
		if env.TmuxSessions != nil {
			h.tmuxSessions = env.TmuxSessions
		}
		h.mu.Unlock()
		if env.Type == "mac.hello" {
			h.broadcastApps(map[string]any{
				"type":   "mac.status",
				"status": h.Status(ctx),
			})
		}
	case "ping":
		mac.enqueueJSON(map[string]any{"type": "pong", "ts": time.Now().UnixMilli()})
	case "approval.create":
		if env.Approval == nil {
			return
		}
		created, err := h.CreateApproval(ctx, *env.Approval, env.TimeoutSec)
		if err != nil {
			mac.enqueueJSON(map[string]any{
				"type":   "approval.error",
				"req_id": env.ReqID,
				"error":  err.Error(),
			})
			return
		}
		mac.enqueueJSON(map[string]any{
			"type":     "approval.ack",
			"req_id":   env.ReqID,
			"approval": created,
		})
		if created.Status != model.StatusPending {
			mac.enqueueJSON(map[string]any{
				"type":     "approval.decision",
				"approval": created,
			})
		}
	case "approval.decide":
		if env.ApprovalID != "" && env.Decision != "" {
			decidedBy := strings.TrimSpace(env.DecidedBy)
			if decidedBy == "" {
				decidedBy = "mac_local"
			}
			_, _ = h.DecideApproval(ctx, env.ApprovalID, env.Decision, env.Reason, "once", decidedBy)
		}
	case "approval.cancel":
		if env.ApprovalID != "" {
			_, _ = h.DecideApproval(ctx, env.ApprovalID, model.StatusCanceled, "Mac 端已取消", "once", "mac_cancel")
		}
	case "message.create":
		if env.Message != nil {
			_, _ = h.CreateMessage(ctx, *env.Message)
		}
	case "term.list_resp":
		h.mu.Lock()
		h.tmuxSessions = env.TmuxSessions
		ch := h.termListWaiters[env.ReqID]
		h.mu.Unlock()
		if ch != nil {
			select {
			case ch <- env.TmuxSessions:
			default:
			}
		}
	case "term.opened":
		if sid, ok := parseStreamID(env.StreamID); ok {
			h.mu.RLock()
			st := h.streams[sid]
			h.mu.RUnlock()
			if st != nil {
				st.app.enqueueJSON(map[string]any{
					"type":      "opened",
					"stream_id": env.StreamID,
					"cols":      env.Cols,
					"rows":      env.Rows,
				})
			}
		}
	case "term.closed", "term.error":
		if sid, ok := parseStreamID(env.StreamID); ok {
			reason := env.Reason
			if reason == "" {
				reason = env.Error
			}
			if reason == "" {
				reason = "mac_closed"
			}
			h.closeTerminalStream(ctx, sid, reason, false)
		}
	case "rpc.resp":
		h.mu.Lock()
		ch := h.rpcWaiters[env.ReqID]
		delete(h.rpcWaiters, env.ReqID)
		h.mu.Unlock()
		if ch != nil {
			st := env.Status
			if st == 0 {
				st = 200
			}
			select {
			case ch <- RPCResult{Status: st, Data: env.Data, Error: env.Error}:
			default:
			}
		}
	case "fs.stream_meta":
		if sid, ok := parseStreamID(env.StreamID); ok {
			h.mu.RLock()
			fs := h.fsStreams[sid]
			h.mu.RUnlock()
			if fs != nil {
				select {
				case fs.metaCh <- FSStreamMeta{
					StatusCode:    env.StatusCode,
					ContentType:   env.ContentType,
					ContentLength: env.ContentLength,
					TotalSize:     env.TotalSize,
					ContentRange:  env.ContentRange,
					AcceptRanges:  env.AcceptRanges,
					Error:         env.Error,
				}:
				default:
				}
			}
		}
	case "fs.stream_error":
		if sid, ok := parseStreamID(env.StreamID); ok {
			h.mu.RLock()
			fs := h.fsStreams[sid]
			h.mu.RUnlock()
			if fs != nil {
				errMsg := env.Error
				if errMsg == "" {
					errMsg = "流传输异常"
				}
				select {
				case fs.metaCh <- FSStreamMeta{StatusCode: 500, Error: errMsg}:
				default:
				}
				fs.enqueueChunk(FSChunk{EOF: true, Err: errors.New(errMsg)})
			}
		}
	}
}

func (h *Hub) ServeTerminalWS(ctx context.Context, conn *websocket.Conn, clientIP string) {
	sid, sidHex := newStreamID()
	appClient := newWSClient(sidHex, conn, 256)
	st := &termStream{
		id:         sid,
		idHex:      sidHex,
		app:        appClient,
		clientIP:   clientIP,
		lastActive: time.Now(),
	}

	h.mu.Lock()
	h.streams[sid] = st
	h.mu.Unlock()

	defer func() {
		h.closeTerminalStream(context.Background(), sid, "app_disconnected", true)
	}()

	conn.SetReadLimit(512 * 1024)
	_ = conn.SetReadDeadline(time.Now().Add(wsPongWait))
	conn.SetPongHandler(func(string) error {
		_ = conn.SetReadDeadline(time.Now().Add(wsPongWait))
		return nil
	})

	go appClient.writeLoop()

	for {
		mt, data, err := conn.ReadMessage()
		if err != nil {
			return
		}
		_ = conn.SetReadDeadline(time.Now().Add(wsPongWait))
		st.mu.Lock()
		st.lastActive = time.Now()
		st.mu.Unlock()

		if mt == websocket.BinaryMessage {
			h.forwardAppStdin(sid, data)
			continue
		}
		if mt != websocket.TextMessage {
			continue
		}
		var req struct {
			Type   string `json:"type"`
			Mode   string `json:"mode"`
			Target string `json:"target"`
			Cols   int    `json:"cols"`
			Rows   int    `json:"rows"`
			Data   string `json:"data"`
		}
		if json.Unmarshal(data, &req) != nil {
			continue
		}
		switch req.Type {
		case "open":
			if req.Cols <= 0 {
				req.Cols = 80
			}
			if req.Rows <= 0 {
				req.Rows = 24
			}
			if req.Mode == "" {
				req.Mode = "new_shell"
			}
			h.mu.RLock()
			mac := h.mac
			h.mu.RUnlock()
			if mac == nil {
				appClient.enqueueJSON(map[string]any{"type": "error", "error": "Mac 守护进程未在线"})
				return
			}
			st.mu.Lock()
			st.mode = req.Mode
			st.target = req.Target
			needAudit := !st.auditOpened
			st.auditOpened = true
			st.mu.Unlock()
			if needAudit {
				_ = h.store.CreateTerminalAudit(ctx, model.TerminalAudit{
					ID:       NewID(),
					StreamID: sidHex,
					Mode:     req.Mode,
					Target:   req.Target,
					OpenedAt: time.Now().UnixMilli(),
					ClientIP: clientIP,
				})
			}
			mac.enqueueJSON(map[string]any{
				"type":      "term.open",
				"stream_id": sidHex,
				"mode":      req.Mode,
				"target":    req.Target,
				"cols":      req.Cols,
				"rows":      req.Rows,
			})
		case "input":
			h.forwardAppStdin(sid, []byte(req.Data))
		case "resize":
			h.mu.RLock()
			mac := h.mac
			h.mu.RUnlock()
			if mac != nil && req.Cols > 0 && req.Rows > 0 {
				mac.enqueueJSON(map[string]any{
					"type":      "term.resize",
					"stream_id": sidHex,
					"cols":      req.Cols,
					"rows":      req.Rows,
				})
			}
		case "ping":
			appClient.enqueueJSON(map[string]any{"type": "pong", "ts": time.Now().UnixMilli()})
		case "close":
			return
		}
	}
}

func (h *Hub) forwardAppStdin(sid [16]byte, payload []byte) {
	if len(payload) == 0 {
		return
	}
	h.mu.RLock()
	mac := h.mac
	h.mu.RUnlock()
	if mac == nil {
		return
	}
	frame := make([]byte, 17+len(payload))
	frame[0] = BinFrameStdin
	copy(frame[1:17], sid[:])
	copy(frame[17:], payload)
	mac.enqueue(websocket.BinaryMessage, frame)
}

func (h *Hub) closeTerminalStream(ctx context.Context, sid [16]byte, reason string, notifyMac bool) {
	h.mu.Lock()
	st := h.streams[sid]
	delete(h.streams, sid)
	mac := h.mac
	h.mu.Unlock()
	if st == nil {
		return
	}

	st.mu.Lock()
	if st.closed {
		st.mu.Unlock()
		return
	}
	st.closed = true
	st.mu.Unlock()

	_ = h.store.CloseTerminalAudit(ctx, st.idHex, reason)

	if notifyMac && mac != nil {
		mac.enqueueJSON(map[string]any{
			"type":      "term.close",
			"stream_id": st.idHex,
			"reason":    reason,
		})
	}
	st.app.enqueueJSON(map[string]any{
		"type":      "closed",
		"stream_id": st.idHex,
		"reason":    reason,
	})
	st.app.close()
}

func (h *Hub) broadcastApps(v any) {
	b, err := json.Marshal(v)
	if err != nil {
		return
	}
	h.mu.RLock()
	clients := make([]*wsClient, 0, len(h.apps))
	for _, c := range h.apps {
		clients = append(clients, c)
	}
	h.mu.RUnlock()
	for _, c := range clients {
		c.enqueue(websocket.TextMessage, b)
	}
}

func parseStreamID(hexID string) ([16]byte, bool) {
	var out [16]byte
	b, err := hex.DecodeString(hexID)
	if err != nil || len(b) != 16 {
		return out, false
	}
	copy(out[:], b)
	return out, true
}

func agentLabel(agent string) string {
	switch agent {
	case "claude":
		return "Claude Code"
	case "codex":
		return "Codex"
	case "agy":
		return "Antigravity"
	default:
		return agent
	}
}
