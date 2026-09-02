package repoaudit

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

const (
	// DeduplicationScoringCandidateLimit is deliberately independent of the
	// configured shortlist limit. Every candidate in the frozen universe is
	// scored, in bounded calls of at most this size.
	DeduplicationScoringCandidateLimit = 16
	DeduplicationMaximumInputBytes     = 1 << 20
	DeduplicationMaximumShortlist      = 20
	DeduplicationDefaultThreshold      = 90
	DeduplicationDefaultCandidateLimit = 4
	DeduplicationAttemptLimit          = 3
	DeduplicationConcurrency           = 4

	maxDeduplicationExplanationBytes = 4096
)

// AcquireDeduplicationSlot waits for one of four workspace-wide model-call
// slots. The OS lock is released on process loss as well as explicit release.
func (s Store) AcquireDeduplicationSlot(ctx context.Context) (func(), error) {
	if s.broker != nil {
		return s.brokerAcquireNamedLease(ctx, reviewLeaseDeduplicationSlot, reviewNamedLeaseRequest{})
	}
	if err := s.localProviderError(); err != nil {
		return nil, err
	}
	if ctx == nil {
		ctx = context.Background()
	}
	for {
		for slot := 0; slot < DeduplicationConcurrency; slot++ {
			lockPath, lockPathErr := repositoryReviewLockPath(
				s.root,
				fmt.Sprintf("deduplication-slot-%02d.lock", slot),
			)
			if lockPathErr != nil {
				return nil, lockPathErr
			}
			release, acquired, err := tryLockRepositoryReviewIssueFile(lockPath)
			if err != nil {
				return nil, err
			}
			if acquired {
				return release, nil
			}
		}
		timer := time.NewTimer(issueGenerationSlotRetryInterval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, ctx.Err()
		case <-timer.C:
		}
	}
}

const DeduplicationScoringInstructions = `Compare the original diagnosis with every supplied candidate diagnosis using only this payload. Return exactly one integer score from 0 through 100 and one explanation for each supplied opaque candidate ID. A score of 100 means an equivalent statement of the same defect. Scores 90-99 require the same mechanism, trigger, invariant, and outcome despite wording or line differences. Scores 70-89 are probably related but materially ambiguous. Scores 40-69 describe the same area or symptom with a different or unclear mechanism. Scores 0-39 are distinct or causally conflicting. Do not use source files, tools, internet, history, cache, configuration, or external knowledge.`

const DeduplicationJudgeInstructions = `Using only the original diagnosis and supplied shortlisted diagnoses, return exactly either {"decision":"new"} or {"decision":"duplicate","candidate_id":"one supplied opaque ID"}. Do not use source files, tools, internet, history, cache, configuration, or external knowledge.`

// NormalizeDeduplicationSymbol produces the symbol component of an admission
// bucket. Location lines are intentionally absent from the bucket. The empty
// string is a valid result for legacy findings which did not persist a symbol.
func NormalizeDeduplicationSymbol(symbol string) string {
	var normalized strings.Builder
	normalized.Grow(len(symbol))
	separator := false
	for _, character := range strings.ToLower(symbol) {
		switch {
		case unicode.IsSpace(character):
			// Whitespace and receiver/pointer syntax are not identity.
			continue
		case character == '(' || character == ')' || character == '*' || character == '&':
			continue
		case character == '.' || character == ':' || character == '/' || character == '\\' ||
			character == '#' || character == '-' || character == '>' || character == '·':
			separator = true
			continue
		}
		if separator && normalized.Len() > 0 {
			value := normalized.String()
			if value[len(value)-1] != '.' {
				normalized.WriteByte('.')
			}
		}
		separator = false
		normalized.WriteRune(character)
	}
	return strings.Trim(normalized.String(), ".")
}

