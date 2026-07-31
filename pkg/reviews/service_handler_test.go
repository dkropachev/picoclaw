package reviews

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/sipeed/picoclaw/pkg/eventing"
	"github.com/sipeed/picoclaw/pkg/workflows"
)

const (
	serviceTestCaseID       = "prc_11111111111111111111111111111111"
	serviceTestFindingID    = "prf_22222222222222222222222222222222"
	serviceTestFindingTwoID = "prf_33333333333333333333333333333333"
	serviceTestSubmissionID = "prs_44444444444444444444444444444444"
)

var serviceTestTime = time.Date(2026, time.July, 30, 12, 0, 0, 123, time.UTC)

func TestServiceProjectsSubmissionWithoutDurableSecrets(t *testing.T) {
	stored := serviceTestDetail(7)
	stored.Submission = &eventing.ReviewSubmission{
		ID:               serviceTestSubmissionID,
		CaseID:           serviceTestCaseID,
		DraftVersion:     6,
		Marker:           "marker-must-not-leak",
		Status:           eventing.ReviewSubmissionClaimed,
		ClaimFrom:        eventing.ReviewSubmissionPending,
		LeaseToken:       "lease-token-must-not-leak",
		LeaseUntil:       timePointer(serviceTestTime.Add(time.Minute)),
		Attempts:         2,
		Request:          json.RawMessage(`{"raw_request":"must-not-leak"}`),
		PublicErrorCode:  "safe_code",
		InternalError:    "database detail must not leak",
		ExternalReviewID: "987",
		ExternalURL:      "https://github.com/scylladb/gocql/pull/42#pullrequestreview-987",
		CreatedAt:        serviceTestTime.Add(-time.Hour),
		UpdatedAt:        serviceTestTime,
	}
	store := &reviewServiceStore{getResult: stored}
	service := newReviewTestService(t, store, nil, nil)

	detail, err := service.Get(context.Background(), " "+serviceTestCaseID+" ")
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if got, want := store.getCalls, []string{serviceTestCaseID}; !reflect.DeepEqual(got, want) {
		t.Fatalf("GetReviewCase calls = %#v, want %#v", got, want)
	}
	if detail.Submission == nil ||
		detail.Submission.ID != serviceTestSubmissionID ||
		detail.Submission.PublicErrorCode != "safe_code" ||
		detail.Submission.Attempts != 2 {
		t.Fatalf("projected submission = %#v", detail.Submission)
	}
	raw, err := json.Marshal(detail)
	if err != nil {
		t.Fatalf("Marshal(detail) error = %v", err)
	}
	for _, forbidden := range []string{
		"marker-must-not-leak",
		"lease-token-must-not-leak",
		"raw_request",
		"database detail must not leak",
	} {
		if strings.Contains(string(raw), forbidden) {
			t.Fatalf("safe detail exposed %q: %s", forbidden, raw)
		}
	}
	var wire struct {
		Submission map[string]any `json:"submission"`
	}
	if err := json.Unmarshal(raw, &wire); err != nil {
		t.Fatalf("Unmarshal(detail) error = %v", err)
	}
	for _, forbidden := range []string{
		"claim_from",
		"lease_until",
		"lease_token",
		"internal_error",
		"request",
		"marker",
	} {
		if _, exists := wire.Submission[forbidden]; exists {
			t.Fatalf("safe submission contains forbidden field %q: %s", forbidden, raw)
		}
	}
}

func TestServiceListCursorIsBoundToNormalizedFilters(t *testing.T) {
	next := eventing.ReviewCaseCursor{
		UpdatedAt: serviceTestTime,
		ID:        serviceTestCaseID,
	}
	store := &reviewServiceStore{
		listResult: eventing.ReviewCasePage{
			Cases: []eventing.ReviewCase{serviceTestDetail(3).Case},
			Next:  &next,
		},
	}
	service := newReviewTestService(t, store, nil, nil)
	filter := ListRequest{
		Status:     eventing.ReviewCaseOpen,
		Repository: " scylladb/gocql ",
		Limit:      17,
	}

	first, err := service.List(context.Background(), filter)
	if err != nil {
		t.Fatalf("first List() error = %v", err)
	}
	if first.NextCursor == "" {
		t.Fatal("first List() next cursor is empty")
	}
	secondRequest := filter
	secondRequest.Cursor = first.NextCursor
	if _, err = service.List(context.Background(), secondRequest); err != nil {
		t.Fatalf("second List() error = %v", err)
	}
	if len(store.listCalls) != 2 {
		t.Fatalf("ListReviewCases calls = %d, want 2", len(store.listCalls))
	}
	if got := store.listCalls[0]; got.Status != eventing.ReviewCaseOpen ||
		got.Repository != "scylladb/gocql" ||
		got.Limit != 17 ||
		got.After != nil {
		t.Fatalf("first list filter = %#v", got)
	}
	if got := store.listCalls[1]; got.After == nil ||
		got.After.ID != next.ID ||
		!got.After.UpdatedAt.Equal(next.UpdatedAt) {
		t.Fatalf("second list cursor = %#v, want %#v", got.After, next)
	}

	before := len(store.listCalls)
	for _, changed := range []ListRequest{
		{
			Status:     eventing.ReviewCaseSubmitted,
			Repository: "scylladb/gocql",
			Limit:      17,
			Cursor:     first.NextCursor,
		},
		{
			Status:     eventing.ReviewCaseOpen,
			Repository: "scylladb/scylla",
			Limit:      17,
			Cursor:     first.NextCursor,
		},
	} {
		if _, err = service.List(context.Background(), changed); !errors.Is(err, ErrInvalidRequest) {
			t.Fatalf("List(changed filter) error = %v, want ErrInvalidRequest", err)
		}
	}
	if got := len(store.listCalls); got != before {
		t.Fatalf("mismatched cursors reached store: calls = %d, want %d", got, before)
	}
}

func TestServiceDelegatesFindingMutationsExactly(t *testing.T) {
	line := 19
	draft := eventing.ReviewFindingDraft{
		Severity:       eventing.ReviewSeverityHigh,
		Title:          "Updated title",
		File:           "pkg/queue/worker.go",
		Line:           &line,
		Message:        "Updated message",
		Evidence:       "The error branch returns before restoration.",
		Impact:         "A queued item can be lost.",
		Recommendation: "Restore before returning.",
		Validation:     "Run the queue fault-injection test.",
	}
	store := &reviewServiceStore{
		updateResult:  serviceTestDetail(8),
		dropResult:    serviceTestDetail(9),
		restoreResult: serviceTestDetail(10),
	}
	service := newReviewTestService(t, store, nil, nil)

	if _, err := service.UpdateFinding(context.Background(), UpdateFindingRequest{
		CaseID:          " " + serviceTestCaseID + " ",
		FindingID:       " " + serviceTestFindingID + " ",
		ExpectedVersion: 7,
		Finding:         draft,
	}); err != nil {
		t.Fatalf("UpdateFinding() error = %v", err)
	}
	if _, err := service.DropFinding(context.Background(), TransitionFindingRequest{
		CaseID:          " " + serviceTestCaseID + " ",
		FindingID:       " " + serviceTestFindingID + " ",
		ExpectedVersion: 8,
		Reason:          "duplicate",
	}); err != nil {
		t.Fatalf("DropFinding() error = %v", err)
	}
	if _, err := service.RestoreFinding(context.Background(), TransitionFindingRequest{
		CaseID:          " " + serviceTestCaseID + " ",
		FindingID:       " " + serviceTestFindingID + " ",
		ExpectedVersion: 9,
		Reason:          "reconsidered",
	}); err != nil {
		t.Fatalf("RestoreFinding() error = %v", err)
	}

	if len(store.updateCalls) != 1 {
		t.Fatalf("UpdateReviewFinding calls = %d, want 1", len(store.updateCalls))
	}
	update := store.updateCalls[0]
	if update.CaseID != serviceTestCaseID ||
		update.FindingID != serviceTestFindingID ||
		update.ExpectedVersion != 7 ||
		!reflect.DeepEqual(update.Finding, draft) {
		t.Fatalf("UpdateReviewFinding input = %#v", update)
	}
	if got, want := store.dropCalls, []eventing.ReviewFindingTransition{{
		CaseID:          serviceTestCaseID,
		FindingID:       serviceTestFindingID,
		ExpectedVersion: 8,
		Reason:          "duplicate",
	}}; !reflect.DeepEqual(got, want) {
		t.Fatalf("DropReviewFinding calls = %#v, want %#v", got, want)
	}
	if got, want := store.restoreCalls, []eventing.ReviewFindingTransition{{
		CaseID:          serviceTestCaseID,
		FindingID:       serviceTestFindingID,
		ExpectedVersion: 9,
		Reason:          "reconsidered",
	}}; !reflect.DeepEqual(got, want) {
		t.Fatalf("RestoreReviewFinding calls = %#v, want %#v", got, want)
	}
}

