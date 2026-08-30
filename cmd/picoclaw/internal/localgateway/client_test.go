package localgateway

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	ppid "github.com/sipeed/picoclaw/pkg/pid"
)

const (
	testRoutePrefix = "/runtime/eventing/development-workspaces"
	testRoutePath   = testRoutePrefix + "/devw_0123456789abcdef0123456789abcdef"
)

type httpDoerFunc func(*http.Request) (*http.Response, error)

func (function httpDoerFunc) Do(request *http.Request) (*http.Response, error) {
	return function(request)
}

func testClient(t *testing.T, doer httpDoer) *Client {
	t.Helper()
	client, err := New(testRoutePrefix)
	require.NoError(t, err)
	client.homePath = func() string { return "/test/picoclaw-home" }
	client.readPID = func(path string) *ppid.PidFileData {
		require.Equal(t, "/test/picoclaw-home", path)
		return &ppid.PidFileData{
			PID: 1234, Token: "test-bearer-token", Host: "127.0.0.1", Port: 18790,
		}
	}
	client.http = doer
	client.timeout = time.Second
	client.maxRequest = 1 << 20
	client.maxResponse = 1 << 20
	client.maxQuery = 1 << 16
	return client
}

func jsonResponse(status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Header: http.Header{
			"Content-Type": []string{"application/json; charset=utf-8"},
		},
		Body:          io.NopCloser(strings.NewReader(body)),
		ContentLength: int64(len(body)),
	}
}

func metadata(pid int, token, host string, port int) *ppid.PidFileData {
	return &ppid.PidFileData{PID: pid, Token: token, Host: host, Port: port}
}

func TestNewFreezesCanonicalRuntimeRoutePrefix(t *testing.T) {
	t.Parallel()

	for _, prefix := range []string{
		"/runtime/eventing/",
		"/runtime/eventing/events",
		testRoutePrefix,
		"/runtime/agents/main/activity",
	} {
		client, err := New(prefix)
		require.NoError(t, err, prefix)
		assert.Equal(t, prefix, client.routePrefix)
	}

	for _, prefix := range []string{
		"",
		"runtime/eventing/",
		"/runtime/",
		"/api/development-workspaces",
		"/runtime//eventing",
		"/runtime/./eventing",
		"/runtime/../api",
		"/runtime/eventing/%65vents",
		"/runtime/eventing?target=events",
		"/runtime/eventing#events",
		"/runtime/eventing\\events",
		"/runtime/eventing/white space",
		"/runtime/eventing/événements",
	} {
		client, err := New(prefix)
		assert.Nil(t, client, prefix)
		require.EqualError(t, err, "local gateway route prefix is invalid", prefix)
		assert.ErrorIs(t, err, ErrInvalidRoutePrefix)
		if prefix != "" {
			assert.NotContains(t, err.Error(), prefix)
		}
	}
}

func TestNewDisablesProxyAndRedirects(t *testing.T) {
	t.Parallel()

	client, err := New(testRoutePrefix)
	require.NoError(t, err)
	httpClient, ok := client.http.(*http.Client)
	require.True(t, ok)
	transport, ok := httpClient.Transport.(*http.Transport)
	require.True(t, ok)
	assert.Nil(t, transport.Proxy)
	require.NotNil(t, httpClient.CheckRedirect)
	assert.ErrorIs(
		t,
		httpClient.CheckRedirect(&http.Request{}, []*http.Request{{}}),
		http.ErrUseLastResponse,
	)
	assert.Equal(t, requestTimeout, httpClient.Timeout)
}

func TestDoJSONBuildsAuthenticatedCanonicalGET(t *testing.T) {
	t.Parallel()

	query := url.Values{
		"limit": {"10"},
		"query": {`repository = "octo/repo"`},
	}
	const raw = " \n{\"value\":9007199254740993,\"tiny\":1e-1000}\t"
	client := testClient(t, httpDoerFunc(func(request *http.Request) (*http.Response, error) {
		assert.Equal(t, http.MethodGet, request.Method)
		assert.Equal(t, testRoutePath, request.URL.Path)
		assert.Equal(t, query.Encode(), request.URL.RawQuery)
		assert.Equal(t, "127.0.0.1:18790", request.URL.Host)
		assert.Equal(t, "http", request.URL.Scheme)
		assert.Nil(t, request.URL.User)
		assert.Equal(t, "Bearer test-bearer-token", request.Header.Get("Authorization"))
		assert.Equal(t, "application/json", request.Header.Get("Accept"))
		assert.Empty(t, request.Header.Get("Content-Type"))
		assert.Nil(t, request.Body)
		return jsonResponse(http.StatusConflict, raw), nil
	}))

	response, err := client.DoJSON(context.Background(), Request{
		Method: http.MethodGet, Path: testRoutePath, Query: query,
	})
	require.NoError(t, err)
	assert.Equal(t, http.StatusConflict, response.StatusCode)
	assert.Equal(t, raw, string(response.Body))
}

