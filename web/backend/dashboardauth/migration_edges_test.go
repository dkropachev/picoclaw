package dashboardauth

import (
	"crypto/sha256"
	"database/sql"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/sipeed/picoclaw/web/backend/launcherconfig"
)

func resetLegacyMigrationHooks(t *testing.T) {
	t.Helper()
	lstat := legacyConfigLstat
	open := legacyConfigOpen
	readAll := legacyConfigReadAll
	mkdir := legacyArchiveMkdir
	chmod := legacyArchiveChmod
	syncDir := legacyArchiveSync
	link := legacyArchiveLink
	remove := legacyArchiveRemove
	writeAtomic := legacyConfigWriteAtomic
	t.Cleanup(func() {
		legacyConfigLstat = lstat
		legacyConfigOpen = open
		legacyConfigReadAll = readAll
		legacyArchiveMkdir = mkdir
		legacyArchiveChmod = chmod
		legacyArchiveSync = syncDir
		legacyArchiveLink = link
		legacyArchiveRemove = remove
		legacyConfigWriteAtomic = writeAtomic
	})
}

func TestChangedLauncherConfigRecoveryFailsClosed(t *testing.T) {
	tests := []struct {
		name         string
		changed      []byte
		archive      bool
		remove       bool
		complete     bool
		wantErr      string
		wantComplete bool
	}{
		{
			name:    "malformed",
			changed: []byte(`{"launcher_token":`),
			archive: true,
			wantErr: "decode changed launcher config",
		},
		{
			name:    "changed auth",
			changed: []byte(`{"port":18800,"launcher_token":"changed"}`),
			archive: true,
			wantErr: "changed after import",
		},
		{name: "clean without archive", changed: []byte(`{"port":18801}`), wantErr: "archive is missing"},
		{name: "clean recovery", changed: []byte(`{"port":18801}`), archive: true, wantComplete: true},
		{name: "missing pending", remove: true, wantErr: "missing before archival"},
		{name: "missing completed", remove: true, archive: true, complete: true, wantComplete: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir, dbPath, configPath, archivePath, original := pendingLauncherMigration(t)
			_ = dir
			if tt.archive {
				if err := os.WriteFile(archivePath, original, 0o600); err != nil {
					t.Fatal(err)
				}
			}
			if tt.complete {
				raw := openLauncherTestDB(t, dbPath)
				if _, err := raw.Exec(`UPDATE launcher_auth_legacy_imports
                        SET archive_status = 'complete', archived_at = '2026-01-01T00:00:00Z'`); err != nil {
					t.Fatal(err)
				}
				raw.Close()
			}
			switch {
			case tt.remove:
				if err := os.Remove(configPath); err != nil {
					t.Fatal(err)
				}
			case tt.changed != nil:
				if err := os.WriteFile(configPath, tt.changed, 0o600); err != nil {
					t.Fatal(err)
				}
			}
			store, err := OpenWithLauncherConfig(dbPath, configPath)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					if store != nil {
						store.Close()
					}
					t.Fatalf("OpenWithLauncherConfig() error = %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			var status string
			if err := store.db.QueryRow(`SELECT archive_status FROM launcher_auth_legacy_imports`).
				Scan(&status); err != nil {
				t.Fatal(err)
			}
			if tt.wantComplete && status != "complete" {
				t.Fatalf("archive status = %q", status)
			}
		})
	}
}

