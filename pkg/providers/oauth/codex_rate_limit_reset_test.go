package oauthprovider

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"
)

const codexResetTestRedeemID = "d0c4c978-8f25-4a0d-a863-a92c04f6685c"

var codexResetTestEndpointSequence atomic.Uint64

func TestIsCodexUsageLimitReachedError(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{
			name: "exact type",
			err: &openai.Error{
				StatusCode: http.StatusTooManyRequests,
				Type:       codexUsageLimitReachedError,
			},
			want: true,
		},
		{
			name: "exact code",
			err: &openai.Error{
				StatusCode: http.StatusTooManyRequests,
				Code:       codexUsageLimitReachedError,
			},
			want: true,
		},
		{
			name: "main limit headers",
			err: &openai.Error{
				StatusCode: http.StatusTooManyRequests,
				Type:       codexUsageLimitReachedError,
				Response: &http.Response{Header: http.Header{
					"X-Codex-Active-Limit":            []string{codexMainRateLimitID},
					"X-Codex-Rate-Limit-Reached-Type": []string{codexRateLimitReachedType},
				}},
			},
			want: true,
		},
		{
			name: "additional active limit",
			err: &openai.Error{
				StatusCode: http.StatusTooManyRequests,
				Type:       codexUsageLimitReachedError,
				Response: &http.Response{Header: http.Header{
					"X-Codex-Active-Limit": []string{"codex_other"},
				}},
			},
		},
		{
			name: "workspace reached type",
			err: &openai.Error{
				StatusCode: http.StatusTooManyRequests,
				Type:       codexUsageLimitReachedError,
				Response: &http.Response{Header: http.Header{
					"X-Codex-Rate-Limit-Reached-Type": []string{"workspace_member_usage_limit_reached"},
				}},
			},
		},
		{
			name: "wrapped exact error",
			err: fmt.Errorf("wrapped: %w", &openai.Error{
				StatusCode: http.StatusTooManyRequests,
				Type:       codexUsageLimitReachedError,
			}),
			want: true,
		},
		{
			name: "generic 429",
			err: &openai.Error{
				StatusCode: http.StatusTooManyRequests,
				Type:       "rate_limit_error",
				Code:       "rate_limit_exceeded",
			},
		},
		{
			name: "wrong status",
			err: &openai.Error{
				StatusCode: http.StatusBadRequest,
				Type:       codexUsageLimitReachedError,
			},
		},
		{
			name: "non API error",
			err:  fmt.Errorf("usage_limit_reached"),
		},
		{
			name: "stream error code",
			err: &codexStreamError{
				code: codexUsageLimitReachedError,
			},
			want: true,
		},
		{
			name: "generic stream error",
			err: &codexStreamError{
				code: "rate_limit_exceeded",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isCodexUsageLimitReachedError(tt.err); got != tt.want {
				t.Fatalf("isCodexUsageLimitReachedError() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestCodexResetLockRegistryReleasesUnusedEntry(t *testing.T) {
	lockKey := codexResetLockKey{
		usageURL:   "https://lock-registry.test/usage",
		consumeURL: "https://lock-registry.test/consume",
		account:    "account",
	}
	release, err := acquireCodexResetLock(t.Context(), lockKey)
	if err != nil {
		t.Fatalf("acquireCodexResetLock() error: %v", err)
	}

	codexResetLocks.Lock()
	entry := codexResetLocks.entries[lockKey]
	refs := 0
	if entry != nil {
		refs = entry.refs
	}
	codexResetLocks.Unlock()
	if entry == nil || refs != 1 {
		t.Fatalf("lock entry = %#v with %d refs, want one reference", entry, refs)
	}

	release()
	codexResetLocks.Lock()
	_, exists := codexResetLocks.entries[lockKey]
	codexResetLocks.Unlock()
	if exists {
		t.Fatal("unused lock entry was not released")
	}
}

func TestCodexResetLockAcquisitionHonorsCancellation(t *testing.T) {
	lockKey := codexResetLockKey{
		usageURL:   "https://lock-cancellation.test/usage",
		consumeURL: "https://lock-cancellation.test/consume",
		account:    "account",
	}
	release, err := acquireCodexResetLock(t.Context(), lockKey)
	if err != nil {
		t.Fatalf("first acquireCodexResetLock() error: %v", err)
	}

	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if secondRelease, acquireErr := acquireCodexResetLock(ctx, lockKey); !errors.Is(
		acquireErr,
		context.Canceled,
	) {
		if secondRelease != nil {
			secondRelease()
		}
		t.Fatalf("second acquire error = %v, want context canceled", acquireErr)
	}
	release()

	codexResetLocks.Lock()
	_, exists := codexResetLocks.entries[lockKey]
	codexResetLocks.Unlock()
	if exists {
		t.Fatal("canceled waiter left a lock entry behind")
	}
}

func TestCodexResetEpisodeTracksOnlyExhaustedWindows(t *testing.T) {
	exhausted := 100.0
	available := 50.0
	status := codexUsageStatus{
		RateLimit: &codexUsageRateLimit{
			PrimaryWindow: &codexUsageRateWindow{
				UsedPercent:        &exhausted,
				LimitWindowSeconds: 18_000,
				ResetAt:            2_000,
			},
			SecondaryWindow: &codexUsageRateWindow{
				UsedPercent:        &available,
				LimitWindowSeconds: 604_800,
				ResetAt:            5_000,
			},
		},
	}
	episode := status.resetEpisodeKey("usage", "account")
	status.RateLimit.SecondaryWindow.ResetAt++
	if got := status.resetEpisodeKey("usage", "account"); got != episode {
		t.Fatalf("unrelated window rollover changed episode: got %#v, want %#v", got, episode)
	}
	status.RateLimit.PrimaryWindow.ResetAt++
	if got := status.resetEpisodeKey("usage", "account"); got == episode {
		t.Fatalf("exhausted window rollover did not change episode: %#v", got)
	}

	now := time.Unix(1_000, 0)
	got := status.unsafeResetSuppressionDuration(now)
	want := 1_001*time.Second + codexConfirmedResetGuard
	if got != want {
		t.Fatalf("unsafe suppression = %v, want %v", got, want)
	}

	bothWindows := codexResetEpisodeKey{
		usageURL:               "usage",
		account:                "account",
		primaryResetAt:         2_000,
		primaryWindowSeconds:   18_000,
		secondaryResetAt:       5_000,
		secondaryWindowSeconds: 604_800,
	}
	secondaryStillExhausted := codexResetEpisodeKey{
		usageURL:               "usage",
		account:                "account",
		secondaryResetAt:       5_000,
		secondaryWindowSeconds: 604_800,
	}
	if !bothWindows.overlaps(secondaryStillExhausted) {
		t.Fatal("episode lost suppression while one exhausted window remained")
	}
	if bothWindows.overlaps(codexResetEpisodeKey{
		usageURL:             "usage",
		account:              "account",
		primaryResetAt:       2_001,
		primaryWindowSeconds: 18_000,
	}) {
		t.Fatal("episode overlapped after every exhausted window changed")
	}
}

func TestCodexProviderAutoResetsUsageLimitAndRetries(t *testing.T) {
	for _, consumeCode := range []string{"reset", "already_redeemed"} {
		t.Run(consumeCode, func(t *testing.T) {
			var responseRequests atomic.Int32
			var usageRequests atomic.Int32
			var consumeRequests atomic.Int32
			var retryCounts []string

			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/responses":
					assertCodexResetAuthHeaders(t, r, "fresh-token", "fresh-account")
					retryCounts = append(retryCounts, r.Header.Get("X-Stainless-Retry-Count"))
					if responseRequests.Add(1) == 1 {
						writeCodexAPIError(
							w,
							http.StatusTooManyRequests,
							codexUsageLimitReachedError,
							"",
						)
						return
					}
					writeCodexResetTestSuccess(w)
				case "/backend-api/wham/usage":
					assertCodexResetAuthHeaders(t, r, "fresh-token", "fresh-account")
					if r.Method != http.MethodGet {
						t.Errorf("usage method = %s, want GET", r.Method)
					}
					if usageRequests.Add(1) == 1 {
						writeCodexUsageResponse(w, codexResetTestUsage(true, 1))
						return
					}
					writeCodexUsageResponse(w, codexResetTestUsage(false, 0))
				case "/backend-api/wham/rate-limit-reset-credits/consume":
					assertCodexResetAuthHeaders(t, r, "fresh-token", "fresh-account")
					if r.Method != http.MethodPost {
						t.Errorf("consume method = %s, want POST", r.Method)
					}
					if got := r.Header.Get("Content-Type"); got != "application/json" {
						t.Errorf("Content-Type = %q, want application/json", got)
					}
					assertCodexResetPayload(t, r, codexResetTestRedeemID)
					consumeRequests.Add(1)
					writeCodexUsageResponse(w, map[string]any{
						"code":          consumeCode,
						"windows_reset": 2,
					})
				default:
					t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
					http.NotFound(w, r)
				}
			}))
			defer server.Close()

			provider := NewCodexProviderWithTokenSource(
				"stale-token",
				"stale-account",
				func() (string, string, error) {
					return "fresh-token", "fresh-account", nil
				},
			)
			provider.client = newCodexOpenAIClient(
				"stale-token",
				"stale-account",
				optionWithCodexResetTestBaseURL(server.URL),
			)
			configureCodexResetTestURLs(provider, server.URL)

			resp, err := provider.Chat(
				t.Context(),
				[]Message{{Role: "user", Content: "Hello"}},
				nil,
				"gpt-5.3-codex",
				map[string]any{},
			)
			if err != nil {
				t.Fatalf("Chat() error: %v", err)
			}
			if resp.Content != "reset succeeded" {
				t.Fatalf("Chat() content = %q, want reset succeeded", resp.Content)
			}
			if got := responseRequests.Load(); got != 2 {
				t.Fatalf("response requests = %d, want 2", got)
			}
			if want := []string{"0", "0"}; !slices.Equal(retryCounts, want) {
				t.Fatalf("SDK retry counts = %#v, want %#v", retryCounts, want)
			}
			if got := usageRequests.Load(); got != 2 {
				t.Fatalf("usage requests = %d, want 2", got)
			}
			if got := consumeRequests.Load(); got != 1 {
				t.Fatalf("consume requests = %d, want 1", got)
			}
		})
	}
}

