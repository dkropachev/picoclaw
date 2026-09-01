package workflows

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type workflowDevelopmentPublishFixture struct {
	workspace    string
	runtime      RuntimeCompatibility
	session      *WorkflowDevelopmentSession
	request      WorkflowDevelopmentPublishRequest
	targetPath   string
	manifestPath string
	archivePath  string
	activePath   string
	localOptions []LocalOption
}

func TestPublishWorkflowDevelopmentFencedRequiresMatchingReadyDependencyGate(
	t *testing.T,
) {
	t.Run("revision mismatch", func(t *testing.T) {
		fixture := newWorkflowDevelopmentPublishFixture(t, "")
		fixture.request.ExpectedDependencyRevision = "dependency:shown"
		_, err := PublishWorkflowDevelopmentFenced(
			context.Background(),
			fixture.workspace,
			fixture.request,
			fixture.runtime,
			func(
				_ context.Context,
				input WorkflowDevelopmentPublishGateInput,
			) (WorkflowDevelopmentPublishGateResult, error) {
				if input.WorkflowRef != fixture.session.TargetWorkflowRef ||
					input.DraftRevision != fixture.session.DraftRevision ||
					input.YAML != fixture.session.YAML ||
					input.Workflow == nil {
					t.Fatalf("gate input = %#v", input)
				}
				return WorkflowDevelopmentPublishGateResult{
					Revision: "dependency:current",
					Ready:    true,
				}, nil
			},
			fixture.localOptions...,
		)
		if !errors.Is(err, ErrWorkflowDevelopmentDependencyRevisionMismatch) {
			t.Fatalf("publish error = %v, want dependency revision mismatch", err)
		}
		assertWorkflowDevelopmentPublishFixtureUnchanged(t, fixture)
	})

	t.Run("not ready", func(t *testing.T) {
		fixture := newWorkflowDevelopmentPublishFixture(t, "")
		fixture.request.ExpectedDependencyRevision = "dependency:current"
		_, err := PublishWorkflowDevelopmentFenced(
			context.Background(),
			fixture.workspace,
			fixture.request,
			fixture.runtime,
			func(
				context.Context,
				WorkflowDevelopmentPublishGateInput,
			) (WorkflowDevelopmentPublishGateResult, error) {
				return WorkflowDevelopmentPublishGateResult{
					Revision: "dependency:current",
					Ready:    false,
				}, nil
			},
			fixture.localOptions...,
		)
		if !errors.Is(err, ErrWorkflowDevelopmentPublishNotReady) {
			t.Fatalf("publish error = %v, want not ready", err)
		}
		assertWorkflowDevelopmentPublishFixtureUnchanged(t, fixture)
	})

	t.Run("gate required for dependency fence", func(t *testing.T) {
		fixture := newWorkflowDevelopmentPublishFixture(t, "")
		fixture.request.ExpectedDependencyRevision = "dependency:current"
		_, err := PublishWorkflowDevelopmentFenced(
			context.Background(),
			fixture.workspace,
			fixture.request,
			fixture.runtime,
			nil,
			fixture.localOptions...,
		)
		if !errors.Is(err, ErrWorkflowDevelopmentPublishGateRequired) {
			t.Fatalf("publish error = %v, want gate required", err)
		}
		assertWorkflowDevelopmentPublishFixtureUnchanged(t, fixture)
	})

	t.Run("matching ready gate publishes exact draft", func(t *testing.T) {
		fixture := newWorkflowDevelopmentPublishFixture(t, "")
		fixture.request.ExpectedDependencyRevision = "dependency:current"
		result, err := PublishWorkflowDevelopmentFenced(
			context.Background(),
			fixture.workspace,
			fixture.request,
			fixture.runtime,
			func(
				context.Context,
				WorkflowDevelopmentPublishGateInput,
			) (WorkflowDevelopmentPublishGateResult, error) {
				return WorkflowDevelopmentPublishGateResult{
					Revision: "dependency:current",
					Ready:    true,
				}, nil
			},
			fixture.localOptions...,
		)
		if err != nil {
			t.Fatalf("PublishWorkflowDevelopmentFenced() error = %v", err)
		}
		if result.WorkflowRef != fixture.session.TargetWorkflowRef {
			t.Fatalf("workflow ref = %q", result.WorkflowRef)
		}
		assertFileData(t, fixture.targetPath, []byte(fixture.session.YAML))
		if active, getErr := GetWorkflowDevelopmentSession(fixture.workspace); getErr != nil {
			t.Fatalf("GetWorkflowDevelopmentSession() error = %v", getErr)
		} else if active != nil {
			t.Fatalf("active session = %#v, want nil", active)
		}
		manifest, missing, readErr := readCompatibilityManifest(fixture.workspace)
		if readErr != nil || missing {
			t.Fatalf("readCompatibilityManifest() = %#v, %v, missing=%v", manifest, readErr, missing)
		}
		stamp := manifest.Workflows[fixture.session.TargetWorkflowRef]
		if stamp.Status != WorkflowValidationStatusValid ||
			stamp.WorkflowHash != workflowHashBytes([]byte(fixture.session.YAML)) {
			t.Fatalf("published stamp = %#v", stamp)
		}
		assertWorkflowDevelopmentArchived(t, fixture, true)
		if info, statErr := os.Stat(fixture.targetPath); statErr != nil {
			t.Fatalf("target stat error = %v", statErr)
		} else if info.Mode().Perm() != 0o640 {
			t.Fatalf("target mode = %o, want preserved 640", info.Mode().Perm())
		}
		if _, statErr := os.Stat(
			workflowDevelopmentPublishJournalPath(fixture.workspace),
		); !errors.Is(statErr, os.ErrNotExist) {
			t.Fatalf("journal stat error = %v, want not exist", statErr)
		}
	})
}

