package api

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/sipeed/picoclaw/pkg/collectionquery"
	"github.com/sipeed/picoclaw/pkg/workflows"
)

var workflowDefinitionCollectionSchema = mustCollectionQuerySchema(
	[]collectionquery.FieldSchema{
		{Name: "ref", Type: collectionquery.TypeString, Sortable: true},
		{Name: "name", Type: collectionquery.TypeString, Sortable: true},
		{
			Name: "status", Type: collectionquery.TypeEnum, Sortable: true,
			SuggestedValues: []string{
				workflows.WorkflowValidationStatusValid,
				workflows.WorkflowValidationStatusInvalid,
				workflows.WorkflowValidationStatusPendingRevalidation,
				workflows.WorkflowValidationStatusNeedsReview,
			},
		},
		{
			Name: "trigger", Type: collectionquery.TypeEnum, Sortable: true,
			SuggestedValues: []string{
				"manual", "schedule", "channel_message", "command",
				"runtime_event", "event", "workflow_call", "multiple", "none",
			},
		},
		{Name: "inputs", Type: collectionquery.TypeNumber, Sortable: true},
		{Name: "secrets", Type: collectionquery.TypeNumber, Sortable: true},
	},
	[]collectionquery.SortField{{Field: "ref", Direction: collectionquery.Ascending}},
)

var workflowRunCollectionSchema = mustCollectionQuerySchema(
	[]collectionquery.FieldSchema{
		{Name: "id", Type: collectionquery.TypeString, Sortable: true},
		{Name: "workflow", Type: collectionquery.TypeString, Sortable: true},
		{
			Name: "status", Type: collectionquery.TypeEnum, Sortable: true,
			SuggestedValues: []string{
				workflows.RunStatusRunning,
				workflows.RunStatusWaiting,
				workflows.RunStatusSucceeded,
				workflows.RunStatusFailed,
				workflows.RunStatusCanceled,
				workflows.RunStatusSkipped,
			},
		},
		{Name: "session", Type: collectionquery.TypeString, Sortable: true},
		{
			Name: "origin", Type: collectionquery.TypeEnum, Sortable: true,
			SuggestedValues: []string{
				"manual",
				workflows.RunOriginExternalEvent,
				workflows.RunOriginExternalEventDraftTest,
			},
		},
		{Name: "created", Type: collectionquery.TypeTimestamp, Sortable: true},
		{Name: "updated", Type: collectionquery.TypeTimestamp, Sortable: true},
		{Name: "completed", Type: collectionquery.TypeTimestamp, Sortable: true},
	},
	[]collectionquery.SortField{{Field: "created", Direction: collectionquery.Descending}},
)

type workflowRunCollectionItem struct {
	workflows.Run
	WorkflowID string `json:"workflow_id,omitempty"`
}

// workflowRunCollectionSummary is the intentionally narrow list projection.
// Diagnostic and execution payloads remain available only from the direct run
// resource so collection reads cannot accidentally become bulk detail reads.
type workflowRunCollectionSummary struct {
	ID          string               `json:"id"`
	WorkflowID  string               `json:"workflow_id,omitempty"`
	WorkflowRef string               `json:"workflow_ref"`
	Status      string               `json:"status"`
	Session     string               `json:"session,omitempty"`
	Origin      *workflows.RunOrigin `json:"origin,omitempty"`
	CreatedAt   time.Time            `json:"created_at"`
	UpdatedAt   time.Time            `json:"updated_at"`
	CompletedAt *time.Time           `json:"completed_at,omitempty"`
}

// MarshalJSON preserves Run's fail-closed private-context serializer while
// adding the API-only workflow identity without changing persisted run JSON.
func (item workflowRunCollectionItem) MarshalJSON() ([]byte, error) {
	raw, err := json.Marshal(item.Run)
	if err != nil {
		return nil, err
	}
	if item.WorkflowID == "" {
		return raw, nil
	}
	var projected map[string]json.RawMessage
	_ = json.Unmarshal(raw, &projected)
	encodedID, _ := json.Marshal(item.WorkflowID)
	projected["workflow_id"] = encodedID
	return json.Marshal(projected)
}

