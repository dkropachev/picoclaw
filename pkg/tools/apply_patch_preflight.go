package tools

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"unicode/utf8"
)

type applyPatchPathFence struct {
	path       string
	exists     bool
	info       os.FileInfo
	mode       os.FileMode
	linkTarget string
}

type applyPatchProtectedRoot struct {
	lexical                 string
	canonical               string
	containsWorkspace       bool
	allowWorkspaceException bool
	trackConstructionLeaf   bool
	constructionLeaf        applyPatchPathFence
	fences                  []applyPatchPathFence
}

type applyPatchWorkspace struct {
	lexical   string
	canonical string
	info      os.FileInfo
	fences    []applyPatchPathFence
}

type applyPatchFileSnapshot struct {
	path      string
	info      os.FileInfo
	mode      os.FileMode
	data      []byte
	linkCount uint64
}

type plannedApplyPatchOp struct {
	kind        string
	sourceLabel string
	targetLabel string
	sourcePath  string
	targetPath  string
	source      *applyPatchFileSnapshot
	before      []byte
	after       []byte
	mode        os.FileMode
	summary     string
}

type applyPatchPlan struct {
	workspace      applyPatchWorkspace
	protectedRoots []applyPatchProtectedRoot
	fences         []applyPatchPathFence
	ops            []plannedApplyPatchOp
	summaries      []string
}

type applyPatchGateEntry struct {
	identity  string
	info      os.FileInfo
	semaphore chan struct{}
	refs      int
}

type applyPatchGateCoordinator struct {
	mu      sync.Mutex
	entries []*applyPatchGateEntry
}

var globalApplyPatchGates applyPatchGateCoordinator

func (coordinator *applyPatchGateCoordinator) lock(
	ctx context.Context,
	workspace string,
) (applyPatchWorkspace, func(), error) {
	snapshot, err := snapshotApplyPatchWorkspace(workspace)
	if err != nil {
		return applyPatchWorkspace{}, nil, err
	}

	coordinator.mu.Lock()
	var entry *applyPatchGateEntry
	for _, candidate := range coordinator.entries {
		if candidate.identity == snapshot.canonical ||
			candidate.info != nil && snapshot.info != nil && os.SameFile(candidate.info, snapshot.info) {
			entry = candidate
			break
		}
	}
	if entry == nil {
		entry = &applyPatchGateEntry{
			identity:  snapshot.canonical,
			info:      snapshot.info,
			semaphore: make(chan struct{}, 1),
		}
		coordinator.entries = append(coordinator.entries, entry)
	}
	entry.refs++
	coordinator.mu.Unlock()

	select {
	case entry.semaphore <- struct{}{}:
	case <-ctx.Done():
		coordinator.releaseReference(entry)
		return applyPatchWorkspace{}, nil, ctx.Err()
	}

	var once sync.Once
	return snapshot, func() {
		once.Do(func() {
			<-entry.semaphore
			coordinator.releaseReference(entry)
		})
	}, nil
}

func (coordinator *applyPatchGateCoordinator) releaseReference(entry *applyPatchGateEntry) {
	coordinator.mu.Lock()
	defer coordinator.mu.Unlock()
	entry.refs--
	if entry.refs != 0 {
		return
	}
	for index, candidate := range coordinator.entries {
		if candidate == entry {
			coordinator.entries = append(coordinator.entries[:index], coordinator.entries[index+1:]...)
			return
		}
	}
}

func snapshotApplyPatchWorkspace(workspace string) (applyPatchWorkspace, error) {
	if workspace == "" || workspace != strings.TrimSpace(workspace) ||
		!utf8.ValidString(workspace) || strings.ContainsRune(workspace, '\x00') {
		return applyPatchWorkspace{}, fmt.Errorf("apply-patch workspace is invalid")
	}
	lexical, err := filepath.Abs(filepath.Clean(workspace))
	if err != nil {
		return applyPatchWorkspace{}, fmt.Errorf("resolve apply-patch workspace: %w", err)
	}
	canonical, err := filepath.EvalSymlinks(lexical)
	if err != nil {
		return applyPatchWorkspace{}, fmt.Errorf("resolve apply-patch workspace: %w", err)
	}
	canonical = filepath.Clean(canonical)
	info, err := os.Stat(canonical)
	if err != nil || !info.IsDir() {
		return applyPatchWorkspace{}, fmt.Errorf("apply-patch workspace is not a directory")
	}
	fences, err := captureApplyPatchPathFences(lexical)
	if err != nil {
		return applyPatchWorkspace{}, fmt.Errorf("inspect apply-patch workspace: %w", err)
	}
	return applyPatchWorkspace{
		lexical: lexical, canonical: canonical, info: info, fences: fences,
	}, nil
}

