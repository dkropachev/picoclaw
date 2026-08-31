package api

import (
	"errors"
	"net/http"
	"os"
	"sort"
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
			SuggestedValues: []string{"open", "dismissed", "posted"},
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
	// Pre-deduplication ledgers retain their legacy collection projection. New
	// ledgers never expose an undecided raw finding through the Findings route.
	if ledger.Found && len(ledger.State.RawFindings) == 0 &&
		len(ledger.State.DeduplicationJobs) == 0 && len(ledger.State.DeduplicatedFindings) == 0 &&
		!ledger.State.HistoricalDeduplication.Required {
		h.handleListRepositoryReviewRunFindingsCollection(w, r)
		return
	}
	findings := repositoryReviewCurrentDeduplicatedFindings(ledger.Automation, ledger.State)
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
		"deduplicated-findings", ledger.Automation.ID, ledger.Automation.CampaignID,
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
		"capabilities": repositoryReviewGlobalCapabilities(ledger),
	}
	if ledger.Found {
		response["repository"] = repoaudit.Summarize(ledger.State)
	}
	writeRepositoryReviewJSON(w, http.StatusOK, response)
}

func (h *Handler) writeRepositoryReviewDeduplicatedFindingsPage(
	w http.ResponseWriter,
	ledger repositoryReviewAutomationLedger,
	scope string,
	offset, limit int,
) {
	findings := append([]repoaudit.DeduplicatedReviewFinding(nil), ledger.State.DeduplicatedFindings...)
	if scope != "all" {
		findings = repositoryReviewCurrentDeduplicatedFindings(ledger.Automation, ledger.State)
	}
	total := len(findings)
	offset = min(offset, total)
	end := min(total, offset+limit)
	page := make([]repositoryReviewRunFindingProjection, 0, end-offset)
	for _, finding := range findings[offset:end] {
		if projection, found := repositoryReviewFindingByID(ledger.State, finding.ID); found {
			projection.Observations = nil
			page = append(page, projectRepositoryReviewRunFinding(ledger.State, projection))
		}
	}
	response := map[string]any{
		"automation":          projectRepositoryReviewAutomation(ledger.Automation),
		"repository":          repoaudit.Summarize(ledger.State),
		"findings":            page,
		"repository_findings": []repoaudit.RepositoryFinding{},
		"scope":               scope,
		"offset":              offset,
		"total":               total,
		"capabilities":        repositoryReviewGlobalCapabilities(ledger),
	}
	if end < total {
		response["next_offset"] = end
	}
	repositoryOffset := offset
	if repositoryTotal := len(ledger.State.RepositoryFindings); repositoryTotal == 0 {
		repositoryOffset = 0
	} else if repositoryOffset >= repositoryTotal {
		repositoryOffset = ((repositoryTotal - 1) / limit) * limit
	}
	repositoryEnd := min(len(ledger.State.RepositoryFindings), repositoryOffset+limit)
	repositoryPage := make([]repoaudit.RepositoryFinding, 0, repositoryEnd-repositoryOffset)
	for _, finding := range ledger.State.RepositoryFindings[repositoryOffset:repositoryEnd] {
		repositoryPage = append(repositoryPage, repositoryReviewRepositoryFindingSummary(finding))
	}
	response["repository_findings"] = repositoryPage
	response["repository_finding_total"] = len(ledger.State.RepositoryFindings)
	response["repository_finding_offset"] = repositoryOffset
	if repositoryEnd < len(ledger.State.RepositoryFindings) {
		response["next_repository_finding_offset"] = repositoryEnd
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
	if ledger.Found && len(ledger.State.RawFindings) == 0 &&
		len(ledger.State.DeduplicationJobs) == 0 && len(ledger.State.DeduplicatedFindings) == 0 &&
		!ledger.State.HistoricalDeduplication.Required {
		h.handleGetRepositoryReviewAutomationFinding(w, r)
		return
	}
	finding, found := repositoryReviewDeduplicatedFindingByID(
		ledger.State, strings.TrimSpace(r.PathValue("finding_id")),
	)
	if !found {
		if _, aggregateFound := repositoryReviewRepositoryFindingByID(
			ledger.State, strings.TrimSpace(r.PathValue("finding_id")),
		); !aggregateFound {
			writeRepositoryReviewAutomationError(w, os.ErrNotExist)
			return
		}
		h.handleGetRepositoryReviewAutomationFinding(w, r)
		return
	}
	capabilities := repositoryReviewGlobalCapabilities(ledger)
	contexts := []repoaudit.FindingContext{}
	if projection, projectionFound := repositoryReviewFindingByID(ledger.State, finding.ID); projectionFound {
		capabilities = repositoryReviewFindingCapabilities(ledger.State, projection)
	}
	if len(finding.RawSourceIDs) > 0 {
		if raw, rawFound := repositoryReviewRawFindingByID(
			ledger.State, finding.RawSourceIDs[0],
		); rawFound {
			if contextRecord, contextFound := repositoryReviewContextByID(
				ledger.State, raw.ContextID,
			); contextFound {
				contextRecord.CampaignID = ""
				contextRecord.RawDigest = ""
				contexts = append(contexts, contextRecord)
			}
		}
	}
	response := map[string]any{
		"automation":       projectRepositoryReviewAutomation(ledger.Automation),
		"repository":       repoaudit.Summarize(ledger.State),
		"finding":          finding,
		"raw_source_total": len(finding.RawSourceIDs),
		"contexts":         contexts,
		"capabilities":     capabilities,
	}
	if projection, projectionFound := repositoryReviewFindingByID(ledger.State, finding.ID); projectionFound {
		response["finding"] = projectRepositoryReviewRunFinding(ledger.State, projection)
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
	finding, found := repositoryReviewDeduplicatedFindingByID(
		ledger.State, strings.TrimSpace(r.PathValue("finding_id")),
	)
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
		"source":     raw,
	}
	if contextRecord, found := repositoryReviewContextByID(ledger.State, raw.ContextID); found {
		response["context"] = contextRecord
	}
	if raw.DeduplicatedFindingID != "" {
		if finding, found := repositoryReviewDeduplicatedFindingByID(
			ledger.State, raw.DeduplicatedFindingID,
		); found {
			if projection, projectionFound := repositoryReviewFindingByID(
				ledger.State, finding.ID,
			); projectionFound {
				response["finding"] = projectRepositoryReviewRunFinding(ledger.State, projection)
			} else {
				response["finding"] = finding
			}
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
	state, retried, err := ledger.Store.RetryDeduplication(ledger.State.Repository, raw.ID)
	if err != nil {
		writeRepositoryReviewError(w, err)
		return
	}
	if controller := h.repositoryReviewControllerInstance(); controller != nil {
		controller.wakeRepositoryFindingDeduplication()
	}
	campaignRaw := make([]repoaudit.RawReviewFinding, 0)
	for _, candidate := range state.RawFindings {
		if candidate.CampaignID == retried.CampaignID {
			campaignRaw = append(campaignRaw, candidate)
		}
	}
	writeRepositoryReviewJSON(w, http.StatusAccepted, map[string]any{
		"automation":          projectRepositoryReviewAutomation(ledger.Automation),
		"repository":          repoaudit.Summarize(state),
		"source":              retried,
		"findings_processing": repositoryReviewFindingsProcessingCounters(campaignRaw),
	})
}

func (h *Handler) handleGetRepositoryReviewFindingsProcessing(
	w http.ResponseWriter,
	r *http.Request,
) {
	offset, limit, stateFilter, err := repositoryReviewFindingsProcessingPage(r)
	if err != nil {
		writeRepositoryReviewError(w, err)
		return
	}
	ledger, err := h.repositoryReviewAutomationLedger(r.Context(), r.PathValue("automation_id"))
	if err != nil {
		writeRepositoryReviewAutomationError(w, err)
		return
	}
	campaignID := strings.TrimSpace(r.PathValue("campaign_id"))
	if campaignID == "" {
		campaignID = ledger.Automation.CampaignID
	}
	if campaignID == "" || len(campaignID) > 256 || strings.ContainsRune(campaignID, 0) {
		writeRepositoryReviewError(w, errors.New("a valid campaign is required"))
		return
	}
	campaignRaw := make([]repoaudit.RawReviewFinding, 0)
	rawFindings := make([]repoaudit.RawReviewFinding, 0)
	for _, raw := range ledger.State.RawFindings {
		if raw.CampaignID != campaignID {
			continue
		}
		campaignRaw = append(campaignRaw, raw)
		if stateFilter == "" || string(raw.State) == stateFilter {
			rawFindings = append(rawFindings, raw)
		}
	}
	sort.SliceStable(rawFindings, func(left, right int) bool {
		if rawFindings[left].CreatedAt.Equal(rawFindings[right].CreatedAt) {
			return rawFindings[left].ID < rawFindings[right].ID
		}
		return rawFindings[left].CreatedAt.Before(rawFindings[right].CreatedAt)
	})
	summaries := make([]repositoryReviewRawFindingSummary, 0, len(rawFindings))
	for _, raw := range rawFindings {
		summaries = append(summaries, projectRepositoryReviewRawFindingSummary(raw))
	}
	total := len(summaries)
	offset = min(offset, total)
	end := min(total, offset+limit)
	response := map[string]any{
		"automation":          projectRepositoryReviewAutomation(ledger.Automation),
		"repository":          repoaudit.Summarize(ledger.State),
		"campaign_id":         campaignID,
		"findings_processing": repositoryReviewFindingsProcessingCounters(campaignRaw),
		"raw_findings": append(
			[]repositoryReviewRawFindingSummary(nil), summaries[offset:end]...,
		),
		"offset": offset,
		"total":  total,
	}
	if end < total {
		response["next_offset"] = end
	}
	writeRepositoryReviewJSON(w, http.StatusOK, response)
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
	raw, found := repositoryReviewRawFindingByID(
		ledger.State, strings.TrimSpace(r.PathValue("source_id")),
	)
	if !found {
		writeRepositoryReviewAutomationError(w, os.ErrNotExist)
		return repositoryReviewAutomationLedger{}, repoaudit.RawReviewFinding{}, false
	}
	if campaignID := strings.TrimSpace(r.PathValue("campaign_id")); campaignID != "" &&
		raw.CampaignID != campaignID {
		writeRepositoryReviewAutomationError(w, os.ErrNotExist)
		return repositoryReviewAutomationLedger{}, repoaudit.RawReviewFinding{}, false
	}
	if findingID := strings.TrimSpace(r.PathValue("finding_id")); findingID != "" {
		finding, exists := repositoryReviewDeduplicatedFindingByID(ledger.State, findingID)
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
) []repoaudit.DeduplicatedReviewFinding {
	result := make([]repoaudit.DeduplicatedReviewFinding, 0, len(state.DeduplicatedFindings))
	for _, finding := range state.DeduplicatedFindings {
		if automation.CampaignID == "" || repoaudit.DeduplicatedFindingBelongsToCampaign(
			state, finding, automation.CampaignID,
		) {
			result = append(result, finding)
		}
	}
	return result
}

func repositoryReviewDeduplicatedFindingByID(
	state repoaudit.RepositoryState,
	id string,
) (repoaudit.DeduplicatedReviewFinding, bool) {
	id = strings.TrimSpace(id)
	for _, finding := range state.DeduplicatedFindings {
		if finding.ID == id {
			return finding, true
		}
	}
	return repoaudit.DeduplicatedReviewFinding{}, false
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
	finding repoaudit.DeduplicatedReviewFinding,
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
	return repositoryReviewRawFindingSummary{
		ID: raw.ID, CampaignID: raw.CampaignID, Path: raw.File.Path, Line: raw.Line,
		Severity: raw.Severity, Title: raw.Title, Symbol: raw.Symbol,
		Model: raw.Model, ModelAlias: raw.ModelAlias, Account: raw.Account,
		Reviewer: raw.Reviewer, DeduplicationState: raw.State,
		Disposition: raw.Disposition, DeduplicatedFindingID: raw.DeduplicatedFindingID,
		Failure: raw.Failure, CreatedAt: raw.CreatedAt, UpdatedAt: raw.UpdatedAt,
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

func repositoryReviewFindingsProcessingPage(
	r *http.Request,
) (int, int, string, error) {
	if r == nil || r.URL == nil {
		return 0, 0, "", errors.New("invalid findings processing request")
	}
	query := r.URL.Query()
	for key, values := range query {
		if (key != "offset" && key != "limit" && key != "state") || len(values) != 1 {
			return 0, 0, "", errors.New("invalid findings processing request")
		}
	}
	offset, err := repositoryReviewPageInteger(query.Get("offset"), 0, 0)
	if err != nil {
		return 0, 0, "", err
	}
	limit, err := repositoryReviewPageInteger(query.Get("limit"), 50, 200)
	if err != nil {
		return 0, 0, "", err
	}
	state := strings.TrimSpace(query.Get("state"))
	if state != "" && state != string(repoaudit.RawFindingDeduplicationPending) &&
		state != string(repoaudit.RawFindingDeduplicationRunning) &&
		state != string(repoaudit.RawFindingDeduplicationFailed) &&
		state != string(repoaudit.RawFindingDeduplicationCompleted) {
		return 0, 0, "", errors.New("invalid findings processing state")
	}
	return offset, limit, state, nil
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
