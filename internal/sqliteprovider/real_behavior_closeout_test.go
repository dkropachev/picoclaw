package sqliteprovider

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"
	"time"
)

const realBehaviorProviderTimeout = 250 * time.Millisecond

func TestRealBehaviorForeignKeyDamageFailsEveryValidationBoundary(t *testing.T) {
	t.Run("inspection", func(t *testing.T) {
		path := createRealBehaviorProviderGeneration(t, true)
		if _, err := Inspect(t.Context(), path, realBehaviorProviderTimeout); !IsInspectionIntegrity(err) {
			t.Fatalf("foreign-key inspection error = %v", err)
		}
	})

	t.Run("maintenance", func(t *testing.T) {
		path := createRealBehaviorProviderGeneration(t, true)
		if _, err := maintainOfflineFixture(t,
			t.Context(), path, realBehaviorProviderTimeout,
		); !IsMaintenanceIntegrity(err) {
			t.Fatalf("foreign-key maintenance error = %v", err)
		}
	})

	t.Run("exclusive boundary", func(t *testing.T) {
		path := createRealBehaviorProviderGeneration(t, true)
		if err := exclusiveRollbackBoundary(
			t.Context(), path, realBehaviorProviderTimeout,
		); !IsMaintenanceIntegrity(err) {
			t.Fatalf("foreign-key exclusive-boundary error = %v", err)
		}
	})

	t.Run("reopen", func(t *testing.T) {
		path := createRealBehaviorProviderGeneration(t, true)
		if _, err := reopenAndValidate(
			t.Context(), path, realBehaviorProviderTimeout,
		); !IsMaintenanceIntegrity(err) {
			t.Fatalf("foreign-key reopen error = %v", err)
		}
	})

	t.Run("staged validation", func(t *testing.T) {
		path := createRealBehaviorProviderGeneration(t, true)
		if err := validateStagedGeneration(
			t.Context(), path, realBehaviorProviderTimeout, 1,
		); !IsMaintenanceIntegrity(err) {
			t.Fatalf("foreign-key staged-validation error = %v", err)
		}
	})

	t.Run("failed stage retained", func(t *testing.T) {
		path := createRealBehaviorProviderGeneration(t, true)
		if err := discardStagedGeneration(path, realBehaviorProviderTimeout); err == nil {
			t.Fatal("corrupt diagnostic stage retention was not reported")
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
		violated, queryErr := realBehaviorForeignKeyViolation(database)
		if queryErr != nil {
			_ = database.Close()
			t.Fatal(queryErr)
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

func realBehaviorForeignKeyViolation(database *sql.DB) (bool, error) {
	rows, err := database.QueryContext(context.Background(), "PRAGMA foreign_key_check")
	if err != nil {
		return false, err
	}
	defer rows.Close()
	violated := rows.Next()
	if err := rows.Err(); err != nil {
		return false, err
	}
	return violated, nil
}
