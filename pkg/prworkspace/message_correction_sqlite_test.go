//go:build !mipsle && !netbsd && !(freebsd && arm)

package prworkspace

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/sipeed/picoclaw/pkg/eventing"
)

func TestMessageCorrectionPersistsAtomicallyInEventingStore(t *testing.T) {
	raw, err := eventing.Open(context.Background(), filepath.Join(t.TempDir(), "message-correction.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := raw.Close(); err != nil {
			t.Error(err)
		}
	})
	service, before := messageCorrectionTestService(t, NewEventingStore(raw))
	result, err := service.AddMessage(context.Background(), AddMessageRequest{
		WorkspaceID: before.Workspace.ID, ExpectedVersion: before.Workspace.Version,
		RequestID: "request-sqlite-message-correction", Stage: "workspace",
		Content:          "Use the repository retry limit in both review and implementation.",
		MarkAsCorrection: true, Applicability: CorrectionReviewAndImpl,
	})
	if err != nil {
		t.Fatal(err)
	}
	assertMessageCorrectionResult(t, result, before.Workspace.Version, CorrectionReviewAndImpl)

	persisted, err := service.Get(context.Background(), before.Workspace.ID)
	if err != nil {
		t.Fatal(err)
	}
	assertMessageCorrectionResult(t, persisted, before.Workspace.Version, CorrectionReviewAndImpl)
	reviewContext := reviewContextBundle(persisted)
	implementationContext := implementationContextBundle(persisted)
	if len(reviewContext.Messages) != 1 || len(implementationContext.Messages) != 1 ||
		len(reviewContext.Corrections) != 1 || len(implementationContext.Corrections) != 1 {
		t.Fatalf("shared correction was not projected into both prompts: review=%d implementation=%d",
			len(reviewContext.Corrections), len(implementationContext.Corrections))
	}
}