func prepareApplyPatchProtectedRoots(
	workspace string,
	roots []string,
) ([]applyPatchProtectedRoot, error) {
	return prepareApplyPatchProtectedRootsWithMode(workspace, roots, true, true)
}

func prepareApplyPatchProtectedRootsWithMode(
	workspace string,
	roots []string,
	allowWorkspaceException bool,
	trackConstructionLeaf bool,
) ([]applyPatchProtectedRoot, error) {
	if len(roots) == 0 {
		return nil, nil
	}
	workspacePath := workspace
	if workspacePath == "" {
		workspacePath = "."
	}
	workspaceAbs, err := filepath.Abs(filepath.Clean(workspacePath))
	if err != nil {
		return nil, fmt.Errorf("prepare apply-patch protected roots: invalid workspace")
	}
	prepared := make([]applyPatchProtectedRoot, 0, len(roots))
	for index, root := range append([]string(nil), roots...) {
		if root == "" || root != strings.TrimSpace(root) || !utf8.ValidString(root) ||
			strings.ContainsRune(root, '\x00') {
			return nil, fmt.Errorf("apply-patch protected root %d is invalid", index)
		}
		lexical := filepath.Clean(root)
		if !filepath.IsAbs(lexical) {
			lexical = filepath.Join(workspaceAbs, lexical)
		}
		lexical, err = filepath.Abs(lexical)
		if err != nil {
			return nil, fmt.Errorf("apply-patch protected root %d is invalid", index)
		}
		canonical, resolveErr := resolveApplyPatchPathAgainstExistingAncestor(lexical)
		if resolveErr != nil {
			return nil, fmt.Errorf("apply-patch protected root %d cannot be resolved", index)
		}
		fences, fenceErr := captureApplyPatchPathFences(lexical)
		if fenceErr != nil || len(fences) == 0 {
			return nil, fmt.Errorf("apply-patch protected root %d cannot be inspected", index)
		}
		prepared = append(prepared, applyPatchProtectedRoot{
			lexical: lexical, canonical: canonical,
			allowWorkspaceException: allowWorkspaceException,
			trackConstructionLeaf:   trackConstructionLeaf,
			constructionLeaf:        fences[0],
		})
	}
	return prepared, nil
}

func (t *ApplyPatchTool) planPatch(
	ctx context.Context,
	workspace applyPatchWorkspace,
	ops []codexPatchOp,
) (*applyPatchPlan, error) {
	protectedRoots, err := snapshotApplyPatchProtectedRoots(workspace, t.protectedRoots)
	if err != nil {
		return nil, err
	}
	plan := &applyPatchPlan{workspace: workspace, protectedRoots: protectedRoots}
	plan.fences = append(plan.fences, workspace.fences...)
	for _, root := range plan.protectedRoots {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		plan.fences = append(plan.fences, root.fences...)
	}

	seenPaths := make(map[string]string)
	seenSources := make([]*applyPatchFileSnapshot, 0)
	for _, op := range ops {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		planned, opFences, err := t.planPatchOperation(ctx, plan, op)
		if err != nil {
			return nil, err
		}
		for _, role := range plannedApplyPatchRoles(planned) {
			key := applyPatchPathKey(role.path)
			for priorPath, priorLabel := range seenPaths {
				if applyPatchPathsOverlap(key, priorPath) {
					return nil, fmt.Errorf(
						"patch path conflict between %s and %s",
						priorLabel,
						role.label,
					)
				}
			}
			seenPaths[key] = role.label
		}
		if planned.source != nil {
			for _, prior := range seenSources {
				if os.SameFile(planned.source.info, prior.info) {
					return nil, fmt.Errorf("patch source file identity is duplicated")
				}
			}
			seenSources = append(seenSources, planned.source)
		}
		plan.fences = append(plan.fences, opFences...)
		plan.ops = append(plan.ops, planned)
		plan.summaries = append(plan.summaries, planned.summary)
	}
	plan.fences = dedupeApplyPatchFences(plan.fences)
	return plan, nil
}

func applyPatchPathsOverlap(left, right string) bool {
	return applyPatchPathWithinIdentity(left, right) ||
		applyPatchPathWithinIdentity(right, left)
}

