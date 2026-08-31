package workflows

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/sipeed/picoclaw/pkg/repoaudit"
	"github.com/sipeed/picoclaw/pkg/reposcope"
)

func TestDecodeRepositoryReviewManagedEvidenceRejectsInvalidEnvelopeAndOptions(t *testing.T) {
	file := repositoryReviewRecoveryFile("service.go", "a")
	plan := repoaudit.Plan{PendingFiles: []repoaudit.FileRef{file}}
	requireError := func(name string, run func() error) {
		t.Helper()
		t.Run(name, func(t *testing.T) {
			t.Helper()
			if err := run(); err == nil {
				t.Fatal("invalid managed evidence was accepted")
			}
		})
	}

	requireError("multiple options", func() error {
		_, err := DecodeRepositoryReviewManagedEvidence(
			nil,
			plan,
			RepositoryReviewManagedEvidenceOptions{},
			RepositoryReviewManagedEvidenceOptions{},
		)
		return err
	})
	requireError("non-array children", func() error {
		_, err := DecodeRepositoryReviewManagedEvidence(42, plan)
		return err
	})
	requireError("too many children", func() error {
		children := make([]map[string]any, maxRepositoryReviewManagedEvidenceChildren+1)
		_, err := DecodeRepositoryReviewManagedEvidence(children, plan)
		return err
	})
	requireError("duplicate pending file", func() error {
		_, err := DecodeRepositoryReviewManagedEvidence(
			nil,
			repoaudit.Plan{PendingFiles: []repoaudit.FileRef{file, file}},
		)
		return err
	})
	requireError("empty pending path", func() error {
		_, err := DecodeRepositoryReviewManagedEvidence(
			nil,
			repoaudit.Plan{PendingFiles: []repoaudit.FileRef{{BlobSHA: file.BlobSHA}}},
		)
		return err
	})
	requireError("empty plan", func() error {
		_, err := DecodeRepositoryReviewManagedEvidence(nil, repoaudit.Plan{})
		return err
	})
	for _, hint := range []int{-1, maxRepositoryReviewManagedAssignments + 1} {
		requireError("invalid assignment hint", func() error {
			_, err := DecodeRepositoryReviewManagedEvidence(
				nil,
				plan,
				RepositoryReviewManagedEvidenceOptions{RequiredAssignments: hint},
			)
			return err
		})
	}
	requireError("terminal outside plan", func() error {
		other := repositoryReviewRecoveryFile("other.go", "b")
		_, err := DecodeRepositoryReviewManagedEvidence(
			nil,
			plan,
			RepositoryReviewManagedEvidenceOptions{
				TerminalUnsupportedFiles: []repoaudit.FileRef{other},
				RequiredAssignments:      1,
			},
		)
		return err
	})
	requireError("duplicate terminal", func() error {
		_, err := DecodeRepositoryReviewManagedEvidence(
			nil,
			plan,
			RepositoryReviewManagedEvidenceOptions{
				TerminalUnsupportedFiles: []repoaudit.FileRef{file, file},
				RequiredAssignments:      1,
			},
		)
		return err
	})
	requireError("children required", func() error {
		_, err := DecodeRepositoryReviewManagedEvidence(nil, plan)
		return err
	})

	decoded, err := DecodeRepositoryReviewManagedEvidence(
		nil,
		plan,
		RepositoryReviewManagedEvidenceOptions{
			TerminalUnsupportedFiles: []repoaudit.FileRef{file},
			RequiredAssignments:      4,
		},
	)
	if err != nil || decoded.RequiredAssignments != 4 {
		t.Fatalf("terminal-only evidence = %#v, err=%v", decoded, err)
	}
}

