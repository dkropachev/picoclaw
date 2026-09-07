package workflows

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/sipeed/picoclaw/pkg/repoaudit"
)

func repositoryReviewTestFinding(finding map[string]any) map[string]any {
	finding["match_hints"] = map[string]any{
		"component": "persistence", "operation": "save versioned state",
		"failure_mode":       "a later writer replaces an accepted update",
		"trigger":            "two writers begin from the same version",
		"violated_invariant": "every accepted update remains represented in committed state",
		"observable_outcome": "one successful update disappears",
		"related_symbols":    []any{"Save"}, "source_anchors": []any{"version"},
		"distinguishing_facts": []any{"requires overlapping writes"},
	}
	finding["fix_effort"] = map[string]any{
		"quick": map[string]any{
			"loc_min": 5, "loc_max": 20, "class": "small",
			"rationale": "Containment is localized to the write path.",
		},
		"quality": map[string]any{
			"loc_min": 30, "loc_max": 100, "class": "medium",
			"rationale": "The state invariant spans persistence and concurrency tests.",
		},
	}
	return finding
}

func TestNativeRepositoryReviewRejectsFieldsOutsideDiagnosisOnlyContract(t *testing.T) {
	_, err := nativeRepositoryReviewObservationWithProvenance(
		map[string]any{
			"summary": "found", "reviewedFiles": []any{"service.go"},
			"findings": []any{map[string]any{
				"severity": "high", "title": "Lost update", "symbol": "Save",
				"file": "service.go", "message": "Concurrent saves overwrite state.",
				"evidence": "Both writes use the same version.", "impact": "Data is lost.",
				"recommendation": "Use compare-and-swap.",
				"validation": map[string]any{
					"status": "confirmed", "summary": "Traced two writers", "checks": []any{},
				},
			}},
			"residualRisks": []any{},
		},
		[]map[string]any{{
			"path": "service.go", "fileHash": strings.Repeat("a", 40),
			"sizeBytes": int64(10), "contentComplete": true,
		}},
		nativeRepositoryReviewProvenance{Model: "review-a"},
		"challenge",
		"response",
	)
	if err == nil || !strings.Contains(err.Error(), `field "recommendation" outside the diagnosis-only contract`) {
		t.Fatalf("recommendation contract error=%v", err)
	}
}

func TestNativeRepositoryReviewRequiresClosedMatchingAndEffortContract(t *testing.T) {
	emptyOutput := map[string]any{
		"summary": "none", "reviewedFiles": []any{}, "findings": []any{},
		"residualRisks": []any{},
	}
	if err := nativeValidateRepositoryReviewOutputFields(emptyOutput); err != nil {
		t.Fatalf("valid empty output was rejected: %v", err)
	}
	for _, field := range []string{"summary", "reviewedFiles", "findings", "residualRisks"} {
		missing := make(map[string]any, len(emptyOutput)-1)
		for key, value := range emptyOutput {
			if key != field {
				missing[key] = value
			}
		}
		if err := nativeValidateRepositoryReviewOutputFields(missing); err == nil ||
			!strings.Contains(err.Error(), `missing required field "`+field+`"`) {
			t.Fatalf("missing root field %q error=%v", field, err)
		}
	}
	scope := []map[string]any{{
		"path": "service.go", "fileHash": strings.Repeat("a", 40),
		"sizeBytes": int64(10), "contentComplete": true,
	}}
	validFinding := func() map[string]any {
		return repositoryReviewTestFinding(map[string]any{
			"severity": "high", "title": "Lost update", "symbol": "Save",
			"file": "service.go", "message": "Concurrent saves overwrite state.",
			"evidence": "Both writes use one version.", "impact": "Data is lost.",
			"validation": map[string]any{
				"status": "confirmed", "summary": "Traced two writers", "checks": []any{},
			},
		})
	}
	observe := func(finding map[string]any) error {
		_, err := nativeRepositoryReviewObservationWithProvenance(
			map[string]any{
				"summary": "found", "reviewedFiles": []any{"service.go"},
				"findings": []any{finding}, "residualRisks": []any{},
			},
			scope,
			nativeRepositoryReviewProvenance{Model: "review-a"},
			"challenge",
			"response",
		)
		return err
	}
	if err := observe(validFinding()); err != nil {
		t.Fatalf("valid matching contract was rejected: %v", err)
	}
	tests := []struct {
		name   string
		mutate func(map[string]any)
		want   string
	}{
		{
			name: "missing match hints",
			mutate: func(finding map[string]any) {
				delete(finding, "match_hints")
			},
			want: `missing required field "match_hints"`,
		},
		{
			name: "extra match hint",
			mutate: func(finding map[string]any) {
				finding["match_hints"].(map[string]any)["remediation"] = "change the lock"
			},
			want: "outside the diagnosis-only contract",
		},
		{
			name: "missing effort estimate field",
			mutate: func(finding map[string]any) {
				quick := finding["fix_effort"].(map[string]any)["quick"].(map[string]any)
				delete(quick, "rationale")
			},
			want: `missing required field "rationale"`,
		},
		{
			name: "inconsistent effort class",
			mutate: func(finding map[string]any) {
				finding["fix_effort"].(map[string]any)["quick"].(map[string]any)["class"] = "tiny"
			},
			want: "class is inconsistent",
		},
		{
			name: "quality smaller than quick",
			mutate: func(finding map[string]any) {
				quality := finding["fix_effort"].(map[string]any)["quality"].(map[string]any)
				quality["loc_min"], quality["loc_max"], quality["class"] = 10, 15, "small"
			},
			want: "must not be smaller",
		},
		{
			name: "too many anchors",
			mutate: func(finding map[string]any) {
				anchors := make([]any, 33)
				for index := range anchors {
					anchors[index] = fmt.Sprintf("anchor-%d", index)
				}
				finding["match_hints"].(map[string]any)["source_anchors"] = anchors
			},
			want: "too many identity values",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			finding := validFinding()
			test.mutate(finding)
			if err := observe(finding); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("contract error=%v, want %q", err, test.want)
			}
		})
	}
}

