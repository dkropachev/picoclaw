package prworkspace

import (
	"context"
	"errors"
	"strings"
)

type CodeTreeEntry struct {
	Name string `json:"name"`
	Path string `json:"path"`
	Type string `json:"type"`
}

type CodeTreePage struct {
	Revision string          `json:"revision"`
	Path     string          `json:"path"`
	Entries  []CodeTreeEntry `json:"entries"`
	Next     string          `json:"next_cursor,omitempty"`
}

type CodeBlob struct {
	Revision  string `json:"revision"`
	Path      string `json:"path"`
	Content   string `json:"content"`
	Truncated bool   `json:"truncated"`
}

type CodeDiff struct {
	Revision          string  `json:"revision"`
	BaseRevision      string  `json:"base_revision"`
	CandidateRevision string  `json:"candidate_revision"`
	Path              string  `json:"path,omitempty"`
	Original          *string `json:"original,omitempty"`
	Modified          *string `json:"modified,omitempty"`
	UnifiedDiff       string  `json:"unified_diff"`
}

type CandidateCodeBrowser interface {
	ListCodeTree(
		ctx context.Context,
		repair RepairAttempt,
		revision, path, after string,
	) (CodeTreePage, error)
	ReadCodeBlob(ctx context.Context, repair RepairAttempt, revision, path string) (CodeBlob, error)
}

func (service *Service) ListCodeTree(
	ctx context.Context, workspaceID, revision, path, after string,
) (CodeTreePage, error) {
	repair, browser, err := service.codeBrowserState(ctx, workspaceID)
	if err != nil {
		return CodeTreePage{}, err
	}
	return browser.ListCodeTree(ctx, repair, revision, path, after)
}

func (service *Service) ReadCodeBlob(
	ctx context.Context, workspaceID, revision, candidateRevision, path string,
) (CodeBlob, error) {
	repair, browser, err := service.codeBrowserState(ctx, workspaceID)
	if err != nil {
		return CodeBlob{}, err
	}
	if revision == "base" && candidateRevision != repair.CandidateSHA {
		return CodeBlob{}, ErrConflict
	}
	return browser.ReadCodeBlob(ctx, repair, revision, path)
}

func (service *Service) ReadCodeDiff(
	ctx context.Context, workspaceID, revision, path string,
) (CodeDiff, error) {
	if !validOpaqueID(workspaceID, "devw_") || service.candidateEvidence == nil {
		return CodeDiff{}, ErrInvalid
	}
	aggregate, err := service.store.Get(ctx, workspaceID)
	if err != nil {
		return CodeDiff{}, err
	}
	repair, ok := latestBrowsableRepair(aggregate)
	if !ok || revision != repair.CandidateSHA {
		return CodeDiff{}, errors.New("candidate code is unavailable")
	}
	evidence, err := service.candidateEvidence.LoadCandidateEvidence(ctx, repair)
	if err != nil {
		return CodeDiff{}, err
	}
	diff := evidence.CandidateDiff
	if path != "" {
		diff = filterUnifiedDiffPath(diff, path)
	}
	result := CodeDiff{
		Revision: evidence.EvidenceDigest, BaseRevision: repair.PublicationFence.BaseCommit,
		CandidateRevision: repair.CandidateSHA, Path: path, UnifiedDiff: diff,
	}
	if path != "" {
		if browser, ok := service.candidateEvidence.(CandidateCodeBrowser); ok {
			if original, readErr := browser.ReadCodeBlob(
				ctx,
				repair,
				repair.PublicationFence.BaseCommit,
				path,
			); readErr == nil {
				result.Original = &original.Content
			}
			if modified, readErr := browser.ReadCodeBlob(ctx, repair, repair.CandidateSHA, path); readErr == nil {
				result.Modified = &modified.Content
			}
		}
	}
	return result, nil
}

func (service *Service) codeBrowserState(
	ctx context.Context, workspaceID string,
) (RepairAttempt, CandidateCodeBrowser, error) {
	if !validOpaqueID(workspaceID, "devw_") {
		return RepairAttempt{}, nil, ErrInvalid
	}
	browser, ok := service.candidateEvidence.(CandidateCodeBrowser)
	if !ok {
		return RepairAttempt{}, nil, errors.New("candidate code browser is unavailable")
	}
	aggregate, err := service.store.Get(ctx, workspaceID)
	if err != nil {
		return RepairAttempt{}, nil, err
	}
	repair, found := latestBrowsableRepair(aggregate)
	if !found {
		return RepairAttempt{}, nil, errors.New("candidate code is unavailable")
	}
	return repair, browser, nil
}

func latestBrowsableRepair(aggregate Aggregate) (RepairAttempt, bool) {
	for index := len(aggregate.RepairAttempts) - 1; index >= 0; index-- {
		repair := aggregate.RepairAttempts[index]
		if repair.PublicationFence != nil && repair.CandidateSHA != "" {
			return repair, true
		}
	}
	return RepairAttempt{}, false
}

func filterUnifiedDiffPath(diff, wanted string) string {
	if wanted == "" || strings.ContainsAny(wanted, "\x00\r\n") {
		return ""
	}
	var result []string
	include := false
	for _, line := range strings.SplitAfter(diff, "\n") {
		if strings.HasPrefix(line, "diff --git ") {
			include = strings.Contains(line, " a/"+wanted+" b/"+wanted)
		}
		if include {
			result = append(result, line)
		}
	}
	return strings.Join(result, "")
}
