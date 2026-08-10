package gitworkspace

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestManagerResumeAppliedCommitSuspensionCrashMatrix(t *testing.T) {
	ctx := context.Background()
	tests := []struct {
		name        string
		parentHEAD  bool
		parentIndex bool
	}{
		{name: "prepared head prepared index"},
		{name: "prepared head parent index", parentIndex: true},
		{name: "parent head prepared index", parentHEAD: true},
		{name: "parent head parent index", parentHEAD: true, parentIndex: true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			suffix := strings.ReplaceAll(test.name, " ", "-")
			fixture, suspended, path := suspensionMatrixAppliedFixture(
				t,
				"pr-development/suspension-matrix-"+suffix,
				"pdlnsuspend_matrix_"+strings.ReplaceAll(test.name, " ", "_"),
			)

			if test.parentHEAD {
				if _, err := runGit(
					ctx,
					fixture.workspace.Path,
					"update-ref",
					"--no-deref",
					"HEAD",
					suspended.Tip,
					suspended.PreparedCommit,
				); err != nil {
					t.Fatalf("install parent HEAD: %v", err)
				}
			}
			if test.parentIndex {
				if _, err := runGit(
					ctx,
					fixture.workspace.Path,
					"read-tree",
					"--reset",
					suspended.Tip,
				); err != nil {
					t.Fatalf("install parent index: %v", err)
				}
			}

			wantHead := suspended.PreparedCommit
			if test.parentHEAD {
				wantHead = suspended.Tip
			}
			wantIndex := suspended.PreparedTree
			if test.parentIndex {
				wantIndex = suspended.Tree
			}
			if head := testGitCommit(t, fixture.workspace.Path, "HEAD"); head != wantHead {
				t.Fatalf("pre-resume HEAD = %q, want %q", head, wantHead)
			}
			if index := testGitObject(t, fixture.workspace.Path, "write-tree"); index != wantIndex {
				t.Fatalf("pre-resume index = %q, want %q", index, wantIndex)
			}

			resumed, err := fixture.manager.ResumeSuspendedPinnedLine(
				ctx,
				suspensionAPITestResumeRequest(
					fixture,
					suspended,
					"pr-development/suspension-matrix-fresh-"+suffix,
					"pdlnresume_matrix_"+strings.ReplaceAll(test.name, " ", "_"),
				),
			)
			if err != nil {
				t.Fatalf("ResumeSuspendedPinnedLine() error = %v", err)
			}
			if resumed.CandidateTree != suspended.CandidateTree ||
				resumed.CandidateDigest != suspended.CandidateDigest ||
				resumed.ChangedFileCount != suspended.ChangedFileCount ||
				resumed.Version != suspended.Version ||
				resumed.MutationEpoch != suspended.MutationEpoch ||
				resumed.RotationHash == "" || resumed.AlreadyResumed {
				t.Fatalf("ResumeSuspendedPinnedLine() = %#v", resumed)
			}
			if head := testGitCommit(t, fixture.workspace.Path, "HEAD"); head != suspended.Tip {
				t.Fatalf("resumed HEAD = %q, want %q", head, suspended.Tip)
			}
			if index := testGitObject(t, fixture.workspace.Path, "write-tree"); index != suspended.Tree {
				t.Fatalf("resumed index = %q, want %q", index, suspended.Tree)
			}
			content, readErr := os.ReadFile(path)
			if readErr != nil || string(content) != "prepared repair plus review fix\n" {
				t.Fatalf("retained content = %q, %v", content, readErr)
			}
			if status := suspensionAPITestStatus(t, fixture.workspace.Path); status != "?? prepared.txt\n" {
				t.Fatalf("resumed status = %q", status)
			}
		})
	}
}