func snapshotApplyPatchProtectedRoots(
	workspace applyPatchWorkspace,
	configured []applyPatchProtectedRoot,
) ([]applyPatchProtectedRoot, error) {
	snapshots := make([]applyPatchProtectedRoot, 0, len(configured))
	for index, root := range configured {
		canonical, err := resolveApplyPatchPathAgainstExistingAncestor(root.lexical)
		if err != nil || canonical != root.canonical {
			return nil, fmt.Errorf("apply-patch protected root %d cannot be resolved", index)
		}
		if root.trackConstructionLeaf && root.constructionLeaf.exists {
			if fenceErr := revalidateApplyPatchFence(root.constructionLeaf); fenceErr != nil {
				return nil, fmt.Errorf("apply-patch protected root %d changed", index)
			}
		}
		lexicalFences, err := captureApplyPatchPathFences(root.lexical)
		if err != nil {
			return nil, fmt.Errorf("apply-patch protected root %d cannot be inspected", index)
		}
		canonicalFences, err := captureApplyPatchPathFences(canonical)
		if err != nil {
			return nil, fmt.Errorf("apply-patch protected root %d cannot be inspected", index)
		}
		snapshots = append(snapshots, applyPatchProtectedRoot{
			lexical: root.lexical, canonical: canonical,
			containsWorkspace: canonical != workspace.canonical &&
				applyPatchExistingAncestorContains(canonical, workspace.canonical),
			allowWorkspaceException: root.allowWorkspaceException,
			trackConstructionLeaf:   root.trackConstructionLeaf,
			constructionLeaf:        root.constructionLeaf,
			fences: dedupeApplyPatchFences(
				append(lexicalFences, canonicalFences...),
			),
		})
	}
	return snapshots, nil
}

type applyPatchRole struct {
	path  string
	label string
}

func plannedApplyPatchRoles(op plannedApplyPatchOp) []applyPatchRole {
	if op.kind == "update" && op.sourcePath == op.targetPath {
		return []applyPatchRole{{path: op.sourcePath, label: op.sourceLabel}}
	}
	roles := make([]applyPatchRole, 0, 2)
	if op.sourcePath != "" {
		roles = append(roles, applyPatchRole{path: op.sourcePath, label: op.sourceLabel})
	}
	if op.targetPath != "" {
		roles = append(roles, applyPatchRole{path: op.targetPath, label: op.targetLabel})
	}
	return roles
}

func (t *ApplyPatchTool) planPatchOperation(
	ctx context.Context,
	plan *applyPatchPlan,
	op codexPatchOp,
) (plannedApplyPatchOp, []applyPatchPathFence, error) {
	if err := validateApplyPatchPermission(t, op); err != nil {
		return plannedApplyPatchOp{}, nil, err
	}
	if err := t.guardApplyPatchPath(ctx, op.path, false); err != nil {
		return plannedApplyPatchOp{}, nil, err
	}
	sourceCandidate, err := t.resolveApplyPatchCandidate(plan, op.path)
	if err != nil {
		return plannedApplyPatchOp{}, nil, err
	}

	planned := plannedApplyPatchOp{
		kind: op.kind, sourceLabel: op.path,
	}
	fences := append([]applyPatchPathFence(nil), sourceCandidate.fences...)
	switch op.kind {
	case "add":
		if sourceCandidate.exists {
			return plannedApplyPatchOp{}, nil, fmt.Errorf("add file %q failed: file already exists", op.path)
		}
		planned.sourcePath = ""
		planned.targetPath = sourceCandidate.canonical
		planned.targetLabel = op.path
		planned.after, err = appendApplyPatchBytesContext(ctx, nil, op.add)
		if err != nil {
			return plannedApplyPatchOp{}, nil, err
		}
		planned.mode = applyPatchFileMode()
		planned.summary = fmt.Sprintf("added %s", op.path)
	case "delete":
		source, snapshotErr := snapshotApplyPatchSource(
			ctx,
			sourceCandidate,
			op.path,
			t.beforeSourceOpen,
		)
		if snapshotErr != nil {
			return plannedApplyPatchOp{}, nil, snapshotErr
		}
		planned.sourcePath = sourceCandidate.canonical
		planned.source = source
		planned.before, err = appendApplyPatchBytesContext(ctx, nil, source.data)
		if err != nil {
			return plannedApplyPatchOp{}, nil, err
		}
		planned.mode = source.mode
		planned.summary = fmt.Sprintf("deleted %s", op.path)
	case "update":
		source, snapshotErr := snapshotApplyPatchSource(
			ctx,
			sourceCandidate,
			op.path,
			t.beforeSourceOpen,
		)
		if snapshotErr != nil {
			return plannedApplyPatchOp{}, nil, snapshotErr
		}
		updated, updateErr := applyCodexPatchHunks(ctx, source.data, op.path, op.hunks)
		if updateErr != nil {
			return plannedApplyPatchOp{}, nil, updateErr
		}
		planned.sourcePath = sourceCandidate.canonical
		planned.targetPath = sourceCandidate.canonical
		planned.targetLabel = op.path
		planned.source = source
		planned.before, err = appendApplyPatchBytesContext(ctx, nil, source.data)
		if err != nil {
			return plannedApplyPatchOp{}, nil, err
		}
		planned.after = updated
		planned.mode = source.mode
		planned.summary = fmt.Sprintf("updated %s", op.path)
		if op.moveTo != "" {
			if err := t.guardApplyPatchPath(ctx, op.moveTo, true); err != nil {
				return plannedApplyPatchOp{}, nil, err
			}
			target, targetErr := t.resolveApplyPatchCandidate(plan, op.moveTo)
			if targetErr != nil {
				return plannedApplyPatchOp{}, nil, targetErr
			}
			if target.exists {
				return plannedApplyPatchOp{}, nil, fmt.Errorf(
					"move file %q failed: destination already exists",
					op.moveTo,
				)
			}
			planned.kind = "move"
			planned.targetPath = target.canonical
			planned.targetLabel = op.moveTo
			planned.summary = fmt.Sprintf("moved %s to %s", op.path, op.moveTo)
			fences = append(fences, target.fences...)
		}
	default:
		return plannedApplyPatchOp{}, nil, fmt.Errorf("unsupported patch operation %q", op.kind)
	}
	return planned, fences, nil
}

