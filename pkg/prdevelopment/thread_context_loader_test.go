package prdevelopment

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/sipeed/picoclaw/pkg/eventing"
	"github.com/sipeed/picoclaw/pkg/workflows"
)

const threadContextLoaderCaseID = "pdc_00000000000000000000000000000001"

func TestDevelopmentThreadContextLoaderUsesExactSelectedCasesAndAdmittedConversationPrefix(
	t *testing.T,
) {
	store := newThreadContextLoaderStore(40, 2, 0, "")
	store.conversation = threadContextLoaderConversation(
		store.snapshot.Thread.Cases[2].CaseID,
		[]string{
			"admitted-zero",
			"admitted-one",
			"not-admitted-two",
			"not-admitted-three",
		},
	)
	agent := &threadContextLoaderAgent{}
	loader := newTestDevelopmentThreadContextLoader(t, store, agent)

	encoded, err := loader.Load(
		context.Background(),
		store.snapshot.Thread.Cases[2].CaseID,
		2,
	)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	wantLinks, err := selectDevelopmentProviderCaseLinks(store.snapshot)
	if err != nil {
		t.Fatalf("selectDevelopmentProviderCaseLinks() error = %v", err)
	}
	wantCaseCalls := make([]string, 0, len(wantLinks))
	for _, link := range wantLinks {
		wantCaseCalls = append(wantCaseCalls, link.CaseID)
	}
	if !reflect.DeepEqual(store.caseCalls, wantCaseCalls) {
		t.Fatalf("GetPRDevelopmentCase() calls = %v, want %v", store.caseCalls, wantCaseCalls)
	}
	if store.snapshotCalls != 1 || store.conversationCalls != 1 {
		t.Fatalf(
			"snapshot calls = %d, conversation calls = %d, want 1 each",
			store.snapshotCalls,
			store.conversationCalls,
		)
	}
	if len(agent.requests) != 0 || len(store.appendCalls) != 0 {
		t.Fatalf(
			"agent requests = %d, checkpoint appends = %d, want zero",
			len(agent.requests),
			len(store.appendCalls),
		)
	}
	var projected developmentThreadContextValue
	if err = json.Unmarshal([]byte(encoded), &projected); err != nil {
		t.Fatalf("decode projected context: %v", err)
	}
	if len(projected.Conversation) != 2 ||
		projected.Conversation[0].Content != "admitted-zero" ||
		projected.Conversation[1].Content != "admitted-one" ||
		strings.Contains(encoded, "not-admitted") {
		t.Fatalf("projected conversation = %+v", projected.Conversation)
	}
}

