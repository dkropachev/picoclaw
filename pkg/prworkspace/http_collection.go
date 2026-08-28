package prworkspace

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/sipeed/picoclaw/pkg/collectionquery"
	"github.com/sipeed/picoclaw/pkg/logger"
)

const (
	developmentWorkspaceInternalPageSize = 100
	// Collection filtering requires one bounded in-memory snapshot. Ten
	// thousand administrative workspaces leaves ample operational headroom
	// while preventing a corrupted store from growing one request without
	// limit. The separate page ceiling also bounds a strictly progressing store
	// that returns pathologically sparse pages.
	developmentWorkspaceMaxCollectionItems = 10_000
	developmentWorkspaceMaxInternalPages   = 256
	developmentWorkspacePublicTextBytes    = 1024
	developmentWorkspaceMaxRawQueryBytes   = 3*(collectionquery.MaxQueryBytes+collectionquery.MaxCursorBytes) + 64
)

var (
	errDevelopmentWorkspaceCollection = errors.New("development workspace collection is unavailable")

	developmentWorkspaceCollectionSchema = mustDevelopmentWorkspaceCollectionSchema(
		[]collectionquery.FieldSchema{
			{Name: "id", Type: collectionquery.TypeString, Sortable: true},
			{
				Name: "intent", Type: collectionquery.TypeEnum, Sortable: true,
				SuggestedValues: []string{string(IntentImplementFeature), string(IntentPickupPR)},
			},
			{
				Name: "source", Type: collectionquery.TypeEnum, Sortable: true,
				SuggestedValues: []string{string(SourceIssue), string(SourceBrief), string(SourcePullRequest)},
			},
			{Name: "repository", Type: collectionquery.TypeString, Sortable: true},
			{Name: "title", Type: collectionquery.TypeString, Sortable: true},
			{
				Name: "phase", Type: collectionquery.TypeEnum, Sortable: true,
				SuggestedValues: []string{
					string(PhaseIntake), string(PhaseCharter), string(PhasePlanning),
					string(PhaseReview), string(PhaseTriage), string(PhaseImplementation),
					string(PhaseValidation), string(PhaseCompletionAudit),
					string(PhasePublication), string(PhaseComplete),
				},
			},
			{
				Name: "execution_state", Type: collectionquery.TypeEnum, Sortable: true,
				SuggestedValues: []string{
					string(ExecutionQueued), string(ExecutionRunning), string(ExecutionWaitingGate),
					string(ExecutionWaitingUser), string(ExecutionSucceeded), string(ExecutionFailed),
					string(ExecutionBlocked), string(ExecutionCanceled), string(ExecutionStale),
					string(ExecutionUnknown),
				},
			},
			{Name: "created", Type: collectionquery.TypeTimestamp, Sortable: true},
			{Name: "updated", Type: collectionquery.TypeTimestamp, Sortable: true},
		},
		[]collectionquery.SortField{{Field: "updated", Direction: collectionquery.Descending}},
	)
)

type developmentWorkspaceCollectionSummary struct {
	ID             string            `json:"id"`
	Intent         DevelopmentIntent `json:"intent"`
	Source         SourceKind        `json:"source"`
	Repository     string            `json:"repository"`
	Title          string            `json:"title"`
	Phase          Phase             `json:"phase"`
	ExecutionState ExecutionState    `json:"execution_state"`
	Created        time.Time         `json:"created"`
	Updated        time.Time         `json:"updated"`
}

type developmentWorkspaceCollectionResponse struct {
	Workspaces     []developmentWorkspaceCollectionSummary `json:"workspaces"`
	Total          int                                     `json:"total"`
	NextCursor     string                                  `json:"next_cursor,omitempty"`
	CanonicalQuery string                                  `json:"canonical_query"`
	QuerySchema    collectionquery.Schema                  `json:"query_schema"`
}

type developmentWorkspaceCollectionRequest struct {
	Query  collectionquery.Query
	Cursor string
	Limit  int
	Now    time.Time
}

func mustDevelopmentWorkspaceCollectionSchema(
	fields []collectionquery.FieldSchema,
	defaultOrder []collectionquery.SortField,
) collectionquery.Schema {
	schema, err := collectionquery.NewSchema(fields, defaultOrder)
	if err != nil {
		panic(err)
	}
	return schema
}

