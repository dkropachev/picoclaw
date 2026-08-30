// Package code implements the picoclaw code command.
package code

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"unicode/utf8"

	"github.com/sipeed/picoclaw/cmd/picoclaw/internal/localgateway"
	"github.com/sipeed/picoclaw/pkg/prworkspace"
)

const (
	clientMaxRequestBodyBytes  = 1 << 20
	clientMaxResponseBodyBytes = 8 << 20
	clientMaxTaskBytes         = 64 << 10
	clientMaxRepositoryBytes   = 8 << 10
	clientMaxRevisionBytes     = 256
	clientMaxAPIErrorCodeBytes = 64
)

var (
	ErrClientUnavailable = errors.New("development workspace client is unavailable")
	ErrInvalidRequest    = errors.New("development workspace request is invalid")
	ErrInvalidResponse   = errors.New("development workspace API returned an invalid response")
)

type clientGateway interface {
	DoJSON(ctx context.Context, request localgateway.Request) (localgateway.Response, error)
}

// Client calls only the protected process-local Development workspace API.
// Its transport freezes the runtime route and PID-derived authority.
type Client struct {
	transport clientGateway
}

// NewClient constructs a client for the single Development runtime subtree.
func NewClient() (*Client, error) {
	transport, err := localgateway.New(prworkspace.RuntimeRoutePrefix)
	if err != nil {
		return nil, err
	}
	return &Client{transport: transport}, nil
}

// Capabilities is the versioned readiness response checked before intake.
type Capabilities struct {
	Version               int      `json:"version"`
	ImplementFeatureReady bool     `json:"implement_feature_ready"`
	Missing               []string `json:"missing"`
}

// CreateRequest is the exact brief intake accepted by picoclaw code.
type CreateRequest struct {
	RequestID          string
	RepositoryIdentity string
	Content            string
}

// ConfirmCharterRequest fences acceptance of one exact charter revision.
type ConfirmCharterRequest struct {
	WorkspaceID             string
	ExpectedVersion         int64
	ExpectedCharterRevision int64
	RequestID               string
}

// RespondGateRequest answers one exact durable gate revision.
type RespondGateRequest struct {
	WorkspaceID     string
	GateID          string
	ExpectedVersion int64
	RequestID       string
	FieldValues     map[string]any
}

// ReconcilePublicationRequest reconciles one ambiguous branch publication.
type ReconcilePublicationRequest struct {
	WorkspaceID          string
	PublicationID        string
	ExpectedVersion      int64
	RequestID            string
	ExpectedHeadRevision string
}

// APIError is the bounded stable projection of a non-success API response.
// Server messages and raw bodies are deliberately excluded.
type APIError struct {
	StatusCode int
	Code       string
	Current    *prworkspace.Aggregate
	dispatched bool
}

func (failure *APIError) Error() string {
	if failure == nil {
		return "development workspace API request failed"
	}
	return fmt.Sprintf(
		"development workspace API request failed with %s (HTTP %d)",
		failure.Code,
		failure.StatusCode,
	)
}

func (failure *APIError) Is(target error) bool {
	return failure != nil && failure.dispatched &&
		target == localgateway.ErrRequestMayHaveBeenSent
}

// RequestMayHaveBeenSent reports whether a mutating request reached the HTTP
// transport or produced a response. Callers must reconcile before retrying.
func RequestMayHaveBeenSent(err error) bool {
	return localgateway.RequestMayHaveBeenSent(err)
}

func (client *Client) Capabilities(ctx context.Context) (Capabilities, error) {
	var result Capabilities
	err := client.do(
		ctx,
		http.MethodGet,
		prworkspace.RuntimeRoutePrefix+"/capabilities",
		nil,
		http.StatusOK,
		&result,
	)
	return result, err
}

func (client *Client) ListRepositories(
	ctx context.Context,
) ([]prworkspace.ConfiguredRepository, error) {
	var result struct {
		Repositories []prworkspace.ConfiguredRepository `json:"repositories"`
	}
	err := client.do(
		ctx,
		http.MethodGet,
		prworkspace.RuntimeRoutePrefix+"/repositories",
		nil,
		http.StatusOK,
		&result,
	)
	return result.Repositories, err
}

func (client *Client) ResolveRepository(
	ctx context.Context,
	repositoryURL string,
) (prworkspace.ConfiguredRepository, error) {
	if !validClientBoundedText(repositoryURL, clientMaxRepositoryBytes, false) {
		return prworkspace.ConfiguredRepository{}, ErrInvalidRequest
	}
	body := struct {
		RepositoryURL string `json:"repository_url"`
	}{RepositoryURL: repositoryURL}
	var result prworkspace.ConfiguredRepository
	err := client.do(
		ctx,
		http.MethodPost,
		prworkspace.RuntimeRoutePrefix+"/repositories/resolve",
		body,
		http.StatusOK,
		&result,
	)
	return result, err
}

