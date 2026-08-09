//go:build linux

package localci

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

const (
	maximumSandboxFiles = 100_000
	maximumSandboxBytes = int64(1 << 30)
	hostSandboxPath     = "/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"
)

type linuxSandbox struct {
	temporaryRoot string
	bubblewrap    string
	generation    [32]byte
	mounts        []DependencyMount
	resources     *systemdResourceController
}

func (*linuxSandbox) localCISandbox() {}

type boundedOutput struct {
	mu       sync.Mutex
	data     []byte
	limit    int
	overflow chan struct{}
	once     sync.Once
}

func newPlatformSandbox(config SandboxConfig, generation [32]byte) (Sandbox, error) {
	temporaryRoot := strings.TrimSpace(config.TemporaryRoot)
	if temporaryRoot == "" {
		temporaryRoot = os.TempDir()
	}
	absolute, err := filepath.Abs(temporaryRoot)
	if err != nil {
		return nil, fmt.Errorf("resolve local CI temporary root: %w", err)
	}
	info, err := os.Lstat(absolute)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("%w: local CI temporary root must be an existing real directory", ErrInvalid)
	}
	resolved, err := filepath.EvalSymlinks(absolute)
	if err != nil || resolved != absolute {
		return nil, fmt.Errorf("%w: local CI temporary root must be canonical", ErrInvalid)
	}
	bubblewrap := strings.TrimSpace(config.BubblewrapPath)
	if bubblewrap == "" {
		bubblewrap, err = exec.LookPath("bwrap")
		if err != nil {
			return nil, fmt.Errorf("%w: bubblewrap was not found", ErrSandboxUnavailable)
		}
	}
	bubblewrap, err = filepath.Abs(bubblewrap)
	if err != nil {
		return nil, fmt.Errorf("resolve bubblewrap: %w", err)
	}
	bubblewrap, err = filepath.EvalSymlinks(bubblewrap)
	if err != nil {
		return nil, fmt.Errorf("%w: resolve bubblewrap: %v", ErrSandboxUnavailable, err)
	}
	info, err = os.Stat(bubblewrap)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&0o111 == 0 {
		return nil, fmt.Errorf("%w: bubblewrap is not an executable regular file", ErrSandboxUnavailable)
	}
	mounts := append([]DependencyMount(nil), config.DependencyMounts...)
	slices.SortFunc(mounts, func(left, right DependencyMount) int {
		return strings.Compare(left.Target, right.Target)
	})
	for index, mount := range mounts {
		source, resolveErr := filepath.Abs(mount.Source)
		if resolveErr != nil {
			return nil, fmt.Errorf("resolve dependency mount: %w", resolveErr)
		}
		resolvedSource, resolveErr := filepath.EvalSymlinks(source)
		mountInfo, statErr := os.Stat(source)
		if resolveErr != nil || statErr != nil || resolvedSource != source || !mountInfo.IsDir() {
			return nil, fmt.Errorf("%w: dependency mount must be a canonical directory", ErrInvalid)
		}
		mounts[index].Source = source
		if index > 0 && mounts[index-1].Target == mount.Target {
			return nil, fmt.Errorf("%w: duplicate dependency mount target", ErrInvalid)
		}
	}
	resources, err := newSystemdResourceController(config.SystemdRunPath, config.SystemctlPath)
	if err != nil {
		return nil, err
	}
	return &linuxSandbox{
		temporaryRoot: absolute,
		bubblewrap:    bubblewrap,
		generation:    generation,
		mounts:        mounts,
		resources:     resources,
	}, nil
}

func (sandbox *linuxSandbox) PassingCacheAllowed() bool {
	// The current backend intentionally uses mutable host toolchains. Exact
	// success reuse remains disabled until an immutable image/view manifest is
	// available; content-addressed discovery and evidence are still persisted.
	return false
}

