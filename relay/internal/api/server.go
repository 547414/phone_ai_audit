package api

import (
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gorilla/websocket"

	"phone-ai-audit/relay/internal/config"
	"phone-ai-audit/relay/internal/hub"
	"phone-ai-audit/relay/internal/model"
	"phone-ai-audit/relay/internal/store"
)

type Server struct {
	cfg      config.Config
	store    *store.Store
	hub      *hub.Hub
	upgrader websocket.Upgrader
	mux      *http.ServeMux
}

func New(cfg config.Config, st *store.Store, h *hub.Hub) *Server {
	s := &Server{
		cfg:   cfg,
		store: st,
		hub:   h,
		upgrader: websocket.Upgrader{
			ReadBufferSize:  8192,
			WriteBufferSize: 8192,
			CheckOrigin:     func(r *http.Request) bool { return true },
		},
		mux: http.NewServeMux(),
	}
	s.routes()
	return s
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mux.ServeHTTP(w, r)
}

func (s *Server) routes() {
	s.mux.HandleFunc("GET /api/v1/health", s.handleHealth)
	s.mux.HandleFunc("GET /api/v1/status", s.requireAny(s.handleStatus))

	// Approvals
	s.mux.HandleFunc("POST /api/v1/approvals", s.requireMac(s.handleCreateApproval))
	s.mux.HandleFunc("GET /api/v1/approvals/{id}/wait", s.requireMac(s.handleWaitApproval))
	s.mux.HandleFunc("POST /api/v1/approvals/{id}/cancel", s.requireMac(s.handleCancelApproval))
	s.mux.HandleFunc("GET /api/v1/approvals", s.requireApp(s.handleListApprovals))
	s.mux.HandleFunc("GET /api/v1/approvals/{id}", s.requireAny(s.handleGetApproval))
	s.mux.HandleFunc("POST /api/v1/approvals/{id}/decide", s.requireApp(s.handleDecideApproval))
	s.mux.HandleFunc("POST /api/v1/approvals/{id}/hold", s.requireAny(s.handleHoldApproval))

	// Messages
	s.mux.HandleFunc("POST /api/v1/messages", s.requireMac(s.handleCreateMessage))
	s.mux.HandleFunc("GET /api/v1/messages", s.requireApp(s.handleListMessages))
	s.mux.HandleFunc("POST /api/v1/messages/read", s.requireApp(s.handleMarkRead))

	// Settings
	s.mux.HandleFunc("GET /api/v1/settings", s.requireAny(s.handleGetSettings))
	s.mux.HandleFunc("PUT /api/v1/settings", s.requireAny(s.handlePutSettings))

	// Terminals
	s.mux.HandleFunc("GET /api/v1/terminals/sessions", s.requireApp(s.handleListTmuxSessions))
	s.mux.HandleFunc("GET /api/v1/terminals/agy-profiles", s.requireApp(s.handleListAgyProfiles))
	s.mux.HandleFunc("PUT /api/v1/terminals/sessions/{name}/note", s.requireApp(s.handleSetTerminalNote))
	s.mux.HandleFunc("DELETE /api/v1/terminals/sessions/{name}", s.requireApp(s.handleKillTmuxSession))
	s.mux.HandleFunc("POST /api/v1/terminals/ticket", s.requireApp(s.handleIssueTerminalTicket))
	s.mux.HandleFunc("GET /api/v1/terminals/audits", s.requireApp(s.handleListTerminalAudits))

	// Remote Mac Files (Zero-Download Streaming + Online Edit)
	s.mux.HandleFunc("GET /api/v1/fs/roots", s.requireApp(s.handleFSRoots))
	s.mux.HandleFunc("GET /api/v1/fs/list", s.requireApp(s.handleFSList))
	s.mux.HandleFunc("GET /api/v1/fs/search", s.requireApp(s.handleFSSearch))
	s.mux.HandleFunc("GET /api/v1/fs/read", s.requireApp(s.handleFSRead))
	s.mux.HandleFunc("PUT /api/v1/fs/write", s.requireApp(s.handleFSWrite))
	s.mux.HandleFunc("POST /api/v1/fs/mkdir", s.requireApp(s.handleFSMkdir))
	s.mux.HandleFunc("POST /api/v1/fs/touch", s.requireApp(s.handleFSTouch))
	s.mux.HandleFunc("POST /api/v1/fs/rename", s.requireApp(s.handleFSRename))
	s.mux.HandleFunc("POST /api/v1/fs/trash", s.requireApp(s.handleFSTrash))
	s.mux.HandleFunc("GET /api/v1/fs/stream", s.requireApp(s.handleFSStream))
	s.mux.HandleFunc("GET /api/v1/fs/pdf/meta", s.requireApp(s.handleFSPDFMeta))
	s.mux.HandleFunc("GET /api/v1/fs/excel/meta", s.requireApp(s.handleFSExcelMeta))
	s.mux.HandleFunc("GET /api/v1/fs/excel/sheet", s.requireApp(s.handleFSExcelSheet))
	s.mux.HandleFunc("PUT /api/v1/fs/excel/cell", s.requireApp(s.handleFSExcelCell))
	s.mux.HandleFunc("GET /api/v1/fs/pptx/outline", s.requireApp(s.handleFSPPTXOutline))

	// Git Workbench
	s.mux.HandleFunc("GET /api/v1/git/status", s.requireApp(s.handleGitStatus))
	s.mux.HandleFunc("GET /api/v1/git/diff", s.requireApp(s.handleGitDiff))
	s.mux.HandleFunc("POST /api/v1/git/commit", s.requireApp(s.handleGitCommit))
	s.mux.HandleFunc("POST /api/v1/git/push", s.requireApp(s.handleGitPush))
	s.mux.HandleFunc("POST /api/v1/git/pull", s.requireApp(s.handleGitPull))
	s.mux.HandleFunc("POST /api/v1/git/discard", s.requireApp(s.handleGitDiscard))
	s.mux.HandleFunc("GET /api/v1/git/log", s.requireApp(s.handleGitLog))

	// Agent Usage & Quotas (RPC to Mac daemon)
	s.mux.HandleFunc("GET /api/v1/usage", s.requireAny(s.handleUsage))

	// WebSockets
	s.mux.HandleFunc("GET /api/v1/ws/mac", s.requireMac(s.handleMacWS))
	s.mux.HandleFunc("GET /api/v1/ws/app", s.requireApp(s.handleAppWS))
	s.mux.HandleFunc("GET /api/v1/ws/terminal", s.requireApp(s.handleTerminalWS))
}

