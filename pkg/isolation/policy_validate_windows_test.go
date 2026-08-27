//go:build windows

package isolation

import (
	"strings"
	"testing"

	"github.com/sipeed/picoclaw/pkg/config"
)

func TestExecutionPolicyValidateEnabledWindows(t *testing.T) {
	valid := NewExecutionPolicy(config.IsolationConfig{Enabled: true})
	if err := valid.Validate(); err != nil {
		t.Fatalf("valid Windows policy = %v", err)
	}
	unsupportedExposure := NewExecutionPolicy(config.IsolationConfig{
		Enabled: true,
		ExposePaths: []config.ExposePath{{
			Source: `C:\data`,
			Target: `C:\data`,
			Mode:   "ro",
		}},
	})
	if err := unsupportedExposure.Validate(); err == nil ||
		!strings.Contains(err.Error(), "does not yet support") {
		t.Fatalf("Windows exposure validation = %v", err)
	}
}
