package prdevelopment

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/sipeed/picoclaw/pkg/eventing"
)

const testDevelopmentCaseID = "pdc_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

type fakeReader struct {
	page              eventing.PRDevelopmentCasePage
	detail            eventing.PRDevelopmentCase
	conversation      eventing.PRDevelopmentConversation
	listFilter        eventing.PRDevelopmentCaseFilter
	listErr           error
	getErr            error
	listCalls         int
	getCalls          int
	conversationCalls int
	appendCalls       []eventing.PRDevelopmentMessageAppend
}

type fakeRepairStore struct {
	*fakeReader
	workbench      eventing.PRDevelopmentWorkbench
	admit          eventing.PRDevelopmentRepairAdmit
	admitErr       error
	workbenchCalls int
	admitCalls     int
}

func (store *fakeRepairStore) GetPRDevelopmentWorkbench(
	_ context.Context,
	caseID string,
) (eventing.PRDevelopmentWorkbench, error) {
	store.workbenchCalls++
	if caseID != testDevelopmentCaseID {
		return eventing.PRDevelopmentWorkbench{}, eventing.ErrNotFound
	}
	return store.workbench, nil
}

func (store *fakeRepairStore) AdmitPRDevelopmentRepair(
	_ context.Context,
	input eventing.PRDevelopmentRepairAdmit,
) (eventing.PRDevelopmentWorkbench, bool, error) {
	store.admitCalls++
	store.admit = input
	return store.workbench, store.admitErr == nil, store.admitErr
}

func (reader *fakeReader) ListPRDevelopmentCases(
	_ context.Context,
	filter eventing.PRDevelopmentCaseFilter,
) (eventing.PRDevelopmentCasePage, error) {
	reader.listCalls++
	reader.listFilter = filter
	return reader.page, reader.listErr
}

func (reader *fakeReader) GetPRDevelopmentCase(
	_ context.Context,
	caseID string,
) (eventing.PRDevelopmentCase, error) {
	reader.getCalls++
	if caseID != testDevelopmentCaseID {
		return eventing.PRDevelopmentCase{}, eventing.ErrNotFound
	}
	return reader.detail, reader.getErr
}

func (reader *fakeReader) GetPRDevelopmentConversation(
	_ context.Context,
	caseID string,
) (eventing.PRDevelopmentConversation, error) {
	reader.conversationCalls++
	if caseID != testDevelopmentCaseID {
		return eventing.PRDevelopmentConversation{}, eventing.ErrNotFound
	}
	conversation := reader.conversation
	if conversation.CaseID == "" {
		conversation.CaseID = caseID
	}
	return conversation, nil
}

func (reader *fakeReader) AppendPRDevelopmentMessage(
	_ context.Context,
	input eventing.PRDevelopmentMessageAppend,
) (eventing.PRDevelopmentConversation, error) {
	reader.appendCalls = append(reader.appendCalls, input)
	if input.CaseID != testDevelopmentCaseID ||
		input.ExpectedVersion != reader.conversation.Version {
		return eventing.PRDevelopmentConversation{},
			eventing.ErrPRDevelopmentConversationConflict
	}
	message := eventing.PRDevelopmentMessage{
		ID:        fmt.Sprintf("pdm_%032x", len(reader.conversation.Messages)+1),
		CaseID:    input.CaseID,
		Ordinal:   len(reader.conversation.Messages),
		Role:      input.Role,
		Content:   strings.TrimSpace(input.Content),
		CreatedAt: time.Date(2026, 8, 5, 13, len(reader.conversation.Messages), 0, 0, time.UTC),
	}
	reader.conversation.CaseID = input.CaseID
	reader.conversation.Messages = append(reader.conversation.Messages, message)
	reader.conversation.Version++
	return reader.conversation, nil
}