func TestPublishWorkflowDevelopmentFencedRechecksDependencyGateBeforeTransaction(
	t *testing.T,
) {
	fixture := newWorkflowDevelopmentPublishFixture(t, "")
	dependencyPath := filepath.Join(fixture.workspace, "dependency-state")
	if err := os.WriteFile(dependencyPath, []byte("shown"), 0o600); err != nil {
		t.Fatal(err)
	}
	fixture.request.ExpectedDependencyRevision = "dependency:shown"
	beforeTarget := readFileData(t, fixture.targetPath)
	beforeManifest := readFileData(t, fixture.manifestPath)
	beforeActive := readFileData(t, fixture.activePath)
	gateCalls := 0

	_, err := PublishWorkflowDevelopmentFenced(
		context.Background(),
		fixture.workspace,
		fixture.request,
		fixture.runtime,
		func(
			_ context.Context,
			_ WorkflowDevelopmentPublishGateInput,
		) (WorkflowDevelopmentPublishGateResult, error) {
			gateCalls++
			state := strings.TrimSpace(string(readFileData(t, dependencyPath)))
			if gateCalls == 1 {
				if writeErr := os.WriteFile(
					dependencyPath,
					[]byte("changed"),
					0o600,
				); writeErr != nil {
					t.Fatal(writeErr)
				}
			}
			return WorkflowDevelopmentPublishGateResult{
				Revision: "dependency:" + state,
				Ready:    true,
			}, nil
		},
		fixture.localOptions...,
	)
	if !errors.Is(err, ErrWorkflowDevelopmentDependencyRevisionMismatch) {
		t.Fatalf("publish error = %v, want dependency revision mismatch", err)
	}
	if gateCalls != 2 {
		t.Fatalf("dependency gate calls = %d, want 2", gateCalls)
	}
	assertFileData(t, fixture.targetPath, beforeTarget)
	assertFileData(t, fixture.manifestPath, beforeManifest)
	assertFileData(t, fixture.activePath, beforeActive)
}

