package isolation

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/sipeed/picoclaw/pkg/config"
)

const (
	maximumPolicyEnvironmentNames = 128
	maximumEnvironmentEntries     = 256
	maximumEnvironmentNameBytes   = 128
	maximumEnvironmentValueBytes  = 16 * 1024
	maximumEnvironmentBytes       = 24 * 1024
)

type capturedPolicyEnvironment struct {
	goos               string
	allowed            []string
	hostPath           string
	hostPathExt        string
	hostPathExtPresent bool
	err                error
}

func capturePolicyEnvironment(
	isolationCfg config.IsolationConfig,
	ambient []string,
	goos string,
) capturedPolicyEnvironment {
	captured := capturedPolicyEnvironment{goos: goos}
	if err := isolationCfg.ValidateEnvironmentAllowlist(); err != nil {
		captured.err = err
		return captured
	}

	names := isolationCfg.EnvironmentAllowlist
	if names == nil {
		names = config.DefaultIsolationEnvironmentAllowlist()
	}
	if len(names) > maximumPolicyEnvironmentNames {
		captured.err = fmt.Errorf(
			"isolation environment allowlist has %d names; maximum is %d",
			len(names),
			maximumPolicyEnvironmentNames,
		)
		return captured
	}

	ambientValues := make(map[string]projectedEnvironmentValue, len(ambient))
	for _, item := range ambient {
		key, value, ok := splitEnvironmentEntry(item)
		if !ok {
			continue
		}
		canonical := canonicalEnvironmentKey(key, goos)
		outputKey := key
		if goos == "windows" {
			outputKey = canonical
		}
		ambientValues[canonical] = projectedEnvironmentValue{
			key:   outputKey,
			value: value,
		}
	}

	captured.hostPath = environmentMapValue(ambientValues, "PATH", goos)
	captured.hostPathExt = environmentMapValue(ambientValues, "PATHEXT", goos)
	if goos == runtime.GOOS {
		captured.hostPath = normalizeExecutableSearchPath(captured.hostPath)
		_, pathExtPresent := environmentMapLookup(ambientValues, "PATHEXT", goos)
		captured.hostPathExtPresent = pathExtPresent
		captured.hostPathExt, _ = normalizeExecutablePathExtensions(
			captured.hostPathExt,
			pathExtPresent,
		)
	}
	if err := validatePrivateLookupEnvironment("PATH", captured.hostPath); err != nil {
		captured.err = err
		return captured
	}
	if err := validatePrivateLookupEnvironment("PATHEXT", captured.hostPathExt); err != nil {
		captured.err = err
		return captured
	}

	allowed := make(map[string]projectedEnvironmentValue, len(names)+4)
	for _, name := range names {
		canonical := canonicalEnvironmentKey(name, goos)
		value, ok := ambientValues[canonical]
		if !ok {
			continue
		}
		outputKey := name
		if goos == "windows" {
			outputKey = canonical
		}
		allowed[canonical] = projectedEnvironmentValue{
			key:   outputKey,
			value: value.value,
		}
	}

	if goos == "windows" {
		systemRoot := environmentMapValue(ambientValues, "SYSTEMROOT", goos)
		if !validWindowsSystemRoot(systemRoot) {
			captured.err = fmt.Errorf("windows SYSTEMROOT is unavailable or invalid")
			return captured
		}
		systemRoot = strings.TrimRight(systemRoot, `\/`)
		systemDrive := windowsSystemDrive(systemRoot)
		if systemDrive == "" {
			captured.err = fmt.Errorf("windows SYSTEMDRIVE is unavailable")
			return captured
		}
		setProjectedEnvironmentValue(allowed, "SYSTEMROOT", systemRoot, goos)
		setProjectedEnvironmentValue(allowed, "WINDIR", systemRoot, goos)
		setProjectedEnvironmentValue(allowed, "SYSTEMDRIVE", systemDrive, goos)
		setProjectedEnvironmentValue(
			allowed,
			"COMSPEC",
			windowsJoin(systemRoot, "System32", "cmd.exe"),
			goos,
		)
		setProjectedEnvironmentValue(
			allowed,
			"NoDefaultCurrentDirectoryInExePath",
			"1",
			goos,
		)
	}
	if goos == runtime.GOOS {
		normalizeFinalExecutableEnvironment(allowed, goos)
	}

	captured.allowed = sortedEnvironmentValues(allowed)
	if err := validateFinalEnvironment(captured.allowed); err != nil {
		captured.err = fmt.Errorf("capture isolation environment: %w", err)
		captured.allowed = nil
	}
	return captured
}

func captureCurrentPolicyEnvironment(
	isolationCfg config.IsolationConfig,
) capturedPolicyEnvironment {
	return capturePolicyEnvironment(isolationCfg, os.Environ(), runtime.GOOS)
}

func cloneCapturedPolicyEnvironment(
	captured capturedPolicyEnvironment,
) capturedPolicyEnvironment {
	cloned := captured
	cloned.allowed = append([]string(nil), captured.allowed...)
	return cloned
}

