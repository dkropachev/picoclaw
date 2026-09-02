package repoeval

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

	"github.com/sipeed/picoclaw/pkg/sqlitestore"
)

//nolint:govet // Boundary tests intentionally keep setup and assertion errors in local scopes.
func TestSQLiteStoreSchemaConfigurationAndReopen(t *testing.T) {
	store := newEvaluationTestStore(t, 700)
	created, err := store.Create(t.Context(), validCreateRequest())
	if err != nil {
		t.Fatal(err)
	}
	database, err := store.open(t.Context())
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
	if version != 1 || foreignKeys != 1 || synchronous != 2 || journal != "wal" {
		t.Fatalf("SQLite configuration version=%d fk=%d sync=%d journal=%q", version, foreignKeys, synchronous, journal)
	}
	if err := validateEvaluationDatabaseSchema(t.Context(), mustEvaluationConn(t, database)); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(store.databasePath())
	if err != nil {
		t.Fatal(err)
	}
	if !repositoryEvaluationPermissionsSafe(0o644) && info.Mode().Perm() != 0o600 {
		t.Fatalf("database mode=%o", info.Mode().Perm())
	}
	loaded, found, err := NewSQLiteStore(store.workspace).Get(t.Context(), created.ID)
	if err != nil || !found || !reflect.DeepEqual(loaded, created) {
		t.Fatalf("reopen loaded=%#v found=%v err=%v", loaded, found, err)
	}
}

