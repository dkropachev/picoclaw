package logger_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sipeed/picoclaw/pkg/logger"
)

func TestSafeEmitterReportsExternalCaller(t *testing.T) {
	initialLevel := logger.GetLevel()
	logger.SetLevel(logger.DEBUG)
	logger.DisableConsole()
	t.Cleanup(func() {
		logger.DisableFileLogging()
		logger.EnableConsole()
		logger.SetLevel(initialLevel)
	})

	path := filepath.Join(t.TempDir(), "caller.log")
	if err := logger.EnableFileLogging(path); err != nil {
		t.Fatalf("EnableFileLogging() error = %v", err)
	}
	logger.InfoSafeCF(
		logger.ComponentLogger,
		logger.DiagnosticMessageEvent,
		logger.NewSafeFields(),
	)
	logger.DisableFileLogging()

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	var record map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSpace(string(data))), &record); err != nil {
		t.Fatalf("Unmarshal() error = %v; data=%q", err, data)
	}
	caller, _ := record["caller"].(string)
	if !strings.Contains(caller, "safe_fields_external_test.go:") {
		t.Fatalf("caller = %q; want external test file", caller)
	}
}
