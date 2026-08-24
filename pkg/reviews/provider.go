package reviews

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"reflect"
	"strings"
	"unicode/utf8"

	picomcp "github.com/sipeed/picoclaw/pkg/mcp"
	"github.com/sipeed/picoclaw/pkg/workflows"
)

const (
	// MaxWorkspaceReviewHistoryPages bounds ambiguous-publication reconciliation.
	MaxWorkspaceReviewHistoryPages = 5
	workspaceReviewsPerPage        = 100
	workspaceRepositoriesPerPage   = 100
	providerMaximumResultBytes     = 16 << 20
	providerMaximumDiffBytes       = 512 << 10
)

var (
	ErrInvalidWorkspaceProviderRequest    = errors.New("invalid PR workspace provider request")
	ErrWorkspaceProviderCallNotDispatched = errors.New("PR workspace provider call was not dispatched")
	ErrProviderIncompatible               = errors.New("PR workspace provider response is incompatible")
	errProviderResultLimit                = errors.New("PR workspace provider result exceeds its limit")
)

// GitHubProvider is the narrow, read-mostly provider boundary used by the
// unified PR workspace. It exposes only exact pull evidence, publication
// reconciliation, authenticated identity, and deferred-issue operations.
type GitHubProvider struct {
	Runner       workflows.ToolRunner
	Server       string
	ArtifactRoot string

	artifactCleanupHook func(string)
}

func NewGitHubProvider(
	runner workflows.ToolRunner,
	artifactRoot string,
) (*GitHubProvider, error) {
	if runner == nil {
		return nil, errors.New("GitHub PR workspace provider runner is required")
	}
	return &GitHubProvider{
		Runner:       runner,
		ArtifactRoot: strings.TrimSpace(artifactRoot),
	}, nil
}

// ReadWorkspacePullJSON returns one bounded exact pull record. The raw bytes
// remain gateway-private; only an allowlisted projection may be persisted.
func (provider *GitHubProvider) ReadWorkspacePullJSON(
	ctx context.Context,
	repository string,
	pullNumber int64,
) ([]byte, error) {
	owner, repo, ok := workspaceRepository(repository)
	if !ok || pullNumber < 1 {
		return nil, ErrInvalidWorkspaceProviderRequest
	}
	return provider.readExact(ctx, map[string]any{
		"method": "get", "owner": owner, "repo": repo,
		"pullNumber": pullNumber,
	}, providerMaximumResultBytes)
}

// ReadWorkspaceIssueJSON returns one bounded exact issue record for
// development-workspace intake.
func (provider *GitHubProvider) ReadWorkspaceIssueJSON(
	ctx context.Context,
	repository string,
	issueNumber int64,
) ([]byte, error) {
	owner, repo, ok := workspaceRepository(repository)
	if !ok || issueNumber < 1 {
		return nil, ErrInvalidWorkspaceProviderRequest
	}
	outputs, err := provider.run(ctx, GitHubIssueReadTool, map[string]any{
		"method": "get", "owner": owner, "repo": repo, "issue_number": issueNumber,
	})
	if err != nil {
		return nil, err
	}
	return provider.exactJSON(outputs, providerMaximumResultBytes)
}

func (provider *GitHubProvider) ReadWorkspaceIssueCommentsJSON(
	ctx context.Context,
	repository string,
	issueNumber int64,
) ([]byte, error) {
	owner, repo, ok := workspaceRepository(repository)
	if !ok || issueNumber < 1 {
		return nil, ErrInvalidWorkspaceProviderRequest
	}
	outputs, err := provider.run(ctx, GitHubIssueReadTool, map[string]any{
		"method": "get_comments", "owner": owner, "repo": repo,
		"issue_number": issueNumber, "page": 1, "perPage": 100,
	})
	if err != nil {
		return nil, err
	}
	return provider.exactJSON(outputs, 256<<10)
}

