package prdevelopment

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/sipeed/picoclaw/pkg/eventing"
	"github.com/sipeed/picoclaw/pkg/gitworkspace"
)

type controllerEffectTestFixture struct {
	runner     *controllerEffectRunner
	journal    *fakeControllerEffectJournal
	git        *fakeControllerGitBackend
	session    eventing.PRDevelopmentRepairSession
	controller eventing.PRDevelopmentController
	calls      []string
}

func newControllerEffectTestFixture(
	t *testing.T,
	lineVersion, mutationEpoch int64,
	bound bool,
) *controllerEffectTestFixture {
	t.Helper()
	createdAt := time.Date(2026, 8, 9, 20, 4, 5, 987654321, time.UTC)
	session := eventing.PRDevelopmentRepairSession{
		ID:             "pdrs_0123456789abcdef0123456789abcdef",
		CaseID:         "pdc_0123456789abcdef0123456789abcdef",
		AgentID:        "agent-controller-effects",
		HeadRepository: "review-user/project-fork",
		HeadRef:        "refs/heads/repair/retries",
		HeadSHA:        strings.Repeat("1", 40),
		CloneURL:       "https://github.com/review-user/project-fork.git",
		ReviewDigest:   strings.Repeat("2", 64),
		ReservationKey: "session-reservation-must-not-be-used",
		WorkspaceID:    "gw-controller-effects",
		Attempts: []eventing.PRDevelopmentRepairAttempt{{
			ID:        "pdr_0123456789abcdef0123456789abcdef",
			SessionID: "pdrs_0123456789abcdef0123456789abcdef",
			Status:    eventing.PRDevelopmentRepairQueued,
			CreatedAt: createdAt,
		}},
		CreatedAt: createdAt,
		UpdatedAt: createdAt,
	}
	controller := eventing.PRDevelopmentController{
		ID:                     "pdctl_0123456789abcdef0123456789abcdef",
		ThreadID:               "pdthread_0123456789abcdef0123456789abcdef",
		OwnerSessionID:         session.ID,
		AgentID:                session.AgentID,
		Revision:               7,
		Phase:                  eventing.PRDevelopmentControllerMutation,
		LineID:                 "pdline_0123456789abcdef0123456789abcdef",
		LineVersion:            lineVersion,
		MutationEpoch:          mutationEpoch,
		CurrentAttemptID:       session.Attempts[0].ID,
		LeaseKind:              eventing.PRDevelopmentControllerMutationLease,
		LeaseOwner:             "controller-effect-worker",
		LeaseToken:             "mutation-lease-token-private",
		LeaseEpoch:             3,
		MutationReservationKey: "controller-reservation-private",
		Claims:                 1,
		CreatedAt:              createdAt,
		UpdatedAt:              createdAt,
	}
	if bound {
		controller.WorkspaceID = session.WorkspaceID
		controller.SourceCloneURL = session.CloneURL
		controller.SourceRef = session.HeadRef
		controller.SourceCommit = session.HeadSHA
		controller.SourceTree = strings.Repeat("3", 40)
		controller.TipCommit = strings.Repeat("4", 40)
		controller.Tree = strings.Repeat("5", 40)
	}
	fixture := &controllerEffectTestFixture{session: session, controller: controller}
	fixture.journal = &fakeControllerEffectJournal{
		t:            t,
		calls:        &fixture.calls,
		session:      session,
		controller:   controller,
		operations:   make(map[string]eventing.PRDevelopmentControllerOperation),
		failFinalize: make(map[eventing.PRDevelopmentControllerOperationKind]int),
	}
	fixture.git = &fakeControllerGitBackend{t: t, calls: &fixture.calls}
	runner, err := newControllerEffectRunnerWithBackend(
		fixture.journal,
		fixture.git,
		session,
		eventing.PRDevelopmentControllerLease{Controller: controller},
	)
	if err != nil {
		t.Fatalf("newControllerEffectRunnerWithBackend() error = %v", err)
	}
	fixture.runner = runner
	return fixture
}