func TestSQLiteLegacyEnumerationAndImporterBoundaries(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "missing")
	if sources, err := legacyEvaluationSources(missing); err != nil || sources != nil {
		t.Fatalf("missing sources=%#v err=%v", sources, err)
	}
	fileRoot := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(fileRoot, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := legacyEvaluationSources(fileRoot); err == nil {
		t.Fatal("file root was enumerated")
	}

	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "README"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	matching := stateNamePrefix + testEvaluationID(800) + stateFileSuffix
	if err := os.Mkdir(filepath.Join(root, matching), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := legacyEvaluationSources(root); err == nil {
		t.Fatal("matching directory was accepted")
	}
	if err := os.Remove(filepath.Join(root, matching)); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(root, "README"), filepath.Join(root, matching)); err == nil {
		if _, err := legacyEvaluationSources(root); err == nil {
			t.Fatal("legacy symlink was accepted")
		}
	}

	store := newEvaluationTestStore(t, 801)
	valid, err := store.Create(t.Context(), validCreateRequest())
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(valid)
	if err != nil {
		t.Fatal(err)
	}
	database, err := store.open(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	conn, err := database.Conn(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	input := sqlitestore.LegacyInput{
		Relative: stateNamePrefix + valid.ID + stateFileSuffix,
		Data:     encoded,
		Digest:   sha256.Sum256(encoded),
	}
	if result, err := importLegacyEvaluation(t.Context(), conn, input); err != nil ||
		result.Skipped != 1 || result.Issues[0].Code != "duplicate_identity" {
		t.Fatalf("duplicate import=%#v err=%v", result, err)
	}
	badIdentity := valid
	badIdentity.ID = testEvaluationID(802)
	badData, _ := json.Marshal(badIdentity)
	input.Data = badData
	input.Digest = sha256.Sum256(badData)
	if result, err := importLegacyEvaluation(t.Context(), conn, input); err != nil ||
		result.Issues[0].Code != "invalid_identity" {
		t.Fatalf("identity import=%#v err=%v", result, err)
	}
	badRecord := valid
	badRecord.Status = "future"
	badData, _ = json.Marshal(badRecord)
	input.Relative = stateNamePrefix + badRecord.ID + stateFileSuffix
	input.Data = badData
	input.Digest = sha256.Sum256(badData)
	if result, err := importLegacyEvaluation(t.Context(), conn, input); err != nil ||
		result.Issues[0].Code != "invalid_record" {
		t.Fatalf("record import=%#v err=%v", result, err)
	}
	if err := conn.Close(); err != nil {
		t.Fatal(err)
	}
	input.Relative = stateNamePrefix + valid.ID + stateFileSuffix
	input.Data = encoded
	input.Digest = sha256.Sum256(encoded)
	if _, err := importLegacyEvaluation(t.Context(), conn, input); err == nil {
		t.Fatal("closed importer connection succeeded")
	}
	_ = database.Close()
}

//nolint:govet // Boundary tests intentionally keep setup and assertion errors in local scopes.
func TestSQLitePayloadRelationshipAndHelperFailures(t *testing.T) {
	t.Run("closed schema connection", func(t *testing.T) {
		store := newEvaluationTestStore(t, 810)
		database, err := store.open(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		conn, err := database.Conn(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		_ = conn.Close()
		if err := validateEvaluationDatabaseSchema(t.Context(), conn); err == nil {
			t.Fatal("closed schema connection validated")
		}
		if err := validateEvaluationSchemaObjectSet(t.Context(), conn); err == nil {
			t.Fatal("closed schema object-set connection validated")
		}
		_ = database.Close()
	})

	for name, statement := range map[string]string{
		"payload":     `UPDATE repository_evaluations SET payload_json = X'7B'`,
		"progress":    `UPDATE repository_evaluations SET progress_percent = 37`,
		"profile":     `UPDATE repository_evaluations SET profile_id = 'rrpf_wrong', profile_version = 1`,
		"models":      `DELETE FROM repository_evaluation_models WHERE position = 0`,
		"runs":        `UPDATE repository_evaluation_runs SET position = 3 WHERE position = 0`,
		"model table": `DROP TABLE repository_evaluation_models`,
		"model scan": `DROP TABLE repository_evaluation_models;
			CREATE TABLE repository_evaluation_models (evaluation_id, position, model_alias);
			INSERT INTO repository_evaluation_models VALUES ('` + testEvaluationID(820) + `', 0, NULL)`,
		"run table": `DROP TABLE repository_evaluation_runs`,
		"run scan": `DROP TABLE repository_evaluation_runs;
			CREATE TABLE repository_evaluation_runs (evaluation_id, position, run_id);
			INSERT INTO repository_evaluation_runs VALUES ('` + testEvaluationID(820) + `', 0, NULL)`,
	} {
		t.Run(name, func(t *testing.T) {
			store := newEvaluationTestStore(t, 820)
			request := validCreateRequest()
			request.OneShot = true
			request.InitialRunID = "run-tamper"
			created, err := store.Create(t.Context(), request)
			if err != nil {
				t.Fatal(err)
			}
			database, err := store.open(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			if _, err := database.Exec(statement); err != nil {
				database.Close()
				t.Fatal(err)
			}
			if name == "runs" {
				conn, connErr := database.Conn(t.Context())
				if connErr != nil {
					database.Close()
					t.Fatal(connErr)
				}
				err = validateEvaluationDatabaseSchema(t.Context(), conn)
				_ = conn.Close()
			} else {
				_, err = loadEvaluation(t.Context(), database, created.ID)
			}
			if err == nil {
				t.Fatalf("%s tamper loaded", name)
			}
			_ = database.Close()
		})
	}

	store := newEvaluationTestStore(t, 830)
	created, err := store.Create(t.Context(), validCreateRequest())
	if err != nil {
		t.Fatal(err)
	}
	database, err := store.open(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	conn, err := database.Conn(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	_ = conn.Close()
	if _, err := insertEvaluationConn(t.Context(), conn, created, true); err == nil {
		t.Fatal("closed insert connection succeeded")
	}
	if err := classifyEvaluationUpdateMiss(t.Context(), conn, created.ID); err == nil {
		t.Fatal("closed update-miss query succeeded")
	}
	_ = database.Close()

	oversized := created
	oversized.Warnings = []string{strings.Repeat("x", int(maxStateFileBytes)+1)}
	if _, err := encodeEvaluationPayload(oversized); err == nil {
		t.Fatal("oversized payload encoded")
	}
	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := store.exists(canceled, created.ID); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled exists error=%v", err)
	}
}

//nolint:govet // Boundary tests intentionally keep setup and assertion errors in local scopes.
func TestSQLitePathSchemaAndAggregateQueryFailures(t *testing.T) {
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
		_, openErr := (Store{root: "relative"}).open(t.Context())
		if err := os.Chdir(original); err != nil {
			t.Fatal(err)
		}
		if openErr == nil {
			t.Fatal("unresolvable evaluation path opened")
		}
	}
	store := newEvaluationTestStore(t, 940)
	if _, err := store.Create(t.Context(), validCreateRequest()); err != nil {
		t.Fatal(err)
	}
	database, err := store.open(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(
		`CREATE UNIQUE INDEX unexpected_evaluation_unique ON repository_evaluations(repository)`,
	); err != nil {
		database.Close()
		t.Fatal(err)
	}
	conn, err := database.Conn(t.Context())
	if err != nil {
		database.Close()
		t.Fatal(err)
	}
	if err := validateEvaluationDatabaseSchema(t.Context(), conn); err == nil {
		t.Fatal("unexpected evaluation unique index validated")
	}
	_ = conn.Close()
	conn, err = database.Conn(t.Context())
	if err != nil {
		database.Close()
		t.Fatal(err)
	}
	_ = conn.Close()
	if err := validateEvaluationAggregateRows(t.Context(), conn); err == nil {
		t.Fatal("closed aggregate connection validated")
	}
	_ = database.Close()
}

func TestSQLiteStoreFacadesPropagateDatabaseFailure(t *testing.T) {
	newFixture := func(t *testing.T) (Store, Evaluation) {
		t.Helper()
		store := newEvaluationTestStore(t, 900)
		created, err := store.Create(t.Context(), validCreateRequest())
		if err != nil {
			t.Fatal(err)
		}
		path := store.databasePath()
		if err := os.Remove(path); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("not-sqlite"), 0o600); err != nil {
			t.Fatal(err)
		}
		return store, created
	}
	for name, call := range map[string]func(*testing.T, Store, Evaluation) error{
		"create": func(t *testing.T, store Store, _ Evaluation) error {
			_, err := store.Create(t.Context(), validCreateRequest())
			return err
		},
		"get": func(t *testing.T, store Store, value Evaluation) error {
			_, _, err := store.Get(t.Context(), value.ID)
			return err
		},
		"list": func(t *testing.T, store Store, _ Evaluation) error { _, err := store.List(t.Context()); return err },
		"update": func(t *testing.T, store Store, value Evaluation) error {
			_, err := store.Update(t.Context(), value.ID, value.Version, func(*Evaluation) error { return nil })
			return err
		},
		"delete": func(t *testing.T, store Store, value Evaluation) error {
			return store.Delete(t.Context(), value.ID, value.Version)
		},
		"bulk": func(t *testing.T, store Store, value Evaluation) error {
			_, err := store.BulkDelete(t.Context(), []BulkDeleteItem{{ID: value.ID, Version: value.Version}})
			return err
		},
		"load":  func(_ *testing.T, store Store, value Evaluation) error { _, err := store.load(value.ID); return err },
		"save":  func(_ *testing.T, store Store, value Evaluation) error { return store.save(value, false) },
		"count": func(_ *testing.T, store Store, _ Evaluation) error { _, err := store.stateCount(); return err },
		"exists": func(t *testing.T, store Store, value Evaluation) error {
			_, err := store.exists(t.Context(), value.ID)
			return err
		},
	} {
		t.Run(name, func(t *testing.T) {
			store, created := newFixture(t)
			if err := call(t, store, created); err == nil {
				t.Fatalf("%s accepted corrupt database", name)
			}
		})
	}
}

func TestSQLiteStoreFacadesPropagateSemanticPayloadFailure(t *testing.T) {
	store := newEvaluationTestStore(t, 920)
	created, err := store.Create(t.Context(), validCreateRequest())
	if err != nil {
		t.Fatal(err)
	}
	database, err := store.open(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	var payload []byte
	if err := database.QueryRow(
		`SELECT payload_json FROM repository_evaluations WHERE evaluation_id = ?`, created.ID,
	).Scan(&payload); err != nil {
		t.Fatal(err)
	}
	var decoded evaluationPayload
	_ = json.Unmarshal(payload, &decoded)
	decoded.DefaultFilesPerLanguage = -1
	payload, _ = json.Marshal(decoded)
	if _, err := database.Exec(
		`UPDATE repository_evaluations SET payload_json = ? WHERE evaluation_id = ?`, payload, created.ID,
	); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec("PRAGMA wal_checkpoint(TRUNCATE)"); err != nil {
		t.Fatal(err)
	}
	_ = database.Close()
	path := store.databasePath()
	store.openForTest = func(context.Context) (*sql.DB, error) { return sql.Open("sqlite", path) }
	if _, _, err := store.Get(t.Context(), created.ID); err == nil {
		t.Fatal("semantic Get succeeded")
	}
	if _, err := store.List(t.Context()); err == nil {
		t.Fatal("semantic List succeeded")
	}
	if _, err := store.Update(
		t.Context(), created.ID, created.Version, func(*Evaluation) error { return nil },
	); err == nil {
		t.Fatal("semantic Update succeeded")
	}
	if err := store.Delete(t.Context(), created.ID, created.Version); err == nil {
		t.Fatal("semantic Delete succeeded")
	}
	if _, err := store.BulkDelete(
		t.Context(), []BulkDeleteItem{{ID: created.ID, Version: created.Version}},
	); err == nil {
		t.Fatal("semantic BulkDelete succeeded")
	}
}

func TestSQLiteStoreListQueryScanAndBoundaries(t *testing.T) {
	for name, setup := range map[string]string{
		"query": "",
		"scan":  `CREATE TABLE repository_evaluations (evaluation_id, updated_at_unix_nano); INSERT INTO repository_evaluations VALUES (NULL, 0)`,
	} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "raw.db")
			if setup != "" {
				database, err := sql.Open("sqlite", path)
				if err != nil {
					t.Fatal(err)
				}
				if _, err := database.Exec(setup); err != nil {
					database.Close()
					t.Fatal(err)
				}
				_ = database.Close()
			}
			store := NewSQLiteStore(t.TempDir())
			store.openForTest = func(context.Context) (*sql.DB, error) { return sql.Open("sqlite", path) }
			if _, err := store.List(t.Context()); err == nil {
				t.Fatalf("%s list succeeded", name)
			}
		})
	}
	store := newEvaluationTestStore(t, 930)
	if _, err := store.Create(t.Context(), validCreateRequest()); err != nil {
		t.Fatal(err)
	}
	if _, err := store.list(t.Context(), 0); err == nil {
		t.Fatal("zero-bound list succeeded")
	}
}

func TestSQLiteStoreCreateDeleteAndHelperBoundaries(t *testing.T) {
	t.Run("invalid constructed timestamp", func(t *testing.T) {
		store := newEvaluationTestStore(t, 949)
		store.now = func() time.Time { return time.Time{} }
		if _, err := store.Create(t.Context(), validCreateRequest()); !errors.Is(err, ErrInvalidEvaluation) {
			t.Fatalf("invalid timestamp create=%v", err)
		}
	})
	t.Run("catalog limit", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "raw.db")
		database, err := sql.Open("sqlite", path)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := database.Exec(`CREATE TABLE repository_evaluations (evaluation_id);
			WITH RECURSIVE numbers(value) AS (SELECT 1 UNION ALL SELECT value + 1 FROM numbers WHERE value < 10000)
			INSERT INTO repository_evaluations SELECT printf('rme_%032x', value) FROM numbers`); err != nil {
			database.Close()
			t.Fatal(err)
		}
		_ = database.Close()
		store := newEvaluationTestStore(t, 950)
		store.openForTest = func(context.Context) (*sql.DB, error) { return sql.Open("sqlite", path) }
		if _, err := store.Create(t.Context(), validCreateRequest()); err == nil ||
			!strings.Contains(err.Error(), "catalog") {
			t.Fatalf("catalog limit error=%v", err)
		}
	})
	for _, failureCall := range []int{2, 3} {
		t.Run(fmt.Sprintf("create open failure %d", failureCall), func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "raw.db")
			database, err := sql.Open("sqlite", path)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := database.Exec(`CREATE TABLE repository_evaluations (evaluation_id)`); err != nil {
				database.Close()
				t.Fatal(err)
			}
			_ = database.Close()
			store := newEvaluationTestStore(t, 951)
			calls := 0
			store.openForTest = func(context.Context) (*sql.DB, error) {
				calls++
				if calls == failureCall {
					return nil, errors.New("injected open failure")
				}
				return sql.Open("sqlite", path)
			}
			if _, err := store.Create(t.Context(), validCreateRequest()); err == nil {
				t.Fatal("create open failure ignored")
			}
		})
	}

	for _, mode := range []string{"open", "reject", "ignore"} {
		t.Run("delete "+mode, func(t *testing.T) {
			store := newEvaluationTestStore(t, 960)
			created, err := store.Create(t.Context(), validCreateRequest())
			if err != nil {
				t.Fatal(err)
			}
			path := store.databasePath()
			if mode != "open" {
				database, err := sql.Open("sqlite", path)
				if err != nil {
					t.Fatal(err)
				}
				action := "FAIL, 'reject'"
				if mode == "ignore" {
					action = "IGNORE"
				}
				if _, err := database.Exec(fmt.Sprintf(
					`CREATE TRIGGER delete_boundary BEFORE DELETE ON repository_evaluations BEGIN SELECT RAISE(%s); END`,
					action,
				)); err != nil {
					database.Close()
					t.Fatal(err)
				}
				_ = database.Close()
			}
			calls := 0
			store.openForTest = func(context.Context) (*sql.DB, error) {
				calls++
				if mode == "open" && calls == 2 {
					return nil, errors.New("open failed")
				}
				return sql.Open("sqlite", path)
			}
			if err := store.Delete(t.Context(), created.ID, created.Version); err == nil {
				t.Fatal("delete boundary succeeded")
			}
		})
	}

	if (Store{}).clock().IsZero() || (Store{}).idGenerator() == nil {
		t.Fatal("zero store helpers unavailable")
	}
	originalRead := repositoryEvaluationRandRead
	repositoryEvaluationRandRead = func([]byte) (int, error) { return 0, errors.New("entropy") }
	if _, err := randomEvaluationID(); err == nil {
		t.Fatal("entropy error ignored")
	}
	repositoryEvaluationRandRead = originalRead
}

