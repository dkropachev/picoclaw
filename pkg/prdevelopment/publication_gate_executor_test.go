package prdevelopment

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	sharedattention "github.com/sipeed/picoclaw/pkg/attention"
	"github.com/sipeed/picoclaw/pkg/eventing"
	"github.com/sipeed/picoclaw/pkg/gitworkspace"
	"github.com/sipeed/picoclaw/pkg/prdevelopment/localci"
	"github.com/sipeed/picoclaw/pkg/session"
	"github.com/sipeed/picoclaw/pkg/workflows"
)

func TestPublicationGateExecutorConstructorAndJSONPrivacy(t *testing.T) {
	t.Parallel()

	fixture := newPublicationGateExecutorFixture(t, publicationGateExecutorPassingGates())
	valid := fixture.config()
	var nilStore *publicationGateExecutionStoreFake
	var nilEvidence *publicationGateExecutorEvidenceFake
	var nilWorkspaces AttentionReviewWorkspaceFactory
	var nilProvider *publicationGateProviderFake
	tests := []struct {
		name   string
		mutate func(*PublicationGateExecutorConfig)
	}{
		{name: "store", mutate: func(config *PublicationGateExecutorConfig) {
			config.Store = nilStore
		}},
		{name: "executor", mutate: func(config *PublicationGateExecutorConfig) {
			config.Executor = nil
		}},
		{name: "evidence", mutate: func(config *PublicationGateExecutorConfig) {
			config.Evidence = nilEvidence
		}},
		{name: "workspaces", mutate: func(config *PublicationGateExecutorConfig) {
			config.Workspaces = nilWorkspaces
		}},
		{name: "provider", mutate: func(config *PublicationGateExecutorConfig) {
			config.Provider = nilProvider
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			config := valid
			test.mutate(&config)
			executor, err := NewPublicationGateExecutor(config)
			if executor != nil || !errors.Is(err, ErrUnavailable) {
				t.Fatalf(
					"NewPublicationGateExecutor() = (%#v, %v), want (nil, ErrUnavailable)",
					executor,
					err,
				)
			}
		})
	}

	values := []any{
		valid,
		PublicationGateExecutionResult{
			Publication: fixture.store.publication,
			RunID:       "wr_private",
			Status:      workflows.RunStatusSucceeded,
			Existing:    true,
		},
	}
	for _, value := range values {
		raw, err := json.Marshal(value)
		if err != nil {
			t.Fatalf("json.Marshal(%T) error = %v", value, err)
		}
		if string(raw) != `{}` {
			t.Fatalf("json.Marshal(%T) = %s, want {}", value, raw)
		}
	}
}

func TestPublicationGateExecutorPreflightsCompilationBeforeSubjectPin(t *testing.T) {
	t.Parallel()

	fixture := newPublicationGateExecutorFixture(t, []workflows.GateSpec{{
		ID:        "invalid-subject-path",
		Kind:      workflows.GateDeterministic,
		When:      "inputs.gate_subject.field_that_does_not_exist == true",
		Title:     "Invalid subject path",
		Questions: []any{"This gate must never be pinned."},
	}})
	executor := fixture.executor(t)

	result, err := executor.ExecuteClaim(t.Context(), fixture.claim)
	if !errors.Is(err, errPublicationGateCorrupt) ||
		!reflect.DeepEqual(result, PublicationGateExecutionResult{}) {
		t.Fatalf("ExecuteClaim() = (%#v, %v), want corrupt/zero", result, err)
	}
	operations := fixture.operations()
	if !containsString(operations, "snapshot") ||
		!containsString(operations, "ci-plan") ||
		!containsString(operations, "git-snapshot") {
		t.Fatalf("preflight operations = %v, want bounded subject construction", operations)
	}
	for _, forbidden := range []string{"pin-subject", "provider", "pin-provider", "admit"} {
		if containsString(operations, forbidden) {
			t.Fatalf("compiler failure crossed %q boundary: %v", forbidden, operations)
		}
	}
}

func TestPublicationGateExecutorAcceptsPassedReviewAndPinsBeforeAdmission(t *testing.T) {
	t.Parallel()

	fixture := newPublicationGateExecutorFixture(t, publicationGateExecutorPassingGates())
	executor := fixture.executor(t)
	result, err := executor.ExecuteClaim(t.Context(), fixture.claim)
	if err != nil {
		t.Fatalf("ExecuteClaim() error = %v, operations=%v", err, fixture.operations())
	}
	if result.RunID == "" || result.Status != workflows.RunStatusSucceeded || result.Existing ||
		result.Publication.ClaimToken != "" {
		t.Fatalf("ExecuteClaim() result = %#v", result)
	}
	operations := fixture.operations()
	subjectIndex := indexOfString(operations, "pin-subject")
	providerReadIndex := indexOfString(operations, "provider")
	providerPinIndex := indexOfString(operations, "pin-provider")
	admitIndex := indexOfString(operations, "admit")
	if subjectIndex < 0 || providerReadIndex <= subjectIndex ||
		providerPinIndex <= providerReadIndex || admitIndex <= providerPinIndex {
		t.Fatalf("unsafe publication gate operation order: %v", operations)
	}
	if fixture.store.admitCalls != 1 || fixture.provider.calls != 1 ||
		fixture.workspace.calls != 1 || fixture.evidence.planCalls != 1 ||
		fixture.evidence.executionCalls != 1 {
		t.Fatalf(
			"calls admit/provider/git/plan/execution = %d/%d/%d/%d/%d",
			fixture.store.admitCalls,
			fixture.provider.calls,
			fixture.workspace.calls,
			fixture.evidence.planCalls,
			fixture.evidence.executionCalls,
		)
	}
	if fixture.store.subjectPin.SubjectRevision == "" ||
		len(fixture.store.subjectPin.PinnedSubject) == 0 ||
		fixture.store.providerPin.Observation.Repository != fixture.snapshot.Case.Repository {
		t.Fatalf(
			"subject/provider pins = (%#v, %#v)",
			fixture.store.subjectPin,
			fixture.store.providerPin,
		)
	}
}

func TestPublicationGateExecutorRunsOrderedMixedGateComposition(t *testing.T) {
	t.Parallel()

	fixture := newPublicationGateExecutorFixture(t, []workflows.GateSpec{
		{ID: "disabled", Kind: workflows.GateZero},
		{
			ID:        "deterministic",
			Kind:      workflows.GateDeterministic,
			When:      "false",
			Title:     "Deterministic publication check",
			Questions: []any{"Approve the deterministic exception?"},
		},
		{
			ID:       "isolated",
			Kind:     workflows.GateAIIsolatedContext,
			AgentID:  "isolated-reviewer",
			Criteria: "Escalate only when the bounded publication evidence is ambiguous.",
			Title:    "Isolated publication decision",
		},
		{
			ID:       "working",
			Kind:     workflows.GateAIWorkingContext,
			AgentID:  "owner-agent",
			Criteria: "Escalate only when the frozen PR discussion leaves the decision unresolved.",
			Title:    "Working-context publication decision",
		},
	})
	runtimeFixture := newAttentionRuntimeFixture(t)
	agent := &attentionRuntimeGateAgent{
		backend: runtimeFixture.sessions, runtimeActive: &runtimeFixture.runtimeActive,
	}
	config := fixture.config()
	config.Executor.Agents = agent
	config.AcquireRuntime = func(
		ctx context.Context,
		agentID string,
	) (context.Context, session.SessionStore, func(), error) {
		if agentID != "owner-agent" ||
			!runtimeFixture.runtimeActive.CompareAndSwap(false, true) {
			return nil, nil, nil, errors.New("unexpected mixed-gate runtime acquisition")
		}
		return ctx, runtimeFixture.sessions, func() {
			runtimeFixture.runtimeActive.Store(false)
		}, nil
	}
	executor, err := NewPublicationGateExecutor(config)
	if err != nil {
		t.Fatalf("NewPublicationGateExecutor() error = %v", err)
	}

	result, err := executor.ExecuteClaim(t.Context(), fixture.claim)
	if err != nil || result.Status != workflows.RunStatusSucceeded {
		t.Fatalf("ExecuteClaim() = (%#v, %v), operations=%v", result, err, fixture.operations())
	}
	if runtimeFixture.runtimeActive.Load() {
		t.Fatal("mixed-gate runtime remained leased after completion")
	}
	if len(agent.requests) != 2 {
		t.Fatalf("mixed-gate model requests = %#v, want isolated then working", agent.requests)
	}
	isolated, working := agent.requests[0], agent.requests[1]
	if isolated.AgentID != "isolated-reviewer" || !isolated.EphemeralSession ||
		isolated.History != "none" || isolated.Cache != "none" ||
		isolated.FrozenReadOnlySession != nil ||
		working.AgentID != "owner-agent" || working.EphemeralSession ||
		working.History != "read_only" || working.FrozenReadOnlySession == nil {
		t.Fatalf("mixed-gate execution profiles = isolated %#v, working %#v", isolated, working)
	}
}

