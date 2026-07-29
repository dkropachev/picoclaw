// Package webhook admits authenticated, connector-neutral HTTP events into the
// durable event inbox.
package webhook

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"

	standardwebhooks "github.com/standard-webhooks/standard-webhooks/libraries/go"

	"github.com/sipeed/picoclaw/pkg/eventing"
)

const (
	// RoutePrefix is the subtree registered on the gateway's shared HTTP
	// listener. A connector name is the single path segment after this prefix.
	RoutePrefix = "/webhooks/events/"

	// RequestMetadataAllowanceBytes bounds non-payload envelope metadata while
	// leaving MaxPayloadBytes as the authoritative payload limit.
	RequestMetadataAllowanceBytes = 256 << 10

	minimumSecretBytes                     = 32
	connectorIdentitySecretConflictMessage = "webhook connector identity conflicts with a signing secret"
)

var (
	// ErrActiveGeneration reports an attempt to activate a controller that
	// already has a live backend.
	ErrActiveGeneration = errors.New("webhook admission generation is already active")
	// ErrGenerationDraining reports an activation attempted before the previous
	// generation's admitted requests have left the durable-insert boundary.
	ErrGenerationDraining = errors.New("webhook admission generation is still draining")
	// ErrGenerationNotOwned reports a generation passed to another controller.
	ErrGenerationNotOwned = errors.New("webhook admission generation is not owned by this controller")
)

// Inserter is the durable write boundary required by generic webhook ingress.
type Inserter interface {
	Insert(
		ctx context.Context,
		event eventing.Envelope,
	) (eventing.InsertResult, error)
}

// BackendConfig contains the generation-specific dependencies prepared before
// a gateway reload commits. ConnectorSecrets must contain only enabled
// connectors.
type BackendConfig struct {
	Store            Inserter
	ConnectorSecrets map[string]string
	MaxPayloadBytes  int
}

// Backend is an immutable, prevalidated admission backend. Preparing a backend
// does not make it externally reachable.
type Backend struct {
	store               Inserter
	connectors          map[string]*standardwebhooks.Webhook
	secretValues        []string
	maxRequestBodyBytes int64
}

// ConnectorCount reports the number of enabled connectors in this backend.
func (backend *Backend) ConnectorCount() int {
	if backend == nil {
		return 0
	}
	return len(backend.connectors)
}

// NewBackend validates and precompiles a candidate admission backend.
func NewBackend(config BackendConfig) (*Backend, error) {
	if config.Store == nil {
		return nil, errors.New("webhook admission store is required")
	}
	if config.MaxPayloadBytes <= 0 ||
		config.MaxPayloadBytes > int(^uint(0)>>1)-RequestMetadataAllowanceBytes {
		return nil, errors.New("webhook admission maximum payload bytes is invalid")
	}
	if len(config.ConnectorSecrets) == 0 {
		return nil, errors.New("webhook admission requires at least one enabled connector")
	}

	seenSecrets := make(map[string]struct{}, len(config.ConnectorSecrets))
	secretValues := make([]string, 0, len(config.ConnectorSecrets))
	for _, secret := range config.ConnectorSecrets {
		if secret == "" {
			continue
		}
		if _, exists := seenSecrets[secret]; exists {
			continue
		}
		seenSecrets[secret] = struct{}{}
		secretValues = append(secretValues, secret)
	}
	for name := range config.ConnectorSecrets {
		for _, secret := range secretValues {
			if strings.Contains(name, secret) {
				return nil, errors.New(connectorIdentitySecretConflictMessage)
			}
		}
	}

	connectors := make(map[string]*standardwebhooks.Webhook, len(config.ConnectorSecrets))
	caseFoldedNames := make(map[string]string, len(config.ConnectorSecrets))
	for name, secret := range config.ConnectorSecrets {
		if !validConnectorName(name) {
			return nil, fmt.Errorf("webhook connector name %q is invalid", name)
		}
		folded := strings.ToLower(name)
		if previous, exists := caseFoldedNames[folded]; exists {
			return nil, fmt.Errorf(
				"webhook connector names %q and %q differ only by case",
				previous,
				name,
			)
		}
		caseFoldedNames[folded] = name

		verifier, err := verifierForSecret(secret)
		if err != nil {
			return nil, fmt.Errorf("webhook connector %q has an invalid signing secret", name)
		}
		connectors[name] = verifier
	}

	return &Backend{
		store:               config.Store,
		connectors:          connectors,
		secretValues:        secretValues,
		maxRequestBodyBytes: int64(config.MaxPayloadBytes + RequestMetadataAllowanceBytes),
	}, nil
}

func verifierForSecret(secret string) (*standardwebhooks.Webhook, error) {
	const prefix = "whsec_"
	if !strings.HasPrefix(secret, prefix) {
		return nil, errors.New("missing Standard Webhooks secret prefix")
	}
	raw, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(secret, prefix))
	if err != nil ||
		len(raw) < minimumSecretBytes ||
		base64.StdEncoding.EncodeToString(raw) != strings.TrimPrefix(secret, prefix) {
		return nil, errors.New("invalid Standard Webhooks secret")
	}
	return standardwebhooks.NewWebhook(secret)
}

