//go:build linux

package isolation

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sipeed/picoclaw/pkg/config"
)

func TestBuildLinuxBwrapArgs_IncludesNamespaceFlagsAndExec(t *testing.T) {
	root := t.TempDir()
	binaryDir := filepath.Join(root, "bin")
	if err := os.MkdirAll(binaryDir, 0o755); err != nil {
		t.Fatal(err)
	}
	binaryPath := filepath.Join(binaryDir, "tool")
	if err := os.WriteFile(binaryPath, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	plan := BuildLinuxMountPlan(root, []config.ExposePath{{Source: binaryDir, Target: binaryDir, Mode: "ro"}})
	args, err := buildLinuxBwrapArgs(binaryPath, binaryPath, []string{binaryPath, "--flag"}, root, plan)
	if err != nil {
		t.Fatalf("buildLinuxBwrapArgs() error = %v", err)
	}
	hasNet := false
	hasIPC := false
	hasExec := false
	for i := range args {
		switch args[i] {
		case "--unshare-net":
			hasNet = true
		case "--unshare-ipc":
			hasIPC = true
		case "--":
			if i+1 < len(args) && args[i+1] == binaryPath {
				hasExec = true
			}
		}
	}
	if hasNet {
		t.Fatalf("bwrap args should not unshare net by default: %v", args)
	}
	if !hasIPC || !hasExec {
		t.Fatalf("bwrap args missing required items: %v", args)
	}
}

func TestResolveLinuxWorkingDir_ResolvesRelativeDir(t *testing.T) {
	cwd := t.TempDir()
	previous, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if chdirErr := os.Chdir(previous); chdirErr != nil {
			t.Fatalf("restore cwd: %v", chdirErr)
		}
	}()
	if chdirErr := os.Chdir(cwd); chdirErr != nil {
		t.Fatal(chdirErr)
	}

	resolvedDir, execDir, err := resolveLinuxWorkingDir("./hooks", "./hook.sh")
	if err != nil {
		t.Fatalf("resolveLinuxWorkingDir() error = %v", err)
	}
	want := filepath.Join(cwd, "hooks")
	if resolvedDir != want || execDir != want {
		t.Fatalf("resolveLinuxWorkingDir() = (%q, %q), want (%q, %q)", resolvedDir, execDir, want, want)
	}
}

func TestResolveLinuxCommandPath_UsesExecDirForRelativeCommand(t *testing.T) {
	execDir := filepath.Join(t.TempDir(), "hooks")
	got, err := resolveLinuxCommandPath("./hook.sh", execDir)
	if err != nil {
		t.Fatalf("resolveLinuxCommandPath() error = %v", err)
	}
	want := filepath.Join(execDir, "hook.sh")
	if got != want {
		t.Fatalf("resolveLinuxCommandPath() = %q, want %q", got, want)
	}
}

