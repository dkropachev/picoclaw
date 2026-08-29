package repoaudit

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"testing"
	"time"
)

func TestRepositoryReviewAssignmentTimeoutDefaultsAndMaterializes(t *testing.T) {
	profileStore := NewStore(t.TempDir())
	profileStore.now = func() time.Time { return automationTestNow }
	profile, err := profileStore.CreateProfile(
		context.Background(),
		validProfileForTest("rrpf_assignment_timeout_default", "Default timeout"),
	)
	if err != nil {
		t.Fatal(err)
	}
	if profile.AssignmentTimeoutSeconds != DefaultRepositoryReviewAssignmentTimeoutSeconds {
		t.Fatalf("profile assignment timeout = %d", profile.AssignmentTimeoutSeconds)
	}

	automationStore := newAutomationTestStore(t)
	automation, err := automationStore.CreateAutomation(
		context.Background(),
		validAutomationForTest("rra_assignment_timeout_default", "Default timeout"),
	)
	if err != nil {
		t.Fatal(err)
	}
	if automation.AssignmentTimeoutSeconds != DefaultRepositoryReviewAssignmentTimeoutSeconds {
		t.Fatalf("automation assignment timeout = %d", automation.AssignmentTimeoutSeconds)
	}

	profile.AssignmentTimeoutSeconds = 7_200
	materialized, err := MaterializeRepositoryReviewAutomation(
		profile,
		validAutomationForTest("rra_assignment_timeout_materialized", "Materialized timeout"),
	)
	if err != nil {
		t.Fatal(err)
	}
	if materialized.AssignmentTimeoutSeconds != 7_200 {
		t.Fatalf("materialized assignment timeout = %d", materialized.AssignmentTimeoutSeconds)
	}
}

func TestRepositoryReviewAssignmentTimeoutValidation(t *testing.T) {
	profileStore := NewStore(t.TempDir())
	profileStore.now = func() time.Time { return automationTestNow }
	profile, err := profileStore.CreateProfile(
		context.Background(),
		validProfileForTest("rrpf_assignment_timeout_validation", "Timeout validation"),
	)
	if err != nil {
		t.Fatal(err)
	}
	automationStore := newAutomationTestStore(t)
	automation, err := automationStore.CreateAutomation(
		context.Background(),
		validAutomationForTest("rra_assignment_timeout_validation", "Timeout validation"),
	)
	if err != nil {
		t.Fatal(err)
	}

	for _, value := range []int{
		MinRepositoryReviewAssignmentTimeoutSeconds,
		120,
		DefaultRepositoryReviewAssignmentTimeoutSeconds,
		MaxRepositoryReviewAssignmentTimeoutSeconds,
	} {
		candidateProfile := profile
		candidateProfile.AssignmentTimeoutSeconds = value
		if err := normalizeProfile(&candidateProfile); err != nil {
			t.Errorf("profile timeout %d rejected: %v", value, err)
		}
		candidateAutomation := automation
		candidateAutomation.AssignmentTimeoutSeconds = value
		if err := normalizeAutomation(&candidateAutomation); err != nil {
			t.Errorf("automation timeout %d rejected: %v", value, err)
		}
	}

	for _, value := range []int{-60, 59, 61, MaxRepositoryReviewAssignmentTimeoutSeconds + 60} {
		candidateProfile := profile
		candidateProfile.AssignmentTimeoutSeconds = value
		if err := normalizeProfile(&candidateProfile); err == nil {
			t.Errorf("profile timeout %d accepted", value)
		}
		candidateAutomation := automation
		candidateAutomation.AssignmentTimeoutSeconds = value
		if err := normalizeAutomation(&candidateAutomation); err == nil {
			t.Errorf("automation timeout %d accepted", value)
		}
	}
}

