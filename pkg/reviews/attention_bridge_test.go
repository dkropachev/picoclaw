package reviews

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sipeed/picoclaw/pkg/eventing"
	"github.com/sipeed/picoclaw/pkg/workflows"
)

const (
	attentionBridgeSecondCaseID = "prc_22222222222222222222222222222222"
	attentionBridgeSubmissionID = "prs_99999999999999999999999999999999"
)

func TestAttentionBridgeProjectsCaseOwnedTriggerStates(t *testing.T) {
	detail := attentionBridgeSubmittedDetail(
		serviceTestCaseID,
		attentionBridgeSubmissionID,
		12,
	)
	now := detail.Case.UpdatedAt.Add(time.Minute)
	baseTrigger := eventing.ReviewAttentionTrigger{
		SubmissionID:  detail.Submission.ID,
		CaseID:        detail.Case.ID,
		CaseVersion:   detail.Case.Version,
		DecisionPoint: eventing.ReviewAttentionDecisionSubmitted,
		AvailableAt:   now,
		CreatedAt:     now,
		UpdatedAt:     now,
	}
	tests := []struct {
		name       string
		status     eventing.ReviewAttentionTriggerStatus
		missing    bool
		wantStatus string
	}{
		{name: "missing migrated occurrence", missing: true, wantStatus: AttentionStatusNone},
		{name: "pending", status: eventing.ReviewAttentionPending, wantStatus: AttentionStatusQueued},
		{name: "claimed", status: eventing.ReviewAttentionClaimed, wantStatus: AttentionStatusProcessing},
		{name: "no-op", status: eventing.ReviewAttentionNoop, wantStatus: AttentionStatusNotRequired},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store := newAttentionBridgeTestStore(detail)
			if !test.missing {
				trigger := baseTrigger
				trigger.Status = test.status
				if test.status == eventing.ReviewAttentionNoop {
					trigger.PolicyRevision, trigger.PinnedPolicy = attentionBridgeTestPin(
						t,
						[]workflows.GateSpec{{ID: "off", Kind: workflows.GateZero}},
					)
					trigger.CompletedAt = &now
				}
				store.setTrigger(trigger)
			}
			service := newAttentionTestService(t, store, nil, nil)
			bridge, err := NewAttentionBridge(AttentionBridgeConfig{
				Service:  service,
				RunStore: workflows.NewFileRunStore(t.TempDir()),
			})
			if err != nil {
				t.Fatalf("NewAttentionBridge() error = %v", err)
			}
			view, err := bridge.Project(context.Background(), detail.Case.ID)
			if err != nil {
				t.Fatalf("Project() error = %v", err)
			}
			if view.CaseVersion != detail.Case.Version || view.Status != test.wantStatus ||
				view.CanRespond || len(view.Turns) != 0 {
				t.Fatalf("Project() = %#v, want status %q", view, test.wantStatus)
			}
		})
	}

	open := detail
	open.Case.Status = eventing.ReviewCaseOpen
	open.Case.ResolvedAt = nil
	open.Case.SubmittedAt = nil
	open.Submission = nil
	store := newAttentionBridgeTestStore(open)
	service := newAttentionTestService(t, store, nil, nil)
	bridge, err := NewAttentionBridge(AttentionBridgeConfig{
		Service: service, RunStore: workflows.NewFileRunStore(t.TempDir()),
	})
	if err != nil {
		t.Fatal(err)
	}
	view, err := bridge.Project(context.Background(), open.Case.ID)
	if err != nil || view.Status != AttentionStatusNone || view.CaseVersion != open.Case.Version {
		t.Fatalf("open Project() = (%#v, %v)", view, err)
	}
}

