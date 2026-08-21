package workflows

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/sipeed/picoclaw/pkg/repoaudit"
)

func TestNativeRepositoryReviewDoesNotCheckpointFileWithFailedChallenge(t *testing.T) {
	workspace := t.TempDir()
	if err := os.MkdirAll(filepath.Join(workspace, "pkg"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, filepath.Join(workspace, "pkg", "service.go"), strings.Repeat("a", 120))
	gitCmd(t, workspace, "init")
	gitCmd(t, workspace, "config", "user.email", "test@example.com")
	gitCmd(t, workspace, "config", "user.name", "Test User")
	gitCmd(t, workspace, "add", "pkg/service.go")
	gitCmd(t, workspace, "commit", "-m", "initial")
	exec := ExecutionContext{WorkspaceDir: workspace, WorkflowRef: RepositoryBugFinderWorkflowRef, RunID: "native-run"}
	inventory, handled, err := RunNativeFunction(context.Background(), "git.inventory", map[string]any{
		"working_directory": ".", "target": "all",
	}, exec)
	if err != nil || !handled {
		t.Fatalf("inventory handled=%v err=%v", handled, err)
	}
	file := inventory["selectedFiles"].([]map[string]any)[0]
	planned, handled, err := RunNativeFunction(context.Background(), "review.repository", map[string]any{
		"action": "plan", "working_directory": ".", "commit": inventory["commit"],
		"inventory_hash": inventory["inventoryHash"], "files": []map[string]any{file},
		"profile": map[string]any{"schema": "test-v1"},
	}, exec)
	if err != nil || !handled {
		t.Fatalf("plan handled=%v err=%v", handled, err)
	}
	validated := map[string]any{
		"summary": "found", "reviewedFiles": []any{"pkg/service.go"}, "findings": []any{map[string]any{
			"severity": "high", "title": "Lost update", "symbol": "Save", "file": "pkg/service.go",
			"message": "A writer overwrites state.", "evidence": "No version fence.",
			"impact": "Data is lost.", "recommendation": "Use CAS.",
			"validation": map[string]any{"status": "confirmed", "summary": "Reproduced", "checks": []any{"race test"}},
		}},
	}
	children := []map[string]any{
		{
			"label": "correctness", "valid": true, "scope": []map[string]any{file},
			"model": map[string]any{"selected": "review-a"}, "structured": validated, "text": "validated",
		},
		{
			"label": "security challenge", "valid": false, "scope": []map[string]any{file},
			"model": map[string]any{"selected": "review-b"}, "run_error": "security violation",
		},
	}
	recorded, handled, err := RunNativeFunction(context.Background(), "review.repository", map[string]any{
		"action": "record", "plan": planned["plan"], "managed_children": children,
	}, exec)
	if err != nil || !handled {
		t.Fatalf("record handled=%v err=%v output=%#v", handled, err, recorded)
	}
	run := recorded["run"].(map[string]any)
	if run["reviewed_files"] != float64(0) || run["unreviewed_files"] != float64(1) {
		t.Fatalf("run coverage=%#v, want failed challenge to keep file pending", run)
	}
	state, found, err := repoaudit.NewStore(workspace).Get(workspace)
	if err != nil || !found {
		t.Fatalf("state found=%v err=%v", found, err)
	}
	if len(state.Files) != 0 || len(state.Findings) != 1 {
		t.Fatalf(
			"state files=%#v findings=%#v; partial finding should persist without checkpoint",
			state.Files,
			state.Findings,
		)
	}
	next, err := repoaudit.NewStore(workspace).Plan(
		context.Background(), workspace, "commit-b", "inventory-b", []repoaudit.FileRef{{
			Path: "pkg/service.go", BlobSHA: file["fileHash"].(string), SizeBytes: 120,
			Category: "code", Mode: "100644",
		}},
		false,
	)
	if err != nil || len(next.PendingFiles) != 1 {
		t.Fatalf("next plan=%#v err=%v, want file retried", next, err)
	}
}

func TestNativeRepositoryReviewOptionalDefaultFallbackReviewerDoesNotBlockCoverage(t *testing.T) {
	file := repoaudit.FileRef{
		Path: "service.go", BlobSHA: strings.Repeat("a", 40), SizeBytes: 10,
		Category: "code", Mode: "100644",
	}
	plan := repoaudit.Plan{PendingFiles: []repoaudit.FileRef{file}}
	scope := nativeRepositoryReviewFileMaps([]repoaudit.FileRef{file})
	observations, completed, err := nativeRepositoryReviewObservations(map[string]any{
		"managed_children": []map[string]any{
			{
				"required": true, "valid": true, "scope": scope,
				"model": map[string]any{"selected": "primary"},
				"structured": map[string]any{
					"summary": "reviewed", "reviewedFiles": []any{"service.go"}, "findings": []any{},
				},
			},
			{
				"required": false, "valid": false, "scope": scope,
				"model":     map[string]any{"selected": "fallback"},
				"run_error": "security violation",
			},
		},
	}, plan)
	if err != nil || len(observations) != 1 || !reflect.DeepEqual(completed, []repoaudit.FileRef{file}) {
		t.Fatalf("optional reviewer coverage observations=%#v completed=%#v err=%v", observations, completed, err)
	}
}

func TestNativeRepositoryReviewDerivesPublishIdentityFromActualGitOrigin(t *testing.T) {
	workspace := t.TempDir()
	writeTestFile(t, filepath.Join(workspace, "service.go"), "package service\n")
	gitCmd(t, workspace, "init")
	gitCmd(t, workspace, "config", "user.email", "test@example.com")
	gitCmd(t, workspace, "config", "user.name", "Test User")
	gitCmd(t, workspace, "remote", "add", "origin", "git@GitHub.com:Owner/Repo.git")
	gitCmd(t, workspace, "add", "service.go")
	gitCmd(t, workspace, "commit", "-m", "initial")
	exec := ExecutionContext{WorkspaceDir: workspace, RunID: "identity-run"}
	inventory, _, err := RunNativeFunction(context.Background(), "git.inventory", map[string]any{
		"working_directory": ".", "target": "all",
	}, exec)
	if err != nil {
		t.Fatal(err)
	}
	planned, _, err := RunNativeFunction(context.Background(), "review.repository", map[string]any{
		"action": "plan", "working_directory": ".", "commit": inventory["commit"],
		"inventory_hash": inventory["inventoryHash"], "files": inventory["selectedFiles"],
	}, exec)
	if err != nil {
		t.Fatal(err)
	}
	plan := planned["plan"].(map[string]any)
	if plan["repository"] != "owner/repo" {
		t.Fatalf("repository identity=%#v, want owner/repo", plan["repository"])
	}
}

func TestNativeRepositoryReviewUsesPreservedGitHubOriginForLocalSourceClone(t *testing.T) {
	workspace := t.TempDir()
	source := filepath.Join(workspace, "source")
	clone := filepath.Join(workspace, "clone")
	if err := os.MkdirAll(source, 0o755); err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, filepath.Join(source, "service.go"), "package service\n")
	gitCmd(t, source, "init")
	gitCmd(t, source, "config", "user.email", "test@example.com")
	gitCmd(t, source, "config", "user.name", "Test User")
	gitCmd(t, source, "remote", "add", "origin", "git@github.com:Owner/Repo.git")
	gitCmd(t, source, "add", "service.go")
	gitCmd(t, source, "commit", "-m", "initial")
	gitCmd(t, workspace, "clone", source, clone)
	gitCmd(t, clone, "remote", "add", "picoclaw-upstream", "git@github.com:Owner/Repo.git")

	identity, err := nativeRepositoryReviewIdentity(context.Background(), map[string]any{
		"workspace": map[string]any{
			"path": clone, "remote_url": source,
			"upstream_url": "git@github.com:Owner/Repo.git",
		},
	}, ExecutionContext{WorkspaceDir: workspace})
	if err != nil || identity != "owner/repo" {
		t.Fatalf("local clone publish identity=%q err=%v", identity, err)
	}
}

