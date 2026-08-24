package prworkspace

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

const (
	codeBrowseBaseCommit    = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	codeBrowseTargetBase    = "dddddddddddddddddddddddddddddddddddddddd"
	codeBrowseCandidate     = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	codeBrowseTree          = "cccccccccccccccccccccccccccccccccccccccc"
	codeBrowseWorkspaceID   = "devw_11111111111111111111111111111111"
	codeBrowseRepairID      = "pra_22222222222222222222222222222222"
	codeBrowseStageID       = "psr_33333333333333333333333333333333"
	codeBrowseCandidateDiff = "diff --git a/pkg/a.go b/pkg/a.go\n--- a/pkg/a.go\n+++ b/pkg/a.go\n@@ -1 +1 @@\n-old-a\n+new-a\n" +
		"diff --git a/pkg/b.go b/pkg/b.go\n--- a/pkg/b.go\n+++ b/pkg/b.go\n@@ -1 +1 @@\n-old-b\n+new-b\n"
)

type codeTreeCall struct {
	revision string
	path     string
	after    string
	repair   RepairAttempt
}

type codeBlobCall struct {
	revision string
	path     string
	repair   RepairAttempt
}

type candidateCodeBrowserStub struct {
	evidence     CandidateEvidence
	evidenceErr  error
	tree         CodeTreePage
	treeErr      error
	blobs        map[string]CodeBlob
	blobErrors   map[string]error
	treeCalls    []codeTreeCall
	blobCalls    []codeBlobCall
	evidenceCall []RepairAttempt
}

func (stub *candidateCodeBrowserStub) LoadCandidateEvidence(
	_ context.Context,
	repair RepairAttempt,
) (CandidateEvidence, error) {
	stub.evidenceCall = append(stub.evidenceCall, repair)
	return stub.evidence, stub.evidenceErr
}

func (stub *candidateCodeBrowserStub) ListCodeTree(
	_ context.Context,
	repair RepairAttempt,
	revision, path, after string,
) (CodeTreePage, error) {
	stub.treeCalls = append(stub.treeCalls, codeTreeCall{
		revision: revision, path: path, after: after, repair: repair,
	})
	return stub.tree, stub.treeErr
}

func (stub *candidateCodeBrowserStub) ReadCodeBlob(
	_ context.Context,
	repair RepairAttempt,
	revision, path string,
) (CodeBlob, error) {
	stub.blobCalls = append(stub.blobCalls, codeBlobCall{
		revision: revision, path: path, repair: repair,
	})
	key := revision + "\x00" + path
	if err := stub.blobErrors[key]; err != nil {
		return CodeBlob{}, err
	}
	if strings.Contains(path, "..") || strings.HasPrefix(path, "/") {
		return CodeBlob{}, errors.New("unsafe path")
	}
	blob, ok := stub.blobs[key]
	if !ok {
		return CodeBlob{}, errors.New("blob unavailable")
	}
	return blob, nil
}

