package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

const (
	coreBuildTags = "goolm,stdjson"
	testGoEnv     = "PICOCLAW_TEST_GO_BINARY"
)

type testConfig struct {
	Agents struct {
		Defaults struct {
			Workspace string `json:"workspace"`
		} `json:"defaults"`
	} `json:"agents"`
	Gateway struct {
		Host string `json:"host"`
		Port int    `json:"port"`
	} `json:"gateway"`
	Events struct {
		Ingress struct {
			DatabasePath string `json:"database_path"`
		} `json:"ingress"`
	} `json:"events"`
}

type testLayout struct {
	root        string
	home        string
	picoHome    string
	configPath  string
	workspace   string
	eventDB     string
	binary      string
	environment []string
}

func main() {
	os.Exit(run(os.Args[1:]))
}

func run(arguments []string) (code int) {
	if len(arguments) > 0 && arguments[0] == "--" {
		arguments = arguments[1:]
	}
	if len(arguments) == 0 {
		fmt.Fprintln(os.Stderr, "usage: go run ./scripts/hermetic-go-test -- <command> [args...]")
		return 2
	}

	repositoryRoot, err := repositoryRoot()
	if err != nil {
		fmt.Fprintf(os.Stderr, "locate repository root: %v\n", err)
		return 1
	}
	goBinary, err := goExecutable()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	caches, err := resolveGoCaches(goBinary)
	if err != nil {
		fmt.Fprintf(os.Stderr, "resolve Go caches: %v\n", err)
		return 1
	}
	layout, err := createTestLayout(caches)
	if err != nil {
		fmt.Fprintf(os.Stderr, "create hermetic test layout: %v\n", err)
		return 1
	}
	defer func() {
		if removeErr := os.RemoveAll(layout.root); removeErr != nil {
			fmt.Fprintf(os.Stderr, "remove hermetic test layout: %v\n", removeErr)
			if code == 0 {
				code = 1
			}
		}
	}()

	commandContext, stopSignals := signal.NotifyContext(
		context.Background(),
		hermeticRunnerSignals()...,
	)
	defer stopSignals()

	build := exec.CommandContext(
		commandContext,
		goBinary,
		"build",
		"-tags", coreBuildTags,
		"-o", layout.binary,
		"./cmd/picoclaw",
	)
	build.Dir = repositoryRoot
	build.Env = layout.environment
	build.Stdin = os.Stdin
	build.Stdout = os.Stdout
	build.Stderr = os.Stderr
	if err = runHermeticCommand(build); err != nil {
		fmt.Fprintf(os.Stderr, "build isolated PicoClaw binary: %v\n", err)
		return exitCode(err)
	}

	command := exec.CommandContext(commandContext, arguments[0], arguments[1:]...)
	command.Env = layout.environment
	command.Stdin = os.Stdin
	command.Stdout = os.Stdout
	command.Stderr = os.Stderr
	err = runHermeticCommand(command)
	if err != nil {
		return exitCode(err)
	}
	return 0
}

func runHermeticCommand(command *exec.Cmd) error {
	if err := configureHermeticCommand(command); err != nil {
		return err
	}
	command.Cancel = func() error { return terminateHermeticCommand(command) }
	command.WaitDelay = 5 * time.Second
	if err := command.Start(); err != nil {
		cleanupHermeticCommand(command)
		return err
	}
	if err := attachHermeticCommand(command); err != nil {
		_ = terminateHermeticCommand(command)
		_ = command.Wait()
		cleanupHermeticCommand(command)
		return err
	}
	err := command.Wait()
	// A successful command may still leave background descendants. The command
	// owns a dedicated process group/job; close that authority before deleting
	// its runtime tree.
	cleanupHermeticCommand(command)
	return err
}

func repositoryRoot() (string, error) {
	_, sourcePath, _, ok := runtime.Caller(0)
	if !ok {
		return "", errors.New("runner source path is unavailable")
	}
	root := filepath.Clean(filepath.Join(filepath.Dir(sourcePath), "..", ".."))
	if _, err := os.Stat(filepath.Join(root, "go.mod")); err != nil {
		return "", err
	}
	return root, nil
}