func restrictedEnvironmentForCommand(
	captured capturedPolicyEnvironment,
	explicit []string,
	dir string,
	enabled bool,
	userEnv UserEnv,
) ([]string, error) {
	if captured.err != nil {
		return nil, captured.err
	}
	if len(explicit) > maximumEnvironmentEntries {
		return nil, fmt.Errorf(
			"explicit environment has %d entries; maximum is %d",
			len(explicit),
			maximumEnvironmentEntries,
		)
	}
	values := make(
		map[string]projectedEnvironmentValue,
		len(captured.allowed)+len(explicit)+8,
	)
	for _, item := range captured.allowed {
		key, value, ok := splitEnvironmentEntry(item)
		if !ok {
			return nil, fmt.Errorf("captured isolation environment is invalid")
		}
		setProjectedEnvironmentValue(values, key, value, captured.goos)
	}
	explicitBytes := 1
	for _, item := range explicit {
		key, value, err := validateExplicitEnvironmentEntry(item)
		if err != nil {
			return nil, err
		}
		explicitBytes += len(key) + 1 + len(value) + 1
		if explicitBytes > maximumEnvironmentBytes {
			return nil, fmt.Errorf(
				"explicit environment exceeds %d encoded bytes",
				maximumEnvironmentBytes,
			)
		}
		setProjectedEnvironmentValue(values, key, value, captured.goos)
	}
	if captured.goos == "windows" {
		for _, key := range []string{
			"SYSTEMROOT",
			"WINDIR",
			"SYSTEMDRIVE",
			"COMSPEC",
			"NoDefaultCurrentDirectoryInExePath",
		} {
			if value := environmentSliceValue(captured.allowed, key, captured.goos); value != "" {
				setProjectedEnvironmentValue(values, key, value, captured.goos)
			}
		}
	}
	if captured.goos == runtime.GOOS {
		normalizeFinalExecutableEnvironment(values, captured.goos)
	}

	pwd, err := effectiveCommandDirectory(dir, captured.goos)
	if err != nil {
		return nil, fmt.Errorf("resolve command working directory: %w", err)
	}
	setProjectedEnvironmentValue(values, "PWD", pwd, captured.goos)

	if enabled {
		if captured.goos == "windows" {
			setProjectedEnvironmentValue(values, "USERPROFILE", userEnv.Home, captured.goos)
			setProjectedEnvironmentValue(values, "HOME", userEnv.Home, captured.goos)
			setProjectedEnvironmentValue(values, "HOMEDRIVE", windowsSystemDrive(userEnv.Home), captured.goos)
			setProjectedEnvironmentValue(values, "HOMEPATH", windowsHomePath(userEnv.Home), captured.goos)
			setProjectedEnvironmentValue(values, "TEMP", userEnv.Tmp, captured.goos)
			setProjectedEnvironmentValue(values, "TMP", userEnv.Tmp, captured.goos)
			setProjectedEnvironmentValue(values, "APPDATA", userEnv.AppData, captured.goos)
			setProjectedEnvironmentValue(values, "LOCALAPPDATA", userEnv.LocalAppData, captured.goos)
		} else {
			setProjectedEnvironmentValue(values, "HOME", userEnv.Home, captured.goos)
			setProjectedEnvironmentValue(values, "TMPDIR", userEnv.Tmp, captured.goos)
			setProjectedEnvironmentValue(values, "XDG_CONFIG_HOME", userEnv.Config, captured.goos)
			setProjectedEnvironmentValue(values, "XDG_CACHE_HOME", userEnv.Cache, captured.goos)
			setProjectedEnvironmentValue(values, "XDG_STATE_HOME", userEnv.State, captured.goos)
		}
	}

	environment := sortedEnvironmentValues(values)
	if err = validateFinalEnvironment(environment); err != nil {
		return nil, err
	}
	return environment, nil
}

func validateExplicitEnvironmentEntry(item string) (string, string, error) {
	key, value, ok := splitEnvironmentEntry(item)
	if !ok || !validEnvironmentName(key) {
		return "", "", fmt.Errorf("explicit environment name is invalid")
	}
	if !utf8.ValidString(value) || strings.IndexByte(value, 0) >= 0 ||
		len(value) > maximumEnvironmentValueBytes {
		return "", "", fmt.Errorf("explicit environment value for %s is invalid", key)
	}
	return key, value, nil
}

func validatePrivateLookupEnvironment(name, value string) error {
	if value == "" {
		return nil
	}
	if !utf8.ValidString(value) || strings.IndexByte(value, 0) >= 0 ||
		len(value) > maximumEnvironmentValueBytes {
		return fmt.Errorf("captured %s lookup value is invalid", name)
	}
	return nil
}

func validateFinalEnvironment(environment []string) error {
	if len(environment) > maximumEnvironmentEntries {
		return fmt.Errorf(
			"environment has %d entries; maximum is %d",
			len(environment),
			maximumEnvironmentEntries,
		)
	}
	total := 1
	for _, item := range environment {
		key, value, ok := splitEnvironmentEntry(item)
		if !ok || !validEnvironmentName(key) || !utf8.ValidString(value) ||
			strings.IndexByte(value, 0) >= 0 || len(value) > maximumEnvironmentValueBytes {
			return fmt.Errorf("environment entry is invalid")
		}
		total += len(key) + 1 + len(value) + 1
		if total > maximumEnvironmentBytes {
			return fmt.Errorf(
				"environment exceeds %d encoded bytes",
				maximumEnvironmentBytes,
			)
		}
	}
	return nil
}

