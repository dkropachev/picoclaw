package repoaudit

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/sipeed/picoclaw/internal/sqlitestore"
)

//nolint:govet // Boundary tests intentionally keep setup and assertion errors in local scopes.
func TestRepositoryReviewSQLiteSchemaConfigurationAndReopen(t *testing.T) {
	store := newRepositoryAuditTestStore(t)
	file := repositoryAuditTestFile("pkg/sqlite.go", "a", 32)
	recorded := recordRepositoryAuditCoverage(
		t, store, "owner/sqlite", "commit-a", "inventory-a", []FileRef{file}, "sqlite-run",
	)
	profileInput := validProfileForTest("rrpf_sqlite", "SQLite")
	profileInput.ScopePolicy.IncludeFolders = []string{"pkg"}
	profileInput.ScopePolicy.ExcludeFolders = []string{"vendor"}
	profile, err := store.CreateProfile(t.Context(), profileInput)
	if err != nil {
		t.Fatal(err)
	}
	automationInput, err := MaterializeRepositoryReviewAutomation(
		profile,
		validAutomationForTest("rra_sqlite", "SQLite automation"),
	)
	if err != nil {
		t.Fatal(err)
	}
	automation, err := store.CreateAutomation(t.Context(), automationInput)
	if err != nil {
		t.Fatal(err)
	}
	database, err := store.openDatabase(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	var version, foreignKeys, synchronous int
	var journal string
	if err := database.QueryRow("PRAGMA user_version").Scan(&version); err != nil {
		t.Fatal(err)
	}
	if err := database.QueryRow("PRAGMA foreign_keys").Scan(&foreignKeys); err != nil {
		t.Fatal(err)
	}
	if err := database.QueryRow("PRAGMA synchronous").Scan(&synchronous); err != nil {
		t.Fatal(err)
	}
	if err := database.QueryRow("PRAGMA journal_mode").Scan(&journal); err != nil {
		t.Fatal(err)
	}
	if version != 2 || foreignKeys != 1 || synchronous != 2 || journal != "wal" {
		t.Fatalf("SQLite configuration version=%d fk=%d sync=%d journal=%q", version, foreignKeys, synchronous, journal)
	}
	info, err := os.Stat(filepath.Join(store.root, repositoryReviewDatabaseFilename))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("database mode=%o", info.Mode().Perm())
	}
	reopened := NewSQLiteStore(store.workspace)
	state, found, err := reopened.GetByID(recorded.State.ID)
	if err != nil || !found || !reflect.DeepEqual(state, recorded.State) {
		t.Fatalf("reopened state=%#v found=%v err=%v", state, found, err)
	}
	loadedProfile, found, err := reopened.GetProfile(t.Context(), profile.ID)
	if err != nil || !found || loadedProfile.ID != profile.ID ||
		loadedProfile.Version != profile.Version || loadedProfile.Name != profile.Name {
		t.Fatalf("reopened profile=%#v want=%#v found=%v err=%v", loadedProfile, profile, found, err)
	}
	loadedAutomation, found, err := reopened.GetAutomation(t.Context(), automation.ID)
	if err != nil || !found || !reflect.DeepEqual(loadedAutomation, automation) {
		t.Fatalf("reopened automation=%#v found=%v err=%v", loadedAutomation, found, err)
	}
}

