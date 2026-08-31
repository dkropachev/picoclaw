package api

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/sipeed/picoclaw/pkg/collectionquery"
	"github.com/sipeed/picoclaw/pkg/repoaudit"
)

const maxRepositoryReviewFileAttributionRunSample = 20

var repositoryReviewFileAttributionCollectionSchema = mustCollectionQuerySchema(
	[]collectionquery.FieldSchema{
		{Name: "path", Type: collectionquery.TypeString, Sortable: true},
		{Name: "commit", Type: collectionquery.TypeString, Sortable: true},
		{Name: "blob", Type: collectionquery.TypeString, Sortable: true},
		{
			Name: "focus", Type: collectionquery.TypeEnum, Sortable: true,
			SuggestedValues: []string{
				repoaudit.RepositoryReviewFocusCorrectnessState,
				repoaudit.RepositoryReviewFocusSecurityTrust,
				repoaudit.RepositoryReviewFocusConcurrencyRecovery,
				repoaudit.RepositoryReviewFocusIntegrationValidation,
			},
		},
		{Name: "agent", Type: collectionquery.TypeString, Sortable: true},
		{Name: "reviewer", Type: collectionquery.TypeString, Sortable: true},
		{Name: "account", Type: collectionquery.TypeString, Sortable: true},
		{Name: "model", Type: collectionquery.TypeString, Sortable: true},
		{
			Name: "source", Type: collectionquery.TypeEnum, Sortable: true,
			SuggestedValues: []string{"legacy", "live", "mixed"},
		},
		{Name: "attempts", Type: collectionquery.TypeNumber, Sortable: true},
		{Name: "runs", Type: collectionquery.TypeNumber, Sortable: true},
		{Name: "latest", Type: collectionquery.TypeTimestamp, Sortable: true},
	},
	[]collectionquery.SortField{
		{Field: "path", Direction: collectionquery.Ascending},
		{Field: "focus", Direction: collectionquery.Ascending},
		{Field: "reviewer", Direction: collectionquery.Ascending},
	},
)

// repositoryReviewFileAttributionSummary is one exact file revision, focus,
// root-agent, reviewer/model, and account row. Attempts deliberately count
// successful persisted assignment results, while RunIDs are distinct so
// retries in one workflow run remain visible.
type repositoryReviewFileAttributionSummary struct {
	latestRecordID    string
	runIDs            map[string]struct{}
	sourceSet         map[string]struct{}
	ID                string    `json:"id"`
	Path              string    `json:"path"`
	CommitSHA         string    `json:"commit_sha"`
	BlobSHA           string    `json:"blob_sha"`
	FocusID           string    `json:"focus_id"`
	RootAgentID       string    `json:"root_agent_id,omitempty"`
	ReviewerIdentity  string    `json:"reviewer_identity,omitempty"`
	Account           string    `json:"account,omitempty"`
	Model             string    `json:"model,omitempty"`
	Source            string    `json:"source"`
	Sources           []string  `json:"sources"`
	Attempts          int       `json:"attempts"`
	RunIDs            []string  `json:"run_ids"`
	RunCount          int       `json:"run_count"`
	LatestCompletedAt time.Time `json:"latest_completed_at"`
}

