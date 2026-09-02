package accountrouter

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/database"
	"github.com/sipeed/picoclaw/pkg/providers"
)

func TestAccountRouterBrokerFailClosedContracts(t *testing.T) {
	sentinel := database.NewError(database.CodeUnavailable, "broker unavailable")
	if err := (*Router)(nil).brokerCall("read", nil, nil, false); database.CodeOf(err) != database.CodeUnavailable {
		t.Fatalf("nil router broker call = %v", err)
	}
	failed := &Router{brokerErr: sentinel}
	if err := failed.brokerCall("read", nil, nil, false); !errors.Is(err, sentinel) {
		t.Fatalf("failed router broker call = %v", err)
	}
	invalid := &Router{broker: &database.Client{}, storeID: "bad id"}
	if err := invalid.brokerCall("read", nil, nil, false); database.CodeOf(err) != database.CodeInvalid {
		t.Fatalf("invalid StoreID broker call = %v", err)
	}
	if selection := failed.brokerSelect("session", SelectReasonInitial); len(selection.Candidates) != 0 {
		t.Fatalf("failed selection = %#v", selection)
	}
	failed.brokerRecordFallbackResult(Selection{}, nil, sentinel, false)
	if _, found := failed.brokerAccountStateSnapshot("account"); found {
		t.Fatal("failed broker returned account state")
	}

	previous := database.RuntimeClient()
	database.InstallProcessClient(nil)
	t.Cleanup(func() { database.InstallProcessClient(previous) })
	if _, err := SessionKeysForStore(
		AccountRoutingStoreID,
		"router",
	); database.CodeOf(
		err,
	) != database.CodeUnavailable {
		t.Fatalf("unavailable session keys = %v", err)
	}
	if err := InvalidateCredentialAuthFailureForStore(
		AccountRoutingStoreID,
		"credential",
	); database.CodeOf(
		err,
	) != database.CodeUnavailable {
		t.Fatalf("unavailable invalidation = %v", err)
	}
	if (&Router{}).StoreID() != "" || (*Router)(nil).StoreID() != "" {
		t.Fatal("local or nil router exposed StoreID")
	}
	if _, _, err := (*Store)(nil).open(t.Context()); err == nil {
		t.Fatal("nil store opened")
	}
	if err := (*Store)(nil).retain(); err == nil {
		t.Fatal("nil store retained")
	}
	if err := (*Store)(nil).closeRetained(); err != nil {
		t.Fatalf("nil retained close = %v", err)
	}
	if err := (*Store)(nil).refresh(); err == nil {
		t.Fatal("nil store refreshed")
	}
	if err := (*Store)(nil).update(func(*State) {}); err != nil {
		t.Fatalf("nil store update = %v", err)
	}
	badStore := &Store{initErr: sentinel}
	if err := badStore.update(func(*State) {}); !errors.Is(err, sentinel) {
		t.Fatalf("store init error = %v", err)
	}
	if err := badStore.retain(); err == nil {
		t.Fatal("pathless store retained")
	}
	if err := badStore.refresh(); err == nil {
		t.Fatal("pathless store refreshed")
	}
	if err := invalidateCredentialAuthFailureStore(nil, "credential:test"); err == nil {
		t.Fatal("nil store credential invalidation succeeded")
	}
	if err := invalidateCredentialAuthFailureStore(&Store{}, "bad"); err == nil {
		t.Fatal("invalid credential ID was accepted")
	}
	selection := Selection{
		RouterName: "router", SessionKey: "session", Reason: SelectReasonInitial,
		Candidates:                         []providers.FallbackCandidate{{Provider: "openai", Model: "model"}},
		CandidateAccounts:                  map[string]string{"candidate": "account"},
		ProviderAccounts:                   map[string]string{"provider": "account"},
		BlockAccountChoices:                map[string]string{"block": "account"},
		accountAuthInvalidationGenerations: map[string]string{"account": "generation"},
	}
	result := &providers.FallbackResult{
		Response: &providers.LLMResponse{Usage: &providers.UsageInfo{TotalTokens: 7}},
		Provider: "openai", Model: "model", IdentityKey: "identity",
		Attempts: []providers.FallbackAttempt{{
			Provider: "openai", Model: "model", IdentityKey: "identity",
			Error: errors.New("private detail"), Reason: providers.FailoverAuth,
		}},
	}
	wire := resultToWire(selection, result, errors.New("request failed"), true)
	if !wire.Present || !wire.HasResponse || wire.Usage == result.Response.Usage ||
		wire.Error != errPrivateProviderRequest.Error() || wire.Attempts[0].Error != wire.Error {
		t.Fatalf("private result wire = %#v", wire)
	}
	decoded, decodedErr := resultFromWire(wire)
	if decoded == nil || decoded.Response == nil || decoded.Response.Usage == nil || decodedErr == nil ||
		decoded.Attempts[0].Error == nil {
		t.Fatalf("decoded result = %#v, %v", decoded, decodedErr)
	}
	classified := resultToWire(selection, nil, errors.New("rate limit"), false)
	if classified.Error == "" {
		t.Fatal("nil result error was not encoded")
	}
	safetyFiltered := resultToWire(
		selection, nil, errors.New("request rejected by the content safety filter"), false,
	)
	if safetyFiltered.Present {
		t.Fatalf("safety-filter-only failure became a router attempt: %#v", safetyFiltered)
	}
	if empty, emptyErr := resultFromWire(accountRouterResultWire{}); empty != nil || emptyErr != nil {
		t.Fatalf("empty wire = %#v, %v", empty, emptyErr)
	}
	converted := selectionFromWire(selectionToWire(selection))
	converted.CandidateAccounts["candidate"] = "changed"
	if selection.CandidateAccounts["candidate"] != "account" {
		t.Fatal("selection wire maps alias source state")
	}
}

