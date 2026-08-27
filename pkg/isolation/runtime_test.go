package isolation

import (
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"testing"

	"github.com/sipeed/picoclaw/pkg"
	"github.com/sipeed/picoclaw/pkg/config"
)

func TestResolveInstanceRoot_UsesPicoclawHome(t *testing.T) {
	t.Setenv(config.EnvHome, "/custom/picoclaw/home")
	root, err := ResolveInstanceRoot()
	if err != nil {
		t.Fatalf("ResolveInstanceRoot() error = %v", err)
	}
	if root != "/custom/picoclaw/home" {
		t.Fatalf("ResolveInstanceRoot() = %q, want %q", root, "/custom/picoclaw/home")
	}
}

func TestResolveInstanceRoot_RejectsRelativeHome(t *testing.T) {
	t.Setenv(config.EnvHome, filepath.Join("relative", "picoclaw"))
	if _, err := ResolveInstanceRoot(); err == nil {
		t.Fatal("ResolveInstanceRoot() accepted a relative instance root")
	}
}

func TestPrepareInstanceRoot_CreatesDirectories(t *testing.T) {
	root := filepath.Join(t.TempDir(), "instance")
	if err := PrepareInstanceRoot(root); err != nil {
		t.Fatalf("PrepareInstanceRoot() error = %v", err)
	}
	for _, dir := range InstanceDirs(root) {
		if info, err := os.Stat(dir); err != nil {
			t.Fatalf("os.Stat(%q): %v", dir, err)
		} else if !info.IsDir() {
			t.Fatalf("%q is not a directory", dir)
		}
	}
}

func TestInstanceDirs_UsesInstanceWorkspaceNotGlobalState(t *testing.T) {
	root := filepath.Join(t.TempDir(), "instance")
	cfg := config.DefaultConfig()
	cfg.Isolation.Enabled = true
	cfg.Agents.Defaults.Workspace = filepath.Join(t.TempDir(), "external-workspace")
	Configure(cfg)
	t.Cleanup(func() { Configure(config.DefaultConfig()) })

	dirs := InstanceDirs(root)
	wantWorkspace := filepath.Join(root, pkg.WorkspaceName)
	found := false
	for _, dir := range dirs {
		if dir == wantWorkspace {
			found = true
		}
		if dir == cfg.WorkspacePath() {
			t.Fatalf("InstanceDirs() should not depend on process-wide workspace state: %q", dir)
		}
	}
	if !found {
		t.Fatalf("InstanceDirs() missing instance workspace dir %q", wantWorkspace)
	}
}

func TestIsSupportedOn(t *testing.T) {
	if got, want := IsSupported(), isSupportedOn(runtime.GOOS); got != want {
		t.Fatalf("IsSupported() = %v, want %v", got, want)
	}
	tests := []struct {
		goos string
		want bool
	}{
		{goos: "linux", want: true},
		{goos: "windows", want: true},
		{goos: "darwin", want: false},
		{goos: "freebsd", want: false},
		{goos: "netbsd", want: false},
	}
	for _, tt := range tests {
		if got := isSupportedOn(tt.goos); got != tt.want {
			t.Fatalf("isSupportedOn(%q) = %v, want %v", tt.goos, got, tt.want)
		}
	}
}

func TestValidateExposePaths(t *testing.T) {
	if err := ValidateExposePaths([]config.ExposePath{{
		Source: "/src", Target: "/dst", Mode: "ro",
	}}); err != nil {
		t.Fatalf("ValidateExposePaths() error = %v", err)
	}
	if err := ValidateExposePaths([]config.ExposePath{{
		Source: "/src", Mode: "rw",
	}}); err != nil {
		t.Fatalf("ValidateExposePaths() implicit target error = %v", err)
	}

	for _, test := range []struct {
		name  string
		items []config.ExposePath
	}{
		{
			name:  "empty source",
			items: []config.ExposePath{{Target: "/dst", Mode: "ro"}},
		},
		{
			name:  "invalid mode",
			items: []config.ExposePath{{Source: "/src", Target: "/dst", Mode: "bad"}},
		},
		{
			name:  "relative source",
			items: []config.ExposePath{{Source: "src", Target: "/dst", Mode: "ro"}},
		},
		{
			name:  "relative target",
			items: []config.ExposePath{{Source: "/src", Target: "dst", Mode: "ro"}},
		},
		{
			name:  "NUL source",
			items: []config.ExposePath{{Source: "/src\x00alias", Target: "/dst", Mode: "ro"}},
		},
		{
			name:  "NUL target",
			items: []config.ExposePath{{Source: "/src", Target: "/dst\x00alias", Mode: "ro"}},
		},
		{
			name: "normalized duplicate target",
			items: []config.ExposePath{
				{Source: "/src", Target: "/dst/../same", Mode: "ro"},
				{Source: "/other", Target: "/same", Mode: "rw"},
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			if err := ValidateExposePaths(test.items); err == nil {
				t.Fatal("ValidateExposePaths() accepted invalid input")
			}
		})
	}
}