// DeduplicationAdmissionBucket is stable across line changes but deliberately
// changes across campaigns, exact paths, blobs, or normalized symbols.
func DeduplicationAdmissionBucket(campaignID string, file FileRef, symbol string) (string, error) {
	campaignID = strings.TrimSpace(campaignID)
	pathValue := strings.TrimSpace(file.Path)
	blobSHA := strings.ToLower(strings.TrimSpace(file.BlobSHA))
	if campaignID == "" || pathValue == "" || blobSHA == "" {
		return "", errors.New("campaign, path, and blob SHA are required for deduplication")
	}
	if !utf8.ValidString(campaignID) || !utf8.ValidString(pathValue) ||
		strings.ContainsRune(campaignID, 0) || strings.ContainsRune(pathValue, 0) {
		return "", errors.New("invalid deduplication admission identity")
	}
	return stableID(
		"rdb_", campaignID, pathValue, blobSHA, NormalizeDeduplicationSymbol(symbol),
	), nil
}

// DeduplicationDiagnosis is the complete diagnosis-only model payload. It has
// no source content, file location, provider configuration, tools, or history.
type DeduplicationDiagnosis struct {
	Severity   string     `json:"severity"`
	Title      string     `json:"title"`
	Symbol     string     `json:"symbol,omitempty"`
	Message    string     `json:"message,omitempty"`
	Evidence   string     `json:"evidence"`
	Impact     string     `json:"impact"`
	Validation Validation `json:"validation"`
	MatchHints MatchHints `json:"match_hints,omitempty"`
	FixEffort  FixEffort  `json:"fix_effort,omitempty"`
}

type DeduplicationCandidateSnapshot struct {
	ID              string                 `json:"-"`
	Version         int64                  `json:"-"`
	CreationOrdinal uint64                 `json:"-"`
	Diagnosis       DeduplicationDiagnosis `json:"diagnosis"`
	OpaqueID        string                 `json:"id"`
}

type DeduplicationScoringCandidate struct {
	ID        string                 `json:"id"`
	Diagnosis DeduplicationDiagnosis `json:"diagnosis"`
}

type DeduplicationScoringRequest struct {
	Finding    DeduplicationDiagnosis          `json:"finding"`
	Candidates []DeduplicationScoringCandidate `json:"candidates"`
}

type DeduplicationCandidateScore struct {
	CandidateID string `json:"candidate_id"`
	Score       int    `json:"score"`
	Explanation string `json:"explanation"`
}

type DeduplicationScoringResponse struct {
	Scores []DeduplicationCandidateScore `json:"scores"`
}

type DeduplicationScorer func(
	context.Context,
	RepositoryReviewDeduplicationSnapshot,
	string,
	DeduplicationScoringRequest,
) (DeduplicationScoringResponse, error)

type DeduplicationShortlistedCandidate struct {
	ID              string                 `json:"-"`
	Version         int64                  `json:"-"`
	CreationOrdinal uint64                 `json:"-"`
	OpaqueID        string                 `json:"id"`
	Diagnosis       DeduplicationDiagnosis `json:"diagnosis"`
	Score           int                    `json:"-"`
	Explanation     string                 `json:"-"`
}

type DeduplicationJudgeRequest struct {
	Finding    DeduplicationDiagnosis              `json:"finding"`
	Candidates []DeduplicationShortlistedCandidate `json:"candidates"`
}

type DeduplicationJudgment struct {
	Decision    string `json:"decision"`
	CandidateID string `json:"candidate_id,omitempty"`
}

type DeduplicationJudge func(
	context.Context,
	RepositoryReviewDeduplicationSnapshot,
	string,
	DeduplicationJudgeRequest,
) (DeduplicationJudgment, error)

type DeduplicationModelResult struct {
	Scores      []DeduplicationCandidateScore
	Shortlisted []DeduplicationShortlistedCandidate
	Judgment    DeduplicationJudgment
}