func TestDoJSONBuildsExactAuthenticatedPOST(t *testing.T) {
	t.Parallel()

	body := []byte(" \n{\"request_id\":\"request-0123456789\"}\t")
	client := testClient(t, httpDoerFunc(func(request *http.Request) (*http.Response, error) {
		assert.Equal(t, http.MethodPost, request.Method)
		assert.Equal(t, testRoutePrefix, request.URL.Path)
		assert.Empty(t, request.URL.RawQuery)
		assert.Equal(t, "application/json", request.Header.Get("Content-Type"))
		assert.Equal(t, "application/json", request.Header.Get("Accept"))
		assert.Equal(t, "Bearer test-bearer-token", request.Header.Get("Authorization"))
		assert.Equal(t, int64(len(body)), request.ContentLength)
		got, err := io.ReadAll(request.Body)
		require.NoError(t, err)
		assert.Equal(t, body, got)
		return jsonResponse(
			http.StatusCreated,
			`{"workspace":{"id":"devw_0123456789abcdef0123456789abcdef"}}`,
		), nil
	}))

	response, err := client.DoJSON(nil, Request{
		Method: http.MethodPost, Path: testRoutePrefix, Body: body,
	})
	require.NoError(t, err)
	assert.Equal(t, http.StatusCreated, response.StatusCode)
}

func TestDoJSONSnapshotsCallerBodyAndQueryBeforePIDDiscovery(t *testing.T) {
	t.Parallel()

	query := url.Values{"q": {"original"}}
	body := []byte(`{"value":"original"}`)
	pidRead := make(chan struct{})
	releasePID := make(chan struct{})
	client := testClient(t, httpDoerFunc(func(request *http.Request) (*http.Response, error) {
		assert.Equal(t, "q=original", request.URL.RawQuery)
		got, err := io.ReadAll(request.Body)
		require.NoError(t, err)
		assert.Equal(t, []byte(`{"value":"original"}`), got)
		return jsonResponse(http.StatusOK, `{}`), nil
	}))
	client.readPID = func(string) *ppid.PidFileData {
		close(pidRead)
		<-releasePID
		return metadata(1234, "test-bearer-token", "127.0.0.1", 18790)
	}

	result := make(chan error, 1)
	go func() {
		_, err := client.DoJSON(context.Background(), Request{
			Method: http.MethodPost,
			Path:   testRoutePath,
			Query:  query,
			Body:   body,
		})
		result <- err
	}()
	<-pidRead
	query.Set("q", "mutated")
	copy(body, []byte(`{"value":"mutated!"}`))
	close(releasePID)
	require.NoError(t, <-result)
}

func TestDoJSONRejectsNonCanonicalOrOutsidePathBeforePIDRead(t *testing.T) {
	t.Parallel()

	client := testClient(t, httpDoerFunc(func(*http.Request) (*http.Response, error) {
		t.Fatal("HTTP request made for invalid path")
		return nil, nil
	}))
	pidReads := 0
	client.readPID = func(string) *ppid.PidFileData {
		pidReads++
		return nil
	}

	for _, path := range []string{
		"",
		"runtime/eventing/development-workspaces",
		"/runtime/eventing/development-workspaces-foreign",
		"/runtime/eventing/development-workspace",
		"/runtime/eventing/development-workspaces/",
		"/runtime/eventing/development-workspaces//child",
		"/runtime/eventing/development-workspaces/./child",
		"/runtime/eventing/development-workspaces/../events",
		"/runtime/eventing/development-workspaces/%2e%2e/events",
		"/runtime/eventing/development-workspaces%2fchild",
		"/runtime/eventing/development-workspaces/child?query=1",
		"/runtime/eventing/development-workspaces/child#fragment",
		"/runtime/eventing/development-workspaces\\child",
		"/runtime/eventing/development-workspaces/white space",
		"/runtime/eventing/development-workspaces/é",
	} {
		_, err := client.DoJSON(context.Background(), Request{Method: http.MethodGet, Path: path})
		require.EqualError(t, err, "local gateway request path is invalid", path)
		assert.ErrorIs(t, err, ErrInvalidPath)
		if path != "" {
			assert.NotContains(t, err.Error(), path)
		}
	}
	assert.Zero(t, pidReads)
}

