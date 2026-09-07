//nolint:govet // Server setup and dispatch use narrow validation scopes.
package database

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"sort"
	"sync"
	"time"
)

const unauthenticatedConnectionTimeout = 30 * time.Second

// ServerOptions configure one broker control server for a canonical home.
type ServerOptions struct {
	Home               string
	CatalogFingerprint string
	RequiredStores     []StoreID
	StatusProvider     StatusProvider
	Handler            Handler
	// OnShutdownRequested runs after the authenticated response is written and
	// while the online storage fence is still held. It must not call Server.Close.
	OnShutdownRequested func()
	// CloseHandler closes broker-owned domain resources on every shutdown path.
	// It runs after requests drain and before discovery or storage fences are released.
	CloseHandler func() error
	// AllowsIdempotency declares exact operations permitted to use stable
	// epoch-scoped replay keys. Undeclared keyed requests fail before dispatch.
	AllowsIdempotency func(domain string, version int, operation string) bool
	Now               func() time.Time
}

// Server owns local authenticated dispatch, discovery, the broker singleton,
// and the online shared storage fence. Domain storage remains the responsibility
// of the broker-side handler.
type Server struct {
	home               string
	stateDir           string
	listener           net.Listener
	manifest           Manifest
	startedAt          time.Time
	catalogFingerprint string
	requiredStores     []StoreID
	statusProvider     StatusProvider
	handler            Handler
	now                func() time.Time
	onShutdown         func()
	closeHandler       func() error
	idempotency        *idempotencyRegistry
	allowsIdempotency  func(string, int, string) bool

	ctx    context.Context
	cancel context.CancelFunc

	singleton   *singletonLock
	onlineFence *Fence

	workers              sync.WaitGroup
	stopOnce             sync.Once
	shutdownCallbackOnce sync.Once
	done                 chan struct{}

	closeErrMu sync.Mutex
	closeErr   error
}

// StartServer starts the owner-only local broker endpoint and publishes its
// authenticated discovery manifest. Only one server may own a canonical home.
func StartServer(parent context.Context, options ServerOptions) (*Server, error) {
	if parent == nil {
		parent = context.Background()
	}
	stateDir, err := prepareStateDirectory(options.Home)
	if err != nil {
		return nil, err
	}
	home, err := CanonicalHome(options.Home)
	if err != nil {
		return nil, err
	}
	if options.CatalogFingerprint != "" && !validCatalogFingerprint(options.CatalogFingerprint) {
		return nil, NewError(CodeInvalid, "database catalog fingerprint is invalid")
	}
	requiredStores, err := validateRequiredStores(options.RequiredStores)
	if err != nil {
		return nil, err
	}
	singleton, err := acquireBrokerSingleton(stateDir)
	if err != nil {
		return nil, err
	}
	cleanupSingleton := true
	defer func() {
		if cleanupSingleton {
			_ = singleton.close()
		}
	}()
	onlineFence, err := AcquireOnlineFence(home)
	if err != nil {
		return nil, err
	}
	cleanupFence := true
	defer func() {
		if cleanupFence {
			_ = onlineFence.Close()
		}
	}()

	endpoint := endpointForStateDirectory(stateDir)
	if err := prepareEndpoint(endpoint); err != nil {
		return nil, err
	}
	token, err := randomHex(tokenBytes)
	if err != nil {
		return nil, err
	}
	epoch, err := randomHex(epochBytes)
	if err != nil {
		return nil, err
	}
	listener, err := listenLocal(endpoint)
	if err != nil {
		return nil, err
	}
	cleanupListener := true
	defer func() {
		if cleanupListener {
			_ = listener.Close()
			_ = cleanupEndpoint(endpoint)
		}
	}()

	manifest := Manifest{
		PID: os.Getpid(), Protocol: ProtocolVersion, Token: token,
		Endpoint: endpoint, Epoch: epoch,
	}
	if err := writeManifest(stateDir, manifest); err != nil {
		return nil, err
	}
	now := options.Now
	if now == nil {
		now = time.Now
	}
	serverCtx, cancel := context.WithCancel(context.Background())
	server := &Server{
		home: home, stateDir: stateDir, listener: listener, manifest: manifest,
		startedAt: now().UTC(), catalogFingerprint: options.CatalogFingerprint,
		requiredStores: requiredStores, statusProvider: options.StatusProvider,
		handler: options.Handler, now: now, onShutdown: options.OnShutdownRequested,
		closeHandler:      options.CloseHandler,
		idempotency:       newIdempotencyRegistry(),
		allowsIdempotency: options.AllowsIdempotency,
		ctx:               serverCtx, cancel: cancel, singleton: singleton, onlineFence: onlineFence,
		done: make(chan struct{}),
	}
	cleanupSingleton = false
	cleanupFence = false
	cleanupListener = false

	server.workers.Add(1)
	go server.accept()
	go func() {
		select {
		case <-parent.Done():
			server.initiateShutdown()
		case <-server.done:
		}
	}()
	return server, nil
}