func (handler *HTTPHandler) serveWorkspaceCollection(w http.ResponseWriter, r *http.Request) {
	request, ok := parseDevelopmentWorkspaceCollectionRequest(w, r)
	if !ok {
		return
	}
	if request.Cursor != "" {
		if _, err := collectionquery.DecodeCursor(
			request.Cursor,
			request.Query,
			validDevelopmentWorkspaceID,
		); err != nil {
			writeDevelopmentWorkspaceCollectionError(
				w,
				http.StatusBadRequest,
				"invalid_cursor",
				"The cursor does not match this query",
				-1,
			)
			return
		}
	}

	items, err := handler.loadDevelopmentWorkspaceCollection(r.Context())
	if err != nil {
		logger.ErrorCF("pr_workspace", "Development workspace collection failed", map[string]any{
			"error": err.Error(),
		})
		writeDevelopmentWorkspaceCollectionError(
			w,
			http.StatusServiceUnavailable,
			"development_workspaces_unavailable",
			"Development workspaces are unavailable",
			-1,
		)
		return
	}
	page, err := collectionquery.Paginate(
		items,
		request.Query,
		request.Cursor,
		request.Limit,
		request.Now,
		developmentWorkspaceCollectionPageOptions(),
	)
	if err != nil {
		status, code, message := http.StatusInternalServerError, "collection_page_failed", "Failed to page workspaces"
		if errors.Is(err, collectionquery.ErrInvalidCursor) {
			status, code, message = http.StatusBadRequest, "invalid_cursor", "The cursor does not match this query"
		}
		writeDevelopmentWorkspaceCollectionError(w, status, code, message, -1)
		return
	}
	writeHTTPJSON(w, http.StatusOK, developmentWorkspaceCollectionResponse{
		Workspaces:     page.Items,
		Total:          page.Total,
		NextCursor:     page.NextCursor,
		CanonicalQuery: request.Query.Canonical(),
		QuerySchema:    developmentWorkspaceCollectionSchemaWithSuggestions(items),
	})
}

func parseDevelopmentWorkspaceCollectionRequest(
	w http.ResponseWriter,
	r *http.Request,
) (developmentWorkspaceCollectionRequest, bool) {
	if r == nil || r.URL == nil {
		writeDevelopmentWorkspaceCollectionError(
			w, http.StatusBadRequest, "invalid_collection_request", "Invalid collection request", -1,
		)
		return developmentWorkspaceCollectionRequest{}, false
	}
	if len(r.URL.RawQuery) > developmentWorkspaceMaxRawQueryBytes {
		writeDevelopmentWorkspaceCollectionError(
			w,
			http.StatusBadRequest,
			"invalid_collection_request",
			"Collection query parameters exceed the transport limit",
			-1,
		)
		return developmentWorkspaceCollectionRequest{}, false
	}
	values, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		writeDevelopmentWorkspaceCollectionError(
			w,
			http.StatusBadRequest,
			"invalid_collection_request",
			"Collection query parameters are malformed",
			-1,
		)
		return developmentWorkspaceCollectionRequest{}, false
	}
	for key, entries := range values {
		if key != "query" && key != "cursor" && key != "limit" || len(entries) != 1 {
			writeDevelopmentWorkspaceCollectionError(
				w,
				http.StatusBadRequest,
				"invalid_collection_request",
				"Only query, cursor, and limit are supported",
				-1,
			)
			return developmentWorkspaceCollectionRequest{}, false
		}
	}
	limit := 0
	if rawLimit := strings.TrimSpace(values.Get("limit")); rawLimit != "" {
		limit, err = strconv.Atoi(rawLimit)
		if err != nil || limit < 1 || limit > collectionquery.MaxPageSize {
			writeDevelopmentWorkspaceCollectionError(
				w,
				http.StatusBadRequest,
				"invalid_page_limit",
				"Limit must be between 1 and 200",
				-1,
			)
			return developmentWorkspaceCollectionRequest{}, false
		}
	}
	query, err := collectionquery.Parse(values.Get("query"), developmentWorkspaceCollectionSchema)
	if err != nil {
		position, message := 0, "Invalid collection query"
		var queryError *collectionquery.QueryError
		if errors.As(err, &queryError) {
			position, message = queryError.Position, queryError.Message
		}
		writeDevelopmentWorkspaceCollectionError(
			w, http.StatusBadRequest, "invalid_query", message, position,
		)
		return developmentWorkspaceCollectionRequest{}, false
	}
	return developmentWorkspaceCollectionRequest{
		Query:  query,
		Cursor: values.Get("cursor"),
		Limit:  limit,
		Now:    time.Now().UTC(),
	}, true
}

