package gitworkspace

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	pathpkg "path"
	"sort"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	developmentLineBrowseTimeout  = 30 * time.Second
	maxDevelopmentBrowseTreeBytes = 8 << 20
	maxDevelopmentBrowseBlobBytes = 1 << 20
	maxDevelopmentBrowseEntries   = 500
)

type PinnedLineBrowseFence struct {
	LineID          string
	ExpectedVersion int64
	ExpectedBase    string
	ExpectedTip     string
	ExpectedTree    string
}

type PinnedLineTreeRequest struct {
	PinnedLineBrowseFence
	Revision string
	Path     string
	After    string
}

type PinnedLineTreeEntry struct {
	Path string `json:"path"`
	Type string `json:"type"`
}

type PinnedLineTreePage struct {
	Entries []PinnedLineTreeEntry `json:"entries"`
	Next    string                `json:"next,omitempty"`
}

type PinnedLineBlobRequest struct {
	PinnedLineBrowseFence
	Revision string
	Path     string
}

type PinnedLineBlob struct {
	Path     string `json:"path"`
	Revision string `json:"revision"`
	Content  string `json:"content"`
}

func (m *Manager) ListPinnedLineTree(ctx context.Context, request PinnedLineTreeRequest) (PinnedLineTreePage, error) {
	var page PinnedLineTreePage
	err := m.withPinnedLineBrowse(ctx, request.PinnedLineBrowseFence, func(
		ctx context.Context, directory string, environment []string,
	) error {
		revision, err := pinnedBrowseRevision(request.Revision, request.ExpectedBase, request.ExpectedTip)
		if err != nil {
			return err
		}
		if request.Path != strings.TrimSpace(request.Path) || pathpkg.IsAbs(request.Path) {
			return fmt.Errorf("%w: browse path is invalid", ErrPinnedLineInvalid)
		}
		prefix := strings.Trim(request.Path, "/")
		if prefix != "" && !validDevelopmentLineReviewPath(prefix) {
			return fmt.Errorf("%w: browse path is invalid", ErrPinnedLineInvalid)
		}
		if request.After != strings.TrimSpace(request.After) ||
			(request.After != "" && !validDevelopmentLineReviewPath(request.After)) {
			return fmt.Errorf("%w: browse cursor is invalid", ErrPinnedLineInvalid)
		}
		if request.After != "" {
			parent := pathpkg.Dir(request.After)
			if parent == "." {
				parent = ""
			}
			if parent != prefix {
				return fmt.Errorf("%w: browse cursor is outside the directory", ErrPinnedLineInvalid)
			}
		}
		treeish := revision
		if prefix != "" {
			treeish += ":" + prefix
		}
		args := []string{"ls-tree", "-z", treeish}
		output, runErr := runPinnedGitPlumbing(ctx, directory, environment, nil, maxDevelopmentBrowseTreeBytes, args...)
		if runErr != nil {
			return fmt.Errorf("list pinned development files: %w", runErr)
		}
		parts := bytes.Split(bytes.TrimSuffix(output, []byte{0}), []byte{0})
		entries := make([]PinnedLineTreeEntry, 0, len(parts))
		for _, raw := range parts {
			if len(raw) == 0 {
				continue
			}
			header, name, ok := bytes.Cut(raw, []byte{'\t'})
			fields := strings.Fields(string(header))
			if !ok || len(fields) != 3 || (fields[1] != "blob" && fields[1] != "tree" && fields[1] != "commit") {
				return fmt.Errorf("%w: repository tree entry is invalid", ErrPinnedLineConflict)
			}
			path := pathpkg.Join(prefix, string(name))
			if !validDevelopmentLineReviewPath(path) || pathpkg.Dir(path) != mapRootDirectory(prefix) {
				return fmt.Errorf("%w: repository path is invalid", ErrPinnedLineConflict)
			}
			if request.After != "" && path <= request.After {
				continue
			}
			entryType := "file"
			if fields[1] == "tree" {
				entryType = "directory"
			}
			entries = append(entries, PinnedLineTreeEntry{Path: path, Type: entryType})
		}
		sort.Slice(entries, func(i, j int) bool { return entries[i].Path < entries[j].Path })
		if len(entries) > maxDevelopmentBrowseEntries {
			page.Next = entries[maxDevelopmentBrowseEntries-1].Path
			entries = entries[:maxDevelopmentBrowseEntries]
		}
		page.Entries = entries
		return nil
	})
	return page, err
}

func mapRootDirectory(prefix string) string {
	if prefix == "" {
		return "."
	}
	return prefix
}

