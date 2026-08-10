package prdevelopment

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/sipeed/picoclaw/pkg/eventing"
	"github.com/sipeed/picoclaw/pkg/workflows"
)

func TestGitHubPublicationObserverProjectsTimedProviderObservation(t *testing.T) {
	stored := validStoredGitHubCase()
	thread := validGitHubThreadIdentity()
	runner := &captureToolRunner{responses: []string{
		providerCasePullJSON(t, func(map[string]any) {}),
		providerReviewsJSON(providerReviewValue(
			"CHANGES_REQUESTED",
			stored.Feedback,
		)),
	}}
	clock := time.Date(
		2026,
		time.August,
		10,
		9,
		30,
		0,
		123456789,
		time.FixedZone("test-offset", -4*60*60),
	)
	clockCalls := 0
	observer := &githubPublicationProviderObserver{
		verifier: &GitHubVerifier{Runner: runner},
		now: func() time.Time {
			clockCalls++
			if len(runner.requests) != 2 {
				t.Fatalf("clock sampled after %d provider calls, want 2", len(runner.requests))
			}
			return clock
		},
	}

	observed, err := observer.ObservePublication(
		context.Background(),
		stored,
		thread,
	)
	if err != nil {
		t.Fatalf("ObservePublication() error = %v", err)
	}
	want := eventing.PRDevelopmentPublicationProviderObservation{
		Repository:         "ScyllaDB/PicoClaw",
		PullNumber:         42,
		HeadRepository:     "contributor/PicoClaw",
		HeadRef:            "feat/fix-race",
		HeadSHA:            testHeadSHA,
		HeadCloneURL:       "https://github.com/contributor/PicoClaw.git",
		CurrentReviewState: eventing.PRDevelopmentReviewChangesRequested,
		ReviewDigest:       "sha256:8a325e37bfcf34ac026032700822081b777635ce82e706d3efbdc047333a388d",
	}
	if observed.Observation != want {
		t.Fatalf("provider observation = %#v, want %#v", observed.Observation, want)
	}
	if observed.ObservedAt != clock.UTC() || observed.ObservedAt.Location() != time.UTC {
		t.Fatalf("observed at = %#v, want canonical UTC %#v", observed.ObservedAt, clock.UTC())
	}
	if clockCalls != 1 {
		t.Fatalf("clock calls = %d, want 1", clockCalls)
	}
	if len(runner.requests) != 2 {
		t.Fatalf("provider calls = %d, want pull and exact review", len(runner.requests))
	}
	assertReadRequest(t, runner.requests[0], "get", 0)
	assertReadRequest(t, runner.requests[1], "get_reviews", 1)
	assertPublicationObservationIsPrivate(t, observed)
}

func TestGitHubPublicationObserverDoesNotTimestampFailedProviderPreflight(t *testing.T) {
	stored := validStoredGitHubCase()
	review := providerReviewValue("CHANGES_REQUESTED", "changed feedback")
	runner := &captureToolRunner{responses: []string{
		providerCasePullJSON(t, func(map[string]any) {}),
		providerReviewsJSON(review),
	}}
	clockCalls := 0
	observer := &githubPublicationProviderObserver{
		verifier: &GitHubVerifier{Runner: runner},
		now: func() time.Time {
			clockCalls++
			return time.Now()
		},
	}

	observed, err := observer.ObservePublication(
		context.Background(),
		stored,
		validGitHubThreadIdentity(),
	)
	if !errors.Is(err, ErrGitHubCaseDrift) {
		t.Fatalf("ObservePublication() error = %v, want review drift", err)
	}
	if observed != (TimedPublicationProviderObservation{}) {
		t.Fatalf("failed provider observation = %#v, want zero", observed)
	}
	if clockCalls != 0 {
		t.Fatalf("failed provider preflight sampled clock %d times", clockCalls)
	}
}

