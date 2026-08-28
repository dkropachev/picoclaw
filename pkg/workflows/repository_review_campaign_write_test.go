package workflows

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/sipeed/picoclaw/pkg/repoaudit"
)

func TestNativeRepositoryReviewCampaignPlanDerivesTrustedAssignmentDenominator(t *testing.T) {
	for _, test := range []struct {
		name           string
		models         []string
		includeDefault bool
		want           int
	}{
		{name: "two explicit reviewers", models: []string{"review-a", "review-b"}, want: 8},
		{name: "default chain with optional fallbacks", models: []string{"fallback-a", "fallback-b"}, includeDefault: true, want: 4},
	} {
		t.Run(test.name, func(t *testing.T) {
			workspace := t.TempDir()
			repository := filepath.Join(workspace, "repo")
			if err := os.MkdirAll(repository, 0o755); err != nil {
				t.Fatal(err)
			}
			writeTestFile(t, filepath.Join(repository, "service.go"), "package service\n")
			gitCmd(t, repository, "init")
			gitCmd(t, repository, "config", "user.email", "test@example.com")
			gitCmd(t, repository, "config", "user.name", "Test User")
			gitCmd(t, repository, "add", "service.go")
			gitCmd(t, repository, "commit", "-m", "initial")
			exec := ExecutionContext{
				WorkspaceDir: workspace, WorkflowRef: RepositoryBugFinderWorkflowRef, RunID: "campaign-plan",
			}
			inventory, _, err := RunNativeFunction(context.Background(), "git.inventory", map[string]any{
				"working_directory": repository, "target": "all",
			}, exec)
			if err != nil {
				t.Fatal(err)
			}
			campaignID := repoaudit.NewRepositoryReviewCampaignID()
			if _, beginErr := repoaudit.NewStore(workspace).BeginCampaign(
				context.Background(), repoaudit.BeginCampaignRequest{
					Repository: repository, CampaignID: campaignID,
					CommitSHA: inventory["commit"].(string), Exact: true,
				},
			); beginErr != nil {
				t.Fatal(beginErr)
			}
			planned, _, err := RunNativeFunction(context.Background(), "review.repository", map[string]any{
				"action": "plan", "working_directory": repository,
				"commit": inventory["commit"], "inventory_hash": inventory["inventoryHash"],
				"files": inventory["selectedFiles"],
				"profile": NewRepositoryBugFinderProfileHashInput(
					"account", "all", "Find bugs.", `{}`, strings.Repeat("d", 64),
					"review-a,review-b", "sha256:graph-a", test.models,
					test.includeDefault, 524288,
				),
				"authoritative": true, "campaign_id": campaignID,
				"resolved_reviewer_models": test.models,
				"include_default_reviewer": test.includeDefault,
			}, exec)
			if err != nil {
				t.Fatal(err)
			}
			plan, err := nativeRepositoryReviewPlan(planned["plan"])
			if err != nil || plan.CampaignID != campaignID || plan.RequiredAssignments != test.want {
				t.Fatalf("campaign plan=%#v err=%v", plan, err)
			}
			if _, _, driftErr := RunNativeFunction(context.Background(), "review.repository", map[string]any{
				"action": "plan", "working_directory": repository,
				"commit": inventory["commit"], "inventory_hash": inventory["inventoryHash"],
				"files": inventory["selectedFiles"],
				"profile": NewRepositoryBugFinderProfileHashInput(
					"account", "all", "Find bugs.", `{}`, strings.Repeat("d", 64),
					"review-a,review-b", "sha256:graph-b", test.models,
					test.includeDefault, 524288,
				),
				"authoritative": true, "campaign_id": campaignID,
				"resolved_reviewer_models": test.models,
				"include_default_reviewer": test.includeDefault,
			}, exec); !errors.Is(driftErr, repoaudit.ErrConflict) {
				t.Fatalf("mid-campaign model graph drift error=%v, want conflict", driftErr)
			}
		})
	}
}