func TestCodexProviderAutoResetsStreamUsageLimitFailures(t *testing.T) {
	tests := []struct {
		name         string
		writeFailure func(http.ResponseWriter)
	}{
		{name: "response failed", writeFailure: writeCodexResponseFailedSSE},
		{name: "top-level error event", writeFailure: writeCodexErrorEventSSE},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var responseRequests atomic.Int32
			var usageRequests atomic.Int32
			var consumeRequests atomic.Int32

			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/responses":
					if responseRequests.Add(1) == 1 {
						tt.writeFailure(w)
						return
					}
					writeCodexResetTestSuccess(w)
				case "/backend-api/wham/usage":
					if usageRequests.Add(1) == 1 {
						writeCodexUsageResponse(w, codexResetTestUsage(true, 1))
						return
					}
					writeCodexUsageResponse(w, codexResetTestUsage(false, 0))
				case "/backend-api/wham/rate-limit-reset-credits/consume":
					consumeRequests.Add(1)
					writeCodexUsageResponse(w, map[string]any{"code": "reset"})
				default:
					http.NotFound(w, r)
				}
			}))
			defer server.Close()

			provider := newCodexResetTestProvider(server.URL, "token", "account")
			resp, err := provider.Chat(
				t.Context(),
				[]Message{{Role: "user", Content: "Hello"}},
				nil,
				"gpt-5.3-codex",
				map[string]any{},
			)
			if err != nil {
				t.Fatalf("Chat() error: %v", err)
			}
			if resp.Content != "reset succeeded" {
				t.Fatalf("Chat() content = %q, want reset success", resp.Content)
			}
			if got := responseRequests.Load(); got != 2 {
				t.Fatalf("response requests = %d, want 2", got)
			}
			if got := usageRequests.Load(); got != 2 {
				t.Fatalf("usage requests = %d, want 2", got)
			}
			if got := consumeRequests.Load(); got != 1 {
				t.Fatalf("consume requests = %d, want 1", got)
			}
		})
	}
}