// EvaluateDeduplicationCandidates performs all isolated model work for one
// already-frozen universe. Store/lease operations intentionally live outside
// this function, so callers cannot accidentally hold a ledger lock here.
func EvaluateDeduplicationCandidates(
	ctx context.Context,
	snapshot RepositoryReviewDeduplicationSnapshot,
	finding DeduplicationDiagnosis,
	candidates []DeduplicationCandidateSnapshot,
	modelInputCeiling int,
	score DeduplicationScorer,
	judge DeduplicationJudge,
) (DeduplicationModelResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	threshold := snapshot.SimilarityThreshold
	candidateLimit := snapshot.CandidateLimit
	if threshold < 0 || threshold > 100 || candidateLimit < 0 ||
		candidateLimit > DeduplicationMaximumShortlist {
		return DeduplicationModelResult{}, errors.New("invalid frozen deduplication settings")
	}
	if candidateLimit == 0 || len(candidates) == 0 {
		return DeduplicationModelResult{Judgment: DeduplicationJudgment{Decision: "new"}}, nil
	}
	if score == nil {
		return DeduplicationModelResult{}, errors.New("deduplication scorer is required")
	}
	requests, ordered, err := PrepareDeduplicationScoringRequests(
		finding, candidates, modelInputCeiling,
	)
	if err != nil {
		return DeduplicationModelResult{}, err
	}
	scores := make([]DeduplicationCandidateScore, 0, len(ordered))
	for _, request := range requests {
		if contextErr := ctx.Err(); contextErr != nil {
			return DeduplicationModelResult{}, contextErr
		}
		response, scoreErr := score(ctx, snapshot, DeduplicationScoringInstructions, request)
		if scoreErr != nil {
			return DeduplicationModelResult{}, fmt.Errorf("deduplication scoring failed: %w", scoreErr)
		}
		if validationErr := ValidateDeduplicationScoringResponse(response, request); validationErr != nil {
			return DeduplicationModelResult{}, validationErr
		}
		scores = append(scores, response.Scores...)
	}
	shortlisted, err := ShortlistDeduplicationCandidates(
		ordered, scores, threshold, candidateLimit,
	)
	if err != nil {
		return DeduplicationModelResult{}, err
	}
	result := DeduplicationModelResult{Scores: scores, Shortlisted: shortlisted}
	if len(shortlisted) == 0 {
		result.Judgment.Decision = "new"
		return result, nil
	}
	if judge == nil {
		return DeduplicationModelResult{}, errors.New("deduplication judge is required")
	}
	request, err := PrepareDeduplicationJudgeRequest(finding, shortlisted, modelInputCeiling)
	if err != nil {
		return DeduplicationModelResult{}, err
	}
	judgment, err := judge(ctx, snapshot, DeduplicationJudgeInstructions, request)
	if err != nil {
		return DeduplicationModelResult{}, fmt.Errorf("deduplication judgment failed: %w", err)
	}
	if err := ValidateDeduplicationJudgment(judgment, shortlisted); err != nil {
		return DeduplicationModelResult{}, err
	}
	result.Judgment = judgment
	return result, nil
}

