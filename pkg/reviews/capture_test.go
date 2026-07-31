package reviews

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/sipeed/picoclaw/pkg/eventing"
	"github.com/sipeed/picoclaw/pkg/workflows"
)

func TestCaptureSinkPersistsReservedDraftWithTrustedEventIdentity(t *testing.T) {
	store := &captureRecordingStore{}
	sink := &CaptureSink{Store: store}
	envelope, dispatch, run := captureTestValues()

	if err := sink.CaptureSucceededEventRun(
		context.Background(),
		envelope,
		dispatch,
		run,
	); err != nil {
		t.Fatalf("CaptureSucceededEventRun() error = %v", err)
	}
	if store.calls != 1 {
		t.Fatalf("capture calls = %d, want 1", store.calls)
	}
	input := store.input
	if input.EventID != envelope.ID ||
		input.DispatchID != dispatch.ID ||
		input.RunID != run.ID ||
		input.WorkflowRevision != dispatch.WorkflowRevision ||
		input.Connector != envelope.Connector ||
		input.Repository != "scylladb/gocql" ||
		input.PullNumber != 42 ||
		input.BaseSHA != strings.Repeat("a", 40) ||
		input.HeadSHA != strings.Repeat("b", 40) {
		t.Fatalf("capture input = %#v", input)
	}
	if input.Draft.SchemaVersion != eventing.ReviewDraftSchemaVersion ||
		len(input.Draft.Findings) != 1 ||
		input.Draft.Findings[0].Message != "Retry can lose the queued item." ||
		len(input.Draft.Tests) != 1 {
		t.Fatalf("capture draft = %#v", input.Draft)
	}
}

func TestCaptureSinkAcceptsAuthenticatedProviderNotificationIdentity(t *testing.T) {
	store := &captureRecordingStore{}
	sink := &CaptureSink{Store: store}
	envelope, dispatch, run := captureTestValues()
	envelope.Attributes["body_authenticated"] = "false"
	envelope.Attributes["provider_authenticated"] = "true"
	envelope.Attributes["notification_id"] = "12345"
	envelope.Attributes["notification_reason"] = "review_requested"

	if err := sink.CaptureSucceededEventRun(
		context.Background(),
		envelope,
		dispatch,
		run,
	); err != nil {
		t.Fatalf("CaptureSucceededEventRun() error = %v", err)
	}
	if store.calls != 1 {
		t.Fatalf("capture calls = %d, want 1", store.calls)
	}
}

func TestCaptureSinkIgnoresRunsWithoutReservedOutput(t *testing.T) {
	store := &captureRecordingStore{}
	sink := &CaptureSink{Store: store}
	_, dispatch, run := captureTestValues()
	run.Outputs = map[string]any{"ordinary": "output"}

	if err := sink.CaptureSucceededEventRun(
		context.Background(),
		eventing.Envelope{},
		dispatch,
		run,
	); err != nil {
		t.Fatalf("CaptureSucceededEventRun() error = %v", err)
	}
	if store.calls != 0 {
		t.Fatalf("capture calls = %d, want 0", store.calls)
	}
}

