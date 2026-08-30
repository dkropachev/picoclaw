package repoaudit

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

type assignmentCoverageFixture struct {
	store      Store
	repository string
	campaignID string
	plan       Plan
	files      []FileRef
	catalog    []RepositoryReviewAssignment
}

func newAssignmentCoverageFixture(t *testing.T, fileCount, maximumPending int) assignmentCoverageFixture {
	t.Helper()
	store := NewStore(t.TempDir())
	store.now = func() time.Time { return repositoryAuditTestNow }
	repository := "owner/assignment-coverage-" + strings.ToLower(strings.ReplaceAll(t.Name(), "/", "-"))
	commit := strings.Repeat("a", 40)
	profileHash := "sha256:" + strings.Repeat("b", 64)
	files := make([]FileRef, 0, fileCount)
	for index := 0; index < fileCount; index++ {
		files = append(files, repositoryAuditTestFile(
			fmt.Sprintf("pkg/file-%02d.go", index), fmt.Sprintf("%x", index+1), int64(index+10),
		))
	}
	catalog := repositoryReviewAssignmentCatalogForTest(t, profileHash)
	campaignID := NewRepositoryReviewCampaignID()
	if _, err := store.BeginCampaign(context.Background(), BeginCampaignRequest{
		Repository: repository, CampaignID: campaignID, CommitSHA: commit,
	}); err != nil {
		t.Fatal(err)
	}
	if maximumPending <= 0 {
		maximumPending = max(1, fileCount)
	}
	plan, err := store.PlanAssignmentsForCampaign(
		context.Background(), repository, commit, "inventory", profileHash, campaignID,
		catalog, files, false, maximumPending, true,
	)
	if err != nil {
		t.Fatal(err)
	}
	return assignmentCoverageFixture{
		store: store, repository: repository, campaignID: campaignID,
		plan: plan, files: files, catalog: catalog,
	}
}

func assignmentCoverageCheckpoint(
	fixture assignmentCoverageFixture,
	runID string,
	assignmentIndex int,
	acknowledged []FileRef,
) CheckpointRepositoryReviewAssignmentRequest {
	assignmentPlan := fixture.plan.AssignmentPlans[assignmentIndex]
	return CheckpointRepositoryReviewAssignmentRequest{
		Plan: fixture.plan, RunID: runID, AssignmentID: assignmentPlan.AssignmentID,
		Digest:            "sha256:" + strings.Repeat(fmt.Sprintf("%x", assignmentIndex+1), 64),
		AcknowledgedFiles: acknowledged,
		Observation: Observation{
			Model: "review-a", Reviewer: assignmentPlan.FocusID,
			ScopeFiles: assignmentPlan.Files,
			RawDigest:  "sha256:" + strings.Repeat("c", 64),
		},
	}
}