// PrepareDeduplicationJudgeRequest enforces the same isolated input ceiling as
// scoring. An empty shortlist is intentionally rejected because it must be
// finalized as new without a second model call.
func PrepareDeduplicationJudgeRequest(
	finding DeduplicationDiagnosis,
	candidates []DeduplicationShortlistedCandidate,
	modelInputCeiling int,
) (DeduplicationJudgeRequest, error) {
	if len(candidates) == 0 {
		return DeduplicationJudgeRequest{}, errors.New("empty deduplication shortlist does not require a judge")
	}
	if len(candidates) > DeduplicationMaximumShortlist {
		return DeduplicationJudgeRequest{}, errors.New("deduplication judge shortlist exceeds 20 candidates")
	}
	seen := make(map[string]struct{}, len(candidates))
	for _, candidate := range candidates {
		if candidate.OpaqueID == "" {
			return DeduplicationJudgeRequest{}, errors.New("deduplication judge candidate has no opaque ID")
		}
		if _, duplicate := seen[candidate.OpaqueID]; duplicate {
			return DeduplicationJudgeRequest{}, errors.New("deduplication judge candidate ID is duplicated")
		}
		seen[candidate.OpaqueID] = struct{}{}
	}
	request := DeduplicationJudgeRequest{
		Finding: finding, Candidates: append([]DeduplicationShortlistedCandidate{}, candidates...),
	}
	limit := DeduplicationMaximumInputBytes
	if modelInputCeiling > 0 && modelInputCeiling < limit {
		limit = modelInputCeiling
	}
	encoded, err := json.Marshal(request)
	if err != nil {
		return DeduplicationJudgeRequest{}, err
	}
	if limit < 1 || len(encoded) > limit {
		return DeduplicationJudgeRequest{}, fmt.Errorf(
			"deduplication judge input exceeds %d bytes", limit,
		)
	}
	return request, nil
}

// DeduplicationCandidateUniverseDigest fingerprints candidate identity and
// version in deterministic creation order. Diagnosis changes must increment a
// candidate version, and completion separately rechecks every frozen version.
func DeduplicationCandidateUniverseDigest(candidates []DeduplicationCandidateSnapshot) (string, error) {
	ordered, err := normalizeDeduplicationCandidateSnapshots(candidates)
	if err != nil {
		return "", err
	}
	values := make([]string, 0, len(ordered)*3+1)
	values = append(values, fmt.Sprint(len(ordered)))
	for _, candidate := range ordered {
		values = append(values, candidate.ID, fmt.Sprint(candidate.Version), fmt.Sprint(candidate.CreationOrdinal))
	}
	return stableID("rdu_", values...), nil
}

// PrepareDeduplicationScoringRequests assigns opaque IDs and partitions the
// entire frozen candidate universe. No request exceeds sixteen candidates,
// one MiB, or the supplied model input ceiling.
func PrepareDeduplicationScoringRequests(
	finding DeduplicationDiagnosis,
	candidates []DeduplicationCandidateSnapshot,
	modelInputCeiling int,
) ([]DeduplicationScoringRequest, []DeduplicationCandidateSnapshot, error) {
	ordered, err := normalizeDeduplicationCandidateSnapshots(candidates)
	if err != nil {
		return nil, nil, err
	}
	limit := DeduplicationMaximumInputBytes
	if modelInputCeiling > 0 && modelInputCeiling < limit {
		limit = modelInputCeiling
	}
	if limit < 1 {
		return nil, nil, errors.New("deduplication model input ceiling is invalid")
	}
	for index := range ordered {
		ordered[index].OpaqueID = fmt.Sprintf("candidate-%06d", index+1)
	}
	if len(ordered) == 0 {
		return []DeduplicationScoringRequest{}, ordered, nil
	}
	requests := make([]DeduplicationScoringRequest, 0, (len(ordered)+15)/16)
	for offset := 0; offset < len(ordered); {
		request := DeduplicationScoringRequest{Finding: finding}
		for offset < len(ordered) && len(request.Candidates) < DeduplicationScoringCandidateLimit {
			candidate := DeduplicationScoringCandidate{
				ID: ordered[offset].OpaqueID, Diagnosis: ordered[offset].Diagnosis,
			}
			trial := request
			trial.Candidates = append(append([]DeduplicationScoringCandidate{}, request.Candidates...), candidate)
			encoded, marshalErr := json.Marshal(trial)
			if marshalErr != nil {
				return nil, nil, marshalErr
			}
			if len(encoded) > limit {
				if len(request.Candidates) == 0 {
					return nil, nil, fmt.Errorf(
						"deduplication scoring input for %s exceeds %d bytes", candidate.ID, limit,
					)
				}
				break
			}
			request = trial
			offset++
		}
		if len(request.Candidates) == 0 {
			return nil, nil, errors.New("deduplication scoring could not make progress")
		}
		requests = append(requests, request)
	}
	return requests, ordered, nil
}