func TestCodexProviderRetriesWhenConcurrentResetAlreadyClearedLimit(t *testing.T) {
	var responseRequests atomic.Int32
	var usageRequests atomic.Int32
	var consumeRequests atomic.Int32

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/responses":
			if responseRequests.Add(1) == 1 {
				writeCodexAPIError(w, http.StatusTooManyRequests, codexUsageLimitReachedError, "")
				return
			}
			writeCodexResetTestSuccess(w)
		case "/backend-api/wham/usage":
			usageRequests.Add(1)
			writeCodexUsageResponse(w, codexResetTestUsage(false, 1))
		case "/backend-api/wham/rate-limit-reset-credits/consume":
			consumeRequests.Add(1)
			http.Error(w, "must not consume", http.StatusInternalServerError)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	provider := newCodexResetTestProvider(server.URL, "token", "account")
	resp, err := provider.Chat(
		t.Context(),
		[]Message{{Role: "user", Content: "Hello"}},
		nil,
		"gpt-5.3-codex",
		map[string]any{},
	)
	if err != nil {
		t.Fatalf("Chat() error: %v", err)
	}
	if resp.Content != "reset succeeded" {
		t.Fatalf("Chat() content = %q, want reset succeeded", resp.Content)
	}
	if got := responseRequests.Load(); got != 2 {
		t.Fatalf("response requests = %d, want 2", got)
	}
	if got := usageRequests.Load(); got != 1 {
		t.Fatalf("usage requests = %d, want 1", got)
	}
	if got := consumeRequests.Load(); got != 0 {
		t.Fatalf("consume requests = %d, want 0", got)
	}
}

func TestCodexProviderPreservesSDKRetryForGenericRateLimit(t *testing.T) {
	var responseRequests atomic.Int32
	var resetRequests atomic.Int32
	var retryCounts []string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/responses":
			retryCounts = append(retryCounts, r.Header.Get("X-Stainless-Retry-Count"))
			if responseRequests.Add(1) == 1 {
				w.Header().Set("Retry-After-Ms", "0")
				writeCodexAPIError(
					w,
					http.StatusTooManyRequests,
					"rate_limit_error",
					"rate_limit_exceeded",
				)
				return
			}
			writeCodexResetTestSuccess(w)
		default:
			resetRequests.Add(1)
			http.Error(w, "must not inspect or reset usage", http.StatusInternalServerError)
		}
	}))
	defer server.Close()

	provider := newCodexResetTestProvider(server.URL, "token", "account")
	resp, err := provider.Chat(
		t.Context(),
		[]Message{{Role: "user", Content: "Hello"}},
		nil,
		"gpt-5.3-codex",
		map[string]any{},
	)
	if err != nil {
		t.Fatalf("Chat() error: %v", err)
	}
	if resp.Content != "reset succeeded" {
		t.Fatalf("Chat() content = %q, want SDK retry success", resp.Content)
	}
	if got := responseRequests.Load(); got != 2 {
		t.Fatalf("response requests = %d, want 2", got)
	}
	if want := []string{"0", "1"}; !slices.Equal(retryCounts, want) {
		t.Fatalf("SDK retry counts = %#v, want %#v", retryCounts, want)
	}
	if got := resetRequests.Load(); got != 0 {
		t.Fatalf("reset requests = %d, want 0", got)
	}
}

func TestCodexProviderDoesNotAutoResetDisallowedUsageHeaders(t *testing.T) {
	tests := []struct {
		name        string
		headerName  string
		headerValue string
	}{
		{
			name:        "additional active limit",
			headerName:  codexActiveLimitHeader,
			headerValue: "codex_other",
		},
		{
			name:        "workspace reached type",
			headerName:  codexRateLimitReachedHeader,
			headerValue: "workspace_member_usage_limit_reached",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var responseRequests atomic.Int32
			var resetRequests atomic.Int32
			var retryCounts []string

			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/responses":
					retryCounts = append(retryCounts, r.Header.Get("X-Stainless-Retry-Count"))
					if responseRequests.Add(1) == 1 {
						w.Header().Set(tt.headerName, tt.headerValue)
						w.Header().Set("Retry-After-Ms", "0")
						writeCodexAPIError(
							w,
							http.StatusTooManyRequests,
							codexUsageLimitReachedError,
							"",
						)
						return
					}
					writeCodexResetTestSuccess(w)
				default:
					resetRequests.Add(1)
					http.Error(w, "must not inspect or reset usage", http.StatusInternalServerError)
				}
			}))
			defer server.Close()

			provider := newCodexResetTestProvider(server.URL, "token", "account")
			resp, err := provider.Chat(
				t.Context(),
				[]Message{{Role: "user", Content: "Hello"}},
				nil,
				"gpt-5.3-codex",
				map[string]any{},
			)
			if err != nil {
				t.Fatalf("Chat() error: %v", err)
			}
			if resp.Content != "reset succeeded" {
				t.Fatalf("Chat() content = %q, want SDK retry success", resp.Content)
			}
			if got := responseRequests.Load(); got != 2 {
				t.Fatalf("response requests = %d, want 2", got)
			}
			if want := []string{"0", "1"}; !slices.Equal(retryCounts, want) {
				t.Fatalf("SDK retry counts = %#v, want %#v", retryCounts, want)
			}
			if got := resetRequests.Load(); got != 0 {
				t.Fatalf("reset requests = %d, want 0", got)
			}
		})
	}
}