func (s *Server) bearerToken(r *http.Request) string {
	auth := strings.TrimSpace(r.Header.Get("Authorization"))
	if strings.HasPrefix(strings.ToLower(auth), "bearer ") {
		return strings.TrimSpace(auth[7:])
	}
	return ""
}

func equalToken(a, b string) bool {
	if len(a) == 0 || len(a) != len(b) {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}

func (s *Server) requireMac(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !equalToken(s.bearerToken(r), s.cfg.MacToken) {
			writeError(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		next(w, r)
	}
}

func (s *Server) requireApp(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !equalToken(s.bearerToken(r), s.cfg.AppToken) {
			writeError(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		next(w, r)
	}
}

func (s *Server) requireAny(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		tok := s.bearerToken(r)
		if !equalToken(tok, s.cfg.MacToken) && !equalToken(tok, s.cfg.AppToken) {
			writeError(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		next(w, r)
	}
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":   true,
		"time": time.Now().UnixMilli(),
	})
}

func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.hub.Status(r.Context()))
}

func (s *Server) handleCreateApproval(w http.ResponseWriter, r *http.Request) {
	var body struct {
		model.Approval
		TimeoutSec int `json:"timeout_sec"`
	}
	if err := readJSON(r, &body, 512*1024); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if body.Agent == "" || body.Summary == "" {
		writeError(w, http.StatusBadRequest, "agent and summary are required")
		return
	}
	created, err := s.hub.CreateApproval(r.Context(), body.Approval, body.TimeoutSec)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, created)
}

func (s *Server) handleWaitApproval(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	timeoutSec := 180
	if raw := r.URL.Query().Get("timeout"); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil && n > 0 && n <= 620 {
			timeoutSec = n
		}
	}
	a, err := s.hub.WaitApproval(r.Context(), id, time.Duration(timeoutSec)*time.Second)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "approval not found")
			return
		}
		writeError(w, http.StatusRequestTimeout, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, a)
}

