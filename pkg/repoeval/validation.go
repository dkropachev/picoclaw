package repoeval

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/url"
	"path"
	"reflect"
	"sort"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	DefaultFilesPerLanguage = 20
	MaxFilesPerLanguage     = 20

	maxEvaluations        = 10_000
	maxRepositoryBytes    = 4096
	maxRefBytes           = 4096
	maxAliasBytes         = 256
	maxPathBytes          = 4096
	maxLanguageBytes      = 128
	maxHashBytes          = 256
	maxRunIDBytes         = 1024
	maxFreeTextBytes      = 32 << 10
	maxSummaryBytes       = 64 << 10
	maxWarningBytes       = 16 << 10
	maxLanguages          = 128
	maxCorpusFiles        = maxLanguages * MaxFilesPerLanguage
	maxChunksPerFile      = 256
	maxCorpusChunks       = 65_536
	maxWarnings           = 256
	maxRunIDs             = 4096
	maxScoreMetrics       = 128
	maxComparisonDetails  = 64
	maxProgressCount      = 10_000_000
	maxCounter            = int64(1<<62 - 1)
	maxBatchCheckpoints   = 1024
	maxBatchEvidenceBytes = 256 << 10
)

var (
	ErrConflict          = errors.New("repository evaluation state changed")
	ErrInvalidEvaluation = errors.New("invalid repository evaluation")
	ErrInvalidTransition = errors.New("invalid repository evaluation status transition")
	ErrControllerLocked  = errors.New("repository evaluation controller is already active")
)

func Clone(evaluation Evaluation) Evaluation {
	clone := evaluation
	clone.CandidateModels = append([]string(nil), evaluation.CandidateModels...)
	clone.Focus.CodeTypes = append([]CodeType(nil), evaluation.Focus.CodeTypes...)
	clone.Focus.IncludeFolders = append([]string(nil), evaluation.Focus.IncludeFolders...)
	clone.Focus.ExcludeFolders = append([]string(nil), evaluation.Focus.ExcludeFolders...)
	clone.FilesPerLanguage = cloneIntMap(evaluation.FilesPerLanguage)
	clone.Corpus = cloneCorpus(evaluation.Corpus)
	clone.Progress.Languages = cloneLanguageProgressMap(evaluation.Progress.Languages)
	clone.Usage.EstimatedCostUSD = cloneFloat(evaluation.Usage.EstimatedCostUSD)
	clone.ModelStats = make(map[string]ModelStats, len(evaluation.ModelStats))
	for model, stats := range evaluation.ModelStats {
		stats.Usage.EstimatedCostUSD = cloneFloat(stats.Usage.EstimatedCostUSD)
		stats.StartedAt = cloneTime(stats.StartedAt)
		stats.CompletedAt = cloneTime(stats.CompletedAt)
		clone.ModelStats[model] = stats
	}
	if evaluation.Checkpoint.Batches != nil {
		clone.Checkpoint.Batches = make([]BatchCheckpoint, len(evaluation.Checkpoint.Batches))
		for index, checkpoint := range evaluation.Checkpoint.Batches {
			clone.Checkpoint.Batches[index] = checkpoint
			clone.Checkpoint.Batches[index].CandidateIDs = append([]string(nil), checkpoint.CandidateIDs...)
			if checkpoint.Candidates != nil {
				clone.Checkpoint.Batches[index].Candidates = make(
					map[string]BatchCandidateCheckpoint,
					len(checkpoint.Candidates),
				)
				for alias, outcome := range checkpoint.Candidates {
					outcome.CompletedCandidateIDs = append([]string(nil), outcome.CompletedCandidateIDs...)
					clone.Checkpoint.Batches[index].Candidates[alias] = outcome
				}
			}
		}
	}
	if evaluation.Checkpoint.ConcreteModels != nil {
		clone.Checkpoint.ConcreteModels = make(map[string]map[string]int, len(evaluation.Checkpoint.ConcreteModels))
		for alias, concrete := range evaluation.Checkpoint.ConcreteModels {
			clone.Checkpoint.ConcreteModels[alias] = cloneIntMap(concrete)
		}
	}
	clone.Comparisons = make([]ModelComparison, len(evaluation.Comparisons))
	for index, comparison := range evaluation.Comparisons {
		clone.Comparisons[index] = comparison
		clone.Comparisons[index].ConcreteModels = cloneIntMap(comparison.ConcreteModels)
		clone.Comparisons[index].OverallScore = cloneFloat(comparison.OverallScore)
		clone.Comparisons[index].Scores = cloneFloatMap(comparison.Scores)
		clone.Comparisons[index].Usage.EstimatedCostUSD = cloneFloat(comparison.Usage.EstimatedCostUSD)
		clone.Comparisons[index].Languages = append([]string(nil), comparison.Languages...)
		clone.Comparisons[index].Regions = append([]string(nil), comparison.Regions...)
		clone.Comparisons[index].Strengths = append([]string(nil), comparison.Strengths...)
		clone.Comparisons[index].Limitations = append([]string(nil), comparison.Limitations...)
	}
	clone.Warnings = append([]string(nil), evaluation.Warnings...)
	clone.RunIDs = append([]string(nil), evaluation.RunIDs...)
	clone.StartedAt = cloneTime(evaluation.StartedAt)
	clone.FinishedAt = cloneTime(evaluation.FinishedAt)
	return clone
}