func TestControllerEffectRunnerAdoptCommitAndParkExactOrder(t *testing.T) {
	t.Parallel()
	fixture := newControllerEffectTestFixture(t, 0, 0, false)
	sourceTree := strings.Repeat("3", 40)
	candidateTree := strings.Repeat("6", 40)
	commit := strings.Repeat("7", 40)
	candidateDigest := strings.Repeat("8", 64)
	reviewDigest := strings.Repeat("9", 64)

	fixture.git.adopt = func(request gitworkspace.PinnedLineAdoptRequest) (gitworkspace.PinnedLineLease, error) {
		requireTestPin(t, request.Pin, fixture.session, fixture.controller)
		if request.WorkspaceID != fixture.session.WorkspaceID ||
			request.LineID != fixture.controller.LineID || request.ExpectedTree != sourceTree {
			t.Fatalf("Adopt request = %#v", request)
		}
		return gitworkspace.PinnedLineLease{
			WorkspaceID: fixture.session.WorkspaceID, Version: 0, MutationEpoch: 1,
			Tip: fixture.session.HeadSHA, Tree: sourceTree,
		}, nil
	}
	line, err := fixture.runner.Adopt(context.Background(), sourceTree)
	if err != nil {
		t.Fatalf("Adopt() error = %v", err)
	}
	if line.Revision != fixture.controller.Revision+1 || line.Tree != sourceTree ||
		line.MutationEpoch != 1 {
		t.Fatalf("Adopt() = %#v", line)
	}

	fixture.git.commit = func(request gitworkspace.PinnedCommitRequest) (gitworkspace.PinnedCommitResult, error) {
		requireTestPin(t, request.Pin, fixture.session, fixture.controller)
		if request.ExpectedParent != fixture.session.HeadSHA || request.ExpectedTree != candidateTree ||
			request.ExpectedCandidateDigest != candidateDigest ||
			request.IntentID != fixture.runner.identities.CommitIntent ||
			request.Message != "Apply the focused repair" ||
			!request.AuthoredAt.Equal(fixture.session.Attempts[0].CreatedAt.UTC().Truncate(time.Second)) ||
			request.AuthoredAt.Nanosecond() != 0 {
			t.Fatalf("Commit request = %#v", request)
		}
		return gitworkspace.PinnedCommitResult{
			WorkspaceID: fixture.session.WorkspaceID, IntentID: request.IntentID,
			ParentCommit: request.ExpectedParent, Tree: request.ExpectedTree,
			CandidateDigest: request.ExpectedCandidateDigest, Commit: commit,
			ChangedFiles: 2, WorkspaceClean: true,
		}, nil
	}
	committed, err := fixture.runner.CommitCandidate(
		context.Background(),
		gitworkspace.PinnedCandidate{
			WorkspaceID: fixture.session.WorkspaceID, ParentCommit: fixture.session.HeadSHA,
			Tree: candidateTree, CandidateDigest: candidateDigest, ChangedFiles: 2,
		},
		"Apply the focused repair",
	)
	if err != nil {
		t.Fatalf("CommitCandidate() error = %v", err)
	}
	if !committed.changed || committed.commit != commit {
		t.Fatalf("CommitCandidate() = %#v", committed)
	}

	var preview gitworkspace.PinnedLineReviewSnapshot
	fixture.git.preview = func(request gitworkspace.PinnedLineParkRequest) (gitworkspace.PinnedLineReviewSnapshot, error) {
		requireChangedParkRequest(t, request, fixture, commit, candidateTree)
		preview = reviewSnapshot(request, reviewDigest, []string{"repair.go"}, "diff --git a/repair.go b/repair.go\n")
		return preview, nil
	}
	fixture.git.park = func(request gitworkspace.PinnedLineParkRequest) (gitworkspace.PinnedLineParkResult, error) {
		requireChangedParkRequest(t, request, fixture, commit, candidateTree)
		return parkResult(request, false), nil
	}
	fixture.git.snapshot = func(request gitworkspace.PinnedLineReviewRequest) (gitworkspace.PinnedLineReviewSnapshot, error) {
		if request.LineID != fixture.controller.LineID || request.ExpectedVersion != 1 ||
			request.ExpectedBase != fixture.session.HeadSHA || request.ExpectedTip != commit ||
			request.ExpectedTree != candidateTree {
			t.Fatalf("Snapshot request = %#v", request)
		}
		return preview, nil
	}
	fence, err := fixture.runner.Park(context.Background(), committed, "Repair complete.", 2)
	if err != nil {
		t.Fatalf("Park() error = %v", err)
	}
	if fence.TipCommit != commit || fence.Tree != candidateTree || fence.NoChanges ||
		fence.LineReviewDigest != reviewDigest {
		t.Fatalf("Park() fence = %#v", fence)
	}
	wantCalls := []string{
		"prepare:adopt", "git:adopt", "finalize:adopt",
		"prepare:commit", "git:commit", "finalize:commit",
		"git:preview", "prepare:park", "git:park", "git:snapshot", "finalize:park",
	}
	if !slices.Equal(fixture.calls, wantCalls) {
		t.Fatalf("calls = %#v, want %#v", fixture.calls, wantCalls)
	}
	if fixture.runner.controller.Phase != eventing.PRDevelopmentControllerReviewPending ||
		fixture.runner.controller.MutationReservationKey != "" ||
		fixture.runner.controller.LeaseToken != "" || fixture.runner.reservation != "" ||
		fixture.runner.leaseToken != "" {
		t.Fatalf("terminal controller retained authority: %#v", fixture.runner.controller)
	}
	encoded, err := json.Marshal(fixture.runner)
	if err != nil || string(encoded) != `{}` {
		t.Fatalf("json.Marshal(runner) = %s, %v", encoded, err)
	}
	beforeReplay := len(fixture.calls)
	replayed, err := fixture.runner.Park(context.Background(), committed, "Repair complete.", 2)
	if err != nil || replayed.FenceHash != fence.FenceHash || len(fixture.calls) != beforeReplay {
		t.Fatalf("terminal Park replay = %#v, %v; calls = %#v", replayed, err, fixture.calls)
	}
}