func (handler *HTTPHandler) loadDevelopmentWorkspaceCollection(
	ctx context.Context,
) ([]developmentWorkspaceCollectionSummary, error) {
	if handler == nil || handler.service == nil {
		return nil, errDevelopmentWorkspaceCollection
	}
	filter := ListFilter{Limit: developmentWorkspaceInternalPageSize}
	seenIDs := make(map[string]struct{})
	workspaces := make([]Workspace, 0, developmentWorkspaceInternalPageSize)
	var previous *WorkspaceCursor
	pagesScanned := 0
	for {
		pagesScanned++
		if pagesScanned > developmentWorkspaceMaxInternalPages {
			return nil, errDevelopmentWorkspaceCollection
		}
		page, err := handler.service.List(ctx, filter)
		if err != nil || len(page.Workspaces) > developmentWorkspaceInternalPageSize {
			return nil, errDevelopmentWorkspaceCollection
		}
		for _, workspace := range page.Workspaces {
			if !validDevelopmentWorkspaceID(workspace.ID) {
				return nil, errDevelopmentWorkspaceCollection
			}
			if _, duplicate := seenIDs[workspace.ID]; duplicate {
				return nil, errDevelopmentWorkspaceCollection
			}
			if len(workspaces) == developmentWorkspaceMaxCollectionItems {
				return nil, errDevelopmentWorkspaceCollection
			}
			seenIDs[workspace.ID] = struct{}{}
			workspaces = append(workspaces, workspace)
		}
		if page.Next == nil {
			break
		}
		if len(workspaces) == developmentWorkspaceMaxCollectionItems {
			return nil, errDevelopmentWorkspaceCollection
		}
		if !validDevelopmentWorkspaceStoreCursor(page.Workspaces, previous, *page.Next) {
			return nil, errDevelopmentWorkspaceCollection
		}
		next := *page.Next
		previous = &next
		filter.AfterUpdated, filter.AfterID = next.UpdatedAt, next.ID
	}

	items := make([]developmentWorkspaceCollectionSummary, 0, len(workspaces))
	for _, workspace := range workspaces {
		summary, err := projectDevelopmentWorkspaceCollectionSummary(workspace)
		if err != nil {
			return nil, err
		}
		items = append(items, summary)
	}
	return items, nil
}

func validDevelopmentWorkspaceStoreCursor(
	page []Workspace,
	previous *WorkspaceCursor,
	next WorkspaceCursor,
) bool {
	if len(page) == 0 || next.UpdatedAt.IsZero() || !validDevelopmentWorkspaceID(next.ID) {
		return false
	}
	last := page[len(page)-1]
	if last.ID != next.ID || !last.UpdatedAt.Equal(next.UpdatedAt) {
		return false
	}
	if previous == nil {
		return true
	}
	return next.UpdatedAt.Before(previous.UpdatedAt) ||
		next.UpdatedAt.Equal(previous.UpdatedAt) && next.ID < previous.ID
}

func projectDevelopmentWorkspaceCollectionSummary(
	workspace Workspace,
) (developmentWorkspaceCollectionSummary, error) {
	if !validDevelopmentWorkspaceID(workspace.ID) || !validDevelopmentWorkspaceIntent(workspace.Intent) ||
		!validDevelopmentWorkspaceSource(workspace.SourceKind) || !validPhase(workspace.Phase) ||
		!validExecutionState(workspace.ExecutionState) ||
		workspace.CreatedAt.IsZero() || workspace.UpdatedAt.IsZero() ||
		(workspace.Intent == IntentPickupPR) != (workspace.SourceKind == SourcePullRequest) {
		return developmentWorkspaceCollectionSummary{}, errDevelopmentWorkspaceCollection
	}
	repository := developmentWorkspaceCollectionText(workspace.Repository, "Unknown repository")
	title := developmentWorkspaceFallbackTitle(workspace, repository)
	return developmentWorkspaceCollectionSummary{
		ID:             workspace.ID,
		Intent:         workspace.Intent,
		Source:         workspace.SourceKind,
		Repository:     repository,
		Title:          title,
		Phase:          workspace.Phase,
		ExecutionState: workspace.ExecutionState,
		Created:        workspace.CreatedAt.UTC(),
		Updated:        workspace.UpdatedAt.UTC(),
	}, nil
}

func developmentWorkspaceCollectionText(value, fallback string) string {
	value = strings.TrimSpace(strings.ToValidUTF8(value, "\uFFFD"))
	if value == "" {
		value = fallback
	}
	return boundedUTF8(value, developmentWorkspacePublicTextBytes)
}

