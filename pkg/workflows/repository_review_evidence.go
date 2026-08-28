package workflows

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"github.com/sipeed/picoclaw/pkg/repoaudit"
)

const (
	maxRepositoryReviewManagedEvidenceChildren = 100_000
	maxRepositoryReviewManagedEvidenceBytes    = 32 << 20
	maxRepositoryReviewManagedAssignments      = 128
	// RepositoryReviewRequiredAssignmentsPerReviewer is the fixed built-in
	// correctness/challenge/corroboration/validation task cohort.
	RepositoryReviewRequiredAssignmentsPerReviewer = 4
)

// RepositoryReviewRequiredAssignments returns the fixed required-child
// denominator for a resolved built-in reviewer ensemble.
func RepositoryReviewRequiredAssignments(reviewerCount int) (int, error) {
	if reviewerCount < 1 ||
		reviewerCount > maxRepositoryReviewManagedAssignments/RepositoryReviewRequiredAssignmentsPerReviewer {
		return 0, errors.New("invalid repository review reviewer count")
	}
	return reviewerCount * RepositoryReviewRequiredAssignmentsPerReviewer, nil
}

// RepositoryBugFinderRequiredAssignments applies the built-in default-chain
// semantics: fallback aliases are optional when the default reviewer is used.
func RepositoryBugFinderRequiredAssignments(
	reviewerModels []string,
	includeDefaultReviewer bool,
) (int, error) {
	if includeDefaultReviewer {
		return RepositoryReviewRequiredAssignments(1)
	}
	return RepositoryReviewRequiredAssignments(len(reviewerModels))
}

// RepositoryReviewManagedEvidence is the validated, evidence-derived coverage
// of one persisted managed repository-review step. A successful child with no
// findings still contributes its exact acknowledgements.
type RepositoryReviewManagedEvidence struct {
	Children            []repoaudit.RepositoryReviewEvidence
	Observations        []repoaudit.Observation
	InspectedFiles      []repoaudit.FileRef
	CompletedFiles      []repoaudit.FileRef
	UnsupportedFiles    []repoaudit.UnsupportedFile
	RequiredAssignments int
}

// RepositoryReviewManagedEvidenceOptions supplies terminal files that were
// persisted separately from managed_children and the trusted assignment count
// needed when every pending file was terminal before reviewer dispatch.
type RepositoryReviewManagedEvidenceOptions struct {
	TerminalUnsupportedFiles []repoaudit.FileRef
	RequiredAssignments      int
	AllowLegacyCoreFindings  bool
}

