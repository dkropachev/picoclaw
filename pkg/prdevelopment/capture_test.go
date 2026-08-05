package prdevelopment

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sipeed/picoclaw/pkg/eventing"
	"github.com/sipeed/picoclaw/pkg/workflows"
)

const (
	testBaseSHA   = "1111111111111111111111111111111111111111"
	testEventHead = "2222222222222222222222222222222222222222"
	testHeadSHA   = "3333333333333333333333333333333333333333"
	testReviewSHA = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
)

func TestCaptureSinkPersistsProviderVerifiedFeedbackAndReconcilesBeforeReread(
	t *testing.T,
) {
	t.Parallel()
	runner := &captureToolRunner{responses: []string{
		providerPullJSON("OPEN", testHeadSHA),
		providerReviewsJSON(providerReviewValue("CHANGES_REQUESTED", "Fix the race.")),
	}}
	store := &captureStore{}
	sink := &CaptureSink{
		Store:    store,
		Verifier: &GitHubVerifier{Runner: runner},
	}
	event, dispatch, run := validCaptureOccurrence()

	if err := sink.CaptureSucceededEventRun(
		context.Background(),
		event,
		dispatch,
		run,
	); err != nil {
		t.Fatalf("CaptureSucceededEventRun() error = %v", err)
	}
	if len(store.inputs) != 1 {
		t.Fatalf("capture inputs = %d, want 1", len(store.inputs))
	}
	input := store.inputs[0]
	if input.Repository != "ScyllaDB/PicoClaw" ||
		input.PullNumber != 42 ||
		input.PullAuthor != "Review-User" ||
		input.TargetUser != "review-user" ||
		input.PullState != "open" ||
		input.BaseSHA != testBaseSHA ||
		input.HeadSHA != testHeadSHA ||
		input.ReviewID != "701" ||
		input.TriggerReviewNodeID != "PRR_kwDOReview701" ||
		input.SubmittedReviewState != "changes_requested" ||
		input.CurrentReviewState != "changes_requested" ||
		input.Feedback != "Fix the race." {
		t.Fatalf("capture input = %#v", input)
	}
	if len(runner.requests) != 2 {
		t.Fatalf("provider requests = %d, want 2", len(runner.requests))
	}
	assertReadRequest(t, runner.requests[0], "get", 0)
	assertReadRequest(t, runner.requests[1], "get_reviews", 1)

	store.lookupExists = true
	runner.responses = nil
	if err := sink.CaptureSucceededEventRun(
		context.Background(),
		event,
		dispatch,
		run,
	); err != nil {
		t.Fatalf("reconciled CaptureSucceededEventRun() error = %v", err)
	}
	if len(runner.requests) != 2 || len(store.inputs) != 1 {
		t.Fatalf(
			"reconciliation performed effects: requests=%d captures=%d",
			len(runner.requests),
			len(store.inputs),
		)
	}
}

func TestCaptureSinkAcceptsDismissedCurrentReviewAndAdvancedHead(t *testing.T) {
	t.Parallel()
	runner := &captureToolRunner{responses: []string{
		providerPullJSON("CLOSED", testHeadSHA),
		providerReviewsJSON(providerReviewValue("DISMISSED", "No longer active.")),
	}}
	store := &captureStore{}
	event, dispatch, run := validCaptureOccurrence()
	if err := (&CaptureSink{
		Store:    store,
		Verifier: &GitHubVerifier{Runner: runner},
	}).CaptureSucceededEventRun(context.Background(), event, dispatch, run); err != nil {
		t.Fatalf("CaptureSucceededEventRun() error = %v", err)
	}
	input := store.inputs[0]
	if input.CurrentReviewState != "dismissed" || input.HeadSHA != testHeadSHA ||
		input.HeadSHA == event.Attributes["pull_request_head_sha"] {
		t.Fatalf("capture input = %#v", input)
	}
}

