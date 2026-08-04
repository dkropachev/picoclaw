package agent

import (
	"context"
	"errors"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sipeed/picoclaw/pkg/bus"
	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/providers"
	"github.com/sipeed/picoclaw/pkg/session"
)

type forcedAdmissionRaceStore struct {
	*session.JSONLBackend

	liveAdmissionReached chan struct{}
	releaseLiveAdmission chan struct{}
	signalOnce           sync.Once
	releaseOnce          sync.Once
}

func (s *forcedAdmissionRaceStore) AdmitSessionScope(
	ctx context.Context,
	admission session.SessionScopeAdmission,
) (bool, error) {
	if admission.Mode == session.ScopeAdmissionLive {
		s.signalOnce.Do(func() { close(s.liveAdmissionReached) })
		select {
		case <-s.releaseLiveAdmission:
		case <-ctx.Done():
			return false, ctx.Err()
		}
	}
	return s.JSONLBackend.AdmitSessionScope(ctx, admission)
}

func (s *forcedAdmissionRaceStore) release() {
	s.releaseOnce.Do(func() { close(s.releaseLiveAdmission) })
}

type forcedAdmissionRaceProvider struct {
	calls atomic.Int32
}

type noPreReadAdmissionStore struct {
	session.SessionStore
	reads     atomic.Int32
	admission session.SessionScopeAdmission
}

func (store *noPreReadAdmissionStore) ReadSessionSnapshot(
	context.Context,
	string,
) (session.SessionSnapshot, bool, error) {
	store.reads.Add(1)
	return session.SessionSnapshot{}, false, errors.New("snapshot read must not run")
}

func (store *noPreReadAdmissionStore) AdmitSessionScope(
	_ context.Context,
	admission session.SessionScopeAdmission,
) (bool, error) {
	store.admission = admission
	return true, nil
}

func (p *forcedAdmissionRaceProvider) Chat(
	context.Context,
	[]providers.Message,
	[]providers.ToolDefinition,
	string,
	map[string]any,
) (*providers.LLMResponse, error) {
	p.calls.Add(1)
	return &providers.LLMResponse{Content: "must not run"}, nil
}

func TestAdmitSessionMetadataNilScopeUsesLockedPreservationWithoutSnapshotRead(t *testing.T) {
	store := &noPreReadAdmissionStore{SessionStore: session.NewSessionManager("")}
	key := session.BuildMainSessionKey("main")
	if err := admitSessionMetadata(t.Context(), store, key, nil, nil, "main"); err != nil {
		t.Fatalf("admitSessionMetadata() error = %v", err)
	}
	if store.reads.Load() != 0 {
		t.Fatalf("snapshot reads = %d, want 0", store.reads.Load())
	}
	if store.admission.Key != key || !store.admission.PreserveExistingScope ||
		store.admission.Mode != session.ScopeAdmissionLive || store.admission.Scope == nil ||
		store.admission.Scope.Channel != "internal" {
		t.Fatalf("atomic admission = %+v", store.admission)
	}
}

func TestAdmitSessionMetadataRejectsCallerRequestedReviewOnLegacyStore(t *testing.T) {
	store := session.NewSessionManager("")
	reviewScope := &session.SessionScope{
		Version:    session.ScopeVersionV1,
		AgentID:    "main",
		Channel:    "review",
		Dimensions: []string{"review"},
		Values:     map[string]string{"review": "prc_spoofed_legacy"},
	}
	err := admitSessionMetadata(
		t.Context(),
		store,
		session.BuildSessionKey(*reviewScope),
		reviewScope,
		nil,
		"main",
	)
	if !errors.Is(err, session.ErrScopeAdmissionConflict) {
		t.Fatalf("admitSessionMetadata(review) error = %v, want conflict", err)
	}
	if sessions := store.ListSessions(); len(sessions) != 0 {
		t.Fatalf("rejected legacy review scope created sessions: %v", sessions)
	}
}

