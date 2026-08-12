package reviews

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"sync"
	"testing"

	"github.com/sipeed/picoclaw/pkg/eventing"
	"github.com/sipeed/picoclaw/pkg/workflows"
)

func TestGitHubProviderStatusUsesOnlyCaseScopedPullRead(t *testing.T) {
	runner := &providerTestRunner{}
	provider := newProviderForTest(t, runner, false)

	status, err := provider.Status(context.Background(), providerTestCase())
	if err != nil {
		t.Fatalf("Status() error = %v", err)
	}
	if status.Availability != ProviderAvailabilityAvailable ||
		status.PullRequest == nil || status.PullRequest.Title != "Bounded provider view" ||
		status.Capabilities.ThreadResolution ||
		!reflect.DeepEqual(status.Limitations, []string{providerLimitationStatusView}) {
		t.Fatalf("Status() = %#v", status)
	}
	requests := runner.snapshot()
	if len(requests) != 1 {
		t.Fatalf("tool calls = %d, want 1", len(requests))
	}
	assertProviderTool(t, requests[0], GitHubPullRequestReadTool, map[string]any{
		"method": "get", "owner": "octo", "repo": "repo", "pullNumber": int64(42),
	})
}

func TestGitHubProviderDetectsLegacyStalledReviewsAndMissingThreadIdentity(t *testing.T) {
	runner := &providerTestRunner{stallReviews: true, omitThreadID: true}
	provider := newProviderForTest(t, runner, true)

	snapshot, err := provider.Snapshot(context.Background(), providerTestCase())
	if err != nil {
		t.Fatalf("Snapshot() error = %v", err)
	}
	if snapshot.Availability != ProviderAvailabilityPartial ||
		snapshot.ReviewHistoryComplete || !snapshot.ThreadsComplete ||
		snapshot.Capabilities.ThreadResolution || len(snapshot.Reviews) != 1 ||
		len(snapshot.Threads) != 1 || snapshot.Threads[0].Token != "" ||
		snapshot.Threads[0].CanResolve {
		t.Fatalf("Snapshot() = %#v", snapshot)
	}
	for _, limitation := range []string{
		providerLimitationReviewPaginationStalled,
		providerLimitationThreadIdentity,
	} {
		if !containsProviderLimitation(snapshot.Limitations, limitation) {
			t.Fatalf("limitations = %#v, missing %q", snapshot.Limitations, limitation)
		}
	}
	requests := runner.snapshot()
	if len(requests) != 4 || requests[1].Args["page"] != 1 || requests[2].Args["page"] != 2 {
		t.Fatalf("tool calls = %#v", requests)
	}
}

func TestGitHubProviderAcceptsLegacyUppercaseThreadIDAndReconcilesMutations(t *testing.T) {
	runner := &providerTestRunner{legacyUpperID: true}
	provider := newProviderForTest(t, runner, true)
	reviewCase := providerTestCase()

	initial, err := provider.Snapshot(context.Background(), reviewCase)
	if err != nil {
		t.Fatalf("Snapshot() error = %v", err)
	}
	if initial.Availability != ProviderAvailabilityAvailable ||
		!initial.Capabilities.ThreadResolution || len(initial.Threads) != 1 ||
		!validProviderToken(initial.Threads[0].Token) {
		t.Fatalf("initial snapshot = %#v", initial)
	}
	token := initial.Threads[0].Token

	resolved, err := provider.MutateThread(context.Background(), reviewCase, ProviderThreadMutationRequest{
		CaseID: reviewCase.ID, Token: token, Action: "resolve",
	})
	if err != nil {
		t.Fatalf("MutateThread(resolve) error = %v", err)
	}
	if !resolved.Threads[0].IsResolved || runner.writeCount() != 1 {
		t.Fatalf("resolved snapshot = %#v, writes=%d", resolved, runner.writeCount())
	}
	if _, err := provider.MutateThread(context.Background(), reviewCase, ProviderThreadMutationRequest{
		CaseID: reviewCase.ID, Token: token, Action: "resolve",
	}); err != nil {
		t.Fatalf("idempotent MutateThread(resolve) error = %v", err)
	}
	if runner.writeCount() != 1 {
		t.Fatalf("idempotent writes = %d, want 1", runner.writeCount())
	}

	for _, request := range runner.snapshot() {
		if request.MCPTool != GitHubPullRequestReviewWriteTool {
			continue
		}
		if got := request.Args["threadId"]; got != providerTestThreadID {
			t.Fatalf("write threadId = %#v", got)
		}
		if request.Args["method"] != "resolve_thread" {
			t.Fatalf("write method = %#v", request.Args["method"])
		}
	}
}

