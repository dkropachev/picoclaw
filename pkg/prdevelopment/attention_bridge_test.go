package prdevelopment

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	sharedattention "github.com/sipeed/picoclaw/pkg/attention"
	"github.com/sipeed/picoclaw/pkg/eventing"
	"github.com/sipeed/picoclaw/pkg/workflows"
)

type developmentAttentionBridgeTestStore struct {
	*attentionRuntimeStore
	projection eventing.PRDevelopmentAttentionTriggerCaseSnapshot
}

func (store *developmentAttentionBridgeTestStore) GetPRDevelopmentConversation(
	_ context.Context,
	caseID string,
) (eventing.PRDevelopmentConversation, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	if caseID != store.snapshot.Case.ID {
		return eventing.PRDevelopmentConversation{}, eventing.ErrNotFound
	}
	return store.snapshot.Conversation, nil
}

func (store *developmentAttentionBridgeTestStore) AppendPRDevelopmentMessage(
	context.Context,
	eventing.PRDevelopmentMessageAppend,
) (eventing.PRDevelopmentConversation, error) {
	return eventing.PRDevelopmentConversation{}, ErrUnavailable
}

func (store *developmentAttentionBridgeTestStore) GetCurrentPRDevelopmentAttentionTriggerForCase(
	_ context.Context,
	caseID string,
) (eventing.PRDevelopmentAttentionTriggerCaseSnapshot, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	if caseID != store.projection.CaseID {
		return eventing.PRDevelopmentAttentionTriggerCaseSnapshot{}, eventing.ErrNotFound
	}
	result := store.projection
	if result.Trigger != nil {
		trigger := *result.Trigger
		trigger.PinnedPolicy = append(json.RawMessage(nil), trigger.PinnedPolicy...)
		result.Trigger = &trigger
	}
	return result, nil
}

func (store *developmentAttentionBridgeTestStore) advanceConversationVersion() {
	store.mu.Lock()
	defer store.mu.Unlock()
	store.projection.ConversationVersion++
	store.snapshot.Conversation.Version = store.projection.ConversationVersion
}

func (store *developmentAttentionBridgeTestStore) mutateProjection(
	mutate func(*eventing.PRDevelopmentAttentionTriggerCaseSnapshot),
) {
	store.mu.Lock()
	defer store.mu.Unlock()
	mutate(&store.projection)
}

type developmentAttentionBridgeFixture struct {
	bridge *AttentionBridge
	store  *developmentAttentionBridgeTestStore
	caseID string
}

func newDevelopmentAttentionBridgeFixture(t *testing.T) *developmentAttentionBridgeFixture {
	t.Helper()
	runtimeFixture := newAttentionRuntimeFixture(t)
	gates := []workflows.GateSpec{{
		ID:        "compatibility",
		Kind:      workflows.GateDeterministic,
		When:      "true",
		Title:     "Choose compatibility behavior",
		Questions: []any{"Which compatibility contract should continue?"},
	}}
	launcher := runtimeFixture.launcher(t, gates, nil)
	result, err := launcher.Launch(context.Background(), AttentionLaunchRequest{
		CaseID:        runtimeFixture.snapshot.Case.ID,
		DecisionPoint: eventing.PRDevelopmentAttentionDecisionReviewRequired,
	})
	if err != nil || result.RunID == "" || result.SubjectRevision == "" ||
		result.Status != workflows.RunStatusWaiting {
		t.Fatalf("Launch() = (%#v, %v), want waiting private run", result, err)
	}
	prepared, err := sharedattention.PrepareSnapshot(sharedattention.PolicySnapshot{
		Revision: "attention-runtime-policy-v1",
		Global:   gates,
	})
	if err != nil || prepared.DecisionRevision() != result.PolicyRevision {
		t.Fatalf("PrepareSnapshot() = (%q, %v), want %q", prepared.DecisionRevision(), err, result.PolicyRevision)
	}
	now := time.Now().UTC()
	trigger := eventing.PRDevelopmentAttentionTrigger{
		ReviewEntryID:       result.ReviewEntryID,
		ReviewEntryHash:     runtimeFixture.snapshot.ReviewEntry.EntryHash,
		CaseID:              result.CaseID,
		ConversationVersion: result.ConversationVersion,
		TranscriptDigest:    runtimeFixture.snapshot.HighWater.TranscriptDigest,
		DecisionPoint:       result.DecisionPoint,
		Status:              eventing.PRDevelopmentAttentionTriggerDelivered,
		Attempts:            1,
		AvailableAt:         now,
		PolicyRevision:      result.PolicyRevision,
		PinnedPolicy:        prepared.Canonical(),
		SubjectRevision:     result.SubjectRevision,
		RunID:               result.RunID,
		CreatedAt:           now,
		UpdatedAt:           now,
		CompletedAt:         &now,
	}
	store := &developmentAttentionBridgeTestStore{
		attentionRuntimeStore: runtimeFixture.store,
		projection: eventing.PRDevelopmentAttentionTriggerCaseSnapshot{
			CaseID:                 result.CaseID,
			ConversationVersion:    runtimeFixture.snapshot.Conversation.Version,
			CurrentReviewEntryID:   trigger.ReviewEntryID,
			CurrentReviewEntryHash: trigger.ReviewEntryHash,
			CurrentReviewOutcome:   eventing.PRDevelopmentLedgerReviewAttentionRequired,
			AttentionRequired:      true,
			Trigger:                &trigger,
			TriggerCurrent:         true,
		},
	}
	service, err := NewService(ServiceConfig{Store: store})
	if err != nil {
		t.Fatalf("NewService() error = %v", err)
	}
	bridge, err := NewAttentionBridge(AttentionBridgeConfig{
		Service:  service,
		Executor: launcher.executor,
		RunStore: runtimeFixture.runs,
	})
	if err != nil {
		t.Fatalf("NewAttentionBridge() error = %v", err)
	}
	return &developmentAttentionBridgeFixture{
		bridge: bridge,
		store:  store,
		caseID: result.CaseID,
	}
}

