package workflows

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestWorkflowInternalStateRootsRejectSymlinks(t *testing.T) {
	for _, rootName := range []string{
		workflowMutationStateDir,
		compatibilityManifestDir,
		workflowDevelopmentDir,
	} {
		t.Run(rootName, func(t *testing.T) {
			workspace := t.TempDir()
			outside := t.TempDir()
			symlinkOrSkip(t, outside, filepath.Join(workspace, rootName))

			_, err := resolveWorkflowInternalPath(workspace, rootName, "probe")
			if !errors.Is(err, ErrWorkflowInternalStateRootUnsafe) {
				t.Fatalf(
					"resolveWorkflowInternalPath() error = %v, want unsafe root",
					err,
				)
			}
		})
	}
}

func TestWorkflowInternalStateRootsRejectSymlinksInsideWorkspace(t *testing.T) {
	for _, rootName := range []string{
		workflowMutationStateDir,
		compatibilityManifestDir,
		workflowDevelopmentDir,
	} {
		t.Run(rootName, func(t *testing.T) {
			workspace := t.TempDir()
			target := filepath.Join(workspace, "unrelated")
			if err := os.MkdirAll(target, 0o755); err != nil {
				t.Fatal(err)
			}
			symlinkOrSkip(t, target, filepath.Join(workspace, rootName))

			_, err := resolveWorkflowInternalPath(workspace, rootName, "probe")
			if !errors.Is(err, ErrWorkflowInternalStateRootUnsafe) {
				t.Fatalf(
					"resolveWorkflowInternalPath() error = %v, want unsafe root",
					err,
				)
			}
		})
	}
}

func TestWorkflowMutationLockDoesNotFollowStateRootSymlink(t *testing.T) {
	workspace := t.TempDir()
	outside := t.TempDir()
	symlinkOrSkip(
		t,
		outside,
		filepath.Join(workspace, workflowMutationStateDir),
	)

	called := false
	err := WithWorkflowMutationLock(workspace, func() error {
		called = true
		return nil
	})
	if !errors.Is(err, ErrWorkflowInternalStateRootUnsafe) {
		t.Fatalf("WithWorkflowMutationLock() error = %v, want unsafe root", err)
	}
	if called {
		t.Fatal("WithWorkflowMutationLock() called operation through unsafe root")
	}
	if _, err := os.Lstat(filepath.Join(outside, "mutation.lock")); !os.IsNotExist(err) {
		t.Fatalf("outside mutation lock stat error = %v, want not exist", err)
	}
}

