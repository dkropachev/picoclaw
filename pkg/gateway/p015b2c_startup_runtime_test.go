package gateway

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/isolation"
	"github.com/sipeed/picoclaw/pkg/logger"
)

type p015B2CStartupHostileScalar struct {
	calls *atomic.Int32
}

func (value p015B2CStartupHostileScalar) String() string {
	value.calls.Add(1)
	panic("startup diagnostics invoked hostile String")
}

func TestP015B2CStartupRunFailureProjectsPathsErrorAndPolicy(t *testing.T) {
	initialLevel := logger.GetLevel()
	logger.DisableConsole()
	t.Cleanup(func() {
		logger.DisableFileLogging()
		logger.EnableConsole()
		logger.SetLevel(initialLevel)
	})

	const (
		homeCanary   = "home-raw-canary"
		configCanary = "config-raw-canary"
	)
	homePath := filepath.Join(t.TempDir(), homeCanary)
	configPath := filepath.Join(homePath, configCanary+"\n.json")
	if err := os.MkdirAll(homePath, 0o700); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	if err := os.WriteFile(configPath, []byte(`{"gateway":`), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	err := Run(true, homePath, configPath, true)
	if err == nil || !strings.Contains(err.Error(), "error loading config") {
		t.Fatalf("Run() error = %v; want config load failure", err)
	}

	gatewayLogPath := filepath.Join(homePath, logPath, logFile)
	data, readErr := os.ReadFile(gatewayLogPath)
	if readErr != nil {
		t.Fatalf("ReadFile(%s) error = %v", gatewayLogPath, readErr)
	}
	records, raw := p015B2CDecodeSafeRecords(t, data)
	var startup map[string]any
	for _, record := range records {
		if record["message"] == "Gateway startup failed" {
			startup = record
			break
		}
	}
	if startup == nil {
		t.Fatalf("startup failure record missing: %s", raw)
	}
	if startup["component"] != "gateway" || startup["error_class"] != "internal" ||
		startup["config_path_state"] != "complete" ||
		startup["home_path_state"] != "complete" ||
		startup["allow_empty"] != true || startup["debug_enabled"] != true {
		t.Fatalf("startup failure projection = %#v", startup)
	}
	if startup["config_path_digest"] == startup["home_path_digest"] {
		t.Fatal("startup config/home path domains collided")
	}
	startupJSON, marshalErr := json.Marshal(startup)
	if marshalErr != nil {
		t.Fatalf("Marshal(startup record) error = %v", marshalErr)
	}
	for _, canary := range []string{homeCanary, configCanary, configPath, homePath} {
		if strings.Contains(string(startupJSON), canary) {
			t.Fatalf("startup record leaked raw path %q: %s", canary, startupJSON)
		}
	}
}

func TestP015B2CStartupCountLogLevelAndVoiceProviderRecords(t *testing.T) {
	var hostileCalls atomic.Int32
	startupInfo := map[string]any{
		"tools": map[string]any{"count": 11},
		"skills": map[string]any{
			"available": 4,
			"total":     7,
		},
		"ignored": p015B2CStartupHostileScalar{calls: &hostileCalls},
	}
	status := collectGatewayStartupStatus(startupInfo)
	if hostileCalls.Load() != 0 {
		t.Fatalf("startup status invoked hostile String() %d times", hostileCalls.Load())
	}
	if status.toolsCount != 11 || status.skillsAvailable != 4 || status.skillsTotal != 7 {
		t.Fatalf("startup status changed functional counts: %#v", status)
	}

	cfg := p015B2CStartupVoiceConfig()
	if err := cfg.ValidateModelSelections(); err != nil {
		t.Fatalf("ValidateModelSelections() error = %v", err)
	}
	provider := gatewayConfiguredVoiceProvider(cfg)
	if provider != "openai" {
		t.Fatalf("configured voice provider = %q; want openai", provider)
	}
	effectiveLogLevel := config.EffectiveGatewayLogLevel(&config.Config{
		Gateway: config.GatewayConfig{LogLevel: "warning"},
	})
	if effectiveLogLevel != "warn" {
		t.Fatalf("effective log level = %q; want warn", effectiveLogLevel)
	}

	records, raw := captureGatewaySafeRecords(t, func() {
		logger.InfoSafeCF(
			logger.ComponentAgent,
			logger.DiagnosticMessageAgentInitialized,
			logger.NewSafeFields(
				logger.SafeInt(logger.FieldToolCount, status.toolsCount),
				logger.SafeInt(logger.FieldAvailableCount, status.skillsAvailable),
				logger.SafeInt(logger.FieldSkillCount, status.skillsTotal),
			),
		)
		logger.InfoSafeCF(
			logger.ComponentLogger,
			logger.DiagnosticMessageLoggerLogLevelSet,
			logger.NewSafeFields(gatewayDiagnosticLogLevelField(effectiveLogLevel)),
		)
		logger.InfoSafeCF(
			logger.ComponentVoice,
			logger.DiagnosticMessageVoiceTranscriptionEnabledAgentLevel,
			logger.NewSafeFields(gatewayDiagnosticProviderField(provider)),
		)
	})
	if len(records) != 3 {
		t.Fatalf("record count = %d; raw=%s", len(records), raw)
	}
	if records[0]["tool_count"] != float64(11) ||
		records[0]["available_count"] != float64(4) ||
		records[0]["skill_count"] != float64(7) {
		t.Fatalf("startup count record = %#v", records[0])
	}
	if records[1]["log_level"] != "warn" {
		t.Fatalf("startup log-level record = %#v", records[1])
	}
	if records[2]["identity_provider_state"] != "complete" ||
		records[2]["identity_provider_count"] != float64(1) {
		t.Fatalf("startup voice provider record = %#v", records[2])
	}
	if strings.Contains(raw, provider) || strings.Contains(raw, "warning") {
		t.Fatalf("startup records contain raw provider/log-level input: %s", raw)
	}
}

func TestP015B2CStartupLimitedModePreservesProviderBehavior(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Agents.Defaults.ModelName = ""
	policy := isolation.NewExecutionPolicy(cfg.Isolation)
	if err := policy.Validate(); err != nil {
		t.Fatalf("execution policy validation error = %v", err)
	}

	records, raw := captureGatewaySafeRecords(t, func() {
		provider, modelID, err := createStartupProvider(cfg, true, policy)
		if err != nil {
			t.Fatalf("createStartupProvider() error = %v", err)
		}
		if provider == nil || modelID != "" {
			t.Fatalf("limited provider/model = %#v/%q", provider, modelID)
		}
		response, chatErr := provider.Chat(context.Background(), nil, nil, "", nil)
		if response != nil || chatErr == nil ||
			chatErr.Error() != config.ErrNoModelConfigured.Error() {
			t.Fatalf("limited Chat() = %#v, %v", response, chatErr)
		}
	})
	if len(records) != 1 {
		t.Fatalf("limited-mode record count = %d; raw=%s", len(records), raw)
	}
	if records[0]["component"] != "gateway" ||
		records[0]["message"] != "Gateway started without a configured model alias" ||
		records[0]["limited_mode"] != true {
		t.Fatalf("limited-mode record = %#v", records[0])
	}
	if strings.Contains(raw, config.ErrNoModelConfigured.Error()) {
		t.Fatalf("limited-mode functional error leaked into structured log: %s", raw)
	}
}

func p015B2CStartupVoiceConfig() *config.Config {
	return &config.Config{
		ModelList: []*config.ModelConfig{{
			ModelName: "voice-account",
			Provider:  "OpenAI",
			Enabled:   true,
		}},
		ModelAliases: []config.ModelAliasConfig{{Name: "asr", Model: "whisper-1"}},
		Voice: config.VoiceConfig{
			AccountRef: "voice-account",
			ModelName:  "asr",
		},
	}
}

func p015B2CDecodeSafeRecords(t *testing.T, data []byte) ([]map[string]any, string) {
	t.Helper()
	raw := strings.TrimSpace(string(data))
	if raw == "" {
		return nil, raw
	}
	lines := strings.Split(raw, "\n")
	records := make([]map[string]any, 0, len(lines))
	for _, line := range lines {
		var record map[string]any
		if err := json.Unmarshal([]byte(line), &record); err != nil {
			t.Fatalf("Unmarshal() error = %v; line=%q", err, line)
		}
		records = append(records, record)
	}
	return records, raw
}