func TestAttentionBridgeRespondsAcrossMultipleGatesAndReplaysExactly(t *testing.T) {
	fixture := newAttentionBridgeWaitingFixture(t, serviceTestCaseID, attentionBridgeSubmissionID)

	initial, err := fixture.bridge.Project(context.Background(), serviceTestCaseID)
	if err != nil {
		t.Fatalf("Project() error = %v", err)
	}
	if initial.Status != AttentionStatusWaiting || !initial.CanRespond ||
		len(initial.Turns) != 1 || initial.Turns[0].Status != workflows.HumanTaskStatusWaiting ||
		!validAttentionResponseToken(initial.Turns[0].ResponseToken) {
		t.Fatalf("initial projection = %#v", initial)
	}
	firstToken := initial.Turns[0].ResponseToken
	assertAttentionProjectionHidesPrivateState(t, fixture, initial)

	readOnly, err := NewAttentionBridge(AttentionBridgeConfig{
		Service: fixture.service, RunStore: fixture.runStore,
	})
	if err != nil {
		t.Fatal(err)
	}
	readOnlyView, err := readOnly.Project(context.Background(), serviceTestCaseID)
	if err != nil || readOnlyView.CanRespond || readOnlyView.Turns[0].ResponseToken != "" {
		t.Fatalf("read-only Project() = (%#v, %v)", readOnlyView, err)
	}
	if _, err = readOnly.Respond(context.Background(), AttentionResponseRequest{
		CaseID: serviceTestCaseID, ExpectedCaseVersion: initial.CaseVersion,
		ResponseToken: firstToken, Response: "Use the safer option.",
	}); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("read-only Respond() error = %v, want unavailable", err)
	}

	firstResponse := "Use the safer option."
	afterFirst, err := fixture.bridge.Respond(context.Background(), AttentionResponseRequest{
		CaseID: serviceTestCaseID, ExpectedCaseVersion: initial.CaseVersion,
		ResponseToken: firstToken, Response: "  " + firstResponse + "  ",
	})
	if err != nil {
		t.Fatalf("first Respond() error = %v", err)
	}
	if afterFirst.Status != AttentionStatusWaiting || !afterFirst.CanRespond ||
		len(afterFirst.Turns) != 2 ||
		afterFirst.Turns[0].Status != workflows.HumanTaskStatusAnswered ||
		afterFirst.Turns[0].Response != firstResponse ||
		afterFirst.Turns[0].ResponseToken != "" ||
		afterFirst.Turns[1].Status != workflows.HumanTaskStatusWaiting ||
		!validAttentionResponseToken(afterFirst.Turns[1].ResponseToken) ||
		afterFirst.Turns[1].ResponseToken == firstToken {
		t.Fatalf("after first response = %#v", afterFirst)
	}
	secondToken := afterFirst.Turns[1].ResponseToken
	assertAttentionProjectionHidesPrivateState(t, fixture, afterFirst)
	readOnlyAfterFirst, err := readOnly.Project(context.Background(), serviceTestCaseID)
	if err != nil || readOnlyAfterFirst.CanRespond ||
		readOnlyAfterFirst.Turns[1].ResponseToken != "" {
		t.Fatalf("read-only post-answer Project() = (%#v, %v)", readOnlyAfterFirst, err)
	}
	readOnlyReplay, err := readOnly.Respond(context.Background(), AttentionResponseRequest{
		CaseID: serviceTestCaseID, ExpectedCaseVersion: initial.CaseVersion,
		ResponseToken: firstToken, Response: firstResponse,
	})
	if err != nil || !reflect.DeepEqual(readOnlyReplay, readOnlyAfterFirst) {
		t.Fatalf(
			"read-only accepted replay = (%#v, %v), want %#v",
			readOnlyReplay,
			err,
			readOnlyAfterFirst,
		)
	}
	if _, err = readOnly.Respond(context.Background(), AttentionResponseRequest{
		CaseID: serviceTestCaseID, ExpectedCaseVersion: initial.CaseVersion,
		ResponseToken: firstToken, Response: "Use the faster option.",
	}); !errors.Is(err, eventing.ErrReviewConflict) {
		t.Fatalf("read-only altered replay error = %v, want conflict", err)
	}

	replayed, err := fixture.bridge.Respond(context.Background(), AttentionResponseRequest{
		CaseID: serviceTestCaseID, ExpectedCaseVersion: initial.CaseVersion,
		ResponseToken: firstToken, Response: firstResponse,
	})
	if err != nil || !reflect.DeepEqual(replayed, afterFirst) {
		t.Fatalf("exact first replay = (%#v, %v), want %#v", replayed, err, afterFirst)
	}
	if _, err = fixture.bridge.Respond(context.Background(), AttentionResponseRequest{
		CaseID: serviceTestCaseID, ExpectedCaseVersion: initial.CaseVersion,
		ResponseToken: firstToken, Response: "Use the faster option.",
	}); !errors.Is(err, eventing.ErrReviewConflict) {
		t.Fatalf("altered replay error = %v, want conflict", err)
	}
	if _, err = fixture.bridge.Respond(context.Background(), AttentionResponseRequest{
		CaseID: serviceTestCaseID, ExpectedCaseVersion: initial.CaseVersion + 1,
		ResponseToken: secondToken, Response: "Proceed.",
	}); !errors.Is(err, eventing.ErrReviewConflict) {
		t.Fatalf("stale version error = %v, want conflict", err)
	}

	other := newAttentionBridgeWaitingFixture(
		t,
		attentionBridgeSecondCaseID,
		"prs_88888888888888888888888888888888",
	)
	if _, err = other.bridge.Respond(context.Background(), AttentionResponseRequest{
		CaseID:              attentionBridgeSecondCaseID,
		ExpectedCaseVersion: other.detail.Case.Version,
		ResponseToken:       secondToken,
		Response:            "Proceed.",
	}); !errors.Is(err, eventing.ErrReviewConflict) {
		t.Fatalf("cross-case token error = %v, want conflict", err)
	}

	secondResponse := "Proceed with the bounded change."
	completed, err := fixture.bridge.Respond(context.Background(), AttentionResponseRequest{
		CaseID: serviceTestCaseID, ExpectedCaseVersion: initial.CaseVersion,
		ResponseToken: secondToken, Response: secondResponse,
	})
	if err != nil {
		t.Fatalf("second Respond() error = %v", err)
	}
	if completed.Status != AttentionStatusCompleted || completed.CanRespond ||
		len(completed.Turns) != 2 || completed.Turns[1].Response != secondResponse ||
		completed.Turns[0].ResponseToken != "" || completed.Turns[1].ResponseToken != "" {
		t.Fatalf("completed projection = %#v", completed)
	}
	replayed, err = fixture.bridge.Respond(context.Background(), AttentionResponseRequest{
		CaseID: serviceTestCaseID, ExpectedCaseVersion: initial.CaseVersion,
		ResponseToken: secondToken, Response: secondResponse,
	})
	if err != nil || !reflect.DeepEqual(replayed, completed) {
		t.Fatalf("terminal exact replay = (%#v, %v), want %#v", replayed, err, completed)
	}
	readOnlyCompleted, err := readOnly.Respond(context.Background(), AttentionResponseRequest{
		CaseID: serviceTestCaseID, ExpectedCaseVersion: initial.CaseVersion,
		ResponseToken: secondToken, Response: secondResponse,
	})
	if err != nil || readOnlyCompleted.Status != AttentionStatusCompleted ||
		readOnlyCompleted.CanRespond {
		t.Fatalf("read-only terminal replay = (%#v, %v)", readOnlyCompleted, err)
	}
}

