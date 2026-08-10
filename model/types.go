package model

import (
	"context"
	"net/http"
	"time"
)

// Provider type constants
const (
	ProviderTypeOpenAI    = "openai"
	ProviderTypeAnthropic = "anthropic"
)

// Provider represents an upstream API provider
type Provider struct {
	ID           int64     `gorm:"primaryKey;autoIncrement" json:"id"`
	Name         string    `gorm:"type:varchar(255);not null" json:"name"`
	Type         string    `gorm:"type:varchar(20);not null;default:'openai'" json:"type"` // "openai" or "anthropic"
	BaseURL      string    `gorm:"type:varchar(512);not null" json:"base_url"`
	ExtraHeaders string    `gorm:"type:text" json:"extra_headers"` // JSON string
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
}

// KeyStatus constants
const (
	KeyStatusActive      = "active"
	KeyStatusRateLimited = "rate_limited"
	KeyStatusDisabled    = "disabled"
	KeyStatusTesting     = "testing"
)

// RecoveryStrategy constants
const (
	RecoveryImmediate = "immediate"
	RecoveryLazy      = "lazy"
)

// Key represents an API key belonging to a provider
type Key struct {
	ID               int64      `gorm:"primaryKey;autoIncrement" json:"id"`
	ProviderID       int64      `gorm:"not null;index" json:"provider_id"`
	Name             string     `gorm:"type:varchar(255)" json:"name"`
	KeyValue         string     `gorm:"type:varchar(1024);not null" json:"key_value"`
	Status           string     `gorm:"type:varchar(20);default:'active'" json:"status"`
	RecoveryStrategy string     `gorm:"type:varchar(20);default:'lazy'" json:"recovery_strategy"`
	RateLimitedUntil *time.Time `json:"rate_limited_until"`
	DisabledReason   string     `gorm:"type:varchar(512)" json:"disabled_reason"`
	RPMLimit         int64      `gorm:"default:0" json:"rpm_limit"`                             // requests per minute
	TPMLimit         int64      `gorm:"default:0" json:"tpm_limit"`                             // tokens per minute
	RP5hLimit        int64      `gorm:"default:0" json:"rp5h_limit"`                            // 5-hour limit
	RP5hMetric       string     `gorm:"type:varchar(10);default:'requests'" json:"rp5h_metric"` // requests|tokens
	RPDLimit         int64      `gorm:"default:0" json:"rpd_limit"`                             // daily limit
	RPDMetric        string     `gorm:"type:varchar(10);default:'requests'" json:"rpd_metric"`
	RPWLimit         int64      `gorm:"default:0" json:"rpw_limit"` // weekly limit
	RPWMetric        string     `gorm:"type:varchar(10);default:'requests'" json:"rpw_metric"`
	RPMLimitMonth    int64      `gorm:"default:0" json:"rpm_month_limit"` // monthly limit
	RPMMetric        string     `gorm:"type:varchar(10);default:'requests'" json:"rpm_metric"`
	// SortOrder is the caller-priority within the provider group (0 = called
	// first). It coexists with the recovery strategy: immediate keys are
	// always preferred over lazy keys, and within each strategy keys are
	// tried in sort_order.
	SortOrder int64     `gorm:"default:0" json:"sort_order"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`

	// Relations
	Provider Provider `gorm:"foreignKey:ProviderID" json:"provider,omitempty"`
}

// ModelGroup represents a logical group of models that share routing rules
type ModelGroup struct {
	ID         int64     `gorm:"primaryKey;autoIncrement" json:"id"`
	GroupID    string    `gorm:"type:varchar(255);not null;uniqueIndex" json:"group_id"` // The model name clients send
	Name       string    `gorm:"type:varchar(255);not null" json:"name"`
	Enabled    bool      `json:"enabled"`                      // defaulted to true by the create handler
	RetryTimes int       `gorm:"default:0" json:"retry_times"` // 0 = inherit global server.retry_times
	// ExtraParams is a JSON object merged into every forwarded request body
	// for this group. Client-sent keys are OVERWRITTEN (extra params win),
	// so e.g. {"temperature": 0.2} pins the sampling temperature.
	ExtraParams string `gorm:"type:text" json:"extra_params"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

// Route maps a ModelGroup to a Provider
type Route struct {
	ID           int64     `gorm:"primaryKey;autoIncrement" json:"id"`
	ModelGroupID int64     `gorm:"not null;index" json:"model_group_id"`
	ProviderID   int64     `gorm:"not null;index" json:"provider_id"`
	TargetModel  string    `gorm:"type:varchar(255)" json:"target_model"` // NULL = use incoming model name
	Priority     int       `gorm:"default:1" json:"priority"`             // Lower = higher priority
	Weight       int       `gorm:"default:10" json:"weight"`
	Enabled      bool      `json:"enabled"` // defaulted to true by the create handler
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`

	ModelGroup ModelGroup `gorm:"foreignKey:ModelGroupID" json:"model_group,omitempty"`
	Provider   Provider   `gorm:"foreignKey:ProviderID" json:"provider,omitempty"`
}

// WindowType constants for rate limiting
type WindowType string