func TestPublishWorkflowDevelopmentFencedRejectsEveryStaleRevision(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(t *testing.T, fixture *workflowDevelopmentPublishFixture)
		want   error
	}{
		{
			name: "session",
			mutate: func(_ *testing.T, fixture *workflowDevelopmentPublishFixture) {
				fixture.request.ExpectedSessionRevision = "sha256:stale"
			},
			want: ErrWorkflowSessionRevisionMismatch,
		},
		{
			name: "draft",
			mutate: func(_ *testing.T, fixture *workflowDevelopmentPublishFixture) {
				fixture.request.ExpectedDraftRevision = "sha256:stale"
			},
			want: ErrWorkflowDraftRevisionMismatch,
		},
		{
			name: "base target request",
			mutate: func(_ *testing.T, fixture *workflowDevelopmentPublishFixture) {
				fixture.request.ExpectedBaseTargetRevision = "sha256:stale"
			},
			want: ErrWorkflowTargetRevisionMismatch,
		},
		{
			name: "base target changed on disk",
			mutate: func(t *testing.T, fixture *workflowDevelopmentPublishFixture) {
				t.Helper()
				if err := os.WriteFile(
					fixture.targetPath,
					[]byte(GenerateWorkflowDraftYAML("external concurrent edit")),
					0o644,
				); err != nil {
					t.Fatal(err)
				}
			},
			want: ErrWorkflowTargetRevisionMismatch,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newWorkflowDevelopmentPublishFixture(t, "")
			test.mutate(t, fixture)
			beforeTarget := readFileData(t, fixture.targetPath)
			beforeManifest := readFileData(t, fixture.manifestPath)
			beforeActive := readFileData(t, fixture.activePath)
			gateCalled := false
			_, err := PublishWorkflowDevelopmentFenced(
				context.Background(),
				fixture.workspace,
				fixture.request,
				fixture.runtime,
				func(
					context.Context,
					WorkflowDevelopmentPublishGateInput,
				) (WorkflowDevelopmentPublishGateResult, error) {
					gateCalled = true
					return WorkflowDevelopmentPublishGateResult{}, nil
				},
				fixture.localOptions...,
			)
			if !errors.Is(err, test.want) {
				t.Fatalf("publish error = %v, want %v", err, test.want)
			}
			if gateCalled {
				t.Fatal("dependency gate ran before stale revision was rejected")
			}
			assertFileData(t, fixture.targetPath, beforeTarget)
			assertFileData(t, fixture.manifestPath, beforeManifest)
			assertFileData(t, fixture.activePath, beforeActive)
		})
	}
}

func TestPublishWorkflowDevelopmentFencedRequiresTestForExactPersistedBytes(
	t *testing.T,
) {
	fixture := newWorkflowDevelopmentPublishFixture(t, "")
	active, getErr := GetWorkflowDevelopmentSession(fixture.workspace)
	if getErr != nil {
		t.Fatal(getErr)
	}
	// The legacy draft key normalizes trailing whitespace. The exact draft
	// revision must still make this previously successful test stale.
	active.YAML += "\n"
	if err := writeActiveDevelopment(fixture.workspace, active); err != nil {
		t.Fatalf("writeActiveDevelopment() error = %v", err)
	}
	active, getErr = GetWorkflowDevelopmentSession(fixture.workspace)
	if getErr != nil {
		t.Fatal(getErr)
	}
	request := WorkflowDevelopmentPublishRequest{
		SessionID:                  active.ID,
		ExpectedSessionRevision:    active.SessionRevision,
		ExpectedDraftRevision:      active.DraftRevision,
		ExpectedBaseTargetRevision: active.BaseTargetRevision,
	}
	beforeTarget := readFileData(t, fixture.targetPath)
	_, publishErr := PublishWorkflowDevelopmentFenced(
		context.Background(),
		fixture.workspace,
		request,
		fixture.runtime,
		nil,
		fixture.localOptions...,
	)
	if publishErr == nil || !strings.Contains(publishErr.Error(), "test is stale") {
		t.Fatalf("publish error = %v, want exact draft test stale", publishErr)
	}
	assertFileData(t, fixture.targetPath, beforeTarget)
}