func TestAttentionBridgeProjectsCanceledGateAsFailedTurn(t *testing.T) {
	fixture := newAttentionBridgeWaitingFixture(t, serviceTestCaseID, attentionBridgeSubmissionID)
	initial, err := fixture.bridge.Project(context.Background(), serviceTestCaseID)
	if err != nil {
		t.Fatal(err)
	}
	tasks, err := fixture.runStore.ListHumanTasks(context.Background(), fixture.result.RunID)
	if err != nil || len(tasks) != 1 {
		t.Fatalf("ListHumanTasks() = (%#v, %v)", tasks, err)
	}
	if _, err = fixture.runStore.CancelHumanTask(
		context.Background(),
		fixture.result.RunID,
		tasks[0].ID,
		"operator canceled attention",
	); err != nil {
		t.Fatalf("CancelHumanTask() error = %v", err)
	}
	view, err := fixture.bridge.Project(context.Background(), serviceTestCaseID)
	if err != nil {
		t.Fatalf("Project(canceled) error = %v", err)
	}
	if view.Status != AttentionStatusFailed || view.CanRespond || len(view.Turns) != 1 ||
		view.Turns[0].Status != workflows.HumanTaskStatusCanceled ||
		view.Turns[0].Title != initial.Turns[0].Title ||
		view.Turns[0].Response != "" || view.Turns[0].ResponseToken != "" {
		t.Fatalf("canceled projection = %#v", view)
	}
	assertAttentionAuthorityInvariant(t, view)
}

