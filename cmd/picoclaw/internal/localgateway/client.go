// Package localgateway provides the protected process-local HTTP transport
// shared by PicoClaw CLI commands. It discovers the running Gateway through
// its PID file and never accepts a caller-supplied endpoint or bearer token.
package localgateway

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/sipeed/picoclaw/cmd/picoclaw/internal"
	"github.com/sipeed/picoclaw/pkg/netbind"
	ppid "github.com/sipeed/picoclaw/pkg/pid"
)

const (
	requestTimeout            = 10 * time.Second
	maxRequestBodyBytes       = 1 << 20
	maxResponseBytes    int64 = 8 << 20
	maxQueryBytes             = 64 << 10
	maxPIDHostBytes           = 1024
	maxPIDTokenBytes          = 4096
)

// Stable error identities let command adapters preserve their existing
// user-facing and unknown-outcome semantics without matching error strings.
var (
	ErrInvalidRoutePrefix = errors.New("local gateway route prefix is invalid")
	ErrClientUnavailable  = errors.New("local gateway client is unavailable")
	ErrInvalidMethod      = errors.New("local gateway request method is invalid")
	ErrInvalidPath        = errors.New("local gateway request path is invalid")
	ErrQueryTooLarge      = errors.New("local gateway request query exceeds the safe limit")
	ErrGETBody            = errors.New("local gateway GET request cannot carry a body")
	ErrRequestTooLarge    = errors.New("local gateway request body exceeds the safe limit")
	ErrInvalidRequestBody = errors.New("local gateway request body is invalid")
	ErrGatewayUnavailable = errors.New(
		"live gateway is unavailable; start picoclaw gateway and retry",
	)
	ErrInvalidPIDMetadata = errors.New(
		"live gateway PID metadata is invalid; restart the gateway",
	)
	ErrInvalidPIDToken = errors.New(
		"live gateway PID metadata has no valid bearer token; restart the gateway",
	)
	ErrInvalidRequest         = errors.New("local gateway request is invalid")
	ErrAPIUnavailable         = errors.New("local gateway API is unavailable")
	ErrInvalidResponse        = errors.New("local gateway API returned an invalid response")
	ErrResponseTooLarge       = errors.New("local gateway response exceeds the safe limit")
	ErrResponseRead           = errors.New("failed to read local gateway response")
	ErrInvalidJSON            = errors.New("local gateway API returned invalid JSON")
	ErrRequestMayHaveBeenSent = errors.New("local gateway request may have been sent")
)

// Request is one canonical JSON request beneath the route prefix frozen by
// New. Path is an already-assembled trusted runtime path, never a URL.
type Request struct {
	Method string
	Path   string
	Query  url.Values
	Body   []byte
}

// Response retains the exact bounded JSON object returned by the Gateway.
// Callers own endpoint-specific status and payload semantics. StatusCode is
// also retained when a syntactically valid HTTP response has an invalid JSON
// payload, allowing callers with status-before-body semantics to preserve
// their existing outcome classification.
type Response struct {
	StatusCode int
	Body       []byte
}

type httpDoer interface {
	Do(request *http.Request) (*http.Response, error)
}

type dispatchedError struct {
	err error
}

func (failure *dispatchedError) Error() string {
	return failure.err.Error()
}

func (failure *dispatchedError) Unwrap() error {
	return failure.err
}

func (*dispatchedError) Is(target error) bool {
	return target == ErrRequestMayHaveBeenSent
}

func dispatched(err error) error {
	if err == nil {
		return nil
	}
	return &dispatchedError{err: err}
}

// RequestMayHaveBeenSent reports whether DoJSON reached the HTTP transport.
// It is deliberately conservative: even a transport or context error may
// follow a committed mutation, so callers must reconcile before retrying.
func RequestMayHaveBeenSent(err error) bool {
	return errors.Is(err, ErrRequestMayHaveBeenSent)
}

// Client is a process-local, PID-authenticated Gateway client. Its route
// prefix and transport dependencies are immutable after construction.
type Client struct {
	routePrefix string
	homePath    func() string
	readPID     func(string) *ppid.PidFileData
	http        httpDoer
	timeout     time.Duration
	maxRequest  int
	maxResponse int64
	maxQuery    int
}