func (sandbox *linuxSandbox) EnvironmentDigest(ctx context.Context, plan Plan) (string, error) {
	if sandbox == nil {
		return "", ErrSandboxUnavailable
	}
	if ctx == nil {
		ctx = context.Background()
	}
	normalized, err := normalizePlan(plan)
	if err != nil {
		return "", err
	}
	identities := []string{
		SandboxProfile,
		runtime.GOOS,
		runtime.GOARCH,
		hex.EncodeToString(sandbox.generation[:]),
		normalized.Digest,
	}
	resourceIdentity, err := sandbox.resources.Identity(ctx)
	if err != nil {
		return "", err
	}
	if err = sandbox.probeBubblewrap(ctx); err != nil {
		return "", err
	}
	identities = append(identities, resourceIdentity)
	binaries, err := sandbox.requiredBinaries(normalized)
	if err != nil {
		return "", err
	}
	for _, binary := range binaries {
		if err = ctx.Err(); err != nil {
			return "", err
		}
		digest, digestErr := digestRegularFile(binary.hostPath, 64<<20)
		if digestErr != nil {
			return "", fmt.Errorf("%w: fingerprint %s: %v", ErrEnvironmentUnavailable, binary.sandboxPath, digestErr)
		}
		identities = append(identities, binary.sandboxPath, digest)
	}
	for _, mount := range sandbox.mounts {
		identities = append(identities, mount.Target, mount.Digest)
	}
	parts := make([][]byte, len(identities))
	for index := range identities {
		parts[index] = []byte(identities[index])
	}
	return digestParts("picoclaw-local-ci-environment-v1", parts...), nil
}

func (sandbox *linuxSandbox) probeBubblewrap(ctx context.Context) error {
	unit, err := localCIUnitName("probe-bwrap")
	if err != nil {
		return err
	}
	arguments := []string{
		"--die-with-parent", "--new-session", "--unshare-all", "--clearenv",
		"--ro-bind", "/usr", "/usr",
	}
	for _, systemPath := range []string{"/lib", "/lib64"} {
		if _, statErr := os.Stat(systemPath); statErr == nil {
			arguments = append(arguments, "--ro-bind", systemPath, systemPath)
		}
	}
	arguments = append(arguments, "--", "/usr/bin/true")
	command := sandbox.resources.command(ctx, unit, 10*time.Second, sandbox.bubblewrap, arguments...)
	if output, runErr := command.CombinedOutput(); runErr != nil {
		if len(output) > 4<<10 {
			output = output[:4<<10]
		}
		return fmt.Errorf(
			"%w: bubblewrap capability probe failed: %s",
			ErrSandboxUnavailable,
			strings.TrimSpace(string(output)),
		)
	}
	return nil
}

type sandboxBinary struct {
	hostPath    string
	sandboxPath string
}

func (sandbox *linuxSandbox) requiredBinaries(plan Plan) ([]sandboxBinary, error) {
	requested := map[string]sandboxBinary{
		"bubblewrap": {hostPath: sandbox.bubblewrap, sandboxPath: "bubblewrap"},
	}
	for _, step := range plan.Steps {
		name := step.Shell
		if len(step.Argv) > 0 {
			name = step.Argv[0]
		}
		if name == "" {
			continue
		}
		if strings.ContainsRune(name, '/') {
			return nil, fmt.Errorf(
				"%w: command path %q is not a trusted toolchain executable",
				ErrEnvironmentUnavailable,
				name,
			)
		}
		binary, found := sandbox.resolveBinary(name)
		if !found {
			return nil, fmt.Errorf("%w: required executable %q is unavailable", ErrEnvironmentUnavailable, name)
		}
		requested[binary.sandboxPath] = binary
	}
	paths := make([]sandboxBinary, 0, len(requested))
	for _, binary := range requested {
		paths = append(paths, binary)
	}
	slices.SortFunc(paths, func(left, right sandboxBinary) int {
		return strings.Compare(left.sandboxPath, right.sandboxPath)
	})
	return paths, nil
}

