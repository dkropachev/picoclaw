package gitworkspace

import (
	"context"
	"strings"
	"testing"
)

func TestSnapshotPinnedPlanningEvidenceReturnsBoundedExactCommittedText(t *testing.T) {
	fixture := newPinnedLineTestFixture(t, "development-planning/evidence")
	evidence, err := fixture.manager.SnapshotPinnedPlanningEvidence(
		context.Background(),
		PinnedCandidateRequest{Pin: fixture.pin, WorkspaceID: fixture.workspace.ID},
	)
	if err != nil {
		t.Fatal(err)
	}
	if evidence.Commit != fixture.pin.ExpectedCommit || len(evidence.Files) == 0 ||
		len(evidence.Files) > maxPlanningFiles {
		t.Fatalf("planning evidence = %#v", evidence)
	}
	foundREADME := false
	for _, file := range evidence.Files {
		if file.Path != "README.md" {
			continue
		}
		foundREADME = true
		if file.Content == "" || !strings.Contains(file.Content, "repo") {
			t.Fatalf("README evidence = %#v", file)
		}
	}
	if !foundREADME {
		t.Fatalf("README missing from evidence: %#v", evidence.Files)
	}
}