func cloneCorpus(manifest *CorpusManifest) *CorpusManifest {
	if manifest == nil {
		return nil
	}
	clone := *manifest
	clone.Files = make([]CorpusFile, len(manifest.Files))
	for index, file := range manifest.Files {
		clone.Files[index] = file
		clone.Files[index].Chunks = append([]CorpusChunk(nil), file.Chunks...)
	}
	clone.LanguageCounts = cloneIntMap(manifest.LanguageCounts)
	return &clone
}

func cloneTime(value *time.Time) *time.Time {
	if value == nil {
		return nil
	}
	copyValue := *value
	return &copyValue
}

func cloneFloat(value *float64) *float64 {
	if value == nil {
		return nil
	}
	copyValue := *value
	return &copyValue
}

func cloneIntMap(values map[string]int) map[string]int {
	if values == nil {
		return nil
	}
	clone := make(map[string]int, len(values))
	for key, value := range values {
		clone[key] = value
	}
	return clone
}

func cloneFloatMap(values map[string]float64) map[string]float64 {
	if values == nil {
		return nil
	}
	clone := make(map[string]float64, len(values))
	for key, value := range values {
		clone[key] = value
	}
	return clone
}

func cloneLanguageProgressMap(values map[string]LanguageProgress) map[string]LanguageProgress {
	if values == nil {
		return nil
	}
	clone := make(map[string]LanguageProgress, len(values))
	for language, progress := range values {
		progress.Regions = append([]string(nil), progress.Regions...)
		clone[language] = progress
	}
	return clone
}

func normalizeCreate(request CreateRequest) (CreateRequest, error) {
	request.Repository = strings.TrimSpace(request.Repository)
	request.Ref = strings.TrimSpace(request.Ref)
	if request.Ref == "" {
		request.Ref = "HEAD"
	}
	request.CandidateModels = normalizeUniqueText(request.CandidateModels)
	request.SelectorModelAlias = strings.TrimSpace(request.SelectorModelAlias)
	request.JudgeModelAlias = strings.TrimSpace(request.JudgeModelAlias)
	request.InitialRunID = strings.TrimSpace(request.InitialRunID)
	focus, err := normalizeFocus(request.Focus)
	if err != nil {
		return CreateRequest{}, err
	}
	request.Focus = focus
	if request.DefaultFilesPerLanguage == 0 {
		request.DefaultFilesPerLanguage = DefaultFilesPerLanguage
	}
	request.FilesPerLanguage, err = normalizeIntMap(request.FilesPerLanguage)
	return request, err
}

func normalizeEvaluation(evaluation Evaluation) (Evaluation, error) {
	evaluation = Clone(evaluation)
	evaluation.Repository = strings.TrimSpace(evaluation.Repository)
	evaluation.Ref = strings.TrimSpace(evaluation.Ref)
	if evaluation.Ref == "" {
		evaluation.Ref = "HEAD"
	}
	evaluation.CandidateModels = normalizeUniqueText(evaluation.CandidateModels)
	evaluation.SelectorModelAlias = strings.TrimSpace(evaluation.SelectorModelAlias)
	evaluation.JudgeModelAlias = strings.TrimSpace(evaluation.JudgeModelAlias)
	focus, err := normalizeFocus(evaluation.Focus)
	if err != nil {
		return Evaluation{}, err
	}
	evaluation.Focus = focus
	evaluation.FilesPerLanguage, err = normalizeIntMap(evaluation.FilesPerLanguage)
	if err != nil {
		return Evaluation{}, err
	}
	if evaluation.Corpus != nil {
		evaluation.Corpus, err = normalizeCorpus(evaluation.Corpus)
		if err != nil {
			return Evaluation{}, err
		}
	}
	if evaluation.Progress.Stage == "" {
		evaluation.Progress.Stage = ProgressIdle
	}
	evaluation.Progress.Languages, err = normalizeLanguageProgressMap(evaluation.Progress.Languages)
	if err != nil {
		return Evaluation{}, err
	}
	evaluation.Progress.CurrentModel = strings.TrimSpace(evaluation.Progress.CurrentModel)
	evaluation.Progress.CurrentPath, err = normalizeOptionalPath(evaluation.Progress.CurrentPath, false)
	if err != nil {
		return Evaluation{}, err
	}
	evaluation.Progress.Message = strings.TrimSpace(evaluation.Progress.Message)
	evaluation.Progress.UpdatedAt = evaluation.Progress.UpdatedAt.UTC()
	stats := make(map[string]ModelStats, len(evaluation.ModelStats))
	for rawModel, value := range evaluation.ModelStats {
		model := strings.TrimSpace(rawModel)
		if _, duplicate := stats[model]; duplicate {
			return Evaluation{}, fmt.Errorf("%w: duplicate normalized model statistics key", ErrInvalidEvaluation)
		}
		value.Summary = strings.TrimSpace(value.Summary)
		value.StartedAt = utcTime(value.StartedAt)
		value.CompletedAt = utcTime(value.CompletedAt)
		stats[model] = value
	}
	evaluation.ModelStats = stats
	for index := range evaluation.Comparisons {
		comparison := &evaluation.Comparisons[index]
		comparison.ModelAlias = strings.TrimSpace(comparison.ModelAlias)
		comparison.Failure = strings.TrimSpace(comparison.Failure)
		comparison.Verdict = strings.TrimSpace(comparison.Verdict)
		comparison.Summary = strings.TrimSpace(comparison.Summary)
		comparison.ConcreteModels, err = normalizeIntMap(comparison.ConcreteModels)
		if err != nil {
			return Evaluation{}, err
		}
		comparison.Languages = normalizeUniqueText(comparison.Languages)
		comparison.Regions, err = normalizePaths(comparison.Regions, true)
		if err != nil {
			return Evaluation{}, err
		}
		comparison.Strengths = normalizeUniqueText(comparison.Strengths)
		comparison.Limitations = normalizeUniqueText(comparison.Limitations)
		comparison.Scores, err = normalizeFloatMap(comparison.Scores)
		if err != nil {
			return Evaluation{}, err
		}
	}
	evaluation.Warnings = normalizeUniqueText(evaluation.Warnings)
	evaluation.RunIDs = normalizeUniqueText(evaluation.RunIDs)
	evaluation.Failure = strings.TrimSpace(evaluation.Failure)
	evaluation.CreatedAt = evaluation.CreatedAt.UTC()
	evaluation.UpdatedAt = evaluation.UpdatedAt.UTC()
	evaluation.StartedAt = utcTime(evaluation.StartedAt)
	evaluation.FinishedAt = utcTime(evaluation.FinishedAt)
	return evaluation, nil
}