func validateApplyPatchPermission(t *ApplyPatchTool, op codexPatchOp) error {
	switch op.kind {
	case "add", "delete":
		if !t.allowCreate {
			return fmt.Errorf("%s file %q failed: write_file is disabled", op.kind, op.path)
		}
	case "update":
		if !t.allowUpdate {
			return fmt.Errorf("update file %q failed: edit_file is disabled", op.path)
		}
		if op.moveTo != "" && !t.allowCreate {
			return fmt.Errorf("move file %q failed: write_file is disabled", op.path)
		}
	}
	return nil
}

func (t *ApplyPatchTool) guardApplyPatchPath(ctx context.Context, path string, move bool) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if applyPatchPathHasGitAlias(path) {
		return fmt.Errorf("patch path is denied: Git control paths are not allowed")
	}
	if err := validateApplyPatchPlatformPath(path); err != nil {
		return err
	}
	if t.pathGuard == nil {
		return nil
	}
	if err := t.pathGuard(path); err != nil {
		if move {
			return fmt.Errorf("patch move path %q is denied: %v", path, err)
		}
		return fmt.Errorf("patch path %q is denied: %v", path, err)
	}
	return ctx.Err()
}

type applyPatchCandidate struct {
	lexical   string
	canonical string
	exists    bool
	info      os.FileInfo
	fences    []applyPatchPathFence
}

func (t *ApplyPatchTool) resolveApplyPatchCandidate(
	plan *applyPatchPlan,
	label string,
) (applyPatchCandidate, error) {
	if label == "" || !utf8.ValidString(label) || strings.ContainsRune(label, '\x00') {
		return applyPatchCandidate{}, fmt.Errorf("patch file path is invalid")
	}
	lexical, err := t.resolveWritePath(label)
	if err != nil {
		return applyPatchCandidate{}, err
	}
	lexical = filepath.Clean(lexical)
	info, lstatErr := os.Lstat(lexical)
	if lstatErr == nil && info.Mode()&os.ModeSymlink != 0 {
		return applyPatchCandidate{}, fmt.Errorf("patch path %q is a terminal symlink", label)
	}
	if lstatErr != nil && !os.IsNotExist(lstatErr) {
		return applyPatchCandidate{}, fmt.Errorf("inspect patch path %q: %w", label, lstatErr)
	}
	if ancestorErr := validateApplyPatchAncestorChain(filepath.Dir(lexical)); ancestorErr != nil {
		return applyPatchCandidate{}, fmt.Errorf(
			"inspect patch path %q ancestors: %w",
			label,
			ancestorErr,
		)
	}
	canonical, err := resolveApplyPatchPathAgainstExistingAncestor(lexical)
	if err != nil {
		return applyPatchCandidate{}, fmt.Errorf("resolve patch path %q: %w", label, err)
	}
	if authorizeErr := t.authorizeApplyPatchCanonical(plan, canonical); authorizeErr != nil {
		return applyPatchCandidate{}, authorizeErr
	}
	if t.beforePathFence != nil {
		t.beforePathFence(label)
	}
	fences, err := captureApplyPatchPathFences(lexical)
	if err != nil {
		return applyPatchCandidate{}, fmt.Errorf("inspect patch path %q: %w", label, err)
	}
	canonicalFences, err := captureApplyPatchPathFences(canonical)
	if err != nil {
		return applyPatchCandidate{}, fmt.Errorf("inspect patch path %q: %w", label, err)
	}
	fences = dedupeApplyPatchFences(append(fences, canonicalFences...))
	currentInfo, currentErr := os.Lstat(lexical)
	if lstatErr == nil {
		if currentErr != nil || currentInfo.Mode()&os.ModeSymlink != 0 ||
			currentInfo.Mode() != info.Mode() || !os.SameFile(currentInfo, info) {
			return applyPatchCandidate{}, fmt.Errorf("patch path %q changed during preflight", label)
		}
	} else if !os.IsNotExist(currentErr) {
		return applyPatchCandidate{}, fmt.Errorf("patch path %q changed during preflight", label)
	}
	coherentCanonical, coherentErr := resolveApplyPatchPathAgainstExistingAncestor(lexical)
	if coherentErr != nil || coherentCanonical != canonical {
		return applyPatchCandidate{}, fmt.Errorf("patch path %q changed during preflight", label)
	}
	if authorizeErr := t.authorizeApplyPatchCanonical(plan, coherentCanonical); authorizeErr != nil {
		return applyPatchCandidate{}, authorizeErr
	}
	if lstatErr == nil {
		canonicalInfo, statErr := os.Lstat(canonical)
		if statErr != nil {
			return applyPatchCandidate{}, fmt.Errorf("inspect patch path %q: %w", label, statErr)
		}
		if canonicalInfo.Mode()&os.ModeSymlink != 0 || !os.SameFile(currentInfo, canonicalInfo) {
			return applyPatchCandidate{}, fmt.Errorf("patch path %q changed during preflight", label)
		}
		info = canonicalInfo
	} else {
		parentInfo, statErr := statApplyPatchExistingAncestor(filepath.Dir(canonical))
		if statErr != nil || !parentInfo.IsDir() {
			return applyPatchCandidate{}, fmt.Errorf("patch path %q parent is not a directory", label)
		}
	}
	return applyPatchCandidate{
		lexical: lexical, canonical: canonical,
		exists: lstatErr == nil, info: info, fences: fences,
	}, nil
}

