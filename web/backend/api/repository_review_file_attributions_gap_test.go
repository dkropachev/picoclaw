package api

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/sipeed/picoclaw/pkg/repoaudit"
)

func TestRepositoryReviewCanonicalFileAttributionRouteSuggestions(t *testing.T) {
	handler, mux, workspace := newRepositoryReviewAutomationTestHandler(t)
	t.Cleanup(handler.Shutdown)
	state := seedRepositoryReviewAPIState(t, workspace)
	store, err := handler.repositoryReviewStore()
	if err != nil {
		t.Fatal(err)
	}
	automation := testRepositoryReviewAutomation()
	automation.ID = "rra_api_seed"
	automation.Repository = state.Repository
	automation.RunIDs = []string{state.Runs[0].ID}
	if _, err = store.CreateAutomation(t.Context(), automation); err != nil {
		t.Fatal(err)
	}
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, httptest.NewRequest(
		http.MethodGet,
		"/api/repository-reviews/automations/"+automation.ID+"/file-attributions?query=ALL",
		nil,
	))
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "pkg/service.go") {
		t.Fatalf("file attributions=%d %s", response.Code, response.Body.String())
	}

	options := repositoryReviewFileAttributionPageOptions("canonical-attribution")
	if _, ok := options.Resolve(repositoryReviewFileAttributionSummary{}, "path", time.Now()); !ok {
		t.Fatal("file attribution page resolver did not delegate")
	}
}

func TestRepositoryReviewFileAttributionSummariesKeepProvenanceDistinct(t *testing.T) {
	completedAt := time.Date(2026, 8, 25, 1, 0, 0, 0, time.UTC)
	file := repoaudit.FileRef{
		Path: "pkg/store.go", BlobSHA: strings.Repeat("a", 40),
		SizeBytes: 240, Category: "code", Mode: "100644",
	}
	base := repositoryReviewFileAttributionForCanonicalAPI(
		t, "rra_summary_distinct", "run-one", repoaudit.RepositoryReviewFocusSecurityTrust,
		"assignment-security", 1, completedAt, file,
	)
	inputs := make([]repoaudit.RepositoryReviewFileAttribution, 0, 4)
	inputs = append(inputs, base)
	for index, mutate := range []func(*repoaudit.RepositoryReviewFileAttribution){
		func(value *repoaudit.RepositoryReviewFileAttribution) {
			value.RootAgentID = "secondary"
		},
		func(value *repoaudit.RepositoryReviewFileAttribution) {
			value.Account = "other-account"
		},
		func(value *repoaudit.RepositoryReviewFileAttribution) {
			value.CommitSHA = strings.Repeat("e", 40)
			value.AcknowledgedFiles[0].BlobSHA = strings.Repeat("f", 40)
		},
	} {
		candidate := base
		candidate.ID = ""
		candidate.AcknowledgedFiles = append([]repoaudit.FileRef(nil), base.AcknowledgedFiles...)
		candidate.ChildIndex = index + 2
		mutate(&candidate)
		normalized, err := repoaudit.NewRepositoryReviewFileAttribution(candidate)
		if err != nil {
			t.Fatal(err)
		}
		inputs = append(inputs, normalized)
	}

	summaries := repositoryReviewFileAttributionSummaries(
		repoaudit.RepositoryReviewAutomation{ID: "rra_summary_distinct"}, inputs,
	)
	if len(summaries) != 4 {
		t.Fatalf("provenance-distinct summaries=%#v", summaries)
	}
	seen := make(map[string]struct{}, len(summaries))
	for _, summary := range summaries {
		if _, duplicate := seen[summary.ID]; duplicate {
			t.Fatalf("duplicate summary cursor ID: %#v", summary)
		}
		seen[summary.ID] = struct{}{}
	}
}

func TestRepositoryReviewFileAttributionSummariesCoalesceSourcesAndCapRunSample(t *testing.T) {
	completedAt := time.Date(2026, 8, 25, 1, 0, 0, 0, time.UTC)
	file := repoaudit.FileRef{
		Path: "pkg/store.go", BlobSHA: strings.Repeat("a", 40),
		SizeBytes: 240, Category: "code", Mode: "100644",
	}
	automationID := "rra_summary_mixed"
	inputs := make([]repoaudit.RepositoryReviewFileAttribution, 0, 21)
	for index := range 21 {
		attribution := repositoryReviewFileAttributionForCanonicalAPI(
			t, automationID, fmt.Sprintf("run-%02d", index+1),
			repoaudit.RepositoryReviewFocusSecurityTrust,
			"assignment-security", index+1, completedAt.Add(time.Duration(index)*time.Minute), file,
		)
		inputs = append(inputs, attribution)
	}

	summaries := repositoryReviewFileAttributionSummaries(
		repoaudit.RepositoryReviewAutomation{ID: automationID}, inputs,
	)
	if len(summaries) != 1 {
		t.Fatalf("mixed-source summaries=%#v", summaries)
	}
	summary := summaries[0]
	if summary.ID == "" || summary.Model != "openai/gpt-5.6-sol" ||
		summary.Source != "live" || len(summary.Sources) != 1 ||
		summary.Attempts != 21 || summary.RunCount != 21 ||
		len(summary.RunIDs) != maxRepositoryReviewFileAttributionRunSample {
		t.Fatalf("mixed-source summary=%#v", summary)
	}
	model, ok := repositoryReviewFileAttributionCollectionField(summary, "model")
	if !ok || model.Text != summary.Model {
		t.Fatalf("model field=%#v ok=%v", model, ok)
	}
	source, ok := repositoryReviewFileAttributionCollectionField(summary, "source")
	if !ok || source.Text != "live" {
		t.Fatalf("source field=%#v ok=%v", source, ok)
	}
}

func repositoryReviewFileAttributionForCanonicalAPI(
	t *testing.T,
	automationID string,
	runID string,
	focusID string,
	assignmentID string,
	childIndex int,
	completedAt time.Time,
	file repoaudit.FileRef,
) repoaudit.RepositoryReviewFileAttribution {
	t.Helper()
	attribution, err := repoaudit.NewRepositoryReviewFileAttribution(
		repoaudit.RepositoryReviewFileAttribution{
			AutomationID: automationID, RunID: runID, CommitSHA: strings.Repeat("c", 40),
			InventoryHash: "inventory", ProfileHash: "profile",
			AssignmentID: assignmentID, FocusID: focusID,
			RootAgentID: "main", ReviewerIdentity: "review",
			Model: "openai/gpt-5.6-sol", ModelAlias: "review", Account: "review-account",
			UsageModel: "openai/gpt-5.6-sol", AcknowledgedFiles: []repoaudit.FileRef{file},
			EvidenceDigest: "sha256:" + strings.Repeat("d", 64),
			Source:         repoaudit.RepositoryReviewFileAttributionSourceLiveCheckpoint,
			ChildIndex:     childIndex, Required: true, CompletedAt: completedAt,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	return attribution
}
