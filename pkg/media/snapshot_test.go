package media

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestFileMediaStoreReadSnapshotDetachedAndBounded(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "source.bin")
	wantBytes := []byte("durable media bytes")
	if err := os.WriteFile(path, wantBytes, 0o600); err != nil {
		t.Fatal(err)
	}

	store := NewFileMediaStore()
	ref, err := store.Store(path, MediaMeta{
		Filename:      "source.bin",
		ContentType:   "application/octet-stream",
		Source:        "snapshot-test",
		CleanupPolicy: CleanupPolicyForgetOnly,
	}, "scope")
	if err != nil {
		t.Fatalf("Store() error = %v", err)
	}

	snapshot, err := store.ReadSnapshot(context.Background(), ref, int64(len(wantBytes)))
	if err != nil {
		t.Fatalf("ReadSnapshot() error = %v", err)
	}
	if !bytes.Equal(snapshot.Bytes, wantBytes) {
		t.Fatalf("ReadSnapshot() bytes = %q, want %q", snapshot.Bytes, wantBytes)
	}
	if snapshot.Meta.Filename != "source.bin" ||
		snapshot.Meta.ContentType != "application/octet-stream" ||
		snapshot.Meta.Source != "snapshot-test" ||
		snapshot.Meta.CleanupPolicy != CleanupPolicyForgetOnly {
		t.Fatalf("ReadSnapshot() meta = %#v", snapshot.Meta)
	}

	// Returned state must not alias either the source file or a later read.
	snapshot.Bytes[0] = 'X'
	snapshot.Meta.Filename = "mutated"
	again, err := store.ReadSnapshot(context.Background(), ref, int64(len(wantBytes)))
	if err != nil {
		t.Fatalf("second ReadSnapshot() error = %v", err)
	}
	if !bytes.Equal(again.Bytes, wantBytes) || again.Meta.Filename != "source.bin" {
		t.Fatalf("second ReadSnapshot() = %#v", again)
	}

	_, err = store.ReadSnapshot(context.Background(), ref, int64(len(wantBytes)-1))
	if !errors.Is(err, ErrSnapshotTooLarge) {
		t.Fatalf("oversized ReadSnapshot() error = %v, want %v", err, ErrSnapshotTooLarge)
	}
}

func TestFileMediaStoreReadSnapshotRejectsUnsafeSourcesWithoutDisclosure(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	store := NewFileMediaStore()

	directoryRef, err := store.Store(dir, MediaMeta{}, "scope")
	if err != nil {
		t.Fatalf("Store(directory) error = %v", err)
	}
	_, err = store.ReadSnapshot(context.Background(), directoryRef, 1024)
	if !errors.Is(err, ErrSnapshotNotRegular) {
		t.Fatalf("ReadSnapshot(directory) error = %v, want %v", err, ErrSnapshotNotRegular)
	}
	assertFrozenErrorRedacted(t, err, directoryRef, dir)

	target := filepath.Join(dir, "secret-target")
	if writeErr := os.WriteFile(target, []byte("secret"), 0o600); writeErr != nil {
		t.Fatal(writeErr)
	}
	symlink := filepath.Join(dir, "secret-link")
	if symlinkErr := os.Symlink(target, symlink); symlinkErr != nil {
		t.Skipf("symlinks unavailable: %v", symlinkErr)
	}
	symlinkRef, storeErr := store.Store(symlink, MediaMeta{}, "scope")
	if storeErr != nil {
		t.Fatalf("Store(symlink) error = %v", storeErr)
	}
	_, err = store.ReadSnapshot(context.Background(), symlinkRef, 1024)
	if !errors.Is(err, ErrSnapshotNotRegular) {
		t.Fatalf("ReadSnapshot(symlink) error = %v, want %v", err, ErrSnapshotNotRegular)
	}
	assertFrozenErrorRedacted(t, err, symlinkRef, target, symlink)

	unknown := "media://top-secret-reference"
	_, err = store.ReadSnapshot(context.Background(), unknown, 1024)
	if !errors.Is(err, ErrSnapshotUnavailable) {
		t.Fatalf("ReadSnapshot(unknown) error = %v, want %v", err, ErrSnapshotUnavailable)
	}
	assertFrozenErrorRedacted(t, err, unknown)
}

func TestFileMediaStoreReadSnapshotHonorsCancellation(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := NewFileMediaStore().ReadSnapshot(ctx, "media://secret", 1024)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("ReadSnapshot() error = %v, want context cancellation", err)
	}
}

