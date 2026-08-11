package prdevelopment

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sipeed/picoclaw/pkg/attention"
	"github.com/sipeed/picoclaw/pkg/eventing"
	"github.com/sipeed/picoclaw/pkg/workflows"
)

func TestPublicationGateProcessorRejectsTypedNilCapabilities(t *testing.T) {
	t.Parallel()

	fixture := newPublicationGateProcessorFixture(t, nil)
	var nilStore *publicationGateStoreFake
	var nilPolicies *publicationGatePolicySourceFake
	var nilProvider *publicationGateProviderFake

	for _, test := range []struct {
		name   string
		config PublicationGateProcessorConfig
	}{
		{
			name: "store",
			config: PublicationGateProcessorConfig{
				Store: nilStore, Policies: fixture.policies, Provider: fixture.provider,
			},
		},
		{
			name: "policies",
			config: PublicationGateProcessorConfig{
				Store: fixture.store, Policies: nilPolicies, Provider: fixture.provider,
			},
		},
		{
			name: "provider",
			config: PublicationGateProcessorConfig{
				Store: fixture.store, Policies: fixture.policies, Provider: nilProvider,
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			processor, err := NewPublicationGateProcessor(test.config)
			if err == nil || processor != nil {
				t.Fatalf("NewPublicationGateProcessor() = (%#v, %v), want nil/error", processor, err)
			}
		})
	}
}

func TestPublicationGateProcessorZeroPoliciesPinInOrderAndBecomePushReady(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name  string
		gates []workflows.GateSpec
	}{
		{name: "empty"},
		{
			name: "ordered all zero",
			gates: []workflows.GateSpec{
				{ID: "off-first", Kind: workflows.GateZero},
				{ID: "off-second", Kind: workflows.GateZero},
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			fixture := newPublicationGateProcessorFixture(t, test.gates)
			processor := fixture.processor(t)

			result, err := processor.ProcessClaim(context.Background(), fixture.store.claim())
			if err != nil {
				t.Fatalf("ProcessClaim() error = %v", err)
			}
			if result.Disposition != PublicationGatePushReady ||
				result.Publication.Status != eventing.PRDevelopmentPublicationPushReady {
				t.Fatalf("ProcessClaim() result = %#v", result)
			}
			if got, want := fixture.operations(),
				[]string{
					"authenticate",
					"policy-source",
					"pin-policy",
					"snapshot",
					"pin-subject",
					"provider",
					"pin-provider",
					"push-ready",
				}; !reflect.DeepEqual(got, want) {
				t.Fatalf("operations = %v, want %v", got, want)
			}
			if fixture.policies.calls != 1 || fixture.provider.calls != 1 {
				t.Fatalf("policy/provider calls = %d/%d", fixture.policies.calls, fixture.provider.calls)
			}
			stored := fixture.store.current()
			if stored.PolicyRevision == "" || len(stored.PinnedPolicy) == 0 ||
				stored.SubjectRevision == "" || len(stored.PinnedSubject) == 0 ||
				stored.ProviderObservationHash == "" || stored.DecisionRunID != "" {
				t.Fatalf("stored zero-gate publication = %#v", stored)
			}
			assertCanonicalPublicationGateJSON(t, stored.PinnedPolicy)
			assertCanonicalPublicationGateJSON(t, stored.PinnedSubject)
		})
	}
}

func TestPublicationGateProcessorActivePolicyStopsAfterPolicyPin(t *testing.T) {
	t.Parallel()

	fixture := newPublicationGateProcessorFixture(t, []workflows.GateSpec{
		{ID: "identity-zero", Kind: workflows.GateZero},
		{
			ID:        "quality",
			Kind:      workflows.GateDeterministic,
			When:      "true",
			Title:     "Check local quality",
			Questions: []any{"May this exact candidate be published?"},
		},
	})
	processor := fixture.processor(t)

	claim := fixture.store.claim()
	result, err := processor.ProcessClaim(context.Background(), claim)
	if err != nil {
		t.Fatalf("ProcessClaim() error = %v", err)
	}
	if result.Disposition != PublicationGateRequiresExecution ||
		result.Publication.PolicyRevision == "" ||
		len(result.Publication.PinnedPolicy) == 0 {
		t.Fatalf("ProcessClaim() result = %#v", result)
	}
	pinned, decodeErr := attention.DecodePreparedPolicy(result.Publication.PinnedPolicy)
	if decodeErr != nil || len(pinned.EffectiveGates()) != 2 ||
		pinned.EffectiveGates()[0].Kind != workflows.GateZero ||
		pinned.EffectiveGates()[1].Kind != workflows.GateDeterministic || pinned.IsNoop() {
		t.Fatalf("mixed pinned policy = %#v, error=%v", pinned.EffectiveGates(), decodeErr)
	}
	stored := fixture.store.current()
	if stored.SubjectRevision != "" || len(stored.PinnedSubject) != 0 ||
		stored.ProviderObservationHash != "" || stored.Status != eventing.PRDevelopmentPublicationClaimed {
		t.Fatalf("active policy advanced past policy pin = %#v", stored)
	}
	if got, want := fixture.operations(), []string{
		"authenticate",
		"policy-source",
		"pin-policy",
	}; !reflect.DeepEqual(
		got,
		want,
	) {
		t.Fatalf("operations = %v, want %v", got, want)
	}
}

func TestPublicationGateProcessorActivePolicyReplaysCompleteDurableDecisionWithoutEffects(
	t *testing.T,
) {
	t.Parallel()

	fixture := newPublicationGateExecutorFixture(t, publicationGateExecutorPassingGates())
	first, err := fixture.executor(t).ExecuteClaim(t.Context(), fixture.claim)
	if err != nil || first.RunID == "" || first.Status != workflows.RunStatusSucceeded ||
		first.Existing {
		t.Fatalf("initial ExecuteClaim() = (%#v, %v), operations=%v", first, err, fixture.operations())
	}
	before := fixture.store.current()
	if before.SubjectRevision == "" || before.ProviderObservationHash == "" ||
		before.DecisionRunID != first.RunID ||
		before.Status != eventing.PRDevelopmentPublicationClaimed ||
		before.ClaimFrom != eventing.PRDevelopmentPublicationPending {
		t.Fatalf("complete durable active decision = %#v, run=%q", before, first.RunID)
	}
	key := publicationDecisionKey(before)
	fixture.store.mu.Lock()
	linkBefore, linked := fixture.store.links[key]
	fixture.store.mu.Unlock()
	if !linked || linkBefore.RunID != first.RunID {
		t.Fatalf("durable decision link = (%#v, %v), want run %q", linkBefore, linked, first.RunID)
	}

	policies := &publicationGatePolicySourceFake{
		fixture: fixture.base,
		err:     errors.New("active replay must not reconstruct policy"),
	}
	fixture.provider.err = errors.New("active replay must not observe provider")
	processor, err := NewPublicationGateProcessor(PublicationGateProcessorConfig{
		Store: fixture.store, Policies: policies, Provider: fixture.provider,
	})
	if err != nil {
		t.Fatalf("NewPublicationGateProcessor() error = %v", err)
	}
	fixture.clearOperations()

	replayed, err := processor.ProcessClaim(t.Context(), fixture.claim)
	if err != nil || replayed.Disposition != PublicationGateRequiresExecution {
		t.Fatalf("replayed ProcessClaim() = (%#v, %v), operations=%v", replayed, err, fixture.operations())
	}
	if replayed.Publication.DecisionRunID != first.RunID ||
		replayed.Publication.SubjectRevision != before.SubjectRevision ||
		replayed.Publication.ProviderObservationHash != before.ProviderObservationHash ||
		replayed.Publication.ClaimToken != "" {
		t.Fatalf("replayed durable active decision = %#v", replayed.Publication)
	}
	if got, want := fixture.operations(), []string{"authenticate"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("replay operations = %v, want %v", got, want)
	}
	if policies.calls != 0 || fixture.provider.callsValue() != 1 {
		t.Fatalf("replay reconstructed policy/provider: policy=%d provider=%d",
			policies.calls, fixture.provider.callsValue())
	}
	if after := fixture.store.current(); !reflect.DeepEqual(after, before) {
		t.Fatalf("processor replay transitioned durable state: before=%#v after=%#v", before, after)
	}
	fixture.store.mu.Lock()
	linkAfter, stillLinked := fixture.store.links[key]
	fixture.store.mu.Unlock()
	if !stillLinked || linkAfter != linkBefore {
		t.Fatalf("processor replay changed decision link: before=%#v after=%#v found=%v",
			linkBefore, linkAfter, stillLinked)
	}
}