// DecodeRepositoryReviewManagedEvidence applies the same native validation
// semantics used by review.repository record to a durable managed_children
// output. It never derives inspection from assigned scope or finding context.
func DecodeRepositoryReviewManagedEvidence(
	value any,
	plan repoaudit.Plan,
	options ...RepositoryReviewManagedEvidenceOptions,
) (RepositoryReviewManagedEvidence, error) {
	if len(options) > 1 {
		return RepositoryReviewManagedEvidence{}, errors.New("multiple managed evidence options")
	}
	children, err := nativeOptionalMapSlice(value)
	if err != nil {
		return RepositoryReviewManagedEvidence{}, fmt.Errorf("managed children: %w", err)
	}
	if len(children) > maxRepositoryReviewManagedEvidenceChildren {
		return RepositoryReviewManagedEvidence{}, errors.New("too many managed review children")
	}
	allowed := make(map[string]repoaudit.FileRef, len(plan.PendingFiles))
	for _, file := range plan.PendingFiles {
		if _, duplicate := allowed[file.Path]; duplicate || file.Path == "" {
			return RepositoryReviewManagedEvidence{}, errors.New("plan pending files are not unique")
		}
		allowed[file.Path] = file
	}
	if len(allowed) == 0 {
		return RepositoryReviewManagedEvidence{}, errors.New("managed review plan has no pending files")
	}
	terminal := make(map[string]struct{})
	requiredHint := 0
	if len(options) == 1 {
		requiredHint = options[0].RequiredAssignments
		if requiredHint < 0 || requiredHint > maxRepositoryReviewManagedAssignments {
			return RepositoryReviewManagedEvidence{}, errors.New("invalid managed assignment hint")
		}
		for _, file := range options[0].TerminalUnsupportedFiles {
			bound, exists := allowed[file.Path]
			if !exists || bound != file {
				return RepositoryReviewManagedEvidence{}, errors.New("terminal file is outside the exact plan")
			}
			if _, duplicate := terminal[file.Path]; duplicate {
				return RepositoryReviewManagedEvidence{}, errors.New("duplicate terminal file")
			}
			terminal[file.Path] = struct{}{}
		}
	}
	if len(children) == 0 {
		if len(terminal) != len(allowed) || requiredHint < 1 {
			return RepositoryReviewManagedEvidence{}, errors.New("managed children are required")
		}
		return RepositoryReviewManagedEvidence{RequiredAssignments: requiredHint}, nil
	}

	result := RepositoryReviewManagedEvidence{
		Children:         make([]repoaudit.RepositoryReviewEvidence, 0, len(children)),
		Observations:     make([]repoaudit.Observation, 0, len(children)),
		UnsupportedFiles: nativeRepositoryReviewUnsupportedFiles(value),
	}
	requiredCoverage := make(map[string]int, len(allowed))
	successfulCoverage := make(map[string]int, len(allowed))
	inspected := make(map[string]repoaudit.FileRef, len(allowed))
	evidenceBytes := 0
	for index, child := range children {
		scopeFiles, scopeErr := nativeRepositoryReviewFiles(child["scope"])
		if scopeErr != nil {
			return RepositoryReviewManagedEvidence{}, fmt.Errorf("managed child %d scope: %w", index, scopeErr)
		}
		if len(scopeFiles) == 0 {
			return RepositoryReviewManagedEvidence{}, fmt.Errorf("managed child %d scope is empty", index)
		}
		if len(scopeFiles) > maxRepositoryReviewManagedEvidenceChildren {
			return RepositoryReviewManagedEvidence{}, fmt.Errorf("managed child %d scope is too large", index)
		}
		scopeSeen := make(map[string]struct{}, len(scopeFiles))
		for _, file := range scopeFiles {
			evidenceBytes += len(file.Path) + len(file.BlobSHA) + len(file.Category) + len(file.Mode) + 32
			if evidenceBytes > maxRepositoryReviewManagedEvidenceBytes {
				return RepositoryReviewManagedEvidence{}, errors.New("managed review evidence is too large")
			}
			bound, exists := allowed[file.Path]
			if !exists || bound != file {
				return RepositoryReviewManagedEvidence{}, fmt.Errorf(
					"managed child %d scope file %q is outside the exact plan", index, file.Path,
				)
			}
			if _, duplicate := scopeSeen[file.Path]; duplicate {
				return RepositoryReviewManagedEvidence{}, fmt.Errorf(
					"managed child %d duplicates scope file %q", index, file.Path,
				)
			}
			scopeSeen[file.Path] = struct{}{}
		}
		required, declared := child["required"].(bool)
		if !declared {
			required = true
		}
		evidence := repoaudit.RepositoryReviewEvidence{
			AssignmentID: fmt.Sprintf("legacy-managed-child-%06d", index+1),
			ScopeFiles:   append([]repoaudit.FileRef(nil), scopeFiles...),
			Required:     required,
		}
		for _, file := range scopeFiles {
			if required {
				requiredCoverage[file.Path]++
			}
		}

		structured := nativeMapValue(child["structured"])
		valid, _ := child["valid"].(bool)
		_, runFailed := child["run_error"]
		if structured == nil || !valid || runFailed {
			result.Children = append(result.Children, evidence)
			continue
		}
		completedScope := nativeRepositoryReviewCompletedScopePaths(child["scope"])
		acknowledgedPaths, reviewErr := nativeRepositoryReviewAcknowledgedPaths(
			structured, scopeFiles, completedScope,
		)
		if reviewErr != nil {
			// Legacy record treated malformed acknowledgements as an unsuccessful
			// child while retaining it in the required denominator.
			result.Children = append(result.Children, evidence)
			continue
		}
		modelMeta := nativeMapValue(child["model"])
		model := strings.TrimSpace(nativeAnyString(modelMeta["selected"]))
		if model == "" {
			model = strings.TrimSpace(nativeAnyString(modelMeta["default"]))
		}
		reviewer := strings.TrimSpace(nativeAnyString(child["label"]))
		raw := strings.TrimSpace(nativeAnyString(child["text"]))
		observation, parseErr := nativeRepositoryReviewObservation(
			structured, child["scope"], model, reviewer, raw,
		)
		if parseErr != nil && len(options) == 1 && options[0].AllowLegacyCoreFindings {
			observation, parseErr = nativeLegacyRepositoryReviewObservation(
				structured, child["scope"], model, reviewer, raw,
			)
		}
		if parseErr != nil {
			return RepositoryReviewManagedEvidence{}, fmt.Errorf("managed child %d: %w", index, parseErr)
		}
		if strings.TrimSpace(observation.Model) == "" {
			return RepositoryReviewManagedEvidence{}, fmt.Errorf("managed child %d has no model", index)
		}
		acknowledged := make([]repoaudit.FileRef, 0, len(acknowledgedPaths))
		for _, file := range scopeFiles {
			if !acknowledgedPaths[file.Path] {
				continue
			}
			acknowledged = append(acknowledged, file)
			inspected[file.Path] = file
			if required {
				successfulCoverage[file.Path]++
			}
		}
		sort.Slice(acknowledged, func(i, j int) bool { return acknowledged[i].Path < acknowledged[j].Path })
		evidence.Successful = true
		evidence.AcknowledgedFiles = acknowledged
		evidence.Observation = &observation
		result.Children = append(result.Children, evidence)
		result.Observations = append(result.Observations, observation)
	}

	requiredAssignments := -1
	unsupported := make(map[string]struct{}, len(result.UnsupportedFiles)+len(terminal))
	for pathValue := range terminal {
		unsupported[pathValue] = struct{}{}
	}
	for _, file := range result.UnsupportedFiles {
		unsupported[file.Path] = struct{}{}
	}
	for pathValue := range allowed {
		if _, terminal := unsupported[pathValue]; terminal {
			continue
		}
		count := requiredCoverage[pathValue]
		if count < 1 {
			return RepositoryReviewManagedEvidence{}, fmt.Errorf(
				"file %q has no required managed assignment", pathValue,
			)
		}
		if requiredAssignments < 0 {
			requiredAssignments = count
		} else if requiredAssignments != count {
			return RepositoryReviewManagedEvidence{}, fmt.Errorf(
				"file %q has %d required assignments, want %d", pathValue, count, requiredAssignments,
			)
		}
	}
	if requiredAssignments < 1 {
		return RepositoryReviewManagedEvidence{}, errors.New("managed review has no required assignments")
	}
	if requiredAssignments > maxRepositoryReviewManagedAssignments {
		return RepositoryReviewManagedEvidence{}, errors.New("managed review has too many required assignments")
	}
	if requiredHint > 0 && requiredAssignments != requiredHint {
		return RepositoryReviewManagedEvidence{}, errors.New("managed assignment count does not match its hint")
	}
	result.RequiredAssignments = requiredAssignments
	for _, file := range inspected {
		result.InspectedFiles = append(result.InspectedFiles, file)
	}
	for pathValue, total := range requiredCoverage {
		if total > 0 && successfulCoverage[pathValue] == total {
			result.CompletedFiles = append(result.CompletedFiles, allowed[pathValue])
		}
	}
	sort.Slice(result.InspectedFiles, func(i, j int) bool {
		return result.InspectedFiles[i].Path < result.InspectedFiles[j].Path
	})
	sort.Slice(result.CompletedFiles, func(i, j int) bool {
		return result.CompletedFiles[i].Path < result.CompletedFiles[j].Path
	})
	return result, nil
}

