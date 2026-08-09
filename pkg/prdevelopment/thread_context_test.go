package prdevelopment

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/sipeed/picoclaw/pkg/eventing"
)

const threadContextThreadID = "pdt_thread_context_private_identity"

func TestSelectDevelopmentProviderCaseLinksKeepsSelectedAndNewestInOrdinalOrder(
	t *testing.T,
) {
	snapshot := threadContextSnapshot(40, 2)

	links, err := selectDevelopmentProviderCaseLinks(snapshot)
	if err != nil {
		t.Fatalf("selectDevelopmentProviderCaseLinks() error = %v", err)
	}
	if len(links) != maximumAIProviderCases {
		t.Fatalf("selected links = %d, want %d", len(links), maximumAIProviderCases)
	}
	wantOrdinals := append([]int{2}, integerRange(9, 40)...)
	for index, link := range links {
		if link.Ordinal != wantOrdinals[index] {
			t.Fatalf("selected link %d ordinal = %d, want %d", index, link.Ordinal, wantOrdinals[index])
		}
		if index > 0 && !links[index-1].LinkedAt.After(link.LinkedAt) {
			t.Fatalf("test setup did not oppose timestamp order at index %d", index)
		}
	}

	provider, err := projectDevelopmentProviderThread(developmentThreadAIContextInput{
		Snapshot:      snapshot,
		ProviderCases: threadContextProviderEvidence(snapshot, links),
	})
	if err != nil {
		t.Fatalf("projectDevelopmentProviderThread() error = %v", err)
	}
	if provider.SelectedOrdinal != 2 || provider.IncludedCases != maximumAIProviderCases ||
		provider.OmittedCases != 8 {
		t.Fatalf("provider counts = %+v", provider)
	}
	for index, review := range provider.Reviews {
		if review.ThreadOrdinal != wantOrdinals[index] {
			t.Fatalf("review %d ordinal = %d, want %d", index, review.ThreadOrdinal, wantOrdinals[index])
		}
	}
	wantRanges := []developmentOrdinalRange{{First: 0, Last: 1}, {First: 3, Last: 8}}
	if !equalDevelopmentOrdinalRanges(provider.OmittedRanges, wantRanges) {
		t.Fatalf("omitted ranges = %+v, want %+v", provider.OmittedRanges, wantRanges)
	}
}

func TestProjectDevelopmentProviderThreadOmitsWholeReviewsWithoutTruncatingFeedback(
	t *testing.T,
) {
	snapshot := threadContextSnapshot(4, 1)
	links := snapshot.Thread.Cases
	evidence := threadContextProviderEvidence(snapshot, links)
	evidence[0].Case.Feedback = "old review: keep this exact text"
	evidence[1].Case.Feedback = "selected review: keep this exact text"
	evidence[2].Case.Feedback = "new review: keep this exact text"
	evidence[3].Case.Feedback = strings.Repeat("whole-review-must-be-omitted;", 5_000)

	provider, err := projectDevelopmentProviderThread(developmentThreadAIContextInput{
		Snapshot:      snapshot,
		ProviderCases: evidence,
	})
	if err != nil {
		t.Fatalf("projectDevelopmentProviderThread() error = %v", err)
	}
	if provider.IncludedCases != 3 || provider.OmittedCases != 1 {
		t.Fatalf("provider counts = %+v", provider)
	}
	if want := []developmentOrdinalRange{{First: 3, Last: 3}}; !equalDevelopmentOrdinalRanges(provider.OmittedRanges, want) {
		t.Fatalf("omitted ranges = %+v, want %+v", provider.OmittedRanges, want)
	}
	wantFeedback := []string{
		"old review: keep this exact text",
		"selected review: keep this exact text",
		"new review: keep this exact text",
	}
	for index, review := range provider.Reviews {
		if review.ThreadOrdinal != index || review.Feedback != wantFeedback[index] {
			t.Fatalf("review %d = %+v, want intact feedback %q", index, review, wantFeedback[index])
		}
	}

	evidence[1].Case.Feedback = strings.Repeat("selected-review-cannot-be-truncated;", 5_000)
	_, err = projectDevelopmentProviderThread(developmentThreadAIContextInput{
		Snapshot:      snapshot,
		ProviderCases: evidence,
	})
	if !errors.Is(err, ErrAIContextCompactionRequired) {
		t.Fatalf("selected oversized review error = %v, want ErrAIContextCompactionRequired", err)
	}
}

