package reviews

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"
	"unicode"
	"unicode/utf8"

	picomcp "github.com/sipeed/picoclaw/pkg/mcp"
	"github.com/sipeed/picoclaw/pkg/workflows"
)

const (
	DefaultGitHubMCPServer = "github"

	GitHubPullRequestReadTool        = "pull_request_read"
	GitHubIssueReadTool              = "issue_read"
	GitHubCreatePullRequestTool      = "create_pull_request"
	GitHubListPullRequestsTool       = "list_pull_requests"
	GitHubListCommitsTool            = "list_commits"
	GitHubGetMeTool                  = "get_me"
	GitHubSearchRepositoriesTool     = "search_repositories"
	GitHubIssueWriteTool             = "issue_write"
	GitHubSearchIssuesTool           = "search_issues"
	GitHubPullRequestReviewWriteTool = "pull_request_review_write"
	GitHubPendingReviewCommentTool   = "add_comment_to_pending_review"

	maxSubmitServerBytes                = 256
	maxSubmitOwnerBytes                 = 100
	maxSubmitRepositoryBytes            = 100
	maxSubmitFindingIDBytes             = 256
	maxSubmitTitleBytes                 = 8 << 10
	maxSubmitFileBytes                  = 4 << 10
	maxSubmitReviewBodyBytes            = 64 << 10
	maxSubmitMarkerBytes                = 1 << 10
	maxSubmitPullRequestReadBytes       = 1 << 20
	maxSubmitJSONDepth                  = 128
	maxSubmitFindings                   = 200
	maxSubmitPullNumber           int64 = 1<<31 - 1
	maxSubmitLine                       = 1<<31 - 1
)

func rejectDuplicateReviewJSONKeys(raw []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	var decodeValue func(int) error
	decodeValue = func(depth int) error {
		if depth > maxSubmitJSONDepth {
			return ErrInvalidSubmitRequest
		}
		token, err := decoder.Token()
		if err != nil {
			return err
		}
		delim, compound := token.(json.Delim)
		if !compound {
			return nil
		}
		switch delim {
		case '{':
			seen := make(map[string]struct{})
			for decoder.More() {
				nameToken, err := decoder.Token()
				if err != nil {
					return err
				}
				name, ok := nameToken.(string)
				if !ok {
					return ErrInvalidSubmitRequest
				}
				if _, duplicate := seen[name]; duplicate {
					return ErrInvalidSubmitRequest
				}
				seen[name] = struct{}{}
				if err := decodeValue(depth + 1); err != nil {
					return err
				}
			}
			closing, err := decoder.Token()
			if err != nil || closing != json.Delim('}') {
				return ErrInvalidSubmitRequest
			}
		case '[':
			for decoder.More() {
				if err := decodeValue(depth + 1); err != nil {
					return err
				}
			}
			closing, err := decoder.Token()
			if err != nil || closing != json.Delim(']') {
				return ErrInvalidSubmitRequest
			}
		default:
			return ErrInvalidSubmitRequest
		}
		return nil
	}
	if err := decodeValue(0); err != nil {
		return err
	}
	var trailing json.RawMessage
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return ErrInvalidSubmitRequest
		}
		return err
	}
	return nil
}

var ErrInvalidSubmitRequest = errors.New("invalid GitHub review submission")

// Submitter is the exact GitHub review-publication boundary used by the
// unified PR workspace.
type Submitter interface {
	Submit(ctx context.Context, request SubmitRequest) (SubmitResult, error)
	SubmitPending(ctx context.Context, request SubmitRequest) (SubmitResult, error)
}

// SubmitFinding is one finding in the caller's immutable active-finding
// snapshot. It intentionally has no dropped-state field: the durable worker
// must filter dropped findings before constructing SubmitRequest.
type SubmitFinding struct {
	ID      string `json:"id,omitempty"`
	Title   string `json:"title"`
	File    string `json:"file,omitempty"`
	Line    *int   `json:"line,omitempty"`
	Message string `json:"message"`
}

// SubmitRequest is the complete immutable payload needed by a durable
// submission worker. HeadSHA pins the pending review to the revision that was
// reviewed, and Marker lets a worker reconcile an ambiguously submitted review.
type SubmitRequest struct {
	Owner      string          `json:"owner"`
	Repo       string          `json:"repo"`
	PullNumber int64           `json:"pull_number"`
	HeadSHA    string          `json:"head_sha"`
	Summary    string          `json:"summary"`
	Marker     string          `json:"marker"`
	Findings   []SubmitFinding `json:"findings"`
}

