package memory

import (
	"context"
	"errors"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sipeed/picoclaw/pkg/database"
	"github.com/sipeed/picoclaw/pkg/providers"
)

func TestSessionBrokerClientRejectsMalformedResponsesAndReplayConflicts(t *testing.T) {
	home := t.TempDir()
	handler := database.HandlerFunc(func(_ context.Context, request database.Request) (any, error) {
		switch request.Operation {
		case sessionOperationPing, sessionOperationAdd, sessionOperationSetSummary,
			sessionOperationSetHistory, sessionOperationTruncate, sessionOperationReplaceSnapshot:
			return sessionBrokerResponse{OK: false}, nil
		case sessionOperationHistory:
			return sessionBrokerResponse{
				History: []providers.Message{{Role: "user", Content: "one"}}, Next: "invalid",
			}, nil
		case sessionOperationList:
			return sessionBrokerResponse{Sessions: []string{" invalid "}}, nil
		case sessionOperationReadState:
			return sessionBrokerResponse{
				CanonicalKey: "key", Found: true, Revision: "revision",
				History: []providers.Message{{Role: "user", Content: "one"}}, Next: "invalid",
			}, nil
		case sessionOperationReadMutationMeta:
			return sessionBrokerResponse{
				CanonicalKey: "key", Found: true, Revision: "revision",
				Meta:  SessionMeta{Key: "key"},
				State: SessionMetaMutationState{SessionExists: true, MetadataExists: true},
			}, nil
		case sessionOperationApplyMeta, sessionOperationApplyAdmission:
			return nil, database.NewError(database.CodeConflict, "changed")
		case sessionOperationResolve:
			return sessionBrokerResponse{CanonicalKey: "key", Found: true}, nil
		case sessionOperationGetMeta:
			return sessionBrokerResponse{Meta: SessionMeta{Key: "key"}}, nil
		case sessionOperationGetSummary:
			return sessionBrokerResponse{Summary: "summary"}, nil
		default:
			return sessionBrokerResponse{}, nil
		}
	})
	client := startMemoryCoverageServer(t, home, handler)
	store := &SQLiteStore{brokerClient: client, storeID: SessionsStoreID}
	if err := store.pingBroker(t.Context()); database.CodeOf(err) != database.CodeIntegrity {
		t.Fatalf("malformed ping error = %v", err)
	}
	if err := store.AddFullMessage(
		t.Context(), "key", providers.Message{Role: "user", Content: "one"},
	); database.CodeOf(err) != database.CodeIntegrity {
		t.Fatalf("malformed add error = %v", err)
	}
	if _, err := store.GetHistory(t.Context(), "key"); database.CodeOf(err) != database.CodeIntegrity {
		t.Fatalf("malformed history page error = %v", err)
	}
	if err := store.SetSummary(t.Context(), "key", "summary"); database.CodeOf(err) != database.CodeIntegrity {
		t.Fatalf("malformed summary response error = %v", err)
	}
	if err := store.SetHistory(t.Context(), "key", nil); database.CodeOf(err) != database.CodeIntegrity {
		t.Fatalf("malformed history mutation error = %v", err)
	}
	if err := store.TruncateHistory(t.Context(), "key", 1); database.CodeOf(err) != database.CodeIntegrity {
		t.Fatalf("malformed truncate response error = %v", err)
	}
	if err := store.UpsertSessionMeta(t.Context(), "key", nil, nil); database.CodeOf(err) != database.CodeIntegrity {
		t.Fatalf("malformed metadata response error = %v", err)
	}
	if keys := store.ListSessions(); keys != nil {
		t.Fatalf("malformed session list = %#v", keys)
	}
	if _, _, _, _, _, err := store.ReadSessionStateStrict(
		t.Context(), "key",
	); database.CodeOf(err) != database.CodeIntegrity {
		t.Fatalf("malformed state page error = %v", err)
	}
	if err := store.ReplaceSessionSnapshot(t.Context(), SessionSnapshotReplacement{
		Key: "key", Scope: []byte(`{"channel":"pico"}`),
	}); database.CodeOf(err) != database.CodeIntegrity {
		t.Fatalf("malformed snapshot response error = %v", err)
	}
	if _, _, err := store.UpdateSessionMetaStrict(
		t.Context(), "key", func(meta *SessionMeta, _ SessionMetaMutationState) error {
			meta.Summary = "updated"
			return nil
		},
	); database.CodeOf(err) != database.CodeConflict {
		t.Fatalf("exhausted metadata replay error = %v", err)
	}
	if _, err := store.AdmitSessionMeta(
		t.Context(), "key", func(SessionMeta, bool) (SessionMetaAdmissionDecision, error) {
			return SessionMetaAdmissionDecision{
				Update: true, Scope: []byte(`{"channel":"pico"}`),
			}, nil
		},
	); database.CodeOf(err) != database.CodeConflict {
		t.Fatalf("exhausted admission replay error = %v", err)
	}
}