func (h *Handler) handleListRepositoryReviewFileAttributionsCollection(
	w http.ResponseWriter,
	r *http.Request,
) {
	listRequest, ok := parseCollectionListRequest(
		w, r, repositoryReviewFileAttributionCollectionSchema,
	)
	if !ok {
		return
	}
	ledger, err := h.repositoryReviewAutomationLedger(
		r.Context(), r.PathValue("automation_id"),
	)
	if err != nil {
		writeRepositoryReviewAutomationError(w, err)
		return
	}
	summaries := repositoryReviewFileAttributionSummaries(
		ledger.Automation, ledger.State.FileAttributions,
	)
	contextID := repositoryReviewCollectionCursorContext(
		"file-attributions", ledger.Automation.ID,
	)
	page, pageErr := collectionquery.Paginate(
		summaries,
		listRequest.Query,
		listRequest.Cursor,
		listRequest.Limit,
		listRequest.Now,
		repositoryReviewFileAttributionPageOptions(contextID),
	)
	if pageErr != nil {
		writeCollectionPageError(w, pageErr)
		return
	}
	paths, commits, blobs := []string{}, []string{}, []string{}
	agents, reviewers, accounts, models := []string{}, []string{}, []string{}, []string{}
	for _, attribution := range summaries {
		appendRepositoryReviewAttributionSuggestion(&paths, attribution.Path)
		appendRepositoryReviewAttributionSuggestion(&commits, attribution.CommitSHA)
		appendRepositoryReviewAttributionSuggestion(&blobs, attribution.BlobSHA)
		appendRepositoryReviewAttributionSuggestion(&agents, attribution.RootAgentID)
		appendRepositoryReviewAttributionSuggestion(&reviewers, attribution.ReviewerIdentity)
		appendRepositoryReviewAttributionSuggestion(&accounts, attribution.Account)
		appendRepositoryReviewAttributionSuggestion(&models, attribution.Model)
	}
	response := map[string]any{
		"automation":        projectRepositoryReviewAutomation(ledger.Automation),
		"file_attributions": page.Items,
		"total":             page.Total,
		"next_cursor":       page.NextCursor,
		"canonical_query":   listRequest.Query.Canonical(),
		"query_schema": collectionSchemaWithSuggestions(
			repositoryReviewFileAttributionCollectionSchema,
			map[collectionquery.Field][]string{
				"path": paths, "commit": commits, "blob": blobs,
				"agent": agents, "reviewer": reviewers, "account": accounts,
				"model": models,
			},
		),
	}
	if ledger.Found {
		response["repository"] = repoaudit.Summarize(ledger.State)
	}
	writeRepositoryReviewJSON(w, http.StatusOK, response)
}

