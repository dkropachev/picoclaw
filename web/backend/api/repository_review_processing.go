package api

import (
	"errors"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/sipeed/picoclaw/pkg/collectionquery"
	"github.com/sipeed/picoclaw/pkg/repoaudit"
)

var repositoryReviewFindingsProcessingCollectionSchema = mustCollectionQuerySchema(
	[]collectionquery.FieldSchema{
		{Name: "id", Type: collectionquery.TypeString, Sortable: true},
		{Name: "campaign", Type: collectionquery.TypeString, Sortable: true},
		{Name: "title", Type: collectionquery.TypeString, Sortable: true},
		{Name: "path", Type: collectionquery.TypeString, Sortable: true},
		{Name: "symbol", Type: collectionquery.TypeString, Sortable: true},
		{
			Name: "severity", Type: collectionquery.TypeEnum, Sortable: true,
			SuggestedValues: []string{"critical", "high", "medium", "low"},
		},
		{Name: "model", Type: collectionquery.TypeString, Sortable: true},
		{Name: "reviewer", Type: collectionquery.TypeString, Sortable: true},
		{
			Name: "state", Type: collectionquery.TypeEnum, Sortable: true,
			SuggestedValues: []string{"pending", "running", "failed", "completed"},
		},
		{
			Name: "disposition", Type: collectionquery.TypeEnum, Sortable: true,
			SuggestedValues: []string{"undecided", "new", "duplicate"},
		},
		{Name: "created", Type: collectionquery.TypeTimestamp, Sortable: true},
		{Name: "updated", Type: collectionquery.TypeTimestamp, Sortable: true},
	},
	[]collectionquery.SortField{{Field: "updated", Direction: collectionquery.Descending}},
)

func (h *Handler) handleListRepositoryReviewFindingsProcessing(
	w http.ResponseWriter,
	r *http.Request,
) {
	legacy, modeErr := repositoryReviewUsesLegacyFindingsProcessingPage(r)
	if modeErr != nil {
		writeCollectionError(
			w,
			http.StatusBadRequest,
			"invalid_collection_request",
			"Collection query parameters are malformed",
			-1,
			nil,
		)
		return
	}
	if legacy {
		h.handleGetRepositoryReviewFindingsProcessing(w, r)
		return
	}
	listRequest, ok := parseCollectionListRequest(
		w, r, repositoryReviewFindingsProcessingCollectionSchema,
	)
	if !ok {
		return
	}
	ledger, err := h.repositoryReviewAutomationLedger(r.Context(), r.PathValue("automation_id"))
	if err != nil {
		writeRepositoryReviewAutomationError(w, err)
		return
	}
	summaries := make([]repositoryReviewRawFindingSummary, 0, len(ledger.State.RawFindings))
	for _, raw := range ledger.State.RawFindings {
		summaries = append(summaries, projectRepositoryReviewRawFindingSummary(raw))
	}
	contextID := repositoryReviewCollectionCursorContext(
		"findings-processing", ledger.Automation.ID, ledger.State.ID,
	)
	page, pageErr := collectionquery.Paginate(
		summaries,
		listRequest.Query,
		listRequest.Cursor,
		listRequest.Limit,
		listRequest.Now,
		repositoryReviewFindingsProcessingPageOptions(contextID),
	)
	if pageErr != nil {
		writeCollectionPageError(w, pageErr)
		return
	}
	campaigns, titles, paths := []string{}, []string{}, []string{}
	symbols, models, reviewers := []string{}, []string{}, []string{}
	for _, source := range summaries {
		campaigns = append(campaigns, source.CampaignID)
		titles = append(titles, source.Title)
		paths = append(paths, source.Path)
		symbols = append(symbols, source.Symbol)
		models = append(models, source.Model)
		reviewers = append(reviewers, source.Reviewer)
	}
	health := repositoryReviewFindingHealthFor(ledger.Automation, ledger.State)
	response := map[string]any{
		"automation":      projectRepositoryReviewAutomation(ledger.Automation),
		"raw_findings":    page.Items,
		"total":           page.Total,
		"next_cursor":     page.NextCursor,
		"canonical_query": listRequest.Query.Canonical(),
		"query_schema": collectionSchemaWithSuggestions(
			repositoryReviewFindingsProcessingCollectionSchema,
			map[collectionquery.Field][]string{
				"campaign": campaigns, "title": titles, "path": paths,
				"symbol": symbols, "model": models, "reviewer": reviewers,
			},
		),
		"capabilities":             repositoryReviewGlobalCapabilities(ledger),
		"findings_processing":      health.FindingsProcessing,
		"historical_consolidation": health.HistoricalConsolidation,
	}
	if ledger.Found {
		response["repository"] = repoaudit.Summarize(ledger.State)
	}
	writeRepositoryReviewJSON(w, http.StatusOK, response)
}