func TestMergeExposePaths_OverrideByTarget(t *testing.T) {
	merged := MergeExposePaths(
		[]config.ExposePath{{Source: "/src-a", Target: "/dst", Mode: "ro"}},
		[]config.ExposePath{{Source: "/src-b", Target: "/dst", Mode: "rw"}},
	)
	if len(merged) != 1 {
		t.Fatalf("MergeExposePaths len = %d, want 1", len(merged))
	}
	if got := merged[0]; got.Source != "/src-b" || got.Target != "/dst" || got.Mode != "rw" {
		t.Fatalf("merged[0] = %+v, want source=/src-b target=/dst mode=rw", got)
	}
}

func TestNormalizeExposePath_DefaultsTargetToSource(t *testing.T) {
	got := NormalizeExposePath(config.ExposePath{Source: "/implicit/../source", Mode: "ro"})
	want := config.ExposePath{Source: "/source", Target: "/source", Mode: "ro"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("NormalizeExposePath() = %#v, want %#v", got, want)
	}
}

func TestMergeExposePaths_ReplacesInPlaceAndAppendsInOrder(t *testing.T) {
	merged := MergeExposePaths(
		[]config.ExposePath{
			{Source: "/root", Target: "/root", Mode: "rw"},
			{Source: "/system", Target: "/system", Mode: "ro"},
		},
		[]config.ExposePath{
			{Source: "/replacement", Target: "/root", Mode: "ro"},
			{Source: "/later-a", Target: "/later-a", Mode: "rw"},
			{Source: "/later-b", Target: "/later-b", Mode: "ro"},
		},
	)
	want := []config.ExposePath{
		{Source: "/replacement", Target: "/root", Mode: "ro"},
		{Source: "/system", Target: "/system", Mode: "ro"},
		{Source: "/later-a", Target: "/later-a", Mode: "rw"},
		{Source: "/later-b", Target: "/later-b", Mode: "ro"},
	}
	if !reflect.DeepEqual(merged, want) {
		t.Fatalf("MergeExposePaths() = %#v, want %#v", merged, want)
	}
}

func TestBuildLinuxMountPlan(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("linux-only default mount set")
	}
	plan := BuildLinuxMountPlan("/rootdir", []config.ExposePath{{Source: "/src", Target: "/dst", Mode: "ro"}})
	if len(plan) == 0 {
		t.Fatal("BuildLinuxMountPlan returned empty plan")
	}
	foundRoot := false
	foundOverride := false
	for _, rule := range plan {
		if rule.Source == "/rootdir" && rule.Target == "/rootdir" && rule.Mode == "rw" {
			foundRoot = true
		}
		if rule.Source == "/src" && rule.Target == "/dst" && rule.Mode == "ro" {
			foundOverride = true
		}
	}
	if !foundRoot {
		t.Fatal("BuildLinuxMountPlan missing root mapping")
	}
	if !foundOverride {
		t.Fatal("BuildLinuxMountPlan missing override mapping")
	}
}

func TestBuildLinuxMountPlan_FixedViewExactOrder(t *testing.T) {
	plan := buildLinuxMountPlan(
		[]config.ExposePath{
			{Source: "/root", Target: "/root", Mode: "rw"},
			{Source: "/system-a", Target: "/system-a", Mode: "ro"},
			{Source: "/system-b", Target: "/system-b", Mode: "ro"},
		},
		[]config.ExposePath{
			{Source: "/override-root", Target: "/root", Mode: "ro"},
			{Source: "/user", Target: "/user", Mode: "rw"},
		},
	)
	want := []MountRule{
		{Source: "/override-root", Target: "/root", Mode: "ro"},
		{Source: "/system-a", Target: "/system-a", Mode: "ro"},
		{Source: "/system-b", Target: "/system-b", Mode: "ro"},
		{Source: "/user", Target: "/user", Mode: "rw"},
	}
	if !reflect.DeepEqual(plan, want) {
		t.Fatalf("buildLinuxMountPlan() = %#v, want %#v", plan, want)
	}
}

