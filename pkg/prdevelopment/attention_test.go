package prdevelopment

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	sharedattention "github.com/sipeed/picoclaw/pkg/attention"
	"github.com/sipeed/picoclaw/pkg/eventing"
	"github.com/sipeed/picoclaw/pkg/gitworkspace"
	"github.com/sipeed/picoclaw/pkg/media"
	"github.com/sipeed/picoclaw/pkg/memory"
	"github.com/sipeed/picoclaw/pkg/prdevelopment/localci"
	"github.com/sipeed/picoclaw/pkg/session"
	"github.com/sipeed/picoclaw/pkg/workflows"
)

const (
	attentionRuntimeCaseID       = "pdc_10101010101010101010101010101010"
	attentionRuntimeThreadID     = "pdt_20202020202020202020202020202020"
	attentionRuntimeSessionID    = "pds_30303030303030303030303030303030"
	attentionRuntimeAttemptID    = "pdr_40404040404040404040404040404040"
	attentionRuntimeControllerID = "pctl_50505050505050505050505050505050"
	attentionRuntimeLineID       = "pdln_60606060606060606060606060606060"
	attentionRuntimeAttemptEntry = "pdle_70707070707070707070707070707070"
	attentionRuntimeReviewEntry  = "pdle_80808080808080808080808080808080"
	attentionRuntimeMessageOne   = "pdm_90909090909090909090909090909090"
	attentionRuntimeMessageTwo   = "pdm_a0a0a0a0a0a0a0a0a0a0a0a0a0a0a0a0"
	attentionRuntimeBase         = "1111111111111111111111111111111111111111"
	attentionRuntimeCommit       = "2222222222222222222222222222222222222222"
	attentionRuntimeTree         = "3333333333333333333333333333333333333333"
)

func TestPRDevelopmentAttentionLauncherNoopHasZeroEffects(t *testing.T) {
	fixture := newAttentionRuntimeFixture(t)
	launcher := fixture.launcher(t, []workflows.GateSpec{
		{ID: "off", Kind: workflows.GateZero},
		{ID: "also_off", Kind: workflows.GateZero},
	}, nil)

	result, err := launcher.Launch(context.Background(), AttentionLaunchRequest{
		CaseID:        attentionRuntimeCaseID,
		DecisionPoint: "pr_development.review_attention_required",
	})
	if err != nil || !result.Noop || result.RunID != "" || result.Status != "" ||
		result.Existing {
		t.Fatalf("Launch(noop) = (%#v, %v)", result, err)
	}
	runs, listErr := fixture.runs.ListRuns(context.Background())
	if listErr != nil || len(runs) != 0 || fixture.runtimeAcquires.Load() != 0 ||
		fixture.workspaceFactoryCalls.Load() != 0 || fixture.workspace.calls.Load() != 0 ||
		fixture.evidence.planCalls.Load() != 0 || fixture.evidence.executionCalls.Load() != 0 ||
		fixture.store.findCalls.Load() != 0 || fixture.store.admitCalls.Load() != 0 {
		t.Fatalf(
			"noop effects runs=%d runtime=%d factory=%d git=%d plan=%d execution=%d find=%d admit=%d err=%v",
			len(runs), fixture.runtimeAcquires.Load(), fixture.workspaceFactoryCalls.Load(),
			fixture.workspace.calls.Load(), fixture.evidence.planCalls.Load(),
			fixture.evidence.executionCalls.Load(), fixture.store.findCalls.Load(),
			fixture.store.admitCalls.Load(), listErr,
		)
	}
}