func TestGitHubProviderRejectsMismatchedPullAndConflictingThreadIDs(t *testing.T) {
	for _, test := range []struct {
		name   string
		runner *providerTestRunner
	}{
		{name: "pull URL", runner: &providerTestRunner{pullURL: "https://evil.example/octo/repo/pull/42"}},
		{name: "thread IDs", runner: &providerTestRunner{conflictingThreadIDs: true}},
	} {
		t.Run(test.name, func(t *testing.T) {
			provider := newProviderForTest(t, test.runner, true)
			_, err := provider.Snapshot(context.Background(), providerTestCase())
			if !errors.Is(err, ErrProviderIncompatible) {
				t.Fatalf("Snapshot() error = %v, want incompatible", err)
			}
		})
	}
}

func TestGitHubProviderMarksOverlappingReviewPagesIncomplete(t *testing.T) {
	provider := newProviderForTest(t, &providerTestRunner{overlapReviews: true}, false)
	snapshot, err := provider.Snapshot(context.Background(), providerTestCase())
	if err != nil {
		t.Fatalf("Snapshot() error = %v", err)
	}
	if snapshot.ReviewHistoryComplete || snapshot.Availability != ProviderAvailabilityPartial ||
		!containsProviderLimitation(snapshot.Limitations, providerLimitationReviewPaginationOverlap) {
		t.Fatalf("Snapshot() = %#v", snapshot)
	}
}

func TestGitHubProviderPreservesCancellationAndDeadline(t *testing.T) {
	for _, test := range []struct {
		name string
		err  error
	}{
		{name: "canceled", err: context.Canceled},
		{name: "deadline", err: context.DeadlineExceeded},
	} {
		t.Run(test.name, func(t *testing.T) {
			provider := newProviderForTest(t, providerToolRunnerFunc(func(
				context.Context,
				workflows.ToolRequest,
			) (map[string]any, error) {
				return nil, test.err
			}), false)
			_, err := provider.Status(context.Background(), providerTestCase())
			if !errors.Is(err, test.err) {
				t.Fatalf("Status() error = %v, want %v", err, test.err)
			}
		})
	}
}

func TestGitHubProviderSnapshotStaysWithinPublicResponseBudget(t *testing.T) {
	provider := newProviderForTest(t, &providerTestRunner{}, true)
	snapshot, err := provider.Snapshot(context.Background(), providerTestCase())
	if err != nil {
		t.Fatalf("Snapshot() error = %v", err)
	}
	raw, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if len(raw) > providerMaximumPublicBytes {
		t.Fatalf("snapshot bytes = %d, maximum %d", len(raw), providerMaximumPublicBytes)
	}
}

func TestGitHubProviderExactArtifactLifecycle(t *testing.T) {
	requireProviderArtifactLifecycle(t)
	t.Run("success", func(t *testing.T) {
		root := privateProviderArtifactRoot(t)
		path := filepath.Join(root, "provider.json")
		writeProviderArtifact(t, path, `{"ok":true}`)
		provider := &GitHubProvider{ArtifactRoot: root}
		raw, err := provider.exactArtifactJSON(providerArtifactTag(path), 1024)
		if err != nil || string(raw) != `{"ok":true}` {
			t.Fatalf("exactArtifactJSON() = %q, %v", raw, err)
		}
		assertProviderArtifactRemoved(t, path)
	})

	t.Run("oversize", func(t *testing.T) {
		root := privateProviderArtifactRoot(t)
		path := filepath.Join(root, "oversize.json")
		writeProviderArtifact(t, path, `{"value":"`+strings.Repeat("x", 256)+`"}`)
		provider := &GitHubProvider{ArtifactRoot: root}
		_, err := provider.exactArtifactJSON(providerArtifactTag(path), 32)
		if !errors.Is(err, errProviderResultLimit) {
			t.Fatalf("exactArtifactJSON() error = %v, want result limit", err)
		}
		assertProviderArtifactRemoved(t, path)
	})

	t.Run("invalid JSON", func(t *testing.T) {
		root := privateProviderArtifactRoot(t)
		path := filepath.Join(root, "invalid.json")
		writeProviderArtifact(t, path, `{"broken":`)
		provider := &GitHubProvider{ArtifactRoot: root}
		_, err := provider.exactArtifactJSON(providerArtifactTag(path), 1024)
		if !errors.Is(err, ErrProviderIncompatible) {
			t.Fatalf("exactArtifactJSON() error = %v, want incompatible", err)
		}
		assertProviderArtifactRemoved(t, path)
	})
}

