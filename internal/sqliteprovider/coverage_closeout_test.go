//nolint:govet // Independent failure-boundary assertions intentionally reuse narrow error names.
package sqliteprovider

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

const providerScriptDriverName = "picoclaw-provider-coverage-script"

var (
	providerScriptRegister sync.Once
	providerScriptID       atomic.Uint64
	providerScripts        sync.Map
)

type providerScript struct {
	mu       sync.Mutex
	steps    []providerScriptStep
	closeErr error
}

type providerScriptStep struct {
	query   string
	columns []string
	rows    [][]driver.Value
	err     error
	rowsErr error
}

type providerScriptDriver struct{}

func (providerScriptDriver) Open(name string) (driver.Conn, error) {
	value, ok := providerScripts.Load(name)
	if !ok {
		return nil, errors.New("provider coverage script is missing")
	}
	return &providerScriptConn{script: value.(*providerScript)}, nil
}

type providerScriptConn struct{ script *providerScript }

func (connection *providerScriptConn) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("provider coverage prepared statements are unsupported")
}

func (connection *providerScriptConn) Begin() (driver.Tx, error) {
	return nil, errors.New("provider coverage transactions are unsupported")
}
func (connection *providerScriptConn) Close() error { return connection.script.closeErr }
func (connection *providerScriptConn) Ping(context.Context) error {
	return nil
}

func (connection *providerScriptConn) QueryContext(
	_ context.Context,
	query string,
	_ []driver.NamedValue,
) (driver.Rows, error) {
	step, err := connection.next(query)
	if err != nil || step.err != nil {
		if err != nil {
			return nil, err
		}
		return nil, step.err
	}
	return &providerScriptRows{
		columns: step.columns,
		rows:    step.rows,
		final:   step.rowsErr,
	}, nil
}

func (connection *providerScriptConn) ExecContext(
	_ context.Context,
	query string,
	_ []driver.NamedValue,
) (driver.Result, error) {
	step, err := connection.next(query)
	if err != nil || step.err != nil {
		if err != nil {
			return nil, err
		}
		return nil, step.err
	}
	return driver.RowsAffected(1), nil
}

func (connection *providerScriptConn) next(query string) (providerScriptStep, error) {
	connection.script.mu.Lock()
	defer connection.script.mu.Unlock()
	if len(connection.script.steps) == 0 {
		return providerScriptStep{}, errors.New("provider coverage script exhausted")
	}
	step := connection.script.steps[0]
	connection.script.steps = connection.script.steps[1:]
	if step.query != "" && !strings.Contains(query, step.query) {
		return providerScriptStep{}, errors.New("provider coverage query mismatch")
	}
	return step, nil
}

type providerScriptRows struct {
	columns []string
	rows    [][]driver.Value
	index   int
	final   error
}

func (rows *providerScriptRows) Columns() []string { return rows.columns }
func (rows *providerScriptRows) Close() error      { return nil }
func (rows *providerScriptRows) Next(destination []driver.Value) error {
	if rows.index < len(rows.rows) {
		copy(destination, rows.rows[rows.index])
		rows.index++
		return nil
	}
	if rows.final != nil {
		err := rows.final
		rows.final = nil
		return err
	}
	return io.EOF
}

func openProviderScript(t *testing.T, steps ...providerScriptStep) *sql.DB {
	t.Helper()
	providerScriptRegister.Do(func() { sql.Register(providerScriptDriverName, providerScriptDriver{}) })
	name := "script-" + time.Now().Format("150405.000000000") + "-" +
		string(rune(providerScriptID.Add(1)))
	script := &providerScript{steps: append([]providerScriptStep(nil), steps...)}
	providerScripts.Store(name, script)
	t.Cleanup(func() { providerScripts.Delete(name) })
	database, err := sql.Open(providerScriptDriverName, name)
	if err != nil {
		t.Fatal(err)
	}
	database.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = database.Close() })
	return database
}

func providerRow(query string, values ...driver.Value) providerScriptStep {
	columns := make([]string, len(values))
	for index := range columns {
		columns[index] = "value"
	}
	return providerScriptStep{query: query, columns: columns, rows: [][]driver.Value{values}}
}