func TestManagerResumeAppliedCommitSuspensionRejectsUnrelatedGitForms(t *testing.T) {
	t.Run("unrelated HEAD", func(t *testing.T) {
		ctx := context.Background()
		fixture, suspended, _ := suspensionMatrixAppliedFixture(
			t,
			"pr-development/suspension-matrix-unrelated-head",
			"pdlnsuspend_matrix_unrelated_head",
		)
		output, err := runGit(
			ctx,
			fixture.workspace.Path,
			"commit-tree",
			suspended.PreparedTree,
			"-p",
			suspended.Tip,
			"-m",
			"Unrelated prepared effect",
		)
		if err != nil {
			t.Fatalf("create unrelated commit: %v", err)
		}
		unrelated := strings.TrimSpace(output)
		if unrelated == suspended.Tip || unrelated == suspended.PreparedCommit {
			t.Fatalf("unrelated commit = %q", unrelated)
		}
		if _, updateErr := runGit(
			ctx,
			fixture.workspace.Path,
			"update-ref",
			"--no-deref",
			"HEAD",
			unrelated,
			suspended.PreparedCommit,
		); updateErr != nil {
			t.Fatalf("install unrelated HEAD: %v", updateErr)
		}

		fresh := "pr-development/suspension-matrix-unrelated-head-fresh"
		_, err = fixture.manager.ResumeSuspendedPinnedLine(
			ctx,
			suspensionAPITestResumeRequest(
				fixture,
				suspended,
				fresh,
				"pdlnresume_matrix_unrelated_head",
			),
		)
		if err == nil || !errors.Is(err, ErrPinnedLineConflict) {
			t.Fatalf("ResumeSuspendedPinnedLine() unrelated HEAD error = %v", err)
		}
		if head := testGitCommit(t, fixture.workspace.Path, "HEAD"); head != unrelated {
			t.Fatalf("failed resume changed unrelated HEAD to %q", head)
		}
		suspensionMatrixAssertSuspendedWithoutFreshAuthority(
			t,
			fixture,
			suspended,
			fresh,
		)
	})

	t.Run("third index tree", func(t *testing.T) {
		ctx := context.Background()
		fixture, suspended, _ := suspensionMatrixAppliedFixture(
			t,
			"pr-development/suspension-matrix-third-index",
			"pdlnsuspend_matrix_third_index",
		)
		if suspended.CandidateTree == suspended.Tree ||
			suspended.CandidateTree == suspended.PreparedTree {
			t.Fatalf(
				"candidate tree %q is not a third tree (parent %q, prepared %q)",
				suspended.CandidateTree,
				suspended.Tree,
				suspended.PreparedTree,
			)
		}
		if _, err := runGit(
			ctx,
			fixture.workspace.Path,
			"read-tree",
			"--reset",
			suspended.CandidateTree,
		); err != nil {
			t.Fatalf("install third index tree: %v", err)
		}

		fresh := "pr-development/suspension-matrix-third-index-fresh"
		_, err := fixture.manager.ResumeSuspendedPinnedLine(
			ctx,
			suspensionAPITestResumeRequest(
				fixture,
				suspended,
				fresh,
				"pdlnresume_matrix_third_index",
			),
		)
		if err == nil || !errors.Is(err, ErrPinnedLineConflict) {
			t.Fatalf("ResumeSuspendedPinnedLine() third index error = %v", err)
		}
		if index := testGitObject(t, fixture.workspace.Path, "write-tree"); index != suspended.CandidateTree {
			t.Fatalf("failed resume changed third index to %q", index)
		}
		suspensionMatrixAssertSuspendedWithoutFreshAuthority(
			t,
			fixture,
			suspended,
			fresh,
		)
	})
}

