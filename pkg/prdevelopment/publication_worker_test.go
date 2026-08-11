package prdevelopment

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/sipeed/picoclaw/pkg/eventing"
)

type publicationWorkerContextKey struct{}

type publicationWorkerQueueRecorder struct {
	calls  int
	ctx    context.Context
	input  eventing.PRDevelopmentPublicationClaimRequest
	claims []eventing.PRDevelopmentPublication
	err    error
}

func (queue *publicationWorkerQueueRecorder) ClaimPRDevelopmentPublications(
	ctx context.Context,
	input eventing.PRDevelopmentPublicationClaimRequest,
) ([]eventing.PRDevelopmentPublication, error) {
	queue.calls++
	queue.ctx = ctx
	queue.input = input
	return queue.claims, queue.err
}

func newPublicationWorkerTestDispatcher(
	t *testing.T,
	recorder *publicationDispatchRecorder,
) *PublicationDispatcher {
	t.Helper()
	dispatcher, err := NewPublicationDispatcher(
		publicationDispatcherTestConfig(recorder),
	)
	if err != nil {
		t.Fatalf("NewPublicationDispatcher() error = %v", err)
	}
	return dispatcher
}

func TestPublicationWorkerClaimsOneAndDispatchesExactClaim(t *testing.T) {
	t.Parallel()

	for _, origin := range []eventing.PRDevelopmentPublicationStatus{
		eventing.PRDevelopmentPublicationPending,
		eventing.PRDevelopmentPublicationGateWaiting,
		eventing.PRDevelopmentPublicationPushReady,
	} {
		t.Run(string(origin), func(t *testing.T) {
			t.Parallel()
			claim := publicationDispatcherTestClaim(origin)
			queue := &publicationWorkerQueueRecorder{
				claims: []eventing.PRDevelopmentPublication{claim},
			}
			wantErr := errors.New("phase handler error")
			dispatchRecorder := &publicationDispatchRecorder{
				errors: map[eventing.PRDevelopmentPublicationStatus]error{
					origin: wantErr,
				},
			}
			worker, err := NewPublicationWorker(PublicationWorkerConfig{
				Queue:      queue,
				Dispatcher: newPublicationWorkerTestDispatcher(t, dispatchRecorder),
			})
			if err != nil {
				t.Fatalf("NewPublicationWorker() error = %v", err)
			}

			ctx := context.WithValue(t.Context(), publicationWorkerContextKey{}, origin)
			handled, processErr := worker.ProcessOne(ctx)
			if !handled {
				t.Fatal("ProcessOne() handled = false, want true")
			}
			if processErr != wantErr {
				t.Fatalf("ProcessOne() error = %v, want exact %v", processErr, wantErr)
			}
			if queue.calls != 1 {
				t.Fatalf("queue calls = %d, want 1", queue.calls)
			}
			wantInput := eventing.PRDevelopmentPublicationClaimRequest{
				WorkerLabel: defaultPublicationWorkerLabel,
				Limit:       1,
				Lease:       defaultPublicationWorkerLease,
			}
			if queue.input != wantInput {
				t.Fatalf("claim input = %#v, want %#v", queue.input, wantInput)
			}
			if queue.ctx != ctx || dispatchRecorder.ctx != ctx {
				t.Fatal("queue and handler did not receive the exact process context")
			}
			if !reflect.DeepEqual(dispatchRecorder.claim, claim) {
				t.Fatal("dispatcher did not receive the exact claimed publication")
			}
			if !reflect.DeepEqual(
				dispatchRecorder.calls,
				[]eventing.PRDevelopmentPublicationStatus{origin},
			) {
				t.Fatalf("handler calls = %v, want only %v", dispatchRecorder.calls, origin)
			}
		})
	}
}

func TestPublicationWorkerNoWorkDoesNotDispatch(t *testing.T) {
	t.Parallel()

	queue := &publicationWorkerQueueRecorder{}
	dispatchRecorder := &publicationDispatchRecorder{}
	worker, err := NewPublicationWorker(PublicationWorkerConfig{
		Queue:      queue,
		Dispatcher: newPublicationWorkerTestDispatcher(t, dispatchRecorder),
	})
	if err != nil {
		t.Fatalf("NewPublicationWorker() error = %v", err)
	}
	handled, err := worker.ProcessOne(t.Context())
	if err != nil || handled {
		t.Fatalf("ProcessOne() = (%v, %v), want (false, nil)", handled, err)
	}
	if queue.calls != 1 || len(dispatchRecorder.calls) != 0 {
		t.Fatalf(
			"queue calls = %d, handler calls = %v; want 1 and none",
			queue.calls,
			dispatchRecorder.calls,
		)
	}
}