func TestPublishWorkflowDevelopmentTransactionRollsBackEveryMutationBoundary(
	t *testing.T,
) {
	injected := errors.New("injected workflow publish boundary failure")
	boundaries := []workflowDevelopmentPublishBoundary{
		workflowDevelopmentPublishBoundaryPrepared,
		workflowDevelopmentPublishBoundaryTargetWritten,
		workflowDevelopmentPublishBoundaryManifestActivated,
		workflowDevelopmentPublishBoundarySessionArchived,
		workflowDevelopmentPublishBoundaryActiveRemoved,
	}
	for _, boundary := range boundaries {
		t.Run(string(boundary), func(t *testing.T) {
			fixture := newWorkflowDevelopmentPublishFixture(t, "")
			beforeTarget := readFileData(t, fixture.targetPath)
			beforeManifest := readFileData(t, fixture.manifestPath)
			beforeActive := readFileData(t, fixture.activePath)
			_, err := publishWorkflowDevelopmentTransaction(
				context.Background(),
				fixture.workspace,
				&fixture.request,
				fixture.runtime,
				nil,
				&workflowDevelopmentPublishHooks{
					afterBoundary: func(
						current workflowDevelopmentPublishBoundary,
					) error {
						if current == boundary {
							return injected
						}
						return nil
					},
				},
				fixture.localOptions...,
			)
			if !errors.Is(err, injected) {
				t.Fatalf("publish error = %v, want injected failure", err)
			}
			assertFileData(t, fixture.targetPath, beforeTarget)
			assertFileData(t, fixture.manifestPath, beforeManifest)
			assertFileData(t, fixture.activePath, beforeActive)
			assertWorkflowDevelopmentArchived(t, fixture, false)
			if _, statErr := os.Stat(
				workflowDevelopmentPublishJournalPath(fixture.workspace),
			); !errors.Is(statErr, os.ErrNotExist) {
				t.Fatalf("journal stat error = %v, want not exist", statErr)
			}
		})
	}
}