func nativeLegacyRepositoryReviewObservation(
	structured map[string]any,
	scopeValue any,
	model string,
	reviewer string,
	raw string,
) (repoaudit.Observation, error) {
	if err := nativeValidateLegacyRepositoryReviewOutput(structured); err != nil {
		return repoaudit.Observation{}, err
	}
	scope, err := nativeRepositoryReviewFiles(scopeValue)
	if err != nil {
		return repoaudit.Observation{}, err
	}
	findingsRaw, _ := nativeOptionalMapSlice(structured["findings"])
	completedPaths := nativeRepositoryReviewCompletedScopePaths(scopeValue)
	reviewedPaths := nativeStringSet(nativeStringSlice(structured["reviewedFiles"]))
	findings := make([]repoaudit.FindingCandidate, 0, len(findingsRaw))
	for _, rawFinding := range findingsRaw {
		data, _ := json.Marshal(rawFinding)
		var finding repoaudit.FindingCandidate
		// The strict legacy schema validator above proved this exact map is a
		// FindingCandidate-compatible JSON object.
		_ = json.Unmarshal(data, &finding)
		pathValue := strings.TrimSpace(filepath.ToSlash(finding.File))
		if completedPaths[pathValue] && reviewedPaths[pathValue] {
			findings = append(findings, finding)
		}
	}
	digest := sha256.Sum256([]byte(raw))
	return repoaudit.Observation{
		Model: model, Reviewer: reviewer, ScopeFiles: scope, Findings: findings,
		Summary:   strings.TrimSpace(nativeAnyString(structured["summary"])),
		RawDigest: "sha256:" + hex.EncodeToString(digest[:]),
	}, nil
}

