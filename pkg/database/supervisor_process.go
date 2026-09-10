package database

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	supervisorBootstrapEnvironment          = "PICOCLAW_DATABASE_SUPERVISOR_BOOTSTRAP"
	supervisorBootstrapIdentityEnvironment  = "PICOCLAW_DATABASE_SUPERVISOR_BOOTSTRAP_IDENTITY"
	supervisorExecutableIdentityEnvironment = "PICOCLAW_DATABASE_SUPERVISOR_EXECUTABLE_IDENTITY"
	defaultSupervisorStartTimeout           = 8 * time.Second
	defaultSupervisorProbeInterval          = 2 * time.Second
	maximumSupervisorRetryBackoff           = 5 * time.Second
	initialSupervisorRetryBackoff           = 100 * time.Millisecond
	supervisorTerminateGrace                = 250 * time.Millisecond
	supervisorRootReapTimeout               = 2 * time.Second
)

type supervisorOperationError struct {
	message string
	cause   error
}

func (operationErr *supervisorOperationError) Error() string {
	if operationErr == nil {
		return "database supervisor operation failed"
	}
	return operationErr.message
}

func (operationErr *supervisorOperationError) Unwrap() error {
	if operationErr == nil {
		return nil
	}
	return operationErr.cause
}

func supervisorError(message string, cause error) error {
	if cause == nil {
		return nil
	}
	return &supervisorOperationError{message: message, cause: cause}
}

// EnsureOptions describe how a trusted composition layer starts the private
// broker when no healthy supervisor owns the canonical home. CatalogFingerprint
// must come from one immutable logical catalog snapshot derived above this
// provider-neutral package. ConfigPath is forwarded to the private child only.
type EnsureOptions struct {
	Home               string
	Executable         string
	ConfigPath         string
	CatalogFingerprint string
	Timeout            time.Duration
}

// EnsureSupervisor attaches to the canonical-home broker or starts the hidden
// supervisor process and waits for authenticated control-plane readiness.
// Concurrent callers may race to start; the broker singleton admits one winner.
func EnsureSupervisor(ctx context.Context, options EnsureOptions) (*Client, error) {
	return ensureSupervisor(ctx, options, supervisorEnsureOps{
		connect: connectReadyBrokerStatus, shutdown: shutdownBrokerForReplacement,
		start: startSupervisorProcessUntil, poll: 50 * time.Millisecond, now: time.Now,
	})
}

type supervisorEnsureOps struct {
	connect  func(context.Context, string) (*Client, BrokerStatus, error)
	shutdown func(context.Context, *Client, string) error
	start    func(EnsureOptions, string, time.Time) (*supervisorAttempt, error)
	poll     time.Duration
	now      func() time.Time
}

func ensureSupervisor(
	ctx context.Context,
	options EnsureOptions,
	ops supervisorEnsureOps,
) (result *Client, resultErr error) {
	var attempts []*supervisorAttempt
	keepPID := 0
	if ctx == nil {
		ctx = context.Background()
	}
	if !validCatalogFingerprint(options.CatalogFingerprint) || ops.connect == nil ||
		ops.shutdown == nil || ops.start == nil || ops.poll <= 0 || ops.now == nil {
		return nil, NewError(CodeInvalid, "database supervisor catalog fingerprint is invalid")
	}
	home, err := PrepareHome(options.Home)
	if err != nil {
		return nil, err
	}
	defer func() {
		// Use the canonical path captured before any launch. The caller may have
		// supplied a relative path and another goroutine may change the process
		// working directory while an attempt is starting.
		cleanupErr := cleanupSupervisorAttempts(home, options.CatalogFingerprint, attempts, keepPID)
		if cleanupErr != nil {
			result = nil
			resultErr = errors.Join(resultErr, cleanupErr)
		}
	}()
	timeout := options.Timeout
	if timeout <= 0 {
		timeout = defaultSupervisorStartTimeout
	}
	readyCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	startupDeadline, _ := readyCtx.Deadline()

	lastStart := time.Time{}
	attemptInFlight := false
	ticker := time.NewTicker(ops.poll)
	defer ticker.Stop()
	for {
		startAllowed := true
		client, status, connectErr := ops.connect(readyCtx, home)
		switch {
		case connectErr == nil && status.CatalogFingerprint == options.CatalogFingerprint:
			keepPID = status.PID
			return client, nil
		case connectErr == nil:
			if shutdownErr := ops.shutdown(readyCtx, client, status.Epoch); shutdownErr == nil {
				lastStart = time.Time{}
			} else if terminalSupervisorError(shutdownErr) {
				return nil, shutdownErr
			} else {
				// A retryable or outcome-unknown shutdown is not proof that the
				// observed generation stopped. Probe again before launching.
				startAllowed = false
			}
		case terminalSupervisorError(connectErr):
			return nil, connectErr
		}

		if err := supervisorStartupContextError(readyCtx); err != nil {
			return nil, err
		}
		if startAllowed && !attemptInFlight &&
			(lastStart.IsZero() || ops.now().Sub(lastStart) >= 500*time.Millisecond) {
			attempt, startErr := ops.start(options, home, startupDeadline)
			if attempt != nil {
				// The ensure operation owns this deadline invariant rather than
				// trusting a platform launcher or test seam to copy it correctly.
				attempt.deadline = startupDeadline
				attempts = append(attempts, attempt)
				attemptInFlight = true
			}
			if startErr == nil && attempt == nil {
				return nil, NewError(CodeInternal, "database supervisor launch returned no attempt")
			}
			if startErr == nil {
				lastStart = ops.now()
			} else if terminalSupervisorError(startErr) {
				return nil, startErr
			}
		}
		select {
		case <-readyCtx.Done():
			return nil, supervisorStartupContextError(readyCtx)
		case <-ticker.C:
		}
	}
}

