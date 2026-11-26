package model

const (
	StatusPending  = "pending"
	StatusApproved = "approved"
	StatusDenied   = "denied"
	StatusFallback = "fallback"
	StatusExpired  = "expired"
	StatusCanceled = "canceled"
)

const (
	ModeAuto   = "auto"
	ModeBoth   = "both"
	ModePhone  = "phone"
	ModeNotify = "notify"
)

const (
	TimeoutApprove  = "approve"
	TimeoutFallback = "fallback"
	TimeoutDeny     = "deny"
)

type Approval struct {
	ID             string   `json:"id"`
	Agent          string   `json:"agent"`
	SessionID      string   `json:"session_id,omitempty"`
	TmuxSession    string   `json:"tmux_session,omitempty"`
	TaskTitle      string   `json:"task_title,omitempty"`
	ProjectName    string   `json:"project_name,omitempty"`
	Cwd            string   `json:"cwd,omitempty"`
	ToolName       string   `json:"tool_name,omitempty"`
	Summary        string   `json:"summary"`
	PayloadJSON    string   `json:"payload_json,omitempty"`
	RiskLevel      string   `json:"risk_level"`
	RiskReasons    []string `json:"risk_reasons,omitempty"`
	Status         string   `json:"status"`
	DecisionReason string   `json:"decision_reason,omitempty"`
	DecisionScope  string   `json:"decision_scope,omitempty"`
	DecidedBy      string   `json:"decided_by,omitempty"`
	CreatedAt      int64    `json:"created_at"`
	ExpiresAt      int64    `json:"expires_at"`
	DecidedAt      int64    `json:"decided_at,omitempty"`
}

type Message struct {
	ID          string `json:"id"`
	Agent       string `json:"agent"`
	EventType   string `json:"event_type"`
	Priority    string `json:"priority"`
	Title       string `json:"title"`
	Body        string `json:"body"`
	TaskTitle   string `json:"task_title,omitempty"`
	ProjectName string `json:"project_name,omitempty"`
	TmuxSession string `json:"tmux_session,omitempty"`
	ApprovalID  string `json:"approval_id,omitempty"`
	CreatedAt   int64  `json:"created_at"`
	ReadAt      int64  `json:"read_at,omitempty"`
}

type Settings struct {
	ApprovalMode       string `json:"approval_mode"`
	IdleThresholdSec   int    `json:"idle_threshold_sec"`
	DefaultTimeoutSec  int    `json:"default_timeout_sec"`
	TimeoutAction      string `json:"timeout_action"`
	TerminalEnabled    bool   `json:"terminal_enabled"`
	FilesEnabled       bool   `json:"files_enabled"`
	MacAwakeEnabled    bool   `json:"mac_awake_enabled"`
	HighRiskRequireApp bool   `json:"high_risk_require_app"`
}

func DefaultSettings() Settings {
	return Settings{
		ApprovalMode:       ModeAuto,
		IdleThresholdSec:   60,
		DefaultTimeoutSec:  30,
		TimeoutAction:      TimeoutApprove,
		TerminalEnabled:    true,
		FilesEnabled:       true,
		MacAwakeEnabled:    false,
		HighRiskRequireApp: false,
	}
}

type TmuxSession struct {
	Name        string `json:"name"`
	Note        string `json:"note,omitempty"`
	Windows     int    `json:"windows"`
	Attached    bool   `json:"attached"`
	Command     string `json:"command,omitempty"`
	Agent       string `json:"agent,omitempty"`
	Cwd         string `json:"cwd,omitempty"`
	ProjectName string `json:"project_name,omitempty"`
	ActivityAt  int64  `json:"activity_at,omitempty"`
}

type TerminalAudit struct {
	ID          string `json:"id"`
	StreamID    string `json:"stream_id"`
	Mode        string `json:"mode"`
	Target      string `json:"target"`
	OpenedAt    int64  `json:"opened_at"`
	ClosedAt    int64  `json:"closed_at,omitempty"`
	CloseReason string `json:"close_reason,omitempty"`
	ClientIP    string `json:"client_ip,omitempty"`
}

type StatusSnapshot struct {
	MacOnline      bool          `json:"mac_online"`
	MacHostname    string        `json:"mac_hostname,omitempty"`
	MacLastSeenAt  int64         `json:"mac_last_seen_at,omitempty"`
	AppOnlineCount int           `json:"app_online_count"`
	PendingCount   int           `json:"pending_count"`
	UnreadCount    int           `json:"unread_count"`
	Settings       Settings      `json:"settings"`
	TmuxSessions   []TmuxSession `json:"tmux_sessions,omitempty"`
}
