package prworkspace

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
)

type DeferredGroupDraft struct {
	Title      string   `json:"title"`
	Body       string   `json:"body"`
	FindingIDs []string `json:"finding_ids"`
	Labels     []string `json:"labels"`
}

type DeferredGroupingOutput struct {
	Groups []DeferredGroupDraft `json:"groups"`
}

type RegroupDeferredRequest struct {
	WorkspaceID     string
	ExpectedVersion int64
	RequestID       string
}

func (service *Service) RegroupDeferred(ctx context.Context, request RegroupDeferredRequest) (Aggregate, error) {
	if service == nil || service.ai.Runner == nil ||
		!validMutationEnvelope(request.WorkspaceID, request.ExpectedVersion, request.RequestID) {
		return Aggregate{}, ErrInvalid
	}
	aggregate, getErr := service.store.Get(ctx, request.WorkspaceID)
	if getErr != nil {
		return Aggregate{}, getErr
	}
	if aggregate.Workspace.Version != request.ExpectedVersion {
		return aggregate, ErrConflict
	}
	deferred := groupableDeferredFindings(aggregate)
	if len(deferred) == 0 {
		return aggregate, fmt.Errorf("%w: no deferred findings", ErrInvalid)
	}
	bundle := contextBundle(aggregate)
	bundle.Findings = deferred
	prompt, promptErr := CompilePrompt(PromptDeferredIssue, bundle, "")
	if promptErr != nil {
		return Aggregate{}, promptErr
	}
	execution, runErr := service.ai.Runner.RunIsolated(ctx, IsolatedAIRequest{
		Operation: "deferred.group", SystemPrompt: prompt.SystemPrompt,
		UserPrompt: prompt.UserPrompt, Schema: deferredGroupingSchema(),
	})
	if runErr != nil {
		return Aggregate{}, runErr
	}
	var output DeferredGroupingOutput
	if err := decodeStructured(execution.Structured, &output); err != nil {
		return Aggregate{}, errors.New("deferred grouping output is invalid")
	}
	now := service.now().UTC()
	groups, groupsErr := validateAndMaterializeGroups(aggregate, deferred, output.Groups, request.RequestID, now)
	if groupsErr != nil {
		return Aggregate{}, groupsErr
	}
	updates := append(groups, supersededDeferredDrafts(aggregate.DeferredGroups, now)...)
	result, mutateErr := service.store.Mutate(ctx, Mutation{
		WorkspaceID: request.WorkspaceID, ExpectedVersion: request.ExpectedVersion,
		RequestID: request.RequestID,
		Patch: AggregatePatch{
			UpsertDeferred: updates,
			Activity: []Activity{
				{
					Kind:      "deferred.regrouped",
					Actor:     "ai",
					Summary:   fmt.Sprintf("Deferred findings grouped into %d issue drafts", len(groups)),
					CreatedAt: service.now().UTC(),
				},
			},
		},
	})
	if mutateErr != nil {
		return result.Aggregate, mutateErr
	}
	return result.Aggregate, nil
}

type UpdateDeferredRequest struct {
	WorkspaceID     string
	GroupID         string
	ExpectedVersion int64
	RequestID       string
	Title           string
	Body            string
	Labels          []string
}

func (service *Service) UpdateDeferred(ctx context.Context, request UpdateDeferredRequest) (Aggregate, error) {
	if !validMutationEnvelope(request.WorkspaceID, request.ExpectedVersion, request.RequestID) ||
		!validOpaqueID(request.GroupID, "pdg_") || !validBoundedText(request.Title, 1024, false) ||
		!validBoundedText(request.Body, 64<<10, false) || !validLabels(request.Labels) {
		return Aggregate{}, ErrInvalid
	}
	aggregate, err := service.store.Get(ctx, request.WorkspaceID)
	if err != nil {
		return Aggregate{}, err
	}
	group, ok := findDeferredGroup(aggregate.DeferredGroups, request.GroupID)
	if !ok || deferredGroupExternallyBound(group) || aggregate.Workspace.Version != request.ExpectedVersion {
		return aggregate, ErrConflict
	}
	group.Title, group.Body, group.Labels = request.Title, request.Body, append([]string(nil), request.Labels...)
	group.Version++
	group.UpdatedAt = service.now().UTC()
	return service.applyDeferredGroups(
		ctx,
		aggregate,
		request.ExpectedVersion,
		request.RequestID,
		[]DeferredGroup{group},
		"deferred.edited",
		"Deferred issue draft edited",
	)
}

