package accountrouter

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/database"
	dbcatalog "github.com/sipeed/picoclaw/pkg/database/catalog"
	"github.com/sipeed/picoclaw/pkg/providers"
)

const (
	BrokerDomain  = "account-routing"
	BrokerVersion = 1

	AccountRoutingStoreID database.StoreID = "workspace.account-routing"

	accountRouterOperationSelect      = "select"
	accountRouterOperationRecord      = "record-result"
	accountRouterOperationSessionKeys = "session-keys"
	accountRouterOperationAccount     = "account-state"
	accountRouterOperationInvalidate  = "invalidate-credential-auth"
)

type accountRouterSpec struct {
	Name     string                     `json:"name"`
	Config   config.AccountRouterConfig `json:"config"`
	Accounts map[string]Account         `json:"accounts"`
}

type accountRouterSelectRequest struct {
	StoreID    database.StoreID  `json:"store_id"`
	Router     accountRouterSpec `json:"router"`
	SessionKey string            `json:"session_key"`
	Reason     SelectReason      `json:"reason"`
}

type accountRouterSelectionWire struct {
	RouterName          string                        `json:"router_name"`
	SessionKey          string                        `json:"session_key"`
	Reason              SelectReason                  `json:"reason"`
	Candidates          []providers.FallbackCandidate `json:"candidates"`
	CandidateAccounts   map[string]string             `json:"candidate_accounts"`
	ProviderAccounts    map[string]string             `json:"provider_accounts"`
	BlockAccountChoices map[string]string             `json:"block_account_choices"`
	Invalidations       map[string]string             `json:"auth_invalidation_generations"`
}

type accountRouterSelectResponse struct {
	Selection accountRouterSelectionWire `json:"selection"`
}

type accountRouterAttemptWire struct {
	Provider    string                   `json:"provider"`
	Model       string                   `json:"model"`
	IdentityKey string                   `json:"identity_key"`
	Error       string                   `json:"error,omitempty"`
	Reason      providers.FailoverReason `json:"reason"`
	Skipped     bool                     `json:"skipped"`
}

type accountRouterResultWire struct {
	Present     bool                       `json:"present"`
	HasResponse bool                       `json:"has_response"`
	Usage       *providers.UsageInfo       `json:"usage,omitempty"`
	Provider    string                     `json:"provider"`
	Model       string                     `json:"model"`
	IdentityKey string                     `json:"identity_key"`
	Attempts    []accountRouterAttemptWire `json:"attempts"`
	Error       string                     `json:"error,omitempty"`
}

type accountRouterRecordRequest struct {
	StoreID   database.StoreID           `json:"store_id"`
	Router    accountRouterSpec          `json:"router"`
	Selection accountRouterSelectionWire `json:"selection"`
	Result    accountRouterResultWire    `json:"result"`
	Private   bool                       `json:"private"`
}

type accountRouterNamedRequest struct {
	StoreID    database.StoreID `json:"store_id"`
	RouterName string           `json:"router_name"`
}

type accountRouterAccountRequest struct {
	StoreID database.StoreID  `json:"store_id"`
	Router  accountRouterSpec `json:"router"`
	Account string            `json:"account"`
}

type accountRouterInvalidationRequest struct {
	StoreID      database.StoreID `json:"store_id"`
	CredentialID string           `json:"credential_id"`
}

type accountRouterMutationResponse struct {
	Updated bool `json:"updated"`
}

type accountRouterSessionKeysResponse struct {
	Keys []string `json:"keys"`
}

type accountRouterAccountResponse struct {
	State AccountState `json:"state"`
	Found bool         `json:"found"`
}

func resolveAccountRouterBrokerStoreID() (database.StoreID, error) {
	home, err := database.CanonicalHome(config.GetHome())
	if err != nil {
		return "", err
	}
	configPath := strings.TrimSpace(os.Getenv(config.EnvConfig))
	if configPath == "" {
		configPath = filepath.Join(home, "config.json")
	}
	cfg, err := config.LoadConfig(configPath)
	if err != nil {
		return "", database.NewError(database.CodeUnavailable, "account-router broker configuration is unavailable")
	}
	catalog, err := dbcatalog.New(home, cfg)
	if err != nil {
		return "", database.NewError(database.CodeUnavailable, "account-router broker catalog is unavailable")
	}
	return catalog.Lookup(AccountRoutingStoreID.String())
}

func (r *Router) usesAccountRouterBroker() bool {
	return r != nil && (r.broker != nil || r.brokerErr != nil)
}