func TestDevelopmentThreadContextLoaderCompactsOneExactReviewedPrefix(t *testing.T) {
	store := newThreadContextLoaderStore(
		1,
		0,
		maximumAILedgerSuffixEntries+2,
		"initial summary",
	)
	for index := range store.snapshot.Ledger.Entries {
		store.snapshot.Ledger.Entries[index].Summary = fmt.Sprintf(
			"description-%03d",
			index,
		)
	}
	line := 17
	store.snapshot.Ledger.Entries[1].Findings = []eventing.PRDevelopmentLedgerReviewFinding{{
		Severity:       eventing.ReviewSeverityHigh,
		Title:          "retained race",
		File:           "worker.go",
		Line:           &line,
		Message:        "release can occur twice",
		Evidence:       "two terminal paths",
		Impact:         "reservation ownership is ambiguous",
		Recommendation: "fence the transition",
		Validation:     "run the race test",
	}}
	privateValues := []string{
		"private-entry-id",
		"private-attempt-id",
		"private-case-id",
		"private-commit",
		"private-tree",
		"private-ci-plan-digest",
		"private-ci-result-digest",
		"private-fence-hash",
		"private-previous-hash",
		"private-entry-hash",
	}
	first := &store.snapshot.Ledger.Entries[0]
	first.ID = privateValues[0]
	first.AttemptID = privateValues[1]
	first.CaseID = privateValues[2]
	first.Commit = privateValues[3]
	first.Tree = privateValues[4]
	first.CIPlanDigest = privateValues[5]
	first.CIResultDigest = privateValues[6]
	first.FenceHash = privateValues[7]
	first.PreviousHash = privateValues[8]
	first.EntryHash = privateValues[9]
	agent := &threadContextLoaderAgent{responses: []map[string]any{{
		"text": `{"summary":"compacted attempts zero through thirty-two"}`,
	}}}
	loader := newTestDevelopmentThreadContextLoader(t, store, agent)

	encoded, err := loader.Load(context.Background(), threadContextLoaderCaseID, 0)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if len(store.appendCalls) != 1 {
		t.Fatalf("checkpoint appends = %d, want 1", len(store.appendCalls))
	}
	appendInput := store.appendCalls[0]
	if appendInput.ThroughOrdinal != 65 ||
		appendInput.SourceDigest != store.snapshot.Ledger.Entries[65].EntryHash ||
		appendInput.Summary != "compacted attempts zero through thirty-two" ||
		appendInput.CompactorID != "ledger-compactor-v1" ||
		appendInput.PromptDigest != developmentLedgerCompactorPromptDigest() {
		t.Fatalf("checkpoint append = %+v", appendInput)
	}
	if store.snapshotCalls != 2 || store.conversationCalls != 2 {
		t.Fatalf(
			"snapshot calls = %d, conversation calls = %d, want reload after append",
			store.snapshotCalls,
			store.conversationCalls,
		)
	}
	if len(agent.requests) != 1 {
		t.Fatalf("agent requests = %d, want 1", len(agent.requests))
	}
	assertLedgerCompactorIsolation(t, agent.requests[0])
	for _, privateValue := range privateValues {
		if strings.Contains(agent.requests[0].Context, privateValue) {
			t.Fatalf("compactor context leaked %q: %s", privateValue, agent.requests[0].Context)
		}
	}
	var compactorInput developmentLedgerCompactorContext
	if err = json.Unmarshal([]byte(agent.requests[0].Context), &compactorInput); err != nil {
		t.Fatalf("decode compactor context: %v", err)
	}
	if compactorInput.PreviousSummary != "" || len(compactorInput.Entries) != 66 {
		t.Fatalf("compactor input = %+v", compactorInput)
	}
	for index, entry := range compactorInput.Entries {
		if entry.Description != fmt.Sprintf("description-%03d", index) {
			t.Fatalf("compactor entry %d = %+v", index, entry)
		}
		if (index%2 == 0 && entry.ValidationStatus != "passed") ||
			(index%2 == 1 && entry.ValidationStatus != "") {
			t.Fatalf("compactor entry %d validation status = %q", index, entry.ValidationStatus)
		}
	}
	if len(compactorInput.Entries[1].Findings) != 1 ||
		compactorInput.Entries[1].Findings[0].Title != "retained race" {
		t.Fatalf("compactor findings = %+v", compactorInput.Entries[1].Findings)
	}
	var projected developmentThreadContextValue
	if err = json.Unmarshal([]byte(encoded), &projected); err != nil {
		t.Fatalf("decode projected context: %v", err)
	}
	if projected.Ledger.Checkpoint == nil ||
		projected.Ledger.Checkpoint.ThroughOrdinal != 65 ||
		len(projected.Ledger.Entries) != maximumAICompactionRetainedEntries ||
		projected.Ledger.Entries[0].Ordinal != 66 {
		t.Fatalf("projected compacted ledger = %+v", projected.Ledger)
	}

	// A restart observes the durable checkpoint and does not ask the model or
	// append a second record for the already compacted prefix.
	restartedAgent := &threadContextLoaderAgent{}
	restarted := newTestDevelopmentThreadContextLoader(t, store, restartedAgent)
	replayed, err := restarted.Load(context.Background(), threadContextLoaderCaseID, 0)
	if err != nil {
		t.Fatalf("restarted Load() error = %v", err)
	}
	if replayed != encoded || len(restartedAgent.requests) != 0 || len(store.appendCalls) != 1 {
		t.Fatalf(
			"restart replay changed context or effects: equal=%t, requests=%d, appends=%d",
			replayed == encoded,
			len(restartedAgent.requests),
			len(store.appendCalls),
		)
	}
}

