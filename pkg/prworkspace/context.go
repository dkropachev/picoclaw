package prworkspace

import (
	"sort"
	"strings"
	"time"
)

const (
	maxPromptMessages     = 64
	maxPromptMessageBytes = 256 << 10
	maxPriorStageEvidence = 32
)

// contextBundle is the single shared, durable context projected into review,
// repair, scope, and completion prompts. Stage-specific system prompts decide
// what an agent may do; they do not maintain private copies of user feedback.
func contextBundle(aggregate Aggregate) PRContextBundle {
	charter, hasCharter := aggregate.ActiveCharter()
	findings := currentContextFindings(aggregate, charter, hasCharter)
	return PRContextBundle{
		WorkspaceID: aggregate.Workspace.ID,
		Provider:    aggregate.ProviderSnapshot,
		Charter:     charter,
		Messages:    currentContextMessages(aggregate, charter, hasCharter),
		Findings:    findings,
		Corrections: currentContextCorrections(aggregate, charter, hasCharter),
		RepositoryLessons: currentContextLessons(
			aggregate.RepositoryLessons,
			aggregate.Workspace.RepositoryID,
			charter,
			hasCharter,
		),
		NudgeLearning: NudgeLearningExamples(aggregate.NudgeRounds),
		PriorEvidence: currentContextEvidence(aggregate, charter, hasCharter),
		Deferred:      currentContextDeferred(aggregate.DeferredGroups, findings),
	}
}

func reviewContextBundle(aggregate Aggregate) PRContextBundle {
	bundle := contextBundle(aggregate)
	bundle.Corrections = filterCorrectionsFor(bundle.Corrections, CorrectionReviewOnly)
	bundle.RepositoryLessons = filterLessonsFor(bundle.RepositoryLessons, CorrectionReviewOnly)
	bundle.Messages = filterMessagesFor(bundle.Messages, CorrectionReviewOnly)
	bundle.NudgeLearning = nudgeLearningForStage(aggregate.NudgeRounds, NudgeReviewSearch)
	return bundle
}

func implementationContextBundle(aggregate Aggregate) PRContextBundle {
	bundle := contextBundle(aggregate)
	bundle.Corrections = filterCorrectionsFor(bundle.Corrections, CorrectionImplementationOnly)
	bundle.RepositoryLessons = filterLessonsFor(bundle.RepositoryLessons, CorrectionImplementationOnly)
	bundle.Messages = filterMessagesFor(bundle.Messages, CorrectionImplementationOnly)
	bundle.NudgeLearning = nudgeLearningForStage(aggregate.NudgeRounds, NudgeImplementationDone)
	return bundle
}

func currentContextFindings(aggregate Aggregate, charter Charter, hasCharter bool) []Finding {
	if !hasCharter {
		return nil
	}
	stages := make(map[string]StageRun, len(aggregate.StageRuns))
	for _, stage := range aggregate.StageRuns {
		stages[stage.ID] = stage
	}
	out := make([]Finding, 0, len(aggregate.Findings))
	for _, finding := range aggregate.Findings {
		if finding.OriginRunID == "" {
			continue
		}
		stage, ok := stages[finding.OriginRunID]
		if ok && currentContextStage(stage, charter, aggregate.ProviderSnapshot.HeadSHA) {
			out = append(out, finding)
		}
	}
	return out
}

func currentContextCorrections(aggregate Aggregate, charter Charter, hasCharter bool) []Correction {
	out := make([]Correction, 0, len(aggregate.Corrections))
	for _, correction := range aggregate.Corrections {
		if correction.HeadSHA == "" || correction.HeadSHA != aggregate.ProviderSnapshot.HeadSHA {
			continue
		}
		// An empty charter ID is deliberate workspace-scoped guidance. It is
		// recorded before the first charter exists and remains applicable to later
		// charter revisions at this exact provider head. Nonempty IDs retain the
		// strict active-charter fence.
		if correction.CharterID != "" && (!hasCharter || correction.CharterID != charter.ID) {
			continue
		}
		out = append(out, correction)
	}
	return withoutSupersededCorrections(out)
}