func (r *Router) accountRouterSpec() accountRouterSpec {
	return accountRouterSpec{Name: r.Name, Config: r.Config, Accounts: r.Accounts}
}

func (r *Router) brokerCall(operation string, request, response any, mutation bool) error {
	if r == nil || r.broker == nil {
		if r != nil && r.brokerErr != nil {
			return r.brokerErr
		}
		return database.NewError(database.CodeUnavailable, "account-router broker client is unavailable")
	}
	if !r.storeID.Valid() {
		return database.NewError(database.CodeInvalid, "account-router store ID is invalid")
	}
	if mutation {
		return r.broker.CallWithOptions(
			context.Background(), BrokerDomain, BrokerVersion, operation, request, response,
			database.CallOptions{Mutation: true},
		)
	}
	return r.broker.Call(context.Background(), BrokerDomain, BrokerVersion, operation, request, response)
}

func selectionToWire(selection Selection) accountRouterSelectionWire {
	return accountRouterSelectionWire{
		RouterName: selection.RouterName, SessionKey: selection.SessionKey, Reason: selection.Reason,
		Candidates:          append([]providers.FallbackCandidate(nil), selection.Candidates...),
		CandidateAccounts:   cloneStringMap(selection.CandidateAccounts),
		ProviderAccounts:    cloneStringMap(selection.ProviderAccounts),
		BlockAccountChoices: cloneStringMap(selection.BlockAccountChoices),
		Invalidations:       cloneStringMap(selection.accountAuthInvalidationGenerations),
	}
}

func selectionFromWire(wire accountRouterSelectionWire) Selection {
	return Selection{
		RouterName: wire.RouterName, SessionKey: wire.SessionKey, Reason: wire.Reason,
		Candidates:                         append([]providers.FallbackCandidate(nil), wire.Candidates...),
		CandidateAccounts:                  cloneStringMap(wire.CandidateAccounts),
		ProviderAccounts:                   cloneStringMap(wire.ProviderAccounts),
		BlockAccountChoices:                cloneStringMap(wire.BlockAccountChoices),
		accountAuthInvalidationGenerations: cloneStringMap(wire.Invalidations),
	}
}

func cloneStringMap(input map[string]string) map[string]string {
	output := make(map[string]string, len(input))
	for key, value := range input {
		output[key] = value
	}
	return output
}

func resultToWire(
	selection Selection,
	result *providers.FallbackResult,
	err error,
	private bool,
) accountRouterResultWire {
	wire := accountRouterResultWire{Present: result != nil}
	if result != nil {
		wire.HasResponse = result.Response != nil
		if result.Response != nil && result.Response.Usage != nil {
			usage := *result.Response.Usage
			wire.Usage = &usage
		}
		wire.Provider, wire.Model, wire.IdentityKey = result.Provider, result.Model, result.IdentityKey
		wire.Attempts = make([]accountRouterAttemptWire, 0, len(result.Attempts))
		for _, attempt := range result.Attempts {
			message := ""
			if attempt.Error != nil {
				message = attempt.Error.Error()
				if private {
					message = errPrivateProviderRequest.Error()
				}
			}
			wire.Attempts = append(wire.Attempts, accountRouterAttemptWire{
				Provider: attempt.Provider, Model: attempt.Model, IdentityKey: attempt.IdentityKey,
				Error: message, Reason: attempt.Reason, Skipped: attempt.Skipped,
			})
		}
	}
	if err != nil {
		wire.Error = err.Error()
		if private {
			wire.Error = errPrivateProviderRequest.Error()
		}
	}
	if result == nil && err != nil {
		for _, candidate := range selection.Candidates {
			classified := providers.ClassifyError(err, candidate.Provider, candidate.Model)
			if classified == nil || classified.Reason == providers.FailoverSafetyFilter {
				continue
			}
			wire.Present = true
			wire.Attempts = append(wire.Attempts, accountRouterAttemptWire{
				Provider: candidate.Provider, Model: candidate.Model, IdentityKey: candidate.StableKey(),
				Error: wire.Error, Reason: classified.Reason,
			})
			break
		}
	}
	return wire
}