//nolint:govet // Boundary tests intentionally keep setup and assertion errors in local scopes.
func TestRepositoryReviewSQLiteEnumerationAndClosedConnectionBoundaries(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "missing")
	if sources, err := legacyRepositoryReviewSources(missing); err != nil || sources != nil {
		t.Fatalf("missing sources=%#v err=%v", sources, err)
	}
	fileRoot := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(fileRoot, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := legacyRepositoryReviewSources(fileRoot); err == nil {
		t.Fatal("file root enumerated")
	}
	root := t.TempDir()
	name := profileFilename("rrpf_directory")
	if err := os.Mkdir(filepath.Join(root, name), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := legacyRepositoryReviewSources(root); err == nil {
		t.Fatal("matching directory enumerated")
	}
	if err := os.Remove(filepath.Join(root, name)); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "target"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(root, "target"), filepath.Join(root, name)); err == nil {
		if _, err := legacyRepositoryReviewSources(root); err == nil {
			t.Fatal("matching symlink enumerated")
		}
	}

	store := newRepositoryAuditTestStore(t)
	if _, err := store.List(); err != nil {
		t.Fatal(err)
	}
	database, err := store.openDatabase(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	conn, err := database.Conn(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	_ = conn.Close()
	if err := validateRepositoryReviewDatabaseSchema(t.Context(), conn); err == nil {
		t.Fatal("closed schema connection validated")
	}
	if err := validateRepositoryReviewSchemaObjectSet(t.Context(), conn); err == nil {
		t.Fatal("closed schema object-set connection validated")
	}
	if err := validateRepositoryReviewAggregateRows(t.Context(), conn); err == nil {
		t.Fatal("closed aggregate query validated")
	}
	if _, err := insertRepositoryStateConn(
		t.Context(), conn, repositoryReviewCoverageState("owner/closed"), true, 0,
	); err == nil {
		t.Fatal("closed state insert succeeded")
	}
	if _, err := insertRepositoryReviewProfileConn(
		t.Context(), conn, profileCoverageFixture("rrpf_closed"), true, 0,
	); err == nil {
		t.Fatal("closed profile insert succeeded")
	}
	if _, err := insertRepositoryReviewAutomationConn(
		t.Context(), conn, validAutomationForTest("rra_closed", "Closed"), true, 0,
	); err == nil {
		t.Fatal("closed automation insert succeeded")
	}
	if _, err := loadRepositoryStateRow(t.Context(), database, RepositoryID("owner/closed")); err == nil {
		t.Fatal("closed state load succeeded")
	}
	if _, err := loadRepositoryReviewProfileRow(t.Context(), database, "rrpf_closed"); err == nil {
		t.Fatal("closed profile load succeeded")
	}
	if _, err := loadRepositoryReviewAutomationRow(t.Context(), database, "rra_closed"); err == nil {
		t.Fatal("closed automation load succeeded")
	}
	_ = database.Close()
	loose, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := loose.Exec(
		`CREATE TABLE values_for_scan (owner, value); INSERT INTO values_for_scan VALUES ('owner', NULL)`,
	); err != nil {
		loose.Close()
		t.Fatal(err)
	}
	if _, err := loadReviewOrderedStrings(
		t.Context(), loose, `SELECT value FROM values_for_scan WHERE owner = ?`, "owner",
	); err == nil {
		t.Fatal("invalid ordered string row scanned")
	}
	_ = loose.Close()
}

//nolint:govet // Boundary tests intentionally keep setup and assertion errors in local scopes.
func TestRepositoryReviewSQLitePathAndUniqueIndexFailures(t *testing.T) {
	if runtime.GOOS != "windows" {
		original, err := os.Getwd()
		if err != nil {
			t.Fatal(err)
		}
		removed := t.TempDir()
		if err := os.Chdir(removed); err != nil {
			t.Fatal(err)
		}
		if err := os.Remove(removed); err != nil {
			t.Fatal(err)
		}
		_, openErr := (Store{root: "relative"}).openDatabase(t.Context())
		if err := os.Chdir(original); err != nil {
			t.Fatal(err)
		}
		if openErr == nil {
			t.Fatal("unresolvable database path opened")
		}
	}
	store := newRepositoryAuditTestStore(t)
	if _, err := store.List(); err != nil {
		t.Fatal(err)
	}
	database, err := store.openDatabase(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(
		`CREATE UNIQUE INDEX unexpected_state_unique ON repository_review_states(last_commit_sha)`,
	); err != nil {
		database.Close()
		t.Fatal(err)
	}
	conn, err := database.Conn(t.Context())
	if err != nil {
		database.Close()
		t.Fatal(err)
	}
	if err := validateRepositoryReviewDatabaseSchema(t.Context(), conn); err == nil {
		t.Fatal("unexpected unique index validated")
	}
	_ = conn.Close()
	_ = database.Close()
}

//nolint:govet // Boundary tests intentionally keep setup and assertion errors in local scopes.
func TestRepositoryReviewSQLiteCASAndProjectionFailureBoundaries(t *testing.T) {
	store := newRepositoryAuditTestStore(t)
	file := repositoryAuditTestFile("pkg/tamper.go", "e", 96)
	recorded := recordRepositoryAuditCoverage(
		t, store, "owner/tamper", "commit-e", "inventory-e", []FileRef{file}, "tamper-run",
	)
	profile, err := store.CreateProfile(t.Context(), validProfileForTest("rrpf_tamper", "Tamper"))
	if err != nil {
		t.Fatal(err)
	}
	automationInput, err := MaterializeRepositoryReviewAutomation(
		profile, validAutomationForTest("rra_tamper", "Tamper automation"),
	)
	if err != nil {
		t.Fatal(err)
	}
	automationInput.RunIDs = []string{"run-tamper"}
	automation, err := store.CreateAutomation(t.Context(), automationInput)
	if err != nil {
		t.Fatal(err)
	}
	database, err := store.openDatabase(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	migrationState := recorded.State
	migrationState.SchemaVersion = SchemaVersion + 1
	migrationPayload, _ := json.Marshal(migrationState)
	invalidState := recorded.State
	invalidState.Runs[0].InspectedFiles = -1
	invalidPayload, _ := json.Marshal(invalidState)

	staleState := recorded.State
	staleState.Version += 10
	if err := saveRepositoryStateDatabase(t.Context(), database, &staleState); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale state save=%v", err)
	}
	if err := saveRepositoryStateDatabase(t.Context(), database, nil); err == nil {
		t.Fatal("nil state save succeeded")
	}
	staleProfile := profile
	staleProfile.Version += 10
	if err := saveRepositoryReviewProfileDatabase(t.Context(), database, staleProfile); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale profile save=%v", err)
	}
	staleAutomation := automation
	staleAutomation.Version += 10
	if err := saveRepositoryReviewAutomationDatabase(
		t.Context(),
		database,
		staleAutomation,
	); !errors.Is(
		err,
		ErrConflict,
	) {
		t.Fatalf("stale automation save=%v", err)
	}

	for name, statement := range map[string]string{
		"state payload":      `UPDATE repository_review_states SET payload_json = X'7B'`,
		"state migration":    fmt.Sprintf(`UPDATE repository_review_states SET payload_json = X'%x'`, migrationPayload),
		"state invalid":      fmt.Sprintf(`UPDATE repository_review_states SET payload_json = X'%x'`, invalidPayload),
		"state typed":        `UPDATE repository_review_states SET finding_count = finding_count + 1`,
		"state records":      `DELETE FROM repository_review_records WHERE record_kind = 'run'`,
		"state record table": `DROP TABLE repository_review_records`,
		"state record extra": fmt.Sprintf(`INSERT INTO repository_review_records (
			state_id, record_kind, position, record_id, status, version, created_at_unix_nano, updated_at_unix_nano
		) VALUES (%q, 'run', 1, 'extra', '', 0, 0, 0)`, recorded.State.ID),
		"state record scan": fmt.Sprintf(`DROP TABLE repository_review_records;
			CREATE TABLE repository_review_records (
				state_id, record_kind, position, record_id, status, version, created_at_unix_nano, updated_at_unix_nano
			);
			INSERT INTO repository_review_records VALUES (%q, 'run', 'bad', 'rewrite-run', '', 0, 0, 0)`, recorded.State.ID),
		"profile typed":       `UPDATE repository_review_profiles SET name = ''`,
		"profile scope":       `UPDATE repository_review_profile_scope SET scope_value = 'invalid' WHERE scope_kind = 'code_type'`,
		"profile scope table": `DROP TABLE repository_review_profile_scope`,
		"profile scope scan": fmt.Sprintf(`DROP TABLE repository_review_profile_scope;
			CREATE TABLE repository_review_profile_scope (profile_id, scope_kind, position, scope_value);
			INSERT INTO repository_review_profile_scope VALUES (%q, 'code_type', 0, NULL)`, profile.ID),
		"automation payload":     `UPDATE repository_review_automations SET payload_json = X'7B'`,
		"automation typed":       `UPDATE repository_review_automations SET name = 'typed-drift'`,
		"automation models":      `DELETE FROM repository_review_automation_models`,
		"automation model table": `DROP TABLE repository_review_automation_models`,
		"automation model drift": `UPDATE repository_review_automation_models SET model_alias = 'different' WHERE position = 0`,
		"automation runs":        `UPDATE repository_review_automation_runs SET position = 3 WHERE position = 0`,
		"automation run table":   `DROP TABLE repository_review_automation_runs`,
	} {
		t.Run(name, func(t *testing.T) {
			clonePath := filepath.Join(t.TempDir(), "copy.db")
			if _, err := database.Exec("PRAGMA wal_checkpoint(TRUNCATE)"); err != nil {
				t.Fatal(err)
			}
			source, err := os.ReadFile(filepath.Join(store.root, repositoryReviewDatabaseFilename))
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(clonePath, source, 0o600); err != nil {
				t.Fatal(err)
			}
			clone, err := sql.Open("sqlite", clonePath)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := clone.Exec("PRAGMA foreign_keys = ON"); err != nil {
				clone.Close()
				t.Fatal(err)
			}
			if _, err := clone.Exec(statement); err != nil {
				clone.Close()
				t.Fatal(err)
			}
			var loadErr error
			switch {
			case strings.HasPrefix(name, "state"):
				_, loadErr = loadRepositoryStateRow(t.Context(), clone, recorded.State.ID)
			case strings.HasPrefix(name, "profile"):
				_, loadErr = loadRepositoryReviewProfileRow(t.Context(), clone, profile.ID)
			default:
				if name == "automation runs" {
					conn, connErr := clone.Conn(t.Context())
					if connErr != nil {
						clone.Close()
						t.Fatal(connErr)
					}
					loadErr = validateRepositoryReviewDatabaseSchema(t.Context(), conn)
					_ = conn.Close()
				} else {
					_, loadErr = loadRepositoryReviewAutomationRow(t.Context(), clone, automation.ID)
				}
			}
			_ = clone.Close()
			if loadErr == nil {
				t.Fatalf("%s tamper loaded", name)
			}
		})
	}
	var automationPayload []byte
	if err := database.QueryRow(
		`SELECT payload_json FROM repository_review_automations WHERE automation_id = ?`, automation.ID,
	).Scan(&automationPayload); err != nil {
		t.Fatal(err)
	}
	var invalidStoredAutomation RepositoryReviewAutomation
	if err := json.Unmarshal(automationPayload, &invalidStoredAutomation); err != nil {
		t.Fatal(err)
	}
	invalidStoredAutomation.MaxFilesPerRun = -1
	automationPayload, _ = json.Marshal(invalidStoredAutomation)
	if _, err := database.Exec(
		`UPDATE repository_review_automations SET payload_json = ? WHERE automation_id = ?`,
		automationPayload, automation.ID,
	); err != nil {
		t.Fatal(err)
	}
	if _, err := loadRepositoryReviewAutomationRow(t.Context(), database, automation.ID); err == nil {
		t.Fatal("invalid stored automation loaded")
	}
	_ = database.Close()
}

func TestRepositoryReviewSQLiteRelationshipWriteFailures(t *testing.T) {
	t.Run("state payload", func(t *testing.T) {
		state := repositoryReviewCoverageState("owner/bad-time")
		state.UpdatedAt = time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC)
		if _, err := insertRepositoryStateConn(t.Context(), nil, state, true, 0); err == nil {
			t.Fatal("unencodable state inserted")
		}
	})
	t.Run("automation payload", func(t *testing.T) {
		automation := validAutomationForTest("rra_bad_time", "Bad time")
		automation.SchemaVersion = RepositoryReviewAutomationSchemaVersion
		automation.Version = 1
		automation.Status = RepositoryReviewAutomationIdle
		automation.CreatedAt = time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC)
		automation.UpdatedAt = automation.CreatedAt
		if _, err := insertRepositoryReviewAutomationConn(
			t.Context(), nil, automation, true, 0,
		); err == nil {
			t.Fatal("unencodable automation inserted")
		}
	})

	for name, setup := range map[string]func(*testing.T, Store, *sql.DB, *sql.Conn){
		"state delete": func(t *testing.T, store Store, database *sql.DB, conn *sql.Conn) {
			file := repositoryAuditTestFile("pkg/delete.go", "f", 1)
			state := recordRepositoryAuditCoverage(t, store, "owner/delete", "c", "i", []FileRef{file}, "r").State
			if _, err := database.Exec("DROP TABLE repository_review_records"); err != nil {
				t.Fatal(err)
			}
			if _, err := insertRepositoryStateConn(t.Context(), conn, state, false, state.Version); err == nil {
				t.Fatal("state child delete failure ignored")
			}
		},
		"state insert": func(t *testing.T, store Store, database *sql.DB, conn *sql.Conn) {
			file := repositoryAuditTestFile("pkg/insert.go", "a", 1)
			state := recordRepositoryAuditCoverage(t, store, "owner/insert", "c", "i", []FileRef{file}, "r").State
			if _, err := database.Exec(`CREATE TRIGGER reject_records BEFORE INSERT ON repository_review_records BEGIN SELECT RAISE(FAIL, 'reject'); END`); err != nil {
				t.Fatal(err)
			}
			if _, err := insertRepositoryStateConn(t.Context(), conn, state, false, state.Version); err == nil {
				t.Fatal("state child insert failure ignored")
			}
		},
		"profile delete": func(t *testing.T, store Store, database *sql.DB, conn *sql.Conn) {
			profile, err := store.CreateProfile(t.Context(), validProfileForTest("rrpf_delete_child", "Delete"))
			if err != nil {
				t.Fatal(err)
			}
			if _, err := database.Exec("DROP TABLE repository_review_profile_scope"); err != nil {
				t.Fatal(err)
			}
			if _, err := insertRepositoryReviewProfileConn(t.Context(), conn, profile, false, profile.Version); err == nil {
				t.Fatal("profile child delete failure ignored")
			}
		},
		"profile insert": func(t *testing.T, store Store, database *sql.DB, conn *sql.Conn) {
			profile, err := store.CreateProfile(t.Context(), validProfileForTest("rrpf_insert_child", "Insert"))
			if err != nil {
				t.Fatal(err)
			}
			if _, err := database.Exec(`CREATE TRIGGER reject_scope BEFORE INSERT ON repository_review_profile_scope BEGIN SELECT RAISE(FAIL, 'reject'); END`); err != nil {
				t.Fatal(err)
			}
			if _, err := insertRepositoryReviewProfileConn(t.Context(), conn, profile, false, profile.Version); err == nil {
				t.Fatal("profile child insert failure ignored")
			}
		},
		"automation models delete": func(t *testing.T, store Store, database *sql.DB, conn *sql.Conn) {
			automation, err := store.CreateAutomation(t.Context(), validAutomationForTest("rra_models_delete", "Delete"))
			if err != nil {
				t.Fatal(err)
			}
			if _, err := database.Exec("DROP TABLE repository_review_automation_models"); err != nil {
				t.Fatal(err)
			}
			if _, err := insertRepositoryReviewAutomationConn(t.Context(), conn, automation, false, automation.Version); err == nil {
				t.Fatal("automation model delete failure ignored")
			}
		},
		"automation models insert": func(t *testing.T, store Store, database *sql.DB, conn *sql.Conn) {
			automation, err := store.CreateAutomation(t.Context(), validAutomationForTest("rra_models_insert", "Insert"))
			if err != nil {
				t.Fatal(err)
			}
			if _, err := database.Exec(`CREATE TRIGGER reject_models BEFORE INSERT ON repository_review_automation_models BEGIN SELECT RAISE(FAIL, 'reject'); END`); err != nil {
				t.Fatal(err)
			}
			if _, err := insertRepositoryReviewAutomationConn(t.Context(), conn, automation, false, automation.Version); err == nil {
				t.Fatal("automation model insert failure ignored")
			}
		},
		"automation runs delete": func(t *testing.T, store Store, database *sql.DB, conn *sql.Conn) {
			automation := validAutomationForTest("rra_runs_delete", "Delete")
			automation.RunIDs = []string{"run"}
			created, err := store.CreateAutomation(t.Context(), automation)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := database.Exec("DROP TABLE repository_review_automation_runs"); err != nil {
				t.Fatal(err)
			}
			if _, err := insertRepositoryReviewAutomationConn(t.Context(), conn, created, false, created.Version); err == nil {
				t.Fatal("automation run delete failure ignored")
			}
		},
		"automation runs insert": func(t *testing.T, store Store, database *sql.DB, conn *sql.Conn) {
			automation := validAutomationForTest("rra_runs_insert", "Insert")
			automation.RunIDs = []string{"run"}
			created, err := store.CreateAutomation(t.Context(), automation)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := database.Exec(`CREATE TRIGGER reject_runs BEFORE INSERT ON repository_review_automation_runs BEGIN SELECT RAISE(FAIL, 'reject'); END`); err != nil {
				t.Fatal(err)
			}
			if _, err := insertRepositoryReviewAutomationConn(t.Context(), conn, created, false, created.Version); err == nil {
				t.Fatal("automation run insert failure ignored")
			}
		},
	} {
		t.Run(name, func(t *testing.T) {
			store := newRepositoryAuditTestStore(t)
			database, err := store.openDatabase(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			conn, err := database.Conn(t.Context())
			if err != nil {
				database.Close()
				t.Fatal(err)
			}
			setup(t, store, database, conn)
			_ = conn.Close()
			_ = database.Close()
		})
	}
}

func TestRepositoryReviewSQLiteSaveConflictAndQueryFailures(t *testing.T) {
	for name, run := range map[string]func(*testing.T, Store, *sql.DB){
		"state query": func(t *testing.T, store Store, database *sql.DB) {
			state := repositoryReviewCoverageState("owner/query")
			if _, err := database.Exec("DROP TABLE repository_review_states"); err != nil {
				t.Fatal(err)
			}
			if err := saveRepositoryStateDatabase(t.Context(), database, &state); err == nil {
				t.Fatal("state query failure ignored")
			}
		},
		"state insert error": func(t *testing.T, store Store, database *sql.DB) {
			state := repositoryReviewCoverageState("owner/insert-error")
			if _, err := database.Exec(`CREATE TRIGGER reject_state_insert BEFORE INSERT ON repository_review_states BEGIN SELECT RAISE(FAIL, 'reject'); END`); err != nil {
				t.Fatal(err)
			}
			if err := saveRepositoryStateDatabase(t.Context(), database, &state); err == nil {
				t.Fatal("state insert error ignored")
			}
		},
		"state insert ignored": func(t *testing.T, store Store, database *sql.DB) {
			state := repositoryReviewCoverageState("owner/insert-ignore")
			if _, err := database.Exec(`CREATE TRIGGER ignore_state_insert BEFORE INSERT ON repository_review_states BEGIN SELECT RAISE(IGNORE); END`); err != nil {
				t.Fatal(err)
			}
			if err := saveRepositoryStateDatabase(t.Context(), database, &state); !errors.Is(err, ErrConflict) {
				t.Fatalf("ignored state insert=%v", err)
			}
		},
		"state update error": func(t *testing.T, store Store, database *sql.DB) {
			file := repositoryAuditTestFile("pkg/state.go", "2", 2)
			state := recordRepositoryAuditCoverage(t, store, "owner/update-error", "c", "i", []FileRef{file}, "r").State
			if _, err := database.Exec(`CREATE TRIGGER reject_state_update BEFORE UPDATE ON repository_review_states BEGIN SELECT RAISE(FAIL, 'reject'); END`); err != nil {
				t.Fatal(err)
			}
			if err := saveRepositoryStateDatabase(t.Context(), database, &state); err == nil {
				t.Fatal("state update error ignored")
			}
		},
		"state update ignored": func(t *testing.T, store Store, database *sql.DB) {
			file := repositoryAuditTestFile("pkg/state.go", "3", 3)
			state := recordRepositoryAuditCoverage(t, store, "owner/update-ignore", "c", "i", []FileRef{file}, "r").State
			if _, err := database.Exec(`CREATE TRIGGER ignore_state_update BEFORE UPDATE ON repository_review_states BEGIN SELECT RAISE(IGNORE); END`); err != nil {
				t.Fatal(err)
			}
			if err := saveRepositoryStateDatabase(t.Context(), database, &state); !errors.Is(err, ErrConflict) {
				t.Fatalf("ignored state update=%v", err)
			}
		},
		"profile query": func(t *testing.T, store Store, database *sql.DB) {
			if _, err := database.Exec("DROP TABLE repository_review_profiles"); err != nil {
				t.Fatal(err)
			}
			if err := saveRepositoryReviewProfileDatabase(t.Context(), database, profileCoverageFixture("rrpf_query")); err == nil {
				t.Fatal("profile query failure ignored")
			}
		},
		"profile ignored": func(t *testing.T, store Store, database *sql.DB) {
			profile, err := store.CreateProfile(t.Context(), validProfileForTest("rrpf_ignore", "Ignore"))
			if err != nil {
				t.Fatal(err)
			}
			if _, err := database.Exec(`CREATE TRIGGER ignore_profile_update BEFORE UPDATE ON repository_review_profiles BEGIN SELECT RAISE(IGNORE); END`); err != nil {
				t.Fatal(err)
			}
			if err := saveRepositoryReviewProfileDatabase(t.Context(), database, profile); !errors.Is(err, ErrConflict) {
				t.Fatalf("ignored profile update=%v", err)
			}
		},
		"profile insert error": func(t *testing.T, store Store, database *sql.DB) {
			if _, err := database.Exec(`CREATE TRIGGER reject_profile_insert BEFORE INSERT ON repository_review_profiles BEGIN SELECT RAISE(FAIL, 'reject'); END`); err != nil {
				t.Fatal(err)
			}
			if err := saveRepositoryReviewProfileDatabase(
				t.Context(), database, profileCoverageFixture("rrpf_insert_error"),
			); err == nil {
				t.Fatal("profile insert error ignored")
			}
		},
		"automation query": func(t *testing.T, store Store, database *sql.DB) {
			if _, err := database.Exec("DROP TABLE repository_review_automations"); err != nil {
				t.Fatal(err)
			}
			automation := validAutomationForTest("rra_query", "Query")
			automation.SchemaVersion = RepositoryReviewAutomationSchemaVersion
			automation.Version = 1
			automation.Status = RepositoryReviewAutomationIdle
			automation.CreatedAt = automationTestNow
			automation.UpdatedAt = automationTestNow
			if err := saveRepositoryReviewAutomationDatabase(t.Context(), database, automation); err == nil {
				t.Fatal("automation query failure ignored")
			}
		},
		"automation ignored": func(t *testing.T, store Store, database *sql.DB) {
			automation, err := store.CreateAutomation(t.Context(), validAutomationForTest("rra_ignore", "Ignore"))
			if err != nil {
				t.Fatal(err)
			}
			if _, err := database.Exec(`CREATE TRIGGER ignore_automation_update BEFORE UPDATE ON repository_review_automations BEGIN SELECT RAISE(IGNORE); END`); err != nil {
				t.Fatal(err)
			}
			if err := saveRepositoryReviewAutomationDatabase(t.Context(), database, automation); !errors.Is(err, ErrConflict) {
				t.Fatalf("ignored automation update=%v", err)
			}
		},
	} {
		t.Run(name, func(t *testing.T) {
			store := newRepositoryAuditTestStore(t)
			database, err := store.openDatabase(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			run(t, store, database)
			_ = database.Close()
		})
	}
}

func TestRepositoryReviewSQLiteFacadesPropagateDatabaseFailure(t *testing.T) {
	type fixture struct {
		store      Store
		state      RepositoryState
		profile    RepositoryReviewProfile
		automation RepositoryReviewAutomation
	}
	newFixture := func(t *testing.T) fixture {
		t.Helper()
		store := newRepositoryAuditTestStore(t)
		file := repositoryAuditTestFile("pkg/failure.go", "6", 6)
		state := recordRepositoryAuditCoverage(
			t, store, "owner/failure", "commit", "inventory", []FileRef{file}, "run",
		).State
		profile, err := store.CreateProfile(t.Context(), validProfileForTest("rrpf_failure", "Failure"))
		if err != nil {
			t.Fatal(err)
		}
		automationInput, err := MaterializeRepositoryReviewAutomation(
			profile, validAutomationForTest("rra_failure", "Failure"),
		)
		if err != nil {
			t.Fatal(err)
		}
		automation, err := store.CreateAutomation(t.Context(), automationInput)
		if err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(store.root, repositoryReviewDatabaseFilename)
		if err := os.Remove(path); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("not-sqlite"), 0o600); err != nil {
			t.Fatal(err)
		}
		return fixture{store: store, state: state, profile: profile, automation: automation}
	}
	tests := map[string]func(*testing.T, fixture){
		"list states":    func(t *testing.T, f fixture) { _, err := f.store.List(); requireSQLiteFailure(t, err) },
		"list summaries": func(t *testing.T, f fixture) { _, err := f.store.ListSummaries(); requireSQLiteFailure(t, err) },
		"get id":         func(t *testing.T, f fixture) { _, _, err := f.store.GetByID(f.state.ID); requireSQLiteFailure(t, err) },
		"get repository": func(t *testing.T, f fixture) {
			_, _, err := f.store.Get(f.state.Repository)
			requireSQLiteFailure(t, err)
		},
		"save state": func(t *testing.T, f fixture) { requireSQLiteFailure(t, f.store.save(&f.state)) },
		"list profiles": func(t *testing.T, f fixture) {
			_, err := f.store.listProfilesUnlocked(maxProfileCount)
			requireSQLiteFailure(t, err)
		},
		"load profile": func(t *testing.T, f fixture) {
			_, _, err := f.store.loadProfile(f.profile.ID)
			requireSQLiteFailure(t, err)
		},
		"save profile": func(t *testing.T, f fixture) { requireSQLiteFailure(t, f.store.saveProfile(f.profile)) },
		"profile assigned": func(t *testing.T, f fixture) {
			_, err := f.store.profileAssignedUnlocked(f.profile.ID)
			requireSQLiteFailure(t, err)
		},
		"profile active": func(t *testing.T, f fixture) {
			_, err := f.store.profileActiveUnlocked(f.profile.ID)
			requireSQLiteFailure(t, err)
		},
		"create profile": func(t *testing.T, f fixture) {
			_, err := f.store.CreateProfile(t.Context(), validProfileForTest("rrpf_new_failure", "New"))
			requireSQLiteFailure(t, err)
		},
		"update profile": func(t *testing.T, f fixture) {
			_, err := f.store.UpdateProfile(
				t.Context(),
				f.profile.ID,
				f.profile.Version,
				func(*RepositoryReviewProfile) error { return nil },
			)
			requireSQLiteFailure(t, err)
		},
		"delete profile": func(t *testing.T, f fixture) {
			requireSQLiteFailure(t, f.store.DeleteProfile(t.Context(), f.profile.ID, f.profile.Version))
		},
		"list automations": func(t *testing.T, f fixture) {
			_, err := f.store.listAutomationsUnlocked(maxAutomationCount)
			requireSQLiteFailure(t, err)
		},
		"load automation": func(t *testing.T, f fixture) {
			_, _, err := f.store.loadAutomation(f.automation.ID)
			requireSQLiteFailure(t, err)
		},
		"save automation": func(t *testing.T, f fixture) { requireSQLiteFailure(t, f.store.saveAutomation(f.automation)) },
		"automation unique": func(t *testing.T, f fixture) {
			requireSQLiteFailure(t, f.store.ensureRepositoryAutomationUniqueUnlocked("rra_other", "owner/other"))
		},
		"profile snapshot": func(t *testing.T, f fixture) {
			requireSQLiteFailure(t, f.store.validateAutomationProfileSnapshotUnlocked(f.automation))
		},
		"create automation": func(t *testing.T, f fixture) {
			_, err := f.store.CreateAutomation(t.Context(), validAutomationForTest("rra_new_failure", "New"))
			requireSQLiteFailure(t, err)
		},
		"update automation": func(t *testing.T, f fixture) {
			_, err := f.store.UpdateAutomation(
				t.Context(),
				f.automation.ID,
				f.automation.Version,
				func(*RepositoryReviewAutomation) error { return nil },
			)
			requireSQLiteFailure(t, err)
		},
	}
	for name, run := range tests {
		t.Run(name, func(t *testing.T) { run(t, newFixture(t)) })
	}
}

func requireSQLiteFailure(t *testing.T, err error) {
	t.Helper()
	if err == nil {
		t.Fatal("corrupt SQLite database operation succeeded")
	}
}

func TestRepositoryReviewSQLiteCatalogQueryAndScanFailures(t *testing.T) {
	for name, test := range map[string]struct {
		setup string
		call  func(Store) error
	}{
		"automation query": {call: func(store Store) error { _, err := store.listAutomationsUnlocked(maxAutomationCount); return err }},
		"automation scan": {
			setup: `CREATE TABLE repository_review_automations (automation_id, updated_at_unix_nano); INSERT INTO repository_review_automations VALUES (NULL, 0)`,
			call:  func(store Store) error { _, err := store.listAutomationsUnlocked(maxAutomationCount); return err },
		},
		"profile query": {call: func(store Store) error { _, err := store.listProfilesUnlocked(maxProfileCount); return err }},
		"profile scan": {
			setup: `CREATE TABLE repository_review_profiles (profile_id, updated_at_unix_nano); INSERT INTO repository_review_profiles VALUES (NULL, 0)`,
			call:  func(store Store) error { _, err := store.listProfilesUnlocked(maxProfileCount); return err },
		},
		"state query": {call: func(store Store) error { _, err := store.List(); return err }},
		"state scan": {
			setup: `CREATE TABLE repository_review_states (state_id, updated_at_unix_nano); INSERT INTO repository_review_states VALUES (NULL, 0)`,
			call:  func(store Store) error { _, err := store.List(); return err },
		},
		"summary query": {call: func(store Store) error { _, err := store.ListSummaries(); return err }},
		"summary scan": {
			setup: `CREATE TABLE repository_review_states (
				schema_version, state_id, repository, version, review_version, last_commit_sha,
				finding_count, repository_finding_count, open_finding_count, issue_draft_count,
				unsupported_count, reviewed_file_count, excluded_file_count, updated_at_unix_nano
			); INSERT INTO repository_review_states VALUES (NULL,NULL,NULL,NULL,NULL,NULL,NULL,NULL,NULL,NULL,NULL,NULL,NULL,NULL)`,
			call: func(store Store) error { _, err := store.ListSummaries(); return err },
		},
	} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "raw.db")
			if test.setup != "" {
				database, err := sql.Open("sqlite", path)
				if err != nil {
					t.Fatal(err)
				}
				if _, err := database.Exec(test.setup); err != nil {
					database.Close()
					t.Fatal(err)
				}
				_ = database.Close()
			}
			store := NewSQLiteStore(t.TempDir())
			store.openForTest = func(context.Context) (*sql.DB, error) { return sql.Open("sqlite", path) }
			if err := test.call(store); err == nil {
				t.Fatalf("%s succeeded", name)
			}
		})
	}
}