func supervisorStartupContextError(ctx context.Context) error {
	if ctx == nil || ctx.Err() == nil {
		return nil
	}
	if errors.Is(ctx.Err(), context.Canceled) {
		return NewError(CodeDeadline, "database supervisor startup was canceled")
	}
	return NewError(CodeUnavailable, "database supervisor did not become ready")
}

func startSupervisorProcess(options EnsureOptions, home string) error {
	timeout := options.Timeout
	if timeout <= 0 {
		timeout = defaultSupervisorStartTimeout
	}
	attempt, err := startSupervisorProcessUntil(options, home, time.Now().Add(timeout))
	if attempt != nil {
		err = errors.Join(err, attempt.release())
	}
	return err
}

func startSupervisorProcessUntil(
	options EnsureOptions,
	home string,
	startupDeadline time.Time,
) (*supervisorAttempt, error) {
	return startSupervisorProcessUntilWith(options, home, startupDeadline, supervisorStartOps{
		prepareBootstrap:   prepareSupervisorBootstrapArtifact,
		discardBootstrap:   discardSupervisorBootstrap,
		configure:          configureSupervisorProcess,
		validateExecutable: validateSupervisorExecutableIdentity,
		launch:             launchSupervisorProcess,
	})
}

type supervisorStartOps struct {
	prepareBootstrap   func(string, string) (supervisorBootstrapArtifact, error)
	discardBootstrap   func(supervisorBootstrapArtifact) error
	configure          func(*exec.Cmd, string) error
	validateExecutable func(string, supervisorFileIdentity) error
	launch             func(*exec.Cmd) (*supervisorProcessLaunch, error)
}

func startSupervisorProcessUntilWith(
	options EnsureOptions,
	home string,
	startupDeadline time.Time,
	ops supervisorStartOps,
) (result *supervisorAttempt, resultErr error) {
	if ops.prepareBootstrap == nil || ops.discardBootstrap == nil || ops.configure == nil ||
		ops.validateExecutable == nil || ops.launch == nil {
		return nil, NewError(CodeInvalid, "database supervisor start operations are invalid")
	}
	spec, err := prepareSupervisorLaunch(options, home, startupDeadline)
	if err != nil {
		return nil, err
	}
	bootstrapArtifact, err := ops.prepareBootstrap(home, spec.bootstrap)
	if err != nil {
		return nil, err
	}
	cleanupBootstrap := true
	defer func() {
		if cleanupBootstrap {
			cleanupErr := ops.discardBootstrap(bootstrapArtifact)
			if cleanupErr != nil {
				result = nil
				resultErr = errors.Join(resultErr, cleanupErr)
			}
		}
	}()

	command := exec.Command(spec.executable, spec.arguments...)
	command.Env = replaceSupervisorEnvironment(
		spec.environment,
		supervisorBootstrapIdentityEnvironment,
		bootstrapArtifact.fileIdentity.String(),
	)
	if configureErr := ops.configure(command, home); configureErr != nil {
		return nil, configureErr
	}
	logFile, _ := command.Stdout.(*os.File)
	validationErr := errors.Join(
		ops.validateExecutable(
			spec.executable,
			spec.executableIdentity,
		),
		validateSupervisorExecutableAncestors(spec.executableAncestors),
	)
	if validationErr != nil {
		var closeErr error
		if logFile != nil {
			closeErr = supervisorError("close database supervisor log", logFile.Close())
		}
		return nil, errors.Join(validationErr, closeErr)
	}
	launch, startErr := ops.launch(command)
	var logCloseErr error
	if logFile != nil {
		logCloseErr = supervisorError("close database supervisor log", logFile.Close())
	}
	if startErr != nil {
		return nil, errors.Join(supervisorError("start database supervisor", startErr), logCloseErr)
	}
	if launch == nil || launch.pid <= 0 || launch.owner == nil || launch.done == nil {
		var cleanupErr error
		if launch != nil && launch.owner != nil {
			cleanupErr = errors.Join(launch.owner.kill(), launch.owner.close())
		}
		return nil, errors.Join(
			NewError(CodeIntegrity, "database supervisor launch ownership is invalid"),
			cleanupErr,
		)
	}
	attempt := &supervisorAttempt{
		pid: launch.pid, owner: launch.owner, done: launch.done,
		deadline: startupDeadline,
	}
	if logCloseErr != nil {
		return nil, errors.Join(logCloseErr, attempt.terminate())
	}

	// The child consumes the one-time file before loading configuration. Removing
	// it here would race the child, so only bound debris from an invalid child.
	cleanupBootstrap = false
	go func() {
		cleanupDelay := time.Until(startupDeadline)
		if cleanupDelay < 0 {
			cleanupDelay = 0
		}
		timer := time.NewTimer(cleanupDelay)
		defer timer.Stop()
		<-timer.C
		_ = ops.discardBootstrap(bootstrapArtifact)
	}()
	return attempt, nil
}

type supervisorLaunchSpec struct {
	executable          string
	executableIdentity  supervisorFileIdentity
	executableAncestors []supervisorExecutableAncestor
	bootstrap           string
	arguments           []string
	environment         []string
}

type supervisorExecutableAncestor struct {
	path     string
	identity supervisorFileIdentity
}

func prepareSupervisorLaunch(
	options EnsureOptions,
	home string,
	startupDeadline time.Time,
) (supervisorLaunchSpec, error) {
	return prepareSupervisorLaunchWith(options, home, startupDeadline, supervisorLaunchResolutionOps{
		executable:  os.Executable,
		lstat:       os.Lstat,
		identity:    supervisorPathIdentity,
		currentTest: currentTestExecutable,
		trust:       validateSupervisorExecutableTrust,
		ancestors:   captureSupervisorExecutableAncestors,
		randomHex:   randomHex,
	})
}

type supervisorLaunchResolutionOps struct {
	executable  func() (string, error)
	lstat       func(string) (os.FileInfo, error)
	identity    func(string) (supervisorFileIdentity, error)
	currentTest func(string) (bool, error)
	trust       func(string, os.FileInfo) error
	ancestors   func(string) ([]supervisorExecutableAncestor, error)
	randomHex   func(int) (string, error)
}