func TestControllerEffectRunnerNoChangeSkipsCommitAndParksExactTip(t *testing.T) {
	t.Parallel()
	fixture := newControllerEffectTestFixture(t, 0, 1, true)
	candidate := gitworkspace.PinnedCandidate{
		WorkspaceID:     fixture.session.WorkspaceID,
		ParentCommit:    fixture.controller.TipCommit,
		Tree:            fixture.controller.Tree,
		CandidateDigest: strings.Repeat("a", 64),
		ChangedFiles:    0,
	}
	noChange, err := fixture.runner.CommitCandidate(context.Background(), candidate, "unused")
	if err != nil {
		t.Fatalf("CommitCandidate(no-change) error = %v", err)
	}
	if noChange.changed || len(fixture.calls) != 0 {
		t.Fatalf("no-change outcome = %#v; calls = %#v", noChange, fixture.calls)
	}
	reviewDigest := strings.Repeat("b", 64)
	var preview gitworkspace.PinnedLineReviewSnapshot
	fixture.git.preview = func(request gitworkspace.PinnedLineParkRequest) (gitworkspace.PinnedLineReviewSnapshot, error) {
		requireNoChangeParkRequest(t, request, fixture)
		preview = reviewSnapshot(request, reviewDigest, nil, "")
		return preview, nil
	}
	fixture.git.park = func(request gitworkspace.PinnedLineParkRequest) (gitworkspace.PinnedLineParkResult, error) {
		requireNoChangeParkRequest(t, request, fixture)
		return parkResult(request, false), nil
	}
	fixture.git.snapshot = func(request gitworkspace.PinnedLineReviewRequest) (gitworkspace.PinnedLineReviewSnapshot, error) {
		return preview, nil
	}
	fence, err := fixture.runner.Park(context.Background(), noChange, "No changes required.", 1)
	if err != nil {
		t.Fatalf("Park(no-change) error = %v", err)
	}
	if !fence.NoChanges || fence.BaseCommit != fixture.controller.TipCommit ||
		fence.TipCommit != fixture.controller.TipCommit || fence.Tree != fixture.controller.Tree {
		t.Fatalf("no-change fence = %#v", fence)
	}
	wantCalls := []string{"git:preview", "prepare:park", "git:park", "git:snapshot", "finalize:park"}
	if !slices.Equal(fixture.calls, wantCalls) {
		t.Fatalf("calls = %#v, want %#v", fixture.calls, wantCalls)
	}
}