func TestPublicationGateExecutorRejectsForeignWorkingAgentBeforeReadsOrAdmission(t *testing.T) {
	t.Parallel()

	fixture := newPublicationGateExecutorFixture(t, []workflows.GateSpec{{
		ID:       "working",
		Kind:     workflows.GateAIWorkingContext,
		AgentID:  "another-agent",
		Criteria: "Ask only if this exact candidate cannot be resolved.",
		Title:    "Working-context decision",
	}})
	executor := fixture.executor(t)

	result, err := executor.ExecuteClaim(t.Context(), fixture.claim)
	if !errors.Is(err, errPublicationGateCorrupt) ||
		!reflect.DeepEqual(result, PublicationGateExecutionResult{}) {
		t.Fatalf("ExecuteClaim() = (%#v, %v), want corrupt/zero", result, err)
	}
	if got, want := fixture.operations(), []string{"authenticate", "snapshot"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("operations = %v, want %v", got, want)
	}
	if fixture.workspaceFactoryCalls != 0 || fixture.workspace.calls != 0 ||
		fixture.provider.calls != 0 || fixture.store.admitCalls != 0 ||
		fixture.evidence.planCalls != 0 || fixture.evidence.executionCalls != 0 {
		t.Fatalf(
			"rejected working gate used workspace/provider/admit/CI: %d/%d/%d/%d/%d/%d",
			fixture.workspaceFactoryCalls,
			fixture.workspace.calls,
			fixture.provider.calls,
			fixture.store.admitCalls,
			fixture.evidence.planCalls,
			fixture.evidence.executionCalls,
		)
	}
}

func TestPublicationGateExecutorCompletePinsReplayBeforeMutableReads(t *testing.T) {
	t.Parallel()

	fixture := newPublicationGateExecutorFixture(t, publicationGateExecutorPassingGates())
	executor := fixture.executor(t)
	first, err := executor.ExecuteClaim(t.Context(), fixture.claim)
	if err != nil || first.Status != workflows.RunStatusSucceeded || first.Existing {
		t.Fatalf("first ExecuteClaim() = (%#v, %v), ops=%v", first, err, fixture.operations())
	}
	providerCalls := fixture.provider.calls
	workspaceCalls := fixture.workspace.calls
	evidenceCalls := fixture.evidence.planCalls + fixture.evidence.executionCalls
	fixture.clearOperations()

	replayed, err := executor.ExecuteClaim(t.Context(), fixture.claim)
	if err != nil {
		t.Fatalf("replayed ExecuteClaim() error = %v, operations=%v", err, fixture.operations())
	}
	if !replayed.Existing || replayed.RunID != first.RunID ||
		replayed.Status != first.Status {
		t.Fatalf("replayed ExecuteClaim() = %#v, first=%#v", replayed, first)
	}
	if got, want := fixture.operations(), []string{"authenticate", "find-run"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("replay operations = %v, want %v", got, want)
	}
	if fixture.provider.calls != providerCalls || fixture.workspace.calls != workspaceCalls ||
		fixture.evidence.planCalls+fixture.evidence.executionCalls != evidenceCalls ||
		fixture.store.admitCalls != 1 {
		t.Fatal("exact replay consulted mutable context/provider or repeated admission")
	}
}

func TestPublicationGateExecutorConcurrentClaimsConvergeOnOneDurableRun(t *testing.T) {
	t.Parallel()

	fixture := newPublicationGateExecutorFixture(t, []workflows.GateSpec{{
		ID:       "isolated",
		Kind:     workflows.GateAIIsolatedContext,
		AgentID:  "publication-reviewer",
		Criteria: "Resolve this exact frozen publication candidate without user input.",
		Title:    "Concurrent publication decision",
	}})
	agent := newPublicationGateConcurrentAgent()
	config := fixture.config()
	config.Executor.Agents = agent
	executor, err := NewPublicationGateExecutor(config)
	if err != nil {
		t.Fatalf("NewPublicationGateExecutor() error = %v", err)
	}
	// Establish the immutable subject and provider pins before racing decision
	// admission. If both callers also race the one-time pinning path, one can
	// legitimately observe the other pin between authentication and refresh and
	// return before the decision boundary, leaving the artificial two-party
	// find barrier with only one participant. This test is specifically about
	// concurrent admission of the same fully pinned decision.
	fixture.store.findErr = eventing.ErrClosed
	primed, primeErr := executor.ExecuteClaim(t.Context(), fixture.claim)
	if !errors.Is(primeErr, workflows.ErrRunAdmissionUnavailable) ||
		!reflect.DeepEqual(primed, PublicationGateExecutionResult{}) {
		t.Fatalf(
			"prime immutable gate pins = (%#v, %v), want private-run unavailable",
			primed,
			primeErr,
		)
	}
	fixture.store.findErr = nil
	primedPublication := fixture.store.current()
	if primedPublication.SubjectRevision == "" ||
		primedPublication.ProviderObservationHash == "" ||
		primedPublication.DecisionRunID != "" {
		t.Fatalf(
			"primed publication = %#v, want complete pins without a decision run",
			primedPublication,
		)
	}
	fixture.clearOperations()
	// Freeze both callers' first exact decision lookup at the same absent
	// snapshot. The admission barrier then proves that both launch attempts
	// race at the real decision boundary rather than relying on scheduler luck.
	fixture.store.findBarrier = newPublicationGateExecutorBarrier(2)
	fixture.store.admitBarrier = newPublicationGateExecutorBarrier(2)

	type outcome struct {
		result PublicationGateExecutionResult
		err    error
	}
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	start := make(chan struct{})
	outcomes := make(chan outcome, 2)
	var callers sync.WaitGroup
	callers.Add(2)
	for range 2 {
		go func() {
			defer callers.Done()
			<-start
			result, executeErr := executor.ExecuteClaim(ctx, fixture.claim)
			outcomes <- outcome{result: result, err: executeErr}
		}()
	}
	close(start)

	select {
	case <-agent.started:
	case <-ctx.Done():
		close(agent.release)
		callers.Wait()
		got := make([]outcome, 0, 2)
		for len(outcomes) > 0 {
			got = append(got, <-outcomes)
		}
		t.Fatalf("concurrent executor did not reach model: %v outcomes=%#v operations=%v current=%#v",
			ctx.Err(), got, fixture.operations(), fixture.store.current())
	}
	var inFlight outcome
	select {
	case inFlight = <-outcomes:
	case <-ctx.Done():
		t.Fatalf("concurrent duplicate did not resolve while winning model was active: %v", ctx.Err())
	}
	if !errors.Is(inFlight.err, sharedattention.ErrPrivateRunAdmissionUncertain) ||
		!reflect.DeepEqual(inFlight.result, PublicationGateExecutionResult{}) {
		t.Fatalf("in-flight duplicate = (%#v, %v), want admission uncertainty", inFlight.result, inFlight.err)
	}
	close(agent.release)

	var winner outcome
	select {
	case winner = <-outcomes:
	case <-ctx.Done():
		t.Fatalf("winning executor did not finish: %v", ctx.Err())
	}
	callers.Wait()
	if winner.err != nil || winner.result.RunID == "" ||
		winner.result.Status != workflows.RunStatusSucceeded || winner.result.Existing {
		t.Fatalf("winning executor = (%#v, %v), operations=%v", winner.result, winner.err, fixture.operations())
	}

	replayed, err := executor.ExecuteClaim(ctx, fixture.claim)
	if err != nil || !replayed.Existing || replayed.RunID != winner.result.RunID ||
		replayed.Status != workflows.RunStatusSucceeded {
		t.Fatalf("post-concurrency replay = (%#v, %v), winner=%#v", replayed, err, winner.result)
	}
	runs, err := fixture.runs.ListRuns(ctx)
	if err != nil {
		t.Fatalf("ListRuns() error = %v", err)
	}
	current := fixture.store.current()
	fixture.store.mu.Lock()
	links := len(fixture.store.links)
	link := fixture.store.links[publicationDecisionKey(current)]
	admitCalls := fixture.store.admitCalls
	createCalls := fixture.store.createCalls
	fixture.store.mu.Unlock()
	if len(runs) != 1 || runs[0].ID != winner.result.RunID ||
		runs[0].Status != workflows.RunStatusSucceeded ||
		links != 1 || link.RunID != winner.result.RunID ||
		current.DecisionRunID != winner.result.RunID ||
		current.SubjectRevision == "" || current.ProviderObservationHash == "" {
		t.Fatalf(
			"durable convergence: runs=%#v links=%d link=%#v publication=%#v",
			runs,
			links,
			link,
			current,
		)
	}
	if admitCalls != 2 || createCalls != 1 || agent.callsValue() != 1 {
		t.Fatalf("admission/create/model calls = %d/%d/%d, want 2/1/1",
			admitCalls, createCalls, agent.callsValue())
	}
}