type workflowDevelopmentSessionResponse struct {
	*workflows.WorkflowDevelopmentSession
	SourceWorkflowID string `json:"source_workflow_id,omitempty"`
}

var loadWorkflowDefinitionResponseProjection = workflowDefinitionResponses

func projectWorkflowDevelopmentSession(
	session *workflows.WorkflowDevelopmentSession,
) *workflowDevelopmentSessionResponse {
	if session == nil {
		return nil
	}
	projected := &workflowDevelopmentSessionResponse{
		WorkflowDevelopmentSession: session,
	}
	if session.SourceWorkflowRef != "" {
		projected.SourceWorkflowID, _ = workflows.WorkflowDefinitionID(
			session.SourceWorkflowRef,
		)
	}
	return projected
}

func workflowDefinitionTriggerLabel(workflow *workflows.Workflow) string {
	if workflow == nil {
		return "none"
	}
	triggers := make([]string, 0, 7)
	if workflow.On.Manual != nil {
		triggers = append(triggers, "manual")
	}
	if len(workflow.On.Schedule) > 0 {
		triggers = append(triggers, "schedule")
	}
	if workflow.On.ChannelMessage != nil {
		triggers = append(triggers, "channel_message")
	}
	if workflow.On.Command != nil {
		triggers = append(triggers, "command")
	}
	if workflow.On.RuntimeEvent != nil {
		triggers = append(triggers, "runtime_event")
	}
	if workflow.On.Event != nil {
		triggers = append(triggers, "event")
	}
	if workflow.On.WorkflowCall != nil {
		triggers = append(triggers, "workflow_call")
	}
	if len(triggers) == 0 {
		return "none"
	}
	if len(triggers) > 1 {
		return "multiple"
	}
	return triggers[0]
}

func (h *Handler) loadWorkflowDefinitionResponses(
	ctx context.Context,
) ([]workflowDefinitionResponse, *workflows.WorkflowCompatibilitySummary, error) {
	cfg, err := h.workflowConfig()
	if err != nil {
		return nil, nil, err
	}
	workspace := cfg.WorkspacePath()
	localOptions := workflowLocalOptionsFromConfig(cfg)
	definitions, err := workflows.ListLocal(ctx, workspace, localOptions...)
	if err != nil {
		return nil, nil, err
	}
	responses, err := loadWorkflowDefinitionResponseProjection(
		ctx,
		workspace,
		definitions,
		localOptions...,
	)
	if err != nil {
		return nil, nil, err
	}
	compatibility, err := workflows.LoadCompatibilitySummary(
		ctx,
		workspace,
		h.workflowCompatibilityRuntime(ctx),
		localOptions...,
	)
	if err != nil {
		return nil, nil, err
	}
	applyWorkflowDefinitionCompatibility(responses, compatibility)
	return responses, compatibility, nil
}

func applyWorkflowDefinitionCompatibility(
	definitions []workflowDefinitionResponse,
	compatibility *workflows.WorkflowCompatibilitySummary,
) {
	statuses := make(map[string]string)
	if compatibility != nil {
		for _, stamp := range compatibility.Workflows {
			statuses[stamp.WorkflowRef] = stamp.Status
		}
	}
	for index := range definitions {
		if status := statuses[definitions[index].Ref]; status != "" {
			definitions[index].Status = status
		}
		if definitions[index].Status == "" {
			if definitions[index].Error != "" {
				definitions[index].Status = workflows.WorkflowValidationStatusInvalid
			} else {
				definitions[index].Status = workflows.WorkflowValidationStatusPendingRevalidation
			}
		}
	}
}

func safeWorkflowDefinitionResponses(
	definitions []workflowDefinitionResponse,
	includeContracts bool,
) []workflowDefinitionResponse {
	projected := make([]workflowDefinitionResponse, len(definitions))
	copy(projected, definitions)
	for index := range projected {
		if projected[index].Error != "" {
			projected[index].Error = "Workflow definition is invalid"
		}
		if !includeContracts {
			projected[index].WorkflowCall = nil
			projected[index].EventTrigger = nil
		}
	}
	return projected
}

