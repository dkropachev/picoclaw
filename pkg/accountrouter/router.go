package accountrouter

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash/fnv"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/sipeed/picoclaw/pkg/auth"
	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/fileutil"
	"github.com/sipeed/picoclaw/pkg/providers"
)

const (
	stateVersion     = 1
	sessionStateTTL  = 30 * 24 * time.Hour
	defaultStatePerm = 0o600

	credentialAuthInvalidationVersion = 1
)

var errPrivateProviderRequest = errors.New("provider request failed")

type SelectReason string

const (
	SelectReasonInitial     SelectReason = "initial"
	SelectReasonCompression SelectReason = "compression"
	SelectReasonUnhealthy   SelectReason = "unhealthy"
)

type Account struct {
	Name       string
	Candidates []providers.FallbackCandidate
	RPM        int
}

type Router struct {
	Name       string
	Config     config.AccountRouterConfig
	Accounts   map[string]Account
	StatePath  string
	ConfigHash string

	store *Store
	now   func() time.Time
}

type Selection struct {
	RouterName          string
	SessionKey          string
	Reason              SelectReason
	Candidates          []providers.FallbackCandidate
	CandidateAccounts   map[string]string
	ProviderAccounts    map[string]string
	BlockAccountChoices map[string]string

	accountAuthInvalidationGenerations map[string]string
}

type State struct {
	Version int                     `json:"version"`
	Routers map[string]*RouterState `json:"routers"`
}

type RouterState struct {
	ConfigHash string                    `json:"config_hash"`
	Accounts   map[string]*AccountState  `json:"accounts"`
	Sessions   map[string]*SessionState  `json:"sessions"`
	Blocks     map[string]*BlockRunState `json:"blocks,omitempty"`
	UpdatedAt  time.Time                 `json:"updated_at"`
}

type AccountState struct {
	State                      string                   `json:"state"`
	Reason                     providers.FailoverReason `json:"reason,omitempty"`
	FailureCount               int                      `json:"failure_count,omitempty"`
	Requests                   int64                    `json:"requests,omitempty"`
	RateWindowStart            time.Time                `json:"rate_window_start,omitempty"`
	RateWindowReqs             int64                    `json:"rate_window_reqs,omitempty"`
	PromptTokens               int64                    `json:"prompt_tokens,omitempty"`
	CompletionTokens           int64                    `json:"completion_tokens,omitempty"`
	TotalTokens                int64                    `json:"total_tokens,omitempty"`
	UnavailableUntil           time.Time                `json:"unavailable_until,omitempty"`
	LastFailureAt              time.Time                `json:"last_failure_at,omitempty"`
	LastSuccessAt              time.Time                `json:"last_success_at,omitempty"`
	LastError                  string                   `json:"last_error,omitempty"`
	AuthInvalidationGeneration string                   `json:"auth_invalidation_generation,omitempty"`
}

type credentialAuthInvalidation struct {
	Version      int    `json:"version"`
	CredentialID string `json:"credential_id"`
	Generation   string `json:"generation"`
}

type SessionState struct {
	ConfigHash string                   `json:"config_hash"`
	Blocks     map[string]BlockAffinity `json:"blocks"`
	UpdatedAt  time.Time                `json:"updated_at"`
}

type BlockAffinity struct {
	Account    string       `json:"account"`
	Reason     SelectReason `json:"reason"`
	SelectedAt time.Time    `json:"selected_at"`
}

type BlockRunState struct {
	Cursor    int       `json:"cursor,omitempty"`
	UpdatedAt time.Time `json:"updated_at,omitempty"`
}

type Store struct {
	path string
	mu   sync.Mutex
	st   State
}

var stores sync.Map