func repositoryReviewFileAttributionSummaries(
	automation repoaudit.RepositoryReviewAutomation,
	records []repoaudit.RepositoryReviewFileAttribution,
) []repositoryReviewFileAttributionSummary {
	grouped := make(map[string]*repositoryReviewFileAttributionSummary)
	for _, record := range records {
		if record.AutomationID != automation.ID {
			continue
		}
		effectiveModel := repositoryReviewFileAttributionEffectiveModel(record)
		seenPaths := make(map[string]struct{}, len(record.AcknowledgedFiles))
		for _, file := range record.AcknowledgedFiles {
			path := strings.TrimSpace(file.Path)
			if path == "" {
				continue
			}
			if _, duplicate := seenPaths[path]; duplicate {
				continue
			}
			seenPaths[path] = struct{}{}
			key := repositoryReviewFileAttributionGroupKey(file, record, effectiveModel)
			summary := grouped[key]
			if summary == nil {
				digest := sha256.Sum256([]byte(key))
				summary = &repositoryReviewFileAttributionSummary{
					ID: hex.EncodeToString(digest[:]), Path: path,
					CommitSHA: record.CommitSHA, BlobSHA: file.BlobSHA, FocusID: record.FocusID,
					RootAgentID:      record.RootAgentID,
					ReviewerIdentity: record.ReviewerIdentity,
					Account:          record.Account,
					Model:            effectiveModel,
					runIDs:           make(map[string]struct{}), sourceSet: make(map[string]struct{}),
				}
				grouped[key] = summary
			}
			summary.Attempts++
			summary.runIDs[record.RunID] = struct{}{}
			if source := strings.TrimSpace(string(record.Source)); source != "" {
				summary.sourceSet[source] = struct{}{}
			}
			if summary.LatestCompletedAt.Before(record.CompletedAt) ||
				(summary.LatestCompletedAt.Equal(record.CompletedAt) && summary.latestRecordID < record.ID) {
				summary.LatestCompletedAt = record.CompletedAt
				summary.latestRecordID = record.ID
			}
		}
	}
	out := make([]repositoryReviewFileAttributionSummary, 0, len(grouped))
	for _, summary := range grouped {
		allRunIDs := sortedRepositoryReviewAttributionSet(summary.runIDs)
		summary.RunCount = len(allRunIDs)
		summary.RunIDs = allRunIDs[:min(len(allRunIDs), maxRepositoryReviewFileAttributionRunSample)]
		summary.Sources = sortedRepositoryReviewAttributionSet(summary.sourceSet)
		summary.Source = repositoryReviewFileAttributionSourceClass(summary.sourceSet)
		summary.runIDs = nil
		summary.sourceSet = nil
		out = append(out, *summary)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

func repositoryReviewFileAttributionGroupKey(
	file repoaudit.FileRef,
	record repoaudit.RepositoryReviewFileAttribution,
	effectiveModel string,
) string {
	key, _ := json.Marshal([]string{
		file.Path, file.BlobSHA, record.CommitSHA, record.FocusID,
		record.RootAgentID, record.ReviewerIdentity, effectiveModel, record.Account,
	})
	return string(key)
}

func repositoryReviewFileAttributionEffectiveModel(
	record repoaudit.RepositoryReviewFileAttribution,
) string {
	if usageModel := strings.TrimSpace(record.UsageModel); usageModel != "" {
		return usageModel
	}
	return strings.TrimSpace(record.Model)
}

func repositoryReviewFileAttributionSourceClass(values map[string]struct{}) string {
	_, legacy := values[string(repoaudit.RepositoryReviewFileAttributionSourceLegacyManagedChild)]
	_, live := values[string(repoaudit.RepositoryReviewFileAttributionSourceLiveCheckpoint)]
	if legacy && live {
		return "mixed"
	}
	if live {
		return "live"
	}
	return "legacy"
}

func appendRepositoryReviewAttributionSuggestion(values *[]string, value string) {
	value = strings.TrimSpace(value)
	if value == "" {
		return
	}
	for _, existing := range *values {
		if strings.EqualFold(existing, value) {
			return
		}
	}
	if len(*values) >= collectionquery.MaxSuggestedValues {
		return
	}
	*values = append(*values, value)
}

func sortedRepositoryReviewAttributionSet(values map[string]struct{}) []string {
	out := make([]string, 0, len(values))
	for value := range values {
		out = append(out, value)
	}
	sort.Strings(out)
	return out
}

func repositoryReviewFileAttributionPageOptions(
	contextID string,
) collectionquery.PageOptions[repositoryReviewFileAttributionSummary] {
	return collectionquery.PageOptions[repositoryReviewFileAttributionSummary]{
		ID: func(attribution repositoryReviewFileAttributionSummary) (string, error) {
			return repositoryReviewCollectionCursorItemID(contextID, attribution.ID)
		},
		ValidateID: repositoryReviewCollectionCursorIDValidator(contextID),
		Resolve: func(
			attribution repositoryReviewFileAttributionSummary,
			field collectionquery.Field,
			_ time.Time,
		) (collectionquery.FieldValue, bool) {
			return repositoryReviewFileAttributionCollectionField(attribution, field)
		},
	}
}

func repositoryReviewFileAttributionCollectionField(
	attribution repositoryReviewFileAttributionSummary,
	field collectionquery.Field,
) (collectionquery.FieldValue, bool) {
	switch field {
	case "path":
		return collectionquery.StringValue(attribution.Path), true
	case "commit":
		return collectionquery.StringValue(attribution.CommitSHA), true
	case "blob":
		return collectionquery.StringValue(attribution.BlobSHA), true
	case "focus":
		return collectionquery.EnumValue(attribution.FocusID), true
	case "agent":
		return collectionquery.StringValue(attribution.RootAgentID), true
	case "reviewer":
		return collectionquery.StringValue(attribution.ReviewerIdentity), true
	case "account":
		return collectionquery.StringValue(attribution.Account), true
	case "model":
		return collectionquery.StringValue(attribution.Model), true
	case "source":
		return collectionquery.EnumValue(attribution.Source), true
	case "attempts":
		return collectionquery.NumberValue(float64(attribution.Attempts)), true
	case "runs":
		return collectionquery.NumberValue(float64(attribution.RunCount)), true
	case "latest":
		return collectionquery.TimestampValue(attribution.LatestCompletedAt), true
	default:
		return collectionquery.FieldValue{}, false
	}
}
