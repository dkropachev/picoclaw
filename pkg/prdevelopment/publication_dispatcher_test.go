package prdevelopment

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/sipeed/picoclaw/pkg/eventing"
)

type publicationDispatchContextKey struct{}

type publicationDispatchRecorder struct {
	calls  []eventing.PRDevelopmentPublicationStatus
	ctx    context.Context
	claim  eventing.PRDevelopmentPublication
	errors map[eventing.PRDevelopmentPublicationStatus]error
}

func (recorder *publicationDispatchRecorder) handle(
	phase eventing.PRDevelopmentPublicationStatus,
) func(context.Context, eventing.PRDevelopmentPublication) error {
	return func(ctx context.Context, claim eventing.PRDevelopmentPublication) error {
		recorder.calls = append(recorder.calls, phase)
		recorder.ctx = ctx
		recorder.claim = claim
		return recorder.errors[phase]
	}
}

func publicationDispatcherTestConfig(
	recorder *publicationDispatchRecorder,
) PublicationDispatcherConfig {
	return PublicationDispatcherConfig{
		Pending: PublicationPendingClaimHandlerFunc(
			recorder.handle(eventing.PRDevelopmentPublicationPending),
		),
		GateWaiting: PublicationGateWaitingClaimHandlerFunc(
			recorder.handle(eventing.PRDevelopmentPublicationGateWaiting),
		),
		PushReady: PublicationPushReadyClaimHandlerFunc(
			recorder.handle(eventing.PRDevelopmentPublicationPushReady),
		),
	}
}

func publicationDispatcherTestClaim(
	origin eventing.PRDevelopmentPublicationStatus,
) eventing.PRDevelopmentPublication {
	claimedAt := time.Date(2026, 8, 11, 12, 0, 0, 0, time.UTC)
	claimUntil := claimedAt.Add(5 * time.Minute)
	return eventing.PRDevelopmentPublication{
		ID:         "pdpub_10101010101010101010101010101010",
		Status:     eventing.PRDevelopmentPublicationClaimed,
		ClaimFrom:  origin,
		ClaimOwner: "publication-dispatcher-test",
		ClaimToken: "claim-token",
		ClaimUntil: &claimUntil,
		ClaimEpoch: 3,
		Claims:     3,
		ClaimedAt:  &claimedAt,
	}
}

func TestPublicationDispatcherRoutesOnlyClaimOrigin(t *testing.T) {
	t.Parallel()

	for _, origin := range []eventing.PRDevelopmentPublicationStatus{
		eventing.PRDevelopmentPublicationPending,
		eventing.PRDevelopmentPublicationGateWaiting,
		eventing.PRDevelopmentPublicationPushReady,
	} {
		t.Run(string(origin), func(t *testing.T) {
			t.Parallel()
			wantErr := errors.New("phase handler failed")
			recorder := &publicationDispatchRecorder{
				errors: map[eventing.PRDevelopmentPublicationStatus]error{origin: wantErr},
			}
			dispatcher, err := NewPublicationDispatcher(
				publicationDispatcherTestConfig(recorder),
			)
			if err != nil {
				t.Fatalf("NewPublicationDispatcher() error = %v", err)
			}
			claim := publicationDispatcherTestClaim(origin)
			ctx := context.WithValue(t.Context(), publicationDispatchContextKey{}, origin)
			if err = dispatcher.DispatchClaim(ctx, claim); !errors.Is(err, wantErr) {
				t.Fatalf("DispatchClaim() error = %v, want %v", err, wantErr)
			}
			if !reflect.DeepEqual(recorder.calls, []eventing.PRDevelopmentPublicationStatus{origin}) {
				t.Fatalf("handler calls = %v, want only %v", recorder.calls, origin)
			}
			if recorder.ctx.Value(publicationDispatchContextKey{}) != origin {
				t.Fatal("handler did not receive the dispatch context")
			}
			if !reflect.DeepEqual(recorder.claim, claim) {
				t.Fatal("handler did not receive the exact claimed publication")
			}
		})
	}
}