func TestPublicationWorkerReturnsClaimErrorUnchanged(t *testing.T) {
	t.Parallel()

	wantErr := errors.New("claim failed")
	queue := &publicationWorkerQueueRecorder{
		claims: []eventing.PRDevelopmentPublication{
			publicationDispatcherTestClaim(eventing.PRDevelopmentPublicationPending),
		},
		err: wantErr,
	}
	dispatchRecorder := &publicationDispatchRecorder{}
	worker, err := NewPublicationWorker(PublicationWorkerConfig{
		Queue:      queue,
		Dispatcher: newPublicationWorkerTestDispatcher(t, dispatchRecorder),
	})
	if err != nil {
		t.Fatalf("NewPublicationWorker() error = %v", err)
	}
	handled, processErr := worker.ProcessOne(t.Context())
	if handled || processErr != wantErr {
		t.Fatalf("ProcessOne() = (%v, %v), want (false, exact claim error)", handled, processErr)
	}
	if len(dispatchRecorder.calls) != 0 {
		t.Fatalf("handler calls = %v, want none", dispatchRecorder.calls)
	}
}

func TestPublicationWorkerFailsClosedOnClaimOverproduction(t *testing.T) {
	t.Parallel()

	claim := publicationDispatcherTestClaim(eventing.PRDevelopmentPublicationPending)
	second := claim
	second.ID = "pdpub_20202020202020202020202020202020"
	queue := &publicationWorkerQueueRecorder{
		claims: []eventing.PRDevelopmentPublication{claim, second},
	}
	dispatchRecorder := &publicationDispatchRecorder{}
	worker, err := NewPublicationWorker(PublicationWorkerConfig{
		Queue:      queue,
		Dispatcher: newPublicationWorkerTestDispatcher(t, dispatchRecorder),
	})
	if err != nil {
		t.Fatalf("NewPublicationWorker() error = %v", err)
	}
	handled, processErr := worker.ProcessOne(t.Context())
	if !handled || processErr == nil {
		t.Fatalf("ProcessOne() = (%v, %v), want (true, error)", handled, processErr)
	}
	if len(dispatchRecorder.calls) != 0 {
		t.Fatalf("handler calls = %v, want none", dispatchRecorder.calls)
	}
}

func TestNewPublicationWorkerRequiresQueueAndCompleteDispatcher(t *testing.T) {
	t.Parallel()

	var typedNilQueue *publicationWorkerQueueRecorder
	tests := []struct {
		name   string
		config func(t *testing.T) PublicationWorkerConfig
	}{
		{
			name: "nil queue",
			config: func(t *testing.T) PublicationWorkerConfig {
				return PublicationWorkerConfig{
					Dispatcher: newPublicationWorkerTestDispatcher(t, &publicationDispatchRecorder{}),
				}
			},
		},
		{
			name: "typed nil queue",
			config: func(t *testing.T) PublicationWorkerConfig {
				return PublicationWorkerConfig{
					Queue:      typedNilQueue,
					Dispatcher: newPublicationWorkerTestDispatcher(t, &publicationDispatchRecorder{}),
				}
			},
		},
		{
			name: "nil dispatcher",
			config: func(*testing.T) PublicationWorkerConfig {
				return PublicationWorkerConfig{Queue: &publicationWorkerQueueRecorder{}}
			},
		},
		{
			name: "missing pending handler",
			config: func(t *testing.T) PublicationWorkerConfig {
				dispatcher := newPublicationWorkerTestDispatcher(t, &publicationDispatchRecorder{})
				dispatcher.pending = nil
				return PublicationWorkerConfig{
					Queue: &publicationWorkerQueueRecorder{}, Dispatcher: dispatcher,
				}
			},
		},
		{
			name: "missing gate waiting handler",
			config: func(t *testing.T) PublicationWorkerConfig {
				dispatcher := newPublicationWorkerTestDispatcher(t, &publicationDispatchRecorder{})
				dispatcher.gateWaiting = nil
				return PublicationWorkerConfig{
					Queue: &publicationWorkerQueueRecorder{}, Dispatcher: dispatcher,
				}
			},
		},
		{
			name: "missing push ready handler",
			config: func(t *testing.T) PublicationWorkerConfig {
				dispatcher := newPublicationWorkerTestDispatcher(t, &publicationDispatchRecorder{})
				dispatcher.pushReady = nil
				return PublicationWorkerConfig{
					Queue: &publicationWorkerQueueRecorder{}, Dispatcher: dispatcher,
				}
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			worker, err := NewPublicationWorker(test.config(t))
			if worker != nil || !errors.Is(err, ErrUnavailable) {
				t.Fatalf(
					"NewPublicationWorker() = (%v, %v), want (nil, ErrUnavailable)",
					worker,
					err,
				)
			}
		})
	}
}