func TestCaptureSinkRequiresExactMarkerAndAuthenticatedRouting(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		mutate func(*eventing.Envelope, *workflows.Run)
		wantOK bool
	}{
		{
			name: "marker absent is ignored",
			mutate: func(_ *eventing.Envelope, run *workflows.Run) {
				delete(run.Outputs, WorkflowCaptureOutput)
			},
			wantOK: true,
		},
		{
			name: "marker version",
			mutate: func(_ *eventing.Envelope, run *workflows.Run) {
				run.Outputs[WorkflowCaptureOutput] = "v2"
			},
		},
		{
			name: "body authentication",
			mutate: func(event *eventing.Envelope, _ *workflows.Run) {
				event.Attributes["body_authenticated"] = "false"
			},
		},
		{
			name: "exact review feedback membership",
			mutate: func(event *eventing.Envelope, _ *workflows.Run) {
				event.Attributes["target_reason"] = "mention,review_feedback_extra"
			},
		},
		{
			name: "review ID outside provider range",
			mutate: func(event *eventing.Envelope, _ *workflows.Run) {
				event.Attributes["review_id"] = "9223372036854775808"
			},
		},
		{
			name: "installed workflow ref",
			mutate: func(_ *eventing.Envelope, run *workflows.Run) {
				run.WorkflowRef = "workflows/copied.yml"
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			event, dispatch, run := validCaptureOccurrence()
			test.mutate(&event, run)
			if run.WorkflowRef != dispatch.WorkflowRef && test.name != "installed workflow ref" {
				dispatch.WorkflowRef = run.WorkflowRef
			}
			store := &captureStore{}
			runner := &captureToolRunner{}
			err := (&CaptureSink{
				Store:    store,
				Verifier: &GitHubVerifier{Runner: runner},
			}).CaptureSucceededEventRun(context.Background(), event, dispatch, run)
			if test.wantOK && err != nil {
				t.Fatalf("CaptureSucceededEventRun() error = %v", err)
			}
			if !test.wantOK && err == nil {
				t.Fatal("CaptureSucceededEventRun() succeeded")
			}
			if len(runner.requests) != 0 || len(store.inputs) != 0 {
				t.Fatalf("invalid admission performed effects: %#v %#v", runner.requests, store.inputs)
			}
		})
	}
}

func TestGitHubVerifierPaginatesAndRejectsProviderMismatch(t *testing.T) {
	t.Parallel()
	firstPage := make([]map[string]any, providerReviewsPerPage)
	for index := range firstPage {
		firstPage[index] = providerReviewValue("COMMENTED", "other")
		firstPage[index]["id"] = index + 1
	}
	firstJSON, err := json.Marshal(firstPage)
	if err != nil {
		t.Fatal(err)
	}
	runner := &captureToolRunner{responses: []string{
		providerPullJSON("OPEN", testHeadSHA),
		string(firstJSON),
		providerReviewsJSON(providerReviewValue("CHANGES_REQUESTED", "target")),
	}}
	evidence := validRoutingEvidence()
	verified, err := (&GitHubVerifier{Runner: runner}).Verify(context.Background(), evidence)
	if err != nil {
		t.Fatalf("Verify() error = %v", err)
	}
	if verified.Feedback != "target" || len(runner.requests) != 3 {
		t.Fatalf("Verify() = %#v, requests=%d", verified, len(runner.requests))
	}
	assertReadRequest(t, runner.requests[2], "get_reviews", 2)

	mismatch := providerReviewValue("CHANGES_REQUESTED", "target")
	mismatch["commit_id"] = strings.Repeat("b", 40)
	runner = &captureToolRunner{responses: []string{
		providerPullJSON("OPEN", testHeadSHA),
		providerReviewsJSON(mismatch),
	}}
	if _, err := (&GitHubVerifier{Runner: runner}).Verify(
		context.Background(),
		evidence,
	); err == nil || !strings.Contains(err.Error(), "commit") {
		t.Fatalf("Verify() error = %v, want commit mismatch", err)
	}
}

