//go:build !windows

package main

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestHermeticGoTestRunnerKillsBackgroundDescendants(t *testing.T) {
	captureDir := t.TempDir()
	stubGo := writeHermeticRunnerGoStub(t, captureDir)
	probe := filepath.Join(t.TempDir(), "background-probe")
	if err := os.WriteFile(probe, []byte(`#!/bin/sh
sleep 30 &
printf '%s\n' "$!" >"$CAPTURE_DIR/background-pid"
exit 0
`), 0o755); err != nil {
		t.Fatal(err)
	}
	command := exec.Command(buildHermeticRunnerExecutable(t), probe)
	command.Env = hermeticRunnerTestEnvironment(t, map[string]string{
		"CAPTURE_DIR":             captureDir,
		"GOCACHE":                 filepath.Join(t.TempDir(), "go-build"),
		"GOMODCACHE":              filepath.Join(t.TempDir(), "go-mod"),
		"PICOCLAW_TEST_GO_BINARY": stubGo,
		"TMPDIR":                  t.TempDir(),
	})
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("runner failed: %v\n%s", err, output)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(
		readHermeticRunnerFile(t, filepath.Join(captureDir, "background-pid")),
	))
	if err != nil {
		t.Fatalf("decode background PID: %v", err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for {
		err = syscall.Kill(pid, 0)
		if errors.Is(err, syscall.ESRCH) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("background test descendant PID %d survived runner cleanup: %v", pid, err)
		}
		time.Sleep(20 * time.Millisecond)
	}
}
