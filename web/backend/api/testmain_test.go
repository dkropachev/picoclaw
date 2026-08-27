package api

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"

	"github.com/sipeed/picoclaw/pkg/config"
)

const (
	apiTestRuntimeManifestEnv = "_PICOCLAW_API_TEST_RUNTIME_MANIFEST"
	apiTestRuntimeOwnerEnv    = "_PICOCLAW_API_TEST_RUNTIME_OWNER_PID"
	apiTestFixtureMarkerEnv   = "_PICOCLAW_API_TEST_FIXTURE_MARKER"
	apiTestFixtureExitCode    = 86
)

// apiTestRuntimeManifest is the complete host-side state granted to the API
// test suite. Keeping every application path in one manifest makes helper
// subprocess inheritance explicit and keeps accidental gateway launches away
// from an operator's home, configuration, binary, and databases.
type apiTestRuntimeManifest struct {
	Root          string `json:"root"`
	OSHome        string `json:"os_home"`
	PicoHome      string `json:"pico_home"`
	ConfigPath    string `json:"config_path"`
	Workspace     string `json:"workspace"`
	EventDB       string `json:"event_db"`
	SuiteBinary   string `json:"suite_binary"`
	FixtureBinary string `json:"fixture_binary"`
	FixtureMarker string `json:"fixture_marker"`
}

var apiSuiteTestRuntime *apiTestRuntimeManifest

// TestMain gives the complete API package one test-owned runtime. Go test is
// not an OS sandbox: without these overrides, helpers can inherit an
// operator's HOME, PicoClaw PID file, config, executable lookup, and databases.
func TestMain(m *testing.M) {
	if handled, code := runAPIGatewayEnvironmentCaptureIfRequested(); handled {
		os.Exit(code)
	}
	if handled, code := runAPITestFixtureBinaryIfRequested(); handled {
		os.Exit(code)
	}

	manifest, ownsRuntime, err := isolatedAPITestRuntime()
	if err != nil {
		fmt.Fprintf(os.Stderr, "create isolated PicoClaw API test runtime: %v\n", err)
		os.Exit(1)
	}
	apiSuiteTestRuntime = manifest
	apiSuiteGatewayProcessOps = newOwnedGatewayTestProcessOps()
	gatewayProcessOps = apiSuiteGatewayProcessOps

	clearAmbientAPITestEnvironment(ownsRuntime)
	if err = applyAPITestRuntimeEnvironment(manifest); err != nil {
		fmt.Fprintf(os.Stderr, "apply isolated PicoClaw API test runtime: %v\n", err)
		if ownsRuntime {
			_ = os.RemoveAll(manifest.Root)
		}
		os.Exit(1)
	}

	code := m.Run()
	if ownsRuntime {
		err = os.RemoveAll(manifest.Root)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "remove isolated PicoClaw API test runtime: %v\n", err)
		if code == 0 {
			code = 1
		}
	}
	// Keep every runtime variable pointed at the now-removed sandbox until
	// process exit. Restoring inherited paths would reopen access for a leaked
	// test goroutine.
	os.Exit(code)
}

func runAPIGatewayEnvironmentCaptureIfRequested() (bool, int) {
	var outputPath string
	for index, argument := range os.Args {
		if argument == "--" && index+2 < len(os.Args) &&
			os.Args[index+1] == "gateway-env-helper" {
			outputPath = os.Args[index+2]
			break
		}
	}
	if outputPath == "" {
		return false, 0
	}
	manifest, err := inheritedAPITestRuntime()
	if err != nil || os.Getenv(apiTestFixtureMarkerEnv) != manifest.FixtureMarker {
		fmt.Fprintln(os.Stderr, "gateway environment capture lacks valid test runtime authority")
		return true, 2
	}
	outputPath, err = filepath.Abs(outputPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "resolve gateway environment capture path: %v\n", err)
		return true, 2
	}
	inside, pathErr := pathWithinAPITestRoot(manifest.Root, filepath.Clean(outputPath))
	if pathErr != nil || !inside {
		fmt.Fprintf(os.Stderr, "gateway environment capture path escapes test runtime: %q\n", outputPath)
		return true, 2
	}
	host, hostSet := os.LookupEnv(config.EnvGatewayHost)
	raw, err := json.Marshal(gatewayStartEnvSnapshot{
		GatewayHost:    host,
		GatewayHostSet: hostSet,
		ConfigPath:     os.Getenv(config.EnvConfig),
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "marshal gateway environment capture: %v\n", err)
		return true, 2
	}
	if err = os.WriteFile(outputPath, raw, 0o600); err != nil {
		fmt.Fprintf(os.Stderr, "write gateway environment capture: %v\n", err)
		return true, 2
	}
	return true, 0
}