func (m *Manager) ReadPinnedLineBlob(ctx context.Context, request PinnedLineBlobRequest) (PinnedLineBlob, error) {
	var result PinnedLineBlob
	err := m.withPinnedLineBrowse(ctx, request.PinnedLineBrowseFence, func(
		ctx context.Context, directory string, environment []string,
	) error {
		if !validDevelopmentLineReviewPath(request.Path) {
			return fmt.Errorf("%w: blob path is invalid", ErrPinnedLineInvalid)
		}
		revision, err := pinnedBrowseRevision(request.Revision, request.ExpectedBase, request.ExpectedTip)
		if err != nil {
			return err
		}
		entry, lsErr := runPinnedGitPlumbing(ctx, directory, environment, nil, 8<<10,
			"ls-tree", revision, "--", request.Path)
		if lsErr != nil || len(entry) == 0 {
			return fmt.Errorf("%w: blob is unavailable", ErrPinnedLineConflict)
		}
		fields := strings.Fields(string(entry))
		if len(fields) < 3 || fields[1] != "blob" || fields[0] == "120000" || fields[0] == "160000" {
			return fmt.Errorf("%w: symlink, submodule, or non-file blob is unavailable", ErrPinnedLineConflict)
		}
		content, readErr := runPinnedGitPlumbing(ctx, directory, environment, nil,
			maxDevelopmentBrowseBlobBytes, "cat-file", "blob", revision+":"+request.Path)
		if readErr != nil {
			return fmt.Errorf("read pinned development blob: %w", readErr)
		}
		if !utf8.Valid(content) || bytes.IndexByte(content, 0) >= 0 {
			return fmt.Errorf("%w: binary blob is unavailable", ErrPinnedLineConflict)
		}
		result = PinnedLineBlob{Path: request.Path, Revision: request.Revision, Content: string(content)}
		return nil
	})
	return result, err
}

func pinnedBrowseRevision(revision, base, tip string) (string, error) {
	switch revision {
	case "base", base:
		return base, nil
	case "candidate", tip:
		return tip, nil
	default:
		return "", fmt.Errorf("%w: browse revision is invalid", ErrPinnedLineInvalid)
	}
}

func (m *Manager) withPinnedLineBrowse(
	ctx context.Context,
	fence PinnedLineBrowseFence,
	read func(context.Context, string, []string) error,
) error {
	if m == nil || read == nil || !validPinnedOperationIdentity(fence.LineID, maxDevelopmentLineIdentityBytes) ||
		fence.ExpectedVersion < 1 || !validPinnedCommit(fence.ExpectedBase) ||
		!validPinnedCommit(fence.ExpectedTip) || !validPinnedCommit(fence.ExpectedTree) {
		return fmt.Errorf("%w: browse fence is invalid", ErrPinnedLineInvalid)
	}
	if ctx == nil {
		ctx = context.Background()
	}
	ctx, cancel := context.WithTimeout(ctx, developmentLineBrowseTimeout)
	defer cancel()
	m.mu.Lock()
	defer m.mu.Unlock()
	unlock, err := m.lockInventory(ctx)
	if err != nil {
		return err
	}
	defer unlock()
	state, err := m.loadLocked()
	if err != nil {
		return err
	}
	line := state.DevelopmentLines[fence.LineID]
	if line == nil || line.State != developmentLineParked || line.Version != fence.ExpectedVersion ||
		line.LastParkPreviousTip != fence.ExpectedBase || line.Tip != fence.ExpectedTip ||
		line.Tree != fence.ExpectedTree {
		return fmt.Errorf("%w: browse fence changed", ErrPinnedLineConflict)
	}
	workspace := state.Workspaces[line.WorkspaceID]
	if workspace == nil || workspace.DroppedAt != nil || workspace.LockedBy != nil ||
		workspace.DevelopmentLineID != line.ID {
		return fmt.Errorf("%w: browse workspace is unavailable", ErrPinnedLineConflict)
	}
	environment, cleanup, err := m.newPinnedGitEnvironment()
	if err != nil {
		return err
	}
	defer cleanup()
	if err := m.verifyDevelopmentLineParkedWorkspace(ctx, workspace, line, workspace.RemoteURL,
		fence.ExpectedTip, fence.ExpectedTree, environment); err != nil {
		return err
	}
	if err := read(ctx, workspace.Path, environment); err != nil {
		return err
	}
	if err := m.verifyDevelopmentLineParkedWorkspace(ctx, workspace, line, workspace.RemoteURL,
		fence.ExpectedTip, fence.ExpectedTree, environment); err != nil {
		return errors.Join(ErrPinnedLineConflict, err)
	}
	return nil
}