func TestPRDevelopmentAttentionBridgeFencesConversationAndRespondsExactly(
	t *testing.T,
) {
	fixture := newDevelopmentAttentionBridgeFixture(t)
	first, err := fixture.bridge.Project(context.Background(), fixture.caseID)
	if err != nil || first.CaseVersion != 2 || first.Status != AttentionStatusWaiting ||
		!first.CanRespond || len(first.Turns) != 1 ||
		!sharedattention.ValidConversationResponseToken(first.Turns[0].ResponseToken) {
		t.Fatalf("Project() = (%#v, %v), want one actionable turn", first, err)
	}
	oldToken := first.Turns[0].ResponseToken
	fixture.store.advanceConversationVersion()
	second, err := fixture.bridge.Project(context.Background(), fixture.caseID)
	if err != nil || second.CaseVersion != 3 || second.Status != AttentionStatusWaiting ||
		second.Turns[0].ResponseToken != oldToken {
		t.Fatalf("Project() after chat = (%#v, %v), want stable task and new case fence", second, err)
	}
	_, err = fixture.bridge.Respond(context.Background(), AttentionResponseRequest{
		CaseID:              fixture.caseID,
		ExpectedCaseVersion: first.CaseVersion,
		ResponseToken:       oldToken,
		Response:            "Preserve compatibility",
	})
	if !errors.Is(err, eventing.ErrPRDevelopmentConversationConflict) {
		t.Fatalf("stale Respond() error = %v, want conversation conflict", err)
	}
	request := AttentionResponseRequest{
		CaseID:              fixture.caseID,
		ExpectedCaseVersion: second.CaseVersion,
		ResponseToken:       second.Turns[0].ResponseToken,
		Response:            "Preserve compatibility",
	}
	completed, err := fixture.bridge.Respond(context.Background(), request)
	if err != nil || completed.Status != AttentionStatusCompleted ||
		completed.CanRespond || len(completed.Turns) != 1 ||
		completed.Turns[0].Status != workflows.HumanTaskStatusAnswered ||
		completed.Turns[0].Response != request.Response ||
		completed.Turns[0].ResponseToken != "" {
		t.Fatalf("Respond() = (%#v, %v), want completed exact response", completed, err)
	}
	replayed, err := fixture.bridge.Respond(context.Background(), request)
	if err != nil || replayed.Status != AttentionStatusCompleted {
		t.Fatalf("replayed Respond() = (%#v, %v), want idempotent completion", replayed, err)
	}
	fixture.store.advanceConversationVersion()
	afterChat, err := fixture.bridge.Project(context.Background(), fixture.caseID)
	if err != nil || afterChat.CaseVersion != 4 ||
		afterChat.Status != AttentionStatusCompleted ||
		len(afterChat.Turns) != 1 || afterChat.Turns[0].Response != request.Response {
		t.Fatalf("Project() after answered-task chat = (%#v, %v), want durable completion", afterChat, err)
	}
	changed := request
	changed.Response = "Choose strict behavior"
	if _, err = fixture.bridge.Respond(context.Background(), changed); !errors.Is(
		err,
		eventing.ErrPRDevelopmentConversationConflict,
	) {
		t.Fatalf("changed replay error = %v, want conflict", err)
	}
}

