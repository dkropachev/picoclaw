package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/caarlos0/env/v11"

	"github.com/sipeed/picoclaw/pkg"
	"github.com/sipeed/picoclaw/pkg/fileutil"
	"github.com/sipeed/picoclaw/pkg/logger"
	providercommon "github.com/sipeed/picoclaw/pkg/providers/common"
	"github.com/sipeed/picoclaw/pkg/providers/protocoltypes"
)

// rrCounter is a global counter for round-robin load balancing across models.
var rrCounter atomic.Uint64

// CurrentVersion is the latest config schema version
const CurrentVersion = 6

// ErrNoModelConfigured is returned when model resolution is requested without
// a configured model alias.
var ErrNoModelConfigured = protocoltypes.ErrNoModelConfigured

// ErrModelAliasDisabled is returned when an alias is explicitly unavailable
// for the selected concrete account.
var ErrModelAliasDisabled = errors.New("model alias disabled for account")

func init() {
	initChannel()
}

// Config is the current config structure with version support.
type Config struct {
	// Config schema version for migration.
	Version   int             `json:"version"             yaml:"-"`
	Isolation IsolationConfig `json:"isolation,omitempty" yaml:"-"`
	Agents    AgentsConfig    `json:"agents"              yaml:"-"`
	Session   SessionConfig   `json:"session,omitempty"   yaml:"-"`
	Evolution EvolutionConfig `json:"evolution,omitempty" yaml:"-"`
	Channels  ChannelsConfig  `json:"channel_list"        yaml:"channel_list"`
	// ModelList stores runnable provider configurations.
	ModelList SecureModelList `json:"model_list" yaml:"model_list"`
	// ModelAliases map stable user-facing names to concrete upstream models.
	ModelAliases []ModelAliasConfig `json:"model_aliases" yaml:"-"`
	// AccountRouters select one or more accounts through a static graph.
	AccountRouters AccountRouterList `json:"account_routers" yaml:"account_routers"`
	// ModelRouters route a chat model alias to one of several configured model aliases.
	ModelRouters ModelRouterList   `json:"model_routers"       yaml:"model_routers"`
	Gateway      GatewayConfig     `json:"gateway"             yaml:"-"`
	Events       EventsConfig      `json:"events,omitempty"    yaml:"events,omitempty"`
	PRLifecycle  PRLifecycleConfig `json:"development"         yaml:"-"`
	Workflows    WorkflowsConfig   `json:"workflows,omitempty" yaml:"-"`
	// GitWorkspaces controls the inventory of local git checkouts reused by agent sessions.
	GitWorkspaces GitWorkspacesConfig `json:"git_workspaces,omitempty" yaml:"-"`
	Hooks         HooksConfig         `json:"hooks,omitempty"          yaml:"-"`
	Tools         ToolsConfig         `json:"tools"                    yaml:",inline"`
	Heartbeat     HeartbeatConfig     `json:"heartbeat"                yaml:"-"`
	Devices       DevicesConfig       `json:"devices"                  yaml:"-"`
	Voice         VoiceConfig         `json:"voice"                    yaml:"-"`
	// BuildInfo contains build-time version information
	BuildInfo BuildInfo `json:"build_info,omitempty" yaml:"-"`

	// cache for sensitive values and compiled regex (computed once)
	sensitiveCache *SensitiveDataCache
}

type EvolutionConfig struct {
	Enabled         bool     `json:"enabled,omitempty"`
	Mode            string   `json:"mode,omitempty"`
	StateDir        string   `json:"state_dir,omitempty"`
	MinTaskCount    int      `json:"min_task_count,omitempty"`
	MinSuccessRatio float64  `json:"min_success_ratio,omitempty"`
	ColdPathTrigger string   `json:"cold_path_trigger,omitempty"`
	ColdPathTimes   []string `json:"cold_path_times,omitempty"`
	// Deprecated: use MinTaskCount.
	MinCaseCount int `json:"min_case_count,omitempty"`
	// Deprecated: use MinSuccessRatio.
	MinSuccessRate float64 `json:"min_success_rate,omitempty"`
}

type WorkflowsConfig struct {
	Enabled               bool   `json:"enabled"                 env:"PICOCLAW_WORKFLOWS_ENABLED"`
	DefinitionsDir        string `json:"definitions_dir"         env:"PICOCLAW_WORKFLOWS_DEFINITIONS_DIR"`
	MaxConcurrentRuns     int    `json:"max_concurrent_runs"     env:"PICOCLAW_WORKFLOWS_MAX_CONCURRENT_RUNS"`
	DefaultTimeoutSeconds int    `json:"default_timeout_seconds" env:"PICOCLAW_WORKFLOWS_DEFAULT_TIMEOUT_SECONDS"`
	MaxCallDepth          int    `json:"max_call_depth"          env:"PICOCLAW_WORKFLOWS_MAX_CALL_DEPTH"`
	RetentionDays         int    `json:"retention_days"          env:"PICOCLAW_WORKFLOWS_RETENTION_DAYS"`
}

const (
	DefaultWorkflowMaxCallDepth      = 4
	DefaultWorkflowMaxConcurrentRuns = 4
	DefaultWorkflowTimeoutSeconds    = 5 * 60
	DefaultWorkflowRetentionDays     = 30
	MaxWorkflowMaxCallDepth          = 64
	MaxWorkflowMaxConcurrentRuns     = 1024
	MaxWorkflowDefaultTimeoutSeconds = 31 * 24 * 60 * 60
	MaxWorkflowRetentionDays         = 10 * 365
)

const (
	DefaultGitWorkspaceMaxTotalSizeBytes       int64 = 20 * 1024 * 1024 * 1024
	DefaultGitWorkspaceIgnoredCleanupDelaySecs       = 24 * 60 * 60
	DefaultGitWorkspaceDropDelaySecs                 = 30 * 24 * 60 * 60
)

type GitWorkspacesConfig struct {
	RootDir                    string `json:"root_dir,omitempty"                      env:"PICOCLAW_GIT_WORKSPACES_ROOT_DIR"`
	MaxTotalSizeBytes          int64  `json:"max_total_size_bytes,omitempty"          env:"PICOCLAW_GIT_WORKSPACES_MAX_TOTAL_SIZE_BYTES"`
	IgnoredCleanupDelaySeconds int    `json:"ignored_cleanup_delay_seconds,omitempty" env:"PICOCLAW_GIT_WORKSPACES_IGNORED_CLEANUP_DELAY_SECONDS"`
	DropDelaySeconds           int    `json:"drop_delay_seconds,omitempty"            env:"PICOCLAW_GIT_WORKSPACES_DROP_DELAY_SECONDS"`
}

func (c GitWorkspacesConfig) EffectiveRootDir(defaultWorkspace string) string {
	dir := strings.TrimSpace(c.RootDir)
	if dir != "" {
		return expandHome(dir)
	}
	defaultWorkspace = strings.TrimSpace(defaultWorkspace)
	if defaultWorkspace == "" {
		defaultWorkspace = filepath.Join(GetHome(), pkg.WorkspaceName)
	}
	return filepath.Join(expandHome(defaultWorkspace), ".git-workspaces")
}

func (c GitWorkspacesConfig) EffectiveMaxTotalSizeBytes() int64 {
	if c.MaxTotalSizeBytes > 0 {
		return c.MaxTotalSizeBytes
	}
	return DefaultGitWorkspaceMaxTotalSizeBytes
}

func (c GitWorkspacesConfig) EffectiveIgnoredCleanupDelay() time.Duration {
	if c.IgnoredCleanupDelaySeconds > 0 {
		return time.Duration(c.IgnoredCleanupDelaySeconds) * time.Second
	}
	return time.Duration(DefaultGitWorkspaceIgnoredCleanupDelaySecs) * time.Second
}

func (c GitWorkspacesConfig) EffectiveDropDelay() time.Duration {
	if c.DropDelaySeconds > 0 {
		return time.Duration(c.DropDelaySeconds) * time.Second
	}
	return time.Duration(DefaultGitWorkspaceDropDelaySecs) * time.Second
}

func (c WorkflowsConfig) EffectiveMaxCallDepth() int {
	if c.MaxCallDepth > 0 {
		if c.MaxCallDepth > MaxWorkflowMaxCallDepth {
			return MaxWorkflowMaxCallDepth
		}
		return c.MaxCallDepth
	}
	return DefaultWorkflowMaxCallDepth
}

func (c WorkflowsConfig) EffectiveMaxConcurrentRuns() int {
	if c.MaxConcurrentRuns > 0 {
		if c.MaxConcurrentRuns > MaxWorkflowMaxConcurrentRuns {
			return MaxWorkflowMaxConcurrentRuns
		}
		return c.MaxConcurrentRuns
	}
	return DefaultWorkflowMaxConcurrentRuns
}

func (c WorkflowsConfig) EffectiveDefaultTimeout() time.Duration {
	if c.DefaultTimeoutSeconds <= 0 {
		return time.Duration(DefaultWorkflowTimeoutSeconds) * time.Second
	}
	if c.DefaultTimeoutSeconds > MaxWorkflowDefaultTimeoutSeconds {
		return time.Duration(MaxWorkflowDefaultTimeoutSeconds) * time.Second
	}
	return time.Duration(c.DefaultTimeoutSeconds) * time.Second
}

func (c WorkflowsConfig) EffectiveDefinitionsDir() string {
	dir := strings.TrimSpace(c.DefinitionsDir)
	if dir == "" {
		return "workflows"
	}
	return dir
}

func (c WorkflowsConfig) EffectiveRetentionDays() int {
	if c.RetentionDays > 0 {
		if c.RetentionDays > MaxWorkflowRetentionDays {
			return MaxWorkflowRetentionDays
		}
		return c.RetentionDays
	}
	return DefaultWorkflowRetentionDays
}

func (c EvolutionConfig) MarshalJSON() ([]byte, error) {
	out := struct {
		Enabled         bool     `json:"enabled,omitempty"`
		Mode            string   `json:"mode,omitempty"`
		StateDir        string   `json:"state_dir,omitempty"`
		MinTaskCount    int      `json:"min_task_count,omitempty"`
		MinSuccessRatio float64  `json:"min_success_ratio,omitempty"`
		ColdPathTrigger string   `json:"cold_path_trigger,omitempty"`
		ColdPathTimes   []string `json:"cold_path_times,omitempty"`
	}{
		Enabled:         c.Enabled,
		Mode:            c.Mode,
		StateDir:        c.StateDir,
		MinTaskCount:    c.EffectiveMinTaskCount(),
		MinSuccessRatio: c.EffectiveMinSuccessRatio(),
		ColdPathTrigger: strings.TrimSpace(c.ColdPathTrigger),
		ColdPathTimes:   c.EffectiveColdPathTimes(),
	}
	if !out.Enabled {
		out.Mode = ""
		out.ColdPathTrigger = ""
		out.ColdPathTimes = nil
	}
	return json.Marshal(out)
}

func (c EvolutionConfig) EffectiveMode() string {
	if !c.Enabled {
		return ""
	}
	switch strings.ToLower(strings.TrimSpace(c.Mode)) {
	case "draft":
		return "draft"
	case "apply":
		return "apply"
	case "", "observe":
		return "observe"
	default:
		return "observe"
	}
}

func (c EvolutionConfig) RunsColdPathAutomatically() bool {
	return c.RunsColdPathAfterTurn() || c.RunsColdPathScheduled()
}

func (c EvolutionConfig) ColdPathTriggerMode() string {
	if c.EffectiveMode() != "draft" && c.EffectiveMode() != "apply" {
		return ""
	}
	switch strings.ToLower(strings.TrimSpace(c.ColdPathTrigger)) {
	case "", "after_turn":
		return "after_turn"
	case "scheduled":
		return "scheduled"
	case "manual", "none", "off":
		return "manual"
	default:
		return "after_turn"
	}
}

func (c EvolutionConfig) RunsColdPathAfterTurn() bool {
	return c.ColdPathTriggerMode() == "after_turn"
}

func (c EvolutionConfig) RunsColdPathScheduled() bool {
	return c.ColdPathTriggerMode() == "scheduled"
}

func (c EvolutionConfig) EffectiveMinTaskCount() int {
	if c.MinTaskCount > 0 {
		return c.MinTaskCount
	}
	if c.MinCaseCount > 0 {
		return c.MinCaseCount
	}
	return 2
}

func (c EvolutionConfig) EffectiveMinSuccessRatio() float64 {
	if c.MinSuccessRatio > 0 {
		return c.MinSuccessRatio
	}
	if c.MinSuccessRate > 0 {
		return c.MinSuccessRate
	}
	return 0.7
}

func (c EvolutionConfig) EffectiveColdPathTimes() []string {
	out := make([]string, 0, len(c.ColdPathTimes))
	for _, value := range c.ColdPathTimes {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		out = append(out, value)
	}
	return out
}

func (c EvolutionConfig) AutoAppliesDrafts() bool {
	return c.EffectiveMode() == "apply"
}

// IsolationConfig controls subprocess isolation for commands started by PicoClaw.
// It is applied by the isolation package rather than by sandboxing the main process.
type IsolationConfig struct {
	Enabled              bool         `json:"enabled,omitempty"`
	ExposePaths          []ExposePath `json:"expose_paths,omitempty"`
	EnvironmentAllowlist []string     `json:"environment_allowlist"`
}

// ExposePath describes a host path that should remain visible inside the isolated
// child-process environment. This is currently implemented on Linux only.
type ExposePath struct {
	Source string `json:"source"`
	Target string `json:"target,omitempty"`
	Mode   string `json:"mode"`
}

// FilterSensitiveData filters sensitive values from content before sending to LLM.
// This prevents the LLM from seeing its own credentials.
// Uses strings.Replacer for O(n+m) performance (computed once per SecurityConfig).
// Short content (below FilterMinLength) is returned unchanged for performance.
func (c *Config) FilterSensitiveData(content string) string {
	if c == nil {
		return content
	}
	// Check if filtering is enabled (default: true)
	if !c.Tools.IsFilterSensitiveDataEnabled() {
		return content
	}
	// Fast path: skip filtering for short content
	if len(content) < c.Tools.GetFilterMinLength() {
		return content
	}
	return c.SensitiveDataReplacer().Replace(content)
}

type HooksConfig struct {
	Enabled   bool                         `json:"enabled"`
	Defaults  HookDefaultsConfig           `json:"defaults,omitempty"`
	Builtins  map[string]BuiltinHookConfig `json:"builtins,omitempty"`
	Processes map[string]ProcessHookConfig `json:"processes,omitempty"`
}

type HookDefaultsConfig struct {
	ObserverTimeoutMS    int `json:"observer_timeout_ms,omitempty"`
	InterceptorTimeoutMS int `json:"interceptor_timeout_ms,omitempty"`
	ApprovalTimeoutMS    int `json:"approval_timeout_ms,omitempty"`
}

type BuiltinHookConfig struct {
	Enabled  bool            `json:"enabled"`
	Priority int             `json:"priority,omitempty"`
	Config   json.RawMessage `json:"config,omitempty"`
}

type ProcessHookConfig struct {
	Enabled   bool              `json:"enabled"`
	Priority  int               `json:"priority,omitempty"`
	Trusted   bool              `json:"trusted,omitempty"`
	Transport string            `json:"transport,omitempty"`
	Command   []string          `json:"command,omitempty"`
	Dir       string            `json:"dir,omitempty"`
	Env       map[string]string `json:"env,omitempty"`
	Observe   []string          `json:"observe,omitempty"`
	Intercept []string          `json:"intercept,omitempty"`
}

// BuildInfo contains build-time version information
type BuildInfo struct {
	Version   string `json:"version"`
	GitCommit string `json:"git_commit"`
	BuildTime string `json:"build_time"`
	GoVersion string `json:"go_version"`
}

// MarshalJSON implements custom JSON marshaling for Config
// to omit providers section when empty and session when empty.
func (c *Config) MarshalJSON() ([]byte, error) {
	type Alias Config
	aux := &struct {
		Session *SessionConfig `json:"session,omitempty"`
		*Alias
	}{
		Alias: (*Alias)(c),
	}

	if len(c.Session.Dimensions) > 0 || len(c.Session.IdentityLinks) > 0 ||
		c.Session.DmScope != "" {
		sessionCfg := c.Session
		aux.Session = &sessionCfg
	}

	return json.Marshal(aux)
}

type AgentsConfig struct {
	Defaults AgentDefaults   `json:"defaults"`
	List     []AgentConfig   `json:"list,omitempty"`
	Dispatch *DispatchConfig `json:"dispatch,omitempty"`
}

// AgentModelConfig supports both string and structured model config.
// String format: "gpt-4" (just primary, no fallbacks)
// Object format: {"primary": "gpt-4", "fallbacks": ["claude-haiku"]}
type AgentModelConfig struct {
	Primary   string   `json:"primary,omitempty"`
	Fallbacks []string `json:"fallbacks,omitempty"`
}

func (m *AgentModelConfig) UnmarshalJSON(data []byte) error {
	var s string
	if err := json.Unmarshal(data, &s); err == nil {
		m.Primary = s
		m.Fallbacks = nil
		return nil
	}
	type raw struct {
		Primary   string   `json:"primary"`
		Fallbacks []string `json:"fallbacks"`
	}
	var r raw
	if err := json.Unmarshal(data, &r); err != nil {
		return err
	}
	m.Primary = r.Primary
	m.Fallbacks = r.Fallbacks
	return nil
}

func (m AgentModelConfig) MarshalJSON() ([]byte, error) {
	if m.Fallbacks == nil {
		return json.Marshal(m.Primary)
	}
	type raw struct {
		Primary   string   `json:"primary,omitempty"`
		Fallbacks []string `json:"fallbacks"`
	}
	return json.Marshal(raw{Primary: m.Primary, Fallbacks: m.Fallbacks})
}

type AgentConfig struct {
	ID         string            `json:"id"`
	Default    bool              `json:"default,omitempty"`
	Name       string            `json:"name,omitempty"`
	Workspace  string            `json:"workspace,omitempty"`
	AccountRef string            `json:"account_ref,omitempty"`
	Model      *AgentModelConfig `json:"model,omitempty"`
	Skills     []string          `json:"skills,omitempty"`
	Subagents  *SubagentsConfig  `json:"subagents,omitempty"`
}

type SubagentsConfig struct {
	AllowAgents []string          `json:"allow_agents,omitempty"`
	Model       *AgentModelConfig `json:"model,omitempty"`
}

type DispatchConfig struct {
	Rules []DispatchRule `json:"rules,omitempty"`
}

type DispatchRule struct {
	Name              string           `json:"name,omitempty"`
	Agent             string           `json:"agent"`
	When              DispatchSelector `json:"when"`
	SessionDimensions []string         `json:"session_dimensions,omitempty"`
}

type DispatchSelector struct {
	Channel   string `json:"channel,omitempty"`
	Account   string `json:"account,omitempty"`
	Space     string `json:"space,omitempty"`
	Chat      string `json:"chat,omitempty"`
	Topic     string `json:"topic,omitempty"`
	Sender    string `json:"sender,omitempty"`
	Mentioned *bool  `json:"mentioned,omitempty"`
}

type SessionConfig struct {
	Dimensions    []string            `json:"dimensions,omitempty"`
	IdentityLinks map[string][]string `json:"identity_links,omitempty"`
	DmScope       string              `json:"dm_scope,omitempty"`
}