func ValidateDeduplicationScoringResponse(
	response DeduplicationScoringResponse,
	request DeduplicationScoringRequest,
) error {
	if len(response.Scores) != len(request.Candidates) {
		return errors.New("deduplication scorer did not return exactly one score per candidate")
	}
	wanted := make(map[string]struct{}, len(request.Candidates))
	for _, candidate := range request.Candidates {
		if candidate.ID == "" {
			return errors.New("deduplication scoring request contains an empty candidate ID")
		}
		if _, duplicate := wanted[candidate.ID]; duplicate {
			return errors.New("deduplication scoring request contains a duplicate candidate ID")
		}
		wanted[candidate.ID] = struct{}{}
	}
	seen := make(map[string]struct{}, len(response.Scores))
	for _, score := range response.Scores {
		if _, supplied := wanted[score.CandidateID]; !supplied {
			return errors.New("deduplication scorer returned an unsupplied candidate ID")
		}
		if _, duplicate := seen[score.CandidateID]; duplicate {
			return errors.New("deduplication scorer returned a duplicate candidate ID")
		}
		if score.Score < 0 || score.Score > 100 {
			return errors.New("deduplication scorer returned a score outside 0 through 100")
		}
		if !validDeduplicationExplanation(score.Explanation) {
			return errors.New("deduplication scorer returned an invalid explanation")
		}
		seen[score.CandidateID] = struct{}{}
	}
	return nil
}

// ShortlistDeduplicationCandidates hard-filters first, then applies stable
// score/creation/ID ordering and the configured limit.
func ShortlistDeduplicationCandidates(
	candidates []DeduplicationCandidateSnapshot,
	scores []DeduplicationCandidateScore,
	threshold, candidateLimit int,
) ([]DeduplicationShortlistedCandidate, error) {
	if threshold < 0 || threshold > 100 {
		return nil, errors.New("deduplication similarity threshold must be between 0 and 100")
	}
	if candidateLimit < 0 || candidateLimit > DeduplicationMaximumShortlist {
		return nil, errors.New("deduplication candidate limit must be between 0 and 20")
	}
	if candidateLimit == 0 {
		return []DeduplicationShortlistedCandidate{}, nil
	}
	ordered, err := normalizeDeduplicationCandidateSnapshots(candidates)
	if err != nil {
		return nil, err
	}
	byOpaqueID := make(map[string]DeduplicationCandidateSnapshot, len(ordered))
	for _, candidate := range ordered {
		if candidate.OpaqueID == "" {
			return nil, errors.New("deduplication candidate is missing its opaque ID")
		}
		if _, duplicate := byOpaqueID[candidate.OpaqueID]; duplicate {
			return nil, errors.New("duplicate opaque deduplication candidate ID")
		}
		byOpaqueID[candidate.OpaqueID] = candidate
	}
	if len(scores) != len(ordered) {
		return nil, errors.New("deduplication score set is incomplete")
	}
	seen := make(map[string]struct{}, len(scores))
	shortlisted := make([]DeduplicationShortlistedCandidate, 0, min(candidateLimit, len(scores)))
	for _, score := range scores {
		candidate, exists := byOpaqueID[score.CandidateID]
		if !exists {
			return nil, errors.New("deduplication score references an unsupplied candidate")
		}
		if _, duplicate := seen[score.CandidateID]; duplicate {
			return nil, errors.New("duplicate deduplication score")
		}
		if score.Score < 0 || score.Score > 100 || !validDeduplicationExplanation(score.Explanation) {
			return nil, errors.New("invalid deduplication score")
		}
		seen[score.CandidateID] = struct{}{}
		if score.Score < threshold {
			continue
		}
		shortlisted = append(shortlisted, DeduplicationShortlistedCandidate{
			ID: candidate.ID, Version: candidate.Version, CreationOrdinal: candidate.CreationOrdinal,
			OpaqueID: candidate.OpaqueID, Diagnosis: candidate.Diagnosis,
			Score: score.Score, Explanation: score.Explanation,
		})
	}
	sort.Slice(shortlisted, func(left, right int) bool {
		if shortlisted[left].Score != shortlisted[right].Score {
			return shortlisted[left].Score > shortlisted[right].Score
		}
		if shortlisted[left].CreationOrdinal != shortlisted[right].CreationOrdinal {
			return shortlisted[left].CreationOrdinal < shortlisted[right].CreationOrdinal
		}
		return shortlisted[left].ID < shortlisted[right].ID
	})
	if len(shortlisted) > candidateLimit {
		shortlisted = shortlisted[:candidateLimit]
	}
	return shortlisted, nil
}