func New(name string, routerConfig *config.AccountRouterConfig, accounts map[string]Account, statePath string) *Router {
	if routerConfig == nil || strings.TrimSpace(name) == "" || strings.TrimSpace(statePath) == "" {
		return nil
	}
	cfg := *routerConfig
	cfg.Blocks = append([]config.AccountRouterBlock(nil), routerConfig.Blocks...)
	for i := range cfg.Blocks {
		cfg.Blocks[i].Accounts = append([]string(nil), routerConfig.Blocks[i].Accounts...)
	}
	cleanAccounts := make(map[string]Account, len(accounts))
	for key, account := range accounts {
		key = strings.TrimSpace(key)
		if key == "" || len(account.Candidates) == 0 {
			continue
		}
		account.Name = key
		account.Candidates = append([]providers.FallbackCandidate(nil), account.Candidates...)
		cleanAccounts[key] = account
	}
	return &Router{
		Name:       strings.TrimSpace(name),
		Config:     cfg,
		Accounts:   cleanAccounts,
		StatePath:  statePath,
		ConfigHash: hashRouterConfig(cfg),
		store:      getStore(statePath),
		now:        time.Now,
	}
}

// InvalidateCredentialAuthFailure records a durable generation change for one
// exact auth-store credential. Account-router stores consume the change in
// their owning process and clear only authentication-failure health for the
// matching credential account across every router in that state file.
//
// The invalidation is kept in a sidecar instead of rewriting the router state
// directly. The gateway owns and caches that state in memory, so an external
// rewrite could otherwise be overwritten by its next persistence cycle.
func InvalidateCredentialAuthFailure(statePath, credentialID string) error {
	statePath = strings.TrimSpace(statePath)
	if statePath == "" {
		return fmt.Errorf("account router state path is required")
	}
	normalizedCredentialID, err := normalizeCredentialID(credentialID)
	if err != nil {
		return err
	}

	statePath = filepath.Clean(statePath)
	if _, statErr := os.Stat(statePath); statErr != nil {
		if errors.Is(statErr, os.ErrNotExist) {
			// No persisted router health exists to invalidate. Avoid creating
			// workspace state as a side effect of onboarding a new credential.
			return nil
		}
		return fmt.Errorf("stat account router state: %w", statErr)
	}

	generationBytes := make([]byte, 16)
	if _, randomErr := rand.Read(generationBytes); randomErr != nil {
		return fmt.Errorf("generate account router auth invalidation: %w", randomErr)
	}
	marker := credentialAuthInvalidation{
		Version:      credentialAuthInvalidationVersion,
		CredentialID: normalizedCredentialID,
		Generation:   hex.EncodeToString(generationBytes),
	}
	data, err := json.MarshalIndent(marker, "", "  ")
	if err != nil {
		return fmt.Errorf("encode account router auth invalidation: %w", err)
	}
	if err := fileutil.WriteFileAtomic(
		credentialAuthInvalidationPath(statePath, normalizedCredentialID),
		data,
		defaultStatePerm,
	); err != nil {
		return fmt.Errorf("write account router auth invalidation: %w", err)
	}
	return nil
}

func (r *Router) Select(sessionKey string, reason SelectReason) Selection {
	if r == nil {
		return Selection{}
	}
	selection := Selection{
		RouterName:                         r.Name,
		SessionKey:                         sessionKey,
		Reason:                             reason,
		CandidateAccounts:                  map[string]string{},
		ProviderAccounts:                   map[string]string{},
		BlockAccountChoices:                map[string]string{},
		accountAuthInvalidationGenerations: map[string]string{},
	}
	if !r.Config.Enabled {
		return selection
	}

	_ = r.store.update(func(st *State) {
		rs := routerState(st, r.Name, r.ConfigHash, r.knownAccountNames())
		now := r.now()
		pruneRouterState(rs, now, r.ConfigHash, r.knownAccountNames())
		session := sessionState(rs, sessionKey, r.ConfigHash, now)
		candidates := r.expandBlock(
			rs,
			session,
			strings.TrimSpace(r.Config.Entry),
			sessionKey,
			reason,
			map[string]bool{},
			&selection,
		)
		selection.Candidates = dedupeCandidates(candidates)
		invalidationGenerations := selection.accountAuthInvalidationGenerations
		for _, candidate := range selection.Candidates {
			if account := selection.CandidateAccounts[candidate.StableKey()]; account != "" {
				selection.ProviderAccounts[providers.ModelKey(candidate.Provider, candidate.Model)] = account
				if _, ok := invalidationGenerations[account]; !ok {
					invalidationGenerations[account] = accountAuthInvalidationGeneration(rs, account)
				}
			}
		}
		if session != nil {
			session.UpdatedAt = now
		}
		rs.UpdatedAt = now
	})

	return selection
}

