package oauthprovider

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"
)

const (
	codexUsageURL                  = "https://chatgpt.com/backend-api/wham/usage"
	codexConsumeResetCreditURL     = "https://chatgpt.com/backend-api/wham/rate-limit-reset-credits/consume"
	codexUsageLimitReachedError    = "usage_limit_reached"
	codexRateLimitReachedType      = "rate_limit_reached"
	codexMainRateLimitID           = "codex"
	codexActiveLimitHeader         = "X-Codex-Active-Limit"
	codexRateLimitReachedHeader    = "X-Codex-Rate-Limit-Reached-Type"
	codexResetRequestTimeout       = 10 * time.Second
	codexResetResponseBodyMaxBytes = 1 << 20
	codexConfirmedResetGuard       = 5 * time.Minute
	codexUnsafeResetFallback       = 8 * 24 * time.Hour
	codexRejectedResetSuppression  = 5 * time.Minute
)

var (
	// Reset attempts are serialized across provider instances for the same
	// ChatGPT account. Suppression retains only the latest unsafe episode per
	// account; confirmed redemptions survive a transient available/stale read.
	codexSuppressedResetEpisodes sync.Map
	codexResetLocks              = struct {
		sync.Mutex
		entries map[codexResetLockKey]*codexResetLockEntry
	}{
		entries: make(map[codexResetLockKey]*codexResetLockEntry),
	}
)

type codexResetLockKey struct {
	usageURL   string
	consumeURL string
	account    string
}

type codexResetEpisodeKey struct {
	usageURL               string
	account                string
	primaryResetAt         int64
	primaryWindowSeconds   int64
	secondaryResetAt       int64
	secondaryWindowSeconds int64
}

type codexResetEpisodeSuppression struct {
	episode             codexResetEpisodeKey
	retainWhenAvailable bool
	expiresAt           time.Time
}

type codexResetLockEntry struct {
	semaphore chan struct{}
	refs      int
}

type codexRateLimitResetter struct {
	httpClient         *http.Client
	usageURL           string
	consumeURL         string
	newRedeemRequestID func() string
}

type codexUsageStatus struct {
	RateLimit             *codexUsageRateLimit             `json:"rate_limit"`
	SpendControl          *codexUsageSpendControl          `json:"spend_control"`
	AdditionalRateLimits  []codexUsageAdditionalRateLimit  `json:"additional_rate_limits"`
	RateLimitReachedType  *codexUsageRateLimitReachedType  `json:"rate_limit_reached_type"`
	RateLimitResetCredits *codexUsageRateLimitResetCredits `json:"rate_limit_reset_credits"`
}

type codexUsageRateLimit struct {
	Allowed         *bool                 `json:"allowed"`
	LimitReached    bool                  `json:"limit_reached"`
	PrimaryWindow   *codexUsageRateWindow `json:"primary_window"`
	SecondaryWindow *codexUsageRateWindow `json:"secondary_window"`
}

type codexUsageRateWindow struct {
	UsedPercent        *float64 `json:"used_percent"`
	LimitWindowSeconds int64    `json:"limit_window_seconds"`
	ResetAt            int64    `json:"reset_at"`
}

type codexUsageSpendControl struct {
	Reached bool `json:"reached"`
}

type codexUsageAdditionalRateLimit struct {
	RateLimit *codexUsageRateLimit `json:"rate_limit"`
}

type codexUsageRateLimitReachedType struct {
	Type string `json:"type"`
}

type codexUsageRateLimitResetCredits struct {
	AvailableCount int64 `json:"available_count"`
}

type codexConsumeResetCreditRequest struct {
	RedeemRequestID string `json:"redeem_request_id"`
}

type codexConsumeResetCreditResponse struct {
	Code string `json:"code"`
}

type codexAPIErrorEnvelope struct {
	Error struct {
		Type string `json:"type"`
		Code string `json:"code"`
	} `json:"error"`
}

type codexResetHTTPStatusError struct {
	status int
}