func TestManagerResumeSuspendedPinnedLineRejectsGitLocksWithoutRemovingThem(t *testing.T) {
	tests := []struct {
		name     string
		lockPath func(t *testing.T, fixture pinnedLineTestFixture) string
	}{
		{
			name: "HEAD lock",
			lockPath: func(_ *testing.T, fixture pinnedLineTestFixture) string {
				return filepath.Join(fixture.workspace.Path, ".git", "HEAD.lock")
			},
		},
		{
			name: "index lock",
			lockPath: func(_ *testing.T, fixture pinnedLineTestFixture) string {
				return filepath.Join(fixture.workspace.Path, ".git", "index.lock")
			},
		},
		{
			name: "private ref lock",
			lockPath: func(t *testing.T, fixture pinnedLineTestFixture) string {
				state := developmentLineStateForTest(t, fixture.manager)
				line := state.DevelopmentLines[pinnedLineTestID]
				if line == nil {
					t.Fatal("development line is missing")
				}
				return adversarialDevelopmentLineMetadataLeaf(
					fixture.workspace.Path,
					line.Branch,
					false,
				) + ".lock"
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			ctx := context.Background()
			suffix := strings.ReplaceAll(strings.ToLower(test.name), " ", "-")
			fixture := newPinnedLineTestFixture(
				t,
				"pr-development/suspension-matrix-"+suffix,
			)
			suspended, err := fixture.manager.SuspendPinnedLine(
				ctx,
				suspensionAPITestSuspendRequest(
					fixture,
					"pdlnsuspend_matrix_"+strings.ReplaceAll(suffix, "-", "_"),
				),
			)
			if err != nil {
				t.Fatalf("SuspendPinnedLine() error = %v", err)
			}
			lockPath := test.lockPath(t, fixture)
			if writeErr := os.WriteFile(
				lockPath,
				[]byte("stale lock\n"),
				0o600,
			); writeErr != nil {
				t.Fatalf("write Git lock: %v", writeErr)
			}

			fresh := "pr-development/suspension-matrix-" + suffix + "-fresh"
			_, err = fixture.manager.ResumeSuspendedPinnedLine(
				ctx,
				suspensionAPITestResumeRequest(
					fixture,
					suspended,
					fresh,
					"pdlnresume_matrix_"+strings.ReplaceAll(suffix, "-", "_"),
				),
			)
			if err == nil || !errors.Is(err, ErrPinnedLineConflict) {
				t.Fatalf("ResumeSuspendedPinnedLine() lock error = %v", err)
			}
			contents, readErr := os.ReadFile(lockPath)
			if readErr != nil || string(contents) != "stale lock\n" {
				t.Fatalf("Git lock after rejection = %q, %v", contents, readErr)
			}
			suspensionMatrixAssertSuspendedWithoutFreshAuthority(
				t,
				fixture,
				suspended,
				fresh,
			)
		})
	}
}

