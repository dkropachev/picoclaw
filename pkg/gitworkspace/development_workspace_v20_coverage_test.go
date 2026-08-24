package gitworkspace

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestPushPinnedLineCreateOnlyDestinationContracts(t *testing.T) {
	t.Run("create and replay", func(t *testing.T) {
		fixture := newPinnedLineBareRemotePushFixture(t, "development-v20/create-destination")
		request := fixture.request
		request.DestinationRef = "feature/development-v20"
		request.CreateDestination = true

		result, err := fixture.manager.PushPinnedLine(context.Background(), request)
		if err != nil {
			t.Fatalf("PushPinnedLine(create destination) error = %v", err)
		}
		if result.Disposition != PinnedLinePushApplied ||
			result.RemoteRef != "refs/heads/feature/development-v20" ||
			result.RemoteTip != fixture.parked.Tip {
			t.Fatalf("PushPinnedLine(create destination) = %#v", result)
		}
		got, resolveErr := runPinnedLineBareRemoteGit(
			t,
			fixture.remote,
			"rev-parse",
			"--verify",
			"refs/heads/feature/development-v20^{commit}",
		)
		if resolveErr != nil || got != fixture.parked.Tip {
			t.Fatalf("created destination tip = %q, want %q", got, fixture.parked.Tip)
		}
		got, resolveErr = runPinnedLineBareRemoteGit(
			t,
			fixture.remote,
			"rev-parse",
			"--verify",
			"refs/heads/"+fixture.pin.SourceRef+"^{commit}",
		)
		if resolveErr != nil || got != request.ExpectedRemoteTip {
			t.Fatalf("source branch changed to %q, want %q", got, request.ExpectedRemoteTip)
		}

		replay, err := fixture.manager.PushPinnedLine(context.Background(), request)
		if err != nil || replay.Disposition != PinnedLinePushAlreadyCurrent {
			t.Fatalf("PushPinnedLine(create replay) = %#v, %v", replay, err)
		}
	})

	t.Run("existing destination is pre-effect conflict", func(t *testing.T) {
		fixture := newPinnedLineBareRemotePushFixture(t, "development-v20/existing-destination")
		before := pinnedLineBareRemoteRefs(t, fixture.remote)
		request := fixture.request
		request.DestinationRef = "base-protected"
		request.CreateDestination = true
		result, err := fixture.manager.PushPinnedLine(context.Background(), request)
		if result != (PinnedLinePushResult{}) || !errors.Is(err, ErrPinnedLineConflict) {
			t.Fatalf("PushPinnedLine(existing destination) = %#v, %v", result, err)
		}
		if after := pinnedLineBareRemoteRefs(t, fixture.remote); after != before {
			t.Fatalf("existing-destination conflict mutated refs: before %q, after %q", before, after)
		}
	})

	t.Run("missing non-create destination", func(t *testing.T) {
		fixture := newPinnedLineBareRemotePushFixture(t, "development-v20/missing-destination")
		request := fixture.request
		request.DestinationRef = "feature/missing"
		result, err := fixture.manager.PushPinnedLine(context.Background(), request)
		if result != (PinnedLinePushResult{}) || !errors.Is(err, ErrPinnedLineConflict) {
			t.Fatalf("PushPinnedLine(missing destination) = %#v, %v", result, err)
		}
	})

	t.Run("invalid destinations", func(t *testing.T) {
		fixture := newPinnedLineBareRemotePushFixture(t, "development-v20/invalid-destination")
		for name, mutate := range map[string]func(*PinnedLinePushRequest){
			"blank create": func(request *PinnedLinePushRequest) { request.CreateDestination = true },
			"same as source": func(request *PinnedLinePushRequest) {
				request.DestinationRef = request.SourceRef
				request.CreateDestination = true
			},
			"whitespace": func(request *PinnedLinePushRequest) { request.DestinationRef = " feature/v20" },
			"unsafe":     func(request *PinnedLinePushRequest) { request.DestinationRef = "../feature" },
			"oversized":  func(request *PinnedLinePushRequest) { request.DestinationRef = strings.Repeat("a", 241) },
		} {
			t.Run(name, func(t *testing.T) {
				request := fixture.request
				mutate(&request)
				result, err := fixture.manager.PushPinnedLine(context.Background(), request)
				if result != (PinnedLinePushResult{}) || !errors.Is(err, ErrPinnedLineInvalid) {
					t.Fatalf("PushPinnedLine(%s) = %#v, %v", name, result, err)
				}
			})
		}
	})
}