func TestProjectDevelopmentLedgerUsesLatestCheckpointAndRawSuffix(t *testing.T) {
	snapshot := threadContextSnapshot(1, 0)
	snapshot.Ledger.Entries = threadContextLedgerEntries(6, "attempt summary")
	snapshot.Ledger.Entries[4].Summary = "raw suffix attempt"
	snapshot.Ledger.Entries[5].Summary = "raw suffix review"
	snapshot.Ledger.Entries[4].CaseOrdinal = 7
	snapshot.Ledger.Entries[5].CaseOrdinal = 7
	line := 37
	snapshot.Ledger.Entries[5].Findings = []eventing.PRDevelopmentLedgerReviewFinding{{
		Severity:       eventing.ReviewSeverityHigh,
		Title:          "boundary race",
		File:           "worker.go",
		Line:           &line,
		Message:        "the reservation may be released twice",
		Evidence:       "release follows both terminal paths",
		Impact:         "the retained branch can lose its owner",
		Recommendation: "fence the terminal transition",
		Validation:     "run the targeted race test",
	}}
	checkpoint := eventing.PRDevelopmentLedgerCheckpoint{
		ThreadID:       snapshot.Thread.ID,
		Generation:     0,
		ThroughOrdinal: 3,
		SourceDigest:   snapshot.Ledger.Entries[3].EntryHash,
		Summary:        "attempts zero and one were compacted",
	}
	snapshot.Ledger.LatestCheckpoint = &checkpoint

	ledger, err := projectDevelopmentLedger(snapshot)
	if err != nil {
		t.Fatalf("projectDevelopmentLedger() error = %v", err)
	}
	if ledger.TotalEntries != 6 || ledger.Checkpoint == nil {
		t.Fatalf("ledger projection = %+v", ledger)
	}
	if ledger.Checkpoint.ThroughOrdinal != 3 || ledger.Checkpoint.CoveredEntries != 4 ||
		ledger.Checkpoint.CoveredAttempts != 2 ||
		ledger.Checkpoint.Summary != checkpoint.Summary {
		t.Fatalf("checkpoint projection = %+v", ledger.Checkpoint)
	}
	if len(ledger.Entries) != 2 || ledger.Entries[0].Ordinal != 4 ||
		ledger.Entries[0].OwnerCaseOrdinal != 7 ||
		ledger.Entries[0].Description != "raw suffix attempt" ||
		ledger.Entries[1].Ordinal != 5 ||
		ledger.Entries[1].OwnerCaseOrdinal != 7 ||
		ledger.Entries[1].Description != "raw suffix review" {
		t.Fatalf("raw ledger suffix = %+v", ledger.Entries)
	}
	if len(ledger.Entries[1].Findings) != 1 ||
		ledger.Entries[1].Findings[0].Line == nil ||
		*ledger.Entries[1].Findings[0].Line != line {
		t.Fatalf("review findings = %+v", ledger.Entries[1].Findings)
	}
}

