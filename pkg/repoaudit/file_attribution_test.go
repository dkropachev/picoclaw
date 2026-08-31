package repoaudit

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestRepositoryReviewFileAttributionConstructorAndMergeCAS(t *testing.T) {
	store := NewStore(t.TempDir())
	store.now = func() time.Time { return repositoryAuditTestNow }
	repository := "owner/file-attribution"
	input := repositoryReviewFileAttributionForTest(3)
	input.RunID = " run-one "
	input.CommitSHA = strings.ToUpper(input.CommitSHA)
	input.AcknowledgedFiles = []FileRef{
		repositoryAuditTestFile("z.go", "c", 2),
		repositoryAuditTestFile("a.go", "d", 1),
	}

	attribution, err := NewRepositoryReviewFileAttribution(input)
	if err != nil {
		t.Fatal(err)
	}
	if attribution.ID == "" || attribution.RunID != "run-one" ||
		attribution.CommitSHA != strings.ToLower(input.CommitSHA) ||
		attribution.AcknowledgedFiles[0].Path != "a.go" {
		t.Fatalf("canonical attribution = %#v", attribution)
	}
	stableVariant := attribution
	stableVariant.ID = ""
	stableVariant.Source = RepositoryReviewFileAttributionSourceLegacyManagedChild
	stableVariant.AssignmentID = "historical-assignment"
	stableVariant.FocusID = RepositoryReviewFocusConcurrencyRecovery
	stable, err := NewRepositoryReviewFileAttribution(stableVariant)
	if err != nil || stable.ID != attribution.ID {
		t.Fatalf("source-neutral attribution ID = %q, want %q, err=%v", stable.ID, attribution.ID, err)
	}
	otherOwner := attribution
	otherOwner.ID = ""
	otherOwner.AutomationID = "rra_other_owner"
	owned, err := NewRepositoryReviewFileAttribution(otherOwner)
	if err != nil || owned.ID == attribution.ID {
		t.Fatalf("automation-owned attribution ID = %q, original %q, err=%v", owned.ID, attribution.ID, err)
	}
	forged := attribution
	forged.ID = "rfa_forged"
	if _, err := NewRepositoryReviewFileAttribution(forged); err == nil {
		t.Fatal("forged attribution ID accepted")
	}
	legacy := repositoryReviewFileAttributionForTest(4)
	legacy.Source = RepositoryReviewFileAttributionSourceLegacyManagedChild
	legacy.Model = "review"
	legacy.ModelAlias = ""
	legacy.Account = ""
	if _, err := NewRepositoryReviewFileAttribution(legacy); err != nil {
		t.Fatalf("legacy ambiguous provenance rejected: %v", err)
	}
	liveAmbiguous := legacy
	liveAmbiguous.Source = RepositoryReviewFileAttributionSourceLiveCheckpoint
	if _, err := NewRepositoryReviewFileAttribution(liveAmbiguous); err == nil {
		t.Fatal("live ambiguous provenance accepted")
	}

	request := MergeRepositoryReviewFileAttributionsRequest{
		Repository: repository, ExpectedVersion: 0,
		Attributions: []RepositoryReviewFileAttribution{
			cloneRepositoryReviewFileAttribution(attribution),
			cloneRepositoryReviewFileAttribution(attribution),
		},
	}
	merged, err := store.MergeRepositoryReviewFileAttributions(context.Background(), request)
	if err != nil || merged.Version != 1 || merged.ReviewVersion != 0 ||
		len(merged.FileAttributions) != 1 {
		t.Fatalf("merged attribution = %#v, %v", merged, err)
	}
	request.Attributions[0].AcknowledgedFiles[0].Path = "mutated.go"
	if merged.FileAttributions[0].AcknowledgedFiles[0].Path != "a.go" {
		t.Fatal("merge retained caller-owned file slice")
	}

	replay := MergeRepositoryReviewFileAttributionsRequest{
		Repository: repository, ExpectedVersion: 0,
		Attributions: []RepositoryReviewFileAttribution{attribution},
	}
	replayed, err := store.MergeRepositoryReviewFileAttributions(context.Background(), replay)
	if err != nil || replayed.Version != 1 || len(replayed.FileAttributions) != 1 {
		t.Fatalf("idempotent merge = %#v, %v", replayed, err)
	}
	conflict := attribution
	conflict.Model = "provider/other"
	if _, err := store.MergeRepositoryReviewFileAttributions(
		context.Background(),
		MergeRepositoryReviewFileAttributionsRequest{
			Repository: repository, ExpectedVersion: 1,
			Attributions: []RepositoryReviewFileAttribution{conflict},
		},
	); !errors.Is(err, ErrConflict) {
		t.Fatalf("conflicting logical child error = %v", err)
	}
	stale := repositoryReviewFileAttributionForTest(5)
	if _, err := store.MergeRepositoryReviewFileAttributions(
		context.Background(),
		MergeRepositoryReviewFileAttributionsRequest{
			Repository: repository, ExpectedVersion: 0,
			Attributions: []RepositoryReviewFileAttribution{stale},
		},
	); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale merge error = %v", err)
	}
	loaded, found, err := store.Get(repository)
	if err != nil || !found || !reflect.DeepEqual(loaded.FileAttributions, merged.FileAttributions) {
		t.Fatalf("loaded attributions = %#v, %v", loaded.FileAttributions, err)
	}
}