func TestGitHubProviderExactArtifactNeverDeletesUnownedOrChangedTargets(t *testing.T) {
	requireProviderArtifactLifecycle(t)
	t.Run("outside root", func(t *testing.T) {
		root := privateProviderArtifactRoot(t)
		outside := filepath.Join(t.TempDir(), "outside.json")
		writeProviderArtifact(t, outside, `{"outside":true}`)
		provider := &GitHubProvider{ArtifactRoot: root}
		if _, err := provider.exactArtifactJSON(providerArtifactTag(outside), 1024); err == nil {
			t.Fatal("exactArtifactJSON() accepted an outside artifact")
		}
		assertProviderArtifactContents(t, outside, `{"outside":true}`)
	})

	t.Run("symlink", func(t *testing.T) {
		root := privateProviderArtifactRoot(t)
		target := filepath.Join(t.TempDir(), "target.json")
		writeProviderArtifact(t, target, `{"target":true}`)
		link := filepath.Join(root, "link.json")
		if err := os.Symlink(target, link); err != nil {
			t.Skipf("create symlink: %v", err)
		}
		provider := &GitHubProvider{ArtifactRoot: root}
		if _, err := provider.exactArtifactJSON(providerArtifactTag(link), 1024); err == nil {
			t.Fatal("exactArtifactJSON() accepted a symlink")
		}
		assertProviderArtifactContents(t, target, `{"target":true}`)
		if info, err := os.Lstat(link); err != nil || info.Mode()&os.ModeSymlink == 0 {
			t.Fatalf("rejected symlink changed: info=%v err=%v", info, err)
		}
	})

	t.Run("path replacement race", func(t *testing.T) {
		root := privateProviderArtifactRoot(t)
		path := filepath.Join(root, "provider.json")
		moved := filepath.Join(root, "original.json")
		writeProviderArtifact(t, path, `{"original":true}`)
		provider := &GitHubProvider{ArtifactRoot: root}
		provider.artifactCleanupHook = func(cleanupPath string) {
			provider.artifactCleanupHook = nil
			if err := os.Rename(cleanupPath, moved); err != nil {
				t.Fatalf("move validated artifact: %v", err)
			}
			writeProviderArtifact(t, cleanupPath, `{"replacement":true}`)
		}
		_, err := provider.exactArtifactJSON(providerArtifactTag(path), 1024)
		if err == nil || !strings.Contains(err.Error(), "changed before cleanup") {
			t.Fatalf("exactArtifactJSON() error = %v, want race rejection", err)
		}
		assertProviderArtifactContents(t, path, `{"replacement":true}`)
		assertProviderArtifactContents(t, moved, `{"original":true}`)
	})
}

func TestGitHubProviderClassifiesMalformedInlineJSONAsIncompatible(t *testing.T) {
	provider := newProviderForTest(t, providerToolRunnerFunc(func(
		context.Context,
		workflows.ToolRequest,
	) (map[string]any, error) {
		return map[string]any{"text": `{"broken":`}, nil
	}), false)
	_, err := provider.Status(context.Background(), providerTestCase())
	if !errors.Is(err, ErrProviderIncompatible) {
		t.Fatalf("Status() error = %v, want incompatible", err)
	}
}

func TestGitHubProviderRejectsContradictoryThreadCountsBeforeProjection(t *testing.T) {
	zero := 0
	for _, test := range []struct {
		name   string
		runner *providerTestRunner
	}{
		{name: "thread total undercounts returned threads", runner: &providerTestRunner{threadListTotal: &zero}},
		{name: "thread exceeds connector comment bound", runner: &providerTestRunner{threadCommentCount: providerMaximumCommentsPerThread + 1}},
	} {
		t.Run(test.name, func(t *testing.T) {
			provider := newProviderForTest(t, test.runner, false)
			_, err := provider.Snapshot(context.Background(), providerTestCase())
			if !errors.Is(err, ErrProviderIncompatible) {
				t.Fatalf("Snapshot() error = %v, want incompatible", err)
			}
		})
	}
}