func TestServiceProjectsOnlyBrowserSafeCapturedSnapshot(t *testing.T) {
	captured := testCapturedDevelopmentCase()
	next := eventing.PRDevelopmentCaseCursor{
		UpdatedAt: captured.UpdatedAt,
		ID:        captured.ID,
	}
	reader := &fakeReader{
		page: eventing.PRDevelopmentCasePage{
			Cases: []eventing.PRDevelopmentCase{captured},
			Next:  &next,
		},
		detail: captured,
	}
	service, err := NewService(ServiceConfig{Store: reader})
	if err != nil {
		t.Fatalf("NewService() error = %v", err)
	}
	page, err := service.List(context.Background(), ListRequest{
		Repository: "octo/repo",
		PullNumber: 17,
		Limit:      1,
	})
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if len(page.Cases) != 1 || page.NextCursor == "" {
		t.Fatalf("List() = %#v", page)
	}
	if reader.listFilter.Repository != "octo/repo" ||
		reader.listFilter.PullNumber != 17 ||
		reader.listFilter.Limit != 1 ||
		reader.listFilter.After != nil {
		t.Fatalf("store filter = %#v", reader.listFilter)
	}
	if page.Cases[0].CapturedAt != captured.CreatedAt ||
		page.Cases[0].HeadSHA != captured.HeadSHA ||
		page.Cases[0].ReviewURL != captured.ReviewURL {
		t.Fatalf("summary = %#v", page.Cases[0])
	}

	_, err = service.List(context.Background(), ListRequest{
		Repository: "octo/repo",
		PullNumber: 17,
		Limit:      1,
		Cursor:     page.NextCursor,
	})
	if err != nil {
		t.Fatalf("List(cursor) error = %v", err)
	}
	if reader.listFilter.After == nil || *reader.listFilter.After != next {
		t.Fatalf("decoded cursor = %#v, want %#v", reader.listFilter.After, next)
	}

	detail, err := service.Get(context.Background(), testDevelopmentCaseID)
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if detail.Case.Feedback != captured.Feedback ||
		detail.Case.BaseSHA != captured.BaseSHA ||
		detail.Case.ReviewCommitSHA != captured.ReviewCommitSHA {
		t.Fatalf("detail = %#v", detail)
	}
	raw, err := json.Marshal(detail)
	if err != nil {
		t.Fatalf("Marshal(detail) error = %v", err)
	}
	for _, secret := range []string{
		captured.EventID,
		captured.DispatchID,
		captured.RunID,
		captured.WorkflowRef,
		captured.WorkflowRevision,
		captured.Connector,
		captured.TargetUser,
		captured.ReviewID,
		captured.TriggerReviewNodeID,
	} {
		if strings.Contains(string(raw), secret) {
			t.Fatalf("safe projection leaked %q in %s", secret, raw)
		}
	}
}

func TestRepairAdmissionProjectsOnlyBrowserSafeLifecycle(t *testing.T) {
	now := time.Date(2026, 8, 8, 13, 0, 0, 0, time.UTC)
	captured := testCapturedDevelopmentCase()
	attempt := eventing.PRDevelopmentRepairAttempt{
		ID:                  "pdr_bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
		SessionID:           "pds_cccccccccccccccccccccccccccccccc",
		Ordinal:             0,
		ConversationVersion: 0,
		IdempotencyKey:      "prq_dddddddddddddddddddddddddddddddd",
		Instruction:         "Address the review feedback.",
		Status:              eventing.PRDevelopmentRepairQueued,
		CreatedAt:           now,
		UpdatedAt:           now,
	}
	session := eventing.PRDevelopmentRepairSession{
		ID:             attempt.SessionID,
		CaseID:         captured.ID,
		Version:        1,
		AgentID:        "main",
		ReservationKey: "private-reservation-capability",
		Attempts:       []eventing.PRDevelopmentRepairAttempt{attempt},
		CreatedAt:      now,
		UpdatedAt:      now,
	}
	store := &fakeRepairStore{
		fakeReader: &fakeReader{detail: captured},
		workbench: eventing.PRDevelopmentWorkbench{
			Case: captured,
			Conversation: eventing.PRDevelopmentConversation{
				CaseID: captured.ID, Messages: []eventing.PRDevelopmentMessage{},
			},
			RepairSession: &session,
		},
	}
	service, err := NewService(ServiceConfig{
		Store:         store,
		RepairStore:   store,
		RepairEnabled: true,
		RepairAgentReady: func(string) bool {
			return true
		},
		AgentID: "main",
	})
	if err != nil {
		t.Fatalf("NewService() error = %v", err)
	}
	detail, err := service.Repair(context.Background(), RepairRequest{
		CaseID: testDevelopmentCaseID, ExpectedConversationVersion: 0,
		ExpectedRepairRevision: 0,
		RequestID:              attempt.IdempotencyKey,
		Instruction:            "  Address the review feedback.  ",
	})
	if err != nil {
		t.Fatalf("Repair() error = %v", err)
	}
	if !detail.RepairAvailable || detail.RepairRevision != 1 ||
		detail.RepairSession == nil || detail.RepairSession.ID != session.ID ||
		len(detail.RepairSession.Attempts) != 1 {
		t.Fatalf("detail = %#v", detail)
	}
	if store.admit.Instruction != attempt.Instruction ||
		store.admit.IdempotencyKey != attempt.IdempotencyKey ||
		store.admit.AgentID != "main" {
		t.Fatalf("admit = %#v", store.admit)
	}
	raw, err := json.Marshal(detail)
	if err != nil {
		t.Fatalf("Marshal(detail) error = %v", err)
	}
	for _, private := range []string{
		attempt.IdempotencyKey,
		session.ReservationKey,
		"lease-token-private",
		"clone-private",
		"review-digest-private",
		"provider-private",
		"model-private",
	} {
		if strings.Contains(string(raw), private) {
			t.Fatalf("safe repair projection leaked %q in %s", private, raw)
		}
	}

	handler := &Handler{Service: service}
	body := `{"expected_conversation_version":0,"expected_repair_revision":0,"request_id":"` +
		attempt.IdempotencyKey + `","instruction":"Address the review feedback."}`
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(
		http.MethodPost,
		RuntimeRoutePrefix+"/"+testDevelopmentCaseID+"/repair",
		strings.NewReader(body),
	)
	request.Header.Set("Content-Type", "application/json")
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusAccepted {
		t.Fatalf("repair status = %d, body=%s", recorder.Code, recorder.Body.String())
	}
	if !strings.Contains(recorder.Body.String(), `"repair_revision":1`) ||
		strings.Contains(recorder.Body.String(), session.ReservationKey) {
		t.Fatalf("repair body = %s", recorder.Body.String())
	}
}