func TestSessionBrokerClientValidatesPaginationContracts(t *testing.T) {
	t.Run("history pages", func(t *testing.T) {
		var calls atomic.Int64
		store := newMemoryCoverageClientStore(t, database.HandlerFunc(
			func(_ context.Context, request database.Request) (any, error) {
				if request.Operation != sessionOperationHistory {
					return nil, database.NewError(database.CodeUnsupported, "unsupported")
				}
				call := calls.Add(1)
				response := sessionBrokerResponse{
					History: []providers.Message{{Role: "user", Content: strconv.FormatInt(call, 10)}},
				}
				if call == 1 {
					response.Next = "1"
				}
				return response, nil
			},
		))
		history, err := store.GetHistory(t.Context(), "key")
		if err != nil || len(history) != 2 || calls.Load() != 2 {
			t.Fatalf("history pages = %#v calls:%d err:%v", history, calls.Load(), err)
		}
	})
	t.Run("list pages", func(t *testing.T) {
		var calls atomic.Int64
		store := newMemoryCoverageClientStore(t, database.HandlerFunc(
			func(_ context.Context, request database.Request) (any, error) {
				if request.Operation != sessionOperationList {
					return nil, database.NewError(database.CodeUnsupported, "unsupported")
				}
				if calls.Add(1) == 1 {
					return sessionBrokerResponse{Sessions: []string{"a"}, Next: "a"}, nil
				}
				return sessionBrokerResponse{Sessions: []string{"b"}}, nil
			},
		))
		keys := store.ListSessions()
		if len(keys) != 2 || keys[0] != "a" || keys[1] != "b" {
			t.Fatalf("list pages = %#v", keys)
		}
	})
	for name, response := range map[string]sessionBrokerResponse{
		"empty continuation":      {Next: "next"},
		"mismatched continuation": {Sessions: []string{"a"}, Next: "b"},
		"invalid key":             {Sessions: []string{" bad "}},
	} {
		t.Run(name, func(t *testing.T) {
			store := newMemoryCoverageClientStore(t, database.HandlerFunc(
				func(context.Context, database.Request) (any, error) { return response, nil },
			))
			if keys := store.ListSessions(); keys != nil {
				t.Fatalf("invalid list page = %#v", keys)
			}
		})
	}
	t.Run("state pages", func(t *testing.T) {
		var calls atomic.Int64
		store := newMemoryCoverageClientStore(t, database.HandlerFunc(
			func(_ context.Context, request database.Request) (any, error) {
				if request.Operation != sessionOperationReadState {
					return nil, database.NewError(database.CodeUnsupported, "unsupported")
				}
				call := calls.Add(1)
				response := sessionBrokerResponse{
					CanonicalKey: "key", Found: true, Revision: "revision",
					History: []providers.Message{{Role: "user", Content: strconv.FormatInt(call, 10)}},
				}
				if call == 1 {
					response.Next = "1"
				}
				return response, nil
			},
		))
		key, history, meta, _, found, err := store.ReadSessionStateStrict(t.Context(), "key")
		if err != nil || !found || key != "key" || len(history) != 2 || meta.Revision != "revision" {
			t.Fatalf("state pages = %q %#v %#v %t %v", key, history, meta, found, err)
		}
	})
	t.Run("state conflict exhaustion", func(t *testing.T) {
		store := newMemoryCoverageClientStore(t, database.HandlerFunc(
			func(_ context.Context, request database.Request) (any, error) {
				if request.Operation != sessionOperationReadState {
					return nil, database.NewError(database.CodeUnsupported, "unsupported")
				}
				var input sessionReadStateRequest
				if err := request.DecodePayload(&input); err != nil {
					return nil, err
				}
				if input.ExpectedRevision != "" {
					return nil, database.NewError(database.CodeConflict, "changed")
				}
				return sessionBrokerResponse{
					CanonicalKey: "key", Found: true, Revision: "revision", Next: "0",
				}, nil
			},
		))
		if _, _, _, _, _, err := store.ReadSessionStateStrict(
			t.Context(),
			"key",
		); database.CodeOf(
			err,
		) != database.CodeConflict {
			t.Fatalf("state conflict exhaustion error = %v", err)
		}
	})
}