// ApplyDmScope translates the user-facing dm_scope value into the internal
// dimensions array that the routing layer consumes. It is a no-op when
// DmScope is empty or when Dimensions is already set (explicit Dimensions
// take precedence over the derived value).
func (s *SessionConfig) ApplyDmScope() {
	if s.DmScope == "" || len(s.Dimensions) > 0 {
		return
	}
	switch s.DmScope {
	case "per-channel-peer":
		s.Dimensions = []string{"chat", "sender"}
	case "per-channel":
		s.Dimensions = []string{"chat"}
	case "per-peer":
		s.Dimensions = []string{"sender"}
	case "global":
		s.Dimensions = nil
	}
}

// DeriveDmScope sets DmScope based on Dimensions when DmScope is empty.
// This handles legacy/fresh configs that only have explicit Dimensions
// without a corresponding DmScope value, ensuring the API response always
// includes a dm_scope that matches the actual runtime dimensions.
func (s *SessionConfig) DeriveDmScope() {
	if s.DmScope != "" || len(s.Dimensions) == 0 {
		return
	}
	switch {
	case slices.Equal(s.Dimensions, []string{"chat", "sender"}):
		s.DmScope = "per-channel-peer"
	case slices.Equal(s.Dimensions, []string{"chat"}):
		s.DmScope = "per-channel"
	case slices.Equal(s.Dimensions, []string{"sender"}):
		s.DmScope = "per-peer"
	}
	// Dimensions not matching any known scope mapping (custom array)
	// is fine — DmScope stays empty and the UI can handle it.
}

// RoutingConfig controls the intelligent model routing feature.
// When enabled, each incoming message is scored against structural features
// (message length, code blocks, tool call history, conversation depth, attachments).
// Messages scoring below Threshold are sent to LightModel; all others use the
// agent's primary model. This reduces cost and latency for simple tasks without
// requiring any keyword matching — all scoring is language-agnostic.
type RoutingConfig struct {
	Enabled    bool    `json:"enabled"`
	LightModel string  `json:"light_model"` // Exact model alias used for simple tasks
	Threshold  float64 `json:"threshold"`   // complexity score in [0,1]; score >= threshold → primary model
}

const (
	AccountRouterProvider                = "router"
	AccountRouterCredentialAccountPrefix = "credential:"
	AccountRouterBlockTypeAccount        = "account"
	AccountRouterBlockTypeLoadBalance    = "load_balance"
	AccountRouterBlockTypeBranch         = "branch"
	AccountRouterStrategyBlind           = "blind"
	AccountRouterStrategyTokensSpent     = "tokens_spent"
	AccountRouterStrategyClosestLimit    = "closest_limit"
	AccountRouterBranchOpGT              = "gt"
	AccountRouterBranchOpGTE             = "gte"
	AccountRouterBranchOpLT              = "lt"
	AccountRouterBranchOpLTE             = "lte"
	AccountRouterBranchOpEQ              = "eq"
	AccountRouterBranchOpNEQ             = "neq"
	AccountRouterMathAdd                 = "add"
	AccountRouterMathSubtract            = "subtract"
	AccountRouterMathMultiply            = "multiply"
	AccountRouterMathDivide              = "divide"
	AccountRouterMathModulo              = "modulo"
	DefaultAccountRouterRefreshInterval  = 60
	defaultAccountRouterMaxFallbackDepth = 64
)

func AccountRouterCredentialAccountID(accountRef string) (string, bool) {
	accountRef = strings.TrimSpace(accountRef)
	if accountRef == "" {
		return "", false
	}
	credentialID, ok := strings.CutPrefix(accountRef, AccountRouterCredentialAccountPrefix)
	if !ok {
		return "", false
	}
	credentialID = strings.ToLower(strings.TrimSpace(credentialID))
	if credentialID == "" {
		return "", false
	}
	return credentialID, true
}

func AccountRouterCredentialAccountProvider(accountRef string) (string, bool) {
	credentialID, ok := AccountRouterCredentialAccountID(accountRef)
	if !ok {
		return "", false
	}
	provider := credentialID
	if prefix, _, hasName := strings.Cut(credentialID, ":"); hasName {
		provider = prefix
	}
	switch provider {
	case "openai",
		"anthropic",
		"google-antigravity",
		"antigravity",
		"github-copilot",
		"copilot":
		return provider, true
	default:
		return "", false
	}
}

type AccountRouterList []AccountRouterConfig

// AccountRouterConfig describes a static account-router graph. Blocks are
// intentionally workflow-like: an entry block points to an account or
// load-balancing block, and each block can fall back to another block.
type AccountRouterConfig struct {
	Name                   string               `json:"name,omitempty"                     yaml:"name,omitempty"`
	Enabled                bool                 `json:"enabled,omitempty"                  yaml:"enabled,omitempty"`
	Entry                  string               `json:"entry,omitempty"                    yaml:"entry,omitempty"`
	RefreshIntervalSeconds int                  `json:"refresh_interval_seconds,omitempty" yaml:"refresh_interval_seconds,omitempty"`
	Blocks                 []AccountRouterBlock `json:"blocks,omitempty"                   yaml:"blocks,omitempty"`
}

type AccountRouterBlock struct {
	ID                     string                  `json:"id"                                 yaml:"id"`
	Type                   string                  `json:"type"                               yaml:"type"`
	Account                string                  `json:"account,omitempty"                  yaml:"account,omitempty"`
	Accounts               []string                `json:"accounts,omitempty"                 yaml:"accounts,omitempty"`
	Fallback               string                  `json:"fallback,omitempty"                 yaml:"fallback,omitempty"`
	Strategy               string                  `json:"strategy,omitempty"                 yaml:"strategy,omitempty"`
	RefreshIntervalSeconds int                     `json:"refresh_interval_seconds,omitempty" yaml:"refresh_interval_seconds,omitempty"`
	Condition              *AccountRouterCondition `json:"condition,omitempty"                yaml:"condition,omitempty"`
	Then                   string                  `json:"then,omitempty"                     yaml:"then,omitempty"`
	Else                   string                  `json:"else,omitempty"                     yaml:"else,omitempty"`
}

type AccountRouterCondition struct {
	Left     AccountRouterExpression `json:"left"`
	Operator string                  `json:"operator"`
	Right    AccountRouterExpression `json:"right"`
}

type AccountRouterExpression struct {
	Account string                   `json:"account,omitempty"`
	Metric  string                   `json:"metric,omitempty"`
	Value   *float64                 `json:"value,omitempty"`
	Op      string                   `json:"op,omitempty"`
	Left    *AccountRouterExpression `json:"left,omitempty"`
	Right   *AccountRouterExpression `json:"right,omitempty"`
}

func (r *AccountRouterConfig) EffectiveRefreshIntervalSeconds() int {
	if r != nil && r.RefreshIntervalSeconds > 0 {
		return r.RefreshIntervalSeconds
	}
	return DefaultAccountRouterRefreshInterval
}

// SubTurnConfig configures the SubTurn execution system.
type SubTurnConfig struct {
	MaxDepth              int `json:"max_depth"               env:"PICOCLAW_AGENTS_DEFAULTS_SUBTURN_MAX_DEPTH"`
	MaxConcurrent         int `json:"max_concurrent"          env:"PICOCLAW_AGENTS_DEFAULTS_SUBTURN_MAX_CONCURRENT"`
	DefaultTimeoutMinutes int `json:"default_timeout_minutes" env:"PICOCLAW_AGENTS_DEFAULTS_SUBTURN_DEFAULT_TIMEOUT_MINUTES"`
	DefaultTokenBudget    int `json:"default_token_budget"    env:"PICOCLAW_AGENTS_DEFAULTS_SUBTURN_DEFAULT_TOKEN_BUDGET"`
	ConcurrencyTimeoutSec int `json:"concurrency_timeout_sec" env:"PICOCLAW_AGENTS_DEFAULTS_SUBTURN_CONCURRENCY_TIMEOUT_SEC"`
}

type ToolFeedbackConfig struct {
	Enabled          bool `json:"enabled"           env:"PICOCLAW_AGENTS_DEFAULTS_TOOL_FEEDBACK_ENABLED"`
	MaxArgsLength    int  `json:"max_args_length"   env:"PICOCLAW_AGENTS_DEFAULTS_TOOL_FEEDBACK_MAX_ARGS_LENGTH"`
	SeparateMessages bool `json:"separate_messages" env:"PICOCLAW_AGENTS_DEFAULTS_TOOL_FEEDBACK_SEPARATE_MESSAGES"`
}

type AgentDefaults struct {
	Workspace                 string             `json:"workspace"                        env:"PICOCLAW_AGENTS_DEFAULTS_WORKSPACE"`
	RestrictToWorkspace       bool               `json:"restrict_to_workspace"            env:"PICOCLAW_AGENTS_DEFAULTS_RESTRICT_TO_WORKSPACE"`
	AllowReadOutsideWorkspace bool               `json:"allow_read_outside_workspace"     env:"PICOCLAW_AGENTS_DEFAULTS_ALLOW_READ_OUTSIDE_WORKSPACE"`
	Provider                  string             `json:"provider"                         env:"PICOCLAW_AGENTS_DEFAULTS_PROVIDER"`
	AccountRef                string             `json:"account_ref,omitempty"            env:"PICOCLAW_AGENTS_DEFAULTS_ACCOUNT_REF"`
	ModelName                 string             `json:"model_name"                       env:"PICOCLAW_AGENTS_DEFAULTS_MODEL_NAME"`
	ModelFallbacks            []string           `json:"model_fallbacks,omitempty"`
	ImageModel                string             `json:"image_model,omitempty"            env:"PICOCLAW_AGENTS_DEFAULTS_IMAGE_MODEL"`
	ImageModelFallbacks       []string           `json:"image_model_fallbacks,omitempty"`
	MaxTokens                 int                `json:"max_tokens"                       env:"PICOCLAW_AGENTS_DEFAULTS_MAX_TOKENS"`
	ContextWindow             int                `json:"context_window,omitempty"         env:"PICOCLAW_AGENTS_DEFAULTS_CONTEXT_WINDOW"`
	Temperature               *float64           `json:"temperature,omitempty"            env:"PICOCLAW_AGENTS_DEFAULTS_TEMPERATURE"`
	MaxToolIterations         int                `json:"max_tool_iterations"              env:"PICOCLAW_AGENTS_DEFAULTS_MAX_TOOL_ITERATIONS"`
	SummarizeMessageThreshold int                `json:"summarize_message_threshold"      env:"PICOCLAW_AGENTS_DEFAULTS_SUMMARIZE_MESSAGE_THRESHOLD"`
	SummarizeTokenPercent     int                `json:"summarize_token_percent"          env:"PICOCLAW_AGENTS_DEFAULTS_SUMMARIZE_TOKEN_PERCENT"`
	MaxMediaSize              int                `json:"max_media_size,omitempty"         env:"PICOCLAW_AGENTS_DEFAULTS_MAX_MEDIA_SIZE"`
	Routing                   *RoutingConfig     `json:"routing,omitempty"`
	SteeringMode              string             `json:"steering_mode,omitempty"          env:"PICOCLAW_AGENTS_DEFAULTS_STEERING_MODE"`      // "one-at-a-time" (default) or "all"
	MaxParallelTurns          int                `json:"max_parallel_turns,omitempty"     env:"PICOCLAW_AGENTS_DEFAULTS_MAX_PARALLEL_TURNS"` // Max concurrent turns (0 or 1 = sequential)
	SubTurn                   SubTurnConfig      `json:"subturn"                                                                                      envPrefix:"PICOCLAW_AGENTS_DEFAULTS_SUBTURN_"`
	ToolFeedback              ToolFeedbackConfig `json:"tool_feedback,omitempty"`
	SplitOnMarker             bool               `json:"split_on_marker"                  env:"PICOCLAW_AGENTS_DEFAULTS_SPLIT_ON_MARKER"` // split messages on <|[SPLIT]|> marker
	ContextManager            string             `json:"context_manager,omitempty"        env:"PICOCLAW_AGENTS_DEFAULTS_CONTEXT_MANAGER"`
	ContextManagerConfig      json.RawMessage    `json:"context_manager_config,omitempty" env:"PICOCLAW_AGENTS_DEFAULTS_CONTEXT_MANAGER_CONFIG"`
	TurnProfile               TurnProfileConfig  `json:"turn_profile,omitempty"`
	MaxLLMRetries             int                `json:"max_llm_retries,omitempty"        env:"PICOCLAW_AGENTS_DEFAULTS_MAX_LLM_RETRIES"`
	LLMRetryBackoffSecs       int                `json:"llm_retry_backoff_secs,omitempty" env:"PICOCLAW_AGENTS_DEFAULTS_LLM_RETRY_BACKOFF_SECS"`
}

const DefaultMaxMediaSize = 20 * 1024 * 1024 // 20 MB

func (d *AgentDefaults) GetMaxMediaSize() int {
	if d.MaxMediaSize > 0 {
		return d.MaxMediaSize
	}
	return DefaultMaxMediaSize
}

// GetToolFeedbackMaxArgsLength returns the max visible text length for tool argument previews.
func (d *AgentDefaults) GetToolFeedbackMaxArgsLength() int {
	if d.ToolFeedback.MaxArgsLength > 0 {
		return d.ToolFeedback.MaxArgsLength
	}
	return 300
}

// IsToolFeedbackEnabled returns true when tool feedback messages should be sent to the chat.
func (d *AgentDefaults) IsToolFeedbackEnabled() bool {
	return d.ToolFeedback.Enabled
}

// IsToolFeedbackSeparateMessagesEnabled returns true when each tool feedback
// update should be sent as its own chat message instead of editing a single
// in-place progress message.
func (d *AgentDefaults) IsToolFeedbackSeparateMessagesEnabled() bool {
	return d.ToolFeedback.SeparateMessages
}

// GetModelName returns the exact model alias selected by the agent defaults.
func (d *AgentDefaults) GetModelName() string {
	return d.ModelName
}

// GroupTriggerConfig controls when the bot responds in group chats.
type GroupTriggerConfig struct {
	MentionOnly bool     `json:"mention_only,omitempty"`
	Prefixes    []string `json:"prefixes,omitempty"`
}

// TypingConfig controls typing indicator behavior (Phase 10).
type TypingConfig struct {
	Enabled bool `json:"enabled,omitempty"`
}

// PlaceholderConfig controls placeholder message behavior (Phase 10).
type PlaceholderConfig struct {
	Enabled bool                `json:"enabled"`
	Text    FlexibleStringSlice `json:"text,omitempty"`
}

// GetRandomText returns a random placeholder text, or default if none set.
func (p *PlaceholderConfig) GetRandomText() string {
	if len(p.Text) == 0 {
		return "Thinking..."
	}
	if len(p.Text) == 1 {
		return p.Text[0]
	}
	idx := rand.Intn(len(p.Text))
	return p.Text[idx]
}

type StreamingConfig struct {
	Enabled         bool `json:"enabled,omitempty"`
	ThrottleSeconds int  `json:"throttle_seconds,omitempty"`
	MinGrowthChars  int  `json:"min_growth_chars,omitempty"`
}

func (c StreamingConfig) IsZero() bool {
	return !c.Enabled && c.ThrottleSeconds == 0 && c.MinGrowthChars == 0
}

func (c StreamingConfig) WithDefaults(throttleSeconds, minGrowthChars int) StreamingConfig {
	if c.Enabled {
		if c.ThrottleSeconds == 0 {
			c.ThrottleSeconds = throttleSeconds
		}
		if c.MinGrowthChars == 0 {
			c.MinGrowthChars = minGrowthChars
		}
	}
	return c
}

type WhatsAppSettings struct {
	BridgeURL        string `json:"bridge_url"         yaml:"-" env:"PICOCLAW_CHANNELS_WHATSAPP_BRIDGE_URL"`
	UseNative        bool   `json:"use_native"         yaml:"-" env:"PICOCLAW_CHANNELS_WHATSAPP_USE_NATIVE"`
	SessionStorePath string `json:"session_store_path" yaml:"-" env:"PICOCLAW_CHANNELS_WHATSAPP_SESSION_STORE_PATH"`
}

type TelegramSettings struct {
	Token             SecureString    `json:"token,omitzero"       yaml:"token,omitempty" env:"PICOCLAW_CHANNELS_TELEGRAM_TOKEN"`
	BaseURL           string          `json:"base_url"             yaml:"-"               env:"PICOCLAW_CHANNELS_TELEGRAM_BASE_URL"`
	Proxy             string          `json:"proxy"                yaml:"-"               env:"PICOCLAW_CHANNELS_TELEGRAM_PROXY"`
	Streaming         StreamingConfig `json:"streaming,omitzero"   yaml:"-"`
	UseMarkdownV2     bool            `json:"use_markdown_v2"      yaml:"-"               env:"PICOCLAW_CHANNELS_TELEGRAM_USE_MARKDOWN_V2"`
	MediaGroupDelayMS int             `json:"media_group_delay_ms" yaml:"-"               env:"PICOCLAW_CHANNELS_TELEGRAM_MEDIA_GROUP_DELAY_MS"`
}

type FeishuSettings struct {
	AppID               string              `json:"app_id"                      yaml:"-"                            env:"PICOCLAW_CHANNELS_FEISHU_APP_ID"`
	AppSecret           SecureString        `json:"app_secret,omitzero"         yaml:"app_secret,omitempty"         env:"PICOCLAW_CHANNELS_FEISHU_APP_SECRET"`
	EncryptKey          SecureString        `json:"encrypt_key,omitzero"        yaml:"encrypt_key,omitempty"        env:"PICOCLAW_CHANNELS_FEISHU_ENCRYPT_KEY"`
	VerificationToken   SecureString        `json:"verification_token,omitzero" yaml:"verification_token,omitempty" env:"PICOCLAW_CHANNELS_FEISHU_VERIFICATION_TOKEN"`
	RandomReactionEmoji FlexibleStringSlice `json:"random_reaction_emoji"       yaml:"-"                            env:"PICOCLAW_CHANNELS_FEISHU_RANDOM_REACTION_EMOJI"`
	IsLark              bool                `json:"is_lark"                     yaml:"-"                            env:"PICOCLAW_CHANNELS_FEISHU_IS_LARK"`
}

type DiscordSettings struct {
	Token       SecureString `json:"token,omitzero" yaml:"token,omitempty" env:"PICOCLAW_CHANNELS_DISCORD_TOKEN"`
	Proxy       string       `json:"proxy"          yaml:"-"               env:"PICOCLAW_CHANNELS_DISCORD_PROXY"`
	MentionOnly bool         `json:"mention_only"   yaml:"-"               env:"PICOCLAW_CHANNELS_DISCORD_MENTION_ONLY"`
}

type MaixCamSettings struct {
	Host string `json:"host" yaml:"-" env:"PICOCLAW_CHANNELS_MAIXCAM_HOST"`
	Port int    `json:"port" yaml:"-" env:"PICOCLAW_CHANNELS_MAIXCAM_PORT"`
}

type QQSettings struct {
	AppID                string       `json:"app_id"                   yaml:"-"                    env:"PICOCLAW_CHANNELS_QQ_APP_ID"`
	AppSecret            SecureString `json:"app_secret,omitzero"      yaml:"app_secret,omitempty" env:"PICOCLAW_CHANNELS_QQ_APP_SECRET"`
	MaxMessageLength     int          `json:"max_message_length"       yaml:"-"                    env:"PICOCLAW_CHANNELS_QQ_MAX_MESSAGE_LENGTH"`
	MaxBase64FileSizeMiB int64        `json:"max_base64_file_size_mib" yaml:"-"                    env:"PICOCLAW_CHANNELS_QQ_MAX_BASE64_FILE_SIZE_MIB"`
	SendMarkdown         bool         `json:"send_markdown"            yaml:"-"                    env:"PICOCLAW_CHANNELS_QQ_SEND_MARKDOWN"`
}