func TestPRDevelopmentAttentionAdmissionUncertaintyOutranksCommitCancellation(
	t *testing.T,
) {
	t.Parallel()

	commitErr := fmt.Errorf(
		"%w: %w",
		eventing.ErrPRDevelopmentAttentionAdmissionUncertain,
		context.Canceled,
	)
	mapped := mapAttentionAdmissionError(commitErr)
	if !errors.Is(mapped, sharedattention.ErrPrivateRunAdmissionUncertain) ||
		errors.Is(mapped, context.Canceled) {
		t.Fatalf("mapped uncertainty = %v", mapped)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	sanitized := sanitizeAttentionLaunchError(ctx, mapped)
	if !errors.Is(sanitized, sharedattention.ErrPrivateRunAdmissionUncertain) ||
		errors.Is(sanitized, context.Canceled) {
		t.Fatalf("sanitized uncertainty = %v", sanitized)
	}
}

func TestPRDevelopmentAttentionLauncherAnchoredTargetUsesRuntimeAndExactDecision(t *testing.T) {
	fixture := newAttentionRuntimeFixture(t)
	launcher := fixture.launcher(t, []workflows.GateSpec{
		{
			ID:        "automatic",
			Kind:      workflows.GateDeterministic,
			When:      "false",
			Title:     "Automatic policy",
			Questions: []any{"Confirm?"},
		},
	}, nil)

	result, err := launcher.Launch(context.Background(), AttentionLaunchRequest{
		CaseID:        attentionRuntimeCaseID,
		DecisionPoint: "pr_development.review_attention_required",
	})
	if err != nil || result.RunID == "" || result.Status != workflows.RunStatusSucceeded ||
		result.Noop || result.Existing || result.SubjectRevision == "" {
		t.Fatalf("Launch() = (%#v, %v)", result, err)
	}
	const expectedSubjectRevision = "sha256:0111ef001d58fef86e3711808e33b57aad29a0eed9c7e12450ffe4c02e3b8551"
	if result.SubjectRevision != expectedSubjectRevision {
		t.Fatalf("subject revision = %q, want %q", result.SubjectRevision, expectedSubjectRevision)
	}
	if fixture.runtimeAcquires.Load() != 1 || fixture.runtimeReleases.Load() != 1 ||
		fixture.runtimeActive.Load() || fixture.workspaceFactoryCalls.Load() != 1 ||
		fixture.workspace.calls.Load() != 1 || fixture.store.admitCalls.Load() != 1 {
		t.Fatalf(
			"runtime effects acquire=%d release=%d active=%v factory=%d git=%d admit=%d",
			fixture.runtimeAcquires.Load(), fixture.runtimeReleases.Load(),
			fixture.runtimeActive.Load(), fixture.workspaceFactoryCalls.Load(),
			fixture.workspace.calls.Load(), fixture.store.admitCalls.Load(),
		)
	}
	links := fixture.store.linksSnapshot()
	if len(links) != 1 {
		t.Fatalf("decision links = %#v", links)
	}
	link := links[0]
	if link.Key.ReviewEntryID != attentionRuntimeReviewEntry ||
		link.Key.ReviewEntryHash != fixture.snapshot.ReviewEntry.EntryHash ||
		link.Key.ConversationVersion != fixture.snapshot.Conversation.Version ||
		link.Key.SubjectRevision != result.SubjectRevision ||
		link.Key.PolicyRevision != result.PolicyRevision || link.RunID != result.RunID ||
		link.Snapshot != fixture.snapshot.HighWater {
		t.Fatalf("decision link = %#v", link)
	}
	canonicalKey, keyErr := canonicalPRDevelopmentAttentionDecisionKey(link.Key)
	wantRunID, runIDErr := sharedattention.RunIDForDecisionKey(canonicalKey)
	const expectedDecisionKey = `{"case_id":"pdc_10101010101010101010101010101010","conversation_version":2,"decision_point":"pr_development.review_attention_required","policy_revision":"sha256:ef9c349961df203fd96132935a597651fefa92ba99a796ca9161c7fabdab6b37","review_entry_hash":"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb","review_entry_id":"pdle_80808080808080808080808080808080","subject_revision":"sha256:0111ef001d58fef86e3711808e33b57aad29a0eed9c7e12450ffe4c02e3b8551"}`
	const expectedRunID = "wr_fc81ff4bfc22f65c8769a9f2c35df53f"
	if keyErr != nil || runIDErr != nil || canonicalKey != expectedDecisionKey ||
		wantRunID != expectedRunID || result.RunID != expectedRunID {
		t.Fatalf("deterministic run ID = (%q, %v, %v), want %q", wantRunID, keyErr, runIDErr, result.RunID)
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	for _, private := range []string{
		attentionRuntimeReviewEntry,
		result.SubjectRevision,
		result.PolicyRevision,
		fixture.snapshot.ReviewEntry.EntryHash,
	} {
		if strings.Contains(string(encoded), private) {
			t.Fatalf("public result exposed private identity %q: %s", private, encoded)
		}
	}

	// The ledger is deliberately anchored at absolute ordinals 4/5. Context
	// construction must locate the target by exact identity, not index 5.
	if got := fixture.workspace.lastRequest(); got.LineID != attentionRuntimeLineID ||
		got.ExpectedVersion != fixture.snapshot.Fence.LineVersion ||
		got.ExpectedBase != attentionRuntimeBase || got.ExpectedTip != attentionRuntimeCommit ||
		got.ExpectedTree != attentionRuntimeTree {
		t.Fatalf("Git review request = %#v", got)
	}

	// Controller/session lifecycle counters are admission high-water, not
	// semantic identity. Their later advance must recover the original link and
	// run instead of forking or rejecting an exact historical decision.
	fixture.store.advanceLifecycleCounters()
	replayed, err := launcher.Launch(context.Background(), AttentionLaunchRequest{
		CaseID:        attentionRuntimeCaseID,
		DecisionPoint: "pr_development.review_attention_required",
	})
	if err != nil || !replayed.Existing || replayed.RunID != result.RunID ||
		replayed.SubjectRevision != result.SubjectRevision ||
		replayed.PolicyRevision != result.PolicyRevision ||
		fixture.store.admitCalls.Load() != 1 || len(fixture.store.linksSnapshot()) != 1 {
		t.Fatalf(
			"lifecycle-only replay = (%#v, %v), admits=%d links=%d",
			replayed,
			err,
			fixture.store.admitCalls.Load(),
			len(fixture.store.linksSnapshot()),
		)
	}
}

func TestPRDevelopmentAttentionWorkingGateProjectsFullProtectedConversation(t *testing.T) {
	fixture := newAttentionRuntimeFixture(t)
	agent := &attentionRuntimeGateAgent{
		backend:       fixture.sessions,
		runtimeActive: &fixture.runtimeActive,
	}
	launcher := fixture.launcher(t, []workflows.GateSpec{
		{
			ID:       "isolated",
			Kind:     workflows.GateAIIsolatedContext,
			AgentID:  "reviewer-agent",
			Criteria: "ask when isolated review evidence is ambiguous",
			Title:    "Check isolated evidence",
		},
		{
			ID:        "automatic",
			Kind:      workflows.GateDeterministic,
			When:      "false",
			Title:     "Automatic policy",
			Questions: []any{"Confirm?"},
		},
		{ID: "off", Kind: workflows.GateZero},
		{
			ID:       "discussion",
			Kind:     workflows.GateAIWorkingContext,
			AgentID:  "owner-agent",
			Criteria: "ask when the repair owner needs a product decision",
			Title:    "Discuss the repair",
		},
	}, agent)

	result, err := launcher.Launch(context.Background(), AttentionLaunchRequest{
		CaseID:        attentionRuntimeCaseID,
		DecisionPoint: "pr_development.review_attention_required",
	})
	if err != nil || result.RunID == "" || result.Status != workflows.RunStatusSucceeded {
		t.Fatalf("Launch(working) = (%#v, %v)", result, err)
	}
	if fixture.runtimeAcquires.Load() != 1 || fixture.runtimeReleases.Load() != 1 ||
		fixture.runtimeActive.Load() || len(agent.captures) != 1 || len(agent.requests) != 2 {
		t.Fatalf(
			"working effects acquire=%d release=%d active=%v captures=%d agents=%d",
			fixture.runtimeAcquires.Load(), fixture.runtimeReleases.Load(),
			fixture.runtimeActive.Load(), len(agent.captures), len(agent.requests),
		)
	}
	ref := agent.captures[0]
	stored, found, err := fixture.sessions.ReadSessionSnapshot(context.Background(), ref.Session)
	if err != nil || !found || stored.Revision != ref.ExpectedRevision ||
		len(stored.History) != len(fixture.snapshot.Conversation.Messages) ||
		stored.Summary != "" || len(stored.Aliases) != 4 ||
		stored.History[0].Content != fixture.snapshot.Conversation.Messages[0].Content ||
		stored.History[1].Content != fixture.snapshot.Conversation.Messages[1].Content {
		t.Fatalf("protected session = (%#v, found=%v, err=%v)", stored, found, err)
	}
	if ref.AgentID != fixture.snapshot.Controller.AgentID ||
		agent.requests[0].AgentID != "reviewer-agent" ||
		agent.requests[0].FrozenReadOnlySession != nil ||
		!agent.requests[0].EphemeralSession || agent.requests[0].Session != "" ||
		agent.requests[1].AgentID != fixture.snapshot.Controller.AgentID ||
		agent.requests[1].FrozenReadOnlySession == nil ||
		agent.requests[1].Session != "" || agent.requests[1].EphemeralSession {
		t.Fatalf("mixed model requests = %#v, capture = %#v", agent.requests, ref)
	}
	workingContextBytes, err := json.Marshal(agent.requests[1].Scope)
	if err != nil {
		t.Fatalf("marshal working gate scope: %v", err)
	}
	workingContext := string(workingContextBytes)
	for _, required := range []string{
		`"line":null`,
		"A product decision needs the user.",
		"compatible behavior",
		`"captured_version":2`,
		`"storage":"protected_read_only_session"`,
	} {
		if !strings.Contains(workingContext, required) {
			t.Fatalf("working gate context omitted %q: %s", required, workingContext)
		}
	}
	for _, private := range []string{
		fixture.snapshot.Conversation.Messages[0].Content,
		fixture.snapshot.Conversation.Messages[1].Content,
		attentionRuntimeReviewEntry,
		attentionRuntimeControllerID,
		attentionRuntimeSessionID,
		fixture.snapshot.ReviewEntry.EntryHash,
		"workspace-attention",
	} {
		if strings.Contains(workingContext, private) {
			t.Fatalf("working gate subject exposed protected value %q", private)
		}
	}
}

func TestPRDevelopmentAttentionRejectsNonOwnerWorkingAgentBeforeRuntime(t *testing.T) {
	fixture := newAttentionRuntimeFixture(t)
	launcher := fixture.launcher(t, []workflows.GateSpec{{
		ID:       "discussion",
		Kind:     workflows.GateAIWorkingContext,
		AgentID:  "different-agent",
		Criteria: "ask when blocked",
		Title:    "Discuss",
	}}, &attentionRuntimeGateAgent{})
	result, err := launcher.Launch(context.Background(), AttentionLaunchRequest{
		CaseID:        attentionRuntimeCaseID,
		DecisionPoint: "pr_development.review_attention_required",
	})
	if result != (AttentionLaunchResult{}) || !errors.Is(err, ErrUnavailable) ||
		fixture.runtimeAcquires.Load() != 0 || fixture.workspaceFactoryCalls.Load() != 0 ||
		fixture.store.admitCalls.Load() != 0 {
		t.Fatalf(
			"non-owner working gate = (%#v, %v), runtime=%d workspace=%d admit=%d",
			result,
			err,
			fixture.runtimeAcquires.Load(),
			fixture.workspaceFactoryCalls.Load(),
			fixture.store.admitCalls.Load(),
		)
	}
}

func TestPRDevelopmentAttentionSessionIndependentMixtureNeedsNoSessionStore(t *testing.T) {
	fixture := newAttentionRuntimeFixture(t)
	fixture.withoutRuntimeSessions = true
	agent := &attentionRuntimeGateAgent{runtimeActive: &fixture.runtimeActive}
	launcher := fixture.launcher(t, []workflows.GateSpec{
		{
			ID:       "isolated",
			Kind:     workflows.GateAIIsolatedContext,
			AgentID:  "reviewer-agent",
			Criteria: "ask only from frozen evidence",
			Title:    "Check evidence",
		},
		{
			ID:        "deterministic",
			Kind:      workflows.GateDeterministic,
			When:      "false",
			Title:     "Deterministic check",
			Questions: []any{"Confirm?"},
		},
	}, agent)
	result, err := launcher.Launch(context.Background(), AttentionLaunchRequest{
		CaseID:        attentionRuntimeCaseID,
		DecisionPoint: "pr_development.review_attention_required",
	})
	if err != nil || result.RunID == "" || result.Status != workflows.RunStatusSucceeded ||
		fixture.runtimeAcquires.Load() != 1 || fixture.runtimeReleases.Load() != 1 ||
		fixture.runtimeActive.Load() || len(agent.requests) != 1 || len(agent.captures) != 0 {
		t.Fatalf(
			"session-independent launch = (%#v, %v), acquire=%d release=%d active=%v requests=%d captures=%d",
			result,
			err,
			fixture.runtimeAcquires.Load(),
			fixture.runtimeReleases.Load(),
			fixture.runtimeActive.Load(),
			len(agent.requests),
			len(agent.captures),
		)
	}
}

func TestPRDevelopmentAttentionWaitingRunReleasesRuntimeWithoutReservation(t *testing.T) {
	fixture := newAttentionRuntimeFixture(t)
	agent := &attentionRuntimeGateAgent{
		runtimeActive: &fixture.runtimeActive,
		ask:           true,
	}
	launcher := fixture.launcher(t, []workflows.GateSpec{{
		ID:       "isolated",
		Kind:     workflows.GateAIIsolatedContext,
		AgentID:  "reviewer-agent",
		Criteria: "ask when the compatibility choice needs the user",
		Title:    "Choose compatibility behavior",
	}}, agent)
	result, err := launcher.Launch(context.Background(), AttentionLaunchRequest{
		CaseID:        attentionRuntimeCaseID,
		DecisionPoint: "pr_development.review_attention_required",
	})
	if err != nil || result.RunID == "" || result.Status != workflows.RunStatusWaiting ||
		fixture.runtimeAcquires.Load() != 1 || fixture.runtimeReleases.Load() != 1 ||
		fixture.runtimeActive.Load() || len(agent.requests) != 1 || len(agent.captures) != 0 {
		t.Fatalf(
			"waiting launch = (%#v, %v), acquire=%d release=%d active=%v requests=%d captures=%d",
			result,
			err,
			fixture.runtimeAcquires.Load(),
			fixture.runtimeReleases.Load(),
			fixture.runtimeActive.Load(),
			len(agent.requests),
			len(agent.captures),
		)
	}
	tasks, err := launcher.executor.ListHumanTasks(context.Background(), result.RunID)
	if err != nil || len(tasks) != 1 || tasks[0].Status != workflows.HumanTaskStatusWaiting {
		t.Fatalf("waiting tasks = (%#v, %v)", tasks, err)
	}
	current, err := fixture.store.GetPRDevelopmentAttentionSnapshot(
		context.Background(),
		attentionRuntimeCaseID,
	)
	if err != nil || current.Controller.Phase != eventing.PRDevelopmentControllerReady ||
		current.Controller.LeaseKind != "" || current.Controller.LeaseToken != "" ||
		current.Controller.LeaseUntil != nil || current.Controller.MutationReservationKey != "" {
		t.Fatalf("attention wait acquired controller authority: (%#v, %v)", current.Controller, err)
	}
}

func TestPRDevelopmentAttentionRejectsOversizedMandatorySubjectBeforeAdmission(t *testing.T) {
	const maximumPinnedReviewDiffBytes = 512 << 10
	diffPrefix := "diff --git a/pkg/example.go b/pkg/example.go\n+"
	tests := []struct {
		name string
		diff string
	}{
		{
			name: "oversized non-ledger component",
			diff: strings.Repeat("x", workflows.MaxWorkflowGateSubjectBytes+1),
		},
		{
			name: "producer-bounded component expands in JSON",
			diff: diffPrefix + strings.Repeat(
				`"`,
				maximumPinnedReviewDiffBytes-len(diffPrefix),
			),
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newAttentionRuntimeFixture(t)
			fixture.workspace.snapshot.UnifiedDiff = test.diff
			launcher := fixture.launcher(t, []workflows.GateSpec{{
				ID:        "automatic",
				Kind:      workflows.GateDeterministic,
				When:      "false",
				Title:     "Automatic policy",
				Questions: []any{"Confirm?"},
			}}, nil)
			result, err := launcher.Launch(context.Background(), AttentionLaunchRequest{
				CaseID:        attentionRuntimeCaseID,
				DecisionPoint: "pr_development.review_attention_required",
			})
			if result != (AttentionLaunchResult{}) ||
				err != ErrAttentionSubjectTooLarge ||
				errors.Is(err, ErrAIContextCompactionRequired) ||
				fixture.runtimeAcquires.Load() != 1 || fixture.runtimeReleases.Load() != 1 ||
				fixture.runtimeActive.Load() || fixture.store.findCalls.Load() != 0 ||
				fixture.store.admitCalls.Load() != 0 {
				t.Fatalf(
					"oversized attention subject = (%#v, %v), acquire=%d release=%d active=%v find=%d admit=%d",
					result,
					err,
					fixture.runtimeAcquires.Load(),
					fixture.runtimeReleases.Load(),
					fixture.runtimeActive.Load(),
					fixture.store.findCalls.Load(),
					fixture.store.admitCalls.Load(),
				)
			}
		})
	}
}

func TestPRDevelopmentAttentionLedgerOverflowStillRequiresCompaction(t *testing.T) {
	fixture := newAttentionRuntimeFixture(t)
	snapshot := fixture.snapshot
	priorCount := maximumAILedgerSuffixEntries + 2
	prior := make([]eventing.PRDevelopmentLedgerEntry, 0, priorCount)
	for index := 0; index < priorCount; index++ {
		prior = append(prior, eventing.PRDevelopmentLedgerEntry{Ordinal: index})
	}
	attempt := snapshot.Ledger.Entries[0]
	review := snapshot.Ledger.Entries[1]
	attempt.Ordinal = len(prior)
	review.Ordinal = attempt.Ordinal + 1
	review.PreviousHash = attempt.EntryHash
	snapshot.Ledger.Entries = append(prior, attempt, review)
	snapshot.ReviewEntry = review
	snapshot.HighWater.LedgerEntryCount = len(snapshot.Ledger.Entries)
	snapshot.HighWater.ReviewEntryOrdinal = review.Ordinal
	fixture.snapshot = snapshot
	fixture.store.snapshot = snapshot

	launcher := fixture.launcher(t, []workflows.GateSpec{{
		ID:        "automatic",
		Kind:      workflows.GateDeterministic,
		When:      "false",
		Title:     "Automatic policy",
		Questions: []any{"Confirm?"},
	}}, nil)
	result, err := launcher.Launch(context.Background(), AttentionLaunchRequest{
		CaseID:        attentionRuntimeCaseID,
		DecisionPoint: "pr_development.review_attention_required",
	})
	if result != (AttentionLaunchResult{}) ||
		err != ErrAIContextCompactionRequired ||
		errors.Is(err, ErrAttentionSubjectTooLarge) ||
		fixture.runtimeAcquires.Load() != 1 || fixture.runtimeReleases.Load() != 1 ||
		fixture.runtimeActive.Load() || fixture.store.findCalls.Load() != 0 ||
		fixture.store.admitCalls.Load() != 0 {
		t.Fatalf(
			"oversized ledger = (%#v, %v), acquire=%d release=%d active=%v find=%d admit=%d",
			result,
			err,
			fixture.runtimeAcquires.Load(),
			fixture.runtimeReleases.Load(),
			fixture.runtimeActive.Load(),
			fixture.store.findCalls.Load(),
			fixture.store.admitCalls.Load(),
		)
	}
}

func TestAttentionTargetLookupSupportsAnchoredAndUnanchoredLedgers(t *testing.T) {
	fixture := newAttentionRuntimeFixture(t)
	tests := []struct {
		name   string
		mutate func(*eventing.PRDevelopmentAttentionSnapshot)
	}{
		{name: "anchored"},
		{name: "unanchored", mutate: func(snapshot *eventing.PRDevelopmentAttentionSnapshot) {
			snapshot.Ledger.Entries = append(
				[]eventing.PRDevelopmentLedgerEntry(nil),
				snapshot.Ledger.Entries...,
			)
			snapshot.Ledger.Entries[0].Ordinal = 0
			snapshot.Ledger.Entries[1].Ordinal = 1
			snapshot.ReviewEntry.Ordinal = 1
			snapshot.HighWater.ReviewEntryOrdinal = 1
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			snapshot := fixture.snapshot
			if test.mutate != nil {
				test.mutate(&snapshot)
			}
			index, err := attentionReviewEntryIndex(snapshot)
			attempt, attemptErr := attentionTargetAttemptEntry(snapshot)
			history, historyErr := attentionLedgerHistory(snapshot)
			if err != nil || attemptErr != nil || historyErr != nil || index != 1 ||
				attempt.ID != attentionRuntimeAttemptEntry || len(history.Entries) != 0 ||
				history.TotalEntries != 2 {
				t.Fatalf(
					"target lookup = index %d/%v attempt %#v/%v history %#v/%v",
					index,
					err,
					attempt,
					attemptErr,
					history,
					historyErr,
				)
			}
		})
	}
}

func TestAttentionSessionBindingRejectsRollbackAndHashDrift(t *testing.T) {
	fixture := newAttentionRuntimeFixture(t)
	scope := attentionSessionScope(attentionRuntimeCaseID, "owner-agent")
	key := session.BuildSessionKey(scope)
	desired := attentionSessionAliases(fixture.snapshot, "owner-agent")
	base := session.SessionSnapshot{
		Key:      key,
		Scope:    session.CloneScope(&scope),
		Aliases:  append([]string(nil), desired...),
		Revision: "revision-1",
	}
	if err := validateAttentionSessionBinding(base, scope, key, desired); err != nil {
		t.Fatalf("exact binding error = %v", err)
	}
	tests := []struct {
		name   string
		mutate func(*session.SessionSnapshot)
	}{
		{name: "review hash drift", mutate: func(value *session.SessionSnapshot) {
			value.Aliases[2] = strings.TrimSuffix(
				value.Aliases[2],
				fixture.snapshot.HighWater.ReviewEntryHash,
			) + attentionRuntimeDigest("f")
		}},
		{name: "conversation digest drift", mutate: func(value *session.SessionSnapshot) {
			value.Aliases[3] = strings.TrimSuffix(
				value.Aliases[3],
				fixture.snapshot.HighWater.TranscriptDigest,
			) + attentionRuntimeDigest("e")
		}},
		{name: "newer review rollback", mutate: func(value *session.SessionSnapshot) {
			value.Aliases[2] = strings.Replace(value.Aliases[2], ":review:5:", ":review:7:", 1)
		}},
		{name: "newer conversation rollback", mutate: func(value *session.SessionSnapshot) {
			value.Aliases[3] = strings.Replace(value.Aliases[3], ":conversation:2:", ":conversation:3:", 1)
		}},
		{name: "noncanonical ordinal", mutate: func(value *session.SessionSnapshot) {
			value.Aliases[2] = strings.Replace(value.Aliases[2], ":review:5:", ":review:05:", 1)
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			changed := base
			changed.Aliases = append([]string(nil), base.Aliases...)
			test.mutate(&changed)
			if err := validateAttentionSessionBinding(changed, scope, key, desired); err == nil {
				t.Fatal("validateAttentionSessionBinding() error = nil")
			}
		})
	}
}

type attentionRuntimeFixture struct {
	snapshot               eventing.PRDevelopmentAttentionSnapshot
	store                  *attentionRuntimeStore
	evidence               *attentionRuntimeEvidence
	workspace              *attentionRuntimeWorkspace
	sessions               *session.JSONLBackend
	runs                   workflows.RunStore
	workspacePath          string
	runtimeActive          atomic.Bool
	runtimeAcquires        atomic.Int32
	runtimeReleases        atomic.Int32
	workspaceFactoryCalls  atomic.Int32
	withoutRuntimeSessions bool
}

func newAttentionRuntimeFixture(t *testing.T) *attentionRuntimeFixture {
	t.Helper()
	snapshot, plan, execution, diff := attentionRuntimeSnapshot(t)
	if err := validateAttentionSnapshot(snapshot); err != nil {
		t.Fatalf("attention runtime snapshot is invalid: %v", err)
	}
	store := &attentionRuntimeStore{
		snapshot: snapshot,
		cases:    map[string]eventing.PRDevelopmentCase{snapshot.Case.ID: snapshot.Case},
		links:    make(map[eventing.PRDevelopmentAttentionDecisionKey]eventing.PRDevelopmentAttentionDecisionRunLink),
	}
	memoryStore, err := memory.NewJSONLStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewJSONLStore() error = %v", err)
	}
	backend := session.NewJSONLBackend(memoryStore)
	t.Cleanup(func() { _ = backend.Close() })
	workspacePath := t.TempDir()
	return &attentionRuntimeFixture{
		snapshot:      snapshot,
		store:         store,
		evidence:      &attentionRuntimeEvidence{plan: plan, execution: execution},
		workspace:     &attentionRuntimeWorkspace{snapshot: diff},
		sessions:      backend,
		runs:          workflows.NewFileRunStore(workspacePath),
		workspacePath: workspacePath,
	}
}

