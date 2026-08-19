package gitworkspace

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestManagerSnapshotPinnedCandidateReviewReturnsExactCandidateDiff(t *testing.T) {
	fixture := newPinnedCommitTestFixture(t, "pr-development/candidate-review")
	if err := os.WriteFile(
		filepath.Join(fixture.workspace.Path, "README.md"),
		[]byte("# reviewed repair\n"),
		0o644,
	); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		filepath.Join(fixture.workspace.Path, "new-file.txt"),
		[]byte("candidate evidence\n"),
		0o644,
	); err != nil {
		t.Fatal(err)
	}

	candidate := fixture.snapshot(t)
	request := pinnedValidationRequest(fixture, candidate)
	review, err := fixture.manager.SnapshotPinnedCandidateReview(nil, request)
	if err != nil {
		t.Fatalf("SnapshotPinnedCandidateReview() error = %v", err)
	}
	if want := []string{"README.md", "new-file.txt"}; !reflect.DeepEqual(review.ChangedPaths, want) {
		t.Fatalf("ChangedPaths = %v, want %v", review.ChangedPaths, want)
	}
	for _, fragment := range []string{
		"diff --git a/README.md b/README.md",
		"+# reviewed repair",
		"diff --git a/new-file.txt b/new-file.txt",
		"+candidate evidence",
	} {
		if !strings.Contains(review.UnifiedDiff, fragment) {
			t.Fatalf("UnifiedDiff does not contain %q:\n%s", fragment, review.UnifiedDiff)
		}
	}

	replayed, err := fixture.manager.SnapshotPinnedCandidateReview(context.Background(), request)
	if err != nil {
		t.Fatalf("replayed SnapshotPinnedCandidateReview() error = %v", err)
	}
	if !reflect.DeepEqual(replayed, review) {
		t.Fatalf("replayed review = %#v, want %#v", replayed, review)
	}
}

func TestManagerSnapshotPinnedCandidateReviewRejectsInvalidAuthority(t *testing.T) {
	var manager *Manager
	if _, err := manager.SnapshotPinnedCandidateReview(
		context.Background(),
		PinnedCandidateValidationRequest{},
	); err == nil {
		t.Fatal("nil manager accepted candidate review")
	}

	fixture := newPinnedCommitTestFixture(t, "pr-development/candidate-review-invalid")
	if err := os.WriteFile(
		filepath.Join(fixture.workspace.Path, "README.md"),
		[]byte("# invalid candidate authority\n"),
		0o644,
	); err != nil {
		t.Fatal(err)
	}
	candidate := fixture.snapshot(t)
	request := pinnedValidationRequest(fixture, candidate)
	request.ExpectedCandidateDigest = strings.Repeat("0", len(request.ExpectedCandidateDigest))
	if request.ExpectedCandidateDigest == candidate.CandidateDigest {
		request.ExpectedCandidateDigest = strings.Repeat("1", len(request.ExpectedCandidateDigest))
	}
	if _, err := fixture.manager.SnapshotPinnedCandidateReview(
		context.Background(),
		request,
	); !errors.Is(err, ErrPinnedCommitConflict) {
		t.Fatalf("mismatched candidate digest error = %v, want ErrPinnedCommitConflict", err)
	}
}
