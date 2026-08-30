package events

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sipeed/picoclaw/cmd/picoclaw/internal/localgateway"
	ppid "github.com/sipeed/picoclaw/pkg/pid"
)

const testEventID = "ev_0123456789abcdef0123456789abcdef"

type jsonGatewayFunc func(context.Context, localgateway.Request) (localgateway.Response, error)

func (function jsonGatewayFunc) DoJSON(
	ctx context.Context,
	request localgateway.Request,
) (localgateway.Response, error) {
	return function(ctx, request)
}

func testGatewayClient(gateway jsonGateway) *gatewayClient {
	return &gatewayClient{transport: gateway}
}

func staticGateway(status int, body string, err error) *gatewayClient {
	return testGatewayClient(jsonGatewayFunc(func(
		context.Context,
		localgateway.Request,
	) (localgateway.Response, error) {
		return localgateway.Response{StatusCode: status, Body: []byte(body)}, err
	}))
}

func dispatchedGatewayError(err error) error {
	return errors.Join(localgateway.ErrRequestMayHaveBeenSent, err)
}

func TestGatewayClientGetBuildsCanonicalTransportRequest(t *testing.T) {
	t.Parallel()

	query := url.Values{
		"type":           {"issues.opened"},
		"source":         {"github"},
		"routing_status": {"pending"},
		"limit":          {"10"},
	}
	client := testGatewayClient(jsonGatewayFunc(func(
		ctx context.Context,
		request localgateway.Request,
	) (localgateway.Response, error) {
		assert.NotNil(t, ctx)
		assert.Equal(t, http.MethodGet, request.Method)
		assert.Equal(t, "/runtime/eventing/events", request.Path)
		assert.Equal(t, query.Encode(), request.Query.Encode())
		assert.Nil(t, request.Body)
		return localgateway.Response{
			StatusCode: http.StatusOK,
			Body:       []byte(`{"events":[]}`),
		}, nil
	}))

	got, err := client.get(context.Background(), "/runtime/eventing/events", query)
	require.NoError(t, err)
	assert.Equal(t, "{\n  \"events\": []\n}\n", string(got))
}

func TestGatewayClientPayloadPreservesExactBoundedJSONObject(t *testing.T) {
	t.Parallel()

	const payload = " \n{\n  \"large\": 9007199254740993,\n  \"tiny\": 1e-1000\n}\t"
	client := testGatewayClient(jsonGatewayFunc(func(
		_ context.Context,
		request localgateway.Request,
	) (localgateway.Response, error) {
		assert.Equal(t, http.MethodGet, request.Method)
		assert.Equal(t, "/runtime/eventing/events/"+testEventID+"/payload", request.Path)
		assert.Empty(t, request.Query)
		assert.Nil(t, request.Body)
		return localgateway.Response{
			StatusCode: http.StatusOK,
			Body:       []byte(payload),
		}, nil
	}))

	got, err := client.payload(context.Background(), testEventID)
	require.NoError(t, err)
	assert.Equal(t, payload, string(got))
}

func TestGatewayClientPayloadRejectsNonObjectJSON(t *testing.T) {
	t.Parallel()

	client := staticGateway(http.StatusOK, `["not","an","object"]`, nil)
	_, err := client.payload(context.Background(), testEventID)
	require.EqualError(t, err, "live gateway event API returned invalid JSON")
}

func TestGatewayClientReplaySendsOneExactEmptyObject(t *testing.T) {
	t.Parallel()

	calls := 0
	client := testGatewayClient(jsonGatewayFunc(func(
		_ context.Context,
		request localgateway.Request,
	) (localgateway.Response, error) {
		calls++
		assert.Equal(t, http.MethodPost, request.Method)
		assert.Equal(t, "/runtime/eventing/events/"+testEventID+"/replay", request.Path)
		assert.Empty(t, request.Query)
		assert.Equal(t, []byte("{}"), request.Body)
		return localgateway.Response{
			StatusCode: http.StatusCreated,
			Body:       []byte(`{"event":{"id":"` + testEventID + `"}}`),
		}, nil
	}))

	_, err := client.replay(context.Background(), testEventID)
	require.NoError(t, err)
	assert.Equal(t, 1, calls)
}