func (client *Client) Create(
	ctx context.Context,
	request CreateRequest,
) (prworkspace.Aggregate, error) {
	if !validClientRequestID(request.RequestID) ||
		!validClientBoundedText(request.RepositoryIdentity, clientMaxRepositoryBytes, false) ||
		!validClientTask(request.Content) {
		return prworkspace.Aggregate{}, ErrInvalidRequest
	}
	body := struct {
		Intent prworkspace.DevelopmentIntent `json:"intent"`
		Source struct {
			Kind               prworkspace.SourceKind `json:"kind"`
			RepositoryIdentity string                 `json:"repository_identity"`
			Content            string                 `json:"content"`
		} `json:"source"`
		RequestID string `json:"request_id"`
	}{
		Intent:    prworkspace.IntentImplementFeature,
		RequestID: request.RequestID,
	}
	body.Source.Kind = prworkspace.SourceBrief
	body.Source.RepositoryIdentity = request.RepositoryIdentity
	body.Source.Content = request.Content

	var result prworkspace.Aggregate
	err := client.do(
		ctx,
		http.MethodPost,
		prworkspace.RuntimeRoutePrefix,
		body,
		http.StatusCreated,
		&result,
	)
	return result, err
}

func (client *Client) Get(
	ctx context.Context,
	workspaceID string,
) (prworkspace.Aggregate, error) {
	if !validClientOpaqueID(workspaceID, "devw_") {
		return prworkspace.Aggregate{}, ErrInvalidRequest
	}
	var result prworkspace.Aggregate
	err := client.do(
		ctx,
		http.MethodGet,
		prworkspace.RuntimeRoutePrefix+"/"+workspaceID,
		nil,
		http.StatusOK,
		&result,
	)
	return result, err
}

func (client *Client) ConfirmCharter(
	ctx context.Context,
	request ConfirmCharterRequest,
) (prworkspace.Aggregate, error) {
	if !validClientMutation(
		request.WorkspaceID,
		request.ExpectedVersion,
		request.RequestID,
	) || request.ExpectedCharterRevision <= 0 {
		return prworkspace.Aggregate{}, ErrInvalidRequest
	}
	body := struct {
		ExpectedVersion         int64  `json:"expected_version"`
		RequestID               string `json:"request_id"`
		ExpectedCharterRevision int64  `json:"expected_charter_revision"`
	}{
		ExpectedVersion:         request.ExpectedVersion,
		RequestID:               request.RequestID,
		ExpectedCharterRevision: request.ExpectedCharterRevision,
	}
	var result prworkspace.Aggregate
	err := client.do(
		ctx,
		http.MethodPost,
		prworkspace.RuntimeRoutePrefix+"/"+request.WorkspaceID+"/charter/confirm",
		body,
		http.StatusOK,
		&result,
	)
	return result, err
}

func (client *Client) RespondGate(
	ctx context.Context,
	request RespondGateRequest,
) (prworkspace.Aggregate, error) {
	if !validClientMutation(
		request.WorkspaceID,
		request.ExpectedVersion,
		request.RequestID,
	) || !validClientOpaqueID(request.GateID, "pgr_") || request.FieldValues == nil {
		return prworkspace.Aggregate{}, ErrInvalidRequest
	}
	body := struct {
		ExpectedVersion int64          `json:"expected_version"`
		RequestID       string         `json:"request_id"`
		FieldValues     map[string]any `json:"field-values"`
	}{
		ExpectedVersion: request.ExpectedVersion,
		RequestID:       request.RequestID,
		FieldValues:     request.FieldValues,
	}
	var result prworkspace.Aggregate
	err := client.do(
		ctx,
		http.MethodPost,
		prworkspace.RuntimeRoutePrefix+"/"+request.WorkspaceID+"/gates/"+
			request.GateID+"/respond",
		body,
		http.StatusOK,
		&result,
	)
	return result, err
}

func (client *Client) ReconcilePublication(
	ctx context.Context,
	request ReconcilePublicationRequest,
) (prworkspace.Aggregate, error) {
	if !validClientMutation(
		request.WorkspaceID,
		request.ExpectedVersion,
		request.RequestID,
	) || !validClientOpaqueID(request.PublicationID, "ppb_") ||
		!validClientBoundedText(request.ExpectedHeadRevision, clientMaxRevisionBytes, false) {
		return prworkspace.Aggregate{}, ErrInvalidRequest
	}
	body := struct {
		ExpectedVersion      int64  `json:"expected_version"`
		RequestID            string `json:"request_id"`
		ExpectedHeadRevision string `json:"expected_head_revision"`
	}{
		ExpectedVersion:      request.ExpectedVersion,
		RequestID:            request.RequestID,
		ExpectedHeadRevision: request.ExpectedHeadRevision,
	}
	var result prworkspace.Aggregate
	err := client.do(
		ctx,
		http.MethodPost,
		prworkspace.RuntimeRoutePrefix+"/"+request.WorkspaceID+"/publications/"+
			request.PublicationID+"/reconcile",
		body,
		http.StatusOK,
		&result,
	)
	return result, err
}