// SubmitResult contains bounded accounting plus the create and submit tool
// outputs. Both output maps are JSON-compatible workflow tool results and may
// contain the provider's external review identity.
type SubmitResult struct {
	InlineComments  int            `json:"inline_comments"`
	BodyFindings    int            `json:"body_findings"`
	PendingReview   map[string]any `json:"pending_review,omitempty"`
	SubmittedReview map[string]any `json:"submitted_review,omitempty"`
}

// SubmitStage identifies the single protocol call that failed.
type SubmitStage string

const (
	SubmitStageValidate       SubmitStage = "validate"
	SubmitStageVerifyHead     SubmitStage = "verify_pull_request_head"
	SubmitStageCreatePending  SubmitStage = "create_pending_review"
	SubmitStageAddComment     SubmitStage = "add_pending_review_comment"
	SubmitStageSubmitPending  SubmitStage = "submit_pending_review"
	SubmitStageRecoverPending SubmitStage = "recover_pending_review"
)

// SubmitStageError reports where submission stopped. When
// ExternalStateMayHaveChanged is true, the failing MCP call may have reached
// GitHub even though its result was not observed. Callers must reconcile the
// external state (using Marker when submission may have completed); they must
// not blindly retry or delete the pending review.
type SubmitStageError struct {
	Stage                       SubmitStage
	FindingIndex                int
	FindingID                   string
	CompletedCalls              int
	ExternalStateMayHaveChanged bool
	Err                         error
}

func (e *SubmitStageError) Error() string {
	if e == nil {
		return "GitHub review submission failed"
	}
	location := string(e.Stage)
	if e.FindingIndex >= 0 {
		location = fmt.Sprintf("%s at finding %d", location, e.FindingIndex)
	}
	if e.Err == nil {
		return "GitHub review submission failed during " + location
	}
	return fmt.Sprintf("GitHub review submission failed during %s: %v", location, e.Err)
}