func TestDevelopmentThreadAIContextIsDeterministicAndExcludesPrivateEvidence(
	t *testing.T,
) {
	snapshot := threadContextSnapshot(1, 0)
	snapshot.Thread.Identity = eventing.PRDevelopmentThreadIdentity{
		Provider:       "__private_provider__",
		ProviderOrigin: "__private_origin__",
		PullAuthorID:   "__private_author_id__",
		RepositoryID:   "__private_repository_id__",
		PullRequestID:  "__private_pull_id__",
	}
	snapshot.Thread.IdentityHash = "__private_identity_hash__"
	snapshot.Thread.Cases[0].PreviousHash = "__private_link_previous_hash__"
	snapshot.Thread.Cases[0].LinkHash = "__private_link_hash__"
	snapshot.Ledger.Entries = threadContextLedgerEntries(2, "visible account")
	snapshot.Ledger.Entries[0].AttemptID = "__private_attempt_id__"
	snapshot.Ledger.Entries[0].Tree = "__private_tree__"
	snapshot.Ledger.Entries[0].CIPlanDigest = "__private_ci_plan__"
	snapshot.Ledger.Entries[0].CIResultDigest = "__private_ci_result__"
	snapshot.Ledger.Entries[0].FenceHash = "__private_fence_hash__"
	snapshot.Ledger.Entries[0].PreviousHash = "__private_previous_hash__"
	snapshot.Ledger.Entries[0].EntryHash = "__private_entry_hash__"

	evidence := threadContextProviderEvidence(snapshot, snapshot.Thread.Cases)
	evidence[0].Case.PRDevelopmentCaptureIdentity = eventing.PRDevelopmentCaptureIdentity{
		EventID:     "__private_event_id__",
		DispatchID:  "__private_dispatch_id__",
		RunID:       "__private_run_id__",
		WorkflowRef: "__private_workflow_ref__",
		Connector:   "__private_connector__",
	}
	evidence[0].Case.PullURL = "__private_pull_url__"
	evidence[0].Case.ReviewID = "__private_review_id__"
	evidence[0].Case.TriggerReviewNodeID = "__private_review_node_id__"
	evidence[0].Case.ReviewURL = "__private_review_url__"
	conversation := eventing.PRDevelopmentConversation{
		CaseID:  snapshot.Thread.Cases[0].CaseID,
		Version: 1,
		Messages: []eventing.PRDevelopmentMessage{{
			ID:      "__private_message_id__",
			CaseID:  snapshot.Thread.Cases[0].CaseID,
			Ordinal: 0,
			Role:    eventing.PRDevelopmentMessageUser,
			Content: "visible user steering",
		}},
	}
	input := developmentThreadAIContextInput{
		Snapshot:      snapshot,
		ProviderCases: evidence,
		Conversation:  conversation,
	}

	first, err := developmentThreadAIContext(input)
	if err != nil {
		t.Fatalf("developmentThreadAIContext() error = %v", err)
	}
	second, err := developmentThreadAIContext(input)
	if err != nil {
		t.Fatalf("developmentThreadAIContext() second error = %v", err)
	}
	if first != second {
		t.Fatal("developmentThreadAIContext() is not deterministic")
	}
	if !json.Valid([]byte(first)) {
		t.Fatalf("developmentThreadAIContext() returned invalid JSON: %s", first)
	}
	if strings.Contains(first, `"provider_review_ordinal"`) {
		t.Fatalf("context used ambiguous provider review ordinal: %s", first)
	}
	for _, secret := range []string{
		"__private_provider__",
		"__private_origin__",
		"__private_author_id__",
		"__private_repository_id__",
		"__private_pull_id__",
		"__private_identity_hash__",
		"__private_link_previous_hash__",
		"__private_link_hash__",
		"__private_attempt_id__",
		"__private_tree__",
		"__private_ci_plan__",
		"__private_ci_result__",
		"__private_fence_hash__",
		"__private_previous_hash__",
		"__private_entry_hash__",
		"__private_event_id__",
		"__private_dispatch_id__",
		"__private_run_id__",
		"__private_workflow_ref__",
		"__private_connector__",
		"__private_pull_url__",
		"__private_review_id__",
		"__private_review_node_id__",
		"__private_review_url__",
		"__private_message_id__",
	} {
		if strings.Contains(first, secret) {
			t.Fatalf("context leaked private value %q: %s", secret, first)
		}
	}
	for _, visible := range []string{
		developmentThreadContextFormat,
		`"owner_case_ordinal"`,
		"visible account",
		"visible user steering",
		"untrusted historical data",
	} {
		if !strings.Contains(first, visible) {
			t.Fatalf("context omitted expected value %q: %s", visible, first)
		}
	}
}