func nativeValidateLegacyRepositoryReviewOutput(structured map[string]any) error {
	if err := nativeValidateRepositoryReviewObjectFields(structured, map[string]struct{}{
		"summary": {}, "reviewedFiles": {}, "findings": {}, "residualRisks": {},
	}, "legacy output"); err != nil {
		return err
	}
	for _, field := range []string{"summary", "reviewedFiles", "findings", "residualRisks"} {
		if _, exists := structured[field]; !exists {
			return fmt.Errorf("legacy repository review output is missing %q", field)
		}
	}
	summary, ok := structured["summary"].(string)
	if !ok || len(summary) > 65536 {
		return errors.New("legacy repository review summary is invalid")
	}
	for _, field := range []string{"reviewedFiles", "residualRisks"} {
		if err := nativeValidateRepositoryReviewStringArray(structured[field]); err != nil {
			return err
		}
	}
	findings, err := nativeMapSlice(structured["findings"])
	if err != nil || len(findings) > 256 {
		return errors.New("legacy repository review findings are invalid")
	}
	for index, finding := range findings {
		if _, enriched := finding["match_hints"]; enriched {
			return fmt.Errorf("legacy finding %d mixes enrichment schemas", index)
		}
		if _, enriched := finding["fix_effort"]; enriched {
			return fmt.Errorf("legacy finding %d mixes enrichment schemas", index)
		}
		if err := nativeValidateRepositoryReviewObjectFields(finding, map[string]struct{}{
			"severity": {}, "title": {}, "symbol": {}, "file": {}, "line": {},
			"message": {}, "evidence": {}, "impact": {}, "validation": {},
		}, fmt.Sprintf("legacy finding %d", index)); err != nil {
			return err
		}
		for _, field := range []string{
			"severity", "title", "symbol", "file", "message", "evidence", "impact", "validation",
		} {
			if _, exists := finding[field]; !exists {
				return fmt.Errorf("legacy finding %d is missing %q", index, field)
			}
		}
		validation, validationOK := finding["validation"].(map[string]any)
		if !validationOK || nativeValidateRepositoryReviewObjectFields(
			validation,
			map[string]struct{}{"status": {}, "summary": {}, "checks": {}},
			fmt.Sprintf("legacy finding %d validation", index),
		) != nil {
			return fmt.Errorf("legacy finding %d validation is invalid", index)
		}
		for _, field := range []string{"status", "summary", "checks"} {
			if _, exists := validation[field]; !exists {
				return fmt.Errorf("legacy finding %d validation is missing %q", index, field)
			}
		}
		if err := nativeValidateRepositoryReviewStringArray(validation["checks"]); err != nil {
			return fmt.Errorf("legacy finding %d validation checks are invalid", index)
		}
		data, marshalErr := json.Marshal(finding)
		var candidate repoaudit.FindingCandidate
		if marshalErr != nil || json.Unmarshal(data, &candidate) != nil ||
			(candidate.Severity != "critical" && candidate.Severity != "high" &&
				candidate.Severity != "medium" && candidate.Severity != "low") ||
			strings.TrimSpace(candidate.Title) == "" || len(candidate.Title) > 65536 ||
			strings.TrimSpace(candidate.Symbol) == "" || len(candidate.Symbol) > 4096 ||
			strings.TrimSpace(candidate.File) == "" || len(candidate.File) > 4096 ||
			strings.TrimSpace(candidate.Message) == "" || len(candidate.Message) > 65536 ||
			strings.TrimSpace(candidate.Evidence) == "" || len(candidate.Evidence) > 65536 ||
			strings.TrimSpace(candidate.Impact) == "" || len(candidate.Impact) > 65536 ||
			candidate.Validation.Status != "confirmed" ||
			strings.TrimSpace(candidate.Validation.Summary) == "" ||
			len(candidate.Validation.Summary) > 65536 || len(candidate.Validation.Checks) > 128 ||
			(candidate.Line != nil && *candidate.Line < 1) {
			return fmt.Errorf("legacy finding %d core fields are invalid", index)
		}
		for _, check := range candidate.Validation.Checks {
			if len(check) > 4096 {
				return fmt.Errorf("legacy finding %d validation check is invalid", index)
			}
		}
	}
	return nil
}