func prepareSupervisorLaunchWith(
	options EnsureOptions,
	home string,
	startupDeadline time.Time,
	ops supervisorLaunchResolutionOps,
) (supervisorLaunchSpec, error) {
	deadlineUnixNS := startupDeadline.UTC().UnixNano()
	if !validCatalogFingerprint(options.CatalogFingerprint) || startupDeadline.IsZero() || deadlineUnixNS <= 0 {
		return supervisorLaunchSpec{}, NewError(CodeInvalid, "database supervisor launch generation is invalid")
	}
	if !startupDeadline.After(time.Now()) {
		return supervisorLaunchSpec{}, NewError(CodeDeadline, "database supervisor startup deadline expired")
	}
	if options.Executable != strings.TrimSpace(options.Executable) ||
		strings.ContainsRune(options.Executable, 0) {
		return supervisorLaunchSpec{}, NewError(CodeInvalid, "database supervisor executable is invalid")
	}
	executable := options.Executable
	if executable == "" {
		resolved, err := ops.executable()
		if err != nil {
			return supervisorLaunchSpec{}, NewError(CodeUnavailable, "database supervisor executable is unavailable")
		}
		executable = resolved
	}
	if !filepath.IsAbs(executable) && !strings.ContainsRune(executable, os.PathSeparator) {
		resolved, err := exec.LookPath(executable)
		if err != nil {
			return supervisorLaunchSpec{}, NewError(CodeUnavailable, "database supervisor executable is unavailable")
		}
		executable = resolved
	}
	executable, err := filepath.Abs(executable)
	if err != nil {
		return supervisorLaunchSpec{}, NewError(CodeInvalid, "database supervisor executable is invalid")
	}
	resolvedExecutable, err := filepath.EvalSymlinks(executable)
	if err != nil {
		return supervisorLaunchSpec{}, NewError(CodeUnavailable, "database supervisor executable is unavailable")
	}
	resolvedExecutable, err = filepath.Abs(resolvedExecutable)
	if err != nil || !sameCanonicalPath(filepath.Clean(resolvedExecutable), filepath.Clean(executable)) {
		return supervisorLaunchSpec{}, NewError(CodeIntegrity, "database supervisor executable ancestor is invalid")
	}
	info, err := ops.lstat(executable)
	if err != nil {
		return supervisorLaunchSpec{}, NewError(CodeUnavailable, "database supervisor executable is unavailable")
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return supervisorLaunchSpec{}, NewError(CodeIntegrity, "database supervisor executable boundary is invalid")
	}
	executableIdentity, identityErr := ops.identity(executable)
	if identityErr != nil || !executableIdentity.Valid() {
		return supervisorLaunchSpec{}, NewError(CodeIntegrity, "database supervisor executable boundary is invalid")
	}
	isCurrentTest, testIdentityErr := ops.currentTest(executable)
	if testIdentityErr != nil {
		return supervisorLaunchSpec{}, NewError(
			CodeIntegrity,
			"database supervisor test executable identity is unavailable",
		)
	}
	if isCurrentTest {
		return supervisorLaunchSpec{}, NewError(
			CodeInvalid,
			"database supervisor executable cannot be the current test binary",
		)
	}
	if !supervisorExecutableModeValid(info) {
		return supervisorLaunchSpec{}, NewError(CodeIntegrity, "database supervisor executable boundary is invalid")
	}
	if trustErr := ops.trust(executable, info); trustErr != nil {
		return supervisorLaunchSpec{}, trustErr
	}
	executableAncestors, err := ops.ancestors(executable)
	if err != nil {
		return supervisorLaunchSpec{}, err
	}
	configPath := options.ConfigPath
	if configPath == "" {
		configPath = filepath.Join(home, "config.json")
	}
	if configPath != strings.TrimSpace(configPath) || strings.ContainsRune(configPath, 0) ||
		!filepath.IsAbs(configPath) || filepath.Clean(configPath) != configPath {
		return supervisorLaunchSpec{}, NewError(CodeInvalid, "database supervisor config path is invalid")
	}
	bootstrap, err := ops.randomHex(tokenBytes)
	if err != nil {
		return supervisorLaunchSpec{}, NewError(CodeInternal, "database supervisor bootstrap failed")
	}
	arguments := []string{
		"database", "__serve",
		"--home", home,
		"--expected-catalog-fingerprint", options.CatalogFingerprint,
		"--startup-deadline-unix-ns", strconv.FormatInt(deadlineUnixNS, 10),
	}
	environment := replaceSupervisorEnvironment(os.Environ(), supervisorBootstrapEnvironment, bootstrap)
	environment = replaceSupervisorEnvironment(
		environment,
		supervisorExecutableIdentityEnvironment,
		executableIdentity.String(),
	)
	environment = replaceSupervisorEnvironment(environment, "PICOCLAW_HOME", home)
	environment = replaceSupervisorEnvironment(environment, "PICOCLAW_CONFIG", configPath)
	return supervisorLaunchSpec{
		executable: executable, executableIdentity: executableIdentity,
		executableAncestors: executableAncestors,
		bootstrap:           bootstrap, arguments: arguments, environment: environment,
	}, nil
}

func captureSupervisorExecutableAncestors(
	executable string,
) ([]supervisorExecutableAncestor, error) {
	return captureSupervisorExecutableAncestorsWith(executable, supervisorExecutableAncestorOps{
		lstat: os.Lstat, trust: validateSupervisorExecutableAncestorTrust,
		identity: supervisorDirectoryIdentity,
	})
}

type supervisorExecutableAncestorOps struct {
	lstat    func(string) (os.FileInfo, error)
	trust    func(string, os.FileInfo) error
	identity func(string) (supervisorFileIdentity, error)
}