func normalizeFocus(focus Focus) (Focus, error) {
	seenTypes := make(map[CodeType]struct{}, len(focus.CodeTypes))
	focus.CodeTypes = append([]CodeType(nil), focus.CodeTypes...)
	for _, codeType := range focus.CodeTypes {
		if _, duplicate := seenTypes[codeType]; duplicate {
			return Focus{}, fmt.Errorf("%w: duplicate focus code type", ErrInvalidEvaluation)
		}
		seenTypes[codeType] = struct{}{}
	}
	sort.Slice(focus.CodeTypes, func(i, j int) bool { return focus.CodeTypes[i] < focus.CodeTypes[j] })
	var err error
	focus.IncludeFolders, err = normalizePaths(focus.IncludeFolders, true)
	if err != nil {
		return Focus{}, err
	}
	focus.ExcludeFolders, err = normalizePaths(focus.ExcludeFolders, true)
	if err != nil {
		return Focus{}, err
	}
	focus.FreeText = strings.TrimSpace(focus.FreeText)
	return focus, nil
}

func normalizeCorpus(manifest *CorpusManifest) (*CorpusManifest, error) {
	clone := cloneCorpus(manifest)
	clone.CommitSHA = strings.ToLower(strings.TrimSpace(clone.CommitSHA))
	clone.InventoryHash = strings.TrimSpace(clone.InventoryHash)
	clone.PolicyHash = strings.TrimSpace(clone.PolicyHash)
	clone.RubricHash = strings.TrimSpace(clone.RubricHash)
	clone.SelectorRunID = strings.TrimSpace(clone.SelectorRunID)
	clone.SelectionRationale = strings.TrimSpace(clone.SelectionRationale)
	clone.GeneratedAt = clone.GeneratedAt.UTC()
	for index := range clone.Files {
		file := &clone.Files[index]
		var err error
		file.CandidateID = strings.ToLower(strings.TrimSpace(file.CandidateID))
		file.Path, err = normalizeOptionalPath(file.Path, false)
		if err != nil {
			return nil, err
		}
		file.BlobSHA = strings.ToLower(strings.TrimSpace(file.BlobSHA))
		file.Language = strings.TrimSpace(file.Language)
		file.Module, err = normalizeOptionalPath(file.Module, true)
		if err != nil {
			return nil, err
		}
		file.Region, err = normalizeOptionalPath(file.Region, true)
		if err != nil {
			return nil, err
		}
		for chunkIndex := range file.Chunks {
			file.Chunks[chunkIndex].ID = strings.TrimSpace(file.Chunks[chunkIndex].ID)
			file.Chunks[chunkIndex].ContentHash = strings.TrimSpace(file.Chunks[chunkIndex].ContentHash)
		}
	}
	var err error
	clone.LanguageCounts, err = normalizeIntMap(clone.LanguageCounts)
	return clone, err
}

func normalizePaths(values []string, allowDot bool) ([]string, error) {
	out := make([]string, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		normalized, err := normalizeOptionalPath(value, allowDot)
		if err != nil {
			return nil, err
		}
		if normalized == "" {
			continue
		}
		if _, duplicate := seen[normalized]; duplicate {
			continue
		}
		seen[normalized] = struct{}{}
		out = append(out, normalized)
	}
	sort.Strings(out)
	return out, nil
}

func normalizeOptionalPath(value string, allowDot bool) (string, error) {
	value = strings.TrimSpace(strings.ReplaceAll(value, "\\", "/"))
	if value == "" {
		return "", nil
	}
	clean := path.Clean(value)
	if clean != value || strings.HasPrefix(clean, "/") || strings.HasPrefix(clean, "../") ||
		(!allowDot && clean == ".") || !validText(clean, maxPathBytes, true) {
		return "", fmt.Errorf("%w: invalid repository path", ErrInvalidEvaluation)
	}
	return clean, nil
}

