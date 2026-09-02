//nolint:govet // Independent lifecycle assertions intentionally use narrow error scopes.
package memory

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sipeed/picoclaw/internal/sqlitestore"
	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/database"
	"github.com/sipeed/picoclaw/pkg/providers"
)

func TestSessionBrokerCompleteTypedOperationLifecycle(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "workspace", "sessions")
	adapter, err := newBrokerAdapterAtDirectory(dir, SessionsStoreID)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = adapter.Close() })
	if adapter.StoreID() != SessionsStoreID || adapter.LocalStore() != nil {
		t.Fatalf("new adapter = StoreID:%q store:%p", adapter.StoreID(), adapter.LocalStore())
	}

	resolved := sessionBrokerCall[StoreResolutionResponse](
		t,
		adapter,
		SessionOperationResolveStore,
		StoreResolutionRequest{WorkspaceSelector: adapter.selector},
	)
	if resolved.StoreID != SessionsStoreID {
		t.Fatalf("resolved StoreID = %q", resolved.StoreID)
	}
	if _, err := sessionBrokerHandle(
		t.Context(), adapter, SessionOperationResolveStore,
		StoreResolutionRequest{WorkspaceSelector: "unknown"},
	); database.CodeOf(err) != database.CodeUnauthorized {
		t.Fatalf("unknown workspace error = %v", err)
	}

	ping := sessionBrokerCall[sessionBrokerResponse](
		t, adapter, sessionOperationPing, sessionStoreRequest{StoreID: SessionsStoreID},
	)
	if !ping.OK || adapter.LocalStore() == nil {
		t.Fatalf("session ping = %#v", ping)
	}
	for _, message := range []providers.Message{
		{Role: "user", Content: "one"},
		{Role: "assistant", Content: "two"},
		{Role: "user", Content: "three"},
	} {
		response := sessionBrokerCall[sessionBrokerResponse](
			t,
			adapter,
			sessionOperationAdd,
			sessionAddRequest{StoreID: SessionsStoreID, Key: "primary", Message: message},
		)
		if !response.OK {
			t.Fatalf("session add response = %#v", response)
		}
	}
	history := sessionBrokerCall[sessionBrokerResponse](
		t,
		adapter,
		sessionOperationHistory,
		sessionHistoryRequest{StoreID: SessionsStoreID, Key: "primary", Offset: 0, Limit: 1},
	)
	if len(history.History) != 1 || history.Next != "1" {
		t.Fatalf("session history page = %#v", history)
	}

	sessionBrokerCall[sessionBrokerResponse](
		t,
		adapter,
		sessionOperationSetSummary,
		sessionSummaryRequest{StoreID: SessionsStoreID, Key: "primary", Summary: "summary"},
	)
	summary := sessionBrokerCall[sessionBrokerResponse](
		t,
		adapter,
		sessionOperationGetSummary,
		sessionKeyRequest{StoreID: SessionsStoreID, Key: "primary"},
	)
	if summary.Summary != "summary" {
		t.Fatalf("session summary = %#v", summary)
	}

	replacementHistory := []providers.Message{
		{Role: "user", Content: "replacement-one"},
		{Role: "assistant", Content: "replacement-two"},
	}
	sessionBrokerCall[sessionBrokerResponse](
		t,
		adapter,
		sessionOperationSetHistory,
		sessionHistoryMutationRequest{
			StoreID: SessionsStoreID, Key: "primary", History: replacementHistory,
		},
	)
	sessionBrokerCall[sessionBrokerResponse](
		t,
		adapter,
		sessionOperationTruncate,
		sessionHistoryMutationRequest{StoreID: SessionsStoreID, Key: "primary", KeepLast: 1},
	)
	sessionBrokerCall[sessionBrokerResponse](
		t,
		adapter,
		sessionOperationCompact,
		sessionKeyRequest{StoreID: SessionsStoreID, Key: "primary"},
	)

	scope := []byte(`{"version":1,"agent_id":"main","channel":"pico","account":"","dimensions":[],"values":{}}`)
	sessionBrokerCall[sessionBrokerResponse](
		t,
		adapter,
		sessionOperationUpsertMeta,
		sessionMetaRequest{
			StoreID: SessionsStoreID, Key: "primary", Scope: scope, Aliases: []string{"alias"},
		},
	)
	resolvedKey := sessionBrokerCall[sessionBrokerResponse](
		t,
		adapter,
		sessionOperationResolve,
		sessionKeyRequest{StoreID: SessionsStoreID, Key: "alias"},
	)
	if !resolvedKey.Found || resolvedKey.CanonicalKey != "primary" {
		t.Fatalf("session alias resolution = %#v", resolvedKey)
	}
	metaResponse := sessionBrokerCall[sessionBrokerResponse](
		t,
		adapter,
		sessionOperationGetMeta,
		sessionKeyRequest{StoreID: SessionsStoreID, Key: "primary"},
	)
	if metaResponse.Meta.Key != "primary" {
		t.Fatalf("session metadata = %#v", metaResponse)
	}
	state := sessionBrokerCall[sessionBrokerResponse](
		t,
		adapter,
		sessionOperationReadState,
		sessionReadStateRequest{
			StoreID: SessionsStoreID, Key: "primary", Offset: 0, Limit: 1,
			ExpectedRevision: metaResponse.Meta.Revision,
		},
	)
	if !state.Found || state.CanonicalKey != "primary" || state.Revision == "" {
		t.Fatalf("session state = %#v", state)
	}
	if _, err := sessionBrokerHandle(
		t.Context(), adapter, sessionOperationReadState,
		sessionReadStateRequest{
			StoreID: SessionsStoreID, Key: "primary", Offset: 0, Limit: 1,
			ExpectedRevision: "stale",
		},
	); database.CodeOf(err) != database.CodeConflict {
		t.Fatalf("stale state error = %v", err)
	}

	snapshot := SessionSnapshotReplacement{
		Key: "snapshot", History: []providers.Message{{Role: "user", Content: "snapshot"}},
		Summary: "snapshot summary", Scope: scope, Aliases: []string{"snapshot-alias"},
	}
	sessionBrokerCall[sessionBrokerResponse](
		t,
		adapter,
		sessionOperationReplaceSnapshot,
		sessionSnapshotRequest{StoreID: SessionsStoreID, Replacement: &snapshot},
	)
	sessionBrokerCall[sessionBrokerResponse](
		t,
		adapter,
		sessionOperationEnsure,
		sessionKeyRequest{StoreID: SessionsStoreID, Key: "empty"},
	)
	listed := sessionBrokerCall[sessionBrokerResponse](
		t,
		adapter,
		sessionOperationList,
		sessionListRequest{StoreID: SessionsStoreID, Limit: 1},
	)
	if len(listed.Sessions) != 1 || listed.Next == "" {
		t.Fatalf("session list page = %#v", listed)
	}

	mutation := sessionBrokerCall[sessionBrokerResponse](
		t,
		adapter,
		sessionOperationReadMutationMeta,
		sessionKeyRequest{StoreID: SessionsStoreID, Key: "primary"},
	)
	updated := cloneSessionMeta(mutation.Meta)
	updated.Summary = "applied"
	applied := sessionBrokerCall[sessionBrokerResponse](
		t,
		adapter,
		sessionOperationApplyMeta,
		sessionApplyMetaRequest{
			StoreID: SessionsStoreID, RequestedKey: "primary", CanonicalKey: "primary",
			Existed: mutation.Found, Expected: mutation.Meta,
			ExpectedRevision: mutation.Revision, Replacement: updated,
		},
	)
	if !applied.Changed || !applied.Found {
		t.Fatalf("applied session metadata = %#v", applied)
	}
	mutation = sessionBrokerCall[sessionBrokerResponse](
		t,
		adapter,
		sessionOperationReadMutationMeta,
		sessionKeyRequest{StoreID: SessionsStoreID, Key: "admission-new"},
	)
	admitted := sessionBrokerCall[sessionBrokerResponse](
		t,
		adapter,
		sessionOperationApplyAdmission,
		sessionApplyAdmissionRequest{
			StoreID: SessionsStoreID, RequestedKey: "admission-new",
			CanonicalKey: mutation.CanonicalKey,
			Existed:      mutation.Found, Expected: mutation.Meta,
			ExpectedRevision: mutation.Revision,
			Decision: SessionMetaAdmissionDecision{
				Update: true, Scope: scope, Aliases: []string{"admitted-alias"},
			},
		},
	)
	if !admitted.Changed {
		t.Fatalf("session admission = %#v", admitted)
	}

	casMeta := sessionBrokerCall[sessionBrokerResponse](
		t,
		adapter,
		sessionOperationGetMeta,
		sessionKeyRequest{StoreID: SessionsStoreID, Key: "empty"},
	).Meta
	casReplacement := cloneSessionMeta(casMeta)
	casReplacement.Summary = "cas"
	cas := sessionBrokerCall[sessionBrokerResponse](
		t,
		adapter,
		sessionOperationCompareSwapMeta,
		sessionMetaCASRequest{
			StoreID: SessionsStoreID, Key: "empty", Expected: casMeta, Replacement: &casReplacement,
		},
	)
	if !cas.Changed {
		t.Fatalf("session metadata CAS = %#v", cas)
	}
	deleteMeta := sessionBrokerCall[sessionBrokerResponse](
		t,
		adapter,
		sessionOperationGetMeta,
		sessionKeyRequest{StoreID: SessionsStoreID, Key: "empty"},
	).Meta
	deleteMeta.Summary = ""
	sessionBrokerCall[sessionBrokerResponse](
		t,
		adapter,
		sessionOperationCompareSwapMeta,
		sessionMetaCASRequest{
			StoreID: SessionsStoreID, Key: "empty",
			Expected: sessionBrokerCall[sessionBrokerResponse](
				t, adapter, sessionOperationGetMeta,
				sessionKeyRequest{StoreID: SessionsStoreID, Key: "empty"},
			).Meta,
			Replacement: &deleteMeta,
		},
	)
	deleteMeta = sessionBrokerCall[sessionBrokerResponse](
		t,
		adapter,
		sessionOperationGetMeta,
		sessionKeyRequest{StoreID: SessionsStoreID, Key: "empty"},
	).Meta
	deletedEmpty := sessionBrokerCall[sessionBrokerResponse](
		t,
		adapter,
		sessionOperationCompareDelete,
		sessionMetaCASRequest{StoreID: SessionsStoreID, Key: "empty", Expected: deleteMeta},
	)
	if !deletedEmpty.Changed {
		t.Fatalf("empty session compare-delete = %#v", deletedEmpty)
	}
	deleted := sessionBrokerCall[sessionBrokerResponse](
		t,
		adapter,
		sessionOperationDelete,
		sessionDeleteRequest{StoreID: SessionsStoreID, Keys: []string{"snapshot"}},
	)
	if !deleted.Changed {
		t.Fatalf("session delete = %#v", deleted)
	}

	if _, err := sessionBrokerHandle(
		t.Context(), adapter, "session.unknown", sessionStoreRequest{StoreID: SessionsStoreID},
	); database.CodeOf(err) != database.CodeUnsupported {
		t.Fatalf("unknown session operation error = %v", err)
	}
}