func pageWorkflowDefinitions(
	items []workflowDefinitionResponse,
	request collectionListRequest,
) (collectionquery.PageResult[workflowDefinitionResponse], error) {
	return collectionquery.Paginate(
		items,
		request.Query,
		request.Cursor,
		request.Limit,
		request.Now,
		collectionquery.PageOptions[workflowDefinitionResponse]{
			ID: func(item workflowDefinitionResponse) (string, error) {
				return item.ID, nil
			},
			ValidateID: workflows.ValidWorkflowDefinitionID,
			Clone: func(item workflowDefinitionResponse) workflowDefinitionResponse {
				return item
			},
			Resolve: resolveWorkflowDefinitionCollectionField,
		},
	)
}

func resolveWorkflowDefinitionCollectionField(
	item workflowDefinitionResponse,
	field collectionquery.Field,
	_ time.Time,
) (collectionquery.FieldValue, bool) {
	switch field {
	case "ref":
		return collectionquery.StringValue(item.Ref), true
	case "name":
		return collectionquery.StringValue(item.Name), true
	case "status":
		return collectionquery.EnumValue(item.Status), true
	case "trigger":
		return collectionquery.EnumValue(item.Trigger), true
	case "inputs":
		return collectionquery.NumberValue(float64(item.Inputs)), true
	case "secrets":
		return collectionquery.NumberValue(float64(item.Secrets)), true
	default:
		return collectionquery.FieldValue{}, false
	}
}

func workflowDefinitionSchemaWithSuggestions(
	items []workflowDefinitionResponse,
) collectionquery.Schema {
	refs := make([]string, 0, len(items))
	names := make([]string, 0, len(items))
	for _, item := range items {
		refs = append(refs, item.Ref)
		names = append(names, item.Name)
	}
	return collectionSchemaWithSuggestions(
		workflowDefinitionCollectionSchema,
		map[collectionquery.Field][]string{
			"ref": refs, "name": names,
		},
	)
}

func (h *Handler) handleListWorkflowDefinitions(w http.ResponseWriter, r *http.Request) {
	request, ok := parseCollectionListRequest(w, r, workflowDefinitionCollectionSchema)
	if !ok {
		return
	}
	definitions, _, err := h.loadWorkflowDefinitionResponses(r.Context())
	if err != nil {
		writeCollectionError(
			w, http.StatusInternalServerError, "workflow_definitions_unavailable",
			"Failed to project workflow definitions", -1, nil,
		)
		return
	}
	definitions = safeWorkflowDefinitionResponses(definitions, false)
	page, err := pageWorkflowDefinitions(definitions, request)
	if err != nil {
		writeCollectionPageError(w, err)
		return
	}
	writeCollectionJSON(w, http.StatusOK, map[string]any{
		"workflows":       page.Items,
		"total":           page.Total,
		"next_cursor":     page.NextCursor,
		"canonical_query": request.Query.Canonical(),
		"query_schema":    workflowDefinitionSchemaWithSuggestions(definitions),
	})
}

func (h *Handler) handleGetWorkflowDefinition(w http.ResponseWriter, r *http.Request) {
	if !validateCollectionQueryParameters(w, r) {
		return
	}
	id := r.PathValue("id")
	if !workflows.ValidWorkflowDefinitionID(id) {
		writeCollectionError(
			w, http.StatusBadRequest, "invalid_workflow_definition_id",
			"Invalid workflow definition ID", -1, nil,
		)
		return
	}
	definitions, _, err := h.loadWorkflowDefinitionResponses(r.Context())
	if err != nil {
		writeCollectionError(
			w, http.StatusInternalServerError, "workflow_definitions_unavailable",
			"Failed to project workflow definitions", -1, nil,
		)
		return
	}
	for _, definition := range safeWorkflowDefinitionResponses(definitions, true) {
		if definition.ID == id &&
			workflows.WorkflowDefinitionIDMatches(id, definition.Ref) {
			writeCollectionJSON(
				w,
				http.StatusOK,
				map[string]workflowDefinitionResponse{"workflow": definition},
			)
			return
		}
	}
	writeCollectionError(
		w, http.StatusNotFound, "workflow_definition_not_found",
		"Workflow definition not found", -1, nil,
	)
}

