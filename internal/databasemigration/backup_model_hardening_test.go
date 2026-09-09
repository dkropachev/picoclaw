package databasemigration

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBackupModelBudgetsRejectAmplification(t *testing.T) {
	if err := (*backupBudget)(nil).enter("."); err == nil {
		t.Fatal("nil traversal budget succeeded")
	}
	for _, value := range []string{
		string(os.PathSeparator), "..", filepath.Join("..", "escape"),
	} {
		if err := newBackupBudget().enter(value); err == nil {
			t.Errorf("traversal budget accepted %q", value)
		}
	}
	budget := &backupBudget{
		maxEntries: 1, maxFiles: 1, maxDepth: 1, maxFileBytes: 2, maxBytes: 2,
	}
	if err := budget.enter("one"); err != nil {
		t.Fatal(err)
	}
	if err := budget.enter("two"); err == nil {
		t.Fatal("entry budget accepted excess traversal")
	}
	if err := (&backupBudget{maxEntries: 2, maxDepth: 1}).enter(
		filepath.Join("one", "two"),
	); err == nil {
		t.Fatal("depth budget accepted excess nesting")
	}

	if err := (*backupBudget)(nil).reserveFile(0); err == nil {
		t.Fatal("nil file budget succeeded")
	}
	if err := newBackupBudget().reserveFile(-1); err == nil {
		t.Fatal("negative file size succeeded")
	}
	if err := budget.reserveFile(2); err != nil {
		t.Fatal(err)
	}
	if err := budget.reserveFile(0); err == nil {
		t.Fatal("file-count budget accepted excess file")
	}
	if err := (&backupBudget{maxFiles: 1, maxFileBytes: 1, maxBytes: 1}).reserveFile(2); err == nil {
		t.Fatal("per-file budget accepted oversized file")
	}
	if err := (&backupBudget{maxFiles: 1, maxFileBytes: 2, maxBytes: 1}).reserveFile(2); err == nil {
		t.Fatal("aggregate-byte budget accepted oversized file")
	}

	if err := (*backupBudget)(nil).reserveArchivePath("file"); err == nil {
		t.Fatal("nil archive budget succeeded")
	}
	if err := newBackupBudget().reserveArchivePath(".."); err == nil {
		t.Fatal("unsafe archive path was accepted")
	}
	if err := (&backupBudget{maxEntries: 1}).reserveArchivePath(
		filepath.Join("one", "two"),
	); err == nil {
		t.Fatal("archive-entry budget accepted path expansion")
	}
	archiveBudget := newBackupBudget()
	if err := archiveBudget.reserveArchivePath(filepath.Join("one", "two")); err != nil {
		t.Fatal(err)
	}

	if err := (*backupBudget)(nil).reservePreparedLegacyRoots(0); err == nil {
		t.Fatal("nil prepared-root budget succeeded")
	}
	for _, count := range []int{-1, backupMaxLegacyRoots + 1} {
		if err := newBackupBudget().reservePreparedLegacyRoots(count); err == nil {
			t.Errorf("prepared-root budget accepted %d", count)
		}
	}
	preparedBudget := newBackupBudget()
	if err := preparedBudget.reservePreparedLegacyRoots(1); err != nil {
		t.Fatal(err)
	}
	if err := preparedBudget.reservePreparedLegacyPath("."); err != nil {
		t.Fatal(err)
	}
	if err := preparedBudget.reservePreparedLegacyPath(filepath.Join("nested", "file")); err != nil {
		t.Fatal(err)
	}
	for _, value := range []string{
		"", "dirty" + string(os.PathSeparator) + ".." + string(os.PathSeparator) + "path", t.TempDir(),
	} {
		if err := preparedBudget.reservePreparedLegacyPath(value); err == nil {
			t.Errorf("prepared-path budget accepted %q", value)
		}
	}
	preparedBudget.preparedEntries = preparedBudget.maxEntries
	if err := preparedBudget.reservePreparedLegacyPath("nested"); err == nil {
		t.Fatal("prepared entry expansion exceeded its budget")
	}
}

