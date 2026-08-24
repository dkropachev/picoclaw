//go:build unix

package repoaudit

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type profileCancelAfterFirstContext struct {
	context.Context
	first    chan struct{}
	calls    atomic.Int32
	canceled atomic.Bool
}

func (ctx *profileCancelAfterFirstContext) Err() error {
	if ctx.calls.Add(1) == 1 {
		close(ctx.first)
		return nil
	}
	if ctx.canceled.Load() {
		return context.Canceled
	}
	return nil
}

func profileCoverageFixture(id string) RepositoryReviewProfile {
	profile := validProfileForTest(id, "Coverage profile")
	profile.SchemaVersion = RepositoryReviewProfileSchemaVersion
	profile.Version = 1
	profile.CreatedAt = automationTestNow
	profile.UpdatedAt = automationTestNow
	return profile
}

func writeProfileCoverageFile(t *testing.T, store Store, name string, data []byte) {
	t.Helper()
	if err := os.MkdirAll(store.root, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(store.root+string(os.PathSeparator)+name, data, 0o600); err != nil {
		t.Fatal(err)
	}
}

func poisonProfileStoreOnClock(t *testing.T, store *Store) {
	t.Helper()
	store.now = func() time.Time {
		if err := os.RemoveAll(store.root); err != nil {
			t.Errorf("remove profile store: %v", err)
		} else if err := os.WriteFile(store.root, []byte("not a directory"), 0o600); err != nil {
			t.Errorf("poison profile store: %v", err)
		}
		return time.Now().UTC().Add(time.Hour)
	}
}

func profilePublicCalls(store Store) []struct {
	name string
	key  string
	call func(context.Context) error
} {
	return []struct {
		name string
		key  string
		call func(context.Context) error
	}{
		{
			name: "list", key: "repository-review-profiles",
			call: func(ctx context.Context) error {
				_, err := store.ListProfiles(ctx)
				return err
			},
		},
		{
			name: "get", key: "profile:rrpf_context_get",
			call: func(ctx context.Context) error {
				_, _, err := store.GetProfile(ctx, "rrpf_context_get")
				return err
			},
		},
		{
			name: "create", key: "profile:rrpf_context_create",
			call: func(ctx context.Context) error {
				_, err := store.CreateProfile(ctx, validProfileForTest("rrpf_context_create", "Create"))
				return err
			},
		},
		{
			name: "update", key: "profile:rrpf_context_update",
			call: func(ctx context.Context) error {
				_, err := store.UpdateProfile(
					ctx, "rrpf_context_update", 1,
					func(*RepositoryReviewProfile) error { return nil },
				)
				return err
			},
		},
		{
			name: "assigned", key: "profile-assignment:rrpf_context_assigned",
			call: func(ctx context.Context) error {
				_, err := store.IsProfileAssigned(ctx, "rrpf_context_assigned")
				return err
			},
		},
		{
			name: "delete", key: "profile:rrpf_context_delete",
			call: func(ctx context.Context) error {
				return store.DeleteProfile(ctx, "rrpf_context_delete", 1)
			},
		},
	}
}

func TestRepositoryReviewProfilePublicErrorBoundaries(t *testing.T) {
	t.Run("pre-canceled context", func(t *testing.T) {
		store := NewStore(t.TempDir())
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		for _, test := range profilePublicCalls(store) {
			t.Run(test.name, func(t *testing.T) {
				if err := test.call(ctx); !errors.Is(err, context.Canceled) {
					t.Fatalf("operation error = %v, want context.Canceled", err)
				}
			})
		}
	})

	t.Run("context canceled after lock", func(t *testing.T) {
		store := NewStore(t.TempDir())
		for _, test := range profilePublicCalls(store) {
			t.Run(test.name, func(t *testing.T) {
				key := store.root + "\x00" + test.key
				value, _ := storeLocks.LoadOrStore(key, &sync.Mutex{})
				mutex := value.(*sync.Mutex)
				mutex.Lock()
				ctx := &profileCancelAfterFirstContext{
					Context: context.Background(),
					first:   make(chan struct{}),
				}
				done := make(chan error, 1)
				go func() { done <- test.call(ctx) }()
				<-ctx.first
				ctx.canceled.Store(true)
				mutex.Unlock()
				if err := <-done; !errors.Is(err, context.Canceled) {
					t.Fatalf("operation error = %v, want context.Canceled", err)
				}
			})
		}
	})

	t.Run("lock failure", func(t *testing.T) {
		restoreRepositoryReviewLockHooks(t)
		sentinel := errors.New("injected profile lock failure")
		repositoryReviewMkdirLockDir = func(string, os.FileMode) error { return sentinel }
		store := NewStore(t.TempDir())
		for _, test := range profilePublicCalls(store) {
			t.Run(test.name, func(t *testing.T) {
				if err := test.call(context.Background()); !errors.Is(err, sentinel) {
					t.Fatalf("operation error = %v, want injected failure", err)
				}
			})
		}
	})

	t.Run("invalid arguments", func(t *testing.T) {
		store := NewStore(t.TempDir())
		if _, _, err := store.GetProfile(context.Background(), "bad"); !errors.Is(err, ErrInvalidProfile) {
			t.Fatalf("GetProfile() error = %v", err)
		}
		if _, err := store.CreateProfile(
			context.Background(), validProfileForTest("bad", "Bad"),
		); !errors.Is(err, ErrInvalidProfile) {
			t.Fatalf("CreateProfile() error = %v", err)
		}
		if _, err := store.UpdateProfile(
			context.Background(), "rrpf_update", 1, nil,
		); !errors.Is(err, ErrInvalidProfile) {
			t.Fatalf("UpdateProfile() error = %v", err)
		}
		if _, err := store.IsProfileAssigned(context.Background(), "bad"); !errors.Is(err, ErrInvalidProfile) {
			t.Fatalf("IsProfileAssigned() error = %v", err)
		}
		if err := store.DeleteProfile(context.Background(), "bad", 1); !errors.Is(err, ErrInvalidProfile) {
			t.Fatalf("DeleteProfile() error = %v", err)
		}
	})
}

func TestRepositoryReviewProfileCreateAndMutationFailures(t *testing.T) {
	t.Run("generated ID and duplicate", func(t *testing.T) {
		store := NewStore(t.TempDir())
		created, err := store.CreateProfile(context.Background(), validProfileForTest("", "Generated"))
		if err != nil {
			t.Fatal(err)
		}
		if !validProfileID(created.ID) || !strings.HasPrefix(created.ID, "rrpf_") {
			t.Fatalf("generated ID = %q", created.ID)
		}
		if _, err := store.CreateProfile(context.Background(), created); !errors.Is(err, ErrConflict) {
			t.Fatalf("duplicate CreateProfile() error = %v", err)
		}
	})

	t.Run("create existing state load failure", func(t *testing.T) {
		store := NewStore(t.TempDir())
		writeProfileCoverageFile(t, store, profileFilename("rrpf_create_corrupt"), []byte("{"))
		if _, err := store.CreateProfile(
			context.Background(), validProfileForTest("rrpf_create_corrupt", "Corrupt"),
		); err == nil {
			t.Fatal("CreateProfile() accepted corrupt existing state")
		}
	})

	t.Run("create normalization failure", func(t *testing.T) {
		store := NewStore(t.TempDir())
		if _, err := store.CreateProfile(
			context.Background(), validProfileForTest("rrpf_create_invalid", ""),
		); !errors.Is(err, ErrInvalidProfile) {
			t.Fatalf("CreateProfile() error = %v", err)
		}
	})

	t.Run("create persistence failure", func(t *testing.T) {
		store := NewStore(t.TempDir())
		poisonProfileStoreOnClock(t, &store)
		if _, err := store.CreateProfile(
			context.Background(), validProfileForTest("rrpf_create_save", "Save"),
		); err == nil {
			t.Fatal("CreateProfile() ignored persistence failure")
		}
	})

	t.Run("update missing", func(t *testing.T) {
		store := NewStore(t.TempDir())
		if _, err := store.UpdateProfile(
			context.Background(), "rrpf_update_missing", 1,
			func(*RepositoryReviewProfile) error { return nil },
		); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("UpdateProfile() error = %v", err)
		}
	})

	t.Run("update state load failure", func(t *testing.T) {
		store := NewStore(t.TempDir())
		writeProfileCoverageFile(t, store, profileFilename("rrpf_update_corrupt"), []byte("{"))
		if _, err := store.UpdateProfile(
			context.Background(), "rrpf_update_corrupt", 1,
			func(*RepositoryReviewProfile) error { return nil },
		); err == nil {
			t.Fatal("UpdateProfile() accepted corrupt state")
		}
	})

	t.Run("update mutation failure", func(t *testing.T) {
		store := NewStore(t.TempDir())
		created, err := store.CreateProfile(
			context.Background(), validProfileForTest("rrpf_update_mutate", "Mutate"),
		)
		if err != nil {
			t.Fatal(err)
		}
		sentinel := errors.New("mutation failed")
		if _, err := store.UpdateProfile(
			context.Background(), created.ID, created.Version,
			func(*RepositoryReviewProfile) error { return sentinel },
		); !errors.Is(err, sentinel) {
			t.Fatalf("UpdateProfile() error = %v", err)
		}
	})

	t.Run("update immutable field", func(t *testing.T) {
		store := NewStore(t.TempDir())
		created, err := store.CreateProfile(
			context.Background(), validProfileForTest("rrpf_update_immutable", "Immutable"),
		)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := store.UpdateProfile(
			context.Background(), created.ID, created.Version,
			func(profile *RepositoryReviewProfile) error {
				profile.ID = "rrpf_changed"
				return nil
			},
		); !errors.Is(err, ErrInvalidProfile) {
			t.Fatalf("UpdateProfile() error = %v", err)
		}
	})

	t.Run("update normalization failure", func(t *testing.T) {
		store := NewStore(t.TempDir())
		created, err := store.CreateProfile(
			context.Background(), validProfileForTest("rrpf_update_invalid", "Invalid"),
		)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := store.UpdateProfile(
			context.Background(), created.ID, created.Version,
			func(profile *RepositoryReviewProfile) error {
				profile.Name = ""
				return nil
			},
		); !errors.Is(err, ErrInvalidProfile) {
			t.Fatalf("UpdateProfile() error = %v", err)
		}
	})

	t.Run("update persistence failure", func(t *testing.T) {
		store := NewStore(t.TempDir())
		created, err := store.CreateProfile(
			context.Background(), validProfileForTest("rrpf_update_save", "Save"),
		)
		if err != nil {
			t.Fatal(err)
		}
		poisonProfileStoreOnClock(t, &store)
		if _, err := store.UpdateProfile(
			context.Background(), created.ID, created.Version,
			func(profile *RepositoryReviewProfile) error {
				profile.Name = "Changed"
				return nil
			},
		); err == nil {
			t.Fatal("UpdateProfile() ignored persistence failure")
		}
	})
}

