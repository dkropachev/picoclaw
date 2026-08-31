package repoaudit

import "strings"

// IssuePublicationBlockerCode is a stable, safe reason that an issue preview
// cannot currently be published. API clients may branch on the code; the
// accompanying message is intended for direct display.
type IssuePublicationBlockerCode string

const (
	IssuePublicationRepositoryNotGitHub      IssuePublicationBlockerCode = "repository_not_github"
	IssuePublicationPreviewNotCanonical      IssuePublicationBlockerCode = "preview_not_canonical"
	IssuePublicationOriginNotPublishable     IssuePublicationBlockerCode = "origin_not_publishable"
	IssuePublicationStateNotPublishable      IssuePublicationBlockerCode = "state_not_publishable"
	IssuePublicationFindingMissing           IssuePublicationBlockerCode = "finding_missing"
	IssuePublicationFindingStatusUnresolved  IssuePublicationBlockerCode = "finding_status_unresolved"
	IssuePublicationDuplicateReviewRequired  IssuePublicationBlockerCode = "duplicate_review_required"
	IssuePublicationIssueAssociationConflict IssuePublicationBlockerCode = "issue_association_conflict"
	IssuePublicationHistoricalMergeActive    IssuePublicationBlockerCode = "historical_merge_in_progress"
	IssuePublicationFindingNotPublishable    IssuePublicationBlockerCode = "finding_not_publishable"
)

// IssuePublicationBlocker is an aggregate projection. Count is the number of
// affected records and never contains their identities or other ledger data.
type IssuePublicationBlocker struct {
	Code    IssuePublicationBlockerCode `json:"code"`
	Count   int                         `json:"count"`
	Message string                      `json:"message"`
}

// IssuePublicationEligibility is the authoritative publication decision used
// by API projections, gateway preflight, and the store mutation fence.
type IssuePublicationEligibility struct {
	CanPublish      bool                      `json:"can_publish"`
	PublishBlockers []IssuePublicationBlocker `json:"publish_blockers"`
}

type issuePublicationBlockerDefinition struct {
	code    IssuePublicationBlockerCode
	message string
}

var issuePublicationBlockerDefinitions = []issuePublicationBlockerDefinition{
	{IssuePublicationRepositoryNotGitHub, "This repository is not a canonical GitHub repository."},
	{IssuePublicationPreviewNotCanonical, "This preview is not the finding's canonical issue preview."},
	{IssuePublicationOriginNotPublishable, "This preview represents an existing issue and cannot be posted."},
	{IssuePublicationStateNotPublishable, "This preview is not in a publishable state."},
	{IssuePublicationFindingMissing, "One or more linked findings are unavailable."},
	{IssuePublicationFindingStatusUnresolved, "One or more linked findings do not yet have a publishable status."},
	{IssuePublicationDuplicateReviewRequired, "A duplicate decision is required before publication."},
	{IssuePublicationIssueAssociationConflict, "A linked finding has conflicting issue associations."},
	{IssuePublicationHistoricalMergeActive, "Historical finding consolidation is in progress."},
	{IssuePublicationFindingNotPublishable, "One or more linked findings are not eligible for publication."},
}