func (r *Router) RecordFallbackResult(selection Selection, result *providers.FallbackResult, err error) {
	r.recordFallbackResult(selection, result, err, false)
}

// RecordPrivateFallbackResult updates health and usage accounting without
// persisting provider error text that may echo private request material.
func (r *Router) RecordPrivateFallbackResult(
	selection Selection,
	result *providers.FallbackResult,
	err error,
) {
	r.recordFallbackResult(selection, result, err, true)
}

func (r *Router) recordFallbackResult(
	selection Selection,
	result *providers.FallbackResult,
	err error,
	private bool,
) {
	if r == nil || selection.RouterName == "" || selection.RouterName != r.Name {
		return
	}
	_ = r.store.update(func(st *State) {
		rs := routerState(st, r.Name, r.ConfigHash, r.knownAccountNames())
		now := r.now()
		pruneRouterState(rs, now, r.ConfigHash, r.knownAccountNames())
		attempts := resultAttempts(result)
		for _, attempt := range attempts {
			if attempt.Error == nil || attempt.Reason == providers.FailoverSafetyFilter {
				continue
			}
			account := selection.CandidateAccounts[attempt.IdentityKey]
			if account == "" {
				account = selection.ProviderAccounts[providers.ModelKey(attempt.Provider, attempt.Model)]
			}
			if account == "" {
				continue
			}
			failureErr := attempt.Error
			if private {
				failureErr = errPrivateProviderRequest
			}
			markSelectionAccountFailure(rs, selection, account, attempt.Reason, failureErr, now)
		}
		if result != nil && result.Response != nil {
			account := selection.CandidateAccounts[result.IdentityKey]
			if account == "" {
				account = selection.ProviderAccounts[providers.ModelKey(result.Provider, result.Model)]
			}
			if account != "" {
				markAccountSuccess(rs, account, result.Response.Usage, now)
				if selection.SessionKey != "" {
					session := sessionState(rs, selection.SessionKey, r.ConfigHash, now)
					for blockID, selectedAccount := range selection.BlockAccountChoices {
						if selectedAccount == account {
							session.Blocks[blockID] = BlockAffinity{
								Account:    account,
								Reason:     selection.Reason,
								SelectedAt: now,
							}
						}
					}
					session.UpdatedAt = now
				}
			}
		} else if err != nil && len(attempts) == 0 {
			for _, candidate := range selection.Candidates {
				account := selection.CandidateAccounts[candidate.StableKey()]
				if account == "" {
					continue
				}
				if failErr := providers.ClassifyError(err, candidate.Provider, candidate.Model); failErr != nil &&
					failErr.Reason != providers.FailoverSafetyFilter {
					failureErr := err
					if private {
						failureErr = errPrivateProviderRequest
					}
					markSelectionAccountFailure(rs, selection, account, failErr.Reason, failureErr, now)
				}
				break
			}
		}
		rs.UpdatedAt = now
	})
}

func (r *Router) expandBlock(
	rs *RouterState,
	session *SessionState,
	blockID string,
	sessionKey string,
	reason SelectReason,
	seen map[string]bool,
	selection *Selection,
) []providers.FallbackCandidate {
	blockID = strings.TrimSpace(blockID)
	if blockID == "" || seen[blockID] {
		return nil
	}
	seen[blockID] = true
	defer delete(seen, blockID)

	block, ok := r.block(blockID)
	if !ok {
		return nil
	}

	var candidates []providers.FallbackCandidate
	switch strings.TrimSpace(block.Type) {
	case config.AccountRouterBlockTypeAccount:
		account := strings.TrimSpace(block.Account)
		accountCandidates := r.accountCandidates(rs, account)
		fallbackCandidates := r.expandBlock(rs, session, block.Fallback, sessionKey, reason, seen, selection)
		if len(accountCandidates) == 0 {
			return fallbackCandidates
		}
		if !isAccountOperational(rs, account, r.now()) && len(fallbackCandidates) > 0 {
			return fallbackCandidates
		}
		selection.BlockAccountChoices[blockID] = account
		candidates = append(candidates, r.tagCandidates(accountCandidates, account, selection)...)
		candidates = append(candidates, fallbackCandidates...)
	case config.AccountRouterBlockTypeLoadBalance:
		account := r.selectLoadBalancedAccount(rs, session, block, sessionKey, reason)
		accountCandidates := r.accountCandidates(rs, account)
		fallbackCandidates := r.expandBlock(rs, session, block.Fallback, sessionKey, reason, seen, selection)
		if len(accountCandidates) == 0 {
			return fallbackCandidates
		}
		selection.BlockAccountChoices[blockID] = account
		candidates = append(candidates, r.tagCandidates(accountCandidates, account, selection)...)
		candidates = append(candidates, fallbackCandidates...)
	case config.AccountRouterBlockTypeBranch:
		next := block.Else
		if r.evaluateCondition(rs, block.Condition) {
			next = block.Then
		}
		candidates = append(
			candidates,
			r.expandBlock(rs, session, next, sessionKey, reason, seen, selection)...,
		)
		candidates = append(
			candidates,
			r.expandBlock(rs, session, block.Fallback, sessionKey, reason, seen, selection)...,
		)
	}
	return candidates
}

