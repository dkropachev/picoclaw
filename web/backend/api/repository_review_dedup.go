package api

import (
	"errors"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/sipeed/picoclaw/pkg/collectionquery"
	"github.com/sipeed/picoclaw/pkg/repoaudit"
)

var repositoryReviewDeduplicatedFindingCollectionSchema = mustCollectionQuerySchema(
	[]collectionquery.FieldSchema{
		{Name: "id", Type: collectionquery.TypeString, Sortable: true},
		{Name: "repository", Type: collectionquery.TypeString, Sortable: true},
		{Name: "title", Type: collectionquery.TypeString, Sortable: true},
		{Name: "path", Type: collectionquery.TypeString, Sortable: true},
		{Name: "symbol", Type: collectionquery.TypeString, Sortable: true},
		{
			Name: "severity", Type: collectionquery.TypeEnum, Sortable: true,
			SuggestedValues: []string{"critical", "high", "medium", "low"},
		},
		{
			Name: "status", Type: collectionquery.TypeEnum, Sortable: true,
			SuggestedValues: []string{"open", "posted"},
		},
		{
			Name: "run_status", Type: collectionquery.TypeEnum, Sortable: true,
			SuggestedValues: []string{
				"pending", "processing", "failed", "associated_new", "associated_existing", "needs_review",
			},
		},
		{
			Name: "association", Type: collectionquery.TypeEnum, Sortable: true,
			SuggestedValues: []string{"unassociated", "new", "existing", "needs_review"},
		},
		{Name: "contributors", Type: collectionquery.TypeString, Sortable: true},
		{Name: "sources", Type: collectionquery.TypeNumber, Sortable: true},
		{Name: "mapped", Type: collectionquery.TypeBoolean, Sortable: true},
		{Name: "created", Type: collectionquery.TypeTimestamp, Sortable: true},
		{Name: "updated", Type: collectionquery.TypeTimestamp, Sortable: true},
	},
	[]collectionquery.SortField{
		{Field: "severity", Direction: collectionquery.Descending},
		{Field: "updated", Direction: collectionquery.Descending},
	},
)

var repositoryReviewRawFindingCollectionSchema = mustCollectionQuerySchema(
	[]collectionquery.FieldSchema{
		{Name: "id", Type: collectionquery.TypeString, Sortable: true},
		{Name: "path", Type: collectionquery.TypeString, Sortable: true},
		{
			Name: "severity", Type: collectionquery.TypeEnum, Sortable: true,
			SuggestedValues: []string{"critical", "high", "medium", "low"},
		},
		{Name: "title", Type: collectionquery.TypeString, Sortable: true},
		{Name: "symbol", Type: collectionquery.TypeString, Sortable: true},
		{Name: "model", Type: collectionquery.TypeString, Sortable: true},
		{Name: "reviewer", Type: collectionquery.TypeString, Sortable: true},
		{
			Name: "deduplication_state", Type: collectionquery.TypeEnum, Sortable: true,
			SuggestedValues: []string{"pending", "running", "failed", "completed"},
		},
		{
			Name: "disposition", Type: collectionquery.TypeEnum, Sortable: true,
			SuggestedValues: []string{"undecided", "new", "duplicate"},
		},
		{Name: "finding", Type: collectionquery.TypeString, Sortable: true},
		{Name: "created", Type: collectionquery.TypeTimestamp, Sortable: true},
		{Name: "updated", Type: collectionquery.TypeTimestamp, Sortable: true},
	},
	[]collectionquery.SortField{{Field: "created", Direction: collectionquery.Descending}},
)

// repositoryReviewDeduplicatedFindingSummary keeps diagnosis evidence,
// history, and source identities on their dedicated detail routes.
type repositoryReviewDeduplicatedFindingSummary struct {
	ID                  string                           `json:"id"`
	Repository          string                           `json:"repository"`
	Path                string                           `json:"path"`
	Line                *int                             `json:"line,omitempty"`
	Severity            string                           `json:"severity"`
	Title               string                           `json:"title"`
	Symbol              string                           `json:"symbol,omitempty"`
	Status              repoaudit.FindingStatus          `json:"status"`
	RunFindingStatus    repositoryReviewRunFindingStatus `json:"run_finding_status"`
	Association         string                           `json:"association"`
	RepositoryFindingID string                           `json:"repository_finding_id,omitempty"`
	Contributors        []string                         `json:"contributors"`
	RawSourceCount      int                              `json:"raw_source_count"`
	CreatedAt           time.Time                        `json:"created_at"`
	UpdatedAt           time.Time                        `json:"updated_at"`
}