func currentContextMessages(aggregate Aggregate, charter Charter, hasCharter bool) []Message {
	eligible := make([]Message, 0, len(aggregate.Messages))
	for _, message := range aggregate.Messages {
		if message.HeadSHA == "" || message.HeadSHA != aggregate.ProviderSnapshot.HeadSHA {
			continue
		}
		if message.CharterID != "" && (!hasCharter || message.CharterID != charter.ID) {
			continue
		}
		eligible = append(eligible, message)
	}
	start := len(eligible)
	bytes := 0
	for start > 0 && len(eligible)-start < maxPromptMessages {
		next := len(eligible[start-1].Content)
		if bytes+next > maxPromptMessageBytes {
			break
		}
		start--
		bytes += next
	}
	return append([]Message(nil), eligible[start:]...)
}

func currentContextLessons(
	values []RepositoryLesson,
	repositoryID string,
	charter Charter,
	hasCharter bool,
) []RepositoryLesson {
	seen := make(map[string]struct{}, len(values))
	out := make([]RepositoryLesson, 0, len(values))
	for _, lesson := range values {
		if !lesson.Active || lesson.RepositoryID != repositoryID {
			continue
		}
		if lesson.PRType != "" && (!hasCharter || lesson.PRType != charter.Type) {
			continue
		}
		if _, duplicate := seen[lesson.ID]; duplicate {
			continue
		}
		seen[lesson.ID] = struct{}{}
		out = append(out, lesson)
	}
	return out
}

func currentContextEvidence(aggregate Aggregate, charter Charter, hasCharter bool) []StageEvidence {
	if !hasCharter {
		return nil
	}
	out := make([]StageEvidence, 0, len(aggregate.StageRuns))
	for _, stage := range aggregate.StageRuns {
		if stage.Evidence == nil || !currentContextStage(stage, charter, aggregate.ProviderSnapshot.HeadSHA) {
			continue
		}
		out = append(out, *stage.Evidence)
	}
	if len(out) > maxPriorStageEvidence {
		out = out[len(out)-maxPriorStageEvidence:]
	}
	return out
}

func currentContextDeferred(values []DeferredGroup, findings []Finding) []DeferredGroup {
	current := make(map[string]Finding, len(findings))
	for _, finding := range findings {
		current[finding.ID] = finding
	}
	out := make([]DeferredGroup, 0, len(values))
	for _, group := range values {
		if len(group.FindingIDs) == 0 {
			continue
		}
		valid := true
		for _, id := range group.FindingIDs {
			finding, ok := current[id]
			if !ok || finding.Disposition != FindingDeferred {
				valid = false
				break
			}
		}
		if valid {
			out = append(out, group)
		}
	}
	return out
}

func currentContextStage(stage StageRun, charter Charter, headSHA string) bool {
	return stage.CharterID == charter.ID && stage.HeadSHA == charter.HeadSHA && stage.HeadSHA == headSHA &&
		stage.State != ExecutionStale && stage.State != ExecutionCanceled
}

func filterCorrectionsFor(values []Correction, wanted CorrectionApplicability) []Correction {
	out := make([]Correction, 0, len(values))
	for _, value := range values {
		if value.Applicability == wanted || value.Applicability == CorrectionReviewAndImpl {
			out = append(out, value)
		}
	}
	return out
}

func filterLessonsFor(values []RepositoryLesson, wanted CorrectionApplicability) []RepositoryLesson {
	out := make([]RepositoryLesson, 0, len(values))
	for _, value := range values {
		if value.Applicability == wanted || value.Applicability == CorrectionReviewAndImpl {
			out = append(out, value)
		}
	}
	return out
}