func TestNativeRepositoryReviewCampaignPlanRejectsUntrustedAuthorityAndRouting(t *testing.T) {
	workspace := t.TempDir()
	repository := filepath.Join(workspace, "repo")
	if err := os.MkdirAll(repository, 0o755); err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, filepath.Join(repository, "service.go"), "package service\n")
	gitCmd(t, repository, "init")
	gitCmd(t, repository, "config", "user.email", "test@example.com")
	gitCmd(t, repository, "config", "user.name", "Test User")
	gitCmd(t, repository, "add", "service.go")
	gitCmd(t, repository, "commit", "-m", "initial")
	exec := ExecutionContext{
		WorkspaceDir: workspace, WorkflowRef: RepositoryBugFinderWorkflowRef,
		RunID: "campaign-plan-boundaries",
	}
	inventory, _, err := RunNativeFunction(context.Background(), "git.inventory", map[string]any{
		"working_directory": repository, "target": "all",
	}, exec)
	if err != nil {
		t.Fatal(err)
	}
	campaignID := repoaudit.NewRepositoryReviewCampaignID()
	args := map[string]any{
		"action": "plan", "working_directory": repository,
		"commit": inventory["commit"], "inventory_hash": inventory["inventoryHash"],
		"files": inventory["selectedFiles"], "campaign_id": campaignID,
		"profile": NewRepositoryBugFinderProfileHashInput(
			"account", "all", "Find bugs.", `{}`, strings.Repeat("d", 64),
			"review-a", "sha256:graph", []string{"review-a"}, false, 524288,
		),
		"resolved_reviewer_models": []string{"review-a"},
		"authoritative":            true,
	}
	foreignExec := exec
	foreignExec.WorkflowRef = "workflows/untrusted.yml"
	if _, _, callErr := RunNativeFunction(
		context.Background(), "review.repository", args, foreignExec,
	); callErr == nil || !strings.Contains(callErr.Error(), "campaign authority") {
		t.Fatalf("foreign workflow campaign error = %v", callErr)
	}
	invalidID := cloneMap(args)
	invalidID["campaign_id"] = "rrc_invalid!"
	if _, _, callErr := RunNativeFunction(
		context.Background(), "review.repository", invalidID, exec,
	); callErr == nil || !strings.Contains(callErr.Error(), "campaign authority") {
		t.Fatalf("invalid campaign ID error = %v", callErr)
	}
	invalidProfile := cloneMap(args)
	invalidProfile["profile"] = map[string]any{"models": 7}
	if _, _, callErr := RunNativeFunction(
		context.Background(), "review.repository", invalidProfile, exec,
	); callErr == nil || !strings.Contains(callErr.Error(), "profile model list") {
		t.Fatalf("invalid campaign profile error = %v", callErr)
	}
	emptyCohort := cloneMap(args)
	emptyCohort["resolved_reviewer_models"] = []string{}
	if _, _, callErr := RunNativeFunction(
		context.Background(), "review.repository", emptyCohort, exec,
	); callErr == nil || !strings.Contains(callErr.Error(), "reviewer count") {
		t.Fatalf("empty campaign reviewer cohort error = %v", callErr)
	}
}

func TestNativeRepositoryReviewCampaignEvidenceIncludesEveryChild(t *testing.T) {
	first := repoaudit.FileRef{
		Path: "a.go", BlobSHA: strings.Repeat("a", 40), SizeBytes: 1, Category: "code", Mode: "100644",
	}
	second := repoaudit.FileRef{
		Path: "b.go", BlobSHA: strings.Repeat("b", 40), SizeBytes: 2, Category: "code", Mode: "100644",
	}
	scope := nativeRepositoryReviewFileMaps([]repoaudit.FileRef{second, first})
	for _, file := range scope {
		file["contentComplete"] = true
	}
	structured := map[string]any{
		"summary": "reviewed", "reviewedFiles": []any{"b.go", "a.go"},
		"findings": []any{}, "residualRisks": []any{},
	}
	plan := repoaudit.Plan{
		CampaignID: repoaudit.NewRepositoryReviewCampaignID(), RequiredAssignments: 2,
		PendingFiles: []repoaudit.FileRef{first, second},
	}
	evidence, err := nativeRepositoryReviewRecordEvidence(map[string]any{
		"managed_children": []map[string]any{
			{
				"index": 1, "required": true, "valid": true, "scope": scope,
				"model": map[string]any{"selected": "review-a"}, "structured": structured,
			},
			{
				"index": 2, "required": true, "valid": false, "scope": scope,
				"model": map[string]any{"selected": "review-b"}, "run_error": "provider failed",
			},
		},
	}, plan)
	if err != nil || len(evidence.ReviewEvidence) != 2 ||
		!evidence.ReviewEvidence[0].Successful || evidence.ReviewEvidence[1].Successful ||
		evidence.ReviewEvidence[0].AssignmentID != "legacy-managed-child-000001" ||
		evidence.ReviewEvidence[1].AssignmentID != "legacy-managed-child-000002" ||
		!reflect.DeepEqual(evidence.InspectedFiles, []repoaudit.FileRef{first, second}) ||
		len(evidence.CompletedFiles) != 0 {
		t.Fatalf("campaign evidence=%#v err=%v", evidence, err)
	}
}

