package repoaudit

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
)

func TestRepositoryReviewScopePolicyDefaultsToProductionCode(t *testing.T) {
	store := newAutomationTestStore(t)
	automation := createAutomationForTest(t, store, "rra_scope_default", "Scope default")
	if !reflect.DeepEqual(automation.ScopePolicy.CodeTypes, []RepositoryReviewCodeType{
		RepositoryReviewCodeTypeHotpathCode,
		RepositoryReviewCodeTypeCode,
	}) {
		t.Fatalf("default scope code types = %#v", automation.ScopePolicy.CodeTypes)
	}
	if len(automation.ScopePolicy.IncludeFolders) != 0 ||
		len(automation.ScopePolicy.ExcludeFolders) != 0 || automation.ScopePolicy.FreeText != "" {
		t.Fatalf("default scope policy = %#v", automation.ScopePolicy)
	}
}

func TestNormalizeRepositoryReviewScopePolicyReturnsDetachedCanonicalPolicy(t *testing.T) {
	source := RepositoryReviewScopePolicy{
		CodeTypes:      []RepositoryReviewCodeType{RepositoryReviewCodeTypeBenchTest, RepositoryReviewCodeTypeCode},
		IncludeFolders: []string{"zeta", " alpha "},
		ExcludeFolders: []string{"zeta/generated"},
		FreeText:       " benchmark hot paths ",
	}
	normalized, err := NormalizeRepositoryReviewScopePolicy(source)
	if err != nil {
		t.Fatal(err)
	}
	source.CodeTypes[0] = RepositoryReviewCodeTypeTest
	source.IncludeFolders[0] = "changed"
	if !reflect.DeepEqual(normalized.CodeTypes, []RepositoryReviewCodeType{
		RepositoryReviewCodeTypeCode, RepositoryReviewCodeTypeBenchTest,
	}) || !reflect.DeepEqual(normalized.IncludeFolders, []string{"alpha", "zeta"}) ||
		normalized.FreeText != "benchmark hot paths" {
		t.Fatalf("canonical policy = %#v", normalized)
	}
	if _, err := NormalizeRepositoryReviewScopePolicy(RepositoryReviewScopePolicy{
		CodeTypes: []RepositoryReviewCodeType{"invalid"},
	}); !errors.Is(err, ErrInvalidAutomation) {
		t.Fatalf("invalid canonical policy error = %v", err)
	}
	if err := normalizeRepositoryReviewScopePolicy(nil); !errors.Is(err, ErrInvalidAutomation) {
		t.Fatalf("nil policy error = %v", err)
	}
	if err := normalizeRepositoryReviewScopePlan(nil); !errors.Is(err, ErrInvalidAutomation) {
		t.Fatalf("nil plan error = %v", err)
	}
	if repositoryReviewCodeTypeRank(RepositoryReviewCodeTypeBenchTest) != 3 ||
		repositoryReviewCodeTypeRank("unknown") != 4 {
		t.Fatal("code type rank boundary mismatch")
	}
}

