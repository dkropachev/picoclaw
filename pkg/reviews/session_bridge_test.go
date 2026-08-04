package reviews

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sipeed/picoclaw/pkg/eventing"
	"github.com/sipeed/picoclaw/pkg/memory"
	"github.com/sipeed/picoclaw/pkg/providers"
	"github.com/sipeed/picoclaw/pkg/session"
	"github.com/sipeed/picoclaw/pkg/workflows"
)

const secondWorkingContextCaseID = "prc_55555555555555555555555555555555"

func TestWorkingContextAtomicCreateReturnsDetachedSafeProjection(t *testing.T) {
	detail := workingContextTestDetail(serviceTestCaseID, 12)
	detail.Submission = &eventing.ReviewSubmission{
		ID:            "prs_99999999999999999999999999999999",
		CaseID:        serviceTestCaseID,
		Marker:        "secret-marker",
		LeaseToken:    "secret-lease",
		InternalError: "secret-diagnostic",
	}
	reviews := newWorkingContextReviewStore(detail)
	backend := newWorkingContextBackend(t)
	sessions := newObservedWorkingContextStore(backend)
	service := newWorkingContextService(t, reviews, sessions)

	var projected WorkingContext
	err := service.WithWorkingContext(context.Background(), WorkingContextRequest{
		CaseID: serviceTestCaseID, AgentID: "main",
	}, func(ctx context.Context, working WorkingContext) error {
		projected = working
		if working.CaseID != serviceTestCaseID || working.CaseVersion != 12 ||
			working.AgentID != "main" || !session.IsOpaqueSessionKey(working.SessionKey) ||
			working.SessionRevision == "" {
			t.Fatalf("working context = %#v", working)
		}
		snapshot, found, readErr := backend.ReadSessionSnapshot(
			ctx,
			workingContextAlias("main", serviceTestCaseID),
		)
		if readErr != nil || !found {
			t.Fatalf("verified snapshot = (found=%v, err=%v)", found, readErr)
		}
		if snapshot.Key != working.SessionKey || snapshot.Revision != working.SessionRevision ||
			snapshot.Summary != "" ||
			!reflect.DeepEqual(snapshot.Scope, scopePointer(workingContextScope(detail.Case, "main"))) ||
			!reflect.DeepEqual(snapshot.Aliases, workingContextAliases(detail.Case, "main")) {
			t.Fatalf("verified snapshot = %#v", snapshot)
		}
		wantHistory, _ := workingContextHistory(detail)
		if !equalWorkingContextHistory(snapshot.History, wantHistory) {
			t.Fatalf("verified history = %#v, want %#v", snapshot.History, wantHistory)
		}
		assertWorkingContextGateSubject(t, working.GateSubject, detail)
		return nil
	})
	if err != nil {
		t.Fatalf("WithWorkingContext() error = %v", err)
	}
	if reviews.readCount() != 1 {
		t.Fatalf("aggregate reads = %d, want exactly 1", reviews.readCount())
	}
	if sessions.replaceCount() != 1 || sessions.legacyWriteCount() != 0 {
		t.Fatalf("session writes = replace %d, legacy %d, want 1/0",
			sessions.replaceCount(), sessions.legacyWriteCount())
	}

	// Every value crossing the callback is JSON-native and detached from the
	// authoritative aggregate, including pointer-backed finding fields.
	caseMap := projected.GateSubject["case"].(map[string]any)
	caseMap["summary"] = "mutated"
	findings := projected.GateSubject["findings"].([]any)
	findings[0].(map[string]any)["message"] = "mutated"
	stored := reviews.detail(serviceTestCaseID)
	if stored.Case.Summary != detail.Case.Summary ||
		stored.Findings[0].Message != detail.Findings[0].Message {
		t.Fatalf("gate-subject mutation changed authority = %#v", stored)
	}
}

func TestWorkingContextAtomicUpdateSurvivesBackendAndServiceRestart(t *testing.T) {
	directory := t.TempDir()
	first := workingContextTestDetail(serviceTestCaseID, 12)
	reviews := newWorkingContextReviewStore(first)
	backend := openWorkingContextBackend(t, directory)
	firstService := newWorkingContextService(t, reviews, backend)
	var firstContext WorkingContext
	if err := firstService.WithWorkingContext(context.Background(), WorkingContextRequest{
		CaseID: serviceTestCaseID, AgentID: "main",
	}, func(_ context.Context, working WorkingContext) error {
		firstContext = working
		return nil
	}); err != nil {
		t.Fatalf("first WithWorkingContext() error = %v", err)
	}
	if err := backend.Close(); err != nil {
		t.Fatalf("close first backend: %v", err)
	}

	second := workingContextTestDetail(serviceTestCaseID, 14)
	second.Messages = append(second.Messages, eventing.ReviewMessage{
		ID:        "prm_99999999999999999999999999999999",
		CaseID:    serviceTestCaseID,
		Ordinal:   len(second.Messages),
		Kind:      eventing.ReviewMessageChat,
		Role:      eventing.ReviewMessageUser,
		Content:   "Also check the retry boundary.",
		CreatedAt: serviceTestTime.Add(5 * time.Minute),
	})
	second.Case.UpdatedAt = serviceTestTime.Add(5 * time.Minute)
	reviews.set(second)
	reopened := openWorkingContextBackend(t, directory)
	t.Cleanup(func() { _ = reopened.Close() })
	secondService := newWorkingContextService(t, reviews, reopened)
	var secondContext WorkingContext
	if err := secondService.WithWorkingContext(context.Background(), WorkingContextRequest{
		CaseID: serviceTestCaseID, AgentID: "main",
	}, func(_ context.Context, working WorkingContext) error {
		secondContext = working
		return nil
	}); err != nil {
		t.Fatalf("second WithWorkingContext() error = %v", err)
	}
	if secondContext.SessionKey != firstContext.SessionKey ||
		secondContext.SessionRevision == firstContext.SessionRevision {
		t.Fatalf("restart contexts = first %#v, second %#v", firstContext, secondContext)
	}
	snapshot, found, err := reopened.ReadSessionSnapshot(
		context.Background(),
		workingContextAlias("main", serviceTestCaseID),
	)
	if err != nil || !found {
		t.Fatalf("updated snapshot = (found=%v, err=%v)", found, err)
	}
	if !reflect.DeepEqual(snapshot.Aliases, workingContextAliases(second.Case, "main")) ||
		len(snapshot.History) != len(second.Messages) ||
		snapshot.History[len(snapshot.History)-1].Content != "Also check the retry boundary." {
		t.Fatalf("updated snapshot = %#v", snapshot)
	}
}

