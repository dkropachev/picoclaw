package memory

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/sipeed/picoclaw/pkg/providers"
)

//nolint:govet // Independent storage assertions intentionally use narrow error scopes.
func TestSQLiteStoreMetadataCompareAndDeleteCompatibilityBoundaries(t *testing.T) {
	_, dir := privateSessionsFixture(t)
	store, err := NewSQLiteStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()

	if changed, err := store.CompareAndSwapSessionMetaStrict(
		ctx, "missing", SessionMeta{}, nil,
	); err != nil || changed {
		t.Fatalf("missing compare-and-swap = (%v, %v)", changed, err)
	}
	if deleted, err := store.CompareAndDeleteEmptySessionStrict(
		ctx, "missing", SessionMeta{},
	); err != nil || deleted {
		t.Fatalf("missing compare-and-delete = (%v, %v)", deleted, err)
	}

	if err := store.EnsureSessionHistory(ctx, "replace"); err != nil {
		t.Fatal(err)
	}
	current, err := store.GetSessionMeta(ctx, "replace")
	if err != nil || current.Key != "replace" {
		t.Fatalf("GetSessionMeta(replace) = (%#v, %v)", current, err)
	}
	stale := cloneSessionMeta(current)
	stale.Summary = "stale"
	if changed, err := store.CompareAndSwapSessionMetaStrict(ctx, "replace", stale, &current); err != nil || changed {
		t.Fatalf("stale compare-and-swap = (%v, %v)", changed, err)
	}
	replacement := cloneSessionMeta(current)
	replacement.Summary = "summary"
	replacement.Aliases = []string{"legacy-replace"}
	if changed, err := store.CompareAndSwapSessionMetaStrict(
		ctx,
		"replace",
		current,
		&replacement,
	); err != nil || !changed {
		t.Fatalf("replacement compare-and-swap = (%v, %v)", changed, err)
	}
	updated, err := store.GetSessionMeta(ctx, "legacy-replace")
	if err != nil || updated.Summary != "summary" || updated.Key != "replace" {
		t.Fatalf("updated metadata = (%#v, %v)", updated, err)
	}
	if changed, err := store.CompareAndSwapSessionMetaStrict(ctx, "replace", updated, nil); err == nil || changed {
		t.Fatalf("nonempty metadata removal = (%v, %v)", changed, err)
	}

	if err := store.EnsureSessionHistory(ctx, "cas-delete"); err != nil {
		t.Fatal(err)
	}
	empty, err := store.GetSessionMeta(ctx, "cas-delete")
	if err != nil || empty.Key != "cas-delete" {
		t.Fatal(err)
	}
	if changed, err := store.CompareAndSwapSessionMetaStrict(ctx, "cas-delete", empty, nil); err != nil || !changed {
		t.Fatalf("empty metadata removal = (%v, %v)", changed, err)
	}

	if err := store.EnsureSessionHistory(ctx, "compare-delete"); err != nil {
		t.Fatal(err)
	}
	empty, _ = store.GetSessionMeta(ctx, "compare-delete")
	stale = cloneSessionMeta(empty)
	stale.Summary = "different"
	if deleted, err := store.CompareAndDeleteEmptySessionStrict(ctx, "compare-delete", stale); err != nil || deleted {
		t.Fatalf("stale empty delete = (%v, %v)", deleted, err)
	}
	if deleted, err := store.CompareAndDeleteEmptySessionStrict(ctx, "compare-delete", empty); err != nil || !deleted {
		t.Fatalf("exact empty delete = (%v, %v)", deleted, err)
	}

	if err := store.AddFullMessage(ctx, "nonempty", providers.Message{Role: "user", Content: "keep"}); err != nil {
		t.Fatal(err)
	}
	nonempty, _ := store.GetSessionMeta(ctx, "nonempty")
	if deleted, err := store.CompareAndDeleteEmptySessionStrict(ctx, "nonempty", nonempty); err == nil || deleted {
		t.Fatalf("nonempty history delete = (%v, %v)", deleted, err)
	}
}

