package database

import (
	"context"
	"errors"
	"os"
	"sync"
	"time"
)

const defaultRequestTimeout = 10 * time.Second

// CallOptions describe mutation ambiguity and optional stable idempotency.
// The client never retries mutations implicitly.
type CallOptions struct {
	Mutation       bool
	IdempotencyKey string
}

// Client is an authenticated local broker client bound to one manifest epoch.
type Client struct {
	home       string
	rediscover bool

	mu       sync.RWMutex
	manifest Manifest
}

// Connect discovers the broker from the owner-only manifest under home.
func Connect(home string) (*Client, error) {
	canonical, err := CanonicalHome(home)
	if err != nil {
		return nil, safeDiscoveryError(err)
	}
	manifest, err := ReadManifest(canonical)
	if err != nil {
		return nil, safeDiscoveryError(err)
	}
	return &Client{home: canonical, rediscover: true, manifest: manifest}, nil
}

// ConnectWithManifest constructs a client from authenticated inherited
// authority, while still validating that its endpoint belongs to home. This is
// intended for the private runtime child; ordinary clients should use Connect.
func ConnectWithManifest(home string, manifest Manifest) (*Client, error) {
	canonical, err := CanonicalHome(home)
	if err != nil {
		return nil, safeDiscoveryError(err)
	}
	stateDir, err := StateDirectory(canonical)
	if err != nil {
		return nil, safeDiscoveryError(err)
	}
	if err := validateManifest(manifest, stateDir); err != nil {
		return nil, err
	}
	return &Client{home: canonical, manifest: manifest}, nil
}

// Refresh atomically reloads discovery authority. Callers may use it once after
// an Unavailable or stale-epoch result; it does not replay any operation.
func (client *Client) Refresh() error {
	if client == nil || client.home == "" {
		return NewError(CodeUnavailable, "database broker client is unavailable")
	}
	manifest, err := ReadManifest(client.home)
	if err != nil {
		return safeDiscoveryError(err)
	}
	client.mu.Lock()
	client.manifest = manifest
	client.mu.Unlock()
	return nil
}

// Epoch returns the immutable epoch currently bound to client calls.
func (client *Client) Epoch() string {
	if client == nil {
		return ""
	}
	client.mu.RLock()
	defer client.mu.RUnlock()
	return client.manifest.Epoch
}

// Call invokes one typed read/nonmutating domain operation.
func (client *Client) Call(
	ctx context.Context,
	domain string,
	version int,
	operation string,
	input any,
	output any,
) error {
	if client == nil {
		return NewError(CodeUnavailable, "database broker client is unavailable")
	}
	err := client.CallWithOptions(ctx, domain, version, operation, input, output, CallOptions{})
	if (CodeOf(err) != CodeUnavailable && CodeOf(err) != CodeConflict) || !client.rediscover {
		return err
	}
	// Reads may rediscover once after broker loss or a stale epoch. Mutations
	// never enter this path because they use CallWithOptions directly.
	if refreshErr := client.Refresh(); refreshErr != nil {
		return err
	}
	return client.CallWithOptions(ctx, domain, version, operation, input, output, CallOptions{})
}

// CallWithOptions invokes one typed domain operation. A transport failure after
// a mutation could have followed a commit and is therefore OutcomeUnknown.
func (client *Client) CallWithOptions(
	ctx context.Context,
	domain string,
	version int,
	operation string,
	input any,
	output any,
	options CallOptions,
) error {
	if client == nil {
		return NewError(CodeUnavailable, "database broker client is unavailable")
	}
	if client.rediscover {
		manifest, discoveryErr := ReadManifest(client.home)
		if discoveryErr != nil {
			return safeDiscoveryError(discoveryErr)
		}
		client.mu.Lock()
		client.manifest = manifest
		client.mu.Unlock()
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if _, hasDeadline := ctx.Deadline(); !hasDeadline {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, defaultRequestTimeout)
		defer cancel()
	}
	deadline, ok := ctx.Deadline()
	if !ok || !deadline.After(time.Now()) {
		return NewError(CodeDeadline, "database request deadline was exceeded")
	}
	payload, err := marshalPayload(input)
	if err != nil {
		return NewError(CodeInvalid, "database request payload is invalid")
	}
	requestID, err := randomHex(16)
	if err != nil {
		return NewError(CodeInternal, "database request ID generation failed")
	}
	client.mu.RLock()
	manifest := client.manifest
	client.mu.RUnlock()
	envelope := RequestEnvelope{
		Protocol: ProtocolVersion, RequestID: requestID, Token: manifest.Token,
		BrokerEpoch: manifest.Epoch, Domain: domain, DomainVersion: version,
		Operation: operation, DeadlineUnixNs: deadline.UnixNano(),
		IdempotencyKey: options.IdempotencyKey, Payload: payload,
	}
	if validationErr := validRequestEnvelope(envelope); validationErr != nil {
		return validationErr
	}
	rawRequest, err := MarshalCanonical(envelope)
	if err != nil || len(rawRequest) == 0 || uint64(len(rawRequest)) > uint64(MaxFrameSize) {
		return NewError(CodeInvalid, "database request exceeds the protocol limit")
	}

	attempt := func() error {
		connection, dialErr := dialLocal(ctx, manifest.Endpoint)
		if dialErr != nil {
			if errors.Is(ctx.Err(), context.DeadlineExceeded) || errors.Is(ctx.Err(), context.Canceled) {
				return NewError(CodeDeadline, "database request deadline was exceeded")
			}
			return NewError(CodeUnavailable, "database broker is unavailable")
		}
		defer connection.Close()
		_ = connection.SetDeadline(deadline)
		stopCancellation := context.AfterFunc(ctx, func() {
			_ = connection.SetDeadline(time.Now())
		})
		defer stopCancellation()

		if writeErr := writeFrameBytes(connection, rawRequest); writeErr != nil {
			return dispatchedCallError(options.Mutation, ctx.Err())
		}
		var response ResponseEnvelope
		if readErr := readFrameStrict(connection, &response); readErr != nil {
			return dispatchedCallError(options.Mutation, ctx.Err())
		}
		if responseErr := validResponseEnvelope(response, requestID, manifest.Epoch); responseErr != nil {
			if CodeOf(responseErr) == CodeConflict {
				return responseErr
			}
			return dispatchedCallError(options.Mutation, nil)
		}
		if response.Error != nil {
			return NewError(response.Error.Code, response.Error.Message)
		}
		if output == nil {
			return nil
		}
		if decodeErr := unmarshalCanonicalStrict(response.Payload, output); decodeErr != nil {
			return dispatchedCallError(options.Mutation, nil)
		}
		return nil
	}
	callErr := attempt()
	if CodeOf(callErr) != CodeOutcomeUnknown || options.IdempotencyKey == "" {
		return callErr
	}
	// The retry uses the exact same epoch, request ID, key, and canonical
	// payload. The broker's epoch-lifetime replay registry therefore returns the
	// original outcome without dispatching the domain mutation twice.
	retryErr := attempt()
	if retryErr == nil {
		return nil
	}
	switch CodeOf(retryErr) {
	case CodeUnavailable, CodeDeadline, CodeOutcomeUnknown:
		return callErr
	default:
		return retryErr
	}
}