func (e *SubmitStageError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

// PullRequestHeadChangedError is an affirmative, pre-write stale signal. It
// means GitHub returned a current head SHA that differs from the immutable
// revision reviewed by PicoClaw.
type PullRequestHeadChangedError struct {
	Expected string
	Actual   string
}

func (e *PullRequestHeadChangedError) Error() string {
	if e == nil {
		return "pull request head changed"
	}
	return fmt.Sprintf(
		"pull request head changed from %s to %s",
		e.Expected,
		e.Actual,
	)
}

// GitHubSubmitter executes GitHub's pending-review protocol through the exact
// MCP identities registered in the workflow tool runner.
type GitHubSubmitter struct {
	Runner       workflows.ToolRunner
	Server       string
	ArtifactRoot string
}

// Submit performs the complex-review protocol exactly once:
//
//  1. read the pull request and verify its current head revision;
//  2. create a pending review without an event, with a complete recovery body
//     and publication marker;
//  3. add every file-scoped finding to that pending review;
//  4. submit it as COMMENT with summary, body-only findings, and marker.
//
// It deliberately performs no retry and never deletes a pending review.
func (s *GitHubSubmitter) Submit(
	ctx context.Context,
	request SubmitRequest,
) (SubmitResult, error) {
	server := DefaultGitHubMCPServer
	if s != nil && s.Server != "" {
		server = s.Server
	}
	if err := validateSubmitRequest(ctx, s, server, request); err != nil {
		return SubmitResult{}, &SubmitStageError{
			Stage:        SubmitStageValidate,
			FindingIndex: -1,
			Err:          err,
		}
	}
	currentHead, err := s.readPullRequestHead(ctx, server, request)
	if err != nil {
		return SubmitResult{}, &SubmitStageError{
			Stage:        SubmitStageVerifyHead,
			FindingIndex: -1,
			Err:          err,
		}
	}
	if currentHead != request.HeadSHA {
		return SubmitResult{}, &SubmitStageError{
			Stage:        SubmitStageVerifyHead,
			FindingIndex: -1,
			Err: &PullRequestHeadChangedError{
				Expected: request.HeadSHA,
				Actual:   currentHead,
			},
		}
	}

	finalBody := submitReviewBody(request)
	recoveryBody := submitReviewRecoveryBody(request)
	completedCalls := 0
	pending, err := s.run(
		ctx,
		server,
		GitHubPullRequestReviewWriteTool,
		map[string]any{
			"method":     "create",
			"owner":      request.Owner,
			"repo":       request.Repo,
			"pullNumber": request.PullNumber,
			"commitID":   request.HeadSHA,
			"body":       recoveryBody,
		},
	)
	if err != nil {
		return SubmitResult{}, externalSubmitError(
			SubmitStageCreatePending,
			-1,
			"",
			completedCalls,
			err,
		)
	}
	completedCalls++

	inlineComments := 0
	bodyFindings := 0
	for index, finding := range request.Findings {
		if finding.File == "" {
			bodyFindings++
			continue
		}
		args := map[string]any{
			"owner":       request.Owner,
			"repo":        request.Repo,
			"pullNumber":  request.PullNumber,
			"path":        finding.File,
			"body":        finding.Message,
			"subjectType": "FILE",
		}
		if finding.Line != nil {
			args["subjectType"] = "LINE"
			args["line"] = *finding.Line
			args["side"] = "RIGHT"
		}
		if _, commentErr := s.run(
			ctx,
			server,
			GitHubPendingReviewCommentTool,
			args,
		); commentErr != nil {
			return SubmitResult{
					InlineComments: inlineComments, BodyFindings: bodyFindings,
					PendingReview: pending,
				}, externalSubmitError(
					SubmitStageAddComment,
					index,
					finding.ID,
					completedCalls,
					commentErr,
				)
		}
		completedCalls++
		inlineComments++
	}

	submitted, err := s.run(
		ctx,
		server,
		GitHubPullRequestReviewWriteTool,
		map[string]any{
			"method":     "submit_pending",
			"owner":      request.Owner,
			"repo":       request.Repo,
			"pullNumber": request.PullNumber,
			"event":      "COMMENT",
			"body":       finalBody,
		},
	)
	if err != nil {
		return SubmitResult{
				InlineComments: inlineComments, BodyFindings: bodyFindings,
				PendingReview: pending,
			}, externalSubmitError(
				SubmitStageSubmitPending,
				-1,
				"",
				completedCalls,
				err,
			)
	}

	return SubmitResult{
		InlineComments:  inlineComments,
		BodyFindings:    bodyFindings,
		PendingReview:   pending,
		SubmittedReview: submitted,
	}, nil
}

// SubmitPending safely completes a marker-bearing pending review discovered
// during ambiguous-publication reconciliation. It never creates a review or
// adds more inline comments. The recovery body includes every finding, so a
// partially completed comment loop cannot lose review information.
func (s *GitHubSubmitter) SubmitPending(
	ctx context.Context,
	request SubmitRequest,
) (SubmitResult, error) {
	server := DefaultGitHubMCPServer
	if s != nil && s.Server != "" {
		server = s.Server
	}
	if err := validateSubmitRequest(ctx, s, server, request); err != nil {
		return SubmitResult{}, &SubmitStageError{
			Stage: SubmitStageValidate, FindingIndex: -1, Err: err,
		}
	}
	currentHead, err := s.readPullRequestHead(ctx, server, request)
	if err != nil {
		return SubmitResult{}, &SubmitStageError{
			Stage: SubmitStageVerifyHead, FindingIndex: -1, Err: err,
		}
	}
	if currentHead != request.HeadSHA {
		return SubmitResult{}, &SubmitStageError{
			Stage: SubmitStageVerifyHead, FindingIndex: -1,
			Err: &PullRequestHeadChangedError{Expected: request.HeadSHA, Actual: currentHead},
		}
	}
	submitted, err := s.run(
		ctx,
		server,
		GitHubPullRequestReviewWriteTool,
		map[string]any{
			"method":     "submit_pending",
			"owner":      request.Owner,
			"repo":       request.Repo,
			"pullNumber": request.PullNumber,
			"event":      "COMMENT",
			"body":       submitReviewRecoveryBody(request),
		},
	)
	if err != nil {
		return SubmitResult{}, externalSubmitError(
			SubmitStageRecoverPending, -1, "", 0, err,
		)
	}
	return SubmitResult{
		BodyFindings: len(request.Findings), SubmittedReview: submitted,
	}, nil
}

func (s *GitHubSubmitter) readPullRequestHead(
	ctx context.Context,
	server string,
	request SubmitRequest,
) (string, error) {
	outputs, err := s.run(
		ctx,
		server,
		GitHubPullRequestReadTool,
		map[string]any{
			"method":     "get",
			"owner":      request.Owner,
			"repo":       request.Repo,
			"pullNumber": request.PullNumber,
		},
	)
	if err != nil {
		return "", err
	}
	raw, err := s.exactPullRequestText(outputs)
	if err != nil {
		return "", err
	}
	if err := rejectDuplicateReviewJSONKeys(raw); err != nil {
		return "", fmt.Errorf("GitHub pull-request result is invalid JSON: %w", err)
	}
	var response struct {
		Head struct {
			SHA string `json:"sha"`
		} `json:"head"`
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	if err := decoder.Decode(&response); err != nil {
		return "", fmt.Errorf("decode GitHub pull-request result: %w", err)
	}
	var extra json.RawMessage
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return "", errors.New("GitHub pull-request result contains trailing JSON")
		}
		return "", fmt.Errorf("decode GitHub pull-request result: %w", err)
	}
	if !validSubmitGitObjectID(response.Head.SHA) {
		return "", errors.New("GitHub pull-request result has an invalid head SHA")
	}
	return response.Head.SHA, nil
}