func validConnectorName(name string) bool {
	if len(name) == 0 || len(name) > 64 {
		return false
	}
	for index, char := range []byte(name) {
		letter := char >= 'a' && char <= 'z' || char >= 'A' && char <= 'Z'
		if letter || index > 0 && char >= '0' && char <= '9' {
			continue
		}
		if index > 0 && (char == '_' || char == '-') {
			continue
		}
		return false
	}
	return true
}

// Controller is a stable HTTP handler whose immutable backend can be
// transactionally activated and drained across gateway reload generations.
type Controller struct {
	mu       sync.Mutex
	active   *generationState
	retiring *generationState
}

// Generation is an opaque identity returned by Activate. It fences delayed
// cleanup so an old generation can never deactivate its replacement.
type Generation struct {
	controller *Controller
	state      *generationState
}

type generationState struct {
	backend   *Backend
	inflight  int
	accepting bool
	drained   chan struct{}
	done      bool
}

// NewController returns an initially inactive admission controller.
func NewController() *Controller {
	return &Controller{}
}

// Activate publishes a prepared backend. The previous generation must have
// drained completely first.
func (c *Controller) Activate(backend *Backend) (Generation, error) {
	if backend == nil {
		return Generation{}, errors.New("webhook admission backend is required")
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	if c.active != nil {
		return Generation{}, ErrActiveGeneration
	}
	if c.retiring != nil {
		return Generation{}, ErrGenerationDraining
	}
	state := &generationState{
		backend:   backend,
		accepting: true,
		drained:   make(chan struct{}),
	}
	c.active = state
	return Generation{controller: c, state: state}, nil
}

// Deactivate atomically rejects new requests for generation and waits for all
// requests already admitted to leave the insert boundary. A repeated or
// delayed call for a drained generation is safe and cannot affect a newer one.
func (c *Controller) Deactivate(ctx context.Context, generation Generation) error {
	if generation == (Generation{}) {
		return nil
	}
	if generation.controller != c || generation.state == nil {
		return ErrGenerationNotOwned
	}
	if ctx == nil {
		ctx = context.Background()
	}

	c.mu.Lock()
	switch {
	case c.active == generation.state:
		c.active = nil
		generation.state.accepting = false
		c.retiring = generation.state
		c.finishDrainLocked(generation.state)
	case c.retiring == generation.state:
		// A previous caller already stopped admission and began the drain.
	case generation.state.done:
		// A delayed cleanup call for an old generation is already satisfied.
	default:
		c.mu.Unlock()
		return ErrGenerationNotOwned
	}
	drained := generation.state.drained
	c.mu.Unlock()

	select {
	case <-drained:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// IsActive reports whether generation is the currently published backend.
func (c *Controller) IsActive(generation Generation) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return generation.controller == c &&
		generation.state != nil &&
		c.active == generation.state &&
		generation.state.accepting
}

func (c *Controller) acquire() *generationState {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.active == nil || !c.active.accepting {
		return nil
	}
	c.active.inflight++
	return c.active
}

func (c *Controller) release(generation *generationState) {
	c.mu.Lock()
	defer c.mu.Unlock()
	generation.inflight--
	c.finishDrainLocked(generation)
}

func (c *Controller) finishDrainLocked(generation *generationState) {
	if generation.accepting || generation.inflight != 0 || generation.done {
		return
	}
	generation.done = true
	close(generation.drained)
	if c.retiring == generation {
		c.retiring = nil
	}
}

// ServeHTTP admits a request addressed as POST /webhooks/events/{connector}.
func (c *Controller) ServeHTTP(w http.ResponseWriter, request *http.Request) {
	if _, ok := connectorFromRequest(request); !ok {
		writeError(w, http.StatusNotFound)
		return
	}
	if request.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		writeError(w, http.StatusMethodNotAllowed)
		return
	}

	generation := c.acquire()
	if generation == nil {
		writeRetryableError(w)
		return
	}
	defer c.release(generation)

	generation.backend.serveHTTP(w, request)
}

func connectorFromRequest(request *http.Request) (string, bool) {
	if request == nil || request.URL == nil {
		return "", false
	}
	// Connector routes contain only unreserved ASCII characters. Requiring the
	// escaped request target to equal its decoded path leaves one canonical
	// spelling for the endpoint and prevents a proxy and the origin from
	// applying path policy to different representations.
	if request.URL.EscapedPath() != request.URL.Path {
		return "", false
	}
	return connectorFromPath(request.URL.Path)
}

func connectorFromPath(path string) (string, bool) {
	if !strings.HasPrefix(path, RoutePrefix) {
		return "", false
	}
	connector := strings.TrimPrefix(path, RoutePrefix)
	if !validConnectorName(connector) {
		return "", false
	}
	return connector, true
}