func TestPublicationGateExecutorCompletePinLookupFailuresNeedRecovery(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		mutate func(*publicationGateExecutorFixture)
	}{
		{
			name: "durable conflict",
			mutate: func(fixture *publicationGateExecutorFixture) {
				fixture.store.findErr = eventing.ErrPRDevelopmentPublicationConflict
			},
		},
		{
			name: "malformed link",
			mutate: func(fixture *publicationGateExecutorFixture) {
				key := publicationDecisionKey(fixture.store.current())
				fixture.store.links[key] = eventing.PRDevelopmentPublicationDecisionRunLink{}
			},
		},
		{
			name: "cross-bound key",
			mutate: func(fixture *publicationGateExecutorFixture) {
				key := publicationDecisionKey(fixture.store.current())
				link := fixture.store.links[key]
				link.Key.ReviewLedgerEntryHash = strings.Repeat("a", 64)
				fixture.store.links[key] = link
			},
		},
		{
			name: "cross-bound run",
			mutate: func(fixture *publicationGateExecutorFixture) {
				key := publicationDecisionKey(fixture.store.current())
				link := fixture.store.links[key]
				link.RunID = "wr_" + strings.Repeat("a", 32)
				fixture.store.links[key] = link
			},
		},
		{
			name: "missing linked run",
			mutate: func(fixture *publicationGateExecutorFixture) {
				fixture.runs = &publicationGateExecutorRunStoreFault{
					RunStore: fixture.runs,
					getErr:   os.ErrNotExist,
				}
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			fixture := newPublicationGateExecutorFixture(t, publicationGateExecutorPassingGates())
			first, err := fixture.executor(t).ExecuteClaim(t.Context(), fixture.claim)
			if err != nil || first.Status != workflows.RunStatusSucceeded || first.Existing {
				t.Fatalf("first ExecuteClaim() = (%#v, %v), ops=%v", first, err, fixture.operations())
			}
			providerCalls := fixture.provider.calls
			workspaceCalls := fixture.workspace.calls
			evidenceCalls := fixture.evidence.planCalls + fixture.evidence.executionCalls
			admitCalls := fixture.store.admitCalls
			test.mutate(fixture)
			fixture.clearOperations()

			result, lookupErr := fixture.executor(t).ExecuteClaim(t.Context(), fixture.claim)
			if !errors.Is(lookupErr, sharedattention.ErrPrivateRunAdmissionUncertain) ||
				!reflect.DeepEqual(result, PublicationGateExecutionResult{}) {
				t.Fatalf("replayed ExecuteClaim() = (%#v, %v), want recovery", result, lookupErr)
			}
			if got, want := fixture.operations(), []string{"authenticate", "find-run"}; !reflect.DeepEqual(got, want) {
				t.Fatalf("recovery lookup operations = %v, want %v", got, want)
			}
			if fixture.provider.calls != providerCalls || fixture.workspace.calls != workspaceCalls ||
				fixture.evidence.planCalls+fixture.evidence.executionCalls != evidenceCalls ||
				fixture.store.admitCalls != admitCalls {
				t.Fatal("recovery lookup consulted mutable context or repeated admission")
			}
		})
	}
}

func TestPublicationGateExecutorAdmissionUncertaintyFailsClosed(t *testing.T) {
	t.Parallel()

	fixture := newPublicationGateExecutorFixture(t, publicationGateExecutorPassingGates())
	fixture.store.uncertainAfterCreate = true
	executor := fixture.executor(t)
	result, err := executor.ExecuteClaim(t.Context(), fixture.claim)
	if !errors.Is(err, sharedattention.ErrPrivateRunAdmissionUncertain) ||
		result.RunID != "" || result.Status != "" || result.Existing ||
		result.Publication.ClaimToken != "" {
		t.Fatalf("ExecuteClaim() = (%#v, %v), want admission uncertainty", result, err)
	}
	if fixture.store.admitCalls != 1 || !containsString(fixture.operations(), "admit") {
		t.Fatalf("admission operations = %v", fixture.operations())
	}
	if len(fixture.store.links) != 0 {
		t.Fatalf("uncertain admission published a decision link: %#v", fixture.store.links)
	}
}

func TestPublicationGateExecutorExpiredClaimAdmissionDoesNotCreateRun(t *testing.T) {
	t.Parallel()

	fixture := newPublicationGateExecutorFixture(t, publicationGateExecutorPassingGates())
	fixture.store.admitErr = eventing.ErrStaleLease
	executor := fixture.executor(t)
	result, err := executor.ExecuteClaim(t.Context(), fixture.claim)
	if !errors.Is(err, workflows.ErrRunAdmissionConflict) || result.RunID != "" ||
		result.Status != "" || result.Existing {
		t.Fatalf("ExecuteClaim() = (%#v, %v), want admission conflict", result, err)
	}
	runs, listErr := fixture.runs.ListRuns(t.Context())
	if listErr != nil {
		t.Fatalf("ListRuns() error = %v", listErr)
	}
	if fixture.store.admitCalls != 1 || len(runs) != 0 || len(fixture.store.links) != 0 {
		t.Fatalf(
			"expired admission calls/runs/links = %d/%d/%d",
			fixture.store.admitCalls,
			len(runs),
			len(fixture.store.links),
		)
	}
}