func captureSupervisorExecutableAncestorsWith(
	executable string,
	ops supervisorExecutableAncestorOps,
) ([]supervisorExecutableAncestor, error) {
	current := filepath.Dir(executable)
	result := make([]supervisorExecutableAncestor, 0, 8)
	for {
		info, err := ops.lstat(current)
		if err != nil || info == nil || info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return nil, NewError(CodeIntegrity, "database supervisor executable ancestor is invalid")
		}
		if trustErr := ops.trust(current, info); trustErr != nil {
			return nil, trustErr
		}
		identity, err := ops.identity(current)
		if err != nil {
			return nil, NewError(CodeIntegrity, "database supervisor executable ancestor identity is unavailable")
		}
		result = append(result, supervisorExecutableAncestor{path: current, identity: identity})
		parent := filepath.Dir(current)
		if sameCanonicalPath(parent, current) {
			return result, nil
		}
		current = parent
	}
}

func validateSupervisorExecutableAncestors(expected []supervisorExecutableAncestor) error {
	if len(expected) == 0 {
		return NewError(CodeIntegrity, "database supervisor executable ancestors are unavailable")
	}
	for _, ancestor := range expected {
		info, err := os.Lstat(ancestor.path)
		identity, identityErr := supervisorDirectoryIdentity(ancestor.path)
		if err != nil || identityErr != nil || info == nil ||
			info.Mode()&os.ModeSymlink != 0 || !info.IsDir() || !ancestor.identity.Valid() ||
			identity != ancestor.identity || validateSupervisorExecutableAncestorTrust(ancestor.path, info) != nil {
			return NewError(CodeIntegrity, "database supervisor executable ancestor changed before launch")
		}
	}
	return nil
}

func validateSupervisorExecutableIdentity(
	path string,
	expected supervisorFileIdentity,
) error {
	resolved, resolveErr := filepath.EvalSymlinks(path)
	resolved, absoluteErr := filepath.Abs(resolved)
	info, lstatErr := os.Lstat(path)
	current, err := supervisorPathIdentity(path)
	if resolveErr != nil || absoluteErr != nil ||
		!sameCanonicalPath(filepath.Clean(resolved), filepath.Clean(path)) ||
		lstatErr != nil || info == nil || info.Mode()&os.ModeSymlink != 0 ||
		!supervisorExecutableModeValid(info) || validateSupervisorExecutableTrust(path, info) != nil ||
		err != nil || !expected.Valid() || current != expected {
		return NewError(CodeIntegrity, "database supervisor executable changed before launch")
	}
	return nil
}

type supervisorProcessOwner interface {
	activate() error
	exited() bool
	terminate() error
	kill() error
	close() error
}

type supervisorProcessLaunch struct {
	pid   int
	owner supervisorProcessOwner
	done  chan struct{}
}

type supervisorAttempt struct {
	pid      int
	owner    supervisorProcessOwner
	done     chan struct{}
	deadline time.Time
	mu       sync.Mutex
	closed   bool
}

func supervisorProcessExited(owner supervisorProcessOwner, done <-chan struct{}) bool {
	return supervisorAttemptDone(done) || owner != nil && owner.exited()
}

func (attempt *supervisorAttempt) release() error {
	if attempt == nil {
		return nil
	}
	attempt.mu.Lock()
	defer attempt.mu.Unlock()
	if attempt.closed {
		return nil
	}
	attempt.closed = true
	if attempt.owner == nil {
		return NewError(CodeIntegrity, "database supervisor process ownership is unavailable")
	}
	return attempt.owner.close()
}

func (attempt *supervisorAttempt) terminate() error {
	if attempt == nil {
		return nil
	}
	attempt.mu.Lock()
	if attempt.closed {
		attempt.mu.Unlock()
		return nil
	}
	attempt.closed = true
	owner := attempt.owner
	done := attempt.done
	attempt.mu.Unlock()
	if owner == nil || done == nil {
		return NewError(CodeIntegrity, "database supervisor process ownership is unavailable")
	}

	var result error
	rootExited := supervisorProcessExited(owner, done)
	if !rootExited {
		result = errors.Join(result, owner.terminate())
		timer := time.NewTimer(supervisorTerminateGrace)
		select {
		case <-done:
		case <-timer.C:
		}
		if !timer.Stop() {
			select {
			case <-timer.C:
			default:
			}
		}
	}
	// Always force the retained process group/job after the cooperative phase,
	// even when the root exited after TERM: descendants may still be alive.
	result = errors.Join(result, owner.kill())
	if !supervisorAttemptDone(done) {
		timer := time.NewTimer(supervisorRootReapTimeout)
		select {
		case <-done:
		case <-timer.C:
			result = errors.Join(result, NewError(
				CodeUnavailable,
				"database supervisor process could not be reaped",
			))
		}
		if !timer.Stop() {
			select {
			case <-timer.C:
			default:
			}
		}
	}
	result = errors.Join(result, owner.close())
	return result
}

func supervisorAttemptDone(done <-chan struct{}) bool {
	if done == nil {
		return true
	}
	select {
	case <-done:
		return true
	default:
		return false
	}
}

func (attempt *supervisorAttempt) releaseIfLive() (bool, error) {
	if attempt == nil {
		return false, nil
	}
	attempt.mu.Lock()
	defer attempt.mu.Unlock()
	if attempt.closed || supervisorProcessExited(attempt.owner, attempt.done) {
		return false, nil
	}
	if attempt.owner == nil {
		return false, NewError(CodeIntegrity, "database supervisor process ownership is unavailable")
	}
	attempt.closed = true
	return true, attempt.owner.close()
}

func cleanupSupervisorAttempts(
	home string,
	expectedFingerprint string,
	attempts []*supervisorAttempt,
	keepPID int,
) error {
	return cleanupSupervisorAttemptsWith(
		home,
		expectedFingerprint,
		attempts,
		keepPID,
		readySupervisorPID,
	)
}

