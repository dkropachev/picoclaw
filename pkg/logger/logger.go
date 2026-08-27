package logger

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"

	"github.com/rs/zerolog"
	"golang.org/x/term"
)

type LogLevel = zerolog.Level

const (
	DEBUG = zerolog.DebugLevel
	INFO  = zerolog.InfoLevel
	WARN  = zerolog.WarnLevel
	ERROR = zerolog.ErrorLevel
	FATAL = zerolog.FatalLevel

	Component = "component"
)

var (
	logLevelNames = map[LogLevel]string{
		DEBUG: "DEBUG",
		INFO:  "INFO",
		WARN:  "WARN",
		ERROR: "ERROR",
		FATAL: "FATAL",
	}

	currentLevel = INFO
	logger       zerolog.Logger
	// logFile mirrors managedFile.file for legacy package-local tests. Access is
	// guarded by mu; emissions retain managedFile rather than this pointer.
	logFile       *os.File
	managedFile   *managedLogFile
	once          sync.Once
	mu            sync.Mutex
	consoleWriter zerolog.ConsoleWriter
	consoleOn     = true
	sinkLevel     = zerolog.TraceLevel
)

// managedLogFile separates publication from lifetime. A logger snapshot may
// keep writing a retired file until its last emission lease is released.
// Every field is guarded by mu.
type managedLogFile struct {
	file    *os.File
	active  uint64
	retired bool
	closed  bool
}

// emissionLease owns the exact logger/file snapshot captured for one record.
// The logger itself is immutable; only the optional file needs a lifetime
// reference because console/discard writers are process-lived values.
type emissionLease struct {
	logger   zerolog.Logger
	file     *managedLogFile
	released bool
}

func init() {
	once.Do(func() {
		// Package admission is decided under mu in acquireEmission. Keep
		// zerolog's process-global gate permissive so a later SetLevel cannot
		// revoke an already admitted immutable logger snapshot.
		zerolog.SetGlobalLevel(zerolog.TraceLevel)

		isTTY := term.IsTerminal(int(os.Stdout.Fd()))

		consoleWriter = zerolog.ConsoleWriter{
			Out:        os.Stdout,
			TimeFormat: "15:04:05", // TODO: make it configurable???

			// Custom formatter to handle multiline strings and JSON objects
			FormatFieldValue: formatFieldValue,
			PartsOrder: []string{
				zerolog.TimestampFieldName,
				zerolog.LevelFieldName,
				Component,
				zerolog.CallerFieldName,
				zerolog.MessageFieldName,
			},
			FieldsExclude: []string{Component},
			FormatPrepare: func(fields map[string]any) error {
				if isTTY {
					fields[Component] = fmt.Sprintf("\x1b[33m%v\x1b[0m", fields[Component])
				}
				return nil
			},
			NoColor: !isTTY,
		}

		rebuildLoggerLocked()
	})
}

// rebuildLoggerLocked publishes a fresh immutable zerolog value. Always use
// io.MultiWriter, including for one output: zerolog Fatal closes a top-level
// io.Closer, which must never bypass managed-file lease retirement.
func rebuildLoggerLocked() {
	writers := make([]io.Writer, 0, 2)
	if consoleOn {
		writers = append(writers, consoleWriter)
	} else {
		writers = append(writers, io.Discard)
	}
	if managedFile != nil {
		writers = append(writers, managedFile.file)
	}
	logger = zerolog.New(io.MultiWriter(writers...)).
		With().Timestamp().Caller().Logger().Level(sinkLevel)
}

func acquireEmission(level LogLevel) (*emissionLease, bool) {
	mu.Lock()
	defer mu.Unlock()
	if level < currentLevel {
		return nil, false
	}
	lease := &emissionLease{logger: logger, file: managedFile}
	if lease.file != nil {
		lease.file.active++
	}
	return lease, true
}

func (lease *emissionLease) release() {
	if lease == nil || lease.released {
		return
	}
	lease.released = true
	if lease.file == nil {
		return
	}

	var closeFile *os.File
	mu.Lock()
	if lease.file.active > 0 {
		lease.file.active--
	}
	if lease.file.retired && lease.file.active == 0 && !lease.file.closed {
		lease.file.closed = true
		closeFile = lease.file.file
	}
	mu.Unlock()
	if closeFile != nil {
		_ = closeFile.Close()
	}
}

