//go:build !mipsle && !netbsd && !(freebsd && arm)

package reviews

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/sipeed/picoclaw/pkg/eventing"
	"github.com/sipeed/picoclaw/pkg/workflows"
)

func TestWorkingContextAcceptsRealSQLiteAggregateOrdinals(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, time.August, 3, 12, 0, 0, 0, time.UTC)
	store, err := eventing.Open(ctx, ":memory:", eventing.WithClock(func() time.Time {
		return now
	}))
	if err != nil {
		t.Fatalf("Open(event store): %v", err)
	}
	t.Cleanup(func() {
		if closeErr := store.Close(); closeErr != nil {
			t.Errorf("Close(event store): %v", closeErr)
		}
	})

	inserted, err := store.Insert(ctx, eventing.Envelope{
		Source:    "github",
		Connector: "github-primary",
		Type:      "pull_request.review_requested",
		DedupeKey: "delivery-review-working-context",
		Payload:   json.RawMessage(`{}`),
	})
	if err != nil {
		t.Fatalf("Insert(event): %v", err)
	}
	claims, err := store.ClaimRouting(ctx, "review-router", 1, time.Minute)
	if err != nil || len(claims) != 1 {
		t.Fatalf("ClaimRouting() = (%d claims, %v), want one", len(claims), err)
	}
	dispatch, created, err := store.CreateRevisionedDispatchForRoutingClaim(
		ctx,
		inserted.Event.Envelope.ID,
		claims[0].Routing.LeaseToken,
		"workflows/github-pr-review.yml",
		strings.Repeat("c", 64),
	)
	if err != nil || !created {
		t.Fatalf("CreateRevisionedDispatchForRoutingClaim() = (created=%v, %v)", created, err)
	}
	reviewCase, created, err := store.CaptureReview(ctx, eventing.ReviewCaptureInput{
		EventID:          inserted.Event.Envelope.ID,
		DispatchID:       dispatch.ID,
		RunID:            dispatch.RunID,
		WorkflowRef:      dispatch.WorkflowRef,
		WorkflowRevision: dispatch.WorkflowRevision,
		Connector:        inserted.Event.Envelope.Connector,
		Repository:       "acme/widgets",
		PullNumber:       42,
		PullURL:          "https://github.com/acme/widgets/pull/42",
		BaseSHA:          strings.Repeat("a", 40),
		HeadSHA:          strings.Repeat("b", 40),
		Draft: eventing.ReviewDraft{
			SchemaVersion: eventing.ReviewDraftSchemaVersion,
			Summary:       "One actionable finding.",
			Findings: []eventing.ReviewFindingDraft{{
				Severity: eventing.ReviewSeverityHigh,
				Title:    "Queued item can be lost",
				File:     "pkg/queue/worker.go",
				Message:  "Restore the item before returning.",
			}},
		},
	})
	if err != nil || !created {
		t.Fatalf("CaptureReview() = (created=%v, %v)", created, err)
	}
	detail, err := store.AppendReviewMessages(ctx, eventing.ReviewMessageAppend{
		CaseID:          reviewCase.ID,
		ExpectedVersion: reviewCase.Version,
		Messages: []eventing.ReviewMessageDraft{
			{
				Kind:    eventing.ReviewMessageChat,
				Role:    eventing.ReviewMessageUser,
				Content: "Is this retry safe?",
			},
			{
				Kind:    eventing.ReviewMessageChat,
				Role:    eventing.ReviewMessageAssistant,
				Content: "No; persistence must happen first.",
			},
		},
	})
	if err != nil {
		t.Fatalf("AppendReviewMessages(): %v", err)
	}
	if detail.Findings[0].Ordinal != 0 || detail.Messages[0].Ordinal != 0 ||
		detail.Messages[1].Ordinal != 1 {
		t.Fatalf(
			"SQLite ordinals = finding %d, messages %d/%d; want 0 and 0/1",
			detail.Findings[0].Ordinal,
			detail.Messages[0].Ordinal,
			detail.Messages[1].Ordinal,
		)
	}

	backend := newWorkingContextBackend(t)
	service := newWorkingContextService(t, store, backend)
	err = service.WithWorkingContext(ctx, WorkingContextRequest{
		CaseID: reviewCase.ID, AgentID: "main",
	}, func(callbackCtx context.Context, working WorkingContext) error {
		if working.CaseVersion != detail.Case.Version || working.SessionRevision == "" {
			return fmt.Errorf("working context = %#v", working)
		}
		snapshot, found, readErr := backend.ReadSessionSnapshot(
			callbackCtx,
			working.SessionKey,
		)
		if readErr != nil || !found || len(snapshot.History) != 2 ||
			snapshot.History[0].Content != "Is this retry safe?" {
			return fmt.Errorf(
				"projected SQLite history = (found=%v, history=%#v, err=%v)",
				found,
				snapshot.History,
				readErr,
			)
		}
		caseSubject := working.GateSubject["case"].(map[string]any)
		if caseSubject["version"].(fmt.Stringer).String() !=
			strconv.FormatInt(detail.Case.Version, 10) {
			return fmt.Errorf("gate case subject = %#v", caseSubject)
		}
		findings := working.GateSubject["findings"].([]any)
		messages := working.GateSubject["messages"].([]any)
		if findings[0].(map[string]any)["ordinal"].(fmt.Stringer).String() != "0" ||
			messages[0].(map[string]any)["ordinal"].(fmt.Stringer).String() != "0" ||
			messages[1].(map[string]any)["ordinal"].(fmt.Stringer).String() != "1" {
			return fmt.Errorf("gate ordinals = findings %#v, messages %#v", findings, messages)
		}
		_, compileErr := workflows.CompileGateWorkflow(
			"SQLite review context",
			[]workflows.GateSpec{{
				ID:       "discussion",
				Kind:     workflows.GateAIWorkingContext,
				AgentID:  "main",
				Criteria: "Ask when the discussion cannot resolve the finding.",
				Title:    "Resolve review finding",
			}},
			working.GateSubject,
		)
		return compileErr
	})
	if err != nil {
		t.Fatalf("WithWorkingContext(real SQLite): %v", err)
	}
}
