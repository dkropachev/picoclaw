//go:build linux

package localci

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const (
	localCIMemoryMax = int64(4 << 30)
	localCITasksMax  = int64(128)
)

var (
	errLocalCIResourceLimit = errors.New("local CI cgroup resource limit was reached")
	errLocalCIQuiesced      = errors.New("local CI cgroup quiescence was recovered")
)

type systemdResourceController struct {
	runPath     string
	controlPath string
	environment []string
}

type localCIResourceSnapshot struct {
	controlGroup string
	groups       []localCIResourceGroupSnapshot
}

type localCIResourceGroupSnapshot struct {
	path         string
	memory       map[string]uint64
	pids         map[string]uint64
	trackMemory  bool
	trackProcess bool
}

func newSystemdResourceController(runPath, controlPath string) (*systemdResourceController, error) {
	resolvedRun, err := resolveControllerExecutable(runPath, "systemd-run")
	if err != nil {
		return nil, err
	}
	resolvedControl, err := resolveControllerExecutable(controlPath, "systemctl")
	if err != nil {
		return nil, err
	}
	runtimeDirectory := os.Getenv("XDG_RUNTIME_DIR")
	wantRuntime := filepath.Join("/run/user", strconv.Itoa(os.Getuid()))
	resolvedRuntime, resolveErr := filepath.EvalSymlinks(runtimeDirectory)
	busAddress := os.Getenv("DBUS_SESSION_BUS_ADDRESS")
	if runtimeDirectory != wantRuntime || resolveErr != nil || resolvedRuntime != runtimeDirectory ||
		busAddress != "unix:path="+filepath.Join(runtimeDirectory, "bus") {
		return nil, fmt.Errorf("%w: a canonical local systemd user bus is required", ErrSandboxUnavailable)
	}
	busInfo, err := os.Lstat(filepath.Join(runtimeDirectory, "bus"))
	if err != nil || busInfo.Mode()&os.ModeSocket == 0 {
		return nil, fmt.Errorf("%w: systemd user bus is unavailable", ErrSandboxUnavailable)
	}
	controller := &systemdResourceController{
		runPath:     resolvedRun,
		controlPath: resolvedControl,
		environment: []string{
			"PATH=/usr/bin:/bin",
			"XDG_RUNTIME_DIR=" + runtimeDirectory,
			"DBUS_SESSION_BUS_ADDRESS=" + busAddress,
		},
	}
	probeCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err = controller.Probe(probeCtx); err != nil {
		return nil, err
	}
	return controller, nil
}

func resolveControllerExecutable(configured, fallback string) (string, error) {
	value := strings.TrimSpace(configured)
	var err error
	if value == "" {
		value, err = exec.LookPath(fallback)
		if err != nil {
			return "", fmt.Errorf("%w: %s was not found", ErrSandboxUnavailable, fallback)
		}
	}
	value, err = filepath.Abs(value)
	if err != nil {
		return "", fmt.Errorf("%w: resolve %s: %v", ErrSandboxUnavailable, fallback, err)
	}
	resolved, err := filepath.EvalSymlinks(value)
	if err != nil {
		return "", fmt.Errorf("%w: resolve %s: %v", ErrSandboxUnavailable, fallback, err)
	}
	info, err := os.Stat(resolved)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&0o111 == 0 {
		return "", fmt.Errorf("%w: %s is not an executable regular file", ErrSandboxUnavailable, fallback)
	}
	return resolved, nil
}

func (controller *systemdResourceController) Probe(ctx context.Context) error {
	if controller == nil {
		return ErrSandboxUnavailable
	}
	unit, err := localCIUnitName("probe")
	if err != nil {
		return err
	}
	probe := `line=$(cat /proc/self/cgroup)
group=${line#*:*:}
cat "/sys/fs/cgroup${group}/memory.max"
cat "/sys/fs/cgroup${group}/memory.swap.max"
cat "/sys/fs/cgroup${group}/pids.max"
`
	command := controller.command(ctx, unit, 10*time.Second, "/bin/sh", "-eu", "-c", probe)
	var stderr bytes.Buffer
	command.Stderr = &stderr
	output, err := command.Output()
	if err != nil || string(output) != "67108864\n0\n32\n" {
		detail := stderr.Bytes()
		if len(detail) == 0 {
			detail = output
		}
		if len(detail) > 4<<10 {
			detail = detail[:4<<10]
		}
		return fmt.Errorf(
			"%w: delegated cgroup-v2 memory and pids controls are required: %s",
			ErrSandboxUnavailable,
			strings.TrimSpace(string(detail)),
		)
	}
	return nil
}