func TestServiceChatDurablyAdvancesVersionsAndIsolatesAgent(t *testing.T) {
	afterUser := serviceTestDetail(8)
	afterUser.Messages = []eventing.ReviewMessage{{
		ID:        "prm_55555555555555555555555555555555",
		CaseID:    serviceTestCaseID,
		Ordinal:   1,
		FindingID: serviceTestFindingID,
		Kind:      eventing.ReviewMessageChat,
		Role:      eventing.ReviewMessageUser,
		Content:   "Explain the failure mode.",
		CreatedAt: serviceTestTime,
	}}
	afterAssistant := afterUser
	afterAssistant.Case.Version = 9
	afterAssistant.Messages = append(
		append([]eventing.ReviewMessage(nil), afterUser.Messages...),
		eventing.ReviewMessage{
			ID:        "prm_66666666666666666666666666666666",
			CaseID:    serviceTestCaseID,
			Ordinal:   2,
			FindingID: serviceTestFindingID,
			Kind:      eventing.ReviewMessageChat,
			Role:      eventing.ReviewMessageAssistant,
			Content:   "The item is removed before the error branch returns.",
			CreatedAt: serviceTestTime.Add(time.Second),
		},
	)
	store := &reviewServiceStore{
		appendResults: []eventing.ReviewCaseDetail{afterUser, afterAssistant},
	}
	agent := &reviewAgentFake{
		outputs: []map[string]any{{
			"text": "  The item is removed before the error branch returns.  ",
		}},
	}
	service := newReviewTestService(t, store, agent, nil)

	detail, err := service.Chat(context.Background(), ChatRequest{
		CaseID:          " " + serviceTestCaseID + " ",
		ExpectedVersion: 7,
		FindingID:       " " + serviceTestFindingID + " ",
		Content:         "  Explain the failure mode.  ",
	})
	if err != nil {
		t.Fatalf("Chat() error = %v", err)
	}
	if detail.Case.Version != 9 || len(detail.Messages) != 2 {
		t.Fatalf("Chat() detail = %#v", detail)
	}
	if len(store.appendCalls) != 2 {
		t.Fatalf("AppendReviewMessages calls = %d, want 2", len(store.appendCalls))
	}
	assertMessageAppend(t, store.appendCalls[0], 7, eventing.ReviewMessageDraft{
		FindingID: serviceTestFindingID,
		Kind:      eventing.ReviewMessageChat,
		Role:      eventing.ReviewMessageUser,
		Content:   "Explain the failure mode.",
	})
	assertMessageAppend(t, store.appendCalls[1], 8, eventing.ReviewMessageDraft{
		FindingID: serviceTestFindingID,
		Kind:      eventing.ReviewMessageChat,
		Role:      eventing.ReviewMessageAssistant,
		Content:   "The item is removed before the error branch returns.",
	})
	if len(agent.requests) != 1 {
		t.Fatalf("RunAgent calls = %d, want 1", len(agent.requests))
	}
	assertIsolatedReviewAgentRequest(t, agent.requests[0])
	if agent.requests[0].Output != nil {
		t.Fatalf("chat output contract = %#v, want nil", agent.requests[0].Output)
	}
	if !strings.Contains(agent.requests[0].Context, "Explain the failure mode.") ||
		!strings.Contains(agent.requests[0].Context, serviceTestFindingID) {
		t.Fatalf("chat context omitted durable transcript/finding: %s", agent.requests[0].Context)
	}
}

func TestServiceChatBoundsAssistantResponseBeforePersistence(t *testing.T) {
	t.Run("exact message boundary", func(t *testing.T) {
		response := strings.Repeat("x", eventing.MaxReviewMessageBytes)
		store := &reviewServiceStore{
			appendResults: []eventing.ReviewCaseDetail{
				serviceTestDetail(8),
				serviceTestDetail(9),
			},
		}
		agent := &reviewAgentFake{outputs: []map[string]any{{"text": response}}}
		service := newReviewTestService(t, store, agent, nil)

		_, err := service.Chat(context.Background(), ChatRequest{
			CaseID:          serviceTestCaseID,
			ExpectedVersion: 7,
			Content:         "Explain this.",
		})
		if err != nil {
			t.Fatalf("Chat() error = %v", err)
		}
		if len(store.appendCalls) != 2 {
			t.Fatalf("AppendReviewMessages calls = %d, want 2", len(store.appendCalls))
		}
		if got := store.appendCalls[1].Messages[0].Content; got != response {
			t.Fatalf("assistant response bytes = %d, want %d", len(got), len(response))
		}
	})

	tests := []struct {
		name     string
		response string
	}{
		{name: "empty after trim", response: " \n\t "},
		{
			name:     "one byte over",
			response: strings.Repeat("x", eventing.MaxReviewMessageBytes+1),
		},
		{name: "NUL", response: "unsafe\x00response"},
		{name: "invalid UTF-8", response: string([]byte{'o', 'k', 0xff})},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store := &reviewServiceStore{
				appendResults: []eventing.ReviewCaseDetail{serviceTestDetail(8)},
			}
			agent := &reviewAgentFake{
				outputs: []map[string]any{{"text": test.response}},
			}
			service := newReviewTestService(t, store, agent, nil)

			_, err := service.Chat(context.Background(), ChatRequest{
				CaseID:          serviceTestCaseID,
				ExpectedVersion: 7,
				Content:         "Explain this.",
			})
			if !errors.Is(err, ErrUnavailable) {
				t.Fatalf("Chat() error = %v, want ErrUnavailable", err)
			}
			if len(store.appendCalls) != 1 {
				t.Fatalf(
					"AppendReviewMessages calls = %d, want only durable user prompt",
					len(store.appendCalls),
				)
			}
		})
	}
}