func TestRepairKeepsSessionAgentWhenConfiguredDefaultChanges(t *testing.T) {
	now := time.Date(2026, 8, 8, 13, 0, 0, 0, time.UTC)
	captured := testCapturedDevelopmentCase()
	attempt := eventing.PRDevelopmentRepairAttempt{
		ID:                  "pdr_bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
		SessionID:           "pds_cccccccccccccccccccccccccccccccc",
		Ordinal:             0,
		ConversationVersion: 0,
		Instruction:         "Address the review feedback.",
		Status:              eventing.PRDevelopmentRepairQueued,
		CreatedAt:           now,
		UpdatedAt:           now,
	}
	session := eventing.PRDevelopmentRepairSession{
		ID:             attempt.SessionID,
		CaseID:         captured.ID,
		Version:        1,
		AgentID:        "repairer",
		ReservationKey: "private-reservation-capability",
		Attempts:       []eventing.PRDevelopmentRepairAttempt{attempt},
		CreatedAt:      now,
		UpdatedAt:      now,
	}
	store := &fakeRepairStore{
		fakeReader: &fakeReader{detail: captured},
		workbench: eventing.PRDevelopmentWorkbench{
			Case: captured,
			Conversation: eventing.PRDevelopmentConversation{
				CaseID: captured.ID, Messages: []eventing.PRDevelopmentMessage{},
			},
			RepairSession: &session,
		},
	}
	ready := true
	var readinessAgents []string
	service, err := NewService(ServiceConfig{
		Store:         store,
		RepairStore:   store,
		RepairEnabled: true,
		RepairAgentReady: func(agentID string) bool {
			readinessAgents = append(readinessAgents, agentID)
			return ready && agentID == "repairer"
		},
		AgentID: "replacement",
	})
	if err != nil {
		t.Fatalf("NewService() error = %v", err)
	}

	detail, err := service.Get(context.Background(), captured.ID)
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if !detail.RepairAvailable || detail.RepairSession == nil ||
		detail.RepairSession.AgentID != "repairer" {
		t.Fatalf("detail after default-agent change = %#v", detail)
	}
	if len(readinessAgents) != 1 || readinessAgents[0] != "repairer" {
		t.Fatalf("readiness agents = %#v, want pinned repairer", readinessAgents)
	}

	_, err = service.Repair(context.Background(), RepairRequest{
		CaseID:                      captured.ID,
		ExpectedConversationVersion: 0,
		ExpectedRepairRevision:      1,
		RequestID:                   "prq_dddddddddddddddddddddddddddddddd",
		Instruction:                 "Continue with the pinned repair agent.",
	})
	if err != nil {
		t.Fatalf("Repair() error = %v", err)
	}
	if store.admitCalls != 1 || store.admit.AgentID != "repairer" {
		t.Fatalf("repair admission = %#v / %d calls", store.admit, store.admitCalls)
	}

	ready = false
	detail, err = service.Get(context.Background(), captured.ID)
	if err != nil {
		t.Fatalf("Get(unavailable pinned agent) error = %v", err)
	}
	if detail.RepairAvailable || detail.RepairUnavailableReason != "runtime_unavailable" {
		t.Fatalf("unavailable pinned-agent detail = %#v", detail)
	}
	_, err = service.Repair(context.Background(), RepairRequest{
		CaseID:                      captured.ID,
		ExpectedConversationVersion: 0,
		ExpectedRepairRevision:      1,
		RequestID:                   "prq_eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee",
		Instruction:                 "This must not be admitted.",
	})
	if !errors.Is(err, ErrUnavailable) {
		t.Fatalf("Repair(unavailable pinned agent) error = %v", err)
	}
	if store.admitCalls != 1 {
		t.Fatalf("repair admissions after unavailable agent = %d, want 1", store.admitCalls)
	}
}

