package gitworkspace

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

type pinnedLineBrowseTestFixture struct {
	pinnedLineTestFixture
	parked PinnedLineParkResult
	fence  PinnedLineBrowseFence
}

func TestManagerPinnedLineBrowseReadsExactBaseAndCandidateRevisions(t *testing.T) {
	ctx := context.Background()
	fixture := newPinnedLineBrowseTestFixture(t, "pr-development/browse-revisions", 0)

	reads := []struct {
		name     string
		revision string
		want     string
	}{
		{name: "base alias", revision: "base", want: "# repo\n"},
		{name: "base SHA", revision: fixture.parked.PreviousTip, want: "# repo\n"},
		{name: "candidate alias", revision: "candidate", want: "# browsable candidate\n"},
		{name: "candidate SHA", revision: fixture.parked.Tip, want: "# browsable candidate\n"},
	}
	for _, test := range reads {
		t.Run(test.name, func(t *testing.T) {
			blob, err := fixture.manager.ReadPinnedLineBlob(ctx, PinnedLineBlobRequest{
				PinnedLineBrowseFence: fixture.fence,
				Revision:              test.revision,
				Path:                  "README.md",
			})
			if err != nil {
				t.Fatalf("ReadPinnedLineBlob() error = %v", err)
			}
			if blob.Path != "README.md" || blob.Revision != test.revision || blob.Content != test.want {
				t.Fatalf("ReadPinnedLineBlob() = %#v", blob)
			}
		})
	}

	base, err := fixture.manager.ListPinnedLineTree(ctx, PinnedLineTreeRequest{
		PinnedLineBrowseFence: fixture.fence,
		Revision:              fixture.parked.PreviousTip,
	})
	if err != nil {
		t.Fatalf("ListPinnedLineTree(base SHA) error = %v", err)
	}
	candidate, err := fixture.manager.ListPinnedLineTree(ctx, PinnedLineTreeRequest{
		PinnedLineBrowseFence: fixture.fence,
		Revision:              fixture.parked.Tip,
	})
	if err != nil {
		t.Fatalf("ListPinnedLineTree(candidate SHA) error = %v", err)
	}
	if treeHasPath(base, "nested") || !treeHasPath(candidate, "nested") {
		t.Fatalf("base/candidate trees = %#v / %#v", base, candidate)
	}
	nested, err := fixture.manager.ListPinnedLineTree(ctx, PinnedLineTreeRequest{
		PinnedLineBrowseFence: fixture.fence, Revision: fixture.parked.Tip, Path: "nested",
	})
	if err != nil || !treeHasPath(nested, "nested/candidate.txt") {
		t.Fatalf("candidate nested tree = %#v, %v", nested, err)
	}

	for _, revision := range []string{"", strings.Repeat("f", len(fixture.parked.Tip))} {
		if revision == fixture.parked.Tip || revision == fixture.parked.PreviousTip {
			continue
		}
		_, err := fixture.manager.ReadPinnedLineBlob(ctx, PinnedLineBlobRequest{
			PinnedLineBrowseFence: fixture.fence,
			Revision:              revision,
			Path:                  "README.md",
		})
		if !errors.Is(err, ErrPinnedLineInvalid) {
			t.Fatalf("ReadPinnedLineBlob(revision %q) error = %v", revision, err)
		}
	}
}

