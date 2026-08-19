package reviews

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	picomcp "github.com/sipeed/picoclaw/pkg/mcp"
	"github.com/sipeed/picoclaw/pkg/workflows"
)

func TestGitHubSubmitterUsesPendingReviewProtocolInOrder(t *testing.T) {
	line := 27
	request := validSubmitRequest()
	request.Findings = []SubmitFinding{
		{
			ID:      "prf_line",
			Title:   "Queue item can be lost",
			File:    "pkg/queue/worker.go",
			Line:    &line,
			Message: "Restore the item before returning from this error path.",
		},
		{
			ID:      "prf_file",
			Title:   "File-wide invariant is undocumented",
			File:    "pkg/queue/invariant.go",
			Message: "Document and enforce the single-owner invariant.",
		},
		{
			ID:      "prf_body",
			Title:   "Migration risk",
			Message: "The rollout plan does not cover existing queued data.",
		},
	}
	runner := &submitRecordingRunner{}
	submitter := &GitHubSubmitter{Runner: runner}

	result, err := submitter.Submit(context.Background(), request)
	if err != nil {
		t.Fatalf("Submit() error = %v", err)
	}
	if result.InlineComments != 2 || result.BodyFindings != 1 {
		t.Fatalf("Submit() result = %#v", result)
	}
	if len(runner.requests) != 5 {
		t.Fatalf("tool calls = %d, want 5", len(runner.requests))
	}

	assertSubmitToolIdentity(
		t,
		runner.requests[0],
		DefaultGitHubMCPServer,
		GitHubPullRequestReadTool,
	)
	if got, want := runner.requests[0].Args, map[string]any{
		"method":     "get",
		"owner":      "scylladb",
		"repo":       "gocql",
		"pullNumber": int64(42),
	}; !reflect.DeepEqual(got, want) {
		t.Fatalf("read args = %#v, want %#v", got, want)
	}

	assertSubmitToolIdentity(
		t,
		runner.requests[1],
		DefaultGitHubMCPServer,
		GitHubPullRequestReviewWriteTool,
	)
	if got, want := runner.requests[1].Args, map[string]any{
		"method":     "create",
		"owner":      "scylladb",
		"repo":       "gocql",
		"pullNumber": int64(42),
		"commitID":   strings.Repeat("a", 40),
		"body":       submitReviewRecoveryBody(request),
	}; !reflect.DeepEqual(got, want) {
		t.Fatalf("create args = %#v, want %#v", got, want)
	}
	if _, exists := runner.requests[1].Args["event"]; exists {
		t.Fatal("create args unexpectedly contain event")
	}
	createBody, ok := runner.requests[1].Args["body"].(string)
	if !ok || strings.Count(createBody, request.Marker) != 1 ||
		!strings.Contains(createBody, request.Findings[0].Message) ||
		!strings.Contains(createBody, request.Findings[1].Message) ||
		!strings.Contains(createBody, request.Findings[2].Message) {
		t.Fatalf("create body is not marker-bearing complete recovery evidence: %q", createBody)
	}

	assertSubmitToolIdentity(
		t,
		runner.requests[2],
		DefaultGitHubMCPServer,
		GitHubPendingReviewCommentTool,
	)
	if got, want := runner.requests[2].Args, map[string]any{
		"owner":       "scylladb",
		"repo":        "gocql",
		"pullNumber":  int64(42),
		"path":        "pkg/queue/worker.go",
		"body":        "Restore the item before returning from this error path.",
		"subjectType": "LINE",
		"line":        27,
		"side":        "RIGHT",
	}; !reflect.DeepEqual(got, want) {
		t.Fatalf("line comment args = %#v, want %#v", got, want)
	}

	assertSubmitToolIdentity(
		t,
		runner.requests[3],
		DefaultGitHubMCPServer,
		GitHubPendingReviewCommentTool,
	)
	if got, want := runner.requests[3].Args, map[string]any{
		"owner":       "scylladb",
		"repo":        "gocql",
		"pullNumber":  int64(42),
		"path":        "pkg/queue/invariant.go",
		"body":        "Document and enforce the single-owner invariant.",
		"subjectType": "FILE",
	}; !reflect.DeepEqual(got, want) {
		t.Fatalf("file comment args = %#v, want %#v", got, want)
	}

	assertSubmitToolIdentity(
		t,
		runner.requests[4],
		DefaultGitHubMCPServer,
		GitHubPullRequestReviewWriteTool,
	)
	submitArgs := runner.requests[4].Args
	if submitArgs["method"] != "submit_pending" ||
		submitArgs["event"] != "COMMENT" ||
		submitArgs["owner"] != "scylladb" ||
		submitArgs["repo"] != "gocql" ||
		submitArgs["pullNumber"] != int64(42) {
		t.Fatalf("submit args = %#v", submitArgs)
	}
	body, ok := submitArgs["body"].(string)
	if !ok ||
		!strings.Contains(body, request.Summary) ||
		!strings.Contains(body, "#### Migration risk") ||
		!strings.Contains(body, request.Findings[2].Message) ||
		!strings.HasSuffix(body, request.Marker) {
		t.Fatalf("submit body = %q", body)
	}
	if strings.Contains(body, request.Findings[0].Message) ||
		strings.Contains(body, request.Findings[1].Message) {
		t.Fatalf("submit body duplicates inline finding: %q", body)
	}
}