func TestWorkingContextTwoServicesConcurrentCreateUsesCAS(t *testing.T) {
	detail := workingContextTestDetail(serviceTestCaseID, 12)
	reviews := newWorkingContextReviewStore(detail)
	backend := newWorkingContextBackend(t)
	barrier := newReplaceBarrier(2)
	sessions := newObservedWorkingContextStore(backend)
	sessions.beforeReplace = barrier.wait
	first := newWorkingContextService(t, reviews, sessions)
	second := newWorkingContextService(t, reviews, sessions)

	var callbacks atomic.Int32
	results := make(chan error, 2)
	launch := func(service *Service) {
		results <- service.WithWorkingContext(context.Background(), WorkingContextRequest{
			CaseID: serviceTestCaseID, AgentID: "main",
		}, func(context.Context, WorkingContext) error {
			callbacks.Add(1)
			return nil
		})
	}
	go launch(first)
	go launch(second)
	barrier.releaseWhenReady(t)
	firstErr := <-results
	secondErr := <-results
	var successes, conflicts int
	for _, err := range []error{firstErr, secondErr} {
		switch {
		case err == nil:
			successes++
		case errors.Is(err, session.ErrSnapshotConflict) && errors.Is(err, ErrUnavailable):
			conflicts++
		default:
			t.Fatalf("concurrent WithWorkingContext() error = %v", err)
		}
	}
	if successes != 1 || conflicts != 1 || callbacks.Load() != 1 {
		t.Fatalf("concurrent results = successes %d, conflicts %d, callbacks %d",
			successes, conflicts, callbacks.Load())
	}
	if reviews.readCount() != 2 || sessions.replaceCount() != 2 {
		t.Fatalf("concurrent calls = reads %d, replacements %d, want 2/2",
			reviews.readCount(), sessions.replaceCount())
	}
}