func TestPublicationWorkerRejectsNonExactLabelAndShortLease(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name   string
		label  string
		lease  time.Duration
		wantIs error
	}{
		{name: "padded label", label: " publication-worker", wantIs: nil},
		{
			name: "short lease", label: "publication-worker",
			lease:  minimumPublicationGateClaimLease - time.Nanosecond,
			wantIs: ErrInvalidRequest,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			worker, err := NewPublicationWorker(PublicationWorkerConfig{
				Queue:         &publicationWorkerQueueRecorder{},
				Dispatcher:    newPublicationWorkerTestDispatcher(t, &publicationDispatchRecorder{}),
				WorkerLabel:   test.label,
				LeaseDuration: test.lease,
			})
			if worker != nil || err == nil {
				t.Fatalf("NewPublicationWorker() = (%v, %v), want (nil, error)", worker, err)
			}
			if test.wantIs != nil && !errors.Is(err, test.wantIs) {
				t.Fatalf("NewPublicationWorker() error = %v, want %v", err, test.wantIs)
			}
		})
	}
}

func TestPublicationWorkerHandoffLeaseMustPrecedeEveryConcreteHandler(t *testing.T) {
	t.Parallel()

	minimum := minimumPublicationGateClaimLease
	tests := []struct {
		name                      string
		handoff                   time.Duration
		pending, waiting, pushing time.Duration
		wantOK                    bool
	}{
		{
			name:    "minimum handoff below every handler",
			handoff: minimum,
			pending: minimum + time.Nanosecond, waiting: minimum + time.Nanosecond,
			pushing: minimum + time.Nanosecond,
			wantOK:  true,
		},
		{
			name:    "custom handoff below every handler",
			handoff: 7 * time.Minute,
			pending: 8 * time.Minute, waiting: 9 * time.Minute, pushing: 10 * time.Minute,
			wantOK: true,
		},
		{
			name:    "equal pending lease",
			handoff: minimum, pending: minimum,
			waiting: minimum + time.Nanosecond, pushing: minimum + time.Nanosecond,
		},
		{
			name:    "equal waiting lease",
			handoff: minimum, waiting: minimum,
			pending: minimum + time.Nanosecond, pushing: minimum + time.Nanosecond,
		},
		{
			name:    "equal push lease",
			handoff: minimum, pushing: minimum,
			pending: minimum + time.Nanosecond, waiting: minimum + time.Nanosecond,
		},
		{
			name:    "handoff longer than handlers",
			handoff: minimum + time.Nanosecond,
			pending: minimum, waiting: minimum, pushing: minimum,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			dispatcher := &PublicationDispatcher{
				pending:     &PublicationPendingGateHandler{leaseDuration: test.pending},
				gateWaiting: &PublicationGateWaitingHandler{leaseDuration: test.waiting},
				pushReady:   &PublicationPushReadyHandler{leaseDuration: test.pushing},
			}
			worker, err := NewPublicationWorker(PublicationWorkerConfig{
				Queue:         &publicationWorkerQueueRecorder{},
				Dispatcher:    dispatcher,
				LeaseDuration: test.handoff,
			})
			if test.wantOK {
				if err != nil || worker == nil {
					t.Fatalf("NewPublicationWorker() = (%#v, %v), want worker", worker, err)
				}
				return
			}
			if worker != nil || !errors.Is(err, ErrInvalidRequest) {
				t.Fatalf(
					"NewPublicationWorker() = (%#v, %v), want nil/ErrInvalidRequest",
					worker,
					err,
				)
			}
		})
	}
}