func TestGitHubSubmitterIncludesBodyOnlyFindingsWithoutCommentCalls(t *testing.T) {
	request := validSubmitRequest()
	request.Findings = []SubmitFinding{{
		ID:      "prf_body",
		Title:   "Release sequencing",
		Message: "Deploy the reader before enabling the new writer.",
	}}
	runner := &submitRecordingRunner{}

	result, err := (&GitHubSubmitter{Runner: runner}).Submit(
		context.Background(),
		request,
	)
	if err != nil {
		t.Fatalf("Submit() error = %v", err)
	}
	if len(runner.requests) != 3 {
		t.Fatalf("tool calls = %d, want read + create + submit", len(runner.requests))
	}
	if result.InlineComments != 0 || result.BodyFindings != 1 {
		t.Fatalf("Submit() result = %#v", result)
	}
	body := runner.requests[2].Args["body"].(string)
	for _, expected := range []string{
		request.Summary,
		"### Additional review findings",
		request.Findings[0].Title,
		request.Findings[0].Message,
		request.Marker,
	} {
		if !strings.Contains(body, expected) {
			t.Fatalf("submit body %q does not contain %q", body, expected)
		}
	}
}

func TestGitHubSubmitterRejectsInvalidInputBeforeCallingTools(t *testing.T) {
	line := 3
	for _, test := range []struct {
		name   string
		mutate func(*SubmitRequest)
	}{
		{
			name: "owner",
			mutate: func(request *SubmitRequest) {
				request.Owner = "bad/owner"
			},
		},
		{
			name: "pull number",
			mutate: func(request *SubmitRequest) {
				request.PullNumber = 0
			},
		},
		{
			name: "head SHA",
			mutate: func(request *SubmitRequest) {
				request.HeadSHA = "main"
			},
		},
		{
			name: "no active findings",
			mutate: func(request *SubmitRequest) {
				request.Findings = nil
			},
		},
		{
			name: "line without file",
			mutate: func(request *SubmitRequest) {
				request.Findings[0].Line = &line
			},
		},
		{
			name: "file traversal",
			mutate: func(request *SubmitRequest) {
				request.Findings[0].File = "../secret"
			},
		},
		{
			name: "blank message",
			mutate: func(request *SubmitRequest) {
				request.Findings[0].Message = " "
			},
		},
		{
			name: "aggregate body",
			mutate: func(request *SubmitRequest) {
				request.Summary = strings.Repeat("s", maxSubmitReviewBodyBytes)
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			request := validSubmitRequest()
			test.mutate(&request)
			runner := &submitRecordingRunner{}

			_, err := (&GitHubSubmitter{Runner: runner}).Submit(
				context.Background(),
				request,
			)
			if err == nil {
				t.Fatal("Submit() error = nil, want validation error")
			}
			var stageErr *SubmitStageError
			if !errors.As(err, &stageErr) {
				t.Fatalf("Submit() error type = %T, want *SubmitStageError", err)
			}
			if stageErr.Stage != SubmitStageValidate ||
				stageErr.FindingIndex != -1 ||
				stageErr.ExternalStateMayHaveChanged {
				t.Fatalf("stage error = %#v", stageErr)
			}
			if !errors.Is(err, ErrInvalidSubmitRequest) {
				t.Fatalf("Submit() error = %v, want ErrInvalidSubmitRequest", err)
			}
			if len(runner.requests) != 0 {
				t.Fatalf("tool calls = %d, want 0", len(runner.requests))
			}
		})
	}
}