func TestSessionBrokerValidationCloseAndErrorMappingBoundaries(t *testing.T) {
	if (*BrokerAdapter)(nil).LocalStore() != nil || (*BrokerAdapter)(nil).StoreID() != "" {
		t.Fatal("nil adapter exposed state")
	}
	if _, err := (*BrokerAdapter)(nil).EnsureLocalStore(); database.CodeOf(err) != database.CodeUnavailable {
		t.Fatalf("nil EnsureLocalStore error = %v", err)
	}
	if err := (*BrokerAdapter)(nil).Close(); err != nil {
		t.Fatalf("nil adapter close = %v", err)
	}
	_, err := newBrokerAdapterAtDirectory("bad\x00dir", SessionsStoreID)
	if database.CodeOf(err) != database.CodeInvalid {
		t.Fatalf("invalid adapter directory error = %v", err)
	}

	adapter, err := newBrokerAdapterAtDirectory(
		filepath.Join(t.TempDir(), "workspace", "sessions"), SessionsStoreID,
	)
	if err != nil {
		t.Fatal(err)
	}
	for _, request := range []database.Request{
		{Domain: "wrong", Version: SessionsBrokerVersion, Operation: sessionOperationPing},
		{Domain: SessionsBrokerDomain, Version: 99, Operation: sessionOperationPing},
		{Domain: SessionsBrokerDomain, Version: SessionsBrokerVersion, Operation: "thread.ping"},
	} {
		if _, err := adapter.Handle(t.Context(), request); database.CodeOf(err) != database.CodeUnsupported {
			t.Fatalf("unsupported request %#v error = %v", request, err)
		}
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := sessionBrokerHandle(
		canceled, adapter, sessionOperationPing, sessionStoreRequest{StoreID: SessionsStoreID},
	); database.CodeOf(err) != database.CodeDeadline {
		t.Fatalf("canceled session request error = %v", err)
	}
	if err := adapter.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := adapter.EnsureLocalStore(); database.CodeOf(err) != database.CodeUnavailable {
		t.Fatalf("closed EnsureLocalStore error = %v", err)
	}
	if _, err := sessionBrokerHandle(
		t.Context(), adapter, sessionOperationPing, sessionStoreRequest{StoreID: SessionsStoreID},
	); database.CodeOf(err) != database.CodeUnavailable {
		t.Fatalf("closed session request error = %v", err)
	}

	for _, test := range []struct {
		err  error
		code database.ErrorCode
	}{
		{nil, ""},
		{database.NewError(database.CodeNotFound, "missing"), database.CodeNotFound},
		{context.Canceled, database.CodeDeadline},
		{ErrSnapshotConflict, database.CodeConflict},
		{sqlitestore.ErrTooNew, database.CodeUnsupported},
		{sqlitestore.ErrIntegrity, database.CodeIntegrity},
		{errors.New("unknown"), database.CodeInternal},
	} {
		if got := database.CodeOf(mapSessionBrokerError(test.err)); got != test.code {
			t.Errorf("mapSessionBrokerError(%v) = %q, want %q", test.err, got, test.code)
		}
	}
	for _, key := range []string{"", " spaced ", strings.Repeat("x", 4097), "bad\x00key"} {
		if validSessionKey(key) {
			t.Errorf("invalid session key accepted: %q", key)
		}
	}
	if !validSessionKey("valid") {
		t.Fatal("valid session key rejected")
	}
}

func TestSessionBrokerRejectsMalformedTypedOperations(t *testing.T) {
	adapter, err := newBrokerAdapterAtDirectory(
		filepath.Join(t.TempDir(), "workspace", "sessions"), SessionsStoreID,
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = adapter.Close() })
	operations := []string{
		sessionOperationPing,
		sessionOperationAdd,
		sessionOperationHistory,
		sessionOperationGetSummary,
		sessionOperationSetSummary,
		sessionOperationSetHistory,
		sessionOperationTruncate,
		sessionOperationCompact,
		sessionOperationList,
		sessionOperationResolve,
		sessionOperationGetMeta,
		sessionOperationUpsertMeta,
		sessionOperationReadState,
		sessionOperationReplaceSnapshot,
		sessionOperationPromoteAlias,
		sessionOperationEnsure,
		sessionOperationDelete,
		sessionOperationCompareSwapMeta,
		sessionOperationCompareDelete,
		sessionOperationReadMutationMeta,
		sessionOperationApplyMeta,
		sessionOperationApplyAdmission,
	}
	for _, operation := range operations {
		t.Run(operation, func(t *testing.T) {
			if _, err := sessionBrokerHandle(
				t.Context(), adapter, operation, database.EmptyPayload{},
			); database.CodeOf(err) != database.CodeInvalid {
				t.Fatalf("malformed %s error = %v", operation, err)
			}
		})
	}

	scope := []byte(`{"version":1,"agent_id":"main","channel":"pico","account":"","dimensions":[],"values":{}}`)
	sessionBrokerCall[sessionBrokerResponse](
		t,
		adapter,
		sessionOperationEnsure,
		sessionKeyRequest{StoreID: SessionsStoreID, Key: "conflict"},
	)
	mutation := sessionBrokerCall[sessionBrokerResponse](
		t,
		adapter,
		sessionOperationReadMutationMeta,
		sessionKeyRequest{StoreID: SessionsStoreID, Key: "conflict"},
	)
	if _, err := sessionBrokerHandle(
		t.Context(), adapter, sessionOperationApplyMeta,
		sessionApplyMetaRequest{
			StoreID: SessionsStoreID, RequestedKey: "conflict", CanonicalKey: "conflict",
			Existed: mutation.Found, Expected: mutation.Meta, ExpectedRevision: "stale",
			Replacement: mutation.Meta,
		},
	); database.CodeOf(err) != database.CodeConflict {
		t.Fatalf("stale metadata application error = %v", err)
	}
	if _, err := sessionBrokerHandle(
		t.Context(), adapter, sessionOperationApplyAdmission,
		sessionApplyAdmissionRequest{
			StoreID: SessionsStoreID, RequestedKey: "conflict", CanonicalKey: "conflict",
			Existed: mutation.Found, Expected: mutation.Meta, ExpectedRevision: "stale",
			Decision: SessionMetaAdmissionDecision{Update: true, Scope: scope},
		},
	); database.CodeOf(err) != database.CodeConflict {
		t.Fatalf("stale admission application error = %v", err)
	}
}

