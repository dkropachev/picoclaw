//go:build windows

package isolation

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/sipeed/picoclaw/pkg/config"
)

const (
	windowsFrozenHelperMarker     = "P014_WINDOWS_FROZEN_HELPER"
	windowsDescendantParentMarker = "P014_WINDOWS_DESCENDANT_PARENT"
	windowsDescendantChildMarker  = "P014_WINDOWS_DESCENDANT_CHILD"
)

func TestWindowsFrozenPATHEXTHelper(t *testing.T) {
	if os.Getenv(windowsFrozenHelperMarker) != "1" {
		return
	}
	_, _ = os.Stdout.WriteString("frozen")
	os.Exit(0)
}

func TestWindowsDescendantParentHelper(t *testing.T) {
	if os.Getenv(windowsDescendantParentMarker) != "1" {
		return
	}
	cmd := exec.Command(
		"p014-descendant",
		"-test.run=^TestWindowsDescendantChildHelper$",
	)
	cmd.Env = append(os.Environ(), windowsDescendantChildMarker+"=1")
	output, err := cmd.Output()
	if err != nil {
		_, _ = fmt.Fprint(os.Stderr, err)
		os.Exit(2)
	}
	_, _ = os.Stdout.Write(output)
	os.Exit(0)
}

func TestWindowsDescendantChildHelper(t *testing.T) {
	if os.Getenv(windowsDescendantChildMarker) != "1" {
		return
	}
	executable, err := os.Executable()
	if err != nil {
		os.Exit(2)
	}
	_, _ = os.Stdout.WriteString(filepath.Clean(executable))
	os.Exit(0)
}

func TestWindowsExecutableExtensionsRejectPathAndOptionInjection(t *testing.T) {
	got := windowsExecutableExtensions(
		"helper",
		`.EXE;.cmd;.EXE;..\escape.exe;/escape;.TOO-LONG-OR-INVALID;`,
		true,
	)
	want := []string{".EXE", ".CMD"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("windowsExecutableExtensions() = %#v, want %#v", got, want)
	}
}

func TestWindowsExecutableExtensionsRequiresAdmittedRequestedExtension(t *testing.T) {
	if got := windowsExecutableExtensions("helper.EXE", ".CMD", true); len(got) != 0 {
		t.Fatalf("unadmitted requested extension = %#v, want none", got)
	}
	if got := windowsExecutableExtensions("helper.CmD", ".EXE;.CMD", true); !reflect.DeepEqual(got, []string{""}) {
		t.Fatalf("admitted requested extension = %#v", got)
	}
}

func TestWindowsExecutableExtensionsDistinguishesAbsentAndEmpty(t *testing.T) {
	if got := windowsExecutableExtensions("helper", "", false); !reflect.DeepEqual(
		got,
		[]string{".COM", ".EXE", ".BAT", ".CMD"},
	) {
		t.Fatalf("absent PATHEXT extensions = %#v", got)
	}
	if got := windowsExecutableExtensions("helper", "", true); len(got) != 0 {
		t.Fatalf("explicit-empty PATHEXT extensions = %#v, want none", got)
	}
}

func TestResolveWindowsExecutableRejectsDriveRelativePath(t *testing.T) {
	if _, err := resolveExecutablePath(
		`C:helper.exe`,
		`C:\Windows\System32`,
		`.EXE`,
		true,
	); err == nil {
		t.Fatal("drive-relative executable path accepted")
	}
}

func TestResolveWindowsExecutableUsesOnlyAbsolutePathEntries(t *testing.T) {
	admitted := t.TempDir()
	working := t.TempDir()
	want := filepath.Join(admitted, "policy-helper.EXE")
	if err := os.WriteFile(want, []byte("fixture"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		filepath.Join(working, "policy-helper.EXE"),
		[]byte("decoy"),
		0o600,
	); err != nil {
		t.Fatal(err)
	}

	previous, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err = os.Chdir(working); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(previous) })

	got, err := resolveExecutablePath(
		"policy-helper",
		`;.;relative;`+admitted,
		`.EXE;.CMD`,
		true,
	)
	if err != nil {
		t.Fatal(err)
	}
	if !filepath.IsAbs(got) || filepath.Clean(got) != filepath.Clean(want) {
		t.Fatalf("resolved executable = %q, want %q", got, want)
	}
}

func TestExecutionPolicyWindowsFrozenPATHEXTOverridesLiveLookup(t *testing.T) {
	admitted := t.TempDir()
	decoy := t.TempDir()
	helper := filepath.Join(admitted, "policy-helper.EXE")
	copyWindowsTestBinary(t, helper)
	t.Setenv("PATH", admitted)
	t.Setenv("PATHEXT", ".EXE")
	policy := NewExecutionPolicy(config.IsolationConfig{
		EnvironmentAllowlist: []string{"PATH", "PATHEXT"},
	})
	t.Setenv("PATH", decoy)
	t.Setenv("PATHEXT", ".CMD")

	for _, requested := range []string{"policy-helper", strings.TrimSuffix(helper, ".EXE")} {
		cmd := exec.Command(
			requested,
			"-test.run=^TestWindowsFrozenPATHEXTHelper$",
		)
		cmd.Env = []string{windowsFrozenHelperMarker + "=1"}
		var stdout bytes.Buffer
		cmd.Stdout = &stdout
		if err := policy.Run(cmd); err != nil {
			t.Fatalf("policy.Run(%q) error = %v", requested, err)
		}
		if stdout.String() != "frozen" || !strings.EqualFold(cmd.Path, helper) {
			t.Fatalf("policy.Run(%q) path=%q output=%q", requested, cmd.Path, stdout.String())
		}
	}
}

func TestExecutionPolicyWindowsDescendantSkipsCurrentDirectory(t *testing.T) {
	admitted := t.TempDir()
	working := t.TempDir()
	parent := filepath.Join(admitted, "p014-parent.EXE")
	wantDescendant := filepath.Join(admitted, "p014-descendant.EXE")
	copyWindowsTestBinary(t, parent)
	copyWindowsTestBinary(t, wantDescendant)
	copyWindowsTestBinary(t, filepath.Join(working, "p014-descendant.EXE"))
	t.Setenv("PATH", admitted)
	t.Setenv("PATHEXT", ".EXE")
	policy := NewExecutionPolicy(config.IsolationConfig{
		EnvironmentAllowlist: []string{"PATH", "PATHEXT"},
	})
	cmd := exec.Command(
		"p014-parent",
		"-test.run=^TestWindowsDescendantParentHelper$",
	)
	cmd.Dir = working
	cmd.Env = []string{windowsDescendantParentMarker + "=1"}
	var stdout bytes.Buffer
	cmd.Stdout = &stdout
	if err := policy.Run(cmd); err != nil {
		t.Fatal(err)
	}
	if got := filepath.Clean(stdout.String()); !strings.EqualFold(got, wantDescendant) {
		t.Fatalf("descendant executable = %q, want %q", got, wantDescendant)
	}
}

func copyWindowsTestBinary(t *testing.T, destination string) {
	t.Helper()
	data, err := os.ReadFile(os.Args[0])
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(destination, data, 0o700); err != nil {
		t.Fatal(err)
	}
}