func TestGitHubPublicationObserverRemoteHeadUsesOneReadAndIgnoresReviewLifecycle(
	t *testing.T,
) {
	stored := validStoredGitHubCase()
	remoteHead := strings.Repeat("e", 40)
	pull := providerCasePullJSON(t, func(value map[string]any) {
		value["state"] = "CLOSED"
		value["merged"] = true
		head := value["head"].(map[string]any)
		head["sha"] = remoteHead
	})
	runner := &captureToolRunner{responses: []string{pull}}
	clock := time.Date(2026, time.August, 10, 14, 0, 0, 0, time.UTC)
	observer := &githubPublicationRemoteHeadObserver{
		verifier: &GitHubVerifier{Runner: runner},
		now:      func() time.Time { return clock },
	}

	observed, err := observer.ObservePublicationRemoteHead(
		context.Background(),
		stored,
		validGitHubThreadIdentity(),
	)
	if err != nil {
		t.Fatalf("ObservePublicationRemoteHead() error = %v", err)
	}
	want := eventing.PRDevelopmentPublicationRemoteObservation{
		Repository:     "ScyllaDB/PicoClaw",
		PullNumber:     42,
		HeadRepository: "contributor/PicoClaw",
		HeadRef:        "feat/fix-race",
		HeadSHA:        remoteHead,
	}
	if observed.Observation != want || observed.ObservedAt != clock {
		t.Fatalf("remote observation = %#v, want %#v at %v", observed, want, clock)
	}
	if len(runner.requests) != 1 {
		t.Fatalf("remote-head provider calls = %d, want one get", len(runner.requests))
	}
	assertReadRequest(t, runner.requests[0], "get", 0)
	assertPublicationObservationIsPrivate(t, observed)
}

func TestGitHubPublicationObserverRemoteHeadBindsProviderIdentity(t *testing.T) {
	stored := validStoredGitHubCase()
	pull := providerCasePullJSON(t, func(value map[string]any) {
		value["user"].(map[string]any)["id"] = json.Number("9999")
	})
	runner := &captureToolRunner{responses: []string{pull}}
	clockCalls := 0
	observer := &githubPublicationRemoteHeadObserver{
		verifier: &GitHubVerifier{Runner: runner},
		now: func() time.Time {
			clockCalls++
			return time.Now()
		},
	}

	observed, err := observer.ObservePublicationRemoteHead(
		context.Background(),
		stored,
		validGitHubThreadIdentity(),
	)
	if !errors.Is(err, ErrGitHubCaseDrift) {
		t.Fatalf("ObservePublicationRemoteHead() error = %v, want identity drift", err)
	}
	if observed != (TimedPublicationRemoteObservation{}) || clockCalls != 0 {
		t.Fatalf("identity drift returned %#v and sampled clock %d times", observed, clockCalls)
	}
	if len(runner.requests) != 1 {
		t.Fatalf("identity drift provider calls = %d, want one", len(runner.requests))
	}
}

func TestGitHubPublicationObserverRemoteHeadUsesDurableEnterpriseOrigin(t *testing.T) {
	const webOrigin = "https://ghe.example.test:8443"
	stored := validStoredGitHubCase()
	stored.PullURL = webOrigin + "/ScyllaDB/PicoClaw/pull/42"
	stored.ReviewURL = stored.PullURL + "#pullrequestreview-701"
	thread := validGitHubThreadIdentity()
	thread.ProviderOrigin = webOrigin
	pull := providerCasePullJSON(t, func(value map[string]any) {
		value["html_url"] = stored.PullURL
	})
	runner := &captureToolRunner{responses: []string{pull}}
	clock := time.Date(2026, time.August, 10, 15, 0, 0, 0, time.UTC)
	observer := &githubPublicationRemoteHeadObserver{
		verifier: &GitHubVerifier{Runner: runner},
		now:      func() time.Time { return clock },
	}

	observed, err := observer.ObservePublicationRemoteHead(
		context.Background(),
		stored,
		thread,
	)
	if err != nil {
		t.Fatalf("ObservePublicationRemoteHead() error = %v", err)
	}
	if observed.Observation.HeadSHA != testHeadSHA || len(runner.requests) != 1 {
		t.Fatalf("enterprise observation = %#v, calls=%d", observed, len(runner.requests))
	}

	mismatchRunner := &captureToolRunner{responses: []string{pull}}
	observer.verifier = &GitHubVerifier{
		Runner:    mismatchRunner,
		WebOrigin: "https://other-ghe.example.test",
	}
	_, err = observer.ObservePublicationRemoteHead(
		context.Background(),
		stored,
		thread,
	)
	if !errors.Is(err, ErrGitHubCaseDrift) || len(mismatchRunner.requests) != 0 {
		t.Fatalf("origin mismatch error=%v, calls=%d, want pre-read drift", err, len(mismatchRunner.requests))
	}
}

