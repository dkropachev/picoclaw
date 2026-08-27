package isolation

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/sipeed/picoclaw/pkg/config"
)

func TestCapturedPolicyEnvironmentDefaultsExcludeAmbientAuthority(t *testing.T) {
	captured := capturePolicyEnvironment(
		config.IsolationConfig{},
		[]string{
			"PATH=/usr/bin:/bin",
			"HOME=/captured/home",
			"LANG=C.UTF-8",
			"OPENAI_API_KEY=secret",
			"HTTP_PROXY=http://user:password@proxy.invalid",
			"SSH_AUTH_SOCK=/tmp/agent.sock",
			"LD_PRELOAD=/tmp/inject.so",
			"NODE_OPTIONS=--require=/tmp/inject.js",
			"ARBITRARY_SECRET=secret",
		},
		"linux",
	)
	if captured.err != nil {
		t.Fatal(captured.err)
	}
	want := []string{
		"HOME=/captured/home",
		"LANG=C.UTF-8",
		"PATH=/usr/bin:/bin",
	}
	if !reflect.DeepEqual(captured.allowed, want) {
		t.Fatalf("captured allowed = %#v, want %#v", captured.allowed, want)
	}
	if captured.hostPath != "/usr/bin:/bin" {
		t.Fatalf("captured host path = %q", captured.hostPath)
	}
}

func TestExecutionPolicyMetadataHelpers(t *testing.T) {
	var zero ExecutionPolicy
	if !errors.Is(zero.Validate(), ErrExecutionPolicyUnavailable) {
		t.Fatalf("zero metadata validation = %v", zero.Validate())
	}
	if _, ok := zero.LookupEnvironment("PATH"); ok {
		t.Fatal("zero policy exposed environment")
	}

	disabled := newExecutionPolicyWithEnvironment(
		config.IsolationConfig{EnvironmentAllowlist: []string{"P014_EMPTY", "P014_VALUE"}},
		[]string{"P014_EMPTY=", "P014_VALUE=captured"},
		"linux",
	)
	if err := disabled.Validate(); err != nil {
		t.Fatalf("disabled metadata validation = %v", err)
	}
	if value, ok := disabled.LookupEnvironment("P014_EMPTY"); !ok || value != "" {
		t.Fatalf("explicit empty lookup = %q, %t", value, ok)
	}
	if value, ok := disabled.LookupEnvironment("P014_VALUE"); !ok || value != "captured" {
		t.Fatalf("captured lookup = %q, %t", value, ok)
	}
	if _, ok := disabled.LookupEnvironment("P014_MISSING"); ok {
		t.Fatal("missing value reported present")
	}

	enabled := newExecutionPolicyWithEnvironment(
		config.IsolationConfig{Enabled: true, EnvironmentAllowlist: []string{}},
		nil,
		"linux",
	)
	if detached, ok := enabled.detachedIsolation(); !ok || !detached.Enabled {
		t.Fatal("enabled metadata reported disabled")
	}

	invalid := newExecutionPolicyWithEnvironment(
		config.IsolationConfig{EnvironmentAllowlist: []string{"BAD-NAME"}},
		nil,
		"linux",
	)
	if err := invalid.Validate(); err == nil {
		t.Fatal("invalid policy validation error = nil")
	}
	if _, ok := invalid.LookupEnvironment("BAD-NAME"); ok {
		t.Fatal("invalid policy exposed environment")
	}
}

func TestExecutionPolicyLookupEnvironmentWindowsCaseFold(t *testing.T) {
	policy := newExecutionPolicyWithEnvironment(
		config.IsolationConfig{EnvironmentAllowlist: []string{"Path"}},
		[]string{`pAtH=C:\Tools`, `SystemRoot=C:\Windows`},
		"windows",
	)
	if err := policy.Validate(); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"PATH", "Path", "path"} {
		value, ok := policy.LookupEnvironment(name)
		if !ok || value != `C:\Tools` {
			t.Fatalf("LookupEnvironment(%q) = %q, %t", name, value, ok)
		}
	}
}