// CreateWorkspacePullRequestJSON invokes only the draft-PR creation
// capability with a closed argument set.
func (provider *GitHubProvider) CreateWorkspacePullRequestJSON(
	ctx context.Context,
	args map[string]any,
) ([]byte, error) {
	allowed := map[string]bool{
		"owner": true, "repo": true, "title": true, "body": true,
		"head": true, "base": true, "draft": true, "maintainer_can_modify": true,
	}
	if len(args) == 0 {
		return nil, ErrInvalidWorkspaceProviderRequest
	}
	clean := make(map[string]any, len(args))
	for key, value := range args {
		if !allowed[key] {
			return nil, ErrInvalidWorkspaceProviderRequest
		}
		clean[key] = value
	}
	for _, key := range []string{"owner", "repo", "title", "head", "base"} {
		value, ok := clean[key].(string)
		if !ok || strings.TrimSpace(value) == "" || strings.ContainsAny(value, "\x00\r\n") {
			return nil, ErrInvalidWorkspaceProviderRequest
		}
	}
	if draft, ok := clean["draft"].(bool); !ok || !draft {
		return nil, ErrInvalidWorkspaceProviderRequest
	}
	outputs, err := provider.run(ctx, GitHubCreatePullRequestTool, clean)
	if err != nil {
		return nil, err
	}
	return provider.exactJSON(outputs, providerMaximumResultBytes)
}

func (provider *GitHubProvider) ListWorkspacePullRequestsJSON(
	ctx context.Context,
	repository, head, base string,
) ([]byte, error) {
	owner, repo, ok := workspaceRepository(repository)
	if !ok || strings.TrimSpace(head) == "" || strings.TrimSpace(base) == "" ||
		strings.ContainsAny(head+base, "\x00\r\n") {
		return nil, ErrInvalidWorkspaceProviderRequest
	}
	outputs, err := provider.run(ctx, GitHubListPullRequestsTool, map[string]any{
		"owner": owner, "repo": repo, "head": owner + ":" + head, "base": base,
		"state": "open", "page": 1, "perPage": 100,
	})
	if err != nil {
		return nil, err
	}
	return provider.exactJSON(outputs, providerMaximumResultBytes)
}

// ReadWorkspaceDiff returns the provider's exact bounded unified diff.
func (provider *GitHubProvider) ReadWorkspaceDiff(
	ctx context.Context,
	repository string,
	pullNumber int64,
) ([]byte, error) {
	owner, repo, ok := workspaceRepository(repository)
	if !ok || pullNumber < 1 {
		return nil, ErrInvalidWorkspaceProviderRequest
	}
	outputs, err := provider.run(ctx, GitHubPullRequestReadTool, map[string]any{
		"method": "get_diff", "owner": owner, "repo": repo,
		"pullNumber": pullNumber,
	})
	if err != nil {
		return nil, err
	}
	return provider.exactText(outputs, providerMaximumDiffBytes)
}

// ReadWorkspaceReviewsJSON returns one bounded review-history page for marker
// reconciliation after an ambiguous publication.
func (provider *GitHubProvider) ReadWorkspaceReviewsJSON(
	ctx context.Context,
	repository string,
	pullNumber int64,
	page int,
) ([]byte, error) {
	owner, repo, ok := workspaceRepository(repository)
	if !ok || pullNumber < 1 || page < 1 || page > MaxWorkspaceReviewHistoryPages {
		return nil, ErrInvalidWorkspaceProviderRequest
	}
	return provider.readExact(ctx, map[string]any{
		"method": "get_reviews", "owner": owner, "repo": repo,
		"pullNumber": pullNumber, "page": page,
		"perPage": workspaceReviewsPerPage,
	}, providerMaximumResultBytes)
}

// ReadWorkspaceViewerJSON resolves the authenticated provider identity.
func (provider *GitHubProvider) ReadWorkspaceViewerJSON(
	ctx context.Context,
) ([]byte, error) {
	outputs, err := provider.run(ctx, GitHubGetMeTool, map[string]any{})
	if err != nil {
		return nil, err
	}
	return provider.exactJSON(outputs, providerMaximumResultBytes)
}

// SearchWorkspaceRepositoriesJSON searches one exact repository name within
// one user or organization. Callers must still verify the returned full_name;
// GitHub name search can return partial matches.
func (provider *GitHubProvider) SearchWorkspaceRepositoriesJSON(
	ctx context.Context,
	repository string,
	ownerQualifier string,
) ([]byte, error) {
	owner, repo, ok := workspaceRepository(repository)
	if !ok || !validWorkspaceRepositorySearchPart(owner, false) ||
		!validWorkspaceRepositorySearchPart(repo, true) ||
		(ownerQualifier != "user" && ownerQualifier != "org") {
		return nil, ErrInvalidWorkspaceProviderRequest
	}
	outputs, err := provider.run(ctx, GitHubSearchRepositoriesTool, map[string]any{
		"query":          repo + " in:name " + ownerQualifier + ":" + owner,
		"minimal_output": false,
		"page":           1,
		"perPage":        workspaceRepositoriesPerPage,
	})
	if err != nil {
		return nil, err
	}
	return provider.exactJSON(outputs, providerMaximumResultBytes)
}

