package logger

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rs/zerolog"
)

type loggerStateTestSnapshot struct {
	level       LogLevel
	sinkLevel   LogLevel
	consoleOn   bool
	console     zerolog.ConsoleWriter
	fatalExitFn func()
}

func prepareLoggerStateTest(t *testing.T) {
	t.Helper()
	mu.Lock()
	snapshot := loggerStateTestSnapshot{
		level:       currentLevel,
		sinkLevel:   sinkLevel,
		consoleOn:   consoleOn,
		console:     consoleWriter,
		fatalExitFn: zerolog.FatalExitFunc,
	}
	mu.Unlock()

	DisableFileLogging()
	mu.Lock()
	currentLevel = DEBUG
	sinkLevel = zerolog.TraceLevel
	consoleWriter.Out = io.Discard
	consoleOn = true
	rebuildLoggerLocked()
	mu.Unlock()

	t.Cleanup(func() {
		DisableFileLogging()
		mu.Lock()
		currentLevel = snapshot.level
		sinkLevel = snapshot.sinkLevel
		consoleOn = snapshot.consoleOn
		consoleWriter = snapshot.console
		rebuildLoggerLocked()
		mu.Unlock()
		zerolog.FatalExitFunc = snapshot.fatalExitFn
	})
}

func currentLogFileForTest(t *testing.T) *os.File {
	t.Helper()
	mu.Lock()
	defer mu.Unlock()
	if logFile == nil {
		t.Fatal("file logging is not enabled")
	}
	return logFile
}

func replaceConsoleOutputForTest(writer io.Writer) {
	mu.Lock()
	consoleWriter.Out = writer
	consoleOn = true
	rebuildLoggerLocked()
	mu.Unlock()
}

func TestLoggerStateRepeatedEnableAtomicallyReplacesFile(t *testing.T) {
	prepareLoggerStateTest(t)
	directory := t.TempDir()
	firstPath := filepath.Join(directory, "first.log")
	secondPath := filepath.Join(directory, "second.log")

	if err := EnableFileLogging(firstPath); err != nil {
		t.Fatalf("EnableFileLogging(first) error = %v", err)
	}
	firstFile := currentLogFileForTest(t)
	Info("first-file-marker")

	if err := EnableFileLogging(secondPath); err != nil {
		t.Fatalf("EnableFileLogging(second) error = %v", err)
	}
	if _, err := firstFile.WriteString("after-retirement"); !errors.Is(err, os.ErrClosed) {
		t.Fatalf("retired first file write error = %v, want os.ErrClosed", err)
	}
	Info("second-file-marker")
	DisableFileLogging()

	firstData, err := os.ReadFile(firstPath)
	if err != nil {
		t.Fatalf("ReadFile(first) error = %v", err)
	}
	secondData, err := os.ReadFile(secondPath)
	if err != nil {
		t.Fatalf("ReadFile(second) error = %v", err)
	}
	if !strings.Contains(string(firstData), "first-file-marker") ||
		strings.Contains(string(firstData), "second-file-marker") {
		t.Fatalf("first log contents = %q", firstData)
	}
	if !strings.Contains(string(secondData), "second-file-marker") {
		t.Fatalf("second log contents = %q", secondData)
	}
}

func TestLoggerStateFailedEnableLeavesCurrentFile(t *testing.T) {
	prepareLoggerStateTest(t)
	directory := t.TempDir()
	activePath := filepath.Join(directory, "active.log")
	if err := EnableFileLogging(activePath); err != nil {
		t.Fatalf("EnableFileLogging(active) error = %v", err)
	}
	activeFile := currentLogFileForTest(t)

	blocker := filepath.Join(directory, "not-a-directory")
	if err := os.WriteFile(blocker, []byte("block"), 0o600); err != nil {
		t.Fatalf("WriteFile(blocker) error = %v", err)
	}
	if err := EnableFileLogging(filepath.Join(blocker, "rejected.log")); err == nil {
		t.Fatal("EnableFileLogging(rejected) error = nil")
	}
	if got := currentLogFileForTest(t); got != activeFile {
		t.Fatalf("active file changed after failed enable: got %p want %p", got, activeFile)
	}
	Info("survived-failed-enable")
	DisableFileLogging()

	data, err := os.ReadFile(activePath)
	if err != nil {
		t.Fatalf("ReadFile(active) error = %v", err)
	}
	if !strings.Contains(string(data), "survived-failed-enable") {
		t.Fatalf("active log contents = %q", data)
	}
}