func resultFromWire(wire accountRouterResultWire) (*providers.FallbackResult, error) {
	var result *providers.FallbackResult
	if wire.Present {
		result = &providers.FallbackResult{
			Provider: wire.Provider, Model: wire.Model, IdentityKey: wire.IdentityKey,
			Attempts: make([]providers.FallbackAttempt, 0, len(wire.Attempts)),
		}
		if wire.HasResponse {
			result.Response = &providers.LLMResponse{Usage: wire.Usage}
		}
		for _, attempt := range wire.Attempts {
			var attemptErr error
			if attempt.Error != "" {
				attemptErr = errors.New(attempt.Error)
			}
			result.Attempts = append(result.Attempts, providers.FallbackAttempt{
				Provider: attempt.Provider, Model: attempt.Model, IdentityKey: attempt.IdentityKey,
				Error: attemptErr, Reason: attempt.Reason, Skipped: attempt.Skipped,
			})
		}
	}
	var resultErr error
	if wire.Error != "" {
		resultErr = errors.New(wire.Error)
	}
	return result, resultErr
}

func (r *Router) brokerSelect(sessionKey string, reason SelectReason) Selection {
	var response accountRouterSelectResponse
	err := r.brokerCall(accountRouterOperationSelect, accountRouterSelectRequest{
		StoreID: r.storeID, Router: r.accountRouterSpec(), SessionKey: sessionKey, Reason: reason,
	}, &response, true)
	if err != nil {
		return Selection{}
	}
	return selectionFromWire(response.Selection)
}

func (r *Router) brokerRecordFallbackResult(
	selection Selection,
	result *providers.FallbackResult,
	err error,
	private bool,
) {
	var response accountRouterMutationResponse
	_ = r.brokerCall(accountRouterOperationRecord, accountRouterRecordRequest{
		StoreID: r.storeID, Router: r.accountRouterSpec(), Selection: selectionToWire(selection),
		Result: resultToWire(selection, result, err, private), Private: private,
	}, &response, true)
}

func (r *Router) brokerAccountStateSnapshot(account string) (AccountState, bool) {
	var response accountRouterAccountResponse
	err := r.brokerCall(accountRouterOperationAccount, accountRouterAccountRequest{
		StoreID: r.storeID, Router: r.accountRouterSpec(), Account: account,
	}, &response, false)
	return response.State, err == nil && response.Found
}

func SessionKeysForStore(storeID database.StoreID, routerName string) ([]string, error) {
	client := database.RuntimeClient()
	if client == nil {
		return nil, database.NewError(database.CodeUnavailable, "account-router broker client is unavailable")
	}
	var response accountRouterSessionKeysResponse
	err := client.Call(context.Background(), BrokerDomain, BrokerVersion, accountRouterOperationSessionKeys,
		accountRouterNamedRequest{StoreID: storeID, RouterName: routerName}, &response)
	return response.Keys, err
}

func InvalidateCredentialAuthFailureForStore(storeID database.StoreID, credentialID string) error {
	client := database.RuntimeClient()
	if client == nil {
		return database.NewError(database.CodeUnavailable, "account-router broker client is unavailable")
	}
	var response accountRouterMutationResponse
	return client.CallWithOptions(
		context.Background(), BrokerDomain, BrokerVersion, accountRouterOperationInvalidate,
		accountRouterInvalidationRequest{StoreID: storeID, CredentialID: credentialID}, &response,
		database.CallOptions{Mutation: true},
	)
}

// BrokerHandler owns the one stable account-router store/pool.
type BrokerHandler struct {
	mu      sync.RWMutex
	storeID database.StoreID
	store   *Store
	closed  bool
}

func NewBrokerHandler(home string, cfg *config.Config) (*BrokerHandler, error) {
	if !database.BrokerAuthorityHeld() && !database.ProviderTestAuthorityHeld() &&
		!allowUnfencedAccountRouterProviderForTests.Load() {
		return nil, database.NewError(
			database.CodeUnauthorized,
			"account-router broker handler requires authenticated broker authority",
		)
	}
	if cfg == nil {
		return nil, database.NewError(database.CodeInvalid, "account-router broker configuration is invalid")
	}
	catalog, err := dbcatalog.New(home, cfg)
	if err != nil {
		return nil, err
	}
	storeID, err := catalog.Lookup(AccountRoutingStoreID.String())
	if err != nil {
		return nil, err
	}
	store, err := getStore(databasePath(cfg.WorkspacePath()))
	if err != nil {
		return nil, err
	}
	if err := store.retain(); err != nil {
		return nil, err
	}
	return &BrokerHandler{storeID: storeID, store: store}, nil
}