func TestControllerEffectRunnerPreviewFailureDoesNotPrepareOrPark(t *testing.T) {
	t.Parallel()
	fixture := newControllerEffectTestFixture(t, 0, 1, true)
	noChange, err := fixture.runner.CommitCandidate(
		context.Background(),
		gitworkspace.PinnedCandidate{
			WorkspaceID:     fixture.session.WorkspaceID,
			ParentCommit:    fixture.controller.TipCommit,
			Tree:            fixture.controller.Tree,
			CandidateDigest: strings.Repeat("c", 64),
		},
		"unused",
	)
	if err != nil {
		t.Fatalf("CommitCandidate() error = %v", err)
	}
	previewFailure := errors.New("bounded review is too large")
	fixture.git.preview = func(gitworkspace.PinnedLineParkRequest) (gitworkspace.PinnedLineReviewSnapshot, error) {
		return gitworkspace.PinnedLineReviewSnapshot{}, previewFailure
	}
	_, err = fixture.runner.Park(context.Background(), noChange, "No changes required.", 1)
	if !errors.Is(err, previewFailure) {
		t.Fatalf("Park() error = %v, want %v", err, previewFailure)
	}
	if !slices.Equal(fixture.calls, []string{"git:preview"}) {
		t.Fatalf("calls after preview failure = %#v", fixture.calls)
	}
	if fixture.runner.reservation == "" || fixture.runner.controller.MutationReservationKey == "" {
		t.Fatal("preview failure retired mutation authority")
	}
}

func TestControllerEffectRunnerParkReplayReusesPreparedIntentAndPreview(t *testing.T) {
	t.Parallel()
	fixture := newControllerEffectTestFixture(t, 0, 1, true)
	noChange, err := fixture.runner.CommitCandidate(
		context.Background(),
		gitworkspace.PinnedCandidate{
			WorkspaceID:     fixture.session.WorkspaceID,
			ParentCommit:    fixture.controller.TipCommit,
			Tree:            fixture.controller.Tree,
			CandidateDigest: strings.Repeat("d", 64),
		},
		"unused",
	)
	if err != nil {
		t.Fatalf("CommitCandidate() error = %v", err)
	}
	reviewDigest := strings.Repeat("e", 64)
	var preview gitworkspace.PinnedLineReviewSnapshot
	fixture.git.preview = func(request gitworkspace.PinnedLineParkRequest) (gitworkspace.PinnedLineReviewSnapshot, error) {
		preview = reviewSnapshot(request, reviewDigest, nil, "")
		return preview, nil
	}
	parkCalls := 0
	fixture.git.park = func(request gitworkspace.PinnedLineParkRequest) (gitworkspace.PinnedLineParkResult, error) {
		parkCalls++
		return parkResult(request, parkCalls > 1), nil
	}
	fixture.git.snapshot = func(gitworkspace.PinnedLineReviewRequest) (gitworkspace.PinnedLineReviewSnapshot, error) {
		return preview, nil
	}
	fixture.journal.failFinalize[eventing.PRDevelopmentControllerOperationPark] = 1
	_, err = fixture.runner.Park(context.Background(), noChange, "No changes required.", 1)
	if err == nil {
		t.Fatal("first Park() error = nil")
	}
	fence, err := fixture.runner.Park(context.Background(), noChange, "No changes required.", 1)
	if err != nil || fence.LineReviewDigest != reviewDigest {
		t.Fatalf("replayed Park() = %#v, %v", fence, err)
	}
	wantCalls := []string{
		"git:preview", "prepare:park", "git:park", "git:snapshot", "finalize:park",
		"git:park", "git:snapshot", "finalize:park",
	}
	if !slices.Equal(fixture.calls, wantCalls) {
		t.Fatalf("replay calls = %#v, want %#v", fixture.calls, wantCalls)
	}
}

