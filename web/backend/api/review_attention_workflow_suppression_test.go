package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/workflows"
)

func TestGenericWorkflowBrowserRoutesSuppressReviewAttentionRuns(t *testing.T) {
	workspace := t.TempDir()
	configPath := writeWorkflowAITestConfig(t, workspace)
	handler := NewHandler(configPath)
	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)
	store := workflows.NewFileRunStore(workspace)
	now := time.Now().UTC()
	const attentionWorkflowRef = "inline/review-attention-gates/v1"
	attention := &workflows.Run{
		ID:           "wr_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		WorkflowRef:  attentionWorkflowRef,
		Status:       workflows.RunStatusRunning,
		ParentRunID:  "wr_bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
		ChildRunIDs:  []string{"wr_bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"},
		CallerJobID:  "malformed-reserved-run",
		RetryOfRunID: "wr_bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
		CreatedAt:    now,
		UpdatedAt:    now,
	}
	ordinary := &workflows.Run{
		ID:          "wr_bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
		WorkflowRef: "workflows/operator-visible.yml",
		Status:      workflows.RunStatusRunning,
		ParentRunID: attention.ID,
		ChildRunIDs: []string{
			attention.ID,
			"wr_cccccccccccccccccccccccccccccccc",
		},
		CallerJobID:  "must-be-scrubbed-with-hidden-parent",
		RetryOfRunID: attention.ID,
		CreatedAt:    now,
		UpdatedAt:    now,
	}
	ordinaryChild := &workflows.Run{
		ID:          "wr_cccccccccccccccccccccccccccccccc",
		WorkflowRef: "workflows/operator-child.yml",
		Status:      workflows.RunStatusSucceeded,
		ParentRunID: ordinary.ID,
		CreatedAt:   now,
		UpdatedAt:   now,
		CompletedAt: &now,
	}
	unrelated := &workflows.Run{
		ID:          "wr_dddddddddddddddddddddddddddddddd",
		WorkflowRef: "workflows/unrelated.yml",
		Status:      workflows.RunStatusRunning,
		CreatedAt:   now,
		UpdatedAt:   now,
	}
	for _, run := range []*workflows.Run{attention, ordinary, ordinaryChild, unrelated} {
		if err := store.CreateRun(context.Background(), run); err != nil {
			t.Fatalf("CreateRun(%s) error = %v", run.ID, err)
		}
	}
	if err := store.AppendEvent(context.Background(), workflows.RunEvent{
		Kind: "workflow.run.start", RunID: attention.ID,
	}); err != nil {
		t.Fatalf("AppendEvent(attention) error = %v", err)
	}

	list := httptest.NewRecorder()
	mux.ServeHTTP(
		list,
		httptest.NewRequest(http.MethodGet, "/api/workflows/runs", nil),
	)
	if list.Code != http.StatusOK {
		t.Fatalf("list status = %d, body=%s", list.Code, list.Body.String())
	}
	var listed struct {
		Runs []workflows.Run `json:"runs"`
	}
	if err := json.Unmarshal(list.Body.Bytes(), &listed); err != nil {
		t.Fatalf("decode list: %v", err)
	}
	if len(listed.Runs) != 3 {
		t.Fatalf("visible runs = %#v, want three ordinary runs", listed.Runs)
	}
	listedByID := make(map[string]workflows.Run, len(listed.Runs))
	for _, run := range listed.Runs {
		listedByID[run.ID] = run
	}
	listedOrdinary, exists := listedByID[ordinary.ID]
	if !exists || listedOrdinary.ParentRunID != "" ||
		listedOrdinary.RetryOfRunID != "" || listedOrdinary.CallerJobID != "" ||
		len(listedOrdinary.ChildRunIDs) != 1 ||
		listedOrdinary.ChildRunIDs[0] != ordinaryChild.ID {
		t.Fatalf("sanitized ordinary list run = %#v", listedOrdinary)
	}
	if strings.Contains(list.Body.String(), attention.ID) ||
		strings.Contains(list.Body.String(), attention.WorkflowRef) {
		t.Fatalf("attention identity leaked in list: %s", list.Body.String())
	}
	detail := httptest.NewRecorder()
	mux.ServeHTTP(
		detail,
		httptest.NewRequest(http.MethodGet, "/api/workflows/runs/"+ordinary.ID, nil),
	)
	if detail.Code != http.StatusOK || strings.Contains(detail.Body.String(), attention.ID) {
		t.Fatalf("ordinary detail leaked attention run: (%d, %s)", detail.Code, detail.Body.String())
	}
	var detailed workflows.Run
	if err := json.Unmarshal(detail.Body.Bytes(), &detailed); err != nil {
		t.Fatalf("decode detail: %v", err)
	}
	if detailed.ParentRunID != "" || detailed.RetryOfRunID != "" ||
		detailed.CallerJobID != "" || len(detailed.ChildRunIDs) != 1 ||
		detailed.ChildRunIDs[0] != ordinaryChild.ID {
		t.Fatalf("sanitized ordinary detail = %#v", detailed)
	}
	graph := httptest.NewRecorder()
	mux.ServeHTTP(
		graph,
		httptest.NewRequest(
			http.MethodGet,
			"/api/workflows/runs/"+ordinary.ID+"/graph",
			nil,
		),
	)
	if graph.Code != http.StatusOK ||
		strings.Contains(graph.Body.String(), attention.ID) ||
		strings.Contains(graph.Body.String(), attention.WorkflowRef) {
		t.Fatalf("ordinary graph leaked attention run: (%d, %s)", graph.Code, graph.Body.String())
	}
	var projectedGraph workflows.RunGraph
	if err := json.Unmarshal(graph.Body.Bytes(), &projectedGraph); err != nil {
		t.Fatalf("decode graph: %v", err)
	}
	for _, node := range projectedGraph.Nodes {
		if node.ParentRunID == attention.ID || node.RetryOfRunID == attention.ID {
			t.Fatalf("attention relationship leaked in graph node: %#v", node)
		}
	}

	tests := []struct {
		name   string
		method string
		path   string
		body   string
	}{
		{
			name: "detail", method: http.MethodGet,
			path: "/api/workflows/runs/" + attention.ID,
		},
		{
			name: "events", method: http.MethodGet,
			path: "/api/workflows/runs/" + attention.ID + "/events",
		},
		{
			name:   "event stream",
			method: http.MethodGet,
			path: "/api/workflows/runs/" + attention.ID +
				"/events/stream?once=true",
		},
		{
			name: "graph", method: http.MethodGet,
			path: "/api/workflows/runs/" + attention.ID + "/graph",
		},
		{
			name: "tasks", method: http.MethodGet,
			path: "/api/workflows/runs/" + attention.ID + "/tasks",
		},
		{
			name: "resume task", method: http.MethodPost,
			path: "/api/workflows/runs/" + attention.ID +
				"/tasks/ht_private/resume",
			body: `{}`,
		},
		{
			name: "cancel task", method: http.MethodPost,
			path: "/api/workflows/runs/" + attention.ID +
				"/tasks/ht_private/cancel",
			body: `{"reason":"stop"}`,
		},
		{
			name: "cancel run", method: http.MethodPost,
			path: "/api/workflows/runs/" + attention.ID + "/cancel",
			body: `{"reason":"stop"}`,
		},
		{
			name: "retry run", method: http.MethodPost,
			path: "/api/workflows/runs/" + attention.ID + "/retry",
			body: `{}`,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var body *strings.Reader
			if test.body == "" {
				body = strings.NewReader("")
			} else {
				body = strings.NewReader(test.body)
			}
			request := httptest.NewRequest(test.method, test.path, body)
			if test.body != "" {
				request.Header.Set("Content-Type", "application/json")
			}
			recorder := httptest.NewRecorder()
			mux.ServeHTTP(recorder, request)
			if recorder.Code != http.StatusNotFound {
				t.Fatalf(
					"%s %s status = %d, want 404; body=%s",
					test.method,
					test.path,
					recorder.Code,
					recorder.Body.String(),
				)
			}
			if strings.Contains(recorder.Body.String(), attention.ID) ||
				strings.Contains(recorder.Body.String(), attention.WorkflowRef) {
				t.Fatalf("attention identity leaked: %s", recorder.Body.String())
			}
		})
	}

	blockedCancel := httptest.NewRecorder()
	mux.ServeHTTP(
		blockedCancel,
		httptest.NewRequest(
			http.MethodPost,
			"/api/workflows/runs/"+ordinary.ID+"/cancel",
			strings.NewReader(`{"reason":"operator cancel"}`),
		),
	)
	if blockedCancel.Code != http.StatusNotFound {
		t.Fatalf("related ordinary cancel = (%d, %s), want 404", blockedCancel.Code, blockedCancel.Body.String())
	}
	storedOrdinary, err := store.GetRun(context.Background(), ordinary.ID)
	if err != nil || storedOrdinary.Status != workflows.RunStatusRunning {
		t.Fatalf("related ordinary changed = (%#v, %v)", storedOrdinary, err)
	}

	unrelatedCancel := httptest.NewRecorder()
	mux.ServeHTTP(
		unrelatedCancel,
		httptest.NewRequest(
			http.MethodPost,
			"/api/workflows/runs/"+unrelated.ID+"/cancel",
			strings.NewReader(`{"reason":"operator cancel"}`),
		),
	)
	if unrelatedCancel.Code != http.StatusOK {
		t.Fatalf("unrelated ordinary cancel = (%d, %s), want 200", unrelatedCancel.Code, unrelatedCancel.Body.String())
	}

	waiting := startWorkflowHumanTaskRun(
		t,
		workspace,
		"wr_eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee",
	)
	waitingTasks := listStoredWorkflowHumanTasks(t, workspace, waiting.RunID)
	if len(waitingTasks) != 1 {
		t.Fatalf("waiting tasks = %#v, want one", waitingTasks)
	}
	taskAttention := &workflows.Run{
		ID:          "wr_ffffffffffffffffffffffffffffffff",
		WorkflowRef: attentionWorkflowRef,
		Status:      workflows.RunStatusRunning,
		ParentRunID: waiting.RunID,
		CreatedAt:   now,
		UpdatedAt:   now,
	}
	if createErr := store.CreateRun(
		context.Background(),
		taskAttention,
	); createErr != nil {
		t.Fatalf("CreateRun(task attention) error = %v", createErr)
	}
	blockedTaskCancel := httptest.NewRecorder()
	mux.ServeHTTP(
		blockedTaskCancel,
		httptest.NewRequest(
			http.MethodPost,
			"/api/workflows/runs/"+waiting.RunID+"/tasks/"+
				waitingTasks[0].ID+"/cancel",
			strings.NewReader(`{"reason":"operator cancel"}`),
		),
	)
	if blockedTaskCancel.Code != http.StatusNotFound {
		t.Fatalf(
			"related task cancel = (%d, %s), want 404",
			blockedTaskCancel.Code,
			blockedTaskCancel.Body.String(),
		)
	}
	storedWaiting, err := store.GetRun(context.Background(), waiting.RunID)
	if err != nil || storedWaiting.Status != workflows.RunStatusWaiting {
		t.Fatalf("waiting run changed = (%#v, %v)", storedWaiting, err)
	}
	storedTaskAttention, err := store.GetRun(context.Background(), taskAttention.ID)
	if err != nil || storedTaskAttention.Status != workflows.RunStatusRunning {
		t.Fatalf("task attention changed = (%#v, %v)", storedTaskAttention, err)
	}

	cfg, revision, err := config.LoadConfigForUpdateSnapshot(configPath)
	if err != nil {
		t.Fatalf("LoadConfigForUpdateSnapshot() error = %v", err)
	}
	cfg.Workflows.Enabled = false
	if _, saveErr := config.SaveConfigIfRevision(
		configPath,
		cfg,
		revision,
	); saveErr != nil {
		t.Fatalf("SaveConfigIfRevision() error = %v", saveErr)
	}
	previousRunners := newWorkflowRuntimeRunners
	t.Cleanup(func() { newWorkflowRuntimeRunners = previousRunners })
	runtimeCalls := 0
	newWorkflowRuntimeRunners = func(string) workflowRuntimeRunners {
		runtimeCalls++
		return workflowRuntimeRunners{}
	}
	disabledResume := httptest.NewRecorder()
	mux.ServeHTTP(
		disabledResume,
		httptest.NewRequest(
			http.MethodPost,
			"/api/workflows/runs/"+attention.ID+"/tasks/ht_private/resume",
			strings.NewReader(`{}`),
		),
	)
	if disabledResume.Code != http.StatusNotFound || runtimeCalls != 0 {
		t.Fatalf(
			"disabled reserved resume = (%d, %s), runtime calls=%d; want 404 without runtime",
			disabledResume.Code,
			disabledResume.Body.String(),
			runtimeCalls,
		)
	}

	stored, err := store.GetRun(context.Background(), attention.ID)
	if err != nil || stored.Status != workflows.RunStatusRunning {
		t.Fatalf("attention run changed = (%#v, %v)", stored, err)
	}
	runs, err := store.ListRuns(context.Background())
	if err != nil || len(runs) != 6 {
		t.Fatalf("durable runs after rejected operations = (%#v, %v)", runs, err)
	}
}