func (provider *GitHubProvider) ListWorkspaceCommitsJSON(
	ctx context.Context,
	repository string,
	ref string,
) ([]byte, error) {
	owner, repo, ok := workspaceRepository(repository)
	if !ok || strings.TrimSpace(ref) == "" || strings.ContainsAny(ref, "\x00\r\n") {
		return nil, ErrInvalidWorkspaceProviderRequest
	}
	outputs, err := provider.run(ctx, GitHubListCommitsTool, map[string]any{
		"owner": owner, "repo": repo, "sha": ref, "page": 1, "perPage": 1,
	})
	if err != nil {
		return nil, err
	}
	return provider.exactJSON(outputs, providerMaximumResultBytes)
}

// CreateWorkspaceIssueJSON invokes only the issue-create capability.
func (provider *GitHubProvider) CreateWorkspaceIssueJSON(
	ctx context.Context,
	args map[string]any,
) ([]byte, error) {
	createArgs, err := workspaceIssueCreateArgs(args)
	if err != nil {
		return nil, errors.Join(ErrWorkspaceProviderCallNotDispatched, err)
	}
	outputs, err := provider.run(ctx, GitHubIssueWriteTool, createArgs)
	if err != nil {
		return nil, err
	}
	return provider.exactJSON(outputs, providerMaximumResultBytes)
}

// SearchWorkspaceIssuesJSON invokes only issue search for marker recovery.
func (provider *GitHubProvider) SearchWorkspaceIssuesJSON(
	ctx context.Context,
	args map[string]any,
) ([]byte, error) {
	outputs, err := provider.run(ctx, GitHubSearchIssuesTool, args)
	if err != nil {
		return nil, err
	}
	return provider.exactJSON(outputs, providerMaximumResultBytes)
}

func workspaceRepository(repository string) (string, string, bool) {
	owner, repo, ok := strings.Cut(strings.TrimSpace(repository), "/")
	return owner, repo, ok && owner != "" && repo != "" && !strings.Contains(repo, "/")
}

func validWorkspaceRepositorySearchPart(value string, repository bool) bool {
	if value == "" || len(value) > 100 {
		return false
	}
	for _, character := range value {
		if character >= 'a' && character <= 'z' ||
			character >= 'A' && character <= 'Z' ||
			character >= '0' && character <= '9' || character == '-' ||
			repository && (character == '_' || character == '.') {
			continue
		}
		return false
	}
	return true
}

func workspaceIssueCreateArgs(args map[string]any) (map[string]any, error) {
	if len(args) == 0 {
		return nil, ErrInvalidWorkspaceProviderRequest
	}
	createArgs := make(map[string]any, len(args)+1)
	for key, value := range args {
		switch key {
		case "owner", "repo", "title", "body", "labels":
			createArgs[key] = value
		default:
			return nil, ErrInvalidWorkspaceProviderRequest
		}
	}
	owner, ownerOK := createArgs["owner"].(string)
	repo, repoOK := createArgs["repo"].(string)
	title, titleOK := createArgs["title"].(string)
	if !ownerOK || !repoOK || !titleOK || strings.TrimSpace(owner) == "" ||
		strings.TrimSpace(repo) == "" || strings.Contains(owner, "/") ||
		strings.Contains(repo, "/") || strings.TrimSpace(title) == "" {
		return nil, ErrInvalidWorkspaceProviderRequest
	}
	createArgs["method"] = "create"
	return createArgs, nil
}

func (provider *GitHubProvider) readExact(
	ctx context.Context,
	args map[string]any,
	limit int,
) ([]byte, error) {
	outputs, err := provider.run(ctx, GitHubPullRequestReadTool, args)
	if err != nil {
		return nil, err
	}
	return provider.exactJSON(outputs, limit)
}

