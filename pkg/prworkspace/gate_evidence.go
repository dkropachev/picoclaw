package prworkspace

import (
	"encoding/json"
	"sort"
	"strings"
)

// projectGateEvidence deliberately copies only browser-safe, decision-relevant
// fields out of the private gate subject. Adding a field to a gate subject does
// not make it public unless it is explicitly handled here.
func projectGateEvidence(subject any) GateEvidence {
	encoded, err := json.Marshal(subject)
	if err != nil {
		return GateEvidence{}
	}
	var root map[string]json.RawMessage
	if json.Unmarshal(encoded, &root) != nil {
		return GateEvidence{}
	}

	var result GateEvidence
	var charter Charter
	if decodeGateEvidence(root["charter"], &charter) {
		result.CharterType = charter.Type
		result.CharterGoal = strings.TrimSpace(charter.Goal)
	}

	var repair RepairAttempt
	if decodeGateEvidence(root["repair"], &repair) {
		result.CandidateSHA = repair.CandidateSHA
		result.ChangedFiles = append(result.ChangedFiles, repair.ChangedFiles...)
		result.FindingIDs = append(result.FindingIDs, repair.FindingIDs...)
		if hasScopeEvidence(repair.Scope) {
			scope := projectScopeGateEvidence(repair.Scope)
			result.Scope = &scope
		}
		result.HardScope = result.HardScope || HardCandidateScopeBlocker(repair.Scope)
	}

	var scope ScopeAssessment
	if decodeGateEvidence(root["scope"], &scope) && hasScopeEvidence(scope) {
		scope = projectScopeGateEvidence(scope)
		result.Scope = &scope
		result.HardScope = result.HardScope || HardCandidateScopeBlocker(scope)
	}

	var validation ValidationRun
	if decodeGateEvidence(root["validation"], &validation) {
		result.ValidationState = validation.State
		result.ValidationChecks = append([]ValidationCheck(nil), validation.Checks...)
		for index := range result.ValidationChecks {
			result.ValidationChecks[index].Summary = ""
		}
		if validation.CandidateSHA != "" {
			result.CandidateSHA = validation.CandidateSHA
		}
	}

	var publication Publication
	if decodeGateEvidence(root["publication"], &publication) {
		result.PublicationKind = publication.Kind
		result.PayloadDigest = publication.PayloadDigest
		result.ExpectedHeadSHA = publication.ExpectedHeadSHA
		result.FindingIDs = append(result.FindingIDs, publication.FindingIDs...)
	}
	if request := root["request"]; len(request) != 0 {
		switch result.PublicationKind {
		case PublicationGitHubReview:
			var value reviewPublicationPayload
			if decodeGateEvidence(request, &value) {
				result.Repository = value.Provider.Repository
				result.ReviewSummary = value.Summary
				for _, finding := range value.Findings {
					result.PublicationFindings = append(result.PublicationFindings, GateFindingEvidence{
						ID: finding.ID, Title: finding.Title, File: finding.File,
						Line: cloneIntPointer(finding.Line), Message: finding.Message,
					})
				}
			}
		case PublicationBranchPush:
			var value branchPublicationPayload
			if decodeGateEvidence(request, &value) {
				result.Repository = value.Provider.Repository
				result.CandidateSHA = value.Repair.CandidateSHA
				result.ChangedFiles = append(result.ChangedFiles, value.Repair.ChangedFiles...)
				result.RepairSummary = value.Repair.ResultSummary
				if result.Scope == nil && hasScopeEvidence(value.Repair.Scope) {
					scope := projectScopeGateEvidence(value.Repair.Scope)
					result.Scope = &scope
				}
			}
		case PublicationGitHubIssue:
			var value issuePublicationPayload
			if decodeGateEvidence(request, &value) {
				result.Repository = value.Repository
				result.IssueTitle = value.Title
				result.IssueBody = value.Body
				result.IssueLabels = append([]string(nil), value.Labels...)
				result.FindingIDs = append(result.FindingIDs, value.FindingIDs...)
			}
		}
	}
	_ = json.Unmarshal(root["provider_revision"], &result.ProviderRevision)

	var findings []Finding
	if decodeGateEvidence(root["findings"], &findings) {
		result.FindingCount += len(findings)
		for _, finding := range findings {
			result.FindingIDs = append(result.FindingIDs, finding.ID)
			result.ChangedFiles = append(result.ChangedFiles, finding.File)
		}
	}
	var finding Finding
	if decodeGateEvidence(root["finding"], &finding) {
		result.FindingCount++
		result.FindingIDs = append(result.FindingIDs, finding.ID)
		result.ChangedFiles = append(result.ChangedFiles, finding.File)
		if result.Scope == nil && hasScopeEvidence(finding.Scope) {
			scope := projectScopeGateEvidence(finding.Scope)
			result.Scope = &scope
		}
		if HardCandidateScopeBlocker(finding.Scope) {
			result.HardScope = true
			result.HardScopeFindingIDs = append(result.HardScopeFindingIDs, finding.ID)
		}
	}
	var drift []Finding
	if decodeGateEvidence(root["candidate_drift"], &drift) {
		result.FindingCount += len(drift)
		for _, finding := range drift {
			result.FindingIDs = append(result.FindingIDs, finding.ID)
			result.ScopeResolutionIDs = append(result.ScopeResolutionIDs, finding.ID)
			result.ChangedFiles = append(result.ChangedFiles, finding.File)
			if HardCandidateScopeBlocker(finding.Scope) {
				result.HardScope = true
				result.HardScopeFindingIDs = append(result.HardScopeFindingIDs, finding.ID)
			}
		}
	}

	var group DeferredGroup
	if decodeGateEvidence(root["group"], &group) {
		result.FindingIDs = append(result.FindingIDs, group.FindingIDs...)
		if result.Scope == nil && hasScopeEvidence(group.Scope) {
			scope := projectScopeGateEvidence(group.Scope)
			result.Scope = &scope
		}
	}
	if result.Scope != nil {
		for _, change := range result.Scope.ChangeEvidence {
			result.ChangedFiles = append(result.ChangedFiles, change.Path)
		}
	}
	result.ChangedFiles = uniqueSortedGateEvidence(result.ChangedFiles)
	result.FindingIDs = uniqueSortedGateEvidence(result.FindingIDs)
	result.HardScopeFindingIDs = uniqueSortedGateEvidence(result.HardScopeFindingIDs)
	result.ScopeResolutionIDs = uniqueSortedGateEvidence(result.ScopeResolutionIDs)
	if result.FindingCount < len(result.FindingIDs) {
		result.FindingCount = len(result.FindingIDs)
	}
	return result
}

func decodeGateEvidence(raw json.RawMessage, target any) bool {
	return len(raw) != 0 && json.Unmarshal(raw, target) == nil
}

func hasScopeEvidence(value ScopeAssessment) bool {
	return value.Distance != "" || value.Size != "" || value.Presence != "" ||
		value.Files != 0 || value.SemanticLines != 0 || value.Modules != 0 ||
		value.Explanation != "" || len(value.ChangeEvidence) != 0
}

func projectScopeGateEvidence(value ScopeAssessment) ScopeAssessment {
	result := value
	result.CharterClauses = append([]string(nil), value.CharterClauses...)
	result.ChangeEvidence = append([]ScopeChange(nil), value.ChangeEvidence...)
	for index := range result.ChangeEvidence {
		result.ChangeEvidence[index].Hunk = ""
		result.ChangeEvidence[index].CharterClauses = append(
			[]string(nil),
			result.ChangeEvidence[index].CharterClauses...)
	}
	return result
}

func uniqueSortedGateEvidence(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	sort.Strings(result)
	return result
}
