package sqliteprovider

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const realBehaviorProviderTimeout = 250 * time.Millisecond

func TestRealBehaviorInspectionPoolRejectsLateGenerationChanges(t *testing.T) {
	t.Run("main inode", func(t *testing.T) {
		path := createRealBehaviorProviderGeneration(t, false)
		inspection, err := Inspect(t.Context(), path, realBehaviorProviderTimeout)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = inspection.Release() })

		if err := os.Rename(path, path+".old"); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, nil, 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := Inspect(t.Context(), path, realBehaviorProviderTimeout); !IsInspectionIntegrity(err) ||
			!strings.Contains(err.Error(), "generation changed") {
			t.Fatalf("replacement inspection error = %v", err)
		}
	})

	t.Run("late sidecar", func(t *testing.T) {
		path := createRealBehaviorProviderGeneration(t, false)
		inspection, err := Inspect(t.Context(), path, realBehaviorProviderTimeout)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.Mkdir(path+"-journal", 0o700); err != nil {
			_ = inspection.Release()
			t.Fatal(err)
		}
		adopted, err := OpenStore(path, realBehaviorProviderTimeout)
		if err == nil || adopted != nil || !strings.Contains(err.Error(), "not a regular file") {
			if adopted != nil {
				_ = adopted.Close()
			}
			t.Fatalf("unsafe sidecar adoption = %p, %v", adopted, err)
		}
		if err := inspection.database.Ping(); !errors.Is(err, os.ErrClosed) &&
			!strings.Contains(err.Error(), "database is closed") {
			t.Fatalf("rejected inspection pool remained open: %v", err)
		}
	})
}

func TestRealBehaviorForeignKeyDamageFailsEveryValidationBoundary(t *testing.T) {
	t.Run("inspection", func(t *testing.T) {
		path := createRealBehaviorProviderGeneration(t, true)
		if _, err := Inspect(t.Context(), path, realBehaviorProviderTimeout); !IsInspectionIntegrity(err) ||
			!strings.Contains(err.Error(), "foreign-key") {
			t.Fatalf("foreign-key inspection error = %v", err)
		}
	})

	t.Run("maintenance", func(t *testing.T) {
		path := createRealBehaviorProviderGeneration(t, true)
		if _, err := MaintainOffline(
			t.Context(), path, realBehaviorProviderTimeout,
		); !IsMaintenanceIntegrity(err) || !strings.Contains(err.Error(), "foreign-key") {
			t.Fatalf("foreign-key maintenance error = %v", err)
		}
	})

	t.Run("exclusive boundary", func(t *testing.T) {
		path := createRealBehaviorProviderGeneration(t, true)
		if err := exclusiveRollbackBoundary(
			t.Context(), path, realBehaviorProviderTimeout,
		); !IsMaintenanceIntegrity(err) || !strings.Contains(err.Error(), "foreign-key") {
			t.Fatalf("foreign-key exclusive-boundary error = %v", err)
		}
	})

	t.Run("reopen", func(t *testing.T) {
		path := createRealBehaviorProviderGeneration(t, true)
		if _, err := reopenAndValidate(
			t.Context(), path, realBehaviorProviderTimeout,
		); !IsMaintenanceIntegrity(err) || !strings.Contains(err.Error(), "foreign-key") {
			t.Fatalf("foreign-key reopen error = %v", err)
		}
	})

	t.Run("staged validation", func(t *testing.T) {
		path := createRealBehaviorProviderGeneration(t, true)
		if err := validateStagedGeneration(
			t.Context(), path, realBehaviorProviderTimeout, 1,
		); !IsMaintenanceIntegrity(err) || !strings.Contains(err.Error(), "foreign-key") {
			t.Fatalf("foreign-key staged-validation error = %v", err)
		}
	})

	t.Run("failed stage retained", func(t *testing.T) {
		path := createRealBehaviorProviderGeneration(t, true)
		if err := discardStagedGeneration(path, realBehaviorProviderTimeout); err != nil {
			t.Fatalf("discard corrupt stage = %v", err)
		}
		if info, err := os.Stat(path); err != nil || !info.Mode().IsRegular() {
			t.Fatalf("diagnostic stage was not retained: %v, %v", info, err)
		}
	})
}

func createRealBehaviorProviderGeneration(t *testing.T, foreignKeyViolation bool) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "store.db")
	database, err := OpenStore(path, realBehaviorProviderTimeout)
	if err != nil {
		t.Fatal(err)
	}
	database.SetMaxOpenConns(1)
	database.SetMaxIdleConns(1)
	if err := Configure(context.Background(), database, realBehaviorProviderTimeout, false); err != nil {
		_ = database.Close()
		t.Fatal(err)
	}
	schema := `
		CREATE TABLE parent(id INTEGER PRIMARY KEY) STRICT;
		PRAGMA user_version = 1;
	`
	if foreignKeyViolation {
		schema = `
			PRAGMA foreign_keys = OFF;
			CREATE TABLE parent(id INTEGER PRIMARY KEY) STRICT;
			CREATE TABLE child(
				id INTEGER PRIMARY KEY,
				parent_id INTEGER NOT NULL REFERENCES parent(id)
			) STRICT;
			INSERT INTO child(id, parent_id) VALUES (1, 99);
			PRAGMA user_version = 1;
		`
	}
	if _, err := database.ExecContext(context.Background(), schema); err != nil {
		_ = database.Close()
		t.Fatal(err)
	}
	if foreignKeyViolation {
		rows, err := database.QueryContext(context.Background(), "PRAGMA foreign_key_check")
		if err != nil {
			_ = database.Close()
			t.Fatal(err)
		}
		violated := rows.Next()
		if closeErr := rows.Close(); closeErr != nil {
			_ = database.Close()
			t.Fatal(closeErr)
		}
		if !violated {
			_ = database.Close()
			t.Fatal("foreign-key violation fixture is valid")
		}
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}
	return path
}