func (client *Client) do(
	ctx context.Context,
	method string,
	path string,
	body any,
	expectedStatus int,
	target any,
) error {
	if client == nil || client.transport == nil {
		return ErrClientUnavailable
	}
	var rawBody []byte
	if body != nil {
		var err error
		rawBody, err = json.Marshal(body)
		if err != nil || len(rawBody) > clientMaxRequestBodyBytes {
			return ErrInvalidRequest
		}
	}
	response, transportErr := client.transport.DoJSON(ctx, localgateway.Request{
		Method: method,
		Path:   path,
		Body:   rawBody,
	})
	dispatched := method == http.MethodPost && response.StatusCode != 0
	// Status is authoritative even if response validation also failed. This
	// preserves version-conflict/current reconciliation on mutating calls.
	if response.StatusCode != 0 && response.StatusCode != expectedStatus {
		return decodeAPIError(response.StatusCode, response.Body, dispatched)
	}
	if transportErr != nil {
		return transportErr
	}
	if response.StatusCode != expectedStatus || target == nil ||
		len(response.Body) == 0 || len(response.Body) > clientMaxResponseBodyBytes {
		return markDispatched(ErrInvalidResponse, dispatched)
	}
	if err := json.Unmarshal(response.Body, target); err != nil {
		return markDispatched(ErrInvalidResponse, dispatched)
	}
	return nil
}

type clientAPIErrorEnvelope struct {
	Code    string                 `json:"code"`
	Current *prworkspace.Aggregate `json:"current"`
}

func decodeAPIError(status int, raw []byte, dispatched bool) error {
	failure := &APIError{
		StatusCode: status,
		Code:       "invalid_response",
		dispatched: dispatched,
	}
	if len(raw) == 0 || len(raw) > clientMaxResponseBodyBytes {
		return failure
	}
	var envelope clientAPIErrorEnvelope
	if err := json.Unmarshal(raw, &envelope); err != nil || !validClientAPIErrorCode(envelope.Code) {
		return failure
	}
	failure.Code = envelope.Code
	failure.Current = envelope.Current
	return failure
}

type clientDispatchedError struct {
	err error
}

func (failure *clientDispatchedError) Error() string {
	return failure.err.Error()
}

func (failure *clientDispatchedError) Unwrap() error {
	return failure.err
}

func (*clientDispatchedError) Is(target error) bool {
	return target == localgateway.ErrRequestMayHaveBeenSent
}

func markDispatched(err error, dispatched bool) error {
	if err == nil || !dispatched || localgateway.RequestMayHaveBeenSent(err) {
		return err
	}
	return &clientDispatchedError{err: err}
}

func validClientMutation(workspaceID string, version int64, requestID string) bool {
	return validClientOpaqueID(workspaceID, "devw_") && version > 0 &&
		validClientRequestID(requestID)
}

func validClientOpaqueID(value string, prefix string) bool {
	if !strings.HasPrefix(value, prefix) || len(value) != len(prefix)+32 {
		return false
	}
	suffix := strings.TrimPrefix(value, prefix)
	if suffix != strings.ToLower(suffix) {
		return false
	}
	_, err := hex.DecodeString(suffix)
	return err == nil
}

func validClientRequestID(value string) bool {
	if len(value) < 16 || len(value) > 128 || value != strings.TrimSpace(value) {
		return false
	}
	for _, character := range value {
		if character >= 'a' && character <= 'z' || character >= 'A' && character <= 'Z' ||
			character >= '0' && character <= '9' || strings.ContainsRune("._:-", character) {
			continue
		}
		return false
	}
	return true
}

func validClientTask(value string) bool {
	return len(value) <= clientMaxTaskBytes && utf8.ValidString(value) && value != "" &&
		value == strings.TrimSpace(value)
}

func validClientBoundedText(value string, maximum int, allowEmpty bool) bool {
	if len(value) > maximum || !utf8.ValidString(value) || value != strings.TrimSpace(value) {
		return false
	}
	if !allowEmpty && value == "" {
		return false
	}
	for _, character := range value {
		if character == 0 || character == '\u007f' || character >= '\u0080' && character <= '\u009f' {
			return false
		}
	}
	return true
}

func validClientAPIErrorCode(value string) bool {
	if value == "" || len(value) > clientMaxAPIErrorCodeBytes {
		return false
	}
	for _, character := range value {
		if character >= 'a' && character <= 'z' || character >= '0' && character <= '9' ||
			character == '_' {
			continue
		}
		return false
	}
	return true
}