func (provider *GitHubProvider) run(
	ctx context.Context,
	tool string,
	args map[string]any,
) (map[string]any, error) {
	if provider == nil || provider.Runner == nil {
		return nil, errors.New("GitHub PR workspace provider is unavailable")
	}
	server := strings.TrimSpace(provider.Server)
	if server == "" {
		server = DefaultGitHubMCPServer
	}
	normalizedArgs, err := normalizeProviderToolArgs(args)
	if err != nil {
		return nil, errors.Join(ErrWorkspaceProviderCallNotDispatched, err)
	}
	outputs, err := provider.Runner.RunTool(ctx, workflows.ToolRequest{
		Name: picomcp.CanonicalToolName(server, tool), Args: normalizedArgs,
		MCP: true, MCPServer: server, MCPTool: tool,
	})
	if err != nil && errors.Is(err, workflows.ErrToolCallNotDispatched) {
		return nil, errors.Join(ErrWorkspaceProviderCallNotDispatched, err)
	}
	return outputs, err
}

// WorkspaceProviderCallMayHaveChangedExternalState distinguishes a failure
// before the MCP runner was invoked from a transport/provider failure whose
// external outcome must be reconciled.
func WorkspaceProviderCallMayHaveChangedExternalState(err error) bool {
	return err != nil && !errors.Is(err, ErrWorkspaceProviderCallNotDispatched)
}

// normalizeProviderToolArgs converts programmatic Go containers (notably
// []string) to the same JSON-shaped []any/map[string]any values produced by a
// decoded tool call. The tool registry validates that JSON shape before it
// invokes an MCP server.
func normalizeProviderToolArgs(args map[string]any) (map[string]any, error) {
	if args == nil {
		return map[string]any{}, nil
	}
	normalized, err := normalizeProviderToolValue(args, 0)
	if err != nil {
		return nil, ErrInvalidWorkspaceProviderRequest
	}
	result, ok := normalized.(map[string]any)
	if !ok {
		return nil, ErrInvalidWorkspaceProviderRequest
	}
	return result, nil
}

func normalizeProviderToolValue(value any, depth int) (any, error) {
	if depth > 64 {
		return nil, ErrInvalidWorkspaceProviderRequest
	}
	switch typed := value.(type) {
	case map[string]any:
		result := make(map[string]any, len(typed))
		for key, item := range typed {
			normalized, err := normalizeProviderToolValue(item, depth+1)
			if err != nil {
				return nil, err
			}
			result[key] = normalized
		}
		return result, nil
	case []any:
		result := make([]any, len(typed))
		for index, item := range typed {
			normalized, err := normalizeProviderToolValue(item, depth+1)
			if err != nil {
				return nil, err
			}
			result[index] = normalized
		}
		return result, nil
	}

	if value == nil {
		return nil, nil
	}
	reflected := reflect.ValueOf(value)
	kind := reflected.Kind()
	if kind == reflect.Array || kind == reflect.Slice {
		result := make([]any, reflected.Len())
		for index := 0; index < reflected.Len(); index++ {
			normalized, err := normalizeProviderToolValue(reflected.Index(index).Interface(), depth+1)
			if err != nil {
				return nil, err
			}
			result[index] = normalized
		}
		return result, nil
	}
	if kind != reflect.Map {
		return value, nil
	}
	encoded, err := json.Marshal(value)
	if err != nil || len(encoded) > providerMaximumResultBytes {
		return nil, ErrInvalidWorkspaceProviderRequest
	}
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.UseNumber()
	var normalized any
	if err = decoder.Decode(&normalized); err != nil {
		return nil, ErrInvalidWorkspaceProviderRequest
	}
	return normalizeProviderToolValue(normalized, depth+1)
}

func (provider *GitHubProvider) exactJSON(
	outputs map[string]any,
	limit int,
) ([]byte, error) {
	if outputs == nil || limit <= 0 || limit > providerMaximumResultBytes {
		return nil, errors.New("provider result is invalid")
	}
	if rawTags, present := outputs["artifact_tags"]; present {
		tags, ok := rawTags.([]string)
		if !ok {
			return nil, errors.New("provider artifact tags are invalid")
		}
		if len(tags) > 0 {
			return provider.exactArtifactJSON(tags, limit)
		}
	}
	value, ok := outputs["text"].(string)
	value = strings.TrimSpace(value)
	if !ok || value == "" {
		return nil, errors.New("provider result is not bounded exact JSON")
	}
	if len(value) > limit {
		return nil, errProviderResultLimit
	}
	if !utf8.ValidString(value) || !json.Valid([]byte(value)) {
		return nil, ErrProviderIncompatible
	}
	return []byte(value), nil
}