func TestNativeRepositoryReviewCampaignTransientUnavailableEvidenceRemainsPending(t *testing.T) {
	file := repoaudit.FileRef{
		Path: "later.go", BlobSHA: strings.Repeat("c", 40), SizeBytes: 3,
		Category: "code", Mode: "100644",
	}
	plan := repoaudit.Plan{
		CampaignID: repoaudit.NewRepositoryReviewCampaignID(), RequiredAssignments: 4,
		PendingFiles: []repoaudit.FileRef{file},
	}
	evidence, err := nativeRepositoryReviewRecordEvidence(map[string]any{
		"managed_children":  []map[string]any{},
		"unavailable_files": nativeRepositoryReviewFileMaps([]repoaudit.FileRef{file}),
	}, plan)
	if err != nil || evidence.ReviewEvidence == nil || evidence.InspectedFiles == nil ||
		evidence.CompletedFiles == nil || len(evidence.ReviewEvidence) != 4 ||
		len(evidence.InspectedFiles) != 0 || len(evidence.CompletedFiles) != 0 {
		t.Fatalf("transient evidence=%#v err=%v", evidence, err)
	}
	for _, child := range evidence.ReviewEvidence {
		if !child.Required || child.Successful ||
			!strings.HasPrefix(child.AssignmentID, "legacy-managed-child-") ||
			!reflect.DeepEqual(child.ScopeFiles, []repoaudit.FileRef{file}) {
			t.Fatalf("transient child=%#v", child)
		}
	}
}

func TestNativeRepositoryReviewCampaignEvidenceSortsMultiFileResults(t *testing.T) {
	file := func(pathValue, digest string) repoaudit.FileRef {
		return repoaudit.FileRef{
			Path: pathValue, BlobSHA: strings.Repeat(digest, 40), SizeBytes: 1,
			Category: "code", Mode: "100644",
		}
	}
	first, second := file("a.go", "a"), file("b.go", "b")
	third, fourth := file("c.go", "c"), file("d.go", "d")
	scope := nativeRepositoryReviewFileMaps([]repoaudit.FileRef{second, first})
	for index := range scope {
		scope[index]["contentComplete"] = true
	}
	evidence, err := nativeRepositoryReviewRecordEvidence(map[string]any{
		"managed_children": []map[string]any{{
			"index": 1, "required": true, "valid": true, "scope": scope,
			"model": map[string]any{"selected": "review-a"},
			"structured": map[string]any{
				"summary": "reviewed", "reviewedFiles": []any{"b.go", "a.go"},
				"findings": []any{}, "residualRisks": []any{},
			},
		}},
		"unavailable_files": nativeRepositoryReviewFileMaps([]repoaudit.FileRef{fourth, third}),
	}, repoaudit.Plan{
		CampaignID: repoaudit.NewRepositoryReviewCampaignID(), RequiredAssignments: 1,
		PendingFiles: []repoaudit.FileRef{first, second, third, fourth},
	})
	if err != nil ||
		!reflect.DeepEqual(evidence.InspectedFiles, []repoaudit.FileRef{first, second}) ||
		!reflect.DeepEqual(evidence.CompletedFiles, []repoaudit.FileRef{first, second}) ||
		len(evidence.ReviewEvidence) != 3 ||
		evidence.ReviewEvidence[1].ScopeFiles[0].Path != "c.go" ||
		evidence.ReviewEvidence[2].ScopeFiles[0].Path != "d.go" {
		t.Fatalf("sorted campaign evidence=%#v err=%v", evidence, err)
	}
}

