package repoaudit

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestRepositoryReviewProfileDeduplicationDefaultsExplicitZeroAndValidation(t *testing.T) {
	store := NewStore(t.TempDir())
	store.now = func() time.Time { return repositoryAuditTestNow }
	defaults, err := store.CreateProfile(
		context.Background(), validProfileForTest("rrpf_dedup_defaults", "Dedup defaults"),
	)
	if err != nil {
		t.Fatal(err)
	}
	if defaults.DeduplicationSimilarityThreshold != DeduplicationDefaultThreshold ||
		defaults.DeduplicationCandidateLimit != DeduplicationDefaultCandidateLimit {
		t.Fatalf("deduplication defaults = %#v", defaults)
	}

	explicit := validProfileForTest("rrpf_dedup_zero", "Dedup zero")
	explicit.DeduplicationSettingsSpecified = true
	created, err := store.CreateProfile(context.Background(), explicit)
	if err != nil {
		t.Fatal(err)
	}
	reloaded, found, err := store.GetProfile(context.Background(), created.ID)
	if err != nil || !found || reloaded.DeduplicationSimilarityThreshold != 0 ||
		reloaded.DeduplicationCandidateLimit != 0 {
		t.Fatalf("explicit zero profile = %#v, found=%v, err=%v", reloaded, found, err)
	}
	automation, err := MaterializeRepositoryReviewAutomation(
		reloaded,
		validAutomationForTest("rra_dedup_zero", "Dedup zero automation"),
	)
	if err != nil {
		t.Fatal(err)
	}
	createdAutomation, err := store.CreateAutomation(context.Background(), automation)
	if err != nil {
		t.Fatal(err)
	}
	updatedAutomation, err := store.UpdateAutomation(
		context.Background(), createdAutomation.ID, createdAutomation.Version,
		func(*RepositoryReviewAutomation) error { return nil },
	)
	if err != nil || updatedAutomation.DeduplicationSimilarityThreshold != 0 ||
		updatedAutomation.DeduplicationCandidateLimit != 0 {
		t.Fatalf("explicit zero automation drifted = %#v, err=%v", updatedAutomation, err)
	}

	for name, mutate := range map[string]func(*RepositoryReviewProfile){
		"threshold below zero": func(profile *RepositoryReviewProfile) {
			profile.DeduplicationSimilarityThreshold = -1
		},
		"threshold above one hundred": func(profile *RepositoryReviewProfile) {
			profile.DeduplicationSimilarityThreshold = 101
		},
		"candidate below zero": func(profile *RepositoryReviewProfile) {
			profile.DeduplicationCandidateLimit = -1
		},
		"candidate above twenty": func(profile *RepositoryReviewProfile) {
			profile.DeduplicationCandidateLimit = 21
		},
	} {
		t.Run(name, func(t *testing.T) {
			_, updateErr := store.UpdateProfile(
				context.Background(), defaults.ID, defaults.Version,
				func(profile *RepositoryReviewProfile) error {
					mutate(profile)
					return nil
				},
			)
			if !errors.Is(updateErr, ErrInvalidProfile) {
				t.Fatalf("UpdateProfile() error = %v", updateErr)
			}
		})
	}
}