//nolint:govet // Independent storage assertions intentionally use narrow error scopes.
func TestSQLiteStoreStrictMetadataMutationCreationRollbackAndFieldOwnership(t *testing.T) {
	_, dir := privateSessionsFixture(t)
	store, err := NewSQLiteStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	if _, _, err := store.UpdateSessionMetaStrict(ctx, " ", func(*SessionMeta, SessionMetaMutationState) error {
		return nil
	}); err == nil {
		t.Fatal("blank strict metadata key was accepted")
	}
	if _, _, err := store.UpdateSessionMetaStrict(ctx, "key", nil); err == nil {
		t.Fatal("nil strict metadata callback was accepted")
	}
	injected := errors.New("rollback")
	if _, _, err := store.UpdateSessionMetaStrict(ctx, "new", func(*SessionMeta, SessionMetaMutationState) error {
		return injected
	}); !errors.Is(err, injected) {
		t.Fatalf("callback rollback error = %v", err)
	}
	if meta, err := store.GetSessionMeta(ctx, "new"); err != nil ||
		meta.Count != 0 || !meta.CreatedAt.IsZero() {
		t.Fatalf("rolled-back projection = (%#v, %v)", meta, err)
	}
	canonical, existed, err := store.UpdateSessionMetaStrict(
		ctx,
		"new",
		func(meta *SessionMeta, state SessionMetaMutationState) error {
			if state.SessionExists || state.MetadataExists {
				t.Fatalf("new mutation state = %#v", state)
			}
			meta.Summary = "created"
			meta.Scope = json.RawMessage(`{"channel":"pico"}`)
			meta.Aliases = []string{"legacy-new"}
			return nil
		},
	)
	if err != nil || existed || canonical != "new" {
		t.Fatalf("new strict update = (%q, %v, %v)", canonical, existed, err)
	}
	canonical, existed, err = store.UpdateSessionMetaStrict(
		ctx,
		"legacy-new",
		func(meta *SessionMeta, state SessionMetaMutationState) error {
			if !state.SessionExists || !state.MetadataExists || meta.Key != "new" {
				t.Fatalf("existing mutation state = %#v meta=%#v", state, meta)
			}
			meta.Summary = "updated"
			return nil
		},
	)
	if err != nil || !existed || canonical != "new" {
		t.Fatalf("alias strict update = (%q, %v, %v)", canonical, existed, err)
	}
	if _, _, err := store.UpdateSessionMetaStrict(
		ctx, "new", func(meta *SessionMeta, _ SessionMetaMutationState) error {
			meta.Key = "changed"
			return nil
		},
	); err == nil {
		t.Fatal("canonical-key mutation was accepted")
	}
	if _, _, err := store.UpdateSessionMetaStrict(
		ctx, "new", func(meta *SessionMeta, _ SessionMetaMutationState) error {
			meta.Count++
			return nil
		},
	); err == nil {
		t.Fatal("history-field mutation was accepted")
	}
	if err := store.UpdateSessionMeta(ctx, "new", func(meta *SessionMeta) error {
		meta.Summary = "facade"
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if meta, err := store.GetSessionMeta(ctx, "new"); err != nil || meta.Summary != "facade" {
		t.Fatalf("facade metadata = (%#v, %v)", meta, err)
	}
}

func TestSQLiteStoreGroupedDeletionAndMatchingCallbacks(t *testing.T) {
	_, dir := privateSessionsFixture(t)
	store, err := NewSQLiteStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()

	for _, keys := range [][]string{nil, {}, {""}, {"  "}} {
		if deleted, err := store.DeleteSessions(ctx, keys); err == nil || deleted {
			t.Fatalf("DeleteSessions(%q) = (%v, %v)", keys, deleted, err)
		}
	}
	if deleted, err := store.DeleteSession(ctx, "missing"); err != nil || deleted {
		t.Fatalf("DeleteSession(missing) = (%v, %v)", deleted, err)
	}

	for _, key := range []string{"one", "two", "three"} {
		if err := store.EnsureSessionHistory(ctx, key); err != nil {
			t.Fatal(err)
		}
	}
	meta, _ := store.GetSessionMeta(ctx, "one")
	if changed, err := store.CompareAndSwapSessionMetaStrict(
		ctx, "one", meta, &meta,
	); err != nil || !changed {
		// The equal replacement exercises the versionless compatibility boundary;
		// install the alias through the strict metadata mutator below.
		t.Fatalf("identity compare-and-swap = (%v, %v)", changed, err)
	}
	if _, _, err := store.UpdateSessionMetaStrict(
		ctx, "one", func(meta *SessionMeta, _ SessionMetaMutationState) error {
			meta.Aliases = []string{"legacy-one"}
			return nil
		},
	); err != nil {
		t.Fatal(err)
	}
	if deleted, err := store.DeleteSessions(
		ctx, []string{"legacy-one", "one", "legacy-one", "missing"},
	); err != nil || !deleted {
		t.Fatalf("alias grouped delete = (%v, %v)", deleted, err)
	}
	if meta, err := store.GetSessionMeta(ctx, "one"); err != nil ||
		meta.Count != 0 || meta.Summary != "" {
		t.Fatalf("deleted owner projection = (%#v, err=%v)", meta, err)
	}

	if deleted, err := store.DeleteSessionsWithAliasesMatching(
		ctx,
		[]string{"two"},
		func(meta SessionMeta, exists bool) bool { return exists && meta.Key == "not-two" },
		nil,
	); err != nil || deleted {
		t.Fatalf("nonmatching callback delete = (%v, %v)", deleted, err)
	}
	if _, _, err := store.UpdateSessionMetaStrict(
		ctx, "two", func(meta *SessionMeta, _ SessionMetaMutationState) error {
			meta.Aliases = []string{"legacy-two"}
			return nil
		},
	); err != nil {
		t.Fatal(err)
	}
	aliasCalls := 0
	if deleted, err := store.DeleteSessionsWithAliasesMatching(
		ctx,
		[]string{"legacy-two", "missing"},
		func(meta SessionMeta, exists bool) bool { return exists && meta.Key == "two" },
		func(meta SessionMeta, alias string) bool {
			aliasCalls++
			return meta.Key == "two" && alias == "legacy-two"
		},
	); err != nil || !deleted || aliasCalls != 1 {
		t.Fatalf("matching callback delete = (%v, %v, calls=%d)", deleted, err, aliasCalls)
	}
	if deleted, err := store.DeleteSessionsWithAliasesMatching(
		ctx,
		[]string{"three"},
		nil,
		nil,
	); err != nil || !deleted {
		t.Fatalf("nil matcher delete = (%v, %v)", deleted, err)
	}
}