func TestManagerPinnedLineBrowseTreePaginationAndBounds(t *testing.T) {
	ctx := context.Background()
	fixture := newPinnedLineBrowseTestFixture(
		t,
		"pr-development/browse-pagination",
		maxDevelopmentBrowseEntries+3,
	)

	first, err := fixture.manager.ListPinnedLineTree(ctx, PinnedLineTreeRequest{
		PinnedLineBrowseFence: fixture.fence,
		Revision:              "candidate",
		Path:                  "files",
	})
	if err != nil {
		t.Fatalf("ListPinnedLineTree(first) error = %v", err)
	}
	if len(first.Entries) != maxDevelopmentBrowseEntries || first.Next == "" ||
		first.Next != first.Entries[len(first.Entries)-1].Path {
		t.Fatalf("first page = entries %d, next %q", len(first.Entries), first.Next)
	}
	second, err := fixture.manager.ListPinnedLineTree(ctx, PinnedLineTreeRequest{
		PinnedLineBrowseFence: fixture.fence,
		Revision:              fixture.parked.Tip,
		Path:                  "files",
		After:                 first.Next,
	})
	if err != nil {
		t.Fatalf("ListPinnedLineTree(second) error = %v", err)
	}
	all := append(append([]PinnedLineTreeEntry(nil), first.Entries...), second.Entries...)
	wantCount := maxDevelopmentBrowseEntries + 3
	if len(all) != wantCount || second.Next != "" {
		t.Fatalf("pages contain %d entries with next %q, want %d and terminal cursor", len(all), second.Next, wantCount)
	}
	for index, entry := range all {
		if (entry.Type != "file" && entry.Type != "directory") || !validDevelopmentLineReviewPath(entry.Path) {
			t.Fatalf("entry %d = %#v", index, entry)
		}
		if index > 0 && all[index-1].Path >= entry.Path {
			t.Fatalf("tree order at %d = %q then %q", index, all[index-1].Path, entry.Path)
		}
	}

	prefixed, err := fixture.manager.ListPinnedLineTree(ctx, PinnedLineTreeRequest{
		PinnedLineBrowseFence: fixture.fence,
		Revision:              "candidate",
		Path:                  "nested",
	})
	if err != nil || len(prefixed.Entries) != 1 || prefixed.Entries[0].Path != "nested/candidate.txt" {
		t.Fatalf("ListPinnedLineTree(nested) = %#v, %v", prefixed, err)
	}

	tooLong := strings.Repeat("a", maxDevelopmentLinePathBytes+1)
	if _, err := fixture.manager.ListPinnedLineTree(ctx, PinnedLineTreeRequest{
		PinnedLineBrowseFence: fixture.fence,
		Revision:              "candidate",
		Path:                  tooLong,
	}); !errors.Is(err, ErrPinnedLineInvalid) {
		t.Fatalf("ListPinnedLineTree(oversized path) error = %v", err)
	}
	if _, err := fixture.manager.ListPinnedLineTree(ctx, PinnedLineTreeRequest{
		PinnedLineBrowseFence: fixture.fence,
		Revision:              "candidate",
		Path:                  "nested",
		After:                 "README.md",
	}); !errors.Is(err, ErrPinnedLineInvalid) {
		t.Fatalf("ListPinnedLineTree(cross-directory cursor) error = %v", err)
	}
}

func TestManagerPinnedLineBrowseRejectsUnsafePathsAndStaleFences(t *testing.T) {
	ctx := context.Background()
	fixture := newPinnedLineBrowseTestFixture(t, "pr-development/browse-rejections", 0)
	unsafePaths := []string{
		"../README.md",
		"/README.md",
		"nested/../README.md",
		"nested\nfile.txt",
		"nested\x00file.txt",
	}
	for _, unsafePath := range unsafePaths {
		t.Run("tree "+strings.ReplaceAll(unsafePath, "/", "_"), func(t *testing.T) {
			_, err := fixture.manager.ListPinnedLineTree(ctx, PinnedLineTreeRequest{
				PinnedLineBrowseFence: fixture.fence,
				Revision:              "candidate",
				Path:                  unsafePath,
			})
			if !errors.Is(err, ErrPinnedLineInvalid) {
				t.Fatalf("ListPinnedLineTree(%q) error = %v", unsafePath, err)
			}
		})
		t.Run("blob "+strings.ReplaceAll(unsafePath, "/", "_"), func(t *testing.T) {
			_, err := fixture.manager.ReadPinnedLineBlob(ctx, PinnedLineBlobRequest{
				PinnedLineBrowseFence: fixture.fence,
				Revision:              "candidate",
				Path:                  unsafePath,
			})
			if !errors.Is(err, ErrPinnedLineInvalid) {
				t.Fatalf("ReadPinnedLineBlob(%q) error = %v", unsafePath, err)
			}
		})
	}

	other := func(value string) string {
		if value[0] == 'f' {
			return "e" + value[1:]
		}
		return "f" + value[1:]
	}
	stale := []struct {
		name   string
		mutate func(*PinnedLineBrowseFence)
	}{
		{name: "version", mutate: func(fence *PinnedLineBrowseFence) { fence.ExpectedVersion++ }},
		{name: "base", mutate: func(fence *PinnedLineBrowseFence) { fence.ExpectedBase = other(fence.ExpectedBase) }},
		{name: "tip", mutate: func(fence *PinnedLineBrowseFence) { fence.ExpectedTip = other(fence.ExpectedTip) }},
		{name: "tree", mutate: func(fence *PinnedLineBrowseFence) { fence.ExpectedTree = other(fence.ExpectedTree) }},
	}
	for _, test := range stale {
		t.Run("stale "+test.name, func(t *testing.T) {
			fence := fixture.fence
			test.mutate(&fence)
			_, err := fixture.manager.ReadPinnedLineBlob(ctx, PinnedLineBlobRequest{
				PinnedLineBrowseFence: fence,
				Revision:              "candidate",
				Path:                  "README.md",
			})
			if !errors.Is(err, ErrPinnedLineConflict) {
				t.Fatalf("ReadPinnedLineBlob(stale %s) error = %v", test.name, err)
			}
		})
	}
}