func (s *GitHubSubmitter) exactPullRequestText(
	outputs map[string]any,
) ([]byte, error) {
	if outputs == nil {
		return nil, errors.New("GitHub pull-request result is missing")
	}
	text, ok := outputs["text"].(string)
	if !ok {
		return nil, errors.New("GitHub pull-request result text is missing")
	}
	text = strings.TrimSpace(text)
	if text == "" ||
		len(text) > maxSubmitPullRequestReadBytes ||
		!utf8.ValidString(text) {
		return nil, errors.New("GitHub pull-request result text is invalid or too large")
	}
	if strings.HasPrefix(text, "{") {
		return []byte(text), nil
	}
	if s == nil || strings.TrimSpace(s.ArtifactRoot) == "" {
		return nil, errors.New("GitHub pull-request exact result artifact is unavailable")
	}
	tags, ok := outputs["artifact_tags"].([]string)
	if !ok || len(tags) != 1 {
		return nil, errors.New("GitHub pull-request exact result artifact is missing")
	}
	artifactPath, ok := strings.CutPrefix(tags[0], "[file:")
	if !ok || !strings.HasSuffix(artifactPath, "]") {
		return nil, errors.New("GitHub pull-request exact result artifact tag is invalid")
	}
	artifactPath = strings.TrimSuffix(artifactPath, "]")
	root, err := filepath.Abs(strings.TrimSpace(s.ArtifactRoot))
	if err != nil {
		return nil, err
	}
	artifactPath, err = filepath.Abs(artifactPath)
	if err != nil {
		return nil, err
	}
	resolvedRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return nil, err
	}
	resolvedPath, err := filepath.EvalSymlinks(artifactPath)
	if err != nil {
		return nil, err
	}
	relative, err := filepath.Rel(resolvedRoot, resolvedPath)
	if err != nil ||
		relative == "." ||
		relative == ".." ||
		strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return nil, errors.New(
			"GitHub pull-request exact result artifact is outside the configured root",
		)
	}
	file, err := os.Open(resolvedPath)
	if err != nil {
		return nil, err
	}
	info, err := file.Stat()
	if err != nil ||
		!info.Mode().IsRegular() ||
		info.Size() > maxSubmitPullRequestReadBytes {
		_ = file.Close()
		return nil, errors.New(
			"GitHub pull-request exact result artifact is invalid or too large",
		)
	}
	data, err := io.ReadAll(io.LimitReader(file, maxSubmitPullRequestReadBytes+1))
	if err != nil ||
		len(data) > maxSubmitPullRequestReadBytes ||
		!utf8.Valid(data) {
		_ = file.Close()
		return nil, errors.New(
			"GitHub pull-request exact result artifact is invalid or too large",
		)
	}
	if err := file.Close(); err != nil {
		return nil, err
	}
	if err := os.Remove(resolvedPath); err != nil {
		return nil, fmt.Errorf(
			"remove consumed GitHub pull-request result artifact: %w",
			err,
		)
	}
	return bytes.TrimSpace(data), nil
}