func (fixture *attentionRuntimeFixture) launcher(
	t *testing.T,
	gates []workflows.GateSpec,
	agent workflows.AgentRunner,
) *AttentionLauncher {
	t.Helper()
	policy := sharedattention.PolicySourceFunc(func(
		ctx context.Context,
		selector sharedattention.PolicySelector,
		use sharedattention.PolicyUse,
	) error {
		if selector.Repository != "owner/repo" ||
			selector.DecisionPoint != "pr_development.review_attention_required" {
			return errors.New("unexpected policy selector")
		}
		return use(ctx, sharedattention.PolicySnapshot{
			Revision: "attention-runtime-policy-v1",
			Global:   append([]workflows.GateSpec(nil), gates...),
		})
	})
	launcher, err := NewAttentionLauncher(AttentionLauncherConfig{
		Store: fixture.store,
		Executor: &workflows.Executor{
			WorkspaceDir: fixture.workspacePath,
			Store:        fixture.runs,
			Agents:       agent,
		},
		Runs:     fixture.runs,
		Policies: policy,
		Evidence: fixture.evidence,
		Workspaces: func() (AttentionReviewWorkspace, error) {
			fixture.workspaceFactoryCalls.Add(1)
			if !fixture.runtimeActive.Load() {
				return nil, errors.New("workspace resolved outside runtime generation")
			}
			fixture.workspace.runtimeActive = &fixture.runtimeActive
			return fixture.workspace, nil
		},
		AcquireRuntime: func(
			ctx context.Context,
			agentID string,
		) (context.Context, session.SessionStore, func(), error) {
			if agentID != fixture.snapshot.Controller.AgentID ||
				!fixture.runtimeActive.CompareAndSwap(false, true) {
				return nil, nil, nil, errors.New("invalid runtime acquisition")
			}
			fixture.runtimeAcquires.Add(1)
			var once sync.Once
			var runtimeSessions session.SessionStore = fixture.sessions
			if fixture.withoutRuntimeSessions {
				runtimeSessions = nil
			}
			return ctx, runtimeSessions, func() {
				once.Do(func() {
					fixture.runtimeActive.Store(false)
					fixture.runtimeReleases.Add(1)
				})
			}, nil
		},
	})
	if err != nil {
		t.Fatalf("NewAttentionLauncher() error = %v", err)
	}
	return launcher
}