func TestDevelopmentThreadContextLoaderUsesMultipleBoundedCompactions(t *testing.T) {
	store := newThreadContextLoaderStore(
		1,
		0,
		maximumAILedgerSuffixEntries,
		strings.Repeat("x", eventing.MaxPRDevelopmentLedgerSummaryBytes),
	)
	agent := &threadContextLoaderAgent{responses: []map[string]any{
		{"text": `{"summary":"first compacted prefix"}`},
		{"text": `{"summary":"second compacted prefix"}`},
	}}
	loader := newTestDevelopmentThreadContextLoader(t, store, agent)

	encoded, err := loader.Load(context.Background(), threadContextLoaderCaseID, 0)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if len(store.appendCalls) != 2 ||
		store.appendCalls[0].ThroughOrdinal != 63 ||
		store.appendCalls[1].ThroughOrdinal != 127 {
		t.Fatalf("checkpoint appends = %+v", store.appendCalls)
	}
	if len(agent.requests) != 2 {
		t.Fatalf("agent requests = %d, want 2", len(agent.requests))
	}
	var second developmentLedgerCompactorContext
	if err = json.Unmarshal([]byte(agent.requests[1].Context), &second); err != nil {
		t.Fatalf("decode second compactor context: %v", err)
	}
	if second.PreviousSummary != "first compacted prefix" || len(second.Entries) != 64 {
		t.Fatalf("second compactor context = %+v", second)
	}
	if second.Entries[0].Kind != eventing.PRDevelopmentLedgerAttempt ||
		second.Entries[1].Kind != eventing.PRDevelopmentLedgerReview {
		t.Fatalf("second compactor entry order = %+v", second.Entries[:2])
	}
	var projected developmentThreadContextValue
	if err = json.Unmarshal([]byte(encoded), &projected); err != nil {
		t.Fatalf("decode projected context: %v", err)
	}
	if projected.Ledger.Checkpoint == nil ||
		projected.Ledger.Checkpoint.Summary != "second compacted prefix" ||
		len(projected.Ledger.Entries) != 0 {
		t.Fatalf("final projected ledger = %+v", projected.Ledger)
	}
}

func TestDevelopmentThreadContextLoaderRejectsMalformedCompactorOutput(t *testing.T) {
	tests := map[string]string{
		"not json":        "not-json",
		"missing summary": `{}`,
		"empty summary":   `{"summary":""}`,
		"unknown field":   `{"summary":"ok","extra":true}`,
		"duplicate field": `{"summary":"one","summary":"two"}`,
		"trailing value":  `{"summary":"ok"} {"summary":"later"}`,
		"oversized": `{"summary":"` + strings.Repeat(
			"x",
			eventing.MaxPRDevelopmentLedgerCheckpointSummaryBytes+1,
		) + `"}`,
	}
	for name, response := range tests {
		t.Run(name, func(t *testing.T) {
			store := newThreadContextLoaderStore(
				1,
				0,
				maximumAILedgerSuffixEntries+2,
				"bounded summary",
			)
			agent := &threadContextLoaderAgent{responses: []map[string]any{{"text": response}}}
			loader := newTestDevelopmentThreadContextLoader(t, store, agent)

			_, err := loader.Load(context.Background(), threadContextLoaderCaseID, 0)
			if !errors.Is(err, ErrUnavailable) {
				t.Fatalf("Load() error = %v, want ErrUnavailable", err)
			}
			if len(store.appendCalls) != 0 {
				t.Fatalf("checkpoint appends = %d, want zero", len(store.appendCalls))
			}
		})
	}
}