func TestSessionBrokerClientReadStateFacadeBoundaries(t *testing.T) {
	for name, response := range map[string]sessionBrokerResponse{
		"missing": {CanonicalKey: "missing", Found: false, Revision: "revision"},
		"found": {
			CanonicalKey: "key", Found: true, Revision: "revision",
			Meta:       SessionMeta{Key: "key"},
			History:    []providers.Message{{Role: "user", Content: "one"}},
			ModifiedAt: time.Unix(1, 0).UTC(),
		},
	} {
		t.Run(name, func(t *testing.T) {
			store := newMemoryCoverageClientStore(t, database.HandlerFunc(
				func(context.Context, database.Request) (any, error) { return response, nil },
			))
			history, meta, modified, err := store.ReadSessionState(t.Context(), name)
			if err != nil {
				t.Fatal(err)
			}
			if response.Found {
				if len(history) != 1 || meta.Key != "key" || modified.IsZero() {
					t.Fatalf("found state = %#v %#v %v", history, meta, modified)
				}
			} else if len(history) != 0 || meta.Key != name || !modified.IsZero() {
				t.Fatalf("missing state = %#v %#v %v", history, meta, modified)
			}
		})
	}
	store := newMemoryCoverageClientStore(t, database.HandlerFunc(
		func(context.Context, database.Request) (any, error) {
			return nil, database.NewError(database.CodeUnavailable, "unavailable")
		},
	))
	if _, _, _, err := store.ReadSessionState(t.Context(), "key"); database.CodeOf(err) != database.CodeUnavailable {
		t.Fatalf("failed state facade error = %v", err)
	}
}

