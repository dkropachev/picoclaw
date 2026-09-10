//nolint:govet // Independent failure-boundary assertions intentionally reuse narrow error names.
package sqliteprovider

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestCoverageProviderControlAndDiagnosticBoundaries(t *testing.T) {
	ctx := context.Background()
	canary := errors.New("provider diagnostic canary")

	if _, err := SchemaVersion(ctx, nil); err == nil {
		t.Fatal("SchemaVersion accepted a nil query boundary")
	}
	database := openProviderScript(t, providerRow("user_version", int64(7)))
	if version, err := SchemaVersion(ctx, database); err != nil || version != 7 {
		t.Fatalf("SchemaVersion = %d, %v", version, err)
	}
	database = openProviderScript(t, providerScriptStep{query: "user_version", err: canary})
	if _, err := SchemaVersion(ctx, database); !errors.Is(err, canary) {
		t.Fatalf("SchemaVersion error = %v", err)
	}
	if err := SetSchemaVersion(ctx, nil, 1); err == nil {
		t.Fatal("SetSchemaVersion accepted a nil execution boundary")
	}
	if err := SetSchemaVersion(ctx, openProviderScript(t), -1); err == nil {
		t.Fatal("SetSchemaVersion accepted a negative version")
	}
	database = openProviderScript(t, providerScriptStep{query: "user_version"})
	if err := SetSchemaVersion(ctx, database, 4); err != nil {
		t.Fatal(err)
	}
	database = openProviderScript(t, providerScriptStep{query: "user_version", err: canary})
	if err := SetSchemaVersion(ctx, database, 4); !errors.Is(err, canary) {
		t.Fatalf("SetSchemaVersion error = %v", err)
	}

	if err := CheckIntegrity(ctx, nil); err == nil {
		t.Fatal("CheckIntegrity accepted a nil query boundary")
	}
	database = openProviderScript(t,
		providerRow("integrity_check", "ok"),
		providerScriptStep{query: "foreign_key_check", columns: []string{"table"}},
	)
	if err := CheckIntegrity(ctx, database); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name  string
		steps []providerScriptStep
	}{
		{name: "integrity query", steps: []providerScriptStep{{query: "integrity_check", err: canary}}},
		{name: "corrupt result", steps: []providerScriptStep{providerRow("integrity_check", "broken")}},
		{name: "foreign key query", steps: []providerScriptStep{
			providerRow("integrity_check", "ok"),
			{query: "foreign_key_check", err: canary},
		}},
		{name: "foreign key row", steps: []providerScriptStep{
			providerRow("integrity_check", "ok"),
			{
				query: "foreign_key_check", columns: []string{"table"},
				rows: [][]driver.Value{{"child"}},
			},
		}},
		{name: "foreign key rows error", steps: []providerScriptStep{
			providerRow("integrity_check", "ok"),
			{query: "foreign_key_check", columns: []string{"table"}, rowsErr: canary},
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			if err := CheckIntegrity(ctx, openProviderScript(t, test.steps...)); err == nil {
				t.Fatal("CheckIntegrity accepted a failed diagnostic")
			}
		})
	}

	if err := CheckIntegrityOnly(ctx, nil); err == nil {
		t.Fatal("CheckIntegrityOnly accepted nil")
	}
	if err := CheckForeignKeys(ctx, nil); err == nil {
		t.Fatal("CheckForeignKeys accepted nil")
	}
}