func TestPublicationGateExecutorGenuineAdmissionConflictRemainsConflict(t *testing.T) {
	t.Parallel()

	fixture := newPublicationGateExecutorFixture(t, publicationGateExecutorPassingGates())
	fixture.store.admitErr = eventing.ErrPRDevelopmentPublicationConflict
	result, err := fixture.executor(t).ExecuteClaim(t.Context(), fixture.claim)
	if !errors.Is(err, workflows.ErrRunAdmissionConflict) ||
		!reflect.DeepEqual(result, PublicationGateExecutionResult{}) {
		t.Fatalf("ExecuteClaim() = (%#v, %v), want admission conflict", result, err)
	}
	runs, listErr := fixture.runs.ListRuns(t.Context())
	if listErr != nil {
		t.Fatalf("ListRuns() error = %v", listErr)
	}
	if fixture.store.admitCalls != 1 || len(runs) != 0 || len(fixture.store.links) != 0 {
		t.Fatalf(
			"conflicting admission calls/runs/links = %d/%d/%d",
			fixture.store.admitCalls,
			len(runs),
			len(fixture.store.links),
		)
	}
}

func TestPublicationGateExecutorWrongExistingAdmissionLinkNeedsRecovery(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		link eventing.PRDevelopmentPublicationDecisionRunLink
	}{
		{
			name: "wrong key",
			link: eventing.PRDevelopmentPublicationDecisionRunLink{
				Key: eventing.PRDevelopmentPublicationDecisionKey{
					PublicationID: "pdpub_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
				},
				RunID: "wr_" + strings.Repeat("a", 32),
			},
		},
		{
			name: "wrong run",
			link: eventing.PRDevelopmentPublicationDecisionRunLink{
				RunID: "wr_" + strings.Repeat("b", 32),
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			fixture := newPublicationGateExecutorFixture(t, publicationGateExecutorPassingGates())
			link := test.link
			if link.Key == (eventing.PRDevelopmentPublicationDecisionKey{}) {
				link.Key = publicationDecisionKey(fixture.store.current())
			}
			fixture.store.admitResult = &link

			result, err := fixture.executor(t).ExecuteClaim(t.Context(), fixture.claim)
			if !errors.Is(err, sharedattention.ErrPrivateRunAdmissionUncertain) ||
				!reflect.DeepEqual(result, PublicationGateExecutionResult{}) {
				t.Fatalf("ExecuteClaim() = (%#v, %v), want recovery", result, err)
			}
			runs, listErr := fixture.runs.ListRuns(t.Context())
			if listErr != nil {
				t.Fatalf("ListRuns() error = %v", listErr)
			}
			if fixture.store.admitCalls != 1 || len(runs) != 0 || len(fixture.store.links) != 0 {
				t.Fatalf(
					"wrong-link admission calls/runs/links = %d/%d/%d",
					fixture.store.admitCalls,
					len(runs),
					len(fixture.store.links),
				)
			}
		})
	}
}

func TestPublicationGateExecutorPinnedModelSubjectExcludesConversationBodies(t *testing.T) {
	t.Parallel()

	fixture := newPublicationGateExecutorFixture(t, publicationGateExecutorPassingGates())
	secretOne := "conversation-body-must-stay-in-protected-session-one"
	secretTwo := "conversation-body-must-stay-in-protected-session-two"
	fixture.setConversationBodies(secretOne, secretTwo)
	executor := fixture.executor(t)
	result, err := executor.ExecuteClaim(t.Context(), fixture.claim)
	if err != nil || result.Status != workflows.RunStatusSucceeded {
		t.Fatalf("ExecuteClaim() = (%#v, %v), operations=%v", result, err, fixture.operations())
	}
	pinned := fixture.store.subjectPin.PinnedSubject
	if bytes.Contains(pinned, []byte(secretOne)) || bytes.Contains(pinned, []byte(secretTwo)) {
		t.Fatalf("pinned subject leaked conversation bodies: %s", pinned)
	}
	policy, found, err := decodePublicationGatePolicy(fixture.store.current())
	if err != nil || !found {
		t.Fatalf("decodePublicationGatePolicy() = (%v, %v)", found, err)
	}
	envelope, subject, found, err := decodePublicationActiveSubject(fixture.store.current(), policy)
	if err != nil || !found || len(envelope.ModelSubject) == 0 || subject == nil {
		t.Fatalf("decodePublicationActiveSubject() = (%#v, %#v, %v, %v)", envelope, subject, found, err)
	}
	if bytes.Contains(envelope.ModelSubject, []byte(secretOne)) ||
		bytes.Contains(envelope.ModelSubject, []byte(secretTwo)) {
		t.Fatalf("private model subject leaked conversation bodies: %s", envelope.ModelSubject)
	}
	conversation, ok := subject["conversation"].(map[string]any)
	if !ok || conversation["message_count"] != json.Number("2") ||
		conversation["storage"] != "protected_read_only_session" {
		t.Fatalf("conversation metadata = %#v", subject["conversation"])
	}
}

func TestPublicationGateExecutorWorkingGateFreezesPinnedTranscriptAfterLaterChat(t *testing.T) {
	t.Parallel()

	fixture := newPublicationGateExecutorFixture(t, []workflows.GateSpec{{
		ID:       "working",
		Kind:     workflows.GateAIWorkingContext,
		AgentID:  "owner-agent",
		Criteria: "Ask only when the frozen discussion cannot resolve publication.",
		Title:    "Resolve from the frozen discussion",
	}})
	firstBody := "protected-first-publication-message"
	secondBody := "protected-second-publication-message"
	laterBody := "later-chat-must-not-enter-the-pinned-run"
	fixture.setConversationBodies(firstBody, secondBody)
	runtimeFixture := newAttentionRuntimeFixture(t)
	agent := &attentionRuntimeGateAgent{
		backend: runtimeFixture.sessions, runtimeActive: &runtimeFixture.runtimeActive,
	}
	config := fixture.config()
	config.Executor.Agents = agent
	config.AcquireRuntime = func(
		ctx context.Context,
		agentID string,
	) (context.Context, session.SessionStore, func(), error) {
		if agentID != "owner-agent" ||
			!runtimeFixture.runtimeActive.CompareAndSwap(false, true) {
			return nil, nil, nil, errors.New("unexpected publication runtime acquisition")
		}
		return ctx, runtimeFixture.sessions, func() {
			runtimeFixture.runtimeActive.Store(false)
		}, nil
	}
	config.Provider = publicationGateExecutorProviderHook{
		inner: fixture.provider,
		after: func() {
			fixture.appendCurrentConversation(laterBody, strings.Repeat("4", 64))
		},
	}
	executor, err := NewPublicationGateExecutor(config)
	if err != nil {
		t.Fatalf("NewPublicationGateExecutor() error = %v", err)
	}
	result, err := executor.ExecuteClaim(t.Context(), fixture.claim)
	if err != nil || result.Status != workflows.RunStatusSucceeded {
		t.Fatalf("ExecuteClaim() = (%#v, %v), operations=%v", result, err, fixture.operations())
	}
	if runtimeFixture.runtimeActive.Load() {
		t.Fatal("working-context runtime remained leased after gate completion")
	}
	if fixture.store.pinnedSnapshotCalls < 2 {
		t.Fatalf("pinned snapshot calls = %d, want initial and runtime refresh", fixture.store.pinnedSnapshotCalls)
	}
	if len(agent.requests) != 1 || agent.requests[0].FrozenReadOnlySession == nil {
		t.Fatalf("working gate agent requests = %#v", agent.requests)
	}
	history := agent.requests[0].FrozenReadOnlySession.Snapshot.History
	if len(history) != 2 || history[0].Content != firstBody || history[1].Content != secondBody {
		t.Fatalf("frozen publication history = %#v", history)
	}
	for _, value := range []any{
		agent.requests[0].Inputs,
		fixture.store.subjectPin.PinnedSubject,
	} {
		raw, marshalErr := json.Marshal(value)
		if marshalErr != nil {
			t.Fatalf("json.Marshal(%T) error = %v", value, marshalErr)
		}
		if bytes.Contains(raw, []byte(firstBody)) || bytes.Contains(raw, []byte(secondBody)) ||
			bytes.Contains(raw, []byte(laterBody)) {
			t.Fatalf("private model input leaked conversation body: %s", raw)
		}
	}
}

