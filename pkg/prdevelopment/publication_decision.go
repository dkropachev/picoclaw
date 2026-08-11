package prdevelopment

import (
	sharedattention "github.com/sipeed/picoclaw/pkg/attention"
	"github.com/sipeed/picoclaw/pkg/eventing"
)

// canonicalPRDevelopmentPublicationDecision is the immutable semantic identity
// shared by publication-gate admission and the case-owned attention bridge.
// Scheduling claims, case-chat revisions, and publication lifecycle state are
// deliberately absent so an exact decision keeps one run across reclaims.
type canonicalPRDevelopmentPublicationDecision struct {
	DecisionPoint           string `json:"decision_point"`
	PolicyRevision          string `json:"policy_revision"`
	ProviderObservationHash string `json:"provider_observation_hash"`
	PublicationID           string `json:"publication_id"`
	ReviewLedgerEntryHash   string `json:"review_ledger_entry_hash"`
	ReviewLedgerEntryID     string `json:"review_ledger_entry_id"`
	SubjectRevision         string `json:"subject_revision"`
}

func canonicalPRDevelopmentPublicationDecisionKey(
	key eventing.PRDevelopmentPublicationDecisionKey,
) (string, error) {
	if !validDevelopmentID(key.PublicationID, "pdpub_") ||
		!validDevelopmentID(key.ReviewLedgerEntryID, "pdle_") ||
		!validControllerSHA256(key.ReviewLedgerEntryHash) ||
		!validAttentionRevision(key.PolicyRevision) ||
		!validAttentionRevision(key.SubjectRevision) ||
		!validControllerSHA256(key.ProviderObservationHash) {
		return "", ErrUnavailable
	}
	return sharedattention.CanonicalDecisionKey(
		canonicalPRDevelopmentPublicationDecision{
			DecisionPoint:           eventing.PRDevelopmentPublicationDecisionBeforePush,
			PolicyRevision:          key.PolicyRevision,
			ProviderObservationHash: key.ProviderObservationHash,
			PublicationID:           key.PublicationID,
			ReviewLedgerEntryHash:   key.ReviewLedgerEntryHash,
			ReviewLedgerEntryID:     key.ReviewLedgerEntryID,
			SubjectRevision:         key.SubjectRevision,
		},
	)
}

func prDevelopmentPublicationRunID(
	key eventing.PRDevelopmentPublicationDecisionKey,
) (string, error) {
	canonical, err := canonicalPRDevelopmentPublicationDecisionKey(key)
	if err != nil {
		return "", err
	}
	return sharedattention.RunIDForDecisionKey(canonical)
}