func normalizeUniqueText(values []string) []string {
	out := make([]string, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, duplicate := seen[value]; duplicate {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	return out
}

func normalizeIntMap(values map[string]int) (map[string]int, error) {
	if values == nil {
		return map[string]int{}, nil
	}
	out := make(map[string]int, len(values))
	for rawKey, value := range values {
		key := strings.TrimSpace(rawKey)
		if _, duplicate := out[key]; duplicate {
			return nil, fmt.Errorf("%w: duplicate normalized map key", ErrInvalidEvaluation)
		}
		out[key] = value
	}
	return out, nil
}

func normalizeFloatMap(values map[string]float64) (map[string]float64, error) {
	if values == nil {
		return map[string]float64{}, nil
	}
	out := make(map[string]float64, len(values))
	for rawKey, value := range values {
		key := strings.TrimSpace(rawKey)
		if _, duplicate := out[key]; duplicate {
			return nil, fmt.Errorf("%w: duplicate normalized score key", ErrInvalidEvaluation)
		}
		out[key] = value
	}
	return out, nil
}

func normalizeLanguageProgressMap(values map[string]LanguageProgress) (map[string]LanguageProgress, error) {
	if values == nil {
		return map[string]LanguageProgress{}, nil
	}
	out := make(map[string]LanguageProgress, len(values))
	for rawLanguage, progress := range values {
		language := strings.TrimSpace(rawLanguage)
		if _, duplicate := out[language]; duplicate {
			return nil, fmt.Errorf("%w: duplicate normalized language progress key", ErrInvalidEvaluation)
		}
		regions, err := normalizePaths(progress.Regions, true)
		if err != nil {
			return nil, err
		}
		progress.Regions = regions
		out[language] = progress
	}
	return out, nil
}

func utcTime(value *time.Time) *time.Time {
	if value == nil {
		return nil
	}
	converted := value.UTC()
	return &converted
}

func validateCreate(request CreateRequest) error {
	if !validRepositoryIdentity(request.Repository) ||
		!validText(request.Ref, maxRefBytes, false) ||
		len(request.CandidateModels) < 2 || len(request.CandidateModels) > 8 ||
		!validText(request.SelectorModelAlias, maxAliasBytes, false) ||
		!validText(request.JudgeModelAlias, maxAliasBytes, false) ||
		request.DefaultFilesPerLanguage < 1 || request.DefaultFilesPerLanguage > MaxFilesPerLanguage ||
		len(request.FilesPerLanguage) > maxLanguages {
		return ErrInvalidEvaluation
	}
	for _, model := range request.CandidateModels {
		if !validText(model, maxAliasBytes, false) {
			return ErrInvalidEvaluation
		}
	}
	if err := validateFocus(request.Focus); err != nil {
		return err
	}
	return validateLanguageLimits(request.FilesPerLanguage)
}

func validateEvaluation(evaluation Evaluation) error {
	request := CreateRequest{
		Repository: evaluation.Repository, Ref: evaluation.Ref,
		CandidateModels:    evaluation.CandidateModels,
		SelectorModelAlias: evaluation.SelectorModelAlias, JudgeModelAlias: evaluation.JudgeModelAlias,
		Focus: evaluation.Focus, DefaultFilesPerLanguage: evaluation.DefaultFilesPerLanguage,
		FilesPerLanguage: evaluation.FilesPerLanguage,
	}
	if evaluation.SchemaVersion != SchemaVersion || !validEvaluationID(evaluation.ID) ||
		evaluation.Version < 1 || !evaluation.Status.Valid() || validateCreate(request) != nil ||
		evaluation.CreatedAt.IsZero() || evaluation.UpdatedAt.Before(evaluation.CreatedAt) ||
		evaluation.CreatedAt.Location() != time.UTC || evaluation.UpdatedAt.Location() != time.UTC {
		return ErrInvalidEvaluation
	}
	if err := validateCorpus(evaluation.Corpus, evaluation); err != nil {
		return err
	}
	if err := validateProgress(evaluation.Progress); err != nil {
		return err
	}
	if err := validateUsage(evaluation.Usage); err != nil {
		return err
	}
	if err := validateModelStats(evaluation.ModelStats); err != nil {
		return err
	}
	if err := validateCheckpoint(evaluation.Checkpoint, evaluation); err != nil {
		return err
	}
	if err := validateComparisons(evaluation.Comparisons, evaluation.CandidateModels, evaluation.Status); err != nil {
		return err
	}
	if err := validateBoundedTexts(evaluation.Warnings, maxWarnings, maxWarningBytes); err != nil {
		return err
	}
	if err := validateBoundedTexts(evaluation.RunIDs, maxRunIDs, maxRunIDBytes); err != nil {
		return err
	}
	if evaluation.Status == StatusFailed {
		if !validText(evaluation.Failure, maxSummaryBytes, false) {
			return ErrInvalidEvaluation
		}
	} else if evaluation.Failure != "" {
		return ErrInvalidEvaluation
	}
	if evaluation.Status.Terminal() != (evaluation.FinishedAt != nil) {
		return ErrInvalidEvaluation
	}
	for _, timestamp := range []*time.Time{evaluation.StartedAt, evaluation.FinishedAt} {
		if timestamp != nil &&
			(timestamp.IsZero() || timestamp.Location() != time.UTC || timestamp.Before(evaluation.CreatedAt)) {
			return ErrInvalidEvaluation
		}
	}
	if evaluation.FinishedAt != nil && evaluation.StartedAt != nil &&
		evaluation.FinishedAt.Before(*evaluation.StartedAt) {
		return ErrInvalidEvaluation
	}
	return nil
}

func validateCheckpoint(checkpoint Checkpoint, evaluation Evaluation) error {
	if len(checkpoint.Batches) > maxBatchCheckpoints ||
		len(checkpoint.ConcreteModels) > len(evaluation.CandidateModels) {
		return ErrInvalidEvaluation
	}
	if (evaluation.Status == StatusDraft || evaluation.Status == StatusPreflighting || evaluation.Status == StatusReady) &&
		(len(checkpoint.Batches) != 0 || len(checkpoint.ConcreteModels) != 0) {
		return ErrInvalidEvaluation
	}
	candidateAliases := make(map[string]struct{}, len(evaluation.CandidateModels))
	for _, alias := range evaluation.CandidateModels {
		candidateAliases[alias] = struct{}{}
	}
	for alias, models := range checkpoint.ConcreteModels {
		if _, ok := candidateAliases[alias]; !ok || len(models) > 128 {
			return ErrInvalidEvaluation
		}
		for model, count := range models {
			if !validText(model, maxAliasBytes, false) || count < 1 || count > maxProgressCount {
				return ErrInvalidEvaluation
			}
		}
	}
	knownCandidates := make(map[string]struct{})
	if evaluation.Corpus != nil {
		for _, file := range evaluation.Corpus.Files {
			knownCandidates[file.CandidateID] = struct{}{}
		}
	}
	seenBatches := make(map[string]struct{}, len(checkpoint.Batches))
	seenCompletedPairs := make(map[string]struct{})
	for _, batch := range checkpoint.Batches {
		if !validText(batch.ID, maxHashBytes, false) || len(batch.CandidateIDs) == 0 ||
			len(batch.CandidateIDs) > maxCorpusFiles || len(batch.Candidates) > len(candidateAliases) ||
			!validText(batch.JudgeJSON, maxBatchEvidenceBytes, false) ||
			!validText(batch.MappingJSON, maxBatchEvidenceBytes, false) || !json.Valid([]byte(batch.JudgeJSON)) ||
			!json.Valid(
				[]byte(batch.MappingJSON),
			) || batch.CompletedAt.IsZero() || batch.CompletedAt.Location() != time.UTC {
			return ErrInvalidEvaluation
		}
		if _, duplicate := seenBatches[batch.ID]; duplicate {
			return ErrInvalidEvaluation
		}
		seenBatches[batch.ID] = struct{}{}
		batchCandidates := make(map[string]struct{}, len(batch.CandidateIDs))
		for _, candidateID := range batch.CandidateIDs {
			if _, ok := knownCandidates[candidateID]; !ok {
				return ErrInvalidEvaluation
			}
			if _, duplicate := batchCandidates[candidateID]; duplicate {
				return ErrInvalidEvaluation
			}
			batchCandidates[candidateID] = struct{}{}
		}
		if len(batch.Candidates) == 0 {
			for alias := range candidateAliases {
				for candidateID := range batchCandidates {
					pair := alias + "\x00" + candidateID
					if _, duplicate := seenCompletedPairs[pair]; duplicate {
						return ErrInvalidEvaluation
					}
					seenCompletedPairs[pair] = struct{}{}
				}
			}
		}
		for alias, outcome := range batch.Candidates {
			if _, ok := candidateAliases[alias]; !ok || outcome.Attempts < 0 ||
				outcome.Successes < 0 || outcome.Failures < 0 ||
				outcome.Attempts != outcome.Successes+outcome.Failures ||
				outcome.Attempts > len(batch.CandidateIDs) ||
				len(outcome.CompletedCandidateIDs) > len(batch.CandidateIDs) ||
				len(outcome.CompletedCandidateIDs) > 3*outcome.Successes {
				return ErrInvalidEvaluation
			}
			seenInOutcome := make(map[string]struct{}, len(outcome.CompletedCandidateIDs))
			for _, candidateID := range outcome.CompletedCandidateIDs {
				if _, ok := batchCandidates[candidateID]; !ok {
					return ErrInvalidEvaluation
				}
				if _, duplicate := seenInOutcome[candidateID]; duplicate {
					return ErrInvalidEvaluation
				}
				seenInOutcome[candidateID] = struct{}{}
				pair := alias + "\x00" + candidateID
				if _, duplicate := seenCompletedPairs[pair]; duplicate {
					return ErrInvalidEvaluation
				}
				seenCompletedPairs[pair] = struct{}{}
			}
		}
	}
	return nil
}

func validateFocus(focus Focus) error {
	if len(focus.CodeTypes) > 4 || len(focus.IncludeFolders) > 256 || len(focus.ExcludeFolders) > 256 ||
		!validText(focus.FreeText, maxFreeTextBytes, true) {
		return ErrInvalidEvaluation
	}
	seen := make(map[CodeType]struct{}, len(focus.CodeTypes))
	for _, codeType := range focus.CodeTypes {
		if !codeType.Valid() {
			return ErrInvalidEvaluation
		}
		if _, duplicate := seen[codeType]; duplicate {
			return ErrInvalidEvaluation
		}
		seen[codeType] = struct{}{}
	}
	for _, folders := range [][]string{focus.IncludeFolders, focus.ExcludeFolders} {
		if err := validatePaths(folders, true); err != nil {
			return err
		}
	}
	return nil
}

func validatePaths(values []string, allowDot bool) error {
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		normalized, err := normalizeOptionalPath(value, allowDot)
		if err != nil || normalized != value {
			return ErrInvalidEvaluation
		}
		if _, duplicate := seen[value]; duplicate {
			return ErrInvalidEvaluation
		}
		seen[value] = struct{}{}
	}
	return nil
}

func validateLanguageLimits(limits map[string]int) error {
	if len(limits) > maxLanguages {
		return ErrInvalidEvaluation
	}
	for language, count := range limits {
		if !validText(language, maxLanguageBytes, false) || count < 1 || count > MaxFilesPerLanguage {
			return ErrInvalidEvaluation
		}
	}
	return nil
}

func validateCorpus(manifest *CorpusManifest, evaluation Evaluation) error {
	if manifest == nil {
		if evaluation.Status != StatusDraft && evaluation.Status != StatusPreflighting &&
			evaluation.Status != StatusCanceling && evaluation.Status != StatusCanceled && evaluation.Status != StatusFailed {
			return ErrInvalidEvaluation
		}
		return nil
	}
	if !validGitObjectID(manifest.CommitSHA) ||
		!validText(manifest.InventoryHash, maxHashBytes, false) ||
		!validText(manifest.PolicyHash, maxHashBytes, false) ||
		!validText(manifest.RubricHash, maxHashBytes, false) ||
		!validText(manifest.SelectorRunID, maxRunIDBytes, false) ||
		!validText(manifest.SelectionRationale, maxSummaryBytes, true) ||
		manifest.GeneratedAt.IsZero() || manifest.GeneratedAt.Location() != time.UTC ||
		len(manifest.Files) < 1 || len(manifest.Files) > maxCorpusFiles ||
		len(manifest.LanguageCounts) > maxLanguages {
		return ErrInvalidEvaluation
	}
	derived := make(map[string]int)
	paths := make(map[string]struct{}, len(manifest.Files))
	candidateIDs := make(map[string]struct{}, len(manifest.Files))
	chunkCount := 0
	for _, file := range manifest.Files {
		if err := validateCorpusFile(file); err != nil {
			return err
		}
		if _, duplicate := paths[file.Path]; duplicate {
			return ErrInvalidEvaluation
		}
		paths[file.Path] = struct{}{}
		if _, duplicate := candidateIDs[file.CandidateID]; duplicate {
			return ErrInvalidEvaluation
		}
		candidateIDs[file.CandidateID] = struct{}{}
		derived[file.Language]++
		limit := evaluation.DefaultFilesPerLanguage
		if override, ok := evaluation.FilesPerLanguage[file.Language]; ok {
			limit = override
		}
		if derived[file.Language] > limit {
			return ErrInvalidEvaluation
		}
		chunkCount += len(file.Chunks)
		if chunkCount > maxCorpusChunks {
			return ErrInvalidEvaluation
		}
	}
	if !reflect.DeepEqual(derived, manifest.LanguageCounts) {
		return ErrInvalidEvaluation
	}
	return nil
}

func validateCorpusFile(file CorpusFile) error {
	normalized, err := normalizeOptionalPath(file.Path, false)
	if err != nil || normalized != file.Path || !validCandidateID(file.CandidateID) ||
		!validGitObjectID(file.BlobSHA) || file.SizeBytes < 0 ||
		!validText(file.Language, maxLanguageBytes, false) || !file.CodeType.Valid() ||
		file.Module == "" || file.Region == "" ||
		len(file.Chunks) < 1 || len(file.Chunks) > maxChunksPerFile {
		return ErrInvalidEvaluation
	}
	module, err := normalizeOptionalPath(file.Module, true)
	if err != nil || module != file.Module {
		return ErrInvalidEvaluation
	}
	seen := make(map[string]struct{}, len(file.Chunks))
	region, err := normalizeOptionalPath(file.Region, true)
	if err != nil || region != file.Region {
		return ErrInvalidEvaluation
	}
	lastEnd := 0
	for _, chunk := range file.Chunks {
		if !validText(chunk.ID, maxAliasBytes, false) ||
			!validText(chunk.ContentHash, maxHashBytes, false) ||
			chunk.StartLine < 1 || chunk.EndLine < chunk.StartLine || chunk.StartLine <= lastEnd {
			return ErrInvalidEvaluation
		}
		if _, duplicate := seen[chunk.ID]; duplicate {
			return ErrInvalidEvaluation
		}
		seen[chunk.ID] = struct{}{}
		lastEnd = chunk.EndLine
	}
	return nil
}

func validateProgress(progress Progress) error {
	counts := []int{
		progress.TotalFiles,
		progress.SelectedFiles,
		progress.CompletedFiles,
		progress.TotalTasks,
		progress.CompletedTasks,
	}
	for _, count := range counts {
		if count < 0 || count > maxProgressCount {
			return ErrInvalidEvaluation
		}
	}
	if !progress.Stage.Valid() || len(progress.Languages) > maxLanguages ||
		progress.SelectedFiles > progress.TotalFiles || progress.CompletedFiles > progress.SelectedFiles ||
		progress.CompletedTasks > progress.TotalTasks || !finiteBetween(progress.Percent, 0, 100) ||
		!validText(progress.CurrentModel, maxAliasBytes, true) ||
		!validText(progress.Message, maxSummaryBytes, true) {
		return ErrInvalidEvaluation
	}
	if progress.CurrentPath != "" {
		normalized, err := normalizeOptionalPath(progress.CurrentPath, false)
		if err != nil || normalized != progress.CurrentPath {
			return ErrInvalidEvaluation
		}
	}
	if !progress.UpdatedAt.IsZero() && progress.UpdatedAt.Location() != time.UTC {
		return ErrInvalidEvaluation
	}
	for language, languageProgress := range progress.Languages {
		if !validText(language, maxLanguageBytes, false) || languageProgress.AvailableFiles < 0 ||
			languageProgress.AvailableFiles > maxProgressCount || languageProgress.SelectedFiles < 0 ||
			languageProgress.SelectedFiles > MaxFilesPerLanguage ||
			languageProgress.SelectedFiles > languageProgress.AvailableFiles ||
			languageProgress.CompletedFiles < 0 ||
			languageProgress.CompletedFiles > languageProgress.SelectedFiles ||
			languageProgress.SelectedBytes < 0 || len(languageProgress.Regions) > 256 {
			return ErrInvalidEvaluation
		}
		if err := validatePaths(languageProgress.Regions, true); err != nil {
			return err
		}
	}
	return nil
}

func validateUsage(usage Usage) error {
	for _, value := range []int64{
		usage.Requests, usage.InputTokens, usage.CachedInputTokens, usage.OutputTokens,
		usage.ReasoningTokens, usage.DurationMillis,
	} {
		if value < 0 || value > maxCounter {
			return ErrInvalidEvaluation
		}
	}
	if usage.EstimatedCostUSD != nil && !finiteBetween(*usage.EstimatedCostUSD, 0, math.MaxFloat64) {
		return ErrInvalidEvaluation
	}
	return nil
}

func validateModelStats(statsByModel map[string]ModelStats) error {
	if len(statsByModel) > 16 {
		return ErrInvalidEvaluation
	}
	for model, stats := range statsByModel {
		if !validText(model, maxAliasBytes, false) || stats.FilesSelected < 0 ||
			stats.FilesSelected > maxCorpusFiles || stats.FilesCompleted < 0 ||
			stats.FilesCompleted > stats.FilesSelected || stats.Attempts < 0 ||
			stats.Attempts > maxProgressCount || stats.Successes < 0 || stats.Failures < 0 ||
			stats.Successes+stats.Failures > stats.Attempts ||
			!finiteBetween(stats.OverallScore, -1_000_000, 1_000_000) ||
			!validText(stats.Summary, maxSummaryBytes, true) || validateUsage(stats.Usage) != nil {
			return ErrInvalidEvaluation
		}
		if err := validateTimePair(stats.StartedAt, stats.CompletedAt); err != nil {
			return err
		}
	}
	return nil
}

func validateComparisons(comparisons []ModelComparison, candidates []string, status Status) error {
	if len(comparisons) > len(candidates) || len(comparisons) > 8 {
		return ErrInvalidEvaluation
	}
	candidateSet := make(map[string]struct{}, len(candidates))
	for _, candidate := range candidates {
		candidateSet[candidate] = struct{}{}
	}
	models := make(map[string]struct{}, len(comparisons))
	ranks := make(map[int]struct{}, len(comparisons))
	for _, comparison := range comparisons {
		if _, ok := candidateSet[comparison.ModelAlias]; !ok || !comparison.Completion.Valid() ||
			len(comparison.ConcreteModels) > 128 || comparison.Failures < 0 ||
			comparison.Failures > maxProgressCount || comparison.Rank < 0 ||
			comparison.Rank > len(candidates) ||
			len(comparison.Scores) > maxScoreMetrics ||
			!validText(comparison.Failure, maxSummaryBytes, true) ||
			!validText(comparison.Verdict, maxSummaryBytes, true) ||
			!validText(comparison.Summary, maxSummaryBytes, true) ||
			len(comparison.Strengths) > maxComparisonDetails || len(comparison.Limitations) > maxComparisonDetails ||
			len(comparison.Languages) > maxLanguages || len(comparison.Regions) > 256 ||
			comparison.FilesAnalyzed < 0 || comparison.FilesAnalyzed > maxCorpusFiles ||
			comparison.BytesAnalyzed < 0 || comparison.ConfirmedFindings < 0 ||
			comparison.ConfirmedFindings > maxProgressCount || comparison.UnsupportedFiles < 0 ||
			comparison.UnsupportedFiles > maxCorpusFiles {
			return ErrInvalidEvaluation
		}
		if comparison.OverallScore != nil && !finiteBetween(*comparison.OverallScore, -1_000_000, 1_000_000) ||
			comparison.Completion == ModelCompletionCompleted && comparison.Failure != "" ||
			comparison.Completion == ModelCompletionCompleted && comparison.OverallScore == nil ||
			comparison.Completion == ModelCompletionPartial &&
				(comparison.Failure == "" || comparison.OverallScore != nil ||
					comparison.Rank != 0 || len(comparison.Scores) != 0) ||
			comparison.Completion == ModelCompletionFailed &&
				(comparison.Failure == "" || comparison.OverallScore != nil || comparison.Rank != 0 || len(comparison.Scores) != 0) ||
			validateUsage(comparison.Usage) != nil {
			return ErrInvalidEvaluation
		}
		for model, count := range comparison.ConcreteModels {
			if !validText(model, maxAliasBytes, false) || count < 1 || count > maxProgressCount {
				return ErrInvalidEvaluation
			}
		}
		if _, duplicate := models[comparison.ModelAlias]; duplicate {
			return ErrInvalidEvaluation
		}
		models[comparison.ModelAlias] = struct{}{}
		if comparison.Rank > 0 {
			if _, duplicate := ranks[comparison.Rank]; duplicate {
				return ErrInvalidEvaluation
			}
			ranks[comparison.Rank] = struct{}{}
		}
		for metric, score := range comparison.Scores {
			if !validText(metric, maxAliasBytes, false) || !finiteBetween(score, -1_000_000, 1_000_000) {
				return ErrInvalidEvaluation
			}
		}
		if err := validateBoundedTexts(comparison.Strengths, maxComparisonDetails, maxWarningBytes); err != nil {
			return err
		}
		if err := validateBoundedTexts(comparison.Limitations, maxComparisonDetails, maxWarningBytes); err != nil {
			return err
		}
		if err := validateBoundedTexts(comparison.Languages, maxLanguages, maxLanguageBytes); err != nil {
			return err
		}
		if err := validatePaths(comparison.Regions, true); err != nil {
			return err
		}
	}
	if status == StatusCompleted {
		if len(comparisons) != len(candidates) {
			return ErrInvalidEvaluation
		}
		for _, comparison := range comparisons {
			if comparison.Completion == ModelCompletionPending {
				return ErrInvalidEvaluation
			}
			if comparison.Completion == ModelCompletionCompleted && comparison.Rank == 0 {
				return ErrInvalidEvaluation
			}
		}
		for rank := 1; rank <= len(ranks); rank++ {
			if _, ok := ranks[rank]; !ok {
				return ErrInvalidEvaluation
			}
		}
	}
	return nil
}

func validateBoundedTexts(values []string, maximumCount, maximumBytes int) error {
	if len(values) > maximumCount {
		return ErrInvalidEvaluation
	}
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		if !validText(value, maximumBytes, false) {
			return ErrInvalidEvaluation
		}
		if _, duplicate := seen[value]; duplicate {
			return ErrInvalidEvaluation
		}
		seen[value] = struct{}{}
	}
	return nil
}