func TestTrailingPrefixRequiresStrictChildWhileExactPrefixAllowsRoot(t *testing.T) {
	t.Parallel()

	client, err := New("/runtime/eventing/")
	require.NoError(t, err)
	assert.False(t, validRequestPath(client.routePrefix, "/runtime/eventing/"))
	assert.True(t, validRequestPath(client.routePrefix, "/runtime/eventing/events"))
	assert.False(t, validRequestPath(client.routePrefix, "/runtime/eventing-foreign/events"))

	client, err = New(testRoutePrefix)
	require.NoError(t, err)
	assert.True(t, validRequestPath(client.routePrefix, testRoutePrefix))
	assert.True(t, validRequestPath(client.routePrefix, testRoutePath))
}

func TestDoJSONRejectsMethodAndBodyViolationsBeforePIDRead(t *testing.T) {
	client := testClient(t, httpDoerFunc(func(*http.Request) (*http.Response, error) {
		t.Fatal("HTTP request made for invalid input")
		return nil, nil
	}))
	pidReads := 0
	client.readPID = func(string) *ppid.PidFileData {
		pidReads++
		return nil
	}

	tests := []struct {
		name    string
		request Request
		want    string
		wantErr error
	}{
		{
			name:    "lowercase method",
			request: Request{Method: "get", Path: testRoutePath},
			want:    "method is invalid",
			wantErr: ErrInvalidMethod,
		},
		{
			name:    "put",
			request: Request{Method: http.MethodPut, Path: testRoutePath},
			want:    "method is invalid",
			wantErr: ErrInvalidMethod,
		},
		{
			name: "GET body",
			request: Request{
				Method: http.MethodGet, Path: testRoutePath, Body: []byte(`{}`),
			},
			want:    "cannot carry a body",
			wantErr: ErrGETBody,
		},
		{
			name:    "POST nil",
			request: Request{Method: http.MethodPost, Path: testRoutePath},
			want:    "body is invalid",
			wantErr: ErrInvalidRequestBody,
		},
		{
			name: "POST empty",
			request: Request{
				Method: http.MethodPost, Path: testRoutePath, Body: []byte{},
			},
			want:    "body is invalid",
			wantErr: ErrInvalidRequestBody,
		},
		{
			name: "POST array",
			request: Request{
				Method: http.MethodPost, Path: testRoutePath, Body: []byte(`[]`),
			},
			want:    "body is invalid",
			wantErr: ErrInvalidRequestBody,
		},
		{
			name: "POST scalar",
			request: Request{
				Method: http.MethodPost, Path: testRoutePath, Body: []byte(`"value"`),
			},
			want:    "body is invalid",
			wantErr: ErrInvalidRequestBody,
		},
		{
			name: "POST malformed",
			request: Request{
				Method: http.MethodPost, Path: testRoutePath, Body: []byte(`{"value":`),
			},
			want:    "body is invalid",
			wantErr: ErrInvalidRequestBody,
		},
		{
			name: "POST trailing",
			request: Request{
				Method: http.MethodPost, Path: testRoutePath, Body: []byte(`{} {}`),
			},
			want:    "body is invalid",
			wantErr: ErrInvalidRequestBody,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := client.DoJSON(context.Background(), test.request)
			require.ErrorContains(t, err, test.want)
			assert.ErrorIs(t, err, test.wantErr)
			assert.False(t, RequestMayHaveBeenSent(err))
		})
	}
	assert.Zero(t, pidReads)
}

func TestDoJSONBoundsRequestBodyAndEncodedQuery(t *testing.T) {
	t.Parallel()

	client := testClient(t, httpDoerFunc(func(*http.Request) (*http.Response, error) {
		return jsonResponse(http.StatusOK, `{}`), nil
	}))
	client.maxRequest = 2
	_, err := client.DoJSON(context.Background(), Request{
		Method: http.MethodPost, Path: testRoutePath, Body: []byte(`{}`),
	})
	require.NoError(t, err)
	_, err = client.DoJSON(context.Background(), Request{
		Method: http.MethodPost, Path: testRoutePath, Body: []byte(" {}"),
	})
	require.EqualError(t, err, "local gateway request body exceeds the safe limit")
	assert.ErrorIs(t, err, ErrRequestTooLarge)
	assert.False(t, RequestMayHaveBeenSent(err))

	client.maxQuery = 5
	_, err = client.DoJSON(context.Background(), Request{
		Method: http.MethodGet, Path: testRoutePath, Query: url.Values{"q": {"a b"}},
	})
	require.NoError(t, err)
	client.maxQuery = 4
	_, err = client.DoJSON(context.Background(), Request{
		Method: http.MethodGet, Path: testRoutePath, Query: url.Values{"q": {"a b"}},
	})
	require.EqualError(t, err, "local gateway request query exceeds the safe limit")
	assert.ErrorIs(t, err, ErrQueryTooLarge)
	assert.False(t, RequestMayHaveBeenSent(err))
}