func (r *Router) evaluateCondition(rs *RouterState, condition *config.AccountRouterCondition) bool {
	if condition == nil {
		return false
	}
	left, ok := r.evaluateExpression(rs, condition.Left)
	if !ok {
		return false
	}
	right, ok := r.evaluateExpression(rs, condition.Right)
	if !ok {
		return false
	}
	switch strings.TrimSpace(condition.Operator) {
	case config.AccountRouterBranchOpGT:
		return left > right
	case config.AccountRouterBranchOpGTE:
		return left >= right
	case config.AccountRouterBranchOpLT:
		return left < right
	case config.AccountRouterBranchOpLTE:
		return left <= right
	case config.AccountRouterBranchOpEQ:
		return math.Abs(left-right) < 1e-9
	case config.AccountRouterBranchOpNEQ:
		return math.Abs(left-right) >= 1e-9
	default:
		return false
	}
}

func (r *Router) evaluateExpression(rs *RouterState, expr config.AccountRouterExpression) (float64, bool) {
	switch strings.TrimSpace(expr.Op) {
	case config.AccountRouterMathAdd, config.AccountRouterMathSubtract, config.AccountRouterMathMultiply,
		config.AccountRouterMathDivide, config.AccountRouterMathModulo:
		if expr.Left == nil || expr.Right == nil {
			return 0, false
		}
		left, ok := r.evaluateExpression(rs, *expr.Left)
		if !ok {
			return 0, false
		}
		right, ok := r.evaluateExpression(rs, *expr.Right)
		if !ok {
			return 0, false
		}
		switch strings.TrimSpace(expr.Op) {
		case config.AccountRouterMathAdd:
			return left + right, true
		case config.AccountRouterMathSubtract:
			return left - right, true
		case config.AccountRouterMathMultiply:
			return left * right, true
		case config.AccountRouterMathDivide:
			if right == 0 {
				return 0, false
			}
			return left / right, true
		case config.AccountRouterMathModulo:
			if right == 0 {
				return 0, false
			}
			return math.Mod(left, right), true
		}
	case "":
		if expr.Value != nil {
			return *expr.Value, true
		}
		return r.accountMetric(rs, expr.Account, expr.Metric)
	}
	return 0, false
}

func (r *Router) accountMetric(rs *RouterState, account string, metric string) (float64, bool) {
	account = strings.TrimSpace(account)
	metric = strings.TrimSpace(metric)
	if account == "" || metric == "" {
		return 0, false
	}
	meta, ok := r.Accounts[account]
	if !ok {
		return 0, false
	}
	state := rs.Accounts[account]
	switch metric {
	case "rpm":
		return float64(meta.RPM), true
	case "requests":
		if state == nil {
			return 0, true
		}
		return float64(state.Requests), true
	case "rate_window_reqs":
		if state == nil {
			return 0, true
		}
		if state.RateWindowStart.IsZero() || r.now().Sub(state.RateWindowStart) >= time.Minute {
			return 0, true
		}
		return float64(state.RateWindowReqs), true
	case "prompt_tokens":
		if state == nil {
			return 0, true
		}
		return float64(state.PromptTokens), true
	case "completion_tokens":
		if state == nil {
			return 0, true
		}
		return float64(state.CompletionTokens), true
	case "total_tokens", "tokens_spent":
		return float64(accountTokens(rs, account)), true
	case "limit_pressure":
		return r.accountLimitPressure(rs, account, r.now()), true
	default:
		return 0, false
	}
}