type SplitDeferredRequest struct {
	WorkspaceID     string
	GroupID         string
	ExpectedVersion int64
	RequestID       string
	FindingIDs      []string
}

func (service *Service) SplitDeferred(ctx context.Context, request SplitDeferredRequest) (Aggregate, error) {
	if !validMutationEnvelope(request.WorkspaceID, request.ExpectedVersion, request.RequestID) ||
		!validOpaqueID(request.GroupID, "pdg_") || len(request.FindingIDs) == 0 ||
		hasDuplicateStrings(request.FindingIDs) {
		return Aggregate{}, ErrInvalid
	}
	aggregate, err := service.store.Get(ctx, request.WorkspaceID)
	if err != nil {
		return Aggregate{}, err
	}
	group, ok := findDeferredGroup(aggregate.DeferredGroups, request.GroupID)
	if !ok || deferredGroupExternallyBound(group) || aggregate.Workspace.Version != request.ExpectedVersion {
		return aggregate, ErrConflict
	}
	if hasDuplicateStrings(group.FindingIDs) {
		return aggregate, ErrInvalid
	}
	moving := make(map[string]struct{}, len(request.FindingIDs))
	for _, id := range request.FindingIDs {
		moving[id] = struct{}{}
	}
	var remain, split []string
	for _, id := range group.FindingIDs {
		if _, selected := moving[id]; selected {
			split = append(split, id)
			delete(moving, id)
		} else {
			remain = append(remain, id)
		}
	}
	if len(moving) != 0 || len(remain) == 0 || len(split) == 0 {
		return aggregate, ErrInvalid
	}
	now := service.now().UTC()
	group.FindingIDs, group.Version, group.UpdatedAt = remain, group.Version+1, now
	newGroup := DeferredGroup{
		ID:    stableID("pdg_", aggregate.Workspace.ID, request.RequestID),
		Title: group.Title + " (split)", Body: group.Body, FindingIDs: split,
		Scope: group.Scope, Labels: append([]string(nil), group.Labels...), Version: 1,
		CreatedAt: now, UpdatedAt: now,
	}
	return service.applyDeferredGroups(
		ctx,
		aggregate,
		request.ExpectedVersion,
		request.RequestID,
		[]DeferredGroup{group, newGroup},
		"deferred.split",
		"Deferred issue draft split",
	)
}

type MergeDeferredRequest struct {
	WorkspaceID     string
	GroupIDs        []string
	ExpectedVersion int64
	RequestID       string
	Title           string
	Body            string
}