// retireManagedFileLocked prevents future acquisition and returns a file that
// can be closed immediately. An active file is closed by its final lease.
func retireManagedFileLocked(file *managedLogFile) *os.File {
	if file == nil || file.retired {
		return nil
	}
	file.retired = true
	if file.active != 0 || file.closed {
		return nil
	}
	file.closed = true
	return file.file
}

func formatFieldValue(i any) string {
	var s string

	switch val := i.(type) {
	case string:
		s = val
	case []byte:
		s = string(val)
	default:
		return fmt.Sprintf("%v", i)
	}

	if unquoted, err := strconv.Unquote(s); err == nil {
		s = unquoted
	}

	if strings.Contains(s, "\n") {
		return fmt.Sprintf("\n%s", s)
	}

	if strings.Contains(s, " ") {
		if (strings.HasPrefix(s, "{") && strings.HasSuffix(s, "}")) ||
			(strings.HasPrefix(s, "[") && strings.HasSuffix(s, "]")) {
			return s
		}
		return fmt.Sprintf("%q", s)
	}

	return s
}

func SetLevel(level LogLevel) {
	mu.Lock()
	currentLevel = level
	mu.Unlock()
}

func SetConsoleLevel(level LogLevel) {
	mu.Lock()
	sinkLevel = level
	rebuildLoggerLocked()
	mu.Unlock()
}

func DisableConsole() {
	mu.Lock()
	consoleOn = false
	rebuildLoggerLocked()
	mu.Unlock()
}

func EnableConsole() {
	mu.Lock()
	consoleOn = true
	rebuildLoggerLocked()
	mu.Unlock()
}

func GetLevel() LogLevel {
	mu.Lock()
	level := currentLevel
	mu.Unlock()
	return level
}

// ParseLevel converts a case-insensitive level name to a LogLevel.
// Returns the level and true if valid, or (INFO, false) if unrecognized.
func ParseLevel(s string) (LogLevel, bool) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "debug":
		return DEBUG, true
	case "info":
		return INFO, true
	case "warn", "warning":
		return WARN, true
	case "error":
		return ERROR, true
	case "fatal":
		return FATAL, true
	default:
		return INFO, false
	}
}

// SetLevelFromString sets the log level from a string value.
// If the string is empty or not a recognized level name, the current level is kept.
func SetLevelFromString(s string) {
	if s == "" {
		return
	}
	if level, ok := ParseLevel(s); ok {
		SetLevel(level)
	}
}

func EnableFileLogging(filePath string) error {
	if err := os.MkdirAll(filepath.Dir(filePath), 0o700); err != nil {
		return fmt.Errorf("failed to create log directory: %w", err)
	}

	newFile, err := os.OpenFile(filePath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return fmt.Errorf("failed to open log file: %w", err)
	}
	candidate := &managedLogFile{file: newFile}

	mu.Lock()
	oldFile := managedFile
	managedFile = candidate
	logFile = newFile
	rebuildLoggerLocked()
	closeFile := retireManagedFileLocked(oldFile)
	mu.Unlock()
	if closeFile != nil {
		_ = closeFile.Close()
	}

	return nil
}

func DisableFileLogging() {
	mu.Lock()
	oldFile := managedFile
	managedFile = nil
	logFile = nil
	rebuildLoggerLocked()
	closeFile := retireManagedFileLocked(oldFile)
	mu.Unlock()
	if closeFile != nil {
		_ = closeFile.Close()
	}
}

func ConfigureFromEnv() {
	if logFile := os.Getenv("PICOCLAW_LOG_FILE"); logFile != "" {
		if strings.HasPrefix(logFile, "~/") {
			if home := os.Getenv("HOME"); home != "" {
				logFile = filepath.Join(home, logFile[2:])
			}
		}

		if err := EnableFileLogging(logFile); err != nil {
			fmt.Fprintf(os.Stderr, "failed to enable file logging: %v\n", err)
		} else {
			DisableConsole()
		}
	}
}

const (
	locUnknown = "<unknown>"
)

func getPackageNameFromFile(filePath string) string {
	dir := filepath.Dir(filePath)
	importPath := filepath.ToSlash(dir)

	parts := strings.Split(importPath, "/")
	if len(parts) == 0 {
		return locUnknown
	}

	pkg := parts[len(parts)-1]
	if pkg == "." {
		return "<main>"
	}

	return pkg
}