//nolint:govet // Boundary tests intentionally keep setup and assertion errors in local scopes.
func TestRepositoryReviewSQLiteDeleteFailureBoundaries(t *testing.T) {
	for _, mode := range []string{"open", "reject", "ignore"} {
		t.Run("profile "+mode, func(t *testing.T) {
			store := newRepositoryAuditTestStore(t)
			profile, err := store.CreateProfile(t.Context(), validProfileForTest("rrpf_delete_boundary", "Delete"))
			if err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(store.root, repositoryReviewDatabaseFilename)
			if mode != "open" {
				database, err := sql.Open("sqlite", path)
				if err != nil {
					t.Fatal(err)
				}
				action := "FAIL, 'reject'"
				if mode == "ignore" {
					action = "IGNORE"
				}
				trigger := fmt.Sprintf(`CREATE TRIGGER delete_boundary
					BEFORE DELETE ON repository_review_profiles
					BEGIN SELECT RAISE(%s); END`, action)
				if _, err := database.Exec(trigger); err != nil {
					database.Close()
					t.Fatal(err)
				}
				_ = database.Close()
			}
			calls := 0
			store.openForTest = func(context.Context) (*sql.DB, error) {
				calls++
				if mode == "open" && calls == 3 {
					return nil, errors.New("open failed")
				}
				return sql.Open("sqlite", path)
			}
			err = store.DeleteProfile(t.Context(), profile.ID, profile.Version)
			if err == nil {
				t.Fatal("delete boundary succeeded")
			}
		})
	}
}

