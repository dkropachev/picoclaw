package repoaudit

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestRepositoryReviewProfileLoadRemovesLegacyModelPrice(t *testing.T) {
	store := NewStore(t.TempDir())
	store.now = func() time.Time { return automationTestNow }
	created, err := store.CreateProfile(
		context.Background(),
		validProfileForTest("rrpf_legacy_price", "Legacy price"),
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
	raw["model_price"] = map[string]any{
		"input_price_per_1m":  1,
		"output_price_per_1m": 4,
		"subscription":        true,
		"equivalent_model":    "metered-review",
	}
	raw["budget"] = map[string]any{
		"max_total_tokens": 1000, "account_ids": []string{"openai:work"},
		"pause_on_unknown": true, "check_interval_seconds": 30,
	}
	data, err = json.Marshal(raw)
	if err != nil {
		t.Fatal(err)
	}
	if writeErr := os.WriteFile(path, data, 0o600); writeErr != nil {
		t.Fatal(writeErr)
	}
	loaded, found, getErr := store.GetProfile(context.Background(), created.ID)
	if getErr != nil || !found {
		t.Fatalf("GetProfile() found=%v error=%v", found, getErr)
	}
	if loaded.AccountRef != "" ||
		!strings.Contains(loaded.BudgetPolicy.GuardExpression, "spent.tokens.total < 1000") ||
		!strings.Contains(loaded.BudgetPolicy.GuardExpression, "false") {
		t.Fatalf("legacy guard migration=%#v", loaded)
	}
	rewritten, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(rewritten, []byte(`"model_price"`)) {
		t.Fatalf("legacy model_price was not removed: %s", rewritten)
	}
	for _, retired := range [][]byte{[]byte(`"max_total_tokens"`), []byte(`"account_ids"`), []byte(`"check_interval_seconds"`)} {
		if bytes.Contains(rewritten, retired) {
			t.Fatalf("legacy guard field %s was not removed: %s", retired, rewritten)
		}
	}
}

func TestRepositoryReviewProfileV1MigratesDefaultIssuePrompt(t *testing.T) {
	store := NewStore(t.TempDir())
	store.now = func() time.Time { return automationTestNow }
	created, err := store.CreateProfile(
		context.Background(), validProfileForTest("rrpf_issue_prompt_v1", "Legacy prompt"),
	)
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(store.profilePath(created.ID))
	if err != nil {
		t.Fatal(err)
	}
	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatal(err)
	}
	raw["schema_version"] = float64(1)
	delete(raw, "issue_prompt")
	data, err = json.Marshal(raw)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(store.profilePath(created.ID), data, 0o600); err != nil {
		t.Fatal(err)
	}

	loaded, found, err := store.GetProfile(context.Background(), created.ID)
	if err != nil || !found || loaded.SchemaVersion != RepositoryReviewProfileSchemaVersion ||
		loaded.IssuePrompt != DefaultRepositoryReviewIssuePrompt {
		t.Fatalf("migrated profile=%#v found=%v err=%v", loaded, found, err)
	}
	rewritten, err := os.ReadFile(store.profilePath(created.ID))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(rewritten, []byte(`"schema_version":2`)) ||
		!bytes.Contains(rewritten, []byte(`"issue_prompt"`)) {
		t.Fatalf("v1 profile was not durably rewritten: %s", rewritten)
	}
}