func TestSessionBrokerMapsClosedProviderOperationFailures(t *testing.T) {
	adapter, err := newBrokerAdapterAtDirectory(
		filepath.Join(t.TempDir(), "workspace", "sessions"), SessionsStoreID,
	)
	if err != nil {
		t.Fatal(err)
	}
	local, err := adapter.EnsureLocalStore()
	if err != nil {
		t.Fatal(err)
	}
	if err := local.Close(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = adapter.Close() })
	scope := []byte(`{"version":1,"agent_id":"main","channel":"pico","account":"","dimensions":[],"values":{}}`)
	meta := SessionMeta{Key: "key"}
	replacement := SessionSnapshotReplacement{Key: "key", Scope: scope}
	requests := []struct {
		operation string
		input     any
	}{
		{sessionOperationAdd, sessionAddRequest{
			StoreID: SessionsStoreID, Key: "key", Message: providers.Message{Role: "user", Content: "x"},
		}},
		{sessionOperationHistory, sessionHistoryRequest{
			StoreID: SessionsStoreID, Key: "key", Limit: 1,
		}},
		{sessionOperationGetSummary, sessionKeyRequest{StoreID: SessionsStoreID, Key: "key"}},
		{sessionOperationSetSummary, sessionSummaryRequest{
			StoreID: SessionsStoreID, Key: "key", Summary: "x",
		}},
		{sessionOperationSetHistory, sessionHistoryMutationRequest{
			StoreID: SessionsStoreID, Key: "key", History: []providers.Message{},
		}},
		{sessionOperationTruncate, sessionHistoryMutationRequest{
			StoreID: SessionsStoreID, Key: "key", KeepLast: 1,
		}},
		{sessionOperationResolve, sessionKeyRequest{StoreID: SessionsStoreID, Key: "key"}},
		{sessionOperationGetMeta, sessionKeyRequest{StoreID: SessionsStoreID, Key: "key"}},
		{sessionOperationUpsertMeta, sessionMetaRequest{
			StoreID: SessionsStoreID, Key: "key", Scope: scope,
		}},
		{sessionOperationReadState, sessionReadStateRequest{
			StoreID: SessionsStoreID, Key: "key", Limit: 1,
		}},
		{sessionOperationReplaceSnapshot, sessionSnapshotRequest{
			StoreID: SessionsStoreID, Replacement: &replacement,
		}},
		{sessionOperationPromoteAlias, sessionMetaRequest{
			StoreID: SessionsStoreID, Key: "key", Scope: scope,
		}},
		{sessionOperationEnsure, sessionKeyRequest{StoreID: SessionsStoreID, Key: "key"}},
		{sessionOperationDelete, sessionDeleteRequest{StoreID: SessionsStoreID, Keys: []string{"key"}}},
		{sessionOperationCompareSwapMeta, sessionMetaCASRequest{
			StoreID: SessionsStoreID, Key: "key", Expected: meta, Replacement: &meta,
		}},
		{sessionOperationCompareDelete, sessionMetaCASRequest{
			StoreID: SessionsStoreID, Key: "key", Expected: meta,
		}},
		{sessionOperationReadMutationMeta, sessionKeyRequest{StoreID: SessionsStoreID, Key: "key"}},
		{sessionOperationApplyMeta, sessionApplyMetaRequest{
			StoreID: SessionsStoreID, RequestedKey: "key", CanonicalKey: "key",
			Expected: meta, Replacement: meta,
		}},
		{sessionOperationApplyAdmission, sessionApplyAdmissionRequest{
			StoreID: SessionsStoreID, RequestedKey: "key", CanonicalKey: "key",
			Expected: meta, Decision: SessionMetaAdmissionDecision{Update: true, Scope: scope},
		}},
	}
	for _, request := range requests {
		t.Run(request.operation, func(t *testing.T) {
			if _, err := sessionBrokerHandle(
				t.Context(), adapter, request.operation, request.input,
			); err == nil {
				t.Fatal("closed provider operation succeeded")
			}
		})
	}
}