func TestDevelopmentCodeBrowserBindsCandidateBaseAndDiffEvidence(t *testing.T) {
	service, aggregate, browser := seededCodeBrowserService(t)
	ctx := context.Background()

	tree, err := service.ListCodeTree(
		ctx, aggregate.Workspace.ID, codeBrowseCandidate, "pkg", "pkg/a.go",
	)
	if err != nil {
		t.Fatal(err)
	}
	if tree.Revision != codeBrowseCandidate || tree.Next != "pkg/next.go" || len(tree.Entries) != 500 {
		t.Fatalf("tree = %#v", tree)
	}
	if len(browser.treeCalls) != 1 || browser.treeCalls[0].revision != codeBrowseCandidate ||
		browser.treeCalls[0].path != "pkg" || browser.treeCalls[0].after != "pkg/a.go" ||
		browser.treeCalls[0].repair.CandidateSHA != codeBrowseCandidate ||
		browser.treeCalls[0].repair.PublicationFence == nil ||
		browser.treeCalls[0].repair.PublicationFence.BaseCommit != codeBrowseBaseCommit {
		t.Fatalf("tree call = %#v", browser.treeCalls)
	}

	candidate, err := service.ReadCodeBlob(
		ctx, aggregate.Workspace.ID, codeBrowseCandidate, "", "pkg/a.go",
	)
	if err != nil || candidate.Content != "new-a\n" {
		t.Fatalf("candidate blob = %#v, %v", candidate, err)
	}
	beforeCalls := len(browser.blobCalls)
	if _, err = service.ReadCodeBlob(
		ctx, aggregate.Workspace.ID, "base", "stale-candidate", "pkg/a.go",
	); !errors.Is(err, ErrConflict) {
		t.Fatalf("mismatched base pair error = %v", err)
	}
	if len(browser.blobCalls) != beforeCalls {
		t.Fatalf("mismatched base reached browser: %#v", browser.blobCalls[beforeCalls:])
	}
	base, err := service.ReadCodeBlob(
		ctx, aggregate.Workspace.ID, "base", codeBrowseCandidate, "pkg/a.go",
	)
	if err != nil || base.Content != "old-a\n" {
		t.Fatalf("base blob = %#v, %v", base, err)
	}

	diff, err := service.ReadCodeDiff(
		ctx, aggregate.Workspace.ID, codeBrowseCandidate, "pkg/a.go",
	)
	if err != nil {
		t.Fatal(err)
	}
	if diff.BaseRevision != codeBrowseBaseCommit || diff.BaseRevision == codeBrowseTargetBase ||
		diff.CandidateRevision != codeBrowseCandidate || diff.Revision != "sha256:evidence" ||
		diff.Original == nil || *diff.Original != "old-a\n" ||
		diff.Modified == nil || *diff.Modified != "new-a\n" ||
		!strings.Contains(diff.UnifiedDiff, "a/pkg/a.go") ||
		strings.Contains(diff.UnifiedDiff, "a/pkg/b.go") {
		t.Fatalf("path diff = %#v", diff)
	}
	if len(browser.evidenceCall) != 1 || browser.evidenceCall[0].ID != codeBrowseRepairID {
		t.Fatalf("evidence calls = %#v", browser.evidenceCall)
	}
	if _, err = service.ReadCodeDiff(
		ctx, aggregate.Workspace.ID, "stale-candidate", "pkg/a.go",
	); err == nil || len(browser.evidenceCall) != 1 {
		t.Fatalf("stale diff error/calls = %v / %#v", err, browser.evidenceCall)
	}
}

func TestDevelopmentCodeDiffKeepsUnifiedFallbackWhenBlobSideUnavailable(t *testing.T) {
	service, aggregate, browser := seededCodeBrowserService(t)
	browser.blobErrors[codeBrowseBaseCommit+"\x00pkg/a.go"] = errors.New("base removed")

	diff, err := service.ReadCodeDiff(
		context.Background(), aggregate.Workspace.ID, codeBrowseCandidate, "pkg/a.go",
	)
	if err != nil {
		t.Fatal(err)
	}
	if diff.Original != nil || diff.Modified == nil || *diff.Modified != "new-a\n" ||
		diff.UnifiedDiff == "" {
		t.Fatalf("fallback diff = %#v", diff)
	}

	browser.blobErrors[codeBrowseCandidate+"\x00pkg/a.go"] = errors.New("candidate removed")
	diff, err = service.ReadCodeDiff(
		context.Background(), aggregate.Workspace.ID, codeBrowseCandidate, "pkg/a.go",
	)
	if err != nil {
		t.Fatal(err)
	}
	if diff.Original != nil || diff.Modified != nil || diff.UnifiedDiff == "" {
		t.Fatalf("unified-only fallback = %#v", diff)
	}
}