func TestRepositoryReviewAssignmentTimeoutSchemaMigrationsPreserveVersions(t *testing.T) {
	t.Run("profile v2", func(t *testing.T) {
		store := NewStore(t.TempDir())
		store.now = func() time.Time { return automationTestNow }
		created, err := store.CreateProfile(
			context.Background(),
			validProfileForTest("rrpf_assignment_timeout_v2", "Legacy profile"),
		)
		if err != nil {
			t.Fatal(err)
		}
		created, err = store.UpdateProfile(
			context.Background(), created.ID, created.Version,
			func(candidate *RepositoryReviewProfile) error {
				candidate.Name = "Legacy profile version two"
				return nil
			},
		)
		if err != nil {
			t.Fatal(err)
		}
		path := store.profilePath(created.ID)
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		var raw map[string]any
		if unmarshalErr := json.Unmarshal(data, &raw); unmarshalErr != nil {
			t.Fatal(unmarshalErr)
		}
		raw["schema_version"] = float64(2)
		delete(raw, "assignment_timeout_seconds")
		data, err = json.Marshal(raw)
		if err != nil {
			t.Fatal(err)
		}
		if writeErr := os.WriteFile(path, data, 0o600); writeErr != nil {
			t.Fatal(writeErr)
		}

		loaded, found, err := store.GetProfile(context.Background(), created.ID)
		if err != nil || !found {
			t.Fatalf("GetProfile() found=%v error=%v", found, err)
		}
		if loaded.SchemaVersion != RepositoryReviewProfileSchemaVersion ||
			loaded.Version != created.Version ||
			!loaded.CreatedAt.Equal(created.CreatedAt) || !loaded.UpdatedAt.Equal(created.UpdatedAt) ||
			loaded.AssignmentTimeoutSeconds != DefaultRepositoryReviewAssignmentTimeoutSeconds {
			t.Fatalf("migrated profile = %#v", loaded)
		}
		rewritten, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Contains(rewritten, []byte(`"schema_version":3`)) ||
			!bytes.Contains(rewritten, []byte(`"assignment_timeout_seconds":3600`)) {
			t.Fatalf("profile migration was not persisted: %s", rewritten)
		}
	})

	t.Run("automation v1", func(t *testing.T) {
		store := newAutomationTestStore(t)
		created := createAutomationForTest(
			t, store, "rra_assignment_timeout_v1", "Legacy automation",
		)
		created, err := store.UpdateAutomation(
			context.Background(), created.ID, created.Version,
			func(candidate *RepositoryReviewAutomation) error {
				candidate.Name = "Legacy automation version two"
				return nil
			},
		)
		if err != nil {
			t.Fatal(err)
		}
		path := store.automationPath(created.ID)
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		var raw map[string]any
		if unmarshalErr := json.Unmarshal(data, &raw); unmarshalErr != nil {
			t.Fatal(unmarshalErr)
		}
		raw["schema_version"] = float64(1)
		delete(raw, "assignment_timeout_seconds")
		data, err = json.Marshal(raw)
		if err != nil {
			t.Fatal(err)
		}
		if writeErr := os.WriteFile(path, data, 0o600); writeErr != nil {
			t.Fatal(writeErr)
		}

		loaded, found, err := store.GetAutomation(context.Background(), created.ID)
		if err != nil || !found {
			t.Fatalf("GetAutomation() found=%v error=%v", found, err)
		}
		if loaded.SchemaVersion != RepositoryReviewAutomationSchemaVersion ||
			loaded.Version != created.Version ||
			!loaded.CreatedAt.Equal(created.CreatedAt) || !loaded.UpdatedAt.Equal(created.UpdatedAt) ||
			loaded.AssignmentTimeoutSeconds != DefaultRepositoryReviewAssignmentTimeoutSeconds {
			t.Fatalf("migrated automation = %#v", loaded)
		}
		rewritten, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Contains(rewritten, []byte(`"schema_version":2`)) ||
			!bytes.Contains(rewritten, []byte(`"assignment_timeout_seconds":3600`)) {
			t.Fatalf("automation migration was not persisted: %s", rewritten)
		}
	})
}