func TestDecodeRepositoryReviewManagedEvidenceDerivesExactCoverage(t *testing.T) {
	alpha := repositoryReviewRecoveryFile("alpha.go", "a")
	zeta := repositoryReviewRecoveryFile("zeta.go", "b")
	plan := repoaudit.Plan{PendingFiles: []repoaudit.FileRef{alpha, zeta}}
	scope := repositoryReviewRecoveryScope(zeta, alpha)
	children := []map[string]any{
		{
			"scope": scope, "valid": true, "label": "correctness", "text": "primary",
			"model": map[string]any{
				"default": "review-default", "actual": "provider/review-default", "account": "account",
			},
			"structured": repositoryReviewRecoveryEmptyOutput(zeta.Path, alpha.Path),
		},
		{
			"scope": scope, "required": false, "valid": true, "label": "optional",
			"model": map[string]any{
				"selected": "review-optional", "actual": "provider/review-optional", "account": "account",
			},
			"structured": repositoryReviewRecoveryEmptyOutput(zeta.Path),
		},
	}
	decoded, err := DecodeRepositoryReviewManagedEvidence(children, plan)
	if err != nil {
		t.Fatal(err)
	}
	if decoded.RequiredAssignments != 1 || len(decoded.Children) != 2 ||
		len(decoded.Observations) != 2 || !decoded.Children[0].Successful ||
		!decoded.Children[1].Successful {
		t.Fatalf("decoded evidence = %#v", decoded)
	}
	wantFiles := []repoaudit.FileRef{alpha, zeta}
	if !reflect.DeepEqual(decoded.InspectedFiles, wantFiles) ||
		!reflect.DeepEqual(decoded.CompletedFiles, wantFiles) {
		t.Fatalf(
			"coverage inspected=%#v completed=%#v, want %#v",
			decoded.InspectedFiles,
			decoded.CompletedFiles,
			wantFiles,
		)
	}
	if got := decoded.Observations[0]; got.Model != "provider/review-default" ||
		got.ModelAlias != "review-default" || got.Account != "account" {
		t.Fatalf("default model provenance = %#v", got)
	}
}

func TestDecodeRepositoryReviewManagedEvidenceHandlesFailuresAndUnsupportedFiles(t *testing.T) {
	regular := repositoryReviewRecoveryFile("regular.go", "a")
	unsupported := repositoryReviewRecoveryFile("asset.bin", "b")
	unsupportedScope := repositoryReviewRecoveryScope(unsupported)
	unsupportedScope[0]["contentComplete"] = false
	unsupportedScope[0]["contentUnavailable"] = "binary"
	children := []map[string]any{
		{
			"scope": repositoryReviewRecoveryScope(regular), "valid": true,
			"model": map[string]any{
				"selected": "review", "actual": "provider/review", "account": "account",
			},
			"structured": repositoryReviewRecoveryEmptyOutput(regular.Path),
		},
		{
			"scope": unsupportedScope, "required": false, "valid": false,
			"run_error": "unsupported",
		},
	}
	decoded, err := DecodeRepositoryReviewManagedEvidence(
		children,
		repoaudit.Plan{PendingFiles: []repoaudit.FileRef{regular, unsupported}},
	)
	if err != nil {
		t.Fatal(err)
	}
	if decoded.RequiredAssignments != 1 || len(decoded.UnsupportedFiles) != 1 ||
		decoded.UnsupportedFiles[0].FileRef != unsupported ||
		decoded.UnsupportedFiles[0].Reason != "binary" || decoded.Children[1].Successful {
		t.Fatalf("unsupported evidence = %#v", decoded)
	}

	malformed := []map[string]any{{
		"scope": repositoryReviewRecoveryScope(regular), "valid": true,
		"model":      map[string]any{"selected": "review"},
		"structured": map[string]any{"summary": "missing acknowledgement"},
	}}
	decoded, err = DecodeRepositoryReviewManagedEvidence(
		malformed,
		repoaudit.Plan{PendingFiles: []repoaudit.FileRef{regular}},
	)
	if err != nil || decoded.Children[0].Successful || len(decoded.InspectedFiles) != 0 {
		t.Fatalf("malformed acknowledgement evidence = %#v, err=%v", decoded, err)
	}
}