func TestWorkingContextRuntimeAdmissionPrecedesSameCaseProjectionLock(t *testing.T) {
	type runtimeLeaseMarker struct{}

	detail := workingContextTestDetail(serviceTestCaseID, 12)
	reviews := newWorkingContextReviewStore(detail)
	backend := newWorkingContextBackend(t)
	pausedAcquireStarted := make(chan struct{})
	resumePausedAcquire := make(chan struct{})
	var resumeOnce sync.Once
	resume := func() { resumeOnce.Do(func() { close(resumePausedAcquire) }) }
	t.Cleanup(resume)

	var unleasedReleases atomic.Int32
	var leasedReleases atomic.Int32
	service, err := NewService(ServiceConfig{
		Store: reviews,
		AcquireWorkingContextRuntime: func(
			ctx context.Context,
			_ string,
		) (context.Context, session.SessionStore, func(), error) {
			if leased, _ := ctx.Value(runtimeLeaseMarker{}).(bool); leased {
				return ctx, backend, func() { leasedReleases.Add(1) }, nil
			}
			close(pausedAcquireStarted)
			select {
			case <-resumePausedAcquire:
			case <-ctx.Done():
				return ctx, nil, nil, ctx.Err()
			}
			return context.WithValue(ctx, runtimeLeaseMarker{}, true), backend,
				func() { unleasedReleases.Add(1) }, nil
		},
	})
	if err != nil {
		t.Fatalf("NewService() error = %v", err)
	}

	unleasedDone := make(chan error, 1)
	go func() {
		unleasedDone <- service.WithWorkingContext(
			context.Background(),
			WorkingContextRequest{CaseID: serviceTestCaseID, AgentID: "main"},
			func(ctx context.Context, _ WorkingContext) error {
				if leased, _ := ctx.Value(runtimeLeaseMarker{}).(bool); !leased {
					return errors.New("unleased callback did not inherit runtime lease")
				}
				return nil
			},
		)
	}()
	select {
	case <-pausedAcquireStarted:
	case <-time.After(5 * time.Second):
		t.Fatal("unleased caller did not reach paused runtime admission")
	}

	leasedCtx := context.WithValue(context.Background(), runtimeLeaseMarker{}, true)
	leasedDone := make(chan error, 1)
	go func() {
		leasedDone <- service.WithWorkingContext(
			leasedCtx,
			WorkingContextRequest{CaseID: serviceTestCaseID, AgentID: "main"},
			func(context.Context, WorkingContext) error { return nil },
		)
	}()
	select {
	case err := <-leasedDone:
		if err != nil {
			t.Fatalf("lease-marked WithWorkingContext() error = %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("lease-marked caller blocked behind paused runtime admission")
	}
	if got := leasedReleases.Load(); got != 1 {
		t.Fatalf("lease-marked runtime releases = %d, want 1", got)
	}
	if got := unleasedReleases.Load(); got != 0 {
		t.Fatalf("paused runtime releases before admission resumed = %d, want 0", got)
	}

	resume()
	select {
	case err := <-unleasedDone:
		if err != nil {
			t.Fatalf("unleased WithWorkingContext() error = %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("unleased caller did not complete after runtime admission resumed")
	}
	if got := leasedReleases.Load(); got != 1 {
		t.Fatalf("lease-marked runtime releases = %d, want exactly 1", got)
	}
	if got := unleasedReleases.Load(); got != 1 {
		t.Fatalf("unleased runtime releases = %d, want exactly 1", got)
	}
	if got := reviews.readCount(); got != 2 {
		t.Fatalf("aggregate reads = %d, want 2 completed projections", got)
	}
}

func TestWorkingContextAgentQualifiedAliasesKeepOwnersSeparate(t *testing.T) {
	detail := workingContextTestDetail(serviceTestCaseID, 12)
	backend := newWorkingContextBackend(t)
	service := newWorkingContextTestService(t, detail, backend)
	contexts := make(map[string]WorkingContext)
	for _, agentID := range []string{"main", "reviewer"} {
		if err := service.WithWorkingContext(context.Background(), WorkingContextRequest{
			CaseID: serviceTestCaseID, AgentID: agentID,
		}, func(_ context.Context, working WorkingContext) error {
			contexts[agentID] = working
			return nil
		}); err != nil {
			t.Fatalf("WithWorkingContext(%q) error = %v", agentID, err)
		}
	}
	if contexts["main"].SessionKey == contexts["reviewer"].SessionKey {
		t.Fatalf("different owners shared session key %q", contexts["main"].SessionKey)
	}
	for agentID, working := range contexts {
		snapshot, found, err := backend.ReadSessionSnapshot(
			context.Background(),
			workingContextAlias(agentID, serviceTestCaseID),
		)
		if err != nil || !found || snapshot.Key != working.SessionKey ||
			snapshot.Scope == nil || snapshot.Scope.AgentID != agentID {
			t.Fatalf("owner %q snapshot = %#v, found=%v, err=%v",
				agentID, snapshot, found, err)
		}
	}
}

func TestWorkingContextStableKeyRejectsChangedImmutableBinding(t *testing.T) {
	first := workingContextTestDetail(serviceTestCaseID, 12)
	changed := workingContextTestDetail(serviceTestCaseID, 13)
	changed.Case.Repository = "other/repository"
	if firstKey, changedKey := session.BuildSessionKey(workingContextScope(first.Case, "main")),
		session.BuildSessionKey(workingContextScope(changed.Case, "main")); firstKey != changedKey {
		t.Fatalf("immutable PR fields changed stable key from %q to %q", firstKey, changedKey)
	}
	backend := newWorkingContextBackend(t)
	reviews := newWorkingContextReviewStore(first)
	service := newWorkingContextService(t, reviews, backend)
	if err := service.WithWorkingContext(context.Background(), WorkingContextRequest{
		CaseID: serviceTestCaseID, AgentID: "main",
	}, func(context.Context, WorkingContext) error { return nil }); err != nil {
		t.Fatalf("seed working context: %v", err)
	}
	reviews.set(changed)
	called := false
	err := service.WithWorkingContext(context.Background(), WorkingContextRequest{
		CaseID: serviceTestCaseID, AgentID: "main",
	}, func(context.Context, WorkingContext) error { called = true; return nil })
	if !errors.Is(err, ErrUnavailable) || called ||
		!strings.Contains(err.Error(), "aliases do not match exactly") {
		t.Fatalf("changed binding = (called=%v, err=%v), want unavailable", called, err)
	}
}

func TestWorkingContextRejectsAliasCollisionAndOwnerMismatch(t *testing.T) {
	detail := workingContextTestDetail(serviceTestCaseID, 12)

	t.Run("agent-qualified alias collision", func(t *testing.T) {
		backend := newWorkingContextBackend(t)
		foreignScope := session.SessionScope{
			Version:    session.ScopeVersionV1,
			AgentID:    "reviewer",
			Channel:    reviewWorkingContextChannel,
			Account:    "default",
			Dimensions: []string{"review"},
			Values:     map[string]string{"review": secondWorkingContextCaseID},
		}
		foreignKey := session.BuildSessionKey(foreignScope)
		if err := backend.ReplaceSessionSnapshot(context.Background(), session.SessionSnapshotReplacement{
			Key: foreignKey, Scope: &foreignScope,
			Aliases: []string{workingContextAlias("main", serviceTestCaseID)},
		}); err != nil {
			t.Fatalf("seed foreign alias: %v", err)
		}
		called := false
		err := newWorkingContextTestService(t, detail, backend).WithWorkingContext(
			context.Background(),
			WorkingContextRequest{CaseID: serviceTestCaseID, AgentID: "main"},
			func(context.Context, WorkingContext) error { called = true; return nil },
		)
		if !errors.Is(err, ErrUnavailable) || called {
			t.Fatalf("alias collision = (called=%v, err=%v), want unavailable/no callback", called, err)
		}
	})

	t.Run("canonical scope owner mismatch", func(t *testing.T) {
		backend := newWorkingContextBackend(t)
		observed := newObservedWorkingContextStore(backend)
		observed.readHook = func(key string, snapshot session.SessionSnapshot, found bool, err error) (
			session.SessionSnapshot, bool, error,
		) {
			if found && err == nil {
				forgedScope := session.CloneScope(snapshot.Scope)
				forgedScope.AgentID = "reviewer"
				snapshot.Scope = forgedScope
			}
			return snapshot, found, err
		}
		called := false
		err := newWorkingContextTestService(t, detail, observed).WithWorkingContext(
			context.Background(),
			WorkingContextRequest{CaseID: serviceTestCaseID, AgentID: "main"},
			func(context.Context, WorkingContext) error { called = true; return nil },
		)
		if !errors.Is(err, ErrUnavailable) || called || observed.replaceCount() != 0 {
			t.Fatalf("owner mismatch = (called=%v, replacements=%d, err=%v)",
				called, observed.replaceCount(), err)
		}
	})
}

func TestWorkingContextOrdinaryCollisionFailsBeforeAnySessionSnapshotRead(t *testing.T) {
	detail := workingContextTestDetail(serviceTestCaseID, 12)
	backend := newWorkingContextBackend(t)
	reviewKey := session.BuildSessionKey(workingContextScope(detail.Case, "main"))
	ordinaryScope := &session.SessionScope{
		Version:    session.ScopeVersionV1,
		AgentID:    "main",
		Channel:    "github",
		Account:    "default",
		Dimensions: []string{"chat"},
		Values:     map[string]string{"chat": "ordinary-collision"},
	}
	if _, err := backend.AdmitSessionScope(context.Background(), session.SessionScopeAdmission{
		Key:   reviewKey,
		Scope: ordinaryScope,
		Mode:  session.ScopeAdmissionLive,
	}); err != nil {
		t.Fatalf("seed ordinary collision: %v", err)
	}
	backend.AddMessage(reviewKey, "user", "ordinary private history must not be read")
	observed := newObservedWorkingContextStore(backend)
	called := false
	err := newWorkingContextTestService(t, detail, observed).WithWorkingContext(
		context.Background(),
		WorkingContextRequest{CaseID: serviceTestCaseID, AgentID: "main"},
		func(context.Context, WorkingContext) error { called = true; return nil },
	)
	if !errors.Is(err, ErrUnavailable) || called || observed.readCount() != 0 ||
		observed.replaceCount() != 0 {
		t.Fatalf("ordinary collision = (called=%v, reads=%d, replacements=%d, err=%v)",
			called, observed.readCount(), observed.replaceCount(), err)
	}
}

func TestWorkingContextRejectsCanonicalSessionWithoutExactAliases(t *testing.T) {
	detail := workingContextTestDetail(serviceTestCaseID, 12)
	backend := newWorkingContextBackend(t)
	scope := workingContextScope(detail.Case, "main")
	key := session.BuildSessionKey(scope)
	if err := backend.ReplaceSessionSnapshot(context.Background(), session.SessionSnapshotReplacement{
		Key:     key,
		History: []providers.Message{{Role: "user", Content: "untrusted direct write"}},
		Summary: "untrusted summary",
		Scope:   &scope,
	}); err != nil {
		t.Fatalf("seed canonical session: %v", err)
	}
	called := false
	err := newWorkingContextTestService(t, detail, backend).WithWorkingContext(
		context.Background(),
		WorkingContextRequest{CaseID: serviceTestCaseID, AgentID: "main"},
		func(context.Context, WorkingContext) error { called = true; return nil },
	)
	if !errors.Is(err, ErrUnavailable) || called {
		t.Fatalf("canonical aliasless session = (called=%v, err=%v), want unavailable", called, err)
	}
}

func TestWorkingContextRejectsStaleAggregateVersion(t *testing.T) {
	newer := workingContextTestDetail(serviceTestCaseID, 14)
	backend := newWorkingContextBackend(t)
	reviews := newWorkingContextReviewStore(newer)
	sessions := newObservedWorkingContextStore(backend)
	service := newWorkingContextService(t, reviews, sessions)
	if err := service.WithWorkingContext(context.Background(), WorkingContextRequest{
		CaseID: serviceTestCaseID, AgentID: "main",
	}, func(context.Context, WorkingContext) error { return nil }); err != nil {
		t.Fatalf("seed newer projection: %v", err)
	}
	reviews.set(workingContextTestDetail(serviceTestCaseID, 13))
	called := false
	err := service.WithWorkingContext(context.Background(), WorkingContextRequest{
		CaseID: serviceTestCaseID, AgentID: "main",
	}, func(context.Context, WorkingContext) error { called = true; return nil })
	if !errors.Is(err, ErrUnavailable) || called || sessions.replaceCount() != 1 {
		t.Fatalf("stale aggregate = (called=%v, replacements=%d, err=%v)",
			called, sessions.replaceCount(), err)
	}
}

func TestWorkingContextFailsClosedForUnsupportedAndTypedNilStores(t *testing.T) {
	detail := workingContextTestDetail(serviceTestCaseID, 12)
	backend := newWorkingContextBackend(t)
	unsupported := &snapshotReaderOnlyWorkingContextStore{
		SessionStore: backend,
		reader:       backend,
	}
	atomicWithoutAdmitter := &snapshotWorkingContextStoreWithoutAdmitter{
		SessionStore: backend,
		reader:       backend,
		replacer:     backend,
	}
	var typedNil *session.JSONLBackend
	tests := []struct {
		name  string
		store session.SessionStore
	}{
		{name: "snapshot reader without replacer", store: unsupported},
		{name: "snapshot reader and replacer without scope admitter", store: atomicWithoutAdmitter},
		{name: "typed nil atomic store", store: typedNil},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			called := false
			err := newWorkingContextTestService(t, detail, test.store).WithWorkingContext(
				context.Background(),
				WorkingContextRequest{CaseID: serviceTestCaseID, AgentID: "main"},
				func(context.Context, WorkingContext) error { called = true; return nil },
			)
			if !errors.Is(err, ErrUnavailable) || called {
				t.Fatalf("WithWorkingContext() = (called=%v, err=%v), want unavailable", called, err)
			}
		})
	}
}

func TestWorkingContextReleasesPartialRuntimeAcquireFailure(t *testing.T) {
	detail := workingContextTestDetail(serviceTestCaseID, 12)
	reviews := newWorkingContextReviewStore(detail)
	var releases atomic.Int32
	service, err := NewService(ServiceConfig{
		Store: reviews,
		AcquireWorkingContextRuntime: func(
			ctx context.Context,
			_ string,
		) (context.Context, session.SessionStore, func(), error) {
			return ctx, nil, func() { releases.Add(1) }, errors.New("partial acquire failed")
		},
	})
	if err != nil {
		t.Fatalf("NewService() error = %v", err)
	}
	called := false
	err = service.WithWorkingContext(context.Background(), WorkingContextRequest{
		CaseID: serviceTestCaseID, AgentID: "main",
	}, func(context.Context, WorkingContext) error { called = true; return nil })
	if !errors.Is(err, ErrUnavailable) || called || releases.Load() != 1 ||
		reviews.readCount() != 0 {
		t.Fatalf("partial acquire = (called=%v, releases=%d, reads=%d, err=%v)",
			called, releases.Load(), reviews.readCount(), err)
	}
}

func TestWorkingContextRejectsIncompleteSuccessfulRuntimeLease(t *testing.T) {
	detail := workingContextTestDetail(serviceTestCaseID, 12)
	for _, test := range []struct {
		name       string
		runtimeCtx bool
		release    bool
		wantDrops  int32
	}{
		{name: "nil runtime context", release: true, wantDrops: 1},
		{name: "nil release", runtimeCtx: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			reviews := newWorkingContextReviewStore(detail)
			backend := newWorkingContextBackend(t)
			var releases atomic.Int32
			service, err := NewService(ServiceConfig{
				Store: reviews,
				AcquireWorkingContextRuntime: func(
					ctx context.Context,
					_ string,
				) (context.Context, session.SessionStore, func(), error) {
					var runtimeCtx context.Context
					if test.runtimeCtx {
						runtimeCtx = ctx
					}
					var release func()
					if test.release {
						release = func() { releases.Add(1) }
					}
					return runtimeCtx, backend, release, nil
				},
			})
			if err != nil {
				t.Fatalf("NewService() error = %v", err)
			}
			called := false
			err = service.WithWorkingContext(context.Background(), WorkingContextRequest{
				CaseID: serviceTestCaseID, AgentID: "main",
			}, func(context.Context, WorkingContext) error { called = true; return nil })
			if !errors.Is(err, ErrUnavailable) || called || releases.Load() != test.wantDrops ||
				reviews.readCount() != 0 {
				t.Fatalf("incomplete lease = (called=%v, releases=%d, reads=%d, err=%v)",
					called, releases.Load(), reviews.readCount(), err)
			}
		})
	}
}

func TestWorkingContextFailsClosedOnReplaceAndReadbackAmbiguity(t *testing.T) {
	detail := workingContextTestDetail(serviceTestCaseID, 12)

	t.Run("committed error with exact readback still fails", func(t *testing.T) {
		backend := newWorkingContextBackend(t)
		observed := newObservedWorkingContextStore(backend)
		observed.afterReplace = func(err error) error {
			if err != nil {
				return err
			}
			return errors.New("directory sync outcome unknown")
		}
		called := false
		err := newWorkingContextTestService(t, detail, observed).WithWorkingContext(
			context.Background(),
			WorkingContextRequest{CaseID: serviceTestCaseID, AgentID: "main"},
			func(context.Context, WorkingContext) error { called = true; return nil },
		)
		if !errors.Is(err, ErrUnavailable) || called {
			t.Fatalf("committed ambiguous replacement = (called=%v, err=%v)", called, err)
		}
	})

	t.Run("precommit error remains missing", func(t *testing.T) {
		backend := newWorkingContextBackend(t)
		observed := newObservedWorkingContextStore(backend)
		observed.replaceHook = func(context.Context, session.SessionSnapshotReplacement) error {
			return errors.New("write failed before commit")
		}
		assertWorkingContextUnavailable(t, detail, observed)
	})

	t.Run("postcommit readback mismatch", func(t *testing.T) {
		backend := newWorkingContextBackend(t)
		observed := newObservedWorkingContextStore(backend)
		observed.readHook = func(_ string, snapshot session.SessionSnapshot, found bool, err error) (
			session.SessionSnapshot, bool, error,
		) {
			if observed.replaceCount() > 0 && found && err == nil {
				snapshot.Summary = "tampered"
			}
			return snapshot, found, err
		}
		assertWorkingContextUnavailable(t, detail, observed)
	})

	t.Run("postcommit readback error", func(t *testing.T) {
		backend := newWorkingContextBackend(t)
		observed := newObservedWorkingContextStore(backend)
		observed.readHook = func(_ string, snapshot session.SessionSnapshot, found bool, err error) (
			session.SessionSnapshot, bool, error,
		) {
			if observed.replaceCount() > 0 {
				return session.SessionSnapshot{}, false, errors.New("strict readback failed")
			}
			return snapshot, found, err
		}
		assertWorkingContextUnavailable(t, detail, observed)
	})

	t.Run("explicit CAS conflict is never reconciled", func(t *testing.T) {
		backend := newWorkingContextBackend(t)
		observed := newObservedWorkingContextStore(backend)
		observed.replaceHook = func(context.Context, session.SessionSnapshotReplacement) error {
			return session.ErrSnapshotConflict
		}
		assertWorkingContextUnavailable(t, detail, observed)
	})
}

func TestWorkingContextFailedFirstReplacementCanRetryExactReservation(t *testing.T) {
	detail := workingContextTestDetail(serviceTestCaseID, 12)
	backend := newWorkingContextBackend(t)
	observed := newObservedWorkingContextStore(backend)
	observed.replaceHook = func(context.Context, session.SessionSnapshotReplacement) error {
		return errors.New("first replacement failed before commit")
	}
	service := newWorkingContextTestService(t, detail, observed)
	firstCalled := false
	firstErr := service.WithWorkingContext(
		context.Background(),
		WorkingContextRequest{CaseID: serviceTestCaseID, AgentID: "main"},
		func(context.Context, WorkingContext) error { firstCalled = true; return nil },
	)
	if !errors.Is(firstErr, ErrUnavailable) || firstCalled {
		t.Fatalf("first replacement = (called=%v, err=%v), want unavailable", firstCalled, firstErr)
	}
	reservation, found, err := backend.ReadSessionSnapshot(
		context.Background(),
		workingContextAlias("main", serviceTestCaseID),
	)
	if err != nil || !found || reservation.Revision == "" || len(reservation.History) != 0 {
		t.Fatalf("failed replacement reservation = (found=%v, snapshot=%+v, err=%v)",
			found, reservation, err)
	}

	observed.mu.Lock()
	observed.replaceHook = nil
	observed.mu.Unlock()
	secondCalled := false
	var retried WorkingContext
	secondErr := service.WithWorkingContext(
		context.Background(),
		WorkingContextRequest{CaseID: serviceTestCaseID, AgentID: "main"},
		func(_ context.Context, working WorkingContext) error {
			secondCalled = true
			retried = working
			return nil
		},
	)
	if secondErr != nil || !secondCalled || retried.SessionRevision == "" ||
		retried.SessionRevision == reservation.Revision || observed.replaceCount() != 2 {
		t.Fatalf("exact retry = (called=%v, context=%+v, replacements=%d, err=%v)",
			secondCalled, retried, observed.replaceCount(), secondErr)
	}
}

func TestWorkingContextCancellationWhileWaitingAndReplacing(t *testing.T) {
	detail := workingContextTestDetail(serviceTestCaseID, 12)

	t.Run("waiting for same-case callback", func(t *testing.T) {
		backend := newWorkingContextBackend(t)
		reviews := newWorkingContextReviewStore(detail)
		service := newWorkingContextService(t, reviews, backend)
		entered := make(chan struct{})
		release := make(chan struct{})
		firstDone := make(chan error, 1)
		go func() {
			firstDone <- service.WithWorkingContext(context.Background(), WorkingContextRequest{
				CaseID: serviceTestCaseID, AgentID: "main",
			}, func(context.Context, WorkingContext) error {
				close(entered)
				<-release
				return nil
			})
		}()
		<-entered
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		called := false
		err := service.WithWorkingContext(ctx, WorkingContextRequest{
			CaseID: serviceTestCaseID, AgentID: "main",
		}, func(context.Context, WorkingContext) error { called = true; return nil })
		if !errors.Is(err, context.Canceled) || called || reviews.readCount() != 1 {
			t.Fatalf("canceled waiter = (called=%v, reads=%d, err=%v)",
				called, reviews.readCount(), err)
		}
		close(release)
		if err := <-firstDone; err != nil {
			t.Fatalf("first callback error = %v", err)
		}
	})

	t.Run("replacement cancellation", func(t *testing.T) {
		backend := newWorkingContextBackend(t)
		observed := newObservedWorkingContextStore(backend)
		started := make(chan struct{})
		observed.replaceHook = func(ctx context.Context, _ session.SessionSnapshotReplacement) error {
			close(started)
			<-ctx.Done()
			return ctx.Err()
		}
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan error, 1)
		go func() {
			done <- newWorkingContextTestService(t, detail, observed).WithWorkingContext(
				ctx,
				WorkingContextRequest{CaseID: serviceTestCaseID, AgentID: "main"},
				func(context.Context, WorkingContext) error {
					return errors.New("callback must not run")
				},
			)
		}()
		<-started
		cancel()
		if err := <-done; !errors.Is(err, context.Canceled) {
			t.Fatalf("replacement cancellation error = %v", err)
		}
	})
}