func TestCodexProviderDoesNotAutoResetWithoutChatGPTAccount(t *testing.T) {
	var responseRequests atomic.Int32
	var resetRequests atomic.Int32

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/responses":
			responseRequests.Add(1)
			writeCodexAPIError(w, http.StatusTooManyRequests, codexUsageLimitReachedError, "")
		default:
			resetRequests.Add(1)
			http.Error(w, "must not inspect or reset usage", http.StatusInternalServerError)
		}
	}))
	defer server.Close()

	provider := newCodexResetTestProvider(server.URL, "token", "")
	_, err := provider.Chat(
		t.Context(),
		[]Message{{Role: "user", Content: "Hello"}},
		nil,
		"gpt-5.3-codex",
		map[string]any{},
	)
	if err == nil || !strings.Contains(err.Error(), "codex API call") {
		t.Fatalf("Chat() error = %v, want original Codex API error", err)
	}
	if got := responseRequests.Load(); got != 1 {
		t.Fatalf("response requests = %d, want 1", got)
	}
	if got := resetRequests.Load(); got != 0 {
		t.Fatalf("reset requests = %d, want 0", got)
	}
}

func TestCodexProviderPropagatesCancellationDuringAutoReset(t *testing.T) {
	var responseRequests atomic.Int32
	var usageRequests atomic.Int32
	var consumeRequests atomic.Int32
	ctx, cancel := context.WithCancel(t.Context())

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/responses":
			responseRequests.Add(1)
			writeCodexAPIError(w, http.StatusTooManyRequests, codexUsageLimitReachedError, "")
		case "/backend-api/wham/usage":
			usageRequests.Add(1)
			cancel()
			<-r.Context().Done()
		case "/backend-api/wham/rate-limit-reset-credits/consume":
			consumeRequests.Add(1)
			http.Error(w, "must not consume", http.StatusInternalServerError)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	provider := newCodexResetTestProvider(server.URL, "token", "account")
	_, err := provider.Chat(
		ctx,
		[]Message{{Role: "user", Content: "Hello"}},
		nil,
		"gpt-5.3-codex",
		map[string]any{},
	)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Chat() error = %v, want context canceled", err)
	}
	if got := responseRequests.Load(); got != 1 {
		t.Fatalf("response requests = %d, want 1", got)
	}
	if got := usageRequests.Load(); got != 1 {
		t.Fatalf("usage requests = %d, want 1", got)
	}
	if got := consumeRequests.Load(); got != 0 {
		t.Fatalf("consume requests = %d, want 0", got)
	}
}

func TestCodexProviderDoesNotConsumeForIneligibleUsage(t *testing.T) {
	tests := []struct {
		name  string
		usage map[string]any
	}{
		{
			name:  "zero credits",
			usage: codexResetTestUsage(true, 0),
		},
		{
			name: "missing credits",
			usage: func() map[string]any {
				usage := codexResetTestUsage(true, 1)
				delete(usage, "rate_limit_reset_credits")
				return usage
			}(),
		},
		{
			name: "spend control only",
			usage: func() map[string]any {
				usage := codexResetTestUsage(false, 2)
				usage["spend_control"] = map[string]any{"reached": true}
				return usage
			}(),
		},
		{
			name: "spend control blocks exhausted main limit",
			usage: func() map[string]any {
				usage := codexResetTestUsage(true, 2)
				usage["spend_control"] = map[string]any{"reached": true}
				return usage
			}(),
		},
		{
			name: "workspace usage limit type",
			usage: func() map[string]any {
				usage := codexResetTestUsage(true, 2)
				usage["rate_limit_reached_type"] = map[string]any{
					"type": "workspace_member_usage_limit_reached",
				}
				return usage
			}(),
		},
		{
			name: "workspace credits type",
			usage: func() map[string]any {
				usage := codexResetTestUsage(true, 2)
				usage["rate_limit_reached_type"] = map[string]any{
					"type": "workspace_owner_credits_depleted",
				}
				return usage
			}(),
		},
		{
			name: "additional model exhausted",
			usage: func() map[string]any {
				usage := codexResetTestUsage(false, 2)
				usage["additional_rate_limits"] = []map[string]any{
					{
						"limit_name": "codex_other",
						"rate_limit": map[string]any{
							"allowed":       false,
							"limit_reached": true,
						},
					},
				}
				return usage
			}(),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var responseRequests atomic.Int32
			var usageRequests atomic.Int32
			var consumeRequests atomic.Int32

			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/responses":
					responseRequests.Add(1)
					writeCodexAPIError(w, http.StatusTooManyRequests, codexUsageLimitReachedError, "")
				case "/backend-api/wham/usage":
					usageRequests.Add(1)
					writeCodexUsageResponse(w, tt.usage)
				case "/backend-api/wham/rate-limit-reset-credits/consume":
					consumeRequests.Add(1)
					http.Error(w, "must not consume", http.StatusInternalServerError)
				default:
					http.NotFound(w, r)
				}
			}))
			defer server.Close()

			provider := newCodexResetTestProvider(server.URL, "token", "account")
			_, err := provider.Chat(
				t.Context(),
				[]Message{{Role: "user", Content: "Hello"}},
				nil,
				"gpt-5.3-codex",
				map[string]any{},
			)
			if err == nil || !strings.Contains(err.Error(), "codex API call") {
				t.Fatalf("Chat() error = %v, want original Codex API error", err)
			}
			if got := responseRequests.Load(); got != 1 {
				t.Fatalf("response requests = %d, want 1", got)
			}
			if got := usageRequests.Load(); got != 1 {
				t.Fatalf("usage requests = %d, want 1", got)
			}
			if got := consumeRequests.Load(); got != 0 {
				t.Fatalf("consume requests = %d, want 0", got)
			}
		})
	}
}