type attentionRuntimeStore struct {
	mu         sync.Mutex
	snapshot   eventing.PRDevelopmentAttentionSnapshot
	cases      map[string]eventing.PRDevelopmentCase
	links      map[eventing.PRDevelopmentAttentionDecisionKey]eventing.PRDevelopmentAttentionDecisionRunLink
	findCalls  atomic.Int32
	admitCalls atomic.Int32
}

func (store *attentionRuntimeStore) GetPRDevelopmentAttentionSnapshot(
	context.Context,
	string,
) (eventing.PRDevelopmentAttentionSnapshot, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	return store.snapshot, nil
}

func (store *attentionRuntimeStore) GetPRDevelopmentCase(
	_ context.Context,
	caseID string,
) (eventing.PRDevelopmentCase, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	captured, ok := store.cases[caseID]
	if !ok {
		return eventing.PRDevelopmentCase{}, eventing.ErrNotFound
	}
	return captured, nil
}

func (store *attentionRuntimeStore) ListPRDevelopmentCases(
	context.Context,
	eventing.PRDevelopmentCaseFilter,
) (eventing.PRDevelopmentCasePage, error) {
	return eventing.PRDevelopmentCasePage{}, nil
}

func (store *attentionRuntimeStore) GetPRDevelopmentAttentionDecisionRun(
	_ context.Context,
	key eventing.PRDevelopmentAttentionDecisionKey,
) (eventing.PRDevelopmentAttentionDecisionRunLink, error) {
	store.findCalls.Add(1)
	store.mu.Lock()
	defer store.mu.Unlock()
	link, ok := store.links[key]
	if !ok {
		return eventing.PRDevelopmentAttentionDecisionRunLink{}, eventing.ErrNotFound
	}
	return link, nil
}