func TestFileMediaStoreReadSnapshotRejectsInvalidLimitAndNoncanonicalRef(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "source.bin")
	if err := os.WriteFile(path, []byte("source"), 0o600); err != nil {
		t.Fatal(err)
	}
	store := NewFileMediaStore()
	ref, err := store.Store(path, MediaMeta{}, "scope")
	if err != nil {
		t.Fatalf("Store() error = %v", err)
	}

	for _, limit := range []int64{0, -1} {
		_, err := store.ReadSnapshot(context.Background(), ref, limit)
		if !errors.Is(err, ErrSnapshotUnavailable) {
			t.Fatalf("ReadSnapshot(limit=%d) error = %v, want %v", limit, err, ErrSnapshotUnavailable)
		}
		assertFrozenErrorRedacted(t, err, ref, path)
	}

	noncanonical := []string{
		"media://AAAAAAAA-AAAA-4AAA-8AAA-AAAAAAAAAAAA",
		"media://{aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa}",
		"media://aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa/extra",
		"MEDIA://aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa",
	}
	for _, candidate := range noncanonical {
		_, err := store.ReadSnapshot(context.Background(), candidate, 1024)
		if !errors.Is(err, ErrSnapshotUnavailable) {
			t.Fatalf("ReadSnapshot(%q) error = %v, want %v", candidate, err, ErrSnapshotUnavailable)
		}
		assertFrozenErrorRedacted(t, err, candidate, path)
	}
}

func TestFileMediaStoreSnapshotRemainsDetachedAfterRelease(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "released.bin")
	want := []byte("captured before release")
	if err := os.WriteFile(path, want, 0o600); err != nil {
		t.Fatal(err)
	}
	store := NewFileMediaStore()
	ref, err := store.Store(path, MediaMeta{
		Filename:    "released.bin",
		ContentType: "application/octet-stream",
	}, "scope")
	if err != nil {
		t.Fatalf("Store() error = %v", err)
	}
	snapshot, err := store.ReadSnapshot(context.Background(), ref, int64(len(want)))
	if err != nil {
		t.Fatalf("ReadSnapshot() error = %v", err)
	}

	if err := store.ReleaseAll("scope"); err != nil {
		t.Fatalf("ReleaseAll() error = %v", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("managed source still exists after release: %v", err)
	}
	_, readAfterReleaseErr := store.ReadSnapshot(
		context.Background(),
		ref,
		int64(len(want)),
	)
	if !errors.Is(readAfterReleaseErr, ErrSnapshotUnavailable) {
		t.Fatalf(
			"ReadSnapshot(after release) error = %v, want %v",
			readAfterReleaseErr,
			ErrSnapshotUnavailable,
		)
	}
	if !bytes.Equal(snapshot.Bytes, want) || snapshot.Meta.Filename != "released.bin" {
		t.Fatalf("captured snapshot changed after release: %#v", snapshot)
	}
}

func TestSnapshotChangeTokenDetectsSameSizeRewriteWithRestoredModTime(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "change-token.bin")
	if err := os.WriteFile(path, []byte("before"), 0o600); err != nil {
		t.Fatal(err)
	}
	file, err := openSnapshotFileNoFollow(path)
	if err != nil {
		t.Fatalf("openSnapshotFileNoFollow() error = %v", err)
	}
	defer file.Close()

	before, err := file.Stat()
	if err != nil {
		t.Fatal(err)
	}
	beforeToken, err := snapshotFileChangeToken(file, before)
	if err != nil {
		t.Fatalf("snapshotFileChangeToken(before) error = %v", err)
	}
	if writeErr := os.WriteFile(path, []byte("after!"), 0o600); writeErr != nil {
		t.Fatal(writeErr)
	}
	if chtimesErr := os.Chtimes(path, before.ModTime(), before.ModTime()); chtimesErr != nil {
		t.Fatal(chtimesErr)
	}
	after, err := file.Stat()
	if err != nil {
		t.Fatal(err)
	}
	afterToken, err := snapshotFileChangeToken(file, after)
	if err != nil {
		t.Fatalf("snapshotFileChangeToken(after) error = %v", err)
	}
	if before.Size() != after.Size() || !before.ModTime().Equal(after.ModTime()) ||
		!sameSnapshotFile(before, after) {
		t.Fatal("test setup changed an ordinary size/modtime/identity check")
	}
	if sameSnapshotChangeToken(beforeToken, afterToken) {
		t.Fatal("change token did not detect same-size rewrite")
	}
}

func assertFrozenErrorRedacted(t *testing.T, err error, secrets ...string) {
	t.Helper()
	if err == nil {
		t.Fatal("expected error")
	}
	for _, secret := range secrets {
		if secret != "" && strings.Contains(err.Error(), secret) {
			t.Fatalf("error %q disclosed %q", err, secret)
		}
	}
}
