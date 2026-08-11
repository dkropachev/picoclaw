package prdevelopment

import (
	"strings"
	"testing"

	"github.com/sipeed/picoclaw/pkg/eventing"
)

func TestPRDevelopmentPublicationDecisionIdentityIsCanonicalAndStable(t *testing.T) {
	t.Parallel()
	key := publicationDecisionIdentityTestKey()
	canonical, err := canonicalPRDevelopmentPublicationDecisionKey(key)
	if err != nil {
		t.Fatal(err)
	}
	const wantCanonical = `{"decision_point":"pr_development.before_push","policy_revision":"sha256:4444444444444444444444444444444444444444444444444444444444444444","provider_observation_hash":"5555555555555555555555555555555555555555555555555555555555555555","publication_id":"pdpub_11111111111111111111111111111111","review_ledger_entry_hash":"3333333333333333333333333333333333333333333333333333333333333333","review_ledger_entry_id":"pdle_22222222222222222222222222222222","subject_revision":"sha256:6666666666666666666666666666666666666666666666666666666666666666"}`
	if canonical != wantCanonical {
		t.Fatalf("canonical decision key = %q, want %q", canonical, wantCanonical)
	}
	runID, err := prDevelopmentPublicationRunID(key)
	if err != nil {
		t.Fatal(err)
	}
	const wantRunID = "wr_7af24a0d92f80f612c93a919a23ed707"
	if runID != wantRunID {
		t.Fatalf("publication run ID = %q, want %q", runID, wantRunID)
	}
	// Case-chat versions, scheduler claims, and publication status are absent
	// from the input type, so every reclaim of this exact key necessarily
	// recovers the same identity.
	replayed, err := prDevelopmentPublicationRunID(key)
	if err != nil || replayed != runID {
		t.Fatalf("replayed run ID = (%q, %v), want %q", replayed, err, runID)
	}
}

func TestPRDevelopmentPublicationDecisionIdentityBindsEveryKeyField(t *testing.T) {
	t.Parallel()
	base := publicationDecisionIdentityTestKey()
	baseRunID, err := prDevelopmentPublicationRunID(base)
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name   string
		mutate func(*eventing.PRDevelopmentPublicationDecisionKey)
	}{
		{name: "publication", mutate: func(key *eventing.PRDevelopmentPublicationDecisionKey) {
			key.PublicationID = "pdpub_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
		}},
		{name: "review entry", mutate: func(key *eventing.PRDevelopmentPublicationDecisionKey) {
			key.ReviewLedgerEntryID = "pdle_bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
		}},
		{name: "review hash", mutate: func(key *eventing.PRDevelopmentPublicationDecisionKey) {
			key.ReviewLedgerEntryHash = strings.Repeat("c", 64)
		}},
		{name: "policy", mutate: func(key *eventing.PRDevelopmentPublicationDecisionKey) {
			key.PolicyRevision = "sha256:" + strings.Repeat("d", 64)
		}},
		{name: "subject", mutate: func(key *eventing.PRDevelopmentPublicationDecisionKey) {
			key.SubjectRevision = "sha256:" + strings.Repeat("e", 64)
		}},
		{name: "provider", mutate: func(key *eventing.PRDevelopmentPublicationDecisionKey) {
			key.ProviderObservationHash = strings.Repeat("f", 64)
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			changed := base
			test.mutate(&changed)
			runID, runErr := prDevelopmentPublicationRunID(changed)
			if runErr != nil || runID == baseRunID {
				t.Fatalf("changed run ID = (%q, %v), base %q", runID, runErr, baseRunID)
			}
		})
	}
}

func TestPRDevelopmentPublicationDecisionIdentityRejectsNoncanonicalValues(t *testing.T) {
	t.Parallel()
	base := publicationDecisionIdentityTestKey()
	tests := []struct {
		name   string
		mutate func(*eventing.PRDevelopmentPublicationDecisionKey)
	}{
		{name: "trimmed publication", mutate: func(key *eventing.PRDevelopmentPublicationDecisionKey) {
			key.PublicationID = " " + key.PublicationID
		}},
		{name: "uppercase review ID", mutate: func(key *eventing.PRDevelopmentPublicationDecisionKey) {
			key.ReviewLedgerEntryID = strings.ToUpper(key.ReviewLedgerEntryID)
		}},
		{name: "short review hash", mutate: func(key *eventing.PRDevelopmentPublicationDecisionKey) {
			key.ReviewLedgerEntryHash = key.ReviewLedgerEntryHash[:63]
		}},
		{name: "bare policy hash", mutate: func(key *eventing.PRDevelopmentPublicationDecisionKey) {
			key.PolicyRevision = strings.TrimPrefix(key.PolicyRevision, "sha256:")
		}},
		{name: "uppercase subject hash", mutate: func(key *eventing.PRDevelopmentPublicationDecisionKey) {
			key.SubjectRevision = strings.ToUpper(key.SubjectRevision)
		}},
		{name: "provider whitespace", mutate: func(key *eventing.PRDevelopmentPublicationDecisionKey) {
			key.ProviderObservationHash += " "
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			changed := base
			test.mutate(&changed)
			if _, err := canonicalPRDevelopmentPublicationDecisionKey(changed); err == nil {
				t.Fatal("canonicalPRDevelopmentPublicationDecisionKey() error = nil")
			}
		})
	}
}

func publicationDecisionIdentityTestKey() eventing.PRDevelopmentPublicationDecisionKey {
	return eventing.PRDevelopmentPublicationDecisionKey{
		PublicationID:           "pdpub_11111111111111111111111111111111",
		ReviewLedgerEntryID:     "pdle_22222222222222222222222222222222",
		ReviewLedgerEntryHash:   strings.Repeat("3", 64),
		PolicyRevision:          "sha256:" + strings.Repeat("4", 64),
		ProviderObservationHash: strings.Repeat("5", 64),
		SubjectRevision:         "sha256:" + strings.Repeat("6", 64),
	}
}