func TestGitHubPublicationObserverRemoteHeadRejectsMalformedProviderJSONBeforeClock(
	t *testing.T,
) {
	runner := &captureToolRunner{responses: []string{
		`{"number":42,"number":42}`,
	}}
	clockCalls := 0
	observer := &githubPublicationRemoteHeadObserver{
		verifier: &GitHubVerifier{Runner: runner},
		now: func() time.Time {
			clockCalls++
			return time.Now()
		},
	}

	_, err := observer.ObservePublicationRemoteHead(
		context.Background(),
		validStoredGitHubCase(),
		validGitHubThreadIdentity(),
	)
	if err == nil || !strings.Contains(err.Error(), "duplicate") {
		t.Fatalf("ObservePublicationRemoteHead() error = %v, want duplicate JSON rejection", err)
	}
	if clockCalls != 0 {
		t.Fatalf("malformed response sampled clock %d times", clockCalls)
	}
}

func TestGitHubPublicationObserverFailsClosedBeforeProviderRead(t *testing.T) {
	stored := validStoredGitHubCase()
	thread := validGitHubThreadIdentity()
	canceled, cancel := context.WithCancel(context.Background())
	cancel()

	tests := []struct {
		name     string
		observer *githubPublicationRemoteHeadObserver
		ctx      context.Context
		thread   eventing.PRDevelopmentThreadIdentity
		want     error
	}{
		{name: "nil observer", ctx: context.Background(), thread: thread},
		{
			name: "nil verifier", observer: &githubPublicationRemoteHeadObserver{},
			ctx: context.Background(), thread: thread,
		},
		{
			name: "nil runner", observer: &githubPublicationRemoteHeadObserver{verifier: &GitHubVerifier{}},
			ctx: context.Background(), thread: thread,
		},
		{
			name: "nil context", observer: configuredPublicationObserver(&captureToolRunner{}),
			thread: thread,
		},
		{
			name: "canceled context", observer: configuredPublicationObserver(&captureToolRunner{}),
			ctx: canceled, thread: thread, want: context.Canceled,
		},
		{
			name: "invalid provider identity", observer: configuredPublicationObserver(&captureToolRunner{}),
			ctx: context.Background(), thread: func() eventing.PRDevelopmentThreadIdentity {
				invalid := thread
				invalid.Provider = "gitlab"
				return invalid
			}(),
			want: ErrGitHubCaseDrift,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			observed, err := test.observer.ObservePublicationRemoteHead(
				test.ctx,
				stored,
				test.thread,
			)
			if err == nil {
				t.Fatal("ObservePublicationRemoteHead() unexpectedly succeeded")
			}
			if test.want != nil && !errors.Is(err, test.want) {
				t.Fatalf("error = %v, want %v", err, test.want)
			}
			if observed != (TimedPublicationRemoteObservation{}) {
				t.Fatalf("failed observation = %#v, want zero", observed)
			}
			if test.observer != nil && test.observer.verifier != nil {
				if runner, ok := test.observer.verifier.Runner.(*captureToolRunner); ok &&
					len(runner.requests) != 0 {
					t.Fatalf("preflight failure caused %d provider calls", len(runner.requests))
				}
			}
		})
	}
}

func TestGitHubPublicationObserverRejectsOutOfRangeClockAfterRead(t *testing.T) {
	stored := validStoredGitHubCase()
	runner := &captureToolRunner{responses: []string{
		providerCasePullJSON(t, func(map[string]any) {}),
	}}
	observer := &githubPublicationRemoteHeadObserver{
		verifier: &GitHubVerifier{Runner: runner},
		now:      func() time.Time { return time.Time{} },
	}

	observed, err := observer.ObservePublicationRemoteHead(
		context.Background(),
		stored,
		validGitHubThreadIdentity(),
	)
	if err == nil || !strings.Contains(err.Error(), "clock") {
		t.Fatalf("ObservePublicationRemoteHead() error = %v, want clock rejection", err)
	}
	if observed != (TimedPublicationRemoteObservation{}) || len(runner.requests) != 1 {
		t.Fatalf("clock failure returned %#v, provider calls=%d", observed, len(runner.requests))
	}
}