func TestPublicationGateProcessorPinnedPolicyIgnoresLivePolicySource(t *testing.T) {
	t.Parallel()

	fixture := newPublicationGateProcessorFixture(t, nil)
	prepared, err := attention.PrepareSnapshot(attention.PolicySnapshot{
		Revision: "catalog-pinned", Global: []workflows.GateSpec{{
			ID: "off", Kind: workflows.GateZero,
		}},
	})
	if err != nil {
		t.Fatalf("PrepareSnapshot() error = %v", err)
	}
	fixture.store.publication.PolicyRevision = prepared.DecisionRevision()
	fixture.store.publication.PinnedPolicy = prepared.Canonical()
	fixture.store.publication.PinnedPolicyHash = strings.Repeat("1", 64)
	fixture.policies.err = errors.New("live policy must not be consulted")
	processor := fixture.processor(t)

	result, err := processor.ProcessClaim(context.Background(), fixture.store.claim())
	if err != nil || result.Disposition != PublicationGatePushReady {
		t.Fatalf("ProcessClaim() = (%#v, %v), current=%#v ops=%v", result, err,
			fixture.store.current(), fixture.operations())
	}
	if fixture.policies.calls != 0 {
		t.Fatalf("live policy source calls = %d, want zero", fixture.policies.calls)
	}
	if containsString(fixture.operations(), "pin-policy") {
		t.Fatalf("pinned policy was rewritten: %v", fixture.operations())
	}
}

func TestPublicationGateProcessorAuthenticatesPinnedPolicyBeforeFastPath(t *testing.T) {
	t.Parallel()

	fixture := newPublicationGateProcessorFixture(t, []workflows.GateSpec{{
		ID: "off", Kind: workflows.GateZero,
	}})
	authoritative, err := attention.PrepareSnapshot(fixture.policies.snapshot)
	if err != nil {
		t.Fatalf("PrepareSnapshot(authoritative) error = %v", err)
	}
	fixture.store.publication.PolicyRevision = authoritative.DecisionRevision()
	fixture.store.publication.PinnedPolicy = authoritative.Canonical()
	fixture.store.publication.PinnedPolicyHash = strings.Repeat("1", 64)
	provided := fixture.store.claim()
	changed, err := attention.PrepareSnapshot(attention.PolicySnapshot{
		Revision: "catalog-caller-mutated",
		Global: []workflows.GateSpec{{
			ID: "changed", Kind: workflows.GateZero,
		}},
	})
	if err != nil {
		t.Fatalf("PrepareSnapshot(changed) error = %v", err)
	}
	provided.PolicyRevision = changed.DecisionRevision()
	provided.PinnedPolicy = changed.Canonical()
	provided.PinnedPolicyHash = strings.Repeat("2", 64)
	processor := fixture.processor(t)

	result, err := processor.ProcessClaim(context.Background(), provided)
	if !errors.Is(err, errPublicationGateCorrupt) || result.Disposition != "" ||
		fixture.policies.calls != 0 || fixture.provider.calls != 0 ||
		!reflect.DeepEqual(fixture.operations(), []string{"authenticate"}) {
		t.Fatalf("mutated pinned-policy fast path = (%#v, %v), ops=%v",
			result, err, fixture.operations())
	}
}

func TestPublicationGateProcessorAuthenticatesBeforeDecodingCallerPins(t *testing.T) {
	t.Parallel()

	fixture := newPublicationGateProcessorFixture(t, nil)
	provided := fixture.store.claim()
	provided.PinnedPolicy = []byte(`{"malformed"`)
	processor := fixture.processor(t)

	result, err := processor.ProcessClaim(context.Background(), provided)
	if !errors.Is(err, errPublicationGateCorrupt) || result.Disposition != "" ||
		!reflect.DeepEqual(fixture.operations(), []string{"authenticate"}) {
		t.Fatalf("malformed caller pin = (%#v, %v), ops=%v",
			result, err, fixture.operations())
	}
}

func TestPublicationGateProcessorZeroGateReplaysEveryDurableBoundary(t *testing.T) {
	t.Parallel()

	for _, boundary := range []string{
		"pin-policy", "pin-subject", "pin-provider", "push-ready",
	} {
		t.Run(boundary, func(t *testing.T) {
			t.Parallel()

			fixture := newPublicationGateProcessorFixture(t, []workflows.GateSpec{{
				ID: "off", Kind: workflows.GateZero,
			}})
			ambiguous := fmt.Errorf("ambiguous %s response", boundary)
			fixture.store.failAfter[boundary] = ambiguous
			processor := fixture.processor(t)

			first, err := processor.ProcessClaim(context.Background(), fixture.store.claim())
			if err != nil || first.Disposition != PublicationGatePushReady ||
				first.Publication.Status != eventing.PRDevelopmentPublicationPushReady {
				t.Fatalf("ambiguously applied ProcessClaim() = (%#v, %v), injected %v", first, err, ambiguous)
			}
			if boundary == "pin-policy" && fixture.policies.calls != 1 {
				t.Fatalf("policy source calls after recovered pin = %d, want 1 total", fixture.policies.calls)
			}
			if boundary == "pin-provider" && fixture.provider.calls != 1 {
				t.Fatalf("provider calls after recovered pin = %d, want 1 total", fixture.provider.calls)
			}
		})
	}
}