func TestServiceRephraseDurablyRecordsStructuredSuggestionWithoutEditing(t *testing.T) {
	afterUser := serviceTestDetail(12)
	afterUser.Messages = []eventing.ReviewMessage{{
		ID:        "prm_77777777777777777777777777777777",
		CaseID:    serviceTestCaseID,
		Ordinal:   1,
		FindingID: serviceTestFindingID,
		Kind:      eventing.ReviewMessageRephrase,
		Role:      eventing.ReviewMessageUser,
		Content:   "Make it concise.",
		CreatedAt: serviceTestTime,
	}}
	afterAssistant := afterUser
	afterAssistant.Case.Version = 13
	afterAssistant.Messages = append(
		append([]eventing.ReviewMessage(nil), afterUser.Messages...),
		eventing.ReviewMessage{
			ID:        "prm_88888888888888888888888888888888",
			CaseID:    serviceTestCaseID,
			Ordinal:   2,
			FindingID: serviceTestFindingID,
			Kind:      eventing.ReviewMessageRephrase,
			Role:      eventing.ReviewMessageAssistant,
			Content:   `{"title":"Restore queued item","message":"Restore the item before returning."}`,
			CreatedAt: serviceTestTime.Add(time.Second),
		},
	)
	store := &reviewServiceStore{
		appendResults: []eventing.ReviewCaseDetail{afterUser, afterAssistant},
	}
	agent := &reviewAgentFake{
		outputs: []map[string]any{{
			"text":             `{"title":"Restore queued item","message":"Restore the item before returning."}`,
			"structured_valid": true,
			"structured": map[string]any{
				"title":   "  Restore queued item \n",
				"message": "\t Restore the item before returning.  ",
			},
		}},
	}
	service := newReviewTestService(t, store, agent, nil)

	result, err := service.Rephrase(context.Background(), RephraseRequest{
		CaseID:          serviceTestCaseID,
		FindingID:       serviceTestFindingID,
		ExpectedVersion: 11,
		Instruction:     "  Make it concise. ",
	})
	if err != nil {
		t.Fatalf("Rephrase() error = %v", err)
	}
	if got, want := result.Suggestion, (RephraseSuggestion{
		Title:   "Restore queued item",
		Message: "Restore the item before returning.",
	}); got != want {
		t.Fatalf("suggestion = %#v, want %#v", got, want)
	}
	if result.Detail.Case.Version != 13 ||
		result.Detail.Findings[0].Title != afterAssistant.Findings[0].Title {
		t.Fatalf("Rephrase() unexpectedly edited finding: %#v", result.Detail)
	}
	if len(store.updateCalls) != 0 {
		t.Fatalf("UpdateReviewFinding calls = %d, want 0", len(store.updateCalls))
	}
	if len(store.appendCalls) != 2 {
		t.Fatalf("AppendReviewMessages calls = %d, want 2", len(store.appendCalls))
	}
	assertMessageAppend(t, store.appendCalls[0], 11, eventing.ReviewMessageDraft{
		FindingID: serviceTestFindingID,
		Kind:      eventing.ReviewMessageRephrase,
		Role:      eventing.ReviewMessageUser,
		Content:   "Make it concise.",
	})
	if store.appendCalls[1].ExpectedVersion != 12 ||
		len(store.appendCalls[1].Messages) != 1 {
		t.Fatalf("assistant append = %#v", store.appendCalls[1])
	}
	var recorded RephraseSuggestion
	if err := json.Unmarshal(
		[]byte(store.appendCalls[1].Messages[0].Content),
		&recorded,
	); err != nil || recorded != result.Suggestion {
		t.Fatalf("recorded structured suggestion = %#v, error = %v", recorded, err)
	}
	if len(agent.requests) != 1 {
		t.Fatalf("RunAgent calls = %d, want 1", len(agent.requests))
	}
	assertIsolatedReviewAgentRequest(t, agent.requests[0])
	output := agent.requests[0].Output
	if output == nil || output.Format != "json" || output.RepairAttempts != 1 {
		t.Fatalf("rephrase output contract = %#v", output)
	}
	schemaRaw, _ := json.Marshal(output.Schema)
	if !strings.Contains(string(schemaRaw), `"additionalProperties":false`) ||
		!strings.Contains(string(schemaRaw), `"title"`) ||
		!strings.Contains(string(schemaRaw), `"message"`) {
		t.Fatalf("rephrase schema = %s", schemaRaw)
	}
}

func TestServiceRephraseBoundsStructuredFieldsAndCombinedMessage(t *testing.T) {
	t.Run("exact title boundary", func(t *testing.T) {
		title := strings.Repeat("t", maxReviewRephraseTitle)
		store := &reviewServiceStore{
			appendResults: []eventing.ReviewCaseDetail{
				serviceTestDetail(8),
				serviceTestDetail(9),
			},
		}
		agent := &reviewAgentFake{outputs: []map[string]any{{
			"text":             "{}",
			"structured_valid": true,
			"structured": map[string]any{
				"title":   title,
				"message": "message",
			},
		}}}
		service := newReviewTestService(t, store, agent, nil)

		result, err := service.Rephrase(context.Background(), RephraseRequest{
			CaseID:          serviceTestCaseID,
			FindingID:       serviceTestFindingID,
			ExpectedVersion: 7,
			Instruction:     "Rewrite.",
		})
		if err != nil {
			t.Fatalf("Rephrase() error = %v", err)
		}
		if result.Suggestion.Title != title {
			t.Fatalf(
				"title bytes = %d, want %d",
				len(result.Suggestion.Title),
				len(title),
			)
		}
	})

	t.Run("exact combined assistant JSON boundary", func(t *testing.T) {
		const title = "T"
		empty, err := json.Marshal(RephraseSuggestion{
			Title:   title,
			Message: "",
		})
		if err != nil {
			t.Fatal(err)
		}
		message := strings.Repeat(
			"m",
			eventing.MaxReviewMessageBytes-len(empty),
		)
		expected, err := json.Marshal(RephraseSuggestion{
			Title:   title,
			Message: message,
		})
		if err != nil {
			t.Fatal(err)
		}
		if len(expected) != eventing.MaxReviewMessageBytes {
			t.Fatalf(
				"test assistant JSON bytes = %d, want %d",
				len(expected),
				eventing.MaxReviewMessageBytes,
			)
		}
		store := &reviewServiceStore{
			appendResults: []eventing.ReviewCaseDetail{
				serviceTestDetail(8),
				serviceTestDetail(9),
			},
		}
		agent := &reviewAgentFake{outputs: []map[string]any{{
			"text":             "{}",
			"structured_valid": true,
			"structured": map[string]any{
				"title":   title,
				"message": message,
			},
		}}}
		service := newReviewTestService(t, store, agent, nil)

		_, err = service.Rephrase(context.Background(), RephraseRequest{
			CaseID:          serviceTestCaseID,
			FindingID:       serviceTestFindingID,
			ExpectedVersion: 7,
			Instruction:     "Rewrite.",
		})
		if err != nil {
			t.Fatalf("Rephrase() error = %v", err)
		}
		if got := store.appendCalls[1].Messages[0].Content; len(got) != len(expected) {
			t.Fatalf("stored assistant JSON bytes = %d, want %d", len(got), len(expected))
		}
	})

	tests := []struct {
		name    string
		title   string
		message string
	}{
		{
			name:    "title one byte over",
			title:   strings.Repeat("t", maxReviewRephraseTitle+1),
			message: "message",
		},
		{
			name:    "message one byte over",
			title:   "title",
			message: strings.Repeat("m", eventing.MaxReviewMessageBytes+1),
		},
		{name: "title NUL", title: "unsafe\x00title", message: "message"},
		{
			name:    "message invalid UTF-8",
			title:   "title",
			message: string([]byte{'o', 'k', 0xff}),
		},
		{
			name:  "combined JSON over message bound",
			title: strings.Repeat("t", maxReviewRephraseTitle),
			message: strings.Repeat(
				"m",
				eventing.MaxReviewMessageBytes-maxReviewRephraseTitle,
			),
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store := &reviewServiceStore{
				appendResults: []eventing.ReviewCaseDetail{serviceTestDetail(8)},
			}
			agent := &reviewAgentFake{outputs: []map[string]any{{
				"text":             "{}",
				"structured_valid": true,
				"structured": map[string]any{
					"title":   test.title,
					"message": test.message,
				},
			}}}
			service := newReviewTestService(t, store, agent, nil)

			_, err := service.Rephrase(context.Background(), RephraseRequest{
				CaseID:          serviceTestCaseID,
				FindingID:       serviceTestFindingID,
				ExpectedVersion: 7,
				Instruction:     "Rewrite.",
			})
			if !errors.Is(err, ErrUnavailable) {
				t.Fatalf("Rephrase() error = %v, want ErrUnavailable", err)
			}
			if len(store.appendCalls) != 1 {
				t.Fatalf(
					"AppendReviewMessages calls = %d, want only durable instruction",
					len(store.appendCalls),
				)
			}
		})
	}
}