func TestWorkflowMutationLockDoesNotFollowLockFileSymlink(t *testing.T) {
	workspace := t.TempDir()
	stateRoot := filepath.Join(workspace, workflowMutationStateDir)
	if err := os.MkdirAll(stateRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	outsidePath := filepath.Join(t.TempDir(), "outside.lock")
	const sentinel = "outside lock"
	if err := os.WriteFile(outsidePath, []byte(sentinel), 0o600); err != nil {
		t.Fatal(err)
	}
	symlinkOrSkip(t, outsidePath, filepath.Join(stateRoot, "mutation.lock"))

	called := false
	err := WithWorkflowMutationLock(workspace, func() error {
		called = true
		return nil
	})
	if !errors.Is(err, ErrWorkflowInternalStateRootUnsafe) {
		t.Fatalf("WithWorkflowMutationLock() error = %v, want unsafe root", err)
	}
	if called {
		t.Fatal("WithWorkflowMutationLock() called operation through unsafe lock")
	}
	if data, err := os.ReadFile(outsidePath); err != nil {
		t.Fatal(err)
	} else if string(data) != sentinel {
		t.Fatalf("outside lock = %q, want %q", data, sentinel)
	}
}

func TestWorkflowTransactionJournalsDoNotFollowSymlinks(t *testing.T) {
	tests := []struct {
		name    string
		journal string
		read    func(string) error
		remove  func(string) error
	}{
		{
			name:    "template",
			journal: workflowTemplateInstallJournalFile,
			read: func(workspace string) error {
				_, _, err := readWorkflowTemplateInstallJournal(workspace)
				return err
			},
			remove: removeWorkflowTemplateInstallJournal,
		},
		{
			name:    "publish",
			journal: workflowDevelopmentPublishJournalFile,
			read: func(workspace string) error {
				_, _, err := readWorkflowDevelopmentPublishJournal(workspace)
				return err
			},
			remove: removeWorkflowDevelopmentPublishJournal,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			workspace := t.TempDir()
			stateRoot := filepath.Join(workspace, workflowMutationStateDir)
			if err := os.MkdirAll(stateRoot, 0o700); err != nil {
				t.Fatal(err)
			}
			outsidePath := filepath.Join(t.TempDir(), test.journal)
			const sentinel = "outside journal"
			if err := os.WriteFile(outsidePath, []byte(sentinel), 0o600); err != nil {
				t.Fatal(err)
			}
			symlinkOrSkip(t, outsidePath, filepath.Join(stateRoot, test.journal))

			if err := test.read(workspace); !errors.Is(
				err,
				ErrWorkflowInternalStateRootUnsafe,
			) {
				t.Fatalf("read journal error = %v, want unsafe root", err)
			}
			if err := test.remove(workspace); !errors.Is(
				err,
				ErrWorkflowInternalStateRootUnsafe,
			) {
				t.Fatalf("remove journal error = %v, want unsafe root", err)
			}
			if data, err := os.ReadFile(outsidePath); err != nil {
				t.Fatal(err)
			} else if string(data) != sentinel {
				t.Fatalf("outside journal = %q, want %q", data, sentinel)
			}
		})
	}
}

func TestWorkflowCompatibilityManifestDoesNotFollowSymlinks(t *testing.T) {
	for _, symlinkRoot := range []bool{true, false} {
		name := "manifest"
		if symlinkRoot {
			name = "root"
		}
		t.Run(name, func(t *testing.T) {
			workspace := t.TempDir()
			outside := t.TempDir()
			outsidePath := filepath.Join(outside, compatibilityManifest)
			const sentinel = "outside manifest"
			if err := os.WriteFile(outsidePath, []byte(sentinel), 0o600); err != nil {
				t.Fatal(err)
			}
			root := filepath.Join(workspace, compatibilityManifestDir)
			if symlinkRoot {
				symlinkOrSkip(t, outside, root)
			} else {
				if err := os.MkdirAll(root, 0o755); err != nil {
					t.Fatal(err)
				}
				symlinkOrSkip(t, outsidePath, filepath.Join(root, compatibilityManifest))
			}

			if _, _, err := readCompatibilityManifest(workspace); !errors.Is(
				err,
				ErrWorkflowInternalStateRootUnsafe,
			) {
				t.Fatalf("readCompatibilityManifest() error = %v, want unsafe root", err)
			}
			err := writeCompatibilityManifest(
				workspace,
				&WorkflowCompatibilityManifest{
					Workflows: map[string]WorkflowValidationStamp{},
				},
			)
			if !errors.Is(err, ErrWorkflowInternalStateRootUnsafe) {
				t.Fatalf("writeCompatibilityManifest() error = %v, want unsafe root", err)
			}
			if data, err := os.ReadFile(outsidePath); err != nil {
				t.Fatal(err)
			} else if string(data) != sentinel {
				t.Fatalf("outside manifest = %q, want %q", data, sentinel)
			}
		})
	}
}