type repositoryReviewRawFindingSummary struct {
	ID                    string                                 `json:"id"`
	CampaignID            string                                 `json:"campaign_id"`
	Path                  string                                 `json:"path"`
	Line                  *int                                   `json:"line,omitempty"`
	Severity              string                                 `json:"severity"`
	Title                 string                                 `json:"title"`
	Symbol                string                                 `json:"symbol,omitempty"`
	Model                 string                                 `json:"model"`
	ModelAlias            string                                 `json:"model_alias,omitempty"`
	Account               string                                 `json:"account,omitempty"`
	Reviewer              string                                 `json:"reviewer,omitempty"`
	DeduplicationState    repoaudit.RawFindingDeduplicationState `json:"deduplication_state"`
	Disposition           repoaudit.RawFindingDisposition        `json:"disposition"`
	DeduplicatedFindingID string                                 `json:"deduplicated_finding_id,omitempty"`
	Failure               *repoaudit.DeduplicationFailure        `json:"failure,omitempty"`
	CreatedAt             time.Time                              `json:"created_at"`
	UpdatedAt             time.Time                              `json:"updated_at"`
}

func (h *Handler) handleListRepositoryReviewDeduplicatedFindingsCollection(
	w http.ResponseWriter,
	r *http.Request,
) {
	listRequest, ok := parseCollectionListRequest(
		w, r, repositoryReviewDeduplicatedFindingCollectionSchema,
	)
	if !ok {
		return
	}
	ledger, err := h.repositoryReviewAutomationLedger(r.Context(), r.PathValue("automation_id"))
	if err != nil {
		writeRepositoryReviewAutomationError(w, err)
		return
	}
	findings := repositoryReviewCurrentDeduplicatedFindings(ledger.Automation, ledger.State)
	rawFindings := repositoryReviewCurrentRawFindings(ledger.Automation, ledger.State)
	statusIndex := newRepositoryReviewRunFindingStatusIndex(ledger.State)
	rawByID := make(map[string]repoaudit.RawReviewFinding, len(ledger.State.RawFindings))
	for _, raw := range ledger.State.RawFindings {
		rawByID[raw.ID] = raw
	}
	summaries := make([]repositoryReviewDeduplicatedFindingSummary, 0, len(findings))
	for _, finding := range findings {
		summaries = append(summaries, projectRepositoryReviewDeduplicatedFindingSummary(
			finding, statusIndex, rawByID,
		))
	}
	contextID := repositoryReviewCollectionCursorContext(
		"deduplicated-findings", ledger.Automation.ID,
		repositoryReviewCurrentCampaignCursorKey(ledger.Automation),
	)
	page, pageErr := collectionquery.Paginate(
		summaries,
		listRequest.Query,
		listRequest.Cursor,
		listRequest.Limit,
		listRequest.Now,
		repositoryReviewDeduplicatedFindingPageOptions(contextID),
	)
	if pageErr != nil {
		writeCollectionPageError(w, pageErr)
		return
	}
	repositories, titles, paths, symbols, contributors := []string{}, []string{}, []string{}, []string{}, []string{}
	for _, finding := range summaries {
		repositories = append(repositories, finding.Repository)
		titles = append(titles, finding.Title)
		paths = append(paths, finding.Path)
		symbols = append(symbols, finding.Symbol)
		contributors = append(contributors, finding.Contributors...)
	}
	response := map[string]any{
		"automation":      projectRepositoryReviewAutomation(ledger.Automation),
		"findings":        page.Items,
		"total":           page.Total,
		"next_cursor":     page.NextCursor,
		"canonical_query": listRequest.Query.Canonical(),
		"query_schema": collectionSchemaWithSuggestions(
			repositoryReviewDeduplicatedFindingCollectionSchema,
			map[collectionquery.Field][]string{
				"repository": repositories, "title": titles, "path": paths,
				"symbol": symbols, "contributors": contributors,
			},
		),
		"capabilities":        repositoryReviewGlobalCapabilities(ledger),
		"findings_processing": repositoryReviewFindingsProcessingCounters(rawFindings),
	}
	if ledger.Found {
		response["repository"] = repoaudit.Summarize(ledger.State)
	}
	writeRepositoryReviewJSON(w, http.StatusOK, response)
}