func TestAccountRouterBrokerHandlerValidationAndLifecycle(t *testing.T) {
	if _, err := NewBrokerHandler(t.TempDir(), nil); database.CodeOf(err) != database.CodeInvalid {
		t.Fatalf("nil broker config = %v", err)
	}
	restoreAuthority := database.SuspendProviderTestAuthority()
	allowUnfencedAccountRouterProviderForTests.Store(false)
	_, unauthorizedErr := NewBrokerHandler(t.TempDir(), config.DefaultConfig())
	allowUnfencedAccountRouterProviderForTests.Store(true)
	restoreAuthority()
	if database.CodeOf(unauthorizedErr) != database.CodeUnauthorized {
		t.Fatalf("unfenced broker handler = %v", unauthorizedErr)
	}

	fixture := newAccountRouterBrokerFixture(t)
	handler := fixture.handler
	sentinel := errors.New("store unavailable")
	if _, err := (*BrokerHandler)(
		nil,
	).Handle(t.Context(), database.Request{}); database.CodeOf(
		err,
	) != database.CodeUnsupported {
		t.Fatalf("nil handler request = %v", err)
	}
	if _, err := handler.Handle(
		t.Context(),
		database.Request{Domain: "wrong"},
	); database.CodeOf(
		err,
	) != database.CodeUnsupported {
		t.Fatalf("wrong domain request = %v", err)
	}
	if err := handler.validateStoreID("bad id"); database.CodeOf(err) != database.CodeUnauthorized {
		t.Fatalf("invalid store = %v", err)
	}
	if _, err := handler.localRouter(accountRouterSpec{}); database.CodeOf(err) != database.CodeInvalid {
		t.Fatalf("invalid router spec = %v", err)
	}
	validSpec := accountRouterSpec{
		Name: "router", Config: *testAccountRouterConfig(), Accounts: testAccountRouterAccounts(),
	}
	handler.store.initErr = sentinel
	if _, err := handler.Handle(t.Context(), accountRouterRequest(
		t, accountRouterOperationSelect,
		accountRouterSelectRequest{
			StoreID: AccountRoutingStoreID, Router: validSpec,
			SessionKey: "session", Reason: SelectReasonInitial,
		},
	)); database.CodeOf(err) != database.CodeInternal {
		t.Fatalf("failed local selection = %v", err)
	}
	handler.store.initErr = nil
	for _, operation := range []string{
		accountRouterOperationSelect,
		accountRouterOperationRecord,
		accountRouterOperationSessionKeys,
		accountRouterOperationAccount,
		accountRouterOperationInvalidate,
	} {
		if _, err := handler.Handle(t.Context(), database.Request{
			Domain: BrokerDomain, Version: BrokerVersion, Operation: operation,
			Payload: json.RawMessage(`{`),
		}); database.CodeOf(err) != database.CodeInvalid {
			t.Errorf("%s malformed payload = %v", operation, err)
		}
	}

	for _, request := range []database.Request{
		accountRouterRequest(t, accountRouterOperationSelect, map[string]any{}),
		accountRouterRequest(t, accountRouterOperationRecord, map[string]any{}),
		accountRouterRequest(t, accountRouterOperationSessionKeys, map[string]any{}),
		accountRouterRequest(t, accountRouterOperationAccount, map[string]any{}),
		accountRouterRequest(t, accountRouterOperationInvalidate, map[string]any{}),
		accountRouterRequest(t, "unknown", map[string]any{}),
	} {
		if _, err := handler.Handle(t.Context(), request); err == nil {
			t.Errorf("%s accepted invalid request", request.Operation)
		}
	}
	for _, request := range []database.Request{
		accountRouterRequest(t, accountRouterOperationSelect, accountRouterSelectRequest{
			StoreID: AccountRoutingStoreID, Router: accountRouterSpec{},
		}),
		accountRouterRequest(t, accountRouterOperationRecord, accountRouterRecordRequest{
			StoreID: AccountRoutingStoreID, Router: accountRouterSpec{},
		}),
		accountRouterRequest(t, accountRouterOperationAccount, accountRouterAccountRequest{
			StoreID: AccountRoutingStoreID, Router: accountRouterSpec{},
		}),
	} {
		if _, err := handler.Handle(t.Context(), request); database.CodeOf(err) != database.CodeInvalid {
			t.Errorf("%s invalid router = %v", request.Operation, err)
		}
	}
	if _, err := handler.Handle(t.Context(), accountRouterRequest(
		t, accountRouterOperationSessionKeys,
		accountRouterNamedRequest{StoreID: AccountRoutingStoreID, RouterName: "missing"},
	)); database.CodeOf(err) != database.CodeInternal {
		t.Fatalf("missing router session keys = %v", err)
	}
	if _, err := handler.Handle(t.Context(), accountRouterRequest(
		t, accountRouterOperationInvalidate,
		accountRouterInvalidationRequest{StoreID: AccountRoutingStoreID},
	)); database.CodeOf(err) != database.CodeInternal {
		t.Fatalf("invalid credential invalidation = %v", err)
	}

	if mapAccountRouterBrokerError(nil) != nil {
		t.Fatal("nil broker error mapping changed nil")
	}
	if database.CodeOf(mapAccountRouterBrokerError(context.Canceled)) != database.CodeDeadline ||
		database.CodeOf(mapAccountRouterBrokerError(errors.New("opaque"))) != database.CodeInternal {
		t.Fatal("broker error mapping changed")
	}
	if err := (*BrokerHandler)(nil).Close(); err != nil {
		t.Fatalf("nil handler close = %v", err)
	}
	closed := &BrokerHandler{closed: true}
	if _, err := closed.Handle(t.Context(), accountRouterRequest(
		t, accountRouterOperationSessionKeys,
		accountRouterNamedRequest{StoreID: AccountRoutingStoreID},
	)); database.CodeOf(err) != database.CodeUnavailable {
		t.Fatalf("closed handler request = %v", err)
	}
	if err := closed.Close(); err != nil {
		t.Fatalf("repeated closed handler close = %v", err)
	}

	previous := database.RuntimeClient()
	database.InstallProcessClient(fixture.client)
	t.Cleanup(func() { database.InstallProcessClient(previous) })
	keys, err := SessionKeysForStore(AccountRoutingStoreID, "missing")
	if database.CodeOf(err) != database.CodeInternal || keys != nil {
		t.Fatalf("broker session key facade = %#v, %v", keys, err)
	}
	if err := InvalidateCredentialAuthFailureForStore(
		AccountRoutingStoreID,
		"",
	); database.CodeOf(
		err,
	) != database.CodeInternal {
		t.Fatalf("broker invalidation facade = %v", err)
	}
	if _, err := SessionKeys("ignored", "missing"); database.CodeOf(err) != database.CodeInternal {
		t.Fatalf("runtime SessionKeys = %v", err)
	}
	if err := InvalidateCredentialAuthFailure("ignored", ""); database.CodeOf(err) != database.CodeInternal {
		t.Fatalf("runtime invalidation = %v", err)
	}
	if err := InvalidateCredentialAuthFailureForWorkspace(
		"ignored",
		"",
	); database.CodeOf(
		err,
	) != database.CodeInternal {
		t.Fatalf("runtime workspace invalidation = %v", err)
	}
	if _, err := sessionKeysFromStore(nil, "router"); err == nil {
		t.Fatal("nil store session keys succeeded")
	}
}