func safeDiscoveryError(err error) error {
	if err == nil {
		return nil
	}
	if code := CodeOf(err); code != CodeInternal {
		return NewError(code, "database broker discovery failed")
	}
	if errors.Is(err, os.ErrPermission) {
		return NewError(CodeUnauthorized, "database broker discovery is not owner-accessible")
	}
	return NewError(CodeUnavailable, "database broker discovery is unavailable")
}

// Ping verifies protocol, authentication, PID, and epoch liveness.
func (client *Client) Ping(ctx context.Context) (PingResponse, error) {
	var response PingResponse
	err := client.Call(ctx, ControlDomain, ControlVersion, ControlOperationPing, EmptyPayload{}, &response)
	if err != nil {
		return PingResponse{}, err
	}
	client.mu.RLock()
	manifest := client.manifest
	client.mu.RUnlock()
	if response.Protocol != ProtocolVersion || response.PID != manifest.PID || response.Epoch != manifest.Epoch {
		return PingResponse{}, NewError(CodeIntegrity, "database broker ping identity is invalid")
	}
	return response, nil
}

// Status returns the broker's deterministic, provider-neutral store readiness.
func (client *Client) Status(ctx context.Context) (BrokerStatus, error) {
	var response BrokerStatus
	err := client.Call(ctx, ControlDomain, ControlVersion, ControlOperationStatus, EmptyPayload{}, &response)
	if err != nil {
		return BrokerStatus{}, err
	}
	validated, err := ValidateStoreStatuses(response.Stores)
	if err != nil {
		return BrokerStatus{}, NewError(CodeIntegrity, "database broker status is invalid")
	}
	requiredStores, err := validateRequiredStores(response.RequiredStores)
	if err != nil {
		return BrokerStatus{}, NewError(CodeIntegrity, "database broker status is invalid")
	}
	if err := requiredStoresHaveStatuses(requiredStores, validated); err != nil {
		return BrokerStatus{}, NewError(CodeIntegrity, "database broker status is invalid")
	}
	if response.CatalogFingerprint != "" && !validCatalogFingerprint(response.CatalogFingerprint) {
		return BrokerStatus{}, NewError(CodeIntegrity, "database broker status is invalid")
	}
	client.mu.RLock()
	manifest := client.manifest
	client.mu.RUnlock()
	if response.Protocol != ProtocolVersion || response.PID != manifest.PID || response.Epoch != manifest.Epoch ||
		response.StartedAt.IsZero() {
		return BrokerStatus{}, NewError(CodeIntegrity, "database broker status identity is invalid")
	}
	response.RequiredStores = requiredStores
	response.Stores = validated
	return response, nil
}

// Shutdown requests controlled broker shutdown. A lost response returns
// OutcomeUnknown because the request may already have been accepted.
func (client *Client) Shutdown(ctx context.Context) error {
	var response ShutdownResponse
	err := client.CallWithOptions(
		ctx, ControlDomain, ControlVersion, ControlOperationShutdown,
		EmptyPayload{}, &response, CallOptions{Mutation: true},
	)
	if err != nil {
		return err
	}
	if !response.Accepted {
		return NewError(CodeIntegrity, "database broker rejected shutdown without an error")
	}
	return nil
}

func dispatchedCallError(mutation bool, contextErr error) error {
	if mutation {
		return NewError(CodeOutcomeUnknown, "database mutation outcome is unknown")
	}
	if errors.Is(contextErr, context.DeadlineExceeded) || errors.Is(contextErr, context.Canceled) {
		return NewError(CodeDeadline, "database request deadline was exceeded")
	}
	return NewError(CodeUnavailable, "database broker is unavailable")
}