func TestDecodeRepositoryReviewManagedEvidenceRejectsInvalidChildCoverage(t *testing.T) {
	alpha := repositoryReviewRecoveryFile("alpha.go", "a")
	beta := repositoryReviewRecoveryFile("beta.go", "b")
	requireError := func(name string, children []map[string]any, plan repoaudit.Plan, options ...RepositoryReviewManagedEvidenceOptions) {
		t.Helper()
		t.Run(name, func(t *testing.T) {
			if _, err := DecodeRepositoryReviewManagedEvidence(children, plan, options...); err == nil {
				t.Fatal("invalid child coverage was accepted")
			}
		})
	}
	oneFilePlan := repoaudit.Plan{PendingFiles: []repoaudit.FileRef{alpha}}
	requireError("invalid scope", []map[string]any{{"scope": 42}}, oneFilePlan)
	requireError("empty scope", []map[string]any{{"scope": []map[string]any{}}}, oneFilePlan)
	largeScope := make([]map[string]any, maxRepositoryReviewManagedEvidenceChildren+1)
	for index := range largeScope {
		largeScope[index] = repositoryReviewRecoveryScope(alpha)[0]
	}
	requireError("oversized scope", []map[string]any{{"scope": largeScope}}, oneFilePlan)
	requireError(
		"outside exact plan",
		[]map[string]any{{"scope": repositoryReviewRecoveryScope(beta)}},
		oneFilePlan,
	)
	requireError(
		"duplicate scope file",
		[]map[string]any{{"scope": repositoryReviewRecoveryScope(alpha, alpha)}},
		oneFilePlan,
	)
	requireError(
		"no required assignment",
		[]map[string]any{{
			"scope": repositoryReviewRecoveryScope(alpha), "required": false, "valid": false,
		}},
		oneFilePlan,
	)
	requireError(
		"inconsistent assignment count",
		[]map[string]any{
			{"scope": repositoryReviewRecoveryScope(alpha, beta), "valid": false},
			{"scope": repositoryReviewRecoveryScope(alpha), "valid": false},
		},
		repoaudit.Plan{PendingFiles: []repoaudit.FileRef{alpha, beta}},
	)
	tooManyAssignments := make([]map[string]any, maxRepositoryReviewManagedAssignments+1)
	for index := range tooManyAssignments {
		tooManyAssignments[index] = map[string]any{
			"scope": repositoryReviewRecoveryScope(alpha), "valid": false,
		}
	}
	requireError("too many assignments", tooManyAssignments, oneFilePlan)
	requireError(
		"assignment hint mismatch",
		[]map[string]any{{"scope": repositoryReviewRecoveryScope(alpha), "valid": false}},
		oneFilePlan,
		RepositoryReviewManagedEvidenceOptions{RequiredAssignments: 2},
	)
	requireError(
		"all-terminal child has no assignments",
		[]map[string]any{{
			"scope": repositoryReviewRecoveryScope(alpha), "required": false, "valid": false,
		}},
		oneFilePlan,
		RepositoryReviewManagedEvidenceOptions{
			TerminalUnsupportedFiles: []repoaudit.FileRef{alpha}, RequiredAssignments: 1,
		},
	)
	requireError(
		"successful child has no model",
		[]map[string]any{{
			"scope": repositoryReviewRecoveryScope(alpha), "valid": true,
			"structured": repositoryReviewRecoveryEmptyOutput(alpha.Path),
		}},
		oneFilePlan,
	)
}

