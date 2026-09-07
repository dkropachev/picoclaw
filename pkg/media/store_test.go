package media

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func createTempFile(t *testing.T, dir, name string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte("test content"), 0o644); err != nil {
		t.Fatalf("failed to create temp file: %v", err)
	}
	return path
}

func TestStoreAndResolve(t *testing.T) {
	dir := t.TempDir()
	store := NewFileMediaStore()

	path := createTempFile(t, dir, "photo.jpg")

	ref, err := store.Store(path, MediaMeta{Filename: "photo.jpg", Source: "telegram"}, "scope1")
	if err != nil {
		t.Fatalf("Store failed: %v", err)
	}

	if !strings.HasPrefix(ref, "media://") {
		t.Errorf("ref should start with media://, got %q", ref)
	}

	resolved, err := store.Resolve(ref)
	if err != nil {
		t.Fatalf("Resolve failed: %v", err)
	}
	if resolved != path {
		t.Errorf("Resolve returned %q, want %q", resolved, path)
	}
}

func TestReleaseAll(t *testing.T) {
	dir := t.TempDir()
	store := NewFileMediaStore()

	paths := make([]string, 3)
	refs := make([]string, 3)
	for i := range 3 {
		paths[i] = createTempFile(t, dir, strings.Repeat("a", i+1)+".jpg")
		var err error
		refs[i], err = store.Store(paths[i], MediaMeta{Source: "test"}, "scope1")
		if err != nil {
			t.Fatalf("Store failed: %v", err)
		}
	}

	if err := store.ReleaseAll("scope1"); err != nil {
		t.Fatalf("ReleaseAll failed: %v", err)
	}

	// Files should be deleted
	for _, p := range paths {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Errorf("file %q should have been deleted", p)
		}
	}

	// Refs should be unresolvable
	for _, ref := range refs {
		if _, err := store.Resolve(ref); err == nil {
			t.Errorf("Resolve(%q) should fail after ReleaseAll", ref)
		}
	}
}

func TestReleaseAllForgetOnlyKeepsFile(t *testing.T) {
	dir := t.TempDir()
	store := NewFileMediaStore()

	path := createTempFile(t, dir, "workspace.txt")
	ref, err := store.Store(path, MediaMeta{
		Source:        "test",
		CleanupPolicy: CleanupPolicyForgetOnly,
	}, "scope1")
	if err != nil {
		t.Fatalf("Store failed: %v", err)
	}

	if err := store.ReleaseAll("scope1"); err != nil {
		t.Fatalf("ReleaseAll failed: %v", err)
	}

	if _, err := store.Resolve(ref); err == nil {
		t.Error("forget-only ref should be unresolvable after release")
	}
	if _, err := os.Stat(path); err != nil {
		t.Errorf("forget-only file should remain on disk: %v", err)
	}
}