func TestSessionBrokerClientMutationAndPageErrorBoundaries(t *testing.T) {
	for name, second := range map[string]sessionBrokerResponse{
		"revision":      {CanonicalKey: "key", Found: true, Revision: "other"},
		"canonical key": {CanonicalKey: "other", Found: true, Revision: "revision"},
	} {
		t.Run(name, func(t *testing.T) {
			var calls atomic.Int64
			store := newMemoryCoverageClientStore(t, database.HandlerFunc(
				func(context.Context, database.Request) (any, error) {
					if calls.Add(1) == 1 {
						return sessionBrokerResponse{
							CanonicalKey: "key", Found: true, Revision: "revision", Next: "0",
						}, nil
					}
					return second, nil
				},
			))
			if _, _, _, _, _, err := store.ReadSessionStateStrict(
				t.Context(),
				"key",
			); database.CodeOf(
				err,
			) != database.CodeIntegrity {
				t.Fatalf("mismatched state page error = %v", err)
			}
		})
	}
	store := newMemoryCoverageClientStore(t, database.HandlerFunc(
		func(_ context.Context, request database.Request) (any, error) {
			switch request.Operation {
			case sessionOperationReplaceSnapshot:
				return nil, database.NewError(database.CodeConflict, "changed")
			case sessionOperationReadMutationMeta:
				return sessionBrokerResponse{
					CanonicalKey: "key", Found: true, Revision: "revision", Meta: SessionMeta{Key: "key"},
				}, nil
			case sessionOperationApplyMeta, sessionOperationApplyAdmission:
				return nil, database.NewError(database.CodeUnavailable, "unavailable")
			default:
				return nil, database.NewError(database.CodeUnsupported, "unsupported")
			}
		},
	))
	if err := store.ReplaceSessionSnapshot(
		t.Context(),
		SessionSnapshotReplacement{Key: "key"},
	); !errors.Is(
		err,
		ErrSnapshotConflict,
	) {
		t.Fatalf("snapshot conflict mapping = %v", err)
	}
	want := context.Canceled
	if _, _, err := store.UpdateSessionMetaStrict(
		t.Context(), "key", func(*SessionMeta, SessionMetaMutationState) error { return want },
	); !errors.Is(err, want) {
		t.Fatalf("metadata callback error = %v", err)
	}
	if _, _, err := store.UpdateSessionMetaStrict(
		t.Context(), "key", func(*SessionMeta, SessionMetaMutationState) error { return nil },
	); database.CodeOf(err) != database.CodeUnavailable {
		t.Fatalf("metadata apply error = %v", err)
	}
	if changed, err := store.AdmitSessionMeta(
		t.Context(), "key", func(SessionMeta, bool) (SessionMetaAdmissionDecision, error) {
			return SessionMetaAdmissionDecision{}, nil
		},
	); err != nil || changed {
		t.Fatalf("admission no-op = %t, %v", changed, err)
	}
	if _, err := store.AdmitSessionMeta(
		t.Context(), "key", func(SessionMeta, bool) (SessionMetaAdmissionDecision, error) {
			return SessionMetaAdmissionDecision{}, want
		},
	); !errors.Is(err, want) {
		t.Fatalf("admission callback error = %v", err)
	}
	if _, err := store.AdmitSessionMeta(
		t.Context(), "key", func(SessionMeta, bool) (SessionMetaAdmissionDecision, error) {
			return SessionMetaAdmissionDecision{Update: true, Scope: []byte(`{}`)}, nil
		},
	); database.CodeOf(err) != database.CodeUnavailable {
		t.Fatalf("admission apply error = %v", err)
	}
}

func TestSessionBrokerClientDeleteMatchingFaultBoundaries(t *testing.T) {
	store := &SQLiteStore{}
	if deleted, err := store.DeleteSessionsWithAliasesMatching(t.Context(), nil, nil, nil); err == nil || deleted {
		t.Fatalf("empty matching delete = %t, %v", deleted, err)
	}
	for name, handler := range map[string]database.Handler{
		"resolve error": database.HandlerFunc(func(context.Context, database.Request) (any, error) {
			return nil, database.NewError(database.CodeUnavailable, "unavailable")
		}),
		"not found": database.HandlerFunc(func(context.Context, database.Request) (any, error) {
			return sessionBrokerResponse{Found: false}, nil
		}),
		"metadata error": database.HandlerFunc(func(_ context.Context, request database.Request) (any, error) {
			if request.Operation == sessionOperationResolve {
				return sessionBrokerResponse{Found: true, CanonicalKey: "key"}, nil
			}
			return nil, database.NewError(database.CodeUnavailable, "unavailable")
		}),
		"alias error": database.HandlerFunc(func(_ context.Context, request database.Request) (any, error) {
			switch request.Operation {
			case sessionOperationResolve:
				var input sessionKeyRequest
				if err := request.DecodePayload(&input); err != nil {
					return nil, err
				}
				if input.Key == "alias" {
					return nil, database.NewError(database.CodeUnavailable, "unavailable")
				}
				return sessionBrokerResponse{Found: true, CanonicalKey: "key"}, nil
			case sessionOperationGetMeta:
				return sessionBrokerResponse{Meta: SessionMeta{Key: "key", Aliases: []string{"alias"}}}, nil
			default:
				return nil, database.NewError(database.CodeUnsupported, "unsupported")
			}
		}),
		"delete error": database.HandlerFunc(func(_ context.Context, request database.Request) (any, error) {
			switch request.Operation {
			case sessionOperationResolve:
				return sessionBrokerResponse{Found: true, CanonicalKey: "key"}, nil
			case sessionOperationGetMeta:
				return sessionBrokerResponse{Meta: SessionMeta{Key: "key"}}, nil
			case sessionOperationDelete:
				return nil, database.NewError(database.CodeUnavailable, "unavailable")
			default:
				return nil, database.NewError(database.CodeUnsupported, "unsupported")
			}
		}),
	} {
		t.Run(name, func(t *testing.T) {
			store := newMemoryCoverageClientStore(t, handler)
			deleted, err := store.DeleteSessionsWithAliasesMatching(
				t.Context(), []string{"requested"},
				func(SessionMeta, bool) bool { return name == "delete error" },
				func(SessionMeta, string) bool { return true },
			)
			if name == "not found" {
				if err != nil || deleted {
					t.Fatalf("not-found matching delete = %t, %v", deleted, err)
				}
				return
			}
			if err == nil || deleted {
				t.Fatalf("faulted matching delete = %t, %v", deleted, err)
			}
		})
	}
}

