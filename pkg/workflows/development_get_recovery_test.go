package workflows

import (
	"context"
	"errors"
	"os"
	"testing"
)

func TestGetWorkflowDevelopmentSessionRecoversPreparedPublish(t *testing.T) {
	fixture := newWorkflowDevelopmentPublishFixture(t, "")
	fixture.request.ExpectedDependencyRevision = "dependency:current"
	targetBefore := readFileData(t, fixture.targetPath)
	interrupted := errors.New("interrupt after target write")
	_, err := publishWorkflowDevelopmentTransaction(
		context.Background(),
		fixture.workspace,
		&fixture.request,
		fixture.runtime,
		readyWorkflowDevelopmentPublishGate("dependency:current"),
		&workflowDevelopmentPublishHooks{
			afterBoundary: func(boundary workflowDevelopmentPublishBoundary) error {
				if boundary == workflowDevelopmentPublishBoundaryTargetWritten {
					return interrupted
				}
				return nil
			},
			leaveJournalOnError: true,
		},
		fixture.localOptions...,
	)
	if !errors.Is(err, interrupted) {
		t.Fatalf("publish error = %v, want interruption", err)
	}

	session, err := GetWorkflowDevelopmentSession(fixture.workspace)
	if err != nil {
		t.Fatalf("GetWorkflowDevelopmentSession() error = %v", err)
	}
	if session == nil || session.SessionRevision != fixture.session.SessionRevision {
		t.Fatalf(
			"recovered session = %#v, want revision %q",
			session,
			fixture.session.SessionRevision,
		)
	}
	assertFileData(t, fixture.targetPath, targetBefore)
	if _, statErr := os.Stat(
		workflowDevelopmentPublishJournalPath(fixture.workspace),
	); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("publish journal stat = %v, want not exist", statErr)
	}
}

func TestGetWorkflowDevelopmentSessionBlocksOnPublishRecoveryConflict(t *testing.T) {
	fixture := newWorkflowDevelopmentPublishFixture(t, "")
	fixture.request.ExpectedDependencyRevision = "dependency:current"
	interrupted := errors.New("interrupt after target write")
	_, err := publishWorkflowDevelopmentTransaction(
		context.Background(),
		fixture.workspace,
		&fixture.request,
		fixture.runtime,
		readyWorkflowDevelopmentPublishGate("dependency:current"),
		&workflowDevelopmentPublishHooks{
			afterBoundary: func(boundary workflowDevelopmentPublishBoundary) error {
				if boundary == workflowDevelopmentPublishBoundaryTargetWritten {
					return interrupted
				}
				return nil
			},
			leaveJournalOnError: true,
		},
		fixture.localOptions...,
	)
	if !errors.Is(err, interrupted) {
		t.Fatalf("publish error = %v, want interruption", err)
	}
	const operatorEdit = "operator edit outside transaction\n"
	if writeErr := os.WriteFile(fixture.targetPath, []byte(operatorEdit), 0o640); writeErr != nil {
		t.Fatal(writeErr)
	}

	session, err := GetWorkflowDevelopmentSession(fixture.workspace)
	if session != nil {
		t.Fatalf("session = %#v, want no partial read", session)
	}
	if !errors.Is(err, ErrWorkflowDevelopmentPublishRecoveryFailed) ||
		!errors.Is(err, ErrWorkflowRecoveryConflict) {
		t.Fatalf("GetWorkflowDevelopmentSession() error = %v, want recovery conflict", err)
	}
	assertFileData(t, fixture.targetPath, []byte(operatorEdit))
	if _, statErr := os.Stat(
		workflowDevelopmentPublishJournalPath(fixture.workspace),
	); statErr != nil {
		t.Fatalf("publish journal stat = %v, want retained journal", statErr)
	}
}

func readyWorkflowDevelopmentPublishGate(
	revision string,
) WorkflowDevelopmentPublishGate {
	return func(
		context.Context,
		WorkflowDevelopmentPublishGateInput,
	) (WorkflowDevelopmentPublishGateResult, error) {
		return WorkflowDevelopmentPublishGateResult{
			Revision: revision,
			Ready:    true,
		}, nil
	}
}