func ValidateDeduplicationJudgment(
	judgment DeduplicationJudgment,
	candidates []DeduplicationShortlistedCandidate,
) error {
	if judgment.Decision != "new" && judgment.Decision != "duplicate" {
		return errors.New("deduplication judgment must be new or duplicate")
	}
	if judgment.Decision == "new" {
		if judgment.CandidateID != "" {
			return errors.New("new deduplication judgment must not select a candidate")
		}
		return nil
	}
	if judgment.CandidateID == "" {
		return errors.New("duplicate deduplication judgment must select a candidate")
	}
	for _, candidate := range candidates {
		if candidate.OpaqueID == judgment.CandidateID {
			return nil
		}
	}
	return errors.New("duplicate deduplication judgment selected an unsupplied candidate")
}

func DecodeDeduplicationScoringResponse(data []byte) (DeduplicationScoringResponse, error) {
	var response DeduplicationScoringResponse
	if err := decodeStrictDeduplicationJSON(data, &response); err != nil {
		return DeduplicationScoringResponse{}, err
	}
	return response, nil
}

func DecodeDeduplicationJudgment(data []byte) (DeduplicationJudgment, error) {
	var judgment DeduplicationJudgment
	if err := decodeStrictDeduplicationJSON(data, &judgment); err != nil {
		return DeduplicationJudgment{}, err
	}
	return judgment, nil
}

func decodeStrictDeduplicationJSON(data []byte, target any) error {
	if len(data) == 0 || len(data) > DeduplicationMaximumInputBytes {
		return errors.New("deduplication model response has an invalid size")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return fmt.Errorf("invalid deduplication model response: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("invalid deduplication model response: trailing JSON value")
		}
		return fmt.Errorf("invalid deduplication model response: %w", err)
	}
	return nil
}

func normalizeDeduplicationCandidateSnapshots(
	candidates []DeduplicationCandidateSnapshot,
) ([]DeduplicationCandidateSnapshot, error) {
	ordered := append([]DeduplicationCandidateSnapshot{}, candidates...)
	seen := make(map[string]struct{}, len(ordered))
	for _, candidate := range ordered {
		if strings.TrimSpace(candidate.ID) == "" || candidate.Version < 1 || candidate.CreationOrdinal < 1 {
			return nil, errors.New("invalid deduplication candidate snapshot")
		}
		if _, duplicate := seen[candidate.ID]; duplicate {
			return nil, errors.New("duplicate deduplication candidate snapshot")
		}
		seen[candidate.ID] = struct{}{}
	}
	sort.Slice(ordered, func(left, right int) bool {
		if ordered[left].CreationOrdinal != ordered[right].CreationOrdinal {
			return ordered[left].CreationOrdinal < ordered[right].CreationOrdinal
		}
		return ordered[left].ID < ordered[right].ID
	})
	return ordered, nil
}

func validDeduplicationExplanation(value string) bool {
	return validBoundedText(strings.TrimSpace(value), maxDeduplicationExplanationBytes)
}