func TestCaptureSinkRejectsInvalidDraftAndUntrustedEventIdentity(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*eventing.Envelope, *eventing.Dispatch, *workflows.Run)
	}{
		{
			name: "unknown draft identity field",
			mutate: func(_ *eventing.Envelope, _ *eventing.Dispatch, run *workflows.Run) {
				run.Outputs[WorkflowDraftOutput].(map[string]any)["repository"] = "attacker/repo"
			},
		},
		{
			name: "unsupported schema",
			mutate: func(_ *eventing.Envelope, _ *eventing.Dispatch, run *workflows.Run) {
				run.Outputs[WorkflowDraftOutput].(map[string]any)["schemaVersion"] = 2
			},
		},
		{
			name: "unauthenticated body",
			mutate: func(envelope *eventing.Envelope, _ *eventing.Dispatch, _ *workflows.Run) {
				envelope.Attributes["body_authenticated"] = "false"
			},
		},
		{
			name: "generic provider marker without notification identity",
			mutate: func(envelope *eventing.Envelope, _ *eventing.Dispatch, _ *workflows.Run) {
				envelope.Attributes["body_authenticated"] = "false"
				envelope.Attributes["provider_authenticated"] = "true"
			},
		},
		{
			name: "authenticated source must target user",
			mutate: func(envelope *eventing.Envelope, _ *eventing.Dispatch, _ *workflows.Run) {
				envelope.Attributes["targets_user"] = "false"
			},
		},
		{
			name: "run mismatch",
			mutate: func(_ *eventing.Envelope, dispatch *eventing.Dispatch, _ *workflows.Run) {
				dispatch.RunID = "wr_other"
			},
		},
		{
			name: "invalid revision",
			mutate: func(envelope *eventing.Envelope, _ *eventing.Dispatch, _ *workflows.Run) {
				envelope.Attributes["pull_request_head_sha"] = "branch-name"
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			store := &captureRecordingStore{}
			sink := &CaptureSink{Store: store}
			envelope, dispatch, run := captureTestValues()
			test.mutate(&envelope, &dispatch, run)

			if err := sink.CaptureSucceededEventRun(
				context.Background(),
				envelope,
				dispatch,
				run,
			); err == nil {
				t.Fatal("CaptureSucceededEventRun() error = nil, want rejection")
			}
			if store.calls != 0 {
				t.Fatalf("capture calls = %d, want 0", store.calls)
			}
		})
	}
}

func TestCaptureSinkPropagatesDurableCaptureFailure(t *testing.T) {
	injected := errors.New("capture unavailable")
	store := &captureRecordingStore{err: injected}
	sink := &CaptureSink{Store: store}
	envelope, dispatch, run := captureTestValues()

	err := sink.CaptureSucceededEventRun(
		context.Background(),
		envelope,
		dispatch,
		run,
	)
	if !errors.Is(err, injected) {
		t.Fatalf("CaptureSucceededEventRun() error = %v, want injected", err)
	}
}

type captureRecordingStore struct {
	calls int
	input eventing.ReviewCaptureInput
	err   error
}

func (s *captureRecordingStore) CaptureReview(
	_ context.Context,
	input eventing.ReviewCaptureInput,
) (eventing.ReviewCase, bool, error) {
	s.calls++
	s.input = input
	return eventing.ReviewCase{}, s.err == nil, s.err
}

func captureTestValues() (
	eventing.Envelope,
	eventing.Dispatch,
	*workflows.Run,
) {
	const (
		eventID    = "ev_00112233445566778899aabbccddeeff"
		dispatchID = "dsp_00112233445566778899aabbccddeeff"
		runID      = "wr_review_capture"
		workflow   = "workflows/github-pr-review.yml"
	)
	envelope := eventing.Envelope{
		ID:        eventID,
		Source:    "github",
		Connector: "github-app",
		Type:      "pull_request.review_requested",
		Attributes: map[string]string{
			"body_authenticated":    "true",
			"source_authenticated":  "true",
			"targets_user":          "true",
			"repository_full_name":  "scylladb/gocql",
			"pull_request_number":   "42",
			"pull_request_url":      "https://github.com/scylladb/gocql/pull/42",
			"pull_request_base_sha": strings.Repeat("a", 40),
			"pull_request_head_sha": strings.Repeat("b", 40),
		},
	}
	dispatch := eventing.Dispatch{
		ID:               dispatchID,
		EventID:          eventID,
		RunID:            runID,
		WorkflowRef:      workflow,
		WorkflowRevision: strings.Repeat("c", 64),
	}
	run := &workflows.Run{
		ID:          runID,
		WorkflowRef: workflow,
		Status:      workflows.RunStatusSucceeded,
		Outputs: map[string]any{
			WorkflowDraftOutput: map[string]any{
				"schemaVersion": 1,
				"summary":       "One finding",
				"findings": []any{
					map[string]any{
						"severity":       "high",
						"title":          "Retry loses work",
						"file":           "queue.go",
						"line":           42,
						"message":        "Retry can lose the queued item.",
						"evidence":       "The item is removed before persistence.",
						"impact":         "A transient failure loses work.",
						"recommendation": "Persist before removing the item.",
					},
				},
				"tests":         []any{"go test ./..."},
				"residualRisks": []any{},
			},
		},
	}
	return envelope, dispatch, run
}