// Manifest returns a detached copy of this server's discovery authority.
func (server *Server) Manifest() Manifest {
	if server == nil {
		return Manifest{}
	}
	return server.manifest
}

// Done closes after listener, connections, discovery state, and lifetime locks
// have all been released.
func (server *Server) Done() <-chan struct{} {
	if server == nil {
		closed := make(chan struct{})
		close(closed)
		return closed
	}
	return server.done
}

// Close initiates graceful server shutdown and waits for cleanup or ctx.
func (server *Server) Close(ctx context.Context) error {
	if server == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	server.initiateShutdown()
	select {
	case <-server.done:
		server.closeErrMu.Lock()
		defer server.closeErrMu.Unlock()
		return server.closeErr
	case <-ctx.Done():
		return NewError(CodeDeadline, "database broker shutdown deadline was exceeded")
	}
}

func (server *Server) accept() {
	defer server.workers.Done()
	for {
		connection, err := server.listener.Accept()
		if err != nil {
			if server.ctx.Err() != nil || errors.Is(err, net.ErrClosed) {
				return
			}
			var netErr net.Error
			if errors.As(err, &netErr) && netErr.Temporary() {
				continue
			}
			server.appendCloseError(fmt.Errorf("accept database broker connection: %w", err))
			server.initiateShutdown()
			return
		}
		server.workers.Add(1)
		go server.serveConnection(connection)
	}
}

func (server *Server) serveConnection(connection net.Conn) {
	defer server.workers.Done()
	defer connection.Close()
	stopOnShutdown := context.AfterFunc(server.ctx, func() {
		_ = connection.SetDeadline(time.Now())
	})
	defer stopOnShutdown()
	_ = connection.SetDeadline(server.now().Add(unauthenticatedConnectionTimeout))

	var envelope RequestEnvelope
	if err := readFrameStrict(connection, &envelope); err != nil {
		return
	}
	if envelope.DeadlineUnixNs > 0 {
		deadline := time.Unix(0, envelope.DeadlineUnixNs)
		ioDeadline := deadline
		now := server.now()
		minimum := now.Add(time.Second)
		maximum := now.Add(unauthenticatedConnectionTimeout)
		if ioDeadline.Before(minimum) {
			ioDeadline = minimum
		}
		if ioDeadline.After(maximum) {
			ioDeadline = maximum
		}
		_ = connection.SetDeadline(ioDeadline)
	}
	peerContext, cancelPeer := context.WithCancel(server.ctx)
	defer cancelPeer()
	go func() {
		var extra [1]byte
		_, _ = connection.Read(extra[:])
		cancelPeer()
	}()
	response, shutdown := server.dispatchContext(peerContext, envelope)
	if err := WriteFrame(connection, response); err != nil {
		return
	}
	if shutdown {
		server.initiateShutdown()
		server.shutdownCallbackOnce.Do(func() {
			if server.onShutdown != nil {
				func() {
					defer func() {
						if recover() != nil {
							server.appendCloseError(NewError(CodeInternal, "database shutdown callback failed"))
						}
					}()
					server.onShutdown()
				}()
			}
		})
	}
}

