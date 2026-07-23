package api

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/sipeed/picoclaw/pkg/auth"
)

const (
	codexAccountLimitsDefaultBaseURL  = "https://chatgpt.com"
	codexAccountLimitsOAuthTokenURL   = "https://auth.openai.com/oauth/token"
	codexAccountLimitsOpenAIClientID  = "app_EMoamEEZ73f0CkXaXp7hrann"
	codexAccountLimitsMaxErrorBodyLen = 1 << 20
)

var (
	codexAccountLimitsBaseURL    = func() string { return "" }
	codexAccountLimitsHTTPClient = http.DefaultClient
	codexAccountLimitsTokenURL   = codexAccountLimitsOAuthTokenURL
)

type codexAccountLimitsResponse struct {
	Accounts []codexAccountLimitAccount `json:"accounts"`
	Error    string                     `json:"error,omitempty"`
}

type codexAccountLimitAccount struct {
	ID               string                   `json:"id"`
	Default          bool                     `json:"default,omitempty"`
	Email            string                   `json:"email,omitempty"`
	AccountID        string                   `json:"account_id,omitempty"`
	Plan             string                   `json:"plan,omitempty"`
	CredentialStatus string                   `json:"credential_status,omitempty"`
	LimitsStatus     string                   `json:"limits_status,omitempty"`
	LimitsError      string                   `json:"limits_error,omitempty"`
	Entries          []codexAccountLimitEntry `json:"entries,omitempty"`
}

type codexAccountLimitEntry struct {
	Name        string `json:"name"`
	Status      string `json:"status"`
	Window      string `json:"window,omitempty"`
	UsedPercent *int   `json:"used_percent,omitempty"`
	RefreshesAt string `json:"refreshes_at,omitempty"`
}

type codexAccountUsageFetch struct {
	index  int
	tokens codexAuthTokens
}

type codexAuthTokens struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	AccountID    string `json:"account_id"`
	IDToken      string `json:"id_token"`
}

type codexTokenInfo struct {
	Email     string
	AccountID string
	Plan      string
	FedRAMP   bool
}

type codexUsagePayload struct {
	PlanType              string                        `json:"plan_type"`
	RateLimit             *codexRateLimitDetails        `json:"rate_limit"`
	Credits               *codexCreditDetails           `json:"credits"`
	SpendControl          *codexSpendControlDetails     `json:"spend_control"`
	AdditionalRateLimits  []codexAdditionalRateLimit    `json:"additional_rate_limits"`
	RateLimitReachedType  *codexRateLimitReachedDetails `json:"rate_limit_reached_type"`
	RateLimitResetCredits *codexRateLimitResetCredits   `json:"rate_limit_reset_credits"`
}

type codexRateLimitDetails struct {
	Allowed         bool                  `json:"allowed"`
	LimitReached    bool                  `json:"limit_reached"`
	PrimaryWindow   *codexRateLimitWindow `json:"primary_window"`
	SecondaryWindow *codexRateLimitWindow `json:"secondary_window"`
}

type codexRateLimitWindow struct {
	UsedPercent        *float64 `json:"used_percent"`
	LimitWindowSeconds int64    `json:"limit_window_seconds"`
	ResetAfterSeconds  int64    `json:"reset_after_seconds"`
	ResetAt            int64    `json:"reset_at"`
}

type codexAdditionalRateLimit struct {
	LimitName      string                 `json:"limit_name"`
	MeteredFeature string                 `json:"metered_feature"`
	RateLimit      *codexRateLimitDetails `json:"rate_limit"`
}

type codexCreditDetails struct {
	HasCredits bool    `json:"has_credits"`
	Unlimited  bool    `json:"unlimited"`
	Balance    *string `json:"balance"`
}

type codexSpendControlDetails struct {
	Reached         bool                    `json:"reached"`
	IndividualLimit *codexSpendControlLimit `json:"individual_limit"`
}

type codexSpendControlLimit struct {
	Limit            string `json:"limit"`
	Used             string `json:"used"`
	RemainingPercent int    `json:"remaining_percent"`
	ResetAt          int64  `json:"reset_at"`
}

type codexRateLimitReachedDetails struct {
	Type string `json:"type"`
}