type providerNamedScript struct {
	name  string
	steps []providerScriptStep
}

func providerIntegrityFailureScripts(canary error) []providerNamedScript {
	return []providerNamedScript{
		{name: "integrity query", steps: []providerScriptStep{{query: "integrity_check", err: canary}}},
		{name: "integrity result", steps: []providerScriptStep{providerRow("integrity_check", "broken")}},
		{name: "foreign query", steps: []providerScriptStep{
			providerRow("integrity_check", "ok"), {query: "foreign_key_check", err: canary},
		}},
		{name: "foreign row", steps: []providerScriptStep{
			providerRow("integrity_check", "ok"),
			{query: "foreign_key_check", columns: []string{"table"}, rows: [][]driver.Value{{"child"}}},
		}},
		{name: "foreign rows error", steps: []providerScriptStep{
			providerRow("integrity_check", "ok"),
			{query: "foreign_key_check", columns: []string{"table"}, rowsErr: canary},
		}},
	}
}

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

func TestCoverageProviderInspectionQueriesAndLifecycle(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "inspection.db")
	database, err := OpenStore(path, 250*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	if err := Configure(t.Context(), database, 250*time.Millisecond, false); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`
		CREATE TABLE storage_import_horizons(component TEXT PRIMARY KEY) STRICT;
		CREATE TABLE item(id INTEGER PRIMARY KEY, value TEXT NOT NULL) STRICT;
		CREATE UNIQUE INDEX item_value_idx ON item(value);
		INSERT INTO storage_import_horizons(component) VALUES ('inspection');
		PRAGMA user_version = 3;
	`); err != nil {
		t.Fatal(err)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}

	inspection, err := Inspect(nil, path, 250*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	if !inspection.Exists || inspection.Empty || inspection.Version != 3 {
		t.Fatalf("inspection = %#v", inspection)
	}
	if ready, err := inspection.HasSchemaObjects(t.Context(), "item", "item_value_idx"); err != nil || !ready {
		t.Fatalf("schema objects ready=%v err=%v", ready, err)
	}
	if ready, err := inspection.HasSchemaObjects(t.Context(), "missing"); err != nil || ready {
		t.Fatalf("missing schema object ready=%v err=%v", ready, err)
	}
	if ready, err := inspection.HasTableColumns(t.Context(), "item", "id", "value"); err != nil || !ready {
		t.Fatalf("table columns ready=%v err=%v", ready, err)
	}
	if ready, err := inspection.HasTableColumns(t.Context(), "item", "missing"); err != nil || ready {
		t.Fatalf("missing table column ready=%v err=%v", ready, err)
	}
	if ready, err := inspection.HasImportHorizon(t.Context(), "inspection"); err != nil || !ready {
		t.Fatalf("import horizon ready=%v err=%v", ready, err)
	}
	if ready, err := inspection.HasImportHorizon(t.Context(), "missing"); err != nil || ready {
		t.Fatalf("missing import horizon ready=%v err=%v", ready, err)
	}

	if ready, err := HasSchemaObjects(t.Context(), path, 250*time.Millisecond, "item"); err != nil || !ready {
		t.Fatalf("standalone schema ready=%v err=%v", ready, err)
	}
	if ready, err := HasSchemaObjects(t.Context(), path, 250*time.Millisecond, "missing"); err != nil || ready {
		t.Fatalf("standalone missing schema ready=%v err=%v", ready, err)
	}
	if ready, err := HasTableColumns(t.Context(), path, 250*time.Millisecond, "item", "value"); err != nil || !ready {
		t.Fatalf("standalone columns ready=%v err=%v", ready, err)
	}
	if ready, err := HasTableColumns(t.Context(), path, 250*time.Millisecond, "item", "missing"); err != nil || ready {
		t.Fatalf("standalone missing column ready=%v err=%v", ready, err)
	}

	if err := inspection.Release(); err != nil {
		t.Fatal(err)
	}
	if err := inspection.Release(); err != nil {
		t.Fatalf("repeat release = %v", err)
	}
	if err := (Inspection{}).Release(); err != nil {
		t.Fatalf("empty release = %v", err)
	}
	if _, err := (Inspection{}).HasSchemaObjects(t.Context(), "item"); err == nil {
		t.Fatal("empty inspection accepted schema query")
	}
	if _, err := (Inspection{}).HasTableColumns(t.Context(), "item", "id"); err == nil {
		t.Fatal("empty inspection accepted column query")
	}
	if _, err := (Inspection{}).HasImportHorizon(t.Context(), "item"); err == nil {
		t.Fatal("empty inspection accepted horizon query")
	}
}