func (s *Server) handleCancelApproval(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	a, err := s.hub.DecideApproval(r.Context(), id, model.StatusCanceled, "Mac 端已取消", "once", "mac_cancel")
	if err != nil && !errors.Is(err, store.ErrAlreadyResolved) {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "approval not found")
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, a)
}

func (s *Server) handleListApprovals(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	limit, _ := strconv.Atoi(q.Get("limit"))
	list, err := s.store.ListApprovals(r.Context(), q.Get("status"), q.Get("agent"), limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": list})
}

func (s *Server) handleGetApproval(w http.ResponseWriter, r *http.Request) {
	a, err := s.store.GetApproval(r.Context(), r.PathValue("id"))
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "approval not found")
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, a)
}

func (s *Server) handleDecideApproval(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var req struct {
		Decision string `json:"decision"`
		Reason   string `json:"reason"`
		Scope    string `json:"scope"`
	}
	if err := readJSON(r, &req, 64*1024); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	a, err := s.hub.DecideApproval(r.Context(), id, req.Decision, strings.TrimSpace(req.Reason), req.Scope, "android_app")
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "approval not found")
			return
		}
		if errors.Is(err, store.ErrAlreadyResolved) {
			writeJSON(w, http.StatusConflict, map[string]any{"error": "already_resolved", "approval": a})
			return
		}
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, a)
}

func (s *Server) handleHoldApproval(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var req struct {
		Paused    bool `json:"paused"`
		ResumeSec int  `json:"resume_sec"`
	}
	if err := readJSON(r, &req, 16*1024); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	a, err := s.hub.SetApprovalHold(r.Context(), id, req.Paused, req.ResumeSec)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "approval not found")
			return
		}
		if errors.Is(err, store.ErrAlreadyResolved) {
			writeJSON(w, http.StatusConflict, map[string]any{"error": "already_resolved", "approval": a})
			return
		}
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, a)
}

func (s *Server) handleCreateMessage(w http.ResponseWriter, r *http.Request) {
	var m model.Message
	if err := readJSON(r, &m, 256*1024); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if m.Agent == "" || m.Title == "" {
		writeError(w, http.StatusBadRequest, "agent and title are required")
		return
	}
	created, err := s.hub.CreateMessage(r.Context(), m)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, created)
}

func (s *Server) handleListMessages(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	limit, _ := strconv.Atoi(q.Get("limit"))
	before, _ := strconv.ParseInt(q.Get("before"), 10, 64)
	list, err := s.store.ListMessages(r.Context(), q.Get("agent"), q.Get("event_type"), before, limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": list})
}

func (s *Server) handleMarkRead(w http.ResponseWriter, r *http.Request) {
	var req struct {
		IDs []string `json:"ids"`
	}
	_ = readJSON(r, &req, 64*1024)
	if err := s.store.MarkMessagesRead(r.Context(), req.IDs); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) handleGetSettings(w http.ResponseWriter, r *http.Request) {
	cfg, err := s.store.GetSettings(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, cfg)
}

func (s *Server) handlePutSettings(w http.ResponseWriter, r *http.Request) {
	cur, err := s.store.GetSettings(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	var patch struct {
		ApprovalMode       *string `json:"approval_mode"`
		IdleThresholdSec   *int    `json:"idle_threshold_sec"`
		DefaultTimeoutSec  *int    `json:"default_timeout_sec"`
		TimeoutAction      *string `json:"timeout_action"`
		TerminalEnabled    *bool   `json:"terminal_enabled"`
		FilesEnabled       *bool   `json:"files_enabled"`
		MacAwakeEnabled    *bool   `json:"mac_awake_enabled"`
		HighRiskRequireApp *bool   `json:"high_risk_require_app"`
	}
	if err := readJSON(r, &patch, 32*1024); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if patch.ApprovalMode != nil {
		cur.ApprovalMode = *patch.ApprovalMode
	}
	if patch.IdleThresholdSec != nil {
		cur.IdleThresholdSec = *patch.IdleThresholdSec
	}
	if patch.DefaultTimeoutSec != nil {
		cur.DefaultTimeoutSec = *patch.DefaultTimeoutSec
	}
	if patch.TimeoutAction != nil {
		cur.TimeoutAction = *patch.TimeoutAction
	}
	if patch.TerminalEnabled != nil {
		cur.TerminalEnabled = *patch.TerminalEnabled
	}
	if patch.FilesEnabled != nil {
		cur.FilesEnabled = *patch.FilesEnabled
	}
	if patch.MacAwakeEnabled != nil {
		cur.MacAwakeEnabled = *patch.MacAwakeEnabled
	}
	if patch.HighRiskRequireApp != nil {
		cur.HighRiskRequireApp = *patch.HighRiskRequireApp
	}
	saved, err := s.hub.UpdateSettings(r.Context(), cur)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, saved)
}

func (s *Server) handleListTmuxSessions(w http.ResponseWriter, r *http.Request) {
	sessions, online := s.hub.ListTmuxSessions(r.Context())
	if sessions == nil {
		sessions = []model.TmuxSession{}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"mac_online": online,
		"sessions":   sessions,
	})
}