type codexRateLimitResetCredits struct {
	AvailableCount int `json:"available_count"`
}

type codexUsageError struct {
	Status int
	Code   string
}

func (e *codexUsageError) Error() string {
	if e.Code != "" {
		return e.Code
	}
	if e.Status > 0 {
		return fmt.Sprintf("upstream_status_%d", e.Status)
	}
	return "unavailable"
}

func (h *Handler) handleCodexAccountLimits(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()

	resp, err := loadCodexAccountLimits(ctx)
	if err != nil {
		resp.Error = err.Error()
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}

func loadCodexAccountLimits(ctx context.Context) (codexAccountLimitsResponse, error) {
	store, err := auth.LoadStore()
	if err != nil {
		return codexAccountLimitsResponse{
			Accounts: []codexAccountLimitAccount{},
		}, errors.New("failed to load Picoclaw credentials")
	}

	candidates := codexAccountCredentialCandidates(store)
	resp := codexAccountLimitsResponse{
		Accounts: make([]codexAccountLimitAccount, 0, len(candidates)),
	}

	usageURL := codexAccountLimitsUsageURL(codexAccountLimitsBaseURL())
	fetches := []codexAccountUsageFetch{}
	for _, candidate := range candidates {
		account := codexAccountLimitAccount{
			ID:        candidate.credentialID,
			Default:   candidate.credentialID == oauthProviderOpenAI,
			Email:     candidate.credential.Email,
			AccountID: candidate.credential.AccountID,
		}

		tokens, status := codexAccountTokensFromCredential(candidate.credential)
		account.CredentialStatus = status
		if status != "available" {
			account.LimitsStatus = "unavailable"
			resp.Accounts = append(resp.Accounts, account)
			continue
		}

		resp.Accounts = append(resp.Accounts, account)
		fetches = append(fetches, codexAccountUsageFetch{
			index:  len(resp.Accounts) - 1,
			tokens: tokens,
		})
	}

	fetchCodexAccountUsages(ctx, usageURL, resp.Accounts, fetches)
	return resp, nil
}

func fetchCodexAccountUsages(
	ctx context.Context,
	usageURL string,
	accounts []codexAccountLimitAccount,
	fetches []codexAccountUsageFetch,
) {
	var mu sync.Mutex
	var wg sync.WaitGroup
	for _, fetch := range fetches {
		wg.Add(1)
		go func() {
			defer wg.Done()
			payload, refreshed, err := fetchCodexAccountUsage(ctx, usageURL, fetch.tokens, false)
			mu.Lock()
			defer mu.Unlock()
			account := &accounts[fetch.index]
			if refreshed.AccountID != "" {
				account.AccountID = refreshed.AccountID
			}
			if refreshed.IDToken != "" {
				refreshedInfo := codexTokenInfoFromIDToken(refreshed.IDToken)
				if account.Email == "" {
					account.Email = refreshedInfo.Email
				}
				if account.Plan == "" {
					account.Plan = refreshedInfo.Plan
				}
			}
			if err != nil {
				account.LimitsStatus = "error"
				account.LimitsError = codexLimitsErrorCode(err)
				return
			}
			if payload.PlanType != "" {
				account.Plan = payload.PlanType
			}
			account.Entries = codexUsageEntries(payload)
			account.LimitsStatus = "available"
		}()
	}
	wg.Wait()
}

type codexAccountCredentialCandidate struct {
	credentialID string
	credential   *auth.AuthCredential
}

func codexAccountCredentialCandidates(store *auth.AuthStore) []codexAccountCredentialCandidate {
	if store == nil || len(store.Credentials) == 0 {
		return nil
	}

	credentialIDs := make([]string, 0, len(store.Credentials))
	for credentialID, credential := range store.Credentials {
		if credential == nil || !credentialIDBelongsToProvider(oauthProviderOpenAI, credentialID) {
			continue
		}
		credentialIDs = append(credentialIDs, credentialID)
	}
	sort.Strings(credentialIDs)

	candidates := make([]codexAccountCredentialCandidate, 0, len(credentialIDs))
	for _, credentialID := range credentialIDs {
		candidates = append(candidates, codexAccountCredentialCandidate{
			credentialID: credentialID,
			credential:   store.Credentials[credentialID],
		})
	}
	return candidates
}

func codexAccountTokensFromCredential(credential *auth.AuthCredential) (codexAuthTokens, string) {
	if credential == nil || strings.TrimSpace(credential.AccessToken) == "" {
		return codexAuthTokens{}, "invalid"
	}
	return codexAuthTokens{
		AccessToken:  strings.TrimSpace(credential.AccessToken),
		RefreshToken: strings.TrimSpace(credential.RefreshToken),
		AccountID:    strings.TrimSpace(credential.AccountID),
	}, "available"
}

func fetchCodexAccountUsage(
	ctx context.Context,
	usageURL string,
	tokens codexAuthTokens,
	fedramp bool,
) (codexUsagePayload, codexAuthTokens, error) {
	payload, err := doFetchCodexAccountUsage(ctx, usageURL, tokens, fedramp)
	if err == nil {
		return payload, tokens, nil
	}
	var usageErr *codexUsageError
	if !errors.As(err, &usageErr) || usageErr.Status != http.StatusUnauthorized || tokens.RefreshToken == "" {
		return codexUsagePayload{}, tokens, err
	}

	refreshed, refreshErr := refreshCodexAccountLimitsToken(ctx, tokens)
	if refreshErr != nil {
		return codexUsagePayload{}, tokens, &codexUsageError{Status: http.StatusUnauthorized, Code: "token_expired"}
	}
	payload, err = doFetchCodexAccountUsage(ctx, usageURL, refreshed, fedramp)
	if err != nil {
		return codexUsagePayload{}, refreshed, err
	}
	return payload, refreshed, nil
}

func doFetchCodexAccountUsage(
	ctx context.Context,
	usageURL string,
	tokens codexAuthTokens,
	fedramp bool,
) (codexUsagePayload, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, usageURL, nil)
	if err != nil {
		return codexUsagePayload{}, err
	}
	req.Header.Set("Authorization", "Bearer "+tokens.AccessToken)
	req.Header.Set("User-Agent", "codex-cli")
	req.Header.Set("Oai-Product-Sku", "codex")
	req.Header.Set("Originator", "codex_cli_rs")
	req.Header.Set("Content-Type", "application/json")
	if tokens.AccountID != "" {
		req.Header.Set("Chatgpt-Account-Id", tokens.AccountID)
	}
	if fedramp {
		req.Header.Set("X-Openai-Fedramp", "true")
	}

	resp, err := codexAccountLimitsHTTPClient.Do(req)
	if err != nil {
		return codexUsagePayload{}, err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return codexUsagePayload{}, codexUpstreamUsageError(resp)
	}

	var payload codexUsagePayload
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return codexUsagePayload{}, err
	}
	return payload, nil
}

