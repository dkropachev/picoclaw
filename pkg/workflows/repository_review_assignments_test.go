package workflows

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"github.com/sipeed/picoclaw/pkg/repoaudit"
)

func TestRepositoryBugFinderAssignmentCatalogAndTrustedTasks(t *testing.T) {
	profileHash := "sha256:" + strings.Repeat("a", 64)
	focuses := RepositoryBugFinderFocuses()
	if len(focuses) != RepositoryReviewRequiredAssignmentsPerReviewer {
		t.Fatalf("focuses = %#v", focuses)
	}
	focuses[0].Task = "mutated caller copy"
	if RepositoryBugFinderFocuses()[0].Task == focuses[0].Task {
		t.Fatal("RepositoryBugFinderFocuses returned aliased state")
	}

	required, err := RepositoryBugFinderAssignmentCatalog(
		[]string{" review-a ", "review-a", "review-b"}, false, " prompt-v1 ", profileHash,
	)
	if err != nil || len(required) != 8 {
		t.Fatalf("required catalog = %#v, %v", required, err)
	}
	for index, assignment := range required {
		if !assignment.Required || assignment.PromptRevision != "prompt-v1" ||
			assignment.ProfileHash != profileHash {
			t.Fatalf("required assignment %d = %#v", index, assignment)
		}
	}
	replayed, err := RepositoryBugFinderAssignmentCatalog(
		[]string{"review-a", "review-b"}, false, "prompt-v1", profileHash,
	)
	if err != nil || !reflect.DeepEqual(replayed, required) {
		t.Fatalf("stable replay = %#v, %v", replayed, err)
	}

	withDefault, err := RepositoryBugFinderAssignmentCatalog(
		[]string{"review-a", "review-b"}, true, "prompt-v1", profileHash,
	)
	if err != nil || len(withDefault) != 12 {
		t.Fatalf("default catalog = %#v, %v", withDefault, err)
	}
	for index, assignment := range withDefault {
		wantRequired := index%3 == 0
		if assignment.Required != wantRequired {
			t.Fatalf("default assignment %d required=%v, want %v", index, assignment.Required, wantRequired)
		}
	}
	defaultOnly, err := RepositoryBugFinderAssignmentCatalog(nil, false, "prompt-v1", profileHash)
	if err != nil || len(defaultOnly) != 4 || !defaultOnly[0].Required || defaultOnly[0].Reviewer != "default" {
		t.Fatalf("implicit default catalog = %#v, %v", defaultOnly, err)
	}
	if _, invalidErr := RepositoryBugFinderAssignmentCatalog(
		[]string{"review-a"}, false, "", profileHash,
	); invalidErr == nil {
		t.Fatal("empty prompt revision was accepted")
	}
	func() {
		original := repositoryBugFinderFocuses
		defer func() { repositoryBugFinderFocuses = original }()
		repositoryBugFinderFocuses = make(
			[]RepositoryReviewFocus,
			maxRepositoryReviewManagedAssignments+1,
		)
		if _, oversizedErr := RepositoryBugFinderAssignmentCatalog(
			[]string{"review-a"}, false, "prompt-v1", profileHash,
		); oversizedErr == nil {
			t.Fatal("oversized assignment cohort was accepted")
		}
	}()

	plan := repositoryReviewUnboundAssignmentPlanForTest(t, required[:4])
	bound, err := BindRepositoryBugFinderAssignmentTasks(plan)
	if err != nil || bound.ID == plan.ID || len(bound.AssignmentPlans) != 4 {
		t.Fatalf("bound plan = %#v, %v", bound, err)
	}
	tasks := make(map[string]string, len(repositoryBugFinderFocuses))
	for _, focus := range repositoryBugFinderFocuses {
		tasks[focus.ID] = focus.Task
	}
	for _, assignmentPlan := range bound.AssignmentPlans {
		if assignmentPlan.Label != assignmentPlan.FocusID ||
			assignmentPlan.Task != tasks[assignmentPlan.FocusID] {
			t.Fatalf("trusted assignment task = %#v", assignmentPlan)
		}
	}
	if _, err := BindRepositoryBugFinderAssignmentTasks(repoaudit.Plan{}); err == nil {
		t.Fatal("task binding accepted a plan without durable identity")
	}
}