func TestProjectRepairSessionRejectsPartialPrivatePinAndNonmonotonicHistory(t *testing.T) {
	now := time.Date(2026, 8, 8, 13, 0, 0, 0, time.UTC)
	attempt := eventing.PRDevelopmentRepairAttempt{
		ID:                  "pdr_bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
		SessionID:           "pds_cccccccccccccccccccccccccccccccc",
		Ordinal:             0,
		ConversationVersion: 0,
		Instruction:         "Address the review feedback.",
		Status:              eventing.PRDevelopmentRepairQueued,
		CreatedAt:           now,
		UpdatedAt:           now,
	}
	session := eventing.PRDevelopmentRepairSession{
		ID:             attempt.SessionID,
		CaseID:         testDevelopmentCaseID,
		Version:        1,
		AgentID:        "main",
		ReservationKey: "private-reservation-capability",
		Attempts:       []eventing.PRDevelopmentRepairAttempt{attempt},
		CreatedAt:      now,
		UpdatedAt:      now,
	}

	partial := session
	partial.CloneURL = "https://github.com/octo/fork.git"
	if _, err := projectRepairSession(testDevelopmentCaseID, 0, partial); err == nil {
		t.Fatal("projectRepairSession(partial private pin) error = nil")
	}

	for _, status := range []eventing.PRDevelopmentRepairStatus{
		eventing.PRDevelopmentRepairRunning,
		eventing.PRDevelopmentRepairRecoveryRequired,
	} {
		executable := session
		executable.Attempts = append([]eventing.PRDevelopmentRepairAttempt(nil), session.Attempts...)
		executable.Attempts[0].Status = status
		if status == eventing.PRDevelopmentRepairRecoveryRequired {
			executable.Attempts[0].Summary = "Execution ownership became ambiguous."
			executable.Attempts[0].ErrorCode = eventing.PRDevelopmentRepairErrorRecoveryRequired
		}
		if _, err := projectRepairSession(testDevelopmentCaseID, 0, executable); err == nil {
			t.Fatalf("projectRepairSession(unpinned %s) error = nil", status)
		}
	}

	completedWithoutWorkspace := session
	completedWithoutWorkspace.HeadRepository = "octo/fork"
	completedWithoutWorkspace.HeadRef = "feature"
	completedWithoutWorkspace.HeadSHA = strings.Repeat("a", 40)
	completedWithoutWorkspace.CloneURL = "https://github.com/octo/fork.git"
	completedWithoutWorkspace.ReviewDigest = "sha256:" + strings.Repeat("b", 64)
	completedWithoutWorkspace.Attempts = append(
		[]eventing.PRDevelopmentRepairAttempt(nil),
		session.Attempts...,
	)
	completedWithoutWorkspace.Attempts[0].Status = eventing.PRDevelopmentRepairCompleted
	completedWithoutWorkspace.Attempts[0].Summary = "Applied the requested repair."
	completedWithoutWorkspace.Attempts[0].Iterations = 1
	if _, err := projectRepairSession(
		testDevelopmentCaseID,
		0,
		completedWithoutWorkspace,
	); err == nil {
		t.Fatal("projectRepairSession(completed without workspace) error = nil")
	}

	second := attempt
	second.ID = "pdr_dddddddddddddddddddddddddddddddd"
	second.Ordinal = 1
	second.Status = eventing.PRDevelopmentRepairFailed
	second.ErrorCode = eventing.PRDevelopmentRepairErrorInternal
	second.Summary = "Stopped safely."
	second.CreatedAt = now.Add(-time.Second)
	second.UpdatedAt = now
	first := attempt
	first.Status = eventing.PRDevelopmentRepairFailed
	first.ErrorCode = eventing.PRDevelopmentRepairErrorInternal
	first.Summary = "Stopped safely."
	nonmonotonic := session
	nonmonotonic.Version = 2
	nonmonotonic.Attempts = []eventing.PRDevelopmentRepairAttempt{first, second}
	if _, err := projectRepairSession(testDevelopmentCaseID, 0, nonmonotonic); err == nil {
		t.Fatal("projectRepairSession(nonmonotonic history) error = nil")
	}

	duplicate := nonmonotonic
	duplicate.Attempts = append(
		[]eventing.PRDevelopmentRepairAttempt(nil),
		nonmonotonic.Attempts...,
	)
	duplicate.Attempts[1].ID = duplicate.Attempts[0].ID
	duplicate.Attempts[1].CreatedAt = duplicate.Attempts[0].CreatedAt
	if _, err := projectRepairSession(testDevelopmentCaseID, 0, duplicate); err == nil {
		t.Fatal("projectRepairSession(duplicate attempt ID) error = nil")
	}

	decreasingConversation := duplicate
	decreasingConversation.Attempts = append(
		[]eventing.PRDevelopmentRepairAttempt(nil),
		duplicate.Attempts...,
	)
	decreasingConversation.Attempts[1].ID = "pdr_eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee"
	decreasingConversation.Attempts[0].ConversationVersion = 2
	decreasingConversation.Attempts[1].ConversationVersion = 1
	if _, err := projectRepairSession(
		testDevelopmentCaseID,
		2,
		decreasingConversation,
	); err == nil {
		t.Fatal("projectRepairSession(decreasing conversation history) error = nil")
	}
}