func TestServiceSubmitCreatesImmutableActiveOnlySnapshot(t *testing.T) {
	line := 27
	stored := serviceTestDetail(14)
	stored.Case.Summary = "Two actionable findings."
	stored.Case.ActiveFindings = 2
	stored.Case.TotalFindings = 3
	stored.Findings = []eventing.ReviewFinding{
		{
			ID:      serviceTestFindingID,
			CaseID:  serviceTestCaseID,
			Ordinal: 1,
			State:   eventing.ReviewFindingActive,
			Title:   "Line finding",
			File:    "pkg/queue/worker.go",
			Line:    &line,
			Message: "Restore before returning.",
		},
		{
			ID:      serviceTestFindingTwoID,
			CaseID:  serviceTestCaseID,
			Ordinal: 2,
			State:   eventing.ReviewFindingDropped,
			Title:   "Dropped title must not be submitted",
			Message: "Dropped message must not be submitted",
		},
		{
			ID:      "prf_99999999999999999999999999999999",
			CaseID:  serviceTestCaseID,
			Ordinal: 3,
			State:   eventing.ReviewFindingActive,
			Title:   "Body finding",
			Message: "Add rollout validation.",
		},
	}
	created := stored
	created.Case.Status = eventing.ReviewCaseSubmitting
	created.Case.Version = 15
	created.Submission = &eventing.ReviewSubmission{
		ID:           serviceTestSubmissionID,
		CaseID:       serviceTestCaseID,
		DraftVersion: 14,
		Status:       eventing.ReviewSubmissionPending,
	}
	store := &reviewServiceStore{
		getResult:    stored,
		createResult: created,
	}
	submitter := &reviewSubmitterFake{}
	service := newReviewTestService(t, store, nil, submitter)

	detail, err := service.Submit(context.Background(), SubmitCaseRequest{
		CaseID:          serviceTestCaseID,
		ExpectedVersion: 14,
	})
	if err != nil {
		t.Fatalf("Submit() error = %v", err)
	}
	if detail.Case.Status != eventing.ReviewCaseSubmitting ||
		detail.Submission == nil ||
		detail.Submission.Status != eventing.ReviewSubmissionPending {
		t.Fatalf("Submit() detail = %#v", detail)
	}
	if len(submitter.requests) != 0 {
		t.Fatalf("request-path GitHub calls = %d, want 0", len(submitter.requests))
	}
	if len(store.createCalls) != 1 {
		t.Fatalf("CreateReviewSubmission calls = %d, want 1", len(store.createCalls))
	}
	draft := store.createCalls[0]
	if draft.CaseID != serviceTestCaseID ||
		draft.ExpectedVersion != 14 ||
		draft.Marker != "<!-- picoclaw-review:"+serviceTestCaseID+":v14 -->" {
		t.Fatalf("submission draft identity = %#v", draft)
	}
	var request SubmitRequest
	if err := json.Unmarshal(draft.Request, &request); err != nil {
		t.Fatalf("decode immutable request: %v", err)
	}
	if request.Owner != "scylladb" ||
		request.Repo != "gocql" ||
		request.PullNumber != 42 ||
		request.HeadSHA != strings.Repeat("b", 40) ||
		request.Summary != stored.Case.Summary ||
		request.Marker != draft.Marker {
		t.Fatalf("immutable request identity = %#v", request)
	}
	if len(request.Findings) != 2 ||
		request.Findings[0].ID != serviceTestFindingID ||
		request.Findings[1].Title != "Body finding" {
		t.Fatalf("immutable active findings = %#v", request.Findings)
	}
	if strings.Contains(string(draft.Request), "Dropped title") ||
		strings.Contains(string(draft.Request), "Dropped message") {
		t.Fatalf("immutable request contains dropped finding: %s", draft.Request)
	}
}

func TestServiceSubmitRejectsAllDroppedWithoutOutboxOrGitHubCall(t *testing.T) {
	stored := serviceTestDetail(22)
	stored.Case.Status = eventing.ReviewCaseAllDropped
	stored.Case.ActiveFindings = 0
	stored.Findings[0].State = eventing.ReviewFindingDropped
	store := &reviewServiceStore{getResult: stored}
	submitter := &reviewSubmitterFake{}
	service := newReviewTestService(t, store, nil, submitter)

	_, err := service.Submit(context.Background(), SubmitCaseRequest{
		CaseID:          serviceTestCaseID,
		ExpectedVersion: 22,
	})
	if !errors.Is(err, eventing.ErrInvalidTransition) {
		t.Fatalf("Submit() error = %v, want ErrInvalidTransition", err)
	}
	if len(store.createCalls) != 0 || len(submitter.requests) != 0 {
		t.Fatalf(
			"all-dropped submission side effects: outbox=%d github=%d",
			len(store.createCalls),
			len(submitter.requests),
		)
	}
}

func TestServiceReconcileDelegatesHumanResolutionAndProjectsSafeDetail(t *testing.T) {
	stored := serviceTestDetail(31)
	stored.Case.Status = eventing.ReviewCaseSubmitted
	stored.Submission = &eventing.ReviewSubmission{
		ID:               serviceTestSubmissionID,
		CaseID:           serviceTestCaseID,
		DraftVersion:     29,
		Marker:           "private-marker",
		Status:           eventing.ReviewSubmissionSubmitted,
		Request:          json.RawMessage(`{"private":"request"}`),
		InternalError:    "private diagnostic",
		ExternalReviewID: "987",
		ExternalURL:      "https://github.com/scylladb/gocql/pull/42#pullrequestreview-987",
		CreatedAt:        serviceTestTime,
		UpdatedAt:        serviceTestTime,
	}
	store := &reviewServiceStore{reconcileResult: stored}
	service := newReviewTestService(t, store, nil, nil)

	detail, err := service.Reconcile(context.Background(), ReconcileCaseRequest{
		CaseID:          " " + serviceTestCaseID + " ",
		ExpectedVersion: 30,
		Resolution:      eventing.ReviewReconciliationSubmitted,
	})
	if err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}
	if got, want := store.reconcileCalls, []eventing.ReviewSubmissionReconciliation{{
		CaseID:          serviceTestCaseID,
		ExpectedVersion: 30,
		Resolution:      eventing.ReviewReconciliationSubmitted,
	}}; !reflect.DeepEqual(got, want) {
		t.Fatalf("ReconcileReviewSubmission calls = %#v, want %#v", got, want)
	}
	raw, err := json.Marshal(detail)
	if err != nil {
		t.Fatalf("Marshal(detail) error = %v", err)
	}
	if !strings.Contains(string(raw), stored.Submission.ExternalURL) {
		t.Fatalf("safe external URL missing: %s", raw)
	}
	for _, forbidden := range []string{
		"private-marker",
		`"private":"request"`,
		"private diagnostic",
	} {
		if strings.Contains(string(raw), forbidden) {
			t.Fatalf("reconcile detail exposed %q: %s", forbidden, raw)
		}
	}
}