func (e *codexResetHTTPStatusError) Error() string {
	return fmt.Sprintf("unexpected HTTP status %d", e.status)
}

func newCodexRateLimitResetter() *codexRateLimitResetter {
	return &codexRateLimitResetter{
		httpClient: &http.Client{
			Timeout: codexResetRequestTimeout,
		},
		usageURL:           codexUsageURL,
		consumeURL:         codexConsumeResetCreditURL,
		newRedeemRequestID: uuid.NewString,
	}
}

func isCodexUsageLimitReachedError(err error) bool {
	var apiErr *openai.Error
	if errors.As(err, &apiErr) {
		var headers http.Header
		if apiErr.Response != nil {
			headers = apiErr.Response.Header
		}
		return isCodexResetEligibleUsageError(
			apiErr.StatusCode,
			apiErr.Type,
			apiErr.Code,
			headers,
		)
	}
	var streamErr *codexStreamError
	if errors.As(err, &streamErr) {
		return isCodexResetEligibleUsageError(
			http.StatusTooManyRequests,
			streamErr.errorType,
			streamErr.code,
			nil,
		)
	}
	return false
}

func isCodexResetEligibleUsageError(
	status int,
	errorType string,
	errorCode string,
	headers http.Header,
) bool {
	if status != http.StatusTooManyRequests ||
		(errorType != codexUsageLimitReachedError && errorCode != codexUsageLimitReachedError) {
		return false
	}
	if activeLimit := headers.Get(codexActiveLimitHeader); activeLimit != "" &&
		activeLimit != codexMainRateLimitID {
		return false
	}
	if reachedType := headers.Get(codexRateLimitReachedHeader); reachedType != "" &&
		reachedType != codexRateLimitReachedType {
		return false
	}
	return true
}

func codexUsageLimitNoRetryMiddleware(
	req *http.Request,
	next option.MiddlewareNext,
) (*http.Response, error) {
	resp, err := next(req)
	if err != nil ||
		resp == nil ||
		resp.StatusCode != http.StatusTooManyRequests ||
		resp.Body == nil {
		return resp, err
	}

	body, readErr := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	resp.Body = io.NopCloser(bytes.NewReader(body))
	if readErr == nil {
		var envelope codexAPIErrorEnvelope
		if json.Unmarshal(body, &envelope) == nil &&
			isCodexResetEligibleUsageError(
				resp.StatusCode,
				envelope.Error.Type,
				envelope.Error.Code,
				resp.Header,
			) {
			// openai-go retries every 429 by default. This response cannot recover
			// until an earned reset is redeemed, so surface it to Chat immediately.
			if resp.Header == nil {
				resp.Header = make(http.Header)
			}
			resp.Header.Set("X-Should-Retry", "false")
		}
	}
	return resp, err
}

func codexRateLimitResetFailureReason(err error) string {
	switch {
	case errors.Is(err, context.Canceled):
		return "context_canceled"
	case errors.Is(err, context.DeadlineExceeded):
		return "deadline_exceeded"
	default:
		return "reset_unavailable"
	}
}