func TestBuildLinuxBwrapArgs_UsesResolvedPathForRelativeCommand(t *testing.T) {
	root := t.TempDir()
	execDir := filepath.Join(root, "hooks")
	if err := os.MkdirAll(execDir, 0o755); err != nil {
		t.Fatal(err)
	}
	resolvedPath := filepath.Join(execDir, "hook.sh")
	if err := os.WriteFile(resolvedPath, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	plan := []MountRule{
		{Source: execDir, Target: execDir, Mode: "rw"},
		{Source: resolvedPath, Target: resolvedPath, Mode: "ro"},
	}
	args, err := buildLinuxBwrapArgs("./hook.sh", resolvedPath, []string{"./hook.sh"}, execDir, plan)
	if err != nil {
		t.Fatalf("buildLinuxBwrapArgs() error = %v", err)
	}
	hasExecDir := false
	for _, arg := range args {
		if arg == execDir {
			hasExecDir = true
			break
		}
	}
	if !hasExecDir {
		t.Fatalf("buildLinuxBwrapArgs() missing resolved chdir: %v", args)
	}
	for i := range args {
		if args[i] == "--" {
			if i+1 >= len(args) || args[i+1] != resolvedPath {
				t.Fatalf("buildLinuxBwrapArgs() exec path = %v, want %q after --", args, resolvedPath)
			}
			return
		}
	}
	t.Fatalf("buildLinuxBwrapArgs() missing exec delimiter: %v", args)
}

func TestAppendLinuxArgumentMounts_AddsAbsoluteArgumentPaths(t *testing.T) {
	root := t.TempDir()
	input := filepath.Join(root, "input.txt")
	if err := os.WriteFile(input, []byte("data"), 0o644); err != nil {
		t.Fatal(err)
	}
	output := filepath.Join(root, "out", "result.txt")
	if err := os.MkdirAll(filepath.Dir(output), 0o755); err != nil {
		t.Fatal(err)
	}

	plan := appendLinuxArgumentMounts(nil, []string{input, "--output=" + output})
	if len(plan) != 2 {
		t.Fatalf("appendLinuxArgumentMounts() len = %d, want 2", len(plan))
	}
	if plan[0].Source != input || plan[0].Mode != "ro" {
		t.Fatalf("appendLinuxArgumentMounts()[0] = %+v, want source=%q mode=ro", plan[0], input)
	}
	if plan[1].Source != filepath.Dir(output) || plan[1].Mode != "rw" {
		t.Fatalf("appendLinuxArgumentMounts()[1] = %+v, want source=%q mode=rw", plan[1], filepath.Dir(output))
	}
}

func TestLinuxIsolationDefensiveBranches(t *testing.T) {
	t.Run("disabled", func(t *testing.T) {
		if err := applyPlatformIsolation(nil, launchProjection{}); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("missing backend", func(t *testing.T) {
		err := applyPlatformIsolation(
			exec.Command("/bin/true"),
			launchProjection{isolation: config.IsolationConfig{Enabled: true}},
		)
		if err == nil || !strings.Contains(err.Error(), "requires bwrap") {
			t.Fatalf("missing backend error = %v", err)
		}
	})

	t.Run("empty command", func(t *testing.T) {
		err := applyPlatformIsolation(nil, launchProjection{
			isolation:   config.IsolationConfig{Enabled: true},
			backendPath: "/fake/bwrap",
		})
		if err != nil {
			t.Fatal(err)
		}
	})

	t.Run("symlink working directory", func(t *testing.T) {
		root := t.TempDir()
		realDir := filepath.Join(root, "real")
		if err := os.Mkdir(realDir, 0o755); err != nil {
			t.Fatal(err)
		}
		binary := filepath.Join(realDir, "tool")
		if err := os.WriteFile(binary, []byte("#!/bin/sh\nexit 0\n"), 0o700); err != nil {
			t.Fatal(err)
		}
		linkedDir := filepath.Join(root, "linked")
		if err := os.Symlink(realDir, linkedDir); err != nil {
			t.Fatal(err)
		}
		cmd := &exec.Cmd{Path: binary, Args: []string{binary}, Dir: linkedDir}
		err := applyPlatformIsolation(cmd, launchProjection{
			isolation:   config.IsolationConfig{Enabled: true},
			backendPath: "/fake/bwrap",
		})
		if err != nil {
			t.Fatal(err)
		}
		if cmd.Path != "/fake/bwrap" || cmd.Dir != "" {
			t.Fatalf("wrapped command = path %q dir %q args %#v", cmd.Path, cmd.Dir, cmd.Args)
		}
	})

	t.Run("invalid base mount", func(t *testing.T) {
		root := t.TempDir()
		binary := filepath.Join(root, "tool")
		if err := os.WriteFile(binary, []byte("#!/bin/sh\nexit 0\n"), 0o700); err != nil {
			t.Fatal(err)
		}
		cmd := &exec.Cmd{Path: binary, Args: []string{binary}}
		err := applyPlatformIsolation(cmd, launchProjection{
			isolation:   config.IsolationConfig{Enabled: true},
			backendPath: "/fake/bwrap",
			linuxBaseMounts: []MountRule{{
				Source: filepath.Join(root, "missing"),
				Target: "/missing",
				Mode:   "ro",
			}},
		})
		if err == nil || !strings.Contains(err.Error(), "stat linux mount source") {
			t.Fatalf("invalid mount error = %v", err)
		}
	})

	t.Run("implicit working directory", func(t *testing.T) {
		working, err := os.Getwd()
		if err != nil {
			t.Fatal(err)
		}
		originalDir, execDir, err := resolveLinuxWorkingDir("", "./tool")
		if err != nil {
			t.Fatal(err)
		}
		if originalDir != "" || execDir != working {
			t.Fatalf("working dirs = %q, %q; want empty, %q", originalDir, execDir, working)
		}
	})

	t.Run("implicit command base", func(t *testing.T) {
		working, err := os.Getwd()
		if err != nil {
			t.Fatal(err)
		}
		got, err := resolveLinuxCommandPath("./tool", "")
		if err != nil {
			t.Fatal(err)
		}
		want := filepath.Join(working, "tool")
		if got != want {
			t.Fatalf("resolved command = %q, want %q", got, want)
		}
	})

	t.Run("argument path edge cases", func(t *testing.T) {
		root := t.TempDir()
		directory := filepath.Join(root, "directory")
		if err := os.Mkdir(directory, 0o755); err != nil {
			t.Fatal(err)
		}
		target := filepath.Join(root, "target")
		if err := os.WriteFile(target, []byte("data"), 0o600); err != nil {
			t.Fatal(err)
		}
		link := filepath.Join(root, "link")
		if err := os.Symlink(target, link); err != nil {
			t.Fatal(err)
		}
		loopA := filepath.Join(root, "loop-a")
		loopB := filepath.Join(root, "loop-b")
		if err := os.Symlink(loopB, loopA); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(loopA, loopB); err != nil {
			t.Fatal(err)
		}

		plan := appendLinuxArgumentMounts(nil, []string{
			directory,
			link,
			loopA,
			"--output=relative",
		})
		if !executionPolicyTestHasMount(plan, MountRule{
			Source: directory, Target: directory, Mode: "rw",
		}) {
			t.Fatalf("directory mount missing from %#v", plan)
		}
		if !executionPolicyTestHasMount(plan, MountRule{
			Source: link, Target: link, Mode: "ro",
		}) || !executionPolicyTestHasMount(plan, MountRule{
			Source: target, Target: target, Mode: "ro",
		}) {
			t.Fatalf("symlink mounts missing from %#v", plan)
		}
		if _, ok := linuxArgumentPath("--output=relative"); ok {
			t.Fatal("relative option value accepted as mount path")
		}
	})

	t.Run("writable regular file", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "file")
		if err := os.WriteFile(path, []byte("data"), 0o600); err != nil {
			t.Fatal(err)
		}
		flag, err := linuxBindFlag(MountRule{Source: path, Target: path, Mode: "rw"})
		if err != nil {
			t.Fatal(err)
		}
		if flag != "--bind" {
			t.Fatalf("writable file bind flag = %q, want --bind", flag)
		}
	})
}