func TestRepositoryReviewScopePolicyAndCommitPlanPersistCanonically(t *testing.T) {
	store := newAutomationTestStore(t)
	fixture := validAutomationForTest("rra_scope_persist", "Scope persist")
	fixture.ScopePolicy = RepositoryReviewScopePolicy{
		CodeTypes: []RepositoryReviewCodeType{
			" TEST ", RepositoryReviewCodeTypeCode, RepositoryReviewCodeTypeHotpathCode,
		},
		IncludeFolders: []string{" services/api ", "cmd"},
		ExcludeFolders: []string{"services/api/generated", " services/api "},
		FreeText:       " Focus on request authorization boundaries. ",
	}
	created, err := store.CreateAutomation(context.Background(), fixture)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(created.ScopePolicy.CodeTypes, []RepositoryReviewCodeType{
		RepositoryReviewCodeTypeHotpathCode,
		RepositoryReviewCodeTypeCode,
		RepositoryReviewCodeTypeTest,
	}) || !reflect.DeepEqual(created.ScopePolicy.IncludeFolders, []string{"cmd", "services/api"}) ||
		!reflect.DeepEqual(created.ScopePolicy.ExcludeFolders, []string{"services/api", "services/api/generated"}) ||
		created.ScopePolicy.FreeText != "Focus on request authorization boundaries." {
		t.Fatalf("normalized scope policy = %#v", created.ScopePolicy)
	}

	commit := strings.Repeat("A", 40)
	hash := strings.Repeat("B", 64)
	policyHash := strings.Repeat("C", 64)
	updated, err := store.UpdateAutomation(
		context.Background(), created.ID, created.Version,
		func(value *RepositoryReviewAutomation) error {
			value.ScopePlan = RepositoryReviewScopePlan{
				CommitSHA: commit, PolicyHash: policyHash, Hash: hash,
				Summary:   "  18 files selected  ",
				Rationale: "  Production and test files under the requested folders.  ",
				Warnings:  []string{" generated folder excluded ", "large files deferred"},
				Counts: RepositoryReviewScopePlanCounts{
					TotalFiles: 100, CodeTypeFiles: 60, IncludeFiles: 24,
					ExcludedFiles: 6, SelectedFiles: 18,
				},
			}
			return nil
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if updated.ScopePlan.CommitSHA != strings.ToLower(commit) ||
		updated.ScopePlan.PolicyHash != strings.ToLower(policyHash) ||
		updated.ScopePlan.Hash != strings.ToLower(hash) ||
		updated.ScopePlan.Summary != "18 files selected" ||
		updated.ScopePlan.Rationale != "Production and test files under the requested folders." ||
		!reflect.DeepEqual(updated.ScopePlan.Warnings, []string{"generated folder excluded", "large files deferred"}) {
		t.Fatalf("normalized scope plan = %#v", updated.ScopePlan)
	}
	loaded, found, err := store.GetAutomation(context.Background(), created.ID)
	if err != nil || !found || !reflect.DeepEqual(loaded.ScopePolicy, updated.ScopePolicy) ||
		!reflect.DeepEqual(loaded.ScopePlan, updated.ScopePlan) {
		t.Fatalf("reloaded automation found=%v err=%v value=%#v", found, err, loaded)
	}
}

func TestRepositoryReviewScopePolicyRejectsUnsafeOrUnboundedValues(t *testing.T) {
	tooManyPrefixes := make([]string, maxRepositoryReviewScopeFolders+1)
	for index := range tooManyPrefixes {
		tooManyPrefixes[index] = "folder-" + automationTestIndex(index)
	}
	tests := []struct {
		name   string
		policy RepositoryReviewScopePolicy
	}{
		{name: "unknown code type", policy: RepositoryReviewScopePolicy{CodeTypes: []RepositoryReviewCodeType{"docs"}}},
		{
			name:   "duplicate code type",
			policy: RepositoryReviewScopePolicy{CodeTypes: []RepositoryReviewCodeType{"code", " CODE "}},
		},
		{name: "absolute include", policy: RepositoryReviewScopePolicy{IncludeFolders: []string{"/etc"}}},
		{name: "parent include", policy: RepositoryReviewScopePolicy{IncludeFolders: []string{"../secret"}}},
		{name: "unclean include", policy: RepositoryReviewScopePolicy{IncludeFolders: []string{"src/../secret"}}},
		{name: "trailing slash", policy: RepositoryReviewScopePolicy{ExcludeFolders: []string{"generated/"}}},
		{name: "windows separator", policy: RepositoryReviewScopePolicy{ExcludeFolders: []string{`generated\code`}}},
		{name: "duplicate prefix", policy: RepositoryReviewScopePolicy{IncludeFolders: []string{"src", " src "}}},
		{name: "too many prefixes", policy: RepositoryReviewScopePolicy{IncludeFolders: tooManyPrefixes}},
		{
			name:   "free text",
			policy: RepositoryReviewScopePolicy{FreeText: strings.Repeat("x", maxRepositoryReviewScopeTextBytes+1)},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := validAutomationForTest("rra_scope_invalid", "Invalid scope")
			fixture.ScopePolicy = test.policy
			if err := normalizeAutomation(&fixture); !errors.Is(err, ErrInvalidAutomation) {
				t.Fatalf("normalize error = %v", err)
			}
		})
	}
}

func TestRepositoryReviewScopePlanRejectsPartialOrUnboundedValues(t *testing.T) {
	valid := RepositoryReviewScopePlan{
		CommitSHA:  strings.Repeat("a", 40),
		PolicyHash: strings.Repeat("b", 64),
		Hash:       strings.Repeat("c", 64),
		Summary:    "selected production code",
	}
	tests := []RepositoryReviewScopePlan{
		{CommitSHA: valid.CommitSHA},
		func() RepositoryReviewScopePlan { value := valid; value.CommitSHA = "not-a-sha"; return value }(),
		func() RepositoryReviewScopePlan { value := valid; value.PolicyHash = "short"; return value }(),
		func() RepositoryReviewScopePlan { value := valid; value.Summary = ""; return value }(),
		func() RepositoryReviewScopePlan {
			value := valid
			value.Warnings = make([]string, maxRepositoryReviewScopeWarnings+1)
			return value
		}(),
		func() RepositoryReviewScopePlan {
			value := valid
			value.Counts.SelectedFiles = maxReviewFiles + 1
			return value
		}(),
		func() RepositoryReviewScopePlan {
			value := valid
			value.Counts = RepositoryReviewScopePlanCounts{
				TotalFiles: 10, CodeTypeFiles: 8, IncludeFiles: 6,
				ExcludedFiles: 3, SelectedFiles: 4,
			}
			return value
		}(),
	}
	for index, plan := range tests {
		fixture := validAutomationForTest("rra_plan_invalid", "Invalid plan")
		fixture.ScopePlan = plan
		if err := normalizeAutomation(&fixture); !errors.Is(err, ErrInvalidAutomation) {
			t.Fatalf("invalid plan %d error = %v", index, err)
		}
	}
}

func TestRepositoryReviewScopeSelectionPersistsCanonicalAndDetached(t *testing.T) {
	store := newAutomationTestStore(t)
	firstID := repositoryReviewScopeCandidateID(1)
	secondID := repositoryReviewScopeCandidateID(2)
	fixture := validAutomationForTest("rra_scope_selection", "Frozen scope selection")
	fixture.ScopePlan = repositoryReviewScopeSelectionPlan()
	fixture.ScopeSelection = &RepositoryReviewScopeSelection{
		IncludePrefixes:     []string{" services/api/ ", "", "cmd"},
		ExcludePrefixes:     []string{"services/api/generated/", " vendor "},
		CandidateIDs:        []string{secondID, " " + firstID + " "},
		HotpathCandidateIDs: []string{secondID},
	}
	created, err := store.CreateAutomation(context.Background(), fixture)
	if err != nil {
		t.Fatal(err)
	}
	want := RepositoryReviewScopeSelection{
		IncludePrefixes:     []string{".", "cmd", "services/api"},
		ExcludePrefixes:     []string{"services/api/generated", "vendor"},
		CandidateIDs:        []string{firstID, secondID},
		HotpathCandidateIDs: []string{secondID},
	}
	normalized, err := NormalizeRepositoryReviewScopeSelection(*fixture.ScopeSelection)
	if err != nil || !reflect.DeepEqual(normalized, want) {
		t.Fatalf("exported scope selection normalization=%#v err=%v", normalized, err)
	}
	if _, normalizeErr := NormalizeRepositoryReviewScopeSelection(RepositoryReviewScopeSelection{
		CandidateIDs: []string{"invalid"},
	}); !errors.Is(normalizeErr, ErrInvalidAutomation) {
		t.Fatalf("exported invalid selection error=%v", normalizeErr)
	}
	invalidAutomation := validAutomationForTest("rra_invalid_frozen_selection", "Invalid selection")
	invalidAutomation.ScopePlan = repositoryReviewScopeSelectionPlan()
	invalidAutomation.ScopeSelection = &RepositoryReviewScopeSelection{
		CandidateIDs: []string{"invalid"},
	}
	if automationErr := normalizeAutomation(&invalidAutomation); !errors.Is(automationErr, ErrInvalidAutomation) {
		t.Fatalf("automation invalid frozen selection error=%v", automationErr)
	}
	plan, err := NormalizeRepositoryReviewScopePlan(repositoryReviewScopeSelectionPlan())
	if err != nil || plan.Hash != repositoryReviewScopeSelectionPlan().Hash {
		t.Fatalf("exported scope plan normalization=%#v err=%v", plan, err)
	}
	invalidPlan := repositoryReviewScopeSelectionPlan()
	invalidPlan.Hash = "invalid"
	if _, planErr := NormalizeRepositoryReviewScopePlan(invalidPlan); !errors.Is(planErr, ErrInvalidAutomation) {
		t.Fatalf("exported invalid scope plan error=%v", planErr)
	}
	if created.ScopeSelection == nil || !reflect.DeepEqual(*created.ScopeSelection, want) {
		t.Fatalf("canonical scope selection = %#v, want %#v", created.ScopeSelection, want)
	}

	fixture.ScopeSelection.IncludePrefixes[0] = "changed"
	fixture.ScopeSelection.CandidateIDs[0] = repositoryReviewScopeCandidateID(3)
	created.ScopeSelection.IncludePrefixes[0] = "also-changed"
	created.ScopeSelection.CandidateIDs[0] = repositoryReviewScopeCandidateID(4)
	loaded, found, err := store.GetAutomation(context.Background(), fixture.ID)
	if err != nil || !found || loaded.ScopeSelection == nil ||
		!reflect.DeepEqual(*loaded.ScopeSelection, want) {
		t.Fatalf("detached persisted scope selection found=%v err=%v value=%#v", found, err, loaded.ScopeSelection)
	}

	clone := cloneAutomation(loaded)
	clone.ScopeSelection.ExcludePrefixes[0] = "changed"
	clone.ScopeSelection.HotpathCandidateIDs[0] = firstID
	if reflect.DeepEqual(clone.ScopeSelection, loaded.ScopeSelection) ||
		loaded.ScopeSelection.ExcludePrefixes[0] != want.ExcludePrefixes[0] ||
		loaded.ScopeSelection.HotpathCandidateIDs[0] != secondID {
		t.Fatalf(
			"scope selection clone aliases source: clone=%#v source=%#v",
			clone.ScopeSelection,
			loaded.ScopeSelection,
		)
	}
}

func TestRepositoryReviewScopeSelectionRequiresPlanButLegacyPlanRemainsValid(t *testing.T) {
	store := newAutomationTestStore(t)
	withoutPlan := validAutomationForTest("rra_selection_without_plan", "Selection without plan")
	withoutPlan.ScopeSelection = &RepositoryReviewScopeSelection{}
	if _, err := store.CreateAutomation(context.Background(), withoutPlan); !errors.Is(err, ErrInvalidAutomation) {
		t.Fatalf("selection without plan error = %v", err)
	}

	legacy := validAutomationForTest("rra_legacy_scope_plan", "Legacy scope plan")
	legacy.ScopePlan = repositoryReviewScopeSelectionPlan()
	created, err := store.CreateAutomation(context.Background(), legacy)
	if err != nil {
		t.Fatalf("legacy plan without frozen selection error = %v", err)
	}
	if created.ScopeSelection != nil {
		t.Fatalf("legacy plan acquired unexpected selection %#v", created.ScopeSelection)
	}
	encoded, err := json.Marshal(created)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), `"scope_selection"`) {
		t.Fatalf("nil internal selection changed legacy JSON: %s", encoded)
	}
}

