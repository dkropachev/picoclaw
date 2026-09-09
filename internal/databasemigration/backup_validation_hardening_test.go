package databasemigration

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/sipeed/picoclaw/internal/fileidentity"
)

func TestPrivateBackupParentPinRejectsUnsafeAndChangedDirectories(t *testing.T) {
	if identity, err := pinPrivateBackupDirectory("relative"); identity.Valid() || err == nil {
		t.Fatalf("relative parent pin = %#v, %v", identity, err)
	}

	root := migrationHome(t)
	regular := filepath.Join(root, "regular")
	writeMigrationFile(t, regular, []byte("file"))
	if identity, err := pinPrivateBackupDirectory(regular); identity.Valid() || err == nil {
		t.Fatalf("regular parent pin = %#v, %v", identity, err)
	}

	private := filepath.Join(root, "private")
	if err := os.Mkdir(private, 0o700); err != nil {
		t.Fatal(err)
	}
	identity, err := pinPrivateBackupDirectory(private)
	if err != nil || !identity.Valid() {
		t.Fatalf("private parent pin = %#v, %v", identity, err)
	}
	if err := validatePinnedPrivateBackupDirectory(private, fileidentity.Identity{}); err == nil {
		t.Fatal("empty pinned parent identity was accepted")
	}
	replacement := private + ".replacement"
	if err := os.Rename(private, private+".original"); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(replacement, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(replacement, private); err != nil {
		t.Fatal(err)
	}
	if err := validatePinnedPrivateBackupDirectory(private, identity); err == nil ||
		!strings.Contains(err.Error(), "changed") {
		t.Fatalf("replaced private parent validation = %v", err)
	}
}

func TestPrivateBackupParentPinRejectsPublicMode(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix permission assertion")
	}
	root := migrationHome(t)
	if err := os.Chmod(root, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(root, 0o700) })
	if identity, err := pinPrivateBackupDirectory(root); identity.Valid() || err == nil ||
		!strings.Contains(err.Error(), "private") {
		t.Fatalf("public parent pin = %#v, %v", identity, err)
	}
}

func TestBackupManifestMetadataBudgetFailsBeforeAmplification(t *testing.T) {
	budget := newBackupBudget()
	budget.maxManifest = budget.manifestBytes + 16
	before := budget.manifestBytes
	if err := budget.reserveManifestStrings(0, strings.Repeat("x", 32)); err == nil ||
		!strings.Contains(err.Error(), "metadata budget") {
		t.Fatalf("oversized manifest metadata = %v", err)
	}
	if budget.manifestBytes != before {
		t.Fatalf("failed metadata reservation changed budget: %d -> %d", before, budget.manifestBytes)
	}
	budget.maxManifest = before - 1
	if err := budget.reserveManifestStrings(0); err == nil {
		t.Fatal("already-exhausted manifest budget was accepted")
	}
}

func TestPreparedLegacyWrapperBudgetBoundary(t *testing.T) {
	budget := newBackupBudget()
	if err := budget.reservePreparedLegacyRoots(32_768); err != nil {
		t.Fatalf("32,768 empty legacy roots rejected: %v", err)
	}
	if budget.preparedEntries != backupMaxEntries {
		t.Fatalf("prepared wrapper entries = %d, want %d", budget.preparedEntries, backupMaxEntries)
	}
	if err := budget.reservePreparedLegacyRoots(1); err == nil {
		t.Fatal("32,769th empty legacy root was accepted")
	}
	if err := newBackupBudget().reservePreparedLegacyRoots(32_769); err == nil {
		t.Fatal("32,769 empty legacy roots were accepted")
	}
}

func TestBackupIdentityHelpersRejectWrongObjectTypes(t *testing.T) {
	root := migrationHome(t)
	file := filepath.Join(root, "file")
	writeMigrationFile(t, file, []byte("file"))
	if identity, err := backupExistingIdentity(file, fileidentity.ObjectTypeDirectory); identity.Valid() ||
		err == nil {
		t.Fatalf("regular as directory identity = %#v, %v", identity, err)
	}
	opened, err := os.Open(file)
	if err != nil {
		t.Fatal(err)
	}
	defer opened.Close()
	if identity, err := backupOpenedIdentity(opened, fileidentity.ObjectTypeDirectory); identity.Valid() ||
		err == nil {
		t.Fatalf("opened regular as directory identity = %#v, %v", identity, err)
	}
	if identity, err := backupPathMatchesOpened(
		root, opened, fileidentity.ObjectTypeRegular,
	); identity.Valid() || err == nil {
		t.Fatalf("mismatched path/handle identity = %#v, %v", identity, err)
	}
	if identity, err := backupOpenedIdentity(nil, fileidentity.ObjectTypeRegular); identity.Valid() ||
		!errors.Is(err, fileidentity.ErrInvalidPath) {
		t.Fatalf("nil opened identity = %#v, %v", identity, err)
	}
}

/*
func TestExactBackupTreeRejectsBoundedGrowthAndPhysicalAliases(t *testing.T) {
	root := migrationHome(t)
	file := filepath.Join(root, "member")
	writeMigrationFile(t, file, []byte("member"))
	rootHandle, err := os.OpenRoot(root)
	if err != nil {
		t.Fatal(err)
	}
	defer rootHandle.Close()
	if verifyErr := verifyExactBackupDirectory(
		t.Context(), root, rootHandle, ".",
		map[string]backupTreeKind{".": backupTreeDirectory},
		map[string]struct{}{".": {}}, map[fileidentity.Identity]string{},
	); verifyErr == nil || !strings.Contains(verifyErr.Error(), "entry limit") {
		t.Fatalf("bounded exact-tree growth = %v", verifyErr)
	}
	info, err := os.Lstat(file)
	if err != nil {
		t.Fatal(err)
	}
	identity, objectType, exists, err := fileidentity.ExistingWithType(file)
	if err != nil || !exists || objectType != fileidentity.ObjectTypeRegular {
		t.Fatalf("member identity = %#v, %v, %t, %v", identity, objectType, exists, err)
	}
	if verifyErr := verifyExactBackupRegular(
		root, rootHandle, "member", info,
		map[fileidentity.Identity]string{identity: "other-member"},
	); verifyErr == nil || !strings.Contains(verifyErr.Error(), "physical object alias") {
		t.Fatalf("exact-tree file alias = %v", verifyErr)
	}
}
*/
