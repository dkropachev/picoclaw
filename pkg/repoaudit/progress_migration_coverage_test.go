package repoaudit

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
)

func TestAutomationLoadMigratesLegacyFindingProgressAliases(t *testing.T) {
	t.Skip("legacy JSON rewrite test replaced by first-open SQLite migration coverage")
	store := newAutomationTestStore(t)
	automation := createAutomationForTest(t, store, "rra_legacy_finding_progress", "Legacy progress")
	path := store.automationPath(automation.ID)
	encoded, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var persisted map[string]any
	if unmarshalErr := json.Unmarshal(encoded, &persisted); unmarshalErr != nil {
		t.Fatal(unmarshalErr)
	}
	progress := persisted["progress"].(map[string]any)
	progress["findings"] = float64(3)
	delete(progress, "raw_findings")
	delete(progress, "deduplicated_findings")
	encoded, err = json.Marshal(persisted)
	if err != nil {
		t.Fatal(err)
	}
	if writeErr := os.WriteFile(path, encoded, 0o600); writeErr != nil {
		t.Fatal(writeErr)
	}

	loaded, found, err := store.GetAutomation(context.Background(), automation.ID)
	if err != nil || !found || loaded.Progress.RawFindings != 0 ||
		loaded.Progress.DeduplicatedFindings != 3 || loaded.Progress.Findings != 3 {
		t.Fatalf("loaded=%#v found=%v err=%v", loaded.Progress, found, err)
	}
	rewritten, err := os.ReadFile(path)
	if err != nil || !strings.Contains(string(rewritten), `"raw_findings":0`) ||
		!strings.Contains(string(rewritten), `"deduplicated_findings":3`) {
		t.Fatalf("rewritten=%s err=%v", rewritten, err)
	}
}

func TestNormalizeAutomationBackfillsDeprecatedFindingAlias(t *testing.T) {
	store := newAutomationTestStore(t)
	automation := createAutomationForTest(t, store, "rra_deduplicated_alias", "Alias")
	automation.Progress.Findings = 0
	automation.Progress.DeduplicatedFindings = 4
	if err := normalizeAutomation(&automation); err != nil {
		t.Fatal(err)
	}
	if automation.Progress.Findings != 4 || automation.Progress.DeduplicatedFindings != 4 {
		t.Fatalf("progress=%#v", automation.Progress)
	}
}

func TestAutomationSaveRejectsOversizedCanonicalEncoding(t *testing.T) {
	store := newAutomationTestStore(t)
	automation := createAutomationForTest(t, store, "rra_oversized_encoding", "Oversized")

	automation.ReviewFocus = strings.Repeat("x\n", maxFindingTextBytes/2)
	automation.Target = strings.Repeat("x\n", 2_048)
	automation.RunIDs = make([]string, maxAutomationRunIDs)
	for index := range automation.RunIDs {
		automation.RunIDs[index] = strings.Repeat(`"`, 1_020) + fmt.Sprintf("%04d", index)
	}

	automation.ReviewerModels = make([]string, maxAutomationReviewers)
	automation.ModelCoverageSketches = make(map[string]string, maxAutomationReviewers)
	sketch := base64.RawStdEncoding.EncodeToString(make([]byte, automationModelCoverageSketchBytes))
	for index := range automation.ReviewerModels {
		alias := strings.Repeat(`"`, 252) + fmt.Sprintf("%04d", index)
		automation.ReviewerModels[index] = alias
		automation.ModelCoverageSketches[alias] = sketch
	}
	automation.IssueWriterModel = automation.ReviewerModels[0]
	automation.ModelPrices = nil
	automation.ModelStats = nil

	automation.AccountLimitSnapshots = make([]RepositoryReviewAccountLimitSnapshot, maxAutomationAccountSnapshots)
	for index := range automation.AccountLimitSnapshots {
		automation.AccountLimitSnapshots[index] = RepositoryReviewAccountLimitSnapshot{
			AccountID: strings.Repeat(`"`, 1_020) + fmt.Sprintf("%04d", index),
			Name:      strings.Repeat(`"`, 256),
			Window:    strings.Repeat(`"`, 64),
			Detail:    strings.Repeat(`"`, 1_024),
			CheckedAt: automationTestNow,
		}
	}

	automation.ScopePolicy.FreeText = strings.Repeat("x\n", maxRepositoryReviewScopeTextBytes/2)
	automation.ScopePolicy.IncludeFolders = oversizedUniquePaths(maxRepositoryReviewScopeFolders)
	automation.ScopePolicy.ExcludeFolders = oversizedUniquePaths(maxRepositoryReviewScopeFolders)
	automation.ScopePlan = repositoryReviewScopeSelectionPlan()
	automation.ScopePlan.Rationale = strings.Repeat("x\n", maxRepositoryReviewScopeRationaleBytes/2)
	automation.ScopePlan.Warnings = make([]string, maxRepositoryReviewScopeWarnings)
	for index := range automation.ScopePlan.Warnings {
		automation.ScopePlan.Warnings[index] = strings.Repeat("x\n", maxRepositoryReviewScopeWarningBytes/2-2) +
			fmt.Sprintf("%04d", index)
	}
	automation.ScopeSelection = &RepositoryReviewScopeSelection{
		IncludePrefixes:     oversizedUniquePaths(maxRepositoryReviewSelectionPrefixes),
		ExcludePrefixes:     oversizedUniquePaths(maxRepositoryReviewSelectionPrefixes),
		CandidateIDs:        oversizedCandidateIDs(),
		HotpathCandidateIDs: oversizedCandidateIDs(),
	}

	if err := store.saveAutomation(automation); err == nil ||
		!strings.Contains(err.Error(), "exceeds its size limit") {
		t.Fatalf("oversized save error=%v", err)
	}
}

func oversizedUniquePaths(count int) []string {
	paths := make([]string, count)
	for index := range paths {
		paths[index] = strings.Repeat(`"`, maxRepositoryReviewScopePrefixBytes-5) +
			fmt.Sprintf("%05d", index)
	}
	return paths
}

func oversizedCandidateIDs() []string {
	ids := make([]string, maxRepositoryReviewSelectionCandidates)
	for index := range ids {
		ids[index] = fmt.Sprintf("cand_%064x", index)
	}
	return ids
}