func (service *Service) MergeDeferred(ctx context.Context, request MergeDeferredRequest) (Aggregate, error) {
	if !validMutationEnvelope(request.WorkspaceID, request.ExpectedVersion, request.RequestID) ||
		len(request.GroupIDs) < 2 || !validBoundedText(request.Title, 1024, false) ||
		!validBoundedText(request.Body, 64<<10, false) {
		return Aggregate{}, ErrInvalid
	}
	aggregate, err := service.store.Get(ctx, request.WorkspaceID)
	if err != nil {
		return Aggregate{}, err
	}
	if aggregate.Workspace.Version != request.ExpectedVersion {
		return aggregate, ErrConflict
	}
	var findingIDs, labels []string
	seenGroups, seenFindings, seenLabels := map[string]struct{}{}, map[string]struct{}{}, map[string]struct{}{}
	for _, id := range request.GroupIDs {
		if _, duplicate := seenGroups[id]; duplicate {
			return aggregate, ErrInvalid
		}
		seenGroups[id] = struct{}{}
		group, ok := findDeferredGroup(aggregate.DeferredGroups, id)
		if !ok || deferredGroupExternallyBound(group) {
			return aggregate, ErrConflict
		}
		for _, findingID := range group.FindingIDs {
			if _, duplicate := seenFindings[findingID]; duplicate {
				return aggregate, ErrInvalid
			}
			seenFindings[findingID] = struct{}{}
			findingIDs = append(findingIDs, findingID)
		}
		for _, label := range group.Labels {
			if _, exists := seenLabels[label]; !exists {
				seenLabels[label] = struct{}{}
				labels = append(labels, label)
			}
		}
	}
	sort.Strings(findingIDs)
	sort.Strings(labels)
	now := service.now().UTC()
	merged := DeferredGroup{
		ID: stableID("pdg_", aggregate.Workspace.ID, request.RequestID), Title: request.Title,
		Body: request.Body, FindingIDs: findingIDs, Labels: labels,
		Scope: aggregateScope(aggregate.Findings, findingIDs), Version: 1, CreatedAt: now, UpdatedAt: now,
	}
	// Old groups remain immutable audit records but are emptied so active UI does
	// not publish them again.
	updates := []DeferredGroup{merged}
	for _, id := range request.GroupIDs {
		group, _ := findDeferredGroup(aggregate.DeferredGroups, id)
		group.FindingIDs = nil
		group.Version++
		group.UpdatedAt = now
		updates = append(updates, group)
	}
	return service.applyDeferredGroups(
		ctx,
		aggregate,
		request.ExpectedVersion,
		request.RequestID,
		updates,
		"deferred.merged",
		"Deferred issue drafts merged",
	)
}

type LinkDeferredRequest struct {
	WorkspaceID      string
	GroupID          string
	ExpectedVersion  int64
	RequestID        string
	ExistingIssueURL string
}

func (service *Service) LinkDeferred(ctx context.Context, request LinkDeferredRequest) (Aggregate, error) {
	if !validMutationEnvelope(request.WorkspaceID, request.ExpectedVersion, request.RequestID) ||
		!validOpaqueID(request.GroupID, "pdg_") || len(request.ExistingIssueURL) > 4096 {
		return Aggregate{}, ErrInvalid
	}
	aggregate, err := service.store.Get(ctx, request.WorkspaceID)
	if err != nil {
		return Aggregate{}, err
	}
	group, ok := findDeferredGroup(aggregate.DeferredGroups, request.GroupID)
	if !ok || group.PublicationID != "" || aggregate.Workspace.Version != request.ExpectedVersion {
		return aggregate, ErrConflict
	}
	if !activeDeferredGroupValid(aggregate, group) ||
		!validExistingDeferredIssueURL(aggregate.ProviderSnapshot, request.ExistingIssueURL) {
		return aggregate, ErrInvalid
	}
	group.ExistingIssueURL = request.ExistingIssueURL
	group.Version++
	now := service.now().UTC()
	group.UpdatedAt = now
	rewardedFindings := rewardDeferredFindings(aggregate.Findings, group.FindingIDs, "user_linked_deferred_issue")
	for index := range rewardedFindings {
		rewardedFindings[index].UpdatedAt = now
	}
	result, err := service.store.Mutate(ctx, Mutation{
		WorkspaceID: aggregate.Workspace.ID, ExpectedVersion: aggregate.Workspace.Version,
		RequestID: request.RequestID,
		Patch: AggregatePatch{
			UpsertDeferred: []DeferredGroup{group},
			UpsertFindings: rewardedFindings,
			ReplaceNudgeRounds: recomputeNudgeRoundRewards(
				aggregate.NudgeRounds,
				upsertByID(aggregate.Findings, rewardedFindings, func(value Finding) string { return value.ID }),
			),
			Activity: []Activity{
				{
					Kind:      "deferred.linked",
					Actor:     "user",
					Summary:   "Deferred work linked to existing issue",
					CreatedAt: now,
				},
			},
		},
	})
	if err != nil {
		return result.Aggregate, err
	}
	return result.Aggregate, nil
}

func deferredGroupExternallyBound(group DeferredGroup) bool {
	return group.PublicationID != "" || group.ExistingIssueURL != ""
}

func hasDuplicateStrings(values []string) bool {
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		if _, duplicate := seen[value]; duplicate {
			return true
		}
		seen[value] = struct{}{}
	}
	return false
}

