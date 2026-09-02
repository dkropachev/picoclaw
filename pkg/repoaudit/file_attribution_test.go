package repoaudit

import (
	"context"
	"errors"
	"os"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestRepositoryReviewFileAttributionConstructorAndMergeCAS(t *testing.T) {
	if !ValidRepositoryReviewAutomationID("rra_file_attribution") ||
		ValidRepositoryReviewAutomationID("invalid") {
		t.Fatal("exported automation ID validation disagrees with durable identity rules")
	}
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
	if _, forgedErr := NewRepositoryReviewFileAttribution(forged); forgedErr == nil {
		t.Fatal("forged attribution ID accepted")
	}
	legacy := repositoryReviewFileAttributionForTest(4)
	legacy.Source = RepositoryReviewFileAttributionSourceLegacyManagedChild
	legacy.Model = "review"
	legacy.ModelAlias = ""
	legacy.Account = ""
	if _, legacyErr := NewRepositoryReviewFileAttribution(legacy); legacyErr != nil {
		t.Fatalf("legacy ambiguous provenance rejected: %v", legacyErr)
	}
	liveAmbiguous := legacy
	liveAmbiguous.Source = RepositoryReviewFileAttributionSourceLiveCheckpoint
	if _, liveErr := NewRepositoryReviewFileAttribution(liveAmbiguous); liveErr == nil {
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
	if _, conflictErr := store.MergeRepositoryReviewFileAttributions(
		context.Background(),
		MergeRepositoryReviewFileAttributionsRequest{
			Repository: repository, ExpectedVersion: 1,
			Attributions: []RepositoryReviewFileAttribution{conflict},
		},
	); !errors.Is(conflictErr, ErrConflict) {
		t.Fatalf("conflicting logical child error = %v", conflictErr)
	}
	stale := repositoryReviewFileAttributionForTest(5)
	if _, staleErr := store.MergeRepositoryReviewFileAttributions(
		context.Background(),
		MergeRepositoryReviewFileAttributionsRequest{
			Repository: repository, ExpectedVersion: 0,
			Attributions: []RepositoryReviewFileAttribution{stale},
		},
	); !errors.Is(staleErr, ErrConflict) {
		t.Fatalf("stale merge error = %v", staleErr)
	}
	loaded, found, err := store.Get(repository)
	if err != nil || !found || !reflect.DeepEqual(loaded.FileAttributions, merged.FileAttributions) {
		t.Fatalf("loaded attributions = %#v, %v", loaded.FileAttributions, err)
	}
}

func TestRepositoryReviewFileAttributionValidationAndMergeBoundaries(t *testing.T) {
	valid, err := NewRepositoryReviewFileAttribution(repositoryReviewFileAttributionForTest(1))
	if err != nil {
		t.Fatal(err)
	}
	invalidFiles := valid
	invalidFiles.ID = ""
	invalidFiles.AcknowledgedFiles = nil
	_, invalidFilesErr := NewRepositoryReviewFileAttribution(invalidFiles)
	if !errors.Is(invalidFilesErr, ErrInvalidPlan) {
		t.Fatalf("empty acknowledged files error = %v", invalidFilesErr)
	}

	if _, nilContextErr := (Store{}).MergeRepositoryReviewFileAttributions(nil,
		MergeRepositoryReviewFileAttributionsRequest{},
	); !errors.Is(nilContextErr, ErrInvalidPlan) {
		t.Fatalf("nil-context invalid merge error = %v", nilContextErr)
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, canceledMergeErr := NewStore(t.TempDir()).MergeRepositoryReviewFileAttributions(
		canceled,
		MergeRepositoryReviewFileAttributionsRequest{
			Repository: "owner/canceled", Attributions: []RepositoryReviewFileAttribution{valid},
		},
	); !errors.Is(canceledMergeErr, context.Canceled) {
		t.Fatalf("canceled merge error = %v", canceledMergeErr)
	}

	store := NewStore(t.TempDir())
	repository := "owner/merge-boundaries"
	invalidAttribution := valid
	invalidAttribution.ID = ""
	invalidAttribution.RunID = ""
	if _, invalidAttributionErr := store.MergeRepositoryReviewFileAttributions(
		t.Context(),
		MergeRepositoryReviewFileAttributionsRequest{
			Repository: repository, Attributions: []RepositoryReviewFileAttribution{invalidAttribution},
		},
	); !errors.Is(invalidAttributionErr, ErrInvalidPlan) {
		t.Fatalf("invalid attribution merge error = %v", invalidAttributionErr)
	}
	duplicateConflict := valid
	duplicateConflict.Model = "provider/other"
	if _, duplicateConflictErr := store.MergeRepositoryReviewFileAttributions(
		t.Context(),
		MergeRepositoryReviewFileAttributionsRequest{
			Repository:   repository,
			Attributions: []RepositoryReviewFileAttribution{valid, duplicateConflict},
		},
	); !errors.Is(duplicateConflictErr, ErrConflict) {
		t.Fatalf("conflicting request duplicate error = %v", duplicateConflictErr)
	}

	lockFailure := NewStore(t.TempDir())
	lockPath := repositoryReviewTestLockPath(t, lockFailure.root, "store.lock")
	if mkdirLockErr := os.MkdirAll(lockPath, 0o700); mkdirLockErr != nil {
		t.Fatal(mkdirLockErr)
	}
	if _, lockMergeErr := lockFailure.MergeRepositoryReviewFileAttributions(
		t.Context(),
		MergeRepositoryReviewFileAttributionsRequest{
			Repository: repository, Attributions: []RepositoryReviewFileAttribution{valid},
		},
	); lockMergeErr == nil {
		t.Fatal("merge ignored lock failure")
	}

	staged := &repositoryReviewStagedErrorContext{}
	if _, stagedContextErr := NewStore(t.TempDir()).MergeRepositoryReviewFileAttributions(
		staged,
		MergeRepositoryReviewFileAttributionsRequest{
			Repository: repository, Attributions: []RepositoryReviewFileAttribution{valid},
		},
	); !errors.Is(stagedContextErr, context.Canceled) || staged.calls.Load() < 2 {
		t.Fatalf("post-lock cancellation error = %v, calls=%d", stagedContextErr, staged.calls.Load())
	}

	loadFailure := NewStore(t.TempDir())
	wantLoadErr := errors.New("load attribution state")
	loadFailure.loadForTest = func(string) (RepositoryState, error) {
		return RepositoryState{}, wantLoadErr
	}
	if _, loadMergeErr := loadFailure.MergeRepositoryReviewFileAttributions(
		t.Context(),
		MergeRepositoryReviewFileAttributionsRequest{
			Repository: repository, Attributions: []RepositoryReviewFileAttribution{valid},
		},
	); !errors.Is(loadMergeErr, wantLoadErr) {
		t.Fatalf("merge load error = %v", loadMergeErr)
	}

	persistFailure := NewStore(t.TempDir())
	initial, err := persistFailure.load(repository)
	if err != nil {
		t.Fatal(err)
	}
	if removeErr := os.RemoveAll(persistFailure.root); removeErr != nil {
		t.Fatal(removeErr)
	}
	if writeErr := os.WriteFile(persistFailure.root, []byte("not-a-directory"), 0o600); writeErr != nil {
		t.Fatal(writeErr)
	}
	persistFailure.loadForTest = func(string) (RepositoryState, error) { return initial, nil }
	if _, persistMergeErr := persistFailure.MergeRepositoryReviewFileAttributions(
		t.Context(),
		MergeRepositoryReviewFileAttributionsRequest{
			Repository: repository, Attributions: []RepositoryReviewFileAttribution{valid},
		},
	); persistMergeErr == nil {
		t.Fatal("merge ignored persistence failure")
	}
}

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
	if saveErr := fixture.store.save(&legacy); saveErr != nil {
		t.Fatal(saveErr)
	}
	insufficient := checkpoint
	insufficient.CompletedAt = time.Time{}
	if _, insufficientErr := fixture.store.CheckpointRepositoryReviewAssignment(
		t.Context(), insufficient,
	); !errors.Is(insufficientErr, ErrConflict) {
		t.Fatalf("timestamp-free legacy repair error = %v", insufficientErr)
	}
	repaired, err := fixture.store.CheckpointRepositoryReviewAssignment(t.Context(), checkpoint)
	if err != nil || !repaired.Idempotent || len(repaired.State.FileAttributions) != 1 ||
		repaired.State.Version != legacy.Version+1 ||
		repaired.State.ReviewVersion != legacy.ReviewVersion {
		t.Fatalf("repaired checkpoint = %#v, %v", repaired, err)
	}
	assignment := fixture.catalog[0]
	if _, err := reconcileRepositoryReviewCheckpointAttribution(
		nil, checkpoint, assignment, fixture.files,
	); !errors.Is(err, ErrInvalidPlan) {
		t.Fatalf("nil replay state error = %v", err)
	}
	timestampConflict := checkpoint
	timestampConflict.CompletedAt = repositoryAuditTestNow.Add(time.Second)
	if _, err := reconcileRepositoryReviewCheckpointAttribution(
		&repaired.State, timestampConflict, assignment, fixture.files,
	); !errors.Is(err, ErrConflict) {
		t.Fatalf("replay timestamp conflict error = %v", err)
	}
	duplicate := repaired.State
	duplicate.FileAttributions = append(
		duplicate.FileAttributions,
		cloneRepositoryReviewFileAttribution(duplicate.FileAttributions[0]),
	)
	if _, err := reconcileRepositoryReviewCheckpointAttribution(
		&duplicate, checkpoint, assignment, fixture.files,
	); !errors.Is(err, ErrConflict) {
		t.Fatalf("duplicate retained attribution error = %v", err)
	}
	tampered := repaired.State
	tampered.FileAttributions = append(
		[]RepositoryReviewFileAttribution(nil), tampered.FileAttributions...,
	)
	tampered.FileAttributions[0].Model = "provider/tampered"
	if _, err := reconcileRepositoryReviewCheckpointAttribution(
		&tampered, checkpoint, assignment, fixture.files,
	); !errors.Is(err, ErrConflict) {
		t.Fatalf("tampered retained attribution error = %v", err)
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

func TestRepositoryReviewCheckpointAttributionConflictAndRepairPersistenceFailure(t *testing.T) {
	fixture := newAssignmentCoverageFixture(t, 1, 1)
	if _, err := fixture.store.BeginRepositoryReviewRun(
		t.Context(),
		BeginRepositoryReviewRunRequest{
			Plan: fixture.plan, RunID: "conflict-run", ReviewableFiles: fixture.files,
		},
	); err != nil {
		t.Fatal(err)
	}
	checkpoint := assignmentCoverageCheckpoint(fixture, "conflict-run", 0, fixture.files)
	checkpoint.CompletedAt = repositoryAuditTestNow
	state, found, err := fixture.store.Get(fixture.repository)
	if err != nil || !found {
		t.Fatalf("active state found=%v err=%v", found, err)
	}
	conflicting := repositoryReviewCheckpointAttribution(
		checkpoint, fixture.catalog[0], fixture.files, checkpoint.CompletedAt,
	)
	conflicting.Model = "provider/conflicting"
	conflicting, err = NewRepositoryReviewFileAttribution(conflicting)
	if err != nil {
		t.Fatal(err)
	}
	state.FileAttributions = []RepositoryReviewFileAttribution{conflicting}
	if saveConflictStateErr := fixture.store.save(&state); saveConflictStateErr != nil {
		t.Fatal(saveConflictStateErr)
	}
	if _, checkpointConflictErr := fixture.store.CheckpointRepositoryReviewAssignment(
		t.Context(), checkpoint,
	); !errors.Is(checkpointConflictErr, ErrConflict) {
		t.Fatalf("fresh checkpoint attribution conflict error = %v", checkpointConflictErr)
	}

	repairFixture := newAssignmentCoverageFixture(t, 1, 1)
	if _, beginRepairErr := repairFixture.store.BeginRepositoryReviewRun(
		t.Context(),
		BeginRepositoryReviewRunRequest{
			Plan: repairFixture.plan, RunID: "repair-save-run", ReviewableFiles: repairFixture.files,
		},
	); beginRepairErr != nil {
		t.Fatal(beginRepairErr)
	}
	repair := assignmentCoverageCheckpoint(
		repairFixture, "repair-save-run", 0, repairFixture.files,
	)
	repair.CompletedAt = repositoryAuditTestNow
	committed, err := repairFixture.store.CheckpointRepositoryReviewAssignment(t.Context(), repair)
	if err != nil {
		t.Fatal(err)
	}
	legacy := committed.State
	legacy.FileAttributions = []RepositoryReviewFileAttribution{}
	reservation := legacy.ActiveReviewRun.Reservations[repair.AssignmentID]
	reservation.CheckpointDigest = repositoryReviewLegacyCheckpointRequestDigest(repair)
	legacy.ActiveReviewRun.Reservations[repair.AssignmentID] = reservation
	broken := repositoryReviewAttributionPersistenceFailureStore(t, legacy)
	if _, err := broken.CheckpointRepositoryReviewAssignment(
		t.Context(), repair,
	); err == nil {
		t.Fatal("checkpoint attribution repair ignored persistence failure")
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

func repositoryReviewAttributionPersistenceFailureStore(
	t *testing.T,
	state RepositoryState,
) Store {
	t.Helper()
	store := NewStore(t.TempDir())
	if err := os.WriteFile(store.root, []byte("not-a-directory"), 0o600); err != nil {
		t.Fatal(err)
	}
	store.loadForTest = func(string) (RepositoryState, error) { return state, nil }
	return store
}
