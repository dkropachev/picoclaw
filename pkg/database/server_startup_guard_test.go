package database

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestServerStartupGuardSuccessRunsOnceBeforePublication(t *testing.T) {
	home := t.TempDir()
	var calls atomic.Int32
	server, err := StartServer(t.Context(), ServerOptions{
		Home: home,
		StartupGuard: func() error {
			calls.Add(1)
			if _, manifestErr := ReadManifest(home); CodeOf(manifestErr) != CodeUnavailable {
				return fmt.Errorf("manifest visible before startup guard completed: %w", manifestErr)
			}
			candidates, candidateErr := manifestCandidatePaths(home)
			if candidateErr != nil {
				return candidateErr
			}
			if len(candidates) != 1 {
				return fmt.Errorf("prepared manifest candidates = %d, want 1", len(candidates))
			}
			return nil
		},
	})
	if err != nil {
		t.Fatalf("StartServer() error = %v", err)
	}
	t.Cleanup(func() { closeServer(t, server) })
	if calls.Load() != 1 {
		t.Fatalf("startup guard calls = %d, want 1", calls.Load())
	}
	published, err := ReadManifest(home)
	if err != nil {
		t.Fatalf("ReadManifest() error = %v", err)
	}
	if published != server.Manifest() {
		t.Fatalf("published manifest = %#v, want %#v", published, server.Manifest())
	}
	assertNoManifestCandidates(t, home)
	if err := callStartupGuard(nil); err != nil {
		t.Fatalf("callStartupGuard(nil) error = %v", err)
	}
}

func TestServerStartupGuardFailsBeforeManifestAndReleasesOwnership(t *testing.T) {
	home := t.TempDir()
	canary := errors.New("startup guard rejected generation")
	var calls atomic.Int32
	var preparedPath string
	server, err := StartServer(t.Context(), ServerOptions{
		Home: home,
		StartupGuard: func() error {
			calls.Add(1)
			candidates, candidateErr := manifestCandidatePaths(home)
			if candidateErr != nil {
				return candidateErr
			}
			if len(candidates) != 1 {
				return fmt.Errorf("prepared manifest candidates = %d, want 1", len(candidates))
			}
			preparedPath = candidates[0]
			return canary
		},
	})
	if server != nil || !errors.Is(err, canary) || calls.Load() != 1 {
		t.Fatalf("rejected guarded server = %#v, %v, calls=%d", server, err, calls.Load())
	}
	if _, manifestErr := ReadManifest(home); CodeOf(manifestErr) != CodeUnavailable {
		t.Fatalf("startup guard published manifest: %v", manifestErr)
	}
	assertNoManifestCandidates(t, home)
	if _, statErr := os.Lstat(preparedPath); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("prepared manifest candidate survived rejection: %v", statErr)
	}
	migrationFence, err := AcquireMigrationFence(home)
	if err != nil {
		t.Fatalf("startup guard retained online fence: %v", err)
	}
	if closeErr := migrationFence.Close(); closeErr != nil {
		t.Fatal(closeErr)
	}
	server, err = StartServer(t.Context(), ServerOptions{Home: home})
	if err != nil {
		t.Fatalf("nil startup guard retained ownership or endpoint: %v", err)
	}
	closeServer(t, server)
}

func TestServerStartupGuardContainsPanicBeforePublication(t *testing.T) {
	const panicSecret = "startup-guard-panic-secret-6a19"
	home := t.TempDir()
	var calls atomic.Int32
	server, err := StartServer(t.Context(), ServerOptions{
		Home: home,
		StartupGuard: func() error {
			calls.Add(1)
			panic(panicSecret)
		},
	})
	if server != nil || CodeOf(err) != CodeInternal || calls.Load() != 1 {
		t.Fatalf("panicking guarded server = %#v, %v, calls=%d", server, err, calls.Load())
	}
	if strings.Contains(err.Error(), panicSecret) {
		t.Fatalf("startup guard panic exposed secret: %v", err)
	}
	if _, manifestErr := ReadManifest(home); CodeOf(manifestErr) != CodeUnavailable {
		t.Fatalf("panicking startup guard published manifest: %v", manifestErr)
	}
	assertNoManifestCandidates(t, home)
	migrationFence, err := AcquireMigrationFence(home)
	if err != nil {
		t.Fatalf("panicking startup guard retained online fence: %v", err)
	}
	if closeErr := migrationFence.Close(); closeErr != nil {
		t.Fatal(closeErr)
	}
	replacement, err := StartServer(t.Context(), ServerOptions{Home: home})
	if err != nil {
		t.Fatalf("panicking startup guard retained ownership or endpoint: %v", err)
	}
	closeServer(t, replacement)
}