func TestNativeRepositoryReviewCampaignRejectsForgedChildRuntimeIdentity(t *testing.T) {
	file := repoaudit.FileRef{Path: "a.go", BlobSHA: strings.Repeat("a", 40), SizeBytes: 1}
	for name, child := range map[string]map[string]any{
		"missing index":    {"required": true, "scope": nativeRepositoryReviewFileMaps([]repoaudit.FileRef{file})},
		"wrong index":      {"index": 2, "required": true, "scope": nativeRepositoryReviewFileMaps([]repoaudit.FileRef{file})},
		"missing required": {"index": 1, "scope": nativeRepositoryReviewFileMaps([]repoaudit.FileRef{file})},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := nativeRepositoryReviewRecordEvidence(map[string]any{
				"managed_children": []map[string]any{child},
			}, repoaudit.Plan{
				CampaignID: repoaudit.NewRepositoryReviewCampaignID(), RequiredAssignments: 1,
				PendingFiles: []repoaudit.FileRef{file},
			}); err == nil {
				t.Fatal("forged campaign child identity was accepted")
			}
		})
	}
}

func TestNativeRepositoryReviewCampaignEvidenceHandlesPartialAndMalformedChildren(t *testing.T) {
	first := repoaudit.FileRef{
		Path: "a.go", BlobSHA: strings.Repeat("a", 40), SizeBytes: 1,
		Category: "code", Mode: "100644",
	}
	second := repoaudit.FileRef{
		Path: "b.go", BlobSHA: strings.Repeat("b", 40), SizeBytes: 2,
		Category: "code", Mode: "100644",
	}
	plan := repoaudit.Plan{
		CampaignID: repoaudit.NewRepositoryReviewCampaignID(), RequiredAssignments: 1,
		PendingFiles: []repoaudit.FileRef{first, second},
	}
	completeScope := nativeRepositoryReviewFileMaps([]repoaudit.FileRef{first, second})
	completeScope[0]["contentComplete"] = true
	completeScope[1]["contentComplete"] = false
	validOutput := map[string]any{
		"summary": "reviewed", "reviewedFiles": []any{"a.go"},
		"findings": []any{}, "residualRisks": []any{},
	}

	for name, args := range map[string]map[string]any{
		"malformed children": {"managed_children": "not-an-array"},
		"empty scope": {"managed_children": []map[string]any{{
			"index": 1, "required": true, "scope": []map[string]any{},
		}}},
		"duplicate scope": {"managed_children": []map[string]any{{
			"index": 1, "required": true,
			"scope": nativeRepositoryReviewFileMaps([]repoaudit.FileRef{first, first}),
		}}},
		"malformed unavailable list": {"managed_children": []map[string]any{}, "unavailable_files": "bad"},
		"malformed unavailable file": {"managed_children": []map[string]any{}, "unavailable_files": []map[string]any{{
			"path": "bad.go", "fileHash": "not-a-hash", "sizeBytes": -1,
		}}},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := nativeRepositoryReviewRecordEvidence(args, plan); err == nil {
				t.Fatal("malformed campaign evidence was accepted")
			}
		})
	}

	invalidAcknowledgement := map[string]any{
		"index": 1, "required": true, "valid": true, "scope": completeScope,
		"model": map[string]any{"selected": "review-a"},
		"structured": map[string]any{
			"summary": "reviewed", "reviewedFiles": []any{"missing.go"},
			"findings": []any{}, "residualRisks": []any{},
		},
	}
	evidence, err := nativeRepositoryReviewRecordEvidence(map[string]any{
		"managed_children": []map[string]any{invalidAcknowledgement},
	}, plan)
	if err != nil || len(evidence.ReviewEvidence) != 1 ||
		evidence.ReviewEvidence[0].Successful || len(evidence.Observations) != 0 ||
		len(evidence.InspectedFiles) != 0 || len(evidence.CompletedFiles) != 0 {
		t.Fatalf("downgraded acknowledgement=%#v err=%v", evidence, err)
	}

	for name, child := range map[string]map[string]any{
		"missing model": {
			"index": 1, "required": true, "valid": true, "scope": completeScope,
			"structured": validOutput,
		},
		"invalid observation": {
			"index": 1, "required": true, "valid": true, "scope": completeScope,
			"model": map[string]any{"selected": "review-a"},
			"structured": map[string]any{
				"summary": "reviewed", "reviewedFiles": []any{"a.go"},
				"findings": []any{}, "residualRisks": []any{}, "unexpected": true,
			},
		},
	} {
		t.Run(name, func(t *testing.T) {
			if malformedEvidence, malformedErr := nativeRepositoryReviewRecordEvidence(map[string]any{
				"managed_children": []map[string]any{child},
			}, plan); malformedErr == nil {
				t.Fatalf("malformed valid child accepted: %#v", malformedEvidence)
			}
		})
	}

	evidence, err = nativeRepositoryReviewRecordEvidence(map[string]any{
		"managed_children": []map[string]any{{
			"index": 1, "required": true, "valid": true, "scope": completeScope,
			"model": map[string]any{"default": "default-reviewer"}, "structured": validOutput,
		}},
	}, plan)
	if err != nil || len(evidence.Observations) != 1 ||
		evidence.Observations[0].Model != "default-reviewer" ||
		!reflect.DeepEqual(evidence.InspectedFiles, []repoaudit.FileRef{first}) ||
		!reflect.DeepEqual(evidence.CompletedFiles, []repoaudit.FileRef{first}) {
		t.Fatalf("partial successful evidence=%#v err=%v", evidence, err)
	}
}