const (
	WindowRPM  WindowType = "rpm"  // 60 seconds
	WindowTPM  WindowType = "tpm"  // 60 seconds (token count)
	WindowRP5h WindowType = "rp5h" // 5 hours
	WindowRPD  WindowType = "rpd"  // 24 hours
	WindowRPW  WindowType = "rpw"  // 7 days
	WindowRPMo WindowType = "rpmo" // 30 days (monthly)
)

// WindowConfig defines the bucket configuration for a window type
type WindowConfig struct {
	WindowType     WindowType
	BucketCount    int
	BucketSize     time.Duration
	WindowDuration time.Duration
}

// GetWindowConfigs returns configuration for all window types
func GetWindowConfigs() []WindowConfig {
	return []WindowConfig{
		{WindowRPM, 60, time.Second, time.Minute},
		{WindowTPM, 60, time.Second, time.Minute},
		{WindowRP5h, 60, 5 * time.Minute, 5 * time.Hour},
		{WindowRPD, 24, time.Hour, 24 * time.Hour},
		{WindowRPW, 7, 24 * time.Hour, 7 * 24 * time.Hour},
		{WindowRPMo, 30, 24 * time.Hour, 30 * 24 * time.Hour},
	}
}

// Consumption records usage per key per hour
type Consumption struct {
	ID               int64     `gorm:"primaryKey;autoIncrement" json:"id"`
	KeyID            int64     `gorm:"not null;index:idx_key_hour,unique" json:"key_id"`
	HourBucket       time.Time `gorm:"not null;index:idx_key_hour,unique" json:"hour_bucket"` // truncated to hour
	ModelName        string    `gorm:"type:varchar(255);default:'';index" json:"model_name"`  // model actually served (after route target resolution)
	RequestCount     int64     `gorm:"default:0" json:"request_count"`
	InputTokens      int64     `gorm:"default:0" json:"input_tokens"`
	OutputTokens     int64     `gorm:"default:0" json:"output_tokens"`
	CacheHitTokens   int64     `gorm:"default:0" json:"cache_hit_tokens"`
	CacheWriteTokens int64     `gorm:"default:0" json:"cache_write_tokens"`
	CostUSD          float64   `gorm:"default:0" json:"cost_usd"`
	Key              Key       `gorm:"foreignKey:KeyID" json:"key,omitempty"`
}

// Pricing defines per-model token pricing. Rates are per 1,000,000 tokens
// (industry convention), stored directly in USD.
type Pricing struct {
	ID               int64     `gorm:"primaryKey;autoIncrement" json:"id"`
	ModelName        string    `gorm:"type:varchar(255);not null;uniqueIndex" json:"model_name"`
	PromptPer1M      float64   `gorm:"default:0" json:"prompt_per_1m"`
	CompletionPer1M  float64   `gorm:"default:0" json:"completion_per_1m"`
	CacheReadPer1M   float64   `gorm:"default:0" json:"cache_read_per_1m"`
	CacheWritePer1M  float64   `gorm:"default:0" json:"cache_write_per_1m"`
	CreatedAt        time.Time `json:"created_at"`
	UpdatedAt        time.Time `json:"updated_at"`
}

// Setting stores key-value configuration
type Setting struct {
	Key   string `gorm:"primaryKey;type:varchar(255)" json:"key"`
	Value string `gorm:"type:text" json:"value"`
}

// Predefined setting keys
const (
	SettingPort        = "server.port"
	SettingAuthToken   = "server.auth_token"
	SettingRetryTimes  = "server.retry_times"
	SettingHealthCheck = "server.health_check_interval"
)

// Default settings
const (
	DefaultPort        = "9998"
	DefaultAuthToken   = ""
	DefaultRetryTimes  = "3"
	DefaultHealthCheck = "120"
)

// RequestMetadata holds information about an incoming API request
type RequestMetadata struct {
	Format      string      // "openai" or "anthropic"
	Model       string      // Model name from request body
	Stream      bool        // Whether streaming is requested
	RequestPath string      // Original URL path
	RequestBody []byte      // Raw request body
	Headers     http.Header // Incoming request headers for forwarding
	TargetModel string      // Model name after route resolution
	// ExtraParams is the model group's configured JSON object to merge into
	// the request body (client keys overwritten).
	ExtraParams string
	// Ctx is the client request's context: when the downstream client
	// disconnects, the upstream fetch is cancelled instead of stalling for
	// the full client timeout.
	Ctx context.Context
}

// RelayResult holds the result of a relay operation
type RelayResult struct {
	Success      bool
	StatusCode   int
	ResponseBody []byte
	Tokens       *TokenUsage
	Error        error
}

// TokenUsage holds token consumption details
type TokenUsage struct {
	PromptTokens     int64
	CompletionTokens int64
	CacheHitTokens   int64
	CacheWriteTokens int64
	TotalTokens      int64
	// Format is the upstream format the usage was parsed from ("openai" or
	// "anthropic"). OpenAI's prompt_tokens INCLUDES cached tokens (billing
	// must subtract them); Anthropic's input_tokens EXCLUDES them.
	Format string
}