func TestSessionBrokerClientPropagatesTypedOperationFailures(t *testing.T) {
	home := t.TempDir()
	want := database.NewError(database.CodeUnavailable, "broker unavailable")
	client := startMemoryCoverageServer(t, home, database.HandlerFunc(
		func(context.Context, database.Request) (any, error) { return nil, want },
	))
	store := &SQLiteStore{brokerClient: client, storeID: SessionsStoreID}
	operations := []func() error{
		func() error { return store.AddMessage(t.Context(), "key", "user", "one") },
		func() error { _, err := store.GetHistory(t.Context(), "key"); return err },
		func() error { _, err := store.GetSummary(t.Context(), "key"); return err },
		func() error { return store.SetSummary(t.Context(), "key", "summary") },
		func() error { return store.SetHistory(t.Context(), "key", nil) },
		func() error { return store.TruncateHistory(t.Context(), "key", 1) },
		func() error { return store.Compact(t.Context(), "key") },
		func() error { _, _, err := store.ResolveSessionKey(t.Context(), "key"); return err },
		func() error { _, err := store.GetSessionMeta(t.Context(), "key"); return err },
		func() error { return store.UpsertSessionMeta(t.Context(), "key", nil, nil) },
		func() error { _, _, _, _, _, err := store.ReadSessionStateStrict(t.Context(), "key"); return err },
		func() error { return store.ReplaceSessionSnapshot(t.Context(), SessionSnapshotReplacement{Key: "key"}) },
		func() error { _, err := store.PromoteAliasHistory(t.Context(), "key", nil, nil); return err },
		func() error { return store.EnsureSessionHistory(t.Context(), "key") },
		func() error { _, err := store.DeleteSessions(t.Context(), []string{"key"}); return err },
	}
	for index, operation := range operations {
		if err := operation(); database.CodeOf(err) != database.CodeUnavailable {
			t.Errorf("operation %d error = %v", index, err)
		}
	}
	if keys := store.ListSessions(); keys != nil {
		t.Fatalf("failed broker list = %#v", keys)
	}
}

func startMemoryCoverageServer(
	t *testing.T,
	home string,
	handler database.Handler,
) *database.Client {
	t.Helper()
	server, err := database.StartServer(t.Context(), database.ServerOptions{Home: home, Handler: handler})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = server.Close(ctx)
	})
	client, err := database.Connect(home)
	if err != nil {
		t.Fatal(err)
	}
	return client
}

func newMemoryCoverageClientStore(t *testing.T, handler database.Handler) *SQLiteStore {
	t.Helper()
	return &SQLiteStore{
		brokerClient: startMemoryCoverageServer(t, t.TempDir(), handler),
		storeID:      SessionsStoreID,
	}
}