func TestAttentionBridgeContinuingAndRecoveryAuthority(t *testing.T) {
	t.Run("continuing is observable but not actionable", func(t *testing.T) {
		fixture := newAttentionBridgeWaitingFixture(t, serviceTestCaseID, attentionBridgeSubmissionID)
		initial, err := fixture.bridge.Project(context.Background(), serviceTestCaseID)
		if err != nil {
			t.Fatal(err)
		}
		claimed := make(chan struct{})
		release := make(chan struct{})
		var releaseOnce sync.Once
		defer releaseOnce.Do(func() { close(release) })
		fixture.bridge.executor.AdmittedHumanTaskClaim = func(
			_ context.Context,
			_, _ string,
			claim func() (*workflows.Run, workflows.WorkflowHumanTask, bool, error),
		) (*workflows.Run, workflows.WorkflowHumanTask, bool, error) {
			run, task, duplicate, claimErr := claim()
			if claimErr == nil {
				close(claimed)
				<-release
			}
			return run, task, duplicate, claimErr
		}
		result := make(chan error, 1)
		go func() {
			_, respondErr := fixture.bridge.Respond(
				context.Background(),
				AttentionResponseRequest{
					CaseID: serviceTestCaseID, ExpectedCaseVersion: initial.CaseVersion,
					ResponseToken: initial.Turns[0].ResponseToken,
					Response:      "Continue after this choice.",
				},
			)
			result <- respondErr
		}()
		select {
		case <-claimed:
		case <-time.After(5 * time.Second):
			t.Fatal("response claim did not become durable")
		}
		continuing, err := fixture.bridge.Project(context.Background(), serviceTestCaseID)
		if err != nil {
			t.Fatalf("Project(continuing) error = %v", err)
		}
		if continuing.Status != AttentionStatusContinuing || continuing.CanRespond ||
			len(continuing.Turns) != 1 ||
			continuing.Turns[0].Status != workflows.HumanTaskStatusContinuing ||
			continuing.Turns[0].Response != "Continue after this choice." ||
			continuing.Turns[0].ResponseToken != "" {
			t.Fatalf("continuing projection = %#v", continuing)
		}
		assertAttentionAuthorityInvariant(t, continuing)
		releaseOnce.Do(func() { close(release) })
		if err = <-result; err != nil {
			t.Fatalf("Respond() completion error = %v", err)
		}
	})

	t.Run("recovery keeps the original fenced answer actionable", func(t *testing.T) {
		fixture := newAttentionBridgeWaitingFixture(t, serviceTestCaseID, attentionBridgeSubmissionID)
		initial, err := fixture.bridge.Project(context.Background(), serviceTestCaseID)
		if err != nil {
			t.Fatal(err)
		}
		tasks, err := fixture.runStore.ListHumanTasks(context.Background(), fixture.result.RunID)
		if err != nil || len(tasks) != 1 {
			t.Fatalf("ListHumanTasks() = (%#v, %v)", tasks, err)
		}
		response := "Retry the accepted continuation."
		_, _, duplicate, err := fixture.runStore.ClaimHumanTask(
			context.Background(),
			fixture.result.RunID,
			tasks[0].ID,
			workflows.HumanTaskResumeRequest{
				ExpectedRevision: tasks[0].Revision,
				InputHash:        tasks[0].InputHash,
				ResponseID: attentionResponseID(
					initial.Turns[0].ResponseToken,
					response,
				),
				Response: response,
			},
		)
		if err != nil || duplicate {
			t.Fatalf("ClaimHumanTask() = (duplicate=%v, %v)", duplicate, err)
		}
		recoveryStore := &attentionBridgeRecoveryRunStore{FileRunStore: fixture.runStore}
		recoveryExecutor := &workflows.Executor{Store: recoveryStore}
		recoveryBridge, err := NewAttentionBridge(AttentionBridgeConfig{
			Service: fixture.service, Executor: recoveryExecutor, RunStore: recoveryStore,
		})
		if err != nil {
			t.Fatal(err)
		}
		recovery, err := recoveryBridge.Project(context.Background(), serviceTestCaseID)
		if err != nil {
			t.Fatalf("Project(recovery) error = %v", err)
		}
		if recovery.Status != AttentionStatusRecoveryRequired || !recovery.CanRespond ||
			len(recovery.Turns) != 1 ||
			recovery.Turns[0].Status != workflows.HumanTaskStatusRecoveryRequired ||
			recovery.Turns[0].Response != response ||
			recovery.Turns[0].ResponseToken != initial.Turns[0].ResponseToken {
			t.Fatalf("recovery projection = %#v", recovery)
		}
		assertAttentionAuthorityInvariant(t, recovery)
		if _, err = recoveryBridge.Respond(context.Background(), AttentionResponseRequest{
			CaseID: serviceTestCaseID, ExpectedCaseVersion: initial.CaseVersion,
			ResponseToken: recovery.Turns[0].ResponseToken,
			Response:      "A different answer.",
		}); !errors.Is(err, eventing.ErrReviewConflict) {
			t.Fatalf("altered recovery response error = %v, want conflict", err)
		}

		readOnlyRecovery, err := NewAttentionBridge(AttentionBridgeConfig{
			Service: fixture.service, RunStore: recoveryStore,
		})
		if err != nil {
			t.Fatal(err)
		}
		readOnlyView, err := readOnlyRecovery.Project(context.Background(), serviceTestCaseID)
		if err != nil || readOnlyView.Status != AttentionStatusRecoveryRequired ||
			readOnlyView.CanRespond || readOnlyView.Turns[0].ResponseToken != "" {
			t.Fatalf("read-only recovery projection = (%#v, %v)", readOnlyView, err)
		}
		assertAttentionAuthorityInvariant(t, readOnlyView)
		replayed, err := readOnlyRecovery.Respond(
			context.Background(),
			AttentionResponseRequest{
				CaseID: serviceTestCaseID, ExpectedCaseVersion: initial.CaseVersion,
				ResponseToken: initial.Turns[0].ResponseToken, Response: response,
			},
		)
		if err != nil || !reflect.DeepEqual(replayed, readOnlyView) {
			t.Fatalf("read-only recovery replay = (%#v, %v), want %#v", replayed, err, readOnlyView)
		}
	})
}

func TestAttentionBridgeConflictingConcurrentResponsesHaveOneWinner(t *testing.T) {
	fixture := newAttentionBridgeWaitingFixture(t, serviceTestCaseID, attentionBridgeSubmissionID)
	initial, err := fixture.bridge.Project(context.Background(), serviceTestCaseID)
	if err != nil {
		t.Fatal(err)
	}
	responses := []string{"Choose option A.", "Choose option B."}
	errorsByResponse := make([]error, len(responses))
	start := make(chan struct{})
	var wait sync.WaitGroup
	for index := range responses {
		wait.Add(1)
		go func(index int) {
			defer wait.Done()
			<-start
			_, errorsByResponse[index] = fixture.bridge.Respond(
				context.Background(),
				AttentionResponseRequest{
					CaseID: serviceTestCaseID, ExpectedCaseVersion: initial.CaseVersion,
					ResponseToken: initial.Turns[0].ResponseToken,
					Response:      responses[index],
				},
			)
		}(index)
	}
	close(start)
	wait.Wait()
	successes, conflicts := 0, 0
	for _, err := range errorsByResponse {
		switch {
		case err == nil:
			successes++
		case errors.Is(err, eventing.ErrReviewConflict):
			conflicts++
		default:
			t.Fatalf("concurrent Respond() unexpected error = %v", err)
		}
	}
	if successes != 1 || conflicts != 1 {
		t.Fatalf("concurrent results successes=%d conflicts=%d errors=%#v", successes, conflicts, errorsByResponse)
	}
	view, err := fixture.bridge.Project(context.Background(), serviceTestCaseID)
	if err != nil || view.Status != AttentionStatusWaiting || len(view.Turns) != 2 ||
		view.Turns[0].Status != workflows.HumanTaskStatusAnswered ||
		view.Turns[1].Status != workflows.HumanTaskStatusWaiting {
		t.Fatalf("post-concurrency projection = (%#v, %v)", view, err)
	}
}