func (r *codexRateLimitResetter) tryReset(
	ctx context.Context,
	token string,
	accountID string,
) (bool, error) {
	if token == "" || accountID == "" {
		return false, nil
	}

	lockKey := codexResetLockKey{
		usageURL:   r.usageURL,
		consumeURL: r.consumeURL,
		account:    accountID,
	}
	releaseLock, err := acquireCodexResetLock(ctx, lockKey)
	if err != nil {
		return false, err
	}
	defer releaseLock()

	status, err := r.getUsageStatus(ctx, token, accountID)
	if err != nil {
		return false, err
	}
	if status.RateLimit == nil {
		return false, nil
	}

	mainExhausted, mainAvailable := status.mainRateLimitState()
	if mainAvailable {
		if suppression, suppressed := loadCodexResetSuppression(lockKey); suppressed {
			if !suppression.retainWhenAvailable {
				codexSuppressedResetEpisodes.Delete(lockKey)
			}
		}
		if status.hasDisallowedReachedType() ||
			status.hasSpendControlExhaustion() ||
			status.hasAdditionalExhaustion() {
			return false, nil
		}
		return true, nil
	}
	if status.hasDisallowedReachedType() || status.hasSpendControlExhaustion() {
		return false, nil
	}
	if !mainExhausted ||
		status.RateLimitResetCredits == nil ||
		status.RateLimitResetCredits.AvailableCount <= 0 {
		return false, nil
	}

	episode := status.resetEpisodeKey(r.usageURL, accountID)
	unsafeSuppression := status.unsafeResetSuppressionDuration(time.Now())
	if suppression, suppressed := loadCodexResetSuppression(lockKey); suppressed &&
		suppression.episode.overlaps(episode) {
		return false, nil
	}

	redeemRequestID := r.newRedeemRequestID()
	response, err := r.consumeResetCredit(ctx, token, accountID, redeemRequestID)
	if err != nil && shouldRetryCodexResetConsume(err) {
		response, err = r.consumeResetCredit(ctx, token, accountID, redeemRequestID)
	}
	if err != nil {
		suppressCodexResetEpisode(lockKey, episode, true, unsafeSuppression)
		return false, err
	}
	switch response.Code {
	case "reset", "already_redeemed":
		suppressCodexResetEpisode(lockKey, episode, true, unsafeSuppression)
		verified, verifyErr := r.getUsageStatus(ctx, token, accountID)
		if verifyErr != nil {
			return true, fmt.Errorf("verifying Codex usage after reset: %w", verifyErr)
		}
		_, available := verified.mainRateLimitState()
		if available {
			suppressCodexResetEpisode(lockKey, episode, true, codexConfirmedResetGuard)
			if verified.hasDisallowedReachedType() || verified.hasSpendControlExhaustion() {
				return true, errors.New("codex usage remained blocked after reset")
			}
			return true, nil
		}
		if verified.hasDisallowedReachedType() ||
			verified.hasSpendControlExhaustion() ||
			verified.hasAdditionalExhaustion() {
			return true, errors.New("codex usage remained blocked after reset")
		}
		return true, errors.New("codex main rate limit remained exhausted after reset")
	case "nothing_to_reset", "no_credit":
		suppressCodexResetEpisode(lockKey, episode, false, codexRejectedResetSuppression)
		return false, nil
	default:
		// A future or malformed 2xx outcome may still have consumed the finite
		// credit. Fail closed for the rest of this exhausted-window episode.
		suppressCodexResetEpisode(lockKey, episode, true, unsafeSuppression)
		return false, errors.New("unrecognized Codex reset outcome")
	}
}

func acquireCodexResetLock(
	ctx context.Context,
	lockKey codexResetLockKey,
) (func(), error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	codexResetLocks.Lock()
	entry := codexResetLocks.entries[lockKey]
	if entry == nil {
		entry = &codexResetLockEntry{semaphore: make(chan struct{}, 1)}
		codexResetLocks.entries[lockKey] = entry
	}
	entry.refs++
	codexResetLocks.Unlock()

	select {
	case entry.semaphore <- struct{}{}:
		return func() {
			<-entry.semaphore
			releaseCodexResetLockReference(lockKey, entry)
		}, nil
	case <-ctx.Done():
		releaseCodexResetLockReference(lockKey, entry)
		return nil, ctx.Err()
	}
}

func releaseCodexResetLockReference(
	lockKey codexResetLockKey,
	entry *codexResetLockEntry,
) {
	codexResetLocks.Lock()
	entry.refs--
	if entry.refs == 0 && codexResetLocks.entries[lockKey] == entry {
		delete(codexResetLocks.entries, lockKey)
	}
	codexResetLocks.Unlock()
}