func TestPublicationGateProcessorConsumesDifferentValidConcurrentPolicyWinner(t *testing.T) {
	t.Parallel()

	fixture := newPublicationGateProcessorFixture(t, []workflows.GateSpec{{
		ID: "losing-zero", Kind: workflows.GateZero,
	}})
	winner, err := attention.PrepareSnapshot(attention.PolicySnapshot{
		Revision: "catalog-concurrent-winner",
		Global: []workflows.GateSpec{{
			ID:        "winning-active",
			Kind:      workflows.GateDeterministic,
			When:      "true",
			Title:     "Concurrent winner",
			Questions: []any{"Continue with the durable winning policy?"},
		}},
	})
	if err != nil {
		t.Fatalf("PrepareSnapshot(winner) error = %v", err)
	}
	fixture.store.policyWinner = &winner
	fixture.store.failAfter["pin-policy"] = eventing.ErrPRDevelopmentPublicationConflict
	processor := fixture.processor(t)

	result, err := processor.ProcessClaim(context.Background(), fixture.store.claim())
	if err != nil {
		t.Fatalf("ProcessClaim() error = %v", err)
	}
	if result.Disposition != PublicationGateRequiresExecution ||
		result.Publication.PolicyRevision != winner.DecisionRevision() ||
		!bytes.Equal(result.Publication.PinnedPolicy, winner.Canonical()) {
		t.Fatalf("concurrent policy result = %#v", result)
	}
	if fixture.store.policyPin.PolicyRevision == winner.DecisionRevision() ||
		fixture.policies.calls != 1 || fixture.provider.calls != 0 ||
		containsString(fixture.operations(), "pin-subject") ||
		containsString(fixture.operations(), "terminal") {
		t.Fatalf("policy winner did not converge safely: attempted=%#v ops=%v",
			fixture.store.policyPin, fixture.operations())
	}
}

func TestPublicationGateProcessorConsumesDifferentValidConversationSubjectWinner(t *testing.T) {
	t.Parallel()

	fixture := newPublicationGateProcessorFixture(t, []workflows.GateSpec{{
		ID: "off", Kind: workflows.GateZero,
	}})
	policy, err := attention.PrepareSnapshot(fixture.policies.snapshot)
	if err != nil {
		t.Fatalf("PrepareSnapshot() error = %v", err)
	}
	winnerSnapshot := fixture.store.snapshot
	winnerSnapshot.Conversation.Version++
	winnerSnapshot.TranscriptDigest = strings.Repeat("3", 64)
	_, canonical, revision, err := buildPublicationZeroSubject(winnerSnapshot, policy)
	if err != nil {
		t.Fatalf("buildPublicationZeroSubject(winner) error = %v", err)
	}
	fixture.store.subjectWinner = &publicationGateSubjectWinner{
		canonical:           canonical,
		revision:            revision,
		conversationVersion: winnerSnapshot.Conversation.Version,
		transcriptDigest:    winnerSnapshot.TranscriptDigest,
	}
	fixture.store.failAfter["pin-subject"] = eventing.ErrPRDevelopmentPublicationConflict
	processor := fixture.processor(t)

	result, err := processor.ProcessClaim(context.Background(), fixture.store.claim())
	if err != nil || result.Disposition != PublicationGatePushReady {
		t.Fatalf("ProcessClaim() = (%#v, %v)", result, err)
	}
	if result.Publication.SubjectRevision != revision ||
		!bytes.Equal(result.Publication.PinnedSubject, canonical) ||
		fixture.store.subjectPin.ExpectedConversationVersion == winnerSnapshot.Conversation.Version ||
		fixture.store.subjectPin.ExpectedTranscriptDigest == winnerSnapshot.TranscriptDigest {
		t.Fatalf("concurrent subject did not win: attempted=%#v result=%#v",
			fixture.store.subjectPin, result.Publication)
	}
	decoded, found, decodeErr := decodePublicationZeroSubject(result.Publication, policy)
	if decodeErr != nil || !found || decoded.ConversationVersion != winnerSnapshot.Conversation.Version ||
		decoded.TranscriptDigest != winnerSnapshot.TranscriptDigest ||
		containsString(fixture.operations(), "terminal") {
		t.Fatalf("winning subject validation = (%#v, %t, %v), ops=%v",
			decoded, found, decodeErr, fixture.operations())
	}
}

func TestPublicationGateProcessorConsumesSameFactsConcurrentProviderTimestamp(t *testing.T) {
	t.Parallel()

	fixture := newPublicationGateProcessorFixture(t, []workflows.GateSpec{{
		ID: "off", Kind: workflows.GateZero,
	}})
	winner := fixture.observed
	winner.ObservedAt = winner.ObservedAt.Add(time.Second)
	fixture.store.providerWinner = &winner
	fixture.store.failAfter["pin-provider"] = eventing.ErrPRDevelopmentPublicationConflict
	processor := fixture.processor(t)

	claim := fixture.store.claim()
	result, err := processor.ProcessClaim(context.Background(), claim)
	if err != nil || result.Disposition != PublicationGatePushReady {
		t.Fatalf("ProcessClaim() = (%#v, %v), current=%#v ops=%v",
			result, err, fixture.store.current(), fixture.operations())
	}
	if fixture.store.providerPin.ObservedAt.Equal(winner.ObservedAt) ||
		result.Publication.ProviderPinnedAt == nil ||
		!result.Publication.ProviderPinnedAt.Equal(winner.ObservedAt) ||
		!reflect.DeepEqual(result.Publication.ProviderObservation, winner.Observation) ||
		containsString(fixture.operations(), "terminal") {
		t.Fatalf("concurrent provider winner = %#v, attempted=%#v ops=%v",
			result.Publication, fixture.store.providerPin, fixture.operations())
	}
}

func TestPublicationGateProcessorLocalCorruptionDoesNotTerminalize(t *testing.T) {
	t.Parallel()

	fixture := newPublicationGateProcessorFixture(t, []workflows.GateSpec{{
		ID: "off", Kind: workflows.GateZero,
	}})
	fixture.store.responseMutators["pin-policy"] = func(
		publication *eventing.PRDevelopmentPublication,
	) {
		publication.ClaimOwner = "inconsistent-local-response"
	}
	processor := fixture.processor(t)

	result, err := processor.ProcessClaim(context.Background(), fixture.store.claim())
	if !errors.Is(err, errPublicationGateCorrupt) || result.Disposition != "" ||
		!reflect.DeepEqual(result.Publication, eventing.PRDevelopmentPublication{}) {
		t.Fatalf("ProcessClaim() = (%#v, %v), want zero/corrupt error", result, err)
	}
	stored := fixture.store.current()
	if stored.Status != eventing.PRDevelopmentPublicationClaimed ||
		stored.CompletedAt != nil || stored.LastErrorCode != "" ||
		containsString(fixture.operations(), "terminal") {
		t.Fatalf("processor-local corruption terminalized durable work: %#v ops=%v",
			stored, fixture.operations())
	}
}

