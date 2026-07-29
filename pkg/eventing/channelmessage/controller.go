package channelmessage

import (
	"context"
	"errors"
	"sync"

	"github.com/sipeed/picoclaw/pkg/bus"
)

var (
	// ErrActiveGeneration reports activation while another backend is live.
	ErrActiveGeneration = errors.New("channel event admission generation is already active")
	// ErrGenerationDraining reports activation or disabled commit before the
	// previous backend's admitted inserts have drained.
	ErrGenerationDraining = errors.New("channel event admission generation is still draining")
	// ErrGenerationNotOwned reports a generation passed to another controller.
	ErrGenerationNotOwned = errors.New("channel event admission generation is not owned by this controller")
	// ErrPreparedConnectorsMismatch prevents activation of a backend whose
	// connector identities were not fenced by the current preparation.
	ErrPreparedConnectorsMismatch = errors.New("channel event admission backend does not match prepared connectors")
	// ErrControllerClosed reports a configured message released by controller
	// shutdown before it could be admitted.
	ErrControllerClosed = errors.New("channel event admission controller is closed")
	// ErrActivationStaged reports a lifecycle mutation attempted while an
	// exclusively owned, non-accepting activation reservation is outstanding.
	ErrActivationStaged = errors.New("channel event admission activation is staged")
)

// Controller is a stable bus admission hook whose immutable backend can be
// prepared, activated, and drained across gateway configuration generations.
type Controller struct {
	mu sync.Mutex

	active   *generationState
	retiring *generationState
	staged   *stagedGenerationState
	pending  map[string]struct{}
	changed  chan struct{}

	preparedConnectors map[string]struct{}
	hasPreparation     bool
	closed             bool
	closedConnectors   map[string]struct{}
}

// Generation is an opaque activation identity. It fences delayed cleanup so a
// stale generation cannot stop or otherwise affect its replacement.
type Generation struct {
	controller *Controller
	state      *generationState
}

// StagedGeneration exclusively owns a validated, non-accepting backend (or
// disabled) reservation. Once Stage succeeds, serialized gateway lifecycle
// makes Commit an operationally infallible publication step. Abort is
// idempotent and cannot affect another reservation.
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

// NewController returns an inactive channel admission controller.
func NewController() *Controller {
	return &Controller{
		pending: make(map[string]struct{}),
		changed: make(chan struct{}),
	}
}

// Prepare publishes the connector identities of a candidate configuration.
// Candidate-only messages wait until Activate or CommitDisabled resolves the
// reload; connectors still owned by the active generation continue normally.
func (controller *Controller) Prepare(connectors []string) error {
	pending, err := connectorSet(connectors)
	if err != nil {
		return err
	}

	controller.mu.Lock()
	defer controller.mu.Unlock()
	if controller.closed {
		return ErrControllerClosed
	}
	if controller.retiring != nil {
		return ErrGenerationDraining
	}
	if controller.staged != nil {
		return ErrActivationStaged
	}
	controller.pending = pending
	controller.preparedConnectors = cloneConnectorSet(pending)
	controller.hasPreparation = true
	controller.signalLocked()
	return nil
}

// CancelPreparation abandons a candidate connector fence without changing the
// active generation. Candidate-only waiters wake and re-evaluate as ordinary
// channel traffic, while connectors owned by the active backend continue to use
// that backend. A retiring generation cannot be canceled because its pending
// fence protects a store that is still draining.
func (controller *Controller) CancelPreparation() error {
	controller.mu.Lock()
	defer controller.mu.Unlock()
	if controller.closed {
		return ErrControllerClosed
	}
	if controller.retiring != nil {
		return ErrGenerationDraining
	}
	if controller.staged != nil {
		return ErrActivationStaged
	}
	controller.pending = make(map[string]struct{})
	controller.preparedConnectors = nil
	controller.hasPreparation = false
	controller.signalLocked()
	return nil
}