//nolint:govet // Independent migration and resolver assertions reuse narrow error names.
func TestAccountRouterOfflineMigrationAndResolutionContracts(t *testing.T) {
	workspace := t.TempDir()
	if err := RunOfflineDatabaseMigration(workspace); database.CodeOf(err) != database.CodeConflict {
		t.Fatalf("unfenced migration = %v", err)
	}
	home := t.TempDir()
	fence, err := database.AcquireMigrationFence(home)
	if err != nil {
		t.Fatal(err)
	}
	if err := RunOfflineDatabaseMigration(workspace); err != nil {
		t.Fatal(err)
	}
	if err := fence.Close(); err != nil {
		t.Fatal(err)
	}
	if router := NewForWorkspace(
		"router-local",
		testAccountRouterConfig(),
		testAccountRouterAccounts(),
		workspace,
	); router == nil {
		t.Fatal("local workspace router was not constructed")
	}

	fixture := newAccountRouterBrokerFixture(t)
	previous := database.RuntimeClient()
	database.InstallProcessClient(fixture.client)
	t.Cleanup(func() { database.InstallProcessClient(previous) })
	t.Setenv(config.EnvHome, fixture.home)
	t.Setenv(config.EnvConfig, filepath.Join(fixture.home, "config.json"))
	if storeID, err := resolveAccountRouterBrokerStoreID(); err != nil || storeID != AccountRoutingStoreID {
		t.Fatalf("resolved StoreID = %q, %v", storeID, err)
	}
	malformedConfig := filepath.Join(fixture.home, "malformed.json")
	if err := os.WriteFile(malformedConfig, []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(config.EnvConfig, malformedConfig)
	if _, err := resolveAccountRouterBrokerStoreID(); database.CodeOf(err) != database.CodeUnavailable {
		t.Fatalf("missing broker config = %v", err)
	}
	if _, err := newSQLiteRouter(
		"router", testAccountRouterConfig(), testAccountRouterAccounts(), "",
	); database.CodeOf(err) != database.CodeUnavailable {
		t.Fatalf("runtime constructor config failure = %v", err)
	}

	t.Setenv(config.EnvConfig, filepath.Join(fixture.home, "config.json"))
	if router := NewForWorkspace(
		"router",
		testAccountRouterConfig(),
		testAccountRouterAccounts(),
		workspace,
	); router == nil {
		t.Fatal("runtime workspace router was not constructed")
	}
	fileWorkspace := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(fileWorkspace, []byte("file"), 0o600); err != nil {
		t.Fatal(err)
	}
	secondFence, err := database.AcquireMigrationFence(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := RunOfflineDatabaseMigration(fileWorkspace); err == nil {
		t.Fatal("file workspace migration succeeded")
	}
	if err := secondFence.Close(); err != nil {
		t.Fatal(err)
	}
}

//nolint:govet // Independent construction failures intentionally use local errors.
func TestAccountRouterBrokerConstructionErrorBoundaries(t *testing.T) {
	home := t.TempDir()
	cfg := config.DefaultConfig()
	cfg.Agents.Defaults.Workspace = filepath.Join(home, "workspace")
	configPath := filepath.Join(home, "config.json")
	if err := config.SaveConfig(configPath, cfg); err != nil {
		t.Fatal(err)
	}
	previousClient := database.RuntimeClient()
	database.InstallProcessClient(nil)
	restoreProviderAuthority := database.SuspendProviderTestAuthority()
	allowUnfencedAccountRouterProviderForTests.Store(false)
	_, localErr := newSQLiteRouter(
		"router", testAccountRouterConfig(), testAccountRouterAccounts(), databasePath(cfg.WorkspacePath()),
	)
	_, sessionErr := SessionKeys(databasePath(cfg.WorkspacePath()), "router")
	allowUnfencedAccountRouterProviderForTests.Store(true)
	restoreProviderAuthority()
	database.InstallProcessClient(previousClient)
	if database.CodeOf(localErr) != database.CodeUnauthorized ||
		database.CodeOf(sessionErr) != database.CodeUnavailable {
		t.Fatalf("unfenced local errors = %v, %v", localErr, sessionErr)
	}
	t.Setenv(config.EnvHome, home)
	t.Setenv(config.EnvConfig, "")
	if storeID, err := resolveAccountRouterBrokerStoreID(); err != nil || storeID != AccountRoutingStoreID {
		t.Fatalf("default config resolution = %q, %v", storeID, err)
	}
	originalWorkingDirectory, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	doomed := t.TempDir()
	if err := os.Chdir(doomed); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(doomed); err != nil {
		_ = os.Chdir(originalWorkingDirectory)
		t.Fatal(err)
	}
	t.Setenv(config.EnvHome, "relative-home")
	_, invalidHomeErr := resolveAccountRouterBrokerStoreID()
	if err := os.Chdir(originalWorkingDirectory); err != nil {
		t.Fatal(err)
	}
	if invalidHomeErr == nil {
		t.Fatal("invalid home resolved")
	}
	t.Setenv(config.EnvHome, home)
	restoreAuthority := database.SuspendProviderTestAuthority()
	_, catalogErr := resolveAccountRouterBrokerStoreID()
	restoreAuthority()
	if database.CodeOf(catalogErr) != database.CodeUnavailable {
		t.Fatalf("unfenced catalog resolution = %v", catalogErr)
	}

	workspaceFile := filepath.Join(home, "workspace-file")
	if err := os.WriteFile(workspaceFile, []byte("file"), 0o600); err != nil {
		t.Fatal(err)
	}
	badCfg := config.DefaultConfig()
	badCfg.Agents.Defaults.Workspace = workspaceFile
	if _, err := NewBrokerHandler(home, badCfg); err == nil {
		t.Fatal("file workspace broker was constructed")
	}
	if _, err := NewBrokerHandler("bad\x00home", cfg); err == nil {
		t.Fatal("invalid broker home was accepted")
	}

	retainedCfg := config.DefaultConfig()
	retainedCfg.Agents.Defaults.Workspace = filepath.Join(home, "retained")
	locator := databasePath(retainedCfg.WorkspacePath())
	paths, err := resolveAccountRouterStorePaths(locator)
	if err != nil {
		t.Fatal(err)
	}
	stores.Store(paths.databasePath, &Store{})
	t.Cleanup(func() { stores.Delete(paths.databasePath) })
	if _, err := NewBrokerHandler(home, retainedCfg); err == nil {
		t.Fatal("pathless cached store was retained")
	}
}

func accountRouterRequest(t *testing.T, operation string, payload any) database.Request {
	t.Helper()
	raw, err := database.MarshalCanonical(payload)
	if err != nil {
		t.Fatal(err)
	}
	return database.Request{
		Domain: BrokerDomain, Version: BrokerVersion, Operation: operation,
		Payload: json.RawMessage(raw),
	}
}