func (controller *systemdResourceController) command(
	ctx context.Context,
	unit string,
	timeout time.Duration,
	executable string,
	arguments ...string,
) *exec.Cmd {
	memoryMax := localCIMemoryMax
	tasksMax := localCITasksMax
	if strings.Contains(unit, "-probe-") {
		memoryMax = 64 << 20
		tasksMax = 32
	}
	runtimeSeconds := max(int64(1), int64((timeout+time.Second-1)/time.Second)+5)
	options := make([]string, 0, 32+len(arguments))
	options = append(options,
		"--user", "--quiet", "--wait", "--pipe", "--service-type=exec",
		"--expand-environment=no",
		"--unit", unit,
		"--property=MemoryAccounting=yes",
		"--property=MemoryMax="+strconv.FormatInt(memoryMax, 10),
		"--property=MemorySwapMax=0",
		"--property=OOMPolicy=kill",
		"--property=TasksAccounting=yes",
		"--property=TasksMax="+strconv.FormatInt(tasksMax, 10),
		"--property=CPUQuota=200%",
		"--property=RuntimeMaxSec="+strconv.FormatInt(runtimeSeconds, 10),
		"--property=LimitNOFILE=256",
		"--property=LimitFSIZE=1073741824",
		"--property=LimitAS=4294967296",
		"--property=LimitCORE=0",
		"--property=KillMode=control-group",
		"--property=TimeoutStopSec=5s",
		"--",
		executable,
	)
	options = append(options, arguments...)
	command := exec.CommandContext(ctx, controller.runPath, options...)
	command.Env = append([]string(nil), controller.environment...)
	return command
}