func (h *Handler) handleListRepositoryReviewRawFindingsCollection(
	w http.ResponseWriter,
	r *http.Request,
) {
	listRequest, ok := parseCollectionListRequest(w, r, repositoryReviewRawFindingCollectionSchema)
	if !ok {
		return
	}
	ledger, err := h.repositoryReviewAutomationLedger(r.Context(), r.PathValue("automation_id"))
	if err != nil {
		writeRepositoryReviewAutomationError(w, err)
		return
	}
	rawFindings := repositoryReviewCurrentRawFindings(ledger.Automation, ledger.State)
	summaries := make([]repositoryReviewRawFindingSummary, 0, len(rawFindings))
	for _, raw := range rawFindings {
		summaries = append(summaries, projectRepositoryReviewRawFindingSummary(raw))
	}
	contextID := repositoryReviewCollectionCursorContext(
		"raw-findings", ledger.Automation.ID, repositoryReviewCurrentCampaignCursorKey(ledger.Automation),
	)
	page, pageErr := collectionquery.Paginate(
		summaries,
		listRequest.Query,
		listRequest.Cursor,
		listRequest.Limit,
		listRequest.Now,
		repositoryReviewRawFindingPageOptions(contextID),
	)
	if pageErr != nil {
		writeCollectionPageError(w, pageErr)
		return
	}
	titles, paths, symbols, models, reviewers, findings := []string{}, []string{}, []string{}, []string{}, []string{}, []string{}
	for _, raw := range summaries {
		titles = append(titles, raw.Title)
		paths = append(paths, raw.Path)
		symbols = append(symbols, raw.Symbol)
		models = append(models, raw.Model)
		reviewers = append(reviewers, raw.Reviewer)
		findings = append(findings, raw.DeduplicatedFindingID)
	}
	response := map[string]any{
		"automation":      projectRepositoryReviewAutomation(ledger.Automation),
		"raw_findings":    page.Items,
		"total":           page.Total,
		"next_cursor":     page.NextCursor,
		"canonical_query": listRequest.Query.Canonical(),
		"query_schema": collectionSchemaWithSuggestions(
			repositoryReviewRawFindingCollectionSchema,
			map[collectionquery.Field][]string{
				"title": titles, "path": paths, "symbol": symbols,
				"model": models, "reviewer": reviewers, "finding": findings,
			},
		),
		"capabilities":        repositoryReviewGlobalCapabilities(ledger),
		"findings_processing": repositoryReviewFindingsProcessingCounters(rawFindings),
	}
	if ledger.Found {
		response["repository"] = repoaudit.Summarize(ledger.State)
	}
	writeRepositoryReviewJSON(w, http.StatusOK, response)
}

func (h *Handler) handleGetRepositoryReviewDeduplicatedFinding(
	w http.ResponseWriter,
	r *http.Request,
) {
	ledger, err := h.repositoryReviewAutomationLedger(r.Context(), r.PathValue("automation_id"))
	if err != nil {
		writeRepositoryReviewAutomationError(w, err)
		return
	}
	findingID := strings.TrimSpace(r.PathValue("finding_id"))
	finding, found := repositoryReviewFindingByID(ledger.State, findingID)
	if !found || ledger.Automation.CampaignID == "" ||
		finding.CampaignID != ledger.Automation.CampaignID {
		writeRepositoryReviewAutomationError(w, os.ErrNotExist)
		return
	}
	capabilities := repositoryReviewFindingCapabilities(ledger.State, finding)
	contexts := repositoryReviewFindingContexts(ledger.State, []repoaudit.Finding{finding})
	response := map[string]any{
		"automation":       projectRepositoryReviewAutomation(ledger.Automation),
		"repository":       repoaudit.Summarize(ledger.State),
		"finding":          projectRepositoryReviewRunFinding(ledger.State, finding),
		"raw_source_total": len(finding.RawSourceIDs),
		"contexts":         contexts,
		"capabilities":     capabilities,
	}
	if finding.RepositoryFindingID != "" {
		if repositoryFinding, exists := repositoryReviewRepositoryFindingByID(
			ledger.State, finding.RepositoryFindingID,
		); exists {
			response["repository_finding"] = repositoryFinding
		}
	}
	writeRepositoryReviewJSON(w, http.StatusOK, response)
}