func TestGitHubVerifierRejectsDuplicateReviewAcrossPages(t *testing.T) {
	t.Parallel()
	firstPage := make([]map[string]any, providerReviewsPerPage)
	firstPage[0] = providerReviewValue("CHANGES_REQUESTED", "first")
	for index := 1; index < len(firstPage); index++ {
		firstPage[index] = providerReviewValue("COMMENTED", "other")
		firstPage[index]["id"] = index
	}
	firstJSON, err := json.Marshal(firstPage)
	if err != nil {
		t.Fatal(err)
	}
	runner := &captureToolRunner{responses: []string{
		providerPullJSON("OPEN", testHeadSHA),
		string(firstJSON),
		providerReviewsJSON(providerReviewValue("CHANGES_REQUESTED", "second")),
	}}
	if _, err := (&GitHubVerifier{Runner: runner}).Verify(
		context.Background(),
		validRoutingEvidence(),
	); err == nil || !strings.Contains(err.Error(), "more than once") {
		t.Fatalf("Verify() error = %v, want duplicate review failure", err)
	}
}

func TestGitHubVerifierRejectsReviewScanOverflow(t *testing.T) {
	t.Parallel()
	fullPage := make([]map[string]any, providerReviewsPerPage)
	for index := range fullPage {
		fullPage[index] = providerReviewValue("COMMENTED", "other")
		fullPage[index]["id"] = index + 1
	}
	fullJSON, err := json.Marshal(fullPage)
	if err != nil {
		t.Fatal(err)
	}
	responses := make([]string, 0, maxProviderReviewPages+2)
	responses = append(responses, providerPullJSON("OPEN", testHeadSHA))
	for range maxProviderReviewPages {
		responses = append(responses, string(fullJSON))
	}
	responses = append(
		responses,
		providerReviewsJSON(providerReviewValue("CHANGES_REQUESTED", "overflow")),
	)
	runner := &captureToolRunner{responses: responses}
	if _, err := (&GitHubVerifier{Runner: runner}).Verify(
		context.Background(),
		validRoutingEvidence(),
	); err == nil || !strings.Contains(err.Error(), "exceeds five complete pages") {
		t.Fatalf("Verify() error = %v, want bounded scan overflow", err)
	}
	if len(runner.requests) != maxProviderReviewPages+2 {
		t.Fatalf("provider requests = %d, want pull, five pages, and one probe", len(runner.requests))
	}
	probe := runner.requests[len(runner.requests)-1]
	assertReadRequest(t, probe, "get_reviews", providerOverflowProbePage)
	if probe.Args["perPage"] != 1 {
		t.Fatalf("overflow probe perPage = %#v, want 1", probe.Args["perPage"])
	}
}

func TestGitHubVerifierRequiresExplicitPullBooleanState(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name   string
		field  string
		value  any
		delete bool
	}{
		{name: "draft omitted", field: "draft", delete: true},
		{name: "draft null", field: "draft", value: nil},
		{name: "merged omitted", field: "merged", delete: true},
		{name: "merged null", field: "merged", value: nil},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			var pull map[string]any
			if err := json.Unmarshal([]byte(providerPullJSON("OPEN", testHeadSHA)), &pull); err != nil {
				t.Fatal(err)
			}
			if test.delete {
				delete(pull, test.field)
			} else {
				pull[test.field] = test.value
			}
			raw, err := json.Marshal(pull)
			if err != nil {
				t.Fatal(err)
			}
			runner := &captureToolRunner{responses: []string{string(raw)}}
			if _, err := (&GitHubVerifier{Runner: runner}).Verify(
				context.Background(),
				validRoutingEvidence(),
			); err == nil || !strings.Contains(err.Error(), "incomplete") {
				t.Fatalf("Verify() error = %v, want incomplete boolean state", err)
			}
			if len(runner.requests) != 1 {
				t.Fatalf("provider requests = %d, want one pull read", len(runner.requests))
			}
		})
	}
}

func TestRemainingProviderReadLimitEnforcesAggregateBudget(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name       string
		totalBytes int
		want       int
		wantError  bool
	}{
		{name: "per-result cap", totalBytes: 0, want: maxProviderJSONBytes},
		{
			name:       "remaining aggregate budget",
			totalBytes: maxProviderTotalBytes - 123,
			want:       123,
		},
		{name: "aggregate exhausted", totalBytes: maxProviderTotalBytes, wantError: true},
		{name: "invalid negative total", totalBytes: -1, wantError: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			got, err := remainingProviderReadLimit(test.totalBytes)
			if test.wantError {
				if err == nil {
					t.Fatalf("remainingProviderReadLimit() = %d, want error", got)
				}
				return
			}
			if err != nil || got != test.want {
				t.Fatalf("remainingProviderReadLimit() = %d, %v, want %d", got, err, test.want)
			}
		})
	}
}

