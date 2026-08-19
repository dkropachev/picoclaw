package prworkspace

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestMessageHTTPAtomicallyRecordsCorrectionForSelectedPrompts(t *testing.T) {
	tests := []struct {
		name               string
		applicability      CorrectionApplicability
		wantApplicability  CorrectionApplicability
		wantReview         int
		wantImplementation int
	}{
		{
			name:               "both",
			applicability:      CorrectionReviewAndImpl,
			wantApplicability:  CorrectionReviewAndImpl,
			wantReview:         1,
			wantImplementation: 1,
		},
		{name: "default is both", wantApplicability: CorrectionReviewAndImpl, wantReview: 1, wantImplementation: 1},
		{
			name:              "review only",
			applicability:     CorrectionReviewOnly,
			wantApplicability: CorrectionReviewOnly,
			wantReview:        1,
		},
		{
			name:               "implementation only",
			applicability:      CorrectionImplementationOnly,
			wantApplicability:  CorrectionImplementationOnly,
			wantImplementation: 1,
		},
	}
	for index, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			service, before := messageCorrectionTestService(t, NewMemoryStore())
			handler, handlerErr := NewHTTPHandler(HTTPConfig{Service: service})
			if handlerErr != nil {
				t.Fatal(handlerErr)
			}
			body := map[string]any{
				"expected_version":   before.Workspace.Version,
				"request_id":         "request-message-correction-" + string(rune('a'+index)),
				"stage":              "workspace",
				"content":            "Keep the retry loop bounded to three attempts.",
				"mark_as_correction": true,
			}
			if test.applicability != "" {
				body["applicability"] = test.applicability
			}
			encoded, marshalErr := json.Marshal(body)
			if marshalErr != nil {
				t.Fatal(marshalErr)
			}
			request := httptest.NewRequest(
				http.MethodPost,
				RuntimeRoutePrefix+"/"+before.Workspace.ID+"/messages",
				bytes.NewReader(encoded),
			)
			request.Header.Set("Content-Type", "application/json")
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if response.Code != http.StatusOK {
				t.Fatalf("status = %d body=%s", response.Code, response.Body.String())
			}
			var result Aggregate
			if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
				t.Fatal(err)
			}
			assertMessageCorrectionResult(t, result, before.Workspace.Version, test.wantApplicability)

			persisted, getErr := service.Get(context.Background(), before.Workspace.ID)
			if getErr != nil {
				t.Fatal(getErr)
			}
			assertMessageCorrectionResult(t, persisted, before.Workspace.Version, test.wantApplicability)
			reviewContext := reviewContextBundle(persisted)
			implementationContext := implementationContextBundle(persisted)
			if len(reviewContext.Messages) != 1 || len(implementationContext.Messages) != 1 {
				t.Fatalf("shared message was not projected into both prompts: review=%d implementation=%d",
					len(reviewContext.Messages), len(implementationContext.Messages))
			}
			if got := len(reviewContext.Corrections); got != test.wantReview {
				t.Fatalf("review corrections = %d, want %d", got, test.wantReview)
			}
			if got := len(implementationContext.Corrections); got != test.wantImplementation {
				t.Fatalf("implementation corrections = %d, want %d", got, test.wantImplementation)
			}
		})
	}
}

func TestMessageHTTPRejectsInvalidCorrectionBeforeWritingMessage(t *testing.T) {
	service, before := messageCorrectionTestService(t, NewMemoryStore())
	handler, err := NewHTTPHandler(HTTPConfig{Service: service})
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(map[string]any{
		"expected_version":   before.Workspace.Version,
		"request_id":         "request-invalid-correction",
		"stage":              "workspace",
		"content":            "This must not be partially persisted.",
		"mark_as_correction": true,
		"applicability":      "somewhere_else",
	})
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(
		http.MethodPost,
		RuntimeRoutePrefix+"/"+before.Workspace.ID+"/messages",
		bytes.NewReader(encoded),
	)
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("status = %d body=%s", response.Code, response.Body.String())
	}
	persisted, err := service.Get(context.Background(), before.Workspace.ID)
	if err != nil {
		t.Fatal(err)
	}
	if persisted.Workspace.Version != before.Workspace.Version || len(persisted.Messages) != 0 ||
		len(persisted.Corrections) != 0 {
		t.Fatalf("invalid correction caused a partial write: version=%d messages=%d corrections=%d",
			persisted.Workspace.Version, len(persisted.Messages), len(persisted.Corrections))
	}
}

func messageCorrectionTestService(t *testing.T, store Store) (*Service, Aggregate) {
	t.Helper()
	input := testCreateInput()
	input.Provider.BaseSHA = "base-abcdef"
	input.Provider.HeadRepositoryID = input.Provider.RepositoryID
	input.Provider.HeadRepository = input.Provider.Repository
	created, err := store.Create(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	charter := Charter{
		ID: stableID("pcr_", input.Workspace.ID, "shared-guidance-charter"), Revision: 1,
		Type: PRTypeFix, Goal: "Fix retry behavior", HeadSHA: input.Provider.HeadSHA,
		BaseSHA: input.Provider.BaseSHA, AcceptanceCriteria: []string{"Retry limit is enforced"},
		Confirmed: true, CreatedAt: input.Workspace.CreatedAt, ConfirmedAt: &input.Workspace.CreatedAt,
	}
	seeded, err := store.Mutate(context.Background(), Mutation{
		WorkspaceID: input.Workspace.ID, ExpectedVersion: created.Aggregate.Workspace.Version,
		RequestID: "request-message-charter-seed",
		Patch: AggregatePatch{
			ActiveCharterID: &charter.ID,
			AppendCharters:  []Charter{charter},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 14, 12, 0, 0, 0, time.UTC)
	service, err := NewService(ServiceConfig{Store: store, Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	return service, seeded.Aggregate
}

func assertMessageCorrectionResult(
	t *testing.T,
	result Aggregate,
	previousVersion int64,
	applicability CorrectionApplicability,
) {
	t.Helper()
	if result.Workspace.Version != previousVersion+1 {
		t.Fatalf("version = %d, want one atomic increment from %d", result.Workspace.Version, previousVersion)
	}
	if len(result.Messages) != 1 || len(result.Corrections) != 1 {
		t.Fatalf("messages=%d corrections=%d", len(result.Messages), len(result.Corrections))
	}
	message, correction := result.Messages[0], result.Corrections[0]
	if correction.TargetType != "workspace" || correction.TargetID != result.Workspace.ID {
		t.Fatalf(
			"correction target = %q/%q, want workspace %q",
			correction.TargetType,
			correction.TargetID,
			result.Workspace.ID,
		)
	}
	if correction.Kind != CorrectionFactual || correction.Applicability != applicability ||
		correction.Correction != message.Content || correction.CharterID != message.CharterID || correction.HeadSHA != message.HeadSHA {
		t.Fatalf("correction does not mirror shared guidance: %#v message=%#v", correction, message)
	}
	if len(result.Activity) != 2 || result.Activity[0].Kind != "message.added" ||
		result.Activity[1].Kind != "correction.added" {
		t.Fatalf("activity = %#v", result.Activity)
	}
}