func TestGitHubPublicationObserverConstructorsKeepCapabilitiesDistinct(t *testing.T) {
	runner := &captureToolRunner{responses: []string{
		providerCasePullJSON(t, func(map[string]any) {}),
	}}
	verifier := &GitHubVerifier{Runner: runner}
	provider := NewGitHubPublicationProviderObserver(verifier)
	remote := NewGitHubPublicationRemoteHeadObserver(verifier)
	if _, widened := provider.(PublicationRemoteHeadObserver); widened {
		t.Fatal("review-bearing observer also exposes the remote-head capability")
	}
	if _, widened := remote.(PublicationProviderObserver); widened {
		t.Fatal("remote-head observer also exposes the review-bearing capability")
	}

	// Construction copies the verifier configuration. Mutating the caller's
	// pointer cannot redirect an already-issued narrow observer.
	replacement := &captureToolRunner{}
	verifier.Runner = replacement
	_, err := remote.ObservePublicationRemoteHead(
		context.Background(),
		validStoredGitHubCase(),
		validGitHubThreadIdentity(),
	)
	if err != nil {
		t.Fatalf("ObservePublicationRemoteHead() error = %v", err)
	}
	if len(runner.requests) != 1 || len(replacement.requests) != 0 {
		t.Fatalf(
			"copied observer calls original=%d replacement=%d, want 1/0",
			len(runner.requests),
			len(replacement.requests),
		)
	}
}

func TestGitHubPublicationObserverRejectsCancellationDuringProviderRead(t *testing.T) {
	stored := validStoredGitHubCase()
	thread := validGitHubThreadIdentity()
	for _, test := range []struct {
		name      string
		responses []string
		cancelAt  int
		observe   func(
			context.Context,
			*cancelingPublicationRunner,
			func() time.Time,
		) error
	}{
		{
			name: "full observation",
			responses: []string{
				providerCasePullJSON(t, func(map[string]any) {}),
				providerReviewsJSON(providerReviewValue(
					"CHANGES_REQUESTED",
					stored.Feedback,
				)),
			},
			cancelAt: 2,
			observe: func(
				ctx context.Context,
				runner *cancelingPublicationRunner,
				now func() time.Time,
			) error {
				observer := &githubPublicationProviderObserver{
					verifier: &GitHubVerifier{Runner: runner},
					now:      now,
				}
				observed, err := observer.ObservePublication(ctx, stored, thread)
				if observed != (TimedPublicationProviderObservation{}) {
					t.Fatalf("canceled full observation = %#v, want zero", observed)
				}
				return err
			},
		},
		{
			name: "head-only observation",
			responses: []string{
				providerCasePullJSON(t, func(map[string]any) {}),
			},
			cancelAt: 1,
			observe: func(
				ctx context.Context,
				runner *cancelingPublicationRunner,
				now func() time.Time,
			) error {
				observer := &githubPublicationRemoteHeadObserver{
					verifier: &GitHubVerifier{Runner: runner},
					now:      now,
				}
				observed, err := observer.ObservePublicationRemoteHead(ctx, stored, thread)
				if observed != (TimedPublicationRemoteObservation{}) {
					t.Fatalf("canceled remote observation = %#v, want zero", observed)
				}
				return err
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			clockCalls := 0
			runner := &cancelingPublicationRunner{
				captureToolRunner: captureToolRunner{responses: test.responses},
				cancel:            cancel,
				cancelAt:          test.cancelAt,
			}
			err := test.observe(ctx, runner, func() time.Time {
				clockCalls++
				return time.Now()
			})
			if !errors.Is(err, context.Canceled) {
				t.Fatalf("observation error = %v, want context canceled", err)
			}
			if clockCalls != 0 || len(runner.requests) != test.cancelAt {
				t.Fatalf(
					"canceled observation clock=%d calls=%d, want 0/%d",
					clockCalls,
					len(runner.requests),
					test.cancelAt,
				)
			}
		})
	}
}

func TestGitHubPublicationObserverRejectsCancellationDuringTimestamp(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	runner := &captureToolRunner{responses: []string{
		providerCasePullJSON(t, func(map[string]any) {}),
	}}
	observer := &githubPublicationRemoteHeadObserver{
		verifier: &GitHubVerifier{Runner: runner},
		now: func() time.Time {
			cancel()
			return time.Now()
		},
	}
	observed, err := observer.ObservePublicationRemoteHead(
		ctx,
		validStoredGitHubCase(),
		validGitHubThreadIdentity(),
	)
	if !errors.Is(err, context.Canceled) ||
		observed != (TimedPublicationRemoteObservation{}) {
		t.Fatalf("timestamp cancellation returned %#v, error %v", observed, err)
	}
}