func TestRepairHandlerRejectsUnsafeRequestsBeforeAdmission(t *testing.T) {
	captured := testCapturedDevelopmentCase()
	store := &fakeRepairStore{
		fakeReader: &fakeReader{detail: captured},
		workbench: eventing.PRDevelopmentWorkbench{
			Case: captured,
			Conversation: eventing.PRDevelopmentConversation{
				CaseID: captured.ID, Messages: []eventing.PRDevelopmentMessage{},
			},
		},
	}
	service, err := NewService(ServiceConfig{
		Store:         store,
		RepairStore:   store,
		RepairEnabled: true,
		RepairAgentReady: func(string) bool {
			return true
		},
		AgentID: "main",
	})
	if err != nil {
		t.Fatalf("NewService() error = %v", err)
	}
	handler := &Handler{Service: service}
	path := RuntimeRoutePrefix + "/" + testDevelopmentCaseID + "/repair"
	valid := `{"expected_conversation_version":0,"expected_repair_revision":0,"request_id":"prq_dddddddddddddddddddddddddddddddd","instruction":"Fix it."}`
	tests := []struct {
		name       string
		target     string
		body       string
		header     string
		wantStatus int
	}{
		{name: "query", target: path + "?x=1", body: valid, wantStatus: http.StatusBadRequest},
		{
			name: "browser origin", target: path, body: valid,
			header: "Origin", wantStatus: http.StatusForbidden,
		},
		{
			name: "browser referer", target: path, body: valid,
			header: "Referer", wantStatus: http.StatusForbidden,
		},
		{
			name: "unknown", target: path,
			body:       strings.TrimSuffix(valid, "}") + `,"extra":true}`,
			wantStatus: http.StatusBadRequest,
		},
		{
			name: "duplicate", target: path,
			body: strings.Replace(
				valid, `"instruction":`, `"instruction":"first","instruction":`, 1,
			),
			wantStatus: http.StatusBadRequest,
		},
		{
			name: "case alias",
			target: RuntimeRoutePrefix +
				"/%70dc_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa/repair",
			body: valid, wantStatus: http.StatusBadRequest,
		},
		{
			name: "bad request id", target: path,
			body: strings.Replace(
				valid,
				"prq_dddddddddddddddddddddddddddddddd",
				"prq_DDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDD",
				1,
			),
			wantStatus: http.StatusBadRequest,
		},
		{
			name: "blank instruction", target: path,
			body:       strings.Replace(valid, "Fix it.", " ", 1),
			wantStatus: http.StatusBadRequest,
		},
		{
			name: "oversized instruction", target: path,
			body: strings.Replace(
				valid,
				"Fix it.",
				strings.Repeat("a", MaximumRepairInstructionBytes+1),
				1,
			),
			wantStatus: http.StatusBadRequest,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodPost, test.target, strings.NewReader(test.body))
			request.Header.Set("Content-Type", "application/json")
			if test.header != "" {
				request.Header.Set(test.header, "https://launcher.example")
			}
			recorder := httptest.NewRecorder()
			handler.ServeHTTP(recorder, request)
			if recorder.Code != test.wantStatus {
				t.Fatalf("status = %d, want %d, body=%s", recorder.Code, test.wantStatus, recorder.Body.String())
			}
		})
	}
	if store.admit.CaseID != "" {
		t.Fatalf("unsafe request reached admission: %#v", store.admit)
	}
}