func isolatedAPITestRuntime() (*apiTestRuntimeManifest, bool, error) {
	manifest, err := inheritedAPITestRuntime()
	if err == nil {
		// Advance ownership in this process's environment. A grandchild now sees
		// its direct parent as owner instead of creating or trusting another tree.
		if err = os.Setenv(apiTestRuntimeOwnerEnv, strconv.Itoa(os.Getpid())); err != nil {
			return nil, false, err
		}
		return manifest, false, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return nil, false, err
	}

	manifest, manifestPath, err := createAPITestRuntime()
	if err != nil {
		return nil, false, err
	}
	cleanup := func() {
		_ = os.RemoveAll(manifest.Root)
	}
	if err = os.Setenv(apiTestRuntimeManifestEnv, manifestPath); err != nil {
		cleanup()
		return nil, false, err
	}
	if err = os.Setenv(apiTestRuntimeOwnerEnv, strconv.Itoa(os.Getpid())); err != nil {
		cleanup()
		return nil, false, err
	}
	if err = os.Setenv(apiTestFixtureMarkerEnv, manifest.FixtureMarker); err != nil {
		cleanup()
		return nil, false, err
	}
	return manifest, true, nil
}

func inheritedAPITestRuntime() (*apiTestRuntimeManifest, error) {
	manifestPath := strings.TrimSpace(os.Getenv(apiTestRuntimeManifestEnv))
	ownerPID, ownerErr := strconv.Atoi(os.Getenv(apiTestRuntimeOwnerEnv))
	if manifestPath == "" || ownerErr != nil || ownerPID != os.Getppid() || !filepath.IsAbs(manifestPath) {
		return nil, os.ErrNotExist
	}

	raw, err := os.ReadFile(manifestPath)
	if err != nil {
		return nil, fmt.Errorf("read inherited API test runtime manifest: %w", err)
	}
	var manifest apiTestRuntimeManifest
	if err = json.Unmarshal(raw, &manifest); err != nil {
		return nil, fmt.Errorf("decode inherited API test runtime manifest: %w", err)
	}
	if err = validateAPITestRuntimeManifest(&manifest, manifestPath); err != nil {
		return nil, err
	}
	if os.Getenv(apiTestFixtureMarkerEnv) != manifest.FixtureMarker {
		return nil, errors.New("inherited API test runtime marker mismatch")
	}
	return &manifest, nil
}