func TestPublicationGateProcessorZeroSubjectIsCanonicalConversationFenced(t *testing.T) {
	t.Parallel()

	fixture := newPublicationGateProcessorFixture(t, []workflows.GateSpec{{
		ID: "off", Kind: workflows.GateZero,
	}})
	processor := fixture.processor(t)
	result, err := processor.ProcessClaim(context.Background(), fixture.store.claim())
	if err != nil || result.Disposition != PublicationGatePushReady {
		t.Fatalf("ProcessClaim() = (%#v, %v)", result, err)
	}
	pin := fixture.store.subjectPin
	if pin.ExpectedConversationVersion != fixture.store.snapshot.Conversation.Version ||
		pin.ExpectedTranscriptDigest != fixture.store.snapshot.TranscriptDigest {
		t.Fatalf("subject conversation fence = (%d, %q), want (%d, %q)",
			pin.ExpectedConversationVersion, pin.ExpectedTranscriptDigest,
			fixture.store.snapshot.Conversation.Version, fixture.store.snapshot.TranscriptDigest)
	}
	if pin.PolicyRevision != fixture.store.policyPin.PolicyRevision ||
		!strings.HasPrefix(pin.SubjectRevision, "sha256:") {
		t.Fatalf("subject pin = %#v", pin)
	}
	assertCanonicalPublicationGateJSON(t, pin.PinnedSubject)
	policy, found, decodeErr := decodePublicationGatePolicy(result.Publication)
	if decodeErr != nil || !found {
		t.Fatalf("decodePublicationGatePolicy() = (%#v, %t, %v)", policy, found, decodeErr)
	}
	subject, found, decodeErr := decodePublicationZeroSubject(result.Publication, policy)
	if decodeErr != nil || !found || subject.Repository != fixture.store.snapshot.Case.Repository ||
		subject.SelectedOrdinal != fixture.store.snapshot.SelectedOrdinal ||
		subject.ThreadCasesDigest != fixture.store.snapshot.Thread.CasesDigest ||
		subject.ConversationVersion != fixture.store.snapshot.Conversation.Version ||
		subject.TranscriptDigest != fixture.store.snapshot.TranscriptDigest ||
		subject.LedgerEntriesDigest != fixture.store.snapshot.Ledger.EntriesDigest ||
		subject.LedgerCheckpointsDigest != fixture.store.snapshot.Ledger.CheckpointsDigest {
		t.Fatalf("zero subject is not the exact snapshot projection: (%#v, %t, %v)",
			subject, found, decodeErr)
	}

	// Later chat may change the current snapshot, but exact durable subject replay
	// must not rebuild or repin the historical decision.
	before := append([]byte(nil), fixture.store.publication.PinnedSubject...)
	fixture.store.snapshot.Conversation.Version++
	fixture.store.snapshot.TranscriptDigest = strings.Repeat("f", 64)
	fixture.store.restoreInitialClaim()
	fixture.clearOperations()
	result, err = processor.ProcessClaim(context.Background(), fixture.store.claim())
	if err != nil || result.Disposition != PublicationGatePushReady {
		t.Fatalf("post-chat replay ProcessClaim() = (%#v, %v)", result, err)
	}
	if !bytes.Equal(before, fixture.store.publication.PinnedSubject) ||
		containsString(fixture.operations(), "pin-subject") {
		t.Fatalf("post-chat replay rebuilt subject: ops=%v", fixture.operations())
	}
}

func TestPublicationGateProcessorProviderFollowsSubjectAndExistingPinSkipsRead(t *testing.T) {
	t.Parallel()

	fixture := newPublicationGateProcessorFixture(t, []workflows.GateSpec{{
		ID: "off", Kind: workflows.GateZero,
	}})
	processor := fixture.processor(t)
	_, err := processor.ProcessClaim(context.Background(), fixture.store.claim())
	if err != nil {
		t.Fatalf("ProcessClaim() error = %v", err)
	}
	ops := fixture.operations()
	if indexOfString(ops, "pin-subject") >= indexOfString(ops, "provider") ||
		indexOfString(ops, "provider") >= indexOfString(ops, "pin-provider") {
		t.Fatalf("provider ordering = %v", ops)
	}

	fixture.store.restoreInitialClaim()
	fixture.clearOperations()
	beforeCalls := fixture.provider.calls
	result, err := processor.ProcessClaim(context.Background(), fixture.store.claim())
	if err != nil || result.Disposition != PublicationGatePushReady {
		t.Fatalf("provider replay ProcessClaim() = (%#v, %v)", result, err)
	}
	if fixture.provider.calls != beforeCalls || containsString(fixture.operations(), "pin-provider") {
		t.Fatalf("existing provider pin caused read/write: calls=%d/%d ops=%v",
			fixture.provider.calls, beforeCalls, fixture.operations())
	}
}

func TestPublicationGateProcessorRejectsInvalidProgressiveStatesWithoutEffects(t *testing.T) {
	t.Parallel()

	prepared, err := attention.PrepareSnapshot(attention.PolicySnapshot{
		Revision: "catalog-valid", Global: []workflows.GateSpec{{
			ID: "off", Kind: workflows.GateZero,
		}},
	})
	if err != nil {
		t.Fatalf("PrepareSnapshot() error = %v", err)
	}
	validSubject := json.RawMessage(`{"format":"pr-development-publication-zero-gate-subject/v1"}`)
	validProvider := eventing.PRDevelopmentPublicationProviderObservation{
		Repository: "acme/widgets", PullNumber: 17, HeadRepository: "acme/widgets",
		HeadRef: "refs/heads/feature", HeadSHA: strings.Repeat("a", 40),
		HeadCloneURL:       "https://github.com/acme/widgets.git",
		CurrentReviewState: eventing.PRDevelopmentReviewApproved,
		ReviewDigest:       strings.Repeat("b", 64),
	}
	now := time.Date(2026, 8, 10, 12, 0, 0, 0, time.UTC)
	for _, test := range []struct {
		name   string
		mutate func(*eventing.PRDevelopmentPublication)
	}{
		{name: "policy bytes without revision", mutate: func(p *eventing.PRDevelopmentPublication) {
			p.PinnedPolicy = prepared.Canonical()
			p.PinnedPolicyHash = strings.Repeat("1", 64)
		}},
		{name: "subject without policy", mutate: func(p *eventing.PRDevelopmentPublication) {
			p.SubjectRevision = "sha256:" + strings.Repeat("c", 64)
			p.PinnedSubject = validSubject
			p.PinnedSubjectHash = strings.Repeat("2", 64)
		}},
		{name: "provider without subject", mutate: func(p *eventing.PRDevelopmentPublication) {
			p.PolicyRevision = prepared.DecisionRevision()
			p.PinnedPolicy = prepared.Canonical()
			p.PinnedPolicyHash = strings.Repeat("1", 64)
			p.ProviderObservation = validProvider
			p.ProviderObservationJSON = []byte(`{"provider":"pinned"}`)
			p.ProviderObservationHash = strings.Repeat("d", 64)
			p.ProviderPinnedAt = &now
			p.ProviderObservedAt = &now
		}},
		{name: "decision run on zero gate", mutate: func(p *eventing.PRDevelopmentPublication) {
			p.PolicyRevision = prepared.DecisionRevision()
			p.PinnedPolicy = prepared.Canonical()
			p.PinnedPolicyHash = strings.Repeat("1", 64)
			p.DecisionRunID = "run_must_not_exist"
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			fixture := newPublicationGateProcessorFixture(t, nil)
			test.mutate(&fixture.store.publication)
			processor := fixture.processor(t)
			result, processErr := processor.ProcessClaim(context.Background(), fixture.store.claim())
			if processErr == nil && result.Disposition != PublicationGateTerminal {
				t.Fatalf("ProcessClaim() = (%#v, nil), want terminal or error", result)
			}
			if fixture.policies.calls != 0 || fixture.provider.calls != 0 {
				t.Fatalf("invalid state invoked policy/provider = %d/%d",
					fixture.policies.calls, fixture.provider.calls)
			}
		})
	}
}