func TestServerStartupGuardBlocksWithOwnershipHeld(t *testing.T) {
	home := t.TempDir()
	canary := errors.New("blocked startup guard rejected generation")
	entered := make(chan struct{})
	release := make(chan struct{})
	var releaseOnce sync.Once
	t.Cleanup(func() {
		releaseOnce.Do(func() { close(release) })
	})
	var calls atomic.Int32
	type startResult struct {
		server *Server
		err    error
	}
	started := make(chan startResult, 1)
	go func() {
		server, err := StartServer(t.Context(), ServerOptions{
			Home: home,
			StartupGuard: func() error {
				calls.Add(1)
				close(entered)
				<-release
				return canary
			},
		})
		started <- startResult{server: server, err: err}
	}()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("startup guard did not begin")
	}

	if _, manifestErr := ReadManifest(home); CodeOf(manifestErr) != CodeUnavailable {
		t.Fatalf("blocking startup guard published manifest: %v", manifestErr)
	}
	stateDir, stateErr := StateDirectory(home)
	if stateErr != nil {
		t.Fatal(stateErr)
	}
	dialCtx, cancelDial := context.WithTimeout(t.Context(), time.Second)
	connection, dialErr := dialLocal(dialCtx, endpointForStateDirectory(stateDir))
	cancelDial()
	if connection != nil {
		_ = connection.Close()
	}
	if dialErr == nil {
		t.Fatal("startup guard exposed a listener before publication")
	}
	if candidates := requireManifestCandidateCount(t, home, 1); len(candidates) != 1 {
		t.Fatalf("prepared manifest candidates = %d, want 1", len(candidates))
	}
	if duplicate, duplicateErr := StartServer(t.Context(), ServerOptions{Home: home}); duplicate != nil ||
		CodeOf(duplicateErr) != CodeAlreadyExists {
		t.Fatalf("duplicate server during startup guard = %#v, %v, want AlreadyExists", duplicate, duplicateErr)
	}
	if fence, fenceErr := AcquireMigrationFence(home); fence != nil || CodeOf(fenceErr) != CodeConflict {
		t.Fatalf("migration fence during startup guard = %#v, %v, want Conflict", fence, fenceErr)
	}

	releaseOnce.Do(func() { close(release) })
	var result startResult
	select {
	case result = <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("guarded StartServer() did not finish")
	}
	if result.server != nil || !errors.Is(result.err, canary) || calls.Load() != 1 {
		t.Fatalf("blocked guarded server = %#v, %v, calls=%d", result.server, result.err, calls.Load())
	}
	assertNoManifestCandidates(t, home)
	migrationFence, err := AcquireMigrationFence(home)
	if err != nil {
		t.Fatalf("startup guard retained online fence: %v", err)
	}
	if closeErr := migrationFence.Close(); closeErr != nil {
		t.Fatal(closeErr)
	}
	replacement, err := StartServer(t.Context(), ServerOptions{Home: home})
	if err != nil {
		t.Fatalf("blocked startup guard retained ownership or endpoint: %v", err)
	}
	closeServer(t, replacement)
}

func TestManifestBeforePublishFailureCleansCandidate(t *testing.T) {
	home := t.TempDir()
	stateDir, err := prepareStateDirectory(home)
	if err != nil {
		t.Fatal(err)
	}
	manifest := Manifest{
		PID: os.Getpid(), Protocol: ProtocolVersion,
		Token: strings.Repeat("a", tokenBytes*2), Endpoint: endpointForStateDirectory(stateDir),
		Epoch: strings.Repeat("b", epochBytes*2),
	}
	canary := errors.New("before-publication canary")
	var guardRan bool
	err = writeManifestGuarded(
		stateDir,
		manifest,
		func() error {
			guardRan = true
			return nil
		},
		func() error {
			if !guardRan {
				return errors.New("before-publication callback ran before startup guard")
			}
			return canary
		},
	)
	if !errors.Is(err, canary) {
		t.Fatalf("before-publication failure = %v, want canary", err)
	}
	if _, manifestErr := ReadManifest(home); CodeOf(manifestErr) != CodeUnavailable {
		t.Fatalf("before-publication failure published discovery: %v", manifestErr)
	}
	assertNoManifestCandidates(t, home)
}