func (server *Server) dispatch(envelope RequestEnvelope) (ResponseEnvelope, bool) {
	return server.dispatchContext(server.ctx, envelope)
}

func (server *Server) dispatchContext(
	parent context.Context,
	envelope RequestEnvelope,
) (ResponseEnvelope, bool) {
	response := ResponseEnvelope{
		Protocol: ProtocolVersion, RequestID: safeRequestID(envelope.RequestID),
		BrokerEpoch: server.manifest.Epoch,
	}
	setError := func(err error) (ResponseEnvelope, bool) {
		response.Error = protocolError(err)
		return response, false
	}
	if envelope.Protocol != ProtocolVersion {
		return setError(NewError(CodeUnsupported, "database broker protocol version is unsupported"))
	}
	if subtle.ConstantTimeCompare([]byte(envelope.Token), []byte(server.manifest.Token)) != 1 {
		return setError(NewError(CodeUnauthorized, "database broker authentication failed"))
	}
	if envelope.BrokerEpoch != server.manifest.Epoch {
		return setError(NewError(CodeConflict, "database broker epoch is stale"))
	}
	if err := validRequestEnvelope(envelope); err != nil {
		return setError(err)
	}
	if envelope.IdempotencyKey != "" && (server.allowsIdempotency == nil ||
		!server.allowsIdempotency(envelope.Domain, envelope.DomainVersion, envelope.Operation)) {
		return setError(NewError(
			CodeUnsupported,
			"database operation does not declare stable idempotency",
		))
	}
	deadline := time.Unix(0, envelope.DeadlineUnixNs)
	if !deadline.After(server.now()) {
		return setError(NewError(CodeDeadline, "database request deadline was exceeded"))
	}
	if parent == nil {
		parent = context.Background()
	}
	requestCtx, cancel := context.WithDeadline(parent, deadline)
	defer cancel()
	record, replay, replayShutdown, err := server.idempotency.begin(requestCtx, envelope)
	if err != nil {
		return setError(err)
	}
	if replay != nil {
		return *replay, replayShutdown
	}
	complete := func(response ResponseEnvelope, shutdown bool) (ResponseEnvelope, bool) {
		return server.idempotency.complete(record, response, shutdown)
	}

	request := Request{
		ID: envelope.RequestID, Domain: envelope.Domain, Version: envelope.DomainVersion,
		Operation: envelope.Operation, IdempotencyKey: envelope.IdempotencyKey,
		Payload: append(json.RawMessage(nil), envelope.Payload...),
	}
	result, shutdown, err := server.handle(requestCtx, request)
	if err != nil {
		errorResponse, _ := setError(err)
		return complete(errorResponse, false)
	}
	payload, err := marshalPayload(result)
	if err != nil {
		errorResponse, _ := setError(
			NewError(CodeInternal, "database broker returned an invalid domain result"),
		)
		return complete(errorResponse, false)
	}
	response.Payload = payload
	return complete(response, shutdown)
}