type DingTalkSettings struct {
	ClientID     string       `json:"client_id"              yaml:"-"                       env:"PICOCLAW_CHANNELS_DINGTALK_CLIENT_ID"`
	ClientSecret SecureString `json:"client_secret,omitzero" yaml:"client_secret,omitempty" env:"PICOCLAW_CHANNELS_DINGTALK_CLIENT_SECRET"`
}

type SlackSettings struct {
	BotToken SecureString `json:"bot_token,omitzero" yaml:"bot_token,omitempty" env:"PICOCLAW_CHANNELS_SLACK_BOT_TOKEN"`
	AppToken SecureString `json:"app_token,omitzero" yaml:"app_token,omitempty" env:"PICOCLAW_CHANNELS_SLACK_APP_TOKEN"`
}

type MatrixSettings struct {
	Homeserver         string       `json:"homeserver"                     yaml:"-"                      env:"PICOCLAW_CHANNELS_MATRIX_HOMESERVER"`
	UserID             string       `json:"user_id"                        yaml:"-"                      env:"PICOCLAW_CHANNELS_MATRIX_USER_ID"`
	AccessToken        SecureString `json:"access_token,omitzero"          yaml:"access_token,omitempty" env:"PICOCLAW_CHANNELS_MATRIX_ACCESS_TOKEN"`
	DeviceID           string       `json:"device_id,omitempty"            yaml:"-"`
	JoinOnInvite       bool         `json:"join_on_invite"                 yaml:"-"`
	MessageFormat      string       `json:"message_format,omitempty"       yaml:"-"`
	CryptoDatabasePath string       `json:"crypto_database_path,omitempty" yaml:"-"`
	CryptoPassphrase   string       `json:"crypto_passphrase,omitempty"    yaml:"-"`
}

// DeltaChatSettings configures the Delta Chat channel. Delta Chat is an
// email-based, end-to-end encrypted messenger; PicoClaw talks to a local
// `deltachat-rpc-server` process over JSON-RPC (stdio).
//
// Email is the only required setting. A full address selects an already
// configured account in DataDir; a first-run marker such as "@nine.testrun.org"
// creates a chatmail account and tells the user which full email to save.
// Mailbox credentials stay in the Delta Chat account store. DisplayName and
// AvatarImage are optional profile settings applied on startup. Password remains
// only for legacy PicoClaw-managed email configuration.
type DeltaChatSettings struct {
	Email          string       `json:"email"                     yaml:"-"                  env:"PICOCLAW_CHANNELS_DELTACHAT_EMAIL"`
	Password       SecureString `json:"password,omitzero"         yaml:"password,omitempty" env:"PICOCLAW_CHANNELS_DELTACHAT_PASSWORD"`
	DisplayName    string       `json:"display_name,omitempty"    yaml:"-"                  env:"PICOCLAW_CHANNELS_DELTACHAT_DISPLAY_NAME"`
	AvatarImage    string       `json:"avatar_image,omitempty"    yaml:"-"                  env:"PICOCLAW_CHANNELS_DELTACHAT_AVATAR_IMAGE"`
	DataDir        string       `json:"data_dir,omitempty"        yaml:"-"                  env:"PICOCLAW_CHANNELS_DELTACHAT_DATA_DIR"`
	RPCServerPath  string       `json:"rpc_server_path,omitempty" yaml:"-"                  env:"PICOCLAW_CHANNELS_DELTACHAT_RPC_SERVER_PATH"`
	InviteLink     string       `json:"invite_link,omitempty"     yaml:"-"                  env:"PICOCLAW_CHANNELS_DELTACHAT_INVITE_LINK"`
	AllowCrosspost bool         `json:"allow_crosspost,omitempty" yaml:"-"                  env:"PICOCLAW_CHANNELS_DELTACHAT_ALLOW_CROSSPOST"`
	IMAPServer     string       `json:"imap_server,omitempty"     yaml:"-"`
	IMAPPort       int          `json:"imap_port,omitempty"       yaml:"-"`
	SMTPServer     string       `json:"smtp_server,omitempty"     yaml:"-"`
	SMTPPort       int          `json:"smtp_port,omitempty"       yaml:"-"`
}

type LINESettings struct {
	ChannelSecret      SecureString `json:"channel_secret,omitzero"       yaml:"channel_secret,omitempty"       env:"PICOCLAW_CHANNELS_LINE_CHANNEL_SECRET"`
	ChannelAccessToken SecureString `json:"channel_access_token,omitzero" yaml:"channel_access_token,omitempty" env:"PICOCLAW_CHANNELS_LINE_CHANNEL_ACCESS_TOKEN"`
	WebhookHost        string       `json:"webhook_host"                  yaml:"-"                              env:"PICOCLAW_CHANNELS_LINE_WEBHOOK_HOST"`
	WebhookPort        int          `json:"webhook_port"                  yaml:"-"                              env:"PICOCLAW_CHANNELS_LINE_WEBHOOK_PORT"`
	WebhookPath        string       `json:"webhook_path"                  yaml:"-"                              env:"PICOCLAW_CHANNELS_LINE_WEBHOOK_PATH"`
}

type OneBotSettings struct {
	WSUrl              string       `json:"ws_url"                yaml:"-"                      env:"PICOCLAW_CHANNELS_ONEBOT_WS_URL"`
	AccessToken        SecureString `json:"access_token,omitzero" yaml:"access_token,omitempty" env:"PICOCLAW_CHANNELS_ONEBOT_ACCESS_TOKEN"`
	ReconnectInterval  int          `json:"reconnect_interval"    yaml:"-"                      env:"PICOCLAW_CHANNELS_ONEBOT_RECONNECT_INTERVAL"`
	GroupTriggerPrefix []string     `json:"group_trigger_prefix"  yaml:"-"                      env:"PICOCLAW_CHANNELS_ONEBOT_GROUP_TRIGGER_PREFIX"`
}

type WeComGroupConfig struct {
	AllowFrom FlexibleStringSlice `json:"allow_from,omitempty"`
}

type WeComSettings struct {
	BotID               string          `json:"bot_id"                  yaml:"-"                env:"BOT_ID"`
	Secret              SecureString    `json:"secret,omitzero"         yaml:"secret,omitempty" env:"SECRET"`
	WebSocketURL        string          `json:"websocket_url,omitempty" yaml:"-"                env:"WEBSOCKET_URL"`
	SendThinkingMessage bool            `json:"send_thinking_message"   yaml:"-"                env:"SEND_THINKING_MESSAGE"`
	Streaming           StreamingConfig `json:"streaming,omitzero"      yaml:"-"`
}

func (c *WeComSettings) SetSecret(secret string) {
	c.Secret = *NewSecureString(secret)
}

type WeixinSettings struct {
	Token      SecureString `json:"token,omitzero"       yaml:"token,omitempty" env:"PICOCLAW_CHANNELS_WEIXIN_TOKEN"`
	AccountID  string       `json:"account_id,omitempty" yaml:"-"               env:"PICOCLAW_CHANNELS_WEIXIN_ACCOUNT_ID"`
	BaseURL    string       `json:"base_url"             yaml:"-"               env:"PICOCLAW_CHANNELS_WEIXIN_BASE_URL"`
	CDNBaseURL string       `json:"cdn_base_url"         yaml:"-"               env:"PICOCLAW_CHANNELS_WEIXIN_CDN_BASE_URL"`
	Proxy      string       `json:"proxy"                yaml:"-"               env:"PICOCLAW_CHANNELS_WEIXIN_PROXY"`
}

// SetToken sets the Weixin token and marks it as dirty for security saving
func (c *WeixinSettings) SetToken(token string) {
	c.Token = *NewSecureString(token)
}

type PicoSettings struct {
	Token           SecureString    `json:"token,omitzero"              yaml:"token,omitempty" env:"PICOCLAW_CHANNELS_PICO_TOKEN"`
	AllowTokenQuery bool            `json:"allow_token_query,omitempty" yaml:"-"`
	AllowOrigins    []string        `json:"allow_origins,omitempty"     yaml:"-"`
	Streaming       StreamingConfig `json:"streaming,omitzero"          yaml:"-"`
	PingInterval    int             `json:"ping_interval,omitempty"     yaml:"-"`
	ReadTimeout     int             `json:"read_timeout,omitempty"      yaml:"-"`
	WriteTimeout    int             `json:"write_timeout,omitempty"     yaml:"-"`
	MaxConnections  int             `json:"max_connections,omitempty"   yaml:"-"`
}

// SetToken sets the Pico token and marks it as dirty for security saving
func (c *PicoSettings) SetToken(token string) {
	c.Token = *NewSecureString(token)
}

type PicoClientSettings struct {
	URL          string       `json:"url"                     yaml:"-"               env:"PICOCLAW_CHANNELS_PICO_CLIENT_URL"`
	Token        SecureString `json:"token,omitzero"          yaml:"token,omitempty" env:"PICOCLAW_CHANNELS_PICO_CLIENT_TOKEN"`
	SessionID    string       `json:"session_id,omitempty"    yaml:"-"`
	PingInterval int          `json:"ping_interval,omitempty" yaml:"-"`
	ReadTimeout  int          `json:"read_timeout,omitempty"  yaml:"-"`
}

type IRCSettings struct {
	Server           string              `json:"server"                     yaml:"-"                           env:"PICOCLAW_CHANNELS_IRC_SERVER"`
	TLS              bool                `json:"tls"                        yaml:"-"                           env:"PICOCLAW_CHANNELS_IRC_TLS"`
	Nick             string              `json:"nick"                       yaml:"-"                           env:"PICOCLAW_CHANNELS_IRC_NICK"`
	User             string              `json:"user,omitempty"             yaml:"-"                           env:"PICOCLAW_CHANNELS_IRC_USER"`
	RealName         string              `json:"real_name,omitempty"        yaml:"-"`
	Password         SecureString        `json:"password,omitzero"          yaml:"password,omitempty"          env:"PICOCLAW_CHANNELS_IRC_PASSWORD"`
	NickServPassword SecureString        `json:"nickserv_password,omitzero" yaml:"nickserv_password,omitempty" env:"PICOCLAW_CHANNELS_IRC_NICKSERV_PASSWORD"`
	SASLUser         string              `json:"sasl_user"                  yaml:"-"                           env:"PICOCLAW_CHANNELS_IRC_SASL_USER"`
	SASLPassword     SecureString        `json:"sasl_password,omitzero"     yaml:"sasl_password,omitempty"     env:"PICOCLAW_CHANNELS_IRC_SASL_PASSWORD"`
	Channels         FlexibleStringSlice `json:"channels"                   yaml:"-"                           env:"PICOCLAW_CHANNELS_IRC_CHANNELS"`
	RequestCaps      FlexibleStringSlice `json:"request_caps,omitempty"     yaml:"-"`
}

type VKSettings struct {
	Token   SecureString `json:"token,omitzero" yaml:"token,omitempty" env:"PICOCLAW_CHANNELS_VK_TOKEN"`
	GroupID int          `json:"group_id"       yaml:"-"               env:"PICOCLAW_CHANNELS_VK_GROUP_ID"`
}

func (c *VKSettings) SetToken(token string) {
	c.Token = *NewSecureString(token)
}

// TeamsWebhookSettings configures the output-only Microsoft Teams webhook channel.
// Multiple webhook targets can be configured and selected via ChatID at send time.
type TeamsWebhookSettings struct {
	Webhooks map[string]TeamsWebhookTarget `json:"webhooks" yaml:"webhooks,omitempty"`
}

// TeamsWebhookTarget represents a single Teams webhook destination.
type TeamsWebhookTarget struct {
	WebhookURL SecureString `json:"webhook_url,omitzero" yaml:"webhook_url,omitempty"`
	Title      string       `json:"title,omitempty"      yaml:"-"`
}

type MQTTSettings struct {
	Broker      string       `json:"broker"                 yaml:"-"                  env:"PICOCLAW_CHANNELS_MQTT_BROKER"`
	AgentID     string       `json:"agent_id"               yaml:"-"                  env:"PICOCLAW_CHANNELS_MQTT_AGENT_ID"`
	TopicPrefix string       `json:"topic_prefix,omitempty" yaml:"-"                  env:"PICOCLAW_CHANNELS_MQTT_TOPIC_PREFIX"`
	Username    SecureString `json:"username,omitzero"      yaml:"username,omitempty" env:"PICOCLAW_CHANNELS_MQTT_USERNAME"`
	Password    SecureString `json:"password,omitzero"      yaml:"password,omitempty" env:"PICOCLAW_CHANNELS_MQTT_PASSWORD"`
	ClientID    string       `json:"client_id,omitempty"    yaml:"-"                  env:"PICOCLAW_CHANNELS_MQTT_CLIENT_ID"`
	KeepAlive   int          `json:"keep_alive,omitempty"   yaml:"-"                  env:"PICOCLAW_CHANNELS_MQTT_KEEP_ALIVE"`
	QoS         int          `json:"qos,omitempty"          yaml:"-"                  env:"PICOCLAW_CHANNELS_MQTT_QOS"`
}

// SlackWebhookSettings configures the output-only Slack webhook channel.
type SlackWebhookSettings struct {
	Webhooks map[string]SlackWebhookTarget `json:"webhooks" yaml:"webhooks,omitempty"`
}

// SlackWebhookTarget represents a single Slack Incoming Webhook destination.
type SlackWebhookTarget struct {
	WebhookURL SecureString `json:"webhook_url,omitzero" yaml:"webhook_url,omitempty"`
	Username   string       `json:"username,omitempty"   yaml:"-"`
	IconEmoji  string       `json:"icon_emoji,omitempty" yaml:"-"`
}

type HeartbeatConfig struct {
	Enabled  bool `json:"enabled"  env:"PICOCLAW_HEARTBEAT_ENABLED"`
	Interval int  `json:"interval" env:"PICOCLAW_HEARTBEAT_INTERVAL"` // minutes, min 5
}

type DevicesConfig struct {
	Enabled    bool `json:"enabled"     env:"PICOCLAW_DEVICES_ENABLED"`
	MonitorUSB bool `json:"monitor_usb" env:"PICOCLAW_DEVICES_MONITOR_USB"`
}

type VoiceConfig struct {
	AccountRef        string `json:"account_ref,omitempty"        env:"PICOCLAW_VOICE_ACCOUNT_REF"`
	ModelName         string `json:"model_name,omitempty"         env:"PICOCLAW_VOICE_MODEL_NAME"`
	TTSAccountRef     string `json:"tts_account_ref,omitempty"    env:"PICOCLAW_VOICE_TTS_ACCOUNT_REF"`
	TTSModelName      string `json:"tts_model_name,omitempty"     env:"PICOCLAW_VOICE_TTS_MODEL_NAME"`
	EchoTranscription bool   `json:"echo_transcription"           env:"PICOCLAW_VOICE_ECHO_TRANSCRIPTION"`
	ElevenLabsAPIKey  string `json:"elevenlabs_api_key,omitempty" env:"PICOCLAW_VOICE_ELEVENLABS_API_KEY"`
}

// ModelAliasConfig maps a stable user-facing alias to a concrete upstream
// model. AccountOverrides can select a different concrete model for a direct
// account, while DisabledAccounts can make the alias unavailable there. Both
// apply only to concrete accounts, never account routers.
type ModelAliasConfig struct {
	Name             string            `json:"name"                        yaml:"name"`
	Model            string            `json:"model"                       yaml:"model"`
	AccountOverrides map[string]string `json:"account_overrides,omitempty" yaml:"account_overrides,omitempty"`
	DisabledAccounts []string          `json:"disabled_accounts,omitempty" yaml:"disabled_accounts,omitempty"`
}

type ModelStreamingConfig struct {
	Enabled bool `json:"enabled,omitempty"`
}

func (c ModelStreamingConfig) IsZero() bool {
	return !c.Enabled
}

// ModelConfig represents a concrete provider account and its transport settings.
// It allows adding new providers (especially OpenAI-compatible ones) via configuration only.
// Model is optional account metadata; runtime selections resolve a model alias
// and replace it before a request is sent. When present, it may be either a plain
// model identifier or a provider-prefixed identifier such as
// "openai/gpt-5.4" or "nvidia/z-ai/glm-5.1".
// Supported providers include openai, anthropic, antigravity, claude-cli,
// codex-cli, github-copilot, and named OpenAI-compatible protocols such as
// groq, deepseek, modelscope, and novita.
type ModelConfig struct {
	// Required fields
	ModelName string `json:"model_name"` // Stable concrete account name
	Provider  string `json:"provider"`   // Provider name for routing and selection. When empty, provider resolution infers it from Model.
	Model     string `json:"model"`      // Model identifier, optionally provider-prefixed.

	// HTTP-based providers
	APIBase     string               `json:"api_base,omitempty"`     // API endpoint URL
	Proxy       string               `json:"proxy,omitempty"`        // HTTP proxy URL
	Fallbacks   []string             `json:"fallbacks,omitempty"`    // Fallback model names for failover
	Router      *AccountRouterConfig `json:"router,omitempty"`       // Static account router graph
	ModelRouter *ModelRouterConfig   `json:"model_router,omitempty"` // Dynamic model router graph

	// Special providers (CLI-based, OAuth, etc.)
	AuthMethod   string `json:"auth_method,omitempty"`   // Authentication method: oauth, token
	CredentialID string `json:"credential_id,omitempty"` // Auth store credential key for OAuth/token providers
	ConnectMode  string `json:"connect_mode,omitempty"`  // Connection mode: stdio, grpc
	Workspace    string `json:"workspace,omitempty"`     // Workspace path for CLI-based providers

	// Optional optimizations
	RPM                         int                  `json:"rpm,omitempty"`              // Requests per minute limit
	MaxTokensField              string               `json:"max_tokens_field,omitempty"` // Field name for max tokens (e.g., "max_completion_tokens")
	RequestTimeout              int                  `json:"request_timeout,omitempty"`
	ThinkingLevel               string               `json:"thinking_level,omitempty"`                // Extended thinking: off|low|medium|high|xhigh|adaptive
	ReasoningEffort             string               `json:"reasoning_effort,omitempty"`              // OpenAI-style reasoning effort: none|minimal|low|medium|high|xhigh
	InputPricePerMTok           float64              `json:"input_price_per_1m,omitempty"`            // Estimated input-token price in USD per 1M tokens
	OutputPricePerMTok          float64              `json:"output_price_per_1m,omitempty"`           // Estimated output-token price in USD per 1M tokens
	Subscription                bool                 `json:"subscription,omitempty"`                  // True when access is subscription-backed rather than direct metered API
	SubscriptionEquivalentModel string               `json:"subscription_equivalent_model,omitempty"` // Exact alias of the API-priced equivalent used for subscription cost estimates
	ToolSchemaTransform         string               `json:"tool_schema_transform,omitempty"`         // Optional tool schema compatibility transform (e.g. "simple")
	Streaming                   ModelStreamingConfig `json:"streaming,omitzero"`                      // Opt-in for provider streaming on this model entry
	ExtraBody                   map[string]any       `json:"extra_body,omitempty"`                    // Additional fields to inject into request body
	CustomHeaders               map[string]string    `json:"custom_headers,omitempty"`                // Additional headers to inject into every HTTP request

	APIKeys SecureStrings `json:"api_keys,omitzero" yaml:"api_keys,omitempty"` // API authentication keys (multiple keys for failover)

	// Enabled indicates whether this account entry is active. Legacy migrations
	// infer the field for existing keyed and local account configurations.
	Enabled bool `json:"enabled,omitempty" yaml:"enabled,omitempty"`
	// UserAgent is the user agent string to use for HTTP requests.
	UserAgent string `json:"user_agent,omitempty" yaml:"-"`

	// isVirtual marks this model as a virtual model generated from multi-key expansion.
	// Virtual models should not be persisted to config files.
	isVirtual bool
}