func TestNativeRepositoryReviewValidationRejectsMalformedCanonicalFieldTypes(t *testing.T) {
	validFinding := func() map[string]any {
		return repositoryReviewTestFinding(map[string]any{
			"severity": "high", "title": "Lost update", "symbol": "Save",
			"file": "service.go", "message": "Concurrent saves overwrite state.",
			"evidence": "Both writes use one version.", "impact": "Data is lost.",
			"validation": map[string]any{
				"status": "confirmed", "summary": "Traced two writers", "checks": []any{},
			},
		})
	}
	validOutput := func() map[string]any {
		return map[string]any{
			"summary": "found", "reviewedFiles": []any{"service.go"},
			"findings": []any{validFinding()}, "residualRisks": []any{},
		}
	}
	tests := []struct {
		name   string
		mutate func(map[string]any)
	}{
		{name: "extra output field", mutate: func(output map[string]any) { output["patch"] = "forbidden" }},
		{name: "invalid summary", mutate: func(output map[string]any) { output["summary"] = 7 }},
		{name: "unserializable reviewed files", mutate: func(output map[string]any) {
			output["reviewedFiles"] = make(chan int)
		}},
		{name: "null residual risks", mutate: func(output map[string]any) { output["residualRisks"] = nil }},
		{name: "scalar residual risks", mutate: func(output map[string]any) { output["residualRisks"] = "none" }},
		{name: "invalid validation", mutate: func(output map[string]any) {
			output["findings"].([]any)[0].(map[string]any)["validation"] = "invalid"
		}},
		{name: "extra validation field", mutate: func(output map[string]any) {
			output["findings"].([]any)[0].(map[string]any)["validation"].(map[string]any)["next"] = "forbidden"
		}},
		{name: "missing validation field", mutate: func(output map[string]any) {
			delete(output["findings"].([]any)[0].(map[string]any)["validation"].(map[string]any), "checks")
		}},
		{name: "invalid match hints", mutate: func(output map[string]any) {
			output["findings"].([]any)[0].(map[string]any)["match_hints"] = "invalid"
		}},
		{name: "missing match hint field", mutate: func(output map[string]any) {
			delete(output["findings"].([]any)[0].(map[string]any)["match_hints"].(map[string]any), "trigger")
		}},
		{name: "invalid fix effort", mutate: func(output map[string]any) {
			output["findings"].([]any)[0].(map[string]any)["fix_effort"] = "invalid"
		}},
		{name: "extra fix effort field", mutate: func(output map[string]any) {
			output["findings"].([]any)[0].(map[string]any)["fix_effort"].(map[string]any)["later"] = map[string]any{}
		}},
		{name: "missing effort estimate", mutate: func(output map[string]any) {
			delete(output["findings"].([]any)[0].(map[string]any)["fix_effort"].(map[string]any), "quick")
		}},
		{name: "invalid effort estimate", mutate: func(output map[string]any) {
			output["findings"].([]any)[0].(map[string]any)["fix_effort"].(map[string]any)["quick"] = "invalid"
		}},
		{name: "extra estimate field", mutate: func(output map[string]any) {
			finding := output["findings"].([]any)[0].(map[string]any)
			quick := finding["fix_effort"].(map[string]any)["quick"].(map[string]any)
			quick["patch"] = "forbidden"
		}},
		{name: "missing estimate field", mutate: func(output map[string]any) {
			finding := output["findings"].([]any)[0].(map[string]any)
			quick := finding["fix_effort"].(map[string]any)["quick"].(map[string]any)
			delete(quick, "loc_min")
		}},
		{name: "unserializable finding", mutate: func(output map[string]any) {
			output["findings"].([]any)[0].(map[string]any)["line"] = make(chan int)
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			output := validOutput()
			test.mutate(output)
			if err := nativeValidateRepositoryReviewOutputFields(output); err == nil {
				t.Fatalf("malformed canonical output was accepted: %#v", output)
			}
		})
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
	if _, _, _, err := nativeReviewableFrozenGitScope("bad", 3, 1); err == nil {
		t.Fatal("invalid reviewable scope was accepted")
	}
	if _, _, _, err := nativeReviewableFrozenGitScope([]any{"bad"}, 3, 1); err == nil {
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
	if identity, err := nativeRepositoryReviewIdentity(context.Background(), map[string]any{
		"repository": "https://github.com/Owner/Repo.git",
	}, exec); err != nil || identity != "owner/repo" {
		t.Fatalf("canonical GitHub identity=%q err=%v", identity, err)
	}
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
		"path": "service.go", "fileHash": file.BlobSHA, "sizeBytes": json.Number("12"),
	}}); err != nil || !reflect.DeepEqual(parsed, []repoaudit.FileRef{file}) {
		t.Fatalf("exact file reference = (%#v, %v)", parsed, err)
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
			_, acknowledgementErr := nativeRepositoryReviewAcknowledgedPaths(
				test.structured, []repoaudit.FileRef{file}, complete,
			)
			if acknowledgementErr == nil || !strings.Contains(acknowledgementErr.Error(), test.want) {
				t.Fatalf("acknowledgement error = %v, want %q", acknowledgementErr, test.want)
			}
		})
	}
	if got := nativeRepositoryReviewCompletedScopePaths("bad"); got != nil {
		t.Fatalf("invalid completed scope = %#v", got)
	}
	structured := map[string]any{
		"summary": "checked", "reviewedFiles": []any{"service.go"},
		"findings": []any{}, "residualRisks": []any{},
	}
	if _, err := nativeRepositoryReviewObservationWithProvenance(
		structured, "bad", nativeRepositoryReviewProvenance{Model: "model"}, "reviewer", "raw",
	); err == nil || !strings.Contains(err.Error(), "scope") {
		t.Fatalf("invalid observation scope error = %v", err)
	}
	if _, err := nativeRepositoryReviewObservationWithProvenance(
		map[string]any{
			"summary": "bad", "reviewedFiles": []any{},
			"findings": "bad", "residualRisks": []any{},
		},
		[]map[string]any{fileMap},
		nativeRepositoryReviewProvenance{Model: "model"},
		"reviewer",
		"raw",
	); err == nil || !strings.Contains(err.Error(), "findings") {
		t.Fatalf("invalid findings error = %v", err)
	}
	if _, err := nativeRepositoryReviewObservationWithProvenance(
		map[string]any{
			"summary": "bad", "reviewedFiles": []any{"service.go"},
			"findings": []map[string]any{{"bad": make(chan int)}}, "residualRisks": []any{},
		},
		[]map[string]any{fileMap},
		nativeRepositoryReviewProvenance{Model: "model"},
		"reviewer",
		"raw",
	); err == nil {
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
		{name: "unserializable begin plan", args: map[string]any{"action": "begin", "plan": make(chan int)}, want: "unsupported type"},
		{name: "begin without catalog", args: map[string]any{"action": "begin", "plan": repoaudit.Plan{}}, want: "no assignment catalog"},
		{name: "unserializable record plan", args: map[string]any{"action": "record", "plan": make(chan int)}, want: "unsupported type"},
		{name: "record without durable plan", args: map[string]any{"action": "record", "plan": repoaudit.Plan{}}, want: "no assignment catalog"},
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
			"acceptedFindingIds": []string{"rrw_finding_one"},
		},
	}, exec)
	if err != nil || preserved["summary"] != "review complete" ||
		!reflect.DeepEqual(preserved["findingIds"], []string{"rrw_finding_one"}) {
		t.Fatalf("preserved result = (%#v, %v)", preserved, err)
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
