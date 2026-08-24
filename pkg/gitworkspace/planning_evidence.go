package gitworkspace

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"unicode/utf8"
)

const (
	maxPlanningManifestBytes = 8 << 20
	maxPlanningEvidenceBytes = 1 << 20
	maxPlanningFiles         = 160
	maxPlanningFileBytes     = 32 << 10
)

type PlanningEvidenceFile struct {
	Path    string `json:"path"`
	Mode    string `json:"mode"`
	Size    int64  `json:"size"`
	Content string `json:"content,omitempty"`
}

type PlanningEvidence struct {
	Commit    string                 `json:"commit"`
	Files     []PlanningEvidenceFile `json:"files"`
	Truncated bool                   `json:"truncated"`
}

// SnapshotPinnedPlanningEvidence returns bounded exact committed repository
// text under the same reservation and control-plane checks used by edits. It
// exposes no checkout path, remote URL, credentials, or mutation capability.
func (m *Manager) SnapshotPinnedPlanningEvidence(
	ctx context.Context,
	request PinnedCandidateRequest,
) (PlanningEvidence, error) {
	if m == nil {
		return PlanningEvidence{}, errors.New("git workspace manager is not configured")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	repository, err := validatePinnedAcquireRequest(ctx, request.Pin)
	if err != nil || !validPinnedOperationIdentity(request.WorkspaceID, 256) {
		return PlanningEvidence{}, fmt.Errorf("%w: planning evidence request is invalid", ErrPinnedLineInvalid)
	}
	_, _, release, err := m.acquirePinnedOperation(ctx, request.Pin.ReservationKey)
	if err != nil {
		return PlanningEvidence{}, err
	}
	defer release()
	m.mu.Lock()
	defer m.mu.Unlock()
	unlock, err := m.lockInventory(ctx)
	if err != nil {
		return PlanningEvidence{}, err
	}
	defer unlock()
	state, err := m.loadLocked()
	if err != nil {
		return PlanningEvidence{}, err
	}
	environment, cleanup, err := m.newPinnedGitEnvironment()
	if err != nil {
		return PlanningEvidence{}, err
	}
	defer cleanup()
	workspace, err := m.pinnedWorkspaceForOperation(
		ctx, state, request.Pin, request.WorkspaceID, repository, environment,
	)
	if err != nil {
		return PlanningEvidence{}, err
	}
	output, err := runPinnedGitPlumbing(ctx, workspace.Path, environment, nil, maxPlanningManifestBytes,
		"ls-tree", "-r", "-l", "-z", request.Pin.ExpectedCommit)
	if err != nil {
		return PlanningEvidence{}, fmt.Errorf("read planning manifest: %w", err)
	}
	result := PlanningEvidence{Commit: request.Pin.ExpectedCommit}
	entries := bytes.Split(bytes.TrimSuffix(output, []byte{0}), []byte{0})
	encodedBytes := 0
	for _, raw := range entries {
		if len(raw) == 0 {
			continue
		}
		metadata, path, ok := strings.Cut(string(raw), "\t")
		fields := strings.Fields(metadata)
		if !ok || len(fields) != 4 || !validDevelopmentLineReviewPath(path) {
			return PlanningEvidence{}, fmt.Errorf("%w: planning manifest is invalid", ErrPinnedLineConflict)
		}
		size := int64(0)
		if fields[3] != "-" {
			var parseErr error
			size, parseErr = strconv.ParseInt(fields[3], 10, 64)
			if parseErr != nil || size < 0 {
				return PlanningEvidence{}, fmt.Errorf("%w: planning blob size is invalid", ErrPinnedLineConflict)
			}
		} else if fields[1] != "commit" {
			return PlanningEvidence{}, fmt.Errorf("%w: planning blob size is invalid", ErrPinnedLineConflict)
		}
		file := PlanningEvidenceFile{Path: path, Mode: fields[0], Size: size}
		if len(result.Files) >= maxPlanningFiles {
			result.Truncated = true
			break
		}
		if fields[1] == "blob" && fields[0] != "120000" && size <= maxPlanningFileBytes &&
			planningTextCandidate(path) && encodedBytes+int(size) <= maxPlanningEvidenceBytes {
			content, readErr := runPinnedGitPlumbing(ctx, workspace.Path, environment, nil,
				maxPlanningFileBytes, "cat-file", "blob", request.Pin.ExpectedCommit+":"+path)
			if readErr == nil && utf8.Valid(content) && bytes.IndexByte(content, 0) < 0 {
				file.Content = string(content)
				encodedBytes += len(content)
			}
		}
		result.Files = append(result.Files, file)
	}
	if _, err := json.Marshal(result); err != nil {
		return PlanningEvidence{}, err
	}
	return result, nil
}

func planningTextCandidate(path string) bool {
	lower := strings.ToLower(path)
	for _, marker := range []string{"/vendor/", "/node_modules/", "/dist/", "/.git/"} {
		if strings.Contains("/"+lower, marker) {
			return false
		}
	}
	for _, suffix := range []string{
		".go", ".ts", ".tsx", ".js", ".jsx", ".rs", ".py", ".java", ".kt", ".c", ".h",
		".cpp", ".hpp", ".cs", ".rb", ".php", ".swift", ".sh", ".sql", ".yaml", ".yml",
		".json", ".toml", ".md", ".css", ".scss", ".html", ".xml", ".proto",
	} {
		if strings.HasSuffix(lower, suffix) {
			return true
		}
	}
	return strings.HasSuffix(lower, "makefile") || strings.HasSuffix(lower, "dockerfile")
}