func TestProcessDirectReviewReservationWinsForcedAdmissionRace(t *testing.T) {
	testCtx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	t.Cleanup(cancel)

	cfg := config.DefaultConfig()
	cfg.Agents.Defaults.Workspace = t.TempDir()
	cfg.Agents.Defaults.ModelName = "test-model"
	cfg.Agents.Defaults.ContextManager = "seahorse"
	messageBus := bus.NewMessageBus()
	provider := &forcedAdmissionRaceProvider{}
	loop := newTestAgentLoopWithStrictModels(cfg, messageBus, provider)
	t.Cleanup(func() {
		loop.Stop()
		messageBus.Close()
		loop.Close()
	})

	runtimeAgent := loop.GetRegistry().GetDefaultAgent()
	if runtimeAgent == nil {
		t.Fatal("default runtime agent is unavailable")
	}
	backend, ok := runtimeAgent.Sessions.(*session.JSONLBackend)
	if !ok {
		t.Fatalf("runtime session store = %T, want *session.JSONLBackend", runtimeAgent.Sessions)
	}
	pausedStore := &forcedAdmissionRaceStore{
		JSONLBackend:         backend,
		liveAdmissionReached: make(chan struct{}),
		releaseLiveAdmission: make(chan struct{}),
	}
	runtimeAgent.Sessions = pausedStore
	t.Cleanup(pausedStore.release)

	caseID := "prc_33333333333333333333333333333333"
	reviewScope := session.SessionScope{
		Version:    session.ScopeVersionV1,
		AgentID:    runtimeAgent.ID,
		Channel:    "review",
		Account:    "default",
		Dimensions: []string{"review"},
		Values:     map[string]string{"review": caseID},
	}
	reviewKey := session.BuildSessionKey(reviewScope)
	reviewAliases := []string{
		"review:agent:main:case:" + caseID,
		"review:agent:main:case:" + caseID + ":binding:forced-race",
		"review:agent:main:case:" + caseID + ":version:1",
	}

	turnDone := make(chan error, 1)
	go func() {
		_, err := loop.ProcessDirectWithChannel(
			testCtx,
			"show me the pending review transcript",
			reviewKey,
			"cli",
			"direct",
		)
		turnDone <- err
	}()

	select {
	case <-pausedStore.liveAdmissionReached:
	case <-testCtx.Done():
		t.Fatalf("live turn did not reach atomic scope admission: %v", testCtx.Err())
	}

	if _, err := backend.AdmitSessionScope(context.Background(), session.SessionScopeAdmission{
		Key:            reviewKey,
		Scope:          session.CloneScope(&reviewScope),
		InitialAliases: append([]string(nil), reviewAliases[:2]...),
		Mode:           session.ScopeAdmissionReview,
	}); err != nil {
		t.Fatalf("reserve review scope during paused live admission: %v", err)
	}
	reservation, found, readErr := backend.ReadSessionSnapshot(context.Background(), reviewKey)
	if readErr != nil || !found {
		t.Fatalf("read review reservation = (found=%v, err=%v)", found, readErr)
	}
	if reservation.Revision == "" {
		t.Fatal("review reservation has no exact revision")
	}
	if replaceErr := backend.ReplaceSessionSnapshot(context.Background(), session.SessionSnapshotReplacement{
		Key:              reviewKey,
		History:          []providers.Message{{Role: "user", Content: "private review transcript"}},
		Summary:          "private review summary",
		Scope:            session.CloneScope(&reviewScope),
		Aliases:          append([]string(nil), reviewAliases...),
		ExpectedRevision: reservation.Revision,
	}); replaceErr != nil {
		t.Fatalf("publish exact review snapshot during paused live admission: %v", replaceErr)
	}
	before, found, readErr := backend.ReadSessionSnapshot(context.Background(), reviewKey)
	if readErr != nil || !found {
		t.Fatalf("read published review snapshot = (found=%v, err=%v)", found, readErr)
	}

	pausedStore.release()
	select {
	case turnErr := <-turnDone:
		if !errors.Is(turnErr, session.ErrScopeAdmissionConflict) {
			t.Fatalf("ProcessDirectWithChannel() error = %v, want ErrScopeAdmissionConflict", turnErr)
		}
	case <-testCtx.Done():
		t.Fatalf("live turn did not finish after review reservation won: %v", testCtx.Err())
	}

	if calls := provider.calls.Load(); calls != 0 {
		t.Fatalf("losing live turn called provider %d time(s), want 0", calls)
	}
	after, found, readErr := backend.ReadSessionSnapshot(context.Background(), reviewKey)
	if readErr != nil || !found {
		t.Fatalf("read review snapshot after rejected turn = (found=%v, err=%v)", found, readErr)
	}
	if !reflect.DeepEqual(after, before) {
		t.Fatalf("losing live turn mutated exact review snapshot:\nbefore: %#v\nafter:  %#v", before, after)
	}

	manager, ok := loop.contextManager.(*seahorseContextManager)
	if !ok {
		t.Fatalf("context manager = %T, want *seahorseContextManager", loop.contextManager)
	}
	conversation, err := manager.engine.GetRetrieval().Store().GetConversationBySessionKey(
		context.Background(),
		reviewKey,
	)
	if err != nil {
		t.Fatalf("read rejected turn from Seahorse: %v", err)
	}
	if conversation != nil {
		t.Fatalf("losing live turn mutated Seahorse context: %#v", conversation)
	}
}