func TestReviewHandlerRoutesStrictReconciliation(t *testing.T) {
	resolved := serviceTestDetail(18)
	resolved.Case.Status = eventing.ReviewCaseOpen
	store := &reviewServiceStore{reconcileResult: resolved}
	handler := &Handler{Service: newReviewTestService(t, store, nil, nil)}
	request := httptest.NewRequest(
		http.MethodPost,
		RuntimeRoutePrefix+"/"+serviceTestCaseID+"/reconcile",
		strings.NewReader(`{"expected_version":17,"resolution":"absent"}`),
	)
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", response.Code, response.Body.String())
	}
	if got, want := store.reconcileCalls, []eventing.ReviewSubmissionReconciliation{{
		CaseID:          serviceTestCaseID,
		ExpectedVersion: 17,
		Resolution:      eventing.ReviewReconciliationAbsent,
	}}; !reflect.DeepEqual(got, want) {
		t.Fatalf("reconcile calls = %#v, want %#v", got, want)
	}
	assertReviewResponseHeaders(t, response)

	bad := httptest.NewRequest(
		http.MethodPost,
		RuntimeRoutePrefix+"/"+serviceTestCaseID+"/reconcile",
		strings.NewReader(
			`{"expected_version":17,"resolution":"absent","resolution":"submitted"}`,
		),
	)
	bad.Header.Set("Content-Type", "application/json")
	badResponse := httptest.NewRecorder()
	handler.ServeHTTP(badResponse, bad)
	if badResponse.Code != http.StatusBadRequest {
		t.Fatalf(
			"duplicate resolution status = %d, want 400; body=%s",
			badResponse.Code,
			badResponse.Body.String(),
		)
	}
	if len(store.reconcileCalls) != 1 {
		t.Fatalf("invalid body reached store: %#v", store.reconcileCalls)
	}

	invalidResolution := httptest.NewRequest(
		http.MethodPost,
		RuntimeRoutePrefix+"/"+serviceTestCaseID+"/reconcile",
		strings.NewReader(`{"expected_version":17,"resolution":"retry"}`),
	)
	invalidResolution.Header.Set("Content-Type", "application/json")
	invalidResponse := httptest.NewRecorder()
	handler.ServeHTTP(invalidResponse, invalidResolution)
	if invalidResponse.Code != http.StatusBadRequest {
		t.Fatalf(
			"invalid resolution status = %d, want 400; body=%s",
			invalidResponse.Code,
			invalidResponse.Body.String(),
		)
	}
	if len(store.reconcileCalls) != 1 {
		t.Fatalf("invalid resolution reached store: %#v", store.reconcileCalls)
	}
}

func TestReviewHandlerRejectsNonCanonicalRoutesQueriesAndBodies(t *testing.T) {
	validFindingPath := RuntimeRoutePrefix + "/" + serviceTestCaseID +
		"/findings/" + serviceTestFindingID
	tests := []struct {
		name        string
		method      string
		target      string
		body        string
		contentType string
		encoding    string
		wantStatus  int
		wantAllow   string
	}{
		{
			name:       "wrong collection method",
			method:     http.MethodPost,
			target:     RuntimeRoutePrefix,
			wantStatus: http.StatusMethodNotAllowed,
			wantAllow:  http.MethodGet,
		},
		{
			name:       "unknown query",
			method:     http.MethodGet,
			target:     RuntimeRoutePrefix + "?extra=1",
			wantStatus: http.StatusBadRequest,
		},
		{
			name:       "duplicate query",
			method:     http.MethodGet,
			target:     RuntimeRoutePrefix + "?limit=1&limit=2",
			wantStatus: http.StatusBadRequest,
		},
		{
			name:       "noncanonical limit",
			method:     http.MethodGet,
			target:     RuntimeRoutePrefix + "?limit=01",
			wantStatus: http.StatusBadRequest,
		},
		{
			name:       "uppercase ID",
			method:     http.MethodGet,
			target:     RuntimeRoutePrefix + "/prc_AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA",
			wantStatus: http.StatusBadRequest,
		},
		{
			name:       "unknown case route",
			method:     http.MethodGet,
			target:     RuntimeRoutePrefix + "/" + serviceTestCaseID + "/unknown",
			wantStatus: http.StatusNotFound,
		},
		{
			name:       "mutation query",
			method:     http.MethodPatch,
			target:     validFindingPath + "?force=true",
			wantStatus: http.StatusBadRequest,
		},
		{
			name:       "wrong finding method",
			method:     http.MethodPost,
			target:     validFindingPath,
			wantStatus: http.StatusMethodNotAllowed,
			wantAllow:  http.MethodPatch,
		},
		{
			name:       "missing content type",
			method:     http.MethodPatch,
			target:     validFindingPath,
			body:       `{"expected_version":1,"finding":{}}`,
			wantStatus: http.StatusBadRequest,
		},
		{
			name:        "unsupported charset",
			method:      http.MethodPatch,
			target:      validFindingPath,
			body:        `{"expected_version":1,"finding":{}}`,
			contentType: "application/json; charset=latin1",
			wantStatus:  http.StatusBadRequest,
		},
		{
			name:        "compressed body",
			method:      http.MethodPatch,
			target:      validFindingPath,
			body:        `{"expected_version":1,"finding":{}}`,
			contentType: "application/json",
			encoding:    "gzip",
			wantStatus:  http.StatusBadRequest,
		},
		{
			name:        "unknown body field",
			method:      http.MethodPatch,
			target:      validFindingPath,
			body:        `{"expected_version":1,"finding":{},"force":true}`,
			contentType: "application/json",
			wantStatus:  http.StatusBadRequest,
		},
		{
			name:        "duplicate top-level field",
			method:      http.MethodPatch,
			target:      validFindingPath,
			body:        `{"expected_version":1,"expected_version":2,"finding":{}}`,
			contentType: "application/json",
			wantStatus:  http.StatusBadRequest,
		},
		{
			name:        "duplicate nested field",
			method:      http.MethodPatch,
			target:      validFindingPath,
			body:        `{"expected_version":1,"finding":{"title":"first","title":"second"}}`,
			contentType: "application/json",
			wantStatus:  http.StatusBadRequest,
		},
		{
			name:        "trailing JSON",
			method:      http.MethodPatch,
			target:      validFindingPath,
			body:        `{"expected_version":1,"finding":{}} {}`,
			contentType: "application/json",
			wantStatus:  http.StatusBadRequest,
		},
		{
			name:        "known-length oversized body",
			method:      http.MethodPatch,
			target:      validFindingPath,
			body:        strings.Repeat("x", maxReviewRequestBody+1),
			contentType: "application/json",
			wantStatus:  http.StatusRequestEntityTooLarge,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store := &reviewServiceStore{}
			handler := &Handler{Service: newReviewTestService(t, store, nil, nil)}
			request := httptest.NewRequest(
				test.method,
				test.target,
				strings.NewReader(test.body),
			)
			if test.contentType != "" {
				request.Header.Set("Content-Type", test.contentType)
			}
			if test.encoding != "" {
				request.Header.Set("Content-Encoding", test.encoding)
			}
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if response.Code != test.wantStatus {
				t.Fatalf(
					"status = %d, want %d; body=%s",
					response.Code,
					test.wantStatus,
					response.Body.String(),
				)
			}
			if got := response.Header().Get("Allow"); got != test.wantAllow {
				t.Fatalf("Allow = %q, want %q", got, test.wantAllow)
			}
			assertReviewResponseHeaders(t, response)
			if len(store.listCalls)+len(store.updateCalls) != 0 {
				t.Fatalf("invalid request reached store: %#v", store)
			}
		})
	}
}