func TestPublicationDispatcherRejectsInvalidClaimsWithoutRouting(t *testing.T) {
	t.Parallel()

	started := time.Date(2026, 8, 11, 12, 1, 0, 0, time.UTC)
	tests := []struct {
		name   string
		mutate func(*eventing.PRDevelopmentPublication)
	}{
		{name: "invalid id", mutate: func(claim *eventing.PRDevelopmentPublication) {
			claim.ID = "pdpub_invalid"
		}},
		{name: "unclaimed status", mutate: func(claim *eventing.PRDevelopmentPublication) {
			claim.Status = eventing.PRDevelopmentPublicationPending
		}},
		{name: "empty origin", mutate: func(claim *eventing.PRDevelopmentPublication) {
			claim.ClaimFrom = ""
		}},
		{name: "unknown origin", mutate: func(claim *eventing.PRDevelopmentPublication) {
			claim.ClaimFrom = "unknown"
		}},
		{name: "post effect origin", mutate: func(claim *eventing.PRDevelopmentPublication) {
			claim.ClaimFrom = eventing.PRDevelopmentPublicationPushStarted
		}},
		{name: "empty owner", mutate: func(claim *eventing.PRDevelopmentPublication) {
			claim.ClaimOwner = ""
		}},
		{name: "padded owner", mutate: func(claim *eventing.PRDevelopmentPublication) {
			claim.ClaimOwner = " owner"
		}},
		{name: "empty token", mutate: func(claim *eventing.PRDevelopmentPublication) {
			claim.ClaimToken = ""
		}},
		{name: "padded token", mutate: func(claim *eventing.PRDevelopmentPublication) {
			claim.ClaimToken = "token "
		}},
		{name: "zero epoch", mutate: func(claim *eventing.PRDevelopmentPublication) {
			claim.ClaimEpoch = 0
		}},
		{name: "zero claims", mutate: func(claim *eventing.PRDevelopmentPublication) {
			claim.Claims = 0
		}},
		{name: "claim count mismatch", mutate: func(claim *eventing.PRDevelopmentPublication) {
			claim.Claims++
		}},
		{name: "missing claimed at", mutate: func(claim *eventing.PRDevelopmentPublication) {
			claim.ClaimedAt = nil
		}},
		{name: "missing claim deadline", mutate: func(claim *eventing.PRDevelopmentPublication) {
			claim.ClaimUntil = nil
		}},
		{name: "nonpositive claim interval", mutate: func(claim *eventing.PRDevelopmentPublication) {
			claim.ClaimUntil = claim.ClaimedAt
		}},
		{name: "effect already started", mutate: func(claim *eventing.PRDevelopmentPublication) {
			claim.EffectStartedAt = &started
		}},
		{name: "already completed", mutate: func(claim *eventing.PRDevelopmentPublication) {
			claim.CompletedAt = &started
		}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			recorder := &publicationDispatchRecorder{}
			dispatcher, err := NewPublicationDispatcher(
				publicationDispatcherTestConfig(recorder),
			)
			if err != nil {
				t.Fatalf("NewPublicationDispatcher() error = %v", err)
			}
			claim := publicationDispatcherTestClaim(
				eventing.PRDevelopmentPublicationPending,
			)
			test.mutate(&claim)
			if err = dispatcher.DispatchClaim(t.Context(), claim); !errors.Is(err, ErrInvalidRequest) {
				t.Fatalf("DispatchClaim() error = %v, want ErrInvalidRequest", err)
			}
			if len(recorder.calls) != 0 {
				t.Fatalf("handler calls = %v, want none", recorder.calls)
			}
		})
	}
}

func TestNewPublicationDispatcherRequiresEveryHandler(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		mutate func(*PublicationDispatcherConfig)
	}{
		{name: "nil pending", mutate: func(config *PublicationDispatcherConfig) {
			config.Pending = nil
		}},
		{name: "typed nil pending", mutate: func(config *PublicationDispatcherConfig) {
			config.Pending = PublicationPendingClaimHandlerFunc(nil)
		}},
		{name: "nil gate waiting", mutate: func(config *PublicationDispatcherConfig) {
			config.GateWaiting = nil
		}},
		{name: "typed nil gate waiting", mutate: func(config *PublicationDispatcherConfig) {
			config.GateWaiting = PublicationGateWaitingClaimHandlerFunc(nil)
		}},
		{name: "nil push ready", mutate: func(config *PublicationDispatcherConfig) {
			config.PushReady = nil
		}},
		{name: "typed nil push ready", mutate: func(config *PublicationDispatcherConfig) {
			config.PushReady = PublicationPushReadyClaimHandlerFunc(nil)
		}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			config := publicationDispatcherTestConfig(&publicationDispatchRecorder{})
			test.mutate(&config)
			dispatcher, err := NewPublicationDispatcher(config)
			if dispatcher != nil || !errors.Is(err, ErrUnavailable) {
				t.Fatalf(
					"NewPublicationDispatcher() = (%v, %v), want (nil, ErrUnavailable)",
					dispatcher,
					err,
				)
			}
		})
	}
}

func TestPublicationDispatcherUnavailableNeverRoutes(t *testing.T) {
	t.Parallel()

	recorder := &publicationDispatchRecorder{}
	dispatcher, err := NewPublicationDispatcher(publicationDispatcherTestConfig(recorder))
	if err != nil {
		t.Fatalf("NewPublicationDispatcher() error = %v", err)
	}
	dispatcher.pushReady = nil
	claim := publicationDispatcherTestClaim(eventing.PRDevelopmentPublicationPending)
	if err = dispatcher.DispatchClaim(t.Context(), claim); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("DispatchClaim() error = %v, want ErrUnavailable", err)
	}
	if len(recorder.calls) != 0 {
		t.Fatalf("handler calls = %v, want none", recorder.calls)
	}

	var nilDispatcher *PublicationDispatcher
	if err = nilDispatcher.DispatchClaim(t.Context(), claim); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("nil DispatchClaim() error = %v, want ErrUnavailable", err)
	}
}

func TestPublicationDispatcherNormalizesNilContext(t *testing.T) {
	t.Parallel()

	called := false
	dispatcher, err := NewPublicationDispatcher(PublicationDispatcherConfig{
		Pending: PublicationPendingClaimHandlerFunc(func(
			ctx context.Context,
			_ eventing.PRDevelopmentPublication,
		) error {
			called = true
			if ctx == nil {
				t.Fatal("handler received a nil context")
			}
			return nil
		}),
		GateWaiting: PublicationGateWaitingClaimHandlerFunc(func(
			context.Context,
			eventing.PRDevelopmentPublication,
		) error {
			return nil
		}),
		PushReady: PublicationPushReadyClaimHandlerFunc(func(
			context.Context,
			eventing.PRDevelopmentPublication,
		) error {
			return nil
		}),
	})
	if err != nil {
		t.Fatalf("NewPublicationDispatcher() error = %v", err)
	}
	if err = dispatcher.DispatchClaim(
		nil,
		publicationDispatcherTestClaim(eventing.PRDevelopmentPublicationPending),
	); err != nil {
		t.Fatalf("DispatchClaim() error = %v", err)
	}
	if !called {
		t.Fatal("pending handler was not called")
	}
}