func TestPublicationGateProcessorPropagatesStaleAndCancellationAsTransient(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name string
		err  error
	}{
		{name: "stale lease", err: eventing.ErrStaleLease},
		{name: "canceled", err: context.Canceled},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			fixture := newPublicationGateProcessorFixture(t, nil)
			fixture.store.snapshotErr = test.err
			processor := fixture.processor(t)

			result, err := processor.ProcessClaim(context.Background(), fixture.store.claim())
			if !errors.Is(err, test.err) || result.Disposition != "" ||
				!reflect.DeepEqual(result.Publication, eventing.PRDevelopmentPublication{}) {
				t.Fatalf("ProcessClaim() = (%#v, %v), want zero/%v", result, err, test.err)
			}
			if fixture.store.publication.Status != eventing.PRDevelopmentPublicationClaimed ||
				containsString(fixture.operations(), "terminal") {
				t.Fatalf("transient failure terminalized publication: %#v ops=%v",
					fixture.store.publication, fixture.operations())
			}
		})
	}
}

func TestPublicationGateProcessorProviderDriftIsTerminalBeforePush(t *testing.T) {
	t.Parallel()

	fixture := newPublicationGateProcessorFixture(t, []workflows.GateSpec{{
		ID: "off", Kind: workflows.GateZero,
	}})
	fixture.provider.observation.Observation.HeadSHA = strings.Repeat("f", 40)
	processor := fixture.processor(t)

	result, err := processor.ProcessClaim(context.Background(), fixture.store.claim())
	if err != nil {
		t.Fatalf("ProcessClaim() error = %v", err)
	}
	if result.Disposition != PublicationGateTerminal ||
		result.Publication.Status != eventing.PRDevelopmentPublicationConflict ||
		result.Publication.LastErrorCode != eventing.PRDevelopmentPublicationErrorProviderChanged ||
		result.Publication.CompletedAt == nil {
		t.Fatalf("provider-drift result = %#v", result)
	}
	if result.Publication.EffectStartedAt != nil || result.Publication.PushRequestHash != "" ||
		!containsString(fixture.operations(), "pin-provider") ||
		containsString(fixture.operations(), "push-ready") {
		t.Fatalf("provider drift crossed publication boundary: %#v ops=%v",
			result.Publication, fixture.operations())
	}
}

func TestPublicationGateProcessorMalformedProviderOutputIsTransient(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name   string
		mutate func(*TimedPublicationProviderObservation)
	}{
		{name: "zero timestamp", mutate: func(observation *TimedPublicationProviderObservation) {
			observation.ObservedAt = time.Time{}
		}},
		{name: "malformed observation", mutate: func(observation *TimedPublicationProviderObservation) {
			observation.Observation.HeadSHA = "malformed"
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			fixture := newPublicationGateProcessorFixture(t, []workflows.GateSpec{{
				ID: "off", Kind: workflows.GateZero,
			}})
			test.mutate(&fixture.provider.observation)
			processor := fixture.processor(t)

			result, err := processor.ProcessClaim(context.Background(), fixture.store.claim())
			if err == nil || result.Disposition != "" ||
				fixture.store.current().Status != eventing.PRDevelopmentPublicationClaimed ||
				containsString(fixture.operations(), "terminal") ||
				containsString(fixture.operations(), "push-ready") {
				t.Fatalf("malformed provider output = (%#v, %v), current=%#v ops=%v",
					result, err, fixture.store.current(), fixture.operations())
			}
		})
	}
}

func TestPublicationGateProcessorCapabilitySentinelCollisionsAreTransient(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name      string
		configure func(*publicationGateProcessorFixture)
	}{
		{name: "policy source", configure: func(fixture *publicationGateProcessorFixture) {
			fixture.policies.err = eventing.ErrPRDevelopmentPublicationSuperseded
		}},
		{name: "provider observer", configure: func(fixture *publicationGateProcessorFixture) {
			fixture.provider.err = eventing.ErrPRDevelopmentPublicationSuperseded
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			fixture := newPublicationGateProcessorFixture(t, []workflows.GateSpec{{
				ID: "off", Kind: workflows.GateZero,
			}})
			test.configure(fixture)
			processor := fixture.processor(t)

			result, err := processor.ProcessClaim(context.Background(), fixture.store.claim())
			if !errors.Is(err, eventing.ErrPRDevelopmentPublicationSuperseded) ||
				result.Disposition != "" ||
				fixture.store.current().Status != eventing.PRDevelopmentPublicationClaimed ||
				containsString(fixture.operations(), "terminal") {
				t.Fatalf("capability sentinel collision = (%#v, %v), current=%#v ops=%v",
					result, err, fixture.store.current(), fixture.operations())
			}
		})
	}
}