func TestControllerEffectRunnerRejectsNoncanonicalParkBeforeEffect(t *testing.T) {
	t.Parallel()
	fixture := newControllerEffectTestFixture(t, 0, 1, true)
	noChange, err := fixture.runner.CommitCandidate(
		context.Background(),
		gitworkspace.PinnedCandidate{
			WorkspaceID:     fixture.session.WorkspaceID,
			ParentCommit:    fixture.controller.TipCommit,
			Tree:            fixture.controller.Tree,
			CandidateDigest: strings.Repeat("f", 64),
		},
		"unused",
	)
	if err != nil {
		t.Fatalf("CommitCandidate() error = %v", err)
	}
	fixture.git.preview = func(request gitworkspace.PinnedLineParkRequest) (gitworkspace.PinnedLineReviewSnapshot, error) {
		return reviewSnapshot(request, strings.Repeat("0", 64), nil, ""), nil
	}
	fixture.journal.mutatePrepared = func(operation *eventing.PRDevelopmentControllerOperation) {
		operation.Request.Tree = strings.Repeat("f", 40)
	}
	_, err = fixture.runner.Park(context.Background(), noChange, "No changes required.", 1)
	if !errors.Is(err, errControllerEffectConflict) {
		t.Fatalf("Park(noncanonical) error = %v", err)
	}
	if !slices.Equal(fixture.calls, []string{"git:preview", "prepare:park"}) {
		t.Fatalf("calls after noncanonical prepare = %#v", fixture.calls)
	}
}

func TestControllerEffectRunnerResumeUsesFreshControllerReservation(t *testing.T) {
	t.Parallel()
	fixture := newControllerEffectTestFixture(t, 2, 2, true)
	fixture.git.resume = func(request gitworkspace.PinnedLineResumeRequest) (gitworkspace.PinnedLineLease, error) {
		requireTestPin(t, request.Pin, fixture.session, fixture.controller)
		if request.ExpectedVersion != 2 || request.ExpectedEpoch != 2 ||
			request.ExpectedTip != fixture.controller.TipCommit ||
			request.ExpectedTree != fixture.controller.Tree {
			t.Fatalf("Resume request = %#v", request)
		}
		return gitworkspace.PinnedLineLease{
			WorkspaceID:   fixture.session.WorkspaceID,
			Version:       2,
			MutationEpoch: 3,
			Tip:           fixture.controller.TipCommit,
			Tree:          fixture.controller.Tree,
			AlreadyOwned:  true,
		}, nil
	}
	line, err := fixture.runner.Resume(context.Background())
	if err != nil {
		t.Fatalf("Resume() error = %v", err)
	}
	if line.MutationEpoch != 3 || line.LineVersion != 2 {
		t.Fatalf("Resume() = %#v", line)
	}
	want := []string{"prepare:resume", "git:resume", "finalize:resume"}
	if !slices.Equal(fixture.calls, want) {
		t.Fatalf("calls = %#v, want %#v", fixture.calls, want)
	}
}

type fakeControllerEffectJournal struct {
	t              *testing.T
	calls          *[]string
	session        eventing.PRDevelopmentRepairSession
	controller     eventing.PRDevelopmentController
	operations     map[string]eventing.PRDevelopmentControllerOperation
	failFinalize   map[eventing.PRDevelopmentControllerOperationKind]int
	mutatePrepared func(*eventing.PRDevelopmentControllerOperation)
}