func TestCaseCursorBindsFiltersAndRejectsNoncanonicalValues(t *testing.T) {
	cursor := eventing.PRDevelopmentCaseCursor{
		UpdatedAt: time.Date(2026, 8, 5, 12, 30, 0, 123, time.UTC),
		ID:        testDevelopmentCaseID,
	}
	filter := cursorFilter{Repository: "octo/repo", PullNumber: 17}
	encoded, err := encodeCaseCursor(cursor, filter)
	if err != nil {
		t.Fatalf("encodeCaseCursor() error = %v", err)
	}
	decoded, err := decodeCaseCursor(encoded, filter)
	if err != nil || decoded == nil || *decoded != cursor {
		t.Fatalf("decodeCaseCursor() = %#v, %v", decoded, err)
	}
	for name, candidate := range map[string]string{
		"padding":      encoded + "=",
		"trailing":     encoded + "a",
		"not base64":   "!",
		"empty object": "e30",
	} {
		t.Run(name, func(t *testing.T) {
			if _, decodeErr := decodeCaseCursor(candidate, filter); decodeErr == nil {
				t.Fatalf("decodeCaseCursor(%q) succeeded", candidate)
			}
		})
	}
	if _, err = decodeCaseCursor(
		encoded,
		cursorFilter{Repository: "other/repo", PullNumber: 17},
	); err == nil {
		t.Fatal("cursor was accepted under a different repository filter")
	}
	if _, err = decodeCaseCursor(
		encoded,
		cursorFilter{Repository: "octo/repo", PullNumber: 18},
	); err == nil {
		t.Fatal("cursor was accepted under a different pull-number filter")
	}
}

func TestCaseCursorAcceptsValidUnixEpochPosition(t *testing.T) {
	cursor := eventing.PRDevelopmentCaseCursor{
		UpdatedAt: time.Unix(0, 0).UTC(),
		ID:        testDevelopmentCaseID,
	}
	encoded, err := encodeCaseCursor(cursor, cursorFilter{})
	if err != nil {
		t.Fatalf("encodeCaseCursor(epoch) error = %v", err)
	}
	decoded, err := decodeCaseCursor(encoded, cursorFilter{})
	if err != nil || decoded == nil || *decoded != cursor {
		t.Fatalf("decodeCaseCursor(epoch) = %#v, %v", decoded, err)
	}
}

func TestServiceRejectsOutOfDomainFiltersBeforeReader(t *testing.T) {
	reader := &fakeReader{}
	service, err := NewService(ServiceConfig{Store: reader})
	if err != nil {
		t.Fatalf("NewService() error = %v", err)
	}
	oversizedRepository := strings.Repeat("a", 127) + "/" +
		strings.Repeat("b", MaximumRepositoryBytes-127)
	for name, request := range map[string]ListRequest{
		"repository": {
			Repository: oversizedRepository,
		},
		"pull number": {
			PullNumber: MaximumPullNumber + 1,
		},
	} {
		t.Run(name, func(t *testing.T) {
			if _, listErr := service.List(context.Background(), request); listErr == nil {
				t.Fatalf("List(%#v) succeeded", request)
			}
		})
	}
	if reader.listCalls != 0 {
		t.Fatalf("invalid filters reached reader %d time(s)", reader.listCalls)
	}
}

func TestHandlerServesExactReadOnlyRoutesAndPlainFeedback(t *testing.T) {
	captured := testCapturedDevelopmentCase()
	reader := &fakeReader{
		page: eventing.PRDevelopmentCasePage{
			Cases: []eventing.PRDevelopmentCase{captured},
		},
		detail: captured,
	}
	service, err := NewService(ServiceConfig{Store: reader})
	if err != nil {
		t.Fatalf("NewService() error = %v", err)
	}
	handler := &Handler{Service: service}

	list := httptest.NewRecorder()
	handler.ServeHTTP(list, httptest.NewRequest(
		http.MethodGet,
		RuntimeRoutePrefix+"?repository=octo%2Frepo&pull_number=17&limit=25",
		nil,
	))
	if list.Code != http.StatusOK {
		t.Fatalf("list status = %d, body=%s", list.Code, list.Body.String())
	}
	assertDevelopmentHeaders(t, list)
	if reader.listFilter.Repository != "octo/repo" ||
		reader.listFilter.PullNumber != 17 ||
		reader.listFilter.Limit != 25 {
		t.Fatalf("list filter = %#v", reader.listFilter)
	}

	detail := httptest.NewRecorder()
	handler.ServeHTTP(detail, httptest.NewRequest(
		http.MethodGet,
		RuntimeRoutePrefix+"/"+testDevelopmentCaseID,
		nil,
	))
	if detail.Code != http.StatusOK {
		t.Fatalf("detail status = %d, body=%s", detail.Code, detail.Body.String())
	}
	assertDevelopmentHeaders(t, detail)
	if !strings.Contains(detail.Body.String(), `\u003cscript\u003e`) ||
		strings.Contains(detail.Body.String(), captured.DispatchID) {
		t.Fatalf("detail body = %s", detail.Body.String())
	}
}

