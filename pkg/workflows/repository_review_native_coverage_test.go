package workflows

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sipeed/picoclaw/pkg/repoaudit"
)

func TestNativeRepositoryReviewCampaignPlanUsesCanonicalAssignmentAuthority(t *testing.T) {
	for _, test := range []struct {
		name           string
		models         []string
		includeDefault bool
		wantRequired   int
	}{
		{name: "two explicit reviewers", models: []string{"review-a", "review-b"}, wantRequired: 8},
		{name: "default with optional fallbacks", models: []string{"fallback-a", "fallback-b"}, includeDefault: true, wantRequired: 4},
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
				WorkspaceDir: workspace, WorkflowRef: RepositoryBugFinderWorkflowRef,
				RunID: "campaign-plan",
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
					DeduplicationSnapshot: &repoaudit.RepositoryReviewDeduplicationSnapshot{
						ReviewerModel: "review-a", DeduplicationModel: "review-a",
						SimilarityThreshold: repoaudit.DeduplicationDefaultThreshold,
						CandidateLimit:      repoaudit.DeduplicationDefaultCandidateLimit,
					},
				},
			); beginErr != nil {
				t.Fatal(beginErr)
			}
			args := map[string]any{
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
				"targetIsDefault":          true,
			}
			if test.name == "two explicit reviewers" {
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
				emptyCohort := cloneMap(args)
				emptyCohort["resolved_reviewer_models"] = []string{}
				if _, _, callErr := RunNativeFunction(
					context.Background(), "review.repository", emptyCohort, exec,
				); callErr == nil || !strings.Contains(callErr.Error(), "reviewer count") {
					t.Fatalf("empty campaign reviewer cohort error = %v", callErr)
				}
			}
			planned, _, err := RunNativeFunction(
				context.Background(), "review.repository", args, exec,
			)
			if err != nil {
				t.Fatal(err)
			}
			plan, err := nativeRepositoryReviewPlan(planned["plan"])
			if err != nil || plan.CampaignID != campaignID ||
				plan.RequiredAssignments != test.wantRequired {
				t.Fatalf("campaign plan=%#v err=%v", plan, err)
			}
			drift := cloneMap(args)
			drift["profile"] = NewRepositoryBugFinderProfileHashInput(
				"account", "all", "Find bugs.", `{}`, strings.Repeat("d", 64),
				"review-a,review-b", "sha256:graph-b", test.models,
				test.includeDefault, 524288,
			)
			if _, _, driftErr := RunNativeFunction(
				context.Background(), "review.repository", drift, exec,
			); !errors.Is(driftErr, repoaudit.ErrConflict) {
				t.Fatalf("mid-campaign profile drift error=%v, want conflict", driftErr)
			}
		})
	}
}