func TestRunnableSnapshotWaitsForWorkflowPublishCommit(t *testing.T) {
	fixture := newWorkflowDevelopmentPublishFixture(t, "")
	manifestActivated := make(chan struct{})
	releasePublish := make(chan struct{})
	publishDone := make(chan error, 1)
	go func() {
		_, err := publishWorkflowDevelopmentTransaction(
			context.Background(),
			fixture.workspace,
			&fixture.request,
			fixture.runtime,
			nil,
			&workflowDevelopmentPublishHooks{
				afterBoundary: func(boundary workflowDevelopmentPublishBoundary) error {
					if boundary == workflowDevelopmentPublishBoundaryManifestActivated {
						close(manifestActivated)
						<-releasePublish
					}
					return nil
				},
			},
			fixture.localOptions...,
		)
		publishDone <- err
	}()
	<-manifestActivated

	type snapshotResult struct {
		workflow *Workflow
		err      error
	}
	snapshotDone := make(chan snapshotResult, 1)
	go func() {
		workflow, err := LoadRunnableLocalSnapshot(
			context.Background(),
			fixture.workspace,
			fixture.session.TargetWorkflowRef,
			fixture.runtime,
			fixture.localOptions...,
		)
		snapshotDone <- snapshotResult{workflow: workflow, err: err}
	}()
	select {
	case result := <-snapshotDone:
		close(releasePublish)
		t.Fatalf(
			"runnable snapshot returned before publish commit: workflow=%#v error=%v",
			result.workflow,
			result.err,
		)
	case <-time.After(50 * time.Millisecond):
	}

	close(releasePublish)
	if err := <-publishDone; err != nil {
		t.Fatalf("publish error = %v", err)
	}
	select {
	case result := <-snapshotDone:
		if result.err != nil {
			t.Fatalf("runnable snapshot after commit error = %v", result.err)
		}
		if result.workflow == nil {
			t.Fatal("runnable snapshot after commit is nil")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("runnable snapshot remained blocked after publish commit")
	}
}

func TestWorkflowDevelopmentPublishPreparedJournalRecoversOnNextMutation(
	t *testing.T,
) {
	fixture := newWorkflowDevelopmentPublishFixture(t, "automation/definitions")
	beforeTarget := readFileData(t, fixture.targetPath)
	beforeManifest := readFileData(t, fixture.manifestPath)
	beforeActive := readFileData(t, fixture.activePath)
	interrupted := errors.New("simulated process interruption")

	_, err := publishWorkflowDevelopmentTransaction(
		context.Background(),
		fixture.workspace,
		&fixture.request,
		fixture.runtime,
		nil,
		&workflowDevelopmentPublishHooks{
			afterBoundary: func(boundary workflowDevelopmentPublishBoundary) error {
				if boundary == workflowDevelopmentPublishBoundaryActiveRemoved {
					return interrupted
				}
				return nil
			},
			leaveJournalOnError: true,
		},
		fixture.localOptions...,
	)
	if !errors.Is(err, interrupted) {
		t.Fatalf("publish error = %v, want interruption", err)
	}
	assertFileData(t, fixture.targetPath, []byte(fixture.session.YAML))
	if active, getErr := rawWorkflowDevelopmentActive(t, fixture.workspace); getErr != nil || active != nil {
		t.Fatalf("active after interruption = %#v, %v; want nil", active, getErr)
	}
	if _, statErr := os.Stat(
		workflowDevelopmentPublishJournalPath(fixture.workspace),
	); statErr != nil {
		t.Fatalf("journal stat after interruption = %v", statErr)
	}

	// Runnable snapshot admission shares the workflow mutation boundary, so it
	// recovers the prepared transaction before any executable bytes can be
	// observed.
	if _, loadErr := LoadRunnableLocalSnapshot(
		context.Background(),
		fixture.workspace,
		fixture.session.TargetWorkflowRef,
		fixture.runtime,
		fixture.localOptions...,
	); loadErr != nil {
		t.Fatalf("LoadRunnableLocalSnapshot() recovery error = %v", loadErr)
	}

	assertFileData(t, fixture.targetPath, beforeTarget)
	assertFileData(t, fixture.manifestPath, beforeManifest)
	assertFileData(t, fixture.activePath, beforeActive)
	assertWorkflowDevelopmentArchived(t, fixture, false)
	if _, statErr := os.Stat(
		workflowDevelopmentPublishJournalPath(fixture.workspace),
	); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("journal stat after recovery = %v, want not exist", statErr)
	}
}

func TestWorkflowDevelopmentPublishCommittedJournalFinalizesOnNextMutation(
	t *testing.T,
) {
	fixture := newWorkflowDevelopmentPublishFixture(t, "")
	interrupted := errors.New("simulated interruption after commit")
	_, err := publishWorkflowDevelopmentTransaction(
		context.Background(),
		fixture.workspace,
		&fixture.request,
		fixture.runtime,
		nil,
		&workflowDevelopmentPublishHooks{
			afterBoundary: func(boundary workflowDevelopmentPublishBoundary) error {
				if boundary == workflowDevelopmentPublishBoundaryCommitted {
					return interrupted
				}
				return nil
			},
			leaveJournalOnError: true,
		},
		fixture.localOptions...,
	)
	if !errors.Is(err, interrupted) {
		t.Fatalf("publish error = %v, want interruption", err)
	}
	assertFileData(t, fixture.targetPath, []byte(fixture.session.YAML))
	committed, readErr := workflowDevelopmentPublishJournalIsCommitted(fixture.workspace)
	if readErr != nil || !committed {
		t.Fatalf("journal committed = %v, error = %v", committed, readErr)
	}
	if active, getErr := rawWorkflowDevelopmentActive(t, fixture.workspace); getErr != nil || active != nil {
		t.Fatalf("active after commit = %#v, %v; want nil", active, getErr)
	}

	unlock, lockErr := lockWorkflowMutation(fixture.workspace)
	if lockErr != nil {
		t.Fatalf("next mutation lock error = %v", lockErr)
	}
	unlock()

	assertFileData(t, fixture.targetPath, []byte(fixture.session.YAML))
	assertWorkflowDevelopmentArchived(t, fixture, true)
	if _, statErr := os.Stat(
		workflowDevelopmentPublishJournalPath(fixture.workspace),
	); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("journal stat after recovery = %v, want not exist", statErr)
	}
}