func (service *Service) applyDeferredGroups(
	ctx context.Context,
	aggregate Aggregate,
	expectedVersion int64,
	requestID string,
	groups []DeferredGroup,
	kind, summary string,
) (Aggregate, error) {
	result, err := service.store.Mutate(ctx, Mutation{
		WorkspaceID: aggregate.Workspace.ID, ExpectedVersion: expectedVersion,
		RequestID: requestID,
		Patch: AggregatePatch{
			UpsertDeferred: groups,
			Activity:       []Activity{{Kind: kind, Actor: "user", Summary: summary, CreatedAt: service.now().UTC()}},
		},
	})
	if err != nil {
		return result.Aggregate, err
	}
	return result.Aggregate, nil
}

func validateAndMaterializeGroups(
	aggregate Aggregate,
	eligible []Finding,
	drafts []DeferredGroupDraft,
	requestID string,
	now time.Time,
) ([]DeferredGroup, error) {
	if len(drafts) == 0 || len(drafts) > 128 {
		return nil, ErrInvalid
	}
	deferred := make(map[string]Finding)
	for _, finding := range eligible {
		deferred[finding.ID] = finding
	}
	seen := make(map[string]struct{}, len(deferred))
	groups := make([]DeferredGroup, 0, len(drafts))
	for index, draft := range drafts {
		if !validBoundedText(draft.Title, 1024, false) || !validBoundedText(draft.Body, 64<<10, false) ||
			len(draft.FindingIDs) == 0 || !validLabels(draft.Labels) {
			return nil, ErrInvalid
		}
		for _, id := range draft.FindingIDs {
			if _, exists := deferred[id]; !exists {
				return nil, ErrInvalid
			}
			if _, duplicate := seen[id]; duplicate {
				return nil, ErrInvalid
			}
			seen[id] = struct{}{}
		}
		groups = append(groups, DeferredGroup{
			ID:    stableID("pdg_", aggregate.Workspace.ID, requestID, fmt.Sprint(index)),
			Title: draft.Title, Body: draft.Body, FindingIDs: append([]string(nil), draft.FindingIDs...),
			Scope: aggregateScope(aggregate.Findings, draft.FindingIDs), Labels: append([]string(nil), draft.Labels...),
			Version: 1, CreatedAt: now, UpdatedAt: now,
		})
	}
	if len(seen) != len(deferred) {
		return nil, errors.New("deferred grouping omitted a finding")
	}
	return groups, nil
}

func groupableDeferredFindings(aggregate Aggregate) []Finding {
	resolved := make(map[string]struct{})
	for _, group := range aggregate.DeferredGroups {
		if group.PublicationID == "" && group.ExistingIssueURL == "" {
			continue
		}
		for _, findingID := range group.FindingIDs {
			resolved[findingID] = struct{}{}
		}
	}
	var result []Finding
	for _, finding := range aggregate.Findings {
		if finding.Disposition != FindingDeferred {
			continue
		}
		if _, alreadyResolved := resolved[finding.ID]; !alreadyResolved {
			result = append(result, finding)
		}
	}
	return result
}

func supersededDeferredDrafts(groups []DeferredGroup, now time.Time) []DeferredGroup {
	var updates []DeferredGroup
	for _, group := range groups {
		if len(group.FindingIDs) == 0 || group.PublicationID != "" || group.ExistingIssueURL != "" {
			continue
		}
		group.FindingIDs = nil
		group.Version++
		group.UpdatedAt = now
		updates = append(updates, group)
	}
	return updates
}

func activeDeferredGroupValid(aggregate Aggregate, group DeferredGroup) bool {
	if len(group.FindingIDs) == 0 || group.PublicationID != "" || group.ExistingIssueURL != "" {
		return false
	}
	seen := make(map[string]struct{}, len(group.FindingIDs))
	for _, findingID := range group.FindingIDs {
		if _, duplicate := seen[findingID]; duplicate {
			return false
		}
		seen[findingID] = struct{}{}
		finding, index := findFinding(aggregate.Findings, findingID)
		if index < 0 || finding.Disposition != FindingDeferred {
			return false
		}
		for _, other := range aggregate.DeferredGroups {
			if other.ID == group.ID || len(other.FindingIDs) == 0 {
				continue
			}
			for _, otherFindingID := range other.FindingIDs {
				if otherFindingID == findingID {
					return false
				}
			}
		}
	}
	return true
}

