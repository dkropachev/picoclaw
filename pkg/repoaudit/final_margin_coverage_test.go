package repoaudit

import (
	"encoding/json"
	"os"
	"testing"
)

// TestRepositoryReviewSummaryLegacyReloadMargins covers both failures after a
// legacy summary has been decoded successfully: migration can fail, or the
// migrated ledger can legitimately remain an uncreated (version-zero) state.
func TestRepositoryReviewSummaryLegacyReloadMargins(t *testing.T) {
	t.Skip("legacy JSON summary reload was replaced by SQLite import and typed projection tests")
	t.Run("migration error", func(t *testing.T) {
		store := NewStore(t.TempDir())
		repository := "owner/legacy-summary-invalid-state"
		state, err := store.load(repository)
		if err != nil {
			t.Fatal(err)
		}
		state.SchemaVersion = 3
		state.Version = -1
		writeRepositoryReviewMarginState(t, store, state)

		if _, err := store.ListSummaries(); err == nil {
			t.Fatal("invalid legacy ledger unexpectedly migrated")
		}
	})

	t.Run("version zero disappears", func(t *testing.T) {
		store := NewStore(t.TempDir())
		repository := "owner/legacy-summary-version-zero"
		state, err := store.load(repository)
		if err != nil {
			t.Fatal(err)
		}
		state.SchemaVersion = 3
		state.Version = 0
		writeRepositoryReviewMarginState(t, store, state)

		if _, err := store.ListSummaries(); err == nil {
			t.Fatal("version-zero legacy ledger did not report disappearance")
		}
	})
}

func writeRepositoryReviewMarginState(t *testing.T, store Store, state RepositoryState) {
	t.Helper()
	if err := os.MkdirAll(store.root, 0o700); err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(store.path(state.Repository), encoded, 0o600); err != nil {
		t.Fatal(err)
	}
}