func TestRepositoryReviewProfileCRUDCASAndPrivateFile(t *testing.T) {
	workspace := t.TempDir()
	store := NewStore(workspace)
	store.now = func() time.Time { return automationTestNow }

	created, err := store.CreateProfile(context.Background(), validProfileForTest("rrpf_crud", "Primary"))
	if err != nil {
		t.Fatal(err)
	}
	if created.ID != "rrpf_crud" || created.Version != 1 ||
		created.SchemaVersion != RepositoryReviewProfileSchemaVersion ||
		created.IssuePrompt != DefaultRepositoryReviewIssuePrompt ||
		!created.CreatedAt.Equal(automationTestNow) || !created.UpdatedAt.Equal(automationTestNow) {
		t.Fatalf("created profile = %#v", created)
	}
	info, err := os.Stat(store.profilePath(created.ID))
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("profile mode = %o, want 600", got)
	}

	loaded, found, err := NewStore(workspace).GetProfile(context.Background(), created.ID)
	if err != nil || !found || loaded.ID != created.ID {
		t.Fatalf("GetProfile() = (%#v, %v, %v)", loaded, found, err)
	}
	loaded.ScopePolicy.CodeTypes[0] = RepositoryReviewCodeTypeTest
	reloaded, _, err := store.GetProfile(context.Background(), created.ID)
	if err != nil || reloaded.ScopePolicy.CodeTypes[0] == RepositoryReviewCodeTypeTest {
		t.Fatalf("GetProfile() did not detach state: %#v, %v", reloaded, err)
	}

	if _, updateErr := store.UpdateProfile(
		context.Background(), created.ID, created.Version+1,
		func(profile *RepositoryReviewProfile) error { profile.Name = "stale"; return nil },
	); !errors.Is(updateErr, ErrConflict) {
		t.Fatalf("stale UpdateProfile() error = %v", updateErr)
	}
	updatedAt := automationTestNow.Add(time.Minute)
	store.now = func() time.Time { return updatedAt }
	updated, err := store.UpdateProfile(
		context.Background(), created.ID, created.Version,
		func(profile *RepositoryReviewProfile) error { profile.Name = "Updated"; return nil },
	)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Version != 2 || updated.Name != "Updated" || !updated.UpdatedAt.Equal(updatedAt) {
		t.Fatalf("updated profile = %#v", updated)
	}
	listed, err := store.ListProfiles(context.Background())
	if err != nil || len(listed) != 1 || listed[0].ID != created.ID {
		t.Fatalf("ListProfiles() = (%#v, %v)", listed, err)
	}
	if err := store.DeleteProfile(context.Background(), created.ID, created.Version); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale DeleteProfile() error = %v", err)
	}
	if err := store.DeleteProfile(context.Background(), created.ID, updated.Version); err != nil {
		t.Fatal(err)
	}
	if _, found, err := store.GetProfile(context.Background(), created.ID); err != nil || found {
		t.Fatalf("deleted GetProfile() = (%v, %v)", found, err)
	}
}