func goExecutable() (string, error) {
	if configured := strings.TrimSpace(os.Getenv(testGoEnv)); configured != "" {
		absolute, err := filepath.Abs(configured)
		if err != nil {
			return "", err
		}
		info, err := os.Stat(absolute)
		if err != nil || !info.Mode().IsRegular() || info.Mode()&0o111 == 0 {
			return "", fmt.Errorf("configured Go executable is invalid: %s", absolute)
		}
		return absolute, nil
	}
	path, err := exec.LookPath("go")
	if err != nil {
		return "", errors.New("go executable is unavailable")
	}
	return path, nil
}

type goCaches struct {
	build   string
	modules string
}

func resolveGoCaches(goBinary string) (goCaches, error) {
	caches := goCaches{
		build:   strings.TrimSpace(os.Getenv("GOCACHE")),
		modules: strings.TrimSpace(os.Getenv("GOMODCACHE")),
	}
	if caches.build != "" && caches.modules != "" {
		return caches, nil
	}
	command := exec.Command(goBinary, "env", "-json", "GOCACHE", "GOMODCACHE")
	output, err := command.Output()
	if err != nil {
		return goCaches{}, err
	}
	var discovered struct {
		Build   string `json:"GOCACHE"`
		Modules string `json:"GOMODCACHE"`
	}
	if err = json.Unmarshal(output, &discovered); err != nil {
		return goCaches{}, err
	}
	if caches.build == "" {
		caches.build = discovered.Build
	}
	if caches.modules == "" {
		caches.modules = discovered.Modules
	}
	if caches.build == "" || caches.modules == "" {
		return goCaches{}, errors.New("Go cache paths are incomplete")
	}
	return caches, nil
}

func createTestLayout(caches goCaches) (testLayout, error) {
	root, err := os.MkdirTemp("", "picoclaw-go-test-")
	if err != nil {
		return testLayout{}, err
	}
	failed := true
	defer func() {
		if failed {
			_ = os.RemoveAll(root)
		}
	}()

	layout := testLayout{
		root:      root,
		home:      filepath.Join(root, "home"),
		picoHome:  filepath.Join(root, "picoclaw"),
		workspace: filepath.Join(root, "picoclaw", "workspace"),
		binary:    filepath.Join(root, "bin", executableName("picoclaw")),
	}
	layout.configPath = filepath.Join(layout.picoHome, "config.json")
	layout.eventDB = filepath.Join(layout.workspace, "eventing", "events.db")

	directories := []string{
		layout.home,
		layout.picoHome,
		layout.workspace,
		filepath.Dir(layout.eventDB),
		filepath.Dir(layout.binary),
		filepath.Join(root, "xdg", "config"),
		filepath.Join(root, "xdg", "data"),
		filepath.Join(root, "xdg", "cache"),
		filepath.Join(root, "xdg", "state"),
		filepath.Join(root, "xdg", "runtime"),
		filepath.Join(root, "codex"),
		filepath.Join(root, "claude"),
		filepath.Join(root, "openclaw"),
		filepath.Join(root, "gnupg"),
		filepath.Join(root, "tmp"),
		filepath.Join(layout.home, "AppData", "Roaming"),
		filepath.Join(layout.home, "AppData", "Local"),
	}
	for _, directory := range directories {
		if err = os.MkdirAll(directory, 0o700); err != nil {
			return testLayout{}, err
		}
	}

	port, err := reserveLoopbackPort()
	if err != nil {
		return testLayout{}, err
	}
	configFile := testConfig{}
	configFile.Agents.Defaults.Workspace = layout.workspace
	configFile.Gateway.Host = "127.0.0.1"
	configFile.Gateway.Port = port
	configFile.Events.Ingress.DatabasePath = layout.eventDB
	configData, err := json.MarshalIndent(configFile, "", "  ")
	if err != nil {
		return testLayout{}, err
	}
	configData = append(configData, '\n')
	if err = os.WriteFile(layout.configPath, configData, 0o600); err != nil {
		return testLayout{}, err
	}
	database, err := os.OpenFile(layout.eventDB, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return testLayout{}, err
	}
	if err = database.Close(); err != nil {
		return testLayout{}, err
	}

	layout.environment = isolatedEnvironment(layout, caches)
	failed = false
	return layout, nil
}