func (r *Router) selectLoadBalancedAccount(
	rs *RouterState,
	session *SessionState,
	block config.AccountRouterBlock,
	sessionKey string,
	reason SelectReason,
) string {
	blockID := strings.TrimSpace(block.ID)
	now := r.now()
	accounts := nonEmptyUnique(block.Accounts)
	operational := make([]string, 0, len(accounts))
	for _, account := range accounts {
		if isAccountOperational(rs, account, now) && len(r.accountCandidates(rs, account)) > 0 {
			operational = append(operational, account)
		}
	}
	if len(operational) == 0 {
		for _, account := range accounts {
			if len(r.accountCandidates(rs, account)) > 0 {
				operational = append(operational, account)
			}
		}
	}
	if len(operational) == 0 {
		return ""
	}
	if reason != SelectReasonCompression && session != nil {
		if affinity, ok := session.Blocks[blockID]; ok && containsString(operational, affinity.Account) {
			return affinity.Account
		}
	}

	chosen := r.chooseAccountByStrategy(rs, block, operational, sessionKey)
	if chosen == "" {
		chosen = operational[0]
	}
	if session != nil {
		session.Blocks[blockID] = BlockAffinity{
			Account:    chosen,
			Reason:     reason,
			SelectedAt: now,
		}
	}
	return chosen
}

func (r *Router) chooseAccountByStrategy(
	rs *RouterState,
	block config.AccountRouterBlock,
	accounts []string,
	sessionKey string,
) string {
	switch strings.TrimSpace(block.Strategy) {
	case config.AccountRouterStrategyTokensSpent:
		sort.SliceStable(accounts, func(i, j int) bool {
			return accountTokens(rs, accounts[i]) < accountTokens(rs, accounts[j])
		})
		return accounts[0]
	case config.AccountRouterStrategyClosestLimit:
		now := r.now()
		sort.SliceStable(accounts, func(i, j int) bool {
			return r.accountLimitPressure(rs, accounts[i], now) < r.accountLimitPressure(rs, accounts[j], now)
		})
		return accounts[0]
	default:
		if sessionKey != "" {
			idx := stableIndex(sessionKey+"|"+block.ID, len(accounts))
			return accounts[idx]
		}
		return r.nextBlindAccount(rs, block, accounts)
	}
}

func (r *Router) nextBlindAccount(rs *RouterState, block config.AccountRouterBlock, accounts []string) string {
	if len(accounts) == 0 {
		return ""
	}
	blockID := strings.TrimSpace(block.ID)
	if rs.Blocks == nil {
		rs.Blocks = map[string]*BlockRunState{}
	}
	state := rs.Blocks[blockID]
	if state == nil {
		state = &BlockRunState{}
		rs.Blocks[blockID] = state
	}
	intervalSeconds := block.RefreshIntervalSeconds
	if intervalSeconds <= 0 {
		intervalSeconds = r.Config.EffectiveRefreshIntervalSeconds()
	}
	now := r.now()
	if state.UpdatedAt.IsZero() {
		state.UpdatedAt = now
		return accounts[state.Cursor%len(accounts)]
	}
	if now.Sub(state.UpdatedAt) >= time.Duration(intervalSeconds)*time.Second {
		state.Cursor++
		state.UpdatedAt = now
	}
	return accounts[state.Cursor%len(accounts)]
}

func (r *Router) accountLimitPressure(rs *RouterState, account string, now time.Time) float64 {
	meta, ok := r.Accounts[account]
	if !ok || meta.RPM <= 0 {
		return 0
	}
	state := rs.Accounts[account]
	if state == nil {
		return 0
	}
	if state.RateWindowStart.IsZero() || now.Sub(state.RateWindowStart) >= time.Minute {
		return 0
	}
	return float64(state.RateWindowReqs) / float64(meta.RPM)
}

func (r *Router) block(id string) (config.AccountRouterBlock, bool) {
	for _, block := range r.Config.Blocks {
		if strings.TrimSpace(block.ID) == id {
			return block, true
		}
	}
	return config.AccountRouterBlock{}, false
}

