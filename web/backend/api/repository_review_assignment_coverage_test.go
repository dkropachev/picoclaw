package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/repoaudit"
)

type repositoryReviewThirdErrorContext struct {
	calls atomic.Int32
}

func (ctx *repositoryReviewThirdErrorContext) Deadline() (time.Time, bool) { return time.Time{}, false }
func (ctx *repositoryReviewThirdErrorContext) Done() <-chan struct{}       { return nil }
func (ctx *repositoryReviewThirdErrorContext) Value(any) any               { return nil }
func (ctx *repositoryReviewThirdErrorContext) Err() error {
	if ctx.calls.Add(1) >= 3 {
		return context.Canceled
	}
	return nil
}

func TestRepositoryReviewAssignmentAdmissionFailureBranches(t *testing.T) {
	type admissionFixture struct {
		handler    *Handler
		store      repoaudit.Store
		controller *repositoryReviewController
		automation repoaudit.RepositoryReviewAutomation
		workspace  string
		commit     string
	}
	prepare := func(t *testing.T, suffix string) admissionFixture {
		t.Helper()
		handler, _, workspace := newRepositoryReviewAutomationTestHandler(t)
		t.Cleanup(handler.Shutdown)
		store, err := handler.repositoryReviewStore()
		if err != nil {
			t.Fatal(err)
		}
		input := testRepositoryReviewAutomation()
		input.Repository = "https://github.com/acme/assignment-" + suffix + ".git"
		automation, err := store.CreateAutomation(t.Context(), input)
		if err != nil {
			t.Fatal(err)
		}
		commit := strings.Repeat("a", 40)
		controller := handler.repositoryReviewControllerInstance()
		controller.resolveCommit = func(
			context.Context,
			*config.Config,
			repoaudit.RepositoryReviewAutomation,
			string,
		) (string, error) {
			return commit, nil
		}
		return admissionFixture{
			handler: handler, store: store, controller: controller,
			automation: automation, workspace: workspace, commit: commit,
		}
	}

	t.Run("budget persistence", func(t *testing.T) {
		fixture := prepare(t, "budget-failure")
		sentinel := errors.New("injected budget reset failure")
		calls := 0
		fixture.controller.update = func(
			ctx context.Context,
			store repoaudit.Store,
			id string,
			version int64,
			mutate func(*repoaudit.RepositoryReviewAutomation) error,
		) (repoaudit.RepositoryReviewAutomation, error) {
			calls++
			if calls == 2 {
				return repoaudit.RepositoryReviewAutomation{}, sentinel
			}
			return updateRepositoryReviewAutomation(ctx, store, id, version, mutate)
		}
		if _, err := fixture.controller.startAutomation(
			t.Context(), fixture.automation.ID, fixture.automation.Version, true, "start",
		); !errors.Is(err, sentinel) {
			t.Fatalf("budget persistence error = %v", err)
		}
	})

	t.Run("abandoned reservation interrupt", func(t *testing.T) {
		fixture := prepare(t, "interrupt-failure")
		calls := 0
		lockPath := filepath.Join(
			fixture.workspace,
			"repository_reviews",
			".locks",
			"store.lock",
		)
		fixture.controller.update = func(
			ctx context.Context,
			store repoaudit.Store,
			id string,
			version int64,
			mutate func(*repoaudit.RepositoryReviewAutomation) error,
		) (repoaudit.RepositoryReviewAutomation, error) {
			calls++
			updated, err := updateRepositoryReviewAutomation(ctx, store, id, version, mutate)
			if err == nil && calls == 1 {
				if removeErr := os.Remove(lockPath); removeErr != nil && !os.IsNotExist(removeErr) {
					t.Fatal(removeErr)
				}
				if mkdirErr := os.Mkdir(lockPath, 0o700); mkdirErr != nil {
					t.Fatal(mkdirErr)
				}
			}
			return updated, err
		}
		if _, err := fixture.controller.startAutomation(
			t.Context(), fixture.automation.ID, fixture.automation.Version, false, "start",
		); err == nil {
			t.Fatal("irregular assignment lock was accepted")
		}
		if err := os.RemoveAll(lockPath); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("ledger read", func(t *testing.T) {
		fixture := prepare(t, "ledger-failure")
		calls := 0
		fixture.controller.update = func(
			ctx context.Context,
			store repoaudit.Store,
			id string,
			version int64,
			mutate func(*repoaudit.RepositoryReviewAutomation) error,
		) (repoaudit.RepositoryReviewAutomation, error) {
			calls++
			updated, err := updateRepositoryReviewAutomation(ctx, store, id, version, mutate)
			if err == nil && calls == 2 {
				if writeErr := os.WriteFile(
					filepath.Join(
						fixture.workspace, "repository_reviews", "repository-reviews.db",
					),
					[]byte("not-sqlite"),
					0o600,
				); writeErr != nil {
					t.Fatal(writeErr)
				}
			}
			return updated, err
		}
		if _, err := fixture.controller.startAutomation(
			t.Context(), fixture.automation.ID, fixture.automation.Version, false, "start",
		); err == nil {
			t.Fatal("corrupt assignment ledger was accepted")
		}
	})

	t.Run("campaign authorization", func(t *testing.T) {
		fixture := prepare(t, "authorization-failure")
		calls := 0
		fixture.controller.update = func(
			ctx context.Context,
			store repoaudit.Store,
			id string,
			version int64,
			mutate func(*repoaudit.RepositoryReviewAutomation) error,
		) (repoaudit.RepositoryReviewAutomation, error) {
			calls++
			updated, err := updateRepositoryReviewAutomation(ctx, store, id, version, mutate)
			if err == nil && calls == 2 {
				identity := repoaudit.CanonicalRepositoryIdentity(updated.Repository)
				_, err = store.BeginCampaign(ctx, repoaudit.BeginCampaignRequest{
					Repository: identity, CampaignID: updated.CampaignID,
					CommitSHA: strings.Repeat("b", 40), Exact: true,
					DeduplicationSnapshot: &repoaudit.RepositoryReviewDeduplicationSnapshot{
						ReviewerModel: "cheap", DeduplicationModel: "cheap",
					},
				})
			}
			return updated, err
		}
		if _, err := fixture.controller.startAutomation(
			t.Context(), fixture.automation.ID, fixture.automation.Version, false, "start",
		); !errors.Is(err, repoaudit.ErrConflict) {
			t.Fatalf("campaign authorization error = %v", err)
		}
	})

	t.Run("late controller shutdown", func(t *testing.T) {
		fixture := prepare(t, "late-shutdown")
		calls := 0
		fixture.controller.update = func(
			ctx context.Context,
			store repoaudit.Store,
			id string,
			version int64,
			mutate func(*repoaudit.RepositoryReviewAutomation) error,
		) (repoaudit.RepositoryReviewAutomation, error) {
			calls++
			updated, err := updateRepositoryReviewAutomation(ctx, store, id, version, mutate)
			if err == nil && calls == 2 {
				fixture.controller.stopped = true
			}
			return updated, err
		}
		if _, err := fixture.controller.startAutomation(
			t.Context(), fixture.automation.ID, fixture.automation.Version, false, "start",
		); !errors.Is(err, context.Canceled) {
			t.Fatalf("late controller shutdown error = %v", err)
		}
	})
}

func TestRepositoryReviewAssignmentTimeoutMutationBoundaries(t *testing.T) {
	handler, mux, _ := newRepositoryReviewAutomationTestHandler(t)
	t.Cleanup(handler.Shutdown)
	profile := createRepositoryReviewProfileForTest(t, mux, "Timeout profile", "cheap")
	invalidCreate := repositoryReviewAutomationMutation(
		t, mux, http.MethodPost, "/api/repository-reviews/automations", map[string]any{
			"repository": "owner/timeout-create", "profile_id": profile.ID,
			"assignment_timeout_seconds": 0,
		},
	)
	if invalidCreate.Code != http.StatusBadRequest {
		t.Fatalf("invalid automation timeout create = %d %s", invalidCreate.Code, invalidCreate.Body.String())
	}
	createdResponse := repositoryReviewAutomationMutation(
		t, mux, http.MethodPost, "/api/repository-reviews/automations", map[string]any{
			"repository": "owner/timeout-update", "profile_id": profile.ID,
		},
	)
	if createdResponse.Code != http.StatusCreated {
		t.Fatalf("automation create = %d %s", createdResponse.Code, createdResponse.Body.String())
	}
	var created struct {
		Automation repoaudit.RepositoryReviewAutomation `json:"automation"`
	}
	if err := json.Unmarshal(createdResponse.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	invalidUpdate := repositoryReviewAutomationMutation(
		t, mux, http.MethodPatch,
		"/api/repository-reviews/automations/"+created.Automation.ID,
		map[string]any{
			"repository": created.Automation.Repository, "profile_id": profile.ID,
			"expected_version": created.Automation.Version, "assignment_timeout_seconds": 61,
		},
	)
	if invalidUpdate.Code != http.StatusBadRequest {
		t.Fatalf("invalid automation timeout update = %d %s", invalidUpdate.Code, invalidUpdate.Body.String())
	}
	profileBody := repositoryReviewProfileBody(profile)
	profileBody["expected_version"] = profile.Version
	profileBody["assignment_timeout_seconds"] = 61
	invalidProfileUpdate := repositoryReviewAutomationMutation(
		t, mux, http.MethodPatch, "/api/repository-reviews/profiles/"+profile.ID, profileBody,
	)
	if invalidProfileUpdate.Code != http.StatusBadRequest {
		t.Fatalf(
			"invalid profile timeout update = %d %s",
			invalidProfileUpdate.Code,
			invalidProfileUpdate.Body.String(),
		)
	}
}

func TestRepositoryReviewAssignmentReconcileStopsAfterCatalogRead(t *testing.T) {
	store := repoaudit.NewStore(t.TempDir())
	automation := testRepositoryReviewAutomation()
	automation.ID = "rra_assignment_reconcile_cancel"
	if _, err := store.CreateAutomation(t.Context(), automation); err != nil {
		t.Fatal(err)
	}
	ctx := &repositoryReviewThirdErrorContext{}
	controller := &repositoryReviewController{
		ctx: ctx, leasedStore: store, leasedConfig: &config.Config{},
		active: make(map[string]*repositoryReviewActiveRun),
	}
	controller.reconcile()
	if ctx.calls.Load() < 3 {
		t.Fatalf("reconcile context checks = %d", ctx.calls.Load())
	}
}

//nolint:govet // Boundary assertions intentionally reuse err in short scopes.
func TestRepositoryReviewAssignmentReconcileRestoresCampaignAfterRestart(t *testing.T) {
	handler, _, _ := newRepositoryReviewAutomationTestHandler(t)
	t.Cleanup(handler.Shutdown)
	store, err := handler.repositoryReviewStore()
	if err != nil {
		t.Fatal(err)
	}
	commit := strings.Repeat("f", 40)
	input := testRepositoryReviewAutomation()
	input.ID = "rra_assignment_reconcile_restart"
	input.Repository = "owner/assignment-reconcile-restart"
	input.Status = repoaudit.RepositoryReviewAutomationRunning
	input.ActiveRunID = "assignment-reconcile-run"
	input.RunIDs = []string{input.ActiveRunID}
	input.StartedAt = time.Now().Add(-time.Minute)
	input.ResolvedCommitSHA = commit
	input.CampaignID = repoaudit.NewRepositoryReviewCampaignID()
	controller := handler.repositoryReviewControllerInstance()
	if err := controller.Start(); err != nil {
		t.Fatal(err)
	}
	automation, err := store.CreateAutomation(t.Context(), input)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.BeginCampaign(t.Context(), repoaudit.BeginCampaignRequest{
		Repository: repoaudit.CanonicalRepositoryIdentity(input.Repository),
		CampaignID: input.CampaignID, CommitSHA: commit,
		DeduplicationSnapshot: &repoaudit.RepositoryReviewDeduplicationSnapshot{
			ReviewerModel: "cheap", DeduplicationModel: "cheap",
		},
	}); err != nil {
		t.Fatal(err)
	}
	controller.reconcile()
	paused, found, err := store.GetAutomation(t.Context(), automation.ID)
	if err != nil || !found || paused.Status != repoaudit.RepositoryReviewAutomationPaused ||
		paused.ActiveRunID != "" || paused.CampaignID != input.CampaignID {
		t.Fatalf("reconciled assignment campaign = %#v found=%v err=%v", paused, found, err)
	}
}