func TestPRDevelopmentAttentionHandlerRoutesAreStrict(t *testing.T) {
	fixture := newDevelopmentAttentionBridgeFixture(t)
	handler := &Handler{Service: fixture.bridge.service, Attention: fixture.bridge}

	get := httptest.NewRequest(
		http.MethodGet,
		RuntimeRoutePrefix+"/"+fixture.caseID+"/attention",
		nil,
	)
	getRecorder := httptest.NewRecorder()
	handler.ServeHTTP(getRecorder, get)
	if getRecorder.Code != http.StatusOK ||
		!strings.Contains(getRecorder.Body.String(), `"status":"waiting"`) ||
		strings.Contains(getRecorder.Body.String(), "workflow") ||
		strings.Contains(getRecorder.Body.String(), "policy_revision") {
		t.Fatalf("GET attention = %d %s", getRecorder.Code, getRecorder.Body.String())
	}
	var view AttentionView
	if err := json.Unmarshal(getRecorder.Body.Bytes(), &view); err != nil ||
		len(view.Turns) != 1 {
		t.Fatalf("decode GET attention = (%#v, %v)", view, err)
	}
	body, err := json.Marshal(map[string]any{
		"expected_case_version": view.CaseVersion,
		"response_token":        view.Turns[0].ResponseToken,
		"response":              "Preserve compatibility",
	})
	if err != nil {
		t.Fatal(err)
	}
	post := httptest.NewRequest(
		http.MethodPost,
		RuntimeRoutePrefix+"/"+fixture.caseID+"/attention/respond",
		bytes.NewReader(body),
	)
	post.Header.Set("Content-Type", "application/json")
	postRecorder := httptest.NewRecorder()
	handler.ServeHTTP(postRecorder, post)
	if postRecorder.Code != http.StatusOK ||
		!strings.Contains(postRecorder.Body.String(), `"status":"completed"`) {
		t.Fatalf("POST attention response = %d %s", postRecorder.Code, postRecorder.Body.String())
	}

	for _, test := range []struct {
		name   string
		method string
		path   string
		body   []byte
		header map[string]string
		status int
	}{
		{
			name: "wrong get method", method: http.MethodPost,
			path:   RuntimeRoutePrefix + "/" + fixture.caseID + "/attention",
			status: http.StatusMethodNotAllowed,
		},
		{
			name: "wrong response method", method: http.MethodGet,
			path:   RuntimeRoutePrefix + "/" + fixture.caseID + "/attention/respond",
			status: http.StatusMethodNotAllowed,
		},
		{
			name: "cross site", method: http.MethodPost,
			path: RuntimeRoutePrefix + "/" + fixture.caseID + "/attention/respond",
			body: body, header: map[string]string{
				"Content-Type": "application/json",
				"Origin":       "https://attacker.example",
			},
			status: http.StatusForbidden,
		},
		{
			name: "unknown field", method: http.MethodPost,
			path:   RuntimeRoutePrefix + "/" + fixture.caseID + "/attention/respond",
			body:   append(bytes.TrimSuffix(body, []byte("}")), []byte(`,"private":"x"}`)...),
			header: map[string]string{"Content-Type": "application/json"},
			status: http.StatusBadRequest,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(test.method, test.path, bytes.NewReader(test.body))
			for name, value := range test.header {
				request.Header.Set(name, value)
			}
			recorder := httptest.NewRecorder()
			handler.ServeHTTP(recorder, request)
			if recorder.Code != test.status {
				t.Fatalf("ServeHTTP() = %d %s, want %d", recorder.Code, recorder.Body.String(), test.status)
			}
		})
	}
}