func validateTimePair(started, completed *time.Time) error {
	for _, timestamp := range []*time.Time{started, completed} {
		if timestamp != nil && (timestamp.IsZero() || timestamp.Location() != time.UTC) {
			return ErrInvalidEvaluation
		}
	}
	if completed != nil && started == nil || completed != nil && completed.Before(*started) {
		return ErrInvalidEvaluation
	}
	return nil
}

func validText(value string, maximum int, allowEmpty bool) bool {
	if value == "" {
		return allowEmpty
	}
	return len(value) <= maximum && utf8.ValidString(value) && !strings.ContainsRune(value, 0) &&
		value == strings.TrimSpace(value)
}

func validGitObjectID(value string) bool {
	if len(value) != 40 && len(value) != 64 {
		return false
	}
	for _, character := range value {
		if character < '0' || character > '9' && character < 'a' || character > 'f' {
			return false
		}
	}
	return true
}

func validCandidateID(value string) bool {
	digest, ok := strings.CutPrefix(value, "cand_")
	return ok && len(digest) == 64 && validLowerHex(digest)
}

func validLowerHex(value string) bool {
	if value == "" {
		return false
	}
	for _, character := range value {
		if character < '0' || character > '9' && character < 'a' || character > 'f' {
			return false
		}
	}
	return true
}

func validRepositoryIdentity(value string) bool {
	if !validText(value, maxRepositoryBytes, false) {
		return false
	}
	for _, character := range value {
		if character < 0x20 || character == 0x7f {
			return false
		}
	}
	if strings.Contains(value, "://") {
		repositoryURL, err := url.Parse(value)
		if err != nil || repositoryURL.Scheme == "" || repositoryURL.RawQuery != "" ||
			repositoryURL.Fragment != "" || repositoryURL.Host == "" && repositoryURL.Scheme != "file" {
			return false
		}
		if repositoryURL.User != nil {
			username := repositoryURL.User.Username()
			_, hasPassword := repositoryURL.User.Password()
			if hasPassword || (repositoryURL.Scheme != "ssh" && repositoryURL.Scheme != "git+ssh") ||
				username != "git" {
				return false
			}
		}
		return true
	}
	if at := strings.IndexByte(value, '@'); at >= 0 {
		colon := strings.IndexByte(value[at+1:], ':')
		if colon >= 0 && value[:at] != "git" {
			return false
		}
	}
	return true
}

func finiteBetween(value, minimum, maximum float64) bool {
	return !math.IsNaN(value) && !math.IsInf(value, 0) && value >= minimum && value <= maximum
}