func repositoryReviewUnboundAssignmentPlanForTest(
	t *testing.T,
	catalog []repoaudit.RepositoryReviewAssignment,
) repoaudit.Plan {
	t.Helper()
	workspace := t.TempDir()
	store := repoaudit.NewStore(workspace)
	commit := strings.Repeat("a", 40)
	campaignID := repoaudit.NewRepositoryReviewCampaignID()
	if _, err := store.BeginCampaign(context.Background(), repoaudit.BeginCampaignRequest{
		Repository: workspace, CampaignID: campaignID, CommitSHA: commit,
		DeduplicationSnapshot: &repoaudit.RepositoryReviewDeduplicationSnapshot{
			ReviewerModel: catalog[0].Reviewer, DeduplicationModel: catalog[0].Reviewer,
			SimilarityThreshold: repoaudit.DeduplicationDefaultThreshold,
			CandidateLimit:      repoaudit.DeduplicationDefaultCandidateLimit,
		},
	}); err != nil {
		t.Fatal(err)
	}
	plan, err := store.PlanAssignmentsForCampaign(
		context.Background(), workspace, commit, "inventory", catalog[0].ProfileHash,
		campaignID, catalog, []repoaudit.FileRef{{
			Path: "service.go", BlobSHA: strings.Repeat("b", 40), SizeBytes: 10,
			Category: "code", Mode: "100644",
		}}, false, 1, true,
	)
	if err != nil {
		t.Fatal(err)
	}
	return plan
}

func TestRepositoryReviewManagedAssignmentCallbacksRejectInvalidCanonicalEvents(t *testing.T) {
	catalog, err := RepositoryBugFinderAssignmentCatalog(
		[]string{"review-a"}, false, RepositoryBugFinderPromptRevision,
		"sha256:"+strings.Repeat("a", 64),
	)
	if err != nil {
		t.Fatal(err)
	}
	plan := repositoryReviewUnboundAssignmentPlanForTest(t, catalog)
	exec := ExecutionContext{WorkspaceDir: t.TempDir(), RunID: "callback-run"}
	if _, _, callbackErr := repositoryReviewManagedAssignmentCallbacks(
		exec, make(chan int), "rra_callback_test", "main",
	); callbackErr == nil || !strings.Contains(callbackErr.Error(), "durable plan") {
		t.Fatalf("invalid callback plan error = %v", callbackErr)
	}
	withoutCatalog := plan
	withoutCatalog.AssignmentCatalog = nil
	if _, _, callbackErr := repositoryReviewManagedAssignmentCallbacks(
		exec, withoutCatalog, "rra_callback_test", "main",
	); callbackErr == nil || !strings.Contains(callbackErr.Error(), "no assignment catalog") {
		t.Fatalf("missing callback catalog error = %v", callbackErr)
	}
	dispatch, checkpoint, err := repositoryReviewManagedAssignmentCallbacks(
		exec, plan, " rra_callback_test ", " main ",
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := dispatch(ManagedAssignmentDispatchEvent{Scope: []any{"invalid"}}); err == nil {
		t.Fatal("dispatch accepted malformed scope")
	}
	if err := checkpoint(ManagedAssignmentCheckpointEvent{Output: "invalid"}); err == nil ||
		!strings.Contains(err.Error(), "not an object") {
		t.Fatalf("checkpoint output error = %v", err)
	}
	if err := checkpoint(ManagedAssignmentCheckpointEvent{
		ManagedAssignmentDispatchEvent: ManagedAssignmentDispatchEvent{Scope: []any{"invalid"}},
		Output:                         map[string]any{},
	}); err == nil {
		t.Fatal("checkpoint accepted malformed scope")
	}
	scope := nativeRepositoryReviewFileMaps(plan.PendingFiles)
	for _, file := range scope {
		file["contentComplete"] = true
	}
	scopeValues := make([]any, len(scope))
	for index := range scope {
		scopeValues[index] = scope[index]
	}
	if err := checkpoint(ManagedAssignmentCheckpointEvent{
		ManagedAssignmentDispatchEvent: ManagedAssignmentDispatchEvent{Scope: scopeValues},
		Output:                         map[string]any{},
	}); err == nil || !strings.Contains(err.Error(), "reviewedFiles is required") {
		t.Fatalf("checkpoint acknowledgement error = %v", err)
	}
	if err := checkpoint(ManagedAssignmentCheckpointEvent{
		ManagedAssignmentDispatchEvent: ManagedAssignmentDispatchEvent{Scope: scopeValues},
		Output:                         map[string]any{"reviewedFiles": []any{}},
	}); err == nil || !strings.Contains(err.Error(), "missing required field") {
		t.Fatalf("checkpoint observation error = %v", err)
	}
}