func TestServerStartupGuardFailurePreservesExistingDiscovery(t *testing.T) {
	home := t.TempDir()
	stateDir, err := prepareStateDirectory(home)
	if err != nil {
		t.Fatal(err)
	}
	stale := Manifest{
		PID: os.Getpid(), Protocol: ProtocolVersion,
		Token: strings.Repeat("a", tokenBytes*2), Endpoint: endpointForStateDirectory(stateDir),
		Epoch: strings.Repeat("b", epochBytes*2),
	}
	if writeErr := writeManifest(stateDir, stale); writeErr != nil {
		t.Fatal(writeErr)
	}
	canary := errors.New("preserve existing discovery")
	server, err := StartServer(t.Context(), ServerOptions{
		Home: home,
		StartupGuard: func() error {
			return canary
		},
	})
	if server != nil || !errors.Is(err, canary) {
		t.Fatalf("guarded server with existing discovery = %#v, %v", server, err)
	}
	preserved, err := ReadManifest(home)
	if err != nil || preserved != stale {
		t.Fatalf("existing discovery after guard failure = %#v, %v; want %#v", preserved, err, stale)
	}
	assertNoManifestCandidates(t, home)
	replacement, err := StartServer(t.Context(), ServerOptions{Home: home})
	if err != nil {
		t.Fatalf("existing discovery prevented replacement startup: %v", err)
	}
	closeServer(t, replacement)
}

func TestServerStartupGuardRenameFailureCleansUpAndCanRetry(t *testing.T) {
	home := t.TempDir()
	stateDir, err := prepareStateDirectory(home)
	if err != nil {
		t.Fatal(err)
	}
	manifestPath := filepath.Join(stateDir, manifestFileName)
	t.Cleanup(func() { _ = os.Remove(manifestPath) })
	var calls atomic.Int32
	guard := func() error {
		if calls.Add(1) == 1 {
			return os.Mkdir(manifestPath, 0o700)
		}
		return nil
	}

	server, err := StartServer(t.Context(), ServerOptions{Home: home, StartupGuard: guard})
	if server != nil || err == nil || !strings.Contains(err.Error(), "publish database broker manifest") {
		t.Fatalf("server with obstructed manifest publication = %#v, %v", server, err)
	}
	if calls.Load() != 1 {
		t.Fatalf("startup guard calls after publication failure = %d, want 1", calls.Load())
	}
	assertNoManifestCandidates(t, home)
	if info, statErr := os.Lstat(manifestPath); statErr != nil || !info.IsDir() {
		t.Fatalf("manifest obstruction after publication failure = %#v, %v", info, statErr)
	}
	migrationFence, err := AcquireMigrationFence(home)
	if err != nil {
		t.Fatalf("publication failure retained online fence: %v", err)
	}
	if closeErr := migrationFence.Close(); closeErr != nil {
		t.Fatal(closeErr)
	}
	if removeErr := os.Remove(manifestPath); removeErr != nil {
		t.Fatalf("remove manifest obstruction: %v", removeErr)
	}

	replacement, err := StartServer(t.Context(), ServerOptions{Home: home, StartupGuard: guard})
	if err != nil {
		t.Fatalf("StartServer() retry error = %v", err)
	}
	if calls.Load() != 2 {
		t.Fatalf("startup guard calls after retry = %d, want 2", calls.Load())
	}
	published, err := ReadManifest(home)
	if err != nil || published != replacement.Manifest() {
		t.Fatalf("ReadManifest() after retry = %#v, %v", published, err)
	}
	assertNoManifestCandidates(t, home)
	closeServer(t, replacement)
}

func manifestCandidatePaths(home string) ([]string, error) {
	stateDir, err := StateDirectory(home)
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(stateDir)
	if err != nil {
		return nil, fmt.Errorf("read database state directory: %w", err)
	}
	paths := make([]string, 0, len(entries))
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".broker-manifest-") {
			paths = append(paths, filepath.Join(stateDir, entry.Name()))
		}
	}
	return paths, nil
}

func requireManifestCandidateCount(t *testing.T, home string, want int) []string {
	t.Helper()
	paths, err := manifestCandidatePaths(home)
	if err != nil {
		t.Fatal(err)
	}
	if len(paths) != want {
		t.Fatalf("manifest candidate count = %d, want %d: %v", len(paths), want, paths)
	}
	return paths
}

func assertNoManifestCandidates(t *testing.T, home string) {
	t.Helper()
	requireManifestCandidateCount(t, home, 0)
}
