package gitworkspace

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

type pinnedValidationTestManifestEntry struct {
	path    string
	mode    string
	kind    string
	content string
}

func TestManagerWithPinnedCandidateValidationRoots(t *testing.T) {
	fixture := newPinnedCommitTestFixture(t, "pr-development/validation-roots")
	if err := os.WriteFile(
		filepath.Join(fixture.workspace.Path, "README.md"),
		[]byte("# candidate\n"),
		0o644,
	); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(fixture.workspace.Path, "bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		filepath.Join(fixture.workspace.Path, "bin", "check.sh"),
		[]byte("#!/bin/sh\nexit 0\n"),
		0o755,
	); err != nil {
		t.Fatal(err)
	}
	hasSymlink := true
	if err := os.MkdirAll(filepath.Join(fixture.workspace.Path, "docs"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(
		"../README.md",
		filepath.Join(fixture.workspace.Path, "docs", "readme-link"),
	); err != nil {
		hasSymlink = false
	}
	candidate := fixture.snapshot(t)
	request := pinnedValidationRequest(fixture, candidate)
	request.Pin.Repository += string(os.PathSeparator) + "."
	parentEntries := []pinnedValidationTestManifestEntry{
		{path: ".gitignore", mode: "100644", kind: "regular", content: "ignored/\n*.log\n"},
		{path: "README.md", mode: "100644", kind: "regular", content: "# repo\n"},
	}
	scriptMode := "100755"
	if runtime.GOOS == "windows" {
		scriptMode = "100644"
	}
	candidateEntries := []pinnedValidationTestManifestEntry{
		{path: ".gitignore", mode: "100644", kind: "regular", content: "ignored/\n*.log\n"},
		{path: "README.md", mode: "100644", kind: "regular", content: "# candidate\n"},
		{path: "bin/check.sh", mode: scriptMode, kind: "regular", content: "#!/bin/sh\nexit 0\n"},
	}
	if hasSymlink {
		candidateEntries = append(candidateEntries, pinnedValidationTestManifestEntry{
			path:    "docs/readme-link",
			mode:    "120000",
			kind:    "symlink",
			content: "../README.md",
		})
	}
	wantParent := pinnedValidationTestManifest(
		testGitObject(t, fixture.workspace.Path, "rev-parse", candidate.ParentCommit+"^{tree}"),
		parentEntries,
	)
	wantCandidate := pinnedValidationTestManifest(candidate.Tree, candidateEntries)

	var first PinnedCandidateValidationRoots
	var disposableRoot string
	err := fixture.manager.WithPinnedCandidateValidationRoots(
		context.Background(),
		request,
		func(_ context.Context, roots PinnedCandidateValidationRoots) error {
			first = roots
			disposableRoot = filepath.Dir(roots.ParentRoot)
			if filepath.Dir(roots.CandidateRoot) != disposableRoot ||
				roots.ParentRoot == roots.CandidateRoot ||
				!filepath.IsAbs(roots.ParentRoot) || !filepath.IsAbs(roots.CandidateRoot) {
				t.Fatalf("validation roots = %#v", roots)
			}
			if roots.Repository != fixture.pin.Repository ||
				roots.Repository == request.Pin.Repository {
				t.Fatalf(
					"normalized repository = %q, raw = %q, want %q",
					roots.Repository,
					request.Pin.Repository,
					fixture.pin.Repository,
				)
			}
			if roots.ParentManifest != wantParent {
				t.Fatalf("parent manifest = %#v, want %#v", roots.ParentManifest, wantParent)
			}
			if roots.CandidateManifest != wantCandidate {
				t.Fatalf("candidate manifest = %#v, want %#v", roots.CandidateManifest, wantCandidate)
			}
			if content, readErr := os.ReadFile(
				filepath.Join(roots.ParentRoot, "README.md"),
			); readErr != nil || string(content) != "# repo\n" {
				t.Fatalf("parent README = %q, %v", content, readErr)
			}
			if content, readErr := os.ReadFile(
				filepath.Join(roots.CandidateRoot, "README.md"),
			); readErr != nil || string(content) != "# candidate\n" {
				t.Fatalf("candidate README = %q, %v", content, readErr)
			}
			for _, root := range []string{roots.ParentRoot, roots.CandidateRoot} {
				if _, statErr := os.Lstat(filepath.Join(root, ".git")); !os.IsNotExist(statErr) {
					t.Fatalf("disposable root %q exposes .git: %v", root, statErr)
				}
			}
			if hasSymlink {
				info, statErr := os.Lstat(
					filepath.Join(roots.CandidateRoot, "docs", "readme-link"),
				)
				if statErr != nil || info.Mode()&os.ModeSymlink == 0 {
					t.Fatalf("candidate symlink info = %#v, %v", info, statErr)
				}
				target, readErr := os.Readlink(
					filepath.Join(roots.CandidateRoot, "docs", "readme-link"),
				)
				if readErr != nil || target != "../README.md" {
					t.Fatalf("candidate symlink target = %q, %v", target, readErr)
				}
			}
			encoded, marshalErr := json.Marshal(roots)
			if marshalErr != nil {
				t.Fatal(marshalErr)
			}
			if strings.Contains(string(encoded), roots.ParentRoot) ||
				strings.Contains(string(encoded), roots.CandidateRoot) {
				t.Fatalf("serialized validation evidence exposes roots: %s", encoded)
			}
			var evidence map[string]any
			if unmarshalErr := json.Unmarshal(encoded, &evidence); unmarshalErr != nil {
				t.Fatal(unmarshalErr)
			}
			if evidence["repository"] != fixture.pin.Repository {
				t.Fatalf("serialized repository evidence = %#v", evidence["repository"])
			}
			return nil
		},
	)
	if err != nil {
		t.Fatalf("WithPinnedCandidateValidationRoots() error = %v", err)
	}
	if _, statErr := os.Lstat(disposableRoot); !os.IsNotExist(statErr) {
		t.Fatalf("disposable root remains after callback: %v", statErr)
	}
	if _, gitDirErr := os.Lstat(filepath.Join(fixture.workspace.Path, ".git")); gitDirErr != nil {
		t.Fatalf("retained checkout Git directory changed: %v", gitDirErr)
	}

	err = fixture.manager.WithPinnedCandidateValidationRoots(
		context.Background(),
		request,
		func(_ context.Context, roots PinnedCandidateValidationRoots) error {
			if roots.ParentManifest != first.ParentManifest ||
				roots.CandidateManifest != first.CandidateManifest {
				t.Fatalf("repeated manifests = %#v, first = %#v", roots, first)
			}
			if filepath.Dir(roots.ParentRoot) == disposableRoot {
				t.Fatal("repeated validation reused a disposable root")
			}
			return nil
		},
	)
	if err != nil {
		t.Fatalf("repeated WithPinnedCandidateValidationRoots() error = %v", err)
	}
}

func TestManagerPinnedCandidateValidationRejectsStaleEvidence(t *testing.T) {
	fixture := newPinnedCommitTestFixture(t, "pr-development/validation-stale")
	if err := os.WriteFile(
		filepath.Join(fixture.workspace.Path, "change.txt"),
		[]byte("candidate\n"),
		0o644,
	); err != nil {
		t.Fatal(err)
	}
	candidate := fixture.snapshot(t)
	valid := pinnedValidationRequest(fixture, candidate)
	tests := []struct {
		name   string
		mutate func(*PinnedCandidateValidationRequest)
	}{
		{name: "workspace", mutate: func(request *PinnedCandidateValidationRequest) {
			request.WorkspaceID = "different-workspace"
		}},
		{name: "parent", mutate: func(request *PinnedCandidateValidationRequest) {
			request.ExpectedParent = differentPinnedValidationHash(request.ExpectedParent)
		}},
		{name: "tree", mutate: func(request *PinnedCandidateValidationRequest) {
			request.ExpectedTree = differentPinnedValidationHash(request.ExpectedTree)
		}},
		{name: "candidate digest", mutate: func(request *PinnedCandidateValidationRequest) {
			request.ExpectedCandidateDigest = differentPinnedValidationHash(
				request.ExpectedCandidateDigest,
			)
		}},
		{name: "pin", mutate: func(request *PinnedCandidateValidationRequest) {
			request.Pin.ExpectedCommit = differentPinnedValidationHash(request.Pin.ExpectedCommit)
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := valid
			test.mutate(&request)
			called := false
			err := fixture.manager.WithPinnedCandidateValidationRoots(
				context.Background(),
				request,
				func(context.Context, PinnedCandidateValidationRoots) error {
					called = true
					return nil
				},
			)
			if !errors.Is(err, ErrPinnedCommitConflict) {
				t.Fatalf("error = %v, want ErrPinnedCommitConflict", err)
			}
			if called {
				t.Fatal("callback ran with stale validation evidence")
			}
		})
	}

	invalid := valid
	invalid.ExpectedCandidateDigest = strings.ToUpper(invalid.ExpectedCandidateDigest)
	err := fixture.manager.WithPinnedCandidateValidationRoots(
		context.Background(),
		invalid,
		func(context.Context, PinnedCandidateValidationRoots) error { return nil },
	)
	if !errors.Is(err, ErrPinnedCommitInvalid) {
		t.Fatalf("malformed evidence error = %v, want ErrPinnedCommitInvalid", err)
	}
	err = fixture.manager.WithPinnedCandidateValidationRoots(
		context.Background(),
		valid,
		nil,
	)
	if !errors.Is(err, ErrPinnedCommitInvalid) {
		t.Fatalf("nil callback error = %v, want ErrPinnedCommitInvalid", err)
	}
}

func TestManagerPinnedCandidateValidationDetachedPostflight(t *testing.T) {
	fixture := newPinnedCommitTestFixture(t, "pr-development/validation-postflight")
	if err := os.WriteFile(
		filepath.Join(fixture.workspace.Path, "change.txt"),
		[]byte("candidate\n"),
		0o644,
	); err != nil {
		t.Fatal(err)
	}
	candidate := fixture.snapshot(t)
	request := pinnedValidationRequest(fixture, candidate)
	ctx, cancel := context.WithCancel(context.Background())
	var root string
	err := fixture.manager.WithPinnedCandidateValidationRoots(
		ctx,
		request,
		func(_ context.Context, roots PinnedCandidateValidationRoots) error {
			root = filepath.Dir(roots.ParentRoot)
			if err := os.WriteFile(
				filepath.Join(roots.CandidateRoot, "callback-drift.txt"),
				[]byte("drift\n"),
				0o644,
			); err != nil {
				return err
			}
			if err := os.WriteFile(
				filepath.Join(fixture.workspace.Path, "postflight-drift.txt"),
				[]byte("drift\n"),
				0o644,
			); err != nil {
				return err
			}
			cancel()
			return context.Canceled
		},
	)
	if !errors.Is(err, context.Canceled) || !errors.Is(err, ErrPinnedCommitConflict) ||
		!strings.Contains(err.Error(), "disposable validation roots changed") {
		t.Fatalf("postflight error = %v, want cancellation and conflict", err)
	}
	if _, statErr := os.Lstat(root); !os.IsNotExist(statErr) {
		t.Fatalf("postflight left disposable root: %v", statErr)
	}
}

func TestManagerPinnedCandidateValidationRejectsCallbackRootMutation(t *testing.T) {
	fixture := newPinnedCommitTestFixture(t, "pr-development/validation-root-postflight")
	for name, content := range map[string]string{
		"change.txt":     "candidate\n",
		"same-a.txt":     "identical\n",
		"same-b.txt":     "identical\n",
		"dir/nested.txt": "nested\n",
	} {
		if err := os.MkdirAll(filepath.Dir(filepath.Join(fixture.workspace.Path, name)), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(fixture.workspace.Path, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	hasSymlink := os.Symlink(
		"change.txt",
		filepath.Join(fixture.workspace.Path, "change-link"),
	) == nil
	request := pinnedValidationRequest(fixture, fixture.snapshot(t))
	changeInfo, err := os.Lstat(filepath.Join(fixture.workspace.Path, "change.txt"))
	if err != nil {
		t.Fatal(err)
	}
	hasChangeToken := pinnedValidationNodeChangeToken(changeInfo).valid
	callbackErr := errors.New("callback rejected validation")
	outside := t.TempDir()
	canaryPath := filepath.Join(outside, "canary.txt")
	if err := os.WriteFile(canaryPath, []byte("do not touch\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name            string
		unixOnly        bool
		requiresSymlink bool
		requiresSpecial bool
		requiresChange  bool
		wantCallbackErr bool
		mutate          func(PinnedCandidateValidationRoots) error
	}{
		{
			name: "candidate content",
			mutate: func(roots PinnedCandidateValidationRoots) error {
				return os.WriteFile(filepath.Join(roots.CandidateRoot, "change.txt"), []byte("tampered!\n"), 0o644)
			},
		},
		{
			name: "parent content",
			mutate: func(roots PinnedCandidateValidationRoots) error {
				return os.WriteFile(filepath.Join(roots.ParentRoot, "README.md"), []byte("tampered\n"), 0o644)
			},
		},
		{
			name:           "content mutate and restore",
			requiresChange: true,
			mutate: func(roots PinnedCandidateValidationRoots) error {
				name := filepath.Join(roots.CandidateRoot, "change.txt")
				if err := os.WriteFile(name, []byte("temporary\n"), 0o644); err != nil {
					return err
				}
				return os.WriteFile(name, []byte("candidate\n"), 0o644)
			},
		},
		{
			name:            "added leaf and callback error",
			wantCallbackErr: true,
			mutate: func(roots PinnedCandidateValidationRoots) error {
				if err := os.WriteFile(
					filepath.Join(roots.CandidateRoot, "added.txt"),
					[]byte("added\n"),
					0o644,
				); err != nil {
					return err
				}
				return callbackErr
			},
		},
		{
			name: "deleted leaf",
			mutate: func(roots PinnedCandidateValidationRoots) error {
				return os.Remove(filepath.Join(roots.CandidateRoot, "change.txt"))
			},
		},
		{
			name: "renamed leaf",
			mutate: func(roots PinnedCandidateValidationRoots) error {
				return os.Rename(
					filepath.Join(roots.CandidateRoot, "change.txt"),
					filepath.Join(roots.CandidateRoot, "renamed.txt"),
				)
			},
		},
		{
			name: "added empty directory",
			mutate: func(roots PinnedCandidateValidationRoots) error {
				return os.Mkdir(filepath.Join(roots.CandidateRoot, "empty"), 0o700)
			},
		},
		{
			name:     "mode",
			unixOnly: true,
			mutate: func(roots PinnedCandidateValidationRoots) error {
				return os.Chmod(filepath.Join(roots.CandidateRoot, "change.txt"), 0o600)
			},
		},
		{
			name: "same content replacement",
			mutate: func(roots PinnedCandidateValidationRoots) error {
				name := filepath.Join(roots.CandidateRoot, "change.txt")
				replacement := filepath.Join(roots.CandidateRoot, "replacement.tmp")
				if err := os.WriteFile(replacement, []byte("candidate\n"), 0o644); err != nil {
					return err
				}
				if err := os.Remove(name); err != nil {
					return err
				}
				return os.Rename(replacement, name)
			},
		},
		{
			name: "same content inode swap",
			mutate: func(roots PinnedCandidateValidationRoots) error {
				left := filepath.Join(roots.CandidateRoot, "same-a.txt")
				right := filepath.Join(roots.CandidateRoot, "same-b.txt")
				temporary := filepath.Join(roots.CandidateRoot, "same.tmp")
				if err := os.Rename(left, temporary); err != nil {
					return err
				}
				if err := os.Rename(right, left); err != nil {
					return err
				}
				return os.Rename(temporary, right)
			},
		},
		{
			name: "hardlink",
			mutate: func(roots PinnedCandidateValidationRoots) error {
				left := filepath.Join(roots.CandidateRoot, "same-a.txt")
				right := filepath.Join(roots.CandidateRoot, "same-b.txt")
				if err := os.Remove(right); err != nil {
					return err
				}
				return os.Link(left, right)
			},
		},
		{
			name: "external hardlink",
			mutate: func(roots PinnedCandidateValidationRoots) error {
				return os.Link(
					filepath.Join(roots.CandidateRoot, "same-a.txt"),
					filepath.Join(outside, "external-hardlink.txt"),
				)
			},
		},
		{
			name:            "regular to outside symlink",
			requiresSymlink: true,
			mutate: func(roots PinnedCandidateValidationRoots) error {
				name := filepath.Join(roots.CandidateRoot, "change.txt")
				if err := os.Remove(name); err != nil {
					return err
				}
				return os.Symlink(canaryPath, name)
			},
		},
		{
			name:            "directory to outside symlink",
			requiresSymlink: true,
			mutate: func(roots PinnedCandidateValidationRoots) error {
				name := filepath.Join(roots.CandidateRoot, "dir")
				if err := os.RemoveAll(name); err != nil {
					return err
				}
				return os.Symlink(outside, name)
			},
		},
		{
			name:            "symlink target",
			requiresSymlink: true,
			mutate: func(roots PinnedCandidateValidationRoots) error {
				name := filepath.Join(roots.CandidateRoot, "change-link")
				if err := os.Remove(name); err != nil {
					return err
				}
				return os.Symlink("same-a.txt", name)
			},
		},
		{
			name:            "regular to special node",
			requiresSpecial: true,
			mutate: func(roots PinnedCandidateValidationRoots) error {
				name := filepath.Join(roots.CandidateRoot, "change.txt")
				if err := os.Remove(name); err != nil {
					return err
				}
				return createPinnedValidationSpecialNode(name)
			},
		},
		{
			name: "parent candidate root swap",
			mutate: func(roots PinnedCandidateValidationRoots) error {
				base := filepath.Dir(roots.ParentRoot)
				temporary := filepath.Join(base, "swap.tmp")
				if err := os.Rename(roots.ParentRoot, temporary); err != nil {
					return err
				}
				if err := os.Rename(roots.CandidateRoot, roots.ParentRoot); err != nil {
					return err
				}
				return os.Rename(temporary, roots.CandidateRoot)
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if test.unixOnly && runtime.GOOS == "windows" {
				t.Skip("requires Unix filesystem semantics")
			}
			if test.requiresSymlink && !hasSymlink {
				t.Skip("symlinks are unavailable")
			}
			if test.requiresSpecial && !pinnedValidationSpecialNodeSupported {
				t.Skip("special filesystem nodes are unavailable")
			}
			if test.requiresChange && !hasChangeToken {
				t.Skip("stable change metadata is unavailable")
			}
			var disposableRoot string
			err := fixture.manager.WithPinnedCandidateValidationRoots(
				context.Background(),
				request,
				func(_ context.Context, roots PinnedCandidateValidationRoots) error {
					disposableRoot = filepath.Dir(roots.ParentRoot)
					return test.mutate(roots)
				},
			)
			if !errors.Is(err, ErrPinnedCommitConflict) {
				t.Fatalf("mutation postflight error = %v, want ErrPinnedCommitConflict", err)
			}
			if test.wantCallbackErr && !errors.Is(err, callbackErr) {
				t.Fatalf("mutation error = %v, want callback error joined", err)
			}
			if _, statErr := os.Lstat(disposableRoot); !os.IsNotExist(statErr) {
				t.Fatalf("mutation postflight left disposable root: %v", statErr)
			}
			canary, readErr := os.ReadFile(canaryPath)
			if readErr != nil || string(canary) != "do not touch\n" {
				t.Fatalf("outside canary = %q, %v", canary, readErr)
			}
		})
	}
}

func TestManagerPinnedCandidateValidationPostflightRejectsControlPlaneDrift(t *testing.T) {
	fixture := newPinnedCommitTestFixture(t, "pr-development/validation-control-postflight")
	if err := os.WriteFile(
		filepath.Join(fixture.workspace.Path, "change.txt"),
		[]byte("candidate\n"),
		0o644,
	); err != nil {
		t.Fatal(err)
	}
	request := pinnedValidationRequest(fixture, fixture.snapshot(t))
	lockPath := filepath.Join(fixture.workspace.Path, ".git", "HEAD.lock")
	var root string
	err := fixture.manager.WithPinnedCandidateValidationRoots(
		context.Background(),
		request,
		func(_ context.Context, roots PinnedCandidateValidationRoots) error {
			root = filepath.Dir(roots.ParentRoot)
			return os.WriteFile(lockPath, []byte("stale\n"), 0o600)
		},
	)
	if !errors.Is(err, ErrPinnedCommitConflict) || !strings.Contains(err.Error(), "HEAD.lock") {
		t.Fatalf("control-plane postflight error = %v", err)
	}
	if _, statErr := os.Lstat(root); !os.IsNotExist(statErr) {
		t.Fatalf("control-plane postflight left disposable root: %v", statErr)
	}
	if err := os.Remove(lockPath); err != nil {
		t.Fatal(err)
	}
}

func TestManagerPinnedCandidateValidationCleansAfterCallbackFailureAndPanic(t *testing.T) {
	fixture := newPinnedCommitTestFixture(t, "pr-development/validation-cleanup")
	if err := os.WriteFile(
		filepath.Join(fixture.workspace.Path, "change.txt"),
		[]byte("candidate\n"),
		0o644,
	); err != nil {
		t.Fatal(err)
	}
	request := pinnedValidationRequest(fixture, fixture.snapshot(t))
	callbackErr := errors.New("validation callback failed")
	var failedRoot string
	err := fixture.manager.WithPinnedCandidateValidationRoots(
		context.Background(),
		request,
		func(_ context.Context, roots PinnedCandidateValidationRoots) error {
			failedRoot = filepath.Dir(roots.ParentRoot)
			return callbackErr
		},
	)
	if !errors.Is(err, callbackErr) {
		t.Fatalf("callback error = %v, want %v", err, callbackErr)
	}
	if _, statErr := os.Lstat(failedRoot); !os.IsNotExist(statErr) {
		t.Fatalf("callback failure left disposable root: %v", statErr)
	}

	var panicRoot string
	var recovered any
	func() {
		defer func() { recovered = recover() }()
		_ = fixture.manager.WithPinnedCandidateValidationRoots(
			context.Background(),
			request,
			func(_ context.Context, roots PinnedCandidateValidationRoots) error {
				panicRoot = filepath.Dir(roots.ParentRoot)
				panic("validation panic")
			},
		)
	}()
	if recovered != "validation panic" {
		t.Fatalf("recovered panic = %#v", recovered)
	}
	if _, statErr := os.Lstat(panicRoot); !os.IsNotExist(statErr) {
		t.Fatalf("callback panic left disposable root: %v", statErr)
	}
	if _, err := fixture.manager.SnapshotPinnedCandidate(
		context.Background(),
		PinnedCandidateRequest{Pin: fixture.pin, WorkspaceID: fixture.workspace.ID},
	); err != nil {
		t.Fatalf("operation lock remained held after callback panic: %v", err)
	}
}

func TestManagerPinnedCandidateValidationRejectsUnsafeSymlinks(t *testing.T) {
	temporaryRoot := t.TempDir()
	for _, variable := range []string{"TMPDIR", "TMP", "TEMP"} {
		t.Setenv(variable, temporaryRoot)
	}
	tests := []struct {
		name    string
		links   map[string]string
		message string
	}{
		{
			name:    "escape",
			links:   map[string]string{"escape": "../outside"},
			message: "escapes",
		},
		{
			name:    "Git control alias",
			links:   map[string]string{"control": ".GiT /config"},
			message: "unsafe target component",
		},
		{
			name:    "recursive",
			links:   map[string]string{"self": "self"},
			message: "recursive",
		},
		{
			name:    "chain",
			links:   map[string]string{"first": "README.md", "second": "first"},
			message: "targets another symlink",
		},
		{
			name:    "case variant chain",
			links:   map[string]string{"Link": "README.md", "second": "link"},
			message: "targets another symlink",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			before := pinnedValidationTemporaryRootSet(t)
			fixture := newPinnedCommitTestFixture(
				t,
				"pr-development/validation-symlink-"+strings.ReplaceAll(test.name, " ", "-"),
			)
			for name, target := range test.links {
				if err := os.Symlink(target, filepath.Join(fixture.workspace.Path, name)); err != nil {
					t.Skipf("symlinks are unavailable: %v", err)
				}
			}
			candidate := fixture.snapshot(t)
			called := false
			err := fixture.manager.WithPinnedCandidateValidationRoots(
				context.Background(),
				pinnedValidationRequest(fixture, candidate),
				func(context.Context, PinnedCandidateValidationRoots) error {
					called = true
					return nil
				},
			)
			if !errors.Is(err, ErrPinnedCommitConflict) ||
				!strings.Contains(err.Error(), test.message) {
				t.Fatalf("unsafe symlink error = %v, want conflict containing %q", err, test.message)
			}
			if called {
				t.Fatal("callback ran with an unsafe symlink")
			}
			for root := range pinnedValidationTemporaryRootSet(t) {
				if _, existed := before[root]; !existed {
					t.Fatalf("failed materialization leaked temporary root %q", root)
				}
			}
		})
	}
}

func TestManagerPinnedCandidateValidationRejectsCaseCollisions(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("requires a case-sensitive test filesystem")
	}
	tests := []struct {
		name  string
		files map[string]string
	}{
		{name: "ASCII", files: map[string]string{"Case.txt": "upper\n", "case.txt": "lower\n"}},
		{name: "Unicode simple fold", files: map[string]string{"Σ.txt": "sigma\n", "ς.txt": "final sigma\n"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newPinnedCommitTestFixture(
				t,
				"pr-development/validation-case-collision-"+strings.ReplaceAll(test.name, " ", "-"),
			)
			for name, content := range test.files {
				if err := os.WriteFile(
					filepath.Join(fixture.workspace.Path, name),
					[]byte(content),
					0o644,
				); err != nil {
					t.Fatal(err)
				}
			}
			candidate := fixture.snapshot(t)
			called := false
			err := fixture.manager.WithPinnedCandidateValidationRoots(
				context.Background(),
				pinnedValidationRequest(fixture, candidate),
				func(context.Context, PinnedCandidateValidationRoots) error {
					called = true
					return nil
				},
			)
			if !errors.Is(err, ErrPinnedCommitConflict) ||
				!strings.Contains(err.Error(), "collide") {
				t.Fatalf("case-collision error = %v", err)
			}
			if called {
				t.Fatal("callback ran with case-colliding paths")
			}
		})
	}
}

func TestPinnedCandidateValidationRejectsGitlinkTrees(t *testing.T) {
	repository := initSourceRepo(t)
	commit := testGitCommit(t, repository, "HEAD")
	input := fmt.Sprintf("160000 commit %s\tmodule\n", commit)
	output, err := runPinnedGitPlumbing(
		context.Background(),
		repository,
		nil,
		strings.NewReader(input),
		maxPinnedCommitGitOutputBytes,
		"mktree",
	)
	if err != nil {
		t.Fatalf("create Gitlink tree: %v", err)
	}
	tree := strings.TrimSpace(string(output))
	if !validPinnedCommit(tree) {
		t.Fatalf("Gitlink tree = %q", tree)
	}
	_, err = listPinnedValidationTree(context.Background(), repository, tree, nil)
	if !errors.Is(err, ErrPinnedCommitConflict) || !strings.Contains(err.Error(), "Git links") {
		t.Fatalf("Gitlink validation error = %v", err)
	}
}

func TestPinnedValidationPathAndOutputBounds(t *testing.T) {
	unsafePaths := []string{
		".git/config",
		"nested/.GiT /config",
		"../outside",
		"/absolute",
		`windows\path`,
		"a/./b",
		"CON/file",
		"stream:name",
		"trailing./file",
		"control\nname",
	}
	for _, value := range unsafePaths {
		t.Run(fmt.Sprintf("path %q", value), func(t *testing.T) {
			if err := validatePinnedValidationPath(value); !errors.Is(err, ErrPinnedCommitConflict) {
				t.Fatalf("validatePinnedValidationPath(%q) error = %v", value, err)
			}
		})
	}
	if err := validatePinnedValidationPath("safe dir/nested-file.txt"); err != nil {
		t.Fatalf("safe path error = %v", err)
	}

	var output strings.Builder
	writer := &pinnedValidationLimitWriter{writer: &output, limit: 3}
	written, err := writer.Write([]byte("abcd"))
	if !errors.Is(err, errPinnedValidationOutputLimit) || written != 3 ||
		output.String() != "abc" || !writer.exceeded || writer.written != 3 {
		t.Fatalf(
			"bounded write = (%d, %v), output %q, writer %#v",
			written,
			err,
			output.String(),
			writer,
		)
	}
}

func TestPinnedValidationRegularOpenIsNoFollowAndNonblocking(t *testing.T) {
	if !pinnedValidationSpecialNodeSupported {
		t.Skip("requires no-follow and nonblocking Unix open flags")
	}
	path := t.TempDir()
	root, err := os.OpenRoot(path)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	if specialErr := createPinnedValidationSpecialNode(filepath.Join(path, "pipe")); specialErr != nil {
		t.Fatal(specialErr)
	}
	directory, err := root.Open(".")
	if err != nil {
		t.Fatal(err)
	}
	defer directory.Close()
	started := time.Now()
	file, openErr := openPinnedValidationRegular(directory, root, "pipe")
	if elapsed := time.Since(started); elapsed > 2*time.Second {
		t.Fatalf("nonblocking special-file open took %s", elapsed)
	}
	if openErr == nil {
		info, statErr := file.Stat()
		closeErr := file.Close()
		if statErr != nil || info.Mode().IsRegular() || closeErr != nil {
			t.Fatalf("special file info = %#v, stat = %v, close = %v", info, statErr, closeErr)
		}
	}
	if err := os.WriteFile(filepath.Join(path, "target"), []byte("canary\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("target", filepath.Join(path, "link")); err != nil {
		t.Skipf("symlinks are unavailable: %v", err)
	}
	if followed, err := openPinnedValidationRegular(directory, root, "link"); err == nil {
		_ = followed.Close()
		t.Fatal("regular-file open followed a symbolic link")
	}
}

func pinnedValidationRequest(
	fixture pinnedCommitTestFixture,
	candidate PinnedCandidate,
) PinnedCandidateValidationRequest {
	return PinnedCandidateValidationRequest{
		Pin:                     fixture.pin,
		WorkspaceID:             fixture.workspace.ID,
		ExpectedParent:          candidate.ParentCommit,
		ExpectedTree:            candidate.Tree,
		ExpectedCandidateDigest: candidate.CandidateDigest,
	}
}

func differentPinnedValidationHash(value string) string {
	if value[0] == '0' {
		return "1" + value[1:]
	}
	return "0" + value[1:]
}

func pinnedValidationTestManifest(
	tree string,
	entries []pinnedValidationTestManifestEntry,
) PinnedTreeManifest {
	digest := sha256.New()
	_, _ = digest.Write([]byte("picoclaw-pinned-validation-tree-v1\x00"))
	writePinnedDigestField(digest, tree)
	var total int64
	for _, entry := range entries {
		contentDigest := sha256.Sum256([]byte(entry.content))
		writePinnedDigestField(digest, entry.path)
		writePinnedDigestField(digest, entry.mode)
		writePinnedDigestField(digest, entry.kind)
		writePinnedDigestField(digest, fmt.Sprintf("%d", len(entry.content)))
		writePinnedDigestField(digest, hex.EncodeToString(contentDigest[:]))
		total += int64(len(entry.content))
	}
	writePinnedDigestField(digest, fmt.Sprintf("%d", len(entries)))
	writePinnedDigestField(digest, fmt.Sprintf("%d", total))
	return PinnedTreeManifest{
		Tree:    tree,
		Digest:  hex.EncodeToString(digest.Sum(nil)),
		Entries: len(entries),
		Bytes:   total,
	}
}

func pinnedValidationTemporaryRootSet(t *testing.T) map[string]struct{} {
	t.Helper()
	paths, err := filepath.Glob(filepath.Join(os.TempDir(), pinnedValidationRootPrefix+"*"))
	if err != nil {
		t.Fatal(err)
	}
	result := make(map[string]struct{}, len(paths))
	for _, candidate := range paths {
		result[candidate] = struct{}{}
	}
	return result
}

func TestPinnedValidationPostflightTimeoutIsBounded(t *testing.T) {
	if pinnedValidationPostflightTimeout <= 0 ||
		pinnedValidationPostflightTimeout > time.Minute {
		t.Fatalf("pinned validation postflight timeout = %s", pinnedValidationPostflightTimeout)
	}
}