func TestGitHubProviderRejectsContradictoryThreadCountBeforeAggregateTruncation(t *testing.T) {
	baseRunner := &providerTestRunner{}
	runner := providerToolRunnerFunc(func(
		ctx context.Context,
		request workflows.ToolRequest,
	) (map[string]any, error) {
		method, _ := request.Args["method"].(string)
		if method != "get_review_comments" {
			return baseRunner.RunTool(ctx, request)
		}
		commentCounts := []int{100, 100, 100, 100, 100, 100, 100, 100, 100, 99, 100}
		threads := make([]any, 0, len(commentCounts))
		for threadIndex, commentCount := range commentCounts {
			comments := make([]any, 0, commentCount)
			for commentIndex := 0; commentIndex < commentCount; commentIndex++ {
				comments = append(comments, map[string]any{
					"body": "Thread body", "path": "pkg/store.go", "line": 72,
				})
			}
			totalCount := commentCount
			if threadIndex == len(commentCounts)-1 {
				totalCount = 50
			}
			threads = append(threads, map[string]any{
				"id":           providerTestThreadID + strings.Repeat("x", threadIndex),
				"is_resolved":  false,
				"is_outdated":  false,
				"is_collapsed": false,
				"total_count":  totalCount,
				"comments":     comments,
			})
		}
		raw, err := json.Marshal(map[string]any{
			"review_threads": threads,
			"totalCount":     len(threads),
			"pageInfo":       map[string]any{"hasNextPage": false},
		})
		if err != nil {
			return nil, err
		}
		return map[string]any{"text": string(raw)}, nil
	})
	provider := newProviderForTest(t, runner, false)

	_, err := provider.Snapshot(context.Background(), providerTestCase())
	if !errors.Is(err, ErrProviderIncompatible) {
		t.Fatalf("Snapshot() error = %v, want incompatible", err)
	}
}

const providerTestThreadID = "PRRT_kwDOProviderTest"

type providerTestRunner struct {
	mu                   sync.Mutex
	requests             []workflows.ToolRequest
	stallReviews         bool
	overlapReviews       bool
	omitThreadID         bool
	legacyUpperID        bool
	conflictingThreadIDs bool
	threadListTotal      *int
	threadCommentCount   int
	pullURL              string
	resolved             bool
	writes               int
}

func (runner *providerTestRunner) RunTool(
	_ context.Context,
	request workflows.ToolRequest,
) (map[string]any, error) {
	runner.mu.Lock()
	defer runner.mu.Unlock()
	runner.requests = append(runner.requests, cloneProviderRequest(request))
	if request.MCPTool == GitHubPullRequestReviewWriteTool {
		action, _ := request.Args["method"].(string)
		if action == "resolve_thread" {
			runner.resolved = true
		}
		if action == "unresolve_thread" {
			runner.resolved = false
		}
		runner.writes++
		return map[string]any{"text": "review thread updated"}, nil
	}
	if request.MCPTool != GitHubPullRequestReadTool {
		return nil, errors.New("unexpected tool")
	}
	method, _ := request.Args["method"].(string)
	var response any
	switch method {
	case "get":
		pullURL := runner.pullURL
		if pullURL == "" {
			pullURL = "https://github.com/octo/repo/pull/42"
		}
		response = map[string]any{
			"number": 42, "title": "Bounded provider view", "state": "open",
			"draft": false, "merged": false, "html_url": pullURL,
			"user":       map[string]any{"login": "author"},
			"base":       map[string]any{"repo": map[string]any{"full_name": "octo/repo"}},
			"updated_at": "2026-08-12T12:00:00Z",
			"body":       "an intentionally ignored provider field",
		}
	case "get_reviews":
		page := request.Args["page"]
		if page == 2 && !runner.stallReviews && !runner.overlapReviews ||
			page == 3 && runner.overlapReviews {
			response = []any{}
		} else {
			body := "Review body"
			if page == 2 && runner.overlapReviews {
				body = "Same review returned on an overlapping second page"
			}
			response = []any{map[string]any{
				"id": 101, "state": "COMMENTED", "body": body,
				"html_url": "https://github.com/octo/repo/pull/42#pullrequestreview-101",
				"user":     map[string]any{"login": "reviewer"}, "commit_id": strings.Repeat("a", 40),
				"submitted_at": "2026-08-12T11:00:00Z",
			}}
		}
	case "get_review_comments":
		commentCount := runner.threadCommentCount
		if commentCount == 0 {
			commentCount = 1
		}
		comments := make([]any, 0, commentCount)
		for index := 0; index < commentCount; index++ {
			comments = append(comments, map[string]any{
				"body": "Thread body", "path": "pkg/store.go", "line": 72,
				"author": "reviewer", "created_at": "2026-08-12T11:01:00Z",
				"html_url": "https://github.com/octo/repo/pull/42#discussion_r1",
			})
		}
		thread := map[string]any{
			"is_resolved": runner.resolved, "is_outdated": false, "is_collapsed": false,
			"total_count": commentCount, "comments": comments,
		}
		if !runner.omitThreadID {
			if runner.legacyUpperID {
				thread["ID"] = providerTestThreadID
			} else {
				thread["id"] = providerTestThreadID
			}
		}
		if runner.conflictingThreadIDs {
			thread["id"] = providerTestThreadID
			thread["ID"] = providerTestThreadID + "other"
		}
		threadTotal := 1
		if runner.threadListTotal != nil {
			threadTotal = *runner.threadListTotal
		}
		response = map[string]any{
			"review_threads": []any{thread}, "totalCount": threadTotal,
			"pageInfo": map[string]any{"hasNextPage": false},
		}
	default:
		return nil, errors.New("unexpected read method")
	}
	raw, err := json.Marshal(response)
	if err != nil {
		return nil, err
	}
	return map[string]any{"text": string(raw)}, nil
}

