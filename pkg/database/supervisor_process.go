package database

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

const (
	supervisorBootstrapEnvironment = "PICOCLAW_DATABASE_SUPERVISOR_BOOTSTRAP"
	defaultSupervisorStartTimeout  = 8 * time.Second
)

// EnsureOptions describe how a launcher or CLI starts the private broker when
// no healthy supervisor owns the canonical home.
type EnsureOptions struct {
	Home       string
	Executable string
	ConfigPath string
	Timeout    time.Duration
}

// EnsureSupervisor attaches to the canonical-home broker or starts the hidden
// supervisor process and waits for authenticated readiness. Concurrent callers
// may race to start; the broker singleton admits exactly one winner.
func EnsureSupervisor(ctx context.Context, options EnsureOptions) (*Client, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	home, err := PrepareHome(options.Home)
	if err != nil {
		return nil, err
	}
	_, expectedFingerprint, err := LoadCatalogConfiguration(options.ConfigPath)
	if err != nil {
		return nil, err
	}
	timeout := options.Timeout
	if timeout <= 0 {
		timeout = defaultSupervisorStartTimeout
	}
	readyCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	lastStart := time.Time{}
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	var lastErr error
	for {
		client, status, connectErr := connectReadyBrokerStatus(readyCtx, home)
		switch {
		case connectErr == nil && status.CatalogFingerprint == expectedFingerprint:
			return client, nil
		case connectErr == nil:
			lastErr = NewError(CodeConflict, "database broker catalog configuration changed")
			if shutdownErr := shutdownBrokerForReplacement(readyCtx, client); shutdownErr != nil {
				lastErr = shutdownErr
			} else {
				lastStart = time.Time{}
			}
		default:
			lastErr = connectErr
		}

		if lastStart.IsZero() || time.Since(lastStart) >= 500*time.Millisecond {
			if startErr := startSupervisorProcess(options, home); startErr != nil {
				lastErr = startErr
			} else {
				lastStart = time.Now()
			}
		}
		select {
		case <-readyCtx.Done():
			if errors.Is(readyCtx.Err(), context.Canceled) {
				return nil, NewError(CodeDeadline, "database supervisor startup was canceled")
			}
			_ = lastErr
			return nil, NewError(CodeUnavailable, "database supervisor did not become ready")
		case <-ticker.C:
		}
	}
}

func startSupervisorProcess(options EnsureOptions, home string) error {
	executable := strings.TrimSpace(options.Executable)
	if executable == "" {
		resolved, err := os.Executable()
		if err != nil {
			return NewError(CodeUnavailable, "database supervisor executable is unavailable")
		}
		executable = resolved
	}
	if !filepath.IsAbs(executable) && !strings.ContainsRune(executable, os.PathSeparator) {
		resolved, err := exec.LookPath(executable)
		if err != nil {
			return NewError(CodeUnavailable, "database supervisor executable is unavailable")
		}
		executable = resolved
	}
	executable, err := filepath.Abs(executable)
	if err != nil {
		return NewError(CodeInvalid, "database supervisor executable is invalid")
	}
	info, err := os.Stat(executable)
	if err != nil || !info.Mode().IsRegular() {
		return NewError(CodeUnavailable, "database supervisor executable is unavailable")
	}
	bootstrap, err := randomHex(tokenBytes)
	if err != nil {
		return NewError(CodeInternal, "database supervisor bootstrap failed")
	}
	bootstrapPath, err := prepareSupervisorBootstrap(home, bootstrap)
	if err != nil {
		return err
	}
	cleanupBootstrap := true
	defer func() {
		if cleanupBootstrap {
			_ = os.Remove(bootstrapPath)
		}
	}()
	command := exec.Command(executable, "database", "__serve", "--home", home)
	command.Env = append(os.Environ(), supervisorBootstrapEnvironment+"="+bootstrap)
	if configPath := strings.TrimSpace(options.ConfigPath); configPath != "" {
		command.Env = replaceSupervisorEnvironment(command.Env, "PICOCLAW_CONFIG", configPath)
	}
	if err := configureSupervisorProcess(command, home); err != nil {
		return err
	}
	logFile, _ := command.Stdout.(*os.File)
	if err := command.Start(); err != nil {
		if logFile != nil {
			_ = logFile.Close()
		}
		return fmt.Errorf("start database supervisor: %w", err)
	}
	if logFile != nil {
		_ = logFile.Close()
	}
	if command.Process != nil {
		_ = command.Process.Release()
	}
	// The child consumes the one-time file before loading configuration. Do not
	// remove it in this parent immediately after Start, which would race the
	// child. Bound debris from a successfully started invalid executable.
	cleanupBootstrap = false
	go func() {
		timer := time.NewTimer(30 * time.Second)
		defer timer.Stop()
		<-timer.C
		_ = os.Remove(bootstrapPath)
	}()
	return nil
}