func TestNativeValidateLegacyRepositoryReviewOutputStrictBranches(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(map[string]any)
	}{
		{name: "unknown root field", mutate: func(output map[string]any) { output["patch"] = "no" }},
		{name: "missing root field", mutate: func(output map[string]any) { delete(output, "summary") }},
		{name: "invalid summary", mutate: func(output map[string]any) { output["summary"] = 1 }},
		{name: "invalid reviewed files", mutate: func(output map[string]any) { output["reviewedFiles"] = nil }},
		{name: "invalid findings", mutate: func(output map[string]any) { output["findings"] = nil }},
		{name: "too many findings", mutate: func(output map[string]any) {
			finding := repositoryReviewRecoveryLegacyFinding("service.go")
			findings := make([]map[string]any, 257)
			for index := range findings {
				findings[index] = finding
			}
			output["findings"] = findings
		}},
		{name: "mixed match hints", mutate: func(output map[string]any) {
			repositoryReviewRecoveryFirstFinding(output)["match_hints"] = map[string]any{}
		}},
		{name: "mixed fix effort", mutate: func(output map[string]any) {
			repositoryReviewRecoveryFirstFinding(output)["fix_effort"] = map[string]any{}
		}},
		{name: "unknown finding field", mutate: func(output map[string]any) {
			repositoryReviewRecoveryFirstFinding(output)["recommendation"] = "fix it"
		}},
		{name: "missing finding field", mutate: func(output map[string]any) {
			delete(repositoryReviewRecoveryFirstFinding(output), "impact")
		}},
		{name: "invalid validation", mutate: func(output map[string]any) {
			repositoryReviewRecoveryFirstFinding(output)["validation"] = "confirmed"
		}},
		{name: "unknown validation field", mutate: func(output map[string]any) {
			repositoryReviewRecoveryValidation(output)["confidence"] = "high"
		}},
		{name: "missing validation field", mutate: func(output map[string]any) {
			delete(repositoryReviewRecoveryValidation(output), "summary")
		}},
		{name: "invalid checks", mutate: func(output map[string]any) {
			repositoryReviewRecoveryValidation(output)["checks"] = nil
		}},
		{name: "invalid core fields", mutate: func(output map[string]any) {
			repositoryReviewRecoveryFirstFinding(output)["severity"] = "urgent"
		}},
		{name: "oversized check", mutate: func(output map[string]any) {
			repositoryReviewRecoveryValidation(output)["checks"] = []string{strings.Repeat("x", 4097)}
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			output := repositoryReviewRecoveryLegacyOutput("service.go")
			test.mutate(output)
			if err := nativeValidateLegacyRepositoryReviewOutput(output); err == nil {
				t.Fatal("invalid legacy output was accepted")
			}
		})
	}
	if err := nativeValidateLegacyRepositoryReviewOutput(
		repositoryReviewRecoveryLegacyOutput("service.go"),
	); err != nil {
		t.Fatalf("valid legacy output rejected: %v", err)
	}
}

func TestNativeLegacyRepositoryReviewObservationValidatesAndFiltersEvidence(t *testing.T) {
	complete := repositoryReviewRecoveryFile("complete.go", "a")
	incomplete := repositoryReviewRecoveryFile("incomplete.go", "b")
	scope := repositoryReviewRecoveryScope(complete, incomplete)
	scope[1]["contentComplete"] = false
	output := repositoryReviewRecoveryLegacyOutput(complete.Path)
	output["reviewedFiles"] = []string{complete.Path, incomplete.Path}
	output["findings"] = []map[string]any{
		repositoryReviewRecoveryLegacyFinding(complete.Path),
		repositoryReviewRecoveryLegacyFinding(incomplete.Path),
	}
	observation, err := nativeLegacyRepositoryReviewObservation(
		output,
		scope,
		"legacy-model",
		"correctness",
		" raw response ",
	)
	if err != nil || len(observation.Findings) != 1 || observation.Findings[0].File != complete.Path ||
		observation.Model != "legacy-model" || len(observation.ScopeFiles) != 2 {
		t.Fatalf("legacy observation = %#v, err=%v", observation, err)
	}
	if _, err := nativeLegacyRepositoryReviewObservation(
		map[string]any{}, scope, "model", "reviewer", "raw",
	); err == nil {
		t.Fatal("invalid legacy output was accepted")
	}
	if _, err := nativeLegacyRepositoryReviewObservation(
		repositoryReviewRecoveryLegacyOutput(complete.Path), 42, "model", "reviewer", "raw",
	); err == nil {
		t.Fatal("invalid legacy scope was accepted")
	}
}