func TestWorkingContextRejectsOversizedGateSubjectBeforeSessionWrite(t *testing.T) {
	detail := workingContextTestDetail(serviceTestCaseID, 12)
	detail.Findings = make([]eventing.ReviewFinding, 200)
	for index := range detail.Findings {
		findingID := fmt.Sprintf("prf_%032x", index+1)
		if index == 0 {
			findingID = serviceTestFindingID
		}
		detail.Findings[index] = eventing.ReviewFinding{
			ID:             findingID,
			CaseID:         serviceTestCaseID,
			Ordinal:        index,
			State:          eventing.ReviewFindingActive,
			Severity:       eventing.ReviewSeverityHigh,
			Title:          "Large but individually valid finding",
			Message:        strings.Repeat("m", 8<<10),
			Recommendation: strings.Repeat("r", 8<<10),
			Revision:       1,
			CreatedAt:      serviceTestTime,
			UpdatedAt:      serviceTestTime,
		}
	}
	detail.Case.ActiveFindings = len(detail.Findings)
	detail.Case.TotalFindings = len(detail.Findings)
	backend := newWorkingContextBackend(t)
	observed := newObservedWorkingContextStore(backend)
	called := false
	err := newWorkingContextTestService(t, detail, observed).WithWorkingContext(
		context.Background(),
		WorkingContextRequest{CaseID: serviceTestCaseID, AgentID: "main"},
		func(context.Context, WorkingContext) error { called = true; return nil },
	)
	if !errors.Is(err, ErrUnavailable) ||
		!strings.Contains(err.Error(), "gate subject exceeds") || called ||
		observed.replaceCount() != 0 {
		t.Fatalf("oversized subject = (called=%v, replacements=%d, err=%v)",
			called, observed.replaceCount(), err)
	}
}