func TestManagerPinnedLineBrowseRejectsSymlinkBinaryAndOversizedBlobs(t *testing.T) {
	ctx := context.Background()
	fixture := newPinnedLineTestFixture(t, "pr-development/browse-blob-kinds")
	if err := os.WriteFile(
		filepath.Join(fixture.workspace.Path, "binary.dat"),
		[]byte{'a', 0, 'b'},
		0o644,
	); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		filepath.Join(fixture.workspace.Path, "oversized.txt"),
		[]byte(strings.Repeat("x", maxDevelopmentBrowseBlobBytes+1)),
		0o644,
	); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("README.md", filepath.Join(fixture.workspace.Path, "readme-link")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	parked := parkPinnedLineBrowseCandidate(t, fixture, "blob_kinds")
	fence := browseFence(parked)

	for _, test := range []struct {
		path    string
		message string
	}{
		{path: "readme-link", message: "symlink"},
		{path: "binary.dat", message: "binary"},
		{path: "oversized.txt", message: "exceeded"},
	} {
		t.Run(test.path, func(t *testing.T) {
			_, err := fixture.manager.ReadPinnedLineBlob(ctx, PinnedLineBlobRequest{
				PinnedLineBrowseFence: fence,
				Revision:              parked.Tip,
				Path:                  test.path,
			})
			if err == nil || !strings.Contains(strings.ToLower(err.Error()), test.message) {
				t.Fatalf("ReadPinnedLineBlob(%q) error = %v", test.path, err)
			}
		})
	}
}

func TestManagerPinnedLineBrowseRejectsSubmoduleBlob(t *testing.T) {
	ctx := context.Background()
	source := initSourceRepo(t)
	base := testGitCommit(t, source, "HEAD")
	if _, err := runGit(ctx, source, "update-index", "--add", "--cacheinfo", "160000,"+base+",nested"); err != nil {
		t.Fatal(err)
	}
	if _, err := runGit(ctx, source, "commit", "-m", "add gitlink"); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 24, 12, 0, 0, 0, time.UTC)
	manager := newTestManager(t, &now)
	pin := PinnedAcquireRequest{
		Repository:     source,
		SourceRef:      "main",
		ExpectedCommit: testGitCommit(t, source, "HEAD"),
		ReservationKey: "pr-development/browse-submodule",
		AgentID:        "main",
	}
	workspace, err := manager.AcquirePinned(ctx, pin)
	if err != nil {
		t.Fatalf("AcquirePinned() error = %v", err)
	}
	tree := testGitObject(t, workspace.Path, "rev-parse", "HEAD^{tree}")
	lease, err := manager.AdoptPinnedLine(ctx, PinnedLineAdoptRequest{
		Pin: pin, WorkspaceID: workspace.ID, LineID: pinnedLineTestID, ExpectedTree: tree,
	})
	if err != nil {
		t.Fatalf("AdoptPinnedLine() error = %v", err)
	}
	parked, err := manager.ParkPinnedLine(ctx, PinnedLineParkRequest{
		Pin: pin, WorkspaceID: workspace.ID, LineID: pinnedLineTestID,
		IntentID: "pdlnpark_browse_submodule", ExpectedVersion: lease.Version,
		MutationEpoch: lease.MutationEpoch, PreviousTip: lease.Tip, Tip: lease.Tip,
		Tree: lease.Tree, NoChanges: true,
	})
	if err != nil {
		t.Fatalf("ParkPinnedLine() error = %v", err)
	}
	_, err = manager.ReadPinnedLineBlob(ctx, PinnedLineBlobRequest{
		PinnedLineBrowseFence: browseFence(parked), Revision: "candidate", Path: "nested",
	})
	if err == nil || !strings.Contains(strings.ToLower(err.Error()), "submodule") {
		t.Fatalf("ReadPinnedLineBlob(submodule) error = %v", err)
	}
}

