package workflows

import (
	"context"
	"errors"
	"os"
	"testing"
)

func TestTemplateCatalogOperationsPreservePublishRecoveryConflict(t *testing.T) {
	for _, operation := range []struct {
		name string
		run  func(context.Context, *workflowDevelopmentPublishFixture) error
	}{
		{
			name: "list",
			run: func(ctx context.Context, fixture *workflowDevelopmentPublishFixture) error {
				_, err := ListWorkflowTemplates(
					ctx,
					fixture.workspace,
					fixture.localOptions...,
				)
				return err
			},
		},
		{
			name: "install",
			run: func(ctx context.Context, fixture *workflowDevelopmentPublishFixture) error {
				_, err := InstallWorkflowTemplateWithCompatibility(
					ctx,
					fixture.workspace,
					CodeReviewWorkflowName,
					false,
					fixture.runtime,
					fixture.localOptions...,
				)
				return err
			},
		},
	} {
		t.Run(operation.name, func(t *testing.T) {
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
					afterBoundary: func(
						boundary workflowDevelopmentPublishBoundary,
					) error {
						if boundary ==
							workflowDevelopmentPublishBoundaryTargetWritten {
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
			if writeErr := os.WriteFile(
				fixture.targetPath,
				[]byte("operator edit outside transaction\n"),
				0o640,
			); writeErr != nil {
				t.Fatal(writeErr)
			}

			err = operation.run(context.Background(), fixture)
			if !errors.Is(err, ErrWorkflowRecoveryConflict) ||
				!errors.Is(err, ErrWorkflowDevelopmentPublishRecoveryFailed) {
				t.Fatalf("operation error = %v, want preserved recovery conflict", err)
			}
			if operation.name == "list" &&
				!errors.Is(err, ErrWorkflowTemplateCatalogUnavailable) {
				t.Fatalf("list error = %v, want catalog unavailable", err)
			}
			if operation.name == "install" &&
				!errors.Is(err, ErrWorkflowTemplateInstallFailed) {
				t.Fatalf("install error = %v, want install failed", err)
			}
		})
	}
}