func TestSQLiteStoreTransactionAndRelationshipFailures(t *testing.T) {
	store := newEvaluationTestStore(t, 970)
	created, err := store.Create(t.Context(), validCreateRequest())
	if err != nil {
		t.Fatal(err)
	}
	database, err := store.open(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if err := saveEvaluation(t.Context(), database, created, false); !errors.Is(err, ErrConflict) {
		t.Fatalf("same-version save=%v", err)
	}
	invalidTime := created
	invalidTime.Progress.UpdatedAt = time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC)
	if _, err := encodeEvaluationPayload(invalidTime); err == nil {
		t.Fatal("unencodable evaluation payload encoded")
	}
	_ = database.Close()

	for name, setup := range map[string]func(*testing.T, Store, *sql.DB, *sql.Conn, Evaluation){
		"model delete": func(t *testing.T, _ Store, database *sql.DB, conn *sql.Conn, value Evaluation) {
			if _, err := database.Exec("DROP TABLE repository_evaluation_models"); err != nil {
				t.Fatal(err)
			}
			if _, err := insertEvaluationConn(t.Context(), conn, value, false); err == nil {
				t.Fatal("model delete failure ignored")
			}
		},
		"model insert": func(t *testing.T, _ Store, database *sql.DB, conn *sql.Conn, value Evaluation) {
			if _, err := database.Exec(`CREATE TRIGGER reject_models BEFORE INSERT ON repository_evaluation_models BEGIN SELECT RAISE(FAIL, 'reject'); END`); err != nil {
				t.Fatal(err)
			}
			if _, err := insertEvaluationConn(t.Context(), conn, value, false); err == nil {
				t.Fatal("model insert failure ignored")
			}
		},
		"run delete": func(t *testing.T, _ Store, database *sql.DB, conn *sql.Conn, value Evaluation) {
			if _, err := database.Exec("DROP TABLE repository_evaluation_runs"); err != nil {
				t.Fatal(err)
			}
			if _, err := insertEvaluationConn(t.Context(), conn, value, false); err == nil {
				t.Fatal("run delete failure ignored")
			}
		},
		"run insert": func(t *testing.T, _ Store, database *sql.DB, conn *sql.Conn, value Evaluation) {
			value.RunIDs = []string{"run"}
			if _, err := database.Exec(`CREATE TRIGGER reject_runs BEFORE INSERT ON repository_evaluation_runs BEGIN SELECT RAISE(FAIL, 'reject'); END`); err != nil {
				t.Fatal(err)
			}
			if _, err := insertEvaluationConn(t.Context(), conn, value, false); err == nil {
				t.Fatal("run insert failure ignored")
			}
		},
	} {
		t.Run(name, func(t *testing.T) {
			store := newEvaluationTestStore(t, 980)
			value, err := store.Create(t.Context(), validCreateRequest())
			if err != nil {
				t.Fatal(err)
			}
			value.Version++
			database, err := store.open(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			conn, err := database.Conn(t.Context())
			if err != nil {
				database.Close()
				t.Fatal(err)
			}
			setup(t, store, database, conn, value)
			_ = conn.Close()
			_ = database.Close()
		})
	}
}

func TestSQLiteStoreBulkDeleteFailureAndCancellation(t *testing.T) {
	for _, mode := range []string{"reject", "ignore"} {
		t.Run(mode, func(t *testing.T) {
			store := newEvaluationTestStore(t, 990)
			created, err := store.Create(t.Context(), validCreateRequest())
			if err != nil {
				t.Fatal(err)
			}
			database, err := sql.Open("sqlite", store.databasePath())
			if err != nil {
				t.Fatal(err)
			}
			action := "FAIL, 'reject'"
			if mode == "ignore" {
				action = "IGNORE"
			}
			if _, err := database.Exec(fmt.Sprintf(
				`CREATE TRIGGER bulk_delete_boundary BEFORE DELETE ON repository_evaluations BEGIN SELECT RAISE(%s); END`,
				action,
			)); err != nil {
				database.Close()
				t.Fatal(err)
			}
			_ = database.Close()
			// This test deliberately installs a trigger that the production open
			// path correctly rejects as an unexpected schema object. Reopen the
			// already-created fixture directly so BulkDelete reaches the injected
			// DELETE boundary being exercised here.
			store.openForTest = func(context.Context) (*sql.DB, error) {
				return sql.Open("sqlite", store.databasePath())
			}
			if _, err := store.BulkDelete(
				t.Context(), []BulkDeleteItem{{ID: created.ID, Version: created.Version}},
			); err == nil {
				t.Fatal("bulk delete boundary succeeded")
			}
		})
	}
	store := newEvaluationTestStore(t, 995)
	created, err := store.Create(t.Context(), validCreateRequest())
	if err != nil {
		t.Fatal(err)
	}
	ctx := &stagedBulkDeleteContext{cancelOn: 3}
	if _, err := store.BulkDelete(ctx, []BulkDeleteItem{{
		ID: created.ID, Version: created.Version,
	}}); !errors.Is(err, context.Canceled) {
		t.Fatalf("bulk cancellation=%v calls=%d", err, ctx.calls)
	}
	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := store.Update(
		canceled,
		created.ID,
		created.Version,
		func(*Evaluation) error { return nil },
	); !errors.Is(
		err,
		context.Canceled,
	) {
		t.Fatalf("canceled update=%v", err)
	}
	for cancelOn := 2; cancelOn <= 12; cancelOn++ {
		candidateStore := newEvaluationTestStore(t, 1000+cancelOn)
		candidate, err := candidateStore.Create(t.Context(), validCreateRequest())
		if err != nil {
			t.Fatal(err)
		}
		_, _ = candidateStore.BulkDelete(
			&stagedBulkDeleteContext{cancelOn: cancelOn},
			[]BulkDeleteItem{{ID: candidate.ID, Version: candidate.Version}},
		)
	}
	if _, err := store.load("invalid"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("invalid load=%v", err)
	}

	queryStore := NewSQLiteStore(t.TempDir())
	path := filepath.Join(t.TempDir(), "raw.db")
	queryStore.openForTest = func(context.Context) (*sql.DB, error) { return sql.Open("sqlite", path) }
	if _, err := queryStore.stateCount(); err == nil {
		t.Fatal("state count query failure ignored")
	}
}

//nolint:govet // Boundary tests intentionally keep setup and assertion errors in local scopes.
func TestSQLiteStoreMigratesAndArchivesLegacyEvaluations(t *testing.T) {
	seed := newEvaluationTestStore(t, 710)
	created, err := seed.Create(t.Context(), validCreateRequest())
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(created)
	if err != nil {
		t.Fatal(err)
	}

	workspace := t.TempDir()
	root := filepath.Join(workspace, storeDirectory)
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	legacyName := stateNamePrefix + created.ID + stateFileSuffix
	legacyPath := filepath.Join(root, legacyName)
	if err := os.WriteFile(legacyPath, encoded, 0o600); err != nil {
		t.Fatal(err)
	}
	malformedPath := filepath.Join(root, stateNamePrefix+testEvaluationID(711)+stateFileSuffix)
	if err := os.WriteFile(malformedPath, []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}

	store := NewSQLiteStore(workspace)
	loaded, found, err := store.Get(t.Context(), created.ID)
	if err != nil || !found || !reflect.DeepEqual(loaded, created) {
		t.Fatalf("migrated loaded=%#v found=%v err=%v", loaded, found, err)
	}
	for _, path := range []string{legacyPath, malformedPath} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("legacy source remains: %s err=%v", path, err)
		}
		archive := filepath.Join(root, "legacy-json", evaluationLegacyArchiveLabel, filepath.Base(path))
		if _, err := os.Stat(archive); err != nil {
			t.Fatalf("archive %s: %v", archive, err)
		}
	}
	database, err := store.open(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	var imported, skipped, complete int
	if err := database.QueryRow(`
		SELECT COALESCE(SUM(imported_count), 0), COALESCE(SUM(skipped_count), 0),
		       COUNT(*) FILTER (WHERE archive_status = 'complete')
		  FROM storage_imports WHERE component = ?`, evaluationDatabaseComponent,
	).Scan(&imported, &skipped, &complete); err != nil {
		t.Fatal(err)
	}
	if imported != 1 || skipped != 1 || complete != 2 {
		t.Fatalf("import accounting imported=%d skipped=%d complete=%d", imported, skipped, complete)
	}
	listed, err := NewSQLiteStore(workspace).List(t.Context())
	if err != nil || len(listed) != 1 || listed[0].ID != created.ID {
		t.Fatalf("idempotent reopen list=%#v err=%v", listed, err)
	}
}