func (r *Router) accountCandidates(rs *RouterState, account string) []providers.FallbackCandidate {
	account = strings.TrimSpace(account)
	meta, ok := r.Accounts[account]
	if !ok {
		return nil
	}
	return append([]providers.FallbackCandidate(nil), meta.Candidates...)
}

func (r *Router) tagCandidates(
	candidates []providers.FallbackCandidate,
	account string,
	selection *Selection,
) []providers.FallbackCandidate {
	out := make([]providers.FallbackCandidate, 0, len(candidates))
	for _, candidate := range candidates {
		selection.CandidateAccounts[candidate.StableKey()] = account
		selection.ProviderAccounts[providers.ModelKey(candidate.Provider, candidate.Model)] = account
		out = append(out, candidate)
	}
	return out
}

func (r *Router) knownAccountNames() map[string]bool {
	out := make(map[string]bool, len(r.Accounts))
	for account := range r.Accounts {
		out[account] = true
	}
	return out
}

func normalizeCredentialID(credentialID string) (string, error) {
	credentialID = strings.ToLower(strings.TrimSpace(credentialID))
	if credentialID == "" {
		return "", fmt.Errorf("credential id is required")
	}
	provider := credentialID
	if prefix, _, ok := strings.Cut(credentialID, ":"); ok {
		provider = prefix
	}
	normalized, err := auth.NormalizeCredentialID(provider, credentialID)
	if err != nil {
		return "", fmt.Errorf("normalize credential id: %w", err)
	}
	return normalized, nil
}

func normalizedCredentialIDForAccount(accountRef string) (string, bool) {
	credentialID, ok := config.AccountRouterCredentialAccountID(accountRef)
	if !ok {
		return "", false
	}
	provider, ok := config.AccountRouterCredentialAccountProvider(accountRef)
	if !ok {
		return "", false
	}
	normalized, err := auth.NormalizeCredentialID(provider, credentialID)
	if err != nil {
		return "", false
	}
	return normalized, true
}

func credentialAuthInvalidationPath(statePath, normalizedCredentialID string) string {
	sum := sha256.Sum256([]byte(normalizedCredentialID))
	return fmt.Sprintf("%s.auth-invalidation.%x", statePath, sum[:16])
}

func readCredentialAuthInvalidation(
	statePath, normalizedCredentialID string,
) (credentialAuthInvalidation, bool) {
	data, err := os.ReadFile(
		credentialAuthInvalidationPath(statePath, normalizedCredentialID),
	)
	if err != nil {
		return credentialAuthInvalidation{}, false
	}
	var marker credentialAuthInvalidation
	if unmarshalErr := json.Unmarshal(data, &marker); unmarshalErr != nil ||
		marker.Version != credentialAuthInvalidationVersion ||
		strings.TrimSpace(marker.Generation) == "" {
		return credentialAuthInvalidation{}, false
	}
	markerCredentialID, err := normalizeCredentialID(marker.CredentialID)
	if err != nil || markerCredentialID != normalizedCredentialID {
		return credentialAuthInvalidation{}, false
	}
	marker.CredentialID = markerCredentialID
	return marker, true
}

func (s *Store) applyCredentialAuthInvalidations() {
	if s == nil || s.path == "" {
		return
	}
	markers := make(map[string]credentialAuthInvalidation)
	missingMarkers := make(map[string]bool)
	for _, router := range s.st.Routers {
		if router == nil {
			continue
		}
		for accountRef, accountState := range router.Accounts {
			if accountState == nil {
				continue
			}
			credentialID, ok := normalizedCredentialIDForAccount(accountRef)
			if !ok {
				continue
			}
			marker, found := markers[credentialID]
			if !found && !missingMarkers[credentialID] {
				marker, found = readCredentialAuthInvalidation(s.path, credentialID)
				if found {
					markers[credentialID] = marker
				} else {
					missingMarkers[credentialID] = true
				}
			}
			if !found || accountState.AuthInvalidationGeneration == marker.Generation {
				continue
			}
			if accountState.Reason == providers.FailoverAuth {
				resetAccountAuthFailure(accountState)
			}
			// Advance the fence even when another health reason is active. This
			// prevents a late result from the pre-renewal credential from
			// introducing a new authentication failure.
			accountState.AuthInvalidationGeneration = marker.Generation
		}
	}
}