func repositoryReviewUsesLegacyFindingsProcessingPage(r *http.Request) (bool, error) {
	if r == nil || r.URL == nil {
		return false, errors.New("invalid findings processing request")
	}
	query, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		return false, err
	}
	return query.Has("offset") || query.Has("state"), nil
}

func repositoryReviewFindingsProcessingPageOptions(
	contextID string,
) collectionquery.PageOptions[repositoryReviewRawFindingSummary] {
	return collectionquery.PageOptions[repositoryReviewRawFindingSummary]{
		ID: func(source repositoryReviewRawFindingSummary) (string, error) {
			return repositoryReviewCollectionCursorItemID(contextID, source.ID)
		},
		ValidateID: repositoryReviewCollectionCursorIDValidator(contextID),
		Resolve: func(
			source repositoryReviewRawFindingSummary,
			field collectionquery.Field,
			_ time.Time,
		) (collectionquery.FieldValue, bool) {
			switch field {
			case "id":
				return collectionquery.StringValue(source.ID), true
			case "campaign":
				return collectionquery.StringValue(source.CampaignID), true
			case "title":
				return collectionquery.StringValue(source.Title), true
			case "path":
				return collectionquery.StringValue(source.Path), true
			case "symbol":
				return collectionquery.StringValue(source.Symbol), true
			case "severity":
				return collectionquery.EnumValue(source.Severity), true
			case "model":
				return collectionquery.StringValue(source.Model), true
			case "reviewer":
				return collectionquery.StringValue(source.Reviewer), true
			case "state":
				return collectionquery.EnumValue(string(source.DeduplicationState)), true
			case "disposition":
				return collectionquery.EnumValue(string(source.Disposition)), true
			case "created":
				return collectionquery.TimestampValue(source.CreatedAt), true
			case "updated":
				return collectionquery.TimestampValue(source.UpdatedAt), true
			default:
				return collectionquery.FieldValue{}, false
			}
		},
		Compare: repositoryReviewSeverityComparator,
	}
}

func (h *Handler) handleGetRepositoryReviewProcessingSource(
	w http.ResponseWriter,
	r *http.Request,
) {
	ledger, raw, ok := h.repositoryReviewCanonicalProcessingSource(w, r)
	if !ok {
		return
	}
	response := repositoryReviewProcessingSourceDetail(ledger, raw)
	writeRepositoryReviewJSON(w, http.StatusOK, response)
}

func repositoryReviewProcessingSourceDetail(
	ledger repositoryReviewAutomationLedger,
	raw repoaudit.RawReviewFinding,
) map[string]any {
	health := repositoryReviewFindingHealthFor(ledger.Automation, ledger.State)
	response := map[string]any{
		"automation":               projectRepositoryReviewAutomation(ledger.Automation),
		"repository":               repoaudit.Summarize(ledger.State),
		"source":                   projectRepositoryReviewRawFindingDetail(raw),
		"capabilities":             repositoryReviewGlobalCapabilities(ledger),
		"findings_processing":      health.FindingsProcessing,
		"historical_consolidation": health.HistoricalConsolidation,
	}
	if contextRecord, found := repositoryReviewContextByID(ledger.State, raw.ContextID); found {
		response["context"] = contextRecord
	}
	repositoryFindingID := ""
	if finding, found := repositoryReviewDeduplicatedFindingByID(
		ledger.State, raw.DeduplicatedFindingID,
	); found {
		repositoryFindingID = finding.RepositoryFindingID
		if projection, projectionFound := repositoryReviewFindingByID(
			ledger.State, finding.ID,
		); projectionFound {
			response["finding"] = projectRepositoryReviewRunFinding(ledger.State, projection)
		} else {
			response["finding"] = finding
		}
	} else if finding, found := repositoryReviewFindingByID(
		ledger.State, raw.DeduplicatedFindingID,
	); found {
		repositoryFindingID = finding.RepositoryFindingID
		response["finding"] = projectRepositoryReviewRunFinding(ledger.State, finding)
	}
	if repositoryFinding, found := repositoryReviewRepositoryFindingByID(
		ledger.State, repositoryFindingID,
	); found {
		response["repository_finding"] = repositoryFinding
	}
	return response
}