func TestDoJSONRestoresSafeBoundsWhenInternalLimitsAreNonPositive(t *testing.T) {
	t.Parallel()

	client := testClient(t, httpDoerFunc(func(*http.Request) (*http.Response, error) {
		return jsonResponse(http.StatusOK, `{}`), nil
	}))
	client.timeout = 0
	client.maxRequest = 0
	client.maxResponse = 0
	client.maxQuery = 0

	_, err := client.DoJSON(context.Background(), Request{
		Method: http.MethodPost,
		Path:   testRoutePath,
		Query:  url.Values{"q": {"bounded"}},
		Body:   []byte(`{}`),
	})
	require.NoError(t, err)
	assert.Nil(t, dispatched(nil))
	assert.False(t, RequestMayHaveBeenSent(nil))
}

func TestDoJSONRejectsUnavailableOrUnsafePIDMetadata(t *testing.T) {
	invalidUTF8 := string([]byte{0xff})
	canary := "PID-HOST-TOKEN-CANARY"
	tests := []struct {
		name    string
		data    *ppid.PidFileData
		want    string
		wantErr error
	}{
		{
			name:    "missing",
			want:    "live gateway is unavailable",
			wantErr: ErrGatewayUnavailable,
		},
		{
			name:    "invalid pid",
			data:    metadata(0, "token", "127.0.0.1", 1),
			want:    "PID metadata is invalid",
			wantErr: ErrInvalidPIDMetadata,
		},
		{
			name:    "invalid port low",
			data:    metadata(1, "token", "127.0.0.1", 0),
			want:    "PID metadata is invalid",
			wantErr: ErrInvalidPIDMetadata,
		},
		{
			name: "invalid port high", data: metadata(1, "token", "127.0.0.1", 65536),
			want:    "PID metadata is invalid",
			wantErr: ErrInvalidPIDMetadata,
		},
		{
			name:    "empty token",
			data:    metadata(1, "", "127.0.0.1", 1),
			want:    "no valid bearer token",
			wantErr: ErrInvalidPIDToken,
		},
		{
			name: "spaced token", data: metadata(1, " token ", "127.0.0.1", 1),
			want:    "no valid bearer token",
			wantErr: ErrInvalidPIDToken,
		},
		{
			name: "header injection", data: metadata(1, canary+"\r\nInjected: yes", "127.0.0.1", 1),
			want:    "no valid bearer token",
			wantErr: ErrInvalidPIDToken,
		},
		{
			name: "non utf8 token", data: metadata(1, invalidUTF8, "127.0.0.1", 1),
			want:    "no valid bearer token",
			wantErr: ErrInvalidPIDToken,
		},
		{
			name:    "oversized token",
			data:    metadata(1, strings.Repeat("x", maxPIDTokenBytes+1), "127.0.0.1", 1),
			want:    "no valid bearer token",
			wantErr: ErrInvalidPIDToken,
		},
		{
			name: "URL host", data: metadata(1, "token", "http://"+canary, 1),
			want:    "PID metadata is invalid",
			wantErr: ErrInvalidPIDMetadata,
		},
		{
			name:    "oversized host",
			data:    metadata(1, "token", strings.Repeat("x", maxPIDHostBytes+1), 1),
			want:    "PID metadata is invalid",
			wantErr: ErrInvalidPIDMetadata,
		},
		{
			name: "invalid hostname", data: metadata(1, "token", "bad_host_"+canary, 1),
			want:    "PID metadata is invalid",
			wantErr: ErrInvalidPIDMetadata,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			client := testClient(t, httpDoerFunc(func(*http.Request) (*http.Response, error) {
				t.Fatal("HTTP request made with unsafe PID metadata")
				return nil, nil
			}))
			client.readPID = func(string) *ppid.PidFileData { return test.data }
			_, err := client.DoJSON(
				context.Background(),
				Request{Method: http.MethodGet, Path: testRoutePath},
			)
			require.ErrorContains(t, err, test.want)
			assert.ErrorIs(t, err, test.wantErr)
			assert.False(t, RequestMayHaveBeenSent(err))
			assert.NotContains(t, err.Error(), canary)
			assert.NotContains(t, err.Error(), "Injected")
		})
	}
}