func TestAttentionBridgePreservesQuestionsAsExactJSON(t *testing.T) {
	questions := map[string]any{
		"fraction": json.Number("1.2300"),
		"large":    json.Number("9007199254740993"),
		"nested":   []any{true, nil, map[string]any{"choice": "safe"}},
	}
	fixture := newAttentionBridgeFixtureWithGates(
		t,
		serviceTestCaseID,
		attentionBridgeSubmissionID,
		[]workflows.GateSpec{{
			ID: "exact", Kind: workflows.GateDeterministic, When: "true",
			Title: "Preserve exact questions", Questions: questions,
		}},
	)
	view, err := fixture.bridge.Project(context.Background(), serviceTestCaseID)
	if err != nil || len(view.Turns) != 1 {
		t.Fatalf("Project() = (%#v, %v)", view, err)
	}
	encoded, err := json.Marshal(view.Turns[0].Questions)
	if err != nil {
		t.Fatal(err)
	}
	if got := string(encoded); got !=
		`{"fraction":1.2300,"large":9007199254740993,"nested":[true,null,{"choice":"safe"}]}` {
		t.Fatalf("questions JSON = %s", got)
	}
}

func TestAttentionBridgeHandlerRoutesAreStrict(t *testing.T) {
	fixture := newAttentionBridgeWaitingFixture(t, serviceTestCaseID, attentionBridgeSubmissionID)
	handler := &Handler{Service: fixture.service, Attention: fixture.bridge}
	attentionPath := RuntimeRoutePrefix + "/" + serviceTestCaseID + "/attention"

	request := httptest.NewRequest(http.MethodGet, attentionPath, nil)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("GET attention status = %d body=%s", response.Code, response.Body.String())
	}
	assertReviewResponseHeaders(t, response)
	var view AttentionView
	if err := json.Unmarshal(response.Body.Bytes(), &view); err != nil ||
		!view.CanRespond || len(view.Turns) != 1 {
		t.Fatalf("GET attention body = %s error=%v", response.Body.String(), err)
	}

	for _, test := range []struct {
		name   string
		method string
		target string
		body   string
		want   int
		allow  string
	}{
		{
			name: "get rejects query", method: http.MethodGet,
			target: attentionPath + "?private=true", want: http.StatusBadRequest,
		},
		{
			name: "get rejects method", method: http.MethodPost,
			target: attentionPath, want: http.StatusMethodNotAllowed, allow: http.MethodGet,
		},
		{
			name: "respond rejects unknown field", method: http.MethodPost,
			target: attentionPath + "/respond",
			body: `{"expected_case_version":12,"response_token":"` +
				view.Turns[0].ResponseToken + `","response":"Proceed.","run_id":"wr_private"}`,
			want: http.StatusBadRequest,
		},
		{
			name: "respond rejects method", method: http.MethodGet,
			target: attentionPath + "/respond", want: http.StatusMethodNotAllowed,
			allow: http.MethodPost,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			req := httptest.NewRequest(test.method, test.target, strings.NewReader(test.body))
			if test.body != "" {
				req.Header.Set("Content-Type", "application/json")
			}
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)
			if rec.Code != test.want || rec.Header().Get("Allow") != test.allow {
				t.Fatalf(
					"status=%d allow=%q body=%s, want status=%d allow=%q",
					rec.Code, rec.Header().Get("Allow"), rec.Body.String(), test.want, test.allow,
				)
			}
			assertReviewResponseHeaders(t, rec)
		})
	}

	body, err := json.Marshal(map[string]any{
		"expected_case_version": view.CaseVersion,
		"response_token":        view.Turns[0].ResponseToken,
		"response":              "Choose the safe implementation.",
	})
	if err != nil {
		t.Fatal(err)
	}
	request = httptest.NewRequest(
		http.MethodPost,
		attentionPath+"/respond",
		strings.NewReader(string(body)),
	)
	request.Header.Set("Content-Type", "application/json; charset=utf-8")
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("POST respond status=%d body=%s", response.Code, response.Body.String())
	}
	assertReviewResponseHeaders(t, response)
}

func TestAttentionBridgeRejectsMalformedAuthorityAndConfiguration(t *testing.T) {
	detail := attentionBridgeSubmittedDetail(
		serviceTestCaseID,
		attentionBridgeSubmissionID,
		12,
	)
	store := newAttentionBridgeTestStore(detail)
	service := newAttentionTestService(t, store, nil, nil)
	runs := workflows.NewFileRunStore(t.TempDir())
	if bridge, err := NewAttentionBridge(AttentionBridgeConfig{}); err == nil || bridge != nil {
		t.Fatalf("NewAttentionBridge(empty) = (%#v, %v)", bridge, err)
	}
	if bridge, err := NewAttentionBridge(AttentionBridgeConfig{
		Service: service,
	}); err == nil || bridge != nil {
		t.Fatalf("NewAttentionBridge(no run store) = (%#v, %v)", bridge, err)
	}
	bridge, err := NewAttentionBridge(AttentionBridgeConfig{Service: service, RunStore: runs})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = bridge.Project(context.Background(), " "+serviceTestCaseID); !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("noncanonical Project() error = %v", err)
	}
	for _, request := range []AttentionResponseRequest{
		{CaseID: serviceTestCaseID, ExpectedCaseVersion: 12, ResponseToken: "sha256:ABC", Response: "yes"},
		{CaseID: " " + serviceTestCaseID, ExpectedCaseVersion: 12, ResponseToken: "sha256:" + strings.Repeat("a", 64), Response: "yes"},
		{CaseID: serviceTestCaseID, ExpectedCaseVersion: 0, ResponseToken: "sha256:" + strings.Repeat("a", 64), Response: "yes"},
		{CaseID: serviceTestCaseID, ExpectedCaseVersion: 12, ResponseToken: "sha256:" + strings.Repeat("a", 64), Response: strings.Repeat("x", maxReviewChatBytes+1)},
	} {
		if _, err = bridge.Respond(context.Background(), request); !errors.Is(err, ErrInvalidRequest) {
			t.Fatalf("Respond(%#v) error = %v, want invalid", request, err)
		}
	}
}