func refreshCodexAccountLimitsToken(ctx context.Context, tokens codexAuthTokens) (codexAuthTokens, error) {
	form := url.Values{
		"client_id":     {codexAccountLimitsOpenAIClientID},
		"grant_type":    {"refresh_token"},
		"refresh_token": {tokens.RefreshToken},
		"scope":         {"openid profile email"},
	}
	req, err := http.NewRequestWithContext(
		ctx,
		http.MethodPost,
		codexAccountLimitsTokenURL,
		strings.NewReader(form.Encode()),
	)
	if err != nil {
		return codexAuthTokens{}, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := codexAccountLimitsHTTPClient.Do(req)
	if err != nil {
		return codexAuthTokens{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return codexAuthTokens{}, codexUpstreamUsageError(resp)
	}

	var refreshed struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		IDToken      string `json:"id_token"`
		AccountID    string `json:"account_id"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&refreshed); err != nil {
		return codexAuthTokens{}, err
	}
	if strings.TrimSpace(refreshed.AccessToken) == "" {
		return codexAuthTokens{}, errors.New("empty refreshed access token")
	}

	tokens.AccessToken = strings.TrimSpace(refreshed.AccessToken)
	if refreshed.RefreshToken != "" {
		tokens.RefreshToken = strings.TrimSpace(refreshed.RefreshToken)
	}
	if refreshed.IDToken != "" {
		tokens.IDToken = strings.TrimSpace(refreshed.IDToken)
	}
	if refreshed.AccountID != "" {
		tokens.AccountID = strings.TrimSpace(refreshed.AccountID)
	} else if info := codexTokenInfoFromIDToken(tokens.IDToken); info.AccountID != "" {
		tokens.AccountID = info.AccountID
	}
	return tokens, nil
}

func codexUpstreamUsageError(resp *http.Response) error {
	body, _ := io.ReadAll(io.LimitReader(resp.Body, codexAccountLimitsMaxErrorBodyLen))
	code := codexUpstreamErrorCode(body)
	if code == "" {
		switch resp.StatusCode {
		case http.StatusUnauthorized:
			code = "unauthorized"
		case http.StatusForbidden:
			code = "forbidden"
		default:
			code = fmt.Sprintf("upstream_status_%d", resp.StatusCode)
		}
	}
	return &codexUsageError{Status: resp.StatusCode, Code: code}
}

func codexUpstreamErrorCode(body []byte) string {
	var payload struct {
		Error   any    `json:"error"`
		Code    string `json:"code"`
		Type    string `json:"type"`
		Message string `json:"message"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return ""
	}
	if payload.Code != "" {
		return sanitizeCodexErrorCode(payload.Code)
	}
	if payload.Type != "" {
		return sanitizeCodexErrorCode(payload.Type)
	}
	if payload.Message != "" {
		return sanitizeCodexErrorCode(payload.Message)
	}
	switch errValue := payload.Error.(type) {
	case map[string]any:
		for _, key := range []string{"code", "type", "message"} {
			if value, ok := errValue[key].(string); ok && value != "" {
				return sanitizeCodexErrorCode(value)
			}
		}
	case string:
		return sanitizeCodexErrorCode(errValue)
	}
	return ""
}

func codexUsageEntries(payload codexUsagePayload) []codexAccountLimitEntry {
	entries := []codexAccountLimitEntry{}
	entries = append(entries, codexEntriesForRateLimit("codex", payload.RateLimit)...)
	for _, limit := range payload.AdditionalRateLimits {
		name := firstNonEmpty(limit.LimitName, limit.MeteredFeature)
		if name == "" {
			name = "additional"
		}
		entries = append(entries, codexEntriesForRateLimit(name, limit.RateLimit)...)
	}
	if len(entries) == 0 {
		entries = append(entries, codexAccountLimitEntry{Name: "codex", Status: "available"})
	}
	return entries
}

func codexEntriesForRateLimit(name string, rateLimit *codexRateLimitDetails) []codexAccountLimitEntry {
	status := "available"
	if rateLimit != nil && (rateLimit.LimitReached || !rateLimit.Allowed) {
		status = "unavailable"
	}
	if rateLimit == nil {
		return []codexAccountLimitEntry{{Name: name, Status: status}}
	}
	entries := []codexAccountLimitEntry{}
	for _, item := range []struct {
		fallback string
		window   *codexRateLimitWindow
	}{
		{fallback: "5h", window: rateLimit.PrimaryWindow},
		{fallback: "weekly", window: rateLimit.SecondaryWindow},
	} {
		if item.window == nil {
			continue
		}
		entries = append(entries, codexEntryForWindow(name, status, item.fallback, item.window))
	}
	if len(entries) == 0 {
		entries = append(entries, codexAccountLimitEntry{Name: name, Status: status})
	}
	return entries
}

func codexEntryForWindow(
	name string,
	status string,
	fallback string,
	window *codexRateLimitWindow,
) codexAccountLimitEntry {
	entry := codexAccountLimitEntry{
		Name:        name,
		Status:      status,
		Window:      codexWindowDisplayName(window, fallback),
		RefreshesAt: codexFormatResetTime(window.ResetAt),
	}
	if window.UsedPercent != nil {
		percent := int(*window.UsedPercent + 0.5)
		entry.UsedPercent = &percent
	}
	return entry
}

func codexWindowDisplayName(window *codexRateLimitWindow, fallback string) string {
	minutes := window.LimitWindowSeconds / 60
	switch minutes {
	case 300:
		return "5h"
	case 10080:
		return "weekly"
	case 0:
		return fallback
	default:
		return formatCompactDuration(window.LimitWindowSeconds)
	}
}

func codexFormatResetTime(resetAt int64) string {
	if resetAt <= 0 {
		return "-"
	}
	return time.Unix(resetAt, 0).Local().Format("2006-01-02 15:04:05 MST")
}

func codexAccountLimitsUsageURL(baseURL string) string {
	base := strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if base == "" {
		base = codexAccountLimitsDefaultBaseURL
	}
	lower := strings.ToLower(base)
	if (strings.HasPrefix(lower, "https://chatgpt.com") || strings.HasPrefix(lower, "https://chat.openai.com")) &&
		!strings.Contains(lower, "/backend-api") {
		base += "/backend-api"
		lower = strings.ToLower(base)
	}
	if strings.Contains(lower, "/backend-api") {
		return base + "/wham/usage"
	}
	return base + "/api/codex/usage"
}

func codexTokenInfoFromIDToken(idToken string) codexTokenInfo {
	claims, err := decodeCodexJWTClaims(idToken)
	if err != nil {
		return codexTokenInfo{}
	}
	info := codexTokenInfo{}
	if email, ok := claims["email"].(string); ok {
		info.Email = email
	}
	if profile, ok := claims["https://api.openai.com/profile"].(map[string]any); ok && info.Email == "" {
		if email, ok := profile["email"].(string); ok {
			info.Email = email
		}
	}
	if accountID, ok := claims["chatgpt_account_id"].(string); ok {
		info.AccountID = accountID
	}
	if accountID, ok := claims["https://api.openai.com/auth.chatgpt_account_id"].(string); ok && info.AccountID == "" {
		info.AccountID = accountID
	}
	if authClaim, ok := claims["https://api.openai.com/auth"].(map[string]any); ok {
		if accountID, ok := authClaim["chatgpt_account_id"].(string); ok && info.AccountID == "" {
			info.AccountID = accountID
		}
		if plan, ok := authClaim["chatgpt_plan_type"].(string); ok {
			info.Plan = plan
		}
		if fedramp, ok := authClaim["chatgpt_account_is_fedramp"].(bool); ok {
			info.FedRAMP = fedramp
		}
	}
	return info
}

func decodeCodexJWTClaims(token string) (map[string]any, error) {
	parts := strings.Split(token, ".")
	if len(parts) < 2 || parts[1] == "" {
		return nil, errors.New("not a jwt")
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return nil, err
	}
	claims := map[string]any{}
	if err := json.Unmarshal(payload, &claims); err != nil {
		return nil, err
	}
	return claims, nil
}

func codexLimitsErrorCode(err error) string {
	var usageErr *codexUsageError
	if errors.As(err, &usageErr) {
		return sanitizeCodexErrorCode(usageErr.Error())
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "timeout"
	}
	if strings.Contains(strings.ToLower(err.Error()), "context deadline exceeded") {
		return "timeout"
	}
	return "unavailable"
}

func sanitizeCodexErrorCode(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	value = strings.ReplaceAll(value, "-", "_")
	value = strings.ReplaceAll(value, " ", "_")
	value = regexp.MustCompile(`[^a-z0-9_]+`).ReplaceAllString(value, "")
	if value == "" {
		return "unavailable"
	}
	switch {
	case strings.Contains(value, "token_expired"):
		return "token_expired"
	case strings.Contains(value, "token") && strings.Contains(value, "expired"):
		return "token_expired"
	case strings.Contains(value, "unauthorized"):
		return "unauthorized"
	case strings.Contains(value, "forbidden"):
		return "forbidden"
	default:
		return value
	}
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func formatCompactDuration(seconds int64) string {
	remaining := seconds
	if remaining < 0 {
		remaining = 0
	}
	if remaining == 0 {
		return "0s"
	}
	units := []struct {
		suffix  string
		seconds int64
	}{
		{"w", 604800},
		{"d", 86400},
		{"h", 3600},
		{"m", 60},
		{"s", 1},
	}
	parts := []string{}
	for _, unit := range units {
		value := remaining / unit.seconds
		if value == 0 {
			continue
		}
		parts = append(parts, strconv.FormatInt(value, 10)+unit.suffix)
		remaining %= unit.seconds
		if len(parts) == 2 {
			break
		}
	}
	return strings.Join(parts, "")
}