func TestWorkflowDevelopmentPublishCommitMarkerErrorsRetryAndRecover(t *testing.T) {
	tests := []struct {
		name             string
		failures         int
		writeBeforeError bool
		wantCallError    bool
		wantCommitted    bool
	}{
		{
			name:          "transient_write_error_retries",
			failures:      1,
			wantCommitted: true,
		},
		{
			name:          "persistent_write_error_rolls_back",
			failures:      2,
			wantCallError: true,
		},
		{
			name:             "persistent_post_replace_sync_error_finalizes",
			failures:         2,
			writeBeforeError: true,
			wantCallError:    true,
			wantCommitted:    true,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newWorkflowDevelopmentPublishFixture(t, "")
			beforeTarget := readFileData(t, fixture.targetPath)
			beforeManifest := readFileData(t, fixture.manifestPath)
			beforeActive := readFileData(t, fixture.activePath)
			injected := errors.New("injected publish commit marker write error")
			commitAttempts := 0

			result, err := publishWorkflowDevelopmentTransaction(
				context.Background(),
				fixture.workspace,
				&fixture.request,
				fixture.runtime,
				nil,
				&workflowDevelopmentPublishHooks{
					writeJournal: func(
						workspace string,
						journal *workflowDevelopmentPublishJournal,
					) error {
						if journal.Phase != workflowDevelopmentPublishPhaseCommitted {
							return writeWorkflowDevelopmentPublishJournal(workspace, journal)
						}
						commitAttempts++
						if commitAttempts > test.failures {
							return writeWorkflowDevelopmentPublishJournal(workspace, journal)
						}
						if test.writeBeforeError {
							if writeErr := writeWorkflowDevelopmentPublishJournal(
								workspace,
								journal,
							); writeErr != nil {
								return errors.Join(injected, writeErr)
							}
						}
						return injected
					},
				},
				fixture.localOptions...,
			)
			if test.wantCallError {
				if result != nil || !errors.Is(err, injected) {
					t.Fatalf(
						"publish result, error = %#v, %v; want injected error",
						result,
						err,
					)
				}
				if _, statErr := os.Lstat(
					workflowDevelopmentPublishJournalPath(fixture.workspace),
				); statErr != nil {
					t.Fatalf("ambiguous journal stat error = %v", statErr)
				}
			} else if err != nil || result == nil {
				t.Fatalf("publish result, error = %#v, %v; want success", result, err)
			}
			if commitAttempts != 2 {
				t.Fatalf("commit marker attempts = %d, want 2", commitAttempts)
			}

			unlock, lockErr := lockWorkflowMutation(fixture.workspace)
			if lockErr != nil {
				t.Fatalf("recovery lock error = %v", lockErr)
			}
			unlock()
			if test.wantCommitted {
				assertFileData(t, fixture.targetPath, []byte(fixture.session.YAML))
				assertWorkflowDevelopmentArchived(t, fixture, true)
				if active, getErr := GetWorkflowDevelopmentSession(fixture.workspace); getErr != nil || active != nil {
					t.Fatalf("committed active = %#v, %v; want nil", active, getErr)
				}
			} else {
				assertFileData(t, fixture.targetPath, beforeTarget)
				assertFileData(t, fixture.manifestPath, beforeManifest)
				assertFileData(t, fixture.activePath, beforeActive)
				assertWorkflowDevelopmentArchived(t, fixture, false)
			}
			if _, statErr := os.Lstat(
				workflowDevelopmentPublishJournalPath(fixture.workspace),
			); !errors.Is(statErr, os.ErrNotExist) {
				t.Fatalf("journal still exists after recovery: %v", statErr)
			}
		})
	}
}