func TestManagerResumeSuspendedPinnedLineRejectsCandidateDrift(t *testing.T) {
	ctx := context.Background()
	fixture := newPinnedLineTestFixture(t, "pr-development/suspension-matrix-drift")
	path := filepath.Join(fixture.workspace.Path, "retained.txt")
	if err := os.WriteFile(path, []byte("exact retained repair\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	suspended, err := fixture.manager.SuspendPinnedLine(
		ctx,
		suspensionAPITestSuspendRequest(fixture, "pdlnsuspend_matrix_drift"),
	)
	if err != nil {
		t.Fatalf("SuspendPinnedLine() error = %v", err)
	}
	if writeErr := os.WriteFile(
		path,
		[]byte("unfenced later repair\n"),
		0o644,
	); writeErr != nil {
		t.Fatal(writeErr)
	}

	fresh := "pr-development/suspension-matrix-drift-fresh"
	_, err = fixture.manager.ResumeSuspendedPinnedLine(
		ctx,
		suspensionAPITestResumeRequest(
			fixture,
			suspended,
			fresh,
			"pdlnresume_matrix_drift",
		),
	)
	if err == nil || !errors.Is(err, ErrPinnedLineConflict) {
		t.Fatalf("ResumeSuspendedPinnedLine() candidate drift error = %v", err)
	}
	contents, readErr := os.ReadFile(path)
	if readErr != nil || string(contents) != "unfenced later repair\n" {
		t.Fatalf("drifted content after rejection = %q, %v", contents, readErr)
	}
	suspensionMatrixAssertSuspendedWithoutFreshAuthority(t, fixture, suspended, fresh)
}

func TestManagerSuspendAndResumePinnedLineExactNoChanges(t *testing.T) {
	ctx := context.Background()
	fixture := newPinnedLineTestFixture(t, "pr-development/suspension-matrix-no-change")
	headBefore := testGitCommit(t, fixture.workspace.Path, "HEAD")
	indexBefore := testGitObject(t, fixture.workspace.Path, "write-tree")

	suspended, err := fixture.manager.SuspendPinnedLine(
		ctx,
		suspensionAPITestSuspendRequest(fixture, "pdlnsuspend_matrix_no_change"),
	)
	if err != nil {
		t.Fatalf("SuspendPinnedLine() error = %v", err)
	}
	if suspended.CandidateTree != fixture.lease.Tree || suspended.ChangedFileCount != 0 ||
		suspended.CandidateDigest == "" || suspended.PreparedCommit != "" ||
		suspended.PreparedTree != "" || suspended.PreparedCommitApplied {
		t.Fatalf("no-change suspension = %#v", suspended)
	}
	suspensionAPITestAssertGitState(t, fixture.workspace.Path, headBefore, indexBefore, "")

	resumed, err := fixture.manager.ResumeSuspendedPinnedLine(
		ctx,
		suspensionAPITestResumeRequest(
			fixture,
			suspended,
			"pr-development/suspension-matrix-no-change-fresh",
			"pdlnresume_matrix_no_change",
		),
	)
	if err != nil {
		t.Fatalf("ResumeSuspendedPinnedLine() error = %v", err)
	}
	if resumed.CandidateTree != fixture.lease.Tree || resumed.ChangedFileCount != 0 ||
		resumed.Version != fixture.lease.Version ||
		resumed.MutationEpoch != fixture.lease.MutationEpoch || resumed.RotationHash == "" {
		t.Fatalf("no-change resume = %#v", resumed)
	}
	suspensionAPITestAssertGitState(t, fixture.workspace.Path, headBefore, indexBefore, "")
}

func TestManagerDevelopmentLineSuspensionIsStandalone(t *testing.T) {
	ctx := context.Background()
	fixture := newPinnedLineTestFixture(t, "pr-development/suspension-matrix-standalone")
	suspendRequest := suspensionAPITestSuspendRequest(
		fixture,
		"pdlnsuspend_matrix_standalone",
	)
	err := fixture.manager.WithPinnedOperation(ctx, fixture.pin, func(operationContext context.Context) error {
		_, suspendErr := fixture.manager.SuspendPinnedLine(operationContext, suspendRequest)
		if suspendErr == nil || !strings.Contains(suspendErr.Error(), "outside another pinned operation") {
			t.Fatalf("inherited SuspendPinnedLine() error = %v", suspendErr)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("WithPinnedOperation() error = %v", err)
	}
	state := developmentLineStateForTest(t, fixture.manager)
	if line := state.DevelopmentLines[pinnedLineTestID]; line == nil ||
		line.State != developmentLineMutating || line.SuspensionCount != 0 {
		t.Fatalf("inherited suspension changed line = %#v", line)
	}
	if workspace := state.Workspaces[fixture.workspace.ID]; workspace == nil ||
		workspace.LockedBy == nil ||
		workspace.LockedBy.SessionKey != fixture.pin.ReservationKey {
		t.Fatalf("inherited suspension changed workspace = %#v", workspace)
	}

	suspended, err := fixture.manager.SuspendPinnedLine(ctx, suspendRequest)
	if err != nil {
		t.Fatalf("standalone SuspendPinnedLine() error = %v", err)
	}
	resumeRequest := suspensionAPITestResumeRequest(
		fixture,
		suspended,
		"pr-development/suspension-matrix-standalone-fresh",
		"pdlnresume_matrix_standalone",
	)
	err = fixture.manager.WithPinnedOperation(ctx, resumeRequest.Pin, func(operationContext context.Context) error {
		_, resumeErr := fixture.manager.ResumeSuspendedPinnedLine(operationContext, resumeRequest)
		if resumeErr == nil || !strings.Contains(resumeErr.Error(), "outside another pinned operation") {
			t.Fatalf("inherited ResumeSuspendedPinnedLine() error = %v", resumeErr)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("fresh WithPinnedOperation() error = %v", err)
	}
	suspensionMatrixAssertSuspendedWithoutFreshAuthority(
		t,
		fixture,
		suspended,
		resumeRequest.Pin.ReservationKey,
	)
	if _, err := fixture.manager.ResumeSuspendedPinnedLine(ctx, resumeRequest); err != nil {
		t.Fatalf("standalone ResumeSuspendedPinnedLine() error = %v", err)
	}
}

func suspensionMatrixAppliedFixture(
	t *testing.T,
	reservation, suspensionIntent string,
) (pinnedLineTestFixture, PinnedLineSuspendResult, string) {
	t.Helper()
	ctx := context.Background()
	fixture := newPinnedLineTestFixture(t, reservation)
	path := filepath.Join(fixture.workspace.Path, "prepared.txt")
	if err := os.WriteFile(path, []byte("prepared repair\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	preparedCandidate := fixture.snapshot(t)
	commitRequest := fixture.commitRequest(preparedCandidate, "Apply matrix repair")
	applied, err := fixture.manager.CommitPinned(ctx, commitRequest)
	if err != nil {
		t.Fatalf("CommitPinned() error = %v", err)
	}
	if writeErr := os.WriteFile(
		path,
		[]byte("prepared repair plus review fix\n"),
		0o644,
	); writeErr != nil {
		t.Fatal(writeErr)
	}
	suspended, err := fixture.manager.SuspendPinnedLineCommitRecovery(
		ctx,
		PinnedLineCommitSuspensionRequest{
			Suspend: suspensionAPITestSuspendRequest(fixture, suspensionIntent),
			Commit:  commitRequest,
		},
	)
	if err != nil {
		t.Fatalf("SuspendPinnedLineCommitRecovery() error = %v", err)
	}
	if !suspended.PreparedCommitApplied || suspended.PreparedCommit != applied.Commit ||
		suspended.PreparedTree != applied.Tree ||
		suspended.CandidateTree == suspended.Tree ||
		suspended.CandidateTree == suspended.PreparedTree {
		t.Fatalf("applied suspension = %#v", suspended)
	}
	return fixture, suspended, path
}

func suspensionMatrixAssertSuspendedWithoutFreshAuthority(
	t *testing.T,
	fixture pinnedLineTestFixture,
	suspended PinnedLineSuspendResult,
	freshReservation string,
) {
	t.Helper()
	state := developmentLineStateForTest(t, fixture.manager)
	line := state.DevelopmentLines[pinnedLineTestID]
	if line == nil || line.State != developmentLineSuspended ||
		line.SuspensionTailHash != suspended.SuspensionHash ||
		line.MutationReservationHash != "" || line.MutationAgentID != "" {
		t.Fatalf("failed resume changed suspended line = %#v", line)
	}
	workspace := state.Workspaces[fixture.workspace.ID]
	if workspace == nil || workspace.LockedBy != nil {
		t.Fatalf("failed resume installed workspace authority = %#v", workspace)
	}
	rotations := state.PinnedReservationRotations[fixture.workspace.ID]
	if len(rotations) != 0 {
		t.Fatalf("failed resume appended rotations = %#v", rotations)
	}
	if line.MutationReservationHash == developmentLineReservationHash(freshReservation) {
		t.Fatalf("failed resume installed fresh reservation %q", freshReservation)
	}
}
