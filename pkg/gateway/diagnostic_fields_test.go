package gateway

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/logger"
)

type gatewayDiagnosticHostileError struct {
	calls  *atomic.Int32
	secret string
}

func (err *gatewayDiagnosticHostileError) Error() string {
	err.calls.Add(1)
	panic("gateway diagnostic invoked hostile Error: " + err.secret)
}

func TestGatewayDiagnosticFieldsAreDomainSeparatedAndInvokeNoErrorMethods(t *testing.T) {
	const (
		workerCanary    = "worker\n-canary"
		channelCanary   = "channel\r-canary"
		modelCanary     = "model\x1b-canary"
		providerCanary  = "provider\u202e-canary"
		workspaceCanary = "workspace\\canary"
		configCanary    = "/tmp/config\n-canary.json"
		homeCanary      = "/tmp/home\r-canary"
		errorCanary     = "error-secret-canary"
	)
	var calls atomic.Int32
	hostile := &gatewayDiagnosticHostileError{calls: &calls, secret: errorCanary}

	records, raw := captureGatewaySafeRecords(t, func() {
		logger.InfoSafeCF(
			logger.ComponentGateway,
			logger.DiagnosticMessageGatewayStartupFailed,
			logger.NewSafeFields(
				gatewayDiagnosticWorkerField(workerCanary),
				gatewayDiagnosticChannelField(channelCanary),
				gatewayDiagnosticModelField(modelCanary),
				gatewayDiagnosticProviderField(providerCanary),
				gatewayDiagnosticWorkspaceField(workspaceCanary),
				gatewayDiagnosticConfigPathField(configCanary),
				gatewayDiagnosticHomePathField(homeCanary),
				gatewayDiagnosticErrorField(logger.ErrorClassInternal, hostile),
				gatewayDiagnosticLogLevelField("debug"),
			),
		)
	})
	if calls.Load() != 0 {
		t.Fatalf("diagnostic invoked hostile Error() %d times", calls.Load())
	}
	if len(records) != 1 {
		t.Fatalf("record count = %d; raw=%q", len(records), raw)
	}
	for _, canary := range []string{
		workerCanary,
		channelCanary,
		modelCanary,
		providerCanary,
		workspaceCanary,
		configCanary,
		homeCanary,
		errorCanary,
		"gateway diagnostic invoked hostile Error",
	} {
		if strings.Contains(raw, canary) {
			t.Fatalf("raw diagnostic contains canary %q: %s", canary, raw)
		}
	}
	record := records[0]
	for _, prefix := range []string{
		"identity_worker",
		"identity_channel",
		"identity_model",
		"identity_provider",
		"identity_workspace",
		"config_path",
		"home_path",
		"error",
	} {
		if record[prefix+"_state"] != "complete" {
			t.Errorf("%s observation = %#v", prefix, record)
		}
	}
	if record["error_class"] != "internal" || record["log_level"] != "debug" {
		t.Fatalf("error/log-level projection = %#v", record)
	}
	if record["config_path_digest"] == record["home_path_digest"] {
		t.Fatal("config and home path digest domains collided")
	}
}

func TestGatewayDiagnosticLogLevelMappingIsClosed(t *testing.T) {
	tests := [...]struct {
		input string
		want  string
	}{
		{"debug", "debug"},
		{" INFO ", "info"},
		{"warning", "warn"},
		{"ERROR", "error"},
		{"fatal", "fatal"},
		{"unknown\nlevel", "unknown"},
		{"", "unknown"},
	}
	records, raw := captureGatewaySafeRecords(t, func() {
		for _, test := range tests {
			logger.InfoSafeCF(
				logger.ComponentLogger,
				logger.DiagnosticMessageLoggerLogLevelSet,
				logger.NewSafeFields(gatewayDiagnosticLogLevelField(test.input)),
			)
		}
	})
	if len(records) != len(tests) {
		t.Fatalf("record count = %d; want %d; raw=%q", len(records), len(tests), raw)
	}
	for index, test := range tests {
		if got := records[index]["log_level"]; got != test.want {
			t.Errorf("log level %q = %v; want %q", test.input, got, test.want)
		}
		if test.input != "" && strings.Contains(raw, test.input) && test.input != test.want {
			t.Fatalf("raw log contains unnormalized input %q: %s", test.input, raw)
		}
	}
}

func TestGatewayConfiguredVoiceProviderUsesValidatedConfigOnly(t *testing.T) {
	if got := gatewayConfiguredVoiceProvider(nil); got != "" {
		t.Fatalf("nil config provider = %q", got)
	}
	if got := gatewayConfiguredVoiceProvider(&config.Config{}); got != "" {
		t.Fatalf("unconfigured provider = %q", got)
	}

	cfg := &config.Config{
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
	if err := cfg.ValidateModelSelections(); err != nil {
		t.Fatalf("ValidateModelSelections() error = %v", err)
	}
	if got := gatewayConfiguredVoiceProvider(cfg); got != "openai" {
		t.Fatalf("validated provider = %q; want openai", got)
	}

	invalid := *cfg
	invalid.Voice.AccountRef = "missing-account"
	if got := gatewayConfiguredVoiceProvider(&invalid); got != "" {
		t.Fatalf("invalid config provider = %q", got)
	}

	provider := gatewayConfiguredVoiceProvider(cfg)
	records, raw := captureGatewaySafeRecords(t, func() {
		logger.InfoSafeCF(
			logger.ComponentVoice,
			logger.DiagnosticMessageVoiceTranscriptionEnabledAgentLevel,
			logger.NewSafeFields(gatewayDiagnosticProviderField(provider)),
		)
	})
	if len(records) != 1 || records[0]["identity_provider_state"] != "complete" {
		t.Fatalf("provider observation = %#v; raw=%q", records, raw)
	}
	if strings.Contains(raw, provider) {
		t.Fatalf("provider identity leaked raw: %s", raw)
	}
}

func captureGatewaySafeRecords(t *testing.T, emit func()) ([]map[string]any, string) {
	t.Helper()
	initialLevel := logger.GetLevel()
	logger.SetLevel(logger.DEBUG)
	logger.DisableConsole()
	t.Cleanup(func() {
		logger.DisableFileLogging()
		logger.EnableConsole()
		logger.SetLevel(initialLevel)
	})

	path := filepath.Join(t.TempDir(), "gateway-safe.log")
	if err := logger.EnableFileLogging(path); err != nil {
		t.Fatalf("EnableFileLogging() error = %v", err)
	}
	emit()
	logger.DisableFileLogging()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
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