func TestSessionBrokerWorkspaceResolutionAndOfflineMigration(t *testing.T) {
	home := t.TempDir()
	if err := RunOfflineDatabaseMigration(
		t.Context(), filepath.Join(home, "workspace", "sessions"),
	); database.CodeOf(err) != database.CodeConflict {
		t.Fatalf("unfenced migration error = %v", err)
	}
	fence, err := database.AcquireMigrationFence(home)
	if err != nil {
		t.Fatal(err)
	}
	if err := RunOfflineDatabaseMigration(
		t.Context(), filepath.Join(home, "workspace", "sessions"),
	); err != nil {
		_ = fence.Close()
		t.Fatal(err)
	}
	if err := fence.Close(); err != nil {
		t.Fatal(err)
	}

	if _, err := ResolveBrokerStoreID(t.Context(), nil, "sessions"); database.CodeOf(err) != database.CodeUnavailable {
		t.Fatalf("nil broker resolution error = %v", err)
	}
	for _, path := range []string{"", "bad\x00sessions"} {
		if _, err := WorkspaceSelector(path); database.CodeOf(err) != database.CodeInvalid {
			t.Fatalf("WorkspaceSelector(%q) error = %v", path, err)
		}
	}
	userHome := t.TempDir()
	t.Setenv("HOME", userHome)
	for _, configured := range []string{"", "relative", "~", "~/nested", filepath.Join(home, "absolute")} {
		if _, err := resolveConfiguredWorkspace(home, configured); err != nil {
			t.Errorf("resolveConfiguredWorkspace(%q): %v", configured, err)
		}
	}
}