func suppressCodexResetEpisode(
	lockKey codexResetLockKey,
	episode codexResetEpisodeKey,
	retainWhenAvailable bool,
	duration time.Duration,
) {
	now := time.Now()
	codexSuppressedResetEpisodes.Store(lockKey, codexResetEpisodeSuppression{
		episode:             episode,
		retainWhenAvailable: retainWhenAvailable,
		expiresAt:           now.Add(duration),
	})
	codexSuppressedResetEpisodes.Range(func(key, value any) bool {
		suppression, ok := value.(codexResetEpisodeSuppression)
		if !ok {
			codexSuppressedResetEpisodes.Delete(key)
		} else if !suppression.expiresAt.After(now) {
			codexSuppressedResetEpisodes.CompareAndDelete(key, value)
		}
		return true
	})
}

func loadCodexResetSuppression(
	lockKey codexResetLockKey,
) (codexResetEpisodeSuppression, bool) {
	value, ok := codexSuppressedResetEpisodes.Load(lockKey)
	if !ok {
		return codexResetEpisodeSuppression{}, false
	}
	suppression, ok := value.(codexResetEpisodeSuppression)
	if !ok {
		codexSuppressedResetEpisodes.Delete(lockKey)
		return codexResetEpisodeSuppression{}, false
	}
	if !suppression.expiresAt.After(time.Now()) {
		codexSuppressedResetEpisodes.CompareAndDelete(lockKey, value)
		return codexResetEpisodeSuppression{}, false
	}
	return suppression, true
}

func (s codexUsageStatus) resetEpisodeKey(usageURL, accountID string) codexResetEpisodeKey {
	key := codexResetEpisodeKey{
		usageURL: usageURL,
		account:  accountID,
	}
	if s.RateLimit == nil {
		return key
	}
	primaryExhausted := codexRateWindowExhausted(s.RateLimit.PrimaryWindow)
	secondaryExhausted := codexRateWindowExhausted(s.RateLimit.SecondaryWindow)
	includeAllWindows := !primaryExhausted && !secondaryExhausted
	if s.RateLimit.PrimaryWindow != nil && (includeAllWindows || primaryExhausted) {
		key.primaryResetAt = s.RateLimit.PrimaryWindow.ResetAt
		key.primaryWindowSeconds = s.RateLimit.PrimaryWindow.LimitWindowSeconds
	}
	if s.RateLimit.SecondaryWindow != nil && (includeAllWindows || secondaryExhausted) {
		key.secondaryResetAt = s.RateLimit.SecondaryWindow.ResetAt
		key.secondaryWindowSeconds = s.RateLimit.SecondaryWindow.LimitWindowSeconds
	}
	return key
}

func (s codexUsageStatus) unsafeResetSuppressionDuration(now time.Time) time.Duration {
	episode := s.resetEpisodeKey("", "")
	latestResetAt := max(episode.primaryResetAt, episode.secondaryResetAt)
	if latestResetAt > now.Unix() {
		return time.Unix(latestResetAt, 0).Sub(now) + codexConfirmedResetGuard
	}
	return codexUnsafeResetFallback
}

func (e codexResetEpisodeKey) overlaps(other codexResetEpisodeKey) bool {
	if e.usageURL != other.usageURL || e.account != other.account {
		return false
	}
	primaryPresent := e.primaryResetAt != 0 || e.primaryWindowSeconds != 0
	otherPrimaryPresent := other.primaryResetAt != 0 || other.primaryWindowSeconds != 0
	if primaryPresent &&
		otherPrimaryPresent &&
		e.primaryResetAt == other.primaryResetAt &&
		e.primaryWindowSeconds == other.primaryWindowSeconds {
		return true
	}
	secondaryPresent := e.secondaryResetAt != 0 || e.secondaryWindowSeconds != 0
	otherSecondaryPresent := other.secondaryResetAt != 0 || other.secondaryWindowSeconds != 0
	if secondaryPresent &&
		otherSecondaryPresent &&
		e.secondaryResetAt == other.secondaryResetAt &&
		e.secondaryWindowSeconds == other.secondaryWindowSeconds {
		return true
	}
	return !primaryPresent && !secondaryPresent && !otherPrimaryPresent && !otherSecondaryPresent
}