func TestPublicationGateExecutorUsesConcurrentSubjectWinnerAnchor(t *testing.T) {
	t.Parallel()

	fixture := newPublicationGateExecutorFixture(t, publicationGateExecutorPassingGates())
	winner := clonePublicationGateExecutorSnapshot(fixture.snapshot)
	appendPublicationGateExecutorConversation(
		&winner,
		"concurrent winner message",
		strings.Repeat("5", 64),
	)
	canonical, revision := fixture.activeSubject(t, winner)
	fixture.clearOperations()
	fixture.store.subjectWinner = &publicationGateSubjectWinner{
		canonical:           canonical,
		revision:            revision,
		conversationVersion: winner.Conversation.Version,
		transcriptDigest:    winner.TranscriptDigest,
	}
	fixture.store.subjectWinnerSnapshot = &winner
	executor := fixture.executor(t)
	result, err := executor.ExecuteClaim(t.Context(), fixture.claim)
	if err != nil || result.Status != workflows.RunStatusSucceeded {
		t.Fatalf("ExecuteClaim() = (%#v, %v), operations=%v", result, err, fixture.operations())
	}
	operations := fixture.operations()
	if indexOfString(operations, "pinned-snapshot") <= indexOfString(operations, "pin-subject") ||
		indexOfString(operations, "provider") <= indexOfString(operations, "pinned-snapshot") {
		t.Fatalf("concurrent subject winner did not reload its anchor: %v", operations)
	}
	if fixture.store.subjectPin.SubjectRevision == revision {
		t.Fatal("test did not exercise a different concurrent subject winner")
	}
	if current := fixture.store.current(); current.SubjectRevision != revision ||
		!bytes.Equal(current.PinnedSubject, canonical) {
		t.Fatalf("durable subject winner = %#v", current)
	}
}

func publicationGateExecutorPassingGates() []workflows.GateSpec {
	return []workflows.GateSpec{{
		ID:        "local-policy",
		Kind:      workflows.GateDeterministic,
		When:      "false",
		Title:     "Local publication policy",
		Questions: []any{"Approve this exact local candidate?"},
	}}
}

type publicationGateExecutorFixture struct {
	t                     *testing.T
	base                  *publicationGateProcessorFixture
	store                 *publicationGateExecutionStoreFake
	snapshot              eventing.PRDevelopmentPublicationGateContextSnapshot
	claim                 eventing.PRDevelopmentPublication
	policy                sharedattention.PreparedPolicy
	provider              *publicationGateProviderFake
	evidence              *publicationGateExecutorEvidenceFake
	workspace             *publicationGateExecutorWorkspaceFake
	runs                  workflows.RunStore
	workspaceDir          string
	workspaceFactoryCalls int
}

func newPublicationGateExecutorFixture(
	t *testing.T,
	gates []workflows.GateSpec,
) *publicationGateExecutorFixture {
	t.Helper()
	attentionSnapshot, plan, execution, diff := attentionRuntimeSnapshot(t)
	attentionSnapshot.ReviewEntry.ReviewOutcome = eventing.PRDevelopmentLedgerReviewPassed
	attentionSnapshot.ReviewEntry.Summary = "the exact local review passed"
	attentionSnapshot.ReviewEntry.Findings = nil
	attentionSnapshot.Ledger.Entries[len(attentionSnapshot.Ledger.Entries)-1] = attentionSnapshot.ReviewEntry
	attentionSnapshot.Thread.Identity = eventing.PRDevelopmentThreadIdentity{
		Provider:       "github",
		ProviderOrigin: "https://github.com",
		PullAuthorID:   "user-1",
		RepositoryID:   "repo-1",
		PullRequestID:  "pull-42",
		PullNumber:     attentionSnapshot.Case.PullNumber,
	}
	cloneURL := "https://github.com/owner/repo.git"
	reviewDigest := "sha256:" + strings.Repeat("d", 64)
	attentionSnapshot.OwnerSession.HeadRepository = attentionSnapshot.Case.HeadRepository
	attentionSnapshot.OwnerSession.HeadRef = attentionSnapshot.Case.HeadRef
	attentionSnapshot.OwnerSession.HeadSHA = attentionSnapshot.Case.HeadSHA
	attentionSnapshot.OwnerSession.CloneURL = cloneURL
	attentionSnapshot.OwnerSession.ReviewDigest = reviewDigest
	if err := validateAttentionSnapshotForReviewOutcome(
		attentionSnapshot,
		eventing.PRDevelopmentLedgerReviewPassed,
	); err != nil {
		t.Fatalf("passed publication attention snapshot is invalid: %v", err)
	}

	now := time.Date(2026, 8, 11, 12, 0, 0, 0, time.UTC)
	claimUntil := now.Add(5 * time.Minute)
	attemptEntry := attentionSnapshot.Ledger.Entries[len(attentionSnapshot.Ledger.Entries)-2]
	reviewEntry := attentionSnapshot.ReviewEntry
	publication := eventing.PRDevelopmentPublication{
		ID:                       "pdpub_10101010101010101010101010101010",
		CaseID:                   attentionSnapshot.Case.ID,
		ThreadID:                 attentionSnapshot.Thread.ID,
		ControllerID:             attentionSnapshot.Controller.ID,
		ControllerRevision:       attentionSnapshot.Controller.Revision,
		OwnerSessionID:           attentionSnapshot.OwnerSession.ID,
		AttemptID:                attemptEntry.AttemptID,
		FenceOrdinal:             attentionSnapshot.Fence.Ordinal,
		FenceHash:                attentionSnapshot.Fence.FenceHash,
		AttemptLedgerEntryID:     attemptEntry.ID,
		AttemptLedgerEntryKind:   attemptEntry.Kind,
		AttemptLedgerEntryHash:   attemptEntry.EntryHash,
		ReviewLedgerEntryID:      reviewEntry.ID,
		ReviewLedgerEntryKind:    reviewEntry.Kind,
		ReviewLedgerEntryHash:    reviewEntry.EntryHash,
		ReviewOutcome:            reviewEntry.ReviewOutcome,
		OrchestrationPhase:       eventing.PRDevelopmentRepairOrchestrationCompleted,
		OrchestrationReceiptHash: strings.Repeat("f", 64),
		CIStatus:                 eventing.PRDevelopmentCIPassed,
		CIPlanDigest:             attemptEntry.CIPlanDigest,
		CIResultDigest:           attemptEntry.CIResultDigest,
		WorkspaceID:              attentionSnapshot.Controller.WorkspaceID,
		LineID:                   attentionSnapshot.Fence.LineID,
		SourceCloneURL:           cloneURL,
		SourceRef:                attentionSnapshot.Case.HeadRef,
		SourceCommit:             attentionSnapshot.Case.HeadSHA,
		SourceTree:               strings.Repeat("1", 40),
		LineVersion:              attentionSnapshot.Fence.LineVersion,
		MutationEpoch:            attentionSnapshot.Fence.MutationEpoch,
		ParkIntentID:             attentionSnapshot.Fence.ParkIntentID,
		BaseCommit:               attentionSnapshot.Fence.BaseCommit,
		TipCommit:                attentionSnapshot.Fence.TipCommit,
		Tree:                     attentionSnapshot.Fence.Tree,
		NoChanges:                attentionSnapshot.Fence.NoChanges,
		Status:                   eventing.PRDevelopmentPublicationClaimed,
		ClaimFrom:                eventing.PRDevelopmentPublicationPending,
		ClaimOwner:               "publication-gate-executor-test",
		ClaimToken:               strings.Repeat("9", 64),
		ClaimUntil:               &claimUntil,
		ClaimEpoch:               1,
		Claims:                   1,
		ClaimedAt:                &now,
		AvailableAt:              now,
		CreatedAt:                now,
		UpdatedAt:                now,
	}
	prepared, err := sharedattention.PrepareSnapshot(sharedattention.PolicySnapshot{
		Revision: "publication-gate-executor-policy-v1",
		Global:   append([]workflows.GateSpec(nil), gates...),
	})
	if err != nil {
		t.Fatalf("PrepareSnapshot() error = %v", err)
	}
	publication.PolicyRevision = prepared.DecisionRevision()
	publication.PinnedPolicy = prepared.Canonical()
	publication.PinnedPolicyHash = strings.Repeat("1", 64)
	orchestration := eventing.PRDevelopmentRepairOrchestration{
		AttemptID:    publication.AttemptID,
		SessionID:    publication.OwnerSessionID,
		CaseID:       publication.CaseID,
		ThreadID:     publication.ThreadID,
		Phase:        publication.OrchestrationPhase,
		ReviewDigest: reviewDigest,
	}
	gateSnapshot := eventing.PRDevelopmentPublicationGateContextSnapshot{
		Publication:      publication,
		SelectedOrdinal:  attentionSnapshot.HighWater.SelectedOrdinal,
		TranscriptDigest: attentionSnapshot.HighWater.TranscriptDigest,
		Case:             attentionSnapshot.Case,
		Thread:           attentionSnapshot.Thread,
		Conversation:     attentionSnapshot.Conversation,
		OwnerSession:     attentionSnapshot.OwnerSession,
		Controller:       attentionSnapshot.Controller,
		Fence:            attentionSnapshot.Fence,
		Orchestration:    orchestration,
		Ledger:           attentionSnapshot.Ledger,
		AttemptEntry:     attemptEntry,
		ReviewEntry:      reviewEntry,
	}
	base := &publicationGateProcessorFixture{t: t}
	baseStore := &publicationGateStoreFake{
		fixture:          base,
		publication:      publication,
		snapshot:         gateSnapshot,
		failAfter:        make(map[string]error),
		responseMutators: make(map[string]func(*eventing.PRDevelopmentPublication)),
	}
	base.store = baseStore
	observed := TimedPublicationProviderObservation{
		Observation: eventing.PRDevelopmentPublicationProviderObservation{
			Repository:         gateSnapshot.Case.Repository,
			PullNumber:         gateSnapshot.Case.PullNumber,
			HeadRepository:     gateSnapshot.Case.HeadRepository,
			HeadRef:            gateSnapshot.Case.HeadRef,
			HeadSHA:            gateSnapshot.Case.HeadSHA,
			HeadCloneURL:       cloneURL,
			CurrentReviewState: gateSnapshot.Case.CurrentReviewState,
			ReviewDigest:       reviewDigest,
		},
		ObservedAt: now.Add(time.Second),
	}
	base.observed = observed
	base.provider = &publicationGateProviderFake{fixture: base, observation: observed}
	store := &publicationGateExecutionStoreFake{
		publicationGateStoreFake: baseStore,
		links: make(
			map[eventing.PRDevelopmentPublicationDecisionKey]eventing.PRDevelopmentPublicationDecisionRunLink,
		),
	}
	workspaceDir := t.TempDir()
	fixture := &publicationGateExecutorFixture{
		t:            t,
		base:         base,
		store:        store,
		snapshot:     gateSnapshot,
		claim:        clonePublicationGatePublication(publication),
		policy:       prepared,
		provider:     base.provider,
		workspaceDir: workspaceDir,
		runs:         workflows.NewFileRunStore(workspaceDir),
	}
	fixture.evidence = &publicationGateExecutorEvidenceFake{
		fixture: fixture,
		plan:    plan,
		run:     execution,
	}
	fixture.workspace = &publicationGateExecutorWorkspaceFake{
		fixture:  fixture,
		snapshot: diff,
	}
	converted, convertErr := publicationGateAttentionSnapshot(gateSnapshot)
	if convertErr != nil {
		t.Fatalf("publicationGateAttentionSnapshot() error = %v", convertErr)
	}
	if err = validateAttentionSnapshotForReviewOutcome(
		converted,
		eventing.PRDevelopmentLedgerReviewPassed,
	); err != nil {
		t.Fatalf("converted passed publication snapshot is invalid: %v", err)
	}
	return fixture
}

