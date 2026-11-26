package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite"

	"phone-ai-audit/relay/internal/model"
)

var ErrNotFound = errors.New("not found")
var ErrAlreadyResolved = errors.New("approval already resolved")

type Store struct {
	db *sql.DB
}

func Open(dbPath string) (*Store, error) {
	if dbPath != ":memory:" {
		if err := os.MkdirAll(filepath.Dir(dbPath), 0o755); err != nil {
			return nil, fmt.Errorf("create db dir: %w", err)
		}
	}
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	db.SetConnMaxLifetime(0)

	pragmas := []string{
		"PRAGMA journal_mode=WAL;",
		"PRAGMA synchronous=NORMAL;",
		"PRAGMA busy_timeout=5000;",
		"PRAGMA foreign_keys=ON;",
	}
	for _, p := range pragmas {
		if _, err := db.Exec(p); err != nil {
			_ = db.Close()
			return nil, fmt.Errorf("pragma %s: %w", p, err)
		}
	}

	s := &Store{db: db}
	if err := s.migrate(); err != nil {
		_ = db.Close()
		return nil, err
	}
	return s, nil
}

func (s *Store) Close() error {
	return s.db.Close()
}

func (s *Store) migrate() error {
	ddl := `
CREATE TABLE IF NOT EXISTS approvals (
	id TEXT PRIMARY KEY,
	agent TEXT NOT NULL,
	session_id TEXT NOT NULL DEFAULT '',
	tmux_session TEXT NOT NULL DEFAULT '',
	task_title TEXT NOT NULL DEFAULT '',
	project_name TEXT NOT NULL DEFAULT '',
	cwd TEXT NOT NULL DEFAULT '',
	tool_name TEXT NOT NULL DEFAULT '',
	summary TEXT NOT NULL,
	payload_json TEXT NOT NULL DEFAULT '',
	risk_level TEXT NOT NULL DEFAULT 'low',
	risk_reasons_json TEXT NOT NULL DEFAULT '[]',
	status TEXT NOT NULL DEFAULT 'pending',
	decision_reason TEXT NOT NULL DEFAULT '',
	decision_scope TEXT NOT NULL DEFAULT 'once',
	decided_by TEXT NOT NULL DEFAULT '',
	created_at INTEGER NOT NULL,
	expires_at INTEGER NOT NULL,
	decided_at INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS idx_approvals_status_created ON approvals(status, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_approvals_expires ON approvals(status, expires_at);

CREATE TABLE IF NOT EXISTS messages (
	id TEXT PRIMARY KEY,
	agent TEXT NOT NULL,
	event_type TEXT NOT NULL,
	priority TEXT NOT NULL DEFAULT 'default',
	title TEXT NOT NULL,
	body TEXT NOT NULL DEFAULT '',
	task_title TEXT NOT NULL DEFAULT '',
	project_name TEXT NOT NULL DEFAULT '',
	tmux_session TEXT NOT NULL DEFAULT '',
	approval_id TEXT NOT NULL DEFAULT '',
	created_at INTEGER NOT NULL,
	read_at INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS idx_messages_created ON messages(created_at DESC);
CREATE INDEX IF NOT EXISTS idx_messages_agent_created ON messages(agent, created_at DESC);

CREATE TABLE IF NOT EXISTS terminal_audits (
	id TEXT PRIMARY KEY,
	stream_id TEXT NOT NULL,
	mode TEXT NOT NULL,
	target TEXT NOT NULL DEFAULT '',
	opened_at INTEGER NOT NULL,
	closed_at INTEGER NOT NULL DEFAULT 0,
	close_reason TEXT NOT NULL DEFAULT '',
	client_ip TEXT NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS idx_terminal_audits_opened ON terminal_audits(opened_at DESC);

CREATE TABLE IF NOT EXISTS kv_settings (
	key TEXT PRIMARY KEY,
	value TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS terminal_notes (
	session_name TEXT PRIMARY KEY,
	note TEXT NOT NULL DEFAULT '',
	updated_at INTEGER NOT NULL DEFAULT 0
);
`
	if _, err := s.db.Exec(ddl); err != nil {
		return fmt.Errorf("migrate sqlite: %w", err)
	}
	return nil
}