func TestCoverageProviderInspectionRejectsUnsafeGenerations(t *testing.T) {
	if IsInspectionIntegrity(errors.New("ordinary")) {
		t.Fatal("ordinary error classified as inspection integrity")
	}

	t.Run("missing", func(t *testing.T) {
		inspection, err := Inspect(t.Context(), filepath.Join(t.TempDir(), "missing.db"), time.Second)
		if err != nil || inspection.Exists {
			t.Fatalf("missing inspection = %#v, %v", inspection, err)
		}
	})
	t.Run("orphan sidecar", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "missing.db")
		if err := os.WriteFile(path+"-wal", []byte("orphan"), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := Inspect(t.Context(), path, time.Second); !IsInspectionIntegrity(err) {
			t.Fatalf("orphan sidecar error = %v", err)
		}
	})
	t.Run("directory endpoint", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "store.db")
		if err := os.Mkdir(path, 0o700); err != nil {
			t.Fatal(err)
		}
		if _, err := Inspect(t.Context(), path, time.Second); !IsInspectionIntegrity(err) {
			t.Fatalf("directory endpoint error = %v", err)
		}
	})
	t.Run("corrupt bytes", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "store.db")
		if err := os.WriteFile(path, []byte("not sqlite"), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := Inspect(t.Context(), path, time.Second); !IsInspectionIntegrity(err) {
			t.Fatalf("corrupt inspection error = %v", err)
		}
	})
	t.Run("canceled", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "store.db")
		database, err := OpenStore(path, time.Second)
		if err != nil {
			t.Fatal(err)
		}
		if err := database.Close(); err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if _, err := Inspect(ctx, path, time.Second); err == nil {
			t.Fatal("canceled inspection succeeded")
		}
	})
}

