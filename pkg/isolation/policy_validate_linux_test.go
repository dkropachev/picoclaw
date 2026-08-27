//go:build linux

package isolation

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sipeed/picoclaw/pkg/config"
)

func TestExecutionPolicyValidateEnabledLinux(t *testing.T) {
	binDir := t.TempDir()
	if err := os.WriteFile(
		filepath.Join(binDir, "bwrap"),
		[]byte("#!/bin/sh\nexit 0\n"),
		0o700,
	); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", binDir)
	valid := NewExecutionPolicy(config.IsolationConfig{
		Enabled:              true,
		EnvironmentAllowlist: []string{"PATH"},
	})
	if err := valid.Validate(); err != nil {
		t.Fatalf("valid enabled policy = %v", err)
	}

	invalidExposure := NewExecutionPolicy(config.IsolationConfig{
		Enabled: true,
		ExposePaths: []config.ExposePath{{
			Source: "/source",
			Mode:   "invalid",
		}},
		EnvironmentAllowlist: []string{"PATH"},
	})
	if err := invalidExposure.Validate(); err == nil ||
		!strings.Contains(err.Error(), "invalid expose_paths mode") {
		t.Fatalf("invalid exposure validation = %v", err)
	}

	t.Setenv("PATH", t.TempDir())
	missingBackend := NewExecutionPolicy(config.IsolationConfig{
		Enabled:              true,
		EnvironmentAllowlist: []string{"PATH"},
	})
	if err := missingBackend.Validate(); err == nil ||
		!strings.Contains(err.Error(), "requires bwrap") {
		t.Fatalf("missing backend validation = %v", err)
	}

	disabledDormant := NewExecutionPolicy(config.IsolationConfig{
		ExposePaths: []config.ExposePath{{Mode: "invalid"}},
	})
	if err := disabledDormant.Validate(); err != nil {
		t.Fatalf("disabled dormant validation = %v", err)
	}
}