func TestGatewayClientReplayAmbiguousFailuresRequireInspection(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		response localgateway.Response
		err      error
	}{
		{
			name: "dispatched transport failure",
			err: dispatchedGatewayError(
				errors.New("timeout containing test-token"),
			),
		},
		{
			name: "internal response",
			response: localgateway.Response{
				StatusCode: http.StatusInternalServerError,
				Body:       []byte(`{"error":"database secret"}`),
			},
		},
		{
			name:     "invalid success response",
			response: localgateway.Response{StatusCode: http.StatusCreated},
			err:      dispatchedGatewayError(localgateway.ErrInvalidResponse),
		},
		{
			name:     "invalid success body",
			response: localgateway.Response{StatusCode: http.StatusCreated},
			err:      dispatchedGatewayError(localgateway.ErrInvalidJSON),
		},
		{
			name: "defensive invalid success body",
			response: localgateway.Response{
				StatusCode: http.StatusCreated,
				Body:       []byte(`[]`),
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			client := testGatewayClient(jsonGatewayFunc(func(
				context.Context,
				localgateway.Request,
			) (localgateway.Response, error) {
				return test.response, test.err
			}))
			_, err := client.replay(context.Background(), testEventID)
			require.EqualError(t, err, replayUnknownOutcomeMessage)
			assert.NotContains(t, err.Error(), "test-token")
			assert.NotContains(t, err.Error(), "database")
			assert.NotContains(t, strings.ToLower(err.Error()), "timeout")
		})
	}
}

func TestGatewayClientReplayStatusBeforeBodyFailureParity(t *testing.T) {
	t.Parallel()

	statuses := []struct {
		name   string
		status int
		want   string
	}{
		{
			name:   "bad request",
			status: http.StatusBadRequest,
			want:   "gateway rejected the event request (400 Bad Request)",
		},
		{
			name:   "unauthorized",
			status: http.StatusUnauthorized,
			want:   "live gateway event credentials are unavailable or stale",
		},
		{
			name:   "forbidden",
			status: http.StatusForbidden,
			want:   "live gateway event credentials are unavailable or stale",
		},
		{
			name:   "not found",
			status: http.StatusNotFound,
			want:   "durable event operations are unavailable on the running gateway",
		},
		{
			name:   "unavailable",
			status: http.StatusServiceUnavailable,
			want:   "durable event operations are temporarily unavailable on the running gateway",
		},
	}
	failures := []struct {
		name string
		err  error
	}{
		{name: "malformed JSON", err: localgateway.ErrInvalidJSON},
		{name: "wrong content type", err: localgateway.ErrInvalidResponse},
		{name: "oversize", err: localgateway.ErrResponseTooLarge},
		{name: "read failure", err: localgateway.ErrResponseRead},
	}

	for _, status := range statuses {
		for _, failure := range failures {
			t.Run(status.name+"/"+failure.name, func(t *testing.T) {
				t.Parallel()
				client := staticGateway(
					status.status,
					"",
					dispatchedGatewayError(failure.err),
				)
				_, err := client.replay(context.Background(), testEventID)
				require.EqualError(t, err, status.want)
				assert.NotContains(t, err.Error(), replayUnknownOutcomeMessage)
			})
		}
	}
}

func TestGatewayClientReplayUnsafeStatusAndDispatchedFailuresAreUnknown(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		response localgateway.Response
		err      error
	}{
		{
			name:     "unsafe status",
			response: localgateway.Response{StatusCode: http.StatusInternalServerError},
			err:      dispatchedGatewayError(localgateway.ErrInvalidJSON),
		},
		{
			name: "dispatched cancellation",
			err:  dispatchedGatewayError(context.Canceled),
		},
		{
			name: "dispatched transport failure",
			err:  dispatchedGatewayError(localgateway.ErrAPIUnavailable),
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			client := testGatewayClient(jsonGatewayFunc(func(
				context.Context,
				localgateway.Request,
			) (localgateway.Response, error) {
				return test.response, test.err
			}))
			_, err := client.replay(context.Background(), testEventID)
			require.EqualError(t, err, replayUnknownOutcomeMessage)
		})
	}
}

func TestGatewayClientReplayPreservesPreDispatchPIDFailures(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		err  error
		want string
	}{
		{
			name: "missing",
			err:  localgateway.ErrGatewayUnavailable,
			want: "live gateway is unavailable; start picoclaw gateway and retry",
		},
		{
			name: "invalid metadata",
			err:  localgateway.ErrInvalidPIDMetadata,
			want: "live gateway PID metadata is invalid; restart the gateway",
		},
		{
			name: "invalid token",
			err:  localgateway.ErrInvalidPIDToken,
			want: "live gateway PID metadata has no valid bearer token; restart the gateway",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			client := staticGateway(0, "", test.err)
			_, err := client.replay(context.Background(), testEventID)
			require.EqualError(t, err, test.want)
			assert.NotContains(t, err.Error(), replayUnknownOutcomeMessage)
		})
	}
}

