package logger

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rs/zerolog"
)

func TestThirdPartyLoggerCompatibilitySurface(t *testing.T) {
	prepareLoggerStateTest(t)
	path := filepath.Join(t.TempDir(), "third-party.log")
	if err := EnableFileLogging(path); err != nil {
		t.Fatalf("EnableFileLogging() error = %v", err)
	}
	var fatalExitCalls int
	zerolog.FatalExitFunc = func() { fatalExitCalls++ }

	compat := NewLogger("third-party")
	compat.Debug("debug", 1)
	compat.Info("info", 2)
	compat.Warn("warn", 3)
	compat.Error("error", 4)
	compat.Debugf("debugf-%d", 5)
	compat.Infof("infof-%d", 6)
	compat.Warnf("warnf-%d", 7)
	compat.Warningf("warningf-%d", 8)
	compat.Errorf("errorf-%d", 9)
	compat.Fatalf("fatalf-%d", 10)
	if fatalExitCalls != 1 {
		t.Fatalf("fatal exit calls = %d, want 1", fatalExitCalls)
	}
	if err := compat.Sync(); err != nil {
		t.Fatalf("Sync() error = %v", err)
	}
	if got := compat.WithLevels(map[int]LogLevel{99: WARN}); got != compat {
		t.Fatal("WithLevels() did not return receiver")
	}
	compat.Log(99, 0, "mapped-%s", "warn")
	compat.Log(int(INFO), 0, "unmapped-%s", "info")
	NewLogger("third-party-nil-levels").Log(
		int(INFO),
		0,
		"token=%s",
		"bot1234:abcd123456789012WXYZ",
	)
	DisableFileLogging()

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	output := string(data)
	for _, marker := range []string{
		"debug1", "info2", "warn3", "error4", "debugf-5", "infof-6",
		"warnf-7", "warningf-8", "errorf-9", "fatalf-10",
		"mapped-warn", "unmapped-info", "bot1234:abcd****WXYZ",
	} {
		if !strings.Contains(output, marker) {
			t.Fatalf("compatibility log missing %q: %s", marker, output)
		}
	}
	if strings.Contains(output, "123456789012") {
		t.Fatalf("bot token was not masked: %s", output)
	}
}