func TestRepositoryReviewRequiredAssignmentFallbackAndProfileBranches(t *testing.T) {
	if _, err := RepositoryBugFinderRequiredAssignments(nil, false); err == nil {
		t.Fatal("empty explicit reviewer ensemble was accepted")
	}
	canonical := NewRepositoryBugFinderProfileHashInput(
		" account ",
		" all ",
		"focus",
		`{}`,
		" scope ",
		" review-a, review-a ",
		" graph ",
		[]string{" review-a ", "review-a", " review-b "},
		false,
		1024,
	)
	if !reflect.DeepEqual(canonical.EffectiveModels, []string{"review-a", "review-b"}) ||
		canonical.Models != "review-a" || canonical.AccountRef != "account" ||
		canonical.Target != "all" || canonical.ScopePlanHash != "scope" ||
		canonical.ModelGraphRevision != "graph" {
		t.Fatalf("canonical profile input = %#v", canonical)
	}
	if _, err := RepositoryBugFinderProfileHash(canonical); err != nil {
		t.Fatalf("canonical profile rejected: %v", err)
	}
	if _, err := RepositoryBugFinderLegacyResolvedProfileHash(canonical); err != nil {
		t.Fatalf("canonical legacy profile rejected: %v", err)
	}
	invalid := canonical
	invalid.Target = ""
	if _, err := RepositoryBugFinderLegacyResolvedProfileHash(invalid); err == nil {
		t.Fatal("invalid legacy profile was accepted")
	}
	if values, ok := canonicalRepositoryBugFinderEffectiveModels([]string{""}, false); ok || values != nil {
		t.Fatalf("blank effective model canonicalized as %#v, ok=%v", values, ok)
	}
	if values, ok := canonicalRepositoryBugFinderEffectiveModels(
		[]string{"same", "same"},
		false,
	); ok || values != nil {
		t.Fatalf("duplicate strict effective models canonicalized as %#v, ok=%v", values, ok)
	}

	for _, test := range []struct {
		name      string
		requested int64
		maximum   int
		want      int64
		wantErr   bool
	}{
		{name: "invalid maximum", requested: 1, maximum: 0, wantErr: true},
		{name: "default maximum", requested: 0, maximum: 2048, want: 2048},
		{name: "clamped maximum", requested: 4096, maximum: 2048, want: 2048},
		{name: "requested value", requested: 1024, maximum: 2048, want: 1024},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, err := RepositoryBugFinderEffectiveMaxContentBytes(test.requested, test.maximum)
			if (err != nil) != test.wantErr || got != test.want {
				t.Fatalf("effective content bytes = %d, err=%v, want %d, err=%v", got, err, test.want, test.wantErr)
			}
		})
	}
}