func createAPITestRuntime() (*apiTestRuntimeManifest, string, error) {
	root, err := os.MkdirTemp("", "picoclaw-api-test-runtime-")
	if err != nil {
		return nil, "", err
	}
	cleanup := func(err error) (*apiTestRuntimeManifest, string, error) {
		_ = os.RemoveAll(root)
		return nil, "", err
	}

	suiteBinary, err := os.Executable()
	if err != nil {
		return cleanup(fmt.Errorf("resolve API test executable: %w", err))
	}
	suiteBinary, err = filepath.Abs(suiteBinary)
	if err != nil {
		return cleanup(fmt.Errorf("resolve absolute API test executable: %w", err))
	}
	markerBytes := make([]byte, 24)
	if _, err = rand.Read(markerBytes); err != nil {
		return cleanup(fmt.Errorf("create API test fixture marker: %w", err))
	}

	manifest := &apiTestRuntimeManifest{
		Root:          filepath.Clean(root),
		OSHome:        filepath.Join(root, "os-home"),
		PicoHome:      filepath.Join(root, "picoclaw-home"),
		SuiteBinary:   filepath.Clean(suiteBinary),
		FixtureMarker: hex.EncodeToString(markerBytes),
	}
	fixtureName := "picoclaw-api-test-fixture"
	if runtime.GOOS == "windows" {
		fixtureName += ".exe"
	}
	manifest.FixtureBinary = filepath.Join(root, "bin", fixtureName)
	manifest.ConfigPath = filepath.Join(manifest.PicoHome, "config.json")
	manifest.Workspace = filepath.Join(manifest.PicoHome, "workspace")
	manifest.EventDB = filepath.Join(manifest.Workspace, "eventing", "events.db")

	for _, dir := range []string{
		manifest.OSHome,
		filepath.Join(manifest.OSHome, ".config"),
		filepath.Join(manifest.OSHome, ".local", "share"),
		filepath.Join(manifest.OSHome, ".cache"),
		filepath.Join(manifest.OSHome, ".local", "state"),
		filepath.Join(manifest.OSHome, ".run"),
		filepath.Join(manifest.OSHome, ".codex"),
		filepath.Join(manifest.OSHome, ".claude"),
		filepath.Join(manifest.OSHome, ".openclaw"),
		filepath.Join(manifest.OSHome, ".gnupg"),
		filepath.Join(manifest.OSHome, ".config", "git"),
		filepath.Join(manifest.OSHome, "AppData", "Roaming"),
		filepath.Join(manifest.OSHome, "AppData", "Local"),
		manifest.PicoHome,
		manifest.Workspace,
		filepath.Dir(manifest.EventDB),
		filepath.Join(root, "bin"),
		filepath.Join(root, "runs"),
		filepath.Join(root, "tmp"),
	} {
		if err = os.MkdirAll(dir, 0o700); err != nil {
			return cleanup(fmt.Errorf("create API test runtime directory %q: %w", dir, err))
		}
	}
	if err = copyAPITestExecutable(manifest.SuiteBinary, manifest.FixtureBinary); err != nil {
		return cleanup(err)
	}
	// An empty SQLite file is a valid fresh database. Runtime tests that enable
	// eventing initialize their schema in this test-owned file.
	if err = os.WriteFile(manifest.EventDB, nil, 0o600); err != nil {
		return cleanup(fmt.Errorf("create API test event database: %w", err))
	}

	cfg := config.DefaultConfig()
	cfg.Agents.Defaults.Workspace = manifest.Workspace
	cfg.Events.Ingress.DatabasePath = manifest.EventDB
	cfg.Gateway.Host = "127.0.0.1"
	cfg.Gateway.Port, err = allocateAPITestLoopbackPort()
	if err != nil {
		return cleanup(fmt.Errorf("allocate API test gateway port: %w", err))
	}
	if err = config.SaveConfig(manifest.ConfigPath, cfg); err != nil {
		return cleanup(fmt.Errorf("write API test config: %w", err))
	}

	manifestPath := filepath.Join(root, "runtime-manifest.json")
	raw, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return cleanup(fmt.Errorf("encode API test runtime manifest: %w", err))
	}
	if err = os.WriteFile(manifestPath, raw, 0o600); err != nil {
		return cleanup(fmt.Errorf("write API test runtime manifest: %w", err))
	}
	return manifest, manifestPath, nil
}

func allocateAPITestLoopbackPort() (int, error) {
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	port := listener.Addr().(*net.TCPAddr).Port
	if err = listener.Close(); err != nil {
		return 0, err
	}
	return port, nil
}

func clearAmbientAPITestEnvironment(ownsRuntime bool) {
	for _, entry := range os.Environ() {
		name, _, ok := strings.Cut(entry, "=")
		upper := strings.ToUpper(name)
		internalHelper := ownsRuntime && strings.HasPrefix(upper, "_PICOCLAW_") &&
			upper != apiTestRuntimeManifestEnv && upper != apiTestRuntimeOwnerEnv &&
			upper != apiTestFixtureMarkerEnv
		if !ok || (!internalHelper && !strings.HasPrefix(upper, "PICOCLAW_") &&
			!isAmbientTestCredentialOrAuthority(upper)) {
			continue
		}
		_ = os.Unsetenv(name)
	}
}

