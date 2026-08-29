package workflows

import (
	"context"
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/sipeed/picoclaw/pkg/repoaudit"
)

//nolint:govet // Boundary assertions intentionally reuse err in short scopes.
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
	if _, err := RepositoryBugFinderAssignmentCatalog(
		[]string{"review-a"}, false, "", profileHash,
	); err == nil {
		t.Fatal("empty prompt revision was accepted")
	}
	func() {
		original := repositoryBugFinderFocuses
		defer func() { repositoryBugFinderFocuses = original }()
		repositoryBugFinderFocuses = make(
			[]RepositoryReviewFocus,
			maxRepositoryReviewManagedAssignments+1,
		)
		if _, err := RepositoryBugFinderAssignmentCatalog(
			[]string{"review-a"}, false, "prompt-v1", profileHash,
		); err == nil {
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

//nolint:govet // Boundary assertions intentionally reuse err in short scopes.
func TestNativeRepositoryReviewAssignmentPlanBeginAndManagedCallbacks(t *testing.T) {
	workspace, exec, output, plan, scope := repositoryReviewNativeAssignmentPlanForTest(t)
	assignmentOutputs, err := nativeMapSlice(output["assignmentPlans"])
	if err != nil || len(assignmentOutputs) != 4 {
		t.Fatalf("assignment plan output = %#v, %v", output["assignmentPlans"], err)
	}
	for _, assignment := range assignmentOutputs {
		if nativeAnyString(assignment["assignment_id"]) == "" ||
			nativeAnyString(assignment["focus_id"]) == "" ||
			nativeAnyString(assignment["task"]) == "" {
			t.Fatalf("incomplete assignment plan output = %#v", assignment)
		}
	}

	legacyBegin, err := nativeRepositoryReview(context.Background(), map[string]any{
		"action": "begin", "plan": repoaudit.Plan{StateVersion: 17},
	}, exec)
	if err != nil || legacyBegin["stateVersion"] != int64(17) {
		t.Fatalf("legacy begin = %#v, %v", legacyBegin, err)
	}
	if _, err := nativeRepositoryReview(context.Background(), map[string]any{
		"action": "begin", "plan": make(chan int),
	}, exec); err == nil {
		t.Fatal("begin accepted an unencodable plan")
	}
	if _, err := nativeRepositoryReview(context.Background(), map[string]any{
		"action": "begin", "plan": output["plan"], "files": "invalid",
	}, exec); err == nil || !strings.Contains(err.Error(), "reviewable repository files") {
		t.Fatalf("invalid reviewable scope error = %v", err)
	}

	begin, err := nativeRepositoryReview(context.Background(), map[string]any{
		"action": "begin", "plan": output["plan"], "files": scope,
	}, exec)
	if err != nil || begin["stateVersion"] == nil {
		t.Fatalf("assignment begin = %#v, %v", begin, err)
	}

	if dispatch, checkpoint, err := repositoryReviewManagedAssignmentCallbacks(
		exec, repoaudit.Plan{ID: "legacy-plan"},
	); err != nil || dispatch != nil || checkpoint != nil {
		t.Fatalf("legacy callbacks = (%v, %v, %v)", dispatch, checkpoint, err)
	}
	for _, invalid := range []any{map[string]any{}, make(chan int)} {
		if _, _, err := repositoryReviewManagedAssignmentCallbacks(exec, invalid); err == nil {
			t.Fatalf("managed callbacks accepted invalid plan %#v", invalid)
		}
	}

	dispatch, checkpoint, err := repositoryReviewManagedAssignmentCallbacks(exec, output["plan"])
	if err != nil || dispatch == nil || checkpoint == nil {
		t.Fatalf("managed callbacks = (%v, %v, %v)", dispatch, checkpoint, err)
	}
	firstEvent := repositoryReviewAssignmentDispatchEventForTest(plan.AssignmentPlans[0], scope)
	if err := dispatch(firstEvent); err != nil {
		t.Fatalf("dispatch exact reservation: %v", err)
	}
	if err := dispatch(ManagedAssignmentDispatchEvent{
		AssignmentID: firstEvent.AssignmentID, Scope: []any{"invalid"},
	}); err == nil {
		t.Fatal("dispatch accepted malformed scope")
	}
	wrongScope := firstEvent
	wrongScope.Scope = []any{map[string]any{
		"path": "other.go", "fileHash": strings.Repeat("c", 40), "sizeBytes": int64(1),
	}}
	if err := dispatch(wrongScope); err == nil {
		t.Fatal("dispatch accepted a scope outside its reservation")
	}

	validOutput := map[string]any{
		"summary": "No confirmed findings.", "reviewedFiles": []any{"service.go"},
		"findings": []any{}, "residualRisks": []any{},
	}
	if err := checkpoint(ManagedAssignmentCheckpointEvent{
		ManagedAssignmentDispatchEvent: firstEvent, Output: "invalid",
	}); err == nil {
		t.Fatal("checkpoint accepted a non-object output")
	}
	badScope := firstEvent
	badScope.Scope = []any{"invalid"}
	if err := checkpoint(ManagedAssignmentCheckpointEvent{
		ManagedAssignmentDispatchEvent: badScope, Output: validOutput,
	}); err == nil {
		t.Fatal("checkpoint accepted malformed scope")
	}
	if err := checkpoint(ManagedAssignmentCheckpointEvent{
		ManagedAssignmentDispatchEvent: firstEvent,
		Output: map[string]any{
			"summary": "bad acknowledgement", "reviewedFiles": []any{"other.go"},
			"findings": []any{}, "residualRisks": []any{},
		},
	}); err == nil {
		t.Fatal("checkpoint accepted an acknowledgement outside its scope")
	}
	if err := checkpoint(ManagedAssignmentCheckpointEvent{
		ManagedAssignmentDispatchEvent: firstEvent,
		Output: map[string]any{
			"summary": "missing fields", "reviewedFiles": []any{"service.go"},
		},
	}); err == nil {
		t.Fatal("checkpoint accepted an invalid structured observation")
	}
	badDigest := ManagedAssignmentCheckpointEvent{
		ManagedAssignmentDispatchEvent: firstEvent, Output: validOutput,
		OutputDigest: "invalid", CheckpointDigest: repositoryReviewAssignmentDigestForTest("1"),
	}
	if err := checkpoint(badDigest); err == nil {
		t.Fatal("checkpoint accepted an invalid output digest")
	}

	firstCheckpoint := repositoryReviewAssignmentCheckpointEventForTest(
		firstEvent, validOutput, "", "", "2",
	)
	if err := checkpoint(firstCheckpoint); err != nil {
		t.Fatalf("checkpoint default model: %v", err)
	}
	if err := checkpoint(firstCheckpoint); err != nil {
		t.Fatalf("checkpoint idempotent replay: %v", err)
	}
	conflict := firstCheckpoint
	conflict.CheckpointDigest = repositoryReviewAssignmentDigestForTest("3")
	if err := checkpoint(conflict); err == nil {
		t.Fatal("checkpoint accepted a conflicting replay")
	}
	if err := dispatch(firstEvent); err == nil {
		t.Fatal("dispatch accepted an already checkpointed assignment")
	}

	for index := 1; index < len(plan.AssignmentPlans); index++ {
		event := repositoryReviewAssignmentDispatchEventForTest(plan.AssignmentPlans[index], scope)
		model, reviewerModel := "", ""
		if index == 1 {
			reviewerModel = "review-a"
		} else if index == 2 {
			model = "selected-review-model"
		}
		checkpointEvent := repositoryReviewAssignmentCheckpointEventForTest(
			event, validOutput, model, reviewerModel, fmt.Sprint(index+3),
		)
		if err := checkpoint(checkpointEvent); err != nil {
			t.Fatalf("checkpoint assignment %d: %v", index, err)
		}
	}
	state, found, err := repoaudit.NewStore(workspace).Get(plan.Repository)
	if err != nil || !found {
		t.Fatalf("load checkpointed state = (%v, %v)", found, err)
	}
	progress := repoaudit.CurrentCampaignAssignmentProgress(state, plan.CampaignID)
	if progress.Completed != 4 || progress.Pending != 0 || progress.Active != 0 {
		t.Fatalf("checkpointed progress = %#v", progress)
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

//nolint:govet // Test fixture construction intentionally reuses err in short scopes.
func repositoryReviewNativeAssignmentPlanForTest(
	t *testing.T,
) (string, ExecutionContext, map[string]any, repoaudit.Plan, []map[string]any) {
	t.Helper()
	workspace := t.TempDir()
	content := "package service\nconst ready = true\n"
	writeTestFile(t, filepath.Join(workspace, "service.go"), content)
	gitCmd(t, workspace, "init")
	gitCmd(t, workspace, "config", "user.email", "test@example.com")
	gitCmd(t, workspace, "config", "user.name", "Test User")
	gitCmd(t, workspace, "remote", "add", "origin", "git@github.com:Owner/Repo.git")
	gitCmd(t, workspace, "add", "service.go")
	gitCmd(t, workspace, "commit", "-m", "assignment fixture")
	commit := strings.TrimSpace(gitCmd(t, workspace, "rev-parse", "HEAD"))
	inventory, err := nativeCollectInventory(context.Background(), workspace, commit)
	if err != nil {
		t.Fatal(err)
	}
	inventoryHash, err := nativeStableHash(inventory)
	if err != nil {
		t.Fatal(err)
	}
	files := []map[string]any{{
		"path": "service.go", "fileHash": inventory[0].BlobHash,
		"sizeBytes": inventory[0].SizeBytes, "category": "code", "mode": inventory[0].Mode,
	}}
	campaignID := repoaudit.NewRepositoryReviewCampaignID()
	store := repoaudit.NewStore(workspace)
	if _, err := store.BeginCampaign(context.Background(), repoaudit.BeginCampaignRequest{
		Repository: "owner/repo", CampaignID: campaignID, CommitSHA: commit,
	}); err != nil {
		t.Fatal(err)
	}
	exec := ExecutionContext{
		WorkspaceDir: workspace, WorkflowRef: RepositoryBugFinderWorkflowRef,
		RunID: "wr-assignment-callbacks",
	}
	output, err := nativeRepositoryReview(context.Background(), map[string]any{
		"action": "plan", "working_directory": ".", "repository": "auto",
		"commit": commit, "inventory_hash": inventoryHash, "files": files,
		"campaign_id": campaignID, "authoritative": true, "compact_output": true,
		"resolved_reviewer_models": []any{"review-a"},
		"include_default_reviewer": false,
		"profile": map[string]any{
			"schema": RepositoryBugFinderProfileSchema, "prompt_revision": RepositoryBugFinderPromptRevision,
			"account_ref": "", "target": "all", "focus": "bugs", "scope_policy": "{}",
			"scope_plan_hash": "scope-plan", "models": []any{"review-a"},
			"model_graph_revision": "graph-v1", "effective_models": []any{"review-a"},
			"include_default_reviewer": false, "max_content_bytes": int64(1024),
		},
	}, exec)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := nativeRepositoryReviewPlan(output["plan"])
	if err != nil || len(plan.AssignmentPlans) != 4 {
		t.Fatalf("native assignment plan = %#v, %v", plan, err)
	}
	pending, err := nativeMapSlice(output["pendingFiles"])
	if err != nil || len(pending) != 1 {
		t.Fatalf("pending scope = %#v, %v", output["pendingFiles"], err)
	}
	pending[0]["contentComplete"] = true
	return workspace, exec, output, plan, pending
}

func repositoryReviewAssignmentDispatchEventForTest(
	plan repoaudit.RepositoryReviewAssignmentPlan,
	scope []map[string]any,
) ManagedAssignmentDispatchEvent {
	detached := make([]any, len(scope))
	for index, file := range scope {
		detached[index] = cloneMap(file)
	}
	return ManagedAssignmentDispatchEvent{
		AssignmentID: plan.AssignmentID, FocusID: plan.FocusID,
		Label: plan.Label, ReviewerModel: plan.Reviewer, Required: !plan.Optional,
		Scope: detached,
	}
}

func repositoryReviewAssignmentCheckpointEventForTest(
	dispatch ManagedAssignmentDispatchEvent,
	output map[string]any,
	model string,
	reviewerModel string,
	digestCharacter string,
) ManagedAssignmentCheckpointEvent {
	dispatch.Model = model
	dispatch.ReviewerModel = reviewerModel
	return ManagedAssignmentCheckpointEvent{
		ManagedAssignmentDispatchEvent: dispatch,
		Output:                         output,
		OutputDigest:                   repositoryReviewAssignmentDigestForTest("a"),
		CheckpointDigest:               repositoryReviewAssignmentDigestForTest(digestCharacter),
	}
}

func repositoryReviewAssignmentDigestForTest(character string) string {
	return "sha256:" + strings.Repeat(character, 64)
}