func TestRepositoryReviewSQLiteRetryStatusSaveFailure(t *testing.T) {
	store := newRepositoryAuditTestStore(t)
	state := recordMappingWorkerFinding(
		t, store, "sqlite-status-save", strings.Repeat("9", 40), "status.go", "status.retry",
	)
	findingID := state.Findings[len(state.Findings)-1].ID
	for index := range state.MappingJobs {
		if state.MappingJobs[index].ReviewFindingID == findingID {
			state.MappingJobs[index].Attempts = RepositoryRunFindingStatusAttemptLimit
			state.MappingJobs[index].Error = "failed"
		}
	}
	failure := store
	failure.loadForTest = func(string) (RepositoryState, error) { return state, nil }
	failure.openForTest = func(context.Context) (*sql.DB, error) {
		return nil, errors.New("save failed")
	}
	if _, _, err := failure.RetryRunFindingStatus(
		state.Repository, []string{findingID},
	); err == nil {
		t.Fatal("retry status ignored SQLite save failure")
	}
}

func TestRepositoryReviewSQLiteSnapshotMappingSaveFailure(t *testing.T) {
	store := newRepositoryAuditTestStore(t)
	state, finding := recordLifecycleFinding(
		t, store, strings.Repeat("a", 40), strings.Repeat("b", 40), "sqlite-snapshot-run",
		"main", "main", true, "snapshot failure",
	)
	failure := store
	failure.loadForTest = func(string) (RepositoryState, error) { return state, nil }
	failure.openForTest = func(context.Context) (*sql.DB, error) {
		return nil, errors.New("save failed")
	}
	if _, err := failure.SnapshotMappingJobs(
		state.Repository, []string{finding.ID}, RepositoryMappingModelSnapshot{Model: "reviewer"},
	); err == nil {
		t.Fatal("mapping snapshot ignored SQLite save failure")
	}
}