func (t *ApplyPatchTool) authorizeApplyPatchCanonical(
	plan *applyPatchPlan,
	canonical string,
) error {
	if platformErr := validateApplyPatchPlatformPath(canonical); platformErr != nil {
		return platformErr
	}
	if applyPatchPathHasGitAlias(canonical) {
		return fmt.Errorf("patch path is denied: Git control paths are not allowed")
	}
	insideWorkspace := applyPatchPathWithinWorkspace(canonical, plan.workspace)
	if t.restrict && !insideWorkspace && !isAllowedPath(canonical, t.allowPaths) {
		return fmt.Errorf("access denied: path is outside the workspace")
	}
	for _, root := range plan.protectedRoots {
		workspaceException := root.allowWorkspaceException &&
			root.containsWorkspace && insideWorkspace
		if applyPatchPathWithinIdentity(canonical, root.canonical) && !workspaceException {
			return fmt.Errorf("patch path is protected")
		}
	}
	return nil
}

func validateApplyPatchAncestorChain(path string) error {
	for current := filepath.Clean(path); ; current = filepath.Dir(current) {
		info, err := os.Lstat(current)
		switch {
		case err == nil && info.Mode()&os.ModeSymlink != 0:
			resolved, resolveErr := filepath.EvalSymlinks(current)
			if resolveErr != nil {
				return fmt.Errorf("symlink is dangling or invalid")
			}
			resolvedInfo, statErr := os.Stat(resolved)
			if statErr != nil || !resolvedInfo.IsDir() {
				return fmt.Errorf("symlink ancestor is not a directory")
			}
		case err == nil && !info.IsDir():
			return fmt.Errorf("ancestor is not a directory")
		case err != nil && !os.IsNotExist(err):
			return err
		}
		parent := filepath.Dir(current)
		if parent == current {
			return nil
		}
	}
}

func statApplyPatchExistingAncestor(path string) (os.FileInfo, error) {
	for current := filepath.Clean(path); ; current = filepath.Dir(current) {
		info, err := os.Stat(current)
		if err == nil {
			return info, nil
		}
		if !os.IsNotExist(err) {
			return nil, err
		}
		parent := filepath.Dir(current)
		if parent == current {
			return nil, os.ErrNotExist
		}
	}
}

