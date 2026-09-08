package repoaudit

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestRepositoryReviewFileAttributionAppendAndStateValidationBoundaries(t *testing.T) {
	valid, err := NewRepositoryReviewFileAttribution(repositoryReviewFileAttributionForTest(1))
	if err != nil {
		t.Fatal(err)
	}
	if _, nilAppendErr := appendRepositoryReviewFileAttribution(nil, valid); nilAppendErr == nil {
		t.Fatal("nil attribution state accepted")
	}
	invalid := valid
	invalid.ID = ""
	invalid.RunID = ""
	_, invalidAppendErr := appendRepositoryReviewFileAttribution(&RepositoryState{}, invalid)
	if invalidAppendErr == nil {
		t.Fatal("invalid attribution append accepted")
	}
	state := RepositoryState{FileAttributions: []RepositoryReviewFileAttribution{
		repositoryReviewFileAttributionForTest(2), valid,
	}}
	changed, err := appendRepositoryReviewFileAttribution(&state, valid)
	if err != nil || changed {
		t.Fatalf("exact append replay = changed %v, err %v", changed, err)
	}
	conflict := valid
	conflict.Model = "provider/conflict"
	if _, conflictAppendErr := appendRepositoryReviewFileAttribution(&state, conflict); !errors.Is(
		conflictAppendErr,
		ErrConflict,
	) {
		t.Fatalf("conflicting append error = %v", conflictAppendErr)
	}
	full := RepositoryState{FileAttributions: make(
		[]RepositoryReviewFileAttribution, maxRepositoryReviewFileAttributions,
	)}
	if _, fullAppendErr := appendRepositoryReviewFileAttribution(&full, valid); fullAppendErr == nil {
		t.Fatal("attribution append exceeded record limit")
	}

	if err := validateRepositoryReviewFileAttributions(make(
		[]RepositoryReviewFileAttribution, maxRepositoryReviewFileAttributions+1,
	)); err == nil {
		t.Fatal("oversized attribution state accepted")
	}
	if err := validateRepositoryReviewFileAttributions([]RepositoryReviewFileAttribution{invalid}); err == nil {
		t.Fatal("invalid attribution state accepted")
	}
	if err := validateRepositoryReviewFileAttributions([]RepositoryReviewFileAttribution{valid, valid}); err == nil {
		t.Fatal("duplicate attribution state accepted")
	}
	if err := validateRepositoryReviewFileAttributionsWithCreditLimit(
		[]RepositoryReviewFileAttribution{valid}, 0,
	); err == nil {
		t.Fatal("attribution file-credit limit was ignored")
	}
}

func TestRepositoryReviewCheckpointPersistsLiveFileAttribution(t *testing.T) {
	fixture := newAssignmentCoverageFixture(t, 1, 1)
	if _, err := fixture.store.BeginRepositoryReviewRun(
		context.Background(),
		BeginRepositoryReviewRunRequest{
			Plan: fixture.plan, RunID: "attributed-run", ReviewableFiles: fixture.files,
		},
	); err != nil {
		t.Fatal(err)
	}
	checkpoint := assignmentCoverageCheckpoint(fixture, "attributed-run", 0, fixture.files)
	checkpoint.AgentID = "main"
	checkpoint.ChildIndex = 7
	checkpoint.CompletedAt = repositoryAuditTestNow
	result, err := fixture.store.CheckpointRepositoryReviewAssignment(
		context.Background(), checkpoint,
	)
	if err != nil || len(result.State.FileAttributions) != 1 {
		t.Fatalf("checkpoint attributions = %#v, %v", result.State.FileAttributions, err)
	}
	attribution := result.State.FileAttributions[0]
	assignment := fixture.catalog[0]
	if attribution.Source != RepositoryReviewFileAttributionSourceLiveCheckpoint ||
		attribution.AutomationID != checkpoint.AutomationID ||
		attribution.RunID != checkpoint.RunID || attribution.AssignmentID != checkpoint.AssignmentID ||
		attribution.FocusID != assignment.FocusID || attribution.ReviewerIdentity != assignment.Reviewer ||
		attribution.RootAgentID != "main" || attribution.ChildIndex != 7 ||
		attribution.Model != checkpoint.Observation.Model ||
		attribution.ModelAlias != checkpoint.Observation.ModelAlias ||
		attribution.Account != checkpoint.Observation.Account || !attribution.Required ||
		attribution.EvidenceDigest != checkpoint.Observation.RawDigest ||
		!reflect.DeepEqual(attribution.AcknowledgedFiles, fixture.files) ||
		!attribution.CompletedAt.Equal(repositoryAuditTestNow) {
		t.Fatalf("live attribution = %#v", attribution)
	}
	replayed, err := fixture.store.CheckpointRepositoryReviewAssignment(
		context.Background(), checkpoint,
	)
	if err != nil || !replayed.Idempotent || len(replayed.State.FileAttributions) != 1 {
		t.Fatalf("checkpoint replay = %#v, %v", replayed, err)
	}
}