func (h *Handler) handleListRepositoryReviewRawSources(w http.ResponseWriter, r *http.Request) {
	offset, limit, err := repositoryReviewRawPage(r)
	if err != nil {
		writeRepositoryReviewError(w, err)
		return
	}
	ledger, err := h.repositoryReviewAutomationLedger(r.Context(), r.PathValue("automation_id"))
	if err != nil {
		writeRepositoryReviewAutomationError(w, err)
		return
	}
	requestedFindingID := strings.TrimSpace(r.PathValue("finding_id"))
	var finding repoaudit.Finding
	found := false
	for _, candidate := range repositoryReviewCurrentDeduplicatedFindings(
		ledger.Automation, ledger.State,
	) {
		if candidate.ID == requestedFindingID {
			finding, found = candidate, true
			break
		}
	}
	if !found {
		writeRepositoryReviewAutomationError(w, os.ErrNotExist)
		return
	}
	byID := make(map[string]repoaudit.RawReviewFinding, len(ledger.State.RawFindings))
	for _, raw := range ledger.State.RawFindings {
		byID[raw.ID] = raw
	}
	sources := make([]repositoryReviewRawFindingSummary, 0, len(finding.RawSourceIDs))
	for _, sourceID := range finding.RawSourceIDs {
		if raw, exists := byID[sourceID]; exists {
			sources = append(sources, projectRepositoryReviewRawFindingSummary(raw))
		}
	}
	total := len(sources)
	offset = min(offset, total)
	end := min(total, offset+limit)
	response := map[string]any{
		"automation": projectRepositoryReviewAutomation(ledger.Automation),
		"repository": repoaudit.Summarize(ledger.State),
		"finding_id": finding.ID,
		"sources":    append([]repositoryReviewRawFindingSummary(nil), sources[offset:end]...),
		"offset":     offset,
		"total":      total,
	}
	if end < total {
		response["next_offset"] = end
	}
	writeRepositoryReviewJSON(w, http.StatusOK, response)
}

func (h *Handler) handleGetRepositoryReviewRawSource(w http.ResponseWriter, r *http.Request) {
	ledger, raw, ok := h.repositoryReviewRawSource(w, r)
	if !ok {
		return
	}
	response := map[string]any{
		"automation": projectRepositoryReviewAutomation(ledger.Automation),
		"repository": repoaudit.Summarize(ledger.State),
		"source":     projectRepositoryReviewRawFindingDetail(raw),
	}
	if contextRecord, found := repositoryReviewContextByID(ledger.State, raw.ContextID); found {
		response["context"] = contextRecord
	}
	if strings.HasPrefix(raw.DeduplicatedFindingID, "rdf_") {
		if finding, found := repositoryReviewFindingByID(ledger.State, raw.DeduplicatedFindingID); found {
			response["finding"] = projectRepositoryReviewRunFinding(ledger.State, finding)
		}
	}
	writeRepositoryReviewJSON(w, http.StatusOK, response)
}