func TestReviewHandlerRoutesAllValidWorkbenchOperations(t *testing.T) {
	tests := []struct {
		name       string
		method     string
		target     string
		body       string
		wantStatus int
		assertCall func(*testing.T, *reviewServiceStore)
	}{
		{
			name:       "list",
			method:     http.MethodGet,
			target:     RuntimeRoutePrefix + "?status=open&repository=scylladb%2Fgocql&limit=5",
			wantStatus: http.StatusOK,
			assertCall: func(t *testing.T, store *reviewServiceStore) {
				t.Helper()
				if len(store.listCalls) != 1 {
					t.Fatalf("ListReviewCases calls = %d, want 1", len(store.listCalls))
				}
				call := store.listCalls[0]
				if call.Status != eventing.ReviewCaseOpen ||
					call.Repository != "scylladb/gocql" ||
					call.Limit != 5 {
					t.Fatalf("ListReviewCases input = %#v", call)
				}
			},
		},
		{
			name:       "get",
			method:     http.MethodGet,
			target:     RuntimeRoutePrefix + "/" + serviceTestCaseID,
			wantStatus: http.StatusOK,
			assertCall: func(t *testing.T, store *reviewServiceStore) {
				t.Helper()
				if got, want := store.getCalls, []string{serviceTestCaseID}; !reflect.DeepEqual(got, want) {
					t.Fatalf("GetReviewCase calls = %#v, want %#v", got, want)
				}
			},
		},
		{
			name:   "edit",
			method: http.MethodPatch,
			target: RuntimeRoutePrefix + "/" + serviceTestCaseID +
				"/findings/" + serviceTestFindingID,
			body: `{
				"expected_version":7,
				"finding":{
					"severity":"high",
					"title":"Edited title",
					"message":"Edited message"
				}
			}`,
			wantStatus: http.StatusOK,
			assertCall: func(t *testing.T, store *reviewServiceStore) {
				t.Helper()
				if len(store.updateCalls) != 1 ||
					store.updateCalls[0].CaseID != serviceTestCaseID ||
					store.updateCalls[0].FindingID != serviceTestFindingID ||
					store.updateCalls[0].ExpectedVersion != 7 ||
					store.updateCalls[0].Finding.Title != "Edited title" {
					t.Fatalf("UpdateReviewFinding calls = %#v", store.updateCalls)
				}
			},
		},
		{
			name:   "drop",
			method: http.MethodPost,
			target: RuntimeRoutePrefix + "/" + serviceTestCaseID +
				"/findings/" + serviceTestFindingID + "/drop",
			body:       `{"expected_version":7,"reason":"not actionable"}`,
			wantStatus: http.StatusOK,
			assertCall: func(t *testing.T, store *reviewServiceStore) {
				t.Helper()
				if len(store.dropCalls) != 1 ||
					store.dropCalls[0].ExpectedVersion != 7 ||
					store.dropCalls[0].Reason != "not actionable" {
					t.Fatalf("DropReviewFinding calls = %#v", store.dropCalls)
				}
			},
		},
		{
			name:   "restore",
			method: http.MethodPost,
			target: RuntimeRoutePrefix + "/" + serviceTestCaseID +
				"/findings/" + serviceTestFindingID + "/restore",
			body:       `{"expected_version":7,"reason":"reconsidered"}`,
			wantStatus: http.StatusOK,
			assertCall: func(t *testing.T, store *reviewServiceStore) {
				t.Helper()
				if len(store.restoreCalls) != 1 ||
					store.restoreCalls[0].ExpectedVersion != 7 ||
					store.restoreCalls[0].Reason != "reconsidered" {
					t.Fatalf("RestoreReviewFinding calls = %#v", store.restoreCalls)
				}
			},
		},
		{
			name:   "chat",
			method: http.MethodPost,
			target: RuntimeRoutePrefix + "/" + serviceTestCaseID + "/chat",
			body: `{
				"expected_version":7,
				"finding_id":"` + serviceTestFindingID + `",
				"content":"Explain this."
			}`,
			wantStatus: http.StatusOK,
			assertCall: func(t *testing.T, store *reviewServiceStore) {
				t.Helper()
				if len(store.appendCalls) != 2 ||
					store.appendCalls[0].Messages[0].Kind != eventing.ReviewMessageChat ||
					store.appendCalls[0].Messages[0].Content != "Explain this." {
					t.Fatalf("chat message appends = %#v", store.appendCalls)
				}
			},
		},
		{
			name:   "rephrase",
			method: http.MethodPost,
			target: RuntimeRoutePrefix + "/" + serviceTestCaseID +
				"/findings/" + serviceTestFindingID + "/rephrase",
			body:       `{"expected_version":7,"instruction":"Make it direct."}`,
			wantStatus: http.StatusOK,
			assertCall: func(t *testing.T, store *reviewServiceStore) {
				t.Helper()
				if len(store.appendCalls) != 2 ||
					store.appendCalls[0].Messages[0].Kind != eventing.ReviewMessageRephrase ||
					store.appendCalls[0].Messages[0].Content != "Make it direct." {
					t.Fatalf("rephrase message appends = %#v", store.appendCalls)
				}
			},
		},
		{
			name:   "submit",
			method: http.MethodPost,
			target: RuntimeRoutePrefix + "/" + serviceTestCaseID + "/submit",
			body:   `{"expected_version":7}`,
			// Submission is durable and asynchronous; the request path never
			// waits for GitHub.
			wantStatus: http.StatusAccepted,
			assertCall: func(t *testing.T, store *reviewServiceStore) {
				t.Helper()
				if len(store.createCalls) != 1 ||
					store.createCalls[0].CaseID != serviceTestCaseID ||
					store.createCalls[0].ExpectedVersion != 7 {
					t.Fatalf("CreateReviewSubmission calls = %#v", store.createCalls)
				}
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			base := serviceTestDetail(7)
			afterFirstMessage := serviceTestDetail(8)
			afterSecondMessage := serviceTestDetail(9)
			created := serviceTestDetail(8)
			created.Case.Status = eventing.ReviewCaseSubmitting
			created.Submission = &eventing.ReviewSubmission{
				ID:           serviceTestSubmissionID,
				CaseID:       serviceTestCaseID,
				DraftVersion: 7,
				Status:       eventing.ReviewSubmissionPending,
			}
			store := &reviewServiceStore{
				getResult:    base,
				listResult:   eventing.ReviewCasePage{Cases: []eventing.ReviewCase{base.Case}},
				updateResult: serviceTestDetail(8),
				dropResult:   serviceTestDetail(8),
				restoreResult: serviceTestDetail(
					8,
				),
				appendResults: []eventing.ReviewCaseDetail{
					afterFirstMessage,
					afterSecondMessage,
				},
				createResult: created,
			}
			agent := &reviewAgentFake{outputs: []map[string]any{{
				"text":             "A concise explanation.",
				"structured_valid": true,
				"structured": map[string]any{
					"title":   "Direct title",
					"message": "Direct message.",
				},
			}}}
			handler := &Handler{Service: newReviewTestService(
				t,
				store,
				agent,
				&reviewSubmitterFake{},
			)}
			request := httptest.NewRequest(
				test.method,
				test.target,
				strings.NewReader(test.body),
			)
			if test.body != "" {
				request.Header.Set("Content-Type", "application/json")
			}
			response := httptest.NewRecorder()

			handler.ServeHTTP(response, request)

			if response.Code != test.wantStatus {
				t.Fatalf(
					"status = %d, want %d; body=%s",
					response.Code,
					test.wantStatus,
					response.Body.String(),
				)
			}
			assertReviewResponseHeaders(t, response)
			var payload any
			if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
				t.Fatalf("response is not JSON: %v; body=%s", err, response.Body.String())
			}
			test.assertCall(t, store)
		})
	}
}