func TestBeginCampaignFreezesDeduplicationSnapshot(t *testing.T) {
	store := newRepositoryAuditTestStore(t)
	repository := "owner/deduplication-snapshot"
	snapshot := &RepositoryReviewDeduplicationSnapshot{
		ProfileID: "rrpf_dedup_snapshot", ProfileVersion: 7,
		ReviewerModel: "reviewer", DeduplicationModel: "deduplicator",
		AccountRef: "openai:work", AccountModelRevision: "config-revision-9",
		SimilarityThreshold: 90, CandidateLimit: 4,
	}
	request := BeginCampaignRequest{
		Repository: repository, CampaignID: NewRepositoryReviewCampaignID(),
		CommitSHA: strings.Repeat("a", 40), Exact: true,
		DeduplicationSnapshot: snapshot,
	}
	started, err := store.BeginCampaign(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	want := *snapshot
	snapshot.DeduplicationModel = "mutated-after-call"
	if started.CurrentCampaign == nil || started.CurrentCampaign.DeduplicationSnapshot == nil ||
		*started.CurrentCampaign.DeduplicationSnapshot != want {
		t.Fatalf("campaign snapshot = %#v, want %#v", started.CurrentCampaign, want)
	}
	mismatched := request
	mismatched.DeduplicationSnapshot = cloneRepositoryReviewDeduplicationSnapshot(
		started.CurrentCampaign.DeduplicationSnapshot,
	)
	mismatched.DeduplicationSnapshot.CandidateLimit = 1
	if _, err := store.BeginCampaign(context.Background(), mismatched); !errors.Is(err, ErrConflict) {
		t.Fatalf("changed campaign snapshot error = %v", err)
	}
}

func TestRepositoryReviewDeduplicationSnapshotResolvesReviewerFallback(t *testing.T) {
	snapshot, err := RepositoryReviewDeduplicationSnapshotFromAutomation(
		RepositoryReviewAutomation{
			ReviewerModels: []string{"reviewer-a"}, AccountRef: "configured-account",
			EffectiveAccountRef: "effective-account", AccountModelRevision: "revision-a",
			DeduplicationSimilarityThreshold: 90, DeduplicationCandidateLimit: 4,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.ReviewerModel != "reviewer-a" || snapshot.DeduplicationModel != "reviewer-a" ||
		snapshot.AccountRef != "effective-account" || snapshot.AccountModelRevision != "revision-a" {
		t.Fatalf("resolved snapshot = %#v", snapshot)
	}
	invalid := snapshot
	invalid.CandidateLimit = DeduplicationMaximumShortlist + 1
	if err := validateRepositoryReviewDeduplicationSnapshot(invalid); err == nil {
		t.Fatal("oversized deduplication shortlist was accepted")
	}
}

func TestAssignmentCheckpointAtomicallyAdmitsRawFindingAndJob(t *testing.T) {
	fixture := newAssignmentCoverageFixture(t, 1, 1)
	state, _, err := fixture.store.Get(fixture.repository)
	if err != nil {
		t.Fatal(err)
	}
	if state.CurrentCampaign == nil {
		t.Fatal("fixture has no campaign")
	}
	state.CurrentCampaign.DeduplicationSnapshot = &RepositoryReviewDeduplicationSnapshot{
		ReviewerModel: "review-a", DeduplicationModel: "dedup-a",
		SimilarityThreshold: 89, CandidateLimit: 1,
	}
	if saveErr := fixture.store.save(&state); saveErr != nil {
		t.Fatal(saveErr)
	}
	plan := fixture.plan
	if _, beginErr := fixture.store.BeginRepositoryReviewRun(context.Background(), BeginRepositoryReviewRunRequest{
		Plan: plan, RunID: "dedup-checkpoint", ReviewableFiles: fixture.files,
	}); beginErr != nil {
		t.Fatal(beginErr)
	}
	assignment := plan.AssignmentPlans[0]
	observation := Observation{
		Model: "provider/review-a", ModelAlias: "review-a", Account: "review-account",
		Reviewer:   fixture.catalog[0].FocusID,
		ScopeFiles: assignment.Files, RawDigest: "sha256:" + strings.Repeat("c", 64),
		Findings: []FindingCandidate{
			repositoryReviewCampaignFinding(assignment.Files[0], "raw diagnosis"),
		},
	}
	result, err := fixture.store.CheckpointRepositoryReviewAssignment(
		context.Background(), CheckpointRepositoryReviewAssignmentRequest{
			Plan: plan, RunID: "dedup-checkpoint", AssignmentID: assignment.AssignmentID,
			Digest:            "sha256:" + strings.Repeat("d", 64),
			AcknowledgedFiles: assignment.Files, Observation: observation,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.AcceptedFindingIDs) != 1 || len(result.State.RawFindings) != 1 ||
		len(result.State.DeduplicationJobs) != 1 || len(result.State.MappingJobs) != 0 {
		t.Fatalf("checkpoint state = %#v", result.State)
	}
	raw := result.State.RawFindings[0]
	job := result.State.DeduplicationJobs[0]
	if result.AcceptedFindingIDs[0] != raw.ID || job.RawFindingID != raw.ID ||
		raw.Model != "provider/review-a" || raw.ModelAlias != "review-a" ||
		raw.Account != "review-account" ||
		job.InsertionOrdinal != raw.InsertionOrdinal || job.AdmissionBucket != raw.AdmissionBucket ||
		job.ModelSnapshot.DeduplicationModel != "dedup-a" ||
		result.State.FindingsProcessing.Pending != 1 ||
		result.State.FindingsProcessing.RawTotal != 1 {
		t.Fatalf("raw=%#v job=%#v counters=%#v", raw, job, result.State.FindingsProcessing)
	}
	reconciled, err := fixture.store.ReconcileJobs(context.Background())
	if err != nil || reconciled.MappingJobsCreated != 0 {
		t.Fatalf("undecided raw finding entered repository mapping: %#v, err=%v", reconciled, err)
	}
	tampered := result.State
	tampered.RawFindings = append([]RawReviewFinding(nil), result.State.RawFindings...)
	tampered.RawFindings[0].Title = "rewritten diagnosis"
	if saveErr := fixture.store.save(&tampered); saveErr == nil {
		t.Fatal("raw diagnosis mutation was accepted")
	}

	replayed, err := fixture.store.CheckpointRepositoryReviewAssignment(
		context.Background(), CheckpointRepositoryReviewAssignmentRequest{
			Plan: plan, RunID: "dedup-checkpoint", AssignmentID: assignment.AssignmentID,
			Digest:            "sha256:" + strings.Repeat("d", 64),
			AcknowledgedFiles: assignment.Files, Observation: observation,
		},
	)
	if err != nil || !replayed.Idempotent || len(replayed.State.RawFindings) != 1 ||
		len(replayed.State.DeduplicationJobs) != 1 {
		t.Fatalf("idempotent checkpoint = %#v, err=%v", replayed, err)
	}
}

func TestRepositoryStateV3MigrationOnlyMarksHistoricalReplay(t *testing.T) {
	state := RepositoryState{
		SchemaVersion: 3, ID: RepositoryID("owner/legacy-dedup"),
		Repository: "owner/legacy-dedup", Findings: []Finding{{ID: "legacy-finding"}},
		UpdatedAt: repositoryAuditTestNow,
	}
	migrated, err := migrateRepositoryState(&state)
	if err != nil || !migrated {
		t.Fatalf("migrateRepositoryState() = %v, %v", migrated, err)
	}
	if state.SchemaVersion != SchemaVersion || state.RawFindings == nil ||
		state.DeduplicatedFindings == nil || state.DeduplicationJobs == nil ||
		len(state.RawFindings) != 0 || !state.HistoricalDeduplication.Required ||
		state.HistoricalDeduplication.Status != HistoricalDeduplicationPending {
		t.Fatalf("migrated state = %#v", state)
	}
	before := state
	if migratedAgain, err := migrateRepositoryState(&state); err != nil || migratedAgain ||
		!reflect.DeepEqual(state, before) {
		t.Fatalf("second migration = %v, %v, state=%#v", migratedAgain, err, state)
	}
}