func TestDevelopmentCodeHTTPRoutesFenceAndBoundPublicResults(t *testing.T) {
	service, aggregate, browser := seededCodeBrowserService(t)
	handler, err := NewHTTPHandler(HTTPConfig{Service: service})
	if err != nil {
		t.Fatal(err)
	}
	basePath := RuntimeRoutePrefix + "/" + aggregate.Workspace.ID + "/code/"

	t.Run("tree pagination", func(t *testing.T) {
		query := url.Values{
			"revision": {codeBrowseCandidate}, "path": {"pkg"}, "cursor": {"pkg/a.go"},
		}
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, basePath+"tree?"+query.Encode(), nil))
		if recorder.Code != http.StatusOK {
			t.Fatalf("status = %d body=%s", recorder.Code, recorder.Body.String())
		}
		var page CodeTreePage
		if err := json.Unmarshal(recorder.Body.Bytes(), &page); err != nil {
			t.Fatal(err)
		}
		if len(page.Entries) != 500 || page.Next != "pkg/next.go" {
			t.Fatalf("page = entries %d next %q", len(page.Entries), page.Next)
		}
	})

	t.Run("base candidate pair", func(t *testing.T) {
		withoutFence := httptest.NewRecorder()
		handler.ServeHTTP(withoutFence, httptest.NewRequest(
			http.MethodGet, basePath+"blob?revision=base&path=pkg%2Fa.go", nil,
		))
		if withoutFence.Code != http.StatusConflict {
			t.Fatalf("missing candidate fence status = %d body=%s", withoutFence.Code, withoutFence.Body.String())
		}

		query := url.Values{
			"revision": {"base"}, "candidate_revision": {codeBrowseCandidate}, "path": {"pkg/a.go"},
		}
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, basePath+"blob?"+query.Encode(), nil))
		if recorder.Code != http.StatusOK {
			t.Fatalf("status = %d body=%s", recorder.Code, recorder.Body.String())
		}
		var blob CodeBlob
		if err := json.Unmarshal(recorder.Body.Bytes(), &blob); err != nil {
			t.Fatal(err)
		}
		if blob.Content != "old-a\n" || blob.Truncated {
			t.Fatalf("blob = %#v", blob)
		}
	})

	t.Run("split diff", func(t *testing.T) {
		query := url.Values{"revision": {codeBrowseCandidate}, "path": {"pkg/a.go"}}
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, basePath+"diff?"+query.Encode(), nil))
		if recorder.Code != http.StatusOK {
			t.Fatalf("status = %d body=%s", recorder.Code, recorder.Body.String())
		}
		var diff CodeDiff
		if err := json.Unmarshal(recorder.Body.Bytes(), &diff); err != nil {
			t.Fatal(err)
		}
		if diff.BaseRevision != codeBrowseBaseCommit || diff.CandidateRevision != codeBrowseCandidate ||
			diff.Original == nil || diff.Modified == nil || diff.UnifiedDiff == "" {
			t.Fatalf("diff = %#v", diff)
		}
	})

	t.Run("rejects unsafe and unknown query", func(t *testing.T) {
		unsafe := httptest.NewRecorder()
		query := url.Values{"revision": {codeBrowseCandidate}, "path": {"../secret"}}
		handler.ServeHTTP(unsafe, httptest.NewRequest(http.MethodGet, basePath+"blob?"+query.Encode(), nil))
		if unsafe.Code != http.StatusConflict || strings.Contains(unsafe.Body.String(), "unsafe path") {
			t.Fatalf("unsafe response = %d %s", unsafe.Code, unsafe.Body.String())
		}

		unknown := httptest.NewRecorder()
		handler.ServeHTTP(unknown, httptest.NewRequest(
			http.MethodGet, basePath+"tree?revision="+codeBrowseCandidate+"&private=true", nil,
		))
		if unknown.Code != http.StatusBadRequest {
			t.Fatalf("unknown query status = %d body=%s", unknown.Code, unknown.Body.String())
		}
	})

	for _, path := range []string{"link", "submodule", "binary", "oversized"} {
		t.Run(path+" failure is bounded", func(t *testing.T) {
			browser.blobErrors[codeBrowseCandidate+"\x00pkg/"+path] = fmt.Errorf("private %s detail", path)
			query := url.Values{"revision": {codeBrowseCandidate}, "path": {"pkg/" + path}}
			recorder := httptest.NewRecorder()
			handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, basePath+"blob?"+query.Encode(), nil))
			if recorder.Code != http.StatusConflict || strings.Contains(recorder.Body.String(), "private") {
				t.Fatalf("response = %d %s", recorder.Code, recorder.Body.String())
			}
		})
	}
}

