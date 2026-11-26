package usage

// TokenStats holds normalized token counters for a time range or model.
// InputTokens excludes cached reads/writes so TotalTokens = Input + CacheRead + CacheWrite + Output.
type TokenStats struct {
	InputTokens      int64 `json:"input_tokens"`
	CacheReadTokens  int64 `json:"cache_read_tokens"`
	CacheWriteTokens int64 `json:"cache_write_tokens"`
	OutputTokens     int64 `json:"output_tokens"`
	TotalTokens      int64 `json:"total_tokens"`
	Requests         int64 `json:"requests"`
}

func (t *TokenStats) Add(other TokenStats) {
	t.InputTokens += other.InputTokens
	t.CacheReadTokens += other.CacheReadTokens
	t.CacheWriteTokens += other.CacheWriteTokens
	t.OutputTokens += other.OutputTokens
	t.TotalTokens += other.TotalTokens
	t.Requests += other.Requests
}

func (t TokenStats) IsZero() bool {
	return t.InputTokens == 0 && t.CacheReadTokens == 0 && t.CacheWriteTokens == 0 && t.OutputTokens == 0 && t.TotalTokens == 0 && t.Requests == 0
}

// QuotaWindow represents either a 5-hour rolling window or a 7-day weekly window.
type QuotaWindow struct {
	Source        string     `json:"source"`                 // "official" | "local"
	UsedPercent   *float64   `json:"used_percent,omitempty"` // 0..100 when official
	ResetsAt      int64      `json:"resets_at"`              // unix milliseconds (0 if idle / unknown)
	WindowMinutes int        `json:"window_minutes"`         // 300 or 10080
	Tokens        TokenStats `json:"tokens"`
	Expired       bool       `json:"expired,omitempty"`
	ObservedAt    int64      `json:"observed_at,omitempty"` // unix milliseconds
	Note          string     `json:"note,omitempty"`
}

type ModelUsage struct {
	Model string     `json:"model"`
	Today TokenStats `json:"today"`
	Week  TokenStats `json:"week"`
	Month TokenStats `json:"month"`
	Total TokenStats `json:"total"`
}

// AccountUsage represents per-account quota windows and token breakdown for a multi-account agent (e.g. Antigravity).
type AccountUsage struct {
	Account          string       `json:"account"`
	Label            string       `json:"label"`
	PlanType         string       `json:"plan_type,omitempty"`
	ModelName        string       `json:"model_name,omitempty"`
	LastActiveAt     int64        `json:"last_active_at"`
	ObservedAt       int64        `json:"observed_at,omitempty"`
	LogFiles         int          `json:"log_files,omitempty"`
	AuthProfile      string       `json:"auth_profile,omitempty"`
	AuthExpiresAt    int64        `json:"auth_expires_at,omitempty"`
	AuthExpiringSoon bool         `json:"auth_expiring_soon,omitempty"`
	AuthExpired      bool         `json:"auth_expired,omitempty"`
	AuthExpireNote   string       `json:"auth_expire_note,omitempty"`
	Window5h         QuotaWindow  `json:"window_5h"`
	WindowWeekly     QuotaWindow  `json:"window_weekly"`
	ExtraNote        string       `json:"extra_note,omitempty"`
	Today            TokenStats   `json:"today"`
	FiveHour         TokenStats   `json:"five_hour"`
	Week             TokenStats   `json:"week"`
	Month            TokenStats   `json:"month"`
	Total            TokenStats   `json:"total"`
	Models           []ModelUsage `json:"models,omitempty"`
}

type AgentUsage struct {
	Agent        string         `json:"agent"`
	DisplayName  string         `json:"display_name"`
	PlanType     string         `json:"plan_type,omitempty"`
	Installed    bool           `json:"installed"`
	LogFiles     int            `json:"log_files"`
	LogBytes     int64          `json:"log_bytes"`
	LastActiveAt int64          `json:"last_active_at"`
	HistoryNote  string         `json:"history_note,omitempty"`
	Today        TokenStats     `json:"today"`
	FiveHour     TokenStats     `json:"five_hour"`
	Week         TokenStats     `json:"week"`
	Month        TokenStats     `json:"month"`
	Total        TokenStats     `json:"total"`
	Window5h     QuotaWindow    `json:"window_5h"`
	WindowWeekly QuotaWindow    `json:"window_weekly"`
	Models       []ModelUsage   `json:"models"`
	Accounts     []AccountUsage `json:"accounts,omitempty"`
}

type DailyPoint struct {
	Date   string `json:"date"`
	Total  int64  `json:"total"`
	Claude int64  `json:"claude"`
	Codex  int64  `json:"codex"`
	Agy    int64  `json:"agy"`
}

type ScanMeta struct {
	ScanMs       int64  `json:"scan_ms"`
	FilesTotal   int    `json:"files_total"`
	FilesScanned int    `json:"files_scanned"`
	FilesCached  int    `json:"files_cached"`
	CachePath    string `json:"cache_path"`
	CacheBytes   int64  `json:"cache_bytes"`
}

type Summary struct {
	GeneratedAt int64        `json:"generated_at"`
	Timezone    string       `json:"timezone"`
	Hostname    string       `json:"hostname"`
	WebURL      string       `json:"web_url,omitempty"`
	Today       TokenStats   `json:"today"`
	FiveHour    TokenStats   `json:"five_hour"`
	Week        TokenStats   `json:"week"`
	Month       TokenStats   `json:"month"`
	Total       TokenStats   `json:"total"`
	Agents      []AgentUsage `json:"agents"`
	Daily       []DailyPoint `json:"daily"`
	Meta        ScanMeta     `json:"meta"`
}

// Options configures directories and behavior for the usage engine (useful for testing).
type Options struct {
	ClaudeDir       string
	ClaudeStatsPath string
	CodexDir        string
	AgyDir          string
	AgyQuotaPath    string
	AgyProfilesPath string
	CachePath       string
	Hostname        string
	WebURL          string
	ProxyURL        string
	DisableNetwork  bool
}