func (s *Store) GetSettings(ctx context.Context) (model.Settings, error) {
	cfg := model.DefaultSettings()
	var raw string
	err := s.db.QueryRowContext(ctx, `SELECT value FROM kv_settings WHERE key = 'settings'`).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return cfg, nil
	}
	if err != nil {
		return cfg, err
	}
	if err := json.Unmarshal([]byte(raw), &cfg); err != nil {
		return model.DefaultSettings(), nil
	}
	normalizeSettings(&cfg)
	return cfg, nil
}

func (s *Store) SaveSettings(ctx context.Context, cfg model.Settings) (model.Settings, error) {
	normalizeSettings(&cfg)
	b, err := json.Marshal(cfg)
	if err != nil {
		return cfg, err
	}
	_, err = s.db.ExecContext(ctx,
		`INSERT INTO kv_settings(key, value) VALUES('settings', ?) ON CONFLICT(key) DO UPDATE SET value=excluded.value`,
		string(b),
	)
	return cfg, err
}

func normalizeSettings(cfg *model.Settings) {
	switch cfg.ApprovalMode {
	case model.ModeAuto, model.ModeBoth, model.ModePhone, model.ModeNotify:
	default:
		cfg.ApprovalMode = model.ModeAuto
	}
	switch cfg.TimeoutAction {
	case model.TimeoutApprove, model.TimeoutFallback, model.TimeoutDeny:
	default:
		cfg.TimeoutAction = model.TimeoutApprove
	}
	if cfg.IdleThresholdSec < 5 {
		cfg.IdleThresholdSec = 5
	}
	if cfg.IdleThresholdSec > 3600 {
		cfg.IdleThresholdSec = 3600
	}
	if cfg.DefaultTimeoutSec < 5 {
		cfg.DefaultTimeoutSec = 5
	}
	if cfg.DefaultTimeoutSec > 600 {
		cfg.DefaultTimeoutSec = 600
	}
}

func (s *Store) CreateApproval(ctx context.Context, a model.Approval) error {
	reasonsJSON, _ := json.Marshal(a.RiskReasons)
	if a.RiskReasons == nil {
		reasonsJSON = []byte("[]")
	}
	if a.RiskLevel == "" {
		a.RiskLevel = "low"
	}
	if a.Status == "" {
		a.Status = model.StatusPending
	}
	_, err := s.db.ExecContext(ctx, `
INSERT INTO approvals (
	id, agent, session_id, tmux_session, task_title, project_name, cwd,
	tool_name, summary, payload_json, risk_level, risk_reasons_json,
	status, decision_reason, decision_scope, decided_by, created_at, expires_at, decided_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		a.ID, a.Agent, a.SessionID, a.TmuxSession, a.TaskTitle, a.ProjectName, a.Cwd,
		a.ToolName, a.Summary, a.PayloadJSON, a.RiskLevel, string(reasonsJSON),
		a.Status, a.DecisionReason, a.DecisionScope, a.DecidedBy, a.CreatedAt, a.ExpiresAt, a.DecidedAt,
	)
	return err
}

func (s *Store) GetApproval(ctx context.Context, id string) (model.Approval, error) {
	row := s.db.QueryRowContext(ctx, `
SELECT id, agent, session_id, tmux_session, task_title, project_name, cwd,
	tool_name, summary, payload_json, risk_level, risk_reasons_json,
	status, decision_reason, decision_scope, decided_by, created_at, expires_at, decided_at
FROM approvals WHERE id = ?`, id)
	a, err := scanApproval(row)
	if errors.Is(err, sql.ErrNoRows) {
		return a, ErrNotFound
	}
	return a, err
}

func (s *Store) ListApprovals(ctx context.Context, status, agent string, limit int) ([]model.Approval, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	var clauses []string
	var args []any
	if status != "" {
		clauses = append(clauses, "status = ?")
		args = append(args, status)
	}
	if agent != "" {
		clauses = append(clauses, "agent = ?")
		args = append(args, agent)
	}
	q := `SELECT id, agent, session_id, tmux_session, task_title, project_name, cwd,
	tool_name, summary, payload_json, risk_level, risk_reasons_json,
	status, decision_reason, decision_scope, decided_by, created_at, expires_at, decided_at
FROM approvals`
	if len(clauses) > 0 {
		q += " WHERE " + strings.Join(clauses, " AND ")
	}
	q += " ORDER BY created_at DESC LIMIT ?"
	args = append(args, limit)

	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]model.Approval, 0)
	for rows.Next() {
		a, err := scanApproval(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

func (s *Store) ResolveApproval(ctx context.Context, id, status, reason, scope, decidedBy string) (model.Approval, error) {
	now := time.Now().UnixMilli()
	if scope == "" {
		scope = "once"
	}
	res, err := s.db.ExecContext(ctx, `
UPDATE approvals
SET status = ?, decision_reason = ?, decision_scope = ?, decided_by = ?, decided_at = ?
WHERE id = ? AND status = 'pending'`,
		status, reason, scope, decidedBy, now, id,
	)
	if err != nil {
		return model.Approval{}, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return model.Approval{}, err
	}
	a, err := s.GetApproval(ctx, id)
	if err != nil {
		return model.Approval{}, err
	}
	if n == 0 {
		return a, ErrAlreadyResolved
	}
	return a, nil
}

func (s *Store) ExpirePendingApprovals(ctx context.Context, nowMs int64, targetStatus string) ([]model.Approval, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id FROM approvals WHERE status = 'pending' AND expires_at > 0 AND expires_at <= ?`,
		nowMs,
	)
	if err != nil {
		return nil, err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		ids = append(ids, id)
	}
	rows.Close()

	resolved := make([]model.Approval, 0, len(ids))
	reason := "审批超时，已退回电脑端处理"
	switch targetStatus {
	case model.StatusApproved:
		reason = "审批超时，系统自动确认允许"
	case model.StatusExpired:
		reason = "审批超时，系统自动拒绝"
	}
	for _, id := range ids {
		a, err := s.ResolveApproval(ctx, id, targetStatus, reason, "once", "system_timeout")
		if err == nil {
			resolved = append(resolved, a)
		}
	}
	return resolved, nil
}