func TestSessionBrokerCatalogResolutionAndDeepCloneBoundaries(t *testing.T) {
	home := t.TempDir()
	if _, err := NewBrokerAdapter(home, nil, "invalid store"); database.CodeOf(err) != database.CodeInvalid {
		t.Fatalf("invalid broker StoreID error = %v", err)
	}
	primary, err := configuredSessionsDirectory(home, nil, SessionsStoreID)
	if err != nil || primary != filepath.Join(home, "workspace", "sessions") {
		t.Fatalf("default sessions directory = %q, %v", primary, err)
	}
	agentWorkspace := filepath.Join(home, "agent")
	cfg := &config.Config{Agents: config.AgentsConfig{
		Defaults: config.AgentDefaults{Workspace: filepath.Join(home, "primary")},
		List: []config.AgentConfig{
			{ID: "blank", Workspace: ""},
			{ID: "duplicate", Workspace: filepath.Join(home, "primary")},
			{ID: "agent", Workspace: agentWorkspace},
		},
	}}
	selector, err := WorkspaceSelector(filepath.Join(agentWorkspace, "sessions"))
	if err != nil {
		t.Fatal(err)
	}
	agentID, err := database.ParseStoreID("workspace." + selector + ".sessions")
	if err != nil {
		t.Fatal(err)
	}
	directory, err := configuredSessionsDirectory(home, cfg, agentID)
	if err != nil || directory != filepath.Join(agentWorkspace, "sessions") {
		t.Fatalf("agent sessions directory = %q, %v", directory, err)
	}
	if _, err := configuredSessionsDirectory(
		home,
		cfg,
		"workspace.unknown.sessions",
	); database.CodeOf(
		err,
	) != database.CodeUnauthorized {
		t.Fatalf("unknown sessions StoreID error = %v", err)
	}

	now := time.Now().UTC()
	cache := &providers.CacheControl{Type: "ephemeral"}
	function := &providers.FunctionCall{Name: "tool", Arguments: `{}`}
	google := &providers.GoogleExtra{ThoughtSignature: "signature"}
	original := []providers.Message{{
		Role: "assistant", Content: "content", CreatedAt: &now,
		Media: []string{"media"}, Attachments: []providers.Attachment{{Ref: "attachment"}},
		Parts:       []providers.PromptPart{{Type: "text", Text: "part"}},
		SystemParts: []providers.ContentBlock{{Type: "text", Text: "system", CacheControl: cache}},
		ToolCalls: []providers.ToolCall{{
			ID: "call", Function: function,
			ExtraContent: &providers.ExtraContent{Google: google},
		}},
	}}
	cloned := cloneProviderMessages(original)
	cloned[0].Media[0] = "changed"
	cloned[0].SystemParts[0].CacheControl.Type = "changed"
	cloned[0].ToolCalls[0].Function.Name = "changed"
	cloned[0].ToolCalls[0].ExtraContent.Google.ThoughtSignature = "changed"
	if original[0].Media[0] != "media" || original[0].SystemParts[0].CacheControl.Type != "ephemeral" ||
		original[0].ToolCalls[0].Function.Name != "tool" ||
		original[0].ToolCalls[0].ExtraContent.Google.ThoughtSignature != "signature" {
		t.Fatal("deep broker message clone aliases input")
	}
}