func TestHydrateImmutableGitScopeCoversBoundedEvidenceOutcomes(t *testing.T) {
	workspace := t.TempDir()
	repo := filepath.Join(workspace, "repo")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	contents := map[string]string{
		"plain.go":   "package plain\n",
		"second.go":  "package second\n",
		"binary.dat": "visible\x00binary",
		"escaped.go": strings.Repeat(`"\\`, 8),
	}
	for name, content := range contents {
		writeTestFile(t, filepath.Join(repo, name), content)
	}
	gitCmd(t, repo, "init")
	gitCmd(t, repo, "config", "user.email", "test@example.com")
	gitCmd(t, repo, "config", "user.name", "Test User")
	gitCmd(t, repo, "add", ".")
	gitCmd(t, repo, "commit", "-m", "immutable fixtures")

	inventory, inventoryErr := nativeCollectInventory(context.Background(), repo, "HEAD")
	if inventoryErr != nil {
		t.Fatal(inventoryErr)
	}
	byPath := make(map[string]nativeGitFile, len(inventory))
	for _, file := range inventory {
		byPath[file.Path] = file
	}
	exec := ExecutionContext{WorkspaceDir: workspace}
	ref := func(name string) map[string]any {
		file := byPath[name]
		return map[string]any{
			"path": name, "fileHash": file.BlobHash, "sizeBytes": file.SizeBytes,
			"category": "code", "source": map[string]any{"workspacePath": repo},
		}
	}

	if _, err := hydrateImmutableGitScope(
		context.Background(), []any{"not-an-object"}, nil, exec,
	); err == nil || !strings.Contains(err.Error(), "must be an object") {
		t.Fatalf("non-object scope error = %v", err)
	}
	binaryCategory, hydrateErr := hydrateImmutableGitScope(context.Background(), []any{map[string]any{
		"path": "image.png", "category": "binary",
	}}, nil, exec)
	if hydrateErr != nil {
		t.Fatal(hydrateErr)
	}
	binaryCategoryFile := binaryCategory.([]any)[0].(map[string]any)
	if binaryCategoryFile["contentComplete"] != false || binaryCategoryFile["contentUnavailable"] != "binary" {
		t.Fatalf("binary category hydration = %#v", binaryCategoryFile)
	}
	if _, err := hydrateImmutableGitScope(context.Background(), []any{map[string]any{
		"path": "missing.go", "category": "code", "sizeBytes": 1,
	}}, nil, exec); err == nil || !strings.Contains(err.Error(), "no workspace source") {
		t.Fatalf("missing source error = %v", err)
	}
	outside := t.TempDir()
	if _, err := hydrateImmutableGitScope(context.Background(), []any{map[string]any{
		"path": "escape.go", "category": "code", "sizeBytes": 1,
		"source": map[string]any{"workspacePath": outside},
	}}, nil, exec); err == nil || !strings.Contains(err.Error(), "stay inside") {
		t.Fatalf("escaping source error = %v", err)
	}
	negative := ref("plain.go")
	negative["sizeBytes"] = -1
	if _, err := hydrateImmutableGitScope(context.Background(), []any{negative}, nil, exec); err == nil ||
		!strings.Contains(err.Error(), "invalid size") {
		t.Fatalf("negative size error = %v", err)
	}

	tooLarge := ref("plain.go")
	tooLarge["sizeBytes"] = int64(32)
	result, hydrateErr := hydrateImmutableGitScope(
		context.Background(), []any{tooLarge}, map[string]any{"max_content_bytes": 8}, exec,
	)
	if hydrateErr != nil {
		t.Fatal(hydrateErr)
	}
	if got := result.([]any)[0].(map[string]any)["contentUnavailable"]; got != "file_too_large" {
		t.Fatalf("oversized metadata outcome = %q", got)
	}

	plain := ref("plain.go")
	second := ref("second.go")
	aggregateLimit := int(byPath["plain.go"].SizeBytes)
	result, hydrateErr = hydrateImmutableGitScope(
		context.Background(), []any{plain, second},
		map[string]any{"max_content_bytes": 128, "max_total_content_bytes": aggregateLimit}, exec,
	)
	if hydrateErr != nil {
		t.Fatal(hydrateErr)
	}
	aggregateFiles := result.([]any)
	if aggregateFiles[0].(map[string]any)["contentComplete"] != true ||
		aggregateFiles[1].(map[string]any)["contentUnavailable"] != "aggregate_limit" {
		t.Fatalf("aggregate hydration = %#v", aggregateFiles)
	}

	unreadable := ref("plain.go")
	unreadable["fileHash"] = strings.Repeat("f", 40)
	if _, err := hydrateImmutableGitScope(
		context.Background(), []any{unreadable}, map[string]any{"max_content_bytes": 128}, exec,
	); err == nil || !strings.Contains(err.Error(), "read immutable blob") {
		t.Fatalf("unreadable blob error = %v", err)
	}
	mismatched := ref("plain.go")
	mismatched["sizeBytes"] = byPath["plain.go"].SizeBytes + 1
	if _, err := hydrateImmutableGitScope(
		context.Background(), []any{mismatched}, map[string]any{"max_content_bytes": 128}, exec,
	); err == nil || !strings.Contains(err.Error(), "size mismatch") {
		t.Fatalf("blob size mismatch error = %v", err)
	}

	result, hydrateErr = hydrateImmutableGitScope(
		context.Background(), []any{ref("binary.dat")}, map[string]any{"max_content_bytes": 128}, exec,
	)
	if hydrateErr != nil || result.([]any)[0].(map[string]any)["contentUnavailable"] != "binary" {
		t.Fatalf("binary blob hydration = (%#v, %v)", result, hydrateErr)
	}
	escaped := ref("escaped.go")
	escapedLimit := int(byPath["escaped.go"].SizeBytes)
	result, hydrateErr = hydrateImmutableGitScope(
		context.Background(), []any{escaped}, map[string]any{"max_content_bytes": escapedLimit}, exec,
	)
	if hydrateErr != nil || result.([]any)[0].(map[string]any)["contentUnavailable"] != "file_too_large" {
		t.Fatalf("prompt-escaped blob hydration = (%#v, %v)", result, hydrateErr)
	}

	wrapper := map[string]any{"batch": "one", "items": []any{ref("plain.go")}}
	wrapped, hydrateErr := hydrateImmutableGitScope(
		context.Background(), wrapper, map[string]any{"max_content_bytes": 128}, exec,
	)
	if hydrateErr != nil || wrapped.(map[string]any)["batch"] != "one" ||
		len(wrapped.(map[string]any)["items"].([]any)) != 1 {
		t.Fatalf("wrapped hydration = (%#v, %v)", wrapped, hydrateErr)
	}
	empty, hydrateErr := hydrateImmutableGitScope(context.Background(), []any{}, nil, exec)
	if hydrateErr != nil || len(empty.([]any)) != 0 {
		t.Fatalf("empty any-slice hydration = (%#v, %v)", empty, hydrateErr)
	}
	if nativeReviewText("valid\x00text") || nativeReviewText(string([]byte{0xff})) {
		t.Fatal("NUL or invalid UTF-8 review text was accepted")
	}
}