func (handler *BrokerHandler) Handle(ctx context.Context, request database.Request) (any, error) {
	if handler == nil || request.Domain != BrokerDomain || request.Version != BrokerVersion {
		return nil, database.NewError(database.CodeUnsupported, "database domain is unsupported")
	}
	handler.mu.RLock()
	defer handler.mu.RUnlock()
	if handler.closed || handler.store == nil {
		return nil, database.NewError(database.CodeUnavailable, "account-router broker is unavailable")
	}
	return handler.handle(ctx, request)
}

func (handler *BrokerHandler) validateStoreID(storeID database.StoreID) error {
	if !storeID.Valid() || storeID != handler.storeID {
		return database.NewError(database.CodeUnauthorized, "account-router store is not cataloged")
	}
	return nil
}

func (handler *BrokerHandler) localRouter(spec accountRouterSpec) (*Router, error) {
	if strings.TrimSpace(spec.Name) == "" {
		return nil, database.NewError(database.CodeInvalid, "account-router specification is invalid")
	}
	return newRouterWithStore(spec.Name, &spec.Config, spec.Accounts, handler.store), nil
}

func (handler *BrokerHandler) handle(ctx context.Context, request database.Request) (any, error) {
	switch request.Operation {
	case accountRouterOperationSelect:
		var input accountRouterSelectRequest
		if err := request.DecodePayload(&input); err != nil {
			return nil, database.NewError(database.CodeInvalid, "account-router request is invalid")
		}
		if err := handler.validateStoreID(input.StoreID); err != nil {
			return nil, err
		}
		router, err := handler.localRouter(input.Router)
		if err != nil {
			return nil, err
		}
		selection, err := router.selectLocal(input.SessionKey, input.Reason)
		if err != nil {
			return nil, mapAccountRouterBrokerError(err)
		}
		return accountRouterSelectResponse{Selection: selectionToWire(selection)}, nil
	case accountRouterOperationRecord:
		var input accountRouterRecordRequest
		if err := request.DecodePayload(&input); err != nil {
			return nil, database.NewError(database.CodeInvalid, "account-router request is invalid")
		}
		if err := handler.validateStoreID(input.StoreID); err != nil {
			return nil, err
		}
		router, err := handler.localRouter(input.Router)
		if err != nil {
			return nil, err
		}
		result, resultErr := resultFromWire(input.Result)
		err = router.recordFallbackResult(selectionFromWire(input.Selection), result, resultErr, input.Private)
		return accountRouterMutationResponse{Updated: err == nil}, mapAccountRouterBrokerError(err)
	case accountRouterOperationSessionKeys:
		var input accountRouterNamedRequest
		if err := request.DecodePayload(&input); err != nil {
			return nil, database.NewError(database.CodeInvalid, "account-router request is invalid")
		}
		if err := handler.validateStoreID(input.StoreID); err != nil {
			return nil, err
		}
		keys, err := sessionKeysFromStore(handler.store, input.RouterName)
		return accountRouterSessionKeysResponse{Keys: keys}, mapAccountRouterBrokerError(err)
	case accountRouterOperationAccount:
		var input accountRouterAccountRequest
		if err := request.DecodePayload(&input); err != nil {
			return nil, database.NewError(database.CodeInvalid, "account-router request is invalid")
		}
		if err := handler.validateStoreID(input.StoreID); err != nil {
			return nil, err
		}
		router, err := handler.localRouter(input.Router)
		if err != nil {
			return nil, err
		}
		state, found := router.AccountStateSnapshot(input.Account)
		return accountRouterAccountResponse{State: state, Found: found}, nil
	case accountRouterOperationInvalidate:
		var input accountRouterInvalidationRequest
		if err := request.DecodePayload(&input); err != nil {
			return nil, database.NewError(database.CodeInvalid, "account-router request is invalid")
		}
		if err := handler.validateStoreID(input.StoreID); err != nil {
			return nil, err
		}
		err := invalidateCredentialAuthFailureStore(handler.store, input.CredentialID)
		return accountRouterMutationResponse{Updated: err == nil}, mapAccountRouterBrokerError(err)
	default:
		return nil, database.NewError(database.CodeUnsupported, "account-router operation is unsupported")
	}
}

func mapAccountRouterBrokerError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return database.NewError(database.CodeDeadline, "account-router operation deadline was exceeded")
	}
	return database.NewError(database.CodeInternal, "account-router operation failed")
}

func (handler *BrokerHandler) Close() error {
	if handler == nil {
		return nil
	}
	handler.mu.Lock()
	defer handler.mu.Unlock()
	if handler.closed {
		return nil
	}
	handler.closed = true
	return handler.store.closeRetained()
}

var _ database.Handler = (*BrokerHandler)(nil)