func TestDoJSONTreatsDeadPIDFileAsUnavailableBeforeHTTP(t *testing.T) {
	t.Parallel()

	homePath := t.TempDir()
	raw := []byte(`{"pid":99999999,"token":"deadbeef","host":"127.0.0.1","port":18790}`)
	pidPath := filepath.Join(homePath, ".picoclaw.pid")
	require.NoError(t, os.WriteFile(pidPath, raw, 0o600))

	client, err := New(testRoutePrefix)
	require.NoError(t, err)
	client.homePath = func() string { return homePath }
	client.http = httpDoerFunc(func(*http.Request) (*http.Response, error) {
		t.Fatal("HTTP request made for dead PID")
		return nil, nil
	})

	_, err = client.DoJSON(
		context.Background(),
		Request{Method: http.MethodGet, Path: testRoutePath},
	)
	require.ErrorIs(t, err, ErrGatewayUnavailable)
	assert.False(t, RequestMayHaveBeenSent(err))
	_, statErr := os.Stat(pidPath)
	assert.ErrorIs(t, statErr, os.ErrNotExist)
}

func TestDoJSONNormalizesWildcardAndIPv6ProbeHosts(t *testing.T) {
	for _, host := range []string{"", "*", "0.0.0.0", "::", "[::1]"} {
		t.Run(strings.ReplaceAll(host, ":", "_"), func(t *testing.T) {
			client := testClient(
				t,
				httpDoerFunc(func(request *http.Request) (*http.Response, error) {
					assert.NotContains(t, request.URL.Host, "0.0.0.0")
					assert.NotEqual(t, "[::]:18790", request.URL.Host)
					parsedHost, parsedPort, err := net.SplitHostPort(request.URL.Host)
					require.NoError(t, err)
					assert.NotEmpty(t, parsedHost)
					assert.Equal(t, "18790", parsedPort)
					return jsonResponse(http.StatusOK, `{}`), nil
				}),
			)
			client.readPID = func(string) *ppid.PidFileData {
				return &ppid.PidFileData{PID: 1, Token: "token", Host: host, Port: 18790}
			}
			_, err := client.DoJSON(
				context.Background(),
				Request{Method: http.MethodGet, Path: testRoutePath},
			)
			require.NoError(t, err, host)
		})
	}
}

func TestDoJSONValidatesAndBoundsEveryResponse(t *testing.T) {
	tests := []struct {
		name        string
		response    func() *http.Response
		want        string
		wantErr     error
		maxResponse int64
	}{
		{
			name:     "nil response",
			response: func() *http.Response { return nil },
			want:     "invalid response",
			wantErr:  ErrInvalidResponse,
		},
		{name: "nil body", response: func() *http.Response {
			return &http.Response{
				StatusCode: 200,
				Header:     http.Header{"Content-Type": {"application/json"}},
			}
		}, want: "invalid response", wantErr: ErrInvalidResponse},
		{
			name: "invalid status",
			response: func() *http.Response {
				return jsonResponse(1000, `{}`)
			},
			want:    "invalid response",
			wantErr: ErrInvalidResponse,
		},
		{name: "missing content type", response: func() *http.Response {
			value := jsonResponse(200, `{}`)
			value.Header.Del("Content-Type")
			return value
		}, want: "invalid response", wantErr: ErrInvalidResponse},
		{name: "wrong content type", response: func() *http.Response {
			value := jsonResponse(200, `{}`)
			value.Header.Set("Content-Type", "text/plain")
			return value
		}, want: "invalid response", wantErr: ErrInvalidResponse},
		{name: "wrong charset", response: func() *http.Response {
			value := jsonResponse(200, `{}`)
			value.Header.Set("Content-Type", "application/json; charset=latin1")
			return value
		}, want: "invalid response", wantErr: ErrInvalidResponse},
		{name: "extra content type parameter", response: func() *http.Response {
			value := jsonResponse(200, `{}`)
			value.Header.Set("Content-Type", "application/json; profile=internal")
			return value
		}, want: "invalid response", wantErr: ErrInvalidResponse},
		{
			name:     "empty body",
			response: func() *http.Response { return jsonResponse(200, "") },
			want:     "invalid JSON",
			wantErr:  ErrInvalidJSON,
		},
		{
			name:     "array",
			response: func() *http.Response { return jsonResponse(200, `[]`) },
			want:     "invalid JSON",
			wantErr:  ErrInvalidJSON,
		},
		{
			name:     "scalar",
			response: func() *http.Response { return jsonResponse(200, `true`) },
			want:     "invalid JSON",
			wantErr:  ErrInvalidJSON,
		},
		{
			name:     "malformed",
			response: func() *http.Response { return jsonResponse(200, `{"value":`) },
			want:     "invalid JSON",
			wantErr:  ErrInvalidJSON,
		},
		{
			name:     "trailing object",
			response: func() *http.Response { return jsonResponse(200, `{} {}`) },
			want:     "invalid JSON",
			wantErr:  ErrInvalidJSON,
		},
		{
			name:        "content length",
			response:    func() *http.Response { value := jsonResponse(200, `{}`); value.ContentLength = 17; return value },
			want:        "exceeds the safe limit",
			wantErr:     ErrResponseTooLarge,
			maxResponse: 16,
		},
		{name: "streamed overflow", response: func() *http.Response {
			value := jsonResponse(200, strings.Repeat(" ", 17))
			value.ContentLength = -1
			return value
		}, want: "exceeds the safe limit", wantErr: ErrResponseTooLarge, maxResponse: 16},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			client := testClient(t, httpDoerFunc(func(*http.Request) (*http.Response, error) {
				return test.response(), nil
			}))
			if test.maxResponse != 0 {
				client.maxResponse = test.maxResponse
			}
			response, err := client.DoJSON(
				context.Background(),
				Request{Method: http.MethodGet, Path: testRoutePath},
			)
			require.ErrorContains(t, err, test.want)
			assert.ErrorIs(t, err, test.wantErr)
			assert.True(t, RequestMayHaveBeenSent(err))
			if test.name == "nil response" || test.name == "nil body" {
				assert.Zero(t, response.StatusCode)
			} else if test.name == "invalid status" {
				assert.Equal(t, 1000, response.StatusCode)
			} else {
				assert.Equal(t, http.StatusOK, response.StatusCode)
			}
		})
	}
}