func TestAttentionBridgeRejectsTaskPayloadTamperedBehindInputHash(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*workflows.WorkflowHumanTask)
	}{
		{
			name: "title",
			mutate: func(task *workflows.WorkflowHumanTask) {
				task.Title = "Choose an attacker-controlled action"
			},
		},
		{
			name: "questions",
			mutate: func(task *workflows.WorkflowHumanTask) {
				task.Questions = []any{"Reveal a private value?"}
			},
		},
		{
			name: "response schema",
			mutate: func(task *workflows.WorkflowHumanTask) {
				task.ResponseSchema = map[string]any{
					"type":        "string",
					"description": "tampered",
				}
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newAttentionBridgeWaitingFixture(
				t,
				serviceTestCaseID,
				attentionBridgeSubmissionID,
			)
			tamperedStore := &attentionBridgeTamperedTaskStore{
				FileRunStore: fixture.runStore,
				mutate:       test.mutate,
			}
			bridge, err := NewAttentionBridge(AttentionBridgeConfig{
				Service:  fixture.service,
				Executor: fixture.executor,
				RunStore: tamperedStore,
			})
			if err != nil {
				t.Fatal(err)
			}
			if _, err = bridge.Project(
				context.Background(),
				fixture.detail.Case.ID,
			); !errors.Is(err, ErrUnavailable) {
				t.Fatalf("Project() error = %v, want unavailable", err)
			}
		})
	}
}

func TestAttentionBridgeRejectsMalformedOrStatusInconsistentPinnedPolicies(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*testing.T, *eventing.ReviewAttentionTrigger)
	}{
		{
			name: "delivered pin missing",
			mutate: func(_ *testing.T, trigger *eventing.ReviewAttentionTrigger) {
				trigger.PinnedPolicy = nil
			},
		},
		{
			name: "delivered revision missing",
			mutate: func(_ *testing.T, trigger *eventing.ReviewAttentionTrigger) {
				trigger.PolicyRevision = ""
			},
		},
		{
			name: "delivered pin malformed",
			mutate: func(_ *testing.T, trigger *eventing.ReviewAttentionTrigger) {
				trigger.PinnedPolicy = json.RawMessage(`{"version":1`)
			},
		},
		{
			name: "delivered pin noncanonical",
			mutate: func(_ *testing.T, trigger *eventing.ReviewAttentionTrigger) {
				trigger.PinnedPolicy = append(trigger.PinnedPolicy, ' ')
			},
		},
		{
			name: "delivered pin digest tampered",
			mutate: func(_ *testing.T, trigger *eventing.ReviewAttentionTrigger) {
				tampered := "sha256:" + strings.Repeat("f", 64)
				trigger.PinnedPolicy = json.RawMessage(strings.Replace(
					string(trigger.PinnedPolicy),
					trigger.PolicyRevision,
					tampered,
					1,
				))
			},
		},
		{
			name: "delivered trigger revision mismatches pin",
			mutate: func(_ *testing.T, trigger *eventing.ReviewAttentionTrigger) {
				trigger.PolicyRevision = "sha256:" + strings.Repeat("e", 64)
			},
		},
		{
			name: "delivered trigger carries zero policy",
			mutate: func(t *testing.T, trigger *eventing.ReviewAttentionTrigger) {
				trigger.PolicyRevision, trigger.PinnedPolicy = attentionBridgeTestPin(
					t,
					[]workflows.GateSpec{{ID: "off", Kind: workflows.GateZero}},
				)
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newAttentionBridgeWaitingFixture(
				t,
				serviceTestCaseID,
				attentionBridgeSubmissionID,
			)
			trigger, err := fixture.store.GetReviewAttentionTrigger(
				context.Background(),
				fixture.detail.Submission.ID,
			)
			if err != nil {
				t.Fatal(err)
			}
			trigger.PinnedPolicy = append(json.RawMessage(nil), trigger.PinnedPolicy...)
			test.mutate(t, &trigger)
			fixture.store.setTrigger(trigger)
			if _, err = fixture.bridge.Project(
				context.Background(),
				fixture.detail.Case.ID,
			); !errors.Is(err, ErrUnavailable) {
				t.Fatalf("Project() error = %v, want unavailable", err)
			}
		})
	}

	t.Run("noop trigger carries active policy", func(t *testing.T) {
		detail := attentionBridgeSubmittedDetail(
			serviceTestCaseID,
			attentionBridgeSubmissionID,
			12,
		)
		store := newAttentionBridgeTestStore(detail)
		revision, pin := attentionBridgeTestPin(t, []workflows.GateSpec{{
			ID: "active", Kind: workflows.GateDeterministic, When: "true",
			Title: "Active gate", Questions: []any{"Proceed?"},
		}})
		now := detail.Case.UpdatedAt.Add(time.Minute)
		store.setTrigger(eventing.ReviewAttentionTrigger{
			SubmissionID: detail.Submission.ID, CaseID: detail.Case.ID,
			CaseVersion:   detail.Case.Version,
			DecisionPoint: eventing.ReviewAttentionDecisionSubmitted,
			Status:        eventing.ReviewAttentionNoop, AvailableAt: now,
			PolicyRevision: revision, PinnedPolicy: pin,
			CreatedAt: now, UpdatedAt: now, CompletedAt: &now,
		})
		service := newAttentionTestService(t, store, nil, nil)
		bridge, err := NewAttentionBridge(AttentionBridgeConfig{
			Service: service, RunStore: workflows.NewFileRunStore(t.TempDir()),
		})
		if err != nil {
			t.Fatal(err)
		}
		if _, err = bridge.Project(context.Background(), detail.Case.ID); !errors.Is(err, ErrUnavailable) {
			t.Fatalf("Project() error = %v, want unavailable", err)
		}
	})
}