func TestExecutionPolicyLookupCommandEnvironment(t *testing.T) {
	working := t.TempDir()
	var zero ExecutionPolicy
	if _, _, err := zero.LookupCommandEnvironment("HOME", working); !errors.Is(err, ErrExecutionPolicyUnavailable) {
		t.Fatalf("zero command environment error = %v", err)
	}
	disabled := newExecutionPolicyWithEnvironment(
		config.IsolationConfig{EnvironmentAllowlist: []string{"HOME"}},
		[]string{"HOME=/captured/home"},
		runtime.GOOS,
	)
	if value, present, err := disabled.LookupCommandEnvironment(
		"HOME",
		working,
	); err != nil || !present || value != "/captured/home" {
		t.Fatalf("disabled command HOME = %q, %t, %v", value, present, err)
	}
	if value, present, err := disabled.LookupCommandEnvironment(
		"PWD",
		working,
	); err != nil || !present || value != filepath.Clean(working) {
		t.Fatalf("disabled command PWD = %q, %t, %v", value, present, err)
	}
	root := t.TempDir()
	t.Setenv(config.EnvHome, root)
	enabled := NewExecutionPolicy(config.IsolationConfig{
		Enabled:              true,
		EnvironmentAllowlist: []string{},
	})
	value, present, err := enabled.LookupCommandEnvironment("HOME", working)
	if err != nil || !present || value != filepath.Join(root, "runtime-user-env", "home") {
		t.Fatalf("enabled command HOME = %q, %t, %v", value, present, err)
	}
}

func TestRestrictedEnvironmentExplicitEmptyAndPrecedence(t *testing.T) {
	captured := capturePolicyEnvironment(
		config.IsolationConfig{EnvironmentAllowlist: []string{}},
		[]string{"PATH=/ambient/bin", "HOME=/ambient/home", "TOKEN=secret"},
		"linux",
	)
	if captured.err != nil {
		t.Fatal(captured.err)
	}
	user := UserEnv{
		Home:   "/isolated/home",
		Tmp:    "/isolated/tmp",
		Config: "/isolated/config",
		Cache:  "/isolated/cache",
		State:  "/isolated/state",
	}
	got, err := restrictedEnvironmentForCommand(
		captured,
		[]string{
			"PATH=/explicit/bin",
			"HOME=/explicit/home",
			"EMPTY=",
			"NAME=first",
			"NAME=second",
		},
		"/work",
		true,
		user,
	)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"EMPTY=",
		"HOME=/isolated/home",
		"NAME=second",
		"PATH=/explicit/bin",
		"PWD=/work",
		"TMPDIR=/isolated/tmp",
		"XDG_CACHE_HOME=/isolated/cache",
		"XDG_CONFIG_HOME=/isolated/config",
		"XDG_STATE_HOME=/isolated/state",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("restricted environment = %#v, want %#v", got, want)
	}
}

func TestExecutionPolicyEnvironmentSnapshotDetachedFromSources(t *testing.T) {
	allowlist := make([]string, 1, 4)
	allowlist[0] = "P014_ALLOWED"
	ambient := []string{"P014_ALLOWED=A", "P014_SECRET=secret"}
	policy := newExecutionPolicyWithEnvironment(
		config.IsolationConfig{EnvironmentAllowlist: allowlist},
		ambient,
		runtime.GOOS,
	)
	allowlist[0] = "P014_SECRET"
	extendedAllowlist := append(allowlist, "PATH")
	extendedAllowlist[1] = "P014_SPARE"
	ambient[0] = "P014_ALLOWED=B"

	snapshot, ok := policy.detachedSnapshot()
	if !ok {
		t.Fatal("policy snapshot unavailable")
	}
	got, err := restrictedEnvironmentForCommand(
		snapshot.environment,
		nil,
		"/work",
		false,
		UserEnv{},
	)
	if err != nil {
		t.Fatal(err)
	}
	if !environmentContains(got, "P014_ALLOWED=A") ||
		environmentHasKey(got, "P014_SECRET", runtime.GOOS) {
		t.Fatalf("detached environment = %#v", got)
	}
	detached, _ := policy.detachedIsolation()
	if !reflect.DeepEqual(detached.EnvironmentAllowlist, []string{"P014_ALLOWED"}) {
		t.Fatalf("detached allowlist = %#v", detached.EnvironmentAllowlist)
	}
}