// APIKey returns the first API key from apiKeys
func (c *ModelConfig) APIKey() string {
	if len(c.APIKeys) > 0 {
		return c.APIKeys[0].String()
	}
	return ""
}

// IsVirtual returns true if this model was generated from multi-key expansion.
func (c *ModelConfig) IsVirtual() bool {
	return c.isVirtual
}

func (c *ModelConfig) IsAccountRouter() bool {
	if c == nil {
		return false
	}
	if c.Router != nil && c.ModelRouter == nil {
		return true
	}
	return strings.EqualFold(strings.TrimSpace(c.Provider), AccountRouterProvider)
}

func (c *ModelConfig) IsModelRouter() bool {
	if c == nil {
		return false
	}
	if c.ModelRouter != nil {
		return true
	}
	return strings.EqualFold(strings.TrimSpace(c.Provider), ModelRouterProvider)
}

// Validate checks if the ModelConfig has all required fields.
func (c *ModelConfig) Validate() error {
	if c.ModelName == "" {
		return fmt.Errorf("model_name is required")
	}
	if c.IsModelRouter() {
		if !c.isVirtual {
			return fmt.Errorf("model routers must be configured in model_routers")
		}
		if c.ModelRouter == nil {
			return fmt.Errorf(
				"model_router config is required for provider %q",
				ModelRouterProvider,
			)
		}
		if strings.TrimSpace(c.Provider) != "" &&
			!strings.EqualFold(strings.TrimSpace(c.Provider), ModelRouterProvider) {
			return fmt.Errorf("model_router config must use provider %q", ModelRouterProvider)
		}
		if c.Model == "" {
			return fmt.Errorf("model is required")
		}
		return c.ModelRouter.validate(false)
	}
	if c.IsAccountRouter() {
		if !c.isVirtual {
			return fmt.Errorf("account routers must be configured in account_routers")
		}
		if c.Router == nil {
			return fmt.Errorf("router config is required for provider %q", AccountRouterProvider)
		}
		if strings.TrimSpace(c.Provider) != "" &&
			!strings.EqualFold(strings.TrimSpace(c.Provider), AccountRouterProvider) {
			return fmt.Errorf("router config must use provider %q", AccountRouterProvider)
		}
		return c.Router.validate(false)
	}
	if _, err := providercommon.NormalizeToolSchemaTransform(c.ToolSchemaTransform); err != nil {
		return err
	}
	if _, err := providercommon.NormalizeReasoningEffort(c.ReasoningEffort); err != nil {
		return err
	}
	if strings.TrimSpace(c.Model) == "" {
		if strings.TrimSpace(c.Provider) == "" {
			return fmt.Errorf("provider is required when account model is empty")
		}
		return nil
	}
	if err := validateConcreteModelIdentifier(c.Model); err != nil {
		return err
	}

	return nil
}

func validateConcreteModelIdentifier(model string) error {
	if strings.TrimSpace(model) == "" {
		return fmt.Errorf("model is required")
	}
	// Reject whitespace in model identifier
	if strings.ContainsAny(model, " \t\n\r") {
		return fmt.Errorf("model identifier contains whitespace")
	}

	// Reject leading slash
	if strings.HasPrefix(model, "/") {
		return fmt.Errorf("model identifier must not start with /")
	}

	// Reject consecutive slashes
	if strings.Contains(model, "//") {
		return fmt.Errorf("model identifier must not contain //")
	}
	return nil
}

func (r *AccountRouterConfig) Validate() error {
	return r.validate(false)
}

func (r *AccountRouterConfig) validate(requireName bool) error {
	if r == nil {
		return fmt.Errorf("router config is required")
	}
	if requireName && strings.TrimSpace(r.Name) == "" {
		return fmt.Errorf("router.name is required")
	}
	if !r.Enabled {
		return fmt.Errorf("router must be enabled")
	}
	entry := strings.TrimSpace(r.Entry)
	if entry == "" {
		return fmt.Errorf("router.entry is required")
	}
	if len(r.Blocks) == 0 {
		return fmt.Errorf("router.blocks must contain at least one block")
	}
	seen := make(map[string]bool, len(r.Blocks))
	for i, block := range r.Blocks {
		id := strings.TrimSpace(block.ID)
		if id == "" {
			return fmt.Errorf("router.blocks[%d].id is required", i)
		}
		if seen[id] {
			return fmt.Errorf("router.blocks[%d].id %q is duplicated", i, id)
		}
		seen[id] = true
	}
	if !seen[entry] {
		return fmt.Errorf("router.entry %q does not reference a block", entry)
	}
	for i, block := range r.Blocks {
		blockType := strings.TrimSpace(block.Type)
		switch blockType {
		case AccountRouterBlockTypeAccount:
			if strings.TrimSpace(block.Account) == "" {
				return fmt.Errorf("router.blocks[%d].account is required", i)
			}
		case AccountRouterBlockTypeLoadBalance:
			accounts := nonEmptyStrings(block.Accounts)
			if len(accounts) == 0 {
				return fmt.Errorf("router.blocks[%d].accounts must contain at least one account", i)
			}
			if hasDuplicateString(accounts) {
				return fmt.Errorf("router.blocks[%d].accounts contains duplicate accounts", i)
			}
			strategy := strings.TrimSpace(block.Strategy)
			switch strategy {
			case "",
				AccountRouterStrategyBlind,
				AccountRouterStrategyTokensSpent,
				AccountRouterStrategyClosestLimit:
			default:
				return fmt.Errorf("router.blocks[%d].strategy %q is unsupported", i, strategy)
			}
		case AccountRouterBlockTypeBranch:
			if block.Condition == nil {
				return fmt.Errorf("router.blocks[%d].condition is required", i)
			}
			if err := validateAccountRouterCondition(block.Condition); err != nil {
				return fmt.Errorf("router.blocks[%d].condition: %w", i, err)
			}
			if strings.TrimSpace(block.Then) == "" {
				return fmt.Errorf("router.blocks[%d].then is required", i)
			}
			if strings.TrimSpace(block.Else) == "" {
				return fmt.Errorf("router.blocks[%d].else is required", i)
			}
		default:
			return fmt.Errorf("router.blocks[%d].type %q is unsupported", i, block.Type)
		}
		for label, next := range accountRouterBlockNextRefs(block) {
			if next != "" && !seen[next] {
				return fmt.Errorf("router.blocks[%d].%s %q does not reference a block", i, label, next)
			}
		}
	}
	return validateAccountRouterFallbackAcyclic(entry, r.Blocks)
}

func validateAccountRouterCondition(condition *AccountRouterCondition) error {
	switch strings.TrimSpace(condition.Operator) {
	case AccountRouterBranchOpGT, AccountRouterBranchOpGTE, AccountRouterBranchOpLT,
		AccountRouterBranchOpLTE, AccountRouterBranchOpEQ, AccountRouterBranchOpNEQ:
	default:
		return fmt.Errorf("operator %q is unsupported", condition.Operator)
	}
	if err := validateAccountRouterExpression(condition.Left); err != nil {
		return fmt.Errorf("left: %w", err)
	}
	if err := validateAccountRouterExpression(condition.Right); err != nil {
		return fmt.Errorf("right: %w", err)
	}
	return nil
}

func validateAccountRouterExpression(expr AccountRouterExpression) error {
	if strings.TrimSpace(expr.Op) != "" {
		switch strings.TrimSpace(expr.Op) {
		case AccountRouterMathAdd, AccountRouterMathSubtract, AccountRouterMathMultiply,
			AccountRouterMathDivide, AccountRouterMathModulo:
		default:
			return fmt.Errorf("math op %q is unsupported", expr.Op)
		}
		if expr.Left == nil || expr.Right == nil {
			return fmt.Errorf("math expression requires left and right")
		}
		if err := validateAccountRouterExpression(*expr.Left); err != nil {
			return fmt.Errorf("left: %w", err)
		}
		if err := validateAccountRouterExpression(*expr.Right); err != nil {
			return fmt.Errorf("right: %w", err)
		}
		return nil
	}
	hasValue := expr.Value != nil
	hasMetric := strings.TrimSpace(expr.Metric) != ""
	if hasValue == hasMetric {
		return fmt.Errorf("expression must define exactly one of value or metric")
	}
	if hasMetric && strings.TrimSpace(expr.Account) == "" {
		return fmt.Errorf("metric expression requires account")
	}
	return nil
}

func nonEmptyStrings(values []string) []string {
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" {
			out = append(out, value)
		}
	}
	return out
}

func hasDuplicateString(values []string) bool {
	seen := map[string]bool{}
	for _, value := range values {
		if seen[value] {
			return true
		}
		seen[value] = true
	}
	return false
}

func validateAccountRouterFallbackAcyclic(entry string, blocks []AccountRouterBlock) error {
	byID := make(map[string]AccountRouterBlock, len(blocks))
	for _, block := range blocks {
		byID[strings.TrimSpace(block.ID)] = block
	}
	visiting := map[string]bool{}
	visited := map[string]bool{}
	var walk func(id string, depth int) error
	walk = func(id string, depth int) error {
		if depth > defaultAccountRouterMaxFallbackDepth {
			return fmt.Errorf(
				"router fallback chain exceeds %d blocks",
				defaultAccountRouterMaxFallbackDepth,
			)
		}
		id = strings.TrimSpace(id)
		if id == "" || visited[id] {
			return nil
		}
		if visiting[id] {
			return fmt.Errorf("router fallback cycle at block %q", id)
		}
		block, ok := byID[id]
		if !ok {
			return nil
		}
		visiting[id] = true
		for _, next := range accountRouterBlockNextRefs(block) {
			if err := walk(next, depth+1); err != nil {
				return err
			}
		}
		visiting[id] = false
		visited[id] = true
		return nil
	}
	return walk(entry, 0)
}

func (c *ModelConfig) SetAPIKey(value string) {
	if len(c.APIKeys) > 0 {
		c.APIKeys[0].Set(value)
	} else {
		c.APIKeys = append(c.APIKeys, NewSecureString(value))
	}
}

type ToolDiscoveryConfig struct {
	Enabled          bool `json:"enabled"            env:"PICOCLAW_TOOLS_DISCOVERY_ENABLED"`
	TTL              int  `json:"ttl"                env:"PICOCLAW_TOOLS_DISCOVERY_TTL"`
	MaxSearchResults int  `json:"max_search_results" env:"PICOCLAW_MAX_SEARCH_RESULTS"`
	UseBM25          bool `json:"use_bm25"           env:"PICOCLAW_TOOLS_DISCOVERY_USE_BM25"`
	UseRegex         bool `json:"use_regex"          env:"PICOCLAW_TOOLS_DISCOVERY_USE_REGEX"`
}

type ToolConfig struct {
	Enabled bool `json:"enabled" yaml:"-" env:"ENABLED"`
}

const (
	ToolSurfaceAuto     = "auto"
	ToolSurfaceCodex    = "codex"
	ToolSurfacePicoClaw = "picoclaw"
	ToolSurfaceSimple   = "simple"

	ToolRuntimeAdaptationAuto  = "auto"
	ToolRuntimeAdaptationNever = "never"
	ToolRuntimeAdaptationAllow = "allow"

	ToolCacheSensitivityAuto   = "auto"
	ToolCacheSensitivityNever  = "never"
	ToolCacheSensitivityAlways = "always"

	ToolVisibleChangeNever           = "never"
	ToolVisibleChangeNextSession     = "next_session"
	ToolVisibleChangeContextBoundary = "context_boundary"
	ToolVisibleChangeImmediate       = "immediate"
)

type ToolAdaptationConfig struct {
	Enabled                bool                            `json:"enabled"                     yaml:"-" env:"ENABLED"`
	VisibleToolSurface     string                          `json:"visible_tool_surface"        yaml:"-" env:"VISIBLE_TOOL_SURFACE"`
	LearnFromToolCalls     bool                            `json:"learn_from_tool_calls"       yaml:"-" env:"LEARN_FROM_TOOL_CALLS"`
	RunModelProbes         bool                            `json:"run_model_probes"            yaml:"-" env:"RUN_MODEL_PROBES"`
	AllowRuntimeDowngrade  string                          `json:"allow_runtime_downgrade"     yaml:"-" env:"ALLOW_RUNTIME_DOWNGRADE"`
	AllowRuntimePromotion  string                          `json:"allow_runtime_promotion"     yaml:"-" env:"ALLOW_RUNTIME_PROMOTION"`
	ApplyVisibleChanges    string                          `json:"apply_visible_changes"       yaml:"-" env:"APPLY_VISIBLE_CHANGES"`
	CacheSensitiveAPIs     string                          `json:"cache_sensitive_apis"        yaml:"-" env:"CACHE_SENSITIVE_APIS"`
	CacheBreakingDowngrade bool                            `json:"cache_breaking_downgrade"    yaml:"-" env:"CACHE_BREAKING_DOWNGRADE"`
	ProfileOverrides       []ToolAdaptationProfileOverride `json:"profile_overrides,omitempty" yaml:"-"`
}

// ToolAdaptationProfileOverride applies profile-specific policy while keeping
// provider credentials and model routing in the normal model configuration.
// Empty policy fields inherit the corresponding global adaptation setting.
type ToolAdaptationProfileOverride struct {
	Provider           string `json:"provider"`
	Model              string `json:"model"`
	VisibleToolSurface string `json:"visible_tool_surface,omitempty"`
	CacheSensitiveAPIs string `json:"cache_sensitive_apis,omitempty"`
}

func DefaultToolAdaptationConfig() ToolAdaptationConfig {
	return ToolAdaptationConfig{
		Enabled:                true,
		VisibleToolSurface:     ToolSurfaceAuto,
		LearnFromToolCalls:     true,
		RunModelProbes:         true,
		AllowRuntimeDowngrade:  ToolRuntimeAdaptationAuto,
		AllowRuntimePromotion:  ToolRuntimeAdaptationAuto,
		ApplyVisibleChanges:    ToolVisibleChangeNextSession,
		CacheSensitiveAPIs:     ToolCacheSensitivityAuto,
		CacheBreakingDowngrade: false,
	}
}

func NormalizeToolSurface(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case ToolSurfaceCodex:
		return ToolSurfaceCodex
	case ToolSurfacePicoClaw:
		return ToolSurfacePicoClaw
	case ToolSurfaceSimple:
		return ToolSurfaceSimple
	case "", ToolSurfaceAuto:
		return ToolSurfaceAuto
	default:
		return ToolSurfaceAuto
	}
}

func NormalizeToolRuntimeAdaptation(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case ToolRuntimeAdaptationNever:
		return ToolRuntimeAdaptationNever
	case ToolRuntimeAdaptationAllow:
		return ToolRuntimeAdaptationAllow
	case "", ToolRuntimeAdaptationAuto:
		return ToolRuntimeAdaptationAuto
	default:
		return ToolRuntimeAdaptationAuto
	}
}

func NormalizeToolCacheSensitivity(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case ToolCacheSensitivityNever:
		return ToolCacheSensitivityNever
	case ToolCacheSensitivityAlways:
		return ToolCacheSensitivityAlways
	case "", ToolCacheSensitivityAuto:
		return ToolCacheSensitivityAuto
	default:
		return ToolCacheSensitivityAuto
	}
}

func NormalizeToolVisibleChangePolicy(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case ToolVisibleChangeNever:
		return ToolVisibleChangeNever
	case ToolVisibleChangeContextBoundary:
		return ToolVisibleChangeContextBoundary
	case ToolVisibleChangeImmediate:
		return ToolVisibleChangeImmediate
	case "", ToolVisibleChangeNextSession:
		return ToolVisibleChangeNextSession
	default:
		return ToolVisibleChangeNextSession
	}
}

func (c ToolAdaptationConfig) Normalized() ToolAdaptationConfig {
	defaults := DefaultToolAdaptationConfig()
	if strings.TrimSpace(c.VisibleToolSurface) == "" {
		c.VisibleToolSurface = defaults.VisibleToolSurface
	}
	if strings.TrimSpace(c.AllowRuntimeDowngrade) == "" {
		c.AllowRuntimeDowngrade = defaults.AllowRuntimeDowngrade
	}
	if strings.TrimSpace(c.AllowRuntimePromotion) == "" {
		c.AllowRuntimePromotion = defaults.AllowRuntimePromotion
	}
	if strings.TrimSpace(c.ApplyVisibleChanges) == "" {
		c.ApplyVisibleChanges = defaults.ApplyVisibleChanges
	}
	if strings.TrimSpace(c.CacheSensitiveAPIs) == "" {
		c.CacheSensitiveAPIs = defaults.CacheSensitiveAPIs
	}

	c.VisibleToolSurface = NormalizeToolSurface(c.VisibleToolSurface)
	c.AllowRuntimeDowngrade = NormalizeToolRuntimeAdaptation(c.AllowRuntimeDowngrade)
	c.AllowRuntimePromotion = NormalizeToolRuntimeAdaptation(c.AllowRuntimePromotion)
	c.ApplyVisibleChanges = NormalizeToolVisibleChangePolicy(c.ApplyVisibleChanges)
	c.CacheSensitiveAPIs = NormalizeToolCacheSensitivity(c.CacheSensitiveAPIs)
	c.ProfileOverrides = normalizeToolAdaptationProfileOverrides(c.ProfileOverrides)
	return c
}

func normalizeToolAdaptationProfileOverrides(
	overrides []ToolAdaptationProfileOverride,
) []ToolAdaptationProfileOverride {
	normalized := make([]ToolAdaptationProfileOverride, 0, len(overrides))
	indexByKey := make(map[string]int, len(overrides))
	for _, override := range overrides {
		override.Provider = strings.TrimSpace(override.Provider)
		override.Model = strings.TrimSpace(override.Model)
		if override.Provider == "" || override.Model == "" {
			continue
		}
		if strings.TrimSpace(override.VisibleToolSurface) != "" {
			override.VisibleToolSurface = NormalizeToolSurface(override.VisibleToolSurface)
		}
		if strings.TrimSpace(override.CacheSensitiveAPIs) != "" {
			override.CacheSensitiveAPIs = NormalizeToolCacheSensitivity(override.CacheSensitiveAPIs)
		}
		key := strings.ToLower(override.Provider) + "/" + strings.ToLower(override.Model)
		if index, exists := indexByKey[key]; exists {
			normalized[index] = override
			continue
		}
		indexByKey[key] = len(normalized)
		normalized = append(normalized, override)
	}
	return normalized
}

type MessageToolsConfig struct {
	ToolConfig `yaml:"-" envPrefix:"PICOCLAW_TOOLS_MESSAGE_"`

	MediaEnabled bool `json:"media_enabled" yaml:"-" env:"PICOCLAW_TOOLS_MESSAGE_MEDIA_ENABLED"`
}

type BraveConfig struct {
	Enabled    bool          `json:"enabled"           yaml:"-"                  env:"PICOCLAW_TOOLS_WEB_BRAVE_ENABLED"`
	APIKeys    SecureStrings `json:"api_keys,omitzero" yaml:"api_keys,omitempty" env:"PICOCLAW_TOOLS_WEB_BRAVE_API_KEYS"`
	MaxResults int           `json:"max_results"       yaml:"-"                  env:"PICOCLAW_TOOLS_WEB_BRAVE_MAX_RESULTS"`
}