func TestPublicationGateProcessorResultsAndCapabilitiesAreJSONPrivate(t *testing.T) {
	t.Parallel()

	fixture := newPublicationGateProcessorFixture(t, nil)
	values := []any{
		PublicationGateProcessResult{
			Disposition: PublicationGatePushReady,
			Publication: fixture.store.publication,
		},
		PublicationGateProcessorConfig{
			Store: fixture.store, Policies: fixture.policies, Provider: fixture.provider,
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

type publicationGateProcessorFixture struct {
	t        *testing.T
	mu       sync.Mutex
	ops      []string
	store    *publicationGateStoreFake
	policies *publicationGatePolicySourceFake
	provider *publicationGateProviderFake
	observed TimedPublicationProviderObservation
}

func newPublicationGateProcessorFixture(
	t *testing.T,
	gates []workflows.GateSpec,
) *publicationGateProcessorFixture {
	t.Helper()
	now := time.Date(2026, 8, 10, 12, 0, 0, 0, time.UTC)
	claimUntil := now.Add(5 * time.Minute)
	hash := func(char string) string { return strings.Repeat(char, 64) }
	oid := func(char string) string { return strings.Repeat(char, 40) }
	publication := eventing.PRDevelopmentPublication{
		ID:                     "pdpub_10101010101010101010101010101010",
		CaseID:                 "pdc_20202020202020202020202020202020",
		ThreadID:               "pdt_30303030303030303030303030303030",
		ControllerID:           "pctl_31313131313131313131313131313131",
		ControllerRevision:     7,
		OwnerSessionID:         "pds_32323232323232323232323232323232",
		ReviewLedgerEntryID:    "pdle_40404040404040404040404040404040",
		ReviewLedgerEntryKind:  eventing.PRDevelopmentLedgerReview,
		ReviewLedgerEntryHash:  hash("4"),
		AttemptLedgerEntryID:   "pdle_50505050505050505050505050505050",
		AttemptLedgerEntryKind: eventing.PRDevelopmentLedgerAttempt,
		AttemptLedgerEntryHash: hash("5"),
		AttemptID:              "pdr_60606060606060606060606060606060",
		FenceOrdinal:           0, FenceHash: hash("6"),
		OrchestrationPhase:       eventing.PRDevelopmentRepairOrchestrationCompleted,
		OrchestrationReceiptHash: hash("f"),
		SourceCloneURL:           "https://github.com/acme/widgets.git",
		SourceRef:                "refs/heads/feature", SourceCommit: oid("a"),
		SourceTree: oid("b"), TipCommit: oid("c"), Tree: oid("d"),
		WorkspaceID: "workspace-1", LineID: "pdln_70707070707070707070707070707070",
		LineVersion: 1, MutationEpoch: 1,
		ParkIntentID: "pdlnpark_80808080808080808080808080808080",
		BaseCommit:   oid("e"), CIStatus: eventing.PRDevelopmentCIPassed,
		CIPlanDigest: hash("7"), CIResultDigest: hash("8"),
		ReviewOutcome: eventing.PRDevelopmentLedgerReviewPassed,
		Status:        eventing.PRDevelopmentPublicationClaimed,
		ClaimFrom:     eventing.PRDevelopmentPublicationPending,
		ClaimOwner:    "publication-test", ClaimToken: hash("9"), ClaimEpoch: 1,
		ClaimUntil: &claimUntil, ClaimedAt: &now, Claims: 1,
		AvailableAt: now, CreatedAt: now, UpdatedAt: now,
	}
	identity := eventing.PRDevelopmentThreadIdentity{
		Provider: "github", ProviderOrigin: "https://github.com",
		PullAuthorID: "user-1", RepositoryID: "repo-1",
		PullRequestID: "pull-17", PullNumber: 17,
	}
	caseValue := eventing.PRDevelopmentCase{
		ID: publication.CaseID,
		PRDevelopmentCaptureInput: eventing.PRDevelopmentCaptureInput{
			Repository: "acme/widgets", PullNumber: 17,
			HeadRepository: "acme/widgets", HeadRef: publication.SourceRef,
			HeadSHA:            publication.SourceCommit,
			CurrentReviewState: eventing.PRDevelopmentReviewApproved,
			ReviewCommitSHA:    publication.SourceCommit,
		},
		CreatedAt: now, UpdatedAt: now,
	}
	fixture := &publicationGateProcessorFixture{t: t}
	fixture.store = &publicationGateStoreFake{
		fixture: fixture, publication: publication,
		failAfter:        make(map[string]error),
		responseMutators: make(map[string]func(*eventing.PRDevelopmentPublication)),
		snapshot: eventing.PRDevelopmentPublicationGateContextSnapshot{
			Publication:      publication,
			SelectedOrdinal:  0,
			TranscriptDigest: hash("a"),
			Case:             caseValue,
			Thread: eventing.PRDevelopmentThread{
				ID: publication.ThreadID, Kind: eventing.PRDevelopmentThreadProvider,
				Identity: identity, CaseCount: 1, CasesDigest: hash("c"),
				Cases: []eventing.PRDevelopmentThreadCaseLink{{
					CaseID: publication.CaseID, Ordinal: 0,
				}},
			},
			Conversation: eventing.PRDevelopmentConversation{
				CaseID: publication.CaseID, Version: 0,
			},
			OwnerSession: eventing.PRDevelopmentRepairSession{
				ID:             publication.OwnerSessionID,
				CaseID:         publication.CaseID,
				HeadRepository: caseValue.HeadRepository,
				HeadRef:        caseValue.HeadRef,
				HeadSHA:        publication.SourceCommit,
				CloneURL:       publication.SourceCloneURL,
				ReviewDigest:   "sha256:" + hash("b"),
			},
			Orchestration: eventing.PRDevelopmentRepairOrchestration{
				AttemptID:    publication.AttemptID,
				SessionID:    publication.OwnerSessionID,
				CaseID:       publication.CaseID,
				ThreadID:     publication.ThreadID,
				Phase:        publication.OrchestrationPhase,
				ReviewDigest: "sha256:" + hash("b"),
			},
			Ledger: eventing.PRDevelopmentLedger{
				EntriesDigest: hash("d"), CheckpointsDigest: hash("e"),
			},
			AttemptEntry: eventing.PRDevelopmentLedgerEntry{
				ID:        publication.AttemptLedgerEntryID,
				EntryHash: publication.AttemptLedgerEntryHash,
				Kind:      eventing.PRDevelopmentLedgerAttempt,
			},
			ReviewEntry: eventing.PRDevelopmentLedgerEntry{
				ID:            publication.ReviewLedgerEntryID,
				EntryHash:     publication.ReviewLedgerEntryHash,
				Kind:          eventing.PRDevelopmentLedgerReview,
				ReviewOutcome: eventing.PRDevelopmentLedgerReviewPassed,
			},
		},
	}
	fixture.policies = &publicationGatePolicySourceFake{
		fixture: fixture,
		snapshot: attention.PolicySnapshot{
			Revision: "catalog-v1", Global: append([]workflows.GateSpec(nil), gates...),
		},
	}
	fixture.observed = TimedPublicationProviderObservation{
		Observation: eventing.PRDevelopmentPublicationProviderObservation{
			Repository: "acme/widgets", PullNumber: 17,
			HeadRepository: "acme/widgets", HeadRef: publication.SourceRef,
			HeadSHA:            publication.SourceCommit,
			HeadCloneURL:       publication.SourceCloneURL,
			CurrentReviewState: eventing.PRDevelopmentReviewApproved,
			ReviewDigest:       "sha256:" + hash("b"),
		},
		ObservedAt: now.Add(time.Second),
	}
	fixture.provider = &publicationGateProviderFake{fixture: fixture, observation: fixture.observed}
	return fixture
}

func (fixture *publicationGateProcessorFixture) processor(t *testing.T) *PublicationGateProcessor {
	t.Helper()
	processor, err := NewPublicationGateProcessor(PublicationGateProcessorConfig{
		Store: fixture.store, Policies: fixture.policies, Provider: fixture.provider,
	})
	if err != nil {
		t.Fatalf("NewPublicationGateProcessor() error = %v", err)
	}
	return processor
}

func (fixture *publicationGateProcessorFixture) record(operation string) {
	fixture.mu.Lock()
	defer fixture.mu.Unlock()
	fixture.ops = append(fixture.ops, operation)
}

func (fixture *publicationGateProcessorFixture) operations() []string {
	fixture.mu.Lock()
	defer fixture.mu.Unlock()
	return append([]string(nil), fixture.ops...)
}

func (fixture *publicationGateProcessorFixture) clearOperations() {
	fixture.mu.Lock()
	defer fixture.mu.Unlock()
	fixture.ops = nil
}

type publicationGatePolicySourceFake struct {
	fixture  *publicationGateProcessorFixture
	snapshot attention.PolicySnapshot
	err      error
	calls    int
}

func (source *publicationGatePolicySourceFake) WithAttentionPolicy(
	ctx context.Context,
	selector attention.PolicySelector,
	use attention.PolicyUse,
) error {
	if source == nil {
		return attention.ErrInvalidPolicySource
	}
	source.calls++
	source.fixture.record("policy-source")
	if source.err != nil {
		return source.err
	}
	if selector.Repository != "acme/widgets" ||
		selector.DecisionPoint != eventing.PRDevelopmentPublicationDecisionBeforePush {
		return fmt.Errorf("unexpected policy selector: %#v", selector)
	}
	return use(ctx, source.snapshot)
}

type publicationGateProviderFake struct {
	mu          sync.Mutex
	fixture     *publicationGateProcessorFixture
	observation TimedPublicationProviderObservation
	err         error
	calls       int
}

func (provider *publicationGateProviderFake) ObservePublication(
	ctx context.Context,
	stored eventing.PRDevelopmentCase,
	expected eventing.PRDevelopmentThreadIdentity,
) (TimedPublicationProviderObservation, error) {
	if provider == nil {
		return TimedPublicationProviderObservation{}, errors.New("nil provider")
	}
	provider.mu.Lock()
	provider.calls++
	providerErr := provider.err
	observation := provider.observation
	provider.mu.Unlock()
	provider.fixture.record("provider")
	if err := ctx.Err(); err != nil {
		return TimedPublicationProviderObservation{}, err
	}
	if providerErr != nil {
		return TimedPublicationProviderObservation{}, providerErr
	}
	if stored.ID != provider.fixture.store.snapshot.Case.ID ||
		expected != provider.fixture.store.snapshot.Thread.Identity {
		return TimedPublicationProviderObservation{}, errors.New("unexpected provider subject")
	}
	return observation, nil
}

func (provider *publicationGateProviderFake) callsValue() int {
	provider.mu.Lock()
	defer provider.mu.Unlock()
	return provider.calls
}

type publicationGateStoreFake struct {
	mu               sync.Mutex
	fixture          *publicationGateProcessorFixture
	publication      eventing.PRDevelopmentPublication
	snapshot         eventing.PRDevelopmentPublicationGateContextSnapshot
	policyPin        eventing.PRDevelopmentPublicationPolicyPin
	subjectPin       eventing.PRDevelopmentPublicationSubjectPin
	providerPin      eventing.PRDevelopmentPublicationProviderPin
	policyWinner     *attention.PreparedPolicy
	subjectWinner    *publicationGateSubjectWinner
	providerWinner   *TimedPublicationProviderObservation
	snapshotErr      error
	failAfter        map[string]error
	responseMutators map[string]func(*eventing.PRDevelopmentPublication)
}

func (store *publicationGateStoreFake) AuthenticateClaimedPRDevelopmentPublicationGate(
	ctx context.Context,
	publicationID string,
	claimToken string,
	claimEpoch int64,
) (eventing.PRDevelopmentPublicationGateAuthentication, error) {
	if store == nil {
		return eventing.PRDevelopmentPublicationGateAuthentication{}, errors.New("nil store")
	}
	store.fixture.record("authenticate")
	if err := ctx.Err(); err != nil {
		return eventing.PRDevelopmentPublicationGateAuthentication{}, err
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if err := store.requireClaim(publicationID, claimToken, claimEpoch); err != nil {
		return eventing.PRDevelopmentPublicationGateAuthentication{}, err
	}
	return eventing.PRDevelopmentPublicationGateAuthentication{
		Publication: redactPublicationGateClaimFake(store.publication),
		Repository:  store.snapshot.Case.Repository,
	}, nil
}

type publicationGateSubjectWinner struct {
	canonical           []byte
	revision            string
	conversationVersion int64
	transcriptDigest    string
}

func (store *publicationGateStoreFake) claim() eventing.PRDevelopmentPublication {
	store.mu.Lock()
	defer store.mu.Unlock()
	return clonePublicationGatePublication(store.publication)
}

func (store *publicationGateStoreFake) current() eventing.PRDevelopmentPublication {
	store.mu.Lock()
	defer store.mu.Unlock()
	return clonePublicationGatePublication(store.publication)
}

func (store *publicationGateStoreFake) restoreInitialClaim() {
	store.mu.Lock()
	defer store.mu.Unlock()
	if store.publication.Status == eventing.PRDevelopmentPublicationPushReady {
		store.publication.Status = eventing.PRDevelopmentPublicationClaimed
		store.publication.ClaimFrom = eventing.PRDevelopmentPublicationPending
		store.publication.ClaimOwner = "publication-test"
		store.publication.ClaimToken = strings.Repeat("9", 64)
		claimUntil := store.snapshot.Publication.ClaimUntil
		store.publication.ClaimUntil = claimUntil
	}
}

func (store *publicationGateStoreFake) GetPRDevelopmentPublication(
	ctx context.Context,
	publicationID string,
) (eventing.PRDevelopmentPublication, error) {
	if store == nil {
		return eventing.PRDevelopmentPublication{}, errors.New("nil store")
	}
	store.fixture.record("get")
	if err := ctx.Err(); err != nil {
		return eventing.PRDevelopmentPublication{}, err
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if publicationID != store.publication.ID {
		return eventing.PRDevelopmentPublication{}, eventing.ErrNotFound
	}
	return redactPublicationGateClaimFake(store.publication), nil
}

func (store *publicationGateStoreFake) GetPRDevelopmentPublicationForReview(
	ctx context.Context,
	reviewLedgerEntryID string,
) (eventing.PRDevelopmentPublication, error) {
	if store == nil {
		return eventing.PRDevelopmentPublication{}, errors.New("nil store")
	}
	if err := ctx.Err(); err != nil {
		return eventing.PRDevelopmentPublication{}, err
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if reviewLedgerEntryID != store.publication.ReviewLedgerEntryID {
		return eventing.PRDevelopmentPublication{}, eventing.ErrNotFound
	}
	return redactPublicationGateClaimFake(store.publication), nil
}

func (store *publicationGateStoreFake) GetClaimedPRDevelopmentPublicationGateContextSnapshot(
	ctx context.Context,
	publicationID string,
	claimToken string,
	claimEpoch int64,
) (eventing.PRDevelopmentPublicationGateContextSnapshot, error) {
	if store == nil {
		return eventing.PRDevelopmentPublicationGateContextSnapshot{}, errors.New("nil store")
	}
	store.fixture.record("snapshot")
	if err := ctx.Err(); err != nil {
		return eventing.PRDevelopmentPublicationGateContextSnapshot{}, err
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if store.snapshotErr != nil {
		return eventing.PRDevelopmentPublicationGateContextSnapshot{}, store.snapshotErr
	}
	if err := store.requireClaim(publicationID, claimToken, claimEpoch); err != nil {
		return eventing.PRDevelopmentPublicationGateContextSnapshot{}, err
	}
	snapshot := store.snapshot
	snapshot.Publication = redactPublicationGateClaimFake(store.publication)
	return snapshot, nil
}

func (store *publicationGateStoreFake) PinPRDevelopmentPublicationPolicy(
	ctx context.Context,
	input eventing.PRDevelopmentPublicationPolicyPin,
) (eventing.PRDevelopmentPublication, bool, error) {
	return store.mutate(ctx, "pin-policy", input.PublicationID, input.ClaimToken, input.ClaimEpoch,
		func() bool {
			if store.publication.PolicyRevision != "" {
				return false
			}
			store.policyPin = input
			policyRevision := input.PolicyRevision
			pinnedPolicy := input.PinnedPolicy
			if store.policyWinner != nil {
				policyRevision = store.policyWinner.DecisionRevision()
				pinnedPolicy = store.policyWinner.Canonical()
			}
			store.publication.PolicyRevision = policyRevision
			store.publication.PinnedPolicy = append([]byte(nil), pinnedPolicy...)
			store.publication.PinnedPolicyHash = strings.Repeat("1", 64)
			return true
		})
}

func (store *publicationGateStoreFake) PinPRDevelopmentPublicationSubject(
	ctx context.Context,
	input eventing.PRDevelopmentPublicationSubjectPin,
) (eventing.PRDevelopmentPublication, bool, error) {
	return store.mutate(ctx, "pin-subject", input.PublicationID, input.ClaimToken, input.ClaimEpoch,
		func() bool {
			if store.publication.SubjectRevision != "" {
				return false
			}
			if input.ExpectedConversationVersion != store.snapshot.Conversation.Version ||
				input.ExpectedTranscriptDigest != store.snapshot.TranscriptDigest {
				return false
			}
			store.subjectPin = input
			subjectRevision := input.SubjectRevision
			pinnedSubject := input.PinnedSubject
			if store.subjectWinner != nil {
				subjectRevision = store.subjectWinner.revision
				pinnedSubject = store.subjectWinner.canonical
				store.snapshot.Conversation.Version = store.subjectWinner.conversationVersion
				store.snapshot.TranscriptDigest = store.subjectWinner.transcriptDigest
			}
			store.publication.SubjectRevision = subjectRevision
			store.publication.PinnedSubject = append([]byte(nil), pinnedSubject...)
			store.publication.PinnedSubjectHash = strings.Repeat("2", 64)
			return true
		})
}

func (store *publicationGateStoreFake) PinPRDevelopmentPublicationProvider(
	ctx context.Context,
	input eventing.PRDevelopmentPublicationProviderPin,
) (eventing.PRDevelopmentPublication, bool, error) {
	store.mu.Lock()
	snapshot := store.snapshot
	store.mu.Unlock()
	if input.ObservedAt.IsZero() || !validPublicationGateProviderObservationForTest(input.Observation) {
		store.fixture.record("pin-provider")
		return eventing.PRDevelopmentPublication{}, false,
			eventing.ErrInvalidPRDevelopmentPublication
	}
	if !publicationProviderMatchesGateContext(input.Observation, snapshot) {
		store.fixture.record("pin-provider")
		return eventing.PRDevelopmentPublication{}, false,
			eventing.ErrPRDevelopmentPublicationConflict
	}
	return store.mutate(ctx, "pin-provider", input.PublicationID, input.ClaimToken, input.ClaimEpoch,
		func() bool {
			if store.publication.ProviderObservationHash != "" {
				return false
			}
			store.providerPin = input
			observation := input.Observation
			observedAt := input.ObservedAt
			if store.providerWinner != nil {
				observation = store.providerWinner.Observation
				observedAt = store.providerWinner.ObservedAt
			}
			store.publication.ProviderObservation = observation
			store.publication.ProviderObservationJSON = []byte(`{"provider":"pinned"}`)
			store.publication.ProviderObservationHash = strings.Repeat("e", 64)
			pinnedAt := observedAt
			store.publication.ProviderPinnedAt = &pinnedAt
			store.publication.ProviderObservedAt = &observedAt
			return true
		})
}

func validPublicationGateProviderObservationForTest(
	observation eventing.PRDevelopmentPublicationProviderObservation,
) bool {
	validState := observation.CurrentReviewState == eventing.PRDevelopmentReviewApproved ||
		observation.CurrentReviewState == eventing.PRDevelopmentReviewChangesRequested ||
		observation.CurrentReviewState == eventing.PRDevelopmentReviewCommented ||
		observation.CurrentReviewState == eventing.PRDevelopmentReviewDismissed
	return validProviderRepositoryIdentity(observation.Repository) && observation.PullNumber > 0 &&
		validProviderRepositoryIdentity(observation.HeadRepository) &&
		validStoredGitRef(observation.HeadRef) && validObjectID(observation.HeadSHA) &&
		validHTTPSURL(observation.HeadCloneURL) &&
		validAttentionRevision(observation.ReviewDigest) && validState
}

func (store *publicationGateStoreFake) MarkPRDevelopmentPublicationPushReady(
	ctx context.Context,
	input eventing.PRDevelopmentPublicationMarkPushReady,
) (eventing.PRDevelopmentPublication, bool, error) {
	return store.mutate(ctx, "push-ready", input.PublicationID, input.ClaimToken, input.ClaimEpoch,
		func() bool {
			if store.publication.Status == eventing.PRDevelopmentPublicationPushReady {
				return false
			}
			store.publication.DecisionRunID = input.DecisionRunID
			store.publication.Status = eventing.PRDevelopmentPublicationPushReady
			store.publication.ClaimFrom = ""
			store.publication.ClaimOwner = ""
			store.publication.ClaimToken = ""
			store.publication.ClaimUntil = nil
			return true
		})
}

func (store *publicationGateStoreFake) CompletePRDevelopmentPublicationPrestart(
	ctx context.Context,
	input eventing.PRDevelopmentPublicationPrestartCompletion,
) (eventing.PRDevelopmentPublication, bool, error) {
	return store.mutate(ctx, "terminal", input.PublicationID, input.ClaimToken, input.ClaimEpoch,
		func() bool {
			completedAt := store.publication.UpdatedAt.Add(time.Second)
			store.publication.Status = input.Status
			store.publication.ClaimFrom = ""
			store.publication.ClaimOwner = ""
			store.publication.ClaimToken = ""
			store.publication.ClaimUntil = nil
			store.publication.LastErrorCode = input.ErrorCode
			store.publication.LastErrorDetail = input.InternalError
			store.publication.CompletedAt = &completedAt
			return true
		})
}

func (store *publicationGateStoreFake) mutate(
	ctx context.Context,
	operation string,
	publicationID string,
	claimToken string,
	claimEpoch int64,
	apply func() bool,
) (eventing.PRDevelopmentPublication, bool, error) {
	store.fixture.record(operation)
	if err := ctx.Err(); err != nil {
		return eventing.PRDevelopmentPublication{}, false, err
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if err := store.requireClaim(publicationID, claimToken, claimEpoch); err != nil {
		return eventing.PRDevelopmentPublication{}, false, err
	}
	changed := apply()
	result := redactPublicationGateClaimFake(store.publication)
	if mutate := store.responseMutators[operation]; mutate != nil {
		mutate(&result)
	}
	if err := store.failAfter[operation]; err != nil {
		return eventing.PRDevelopmentPublication{}, false, err
	}
	return result, changed, nil
}

func (store *publicationGateStoreFake) requireClaim(
	publicationID string,
	claimToken string,
	claimEpoch int64,
) error {
	if store.publication.ID != publicationID || store.publication.ClaimToken != claimToken ||
		store.publication.ClaimEpoch != claimEpoch {
		return eventing.ErrStaleLease
	}
	return nil
}

func redactPublicationGateClaimFake(publication eventing.PRDevelopmentPublication) eventing.PRDevelopmentPublication {
	publication.ClaimToken = ""
	return clonePublicationGatePublication(publication)
}

func clonePublicationGatePublication(publication eventing.PRDevelopmentPublication) eventing.PRDevelopmentPublication {
	publication.PinnedPolicy = append([]byte(nil), publication.PinnedPolicy...)
	publication.PinnedSubject = append([]byte(nil), publication.PinnedSubject...)
	publication.ProviderObservationJSON = append([]byte(nil), publication.ProviderObservationJSON...)
	return publication
}

func assertCanonicalPublicationGateJSON(t *testing.T, raw []byte) {
	t.Helper()
	if !json.Valid(raw) {
		t.Fatalf("canonical JSON is invalid: %q", raw)
	}
	var compact bytes.Buffer
	if err := json.Compact(&compact, raw); err != nil {
		t.Fatalf("compact canonical JSON: %v", err)
	}
	if !bytes.Equal(raw, compact.Bytes()) {
		t.Fatalf("JSON is not compact canonical form: %q", raw)
	}
}

func containsString(values []string, want string) bool {
	return indexOfString(values, want) >= 0
}

func indexOfString(values []string, want string) int {
	for index, value := range values {
		if value == want {
			return index
		}
	}
	return -1
}

var (
	_ PublicationGateStore        = (*publicationGateStoreFake)(nil)
	_ attention.PolicySource      = (*publicationGatePolicySourceFake)(nil)
	_ PublicationProviderObserver = (*publicationGateProviderFake)(nil)
)