func (fixture *publicationGateExecutorFixture) config() PublicationGateExecutorConfig {
	return PublicationGateExecutorConfig{
		Store: fixture.store,
		Executor: &workflows.Executor{
			WorkspaceDir: fixture.workspaceDir,
			Store:        fixture.runs,
		},
		Runs:     fixture.runs,
		Evidence: fixture.evidence,
		Workspaces: func() (AttentionReviewWorkspace, error) {
			fixture.base.mu.Lock()
			fixture.workspaceFactoryCalls++
			fixture.base.mu.Unlock()
			fixture.record("workspace")
			return fixture.workspace, nil
		},
		Provider: fixture.provider,
	}
}

func (fixture *publicationGateExecutorFixture) executor(t *testing.T) *PublicationGateExecutor {
	t.Helper()
	executor, err := NewPublicationGateExecutor(fixture.config())
	if err != nil {
		t.Fatalf("NewPublicationGateExecutor() error = %v", err)
	}
	return executor
}

func (fixture *publicationGateExecutorFixture) record(operation string) {
	fixture.base.record(operation)
}

func (fixture *publicationGateExecutorFixture) operations() []string {
	return fixture.base.operations()
}

func (fixture *publicationGateExecutorFixture) clearOperations() {
	fixture.base.clearOperations()
}

func (fixture *publicationGateExecutorFixture) setConversationBodies(bodies ...string) {
	fixture.t.Helper()
	if len(bodies) != len(fixture.store.snapshot.Conversation.Messages) {
		fixture.t.Fatalf(
			"conversation bodies = %d, want %d",
			len(bodies),
			len(fixture.store.snapshot.Conversation.Messages),
		)
	}
	fixture.store.mu.Lock()
	defer fixture.store.mu.Unlock()
	for index, body := range bodies {
		fixture.store.snapshot.Conversation.Messages[index].Content = body
		fixture.snapshot.Conversation.Messages[index].Content = body
	}
}

func (fixture *publicationGateExecutorFixture) appendCurrentConversation(
	body string,
	digest string,
) {
	fixture.store.mu.Lock()
	defer fixture.store.mu.Unlock()
	appendPublicationGateExecutorConversation(&fixture.store.snapshot, body, digest)
}

func (fixture *publicationGateExecutorFixture) activeSubject(
	t *testing.T,
	snapshot eventing.PRDevelopmentPublicationGateContextSnapshot,
) ([]byte, string) {
	t.Helper()
	converted, err := publicationGateAttentionSnapshot(snapshot)
	if err != nil {
		t.Fatalf("publicationGateAttentionSnapshot() error = %v", err)
	}
	loader, err := newAttentionContextLoader(
		publicationAttentionContextStore{cases: fixture.store},
		fixture.evidence,
		func() (AttentionReviewWorkspace, error) { return fixture.workspace, nil },
		nil,
	)
	if err != nil {
		t.Fatalf("newAttentionContextLoader() error = %v", err)
	}
	loaded, err := loader.loadForReviewOutcome(
		t.Context(),
		converted,
		eventing.PRDevelopmentLedgerReviewPassed,
	)
	if err != nil {
		t.Fatalf("loadForReviewOutcome() error = %v", err)
	}
	modelCanonical, err := json.Marshal(loaded.subject)
	if err != nil {
		t.Fatalf("json.Marshal(model subject) error = %v", err)
	}
	_, canonical, revision, err := buildPublicationActiveSubject(
		snapshot,
		fixture.policy,
		modelCanonical,
	)
	if err != nil {
		t.Fatalf("buildPublicationActiveSubject() error = %v", err)
	}
	return canonical, revision
}