func TestBuildWindowsAccessRules(t *testing.T) {
	rules := BuildWindowsAccessRules(
		`C:\picoclaw`,
		[]config.ExposePath{{Source: `D:\data`, Target: `C:\mapped`, Mode: "ro"}},
	)
	if len(rules) == 0 {
		t.Fatal("BuildWindowsAccessRules returned empty rules")
	}
	foundRoot := false
	foundOverride := false
	for _, rule := range rules {
		if rule.Path == `C:\picoclaw` && rule.Mode == "rw" {
			foundRoot = true
		}
		if rule.Path == `D:\data` && rule.Mode == "ro" {
			foundOverride = true
		}
	}
	if !foundRoot {
		t.Fatal("BuildWindowsAccessRules missing root rule")
	}
	if !foundOverride {
		t.Fatal("BuildWindowsAccessRules missing override rule")
	}
}

func TestBuildWindowsAccessRules_PreservesConfiguredOrder(t *testing.T) {
	rules := BuildWindowsAccessRules(
		`C:\picoclaw`,
		[]config.ExposePath{
			{Source: `D:\first`, Target: `C:\mapped-first`, Mode: "ro"},
			{Source: `E:\second`, Target: `C:\mapped-second`, Mode: "rw"},
		},
	)
	want := []AccessRule{
		{Path: `C:\picoclaw`, Mode: "rw"},
		{Path: `D:\first`, Mode: "ro"},
		{Path: `E:\second`, Mode: "rw"},
	}
	if !reflect.DeepEqual(rules, want) {
		t.Fatalf("BuildWindowsAccessRules() = %#v, want %#v", rules, want)
	}
}

func TestValidateWindowsExposePaths(t *testing.T) {
	if err := validateWindowsExposePaths(nil); err != nil {
		t.Fatalf("validateWindowsExposePaths(nil) error = %v", err)
	}
	err := validateWindowsExposePaths([]config.ExposePath{{Source: `D:\data`, Target: `D:\data`, Mode: "ro"}})
	if err == nil {
		t.Fatal("validateWindowsExposePaths() expected error for expose_paths")
	}
}

func TestDefaultLinuxSystemExposePaths(t *testing.T) {
	paths := defaultLinuxSystemExposePaths()
	needed := map[string]bool{}
	for _, path := range []string{"/etc/hosts", "/etc/nsswitch.conf", "/etc/ssl", "/usr/share/zoneinfo", "/etc/localtime"} {
		if _, err := os.Stat(path); err == nil {
			needed[path] = false
		}
	}
	for _, item := range paths {
		if _, ok := needed[item.Source]; ok {
			needed[item.Source] = true
		}
	}
	for path, found := range needed {
		if !found {
			t.Fatalf("defaultLinuxSystemExposePaths missing %s", path)
		}
	}
}

func TestExistingExposePaths_SkipsMissingPaths(t *testing.T) {
	existing := filepath.Join(t.TempDir(), "existing")
	if err := os.MkdirAll(existing, 0o755); err != nil {
		t.Fatalf("os.MkdirAll() error = %v", err)
	}
	filtered := existingExposePaths([]config.ExposePath{
		{Source: existing, Target: existing, Mode: "ro"},
		{Source: filepath.Join(t.TempDir(), "missing"), Target: "/missing", Mode: "ro"},
	})
	if len(filtered) != 1 {
		t.Fatalf("existingExposePaths() len = %d, want 1", len(filtered))
	}
	if got := filtered[0]; got.Source != existing {
		t.Fatalf("existingExposePaths()[0] = %+v, want source=%q", got, existing)
	}
}

func TestPrepareCommand_AppliesUserEnv(t *testing.T) {
	if !isSupportedOn(runtime.GOOS) {
		t.Skipf("isolation not supported on %s", runtime.GOOS)
	}
	t.Setenv(config.EnvHome, filepath.Join(t.TempDir(), "home"))
	if runtime.GOOS == "linux" {
		binDir := filepath.Join(t.TempDir(), "bin")
		if err := os.MkdirAll(binDir, 0o755); err != nil {
			t.Fatalf("os.MkdirAll() error = %v", err)
		}
		fakeBwrap := filepath.Join(binDir, "bwrap")
		if err := os.WriteFile(fakeBwrap, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
			t.Fatalf("os.WriteFile() error = %v", err)
		}
		t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	}
	cfg := config.DefaultConfig()
	cfg.Isolation.Enabled = true
	Configure(cfg)
	t.Cleanup(func() { Configure(config.DefaultConfig()) })
	cmd := exec.Command("sh", "-c", "true")
	if err := PrepareCommand(cmd); err != nil {
		t.Fatalf("PrepareCommand() error = %v", err)
	}
	hasHome := false
	for _, env := range cmd.Env {
		if len(env) > 5 && env[:5] == "HOME=" {
			hasHome = true
			break
		}
	}
	if runtime.GOOS != "windows" && !hasHome {
		t.Fatal("PrepareCommand() did not inject HOME")
	}
}