func TestGatewayClientReturnsBoundedGenericErrors(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		status     int
		want       string
		notContain string
	}{
		{name: "bad request", status: http.StatusBadRequest, want: "400 Bad Request"},
		{name: "unauthorized", status: http.StatusUnauthorized, want: "credentials"},
		{name: "not found", status: http.StatusNotFound, want: "unavailable"},
		{
			name:   "unavailable",
			status: http.StatusServiceUnavailable,
			want:   "temporarily unavailable",
		},
		{
			name:       "internal",
			status:     http.StatusInternalServerError,
			want:       "HTTP status 500",
			notContain: "server-secret",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			client := staticGateway(test.status, `{"error":"server-secret"}`, nil)
			_, err := client.get(context.Background(), "/runtime/eventing/events", nil)
			require.Error(t, err)
			assert.Contains(t, err.Error(), test.want)
			assert.NotContains(t, err.Error(), "server-secret")
			assert.NotContains(t, err.Error(), "test-token")
		})
	}
}

func TestGatewayClientGETClassifiesStatusBeforeBodyFailure(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		status int
		want   string
	}{
		{
			name:   "safe status",
			status: http.StatusServiceUnavailable,
			want:   "durable event operations are temporarily unavailable on the running gateway",
		},
		{
			name:   "internal status",
			status: http.StatusInternalServerError,
			want:   "live gateway event request failed with HTTP status 500",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			client := staticGateway(
				test.status,
				"",
				dispatchedGatewayError(localgateway.ErrInvalidJSON),
			)
			_, err := client.get(context.Background(), "/runtime/eventing/events", nil)
			require.EqualError(t, err, test.want)
			assert.NotContains(t, err.Error(), "invalid JSON")
		})
	}
}

func TestGatewayClientMapsTransportErrorsWithoutSecrets(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		err  error
		want string
	}{
		{
			name: "transport",
			err:  localgateway.ErrAPIUnavailable,
			want: "live gateway event API is unavailable",
		},
		{
			name: "client",
			err:  localgateway.ErrClientUnavailable,
			want: "live gateway event client is unavailable",
		},
		{
			name: "path",
			err:  localgateway.ErrInvalidPath,
			want: "live gateway event request path is invalid",
		},
		{
			name: "request",
			err:  localgateway.ErrInvalidRequest,
			want: "live gateway PID metadata is invalid; restart the gateway",
		},
		{
			name: "gateway",
			err:  localgateway.ErrGatewayUnavailable,
			want: localgateway.ErrGatewayUnavailable.Error(),
		},
		{
			name: "PID",
			err:  localgateway.ErrInvalidPIDMetadata,
			want: localgateway.ErrInvalidPIDMetadata.Error(),
		},
		{
			name: "token",
			err:  localgateway.ErrInvalidPIDToken,
			want: localgateway.ErrInvalidPIDToken.Error(),
		},
		{
			name: "method",
			err:  localgateway.ErrInvalidMethod,
			want: localgateway.ErrInvalidMethod.Error(),
		},
		{
			name: "query",
			err:  localgateway.ErrQueryTooLarge,
			want: "gateway rejected the event request (400 Bad Request)",
		},
		{name: "GET body", err: localgateway.ErrGETBody, want: localgateway.ErrGETBody.Error()},
		{
			name: "request size",
			err:  localgateway.ErrRequestTooLarge,
			want: localgateway.ErrRequestTooLarge.Error(),
		},
		{
			name: "request body",
			err:  localgateway.ErrInvalidRequestBody,
			want: localgateway.ErrInvalidRequestBody.Error(),
		},
		{
			name: "invalid response",
			err:  localgateway.ErrInvalidResponse,
			want: "live gateway event API returned an invalid response",
		},
		{
			name: "oversize",
			err:  localgateway.ErrResponseTooLarge,
			want: "live gateway event response exceeds the safe display limit",
		},
		{
			name: "read",
			err:  localgateway.ErrResponseRead,
			want: "failed to read live gateway event response",
		},
		{
			name: "invalid JSON",
			err:  localgateway.ErrInvalidJSON,
			want: "live gateway event API returned invalid JSON",
		},
		{
			name: "unknown secret",
			err:  errors.New("dial detail containing secret material"),
			want: "live gateway event API returned an invalid response",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			client := staticGateway(0, "", test.err)
			_, err := client.get(context.Background(), "/runtime/eventing/events", nil)
			require.EqualError(t, err, test.want)
			assert.NotContains(t, err.Error(), "secret material")
		})
	}
}