func TestWorkflowDevelopmentPublishUsesConfiguredDefinitionsAndKeepsUnrelatedInvalid(
	t *testing.T,
) {
	fixture := newWorkflowDevelopmentPublishFixture(t, "automation/definitions")
	invalidRef := filepath.Join(
		fixture.workspace,
		"automation",
		"definitions",
		"unrelated-invalid.yml",
	)
	if err := os.WriteFile(invalidRef, []byte("name: [unterminated\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := PublishWorkflowDevelopmentFenced(
		context.Background(),
		fixture.workspace,
		fixture.request,
		fixture.runtime,
		nil,
		fixture.localOptions...,
	); err != nil {
		t.Fatalf("PublishWorkflowDevelopmentFenced() error = %v", err)
	}
	assertFileData(t, fixture.targetPath, []byte(fixture.session.YAML))
	defaultPath := filepath.Join(fixture.workspace, fixture.session.TargetWorkflowRef)
	if _, statErr := os.Stat(defaultPath); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("default definitions target stat = %v, want not exist", statErr)
	}
	manifest, missing, err := readCompatibilityManifest(fixture.workspace)
	if err != nil || missing {
		t.Fatalf("readCompatibilityManifest() = %#v, %v, missing=%v", manifest, err, missing)
	}
	if stamp := manifest.Workflows[fixture.session.TargetWorkflowRef]; stamp.Status != WorkflowValidationStatusValid {
		t.Fatalf("published workflow stamp = %#v, want valid", stamp)
	}
	invalidStamp, ok := manifest.Workflows["workflows/unrelated-invalid.yml"]
	if !ok || invalidStamp.Status != WorkflowValidationStatusInvalid {
		t.Fatalf("unrelated invalid stamp = %#v, found=%v", invalidStamp, ok)
	}
	if err := EnsureWorkflowRunnable(
		context.Background(),
		fixture.workspace,
		fixture.session.TargetWorkflowRef,
		fixture.runtime,
		fixture.localOptions...,
	); err != nil {
		t.Fatalf("EnsureWorkflowRunnable() error = %v", err)
	}
}

func newWorkflowDevelopmentPublishFixture(
	t *testing.T,
	definitionsDir string,
) *workflowDevelopmentPublishFixture {
	t.Helper()
	ctx := context.Background()
	workspace := t.TempDir()
	runtime := RuntimeCompatibility{
		PicoclawVersion: "v1.2.3",
		GitCommit:       "publish-fixture",
	}
	var opts []LocalOption
	if definitionsDir != "" {
		opts = append(opts, WithDefinitionsDir(definitionsDir))
	} else {
		definitionsDir = DefaultDefinitionsDir
	}
	ref := "workflows/fenced-publish.yml"
	targetPath := filepath.Join(
		workspace,
		filepath.FromSlash(definitionsDir),
		"fenced-publish.yml",
	)
	if err := os.MkdirAll(filepath.Dir(targetPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		targetPath,
		[]byte(GenerateWorkflowDraftYAML("old published behavior")),
		0o640,
	); err != nil {
		t.Fatal(err)
	}
	if _, err := RevalidateLocal(ctx, workspace, runtime, opts...); err != nil {
		t.Fatalf("RevalidateLocal() error = %v", err)
	}
	if _, err := StartWorkflowDevelopment(
		ctx,
		workspace,
		runtime,
		WorkflowDevelopmentStartRequest{
			Prompt:    "new draft behavior",
			TargetRef: ref,
		},
		opts...,
	); err != nil {
		t.Fatalf("StartWorkflowDevelopment() error = %v", err)
	}
	session, err := RecordWorkflowDevelopmentTest(
		workspace,
		&RunResult{RunID: "wr_publish_ready", Status: RunStatusSucceeded},
		nil,
	)
	if err != nil {
		t.Fatalf("RecordWorkflowDevelopmentTest() error = %v", err)
	}
	return &workflowDevelopmentPublishFixture{
		workspace: workspace,
		runtime:   runtime,
		session:   session,
		request: WorkflowDevelopmentPublishRequest{
			SessionID:                  session.ID,
			ExpectedSessionRevision:    session.SessionRevision,
			ExpectedDraftRevision:      session.DraftRevision,
			ExpectedBaseTargetRevision: session.BaseTargetRevision,
		},
		targetPath:   targetPath,
		manifestPath: compatibilityManifestPath(workspace),
		archivePath:  workflowDevelopmentArchivePath(workspace, session.ID),
		activePath:   activeDevelopmentPath(workspace),
		localOptions: opts,
	}
}

func assertWorkflowDevelopmentPublishFixtureUnchanged(
	t *testing.T,
	fixture *workflowDevelopmentPublishFixture,
) {
	t.Helper()
	if active, err := GetWorkflowDevelopmentSession(fixture.workspace); err != nil {
		t.Fatalf("GetWorkflowDevelopmentSession() error = %v", err)
	} else if active == nil || active.SessionRevision != fixture.session.SessionRevision {
		t.Fatalf("active session = %#v, want revision %q", active, fixture.session.SessionRevision)
	}
	assertWorkflowDevelopmentArchived(t, fixture, false)
	if _, statErr := os.Stat(
		workflowDevelopmentPublishJournalPath(fixture.workspace),
	); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("journal stat = %v, want not exist", statErr)
	}
}