func TestWorkingContextRejectsInvalidAggregateBeforeSessionWrite(t *testing.T) {
	addSecondFinding := func(detail *eventing.ReviewCaseDetail) {
		second := detail.Findings[0]
		second.ID = "prf_99999999999999999999999999999999"
		second.Ordinal = 1
		detail.Findings = append(detail.Findings, second)
		detail.Case.ActiveFindings = 2
		detail.Case.TotalFindings = 2
	}
	tests := []struct {
		name   string
		mutate func(*eventing.ReviewCaseDetail)
	}{
		{name: "non-prf finding ID", mutate: func(detail *eventing.ReviewCaseDetail) {
			detail.Findings[0].ID = "finding"
		}},
		{name: "duplicate finding ID", mutate: func(detail *eventing.ReviewCaseDetail) {
			addSecondFinding(detail)
			detail.Findings[1].ID = detail.Findings[0].ID
		}},
		{name: "noncontiguous finding ordinal", mutate: func(detail *eventing.ReviewCaseDetail) {
			detail.Findings[0].Ordinal = 1
		}},
		{name: "invalid finding state", mutate: func(detail *eventing.ReviewCaseDetail) {
			detail.Findings[0].State = "unknown"
		}},
		{name: "invalid finding severity", mutate: func(detail *eventing.ReviewCaseDetail) {
			detail.Findings[0].Severity = "urgent"
		}},
		{name: "invalid finding revision", mutate: func(detail *eventing.ReviewCaseDetail) {
			detail.Findings[0].Revision = 0
		}},
		{name: "invalid finding timestamp", mutate: func(detail *eventing.ReviewCaseDetail) {
			detail.Findings[0].UpdatedAt = detail.Findings[0].CreatedAt.Add(-time.Second)
		}},
		{name: "absolute finding file", mutate: func(detail *eventing.ReviewCaseDetail) {
			detail.Findings[0].File = "/absolute.go"
		}},
		{name: "escaping finding file", mutate: func(detail *eventing.ReviewCaseDetail) {
			detail.Findings[0].File = "../escape.go"
		}},
		{name: "backslash finding file", mutate: func(detail *eventing.ReviewCaseDetail) {
			detail.Findings[0].File = `dir\file.go`
		}},
		{name: "finding line without file", mutate: func(detail *eventing.ReviewCaseDetail) {
			detail.Findings[0].File = ""
			line := 1
			detail.Findings[0].Line = &line
		}},
		{name: "case count mismatch", mutate: func(detail *eventing.ReviewCaseDetail) {
			detail.Case.TotalFindings = 2
		}},
		{name: "case status mismatch", mutate: func(detail *eventing.ReviewCaseDetail) {
			detail.Case.Status = eventing.ReviewCaseAllDropped
		}},
		{name: "message ordinal mismatch", mutate: func(detail *eventing.ReviewCaseDetail) {
			detail.Messages[1].Ordinal = 99
		}},
		{name: "rephrase without finding", mutate: func(detail *eventing.ReviewCaseDetail) {
			detail.Messages[0].Kind = eventing.ReviewMessageRephrase
			detail.Messages[0].FindingID = ""
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			detail := workingContextTestDetail(serviceTestCaseID, 12)
			test.mutate(&detail)
			backend := newWorkingContextBackend(t)
			observed := newObservedWorkingContextStore(backend)
			assertWorkingContextUnavailable(t, detail, observed)
			if observed.replaceCount() != 0 {
				t.Fatalf("invalid aggregate replacements = %d, want 0", observed.replaceCount())
			}
		})
	}
}

