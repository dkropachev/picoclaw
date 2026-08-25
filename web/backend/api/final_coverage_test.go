package api

import (
	"testing"

	"github.com/sipeed/picoclaw/pkg/config"
)

func TestCollectionSupportHelpersPreserveDefensiveContracts(t *testing.T) {
	t.Run("capability arrays encode as arrays", func(t *testing.T) {
		capabilities := agentCapabilities{}
		ensureCapabilityResponseArrays(&capabilities)

		if capabilities.Tools.Values == nil ||
			capabilities.Skills.Values == nil ||
			capabilities.Skills.InheritedValues == nil ||
			capabilities.MCPServers.Values == nil {
			t.Fatalf("nil capability slice remained: %#v", capabilities)
		}
	})

	t.Run("MCP maps are detached", func(t *testing.T) {
		original := map[string]string{"Authorization": "Bearer secret"}
		cloned := cloneMCPStringMap(original)
		cloned["Authorization"] = "redacted"

		if original["Authorization"] != "Bearer secret" {
			t.Fatalf("clone mutated source map: %#v", original)
		}
	})

	t.Run("restart precondition is a bad request", func(t *testing.T) {
		err := &preconditionFailedError{reason: "model setup required"}
		if err.Error() != "model setup required" || !err.IsBadRequest() {
			t.Fatalf("unexpected precondition error behavior: %q", err)
		}
	})

	t.Run("thread policy modes normalize explicitly", func(t *testing.T) {
		tests := []struct {
			input   string
			want    string
			wantErr bool
		}{
			{input: " AUTO ", want: config.ThreadPolicyModeAuto},
			{input: "", want: config.ThreadPolicyModeTool},
			{input: " TOOL ", want: config.ThreadPolicyModeTool},
			{input: "suggest", want: config.ThreadPolicyModeSuggest},
			{input: " OFF ", want: config.ThreadPolicyModeOff},
			{input: "unsupported", wantErr: true},
		}
		for _, test := range tests {
			got, err := normalizeThreadPolicyMode(test.input)
			if (err != nil) != test.wantErr || got != test.want {
				t.Errorf(
					"normalizeThreadPolicyMode(%q) = %q, %v; want %q, error=%v",
					test.input,
					got,
					err,
					test.want,
					test.wantErr,
				)
			}
		}
	})
}