func TestGitHubPublicationObserverRejectsNoncanonicalProjectedRef(t *testing.T) {
	stored := validStoredGitHubCase()
	pull := providerCasePullJSON(t, func(value map[string]any) {
		value["head"].(map[string]any)["ref"] = "feature.lock"
	})
	for _, test := range []struct {
		name    string
		observe func(*captureToolRunner, func() time.Time) error
	}{
		{
			name: "full observation",
			observe: func(runner *captureToolRunner, now func() time.Time) error {
				runner.responses = append(
					runner.responses,
					providerReviewsJSON(providerReviewValue(
						"CHANGES_REQUESTED",
						stored.Feedback,
					)),
				)
				observer := &githubPublicationProviderObserver{
					verifier: &GitHubVerifier{Runner: runner},
					now:      now,
				}
				observed, err := observer.ObservePublication(
					context.Background(),
					stored,
					validGitHubThreadIdentity(),
				)
				if observed != (TimedPublicationProviderObservation{}) {
					t.Fatalf("invalid-ref full observation = %#v", observed)
				}
				return err
			},
		},
		{
			name: "head-only observation",
			observe: func(runner *captureToolRunner, now func() time.Time) error {
				observer := &githubPublicationRemoteHeadObserver{
					verifier: &GitHubVerifier{Runner: runner},
					now:      now,
				}
				observed, err := observer.ObservePublicationRemoteHead(
					context.Background(),
					stored,
					validGitHubThreadIdentity(),
				)
				if observed != (TimedPublicationRemoteObservation{}) {
					t.Fatalf("invalid-ref remote observation = %#v", observed)
				}
				return err
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			runner := &captureToolRunner{responses: []string{pull}}
			clockCalls := 0
			err := test.observe(runner, func() time.Time {
				clockCalls++
				return time.Now()
			})
			if err == nil || clockCalls != 0 {
				t.Fatalf("invalid-ref error=%v clock=%d, want rejection before clock", err, clockCalls)
			}
		})
	}
}

func TestGitHubPublicationRemoteHeadRejectsProviderDriftBeforeClock(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(map[string]any)
	}{
		{
			name: "pull number",
			mutate: func(value map[string]any) {
				value["number"] = 43
			},
		},
		{
			name: "pull URL",
			mutate: func(value map[string]any) {
				value["html_url"] = "https://github.com/ScyllaDB/PicoClaw/pull/43"
			},
		},
		{
			name: "base repository",
			mutate: func(value map[string]any) {
				baseRepository := providerCasePullBase(value)["repo"].(map[string]any)
				baseRepository["full_name"] = "other/PicoClaw"
			},
		},
		{
			name: "pull author",
			mutate: func(value map[string]any) {
				value["user"].(map[string]any)["login"] = "someone-else"
			},
		},
		{
			name: "head object ID",
			mutate: func(value map[string]any) {
				value["head"].(map[string]any)["sha"] = "not-an-object-id"
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			runner := &captureToolRunner{responses: []string{
				providerCasePullJSON(t, test.mutate),
			}}
			clockCalls := 0
			observer := &githubPublicationRemoteHeadObserver{
				verifier: &GitHubVerifier{Runner: runner},
				now: func() time.Time {
					clockCalls++
					return time.Now()
				},
			}
			observed, err := observer.ObservePublicationRemoteHead(
				context.Background(),
				validStoredGitHubCase(),
				validGitHubThreadIdentity(),
			)
			if err == nil || observed != (TimedPublicationRemoteObservation{}) ||
				clockCalls != 0 || len(runner.requests) != 1 {
				t.Fatalf(
					"drift result=%#v error=%v clock=%d calls=%d",
					observed,
					err,
					clockCalls,
					len(runner.requests),
				)
			}
		})
	}
}

type cancelingPublicationRunner struct {
	captureToolRunner
	cancel   context.CancelFunc
	cancelAt int
}

func (runner *cancelingPublicationRunner) RunTool(
	ctx context.Context,
	request workflows.ToolRequest,
) (map[string]any, error) {
	result, err := runner.captureToolRunner.RunTool(ctx, request)
	if len(runner.requests) == runner.cancelAt {
		runner.cancel()
	}
	return result, err
}

func configuredPublicationObserver(
	runner *captureToolRunner,
) *githubPublicationRemoteHeadObserver {
	return &githubPublicationRemoteHeadObserver{
		verifier: &GitHubVerifier{Runner: runner},
	}
}

func assertPublicationObservationIsPrivate(t *testing.T, value any) {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("marshal private publication observation: %v", err)
	}
	if string(raw) != "{}" {
		t.Fatalf("private publication observation JSON = %s, want {}", raw)
	}
}