func cleanupSupervisorAttemptsWith(
	home string,
	expectedFingerprint string,
	attempts []*supervisorAttempt,
	keepPID int,
	readyPID func(string, string) int,
) error {
	if len(attempts) == 0 {
		return nil
	}
	var cleanup sync.WaitGroup
	errorsByAttempt := make(chan error, len(attempts))
	for _, attempt := range attempts {
		if attempt == nil {
			continue
		}
		if attempt.pid == keepPID {
			preserved, releaseErr := attempt.releaseIfLive()
			if releaseErr != nil {
				errorsByAttempt <- releaseErr
			}
			if preserved {
				continue
			}
		}
		cleanup.Add(1)
		go func() {
			defer cleanup.Done()
			// A child is allowed to publish through its absolute launch
			// deadline. Do not tear down its process group/job before that
			// point: another caller may attach to the winner after this caller's
			// final readiness probe or cancellation.
			if delay := time.Until(attempt.deadline); delay > 0 {
				timer := time.NewTimer(delay)
				<-timer.C
				timer.Stop()
			}
			// Authenticate immediately before the destructive action. Once the
			// deadline has passed, the child's prepublication guard prevents this
			// attempt from becoming a valid matching winner later.
			if readyPID != nil && readyPID(home, expectedFingerprint) == attempt.pid {
				preserved, releaseErr := attempt.releaseIfLive()
				if releaseErr != nil {
					errorsByAttempt <- releaseErr
				}
				if preserved {
					return
				}
			}
			if terminateErr := attempt.terminate(); terminateErr != nil {
				errorsByAttempt <- terminateErr
			}
		}()
	}
	cleanup.Wait()
	close(errorsByAttempt)
	var result error
	for cleanupErr := range errorsByAttempt {
		result = errors.Join(result, cleanupErr)
	}
	return result
}

func readySupervisorPID(home, expectedFingerprint string) int {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_, status, err := connectReadyBrokerStatus(ctx, home)
	if err != nil || status.CatalogFingerprint != expectedFingerprint {
		return 0
	}
	return status.PID
}

func configureSupervisorLog(command *exec.Cmd, home string) error {
	logFile, err := openSupervisorLog(home)
	if err != nil {
		return err
	}
	command.Stdin = nil
	command.Stdout = logFile
	command.Stderr = logFile
	return nil
}

func openSupervisorLog(home string) (*os.File, error) {
	return openSupervisorLogWith(home, supervisorLogOps{
		canonicalHome: CanonicalHome,
		lstat:         os.Lstat,
		createDirectory: func(path string) error {
			return createOwnerOnlyDirectory(path)
		},
		validateDirectory:  validateOwnerOnlyDirectory,
		createFile:         createOwnerOnlyExclusiveAppendFile,
		validateFile:       validateOwnerOnlyFile,
		validateOpenedFile: validateSupervisorOpenedFile,
		openAppend:         openOwnerOnlyAppendFile,
		validateBoundary:   validateSupervisorLogDirectory,
		directoryIdentity:  supervisorDirectoryIdentity,
	})
}

type supervisorLogOps struct {
	canonicalHome      func(string) (string, error)
	lstat              func(string) (os.FileInfo, error)
	createDirectory    func(string) error
	validateDirectory  func(string, os.FileInfo) error
	createFile         func(string, os.FileMode) (*os.File, error)
	validateFile       func(string, os.FileInfo, os.FileMode) error
	validateOpenedFile func(string, *os.File, os.FileMode) error
	openAppend         func(string, os.FileMode) (*os.File, error)
	validateBoundary   func(string, supervisorFileIdentity) error
	directoryIdentity  func(string) (supervisorFileIdentity, error)
}