func TestHandlerRejectsMalformedOrMutableRequestsWithoutStoreCalls(t *testing.T) {
	reader := &fakeReader{detail: testCapturedDevelopmentCase()}
	service, err := NewService(ServiceConfig{Store: reader})
	if err != nil {
		t.Fatalf("NewService() error = %v", err)
	}
	handler := &Handler{Service: service}
	oversizedRepositoryQuery := strings.Repeat("a", 127) + "%2F" +
		strings.Repeat("b", MaximumRepositoryBytes-127)
	bad := []struct {
		method string
		path   string
		status int
	}{
		{http.MethodPost, RuntimeRoutePrefix, http.StatusMethodNotAllowed},
		{http.MethodPatch, RuntimeRoutePrefix + "/" + testDevelopmentCaseID, http.StatusMethodNotAllowed},
		{http.MethodGet, RuntimeRoutePrefix + "/", http.StatusBadRequest},
		{http.MethodGet, RuntimeRoutePrefix + "/pdc_AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA", http.StatusBadRequest},
		{http.MethodGet, RuntimeRoutePrefix + "/%70dc_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", http.StatusBadRequest},
		{http.MethodGet, RuntimeRoutePrefix + "/" + testDevelopmentCaseID + "/chat", http.StatusMethodNotAllowed},
		{http.MethodGet, RuntimeRoutePrefix + "/" + testDevelopmentCaseID + "/repair", http.StatusMethodNotAllowed},
		{http.MethodGet, RuntimeRoutePrefix + "/" + testDevelopmentCaseID + "?private=1", http.StatusBadRequest},
		{http.MethodGet, RuntimeRoutePrefix + "?unknown=x", http.StatusBadRequest},
		{http.MethodGet, RuntimeRoutePrefix + "?repository=octo", http.StatusBadRequest},
		{http.MethodGet, RuntimeRoutePrefix + "?repository=" + oversizedRepositoryQuery, http.StatusBadRequest},
		{http.MethodGet, RuntimeRoutePrefix + "?pull_number=01", http.StatusBadRequest},
		{http.MethodGet, RuntimeRoutePrefix + "?pull_number=-1", http.StatusBadRequest},
		{http.MethodGet, RuntimeRoutePrefix + "?pull_number=2147483648", http.StatusBadRequest},
		{http.MethodGet, RuntimeRoutePrefix + "?limit=101", http.StatusBadRequest},
		{http.MethodGet, RuntimeRoutePrefix + "?cursor=", http.StatusBadRequest},
		{http.MethodGet, RuntimeRoutePrefix, http.StatusBadRequest},
	}
	for index, test := range bad {
		recorder := httptest.NewRecorder()
		var body *strings.Reader
		if index == len(bad)-1 {
			body = strings.NewReader("not allowed")
		} else {
			body = strings.NewReader("")
		}
		handler.ServeHTTP(
			recorder,
			httptest.NewRequest(test.method, test.path, body),
		)
		if recorder.Code != test.status {
			t.Fatalf(
				"%s %s status = %d, want %d, body=%s",
				test.method,
				test.path,
				recorder.Code,
				test.status,
				recorder.Body.String(),
			)
		}
		assertDevelopmentHeaders(t, recorder)
	}
	if reader.listCalls != 0 || reader.getCalls != 0 {
		t.Fatalf("invalid requests reached store: list=%d get=%d", reader.listCalls, reader.getCalls)
	}
}

