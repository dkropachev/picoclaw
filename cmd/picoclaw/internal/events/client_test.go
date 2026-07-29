package events

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	ppid "github.com/sipeed/picoclaw/pkg/pid"
)

const testEventID = "ev_0123456789abcdef0123456789abcdef"

type httpDoerFunc func(*http.Request) (*http.Response, error)

func (function httpDoerFunc) Do(request *http.Request) (*http.Response, error) {
	return function(request)
}

func testGatewayClient(doer httpDoer) *gatewayClient {
	return &gatewayClient{
		homePath: func() string { return "/test/picoclaw-home" },
		readPID: func(path string) *ppid.PidFileData {
			if path != "/test/picoclaw-home" {
				panic("unexpected home path")
			}
			return &ppid.PidFileData{
				PID:   1234,
				Token: "test-token",
				Port:  18790,
				Host:  "127.0.0.1",
			}
		},
		http:     doer,
		timeout:  time.Second,
		maxBytes: 1 << 20,
	}
}

func jsonResponse(status int, body string) *http.Response {
	return &http.Response{
		StatusCode:    status,
		Header:        http.Header{"Content-Type": []string{"application/json"}},
		Body:          io.NopCloser(strings.NewReader(body)),
		ContentLength: int64(len(body)),
	}
}

func TestGatewayClientGetBuildsAuthenticatedCanonicalRequest(t *testing.T) {
	t.Parallel()

	query := url.Values{
		"type":           {"issues.opened"},
		"source":         {"github"},
		"routing_status": {"pending"},
		"limit":          {"10"},
	}
	client := testGatewayClient(httpDoerFunc(func(request *http.Request) (*http.Response, error) {
		assert.Equal(t, http.MethodGet, request.Method)
		assert.Equal(t, "/runtime/eventing/events", request.URL.Path)
		assert.Equal(t, query.Encode(), request.URL.RawQuery)
		assert.Equal(t, "127.0.0.1:18790", request.URL.Host)
		assert.Equal(t, "Bearer test-token", request.Header.Get("Authorization"))
		assert.Equal(t, "application/json", request.Header.Get("Accept"))
		assert.Empty(t, request.Header.Get("Content-Type"))
		return jsonResponse(http.StatusOK, `{"events":[]}`), nil
	}))

	got, err := client.get(
		context.Background(),
		"/runtime/eventing/events",
		query,
	)
	require.NoError(t, err)
	assert.Equal(t, "{\n  \"events\": []\n}\n", string(got))
}

func TestGatewayClientPayloadPreservesExactBoundedJSONObject(t *testing.T) {
	t.Parallel()

	const payload = " \n{\n  \"large\": 9007199254740993,\n  \"tiny\": 1e-1000\n}\t"
	client := testGatewayClient(httpDoerFunc(func(request *http.Request) (*http.Response, error) {
		assert.Equal(t, http.MethodGet, request.Method)
		assert.Equal(t, "/runtime/eventing/events/"+testEventID+"/payload", request.URL.Path)
		assert.Empty(t, request.URL.RawQuery)
		assert.Equal(t, "Bearer test-token", request.Header.Get("Authorization"))
		return jsonResponse(http.StatusOK, payload), nil
	}))

	got, err := client.payload(context.Background(), testEventID)
	require.NoError(t, err)
	assert.Equal(t, payload, string(got))
}

func TestGatewayClientPayloadRejectsNonObjectJSON(t *testing.T) {
	t.Parallel()

	client := testGatewayClient(httpDoerFunc(func(*http.Request) (*http.Response, error) {
		return jsonResponse(http.StatusOK, `["not","an","object"]`), nil
	}))
	_, err := client.payload(context.Background(), testEventID)
	require.EqualError(t, err, "live gateway event API returned invalid JSON")
}