func snapshotApplyPatchSource(
	ctx context.Context,
	candidate applyPatchCandidate,
	label string,
	beforeOpen func(string),
) (*applyPatchFileSnapshot, error) {
	if !candidate.exists {
		return nil, fmt.Errorf("patch source %q does not exist", label)
	}
	if candidate.info == nil || !candidate.info.Mode().IsRegular() {
		return nil, fmt.Errorf("patch source %q must be a regular file", label)
	}
	if beforeOpen != nil {
		beforeOpen(candidate.canonical)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	file, err := openApplyPatchSource(candidate.canonical)
	if err != nil {
		return nil, fmt.Errorf("read patch source %q: %w", label, err)
	}
	defer file.Close()
	beforeInfo, err := file.Stat()
	if err != nil || !beforeInfo.Mode().IsRegular() ||
		candidate.info == nil || !os.SameFile(candidate.info, beforeInfo) {
		return nil, fmt.Errorf("inspect patch source %q", label)
	}
	linkCount, err := applyPatchLinkCount(file, beforeInfo)
	if err != nil {
		return nil, fmt.Errorf("inspect patch source %q links: %w", label, err)
	}
	if linkCount != 1 {
		return nil, fmt.Errorf("patch source %q has multiple links", label)
	}
	data, err := readApplyPatchSourceContext(ctx, file)
	if err != nil {
		return nil, fmt.Errorf("read patch source %q: %w", label, err)
	}
	afterInfo, err := file.Stat()
	if err != nil || !os.SameFile(beforeInfo, afterInfo) || beforeInfo.Mode() != afterInfo.Mode() {
		return nil, fmt.Errorf("patch source %q changed while reading", label)
	}
	afterLinks, err := applyPatchLinkCount(file, afterInfo)
	if err != nil || afterLinks != linkCount {
		return nil, fmt.Errorf("patch source %q links changed while reading", label)
	}
	return &applyPatchFileSnapshot{
		path: candidate.canonical, info: beforeInfo,
		mode: beforeInfo.Mode(), data: data,
		linkCount: linkCount,
	}, nil
}

func readApplyPatchSourceContext(ctx context.Context, file *os.File) ([]byte, error) {
	const chunkSize = 64 * 1024
	data := make([]byte, 0)
	buffer := make([]byte, chunkSize)
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		count, err := file.Read(buffer)
		if count > 0 {
			data = append(data, buffer[:count]...)
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				return data, nil
			}
			return nil, err
		}
	}
}

func applyCodexPatchHunks(
	ctx context.Context,
	before []byte,
	label string,
	hunks []codexPatchHunk,
) ([]byte, error) {
	updated, err := appendApplyPatchBytesContext(ctx, nil, before)
	if err != nil {
		return nil, err
	}
	for _, hunk := range hunks {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		oldText, newText, err := codexPatchHunkBytesContext(ctx, hunk)
		if err != nil {
			return nil, err
		}
		if hunk.endOfFile {
			if len(oldText) == 0 {
				updated, err = appendApplyPatchBytesContext(ctx, updated, newText)
				if err != nil {
					return nil, err
				}
				continue
			}
			matchIndex, matches, matchErr := findUniqueApplyPatchMatch(ctx, updated, oldText)
			if matchErr != nil {
				return nil, matchErr
			}
			if matches > 1 {
				return nil, fmt.Errorf("update file %q failed: hunk context is ambiguous", label)
			}
			if matches == 0 || matchIndex+len(oldText) != len(updated) {
				return nil, fmt.Errorf("update file %q failed: hunk context not found at end of file", label)
			}
			updated, err = replaceApplyPatchMatchContext(ctx, updated, matchIndex, len(oldText), newText)
			if err != nil {
				return nil, err
			}
			continue
		}
		matchIndex, matches, matchErr := findUniqueApplyPatchMatch(ctx, updated, oldText)
		if matchErr != nil {
			return nil, matchErr
		}
		if matches == 0 {
			return nil, fmt.Errorf("update file %q failed: hunk context not found", label)
		}
		if matches > 1 {
			return nil, fmt.Errorf("update file %q failed: hunk context is ambiguous", label)
		}
		updated, err = replaceApplyPatchMatchContext(ctx, updated, matchIndex, len(oldText), newText)
		if err != nil {
			return nil, err
		}
	}
	return updated, nil
}

func findUniqueApplyPatchMatch(
	ctx context.Context,
	haystack []byte,
	needle []byte,
) (first int, count int, err error) {
	if len(needle) == 0 {
		return 0, 0, nil
	}
	const checkInterval = 64 * 1024
	prefix := make([]int, len(needle))
	for index, matched := 1, 0; index < len(needle); {
		if index%checkInterval == 0 {
			if ctxErr := ctx.Err(); ctxErr != nil {
				return 0, 0, ctxErr
			}
		}
		if needle[index] == needle[matched] {
			matched++
			prefix[index] = matched
			index++
		} else if matched > 0 {
			matched = prefix[matched-1]
		} else {
			index++
		}
	}
	first = -1
	for index, matched := 0, 0; index < len(haystack); index++ {
		if index%checkInterval == 0 {
			if ctxErr := ctx.Err(); ctxErr != nil {
				return 0, 0, ctxErr
			}
		}
		for matched > 0 && haystack[index] != needle[matched] {
			matched = prefix[matched-1]
		}
		if haystack[index] == needle[matched] {
			matched++
		}
		if matched == len(needle) {
			position := index - len(needle) + 1
			if first < 0 {
				first = position
			}
			count++
			if count > 1 {
				return first, count, nil
			}
			matched = prefix[matched-1]
		}
	}
	return first, count, ctx.Err()
}