func TestManagerPinnedLineBrowsePostflightRejectsRetainedRefDrift(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test Git shim requires a POSIX shell")
	}
	ctx := context.Background()
	fixture := newPinnedLineBrowseTestFixture(t, "pr-development/browse-postflight", 0)
	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	realGit, err = filepath.Abs(realGit)
	if err != nil {
		t.Fatal(err)
	}
	shimDirectory := t.TempDir()
	shim := filepath.Join(shimDirectory, "git")
	shimScript := `#!/bin/sh
if [ "$1" = "cat-file" ] && [ "$2" = "blob" ]; then
  "$PICOCLAW_BROWSE_REAL_GIT" "$@"
  status=$?
  if [ "$status" -eq 0 ]; then
    "$PICOCLAW_BROWSE_REAL_GIT" update-ref "$PICOCLAW_BROWSE_REF" "$PICOCLAW_BROWSE_BASE" "$PICOCLAW_BROWSE_TIP" >/dev/null 2>&1
  fi
  exit "$status"
fi
exec "$PICOCLAW_BROWSE_REAL_GIT" "$@"
`
	if writeErr := os.WriteFile(shim, []byte(shimScript), 0o755); writeErr != nil {
		t.Fatal(writeErr)
	}
	t.Setenv("PICOCLAW_BROWSE_REAL_GIT", realGit)
	t.Setenv("PICOCLAW_BROWSE_REF", "refs/heads/"+developmentLineBranch(pinnedLineTestID))
	t.Setenv("PICOCLAW_BROWSE_BASE", fixture.parked.PreviousTip)
	t.Setenv("PICOCLAW_BROWSE_TIP", fixture.parked.Tip)
	t.Setenv("PATH", shimDirectory+string(os.PathListSeparator)+os.Getenv("PATH"))

	_, err = fixture.manager.ReadPinnedLineBlob(ctx, PinnedLineBlobRequest{
		PinnedLineBrowseFence: fixture.fence,
		Revision:              "candidate",
		Path:                  "README.md",
	})
	if !errors.Is(err, ErrPinnedLineConflict) {
		t.Fatalf("ReadPinnedLineBlob(postflight ref drift) error = %v", err)
	}
}

func newPinnedLineBrowseTestFixture(
	t *testing.T,
	reservation string,
	generatedFiles int,
) pinnedLineBrowseTestFixture {
	t.Helper()
	fixture := newPinnedLineTestFixture(t, reservation)
	if err := os.WriteFile(
		filepath.Join(fixture.workspace.Path, "README.md"),
		[]byte("# browsable candidate\n"),
		0o644,
	); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(fixture.workspace.Path, "nested"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		filepath.Join(fixture.workspace.Path, "nested", "candidate.txt"),
		[]byte("candidate\n"),
		0o644,
	); err != nil {
		t.Fatal(err)
	}
	if generatedFiles > 0 {
		directory := filepath.Join(fixture.workspace.Path, "files")
		if err := os.MkdirAll(directory, 0o755); err != nil {
			t.Fatal(err)
		}
		for index := 0; index < generatedFiles; index++ {
			name := filepath.Join(directory, leftPadDecimal(index, 4)+".txt")
			if err := os.WriteFile(name, []byte("bounded\n"), 0o644); err != nil {
				t.Fatal(err)
			}
		}
	}
	parked := parkPinnedLineBrowseCandidate(t, fixture, "ordinary")
	return pinnedLineBrowseTestFixture{
		pinnedLineTestFixture: fixture,
		parked:                parked,
		fence:                 browseFence(parked),
	}
}

func parkPinnedLineBrowseCandidate(
	t *testing.T,
	fixture pinnedLineTestFixture,
	suffix string,
) PinnedLineParkResult {
	t.Helper()
	candidate := fixture.snapshot(t)
	commit, err := fixture.manager.CommitPinned(context.Background(), PinnedCommitRequest{
		Pin: fixture.pin, WorkspaceID: fixture.workspace.ID,
		IntentID:       "pdcmt_bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
		ExpectedParent: candidate.ParentCommit, ExpectedTree: candidate.Tree,
		ExpectedCandidateDigest: candidate.CandidateDigest,
		Message:                 "Prepare browse candidate",
		AuthoredAt:              time.Date(2026, 8, 24, 10, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatalf("CommitPinned() error = %v", err)
	}
	parked, err := fixture.manager.ParkPinnedLine(context.Background(), PinnedLineParkRequest{
		Pin: fixture.pin, WorkspaceID: fixture.workspace.ID, LineID: pinnedLineTestID,
		IntentID: "pdlnpark_browse_" + suffix, ExpectedVersion: fixture.lease.Version,
		MutationEpoch: fixture.lease.MutationEpoch, PreviousTip: fixture.lease.Tip,
		Tip: commit.Commit, Tree: commit.Tree,
	})
	if err != nil {
		t.Fatalf("ParkPinnedLine() error = %v", err)
	}
	return parked
}

func browseFence(parked PinnedLineParkResult) PinnedLineBrowseFence {
	return PinnedLineBrowseFence{
		LineID: pinnedLineTestID, ExpectedVersion: parked.Version,
		ExpectedBase: parked.PreviousTip, ExpectedTip: parked.Tip, ExpectedTree: parked.Tree,
	}
}

func treeHasPath(page PinnedLineTreePage, wanted string) bool {
	for _, entry := range page.Entries {
		if entry.Path == wanted {
			return true
		}
	}
	return false
}

func leftPadDecimal(value, width int) string {
	text := ""
	for value > 0 {
		text = string(rune('0'+value%10)) + text
		value /= 10
	}
	if text == "" {
		text = "0"
	}
	return strings.Repeat("0", width-len(text)) + text
}