func TestDoJSONRetainsHTTPStatusWhenPayloadValidationFails(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(*http.Response)
		wantErr error
	}{
		{
			name: "content type",
			mutate: func(response *http.Response) {
				response.Header.Set("Content-Type", "text/plain")
			},
			wantErr: ErrInvalidResponse,
		},
		{
			name: "JSON",
			mutate: func(response *http.Response) {
				response.Body = io.NopCloser(strings.NewReader(`[]`))
			},
			wantErr: ErrInvalidJSON,
		},
		{
			name: "size",
			mutate: func(response *http.Response) {
				response.ContentLength = 1 << 20
			},
			wantErr: ErrResponseTooLarge,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			client := testClient(t, httpDoerFunc(func(*http.Request) (*http.Response, error) {
				response := jsonResponse(http.StatusNotFound, `{}`)
				test.mutate(response)
				return response, nil
			}))
			client.maxResponse = 16
			response, err := client.DoJSON(
				context.Background(),
				Request{Method: http.MethodGet, Path: testRoutePath},
			)
			require.Error(t, err)
			assert.ErrorIs(t, err, test.wantErr)
			assert.True(t, RequestMayHaveBeenSent(err))
			assert.Equal(t, http.StatusNotFound, response.StatusCode)
			assert.Nil(t, response.Body)
		})
	}
}

type failingReadCloser struct {
	closed atomic.Bool
}

func (*failingReadCloser) Read([]byte) (int, error) {
	return 0, errors.New("read failure with SERVER-RESPONSE-CANARY")
}

func (reader *failingReadCloser) Close() error {
	reader.closed.Store(true)
	return nil
}

func TestDoJSONClosesResponseAndHidesReadFailure(t *testing.T) {
	t.Parallel()

	body := &failingReadCloser{}
	client := testClient(t, httpDoerFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": {"application/json"}},
			Body:       body,
		}, nil
	}))
	response, err := client.DoJSON(
		context.Background(),
		Request{Method: http.MethodGet, Path: testRoutePath},
	)
	require.EqualError(t, err, "failed to read local gateway response")
	assert.ErrorIs(t, err, ErrResponseRead)
	assert.True(t, RequestMayHaveBeenSent(err))
	assert.Equal(t, http.StatusOK, response.StatusCode)
	assert.NotContains(t, err.Error(), "SERVER-RESPONSE-CANARY")
	assert.True(t, body.closed.Load())
}