func TestLoggerStateDisableReturnsBeforeFinalLease(t *testing.T) {
	prepareLoggerStateTest(t)
	path := filepath.Join(t.TempDir(), "leased.log")
	if err := EnableFileLogging(path); err != nil {
		t.Fatalf("EnableFileLogging() error = %v", err)
	}
	file := currentLogFileForTest(t)
	lease, ok := acquireEmission(INFO)
	if !ok || lease.file == nil {
		t.Fatal("failed to acquire file emission lease")
	}

	disabled := make(chan struct{})
	go func() {
		DisableFileLogging()
		close(disabled)
	}()
	select {
	case <-disabled:
	case <-time.After(time.Second):
		t.Fatal("DisableFileLogging blocked on active emission lease")
	}
	if _, err := file.WriteString("while-leased\n"); err != nil {
		t.Fatalf("retired file closed before final lease: %v", err)
	}
	lease.release()
	if _, err := file.WriteString("after-release"); !errors.Is(err, os.ErrClosed) {
		t.Fatalf("final-lease file write error = %v, want os.ErrClosed", err)
	}
}

type reentrantDisableWriter struct {
	once sync.Once
	done chan struct{}
}

func (writer *reentrantDisableWriter) Write(value []byte) (int, error) {
	writer.once.Do(func() {
		DisableFileLogging()
		close(writer.done)
	})
	return len(value), nil
}

