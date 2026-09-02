package accountrouter

import (
	"database/sql"
	"strings"
	"testing"
	"time"

	"github.com/sipeed/picoclaw/pkg/providers"
)

func TestAccountRouterSQLiteSnapshotsExposeDetachedTypedState(t *testing.T) {
	workspace := privateAccountRouterWorkspace(t)
	router, err := newSQLiteRouter(
		"router-main",
		testAccountRouterConfig(),
		testAccountRouterAccounts(),
		databasePath(workspace),
	)
	if err != nil {
		t.Fatal(err)
	}
	for _, session := range []string{"z-session", "a-session"} {
		selection := router.Select(session, SelectReasonInitial)
		if len(selection.Candidates) == 0 {
			t.Fatalf("Select(%q) returned no candidate", session)
		}
	}
	keys, err := SessionKeys(router.store.path, router.Name)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(keys, ",") != "a-session,z-session" {
		t.Fatalf("SessionKeys() = %v", keys)
	}
	keys[0] = "caller-mutated"
	again, err := SessionKeys(router.store.path, router.Name)
	if err != nil || strings.Join(again, ",") != "a-session,z-session" {
		t.Fatalf("detached SessionKeys() = %v, %v", again, err)
	}
	if _, err := SessionKeys(router.store.path, "missing"); err == nil {
		t.Fatal("SessionKeys(missing router) error = nil")
	}
	if _, err := SessionKeys("", router.Name); err == nil {
		t.Fatal("SessionKeys(blank path) error = nil")
	}

	state, ok := router.AccountStateSnapshot("account-a")
	if !ok || state.State == "" {
		t.Fatalf("AccountStateSnapshot() = (%#v, %v)", state, ok)
	}
	state.State = "caller-mutated"
	againState, ok := router.AccountStateSnapshot("account-a")
	if !ok || againState.State == "caller-mutated" {
		t.Fatalf("detached account snapshot = (%#v, %v)", againState, ok)
	}
	if _, ok := router.AccountStateSnapshot("missing"); ok {
		t.Fatal("missing account snapshot was found")
	}
	var nilRouter *Router
	if _, ok := nilRouter.AccountStateSnapshot("account-a"); ok {
		t.Fatal("nil router account snapshot was found")
	}
}

func TestAccountRouterStateValidationBoundaries(t *testing.T) {
	now := time.Date(2026, time.August, 31, 1, 2, 3, 4, time.UTC)
	invalidTime := time.Date(10000, time.January, 1, 0, 0, 0, 0, time.UTC)
	validAccount := func() *AccountState { return &AccountState{State: "operational"} }

	if err := validateAccountRouterState(nil); err == nil {
		t.Fatal("nil state was accepted")
	}
	for name, state := range map[string]*State{
		"nil router": {Routers: map[string]*RouterState{"router": nil}},
		"invalid account": {Routers: map[string]*RouterState{"router": {
			Accounts: map[string]*AccountState{"account": nil},
		}}},
		"invalid session": {Routers: map[string]*RouterState{"router": {
			Sessions: map[string]*SessionState{"session": nil},
		}}},
		"invalid affinity": {Routers: map[string]*RouterState{"router": {
			Sessions: map[string]*SessionState{"session": {
				UpdatedAt: now,
				Blocks:    map[string]BlockAffinity{"block": {}},
			}},
		}}},
		"invalid block": {Routers: map[string]*RouterState{"router": {
			Blocks: map[string]*BlockRunState{"block": nil},
		}}},
	} {
		t.Run(name, func(t *testing.T) {
			if err := validateAccountRouterState(state); err == nil {
				t.Fatalf("invalid state accepted: %#v", state)
			}
		})
	}

	for name, account := range map[string]*AccountState{
		"nil":         nil,
		"health":      {State: "broken"},
		"reason":      {State: "operational", Reason: providers.FailoverReason("private")},
		"counter":     {State: "operational", Requests: -1},
		"diagnostics": {State: "operational", LastError: "bad\x00error"},
		"timestamp":   {State: "operational", LastSuccessAt: invalidTime},
	} {
		t.Run("account "+name, func(t *testing.T) {
			if err := validateAccountRouterAccountState("account", account); err == nil {
				t.Fatalf("invalid account accepted: %#v", account)
			}
		})
	}
	if err := validateAccountRouterAccountState("account", validAccount()); err != nil {
		t.Fatalf("valid account rejected: %v", err)
	}
	if err := validateAccountRouterSessionState("session", &SessionState{}); err == nil {
		t.Fatal("zero-time session was accepted")
	}
	if err := validateAccountRouterAffinity("block", BlockAffinity{}); err == nil {
		t.Fatal("empty affinity was accepted")
	}
	if validAccountRouterFailureReason(providers.FailoverReason("private")) {
		t.Fatal("private failure reason was accepted")
	}
	if err := accountRouterValidateTime(time.Time{}, false); err == nil {
		t.Fatal("required zero timestamp was accepted")
	}
	if err := accountRouterValidateTime(invalidTime, true); err == nil {
		t.Fatal("out-of-range timestamp was accepted")
	}
	if _, _, err := accountRouterRequiredTimeValues(time.Time{}); err == nil {
		t.Fatal("required timestamp conversion accepted zero")
	}
	if _, err := accountRouterScannedTime(sql.NullInt64{Valid: true}, sql.NullInt64{}); err == nil {
		t.Fatal("inconsistent timestamp columns were accepted")
	}
}