func getStore(path string) *Store {
	path = filepath.Clean(path)
	if value, ok := stores.Load(path); ok {
		return value.(*Store)
	}
	store := &Store{path: path, st: State{Version: stateVersion, Routers: map[string]*RouterState{}}}
	store.load()
	actual, _ := stores.LoadOrStore(path, store)
	return actual.(*Store)
}

func (s *Store) load() {
	if s == nil || s.path == "" {
		return
	}
	data, err := os.ReadFile(s.path)
	if err != nil {
		return
	}
	var st State
	if err := json.Unmarshal(data, &st); err != nil {
		corruptPath := fmt.Sprintf("%s.corrupt.%d", s.path, time.Now().Unix())
		_ = os.Rename(s.path, corruptPath)
		return
	}
	if st.Version == 0 {
		st.Version = stateVersion
	}
	if st.Routers == nil {
		st.Routers = map[string]*RouterState{}
	}
	s.st = st
}

func (s *Store) update(fn func(*State)) error {
	if s == nil || fn == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.st.Version == 0 {
		s.st.Version = stateVersion
	}
	if s.st.Routers == nil {
		s.st.Routers = map[string]*RouterState{}
	}
	s.applyCredentialAuthInvalidations()
	fn(&s.st)
	s.applyCredentialAuthInvalidations()
	data, err := json.MarshalIndent(s.st, "", "  ")
	if err != nil {
		return err
	}
	return fileutil.WriteFileAtomic(s.path, data, defaultStatePerm)
}

func routerState(st *State, name string, configHash string, knownAccounts map[string]bool) *RouterState {
	if st.Routers == nil {
		st.Routers = map[string]*RouterState{}
	}
	rs := st.Routers[name]
	if rs == nil || rs.ConfigHash != configHash {
		rs = &RouterState{
			ConfigHash: configHash,
			Accounts:   map[string]*AccountState{},
			Sessions:   map[string]*SessionState{},
			Blocks:     map[string]*BlockRunState{},
		}
		st.Routers[name] = rs
	}
	if rs.Accounts == nil {
		rs.Accounts = map[string]*AccountState{}
	}
	if rs.Sessions == nil {
		rs.Sessions = map[string]*SessionState{}
	}
	if rs.Blocks == nil {
		rs.Blocks = map[string]*BlockRunState{}
	}
	for account := range knownAccounts {
		if rs.Accounts[account] == nil {
			rs.Accounts[account] = &AccountState{State: "operational"}
		}
	}
	return rs
}

func sessionState(rs *RouterState, sessionKey string, configHash string, now time.Time) *SessionState {
	if sessionKey == "" {
		return nil
	}
	session := rs.Sessions[sessionKey]
	if session == nil || session.ConfigHash != configHash {
		session = &SessionState{
			ConfigHash: configHash,
			Blocks:     map[string]BlockAffinity{},
			UpdatedAt:  now,
		}
		rs.Sessions[sessionKey] = session
	}
	if session.Blocks == nil {
		session.Blocks = map[string]BlockAffinity{}
	}
	return session
}

func pruneRouterState(rs *RouterState, now time.Time, configHash string, knownAccounts map[string]bool) {
	for account := range rs.Accounts {
		if !knownAccounts[account] {
			delete(rs.Accounts, account)
		}
	}
	for key, session := range rs.Sessions {
		if session == nil || session.ConfigHash != configHash || now.Sub(session.UpdatedAt) > sessionStateTTL {
			delete(rs.Sessions, key)
		}
	}
}

func accountAuthInvalidationGeneration(rs *RouterState, account string) string {
	if rs == nil || rs.Accounts == nil || rs.Accounts[account] == nil {
		return ""
	}
	return rs.Accounts[account].AuthInvalidationGeneration
}

func markSelectionAccountFailure(
	rs *RouterState,
	selection Selection,
	account string,
	reason providers.FailoverReason,
	err error,
	now time.Time,
) {
	if reason == providers.FailoverAuth {
		selectedGeneration, generationTracked := selection.accountAuthInvalidationGenerations[account]
		if generationTracked && selectedGeneration != accountAuthInvalidationGeneration(rs, account) {
			return
		}
	}
	markAccountFailure(rs, account, reason, err, now)
}

