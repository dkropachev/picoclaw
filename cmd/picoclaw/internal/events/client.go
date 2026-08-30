package events

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/sipeed/picoclaw/cmd/picoclaw/internal/localgateway"
	eventoperator "github.com/sipeed/picoclaw/pkg/eventing/operator"
)

const (
	replayUnknownOutcomeMessage = "replay outcome unknown; inspect events before retrying"
)

type jsonGateway interface {
	DoJSON(ctx context.Context, request localgateway.Request) (localgateway.Response, error)
}

type gatewayClient struct {
	transport jsonGateway
}

func newGatewayClient() *gatewayClient {
	transport, err := localgateway.New(eventoperator.RoutePrefix)
	if err != nil {
		return &gatewayClient{}
	}
	return &gatewayClient{transport: transport}
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
	if client == nil || client.transport == nil {
		return nil, errors.New("live gateway event client is unavailable")
	}
	replayRequest := method == http.MethodPost &&
		strings.HasSuffix(path, "/replay")
	response, err := client.transport.DoJSON(ctx, localgateway.Request{
		Method: method,
		Path:   path,
		Query:  query,
		Body:   body,
	})
	if response.StatusCode != 0 && response.StatusCode != expectedStatus {
		if replayRequest && !safeReplayFailureStatus(response.StatusCode) {
			return nil, replayOutcomeUnknownError()
		}
		return nil, gatewayStatusError(response.StatusCode)
	}
	if err != nil {
		if replayRequest && localgateway.RequestMayHaveBeenSent(err) {
			return nil, replayOutcomeUnknownError()
		}
		return nil, eventGatewayError(err)
	}
	if response.StatusCode != expectedStatus {
		if replayRequest {
			return nil, replayOutcomeUnknownError()
		}
		return nil, gatewayStatusError(response.StatusCode)
	}
	return response.Body, nil
}

func eventGatewayError(err error) error {
	switch {
	case errors.Is(err, context.Canceled):
		return fmt.Errorf("live gateway event request failed: %w", context.Canceled)
	case errors.Is(err, context.DeadlineExceeded):
		return fmt.Errorf("live gateway event request failed: %w", context.DeadlineExceeded)
	case errors.Is(err, localgateway.ErrClientUnavailable):
		return errors.New("live gateway event client is unavailable")
	case errors.Is(err, localgateway.ErrInvalidPath):
		return errors.New("live gateway event request path is invalid")
	case errors.Is(err, localgateway.ErrInvalidRequest):
		return errors.New("live gateway PID metadata is invalid; restart the gateway")
	case errors.Is(err, localgateway.ErrQueryTooLarge):
		return gatewayStatusError(http.StatusBadRequest)
	case errors.Is(err, localgateway.ErrAPIUnavailable):
		return errors.New("live gateway event API is unavailable")
	case errors.Is(err, localgateway.ErrInvalidResponse):
		return errors.New("live gateway event API returned an invalid response")
	case errors.Is(err, localgateway.ErrResponseTooLarge):
		return errors.New("live gateway event response exceeds the safe display limit")
	case errors.Is(err, localgateway.ErrResponseRead):
		return errors.New("failed to read live gateway event response")
	case errors.Is(err, localgateway.ErrInvalidJSON):
		return errors.New("live gateway event API returned invalid JSON")
	case errors.Is(err, localgateway.ErrGatewayUnavailable),
		errors.Is(err, localgateway.ErrInvalidPIDMetadata),
		errors.Is(err, localgateway.ErrInvalidPIDToken),
		errors.Is(err, localgateway.ErrInvalidMethod),
		errors.Is(err, localgateway.ErrGETBody),
		errors.Is(err, localgateway.ErrRequestTooLarge),
		errors.Is(err, localgateway.ErrInvalidRequestBody):
		return err
	default:
		return errors.New("live gateway event API returned an invalid response")
	}
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
		return errors.New(
			"durable event operations are temporarily unavailable on the running gateway",
		)
	default:
		if status < 100 || status > 999 {
			return errors.New("live gateway event API returned an invalid status")
		}
		return fmt.Errorf("live gateway event request failed with HTTP status %d", status)
	}
}