// New freezes one canonical protected runtime subtree. The returned client
// has no facility for replacing the PID-derived host or bearer token.
func New(routePrefix string) (*Client, error) {
	if !validRoutePrefix(routePrefix) {
		return nil, ErrInvalidRoutePrefix
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	// PID bearer credentials are process-local authority. Never disclose them
	// to an environment-configured proxy.
	transport.Proxy = nil
	return &Client{
		routePrefix: routePrefix,
		homePath:    internal.GetPicoclawHome,
		readPID:     ppid.ReadPidFileWithCheck,
		http: &http.Client{
			Transport: transport,
			Timeout:   requestTimeout,
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
		timeout:     requestTimeout,
		maxRequest:  maxRequestBodyBytes,
		maxResponse: maxResponseBytes,
		maxQuery:    maxQueryBytes,
	}, nil
}

// DoJSON sends one canonical GET or POST and returns the exact bounded JSON
// object response. HTTP error statuses are returned as Response values so the
// endpoint owner can classify idempotency and unknown outcomes correctly.
func (client *Client) DoJSON(ctx context.Context, input Request) (Response, error) {
	if client == nil || client.homePath == nil || client.readPID == nil || client.http == nil ||
		!validRoutePrefix(client.routePrefix) {
		return Response{}, ErrClientUnavailable
	}
	prepared, err := client.prepareRequest(input)
	if err != nil {
		return Response{}, err
	}

	pidData := client.readPID(client.homePath())
	endpoint, token, err := gatewayEndpoint(pidData, prepared.path, prepared.rawQuery)
	if err != nil {
		return Response{}, err
	}

	timeout := client.timeout
	if timeout <= 0 {
		timeout = requestTimeout
	}
	if ctx == nil {
		ctx = context.Background()
	}
	requestCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	var body io.Reader
	if prepared.method == http.MethodPost {
		body = bytes.NewReader(prepared.body)
	}
	request, err := http.NewRequestWithContext(requestCtx, prepared.method, endpoint.String(), body)
	if err != nil {
		return Response{}, ErrInvalidRequest
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("Authorization", "Bearer "+token)
	if prepared.method == http.MethodPost {
		request.Header.Set("Content-Type", "application/json")
	}

	response, err := client.http.Do(request)
	if err != nil {
		if requestCtx.Err() != nil {
			return Response{}, dispatched(
				fmt.Errorf("local gateway request failed: %w", requestCtx.Err()),
			)
		}
		return Response{}, dispatched(ErrAPIUnavailable)
	}
	if response == nil {
		return Response{}, dispatched(ErrInvalidResponse)
	}
	if response.Body == nil {
		return Response{}, dispatched(ErrInvalidResponse)
	}
	defer response.Body.Close()
	result := Response{StatusCode: response.StatusCode}

	if response.StatusCode < 100 || response.StatusCode > 999 {
		return result, dispatched(ErrInvalidResponse)
	}
	if !jsonResponseContentType(response.Header.Get("Content-Type")) {
		return result, dispatched(ErrInvalidResponse)
	}
	maximum := client.maxResponse
	if maximum <= 0 {
		maximum = maxResponseBytes
	}
	if response.ContentLength > maximum {
		return result, dispatched(ErrResponseTooLarge)
	}
	raw, err := io.ReadAll(io.LimitReader(response.Body, maximum+1))
	if err != nil {
		return result, dispatched(ErrResponseRead)
	}
	if int64(len(raw)) > maximum {
		return result, dispatched(ErrResponseTooLarge)
	}
	if !exactJSONObject(raw) {
		return result, dispatched(ErrInvalidJSON)
	}
	result.Body = raw
	return result, nil
}

type preparedRequest struct {
	method   string
	path     string
	rawQuery string
	body     []byte
}

func (client *Client) prepareRequest(input Request) (preparedRequest, error) {
	if input.Method != http.MethodGet && input.Method != http.MethodPost {
		return preparedRequest{}, ErrInvalidMethod
	}
	if !validRequestPath(client.routePrefix, input.Path) {
		return preparedRequest{}, ErrInvalidPath
	}
	maximumQuery := client.maxQuery
	if maximumQuery <= 0 {
		maximumQuery = maxQueryBytes
	}
	rawQuery, err := encodeBoundedQuery(input.Query, maximumQuery)
	if err != nil {
		return preparedRequest{}, err
	}
	if input.Method == http.MethodGet {
		if input.Body != nil {
			return preparedRequest{}, ErrGETBody
		}
		return preparedRequest{
			method:   input.Method,
			path:     input.Path,
			rawQuery: rawQuery,
		}, nil
	}
	maximumRequest := client.maxRequest
	if maximumRequest <= 0 {
		maximumRequest = maxRequestBodyBytes
	}
	if len(input.Body) > maximumRequest {
		return preparedRequest{}, ErrRequestTooLarge
	}
	body := bytes.Clone(input.Body)
	if !exactJSONObject(body) {
		return preparedRequest{}, ErrInvalidRequestBody
	}
	return preparedRequest{
		method:   input.Method,
		path:     input.Path,
		rawQuery: rawQuery,
		body:     body,
	}, nil
}

func encodeBoundedQuery(query url.Values, maximum int) (string, error) {
	minimumEncodedBytes := 0
	snapshot := make(url.Values)
	for key, values := range query {
		var snapshotValues []string
		for _, value := range values {
			separatorBytes := 1
			if minimumEncodedBytes > 0 {
				separatorBytes++
			}
			remaining := maximum - minimumEncodedBytes
			if separatorBytes > remaining || len(key) > remaining-separatorBytes {
				return "", ErrQueryTooLarge
			}
			remaining -= separatorBytes + len(key)
			if len(value) > remaining {
				return "", ErrQueryTooLarge
			}
			minimumEncodedBytes += separatorBytes + len(key) + len(value)
			snapshotValues = append(snapshotValues, value)
		}
		if len(snapshotValues) > 0 {
			snapshot[key] = snapshotValues
		}
	}
	encoded := snapshot.Encode()
	if len(encoded) > maximum {
		return "", ErrQueryTooLarge
	}
	return encoded, nil
}

func validRoutePrefix(prefix string) bool {
	if !canonicalRuntimePath(prefix, true) || !strings.HasPrefix(prefix, "/runtime/") {
		return false
	}
	if prefix == "/runtime/" {
		return false
	}
	return true
}

func validRequestPath(prefix, path string) bool {
	if !canonicalRuntimePath(path, false) {
		return false
	}
	if strings.HasSuffix(prefix, "/") {
		return len(path) > len(prefix) && strings.HasPrefix(path, prefix)
	}
	return path == prefix || strings.HasPrefix(path, prefix+"/")
}

func canonicalRuntimePath(path string, allowTrailingSlash bool) bool {
	if path == "" || path[0] != '/' || !utf8.ValidString(path) ||
		strings.ContainsAny(path, "\\?#%") || strings.Contains(path, "//") ||
		!allowTrailingSlash && strings.HasSuffix(path, "/") {
		return false
	}
	segments := strings.Split(strings.TrimPrefix(path, "/"), "/")
	for index, segment := range segments {
		if segment == "" {
			return allowTrailingSlash && index == len(segments)-1 && index > 0
		}
		if segment == "." || segment == ".." {
			return false
		}
		for _, character := range segment {
			if character < 0x21 || character > 0x7e {
				return false
			}
		}
	}
	return true
}

func gatewayEndpoint(
	pidData *ppid.PidFileData,
	path string,
	rawQuery string,
) (*url.URL, string, error) {
	if pidData == nil {
		return nil, "", ErrGatewayUnavailable
	}
	if pidData.PID <= 0 || pidData.Port <= 0 || pidData.Port > 65535 {
		return nil, "", ErrInvalidPIDMetadata
	}

	token := pidData.Token
	if token == "" || token != strings.TrimSpace(token) || len(token) > maxPIDTokenBytes ||
		!utf8.ValidString(token) || !validBearerToken(token) {
		return nil, "", ErrInvalidPIDToken
	}

	rawHost := strings.TrimSpace(pidData.Host)
	if len(rawHost) > maxPIDHostBytes {
		return nil, "", ErrInvalidPIDMetadata
	}
	plan, err := netbind.BuildPlan(rawHost, netbind.DefaultLoopback)
	if err != nil {
		return nil, "", ErrInvalidPIDMetadata
	}
	host := strings.TrimSpace(plan.ProbeHost)
	if !validGatewayProbeHost(host) {
		return nil, "", ErrInvalidPIDMetadata
	}

	endpoint := &url.URL{
		Scheme: "http",
		Host:   net.JoinHostPort(host, strconv.Itoa(pidData.Port)),
		Path:   path,
	}
	endpoint.RawQuery = rawQuery
	return endpoint, token, nil
}

func validBearerToken(token string) bool {
	for _, character := range token {
		if character < 0x21 || character > 0x7e {
			return false
		}
	}
	return true
}

func validGatewayProbeHost(host string) bool {
	if host == "" || len(host) > 253 {
		return false
	}
	if ip := net.ParseIP(strings.Trim(host, "[]")); ip != nil {
		return true
	}
	if strings.EqualFold(host, "localhost") {
		return true
	}
	for _, label := range strings.Split(host, ".") {
		if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, character := range label {
			if character >= 'a' && character <= 'z' || character >= 'A' && character <= 'Z' ||
				character >= '0' && character <= '9' || character == '-' {
				continue
			}
			return false
		}
	}
	return true
}

func jsonResponseContentType(value string) bool {
	mediaType, parameters, err := mime.ParseMediaType(value)
	if err != nil || !strings.EqualFold(mediaType, "application/json") {
		return false
	}
	for name, parameter := range parameters {
		if !strings.EqualFold(name, "charset") || !strings.EqualFold(parameter, "utf-8") {
			return false
		}
	}
	return true
}

func exactJSONObject(raw []byte) bool {
	trimmed := bytes.TrimSpace(raw)
	return len(trimmed) > 0 && trimmed[0] == '{' && json.Valid(raw)
}