// APIKey returns the Brave API key
func (c *BraveConfig) APIKey() string {
	if len(c.APIKeys) == 0 {
		return ""
	}
	return c.APIKeys[0].String()
}

// SetAPIKey sets the Brave API key
func (c *BraveConfig) SetAPIKey(key string) {
	c.APIKeys = SimpleSecureStrings(key)
}

func (c *BraveConfig) SetAPIKeys(keys []string) {
	c.APIKeys = SimpleSecureStrings(keys...)
}

type TavilyConfig struct {
	Enabled    bool          `json:"enabled"           yaml:"-"                  env:"PICOCLAW_TOOLS_WEB_TAVILY_ENABLED"`
	APIKeys    SecureStrings `json:"api_keys,omitzero" yaml:"api_keys,omitempty" env:"PICOCLAW_TOOLS_WEB_TAVILY_API_KEYS"`
	BaseURL    string        `json:"base_url"          yaml:"-"                  env:"PICOCLAW_TOOLS_WEB_TAVILY_BASE_URL"`
	MaxResults int           `json:"max_results"       yaml:"-"                  env:"PICOCLAW_TOOLS_WEB_TAVILY_MAX_RESULTS"`
}

// APIKey returns the Tavily API key
func (c *TavilyConfig) APIKey() string {
	if len(c.APIKeys) == 0 {
		return ""
	}
	return c.APIKeys[0].String()
}

// SetAPIKey sets the Tavily API key
func (c *TavilyConfig) SetAPIKey(key string) {
	c.APIKeys = SimpleSecureStrings(key)
}

// SetAPIKeys sets the Tavily API keys
func (c *TavilyConfig) SetAPIKeys(keys []string) {
	c.APIKeys = make(SecureStrings, len(keys))
	for i, k := range keys {
		c.APIKeys[i] = NewSecureString(k)
	}
}

type KagiConfig struct {
	Enabled    bool          `json:"enabled"           yaml:"-"                  env:"PICOCLAW_TOOLS_WEB_KAGI_ENABLED"`
	APIKeys    SecureStrings `json:"api_keys,omitzero" yaml:"api_keys,omitempty" env:"PICOCLAW_TOOLS_WEB_KAGI_API_KEYS"`
	BaseURL    string        `json:"base_url"          yaml:"-"                  env:"PICOCLAW_TOOLS_WEB_KAGI_BASE_URL"`
	MaxResults int           `json:"max_results"       yaml:"-"                  env:"PICOCLAW_TOOLS_WEB_KAGI_MAX_RESULTS"`
}

// APIKey returns the Kagi API key
func (c *KagiConfig) APIKey() string {
	if len(c.APIKeys) == 0 {
		return ""
	}
	return c.APIKeys[0].String()
}

// SetAPIKey sets the Kagi API key
func (c *KagiConfig) SetAPIKey(key string) {
	c.APIKeys = SimpleSecureStrings(key)
}

// SetAPIKeys sets the Kagi API keys
func (c *KagiConfig) SetAPIKeys(keys []string) {
	c.APIKeys = SimpleSecureStrings(keys...)
}

type DuckDuckGoConfig struct {
	Enabled    bool `json:"enabled"     env:"PICOCLAW_TOOLS_WEB_DUCKDUCKGO_ENABLED"`
	MaxResults int  `json:"max_results" env:"PICOCLAW_TOOLS_WEB_DUCKDUCKGO_MAX_RESULTS"`
}

type SogouConfig struct {
	Enabled    bool `json:"enabled"     env:"PICOCLAW_TOOLS_WEB_SOGOU_ENABLED"`
	MaxResults int  `json:"max_results" env:"PICOCLAW_TOOLS_WEB_SOGOU_MAX_RESULTS"`
}

type GeminiSearchConfig struct {
	Enabled    bool         `json:"enabled"          yaml:"-"                 env:"PICOCLAW_TOOLS_WEB_GEMINI_ENABLED"`
	APIKey     SecureString `json:"api_key,omitzero" yaml:"api_key,omitempty" env:"PICOCLAW_TOOLS_WEB_GEMINI_API_KEY"`
	ModelAlias string       `json:"model_alias"      yaml:"-"                 env:"PICOCLAW_TOOLS_WEB_GEMINI_MODEL_ALIAS"`
	MaxResults int          `json:"max_results"      yaml:"-"                 env:"PICOCLAW_TOOLS_WEB_GEMINI_MAX_RESULTS"`
}

type PerplexityConfig struct {
	Enabled    bool          `json:"enabled"           yaml:"-"                  env:"PICOCLAW_TOOLS_WEB_PERPLEXITY_ENABLED"`
	APIKeys    SecureStrings `json:"api_keys,omitzero" yaml:"api_keys,omitempty" env:"PICOCLAW_TOOLS_WEB_PERPLEXITY_API_KEYS"`
	ModelAlias string        `json:"model_alias"       yaml:"-"                  env:"PICOCLAW_TOOLS_WEB_PERPLEXITY_MODEL_ALIAS"`
	MaxResults int           `json:"max_results"       yaml:"-"                  env:"PICOCLAW_TOOLS_WEB_PERPLEXITY_MAX_RESULTS"`
}

// APIKey returns the Perplexity API key
func (c *PerplexityConfig) APIKey() string {
	if len(c.APIKeys) == 0 {
		return ""
	}
	return c.APIKeys[0].String()
}

// SetAPIKey sets the Perplexity API key
func (c *PerplexityConfig) SetAPIKey(key string) {
	c.APIKeys = SimpleSecureStrings(key)
}

type SearXNGConfig struct {
	Enabled    bool   `json:"enabled"     env:"PICOCLAW_TOOLS_WEB_SEARXNG_ENABLED"`
	BaseURL    string `json:"base_url"    env:"PICOCLAW_TOOLS_WEB_SEARXNG_BASE_URL"`
	MaxResults int    `json:"max_results" env:"PICOCLAW_TOOLS_WEB_SEARXNG_MAX_RESULTS"`
}

type GLMSearchConfig struct {
	Enabled bool         `json:"enabled"          yaml:"-"                 env:"PICOCLAW_TOOLS_WEB_GLM_ENABLED"`
	APIKey  SecureString `json:"api_key,omitzero" yaml:"api_key,omitempty" env:"PICOCLAW_TOOLS_WEB_GLM_API_KEY"`
	BaseURL string       `json:"base_url"         yaml:"-"                 env:"PICOCLAW_TOOLS_WEB_GLM_BASE_URL"`
	// SearchEngine specifies the search backend: "search_std" (default),
	// "search_pro", "search_pro_sogou", or "search_pro_quark".
	SearchEngine string `json:"search_engine" yaml:"-" env:"PICOCLAW_TOOLS_WEB_GLM_SEARCH_ENGINE"`
	MaxResults   int    `json:"max_results"   yaml:"-" env:"PICOCLAW_TOOLS_WEB_GLM_MAX_RESULTS"`
}

type BaiduSearchConfig struct {
	Enabled    bool         `json:"enabled"          yaml:"-"                 env:"PICOCLAW_TOOLS_WEB_BAIDU_ENABLED"`
	APIKey     SecureString `json:"api_key,omitzero" yaml:"api_key,omitempty" env:"PICOCLAW_TOOLS_WEB_BAIDU_API_KEY"`
	BaseURL    string       `json:"base_url"         yaml:"-"                 env:"PICOCLAW_TOOLS_WEB_BAIDU_BASE_URL"`
	MaxResults int          `json:"max_results"      yaml:"-"                 env:"PICOCLAW_TOOLS_WEB_BAIDU_MAX_RESULTS"`
}

type WebToolsConfig struct {
	ToolConfig  `                   yaml:"-"                      envPrefix:"PICOCLAW_TOOLS_WEB_"`
	Brave       BraveConfig        `yaml:"brave,omitempty"                                        json:"brave"`
	Tavily      TavilyConfig       `yaml:"tavily,omitempty"                                       json:"tavily"`
	Kagi        KagiConfig         `yaml:"kagi,omitempty"                                         json:"kagi"`
	Sogou       SogouConfig        `yaml:"-"                                                      json:"sogou"`
	DuckDuckGo  DuckDuckGoConfig   `yaml:"-"                                                      json:"duckduckgo"`
	Gemini      GeminiSearchConfig `yaml:"gemini,omitempty"                                       json:"gemini"`
	Perplexity  PerplexityConfig   `yaml:"perplexity,omitempty"                                   json:"perplexity"`
	SearXNG     SearXNGConfig      `yaml:"-"                                                      json:"searxng"`
	GLMSearch   GLMSearchConfig    `yaml:"glm_search,omitempty"                                   json:"glm_search"`
	BaiduSearch BaiduSearchConfig  `yaml:"baidu_search,omitempty"                                 json:"baidu_search"`
	Provider    string             `yaml:"-"                                                      json:"provider,omitempty" env:"PICOCLAW_TOOLS_WEB_PROVIDER"`
	// PreferNative controls whether to use provider-native web search when
	// the active LLM supports it (e.g. OpenAI web_search_preview). When true,
	// the client-side web_search tool is hidden to avoid duplicate search surfaces,
	// and the provider's built-in search is used instead. Falls back to client-side
	// search when the provider does not support native search.
	PreferNative bool `yaml:"-" json:"prefer_native" env:"PICOCLAW_TOOLS_WEB_PREFER_NATIVE"`
	// Proxy is an optional proxy URL for web tools (http/https/socks5/socks5h).
	// For authenticated proxies, prefer HTTP_PROXY/HTTPS_PROXY env vars instead of embedding credentials in config.
	Proxy                string              `yaml:"-" json:"proxy,omitempty"                  env:"PICOCLAW_TOOLS_WEB_PROXY"`
	FetchLimitBytes      int64               `yaml:"-" json:"fetch_limit_bytes,omitempty"      env:"PICOCLAW_TOOLS_WEB_FETCH_LIMIT_BYTES"`
	Format               string              `yaml:"-" json:"format,omitempty"                 env:"PICOCLAW_TOOLS_WEB_FORMAT"`
	PrivateHostWhitelist FlexibleStringSlice `yaml:"-" json:"private_host_whitelist,omitempty" env:"PICOCLAW_TOOLS_WEB_PRIVATE_HOST_WHITELIST"`
}

type CronToolsConfig struct {
	ToolConfig `envPrefix:"PICOCLAW_TOOLS_CRON_"`
	// 0 means no timeout.
	ExecTimeoutMinutes    int      `json:"exec_timeout_minutes"    env:"PICOCLAW_TOOLS_CRON_EXEC_TIMEOUT_MINUTES"`
	AllowCommand          bool     `json:"allow_command"           env:"PICOCLAW_TOOLS_CRON_ALLOW_COMMAND"`
	CommandAllowedRemotes []string `json:"command_allowed_remotes" env:"PICOCLAW_TOOLS_CRON_COMMAND_ALLOWED_REMOTES"`
}

type ExecConfig struct {
	ToolConfig          `         envPrefix:"PICOCLAW_TOOLS_EXEC_"`
	EnableDenyPatterns  bool     `                                 json:"enable_deny_patterns"  env:"PICOCLAW_TOOLS_EXEC_ENABLE_DENY_PATTERNS"`
	AllowRemote         bool     `                                 json:"allow_remote"          env:"PICOCLAW_TOOLS_EXEC_ALLOW_REMOTE"`
	CustomDenyPatterns  []string `                                 json:"custom_deny_patterns"  env:"PICOCLAW_TOOLS_EXEC_CUSTOM_DENY_PATTERNS"`
	CustomAllowPatterns []string `                                 json:"custom_allow_patterns" env:"PICOCLAW_TOOLS_EXEC_CUSTOM_ALLOW_PATTERNS"`
	TimeoutSeconds      int      `                                 json:"timeout_seconds"       env:"PICOCLAW_TOOLS_EXEC_TIMEOUT_SECONDS"` // 0 means use default (60s)
}

type SkillsToolsConfig struct {
	ToolConfig `                       yaml:"-"                    envPrefix:"PICOCLAW_TOOLS_SKILLS_"`
	Registries SkillsRegistriesConfig `yaml:"registries,omitempty"                                    json:"registries"`
	// Deprecated: use registries.github instead.
	Github                SkillsGithubConfig `yaml:"github,omitempty" json:"github"`
	MaxConcurrentSearches int                `yaml:"-"                json:"max_concurrent_searches" env:"PICOCLAW_TOOLS_SKILLS_MAX_CONCURRENT_SEARCHES"`
	SearchCache           SearchCacheConfig  `yaml:"-"                json:"search_cache"`
}

type MediaCleanupConfig struct {
	ToolConfig `    envPrefix:"PICOCLAW_MEDIA_CLEANUP_"`
	MaxAge     int `                                    json:"max_age_minutes"  env:"PICOCLAW_MEDIA_CLEANUP_MAX_AGE"`
	Interval   int `                                    json:"interval_minutes" env:"PICOCLAW_MEDIA_CLEANUP_INTERVAL"`
}

type ReadFileToolConfig struct {
	Enabled         bool   `json:"enabled"`
	Mode            string `json:"mode"`
	MaxReadFileSize int    `json:"max_read_file_size"`
}

const (
	ThreadPolicyModeOff     = "off"
	ThreadPolicyModeSuggest = "suggest"
	ThreadPolicyModeAuto    = "auto"
	ThreadPolicyModeTool    = "tool"

	ThreadAttachStrategySearchThenCreate = "search_then_create"
	ThreadAttachStrategySearchThenAsk    = "search_then_ask"
	ThreadAttachStrategyNever            = "never"

	ThreadPolicyThresholdAny = "any"
	ThreadPolicyThresholdAll = "all"
)

type ThreadsToolConfig struct {
	Enabled bool               `json:"enabled" yaml:"-" env:"PICOCLAW_TOOLS_THREADS_ENABLED"`
	Policy  ThreadPolicyConfig `json:"policy"  yaml:"-"`
}

type ThreadPolicyConfig struct {
	Enabled      bool               `json:"enabled"      env:"PICOCLAW_TOOLS_THREADS_POLICY_ENABLED"`
	Mode         string             `json:"mode"         env:"PICOCLAW_TOOLS_THREADS_POLICY_MODE"`
	Instructions string             `json:"instructions" env:"PICOCLAW_TOOLS_THREADS_POLICY_INSTRUCTIONS"`
	Rules        []ThreadPolicyRule `json:"rules"`

	Agents map[string]ThreadAgentPolicy `json:"agents,omitempty"`
}

type ThreadPolicyRule struct {
	Type              string  `json:"type"`
	Description       string  `json:"description"`
	Mode              string  `json:"mode,omitempty"`
	AttachStrategy    string  `json:"attach_strategy,omitempty"`
	MinMessages       int     `json:"min_messages,omitempty"`
	MinTextChars      int     `json:"min_text_chars,omitempty"`
	ThresholdLogic    string  `json:"threshold_logic,omitempty"`
	MinAutoConfidence float64 `json:"min_auto_confidence,omitempty"`
	ConfirmIfMultiple bool    `json:"confirm_if_multiple,omitempty"`
}

type ThreadAgentPolicy struct {
	Mode           string `json:"mode,omitempty"`
	AttachStrategy string `json:"attach_strategy,omitempty"`
}

func (p ThreadPolicyConfig) EffectiveMode() string {
	switch strings.ToLower(strings.TrimSpace(p.Mode)) {
	case ThreadPolicyModeOff:
		return ThreadPolicyModeOff
	case ThreadPolicyModeSuggest:
		return ThreadPolicyModeSuggest
	case ThreadPolicyModeTool:
		return ThreadPolicyModeTool
	case ThreadPolicyModeAuto:
		return ThreadPolicyModeAuto
	case "":
		return ThreadPolicyModeTool
	default:
		return ThreadPolicyModeTool
	}
}

func NormalizeThreadPolicyMode(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case ThreadPolicyModeOff:
		return ThreadPolicyModeOff
	case ThreadPolicyModeSuggest:
		return ThreadPolicyModeSuggest
	case ThreadPolicyModeTool:
		return ThreadPolicyModeTool
	case ThreadPolicyModeAuto:
		return ThreadPolicyModeAuto
	case "":
		return ThreadPolicyModeTool
	default:
		return ThreadPolicyModeTool
	}
}

func NormalizeThreadAttachStrategy(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case ThreadAttachStrategySearchThenAsk:
		return ThreadAttachStrategySearchThenAsk
	case ThreadAttachStrategyNever:
		return ThreadAttachStrategyNever
	case "", ThreadAttachStrategySearchThenCreate:
		return ThreadAttachStrategySearchThenCreate
	default:
		return ThreadAttachStrategySearchThenCreate
	}
}

func NormalizeThreadPolicyType(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "coding", "reviewing", "investigating":
		return strings.ToLower(strings.TrimSpace(value))
	default:
		return "general"
	}
}

func NormalizeThreadPolicyThresholdLogic(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case ThreadPolicyThresholdAll:
		return ThreadPolicyThresholdAll
	case "", ThreadPolicyThresholdAny:
		return ThreadPolicyThresholdAny
	default:
		return ThreadPolicyThresholdAny
	}
}

func NormalizeThreadPolicyRules(rules []ThreadPolicyRule) []ThreadPolicyRule {
	if len(rules) == 0 {
		return nil
	}
	out := make([]ThreadPolicyRule, 0, len(rules))
	for _, rule := range rules {
		description := strings.TrimSpace(rule.Description)
		if description == "" {
			continue
		}
		minMessages := normalizeNonNegativeInt(rule.MinMessages)
		minTextChars := normalizeNonNegativeInt(rule.MinTextChars)
		thresholdLogic := ""
		if minMessages > 0 || minTextChars > 0 {
			thresholdLogic = NormalizeThreadPolicyThresholdLogic(rule.ThresholdLogic)
		}
		out = append(out, ThreadPolicyRule{
			Type:              NormalizeThreadPolicyType(rule.Type),
			Description:       description,
			Mode:              normalizeOptionalThreadPolicyMode(rule.Mode),
			AttachStrategy:    normalizeOptionalThreadAttachStrategy(rule.AttachStrategy),
			MinMessages:       minMessages,
			MinTextChars:      minTextChars,
			ThresholdLogic:    thresholdLogic,
			MinAutoConfidence: rule.MinAutoConfidence,
			ConfirmIfMultiple: rule.ConfirmIfMultiple,
		})
	}
	return out
}

func normalizeNonNegativeInt(value int) int {
	if value < 0 {
		return 0
	}
	return value
}

func normalizeOptionalThreadPolicyMode(value string) string {
	if strings.TrimSpace(value) == "" {
		return ""
	}
	return NormalizeThreadPolicyMode(value)
}

func normalizeOptionalThreadAttachStrategy(value string) string {
	if strings.TrimSpace(value) == "" {
		return ""
	}
	return NormalizeThreadAttachStrategy(value)
}

const (
	ReadFileModeBytes = "bytes"
	ReadFileModeLines = "lines"
)

func (c ReadFileToolConfig) EffectiveMode() string {
	switch strings.ToLower(strings.TrimSpace(c.Mode)) {
	case ReadFileModeLines:
		return ReadFileModeLines
	case "", ReadFileModeBytes:
		return ReadFileModeBytes
	default:
		return ReadFileModeBytes
	}
}