func TestWorkflowBrowserRetentionPreservesReviewAttentionRuns(t *testing.T) {
	workspace := t.TempDir()
	configPath := writeWorkflowAITestConfig(t, workspace)
	cfg, revision, err := config.LoadConfigForUpdateSnapshot(configPath)
	if err != nil {
		t.Fatal(err)
	}
	cfg.Workflows.RetentionDays = 1
	if _, err := config.SaveConfigIfRevision(configPath, cfg, revision); err != nil {
		t.Fatal(err)
	}
	store := workflows.NewFileRunStore(workspace)
	now := time.Now().UTC()
	old := now.Add(-72 * time.Hour)
	attention := &workflows.Run{
		ID:          "wr_11111111111111111111111111111111",
		WorkflowRef: "inline/review-attention-gates/v1",
		Status:      workflows.RunStatusSucceeded,
		CreatedAt:   old,
		UpdatedAt:   old,
		CompletedAt: &old,
	}
	ordinary := &workflows.Run{
		ID:          "wr_22222222222222222222222222222222",
		WorkflowRef: "workflows/expired.yml",
		Status:      workflows.RunStatusFailed,
		CreatedAt:   old,
		UpdatedAt:   old,
		CompletedAt: &old,
	}
	for _, run := range []*workflows.Run{attention, ordinary} {
		if err := store.CreateRun(context.Background(), run); err != nil {
			t.Fatalf("CreateRun(%s) error = %v", run.ID, err)
		}
	}
	handler := NewHandler(configPath)
	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)
	response := httptest.NewRecorder()
	mux.ServeHTTP(
		response,
		httptest.NewRequest(http.MethodGet, "/api/workflows/runs", nil),
	)
	if response.Code != http.StatusOK || strings.Contains(response.Body.String(), attention.ID) {
		t.Fatalf("retained attention list response = (%d, %s)", response.Code, response.Body.String())
	}
	if _, err := store.GetRun(context.Background(), attention.ID); err != nil {
		t.Fatalf("retained attention GetRun() error = %v", err)
	}
	if _, err := store.GetRun(context.Background(), ordinary.ID); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expired ordinary GetRun() error = %v, want not exist", err)
	}
}