func TestRestrictedEnvironmentRejectsInvalidAndBoundedExplicitInput(t *testing.T) {
	captured := capturePolicyEnvironment(
		config.IsolationConfig{EnvironmentAllowlist: []string{}},
		nil,
		"linux",
	)
	tests := make([]struct {
		name string
		env  []string
	}, 0, 8)
	tests = append(tests,
		struct {
			name string
			env  []string
		}{name: "missing separator", env: []string{"NAME"}},
		struct {
			name string
			env  []string
		}{name: "empty name", env: []string{"=value"}},
		struct {
			name string
			env  []string
		}{name: "invalid name", env: []string{"BAD-NAME=value"}},
		struct {
			name string
			env  []string
		}{name: "nul value", env: []string{"NAME=value\x00tail"}},
		struct {
			name string
			env  []string
		}{name: "invalid utf8", env: []string{"NAME=" + string([]byte{0xff})}},
		struct {
			name string
			env  []string
		}{name: "value too large", env: []string{"NAME=" + strings.Repeat("x", maximumEnvironmentValueBytes+1)}},
	)
	tooMany := make([]string, maximumEnvironmentEntries+1)
	for index := range tooMany {
		tooMany[index] = "NAME=small"
	}
	tests = append(tests, struct {
		name string
		env  []string
	}{name: "too many", env: tooMany})
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := restrictedEnvironmentForCommand(
				captured,
				test.env,
				"/work",
				false,
				UserEnv{},
			); err == nil {
				t.Fatal("restrictedEnvironmentForCommand() error = nil")
			}
		})
	}
}

func TestEnvironmentValidationAndProjectionFailureBranches(t *testing.T) {
	invalidCaptured := capturedPolicyEnvironment{
		goos:    "linux",
		allowed: []string{"malformed"},
	}
	if _, err := restrictedEnvironmentForCommand(
		invalidCaptured,
		nil,
		t.TempDir(),
		false,
		UserEnv{},
	); err == nil || !strings.Contains(err.Error(), "captured") {
		t.Fatalf("invalid captured environment error = %v", err)
	}

	capturedValues := make([]string, maximumPolicyEnvironmentNames)
	for index := range capturedValues {
		capturedValues[index] = fmt.Sprintf("CAPTURED_%03d=value", index)
	}
	explicit := make([]string, maximumPolicyEnvironmentNames)
	for index := range explicit {
		explicit[index] = fmt.Sprintf("EXPLICIT_%03d=value", index)
	}
	if _, err := restrictedEnvironmentForCommand(
		capturedPolicyEnvironment{goos: "linux", allowed: capturedValues},
		explicit,
		t.TempDir(),
		false,
		UserEnv{},
	); err == nil || !strings.Contains(err.Error(), "entries") {
		t.Fatalf("merged entry-count error = %v", err)
	}

	tooManyFinalEntries := make([]string, maximumEnvironmentEntries+1)
	if err := validateFinalEnvironment(tooManyFinalEntries); err == nil ||
		!strings.Contains(err.Error(), "entries") {
		t.Fatalf("final entry-count validation = %v", err)
	}
	for _, environment := range [][]string{
		{"malformed"},
		{"BAD-NAME=value"},
		{"NAME=" + string([]byte{0xff})},
		{"NAME=" + strings.Repeat("x", maximumEnvironmentValueBytes+1)},
	} {
		if err := validateFinalEnvironment(environment); err == nil {
			t.Fatalf("validateFinalEnvironment(%#v) error = nil", environment)
		}
	}
	largeEnvironment := []string{
		"A=" + strings.Repeat("a", 12*1024),
		"B=" + strings.Repeat("b", 12*1024),
	}
	if err := validateFinalEnvironment(largeEnvironment); err == nil ||
		!strings.Contains(err.Error(), "encoded bytes") {
		t.Fatalf("aggregate environment validation = %v", err)
	}

	for _, name := range []string{"", "1NAME", "BAD-NAME", "NÄME", strings.Repeat("A", maximumEnvironmentNameBytes+1)} {
		if validEnvironmentName(name) {
			t.Fatalf("validEnvironmentName(%q) = true", name)
		}
	}
	if !validEnvironmentName("_Mixed_123") {
		t.Fatal("portable mixed-case environment name rejected")
	}

	if err := validatePrivateLookupEnvironment(
		"PATH",
		strings.Repeat("x", maximumEnvironmentValueBytes+1),
	); err == nil {
		t.Fatal("oversized private lookup accepted")
	}
	if err := validatePrivateLookupEnvironment("PATH", string([]byte{0xff})); err == nil {
		t.Fatal("invalid UTF-8 private lookup accepted")
	}
}