func TestGitHubSubmitterVerifiesHeadBeforeAnyWrite(t *testing.T) {
	request := validSubmitRequest()
	current := strings.Repeat("b", 40)
	runner := &submitRecordingRunner{headSHA: current}

	_, err := (&GitHubSubmitter{Runner: runner}).Submit(
		context.Background(),
		request,
	)
	if err == nil {
		t.Fatal("Submit() error = nil, want stale head")
	}
	var changed *PullRequestHeadChangedError
	if !errors.As(err, &changed) ||
		changed.Expected != request.HeadSHA ||
		changed.Actual != current {
		t.Fatalf("Submit() error = %#v, want typed head change", err)
	}
	var stageErr *SubmitStageError
	if !errors.As(err, &stageErr) ||
		stageErr.Stage != SubmitStageVerifyHead ||
		stageErr.ExternalStateMayHaveChanged ||
		stageErr.CompletedCalls != 0 {
		t.Fatalf("Submit() stage error = %#v", stageErr)
	}
	if len(runner.requests) != 1 ||
		runner.requests[0].MCPTool != GitHubPullRequestReadTool {
		t.Fatalf("tool calls = %#v, want one pull-request read", runner.requests)
	}
}

func TestGitHubSubmitterReadFailureIsDefinitePreWriteFailure(t *testing.T) {
	readErr := errors.New("GitHub read failed")
	runner := &submitRecordingRunner{failCall: 1, err: readErr}

	_, err := (&GitHubSubmitter{Runner: runner}).Submit(
		context.Background(),
		validSubmitRequest(),
	)
	if !errors.Is(err, readErr) {
		t.Fatalf("Submit() error = %v, want wrapped read failure", err)
	}
	var stageErr *SubmitStageError
	if !errors.As(err, &stageErr) ||
		stageErr.Stage != SubmitStageVerifyHead ||
		stageErr.ExternalStateMayHaveChanged ||
		stageErr.CompletedCalls != 0 {
		t.Fatalf("Submit() stage error = %#v", stageErr)
	}
	if len(runner.requests) != 1 {
		t.Fatalf("tool calls = %d, want read only", len(runner.requests))
	}
}

func TestGitHubSubmitterConsumesBoundedExactHeadReadArtifact(t *testing.T) {
	root := t.TempDir()
	artifact := filepath.Join(root, "pull-request.json")
	request := validSubmitRequest()
	body := `{"padding":"` + strings.Repeat("x", 32<<10) +
		`","head":{"sha":"` + request.HeadSHA + `"}}`
	if err := os.WriteFile(artifact, []byte(body), 0o600); err != nil {
		t.Fatalf("WriteFile(artifact) error = %v", err)
	}
	tag := "[file:" + artifact + "]"
	runner := &submitRecordingRunner{
		readText:     &tag,
		artifactTags: []string{tag},
	}
	_, err := (&GitHubSubmitter{
		Runner:       runner,
		ArtifactRoot: root,
	}).Submit(context.Background(), request)
	if err != nil {
		t.Fatalf("Submit() error = %v", err)
	}
	if _, err := os.Stat(artifact); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("consumed artifact still exists or stat failed: %v", err)
	}
	if len(runner.requests) != 3 {
		t.Fatalf("tool calls = %d, want read + create + submit", len(runner.requests))
	}
}