func TestWorkflowAttentionPrivacyNormalizesMalformedRelationshipIDs(t *testing.T) {
	const (
		hiddenID   = "wr_33333333333333333333333333333333"
		ordinaryID = "wr_44444444444444444444444444444444"
	)
	snapshot := newWorkflowRunPrivacySnapshot([]workflows.Run{
		{
			ID:          " " + hiddenID + " ",
			WorkflowRef: "inline/review-attention-gates/v1",
		},
		{
			ID:           ordinaryID,
			WorkflowRef:  "workflows/ordinary.yml",
			ParentRunID:  hiddenID,
			ChildRunIDs:  []string{" " + hiddenID + " "},
			RetryOfRunID: " " + hiddenID + " ",
		},
	})
	safe := snapshot.sanitizeRun(&workflows.Run{
		ID:           ordinaryID,
		WorkflowRef:  "workflows/ordinary.yml",
		ParentRunID:  hiddenID,
		ChildRunIDs:  []string{" " + hiddenID + " "},
		RetryOfRunID: " " + hiddenID + " ",
	})
	if safe == nil || safe.ParentRunID != "" || safe.RetryOfRunID != "" ||
		len(safe.ChildRunIDs) != 0 || snapshot.runMutationAllowed(ordinaryID) {
		t.Fatalf(
			"normalized privacy snapshot = safe %#v, mutation allowed %t",
			safe,
			snapshot.runMutationAllowed(ordinaryID),
		)
	}
	graph := projectWorkflowRunGraphWithoutAttention(&workflows.RunGraph{
		RunID: ordinaryID,
		Nodes: []workflows.RunGraphNode{{
			ID: ordinaryID, WorkflowRef: "workflows/ordinary.yml",
			ParentRunID: hiddenID, RetryOfRunID: hiddenID,
		}},
		Edges: []workflows.RunGraphEdge{{From: hiddenID, To: ordinaryID, Kind: "child"}},
	}, snapshot)
	if graph == nil || len(graph.Nodes) != 1 || len(graph.Edges) != 0 ||
		graph.Nodes[0].ParentRunID != "" || graph.Nodes[0].RetryOfRunID != "" {
		t.Fatalf("normalized private graph = %#v", graph)
	}
}