func TestNativeRepositoryReviewRejectsMalformedActionsAndEvidence(t *testing.T) {
	exec := ExecutionContext{
		WorkspaceDir: t.TempDir(), WorkflowRef: RepositoryBugFinderWorkflowRef, RunID: "native-errors",
	}
	for _, test := range []struct {
		name string
		args map[string]any
		want string
	}{
		{name: "unknown action", args: map[string]any{"action": "unknown"}, want: "unsupported"},
		{name: "malformed plan files", args: map[string]any{"action": "plan", "files": []any{"bad"}}, want: "review repository files"},
		{name: "malformed freeze scope", args: map[string]any{"action": "freeze", "files": "bad"}, want: "immutable Git scope"},
		{name: "unserializable record plan", args: map[string]any{"action": "record", "plan": make(chan int)}, want: "unsupported type"},
		{name: "record without evidence", args: map[string]any{"action": "record", "plan": repoaudit.Plan{}}, want: "structured review evidence"},
		{name: "unserializable result plan", args: map[string]any{"action": "result", "plan": make(chan int)}, want: "unsupported type"},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, err := nativeRepositoryReview(context.Background(), test.args, exec)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("nativeRepositoryReview(%#v) error = %v, want %q", test.args, err, test.want)
			}
		})
	}

	pending, err := nativeRepositoryReview(context.Background(), map[string]any{
		"action": "result",
		"plan":   repoaudit.Plan{PendingFiles: []repoaudit.FileRef{{Path: "service.go"}}},
	}, exec)
	if err != nil || pending["summary"] != "Repository review batch completed." {
		t.Fatalf("pending result = (%#v, %v)", pending, err)
	}
	noop, err := nativeRepositoryReview(context.Background(), map[string]any{
		"action": "result", "plan": repoaudit.Plan{}, "excluded_count": "2",
	}, exec)
	if err != nil || noop["summary"] != "No changed reviewable files required model review." ||
		noop["findingIds"] == nil {
		t.Fatalf("noop result = (%#v, %v)", noop, err)
	}
	preserved, err := nativeRepositoryReview(context.Background(), map[string]any{
		"action": "result",
		"plan":   repoaudit.Plan{},
		"review": map[string]any{"summary": "review complete"},
		"recorded": map[string]any{
			"run":                map[string]any{"reviewed_files": 1},
			"acceptedFindingIds": []string{"finding-1"},
		},
	}, exec)
	if err != nil || preserved["summary"] != "review complete" ||
		!reflect.DeepEqual(preserved["findingIds"], []string{"finding-1"}) {
		t.Fatalf("preserved result = (%#v, %v)", preserved, err)
	}
}