func TestReleaseAllSharedPathDeletesOnFinalRefOnly(t *testing.T) {
	dir := t.TempDir()
	store := NewFileMediaStore()

	path := createTempFile(t, dir, "shared.jpg")
	refA, err := store.Store(path, MediaMeta{
		Source:        "test",
		CleanupPolicy: CleanupPolicyDeleteOnCleanup,
	}, "scopeA")
	if err != nil {
		t.Fatalf("Store(scopeA) failed: %v", err)
	}
	refB, err := store.Store(path, MediaMeta{
		Source:        "test",
		CleanupPolicy: CleanupPolicyDeleteOnCleanup,
	}, "scopeB")
	if err != nil {
		t.Fatalf("Store(scopeB) failed: %v", err)
	}

	if err := store.ReleaseAll("scopeA"); err != nil {
		t.Fatalf("ReleaseAll(scopeA) failed: %v", err)
	}

	if _, err := store.Resolve(refA); err == nil {
		t.Error("refA should be unresolvable after ReleaseAll(scopeA)")
	}
	if _, err := store.Resolve(refB); err != nil {
		t.Fatalf("refB should still resolve: %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Errorf("shared file should remain until final ref is released: %v", err)
	}

	if err := store.ReleaseAll("scopeB"); err != nil {
		t.Fatalf("ReleaseAll(scopeB) failed: %v", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Error("shared file should be deleted after final ref is released")
	}
}

func TestReleaseAllMixedPoliciesKeepsFile(t *testing.T) {
	dir := t.TempDir()
	store := NewFileMediaStore()

	path := createTempFile(t, dir, "shared.txt")
	if _, err := store.Store(path, MediaMeta{
		Source:        "test",
		CleanupPolicy: CleanupPolicyDeleteOnCleanup,
	}, "owned"); err != nil {
		t.Fatalf("Store(owned) failed: %v", err)
	}
	if _, err := store.Store(path, MediaMeta{
		Source:        "test",
		CleanupPolicy: CleanupPolicyForgetOnly,
	}, "borrowed"); err != nil {
		t.Fatalf("Store(borrowed) failed: %v", err)
	}

	if err := store.ReleaseAll("owned"); err != nil {
		t.Fatalf("ReleaseAll(owned) failed: %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("mixed-policy file should remain after owned ref release: %v", err)
	}

	if err := store.ReleaseAll("borrowed"); err != nil {
		t.Fatalf("ReleaseAll(borrowed) failed: %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Errorf("mixed-policy path should not be auto-deleted: %v", err)
	}
}

func TestPendingDeletionSkippedAfterPathReregistered(t *testing.T) {
	dir := t.TempDir()
	store := NewFileMediaStore()
	path := createTempFile(t, dir, "reregistered.jpg")

	oldRef, err := store.Store(path, MediaMeta{
		Source:        "test",
		CleanupPolicy: CleanupPolicyDeleteOnCleanup,
	}, "old")
	if err != nil {
		t.Fatalf("Store(old) failed: %v", err)
	}
	deletion := scheduleDeletionForTest(t, store, "old", oldRef, path)

	newRef, err := store.Store(path, MediaMeta{
		Source:        "test",
		CleanupPolicy: CleanupPolicyDeleteOnCleanup,
	}, "new")
	if err != nil {
		t.Fatalf("Store(new) failed: %v", err)
	}

	if err := store.removePendingPath(deletion); err != nil {
		t.Fatalf("removePendingPath(old) failed: %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("older cleanup deleted a re-registered path: %v", err)
	}
	if _, err := store.Resolve(newRef); err != nil {
		t.Fatalf("new ref should remain resolvable: %v", err)
	}

	if err := store.ReleaseAll("new"); err != nil {
		t.Fatalf("ReleaseAll(new) failed: %v", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Error("delete-on-cleanup path should be removed with its new final ref")
	}
}

func TestPendingDeletionCancelledByReleasedForgetOnlyDotAlias(t *testing.T) {
	dir := t.TempDir()
	store := NewFileMediaStore()
	path := createTempFile(t, dir, "borrowed.txt")
	dotAlias := filepath.Dir(path) + string(os.PathSeparator) + "." + string(os.PathSeparator) + filepath.Base(path)

	oldRef, err := store.Store(path, MediaMeta{
		Source:        "test",
		CleanupPolicy: CleanupPolicyDeleteOnCleanup,
	}, "old")
	if err != nil {
		t.Fatalf("Store(old) failed: %v", err)
	}
	deletion := scheduleDeletionForTest(t, store, "old", oldRef, path)

	newRef, err := store.Store(dotAlias, MediaMeta{
		Source:        "test",
		CleanupPolicy: CleanupPolicyForgetOnly,
	}, "borrowed")
	if err != nil {
		t.Fatalf("Store(borrowed) failed: %v", err)
	}
	resolved, err := store.Resolve(newRef)
	if err != nil {
		t.Fatalf("Resolve(borrowed) failed: %v", err)
	}
	if resolved != filepath.Clean(path) || !filepath.IsAbs(resolved) {
		t.Fatalf("Resolve(borrowed) = %q, want canonical absolute path %q", resolved, filepath.Clean(path))
	}
	if err := store.ReleaseAll("borrowed"); err != nil {
		t.Fatalf("ReleaseAll(borrowed) failed: %v", err)
	}

	// The re-registration must cancel the old deletion permanently. It is not
	// enough to check for a live ref here because the forget-only ref has
	// already been released.
	if err := store.removePendingPath(deletion); err != nil {
		t.Fatalf("removePendingPath(old) failed: %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("old cleanup deleted a forget-only path: %v", err)
	}
}

func TestDistinctSameFileKeysDisableAutomaticDeletion(t *testing.T) {
	dir := t.TempDir()
	store := NewFileMediaStore()
	path := createTempFile(t, dir, "identity-source.txt")
	alias := filepath.Join(dir, "identity-alias.txt")
	if err := os.Link(path, alias); err != nil {
		t.Skipf("hard links unavailable: %v", err)
	}
	if lifecyclePathKey(path) == lifecyclePathKey(alias) {
		t.Fatal("test requires distinct lexical lifecycle keys")
	}

	if _, err := store.Store(path, MediaMeta{Source: "test"}, "first"); err != nil {
		t.Fatalf("Store(first) failed: %v", err)
	}
	if _, err := store.Store(alias, MediaMeta{Source: "test"}, "second"); err != nil {
		t.Fatalf("Store(second) failed: %v", err)
	}

	store.mu.RLock()
	firstState := store.pathStates[lifecyclePathKey(path)]
	secondState := store.pathStates[lifecyclePathKey(alias)]
	store.mu.RUnlock()
	if firstState.deleteEligible || secondState.deleteEligible {
		t.Fatalf(
			"same-file distinct-key states remained delete eligible: %#v / %#v",
			firstState,
			secondState,
		)
	}

	if err := store.ReleaseAll("first"); err != nil {
		t.Fatalf("ReleaseAll(first) failed: %v", err)
	}
	if err := store.ReleaseAll("second"); err != nil {
		t.Fatalf("ReleaseAll(second) failed: %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("first same-file path was deleted: %v", err)
	}
	if _, err := os.Stat(alias); err != nil {
		t.Fatalf("second same-file path was deleted: %v", err)
	}
}

func TestPendingDeletionCancelledByDistinctSameFileKey(t *testing.T) {
	dir := t.TempDir()
	store := NewFileMediaStore()
	path := createTempFile(t, dir, "pending-identity-source.txt")
	alias := filepath.Join(dir, "pending-identity-alias.txt")
	if err := os.Link(path, alias); err != nil {
		t.Skipf("hard links unavailable: %v", err)
	}

	oldRef, err := store.Store(path, MediaMeta{Source: "test"}, "old")
	if err != nil {
		t.Fatalf("Store(old) failed: %v", err)
	}
	deletion := scheduleDeletionForTest(t, store, "old", oldRef, path)

	if _, err := store.Store(alias, MediaMeta{
		Source:        "test",
		CleanupPolicy: CleanupPolicyForgetOnly,
	}, "borrowed"); err != nil {
		t.Fatalf("Store(alias) failed: %v", err)
	}
	if err := store.ReleaseAll("borrowed"); err != nil {
		t.Fatalf("ReleaseAll(alias) failed: %v", err)
	}
	if err := store.removePendingPath(deletion); err != nil {
		t.Fatalf("removePendingPath(old) failed: %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("identity-matched old deletion removed its path: %v", err)
	}
	if _, err := os.Stat(alias); err != nil {
		t.Fatalf("identity-matched alias disappeared: %v", err)
	}
}

func TestStoredRelativePathDoesNotDriftAfterWorkingDirectoryChange(t *testing.T) {
	firstDir := t.TempDir()
	secondDir := t.TempDir()
	const filename = "relative-media.txt"
	originalPath := filepath.Join(firstDir, filename)
	decoyPath := filepath.Join(secondDir, filename)
	if err := os.WriteFile(originalPath, []byte("original"), 0o600); err != nil {
		t.Fatalf("WriteFile(original) failed: %v", err)
	}
	if err := os.WriteFile(decoyPath, []byte("decoy"), 0o600); err != nil {
		t.Fatalf("WriteFile(decoy) failed: %v", err)
	}

	t.Chdir(firstDir)
	store := NewFileMediaStore()
	ref, err := store.Store(filename, MediaMeta{Source: "test"}, "scope")
	if err != nil {
		t.Fatalf("Store(relative) failed: %v", err)
	}
	t.Chdir(secondDir)

	resolved, err := store.Resolve(ref)
	if err != nil {
		t.Fatalf("Resolve(relative) failed: %v", err)
	}
	if resolved != originalPath || !filepath.IsAbs(resolved) {
		t.Fatalf("Resolve(relative) = %q, want %q", resolved, originalPath)
	}
	snapshot, err := store.ReadSnapshot(context.Background(), ref, 64)
	if err != nil {
		t.Fatalf("ReadSnapshot(relative) failed: %v", err)
	}
	if string(snapshot.Bytes) != "original" {
		t.Fatalf("ReadSnapshot(relative) bytes = %q, want original", snapshot.Bytes)
	}
	if err := store.ReleaseAll("scope"); err != nil {
		t.Fatalf("ReleaseAll(relative) failed: %v", err)
	}
	if _, err := os.Stat(originalPath); !os.IsNotExist(err) {
		t.Fatalf("original managed path still exists: %v", err)
	}
	if decoy, err := os.ReadFile(decoyPath); err != nil || string(decoy) != "decoy" {
		t.Fatalf("working-directory decoy changed: %q, %v", decoy, err)
	}
}

func TestPendingDeletionPreservesExternalReplacement(t *testing.T) {
	dir := t.TempDir()
	store := NewFileMediaStore()
	path := createTempFile(t, dir, "managed.jpg")

	ref, err := store.Store(path, MediaMeta{Source: "test"}, "old")
	if err != nil {
		t.Fatalf("Store(old) failed: %v", err)
	}
	deletion := scheduleDeletionForTest(t, store, "old", ref, path)

	replacementPath := filepath.Join(dir, "replacement.jpg")
	want := []byte("external replacement")
	err = os.WriteFile(replacementPath, want, 0o644)
	if err != nil {
		t.Fatalf("WriteFile(replacement) failed: %v", err)
	}
	err = os.Remove(path)
	if err != nil {
		t.Fatalf("Remove(original) failed: %v", err)
	}
	err = os.Rename(replacementPath, path)
	if err != nil {
		t.Fatalf("Rename(replacement) failed: %v", err)
	}

	err = store.removePendingPath(deletion)
	if err != nil {
		t.Fatalf("removePendingPath failed: %v", err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("replacement should remain: %v", err)
	}
	if string(got) != string(want) {
		t.Fatalf("replacement content = %q, want %q", got, want)
	}
}

func TestStoreRejectsLiveLifecycleKeyAfterExternalReplacement(t *testing.T) {
	dir := t.TempDir()
	store := NewFileMediaStore()
	path := createTempFile(t, dir, "live.jpg")
	dotAlias := filepath.Dir(path) + string(os.PathSeparator) + "." + string(os.PathSeparator) + filepath.Base(path)

	ref, err := store.Store(path, MediaMeta{Source: "test"}, "old")
	if err != nil {
		t.Fatalf("Store(old) failed: %v", err)
	}

	replacementPath := filepath.Join(dir, "replacement-live.jpg")
	want := []byte("new external file")
	err = os.WriteFile(replacementPath, want, 0o644)
	if err != nil {
		t.Fatalf("WriteFile(replacement) failed: %v", err)
	}
	err = os.Remove(path)
	if err != nil {
		t.Fatalf("Remove(original) failed: %v", err)
	}
	err = os.Rename(replacementPath, path)
	if err != nil {
		t.Fatalf("Rename(replacement) failed: %v", err)
	}

	_, err = store.Store(dotAlias, MediaMeta{Source: "test"}, "new")
	if err == nil {
		t.Fatal("Store should reject a live lifecycle key whose identity changed")
	} else if !strings.Contains(err.Error(), "identity changed") {
		t.Fatalf("Store error = %q, want identity-change error", err)
	}
	_, err = store.Resolve(ref)
	if err != nil {
		t.Fatalf("failed coalescing attempt mutated old ref: %v", err)
	}

	err = store.ReleaseAll("old")
	if err != nil {
		t.Fatalf("ReleaseAll(old) failed: %v", err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("replacement should remain after old lifecycle release: %v", err)
	}
	if string(got) != string(want) {
		t.Fatalf("replacement content = %q, want %q", got, want)
	}
}

func TestStoreDoesNotRegisterPathDeletedWhileWaitingForLifecycleLock(t *testing.T) {
	dir := t.TempDir()
	store := NewFileMediaStore()
	path := createTempFile(t, dir, "waiting.jpg")

	type storeResult struct {
		ref string
		err error
	}
	started := make(chan struct{})
	result := make(chan storeResult, 1)

	store.mu.Lock()
	go func() {
		close(started)
		ref, err := store.Store(path, MediaMeta{Source: "test"}, "scope")
		result <- storeResult{ref: ref, err: err}
	}()
	<-started

	// Give Store an opportunity to reach the lifecycle lock. A stale Stat
	// performed before that lock would let it register the now-deleted path.
	time.Sleep(20 * time.Millisecond)
	if err := os.Remove(path); err != nil {
		store.mu.Unlock()
		t.Fatalf("Remove failed: %v", err)
	}
	store.mu.Unlock()

	got := <-result
	if got.err == nil {
		t.Fatalf("Store registered deleted path as %q", got.ref)
	}
	store.mu.RLock()
	defer store.mu.RUnlock()
	if len(store.refs) != 0 || len(store.pathStates) != 0 {
		t.Fatal("failed Store left lifecycle mappings behind")
	}
}

func scheduleDeletionForTest(
	t *testing.T,
	store *FileMediaStore,
	scope string,
	ref string,
	path string,
) pendingPathDeletion {
	t.Helper()

	store.mu.Lock()
	defer store.mu.Unlock()

	delete(store.scopeToRefs[scope], ref)
	if len(store.scopeToRefs[scope]) == 0 {
		delete(store.scopeToRefs, scope)
	}
	deletion, ok := store.releaseRefLocked(ref, path)
	if !ok {
		t.Fatal("expected final ref to schedule path deletion")
	}
	return deletion
}

func TestMultiScopeIsolation(t *testing.T) {
	dir := t.TempDir()
	store := NewFileMediaStore()

	pathA := createTempFile(t, dir, "fileA.jpg")
	pathB := createTempFile(t, dir, "fileB.jpg")

	refA, _ := store.Store(pathA, MediaMeta{Source: "test"}, "scopeA")
	refB, _ := store.Store(pathB, MediaMeta{Source: "test"}, "scopeB")

	// Release only scopeA
	if err := store.ReleaseAll("scopeA"); err != nil {
		t.Fatalf("ReleaseAll(scopeA) failed: %v", err)
	}

	// scopeA file should be gone
	if _, err := os.Stat(pathA); !os.IsNotExist(err) {
		t.Error("file A should have been deleted")
	}
	if _, err := store.Resolve(refA); err == nil {
		t.Error("refA should be unresolvable after release")
	}

	// scopeB file should still exist
	if _, err := os.Stat(pathB); err != nil {
		t.Error("file B should still exist")
	}
	resolved, err := store.Resolve(refB)
	if err != nil {
		t.Fatalf("refB should still resolve: %v", err)
	}
	if resolved != pathB {
		t.Errorf("resolved %q, want %q", resolved, pathB)
	}
}

func TestReleaseAllIdempotent(t *testing.T) {
	store := NewFileMediaStore()

	// ReleaseAll on non-existent scope should not error
	if err := store.ReleaseAll("nonexistent"); err != nil {
		t.Fatalf("ReleaseAll on empty scope should not error: %v", err)
	}

	// Create and release, then release again
	dir := t.TempDir()
	path := createTempFile(t, dir, "file.jpg")
	_, _ = store.Store(path, MediaMeta{Source: "test"}, "scope1")

	if err := store.ReleaseAll("scope1"); err != nil {
		t.Fatalf("first ReleaseAll failed: %v", err)
	}
	if err := store.ReleaseAll("scope1"); err != nil {
		t.Fatalf("second ReleaseAll should not error: %v", err)
	}
}

func TestReleaseAllCleansMappingsIfRefsMissing(t *testing.T) {
	dir := t.TempDir()
	store := NewFileMediaStore()

	path := createTempFile(t, dir, "file.jpg")
	ref, err := store.Store(path, MediaMeta{Source: "test"}, "scope1")
	if err != nil {
		t.Fatalf("Store failed: %v", err)
	}

	// Simulate internal inconsistency: scopeToRefs/refToScope contains ref but refs map doesn't.
	store.mu.Lock()
	delete(store.refs, ref)
	store.mu.Unlock()

	if err := store.ReleaseAll("scope1"); err != nil {
		t.Fatalf("ReleaseAll failed: %v", err)
	}

	// ReleaseAll should still clean mappings (even if it can't delete the file without the path).
	store.mu.RLock()
	defer store.mu.RUnlock()
	if _, ok := store.refToScope[ref]; ok {
		t.Error("refToScope should not contain ref after ReleaseAll")
	}
	if _, ok := store.scopeToRefs["scope1"]; ok {
		t.Error("scopeToRefs should not contain scope1 after ReleaseAll")
	}
}

func TestStoreNonexistentFile(t *testing.T) {
	store := NewFileMediaStore()

	_, err := store.Store("/nonexistent/path/file.jpg", MediaMeta{Source: "test"}, "scope1")
	if err == nil {
		t.Error("Store should fail for nonexistent file")
	}
	// Error message should include the underlying os error, not just "file does not exist"
	if !strings.Contains(err.Error(), "no such file or directory") &&
		!strings.Contains(err.Error(), "cannot find") {
		t.Errorf("Error should contain OS error detail, got: %v", err)
	}
}

func TestResolveWithMeta(t *testing.T) {
	dir := t.TempDir()
	store := NewFileMediaStore()

	path := createTempFile(t, dir, "image.png")
	meta := MediaMeta{
		Filename:    "image.png",
		ContentType: "image/png",
		Source:      "telegram",
	}

	ref, err := store.Store(path, meta, "scope1")
	if err != nil {
		t.Fatalf("Store failed: %v", err)
	}

	resolvedPath, resolvedMeta, err := store.ResolveWithMeta(ref)
	if err != nil {
		t.Fatalf("ResolveWithMeta failed: %v", err)
	}
	if resolvedPath != path {
		t.Errorf("ResolveWithMeta path = %q, want %q", resolvedPath, path)
	}
	if resolvedMeta.Filename != meta.Filename {
		t.Errorf("ResolveWithMeta Filename = %q, want %q", resolvedMeta.Filename, meta.Filename)
	}
	if resolvedMeta.ContentType != meta.ContentType {
		t.Errorf("ResolveWithMeta ContentType = %q, want %q", resolvedMeta.ContentType, meta.ContentType)
	}
	if resolvedMeta.Source != meta.Source {
		t.Errorf("ResolveWithMeta Source = %q, want %q", resolvedMeta.Source, meta.Source)
	}

	// Unknown ref should fail
	_, _, err = store.ResolveWithMeta("media://nonexistent")
	if err == nil {
		t.Error("ResolveWithMeta should fail for unknown ref")
	}
}

func TestConcurrentSafety(t *testing.T) {
	dir := t.TempDir()
	store := NewFileMediaStore()

	const goroutines = 20
	const filesPerGoroutine = 5

	var wg sync.WaitGroup
	wg.Add(goroutines)

	for g := range goroutines {
		go func(gIdx int) {
			defer wg.Done()
			scope := strings.Repeat("s", gIdx+1)

			for i := range filesPerGoroutine {
				path := createTempFile(t, dir, strings.Repeat("f", gIdx*filesPerGoroutine+i+1)+".tmp")
				ref, err := store.Store(path, MediaMeta{Source: "test"}, scope)
				if err != nil {
					t.Errorf("Store failed: %v", err)
					return
				}

				if _, err := store.Resolve(ref); err != nil {
					t.Errorf("Resolve failed: %v", err)
				}
			}

			if err := store.ReleaseAll(scope); err != nil {
				t.Errorf("ReleaseAll failed: %v", err)
			}
		}(g)
	}

	wg.Wait()
}

// --- TTL cleanup tests ---

func newTestStoreWithCleanup(maxAge time.Duration) *FileMediaStore {
	s := NewFileMediaStoreWithCleanup(MediaCleanerConfig{
		Enabled:  true,
		MaxAge:   maxAge,
		Interval: time.Hour, // won't tick in tests
	})
	return s
}

func TestCleanExpiredRemovesOldEntries(t *testing.T) {
	dir := t.TempDir()
	now := time.Now()
	store := newTestStoreWithCleanup(10 * time.Minute)
	store.nowFunc = func() time.Time { return now.Add(-20 * time.Minute) }

	path := createTempFile(t, dir, "old.jpg")
	ref, err := store.Store(path, MediaMeta{Source: "test"}, "scope1")
	if err != nil {
		t.Fatalf("Store failed: %v", err)
	}

	// Advance clock to present
	store.nowFunc = func() time.Time { return now }
	removed := store.CleanExpired()

	if removed != 1 {
		t.Errorf("expected 1 removed, got %d", removed)
	}
	if _, err := store.Resolve(ref); err == nil {
		t.Error("expired ref should be unresolvable")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Error("expired file should be deleted")
	}
}

func TestCleanExpiredForgetOnlyKeepsFile(t *testing.T) {
	dir := t.TempDir()
	now := time.Now()
	store := newTestStoreWithCleanup(10 * time.Minute)
	store.nowFunc = func() time.Time { return now.Add(-20 * time.Minute) }

	path := createTempFile(t, dir, "workspace.txt")
	ref, err := store.Store(path, MediaMeta{
		Source:        "test",
		CleanupPolicy: CleanupPolicyForgetOnly,
	}, "scope1")
	if err != nil {
		t.Fatalf("Store failed: %v", err)
	}

	store.nowFunc = func() time.Time { return now }
	removed := store.CleanExpired()

	if removed != 1 {
		t.Errorf("expected 1 removed, got %d", removed)
	}
	if _, err := store.Resolve(ref); err == nil {
		t.Error("expired forget-only ref should be unresolvable")
	}
	if _, err := os.Stat(path); err != nil {
		t.Errorf("forget-only file should remain on disk: %v", err)
	}
}

func TestCleanExpiredKeepsNonExpired(t *testing.T) {
	dir := t.TempDir()
	now := time.Now()
	store := newTestStoreWithCleanup(10 * time.Minute)
	store.nowFunc = func() time.Time { return now }

	path := createTempFile(t, dir, "fresh.jpg")
	ref, err := store.Store(path, MediaMeta{Source: "test"}, "scope1")
	if err != nil {
		t.Fatalf("Store failed: %v", err)
	}

	removed := store.CleanExpired()
	if removed != 0 {
		t.Errorf("expected 0 removed, got %d", removed)
	}

	if _, err := store.Resolve(ref); err != nil {
		t.Errorf("fresh ref should still resolve: %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Error("fresh file should still exist")
	}
}

func TestCleanExpiredMixedAges(t *testing.T) {
	dir := t.TempDir()
	now := time.Now()
	store := newTestStoreWithCleanup(10 * time.Minute)

	// Store old entry
	store.nowFunc = func() time.Time { return now.Add(-20 * time.Minute) }
	oldPath := createTempFile(t, dir, "old.jpg")
	oldRef, _ := store.Store(oldPath, MediaMeta{Source: "test"}, "scope1")

	// Store fresh entry
	store.nowFunc = func() time.Time { return now }
	freshPath := createTempFile(t, dir, "fresh.jpg")
	freshRef, _ := store.Store(freshPath, MediaMeta{Source: "test"}, "scope1")

	removed := store.CleanExpired()
	if removed != 1 {
		t.Errorf("expected 1 removed, got %d", removed)
	}

	if _, err := store.Resolve(oldRef); err == nil {
		t.Error("old ref should be gone")
	}
	if _, err := store.Resolve(freshRef); err != nil {
		t.Errorf("fresh ref should still resolve: %v", err)
	}
}

func TestCleanExpiredSharedPathDeletesOnFinalRefOnly(t *testing.T) {
	dir := t.TempDir()
	now := time.Now()
	store := newTestStoreWithCleanup(10 * time.Minute)

	path := createTempFile(t, dir, "shared.jpg")

	store.nowFunc = func() time.Time { return now.Add(-20 * time.Minute) }
	oldRef, err := store.Store(path, MediaMeta{
		Source:        "test",
		CleanupPolicy: CleanupPolicyDeleteOnCleanup,
	}, "scope-old")
	if err != nil {
		t.Fatalf("Store(old) failed: %v", err)
	}

	store.nowFunc = func() time.Time { return now }
	freshRef, err := store.Store(path, MediaMeta{
		Source:        "test",
		CleanupPolicy: CleanupPolicyDeleteOnCleanup,
	}, "scope-fresh")
	if err != nil {
		t.Fatalf("Store(fresh) failed: %v", err)
	}

	removed := store.CleanExpired()
	if removed != 1 {
		t.Errorf("expected 1 removed, got %d", removed)
	}
	if _, err := store.Resolve(oldRef); err == nil {
		t.Error("old ref should be gone after cleanup")
	}
	if _, err := store.Resolve(freshRef); err != nil {
		t.Fatalf("fresh ref should still resolve: %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Errorf("shared file should remain while fresh ref exists: %v", err)
	}

	if err := store.ReleaseAll("scope-fresh"); err != nil {
		t.Fatalf("ReleaseAll(scope-fresh) failed: %v", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Error("shared file should be deleted after final ref is released")
	}
}

func TestCleanExpiredCleansEmptyScopes(t *testing.T) {
	dir := t.TempDir()
	now := time.Now()
	store := newTestStoreWithCleanup(10 * time.Minute)

	// Store old entry as the only one in scope
	store.nowFunc = func() time.Time { return now.Add(-20 * time.Minute) }
	path := createTempFile(t, dir, "only.jpg")
	store.Store(path, MediaMeta{Source: "test"}, "lonely_scope")

	store.nowFunc = func() time.Time { return now }
	store.CleanExpired()

	store.mu.RLock()
	defer store.mu.RUnlock()
	if _, ok := store.scopeToRefs["lonely_scope"]; ok {
		t.Error("empty scope should be cleaned up")
	}
}

func TestStartStopLifecycle(t *testing.T) {
	store := NewFileMediaStoreWithCleanup(MediaCleanerConfig{
		Enabled:  true,
		MaxAge:   time.Minute,
		Interval: 50 * time.Millisecond,
	})

	// Start and stop should not panic
	store.Start()
	// Double start should not spawn a second goroutine
	store.Start()
	time.Sleep(100 * time.Millisecond)
	store.Stop()

	// Double stop should not panic
	store.Stop()
}

func TestStopWaitsForCleanerRetirement(t *testing.T) {
	store := NewFileMediaStoreWithCleanup(MediaCleanerConfig{
		Enabled:  true,
		MaxAge:   time.Minute,
		Interval: time.Hour,
	})
	cleanerExited := make(chan struct{})
	store.cleanerMu.Lock()
	store.cleanerDone = cleanerExited
	store.cleanerMu.Unlock()

	stopReturned := make(chan struct{})
	go func() {
		store.Stop()
		close(stopReturned)
	}()
	select {
	case <-store.stop:
	case <-time.After(time.Second):
		t.Fatal("Stop() did not signal cleaner cancellation")
	}
	select {
	case <-stopReturned:
		t.Fatal("Stop() returned before the cleaner retired")
	default:
	}

	close(cleanerExited)
	select {
	case <-stopReturned:
	case <-time.After(time.Second):
		t.Fatal("Stop() did not return after cleaner retirement")
	}
	store.Stop()
}

func TestBackgroundCleanerExpiresEntriesBeforeStop(t *testing.T) {
	dir := t.TempDir()
	now := time.Now()
	store := NewFileMediaStoreWithCleanup(MediaCleanerConfig{
		Enabled:  true,
		MaxAge:   time.Minute,
		Interval: time.Millisecond,
	})
	store.nowFunc = func() time.Time { return now.Add(-2 * time.Minute) }
	path := createTempFile(t, dir, "background-expired.jpg")
	ref, err := store.Store(path, MediaMeta{Source: "test"}, "background")
	if err != nil {
		t.Fatalf("Store() error = %v", err)
	}
	store.nowFunc = func() time.Time { return now }
	store.Start()

	deadline := time.Now().Add(2 * time.Second)
	for {
		if _, err = store.Resolve(ref); err != nil {
			break
		}
		if time.Now().After(deadline) {
			store.Stop()
			t.Fatal("background cleaner did not expire the media entry")
		}
		time.Sleep(time.Millisecond)
	}
	store.Stop()
	if _, err = os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("expired media path stat error = %v, want not-exist", err)
	}
}

func TestCleanExpiredZeroMaxAge(t *testing.T) {
	store := NewFileMediaStoreWithCleanup(MediaCleanerConfig{
		Enabled:  true,
		MaxAge:   0,
		Interval: time.Hour,
	})

	dir := t.TempDir()
	path := createTempFile(t, dir, "file.jpg")
	ref, _ := store.Store(path, MediaMeta{Source: "test"}, "scope1")

	// Zero MaxAge should be a no-op
	removed := store.CleanExpired()
	if removed != 0 {
		t.Errorf("expected 0 removed with zero MaxAge, got %d", removed)
	}
	if _, err := store.Resolve(ref); err != nil {
		t.Errorf("ref should still resolve: %v", err)
	}
}

func TestStartDisabledIsNoop(t *testing.T) {
	store := NewFileMediaStoreWithCleanup(MediaCleanerConfig{
		Enabled:  false,
		MaxAge:   time.Minute,
		Interval: time.Minute,
	})
	// Should not start any goroutine or panic
	store.Start()
	store.Stop()
}

func TestStartZeroIntervalNoPanic(t *testing.T) {
	store := NewFileMediaStoreWithCleanup(MediaCleanerConfig{
		Enabled:  true,
		MaxAge:   time.Minute,
		Interval: 0,
	})
	// Zero interval should not panic (time.NewTicker panics on <= 0)
	store.Start()
	store.Stop()
}

func TestStartZeroMaxAgeNoPanic(t *testing.T) {
	store := NewFileMediaStoreWithCleanup(MediaCleanerConfig{
		Enabled:  true,
		MaxAge:   0,
		Interval: time.Minute,
	})
	store.Start()
	store.Stop()
}

func TestConcurrentCleanupSafety(t *testing.T) {
	dir := t.TempDir()
	store := newTestStoreWithCleanup(50 * time.Millisecond)
	store.nowFunc = time.Now

	const workers = 10
	const ops = 20
	var wg sync.WaitGroup
	wg.Add(workers * 4)

	// Store workers
	for w := range workers {
		go func(wIdx int) {
			defer wg.Done()
			scope := fmt.Sprintf("scope-%d", wIdx)
			for i := range ops {
				p := createTempFile(t, dir, fmt.Sprintf("w%d-f%d.tmp", wIdx, i))
				store.Store(p, MediaMeta{Source: "test"}, scope)
			}
		}(w)
	}

	// Resolve workers
	for range workers {
		go func() {
			defer wg.Done()
			for range ops {
				store.Resolve("media://nonexistent")
			}
		}()
	}

	// ReleaseAll workers
	for w := range workers {
		go func(wIdx int) {
			defer wg.Done()
			for range ops {
				store.ReleaseAll(fmt.Sprintf("scope-%d", wIdx))
			}
		}(w)
	}

	// CleanExpired workers
	for range workers {
		go func() {
			defer wg.Done()
			for range ops {
				store.CleanExpired()
			}
		}()
	}

	wg.Wait()
}

func TestRefToScopeConsistency(t *testing.T) {
	dir := t.TempDir()
	store := NewFileMediaStore()

	// Store entries in two scopes
	ref1, _ := store.Store(createTempFile(t, dir, "a.jpg"), MediaMeta{Source: "test"}, "s1")
	ref2, _ := store.Store(createTempFile(t, dir, "b.jpg"), MediaMeta{Source: "test"}, "s1")
	ref3, _ := store.Store(createTempFile(t, dir, "c.jpg"), MediaMeta{Source: "test"}, "s2")

	store.mu.RLock()
	checkRef := func(ref, expectedScope string) {
		t.Helper()
		if scope, ok := store.refToScope[ref]; !ok || scope != expectedScope {
			t.Errorf("refToScope[%s] = %q, want %q", ref, scope, expectedScope)
		}
	}
	checkRef(ref1, "s1")
	checkRef(ref2, "s1")
	checkRef(ref3, "s2")
	store.mu.RUnlock()

	// Release s1 and verify refToScope is cleaned
	store.ReleaseAll("s1")

	store.mu.RLock()
	defer store.mu.RUnlock()
	if _, ok := store.refToScope[ref1]; ok {
		t.Error("refToScope should not contain ref1 after ReleaseAll")
	}
	if _, ok := store.refToScope[ref2]; ok {
		t.Error("refToScope should not contain ref2 after ReleaseAll")
	}
	if _, ok := store.refToScope[ref3]; !ok {
		t.Error("refToScope should still contain ref3")
	}
}