func TestLoggerStateReentrantDisableDoesNotDeadlock(t *testing.T) {
	prepareLoggerStateTest(t)
	path := filepath.Join(t.TempDir(), "reentrant.log")
	if err := EnableFileLogging(path); err != nil {
		t.Fatalf("EnableFileLogging() error = %v", err)
	}
	file := currentLogFileForTest(t)
	reentrant := &reentrantDisableWriter{done: make(chan struct{})}
	replaceConsoleOutputForTest(reentrant)

	emitted := make(chan struct{})
	go func() {
		Info("reentrant-disable-marker")
		close(emitted)
	}()
	select {
	case <-reentrant.done:
	case <-time.After(time.Second):
		t.Fatal("console writer did not reenter DisableFileLogging")
	}
	select {
	case <-emitted:
	case <-time.After(time.Second):
		t.Fatal("emission deadlocked after reentrant DisableFileLogging")
	}

	if _, err := file.WriteString("after-emission"); !errors.Is(err, os.ErrClosed) {
		t.Fatalf("reentrant retired file write error = %v, want os.ErrClosed", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	if !strings.Contains(string(data), "reentrant-disable-marker") {
		t.Fatalf("retired file missed in-flight record: %q", data)
	}
}

func TestLoggerStateSetConsoleLevelKeepsCompositeSemantics(t *testing.T) {
	prepareLoggerStateTest(t)
	path := filepath.Join(t.TempDir(), "level.log")
	if err := EnableFileLogging(path); err != nil {
		t.Fatalf("EnableFileLogging() error = %v", err)
	}
	SetLevel(DEBUG)
	SetConsoleLevel(ERROR)
	Info("composite-info-hidden")
	Error("composite-error-visible")
	DisableFileLogging()

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	if strings.Contains(string(data), "composite-info-hidden") ||
		!strings.Contains(string(data), "composite-error-visible") {
		t.Fatalf("legacy composite level behavior changed: %q", data)
	}
}

func TestLoggerStateLevelChangeDoesNotRevokeAdmittedEmission(t *testing.T) {
	prepareLoggerStateTest(t)
	path := filepath.Join(t.TempDir(), "admitted.log")
	if err := EnableFileLogging(path); err != nil {
		t.Fatalf("EnableFileLogging() error = %v", err)
	}
	SetLevel(INFO)
	lease, ok := acquireEmission(INFO)
	if !ok || lease.file == nil {
		t.Fatal("failed to admit INFO emission")
	}

	SetLevel(ERROR)
	getEvent(lease.logger, INFO).Msg("admitted-before-level-change")
	lease.release()
	DisableFileLogging()

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	if !strings.Contains(string(data), "admitted-before-level-change") {
		t.Fatalf("admitted record was revoked by SetLevel: %q", data)
	}
}

func TestLoggerStateConvenienceVariantsAndTypedFields(t *testing.T) {
	prepareLoggerStateTest(t)
	path := filepath.Join(t.TempDir(), "variants.log")
	if err := EnableFileLogging(path); err != nil {
		t.Fatalf("EnableFileLogging() error = %v", err)
	}
	activeFile := currentLogFileForTest(t)
	if err := EnableFileLogging(t.TempDir()); err == nil {
		t.Fatal("EnableFileLogging(directory) error = nil")
	}
	if got := currentLogFileForTest(t); got != activeFile {
		t.Fatalf("active file changed after open failure: got %p want %p", got, activeFile)
	}

	DebugF("debug-typed-fields", map[string]any{
		"int64": int64(7), "float64": 1.25, "bool": true,
		"fallback": []string{"item"},
	})
	Warnf("warn-%s", "formatted")
	ErrorC("variant-component", "error-component")

	lease, ok := acquireEmission(INFO)
	if !ok || lease.file == nil {
		t.Fatal("failed to acquire default-level emission lease")
	}
	getEvent(lease.logger, LogLevel(127)).Msg("default-level-event")
	lease.release()
	lease.release()
	var nilLease *emissionLease
	nilLease.release()
	DisableFileLogging()

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	for _, marker := range []string{
		"debug-typed-fields", "warn-formatted", "error-component",
		"variant-component", "default-level-event", `"int64":7`,
		`"float64":1.25`, `"bool":true`, `"fallback":["item"]`,
	} {
		if !strings.Contains(string(data), marker) {
			t.Fatalf("log contents missing %q: %q", marker, data)
		}
	}
}

func TestLoggerStateFatalReentrantDisableReleasesFile(t *testing.T) {
	prepareLoggerStateTest(t)
	path := filepath.Join(t.TempDir(), "fatal.log")
	if err := EnableFileLogging(path); err != nil {
		t.Fatalf("EnableFileLogging() error = %v", err)
	}
	file := currentLogFileForTest(t)
	reentrant := &reentrantDisableWriter{done: make(chan struct{})}
	replaceConsoleOutputForTest(reentrant)
	var exitCalled atomic.Bool
	zerolog.FatalExitFunc = func() {
		exitCalled.Store(true)
	}

	Fatal("fatal-lease-marker")
	if !exitCalled.Load() {
		t.Fatal("zerolog FatalExitFunc was not called")
	}
	if _, err := file.WriteString("after-fatal"); !errors.Is(err, os.ErrClosed) {
		t.Fatalf("fatal retired file write error = %v, want os.ErrClosed", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	if !strings.Contains(string(data), "fatal-lease-marker") {
		t.Fatalf("fatal record missing: %q", data)
	}
}

func TestLoggerStateConcurrentMutationAndEmission(t *testing.T) {
	prepareLoggerStateTest(t)
	directory := t.TempDir()
	const iterations = 80
	var workers sync.WaitGroup
	errorsSeen := make(chan error, iterations)

	for worker := 0; worker < 4; worker++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for index := 0; index < iterations; index++ {
				DebugCF("race", "debug", map[string]any{"worker": worker, "index": index})
				InfoCF("race", "info", map[string]any{"worker": worker, "index": index})
				WarnCF("race", "warn", map[string]any{"worker": worker, "index": index})
				ErrorCF("race", "error", map[string]any{"worker": worker, "index": index})
			}
		}()
	}

	workers.Add(1)
	go func() {
		defer workers.Done()
		levels := []LogLevel{DEBUG, INFO, WARN, ERROR}
		for index := 0; index < iterations; index++ {
			SetLevel(levels[index%len(levels)])
			_ = GetLevel()
			SetConsoleLevel(levels[(index+1)%len(levels)])
			if index%2 == 0 {
				DisableConsole()
			} else {
				EnableConsole()
			}
		}
	}()

	workers.Add(1)
	go func() {
		defer workers.Done()
		for index := 0; index < iterations; index++ {
			path := filepath.Join(directory, fmt.Sprintf("race-%d.log", index%3))
			if err := EnableFileLogging(path); err != nil {
				errorsSeen <- err
				return
			}
			if index%3 == 0 {
				DisableFileLogging()
			}
		}
	}()

	workers.Wait()
	close(errorsSeen)
	for err := range errorsSeen {
		t.Errorf("concurrent file mutation error: %v", err)
	}
	DisableFileLogging()
}