func TestDevelopmentThreadContextLoaderDoesNotCompactWithoutReviewedBoundary(t *testing.T) {
	store := newThreadContextLoaderStore(1, 0, 0, "")
	store.cases[threadContextLoaderCaseID] = threadContextLoaderCase(
		store.snapshot.Thread.Cases[0],
		strings.Repeat("oversized selected provider review;", 5_000),
	)
	agent := &threadContextLoaderAgent{responses: []map[string]any{{
		"text": `{"summary":"must not be used"}`,
	}}}
	loader := newTestDevelopmentThreadContextLoader(t, store, agent)

	_, err := loader.Load(context.Background(), threadContextLoaderCaseID, 0)
	if !errors.Is(err, ErrAIContextCompactionRequired) {
		t.Fatalf("Load() error = %v, want ErrAIContextCompactionRequired", err)
	}
	if len(agent.requests) != 0 || len(store.appendCalls) != 0 {
		t.Fatalf(
			"agent requests = %d, checkpoint appends = %d, want zero",
			len(agent.requests),
			len(store.appendCalls),
		)
	}
}

func TestDevelopmentThreadContextLoaderFailsClosedOnNonAdvancingAppend(t *testing.T) {
	store := newThreadContextLoaderStore(
		1,
		0,
		maximumAILedgerSuffixEntries+2,
		"bounded summary",
	)
	store.doNotAdvanceCheckpoint = true
	agent := &threadContextLoaderAgent{responses: []map[string]any{
		{"text": `{"summary":"first answer"}`},
		{"text": `{"summary":"must not be requested"}`},
	}}
	loader := newTestDevelopmentThreadContextLoader(t, store, agent)

	_, err := loader.Load(context.Background(), threadContextLoaderCaseID, 0)
	if !errors.Is(err, ErrUnavailable) {
		t.Fatalf("Load() error = %v, want ErrUnavailable", err)
	}
	if len(agent.requests) != 1 {
		t.Fatalf("agent requests = %d, want 1", len(agent.requests))
	}
}

func TestProjectDevelopmentLedgerCarriesExactKnownCIStatus(t *testing.T) {
	snapshot := threadContextSnapshot(1, 0)
	snapshot.Ledger.Entries = threadContextLedgerEntries(2, "bounded summary")
	snapshot.Ledger.Entries[0].CIStatus = eventing.PRDevelopmentCIFailed

	projected, err := projectDevelopmentLedger(snapshot)
	if err != nil {
		t.Fatalf("projectDevelopmentLedger() error = %v", err)
	}
	if len(projected.Entries) != 2 || projected.Entries[0].ValidationStatus != "failed" {
		t.Fatalf("projected ledger = %+v", projected)
	}

	for _, invalid := range []eventing.PRDevelopmentCIStatus{"", "unknown"} {
		snapshot.Ledger.Entries[0].CIStatus = invalid
		if _, err = projectDevelopmentLedger(snapshot); !errors.Is(err, ErrUnavailable) {
			t.Fatalf("CI status %q error = %v, want ErrUnavailable", invalid, err)
		}
	}
	snapshot.Ledger.Entries[0].CIStatus = eventing.PRDevelopmentCIPassed
	snapshot.Ledger.Entries[1].CIStatus = eventing.PRDevelopmentCIPassed
	if _, err = projectDevelopmentLedger(snapshot); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("review CI status error = %v, want ErrUnavailable", err)
	}
}

func newTestDevelopmentThreadContextLoader(
	t *testing.T,
	store *threadContextLoaderStore,
	agent *threadContextLoaderAgent,
) *developmentThreadContextLoader {
	t.Helper()
	loader, err := newDevelopmentThreadContextLoader(developmentThreadContextLoaderConfig{
		Store:       store,
		Agent:       agent,
		AgentID:     "default",
		CompactorID: "ledger-compactor-v1",
	})
	if err != nil {
		t.Fatalf("newDevelopmentThreadContextLoader() error = %v", err)
	}
	return loader
}