func validEnvironmentName(name string) bool {
	if name == "" || len(name) > maximumEnvironmentNameBytes || !utf8.ValidString(name) {
		return false
	}
	for index, character := range name {
		if character == '_' || character >= 'A' && character <= 'Z' ||
			character >= 'a' && character <= 'z' ||
			index > 0 && character >= '0' && character <= '9' {
			continue
		}
		return false
	}
	return true
}

func splitEnvironmentEntry(item string) (string, string, bool) {
	index := strings.IndexByte(item, '=')
	if index <= 0 {
		return "", "", false
	}
	return item[:index], item[index+1:], true
}

func canonicalEnvironmentKey(key, goos string) string {
	if goos == "windows" {
		return strings.ToUpper(key)
	}
	return key
}

func setProjectedEnvironmentValue(
	values map[string]projectedEnvironmentValue,
	key string,
	value string,
	goos string,
) {
	canonical := canonicalEnvironmentKey(key, goos)
	outputKey := key
	if goos == "windows" {
		outputKey = canonical
	}
	values[canonical] = projectedEnvironmentValue{key: outputKey, value: value}
}

func environmentMapValue(
	values map[string]projectedEnvironmentValue,
	key string,
	goos string,
) string {
	return values[canonicalEnvironmentKey(key, goos)].value
}

func environmentMapLookup(
	values map[string]projectedEnvironmentValue,
	key string,
	goos string,
) (string, bool) {
	value, ok := values[canonicalEnvironmentKey(key, goos)]
	return value.value, ok
}

func normalizeFinalExecutableEnvironment(
	values map[string]projectedEnvironmentValue,
	goos string,
) {
	pathValue, pathPresent := environmentMapLookup(values, "PATH", goos)
	if pathPresent {
		setProjectedEnvironmentValue(
			values,
			"PATH",
			normalizeExecutableSearchPath(pathValue),
			goos,
		)
	}
	pathExtValue, pathExtPresent := environmentMapLookup(values, "PATHEXT", goos)
	if normalized, emit := normalizeExecutablePathExtensions(pathExtValue, pathExtPresent); emit {
		setProjectedEnvironmentValue(values, "PATHEXT", normalized, goos)
	}
}

func environmentSliceValue(environment []string, key, goos string) string {
	value, _ := environmentSliceLookup(environment, key, goos)
	return value
}

func environmentSliceLookup(environment []string, key, goos string) (string, bool) {
	canonical := canonicalEnvironmentKey(key, goos)
	for index := len(environment) - 1; index >= 0; index-- {
		name, value, ok := splitEnvironmentEntry(environment[index])
		if ok && canonicalEnvironmentKey(name, goos) == canonical {
			return value, true
		}
	}
	return "", false
}

func sortedEnvironmentValues(
	values map[string]projectedEnvironmentValue,
) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	environment := make([]string, 0, len(keys))
	for _, key := range keys {
		value := values[key]
		environment = append(environment, value.key+"="+value.value)
	}
	return environment
}

func effectiveCommandDirectory(dir, goos string) (string, error) {
	if dir == "" {
		return os.Getwd()
	}
	if goos == "windows" && windowsAbsolutePath(dir) {
		if len(dir) == 3 && dir[1] == ':' {
			return strings.ToUpper(dir[:2]) + `\`, nil
		}
		return strings.TrimRight(dir, `\/`), nil
	}
	if filepath.IsAbs(dir) {
		return filepath.Clean(dir), nil
	}
	workingDirectory, err := os.Getwd()
	if err != nil {
		return "", err
	}
	return filepath.Clean(filepath.Join(workingDirectory, dir)), nil
}

func windowsAbsolutePath(path string) bool {
	return len(path) >= 3 && path[1] == ':' && (path[2] == '\\' || path[2] == '/') ||
		strings.HasPrefix(path, `\\`)
}

func validWindowsSystemRoot(path string) bool {
	if !windowsAbsolutePath(path) || len(path) < 4 || path[1] != ':' {
		return false
	}
	for _, character := range path {
		if character < 0x20 || character == 0x7f {
			return false
		}
	}
	return true
}

func windowsSystemDrive(path string) string {
	if len(path) >= 2 && path[1] == ':' {
		return strings.ToUpper(path[:2])
	}
	return ""
}

func windowsHomePath(path string) string {
	if len(path) >= 2 && path[1] == ':' {
		if len(path) == 2 {
			return `\`
		}
		return path[2:]
	}
	return path
}

func windowsJoin(parts ...string) string {
	joined := strings.TrimRight(parts[0], `\/`)
	for _, part := range parts[1:] {
		joined += `\` + strings.Trim(part, `\/`)
	}
	return joined
}