func TestNativeFrozenGitScopeEnforcesIdentityShapeLifetimeAndCapacity(t *testing.T) {
	exec := ExecutionContext{RunID: "freeze-errors", WorkflowRef: "workflows/review.yml"}
	if _, err := storeNativeFrozenGitScope(ExecutionContext{}, []map[string]any{}); err == nil ||
		!strings.Contains(err.Error(), "workflow run identity") {
		t.Fatalf("missing identity error = %v", err)
	}
	if _, err := nativeFrozenGitScopeMemoryBytes("not-a-scope"); err == nil {
		t.Fatal("invalid frozen scope shape was accepted")
	}
	if _, err := nativeFrozenGitScopeMemoryBytes([]any{"not-an-object"}); err == nil {
		t.Fatal("non-object frozen scope item was accepted")
	}
	if _, err := nativeFrozenGitScopeMemoryBytes(map[string]any{
		"items": []any{}, "metadata": make(chan int),
	}); err == nil {
		t.Fatal("unserializable frozen scope wrapper was accepted")
	}
	if _, err := nativeFrozenGitScopeMemoryBytes([]map[string]any{{"metadata": make(chan int)}}); err == nil {
		t.Fatal("unserializable frozen scope item was accepted")
	}
	if _, err := storeNativeFrozenGitScope(exec, []map[string]any{{
		"path": "huge.go", "content": strings.Repeat("x", maxNativeFrozenGitScopeBytes+1),
	}}); err == nil || !strings.Contains(err.Error(), "size limit") {
		t.Fatalf("oversize frozen scope error = %v", err)
	}
	if _, err := consumeNativeFrozenGitScope(exec, "short"); err == nil {
		t.Fatal("short frozen scope token was accepted")
	}
	if refs := nativeFrozenGitScopeReferences("bad"); refs != nil {
		t.Fatalf("invalid frozen references = %#v", refs)
	}
	refs := nativeFrozenGitScopeReferences([]any{"bad", map[string]any{
		"path": "service.go", "content": "secret", "selected": true,
	}})
	if len(refs) != 1 || refs[0]["path"] != "service.go" || refs[0]["content"] != nil {
		t.Fatalf("safe frozen references = %#v", refs)
	}
	if _, _, _, err := nativeReviewableFrozenGitScope("bad", 1); err == nil {
		t.Fatal("invalid reviewable scope was accepted")
	}
	if _, _, _, err := nativeReviewableFrozenGitScope([]any{"bad"}, 1); err == nil {
		t.Fatal("non-object reviewable scope item was accepted")
	}

	nativeFrozenGitScopes.Lock()
	savedEntries, savedBytes := nativeFrozenGitScopes.entries, nativeFrozenGitScopes.bytes
	nativeFrozenGitScopes.entries = map[string]nativeFrozenGitScopeEntry{
		strings.Repeat("e", 64): {bytes: 4, expiresAt: time.Now().Add(-time.Minute)},
	}
	nativeFrozenGitScopes.bytes = 4
	nativeFrozenGitScopes.Unlock()
	defer func() {
		nativeFrozenGitScopes.Lock()
		nativeFrozenGitScopes.entries = savedEntries
		nativeFrozenGitScopes.bytes = savedBytes
		nativeFrozenGitScopes.Unlock()
	}()

	token, err := storeNativeFrozenGitScope(exec, []map[string]any{{"path": "service.go"}})
	if err != nil {
		t.Fatal(err)
	}
	nativeFrozenGitScopes.Lock()
	_, expiredStillPresent := nativeFrozenGitScopes.entries[strings.Repeat("e", 64)]
	nativeFrozenGitScopes.Unlock()
	if expiredStillPresent {
		t.Fatal("expired frozen scope was not evicted")
	}
	discardNativeFrozenGitScope(token)

	nativeFrozenGitScopes.Lock()
	nativeFrozenGitScopes.entries = make(map[string]nativeFrozenGitScopeEntry, maxNativeFrozenGitScopes)
	for index := 0; index < maxNativeFrozenGitScopes; index++ {
		nativeFrozenGitScopes.entries[strings.Repeat(string(rune('a'+index)), 64)] = nativeFrozenGitScopeEntry{
			expiresAt: time.Now().Add(time.Hour),
		}
	}
	nativeFrozenGitScopes.bytes = 0
	nativeFrozenGitScopes.Unlock()
	if _, err := storeNativeFrozenGitScope(exec, []map[string]any{}); err == nil ||
		!strings.Contains(err.Error(), "capacity") {
		t.Fatalf("full frozen cache error = %v", err)
	}
}