type ToolsConfig struct {
	AllowReadPaths  []string             `json:"allow_read_paths"  yaml:"-" env:"PICOCLAW_TOOLS_ALLOW_READ_PATHS"`
	AllowWritePaths []string             `json:"allow_write_paths" yaml:"-" env:"PICOCLAW_TOOLS_ALLOW_WRITE_PATHS"`
	Adaptation      ToolAdaptationConfig `json:"adaptation"        yaml:"-"                                        envPrefix:"PICOCLAW_TOOLS_ADAPTATION_"`
	// FilterSensitiveData controls whether to filter sensitive values (API keys,
	// tokens, secrets) from tool results before sending to the LLM.
	// Default: true (enabled)
	FilterSensitiveData bool `json:"filter_sensitive_data" yaml:"-" env:"PICOCLAW_TOOLS_FILTER_SENSITIVE_DATA"`
	// FilterMinLength is the minimum content length required for filtering.
	// Content shorter than this will be returned unchanged for performance.
	// Default: 8
	FilterMinLength int                `json:"filter_min_length" yaml:"-"                env:"PICOCLAW_TOOLS_FILTER_MIN_LENGTH"`
	Web             WebToolsConfig     `json:"web"               yaml:"web,omitempty"`
	Cron            CronToolsConfig    `json:"cron"              yaml:"-"`
	Exec            ExecConfig         `json:"exec"              yaml:"-"`
	Skills          SkillsToolsConfig  `json:"skills"            yaml:"skills,omitempty"`
	MediaCleanup    MediaCleanupConfig `json:"media_cleanup"     yaml:"-"`
	MCP             MCPConfig          `json:"mcp"               yaml:"-"`
	AppendFile      ToolConfig         `json:"append_file"       yaml:"-"                                                       envPrefix:"PICOCLAW_TOOLS_APPEND_FILE_"`
	EditFile        ToolConfig         `json:"edit_file"         yaml:"-"                                                       envPrefix:"PICOCLAW_TOOLS_EDIT_FILE_"`
	FindSkills      ToolConfig         `json:"find_skills"       yaml:"-"                                                       envPrefix:"PICOCLAW_TOOLS_FIND_SKILLS_"`
	I2C             ToolConfig         `json:"i2c"               yaml:"-"                                                       envPrefix:"PICOCLAW_TOOLS_I2C_"`
	InstallSkill    ToolConfig         `json:"install_skill"     yaml:"-"                                                       envPrefix:"PICOCLAW_TOOLS_INSTALL_SKILL_"`
	ListDir         ToolConfig         `json:"list_dir"          yaml:"-"                                                       envPrefix:"PICOCLAW_TOOLS_LIST_DIR_"`
	LoadImage       ToolConfig         `json:"load_image"        yaml:"-"                                                       envPrefix:"PICOCLAW_TOOLS_LOAD_IMAGE_"`
	Message         MessageToolsConfig `json:"message"           yaml:"-"`
	ReadFile        ReadFileToolConfig `json:"read_file"         yaml:"-"                                                       envPrefix:"PICOCLAW_TOOLS_READ_FILE_"`
	Serial          ToolConfig         `json:"serial"            yaml:"-"                                                       envPrefix:"PICOCLAW_TOOLS_SERIAL_"`
	SendFile        ToolConfig         `json:"send_file"         yaml:"-"                                                       envPrefix:"PICOCLAW_TOOLS_SEND_FILE_"`
	SendTTS         ToolConfig         `json:"send_tts"          yaml:"-"                                                       envPrefix:"PICOCLAW_TOOLS_SEND_TTS_"`
	Spawn           ToolConfig         `json:"spawn"             yaml:"-"                                                       envPrefix:"PICOCLAW_TOOLS_SPAWN_"`
	SpawnStatus     ToolConfig         `json:"spawn_status"      yaml:"-"                                                       envPrefix:"PICOCLAW_TOOLS_SPAWN_STATUS_"`
	SPI             ToolConfig         `json:"spi"               yaml:"-"                                                       envPrefix:"PICOCLAW_TOOLS_SPI_"`
	Subagent        ToolConfig         `json:"subagent"          yaml:"-"                                                       envPrefix:"PICOCLAW_TOOLS_SUBAGENT_"`
	Threads         ThreadsToolConfig  `json:"threads"           yaml:"-"`
	WebFetch        ToolConfig         `json:"web_fetch"         yaml:"-"                                                       envPrefix:"PICOCLAW_TOOLS_WEB_FETCH_"`
	GitWorkspace    ToolConfig         `json:"git_workspace"     yaml:"-"                                                       envPrefix:"PICOCLAW_TOOLS_GIT_WORKSPACE_"`
	Workflow        ToolConfig         `json:"workflow"          yaml:"-"                                                       envPrefix:"PICOCLAW_TOOLS_WORKFLOW_"`
	WriteFile       ToolConfig         `json:"write_file"        yaml:"-"                                                       envPrefix:"PICOCLAW_TOOLS_WRITE_FILE_"`
}

// IsFilterSensitiveDataEnabled returns true if sensitive data filtering is enabled
func (c *ToolsConfig) IsFilterSensitiveDataEnabled() bool {
	return c.FilterSensitiveData
}

// GetFilterMinLength returns the minimum content length for filtering (default: 8)
func (c *ToolsConfig) GetFilterMinLength() int {
	if c.FilterMinLength <= 0 {
		return 8
	}
	return c.FilterMinLength
}

type SearchCacheConfig struct {
	MaxSize    int `json:"max_size"    env:"PICOCLAW_SKILLS_SEARCH_CACHE_MAX_SIZE"`
	TTLSeconds int `json:"ttl_seconds" env:"PICOCLAW_SKILLS_SEARCH_CACHE_TTL_SECONDS"`
}

type SkillsRegistriesConfig []*SkillRegistryConfig

func (c *SkillsRegistriesConfig) Get(name string) (SkillRegistryConfig, bool) {
	if c == nil {
		return SkillRegistryConfig{}, false
	}
	name = strings.TrimSpace(name)
	if name == "" {
		return SkillRegistryConfig{}, false
	}
	for _, registry := range *c {
		if registry == nil || registry.Name != name {
			continue
		}
		return *registry, true
	}
	return SkillRegistryConfig{}, false
}

func (c *SkillsRegistriesConfig) Set(name string, cfg SkillRegistryConfig) {
	if c == nil {
		return
	}
	name = strings.TrimSpace(name)
	if name == "" {
		return
	}
	cfg.Name = name
	for i, registry := range *c {
		if registry == nil || registry.Name != name {
			continue
		}
		(*c)[i] = &cfg
		return
	}
	*c = append(*c, &cfg)
}

type SkillsGithubConfig struct {
	BaseURL string       `json:"base_url,omitempty" yaml:"-"               env:"PICOCLAW_TOOLS_SKILLS_GITHUB_BASE_URL"`
	Token   SecureString `json:"token,omitzero"     yaml:"token,omitempty" env:"PICOCLAW_TOOLS_SKILLS_GITHUB_TOKEN"`
	Proxy   string       `json:"proxy,omitempty"    yaml:"-"               env:"PICOCLAW_TOOLS_SKILLS_GITHUB_PROXY"`
}

type SkillRegistryConfig struct {
	Name      string         `json:"name,omitempty"      yaml:"-"                    env:"-"`
	Enabled   bool           `json:"enabled"             yaml:"-"                    env:"-"`
	BaseURL   string         `json:"base_url"            yaml:"-"                    env:"-"`
	AuthToken SecureString   `json:"auth_token,omitzero" yaml:"auth_token,omitempty" env:"-"`
	Param     map[string]any `json:"-"                   yaml:"-"                    env:"-"`
}

const (
	envSkillsClawHubEnabled         = "PICOCLAW_SKILLS_REGISTRIES_CLAWHUB_ENABLED"
	envSkillsClawHubBaseURL         = "PICOCLAW_SKILLS_REGISTRIES_CLAWHUB_BASE_URL"
	envSkillsClawHubAuthToken       = "PICOCLAW_SKILLS_REGISTRIES_CLAWHUB_AUTH_TOKEN"
	envSkillsClawHubSearchPath      = "PICOCLAW_SKILLS_REGISTRIES_CLAWHUB_SEARCH_PATH"
	envSkillsClawHubSkillsPath      = "PICOCLAW_SKILLS_REGISTRIES_CLAWHUB_SKILLS_PATH"
	envSkillsClawHubDownloadPath    = "PICOCLAW_SKILLS_REGISTRIES_CLAWHUB_DOWNLOAD_PATH"
	envSkillsClawHubTimeout         = "PICOCLAW_SKILLS_REGISTRIES_CLAWHUB_TIMEOUT"
	envSkillsClawHubMaxZipSize      = "PICOCLAW_SKILLS_REGISTRIES_CLAWHUB_MAX_ZIP_SIZE"
	envSkillsClawHubMaxResponseSize = "PICOCLAW_SKILLS_REGISTRIES_CLAWHUB_MAX_RESPONSE_SIZE"
	envSkillsGitHubEnabled          = "PICOCLAW_SKILLS_REGISTRIES_GITHUB_ENABLED"
	envSkillsGitHubBaseURL          = "PICOCLAW_SKILLS_REGISTRIES_GITHUB_BASE_URL"
	envSkillsGitHubAuthToken        = "PICOCLAW_SKILLS_REGISTRIES_GITHUB_AUTH_TOKEN"
	envSkillsGitHubProxy            = "PICOCLAW_SKILLS_REGISTRIES_GITHUB_PROXY"
)

func (c *SkillRegistryConfig) DecodeParam(target any) error {
	if c == nil {
		return nil
	}
	if len(c.Param) == 0 {
		return nil
	}
	data, err := json.Marshal(c.Param)
	if err != nil {
		return err
	}
	return json.Unmarshal(data, target)
}

// MCPServerAuthConfig links a remote MCP server to a credential in the auth store.
// Tokens are deliberately kept out of config.json.
type MCPServerAuthConfig struct {
	// Type is "bearer" or "oauth". Both use the stored access token as a Bearer token.
	Type string `json:"type,omitempty"`
	// CredentialID is the auth-store key. When omitted, "mcp:<server-name>" is used.
	CredentialID string `json:"credential_id,omitempty"`
	// Revision changes whenever credential material changes so a running gateway
	// can detect that it must reconnect without exposing the credential itself.
	Revision int64 `json:"revision,omitempty"`
}

// MCPServerConfig defines configuration for a single MCP server
type MCPServerConfig struct {
	// Enabled indicates whether this MCP server is active
	Enabled bool `json:"enabled"`
	// Deferred controls whether this server's tools are registered as hidden (deferred/discovery mode).
	// When nil, the global Discovery.Enabled setting applies.
	// When explicitly set to true or false, it overrides the global setting for this server only.
	Deferred *bool `json:"deferred,omitempty"`
	// Command is the executable to run (e.g., "npx", "python", "/path/to/server")
	Command string `json:"command"`
	// Args are the arguments to pass to the command
	Args []string `json:"args,omitempty"`
	// Env are environment variables to set for the server process (stdio only)
	Env map[string]string `json:"env,omitempty"`
	// EnvFile is the path to a file containing environment variables (stdio only)
	EnvFile string `json:"env_file,omitempty"`
	// Type is "stdio", "sse", "http", or "streamable-http".
	// "http" and "streamable-http" both select streamable HTTP request-response
	// mode, while "sse" keeps the standalone SSE listener enabled for
	// server-initiated notifications. Defaults: stdio if command is set, sse if
	// url is set.
	Type string `json:"type,omitempty"`
	// URL is used for SSE/HTTP transport
	URL string `json:"url,omitempty"`
	// Headers are HTTP headers to send with requests (sse/http only)
	Headers map[string]string `json:"headers,omitempty"`
	// Auth references a credential stored outside config.json (sse/http only).
	Auth *MCPServerAuthConfig `json:"auth,omitempty"`
}

// MCPConfig defines configuration for all MCP servers
type MCPConfig struct {
	ToolConfig `                    envPrefix:"PICOCLAW_TOOLS_MCP_"`
	Discovery  ToolDiscoveryConfig `                                json:"discovery"`
	// MaxInlineTextChars controls how much MCP text stays inline before it is saved as an artifact.
	MaxInlineTextChars int `json:"max_inline_text_chars,omitempty" env:"PICOCLAW_TOOLS_MCP_MAX_INLINE_TEXT_CHARS"`
	// Servers is a map of server name to server configuration
	Servers map[string]MCPServerConfig `json:"servers,omitempty"`
}

const DefaultMCPMaxInlineTextChars = 16 * 1024

func (c *MCPConfig) GetMaxInlineTextChars() int {
	if c.MaxInlineTextChars > 0 {
		return c.MaxInlineTextChars
	}
	return DefaultMCPMaxInlineTextChars
}

// LoadConfig loads and fully validates the runtime configuration.
func LoadConfig(path string) (*Config, error) {
	return loadConfigWithOptions(path, true)
}

// LoadConfigForUpdate loads configuration for a management transaction while
// deferring event-webhook secret resolution and ingress validation. This lets a
// request replace or disable a broken reference before the final candidate is
// resolved and validated.
func LoadConfigForUpdate(path string) (*Config, error) {
	return loadConfigWithOptions(path, false)
}