func developmentWorkspaceFallbackTitle(workspace Workspace, repository string) string {
	switch workspace.SourceKind {
	case SourceBrief:
		return "Feature brief"
	case SourceIssue:
		if workspace.SourceNumber > 0 {
			return fmt.Sprintf("Issue #%d", workspace.SourceNumber)
		}
	case SourcePullRequest:
		number := workspace.PullNumber
		if number == 0 {
			number = workspace.SourceNumber
		}
		if number > 0 {
			return fmt.Sprintf("Pull request #%d", number)
		}
	}
	return repository
}

func validDevelopmentWorkspaceIntent(intent DevelopmentIntent) bool {
	return intent == IntentImplementFeature || intent == IntentPickupPR
}

func validDevelopmentWorkspaceSource(source SourceKind) bool {
	return source == SourceIssue || source == SourceBrief || source == SourcePullRequest
}

func validDevelopmentWorkspaceID(id string) bool {
	return validOpaqueID(id, "devw_")
}

func developmentWorkspaceCollectionPageOptions() collectionquery.PageOptions[developmentWorkspaceCollectionSummary] {
	return collectionquery.PageOptions[developmentWorkspaceCollectionSummary]{
		Resolve: func(
			item developmentWorkspaceCollectionSummary,
			field collectionquery.Field,
			_ time.Time,
		) (collectionquery.FieldValue, bool) {
			return resolveDevelopmentWorkspaceCollectionField(item, field)
		},
		ID: func(item developmentWorkspaceCollectionSummary) (string, error) {
			return item.ID, nil
		},
		ValidateID: validDevelopmentWorkspaceID,
	}
}

func resolveDevelopmentWorkspaceCollectionField(
	item developmentWorkspaceCollectionSummary,
	field collectionquery.Field,
) (collectionquery.FieldValue, bool) {
	switch field {
	case "id":
		return collectionquery.StringValue(item.ID), true
	case "intent":
		return collectionquery.EnumValue(string(item.Intent)), true
	case "source":
		return collectionquery.EnumValue(string(item.Source)), true
	case "repository":
		return collectionquery.StringValue(item.Repository), true
	case "title":
		return collectionquery.StringValue(item.Title), true
	case "phase":
		return collectionquery.EnumValue(string(item.Phase)), true
	case "execution_state":
		return collectionquery.EnumValue(string(item.ExecutionState)), true
	case "created":
		return collectionquery.TimestampValue(item.Created), true
	case "updated":
		return collectionquery.TimestampValue(item.Updated), true
	default:
		return collectionquery.FieldValue{}, false
	}
}

func developmentWorkspaceCollectionSchemaWithSuggestions(
	items []developmentWorkspaceCollectionSummary,
) collectionquery.Schema {
	values := map[collectionquery.Field][]string{
		"id":         make([]string, 0, len(items)),
		"repository": make([]string, 0, len(items)),
		"title":      make([]string, 0, len(items)),
	}
	for _, item := range items {
		values["id"] = append(values["id"], item.ID)
		values["repository"] = append(values["repository"], item.Repository)
		values["title"] = append(values["title"], item.Title)
	}
	result := developmentWorkspaceCollectionSchema.Clone()
	for index := range result.Fields {
		candidates, ok := values[result.Fields[index].Name]
		if !ok {
			continue
		}
		result.Fields[index].SuggestedValues = boundedDevelopmentWorkspaceSuggestions(candidates)
	}
	return result
}

func boundedDevelopmentWorkspaceSuggestions(values []string) []string {
	sort.SliceStable(values, func(left, right int) bool {
		return strings.ToLower(values[left]) < strings.ToLower(values[right])
	})
	result := make([]string, 0, min(len(values), collectionquery.MaxSuggestedValues))
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		key := strings.ToLower(value)
		if value == "" || len(value) > collectionquery.MaxSuggestedValueBytes || !utf8.ValidString(value) ||
			strings.IndexFunc(value, unicode.IsControl) >= 0 {
			continue
		}
		if _, duplicate := seen[key]; duplicate {
			continue
		}
		seen[key] = struct{}{}
		result = append(result, value)
		if len(result) == collectionquery.MaxSuggestedValues {
			break
		}
	}
	return result
}

func writeDevelopmentWorkspaceCollectionError(
	w http.ResponseWriter,
	status int,
	code, message string,
	position int,
) {
	message = boundedUTF8(strings.Map(func(character rune) rune {
		if unicode.IsControl(character) {
			return -1
		}
		return character
	}, message), collectionquery.MaxQueryErrorMessageLen)
	body := map[string]any{"code": code, "message": message}
	if code == "invalid_query" && position >= 0 {
		body["position"] = position
	}
	writeHTTPJSON(w, status, body)
}