func TestSessionBrokerAuthorityAndResolutionFailureBoundaries(t *testing.T) {
	home := t.TempDir()
	func() {
		restoreAuthority := database.SuspendProviderTestAuthority()
		defer restoreAuthority()
		allowUnfencedSessionsProviderForTests.Store(false)
		if adapter, err := NewBrokerAdapter(home, nil, SessionsStoreID); adapter != nil ||
			database.CodeOf(err) != database.CodeUnauthorized {
			t.Fatalf("unfenced broker adapter = %#v, %v", adapter, err)
		}
	}()
	allowUnfencedSessionsProviderForTests.Store(true)
	t.Cleanup(func() { allowUnfencedSessionsProviderForTests.Store(true) })
	if _, err := NewBrokerAdapter(
		home,
		nil,
		"workspace.unknown.sessions",
	); database.CodeOf(
		err,
	) != database.CodeUnauthorized {
		t.Fatalf("uncataloged broker adapter error = %v", err)
	}
	if _, err := configuredSessionsDirectory(
		" invalid-home ",
		nil,
		SessionsStoreID,
	); database.CodeOf(
		err,
	) != database.CodeInvalid {
		t.Fatalf("invalid configured sessions home error = %v", err)
	}

	client := startMemoryCoverageServer(t, t.TempDir(), database.HandlerFunc(
		func(_ context.Context, request database.Request) (any, error) {
			if request.Operation == SessionOperationResolveStore {
				return StoreResolutionResponse{}, nil
			}
			return nil, database.NewError(database.CodeUnavailable, "unavailable")
		},
	))
	if _, err := ResolveBrokerStoreID(
		t.Context(),
		client,
		filepath.Join(home, "sessions"),
	); database.CodeOf(
		err,
	) != database.CodeIntegrity {
		t.Fatalf("invalid resolved StoreID error = %v", err)
	}
	store := &SQLiteStore{brokerClient: client, storeID: SessionsStoreID}
	if err := store.pingBroker(t.Context()); database.CodeOf(err) != database.CodeUnavailable {
		t.Fatalf("broker ping failure = %v", err)
	}
}