func TestGitHubVerifierPreservesMaximumValidFeedback(t *testing.T) {
	t.Parallel()
	body := strings.Repeat("x", maxProviderReviewBody-2) + "\x00y"
	runner := &captureToolRunner{responses: []string{
		providerPullJSON("OPEN", testHeadSHA),
		providerReviewsJSON(providerReviewValue("CHANGES_REQUESTED", body)),
	}}
	verified, err := (&GitHubVerifier{Runner: runner}).Verify(
		context.Background(),
		validRoutingEvidence(),
	)
	if err != nil {
		t.Fatalf("Verify() error = %v", err)
	}
	if verified.Feedback != body {
		t.Fatalf("feedback length = %d, want exact %d-byte body", len(verified.Feedback), len(body))
	}
}

func TestGitHubVerifierRejectsDuplicateJSONAndConfinesArtifacts(t *testing.T) {
	t.Parallel()
	if err := decodeProviderJSON(
		[]byte(`{"number":42,"number":43}`),
		&providerPullRequest{},
	); err == nil {
		t.Fatal("decodeProviderJSON() accepted duplicate object names")
	}

	root := t.TempDir()
	artifact := filepath.Join(root, "result.json")
	if err := os.WriteFile(artifact, []byte(`{"ok":true}`), 0o600); err != nil {
		t.Fatal(err)
	}
	verifier := &GitHubVerifier{ArtifactRoot: root}
	raw, err := verifier.exactJSON(
		map[string]any{
			"text":          "stored as artifact",
			"artifact_tags": []string{"[file:" + artifact + "]"},
		},
		1024,
	)
	if err != nil || string(raw) != `{"ok":true}` {
		t.Fatalf("exactJSON() = %q, %v", raw, err)
	}
	if _, err := os.Stat(artifact); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("consumed artifact remains: %v", err)
	}

	outside := filepath.Join(t.TempDir(), "outside.json")
	if err := os.WriteFile(outside, []byte(`[]`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := verifier.exactJSON(
		map[string]any{
			"text":          "stored as artifact",
			"artifact_tags": []string{"[file:" + outside + "]"},
		},
		1024,
	); err == nil {
		t.Fatal("exactJSON() accepted an out-of-root artifact")
	}
}

type captureStore struct {
	lookupExists bool
	inputs       []eventing.PRDevelopmentCaptureInput
}

func (s *captureStore) LookupPRDevelopmentCapture(
	_ context.Context,
	_ eventing.PRDevelopmentCaptureIdentity,
) (eventing.PRDevelopmentCase, bool, error) {
	return eventing.PRDevelopmentCase{}, s.lookupExists, nil
}

func (s *captureStore) CapturePRDevelopmentCase(
	_ context.Context,
	input eventing.PRDevelopmentCaptureInput,
) (eventing.PRDevelopmentCase, bool, error) {
	s.inputs = append(s.inputs, input)
	return eventing.PRDevelopmentCase{}, true, nil
}

type captureToolRunner struct {
	requests  []workflows.ToolRequest
	responses []string
}

func (r *captureToolRunner) RunTool(
	_ context.Context,
	request workflows.ToolRequest,
) (map[string]any, error) {
	r.requests = append(r.requests, request)
	if len(r.responses) == 0 {
		return nil, errors.New("unexpected provider call")
	}
	response := r.responses[0]
	r.responses = r.responses[1:]
	return map[string]any{"text": response, "artifact_tags": []string{}}, nil
}

func validCaptureOccurrence() (eventing.Envelope, eventing.Dispatch, *workflows.Run) {
	evidence := validRoutingEvidence()
	event := eventing.Envelope{
		ID:        "evt-own-pr-feedback",
		Source:    "github",
		Connector: "github-main",
		Type:      "pull_request_review.submitted",
		Attributes: map[string]string{
			"source_authenticated":          "true",
			"body_authenticated":            "true",
			"provider_authenticated":        "false",
			"targets_user":                  "true",
			"target_reason":                 "review_feedback,mention",
			"pull_request_author_is_target": "true",
			"review_author_is_target":       "false",
			"repository_full_name":          evidence.Repository,
			"pull_request_number":           "42",
			"pull_request_url":              evidence.PullURL,
			"pull_request_author":           evidence.PullAuthor,
			"pull_request_head_sha":         testEventHead,
			"target_user":                   evidence.TargetUser,
			"review_id":                     evidence.ReviewID,
			"review_node_id":                evidence.ReviewNodeID,
			"review_url":                    evidence.ReviewURL,
			"review_author":                 evidence.ReviewAuthor,
			"review_state":                  evidence.ReviewState,
			"review_commit_sha":             evidence.ReviewCommitSHA,
			"review_submitted_at":           evidence.ReviewSubmittedAt.Format(time.RFC3339Nano),
		},
	}
	dispatch := eventing.Dispatch{
		ID:               "dsp-own-pr-feedback",
		EventID:          event.ID,
		WorkflowRef:      workflows.GitHubPRDevelopmentWorkflowRef,
		WorkflowRevision: strings.Repeat("b", 64),
		RunID:            "run-own-pr-feedback",
	}
	run := &workflows.Run{
		ID:          dispatch.RunID,
		WorkflowRef: dispatch.WorkflowRef,
		Status:      workflows.RunStatusSucceeded,
		Outputs: map[string]any{
			WorkflowCaptureOutput: WorkflowCaptureVersion,
		},
	}
	return event, dispatch, run
}

func validRoutingEvidence() RoutingEvidence {
	return RoutingEvidence{
		Repository:        "ScyllaDB/PicoClaw",
		PullNumber:        42,
		PullURL:           "https://github.com/ScyllaDB/PicoClaw/pull/42",
		PullAuthor:        "Review-User",
		TargetUser:        "review-user",
		ReviewID:          "701",
		ReviewNodeID:      "PRR_kwDOReview701",
		ReviewURL:         "https://github.com/ScyllaDB/PicoClaw/pull/42#pullrequestreview-701",
		ReviewAuthor:      "independent-reviewer",
		ReviewState:       "changes_requested",
		ReviewCommitSHA:   testReviewSHA,
		ReviewSubmittedAt: time.Date(2026, 8, 5, 14, 30, 0, 0, time.UTC),
	}
}

func providerPullJSON(state, headSHA string) string {
	value := map[string]any{
		"number":   42,
		"state":    state,
		"draft":    false,
		"merged":   state == "CLOSED",
		"html_url": "https://github.com/ScyllaDB/PicoClaw/pull/42",
		"user":     map[string]any{"login": "Review-User"},
		"head": map[string]any{
			"ref":  "feat/fix-race",
			"sha":  headSHA,
			"repo": map[string]any{"full_name": "contributor/PicoClaw"},
		},
		"base": map[string]any{
			"ref":  "main",
			"sha":  testBaseSHA,
			"repo": map[string]any{"full_name": "ScyllaDB/PicoClaw"},
		},
	}
	raw, _ := json.Marshal(value)
	return string(raw)
}

func providerReviewValue(state, body string) map[string]any {
	return map[string]any{
		"id":           701,
		"state":        state,
		"body":         body,
		"html_url":     "https://github.com/ScyllaDB/PicoClaw/pull/42#pullrequestreview-701",
		"user":         map[string]any{"login": "Independent-Reviewer"},
		"commit_id":    testReviewSHA,
		"submitted_at": "2026-08-05T14:30:00Z",
	}
}

func providerReviewsJSON(values ...map[string]any) string {
	raw, _ := json.Marshal(values)
	return string(raw)
}

func assertReadRequest(
	t *testing.T,
	request workflows.ToolRequest,
	method string,
	page int,
) {
	t.Helper()
	if !request.MCP || request.MCPServer != "github" ||
		request.MCPTool != "pull_request_read" || request.Args["method"] != method {
		t.Fatalf("provider request = %#v", request)
	}
	if page > 0 && request.Args["page"] != page {
		t.Fatalf("provider request page = %#v, want %d", request.Args["page"], page)
	}
}