func loadConfigWithOptions(path string, validateEventIngressRuntime bool) (*Config, error) {
	updateResolver(filepath.Dir(path))

	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			logger.WarnF(
				"config file not found, using default config",
				map[string]any{"path": path},
			)
			return DefaultConfig(), nil
		}
		return nil, err
	}

	// First, try to detect config version by reading the version field
	var versionInfo struct {
		Version int `json:"version"`
	}
	if e := json.Unmarshal(data, &versionInfo); e != nil {
		e = wrapJSONError(data, e, "config.json")
		logger.ErrorCF(
			"config",
			formatDiagnosticLogMessage("Malformed config file", e),
			map[string]any{"path": path},
		)
		return nil, e
	}
	if len(data) <= 10 {
		logger.Warn(fmt.Sprintf("content is [%s]", string(data)))
		return DefaultConfig(), nil
	}

	// Load config based on detected version
	var cfg *Config
	migratedFrom := -1
	switch versionInfo.Version {
	case 0:
		logger.InfoF(
			"config migrate start",
			map[string]any{"from": versionInfo.Version, "to": CurrentVersion},
		)
		if err = validateLegacyConfigDiagnostics(data); err != nil {
			logger.ErrorCF(
				"config",
				formatDiagnosticLogMessage("Failed to load config", err),
				map[string]any{"path": path},
			)
			return nil, err
		}

		var m map[string]any
		m, err = loadConfigMap(path)
		if err != nil {
			logger.ErrorCF(
				"config",
				formatDiagnosticLogMessage("Failed to load config", err),
				map[string]any{"path": path},
			)
			return nil, err
		}

		migrateErr := migrateV0ToV1(m)
		if migrateErr != nil {
			return nil, fmt.Errorf("V0→V1 migration failed: %w", migrateErr)
		}
		migrateErr = migrateV1ToV2(m)
		if migrateErr != nil {
			return nil, fmt.Errorf("V1→V2 migration failed: %w", migrateErr)
		}
		migrateErr = migrateV2ToV3(m)
		if migrateErr != nil {
			return nil, fmt.Errorf("V2→V3 migration failed: %w", migrateErr)
		}
		migrateErr = migrateV3ToV4(m)
		if migrateErr != nil {
			return nil, fmt.Errorf("V3→V4 migration failed: %w", migrateErr)
		}
		migrateErr = migrateV4ToV5(m)
		if migrateErr != nil {
			return nil, fmt.Errorf("V4→V5 migration failed: %w", migrateErr)
		}
		migrateErr = migrateV5ToV6(m)
		if migrateErr != nil {
			return nil, fmt.Errorf("V5→V6 migration failed: %w", migrateErr)
		}

		var migrated []byte
		migrated, err = json.Marshal(m)
		if err != nil {
			return nil, err
		}

		cfg, err = loadConfig(migrated)
		if err != nil {
			return nil, err
		}

		err = MakeBackup(path)
		if err != nil {
			return nil, err
		}

		migratedFrom = versionInfo.Version
	case 1:
		// V1→V6 migration: infer Enabled, migrate channels, introduce semantic
		// model aliases, and admit trusted review-attention policy storage.
		logger.InfoF(
			"config migrate start",
			map[string]any{"from": versionInfo.Version, "to": CurrentVersion},
		)
		if err = validateLegacyConfigDiagnostics(data); err != nil {
			logger.ErrorCF(
				"config",
				formatDiagnosticLogMessage("Failed to load config", err),
				map[string]any{"path": path},
			)
			return nil, err
		}

		var m map[string]any
		m, err = loadConfigMap(path)
		if err != nil {
			logger.ErrorCF(
				"config",
				formatDiagnosticLogMessage("Failed to load config", err),
				map[string]any{"path": path},
			)
			return nil, err
		}

		migrateErr := migrateV1ToV2(m)
		if migrateErr != nil {
			return nil, fmt.Errorf("V1→V2 migration failed: %w", migrateErr)
		}
		migrateErr = migrateV2ToV3(m)
		if migrateErr != nil {
			return nil, fmt.Errorf("V2→V3 migration failed: %w", migrateErr)
		}
		migrateErr = migrateV3ToV4(m)
		if migrateErr != nil {
			return nil, fmt.Errorf("V3→V4 migration failed: %w", migrateErr)
		}
		migrateErr = migrateV4ToV5(m)
		if migrateErr != nil {
			return nil, fmt.Errorf("V4→V5 migration failed: %w", migrateErr)
		}
		migrateErr = migrateV5ToV6(m)
		if migrateErr != nil {
			return nil, fmt.Errorf("V5→V6 migration failed: %w", migrateErr)
		}

		var migrated []byte
		migrated, err = json.Marshal(m)
		if err != nil {
			return nil, err
		}

		cfg, err = loadConfig(migrated)
		if err != nil {
			return nil, err
		}

		err = MakeBackup(path)
		if err != nil {
			return nil, err
		}

		migratedFrom = versionInfo.Version
	case 2:
		// V2→V6 migration: migrate channels, introduce semantic model aliases,
		// and admit trusted review-attention policy storage.
		logger.InfoF(
			"config migrate start",
			map[string]any{"from": versionInfo.Version, "to": CurrentVersion},
		)
		if err = validateLegacyConfigDiagnostics(data); err != nil {
			logger.ErrorCF(
				"config",
				formatDiagnosticLogMessage("Failed to load config", err),
				map[string]any{"path": path},
			)
			return nil, err
		}
		var m map[string]any
		m, err = loadConfigMap(path)
		if err != nil {
			logger.ErrorCF(
				"config",
				formatDiagnosticLogMessage("Failed to load config", err),
				map[string]any{"path": path},
			)
			return nil, err
		}
		migrateErr := migrateV2ToV3(m)
		if migrateErr != nil {
			return nil, fmt.Errorf("V2→V3 migration failed: %w", migrateErr)
		}
		migrateErr = migrateV3ToV4(m)
		if migrateErr != nil {
			return nil, fmt.Errorf("V3→V4 migration failed: %w", migrateErr)
		}
		migrateErr = migrateV4ToV5(m)
		if migrateErr != nil {
			return nil, fmt.Errorf("V4→V5 migration failed: %w", migrateErr)
		}
		migrateErr = migrateV5ToV6(m)
		if migrateErr != nil {
			return nil, fmt.Errorf("V5→V6 migration failed: %w", migrateErr)
		}

		var migrated []byte
		migrated, err = json.Marshal(m)
		if err != nil {
			return nil, err
		}

		cfg, err = loadConfig(migrated)
		if err != nil {
			return nil, err
		}

		err = MakeBackup(path)
		if err != nil {
			return nil, err
		}

		migratedFrom = versionInfo.Version
	case 3:
		// V3→V6 migration: separate account selection, normalize semantic
		// aliases, and admit trusted review-attention policy storage.
		logger.InfoF(
			"config migrate start",
			map[string]any{"from": versionInfo.Version, "to": CurrentVersion},
		)
		if err = validateLegacyConfigDiagnostics(data); err != nil {
			logger.ErrorCF(
				"config",
				formatDiagnosticLogMessage("Failed to load config", err),
				map[string]any{"path": path},
			)
			return nil, err
		}
		var m map[string]any
		m, err = loadConfigMap(path)
		if err != nil {
			logger.ErrorCF(
				"config",
				formatDiagnosticLogMessage("Failed to load config", err),
				map[string]any{"path": path},
			)
			return nil, err
		}
		migrateErr := migrateV3ToV4(m)
		if migrateErr != nil {
			return nil, fmt.Errorf("V3→V4 migration failed: %w", migrateErr)
		}
		migrateErr = migrateV4ToV5(m)
		if migrateErr != nil {
			return nil, fmt.Errorf("V4→V5 migration failed: %w", migrateErr)
		}
		migrateErr = migrateV5ToV6(m)
		if migrateErr != nil {
			return nil, fmt.Errorf("V5→V6 migration failed: %w", migrateErr)
		}

		var migrated []byte
		migrated, err = json.Marshal(m)
		if err != nil {
			return nil, err
		}
		cfg, err = loadConfig(migrated)
		if err != nil {
			return nil, err
		}
		err = MakeBackup(path)
		if err != nil {
			return nil, err
		}
		migratedFrom = versionInfo.Version
	case 4:
		// V4→V6 migration: remove mechanically generated account/model aliases,
		// then admit trusted review-attention policy storage.
		logger.InfoF(
			"config migrate start",
			map[string]any{"from": versionInfo.Version, "to": CurrentVersion},
		)
		var m map[string]any
		m, err = loadConfigMap(path)
		if err != nil {
			return nil, err
		}
		if migrateErr := migrateV4ToV5(m); migrateErr != nil {
			return nil, fmt.Errorf("V4→V5 migration failed: %w", migrateErr)
		}
		if migrateErr := migrateV5ToV6(m); migrateErr != nil {
			return nil, fmt.Errorf("V5→V6 migration failed: %w", migrateErr)
		}
		var migrated []byte
		migrated, err = json.Marshal(m)
		if err != nil {
			return nil, err
		}
		cfg, err = loadConfig(migrated)
		if err != nil {
			return nil, err
		}
		if err = MakeBackup(path); err != nil {
			return nil, err
		}
		migratedFrom = versionInfo.Version
	case 5:
		// V5→V6 is additive and preserves any preview review-attention policy.
		logger.InfoF(
			"config migrate start",
			map[string]any{"from": versionInfo.Version, "to": CurrentVersion},
		)
		var m map[string]any
		decoder := json.NewDecoder(bytes.NewReader(data))
		decoder.UseNumber()
		if err = decoder.Decode(&m); err != nil {
			return nil, wrapJSONError(data, err, "config.json")
		}
		if migrateErr := migrateV5ToV6(m); migrateErr != nil {
			return nil, fmt.Errorf("V5→V6 migration failed: %w", migrateErr)
		}
		var migrated []byte
		migrated, err = json.Marshal(m)
		if err != nil {
			return nil, err
		}
		cfg, err = loadConfig(migrated)
		if err != nil {
			return nil, err
		}
		// V5 was the current schema before this additive migration, so preserve
		// its existing security-overlay load semantics while advancing to V6.
		secPath := securityPath(path)
		if err = loadSecurityConfig(cfg, secPath); err != nil && !errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("failed to load security config: %w", err)
		}
		if err = MakeBackup(path); err != nil {
			return nil, err
		}
		migratedFrom = versionInfo.Version
	case CurrentVersion:
		// Current version
		cfg, err = loadConfig(data)
		if err != nil {
			logger.ErrorCF(
				"config",
				formatDiagnosticLogMessage("Failed to load config", err),
				map[string]any{"path": path},
			)
			return nil, err
		}
		// Load security configuration
		secPath := securityPath(path)
		err = loadSecurityConfig(cfg, secPath)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("failed to load security config: %w", err)
		}

	default:
		return nil, fmt.Errorf("unsupported config version: %d", versionInfo.Version)
	}
	if err = cfg.Isolation.ValidateEnvironmentAllowlist(); err != nil {
		return nil, fmt.Errorf("invalid isolation config: %w", err)
	}

	applyLegacyBindingsMigration(data, cfg)

	gatewayHostBeforeEnv := cfg.Gateway.Host

	if err = env.Parse(cfg); err != nil {
		return nil, err
	}
	if validateEventIngressRuntime {
		if err = cfg.Events.Ingress.resolveWebhookSecrets(); err != nil {
			return nil, fmt.Errorf("resolve event ingress secrets: %w", err)
		}
	}
	applySkillsRegistryEnvCompat(cfg)
	if validateEventIngressRuntime {
		if err = cfg.Events.Ingress.Validate(); err != nil {
			return nil, fmt.Errorf("invalid event ingress config: %w", err)
		}
	}

	if err = InitChannelList(cfg.Channels); err != nil {
		return nil, err
	}
	if validateEventIngressRuntime {
		if err = cfg.Events.Ingress.ValidatePublicIdentities(
			cfg.SensitiveDataValues()...,
		); err != nil {
			return nil, fmt.Errorf("invalid event ingress public identity: %w", err)
		}
		if err = cfg.Events.Ingress.ValidateEventChannelAdapters(
			cfg.Channels,
			cfg.SensitiveDataValues()...,
		); err != nil {
			return nil, fmt.Errorf("invalid event channel ingress config: %w", err)
		}
	}
	if err = cfg.ValidateTurnProfile(); err != nil {
		return nil, err
	}
	if cfg.PRLifecycle.IsZero() {
		cfg.PRLifecycle = DefaultPRLifecycleConfig()
	}
	if err = cfg.PRLifecycle.Validate(); err != nil {
		return nil, fmt.Errorf("invalid PR lifecycle config: %w", err)
	}
	if err = cfg.PRLifecycle.ValidateAgentReferences(cfg.Agents); err != nil {
		return nil, fmt.Errorf("invalid PR lifecycle config: %w", err)
	}
	cfg.Tools.Adaptation = cfg.Tools.Adaptation.Normalized()
	cfg.Gateway.Host, err = resolveGatewayHostFromEnv(gatewayHostBeforeEnv)
	if err != nil {
		return nil, fmt.Errorf("invalid gateway host: %w", err)
	}

	// Expand multi-key configs into separate entries for key-level failover
	cfg.ModelList = expandMultiKeyModels(cfg.ModelList)
	cfg.MaterializeAccountRouterModels()
	cfg.MaterializeModelRouterModels()

	// Validate model_list for uniqueness and required fields
	if err = cfg.ValidateModelList(); err != nil {
		return nil, err
	}
	if err = cfg.ValidateModelSelections(); err != nil {
		return nil, err
	}

	// Ensure Workspace has a default if not set
	if cfg.Agents.Defaults.Workspace == "" {
		homePath := GetHome()
		cfg.Agents.Defaults.Workspace = filepath.Join(homePath, pkg.WorkspaceName)
	}

	cfg.Session.ApplyDmScope()
	cfg.Session.DeriveDmScope()

	if migratedFrom >= 0 {
		if saveErr := SaveConfig(path, cfg); saveErr != nil {
			logger.WarnF(
				"config migration validated but could not be persisted",
				map[string]any{
					"error": saveErr.Error(),
					"from":  migratedFrom,
					"to":    CurrentVersion,
				},
			)
		} else {
			logger.InfoF(
				"config migrate success",
				map[string]any{"from": migratedFrom, "to": CurrentVersion},
			)
		}
	}

	return cfg, nil
}

func applySkillsRegistryEnvCompat(cfg *Config) {
	if cfg == nil {
		return
	}

	registryCfg, foundClawHub := cfg.Tools.Skills.Registries.Get("clawhub")
	if !foundClawHub {
		registryCfg = SkillRegistryConfig{
			Name:  "clawhub",
			Param: map[string]any{},
		}
	}
	if registryCfg.Param == nil {
		registryCfg.Param = map[string]any{}
	}

	if raw, envSet := os.LookupEnv(envSkillsClawHubEnabled); envSet {
		if value, err := strconv.ParseBool(strings.TrimSpace(raw)); err == nil {
			registryCfg.Enabled = value
		}
	}
	if value, envSet := os.LookupEnv(envSkillsClawHubBaseURL); envSet {
		registryCfg.BaseURL = value
	}
	if value, envSet := os.LookupEnv(envSkillsClawHubAuthToken); envSet {
		registryCfg.AuthToken = *NewSecureString(value)
	}
	if value, envSet := os.LookupEnv(envSkillsClawHubSearchPath); envSet {
		registryCfg.Param["search_path"] = value
	}
	if value, envSet := os.LookupEnv(envSkillsClawHubSkillsPath); envSet {
		registryCfg.Param["skills_path"] = value
	}
	if value, envSet := os.LookupEnv(envSkillsClawHubDownloadPath); envSet {
		registryCfg.Param["download_path"] = value
	}
	if raw, envSet := os.LookupEnv(envSkillsClawHubTimeout); envSet {
		if value, err := strconv.Atoi(strings.TrimSpace(raw)); err == nil {
			registryCfg.Param["timeout"] = value
		}
	}
	if raw, envSet := os.LookupEnv(envSkillsClawHubMaxZipSize); envSet {
		if value, err := strconv.Atoi(strings.TrimSpace(raw)); err == nil {
			registryCfg.Param["max_zip_size"] = value
		}
	}
	if raw, envSet := os.LookupEnv(envSkillsClawHubMaxResponseSize); envSet {
		if value, err := strconv.Atoi(strings.TrimSpace(raw)); err == nil {
			registryCfg.Param["max_response_size"] = value
		}
	}

	cfg.Tools.Skills.Registries.Set("clawhub", registryCfg)

	githubCfg, foundGitHub := cfg.Tools.Skills.Registries.Get("github")
	if !foundGitHub {
		githubCfg = SkillRegistryConfig{
			Name:  "github",
			Param: map[string]any{},
		}
	}
	if githubCfg.Param == nil {
		githubCfg.Param = map[string]any{}
	}

	if raw, envSet := os.LookupEnv(envSkillsGitHubEnabled); envSet {
		if value, err := strconv.ParseBool(strings.TrimSpace(raw)); err == nil {
			githubCfg.Enabled = value
		}
	}
	if value, envSet := os.LookupEnv(envSkillsGitHubBaseURL); envSet {
		githubCfg.BaseURL = value
	}
	if value, envSet := os.LookupEnv(envSkillsGitHubAuthToken); envSet {
		githubCfg.AuthToken = *NewSecureString(value)
	}
	if value, envSet := os.LookupEnv(envSkillsGitHubProxy); envSet {
		githubCfg.Param["proxy"] = value
	}

	cfg.Tools.Skills.Registries.Set("github", githubCfg)
}

func MakeBackup(path string) error {
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return nil
	}
	dateSuffix := time.Now().Format(".20060102.bak")
	// Backup config file
	bakPath := path + dateSuffix
	if err := fileutil.CopyFile(path, bakPath, 0o600); err != nil {
		logger.ErrorF("failed to create config backup", map[string]any{"error": err})
		return fmt.Errorf("failed to create config backup: %w", err)
	}
	// Backup security config file
	secPath := securityPath(path)
	if _, err := os.Stat(secPath); err == nil {
		secBakPath := secPath + dateSuffix
		if secErr := fileutil.CopyFile(secPath, secBakPath, 0o600); secErr != nil {
			logger.ErrorF("failed to create security backup", map[string]any{"error": secErr})
			return fmt.Errorf("failed to create security backup: %w", secErr)
		}
	}
	return nil
}

func toNameIndex(list []*ModelConfig) []string {
	nameList := make([]string, 0, len(list))
	countMap := make(map[string]int)
	for _, model := range list {
		name := model.ModelName
		index := countMap[name]
		nameList = append(nameList, fmt.Sprintf("%s:%d", name, index))
		countMap[name]++
	}
	return nameList
}

func SaveConfig(path string, cfg *Config) error {
	unlock, err := lockConfigMutation(path)
	if err != nil {
		return err
	}
	defer unlock()
	return saveConfigUnlocked(path, cfg)
}

func saveConfigUnlocked(path string, cfg *Config) error {
	if cfg == nil {
		return errors.New("config is required")
	}
	if err := cfg.Isolation.ValidateEnvironmentAllowlist(); err != nil {
		return fmt.Errorf("invalid isolation config: %w", err)
	}
	if cfg.PRLifecycle.IsZero() {
		cfg.PRLifecycle = DefaultPRLifecycleConfig()
	}
	if err := cfg.PRLifecycle.Validate(); err != nil {
		return fmt.Errorf("invalid PR lifecycle config: %w", err)
	}
	if err := cfg.PRLifecycle.ValidateAgentReferences(cfg.Agents); err != nil {
		return fmt.Errorf("invalid PR lifecycle config: %w", err)
	}
	if err := cfg.Events.Ingress.ValidatePublicIdentities(
		cfg.SensitiveDataValues()...,
	); err != nil {
		return err
	}
	if cfg.Version < CurrentVersion {
		cfg.Version = CurrentVersion
	}
	// Filter out virtual models before serializing to config file
	nonVirtualModels := make([]*ModelConfig, 0, len(cfg.ModelList))
	for _, m := range cfg.ModelList {
		if !m.isVirtual {
			nonVirtualModels = append(nonVirtualModels, m)
		}
	}
	// Temporarily replace ModelList with filtered version for serialization
	originalModelList := cfg.ModelList
	defer func() {
		// Restore original ModelList after serialization
		cfg.ModelList = originalModelList
	}()
	cfg.ModelList = nonVirtualModels

	if err := saveSecurityConfig(securityPath(path), cfg); err != nil {
		logger.ErrorCF("config", "cannot save .security.yml", map[string]any{"error": err})
		return err
	}

	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	return fileutil.WriteFileAtomic(path, data, 0o600)
}

func (c *Config) WorkspacePath() string {
	return expandHome(c.Agents.Defaults.Workspace)
}

func (c *Config) GitWorkspaceRootPath() string {
	if c == nil {
		return filepath.Join(GetHome(), pkg.WorkspaceName, ".git-workspaces")
	}
	return c.GitWorkspaces.EffectiveRootDir(c.WorkspacePath())
}

func expandHome(path string) string {
	if path == "" {
		return path
	}
	if path[0] == '~' {
		home, _ := os.UserHomeDir()
		if len(path) > 1 && path[1] == '/' {
			return home + path[1:]
		}
		return home
	}
	return path
}

// GetModelAlias returns the alias with the exact configured name. Alias
// lookup is intentionally exact: it never treats a raw model identifier,
// account name, or router name as an alias.
func (c *Config) GetModelAlias(name string) (*ModelAliasConfig, error) {
	if strings.TrimSpace(name) == "" {
		return nil, ErrNoModelConfigured
	}
	if c != nil {
		for i := range c.ModelAliases {
			if c.ModelAliases[i].Name == name {
				return &c.ModelAliases[i], nil
			}
		}
	}
	return nil, fmt.Errorf("model alias %q is not configured", name)
}

// ResolveModelAlias resolves an exact alias for a concrete account. A
// per-account override wins when present; account routers are not valid
// override keys and are rejected during validation.
func (c *Config) ResolveModelAlias(aliasName, concreteAccountRef string) (string, error) {
	if err := c.validateConcreteModelAccountRef(concreteAccountRef); err != nil {
		return "", err
	}
	alias, err := c.GetModelAlias(aliasName)
	if err != nil {
		return "", err
	}
	for _, disabledAccountRef := range alias.DisabledAccounts {
		if disabledAccountRef == concreteAccountRef {
			return "", fmt.Errorf(
				"%w: model alias %q is disabled for account %q",
				ErrModelAliasDisabled,
				aliasName,
				concreteAccountRef,
			)
		}
	}
	if alias.AccountOverrides != nil {
		if model, ok := alias.AccountOverrides[concreteAccountRef]; ok {
			if strings.TrimSpace(model) == "" {
				return "", ErrNoModelConfigured
			}
			return model, nil
		}
	}
	if strings.TrimSpace(alias.Model) == "" {
		return "", ErrNoModelConfigured
	}
	return alias.Model, nil
}

// ResolveModelAliasConfig applies an exact model alias to a concrete
// model_list account without mutating the stored account configuration.
// Credential-backed virtual accounts are resolved by the runtime that owns
// the credential store and therefore are not accepted here.
func (c *Config) ResolveModelAliasConfig(
	aliasName string,
	concreteAccountRef string,
) (*ModelConfig, error) {
	concreteAccountRef = strings.TrimSpace(concreteAccountRef)
	if concreteAccountRef == "" {
		return nil, fmt.Errorf("no account configured")
	}
	model, err := c.ResolveModelAlias(aliasName, concreteAccountRef)
	if err != nil {
		return nil, err
	}
	account, err := c.GetEnabledModelConfig(concreteAccountRef)
	if err != nil {
		return nil, fmt.Errorf("account %q is not configured: %w", concreteAccountRef, err)
	}
	if account.IsAccountRouter() || account.IsModelRouter() {
		return nil, fmt.Errorf("account %q is not a concrete account", concreteAccountRef)
	}
	clone := *account
	clone.Model = model
	return &clone, nil
}

