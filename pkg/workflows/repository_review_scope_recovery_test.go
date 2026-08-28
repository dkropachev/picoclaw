package workflows

import (
	"reflect"
	"strings"
	"testing"

	"github.com/sipeed/picoclaw/pkg/repoaudit"
	"github.com/sipeed/picoclaw/pkg/reposcope"
)

func TestRecoverRepositoryReviewFrozenScopeRoundTripsAfterAutomationNormalization(t *testing.T) {
	commit := strings.Repeat("a", 40)
	inventory := reposcope.Inventory{CommitID: commit, ID: "inventory", Files: []reposcope.FileMetadata{
		{
			Path: "zeta/service.go", BlobID: strings.Repeat("1", 40), Size: 100,
			Kind: reposcope.FileKindRegular, Sample: []byte("package zeta\n"),
		},
		{
			Path: "alpha/router.go", BlobID: strings.Repeat("2", 40), Size: 120,
			Kind: reposcope.FileKindRegular, Sample: []byte("package alpha\n"),
		},
	}}
	hardScope := repoaudit.RepositoryReviewScopePolicy{
		CodeTypes: []repoaudit.RepositoryReviewCodeType{repoaudit.RepositoryReviewCodeTypeCode},
	}
	candidates, rejected, err := reposcope.BuildCandidates(
		inventory, reposcope.Scope{CodeTypes: []reposcope.CodeType{reposcope.CodeTypeCode}},
		reposcope.BuildOptions{},
	)
	if err != nil || len(rejected) != 0 {
		t.Fatalf("catalog = %#v, %#v, %v", candidates, rejected, err)
	}
	selection, plan, err := RecoverRepositoryReviewFrozenScope(
		candidates, hardScope, commit, inventory.ID, []string{"zeta/service.go", "alpha/router.go"},
	)
	if err != nil {
		t.Fatal(err)
	}
	selection, err = repoaudit.NormalizeRepositoryReviewScopeSelection(selection)
	if err != nil {
		t.Fatal(err)
	}
	plan, err = repoaudit.NormalizeRepositoryReviewScopePlan(plan)
	if err != nil {
		t.Fatal(err)
	}
	selectionMap, err := nativeJSONMap(selection)
	if err != nil {
		t.Fatal(err)
	}
	planMap, err := nativeJSONMap(plan)
	if err != nil {
		t.Fatal(err)
	}
	filtered, err := nativeRepositoryEvaluationFilter(map[string]any{
		"candidates": candidates, "hard_scope": hardScope, "commit": commit,
		"scope_planned": true, "frozen_selection": selectionMap, "frozen_plan": planMap,
	})
	if err != nil {
		t.Fatal(err)
	}
	var replayedSelection repoaudit.RepositoryReviewScopeSelection
	var replayedPlan repoaudit.RepositoryReviewScopePlan
	if err := nativeRepositoryEvaluationDecode(filtered["scopeSelection"], &replayedSelection); err != nil {
		t.Fatal(err)
	}
	if err := nativeRepositoryEvaluationDecode(filtered["scopePlan"], &replayedPlan); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(replayedSelection, selection) || !reflect.DeepEqual(replayedPlan, plan) {
		t.Fatalf("frozen replay = %#v %#v, want %#v %#v", replayedSelection, replayedPlan, selection, plan)
	}
}