func TestHandlerMapsSafeErrors(t *testing.T) {
	tests := []struct {
		name   string
		err    error
		status int
		body   string
	}{
		{"not found", eventing.ErrNotFound, http.StatusNotFound, "development case not found"},
		{"invalid", eventing.ErrInvalidPRDevelopment, http.StatusBadRequest, "invalid development workbench request"},
		{"timeout", context.DeadlineExceeded, http.StatusGatewayTimeout, "development workbench timed out"},
		{
			"internal",
			errors.New("database secret"),
			http.StatusInternalServerError,
			"development workbench operation failed",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			reader := &fakeReader{detail: testCapturedDevelopmentCase(), getErr: test.err}
			service, err := NewService(ServiceConfig{Store: reader})
			if err != nil {
				t.Fatalf("NewService() error = %v", err)
			}
			recorder := httptest.NewRecorder()
			(&Handler{Service: service}).ServeHTTP(
				recorder,
				httptest.NewRequest(http.MethodGet, RuntimeRoutePrefix+"/"+testDevelopmentCaseID, nil),
			)
			if recorder.Code != test.status || !strings.Contains(recorder.Body.String(), test.body) {
				t.Fatalf("response = %d %s", recorder.Code, recorder.Body.String())
			}
			if strings.Contains(recorder.Body.String(), "database secret") {
				t.Fatalf("internal error leaked: %s", recorder.Body.String())
			}
		})
	}
}

func TestHandlerRejectsNonIdentityOrAmbiguousReadEncoding(t *testing.T) {
	reader := &fakeReader{detail: testCapturedDevelopmentCase()}
	service, err := NewService(ServiceConfig{Store: reader})
	if err != nil {
		t.Fatalf("NewService() error = %v", err)
	}
	for name, values := range map[string][]string{
		"compressed": {"br"},
		"ambiguous":  {"identity", "identity"},
	} {
		t.Run(name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodGet, RuntimeRoutePrefix, nil)
			request.Header["Content-Encoding"] = values
			recorder := httptest.NewRecorder()
			(&Handler{Service: service}).ServeHTTP(recorder, request)
			if recorder.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, body=%s", recorder.Code, recorder.Body.String())
			}
		})
	}
	if reader.listCalls != 0 || reader.getCalls != 0 {
		t.Fatalf("encoded reads reached store: list=%d get=%d", reader.listCalls, reader.getCalls)
	}
}

func testCapturedDevelopmentCase() eventing.PRDevelopmentCase {
	return eventing.PRDevelopmentCase{
		ID: testDevelopmentCaseID,
		PRDevelopmentCaptureInput: eventing.PRDevelopmentCaptureInput{
			PRDevelopmentCaptureIdentity: eventing.PRDevelopmentCaptureIdentity{
				EventID:          "evt_internal_event",
				DispatchID:       "dsp_internal_dispatch",
				RunID:            "run_internal_run",
				WorkflowRef:      "local://workflows/internal.yaml",
				WorkflowRevision: strings.Repeat("1", 64),
				Connector:        "github/internal-connector",
			},
			Repository:           "octo/repo",
			PullNumber:           17,
			PullURL:              "https://github.com/octo/repo/pull/17",
			PullAuthor:           "owner",
			TargetUser:           "internal-target-user",
			PullState:            eventing.PRDevelopmentPullOpen,
			BaseRepository:       "octo/repo",
			BaseRef:              "main",
			BaseSHA:              strings.Repeat("b", 40),
			HeadRepository:       "fork/repo",
			HeadRef:              "fix-boundary",
			HeadSHA:              strings.Repeat("c", 40),
			ReviewID:             "9007199254740993",
			TriggerReviewNodeID:  "PRR_internal_node",
			ReviewAuthor:         "reviewer",
			SubmittedReviewState: eventing.PRDevelopmentReviewChangesRequested,
			CurrentReviewState:   eventing.PRDevelopmentReviewChangesRequested,
			ReviewCommitSHA:      strings.Repeat("d", 40),
			ReviewSubmittedAt:    time.Date(2026, 8, 5, 12, 0, 0, 0, time.UTC),
			ReviewURL:            "https://github.com/octo/repo/pull/17#pullrequestreview-1",
			Feedback:             "<script>fetch('https://attacker.example')</script>\x00![x](https://attacker.example/x)",
		},
		CreatedAt: time.Date(2026, 8, 5, 12, 1, 0, 0, time.UTC),
		UpdatedAt: time.Date(2026, 8, 5, 12, 1, 0, 0, time.UTC),
	}
}

func assertDevelopmentHeaders(t *testing.T, recorder *httptest.ResponseRecorder) {
	t.Helper()
	if recorder.Header().Get("Cache-Control") != "no-store" ||
		recorder.Header().Get("X-Content-Type-Options") != "nosniff" ||
		recorder.Header().Get("Content-Type") != "application/json; charset=utf-8" {
		t.Fatalf("response headers = %#v", recorder.Header())
	}
}
