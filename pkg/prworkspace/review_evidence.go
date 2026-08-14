package prworkspace

import "context"

// ReviewEvidence is immutable, bounded input for an isolated reviewer. The
// provider revision fences the diff to the exact workspace head.
type ReviewEvidence struct {
	ProviderRevision string `json:"provider_revision"`
	BaseSHA          string `json:"base_sha"`
	HeadSHA          string `json:"head_sha"`
	UnifiedDiff      string `json:"unified_diff"`
}

// ReviewEvidenceLoader owns only read authority. It must re-observe the
// provider around evidence acquisition and fail if the expected revision
// changes; it grants no model or provider-write capability.
type ReviewEvidenceLoader interface {
	LoadReviewEvidence(context.Context, ProviderSnapshot) (ReviewEvidence, error)
}