func assertLedgerCompactorIsolation(t *testing.T, request workflows.AgentRequest) {
	t.Helper()
	if request.AgentID != "default" || request.Context == "" ||
		!request.EphemeralSession || request.History != "none" ||
		request.Cache != "none" || request.Tools != workflows.AgentToolsNone ||
		request.Session != "" || request.Message != "" || request.Prompt != "" ||
		request.MessageID != "" || request.Scope != nil || request.Inputs != nil ||
		request.FrozenReadOnlySession != nil || !request.PrivateContext ||
		request.IsolatedSystemPrompt != developmentLedgerCompactorPrompt ||
		!reflect.DeepEqual(request.Managed, map[string]any{"mode": "off"}) ||
		!reflect.DeepEqual(request.Delivery, workflows.Delivery{}) {
		t.Fatalf("compactor request has broader authority: %#v", request)
	}
	if request.Output == nil || request.Output.Format != "json" ||
		request.Output.RepairAttempts != 1 {
		t.Fatalf("compactor output contract = %#v", request.Output)
	}
}

type threadContextLoaderStore struct {
	snapshot               eventing.PRDevelopmentContextSnapshot
	cases                  map[string]eventing.PRDevelopmentCase
	conversation           eventing.PRDevelopmentConversation
	snapshotCalls          int
	caseCalls              []string
	conversationCalls      int
	appendCalls            []eventing.PRDevelopmentLedgerCheckpointAppend
	doNotAdvanceCheckpoint bool
}

func newThreadContextLoaderStore(
	totalCases, selectedOrdinal, ledgerEntries int,
	summary string,
) *threadContextLoaderStore {
	snapshot := threadContextSnapshot(totalCases, selectedOrdinal)
	snapshot.Thread.ID = "pdt_00000000000000000000000000000001"
	snapshot.Ledger.ThreadID = snapshot.Thread.ID
	cases := make(map[string]eventing.PRDevelopmentCase, totalCases)
	for ordinal := range snapshot.Thread.Cases {
		caseID := fmt.Sprintf("pdc_%032x", ordinal+1)
		snapshot.Thread.Cases[ordinal].CaseID = caseID
		cases[caseID] = threadContextLoaderCase(
			snapshot.Thread.Cases[ordinal],
			fmt.Sprintf("feedback-%03d", ordinal),
		)
	}
	snapshot.Ledger.Entries = threadContextLedgerEntries(ledgerEntries, summary)
	for index := range snapshot.Ledger.Entries {
		entry := &snapshot.Ledger.Entries[index]
		entry.ThreadID = snapshot.Thread.ID
		entry.EntryHash = fmt.Sprintf("%064x", index+1)
	}
	selectedCaseID := snapshot.Thread.Cases[selectedOrdinal].CaseID
	return &threadContextLoaderStore{
		snapshot:     snapshot,
		cases:        cases,
		conversation: threadContextLoaderConversation(selectedCaseID, nil),
	}
}

func threadContextLoaderCase(
	link eventing.PRDevelopmentThreadCaseLink,
	feedback string,
) eventing.PRDevelopmentCase {
	return eventing.PRDevelopmentCase{
		ID: link.CaseID,
		PRDevelopmentCaptureInput: eventing.PRDevelopmentCaptureInput{
			PullState:            eventing.PRDevelopmentPullOpen,
			BaseRepository:       "owner/base",
			BaseRef:              "main",
			BaseSHA:              "1111111111111111111111111111111111111111",
			HeadRepository:       "owner/head",
			HeadRef:              "feature",
			HeadSHA:              "2222222222222222222222222222222222222222",
			ReviewAuthor:         "reviewer",
			SubmittedReviewState: eventing.PRDevelopmentReviewChangesRequested,
			CurrentReviewState:   eventing.PRDevelopmentReviewChangesRequested,
			ReviewCommitSHA:      "2222222222222222222222222222222222222222",
			ReviewSubmittedAt:    time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC),
			Feedback:             feedback,
		},
		CreatedAt: time.Date(2026, 8, 9, 12, 1, 0, 0, time.UTC),
	}
}