func TestRepositoryReviewNativeCloseoutBranches(t *testing.T) {
	workspace := t.TempDir()
	writeTestFile(t, filepath.Join(workspace, "a.go"), "package review\n")
	writeTestFile(t, filepath.Join(workspace, "b.go"), "package review\n")
	gitCmd(t, workspace, "init")
	gitCmd(t, workspace, "config", "user.email", "test@example.com")
	gitCmd(t, workspace, "config", "user.name", "Test User")
	gitCmd(t, workspace, "remote", "add", "origin", "git@github.com:Owner/Repo.git")
	gitCmd(t, workspace, "add", ".")
	gitCmd(t, workspace, "commit", "-m", "review fixture")
	exec := ExecutionContext{
		WorkspaceDir: workspace, WorkflowRef: RepositoryBugFinderWorkflowRef, RunID: "closeout-review",
	}
	inventory, inventoryErr := nativeCollectInventory(context.Background(), workspace, "HEAD")
	if inventoryErr != nil {
		t.Fatal(inventoryErr)
	}
	inventoryHash, hashErr := nativeStableHash(inventory)
	if hashErr != nil {
		t.Fatal(hashErr)
	}
	commit, commitErr := nativeResolveCommit(context.Background(), workspace, "HEAD")
	if commitErr != nil {
		t.Fatal(commitErr)
	}
	campaignID := repoaudit.NewRepositoryReviewCampaignID()
	reviewStore := repoaudit.NewSQLiteStore(workspace)
	if _, beginErr := reviewStore.BeginCampaign(context.Background(), repoaudit.BeginCampaignRequest{
		Repository: "owner/repo", CampaignID: campaignID, CommitSHA: commit,
		DeduplicationSnapshot: &repoaudit.RepositoryReviewDeduplicationSnapshot{
			ReviewerModel: "review-a", DeduplicationModel: "review-a",
			SimilarityThreshold: repoaudit.DeduplicationDefaultThreshold,
			CandidateLimit:      repoaudit.DeduplicationDefaultCandidateLimit,
		},
	}); beginErr != nil {
		t.Fatal(beginErr)
	}
	files := make([]map[string]any, 0, len(inventory))
	for _, file := range inventory {
		files = append(files, map[string]any{
			"path": file.Path, "fileHash": file.BlobHash, "sizeBytes": file.SizeBytes,
			"category": "code", "mode": file.Mode,
		})
	}
	basePlan := map[string]any{
		"action": "plan", "working_directory": ".", "commit": "HEAD",
		"inventory_hash": inventoryHash, "files": files,
		"campaign_id": campaignID, "authoritative": true,
		"profile": NewRepositoryBugFinderProfileHashInput(
			"account", "all", "find bugs", `{}`, inventoryHash,
			"review-a,review-b", "graph-v1", []string{"review-a", "review-b"},
			true, 4096,
		),
		"resolved_reviewer_models": []any{"review-a", "review-b"},
	}
	badHash := cloneMap(basePlan)
	badHash["inventory_hash"] = "wrong"
	if _, err := nativeRepositoryReview(context.Background(), badHash, exec); err == nil ||
		!strings.Contains(err.Error(), "inventory hash") {
		t.Fatalf("plan binding error = %v", err)
	}
	badProfile := cloneMap(basePlan)
	badProfile["profile"] = make(chan int)
	if _, err := nativeRepositoryReview(context.Background(), badProfile, exec); err == nil ||
		!strings.Contains(err.Error(), "unsupported type") {
		t.Fatalf("profile hashing error = %v", err)
	}
	badIdentity := cloneMap(basePlan)
	badIdentity["repository"] = "different/repo"
	if _, err := nativeRepositoryReview(context.Background(), badIdentity, exec); err == nil ||
		!strings.Contains(err.Error(), "does not match") {
		t.Fatalf("repository identity error = %v", err)
	}

	planArgs := cloneMap(basePlan)
	planArgs["repository"] = "auto"
	planArgs["include_default_reviewer"] = true
	planArgs["resolved_reviewer_models"] = []any{"review-a", "review-b"}
	planArgs["max_files"] = 128
	planned, planErr := nativeRepositoryReview(context.Background(), planArgs, exec)
	if planErr != nil || planned["includeDefaultReviewer"] != true || planned["maxFiles"].(int) != 128 {
		t.Fatalf("bounded default-reviewer plan = (%#v, %v)", planned, planErr)
	}

	if _, err := nativeRepositoryReview(context.Background(), map[string]any{
		"action": "freeze", "files": []any{map[string]any{"path": "image.png", "category": "binary"}},
	}, ExecutionContext{WorkspaceDir: workspace}); err == nil || !strings.Contains(err.Error(), "run identity") {
		t.Fatalf("freeze identity error = %v", err)
	}
	if _, err := nativeRepositoryReview(context.Background(), map[string]any{
		"action": "freeze", "files": []any{}, "copies": 3,
	}, exec); err == nil || !strings.Contains(err.Error(), "copies must be one or two") {
		t.Fatalf("freeze copy-count error = %v", err)
	}
	catalog, catalogErr := RepositoryBugFinderAssignmentCatalog(
		[]string{"review-a"}, false, RepositoryBugFinderPromptRevision,
		"sha256:"+strings.Repeat("a", 64),
	)
	if catalogErr != nil {
		t.Fatal(catalogErr)
	}
	if _, err := nativeRepositoryReview(context.Background(), map[string]any{
		"action": "begin", "plan": repoaudit.Plan{ID: "rpl_invalid", AssignmentCatalog: catalog},
		"files": "invalid",
	}, exec); err == nil || !strings.Contains(err.Error(), "reviewable repository files") {
		t.Fatalf("begin file-shape error = %v", err)
	}
	if _, err := nativeRepositoryReview(context.Background(), map[string]any{
		"action": "record", "plan": repoaudit.Plan{},
		"review": map[string]any{"summary": "empty", "reviewedFiles": []any{}, "findings": []any{}},
		"scope":  []map[string]any{},
	}, exec); err == nil {
		t.Fatal("record accepted a plan without durable identity")
	}
	if _, err := nativeRepositoryReview(context.Background(), map[string]any{
		"action": "result", "plan": repoaudit.Plan{Authoritative: true},
	}, exec); err == nil {
		t.Fatal("result finalized an authoritative plan without durable identity")
	}

	if files := nativeRepositoryReviewUnsupportedFiles([]map[string]any{{"scope": "bad"}}); len(files) != 0 {
		t.Fatalf("malformed child unsupported scope = %#v", files)
	}
	if files := nativeRepositoryReviewUnsupportedScopeFiles([]map[string]any{{
		"path": "later.go", "fileHash": "a", "sizeBytes": 1, "contentUnavailable": "aggregate_limit",
	}}); len(files) != 0 {
		t.Fatalf("transient unsupported scope = %#v", files)
	}
	if identity, err := nativeRepositoryReviewIdentity(
		context.Background(), map[string]any{"repository": "auto"}, exec,
	); err != nil || identity != "owner/repo" {
		t.Fatalf("auto repository identity = (%q, %v)", identity, err)
	}
	if got := nativeRepositorySourceIdentity(
		"relative/mirror.git",
		workspace,
	); got != filepath.Join(
		workspace,
		"relative/mirror.git",
	) {
		t.Fatalf("relative repository identity = %q", got)
	}
	workspaceMap := (nativeGitWorkspaceRef{
		ID: "workspace", RepoID: "owner/repo", RemoteURL: "remote", UpstreamURL: "upstream",
		Ref: "main", Path: workspace,
	}).Map()
	if workspaceMap["upstream_url"] != "upstream" || len(workspaceMap) != 6 {
		t.Fatalf("workspace map = %#v", workspaceMap)
	}
}
