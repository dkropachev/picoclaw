package workflows

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWriteWorkflowTemplateAtomicDoesNotRemoveReusedTempNameAfterReplace(
	t *testing.T,
) {
	dir := t.TempDir()
	target := filepath.Join(dir, "workflow.yml")
	injected := errors.New("injected directory sync failure")
	var replacedSource string
	const sentinel = "new owner"

	err := writeWorkflowTemplateAtomicWithHooks(
		target,
		[]byte("name: durable\n"),
		0o600,
		func(source, target string) error {
			replacedSource = source
			return replaceWorkflowFile(source, target)
		},
		func(string) error {
			if writeErr := os.WriteFile(replacedSource, []byte(sentinel), 0o600); writeErr != nil {
				t.Fatalf("create reused temp name: %v", writeErr)
			}
			return injected
		},
	)
	if !errors.Is(err, injected) {
		t.Fatalf("atomic write error = %v, want injected sync failure", err)
	}
	if data, readErr := os.ReadFile(replacedSource); readErr != nil {
		t.Fatalf("reused temp name was removed: %v", readErr)
	} else if string(data) != sentinel {
		t.Fatalf("reused temp data = %q, want %q", data, sentinel)
	}
	assertFileData(t, target, []byte("name: durable\n"))
}

func TestBuiltInWorkflowTemplateRegistryIsUniqueAndValid(t *testing.T) {
	names := map[string]bool{}
	refs := map[string]bool{}
	for _, template := range builtInWorkflowTemplateRegistry {
		if template.name == "" || names[template.name] {
			t.Fatalf("template name %q is empty or duplicated", template.name)
		}
		if template.ref == "" || refs[template.ref] {
			t.Fatalf("template ref %q is empty or duplicated", template.ref)
		}
		names[template.name] = true
		refs[template.ref] = true
		if err := validateWorkflowTemplate(template.raw); err != nil {
			t.Fatalf("template %q validation error = %v", template.name, err)
		}
	}
	if len(names) != 6 {
		t.Fatalf("built-in template count = %d, want 6", len(names))
	}
}