func TestRepositoryReviewFileAttributionSchemaMigration(t *testing.T) {
	state := RepositoryState{SchemaVersion: 4, Findings: []Finding{{ID: "schema-four-finding"}}}
	migrated, err := migrateRepositoryState(&state)
	if err != nil || !migrated || state.SchemaVersion != SchemaVersion ||
		state.FileAttributions == nil || len(state.FileAttributions) != 0 ||
		state.HistoricalDeduplication.Required {
		t.Fatalf("migrated state = %#v, %v", state, err)
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

func TestRepositoryReviewCheckpointReplayVerifiesAndRepairsAttribution(t *testing.T) {
	fixture := newAssignmentCoverageFixture(t, 1, 1)
	if _, err := fixture.store.BeginRepositoryReviewRun(
		t.Context(),
		BeginRepositoryReviewRunRequest{
			Plan: fixture.plan, RunID: "repair-run", ReviewableFiles: fixture.files,
		},
	); err != nil {
		t.Fatal(err)
	}
	checkpoint := assignmentCoverageCheckpoint(fixture, "repair-run", 0, fixture.files)
	checkpoint.CompletedAt = repositoryAuditTestNow
	committed, err := fixture.store.CheckpointRepositoryReviewAssignment(t.Context(), checkpoint)
	if err != nil || len(committed.State.FileAttributions) != 1 {
		t.Fatalf("checkpoint = %#v, %v", committed, err)
	}

	legacy := committed.State
	legacy.FileAttributions = []RepositoryReviewFileAttribution{}
	reservation := legacy.ActiveReviewRun.Reservations[checkpoint.AssignmentID]
	reservation.CheckpointDigest = repositoryReviewLegacyCheckpointRequestDigest(checkpoint)
	legacy.ActiveReviewRun.Reservations[checkpoint.AssignmentID] = reservation
	if err := fixture.store.save(&legacy); err != nil {
		t.Fatal(err)
	}
	insufficient := checkpoint
	insufficient.CompletedAt = time.Time{}
	if _, err := fixture.store.CheckpointRepositoryReviewAssignment(
		t.Context(), insufficient,
	); !errors.Is(err, ErrConflict) {
		t.Fatalf("timestamp-free legacy repair error = %v", err)
	}
	repaired, err := fixture.store.CheckpointRepositoryReviewAssignment(t.Context(), checkpoint)
	if err != nil || !repaired.Idempotent || len(repaired.State.FileAttributions) != 1 ||
		repaired.State.Version != legacy.Version+1 ||
		repaired.State.ReviewVersion != legacy.ReviewVersion {
		t.Fatalf("repaired checkpoint = %#v, %v", repaired, err)
	}
	for name, mutate := range map[string]func(*CheckpointRepositoryReviewAssignmentRequest){
		"automation": func(request *CheckpointRepositoryReviewAssignmentRequest) {
			request.AutomationID = "rra_other_automation"
		},
		"agent": func(request *CheckpointRepositoryReviewAssignmentRequest) {
			request.AgentID = "other-agent"
		},
		"child": func(request *CheckpointRepositoryReviewAssignmentRequest) {
			request.ChildIndex++
		},
	} {
		t.Run(name, func(t *testing.T) {
			conflict := checkpoint
			mutate(&conflict)
			if _, err := fixture.store.CheckpointRepositoryReviewAssignment(
				t.Context(), conflict,
			); !errors.Is(err, ErrConflict) {
				t.Fatalf("conflicting replay error = %v", err)
			}
		})
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