func TestWorkflowDevelopmentActiveDoesNotFollowSymlinks(t *testing.T) {
	for _, symlinkRoot := range []bool{true, false} {
		name := "active"
		if symlinkRoot {
			name = "root"
		}
		t.Run(name, func(t *testing.T) {
			workspace := t.TempDir()
			outside := t.TempDir()
			outsidePath := filepath.Join(outside, workflowDevelopmentActive)
			const sentinel = "outside active session"
			if err := os.WriteFile(outsidePath, []byte(sentinel), 0o600); err != nil {
				t.Fatal(err)
			}
			root := filepath.Join(workspace, workflowDevelopmentDir)
			if symlinkRoot {
				symlinkOrSkip(t, outside, root)
			} else {
				if err := os.MkdirAll(root, 0o755); err != nil {
					t.Fatal(err)
				}
				symlinkOrSkip(t, outsidePath, filepath.Join(root, workflowDevelopmentActive))
			}

			if _, err := GetWorkflowDevelopmentSession(workspace); !errors.Is(
				err,
				ErrWorkflowInternalStateRootUnsafe,
			) {
				t.Fatalf(
					"GetWorkflowDevelopmentSession() error = %v, want unsafe root",
					err,
				)
			}
			if err := writeActiveDevelopment(
				workspace,
				&WorkflowDevelopmentSession{},
			); !errors.Is(err, ErrWorkflowInternalStateRootUnsafe) {
				t.Fatalf("writeActiveDevelopment() error = %v, want unsafe root", err)
			}
			if data, err := os.ReadFile(outsidePath); err != nil {
				t.Fatal(err)
			} else if string(data) != sentinel {
				t.Fatalf("outside active session = %q, want %q", data, sentinel)
			}
		})
	}
}

func TestWorkflowDevelopmentArchiveDoesNotFollowSymlink(t *testing.T) {
	workspace := t.TempDir()
	root := filepath.Join(workspace, workflowDevelopmentDir)
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	symlinkOrSkip(t, outside, filepath.Join(root, "archive"))
	session := &WorkflowDevelopmentSession{ID: "dev_safe"}

	err := archiveDevelopmentSession(workspace, session, "discarded")
	if !errors.Is(err, ErrWorkflowInternalStateRootUnsafe) {
		t.Fatalf("archiveDevelopmentSession() error = %v, want unsafe root", err)
	}
	if entries, err := os.ReadDir(outside); err != nil {
		t.Fatal(err)
	} else if len(entries) != 0 {
		t.Fatalf("outside archive entries = %#v, want none", entries)
	}
}

func TestNativeWorkflowStateDoesNotFollowStateRootSymlink(t *testing.T) {
	workspace := t.TempDir()
	outside := t.TempDir()
	symlinkOrSkip(t, outside, filepath.Join(workspace, workflowStateDir))

	_, _, err := RunNativeFunction(
		context.Background(),
		"workflow.state",
		map[string]any{
			"action": "set",
			"key":    "secret",
			"value":  "outside",
		},
		ExecutionContext{WorkspaceDir: workspace},
	)
	if !errors.Is(err, ErrWorkflowInternalStateRootUnsafe) {
		t.Fatalf("RunNativeFunction() error = %v, want unsafe root", err)
	}
	if entries, err := os.ReadDir(outside); err != nil {
		t.Fatal(err)
	} else if len(entries) != 0 {
		t.Fatalf("outside state entries = %#v, want none", entries)
	}
}

func TestWorkflowInternalStateAllowsSymlinkedWorkspace(t *testing.T) {
	base := t.TempDir()
	workspace := filepath.Join(base, "workspace")
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		t.Fatal(err)
	}
	workspaceAlias := filepath.Join(base, "workspace-link")
	symlinkOrSkip(t, workspace, workspaceAlias)

	if err := WithWorkflowMutationLock(workspaceAlias, func() error {
		return writeCompatibilityManifest(
			workspaceAlias,
			&WorkflowCompatibilityManifest{
				Workflows: map[string]WorkflowValidationStamp{},
			},
		)
	}); err != nil {
		t.Fatalf("state mutation through workspace symlink error = %v", err)
	}
	if _, err := os.Stat(filepath.Join(
		workspace,
		compatibilityManifestDir,
		compatibilityManifest,
	)); err != nil {
		t.Fatalf("manifest in evaluated workspace stat error = %v", err)
	}
}

func symlinkOrSkip(t *testing.T, oldname, newname string) {
	t.Helper()
	if err := os.Symlink(oldname, newname); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
}
