package agent

import (
	"context"
	"testing"

	"github.com/sipeed/picoclaw/pkg/gitworkspace"
)

type controllerGitWorkspaceManagerFake struct{}

func (*controllerGitWorkspaceManagerFake) Acquire(
	context.Context,
	gitworkspace.AcquireRequest,
) (gitworkspace.WorkspaceInfo, error) {
	return gitworkspace.WorkspaceInfo{}, nil
}

func (*controllerGitWorkspaceManagerFake) WithPinnedOperation(
	ctx context.Context,
	_ gitworkspace.PinnedAcquireRequest,
	run func(context.Context) error,
) error {
	return run(ctx)
}

func (*controllerGitWorkspaceManagerFake) AcquirePinned(
	context.Context,
	gitworkspace.PinnedAcquireRequest,
) (gitworkspace.WorkspaceInfo, error) {
	return gitworkspace.WorkspaceInfo{}, nil
}

func (*controllerGitWorkspaceManagerFake) ReleaseSession(
	context.Context,
	gitworkspace.ReleaseRequest,
) ([]gitworkspace.WorkspaceInfo, error) {
	return nil, nil
}

func (*controllerGitWorkspaceManagerFake) Stats(
	context.Context,
) (gitworkspace.Stats, error) {
	return gitworkspace.Stats{}, nil
}

func (*controllerGitWorkspaceManagerFake) CleanupIgnored(
	context.Context,
	string,
) (gitworkspace.CleanupResult, error) {
	return gitworkspace.CleanupResult{}, nil
}

func (*controllerGitWorkspaceManagerFake) Drop(
	context.Context,
	string,
) (gitworkspace.WorkspaceInfo, error) {
	return gitworkspace.WorkspaceInfo{}, nil
}

func (*controllerGitWorkspaceManagerFake) Reconcile(
	context.Context,
) (gitworkspace.ReconcileResult, error) {
	return gitworkspace.ReconcileResult{}, nil
}

func TestControllerGitWorkspaceManagerReturnsConcreteProductionManager(t *testing.T) {
	manager, err := gitworkspace.NewManager(gitworkspace.Options{RootDir: t.TempDir()})
	if err != nil {
		t.Fatalf("gitworkspace.NewManager() error = %v", err)
	}
	loop := &AgentLoop{gitWorkspaces: manager}

	got, err := loop.ControllerGitWorkspaceManager()
	if err != nil {
		t.Fatalf("ControllerGitWorkspaceManager() error = %v", err)
	}
	if got != manager {
		t.Fatalf("ControllerGitWorkspaceManager() = %p, want %p", got, manager)
	}
}

func TestControllerGitWorkspaceManagerFailsClosed(t *testing.T) {
	var typedNil *gitworkspace.Manager
	tests := []struct {
		name string
		loop *AgentLoop
	}{
		{name: "nil agent loop"},
		{name: "nil manager", loop: &AgentLoop{}},
		{name: "typed nil production manager", loop: &AgentLoop{gitWorkspaces: typedNil}},
		{
			name: "alternate manager implementation",
			loop: &AgentLoop{gitWorkspaces: &controllerGitWorkspaceManagerFake{}},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			manager, err := test.loop.ControllerGitWorkspaceManager()
			if err == nil {
				t.Fatal("ControllerGitWorkspaceManager() error = nil")
			}
			if manager != nil {
				t.Fatalf("ControllerGitWorkspaceManager() = %p, want nil", manager)
			}
		})
	}
}