type publicationGateExecutionStoreFake struct {
	*publicationGateStoreFake
	subjectPinMu          sync.Mutex
	links                 map[eventing.PRDevelopmentPublicationDecisionKey]eventing.PRDevelopmentPublicationDecisionRunLink
	findBarrier           *publicationGateExecutorBarrier
	admitBarrier          *publicationGateExecutorBarrier
	findErr               error
	admitErr              error
	admitResult           *eventing.PRDevelopmentPublicationDecisionRunLink
	uncertainAfterCreate  bool
	findCalls             int
	admitCalls            int
	createCalls           int
	pinnedSnapshotCalls   int
	caseCalls             int
	pinnedSnapshot        *eventing.PRDevelopmentPublicationGateContextSnapshot
	subjectWinnerSnapshot *eventing.PRDevelopmentPublicationGateContextSnapshot
}

func (store *publicationGateExecutionStoreFake) AuthenticateClaimedPRDevelopmentPublicationGate(
	ctx context.Context,
	publicationID string,
	claimToken string,
	claimEpoch int64,
) (eventing.PRDevelopmentPublicationGateAuthentication, error) {
	store.subjectPinMu.Lock()
	defer store.subjectPinMu.Unlock()
	return store.publicationGateStoreFake.AuthenticateClaimedPRDevelopmentPublicationGate(
		ctx,
		publicationID,
		claimToken,
		claimEpoch,
	)
}