func openSupervisorLogWith(home string, ops supervisorLogOps) (*os.File, error) {
	if ops.canonicalHome == nil || ops.lstat == nil || ops.createDirectory == nil ||
		ops.validateDirectory == nil || ops.createFile == nil || ops.validateFile == nil ||
		ops.validateOpenedFile == nil || ops.openAppend == nil || ops.validateBoundary == nil ||
		ops.directoryIdentity == nil {
		return nil, NewError(CodeInvalid, "database supervisor log operations are invalid")
	}
	canonical, err := ops.canonicalHome(home)
	if err != nil {
		return nil, err
	}
	logs := filepath.Join(canonical, "logs")
	info, err := ops.lstat(logs)
	if errors.Is(err, os.ErrNotExist) {
		if createErr := ops.createDirectory(logs); createErr != nil && !errors.Is(createErr, os.ErrExist) {
			return nil, supervisorError("create database supervisor log directory", createErr)
		}
		info, err = ops.lstat(logs)
	}
	if err != nil {
		return nil, supervisorError("inspect database supervisor log directory", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return nil, NewError(CodeIntegrity, "database supervisor log directory is invalid")
	}
	if validationErr := ops.validateDirectory(logs, info); validationErr != nil {
		return nil, validationErr
	}
	logsIdentity, err := ops.directoryIdentity(logs)
	if err != nil {
		return nil, NewError(CodeIntegrity, "database supervisor log directory identity is unavailable")
	}

	path := filepath.Join(logs, "database-supervisor.log")
	info, err = ops.lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		file, createErr := ops.createFile(path, 0o600)
		if createErr == nil {
			if validationErr := ops.validateOpenedFile(path, file, 0o600); validationErr != nil {
				if file != nil {
					_ = file.Close()
				}
				return nil, validationErr
			}
			if boundaryErr := ops.validateBoundary(logs, logsIdentity); boundaryErr != nil {
				_ = file.Close()
				return nil, boundaryErr
			}
			return file, nil
		}
		if !errors.Is(createErr, os.ErrExist) {
			return nil, supervisorError("create database supervisor log", createErr)
		}
		info, err = ops.lstat(path)
	}
	if err != nil {
		return nil, supervisorError("inspect database supervisor log", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || ops.validateFile(path, info, 0o600) != nil {
		return nil, NewError(CodeIntegrity, "database supervisor log boundary is invalid")
	}
	file, err := ops.openAppend(path, 0o600)
	if err != nil {
		return nil, supervisorError("open database supervisor log", err)
	}
	if validationErr := ops.validateOpenedFile(path, file, 0o600); validationErr != nil {
		_ = file.Close()
		return nil, validationErr
	}
	if boundaryErr := ops.validateBoundary(logs, logsIdentity); boundaryErr != nil {
		_ = file.Close()
		return nil, boundaryErr
	}
	return file, nil
}

func validateSupervisorLogDirectory(path string, expected supervisorFileIdentity) error {
	current, err := os.Lstat(path)
	identity, identityErr := supervisorDirectoryIdentity(path)
	if err != nil || identityErr != nil || !expected.Valid() || current == nil || identity != expected ||
		current.Mode()&os.ModeSymlink != 0 || !current.IsDir() ||
		validateOwnerOnlyDirectory(path, current) != nil {
		return NewError(CodeIntegrity, "database supervisor log directory changed while opening")
	}
	return nil
}

func replaceSupervisorEnvironment(environment []string, name, value string) []string {
	result := make([]string, 0, len(environment)+1)
	for _, entry := range environment {
		key, _, found := strings.Cut(entry, "=")
		if !found || !sameSupervisorEnvironmentName(key, name) {
			result = append(result, entry)
		}
	}
	return append(result, name+"="+value)
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

func shutdownBrokerForReplacement(ctx context.Context, client *Client, observedEpoch string) error {
	return shutdownBrokerForReplacementWith(ctx, client, observedEpoch, supervisorReplacementOps{
		bind: ConnectWithManifest,
		gone: replacementEpochGone,
		shutdown: func(ctx context.Context, client *Client) error {
			return client.Shutdown(ctx)
		},
	})
}

type supervisorReplacementOps struct {
	bind     func(string, Manifest) (*Client, error)
	gone     func(context.Context, string, string) (bool, error)
	shutdown func(context.Context, *Client) error
}

func shutdownBrokerForReplacementWith(
	ctx context.Context,
	client *Client,
	observedEpoch string,
	ops supervisorReplacementOps,
) error {
	if ops.bind == nil || ops.gone == nil || ops.shutdown == nil {
		return NewError(CodeInvalid, "database supervisor replacement operations are invalid")
	}
	if client == nil || !validLowerHex(observedEpoch, epochBytes*2) {
		return NewError(CodeUnavailable, "database broker is unavailable")
	}
	client.mu.RLock()
	home := client.home
	manifest := client.manifest
	client.mu.RUnlock()
	if manifest.Epoch != observedEpoch {
		return NewError(CodeConflict, "database broker epoch changed before replacement")
	}
	boundClient, err := ops.bind(home, manifest)
	if err != nil {
		gone, goneErr := ops.gone(ctx, home, observedEpoch)
		if gone {
			return nil
		}
		return errors.Join(err, goneErr)
	}
	gone, goneErr := ops.gone(ctx, home, observedEpoch)
	if gone {
		return nil
	}
	if goneErr != nil {
		return goneErr
	}
	shutdownCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	err = ops.shutdown(shutdownCtx, boundClient)
	cancel()
	if err != nil {
		gone, goneErr = ops.gone(ctx, home, observedEpoch)
		if gone {
			return nil
		}
		if terminalSupervisorError(goneErr) {
			return goneErr
		}
		switch CodeOf(err) {
		case CodeOutcomeUnknown, CodeUnavailable, CodeConflict:
		default:
			return errors.Join(err, goneErr)
		}
	}
	ticker := time.NewTicker(25 * time.Millisecond)
	defer ticker.Stop()
	for {
		gone, checkErr := ops.gone(ctx, home, observedEpoch)
		if gone {
			return nil
		}
		if terminalSupervisorError(checkErr) {
			return checkErr
		}
		select {
		case <-ctx.Done():
			return NewError(CodeDeadline, "database supervisor replacement was canceled")
		case <-ticker.C:
		}
	}
}

func replacementEpochGone(ctx context.Context, home, observedEpoch string) (bool, error) {
	return replacementEpochGoneWith(ctx, home, observedEpoch, supervisorReplacementObservationOps{
		read: ReadManifest,
		bind: ConnectWithManifest,
		ping: func(ctx context.Context, client *Client) error {
			_, err := client.Ping(ctx)
			return err
		},
	})
}

type supervisorReplacementObservationOps struct {
	read func(string) (Manifest, error)
	bind func(string, Manifest) (*Client, error)
	ping func(context.Context, *Client) error
}

func replacementEpochGoneWith(
	ctx context.Context,
	home string,
	observedEpoch string,
	ops supervisorReplacementObservationOps,
) (bool, error) {
	if ops.read == nil || ops.bind == nil || ops.ping == nil {
		return false, NewError(CodeInvalid, "database supervisor replacement observation operations are invalid")
	}
	manifest, err := ops.read(home)
	if err != nil {
		if CodeOf(err) == CodeUnavailable {
			return true, nil
		}
		return false, err
	}
	if manifest.Epoch == observedEpoch {
		return false, nil
	}
	candidate, err := ops.bind(home, manifest)
	if err != nil {
		return false, err
	}
	pingCtx, cancel := context.WithTimeout(ctx, 250*time.Millisecond)
	defer cancel()
	if err := ops.ping(pingCtx, candidate); err != nil {
		return false, err
	}
	return true, nil
}

// ConsumeSupervisorBootstrap proves that the hidden serve command was created
// by EnsureSupervisor. The authority is consumed before config or stores load
// and is never converted into provider access authority.
//
// Deprecated: production supervisor composition must use
// ConsumeSupervisorBootstrapGuard and retain its physical-home guard through
// discovery publication.
func ConsumeSupervisorBootstrap(home string) bool {
	_, err := consumeSupervisorBootstrapGuardWith(
		home,
		consumeSupervisorBootstrapEnvironment(),
		defaultSupervisorBootstrapGuardOps(),
	)
	return err == nil
}

func consumeSupervisorBootstrapEnvironment() supervisorBootstrapAuthority {
	authority := supervisorBootstrapAuthority{
		token:              os.Getenv(supervisorBootstrapEnvironment),
		bootstrapIdentity:  os.Getenv(supervisorBootstrapIdentityEnvironment),
		executableIdentity: os.Getenv(supervisorExecutableIdentityEnvironment),
	}
	_ = os.Unsetenv(supervisorBootstrapEnvironment)
	_ = os.Unsetenv(supervisorBootstrapIdentityEnvironment)
	_ = os.Unsetenv(supervisorExecutableIdentityEnvironment)
	return authority
}

type supervisorBootstrapAuthority struct {
	token              string
	bootstrapIdentity  string
	executableIdentity string
}

type supervisorBootstrapConsumeOps struct {
	executable        func() (string, error)
	pathIdentity      func(string) (supervisorFileIdentity, error)
	stateDirectory    func(string) (string, error)
	directoryIdentity func(string) (supervisorFileIdentity, error)
	consume           func(string, supervisorFileIdentity, string, string) error
}

func consumeSupervisorBootstrapWith(
	home string,
	authority supervisorBootstrapAuthority,
	ops supervisorBootstrapConsumeOps,
) bool {
	if ops.executable == nil || ops.pathIdentity == nil || ops.stateDirectory == nil ||
		ops.directoryIdentity == nil || ops.consume == nil {
		return false
	}
	if !supervisorBootstrapImageAuthorityValid(authority, ops) {
		return false
	}
	stateDir, err := ops.stateDirectory(home)
	if err != nil {
		return false
	}
	stateIdentity, err := ops.directoryIdentity(stateDir)
	if err != nil {
		return false
	}
	return ops.consume(
		stateDir,
		stateIdentity,
		".bootstrap-"+authority.token,
		authority.bootstrapIdentity,
	) == nil
}

func prepareSupervisorBootstrap(home, token string) (string, error) {
	artifact, err := prepareSupervisorBootstrapArtifact(home, token)
	return artifact.path, err
}

type supervisorBootstrapArtifact struct {
	path          string
	stateDir      string
	name          string
	stateIdentity supervisorFileIdentity
	fileIdentity  supervisorFileIdentity
}

func prepareSupervisorBootstrapArtifact(
	home string,
	token string,
) (supervisorBootstrapArtifact, error) {
	return prepareSupervisorBootstrapArtifactWith(home, token, supervisorBootstrapOps{
		create: createOwnerOnlyExclusiveFile,
		syncFile: func(file *os.File) error {
			return file.Sync()
		},
		closeFile: func(file *os.File) error {
			return file.Close()
		},
		identity:          supervisorOpenedIdentity,
		discard:           discardSupervisorBootstrap,
		syncDir:           syncDirectory,
		directoryIdentity: supervisorDirectoryIdentity,
	})
}

type supervisorBootstrapOps struct {
	create            func(string, os.FileMode) (*os.File, error)
	syncFile          func(*os.File) error
	closeFile         func(*os.File) error
	identity          func(*os.File) (supervisorFileIdentity, error)
	discard           func(supervisorBootstrapArtifact) error
	syncDir           func(string) error
	directoryIdentity func(string) (supervisorFileIdentity, error)
}

func prepareSupervisorBootstrapWith(home, token string, ops supervisorBootstrapOps) (string, error) {
	artifact, err := prepareSupervisorBootstrapArtifactWith(home, token, ops)
	return artifact.path, err
}

func prepareSupervisorBootstrapArtifactWith(
	home string,
	token string,
	ops supervisorBootstrapOps,
) (supervisorBootstrapArtifact, error) {
	if !validLowerHex(token, tokenBytes*2) {
		return supervisorBootstrapArtifact{}, NewError(
			CodeInvalid,
			"database supervisor bootstrap token is invalid",
		)
	}
	if ops.create == nil || ops.syncFile == nil || ops.closeFile == nil ||
		ops.identity == nil || ops.discard == nil || ops.syncDir == nil ||
		ops.directoryIdentity == nil {
		return supervisorBootstrapArtifact{}, NewError(
			CodeInvalid,
			"database supervisor bootstrap operations are invalid",
		)
	}
	stateDir, err := prepareStateDirectory(home)
	if err != nil {
		return supervisorBootstrapArtifact{}, err
	}
	stateIdentity, err := ops.directoryIdentity(stateDir)
	if err != nil {
		return supervisorBootstrapArtifact{}, supervisorError(
			"inspect database supervisor bootstrap directory",
			err,
		)
	}
	name := ".bootstrap-" + token
	path := filepath.Join(stateDir, name)
	artifact := supervisorBootstrapArtifact{
		path: path, stateDir: stateDir, name: name, stateIdentity: stateIdentity,
	}
	file, err := ops.create(path, 0o600)
	if err != nil {
		return supervisorBootstrapArtifact{}, supervisorError("create database supervisor bootstrap", err)
	}
	if file == nil {
		return supervisorBootstrapArtifact{}, NewError(
			CodeInternal,
			"database supervisor bootstrap creation failed",
		)
	}
	fileIdentity, err := ops.identity(file)
	if err != nil || !fileIdentity.Valid() {
		_ = ops.closeFile(file)
		return supervisorBootstrapArtifact{}, NewError(
			CodeIntegrity,
			"database supervisor bootstrap identity is unavailable",
		)
	}
	artifact.fileIdentity = fileIdentity
	if validationErr := validateSupervisorBootstrapBoundary(
		stateDir, stateIdentity, path, file,
	); validationErr != nil {
		_ = ops.closeFile(file)
		return supervisorBootstrapArtifact{}, errors.Join(validationErr, ops.discard(artifact))
	}
	if err := ops.syncFile(file); err != nil {
		_ = ops.closeFile(file)
		primary := supervisorError("sync database supervisor bootstrap", err)
		return supervisorBootstrapArtifact{}, errors.Join(primary, ops.discard(artifact))
	}
	if err := ops.closeFile(file); err != nil {
		primary := supervisorError("close database supervisor bootstrap", err)
		return supervisorBootstrapArtifact{}, errors.Join(primary, ops.discard(artifact))
	}
	if err := ops.syncDir(stateDir); err != nil {
		primary := supervisorError("sync database supervisor bootstrap directory", err)
		return supervisorBootstrapArtifact{}, errors.Join(primary, ops.discard(artifact))
	}
	return artifact, nil
}

func discardSupervisorBootstrap(artifact supervisorBootstrapArtifact) error {
	if artifact.path == "" || artifact.stateDir == "" || artifact.name == "" ||
		!artifact.stateIdentity.Valid() ||
		filepath.Join(artifact.stateDir, artifact.name) != artifact.path {
		return NewError(CodeInvalid, "database supervisor bootstrap artifact is invalid")
	}
	return consumeSupervisorBootstrapFileExpected(
		artifact.stateDir,
		artifact.stateIdentity,
		artifact.name,
		artifact.fileIdentity,
	)
}

func validateSupervisorBootstrapBoundary(
	stateDir string,
	expectedState supervisorFileIdentity,
	path string,
	file *os.File,
) error {
	currentState, stateErr := os.Lstat(stateDir)
	currentStateIdentity, stateIdentityErr := supervisorDirectoryIdentity(stateDir)
	var opened os.FileInfo
	var statErr error
	var links int64
	var linkErr error
	var openedIdentity supervisorFileIdentity
	var openedIdentityErr error
	if file == nil {
		statErr = NewError(CodeIntegrity, "database supervisor bootstrap file is unavailable")
	} else {
		opened, statErr = file.Stat()
		links, linkErr = supervisorFileLinkCount(file)
		openedIdentity, openedIdentityErr = supervisorOpenedIdentity(file)
	}
	current, lstatErr := os.Lstat(path)
	currentIdentity, currentIdentityErr := supervisorPathIdentity(path)
	if stateErr != nil || stateIdentityErr != nil || statErr != nil || linkErr != nil ||
		openedIdentityErr != nil || lstatErr != nil || currentIdentityErr != nil || !expectedState.Valid() ||
		currentState == nil || opened == nil || current == nil ||
		currentStateIdentity != expectedState || openedIdentity != currentIdentity || links != 1 ||
		currentState.Mode()&os.ModeSymlink != 0 || current.Mode()&os.ModeSymlink != 0 ||
		validateOwnerOnlyDirectory(stateDir, currentState) != nil ||
		validateOwnerOnlyFile(path, opened, 0o600) != nil ||
		validateOwnerOnlyFile(path, current, 0o600) != nil {
		return errors.Join(
			NewError(CodeIntegrity, "database supervisor bootstrap boundary changed"),
			stateErr, stateIdentityErr, statErr, linkErr, openedIdentityErr, lstatErr, currentIdentityErr,
		)
	}
	return nil
}

// MonitorSupervisor keeps a supervisor available with bounded exponential
// backoff. It returns when ctx ends or validation makes progress impossible.
func MonitorSupervisor(ctx context.Context, options EnsureOptions) error {
	return monitorSupervisor(ctx, options, supervisorMonitorOps{
		ensure: EnsureSupervisor, probeInterval: defaultSupervisorProbeInterval,
		maximumBackoff: maximumSupervisorRetryBackoff,
	})
}

type supervisorMonitorOps struct {
	ensure         func(context.Context, EnsureOptions) (*Client, error)
	probeInterval  time.Duration
	initialBackoff time.Duration
	maximumBackoff time.Duration
}

func monitorSupervisor(ctx context.Context, options EnsureOptions, ops supervisorMonitorOps) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if ops.ensure == nil || ops.probeInterval <= 0 || ops.maximumBackoff <= 0 {
		return NewError(CodeInvalid, "database supervisor monitor timing is invalid")
	}
	backoff := ops.initialBackoff
	if backoff <= 0 {
		backoff = initialSupervisorRetryBackoff
	}
	for {
		if _, err := ops.ensure(ctx, options); err == nil {
			break
		} else if terminalSupervisorError(err) {
			return err
		}
		timer := time.NewTimer(backoff)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
		if backoff < ops.maximumBackoff {
			backoff *= 2
			if backoff > ops.maximumBackoff {
				backoff = ops.maximumBackoff
			}
		}
	}
	probe := time.NewTicker(ops.probeInterval)
	defer probe.Stop()
	backoff = ops.initialBackoff
	if backoff <= 0 {
		backoff = initialSupervisorRetryBackoff
	}
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-probe.C:
		}
		if _, err := ops.ensure(ctx, options); err == nil {
			backoff = ops.initialBackoff
			if backoff <= 0 {
				backoff = initialSupervisorRetryBackoff
			}
			continue
		} else if terminalSupervisorError(err) {
			return err
		}
		for {
			if _, err := ops.ensure(ctx, options); err == nil {
				backoff = ops.initialBackoff
				if backoff <= 0 {
					backoff = initialSupervisorRetryBackoff
				}
				break
			} else if terminalSupervisorError(err) {
				return err
			}
			timer := time.NewTimer(backoff)
			select {
			case <-ctx.Done():
				timer.Stop()
				return ctx.Err()
			case <-timer.C:
			}
			if backoff < ops.maximumBackoff {
				backoff *= 2
				if backoff > ops.maximumBackoff {
					backoff = ops.maximumBackoff
				}
			}
		}
	}
}

func terminalSupervisorError(err error) bool {
	switch CodeOf(err) {
	case CodeInvalid, CodeUnauthorized, CodeIntegrity, CodeUnsupported:
		return true
	default:
		return false
	}
}