// Stage validates and reserves a prepared backend without making it reachable
// to channel publishers. A nil backend reserves a disabled commit.
func (controller *Controller) Stage(
	backend *Backend,
) (*StagedGeneration, error) {
	controller.mu.Lock()
	defer controller.mu.Unlock()
	if controller.closed {
		return nil, ErrControllerClosed
	}
	if controller.active != nil {
		return nil, ErrActiveGeneration
	}
	if controller.retiring != nil {
		return nil, ErrGenerationDraining
	}
	if controller.staged != nil {
		return nil, ErrActivationStaged
	}

	var generation *generationState
	if backend != nil {
		backendConnectors, err := connectorSet(backend.ConnectorNames())
		if err != nil {
			return nil, err
		}
		if !controller.hasPreparation ||
			!sameConnectorSet(controller.preparedConnectors, backendConnectors) {
			return nil, ErrPreparedConnectorsMismatch
		}
		generation = &generationState{
			backend: backend,
			drained: make(chan struct{}),
		}
	}

	state := &stagedGenerationState{generation: generation}
	controller.staged = state
	return &StagedGeneration{
		controller: controller,
		state:      state,
	}, nil
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

// Commit makes the reserved backend accepting, or atomically opens a disabled
// configuration's pending connector fence. It has no operational failure after
// a successful Stage while gateway lifecycle mutations remain serialized.
func (staged *StagedGeneration) Commit() {
	if staged == nil {
		return
	}
	staged.once.Do(func() {
		staged.controller.commitStaged(staged.state)
	})
}

// Abort abandons this reservation while retaining its prepared connector fence.
func (staged *StagedGeneration) Abort() {
	if staged == nil {
		return
	}
	staged.once.Do(func() {
		staged.controller.abortStaged(staged.state)
	})
}

func (controller *Controller) commitStaged(state *stagedGenerationState) {
	controller.mu.Lock()
	defer controller.mu.Unlock()
	if controller.staged != state {
		return
	}
	controller.staged = nil
	if state.generation != nil {
		state.generation.accepting = true
		controller.active = state.generation
	}
	controller.pending = make(map[string]struct{})
	controller.preparedConnectors = nil
	controller.hasPreparation = false
	controller.signalLocked()
}

func (controller *Controller) abortStaged(state *stagedGenerationState) {
	controller.mu.Lock()
	defer controller.mu.Unlock()
	if controller.staged == state {
		controller.staged = nil
	}
}

// Activate is the single-controller convenience form of Stage followed by
// Commit.
func (controller *Controller) Activate(backend *Backend) (Generation, error) {
	if backend == nil {
		return Generation{}, errors.New("channel event admission backend is required")
	}
	staged, err := controller.Stage(backend)
	if err != nil {
		return Generation{}, err
	}
	generation := staged.Generation()
	staged.Commit()
	return generation, nil
}

// CommitDisabled resolves a prepared configuration without an admission
// backend. Waiting messages wake and pass through the ordinary channel path.
func (controller *Controller) CommitDisabled() error {
	staged, err := controller.Stage(nil)
	if err != nil {
		return err
	}
	staged.Commit()
	return nil
}

// Deactivate atomically stops generation, blocks messages for both the old and
// next configured connector sets, and waits for already-admitted inserts to
// finish. Repeated cleanup of a drained generation is harmless.
func (controller *Controller) Deactivate(
	ctx context.Context,
	generation Generation,
	nextConnectors []string,
) error {
	if generation != (Generation{}) &&
		(generation.controller != controller || generation.state == nil) {
		return ErrGenerationNotOwned
	}
	if ctx == nil {
		ctx = context.Background()
	}

	controller.mu.Lock()
	if controller.staged != nil {
		controller.mu.Unlock()
		return ErrActivationStaged
	}
	if generation == (Generation{}) {
		controller.mu.Unlock()
		return nil
	}
	switch {
	case controller.active == generation.state:
		next, err := connectorSet(nextConnectors)
		if err != nil {
			controller.mu.Unlock()
			return err
		}
		if controller.hasPreparation &&
			!sameConnectorSet(controller.preparedConnectors, next) {
			controller.mu.Unlock()
			return ErrPreparedConnectorsMismatch
		}
		controller.preparedConnectors = cloneConnectorSet(next)
		controller.hasPreparation = true
		for _, connector := range generation.state.backend.ConnectorNames() {
			next[connector] = struct{}{}
		}
		controller.active = nil
		generation.state.accepting = false
		controller.retiring = generation.state
		controller.pending = next
		controller.signalLocked()
		controller.finishDrainLocked(generation.state)
	case controller.retiring == generation.state:
		// Another caller already established the pending connector fence.
	case generation.state.done:
		// Delayed cleanup for an old generation cannot affect its replacement.
	default:
		controller.mu.Unlock()
		return ErrGenerationNotOwned
	}
	drained := generation.state.drained
	controller.mu.Unlock()

	select {
	case <-drained:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// IsActive reports whether generation is the currently published backend.
func (controller *Controller) IsActive(generation Generation) bool {
	controller.mu.Lock()
	defer controller.mu.Unlock()
	return generation.controller == controller &&
		generation.state != nil &&
		controller.active == generation.state &&
		generation.state.accepting
}

// AdmitInbound implements bus.InboundAdmission. During a reload, only messages
// belonging to pending configured connectors wait; internal and unrelated
// messages always pass immediately.
func (controller *Controller) AdmitInbound(
	ctx context.Context,
	message bus.InboundMessage,
) (bool, error) {
	if !message.ChannelOrigin {
		return true, nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	connector := messageConnector(message)

	for {
		controller.mu.Lock()
		if controller.active != nil &&
			controller.active.accepting &&
			controller.active.backend.HasConnector(connector) {
			generation := controller.active
			generation.inflight++
			controller.mu.Unlock()

			forward, err := generation.backend.AdmitInbound(ctx, message)
			controller.release(generation)
			return forward, err
		}
		if controller.closed {
			_, configured := controller.closedConnectors[connector]
			controller.mu.Unlock()
			if configured {
				return false, ErrControllerClosed
			}
			return true, nil
		}
		if _, wait := controller.pending[connector]; !wait {
			controller.mu.Unlock()
			return true, nil
		}
		changed := controller.changed
		controller.mu.Unlock()

		select {
		case <-changed:
		case <-ctx.Done():
			return false, ctx.Err()
		}
	}
}

// Close permanently stops admission, wakes pending messages with
// ErrControllerClosed, and waits for inserts already admitted by the active
// generation. Unrelated and internal messages remain pass-through.
func (controller *Controller) Close(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}

	controller.mu.Lock()
	if controller.staged != nil {
		controller.mu.Unlock()
		return ErrActivationStaged
	}
	if !controller.closed {
		controller.closed = true
		controller.closedConnectors = make(map[string]struct{}, len(controller.pending))
		for connector := range controller.pending {
			controller.closedConnectors[connector] = struct{}{}
		}
		if controller.active != nil {
			for _, connector := range controller.active.backend.ConnectorNames() {
				controller.closedConnectors[connector] = struct{}{}
			}
			controller.active.accepting = false
			controller.retiring = controller.active
			controller.active = nil
		}
		if controller.retiring != nil {
			for _, connector := range controller.retiring.backend.ConnectorNames() {
				controller.closedConnectors[connector] = struct{}{}
			}
			controller.finishDrainLocked(controller.retiring)
		}
		controller.pending = make(map[string]struct{})
		controller.signalLocked()
	}
	retiring := controller.retiring
	controller.mu.Unlock()

	if retiring == nil {
		return nil
	}
	select {
	case <-retiring.drained:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (controller *Controller) release(generation *generationState) {
	controller.mu.Lock()
	defer controller.mu.Unlock()
	generation.inflight--
	controller.finishDrainLocked(generation)
}

func (controller *Controller) finishDrainLocked(generation *generationState) {
	if generation.accepting || generation.inflight != 0 || generation.done {
		return
	}
	generation.done = true
	close(generation.drained)
	if controller.retiring == generation {
		controller.retiring = nil
	}
}

func (controller *Controller) signalLocked() {
	close(controller.changed)
	controller.changed = make(chan struct{})
}

func connectorSet(connectors []string) (map[string]struct{}, error) {
	set := make(map[string]struct{}, len(connectors))
	for _, connector := range connectors {
		if !validConnector(connector) {
			return nil, errors.New("channel event admission connector is invalid")
		}
		set[connector] = struct{}{}
	}
	return set, nil
}

func cloneConnectorSet(source map[string]struct{}) map[string]struct{} {
	cloned := make(map[string]struct{}, len(source))
	for connector := range source {
		cloned[connector] = struct{}{}
	}
	return cloned
}

func sameConnectorSet(left, right map[string]struct{}) bool {
	if len(left) != len(right) {
		return false
	}
	for connector := range left {
		if _, exists := right[connector]; !exists {
			return false
		}
	}
	return true
}

var _ bus.InboundAdmission = (*Controller)(nil)