func TestGatewayClientMapsDeadlineAndStatuslessSuccessDefensively(t *testing.T) {
	t.Parallel()

	client := staticGateway(0, "", context.DeadlineExceeded)
	_, err := client.get(context.Background(), "/runtime/eventing/events", nil)
	require.EqualError(t, err, "live gateway event request failed: context deadline exceeded")
	assert.ErrorIs(t, err, context.DeadlineExceeded)

	client = staticGateway(0, `{}`, nil)
	_, err = client.get(context.Background(), "/runtime/eventing/events", nil)
	require.EqualError(t, err, "live gateway event API returned an invalid status")
}

func TestGatewayClientRejectsUnavailableAdapter(t *testing.T) {
	t.Parallel()

	for _, client := range []*gatewayClient{nil, {}} {
		_, err := client.get(context.Background(), "/runtime/eventing/events", nil)
		require.EqualError(t, err, "live gateway event client is unavailable")
	}
}

func TestNewGatewayClientHasSharedTransport(t *testing.T) {
	t.Parallel()

	client := newGatewayClient()
	require.NotNil(t, client)
	require.NotNil(t, client.transport)
}

func TestNewGatewayClientWiresEventsRouteAndPIDAuthentication(t *testing.T) {
	homePath := t.TempDir()
	t.Setenv("PICOCLAW_HOME", homePath)

	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		assert.Contains(
			t,
			[]string{"/runtime/eventing/events", "/runtime/eventing/dispatches"},
			request.URL.Path,
		)
		assert.Equal(t, "Bearer wiring-token", request.Header.Get("Authorization"))
		writer.Header().Set("Content-Type", "application/json")
		_, err := writer.Write([]byte(`{"events":[]}`))
		require.NoError(t, err)
	}))
	defer server.Close()
	endpoint, err := url.Parse(server.URL)
	require.NoError(t, err)
	port, err := strconv.Atoi(endpoint.Port())
	require.NoError(t, err)
	pidData, err := json.Marshal(ppid.PidFileData{
		PID:   os.Getpid(),
		Token: "wiring-token",
		Host:  endpoint.Hostname(),
		Port:  port,
	})
	require.NoError(t, err)
	require.NoError(
		t,
		os.WriteFile(filepath.Join(homePath, ".picoclaw.pid"), pidData, 0o600),
	)

	client := newGatewayClient()
	output, err := client.get(
		context.Background(),
		"/runtime/eventing/events",
		nil,
	)
	require.NoError(t, err)
	assert.Equal(t, "{\n  \"events\": []\n}\n", string(output))
	_, err = client.get(context.Background(), "/runtime/eventing/dispatches", nil)
	require.NoError(t, err)
}

func TestGatewayClientHonorsCanceledContext(t *testing.T) {
	t.Parallel()

	client := testGatewayClient(jsonGatewayFunc(func(
		ctx context.Context,
		_ localgateway.Request,
	) (localgateway.Response, error) {
		return localgateway.Response{}, ctx.Err()
	}))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := client.get(ctx, "/runtime/eventing/events", nil)
	require.Error(t, err)
	assert.ErrorIs(t, err, context.Canceled)
	require.EqualError(t, err, "live gateway event request failed: context canceled")
}

func TestPrettyJSONPreservesNumericTokensAndEscapedControlContent(t *testing.T) {
	t.Parallel()

	raw := []byte(`{"value":9007199254740993,"tiny":1e-400,"text":"\u001b[31m"}`)
	formatted, err := prettyJSON(raw)
	require.NoError(t, err)
	assert.Contains(t, string(formatted), "9007199254740993")
	assert.Contains(t, string(formatted), "1e-400")
	assert.Contains(t, string(formatted), `\u001b[31m`)
	assert.NotContains(t, string(formatted), "\x1b")
}

func TestPrettyJSONRejectsTrailingValues(t *testing.T) {
	t.Parallel()

	_, err := prettyJSON(bytes.Repeat([]byte("{}"), 2))
	require.Error(t, err)
}
