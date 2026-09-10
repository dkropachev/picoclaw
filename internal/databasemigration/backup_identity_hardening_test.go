package databasemigration

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/sipeed/picoclaw/internal/fileidentity"
	"github.com/sipeed/picoclaw/internal/storecatalog"
)

func TestLoadBackupSessionRequiresStablePrivateParent(t *testing.T) {
	home := migrationHome(t)
	spec := storecatalog.Spec{ID: "global/auth", Path: filepath.Join(home, "auth.db")}
	session, err := snapshotBackup(
		t.Context(), time.Now, home,
		[]storecatalog.Spec{spec}, []storecatalog.Spec{spec}, "",
	)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS == "windows" {
		t.Skip("Unix permission assertion")
	}
	if err := os.Chmod(session.parent, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(session.parent, 0o700) })
	if loaded, err := loadBackupSession(t.Context(), session.root); loaded != nil || err == nil ||
		!strings.Contains(err.Error(), "parent") {
		t.Fatalf("load under public parent = %#v, %v", loaded, err)
	}
}

func TestSnapshotCanonicalizesCompleteSelectedSpec(t *testing.T) {
	home := migrationHome(t)
	spec := storecatalog.Spec{
		ID: "global/auth", Domain: "auth", Path: filepath.Join(home, "auth.db"), Required: true,
	}
	for _, mutate := range []func(*storecatalog.Spec){
		func(selected *storecatalog.Spec) { selected.Domain = "other" },
		func(selected *storecatalog.Spec) { selected.Required = false },
	} {
		selected := spec
		mutate(&selected)
		if session, err := snapshotBackup(
			t.Context(), time.Now, home,
			[]storecatalog.Spec{spec}, []storecatalog.Spec{selected}, "",
		); session != nil || err == nil || !strings.Contains(err.Error(), "differs") {
			t.Fatalf("changed selected fields = %#v, %v", session, err)
		}
	}
}

func TestCopyBackupFileRejectsDestinationReplacementAfterSecure(t *testing.T) {
	root := migrationHome(t)
	source := filepath.Join(root, "source")
	writeMigrationFile(t, source, []byte("source payload"))
	backupRoot := filepath.Join(root, "archive")
	if err := ensurePrivateBackupDirectory(backupRoot); err != nil {
		t.Fatal(err)
	}
	ops := defaultBackupCopyOps()
	originalSecure := ops.secure
	ops.secure = func(path string) error {
		if err := originalSecure(path); err != nil {
			return err
		}
		original := path + ".original"
		if err := os.Rename(path, original); err != nil {
			return err
		}
		return os.WriteFile(path, []byte("other payload!"), 0o600)
	}
	record, err := copyBackupFileWithOps(
		context.Background(), backupRoot, "global/auth", "legacy", source,
		"payload", newBackupBudget(), ops,
	)
	if err == nil || record.StoreID != "" || !strings.Contains(err.Error(), "identity") {
		t.Fatalf("destination replacement copy = %#v, %v", record, err)
	}
}

func TestSharedLiveVerificationIndexesCatalogAndRecordsOnce(t *testing.T) {
	home := migrationHome(t)
	specs := []storecatalog.Spec{
		{ID: "global/auth", Path: filepath.Join(home, "auth.db")},
		{ID: "global/models", Path: filepath.Join(home, "models.db")},
	}
	session := backupArchiveSnapshot(t, home, specs, specs)
	ops := defaultBackupLiveVerifyOps()
	originalLstat := ops.lstat
	lookups := 0
	ops.lstat = func(path string) (os.FileInfo, error) {
		lookups++
		return originalLstat(path)
	}
	state, err := session.newBackupLiveVerificationState(t.Context(), ops)
	if err != nil {
		t.Fatal(err)
	}
	for _, spec := range specs {
		if verifyErr := session.verifyLiveSourcesWithState(
			t.Context(), spec, ops, state,
		); verifyErr != nil {
			t.Fatal(verifyErr)
		}
	}
	// Eight catalog-generation exclusions are indexed once, then each of the
	// two selected four-member generations is inspected once.
	if lookups != 16 {
		t.Fatalf("shared live verification lstat calls = %d, want 16", lookups)
	}

	invalid := *session
	invalid.manifest.Files = append(invalid.manifest.Files, BackupFileManifest{StoreID: "unknown"})
	state, err = invalid.newBackupLiveVerificationState(t.Context(), defaultBackupLiveVerifyOps())
	if state != nil || err == nil || !strings.Contains(err.Error(), "unknown store") {
		t.Fatalf("unknown-store live index = %#v, %v", state, err)
	}
}