func TestBackupModelManifestMetadataBudgetIsTransactional(t *testing.T) {
	if err := (*backupBudget)(nil).reserveManifestStrings(0); err == nil {
		t.Fatal("nil manifest budget succeeded")
	}
	for _, fixed := range []int64{-1, 1} {
		budget := &backupBudget{maxManifest: 0}
		if err := budget.reserveManifestStrings(fixed); err == nil {
			t.Errorf("manifest fixed cost %d was accepted", fixed)
		}
	}
	budget := newBackupBudget()
	before := budget.manifestBytes
	budget.maxManifest = before + 20
	if err := budget.reserveManifestStrings(0, strings.Repeat("x", 20)); err == nil {
		t.Fatal("oversized encoded manifest string was accepted")
	}
	if budget.manifestBytes != before {
		t.Fatal("failed manifest reservation mutated the budget")
	}
	budget.maxManifest = backupMaxManifestSize
	store := validManifestValidationFixture(t).Stores[0]
	if err := budget.reserveManifestStore(store); err != nil {
		t.Fatal(err)
	}
	if err := budget.reserveManifestFile(validManifestValidationFixture(t).Files[0]); err != nil {
		t.Fatal(err)
	}
}

func TestBackupModelPathGrammarAndDerivations(t *testing.T) {
	root := migrationHome(t)
	base := filepath.Join(root, "legacy")
	for _, test := range []struct {
		source string
		want   string
		inside bool
	}{
		{source: base, want: ".", inside: true},
		{source: filepath.Join(base, "nested", "file"), want: filepath.Join("nested", "file"), inside: true},
		{source: filepath.Join(root, "other")},
	} {
		got, inside, err := legacySourceRelative(base, test.source)
		if err != nil || got != test.want || inside != test.inside {
			t.Errorf("legacySourceRelative(%q) = %q, %t, %v", test.source, got, inside, err)
		}
	}
	if _, _, err := legacySourceRelative("relative", base); err == nil {
		t.Fatal("relative legacy root was accepted")
	}

	invalidUTF8 := string([]byte{'b', 'a', 'd', 0xff})
	overlong := strings.Repeat("x", backupMaxComponent+1)
	deep := "leaf"
	for range backupMaxArchiveDepth {
		deep = filepath.Join("nested", deep)
	}
	for _, value := range []string{
		".", "..", filepath.Join("..", "escape"), string(os.PathSeparator),
		invalidUTF8, overlong, filepath.Join("stores", overlong), deep,
	} {
		if path, err := backupFilePath(root, value); path != "" || err == nil {
			t.Errorf("backupFilePath(%q) = %q, %v", value, path, err)
		}
	}
	validRelative := filepath.Join("stores", "auth")
	if path, err := backupFilePath(root, validRelative); err != nil ||
		path != filepath.Join(root, validRelative) {
		t.Fatalf("valid backup path = %q, %v", path, err)
	}
	for _, value := range []string{"", ".", "..", t.TempDir(), "dirty/../path", deep} {
		if safeBackupRelative(value) {
			t.Errorf("unsafe relative path accepted: %q", value)
		}
	}
	if !safeBackupRelative(validRelative) || !validBackupManifestRelative(filepath.ToSlash(validRelative)) {
		t.Fatal("safe manifest-relative path rejected")
	}
	if validBackupManifestRelative("dirty/../path") {
		t.Fatal("noncanonical manifest path accepted")
	}
	if validBackupAbsolutePath("relative") || !validBackupAbsolutePath(root) {
		t.Fatal("absolute path grammar result is inverted")
	}
	if backupPathDepth(string(os.PathSeparator)) <= backupMaxArchiveDepth {
		t.Fatal("filesystem root depth did not fail closed")
	}
	if !validBackupPathComponent("safe") || validBackupPathComponent("..") ||
		validBackupPathComponent(overlong) {
		t.Fatal("path component grammar result is invalid")
	}
	if !backupPathHasSuffix(filepath.Join(root, "evidence.partial"), ".partial") ||
		backupPathHasSuffix(filepath.Join(root, "evidence"), ".partial") {
		t.Fatal("backup suffix classification is invalid")
	}
	if paths := generationPaths(filepath.Join(root, "store.db")); len(paths) != 4 ||
		paths[1] != filepath.Join(root, "store.db")+"-wal" {
		t.Fatalf("generation paths = %q", paths)
	}
}