func TestNativeRepositoryReviewHelpersRejectUntrustedReferences(t *testing.T) {
	workspace := t.TempDir()
	writeTestFile(t, filepath.Join(workspace, "service.go"), "package service\n")
	gitCmd(t, workspace, "init")
	gitCmd(t, workspace, "config", "user.email", "test@example.com")
	gitCmd(t, workspace, "config", "user.name", "Test User")
	gitCmd(t, workspace, "add", "service.go")
	gitCmd(t, workspace, "commit", "-m", "initial")
	exec := ExecutionContext{WorkspaceDir: workspace, RunID: "inventory-errors"}
	inventory, err := nativeCollectInventory(context.Background(), workspace, "HEAD")
	if err != nil || len(inventory) != 1 {
		t.Fatalf("inventory = (%#v, %v)", inventory, err)
	}
	inventoryHash, err := nativeStableHash(inventory)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := nativeBindRepositoryReviewInventory(context.Background(), map[string]any{
		"commit": "missing-ref", "inventory_hash": inventoryHash,
	}, exec, nil); err == nil {
		t.Fatal("missing inventory commit was accepted")
	}
	if _, err := nativeBindRepositoryReviewInventory(context.Background(), map[string]any{
		"commit": "HEAD", "inventory_hash": "wrong",
	}, exec, nil); err == nil || !strings.Contains(err.Error(), "inventory hash") {
		t.Fatalf("inventory mismatch error = %v", err)
	}
	if _, err := nativeBindRepositoryReviewInventory(context.Background(), map[string]any{
		"commit": "HEAD", "inventory_hash": inventoryHash,
	}, exec, []repoaudit.FileRef{{Path: "service.go", BlobSHA: "wrong", SizeBytes: inventory[0].SizeBytes}}); err == nil ||
		!strings.Contains(err.Error(), "does not match") {
		t.Fatalf("file mismatch error = %v", err)
	}

	gitCmd(t, workspace, "remote", "add", "origin", "git@github.com:Owner/Repo.git")
	if _, err := nativeRepositoryReviewIdentity(context.Background(), map[string]any{
		"repository": "different/repo",
	}, exec); err == nil || !strings.Contains(err.Error(), "does not match") {
		t.Fatalf("GitHub identity mismatch error = %v", err)
	}
	gitCmd(t, workspace, "remote", "remove", "origin")
	if _, err := nativeRepositoryReviewIdentity(context.Background(), map[string]any{
		"repository": "owner/repo",
	}, exec); err == nil || !strings.Contains(err.Error(), "publishable") {
		t.Fatalf("local identity mismatch error = %v", err)
	}

	if got := nativeRepositorySourceIdentity("git@GitLab.Example:Group/Repo.git", workspace); got !=
		"ssh://gitlab.example/Group/Repo.git" {
		t.Fatalf("SCP source identity = %q", got)
	}
	for _, remote := range []string{"", "git@example.com:owner/repo.git", "https://github.com/owner/repo.git?token=x"} {
		if got := nativeGitHubRepositoryIdentity(remote); got != "" {
			t.Fatalf("unsafe GitHub identity %q = %q", remote, got)
		}
	}
	if nativeValidGitHubName(strings.Repeat("a", 101), false) || nativeValidGitHubName("bad/name", true) {
		t.Fatal("invalid GitHub repository names were accepted")
	}
}