func replaceApplyPatchMatchContext(
	ctx context.Context,
	source []byte,
	index int,
	oldLength int,
	replacement []byte,
) ([]byte, error) {
	if index < 0 || oldLength < 0 || index+oldLength > len(source) {
		return nil, fmt.Errorf("invalid planned hunk replacement")
	}
	result := make([]byte, 0, len(source)-oldLength+len(replacement))
	var err error
	result, err = appendApplyPatchBytesContext(ctx, result, source[:index])
	if err != nil {
		return nil, err
	}
	result, err = appendApplyPatchBytesContext(ctx, result, replacement)
	if err != nil {
		return nil, err
	}
	return appendApplyPatchBytesContext(ctx, result, source[index+oldLength:])
}

func appendApplyPatchBytesContext(
	ctx context.Context,
	destination []byte,
	source []byte,
) ([]byte, error) {
	const chunkSize = 64 * 1024
	for start := 0; start < len(source); start += chunkSize {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		end := min(start+chunkSize, len(source))
		destination = append(destination, source[start:end]...)
	}
	return destination, ctx.Err()
}

func equalApplyPatchBytesContext(
	ctx context.Context,
	left []byte,
	right []byte,
) (bool, error) {
	if len(left) != len(right) {
		return false, ctx.Err()
	}
	const chunkSize = 64 * 1024
	for start := 0; start < len(left); start += chunkSize {
		if err := ctx.Err(); err != nil {
			return false, err
		}
		end := min(start+chunkSize, len(left))
		for index := start; index < end; index++ {
			if left[index] != right[index] {
				return false, nil
			}
		}
	}
	return true, ctx.Err()
}

func revalidateApplyPatchPlan(ctx context.Context, plan *applyPatchPlan) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	workspace, err := snapshotApplyPatchWorkspace(plan.workspace.lexical)
	if err != nil || workspace.canonical != plan.workspace.canonical ||
		!os.SameFile(workspace.info, plan.workspace.info) {
		return fmt.Errorf("apply-patch workspace changed during preflight")
	}
	for _, root := range plan.protectedRoots {
		canonical, resolveErr := resolveApplyPatchPathAgainstExistingAncestor(root.lexical)
		if resolveErr != nil || canonical != root.canonical {
			return fmt.Errorf("apply-patch protected-root state changed")
		}
	}
	for _, fence := range plan.fences {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := revalidateApplyPatchFence(fence); err != nil {
			return err
		}
	}
	for _, op := range plan.ops {
		if err := ctx.Err(); err != nil {
			return err
		}
		if op.source != nil {
			current, snapshotErr := snapshotApplyPatchSource(ctx, applyPatchCandidate{
				canonical: op.source.path, exists: true, info: op.source.info,
			}, op.sourceLabel, nil)
			if snapshotErr != nil {
				return snapshotErr
			}
			equalData, equalErr := equalApplyPatchBytesContext(ctx, current.data, op.source.data)
			if equalErr != nil {
				return equalErr
			}
			if !os.SameFile(current.info, op.source.info) ||
				current.mode != op.source.mode || current.linkCount != op.source.linkCount ||
				!equalData {
				return fmt.Errorf("patch source %q changed during preflight", op.sourceLabel)
			}
		}
	}
	return nil
}

func commitApplyPatchPlan(plan *applyPatchPlan) error {
	for _, op := range plan.ops {
		switch op.kind {
		case "add":
			if err := os.MkdirAll(filepath.Dir(op.targetPath), applyPatchParentMode()); err != nil {
				return fmt.Errorf("add file %q failed: %w", op.targetLabel, err)
			}
			if err := os.WriteFile(op.targetPath, op.after, applyPatchFileMode()); err != nil {
				return fmt.Errorf("add file %q failed: %w", op.targetLabel, err)
			}
		case "delete":
			if err := os.Remove(op.sourcePath); err != nil {
				return fmt.Errorf("delete file %q failed: %w", op.sourceLabel, err)
			}
		case "update":
			if err := os.WriteFile(op.targetPath, op.after, applyPatchFileMode()); err != nil {
				return fmt.Errorf("update file %q failed: %w", op.targetLabel, err)
			}
		case "move":
			if err := os.MkdirAll(filepath.Dir(op.targetPath), applyPatchParentMode()); err != nil {
				return fmt.Errorf("move file %q failed: %w", op.targetLabel, err)
			}
			if err := os.WriteFile(op.targetPath, op.after, applyPatchFileMode()); err != nil {
				return fmt.Errorf("update file %q failed: %w", op.targetLabel, err)
			}
			if err := os.Remove(op.sourcePath); err != nil {
				return fmt.Errorf("move file %q failed after writing target: %w", op.sourceLabel, err)
			}
		}
	}
	return nil
}