func (s *GitHubSubmitter) run(
	ctx context.Context,
	server string,
	tool string,
	args map[string]any,
) (map[string]any, error) {
	return s.Runner.RunTool(ctx, workflows.ToolRequest{
		Name:      picomcp.CanonicalToolName(server, tool),
		Args:      args,
		MCP:       true,
		MCPServer: server,
		MCPTool:   tool,
	})
}

func externalSubmitError(
	stage SubmitStage,
	findingIndex int,
	findingID string,
	completedCalls int,
	err error,
) *SubmitStageError {
	return &SubmitStageError{
		Stage:                       stage,
		FindingIndex:                findingIndex,
		FindingID:                   findingID,
		CompletedCalls:              completedCalls,
		ExternalStateMayHaveChanged: true,
		Err:                         err,
	}
}

func validateSubmitRequest(
	ctx context.Context,
	submitter *GitHubSubmitter,
	server string,
	request SubmitRequest,
) error {
	if ctx == nil {
		return fmt.Errorf("%w: context is required", ErrInvalidSubmitRequest)
	}
	if submitter == nil || submitter.Runner == nil {
		return fmt.Errorf("%w: tool runner is required", ErrInvalidSubmitRequest)
	}
	if err := validateSubmitText("MCP server", server, maxSubmitServerBytes, true); err != nil {
		return err
	}
	if !validSubmitRepositoryComponent(request.Owner, maxSubmitOwnerBytes) {
		return fmt.Errorf("%w: owner is invalid", ErrInvalidSubmitRequest)
	}
	if !validSubmitRepositoryComponent(request.Repo, maxSubmitRepositoryBytes) {
		return fmt.Errorf("%w: repository is invalid", ErrInvalidSubmitRequest)
	}
	if request.PullNumber <= 0 || request.PullNumber > maxSubmitPullNumber {
		return fmt.Errorf("%w: pull number is invalid", ErrInvalidSubmitRequest)
	}
	if !validSubmitGitObjectID(request.HeadSHA) {
		return fmt.Errorf("%w: head SHA is invalid", ErrInvalidSubmitRequest)
	}
	if err := validateSubmitText(
		"summary",
		request.Summary,
		maxSubmitReviewBodyBytes,
		true,
	); err != nil {
		return err
	}
	if err := validateSubmitText(
		"marker",
		request.Marker,
		maxSubmitMarkerBytes,
		true,
	); err != nil {
		return err
	}
	if len(request.Findings) == 0 || len(request.Findings) > maxSubmitFindings {
		return fmt.Errorf(
			"%w: active finding count must be between 1 and %d",
			ErrInvalidSubmitRequest,
			maxSubmitFindings,
		)
	}

	seenIDs := make(map[string]struct{}, len(request.Findings))
	for index, finding := range request.Findings {
		if err := validateSubmitFinding(index, finding, seenIDs); err != nil {
			return err
		}
	}
	for name, body := range map[string]string{
		"submitted review": submitReviewBody(request),
		"recovery review":  submitReviewRecoveryBody(request),
	} {
		if len(body) > maxSubmitReviewBodyBytes || strings.Count(body, request.Marker) != 1 {
			return fmt.Errorf(
				"%w: %s body exceeds %d bytes or does not contain exactly one marker",
				ErrInvalidSubmitRequest, name, maxSubmitReviewBodyBytes,
			)
		}
	}
	return nil
}