func (journal *fakeControllerEffectJournal) PreparePRDevelopmentControllerOperation(
	_ context.Context,
	input eventing.PRDevelopmentControllerOperationPrepare,
) (eventing.PRDevelopmentControllerOperation, bool, error) {
	*journal.calls = append(*journal.calls, "prepare:"+string(input.Kind))
	if existing, ok := journal.operations[input.OperationID]; ok {
		return existing, false, nil
	}
	controller := journal.controller
	operation := eventing.PRDevelopmentControllerOperation{
		ID:                         input.OperationID,
		ControllerID:               input.ControllerID,
		AttemptID:                  input.AttemptID,
		Kind:                       input.Kind,
		Status:                     eventing.PRDevelopmentControllerOperationPending,
		PreparedControllerRevision: input.ExpectedRevision,
		AgentID:                    controller.AgentID,
		WorkspaceID:                journal.session.WorkspaceID,
		LineID:                     controller.LineID,
		SourceCloneURL:             journal.session.CloneURL,
		SourceRef:                  journal.session.HeadRef,
		SourceCommit:               journal.session.HeadSHA,
		SourceTree:                 controller.SourceTree,
		LineVersion:                controller.LineVersion,
		MutationEpoch:              controller.MutationEpoch,
		TipCommit:                  controller.TipCommit,
		Tree:                       controller.Tree,
		MutationReservationDigest:  strings.Repeat("a", 64),
		MutationLeaseEpoch:         input.LeaseEpoch,
		MutationLeaseTokenDigest:   strings.Repeat("b", 64),
		EffectIntentID:             input.Request.EffectIntentID,
		Request:                    input.Request,
	}
	if input.Kind == eventing.PRDevelopmentControllerOperationAdopt {
		operation.SourceTree = input.Request.ExpectedTree
		operation.TipCommit = journal.session.HeadSHA
		operation.Tree = input.Request.ExpectedTree
	}
	if journal.mutatePrepared != nil {
		journal.mutatePrepared(&operation)
	}
	journal.operations[input.OperationID] = operation
	return operation, true, nil
}

func (journal *fakeControllerEffectJournal) FinalizePRDevelopmentControllerOperation(
	_ context.Context,
	input eventing.PRDevelopmentControllerOperationFinalize,
) (eventing.PRDevelopmentControllerOperationTransition, bool, error) {
	operation, ok := journal.operations[input.OperationID]
	if !ok {
		journal.t.Fatalf("Finalize operation %q was not prepared", input.OperationID)
	}
	*journal.calls = append(*journal.calls, "finalize:"+string(operation.Kind))
	if journal.failFinalize[operation.Kind] > 0 {
		journal.failFinalize[operation.Kind]--
		return eventing.PRDevelopmentControllerOperationTransition{}, false, errors.New("injected finalization failure")
	}
	durable := input.Result
	durable.AlreadyOwned = false
	durable.AlreadyApplied = false
	durable.AlreadyParked = false
	operation.Status = eventing.PRDevelopmentControllerOperationFinalized
	operation.Result = durable
	controller := journal.controller
	var fence *eventing.PRDevelopmentAttemptReviewFence
	switch operation.Kind {
	case eventing.PRDevelopmentControllerOperationAdopt:
		controller.WorkspaceID = journal.session.WorkspaceID
		controller.SourceCloneURL = journal.session.CloneURL
		controller.SourceRef = journal.session.HeadRef
		controller.SourceCommit = journal.session.HeadSHA
		controller.SourceTree = operation.Request.ExpectedTree
		controller.LineVersion = durable.Version
		controller.MutationEpoch = durable.MutationEpoch
		controller.TipCommit = durable.Tip
		controller.Tree = durable.Tree
		controller.Revision++
	case eventing.PRDevelopmentControllerOperationResume:
		controller.MutationEpoch = durable.MutationEpoch
		controller.Revision++
	case eventing.PRDevelopmentControllerOperationCommit:
	case eventing.PRDevelopmentControllerOperationPark:
		controller.LineVersion = durable.Version
		controller.MutationEpoch = durable.MutationEpoch
		controller.TipCommit = durable.Tip
		controller.Tree = durable.Tree
		controller.Revision++
		controller.Phase = eventing.PRDevelopmentControllerReviewPending
		controller.LeaseKind = ""
		controller.LeaseOwner = ""
		controller.LeaseToken = ""
		controller.LeaseUntil = nil
		controller.MutationReservationKey = ""
		fence = &eventing.PRDevelopmentAttemptReviewFence{
			AttemptID:        operation.AttemptID,
			ControllerID:     operation.ControllerID,
			ThreadID:         controller.ThreadID,
			LineID:           controller.LineID,
			Ordinal:          controller.FenceCount,
			LineVersion:      durable.Version,
			MutationEpoch:    durable.MutationEpoch,
			ParkIntentID:     durable.ReviewParkIntentID,
			BaseCommit:       durable.ReviewBaseCommit,
			TipCommit:        durable.ReviewCommit,
			Tree:             durable.ReviewTree,
			NoChanges:        durable.NoChanges,
			LineReviewDigest: durable.ReviewDigest,
			FenceHash:        strings.Repeat("c", 64),
		}
		controller.FenceCount++
	default:
		journal.t.Fatalf("unexpected operation kind %q", operation.Kind)
	}
	journal.controller = controller
	journal.operations[operation.ID] = operation
	return eventing.PRDevelopmentControllerOperationTransition{
		Controller: controller,
		Operation:  operation,
		Fence:      fence,
	}, true, nil
}

