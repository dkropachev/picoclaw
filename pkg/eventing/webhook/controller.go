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
	// ErrActivationStaged reports a lifecycle mutation attempted while an
	// exclusively owned, non-accepting activation reservation is outstanding.
	ErrActivationStaged = errors.New("webhook admission activation is staged")
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
	staged   *stagedGenerationState
}

// Generation is an opaque identity returned by Activate. It fences delayed
// cleanup so an old generation can never deactivate its replacement.
type Generation struct {
	controller *Controller
	state      *generationState
}

// StagedGeneration exclusively owns a validated, non-accepting backend
// reservation. Once Stage succeeds, gateway lifecycle serialization makes
// Commit an operationally infallible publication step. Abort is idempotent and
// cannot affect another reservation.
type StagedGeneration struct {
	controller *Controller
	state      *stagedGenerationState
	once       sync.Once
}

type stagedGenerationState struct {
	generation *generationState
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

// Stage validates and reserves a backend without making it reachable to HTTP
// requests. A nil backend reserves a disabled commit. The previous generation
// must have drained completely first.
func (c *Controller) Stage(backend *Backend) (*StagedGeneration, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.active != nil {
		return nil, ErrActiveGeneration
	}
	if c.retiring != nil {
		return nil, ErrGenerationDraining
	}
	if c.staged != nil {
		return nil, ErrActivationStaged
	}
	var generation *generationState
	if backend != nil {
		generation = &generationState{
			backend: backend,
			drained: make(chan struct{}),
		}
	}
	state := &stagedGenerationState{generation: generation}
	c.staged = state
	return &StagedGeneration{controller: c, state: state}, nil
}

// Generation returns the opaque identity that Commit will publish. A staged
// disabled configuration has the zero generation.
func (staged *StagedGeneration) Generation() Generation {
	if staged == nil ||
		staged.controller == nil ||
		staged.state == nil ||
		staged.state.generation == nil {
		return Generation{}
	}
	return Generation{
		controller: staged.controller,
		state:      staged.state.generation,
	}
}

// Commit makes the reserved backend accepting. It has no operational failure
// after a successful Stage while gateway lifecycle mutations remain serialized.
func (staged *StagedGeneration) Commit() {
	if staged == nil {
		return
	}
	staged.once.Do(func() {
		staged.controller.commitStaged(staged.state)
	})
}

// Abort abandons this reservation without publishing its backend.
func (staged *StagedGeneration) Abort() {
	if staged == nil {
		return
	}
	staged.once.Do(func() {
		staged.controller.abortStaged(staged.state)
	})
}

func (c *Controller) commitStaged(state *stagedGenerationState) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.staged != state {
		return
	}
	c.staged = nil
	if state.generation != nil {
		state.generation.accepting = true
		c.active = state.generation
	}
}

func (c *Controller) abortStaged(state *stagedGenerationState) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.staged == state {
		c.staged = nil
	}
}

// Activate is the single-controller convenience form of Stage followed by
// Commit.
func (c *Controller) Activate(backend *Backend) (Generation, error) {
	if backend == nil {
		return Generation{}, errors.New("webhook admission backend is required")
	}
	staged, err := c.Stage(backend)
	if err != nil {
		return Generation{}, err
	}
	generation := staged.Generation()
	staged.Commit()
	return generation, nil
}

// Deactivate atomically rejects new requests for generation and waits for all
// requests already admitted to leave the insert boundary. A repeated or
// delayed call for a drained generation is safe and cannot affect a newer one.
func (c *Controller) Deactivate(ctx context.Context, generation Generation) error {
	if generation != (Generation{}) &&
		(generation.controller != c || generation.state == nil) {
		return ErrGenerationNotOwned
	}
	if ctx == nil {
		ctx = context.Background()
	}

	c.mu.Lock()
	if c.staged != nil {
		c.mu.Unlock()
		return ErrActivationStaged
	}
	if generation == (Generation{}) {
		c.mu.Unlock()
		return nil
	}
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