func filterMessagesFor(values []Message, wanted CorrectionApplicability) []Message {
	out := make([]Message, 0, len(values))
	for _, value := range values {
		stage := strings.ToLower(strings.TrimSpace(value.Stage))
		if stage == "" || stage == "all" || stage == "both" || stage == "workspace" ||
			wanted == CorrectionReviewOnly && (stage == "review" || stage == "triage") ||
			wanted == CorrectionImplementationOnly &&
				(stage == "implementation" || stage == "validation" || stage == "completion_audit" || stage == "publication") {
			out = append(out, value)
		}
	}
	return out
}

func withoutSupersededCorrections(values []Correction) []Correction {
	superseded := make(map[string]struct{}, len(values))
	for _, value := range values {
		if value.SupersedesID != "" {
			superseded[value.SupersedesID] = struct{}{}
		}
	}
	out := make([]Correction, 0, len(values))
	for _, value := range values {
		if _, removed := superseded[value.ID]; !removed {
			out = append(out, value)
		}
	}
	return out
}

func nudgeLearningForStage(values []NudgeRoundRecord, stage NudgeStage) []NudgeLearningExample {
	filtered := make([]NudgeRoundRecord, 0, len(values))
	for _, value := range values {
		if value.Stage == stage {
			filtered = append(filtered, value)
		}
	}
	return NudgeLearningExamples(filtered)
}

func reviewStageEvidence(
	runID, summary, promptDigest string,
	rounds []ReviewRound,
	findings []Finding,
	createdAt time.Time,
) *StageEvidence {
	return &StageEvidence{
		Stage: "review", RunID: runID, Summary: summary,
		Coverage: mergeReviewCoverage(rounds), FindingIDs: findingIDs(findings),
		PromptDigest: promptDigest, CreatedAt: createdAt,
	}
}

func completionStageEvidence(
	runID, stage, summary, promptDigest string,
	rounds []CompletionRound,
	findings []Finding,
	validation map[string]any,
	createdAt time.Time,
) *StageEvidence {
	return &StageEvidence{
		Stage: stage, RunID: runID, Summary: summary,
		Coverage: mergeCompletionCoverage(rounds), FindingIDs: findingIDs(findings),
		Validation: validation, PromptDigest: promptDigest, CreatedAt: createdAt,
	}
}

func mergeReviewCoverage(rounds []ReviewRound) Coverage {
	values := make([]Coverage, 0, len(rounds))
	for _, round := range rounds {
		values = append(values, round.Result.Coverage)
	}
	return mergeCoverage(values)
}

func mergeCompletionCoverage(rounds []CompletionRound) Coverage {
	values := make([]Coverage, 0, len(rounds))
	for _, round := range rounds {
		values = append(values, round.Result.Coverage)
	}
	return mergeCoverage(values)
}

func mergeCoverage(values []Coverage) Coverage {
	result := Coverage{}
	result.ReviewedAreas = mergeStrings(values, func(value Coverage) []string { return value.ReviewedAreas })
	result.UnreviewedAreas = mergeStrings(values, func(value Coverage) []string { return value.UnreviewedAreas })
	result.TestsConsidered = mergeStrings(values, func(value Coverage) []string { return value.TestsConsidered })
	result.ResidualRisks = mergeStrings(values, func(value Coverage) []string { return value.ResidualRisks })
	if len(result.ReviewedAreas) > 0 && len(result.UnreviewedAreas) > 0 {
		reviewed := make(map[string]struct{}, len(result.ReviewedAreas))
		for _, value := range result.ReviewedAreas {
			reviewed[value] = struct{}{}
		}
		filtered := result.UnreviewedAreas[:0]
		for _, value := range result.UnreviewedAreas {
			if _, done := reviewed[value]; !done {
				filtered = append(filtered, value)
			}
		}
		result.UnreviewedAreas = filtered
	}
	return result
}

func mergeStrings(values []Coverage, selectValues func(Coverage) []string) []string {
	set := make(map[string]struct{})
	for _, value := range values {
		for _, item := range selectValues(value) {
			if item != "" {
				set[item] = struct{}{}
			}
		}
	}
	result := make([]string, 0, len(set))
	for item := range set {
		result = append(result, item)
	}
	sort.Strings(result)
	return result
}