func TestGitHubSubmitterRejectsHeadReadArtifactSymlinkOutsideRoot(t *testing.T) {
	root := t.TempDir()
	outsideRoot := t.TempDir()
	outside := filepath.Join(outsideRoot, "pull-request.json")
	request := validSubmitRequest()
	if err := os.WriteFile(
		outside,
		[]byte(`{"head":{"sha":"`+request.HeadSHA+`"}}`),
		0o600,
	); err != nil {
		t.Fatalf("WriteFile(outside artifact) error = %v", err)
	}
	link := filepath.Join(root, "pull-request.json")
	if err := os.Symlink(outside, link); err != nil {
		t.Skipf("Symlink unavailable: %v", err)
	}
	tag := "[file:" + link + "]"
	runner := &submitRecordingRunner{
		readText:     &tag,
		artifactTags: []string{tag},
	}
	_, err := (&GitHubSubmitter{
		Runner:       runner,
		ArtifactRoot: root,
	}).Submit(context.Background(), request)
	if err == nil {
		t.Fatal("Submit() error = nil, want outside-root rejection")
	}
	var stageErr *SubmitStageError
	if !errors.As(err, &stageErr) ||
		stageErr.Stage != SubmitStageVerifyHead ||
		stageErr.ExternalStateMayHaveChanged {
		t.Fatalf("Submit() stage error = %#v", stageErr)
	}
	if _, err := os.Stat(outside); err != nil {
		t.Fatalf("rejected outside artifact was consumed: %v", err)
	}
	if len(runner.requests) != 1 {
		t.Fatalf("tool calls = %d, want read only", len(runner.requests))
	}
}

func TestGitHubSubmitterRejectsMalformedOrUnboundedHeadReadsBeforeWrite(t *testing.T) {
	validSHA := strings.Repeat("a", 40)
	tests := []struct {
		name string
		text string
	}{
		{name: "missing text JSON shape", text: `{"number":42}`},
		{name: "invalid SHA", text: `{"head":{"sha":"main"}}`},
		{name: "duplicate head key", text: `{"head":{"sha":"` + validSHA + `","sha":"` + validSHA + `"}}`},
		{name: "trailing JSON", text: `{"head":{"sha":"` + validSHA + `"}} {}`},
		{
			name: "excessive JSON nesting",
			text: `{"padding":` + strings.Repeat("[", maxSubmitJSONDepth+1) +
				`0` + strings.Repeat("]", maxSubmitJSONDepth+1) +
				`,"head":{"sha":"` + validSHA + `"}}`,
		},
		{name: "oversized", text: strings.Repeat(" ", maxSubmitPullRequestReadBytes+1)},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			text := test.text
			runner := &submitRecordingRunner{readText: &text}
			_, err := (&GitHubSubmitter{Runner: runner}).Submit(
				context.Background(),
				validSubmitRequest(),
			)
			if err == nil {
				t.Fatal("Submit() error = nil")
			}
			var stageErr *SubmitStageError
			if !errors.As(err, &stageErr) ||
				stageErr.Stage != SubmitStageVerifyHead ||
				stageErr.ExternalStateMayHaveChanged {
				t.Fatalf("Submit() stage error = %#v", stageErr)
			}
			if len(runner.requests) != 1 {
				t.Fatalf("tool calls = %d, want read only", len(runner.requests))
			}
		})
	}
}