func (s *Server) handleListAgyProfiles(w http.ResponseWriter, r *http.Request) {
	s.writeRPC(w, s.hub.CallMacRPC(r.Context(), "agy.profiles", nil))
}

func (s *Server) handleSetTerminalNote(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimSpace(r.PathValue("name"))
	if name == "" {
		writeError(w, http.StatusBadRequest, "missing session name")
		return
	}
	var req struct {
		Note string `json:"note"`
	}
	if err := readJSON(r, &req, 16*1024); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := s.hub.SetTerminalNote(r.Context(), name, req.Note); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "name": name, "note": strings.TrimSpace(req.Note)})
}

func (s *Server) handleKillTmuxSession(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimSpace(r.PathValue("name"))
	if name == "" {
		writeError(w, http.StatusBadRequest, "missing session name")
		return
	}
	if err := s.hub.KillTmuxSession(r.Context(), name); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "killed": name})
}

func (s *Server) handleIssueTerminalTicket(w http.ResponseWriter, r *http.Request) {
	tok, err := s.hub.IssueTerminalTicket(r.Context(), clientIP(r))
	if err != nil {
		writeError(w, http.StatusForbidden, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ticket":     tok,
		"expires_in": 45,
	})
}

func (s *Server) handleListTerminalAudits(w http.ResponseWriter, r *http.Request) {
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	items, err := s.store.ListTerminalAudits(r.Context(), limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

// --- Remote Mac Files & Git Workbench handlers ---

func (s *Server) writeRPC(w http.ResponseWriter, res hub.RPCResult) {
	status := res.Status
	if status == 0 {
		status = http.StatusOK
	}
	if status >= 400 {
		out := map[string]any{"error": res.Error}
		if len(res.Data) > 0 && string(res.Data) != "null" {
			out["data"] = json.RawMessage(res.Data)
		}
		writeJSON(w, status, out)
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	if len(res.Data) > 0 {
		_, _ = w.Write(res.Data)
	} else {
		_, _ = w.Write([]byte(`{"ok":true}`))
	}
}

func (s *Server) handleFSRoots(w http.ResponseWriter, r *http.Request) {
	s.writeRPC(w, s.hub.CallMacRPC(r.Context(), "fs.roots", nil))
}

func (s *Server) handleFSList(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	hidden := q.Get("hidden") == "1" || strings.EqualFold(q.Get("hidden"), "true")
	s.writeRPC(w, s.hub.CallMacRPC(r.Context(), "fs.list", map[string]any{
		"path":   q.Get("path"),
		"hidden": hidden,
		"sort":   q.Get("sort"),
	}))
}

func (s *Server) handleFSSearch(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	s.writeRPC(w, s.hub.CallMacRPC(r.Context(), "fs.search", map[string]any{
		"path": q.Get("path"),
		"q":    q.Get("q"),
		"mode": q.Get("mode"),
	}))
}

func (s *Server) handleFSRead(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	offsetLine, _ := strconv.Atoi(q.Get("offset_line"))
	limitLines, _ := strconv.Atoi(q.Get("limit_lines"))
	tail := q.Get("tail") == "1" || strings.EqualFold(q.Get("tail"), "true")
	s.writeRPC(w, s.hub.CallMacRPC(r.Context(), "fs.read", map[string]any{
		"path":        q.Get("path"),
		"offset_line": offsetLine,
		"limit_lines": limitLines,
		"tail":        tail,
	}))
}

func (s *Server) handleFSWrite(w http.ResponseWriter, r *http.Request) {
	var body map[string]any
	if err := readJSON(r, &body, 2*1024*1024); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	s.writeRPC(w, s.hub.CallMacRPC(r.Context(), "fs.write", body))
}

func (s *Server) handleFSMkdir(w http.ResponseWriter, r *http.Request) {
	var body map[string]any
	if err := readJSON(r, &body, 32*1024); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	s.writeRPC(w, s.hub.CallMacRPC(r.Context(), "fs.mkdir", body))
}

func (s *Server) handleFSTouch(w http.ResponseWriter, r *http.Request) {
	var body map[string]any
	if err := readJSON(r, &body, 32*1024); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	s.writeRPC(w, s.hub.CallMacRPC(r.Context(), "fs.touch", body))
}

func (s *Server) handleFSRename(w http.ResponseWriter, r *http.Request) {
	var body map[string]any
	if err := readJSON(r, &body, 32*1024); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	s.writeRPC(w, s.hub.CallMacRPC(r.Context(), "fs.rename", body))
}

func (s *Server) handleFSTrash(w http.ResponseWriter, r *http.Request) {
	var body map[string]any
	if err := readJSON(r, &body, 32*1024); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	s.writeRPC(w, s.hub.CallMacRPC(r.Context(), "fs.trash", body))
}

func (s *Server) handleFSPDFMeta(w http.ResponseWriter, r *http.Request) {
	s.writeRPC(w, s.hub.CallMacRPC(r.Context(), "fs.pdf.meta", map[string]any{
		"path": r.URL.Query().Get("path"),
	}))
}

func (s *Server) handleFSExcelMeta(w http.ResponseWriter, r *http.Request) {
	s.writeRPC(w, s.hub.CallMacRPC(r.Context(), "fs.excel.meta", map[string]any{
		"path": r.URL.Query().Get("path"),
	}))
}

func (s *Server) handleFSExcelSheet(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	rowStart, _ := strconv.Atoi(q.Get("row_start"))
	limit, _ := strconv.Atoi(q.Get("limit"))
	s.writeRPC(w, s.hub.CallMacRPC(r.Context(), "fs.excel.sheet", map[string]any{
		"path":      q.Get("path"),
		"sheet":     q.Get("sheet"),
		"row_start": rowStart,
		"limit":     limit,
	}))
}

func (s *Server) handleFSExcelCell(w http.ResponseWriter, r *http.Request) {
	var body map[string]any
	if err := readJSON(r, &body, 128*1024); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	s.writeRPC(w, s.hub.CallMacRPC(r.Context(), "fs.excel.cell", body))
}

func (s *Server) handleFSPPTXOutline(w http.ResponseWriter, r *http.Request) {
	s.writeRPC(w, s.hub.CallMacRPC(r.Context(), "fs.pptx.outline", map[string]any{
		"path": r.URL.Query().Get("path"),
	}))
}

func (s *Server) handleFSStream(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	reqPath := q.Get("path")
	kind := q.Get("kind")
	page, _ := strconv.Atoi(q.Get("page"))
	dpi, _ := strconv.Atoi(q.Get("dpi"))
	maxDim, _ := strconv.Atoi(q.Get("max_dim"))
	rangeHdr := r.Header.Get("Range")

	fs, meta, err := s.hub.OpenMacFSStream(r.Context(), reqPath, kind, page, dpi, maxDim, rangeHdr)
	if err != nil {
		code := meta.StatusCode
		if code == 0 {
			code = http.StatusBadGateway
		}
		writeError(w, code, err.Error())
		return
	}
	defer fs.Close(true)

	if meta.ContentType != "" {
		w.Header().Set("Content-Type", meta.ContentType)
	}
	if meta.ContentLength >= 0 {
		w.Header().Set("Content-Length", strconv.FormatInt(meta.ContentLength, 10))
	}
	if meta.ContentRange != "" {
		w.Header().Set("Content-Range", meta.ContentRange)
	}
	if meta.AcceptRanges != "" {
		w.Header().Set("Accept-Ranges", meta.AcceptRanges)
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Accel-Buffering", "no")

	status := meta.StatusCode
	if status == 0 {
		status = http.StatusOK
	}
	w.WriteHeader(status)

	flusher, canFlush := w.(http.Flusher)
	for {
		select {
		case <-r.Context().Done():
			return
		case <-fs.Done():
			return
		case ch := <-fs.Chunks():
			if len(ch.Data) > 0 {
				if _, werr := w.Write(ch.Data); werr != nil {
					return
				}
				if canFlush {
					flusher.Flush()
				}
			}
			if ch.EOF {
				return
			}
		}
	}
}

func (s *Server) handleGitStatus(w http.ResponseWriter, r *http.Request) {
	s.writeRPC(w, s.hub.CallMacRPC(r.Context(), "git.status", map[string]any{
		"path": r.URL.Query().Get("path"),
	}))
}

func (s *Server) handleGitDiff(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	staged := q.Get("staged") == "1" || strings.EqualFold(q.Get("staged"), "true")
	s.writeRPC(w, s.hub.CallMacRPC(r.Context(), "git.diff", map[string]any{
		"path":   q.Get("path"),
		"file":   q.Get("file"),
		"staged": staged,
		"commit": q.Get("commit"),
	}))
}

func (s *Server) handleGitCommit(w http.ResponseWriter, r *http.Request) {
	var body map[string]any
	if err := readJSON(r, &body, 256*1024); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	s.writeRPC(w, s.hub.CallMacRPC(r.Context(), "git.commit", body))
}

func (s *Server) handleGitPush(w http.ResponseWriter, r *http.Request) {
	var body map[string]any
	if err := readJSON(r, &body, 32*1024); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	s.writeRPC(w, s.hub.CallMacRPC(r.Context(), "git.push", body))
}

func (s *Server) handleGitPull(w http.ResponseWriter, r *http.Request) {
	var body map[string]any
	if err := readJSON(r, &body, 32*1024); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	s.writeRPC(w, s.hub.CallMacRPC(r.Context(), "git.pull", body))
}

func (s *Server) handleGitDiscard(w http.ResponseWriter, r *http.Request) {
	var body map[string]any
	if err := readJSON(r, &body, 32*1024); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	s.writeRPC(w, s.hub.CallMacRPC(r.Context(), "git.discard", body))
}

func (s *Server) handleGitLog(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	limit, _ := strconv.Atoi(q.Get("limit"))
	s.writeRPC(w, s.hub.CallMacRPC(r.Context(), "git.log", map[string]any{
		"path":  q.Get("path"),
		"limit": limit,
	}))
}

func (s *Server) handleUsage(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query().Get("force")
	force := q == "1" || strings.EqualFold(q, "true")
	s.writeRPC(w, s.hub.CallMacRPC(r.Context(), "usage.summary", map[string]any{
		"force": force,
	}))
}

func (s *Server) handleMacWS(w http.ResponseWriter, r *http.Request) {
	conn, err := s.upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	s.hub.ServeMacWS(r.Context(), conn)
}

func (s *Server) handleAppWS(w http.ResponseWriter, r *http.Request) {
	conn, err := s.upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	s.hub.ServeAppWS(r.Context(), conn)
}

func (s *Server) handleTerminalWS(w http.ResponseWriter, r *http.Request) {
	ticket := strings.TrimSpace(r.Header.Get("X-Terminal-Ticket"))
	if ticket == "" {
		writeError(w, http.StatusForbidden, "missing X-Terminal-Ticket")
		return
	}
	if _, ok := s.hub.ConsumeTerminalTicket(ticket); !ok {
		writeError(w, http.StatusForbidden, "invalid or expired terminal ticket")
		return
	}
	conn, err := s.upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	s.hub.ServeTerminalWS(r.Context(), conn, clientIP(r))
}

func readJSON(r *http.Request, dst any, maxBytes int64) error {
	defer r.Body.Close()
	b, err := io.ReadAll(io.LimitReader(r.Body, maxBytes))
	if err != nil {
		return err
	}
	if len(b) == 0 {
		return nil
	}
	return json.Unmarshal(b, dst)
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]any{"error": msg})
}

func clientIP(r *http.Request) string {
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		parts := strings.Split(xff, ",")
		return strings.TrimSpace(parts[0])
	}
	if xri := r.Header.Get("X-Real-IP"); xri != "" {
		return strings.TrimSpace(xri)
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}