func (store *attentionRuntimeStore) AdmitPRDevelopmentAttentionDecisionRun(
	ctx context.Context,
	admission eventing.PRDevelopmentAttentionDecisionRunAdmission,
	create func(context.Context) error,
) (eventing.PRDevelopmentAttentionDecisionRunLink, bool, error) {
	store.admitCalls.Add(1)
	store.mu.Lock()
	defer store.mu.Unlock()
	if existing, ok := store.links[admission.Key]; ok {
		return existing, true, nil
	}
	if admission.Snapshot != store.snapshot.HighWater {
		return eventing.PRDevelopmentAttentionDecisionRunLink{}, false,
			eventing.ErrPRDevelopmentAttentionConflict
	}
	if err := create(ctx); err != nil {
		return eventing.PRDevelopmentAttentionDecisionRunLink{}, false, err
	}
	link := eventing.PRDevelopmentAttentionDecisionRunLink{
		Key:       admission.Key,
		Snapshot:  admission.Snapshot,
		RunID:     admission.RunID,
		CreatedAt: time.Now().UTC(),
	}
	store.links[admission.Key] = link
	return link, false, nil
}

func (store *attentionRuntimeStore) linksSnapshot() []eventing.PRDevelopmentAttentionDecisionRunLink {
	store.mu.Lock()
	defer store.mu.Unlock()
	links := make([]eventing.PRDevelopmentAttentionDecisionRunLink, 0, len(store.links))
	for _, link := range store.links {
		links = append(links, link)
	}
	return links
}