func (runner *providerTestRunner) snapshot() []workflows.ToolRequest {
	runner.mu.Lock()
	defer runner.mu.Unlock()
	result := make([]workflows.ToolRequest, len(runner.requests))
	copy(result, runner.requests)
	return result
}

func (runner *providerTestRunner) writeCount() int {
	runner.mu.Lock()
	defer runner.mu.Unlock()
	return runner.writes
}

func newProviderForTest(t *testing.T, runner workflows.ToolRunner, write bool) *GitHubProvider {
	t.Helper()
	provider, err := NewGitHubProvider(runner, "", write)
	if err != nil {
		t.Fatal(err)
	}
	return provider
}

func providerTestCase() eventing.ReviewCase {
	return eventing.ReviewCase{
		ID:         "prc_11111111111111111111111111111111",
		Connector:  "github-main",
		Repository: "octo/repo",
		PullNumber: 42,
		PullURL:    "https://github.com/octo/repo/pull/42",
	}
}

func cloneProviderRequest(request workflows.ToolRequest) workflows.ToolRequest {
	copyRequest := request
	copyRequest.Args = map[string]any{}
	for k, v := range request.Args {
		copyRequest.Args[k] = v
	}
	return copyRequest
}

func assertProviderTool(t *testing.T, request workflows.ToolRequest, tool string, args map[string]any) {
	t.Helper()
	if request.Name != "mcp_github_"+tool || request.MCPServer != "github" || request.MCPTool != tool || !request.MCP ||
		!reflect.DeepEqual(request.Args, args) {
		t.Fatalf("tool request = %#v", request)
	}
}

func containsProviderLimitation(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func providerArtifactTag(path string) []string {
	return []string{"[file:" + path + "]"}
}

func privateProviderArtifactRoot(t *testing.T) string {
	t.Helper()
	root := filepath.Join(t.TempDir(), "private")
	if err := os.Mkdir(root, 0o700); err != nil {
		t.Fatalf("create private provider artifact root: %v", err)
	}
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatalf("secure private provider artifact root: %v", err)
	}
	return root
}

func requireProviderArtifactLifecycle(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" || runtime.GOOS == "js" || runtime.GOOS == "plan9" {
		t.Skip("safe provider artifact consumption is unavailable on this platform")
	}
}

func writeProviderArtifact(t *testing.T, path string, contents string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatalf("write provider artifact: %v", err)
	}
}

func assertProviderArtifactRemoved(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("consumed provider artifact still exists or stat failed: %v", err)
	}
}

func assertProviderArtifactContents(t *testing.T, path string, want string) {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read provider artifact: %v", err)
	}
	if string(raw) != want {
		t.Fatalf("provider artifact = %q, want %q", raw, want)
	}
}

type providerToolRunnerFunc func(
	context.Context,
	workflows.ToolRequest,
) (map[string]any, error)

func (run providerToolRunnerFunc) RunTool(
	ctx context.Context,
	request workflows.ToolRequest,
) (map[string]any, error) {
	return run(ctx, request)
}