func (s *Store) CreateMessage(ctx context.Context, m model.Message) error {
	if m.Priority == "" {
		m.Priority = "default"
	}
	if m.CreatedAt == 0 {
		m.CreatedAt = time.Now().UnixMilli()
	}
	_, err := s.db.ExecContext(ctx, `
INSERT INTO messages (
	id, agent, event_type, priority, title, body, task_title, project_name,
	tmux_session, approval_id, created_at, read_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		m.ID, m.Agent, m.EventType, m.Priority, m.Title, m.Body, m.TaskTitle, m.ProjectName,
		m.TmuxSession, m.ApprovalID, m.CreatedAt, m.ReadAt,
	)
	return err
}

func (s *Store) ListMessages(ctx context.Context, agent, eventType string, beforeMs int64, limit int) ([]model.Message, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	var clauses []string
	var args []any
	if agent != "" {
		clauses = append(clauses, "agent = ?")
		args = append(args, agent)
	}
	if eventType != "" {
		clauses = append(clauses, "event_type = ?")
		args = append(args, eventType)
	}
	if beforeMs > 0 {
		clauses = append(clauses, "created_at < ?")
		args = append(args, beforeMs)
	}
	q := `SELECT id, agent, event_type, priority, title, body, task_title, project_name,
	tmux_session, approval_id, created_at, read_at
FROM messages`
	if len(clauses) > 0 {
		q += " WHERE " + strings.Join(clauses, " AND ")
	}
	q += " ORDER BY created_at DESC LIMIT ?"
	args = append(args, limit)

	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]model.Message, 0)
	for rows.Next() {
		var m model.Message
		if err := rows.Scan(
			&m.ID, &m.Agent, &m.EventType, &m.Priority, &m.Title, &m.Body,
			&m.TaskTitle, &m.ProjectName, &m.TmuxSession, &m.ApprovalID, &m.CreatedAt, &m.ReadAt,
		); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

func (s *Store) MarkMessagesRead(ctx context.Context, ids []string) error {
	now := time.Now().UnixMilli()
	if len(ids) == 0 {
		_, err := s.db.ExecContext(ctx, `UPDATE messages SET read_at = ? WHERE read_at = 0`, now)
		return err
	}
	placeholders := make([]string, len(ids))
	args := make([]any, 0, len(ids)+1)
	args = append(args, now)
	for i, id := range ids {
		placeholders[i] = "?"
		args = append(args, id)
	}
	q := fmt.Sprintf(`UPDATE messages SET read_at = ? WHERE read_at = 0 AND id IN (%s)`, strings.Join(placeholders, ","))
	_, err := s.db.ExecContext(ctx, q, args...)
	return err
}

func (s *Store) MarkMessagesReadByApprovalID(ctx context.Context, approvalID string) error {
	if strings.TrimSpace(approvalID) == "" {
		return nil
	}
	_, err := s.db.ExecContext(ctx,
		`UPDATE messages SET read_at = ? WHERE read_at = 0 AND approval_id = ?`,
		time.Now().UnixMilli(), approvalID,
	)
	return err
}

func (s *Store) Counts(ctx context.Context) (pending int, unread int, err error) {
	if err = s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM approvals WHERE status = 'pending'`).Scan(&pending); err != nil {
		return 0, 0, err
	}
	if err = s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM messages WHERE read_at = 0`).Scan(&unread); err != nil {
		return 0, 0, err
	}
	return pending, unread, nil
}

func (s *Store) CreateTerminalAudit(ctx context.Context, a model.TerminalAudit) error {
	_, err := s.db.ExecContext(ctx, `
INSERT INTO terminal_audits (id, stream_id, mode, target, opened_at, closed_at, close_reason, client_ip)
VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		a.ID, a.StreamID, a.Mode, a.Target, a.OpenedAt, a.ClosedAt, a.CloseReason, a.ClientIP,
	)
	return err
}