func validateSubmitFinding(
	index int,
	finding SubmitFinding,
	seenIDs map[string]struct{},
) error {
	if finding.ID != "" {
		if err := validateSubmitText(
			"finding ID",
			finding.ID,
			maxSubmitFindingIDBytes,
			true,
		); err != nil {
			return fmt.Errorf("finding %d: %w", index, err)
		}
		if _, duplicate := seenIDs[finding.ID]; duplicate {
			return fmt.Errorf(
				"%w: finding %d has duplicate ID %q",
				ErrInvalidSubmitRequest,
				index,
				finding.ID,
			)
		}
		seenIDs[finding.ID] = struct{}{}
	}
	if err := validateSubmitText(
		"finding title",
		finding.Title,
		maxSubmitTitleBytes,
		true,
	); err != nil {
		return fmt.Errorf("finding %d: %w", index, err)
	}
	if err := validateSubmitText(
		"finding message",
		finding.Message,
		maxSubmitReviewBodyBytes,
		true,
	); err != nil {
		return fmt.Errorf("finding %d: %w", index, err)
	}
	if finding.File != "" {
		if err := validateSubmitText(
			"finding file",
			finding.File,
			maxSubmitFileBytes,
			true,
		); err != nil {
			return fmt.Errorf("finding %d: %w", index, err)
		}
		cleaned := path.Clean(finding.File)
		if strings.HasPrefix(finding.File, "/") ||
			cleaned == "." ||
			cleaned == ".." ||
			strings.HasPrefix(cleaned, "../") ||
			cleaned != finding.File ||
			strings.ContainsRune(finding.File, '\\') {
			return fmt.Errorf(
				"%w: finding %d file must be a clean repository-relative slash path",
				ErrInvalidSubmitRequest,
				index,
			)
		}
	}
	if finding.Line != nil {
		if finding.File == "" || *finding.Line <= 0 || *finding.Line > maxSubmitLine {
			return fmt.Errorf(
				"%w: finding %d line requires a file and must be positive",
				ErrInvalidSubmitRequest,
				index,
			)
		}
	}
	return nil
}

func validateSubmitText(
	field string,
	value string,
	limit int,
	required bool,
) error {
	if !utf8.ValidString(value) ||
		len(value) > limit ||
		value != strings.TrimSpace(value) ||
		strings.IndexByte(value, 0) >= 0 {
		return fmt.Errorf("%w: %s is invalid or exceeds %d bytes", ErrInvalidSubmitRequest, field, limit)
	}
	if required && value == "" {
		return fmt.Errorf("%w: %s is required", ErrInvalidSubmitRequest, field)
	}
	for _, char := range value {
		if unicode.IsControl(char) && char != '\n' && char != '\r' && char != '\t' {
			return fmt.Errorf("%w: %s contains a control character", ErrInvalidSubmitRequest, field)
		}
	}
	return nil
}

func validSubmitRepositoryComponent(value string, limit int) bool {
	if value == "" ||
		len(value) > limit ||
		value != strings.TrimSpace(value) ||
		value == "." ||
		value == ".." ||
		!utf8.ValidString(value) {
		return false
	}
	for _, char := range value {
		if char >= 'a' && char <= 'z' ||
			char >= 'A' && char <= 'Z' ||
			char >= '0' && char <= '9' ||
			char == '_' ||
			char == '-' ||
			char == '.' {
			continue
		}
		return false
	}
	return true
}

func validSubmitGitObjectID(value string) bool {
	if len(value) != 40 && len(value) != 64 {
		return false
	}
	for _, char := range value {
		if char >= '0' && char <= '9' || char >= 'a' && char <= 'f' {
			continue
		}
		return false
	}
	return true
}

func submitReviewBody(request SubmitRequest) string {
	sections := []string{request.Summary}
	bodyFindingCount := 0
	var bodyFindings strings.Builder
	for _, finding := range request.Findings {
		if finding.File != "" {
			continue
		}
		if bodyFindingCount == 0 {
			bodyFindings.WriteString("### Additional review findings")
		}
		bodyFindings.WriteString("\n\n#### ")
		bodyFindings.WriteString(finding.Title)
		bodyFindings.WriteString("\n\n")
		bodyFindings.WriteString(finding.Message)
		bodyFindingCount++
	}
	if bodyFindingCount > 0 {
		sections = append(sections, bodyFindings.String())
	}
	sections = append(sections, request.Marker)
	return strings.Join(sections, "\n\n")
}

func submitReviewRecoveryBody(request SubmitRequest) string {
	sections := make([]string, 1, 3)
	sections[0] = request.Summary
	var findings strings.Builder
	findings.WriteString("### Review findings")
	for _, finding := range request.Findings {
		findings.WriteString("\n\n#### ")
		findings.WriteString(finding.Title)
		if finding.File != "" {
			findings.WriteString(" (`")
			findings.WriteString(finding.File)
			if finding.Line != nil {
				fmt.Fprintf(&findings, ":%d", *finding.Line)
			}
			findings.WriteString("`)")
		}
		findings.WriteString("\n\n")
		findings.WriteString(finding.Message)
	}
	sections = append(sections, findings.String(), request.Marker)
	return strings.Join(sections, "\n\n")
}
