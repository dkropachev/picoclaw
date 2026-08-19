package localci

import (
	"bytes"
	"context"
	"fmt"
)

type discoveryIndexRecord struct {
	Version         int    `json:"version"`
	Key             string `json:"key"`
	BaselineDigest  string `json:"baseline_digest"`
	CandidateDigest string `json:"candidate_digest"`
	EffectiveDigest string `json:"effective_digest"`
	Changed         bool   `json:"changed"`
	Digest          string `json:"digest"`
}

func discoveryCacheKey(parentManifestDigest, candidateManifestDigest string) (string, error) {
	if !validDigest(parentManifestDigest) || !validDigest(candidateManifestDigest) {
		return "", fmt.Errorf("%w: invalid discovery manifest identity", ErrInvalid)
	}
	return digestParts(
		"picoclaw-local-ci-discovery-key-v1",
		[]byte(parentManifestDigest),
		[]byte(candidateManifestDigest),
		[]byte(DiscoveryVersion),
		[]byte(fmt.Sprintf("%d", PlanVersion)),
	), nil
}

func (store *FileEvidenceStore) PutResolvedPlan(
	ctx context.Context,
	key string,
	resolved ResolvedPlan,
) error {
	if !validDigest(key) {
		return fmt.Errorf("%w: invalid discovery cache key", ErrInvalid)
	}
	expected, err := resolveDiscoveredPlans(resolved.Baseline, resolved.Candidate)
	if err != nil {
		return err
	}
	if expected.Changed != resolved.Changed || expected.Effective.Digest != resolved.Effective.Digest {
		return fmt.Errorf("%w: inconsistent resolved local CI plan", ErrInvalid)
	}
	for _, plan := range []Plan{expected.Baseline, expected.Candidate, expected.Effective} {
		if err = store.PutPlan(ctx, plan); err != nil {
			return err
		}
	}
	record, err := finalizeDiscoveryIndex(discoveryIndexRecord{
		Key:             key,
		BaselineDigest:  expected.Baseline.Digest,
		CandidateDigest: expected.Candidate.Digest,
		EffectiveDigest: expected.Effective.Digest,
		Changed:         expected.Changed,
	})
	if err != nil {
		return err
	}
	return store.putImmutable(ctx, "discovery", key, record)
}

func (store *FileEvidenceStore) GetResolvedPlan(
	ctx context.Context,
	key string,
) (ResolvedPlan, bool, error) {
	if !validDigest(key) {
		return ResolvedPlan{}, false, fmt.Errorf("%w: invalid discovery cache key", ErrInvalid)
	}
	var record discoveryIndexRecord
	found, err := store.readObject(ctx, "discovery", key, &record)
	if err != nil || !found {
		return ResolvedPlan{}, found, err
	}
	normalized, err := finalizeDiscoveryIndex(record)
	if err != nil || normalized.Digest != record.Digest ||
		normalized.Key != key ||
		!bytes.Equal(mustJSON(normalized), mustJSON(record)) {
		return ResolvedPlan{}, false, ErrEvidenceCorrupt
	}
	baseline, found, err := store.GetPlan(ctx, normalized.BaselineDigest)
	if err != nil || !found {
		return ResolvedPlan{}, false, ErrEvidenceCorrupt
	}
	candidate, found, err := store.GetPlan(ctx, normalized.CandidateDigest)
	if err != nil || !found {
		return ResolvedPlan{}, false, ErrEvidenceCorrupt
	}
	effective, found, err := store.GetPlan(ctx, normalized.EffectiveDigest)
	if err != nil || !found {
		return ResolvedPlan{}, false, ErrEvidenceCorrupt
	}
	resolved, err := resolveDiscoveredPlans(baseline, candidate)
	if err != nil || resolved.Changed != normalized.Changed ||
		resolved.Effective.Digest != effective.Digest {
		return ResolvedPlan{}, false, ErrEvidenceCorrupt
	}
	return resolved, true, nil
}

func finalizeDiscoveryIndex(record discoveryIndexRecord) (discoveryIndexRecord, error) {
	record.Version = EvidenceVersion
	record.Digest = ""
	if !validDigest(record.Key) || !validDigest(record.BaselineDigest) ||
		!validDigest(record.CandidateDigest) || !validDigest(record.EffectiveDigest) {
		return discoveryIndexRecord{}, fmt.Errorf("%w: invalid discovery index", ErrInvalid)
	}
	digest, err := digestJSON("picoclaw-local-ci-discovery-index-v1", record)
	if err != nil {
		return discoveryIndexRecord{}, err
	}
	record.Digest = digest
	return record, nil
}