func TestReviewHandlerConflictIncludesLatestSafeDetail(t *testing.T) {
	latest := serviceTestDetail(31)
	latest.Submission = &eventing.ReviewSubmission{
		ID:            serviceTestSubmissionID,
		CaseID:        serviceTestCaseID,
		DraftVersion:  30,
		Marker:        "private-marker",
		Status:        eventing.ReviewSubmissionClaimed,
		LeaseToken:    "private-lease",
		Request:       json.RawMessage(`{"private":"request"}`),
		InternalError: "private-internal-error",
	}
	store := &reviewServiceStore{
		getResult: latest,
		updateErr: eventing.ErrReviewConflict,
	}
	handler := &Handler{Service: newReviewTestService(t, store, nil, nil)}
	request := httptest.NewRequest(
		http.MethodPatch,
		RuntimeRoutePrefix+"/"+serviceTestCaseID+"/findings/"+serviceTestFindingID,
		strings.NewReader(`{
			"expected_version":30,
			"finding":{
				"severity":"high",
				"title":"Updated",
				"message":"Updated message"
			}
		}`),
	)
	request.Header.Set("Content-Type", "application/json; charset=utf-8")
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	if response.Code != http.StatusConflict {
		t.Fatalf("status = %d, body=%s", response.Code, response.Body.String())
	}
	assertReviewResponseHeaders(t, response)
	if len(store.updateCalls) != 1 || len(store.getCalls) != 1 {
		t.Fatalf(
			"conflict calls: update=%d get-latest=%d",
			len(store.updateCalls),
			len(store.getCalls),
		)
	}
	var body struct {
		Error  string  `json:"error"`
		Detail *Detail `json:"detail"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode conflict response: %v", err)
	}
	if body.Error != "review changed; reload before retrying" ||
		body.Detail == nil ||
		body.Detail.Case.Version != 31 ||
		body.Detail.Submission == nil {
		t.Fatalf("conflict response = %#v", body)
	}
	for _, forbidden := range []string{
		"private-marker",
		"private-lease",
		`"private":"request"`,
		"private-internal-error",
	} {
		if strings.Contains(response.Body.String(), forbidden) {
			t.Fatalf("conflict detail exposed %q: %s", forbidden, response.Body.String())
		}
	}
}

func TestReviewHandlerMapsOperationErrorsWithoutLeakingDiagnostics(t *testing.T) {
	internal := errors.New("database password secret")
	tests := []struct {
		name        string
		err         error
		wantStatus  int
		wantMessage string
		wantLatest  bool
	}{
		{
			name:        "not found",
			err:         eventing.ErrNotFound,
			wantStatus:  http.StatusNotFound,
			wantMessage: "review not found",
		},
		{
			name:        "invalid review",
			err:         eventing.ErrInvalidReview,
			wantStatus:  http.StatusBadRequest,
			wantMessage: "invalid review request",
			wantLatest:  true,
		},
		{
			name:        "invalid transition",
			err:         eventing.ErrInvalidTransition,
			wantStatus:  http.StatusConflict,
			wantMessage: "review state does not allow this operation",
			wantLatest:  true,
		},
		{
			name:        "unavailable",
			err:         ErrUnavailable,
			wantStatus:  http.StatusServiceUnavailable,
			wantMessage: "review service unavailable",
			wantLatest:  true,
		},
		{
			name:        "deadline",
			err:         context.DeadlineExceeded,
			wantStatus:  http.StatusGatewayTimeout,
			wantMessage: "review operation timed out",
			wantLatest:  true,
		},
		{
			name:        "internal",
			err:         internal,
			wantStatus:  http.StatusInternalServerError,
			wantMessage: "review operation failed",
			wantLatest:  true,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store := &reviewServiceStore{
				getResult: serviceTestDetail(41),
				updateErr: test.err,
			}
			handler := &Handler{Service: newReviewTestService(t, store, nil, nil)}
			request := httptest.NewRequest(
				http.MethodPatch,
				RuntimeRoutePrefix+"/"+serviceTestCaseID+
					"/findings/"+serviceTestFindingID,
				strings.NewReader(`{
					"expected_version":40,
					"finding":{
						"severity":"medium",
						"title":"Updated",
						"message":"Updated message"
					}
				}`),
			)
			request.Header.Set("Content-Type", "application/json")
			response := httptest.NewRecorder()

			handler.ServeHTTP(response, request)

			if response.Code != test.wantStatus {
				t.Fatalf(
					"status = %d, want %d; body=%s",
					response.Code,
					test.wantStatus,
					response.Body.String(),
				)
			}
			var body struct {
				Error  string  `json:"error"`
				Detail *Detail `json:"detail"`
			}
			if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
				t.Fatalf("decode response: %v", err)
			}
			if body.Error != test.wantMessage {
				t.Fatalf("error message = %q, want %q", body.Error, test.wantMessage)
			}
			if (body.Detail != nil) != test.wantLatest {
				t.Fatalf("latest detail present = %v, want %v", body.Detail != nil, test.wantLatest)
			}
			wantGetCalls := 0
			if test.wantLatest {
				wantGetCalls = 1
			}
			if len(store.getCalls) != wantGetCalls {
				t.Fatalf("latest-detail calls = %d, want %d", len(store.getCalls), wantGetCalls)
			}
			if strings.Contains(response.Body.String(), internal.Error()) {
				t.Fatalf("response leaked internal diagnostic: %s", response.Body.String())
			}
		})
	}
}

func serviceTestDetail(version int64) eventing.ReviewCaseDetail {
	return eventing.ReviewCaseDetail{
		Case: eventing.ReviewCase{
			ID:             serviceTestCaseID,
			Connector:      "github-main",
			Repository:     "scylladb/gocql",
			PullNumber:     42,
			PullURL:        "https://github.com/scylladb/gocql/pull/42",
			BaseSHA:        strings.Repeat("a", 40),
			HeadSHA:        strings.Repeat("b", 40),
			Summary:        "One actionable finding.",
			Status:         eventing.ReviewCaseOpen,
			Version:        version,
			ActiveFindings: 1,
			TotalFindings:  1,
			CreatedAt:      serviceTestTime.Add(-time.Hour),
			UpdatedAt:      serviceTestTime,
		},
		Findings: []eventing.ReviewFinding{{
			ID:        serviceTestFindingID,
			CaseID:    serviceTestCaseID,
			Ordinal:   1,
			State:     eventing.ReviewFindingActive,
			Severity:  eventing.ReviewSeverityHigh,
			Title:     "Queued item can be lost",
			File:      "pkg/queue/worker.go",
			Message:   "Restore the item before returning.",
			Revision:  1,
			CreatedAt: serviceTestTime.Add(-time.Hour),
			UpdatedAt: serviceTestTime,
		}},
	}
}

func newReviewTestService(
	t *testing.T,
	store Store,
	agent workflows.AgentRunner,
	submitter Submitter,
) *Service {
	t.Helper()
	service, err := NewService(ServiceConfig{
		Store:     store,
		Agent:     agent,
		Submitter: submitter,
	})
	if err != nil {
		t.Fatalf("NewService() error = %v", err)
	}
	return service
}

func assertMessageAppend(
	t *testing.T,
	got eventing.ReviewMessageAppend,
	version int64,
	want eventing.ReviewMessageDraft,
) {
	t.Helper()
	if got.CaseID != serviceTestCaseID ||
		got.ExpectedVersion != version ||
		len(got.Messages) != 1 ||
		!reflect.DeepEqual(got.Messages[0], want) {
		t.Fatalf("message append = %#v, want version %d message %#v", got, version, want)
	}
}

func assertIsolatedReviewAgentRequest(t *testing.T, request workflows.AgentRequest) {
	t.Helper()
	if request.Tools != workflows.AgentToolsNone ||
		request.History != "none" ||
		request.Cache != "none" ||
		request.Session != "review:"+serviceTestCaseID+":finding:"+serviceTestFindingID {
		t.Fatalf("agent isolation/session = %#v", request)
	}
	managed, ok := request.Managed.(map[string]any)
	if !ok || !reflect.DeepEqual(managed, map[string]any{"mode": "off"}) {
		t.Fatalf("agent managed mode = %#v", request.Managed)
	}
}

func assertReviewResponseHeaders(t *testing.T, response *httptest.ResponseRecorder) {
	t.Helper()
	if got := response.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("Cache-Control = %q, want no-store", got)
	}
	if got := response.Header().Get("X-Content-Type-Options"); got != "nosniff" {
		t.Fatalf("X-Content-Type-Options = %q, want nosniff", got)
	}
	if got := response.Header().Get("Content-Type"); got != "application/json; charset=utf-8" {
		t.Fatalf("Content-Type = %q, want JSON UTF-8", got)
	}
}

func timePointer(value time.Time) *time.Time {
	return &value
}

type reviewServiceStore struct {
	getResult eventing.ReviewCaseDetail
	getErr    error
	getCalls  []string

	listResult eventing.ReviewCasePage
	listErr    error
	listCalls  []eventing.ReviewCaseFilter

	updateResult eventing.ReviewCaseDetail
	updateErr    error
	updateCalls  []eventing.ReviewFindingUpdate

	dropResult eventing.ReviewCaseDetail
	dropErr    error
	dropCalls  []eventing.ReviewFindingTransition

	restoreResult eventing.ReviewCaseDetail
	restoreErr    error
	restoreCalls  []eventing.ReviewFindingTransition

	appendResults []eventing.ReviewCaseDetail
	appendErr     error
	appendCalls   []eventing.ReviewMessageAppend

	createResult eventing.ReviewCaseDetail
	createErr    error
	createCalls  []eventing.ReviewSubmissionDraft

	reconcileResult eventing.ReviewCaseDetail
	reconcileErr    error
	reconcileCalls  []eventing.ReviewSubmissionReconciliation
}

func (store *reviewServiceStore) CaptureReview(
	context.Context,
	eventing.ReviewCaptureInput,
) (eventing.ReviewCase, bool, error) {
	return eventing.ReviewCase{}, false, nil
}

func (store *reviewServiceStore) GetReviewCase(
	_ context.Context,
	id string,
) (eventing.ReviewCaseDetail, error) {
	store.getCalls = append(store.getCalls, id)
	return store.getResult, store.getErr
}

func (store *reviewServiceStore) ListReviewCases(
	_ context.Context,
	filter eventing.ReviewCaseFilter,
) (eventing.ReviewCasePage, error) {
	store.listCalls = append(store.listCalls, filter)
	return store.listResult, store.listErr
}

func (store *reviewServiceStore) UpdateReviewFinding(
	_ context.Context,
	update eventing.ReviewFindingUpdate,
) (eventing.ReviewCaseDetail, error) {
	store.updateCalls = append(store.updateCalls, update)
	return store.updateResult, store.updateErr
}

func (store *reviewServiceStore) DropReviewFinding(
	_ context.Context,
	transition eventing.ReviewFindingTransition,
) (eventing.ReviewCaseDetail, error) {
	store.dropCalls = append(store.dropCalls, transition)
	return store.dropResult, store.dropErr
}

func (store *reviewServiceStore) RestoreReviewFinding(
	_ context.Context,
	transition eventing.ReviewFindingTransition,
) (eventing.ReviewCaseDetail, error) {
	store.restoreCalls = append(store.restoreCalls, transition)
	return store.restoreResult, store.restoreErr
}

func (store *reviewServiceStore) AppendReviewMessages(
	_ context.Context,
	appendRequest eventing.ReviewMessageAppend,
) (eventing.ReviewCaseDetail, error) {
	store.appendCalls = append(store.appendCalls, appendRequest)
	if store.appendErr != nil {
		return eventing.ReviewCaseDetail{}, store.appendErr
	}
	if len(store.appendResults) == 0 {
		return eventing.ReviewCaseDetail{}, nil
	}
	result := store.appendResults[0]
	store.appendResults = store.appendResults[1:]
	return result, nil
}

func (store *reviewServiceStore) CreateReviewSubmission(
	_ context.Context,
	draft eventing.ReviewSubmissionDraft,
) (eventing.ReviewCaseDetail, error) {
	draft.Request = append(json.RawMessage(nil), draft.Request...)
	store.createCalls = append(store.createCalls, draft)
	return store.createResult, store.createErr
}

func (store *reviewServiceStore) ReconcileReviewSubmission(
	_ context.Context,
	request eventing.ReviewSubmissionReconciliation,
) (eventing.ReviewCaseDetail, error) {
	store.reconcileCalls = append(store.reconcileCalls, request)
	return store.reconcileResult, store.reconcileErr
}

func (store *reviewServiceStore) GetReviewSubmission(
	context.Context,
	string,
) (eventing.ReviewSubmission, error) {
	return eventing.ReviewSubmission{}, eventing.ErrNotFound
}

func (store *reviewServiceStore) ClaimReviewSubmissions(
	context.Context,
	string,
	int,
	time.Duration,
) ([]eventing.ReviewSubmission, error) {
	return nil, nil
}

func (store *reviewServiceStore) RenewReviewSubmissionLease(
	context.Context,
	string,
	string,
	time.Duration,
) error {
	return nil
}

func (store *reviewServiceStore) FinishReviewSubmission(
	context.Context,
	eventing.ReviewSubmissionOutcome,
) (eventing.ReviewCaseDetail, error) {
	return eventing.ReviewCaseDetail{}, nil
}

type reviewAgentFake struct {
	requests []workflows.AgentRequest
	outputs  []map[string]any
	err      error
}

func (agent *reviewAgentFake) RunAgent(
	_ context.Context,
	request workflows.AgentRequest,
) (map[string]any, error) {
	agent.requests = append(agent.requests, request)
	if agent.err != nil {
		return nil, agent.err
	}
	if len(agent.outputs) == 0 {
		return nil, errors.New("unexpected review agent call")
	}
	output := agent.outputs[0]
	agent.outputs = agent.outputs[1:]
	return output, nil
}

type reviewSubmitterFake struct {
	requests []SubmitRequest
	result   SubmitResult
	err      error
}

func (submitter *reviewSubmitterFake) Submit(
	_ context.Context,
	request SubmitRequest,
) (SubmitResult, error) {
	submitter.requests = append(submitter.requests, request)
	return submitter.result, submitter.err
}