//nolint:govet // Boundary assertions intentionally reuse err in short scopes.
func TestRepositoryReviewAssignmentPrimitiveCoverage(t *testing.T) {
	profileHash := "sha256:" + strings.Repeat("d", 64)
	catalog := repositoryReviewAssignmentCatalogForTest(t, profileHash)
	if validRepositoryReviewFocusID("unknown") {
		t.Fatal("unknown focus accepted")
	}
	if _, err := NewRepositoryReviewAssignment("unknown", "review-a", "prompt", profileHash, true); err == nil {
		t.Fatal("assignment with unknown focus accepted")
	}
	if _, err := NewRepositoryReviewAssignment(catalog[0].FocusID, "", "prompt", profileHash, true); err == nil {
		t.Fatal("assignment with empty reviewer accepted")
	}
	if _, err := NormalizeRepositoryReviewAssignmentCatalog(nil); err == nil {
		t.Fatal("empty catalog accepted")
	}
	if _, err := NormalizeRepositoryReviewAssignmentCatalog(
		make([]RepositoryReviewAssignment, maxRepositoryReviewRequiredAssignments+1),
	); err == nil {
		t.Fatal("oversized catalog accepted")
	}
	invalid := append([]RepositoryReviewAssignment(nil), catalog...)
	invalid[0].ID = "rra_invalid"
	if _, err := NormalizeRepositoryReviewAssignmentCatalog(invalid); err == nil {
		t.Fatal("catalog with forged ID accepted")
	}
	if _, err := NormalizeRepositoryReviewAssignmentCatalog(
		[]RepositoryReviewAssignment{catalog[0], catalog[0]},
	); err == nil {
		t.Fatal("duplicate catalog assignment accepted")
	}
	other, err := NewRepositoryReviewAssignment(
		catalog[1].FocusID, catalog[1].Reviewer, catalog[1].PromptRevision,
		"sha256:"+strings.Repeat("e", 64), true,
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := NormalizeRepositoryReviewAssignmentCatalog(
		[]RepositoryReviewAssignment{catalog[0], other},
	); err == nil {
		t.Fatal("mixed profile catalog accepted")
	}
	optional := make([]RepositoryReviewAssignment, 0, len(RepositoryReviewFocusIDs()))
	for _, focusID := range RepositoryReviewFocusIDs() {
		assignment, assignmentErr := NewRepositoryReviewAssignment(
			focusID, "optional", "prompt", profileHash, false,
		)
		if assignmentErr != nil {
			t.Fatal(assignmentErr)
		}
		optional = append(optional, assignment)
	}
	if _, err := NormalizeRepositoryReviewAssignmentCatalog(optional); err == nil {
		t.Fatal("all-optional catalog accepted")
	}
	if _, found := repositoryReviewAssignmentIndex(catalog, "missing"); found {
		t.Fatal("missing assignment found")
	}

	if _, err := decodeRepositoryReviewAssignmentBits("%%%", catalog); err == nil {
		t.Fatal("malformed bitmask accepted")
	}
	if _, err := decodeRepositoryReviewAssignmentBits(
		base64.RawStdEncoding.EncodeToString([]byte{1, 2}), catalog,
	); err == nil {
		t.Fatal("wrong-sized bitmask accepted")
	}
	if _, err := decodeRepositoryReviewAssignmentBits(
		base64.RawStdEncoding.EncodeToString([]byte{0x80}), catalog,
	); err == nil {
		t.Fatal("unknown assignment bits accepted")
	}
	if got := encodeRepositoryReviewAssignmentBits([]byte{0}); got != "" {
		t.Fatalf("zero mask = %q", got)
	}
	if got := encodeRepositoryReviewAssignmentBits([]byte{1}); got == "" {
		t.Fatal("nonzero mask encoded empty")
	}
	bits := make([]byte, 1)
	if setRepositoryReviewAssignmentBit(bits, -1) || setRepositoryReviewAssignmentBit(bits, 9) {
		t.Fatal("out-of-range assignment bit was set")
	}

	if _, err := projectRepositoryReviewAssignmentCoverage(
		RepositoryReviewCampaignPathCoverage{Unsupported: true, AssignmentBits: "AQ"}, catalog,
	); !errors.Is(err, ErrConflict) {
		t.Fatalf("unsupported mask error = %v", err)
	}
	if _, err := projectRepositoryReviewAssignmentCoverage(
		RepositoryReviewCampaignPathCoverage{AssignmentBits: "bad"}, catalog,
	); err == nil {
		t.Fatal("malformed projection accepted")
	}
	if _, err := repositoryReviewAssignmentComplete(
		RepositoryReviewCampaignPathCoverage{}, catalog, "missing",
	); err == nil {
		t.Fatal("unknown completion assignment accepted")
	}
	if _, err := repositoryReviewAssignmentComplete(
		RepositoryReviewCampaignPathCoverage{AssignmentBits: "bad"}, catalog, catalog[0].ID,
	); err == nil {
		t.Fatal("malformed completion mask accepted")
	}
	for name, coverage := range map[string]RepositoryReviewCampaignPathCoverage{
		"unsupported": {Unsupported: true},
		"bad mask":    {AssignmentBits: "bad"},
	} {
		t.Run("set completion "+name, func(t *testing.T) {
			if _, _, err := setRepositoryReviewAssignmentComplete(coverage, catalog, catalog[0].ID); err == nil {
				t.Fatal("invalid completion state accepted")
			}
		})
	}
	if _, _, err := setRepositoryReviewAssignmentComplete(
		RepositoryReviewCampaignPathCoverage{}, catalog, "missing",
	); err == nil {
		t.Fatal("unknown assignment set complete")
	}

	allRequired, changed, err := setAllRequiredRepositoryReviewAssignments(
		RepositoryReviewCampaignPathCoverage{}, catalog,
	)
	if err != nil || !changed || !allRequired.Completed {
		t.Fatalf("all-required coverage = %#v changed=%v err=%v", allRequired, changed, err)
	}
	if _, changed, err = setAllRequiredRepositoryReviewAssignments(allRequired, catalog); err != nil || changed {
		t.Fatalf("all-required replay changed=%v err=%v", changed, err)
	}
	if _, _, err := setAllRequiredRepositoryReviewAssignments(
		RepositoryReviewCampaignPathCoverage{Unsupported: true}, catalog,
	); !errors.Is(err, ErrConflict) {
		t.Fatalf("unsupported all-required error = %v", err)
	}
	if _, _, err := setAllRequiredRepositoryReviewAssignments(
		RepositoryReviewCampaignPathCoverage{AssignmentBits: "bad"}, catalog,
	); err == nil {
		t.Fatal("bad all-required mask accepted")
	}
	if _, err := CreditRepositoryReviewAssignment(
		RepositoryReviewCampaignPathCoverage{}, nil, catalog[0].ID,
	); err == nil {
		t.Fatal("credit accepted empty catalog")
	}
	credited, err := CreditRepositoryReviewAssignment(
		RepositoryReviewCampaignPathCoverage{}, catalog, catalog[0].ID,
	)
	if err != nil || !credited.Inspected || credited.Completed {
		t.Fatalf("single credit = %#v err=%v", credited, err)
	}
	if _, err := CreditAllRequiredRepositoryReviewAssignments(
		RepositoryReviewCampaignPathCoverage{}, nil,
	); err == nil {
		t.Fatal("all-required credit accepted empty catalog")
	}
	credited, err = CreditAllRequiredRepositoryReviewAssignments(
		RepositoryReviewCampaignPathCoverage{}, catalog,
	)
	if err != nil || !credited.Completed {
		t.Fatalf("required credits = %#v err=%v", credited, err)
	}
	if repositoryReviewAssignmentCatalogEqual(catalog, catalog[:1]) {
		t.Fatal("different catalogs compare equal")
	}
}

//nolint:govet // Boundary assertions intentionally reuse err in short scopes.
func TestRepositoryReviewAssignmentPlanAndProgressCoverage(t *testing.T) {
	fixture := newAssignmentCoverageFixture(t, 2, 2)
	allowed := map[string]FileRef{
		fixture.files[0].Path: fixture.files[0],
		fixture.files[1].Path: fixture.files[1],
	}
	if _, err := normalizeRepositoryReviewAssignmentPlans(
		make([]RepositoryReviewAssignmentPlan, len(fixture.catalog)+1), fixture.catalog, allowed,
	); err == nil {
		t.Fatal("too many assignment plans accepted")
	}
	valid := fixture.plan.AssignmentPlans[0]
	for name, mutate := range map[string]func(*RepositoryReviewAssignmentPlan){
		"unknown": func(plan *RepositoryReviewAssignmentPlan) { plan.AssignmentID = "missing" },
		"empty":   func(plan *RepositoryReviewAssignmentPlan) { plan.Files = nil },
		"focus":   func(plan *RepositoryReviewAssignmentPlan) { plan.FocusID = "wrong" },
		"reviewer": func(plan *RepositoryReviewAssignmentPlan) {
			plan.Reviewer = "wrong"
		},
		"optional": func(plan *RepositoryReviewAssignmentPlan) { plan.Optional = !plan.Optional },
		"label":    func(plan *RepositoryReviewAssignmentPlan) { plan.Label = "bad\x00label" },
		"task":     func(plan *RepositoryReviewAssignmentPlan) { plan.Task = "bad\x00task" },
		"file": func(plan *RepositoryReviewAssignmentPlan) {
			plan.Files = []FileRef{{Path: "../bad", BlobSHA: strings.Repeat("a", 40)}}
		},
		"outside": func(plan *RepositoryReviewAssignmentPlan) {
			plan.Files = []FileRef{repositoryAuditTestFile("outside.go", "a", 1)}
		},
	} {
		t.Run(name, func(t *testing.T) {
			candidate := valid
			mutate(&candidate)
			if _, err := normalizeRepositoryReviewAssignmentPlans(
				[]RepositoryReviewAssignmentPlan{candidate}, fixture.catalog, allowed,
			); err == nil {
				t.Fatal("invalid assignment plan accepted")
			}
		})
	}
	if _, err := normalizeRepositoryReviewAssignmentPlans(
		[]RepositoryReviewAssignmentPlan{valid, valid}, fixture.catalog, allowed,
	); err == nil {
		t.Fatal("duplicate assignment plan accepted")
	}
	reversed := []RepositoryReviewAssignmentPlan{
		fixture.plan.AssignmentPlans[1], fixture.plan.AssignmentPlans[0],
	}
	normalized, err := normalizeRepositoryReviewAssignmentPlans(reversed, fixture.catalog, allowed)
	if err != nil || normalized[0].AssignmentID != fixture.catalog[0].ID {
		t.Fatalf("normalized plan order = %#v err=%v", normalized, err)
	}
	defaultAssignment, err := NewRepositoryReviewAssignment(
		RepositoryReviewFocusCorrectnessState, "default", "prompt", fixture.plan.ProfileHash, true,
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := normalizeRepositoryReviewAssignmentPlans(
		[]RepositoryReviewAssignmentPlan{{
			AssignmentID: defaultAssignment.ID, FocusID: defaultAssignment.FocusID,
			Reviewer: "", Files: []FileRef{fixture.files[0]},
		}},
		[]RepositoryReviewAssignment{defaultAssignment}, allowed,
	); err != nil {
		t.Fatalf("default reviewer plan rejected: %v", err)
	}

	if _, err := BindRepositoryReviewAssignmentTasks(Plan{}, nil); err == nil {
		t.Fatal("tasks bound to invalid plan")
	}
	if _, err := BindRepositoryReviewAssignmentTasks(fixture.plan, map[string]string{}); err == nil {
		t.Fatal("missing assignment task accepted")
	}
	tasks := make(map[string]string)
	for _, focusID := range RepositoryReviewFocusIDs() {
		tasks[focusID] = "task for " + focusID
	}
	bound, err := BindRepositoryReviewAssignmentTasks(fixture.plan, tasks)
	if err != nil || bound.ID == fixture.plan.ID || bound.AssignmentPlans[0].Task == "" {
		t.Fatalf("bound tasks = %#v err=%v", bound.AssignmentPlans, err)
	}
	invalidPlan := fixture.plan
	invalidPlan.RequiredAssignments++
	invalidPlan.ID = planDigest(invalidPlan)
	if _, err := BindRepositoryReviewAssignmentTasks(invalidPlan, tasks); err == nil {
		t.Fatal("tasks made invalid plan valid")
	}

	if got := CurrentCampaignAssignmentProgress(RepositoryState{}, fixture.campaignID); got.Total != 0 {
		t.Fatalf("empty progress = %#v", got)
	}
	state, _, err := fixture.store.Get(fixture.repository)
	if err != nil {
		t.Fatal(err)
	}
	state.CurrentCampaign.Paths[fixture.files[1].Path] = RepositoryReviewCampaignPathCoverage{Unsupported: true}
	credited, err := CreditRepositoryReviewAssignment(
		RepositoryReviewCampaignPathCoverage{}, fixture.catalog, fixture.catalog[0].ID,
	)
	if err != nil {
		t.Fatal(err)
	}
	state.CurrentCampaign.Paths[fixture.files[0].Path] = credited
	state.ActiveReviewRun = &RepositoryReviewActiveRun{
		ID: "active", CampaignID: fixture.campaignID,
		Reservations: map[string]RepositoryReviewAssignmentReservation{
			fixture.catalog[1].ID: {
				AssignmentID: fixture.catalog[1].ID, Files: []FileRef{fixture.files[0]},
			},
		},
	}
	progress := CurrentCampaignAssignmentProgress(state, fixture.campaignID)
	if progress.Total != 4 || progress.Completed != 1 || progress.Active != 1 || progress.Pending != 2 {
		t.Fatalf("projected progress = %#v", progress)
	}
	state.CurrentCampaign.Paths[fixture.files[0].Path] = RepositoryReviewCampaignPathCoverage{
		AssignmentBits: "bad",
	}
	if got := CurrentCampaignAssignmentProgress(state, fixture.campaignID); got.Total != 0 {
		t.Fatalf("malformed progress was projected: %#v", got)
	}
}

//nolint:govet // Boundary assertions intentionally reuse err in short scopes.
func TestRepositoryReviewAssignmentRunDefensiveCoverage(t *testing.T) {
	t.Run("begin", func(t *testing.T) {
		fixture := newAssignmentCoverageFixture(t, 1, 1)
		canceled, cancel := context.WithCancel(context.Background())
		cancel()
		if _, err := fixture.store.BeginRepositoryReviewRun(
			canceled,
			BeginRepositoryReviewRunRequest{},
		); !errors.Is(
			err,
			context.Canceled,
		) {
			t.Fatalf("canceled begin error = %v", err)
		}
		if _, err := fixture.store.BeginRepositoryReviewRun(
			context.Background(),
			BeginRepositoryReviewRunRequest{},
		); err == nil {
			t.Fatal("empty begin accepted")
		}
		if _, err := repositoryReviewActiveRunFromPlan(
			fixture.plan, "run", []FileRef{repositoryAuditTestFile("outside.go", "f", 1)}, repositoryAuditTestNow,
		); err == nil {
			t.Fatal("outside reviewable file accepted")
		}
		empty, err := repositoryReviewActiveRunFromPlan(
			fixture.plan, "run", []FileRef{}, repositoryAuditTestNow,
		)
		if err != nil || len(empty.Reservations) != 0 {
			t.Fatalf("empty reviewable run = %#v err=%v", empty, err)
		}
		request := BeginRepositoryReviewRunRequest{
			Plan: fixture.plan, RunID: "run", ReviewableFiles: fixture.files,
		}
		first, err := fixture.store.BeginRepositoryReviewRun(nil, request)
		if err != nil {
			t.Fatal(err)
		}
		replayed, err := fixture.store.BeginRepositoryReviewRun(context.Background(), request)
		if err != nil || replayed.Version != first.Version {
			t.Fatalf("begin replay = %#v err=%v", replayed, err)
		}
		conflict := request
		conflict.RunID = "other"
		if _, err := fixture.store.BeginRepositoryReviewRun(
			context.Background(),
			conflict,
		); !errors.Is(
			err,
			ErrConflict,
		) {
			t.Fatalf("competing begin error = %v", err)
		}
		if _, err := fixture.store.InterruptRepositoryReviewRun(
			context.Background(),
			fixture.repository,
			"run",
		); err != nil {
			t.Fatal(err)
		}
		next, err := fixture.store.PlanAssignmentsForCampaign(
			context.Background(), fixture.repository, fixture.plan.CommitSHA, fixture.plan.InventoryHash,
			fixture.plan.ProfileHash, fixture.campaignID, fixture.catalog, fixture.files, false, 1, true,
		)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := fixture.store.BeginRepositoryReviewRun(context.Background(), BeginRepositoryReviewRunRequest{
			Plan: next, RunID: "run", ReviewableFiles: fixture.files,
		}); !errors.Is(err, ErrConflict) {
			t.Fatalf("retained run ID reuse error = %v", err)
		}
	})

	t.Run("verify", func(t *testing.T) {
		fixture := newAssignmentCoverageFixture(t, 2, 2)
		assignmentPlan := fixture.plan.AssignmentPlans[0]
		request := VerifyRepositoryReviewAssignmentRequest{
			Repository: fixture.repository, RunID: "run",
			AssignmentID: assignmentPlan.AssignmentID, Files: assignmentPlan.Files,
		}
		canceled, cancel := context.WithCancel(context.Background())
		cancel()
		if err := fixture.store.VerifyRepositoryReviewAssignment(canceled, request); !errors.Is(err, context.Canceled) {
			t.Fatalf("canceled verify error = %v", err)
		}
		if err := fixture.store.VerifyRepositoryReviewAssignment(
			context.Background(),
			VerifyRepositoryReviewAssignmentRequest{},
		); err == nil {
			t.Fatal("empty verify accepted")
		}
		reversed := request
		reversed.Files = []FileRef{fixture.files[1], fixture.files[0]}
		if err := fixture.store.VerifyRepositoryReviewAssignment(context.Background(), reversed); err == nil {
			t.Fatal("noncanonical verify scope accepted")
		}
		if err := fixture.store.VerifyRepositoryReviewAssignment(
			context.Background(),
			request,
		); !errors.Is(
			err,
			ErrConflict,
		) {
			t.Fatalf("verify without active run error = %v", err)
		}
		if _, err := fixture.store.BeginRepositoryReviewRun(context.Background(), BeginRepositoryReviewRunRequest{
			Plan: fixture.plan, RunID: "run", ReviewableFiles: fixture.files,
		}); err != nil {
			t.Fatal(err)
		}
		if err := fixture.store.VerifyRepositoryReviewAssignment(nil, request); err != nil {
			t.Fatalf("valid verify error = %v", err)
		}
		wrongRun := request
		wrongRun.RunID = "wrong"
		if err := fixture.store.VerifyRepositoryReviewAssignment(
			context.Background(),
			wrongRun,
		); !errors.Is(
			err,
			ErrConflict,
		) {
			t.Fatalf("wrong run verify error = %v", err)
		}
		unknown := request
		unknown.AssignmentID = "rra_unknown"
		if err := fixture.store.VerifyRepositoryReviewAssignment(
			context.Background(),
			unknown,
		); !errors.Is(
			err,
			ErrConflict,
		) {
			t.Fatalf("unknown reservation verify error = %v", err)
		}
		subset := request
		subset.Files = fixture.files[:1]
		if err := fixture.store.VerifyRepositoryReviewAssignment(
			context.Background(),
			subset,
		); !errors.Is(
			err,
			ErrConflict,
		) {
			t.Fatalf("subset verify error = %v", err)
		}
	})

	t.Run("checkpoint", func(t *testing.T) {
		fixture := newAssignmentCoverageFixture(t, 1, 1)
		checkpoint := assignmentCoverageCheckpoint(fixture, "run", 0, fixture.files)
		canceled, cancel := context.WithCancel(context.Background())
		cancel()
		if _, err := fixture.store.CheckpointRepositoryReviewAssignment(
			canceled,
			checkpoint,
		); !errors.Is(
			err,
			context.Canceled,
		) {
			t.Fatalf("canceled checkpoint error = %v", err)
		}
		invalid := checkpoint
		invalid.Digest = "bad"
		if _, err := fixture.store.CheckpointRepositoryReviewAssignment(context.Background(), invalid); err == nil {
			t.Fatal("invalid checkpoint digest accepted")
		}
		if _, err := fixture.store.CheckpointRepositoryReviewAssignment(
			context.Background(),
			checkpoint,
		); !errors.Is(
			err,
			ErrConflict,
		) {
			t.Fatalf("checkpoint without active run error = %v", err)
		}
		if _, err := fixture.store.BeginRepositoryReviewRun(context.Background(), BeginRepositoryReviewRunRequest{
			Plan: fixture.plan, RunID: "run", ReviewableFiles: fixture.files,
		}); err != nil {
			t.Fatal(err)
		}
		for name, mutate := range map[string]func(*CheckpointRepositoryReviewAssignmentRequest){
			"run": func(request *CheckpointRepositoryReviewAssignmentRequest) { request.RunID = "wrong" },
			"assignment": func(request *CheckpointRepositoryReviewAssignmentRequest) {
				request.AssignmentID = "rra_unknown"
			},
			"scope": func(request *CheckpointRepositoryReviewAssignmentRequest) {
				request.Observation.ScopeFiles = nil
			},
			"acknowledgement": func(request *CheckpointRepositoryReviewAssignmentRequest) {
				request.AcknowledgedFiles = []FileRef{repositoryAuditTestFile("outside.go", "f", 1)}
			},
			"model": func(request *CheckpointRepositoryReviewAssignmentRequest) { request.Observation.Model = "" },
			"reviewer": func(request *CheckpointRepositoryReviewAssignmentRequest) {
				request.Observation.Reviewer = "wrong"
			},
			"raw digest": func(request *CheckpointRepositoryReviewAssignmentRequest) {
				request.Observation.RawDigest = "bad"
			},
			"summary": func(request *CheckpointRepositoryReviewAssignmentRequest) {
				request.Observation.Summary = "bad\x00summary"
			},
			"findings": func(request *CheckpointRepositoryReviewAssignmentRequest) {
				request.Observation.Findings = make([]FindingCandidate, maxFindingsPerObservation+1)
			},
		} {
			t.Run(name, func(t *testing.T) {
				candidate := checkpoint
				candidate.Observation = checkpoint.Observation
				mutate(&candidate)
				if _, err := fixture.store.CheckpointRepositoryReviewAssignment(
					context.Background(), candidate,
				); err == nil {
					t.Fatal("invalid checkpoint accepted")
				}
			})
		}
		unacknowledged := checkpoint
		unacknowledged.AcknowledgedFiles = nil
		unacknowledged.Observation.Findings = []FindingCandidate{
			repositoryReviewCampaignFinding(fixture.files[0], "outside acknowledgement"),
		}
		if _, err := fixture.store.CheckpointRepositoryReviewAssignment(
			context.Background(), unacknowledged,
		); err == nil {
			t.Fatal("unacknowledged finding accepted")
		}
		badCandidate := checkpoint
		badCandidate.Observation.Findings = []FindingCandidate{{
			Severity: "high", Title: "bad", File: fixture.files[0].Path,
			Validation: Validation{Status: "confirmed"},
		}}
		if _, err := fixture.store.CheckpointRepositoryReviewAssignment(
			context.Background(), badCandidate,
		); err == nil {
			t.Fatal("invalid confirmed finding accepted")
		}
	})

	t.Run("completed reservation fences", func(t *testing.T) {
		fixture := newAssignmentCoverageFixture(t, 1, 1)
		state, _, err := fixture.store.Get(fixture.repository)
		if err != nil {
			t.Fatal(err)
		}
		credited, err := CreditRepositoryReviewAssignment(
			state.CurrentCampaign.Paths[fixture.files[0].Path], fixture.catalog, fixture.catalog[0].ID,
		)
		if err != nil {
			t.Fatal(err)
		}
		state.CurrentCampaign.Paths[fixture.files[0].Path] = credited
		state.Version++
		if err := fixture.store.save(&state); err != nil {
			t.Fatal(err)
		}
		if _, err := fixture.store.BeginRepositoryReviewRun(context.Background(), BeginRepositoryReviewRunRequest{
			Plan: fixture.plan, RunID: "run", ReviewableFiles: fixture.files,
		}); !errors.Is(err, ErrConflict) {
			t.Fatalf("completed begin reservation error = %v", err)
		}

		// An independently committed bit appearing after reservation must also
		// close the final dispatch fence even before this reservation checkpoints.
		fresh := newAssignmentCoverageFixture(t, 1, 1)
		if _, err := fresh.store.BeginRepositoryReviewRun(context.Background(), BeginRepositoryReviewRunRequest{
			Plan: fresh.plan, RunID: "run", ReviewableFiles: fresh.files,
		}); err != nil {
			t.Fatal(err)
		}
		freshState, _, err := fresh.store.Get(fresh.repository)
		if err != nil {
			t.Fatal(err)
		}
		credited, err = CreditRepositoryReviewAssignment(
			freshState.CurrentCampaign.Paths[fresh.files[0].Path], fresh.catalog, fresh.catalog[0].ID,
		)
		if err != nil {
			t.Fatal(err)
		}
		freshState.CurrentCampaign.Paths[fresh.files[0].Path] = credited
		freshState.Version++
		if err := fresh.store.save(&freshState); err != nil {
			t.Fatal(err)
		}
		assignmentPlan := fresh.plan.AssignmentPlans[0]
		if err := fresh.store.VerifyRepositoryReviewAssignment(
			context.Background(),
			VerifyRepositoryReviewAssignmentRequest{
				Repository: fresh.repository, RunID: "run", AssignmentID: assignmentPlan.AssignmentID,
				Files: assignmentPlan.Files,
			},
		); !errors.Is(
			err,
			ErrConflict,
		) {
			t.Fatalf("completed dispatch fence error = %v", err)
		}
	})
}

func TestRepositoryReviewAssignmentFinalizeInterruptAndValidationCoverage(t *testing.T) {
	t.Run("finalize", func(t *testing.T) {
		fixture := newAssignmentCoverageFixture(t, 2, 1)
		canceled, cancel := context.WithCancel(context.Background())
		cancel()
		if _, err := fixture.store.FinalizeRepositoryReviewRun(
			canceled,
			FinalizeRepositoryReviewRunRequest{},
		); !errors.Is(
			err,
			context.Canceled,
		) {
			t.Fatalf("canceled finalize error = %v", err)
		}
		if _, err := fixture.store.FinalizeRepositoryReviewRun(
			context.Background(),
			FinalizeRepositoryReviewRunRequest{},
		); err == nil {
			t.Fatal("empty finalize accepted")
		}
		if _, err := fixture.store.FinalizeRepositoryReviewRun(context.Background(), FinalizeRepositoryReviewRunRequest{
			Plan: fixture.plan, RunID: "run",
		}); !errors.Is(err, ErrConflict) {
			t.Fatalf("finalize without active run error = %v", err)
		}
		if _, err := fixture.store.BeginRepositoryReviewRun(context.Background(), BeginRepositoryReviewRunRequest{
			Plan: fixture.plan, RunID: "run", ReviewableFiles: fixture.plan.PendingFiles,
		}); err != nil {
			t.Fatal(err)
		}
		outside := FinalizeRepositoryReviewRunRequest{
			Plan: fixture.plan, RunID: "run", UnsupportedFiles: []UnsupportedFile{{
				FileRef: repositoryAuditTestFile("outside.go", "f", 1), Reason: "outside",
			}},
		}
		if _, err := fixture.store.FinalizeRepositoryReviewRun(context.Background(), outside); err == nil {
			t.Fatal("outside unsupported file accepted")
		}
		blankReason := outside
		blankReason.UnsupportedFiles = []UnsupportedFile{{FileRef: fixture.plan.PendingFiles[0]}}
		if _, err := fixture.store.FinalizeRepositoryReviewRun(context.Background(), blankReason); err == nil {
			t.Fatal("blank unsupported reason accepted")
		}
		checkpoint := assignmentCoverageCheckpoint(fixture, "run", 0, fixture.plan.PendingFiles)
		if _, err := fixture.store.CheckpointRepositoryReviewAssignment(context.Background(), checkpoint); err != nil {
			t.Fatal(err)
		}
		overlap := FinalizeRepositoryReviewRunRequest{
			Plan: fixture.plan, RunID: "run", UnsupportedFiles: []UnsupportedFile{{
				FileRef: fixture.plan.PendingFiles[0], Reason: "overlap",
			}},
		}
		if _, err := fixture.store.FinalizeRepositoryReviewRun(
			context.Background(),
			overlap,
		); !errors.Is(
			err,
			ErrConflict,
		) {
			t.Fatalf("checkpoint/unsupported overlap error = %v", err)
		}
		result, err := fixture.store.FinalizeRepositoryReviewRun(
			context.Background(),
			FinalizeRepositoryReviewRunRequest{
				Plan: fixture.plan, RunID: "run", ExcludedFiles: 1,
			},
		)
		if err != nil || result.Run.RemainingFiles != 2 || result.Run.ExcludedFiles != 1 {
			t.Fatalf("finalized run = %#v err=%v", result.Run, err)
		}
		replayed, err := fixture.store.FinalizeRepositoryReviewRun(
			context.Background(),
			FinalizeRepositoryReviewRunRequest{
				Plan: fixture.plan, RunID: "run", ExcludedFiles: 1,
			},
		)
		if err != nil || replayed.Run.ID != "run" {
			t.Fatalf("finalize replay = %#v err=%v", replayed.Run, err)
		}
	})

	t.Run("interrupt", func(t *testing.T) {
		fixture := newAssignmentCoverageFixture(t, 1, 1)
		canceled, cancel := context.WithCancel(context.Background())
		cancel()
		if _, err := fixture.store.InterruptRepositoryReviewRun(
			canceled,
			fixture.repository,
			"run",
		); !errors.Is(
			err,
			context.Canceled,
		) {
			t.Fatalf("canceled interrupt error = %v", err)
		}
		if _, err := fixture.store.InterruptRepositoryReviewRun(context.Background(), "", ""); err == nil {
			t.Fatal("invalid interrupt accepted")
		}
		state, err := fixture.store.InterruptRepositoryReviewRun(nil, fixture.repository, "run")
		if err != nil || state.ActiveReviewRun != nil {
			t.Fatalf("no-active interrupt state=%#v err=%v", state.ActiveReviewRun, err)
		}
		if _, err := fixture.store.BeginRepositoryReviewRun(context.Background(), BeginRepositoryReviewRunRequest{
			Plan: fixture.plan, RunID: "run", ReviewableFiles: fixture.files,
		}); err != nil {
			t.Fatal(err)
		}
		if _, err := fixture.store.InterruptRepositoryReviewRun(
			context.Background(),
			fixture.repository,
			"wrong",
		); !errors.Is(
			err,
			ErrConflict,
		) {
			t.Fatalf("wrong interrupt run error = %v", err)
		}
		if _, err := fixture.store.InterruptRepositoryReviewRun(
			context.Background(),
			fixture.repository,
			"run",
		); err != nil {
			t.Fatal(err)
		}
		if _, _, err := fixture.store.InterruptAbandonedRepositoryReviewRun(context.Background(), ""); err == nil {
			t.Fatal("invalid abandoned interrupt accepted")
		}
		if _, runID, err := fixture.store.InterruptAbandonedRepositoryReviewRun(
			context.Background(), fixture.repository,
		); err != nil || runID != "" {
			t.Fatalf("no-active abandoned interrupt run=%q err=%v", runID, err)
		}
	})

	t.Run("helpers and validation", func(t *testing.T) {
		if _, err := persistRepositoryReviewCheckpointObservation(
			nil, Plan{}, "run", "assignment", Observation{}, nil, repositoryAuditTestNow,
		); err == nil {
			t.Fatal("nil checkpoint state accepted")
		}
		state := RepositoryState{}
		archiveInterruptedRepositoryReviewRun(nil, repositoryAuditTestNow)
		archiveInterruptedRepositoryReviewRun(&state, repositoryAuditTestNow)

		fixture := newAssignmentCoverageFixture(t, 1, 1)
		activeState, err := fixture.store.BeginRepositoryReviewRun(
			context.Background(),
			BeginRepositoryReviewRunRequest{
				Plan: fixture.plan, RunID: "run", ReviewableFiles: fixture.files,
			},
		)
		if err != nil {
			t.Fatal(err)
		}
		if err := validateRepositoryReviewActiveRun(activeState); err != nil {
			t.Fatalf("valid active run rejected: %v", err)
		}
		mutations := map[string]func(*RepositoryState){
			"duplicate run": func(value *RepositoryState) {
				value.Runs = []ReviewRun{{ID: value.ActiveReviewRun.ID}}
			},
			"bad identity": func(value *RepositoryState) { value.ActiveReviewRun.ID = "" },
			"reservation key": func(value *RepositoryState) {
				for key, reservation := range value.ActiveReviewRun.Reservations {
					delete(value.ActiveReviewRun.Reservations, key)
					value.ActiveReviewRun.Reservations["wrong"] = reservation
					break
				}
			},
			"unknown assignment": func(value *RepositoryState) {
				for key, reservation := range value.ActiveReviewRun.Reservations {
					delete(value.ActiveReviewRun.Reservations, key)
					reservation.AssignmentID = "rra_unknown"
					value.ActiveReviewRun.Reservations[reservation.AssignmentID] = reservation
					break
				}
			},
			"bad files": func(value *RepositoryState) {
				for key, reservation := range value.ActiveReviewRun.Reservations {
					reservation.Files = nil
					value.ActiveReviewRun.Reservations[key] = reservation
					break
				}
			},
			"ack without digest": func(value *RepositoryState) {
				for key, reservation := range value.ActiveReviewRun.Reservations {
					reservation.AcknowledgedFiles = reservation.Files
					value.ActiveReviewRun.Reservations[key] = reservation
					break
				}
			},
			"bad digest": func(value *RepositoryState) {
				for key, reservation := range value.ActiveReviewRun.Reservations {
					reservation.CheckpointDigest = "bad"
					value.ActiveReviewRun.Reservations[key] = reservation
					break
				}
			},
			"bad finding": func(value *RepositoryState) {
				value.ActiveReviewRun.FindingIDs = []string{"missing"}
			},
		}
		for name, mutate := range mutations {
			t.Run(name, func(t *testing.T) {
				candidate := activeState
				active := *activeState.ActiveReviewRun
				active.Reservations = make(map[string]RepositoryReviewAssignmentReservation)
				for key, reservation := range activeState.ActiveReviewRun.Reservations {
					active.Reservations[key] = reservation
				}
				candidate.ActiveReviewRun = &active
				candidate.Runs = append([]ReviewRun(nil), activeState.Runs...)
				mutate(&candidate)
				if err := validateRepositoryReviewActiveRun(candidate); err == nil {
					t.Fatal("invalid active run accepted")
				}
			})
		}
	})
}

func TestRepositoryReviewAssignmentCheckpointPersistenceCoverage(t *testing.T) {
	fixture := newAssignmentCoverageFixture(t, 1, 1)
	state, _, err := fixture.store.Get(fixture.repository)
	if err != nil {
		t.Fatal(err)
	}
	validFinding := repositoryReviewCampaignFinding(fixture.files[0], "duplicate in one checkpoint")
	observation := Observation{
		Model: "review-a", Reviewer: fixture.catalog[0].FocusID,
		ScopeFiles: []FileRef{fixture.files[0]},
		RawDigest:  "sha256:" + strings.Repeat("e", 64),
		Findings:   []FindingCandidate{validFinding, validFinding},
	}
	accepted, err := persistRepositoryReviewCheckpointObservation(
		&state, fixture.plan, "direct-run", fixture.catalog[0].ID,
		observation, fixture.files, repositoryAuditTestNow,
	)
	if err != nil || len(accepted) != 2 || len(state.RawFindings) != 2 ||
		len(state.DeduplicationJobs) != 2 || len(state.Findings) != 1 || len(state.Contexts) != 1 {
		t.Fatalf("merged direct checkpoint accepted=%#v state=%#v err=%v", accepted, state.Findings, err)
	}
	// Retain the context while removing the finding to cover deterministic
	// context replacement on a reconstructed checkpoint.
	state.Findings = nil
	state.RawFindings = nil
	state.DeduplicationJobs = nil
	state.NextDeduplicationOrdinal = 0
	state.FindingsProcessing = FindingsProcessingCounters{}
	if _, err := persistRepositoryReviewCheckpointObservation(
		&state, fixture.plan, "direct-run", fixture.catalog[0].ID,
		observation, fixture.files, repositoryAuditTestNow,
	); err != nil || len(state.Contexts) != 1 {
		t.Fatalf("context replacement state=%#v err=%v", state.Contexts, err)
	}

	for name, finding := range map[string]FindingCandidate{
		"unconfirmed": func() FindingCandidate {
			candidate := validFinding
			candidate.Validation.Status = "unconfirmed"
			return candidate
		}(),
		"unacknowledged": func() FindingCandidate {
			candidate := validFinding
			candidate.File = "outside.go"
			return candidate
		}(),
		"invalid": {
			Severity: "high", Title: "invalid", File: fixture.files[0].Path,
			Validation: Validation{Status: "confirmed"},
		},
	} {
		t.Run(name, func(t *testing.T) {
			candidateState := state
			_, err := persistRepositoryReviewCheckpointObservation(
				&candidateState, fixture.plan, "bad-run", fixture.catalog[0].ID,
				Observation{
					Model: "review-a", Reviewer: fixture.catalog[0].FocusID,
					ScopeFiles: fixture.files, RawDigest: "sha256:" + strings.Repeat("f", 64),
					Findings: []FindingCandidate{finding},
				},
				fixture.files, repositoryAuditTestNow,
			)
			if err == nil {
				t.Fatal("invalid direct checkpoint accepted")
			}
		})
	}
}

func TestRepositoryReviewAssignmentCompletedFinalizeAndArchiveCoverage(t *testing.T) {
	t.Run("completed file and finalized defenses", func(t *testing.T) {
		fixture := newAssignmentCoverageFixture(t, 1, 1)
		if _, err := fixture.store.BeginRepositoryReviewRun(context.Background(), BeginRepositoryReviewRunRequest{
			Plan: fixture.plan, RunID: "run", ReviewableFiles: fixture.files,
		}); err != nil {
			t.Fatal(err)
		}
		for index := range fixture.plan.AssignmentPlans {
			if _, err := fixture.store.CheckpointRepositoryReviewAssignment(
				context.Background(), assignmentCoverageCheckpoint(fixture, "run", index, fixture.files),
			); err != nil {
				t.Fatal(err)
			}
		}
		result, err := fixture.store.FinalizeRepositoryReviewRun(
			context.Background(),
			FinalizeRepositoryReviewRunRequest{
				Plan: fixture.plan, RunID: "run", CompletedAt: repositoryAuditTestNow.Add(time.Minute),
			},
		)
		if err != nil || result.Run.ReviewedFiles != 1 || result.Run.RemainingFiles != 0 ||
			result.State.Files[fixture.files[0].Path].RunID != "run" {
			t.Fatalf("completed finalization run=%#v files=%#v err=%v", result.Run, result.State.Files, err)
		}
		differentPlan := fixture.plan
		differentPlan.TargetBranch = "other"
		differentPlan.ID = planDigest(differentPlan)
		if _, err := fixture.store.FinalizeRepositoryReviewRun(context.Background(), FinalizeRepositoryReviewRunRequest{
			Plan: differentPlan, RunID: "run",
		}); !errors.Is(err, ErrConflict) {
			t.Fatalf("mismatched finalized replay error = %v", err)
		}
		unknownCheckpoint := assignmentCoverageCheckpoint(fixture, "run", 0, fixture.files)
		unknownCheckpoint.AssignmentID = "rra_unknown"
		if _, err := fixture.store.CheckpointRepositoryReviewAssignment(
			context.Background(), unknownCheckpoint,
		); !errors.Is(err, ErrConflict) {
			t.Fatalf("unknown finalized checkpoint error = %v", err)
		}
	})

	t.Run("interrupted and bounded archive", func(t *testing.T) {
		fixture := newAssignmentCoverageFixture(t, 1, 1)
		if _, err := fixture.store.BeginRepositoryReviewRun(context.Background(), BeginRepositoryReviewRunRequest{
			Plan: fixture.plan, RunID: "run", ReviewableFiles: fixture.files,
		}); err != nil {
			t.Fatal(err)
		}
		state, _, err := fixture.store.Get(fixture.repository)
		if err != nil {
			t.Fatal(err)
		}
		state.Runs = make([]ReviewRun, 1000)
		for index := range state.Runs {
			state.Runs[index] = ReviewRun{ID: fmt.Sprintf("old-%04d", index)}
		}
		state.Contexts = append(state.Contexts, FindingContext{
			ID: "context", CampaignID: fixture.campaignID, RunID: "run", Model: "review-a",
		})
		for key, reservation := range state.ActiveReviewRun.Reservations {
			reservation.CheckpointDigest = "sha256:" + strings.Repeat("1", 64)
			reservation.AcknowledgedFiles = reservation.Files
			state.ActiveReviewRun.Reservations[key] = reservation
			break
		}
		archiveInterruptedRepositoryReviewRun(&state, repositoryAuditTestNow)
		if state.ActiveReviewRun != nil || len(state.Runs) != 1000 ||
			!state.Runs[len(state.Runs)-1].Interrupted || len(state.Runs[len(state.Runs)-1].Models) != 1 {
			t.Fatalf("bounded interrupted archive = %#v", state.Runs[len(state.Runs)-1])
		}

		fallback := RepositoryState{ActiveReviewRun: &RepositoryReviewActiveRun{
			ID: "fallback", CampaignID: fixture.campaignID, PlanID: fixture.plan.ID,
			CommitSHA: fixture.plan.CommitSHA, InventoryHash: fixture.plan.InventoryHash,
			ProfileHash: fixture.plan.ProfileHash, StartedAt: repositoryAuditTestNow,
			Reservations: map[string]RepositoryReviewAssignmentReservation{
				fixture.catalog[0].ID: {
					AssignmentID: fixture.catalog[0].ID, Files: fixture.files,
				},
			},
		}}
		archiveInterruptedRepositoryReviewRun(&fallback, repositoryAuditTestNow)
		if len(fallback.Runs) != 1 || fallback.Runs[0].RemainingFiles != 1 || fallback.Runs[0].ScopeDigest != "" {
			t.Fatalf("fallback interrupted archive = %#v", fallback.Runs)
		}
	})
}