func TestCoverageProviderPreparationAndConfigurationEdges(t *testing.T) {
	for _, path := range []string{"", " ", "file:store.db", "bad\x00.db"} {
		if err := PrepareStore(path); err == nil {
			t.Fatalf("PrepareStore(%q) succeeded", path)
		}
	}
	for _, path := range []string{"", " ", "bad\x00dir"} {
		if err := EnsurePrivateDirectory(path); err == nil {
			t.Fatalf("EnsurePrivateDirectory(%q) succeeded", path)
		}
	}
	blocked := filepath.Join(t.TempDir(), "blocked")
	if err := os.WriteFile(blocked, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := EnsurePrivateDirectory(blocked); err == nil {
		t.Fatal("EnsurePrivateDirectory accepted a file")
	}
	if err := PrepareStore(filepath.Join(blocked, "store.db")); err == nil {
		t.Fatal("PrepareStore accepted a file parent")
	}

	if _, err := OpenStore("file:forbidden.db", time.Second); err == nil {
		t.Fatal("OpenStore accepted a provider URI")
	}
	if err := Configure(t.Context(), nil, time.Second, false); err == nil {
		t.Fatal("Configure accepted nil")
	}
	if _, err := EnableWAL(t.Context(), nil, time.Second); err == nil {
		t.Fatal("EnableWAL accepted nil")
	}
	if err := ConfigureOffline(t.Context(), nil, time.Second); err == nil {
		t.Fatal("ConfigureOffline accepted nil")
	}
	memory, err := OpenStore(":memory:", time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer memory.Close()
	if err := Configure(nil, memory, time.Second, true); err == nil {
		t.Fatal("Configure accepted nil context")
	}
	if err := Configure(t.Context(), memory, time.Second, false); err == nil {
		t.Fatal("file-backed configuration accepted memory journal mode")
	}
	if _, err := EnableWAL(nil, memory, time.Second); err == nil {
		t.Fatal("EnableWAL accepted nil context")
	}
	if err := ConfigureOffline(nil, memory, time.Second); err == nil {
		t.Fatal("offline memory configuration succeeded")
	}
}

func TestCoverageOpenStoreFailurePhases(t *testing.T) {
	canary := errors.New("open store canary")
	if database, err := openStore(":memory:", time.Second, providerOpenOps{}); err == nil || database != nil {
		t.Fatalf("missing open operations = %#v, %v", database, err)
	}
	identityPath := filepath.Join(t.TempDir(), "identity.db")
	if err := os.WriteFile(identityPath, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	identity, err := os.Lstat(identityPath)
	if err != nil {
		t.Fatal(err)
	}
	otherIdentityPath := filepath.Join(t.TempDir(), "other.db")
	if err := os.WriteFile(otherIdentityPath, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	otherIdentity, err := os.Lstat(otherIdentityPath)
	if err != nil {
		t.Fatal(err)
	}
	closed, err := OpenStore(":memory:", time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if err := closed.Close(); err != nil {
		t.Fatal(err)
	}
	base := func() providerOpenOps {
		return providerOpenOps{
			validateAncestors: func(string) error { return nil },
			prepare:           func(string) error { return nil },
			dsn:               func(string, time.Duration) (string, error) { return "dsn", nil },
			open:              func(string) (*sql.DB, error) { return closed, nil },
			ping:              func(*sql.DB, time.Duration) error { return nil },
			mainIdentity:      func(string) (os.FileInfo, error) { return identity, nil },
			generationIdentity: func(string, os.FileInfo) ([4]os.FileInfo, error) {
				return [4]os.FileInfo{identity}, nil
			},
			secure: func(string) error { return nil },
		}
	}
	filePath := filepath.Join(t.TempDir(), "store.db")
	tests := []struct {
		name   string
		path   string
		mutate func(*providerOpenOps)
	}{
		{name: "memory dsn", path: ":memory:", mutate: func(ops *providerOpenOps) {
			ops.dsn = func(string, time.Duration) (string, error) { return "", canary }
		}},
		{name: "memory open", path: ":memory:", mutate: func(ops *providerOpenOps) {
			ops.open = func(string) (*sql.DB, error) { return nil, canary }
		}},
		{name: "ancestor", path: filePath, mutate: func(ops *providerOpenOps) {
			ops.validateAncestors = func(string) error { return canary }
		}},
		{name: "prepare", path: filePath, mutate: func(ops *providerOpenOps) {
			ops.prepare = func(string) error { return canary }
		}},
		{name: "dsn", path: filePath, mutate: func(ops *providerOpenOps) {
			ops.dsn = func(string, time.Duration) (string, error) { return "", canary }
		}},
		{name: "open", path: filePath, mutate: func(ops *providerOpenOps) {
			ops.open = func(string) (*sql.DB, error) { return nil, canary }
		}},
		{name: "ping", path: filePath, mutate: func(ops *providerOpenOps) {
			ops.ping = func(*sql.DB, time.Duration) error { return canary }
		}},
		{name: "main identity", path: filePath, mutate: func(ops *providerOpenOps) {
			ops.mainIdentity = func(string) (os.FileInfo, error) { return nil, canary }
		}},
		{name: "generation identity", path: filePath, mutate: func(ops *providerOpenOps) {
			ops.generationIdentity = func(string, os.FileInfo) ([4]os.FileInfo, error) {
				return [4]os.FileInfo{}, canary
			}
		}},
		{name: "secure", path: filePath, mutate: func(ops *providerOpenOps) {
			ops.secure = func(string) error { return canary }
		}},
		{name: "main replacement", path: filePath, mutate: func(ops *providerOpenOps) {
			calls := 0
			ops.mainIdentity = func(string) (os.FileInfo, error) {
				calls++
				if calls == 1 {
					return identity, nil
				}
				return otherIdentity, nil
			}
		}},
		{name: "rollback journal replacement", path: filePath, mutate: func(ops *providerOpenOps) {
			calls := 0
			ops.generationIdentity = func(string, os.FileInfo) ([4]os.FileInfo, error) {
				calls++
				if calls == 1 {
					return [4]os.FileInfo{identity, nil, nil, identity}, nil
				}
				return [4]os.FileInfo{identity, nil, nil, otherIdentity}, nil
			}
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			ops := base()
			test.mutate(&ops)
			if database, err := openStore(test.path, time.Second, ops); err == nil || database != nil {
				t.Fatalf("openStore = %#v, %v", database, err)
			}
		})
	}
}

func TestCoverageProviderScriptedConfigurationFailures(t *testing.T) {
	canary := errors.New("configuration canary")
	for _, test := range []struct {
		name  string
		steps []providerScriptStep
	}{
		{name: "journal query", steps: []providerScriptStep{{query: "journal_mode", err: canary}}},
		{name: "wrong journal", steps: []providerScriptStep{providerRow("journal_mode", "delete")}},
		{name: "verification query", steps: []providerScriptStep{
			providerRow("journal_mode", "wal"), {query: "pragma_foreign_keys", err: canary},
		}},
		{name: "wrong settings", steps: []providerScriptStep{
			providerRow("journal_mode", "wal"),
			providerRow("pragma_foreign_keys", int64(0), int64(1), int64(0)),
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			if err := Configure(t.Context(), openProviderScript(t, test.steps...), time.Second, false); err == nil {
				t.Fatal("invalid live provider configuration succeeded")
			}
		})
	}
	if err := Configure(t.Context(), openProviderScript(t,
		providerRow("journal_mode", "wal"),
		providerRow("pragma_foreign_keys", int64(1), int64(1000), int64(2)),
	), time.Second, false); err != nil {
		t.Fatal(err)
	}

	for _, test := range []struct {
		name  string
		steps []providerScriptStep
	}{
		{name: "locking query", steps: []providerScriptStep{{query: "locking_mode", err: canary}}},
		{name: "wrong locking", steps: []providerScriptStep{providerRow("locking_mode", "normal")}},
		{name: "journal query", steps: []providerScriptStep{
			providerRow("locking_mode", "exclusive"), {query: "journal_mode", err: canary},
		}},
		{name: "wrong journal", steps: []providerScriptStep{
			providerRow("locking_mode", "exclusive"), providerRow("journal_mode", "wal"),
		}},
		{name: "verification query", steps: []providerScriptStep{
			providerRow("locking_mode", "exclusive"), providerRow("journal_mode", "delete"),
			{query: "pragma_foreign_keys", err: canary},
		}},
		{name: "wrong settings", steps: []providerScriptStep{
			providerRow("locking_mode", "exclusive"), providerRow("journal_mode", "delete"),
			providerRow("pragma_foreign_keys", int64(1), int64(2), int64(1)),
		}},
	} {
		t.Run("offline "+test.name, func(t *testing.T) {
			if err := ConfigureOffline(
				t.Context(), openProviderScript(t, test.steps...), time.Second,
			); err == nil {
				t.Fatal("invalid offline provider configuration succeeded")
			}
		})
	}
	if err := ConfigureOffline(t.Context(), openProviderScript(t,
		providerRow("locking_mode", "exclusive"), providerRow("journal_mode", "delete"),
		providerRow("pragma_foreign_keys", int64(1), int64(1000), int64(2)),
	), time.Second); err != nil {
		t.Fatal(err)
	}
}

func TestCoverageProviderGenerationAndSecurityBoundaries(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "store.db")
	if err := os.Mkdir(path, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := PrepareStore(path); err == nil {
		t.Fatal("PrepareStore accepted directory endpoint")
	}
	if err := secureProviderFile(path); err == nil {
		t.Fatal("file security accepted directory")
	}
	if err := secureProviderDirectory(filepath.Join(root, "missing")); err == nil {
		t.Fatal("directory security accepted missing path")
	}
	target := filepath.Join(root, "target")
	if err := os.WriteFile(target, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "link")
	if err := os.Symlink(target, link); err == nil {
		if err := secureProviderFile(link); err == nil {
			t.Fatal("file security accepted symlink")
		}
	}

	main := filepath.Join(t.TempDir(), "hardlink.db")
	if err := os.WriteFile(main, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	alias := main + ".alias"
	if err := os.Link(main, alias); err == nil {
		if err := SecureGeneration(main); err == nil || !strings.Contains(err.Error(), "hardlink") {
			t.Fatalf("hardlink generation = %v", err)
		}
	}
	sidecarPath := filepath.Join(t.TempDir(), "sidecar.db")
	if err := os.WriteFile(sidecarPath+"-wal", nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := PrepareStore(sidecarPath); err == nil {
		t.Fatal("provider accepted a sidecar without its database")
	}
	if err := SecureGeneration(sidecarPath); err == nil {
		t.Fatal("generation without main database secured")
	}
}

func TestCoverageProviderFilesystemFailurePhases(t *testing.T) {
	if DriverName() != driverName {
		t.Fatal("provider driver name changed")
	}
	if got := FileURLPath("C:/store.db", "C:"); got != "/C:/store.db" {
		t.Fatalf("volume file URL path = %q", got)
	}
	canary := errors.New("provider filesystem canary")
	root := t.TempDir()
	path := filepath.Join(root, "store.db")
	directoryInfo, err := os.Lstat(root)
	if err != nil {
		t.Fatal(err)
	}
	realFile := filepath.Join(root, "identity")
	if err := os.WriteFile(realFile, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	fileInfo, err := os.Lstat(realFile)
	if err != nil {
		t.Fatal(err)
	}
	otherFile := filepath.Join(root, "other")
	if err := os.WriteFile(otherFile, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	otherInfo, err := os.Lstat(otherFile)
	if err != nil {
		t.Fatal(err)
	}

	basePrepare := func() (providerFilesystem, *providerFaultFile) {
		file := &providerFaultFile{info: fileInfo}
		filesystem := systemProviderFilesystem()
		filesystem.validateSyntax = func(string) error { return nil }
		filesystem.validateAncestors = func(string) error { return nil }
		filesystem.mkdirAll = func(string, os.FileMode) error { return nil }
		filesystem.lstat = func(candidate string) (os.FileInfo, error) {
			if candidate == filepath.Dir(path) {
				return directoryInfo, nil
			}
			if candidate == path {
				return fileInfo, nil
			}
			return nil, os.ErrNotExist
		}
		filesystem.openFile = func(string, int, os.FileMode) (providerFile, error) {
			return file, nil
		}
		filesystem.secureDirectory = func(string) error { return nil }
		filesystem.secureFile = func(string) error { return nil }
		filesystem.syncDirectory = func(string) error { return nil }
		filesystem.linkCount = func(string, os.FileInfo) generationLinkClass { return generationLinkSingle }
		filesystem.owner = func(string, os.FileInfo) generationOwnerClass { return generationOwnerCurrent }
		return filesystem, file
	}
	forceCreated := func(filesystem *providerFilesystem) {
		pathCalls := 0
		filesystem.lstat = func(candidate string) (os.FileInfo, error) {
			if candidate == filepath.Dir(path) {
				return directoryInfo, nil
			}
			if candidate == path {
				pathCalls++
				if pathCalls <= 3 {
					return nil, os.ErrNotExist
				}
				return fileInfo, nil
			}
			return nil, os.ErrNotExist
		}
	}
	prepareTests := []struct {
		name   string
		mutate func(*providerFilesystem, *providerFaultFile)
	}{
		{name: "syntax", mutate: func(fs *providerFilesystem, _ *providerFaultFile) {
			fs.validateSyntax = func(string) error { return canary }
		}},
		{name: "ancestor", mutate: func(fs *providerFilesystem, _ *providerFaultFile) {
			fs.validateAncestors = func(string) error { return canary }
		}},
		{name: "mkdir", mutate: func(fs *providerFilesystem, _ *providerFaultFile) {
			fs.mkdirAll = func(string, os.FileMode) error { return canary }
		}},
		{name: "parent stat", mutate: func(fs *providerFilesystem, _ *providerFaultFile) {
			fs.lstat = func(string) (os.FileInfo, error) { return nil, canary }
		}},
		{name: "parent type", mutate: func(fs *providerFilesystem, _ *providerFaultFile) {
			fs.lstat = func(string) (os.FileInfo, error) { return fileInfo, nil }
		}},
		{name: "parent security", mutate: func(fs *providerFilesystem, _ *providerFaultFile) {
			fs.secureDirectory = func(string) error { return canary }
		}},
		{name: "generation", mutate: func(fs *providerFilesystem, _ *providerFaultFile) {
			calls := 0
			fs.lstat = func(string) (os.FileInfo, error) {
				calls++
				if calls == 1 {
					return directoryInfo, nil
				}
				return nil, canary
			}
		}},
		{name: "open", mutate: func(fs *providerFilesystem, _ *providerFaultFile) {
			forceCreated(fs)
			fs.openFile = func(string, int, os.FileMode) (providerFile, error) { return nil, canary }
		}},
		{name: "opened stat", mutate: func(fs *providerFilesystem, file *providerFaultFile) {
			forceCreated(fs)
			file.statErr = canary
		}},
		{name: "identity", mutate: func(fs *providerFilesystem, _ *providerFaultFile) {
			pathCalls := 0
			fs.lstat = func(candidate string) (os.FileInfo, error) {
				if candidate == filepath.Dir(path) {
					return directoryInfo, nil
				}
				if candidate == path {
					pathCalls++
					if pathCalls <= 3 {
						return nil, os.ErrNotExist
					}
					return otherInfo, nil
				}
				return nil, os.ErrNotExist
			}
		}},
		{name: "created chmod", mutate: func(fs *providerFilesystem, file *providerFaultFile) {
			forceCreated(fs)
			file.chmodErr = canary
		}},
		{name: "file security", mutate: func(fs *providerFilesystem, _ *providerFaultFile) {
			forceCreated(fs)
			fs.secureFile = func(string) error { return canary }
		}},
		{name: "file sync", mutate: func(fs *providerFilesystem, file *providerFaultFile) {
			forceCreated(fs)
			file.syncErr = canary
		}},
		{name: "file close", mutate: func(fs *providerFilesystem, file *providerFaultFile) {
			forceCreated(fs)
			file.closeErr = canary
		}},
	}
	for _, test := range prepareTests {
		t.Run("prepare "+test.name, func(t *testing.T) {
			filesystem, file := basePrepare()
			test.mutate(&filesystem, file)
			if err := prepareStore(path, filesystem); err == nil {
				t.Fatal("injected preparation failure succeeded")
			}
		})
	}
	filesystem, _ := basePrepare()
	filesystem.syncDirectory = func(string) error { return canary }
	if err := prepareStore(path, filesystem); !errors.Is(err, canary) {
		t.Fatalf("directory sync error = %v", err)
	}

	baseDirectory := func() providerFilesystem {
		filesystem := systemProviderFilesystem()
		filesystem.validateSyntax = func(string) error { return nil }
		filesystem.validateAncestors = func(string) error { return nil }
		filesystem.mkdirAll = func(string, os.FileMode) error { return nil }
		filesystem.lstat = func(string) (os.FileInfo, error) { return directoryInfo, nil }
		filesystem.secureDirectory = func(string) error { return nil }
		filesystem.syncDirectory = func(string) error { return nil }
		return filesystem
	}
	directoryTests := []func(*providerFilesystem){
		func(fs *providerFilesystem) { fs.validateSyntax = func(string) error { return canary } },
		func(fs *providerFilesystem) { fs.validateAncestors = func(string) error { return canary } },
		func(fs *providerFilesystem) { fs.mkdirAll = func(string, os.FileMode) error { return canary } },
		func(fs *providerFilesystem) { fs.lstat = func(string) (os.FileInfo, error) { return nil, canary } },
		func(fs *providerFilesystem) { fs.lstat = func(string) (os.FileInfo, error) { return fileInfo, nil } },
		func(fs *providerFilesystem) { fs.secureDirectory = func(string) error { return canary } },
		func(fs *providerFilesystem) { fs.syncDirectory = func(string) error { return canary } },
	}
	for index, mutate := range directoryTests {
		filesystem := baseDirectory()
		mutate(&filesystem)
		if err := ensurePrivateDirectory(root, filesystem); err == nil {
			t.Fatalf("directory failure %d succeeded", index)
		}
	}

	filesystem = systemProviderFilesystem()
	filesystem.validateSyntax = func(string) error { return canary }
	if err := secureGeneration(path, filesystem); !errors.Is(err, canary) {
		t.Fatalf("secure syntax error = %v", err)
	}
	filesystem = systemProviderFilesystem()
	filesystem.validateAncestors = func(string) error { return canary }
	if err := secureGeneration(path, filesystem); !errors.Is(err, canary) {
		t.Fatalf("secure ancestor error = %v", err)
	}
	filesystem = systemProviderFilesystem()
	filesystem.validateSyntax = func(string) error { return nil }
	filesystem.validateAncestors = func(string) error { return nil }
	filesystem.lstat = func(candidate string) (os.FileInfo, error) {
		if candidate == path {
			return fileInfo, nil
		}
		return nil, os.ErrNotExist
	}
	filesystem.secureDirectory = nil
	if err := secureGeneration(path, filesystem); err == nil {
		t.Fatal("missing generation parent security succeeded")
	}
	filesystem.secureDirectory = func(string) error { return canary }
	if err := secureGeneration(path, filesystem); !errors.Is(err, canary) {
		t.Fatalf("secure generation parent error = %v", err)
	}
	for _, invalid := range []string{"", " ", ":memory:", "file:store.db"} {
		secureCalls := 0
		filesystem = systemProviderFilesystem()
		filesystem.secureDirectory = func(string) error {
			secureCalls++
			return nil
		}
		if err := secureGeneration(invalid, filesystem); err == nil {
			t.Fatalf("invalid generation path %q succeeded", invalid)
		}
		if secureCalls != 0 {
			t.Fatalf("invalid generation path %q secured its parent", invalid)
		}
	}
	for name, lstat := range map[string]func(string) (os.FileInfo, error){
		"missing":    func(string) (os.FileInfo, error) { return nil, os.ErrNotExist },
		"nonregular": func(string) (os.FileInfo, error) { return directoryInfo, nil },
	} {
		secureCalls := 0
		filesystem = systemProviderFilesystem()
		filesystem.validateSyntax = func(string) error { return nil }
		filesystem.validateAncestors = func(string) error { return nil }
		filesystem.lstat = lstat
		filesystem.secureDirectory = func(string) error {
			secureCalls++
			return nil
		}
		if err := secureGeneration("store.db", filesystem); err == nil {
			t.Fatalf("%s generation preflight succeeded", name)
		}
		if secureCalls != 0 {
			t.Fatalf("%s generation preflight secured its parent", name)
		}
	}
	secureCalls := 0
	filesystem = systemProviderFilesystem()
	filesystem.validateSyntax = func(string) error { return nil }
	filesystem.validateAncestors = func(string) error { return nil }
	filesystem.lstat = func(string) (os.FileInfo, error) { return nil, canary }
	filesystem.secureDirectory = func(string) error {
		secureCalls++
		return nil
	}
	if err := secureGeneration("store.db", filesystem); !errors.Is(err, canary) ||
		errors.Is(err, errProviderUnsafeBoundary) {
		t.Fatalf("generation preflight I/O error = %v", err)
	}
	if secureCalls != 0 {
		t.Fatal("generation preflight I/O error secured its parent")
	}
	for name, after := range map[string]func(string) (os.FileInfo, error){
		"disappeared": func(string) (os.FileInfo, error) { return nil, os.ErrNotExist },
		"replaced":    func(string) (os.FileInfo, error) { return otherInfo, nil },
	} {
		parentSecured := false
		filesystem = systemProviderFilesystem()
		filesystem.validateSyntax = func(string) error { return nil }
		filesystem.validateAncestors = func(string) error { return nil }
		filesystem.lstat = func(candidate string) (os.FileInfo, error) {
			if parentSecured {
				return after(candidate)
			}
			return fileInfo, nil
		}
		filesystem.secureDirectory = func(string) error {
			parentSecured = true
			return nil
		}
		if err := secureGeneration("store.db", filesystem); err == nil ||
			!errors.Is(err, errProviderUnsafeBoundary) {
			t.Fatalf("main %s during parent security error = %v", name, err)
		}
	}
}

func TestCoverageGenerationMemberFailurePhases(t *testing.T) {
	root := t.TempDir()
	mainPath := filepath.Join(root, "store.db")
	if err := os.WriteFile(mainPath, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	mainInfo, err := os.Lstat(mainPath)
	if err != nil {
		t.Fatal(err)
	}
	directoryInfo, err := os.Lstat(root)
	if err != nil {
		t.Fatal(err)
	}
	canary := errors.New("generation canary")
	base := func() providerFilesystem {
		filesystem := systemProviderFilesystem()
		filesystem.lstat = func(candidate string) (os.FileInfo, error) {
			if candidate == mainPath {
				return mainInfo, nil
			}
			return nil, os.ErrNotExist
		}
		filesystem.secureFile = func(string) error { return nil }
		filesystem.linkCount = func(string, os.FileInfo) generationLinkClass { return generationLinkSingle }
		filesystem.owner = func(string, os.FileInfo) generationOwnerClass { return generationOwnerCurrent }
		return filesystem
	}
	tests := []struct {
		name    string
		require bool
		mutate  func(*providerFilesystem)
	}{
		{name: "required missing", require: true, mutate: func(fs *providerFilesystem) {
			fs.lstat = func(string) (os.FileInfo, error) { return nil, os.ErrNotExist }
		}},
		{name: "stat", mutate: func(fs *providerFilesystem) {
			fs.lstat = func(string) (os.FileInfo, error) { return nil, canary }
		}},
		{name: "type", mutate: func(fs *providerFilesystem) {
			fs.lstat = func(string) (os.FileInfo, error) { return directoryInfo, nil }
		}},
		{name: "link", mutate: func(fs *providerFilesystem) {
			fs.linkCount = func(string, os.FileInfo) generationLinkClass { return generationLinkMultiple }
		}},
		{name: "owner", mutate: func(fs *providerFilesystem) {
			fs.owner = func(string, os.FileInfo) generationOwnerClass { return generationOwnerForeign }
		}},
		{name: "security", mutate: func(fs *providerFilesystem) {
			fs.secureFile = func(string) error { return canary }
		}},
		{name: "changed", mutate: func(fs *providerFilesystem) {
			calls := 0
			fs.lstat = func(candidate string) (os.FileInfo, error) {
				if candidate != mainPath {
					return nil, os.ErrNotExist
				}
				calls++
				if calls == 1 {
					return mainInfo, nil
				}
				return directoryInfo, nil
			}
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			filesystem := base()
			test.mutate(&filesystem)
			if err := validateGenerationMembersWithFilesystem(
				mainPath, test.require, filesystem,
			); err == nil {
				t.Fatal("generation phase succeeded")
			}
		})
	}
	t.Run("optional security disappeared", func(t *testing.T) {
		filesystem := base()
		calls := 0
		filesystem.lstat = func(candidate string) (os.FileInfo, error) {
			if candidate == mainPath {
				return mainInfo, nil
			}
			if candidate == mainPath+"-wal" {
				calls++
				if calls == 1 {
					return mainInfo, nil
				}
				return nil, os.ErrNotExist
			}
			return nil, os.ErrNotExist
		}
		filesystem.secureFile = func(candidate string) error {
			if candidate == mainPath+"-wal" {
				return os.ErrNotExist
			}
			return nil
		}
		if err := validateGenerationMembersWithFilesystem(
			mainPath, true, filesystem,
		); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("optional changed disappeared", func(t *testing.T) {
		filesystem := base()
		calls := 0
		filesystem.lstat = func(candidate string) (os.FileInfo, error) {
			if candidate == mainPath {
				return mainInfo, nil
			}
			if candidate == mainPath+"-wal" {
				calls++
				if calls == 1 {
					return mainInfo, nil
				}
				return nil, os.ErrNotExist
			}
			return nil, os.ErrNotExist
		}
		if err := validateGenerationMembersWithFilesystem(
			mainPath, true, filesystem,
		); err != nil {
			t.Fatal(err)
		}
	})
}

func TestCoverageProviderSchemaQueryFailure(t *testing.T) {
	canary := errors.New("schema query canary")
	if err := ValidateUniqueIndexes(t.Context(), openProviderScript(t,
		providerRow("sqlite_schema", int64(1)),
		providerScriptStep{query: "pragma_index_list", err: canary}), "items", "items_key"); !errors.Is(err, canary) {
		t.Fatalf("required index query error = %v", err)
	}
	if err := ValidateUniqueIndexes(t.Context(), openProviderScript(t,
		providerRow("sqlite_schema", int64(1)),
		providerRow("pragma_index_list", int64(1)),
		providerScriptStep{query: "pragma_index_list", err: canary},
	), "items", "items_key"); !errors.Is(err, canary) {
		t.Fatalf("unexpected index query error = %v", err)
	}
}

func TestCoverageProviderWALRetryDeadline(t *testing.T) {
	path := filepath.Join(t.TempDir(), "locked.db")
	owner, err := OpenStore(path, 250*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	owner.SetMaxOpenConns(1)
	owner.SetMaxIdleConns(1)
	if err := ConfigureOffline(t.Context(), owner, 250*time.Millisecond); err != nil {
		t.Fatal(err)
	}
	connection, err := owner.Conn(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := connection.ExecContext(t.Context(), "BEGIN EXCLUSIVE"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = connection.ExecContext(context.Background(), "ROLLBACK")
		_ = connection.Close()
		_ = owner.Close()
	})
	contender, err := OpenStore(path, 5*time.Millisecond)
	if err == nil || contender != nil {
		if contender != nil {
			_ = contender.Close()
		}
		t.Fatalf("provider opened through exclusive lock: %#v, %v", contender, err)
	}
}