func TestRepositoryReviewProfileCatalogFailures(t *testing.T) {
	t.Run("missing catalog", func(t *testing.T) {
		profiles, err := NewStore(t.TempDir()).ListProfiles(context.Background())
		if err != nil || len(profiles) != 0 {
			t.Fatalf("ListProfiles() = (%#v, %v)", profiles, err)
		}
	})

	t.Run("unsafe root", func(t *testing.T) {
		store := NewStore(t.TempDir())
		if err := os.WriteFile(store.root, []byte("not a directory"), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := store.ListProfiles(context.Background()); err == nil {
			t.Fatal("ListProfiles() accepted non-directory root")
		}
	})

	t.Run("unreadable catalog", func(t *testing.T) {
		store := NewStore(t.TempDir())
		if err := os.MkdirAll(store.root, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(store.root, 0); err != nil {
			t.Skipf("cannot restrict profile catalog: %v", err)
		}
		_, listErr := store.ListProfiles(context.Background())
		if err := os.Chmod(store.root, 0o700); err != nil {
			t.Fatal(err)
		}
		if listErr == nil {
			t.Skip("filesystem permissions are not enforced")
		}
	})

	t.Run("ignored file and sort order", func(t *testing.T) {
		store := NewStore(t.TempDir())
		oldTime := automationTestNow
		newTime := oldTime.Add(time.Minute)
		store.now = func() time.Time { return oldTime }
		for _, id := range []string{"rrpf_sort_a", "rrpf_sort_z"} {
			if _, err := store.CreateProfile(context.Background(), validProfileForTest(id, id)); err != nil {
				t.Fatal(err)
			}
		}
		store.now = func() time.Time { return newTime }
		if _, err := store.CreateProfile(
			context.Background(), validProfileForTest("rrpf_sort_b", "B"),
		); err != nil {
			t.Fatal(err)
		}
		writeProfileCoverageFile(t, store, "unrelated.json", []byte("{}"))
		profiles, err := store.ListProfiles(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if len(profiles) != 3 || profiles[0].ID != "rrpf_sort_b" ||
			profiles[1].ID != "rrpf_sort_a" || profiles[2].ID != "rrpf_sort_z" {
			t.Fatalf("sorted profiles = %#v", profiles)
		}
	})

	t.Run("invalid filename", func(t *testing.T) {
		store := NewStore(t.TempDir())
		writeProfileCoverageFile(t, store, "profile_rrpf_bad!.json", []byte("{}"))
		if _, err := store.ListProfiles(context.Background()); !errors.Is(err, ErrInvalidProfile) {
			t.Fatalf("ListProfiles() error = %v", err)
		}
	})

	t.Run("catalog limit", func(t *testing.T) {
		store := NewStore(t.TempDir())
		if _, err := store.CreateProfile(
			context.Background(), validProfileForTest("rrpf_catalog_limit", "Limit"),
		); err != nil {
			t.Fatal(err)
		}
		if _, err := store.listProfilesUnlocked(0); !errors.Is(err, ErrInvalidProfile) {
			t.Fatalf("listProfilesUnlocked() error = %v", err)
		}
	})

	t.Run("corrupt entry", func(t *testing.T) {
		store := NewStore(t.TempDir())
		writeProfileCoverageFile(t, store, profileFilename("rrpf_catalog_corrupt"), []byte("{"))
		if _, err := store.ListProfiles(context.Background()); err == nil {
			t.Fatal("ListProfiles() accepted corrupt entry")
		}
	})
}

func TestRepositoryReviewProfileLoadSaveAndAssignmentFailures(t *testing.T) {
	t.Run("load invalid ID", func(t *testing.T) {
		if _, _, err := NewStore(t.TempDir()).loadProfile("bad"); !errors.Is(err, ErrInvalidProfile) {
			t.Fatalf("loadProfile() error = %v", err)
		}
	})

	t.Run("load identity mismatch", func(t *testing.T) {
		store := NewStore(t.TempDir())
		data, err := json.Marshal(profileCoverageFixture("rrpf_other"))
		if err != nil {
			t.Fatal(err)
		}
		writeProfileCoverageFile(t, store, profileFilename("rrpf_identity"), data)
		if _, _, err := store.GetProfile(context.Background(), "rrpf_identity"); err == nil {
			t.Fatal("GetProfile() accepted identity mismatch")
		}
	})

	t.Run("load invalid state", func(t *testing.T) {
		store := NewStore(t.TempDir())
		profile := profileCoverageFixture("rrpf_load_invalid")
		profile.Name = ""
		data, err := json.Marshal(profile)
		if err != nil {
			t.Fatal(err)
		}
		writeProfileCoverageFile(t, store, profileFilename(profile.ID), data)
		if _, _, err := store.GetProfile(context.Background(), profile.ID); !errors.Is(err, ErrInvalidProfile) {
			t.Fatalf("GetProfile() error = %v", err)
		}
	})

	t.Run("save invalid state", func(t *testing.T) {
		profile := profileCoverageFixture("rrpf_save_invalid")
		profile.Name = ""
		if err := NewStore(t.TempDir()).saveProfile(profile); !errors.Is(err, ErrInvalidProfile) {
			t.Fatalf("saveProfile() error = %v", err)
		}
	})

	t.Run("save non-regular destination", func(t *testing.T) {
		store := NewStore(t.TempDir())
		profile := profileCoverageFixture("rrpf_save_directory")
		if err := os.MkdirAll(store.profilePath(profile.ID), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := store.saveProfile(profile); err == nil {
			t.Fatal("saveProfile() accepted directory destination")
		}
	})

	for _, test := range []struct {
		name string
		call func(Store, RepositoryReviewProfile) error
	}{
		{
			name: "assignment lookup",
			call: func(store Store, profile RepositoryReviewProfile) error {
				_, err := store.IsProfileAssigned(context.Background(), profile.ID)
				return err
			},
		},
		{
			name: "active lookup during update",
			call: func(store Store, profile RepositoryReviewProfile) error {
				_, err := store.UpdateProfile(
					context.Background(), profile.ID, profile.Version,
					func(*RepositoryReviewProfile) error { return nil },
				)
				return err
			},
		},
		{
			name: "assignment lookup during delete",
			call: func(store Store, profile RepositoryReviewProfile) error {
				return store.DeleteProfile(context.Background(), profile.ID, profile.Version)
			},
		},
	} {
		t.Run(test.name+" catalog error", func(t *testing.T) {
			store := NewStore(t.TempDir())
			profile, err := store.CreateProfile(
				context.Background(), validProfileForTest("rrpf_catalog_error", "Catalog"),
			)
			if err != nil {
				t.Fatal(err)
			}
			writeProfileCoverageFile(t, store, automationFilename("rra_catalog_error"), []byte("{"))
			if err := test.call(store, profile); err == nil {
				t.Fatal("profile operation ignored corrupt automation catalog")
			}
		})
	}

	t.Run("delete missing", func(t *testing.T) {
		if err := NewStore(t.TempDir()).DeleteProfile(
			context.Background(), "rrpf_delete_missing", 1,
		); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("DeleteProfile() error = %v", err)
		}
	})

	t.Run("delete state load failure", func(t *testing.T) {
		store := NewStore(t.TempDir())
		writeProfileCoverageFile(t, store, profileFilename("rrpf_delete_corrupt"), []byte("{"))
		if err := store.DeleteProfile(context.Background(), "rrpf_delete_corrupt", 1); err == nil {
			t.Fatal("DeleteProfile() accepted corrupt state")
		}
	})
}

func TestNormalizeRepositoryReviewProfileBoundaries(t *testing.T) {
	t.Run("nil", func(t *testing.T) {
		if err := normalizeProfile(nil); !errors.Is(err, ErrInvalidProfile) {
			t.Fatalf("normalizeProfile(nil) error = %v", err)
		}
	})

	t.Run("defaults", func(t *testing.T) {
		profile := profileCoverageFixture("rrpf_defaults")
		profile.MaxFilesPerRun = 0
		profile.MaxContentBytes = 0
		profile.MaxParallelChildren = 0
		if err := normalizeProfile(&profile); err != nil {
			t.Fatal(err)
		}
		if profile.MaxFilesPerRun != defaultAutomationMaxFilesPerRun ||
			profile.MaxContentBytes != defaultAutomationMaxContentBytes ||
			profile.MaxParallelChildren != defaultAutomationMaxParallelChildren {
			t.Fatalf("normalized defaults = %#v", profile)
		}
	})

	tests := []struct {
		name   string
		mutate func(*RepositoryReviewProfile)
	}{
		{name: "guard expression", mutate: func(profile *RepositoryReviewProfile) {
			profile.BudgetPolicy.GuardExpression = "account.limits.*"
		}},
		{
			name: "scope policy",
			mutate: func(profile *RepositoryReviewProfile) {
				profile.ScopePolicy.CodeTypes = []RepositoryReviewCodeType{"invalid"}
			},
		},
		{
			name:   "required fields",
			mutate: func(profile *RepositoryReviewProfile) { profile.Name = "" },
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			profile := profileCoverageFixture("rrpf_normalize_error")
			test.mutate(&profile)
			if err := normalizeProfile(&profile); !errors.Is(err, ErrInvalidProfile) {
				t.Fatalf("normalizeProfile() error = %v", err)
			}
		})
	}

	t.Run("materialize invalid profile", func(t *testing.T) {
		profile := profileCoverageFixture("rrpf_materialize_invalid")
		profile.Name = ""
		if _, err := MaterializeRepositoryReviewAutomation(
			profile, validAutomationForTest("rra_materialize_invalid", "Invalid"),
		); !errors.Is(err, ErrInvalidProfile) {
			t.Fatalf("MaterializeRepositoryReviewAutomation() error = %v", err)
		}
	})

	t.Run("materialize invalid branch", func(t *testing.T) {
		automation := validAutomationForTest("rra_materialize_branch", "Branch")
		automation.Ref = "HEAD"
		if _, err := MaterializeRepositoryReviewAutomation(
			profileCoverageFixture("rrpf_materialize_branch"), automation,
		); err == nil {
			t.Fatal("MaterializeRepositoryReviewAutomation() accepted HEAD")
		}
	})

	for _, test := range []struct {
		id   string
		want bool
	}{
		{id: "rrpf_a", want: true},
		{id: "rrpf_a_b-1", want: true},
		{id: "rrpf_", want: false},
		{id: "profile_a", want: false},
		{id: "rrpf__bad", want: false},
		{id: "rrpf_bad!", want: false},
	} {
		if got := validProfileID(test.id); got != test.want {
			t.Errorf("validProfileID(%q) = %v, want %v", test.id, got, test.want)
		}
	}
}

func TestRepositoryReviewProfileAssignmentHelperFailures(t *testing.T) {
	t.Run("automation profile validation", func(t *testing.T) {
		store := NewStore(t.TempDir())
		created, err := store.CreateAutomation(
			context.Background(), validAutomationForTest("rra_profile_validation", "Validation"),
		)
		if err != nil {
			t.Fatal(err)
		}
		created.ProfileVersion = 1
		if err := validateAutomation(created); !errors.Is(err, ErrInvalidAutomation) {
			t.Fatalf("profile version without ID error = %v", err)
		}
		created.ProfileID = "rrpf_missing"
		if err := validateAutomation(created); !errors.Is(err, ErrInvalidAutomation) {
			t.Fatalf("invalid profile assignment error = %v", err)
		}
	})

	t.Run("profile snapshot load failure", func(t *testing.T) {
		store := NewStore(t.TempDir())
		writeProfileCoverageFile(t, store, profileFilename("rrpf_snapshot_corrupt"), []byte("{"))
		automation := validAutomationForTest("rra_snapshot_corrupt", "Corrupt")
		automation.ProfileID = "rrpf_snapshot_corrupt"
		automation.ProfileVersion = 1
		if err := store.validateAutomationProfileSnapshotUnlocked(automation); err == nil {
			t.Fatal("profile snapshot accepted corrupt profile")
		}
	})

	t.Run("profile snapshot missing", func(t *testing.T) {
		store := NewStore(t.TempDir())
		automation := validAutomationForTest("rra_snapshot_missing", "Missing")
		automation.ProfileID = "rrpf_snapshot_missing"
		automation.ProfileVersion = 1
		if err := store.validateAutomationProfileSnapshotUnlocked(automation); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("missing profile snapshot error = %v", err)
		}
	})

	t.Run("profile snapshot materialization failure", func(t *testing.T) {
		store := NewStore(t.TempDir())
		profile, err := store.CreateProfile(
			context.Background(), validProfileForTest("rrpf_snapshot_branch", "Branch"),
		)
		if err != nil {
			t.Fatal(err)
		}
		automation := validAutomationForTest("rra_snapshot_branch", "Branch")
		automation.ProfileID = profile.ID
		automation.ProfileVersion = profile.Version
		automation.Ref = "HEAD"
		if err := store.validateAutomationProfileSnapshotUnlocked(automation); err == nil {
			t.Fatal("profile snapshot accepted invalid branch")
		}
	})

	t.Run("repository uniqueness validation", func(t *testing.T) {
		store := NewStore(t.TempDir())
		if err := store.ensureRepositoryAutomationUniqueUnlocked("rra_empty", ""); !errors.Is(
			err, ErrInvalidAutomation,
		) {
			t.Fatalf("empty canonical repository error = %v", err)
		}
		writeProfileCoverageFile(t, store, automationFilename("rra_uniqueness_corrupt"), []byte("{"))
		if err := store.ensureRepositoryAutomationUniqueUnlocked(
			"rra_unique", "owner/unique",
		); err == nil {
			t.Fatal("repository uniqueness ignored corrupt catalog")
		}
	})

	t.Run("canonical repository forms", func(t *testing.T) {
		if got := canonicalAutomationRepository(""); got != "" {
			t.Fatalf("empty canonical repository = %q", got)
		}
		if got := canonicalAutomationRepository("/tmp/../repo"); got != "/repo" {
			t.Fatalf("absolute canonical repository = %q", got)
		}
		if got := canonicalAutomationRepository("github.com:Owner/Repo.git"); got != "owner/repo" {
			t.Fatalf("host canonical repository = %q", got)
		}
	})

	t.Run("finalize lock failure", func(t *testing.T) {
		store := NewStore(t.TempDir())
		plan, err := store.PlanWithProfileLimitAuthoritative(
			context.Background(), "owner/profile-lock", "commit", "inventory", "profile",
			nil, false, 1, true,
		)
		if err != nil {
			t.Fatal(err)
		}
		restoreRepositoryReviewLockHooks(t)
		sentinel := errors.New("injected finalize lock failure")
		repositoryReviewMkdirLockDir = func(string, os.FileMode) error { return sentinel }
		if _, err := store.FinalizeNoopPlan(plan); !errors.Is(err, sentinel) {
			t.Fatalf("FinalizeNoopPlan() error = %v", err)
		}
	})
}