func TestPublicationWorkerUsesCustomClaimIdentityAndNormalizesNilContext(t *testing.T) {
	t.Parallel()

	claim := publicationDispatcherTestClaim(eventing.PRDevelopmentPublicationPending)
	queue := &publicationWorkerQueueRecorder{
		claims: []eventing.PRDevelopmentPublication{claim},
	}
	worker, err := NewPublicationWorker(PublicationWorkerConfig{
		Queue:         queue,
		Dispatcher:    newPublicationWorkerTestDispatcher(t, &publicationDispatchRecorder{}),
		WorkerLabel:   "publication-worker-custom",
		LeaseDuration: 7 * time.Minute,
	})
	if err != nil {
		t.Fatalf("NewPublicationWorker() error = %v", err)
	}
	handled, err := worker.ProcessOne(nil)
	if err != nil || !handled {
		t.Fatalf("ProcessOne() = (%v, %v), want (true, nil)", handled, err)
	}
	if queue.ctx == nil {
		t.Fatal("queue received a nil context")
	}
	wantInput := eventing.PRDevelopmentPublicationClaimRequest{
		WorkerLabel: "publication-worker-custom", Limit: 1, Lease: 7 * time.Minute,
	}
	if queue.input != wantInput {
		t.Fatalf("claim input = %#v, want %#v", queue.input, wantInput)
	}
}

func TestPublicationWorkerDoesNotClaimAfterDispatcherBecomesIncomplete(t *testing.T) {
	t.Parallel()

	queue := &publicationWorkerQueueRecorder{}
	dispatcher := newPublicationWorkerTestDispatcher(t, &publicationDispatchRecorder{})
	worker, err := NewPublicationWorker(PublicationWorkerConfig{
		Queue: queue, Dispatcher: dispatcher,
	})
	if err != nil {
		t.Fatalf("NewPublicationWorker() error = %v", err)
	}
	dispatcher.pushReady = nil
	handled, processErr := worker.ProcessOne(t.Context())
	if handled || !errors.Is(processErr, ErrUnavailable) {
		t.Fatalf("ProcessOne() = (%v, %v), want (false, ErrUnavailable)", handled, processErr)
	}
	if queue.calls != 0 {
		t.Fatalf("queue calls = %d, want 0", queue.calls)
	}
}

func TestPublicationWorkerConfigurationIsJSONPrivate(t *testing.T) {
	t.Parallel()

	queue := &publicationWorkerQueueRecorder{
		err: errors.New("private queue diagnostic"),
	}
	dispatcher := newPublicationWorkerTestDispatcher(t, &publicationDispatchRecorder{})
	config := PublicationWorkerConfig{
		Queue: queue, Dispatcher: dispatcher,
		WorkerLabel: "private-publication-worker", LeaseDuration: 9 * time.Minute,
	}
	encoded, err := json.Marshal(config)
	if err != nil {
		t.Fatalf("json.Marshal(config) error = %v", err)
	}
	if string(encoded) != `{}` {
		t.Fatalf("json.Marshal(config) = %s, want {}", encoded)
	}
	worker, err := NewPublicationWorker(config)
	if err != nil {
		t.Fatalf("NewPublicationWorker() error = %v", err)
	}
	encoded, err = json.Marshal(worker)
	if err != nil {
		t.Fatalf("json.Marshal(worker) error = %v", err)
	}
	if string(encoded) != `{}` {
		t.Fatalf("json.Marshal(worker) = %s, want {}", encoded)
	}
}

func TestPublicationWorkerUnavailableDoesNotClaim(t *testing.T) {
	t.Parallel()

	var worker *PublicationWorker
	handled, err := worker.ProcessOne(t.Context())
	if handled || !errors.Is(err, ErrUnavailable) {
		t.Fatalf("nil ProcessOne() = (%v, %v), want (false, ErrUnavailable)", handled, err)
	}
}