func readFileData(t *testing.T, path string) []byte {
	t.Helper()
	clean := filepath.Clean(path)
	if filepath.Base(clean) == compatibilityManifest &&
		filepath.Base(filepath.Dir(clean)) == compatibilityManifestDir {
		workspace := filepath.Dir(filepath.Dir(clean))
		manifest, missing, err := readCompatibilityManifest(workspace)
		if err != nil || missing {
			t.Fatalf("read compatibility snapshot %s: missing=%v error=%v", path, missing, err)
		}
		data, err := json.Marshal(manifest)
		if err != nil {
			t.Fatal(err)
		}
		return data
	}
	if filepath.Base(clean) == workflowDevelopmentActive &&
		filepath.Base(filepath.Dir(clean)) == workflowDevelopmentDir {
		workspace := filepath.Dir(filepath.Dir(clean))
		session, err := GetWorkflowDevelopmentSession(workspace)
		if err != nil || session == nil {
			t.Fatalf("read active development snapshot %s: %#v, %v", path, session, err)
		}
		data, err := json.Marshal(session)
		if err != nil {
			t.Fatal(err)
		}
		return data
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return data
}

func assertFileData(t *testing.T, path string, want []byte) {
	t.Helper()
	got := readFileData(t, path)
	if !bytes.Equal(got, want) {
		t.Fatalf("file %s data mismatch\n got: %q\nwant: %q", path, got, want)
	}
}

func assertWorkflowDevelopmentArchived(
	t *testing.T,
	fixture *workflowDevelopmentPublishFixture,
	want bool,
) {
	t.Helper()
	db, err := openWorkflowDatabase(t.Context(), fixture.workspace)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var lifecycle string
	err = db.QueryRowContext(t.Context(), `SELECT lifecycle FROM workflow_development_sessions
		WHERE session_id=?`, fixture.session.ID).Scan(&lifecycle)
	if err != nil {
		t.Fatal(err)
	}
	archived := lifecycle == "published" || lifecycle == "discarded"
	if archived != want {
		t.Fatalf("development lifecycle = %q, want archived=%v", lifecycle, want)
	}
}

func rawWorkflowDevelopmentActive(
	t *testing.T,
	workspace string,
) (*WorkflowDevelopmentSession, error) {
	t.Helper()
	db, err := openWorkflowDatabase(t.Context(), workspace)
	if err != nil {
		return nil, err
	}
	defer db.Close()
	return loadWorkflowDevelopmentSession(t.Context(), db, "active")
}