func TestNativeRepositoryReviewEvidenceContracts(t *testing.T) {
	file := repoaudit.FileRef{Path: "service.go", BlobSHA: strings.Repeat("a", 40), SizeBytes: 12}
	fileMap := nativeRepositoryReviewFileMaps([]repoaudit.FileRef{file})[0]
	if parsed, err := nativeRepositoryReviewFiles([]map[string]any{{
		"path": "service.go", "blob_sha": file.BlobSHA, "size_bytes": json.Number("12"),
	}}); err != nil || !reflect.DeepEqual(parsed, []repoaudit.FileRef{file}) {
		t.Fatalf("legacy exact file reference = (%#v, %v)", parsed, err)
	}
	for _, value := range []any{
		[]map[string]any{{"path": "", "fileHash": file.BlobSHA, "sizeBytes": 1}},
		[]map[string]any{{"path": "service.go", "fileHash": "", "sizeBytes": 1}},
		[]map[string]any{{"path": "service.go", "fileHash": file.BlobSHA, "sizeBytes": -1}},
	} {
		if _, err := nativeRepositoryReviewFiles(value); err == nil {
			t.Fatalf("invalid exact file reference %#v was accepted", value)
		}
	}
	if _, err := nativeRepositoryReviewPlan(make(chan int)); err == nil {
		t.Fatal("unserializable plan was accepted")
	}
	if _, err := nativeRepositoryReviewPlan("not-a-plan"); err == nil {
		t.Fatal("non-object plan was accepted")
	}
	if _, err := nativeRepositoryReviewPlanOutput(repoaudit.Plan{}, "bad", false); err == nil {
		t.Fatal("invalid original plan files were accepted")
	}
	bound := nativeRepositoryReviewBoundFileMaps([]repoaudit.FileRef{file}, nil)
	if len(bound) != 1 || bound[0]["path"] != file.Path {
		t.Fatalf("fallback bound file = %#v", bound)
	}

	if _, _, err := nativeRepositoryReviewObservations(map[string]any{
		"managed_children": "bad",
	}, repoaudit.Plan{}); err == nil || !strings.Contains(err.Error(), "managed children") {
		t.Fatalf("invalid managed children error = %v", err)
	}
	if observations, completed, err := nativeRepositoryReviewObservations(map[string]any{
		"reviewable_count": 0,
	}, repoaudit.Plan{}); err != nil || observations != nil || len(completed) != 0 {
		t.Fatalf("bounded empty review = (%#v, %#v, %v)", observations, completed, err)
	}
	if _, _, err := nativeRepositoryReviewObservations(map[string]any{
		"managed_children": []map[string]any{{"scope": "bad"}},
	}, repoaudit.Plan{}); err == nil || !strings.Contains(err.Error(), "managed child 0 scope") {
		t.Fatalf("invalid child scope error = %v", err)
	}
	if _, _, err := nativeRepositoryReviewObservations(
		nil,
		repoaudit.Plan{PendingFiles: []repoaudit.FileRef{file}},
	); err == nil {
		t.Fatal("missing single-review evidence was accepted")
	}
	structured := map[string]any{
		"summary": "checked", "reviewedFiles": []any{"service.go"}, "findings": []any{},
	}
	observations, completed, err := nativeRepositoryReviewObservations(map[string]any{
		"review": structured,
	}, repoaudit.Plan{PendingFiles: []repoaudit.FileRef{file}})
	if err != nil || len(observations) != 1 || observations[0].Model != "default" ||
		!reflect.DeepEqual(completed, []repoaudit.FileRef{file}) {
		t.Fatalf("default single review = (%#v, %#v, %v)", observations, completed, err)
	}

	complete := map[string]bool{"service.go": true}
	for _, test := range []struct {
		name       string
		structured map[string]any
		want       string
	}{
		{name: "missing list", structured: map[string]any{}, want: "required"},
		{name: "outside scope", structured: map[string]any{"reviewedFiles": []any{"other.go"}}, want: "not readable"},
		{name: "duplicate", structured: map[string]any{"reviewedFiles": []any{"service.go", "service.go"}}, want: "duplicated"},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, err := nativeRepositoryReviewAcknowledgedPaths(test.structured, []repoaudit.FileRef{file}, complete)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("acknowledgement error = %v, want %q", err, test.want)
			}
		})
	}
	if got := nativeRepositoryReviewCompletedScopePaths("bad"); got != nil {
		t.Fatalf("invalid completed scope = %#v", got)
	}
	if _, err := nativeRepositoryReviewObservation(structured, "bad", "model", "reviewer", "raw"); err == nil ||
		!strings.Contains(err.Error(), "scope") {
		t.Fatalf("invalid observation scope error = %v", err)
	}
	if _, err := nativeRepositoryReviewObservation(
		map[string]any{"findings": "bad"},
		[]map[string]any{fileMap},
		"model",
		"reviewer",
		"raw",
	); err == nil ||
		!strings.Contains(err.Error(), "findings") {
		t.Fatalf("invalid findings error = %v", err)
	}
	if _, err := nativeRepositoryReviewObservation(map[string]any{
		"reviewedFiles": []any{"service.go"}, "findings": []map[string]any{{"bad": make(chan int)}},
	}, []map[string]any{fileMap}, "model", "reviewer", "raw"); err == nil {
		t.Fatal("unserializable finding was accepted")
	}
	if merged := mergeNativeRepositoryUnsupportedFiles(
		[]repoaudit.UnsupportedFile{{FileRef: repoaudit.FileRef{Path: "b"}, Reason: "binary"}},
		[]repoaudit.UnsupportedFile{
			{FileRef: repoaudit.FileRef{Path: "a"}, Reason: "file_too_large"},
			{FileRef: repoaudit.FileRef{Path: "b"}, Reason: "file_too_large"},
		},
	); len(merged) != 2 || merged[0].Path != "a" || merged[1].Reason != "file_too_large" {
		t.Fatalf("merged unsupported files = %#v", merged)
	}
}

