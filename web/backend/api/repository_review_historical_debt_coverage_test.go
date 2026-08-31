package api

import (
	"context"
	"errors"
	"maps"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/repoaudit"
	"github.com/sipeed/picoclaw/pkg/workflows"
)

func TestRepositoryReviewHistoricalDebtDeduplicationCallbacks(t *testing.T) {
	if _, err := runRepositoryDeduplicationAgent(
		t.Context(), nil, workflows.AgentRequest{},
	); err == nil {
		t.Fatal("default deduplication agent wrapper accepted a nil runner")
	}

	handler, _, workspace := newRepositoryReviewAutomationTestHandler(t)
	t.Cleanup(handler.Shutdown)
	state := seedRepositoryReviewAPIState(t, workspace)
	_ = seedRepositoryReviewDeduplicationAPIState(t, workspace, state, "rrc_debt")
	store, err := handler.repositoryReviewStore()
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := config.LoadConfig(handler.configPath)
	if err != nil {
		t.Fatal(err)
	}
	controller := newRepositoryReviewController(handler)
	controller.leasedStore = store
	controller.leasedConfig = cfg
	t.Cleanup(controller.Stop)
	original := processRepositoryDeduplicationJobs
	t.Cleanup(func() { processRepositoryDeduplicationJobs = original })
	processRepositoryDeduplicationJobs = func(
		_ repoaudit.Store,
		ctx context.Context,
		_ string,
		options repoaudit.DeduplicationProcessOptions,
	) (repoaudit.DeduplicationProcessResult, error) {
		snapshot := repoaudit.RepositoryReviewDeduplicationSnapshot{}
		_, _ = options.Score(
			ctx, snapshot, "score", repoaudit.DeduplicationScoringRequest{},
		)
		_, _ = options.Judge(
			ctx, snapshot, "judge", repoaudit.DeduplicationJudgeRequest{},
		)
		return repoaudit.DeduplicationProcessResult{}, nil
	}
	if err := controller.processRepositoryFindingDeduplications(t.Context()); err != nil {
		t.Fatal(err)
	}

	blockedRoot := filepath.Join(t.TempDir(), "store-file")
	if err := os.WriteFile(blockedRoot, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	historical := newRepositoryReviewController(nil)
	historical.leasedConfig = &config.Config{}
	historical.leasedStore = repoaudit.NewStore(blockedRoot)
	historical.wakeHistoricalFindingDeduplication()
}

type repositoryReviewAttributionDebtFixture struct {
	state       repoaudit.RepositoryState
	fence       repoaudit.RepositoryReviewFileAttributionCreditFence
	file        repoaudit.FileRef
	attribution repoaudit.RepositoryReviewFileAttribution
	assignment  repoaudit.RepositoryReviewAssignment
}

func newRepositoryReviewAttributionDebtFixture(t *testing.T) repositoryReviewAttributionDebtFixture {
	t.Helper()
	campaignID := repoaudit.NewRepositoryReviewCampaignID()
	profileHash := "sha256:" + strings.Repeat("1", 64)
	assignment, err := repoaudit.NewRepositoryReviewAssignment(
		repoaudit.RepositoryReviewFocusCorrectnessState,
		"review",
		"prompt-v1",
		profileHash,
		true,
	)
	if err != nil {
		t.Fatal(err)
	}
	file := repoaudit.FileRef{
		Path: "code.go", BlobSHA: strings.Repeat("a", 40), SizeBytes: 10,
		Category: "code", Mode: "100644",
	}
	completedAt := time.Now().UTC()
	attribution, err := repoaudit.NewRepositoryReviewFileAttribution(
		repoaudit.RepositoryReviewFileAttribution{
			AutomationID: "rra_debt", RunID: "wr_debt",
			CommitSHA: strings.Repeat("b", 40), InventoryHash: "inventory",
			ProfileHash:  "sha256:" + strings.Repeat("2", 64),
			AssignmentID: "legacy-managed-child-000001",
			FocusID:      repoaudit.RepositoryReviewFocusCorrectnessState,
			RootAgentID:  "main", ReviewerIdentity: "review", Model: "review",
			AcknowledgedFiles: []repoaudit.FileRef{file},
			EvidenceDigest:    "sha256:" + strings.Repeat("3", 64),
			Source:            repoaudit.RepositoryReviewFileAttributionSourceLegacyManagedChild,
			ChildIndex:        1, Required: true, CompletedAt: completedAt,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	state := repoaudit.RepositoryState{
		Runs: []repoaudit.ReviewRun{{
			ID: attribution.RunID, CampaignID: campaignID, LegacyRecovered: true,
			CommitSHA: attribution.CommitSHA, InventoryHash: attribution.InventoryHash,
			ProfileHash: attribution.ProfileHash, CompletedAt: completedAt,
		}},
		CurrentCampaign: &repoaudit.RepositoryReviewCampaignCoverage{
			ID: campaignID, CommitSHA: attribution.CommitSHA,
			InventoryHash: attribution.InventoryHash, ProfileHash: profileHash,
			ScopeDigest:         "sha256:" + strings.Repeat("4", 64),
			RequiredAssignments: 1,
			AssignmentCatalog:   []repoaudit.RepositoryReviewAssignment{assignment},
			SelectedFiles:       1, Exact: true,
			RecoveryDigest: "sha256:" + strings.Repeat("5", 64),
			Paths:          map[string]repoaudit.RepositoryReviewCampaignPathCoverage{},
		},
	}
	return repositoryReviewAttributionDebtFixture{
		state: state,
		fence: repoaudit.RepositoryReviewFileAttributionCreditFence{
			AutomationID: attribution.AutomationID, CampaignID: campaignID,
		},
		file: file, attribution: attribution, assignment: assignment,
	}
}

func TestRepositoryReviewHistoricalDebtAttributionBoundaries(t *testing.T) {
	fixture := newRepositoryReviewAttributionDebtFixture(t)
	cloneState := func() repoaudit.RepositoryState {
		state := fixture.state
		state.Runs = append([]repoaudit.ReviewRun(nil), fixture.state.Runs...)
		campaign := *fixture.state.CurrentCampaign
		campaign.AssignmentCatalog = append(
			[]repoaudit.RepositoryReviewAssignment(nil),
			fixture.state.CurrentCampaign.AssignmentCatalog...,
		)
		campaign.Paths = maps.Clone(fixture.state.CurrentCampaign.Paths)
		state.CurrentCampaign = &campaign
		return state
	}
	preview := func(
		state repoaudit.RepositoryState,
		attributions []repoaudit.RepositoryReviewFileAttribution,
	) error {
		t.Helper()
		_, err := repoaudit.PreviewRepositoryReviewFileAttributionCredits(
			state, fixture.fence, attributions,
		)
		return err
	}

	duplicateRuns := cloneState()
	duplicateRuns.Runs = append(duplicateRuns.Runs, duplicateRuns.Runs[0])
	if err := preview(
		duplicateRuns,
		[]repoaudit.RepositoryReviewFileAttribution{fixture.attribution},
	); !errors.Is(err, repoaudit.ErrConflict) {
		t.Fatalf("duplicate run error = %v", err)
	}

	ambiguous := cloneState()
	second, assignmentErr := repoaudit.NewRepositoryReviewAssignment(
		fixture.assignment.FocusID, fixture.assignment.Reviewer, "prompt-v2",
		fixture.assignment.ProfileHash, true,
	)
	if assignmentErr != nil {
		t.Fatal(assignmentErr)
	}
	ambiguous.CurrentCampaign.AssignmentCatalog = append(
		[]repoaudit.RepositoryReviewAssignment{fixture.assignment}, second,
	)
	ambiguous.CurrentCampaign.RequiredAssignments = 2
	if err := preview(
		ambiguous,
		[]repoaudit.RepositoryReviewFileAttribution{fixture.attribution},
	); !errors.Is(err, repoaudit.ErrConflict) {
		t.Fatalf("ambiguous assignment error = %v", err)
	}

	changedRun := fixture.attribution
	changedRun.ID = ""
	changedRun.ProfileHash = "sha256:" + strings.Repeat("6", 64)
	changedRun, attributionErr := repoaudit.NewRepositoryReviewFileAttribution(changedRun)
	if attributionErr != nil {
		t.Fatal(attributionErr)
	}
	if err := preview(
		cloneState(),
		[]repoaudit.RepositoryReviewFileAttribution{changedRun},
	); !errors.Is(
		err,
		repoaudit.ErrConflict,
	) {
		t.Fatalf("changed run evidence error = %v", err)
	}

	unsupported := cloneState()
	unsupported.CurrentCampaign.Paths = map[string]repoaudit.RepositoryReviewCampaignPathCoverage{
		fixture.file.Path: {Unsupported: true},
	}
	if err := preview(
		unsupported,
		[]repoaudit.RepositoryReviewFileAttribution{fixture.attribution},
	); !errors.Is(
		err,
		repoaudit.ErrConflict,
	) {
		t.Fatalf("unsupported path error = %v", err)
	}

	badBits := cloneState()
	badBits.CurrentCampaign.Paths = map[string]repoaudit.RepositoryReviewCampaignPathCoverage{
		fixture.file.Path: {AssignmentBits: "bad", Inspected: true},
	}
	if err := preview(
		badBits,
		[]repoaudit.RepositoryReviewFileAttribution{fixture.attribution},
	); err == nil {
		t.Fatal("invalid assignment bits were accepted")
	}

	tooMany := make(
		[]repoaudit.RepositoryReviewFileAttribution,
		100_001,
	)
	if err := preview(cloneState(), tooMany); !errors.Is(err, repoaudit.ErrInvalidPlan) {
		t.Fatalf("oversized attribution set error = %v", err)
	}

	conflict := fixture.attribution
	conflict.UsageModel = "different-model"
	conflict.ID = ""
	conflict, conflictErr := repoaudit.NewRepositoryReviewFileAttribution(conflict)
	if conflictErr != nil {
		t.Fatal(conflictErr)
	}
	if err := preview(
		cloneState(),
		[]repoaudit.RepositoryReviewFileAttribution{fixture.attribution, conflict},
	); !errors.Is(err, repoaudit.ErrConflict) {
		t.Fatalf("conflicting duplicate attribution error = %v", err)
	}

	security, securityErr := repoaudit.NewRepositoryReviewAssignment(
		repoaudit.RepositoryReviewFocusSecurityTrust, "review", "prompt-v1",
		fixture.assignment.ProfileHash, true,
	)
	if securityErr != nil {
		t.Fatal(securityErr)
	}
	conflictingFile := fixture.attribution
	conflictingFile.ID = ""
	conflictingFile.ChildIndex = 2
	conflictingFile.FocusID = repoaudit.RepositoryReviewFocusSecurityTrust
	conflictingFile.AssignmentID = "legacy-managed-child-000002"
	conflictingFile.AcknowledgedFiles = []repoaudit.FileRef{{
		Path: fixture.file.Path, BlobSHA: strings.Repeat("c", 40), SizeBytes: 10,
		Category: "code", Mode: "100644",
	}}
	conflictingFile, fileErr := repoaudit.NewRepositoryReviewFileAttribution(conflictingFile)
	if fileErr != nil {
		t.Fatal(fileErr)
	}
	fileConflictState := cloneState()
	fileConflictState.CurrentCampaign.AssignmentCatalog = []repoaudit.RepositoryReviewAssignment{
		fixture.assignment, security,
	}
	fileConflictState.CurrentCampaign.RequiredAssignments = 2
	if err := preview(
		fileConflictState,
		[]repoaudit.RepositoryReviewFileAttribution{fixture.attribution, conflictingFile},
	); !errors.Is(err, repoaudit.ErrConflict) {
		t.Fatalf("conflicting file revision error = %v", err)
	}
}