func threadContextLoaderConversation(
	caseID string,
	contents []string,
) eventing.PRDevelopmentConversation {
	messages := make([]eventing.PRDevelopmentMessage, 0, len(contents))
	for ordinal, content := range contents {
		role := eventing.PRDevelopmentMessageUser
		if ordinal%2 == 1 {
			role = eventing.PRDevelopmentMessageAssistant
		}
		messages = append(messages, eventing.PRDevelopmentMessage{
			ID:        fmt.Sprintf("pdm_%032x", ordinal+1),
			CaseID:    caseID,
			Ordinal:   ordinal,
			Role:      role,
			Content:   content,
			CreatedAt: time.Date(2026, 8, 9, 13, ordinal, 0, 0, time.UTC),
		})
	}
	return eventing.PRDevelopmentConversation{
		CaseID:   caseID,
		Version:  int64(len(messages)),
		Messages: messages,
	}
}

func (store *threadContextLoaderStore) GetPRDevelopmentContextSnapshot(
	context.Context,
	string,
) (eventing.PRDevelopmentContextSnapshot, error) {
	store.snapshotCalls++
	return store.snapshot, nil
}

func (store *threadContextLoaderStore) GetPRDevelopmentCase(
	_ context.Context,
	caseID string,
) (eventing.PRDevelopmentCase, error) {
	store.caseCalls = append(store.caseCalls, caseID)
	captured, ok := store.cases[caseID]
	if !ok {
		return eventing.PRDevelopmentCase{}, errors.New("case missing")
	}
	return captured, nil
}

func (store *threadContextLoaderStore) GetPRDevelopmentConversation(
	context.Context,
	string,
) (eventing.PRDevelopmentConversation, error) {
	store.conversationCalls++
	return store.conversation, nil
}

func (store *threadContextLoaderStore) AppendPRDevelopmentLedgerCheckpoint(
	_ context.Context,
	input eventing.PRDevelopmentLedgerCheckpointAppend,
) (eventing.PRDevelopmentLedgerCheckpoint, bool, error) {
	store.appendCalls = append(store.appendCalls, input)
	checkpoint := eventing.PRDevelopmentLedgerCheckpoint{
		ThreadID:       store.snapshot.Thread.ID,
		Generation:     len(store.snapshot.Ledger.Checkpoints),
		ThroughOrdinal: input.ThroughOrdinal,
		SourceDigest:   input.SourceDigest,
		Summary:        input.Summary,
		CompactorID:    input.CompactorID,
		PromptDigest:   input.PromptDigest,
	}
	if store.doNotAdvanceCheckpoint {
		return checkpoint, false, nil
	}
	store.snapshot.Ledger.Checkpoints = append(
		store.snapshot.Ledger.Checkpoints,
		checkpoint,
	)
	store.snapshot.Ledger.LatestCheckpoint = &store.snapshot.Ledger.Checkpoints[len(store.snapshot.Ledger.Checkpoints)-1]
	return checkpoint, true, nil
}

type threadContextLoaderAgent struct {
	responses []map[string]any
	errors    []error
	requests  []workflows.AgentRequest
}

func (agent *threadContextLoaderAgent) RunAgent(
	_ context.Context,
	request workflows.AgentRequest,
) (map[string]any, error) {
	agent.requests = append(agent.requests, request)
	index := len(agent.requests) - 1
	if index < len(agent.errors) && agent.errors[index] != nil {
		return nil, agent.errors[index]
	}
	if index >= len(agent.responses) {
		return nil, errors.New("unexpected compactor request")
	}
	return agent.responses[index], nil
}