func TestListWorkflowTemplatesReportsSafeStatesInConfiguredDirectory(t *testing.T) {
	ctx := context.Background()
	workspace := t.TempDir()
	opts := []LocalOption{WithDefinitionsDir("automation/definitions")}

	entries, listErr := ListWorkflowTemplates(ctx, workspace, opts...)
	if listErr != nil {
		t.Fatalf("ListWorkflowTemplates(available) error = %v", listErr)
	}
	assertWorkflowTemplateState(
		t,
		entries,
		CodeReviewWorkflowName,
		WorkflowTemplateStateAvailable,
		"",
	)
	assertWorkflowTemplateState(
		t,
		entries,
		GitHubIssueTriageWorkflowName,
		WorkflowTemplateStateAvailable,
		"",
	)
	codeReviewPath := filepath.Join(workspace, "automation", "definitions", "code-review.yml")
	if err := os.MkdirAll(filepath.Dir(codeReviewPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(codeReviewPath, []byte(CodeReviewWorkflowYAML), 0o644); err != nil {
		t.Fatal(err)
	}
	issueTriagePath := filepath.Join(
		workspace,
		"automation",
		"definitions",
		"github-issue-triage.yml",
	)
	if err := os.Mkdir(issueTriagePath, 0o755); err != nil {
		t.Fatal(err)
	}

	entries, listErr = ListWorkflowTemplates(ctx, workspace, opts...)
	if listErr != nil {
		t.Fatalf("ListWorkflowTemplates(installed/blocked) error = %v", listErr)
	}
	assertWorkflowTemplateState(
		t,
		entries,
		CodeReviewWorkflowName,
		WorkflowTemplateStateInstalled,
		"",
	)
	assertWorkflowTemplateState(
		t,
		entries,
		GitHubIssueTriageWorkflowName,
		WorkflowTemplateStateBlocked,
		WorkflowTemplateBlockedNotRegular,
	)
	assertSafeWorkflowTemplateJSON(t, workspace, entries)

	if err := os.Remove(issueTriagePath); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(issueTriagePath, []byte("modified\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	entries, listErr = ListWorkflowTemplates(ctx, workspace, opts...)
	if listErr != nil {
		t.Fatalf("ListWorkflowTemplates(modified) error = %v", listErr)
	}
	assertWorkflowTemplateState(
		t,
		entries,
		GitHubIssueTriageWorkflowName,
		WorkflowTemplateStateModified,
		"",
	)
}

func TestListWorkflowTemplatesReportsInvalidConfigurationWithoutRawError(t *testing.T) {
	workspace := t.TempDir()
	entries, err := ListWorkflowTemplates(
		context.Background(),
		workspace,
		WithDefinitionsDir("../private"),
	)
	if err != nil {
		t.Fatalf("ListWorkflowTemplates() error = %v", err)
	}
	if len(entries) != len(builtInWorkflowTemplateRegistry) {
		t.Fatalf("entry count = %d, want %d", len(entries), len(builtInWorkflowTemplateRegistry))
	}
	for _, entry := range entries {
		if entry.State != WorkflowTemplateStateBlocked ||
			entry.BlockedReason != WorkflowTemplateBlockedConfiguration {
			t.Fatalf("entry = %#v, want configuration-invalid blocked state", entry)
		}
	}
	assertSafeWorkflowTemplateJSON(t, workspace, entries)
}

func TestInstallWorkflowTemplateWithCompatibilityIsExactAndIdempotent(t *testing.T) {
	ctx := context.Background()
	workspace := t.TempDir()
	opts := []LocalOption{WithDefinitionsDir("automation")}
	runtime := RuntimeCompatibility{PicoclawVersion: "v-template-test"}

	result, installErr := InstallWorkflowTemplateWithCompatibility(
		ctx,
		workspace,
		CodeReviewWorkflowName,
		false,
		runtime,
		opts...,
	)
	if installErr != nil {
		t.Fatalf("InstallWorkflowTemplateWithCompatibility() error = %v", installErr)
	}
	if !result.Installed || result.Overwritten || !result.Revalidated ||
		result.State != WorkflowTemplateStateInstalled {
		t.Fatalf("install result = %#v", result)
	}
	assertSafeWorkflowTemplateJSON(t, workspace, result)

	target := filepath.Join(workspace, "automation", "code-review.yml")
	data, readErr := os.ReadFile(target)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if string(data) != CodeReviewWorkflowYAML {
		t.Fatal("installed bytes differ from the built-in template")
	}
	if err := EnsureWorkflowRunnable(ctx, workspace, CodeReviewWorkflowRef, runtime, opts...); err != nil {
		t.Fatalf("EnsureWorkflowRunnable() error = %v", err)
	}

	if err := os.Chmod(target, 0o600); err != nil {
		t.Fatal(err)
	}
	second, secondErr := InstallWorkflowTemplateWithCompatibility(
		ctx,
		workspace,
		CodeReviewWorkflowName,
		true,
		runtime,
		opts...,
	)
	if secondErr != nil {
		t.Fatalf("second InstallWorkflowTemplateWithCompatibility() error = %v", secondErr)
	}
	if second.Installed || second.Overwritten || !second.Revalidated {
		t.Fatalf("idempotent install result = %#v", second)
	}
	info, statErr := os.Stat(target)
	if statErr != nil {
		t.Fatal(statErr)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("idempotent install mode = %#o, want %#o", info.Mode().Perm(), 0o600)
	}
}

func TestInstallWorkflowTemplateRequiresOverwriteForModifiedTarget(t *testing.T) {
	ctx := context.Background()
	workspace := t.TempDir()
	runtime := RuntimeCompatibility{PicoclawVersion: "v-template-test"}
	target := filepath.Join(workspace, "workflows", "code-review.yml")
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		t.Fatal(err)
	}
	modified := []byte("user-owned workflow\n")
	if err := os.WriteFile(target, modified, 0o640); err != nil {
		t.Fatal(err)
	}
	if _, err := RevalidateLocal(ctx, workspace, runtime); err != nil {
		t.Fatalf("RevalidateLocal() error = %v", err)
	}
	manifestBefore, err := os.ReadFile(compatibilityManifestPath(workspace))
	if err != nil {
		t.Fatal(err)
	}

	result, err := InstallWorkflowTemplateWithCompatibility(
		ctx,
		workspace,
		CodeReviewWorkflowName,
		false,
		runtime,
	)
	if result != nil || !errors.Is(err, ErrWorkflowTemplateOverwriteRequired) {
		t.Fatalf("install result, error = %#v, %v; want overwrite-required", result, err)
	}
	targetAfter, readErr := os.ReadFile(target)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if string(targetAfter) != string(modified) {
		t.Fatalf("target changed without overwrite: %q", targetAfter)
	}
	manifestAfter, readErr := os.ReadFile(compatibilityManifestPath(workspace))
	if readErr != nil {
		t.Fatal(readErr)
	}
	if string(manifestAfter) != string(manifestBefore) {
		t.Fatal("manifest changed when overwrite was not authorized")
	}

	result, err = InstallWorkflowTemplateWithCompatibility(
		ctx,
		workspace,
		CodeReviewWorkflowName,
		true,
		runtime,
	)
	if err != nil {
		t.Fatalf("overwrite install error = %v", err)
	}
	if !result.Installed || !result.Overwritten || !result.Revalidated {
		t.Fatalf("overwrite result = %#v", result)
	}
	targetAfter, err = os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(targetAfter) != CodeReviewWorkflowYAML {
		t.Fatal("overwrite did not restore exact built-in bytes")
	}
}

func TestInstallWorkflowTemplateBlocksNonRegularTargetEvenWithOverwrite(t *testing.T) {
	workspace := t.TempDir()
	target := filepath.Join(workspace, "workflows", "code-review.yml")
	if err := os.MkdirAll(target, 0o755); err != nil {
		t.Fatal(err)
	}

	result, err := InstallWorkflowTemplateWithCompatibility(
		context.Background(),
		workspace,
		CodeReviewWorkflowName,
		true,
		RuntimeCompatibility{PicoclawVersion: "v-template-test"},
	)
	if result != nil || !errors.Is(err, ErrWorkflowTemplateTargetBlocked) {
		t.Fatalf("install result, error = %#v, %v; want target-blocked", result, err)
	}
	info, statErr := os.Lstat(target)
	if statErr != nil {
		t.Fatal(statErr)
	}
	if !info.IsDir() {
		t.Fatalf("blocked target mode = %v, want unchanged directory", info.Mode())
	}
}

func TestInstallWorkflowTemplateRollsBackTargetAndManifest(t *testing.T) {
	ctx := context.Background()
	workspace := t.TempDir()
	target := filepath.Join(workspace, "workflows", "code-review.yml")
	manifestPath := compatibilityManifestPath(workspace)
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(manifestPath), 0o700); err != nil {
		t.Fatal(err)
	}
	originalTarget := []byte("user-owned target\n")
	originalManifest := []byte("user-owned manifest\n")
	if err := os.WriteFile(target, originalTarget, 0o640); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(manifestPath, originalManifest, 0o600); err != nil {
		t.Fatal(err)
	}

	revalidate := func(
		_ context.Context,
		_ string,
		_ RuntimeCompatibility,
		_ *workflowCompatibilityOverlay,
		_ ...LocalOption,
	) (*WorkflowCompatibilityManifest, error) {
		return nil, errors.New("raw failure at /private/secret")
	}
	result, err := installWorkflowTemplateWithCompatibility(
		ctx,
		workspace,
		CodeReviewWorkflowName,
		true,
		RuntimeCompatibility{PicoclawVersion: "v-template-test"},
		revalidate,
	)
	if result != nil || !errors.Is(err, ErrWorkflowTemplateRevalidationFailed) {
		t.Fatalf("install result, error = %#v, %v; want safe revalidation failure", result, err)
	}
	if strings.Contains(err.Error(), "private") || strings.Contains(err.Error(), "secret") {
		t.Fatalf("public error leaked raw failure: %v", err)
	}
	assertFileContentAndMode(t, target, originalTarget, 0o640)
	assertFileContentAndMode(t, manifestPath, originalManifest, 0o600)
}

func TestInstallWorkflowTemplateUsesUniqueAtomicManifestTemp(t *testing.T) {
	workspace := t.TempDir()
	target := filepath.Join(workspace, "workflows", "code-review.yml")
	manifestPath := compatibilityManifestPath(workspace)
	if err := os.MkdirAll(filepath.Dir(manifestPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(manifestPath+".tmp", 0o700); err != nil {
		t.Fatal(err)
	}

	result, err := InstallWorkflowTemplateWithCompatibility(
		context.Background(),
		workspace,
		CodeReviewWorkflowName,
		false,
		RuntimeCompatibility{PicoclawVersion: "v-template-test"},
	)
	if err != nil {
		t.Fatalf("install error = %v", err)
	}
	if result == nil || !result.Installed || !result.Revalidated {
		t.Fatalf("install result = %#v, want installed and revalidated", result)
	}
	if _, statErr := os.Lstat(target); statErr != nil {
		t.Fatalf("target stat error = %v", statErr)
	}
	if _, statErr := os.Lstat(manifestPath); statErr != nil {
		t.Fatalf("manifest stat error = %v", statErr)
	}
	if info, statErr := os.Lstat(manifestPath + ".tmp"); statErr != nil {
		t.Fatalf("fixed-name blocker stat error = %v", statErr)
	} else if !info.IsDir() {
		t.Fatalf("fixed-name blocker mode = %v, want unchanged directory", info.Mode())
	}
}

func TestLegacyWorkflowTemplateInstallUsesExactByteSafety(t *testing.T) {
	ctx := context.Background()
	workspace := t.TempDir()
	first, firstErr := InstallCodeReviewWorkflow(ctx, workspace, false)
	if firstErr != nil {
		t.Fatalf("first install error = %v", firstErr)
	}
	if !first.Installed {
		t.Fatalf("first install = %#v, want installed", first)
	}
	if err := os.WriteFile(first.Path, []byte("modified\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	second, secondErr := InstallCodeReviewWorkflow(ctx, workspace, false)
	if second != nil || !errors.Is(secondErr, ErrWorkflowTemplateOverwriteRequired) {
		t.Fatalf("modified install result, error = %#v, %v", second, secondErr)
	}
	third, thirdErr := InstallCodeReviewWorkflow(ctx, workspace, true)
	if thirdErr != nil {
		t.Fatalf("overwrite error = %v", thirdErr)
	}
	if !third.Installed || !third.Overwritten {
		t.Fatalf("overwrite result = %#v", third)
	}
	fourth, fourthErr := InstallCodeReviewWorkflow(ctx, workspace, true)
	if fourthErr != nil {
		t.Fatalf("exact-byte reinstall error = %v", fourthErr)
	}
	if fourth.Installed || fourth.Overwritten {
		t.Fatalf("exact-byte reinstall = %#v, want no-op", fourth)
	}
}

func TestWorkflowTemplateCatalogUnknownNameIsSafe(t *testing.T) {
	workspace := t.TempDir()
	result, err := InstallWorkflowTemplateWithCompatibility(
		context.Background(),
		workspace,
		"../../private-template",
		true,
		RuntimeCompatibility{},
	)
	if result != nil || !errors.Is(err, ErrWorkflowTemplateUnknown) {
		t.Fatalf("install result, error = %#v, %v; want unknown-template", result, err)
	}
	if strings.Contains(err.Error(), "private-template") {
		t.Fatalf("unknown-template error leaked input: %v", err)
	}
}

func TestWorkflowTemplateCatalogCoreRejectsActiveDevelopment(t *testing.T) {
	workspace := t.TempDir()
	if _, err := StartWorkflowDevelopment(
		context.Background(),
		workspace,
		RuntimeCompatibility{PicoclawVersion: "template-test"},
		WorkflowDevelopmentStartRequest{
			Prompt:    "active template fence",
			TargetRef: "workflows/active-template.yml",
		},
	); err != nil {
		t.Fatalf("StartWorkflowDevelopment() error = %v", err)
	}
	result, err := InstallWorkflowTemplateWithCompatibility(
		context.Background(),
		workspace,
		CodeReviewWorkflowName,
		false,
		RuntimeCompatibility{PicoclawVersion: "template-test"},
	)
	if result != nil || !errors.Is(err, ErrActiveDevelopmentExists) {
		t.Fatalf(
			"InstallWorkflowTemplateWithCompatibility() = %#v, %v; want active-development conflict",
			result,
			err,
		)
	}
	if _, statErr := os.Stat(
		workflowTemplateInstallJournalPath(workspace),
	); !errors.Is(statErr, fs.ErrNotExist) {
		t.Fatalf("active-development rejection left journal: %v", statErr)
	}
}

func TestWorkflowTemplateCatalogRejectsDefinitionsRootSymlinkEscape(t *testing.T) {
	if testing.Short() {
		t.Skip("symlink test skipped in short mode")
	}
	workspace := t.TempDir()
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(workspace, "automation")); err != nil {
		t.Skipf("symlink unsupported: %v", err)
	}
	result, err := InstallWorkflowTemplateWithCompatibility(
		context.Background(),
		workspace,
		CodeReviewWorkflowName,
		false,
		RuntimeCompatibility{PicoclawVersion: "template-test"},
		WithDefinitionsDir("automation"),
	)
	if result != nil || !errors.Is(err, ErrWorkflowTemplateTargetBlocked) {
		t.Fatalf(
			"InstallWorkflowTemplateWithCompatibility() = %#v, %v; want blocked target",
			result,
			err,
		)
	}
	if _, statErr := os.Stat(filepath.Join(outside, "code-review.yml")); !errors.Is(
		statErr,
		fs.ErrNotExist,
	) {
		t.Fatalf("template escaped workspace: %v", statErr)
	}
}

func TestWorkflowTemplateInstallPreparedJournalRecoversAtEveryBoundary(t *testing.T) {
	for _, boundary := range []workflowTemplateInstallBoundary{
		workflowTemplateInstallBoundaryPrepared,
		workflowTemplateInstallBoundaryTargetWritten,
		workflowTemplateInstallBoundaryManifestRevalidated,
	} {
		t.Run(string(boundary), func(t *testing.T) {
			ctx := context.Background()
			workspace := t.TempDir()
			opts := []LocalOption{WithDefinitionsDir("automation/definitions")}
			target := filepath.Join(
				workspace,
				"automation",
				"definitions",
				"code-review.yml",
			)
			manifestPath := compatibilityManifestPath(workspace)
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.MkdirAll(filepath.Dir(manifestPath), 0o700); err != nil {
				t.Fatal(err)
			}
			originalTarget := []byte("original target\n")
			originalManifest := []byte("original manifest\n")
			if err := os.WriteFile(target, originalTarget, 0o640); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(manifestPath, originalManifest, 0o600); err != nil {
				t.Fatal(err)
			}

			injected := errors.New("injected template transaction crash")
			result, err := installWorkflowTemplateTransaction(
				ctx,
				workspace,
				CodeReviewWorkflowName,
				true,
				RuntimeCompatibility{PicoclawVersion: "v-template-test"},
				buildCompatibilityManifestLocked,
				&workflowTemplateInstallHooks{
					afterBoundary: func(got workflowTemplateInstallBoundary) error {
						if got == boundary {
							return injected
						}
						return nil
					},
					leaveJournalOnError: true,
				},
				opts...,
			)
			if result != nil || !errors.Is(err, injected) {
				t.Fatalf(
					"install result, error = %#v, %v; want injected failure",
					result,
					err,
				)
			}
			journalData, readErr := os.ReadFile(
				workflowTemplateInstallJournalPath(workspace),
			)
			if readErr != nil {
				t.Fatalf("read prepared journal: %v", readErr)
			}
			if strings.Contains(string(journalData), workspace) {
				t.Fatal("template transaction journal exposed an absolute workspace path")
			}

			entries, listErr := ListWorkflowTemplates(ctx, workspace, opts...)
			if listErr != nil {
				t.Fatalf("catalog read recovery error = %v", listErr)
			}
			assertWorkflowTemplateState(
				t,
				entries,
				CodeReviewWorkflowName,
				WorkflowTemplateStateModified,
				"",
			)
			assertFileContentAndMode(t, target, originalTarget, 0o640)
			assertFileContentAndMode(t, manifestPath, originalManifest, 0o600)
			if _, statErr := os.Lstat(
				workflowTemplateInstallJournalPath(workspace),
			); !errors.Is(statErr, fs.ErrNotExist) {
				t.Fatalf("prepared journal still exists after recovery: %v", statErr)
			}
		})
	}
}

func TestWorkflowTemplateInstallRecoveryRestoresMissingPreimages(t *testing.T) {
	ctx := context.Background()
	workspace := t.TempDir()
	injected := errors.New("injected template transaction crash")
	result, err := installWorkflowTemplateTransaction(
		ctx,
		workspace,
		CodeReviewWorkflowName,
		false,
		RuntimeCompatibility{PicoclawVersion: "v-template-test"},
		buildCompatibilityManifestLocked,
		&workflowTemplateInstallHooks{
			afterBoundary: func(boundary workflowTemplateInstallBoundary) error {
				if boundary == workflowTemplateInstallBoundaryManifestRevalidated {
					return injected
				}
				return nil
			},
			leaveJournalOnError: true,
		},
	)
	if result != nil || !errors.Is(err, injected) {
		t.Fatalf("install result, error = %#v, %v; want injected failure", result, err)
	}
	if _, err := LoadLocal(ctx, workspace, CodeReviewWorkflowRef); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("LoadLocal() recovery error = %v, want missing workflow", err)
	}
	for _, path := range []string{
		filepath.Join(workspace, "workflows", "code-review.yml"),
		compatibilityManifestPath(workspace),
		workflowTemplateInstallJournalPath(workspace),
	} {
		if _, statErr := os.Lstat(path); !errors.Is(statErr, fs.ErrNotExist) {
			t.Fatalf("%s still exists after missing-preimage recovery: %v", path, statErr)
		}
	}
}

func TestWorkflowTemplateInstallRecoveryPreservesPostCrashEdits(t *testing.T) {
	tests := []struct {
		name string
		edit func(t *testing.T, target, manifest string)
	}{
		{
			name: "target_contents",
			edit: func(t *testing.T, target, _ string) {
				t.Helper()
				if err := os.WriteFile(target, []byte("operator target edit\n"), 0o644); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "target_mode",
			edit: func(t *testing.T, target, _ string) {
				t.Helper()
				if err := os.Chmod(target, 0o600); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "manifest_contents",
			edit: func(t *testing.T, _, manifest string) {
				t.Helper()
				if err := os.WriteFile(
					manifest,
					[]byte("operator manifest edit\n"),
					0o600,
				); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "manifest_mode",
			edit: func(t *testing.T, _, manifest string) {
				t.Helper()
				if err := os.Chmod(manifest, 0o640); err != nil {
					t.Fatal(err)
				}
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			workspace := t.TempDir()
			target := filepath.Join(workspace, "workflows", "code-review.yml")
			manifest := compatibilityManifestPath(workspace)
			interrupted := errors.New("simulated process interruption")
			result, err := installWorkflowTemplateTransaction(
				context.Background(),
				workspace,
				CodeReviewWorkflowName,
				false,
				RuntimeCompatibility{PicoclawVersion: "v-template-test"},
				buildCompatibilityManifestLocked,
				&workflowTemplateInstallHooks{
					afterBoundary: func(boundary workflowTemplateInstallBoundary) error {
						if boundary == workflowTemplateInstallBoundaryManifestRevalidated {
							return interrupted
						}
						return nil
					},
					leaveJournalOnError: true,
				},
			)
			if result != nil || !errors.Is(err, interrupted) {
				t.Fatalf(
					"install result, error = %#v, %v; want interruption",
					result,
					err,
				)
			}
			journal, missing, readErr := readWorkflowTemplateInstallJournal(workspace)
			if readErr != nil || missing {
				t.Fatalf("read prepared journal = %#v, %v, missing=%v", journal, readErr, missing)
			}
			if journal.Stage != workflowTemplateInstallStageManifestWriteStarted {
				t.Fatalf("journal stage = %q, want manifest write started", journal.Stage)
			}
			journalPath := workflowTemplateInstallJournalPath(workspace)
			journalBefore, err := os.ReadFile(journalPath)
			if err != nil {
				t.Fatal(err)
			}

			test.edit(t, target, manifest)
			targetBefore := captureWorkflowTemplateFileForTest(t, target)
			manifestBefore := captureWorkflowTemplateFileForTest(t, manifest)
			recoveryErr := recoverWorkflowTemplateInstallTransaction(workspace)
			if !errors.Is(recoveryErr, ErrWorkflowTemplateRecoveryFailed) ||
				!errors.Is(recoveryErr, ErrWorkflowRecoveryConflict) {
				t.Fatalf("recovery error = %v, want stable recovery conflict", recoveryErr)
			}
			assertWorkflowTemplateFileSnapshotForTest(t, target, targetBefore)
			assertWorkflowTemplateFileSnapshotForTest(t, manifest, manifestBefore)
			journalAfter, readErr := os.ReadFile(journalPath)
			if readErr != nil {
				t.Fatalf("conflicted journal was removed: %v", readErr)
			}
			if !bytes.Equal(journalAfter, journalBefore) {
				t.Fatal("conflicted journal changed during recovery")
			}
		})
	}
}

func TestWorkflowTemplateInstallRecoveryRunsBeforeNextMutation(t *testing.T) {
	ctx := context.Background()
	workspace := t.TempDir()
	target := filepath.Join(workspace, "workflows", "code-review.yml")
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		t.Fatal(err)
	}
	originalTarget := []byte("user-owned workflow\n")
	if err := os.WriteFile(target, originalTarget, 0o640); err != nil {
		t.Fatal(err)
	}
	injected := errors.New("injected template transaction crash")
	result, err := installWorkflowTemplateTransaction(
		ctx,
		workspace,
		CodeReviewWorkflowName,
		true,
		RuntimeCompatibility{PicoclawVersion: "v-template-test"},
		buildCompatibilityManifestLocked,
		&workflowTemplateInstallHooks{
			afterBoundary: func(boundary workflowTemplateInstallBoundary) error {
				if boundary == workflowTemplateInstallBoundaryTargetWritten {
					return injected
				}
				return nil
			},
			leaveJournalOnError: true,
		},
	)
	if result != nil || !errors.Is(err, injected) {
		t.Fatalf("install result, error = %#v, %v; want injected failure", result, err)
	}

	result, err = InstallWorkflowTemplateWithCompatibility(
		ctx,
		workspace,
		CodeReviewWorkflowName,
		false,
		RuntimeCompatibility{PicoclawVersion: "v-template-test"},
	)
	if result != nil || !errors.Is(err, ErrWorkflowTemplateOverwriteRequired) {
		t.Fatalf(
			"next mutation result, error = %#v, %v; want recovered overwrite requirement",
			result,
			err,
		)
	}
	assertFileContentAndMode(t, target, originalTarget, 0o640)
	if _, statErr := os.Lstat(
		workflowTemplateInstallJournalPath(workspace),
	); !errors.Is(statErr, fs.ErrNotExist) {
		t.Fatalf("prepared journal still exists before next mutation: %v", statErr)
	}
}

func TestWorkflowTemplateInstallCommittedJournalFinalizesWithoutRollback(t *testing.T) {
	ctx := context.Background()
	workspace := t.TempDir()
	injected := errors.New("injected crash after commit")
	result, err := installWorkflowTemplateTransaction(
		ctx,
		workspace,
		CodeReviewWorkflowName,
		false,
		RuntimeCompatibility{PicoclawVersion: "v-template-test"},
		buildCompatibilityManifestLocked,
		&workflowTemplateInstallHooks{
			afterBoundary: func(boundary workflowTemplateInstallBoundary) error {
				if boundary == workflowTemplateInstallBoundaryCommitted {
					return injected
				}
				return nil
			},
			leaveJournalOnError: true,
		},
	)
	if result != nil || !errors.Is(err, injected) {
		t.Fatalf("install result, error = %#v, %v; want injected failure", result, err)
	}
	if _, statErr := os.Lstat(
		workflowTemplateInstallJournalPath(workspace),
	); statErr != nil {
		t.Fatalf("committed journal stat error = %v", statErr)
	}

	entries, listErr := ListWorkflowTemplates(ctx, workspace)
	if listErr != nil {
		t.Fatalf("catalog committed recovery error = %v", listErr)
	}
	assertWorkflowTemplateState(
		t,
		entries,
		CodeReviewWorkflowName,
		WorkflowTemplateStateInstalled,
		"",
	)
	if err := EnsureWorkflowRunnable(
		ctx,
		workspace,
		CodeReviewWorkflowRef,
		RuntimeCompatibility{PicoclawVersion: "v-template-test"},
	); err != nil {
		t.Fatalf("committed install was rolled back: %v", err)
	}
	if _, statErr := os.Lstat(
		workflowTemplateInstallJournalPath(workspace),
	); !errors.Is(statErr, fs.ErrNotExist) {
		t.Fatalf("committed journal still exists after finalization: %v", statErr)
	}
}

func TestWorkflowTemplateInstallCommitMarkerErrorsRetryAndRecover(t *testing.T) {
	tests := []struct {
		name             string
		failures         int
		writeBeforeError bool
		wantCallError    bool
		wantInstalled    bool
	}{
		{
			name:          "transient_write_error_retries",
			failures:      1,
			wantInstalled: true,
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
			wantInstalled:    true,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			workspace := t.TempDir()
			injected := errors.New("injected commit marker write error")
			commitAttempts := 0
			result, err := installWorkflowTemplateTransaction(
				context.Background(),
				workspace,
				CodeReviewWorkflowName,
				false,
				RuntimeCompatibility{PicoclawVersion: "v-template-test"},
				buildCompatibilityManifestLocked,
				&workflowTemplateInstallHooks{
					writeJournal: func(
						workspace string,
						journal *workflowTemplateInstallJournal,
					) error {
						if journal.Phase != workflowTemplateInstallPhaseCommitted {
							return writeWorkflowTemplateInstallJournal(workspace, journal)
						}
						commitAttempts++
						if commitAttempts > test.failures {
							return writeWorkflowTemplateInstallJournal(workspace, journal)
						}
						if test.writeBeforeError {
							if writeErr := writeWorkflowTemplateInstallJournal(
								workspace,
								journal,
							); writeErr != nil {
								return errors.Join(injected, writeErr)
							}
						}
						return injected
					},
				},
			)
			if test.wantCallError {
				if result != nil || !errors.Is(err, injected) {
					t.Fatalf(
						"install result, error = %#v, %v; want injected error",
						result,
						err,
					)
				}
				if _, statErr := os.Lstat(
					workflowTemplateInstallJournalPath(workspace),
				); statErr != nil {
					t.Fatalf("ambiguous journal stat error = %v", statErr)
				}
			} else if err != nil || result == nil {
				t.Fatalf("install result, error = %#v, %v; want success", result, err)
			}
			if commitAttempts != 2 {
				t.Fatalf(
					"commit marker attempts = %d, want 2",
					commitAttempts,
				)
			}

			entries, listErr := ListWorkflowTemplates(context.Background(), workspace)
			if listErr != nil {
				t.Fatalf("catalog recovery error = %v", listErr)
			}
			wantState := WorkflowTemplateStateAvailable
			if test.wantInstalled {
				wantState = WorkflowTemplateStateInstalled
			}
			assertWorkflowTemplateState(
				t,
				entries,
				CodeReviewWorkflowName,
				wantState,
				"",
			)
			targetPath := filepath.Join(workspace, "workflows", "code-review.yml")
			manifestPath := compatibilityManifestPath(workspace)
			if test.wantInstalled {
				assertFileData(t, targetPath, []byte(CodeReviewWorkflowYAML))
				if _, statErr := os.Lstat(manifestPath); statErr != nil {
					t.Fatalf("committed manifest stat error = %v", statErr)
				}
			} else {
				for _, path := range []string{targetPath, manifestPath} {
					if _, statErr := os.Lstat(path); !errors.Is(statErr, fs.ErrNotExist) {
						t.Fatalf("rolled-back path %s stat error = %v", path, statErr)
					}
				}
			}
			if _, statErr := os.Lstat(
				workflowTemplateInstallJournalPath(workspace),
			); !errors.Is(statErr, fs.ErrNotExist) {
				t.Fatalf("journal still exists after recovery: %v", statErr)
			}
		})
	}
}

func TestWorkflowTemplateInstallRecoveryRejectsUnsafeJournalPaths(t *testing.T) {
	workspace := t.TempDir()
	outside := filepath.Join(filepath.Dir(workspace), "outside-template.yml")
	outsideData := []byte("must remain unchanged\n")
	if err := os.WriteFile(outside, outsideData, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Remove(outside) })
	journalPath := workflowTemplateInstallJournalPath(workspace)
	if err := os.MkdirAll(filepath.Dir(journalPath), 0o700); err != nil {
		t.Fatal(err)
	}
	journal := `{
  "version": 2,
  "phase": "prepared",
  "stage": "prepared",
  "definitions_dir": "../",
  "template_name": "code-review",
  "target_ref": "workflows/code-review.yml",
  "target": {
    "preimage": {"exists": false},
    "postimage": {"exists": true, "data": "bmFtZTogQ29kZSBSZXZpZXcK", "mode": 420}
  },
  "manifest": {
    "preimage": {"exists": false},
    "postimage": {"exists": true, "data": "e30=", "mode": 384}
  }
}`
	if err := os.WriteFile(journalPath, []byte(journal), 0o600); err != nil {
		t.Fatal(err)
	}

	entries, err := ListWorkflowTemplates(context.Background(), workspace)
	if entries != nil || !errors.Is(err, ErrWorkflowTemplateCatalogUnavailable) {
		t.Fatalf(
			"ListWorkflowTemplates() = %#v, %v; want safe catalog-unavailable error",
			entries,
			err,
		)
	}
	assertFileContentAndMode(t, outside, outsideData, 0o600)
	if _, statErr := os.Lstat(journalPath); statErr != nil {
		t.Fatalf("unsafe journal should remain for operator recovery: %v", statErr)
	}
}

func assertWorkflowTemplateState(
	t *testing.T,
	entries []WorkflowTemplateCatalogEntry,
	name string,
	state WorkflowTemplateState,
	blockedReason WorkflowTemplateBlockedReason,
) {
	t.Helper()
	for _, entry := range entries {
		if entry.Name != name {
			continue
		}
		if entry.State != state || entry.BlockedReason != blockedReason {
			t.Fatalf(
				"template %q = %#v, want state %q, blocked reason %q",
				name,
				entry,
				state,
				blockedReason,
			)
		}
		return
	}
	t.Fatalf("template %q not found in %#v", name, entries)
}

func assertSafeWorkflowTemplateJSON(t *testing.T, workspace string, value any) {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	serialized := string(data)
	for _, forbidden := range []string{workspace, `"path"`, `"error"`} {
		if strings.Contains(serialized, forbidden) {
			t.Fatalf("safe template JSON = %s, unexpectedly contains %q", serialized, forbidden)
		}
	}
}

func assertFileContentAndMode(
	t *testing.T,
	path string,
	want []byte,
	wantMode os.FileMode,
) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != string(want) {
		t.Fatalf("%s content = %q, want %q", filepath.Base(path), data, want)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != wantMode.Perm() {
		t.Fatalf(
			"%s mode = %#o, want %#o",
			filepath.Base(path),
			info.Mode().Perm(),
			wantMode.Perm(),
		)
	}
}

func captureWorkflowTemplateFileForTest(
	t *testing.T,
	path string,
) workflowTemplateFileSnapshot {
	t.Helper()
	snapshot, err := captureWorkflowTemplateFile(path)
	if err != nil {
		t.Fatalf("capture %s: %v", filepath.Base(path), err)
	}
	return snapshot
}

func assertWorkflowTemplateFileSnapshotForTest(
	t *testing.T,
	path string,
	want workflowTemplateFileSnapshot,
) {
	t.Helper()
	got := captureWorkflowTemplateFileForTest(t, path)
	if !workflowTemplateFileSnapshotsEqual(got, want) {
		t.Fatalf(
			"%s snapshot changed during conflicted recovery: got %#v, want %#v",
			filepath.Base(path),
			got,
			want,
		)
	}
}