func (controller *systemdResourceController) Terminate(unit string) error {
	if controller == nil || unit == "" {
		return ErrSandboxUnavailable
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var resourceErr error
	if state, stateErr := controller.readUnitState(ctx, unit); stateErr == nil &&
		state.LoadState == "loaded" && state.ControlGroup != "" {
		resourceErr = inspectLocalCICgroup(state.ControlGroup, -1)
	}
	kill := exec.CommandContext(
		ctx,
		controller.controlPath,
		"--user", "kill", "--kill-whom=all", "--signal=SIGKILL", unit,
	)
	kill.Env = append([]string(nil), controller.environment...)
	killErr := kill.Run()
	stop := exec.CommandContext(ctx, controller.controlPath, "--user", "stop", unit)
	stop.Env = append([]string(nil), controller.environment...)
	stopErr := stop.Run()
	verifyErr := controller.waitUnitTerminal(ctx, unit)
	if verifyErr == nil {
		// kill/stop commonly report that an already collected unit is absent.
		// A conclusive terminal-state check is the authority in that case.
		return resourceErr
	}
	return fmt.Errorf(
		"terminate local CI cgroup: %w",
		errors.Join(ctx.Err(), resourceErr, killErr, stopErr, verifyErr),
	)
}

// PrepareRunning waits until systemd has placed the blocked trusted wrapper in
// its exact cgroup, then snapshots every finite ancestor resource counter. The
// wrapper is released only after this returns, so a shared parent limit hit by
// concurrent validations cannot be mistaken for a successful child step.
func (controller *systemdResourceController) PrepareRunning(
	ctx context.Context,
	unit string,
) (localCIResourceSnapshot, error) {
	if controller == nil || unit == "" {
		return localCIResourceSnapshot{}, ErrSandboxUnavailable
	}
	if ctx == nil {
		ctx = context.Background()
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	var lastErr error
	for {
		state, err := controller.readUnitState(ctx, unit)
		if err == nil && state.LoadState == "loaded" && state.ActiveState == "active" &&
			state.ControlGroup != "" {
			snapshot, snapshotErr := captureLocalCIResourceSnapshot(state.ControlGroup)
			if snapshotErr == nil {
				return snapshot, nil
			}
			lastErr = snapshotErr
		} else if err != nil {
			lastErr = err
		} else {
			lastErr = fmt.Errorf(
				"unit state is %s/%s/%s",
				state.LoadState,
				state.ActiveState,
				state.SubState,
			)
		}
		select {
		case <-ctx.Done():
			return localCIResourceSnapshot{}, errors.Join(ctx.Err(), lastErr)
		case <-ticker.C:
		}
	}
}

// InspectRunning proves the trusted wrapper is still live and checks both its
// leaf and every finite ancestor for resource counter advances before release.
func (controller *systemdResourceController) InspectRunning(
	unit string,
	snapshot localCIResourceSnapshot,
) error {
	if controller == nil || unit == "" || snapshot.controlGroup == "" {
		return ErrSandboxUnavailable
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	state, err := controller.readUnitState(ctx, unit)
	if err != nil {
		return fmt.Errorf("inspect running local CI cgroup: %w", err)
	}
	if state.LoadState != "loaded" || state.ActiveState != "active" ||
		state.ControlGroup != snapshot.controlGroup {
		return fmt.Errorf(
			"running local CI cgroup has unexpected state %s/%s/%s",
			state.LoadState,
			state.ActiveState,
			state.SubState,
		)
	}
	if err = inspectLocalCICgroup(state.ControlGroup, 1); err != nil {
		return err
	}
	return verifyLocalCIResourceSnapshot(snapshot)
}

// Finalize proves that a transient unit is terminal after its systemd-run
// client exits. If the service unexpectedly outlived the client it is killed,
// but the result remains an infrastructure failure rather than becoming green.
func (controller *systemdResourceController) Finalize(unit string) error {
	if controller == nil || unit == "" {
		return ErrSandboxUnavailable
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	state, err := controller.readUnitState(ctx, unit)
	if err != nil {
		terminateErr := controller.Terminate(unit)
		if terminateErr == nil || errors.Is(terminateErr, errLocalCIResourceLimit) {
			return errors.Join(
				errLocalCIQuiesced,
				fmt.Errorf("inspect local CI cgroup: %w", err),
				terminateErr,
			)
		}
		return errors.Join(fmt.Errorf("inspect local CI cgroup: %w", err), terminateErr)
	}
	if state.terminal() {
		return controller.verifyTerminalUnit(state)
	}
	terminateErr := controller.Terminate(unit)
	if terminateErr == nil || errors.Is(terminateErr, errLocalCIResourceLimit) {
		return errors.Join(
			errLocalCIQuiesced,
			errors.New("local CI service outlived its systemd-run client"),
			terminateErr,
		)
	}
	return errors.Join(
		errors.New("local CI service outlived its systemd-run client"),
		terminateErr,
	)
}

type localCIUnitState struct {
	LoadState    string
	ActiveState  string
	SubState     string
	ControlGroup string
	Result       string
}

func (state localCIUnitState) terminal() bool {
	if state.LoadState == "not-found" {
		return state.ActiveState == "inactive"
	}
	return state.LoadState == "loaded" &&
		(state.ActiveState == "inactive" || state.ActiveState == "failed")
}

func (controller *systemdResourceController) waitUnitTerminal(
	ctx context.Context,
	unit string,
) error {
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	var lastErr error
	for {
		state, err := controller.readUnitState(ctx, unit)
		if err == nil && state.terminal() {
			return controller.verifyTerminalUnit(state)
		}
		if err != nil {
			lastErr = err
		} else {
			lastErr = fmt.Errorf(
				"unit state is %s/%s/%s",
				state.LoadState,
				state.ActiveState,
				state.SubState,
			)
		}
		select {
		case <-ctx.Done():
			return errors.Join(ctx.Err(), lastErr)
		case <-ticker.C:
		}
	}
}

func (controller *systemdResourceController) readUnitState(
	ctx context.Context,
	unit string,
) (localCIUnitState, error) {
	command := exec.CommandContext(
		ctx,
		controller.controlPath,
		"--user", "show", "--no-pager",
		"--property=LoadState",
		"--property=ActiveState",
		"--property=SubState",
		"--property=ControlGroup",
		"--property=Result",
		unit,
	)
	command.Env = append([]string(nil), controller.environment...)
	output, err := command.Output()
	if err != nil {
		return localCIUnitState{}, err
	}
	if len(output) > 16<<10 {
		return localCIUnitState{}, errors.New("systemd unit state output exceeded its bound")
	}
	values := make(map[string]string, 5)
	for _, line := range strings.Split(strings.TrimSpace(string(output)), "\n") {
		key, value, found := strings.Cut(line, "=")
		if !found {
			continue
		}
		if _, duplicate := values[key]; duplicate {
			return localCIUnitState{}, errors.New("systemd unit state contains duplicate properties")
		}
		values[key] = value
	}
	state := localCIUnitState{
		LoadState:    values["LoadState"],
		ActiveState:  values["ActiveState"],
		SubState:     values["SubState"],
		ControlGroup: values["ControlGroup"],
		Result:       values["Result"],
	}
	if state.LoadState == "" || state.ActiveState == "" || state.SubState == "" {
		return localCIUnitState{}, errors.New("systemd unit state is incomplete")
	}
	return state, nil
}

func (controller *systemdResourceController) verifyTerminalUnit(state localCIUnitState) error {
	if !state.terminal() {
		return errors.New("local CI systemd unit is not terminal")
	}
	if state.LoadState == "not-found" {
		return nil
	}
	if state.Result == "oom-kill" || state.Result == "resources" {
		return errLocalCIResourceLimit
	}
	if state.ControlGroup == "" {
		return nil
	}
	return inspectLocalCICgroup(state.ControlGroup, 0)
}

func inspectLocalCICgroup(controlGroup string, wantPopulated int) error {
	group, err := localCICgroupPath(controlGroup)
	if err != nil {
		return err
	}
	events, err := readCgroupCounters(filepath.Join(group, "cgroup.events"))
	if errors.Is(err, os.ErrNotExist) {
		if wantPopulated > 0 {
			return errors.New("running local CI cgroup disappeared before inspection")
		}
		return nil
	}
	if err != nil {
		return err
	}
	if wantPopulated >= 0 && events["populated"] != uint64(wantPopulated) {
		return fmt.Errorf(
			"local CI cgroup populated=%d, want %d",
			events["populated"],
			wantPopulated,
		)
	}
	memory, err := readCgroupCounters(filepath.Join(group, "memory.events"))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	pids, pidsErr := readCgroupCounters(filepath.Join(group, "pids.events"))
	if pidsErr != nil && !errors.Is(pidsErr, os.ErrNotExist) {
		return pidsErr
	}
	if localCIResourceLimitReached(memory, pids) {
		return errLocalCIResourceLimit
	}
	return nil
}

func captureLocalCIResourceSnapshot(controlGroup string) (localCIResourceSnapshot, error) {
	group, err := localCICgroupPath(controlGroup)
	if err != nil {
		return localCIResourceSnapshot{}, err
	}
	root := filepath.Clean("/sys/fs/cgroup")
	snapshot := localCIResourceSnapshot{controlGroup: controlGroup}
	for current := group; current != root; current = filepath.Dir(current) {
		memoryFinite, memoryErr := finiteCgroupLimit(filepath.Join(current, "memory.max"))
		if memoryErr != nil {
			return localCIResourceSnapshot{}, memoryErr
		}
		pidsFinite, pidsErr := finiteCgroupLimit(filepath.Join(current, "pids.max"))
		if pidsErr != nil {
			return localCIResourceSnapshot{}, pidsErr
		}
		entry := localCIResourceGroupSnapshot{
			path:         current,
			trackMemory:  memoryFinite,
			trackProcess: pidsFinite,
		}
		if memoryFinite {
			entry.memory, err = readCgroupCounters(filepath.Join(current, "memory.events"))
			if err != nil {
				return localCIResourceSnapshot{}, err
			}
		}
		if pidsFinite {
			entry.pids, err = readCgroupCounters(filepath.Join(current, "pids.events"))
			if err != nil {
				return localCIResourceSnapshot{}, err
			}
		}
		if memoryFinite || pidsFinite {
			snapshot.groups = append(snapshot.groups, entry)
		}
	}
	if len(snapshot.groups) == 0 {
		return localCIResourceSnapshot{}, errors.New("local CI cgroup has no finite resource policy")
	}
	return snapshot, nil
}

func verifyLocalCIResourceSnapshot(snapshot localCIResourceSnapshot) error {
	for _, group := range snapshot.groups {
		if group.trackMemory {
			current, err := readCgroupCounters(filepath.Join(group.path, "memory.events"))
			if err != nil {
				return err
			}
			if resourceCounterAdvanced(group.memory, current, "max", "oom", "oom_kill", "oom_group_kill") {
				return errLocalCIResourceLimit
			}
		}
		if group.trackProcess {
			current, err := readCgroupCounters(filepath.Join(group.path, "pids.events"))
			if err != nil {
				return err
			}
			if resourceCounterAdvanced(group.pids, current, "max") {
				return errLocalCIResourceLimit
			}
		}
	}
	return nil
}

func resourceCounterAdvanced(before, after map[string]uint64, names ...string) bool {
	for _, name := range names {
		if after[name] > before[name] {
			return true
		}
	}
	return false
}

func finiteCgroupLimit(name string) (bool, error) {
	raw, err := os.ReadFile(name)
	if err != nil {
		return false, err
	}
	if len(raw) > 128 {
		return false, errors.New("cgroup limit output exceeded its bound")
	}
	value := strings.TrimSpace(string(raw))
	if value == "max" {
		return false, nil
	}
	if _, err = strconv.ParseUint(value, 10, 64); err != nil {
		return false, errors.New("cgroup limit output is malformed")
	}
	return true, nil
}

func localCICgroupPath(controlGroup string) (string, error) {
	root := filepath.Clean("/sys/fs/cgroup")
	group := filepath.Clean(filepath.Join(root, filepath.FromSlash(strings.TrimPrefix(controlGroup, "/"))))
	if group == root || !strings.HasPrefix(group, root+string(filepath.Separator)) {
		return "", errors.New("systemd returned an invalid local CI cgroup path")
	}
	return group, nil
}

func localCIResourceLimitReached(memory, pids map[string]uint64) bool {
	return memory["max"] > 0 || memory["oom"] > 0 || memory["oom_kill"] > 0 ||
		memory["oom_group_kill"] > 0 || pids["max"] > 0
}

func readCgroupCounters(name string) (map[string]uint64, error) {
	raw, err := os.ReadFile(name)
	if err != nil {
		return nil, err
	}
	if len(raw) > 16<<10 {
		return nil, errors.New("cgroup event output exceeded its bound")
	}
	values := make(map[string]uint64)
	for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 {
			return nil, errors.New("cgroup event output is malformed")
		}
		value, parseErr := strconv.ParseUint(fields[1], 10, 64)
		if parseErr != nil {
			return nil, errors.New("cgroup event counter is malformed")
		}
		values[fields[0]] = value
	}
	return values, nil
}

func (controller *systemdResourceController) Identity(ctx context.Context) (string, error) {
	if err := controller.Probe(ctx); err != nil {
		return "", err
	}
	runDigest, err := digestRegularFile(controller.runPath, 64<<20)
	if err != nil {
		return "", err
	}
	controlDigest, err := digestRegularFile(controller.controlPath, 64<<20)
	if err != nil {
		return "", err
	}
	return digestParts(
		"picoclaw-local-ci-systemd-resource-controller-v1",
		[]byte(runDigest),
		[]byte(controlDigest),
		[]byte(strconv.FormatInt(localCIMemoryMax, 10)),
		[]byte(strconv.FormatInt(localCITasksMax, 10)),
	), nil
}

func localCIUnitName(kind string) (string, error) {
	var nonce [12]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return "", err
	}
	return "picoclaw-local-ci-" + kind + "-" + hex.EncodeToString(nonce[:]), nil
}