func reserveLoopbackPort() (int, error) {
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

func isolatedEnvironment(layout testLayout, caches goCaches) []string {
	remove := func(name string) bool {
		upper := strings.ToUpper(name)
		return strings.HasPrefix(upper, "PICOCLAW_") ||
			isAmbientTestCredentialOrAuthority(upper) ||
			strings.HasPrefix(upper, "XDG_") ||
			upper == "HOME" || upper == "USERPROFILE" ||
			upper == "CODEX_HOME" || upper == "CLAUDE_CONFIG_DIR" ||
			upper == "OPENCLAW_HOME" || upper == "GNUPGHOME" ||
			upper == "GIT_CONFIG_GLOBAL" || upper == "GIT_CONFIG_NOSYSTEM" ||
			upper == "GOCACHE" || upper == "GOMODCACHE" ||
			upper == "TMPDIR" || upper == "TEMP" || upper == "TMP" ||
			upper == "DBUS_SESSION_BUS_ADDRESS" || upper == "APPDATA" ||
			upper == "LOCALAPPDATA" || upper == "HOMEDRIVE" || upper == "HOMEPATH"
	}
	environment := make([]string, 0, len(os.Environ())+24)
	for _, entry := range os.Environ() {
		name, _, ok := strings.Cut(entry, "=")
		if ok && remove(name) {
			continue
		}
		environment = append(environment, entry)
	}
	values := map[string]string{
		"HOME":                      layout.home,
		"USERPROFILE":               layout.home,
		"XDG_CONFIG_HOME":           filepath.Join(layout.root, "xdg", "config"),
		"XDG_DATA_HOME":             filepath.Join(layout.root, "xdg", "data"),
		"XDG_CACHE_HOME":            filepath.Join(layout.root, "xdg", "cache"),
		"XDG_STATE_HOME":            filepath.Join(layout.root, "xdg", "state"),
		"XDG_RUNTIME_DIR":           filepath.Join(layout.root, "xdg", "runtime"),
		"PICOCLAW_HOME":             layout.picoHome,
		"PICOCLAW_CONFIG":           layout.configPath,
		"PICOCLAW_BINARY":           layout.binary,
		"PICOCLAW_TEST_ROOT":        layout.root,
		"PICOCLAW_TEST_CONFIG":      layout.configPath,
		"PICOCLAW_TEST_EVENT_DB":    layout.eventDB,
		"CODEX_HOME":                filepath.Join(layout.root, "codex"),
		"CLAUDE_CONFIG_DIR":         filepath.Join(layout.root, "claude"),
		"OPENCLAW_HOME":             filepath.Join(layout.root, "openclaw"),
		"GNUPGHOME":                 filepath.Join(layout.root, "gnupg"),
		"GIT_CONFIG_GLOBAL":         filepath.Join(layout.root, "gitconfig"),
		"GIT_CONFIG_NOSYSTEM":       "1",
		"TMPDIR":                    filepath.Join(layout.root, "tmp"),
		"TEMP":                      filepath.Join(layout.root, "tmp"),
		"TMP":                       filepath.Join(layout.root, "tmp"),
		"DBUS_SESSION_BUS_ADDRESS":  "unix:path=" + filepath.Join(layout.root, "no-systemd-bus"),
		"APPDATA":                   filepath.Join(layout.home, "AppData", "Roaming"),
		"LOCALAPPDATA":              filepath.Join(layout.home, "AppData", "Local"),
		"AWS_EC2_METADATA_DISABLED": "true",
		"GIT_TERMINAL_PROMPT":       "0",
		"GCM_INTERACTIVE":           "never",
		"GOAUTH":                    "off",
		"GOENV":                     "off",
		"GOCACHE":                   caches.build,
		"GOMODCACHE":                caches.modules,
	}
	if runtime.GOOS == "windows" {
		volume := filepath.VolumeName(layout.home)
		values["HOMEDRIVE"] = volume
		values["HOMEPATH"] = strings.TrimPrefix(layout.home, volume)
	}
	for name, value := range values {
		environment = append(environment, name+"="+value)
	}
	return environment
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

func executableName(base string) string {
	if runtime.GOOS == "windows" {
		return base + ".exe"
	}
	return base
}

func exitCode(err error) int {
	var exitError *exec.ExitError
	if errors.As(err, &exitError) {
		if code := exitError.ExitCode(); code > 0 {
			return code
		}
	}
	return 1
}