func assertWorkingContextGateSubject(
	t *testing.T,
	subject map[string]any,
	detail eventing.ReviewCaseDetail,
) {
	t.Helper()
	if subject == nil || subject["submission"] != nil {
		t.Fatalf("unsafe gate subject = %#v", subject)
	}
	caseMap, ok := subject["case"].(map[string]any)
	if !ok || caseMap["id"] != detail.Case.ID || caseMap["version"].(fmt.Stringer).String() != "12" {
		t.Fatalf("gate subject case = %#v", subject["case"])
	}
	findings, ok := subject["findings"].([]any)
	if !ok || len(findings) != len(detail.Findings) {
		t.Fatalf("gate subject findings = %#v", subject["findings"])
	}
	if line, exists := findings[0].(map[string]any)["line"]; !exists || line != nil {
		t.Fatalf("gate subject finding line = %#v, exists=%v, want explicit null", line, exists)
	}
	messages, ok := subject["messages"].([]any)
	if !ok || len(messages) != len(detail.Messages) {
		t.Fatalf("gate subject messages = %#v", subject["messages"])
	}
	for index, raw := range messages {
		metadata, ok := raw.(map[string]any)
		if !ok || metadata["id"] != detail.Messages[index].ID ||
			metadata["ordinal"].(fmt.Stringer).String() != strconv.Itoa(index) ||
			metadata["content"] != nil || metadata["created_at"] == "" {
			t.Fatalf("gate subject message %d = %#v", index, raw)
		}
	}
	encoded := fmt.Sprintf("%#v", subject)
	for _, secret := range []string{
		"secret-marker",
		"secret-lease",
		"secret-diagnostic",
		"Is this retry actually safe?",
		"No. The item can be lost",
	} {
		if strings.Contains(encoded, secret) {
			t.Fatalf("gate subject leaked %q: %#v", secret, subject)
		}
	}
	_, err := workflows.CompileGateWorkflow(
		"review subject preflight",
		[]workflows.GateSpec{{
			ID: "review", Kind: workflows.GateAIWorkingContext,
			AgentID: "main", Criteria: "ask when needed", Title: "Review needs attention",
		}},
		subject,
	)
	if err != nil {
		t.Fatalf("gate compiler rejected projected subject: %v", err)
	}
}