func TestGatewayClientReplaySendsOneExactEmptyObject(t *testing.T) {
	t.Parallel()

	calls := 0
	client := testGatewayClient(httpDoerFunc(func(request *http.Request) (*http.Response, error) {
		calls++
		assert.Equal(t, http.MethodPost, request.Method)
		assert.Equal(t, "/runtime/eventing/events/"+testEventID+"/replay", request.URL.Path)
		assert.Empty(t, request.URL.RawQuery)
		assert.Equal(t, "application/json", request.Header.Get("Content-Type"))
		assert.Equal(t, int64(2), request.ContentLength)
		body, err := io.ReadAll(request.Body)
		require.NoError(t, err)
		assert.Equal(t, []byte("{}"), body)
		return jsonResponse(http.StatusCreated, `{"event":{"id":"`+testEventID+`"}}`), nil
	}))

	_, err := client.replay(context.Background(), testEventID)
	require.NoError(t, err)
	assert.Equal(t, 1, calls)
}

func TestGatewayClientReplayAmbiguousFailuresRequireInspection(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		do   func(*http.Request) (*http.Response, error)
	}{
		{
			name: "transport timeout",
			do: func(*http.Request) (*http.Response, error) {
				return nil, errors.New("timeout containing test-token")
			},
		},
		{
			name: "internal response",
			do: func(*http.Request) (*http.Response, error) {
				return jsonResponse(
					http.StatusInternalServerError,
					`{"error":"database secret"}`,
				), nil
			},
		},
		{
			name: "invalid success content type",
			do: func(*http.Request) (*http.Response, error) {
				response := jsonResponse(http.StatusCreated, `{"event":{}}`)
				response.Header.Set("Content-Type", "text/plain")
				return response, nil
			},
		},
		{
			name: "invalid success body",
			do: func(*http.Request) (*http.Response, error) {
				return jsonResponse(http.StatusCreated, `{"event":`), nil
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			client := testGatewayClient(httpDoerFunc(test.do))
			_, err := client.replay(context.Background(), testEventID)
			require.EqualError(t, err, replayUnknownOutcomeMessage)
			assert.NotContains(t, err.Error(), "test-token")
			assert.NotContains(t, err.Error(), "database")
			assert.NotContains(t, strings.ToLower(err.Error()), "timeout")
		})
	}
}

func TestGatewayClientReplayRetainsSafeRejectedAndPreDispatchFailures(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		status int
		want   string
	}{
		{
			name:   "not found",
			status: http.StatusNotFound,
			want:   "durable event operations are unavailable",
		},
		{
			name:   "gateway unavailable before replay",
			status: http.StatusServiceUnavailable,
			want:   "temporarily unavailable",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			client := testGatewayClient(httpDoerFunc(func(*http.Request) (*http.Response, error) {
				return jsonResponse(test.status, `{"error":"not committed"}`), nil
			}))
			_, err := client.replay(context.Background(), testEventID)
			require.Error(t, err)
			assert.Contains(t, err.Error(), test.want)
			assert.NotContains(t, err.Error(), replayUnknownOutcomeMessage)
		})
	}
}

func TestGatewayEndpointRejectsUnavailableOrUnsafePIDMetadata(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		data *ppid.PidFileData
		want string
	}{
		{
			name: "missing",
			want: "live gateway is unavailable",
		},
		{
			name: "invalid pid",
			data: &ppid.PidFileData{PID: 0, Token: "token", Port: 18790, Host: "127.0.0.1"},
			want: "PID metadata is invalid",
		},
		{
			name: "invalid port",
			data: &ppid.PidFileData{PID: 1, Token: "token", Port: 65536, Host: "127.0.0.1"},
			want: "PID metadata is invalid",
		},
		{
			name: "missing token",
			data: &ppid.PidFileData{PID: 1, Port: 18790, Host: "127.0.0.1"},
			want: "no valid bearer token",
		},
		{
			name: "header injection token",
			data: &ppid.PidFileData{PID: 1, Token: "token\nInjected: yes", Port: 18790, Host: "127.0.0.1"},
			want: "no valid bearer token",
		},
		{
			name: "invalid host",
			data: &ppid.PidFileData{PID: 1, Token: "token", Port: 18790, Host: "http://example.invalid"},
			want: "PID metadata is invalid",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			_, _, err := gatewayEndpoint(
				test.data,
				"/runtime/eventing/events",
				nil,
			)
			require.Error(t, err)
			assert.Contains(t, err.Error(), test.want)
			assert.NotContains(t, err.Error(), "Injected")
		})
	}
}