func TestDevelopmentCodeBrowserRequiresBrowsableRepairAndCapability(t *testing.T) {
	store := NewMemoryStore()
	created, err := store.Create(context.Background(), testCreateInput())
	if err != nil {
		t.Fatal(err)
	}
	service, err := NewService(ServiceConfig{Store: store, CandidateEvidence: fixedCandidateEvidenceLoader{}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = service.ListCodeTree(
		context.Background(), created.Aggregate.Workspace.ID, "candidate", "", "",
	); err == nil {
		t.Fatal("tree read succeeded without browser capability and retained repair")
	}

	service.candidateEvidence = &candidateCodeBrowserStub{}
	if _, err = service.ReadCodeBlob(
		context.Background(), created.Aggregate.Workspace.ID, "candidate", "", "pkg/a.go",
	); err == nil {
		t.Fatal("blob read succeeded without retained repair")
	}
}

func seededCodeBrowserService(
	t *testing.T,
) (*Service, Aggregate, *candidateCodeBrowserStub) {
	t.Helper()
	now := time.Date(2026, 8, 24, 12, 0, 0, 0, time.UTC)
	input := testCreateInput()
	input.Workspace.ID = codeBrowseWorkspaceID
	input.Workspace.ProviderHeadSHA = codeBrowseBaseCommit
	input.Provider.BaseSHA = codeBrowseTargetBase
	input.Provider.HeadSHA = codeBrowseBaseCommit
	store := NewMemoryStore()
	created, err := store.Create(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	repair := RepairAttempt{
		ID: codeBrowseRepairID, StageRunID: codeBrowseStageID, Number: 1,
		State: ExecutionSucceeded, CandidateSHA: codeBrowseCandidate,
		Scope: ScopeAssessment{
			Distance: ScopeExact, Size: ChangeSizeXS, Presence: WorkCandidatePresent,
			Files: 2, SemanticLines: 4, Modules: 1, TypeCompatible: true, Confidence: 1,
		},
		PromptDigest: "sha256:repair", ScopePromptDigest: "sha256:scope",
		StartedAt: now, FinishedAt: &now,
		PublicationFence: &ImplementationPublicationFence{
			GitWorkspaceID: "gws_11111111111111111111111111111111",
			LineID:         "gln_11111111111111111111111111111111", LineVersion: 2,
			MutationEpoch: 1, ParkIntentID: "park_11111111111111111111111111111111",
			BaseCommit: codeBrowseBaseCommit, Tip: codeBrowseCandidate, Tree: codeBrowseTree,
		},
	}
	mutated, err := store.Mutate(context.Background(), Mutation{
		WorkspaceID: created.Aggregate.Workspace.ID, ExpectedVersion: created.Aggregate.Workspace.Version,
		RequestID: "request-code-browser-seed", Patch: AggregatePatch{AppendRepairs: []RepairAttempt{repair}},
	})
	if err != nil {
		t.Fatal(err)
	}
	entries := make([]CodeTreeEntry, 500)
	for index := range entries {
		entries[index] = CodeTreeEntry{Path: fmt.Sprintf("pkg/file-%03d.go", index), Type: "file"}
	}
	browser := &candidateCodeBrowserStub{
		evidence: CandidateEvidence{
			CandidateSHA: codeBrowseCandidate, CandidateDiff: codeBrowseCandidateDiff,
			EvidenceDigest: "sha256:evidence",
		},
		tree: CodeTreePage{
			Revision: codeBrowseCandidate, Entries: entries, Next: "pkg/next.go",
		},
		blobs: map[string]CodeBlob{
			"base\x00pkg/a.go": {
				Revision: codeBrowseCandidate, Path: "pkg/a.go", Content: "old-a\n",
			},
			codeBrowseBaseCommit + "\x00pkg/a.go": {
				Revision: codeBrowseCandidate, Path: "pkg/a.go", Content: "old-a\n",
			},
			codeBrowseCandidate + "\x00pkg/a.go": {
				Revision: codeBrowseCandidate, Path: "pkg/a.go", Content: "new-a\n",
			},
		},
		blobErrors: make(map[string]error),
	}
	service, err := NewService(ServiceConfig{Store: store, CandidateEvidence: browser})
	if err != nil {
		t.Fatal(err)
	}
	return service, mutated.Aggregate, browser
}