func TestDoJSONPreservesExactJSONObjectTokensAndControls(t *testing.T) {
	t.Parallel()

	raw := " \n{\"integer\":9007199254740993,\"tiny\":1e-400,\"text\":\"\\u001b[31m\"}\t"
	client := testClient(t, httpDoerFunc(func(*http.Request) (*http.Response, error) {
		response := jsonResponse(http.StatusTeapot, raw)
		response.Header.Set("Content-Type", "APPLICATION/JSON; CHARSET=UTF-8")
		return response, nil
	}))
	client.maxResponse = int64(len(raw))
	response, err := client.DoJSON(
		context.Background(),
		Request{Method: http.MethodGet, Path: testRoutePath},
	)
	require.NoError(t, err)
	assert.Equal(t, http.StatusTeapot, response.StatusCode)
	assert.Equal(t, raw, string(response.Body))
	assert.NotContains(t, string(response.Body), "\x1b")
}

func TestDoJSONHidesTransportErrorDetails(t *testing.T) {
	t.Parallel()

	client := testClient(t, httpDoerFunc(func(*http.Request) (*http.Response, error) {
		return nil, errors.New("dial host token BODY-TRANSPORT-CANARY")
	}))
	_, err := client.DoJSON(
		context.Background(),
		Request{Method: http.MethodGet, Path: testRoutePath},
	)
	require.EqualError(t, err, "local gateway API is unavailable")
	assert.ErrorIs(t, err, ErrAPIUnavailable)
	assert.True(t, RequestMayHaveBeenSent(err))
	assert.NotContains(t, err.Error(), "BODY-TRANSPORT-CANARY")
	assert.NotContains(t, err.Error(), "test-bearer-token")
}

func TestDoJSONDistinguishesPreDispatchAndPossiblyDispatchedFailures(t *testing.T) {
	t.Parallel()

	client := testClient(t, httpDoerFunc(func(*http.Request) (*http.Response, error) {
		return jsonResponse(http.StatusOK, `[]`), nil
	}))
	_, err := client.DoJSON(context.Background(), Request{
		Method: http.MethodGet,
		Path:   "/runtime/eventing/foreign",
	})
	require.ErrorIs(t, err, ErrInvalidPath)
	assert.False(t, RequestMayHaveBeenSent(err))

	client.readPID = func(string) *ppid.PidFileData { return nil }
	_, err = client.DoJSON(
		context.Background(),
		Request{Method: http.MethodGet, Path: testRoutePath},
	)
	require.ErrorIs(t, err, ErrGatewayUnavailable)
	assert.False(t, RequestMayHaveBeenSent(err))

	client.readPID = func(string) *ppid.PidFileData {
		return &ppid.PidFileData{
			PID: 1234, Token: "test-bearer-token", Host: "127.0.0.1", Port: 18790,
		}
	}
	_, err = client.DoJSON(
		context.Background(),
		Request{Method: http.MethodGet, Path: testRoutePath},
	)
	require.ErrorIs(t, err, ErrInvalidJSON)
	assert.True(t, RequestMayHaveBeenSent(err))
}

func TestDoJSONHonorsCancellationAndTimeout(t *testing.T) {
	t.Run("caller cancellation", func(t *testing.T) {
		client := testClient(t, httpDoerFunc(func(request *http.Request) (*http.Response, error) {
			<-request.Context().Done()
			return nil, errors.New("transport wrapped CANCELLATION-CANARY")
		}))
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		_, err := client.DoJSON(ctx, Request{Method: http.MethodGet, Path: testRoutePath})
		require.Error(t, err)
		assert.ErrorIs(t, err, context.Canceled)
		assert.True(t, RequestMayHaveBeenSent(err))
		assert.NotContains(t, err.Error(), "CANCELLATION-CANARY")
	})

	t.Run("client timeout", func(t *testing.T) {
		client := testClient(t, httpDoerFunc(func(request *http.Request) (*http.Response, error) {
			<-request.Context().Done()
			return nil, errors.New("transport wrapped TIMEOUT-CANARY")
		}))
		client.timeout = 5 * time.Millisecond
		_, err := client.DoJSON(
			context.Background(),
			Request{Method: http.MethodGet, Path: testRoutePath},
		)
		require.Error(t, err)
		assert.ErrorIs(t, err, context.DeadlineExceeded)
		assert.True(t, RequestMayHaveBeenSent(err))
		assert.NotContains(t, err.Error(), "TIMEOUT-CANARY")
	})
}