func (h *Handler) handleRetryRepositoryReviewProcessingSource(
	w http.ResponseWriter,
	r *http.Request,
) {
	if err := validateRepositoryReviewMutation(r); err != nil || r.URL == nil ||
		r.URL.RawQuery != "" {
		writeRepositoryReviewError(w, errors.New("invalid finding processing retry request"))
		return
	}
	var request struct{}
	if err := decodeRepositoryReviewRequest(r, &request); err != nil {
		writeRepositoryReviewError(w, err)
		return
	}
	ledger, raw, ok := h.repositoryReviewCanonicalProcessingSource(w, r)
	if !ok {
		return
	}
	if repoaudit.HistoricalDeduplicationRawFinding(raw) {
		writeRepositoryReviewError(w, errors.Join(
			repoaudit.ErrConflict,
			errors.New("historical sources require retrying historical consolidation"),
		))
		return
	}
	state, retried, err := ledger.Store.RetryDeduplication(ledger.State.Repository, raw.ID)
	if err != nil {
		writeRepositoryReviewError(w, err)
		return
	}
	if controller := h.repositoryReviewControllerInstance(); controller != nil {
		controller.wakeRepositoryFindingDeduplication()
	}
	health := repositoryReviewFindingHealthFor(ledger.Automation, state)
	writeRepositoryReviewJSON(w, http.StatusAccepted, map[string]any{
		"automation":          projectRepositoryReviewAutomation(ledger.Automation),
		"repository":          repoaudit.Summarize(state),
		"source":              projectRepositoryReviewRawFindingDetail(retried),
		"findings_processing": health.FindingsProcessing,
		"health":              health,
	})
}

func (h *Handler) handleRetryRepositoryReviewProcessingSources(
	w http.ResponseWriter,
	r *http.Request,
) {
	if err := validateRepositoryReviewMutation(r); err != nil || r.URL == nil ||
		r.URL.RawQuery != "" {
		writeRepositoryReviewError(w, errors.New("invalid finding processing bulk retry request"))
		return
	}
	var request struct {
		SourceIDs []string `json:"source_ids"`
	}
	if err := decodeRepositoryReviewRequest(r, &request); err != nil {
		writeRepositoryReviewError(w, err)
		return
	}
	normalizedSourceIDs, err := repoaudit.NormalizeDeduplicationRetrySourceIDs(request.SourceIDs)
	if err != nil {
		writeRepositoryReviewError(w, err)
		return
	}
	ledger, err := h.repositoryReviewAutomationLedger(r.Context(), r.PathValue("automation_id"))
	if err != nil {
		writeRepositoryReviewAutomationError(w, err)
		return
	}
	state, result, err := ledger.Store.RetryDeduplications(
		ledger.State.Repository, normalizedSourceIDs,
	)
	if err != nil {
		writeRepositoryReviewError(w, err)
		return
	}
	if len(result.RetriedIDs) > 0 {
		if controller := h.repositoryReviewControllerInstance(); controller != nil {
			controller.wakeRepositoryFindingDeduplication()
		}
	}
	health := repositoryReviewFindingHealthFor(ledger.Automation, state)
	writeRepositoryReviewJSON(w, http.StatusAccepted, map[string]any{
		"retried_ids":         result.RetriedIDs,
		"failures":            result.Failures,
		"findings_processing": health.FindingsProcessing,
		"health":              health,
	})
}

func (h *Handler) repositoryReviewCanonicalProcessingSource(
	w http.ResponseWriter,
	r *http.Request,
) (repositoryReviewAutomationLedger, repoaudit.RawReviewFinding, bool) {
	ledger, err := h.repositoryReviewAutomationLedger(r.Context(), r.PathValue("automation_id"))
	if err != nil {
		writeRepositoryReviewAutomationError(w, err)
		return repositoryReviewAutomationLedger{}, repoaudit.RawReviewFinding{}, false
	}
	raw, found := repositoryReviewRawFindingByAlias(
		ledger.State.RawFindings, strings.TrimSpace(r.PathValue("source_id")),
	)
	if !found {
		writeRepositoryReviewAutomationError(w, os.ErrNotExist)
		return repositoryReviewAutomationLedger{}, repoaudit.RawReviewFinding{}, false
	}
	return ledger, raw, true
}