func TestProjectDevelopmentLedgerRequiresCompactionForUnboundedSuffix(t *testing.T) {
	t.Run("entry count", func(t *testing.T) {
		snapshot := threadContextSnapshot(1, 0)
		snapshot.Ledger.Entries = threadContextLedgerEntries(
			maximumAILedgerSuffixEntries+1,
			"bounded summary",
		)

		_, err := projectDevelopmentLedger(snapshot)
		if !errors.Is(err, ErrAIContextCompactionRequired) {
			t.Fatalf("projectDevelopmentLedger() error = %v, want ErrAIContextCompactionRequired", err)
		}
	})

	t.Run("byte size", func(t *testing.T) {
		snapshot := threadContextSnapshot(1, 0)
		snapshot.Ledger.Entries = threadContextLedgerEntries(
			maximumAILedgerSuffixEntries,
			strings.Repeat("x", eventing.MaxPRDevelopmentLedgerSummaryBytes),
		)

		_, err := projectDevelopmentLedger(snapshot)
		if !errors.Is(err, ErrAIContextCompactionRequired) {
			t.Fatalf("projectDevelopmentLedger() error = %v, want ErrAIContextCompactionRequired", err)
		}
	})
}

func TestDevelopmentThreadAIContextTrimsOldestConversationMessages(t *testing.T) {
	snapshot := threadContextSnapshot(1, 0)
	evidence := threadContextProviderEvidence(snapshot, snapshot.Thread.Cases)
	conversation := eventing.PRDevelopmentConversation{
		CaseID: snapshot.Thread.Cases[0].CaseID,
	}
	for ordinal := 0; ordinal < maximumAITranscript; ordinal++ {
		conversation.Messages = append(conversation.Messages, eventing.PRDevelopmentMessage{
			Ordinal: ordinal,
			Role:    eventing.PRDevelopmentMessageUser,
			Content: fmt.Sprintf("message-%02d:%s", ordinal, strings.Repeat("x", 16<<10)),
		})
	}
	conversation.Version = int64(len(conversation.Messages))

	encoded, err := developmentThreadAIContext(developmentThreadAIContextInput{
		Snapshot:      snapshot,
		ProviderCases: evidence,
		Conversation:  conversation,
	})
	if err != nil {
		t.Fatalf("developmentThreadAIContext() error = %v", err)
	}
	if len(encoded) > maximumAIContextBytes {
		t.Fatalf("context bytes = %d, want <= %d", len(encoded), maximumAIContextBytes)
	}
	var value developmentThreadContextValue
	if err := json.Unmarshal([]byte(encoded), &value); err != nil {
		t.Fatalf("decode context: %v", err)
	}
	if value.OmittedMessages == 0 ||
		value.OmittedMessages+len(value.Conversation) != maximumAITranscript {
		t.Fatalf(
			"omitted messages = %d, retained = %d, original = %d",
			value.OmittedMessages,
			len(value.Conversation),
			maximumAITranscript,
		)
	}
	if len(value.Conversation) == 0 ||
		!strings.HasPrefix(
			value.Conversation[0].Content,
			fmt.Sprintf("message-%02d:", value.OmittedMessages),
		) ||
		!strings.HasPrefix(
			value.Conversation[len(value.Conversation)-1].Content,
			fmt.Sprintf("message-%02d:", maximumAITranscript-1),
		) {
		t.Fatalf("conversation did not retain a newest contiguous suffix")
	}
}