//nolint:govet // Boundary tests intentionally keep setup and assertion errors in local scopes.
func TestSQLiteStoreRejectsTooNewAndTamperedSchema(t *testing.T) {
	for name, mutate := range map[string]func(*testing.T, *sql.DB){
		"too new": func(t *testing.T, database *sql.DB) {
			if _, err := database.Exec("PRAGMA user_version = 2"); err != nil {
				t.Fatal(err)
			}
		},
		"schema": func(t *testing.T, database *sql.DB) {
			if _, err := database.Exec("DROP INDEX repository_evaluations_status_idx"); err != nil {
				t.Fatal(err)
			}
		},
		"rogue table": func(t *testing.T, database *sql.DB) {
			if _, err := database.Exec(`CREATE TABLE rogue_evaluation_table(id INTEGER)`); err != nil {
				t.Fatal(err)
			}
		},
		"rogue view": func(t *testing.T, database *sql.DB) {
			if _, err := database.Exec(`CREATE VIEW rogue_evaluation_view AS
				SELECT evaluation_id FROM repository_evaluations`); err != nil {
				t.Fatal(err)
			}
		},
		"rogue index": func(t *testing.T, database *sql.DB) {
			if _, err := database.Exec(`CREATE INDEX rogue_evaluation_index
				ON repository_evaluations(ref)`); err != nil {
				t.Fatal(err)
			}
		},
		"rogue trigger": func(t *testing.T, database *sql.DB) {
			if _, err := database.Exec(`CREATE TRIGGER rogue_evaluation_trigger
				AFTER INSERT ON repository_evaluations BEGIN SELECT 1; END`); err != nil {
				t.Fatal(err)
			}
		},
		"relationships": func(t *testing.T, database *sql.DB) {
			if _, err := database.Exec(`DELETE FROM repository_evaluation_models WHERE position = 0`); err != nil {
				t.Fatal(err)
			}
		},
	} {
		t.Run(name, func(t *testing.T) {
			store := newEvaluationTestStore(t, 720)
			if _, err := store.Create(t.Context(), validCreateRequest()); err != nil {
				t.Fatal(err)
			}
			database, err := sql.Open("sqlite", store.databasePath())
			if err != nil {
				t.Fatal(err)
			}
			mutate(t, database)
			if err := database.Close(); err != nil {
				t.Fatal(err)
			}
			_, err = store.List(t.Context())
			if name == "too new" && !errors.Is(err, sqlitestore.ErrTooNew) {
				t.Fatalf("too-new error=%v", err)
			}
			if name != "too new" && !errors.Is(err, sqlitestore.ErrInvalidSchema) {
				t.Fatalf("schema error=%v", err)
			}
		})
	}
}

func (s Store) databasePath() string {
	return filepath.Join(s.root, evaluationDatabaseFilename)
}

func mustEvaluationConn(t *testing.T, database *sql.DB) *sql.Conn {
	t.Helper()
	conn, err := database.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return conn
}