func TestAccountRouterLegacyDecodersRejectAmbiguityAndNormalizeSparseState(t *testing.T) {
	for name, raw := range map[string]string{
		"empty":              "",
		"not object":         "[]",
		"bad version":        `{"version":"one"}`,
		"routers not object": `{"routers":[]}`,
		"trailing root":      `{"routers":{}} {}`,
		"trailing routers":   `{"routers": {"router":{}} true}`,
		"malformed router":   `{"routers":{"router":`,
	} {
		t.Run(name, func(t *testing.T) {
			if _, _, err := decodeLegacyAccountRouterEntries([]byte(raw)); err == nil {
				t.Fatalf("legacy decoder accepted %q", raw)
			}
		})
	}
	version, entries, err := decodeLegacyAccountRouterEntries(
		[]byte(`{"version":1,"version":2,"routers":{"b":{},"a":{}}}`),
	)
	if err != nil || version != 1 || len(entries) != 2 {
		t.Fatalf("legacy entries = version:%d entries:%#v error:%v", version, entries, err)
	}

	router := &RouterState{
		Accounts: map[string]*AccountState{"account": {}},
		Sessions: map[string]*SessionState{"session": {}},
	}
	normalizeLegacyRouterState(router)
	if router.Accounts["account"].State != "operational" ||
		router.Sessions["session"].Blocks == nil || router.Blocks == nil {
		t.Fatalf("normalized sparse router = %#v", router)
	}
	empty := &RouterState{}
	normalizeLegacyRouterState(empty)
	if empty.Accounts == nil || empty.Sessions == nil || empty.Blocks == nil {
		t.Fatalf("normalized empty router = %#v", empty)
	}

	for name, raw := range map[string]string{
		"malformed":          "{",
		"trailing":           `{"version":1,"credential_id":"openai:work","generation":"g"}{}`,
		"wrong version":      `{"version":2,"credential_id":"openai:work","generation":"g"}`,
		"missing id":         `{"version":1,"generation":"g"}`,
		"missing generation": `{"version":1,"credential_id":"openai:work"}`,
	} {
		t.Run("invalidation "+name, func(t *testing.T) {
			if marker, ok := decodeLegacyAccountRouterInvalidation([]byte(raw)); ok {
				t.Fatalf("invalid marker accepted: %#v", marker)
			}
		})
	}
}