type fakeControllerGitBackend struct {
	t        *testing.T
	calls    *[]string
	adopt    func(gitworkspace.PinnedLineAdoptRequest) (gitworkspace.PinnedLineLease, error)
	resume   func(gitworkspace.PinnedLineResumeRequest) (gitworkspace.PinnedLineLease, error)
	commit   func(gitworkspace.PinnedCommitRequest) (gitworkspace.PinnedCommitResult, error)
	preview  func(gitworkspace.PinnedLineParkRequest) (gitworkspace.PinnedLineReviewSnapshot, error)
	park     func(gitworkspace.PinnedLineParkRequest) (gitworkspace.PinnedLineParkResult, error)
	snapshot func(gitworkspace.PinnedLineReviewRequest) (gitworkspace.PinnedLineReviewSnapshot, error)
}

func (git *fakeControllerGitBackend) AdoptPinnedLine(
	_ context.Context,
	request gitworkspace.PinnedLineAdoptRequest,
) (gitworkspace.PinnedLineLease, error) {
	*git.calls = append(*git.calls, "git:adopt")
	if git.adopt == nil {
		git.t.Fatal("unexpected AdoptPinnedLine call")
	}
	return git.adopt(request)
}

func (git *fakeControllerGitBackend) ResumePinnedLine(
	_ context.Context,
	request gitworkspace.PinnedLineResumeRequest,
) (gitworkspace.PinnedLineLease, error) {
	*git.calls = append(*git.calls, "git:resume")
	if git.resume == nil {
		git.t.Fatal("unexpected ResumePinnedLine call")
	}
	return git.resume(request)
}

func (git *fakeControllerGitBackend) CommitPinned(
	_ context.Context,
	request gitworkspace.PinnedCommitRequest,
) (gitworkspace.PinnedCommitResult, error) {
	*git.calls = append(*git.calls, "git:commit")
	if git.commit == nil {
		git.t.Fatal("unexpected CommitPinned call")
	}
	return git.commit(request)
}

func (git *fakeControllerGitBackend) PreviewPinnedLineReview(
	_ context.Context,
	request gitworkspace.PinnedLineParkRequest,
) (gitworkspace.PinnedLineReviewSnapshot, error) {
	*git.calls = append(*git.calls, "git:preview")
	if git.preview == nil {
		git.t.Fatal("unexpected PreviewPinnedLineReview call")
	}
	return git.preview(request)
}