func TestCodexRateLimitResetterAllowsMainExhaustionWithSecondarySignals(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(map[string]any)
	}{
		{
			name: "additional model also exhausted",
			mutate: func(usage map[string]any) {
				usage["additional_rate_limits"] = []map[string]any{
					{
						"limit_name": "codex_other",
						"rate_limit": map[string]any{
							"allowed":       false,
							"limit_reached": true,
						},
					},
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var usageRequests atomic.Int32
			var consumeRequests atomic.Int32
			exhausted := codexResetTestUsage(true, 1)
			tt.mutate(exhausted)
			available := codexResetTestUsage(false, 0)
			tt.mutate(available)

			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/backend-api/wham/usage":
					if usageRequests.Add(1) == 1 {
						writeCodexUsageResponse(w, exhausted)
						return
					}
					writeCodexUsageResponse(w, available)
				case "/backend-api/wham/rate-limit-reset-credits/consume":
					consumeRequests.Add(1)
					writeCodexUsageResponse(w, map[string]any{"code": "reset"})
				default:
					http.NotFound(w, r)
				}
			}))
			defer server.Close()

			resetter := newCodexRateLimitResetter()
			configureCodexResetterTestURLs(resetter, server.URL)
			shouldRetry, err := resetter.tryReset(t.Context(), "token", "account")
			if err != nil {
				t.Fatalf("tryReset() error: %v", err)
			}
			if !shouldRetry {
				t.Fatal("tryReset() should retry after resetting exhausted main limit")
			}
			if got := consumeRequests.Load(); got != 1 {
				t.Fatalf("consume requests = %d, want 1", got)
			}
		})
	}
}

func TestCodexRateLimitResetterReusesIDAfterAmbiguousConsumeFailure(t *testing.T) {
	var usageRequests atomic.Int32
	var consumeRequests atomic.Int32
	var redeemIDRequests atomic.Int32
	redeemIDs := make([]string, 0, 2)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/backend-api/wham/usage":
			if usageRequests.Add(1) == 1 {
				writeCodexUsageResponse(w, codexResetTestUsage(true, 1))
				return
			}
			writeCodexUsageResponse(w, codexResetTestUsage(false, 0))
		case "/backend-api/wham/rate-limit-reset-credits/consume":
			var payload codexConsumeResetCreditRequest
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				t.Errorf("decode consume payload: %v", err)
				http.Error(w, "bad payload", http.StatusBadRequest)
				return
			}
			redeemIDs = append(redeemIDs, payload.RedeemRequestID)
			if consumeRequests.Add(1) == 1 {
				http.Error(w, "ambiguous gateway failure", http.StatusBadGateway)
				return
			}
			writeCodexUsageResponse(w, map[string]any{"code": "already_redeemed"})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	resetter := newCodexRateLimitResetter()
	configureCodexResetterTestURLs(resetter, server.URL)
	resetter.newRedeemRequestID = func() string {
		return fmt.Sprintf("redeem-%d", redeemIDRequests.Add(1))
	}

	shouldRetry, err := resetter.tryReset(t.Context(), "token", "account")
	if err != nil || !shouldRetry {
		t.Fatalf("tryReset() = (%v, %v), want successful retry", shouldRetry, err)
	}
	if got := redeemIDRequests.Load(); got != 1 {
		t.Fatalf("redeem ID generations = %d, want 1", got)
	}
	if got := consumeRequests.Load(); got != 2 {
		t.Fatalf("consume requests = %d, want 2", got)
	}
	if len(redeemIDs) != 2 || redeemIDs[0] != redeemIDs[1] {
		t.Fatalf("redeem IDs = %#v, want the same ID twice", redeemIDs)
	}
}

func TestCodexRateLimitResetterSuppressesEpisodeAfterAmbiguousConsumeFailures(t *testing.T) {
	var usageRequests atomic.Int32
	var consumeRequests atomic.Int32

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/backend-api/wham/usage":
			usageRequests.Add(1)
			writeCodexUsageResponse(w, codexResetTestUsage(true, 1))
		case "/backend-api/wham/rate-limit-reset-credits/consume":
			consumeRequests.Add(1)
			http.Error(w, "ambiguous gateway failure", http.StatusBadGateway)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	resetter := newCodexRateLimitResetter()
	configureCodexResetterTestURLs(resetter, server.URL)
	shouldRetry, err := resetter.tryReset(t.Context(), "token", "account")
	if err == nil || shouldRetry {
		t.Fatalf("first tryReset() = (%v, %v), want ambiguous failure", shouldRetry, err)
	}
	shouldRetry, err = resetter.tryReset(t.Context(), "token", "account")
	if err != nil || shouldRetry {
		t.Fatalf("second tryReset() = (%v, %v), want suppressed episode", shouldRetry, err)
	}
	if got := usageRequests.Load(); got != 2 {
		t.Fatalf("usage requests = %d, want 2", got)
	}
	if got := consumeRequests.Load(); got != 2 {
		t.Fatalf("consume requests = %d, want one same-ID replay and no later consume", got)
	}
}

func TestCodexRateLimitResetterDoesNotRetryPermanentConsumeFailure(t *testing.T) {
	var consumeRequests atomic.Int32

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/backend-api/wham/usage":
			writeCodexUsageResponse(w, codexResetTestUsage(true, 1))
		case "/backend-api/wham/rate-limit-reset-credits/consume":
			consumeRequests.Add(1)
			http.Error(w, "not authorized", http.StatusForbidden)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	resetter := newCodexRateLimitResetter()
	configureCodexResetterTestURLs(resetter, server.URL)
	shouldRetry, err := resetter.tryReset(t.Context(), "token", "account")
	if err == nil || shouldRetry {
		t.Fatalf("tryReset() = (%v, %v), want permanent consume failure", shouldRetry, err)
	}
	if got := consumeRequests.Load(); got != 1 {
		t.Fatalf("consume requests = %d, want 1", got)
	}
}