func TestCoverageInspectedPoolIdentityAndCleanup(t *testing.T) {
	root := t.TempDir()
	firstPath := filepath.Join(root, "first.db")
	secondPath := filepath.Join(root, "second.db")
	if err := os.WriteFile(firstPath, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(secondPath, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	firstInfo, _ := os.Lstat(firstPath)
	secondInfo, _ := os.Lstat(secondPath)
	if _, err := retainInspectedPool(firstPath, nil, firstInfo); err == nil {
		t.Fatal("retained nil pool")
	}
	database, err := OpenStore(":memory:", time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := retainInspectedPool(firstPath, database, nil); err == nil {
		t.Fatal("retained pool without identity")
	}
	if retained, err := retainInspectedPool(firstPath, database, firstInfo); err != nil || retained != database {
		t.Fatalf("retain = %p, %v", retained, err)
	}
	if retained, err := inspectedPoolFor(firstPath, firstInfo); err != nil || retained != database {
		t.Fatalf("lookup = %p, %v", retained, err)
	}
	if _, err := inspectedPoolFor(firstPath, secondInfo); err == nil {
		t.Fatal("lookup accepted changed identity")
	}
	duplicate, err := OpenStore(":memory:", time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if retained, err := retainInspectedPool(firstPath, duplicate, firstInfo); err != nil || retained != database {
		t.Fatalf("duplicate retain = %p, %v", retained, err)
	}
	changed, err := OpenStore(":memory:", time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := retainInspectedPool(firstPath, changed, secondInfo); err == nil {
		t.Fatal("duplicate retain accepted changed identity")
	}
	if err := releaseInspectedPool(firstPath, changed); err != nil {
		t.Fatalf("mismatched release = %v", err)
	}
	if err := CloseInspectedPools([]string{firstPath, secondPath}); err != nil {
		t.Fatal(err)
	}
	if retained, err := inspectedPoolFor(firstPath, firstInfo); err != nil || retained != nil {
		t.Fatalf("post-close lookup = %p, %v", retained, err)
	}
	if adopted, err := adoptInspectedPool(secondPath); err != nil || adopted != nil {
		t.Fatalf("missing adoption = %p, %v", adopted, err)
	}
}

func TestCoverageProviderPreparationConfigurationAndMaintenanceEdges(t *testing.T) {
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
	if err := Configure(nil, memory, time.Second, true); err != nil {
		t.Fatal(err)
	}
	if err := Configure(t.Context(), memory, time.Second, false); err == nil {
		t.Fatal("file-backed configuration accepted memory journal mode")
	}

	missing := filepath.Join(t.TempDir(), "missing.db")
	if _, err := MaintainOffline(t.Context(), missing, 100*time.Millisecond); err != nil {
		t.Fatalf("maintenance of new empty generation = %v", err)
	}
	corrupt := filepath.Join(t.TempDir(), "corrupt.db")
	if err := os.WriteFile(corrupt, []byte("not sqlite"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := MaintainOffline(t.Context(), corrupt, 100*time.Millisecond); !IsMaintenanceIntegrity(err) {
		t.Fatalf("corrupt maintenance error = %v", err)
	}
	if IsMaintenanceIntegrity(errors.New("ordinary")) {
		t.Fatal("ordinary error classified as maintenance integrity")
	}
}

func TestCoverageProviderMaintenanceScriptedFailures(t *testing.T) {
	canary := errors.New("maintenance rows canary")
	for _, test := range providerIntegrityFailureScripts(canary) {
		t.Run(test.name, func(t *testing.T) {
			if err := maintenanceIntegrity(
				t.Context(),
				openProviderScript(t, test.steps...),
			); !IsMaintenanceIntegrity(
				err,
			) {
				t.Fatalf("maintenance integrity error = %v", err)
			}
		})
	}
	if err := maintenanceCheckpoint(t.Context(), openProviderScript(t,
		providerRow("wal_checkpoint", int64(0), int64(3), int64(3))), "FULL"); err != nil {
		t.Fatal(err)
	}
	if err := maintenanceCheckpoint(t.Context(), openProviderScript(t,
		providerRow("wal_checkpoint", int64(1), int64(3), int64(0))), "FULL"); err == nil {
		t.Fatal("busy checkpoint succeeded")
	}
	if err := maintenanceCheckpoint(t.Context(), openProviderScript(t,
		providerScriptStep{query: "wal_checkpoint", err: canary}), "FULL"); !errors.Is(err, canary) {
		t.Fatalf("checkpoint query error = %v", err)
	}
}

func TestCoverageStagedMigrationInputAndCleanupEdges(t *testing.T) {
	validMigration := func(ctx context.Context, stage string) error {
		database, err := openOfflineStage(ctx, stage, 100*time.Millisecond)
		if err != nil {
			return err
		}
		defer database.Close()
		if _, err := database.ExecContext(ctx, "CREATE TABLE staged(id INTEGER PRIMARY KEY)"); err != nil {
			return err
		}
		return SetSchemaVersion(ctx, database, 1)
	}
	for _, test := range []struct {
		name    string
		path    string
		version int
		migrate StagedMigration
	}{
		{name: "empty path", version: 1, migrate: validMigration},
		{name: "memory", path: ":memory:", version: 1, migrate: validMigration},
		{name: "nul path", path: "bad\x00.db", version: 1, migrate: validMigration},
		{name: "zero version", path: filepath.Join(t.TempDir(), "zero.db"), migrate: validMigration},
		{name: "nil migration", path: filepath.Join(t.TempDir(), "nil.db"), version: 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			if err := MigrateStagedOffline(
				t.Context(),
				test.path,
				time.Second,
				test.version,
				test.migrate,
			); err == nil {
				t.Fatal("invalid staged migration succeeded")
			}
		})
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if err := MigrateStagedOffline(
		canceled, filepath.Join(t.TempDir(), "canceled.db"), time.Second, 1, validMigration,
	); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled staged migration = %v", err)
	}

	newPath := filepath.Join(t.TempDir(), "new.db")
	if err := MigrateStagedOffline(nil, newPath, 100*time.Millisecond, 1, validMigration); err != nil {
		t.Fatal(err)
	}
	if ready, err := HasSchemaObjects(t.Context(), newPath, time.Second, "staged"); err != nil || !ready {
		t.Fatalf("new staged generation ready=%v err=%v", ready, err)
	}

	wrongVersionPath := filepath.Join(t.TempDir(), "wrong.db")
	if err := MigrateStagedOffline(
		t.Context(), wrongVersionPath, 100*time.Millisecond, 2, validMigration,
	); err == nil || !strings.Contains(err.Error(), "staged schema version") {
		t.Fatalf("wrong staged version error = %v", err)
	}
	if _, err := os.Stat(wrongVersionPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("failed staged destination remains: %v", err)
	}

	unsafe := filepath.Join(t.TempDir(), "unsafe.db")
	if err := os.Mkdir(unsafe, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := MigrateStagedOffline(t.Context(), unsafe, time.Second, 1, validMigration); err == nil {
		t.Fatal("staged migration accepted directory source")
	}
	if exists, err := regularGenerationExists(filepath.Join(t.TempDir(), "missing.db")); err != nil || exists {
		t.Fatalf("missing generation exists=%v err=%v", exists, err)
	}
	if exists, err := regularGenerationExists(unsafe); err == nil || exists {
		t.Fatalf("unsafe generation exists=%v err=%v", exists, err)
	}

	sidecarBase := filepath.Join(t.TempDir(), "sidecar.db")
	if err := os.WriteFile(sidecarBase+"-wal", nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := requireNoGenerationSidecars(sidecarBase); err == nil {
		t.Fatal("active staged sidecar accepted")
	}
	if err := requireNoGenerationSidecars(filepath.Join(t.TempDir(), "clean.db")); err != nil {
		t.Fatal(err)
	}
	if err := discardStagedGeneration(filepath.Join(t.TempDir(), "missing.db"), time.Second); err != nil {
		t.Fatal(err)
	}
	corruptStage := filepath.Join(t.TempDir(), "corrupt-stage.db")
	if err := os.WriteFile(corruptStage, []byte("broken"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := discardStagedGeneration(corruptStage, 100*time.Millisecond); err != nil {
		t.Fatalf("corrupt disposable stage cleanup = %v", err)
	}
	if _, err := os.Stat(corruptStage); err != nil {
		t.Fatalf("diagnostic corrupt stage was not retained: %v", err)
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

func TestCoverageProviderInspectionQueryFailures(t *testing.T) {
	canary := errors.New("inspection query canary")
	for _, test := range []struct {
		name string
		run  func(Inspection) error
	}{
		{name: "schema", run: func(inspection Inspection) error {
			_, err := inspection.HasSchemaObjects(t.Context(), "item")
			return err
		}},
		{name: "columns", run: func(inspection Inspection) error {
			_, err := inspection.HasTableColumns(t.Context(), "item", "id")
			return err
		}},
		{name: "horizon", run: func(inspection Inspection) error {
			_, err := inspection.HasImportHorizon(t.Context(), "item")
			return err
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			inspection := Inspection{database: openProviderScript(t,
				providerScriptStep{query: "COUNT", err: canary})}
			if err := test.run(inspection); !errors.Is(err, canary) {
				t.Fatalf("inspection query error = %v", err)
			}
		})
	}

	path := filepath.Join(t.TempDir(), "store.db")
	database, err := OpenStore(path, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec("CREATE TABLE item(id INTEGER PRIMARY KEY, value TEXT)"); err != nil {
		t.Fatal(err)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := HasSchemaObjects(canceled, path, time.Second, "item"); err == nil {
		t.Fatal("canceled standalone schema query succeeded")
	}
	if _, err := HasTableColumns(canceled, path, time.Second, "item", "id"); err == nil {
		t.Fatal("canceled standalone column query succeeded")
	}
	missing := filepath.Join(t.TempDir(), "missing.db")
	if _, err := HasSchemaObjects(t.Context(), missing, time.Second, "item"); err == nil {
		t.Fatal("standalone schema query opened missing generation")
	}
	if _, err := HasTableColumns(t.Context(), missing, time.Second, "item", "id"); err == nil {
		t.Fatal("standalone column query opened missing generation")
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
	if err := PrepareStore(sidecarPath); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(sidecarPath); err != nil {
		t.Fatal(err)
	}
	if err := SecureGeneration(sidecarPath); err == nil {
		t.Fatal("generation without main database secured")
	}
}

func TestCoverageProviderMaintenanceHelperEdges(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "missing.db")
	if _, err := inspectAndRecover(t.Context(), missing, 100*time.Millisecond); err != nil {
		t.Fatal(err)
	}
	if err := exclusiveRollbackBoundary(t.Context(), missing, 100*time.Millisecond); err != nil {
		t.Fatal(err)
	}
	if err := checkpointGeneration(t.Context(), missing, 100*time.Millisecond); err != nil {
		t.Fatal(err)
	}
	if _, err := reopenAndValidate(t.Context(), missing, 100*time.Millisecond); err != nil {
		t.Fatal(err)
	}

	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	for name, run := range map[string]func() error{
		"inspect":    func() error { _, err := inspectAndRecover(canceled, missing, time.Second); return err },
		"boundary":   func() error { return exclusiveRollbackBoundary(canceled, missing, time.Second) },
		"checkpoint": func() error { return checkpointGeneration(canceled, missing, time.Second) },
		"reopen":     func() error { _, err := reopenAndValidate(canceled, missing, time.Second); return err },
	} {
		t.Run(name, func(t *testing.T) {
			if err := run(); err == nil {
				t.Fatal("canceled maintenance helper succeeded")
			}
		})
	}
	unsafe := filepath.Join(t.TempDir(), "directory.db")
	if err := os.Mkdir(unsafe, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := openMaintenanceStore(unsafe, time.Second); err == nil {
		t.Fatal("maintenance opened directory endpoint")
	}
}

func TestCoverageStagedGenerationHelperEdges(t *testing.T) {
	root := t.TempDir()
	valid := filepath.Join(root, "valid.db")
	database, err := openOfflineStage(t.Context(), valid, 100*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec("CREATE TABLE item(id INTEGER PRIMARY KEY)"); err != nil {
		t.Fatal(err)
	}
	if err := SetSchemaVersion(t.Context(), database, 1); err != nil {
		t.Fatal(err)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}
	if err := validateStagedGeneration(t.Context(), valid, time.Second, 1); err != nil {
		t.Fatal(err)
	}
	if err := validateStagedGeneration(t.Context(), valid, time.Second, 2); err == nil {
		t.Fatal("staged generation accepted wrong version")
	}
	if err := normalizeOriginalForCutover(t.Context(), valid, time.Second); err != nil {
		t.Fatal(err)
	}
	if err := activateInstalledGeneration(t.Context(), valid, time.Second, 2); err == nil {
		t.Fatal("activation accepted wrong version")
	}
	if exists, err := regularGenerationExists(valid); err != nil || !exists {
		t.Fatalf("regular generation = %v, %v", exists, err)
	}

	disposable := filepath.Join(root, "disposable.db")
	database, err = openOfflineStage(t.Context(), disposable, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}
	if err := discardStagedGeneration(disposable, time.Second); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(disposable); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("disposable generation remains: %v", err)
	}
	directory := filepath.Join(root, "directory.db")
	if err := os.Mkdir(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := discardStagedGeneration(directory, time.Second); err == nil {
		t.Fatal("directory stage discarded")
	}
	if _, err := replaceStagedGeneration(filepath.Join(root, "missing-stage"), valid); err == nil {
		t.Fatal("missing stage replaced generation")
	}
	if err := syncStagedMigrationDirectory(filepath.Join(root, "missing-directory")); err == nil {
		t.Fatal("missing staged directory synced")
	}
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

func TestCoverageInspectionIntegrityScriptedFailures(t *testing.T) {
	canary := errors.New("inspection integrity canary")
	for _, test := range providerIntegrityFailureScripts(canary) {
		t.Run(test.name, func(t *testing.T) {
			if err := inspectIntegrity(t.Context(), openProviderScript(t, test.steps...)); !IsInspectionIntegrity(err) {
				t.Fatalf("inspection integrity error = %v", err)
			}
		})
	}
}

func TestCoverageProviderWALRetryDeadline(t *testing.T) {
	path := filepath.Join(t.TempDir(), "locked.db")
	owner, err := OpenStore(path, time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	owner.SetMaxOpenConns(1)
	owner.SetMaxIdleConns(1)
	if err := ConfigureOffline(t.Context(), owner, time.Millisecond); err != nil {
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
	contender, err := OpenStore(path, time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	defer contender.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Millisecond)
	defer cancel()
	if _, err := EnableWAL(ctx, contender, 5*time.Millisecond); err == nil {
		t.Fatal("WAL selection succeeded through exclusive lock")
	}
}

func TestCoverageStagedMigrationFailurePhases(t *testing.T) {
	t.Run("corrupt source snapshot", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "source.db")
		if err := os.WriteFile(path, []byte("not sqlite"), 0o600); err != nil {
			t.Fatal(err)
		}
		err := MigrateStagedOffline(t.Context(), path, time.Second, 1,
			func(context.Context, string) error { return nil })
		if err == nil || !strings.Contains(err.Error(), "snapshot SQLite migration stage") {
			t.Fatalf("corrupt source migration = %v", err)
		}
	})

	t.Run("source changes before normalization", func(t *testing.T) {
		path := createStagedMigrationFixture(t)
		err := MigrateStagedOffline(t.Context(), path, time.Second, 1,
			func(ctx context.Context, stage string) error {
				if err := installStagedFixtureTable(ctx, stage); err != nil {
					return err
				}
				return os.WriteFile(path, []byte("corrupt after snapshot"), 0o600)
			})
		if err == nil || !strings.Contains(err.Error(), "normalize SQLite cutover source") {
			t.Fatalf("changed source migration = %v", err)
		}
	})

	t.Run("stage sidecar", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "target.db")
		err := MigrateStagedOffline(t.Context(), path, time.Second, 1,
			func(ctx context.Context, stage string) error {
				if err := installStagedFixtureTable(ctx, stage); err != nil {
					return err
				}
				return os.Mkdir(stage+"-journal", 0o700)
			})
		if err == nil {
			t.Fatal("staged migration accepted active stage sidecar")
		}
	})

	t.Run("backup canceled", func(t *testing.T) {
		source := createStagedMigrationFixture(t)
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if err := backupGenerationToStage(
			ctx, source, filepath.Join(t.TempDir(), "stage.db"), time.Second,
		); !errors.Is(err, context.Canceled) {
			t.Fatalf("canceled stage backup = %v", err)
		}
	})

	t.Run("backup corrupt", func(t *testing.T) {
		source := filepath.Join(t.TempDir(), "corrupt.db")
		if err := os.WriteFile(source, []byte("corrupt"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := backupGenerationToStage(
			t.Context(), source, filepath.Join(t.TempDir(), "stage.db"), time.Second,
		); err == nil {
			t.Fatal("corrupt stage backup succeeded")
		}
	})

	t.Run("offline stage canceled", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if database, err := openOfflineStage(
			ctx,
			filepath.Join(t.TempDir(), "stage.db"),
			time.Second,
		); err == nil ||
			database != nil {
			if database != nil {
				_ = database.Close()
			}
			t.Fatalf("canceled offline stage = %#v, %v", database, err)
		}
	})
}