// EvaluateIssuePublication determines whether draft can be sent to the
// protected GitHub publication boundary. It intentionally reports every
// independent blocker so grouped legacy previews remain diagnosable.
func EvaluateIssuePublication(
	state RepositoryState,
	draft IssueDraft,
) IssuePublicationEligibility {
	counts := make(map[IssuePublicationBlockerCode]int, len(issuePublicationBlockerDefinitions))
	add := func(code IssuePublicationBlockerCode) { counts[code]++ }

	if !IsCanonicalGitHubRepository(state.Repository) {
		add(IssuePublicationRepositoryNotGitHub)
	}
	if !draft.Canonical {
		add(IssuePublicationPreviewNotCanonical)
	}
	if draft.Origin != IssueDraftOriginAIGenerated && draft.Origin != IssueDraftOriginLegacy {
		add(IssuePublicationOriginNotPublishable)
	}
	if draft.State != IssueDraftEditing && draft.State != IssueDraftPublishing &&
		draft.State != IssueDraftUnknown {
		add(IssuePublicationStateNotPublishable)
	}
	if HistoricalDeduplicationMergeInProgress(state) {
		add(IssuePublicationHistoricalMergeActive)
	}

	for _, findingID := range draft.FindingIDs {
		index := findingIndexByID(state.Findings, findingID)
		if index < 0 {
			add(IssuePublicationFindingMissing)
			continue
		}
		finding := state.Findings[index]
		findingStatusUnresolved := issuePublicationFindingStatusUnresolved(state, finding)
		if findingStatusUnresolved {
			add(IssuePublicationFindingStatusUnresolved)
		}

		duplicateReviewRequired := finding.RepositoryMatchState == RepositoryMatchProvisional
		issueAssociationConflict := (finding.IssueDraftID != "" && finding.IssueDraftID != draft.ID) ||
			repositoryFindingHasIssueConflict(state, finding)
		if finding.RepositoryFindingID != "" {
			repositoryFindingIndex := repositoryFindingIndexByID(
				state.RepositoryFindings,
				finding.RepositoryFindingID,
			)
			if repositoryFindingIndex >= 0 {
				repositoryFinding := state.RepositoryFindings[repositoryFindingIndex]
				duplicateReviewRequired = duplicateReviewRequired ||
					repositoryFinding.MatchState == RepositoryMatchProvisional
			}
		}
		if duplicateReviewRequired {
			add(IssuePublicationDuplicateReviewRequired)
		}
		if issueAssociationConflict {
			add(IssuePublicationIssueAssociationConflict)
		}
		if !repositoryFindingAllowsIssueActions(state, finding) &&
			!findingStatusUnresolved && !duplicateReviewRequired && !issueAssociationConflict {
			add(IssuePublicationFindingNotPublishable)
		}
	}

	blockers := make([]IssuePublicationBlocker, 0, len(counts))
	for _, definition := range issuePublicationBlockerDefinitions {
		if count := counts[definition.code]; count > 0 {
			blockers = append(blockers, IssuePublicationBlocker{
				Code: definition.code, Count: count, Message: definition.message,
			})
		}
	}
	return IssuePublicationEligibility{
		CanPublish: len(blockers) == 0, PublishBlockers: blockers,
	}
}

func issuePublicationFindingStatusUnresolved(
	state RepositoryState,
	finding Finding,
) bool {
	if finding.Status != FindingOpen || finding.DeduplicationPending {
		return true
	}
	if finding.RepositoryFindingID != "" {
		return false
	}
	for _, job := range state.MappingJobs {
		if job.ReviewFindingID == finding.ID && job.State != RepositoryMappingCompleted {
			return true
		}
	}
	return false
}

// HasBlocker reports whether an eligibility decision contains code.
func (eligibility IssuePublicationEligibility) HasBlocker(
	code IssuePublicationBlockerCode,
) bool {
	for _, blocker := range eligibility.PublishBlockers {
		if blocker.Code == code {
			return true
		}
	}
	return false
}

// AllowsPostedAcknowledgement preserves idempotent reads of an already-posted
// preview while still rejecting structural or association conflicts. A posted
// preview is expected to have a non-publishable state, a resolved finding
// status, and sometimes an existing-issue origin.
func (eligibility IssuePublicationEligibility) AllowsPostedAcknowledgement() bool {
	for _, blocker := range eligibility.PublishBlockers {
		switch blocker.Code {
		case IssuePublicationRepositoryNotGitHub,
			IssuePublicationOriginNotPublishable,
			IssuePublicationStateNotPublishable,
			IssuePublicationFindingStatusUnresolved:
			continue
		default:
			return false
		}
	}
	return true
}

// IsCanonicalGitHubRepository recognizes the normalized owner/repository
// ledger identity accepted by protected GitHub issue operations.
func IsCanonicalGitHubRepository(repository string) bool {
	owner, name, ok := strings.Cut(repository, "/")
	if !ok || owner == "" || name == "" || strings.Contains(name, "/") ||
		repository != strings.ToLower(repository) {
		return false
	}
	for index, part := range []string{owner, name} {
		if len(part) > 100 || part == "." || part == ".." {
			return false
		}
		for _, character := range part {
			if character >= 'a' && character <= 'z' || character >= '0' && character <= '9' ||
				character == '-' || index == 1 && (character == '_' || character == '.') {
				continue
			}
			return false
		}
	}
	return true
}