func validWorkflowRunResourceID(id string) bool {
	if len(id) < len("wr_a") || len(id) > 1024 || !utf8.ValidString(id) ||
		!strings.HasPrefix(id, "wr_") {
		return false
	}
	for index := len("wr_"); index < len(id); index++ {
		character := id[index]
		if character >= 'a' && character <= 'z' ||
			character >= 'A' && character <= 'Z' ||
			character >= '0' && character <= '9' ||
			character == '_' || character == '-' {
			continue
		}
		return false
	}
	return true
}

func workflowRunCollectionItemFromRun(run workflows.Run) workflowRunCollectionItem {
	item := workflowRunCollectionItem{Run: run}
	item.WorkflowID, _ = workflows.WorkflowDefinitionID(run.WorkflowRef)
	return item
}

func workflowRunCollectionSummaryFromRun(
	run workflows.Run,
) workflowRunCollectionSummary {
	item := workflowRunCollectionSummary{
		ID:          run.ID,
		WorkflowRef: run.WorkflowRef,
		Status:      run.Status,
		Session:     run.Session,
		Origin:      run.Origin,
		CreatedAt:   run.CreatedAt,
		UpdatedAt:   run.UpdatedAt,
		CompletedAt: run.CompletedAt,
	}
	item.WorkflowID, _ = workflows.WorkflowDefinitionID(run.WorkflowRef)
	return item
}

func workflowRunOrigin(run workflowRunCollectionSummary) string {
	if run.Origin != nil {
		switch run.Origin.Kind {
		case workflows.RunOriginExternalEvent,
			workflows.RunOriginExternalEventDraftTest:
			return run.Origin.Kind
		}
	}
	return "manual"
}

func pageWorkflowRuns(
	items []workflowRunCollectionSummary,
	request collectionListRequest,
) (collectionquery.PageResult[workflowRunCollectionSummary], error) {
	return collectionquery.Paginate(
		items,
		request.Query,
		request.Cursor,
		request.Limit,
		request.Now,
		collectionquery.PageOptions[workflowRunCollectionSummary]{
			ID: func(item workflowRunCollectionSummary) (string, error) {
				return item.ID, nil
			},
			ValidateID: validWorkflowRunResourceID,
			Clone: func(item workflowRunCollectionSummary) workflowRunCollectionSummary {
				return item
			},
			Resolve: resolveWorkflowRunCollectionField,
		},
	)
}

func resolveWorkflowRunCollectionField(
	item workflowRunCollectionSummary,
	field collectionquery.Field,
	_ time.Time,
) (collectionquery.FieldValue, bool) {
	switch field {
	case "id":
		return collectionquery.StringValue(item.ID), true
	case "workflow":
		return collectionquery.StringValue(item.WorkflowRef), true
	case "status":
		return collectionquery.EnumValue(item.Status), true
	case "session":
		return collectionquery.StringValue(item.Session), true
	case "origin":
		return collectionquery.EnumValue(workflowRunOrigin(item)), true
	case "created":
		return collectionquery.TimestampValue(item.CreatedAt), true
	case "updated":
		return collectionquery.TimestampValue(item.UpdatedAt), true
	case "completed":
		if item.CompletedAt == nil {
			// The query type has no null value. Epoch is a stable sentinel
			// that remains older than every issued run.
			return collectionquery.TimestampValue(time.Unix(0, 0).UTC()), true
		}
		return collectionquery.TimestampValue(*item.CompletedAt), true
	default:
		return collectionquery.FieldValue{}, false
	}
}

func workflowRunSchemaWithSuggestions(
	items []workflowRunCollectionSummary,
) collectionquery.Schema {
	ids := make([]string, 0, len(items))
	workflowRefs := make([]string, 0, len(items))
	sessions := make([]string, 0, len(items))
	for _, item := range items {
		ids = append(ids, item.ID)
		workflowRefs = append(workflowRefs, item.WorkflowRef)
		sessions = append(sessions, item.Session)
	}
	return collectionSchemaWithSuggestions(
		workflowRunCollectionSchema,
		map[collectionquery.Field][]string{
			"id": ids, "workflow": workflowRefs, "session": sessions,
		},
	)
}