func assertWorkingContextUnavailable(
	t *testing.T,
	detail eventing.ReviewCaseDetail,
	sessions session.SessionStore,
) {
	t.Helper()
	called := false
	err := newWorkingContextTestService(t, detail, sessions).WithWorkingContext(
		context.Background(),
		WorkingContextRequest{CaseID: serviceTestCaseID, AgentID: "main"},
		func(context.Context, WorkingContext) error { called = true; return nil },
	)
	if !errors.Is(err, ErrUnavailable) || called {
		t.Fatalf("WithWorkingContext() = (called=%v, err=%v), want unavailable", called, err)
	}
}

func newWorkingContextBackend(t *testing.T) *session.JSONLBackend {
	t.Helper()
	backend := openWorkingContextBackend(t, t.TempDir())
	t.Cleanup(func() { _ = backend.Close() })
	return backend
}

func openWorkingContextBackend(t *testing.T, directory string) *session.JSONLBackend {
	t.Helper()
	store, err := memory.NewJSONLStore(directory)
	if err != nil {
		t.Fatalf("NewJSONLStore() error = %v", err)
	}
	return session.NewJSONLBackend(store)
}

func newWorkingContextTestService(
	t *testing.T,
	detail eventing.ReviewCaseDetail,
	sessions session.SessionStore,
) *Service {
	t.Helper()
	return newWorkingContextService(t, newWorkingContextReviewStore(detail), sessions)
}

func newWorkingContextService(
	t *testing.T,
	store Store,
	sessions session.SessionStore,
) *Service {
	t.Helper()
	service, err := NewService(ServiceConfig{
		Store: store,
		AcquireWorkingContextRuntime: func(
			ctx context.Context,
			_ string,
		) (context.Context, session.SessionStore, func(), error) {
			return ctx, sessions, func() {}, nil
		},
	})
	if err != nil {
		t.Fatalf("NewService() error = %v", err)
	}
	return service
}

func workingContextTestDetail(caseID string, version int64) eventing.ReviewCaseDetail {
	detail := serviceTestDetail(version)
	detail.Case.ID = caseID
	detail.Case.EventID = "ev_11111111111111111111111111111111"
	detail.Case.DispatchID = "dsp_22222222222222222222222222222222"
	detail.Case.RunID = "wr_33333333333333333333333333333333"
	detail.Case.WorkflowRef = ".picoclaw/workflows/review.yml"
	detail.Case.WorkflowRevision = strings.Repeat("c", 64)
	detail.Case.UpdatedAt = serviceTestTime.Add(4 * time.Minute)
	for index := range detail.Findings {
		detail.Findings[index].CaseID = caseID
		detail.Findings[index].Ordinal = index
	}
	detail.Messages = []eventing.ReviewMessage{
		{
			ID:        "prm_66666666666666666666666666666666",
			CaseID:    caseID,
			Ordinal:   0,
			Kind:      eventing.ReviewMessageChat,
			Role:      eventing.ReviewMessageUser,
			Content:   "Is this retry actually safe?",
			CreatedAt: serviceTestTime.Add(time.Minute),
		},
		{
			ID:        "prm_77777777777777777777777777777777",
			CaseID:    caseID,
			Ordinal:   1,
			Kind:      eventing.ReviewMessageChat,
			Role:      eventing.ReviewMessageAssistant,
			Content:   "No. The item can be lost after the early return.",
			CreatedAt: serviceTestTime.Add(2 * time.Minute),
		},
		{
			ID:        "prm_88888888888888888888888888888888",
			CaseID:    caseID,
			Ordinal:   2,
			FindingID: serviceTestFindingID,
			Kind:      eventing.ReviewMessageRephrase,
			Role:      eventing.ReviewMessageUser,
			Content:   "Make the finding more direct.",
			CreatedAt: serviceTestTime.Add(3 * time.Minute),
		},
		{
			ID:        "prm_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
			CaseID:    caseID,
			Ordinal:   3,
			FindingID: serviceTestFindingID,
			Kind:      eventing.ReviewMessageRephrase,
			Role:      eventing.ReviewMessageAssistant,
			Content:   `{"title":"Restore before returning","message":"Restore the queued item before the early return."}`,
			CreatedAt: serviceTestTime.Add(4 * time.Minute),
		},
	}
	return detail
}

type workingContextReviewStore struct {
	Store
	mu      sync.RWMutex
	details map[string]eventing.ReviewCaseDetail
	reads   int
}

func newWorkingContextReviewStore(details ...eventing.ReviewCaseDetail) *workingContextReviewStore {
	store := &workingContextReviewStore{details: make(map[string]eventing.ReviewCaseDetail)}
	for _, detail := range details {
		store.details[detail.Case.ID] = detail
	}
	return store
}

