//nolint:govet // Narrow assertions intentionally use independent error scopes.
package memory

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/sipeed/picoclaw/pkg/internal/sessiondb"
)

func TestSQLiteSessionHorizonCannotApplyLateDeleteManifest(t *testing.T) {
	workspace := t.TempDir()
	if err := os.Chmod(workspace, 0o700); err != nil {
		t.Fatal(err)
	}
	sessionsDir := filepath.Join(workspace, "sessions")
	store, err := NewStore(sessionsDir)
	if err != nil {
		t.Fatal(err)
	}
	const sessionKey = "agent:test:late-delete"
	if err := store.SetSummary(t.Context(), sessionKey, "must survive"); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	manifestName := deleteManifestPrefix + "late.json"
	manifestPath := filepath.Join(sessionsDir, manifestName)
	encoded, err := json.Marshal(sessionDeleteManifest{Version: 1, Keys: []string{sessionKey}})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(manifestPath, encoded, 0o600); err != nil {
		t.Fatal(err)
	}

	reopened, err := NewStore(sessionsDir)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	summary, err := reopened.GetSummary(t.Context(), sessionKey)
	if err != nil || summary != "must survive" {
		t.Fatalf("summary after late delete manifest = %q, %v", summary, err)
	}
	if _, err := os.Lstat(manifestPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("late delete manifest remains: %v", err)
	}
	archivePath := filepath.Join(
		workspace, "legacy-json", legacySessionsArchiveVersion, "sessions", manifestName,
	)
	if archived, err := os.ReadFile(archivePath); err != nil || string(archived) != string(encoded) {
		t.Fatalf("late delete archive = %q, %v", archived, err)
	}
	var imported, skipped, issues int
	if err := sessiondb.Bind(reopened.ThreadStore()).Database().QueryRow(`SELECT imported_count, skipped_count,
	    (SELECT COUNT(*) FROM storage_import_issues AS issue
	      WHERE issue.component = storage_imports.component
	        AND issue.source_id = storage_imports.source_id
	        AND issue.issue_code = 'late-source')
	    FROM storage_imports
	    WHERE component = ? AND source_relative = ?`,
		sessionsComponent, filepath.ToSlash(filepath.Join("sessions", manifestName)),
	).Scan(&imported, &skipped, &issues); err != nil || imported != 0 || skipped != 1 || issues != 1 {
		t.Fatalf("late delete audit = imported:%d skipped:%d issues:%d error:%v",
			imported, skipped, issues, err)
	}
}