type attentionBridgeFixture struct {
	bridge   *AttentionBridge
	service  *Service
	store    *attentionBridgeTestStore
	runStore *workflows.FileRunStore
	executor *workflows.Executor
	result   AttentionLaunchResult
	detail   eventing.ReviewCaseDetail
}

func newAttentionBridgeWaitingFixture(
	t *testing.T,
	caseID string,
	submissionID string,
) *attentionBridgeFixture {
	t.Helper()
	return newAttentionBridgeFixtureWithGates(
		t,
		caseID,
		submissionID,
		[]workflows.GateSpec{
			{
				ID: "scope", Kind: workflows.GateDeterministic, When: "true",
				Title: "Confirm implementation scope", Questions: []any{"Which scope should continue?"},
			},
			{
				ID: "risk", Kind: workflows.GateDeterministic, When: "true",
				Title: "Confirm residual risk", Questions: []any{"Which risk is acceptable?"},
			},
		},
	)
}

func newAttentionBridgeFixtureWithGates(
	t *testing.T,
	caseID string,
	submissionID string,
	gates []workflows.GateSpec,
) *attentionBridgeFixture {
	t.Helper()
	detail := attentionBridgeSubmittedDetail(caseID, submissionID, 12)
	store := newAttentionBridgeTestStore(detail)
	service := newAttentionTestService(t, store, nil, nil)
	workspace := t.TempDir()
	runStore := workflows.NewFileRunStore(workspace)
	executor := &workflows.Executor{WorkspaceDir: workspace, Store: runStore}
	policy := attentionTestPolicy("attention-bridge-policy-v1", gates)
	launcher := newAttentionTestLauncher(
		t,
		service,
		executor,
		&attentionTestPolicySource{snapshots: []AttentionPolicySnapshot{policy}},
	)
	result, err := launcher.Launch(context.Background(), AttentionLaunchRequest{
		CaseID:              caseID,
		ExpectedCaseVersion: detail.Case.Version,
		DecisionPoint:       eventing.ReviewAttentionDecisionSubmitted,
	})
	if err != nil || result.Status != workflows.RunStatusWaiting || result.RunID == "" ||
		!validAttentionResponseToken(result.PolicyRevision) {
		t.Fatalf("Launch() = (%#v, %v), want waiting", result, err)
	}
	policyRevision, pinnedPolicy := attentionBridgeTestPin(t, gates)
	if policyRevision != result.PolicyRevision {
		t.Fatalf("pinned revision %q != launch revision %q", policyRevision, result.PolicyRevision)
	}
	now := detail.Case.UpdatedAt.Add(time.Minute)
	store.setTrigger(eventing.ReviewAttentionTrigger{
		SubmissionID:   submissionID,
		CaseID:         caseID,
		CaseVersion:    detail.Case.Version,
		DecisionPoint:  eventing.ReviewAttentionDecisionSubmitted,
		Status:         eventing.ReviewAttentionDelivered,
		Attempts:       1,
		AvailableAt:    now,
		PolicyRevision: result.PolicyRevision,
		PinnedPolicy:   pinnedPolicy,
		RunID:          result.RunID,
		CreatedAt:      now,
		UpdatedAt:      now,
		CompletedAt:    &now,
	})
	bridge, err := NewAttentionBridge(AttentionBridgeConfig{
		Service: service, Executor: executor, RunStore: runStore,
	})
	if err != nil {
		t.Fatalf("NewAttentionBridge() error = %v", err)
	}
	return &attentionBridgeFixture{
		bridge: bridge, service: service, store: store, runStore: runStore,
		executor: executor, result: result, detail: detail,
	}
}

func attentionBridgeTestPin(
	t *testing.T,
	gates []workflows.GateSpec,
) (string, json.RawMessage) {
	t.Helper()
	resolved, err := resolveAttentionPolicy(attentionTestPolicy("attention-bridge-policy-v1", gates))
	if err != nil {
		t.Fatalf("resolveAttentionPolicy() error = %v", err)
	}
	prepared, err := encodePreparedAttentionPolicy(resolved)
	if err != nil {
		t.Fatalf("encodePreparedAttentionPolicy() error = %v", err)
	}
	return resolved.decisionRevision, append(json.RawMessage(nil), prepared.canonical...)
}