func (store *workingContextReviewStore) GetReviewCase(
	ctx context.Context,
	caseID string,
) (eventing.ReviewCaseDetail, error) {
	if err := ctx.Err(); err != nil {
		return eventing.ReviewCaseDetail{}, err
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	store.reads++
	detail, ok := store.details[caseID]
	if !ok {
		return eventing.ReviewCaseDetail{}, eventing.ErrNotFound
	}
	return cloneWorkingContextDetail(detail), nil
}

func (store *workingContextReviewStore) set(detail eventing.ReviewCaseDetail) {
	store.mu.Lock()
	defer store.mu.Unlock()
	store.details[detail.Case.ID] = detail
}

func (store *workingContextReviewStore) readCount() int {
	store.mu.RLock()
	defer store.mu.RUnlock()
	return store.reads
}

func (store *workingContextReviewStore) detail(caseID string) eventing.ReviewCaseDetail {
	store.mu.RLock()
	defer store.mu.RUnlock()
	return cloneWorkingContextDetail(store.details[caseID])
}

func cloneWorkingContextDetail(detail eventing.ReviewCaseDetail) eventing.ReviewCaseDetail {
	detail.Case = cloneCase(detail.Case)
	detail.Findings = cloneWorkingContextFindings(detail.Findings)
	detail.Messages = append([]eventing.ReviewMessage(nil), detail.Messages...)
	if detail.Submission != nil {
		submission := *detail.Submission
		submission.Request = append([]byte(nil), detail.Submission.Request...)
		detail.Submission = &submission
	}
	return detail
}

type observedWorkingContextStore struct {
	session.SessionStore
	reader   session.SnapshotReader
	replacer session.SnapshotReplacer
	admitter session.ScopeAdmitter

	mu            sync.Mutex
	reads         int
	replacements  int
	legacyWrites  int
	beforeReplace func(context.Context, session.SessionSnapshotReplacement) error
	replaceHook   func(context.Context, session.SessionSnapshotReplacement) error
	afterReplace  func(error) error
	readHook      func(string, session.SessionSnapshot, bool, error) (session.SessionSnapshot, bool, error)
}

func newObservedWorkingContextStore(backend *session.JSONLBackend) *observedWorkingContextStore {
	return &observedWorkingContextStore{
		SessionStore: backend,
		reader:       backend,
		replacer:     backend,
		admitter:     backend,
	}
}

func (store *observedWorkingContextStore) AdmitSessionScope(
	ctx context.Context,
	admission session.SessionScopeAdmission,
) (bool, error) {
	return store.admitter.AdmitSessionScope(ctx, admission)
}

func (store *observedWorkingContextStore) ReadSessionSnapshot(
	ctx context.Context,
	key string,
) (session.SessionSnapshot, bool, error) {
	snapshot, found, err := store.reader.ReadSessionSnapshot(ctx, key)
	store.mu.Lock()
	store.reads++
	hook := store.readHook
	store.mu.Unlock()
	if hook != nil {
		return hook(key, snapshot, found, err)
	}
	return snapshot, found, err
}

func (store *observedWorkingContextStore) readCount() int {
	store.mu.Lock()
	defer store.mu.Unlock()
	return store.reads
}

func (store *observedWorkingContextStore) ReplaceSessionSnapshot(
	ctx context.Context,
	replacement session.SessionSnapshotReplacement,
) error {
	store.mu.Lock()
	store.replacements++
	before := store.beforeReplace
	hook := store.replaceHook
	after := store.afterReplace
	store.mu.Unlock()
	if before != nil {
		if err := before(ctx, replacement); err != nil {
			return err
		}
	}
	var err error
	if hook != nil {
		err = hook(ctx, replacement)
	} else {
		err = store.replacer.ReplaceSessionSnapshot(ctx, replacement)
	}
	if after != nil {
		return after(err)
	}
	return err
}

func (store *observedWorkingContextStore) SetHistory(key string, history []providers.Message) {
	store.mu.Lock()
	store.legacyWrites++
	store.mu.Unlock()
	store.SessionStore.SetHistory(key, history)
}

func (store *observedWorkingContextStore) SetSummary(key, summary string) {
	store.mu.Lock()
	store.legacyWrites++
	store.mu.Unlock()
	store.SessionStore.SetSummary(key, summary)
}

func (store *observedWorkingContextStore) Save(key string) error {
	store.mu.Lock()
	store.legacyWrites++
	store.mu.Unlock()
	return store.SessionStore.Save(key)
}

func (store *observedWorkingContextStore) replaceCount() int {
	store.mu.Lock()
	defer store.mu.Unlock()
	return store.replacements
}

func (store *observedWorkingContextStore) legacyWriteCount() int {
	store.mu.Lock()
	defer store.mu.Unlock()
	return store.legacyWrites
}

type snapshotReaderOnlyWorkingContextStore struct {
	session.SessionStore
	reader session.SnapshotReader
}

func (store *snapshotReaderOnlyWorkingContextStore) ReadSessionSnapshot(
	ctx context.Context,
	key string,
) (session.SessionSnapshot, bool, error) {
	return store.reader.ReadSessionSnapshot(ctx, key)
}

type snapshotWorkingContextStoreWithoutAdmitter struct {
	session.SessionStore
	reader   session.SnapshotReader
	replacer session.SnapshotReplacer
}

func (store *snapshotWorkingContextStoreWithoutAdmitter) ReadSessionSnapshot(
	ctx context.Context,
	key string,
) (session.SessionSnapshot, bool, error) {
	return store.reader.ReadSessionSnapshot(ctx, key)
}

func (store *snapshotWorkingContextStoreWithoutAdmitter) ReplaceSessionSnapshot(
	ctx context.Context,
	replacement session.SessionSnapshotReplacement,
) error {
	return store.replacer.ReplaceSessionSnapshot(ctx, replacement)
}

type replaceBarrier struct {
	want    int
	arrived chan struct{}
	release chan struct{}
}

func newReplaceBarrier(want int) *replaceBarrier {
	return &replaceBarrier{
		want: want, arrived: make(chan struct{}, want), release: make(chan struct{}),
	}
}

func (barrier *replaceBarrier) wait(
	ctx context.Context,
	_ session.SessionSnapshotReplacement,
) error {
	select {
	case barrier.arrived <- struct{}{}:
	case <-ctx.Done():
		return ctx.Err()
	}
	select {
	case <-barrier.release:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (barrier *replaceBarrier) releaseWhenReady(t *testing.T) {
	t.Helper()
	for index := 0; index < barrier.want; index++ {
		select {
		case <-barrier.arrived:
		case <-time.After(5 * time.Second):
			t.Fatal("timed out waiting for concurrent replacements")
		}
	}
	close(barrier.release)
}

func scopePointer(scope session.SessionScope) *session.SessionScope { return &scope }