func TestDoJSONDoesNotFollowRedirectOrForwardBearer(t *testing.T) {
	t.Parallel()

	var redirected atomic.Int64
	destination := httptest.NewServer(
		http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
			redirected.Add(1)
			if request.Header.Get("Authorization") != "" {
				t.Error("bearer token forwarded to redirect destination")
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{}`))
		}),
	)
	defer destination.Close()

	source := httptest.NewServer(
		http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
			assert.Equal(t, "Bearer redirect-token", request.Header.Get("Authorization"))
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("Location", destination.URL+testRoutePath)
			w.WriteHeader(http.StatusTemporaryRedirect)
			_, _ = w.Write([]byte(`{"redirected":false}`))
		}),
	)
	defer source.Close()

	parsed, err := url.Parse(source.URL)
	require.NoError(t, err)
	port, err := strconv.Atoi(parsed.Port())
	require.NoError(t, err)
	client, err := New(testRoutePrefix)
	require.NoError(t, err)
	client.homePath = func() string { return "/test/home" }
	client.readPID = func(string) *ppid.PidFileData {
		return &ppid.PidFileData{
			PID:   1,
			Token: "redirect-token",
			Host:  parsed.Hostname(),
			Port:  port,
		}
	}
	response, err := client.DoJSON(
		context.Background(),
		Request{Method: http.MethodGet, Path: testRoutePath},
	)
	require.NoError(t, err)
	assert.Equal(t, http.StatusTemporaryRedirect, response.StatusCode)
	assert.JSONEq(t, `{"redirected":false}`, string(response.Body))
	assert.Zero(t, redirected.Load())
}

func TestDoJSONRejectsUnavailableClientDependencies(t *testing.T) {
	var nilClient *Client
	_, err := nilClient.DoJSON(
		context.Background(),
		Request{Method: http.MethodGet, Path: testRoutePath},
	)
	require.EqualError(t, err, "local gateway client is unavailable")
	assert.ErrorIs(t, err, ErrClientUnavailable)

	base := testClient(t, httpDoerFunc(func(*http.Request) (*http.Response, error) {
		return jsonResponse(http.StatusOK, `{}`), nil
	}))
	for name, mutate := range map[string]func(*Client){
		"home":   func(value *Client) { value.homePath = nil },
		"pid":    func(value *Client) { value.readPID = nil },
		"http":   func(value *Client) { value.http = nil },
		"prefix": func(value *Client) { value.routePrefix = "/api/foreign" },
	} {
		t.Run(name, func(t *testing.T) {
			clientCopy := *base
			mutate(&clientCopy)
			_, err := clientCopy.DoJSON(
				context.Background(),
				Request{Method: http.MethodGet, Path: testRoutePath},
			)
			require.EqualError(t, err, "local gateway client is unavailable")
		})
	}
}

func TestDoJSONConcurrentRequestsKeepRouteAndCredentialsIsolated(t *testing.T) {
	t.Parallel()

	const requests = 32
	var calls atomic.Int64
	client := testClient(t, httpDoerFunc(func(request *http.Request) (*http.Response, error) {
		assert.True(t, strings.HasPrefix(request.URL.Path, testRoutePrefix+"/"))
		assert.Equal(t, "Bearer test-bearer-token", request.Header.Get("Authorization"))
		calls.Add(1)
		return jsonResponse(http.StatusOK, `{}`), nil
	}))

	errorsFound := make(chan error, requests)
	for index := 0; index < requests; index++ {
		go func() {
			_, err := client.DoJSON(context.Background(), Request{
				Method: http.MethodGet,
				Path: testRoutePrefix + "/devw_" + strings.Repeat(
					string("0123456789abcdef"[index%16]),
					32,
				),
			})
			errorsFound <- err
		}()
	}
	for index := 0; index < requests; index++ {
		require.NoError(t, <-errorsFound)
	}
	assert.Equal(t, int64(requests), calls.Load())
}

func TestJSONContentTypeAndExactObjectHelpers(t *testing.T) {
	t.Parallel()

	for _, value := range []string{
		"application/json",
		"application/json; charset=utf-8",
		"APPLICATION/JSON; CHARSET=UTF-8",
	} {
		assert.True(t, jsonResponseContentType(value), value)
	}
	for _, value := range []string{
		"",
		"text/json",
		"application/problem+json",
		"application/json; charset=latin1",
		"application/json; profile=private",
		"application/json; charset=utf-8; profile=private",
	} {
		assert.False(t, jsonResponseContentType(value), value)
	}

	for _, value := range [][]byte{[]byte(`{}`), []byte(" \n{\"a\":1}\t")} {
		assert.True(t, exactJSONObject(value), string(value))
	}
	for _, value := range [][]byte{nil, []byte(``), []byte(`[]`), []byte(`null`), []byte(`{}[]`), bytes.Repeat([]byte(`{}`), 2)} {
		assert.False(t, exactJSONObject(value), string(value))
	}
}