func TestRecoverRepositoryReviewFrozenScopeRejectsUntrustedRecoveryData(t *testing.T) {
	commit, inventoryID, candidates, hardScope := repositoryReviewRecoveryCandidates(t)
	tests := []struct {
		name       string
		candidates any
		hardScope  any
		commit     string
		inventory  string
		paths      []string
	}{
		{
			name: "invalid candidate encoding", candidates: map[string]any{"bad": true},
			hardScope: hardScope, commit: commit, inventory: inventoryID, paths: []string{"alpha/router.go"},
		},
		{
			name: "invalid candidate", candidates: func() []reposcope.Candidate {
				values := append([]reposcope.Candidate(nil), candidates...)
				values[0].ID = "tampered"
				return values
			}(), hardScope: hardScope, commit: commit, inventory: inventoryID, paths: []string{"alpha/router.go"},
		},
		{
			name: "wrong commit", candidates: candidates, hardScope: hardScope,
			commit: strings.Repeat("b", 40), inventory: inventoryID, paths: []string{"alpha/router.go"},
		},
		{
			name: "wrong inventory", candidates: candidates, hardScope: hardScope,
			commit: commit, inventory: "other", paths: []string{"alpha/router.go"},
		},
		{
			name: "duplicate path",
			candidates: append(
				append([]reposcope.Candidate(nil), candidates...),
				candidates[0],
			),
			hardScope: hardScope, commit: commit, inventory: inventoryID, paths: []string{"alpha/router.go"},
		},
		{
			name: "noncanonical path", candidates: candidates, hardScope: hardScope,
			commit: commit, inventory: inventoryID, paths: []string{" alpha/router.go"},
		},
		{
			name: "duplicate selected path", candidates: candidates, hardScope: hardScope,
			commit: commit, inventory: inventoryID, paths: []string{"alpha/router.go", "alpha/router.go"},
		},
		{
			name: "unknown selected path", candidates: candidates, hardScope: hardScope,
			commit: commit, inventory: inventoryID, paths: []string{"missing.go"},
		},
		{
			name: "hard scope excludes selection", candidates: candidates,
			hardScope: map[string]any{"code_types": []string{"bench-test"}},
			commit:    commit, inventory: inventoryID, paths: []string{"alpha/router.go"},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, _, err := RecoverRepositoryReviewFrozenScope(
				test.candidates,
				test.hardScope,
				test.commit,
				test.inventory,
				test.paths,
			); err == nil {
				t.Fatal("untrusted recovered scope was accepted")
			}
		})
	}
}

func TestRecoverRepositoryReviewFrozenScopeMatchesNativeFilter(t *testing.T) {
	commit, inventoryID, candidates, hardScope := repositoryReviewRecoveryCandidates(t)
	paths := []string{"zeta/service.go", "alpha/router.go"}
	selection, plan, err := RecoverRepositoryReviewFrozenScope(
		candidates,
		hardScope,
		" "+strings.ToUpper(commit)+" ",
		" "+inventoryID+" ",
		paths,
	)
	if err != nil {
		t.Fatal(err)
	}
	ids := make([]string, 0, len(candidates))
	for _, candidate := range candidates {
		ids = append(ids, candidate.ID)
	}
	if ids[0] > ids[1] {
		ids[0], ids[1] = ids[1], ids[0]
	}
	filtered, err := nativeRepositoryEvaluationFilter(map[string]any{
		"candidates": candidates,
		"planner": map[string]any{
			"includePrefixes": []string{}, "excludePrefixes": []string{},
			"candidateIds": ids, "hotpathCandidateIds": []string{},
			"rationale": repositoryReviewLegacyScopeRecoveryRationale,
			"warnings":  []string{repositoryReviewLegacyScopeRecoveryWarning},
		},
		"scope_planned": false,
		"hard_scope":    hardScope,
		"commit":        commit,
	})
	if err != nil {
		t.Fatal(err)
	}
	wantSelection, err := nativeRepositoryEvaluationParseScopeSelection(filtered["scopeSelection"])
	if err != nil {
		t.Fatal(err)
	}
	wantPlan, err := nativeRepositoryEvaluationParseScopePlan(filtered["scopePlan"])
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(selection, wantSelection) || !reflect.DeepEqual(plan, wantPlan) {
		t.Fatalf("recovery = %#v %#v, native = %#v %#v", selection, plan, wantSelection, wantPlan)
	}
}

func repositoryReviewRecoveryFile(pathValue, hashCharacter string) repoaudit.FileRef {
	return repoaudit.FileRef{
		Path: pathValue, BlobSHA: strings.Repeat(hashCharacter, 40), SizeBytes: 10,
		Category: "code", Mode: "100644",
	}
}