func (store *publicationGateExecutionStoreFake) PinPRDevelopmentPublicationSubject(
	ctx context.Context,
	input eventing.PRDevelopmentPublicationSubjectPin,
) (eventing.PRDevelopmentPublication, bool, error) {
	store.subjectPinMu.Lock()
	defer store.subjectPinMu.Unlock()
	pinned, changed, err := store.publicationGateStoreFake.PinPRDevelopmentPublicationSubject(
		ctx,
		input,
	)
	if err != nil {
		return pinned, changed, err
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if store.subjectWinnerSnapshot != nil {
		store.snapshot = clonePublicationGateExecutorSnapshot(*store.subjectWinnerSnapshot)
	}
	snapshot := clonePublicationGateExecutorSnapshot(store.snapshot)
	snapshot.Publication = redactPublicationGateClaimFake(store.publication)
	store.pinnedSnapshot = &snapshot
	return pinned, changed, nil
}

func (store *publicationGateExecutionStoreFake) GetClaimedPRDevelopmentPublicationPinnedGateContextSnapshot(
	ctx context.Context,
	publicationID string,
	claimToken string,
	claimEpoch int64,
	anchor eventing.PRDevelopmentPublicationGateContextAnchor,
) (eventing.PRDevelopmentPublicationGateContextSnapshot, error) {
	store.subjectPinMu.Lock()
	defer store.subjectPinMu.Unlock()
	store.fixture.record("pinned-snapshot")
	if err := ctx.Err(); err != nil {
		return eventing.PRDevelopmentPublicationGateContextSnapshot{}, err
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	store.pinnedSnapshotCalls++
	if err := store.requireClaim(publicationID, claimToken, claimEpoch); err != nil {
		return eventing.PRDevelopmentPublicationGateContextSnapshot{}, err
	}
	if store.pinnedSnapshot == nil ||
		anchor.SubjectRevision != store.publication.SubjectRevision ||
		anchor.ConversationVersion != store.pinnedSnapshot.Conversation.Version ||
		anchor.TranscriptDigest != store.pinnedSnapshot.TranscriptDigest {
		return eventing.PRDevelopmentPublicationGateContextSnapshot{},
			eventing.ErrPRDevelopmentPublicationConflict
	}
	snapshot := clonePublicationGateExecutorSnapshot(*store.pinnedSnapshot)
	snapshot.Publication = redactPublicationGateClaimFake(store.publication)
	return snapshot, nil
}

func (store *publicationGateExecutionStoreFake) GetPRDevelopmentCase(
	ctx context.Context,
	caseID string,
) (eventing.PRDevelopmentCase, error) {
	store.fixture.record("case")
	if err := ctx.Err(); err != nil {
		return eventing.PRDevelopmentCase{}, err
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	store.caseCalls++
	if caseID != store.snapshot.Case.ID {
		return eventing.PRDevelopmentCase{}, eventing.ErrNotFound
	}
	return store.snapshot.Case, nil
}

func (store *publicationGateExecutionStoreFake) GetPRDevelopmentPublicationDecisionRun(
	ctx context.Context,
	key eventing.PRDevelopmentPublicationDecisionKey,
) (eventing.PRDevelopmentPublicationDecisionRunLink, error) {
	store.fixture.record("find-run")
	if err := ctx.Err(); err != nil {
		return eventing.PRDevelopmentPublicationDecisionRunLink{}, err
	}
	store.mu.Lock()
	store.findCalls++
	findErr := store.findErr
	barrier := store.findBarrier
	link, ok := store.links[key]
	store.mu.Unlock()
	if barrier != nil {
		if err := barrier.Wait(ctx); err != nil {
			return eventing.PRDevelopmentPublicationDecisionRunLink{}, err
		}
	}
	if findErr != nil {
		return eventing.PRDevelopmentPublicationDecisionRunLink{}, findErr
	}
	if !ok {
		return eventing.PRDevelopmentPublicationDecisionRunLink{}, eventing.ErrNotFound
	}
	return link, nil
}

func (store *publicationGateExecutionStoreFake) AdmitPRDevelopmentPublicationDecisionRun(
	ctx context.Context,
	admission eventing.PRDevelopmentPublicationDecisionRunAdmission,
	create func(context.Context) error,
) (eventing.PRDevelopmentPublicationDecisionRunLink, bool, error) {
	store.fixture.record("admit")
	if err := ctx.Err(); err != nil {
		return eventing.PRDevelopmentPublicationDecisionRunLink{}, false, err
	}
	if create == nil {
		return eventing.PRDevelopmentPublicationDecisionRunLink{}, false,
			workflows.ErrRunAdmissionConflict
	}
	store.mu.Lock()
	store.admitCalls++
	barrier := store.admitBarrier
	store.mu.Unlock()
	if barrier != nil {
		if err := barrier.Wait(ctx); err != nil {
			return eventing.PRDevelopmentPublicationDecisionRunLink{}, false, err
		}
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if admission.ClaimToken != store.publication.ClaimToken ||
		admission.ClaimEpoch != store.publication.ClaimEpoch ||
		admission.Key != publicationDecisionKey(store.publication) {
		return eventing.PRDevelopmentPublicationDecisionRunLink{}, false,
			eventing.ErrPRDevelopmentPublicationConflict
	}
	if link, ok := store.links[admission.Key]; ok {
		return link, true, nil
	}
	if store.admitResult != nil {
		link := *store.admitResult
		return link, true, nil
	}
	if store.admitErr != nil {
		return eventing.PRDevelopmentPublicationDecisionRunLink{}, false, store.admitErr
	}
	store.createCalls++
	if err := create(ctx); err != nil {
		return eventing.PRDevelopmentPublicationDecisionRunLink{}, false, err
	}
	if store.uncertainAfterCreate {
		return eventing.PRDevelopmentPublicationDecisionRunLink{}, false,
			eventing.ErrPRDevelopmentPublicationAdmissionUncertain
	}
	link := eventing.PRDevelopmentPublicationDecisionRunLink{
		Key: admission.Key, RunID: admission.RunID,
	}
	store.links[admission.Key] = link
	store.publication.DecisionRunID = admission.RunID
	return link, false, nil
}

type publicationGateExecutorBarrier struct {
	mu      sync.Mutex
	want    int
	arrived int
	release chan struct{}
}

func newPublicationGateExecutorBarrier(want int) *publicationGateExecutorBarrier {
	return &publicationGateExecutorBarrier{want: want, release: make(chan struct{})}
}

func (barrier *publicationGateExecutorBarrier) Wait(ctx context.Context) error {
	barrier.mu.Lock()
	barrier.arrived++
	if barrier.arrived == barrier.want {
		close(barrier.release)
	}
	release := barrier.release
	barrier.mu.Unlock()
	select {
	case <-release:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

type publicationGateConcurrentAgent struct {
	mu      sync.Mutex
	calls   int
	started chan struct{}
	release chan struct{}
	once    sync.Once
}

func newPublicationGateConcurrentAgent() *publicationGateConcurrentAgent {
	return &publicationGateConcurrentAgent{
		started: make(chan struct{}),
		release: make(chan struct{}),
	}
}

func (agent *publicationGateConcurrentAgent) RunAgent(
	ctx context.Context,
	request workflows.AgentRequest,
) (map[string]any, error) {
	agent.mu.Lock()
	agent.calls++
	agent.mu.Unlock()
	agent.once.Do(func() { close(agent.started) })
	select {
	case <-agent.release:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	response := `{"ask_user":false,"reason":"the frozen candidate is safe","questions":[]}`
	structured := workflows.ValidateAgentStructuredOutput(response, request.Output)
	if !structured.Valid {
		return nil, errors.New("invalid concurrent gate agent output")
	}
	return map[string]any{
		"text":             response,
		"structured":       structured.Structured,
		"structured_json":  structured.RawJSON,
		"structured_valid": true,
	}, nil
}

func (agent *publicationGateConcurrentAgent) callsValue() int {
	agent.mu.Lock()
	defer agent.mu.Unlock()
	return agent.calls
}

type publicationGateExecutorRunStoreFault struct {
	workflows.RunStore
	getErr error
}

func (store *publicationGateExecutorRunStoreFault) GetRun(
	context.Context,
	string,
) (*workflows.Run, error) {
	return nil, store.getErr
}

type publicationGateExecutorEvidenceFake struct {
	fixture        *publicationGateExecutorFixture
	plan           localci.Plan
	run            localci.Execution
	planCalls      int
	executionCalls int
}

func (evidence *publicationGateExecutorEvidenceFake) GetPlan(
	ctx context.Context,
	digest string,
) (localci.Plan, bool, error) {
	evidence.fixture.record("ci-plan")
	evidence.fixture.base.mu.Lock()
	evidence.planCalls++
	plan := evidence.plan
	evidence.fixture.base.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return localci.Plan{}, false, err
	}
	if digest != plan.Digest {
		return localci.Plan{}, false, nil
	}
	return plan, true, nil
}

func (evidence *publicationGateExecutorEvidenceFake) GetExecution(
	ctx context.Context,
	digest string,
) (localci.Execution, bool, error) {
	evidence.fixture.record("ci-execution")
	evidence.fixture.base.mu.Lock()
	evidence.executionCalls++
	run := evidence.run
	evidence.fixture.base.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return localci.Execution{}, false, err
	}
	if digest != run.Digest {
		return localci.Execution{}, false, nil
	}
	return run, true, nil
}

type publicationGateExecutorWorkspaceFake struct {
	fixture  *publicationGateExecutorFixture
	snapshot gitworkspace.PinnedLineReviewSnapshot
	calls    int
}

type publicationGateExecutorProviderHook struct {
	inner PublicationProviderObserver
	after func()
}

func (hook publicationGateExecutorProviderHook) ObservePublication(
	ctx context.Context,
	stored eventing.PRDevelopmentCase,
	expected eventing.PRDevelopmentThreadIdentity,
) (TimedPublicationProviderObservation, error) {
	observation, err := hook.inner.ObservePublication(ctx, stored, expected)
	if err == nil && hook.after != nil {
		hook.after()
	}
	return observation, err
}

func clonePublicationGateExecutorSnapshot(
	snapshot eventing.PRDevelopmentPublicationGateContextSnapshot,
) eventing.PRDevelopmentPublicationGateContextSnapshot {
	snapshot.Publication = clonePublicationGatePublication(snapshot.Publication)
	snapshot.Thread.Cases = append(
		[]eventing.PRDevelopmentThreadCaseLink(nil),
		snapshot.Thread.Cases...,
	)
	snapshot.Conversation.Messages = append(
		[]eventing.PRDevelopmentMessage(nil),
		snapshot.Conversation.Messages...,
	)
	snapshot.OwnerSession.Attempts = append(
		[]eventing.PRDevelopmentRepairAttempt(nil),
		snapshot.OwnerSession.Attempts...,
	)
	snapshot.Ledger.Entries = append(
		[]eventing.PRDevelopmentLedgerEntry(nil),
		snapshot.Ledger.Entries...,
	)
	for index := range snapshot.Ledger.Entries {
		snapshot.Ledger.Entries[index].Findings = append(
			[]eventing.PRDevelopmentLedgerReviewFinding(nil),
			snapshot.Ledger.Entries[index].Findings...,
		)
	}
	snapshot.Ledger.Checkpoints = append(
		[]eventing.PRDevelopmentLedgerCheckpoint(nil),
		snapshot.Ledger.Checkpoints...,
	)
	snapshot.ReviewEntry.Findings = append(
		[]eventing.PRDevelopmentLedgerReviewFinding(nil),
		snapshot.ReviewEntry.Findings...,
	)
	return snapshot
}

func appendPublicationGateExecutorConversation(
	snapshot *eventing.PRDevelopmentPublicationGateContextSnapshot,
	body string,
	digest string,
) {
	ordinal := len(snapshot.Conversation.Messages)
	snapshot.Conversation.Messages = append(
		snapshot.Conversation.Messages,
		eventing.PRDevelopmentMessage{
			ID:        "pdm_90909090909090909090909090909090",
			CaseID:    snapshot.Conversation.CaseID,
			Ordinal:   ordinal,
			Role:      eventing.PRDevelopmentMessageUser,
			Content:   body,
			CreatedAt: time.Date(2026, 8, 11, 12, 1, 0, 0, time.UTC),
		},
	)
	snapshot.Conversation.Version = int64(len(snapshot.Conversation.Messages))
	snapshot.TranscriptDigest = digest
}

func (workspace *publicationGateExecutorWorkspaceFake) SnapshotPinnedLineReview(
	ctx context.Context,
	request gitworkspace.PinnedLineReviewRequest,
) (gitworkspace.PinnedLineReviewSnapshot, error) {
	workspace.fixture.record("git-snapshot")
	workspace.fixture.base.mu.Lock()
	workspace.calls++
	workspace.fixture.base.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return gitworkspace.PinnedLineReviewSnapshot{}, err
	}
	want := gitworkspace.PinnedLineReviewRequest{
		LineID:          workspace.fixture.snapshot.Fence.LineID,
		ExpectedVersion: workspace.fixture.snapshot.Fence.LineVersion,
		ExpectedBase:    workspace.fixture.snapshot.Fence.BaseCommit,
		ExpectedTip:     workspace.fixture.snapshot.Fence.TipCommit,
		ExpectedTree:    workspace.fixture.snapshot.Fence.Tree,
	}
	if request != want {
		return gitworkspace.PinnedLineReviewSnapshot{}, errors.New("unexpected pinned review request")
	}
	return workspace.snapshot, nil
}

var (
	_ PublicationGateExecutionStore = (*publicationGateExecutionStoreFake)(nil)
	_ AttentionEvidenceStore        = (*publicationGateExecutorEvidenceFake)(nil)
	_ AttentionReviewWorkspace      = (*publicationGateExecutorWorkspaceFake)(nil)
)
