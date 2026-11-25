package proto

type Approval struct {
	ID             string   `json:"id,omitempty"`
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
	Status         string   `json:"status,omitempty"`
	DecisionReason string   `json:"decision_reason,omitempty"`
	DecisionScope  string   `json:"decision_scope,omitempty"`
	DecidedBy      string   `json:"decided_by,omitempty"`
	CreatedAt      int64    `json:"created_at,omitempty"`
	ExpiresAt      int64    `json:"expires_at,omitempty"`
	DecidedAt      int64    `json:"decided_at,omitempty"`
}

type Message struct {
	ID          string `json:"id,omitempty"`
	Agent       string `json:"agent"`
	EventType   string `json:"event_type"`
	Priority    string `json:"priority"`
	Title       string `json:"title"`
	Body        string `json:"body"`
	TaskTitle   string `json:"task_title,omitempty"`
	ProjectName string `json:"project_name,omitempty"`
	TmuxSession string `json:"tmux_session,omitempty"`
	ApprovalID  string `json:"approval_id,omitempty"`
	CreatedAt   int64  `json:"created_at,omitempty"`
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

type TmuxSession struct {
	Name        string `json:"name"`
	Windows     int    `json:"windows"`
	Attached    bool   `json:"attached"`
	Command     string `json:"command,omitempty"`
	Agent       string `json:"agent,omitempty"`
	Cwd         string `json:"cwd,omitempty"`
	ProjectName string `json:"project_name,omitempty"`
	ActivityAt  int64  `json:"activity_at,omitempty"`
}

// Local IPC request/response between ai-audit-hook and ai-audit-agent over Unix socket.
type HookApprovalReq struct {
	Approval   Approval `json:"approval"`
	TimeoutSec int      `json:"timeout_sec"`
}

type HookMessageReq struct {
	Message Message `json:"message"`
}