func (s *Store) CloseTerminalAudit(ctx context.Context, streamID, reason string) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE terminal_audits SET closed_at = ?, close_reason = ? WHERE stream_id = ? AND closed_at = 0`,
		time.Now().UnixMilli(), reason, streamID,
	)
	return err
}

func (s *Store) ListTerminalAudits(ctx context.Context, limit int) ([]model.TerminalAudit, error) {
	if limit <= 0 || limit > 100 {
		limit = 30
	}
	rows, err := s.db.QueryContext(ctx, `
SELECT id, stream_id, mode, target, opened_at, closed_at, close_reason, client_ip
FROM terminal_audits ORDER BY opened_at DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]model.TerminalAudit, 0)
	for rows.Next() {
		var a model.TerminalAudit
		if err := rows.Scan(&a.ID, &a.StreamID, &a.Mode, &a.Target, &a.OpenedAt, &a.ClosedAt, &a.CloseReason, &a.ClientIP); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

func (s *Store) PruneOldRecords(ctx context.Context, keepDays int) {
	if keepDays <= 0 {
		keepDays = 30
	}
	cutoff := time.Now().AddDate(0, 0, -keepDays).UnixMilli()
	_, _ = s.db.ExecContext(ctx, `DELETE FROM messages WHERE created_at < ?`, cutoff)
	_, _ = s.db.ExecContext(ctx, `DELETE FROM approvals WHERE status != 'pending' AND created_at < ?`, cutoff)
	_, _ = s.db.ExecContext(ctx, `DELETE FROM terminal_audits WHERE opened_at < ?`, cutoff)
}

func (s *Store) SetTerminalNote(ctx context.Context, sessionName, note string) error {
	note = strings.TrimSpace(note)
	if note == "" {
		_, err := s.db.ExecContext(ctx, `DELETE FROM terminal_notes WHERE session_name = ?`, sessionName)
		return err
	}
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO terminal_notes(session_name, note, updated_at) VALUES(?, ?, ?)
		 ON CONFLICT(session_name) DO UPDATE SET note=excluded.note, updated_at=excluded.updated_at`,
		sessionName, note, time.Now().UnixMilli(),
	)
	return err
}

func (s *Store) DeleteTerminalNote(ctx context.Context, sessionName string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM terminal_notes WHERE session_name = ?`, sessionName)
	return err
}

func (s *Store) GetTerminalNotes(ctx context.Context) (map[string]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT session_name, note FROM terminal_notes`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make(map[string]string)
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err == nil {
			out[k] = v
		}
	}
	return out, rows.Err()
}

type scanner interface {
	Scan(dest ...any) error
}

func scanApproval(row scanner) (model.Approval, error) {
	var a model.Approval
	var reasonsRaw string
	err := row.Scan(
		&a.ID, &a.Agent, &a.SessionID, &a.TmuxSession, &a.TaskTitle, &a.ProjectName, &a.Cwd,
		&a.ToolName, &a.Summary, &a.PayloadJSON, &a.RiskLevel, &reasonsRaw,
		&a.Status, &a.DecisionReason, &a.DecisionScope, &a.DecidedBy, &a.CreatedAt, &a.ExpiresAt, &a.DecidedAt,
	)
	if err != nil {
		return a, err
	}
	_ = json.Unmarshal([]byte(reasonsRaw), &a.RiskReasons)
	return a, nil
}