func (store *attentionRuntimeStore) advanceLifecycleCounters() {
	store.mu.Lock()
	defer store.mu.Unlock()
	store.snapshot.Controller.Revision++
	store.snapshot.HighWater.ControllerRevision = store.snapshot.Controller.Revision
	store.snapshot.OwnerSession.Version++
	store.snapshot.HighWater.OwnerSessionVersion = store.snapshot.OwnerSession.Version
}

type attentionRuntimeEvidence struct {
	plan           localci.Plan
	execution      localci.Execution
	planCalls      atomic.Int32
	executionCalls atomic.Int32
}

func (evidence *attentionRuntimeEvidence) GetPlan(
	context.Context,
	string,
) (localci.Plan, bool, error) {
	evidence.planCalls.Add(1)
	return evidence.plan, true, nil
}

func (evidence *attentionRuntimeEvidence) GetExecution(
	context.Context,
	string,
) (localci.Execution, bool, error) {
	evidence.executionCalls.Add(1)
	return evidence.execution, true, nil
}

type attentionRuntimeWorkspace struct {
	mu            sync.Mutex
	snapshot      gitworkspace.PinnedLineReviewSnapshot
	requests      []gitworkspace.PinnedLineReviewRequest
	calls         atomic.Int32
	runtimeActive *atomic.Bool
}

func (workspace *attentionRuntimeWorkspace) SnapshotPinnedLineReview(
	_ context.Context,
	request gitworkspace.PinnedLineReviewRequest,
) (gitworkspace.PinnedLineReviewSnapshot, error) {
	workspace.calls.Add(1)
	if workspace.runtimeActive == nil || !workspace.runtimeActive.Load() {
		return gitworkspace.PinnedLineReviewSnapshot{}, errors.New("Git read outside runtime lease")
	}
	workspace.mu.Lock()
	workspace.requests = append(workspace.requests, request)
	workspace.mu.Unlock()
	return workspace.snapshot, nil
}

func (workspace *attentionRuntimeWorkspace) lastRequest() gitworkspace.PinnedLineReviewRequest {
	workspace.mu.Lock()
	defer workspace.mu.Unlock()
	if len(workspace.requests) == 0 {
		return gitworkspace.PinnedLineReviewRequest{}
	}
	return workspace.requests[len(workspace.requests)-1]
}

type attentionRuntimeGateAgent struct {
	backend       *session.JSONLBackend
	runtimeActive *atomic.Bool
	ask           bool
	captures      []workflows.ReadOnlySessionRef
	requests      []workflows.AgentRequest
}

func (agent *attentionRuntimeGateAgent) CaptureReadOnlySession(
	ctx context.Context,
	ref workflows.ReadOnlySessionRef,
) (*workflows.FrozenReadOnlySession, error) {
	if agent.runtimeActive == nil || !agent.runtimeActive.Load() {
		return nil, errors.New("session capture outside runtime lease")
	}
	snapshot, found, err := agent.backend.ReadSessionSnapshot(ctx, ref.Session)
	if err != nil || !found || snapshot.Revision != ref.ExpectedRevision {
		return nil, errors.New("exact projected session is unavailable")
	}
	agent.captures = append(agent.captures, ref)
	return &workflows.FrozenReadOnlySession{
		AgentID:         ref.AgentID,
		Snapshot:        snapshot,
		HistoryRevision: "sha256:" + attentionRuntimeDigest("b"),
		FrozenMedia:     media.FrozenSet{Version: media.FrozenSetVersion},
	}, nil
}

func (agent *attentionRuntimeGateAgent) RunAgent(
	_ context.Context,
	request workflows.AgentRequest,
) (map[string]any, error) {
	if agent.runtimeActive == nil || !agent.runtimeActive.Load() {
		return nil, errors.New("working model outside runtime lease")
	}
	agent.requests = append(agent.requests, request)
	response := `{"ask_user":false,"reason":"the exact context resolves the decision","questions":[]}`
	if agent.ask {
		response = `{"ask_user":true,"reason":"two compatible outcomes need a product choice","questions":["Which compatibility behavior should be retained?"]}`
	}
	structured := workflows.ValidateAgentStructuredOutput(response, request.Output)
	if !structured.Valid {
		return nil, fmt.Errorf("invalid test gate output: %s", structured.Error)
	}
	return map[string]any{
		"text":             response,
		"structured":       structured.Structured,
		"structured_json":  structured.RawJSON,
		"structured_valid": true,
	}, nil
}