func TestBackupModelCanonicalOrderingAndEncoding(t *testing.T) {
	files := []BackupFileManifest{
		{StoreID: "global/models", Role: "database"},
		{StoreID: "global/auth", Role: "legacy", Source: "/z"},
		{StoreID: "global/auth", Role: "database"},
	}
	sortBackupManifestFiles(files)
	if files[0].StoreID != "global/auth" || files[0].Role != "database" ||
		files[2].StoreID != "global/models" {
		t.Fatalf("canonical files = %#v", files)
	}
	for role, want := range map[string]int{
		"database": 0, "wal": 1, "shm": 2, "journal": 3, "legacy": 4, "other": 5,
	} {
		if got := backupRoleOrder(role); got != want {
			t.Errorf("backupRoleOrder(%q) = %d, want %d", role, got, want)
		}
	}
	first := backupStoreDirectory("a/b.c")
	second := backupStoreDirectory("a.b/c")
	if first == second || filepath.IsAbs(first) || strings.Contains(first, "..") {
		t.Fatalf("unsafe/noninjective store directories: %q, %q", first, second)
	}
	long := backupStoreDirectory("global/" + strings.Repeat("a", 200))
	for _, component := range strings.Split(long, string(os.PathSeparator)) {
		if len(component) > backupMaxComponent {
			t.Fatalf("oversized encoded component %q", component)
		}
	}
}

func TestBackupModelManifestMarshalBounds(t *testing.T) {
	manifest := validManifestValidationFixture(t)
	payload, err := marshalBackupManifest(manifest)
	if err != nil || len(payload) == 0 || payload[len(payload)-1] != '\n' {
		t.Fatalf("canonical manifest = %q, %v", payload, err)
	}
	if payload, err := marshalBackupManifestLimit(manifest, 0); payload != nil || err == nil {
		t.Fatalf("zero manifest limit = %q, %v", payload, err)
	}
	if payload, err := marshalBackupManifestLimit(manifest, 1); payload != nil || err == nil {
		t.Fatalf("small manifest limit = %q, %v", payload, err)
	}
	invalid := manifest
	invalid.Version++
	if payload, err := marshalBackupManifest(invalid); payload != nil || err == nil {
		t.Fatalf("invalid manifest marshal = %q, %v", payload, err)
	}
	if err := validateBackupManifestLimit(manifest, 1); err == nil ||
		!strings.Contains(err.Error(), "metadata budget") {
		t.Fatalf("aggregate metadata manifest validation = %v", err)
	}
	if err := validateBackupManifestLimit(manifest, 0); err == nil {
		t.Fatal("zero metadata limit was accepted")
	}
}

func TestBackupModelWindowsPathGrammar(t *testing.T) {
	if validWindowsBackupPathComponents(`C:relative`, true) {
		t.Fatal("invalid Windows path components accepted")
	}
	for _, path := range []string{
		`C:relative`, `\\?\C:\data\store.db`, `\\.\C:\data\store.db`,
		`\??\C:\data\store.db`, `\rooted`, `\\CON\share\store.db`,
		`\\server\NUL\store.db`,
	} {
		if validWindowsBackupPathString(path, true) {
			t.Errorf("unsafe absolute Windows path accepted: %q", path)
		}
	}
	for _, component := range []string{
		"CON", "nul.txt", "CLOCK$", "CONIN$", "COM1", "LPT9.log", "COM¹.txt",
		"name.", "name ", "file:stream", "PROGRA~1", "DATA~12.json", "control\x01",
	} {
		if validWindowsBackupComponent(component) {
			t.Errorf("unsafe Windows component accepted: %q", component)
		}
	}
	if !validWindowsBackupPathString(`C:\data\store.db`, true) ||
		!validWindowsBackupPathString(`\\server\share\store.db`, true) ||
		!validWindowsBackupPathString(`stores\auth\generation\database`, false) ||
		!validWindowsBackupComponent("database.db") ||
		!validWindowsBackupComponent("release~candidate.db") {
		t.Fatal("safe Windows path grammar rejected")
	}
	for _, path := range []string{
		`C:\safe\NUL\store.db`,
		`C:\safe\file:stream\store.db`,
		`C:\safe\PROGRA~1\store.db`,
		`\\server\share\safe\name.\store.db`,
	} {
		if validWindowsBackupPathComponents(path, true) {
			t.Errorf("unsafe nested Windows component accepted: %q", path)
		}
	}
	if !validWindowsBackupPathComponents(`C:\safe\release~candidate\store.db`, true) ||
		!validWindowsBackupPathComponents(`relative\safe\store.db`, false) {
		t.Fatal("safe nested Windows components rejected")
	}
}