func TestRunAgentLoopNilScopeReviewReservationWinsForcedAdmissionRace(t *testing.T) {
	testCtx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	t.Cleanup(cancel)

	cfg := config.DefaultConfig()
	cfg.Agents.Defaults.Workspace = t.TempDir()
	cfg.Agents.Defaults.ModelName = "test-model"
	messageBus := bus.NewMessageBus()
	provider := &forcedAdmissionRaceProvider{}
	loop := newTestAgentLoopWithStrictModels(cfg, messageBus, provider)
	t.Cleanup(func() {
		loop.Stop()
		messageBus.Close()
		loop.Close()
	})

	runtimeAgent := loop.GetRegistry().GetDefaultAgent()
	if runtimeAgent == nil {
		t.Fatal("default runtime agent is unavailable")
	}
	backend, ok := runtimeAgent.Sessions.(*session.JSONLBackend)
	if !ok {
		t.Fatalf("runtime session store = %T, want *session.JSONLBackend", runtimeAgent.Sessions)
	}
	pausedStore := &forcedAdmissionRaceStore{
		JSONLBackend:         backend,
		liveAdmissionReached: make(chan struct{}),
		releaseLiveAdmission: make(chan struct{}),
	}
	runtimeAgent.Sessions = pausedStore
	t.Cleanup(pausedStore.release)

	caseID := "prc_44444444444444444444444444444444"
	reviewScope := session.SessionScope{
		Version:    session.ScopeVersionV1,
		AgentID:    runtimeAgent.ID,
		Channel:    "review",
		Account:    "default",
		Dimensions: []string{"review"},
		Values:     map[string]string{"review": caseID},
	}
	reviewKey := session.BuildSessionKey(reviewScope)
	reviewAliases := []string{
		"review:agent:main:case:" + caseID,
		"review:agent:main:case:" + caseID + ":binding:nil-scope-race",
		"review:agent:main:case:" + caseID + ":version:1",
	}

	turnDone := make(chan error, 1)
	go func() {
		_, err := loop.runAgentLoop(testCtx, runtimeAgent, processOptions{
			Dispatch: DispatchRequest{
				SessionKey:  reviewKey,
				UserMessage: "read a session supplied without structured scope",
			},
			DefaultResponse: defaultResponse,
			EnableSummary:   true,
		})
		turnDone <- err
	}()

	select {
	case <-pausedStore.liveAdmissionReached:
	case <-testCtx.Done():
		t.Fatalf("nil-scope turn bypassed atomic scope admission: %v", testCtx.Err())
	}

	if _, err := backend.AdmitSessionScope(context.Background(), session.SessionScopeAdmission{
		Key:            reviewKey,
		Scope:          session.CloneScope(&reviewScope),
		InitialAliases: append([]string(nil), reviewAliases[:2]...),
		Mode:           session.ScopeAdmissionReview,
	}); err != nil {
		t.Fatalf("reserve review scope during paused nil-scope admission: %v", err)
	}
	reservation, found, readErr := backend.ReadSessionSnapshot(context.Background(), reviewKey)
	if readErr != nil || !found {
		t.Fatalf("read review reservation = (found=%v, err=%v)", found, readErr)
	}
	if replaceErr := backend.ReplaceSessionSnapshot(context.Background(), session.SessionSnapshotReplacement{
		Key:              reviewKey,
		History:          []providers.Message{{Role: "user", Content: "private nil-scope race transcript"}},
		Summary:          "private nil-scope race summary",
		Scope:            session.CloneScope(&reviewScope),
		Aliases:          append([]string(nil), reviewAliases...),
		ExpectedRevision: reservation.Revision,
	}); replaceErr != nil {
		t.Fatalf("publish review snapshot during paused nil-scope admission: %v", replaceErr)
	}
	before, found, readErr := backend.ReadSessionSnapshot(context.Background(), reviewKey)
	if readErr != nil || !found {
		t.Fatalf("read published review snapshot = (found=%v, err=%v)", found, readErr)
	}

	pausedStore.release()
	select {
	case turnErr := <-turnDone:
		if !errors.Is(turnErr, session.ErrScopeAdmissionConflict) {
			t.Fatalf("runAgentLoop() error = %v, want ErrScopeAdmissionConflict", turnErr)
		}
	case <-testCtx.Done():
		t.Fatalf("nil-scope turn did not finish after review reservation won: %v", testCtx.Err())
	}
	if calls := provider.calls.Load(); calls != 0 {
		t.Fatalf("losing nil-scope turn called provider %d time(s), want 0", calls)
	}
	after, found, readErr := backend.ReadSessionSnapshot(context.Background(), reviewKey)
	if readErr != nil || !found {
		t.Fatalf("read review snapshot after rejected nil-scope turn = (found=%v, err=%v)", found, readErr)
	}
	if !reflect.DeepEqual(after, before) {
		t.Fatalf("losing nil-scope turn mutated exact review snapshot:\nbefore: %#v\nafter:  %#v", before, after)
	}
}