func threadContextSnapshot(totalCases, selectedOrdinal int) eventing.PRDevelopmentContextSnapshot {
	base := time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC)
	cases := make([]eventing.PRDevelopmentThreadCaseLink, 0, totalCases)
	for ordinal := 0; ordinal < totalCases; ordinal++ {
		cases = append(cases, eventing.PRDevelopmentThreadCaseLink{
			CaseID:   fmt.Sprintf("pdc_thread_context_%03d", ordinal),
			Ordinal:  ordinal,
			LinkedAt: base.Add(time.Duration(totalCases-ordinal) * time.Hour),
		})
	}
	return eventing.PRDevelopmentContextSnapshot{
		SelectedOrdinal: selectedOrdinal,
		Thread: eventing.PRDevelopmentThread{
			ID:        threadContextThreadID,
			Kind:      eventing.PRDevelopmentThreadProvider,
			CaseCount: totalCases,
			Cases:     cases,
		},
		Ledger: eventing.PRDevelopmentLedger{ThreadID: threadContextThreadID},
	}
}

func threadContextProviderEvidence(
	snapshot eventing.PRDevelopmentContextSnapshot,
	links []eventing.PRDevelopmentThreadCaseLink,
) []developmentProviderCaseEvidence {
	base := time.Date(2026, 8, 9, 14, 0, 0, 0, time.UTC)
	evidence := make([]developmentProviderCaseEvidence, 0, len(links))
	for _, link := range links {
		ordinal := link.Ordinal
		evidence = append(evidence, developmentProviderCaseEvidence{
			Link: link,
			Case: eventing.PRDevelopmentCase{
				ID: link.CaseID,
				PRDevelopmentCaptureInput: eventing.PRDevelopmentCaptureInput{
					PullState:            eventing.PRDevelopmentPullOpen,
					BaseRepository:       "owner/base",
					BaseRef:              "main",
					BaseSHA:              fmt.Sprintf("base-%03d", ordinal),
					HeadRepository:       "owner/head",
					HeadRef:              "feature",
					HeadSHA:              fmt.Sprintf("head-%03d", ordinal),
					ReviewAuthor:         fmt.Sprintf("reviewer-%03d", ordinal),
					SubmittedReviewState: eventing.PRDevelopmentReviewChangesRequested,
					CurrentReviewState:   eventing.PRDevelopmentReviewChangesRequested,
					ReviewCommitSHA:      fmt.Sprintf("review-%03d", ordinal),
					ReviewSubmittedAt:    base.Add(-time.Duration(ordinal) * time.Hour),
					Feedback:             fmt.Sprintf("feedback-%03d", ordinal),
				},
				CreatedAt: base.Add(-time.Duration(ordinal) * time.Minute),
			},
		})
	}
	return evidence
}

func threadContextLedgerEntries(count int, summary string) []eventing.PRDevelopmentLedgerEntry {
	base := time.Date(2026, 8, 9, 16, 0, 0, 0, time.UTC)
	entries := make([]eventing.PRDevelopmentLedgerEntry, 0, count)
	for ordinal := 0; ordinal < count; ordinal++ {
		kind := eventing.PRDevelopmentLedgerAttempt
		outcome := eventing.PRDevelopmentLedgerReviewOutcome("")
		commit := fmt.Sprintf("commit-%03d", ordinal/2)
		if ordinal%2 == 1 {
			kind = eventing.PRDevelopmentLedgerReview
			outcome = eventing.PRDevelopmentLedgerReviewPassed
			commit = ""
		}
		entries = append(entries, eventing.PRDevelopmentLedgerEntry{
			ThreadID:      threadContextThreadID,
			Ordinal:       ordinal,
			Kind:          kind,
			FenceOrdinal:  ordinal / 2,
			CaseOrdinal:   0,
			Commit:        commit,
			Summary:       summary,
			ReviewOutcome: outcome,
			EntryHash:     fmt.Sprintf("entry-hash-%03d", ordinal),
			CreatedAt:     base.Add(-time.Duration(ordinal) * time.Minute),
		})
	}
	return entries
}

func integerRange(first, end int) []int {
	values := make([]int, 0, end-first)
	for value := first; value < end; value++ {
		values = append(values, value)
	}
	return values
}

func equalDevelopmentOrdinalRanges(left, right []developmentOrdinalRange) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}