func captureApplyPatchPathFences(path string) ([]applyPatchPathFence, error) {
	cleaned := filepath.Clean(path)
	fences := make([]applyPatchPathFence, 0)
	for current := cleaned; ; current = filepath.Dir(current) {
		fence := applyPatchPathFence{path: current}
		info, err := os.Lstat(current)
		if err == nil {
			fence.exists = true
			fence.info = info
			fence.mode = info.Mode()
			if info.Mode()&os.ModeSymlink != 0 {
				fence.linkTarget, err = os.Readlink(current)
				if err != nil {
					return nil, err
				}
			}
		} else if !os.IsNotExist(err) {
			return nil, err
		}
		fences = append(fences, fence)
		parent := filepath.Dir(current)
		if parent == current {
			break
		}
	}
	return fences, nil
}

func revalidateApplyPatchFence(fence applyPatchPathFence) error {
	info, err := os.Lstat(fence.path)
	if !fence.exists {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("patch path state changed during preflight")
	}
	if err != nil || info.Mode() != fence.mode || !os.SameFile(info, fence.info) {
		return fmt.Errorf("patch path state changed during preflight")
	}
	if fence.mode&os.ModeSymlink != 0 {
		target, readErr := os.Readlink(fence.path)
		if readErr != nil || target != fence.linkTarget {
			return fmt.Errorf("patch path state changed during preflight")
		}
	}
	return nil
}

func dedupeApplyPatchFences(fences []applyPatchPathFence) []applyPatchPathFence {
	seen := make(map[string]struct{}, len(fences))
	result := make([]applyPatchPathFence, 0, len(fences))
	for _, fence := range fences {
		key := filepath.Clean(fence.path)
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		result = append(result, fence)
	}
	return result
}

func resolveApplyPatchPathAgainstExistingAncestor(path string) (string, error) {
	cleaned := filepath.Clean(path)
	for current := cleaned; ; current = filepath.Dir(current) {
		resolved, err := filepath.EvalSymlinks(current)
		if err == nil {
			suffix, relErr := filepath.Rel(current, cleaned)
			if relErr != nil {
				return "", relErr
			}
			if suffix == "." {
				return filepath.Clean(resolved), nil
			}
			return filepath.Clean(filepath.Join(resolved, suffix)), nil
		}
		if !os.IsNotExist(err) {
			return "", err
		}
		parent := filepath.Dir(current)
		if parent == current {
			return "", os.ErrNotExist
		}
	}
}

func applyPatchPathWithinExact(candidate, root string) bool {
	cleanedCandidate := filepath.Clean(candidate)
	cleanedRoot := filepath.Clean(root)
	if cleanedCandidate == cleanedRoot {
		return true
	}
	prefix := cleanedRoot
	if !strings.HasSuffix(prefix, string(os.PathSeparator)) {
		prefix += string(os.PathSeparator)
	}
	return strings.HasPrefix(cleanedCandidate, prefix)
}

func applyPatchPathWithinWorkspace(candidate string, workspace applyPatchWorkspace) bool {
	return applyPatchPathWithinExact(candidate, workspace.canonical) ||
		applyPatchExistingAncestorContains(workspace.canonical, candidate)
}

func applyPatchExistingAncestorContains(root, candidate string) bool {
	rootInfo, err := os.Stat(root)
	if err != nil || !rootInfo.IsDir() {
		return false
	}
	for current := filepath.Clean(candidate); ; current = filepath.Dir(current) {
		info, statErr := os.Stat(current)
		if statErr == nil {
			if os.SameFile(info, rootInfo) {
				return true
			}
		} else if !os.IsNotExist(statErr) {
			return false
		}
		parent := filepath.Dir(current)
		if parent == current {
			return false
		}
	}
}

func applyPatchPathWithinIdentity(candidate, root string) bool {
	rel, err := filepath.Rel(applyPatchPathKey(root), applyPatchPathKey(candidate))
	return err == nil && (rel == "." || filepath.IsLocal(rel))
}

func applyPatchPathHasGitAlias(path string) bool {
	for _, component := range strings.FieldsFunc(path, func(character rune) bool {
		return character == '/' || character == '\\'
	}) {
		base, _, _ := strings.Cut(component, ":")
		base = strings.TrimRight(base, " .")
		if strings.EqualFold(base, ".git") {
			return true
		}
	}
	return false
}
