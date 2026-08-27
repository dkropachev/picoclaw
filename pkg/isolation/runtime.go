package isolation

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"

	"github.com/sipeed/picoclaw/pkg"
	"github.com/sipeed/picoclaw/pkg/config"
)

// MountRule describes a source-to-target mount exposed inside the Linux
// isolation view.
type MountRule struct {
	Source string
	Target string
	Mode   string
}

// AccessRule describes the effective Windows-side access rule for a host path.
type AccessRule struct {
	Path string
	Mode string
}

// UserEnv contains the redirected per-instance user directories injected into
// isolated child processes.
type UserEnv struct {
	Home         string
	Tmp          string
	Config       string
	Cache        string
	State        string
	AppData      string
	LocalAppData string
}

// ResolveInstanceRoot resolves the instance root used to build the isolated
// filesystem and redirected user environment.
func ResolveInstanceRoot() (string, error) {
	root := filepath.Clean(config.GetHome())
	if root == "." || !filepath.IsAbs(root) {
		return "", fmt.Errorf("instance root must resolve to an absolute directory")
	}
	return root, nil
}

// PrepareInstanceRoot creates the directories required by the isolation runtime.
func PrepareInstanceRoot(root string) error {
	for _, dir := range InstanceDirs(root) {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("prepare instance dir %s: %w", dir, err)
		}
	}
	return nil
}

// InstanceDirs returns the directories that must exist under the instance root
// for isolation-aware child processes.
func InstanceDirs(root string) []string {
	dirs := []string{
		root,
		filepath.Join(root, "skills"),
		filepath.Join(root, "logs"),
		filepath.Join(root, "cache"),
		filepath.Join(root, "state"),
		filepath.Join(root, "runtime-user-env"),
		filepath.Join(root, "runtime-user-env", "home"),
		filepath.Join(root, "runtime-user-env", "tmp"),
		filepath.Join(root, "runtime-user-env", "config"),
		filepath.Join(root, "runtime-user-env", "cache"),
		filepath.Join(root, "runtime-user-env", "state"),
	}
	dirs = append(dirs, filepath.Join(root, pkg.WorkspaceName))
	if runtime.GOOS == "windows" {
		dirs = append(dirs,
			filepath.Join(root, "runtime-user-env", "AppData", "Roaming"),
			filepath.Join(root, "runtime-user-env", "AppData", "Local"),
		)
	}
	return dirs
}

// ResolveUserEnv derives the redirected user directories rooted under the
// instance runtime area.
func ResolveUserEnv(root string) UserEnv {
	base := filepath.Join(root, "runtime-user-env")
	return UserEnv{
		Home:         filepath.Join(base, "home"),
		Tmp:          filepath.Join(base, "tmp"),
		Config:       filepath.Join(base, "config"),
		Cache:        filepath.Join(base, "cache"),
		State:        filepath.Join(base, "state"),
		AppData:      filepath.Join(base, "AppData", "Roaming"),
		LocalAppData: filepath.Join(base, "AppData", "Local"),
	}
}

// ApplyUserEnv rewrites the child process environment so home, temp, and
// platform-specific user-data directories point into the instance root.
func ApplyUserEnv(cmd *exec.Cmd, root string) {
	if cmd == nil {
		return
	}
	cmd.Env = projectUserEnvironment(cmd.Environ(), ResolveUserEnv(root), runtime.GOOS)
}

type projectedEnvironmentValue struct {
	key   string
	value string
}