func TestGatewayEndpointNormalizesWildcardAndIPv6ProbeHosts(t *testing.T) {
	t.Parallel()

	for _, host := range []string{"*", "0.0.0.0", "::", "[::1]"} {
		endpoint, token, err := gatewayEndpoint(
			&ppid.PidFileData{
				PID:   1,
				Token: "token",
				Port:  18790,
				Host:  host,
			},
			"/runtime/eventing/events",
			nil,
		)
		require.NoError(t, err, host)
		assert.Equal(t, "token", token)
		assert.NotContains(t, endpoint.Host, "0.0.0.0", host)
		assert.NotEqual(t, "[::]:18790", endpoint.Host, host)
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
		{name: "unavailable", status: http.StatusServiceUnavailable, want: "temporarily unavailable"},
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
			client := testGatewayClient(httpDoerFunc(func(*http.Request) (*http.Response, error) {
				return jsonResponse(test.status, `{"error":"server-secret"}`), nil
			}))
			_, err := client.get(
				context.Background(),
				"/runtime/eventing/events",
				nil,
			)
			require.Error(t, err)
			assert.Contains(t, err.Error(), test.want)
			assert.NotContains(t, err.Error(), "server-secret")
			assert.NotContains(t, err.Error(), "test-token")
		})
	}
}

func TestGatewayClientDoesNotExposeTransportErrors(t *testing.T) {
	t.Parallel()

	client := testGatewayClient(httpDoerFunc(func(*http.Request) (*http.Response, error) {
		return nil, errors.New("dial detail containing secret material")
	}))
	_, err := client.get(context.Background(), "/runtime/eventing/events", nil)
	require.EqualError(t, err, "live gateway event API is unavailable")
}

func TestGatewayClientBoundsAndValidatesSuccessfulJSON(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		contentType string
		body        string
		contentLen  int64
		want        string
	}{
		{
			name:        "wrong content type",
			contentType: "text/html",
			body:        `{"events":[]}`,
			want:        "invalid response",
		},
		{
			name:        "invalid json",
			contentType: "application/json",
			body:        `{"events":`,
			want:        "invalid JSON",
		},
		{
			name:        "non object",
			contentType: "application/json",
			body:        `[]`,
			want:        "invalid JSON",
		},
		{
			name:        "content length",
			contentType: "application/json",
			body:        `{}`,
			contentLen:  17,
			want:        "safe display limit",
		},
		{
			name:        "streamed overflow",
			contentType: "application/json",
			body:        strings.Repeat(" ", 17),
			contentLen:  -1,
			want:        "safe display limit",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			client := testGatewayClient(httpDoerFunc(func(*http.Request) (*http.Response, error) {
				response := jsonResponse(http.StatusOK, test.body)
				response.Header.Set("Content-Type", test.contentType)
				if test.contentLen != 0 {
					response.ContentLength = test.contentLen
				}
				return response, nil
			}))
			client.maxBytes = 16
			_, err := client.get(context.Background(), "/runtime/eventing/events", nil)
			require.Error(t, err)
			assert.Contains(t, err.Error(), test.want)
		})
	}
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

func TestNewGatewayClientDisablesProxyAndRedirects(t *testing.T) {
	t.Parallel()

	client := newGatewayClient()
	httpClient, ok := client.http.(*http.Client)
	require.True(t, ok)
	transport, ok := httpClient.Transport.(*http.Transport)
	require.True(t, ok)
	assert.Nil(t, transport.Proxy)
	require.NotNil(t, httpClient.CheckRedirect)
	err := httpClient.CheckRedirect(
		&http.Request{},
		[]*http.Request{{}},
	)
	assert.ErrorIs(t, err, http.ErrUseLastResponse)
	assert.Equal(t, eventRequestTimeout, httpClient.Timeout)
}

func TestGatewayClientHonorsCanceledContext(t *testing.T) {
	t.Parallel()

	client := testGatewayClient(httpDoerFunc(func(request *http.Request) (*http.Response, error) {
		<-request.Context().Done()
		return nil, request.Context().Err()
	}))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := client.get(ctx, "/runtime/eventing/events", nil)
	require.Error(t, err)
	assert.ErrorIs(t, err, context.Canceled)
}

func TestPrettyJSONRejectsTrailingValues(t *testing.T) {
	t.Parallel()

	_, err := prettyJSON(bytes.Repeat([]byte("{}"), 2))
	require.Error(t, err)
}
