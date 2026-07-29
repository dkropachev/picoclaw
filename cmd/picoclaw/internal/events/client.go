package events

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
	eventoperator "github.com/sipeed/picoclaw/pkg/eventing/operator"
	"github.com/sipeed/picoclaw/pkg/netbind"
	ppid "github.com/sipeed/picoclaw/pkg/pid"
)

const (
	eventRequestTimeout         = 10 * time.Second
	maxEventResponseBytes int64 = 8 << 20
	maxPIDHostBytes             = 1024
	maxPIDTokenBytes            = 4096

	replayUnknownOutcomeMessage = "replay outcome unknown; inspect events before retrying"
)

type httpDoer interface {
	Do(request *http.Request) (*http.Response, error)
}

type gatewayClient struct {
	homePath func() string
	readPID  func(string) *ppid.PidFileData
	http     httpDoer
	timeout  time.Duration
	maxBytes int64
}

func newGatewayClient() *gatewayClient {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	// Gateway operations are process-local. Never disclose the PID bearer token
	// to a configured HTTP proxy.
	transport.Proxy = nil
	return &gatewayClient{
		homePath: internal.GetPicoclawHome,
		readPID:  ppid.ReadPidFileWithCheck,
		http: &http.Client{
			Transport: transport,
			Timeout:   eventRequestTimeout,
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
		timeout:  eventRequestTimeout,
		maxBytes: maxEventResponseBytes,
	}
}

func (client *gatewayClient) get(
	ctx context.Context,
	path string,
	query url.Values,
) ([]byte, error) {
	raw, err := client.request(
		ctx,
		http.MethodGet,
		path,
		query,
		nil,
		http.StatusOK,
	)
	if err != nil {
		return nil, err
	}
	return prettyJSON(raw)
}

func (client *gatewayClient) payload(
	ctx context.Context,
	eventID string,
) ([]byte, error) {
	raw, err := client.request(
		ctx,
		http.MethodGet,
		eventoperator.RoutePrefix+"events/"+eventID+"/payload",
		nil,
		nil,
		http.StatusOK,
	)
	if err != nil {
		return nil, err
	}
	if !exactJSONObject(raw) {
		return nil, errors.New("live gateway event API returned invalid JSON")
	}
	return raw, nil
}

func (client *gatewayClient) replay(
	ctx context.Context,
	eventID string,
) ([]byte, error) {
	raw, err := client.request(
		ctx,
		http.MethodPost,
		eventoperator.RoutePrefix+"events/"+eventID+"/replay",
		nil,
		[]byte("{}"),
		http.StatusCreated,
	)
	if err != nil {
		return nil, err
	}
	formatted, err := prettyJSON(raw)
	if err != nil {
		return nil, replayOutcomeUnknownError()
	}
	return formatted, nil
}

func (client *gatewayClient) request(
	ctx context.Context,
	method string,
	path string,
	query url.Values,
	body []byte,
	expectedStatus int,
) ([]byte, error) {
	if client == nil || client.homePath == nil || client.readPID == nil || client.http == nil {
		return nil, errors.New("live gateway event client is unavailable")
	}
	pidData := client.readPID(client.homePath())
	endpoint, token, err := gatewayEndpoint(pidData, path, query)
	if err != nil {
		return nil, err
	}

	timeout := client.timeout
	if timeout <= 0 {
		timeout = eventRequestTimeout
	}
	if ctx == nil {
		ctx = context.Background()
	}
	requestCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	var requestBody io.Reader
	if body != nil {
		requestBody = bytes.NewReader(body)
	}
	request, err := http.NewRequestWithContext(requestCtx, method, endpoint.String(), requestBody)
	if err != nil {
		return nil, errors.New("live gateway PID metadata is invalid; restart the gateway")
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("Authorization", "Bearer "+token)
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	replayRequest := method == http.MethodPost &&
		strings.HasSuffix(path, "/replay")

	response, err := client.http.Do(request)
	if err != nil {
		if replayRequest {
			return nil, replayOutcomeUnknownError()
		}
		if requestCtx.Err() != nil {
			return nil, fmt.Errorf("live gateway event request failed: %w", requestCtx.Err())
		}
		return nil, errors.New("live gateway event API is unavailable")
	}
	if response == nil || response.Body == nil {
		if replayRequest {
			return nil, replayOutcomeUnknownError()
		}
		return nil, errors.New("live gateway event API returned an invalid response")
	}
	defer response.Body.Close()

	if response.StatusCode != expectedStatus {
		if replayRequest && !safeReplayFailureStatus(response.StatusCode) {
			return nil, replayOutcomeUnknownError()
		}
		return nil, gatewayStatusError(response.StatusCode)
	}
	if !jsonResponseContentType(response.Header.Get("Content-Type")) {
		if replayRequest {
			return nil, replayOutcomeUnknownError()
		}
		return nil, errors.New("live gateway event API returned an invalid response")
	}

	maxBytes := client.maxBytes
	if maxBytes <= 0 {
		maxBytes = maxEventResponseBytes
	}
	if response.ContentLength > maxBytes {
		if replayRequest {
			return nil, replayOutcomeUnknownError()
		}
		return nil, errors.New("live gateway event response exceeds the safe display limit")
	}
	raw, err := io.ReadAll(io.LimitReader(response.Body, maxBytes+1))
	if err != nil {
		if replayRequest {
			return nil, replayOutcomeUnknownError()
		}
		return nil, errors.New("failed to read live gateway event response")
	}
	if int64(len(raw)) > maxBytes {
		if replayRequest {
			return nil, replayOutcomeUnknownError()
		}
		return nil, errors.New("live gateway event response exceeds the safe display limit")
	}
	return raw, nil
}

func safeReplayFailureStatus(status int) bool {
	switch status {
	case http.StatusBadRequest,
		http.StatusUnauthorized,
		http.StatusForbidden,
		http.StatusNotFound,
		http.StatusServiceUnavailable:
		return true
	default:
		return false
	}
}

func replayOutcomeUnknownError() error {
	return errors.New(replayUnknownOutcomeMessage)
}

func gatewayEndpoint(
	pidData *ppid.PidFileData,
	path string,
	query url.Values,
) (*url.URL, string, error) {
	if pidData == nil {
		return nil, "", errors.New("live gateway is unavailable; start picoclaw gateway and retry")
	}
	if pidData.PID <= 0 || pidData.Port <= 0 || pidData.Port > 65535 {
		return nil, "", errors.New("live gateway PID metadata is invalid; restart the gateway")
	}

	token := pidData.Token
	if token == "" ||
		token != strings.TrimSpace(token) ||
		len(token) > maxPIDTokenBytes ||
		!utf8.ValidString(token) ||
		!validBearerToken(token) {
		return nil, "", errors.New("live gateway PID metadata has no valid bearer token; restart the gateway")
	}

	rawHost := strings.TrimSpace(pidData.Host)
	if len(rawHost) > maxPIDHostBytes {
		return nil, "", errors.New("live gateway PID metadata is invalid; restart the gateway")
	}
	plan, err := netbind.BuildPlan(rawHost, netbind.DefaultLoopback)
	if err != nil {
		return nil, "", errors.New("live gateway PID metadata is invalid; restart the gateway")
	}
	host := strings.TrimSpace(plan.ProbeHost)
	if !validGatewayProbeHost(host) {
		return nil, "", errors.New("live gateway PID metadata is invalid; restart the gateway")
	}
	if path == "" || path[0] != '/' || !strings.HasPrefix(path, eventoperator.RoutePrefix) {
		return nil, "", errors.New("live gateway event request path is invalid")
	}

	endpoint := &url.URL{
		Scheme: "http",
		Host:   net.JoinHostPort(host, strconv.Itoa(pidData.Port)),
		Path:   path,
	}
	if len(query) > 0 {
		endpoint.RawQuery = query.Encode()
	}
	return endpoint, token, nil
}

func validBearerToken(token string) bool {
	for _, char := range token {
		if char < 0x21 || char > 0x7e {
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
		if len(label) == 0 || len(label) > 63 ||
			label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, char := range label {
			if char >= 'a' && char <= 'z' ||
				char >= 'A' && char <= 'Z' ||
				char >= '0' && char <= '9' ||
				char == '-' {
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
		if !strings.EqualFold(name, "charset") ||
			!strings.EqualFold(parameter, "utf-8") {
			return false
		}
	}
	return true
}

func prettyJSON(raw []byte) ([]byte, error) {
	raw = bytes.TrimSpace(raw)
	if !exactJSONObject(raw) {
		return nil, errors.New("live gateway event API returned invalid JSON")
	}
	var formatted bytes.Buffer
	if err := json.Indent(&formatted, raw, "", "  "); err != nil {
		return nil, errors.New("live gateway event API returned invalid JSON")
	}
	formatted.WriteByte('\n')
	return formatted.Bytes(), nil
}

func exactJSONObject(raw []byte) bool {
	trimmed := bytes.TrimSpace(raw)
	return len(trimmed) > 0 && trimmed[0] == '{' && json.Valid(raw)
}

func gatewayStatusError(status int) error {
	switch status {
	case http.StatusBadRequest:
		return errors.New("gateway rejected the event request (400 Bad Request)")
	case http.StatusUnauthorized, http.StatusForbidden:
		return errors.New("live gateway event credentials are unavailable or stale")
	case http.StatusNotFound:
		return errors.New("durable event operations are unavailable on the running gateway")
	case http.StatusServiceUnavailable:
		return errors.New("durable event operations are temporarily unavailable on the running gateway")
	default:
		if status < 100 || status > 999 {
			return errors.New("live gateway event API returned an invalid status")
		}
		return fmt.Errorf("live gateway event request failed with HTTP status %d", status)
	}
}