func TestCodexProviderRetriesChatExactlyOnceAfterReset(t *testing.T) {
	var responseRequests atomic.Int32
	var usageRequests atomic.Int32
	var consumeRequests atomic.Int32

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/responses":
			responseRequests.Add(1)
			writeCodexAPIError(w, http.StatusTooManyRequests, codexUsageLimitReachedError, "")
		case "/backend-api/wham/usage":
			if usageRequests.Add(1) == 1 {
				writeCodexUsageResponse(w, codexResetTestUsage(true, 1))
				return
			}
			writeCodexUsageResponse(w, codexResetTestUsage(false, 0))
		case "/backend-api/wham/rate-limit-reset-credits/consume":
			consumeRequests.Add(1)
			writeCodexUsageResponse(w, map[string]any{"code": "reset"})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	provider := newCodexResetTestProvider(server.URL, "token", "account")
	_, err := provider.Chat(
		t.Context(),
		[]Message{{Role: "user", Content: "Hello"}},
		nil,
		"gpt-5.3-codex",
		map[string]any{},
	)
	if err == nil || !strings.Contains(err.Error(), "codex API call") {
		t.Fatalf("Chat() error = %v, want second Codex API error", err)
	}
	if got := responseRequests.Load(); got != 2 {
		t.Fatalf("response requests = %d, want exactly 2", got)
	}
	if got := usageRequests.Load(); got != 2 {
		t.Fatalf("usage requests = %d, want 2", got)
	}
	if got := consumeRequests.Load(); got != 1 {
		t.Fatalf("consume requests = %d, want 1", got)
	}
}

func TestCodexProviderSerializesConcurrentResetAttempts(t *testing.T) {
	var responseRequests atomic.Int32
	var usageRequests atomic.Int32
	var consumeRequests atomic.Int32
	var resetComplete atomic.Bool
	initialRequestsReady := make(chan struct{})

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/responses":
			requestNumber := responseRequests.Add(1)
			if requestNumber <= 2 {
				if requestNumber == 2 {
					close(initialRequestsReady)
				}
				<-initialRequestsReady
				writeCodexAPIError(w, http.StatusTooManyRequests, codexUsageLimitReachedError, "")
				return
			}
			writeCodexResetTestSuccess(w)
		case "/backend-api/wham/usage":
			usageRequests.Add(1)
			if resetComplete.Load() {
				writeCodexUsageResponse(w, codexResetTestUsage(false, 0))
				return
			}
			writeCodexUsageResponse(w, codexResetTestUsage(true, 1))
		case "/backend-api/wham/rate-limit-reset-credits/consume":
			consumeRequests.Add(1)
			resetComplete.Store(true)
			writeCodexUsageResponse(w, map[string]any{"code": "reset"})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	provider := newCodexResetTestProvider(server.URL, "token", "account")
	errs := make(chan error, 2)
	var calls sync.WaitGroup
	for range 2 {
		calls.Add(1)
		go func() {
			defer calls.Done()
			resp, err := provider.Chat(
				t.Context(),
				[]Message{{Role: "user", Content: "Hello"}},
				nil,
				"gpt-5.3-codex",
				map[string]any{},
			)
			if err == nil && resp.Content != "reset succeeded" {
				err = fmt.Errorf("content = %q, want reset succeeded", resp.Content)
			}
			errs <- err
		}()
	}
	calls.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent Chat() error: %v", err)
		}
	}

	if got := responseRequests.Load(); got != 4 {
		t.Fatalf("response requests = %d, want 4", got)
	}
	if got := usageRequests.Load(); got != 3 {
		t.Fatalf("usage requests = %d, want 3", got)
	}
	if got := consumeRequests.Load(); got != 1 {
		t.Fatalf("consume requests = %d, want 1", got)
	}
}

func TestCodexRateLimitResetterSuppressesNonSuccessConsumeOutcome(t *testing.T) {
	for _, outcome := range []string{"nothing_to_reset", "no_credit"} {
		t.Run(outcome, func(t *testing.T) {
			var usageRequests atomic.Int32
			var consumeRequests atomic.Int32

			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/backend-api/wham/usage":
					usageRequests.Add(1)
					writeCodexUsageResponse(w, codexResetTestUsage(true, 1))
				case "/backend-api/wham/rate-limit-reset-credits/consume":
					consumeRequests.Add(1)
					writeCodexUsageResponse(w, map[string]any{"code": outcome})
				default:
					http.NotFound(w, r)
				}
			}))
			defer server.Close()

			resetter := newCodexRateLimitResetter()
			configureCodexResetterTestURLs(resetter, server.URL)
			for attempt := 1; attempt <= 2; attempt++ {
				shouldRetry, err := resetter.tryReset(t.Context(), "token", "account")
				if err != nil {
					t.Fatalf("tryReset() attempt %d error: %v", attempt, err)
				}
				if shouldRetry {
					t.Fatalf("tryReset() attempt %d = retry, want original error", attempt)
				}
			}
			if got := usageRequests.Load(); got != 2 {
				t.Fatalf("usage requests = %d, want 2", got)
			}
			if got := consumeRequests.Load(); got != 1 {
				t.Fatalf("consume requests = %d, want 1", got)
			}
		})
	}
}