func validExistingDeferredIssueURL(provider ProviderSnapshot, raw string) bool {
	issue, err := url.ParseRequestURI(raw)
	if err != nil || issue.Scheme != "https" || issue.Host == "" || issue.User != nil ||
		issue.RawQuery != "" || issue.Fragment != "" {
		return false
	}
	origin, err := url.ParseRequestURI(provider.ProviderOrigin)
	if err != nil || !strings.EqualFold(issue.Scheme, origin.Scheme) ||
		!strings.EqualFold(issue.Host, origin.Host) {
		return false
	}
	prefix := strings.TrimSuffix(origin.Path, "/") + "/" + provider.Repository + "/issues/"
	number, ok := strings.CutPrefix(issue.Path, prefix)
	if !ok || number == "" || strings.Contains(number, "/") {
		return false
	}
	value, err := strconv.ParseInt(number, 10, 64)
	return err == nil && value > 0
}

func aggregateScope(findings []Finding, ids []string) ScopeAssessment {
	wanted := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		wanted[id] = struct{}{}
	}
	result := ScopeAssessment{
		Distance:       ScopeExact,
		Size:           ChangeSizeXS,
		TypeCompatible: true,
		Confidence:     1,
		Estimated:      true,
	}
	for _, finding := range findings {
		if _, exists := wanted[finding.ID]; !exists {
			continue
		}
		if scopeDistanceRank(finding.Scope.Distance) > scopeDistanceRank(result.Distance) {
			result.Distance = finding.Scope.Distance
		}
		if changeSizeRank(finding.Scope.Size) > changeSizeRank(result.Size) {
			result.Size = finding.Scope.Size
		}
		result.Files += finding.Scope.Files
		result.SemanticLines += finding.Scope.SemanticLines
		result.Modules += finding.Scope.Modules
		result.TypeCompatible = result.TypeCompatible && finding.Scope.TypeCompatible
		if finding.Scope.Confidence < result.Confidence {
			result.Confidence = finding.Scope.Confidence
		}
	}
	return result
}

func scopeDistanceRank(value ScopeDistance) int {
	switch value {
	case ScopeExact:
		return 0
	case ScopeNecessaryAdjacent:
		return 1
	case ScopeRelatedFollowup:
		return 2
	default:
		return 3
	}
}

func changeSizeRank(value ChangeSize) int {
	switch value {
	case ChangeSizeXS:
		return 0
	case ChangeSizeS:
		return 1
	case ChangeSizeM:
		return 2
	default:
		return 3
	}
}

func findDeferredGroup(values []DeferredGroup, id string) (DeferredGroup, bool) {
	for _, value := range values {
		if value.ID == id {
			return value, true
		}
	}
	return DeferredGroup{}, false
}

func validLabels(values []string) bool {
	if len(values) > 32 {
		return false
	}
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		if !validBoundedText(value, 128, false) {
			return false
		}
		key := strings.ToLower(value)
		if _, duplicate := seen[key]; duplicate {
			return false
		}
		seen[key] = struct{}{}
	}
	return true
}

func deferredGroupingSchema() map[string]any {
	return map[string]any{
		"type": "object", "required": []any{"groups"}, "additionalProperties": false,
		"properties": map[string]any{
			"groups": map[string]any{
				"type": "array", "maxItems": maxAIReviewFindings,
				"items": map[string]any{
					"type": "object", "additionalProperties": false,
					"required": []any{"title", "body", "finding_ids", "labels"},
					"properties": map[string]any{
						"title": map[string]any{"type": "string"},
						"body":  map[string]any{"type": "string"},
						"finding_ids": map[string]any{
							"type":     "array",
							"maxItems": maxAIReviewFindings,
							"items":    map[string]any{"type": "string"},
						},
						"labels": map[string]any{
							"type":     "array",
							"maxItems": 32,
							"items":    map[string]any{"type": "string"},
						},
					},
				},
			},
		},
	}
}