func TestNativeRepositoryReviewCampaignEvidenceBoundaryHelpers(t *testing.T) {
	for _, test := range []struct {
		value any
		want  bool
	}{
		{value: int64(1), want: true},
		{value: float64(1), want: true},
		{value: json.Number("1"), want: true},
		{value: int64(-1)},
		{value: float64(1.5)},
		{value: json.Number("1.5")},
		{value: uint(1)},
	} {
		index, ok := nativeRepositoryReviewChildIndex(test.value)
		if ok != test.want || test.want && index != 1 {
			t.Fatalf("child index %#v = (%d, %v), want valid=%v", test.value, index, ok, test.want)
		}
	}
}

func TestNativeRepositoryReviewUnavailableScopeFilesKeepsOnlyExactAggregateLimits(t *testing.T) {
	if files := nativeRepositoryReviewUnavailableScopeFiles("invalid"); files != nil {
		t.Fatalf("invalid scope files=%#v", files)
	}
	first := repoaudit.FileRef{Path: "a.go", BlobSHA: strings.Repeat("a", 40), SizeBytes: 1}
	second := repoaudit.FileRef{Path: "b.go", BlobSHA: strings.Repeat("b", 40), SizeBytes: 2}
	firstMap := nativeRepositoryReviewFileMaps([]repoaudit.FileRef{first})[0]
	firstMap["contentUnavailable"] = " aggregate_limit "
	secondMap := nativeRepositoryReviewFileMaps([]repoaudit.FileRef{second})[0]
	secondMap["contentUnavailable"] = "aggregate_limit"
	files := nativeRepositoryReviewUnavailableScopeFiles([]any{
		"not-a-file",
		map[string]any{"path": "binary.bin", "contentUnavailable": "binary"},
		map[string]any{"path": "bad.go", "contentUnavailable": "aggregate_limit"},
		secondMap,
		firstMap,
	})
	if len(files) != 2 || files[0]["path"] != "a.go" || files[1]["path"] != "b.go" ||
		files[0]["contentUnavailable"] != nil || files[1]["contentUnavailable"] != nil {
		t.Fatalf("aggregate-limit files=%#v", files)
	}
}