func TestSnapshotPinnedPlanningEvidenceBoundsMixedRepositoryContent(t *testing.T) {
	ctx := context.Background()
	source := initSourceRepo(t)
	writePlanningFixture := func(path string, content []byte) {
		t.Helper()
		fullPath := filepath.Join(source, filepath.FromSlash(path))
		if err := os.MkdirAll(filepath.Dir(fullPath), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(fullPath, content, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	writePlanningFixture("00-readable.go", []byte("package readable\n"))
	writePlanningFixture("01-binary.go", []byte{'p', 'a', 'c', 'k', 'a', 'g', 'e', 0, 'x'})
	writePlanningFixture("02-oversized.go", []byte(strings.Repeat("x", maxPlanningFileBytes+1)))
	writePlanningFixture("03-plain.bin", []byte("plain text but unsupported extension\n"))
	writePlanningFixture("vendor/04-vendored.go", []byte("package vendored\n"))
	for index := 0; index < maxPlanningFiles+10; index++ {
		writePlanningFixture(
			fmt.Sprintf("zz-generated/%03d.go", index),
			[]byte(fmt.Sprintf("package generated // %d\n", index)),
		)
	}
	if _, err := runGit(ctx, source, "add", "-A"); err != nil {
		t.Fatal(err)
	}
	if _, err := runGit(ctx, source, "commit", "-m", "add mixed planning corpus"); err != nil {
		t.Fatal(err)
	}

	now := time.Date(2026, 8, 25, 1, 0, 0, 0, time.UTC)
	manager := newTestManager(t, &now)
	pin := PinnedAcquireRequest{
		Repository: source, SourceRef: "main", ExpectedCommit: testGitCommit(t, source, "HEAD"),
		ReservationKey: "development-v20/planning-evidence", AgentID: "planner",
	}
	workspace, err := manager.AcquirePinned(ctx, pin)
	if err != nil {
		t.Fatalf("AcquirePinned() error = %v", err)
	}
	evidence, err := manager.SnapshotPinnedPlanningEvidence(nil, PinnedCandidateRequest{
		Pin: pin, WorkspaceID: workspace.ID,
	})
	if err != nil {
		t.Fatalf("SnapshotPinnedPlanningEvidence() error = %v", err)
	}
	if !evidence.Truncated || len(evidence.Files) != maxPlanningFiles || evidence.Commit != pin.ExpectedCommit {
		t.Fatalf("bounded planning evidence = commit %q, files %d, truncated %v",
			evidence.Commit, len(evidence.Files), evidence.Truncated)
	}
	files := make(map[string]PlanningEvidenceFile, len(evidence.Files))
	for _, file := range evidence.Files {
		files[file.Path] = file
	}
	if files["00-readable.go"].Content != "package readable\n" {
		t.Fatalf("readable evidence = %#v", files["00-readable.go"])
	}
	for _, path := range []string{"01-binary.go", "02-oversized.go", "03-plain.bin", "vendor/04-vendored.go"} {
		if file, ok := files[path]; !ok || file.Content != "" {
			t.Fatalf("bounded non-text evidence %q = %#v, present %v", path, file, ok)
		}
	}

	var nilManager *Manager
	if _, err := nilManager.SnapshotPinnedPlanningEvidence(ctx, PinnedCandidateRequest{}); err == nil {
		t.Fatal("nil manager planning evidence unexpectedly succeeded")
	}
	invalid := PinnedCandidateRequest{Pin: pin, WorkspaceID: ""}
	if _, err := manager.SnapshotPinnedPlanningEvidence(ctx, invalid); !errors.Is(err, ErrPinnedLineInvalid) {
		t.Fatalf("invalid planning request error = %v", err)
	}
}

func TestPlanningTextCandidateLanguageAndGeneratedFilters(t *testing.T) {
	for _, path := range []string{
		"cmd/main.go", "web/app.TSX", "scripts/release.sh", "Dockerfile", "build/Makefile",
	} {
		if !planningTextCandidate(path) {
			t.Fatalf("planningTextCandidate(%q) = false", path)
		}
	}
	for _, path := range []string{
		"vendor/module.go", "node_modules/package/index.js", "dist/app.js", ".git/config", "assets/image.png",
	} {
		if planningTextCandidate(path) {
			t.Fatalf("planningTextCandidate(%q) = true", path)
		}
	}
}

func TestPinnedBrowseDefensiveContracts(t *testing.T) {
	fixture := newPinnedLineBrowseTestFixture(t, "development-v20/browse-defensive", 0)
	ctx := context.Background()

	for name, request := range map[string]PinnedLineTreeRequest{
		"invalid revision": {
			PinnedLineBrowseFence: fixture.fence, Revision: "unknown",
		},
		"cursor whitespace": {
			PinnedLineBrowseFence: fixture.fence, Revision: "candidate", After: " README.md",
		},
		"missing directory": {
			PinnedLineBrowseFence: fixture.fence, Revision: "candidate", Path: "missing",
		},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := fixture.manager.ListPinnedLineTree(ctx, request); err == nil {
				t.Fatalf("ListPinnedLineTree(%s) unexpectedly succeeded", name)
			}
		})
	}
	if _, err := fixture.manager.ReadPinnedLineBlob(ctx, PinnedLineBlobRequest{
		PinnedLineBrowseFence: fixture.fence, Revision: "candidate", Path: "missing.txt",
	}); !errors.Is(err, ErrPinnedLineConflict) {
		t.Fatalf("ReadPinnedLineBlob(missing) error = %v", err)
	}

	if err := fixture.manager.withPinnedLineBrowse(ctx, fixture.fence, nil); !errors.Is(err, ErrPinnedLineInvalid) {
		t.Fatalf("withPinnedLineBrowse(nil read) error = %v", err)
	}
	var nilManager *Manager
	if err := nilManager.withPinnedLineBrowse(ctx, fixture.fence, func(context.Context, string, []string) error {
		return nil
	}); !errors.Is(err, ErrPinnedLineInvalid) {
		t.Fatalf("nil manager browse error = %v", err)
	}
	if err := fixture.manager.withPinnedLineBrowse(nil, fixture.fence, func(context.Context, string, []string) error {
		return errors.New("reader stopped")
	}); err == nil || err.Error() != "reader stopped" {
		t.Fatalf("browse reader error = %v", err)
	}
}

func TestPushPinnedLineFastValidationAndTransportContracts(t *testing.T) {
	fixture := newPinnedLinePushTestFixture(t, "development-v20/push-validation")
	ctx := context.Background()

	var nilManager *Manager
	if _, err := nilManager.PushPinnedLine(ctx, fixture.request); err == nil {
		t.Fatal("nil manager push unexpectedly succeeded")
	}
	if _, err := fixture.manager.PushPinnedLine(nil, PinnedLinePushRequest{}); !errors.Is(err, ErrPinnedLineInvalid) {
		t.Fatalf("nil-context invalid push error = %v", err)
	}
	for name, mutate := range map[string]func(*PinnedLinePushRequest){
		"blank repository":      func(request *PinnedLinePushRequest) { request.Repository = "" },
		"repository whitespace": func(request *PinnedLinePushRequest) { request.Repository += " " },
		"bad source":            func(request *PinnedLinePushRequest) { request.SourceRef = "../main" },
		"blank workspace":       func(request *PinnedLinePushRequest) { request.WorkspaceID = "" },
		"zero version":          func(request *PinnedLinePushRequest) { request.ExpectedVersion = 0 },
		"wrong epoch":           func(request *PinnedLinePushRequest) { request.ExpectedMutationEpoch++ },
		"zero remote": func(request *PinnedLinePushRequest) {
			request.ExpectedRemoteTip = strings.Repeat("0", len(request.ExpectedRemoteTip))
		},
	} {
		t.Run(name, func(t *testing.T) {
			request := fixture.request
			mutate(&request)
			if _, err := fixture.manager.PushPinnedLine(ctx, request); !errors.Is(err, ErrPinnedLineInvalid) {
				t.Fatalf("PushPinnedLine(%s) error = %v", name, err)
			}
		})
	}

	manager := fixture.manager
	manager.pinnedLinePushTransport = func(string) (string, error) { return "relative", nil }
	if _, err := manager.PushPinnedLine(ctx, fixture.request); !errors.Is(err, ErrPinnedLineInvalid) {
		t.Fatalf("invalid internal transport error = %v", err)
	}
	manager.pinnedLinePushTransport = nil
	if got, err := manager.resolvePinnedLinePushTransport(
		fixture.pin.Repository,
	); err != nil || got != fixture.pin.Repository {
		t.Fatalf("default transport = %q, %v", got, err)
	}
}

func TestPinnedLinePushRepositoryAndConfigurationAllowlist(t *testing.T) {
	remote := "git@github.com:octo/repo.git"
	if !validPinnedLinePushRepository(remote, remote, false) {
		t.Fatal("canonical SCP transport was rejected")
	}
	local := filepath.Join(t.TempDir(), "repository.git")
	if !validPinnedLinePushRepository(local, local, true) ||
		validPinnedLinePushRepository(local, local, false) ||
		validPinnedLinePushRepository(local+string(filepath.Separator)+"..", local, true) {
		t.Fatal("local transport allowlist did not enforce exact internal authority")
	}

	for _, key := range []string{
		"core.gitproxy",
		"ssh.variant",
		"remote.pushdefault",
		"push.followtags",
		"http.proxy",
		"branch.main.pushremote",
		"remote.origin.mirror",
		"remote.origin.proxy",
		"remote.origin.proxyauthmethod",
		"remote.origin.push",
		"remote.origin.pushurl",
		"remote.origin.receivepack",
		"remote.origin.serveroption",
		"remote.origin.uploadpack",
		"remote.origin.vcs",
	} {
		if !unsafePinnedLinePushConfigKey(key) {
			t.Fatalf("unsafe push configuration %q was accepted", key)
		}
	}
	for _, key := range []string{"user.name", "remote.origin.url", "branch.main.merge"} {
		if unsafePinnedLinePushConfigKey(key) {
			t.Fatalf("safe push configuration %q was rejected", key)
		}
	}

	root := t.TempDir()
	if !isWithin(filepath.Join(root, "nested", "candidate"), root) ||
		isWithin(root, root) || isWithin(filepath.Dir(root), root) {
		t.Fatal("workspace path containment accepted an escape or rejected a descendant")
	}
	for _, workspaceID := range []string{"repository", "repository-2", "repository-10"} {
		if !validLegacyPinnedWorkspaceID("repository", workspaceID) {
			t.Fatalf("valid legacy workspace ID %q was rejected", workspaceID)
		}
	}
	for _, workspaceID := range []string{"other", "repository-1", "repository-02", "repository-x"} {
		if validLegacyPinnedWorkspaceID("repository", workspaceID) {
			t.Fatalf("invalid legacy workspace ID %q was accepted", workspaceID)
		}
	}
}
