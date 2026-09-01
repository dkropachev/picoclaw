//go:build unix

package repoaudit

import (
	"context"
	"encoding/json"
	"os"
	"testing"
	"time"
)

func requireRepositoryReviewPermissionEnforcement(t *testing.T) {
	t.Helper()
	if os.Geteuid() == 0 {
		t.Skip("root can bypass directory permission checks")
	}
}

func TestRepositoryReviewProfilePersistenceLateFailures(t *testing.T) {
	t.Skip("per-record JSON persistence failures were replaced by SQLite transaction tests")
	t.Run("JSON timestamp", func(t *testing.T) {
		profile := profileCoverageFixture("rrpf_json_timestamp")
		outsideJSONTime := time.Date(10_000, time.January, 1, 0, 0, 0, 0, time.UTC)
		profile.CreatedAt = outsideJSONTime
		profile.UpdatedAt = outsideJSONTime
		if err := NewStore(t.TempDir()).saveProfile(profile); err == nil {
			t.Fatal("profile with an unencodable timestamp was saved")
		}
	})

	t.Run("destination lookup permission", func(t *testing.T) {
		requireRepositoryReviewPermissionEnforcement(t)
		store := NewStore(t.TempDir())
		if err := os.MkdirAll(store.root, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(store.root, 0); err != nil {
			t.Skipf("cannot restrict profile root: %v", err)
		}
		t.Cleanup(func() { _ = os.Chmod(store.root, 0o700) })
		if err := store.saveProfile(profileCoverageFixture("rrpf_lstat_permission")); err == nil {
			t.Fatal("profile save ignored an inaccessible destination")
		}
	})

	t.Run("legacy rewrite permission", func(t *testing.T) {
		requireRepositoryReviewPermissionEnforcement(t)
		store := NewStore(t.TempDir())
		profile := profileCoverageFixture("rrpf_legacy_rewrite_permission")
		encoded, err := json.Marshal(profile)
		if err != nil {
			t.Fatal(err)
		}
		var root map[string]json.RawMessage
		if decodeErr := json.Unmarshal(encoded, &root); decodeErr != nil {
			t.Fatal(decodeErr)
		}
		root["budget"] = json.RawMessage(`{"max_total_tokens":100}`)
		encoded, err = json.Marshal(root)
		if err != nil {
			t.Fatal(err)
		}
		writeProfileCoverageFile(t, store, profileFilename(profile.ID), encoded)
		if err := os.Chmod(store.root, 0o500); err != nil {
			t.Skipf("cannot restrict profile root: %v", err)
		}
		t.Cleanup(func() { _ = os.Chmod(store.root, 0o700) })
		if _, _, err := store.loadProfile(profile.ID); err == nil {
			t.Fatal("legacy profile rewrite ignored a read-only catalog")
		}
	})
}

func TestRepositoryReviewAutomationPersistenceLateFailures(t *testing.T) {
	t.Skip("per-record JSON persistence failures were replaced by SQLite transaction tests")
	t.Run("JSON timestamp", func(t *testing.T) {
		automation := validAutomationForTest("rra_json_timestamp", "JSON timestamp")
		outsideJSONTime := time.Date(10_000, time.January, 1, 0, 0, 0, 0, time.UTC)
		automation.CreatedAt = outsideJSONTime
		automation.UpdatedAt = outsideJSONTime
		if err := NewStore(t.TempDir()).saveAutomation(automation); err == nil {
			t.Fatal("automation with an unencodable timestamp was saved")
		}
	})

	t.Run("destination lookup permission", func(t *testing.T) {
		requireRepositoryReviewPermissionEnforcement(t)
		store := NewStore(t.TempDir())
		if err := os.MkdirAll(store.root, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(store.root, 0); err != nil {
			t.Skipf("cannot restrict automation root: %v", err)
		}
		t.Cleanup(func() { _ = os.Chmod(store.root, 0o700) })
		if err := store.saveAutomation(validAutomationForTest("rra_lstat_permission", "Permission")); err == nil {
			t.Fatal("automation save ignored an inaccessible destination")
		}
	})

	t.Run("legacy rewrite permission", func(t *testing.T) {
		requireRepositoryReviewPermissionEnforcement(t)
		store := NewStore(t.TempDir())
		automation, err := store.CreateAutomation(
			context.Background(),
			validAutomationForTest("rra_legacy_rewrite_permission", "Legacy rewrite"),
		)
		if err != nil {
			t.Fatal(err)
		}
		automation.ModelPrices = map[string]RepositoryReviewModelPrice{
			"review-a": {InputPricePer1M: 1, OutputPricePer1M: 2},
		}
		encoded, err := json.Marshal(automation)
		if err != nil {
			t.Fatal(err)
		}
		var root map[string]json.RawMessage
		if decodeErr := json.Unmarshal(encoded, &root); decodeErr != nil {
			t.Fatal(decodeErr)
		}
		root["model_prices"] = json.RawMessage(
			`{"review-a":{"input_price_per_1m":1,"output_price_per_1m":2,"subscription":true}}`,
		)
		encoded, err = json.Marshal(root)
		if err != nil {
			t.Fatal(err)
		}
		writeProfileCoverageFile(t, store, automationFilename(automation.ID), encoded)
		if err := os.Chmod(store.root, 0o500); err != nil {
			t.Skipf("cannot restrict automation root: %v", err)
		}
		t.Cleanup(func() { _ = os.Chmod(store.root, 0o700) })
		if _, _, err := store.loadAutomation(automation.ID); err == nil {
			t.Fatal("legacy automation rewrite ignored a read-only catalog")
		}
	})
}
