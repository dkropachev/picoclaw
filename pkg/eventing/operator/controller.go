package operator

import (
	"context"
	"errors"
	"net/http"
	"sync"
)

var (
	// ErrActiveGeneration reports activation while another backend is live.
	ErrActiveGeneration = errors.New("event operator generation is already active")
	// ErrGenerationDraining reports activation before admitted requests from
	// the previous generation have drained.
	ErrGenerationDraining = errors.New("event operator generation is still draining")
	// ErrGenerationNotOwned reports a generation from another controller.
	ErrGenerationNotOwned = errors.New("event operator generation is not owned by this controller")
	// ErrActivationStaged reports a lifecycle mutation while an exclusive
	// non-accepting reservation is outstanding.
	ErrActivationStaged = errors.New("event operator activation is staged")
)

// Controller is a stable HTTP handler whose immutable backend can be staged,
// published, and drained across gateway reloads.
type Controller struct {
	mu       sync.Mutex
	active   *generationState
	retiring *generationState
	staged   *stagedGenerationState
}

// Generation is an opaque activation identity.
type Generation struct {
	controller *Controller
	state      *generationState
}

// StagedGeneration exclusively owns a validated, non-accepting reservation.
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

// preparedResponse detaches a generation-backed operation from the network
// writer. Store access, response projection, and serialization complete while
// the generation is acquired; only the resulting inert bytes and headers are
// copied to the real writer after release.
type preparedResponse struct {
	header http.Header
	status int
	body   []byte
}

func newPreparedResponse() *preparedResponse {
	return &preparedResponse{header: make(http.Header)}
}

func (response *preparedResponse) Header() http.Header {
	return response.header
}

func (response *preparedResponse) WriteHeader(status int) {
	if response.status == 0 {
		response.status = status
	}
}

func (response *preparedResponse) Write(body []byte) (int, error) {
	if response.status == 0 {
		response.status = http.StatusOK
	}
	response.body = append(response.body, body...)
	return len(body), nil
}

func (response *preparedResponse) writeTo(w http.ResponseWriter) {
	for name, values := range response.header {
		w.Header()[name] = append([]string(nil), values...)
	}
	status := response.status
	if status == 0 {
		status = http.StatusOK
	}
	w.WriteHeader(status)
	_, _ = w.Write(response.body)
}

// NewController returns an initially inactive controller.
func NewController() *Controller {
	return &Controller{}
}

// Stage reserves backend without publishing it. A nil backend stages a
// disabled configuration.
func (controller *Controller) Stage(
	backend *Backend,
) (*StagedGeneration, error) {
	controller.mu.Lock()
	defer controller.mu.Unlock()
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

// Generation returns the identity Commit will publish. A staged disabled
// configuration returns the zero generation.
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

// Commit atomically publishes the reservation.
func (staged *StagedGeneration) Commit() {
	if staged == nil {
		return
	}
	staged.once.Do(func() {
		staged.controller.commitStaged(staged.state)
	})
}

// Abort abandons the reservation without publishing it.
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
}

func (controller *Controller) abortStaged(state *stagedGenerationState) {
	controller.mu.Lock()
	defer controller.mu.Unlock()
	if controller.staged == state {
		controller.staged = nil
	}
}

// Activate is the convenience form of Stage followed by Commit.
func (controller *Controller) Activate(
	backend *Backend,
) (Generation, error) {
	if backend == nil {
		return Generation{}, errors.New("event operator backend is required")
	}
	staged, err := controller.Stage(backend)
	if err != nil {
		return Generation{}, err
	}
	generation := staged.Generation()
	staged.Commit()
	return generation, nil
}

// Deactivate rejects new operations for generation and waits for admitted
// operations to leave the store boundary.
func (controller *Controller) Deactivate(
	ctx context.Context,
	generation Generation,
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
		controller.active = nil
		generation.state.accepting = false
		controller.retiring = generation.state
		controller.finishDrainLocked(generation.state)
	case controller.retiring == generation.state:
	case generation.state.done:
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

// IsActive reports whether generation is currently published.
func (controller *Controller) IsActive(generation Generation) bool {
	controller.mu.Lock()
	defer controller.mu.Unlock()
	return generation.controller == controller &&
		generation.state != nil &&
		controller.active == generation.state &&
		generation.state.accepting
}

func (controller *Controller) acquire() *generationState {
	controller.mu.Lock()
	defer controller.mu.Unlock()
	if controller.active == nil || !controller.active.accepting {
		return nil
	}
	controller.active.inflight++
	return controller.active
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

// ServeHTTP serves strict operator routes through the current generation.
func (controller *Controller) ServeHTTP(
	w http.ResponseWriter,
	request *http.Request,
) {
	route := routeFromRequest(request)
	if route.kind == routeUnknown {
		writeOperatorStatus(w, http.StatusNotFound)
		return
	}
	if route.method != "" && request.Method != route.method {
		w.Header().Set("Allow", route.method)
		writeOperatorStatus(w, http.StatusMethodNotAllowed)
		return
	}

	generation := controller.acquire()
	if generation == nil {
		writeOperatorError(w, ErrUnavailable)
		return
	}
	response := newPreparedResponse()
	func() {
		defer controller.release(generation)
		generation.backend.serveHTTP(response, request, route)
	}()
	response.writeTo(w)
}