func TestSessionBrokerClientFacadeCompleteLifecycle(t *testing.T) {
	home := t.TempDir()
	dir := filepath.Join(home, "workspace", "sessions")
	_, _, client := startSessionBroker(t, home, dir)
	previous := database.RuntimeClient()
	database.InstallProcessClient(client)
	t.Cleanup(func() { database.InstallProcessClient(previous) })
	store, err := NewStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	if store.db != nil || store.StoreID() != SessionsStoreID {
		t.Fatalf("broker client store = db:%p id:%q", store.db, store.StoreID())
	}
	if err := store.AddMessage(t.Context(), "client", "user", "one"); err != nil {
		t.Fatal(err)
	}
	if err := store.AddFullMessage(
		t.Context(), "client", providers.Message{Role: "assistant", Content: "two"},
	); err != nil {
		t.Fatal(err)
	}
	if history, err := store.GetHistory(t.Context(), "client"); err != nil || len(history) != 2 {
		t.Fatalf("client history = %#v, %v", history, err)
	}
	if err := store.SetSummary(t.Context(), "client", "summary"); err != nil {
		t.Fatal(err)
	}
	if summary, err := store.GetSummary(t.Context(), "client"); err != nil || summary != "summary" {
		t.Fatalf("client summary = %q, %v", summary, err)
	}
	if err := store.SetHistory(t.Context(), "client", []providers.Message{
		{Role: "user", Content: "reset-one"},
		{Role: "assistant", Content: "reset-two"},
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.TruncateHistory(t.Context(), "client", 1); err != nil {
		t.Fatal(err)
	}
	if err := store.Compact(t.Context(), "client"); err != nil {
		t.Fatal(err)
	}

	scope := []byte(`{"version":1,"agent_id":"main","channel":"pico","account":"","dimensions":[],"values":{}}`)
	if err := store.UpsertSessionMeta(t.Context(), "client", scope, []string{"client-alias"}); err != nil {
		t.Fatal(err)
	}
	key, found, err := store.ResolveSessionKey(t.Context(), "client-alias")
	if err != nil || !found || key != "client" {
		t.Fatalf("client alias = %q, %t, %v", key, found, err)
	}
	key, history, meta, modified, found, err := store.ReadSessionStateStrict(
		t.Context(), "client",
	)
	if err != nil || !found || key != "client" || len(history) != 1 || meta.Key != "client" || modified.IsZero() {
		t.Fatalf("client state = %q %#v %#v %v %t %v", key, history, meta, modified, found, err)
	}
	if snapshotKey, snapshotHistory, snapshotMeta, snapshotFound, err := store.ReadSessionSnapshot(
		t.Context(), "client",
	); err != nil || !snapshotFound || snapshotKey != "client" || len(snapshotHistory) != 1 ||
		snapshotMeta.Revision == "" {
		t.Fatalf(
			"client snapshot = %q %#v %#v %t %v",
			snapshotKey, snapshotHistory, snapshotMeta, snapshotFound, err,
		)
	}
	if err := store.ReplaceSessionSnapshot(t.Context(), SessionSnapshotReplacement{
		Key: "replacement", History: []providers.Message{{Role: "user", Content: "replacement"}},
		Summary: "replacement", Scope: scope, Aliases: []string{"replacement-alias"},
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.EnsureSessionHistory(t.Context(), "empty-client"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.UpdateSessionMetaStrict(
		t.Context(), "client", func(meta *SessionMeta, state SessionMetaMutationState) error {
			if !state.SessionExists || !state.MetadataExists {
				t.Fatalf("client mutation state = %#v", state)
			}
			meta.Summary = "updated"
			return nil
		},
	); err != nil {
		t.Fatal(err)
	}
	if err := store.UpdateSessionMeta(t.Context(), "client", func(meta *SessionMeta) error {
		meta.Summary = "updated-again"
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if updated, err := store.AdmitSessionMeta(
		t.Context(), "admitted-client",
		func(meta SessionMeta, exists bool) (SessionMetaAdmissionDecision, error) {
			if exists || meta.Key != "admitted-client" {
				t.Fatalf("new admission = exists:%t meta:%#v", exists, meta)
			}
			return SessionMetaAdmissionDecision{Update: true, Scope: scope, Aliases: []string{"admitted"}}, nil
		},
	); err != nil || !updated {
		t.Fatalf("client admission = %t, %v", updated, err)
	}
	current, err := store.GetSessionMeta(t.Context(), "empty-client")
	if err != nil {
		t.Fatal(err)
	}
	replacement := cloneSessionMeta(current)
	replacement.Summary = "cas"
	if changed, err := store.CompareAndSwapSessionMetaStrict(
		t.Context(), "empty-client", current, &replacement,
	); err != nil || !changed {
		t.Fatalf("client CAS = %t, %v", changed, err)
	}
	current, err = store.GetSessionMeta(t.Context(), "empty-client")
	if err != nil {
		t.Fatal(err)
	}
	empty := cloneSessionMeta(current)
	empty.Summary = ""
	if changed, err := store.CompareAndSwapSessionMetaStrict(
		t.Context(), "empty-client", current, &empty,
	); err != nil || !changed {
		t.Fatalf("client clear CAS = %t, %v", changed, err)
	}
	empty, err = store.GetSessionMeta(t.Context(), "empty-client")
	if err != nil {
		t.Fatal(err)
	}
	if changed, err := store.CompareAndDeleteEmptySessionStrict(
		t.Context(), "empty-client", empty,
	); err != nil || !changed {
		t.Fatalf("client compare-delete = %t, %v", changed, err)
	}
	if deleted, err := store.DeleteSession(t.Context(), "replacement"); err != nil || !deleted {
		t.Fatalf("client delete = %t, %v", deleted, err)
	}
	if deleted, err := store.DeleteSessions(t.Context(), []string{"admitted-client"}); err != nil || !deleted {
		t.Fatalf("client grouped delete = %t, %v", deleted, err)
	}
	if err := store.EnsureSessionHistory(t.Context(), "matched-client"); err != nil {
		t.Fatal(err)
	}
	if err := store.UpsertSessionMeta(
		t.Context(), "matched-client", scope, []string{"matched-alias"},
	); err != nil {
		t.Fatal(err)
	}
	if deleted, err := store.DeleteSessionsWithAliasesMatching(
		t.Context(), []string{"matched-alias"},
		func(SessionMeta, bool) bool { return false },
		func(SessionMeta, string) bool { return false },
	); err != nil || deleted {
		t.Fatalf("nonmatching client delete = %t, %v", deleted, err)
	}
	if deleted, err := store.DeleteSessionsWithAliasesMatching(
		t.Context(), []string{"matched-alias"},
		func(meta SessionMeta, exists bool) bool { return exists && meta.Key == "matched-client" },
		func(SessionMeta, string) bool { return true },
	); err != nil || !deleted {
		t.Fatalf("matching client delete = %t, %v", deleted, err)
	}
	if len(store.ListSessions()) == 0 {
		t.Fatal("client session list is empty")
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
}

func sessionBrokerCall[T any](
	t *testing.T,
	adapter *BrokerAdapter,
	operation string,
	input any,
) T {
	t.Helper()
	result, err := sessionBrokerHandle(t.Context(), adapter, operation, input)
	if err != nil {
		t.Fatalf("session broker %s: %v", operation, err)
	}
	response, ok := result.(T)
	if !ok {
		t.Fatalf("session broker %s response = %T, want typed response", operation, result)
	}
	return response
}

func sessionBrokerHandle(
	ctx context.Context,
	adapter *BrokerAdapter,
	operation string,
	input any,
) (any, error) {
	payload, err := database.MarshalCanonical(input)
	if err != nil {
		return nil, err
	}
	return adapter.Handle(ctx, database.Request{
		Domain: SessionsBrokerDomain, Version: SessionsBrokerVersion,
		Operation: operation, Payload: payload,
	})
}