func markAccountFailure(rs *RouterState, account string, reason providers.FailoverReason, err error, now time.Time) {
	if account == "" {
		return
	}
	state := rs.Accounts[account]
	if state == nil {
		state = &AccountState{}
		rs.Accounts[account] = state
	}
	state.State = "unavailable"
	state.Reason = reason
	state.FailureCount++
	state.LastFailureAt = now
	state.UnavailableUntil = now.Add(cooldownFor(reason, state.FailureCount))
	if err != nil {
		state.LastError = err.Error()
	}
}

func resetAccountAuthFailure(state *AccountState) {
	if state == nil {
		return
	}
	state.State = "operational"
	state.Reason = ""
	state.FailureCount = 0
	state.UnavailableUntil = time.Time{}
	state.LastError = ""
}

func markAccountSuccess(rs *RouterState, account string, usage *providers.UsageInfo, now time.Time) {
	if account == "" {
		return
	}
	state := rs.Accounts[account]
	if state == nil {
		state = &AccountState{}
		rs.Accounts[account] = state
	}
	state.State = "operational"
	state.Reason = ""
	state.FailureCount = 0
	state.UnavailableUntil = time.Time{}
	state.LastSuccessAt = now
	state.LastError = ""
	state.Requests++
	if state.RateWindowStart.IsZero() || now.Sub(state.RateWindowStart) >= time.Minute {
		state.RateWindowStart = now
		state.RateWindowReqs = 0
	}
	state.RateWindowReqs++
	if usage != nil {
		state.PromptTokens += int64(usage.PromptTokens)
		state.CompletionTokens += int64(usage.CompletionTokens)
		state.TotalTokens += int64(usage.TotalTokens)
	}
}

func isAccountOperational(rs *RouterState, account string, now time.Time) bool {
	state := rs.Accounts[account]
	if state == nil || state.State != "unavailable" {
		return true
	}
	if !state.UnavailableUntil.IsZero() && now.After(state.UnavailableUntil) {
		state.State = "operational"
		state.Reason = ""
		state.UnavailableUntil = time.Time{}
		return true
	}
	return false
}

func cooldownFor(reason providers.FailoverReason, failures int) time.Duration {
	failures = max(1, failures)
	switch reason {
	case providers.FailoverAuth, providers.FailoverBilling:
		return 24 * time.Hour
	case providers.FailoverRateLimit:
		return time.Minute
	case providers.FailoverNetwork, providers.FailoverTimeout, providers.FailoverOverloaded:
		delay := time.Minute * time.Duration(1<<min(failures-1, 5))
		return min(delay, time.Hour)
	default:
		return 5 * time.Minute
	}
}

func resultAttempts(result *providers.FallbackResult) []providers.FallbackAttempt {
	if result == nil {
		return nil
	}
	return result.Attempts
}

func dedupeCandidates(candidates []providers.FallbackCandidate) []providers.FallbackCandidate {
	seen := map[string]bool{}
	out := make([]providers.FallbackCandidate, 0, len(candidates))
	for _, candidate := range candidates {
		key := candidate.StableKey()
		if key == "" || seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, candidate)
	}
	return out
}

func accountTokens(rs *RouterState, account string) int64 {
	state := rs.Accounts[account]
	if state == nil {
		return 0
	}
	if state.TotalTokens > 0 {
		return state.TotalTokens
	}
	return state.PromptTokens + state.CompletionTokens
}

func nonEmptyUnique(values []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" || seen[value] {
			continue
		}
		seen[value] = true
		out = append(out, value)
	}
	return out
}

func containsString(values []string, needle string) bool {
	for _, value := range values {
		if value == needle {
			return true
		}
	}
	return false
}

func stableIndex(seed string, modulo int) int {
	if modulo <= 1 {
		return 0
	}
	h := fnv.New64a()
	_, _ = h.Write([]byte(seed))
	return int(h.Sum64() % uint64(modulo))
}

func hashRouterConfig(cfg config.AccountRouterConfig) string {
	data, _ := json.Marshal(cfg)
	sum := sha256.Sum256(data)
	return fmt.Sprintf("%x", sum[:8])
}