func projectUserEnvironment(base []string, userEnv UserEnv, goos string) []string {
	envMap := make(map[string]projectedEnvironmentValue, len(base)+6)
	canonicalKey := func(key string) string {
		if goos == "windows" {
			return strings.ToUpper(key)
		}
		return key
	}
	set := func(key, value string) {
		canonical := canonicalKey(key)
		outputKey := key
		if goos == "windows" {
			outputKey = canonical
		}
		envMap[canonical] = projectedEnvironmentValue{key: outputKey, value: value}
	}

	for _, item := range base {
		if idx := strings.IndexRune(item, '='); idx > 0 {
			set(item[:idx], item[idx+1:])
		}
	}

	if goos == "windows" {
		set("USERPROFILE", userEnv.Home)
		set("HOME", userEnv.Home)
		set("TEMP", userEnv.Tmp)
		set("TMP", userEnv.Tmp)
		set("APPDATA", userEnv.AppData)
		set("LOCALAPPDATA", userEnv.LocalAppData)
	} else {
		set("HOME", userEnv.Home)
		set("TMPDIR", userEnv.Tmp)
		set("XDG_CONFIG_HOME", userEnv.Config)
		set("XDG_CACHE_HOME", userEnv.Cache)
		set("XDG_STATE_HOME", userEnv.State)
	}

	keys := make([]string, 0, len(envMap))
	for key := range envMap {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	env := make([]string, 0, len(keys))
	for _, key := range keys {
		value := envMap[key]
		env = append(env, fmt.Sprintf("%s=%s", value.key, value.value))
	}
	return env
}

// ValidateExposePaths verifies the user-supplied path exposure rules before a
// child process is started.
func ValidateExposePaths(items []config.ExposePath) error {
	seen := map[string]struct{}{}
	for _, item := range items {
		if item.Source == "" {
			return fmt.Errorf("source is required")
		}
		if item.Mode != "ro" && item.Mode != "rw" {
			return fmt.Errorf("invalid expose_paths mode: %s", item.Mode)
		}

		source := filepath.Clean(item.Source)
		target := item.Target
		if target == "" {
			target = source
		}
		target = filepath.Clean(target)

		if strings.IndexByte(source, 0) >= 0 || strings.IndexByte(target, 0) >= 0 {
			return fmt.Errorf("source and target must not contain NUL bytes")
		}
		if !filepath.IsAbs(source) || !filepath.IsAbs(target) {
			return fmt.Errorf("source and target must be absolute paths")
		}
		if _, ok := seen[target]; ok {
			return fmt.Errorf("duplicate expose_path target: %s", target)
		}
		seen[target] = struct{}{}
	}
	return nil
}

// NormalizeExposePath fills implicit defaults and cleans path values so merge
// and validation logic can work with canonical paths.
func NormalizeExposePath(item config.ExposePath) config.ExposePath {
	source := filepath.Clean(item.Source)
	target := item.Target
	if target == "" {
		target = source
	}
	return config.ExposePath{
		Source: source,
		Target: filepath.Clean(target),
		Mode:   item.Mode,
	}
}

// DefaultExposePaths returns the minimum built-in host paths required for the
// current platform to run isolated child processes.
func DefaultExposePaths(root string) []config.ExposePath {
	items := []config.ExposePath{{
		Source: root,
		Target: root,
		Mode:   "rw",
	}}
	if runtime.GOOS == "linux" {
		items = append(items, defaultLinuxSystemExposePaths()...)
	}
	return items
}

func defaultLinuxSystemExposePaths() []config.ExposePath {
	return existingExposePaths([]config.ExposePath{
		{Source: "/usr", Target: "/usr", Mode: "ro"},
		{Source: "/bin", Target: "/bin", Mode: "ro"},
		{Source: "/lib", Target: "/lib", Mode: "ro"},
		{Source: "/lib64", Target: "/lib64", Mode: "ro"},
		{Source: "/etc/resolv.conf", Target: "/etc/resolv.conf", Mode: "ro"},
		{Source: "/etc/hosts", Target: "/etc/hosts", Mode: "ro"},
		{Source: "/etc/nsswitch.conf", Target: "/etc/nsswitch.conf", Mode: "ro"},
		{Source: "/etc/passwd", Target: "/etc/passwd", Mode: "ro"},
		{Source: "/etc/group", Target: "/etc/group", Mode: "ro"},
		{Source: "/etc/ssl", Target: "/etc/ssl", Mode: "ro"},
		{Source: "/etc/pki", Target: "/etc/pki", Mode: "ro"},
		{Source: "/etc/ca-certificates", Target: "/etc/ca-certificates", Mode: "ro"},
		{Source: "/usr/share/ca-certificates", Target: "/usr/share/ca-certificates", Mode: "ro"},
		{Source: "/usr/local/share/ca-certificates", Target: "/usr/local/share/ca-certificates", Mode: "ro"},
		{Source: "/etc/alternatives", Target: "/etc/alternatives", Mode: "ro"},
		{Source: "/usr/share/zoneinfo", Target: "/usr/share/zoneinfo", Mode: "ro"},
		{Source: "/etc/localtime", Target: "/etc/localtime", Mode: "ro"},
	})
}

// existingExposePaths keeps only the builtin host paths that exist on the
// current machine so Linux isolation does not fail on distro-specific paths.
func existingExposePaths(items []config.ExposePath) []config.ExposePath {
	filtered := make([]config.ExposePath, 0, len(items))
	for _, item := range items {
		if _, err := os.Stat(item.Source); err == nil {
			filtered = append(filtered, item)
		}
	}
	return filtered
}

// MergeExposePaths merges built-in rules with user overrides. Rules are keyed
// by target path so later entries replace earlier ones for the same target.
func MergeExposePaths(defaults []config.ExposePath, overrides []config.ExposePath) []config.ExposePath {
	merged := make([]config.ExposePath, 0, len(defaults)+len(overrides))
	indexByTarget := make(map[string]int, len(defaults)+len(overrides))
	appendOrReplace := func(item config.ExposePath) {
		normalized := NormalizeExposePath(item)
		if idx, ok := indexByTarget[normalized.Target]; ok {
			merged[idx] = normalized
			return
		}
		indexByTarget[normalized.Target] = len(merged)
		merged = append(merged, normalized)
	}
	for _, item := range defaults {
		appendOrReplace(item)
	}
	for _, item := range overrides {
		appendOrReplace(item)
	}
	return merged
}

// BuildLinuxMountPlan converts the merged expose-path configuration into the
// mount rules consumed by the Linux bubblewrap backend.
func BuildLinuxMountPlan(root string, overrides []config.ExposePath) []MountRule {
	return buildLinuxMountPlan(DefaultExposePaths(root), overrides)
}

func buildLinuxMountPlan(defaults, overrides []config.ExposePath) []MountRule {
	merged := MergeExposePaths(defaults, overrides)
	plan := make([]MountRule, 0, len(merged))
	for _, item := range merged {
		plan = append(plan, MountRule{Source: item.Source, Target: item.Target, Mode: item.Mode})
	}
	return plan
}

// BuildWindowsAccessRules derives the host-path access policy used by the
// Windows restricted-token backend.
func BuildWindowsAccessRules(root string, overrides []config.ExposePath) []AccessRule {
	merged := MergeExposePaths(nil, overrides)
	rules := make([]AccessRule, 0, len(merged)+1)
	rules = append(rules, AccessRule{Path: root, Mode: "rw"})
	for _, item := range merged {
		rules = append(rules, AccessRule{Path: item.Source, Mode: item.Mode})
	}
	return rules
}

func validateWindowsExposePaths(items []config.ExposePath) error {
	if len(items) == 0 {
		return nil
	}
	return fmt.Errorf("windows isolation does not yet support expose_paths filesystem rules")
}

// IsSupported reports whether the current platform has an implemented isolation
// backend.
func IsSupported() bool {
	return isSupportedOn(runtime.GOOS)
}

func isSupportedOn(goos string) bool {
	switch goos {
	case "linux", "windows":
		return true
	default:
		return false
	}
}

type launchProjection struct {
	isolation       config.IsolationConfig
	root            string
	goos            string
	linuxBaseMounts []MountRule
	windowsAccess   []AccessRule
	environment     []string
}

type launchOperations struct {
	goos        string
	resolveRoot func() (string, error)
	prepareRoot func(string) error
	apply       func(*exec.Cmd, launchProjection) error
	start       func(*exec.Cmd) error
	postStart   func(*exec.Cmd, launchProjection) error
	cleanup     func(*exec.Cmd)
	terminate   func(*exec.Cmd)
	wait        func(*exec.Cmd) error
}

func defaultLaunchOperations() launchOperations {
	return launchOperations{
		goos:        runtime.GOOS,
		resolveRoot: ResolveInstanceRoot,
		prepareRoot: PrepareInstanceRoot,
		apply:       applyPlatformIsolation,
		start: func(cmd *exec.Cmd) error {
			return cmd.Start()
		},
		postStart: postStartPlatformIsolation,
		cleanup:   cleanupPendingPlatformResources,
		terminate: terminateStartedCommand,
		wait: func(cmd *exec.Cmd) error {
			return cmd.Wait()
		},
	}
}

func buildLaunchProjection(
	isolationCfg config.IsolationConfig,
	operations launchOperations,
) (launchProjection, error) {
	launch := launchProjection{
		isolation: cloneIsolationConfig(isolationCfg),
		goos:      operations.goos,
	}
	if !launch.isolation.Enabled {
		return launch, nil
	}
	if !isSupportedOn(launch.goos) {
		return launchProjection{}, fmt.Errorf(
			"subprocess isolation is not supported on %s",
			launch.goos,
		)
	}
	root, err := operations.resolveRoot()
	if err != nil {
		return launchProjection{}, err
	}
	root = filepath.Clean(root)
	if root == "." || !filepath.IsAbs(root) {
		return launchProjection{}, fmt.Errorf(
			"instance root must resolve to an absolute directory",
		)
	}
	launch.root = root
	if err = ValidateExposePaths(launch.isolation.ExposePaths); err != nil {
		return launchProjection{}, err
	}

	switch launch.goos {
	case "linux":
		launch.linuxBaseMounts = BuildLinuxMountPlan(
			launch.root,
			launch.isolation.ExposePaths,
		)
	case "windows":
		if err = validateWindowsExposePaths(launch.isolation.ExposePaths); err != nil {
			return launchProjection{}, err
		}
		launch.windowsAccess = BuildWindowsAccessRules(
			launch.root,
			launch.isolation.ExposePaths,
		)
	}
	if err = operations.prepareRoot(launch.root); err != nil {
		return launchProjection{}, err
	}
	return launch, nil
}

func projectionForPolicy(
	policy ExecutionPolicy,
	operations launchOperations,
) (launchProjection, error) {
	isolationCfg, ok := policy.detachedIsolation()
	if !ok {
		return launchProjection{}, ErrExecutionPolicyUnavailable
	}
	return buildLaunchProjection(isolationCfg, operations)
}

func prepareCommandForPolicy(
	policy ExecutionPolicy,
	cmd *exec.Cmd,
	operations launchOperations,
) (launchProjection, error) {
	isolationCfg, ok := policy.detachedIsolation()
	if !ok {
		return launchProjection{}, ErrExecutionPolicyUnavailable
	}
	if cmd == nil {
		return launchProjection{}, fmt.Errorf("command is required")
	}
	launch, err := buildLaunchProjection(isolationCfg, operations)
	if err != nil {
		return launchProjection{}, err
	}
	if !launch.isolation.Enabled {
		return launch, nil
	}
	launch.environment = projectUserEnvironment(
		cmd.Environ(),
		ResolveUserEnv(launch.root),
		launch.goos,
	)
	cmd.Env = append([]string(nil), launch.environment...)
	if err = operations.apply(cmd, launch); err != nil {
		operations.cleanup(cmd)
		return launchProjection{}, err
	}
	return launch, nil
}

func startExecutionPolicy(
	policy ExecutionPolicy,
	cmd *exec.Cmd,
	operations launchOperations,
) error {
	launch, err := prepareCommandForPolicy(policy, cmd, operations)
	if err != nil {
		return err
	}
	if err = operations.start(cmd); err != nil {
		operations.cleanup(cmd)
		return err
	}
	if err = operations.postStart(cmd, launch); err != nil {
		operations.terminate(cmd)
		return err
	}
	return nil
}

func runExecutionPolicy(
	policy ExecutionPolicy,
	cmd *exec.Cmd,
	operations launchOperations,
) error {
	if err := startExecutionPolicy(policy, cmd, operations); err != nil {
		return err
	}
	return operations.wait(cmd)
}

func startLegacyPolicy(cmd *exec.Cmd, operations launchOperations) error {
	return startExecutionPolicy(currentLegacyPolicy(), cmd, operations)
}

func runLegacyPolicy(cmd *exec.Cmd, operations launchOperations) error {
	return runExecutionPolicy(currentLegacyPolicy(), cmd, operations)
}

func prepareLegacyPolicy(cmd *exec.Cmd, operations launchOperations) error {
	policy := currentLegacyPolicy()
	isolationCfg, ok := policy.detachedIsolation()
	if !ok {
		return ErrExecutionPolicyUnavailable
	}
	if isolationCfg.Enabled && operations.goos == "windows" {
		return fmt.Errorf(
			"PrepareCommand cannot complete Windows isolation; use Start or Run",
		)
	}
	_, err := prepareCommandForPolicy(policy, cmd, operations)
	return err
}

// Preflight validates one snapshot of the compatibility policy and prepares
// the instance runtime directories.
//
// Deprecated: call Start or Run on an explicit ExecutionPolicy.
func Preflight() error {
	_, err := projectionForPolicy(currentLegacyPolicy(), defaultLaunchOperations())
	return err
}

// Start uses one snapshot of the compatibility policy for the complete launch.
//
// Deprecated: call ExecutionPolicy.Start.
func Start(cmd *exec.Cmd) error {
	return startLegacyPolicy(cmd, defaultLaunchOperations())
}

// Run uses one snapshot of the compatibility policy for start and wait.
//
// Deprecated: call ExecutionPolicy.Run.
func Run(cmd *exec.Cmd) error {
	return runLegacyPolicy(cmd, defaultLaunchOperations())
}

func terminateStartedCommand(cmd *exec.Cmd) {
	cleanupPendingPlatformResources(cmd)
	if cmd == nil || cmd.Process == nil {
		return
	}
	_ = cmd.Process.Kill()
	_ = cmd.Wait()
}

// PrepareCommand mutates the command using one compatibility-policy snapshot.
// It cannot complete the Windows post-start Job Object step.
//
// Deprecated: call Start or Run on an explicit ExecutionPolicy.
func PrepareCommand(cmd *exec.Cmd) error {
	return prepareLegacyPolicy(cmd, defaultLaunchOperations())
}
