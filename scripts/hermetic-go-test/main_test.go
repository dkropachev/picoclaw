package main

import (
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
)

const (
	hermeticChildModeEnv    = "HERMETIC_GO_TEST_CHILD_MODE"
	hermeticChildCaptureEnv = "HERMETIC_GO_TEST_CHILD_CAPTURE"
)

func TestParseRunOptions(t *testing.T) {
	tests := []struct {
		name      string
		arguments []string
		want      runOptions
		wantError string
	}{
		{name: "missing command", wantError: "usage:"},
		{
			name:      "go run separator",
			arguments: []string{"--", "go", "test", "./..."},
			want:      runOptions{command: []string{"go", "test", "./..."}},
		},
		{
			name:      "skip core build",
			arguments: []string{"--", skipCoreBuildOption, "--", "go", "test", "./..."},
			want: runOptions{
				command:       []string{"go", "test", "./..."},
				skipCoreBuild: true,
			},
		},
		{
			name:      "compiled runner skip",
			arguments: []string{skipCoreBuildOption, "command"},
			want:      runOptions{command: []string{"command"}, skipCoreBuild: true},
		},
		{
			name:      "unknown option",
			arguments: []string{"--unknown", "command"},
			wantError: "unknown hermetic test runner option",
		},
		{
			name:      "duplicate option",
			arguments: []string{skipCoreBuildOption, skipCoreBuildOption, "command"},
			wantError: "duplicate hermetic test runner option",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := parseRunOptions(test.arguments)
			if test.wantError != "" {
				if err == nil || !strings.Contains(err.Error(), test.wantError) {
					t.Fatalf("parseRunOptions() error = %v", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseRunOptions() error = %v", err)
			}
			if !reflect.DeepEqual(got, test.want) {
				t.Fatalf("parseRunOptions() = %#v, want %#v", got, test.want)
			}
		})
	}
}

func TestRunRejectsInvalidOptionsBeforeCreatingRuntime(t *testing.T) {
	for name, arguments := range map[string][]string{
		"missing command": nil,
		"unknown option":  {"--unknown", "command"},
	} {
		t.Run(name, func(t *testing.T) {
			if code := run(arguments); code != 2 {
				t.Fatalf("run() code = %d, want 2", code)
			}
		})
	}
}

func TestCreateInertCoreBinaryFailsClosed(t *testing.T) {
	path := filepath.Join(t.TempDir(), executableName("picoclaw"))
	if err := createInertCoreBinary(path); err != nil {
		t.Fatalf("createInertCoreBinary() error = %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != inertCoreBinaryBytes {
		t.Fatalf("sentinel data = %q, error = %v", data, err)
	}
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o111 != 0 {
		t.Fatalf("sentinel info = (%v, %v)", info, err)
	}
	if err = exec.Command(path).Run(); err == nil {
		t.Fatal("inert core binary executed successfully")
	}
	if err = createInertCoreBinary(path); !errors.Is(err, os.ErrExist) {
		t.Fatalf("second createInertCoreBinary() error = %v, want exists", err)
	}
}

func TestRunWithSkippedCoreBuildUsesDisposableSentinel(t *testing.T) {
	if mode := os.Getenv(hermeticChildModeEnv); mode != "" {
		runHermeticChildProbe(t, mode)
		return
	}

	capture := filepath.Join(t.TempDir(), "child.json")
	t.Setenv(hermeticChildModeEnv, "skip")
	t.Setenv(hermeticChildCaptureEnv, capture)
	if runtime.GOOS != "windows" {
		t.Setenv(testGoEnv, os.Args[0])
	} else {
		t.Setenv(testGoEnv, "")
	}
	t.Setenv("GOCACHE", t.TempDir())
	t.Setenv("GOMODCACHE", t.TempDir())
	code := run([]string{
		skipCoreBuildOption,
		"--",
		os.Args[0],
		"-test.run=^TestRunWithSkippedCoreBuildUsesDisposableSentinel$",
	})
	if code != 0 {
		t.Fatalf("run() code = %d", code)
	}
	probe := readHermeticChildProbe(t, capture)
	if probe.Mode != "skip" || probe.Binary == "" || probe.Root == "" {
		t.Fatalf("child probe = %#v", probe)
	}
	if filepath.Dir(filepath.Dir(probe.Binary)) != probe.Root {
		t.Fatalf("binary %q is outside root %q", probe.Binary, probe.Root)
	}
	if _, err := os.Stat(probe.Root); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("test root survived command: %q, error = %v", probe.Root, err)
	}
}

func TestRunBuildsCoreByDefault(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell Go stub is exercised by Unix coverage and CI")
	}
	if mode := os.Getenv(hermeticChildModeEnv); mode != "" {
		runHermeticChildProbe(t, mode)
		return
	}

	captureDir := t.TempDir()
	goStub := filepath.Join(captureDir, "go")
	stub := `#!/usr/bin/env bash
set -euo pipefail
printf '%s\n' "$@" >"$HERMETIC_GO_TEST_BUILD_ARGS"
test "${1-}" = build
shift
output=""
while [[ "$#" -gt 0 ]]; do
  if [[ "$1" = -o ]]; then
    output="$2"
    shift 2
    continue
  fi
  shift
done
test -n "$output"
printf '#!/bin/sh\nexit 0\n' >"$output"
chmod 0755 "$output"
`
	if err := os.WriteFile(goStub, []byte(stub), 0o755); err != nil {
		t.Fatal(err)
	}
	childCapture := filepath.Join(captureDir, "child.json")
	buildArguments := filepath.Join(captureDir, "build-args")
	t.Setenv(hermeticChildModeEnv, "build")
	t.Setenv(hermeticChildCaptureEnv, childCapture)
	t.Setenv("HERMETIC_GO_TEST_BUILD_ARGS", buildArguments)
	t.Setenv(testGoEnv, goStub)
	t.Setenv("GOCACHE", t.TempDir())
	t.Setenv("GOMODCACHE", t.TempDir())
	code := run([]string{
		"--",
		os.Args[0],
		"-test.run=^TestRunBuildsCoreByDefault$",
	})
	if code != 0 {
		t.Fatalf("run() code = %d", code)
	}
	probe := readHermeticChildProbe(t, childCapture)
	if probe.Mode != "build" {
		t.Fatalf("child probe = %#v", probe)
	}
	arguments, err := os.ReadFile(buildArguments)
	if err != nil {
		t.Fatal(err)
	}
	wantSuffix := "build\n-tags\n" + coreBuildTags + "\n-o\n" + probe.Binary + "\n./cmd/picoclaw\n"
	if string(arguments) != wantSuffix {
		t.Fatalf("build arguments = %q, want %q", arguments, wantSuffix)
	}
}