func TestPRDevelopmentAttentionBridgeProjectsOnlyCurrentPublicLifecycle(t *testing.T) {
	zeroPolicy, err := sharedattention.PrepareSnapshot(sharedattention.PolicySnapshot{
		Revision: "attention-bridge-zero-v1",
		Global:   []workflows.GateSpec{{ID: "off", Kind: workflows.GateZero}},
	})
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name       string
		mutate     func(*eventing.PRDevelopmentAttentionTriggerCaseSnapshot)
		wantStatus string
		wantErr    error
	}{
		{
			name: "pending pinned occurrence",
			mutate: func(snapshot *eventing.PRDevelopmentAttentionTriggerCaseSnapshot) {
				snapshot.Trigger.Status = eventing.PRDevelopmentAttentionTriggerPending
				snapshot.Trigger.RunID = ""
				snapshot.Trigger.CompletedAt = nil
			},
			wantStatus: AttentionStatusQueued,
		},
		{
			name: "claimed occurrence",
			mutate: func(snapshot *eventing.PRDevelopmentAttentionTriggerCaseSnapshot) {
				until := time.Now().UTC().Add(time.Minute)
				snapshot.Trigger.Status = eventing.PRDevelopmentAttentionTriggerClaimed
				snapshot.Trigger.RunID = ""
				snapshot.Trigger.CompletedAt = nil
				snapshot.Trigger.LeaseToken = "opaque-lease"
				snapshot.Trigger.LeaseUntil = &until
			},
			wantStatus: AttentionStatusChecking,
		},
		{
			name: "admission recovery is terminal without task authority",
			mutate: func(snapshot *eventing.PRDevelopmentAttentionTriggerCaseSnapshot) {
				snapshot.Trigger.Status = eventing.PRDevelopmentAttentionTriggerRecoveryRequired
				snapshot.Trigger.RunID = ""
			},
			wantStatus: AttentionStatusRecoveryRequired,
		},
		{
			name: "failed occurrence",
			mutate: func(snapshot *eventing.PRDevelopmentAttentionTriggerCaseSnapshot) {
				snapshot.Trigger.Status = eventing.PRDevelopmentAttentionTriggerFailed
				snapshot.Trigger.RunID = ""
			},
			wantStatus: AttentionStatusFailed,
		},
		{
			name: "zero gate",
			mutate: func(snapshot *eventing.PRDevelopmentAttentionTriggerCaseSnapshot) {
				snapshot.Trigger.Status = eventing.PRDevelopmentAttentionTriggerNoop
				snapshot.Trigger.RunID = ""
				snapshot.Trigger.SubjectRevision = ""
				snapshot.Trigger.PolicyRevision = zeroPolicy.DecisionRevision()
				snapshot.Trigger.PinnedPolicy = zeroPolicy.Canonical()
			},
			wantStatus: AttentionStatusNotRequired,
		},
		{
			name: "later nonattention review hides historical trigger",
			mutate: func(snapshot *eventing.PRDevelopmentAttentionTriggerCaseSnapshot) {
				snapshot.TriggerCurrent = false
				snapshot.AttentionRequired = false
				snapshot.CurrentReviewOutcome = eventing.PRDevelopmentLedgerReviewChangesRequired
			},
			wantStatus: AttentionStatusNone,
		},
		{
			name: "migrated attention review has no synthetic occurrence",
			mutate: func(snapshot *eventing.PRDevelopmentAttentionTriggerCaseSnapshot) {
				snapshot.Trigger = nil
				snapshot.TriggerCurrent = false
			},
			wantStatus: AttentionStatusNone,
		},
		{
			name: "corrupt policy fails closed",
			mutate: func(snapshot *eventing.PRDevelopmentAttentionTriggerCaseSnapshot) {
				snapshot.Trigger.PinnedPolicy = json.RawMessage(`{"invalid":true}`)
			},
			wantErr: ErrUnavailable,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newDevelopmentAttentionBridgeFixture(t)
			fixture.store.mutateProjection(test.mutate)
			view, projectErr := fixture.bridge.Project(context.Background(), fixture.caseID)
			if test.wantErr != nil {
				if !errors.Is(projectErr, test.wantErr) {
					t.Fatalf("Project() = (%#v, %v), want %v", view, projectErr, test.wantErr)
				}
				return
			}
			if projectErr != nil || view.Status != test.wantStatus ||
				view.CanRespond || len(view.Turns) != 0 {
				t.Fatalf(
					"Project() = (%#v, %v), want status %q without task authority",
					view,
					projectErr,
					test.wantStatus,
				)
			}
		})
	}
}