func attentionBridgeSubmittedDetail(
	caseID string,
	submissionID string,
	version int64,
) eventing.ReviewCaseDetail {
	detail := submittedAttentionTestDetail(caseID, version)
	submittedAt := detail.Case.UpdatedAt
	createdAt := submittedAt.Add(-time.Minute)
	detail.Submission = &eventing.ReviewSubmission{
		ID:               submissionID,
		CaseID:           caseID,
		DraftVersion:     version - 2,
		Status:           eventing.ReviewSubmissionSubmitted,
		Attempts:         1,
		ExternalReviewID: "external-review-1",
		ExternalURL:      "https://github.com/scylladb/gocql/pull/42#pullrequestreview-1",
		CreatedAt:        createdAt,
		UpdatedAt:        submittedAt,
		SubmittedAt:      &submittedAt,
	}
	return detail
}

func assertAttentionProjectionHidesPrivateState(
	t *testing.T,
	fixture *attentionBridgeFixture,
	view AttentionView,
) {
	t.Helper()
	tasks, err := fixture.runStore.ListHumanTasks(context.Background(), fixture.result.RunID)
	if err != nil || len(tasks) == 0 {
		t.Fatalf("ListHumanTasks() = (%#v, %v)", tasks, err)
	}
	encoded, err := json.Marshal(view)
	if err != nil {
		t.Fatal(err)
	}
	for _, private := range []string{
		fixture.result.RunID,
		fixture.result.PolicyRevision,
		fixture.detail.Submission.ID,
		tasks[0].ID,
		tasks[0].InputHash,
		`"run_id"`,
		`"task_id"`,
		`"input_hash"`,
		`"policy_revision"`,
		`"workflow_ref"`,
		`"session"`,
	} {
		if strings.Contains(string(encoded), private) {
			t.Fatalf("projection exposed private value %q: %s", private, encoded)
		}
	}
}

type attentionBridgeTestStore struct {
	*attentionTestStore
	triggerMu sync.RWMutex
	triggers  map[string]eventing.ReviewAttentionTrigger
}

type attentionBridgeRecoveryRunStore struct {
	*workflows.FileRunStore
}

type attentionBridgeTamperedTaskStore struct {
	*workflows.FileRunStore
	mutate func(*workflows.WorkflowHumanTask)
}

func (store *attentionBridgeTamperedTaskStore) ListHumanTasks(
	ctx context.Context,
	runID string,
) ([]workflows.WorkflowHumanTask, error) {
	tasks, err := store.FileRunStore.ListHumanTasks(ctx, runID)
	if err != nil {
		return nil, err
	}
	if len(tasks) != 0 && store.mutate != nil {
		store.mutate(&tasks[0])
	}
	return tasks, nil
}

func (store *attentionBridgeRecoveryRunStore) ListHumanTasks(
	ctx context.Context,
	runID string,
) ([]workflows.WorkflowHumanTask, error) {
	tasks, err := store.FileRunStore.ListHumanTasks(ctx, runID)
	if err != nil {
		return nil, err
	}
	for index := range tasks {
		if tasks[index].Status == workflows.HumanTaskStatusContinuing {
			tasks[index].Status = workflows.HumanTaskStatusRecoveryRequired
		}
	}
	return tasks, nil
}

func assertAttentionAuthorityInvariant(t *testing.T, view AttentionView) {
	t.Helper()
	tokens := 0
	for _, turn := range view.Turns {
		if turn.ResponseToken != "" {
			tokens++
			if turn.Status != workflows.HumanTaskStatusWaiting &&
				turn.Status != workflows.HumanTaskStatusRecoveryRequired {
				t.Fatalf("token on non-actionable turn: %#v", view)
			}
		}
	}
	if view.CanRespond && tokens != 1 || !view.CanRespond && tokens != 0 {
		t.Fatalf("authority invariant failed: %#v", view)
	}
}

func newAttentionBridgeTestStore(
	details ...eventing.ReviewCaseDetail,
) *attentionBridgeTestStore {
	return &attentionBridgeTestStore{
		attentionTestStore: newAttentionTestStore(details...),
		triggers:           make(map[string]eventing.ReviewAttentionTrigger),
	}
}

func (store *attentionBridgeTestStore) GetReviewAttentionTrigger(
	ctx context.Context,
	submissionID string,
) (eventing.ReviewAttentionTrigger, error) {
	if err := ctx.Err(); err != nil {
		return eventing.ReviewAttentionTrigger{}, err
	}
	store.triggerMu.RLock()
	defer store.triggerMu.RUnlock()
	trigger, ok := store.triggers[submissionID]
	if !ok {
		return eventing.ReviewAttentionTrigger{}, eventing.ErrNotFound
	}
	return trigger, nil
}

func (store *attentionBridgeTestStore) setTrigger(
	trigger eventing.ReviewAttentionTrigger,
) {
	store.triggerMu.Lock()
	defer store.triggerMu.Unlock()
	store.triggers[trigger.SubmissionID] = trigger
}