func isAmbientTestCredentialOrAuthority(name string) bool {
	upper := strings.ToUpper(strings.TrimSpace(name))
	for _, prefix := range []string{
		"AWS_",
		"AZURE_",
		"CLOUDSDK_",
		"COHERE_",
		"DEEPSEEK_",
		"GCLOUD_",
		"GEMINI_",
		"GOOGLE_",
		"GROQ_",
		"HF_",
		"HUGGINGFACE_",
		"HUGGING_FACE_",
		"MISTRAL_",
		"OCI_",
		"OPENAI_",
		"ANTHROPIC_",
		"LISTEN_",
		"SYSTEMD_",
		"VAULT_",
		"VERTEX_",
		"WATCHDOG_",
	} {
		if strings.HasPrefix(upper, prefix) {
			return true
		}
	}
	for _, suffix := range []string{
		"_ACCESS_KEY",
		"_API_KEY",
		"_API_KEYS",
		"_CREDENTIAL",
		"_CREDENTIALS",
		"_CREDENTIALS_FILE",
		"_JWT",
		"_JWT_V2",
		"_PAT",
		"_PASSWORD",
		"_PRIVATE_KEY",
		"_SECRET",
		"_SECRET_KEY",
		"_TOKEN",
		"_TOKEN_FILE",
	} {
		if strings.HasSuffix(upper, suffix) {
			return true
		}
	}
	switch upper {
	case "API_KEY", "ACCESS_TOKEN", "AUTH_TOKEN", "CREDENTIALS", "GH_PAT", "GITHUB_PAT", "PASSWORD", "PAT",
		"PRIVATE_KEY", "REFRESH_TOKEN", "SECRET", "TOKEN",
		"BASH_ENV", "BOTO_CONFIG", "DOCKER_AUTH_CONFIG", "DOCKER_CONFIG", "DOCKER_CONTEXT", "DOCKER_HOST",
		"GCM_INTERACTIVE", "GIT_ASKPASS", "GIT_CEILING_DIRECTORIES", "GIT_COMMON_DIR", "GIT_CONFIG_PARAMETERS", "GIT_DIR",
		"GIT_EXEC_PATH", "GIT_INDEX_FILE", "GIT_OBJECT_DIRECTORY", "GIT_SSH", "GIT_SSH_COMMAND", "GIT_WORK_TREE",
		"GIT_TERMINAL_PROMPT", "GOAUTH", "GOENV",
		"GPG_AGENT_INFO", "KRB5CCNAME", "KRB5_CONFIG", "KUBECONFIG", "LD_LIBRARY_PATH", "LD_PRELOAD",
		"INVOCATION_ID", "JOURNAL_STREAM", "NOTIFY_SOCKET",
		"DYLD_INSERT_LIBRARIES", "DYLD_LIBRARY_PATH", "MYSQL_PWD", "NETRC", "NODE_AUTH_TOKEN", "NODE_OPTIONS",
		"NPM_CONFIG_USERCONFIG", "NPM_TOKEN", "PGPASSFILE", "SSH_AGENT_PID", "SSH_ASKPASS", "SSH_ASKPASS_REQUIRE",
		"SSH_AUTH_SOCK", "SSLKEYLOGFILE":
		return true
	}
	return strings.HasPrefix(upper, "GIT_CONFIG_") || strings.HasPrefix(upper, "TF_TOKEN_")
}

func validateAPITestRuntimeManifest(manifest *apiTestRuntimeManifest, manifestPath string) error {
	if manifest == nil || manifest.FixtureMarker == "" {
		return errors.New("invalid inherited API test runtime manifest")
	}
	paths := []string{
		manifest.Root,
		manifest.OSHome,
		manifest.PicoHome,
		manifest.ConfigPath,
		manifest.Workspace,
		manifest.EventDB,
		manifest.SuiteBinary,
		manifest.FixtureBinary,
		manifestPath,
	}
	for _, path := range paths {
		if !filepath.IsAbs(path) || filepath.Clean(path) != path {
			return fmt.Errorf("API test runtime path is not clean and absolute: %q", path)
		}
	}
	for _, path := range []string{
		manifest.OSHome,
		manifest.PicoHome,
		manifest.ConfigPath,
		manifest.Workspace,
		manifest.EventDB,
		manifest.FixtureBinary,
		manifestPath,
	} {
		inside, err := pathWithinAPITestRoot(manifest.Root, path)
		if err != nil || !inside {
			return fmt.Errorf("API test runtime path escapes root: %q", path)
		}
	}
	currentExecutable, err := os.Executable()
	if err != nil {
		return fmt.Errorf("resolve inherited API test executable: %w", err)
	}
	currentExecutable, err = filepath.Abs(currentExecutable)
	if err != nil {
		return fmt.Errorf("resolve absolute inherited API test executable: %w", err)
	}
	if !sameAPITestPath(manifest.SuiteBinary, currentExecutable) &&
		!sameAPITestPath(manifest.FixtureBinary, currentExecutable) {
		return fmt.Errorf(
			"inherited API test binaries %q and %q do not match current executable %q",
			manifest.SuiteBinary,
			manifest.FixtureBinary,
			currentExecutable,
		)
	}
	return nil
}

func pathWithinAPITestRoot(root, path string) (bool, error) {
	relative, err := filepath.Rel(root, path)
	if err != nil {
		return false, err
	}
	return relative != ".." && !strings.HasPrefix(relative, ".."+string(os.PathSeparator)), nil
}

func sameAPITestPath(left, right string) bool {
	left = filepath.Clean(left)
	right = filepath.Clean(right)
	if runtime.GOOS == "windows" {
		return strings.EqualFold(left, right)
	}
	return left == right
}