func TestRepositoryReviewSQLiteProfileAutomationCatalogFailuresReachCallers(t *testing.T) {
	store := newRepositoryAuditTestStore(t)
	profile, err := store.CreateProfile(t.Context(), validProfileForTest("rrpf_catalog_failure", "Catalog"))
	if err != nil {
		t.Fatal(err)
	}
	automationInput, err := MaterializeRepositoryReviewAutomation(
		profile, validAutomationForTest("rra_catalog_failure", "Catalog"),
	)
	if err != nil {
		t.Fatal(err)
	}
	automation, err := store.CreateAutomation(t.Context(), automationInput)
	if err != nil {
		t.Fatal(err)
	}
	database, err := store.openDatabase(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	var payload []byte
	if err := database.QueryRow(
		`SELECT payload_json FROM repository_review_automations WHERE automation_id = ?`, automation.ID,
	).Scan(&payload); err != nil {
		t.Fatal(err)
	}
	var invalid RepositoryReviewAutomation
	_ = json.Unmarshal(payload, &invalid)
	invalid.MaxFilesPerRun = -1
	payload, _ = json.Marshal(invalid)
	if _, err := database.Exec(
		`UPDATE repository_review_automations SET payload_json = ? WHERE automation_id = ?`,
		payload, automation.ID,
	); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec("PRAGMA wal_checkpoint(TRUNCATE)"); err != nil {
		t.Fatal(err)
	}
	_ = database.Close()
	path := filepath.Join(store.root, repositoryReviewDatabaseFilename)
	store.openForTest = func(context.Context) (*sql.DB, error) { return sql.Open("sqlite", path) }
	if _, err := store.UpdateProfile(
		t.Context(), profile.ID, profile.Version, func(*RepositoryReviewProfile) error { return nil },
	); err == nil {
		t.Fatal("catalog-error profile update succeeded")
	}
	if err := store.DeleteProfile(t.Context(), profile.ID, profile.Version); err == nil {
		t.Fatal("catalog-error profile delete succeeded")
	}
}

func TestRepositoryReviewSQLiteDetectsRelationshipProjectionTamper(t *testing.T) {
	store := newRepositoryAuditTestStore(t)
	file := repositoryAuditTestFile("pkg/relationship.go", "c", 64)
	recorded := recordRepositoryAuditCoverage(
		t, store, "owner/relationship", "commit-c", "inventory-c", []FileRef{file}, "relationship-run",
	)
	database, err := store.openDatabase(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`
		UPDATE repository_review_records SET status = 'tampered'
		 WHERE state_id = ? AND record_kind = 'run'`, recorded.State.ID); err != nil {
		database.Close()
		t.Fatal(err)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.GetByID(recorded.State.ID); err == nil ||
		!strings.Contains(err.Error(), "relationship projection mismatch") {
		t.Fatalf("relationship tamper error=%v", err)
	}
}

func TestRepositoryReviewSQLitePrimitiveEncodings(t *testing.T) {
	codeTypes := []RepositoryReviewCodeType{
		RepositoryReviewCodeTypeCode,
		RepositoryReviewCodeTypeTest,
	}
	if got := repositoryReviewCodeTypeStrings(codeTypes); !reflect.DeepEqual(
		got, []string{"code", "test"},
	) {
		t.Fatalf("code type strings=%#v", got)
	}
	if reviewBoolInteger(true) != 1 || reviewBoolInteger(false) != 0 {
		t.Fatal("boolean encoding mismatch")
	}
	if id, version := nullableReviewProfile("", 7); id != nil || version != nil {
		t.Fatalf("nil profile encoding=(%#v, %#v)", id, version)
	}
	if id, version := nullableReviewProfile("rrpf_encoding", 7); id != "rrpf_encoding" || version != int64(7) {
		t.Fatalf("profile encoding=(%#v, %#v)", id, version)
	}

	now := time.Date(2026, time.August, 31, 12, 34, 56, 789, time.FixedZone("offset", 3600))
	if nullableReviewTime(time.Time{}) != nil || nullableReviewTime(now) != now.UTC().UnixNano() {
		t.Fatal("time encoding mismatch")
	}
	if !reviewNullTimeMatches(time.Time{}, sql.NullInt64{}) ||
		reviewNullTimeMatches(time.Time{}, sql.NullInt64{Valid: true}) ||
		!reviewNullTimeMatches(now, sql.NullInt64{Int64: now.UTC().UnixNano(), Valid: true}) ||
		reviewNullTimeMatches(now, sql.NullInt64{Int64: now.UTC().UnixNano() + 1, Valid: true}) {
		t.Fatal("nullable time comparison mismatch")
	}
	if !equalReviewStrings([]string{"a", "b"}, []string{"a", "b"}) ||
		equalReviewStrings([]string{"a"}, []string{"a", "b"}) ||
		equalReviewStrings([]string{"a", "b"}, []string{"a", "c"}) {
		t.Fatal("ordered string comparison mismatch")
	}

	database, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	database.SetMaxOpenConns(1)
	defer database.Close()
	if _, err = database.Exec(`CREATE TABLE ordered_values (
		owner TEXT NOT NULL, position INTEGER NOT NULL, value TEXT
	)`); err != nil {
		t.Fatal(err)
	}
	if _, err = database.Exec(`INSERT INTO ordered_values VALUES
		('owner', 1, 'second'), ('owner', 0, 'first')`); err != nil {
		t.Fatal(err)
	}
	values, err := loadReviewOrderedStrings(
		t.Context(), database,
		`SELECT value FROM ordered_values WHERE owner = ? ORDER BY position`, "owner",
	)
	if err != nil || !reflect.DeepEqual(values, []string{"first", "second"}) {
		t.Fatalf("ordered values=%#v err=%v", values, err)
	}
	if _, err = loadReviewOrderedStrings(
		t.Context(), database, `SELECT value FROM missing_values WHERE owner = ?`, "owner",
	); err == nil {
		t.Fatal("missing ordered-value table was accepted")
	}
	if _, err = database.Exec(`INSERT INTO ordered_values VALUES ('null', 0, NULL)`); err != nil {
		t.Fatal(err)
	}
	if _, err = loadReviewOrderedStrings(
		t.Context(), database,
		`SELECT value FROM ordered_values WHERE owner = ? ORDER BY position`, "null",
	); err == nil {
		t.Fatal("NULL ordered value was accepted")
	}
}

func TestRepositoryReviewSQLiteConfigurationImportAndDecoderBoundaries(t *testing.T) {
	profile := profileCoverageFixture("rrpf_config_decoder")
	profile.SchemaVersion = 1
	profile.IssuePrompt = ""
	encodedProfile, err := json.Marshal(profile)
	if err != nil {
		t.Fatal(err)
	}
	var profileFields map[string]json.RawMessage
	_ = json.Unmarshal(encodedProfile, &profileFields)
	delete(profileFields, "deduplication_similarity_threshold")
	delete(profileFields, "deduplication_candidate_limit")
	encodedProfile, _ = json.Marshal(profileFields)
	decodedProfile, err := decodeLegacyRepositoryReviewProfile(profile.ID, encodedProfile)
	if err != nil || decodedProfile.SchemaVersion != RepositoryReviewProfileSchemaVersion ||
		decodedProfile.IssuePrompt != DefaultRepositoryReviewIssuePrompt ||
		decodedProfile.DeduplicationSimilarityThreshold != DeduplicationDefaultThreshold ||
		decodedProfile.DeduplicationCandidateLimit != DeduplicationDefaultCandidateLimit {
		t.Fatalf("decoded profile=%#v err=%v", decodedProfile, err)
	}
	if _, decodeErr := decodeLegacyRepositoryReviewProfile("rrpf_other", encodedProfile); decodeErr == nil {
		t.Fatal("profile identity mismatch decoded")
	}
	if _, decodeErr := decodeLegacyRepositoryReviewProfile(profile.ID, []byte("{")); decodeErr == nil {
		t.Fatal("malformed profile decoded")
	}

	automation := validAutomationForTest("rra_config_decoder", "Configuration decoder")
	automation.SchemaVersion = RepositoryReviewAutomationSchemaVersion
	automation.Version = 1
	automation.Status = RepositoryReviewAutomationIdle
	automation.CreatedAt = automationTestNow
	automation.UpdatedAt = automationTestNow
	if normalizeErr := normalizeAutomation(&automation); normalizeErr != nil {
		t.Fatal(normalizeErr)
	}
	automation.SchemaVersion = 1
	automation.CampaignID = NewRepositoryReviewCampaignID()
	automation.RunIDs = []string{"run-old"}
	encodedAutomation, _ := json.Marshal(automation)
	var automationFields map[string]json.RawMessage
	_ = json.Unmarshal(encodedAutomation, &automationFields)
	delete(automationFields, "deduplication_similarity_threshold")
	delete(automationFields, "deduplication_candidate_limit")
	encodedAutomation, _ = json.Marshal(automationFields)
	decodedAutomation, err := decodeLegacyRepositoryReviewAutomation(automation.ID, encodedAutomation)
	if err != nil || decodedAutomation.SchemaVersion != RepositoryReviewAutomationSchemaVersion ||
		decodedAutomation.DeduplicationSimilarityThreshold != DeduplicationDefaultThreshold ||
		decodedAutomation.DeduplicationCandidateLimit != DeduplicationDefaultCandidateLimit ||
		!repositoryReviewAutomationHistoryReset(decodedAutomation) {
		t.Fatalf("decoded automation=%#v err=%v", decodedAutomation, err)
	}
	if _, decodeErr := decodeLegacyRepositoryReviewAutomation("rra_other", encodedAutomation); decodeErr == nil {
		t.Fatal("automation identity mismatch decoded")
	}
	if _, decodeErr := decodeLegacyRepositoryReviewAutomation(automation.ID, []byte("{")); decodeErr == nil {
		t.Fatal("malformed automation decoded")
	}
	automationFields["name"] = json.RawMessage(`""`)
	invalidAutomation, _ := json.Marshal(automationFields)
	if _, decodeErr := decodeLegacyRepositoryReviewAutomation(automation.ID, invalidAutomation); decodeErr == nil {
		t.Fatal("invalid automation decoded")
	}

	store := newRepositoryAuditTestStore(t)
	database, err := store.openDatabase(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	conn, err := database.Conn(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	legacyInput := func(relative string, value any) sqlitestore.LegacyInput {
		data, marshalErr := json.Marshal(value)
		if marshalErr != nil {
			t.Fatal(marshalErr)
		}
		return sqlitestore.LegacyInput{
			Relative: relative, Data: data, Digest: sha256.Sum256(data),
		}
	}
	importProfile := profileCoverageFixture("rrpf_config_import")
	if result, err := importLegacyRepositoryReviewSource(
		t.Context(), conn, legacyInput(profileFilename(importProfile.ID), importProfile),
	); err != nil || result.Imported != 1 {
		t.Fatalf("profile import=%#v err=%v", result, err)
	}
	if result, err := importLegacyRepositoryReviewSource(
		t.Context(), conn, legacyInput(profileFilename(importProfile.ID), importProfile),
	); err != nil || result.Skipped != 1 || result.Issues[0].Code != "duplicate_identity" {
		t.Fatalf("duplicate profile import=%#v err=%v", result, err)
	}
	invalidProfile := importProfile
	invalidProfile.ID = "rrpf_invalid_config_import"
	invalidProfile.Name = ""
	if result, err := importLegacyRepositoryReviewSource(
		t.Context(), conn, legacyInput(profileFilename(invalidProfile.ID), invalidProfile),
	); err != nil || result.Skipped != 1 || result.Issues[0].Code != "invalid_profile" {
		t.Fatalf("invalid profile import=%#v err=%v", result, err)
	}
	brokenAutomation := validAutomationForTest("rra_broken_config_profile", "Broken profile")
	brokenAutomation.SchemaVersion = RepositoryReviewAutomationSchemaVersion
	brokenAutomation.Version = 1
	brokenAutomation.Status = RepositoryReviewAutomationIdle
	brokenAutomation.CreatedAt = automationTestNow
	brokenAutomation.UpdatedAt = automationTestNow
	brokenAutomation.ProfileID = "rrpf_absent"
	brokenAutomation.ProfileVersion = 1
	brokenAutomation.Target = "all"
	brokenAutomation.ReviewerModels = []string{"review-a"}
	brokenAutomation.CompareModels = false
	delete(brokenAutomation.ModelPrices, "review-b")
	if err := normalizeAutomation(&brokenAutomation); err != nil {
		t.Fatal(err)
	}
	if result, err := importLegacyRepositoryReviewSource(
		t.Context(), conn, legacyInput(automationFilename(brokenAutomation.ID), brokenAutomation),
	); err != nil || result.Skipped != 1 || result.Issues[0].Code != "broken_profile_reference" {
		t.Fatalf("broken automation import=%#v err=%v", result, err)
	}
	importAutomation := decodedAutomation
	importAutomation.ID = "rra_config_import"
	if result, err := importLegacyRepositoryReviewSource(
		t.Context(), conn, legacyInput(automationFilename(importAutomation.ID), importAutomation),
	); err != nil || result.Imported != 1 {
		t.Fatalf("automation import=%#v err=%v", result, err)
	}
	if result, err := importLegacyRepositoryReviewSource(
		t.Context(), conn, legacyInput("unknown.json", map[string]any{}),
	); err != nil || result.Skipped != 1 || result.Issues[0].Code != "unknown_source" {
		t.Fatalf("unknown import=%#v err=%v", result, err)
	}
	if err := conn.Close(); err != nil {
		t.Fatal(err)
	}
	closedProfile := profileCoverageFixture("rrpf_closed_config_import")
	if _, err := importLegacyRepositoryReviewSource(
		t.Context(), conn, legacyInput(profileFilename(closedProfile.ID), closedProfile),
	); err == nil {
		t.Fatal("closed profile import connection succeeded")
	}
	closedAutomation := decodedAutomation
	closedAutomation.ID = "rra_closed_config_import"
	if _, err := importLegacyRepositoryReviewSource(
		t.Context(), conn, legacyInput(automationFilename(closedAutomation.ID), closedAutomation),
	); err == nil {
		t.Fatal("closed automation import connection succeeded")
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}
}
