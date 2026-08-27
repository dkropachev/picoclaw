package tools

import (
	"context"
	"errors"
	"fmt"
	"os"
	"runtime"
	"strings"
	"sync"
	"testing"

	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/isolation"
)

func TestExecToolExactPolicyForegroundAndBackgroundIgnoreAmbientAndLegacyChurn(t *testing.T) {
	if testing.Short() {
		t.Skip("starts shell helpers")
	}
	if err := os.Setenv("P014_TOOL_OWNER", "A"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Unsetenv("P014_TOOL_OWNER") })
	if err := os.Setenv("P014_TOOL_SECRET", "must-not-pass"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Unsetenv("P014_TOOL_SECRET") })

	cfg := config.DefaultConfig()
	cfg.Isolation.EnvironmentAllowlist = []string{"PATH", "HOME", "P014_TOOL_OWNER"}
	policyA := isolation.NewExecutionPolicy(cfg.Isolation)
	if err := os.Setenv("P014_TOOL_OWNER", "B"); err != nil {
		t.Fatal(err)
	}
	policyB := isolation.NewExecutionPolicy(cfg.Isolation)
	if err := os.Setenv("P014_TOOL_OWNER", "LIVE"); err != nil {
		t.Fatal(err)
	}
	isolation.Configure(&config.Config{Isolation: config.IsolationConfig{
		EnvironmentAllowlist: []string{"P014_TOOL_SECRET"},
	}})
	t.Cleanup(func() { isolation.Configure(nil) })

	toolA, err := NewExecToolWithConfigAndExecutionPolicy("", false, cfg, policyA)
	if err != nil {
		t.Fatal(err)
	}
	toolB, err := NewExecToolWithConfigAndExecutionPolicy("", false, cfg, policyB)
	if err != nil {
		t.Fatal(err)
	}

	type testCase struct {
		name string
		tool *ExecTool
		want string
	}
	tests := []testCase{{name: "A", tool: toolA, want: "A"}, {name: "B", tool: toolB, want: "B"}}
	command := executionPolicyToolEnvironmentCommand()
	stopConfigure := make(chan struct{})
	configureDone := make(chan struct{})
	go func() {
		defer close(configureDone)
		for {
			select {
			case <-stopConfigure:
				return
			default:
				isolation.Configure(&config.Config{Isolation: config.IsolationConfig{
					EnvironmentAllowlist: []string{"P014_TOOL_SECRET"},
				}})
				isolation.Configure(config.DefaultConfig())
			}
		}
	}()
	var workers sync.WaitGroup
	errorsSeen := make(chan error, 20)
	for iteration := 0; iteration < 10; iteration++ {
		for _, test := range tests {
			workers.Add(1)
			go func() {
				defer workers.Done()
				result := test.tool.runSync(
					context.Background(),
					command,
					"",
				)
				if result == nil || result.IsError || result.ForLLM != test.want {
					errorsSeen <- errors.New(test.name + " foreground projection crossed")
				}
			}()
		}
	}
	workers.Wait()
	close(stopConfigure)
	<-configureDone
	close(errorsSeen)
	for err := range errorsSeen {
		t.Error(err)
	}

	for _, test := range tests {
		ptyModes := []bool{false}
		if runtime.GOOS != "windows" {
			ptyModes = append(ptyModes, true)
		}
		for _, ptyEnabled := range ptyModes {
			owner := processTestOwner(fmt.Sprintf("p014-%s-%t", test.name, ptyEnabled))
			manager := installProcessTestManager(t, test.tool, owner)
			started := startOwnedBackgroundProcess(
				t,
				test.tool,
				processTestContext(owner),
				command,
				ptyEnabled,
			)
			session, err := manager.Get(owner, started.SessionID)
			if err != nil {
				t.Fatal(err)
			}
			awaitProcessTestWait(t, session, started.SessionID)
			if exitCode := session.GetExitCode(); exitCode != 0 {
				t.Fatalf("%s pty=%t exit = %d", test.name, ptyEnabled, exitCode)
			}
			if got := strings.TrimSpace(session.Read()); got != test.want {
				t.Fatalf("%s pty=%t output = %q", test.name, ptyEnabled, got)
			}
		}
	}
}

func executionPolicyToolEnvironmentCommand() string {
	if runtime.GOOS == "windows" {
		return `[Console]::Out.Write($env:P014_TOOL_OWNER); if ($env:P014_TOOL_SECRET) { exit 1 }`
	}
	return `printf '%s' "$P014_TOOL_OWNER"; test -z "$P014_TOOL_SECRET"`
}

func TestExecToolStrictZeroPolicyFailsClosed(t *testing.T) {
	cfg := config.DefaultConfig()
	tool, err := NewExecToolWithConfigAndExecutionPolicy(
		"",
		false,
		cfg,
		isolation.ExecutionPolicy{},
	)
	if err != nil {
		t.Fatal(err)
	}
	result := tool.runSync(context.Background(), ":", "")
	if result == nil || !result.IsError ||
		!strings.Contains(result.ForLLM, isolation.ErrExecutionPolicyUnavailable.Error()) {
		t.Fatalf("zero-policy result = %#v", result)
	}
}

func TestExecToolLookupGuardEnvironmentFailsClosedAndUsesBoundPolicy(t *testing.T) {
	if value, ok := (*ExecTool)(nil).lookupGuardEnvironment("P014_GUARD_ENV", ""); ok || value != "" {
		t.Fatalf("nil tool lookup = %q, %t; want empty, false", value, ok)
	}
	if value, ok := (&ExecTool{}).lookupGuardEnvironment("P014_GUARD_ENV", ""); ok || value != "" {
		t.Fatalf("zero-policy lookup = %q, %t; want empty, false", value, ok)
	}

	t.Setenv("P014_GUARD_ENV", "frozen")
	cfg := config.DefaultConfig()
	cfg.Isolation.EnvironmentAllowlist = []string{"P014_GUARD_ENV"}
	tool := &ExecTool{executionPolicy: isolation.NewExecutionPolicy(cfg.Isolation)}
	t.Setenv("P014_GUARD_ENV", "live")

	value, ok := tool.lookupGuardEnvironment("P014_GUARD_ENV", t.TempDir())
	if !ok || value != "frozen" {
		t.Fatalf("bound-policy lookup = %q, %t; want frozen, true", value, ok)
	}
}