func TestRepositoryReviewScopeSelectionRejectsUnsafeOrUnboundedValues(t *testing.T) {
	tooManyPrefixes := make([]string, maxRepositoryReviewSelectionPrefixes+1)
	for index := range tooManyPrefixes {
		tooManyPrefixes[index] = fmt.Sprintf("folder-%d", index)
	}
	tooManyIDs := make([]string, maxRepositoryReviewSelectionCandidates+1)
	for index := range tooManyIDs {
		tooManyIDs[index] = repositoryReviewScopeCandidateID(index)
	}
	validID := repositoryReviewScopeCandidateID(1)
	atLimits := &RepositoryReviewScopeSelection{
		IncludePrefixes:     tooManyPrefixes[:maxRepositoryReviewSelectionPrefixes],
		CandidateIDs:        tooManyIDs[:maxRepositoryReviewSelectionCandidates],
		HotpathCandidateIDs: tooManyIDs[:maxRepositoryReviewSelectionCandidates],
	}
	if err := normalizeRepositoryReviewScopeSelection(atLimits); err != nil {
		t.Fatalf("selection at limits error = %v", err)
	}
	tests := []struct {
		name      string
		selection *RepositoryReviewScopeSelection
	}{
		{name: "nil", selection: nil},
		{name: "too many includes", selection: &RepositoryReviewScopeSelection{IncludePrefixes: tooManyPrefixes}},
		{name: "too many candidates", selection: &RepositoryReviewScopeSelection{CandidateIDs: tooManyIDs}},
		{name: "too many hotpaths", selection: &RepositoryReviewScopeSelection{HotpathCandidateIDs: tooManyIDs}},
		{name: "parent prefix", selection: &RepositoryReviewScopeSelection{IncludePrefixes: []string{"../secret"}}},
		{name: "absolute prefix", selection: &RepositoryReviewScopeSelection{ExcludePrefixes: []string{"/etc"}}},
		{
			name: "unclean prefix",
			selection: &RepositoryReviewScopeSelection{
				IncludePrefixes: []string{"src/../secret"},
			},
		},
		{name: "control prefix", selection: &RepositoryReviewScopeSelection{IncludePrefixes: []string{"src\nprivate"}}},
		{
			name: "oversized prefix",
			selection: &RepositoryReviewScopeSelection{
				IncludePrefixes: []string{
					strings.Repeat("x", maxRepositoryReviewScopePrefixBytes+1),
				},
			},
		},
		{
			name: "duplicate canonical prefix",
			selection: &RepositoryReviewScopeSelection{
				IncludePrefixes: []string{"src", " src/ "},
			},
		},
		{
			name: "malformed candidate",
			selection: &RepositoryReviewScopeSelection{
				CandidateIDs: []string{"cand_unknown"},
			},
		},
		{
			name: "uppercase candidate",
			selection: &RepositoryReviewScopeSelection{
				CandidateIDs: []string{"cand_" + strings.Repeat("A", 64)},
			},
		},
		{
			name: "duplicate candidate",
			selection: &RepositoryReviewScopeSelection{
				CandidateIDs: []string{validID, " " + validID + " "},
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if err := normalizeRepositoryReviewScopeSelection(test.selection); !errors.Is(err, ErrInvalidAutomation) {
				t.Fatalf("normalize error = %v", err)
			}
		})
	}
}

func repositoryReviewScopeSelectionPlan() RepositoryReviewScopePlan {
	return RepositoryReviewScopePlan{
		CommitSHA: strings.Repeat("a", 40), PolicyHash: strings.Repeat("b", 64),
		Hash: strings.Repeat("c", 64), Summary: "frozen repository scope",
	}
}

func repositoryReviewScopeCandidateID(index int) string {
	return fmt.Sprintf("cand_%064x", index)
}
