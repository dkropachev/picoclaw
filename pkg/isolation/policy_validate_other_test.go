//go:build !linux && !windows

package isolation

import (
	"strings"
	"testing"

	"github.com/sipeed/picoclaw/pkg/config"
)

func TestExecutionPolicyValidateUnsupportedPlatform(t *testing.T) {
	if err := NewExecutionPolicy(config.IsolationConfig{}).Validate(); err != nil {
		t.Fatalf("disabled policy validation = %v", err)
	}
	if err := NewExecutionPolicy(
		config.IsolationConfig{Enabled: true},
	).Validate(); err == nil ||
		!strings.Contains(err.Error(), "not supported") {
		t.Fatalf("enabled policy validation = %v", err)
	}
}