func TestBackupVerificationRejectsSameByteIdentityReplacement(t *testing.T) {
	_, _, session := backupArchiveGenerationSnapshot(t)
	record := session.manifest.Files[0]
	path := filepath.Join(session.root, filepath.FromSlash(record.Backup))
	originalInfo, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	payload, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	ops := defaultBackupVerifyOps()
	originalHash := ops.hash
	replaced := false
	ops.hash = func(
		ctx context.Context,
		path string,
		info os.FileInfo,
		expectedIdentity fileidentity.Identity,
		limit int64,
	) (string, int64, error) {
		digest, size, hashErr := originalHash(ctx, path, info, expectedIdentity, limit)
		if hashErr != nil || replaced {
			return digest, size, hashErr
		}
		replaced = true
		if err := os.Rename(path, filepath.Join(t.TempDir(), "original")); err != nil {
			return "", 0, err
		}
		if err := os.WriteFile(path, payload, 0o600); err != nil {
			return "", 0, err
		}
		if err := os.Chtimes(path, originalInfo.ModTime(), originalInfo.ModTime()); err != nil {
			return "", 0, err
		}
		return digest, size, nil
	}
	if err := session.verifyFilesWithOps(t.Context(), ops); err == nil ||
		!strings.Contains(err.Error(), "identities changed") {
		t.Fatalf("same-byte archive identity replacement = %v", err)
	}
}

func TestBackupVerificationRechecksFinalRootIdentity(t *testing.T) {
	_, _, session := backupArchiveGenerationSnapshot(t)
	other := migrationHome(t)
	otherIdentity, otherType, exists, err := fileidentity.ExistingWithType(other)
	if err != nil || !exists {
		t.Fatal(err)
	}
	ops := defaultBackupVerifyOps()
	originalIdentity := ops.identity
	calls := 0
	ops.identity = func(path string) (fileidentity.Identity, fileidentity.ObjectType, bool, error) {
		calls++
		if calls == 2 {
			return otherIdentity, otherType, true, nil
		}
		return originalIdentity(path)
	}
	if err := session.verifyFilesWithOps(t.Context(), ops); err == nil ||
		!strings.Contains(err.Error(), "root identity changed during") {
		t.Fatalf("final root identity transition = %v", err)
	}
}

func TestBackupVerificationRechecksBytesAfterHashing(t *testing.T) {
	for _, test := range []string{"payload", "manifest"} {
		t.Run(test, func(t *testing.T) {
			_, _, fresh := backupArchiveGenerationSnapshot(t)
			path := filepath.Join(fresh.root, backupManifestName)
			if test == "payload" {
				path = filepath.Join(fresh.root, filepath.FromSlash(fresh.manifest.Files[0].Backup))
			}
			ops := defaultBackupVerifyOps()
			exactTree := ops.exactTree
			calls := 0
			ops.exactTree = func(
				ctx context.Context, root string, manifest BackupManifest,
			) (map[fileidentity.Identity]string, error) {
				inventory, err := exactTree(ctx, root, manifest)
				calls++
				if calls == 2 && err == nil {
					payload, readErr := os.ReadFile(path)
					if readErr != nil {
						return nil, readErr
					}
					payload[0] ^= 1
					err = os.WriteFile(path, payload, 0o600)
				}
				return inventory, err
			}
			if err := fresh.verifyFilesWithOps(t.Context(), ops); err == nil {
				t.Fatal("post-hash byte change verified")
			}
		})
	}
}

func TestBackupArchiveRemainingInputBoundaries(t *testing.T) {
	if err := (*backupSession)(nil).verifyLiveSourcesWithState(
		t.Context(), storecatalog.Spec{}, defaultBackupLiveVerifyOps(),
		&backupLiveVerificationState{},
	); err == nil || !strings.Contains(err.Error(), "input") {
		t.Fatalf("nil live-source state receiver = %v", err)
	}
	home := migrationHome(t)
	overlongSidecar := storecatalog.Spec{
		ID: "global/auth", Path: filepath.Join(home, strings.Repeat("x", backupMaxComponent-4)),
	}
	if selected, err := validateBackupCatalogInputs(
		[]storecatalog.Spec{overlongSidecar}, []storecatalog.Spec{overlongSidecar},
	); selected != nil || err == nil || !strings.Contains(err.Error(), "generation path") {
		t.Fatalf("overlong catalog sidecar = %#v, %v", selected, err)
	}
	parent := migrationHome(t)
	session := &backupSession{
		root: filepath.Join(parent, "archive"), parent: parent,
		manifest: validManifestValidationFixture(t),
	}
	if err := commitBackupSnapshotWithOps(session, defaultBackupCommitOps()); err == nil ||
		!strings.Contains(err.Error(), "commit input") {
		t.Fatalf("finish with unpinned parent = %v", err)
	}
	_, _, published := backupArchiveGenerationSnapshot(t)
	if err := commitBackupSnapshotWithOps(
		published, defaultBackupCommitOps(),
	); err == nil || !strings.Contains(err.Error(), "commit input") {
		t.Fatalf("finish on published root = %v", err)
	}
	mismatched := *published
	mismatched.root += backupPartialSuffix
	mismatched.finalRoot += "-different"
	if err := commitBackupSnapshotWithOps(
		&mismatched, defaultBackupCommitOps(),
	); err == nil || !strings.Contains(err.Error(), "commit input") {
		t.Fatalf("finish on mismatched stage = %v", err)
	}
}