func attentionRuntimeSnapshot(
	t *testing.T,
) (
	eventing.PRDevelopmentAttentionSnapshot,
	localci.Plan,
	localci.Execution,
	gitworkspace.PinnedLineReviewSnapshot,
) {
	t.Helper()
	now := time.Date(2026, time.August, 9, 12, 0, 0, 0, time.UTC)
	attempt := eventing.PRDevelopmentRepairAttempt{
		ID:                  attentionRuntimeAttemptID,
		SessionID:           attentionRuntimeSessionID,
		Ordinal:             2,
		ConversationVersion: 2,
		Instruction:         "resolve the review concern",
		Status:              eventing.PRDevelopmentRepairCompleted,
		Claims:              1,
		Summary:             "implemented a locally validated candidate",
		Iterations:          2,
		CreatedAt:           now.Add(-2 * time.Minute),
		UpdatedAt:           now.Add(-time.Minute),
	}
	owner := eventing.PRDevelopmentRepairSession{
		ID:      attentionRuntimeSessionID,
		CaseID:  attentionRuntimeCaseID,
		Version: 9,
		AgentID: "owner-agent",
		Attempts: []eventing.PRDevelopmentRepairAttempt{
			{ID: "pdr_11111111111111111111111111111111", Ordinal: 0},
			{ID: "pdr_22222222222222222222222222222222", Ordinal: 1},
			attempt,
		},
	}
	fence := eventing.PRDevelopmentAttemptReviewFence{
		AttemptID:                  attentionRuntimeAttemptID,
		ControllerID:               attentionRuntimeControllerID,
		ThreadID:                   attentionRuntimeThreadID,
		LineID:                     attentionRuntimeLineID,
		Ordinal:                    2,
		LineVersion:                3,
		MutationEpoch:              3,
		ParkIntentID:               "pdlnpark_11111111111111111111111111111111",
		BaseCommit:                 attentionRuntimeBase,
		TipCommit:                  attentionRuntimeCommit,
		Tree:                       attentionRuntimeTree,
		LineReviewDigest:           attentionRuntimeDigest("1"),
		MutationReservationDigest:  attentionRuntimeDigest("2"),
		MutationLeaseEpoch:         3,
		MutationLeaseTokenDigest:   attentionRuntimeDigest("3"),
		MutationControllerRevision: 12,
		ReviewLeaseEpoch:           4,
		ReviewLeaseTokenDigest:     attentionRuntimeDigest("4"),
		ReviewControllerRevision:   13,
		PreviousHash:               attentionRuntimeDigest("5"),
		FenceHash:                  attentionRuntimeDigest("6"),
		CreatedAt:                  now.Add(-time.Minute),
		ReviewedAt:                 pointerTime(now),
	}
	controller := eventing.PRDevelopmentController{
		ID:               attentionRuntimeControllerID,
		ThreadID:         attentionRuntimeThreadID,
		OwnerSessionID:   attentionRuntimeSessionID,
		AgentID:          "owner-agent",
		Revision:         14,
		Phase:            eventing.PRDevelopmentControllerReady,
		LineID:           attentionRuntimeLineID,
		WorkspaceID:      "workspace-attention",
		LineVersion:      3,
		MutationEpoch:    3,
		TipCommit:        attentionRuntimeCommit,
		Tree:             attentionRuntimeTree,
		CurrentAttemptID: attentionRuntimeAttemptID,
		FenceCount:       3,
		FencesDigest:     fence.FenceHash,
	}
	attemptEntry := eventing.PRDevelopmentLedgerEntry{
		ID:             attentionRuntimeAttemptEntry,
		ThreadID:       attentionRuntimeThreadID,
		Ordinal:        4,
		Kind:           eventing.PRDevelopmentLedgerAttempt,
		AttemptID:      attentionRuntimeAttemptID,
		FenceOrdinal:   2,
		CaseID:         attentionRuntimeCaseID,
		CaseOrdinal:    0,
		Commit:         attentionRuntimeCommit,
		Summary:        "implemented the candidate",
		CIPlanDigest:   attentionRuntimeDigest("7"),
		CIResultDigest: attentionRuntimeDigest("8"),
		CIStatus:       eventing.PRDevelopmentCIPassed,
		FenceHash:      fence.FenceHash,
		PreviousHash:   attentionRuntimeDigest("9"),
		EntryHash:      attentionRuntimeDigest("a"),
		CreatedAt:      now.Add(-30 * time.Second),
	}
	reviewEntry := eventing.PRDevelopmentLedgerEntry{
		ID:            attentionRuntimeReviewEntry,
		ThreadID:      attentionRuntimeThreadID,
		Ordinal:       5,
		Kind:          eventing.PRDevelopmentLedgerReview,
		AttemptID:     attentionRuntimeAttemptID,
		FenceOrdinal:  2,
		CaseID:        attentionRuntimeCaseID,
		CaseOrdinal:   0,
		Summary:       "A product decision needs the user.",
		ReviewOutcome: eventing.PRDevelopmentLedgerReviewAttentionRequired,
		Findings: []eventing.PRDevelopmentLedgerReviewFinding{{
			Severity:       eventing.ReviewSeverityHigh,
			Title:          "Choose compatibility behavior",
			File:           "pkg/example.go",
			Message:        "Two safe behaviors have different compatibility tradeoffs.",
			Recommendation: "Choose the expected compatibility contract.",
		}},
		FenceHash:    fence.FenceHash,
		PreviousHash: attemptEntry.EntryHash,
		EntryHash:    attentionRuntimeDigest("b"),
		CreatedAt:    now,
	}
	thread := eventing.PRDevelopmentThread{
		ID:          attentionRuntimeThreadID,
		Kind:        eventing.PRDevelopmentThreadProvider,
		CaseCount:   1,
		CasesDigest: attentionRuntimeDigest("c"),
		Cases: []eventing.PRDevelopmentThreadCaseLink{{
			CaseID:       attentionRuntimeCaseID,
			Ordinal:      0,
			LinkedAt:     now.Add(-time.Hour),
			PreviousHash: attentionRuntimeDigest("d"),
			LinkHash:     attentionRuntimeDigest("e"),
		}},
	}
	captured := eventing.PRDevelopmentCase{
		ID: attentionRuntimeCaseID,
		PRDevelopmentCaptureInput: eventing.PRDevelopmentCaptureInput{
			PRDevelopmentCaptureIdentity: eventing.PRDevelopmentCaptureIdentity{
				EventID:          "ev_11111111111111111111111111111111",
				DispatchID:       "dsp_22222222222222222222222222222222",
				RunID:            "wr_33333333333333333333333333333333",
				WorkflowRef:      workflows.GitHubPRDevelopmentWorkflowRef,
				WorkflowRevision: attentionRuntimeDigest("f"),
				Connector:        "github",
			},
			Repository:           "owner/repo",
			PullNumber:           42,
			PullURL:              "https://github.com/owner/repo/pull/42",
			PullState:            eventing.PRDevelopmentPullOpen,
			BaseRepository:       "owner/repo",
			BaseRef:              "main",
			BaseSHA:              attentionRuntimeBase,
			HeadRepository:       "owner/repo",
			HeadRef:              "feature",
			HeadSHA:              attentionRuntimeCommit,
			ReviewAuthor:         "reviewer",
			SubmittedReviewState: eventing.PRDevelopmentReviewChangesRequested,
			CurrentReviewState:   eventing.PRDevelopmentReviewChangesRequested,
			ReviewCommitSHA:      attentionRuntimeCommit,
			ReviewSubmittedAt:    now.Add(-time.Hour),
			Feedback:             "Please resolve the compatibility behavior.",
		},
		CreatedAt: now.Add(-time.Hour),
		UpdatedAt: now,
	}
	conversation := eventing.PRDevelopmentConversation{
		CaseID:  attentionRuntimeCaseID,
		Version: 2,
		Messages: []eventing.PRDevelopmentMessage{
			{
				ID:        attentionRuntimeMessageOne,
				CaseID:    attentionRuntimeCaseID,
				Ordinal:   0,
				Role:      eventing.PRDevelopmentMessageUser,
				Content:   "We need to preserve the old client contract.",
				CreatedAt: now.Add(-20 * time.Second),
			},
			{
				ID:        attentionRuntimeMessageTwo,
				CaseID:    attentionRuntimeCaseID,
				Ordinal:   1,
				Role:      eventing.PRDevelopmentMessageAssistant,
				Content:   "The strict and compatible behaviors are both locally safe.",
				CreatedAt: now.Add(-10 * time.Second),
			},
		},
	}
	ledger := eventing.PRDevelopmentLedger{
		ThreadID:          attentionRuntimeThreadID,
		Entries:           []eventing.PRDevelopmentLedgerEntry{attemptEntry, reviewEntry},
		EntriesDigest:     reviewEntry.EntryHash,
		CheckpointsDigest: attentionRuntimeDigest("0"),
	}
	high := eventing.PRDevelopmentAttentionHighWater{
		CaseID:                  attentionRuntimeCaseID,
		SelectedOrdinal:         0,
		ConversationVersion:     2,
		TranscriptDigest:        attentionRuntimeDigest("1"),
		ThreadID:                attentionRuntimeThreadID,
		ThreadCaseCount:         1,
		ThreadCasesDigest:       thread.CasesDigest,
		LedgerEntryCount:        2,
		LedgerEntriesDigest:     ledger.EntriesDigest,
		LedgerCheckpointCount:   0,
		LedgerCheckpointsDigest: ledger.CheckpointsDigest,
		ReviewEntryID:           reviewEntry.ID,
		ReviewEntryOrdinal:      reviewEntry.Ordinal,
		ReviewEntryHash:         reviewEntry.EntryHash,
		AttemptID:               attentionRuntimeAttemptID,
		AttemptOrdinal:          2,
		FenceOrdinal:            fence.Ordinal,
		FenceHash:               fence.FenceHash,
		ControllerID:            controller.ID,
		ControllerRevision:      controller.Revision,
		ControllerLineVersion:   controller.LineVersion,
		ControllerFenceCount:    controller.FenceCount,
		ControllerFencesDigest:  controller.FencesDigest,
		OwnerSessionID:          owner.ID,
		OwnerSessionVersion:     owner.Version,
		OwnerAttemptCount:       len(owner.Attempts),
	}
	plan := localci.Plan{
		Version:          localci.EvidenceVersion,
		DiscoveryVersion: localci.DiscoveryVersion,
		DependencyDigest: attentionRuntimeDigest("2"),
		Digest:           attemptEntry.CIPlanDigest,
		Complete:         true,
		Steps: []localci.Step{{
			ID:       "targeted-test",
			Name:     "Targeted test",
			Kind:     localci.StepTest,
			Origin:   localci.OriginMake,
			Source:   "Makefile",
			Required: true,
		}},
	}
	execution := localci.Execution{
		Version:   localci.EvidenceVersion,
		Digest:    attemptEntry.CIResultDigest,
		ResultKey: attentionRuntimeDigest("3"),
		Evidence: localci.CandidateEvidence{
			DependencyDigest: plan.DependencyDigest,
			PlanDigest:       plan.Digest,
		},
		Status: localci.StatusPassed,
		Steps: []localci.StepResult{{
			StepID: "targeted-test",
			Status: localci.StatusPassed,
			Output: "ok",
		}},
		StartedAt:   now.Add(-time.Minute),
		CompletedAt: now.Add(-30 * time.Second),
	}
	diff := gitworkspace.PinnedLineReviewSnapshot{
		Version:       fence.LineVersion,
		MutationEpoch: fence.MutationEpoch,
		ParkIntentID:  fence.ParkIntentID,
		BaseCommit:    fence.BaseCommit,
		Commit:        fence.TipCommit,
		Tree:          fence.Tree,
		ChangedPaths:  []string{"pkg/example.go"},
		UnifiedDiff:   "diff --git a/pkg/example.go b/pkg/example.go\n+compatible behavior\n",
		ReviewDigest:  fence.LineReviewDigest,
	}
	return eventing.PRDevelopmentAttentionSnapshot{
		Case:         captured,
		Thread:       thread,
		Conversation: conversation,
		OwnerSession: owner,
		Controller:   controller,
		Fence:        fence,
		Ledger:       ledger,
		ReviewEntry:  reviewEntry,
		HighWater:    high,
	}, plan, execution, diff
}

func attentionRuntimeDigest(character string) string {
	return strings.Repeat(character, 64)
}

func pointerTime(value time.Time) *time.Time {
	return &value
}