func pendingLauncherMigration(t *testing.T) (dir, dbPath, configPath, archivePath string, original []byte) {
	t.Helper()
	dir = t.TempDir()
	dbPath = filepath.Join(dir, DBFilename)
	configPath = filepath.Join(dir, launcherconfig.FileName)
	original = []byte(`{"port":18800,"dashboard_password_hash":"` + testHash(t, "legacy") + `"}` + "\n")
	if err := os.WriteFile(configPath, original, 0o600); err != nil {
		t.Fatal(err)
	}
	archivePath = filepath.Join(dir, "legacy-json", legacyArchiveVersion, launcherconfig.FileName)
	if err := os.MkdirAll(filepath.Dir(archivePath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(archivePath, []byte("collision"), 0o600); err != nil {
		t.Fatal(err)
	}
	if store, err := OpenWithLauncherConfig(dbPath, configPath); err == nil {
		store.Close()
		t.Fatal("collision fixture unexpectedly opened")
	}
	if err := os.Remove(archivePath); err != nil {
		t.Fatal(err)
	}
	return dir, dbPath, configPath, archivePath, original
}

func openLauncherTestDB(t *testing.T, path string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	return db
}

type legacyFileWrapper struct {
	legacyConfigFile
	statErr  error
	closeErr error
	statInfo os.FileInfo
}

func (f *legacyFileWrapper) Stat() (os.FileInfo, error) {
	if f.statErr != nil {
		return nil, f.statErr
	}
	if f.statInfo != nil {
		return f.statInfo, nil
	}
	return f.legacyConfigFile.Stat()
}

func (f *legacyFileWrapper) Close() error {
	if f.closeErr != nil {
		_ = f.legacyConfigFile.Close()
		return f.closeErr
	}
	return f.legacyConfigFile.Close()
}

func TestReadLegacyConfigFailureBoundaries(t *testing.T) {
	t.Run("missing parent", func(t *testing.T) {
		if _, _, err := readLegacyConfig(filepath.Join(t.TempDir(), "missing", launcherconfig.FileName)); err == nil {
			t.Fatal("missing parent succeeded")
		}
	})
	t.Run("missing source", func(t *testing.T) {
		dir := privateDashboardTestDir(t)
		if _, found, err := readLegacyConfig(filepath.Join(dir, launcherconfig.FileName)); err != nil || found {
			t.Fatalf("missing source = %t, %v", found, err)
		}
	})
	t.Run("unsafe and oversized", func(t *testing.T) {
		dir := privateDashboardTestDir(t)
		path := filepath.Join(dir, launcherconfig.FileName)
		if err := os.Mkdir(path, 0o700); err != nil {
			t.Fatal(err)
		}
		if _, _, err := readLegacyConfig(path); err == nil {
			t.Fatal("directory source succeeded")
		}
		if err := os.Remove(path); err != nil {
			t.Fatal(err)
		}
		file, err := os.Create(path)
		if err != nil {
			t.Fatal(err)
		}
		if err := file.Truncate(legacyConfigMaxBytes + 1); err != nil {
			t.Fatal(err)
		}
		file.Close()
		if _, _, err := readLegacyConfig(path); err == nil {
			t.Fatal("oversized source succeeded")
		}
	})
	for _, failure := range []string{"open", "stat", "read", "read-too-large", "changed"} {
		t.Run(failure, func(t *testing.T) {
			resetLegacyMigrationHooks(t)
			dir := privateDashboardTestDir(t)
			path := filepath.Join(dir, launcherconfig.FileName)
			if err := os.WriteFile(path, []byte(`{"port":18800}`), 0o600); err != nil {
				t.Fatal(err)
			}
			switch failure {
			case "open":
				legacyConfigOpen = func(string) (legacyConfigFile, error) { return nil, errors.New("open") }
			case "stat":
				original := legacyConfigOpen
				legacyConfigOpen = func(path string) (legacyConfigFile, error) {
					file, err := original(path)
					return &legacyFileWrapper{legacyConfigFile: file, statErr: errors.New("stat")}, err
				}
			case "read":
				legacyConfigReadAll = func(io.Reader) ([]byte, error) { return nil, errors.New("read") }
			case "read-too-large":
				legacyConfigReadAll = func(io.Reader) ([]byte, error) {
					return make([]byte, legacyConfigMaxBytes+1), nil
				}
			case "changed":
				original := legacyConfigLstat
				calls := 0
				other := filepath.Join(dir, "other")
				if err := os.WriteFile(other, []byte("other"), 0o600); err != nil {
					t.Fatal(err)
				}
				legacyConfigLstat = func(candidate string) (os.FileInfo, error) {
					if candidate == path {
						calls++
						if calls > 1 {
							return original(other)
						}
					}
					return original(candidate)
				}
			}
			if _, _, err := readLegacyConfig(path); err == nil {
				t.Fatalf("%s failure succeeded", failure)
			}
		})
	}
}

func privateDashboardTestDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestUnsafeLauncherConfigParentFailsClosed(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX directory permission semantics")
	}
	configDir := t.TempDir()
	if err := os.Chmod(configDir, 0o770); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(configDir, 0o700) })
	configPath := filepath.Join(configDir, launcherconfig.FileName)
	if err := os.WriteFile(configPath, []byte(`{"port":18800,"launcher_token":"secret"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if store, err := OpenWithLauncherConfig(filepath.Join(t.TempDir(), DBFilename), configPath); err == nil {
		store.Close()
		t.Fatal("unsafe launcher-config parent succeeded")
	} else if !strings.Contains(err.Error(), "safe real directory") {
		t.Fatalf("OpenWithLauncherConfig() error = %v", err)
	}
}

func TestLegacyAuthFieldParsing(t *testing.T) {
	for _, test := range []struct {
		data    string
		present bool
		wantErr bool
	}{
		{data: `{"port":18800}`},
		{data: `{"launcher_token":null}`, present: true},
		{data: `{"dashboard_password_hash":"hash"}`, present: true},
		{data: `{"launcher_token":`, wantErr: true},
	} {
		present, err := hasLegacyAuthFields([]byte(test.data))
		if present != test.present || (err != nil) != test.wantErr {
			t.Fatalf("hasLegacyAuthFields(%q) = %t, %v", test.data, present, err)
		}
	}
}

func TestArchiveDirectoryFailureBoundaries(t *testing.T) {
	for _, failure := range []string{
		"invalid-name", "missing-parent", "mkdir", "parent-sync", "post-mkdir-lstat",
		"file-child", "chmod", "child-sync", "changed-parent",
	} {
		t.Run(failure, func(t *testing.T) {
			resetLegacyMigrationHooks(t)
			parent := privateDashboardTestDir(t)
			name := "child"
			switch failure {
			case "invalid-name":
				name = "bad/name"
			case "missing-parent":
				parent = filepath.Join(parent, "missing")
			case "mkdir":
				legacyArchiveMkdir = func(string, os.FileMode) error { return errors.New("mkdir") }
			case "parent-sync":
				legacyArchiveSync = func(path string) error {
					if path == parent {
						return errors.New("sync")
					}
					return nil
				}
			case "post-mkdir-lstat":
				original := legacyConfigLstat
				child := filepath.Join(parent, name)
				calls := 0
				legacyConfigLstat = func(path string) (os.FileInfo, error) {
					if path == child {
						calls++
						if calls > 1 {
							return nil, errors.New("lstat")
						}
					}
					return original(path)
				}
			case "file-child":
				if err := os.WriteFile(filepath.Join(parent, name), []byte("x"), 0o600); err != nil {
					t.Fatal(err)
				}
			case "chmod":
				legacyArchiveChmod = func(string, os.FileMode) error { return errors.New("chmod") }
			case "child-sync":
				legacyArchiveSync = func(path string) error {
					if path == filepath.Join(parent, name) {
						return errors.New("sync")
					}
					return nil
				}
			case "changed-parent":
				original := legacyConfigLstat
				other := privateDashboardTestDir(t)
				calls := 0
				legacyConfigLstat = func(path string) (os.FileInfo, error) {
					if path == parent {
						calls++
						if calls > 1 {
							return original(other)
						}
					}
					return original(path)
				}
			}
			if _, err := ensurePrivateChildDirectory(parent, name); err == nil {
				t.Fatalf("%s failure succeeded", failure)
			}
		})
	}
}

func TestArchivePublicationFailureBoundaries(t *testing.T) {
	for _, failure := range []string{
		"existing-sync", "dir-after-inspect", "source-missing", "source-changed",
		"dir-changed", "link", "exist-correct", "exist-conflict", "wrong-inode",
		"wrong-inode-cleanup", "sync", "post-sync-dir-change", "verify-cleanup",
	} {
		t.Run(failure, func(t *testing.T) {
			resetLegacyMigrationHooks(t)
			dir := privateDashboardTestDir(t)
			sourcePath := filepath.Join(dir, launcherconfig.FileName)
			body := []byte(`{"port":18800,"launcher_token":"secret"}`)
			if err := os.WriteFile(sourcePath, body, 0o600); err != nil {
				t.Fatal(err)
			}
			archiveDir := filepath.Join(dir, "archive")
			if err := os.Mkdir(archiveDir, 0o700); err != nil {
				t.Fatal(err)
			}
			snapshot, found, readErr := readLegacyConfig(sourcePath)
			if readErr != nil || !found {
				t.Fatal(readErr)
			}
			archivePath := filepath.Join(archiveDir, launcherconfig.FileName)
			switch failure {
			case "existing-sync":
				if err := os.Link(sourcePath, archivePath); err != nil {
					t.Fatal(err)
				}
				legacyArchiveSync = func(string) error { return errors.New("sync") }
			case "dir-after-inspect":
				original := legacyConfigLstat
				calls := 0
				legacyConfigLstat = func(path string) (os.FileInfo, error) {
					if path == archiveDir {
						calls++
						if calls > 1 {
							return nil, errors.New("dir")
						}
					}
					return original(path)
				}
			case "source-missing":
				if err := os.Remove(sourcePath); err != nil {
					t.Fatal(err)
				}
			case "source-changed":
				if err := os.WriteFile(sourcePath, []byte(`{"port":18801}`), 0o600); err != nil {
					t.Fatal(err)
				}
			case "dir-changed":
				original := legacyConfigLstat
				other := privateDashboardTestDir(t)
				calls := 0
				legacyConfigLstat = func(path string) (os.FileInfo, error) {
					if path == archiveDir {
						calls++
						if calls >= 3 {
							return original(other)
						}
					}
					return original(path)
				}
			case "link":
				legacyArchiveLink = func(string, string) error { return errors.New("link") }
			case "exist-correct":
				legacyArchiveLink = func(source, target string) error {
					if err := os.Link(source, target); err != nil {
						return err
					}
					return fs.ErrExist
				}
			case "exist-conflict":
				legacyArchiveLink = func(_, target string) error {
					if err := os.WriteFile(target, []byte("conflict"), 0o600); err != nil {
						return err
					}
					return fs.ErrExist
				}
			case "wrong-inode", "wrong-inode-cleanup":
				legacyArchiveLink = func(_, target string) error {
					return os.WriteFile(target, body, 0o600)
				}
				if failure == "wrong-inode-cleanup" {
					legacyArchiveRemove = func(string) error { return errors.New("cleanup") }
				}
			case "sync":
				legacyArchiveSync = func(string) error { return errors.New("sync") }
			case "post-sync-dir-change":
				legacyArchiveSync = func(path string) error {
					old := path + "-old"
					if err := os.Rename(path, old); err != nil {
						return err
					}
					return os.Mkdir(path, 0o700)
				}
			case "verify-cleanup":
				legacyArchiveSync = func(string) error {
					if err := os.Remove(archivePath); err != nil {
						return err
					}
					return os.WriteFile(archivePath, []byte("changed"), 0o600)
				}
			}
			publishErr := publishArchiveCopy(sourcePath, archiveDir, launcherconfig.FileName, snapshot)
			if failure == "exist-correct" {
				if publishErr != nil {
					t.Fatal(publishErr)
				}
				return
			}
			if publishErr == nil {
				t.Fatalf("%s failure succeeded", failure)
			}
		})
	}
}

func TestStripLegacyAuthFailureAndRaceBoundaries(t *testing.T) {
	for _, failure := range []string{
		"missing", "read", "changed-clean", "changed-auth", "changed-malformed",
		"matching-malformed", "matching-clean", "second-read", "second-clean",
		"second-auth", "second-malformed", "parent", "write", "post-write-parent",
		"verify-read", "verify-missing", "verify-auth", "verify-mode", "verify-malformed",
	} {
		t.Run(failure, func(t *testing.T) {
			resetLegacyMigrationHooks(t)
			dir := privateDashboardTestDir(t)
			path := filepath.Join(dir, launcherconfig.FileName)
			original := []byte(`{"port":18800,"launcher_token":"secret"}`)
			if err := os.WriteFile(path, original, 0o600); err != nil {
				t.Fatal(err)
			}
			expected := sha256.Sum256(original)
			realReadAll := legacyConfigReadAll
			realWrite := legacyConfigWriteAtomic
			switch failure {
			case "missing":
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
			case "read":
				legacyConfigReadAll = func(io.Reader) ([]byte, error) { return nil, errors.New("read") }
			case "changed-clean":
				if err := os.WriteFile(path, []byte(`{"port":18801}`), 0o600); err != nil {
					t.Fatal(err)
				}
			case "changed-auth":
				if err := os.WriteFile(path, []byte(`{"port":18801,"launcher_token":"other"}`), 0o600); err != nil {
					t.Fatal(err)
				}
			case "changed-malformed":
				if err := os.WriteFile(path, []byte(`{"launcher_token":`), 0o600); err != nil {
					t.Fatal(err)
				}
			case "matching-malformed":
				malformed := []byte(`{"launcher_token":`)
				if err := os.WriteFile(path, malformed, 0o600); err != nil {
					t.Fatal(err)
				}
				expected = sha256.Sum256(malformed)
			case "matching-clean":
				clean := []byte(`{"port":18800}`)
				if err := os.WriteFile(path, clean, 0o600); err != nil {
					t.Fatal(err)
				}
				expected = sha256.Sum256(clean)
			case "second-read":
				calls := 0
				legacyConfigReadAll = func(reader io.Reader) ([]byte, error) {
					calls++
					if calls == 2 {
						return nil, errors.New("read")
					}
					return realReadAll(reader)
				}
			case "second-clean", "second-auth", "second-malformed":
				calls := 0
				legacyConfigReadAll = func(reader io.Reader) ([]byte, error) {
					calls++
					if calls == 2 {
						switch failure {
						case "second-clean":
							return []byte(`{"port":18801}`), nil
						case "second-auth":
							return []byte(`{"launcher_token":"other"}`), nil
						default:
							return []byte(`{"launcher_token":`), nil
						}
					}
					return realReadAll(reader)
				}
			case "parent", "post-write-parent":
				originalLstat := legacyConfigLstat
				other := privateDashboardTestDir(t)
				calls := 0
				legacyConfigLstat = func(candidate string) (os.FileInfo, error) {
					if candidate == dir {
						calls++
						if failure == "parent" && calls == 5 {
							return nil, errors.New("parent")
						}
						if failure == "post-write-parent" && calls == 6 {
							return originalLstat(other)
						}
					}
					return originalLstat(candidate)
				}
			case "write":
				legacyConfigWriteAtomic = func(string, []byte, os.FileMode) error { return errors.New("write") }
			case "verify-read":
				calls := 0
				legacyConfigReadAll = func(reader io.Reader) ([]byte, error) {
					calls++
					if calls == 3 {
						return nil, errors.New("read")
					}
					return realReadAll(reader)
				}
			case "verify-missing":
				legacyConfigWriteAtomic = func(path string, data []byte, mode os.FileMode) error {
					if err := realWrite(path, data, mode); err != nil {
						return err
					}
					return os.Remove(path)
				}
			case "verify-auth":
				legacyConfigWriteAtomic = func(string, []byte, os.FileMode) error { return nil }
			case "verify-mode":
				legacyConfigWriteAtomic = func(path string, data []byte, mode os.FileMode) error {
					if err := realWrite(path, data, mode); err != nil {
						return err
					}
					return os.Chmod(path, 0o640)
				}
			case "verify-malformed":
				legacyConfigWriteAtomic = func(path string, _ []byte, mode os.FileMode) error {
					return realWrite(path, []byte(`{"launcher_token":`), mode)
				}
			}
			err := stripLegacyAuthFields(path, expected, 0o600)
			if failure == "changed-clean" || failure == "matching-clean" || failure == "second-clean" {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			if err == nil {
				t.Fatalf("%s failure succeeded", failure)
			}
		})
	}
}

func TestArchiveCopyInspectionFailureBoundaries(t *testing.T) {
	for _, failure := range []string{
		"parent", "lstat", "unsafe", "open", "read", "stat", "close", "changed", "digest", "parent-changed",
	} {
		t.Run(failure, func(t *testing.T) {
			resetLegacyMigrationHooks(t)
			dir := privateDashboardTestDir(t)
			path := filepath.Join(dir, "archive.json")
			body := []byte("archive")
			if err := os.WriteFile(path, body, 0o600); err != nil {
				t.Fatal(err)
			}
			digest := sha256.Sum256(body)
			size := int64(len(body))
			mode := os.FileMode(0o600)
			switch failure {
			case "parent":
				path = filepath.Join(dir, "missing", "archive.json")
			case "lstat":
				original := legacyConfigLstat
				legacyConfigLstat = func(candidate string) (os.FileInfo, error) {
					if candidate == path {
						return nil, errors.New("lstat")
					}
					return original(candidate)
				}
			case "unsafe":
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
				if err := os.Mkdir(path, 0o700); err != nil {
					t.Fatal(err)
				}
			case "open":
				legacyConfigOpen = func(string) (legacyConfigFile, error) { return nil, errors.New("open") }
			case "read":
				legacyConfigReadAll = func(io.Reader) ([]byte, error) { return nil, errors.New("read") }
			case "stat":
				original := legacyConfigOpen
				legacyConfigOpen = func(path string) (legacyConfigFile, error) {
					file, err := original(path)
					return &legacyFileWrapper{legacyConfigFile: file, statErr: errors.New("stat")}, err
				}
			case "close":
				original := legacyConfigOpen
				legacyConfigOpen = func(path string) (legacyConfigFile, error) {
					file, err := original(path)
					return &legacyFileWrapper{legacyConfigFile: file, closeErr: errors.New("close")}, err
				}
			case "changed":
				original := legacyConfigLstat
				other := filepath.Join(dir, "other")
				if err := os.WriteFile(other, body, 0o600); err != nil {
					t.Fatal(err)
				}
				calls := 0
				legacyConfigLstat = func(candidate string) (os.FileInfo, error) {
					if candidate == path {
						calls++
						if calls > 1 {
							return original(other)
						}
					}
					return original(candidate)
				}
			case "digest":
				digest = sha256.Sum256([]byte("different"))
			case "parent-changed":
				original := legacyConfigLstat
				other := privateDashboardTestDir(t)
				calls := 0
				legacyConfigLstat = func(candidate string) (os.FileInfo, error) {
					if candidate == dir {
						calls++
						if calls > 1 {
							return original(other)
						}
					}
					return original(candidate)
				}
			}
			if found, err := archiveCopyMatches(path, digest, size, mode); err == nil || found {
				t.Fatalf("%s inspection = %t, %v", failure, found, err)
			}
		})
	}
}

func TestLegacyImportValueFailures(t *testing.T) {
	for _, test := range []struct {
		name    string
		body    string
		wantErr string
	}{
		{name: "hash type", body: `{"port":18800,"dashboard_password_hash":42}`, wantErr: "hash is not a string"},
		{name: "token type", body: `{"port":18800,"launcher_token":42}`, wantErr: "token is not a string"},
		{name: "invalid hash oversized fallback", body: `{"port":18800,"dashboard_password_hash":"invalid","launcher_token":"` + strings.Repeat("x", 73) + `"}`, wantErr: "password length"},
		{name: "oversized token", body: `{"port":18800,"launcher_token":"` + strings.Repeat("x", 73) + `"}`, wantErr: "password length"},
		{name: "empty values", body: `{"port":18800,"dashboard_password_hash":null,"launcher_token":""}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			dir := privateDashboardTestDir(t)
			configPath := filepath.Join(dir, launcherconfig.FileName)
			if err := os.WriteFile(configPath, []byte(test.body), 0o600); err != nil {
				t.Fatal(err)
			}
			store, err := OpenWithLauncherConfig(filepath.Join(dir, DBFilename), configPath)
			if test.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), test.wantErr) {
					if store != nil {
						store.Close()
					}
					t.Fatalf("OpenWithLauncherConfig() error = %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			initialized, err := store.IsInitialized(t.Context())
			if err != nil || initialized {
				t.Fatalf("empty legacy state = %t, %v", initialized, err)
			}
		})
	}
}

func TestUnversionedCredentialMigrationFailures(t *testing.T) {
	for _, failure := range []string{"ddl", "index", "rename", "copy"} {
		t.Run(failure, func(t *testing.T) {
			dir := privateDashboardTestDir(t)
			dbPath := filepath.Join(dir, DBFilename)
			db := openLauncherTestDB(t, dbPath)
			if failure == "ddl" {
				if _, err := db.Exec(`CREATE TABLE dashboard_credentials (
                        id INTEGER PRIMARY KEY, bcrypt_hash TEXT
                    )`); err != nil {
					t.Fatal(err)
				}
			} else {
				if _, err := db.Exec(sqlLegacyCreateCredentials); err != nil {
					t.Fatal(err)
				}
			}
			switch failure {
			case "index":
				if _, err := db.Exec(
					`CREATE INDEX rogue_credentials_idx ON dashboard_credentials(bcrypt_hash)`,
				); err != nil {
					t.Fatal(err)
				}
			case "rename":
				if _, err := db.Exec(`CREATE TABLE dashboard_credentials_unversioned(id INTEGER)`); err != nil {
					t.Fatal(err)
				}
			case "copy":
				if _, err := db.Exec(`PRAGMA ignore_check_constraints = ON`); err != nil {
					t.Fatal(err)
				}
				if _, err := db.Exec(
					`INSERT INTO dashboard_credentials(id, bcrypt_hash) VALUES (2, 'hash')`,
				); err != nil {
					t.Fatal(err)
				}
				if _, err := db.Exec(`PRAGMA ignore_check_constraints = OFF`); err != nil {
					t.Fatal(err)
				}
			}
			if err := db.Close(); err != nil {
				t.Fatal(err)
			}
			if store, err := Open(dbPath); err == nil {
				store.Close()
				t.Fatalf("%s migration succeeded", failure)
			}
		})
	}
}

func TestArchiveLedgerCompletionFailures(t *testing.T) {
	for _, failure := range []string{"changed", "complete", "invalid-status", "update", "ignored-update"} {
		t.Run(failure, func(t *testing.T) {
			_, dbPath, _, _, _ := pendingLauncherMigration(t)
			db := openLauncherTestDB(t, dbPath)
			record, found, readErr := readLegacyImportRecord(t.Context(), db)
			if readErr != nil || !found {
				t.Fatalf("read ledger = %t, %v", found, readErr)
			}
			switch failure {
			case "changed":
				record.size++
			case "complete":
				if _, err := db.Exec(`UPDATE launcher_auth_legacy_imports
                        SET archive_status = 'complete', archived_at = '2026-01-01T00:00:00Z'`); err != nil {
					t.Fatal(err)
				}
			case "invalid-status":
				if _, err := db.Exec(`PRAGMA ignore_check_constraints = ON`); err != nil {
					t.Fatal(err)
				}
				if _, err := db.Exec(`UPDATE launcher_auth_legacy_imports SET archive_status = 'invalid'`); err != nil {
					t.Fatal(err)
				}
			case "update":
				if _, err := db.Exec(`CREATE TRIGGER reject_launcher_archive_update
                        BEFORE UPDATE ON launcher_auth_legacy_imports
                        BEGIN SELECT RAISE(ABORT, 'reject'); END`); err != nil {
					t.Fatal(err)
				}
			case "ignored-update":
				if _, err := db.Exec(`CREATE TRIGGER ignore_launcher_archive_update
                        BEFORE UPDATE ON launcher_auth_legacy_imports
                        BEGIN SELECT RAISE(IGNORE); END`); err != nil {
					t.Fatal(err)
				}
			}
			completeErr := markArchiveComplete(t.Context(), db, record)
			if failure == "complete" {
				if completeErr != nil {
					t.Fatal(completeErr)
				}
			} else if completeErr == nil {
				t.Fatalf("%s ledger completion succeeded", failure)
			}
			if err := db.Close(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestLegacyImportDatabaseFailures(t *testing.T) {
	for _, failure := range []string{
		"malformed", "query", "invalid-fallback-insert", "hash-insert", "token-insert", "ledger-insert",
	} {
		t.Run(failure, func(t *testing.T) {
			dir := privateDashboardTestDir(t)
			configPath := filepath.Join(dir, launcherconfig.FileName)
			body := `{"port":18800,"dashboard_password_hash":null,"launcher_token":""}`
			switch failure {
			case "malformed":
				body = `{`
			case "invalid-fallback-insert":
				body = `{"port":18800,"dashboard_password_hash":"invalid","launcher_token":"secret"}`
			case "hash-insert":
				body = `{"port":18800,"dashboard_password_hash":"` + testHash(t, "secret") + `"}`
			case "token-insert":
				body = `{"port":18800,"launcher_token":"secret"}`
			}
			if err := os.WriteFile(configPath, []byte(body), 0o600); err != nil {
				t.Fatal(err)
			}
			db, conn := openMigrationDomainConn(t)
			if strings.Contains(failure, "insert") {
				table := "dashboard_credentials"
				if failure == "ledger-insert" {
					table = "launcher_auth_legacy_imports"
				}
				if _, err := conn.ExecContext(t.Context(), `CREATE TRIGGER reject_insert
                        BEFORE INSERT ON `+table+`
                        BEGIN SELECT RAISE(ABORT, 'reject'); END`); err != nil {
					t.Fatal(err)
				}
			}
			if failure == "query" {
				if err := conn.Close(); err != nil {
					t.Fatal(err)
				}
			}
			err := importLegacyLauncherConfig(t.Context(), conn, configPath)
			if err == nil {
				t.Fatalf("%s import succeeded", failure)
			}
			_ = conn.Close()
			_ = db.Close()
		})
	}
}

func openMigrationDomainConn(t *testing.T) (*sql.DB, *sql.Conn) {
	t.Helper()
	db := openLauncherTestDB(t, filepath.Join(privateDashboardTestDir(t), "domain.db"))
	if _, err := db.Exec(sqlCreateCredentials); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(sqlCreateLegacyImports); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(sqlCreateLegacyImportsIndex); err != nil {
		t.Fatal(err)
	}
	conn, err := db.Conn(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	return db, conn
}

func TestLegacyImportRecordInvalidState(t *testing.T) {
	db, conn := openMigrationDomainConn(t)
	if _, err := conn.ExecContext(t.Context(), `PRAGMA ignore_check_constraints = ON`); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.ExecContext(t.Context(), `INSERT INTO launcher_auth_legacy_imports (
            source_id, source_relative, source_digest, source_size, source_limit, source_mode,
            credential_source, imported_count, skipped_count, issue_code, archive_status, imported_at
        ) VALUES (?, ?, ?, 1, ?, 384, 'none', 0, 0, NULL, 'pending', ?)`,
		legacySourceID, launcherconfig.FileName, []byte{1}, legacyConfigMaxBytes, "now"); err != nil {
		t.Fatal(err)
	}
	record, found, err := readLegacyImportRecord(t.Context(), conn)
	if err != nil || !found || record.size != -1 {
		t.Fatalf("invalid digest record = %+v, %t, %v", record, found, err)
	}
	if err := validateLegacyImportRecord(record); err == nil {
		t.Fatal("invalid ledger record validated")
	}
	if err := conn.Close(); err != nil {
		t.Fatal(err)
	}
	if _, _, err := readLegacyImportRecord(t.Context(), conn); err == nil {
		t.Fatal("closed connection read succeeded")
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestRemainingMigrationSecurityFailures(t *testing.T) {
	t.Run("schema unique index", func(t *testing.T) {
		path := filepath.Join(privateDashboardTestDir(t), DBFilename)
		store, err := Open(path)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := store.db.Exec(
			`CREATE UNIQUE INDEX rogue_unique ON dashboard_credentials(bcrypt_hash)`,
		); err != nil {
			t.Fatal(err)
		}
		store.Close()
		if store, err := Open(path); err == nil {
			store.Close()
			t.Fatal("rogue unique index succeeded")
		}
	})
	t.Run("schema trigger", func(t *testing.T) {
		path := filepath.Join(privateDashboardTestDir(t), DBFilename)
		store, err := Open(path)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := store.db.Exec(
			`CREATE TRIGGER rogue_trigger AFTER INSERT ON dashboard_credentials BEGIN SELECT 1; END`,
		); err != nil {
			t.Fatal(err)
		}
		store.Close()
		if store, err := Open(path); err == nil {
			store.Close()
			t.Fatal("rogue trigger succeeded")
		}
	})
	t.Run("invalid pending record", func(t *testing.T) {
		_, dbPath, configPath, _, _ := pendingLauncherMigration(t)
		db := openLauncherTestDB(t, dbPath)
		if _, err := db.Exec(`PRAGMA ignore_check_constraints = ON`); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`UPDATE launcher_auth_legacy_imports SET source_relative = 'wrong.json'`); err != nil {
			t.Fatal(err)
		}
		db.Close()
		if store, err := OpenWithLauncherConfig(dbPath, configPath); err == nil {
			store.Close()
			t.Fatal("invalid pending record succeeded")
		}
	})
	t.Run("unsafe pending source", func(t *testing.T) {
		if runtime.GOOS == "windows" {
			t.Skip("POSIX mode semantics")
		}
		_, dbPath, configPath, _, _ := pendingLauncherMigration(t)
		if err := os.Chmod(configPath, 0o622); err != nil {
			t.Fatal(err)
		}
		if store, err := OpenWithLauncherConfig(dbPath, configPath); err == nil {
			store.Close()
			t.Fatal("unsafe pending source succeeded")
		}
	})
	t.Run("cleanup failure bubbles through finish", func(t *testing.T) {
		resetLegacyMigrationHooks(t)
		_, dbPath, configPath, _, _ := pendingLauncherMigration(t)
		legacyConfigWriteAtomic = func(string, []byte, os.FileMode) error { return errors.New("write") }
		if store, err := OpenWithLauncherConfig(dbPath, configPath); err == nil {
			store.Close()
			t.Fatal("cleanup failure succeeded")
		}
	})
	t.Run("lstat error", func(t *testing.T) {
		resetLegacyMigrationHooks(t)
		dir := privateDashboardTestDir(t)
		path := filepath.Join(dir, launcherconfig.FileName)
		legacyConfigLstat = func(candidate string) (os.FileInfo, error) {
			if candidate == path {
				return nil, errors.New("lstat")
			}
			return os.Lstat(candidate)
		}
		if _, _, err := readLegacyConfig(path); err == nil {
			t.Fatal("lstat failure succeeded")
		}
	})
	t.Run("opened identity mismatch", func(t *testing.T) {
		resetLegacyMigrationHooks(t)
		dir := privateDashboardTestDir(t)
		path := filepath.Join(dir, launcherconfig.FileName)
		other := filepath.Join(dir, "other")
		if err := os.WriteFile(path, []byte("source"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(other, []byte("other"), 0o600); err != nil {
			t.Fatal(err)
		}
		otherInfo, err := os.Stat(other)
		if err != nil {
			t.Fatal(err)
		}
		original := legacyConfigOpen
		legacyConfigOpen = func(path string) (legacyConfigFile, error) {
			file, err := original(path)
			return &legacyFileWrapper{legacyConfigFile: file, statInfo: otherInfo}, err
		}
		if _, _, err := readLegacyConfig(path); err == nil {
			t.Fatal("opened identity mismatch succeeded")
		}
	})
	t.Run("oversized lstat", func(t *testing.T) {
		dir := privateDashboardTestDir(t)
		path := filepath.Join(dir, launcherconfig.FileName)
		file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
		if err != nil {
			t.Fatal(err)
		}
		if err := file.Truncate(legacyConfigMaxBytes + 1); err != nil {
			t.Fatal(err)
		}
		file.Close()
		if _, _, err := readLegacyConfig(path); err == nil {
			t.Fatal("oversized legacy config succeeded")
		}
	})
	t.Run("unsafe archive preparation", func(t *testing.T) {
		if runtime.GOOS == "windows" {
			t.Skip("POSIX mode semantics")
		}
		dir := privateDashboardTestDir(t)
		if err := os.Chmod(dir, 0o770); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })
		if _, err := prepareArchiveDirectory(dir); err == nil {
			t.Fatal("unsafe archive parent succeeded")
		}
	})
	t.Run("exist without destination", func(t *testing.T) {
		resetLegacyMigrationHooks(t)
		dir := privateDashboardTestDir(t)
		sourcePath := filepath.Join(dir, launcherconfig.FileName)
		if err := os.WriteFile(sourcePath, []byte(`{"port":18800}`), 0o600); err != nil {
			t.Fatal(err)
		}
		archiveDir := filepath.Join(dir, "archive")
		if err := os.Mkdir(archiveDir, 0o700); err != nil {
			t.Fatal(err)
		}
		snapshot, _, err := readLegacyConfig(sourcePath)
		if err != nil {
			t.Fatal(err)
		}
		legacyArchiveLink = func(string, string) error { return fs.ErrExist }
		if err := publishArchiveCopy(sourcePath, archiveDir, launcherconfig.FileName, snapshot); err == nil {
			t.Fatal("missing existing destination succeeded")
		}
	})
	t.Run("preexisting verify failure", func(t *testing.T) {
		resetLegacyMigrationHooks(t)
		dir := privateDashboardTestDir(t)
		sourcePath := filepath.Join(dir, launcherconfig.FileName)
		body := []byte(`{"port":18800}`)
		if err := os.WriteFile(sourcePath, body, 0o600); err != nil {
			t.Fatal(err)
		}
		archiveDir := filepath.Join(dir, "archive")
		if err := os.Mkdir(archiveDir, 0o700); err != nil {
			t.Fatal(err)
		}
		archivePath := filepath.Join(archiveDir, launcherconfig.FileName)
		snapshot, _, err := readLegacyConfig(sourcePath)
		if err != nil {
			t.Fatal(err)
		}
		legacyArchiveLink = func(source, target string) error {
			if err := os.Link(source, target); err != nil {
				return err
			}
			return fs.ErrExist
		}
		legacyArchiveSync = func(string) error {
			return os.WriteFile(archivePath, []byte("changed"), 0o600)
		}
		if err := publishArchiveCopy(sourcePath, archiveDir, launcherconfig.FileName, snapshot); err == nil {
			t.Fatal("preexisting verification failure succeeded")
		}
	})
	t.Run("missing directory revalidation", func(t *testing.T) {
		info, err := os.Stat(privateDashboardTestDir(t))
		if err != nil {
			t.Fatal(err)
		}
		if err := directoryUnchanged(filepath.Join(t.TempDir(), "missing"), info); err == nil {
			t.Fatal("missing directory revalidation succeeded")
		}
	})
}