func TestGitHubSubmitterStopsAtFirstToolErrorWithoutRetryOrDelete(t *testing.T) {
	firstLine := 8
	secondLine := 19
	request := validSubmitRequest()
	request.Findings = []SubmitFinding{
		{
			ID:      "prf_first",
			Title:   "First",
			File:    "first.go",
			Line:    &firstLine,
			Message: "First comment.",
		},
		{
			ID:      "prf_second",
			Title:   "Second",
			File:    "second.go",
			Line:    &secondLine,
			Message: "Second comment.",
		},
	}
	failure := errors.New("transport result lost")
	for _, test := range []struct {
		name           string
		failCall       int
		stage          SubmitStage
		findingIndex   int
		completedCalls int
	}{
		{
			name:           "create",
			failCall:       2,
			stage:          SubmitStageCreatePending,
			findingIndex:   -1,
			completedCalls: 0,
		},
		{
			name:           "first comment",
			failCall:       3,
			stage:          SubmitStageAddComment,
			findingIndex:   0,
			completedCalls: 1,
		},
		{
			name:           "second comment",
			failCall:       4,
			stage:          SubmitStageAddComment,
			findingIndex:   1,
			completedCalls: 2,
		},
		{
			name:           "submit",
			failCall:       5,
			stage:          SubmitStageSubmitPending,
			findingIndex:   -1,
			completedCalls: 3,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			runner := &submitRecordingRunner{
				failCall: test.failCall,
				err:      failure,
			}

			_, err := (&GitHubSubmitter{Runner: runner}).Submit(
				context.Background(),
				request,
			)
			if !errors.Is(err, failure) {
				t.Fatalf("Submit() error = %v, want wrapped failure", err)
			}
			var stageErr *SubmitStageError
			if !errors.As(err, &stageErr) {
				t.Fatalf("Submit() error type = %T, want *SubmitStageError", err)
			}
			if stageErr.Stage != test.stage ||
				stageErr.FindingIndex != test.findingIndex ||
				stageErr.CompletedCalls != test.completedCalls ||
				!stageErr.ExternalStateMayHaveChanged {
				t.Fatalf("stage error = %#v", stageErr)
			}
			if len(runner.requests) != test.failCall {
				t.Fatalf(
					"tool calls = %d, want stop after %d",
					len(runner.requests),
					test.failCall,
				)
			}
			for _, toolRequest := range runner.requests {
				if strings.Contains(toolRequest.MCPTool, "delete") {
					t.Fatalf("unexpected destructive tool call = %#v", toolRequest)
				}
			}
		})
	}
}

func TestGitHubSubmitterRecoversPendingReviewWithoutCreatingOrAddingComments(t *testing.T) {
	line := 27
	request := validSubmitRequest()
	request.Findings = []SubmitFinding{
		{
			ID: "prf_line", Title: "Queue item can be lost", File: "pkg/queue/worker.go", Line: &line,
			Message: "Restore the item before returning from this error path.",
		},
		{
			ID: "prf_body", Title: "Migration risk",
			Message: "The rollout plan does not cover existing queued data.",
		},
	}
	runner := &submitRecordingRunner{}
	result, err := (&GitHubSubmitter{Runner: runner}).SubmitPending(context.Background(), request)
	if err != nil {
		t.Fatalf("SubmitPending() error = %v", err)
	}
	if result.BodyFindings != len(request.Findings) || len(runner.requests) != 2 {
		t.Fatalf("SubmitPending() result=%#v calls=%d", result, len(runner.requests))
	}
	assertSubmitToolIdentity(t, runner.requests[0], DefaultGitHubMCPServer, GitHubPullRequestReadTool)
	assertSubmitToolIdentity(t, runner.requests[1], DefaultGitHubMCPServer, GitHubPullRequestReviewWriteTool)
	args := runner.requests[1].Args
	if args["method"] != "submit_pending" || args["event"] != "COMMENT" {
		t.Fatalf("recovery submit args = %#v", args)
	}
	body, ok := args["body"].(string)
	if !ok || strings.Count(body, request.Marker) != 1 ||
		!strings.Contains(body, request.Findings[0].Message) ||
		!strings.Contains(body, request.Findings[1].Message) {
		t.Fatalf("recovery body = %q", body)
	}
	for _, call := range runner.requests {
		if call.MCPTool == GitHubPendingReviewCommentTool || call.Args["method"] == "create" {
			t.Fatalf("recovery performed a duplicate write: %#v", call)
		}
	}
}