func (h *Handler) handleRetryRepositoryReviewRawSource(w http.ResponseWriter, r *http.Request) {
	if err := validateRepositoryReviewMutation(r); err != nil || r.URL == nil ||
		r.URL.RawQuery != "" {
		writeRepositoryReviewError(w, errors.New("invalid raw finding retry request"))
		return
	}
	var request struct{}
	if err := decodeRepositoryReviewRequest(r, &request); err != nil {
		writeRepositoryReviewError(w, err)
		return
	}
	ledger, raw, ok := h.repositoryReviewRawSource(w, r)
	if !ok {
		return
	}
	_, persisted := repositoryReviewRawFindingByID(ledger.State, raw.ID)
	if !persisted {
		writeRepositoryReviewError(w, os.ErrNotExist)
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
	currentRaw := repositoryReviewCurrentRawFindings(ledger.Automation, state)
	writeRepositoryReviewJSON(w, http.StatusAccepted, map[string]any{
		"automation":          projectRepositoryReviewAutomation(ledger.Automation),
		"repository":          repoaudit.Summarize(state),
		"source":              projectRepositoryReviewRawFindingDetail(retried),
		"findings_processing": repositoryReviewFindingsProcessingCounters(currentRaw),
	})
}

func (h *Handler) repositoryReviewRawSource(
	w http.ResponseWriter,
	r *http.Request,
) (repositoryReviewAutomationLedger, repoaudit.RawReviewFinding, bool) {
	ledger, err := h.repositoryReviewAutomationLedger(r.Context(), r.PathValue("automation_id"))
	if err != nil {
		writeRepositoryReviewAutomationError(w, err)
		return repositoryReviewAutomationLedger{}, repoaudit.RawReviewFinding{}, false
	}
	requestedID := strings.TrimSpace(r.PathValue("source_id"))
	raw, found := repositoryReviewRawFindingByID(ledger.State, requestedID)
	if found && raw.CampaignID != ledger.Automation.CampaignID {
		found = false
	}
	if !found {
		writeRepositoryReviewAutomationError(w, os.ErrNotExist)
		return repositoryReviewAutomationLedger{}, repoaudit.RawReviewFinding{}, false
	}
	if findingID := strings.TrimSpace(r.PathValue("finding_id")); findingID != "" {
		var finding repoaudit.Finding
		exists := false
		for _, candidate := range repositoryReviewCurrentDeduplicatedFindings(
			ledger.Automation, ledger.State,
		) {
			if candidate.ID == findingID {
				finding, exists = candidate, true
				break
			}
		}
		if !exists || !containsRepositoryReviewSourceID(finding.RawSourceIDs, raw.ID) {
			writeRepositoryReviewAutomationError(w, os.ErrNotExist)
			return repositoryReviewAutomationLedger{}, repoaudit.RawReviewFinding{}, false
		}
	}
	return ledger, raw, true
}

func repositoryReviewCurrentDeduplicatedFindings(
	automation repoaudit.RepositoryReviewAutomation,
	state repoaudit.RepositoryState,
) []repoaudit.Finding {
	result := make([]repoaudit.Finding, 0, len(state.Findings))
	if automation.CampaignID == "" {
		return result
	}
	for _, finding := range state.Findings {
		if finding.CampaignID == automation.CampaignID {
			result = append(result, finding)
		}
	}
	return result
}

func repositoryReviewCurrentRawFindings(
	automation repoaudit.RepositoryReviewAutomation,
	state repoaudit.RepositoryState,
) []repoaudit.RawReviewFinding {
	result := make([]repoaudit.RawReviewFinding, 0, len(state.RawFindings))
	if automation.CampaignID == "" {
		return result
	}
	for _, finding := range state.RawFindings {
		if finding.CampaignID == automation.CampaignID {
			result = append(result, finding)
		}
	}
	return result
}

func repositoryReviewCurrentCampaignCursorKey(
	automation repoaudit.RepositoryReviewAutomation,
) string {
	if !automation.StartedAt.IsZero() {
		return automation.StartedAt.UTC().Format(time.RFC3339Nano)
	}
	if automation.CampaignID != "" {
		return automation.CampaignID
	}
	return "current"
}

func repositoryReviewRawFindingByID(
	state repoaudit.RepositoryState,
	id string,
) (repoaudit.RawReviewFinding, bool) {
	id = strings.TrimSpace(id)
	for _, finding := range state.RawFindings {
		if finding.ID == id {
			return finding, true
		}
	}
	return repoaudit.RawReviewFinding{}, false
}

func repositoryReviewContextByID(
	state repoaudit.RepositoryState,
	id string,
) (repoaudit.FindingContext, bool) {
	for _, contextRecord := range state.Contexts {
		if contextRecord.ID == id {
			return contextRecord, true
		}
	}
	return repoaudit.FindingContext{}, false
}

func projectRepositoryReviewDeduplicatedFindingSummary(
	finding repoaudit.Finding,
	statusIndex repositoryReviewRunFindingStatusIndex,
	rawByID map[string]repoaudit.RawReviewFinding,
) repositoryReviewDeduplicatedFindingSummary {
	projection := repoaudit.Finding{
		ID: finding.ID, RepositoryFindingID: finding.RepositoryFindingID,
		RepositoryMatchState: finding.RepositoryMatchState,
	}
	runStatus := statusIndex.status(projection)
	contributors := make([]string, 0)
	for _, rawID := range finding.RawSourceIDs {
		raw, found := rawByID[rawID]
		if !found {
			continue
		}
		model := raw.ModelAlias
		if model == "" {
			model = raw.Model
		}
		contributors = appendUniqueRepositoryReviewContributor(contributors, model)
		contributors = appendUniqueRepositoryReviewContributor(contributors, raw.Reviewer)
	}
	return repositoryReviewDeduplicatedFindingSummary{
		ID: finding.ID, Repository: finding.Repository, Path: finding.File.Path,
		Line: finding.Line, Severity: finding.Severity, Title: finding.Title,
		Symbol: finding.Symbol, Status: finding.Status,
		RunFindingStatus:    runStatus,
		Association:         repositoryReviewRunFindingAssociation(runStatus),
		RepositoryFindingID: finding.RepositoryFindingID,
		Contributors:        contributors,
		RawSourceCount:      len(finding.RawSourceIDs),
		CreatedAt:           finding.CreatedAt, UpdatedAt: finding.UpdatedAt,
	}
}

func appendUniqueRepositoryReviewContributor(values []string, value string) []string {
	value = strings.TrimSpace(value)
	if value == "" {
		return values
	}
	for _, existing := range values {
		if existing == value {
			return values
		}
	}
	return append(values, value)
}

func projectRepositoryReviewRawFindingSummary(
	raw repoaudit.RawReviewFinding,
) repositoryReviewRawFindingSummary {
	parentID := raw.DeduplicatedFindingID
	if !strings.HasPrefix(parentID, "rdf_") {
		parentID = ""
	}
	return repositoryReviewRawFindingSummary{
		ID: raw.ID, CampaignID: raw.CampaignID, Path: raw.File.Path, Line: raw.Line,
		Severity: raw.Severity, Title: raw.Title, Symbol: raw.Symbol,
		Model: raw.Model, ModelAlias: raw.ModelAlias, Account: raw.Account,
		Reviewer: raw.Reviewer, DeduplicationState: raw.State,
		Disposition: raw.Disposition, DeduplicatedFindingID: parentID,
		Failure: raw.Failure, CreatedAt: raw.CreatedAt, UpdatedAt: raw.UpdatedAt,
	}
}

func projectRepositoryReviewRawFindingDetail(
	raw repoaudit.RawReviewFinding,
) repoaudit.RawReviewFinding {
	raw.DeduplicationSnapshotDigest = ""
	if !strings.HasPrefix(raw.DeduplicatedFindingID, "rdf_") {
		raw.DeduplicatedFindingID = ""
	}
	return raw
}

func repositoryReviewRawFindingPageOptions(
	contextID string,
) collectionquery.PageOptions[repositoryReviewRawFindingSummary] {
	return collectionquery.PageOptions[repositoryReviewRawFindingSummary]{
		ID: func(finding repositoryReviewRawFindingSummary) (string, error) {
			return repositoryReviewCollectionCursorItemID(contextID, finding.ID)
		},
		ValidateID: repositoryReviewCollectionCursorIDValidator(contextID),
		Resolve: func(
			finding repositoryReviewRawFindingSummary,
			field collectionquery.Field,
			_ time.Time,
		) (collectionquery.FieldValue, bool) {
			switch field {
			case "id":
				return collectionquery.StringValue(finding.ID), true
			case "path":
				return collectionquery.StringValue(finding.Path), true
			case "severity":
				return collectionquery.EnumValue(finding.Severity), true
			case "title":
				return collectionquery.StringValue(finding.Title), true
			case "symbol":
				return collectionquery.StringValue(finding.Symbol), true
			case "model":
				return collectionquery.StringValue(finding.Model), true
			case "reviewer":
				return collectionquery.StringValue(finding.Reviewer), true
			case "deduplication_state":
				return collectionquery.EnumValue(string(finding.DeduplicationState)), true
			case "disposition":
				return collectionquery.EnumValue(string(finding.Disposition)), true
			case "finding":
				return collectionquery.StringValue(finding.DeduplicatedFindingID), true
			case "created":
				return collectionquery.TimestampValue(finding.CreatedAt), true
			case "updated":
				return collectionquery.TimestampValue(finding.UpdatedAt), true
			default:
				return collectionquery.FieldValue{}, false
			}
		},
		Compare: repositoryReviewSeverityComparator,
	}
}

func repositoryReviewDeduplicatedFindingPageOptions(
	contextID string,
) collectionquery.PageOptions[repositoryReviewDeduplicatedFindingSummary] {
	return collectionquery.PageOptions[repositoryReviewDeduplicatedFindingSummary]{
		ID: func(finding repositoryReviewDeduplicatedFindingSummary) (string, error) {
			return repositoryReviewCollectionCursorItemID(contextID, finding.ID)
		},
		ValidateID: repositoryReviewCollectionCursorIDValidator(contextID),
		Resolve: func(
			finding repositoryReviewDeduplicatedFindingSummary,
			field collectionquery.Field,
			_ time.Time,
		) (collectionquery.FieldValue, bool) {
			switch field {
			case "id":
				return collectionquery.StringValue(finding.ID), true
			case "repository":
				return collectionquery.StringValue(finding.Repository), true
			case "title":
				return collectionquery.StringValue(finding.Title), true
			case "path":
				return collectionquery.StringValue(finding.Path), true
			case "symbol":
				return collectionquery.StringValue(finding.Symbol), true
			case "severity":
				return collectionquery.EnumValue(finding.Severity), true
			case "status":
				return collectionquery.EnumValue(string(finding.Status)), true
			case "run_status":
				return collectionquery.EnumValue(string(finding.RunFindingStatus)), true
			case "association":
				return collectionquery.EnumValue(finding.Association), true
			case "contributors":
				return collectionquery.StringValue(strings.Join(finding.Contributors, " ")), true
			case "sources":
				return collectionquery.NumberValue(float64(finding.RawSourceCount)), true
			case "mapped":
				return collectionquery.BooleanValue(finding.RepositoryFindingID != ""), true
			case "created":
				return collectionquery.TimestampValue(finding.CreatedAt), true
			case "updated":
				return collectionquery.TimestampValue(finding.UpdatedAt), true
			default:
				return collectionquery.FieldValue{}, false
			}
		},
		Compare: repositoryReviewSeverityComparator,
	}
}

func repositoryReviewRawPage(r *http.Request) (int, int, error) {
	if r == nil || r.URL == nil {
		return 0, 0, errors.New("invalid raw finding source request")
	}
	query := r.URL.Query()
	for key, values := range query {
		if (key != "offset" && key != "limit") || len(values) != 1 {
			return 0, 0, errors.New("invalid raw finding source request")
		}
	}
	offset, err := repositoryReviewPageInteger(query.Get("offset"), 0, 0)
	if err != nil {
		return 0, 0, err
	}
	limit, err := repositoryReviewPageInteger(query.Get("limit"), 50, 200)
	return offset, limit, err
}

func repositoryReviewFindingsProcessingCounters(
	findings []repoaudit.RawReviewFinding,
) repoaudit.FindingsProcessingCounters {
	result := repoaudit.FindingsProcessingCounters{RawTotal: len(findings)}
	for _, finding := range findings {
		if finding.UpdatedAt.After(result.UpdatedAt) {
			result.UpdatedAt = finding.UpdatedAt
		}
		switch finding.State {
		case repoaudit.RawFindingDeduplicationPending:
			result.Pending++
		case repoaudit.RawFindingDeduplicationRunning:
			result.Processing++
		case repoaudit.RawFindingDeduplicationFailed:
			result.Failed++
		case repoaudit.RawFindingDeduplicationCompleted:
			result.Completed++
		}
		switch finding.Disposition {
		case repoaudit.RawFindingDispositionNew:
			result.New++
		case repoaudit.RawFindingDispositionDuplicate:
			result.Duplicates++
		}
	}
	return result
}

func containsRepositoryReviewSourceID(ids []string, wanted string) bool {
	for _, id := range ids {
		if id == wanted {
			return true
		}
	}
	return false
}