func TestRepositoryReviewCheckpointRequiresExactAttributionProvenance(t *testing.T) {
	fixture := newAssignmentCoverageFixture(t, 1, 1)
	if _, err := fixture.store.BeginRepositoryReviewRun(
		t.Context(),
		BeginRepositoryReviewRunRequest{
			Plan: fixture.plan, RunID: "provenance-run", ReviewableFiles: fixture.files,
		},
	); err != nil {
		t.Fatal(err)
	}
	valid := assignmentCoverageCheckpoint(fixture, "provenance-run", 0, fixture.files)
	for name, mutate := range map[string]func(*CheckpointRepositoryReviewAssignmentRequest){
		"automation missing": func(request *CheckpointRepositoryReviewAssignmentRequest) {
			request.AutomationID = ""
		},
		"automation invalid": func(request *CheckpointRepositoryReviewAssignmentRequest) {
			request.AutomationID = "not-an-automation"
		},
		"agent missing": func(request *CheckpointRepositoryReviewAssignmentRequest) {
			request.AgentID = ""
		},
		"agent noncanonical": func(request *CheckpointRepositoryReviewAssignmentRequest) {
			request.AgentID = "Main Agent"
		},
		"child missing": func(request *CheckpointRepositoryReviewAssignmentRequest) {
			request.ChildIndex = 0
		},
	} {
		t.Run(name, func(t *testing.T) {
			request := valid
			mutate(&request)
			if _, err := fixture.store.CheckpointRepositoryReviewAssignment(t.Context(), request); err == nil {
				t.Fatal("checkpoint accepted incomplete attribution provenance")
			}
		})
	}
	if _, err := fixture.store.CheckpointRepositoryReviewAssignment(t.Context(), valid); err != nil {
		t.Fatalf("valid checkpoint rejected: %v", err)
	}
}

func repositoryReviewFileAttributionForTest(childIndex int) RepositoryReviewFileAttribution {
	return RepositoryReviewFileAttribution{
		AutomationID: "rra_file_attribution", RunID: "run",
		CommitSHA: strings.Repeat("a", 40), InventoryHash: "inventory",
		ProfileHash: "sha256:" + strings.Repeat("b", 64), AssignmentID: "assignment",
		FocusID: RepositoryReviewFocusSecurityTrust, RootAgentID: "main",
		ReviewerIdentity: "review", Model: "provider/review", ModelAlias: "review",
		Account: "account", UsageModel: "provider/review",
		AcknowledgedFiles: []FileRef{repositoryAuditTestFile("file.go", "c", 1)},
		EvidenceDigest:    "sha256:" + strings.Repeat("d", 64),
		Source:            RepositoryReviewFileAttributionSourceLiveCheckpoint,
		ChildIndex:        childIndex, Required: true, CompletedAt: repositoryAuditTestNow,
	}
}

type repositoryReviewStagedErrorContext struct {
	calls atomic.Int32
}

func (ctx *repositoryReviewStagedErrorContext) Deadline() (time.Time, bool) {
	return time.Time{}, false
}
func (ctx *repositoryReviewStagedErrorContext) Done() <-chan struct{} { return nil }
func (ctx *repositoryReviewStagedErrorContext) Value(any) any         { return nil }
func (ctx *repositoryReviewStagedErrorContext) Err() error {
	if ctx.calls.Add(1) > 1 {
		return context.Canceled
	}
	return nil
}

var _ context.Context = (*repositoryReviewStagedErrorContext)(nil)