func (provider *GitHubProvider) exactText(
	outputs map[string]any,
	limit int,
) ([]byte, error) {
	if outputs == nil || limit <= 0 || limit > providerMaximumResultBytes {
		return nil, errors.New("provider result is invalid")
	}
	if rawTags, present := outputs["artifact_tags"]; present {
		tags, ok := rawTags.([]string)
		if !ok {
			return nil, errors.New("provider artifact tags are invalid")
		}
		if len(tags) > 0 {
			return provider.exactArtifactText(tags, limit)
		}
	}
	value, ok := outputs["text"].(string)
	if !ok {
		return nil, errors.New("provider result is not bounded exact text")
	}
	return validateWorkspaceDiff([]byte(value), limit)
}

func (provider *GitHubProvider) exactArtifactText(
	tags []string,
	limit int,
) (raw []byte, returnErr error) {
	artifact, err := provider.acquireExactArtifact(tags)
	if artifact != nil {
		defer func() { returnErr = errors.Join(returnErr, artifact.Consume()) }()
	}
	if err != nil {
		return nil, err
	}
	if artifact.Size > int64(limit) {
		return nil, errProviderResultLimit
	}
	raw, err = readProviderArtifact(artifact, limit)
	if err != nil {
		return nil, err
	}
	return validateWorkspaceDiff(raw, limit)
}

func validateWorkspaceDiff(raw []byte, limit int) ([]byte, error) {
	if len(raw) > limit {
		return nil, errProviderResultLimit
	}
	if !utf8.Valid(raw) || bytes.IndexByte(raw, 0) >= 0 {
		return nil, ErrProviderIncompatible
	}
	if len(bytes.TrimSpace(raw)) != 0 && !bytes.HasPrefix(raw, []byte("diff --git ")) {
		return nil, ErrProviderIncompatible
	}
	return append([]byte(nil), raw...), nil
}

func (provider *GitHubProvider) exactArtifactJSON(
	tags []string,
	limit int,
) (raw []byte, returnErr error) {
	artifact, err := provider.acquireExactArtifact(tags)
	if artifact != nil {
		defer func() { returnErr = errors.Join(returnErr, artifact.Consume()) }()
	}
	if err != nil {
		return nil, err
	}
	if artifact.Size > int64(limit) {
		return nil, errProviderResultLimit
	}
	raw, err = readProviderArtifact(artifact, limit)
	if err != nil {
		return nil, err
	}
	raw = bytes.TrimSpace(raw)
	if !utf8.Valid(raw) || !json.Valid(raw) {
		return nil, ErrProviderIncompatible
	}
	return raw, nil
}

func (provider *GitHubProvider) acquireExactArtifact(
	tags []string,
) (*providerArtifact, error) {
	if provider == nil || len(tags) != 1 || provider.ArtifactRoot == "" {
		return nil, errors.New("provider exact artifact unavailable")
	}
	rawPath, ok := strings.CutPrefix(tags[0], "[file:")
	if !ok || !strings.HasSuffix(rawPath, "]") {
		return nil, errors.New("invalid provider artifact tag")
	}
	return acquireProviderArtifact(
		provider.ArtifactRoot,
		strings.TrimSuffix(rawPath, "]"),
		provider.artifactCleanupHook,
	)
}

func readProviderArtifact(artifact *providerArtifact, limit int) ([]byte, error) {
	if artifact == nil || artifact.File == nil {
		return nil, errors.New("provider artifact cannot be opened safely")
	}
	raw, readErr := io.ReadAll(io.LimitReader(artifact.File, int64(limit)+1))
	closeErr := artifact.File.Close()
	artifact.File = nil
	if readErr != nil || closeErr != nil {
		return nil, errors.New("provider artifact cannot be read safely")
	}
	if len(raw) > limit {
		return nil, errProviderResultLimit
	}
	return raw, nil
}

type providerArtifact struct {
	File    *os.File
	Size    int64
	consume func() error
}

func (artifact *providerArtifact) Consume() error {
	if artifact == nil {
		return nil
	}
	var closeErr error
	if artifact.File != nil {
		closeErr = artifact.File.Close()
		artifact.File = nil
	}
	var cleanupErr error
	if artifact.consume != nil {
		cleanupErr = artifact.consume()
		artifact.consume = nil
	}
	return errors.Join(closeErr, cleanupErr)
}