func TestRepositoryReviewProfileRejectsSymlinkFile(t *testing.T) {
	store := NewStore(t.TempDir())
	if err := os.MkdirAll(store.root, 0o700); err != nil {
		t.Fatal(err)
	}
	target := store.profilePath("rrpf_target")
	if err := os.WriteFile(target, []byte(`{}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, store.profilePath("rrpf_link")); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.GetProfile(context.Background(), "rrpf_link"); err == nil {
		t.Fatal("GetProfile() accepted a symlink")
	}
	if _, listErr := store.ListProfiles(context.Background()); listErr == nil {
		t.Fatal("ListProfiles() accepted a symlink")
	}
}

func TestNormalizeRepositoryReviewBranch(t *testing.T) {
	for _, valid := range []string{"", "main", "release/2026.08", "feature/review-ui"} {
		got, err := NormalizeRepositoryReviewBranch(valid)
		if err != nil || got != valid {
			t.Errorf("NormalizeRepositoryReviewBranch(%q) = (%q, %v)", valid, got, err)
		}
	}
	sha40 := "0123456789abcdef0123456789abcdef01234567"
	sha64 := sha40 + "0123456789abcdef01234567"
	for _, invalid := range []string{
		"HEAD", "head", "0123456", "0123456789ab", sha40, sha64,
		"refs/heads/main", "refs/tags/v1", "tags/v1",
		"main~1", "main^", "main@{1}", "https://example.com/repo", "git@example.com:repo",
		"main#fragment", "fix#123", " main", "main ", "feature name", "feature\nname", "-main", ".hidden", "main.lock",
		"feature//name", "feature/../main", "feature/", "feature\\name",
	} {
		if got, err := NormalizeRepositoryReviewBranch(invalid); err == nil {
			t.Errorf("NormalizeRepositoryReviewBranch(%q) = %q, want error", invalid, got)
		}
	}
	if got, err := NormalizeRepositoryReviewBranch("  \t"); err != nil || got != "" {
		t.Fatalf("blank branch = (%q, %v)", got, err)
	}
}

func TestMaterializeRepositoryReviewAutomation(t *testing.T) {
	store := NewStore(t.TempDir())
	profile, err := store.CreateProfile(context.Background(), validProfileForTest("rrpf_materialize", "Strict"))
	if err != nil {
		t.Fatal(err)
	}
	automation := validAutomationForTest("rra_materialize", "Repository")
	automation.Ref = "release/next"
	automation.Target = "src/..."
	automation.Status = RepositoryReviewAutomationIdle
	materialized, err := MaterializeRepositoryReviewAutomation(profile, automation)
	if err != nil {
		t.Fatal(err)
	}
	if materialized.ProfileID != profile.ID || materialized.ProfileVersion != profile.Version ||
		materialized.Ref != "release/next" || materialized.Target != "all" ||
		len(materialized.ReviewerModels) != 1 || materialized.ReviewerModels[0] != profile.ReviewerModel ||
		materialized.IssueWriterModel != profile.ReviewerModel ||
		materialized.CompareModels || materialized.ReviewFocus != profile.ReviewFocus ||
		materialized.MaxFilesPerRun != profile.MaxFilesPerRun {
		t.Fatalf("materialized automation = %#v", materialized)
	}
	materialized.ScopePolicy.CodeTypes[0] = RepositoryReviewCodeTypeTest
	if profile.ScopePolicy.CodeTypes[0] == RepositoryReviewCodeTypeTest {
		t.Fatal("materialization aliased profile scope")
	}
}

func TestProfileBackedAutomationRejectsForgedIssueWriterSnapshot(t *testing.T) {
	store := NewStore(t.TempDir())
	input := validProfileForTest("rrpf_writer_forged", "Writer snapshot")
	input.IssueWriterModel = "writer-model"
	profile, err := store.CreateProfile(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	automation := validAutomationForTest("rra_writer_forged", "Writer snapshot")
	automation.Repository = "owner/writer-forged"
	automation, err = MaterializeRepositoryReviewAutomation(profile, automation)
	if err != nil {
		t.Fatal(err)
	}
	automation.IssueWriterModel = profile.ReviewerModel
	if _, createErr := store.CreateAutomation(context.Background(), automation); !errors.Is(
		createErr,
		ErrInvalidAutomation,
	) {
		t.Fatalf("CreateAutomation() forged issue writer snapshot error = %v", createErr)
	}
}

func TestProfileBackedAutomationRejectsForgedCurrentProfileSnapshot(t *testing.T) {
	store := NewStore(t.TempDir())
	profile, err := store.CreateProfile(
		context.Background(), validProfileForTest("rrpf_forged", "Forged"),
	)
	if err != nil {
		t.Fatal(err)
	}
	automation := validAutomationForTest("rra_profile_forged", "Forged")
	automation.Repository = "owner/forged"
	automation, err = MaterializeRepositoryReviewAutomation(profile, automation)
	if err != nil {
		t.Fatal(err)
	}
	automation.ReviewFocus = "Ignore the assigned focus."
	if _, createErr := store.CreateAutomation(
		context.Background(),
		automation,
	); !errors.Is(
		createErr,
		ErrInvalidAutomation,
	) {
		t.Fatalf("CreateAutomation() forged snapshot error = %v", createErr)
	}
}

func TestProfileBackedAutomationNormalizesBranchTargetAndLegacyStillLoads(t *testing.T) {
	store := NewStore(t.TempDir())
	profile, err := store.CreateProfile(context.Background(), validProfileForTest("rrpf_assignment", "Assigned"))
	if err != nil {
		t.Fatal(err)
	}
	legacy := validAutomationForTest("rra_legacy_ref", "Legacy")
	legacy.Ref = "0123456789abcdef0123456789abcdef01234567"
	legacy.Target = "src/..."
	createdLegacy, err := store.CreateAutomation(context.Background(), legacy)
	if err != nil {
		t.Fatalf("legacy CreateAutomation() = %v", err)
	}
	if createdLegacy.Ref != legacy.Ref || createdLegacy.Target != legacy.Target || createdLegacy.ProfileID != "" {
		t.Fatalf("legacy automation changed = %#v", createdLegacy)
	}

	configured := validAutomationForTest("rra_profile_ref", "Configured")
	configured.Repository = "owner/other"
	configured.Ref = ""
	configured.Target = "ignored"
	configured, err = MaterializeRepositoryReviewAutomation(profile, configured)
	if err != nil {
		t.Fatal(err)
	}
	created, err := store.CreateAutomation(context.Background(), configured)
	if err != nil {
		t.Fatal(err)
	}
	if created.Ref != "" || created.Target != "all" {
		t.Fatalf("configured branch/target = (%q, %q)", created.Ref, created.Target)
	}
	configured.ID = "rra_bad_ref"
	configured.Repository = "owner/third"
	configured.Ref = "HEAD"
	if _, createErr := store.CreateAutomation(
		context.Background(),
		configured,
	); !errors.Is(
		createErr,
		ErrInvalidAutomation,
	) {
		t.Fatalf("profile-backed HEAD error = %v", createErr)
	}
}

func TestProfileBackedAutomationConcurrentRepositoryConflict(t *testing.T) {
	workspace := t.TempDir()
	profile, err := NewStore(workspace).CreateProfile(
		context.Background(), validProfileForTest("rrpf_concurrent", "Concurrent"),
	)
	if err != nil {
		t.Fatal(err)
	}
	start := make(chan struct{})
	errorsByCall := make(chan error, 2)
	var wait sync.WaitGroup
	for index, repository := range []string{"Owner/Repo.git", "https://github.com/owner/repo"} {
		wait.Add(1)
		go func(index int, repository string) {
			defer wait.Done()
			candidate := validAutomationForTest("rra_concurrent_"+automationTestIndex(index+1), "Concurrent")
			candidate.Repository = repository
			candidate, materializeErr := MaterializeRepositoryReviewAutomation(profile, candidate)
			if materializeErr != nil {
				errorsByCall <- materializeErr
				return
			}
			<-start
			_, createErr := NewStore(workspace).CreateAutomation(context.Background(), candidate)
			errorsByCall <- createErr
		}(index, repository)
	}
	close(start)
	wait.Wait()
	close(errorsByCall)
	successes, conflicts := 0, 0
	for err := range errorsByCall {
		switch {
		case err == nil:
			successes++
		case errors.Is(err, ErrRepositoryReviewRepositoryConflict):
			conflicts++
		default:
			t.Fatalf("concurrent CreateAutomation() error = %v", err)
		}
	}
	if successes != 1 || conflicts != 1 {
		t.Fatalf("concurrent results = %d success, %d conflict", successes, conflicts)
	}
}

func TestProfileBackedAutomationCanonicalRepositoryConflicts(t *testing.T) {
	testCases := []struct {
		name       string
		firstRepo  string
		secondRepo string
	}{
		{
			name:       "trailing git suffix",
			firstRepo:  "https://github.com/acme/repo.git/",
			secondRepo: "ACME/REPO",
		},
		{
			name:       "remote transport",
			firstRepo:  "git@gitlab.com:group/repo.git",
			secondRepo: "https://gitlab.com/group/repo",
		},
	}
	for index, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			store := NewStore(t.TempDir())
			suffix := automationTestIndex(index + 1)
			profile, err := store.CreateProfile(
				context.Background(),
				validProfileForTest("rrpf_canonical_"+suffix, "Canonical"),
			)
			if err != nil {
				t.Fatal(err)
			}
			first := validAutomationForTest("rra_canonical_first_"+suffix, "First")
			first.Repository = testCase.firstRepo
			first, err = MaterializeRepositoryReviewAutomation(profile, first)
			if err != nil {
				t.Fatal(err)
			}
			if _, createErr := store.CreateAutomation(context.Background(), first); createErr != nil {
				t.Fatal(createErr)
			}
			second := validAutomationForTest("rra_canonical_second_"+suffix, "Second")
			second.Repository = testCase.secondRepo
			second, err = MaterializeRepositoryReviewAutomation(profile, second)
			if err != nil {
				t.Fatal(err)
			}
			if _, createErr := store.CreateAutomation(context.Background(), second); !errors.Is(
				createErr,
				ErrRepositoryReviewRepositoryConflict,
			) {
				t.Fatalf("CreateAutomation() conflict error = %v", createErr)
			}
		})
	}
}

func TestProfileBackedAutomationUpdateConflictIsAtomic(t *testing.T) {
	store := NewStore(t.TempDir())
	profile, err := store.CreateProfile(context.Background(), validProfileForTest("rrpf_update", "Update"))
	if err != nil {
		t.Fatal(err)
	}
	first := validAutomationForTest("rra_update_first", "First")
	first.Repository = "owner/first"
	first, err = MaterializeRepositoryReviewAutomation(profile, first)
	if err != nil {
		t.Fatal(err)
	}
	first, err = store.CreateAutomation(context.Background(), first)
	if err != nil {
		t.Fatal(err)
	}
	second, err := store.CreateAutomation(
		context.Background(), func() RepositoryReviewAutomation {
			value := validAutomationForTest("rra_update_second", "Second")
			value.Repository = "owner/second"
			return value
		}(),
	)
	if err != nil {
		t.Fatal(err)
	}
	_, err = store.UpdateAutomation(
		context.Background(), second.ID, second.Version,
		func(value *RepositoryReviewAutomation) error {
			value.Repository = "OWNER/FIRST.git"
			materialized, materializeErr := MaterializeRepositoryReviewAutomation(profile, *value)
			if materializeErr != nil {
				return materializeErr
			}
			*value = materialized
			return nil
		},
	)
	if !errors.Is(err, ErrRepositoryReviewRepositoryConflict) {
		t.Fatalf("UpdateAutomation() error = %v", err)
	}
	unchanged, _, err := store.GetAutomation(context.Background(), second.ID)
	if err != nil || unchanged.Version != second.Version || unchanged.Repository != second.Repository ||
		unchanged.ProfileID != "" {
		t.Fatalf("conflicting update persisted = (%#v, %v)", unchanged, err)
	}
	_ = first
}

func TestProfileBackedAutomationRequiresCurrentProfileSnapshot(t *testing.T) {
	store := NewStore(t.TempDir())
	profile, err := store.CreateProfile(
		context.Background(), validProfileForTest("rrpf_snapshot", "Snapshot"),
	)
	if err != nil {
		t.Fatal(err)
	}
	automation := validAutomationForTest("rra_profile_snapshot", "Snapshot")
	automation.Repository = "owner/snapshot"
	automation, err = MaterializeRepositoryReviewAutomation(profile, automation)
	if err != nil {
		t.Fatal(err)
	}
	updatedProfile, err := store.UpdateProfile(
		context.Background(), profile.ID, profile.Version,
		func(candidate *RepositoryReviewProfile) error {
			candidate.Name = "Snapshot v2"
			return nil
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, createErr := store.CreateAutomation(context.Background(), automation); !errors.Is(createErr, ErrConflict) {
		t.Fatalf("CreateAutomation() stale profile error = %v", createErr)
	}
	automation, err = MaterializeRepositoryReviewAutomation(updatedProfile, automation)
	if err != nil {
		t.Fatal(err)
	}
	created, err := store.CreateAutomation(context.Background(), automation)
	if err != nil {
		t.Fatal(err)
	}
	latestProfile, err := store.UpdateProfile(
		context.Background(), updatedProfile.ID, updatedProfile.Version,
		func(candidate *RepositoryReviewProfile) error {
			candidate.Name = "Snapshot v3"
			return nil
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	runtimeUpdated, err := store.UpdateAutomation(
		context.Background(), created.ID, created.Version,
		func(candidate *RepositoryReviewAutomation) error {
			candidate.Progress.Stage = "waiting"
			return nil
		},
	)
	if err != nil || runtimeUpdated.Progress.Stage != "waiting" {
		t.Fatalf("runtime-only stale-profile update = (%#v, %v)", runtimeUpdated, err)
	}
	_, err = store.UpdateAutomation(
		context.Background(), runtimeUpdated.ID, runtimeUpdated.Version,
		func(candidate *RepositoryReviewAutomation) error {
			candidate.Status = RepositoryReviewAutomationRunning
			candidate.ActiveRunID = "wfr_stale_profile"
			candidate.RunIDs = append(candidate.RunIDs, candidate.ActiveRunID)
			return nil
		},
	)
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("UpdateAutomation() stale-profile admission error = %v", err)
	}
	_, err = store.UpdateAutomation(
		context.Background(), runtimeUpdated.ID, runtimeUpdated.Version,
		func(candidate *RepositoryReviewAutomation) error {
			candidate.ReviewFocus = "forged stale materialization"
			return nil
		},
	)
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("UpdateAutomation() stale profile error = %v", err)
	}
	if latestProfile.Version != 3 {
		t.Fatalf("latest profile version = %d", latestProfile.Version)
	}
}

func TestDeleteAssignedRepositoryReviewProfileIsBlocked(t *testing.T) {
	store := NewStore(t.TempDir())
	profile, err := store.CreateProfile(context.Background(), validProfileForTest("rrpf_delete_assigned", "Assigned"))
	if err != nil {
		t.Fatal(err)
	}
	automation := validAutomationForTest("rra_delete_assigned", "Assigned")
	automation, err = MaterializeRepositoryReviewAutomation(profile, automation)
	if err != nil {
		t.Fatal(err)
	}
	if _, createErr := store.CreateAutomation(context.Background(), automation); createErr != nil {
		t.Fatal(createErr)
	}
	assigned, err := store.IsProfileAssigned(context.Background(), profile.ID)
	if err != nil || !assigned {
		t.Fatalf("IsProfileAssigned() = (%v, %v)", assigned, err)
	}
	if err := store.DeleteProfile(
		context.Background(),
		profile.ID,
		profile.Version,
	); !errors.Is(
		err,
		ErrProfileAssigned,
	) {
		t.Fatalf("DeleteProfile() error = %v", err)
	}
}

func TestUpdateRepositoryReviewProfileIsBlockedByActiveAssignment(t *testing.T) {
	store := NewStore(t.TempDir())
	profile, err := store.CreateProfile(
		context.Background(), validProfileForTest("rrpf_active", "Active"),
	)
	if err != nil {
		t.Fatal(err)
	}
	automation := validAutomationForTest("rra_profile_active", "Active")
	automation.Repository = "owner/active"
	automation, err = MaterializeRepositoryReviewAutomation(profile, automation)
	if err != nil {
		t.Fatal(err)
	}
	automation.Status = RepositoryReviewAutomationRunning
	automation.ActiveRunID = "wfr_active"
	automation.RunIDs = []string{automation.ActiveRunID}
	created, createErr := store.CreateAutomation(context.Background(), automation)
	if createErr != nil {
		t.Fatal(createErr)
	}
	if deleteErr := store.DeleteAutomation(
		context.Background(),
		created.ID,
		created.Version,
	); !errors.Is(deleteErr, ErrAutomationActive) {
		t.Fatalf("DeleteAutomation() active error = %v", deleteErr)
	}
	_, err = store.UpdateProfile(
		context.Background(), profile.ID, profile.Version,
		func(candidate *RepositoryReviewProfile) error {
			candidate.Name = "Changed"
			return nil
		},
	)
	if !errors.Is(err, ErrProfileActive) {
		t.Fatalf("UpdateProfile() error = %v", err)
	}
}

func validProfileForTest(id, name string) RepositoryReviewProfile {
	return RepositoryReviewProfile{
		ID: id, Name: name, ReviewFocus: "Find concrete bugs.",
		ScopePolicy: RepositoryReviewScopePolicy{
			CodeTypes: []RepositoryReviewCodeType{RepositoryReviewCodeTypeCode},
		},
		ReviewerModel: "review-a",
		AutoContinue:  true, MaxFilesPerRun: 12, MaxContentBytes: 64 << 10,
		MaxParallelChildren: 1,
		BudgetPolicy:        RepositoryReviewBudgetPolicy{},
	}
}