func (git *fakeControllerGitBackend) ParkPinnedLine(
	_ context.Context,
	request gitworkspace.PinnedLineParkRequest,
) (gitworkspace.PinnedLineParkResult, error) {
	*git.calls = append(*git.calls, "git:park")
	if git.park == nil {
		git.t.Fatal("unexpected ParkPinnedLine call")
	}
	return git.park(request)
}

func (git *fakeControllerGitBackend) SnapshotPinnedLineReview(
	_ context.Context,
	request gitworkspace.PinnedLineReviewRequest,
) (gitworkspace.PinnedLineReviewSnapshot, error) {
	*git.calls = append(*git.calls, "git:snapshot")
	if git.snapshot == nil {
		git.t.Fatal("unexpected SnapshotPinnedLineReview call")
	}
	return git.snapshot(request)
}

func requireTestPin(
	t *testing.T,
	pin gitworkspace.PinnedAcquireRequest,
	session eventing.PRDevelopmentRepairSession,
	controller eventing.PRDevelopmentController,
) {
	t.Helper()
	if pin.Repository != session.CloneURL || pin.Repository == session.HeadRepository ||
		pin.SourceRef != session.HeadRef || pin.ExpectedCommit != session.HeadSHA ||
		pin.ReservationKey != controller.MutationReservationKey ||
		pin.ReservationKey == session.ReservationKey || pin.AgentID != session.AgentID {
		t.Fatalf("pinned Git identity = %#v", pin)
	}
}

func requireChangedParkRequest(
	t *testing.T,
	request gitworkspace.PinnedLineParkRequest,
	fixture *controllerEffectTestFixture,
	commit, tree string,
) {
	t.Helper()
	requireTestPin(t, request.Pin, fixture.session, fixture.controller)
	if request.WorkspaceID != fixture.session.WorkspaceID ||
		request.LineID != fixture.controller.LineID ||
		request.IntentID != fixture.runner.identities.ParkIntent ||
		request.ExpectedVersion != 0 || request.MutationEpoch != 1 ||
		request.PreviousTip != fixture.session.HeadSHA || request.Tip != commit ||
		request.Tree != tree || request.NoChanges {
		t.Fatalf("changed Park request = %#v", request)
	}
}

func requireNoChangeParkRequest(
	t *testing.T,
	request gitworkspace.PinnedLineParkRequest,
	fixture *controllerEffectTestFixture,
) {
	t.Helper()
	requireTestPin(t, request.Pin, fixture.session, fixture.controller)
	if request.ExpectedVersion != fixture.controller.LineVersion ||
		request.MutationEpoch != fixture.controller.MutationEpoch ||
		request.PreviousTip != fixture.controller.TipCommit ||
		request.Tip != fixture.controller.TipCommit || request.Tree != fixture.controller.Tree ||
		!request.NoChanges {
		t.Fatalf("no-change Park request = %#v", request)
	}
}

func reviewSnapshot(
	request gitworkspace.PinnedLineParkRequest,
	digest string,
	paths []string,
	diff string,
) gitworkspace.PinnedLineReviewSnapshot {
	return gitworkspace.PinnedLineReviewSnapshot{
		Version:       request.ExpectedVersion + 1,
		MutationEpoch: request.MutationEpoch,
		ParkIntentID:  request.IntentID,
		BaseCommit:    request.PreviousTip,
		Commit:        request.Tip,
		Tree:          request.Tree,
		ChangedPaths:  paths,
		UnifiedDiff:   diff,
		ReviewDigest:  digest,
	}
}

func parkResult(
	request gitworkspace.PinnedLineParkRequest,
	alreadyParked bool,
) gitworkspace.PinnedLineParkResult {
	return gitworkspace.PinnedLineParkResult{
		WorkspaceID:    request.WorkspaceID,
		Version:        request.ExpectedVersion + 1,
		MutationEpoch:  request.MutationEpoch,
		PreviousTip:    request.PreviousTip,
		Tip:            request.Tip,
		Tree:           request.Tree,
		NoChanges:      request.NoChanges,
		AlreadyParked:  alreadyParked,
		WorkspaceClean: true,
	}
}