func (server *Server) handle(ctx context.Context, request Request) (result any, shutdown bool, err error) {
	defer func() {
		if recover() != nil {
			result = nil
			shutdown = false
			err = NewError(CodeInternal, "database broker domain handler failed")
		}
	}()
	if request.Domain == ControlDomain {
		if request.Version != ControlVersion {
			return nil, false, NewError(CodeUnsupported, "database control version is unsupported")
		}
		var empty EmptyPayload
		if err := request.DecodePayload(&empty); err != nil {
			return nil, false, NewError(CodeInvalid, "database control payload is invalid")
		}
		switch request.Operation {
		case ControlOperationPing:
			return PingResponse{
				Protocol: ProtocolVersion, PID: server.manifest.PID, Epoch: server.manifest.Epoch,
			}, false, nil
		case ControlOperationStatus:
			var statuses []StoreStatus
			if server.statusProvider != nil {
				provided, statusErr := server.statusProvider(ctx)
				if statusErr != nil {
					return nil, false, statusErr
				}
				statuses = provided
			}
			validated, validateErr := ValidateStoreStatuses(statuses)
			if validateErr != nil {
				return nil, false, validateErr
			}
			if validateErr = requiredStoresHaveStatuses(server.requiredStores, validated); validateErr != nil {
				return nil, false, validateErr
			}
			return BrokerStatus{
				Protocol: ProtocolVersion, PID: server.manifest.PID, Epoch: server.manifest.Epoch,
				StartedAt: server.startedAt, CatalogFingerprint: server.catalogFingerprint,
				RequiredStores: append([]StoreID(nil), server.requiredStores...), Stores: validated,
			}, false, nil
		case ControlOperationShutdown:
			return ShutdownResponse{Accepted: true}, true, nil
		default:
			return nil, false, NewError(CodeUnsupported, "database control operation is unsupported")
		}
	}
	if server.handler == nil {
		return nil, false, NewError(CodeUnsupported, "database domain is unsupported")
	}
	result, err = server.handler.Handle(ctx, request)
	return result, false, err
}

func validateRequiredStores(ids []StoreID) ([]StoreID, error) {
	validated := append([]StoreID(nil), ids...)
	seen := make(map[StoreID]struct{}, len(validated))
	for _, id := range validated {
		if !id.Valid() {
			return nil, NewError(CodeInvalid, "database required-store catalog is invalid")
		}
		if _, duplicate := seen[id]; duplicate {
			return nil, NewError(CodeIntegrity, "database required-store catalog contains a duplicate")
		}
		seen[id] = struct{}{}
	}
	sort.Slice(validated, func(i, j int) bool { return validated[i] < validated[j] })
	return validated, nil
}

func requiredStoresHaveStatuses(required []StoreID, statuses []StoreStatus) error {
	if len(required) == 0 {
		return nil
	}
	available := make(map[StoreID]struct{}, len(statuses))
	for _, status := range statuses {
		available[status.ID] = struct{}{}
	}
	for _, id := range required {
		if _, found := available[id]; !found {
			return NewError(CodeIntegrity, "required database store has no readiness status")
		}
	}
	return nil
}

func (server *Server) initiateShutdown() {
	if server == nil {
		return
	}
	server.stopOnce.Do(func() {
		server.cancel()
		if server.listener != nil {
			_ = server.listener.Close()
		}
		go server.finishShutdown()
	})
}

func (server *Server) finishShutdown() {
	server.workers.Wait()
	if server.closeHandler != nil {
		server.appendCloseError(callCloseHandler(server.closeHandler))
	}
	server.appendCloseError(removeManifestForEpoch(server.home, server.manifest.Epoch))
	server.appendCloseError(cleanupEndpoint(server.manifest.Endpoint))
	server.appendCloseError(server.onlineFence.Close())
	server.appendCloseError(server.singleton.close())
	close(server.done)
}

func callCloseHandler(closeHandler func() error) (err error) {
	defer func() {
		if recover() != nil {
			err = NewError(CodeInternal, "database broker close handler failed")
		}
	}()
	return closeHandler()
}

func (server *Server) appendCloseError(err error) {
	if err == nil {
		return
	}
	server.closeErrMu.Lock()
	server.closeErr = errors.Join(server.closeErr, err)
	server.closeErrMu.Unlock()
}

func safeRequestID(value string) string {
	if validRequestID(value) {
		return value
	}
	return "invalid"
}

func marshalPayload(value any) (json.RawMessage, error) {
	if value == nil {
		value = EmptyPayload{}
	}
	raw, err := MarshalCanonical(value)
	if err != nil {
		return nil, err
	}
	if !jsonObject(raw) {
		return nil, NewError(CodeInvalid, "database domain result must be a JSON object")
	}
	return json.RawMessage(raw), nil
}