func TestCodexRateLimitResetterFailsClosedForUnknownConsumeOutcome(t *testing.T) {
	var usageRequests atomic.Int32
	var consumeRequests atomic.Int32
	usageResponses := []map[string]any{
		codexResetTestUsage(true, 1),
		codexResetTestUsage(false, 0),
		codexResetTestUsage(true, 1),
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/backend-api/wham/usage":
			request := int(usageRequests.Add(1))
			writeCodexUsageResponse(w, usageResponses[request-1])
		case "/backend-api/wham/rate-limit-reset-credits/consume":
			consumeRequests.Add(1)
			writeCodexUsageResponse(w, map[string]any{"code": "future_success"})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	resetter := newCodexRateLimitResetter()
	configureCodexResetterTestURLs(resetter, server.URL)
	shouldRetry, err := resetter.tryReset(t.Context(), "token", "account")
	if err == nil || shouldRetry {
		t.Fatalf("first tryReset() = (%v, %v), want ambiguous outcome failure", shouldRetry, err)
	}
	shouldRetry, err = resetter.tryReset(t.Context(), "token", "account")
	if err != nil || !shouldRetry {
		t.Fatalf("second tryReset() = (%v, %v), want available-state retry", shouldRetry, err)
	}
	shouldRetry, err = resetter.tryReset(t.Context(), "token", "account")
	if err != nil || shouldRetry {
		t.Fatalf("third tryReset() = (%v, %v), want same-episode suppression", shouldRetry, err)
	}
	if got := usageRequests.Load(); got != 3 {
		t.Fatalf("usage requests = %d, want 3", got)
	}
	if got := consumeRequests.Load(); got != 1 {
		t.Fatalf("consume requests = %d, want 1", got)
	}
}

func TestCodexRateLimitResetterRetainsConfirmedSuppressionUntilNewEpisode(t *testing.T) {
	var usageRequests atomic.Int32
	var consumeRequests atomic.Int32

	oldExhausted := codexResetTestUsage(true, 1)
	oldAvailable := codexResetTestUsage(false, 0)
	newExhausted := codexResetTestUsage(true, 1)
	newAvailable := codexResetTestUsage(false, 0)
	for _, usage := range []map[string]any{newExhausted, newAvailable} {
		rateLimit := usage["rate_limit"].(map[string]any)
		rateLimit["primary_window"].(map[string]any)["reset_at"] = int64(1_800_000_001)
		rateLimit["secondary_window"].(map[string]any)["reset_at"] = int64(1_800_500_001)
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/backend-api/wham/usage":
			switch usageRequests.Add(1) {
			case 1:
				writeCodexUsageResponse(w, oldExhausted)
			case 2, 3:
				writeCodexUsageResponse(w, oldAvailable)
			case 4:
				writeCodexUsageResponse(w, oldExhausted)
			case 5:
				writeCodexUsageResponse(w, newExhausted)
			case 6:
				writeCodexUsageResponse(w, newAvailable)
			default:
				http.Error(w, "unexpected usage request", http.StatusInternalServerError)
			}
		case "/backend-api/wham/rate-limit-reset-credits/consume":
			consumeRequests.Add(1)
			writeCodexUsageResponse(w, map[string]any{"code": "reset"})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	resetter := newCodexRateLimitResetter()
	configureCodexResetterTestURLs(resetter, server.URL)

	shouldRetry, err := resetter.tryReset(t.Context(), "token", "account")
	if err != nil || !shouldRetry {
		t.Fatalf("first tryReset() = (%v, %v), want confirmed reset", shouldRetry, err)
	}
	shouldRetry, err = resetter.tryReset(t.Context(), "token", "account")
	if err != nil || !shouldRetry {
		t.Fatalf("second tryReset() = (%v, %v), want available-state retry", shouldRetry, err)
	}
	shouldRetry, err = resetter.tryReset(t.Context(), "token", "account")
	if err != nil || shouldRetry {
		t.Fatalf(
			"third tryReset() = (%v, %v), want same-episode stale state suppressed",
			shouldRetry,
			err,
		)
	}
	shouldRetry, err = resetter.tryReset(t.Context(), "token", "account")
	if err != nil || !shouldRetry {
		t.Fatalf("fourth tryReset() = (%v, %v), want new episode reset", shouldRetry, err)
	}
	if got := consumeRequests.Load(); got != 2 {
		t.Fatalf("consume requests = %d, want 2 across distinct exhaustion episodes", got)
	}
}

func TestCodexProviderSuppressesSecondCreditForUnverifiedEpisode(t *testing.T) {
	tests := []struct {
		name                 string
		failPostVerification bool
	}{
		{name: "still exhausted"},
		{name: "verification request fails", failPostVerification: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var responseRequests atomic.Int32
			var usageRequests atomic.Int32
			var consumeRequests atomic.Int32

			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/responses":
					responseRequests.Add(1)
					writeCodexAPIError(w, http.StatusTooManyRequests, codexUsageLimitReachedError, "")
				case "/backend-api/wham/usage":
					requestNumber := usageRequests.Add(1)
					if tt.failPostVerification && requestNumber == 2 {
						http.Error(w, "temporary failure", http.StatusBadGateway)
						return
					}
					writeCodexUsageResponse(w, codexResetTestUsage(true, 1))
				case "/backend-api/wham/rate-limit-reset-credits/consume":
					consumeRequests.Add(1)
					writeCodexUsageResponse(w, map[string]any{"code": "reset"})
				default:
					http.NotFound(w, r)
				}
			}))
			defer server.Close()

			provider := newCodexResetTestProvider(server.URL, "token", "account")
			for call := 1; call <= 2; call++ {
				_, err := provider.Chat(
					t.Context(),
					[]Message{{Role: "user", Content: "Hello"}},
					nil,
					"gpt-5.3-codex",
					map[string]any{},
				)
				if err == nil || !strings.Contains(err.Error(), "codex API call") {
					t.Fatalf("Chat() call %d error = %v, want original Codex API error", call, err)
				}
			}

			if got := responseRequests.Load(); got != 3 {
				t.Fatalf(
					"response requests = %d, want one retry after redemption and no retry on suppressed episode",
					got,
				)
			}
			if got := usageRequests.Load(); got != 3 {
				t.Fatalf("usage requests = %d, want 3", got)
			}
			if got := consumeRequests.Load(); got != 1 {
				t.Fatalf("consume requests = %d, want 1", got)
			}
		})
	}
}