func repositoryReviewRecoveryScope(files ...repoaudit.FileRef) []map[string]any {
	out := make([]map[string]any, 0, len(files))
	for _, file := range files {
		out = append(out, map[string]any{
			"path": file.Path, "fileHash": file.BlobSHA, "sizeBytes": file.SizeBytes,
			"category": file.Category, "mode": file.Mode, "contentComplete": true,
		})
	}
	return out
}

func repositoryReviewRecoveryEmptyOutput(paths ...string) map[string]any {
	return map[string]any{
		"summary": "reviewed", "reviewedFiles": paths,
		"findings": []map[string]any{}, "residualRisks": []string{},
	}
}

func repositoryReviewRecoveryLegacyOutput(pathValue string) map[string]any {
	return map[string]any{
		"summary": "legacy", "reviewedFiles": []string{pathValue},
		"findings":      []map[string]any{repositoryReviewRecoveryLegacyFinding(pathValue)},
		"residualRisks": []string{},
	}
}

func repositoryReviewRecoveryLegacyFinding(pathValue string) map[string]any {
	return map[string]any{
		"severity": "high", "title": "Lost state", "symbol": "Save", "file": pathValue,
		"message": "A write loses state.", "evidence": "The branch overwrites the value.",
		"impact": "A successful request disappears.",
		"validation": map[string]any{
			"status": "confirmed", "summary": "Traced the branch.", "checks": []string{"branch"},
		},
	}
}

func repositoryReviewRecoveryFirstFinding(output map[string]any) map[string]any {
	return output["findings"].([]map[string]any)[0]
}

func repositoryReviewRecoveryValidation(output map[string]any) map[string]any {
	return repositoryReviewRecoveryFirstFinding(output)["validation"].(map[string]any)
}

func repositoryReviewRecoveryCandidates(
	t *testing.T,
) (string, string, []reposcope.Candidate, repoaudit.RepositoryReviewScopePolicy) {
	t.Helper()
	commit := strings.Repeat("a", 40)
	inventory := reposcope.Inventory{
		CommitID: commit,
		ID:       "inventory",
		Files: []reposcope.FileMetadata{
			{
				Path: "zeta/service.go", BlobID: strings.Repeat("1", 40), Size: 100,
				Kind: reposcope.FileKindRegular, Sample: []byte("package zeta\n"),
			},
			{
				Path: "alpha/router.go", BlobID: strings.Repeat("2", 40), Size: 120,
				Kind: reposcope.FileKindRegular, Sample: []byte("package alpha\n"),
			},
		},
	}
	hardScope := repoaudit.RepositoryReviewScopePolicy{
		CodeTypes: []repoaudit.RepositoryReviewCodeType{repoaudit.RepositoryReviewCodeTypeCode},
	}
	candidates, rejected, err := reposcope.BuildCandidates(
		inventory,
		reposcope.Scope{CodeTypes: []reposcope.CodeType{reposcope.CodeTypeCode}},
		reposcope.BuildOptions{},
	)
	if err != nil || len(rejected) != 0 || len(candidates) != 2 {
		t.Fatalf("candidates = %#v, rejected=%#v, err=%v", candidates, rejected, err)
	}
	return commit, inventory.ID, candidates, hardScope
}

func TestRepositoryReviewRecoveryErrorsRemainClassifiable(t *testing.T) {
	commit, inventoryID, candidates, hardScope := repositoryReviewRecoveryCandidates(t)
	mutated := append([]reposcope.Candidate(nil), candidates...)
	mutated[0].ID = "tampered"
	_, _, err := RecoverRepositoryReviewFrozenScope(
		mutated,
		hardScope,
		commit,
		inventoryID,
		[]string{mutated[0].Path},
	)
	if !errors.Is(err, reposcope.ErrInvalidCandidate) {
		t.Fatalf("recovery error = %v", err)
	}
}