func applyAPITestRuntimeEnvironment(manifest *apiTestRuntimeManifest) error {
	values := map[string]string{
		"HOME":                      manifest.OSHome,
		"USERPROFILE":               manifest.OSHome,
		"XDG_CONFIG_HOME":           filepath.Join(manifest.OSHome, ".config"),
		"XDG_DATA_HOME":             filepath.Join(manifest.OSHome, ".local", "share"),
		"XDG_CACHE_HOME":            filepath.Join(manifest.OSHome, ".cache"),
		"XDG_STATE_HOME":            filepath.Join(manifest.OSHome, ".local", "state"),
		"XDG_RUNTIME_DIR":           filepath.Join(manifest.OSHome, ".run"),
		"CODEX_HOME":                filepath.Join(manifest.OSHome, ".codex"),
		"CLAUDE_CONFIG_DIR":         filepath.Join(manifest.OSHome, ".claude"),
		"OPENCLAW_HOME":             filepath.Join(manifest.OSHome, ".openclaw"),
		"GNUPGHOME":                 filepath.Join(manifest.OSHome, ".gnupg"),
		"GIT_CONFIG_GLOBAL":         filepath.Join(manifest.OSHome, ".config", "git", "config"),
		"GIT_CONFIG_NOSYSTEM":       "1",
		"TMPDIR":                    filepath.Join(manifest.Root, "tmp"),
		"TEMP":                      filepath.Join(manifest.Root, "tmp"),
		"TMP":                       filepath.Join(manifest.Root, "tmp"),
		"DBUS_SESSION_BUS_ADDRESS":  "unix:path=" + filepath.Join(manifest.Root, "no-systemd-bus"),
		"APPDATA":                   filepath.Join(manifest.OSHome, "AppData", "Roaming"),
		"LOCALAPPDATA":              filepath.Join(manifest.OSHome, "AppData", "Local"),
		"AWS_EC2_METADATA_DISABLED": "true",
		"GIT_TERMINAL_PROMPT":       "0",
		"GCM_INTERACTIVE":           "never",
		"GOAUTH":                    "off",
		"GOENV":                     "off",
		config.EnvHome:              manifest.PicoHome,
		config.EnvConfig:            manifest.ConfigPath,
		config.EnvBinary:            manifest.FixtureBinary,
		apiTestFixtureMarkerEnv:     manifest.FixtureMarker,
	}
	if runtime.GOOS == "windows" {
		volume := filepath.VolumeName(manifest.OSHome)
		values["HOMEDRIVE"] = volume
		values["HOMEPATH"] = strings.TrimPrefix(manifest.OSHome, volume)
	}
	for key, value := range values {
		if err := os.Setenv(key, value); err != nil {
			return fmt.Errorf("set %s: %w", key, err)
		}
	}
	return nil
}

func copyAPITestExecutable(source, target string) error {
	sourceFile, err := os.Open(source)
	if err != nil {
		return fmt.Errorf("open API test executable: %w", err)
	}
	defer sourceFile.Close()
	targetFile, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o700)
	if err != nil {
		return fmt.Errorf("create API fixture executable: %w", err)
	}
	if _, err = io.Copy(targetFile, sourceFile); err != nil {
		_ = targetFile.Close()
		return fmt.Errorf("copy API fixture executable: %w", err)
	}
	if err = targetFile.Sync(); err != nil {
		_ = targetFile.Close()
		return fmt.Errorf("sync API fixture executable: %w", err)
	}
	if err = targetFile.Close(); err != nil {
		return fmt.Errorf("close API fixture executable: %w", err)
	}
	return nil
}

// runAPITestFixtureBinaryIfRequested turns this package's test executable into
// a fail-closed binary fixture for accidental `picoclaw gateway` launches. The
// marker and direct-parent manifest check prevent ordinary test invocations
// from entering this mode. Without this early intercept, invoking a Go test
// executable with positional gateway arguments can recursively run the suite.
func runAPITestFixtureBinaryIfRequested() (bool, int) {
	manifest, err := inheritedAPITestRuntime()
	if err != nil || os.Getenv(apiTestFixtureMarkerEnv) != manifest.FixtureMarker {
		return false, 0
	}
	currentExecutable, err := os.Executable()
	if err != nil {
		return true, apiTestFixtureExitCode
	}
	currentExecutable, err = filepath.Abs(currentExecutable)
	if err != nil || !sameAPITestPath(currentExecutable, manifest.FixtureBinary) {
		return false, 0
	}
	if len(os.Args) < 2 || strings.HasPrefix(os.Args[1], "-test.") {
		return false, 0
	}
	if os.Args[1] == "version" {
		fmt.Printf("picoclaw test-fixture (git: test-fixture)\nBuild: test\nGo: %s\n", runtime.Version())
		return true, 0
	}
	fmt.Fprintf(os.Stderr, "API test fixture binary refused command %q\n", os.Args[1])
	return true, apiTestFixtureExitCode
}