// ValidateModelAliases validates alias identity, concrete model mappings, and
// direct-account-only overrides.
func (c *Config) ValidateModelAliases() error {
	if c == nil {
		return nil
	}

	accountRouters := make(map[string]struct{}, len(c.AccountRouters))
	for i := range c.AccountRouters {
		if name := c.AccountRouters[i].Name; strings.TrimSpace(name) != "" {
			accountRouters[name] = struct{}{}
		}
	}
	modelRouters := make(map[string]struct{}, len(c.ModelRouters))
	for i := range c.ModelRouters {
		if name := c.ModelRouters[i].Name; strings.TrimSpace(name) != "" {
			modelRouters[name] = struct{}{}
		}
	}
	concreteAccounts := make(map[string]struct{}, len(c.ModelList))
	concreteAccountProviders := make(map[string][]string, len(c.ModelList))
	for _, model := range c.ModelList {
		if model == nil ||
			!model.Enabled ||
			model.IsAccountRouter() ||
			model.IsModelRouter() {
			continue
		}
		if name := model.ModelName; strings.TrimSpace(name) != "" {
			concreteAccounts[name] = struct{}{}
			provider := strings.TrimSpace(model.Provider)
			if provider == "" {
				provider, _ = protocoltypes.SplitKnownProviderModel(model.Model)
				if provider == "" {
					provider = "openai"
				}
			}
			concreteAccountProviders[name] = append(
				concreteAccountProviders[name],
				provider,
			)
		}
	}

	seen := make(map[string]int, len(c.ModelAliases))
	for i := range c.ModelAliases {
		alias := &c.ModelAliases[i]
		if strings.TrimSpace(alias.Name) == "" {
			return fmt.Errorf("model_aliases[%d].name is required", i)
		}
		if previous, ok := seen[alias.Name]; ok {
			return fmt.Errorf(
				"model_aliases[%d].name %q duplicates model_aliases[%d]",
				i,
				alias.Name,
				previous,
			)
		}
		seen[alias.Name] = i
		if _, ok := modelRouters[alias.Name]; ok {
			return fmt.Errorf(
				"model_aliases[%d].name %q conflicts with a model router",
				i,
				alias.Name,
			)
		}
		if err := validateConcreteModelIdentifier(alias.Model); err != nil {
			return fmt.Errorf("model_aliases[%d]: %w", i, err)
		}
		for accountRef, model := range alias.AccountOverrides {
			if strings.TrimSpace(accountRef) == "" {
				return fmt.Errorf(
					"model_aliases[%d].account_overrides contains an empty account reference",
					i,
				)
			}
			if err := validateConcreteModelIdentifier(model); err != nil {
				return fmt.Errorf(
					"model_aliases[%d].account_overrides[%q]: %w",
					i,
					accountRef,
					err,
				)
			}
			if _, ok := accountRouters[accountRef]; ok {
				return fmt.Errorf(
					"model_aliases[%d].account_overrides[%q] must reference a concrete account, not an account router",
					i,
					accountRef,
				)
			}
			if _, ok := modelRouters[accountRef]; ok {
				return fmt.Errorf(
					"model_aliases[%d].account_overrides[%q] must reference a concrete account, not a model router",
					i,
					accountRef,
				)
			}
			if _, ok := concreteAccounts[accountRef]; ok {
				for _, provider := range concreteAccountProviders[accountRef] {
					if err := validateModelProviderCompatibility(
						provider,
						model,
						false,
					); err != nil {
						return fmt.Errorf(
							"model_aliases[%d].account_overrides[%q]: %w",
							i,
							accountRef,
							err,
						)
					}
				}
				continue
			}
			if provider, ok := AccountRouterCredentialAccountProvider(accountRef); ok {
				if err := validateModelProviderCompatibility(
					provider,
					model,
					false,
				); err != nil {
					return fmt.Errorf(
						"model_aliases[%d].account_overrides[%q]: %w",
						i,
						accountRef,
						err,
					)
				}
				continue
			}
			return fmt.Errorf(
				"model_aliases[%d].account_overrides[%q] references an unknown concrete account",
				i,
				accountRef,
			)
		}
		disabledSeen := make(map[string]struct{}, len(alias.DisabledAccounts))
		for j, accountRef := range alias.DisabledAccounts {
			if strings.TrimSpace(accountRef) == "" {
				return fmt.Errorf(
					"model_aliases[%d].disabled_accounts[%d] is empty",
					i,
					j,
				)
			}
			if accountRef != strings.TrimSpace(accountRef) {
				return fmt.Errorf(
					"model_aliases[%d].disabled_accounts[%d] must be an exact account reference",
					i,
					j,
				)
			}
			if _, duplicate := disabledSeen[accountRef]; duplicate {
				return fmt.Errorf(
					"model_aliases[%d].disabled_accounts contains duplicate account %q",
					i,
					accountRef,
				)
			}
			disabledSeen[accountRef] = struct{}{}
			if _, overridden := alias.AccountOverrides[accountRef]; overridden {
				return fmt.Errorf(
					"model_aliases[%d] cannot both override and disable account %q",
					i,
					accountRef,
				)
			}
			if _, ok := accountRouters[accountRef]; ok {
				return fmt.Errorf(
					"model_aliases[%d].disabled_accounts[%d] must reference a concrete account, not an account router",
					i,
					j,
				)
			}
			if _, ok := modelRouters[accountRef]; ok {
				return fmt.Errorf(
					"model_aliases[%d].disabled_accounts[%d] must reference a concrete account, not a model router",
					i,
					j,
				)
			}
			if _, ok := concreteAccounts[accountRef]; ok {
				continue
			}
			if _, ok := AccountRouterCredentialAccountProvider(accountRef); ok {
				continue
			}
			return fmt.Errorf(
				"model_aliases[%d].disabled_accounts[%d] references an unknown concrete account",
				i,
				j,
			)
		}
	}
	return nil
}

// GetModelConfig returns the ModelConfig for the given model name.
// If multiple configs exist with the same model_name, it uses round-robin
// selection for load balancing. Returns an error if the model is not found.
func (c *Config) GetModelConfig(modelName string) (*ModelConfig, error) {
	matches := c.findMatches(modelName)
	return selectModelConfigMatch(modelName, matches)
}

// GetEnabledModelConfig returns an enabled ModelConfig for the exact account
// or virtual-router name. Disabled entries are excluded before round-robin
// selection so an inactive duplicate cannot be chosen by a runtime path.
//
// Virtual account and model routers remain eligible when enabled; callers
// that require a concrete account must continue to reject router configs.
func (c *Config) GetEnabledModelConfig(modelName string) (*ModelConfig, error) {
	matches := c.findEnabledMatches(modelName)
	return selectModelConfigMatch(modelName, matches)
}

func selectModelConfigMatch(
	modelName string,
	matches []*ModelConfig,
) (*ModelConfig, error) {
	if len(matches) == 0 {
		return nil, fmt.Errorf("model %q not found in model_list or providers", modelName)
	}
	if len(matches) == 1 {
		return matches[0], nil
	}

	// Multiple configs - use round-robin for load balancing
	idx := (rrCounter.Add(1) - 1) % uint64(len(matches))
	return matches[idx], nil
}

// findMatches finds all ModelConfig entries with the given model_name.
func (c *Config) findMatches(modelName string) []*ModelConfig {
	var matches []*ModelConfig
	for i := range c.ModelList {
		model := c.ModelList[i]
		if model != nil && model.ModelName == modelName {
			matches = append(matches, model)
		}
	}
	return matches
}

func (c *Config) findEnabledMatches(modelName string) []*ModelConfig {
	matches := c.findMatches(modelName)
	enabled := matches[:0]
	for _, model := range matches {
		if model.Enabled {
			enabled = append(enabled, model)
		}
	}
	return enabled
}

// ValidateModelList validates all ModelConfig entries in the model_list.
// It checks that each model config is valid.
// Note: Multiple entries with the same model_name are allowed for load balancing.
func (c *Config) ValidateModelList() error {
	for i := range c.ModelList {
		if c.ModelList[i] == nil {
			return fmt.Errorf("model_list[%d]: model config is required", i)
		}
		if c.ModelList[i].IsModelRouter() && !c.ModelList[i].IsVirtual() {
			return fmt.Errorf(
				"model_list[%d]: model routers must be configured in model_routers",
				i,
			)
		}
		if c.ModelList[i].IsAccountRouter() && !c.ModelList[i].IsVirtual() {
			return fmt.Errorf(
				"model_list[%d]: account routers must be configured in account_routers",
				i,
			)
		}
		if err := c.ModelList[i].Validate(); err != nil {
			return fmt.Errorf("model_list[%d]: %w", i, err)
		}
	}
	if err := c.ValidateAccountRouters(); err != nil {
		return err
	}
	if err := c.validateAccountRouterReferences(); err != nil {
		return err
	}
	if err := c.ValidateModelRouters(); err != nil {
		return err
	}
	if err := c.validateModelRouterReferences(); err != nil {
		return err
	}
	if err := c.ValidateModelAliases(); err != nil {
		return err
	}
	return nil
}

func (c *Config) ValidateAccountRouters() error {
	if c == nil {
		return nil
	}
	modelNames := make(map[string]struct{}, len(c.ModelList))
	for _, model := range c.ModelList {
		if model == nil || model.IsAccountRouter() {
			continue
		}
		name := strings.TrimSpace(model.ModelName)
		if name != "" {
			modelNames[name] = struct{}{}
		}
	}

	seen := make(map[string]int, len(c.AccountRouters))
	for i := range c.AccountRouters {
		router := &c.AccountRouters[i]
		name := strings.TrimSpace(router.Name)
		if name == "" {
			return fmt.Errorf("account_routers[%d].name is required", i)
		}
		if err := router.Validate(); err != nil {
			return fmt.Errorf("account_routers[%d]: %w", i, err)
		}
		if previous, ok := seen[name]; ok {
			return fmt.Errorf(
				"account_routers[%d].name %q duplicates account_routers[%d]",
				i,
				name,
				previous,
			)
		}
		seen[name] = i
		if _, ok := modelNames[name]; ok {
			return fmt.Errorf(
				"account_routers[%d].name %q conflicts with model_list model_name",
				i,
				name,
			)
		}
	}
	return nil
}

func (c *Config) validateAccountRouterReferences() error {
	if c == nil {
		return nil
	}
	accounts := make(map[string][]int)
	disabledAccounts := make(map[string][]int)
	routers := make(map[string]int)
	for i, model := range c.ModelList {
		if model == nil || model.IsModelRouter() {
			continue
		}
		name := strings.TrimSpace(model.ModelName)
		if name == "" {
			continue
		}
		if !model.Enabled {
			disabledAccounts[name] = append(disabledAccounts[name], i)
			continue
		}
		accounts[name] = append(accounts[name], i)
	}
	for i := range c.AccountRouters {
		name := strings.TrimSpace(c.AccountRouters[i].Name)
		if name != "" {
			routers[name] = i
		}
	}

	for i := range c.AccountRouters {
		router := &c.AccountRouters[i]
		for _, block := range router.Blocks {
			for _, account := range accountRouterBlockAccounts(block) {
				account = strings.TrimSpace(account)
				if account == "" {
					continue
				}
				if _, ok := AccountRouterCredentialAccountProvider(account); ok {
					continue
				}
				if routerIdx, ok := routers[account]; ok {
					return fmt.Errorf(
						"account_routers[%d] block %q references router %q at account_routers[%d]",
						i,
						block.ID,
						account,
						routerIdx,
					)
				}
				matches := accounts[account]
				if len(matches) == 0 {
					if len(disabledAccounts[account]) > 0 {
						return fmt.Errorf(
							"account_routers[%d] block %q references disabled account %q",
							i,
							block.ID,
							account,
						)
					}
					return fmt.Errorf(
						"account_routers[%d] block %q references unknown account %q",
						i,
						block.ID,
						account,
					)
				}
				if len(matches) > 1 {
					return fmt.Errorf(
						"account_routers[%d] block %q references ambiguous account %q",
						i,
						block.ID,
						account,
					)
				}
			}
		}
	}
	return nil
}

func (c *Config) MaterializeAccountRouterModels() {
	if c == nil {
		return
	}
	models := c.ModelList[:0]
	for _, model := range c.ModelList {
		if model == nil || model.IsVirtual() && model.IsAccountRouter() {
			continue
		}
		models = append(models, model)
	}
	c.ModelList = models
	for i := range c.AccountRouters {
		router := cloneAccountRouterConfig(&c.AccountRouters[i])
		name := strings.TrimSpace(router.Name)
		if name == "" {
			continue
		}
		c.ModelList = append(c.ModelList, &ModelConfig{
			ModelName: name,
			Provider:  AccountRouterProvider,
			Router:    router,
			Enabled:   router.Enabled,
			isVirtual: true,
		})
	}
}

func accountRouterBlockAccounts(block AccountRouterBlock) []string {
	switch strings.TrimSpace(block.Type) {
	case AccountRouterBlockTypeAccount:
		return []string{block.Account}
	case AccountRouterBlockTypeLoadBalance:
		return block.Accounts
	case AccountRouterBlockTypeBranch:
		var accounts []string
		if block.Condition != nil {
			accounts = append(accounts, accountRouterExpressionAccounts(block.Condition.Left)...)
			accounts = append(accounts, accountRouterExpressionAccounts(block.Condition.Right)...)
		}
		return accounts
	default:
		return nil
	}
}

func accountRouterExpressionAccounts(expr AccountRouterExpression) []string {
	var accounts []string
	if account := strings.TrimSpace(expr.Account); account != "" {
		accounts = append(accounts, account)
	}
	if expr.Left != nil {
		accounts = append(accounts, accountRouterExpressionAccounts(*expr.Left)...)
	}
	if expr.Right != nil {
		accounts = append(accounts, accountRouterExpressionAccounts(*expr.Right)...)
	}
	return accounts
}

func accountRouterBlockNextRefs(block AccountRouterBlock) map[string]string {
	refs := map[string]string{}
	if fallback := strings.TrimSpace(block.Fallback); fallback != "" {
		refs["fallback"] = fallback
	}
	if strings.TrimSpace(block.Type) == AccountRouterBlockTypeBranch {
		refs["then"] = strings.TrimSpace(block.Then)
		refs["else"] = strings.TrimSpace(block.Else)
	}
	return refs
}

func cloneAccountRouterConfig(in *AccountRouterConfig) *AccountRouterConfig {
	if in == nil {
		return nil
	}
	out := *in
	out.Blocks = append([]AccountRouterBlock(nil), in.Blocks...)
	for i := range out.Blocks {
		out.Blocks[i].Accounts = append([]string(nil), in.Blocks[i].Accounts...)
	}
	return &out
}

func (c *Config) SecurityCopyFrom(path string) error {
	return c.securityCopyFrom(path, true)
}

// SecurityCopyFromForUpdate overlays security-managed values while deferring
// event-webhook resolution until explicit request values have been reapplied.
func (c *Config) SecurityCopyFromForUpdate(path string) error {
	return c.securityCopyFrom(path, false)
}

func (c *Config) securityCopyFrom(path string, resolveEventWebhooks bool) error {
	if err := loadSecurityConfig(c, securityPath(path)); err != nil {
		return err
	}
	if resolveEventWebhooks {
		if err := c.Events.Ingress.resolveWebhookSecrets(); err != nil {
			return fmt.Errorf("resolve event ingress secrets: %w", err)
		}
	}
	return nil
}

// ResetToDefaults backs up the current config, creates a default config,
// preserves security credentials from the existing config, and saves it.
func ResetToDefaults(configPath string) error {
	unlock, err := lockConfigMutation(configPath)
	if err != nil {
		return err
	}
	defer unlock()
	if err := MakeBackup(configPath); err != nil {
		return fmt.Errorf("backup before reset: %w", err)
	}
	cfg := DefaultConfig()
	cfg.Session.ApplyDmScope()
	cfg.Session.DeriveDmScope()
	if err := cfg.SecurityCopyFrom(configPath); err != nil {
		logger.WarnF("could not preserve security config", map[string]any{"error": err})
	}
	return saveConfigUnlocked(configPath, cfg)
}

func expandMultiKeyModels(models []*ModelConfig) []*ModelConfig {
	var expanded []*ModelConfig

	for _, m := range models {
		keys := m.APIKeys.Values()

		// Single key or no keys: keep as-is
		if len(keys) <= 1 {
			expanded = append(expanded, m)
			continue
		}

		// Multiple keys: expand
		originalName := m.ModelName

		// Create entries for additional keys (key_1, key_2, ...)
		var fallbackNames []string
		for i := 1; i < len(keys); i++ {
			suffix := fmt.Sprintf("__key_%d", i)
			expandedName := originalName + suffix

			// Create a copy for the additional key
			additionalEntry := &ModelConfig{
				ModelName:                   expandedName,
				Provider:                    m.Provider,
				Model:                       m.Model,
				APIBase:                     m.APIBase,
				APIKeys:                     SimpleSecureStrings(keys[i]),
				Proxy:                       m.Proxy,
				AuthMethod:                  m.AuthMethod,
				Router:                      cloneAccountRouterConfig(m.Router),
				ModelRouter:                 cloneModelRouterConfig(m.ModelRouter),
				CredentialID:                m.CredentialID,
				ConnectMode:                 m.ConnectMode,
				Workspace:                   m.Workspace,
				RPM:                         m.RPM,
				MaxTokensField:              m.MaxTokensField,
				RequestTimeout:              m.RequestTimeout,
				ThinkingLevel:               m.ThinkingLevel,
				ReasoningEffort:             m.ReasoningEffort,
				InputPricePerMTok:           m.InputPricePerMTok,
				OutputPricePerMTok:          m.OutputPricePerMTok,
				Subscription:                m.Subscription,
				SubscriptionEquivalentModel: m.SubscriptionEquivalentModel,
				ToolSchemaTransform:         m.ToolSchemaTransform,
				Streaming:                   m.Streaming,
				ExtraBody:                   m.ExtraBody,
				CustomHeaders:               m.CustomHeaders,
				UserAgent:                   m.UserAgent,
				Enabled:                     m.Enabled,
				isVirtual:                   true,
			}
			expanded = append(expanded, additionalEntry)
			fallbackNames = append(fallbackNames, expandedName)
		}

		// Create the primary entry with first key and fallbacks
		primaryEntry := &ModelConfig{
			ModelName:                   originalName,
			Provider:                    m.Provider,
			Model:                       m.Model,
			APIBase:                     m.APIBase,
			Proxy:                       m.Proxy,
			AuthMethod:                  m.AuthMethod,
			Router:                      cloneAccountRouterConfig(m.Router),
			ModelRouter:                 cloneModelRouterConfig(m.ModelRouter),
			CredentialID:                m.CredentialID,
			ConnectMode:                 m.ConnectMode,
			Workspace:                   m.Workspace,
			RPM:                         m.RPM,
			MaxTokensField:              m.MaxTokensField,
			RequestTimeout:              m.RequestTimeout,
			ThinkingLevel:               m.ThinkingLevel,
			ReasoningEffort:             m.ReasoningEffort,
			InputPricePerMTok:           m.InputPricePerMTok,
			OutputPricePerMTok:          m.OutputPricePerMTok,
			Subscription:                m.Subscription,
			SubscriptionEquivalentModel: m.SubscriptionEquivalentModel,
			ToolSchemaTransform:         m.ToolSchemaTransform,
			Streaming:                   m.Streaming,
			ExtraBody:                   m.ExtraBody,
			CustomHeaders:               m.CustomHeaders,
			UserAgent:                   m.UserAgent,
			Enabled:                     m.Enabled,
			APIKeys:                     SimpleSecureStrings(keys[0]),
		}

		// Prepend new fallbacks to existing ones
		if len(fallbackNames) > 0 {
			primaryEntry.Fallbacks = append(fallbackNames, m.Fallbacks...)
		} else if len(m.Fallbacks) > 0 {
			primaryEntry.Fallbacks = m.Fallbacks
		}

		expanded = append(expanded, primaryEntry)
	}

	return expanded
}

func (t *ToolsConfig) IsToolEnabled(name string) bool {
	switch name {
	case "web":
		return t.Web.Enabled
	case "cron":
		return t.Cron.Enabled
	case "exec":
		return t.Exec.Enabled
	case "skills":
		return t.Skills.Enabled
	case "media_cleanup":
		return t.MediaCleanup.Enabled
	case "append_file":
		return t.AppendFile.Enabled
	case "edit_file":
		return t.EditFile.Enabled
	case "find_skills":
		return t.FindSkills.Enabled
	case "i2c":
		return t.I2C.Enabled
	case "install_skill":
		return t.InstallSkill.Enabled
	case "list_dir":
		return t.ListDir.Enabled
	case "load_image":
		return t.LoadImage.Enabled
	case "message":
		return t.Message.Enabled
	case "read_file":
		return t.ReadFile.Enabled
	case "serial":
		return t.Serial.Enabled
	case "spawn":
		return t.Spawn.Enabled
	case "spawn_status":
		return t.SpawnStatus.Enabled
	case "spi":
		return t.SPI.Enabled
	case "subagent":
		return t.Subagent.Enabled
	case "threads":
		return t.Threads.Enabled
	case "web_fetch":
		return t.WebFetch.Enabled
	case "git_workspace":
		return t.GitWorkspace.Enabled
	case "workflow":
		return t.Workflow.Enabled
	case "send_file":
		return t.SendFile.Enabled
	case "send_tts":
		return t.SendTTS.Enabled
	case "write_file":
		return t.WriteFile.Enabled
	case "mcp":
		return t.MCP.Enabled
	default:
		return true
	}
}