func TestCapturePolicyEnvironmentRejectsCapturedValueBounds(t *testing.T) {
	policy := capturePolicyEnvironment(
		config.IsolationConfig{EnvironmentAllowlist: []string{"HOME"}},
		[]string{"HOME=" + strings.Repeat("x", maximumEnvironmentValueBytes+1)},
		"linux",
	)
	if policy.err == nil || !strings.Contains(policy.err.Error(), "capture isolation environment") {
		t.Fatalf("oversized captured value error = %v", policy.err)
	}

	invalidLookup := capturePolicyEnvironment(
		config.IsolationConfig{EnvironmentAllowlist: []string{}},
		[]string{"PATH=" + string([]byte{0xff})},
		"synthetic",
	)
	if invalidLookup.err == nil || !strings.Contains(invalidLookup.err.Error(), "PATH") {
		t.Fatalf("invalid lookup capture error = %v", invalidLookup.err)
	}
}

func TestEffectiveCommandDirectoryRelative(t *testing.T) {
	workingDirectory, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	got, err := effectiveCommandDirectory("relative/child", runtime.GOOS)
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(workingDirectory, "relative", "child")
	if got != filepath.Clean(want) {
		t.Fatalf("relative effective directory = %q, want %q", got, want)
	}
	if drive := windowsSystemDrive("relative"); drive != "" {
		t.Fatalf("relative Windows system drive = %q", drive)
	}
	if path := windowsHomePath("relative"); path != "relative" {
		t.Fatalf("relative Windows home path = %q", path)
	}
	if path := windowsHomePath("C:"); path != `\` {
		t.Fatalf("drive-only Windows home path = %q", path)
	}
}

func TestCapturedWindowsEnvironmentCanonicalAndReserved(t *testing.T) {
	captured := capturePolicyEnvironment(
		config.IsolationConfig{EnvironmentAllowlist: []string{
			"PATH", "HOME", "TEMP",
		}},
		[]string{
			`Path=C:\Tools;C:\Windows\System32`,
			`home=C:\Users\captured`,
			`Temp=C:\AmbientTemp`,
			`SystemRoot=C:\Windows`,
			`TOKEN=secret`,
		},
		"windows",
	)
	if captured.err != nil {
		t.Fatal(captured.err)
	}
	got, err := restrictedEnvironmentForCommand(
		captured,
		[]string{
			`home=C:\Explicit`,
			`temp=C:\ExplicitTemp`,
			`systemroot=Z:\Untrusted`,
			"EMPTY=",
		},
		`C:\Work`,
		true,
		UserEnv{
			Home:         `D:\Runtime\home`,
			Tmp:          `D:\Runtime\tmp`,
			AppData:      `D:\Runtime\AppData\Roaming`,
			LocalAppData: `D:\Runtime\AppData\Local`,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	wantEntries := []string{
		`APPDATA=D:\Runtime\AppData\Roaming`,
		`COMSPEC=C:\Windows\System32\cmd.exe`,
		"EMPTY=",
		`HOME=D:\Runtime\home`,
		`HOMEDRIVE=D:`,
		`HOMEPATH=\Runtime\home`,
		`LOCALAPPDATA=D:\Runtime\AppData\Local`,
		`NODEFAULTCURRENTDIRECTORYINEXEPATH=1`,
		`PATH=C:\Tools;C:\Windows\System32`,
		`PWD=C:\Work`,
		`SYSTEMDRIVE=C:`,
		`SYSTEMROOT=C:\Windows`,
		`TEMP=D:\Runtime\tmp`,
		`TMP=D:\Runtime\tmp`,
		`USERPROFILE=D:\Runtime\home`,
		`WINDIR=C:\Windows`,
	}
	if !reflect.DeepEqual(got, wantEntries) {
		t.Fatalf("Windows environment = %#v, want %#v", got, wantEntries)
	}
	if !sort.StringsAreSorted(got) || environmentHasKey(got, "TOKEN", "windows") {
		t.Fatalf("Windows environment not canonical: %#v", got)
	}
}

func TestCapturedWindowsEnvironmentRequiresSystemRoot(t *testing.T) {
	for _, systemRoot := range []string{"", `Windows`, `C:Windows`, "C:\\Win\x00dows"} {
		ambient := []string{`PATH=C:\Windows\System32`}
		if systemRoot != "" {
			ambient = append(ambient, "SYSTEMROOT="+systemRoot)
		}
		captured := capturePolicyEnvironment(
			config.IsolationConfig{EnvironmentAllowlist: []string{}},
			ambient,
			"windows",
		)
		if captured.err == nil || !strings.Contains(captured.err.Error(), "SYSTEMROOT") {
			t.Fatalf("SYSTEMROOT %q capture error = %v", systemRoot, captured.err)
		}
	}
}

func TestExecutionPolicyFrozenPathSelectsExactConcurrentExecutable(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix helper script test")
	}
	dirA := t.TempDir()
	dirB := t.TempDir()
	writePolicyExecutable(t, dirA, "p014-frozen-helper", "A")
	writePolicyExecutable(t, dirB, "p014-frozen-helper", "B")
	policyA := newExecutionPolicyWithEnvironment(
		config.IsolationConfig{EnvironmentAllowlist: []string{"PATH", "HOME"}},
		[]string{"PATH=" + dirA, "HOME=/home/a", "TOKEN=secret-a"},
		runtime.GOOS,
	)
	policyB := newExecutionPolicyWithEnvironment(
		config.IsolationConfig{EnvironmentAllowlist: []string{"PATH", "HOME"}},
		[]string{"PATH=" + dirB, "HOME=/home/b", "TOKEN=secret-b"},
		runtime.GOOS,
	)

	type launchCase struct {
		policy isolationPolicyRunner
		want   string
	}
	cases := []launchCase{{policy: policyA, want: "A"}, {policy: policyB, want: "B"}}
	var workers sync.WaitGroup
	errorsSeen := make(chan error, 20)
	for iteration := 0; iteration < 10; iteration++ {
		for _, test := range cases {
			workers.Add(1)
			go func() {
				defer workers.Done()
				cmd := exec.Command("p014-frozen-helper")
				var stdout bytes.Buffer
				cmd.Stdout = &stdout
				if err := test.policy.Run(cmd); err != nil {
					errorsSeen <- err
					return
				}
				if got := strings.TrimSpace(stdout.String()); got != test.want {
					errorsSeen <- errors.New("wrong helper output: " + got)
					return
				}
				if environmentHasKey(cmd.Env, "TOKEN", runtime.GOOS) {
					errorsSeen <- errors.New("ambient token crossed")
				}
			}()
		}
	}
	workers.Wait()
	close(errorsSeen)
	for err := range errorsSeen {
		t.Error(err)
	}
}

func TestExecutionPolicyExplicitPathOverridesCapturedPathCoherently(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix helper script test")
	}
	dirA := t.TempDir()
	dirB := t.TempDir()
	writePolicyExecutable(t, dirA, "p014-override-helper", "A")
	writePolicyExecutable(t, dirB, "p014-override-helper", "B")
	policy := newExecutionPolicyWithEnvironment(
		config.IsolationConfig{EnvironmentAllowlist: []string{"PATH"}},
		[]string{"PATH=" + dirA},
		runtime.GOOS,
	)
	cmd := exec.Command("p014-override-helper")
	cmd.Env = []string{"PATH=" + dirB}
	var stdout bytes.Buffer
	cmd.Stdout = &stdout
	if err := policy.Run(cmd); err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(stdout.String()); got != "B" {
		t.Fatalf("helper output = %q, want B", got)
	}
	if value := environmentSliceValue(cmd.Env, "PATH", runtime.GOOS); value != dirB {
		t.Fatalf("child PATH = %q, want %q", value, dirB)
	}
}

func TestExecutionPolicyPathSkipsEmptyAndRelativeEntries(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix helper script test")
	}
	working := t.TempDir()
	admitted := t.TempDir()
	writePolicyExecutable(t, working, "p014-relative-helper", "WRONG")
	writePolicyExecutable(t, admitted, "p014-relative-helper", "RIGHT")
	policy := newExecutionPolicyWithEnvironment(
		config.IsolationConfig{EnvironmentAllowlist: []string{"PATH"}},
		[]string{"PATH=:." + string(os.PathListSeparator) + admitted},
		runtime.GOOS,
	)
	cmd := exec.Command("p014-relative-helper")
	cmd.Dir = working
	var stdout bytes.Buffer
	cmd.Stdout = &stdout
	if err := policy.Run(cmd); err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(stdout.String()); got != "RIGHT" {
		t.Fatalf("helper output = %q, want RIGHT", got)
	}
	if got := environmentSliceValue(cmd.Env, "PATH", runtime.GOOS); got != admitted {
		t.Fatalf("normalized child PATH = %q, want %q", got, admitted)
	}
}

func TestExecutionPolicyNormalizedPathGovernsDescendants(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix shell descendant test")
	}
	working := t.TempDir()
	admitted := t.TempDir()
	writePolicyExecutable(t, working, "p014-descendant-helper", "WRONG")
	writePolicyExecutable(t, admitted, "p014-descendant-helper", "RIGHT")
	top := filepath.Join(admitted, "p014-parent-helper")
	if err := os.WriteFile(
		top,
		[]byte("#!/bin/sh\np014-descendant-helper\n"),
		0o700,
	); err != nil {
		t.Fatal(err)
	}
	policy := newExecutionPolicyWithEnvironment(
		config.IsolationConfig{EnvironmentAllowlist: []string{"PATH"}},
		[]string{"PATH=:." + string(os.PathListSeparator) + admitted},
		runtime.GOOS,
	)
	cmd := exec.Command("p014-parent-helper")
	cmd.Dir = working
	var stdout bytes.Buffer
	cmd.Stdout = &stdout
	if err := policy.Run(cmd); err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(stdout.String()); got != "RIGHT" {
		t.Fatalf("descendant helper output = %q, want RIGHT", got)
	}
}

func TestExecutionPolicyExplicitRelativeExecutableKeepsDirSemantics(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix helper script test")
	}
	dir := t.TempDir()
	writePolicyExecutable(t, dir, "p014-explicit-helper", "RELATIVE")
	policy := newExecutionPolicyWithEnvironment(
		config.IsolationConfig{EnvironmentAllowlist: []string{}},
		nil,
		runtime.GOOS,
	)
	cmd := exec.Command("./p014-explicit-helper")
	cmd.Dir = dir
	var stdout bytes.Buffer
	cmd.Stdout = &stdout
	if err := policy.Run(cmd); err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(stdout.String()); got != "RELATIVE" {
		t.Fatalf("helper output = %q", got)
	}
}

func TestEnvironmentProjectionDefensiveBranches(t *testing.T) {
	t.Run("malformed ambient entry is ignored", func(t *testing.T) {
		captured := capturePolicyEnvironment(
			config.IsolationConfig{EnvironmentAllowlist: []string{}},
			[]string{"malformed"},
			"linux",
		)
		if captured.err != nil {
			t.Fatal(captured.err)
		}
		if len(captured.allowed) != 0 {
			t.Fatalf("captured environment = %#v, want empty", captured.allowed)
		}
	})

	t.Run("invalid private PATHEXT fails capture and projection", func(t *testing.T) {
		captured := capturePolicyEnvironment(
			config.IsolationConfig{EnvironmentAllowlist: []string{}},
			[]string{"PATHEXT=" + string([]byte{0xff})},
			"synthetic",
		)
		if captured.err == nil || !strings.Contains(captured.err.Error(), "PATHEXT") {
			t.Fatalf("captured PATHEXT error = %v", captured.err)
		}
		if _, err := restrictedEnvironmentForCommand(
			captured,
			nil,
			"/work",
			false,
			UserEnv{},
		); err == nil || !strings.Contains(err.Error(), "PATHEXT") {
			t.Fatalf("restricted environment error = %v", err)
		}
	})

	t.Run("aggregate explicit environment bound", func(t *testing.T) {
		captured := capturePolicyEnvironment(
			config.IsolationConfig{EnvironmentAllowlist: []string{}},
			nil,
			"linux",
		)
		_, err := restrictedEnvironmentForCommand(
			captured,
			[]string{
				"FIRST=" + strings.Repeat("a", 13*1024),
				"SECOND=" + strings.Repeat("b", 13*1024),
			},
			"/work",
			false,
			UserEnv{},
		)
		if err == nil || !strings.Contains(err.Error(), "encoded bytes") {
			t.Fatalf("aggregate explicit environment error = %v", err)
		}
	})

	t.Run("Windows drive root is canonical", func(t *testing.T) {
		got, err := effectiveCommandDirectory("c:/", "windows")
		if err != nil {
			t.Fatal(err)
		}
		if got != `C:\` {
			t.Fatalf("effective Windows drive root = %q, want C:\\\\", got)
		}
	})

	t.Run("Unix executable validation and PATH filtering", func(t *testing.T) {
		if runtime.GOOS == "windows" {
			t.Skip("Unix executable resolver")
		}
		if _, err := resolveExecutablePath("", "", "", false); err == nil ||
			!strings.Contains(err.Error(), "invalid") {
			t.Fatalf("empty executable error = %v", err)
		}
		pathValue := string(os.PathListSeparator) + "relative" +
			string(os.PathListSeparator) + t.TempDir()
		if _, err := resolveExecutablePath(
			"p014-definitely-missing",
			pathValue,
			"",
			false,
		); !errors.Is(err, exec.ErrNotFound) {
			t.Fatalf("filtered PATH lookup error = %v, want ErrNotFound", err)
		}
	})
}

type isolationPolicyRunner interface {
	Run(cmd *exec.Cmd) error
}

func writePolicyExecutable(t *testing.T, dir, name, output string) {
	t.Helper()
	path := filepath.Join(dir, name)
	data := []byte("#!/bin/sh\nprintf '%s\\n' '" + output + "'\n")
	if err := os.WriteFile(path, data, 0o700); err != nil {
		t.Fatal(err)
	}
}

func environmentContains(environment []string, want string) bool {
	for _, item := range environment {
		if item == want {
			return true
		}
	}
	return false
}

func environmentHasKey(environment []string, key, goos string) bool {
	canonical := canonicalEnvironmentKey(key, goos)
	for _, item := range environment {
		name, _, ok := splitEnvironmentEntry(item)
		if ok && canonicalEnvironmentKey(name, goos) == canonical {
			return true
		}
	}
	return false
}