func TestGitHubSubmitterPendingRecoveryWriteFailureIsAmbiguous(t *testing.T) {
	failure := errors.New("submit result lost")
	runner := &submitRecordingRunner{failCall: 2, err: failure}
	_, err := (&GitHubSubmitter{Runner: runner}).SubmitPending(context.Background(), validSubmitRequest())
	var stageErr *SubmitStageError
	if !errors.Is(err, failure) || !errors.As(err, &stageErr) ||
		stageErr.Stage != SubmitStageRecoverPending || !stageErr.ExternalStateMayHaveChanged ||
		stageErr.CompletedCalls != 0 || len(runner.requests) != 2 {
		t.Fatalf("pending recovery error=%v stage=%#v calls=%d", err, stageErr, len(runner.requests))
	}
}

func TestGitHubSubmitterPreservesConfiguredServerIdentity(t *testing.T) {
	request := validSubmitRequest()
	runner := &submitRecordingRunner{}
	const server = "GitHub Enterprise"

	if _, err := (&GitHubSubmitter{
		Runner: runner,
		Server: server,
	}).Submit(context.Background(), request); err != nil {
		t.Fatalf("Submit() error = %v", err)
	}
	for _, toolRequest := range runner.requests {
		if toolRequest.MCPServer != server ||
			toolRequest.Name != picomcp.CanonicalToolName(server, toolRequest.MCPTool) {
			t.Fatalf("tool identity = %#v", toolRequest)
		}
	}
}

type submitRecordingRunner struct {
	requests     []workflows.ToolRequest
	failCall     int
	err          error
	headSHA      string
	readText     *string
	artifactTags []string
}

func (r *submitRecordingRunner) RunTool(
	_ context.Context,
	request workflows.ToolRequest,
) (map[string]any, error) {
	r.requests = append(r.requests, request)
	call := len(r.requests)
	if call == r.failCall {
		return nil, r.err
	}
	if request.MCPTool == GitHubPullRequestReadTool {
		if r.readText != nil {
			return map[string]any{
				"text":          *r.readText,
				"artifact_tags": append([]string(nil), r.artifactTags...),
			}, nil
		}
		headSHA := r.headSHA
		if headSHA == "" {
			headSHA = strings.Repeat("a", 40)
		}
		return map[string]any{
			"text": `{"head":{"sha":"` + headSHA + `"}}`,
		}, nil
	}
	return map[string]any{"call": call}, nil
}

func validSubmitRequest() SubmitRequest {
	return SubmitRequest{
		Owner:      "scylladb",
		Repo:       "gocql",
		PullNumber: 42,
		HeadSHA:    strings.Repeat("a", 40),
		Summary:    "Review found one actionable issue.",
		Marker:     "<!-- picoclaw-review:prs_0123456789abcdef -->",
		Findings: []SubmitFinding{{
			ID:      "prf_body",
			Title:   "Release sequencing",
			Message: "Deploy the reader before enabling the new writer.",
		}},
	}
}

func assertSubmitToolIdentity(
	t *testing.T,
	request workflows.ToolRequest,
	server string,
	tool string,
) {
	t.Helper()
	if !request.MCP ||
		request.MCPServer != server ||
		request.MCPTool != tool ||
		request.Name != picomcp.CanonicalToolName(server, tool) {
		t.Fatalf("tool identity = %#v", request)
	}
}