func replaceSupervisorEnvironment(environment []string, name, value string) []string {
	prefix := name + "="
	result := make([]string, 0, len(environment)+1)
	for _, entry := range environment {
		if !strings.HasPrefix(entry, prefix) {
			result = append(result, entry)
		}
	}
	return append(result, prefix+value)
}

func connectReadyBroker(ctx context.Context, home string) (*Client, error) {
	client, err := Connect(home)
	if err != nil {
		return nil, err
	}
	pingCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	if _, err := client.Ping(pingCtx); err != nil {
		return nil, err
	}
	return client, nil
}

func connectReadyBrokerStatus(ctx context.Context, home string) (*Client, BrokerStatus, error) {
	client, err := connectReadyBroker(ctx, home)
	if err != nil {
		return nil, BrokerStatus{}, err
	}
	statusCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	status, err := client.Status(statusCtx)
	if err != nil {
		return nil, BrokerStatus{}, err
	}
	return client, status, nil
}

func shutdownBrokerForReplacement(ctx context.Context, client *Client) error {
	if client == nil {
		return NewError(CodeUnavailable, "database broker is unavailable")
	}
	epoch := client.Epoch()
	shutdownCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	err := client.Shutdown(shutdownCtx)
	cancel()
	if err != nil {
		switch CodeOf(err) {
		case CodeOutcomeUnknown, CodeUnavailable, CodeConflict:
		default:
			return err
		}
	}
	ticker := time.NewTicker(25 * time.Millisecond)
	defer ticker.Stop()
	for {
		candidate, connectErr := Connect(client.home)
		if connectErr != nil || candidate.Epoch() != epoch {
			return nil
		}
		pingCtx, pingCancel := context.WithTimeout(ctx, 250*time.Millisecond)
		_, pingErr := candidate.Ping(pingCtx)
		pingCancel()
		if pingErr != nil {
			return nil
		}
		select {
		case <-ctx.Done():
			return NewError(CodeDeadline, "database supervisor replacement was canceled")
		case <-ticker.C:
		}
	}
}

// ConsumeSupervisorBootstrap proves that the hidden serve command was created
// by EnsureSupervisor. The authority is consumed before any config or store is
// loaded and is not inherited by later children.
func ConsumeSupervisorBootstrap(home string) bool {
	value := os.Getenv(supervisorBootstrapEnvironment)
	_ = os.Unsetenv(supervisorBootstrapEnvironment)
	if !validLowerHex(value, tokenBytes*2) {
		return false
	}
	stateDir, err := StateDirectory(home)
	if err != nil {
		return false
	}
	path := filepath.Join(stateDir, ".bootstrap-"+value)
	info, err := os.Lstat(path)
	if err != nil || info.Mode()&os.ModeSymlink != 0 || validateOwnerOnlyFile(path, info, 0o600) != nil {
		return false
	}
	if err := os.Remove(path); err != nil {
		return false
	}
	if syncDirectory(stateDir) != nil {
		return false
	}
	markBrokerAuthorityHeld()
	return true
}

func prepareSupervisorBootstrap(home, token string) (string, error) {
	stateDir, err := prepareStateDirectory(home)
	if err != nil {
		return "", err
	}
	path := filepath.Join(stateDir, ".bootstrap-"+token)
	file, err := createOwnerOnlyExclusiveFile(path, 0o600)
	if err != nil {
		return "", fmt.Errorf("create database supervisor bootstrap: %w", err)
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		_ = os.Remove(path)
		return "", fmt.Errorf("sync database supervisor bootstrap: %w", err)
	}
	if err := file.Close(); err != nil {
		_ = os.Remove(path)
		return "", fmt.Errorf("close database supervisor bootstrap: %w", err)
	}
	if err := syncDirectory(stateDir); err != nil {
		_ = os.Remove(path)
		return "", fmt.Errorf("sync database supervisor bootstrap directory: %w", err)
	}
	return path, nil
}

// MonitorSupervisor keeps an owned launcher supervisor available with bounded
// exponential backoff. It returns only when ctx ends or home/executable
// validation makes further safe progress impossible.
func MonitorSupervisor(ctx context.Context, options EnsureOptions) error {
	if ctx == nil {
		ctx = context.Background()
	}
	_, err := EnsureSupervisor(ctx, options)
	if err != nil {
		return err
	}
	probe := time.NewTicker(2 * time.Second)
	defer probe.Stop()
	backoff := 100 * time.Millisecond
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-probe.C:
		}
		_, ensureErr := EnsureSupervisor(ctx, options)
		if ensureErr == nil {
			backoff = 100 * time.Millisecond
			continue
		}
		for {
			_, err = EnsureSupervisor(ctx, options)
			if err == nil {
				backoff = 100 * time.Millisecond
				break
			}
			timer := time.NewTimer(backoff)
			select {
			case <-ctx.Done():
				timer.Stop()
				return ctx.Err()
			case <-timer.C:
			}
			if backoff < 5*time.Second {
				backoff *= 2
				if backoff > 5*time.Second {
					backoff = 5 * time.Second
				}
			}
		}
	}
}