func (sandbox *linuxSandbox) resolveBinary(name string) (sandboxBinary, bool) {
	for _, mount := range sandbox.mounts {
		if mount.Target != "/dependencies/bin" {
			continue
		}
		candidate := filepath.Join(mount.Source, name)
		info, err := os.Lstat(candidate)
		if err == nil && info.Mode().IsRegular() && info.Mode()&0o111 != 0 {
			return sandboxBinary{hostPath: candidate, sandboxPath: "/dependencies/bin/" + name}, true
		}
	}
	for _, directory := range strings.Split(hostSandboxPath, ":") {
		candidate := filepath.Join(directory, name)
		info, err := os.Stat(candidate)
		if err == nil && info.Mode().IsRegular() && info.Mode()&0o111 != 0 {
			return sandboxBinary{hostPath: candidate, sandboxPath: candidate}, true
		}
	}
	return sandboxBinary{}, false
}

func digestRegularFile(path string, limit int64) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() > limit {
		return "", errors.New("file is not a bounded regular file")
	}
	hash := sha256.New()
	written, err := io.Copy(hash, io.LimitReader(file, limit+1))
	if err != nil || written > limit {
		return "", errors.New("file exceeds fingerprint limit")
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func (sandbox *linuxSandbox) RunStep(
	ctx context.Context,
	candidateRoot string,
	step Step,
	limits Limits,
) (StepResult, error) {
	started := time.Now()
	result := StepResult{StepID: step.ID, ExitCode: -1}
	if sandbox == nil {
		result.Status = StatusInfrastructureError
		return result, ErrSandboxUnavailable
	}
	if ctx == nil {
		ctx = context.Background()
	}
	normalized, err := normalizeStep(step)
	if err != nil {
		result.Status = StatusInfrastructureError
		return result, err
	}
	limits = normalizeLimits(limits)
	timeout := time.Duration(normalized.TimeoutSeconds) * time.Second
	if timeout > limits.StepTimeout {
		timeout = limits.StepTimeout
	}
	stepCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	executionRoot, err := os.MkdirTemp(sandbox.temporaryRoot, ".local-ci-step-")
	if err != nil {
		result.Status = StatusInfrastructureError
		return result, fmt.Errorf("create local CI step root: %w", err)
	}
	cleanup := func() error { return os.RemoveAll(executionRoot) }
	workRoot := filepath.Join(executionRoot, "workspace")
	if err = copySandboxTree(candidateRoot, workRoot); err != nil {
		_ = cleanup()
		result.Status = StatusInfrastructureError
		return result, err
	}
	if err = validateSandboxWorkingDirectory(workRoot, normalized.WorkingDirectory); err != nil {
		_ = cleanup()
		result.Status = StatusInfrastructureError
		return result, err
	}
	arguments, err := sandbox.bubblewrapArguments(executionRoot, workRoot, normalized)
	if err != nil {
		_ = cleanup()
		result.Status = StatusInfrastructureError
		return result, err
	}
	unit, err := localCIUnitName("step")
	if err != nil {
		_ = cleanup()
		result.Status = StatusInfrastructureError
		return result, fmt.Errorf("create local CI resource scope: %w", err)
	}
	serviceWrapper := filepath.Join(executionRoot, "service-wrapper")
	startMarker := filepath.Join(executionRoot, "service-start")
	doneMarker := filepath.Join(executionRoot, "service-done")
	releaseMarker := filepath.Join(executionRoot, "service-release")
	wrapper := `#!/bin/sh
set -u
start=$1
done=$2
release=$3
shift 3
while [ ! -f "$start" ]; do /bin/sleep 0.01; done
set +e
"$@"
status=$?
umask 077
: > "$done"
while [ ! -f "$release" ]; do /bin/sleep 0.01; done
exit "$status"
`
	if err = os.WriteFile(serviceWrapper, []byte(wrapper), 0o500); err != nil {
		_ = cleanup()
		result.Status = StatusInfrastructureError
		return result, fmt.Errorf("write local CI service wrapper: %w", err)
	}
	serviceArguments := make([]string, 0, 5+len(arguments))
	serviceArguments = append(
		serviceArguments,
		serviceWrapper,
		startMarker,
		doneMarker,
		releaseMarker,
		sandbox.bubblewrap,
	)
	serviceArguments = append(serviceArguments, arguments...)
	command := sandbox.resources.command(stepCtx, unit, timeout, "/bin/sh", serviceArguments...)
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	output := &boundedOutput{
		limit:    limits.OutputBytes,
		overflow: make(chan struct{}),
	}
	command.Stdout = output
	command.Stderr = output
	if err = command.Start(); err != nil {
		_ = cleanup()
		result.Status = StatusInfrastructureError
		return result, fmt.Errorf("start local CI sandbox: %w", err)
	}
	resourceSnapshot, prepareErr := sandbox.resources.PrepareRunning(stepCtx, unit)
	if prepareErr != nil {
		containmentErr := sandbox.resources.Terminate(unit)
		killProcessGroup(command)
		_ = command.Wait()
		if containmentErr == nil || errors.Is(containmentErr, errLocalCIResourceLimit) {
			_ = cleanup()
		}
		result.Status = StatusInfrastructureError
		return result, errors.Join(
			fmt.Errorf("prepare local CI resource inspection: %w", prepareErr),
			containmentErr,
		)
	}
	if err = writeSandboxMarker(startMarker); err != nil {
		containmentErr := sandbox.resources.Terminate(unit)
		killProcessGroup(command)
		_ = command.Wait()
		if containmentErr == nil || errors.Is(containmentErr, errLocalCIResourceLimit) {
			_ = cleanup()
		}
		result.Status = StatusInfrastructureError
		return result, errors.Join(fmt.Errorf("start local CI service: %w", err), containmentErr)
	}
	processDone := make(chan error, 1)
	go func() { processDone <- command.Wait() }()
	serviceDone := make(chan error, 1)
	go func() { serviceDone <- waitSandboxMarker(stepCtx, doneMarker) }()
	var runErr error
	var containmentErr error
	select {
	case markerErr := <-serviceDone:
		if markerErr != nil {
			containmentErr = markerErr
			containmentErr = errors.Join(containmentErr, sandbox.resources.Terminate(unit))
			killProcessGroup(command)
			runErr = <-processDone
			break
		}
		containmentErr = sandbox.resources.InspectRunning(unit, resourceSnapshot)
		if releaseErr := writeSandboxMarker(releaseMarker); releaseErr != nil {
			containmentErr = errors.Join(containmentErr, releaseErr, sandbox.resources.Terminate(unit))
			killProcessGroup(command)
		}
		runErr = <-processDone
	case runErr = <-processDone:
		containmentErr = errors.New("local CI service exited before resource inspection")
	case <-output.overflow:
		containmentErr = errors.Join(
			sandbox.resources.InspectRunning(unit, resourceSnapshot),
			sandbox.resources.Terminate(unit),
		)
		killProcessGroup(command)
		runErr = <-processDone
		result.Status = StatusOutputLimitExceeded
	case <-stepCtx.Done():
		containmentErr = errors.Join(
			sandbox.resources.InspectRunning(unit, resourceSnapshot),
			sandbox.resources.Terminate(unit),
		)
		killProcessGroup(command)
		runErr = <-processDone
		if errors.Is(stepCtx.Err(), context.DeadlineExceeded) {
			result.Status = StatusTimedOut
		} else {
			result.Status = StatusCanceled
		}
	}
	finalizeErr := sandbox.resources.Finalize(unit)
	containmentErr = errors.Join(containmentErr, finalizeErr)
	rawOutput, truncated := output.snapshot()
	result.Output = strings.ToValidUTF8(string(rawOutput), "�")
	result.OutputDigest = digestParts("picoclaw-local-ci-output-v1", []byte(result.Output))
	result.ObservedOutputBytes = int64(len(result.Output))
	if result.ObservedOutputBytes > int64(limits.OutputBytes) {
		truncated = true
	}
	result.OutputTruncated = truncated
	result.DurationMillis = time.Since(started).Milliseconds()
	if truncated {
		result.Status = StatusOutputLimitExceeded
		result.ExitCode = processExitCode(runErr)
	} else if result.Status == "" {
		result.Status, result.ExitCode = classifySandboxExit(runErr, rawOutput)
	} else {
		result.ExitCode = processExitCode(runErr)
	}
	quiesced := finalizeErr == nil || errors.Is(finalizeErr, errLocalCIResourceLimit) ||
		errors.Is(finalizeErr, errLocalCIQuiesced)
	if quiesced {
		if cleanupErr := cleanup(); cleanupErr != nil {
			result.Status = StatusInfrastructureError
			return result, fmt.Errorf("remove local CI step root: %w", cleanupErr)
		}
	} else {
		result.Status = StatusInfrastructureError
		return result, fmt.Errorf(
			"%w: local CI scratch quarantined because cgroup quiescence was not proven",
			containmentErr,
		)
	}
	if containmentErr != nil {
		result.Status = StatusInfrastructureError
		return result, containmentErr
	}
	return result, nil
}

func writeSandboxMarker(name string) error {
	file, err := os.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	return file.Close()
}

func waitSandboxMarker(ctx context.Context, name string) error {
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		info, err := os.Lstat(name)
		if err == nil {
			if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Size() != 0 {
				return errors.New("local CI service marker is invalid")
			}
			return nil
		}
		if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

func (sandbox *linuxSandbox) bubblewrapArguments(
	executionRoot, workRoot string,
	step Step,
) ([]string, error) {
	arguments := []string{
		"--die-with-parent",
		"--new-session",
		"--unshare-all",
		"--clearenv",
		"--cap-drop", "ALL",
		"--proc", "/proc",
		"--dev", "/dev",
		"--size", "536870912", "--tmpfs", "/tmp",
		"--dir", "/home",
		"--size", "134217728", "--tmpfs", "/home/picoclaw",
		"--size", "16777216", "--tmpfs", "/run",
		"--size", "1073741824", "--tmpfs", "/cache",
		"--size", "1073741824", "--tmpfs", "/workspace",
		"--dir", "/dependencies",
		"--ro-bind", workRoot, "/source",
		"--setenv", "HOME", "/home/picoclaw",
		"--setenv", "TMPDIR", "/tmp",
		"--setenv", "XDG_CONFIG_HOME", "/home/picoclaw/.config",
		"--setenv", "XDG_CACHE_HOME", "/cache",
		"--setenv", "GOCACHE", "/cache/go-build",
		"--setenv", "npm_config_cache", "/cache/npm",
		"--setenv", "PATH", sandbox.sandboxPath(),
		"--setenv", "LANG", "C.UTF-8",
		"--setenv", "LC_ALL", "C.UTF-8",
		"--setenv", "TZ", "UTC",
		"--setenv", "CI", "true",
		"--setenv", "NO_COLOR", "1",
		"--setenv", "GIT_CONFIG_NOSYSTEM", "1",
		"--setenv", "GIT_TERMINAL_PROMPT", "0",
		"--setenv", "PICOCLAW_LOCAL_CI", "1",
	}
	for _, systemPath := range linuxSandboxSystemPaths() {
		arguments = append(arguments, "--ro-bind", systemPath, systemPath)
	}
	for _, mount := range sandbox.mounts {
		arguments = append(arguments, "--ro-bind", mount.Source, mount.Target)
	}
	for _, variable := range step.Environment {
		arguments = append(arguments, "--setenv", variable.Name, variable.Value)
	}
	workingDirectory := "/workspace"
	if step.WorkingDirectory != "" {
		workingDirectory += "/" + step.WorkingDirectory
	}
	entryPath := filepath.Join(executionRoot, "sandbox-entry")
	entry := "#!/bin/sh\nset -eu\n/bin/cp -a /source/. /workspace/\ncd \"$1\"\nshift\nexec \"$@\"\n"
	if err := os.WriteFile(entryPath, []byte(entry), 0o500); err != nil {
		return nil, fmt.Errorf("write local CI sandbox entry: %w", err)
	}
	arguments = append(arguments, "--ro-bind", entryPath, "/run/picoclaw-entry", "--")
	command := []string{"/bin/sh", "/run/picoclaw-entry", workingDirectory}
	if step.Script != "" {
		scriptPath := filepath.Join(executionRoot, "step-script")
		if err := os.WriteFile(scriptPath, []byte(step.Script), 0o500); err != nil {
			return nil, fmt.Errorf("write local CI sandbox script: %w", err)
		}
		arguments = append(arguments[:len(arguments)-1], "--ro-bind", scriptPath, "/run/picoclaw-step", "--")
		if step.Shell == "bash" {
			command = append(command, "/bin/bash", "--noprofile", "--norc", "-euo", "pipefail", "/run/picoclaw-step")
		} else {
			command = append(command, "/bin/sh", "-eu", "/run/picoclaw-step")
		}
		return append(arguments, command...), nil
	}
	command = append(command, step.Argv...)
	return append(arguments, command...), nil
}

func linuxSandboxSystemPaths() []string {
	candidates := []string{
		"/usr", "/bin", "/sbin", "/lib", "/lib64",
		"/etc/alternatives", "/etc/ld.so.cache", "/etc/ld.so.conf", "/etc/ld.so.conf.d",
		"/etc/passwd", "/etc/group", "/etc/localtime", "/usr/share/zoneinfo",
	}
	paths := make([]string, 0, len(candidates))
	for _, candidate := range candidates {
		if _, err := os.Stat(candidate); err == nil {
			paths = append(paths, candidate)
		}
	}
	return paths
}

func validateSandboxWorkingDirectory(root, relative string) error {
	target := root
	if relative != "" {
		target = filepath.Join(root, filepath.FromSlash(relative))
	}
	resolved, err := filepath.EvalSymlinks(target)
	if err != nil {
		return fmt.Errorf("%w: sandbox working directory is unavailable", ErrInvalid)
	}
	relativeToRoot, err := filepath.Rel(root, resolved)
	if err != nil || !filepath.IsLocal(relativeToRoot) {
		return fmt.Errorf("%w: sandbox working directory escaped candidate", ErrInvalid)
	}
	info, err := os.Stat(resolved)
	if err != nil || !info.IsDir() {
		return fmt.Errorf("%w: sandbox working directory is not a directory", ErrInvalid)
	}
	return nil
}

func copySandboxTree(source, destination string) error {
	if err := os.Mkdir(destination, 0o700); err != nil {
		return fmt.Errorf("create sandbox workspace: %w", err)
	}
	entries := 0
	var total int64
	err := filepath.WalkDir(source, func(current string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if current == source {
			return nil
		}
		entries++
		if entries > maximumSandboxFiles {
			return errors.New("sandbox candidate file limit exceeded")
		}
		relative, err := filepath.Rel(source, current)
		if err != nil || !filepath.IsLocal(relative) {
			return errors.New("sandbox candidate path escaped root")
		}
		for _, segment := range strings.Split(filepath.ToSlash(relative), "/") {
			if strings.EqualFold(segment, ".git") {
				return errors.New("sandbox candidate contains a Git control path")
			}
		}
		target := filepath.Join(destination, relative)
		info, err := os.Lstat(current)
		if err != nil {
			return err
		}
		switch {
		case info.IsDir():
			return os.Mkdir(target, 0o700)
		case info.Mode().IsRegular():
			total += info.Size()
			if total > maximumSandboxBytes {
				return errors.New("sandbox candidate byte limit exceeded")
			}
			return copySandboxFile(current, target, info)
		case info.Mode()&os.ModeSymlink != 0:
			link, readErr := os.Readlink(current)
			if readErr != nil || filepath.IsAbs(link) ||
				!filepath.IsLocal(filepath.Join(filepath.Dir(relative), link)) {
				return errors.New("sandbox candidate contains an unsafe symlink")
			}
			return os.Symlink(link, target)
		default:
			return errors.New("sandbox candidate contains a special file")
		}
	})
	if err != nil {
		return fmt.Errorf("copy local CI candidate: %w", err)
	}
	return nil
}

func copySandboxFile(source, destination string, expected os.FileInfo) error {
	descriptor, err := unix.Open(
		source,
		unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_NONBLOCK,
		0,
	)
	if err != nil {
		return err
	}
	input := os.NewFile(uintptr(descriptor), source)
	if input == nil {
		_ = unix.Close(descriptor)
		return errors.New("open sandbox source file")
	}
	defer input.Close()
	opened, err := input.Stat()
	if err != nil || !opened.Mode().IsRegular() || !os.SameFile(expected, opened) ||
		expected.Size() != opened.Size() || expected.Mode() != opened.Mode() {
		return errors.New("sandbox source file changed while opening")
	}
	permissions := os.FileMode(0o600)
	if expected.Mode()&0o111 != 0 {
		permissions = 0o700
	}
	output, err := os.OpenFile(destination, os.O_WRONLY|os.O_CREATE|os.O_EXCL, permissions)
	if err != nil {
		return err
	}
	written, copyErr := io.Copy(output, io.LimitReader(input, expected.Size()+1))
	if copyErr == nil && written != expected.Size() {
		copyErr = errors.New("sandbox source file changed while copying")
	}
	after, statErr := input.Stat()
	if statErr != nil || after.Size() != expected.Size() || after.Mode() != expected.Mode() ||
		!os.SameFile(expected, after) {
		copyErr = errors.Join(copyErr, errors.New("sandbox source file changed after copying"), statErr)
	}
	closeErr := output.Close()
	return errors.Join(copyErr, closeErr)
}

func (output *boundedOutput) Write(data []byte) (int, error) {
	output.mu.Lock()
	defer output.mu.Unlock()
	remaining := output.limit - len(output.data)
	if remaining > 0 {
		copied := min(remaining, len(data))
		output.data = append(output.data, data[:copied]...)
	}
	if len(data) > remaining {
		output.once.Do(func() { close(output.overflow) })
	}
	return len(data), nil
}

func (output *boundedOutput) snapshot() ([]byte, bool) {
	output.mu.Lock()
	defer output.mu.Unlock()
	data := append([]byte(nil), output.data...)
	select {
	case <-output.overflow:
		return data, true
	default:
		return data, false
	}
}

func killProcessGroup(command *exec.Cmd) {
	if command == nil || command.Process == nil {
		return
	}
	_ = syscall.Kill(-command.Process.Pid, syscall.SIGKILL)
	_ = command.Process.Kill()
}

func (sandbox *linuxSandbox) sandboxPath() string {
	for _, mount := range sandbox.mounts {
		if mount.Target == "/dependencies/bin" {
			return "/dependencies/bin:" + hostSandboxPath
		}
	}
	return hostSandboxPath
}

func classifySandboxExit(err error, output []byte) (Status, int) {
	if err == nil {
		return StatusPassed, 0
	}
	exitCode := processExitCode(err)
	if exitCode == 1 && strings.Contains(string(output), "bwrap:") {
		return StatusInfrastructureError, exitCode
	}
	if exitCode == 125 || exitCode == 126 || exitCode == 127 {
		return StatusEnvironmentUnavailable, exitCode
	}
	return StatusFailed, exitCode
}

func processExitCode(err error) int {
	var exitError *exec.ExitError
	if errors.As(err, &exitError) {
		return exitError.ExitCode()
	}
	if err == nil {
		return 0
	}
	return -1
}