func TestRecoverRepositoryReviewFrozenScopeRejectsPartialHardScopeExclusion(t *testing.T) {
	commit := strings.Repeat("a", 40)
	inventory := reposcope.Inventory{CommitID: commit, ID: "inventory", Files: []reposcope.FileMetadata{
		{
			Path: "alpha/router.go", BlobID: strings.Repeat("1", 40), Size: 100,
			Kind: reposcope.FileKindRegular, Sample: []byte("package alpha\n"),
		},
		{
			Path: "zeta/service_test.go", BlobID: strings.Repeat("2", 40), Size: 120,
			Kind: reposcope.FileKindRegular, Sample: []byte("package zeta\n"),
		},
	}}
	candidates, rejected, err := reposcope.BuildCandidates(
		inventory,
		reposcope.Scope{CodeTypes: []reposcope.CodeType{reposcope.CodeTypeCode, reposcope.CodeTypeTest}},
		reposcope.BuildOptions{},
	)
	if err != nil || len(rejected) != 0 || len(candidates) != 2 {
		t.Fatalf("catalog = %#v, rejected=%#v, err=%v", candidates, rejected, err)
	}
	hardScope := map[string]any{"code_types": []string{"code"}}
	_, _, err = RecoverRepositoryReviewFrozenScope(
		candidates,
		hardScope,
		commit,
		inventory.ID,
		[]string{"alpha/router.go", "zeta/service_test.go"},
	)
	if err == nil || !strings.Contains(err.Error(), "exact path union") {
		t.Fatalf("partial hard-scope exclusion error = %v; candidates=%#v", err, candidates)
	}
}

func TestRepositoryReviewRequiredAssignmentsUsesFourTaskCohort(t *testing.T) {
	for reviewers, want := range map[int]int{1: 4, 2: 8, 32: 128} {
		if got, err := RepositoryReviewRequiredAssignments(reviewers); err != nil || got != want {
			t.Fatalf("reviewers=%d assignments=%d err=%v, want %d", reviewers, got, err, want)
		}
	}
	for _, reviewers := range []int{0, 33} {
		if _, err := RepositoryReviewRequiredAssignments(reviewers); err == nil {
			t.Fatalf("invalid reviewer count %d accepted", reviewers)
		}
	}
	if got, err := RepositoryBugFinderRequiredAssignments(
		[]string{"fallback-a", "fallback-b"}, true,
	); err != nil || got != 4 {
		t.Fatalf("default-chain assignments=%d err=%v, want 4", got, err)
	}
}

func TestDecodeRepositoryReviewManagedEvidenceAcceptsOnlyExactLegacyCoreSchema(t *testing.T) {
	file := repoaudit.FileRef{
		Path: "legacy.go", BlobSHA: strings.Repeat("a", 40), SizeBytes: 10,
		Category: "code", Mode: "100644",
	}
	structured := map[string]any{
		"summary": "legacy", "reviewedFiles": []string{file.Path}, "residualRisks": []string{},
		"findings": []map[string]any{{
			"severity": "high", "title": "legacy defect", "symbol": "Run", "file": file.Path,
			"message": "state is lost", "evidence": "the branch drops state", "impact": "request fails",
			"validation": map[string]any{
				"status": "confirmed", "summary": "traced", "checks": []string{"branch"},
			},
		}},
	}
	children := []map[string]any{{
		"scope": []map[string]any{{
			"path": file.Path, "fileHash": file.BlobSHA, "sizeBytes": file.SizeBytes,
			"category": file.Category, "mode": file.Mode, "contentComplete": true,
		}},
		"valid": true, "structured": structured, "text": "legacy raw",
		"model": map[string]any{"selected": "review"}, "label": "correctness",
	}}
	decoded, err := DecodeRepositoryReviewManagedEvidence(
		children, repoaudit.Plan{PendingFiles: []repoaudit.FileRef{file}},
		RepositoryReviewManagedEvidenceOptions{AllowLegacyCoreFindings: true},
	)
	if err != nil || len(decoded.Observations) != 1 || len(decoded.Observations[0].Findings) != 1 ||
		!reflect.DeepEqual(decoded.Observations[0].Findings[0].MatchHints, repoaudit.MatchHints{}) ||
		!reflect.DeepEqual(decoded.Observations[0].Findings[0].FixEffort, repoaudit.FixEffort{}) {
		t.Fatalf("legacy core evidence = %#v err=%v", decoded, err)
	}
	structured["findings"].([]map[string]any)[0]["recommendation"] = "change it"
	if _, err := DecodeRepositoryReviewManagedEvidence(
		children, repoaudit.Plan{PendingFiles: []repoaudit.FileRef{file}},
		RepositoryReviewManagedEvidenceOptions{AllowLegacyCoreFindings: true},
	); err == nil {
		t.Fatal("legacy core decoder accepted an unknown remediation field")
	}
}
