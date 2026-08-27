package cliprovider

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/isolation"
)

func TestCLIProviderPolicyConstructorsRetainExactPolicy(t *testing.T) {
	policy := isolation.NewExecutionPolicy(config.IsolationConfig{
		Enabled: true,
		ExposePaths: []config.ExposePath{
			{Source: "/source", Target: "/target", Mode: "ro"},
		},
	})

	claude := NewClaudeCliProviderWithExecutionPolicy("/claude", policy)
	if claude.executionPolicy != policy {
		t.Fatal("Claude CLI provider did not retain the exact execution policy")
	}

	codex := NewCodexCliProviderWithExecutionPolicy("/codex", policy)
	if codex.executionPolicy != policy {
		t.Fatal("Codex CLI provider did not retain the exact execution policy")
	}
}

func TestCLIProviderCompatibilityConstructorsUseValidDefaultPolicy(t *testing.T) {
	// Select a legacy policy that cannot launch. Compatibility constructors must
	// ignore it and construct their own valid disabled default policy.
	isolation.Configure(&config.Config{Isolation: config.IsolationConfig{
		Enabled: true,
		ExposePaths: []config.ExposePath{
			{Source: "/invalid", Mode: "invalid"},
		},
	}})
	t.Cleanup(func() { isolation.Configure(nil) })

	claudeScript := createMockCLI(t, `{"type":"result","result":"ok"}`, "", 0)
	claude := NewClaudeCliProvider(t.TempDir())
	claude.command = claudeScript
	if _, err := claude.Chat(
		context.Background(),
		[]Message{{Role: "user", Content: "test"}},
		nil,
		"test-model",
		nil,
	); err != nil {
		t.Fatalf("legacy Claude constructor produced an invalid policy: %v", err)
	}

	codexScript := createMockCodexCLI(t, []string{
		`{"type":"item.completed","item":{"id":"1","type":"agent_message","text":"ok"}}`,
		`{"type":"turn.completed"}`,
	})
	codex := NewCodexCliProvider(t.TempDir())
	codex.command = codexScript
	if _, err := codex.Chat(
		context.Background(),
		[]Message{{Role: "user", Content: "test"}},
		nil,
		"test-model",
		nil,
	); err != nil {
		t.Fatalf("legacy Codex constructor produced an invalid policy: %v", err)
	}
}

func TestCLIProviderZeroPolicyFailsBeforeLaunch(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("mock CLI scripts not supported on Windows")
	}

	tests := []struct {
		name string
		run  func(command string) error
	}{
		{
			name: "claude",
			run: func(command string) error {
				provider := NewClaudeCliProviderWithExecutionPolicy(
					t.TempDir(),
					isolation.ExecutionPolicy{},
				)
				provider.command = command
				_, err := provider.Chat(
					context.Background(),
					[]Message{{Role: "user", Content: "test"}},
					nil,
					"test-model",
					nil,
				)
				return err
			},
		},
		{
			name: "codex",
			run: func(command string) error {
				provider := NewCodexCliProviderWithExecutionPolicy(
					t.TempDir(),
					isolation.ExecutionPolicy{},
				)
				provider.command = command
				_, err := provider.Chat(
					context.Background(),
					[]Message{{Role: "user", Content: "test"}},
					nil,
					"test-model",
					nil,
				)
				return err
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			dir := t.TempDir()
			marker := filepath.Join(dir, "launched")
			command := filepath.Join(dir, test.name)
			script := "#!/bin/sh\ntouch '" + marker + "'\n"
			if err := os.WriteFile(command, []byte(script), 0o755); err != nil {
				t.Fatal(err)
			}

			err := test.run(command)
			if !errors.Is(err, isolation.ErrExecutionPolicyUnavailable) {
				t.Fatalf("Chat() error = %v, want %v", err, isolation.ErrExecutionPolicyUnavailable)
			}
			if _, statErr := os.Stat(marker); !errors.Is(statErr, os.ErrNotExist) {
				t.Fatalf("strict zero policy launched the CLI; marker stat error = %v", statErr)
			}
		})
	}
}

func TestCLIProvidersUseFrozenPathHomeAndRestrictedEnvironment(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix mock CLI scripts")
	}
	for _, providerName := range []string{"claude", "codex"} {
		t.Run(providerName, func(t *testing.T) {
			dirA := t.TempDir()
			dirB := t.TempDir()
			homeA := t.TempDir()
			homeB := t.TempDir()
			body := `#!/bin/sh
test "$P014_CLI_OWNER" = "generation-a" || exit 21
test "$HOME" = "` + homeA + `" || exit 22
test -z "${OPENAI_API_KEY+x}" || exit 23
test -z "${HTTPS_PROXY+x}" || exit 24
test -z "${NODE_OPTIONS+x}" || exit 25
`
			if providerName == "claude" {
				body += `printf '%s\n' '{"type":"result","result":"generation-a"}'
`
			} else {
				body += `printf '%s\n' '{"type":"item.completed","item":{"id":"1","type":"agent_message","text":"generation-a"}}' '{"type":"turn.completed"}'
`
			}
			if err := os.WriteFile(filepath.Join(dirA, providerName), []byte(body), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(
				filepath.Join(dirB, providerName),
				[]byte("#!/bin/sh\nexit 99\n"),
				0o700,
			); err != nil {
				t.Fatal(err)
			}
			t.Setenv("PATH", dirA)
			t.Setenv("HOME", homeA)
			t.Setenv("P014_CLI_OWNER", "generation-a")
			t.Setenv("OPENAI_API_KEY", "must-not-pass")
			t.Setenv("HTTPS_PROXY", "http://user:password@proxy.invalid")
			t.Setenv("NODE_OPTIONS", "--require=/tmp/inject.js")
			policy := isolation.NewExecutionPolicy(config.IsolationConfig{
				EnvironmentAllowlist: []string{"PATH", "HOME", "P014_CLI_OWNER"},
			})
			t.Setenv("PATH", dirB)
			t.Setenv("HOME", homeB)
			t.Setenv("P014_CLI_OWNER", "live-generation-b")

			var response *LLMResponse
			var err error
			if providerName == "claude" {
				provider := NewClaudeCliProviderWithExecutionPolicy(t.TempDir(), policy)
				response, err = provider.Chat(
					context.Background(),
					[]Message{{Role: "user", Content: "test"}},
					nil,
					"test-model",
					nil,
				)
			} else {
				provider := NewCodexCliProviderWithExecutionPolicy(t.TempDir(), policy)
				response, err = provider.Chat(
					context.Background(),
					[]Message{{Role: "user", Content: "test"}},
					nil,
					"test-model",
					nil,
				)
			}
			if err != nil || response == nil || response.Content != "generation-a" {
				t.Fatalf("CLI response = %#v, %v", response, err)
			}
		})
	}
}