func TestNativeRepositoryReviewCoercionAndGroupingContracts(t *testing.T) {
	if got := nativeReviewRelationshipScore(
		map[string]any{"path": "pkg/service.go", "content": "calls service_test"},
		map[string]any{"path": "pkg/service_test.go", "content": "tests service"},
	); got != 16 {
		t.Fatalf("same-directory relationship score = %d, want 16", got)
	}
	if got := nativeReviewRelationshipScore(
		map[string]any{"path": "pkg/service.go", "content": "calls service"},
		map[string]any{"path": "tests/service.ts", "content": "tests service"},
	); got != 14 {
		t.Fatalf("same-stem relationship score = %d, want 14", got)
	}
	if got := nativeGroupFrozenReviewScope([]map[string]any{{"path": "one"}}, 3, 10); len(got) != 1 {
		t.Fatalf("single-file group = %#v", got)
	}
	invalidUnsupported := nativeRepositoryReviewUnsupportedFiles("bad")
	if invalidUnsupported != nil {
		t.Fatalf("invalid unsupported children = %#v", invalidUnsupported)
	}
	files := nativeRepositoryReviewUnsupportedFiles([]map[string]any{{
		"scope": []map[string]any{
			{
				"path":               "complete.bin",
				"fileHash":           "a",
				"sizeBytes":          1,
				"contentComplete":    true,
				"contentUnavailable": "binary",
			},
			{"path": "bad.bin", "contentComplete": false, "contentUnavailable": "binary"},
		},
	}})
	if len(files) != 0 {
		t.Fatalf("invalid/complete unsupported files = %#v", files)
	}
	if files := nativeRepositoryReviewUnsupportedScopeFiles("bad"); files != nil {
		t.Fatalf("invalid unsupported scope files = %#v", files)
	}

	values := map[string]any{
		"int": 2, "int64": int64(3), "float": float64(4), "number": json.Number("5"),
		"string": " 6 ", "invalid": "bad",
	}
	for key, want := range map[string]int64{"int": 2, "int64": 3, "float": 4, "number": 5, "string": 6, "invalid": 0, "missing": 0} {
		if got := nativeInt64Any(values, key); got != want {
			t.Fatalf("nativeInt64Any(%q) = %d, want %d", key, got, want)
		}
	}
	if firstNonNil(nil, "value", "later") != "value" || firstNonNil(nil, nil) != nil {
		t.Fatal("firstNonNil did not preserve the first available value")
	}
	if _, err := nativeJSONMap(make(chan int)); err == nil {
		t.Fatal("unserializable JSON map was accepted")
	}
	if _, err := nativeJSONMap("scalar"); err == nil {
		t.Fatal("scalar JSON map was accepted")
	}
}