func newCodexResetTestProvider(baseURL, token, accountID string) *CodexProvider {
	provider := NewCodexProvider(token, accountID)
	provider.client = newCodexOpenAIClient(
		token,
		accountID,
		optionWithCodexResetTestBaseURL(baseURL),
	)
	configureCodexResetTestURLs(provider, baseURL)
	return provider
}

func optionWithCodexResetTestBaseURL(baseURL string) option.RequestOption {
	return option.WithBaseURL(baseURL)
}

func configureCodexResetTestURLs(provider *CodexProvider, baseURL string) {
	configureCodexResetterTestURLs(provider.rateLimitReset, baseURL)
}

func configureCodexResetterTestURLs(resetter *codexRateLimitResetter, baseURL string) {
	testEndpointQuery := fmt.Sprintf("?test_endpoint=%d", codexResetTestEndpointSequence.Add(1))
	resetter.usageURL = baseURL + "/backend-api/wham/usage" + testEndpointQuery
	resetter.consumeURL = baseURL +
		"/backend-api/wham/rate-limit-reset-credits/consume" + testEndpointQuery
	resetter.newRedeemRequestID = func() string {
		return codexResetTestRedeemID
	}
}

func assertCodexResetAuthHeaders(t *testing.T, r *http.Request, token, accountID string) {
	t.Helper()
	if got := r.Header.Get("Authorization"); got != "Bearer "+token {
		t.Errorf("Authorization = %q, want Bearer %s", got, token)
	}
	if got := r.Header.Get("Chatgpt-Account-Id"); got != accountID {
		t.Errorf("ChatGPT-Account-Id = %q, want %q", got, accountID)
	}
	if strings.HasPrefix(r.URL.Path, "/backend-api/wham/") {
		if got := r.Header.Get("User-Agent"); got != "codex-cli" {
			t.Errorf("User-Agent = %q, want codex-cli", got)
		}
	}
}

func assertCodexResetPayload(t *testing.T, r *http.Request, redeemRequestID string) {
	t.Helper()
	var payload map[string]any
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		t.Errorf("decode reset payload: %v", err)
		return
	}
	if len(payload) != 1 {
		t.Errorf("reset payload = %#v, want exactly one field", payload)
	}
	if got := payload["redeem_request_id"]; got != redeemRequestID {
		t.Errorf("redeem_request_id = %#v, want %q", got, redeemRequestID)
	}
}

func codexResetTestUsage(exhausted bool, availableCredits int64) map[string]any {
	primaryUsedPercent := 20
	if exhausted {
		primaryUsedPercent = 100
	}
	return map[string]any{
		"rate_limit": map[string]any{
			"allowed":       !exhausted,
			"limit_reached": exhausted,
			"primary_window": map[string]any{
				"limit_window_seconds": 18_000,
				"reset_at":             1_800_000_000,
				"used_percent":         primaryUsedPercent,
			},
			"secondary_window": map[string]any{
				"limit_window_seconds": 604_800,
				"reset_at":             1_800_500_000,
				"used_percent":         50,
			},
		},
		"rate_limit_reached_type": map[string]any{
			"type": codexRateLimitReachedType,
		},
		"rate_limit_reset_credits": map[string]any{
			"available_count": availableCredits,
		},
	}
}

func writeCodexAPIError(w http.ResponseWriter, status int, errorType, code string) {
	errorPayload := map[string]any{
		"message": "usage limit reached",
		"type":    errorType,
	}
	if code != "" {
		errorPayload["code"] = code
	}
	writeCodexUsageResponseWithStatus(w, status, map[string]any{"error": errorPayload})
}

func writeCodexUsageResponse(w http.ResponseWriter, payload map[string]any) {
	writeCodexUsageResponseWithStatus(w, http.StatusOK, payload)
}

func writeCodexUsageResponseWithStatus(w http.ResponseWriter, status int, payload map[string]any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}

func writeCodexResetTestSuccess(w http.ResponseWriter) {
	writeCompletedSSE(w, map[string]any{
		"id":     "resp_reset",
		"object": "response",
		"status": "completed",
		"output": []map[string]any{
			{
				"id":     "msg_reset",
				"type":   "message",
				"role":   "assistant",
				"status": "completed",
				"content": []map[string]any{
					{"type": "output_text", "text": "reset succeeded"},
				},
			},
		},
		"usage": map[string]any{
			"input_tokens":          2,
			"output_tokens":         2,
			"total_tokens":          4,
			"input_tokens_details":  map[string]any{"cached_tokens": 0},
			"output_tokens_details": map[string]any{"reasoning_tokens": 0},
		},
	})
}

func writeCodexResponseFailedSSE(w http.ResponseWriter) {
	writeCodexTestSSEEvent(w, map[string]any{
		"type":            "response.failed",
		"sequence_number": 1,
		"response": map[string]any{
			"id":     "resp_failed",
			"object": "response",
			"status": "failed",
			"error": map[string]any{
				"type":    codexUsageLimitReachedError,
				"code":    codexUsageLimitReachedError,
				"message": "usage limit reached",
			},
			"output": []any{},
		},
	})
}

func writeCodexErrorEventSSE(w http.ResponseWriter) {
	writeCodexTestSSEEvent(w, map[string]any{
		"type":            "error",
		"sequence_number": 1,
		"code":            codexUsageLimitReachedError,
		"message":         "usage limit reached",
		"param":           "",
	})
}

func writeCodexTestSSEEvent(w http.ResponseWriter, event map[string]any) {
	payload, _ := json.Marshal(event)
	w.Header().Set("Content-Type", "text/event-stream")
	fmt.Fprintf(w, "event: %s\n", event["type"])
	fmt.Fprintf(w, "data: %s\n\n", payload)
	fmt.Fprint(w, "data: [DONE]\n\n")
}