type hermeticChildProbe struct {
	Mode   string `json:"mode"`
	Root   string `json:"root"`
	Binary string `json:"binary"`
}

func runHermeticChildProbe(t *testing.T, mode string) {
	t.Helper()
	path := os.Getenv("PICOCLAW_BINARY")
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() {
		t.Fatalf("PICOCLAW_BINARY = %q, info = %v, error = %v", path, info, err)
	}
	switch mode {
	case "skip":
		if info.Mode().Perm()&0o111 != 0 {
			t.Fatalf("skipped binary mode = %v, want non-executable", info.Mode().Perm())
		}
		if err = exec.Command(path).Run(); err == nil {
			t.Fatal("skipped core binary executed successfully")
		}
	case "build":
		if runtime.GOOS != "windows" && info.Mode().Perm()&0o111 == 0 {
			t.Fatalf("built binary mode = %v, want executable", info.Mode().Perm())
		}
	default:
		t.Fatalf("unknown child mode %q", mode)
	}
	payload, err := json.Marshal(hermeticChildProbe{
		Mode:   mode,
		Root:   os.Getenv("PICOCLAW_TEST_ROOT"),
		Binary: path,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(os.Getenv(hermeticChildCaptureEnv), payload, 0o600); err != nil {
		t.Fatal(err)
	}
}

func readHermeticChildProbe(t *testing.T, path string) hermeticChildProbe {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var probe hermeticChildProbe
	if err = json.Unmarshal(data, &probe); err != nil {
		t.Fatal(err)
	}
	return probe
}