func getCallerSkip() (int, string) {
	for i := 2; i < 15; i++ {
		pc, file, _, ok := runtime.Caller(i)
		if !ok {
			continue
		}

		fn := runtime.FuncForPC(pc)
		if fn == nil {
			continue
		}

		// bypass common loggers
		if strings.HasSuffix(file, "/logger.go") ||
			strings.HasSuffix(file, "/logger_3rd_party.go") ||
			strings.HasSuffix(file, "/log.go") {
			continue
		}

		funcName := fn.Name()
		if strings.HasPrefix(funcName, "runtime.") {
			continue
		}

		return i - 1, getPackageNameFromFile(file)
	}

	return 3, locUnknown
}

//nolint:zerologlint
func getEvent(logger zerolog.Logger, level LogLevel) *zerolog.Event {
	switch level {
	case zerolog.DebugLevel:
		return logger.Debug()
	case zerolog.InfoLevel:
		return logger.Info()
	case zerolog.WarnLevel:
		return logger.Warn()
	case zerolog.ErrorLevel:
		return logger.Error()
	case zerolog.FatalLevel:
		return logger.Fatal()
	default:
		return logger.Info()
	}
}

func logMessage(level LogLevel, component string, message string, fields map[string]any) {
	lease, ok := acquireEmission(level)
	if !ok {
		return
	}
	// Install the release before constructing a Fatal event: zerolog may invoke
	// FatalExitFunc while creating an event disabled by its local/global level.
	defer lease.release()

	skip, pkg := getCallerSkip()

	event := getEvent(lease.logger, level)

	if component == "" {
		component = pkg
	}

	event.Str(Component, component)

	appendFields(event, fields)

	event.CallerSkipFrame(skip).Msg(message)
}

func appendFields(event *zerolog.Event, fields map[string]any) {
	for k, v := range fields {
		// Type switch to avoid double JSON serialization of strings
		switch val := v.(type) {
		case error:
			event.Str(k, val.Error())
		case string:
			event.Str(k, val)
		case int:
			event.Int(k, val)
		case int64:
			event.Int64(k, val)
		case float64:
			event.Float64(k, val)
		case bool:
			event.Bool(k, val)
		default:
			event.Interface(k, v) // Fallback for struct, slice and maps
		}
	}
}

func Debug(message string) {
	logMessage(DEBUG, "", message, nil)
}

func DebugC(component string, message string) {
	logMessage(DEBUG, component, message, nil)
}

func Debugf(message string, ss ...any) {
	logMessage(DEBUG, "", fmt.Sprintf(message, ss...), nil)
}

func DebugF(message string, fields map[string]any) {
	logMessage(DEBUG, "", message, fields)
}

func DebugCF(component string, message string, fields map[string]any) {
	logMessage(DEBUG, component, message, fields)
}

func Info(message string) {
	logMessage(INFO, "", message, nil)
}

func InfoC(component string, message string) {
	logMessage(INFO, component, message, nil)
}

func InfoF(message string, fields map[string]any) {
	logMessage(INFO, "", message, fields)
}

func Infof(message string, ss ...any) {
	logMessage(INFO, "", fmt.Sprintf(message, ss...), nil)
}

func InfoCF(component string, message string, fields map[string]any) {
	logMessage(INFO, component, message, fields)
}

func Warn(message string) {
	logMessage(WARN, "", message, nil)
}

func WarnC(component string, message string) {
	logMessage(WARN, component, message, nil)
}

func WarnF(message string, fields map[string]any) {
	logMessage(WARN, "", message, fields)
}

func WarnCF(component string, message string, fields map[string]any) {
	logMessage(WARN, component, message, fields)
}

func Warnf(message string, ss ...any) {
	logMessage(WARN, "", fmt.Sprintf(message, ss...), nil)
}

func Error(message string) {
	logMessage(ERROR, "", message, nil)
}

func ErrorC(component string, message string) {
	logMessage(ERROR, component, message, nil)
}

func Errorf(message string, ss ...any) {
	logMessage(ERROR, "", fmt.Sprintf(message, ss...), nil)
}

func ErrorF(message string, fields map[string]any) {
	logMessage(ERROR, "", message, fields)
}

func ErrorCF(component string, message string, fields map[string]any) {
	logMessage(ERROR, component, message, fields)
}

func Fatal(message string) {
	logMessage(FATAL, "", message, nil)
}

func FatalC(component string, message string) {
	logMessage(FATAL, component, message, nil)
}

func Fatalf(message string, ss ...any) {
	logMessage(FATAL, "", fmt.Sprintf(message, ss...), nil)
}

func FatalF(message string, fields map[string]any) {
	logMessage(FATAL, "", message, fields)
}

func FatalCF(component string, message string, fields map[string]any) {
	logMessage(FATAL, component, message, fields)
}