func codexRateWindowExhausted(window *codexUsageRateWindow) bool {
	return window != nil && window.UsedPercent != nil && *window.UsedPercent >= 100
}

func (s codexUsageStatus) mainRateLimitState() (exhausted bool, available bool) {
	if s.RateLimit == nil {
		return false, false
	}
	if s.RateLimit.LimitReached {
		return true, false
	}
	if s.RateLimit.Allowed == nil {
		return false, false
	}
	return !*s.RateLimit.Allowed, *s.RateLimit.Allowed
}

func (s codexUsageStatus) hasDisallowedReachedType() bool {
	if s.RateLimitReachedType != nil &&
		s.RateLimitReachedType.Type != "" &&
		s.RateLimitReachedType.Type != codexRateLimitReachedType {
		return true
	}
	return false
}

func (s codexUsageStatus) hasSpendControlExhaustion() bool {
	return s.SpendControl != nil && s.SpendControl.Reached
}

func (s codexUsageStatus) hasAdditionalExhaustion() bool {
	for _, additional := range s.AdditionalRateLimits {
		if additional.RateLimit == nil {
			continue
		}
		exhausted := additional.RateLimit.LimitReached
		if additional.RateLimit.Allowed != nil {
			exhausted = exhausted || !*additional.RateLimit.Allowed
		}
		if exhausted {
			return true
		}
	}
	return false
}

func (r *codexRateLimitResetter) getUsageStatus(
	ctx context.Context,
	token string,
	accountID string,
) (codexUsageStatus, error) {
	var status codexUsageStatus
	if err := r.doJSON(ctx, http.MethodGet, r.usageURL, token, accountID, nil, &status); err != nil {
		return codexUsageStatus{}, fmt.Errorf("checking Codex usage: %w", err)
	}
	return status, nil
}

func (r *codexRateLimitResetter) consumeResetCredit(
	ctx context.Context,
	token string,
	accountID string,
	redeemRequestID string,
) (codexConsumeResetCreditResponse, error) {
	body, err := json.Marshal(codexConsumeResetCreditRequest{
		RedeemRequestID: redeemRequestID,
	})
	if err != nil {
		return codexConsumeResetCreditResponse{}, fmt.Errorf("encoding Codex reset request: %w", err)
	}

	var response codexConsumeResetCreditResponse
	if err := r.doJSON(
		ctx,
		http.MethodPost,
		r.consumeURL,
		token,
		accountID,
		body,
		&response,
	); err != nil {
		return codexConsumeResetCreditResponse{}, fmt.Errorf("consuming Codex reset credit: %w", err)
	}
	return response, nil
}

func shouldRetryCodexResetConsume(err error) bool {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	var statusErr *codexResetHTTPStatusError
	if !errors.As(err, &statusErr) {
		// A transport or decode failure can be ambiguous after the server has
		// received the request, so retry once with the same idempotency key.
		return true
	}
	return statusErr.status == http.StatusRequestTimeout ||
		statusErr.status == http.StatusConflict ||
		statusErr.status == http.StatusTooManyRequests ||
		statusErr.status >= http.StatusInternalServerError
}

func (r *codexRateLimitResetter) doJSON(
	ctx context.Context,
	method string,
	url string,
	token string,
	accountID string,
	body []byte,
	dst any,
) error {
	var requestBody io.Reader
	if body != nil {
		requestBody = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, url, requestBody)
	if err != nil {
		return fmt.Errorf("creating request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Chatgpt-Account-Id", accountID)
	req.Header.Set("User-Agent", "codex-cli")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := r.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("sending request: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, codexResetResponseBodyMaxBytes))
		return &codexResetHTTPStatusError{status: resp.StatusCode}
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, codexResetResponseBodyMaxBytes)).Decode(dst); err != nil {
		return fmt.Errorf("decoding response: %w", err)
	}
	return nil
}
