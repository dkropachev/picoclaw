package workflows

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	osexec "os/exec"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/sipeed/picoclaw/pkg/fileutil"
)

const (
	workflowStateDir                   = "workflow_state"
	workflowArtifactsDir               = "workflow_artifacts"
	maxNativeGitDiffFiles              = 4096
	maxNativeGitDiffFileBytes          = 128 << 10
	maxNativeGitDiffAggregateBytes     = 512 << 10
	maxNativeGitRemoteBytes            = 2048
	maxNativeGitErrorOutputBytes       = 64 << 10
	nativeGitDiffContextLines          = 80
	nativeGitDiffInterHunkContextLines = 16
)

var safeStorageSegmentPattern = regexp.MustCompile(`[^A-Za-z0-9._-]+`)

var nativeFunctionNames = map[string]struct{}{
	"workflow.state":    {},
	"workflow.artifact": {},
	"git.inventory":     {},
	"git.diff":          {},
	"git.filter":        {},
}

// NativeFunctionNames returns a sorted copy of the workflow functions
// implemented by the runtime. Callers may mutate the returned slice.
func NativeFunctionNames() []string {
	names := make([]string, 0, len(nativeFunctionNames))
	for name := range nativeFunctionNames {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// IsNativeFunction reports whether name is implemented by the workflow
// runtime without an embedding FunctionRunner.
func IsNativeFunction(name string) bool {
	_, ok := nativeFunctionNames[strings.TrimSpace(name)]
	return ok
}

var nativeCodeExtensions = map[string]struct{}{
	".c":     {},
	".cc":    {},
	".cpp":   {},
	".cs":    {},
	".go":    {},
	".h":     {},
	".hpp":   {},
	".java":  {},
	".js":    {},
	".jsx":   {},
	".kt":    {},
	".mjs":   {},
	".py":    {},
	".rb":    {},
	".rs":    {},
	".sh":    {},
	".swift": {},
	".ts":    {},
	".tsx":   {},
}

var nativeConfigExtensions = map[string]struct{}{
	".json": {},
	".toml": {},
	".yaml": {},
	".yml":  {},
}

var nativeTestMarkers = []string{"test", "tests", "spec", "__tests__", "__mocks__"}

var nativeExcludePatterns = []string{
	".git/*",
	"node_modules/*",
	"vendor/*",
	"dist/*",
	"build/*",
	"target/*",
	"coverage/*",
	"*.lock",
	"package-lock.json",
	"pnpm-lock.yaml",
	"yarn.lock",
}

type nativeStateEnvelope struct {
	Key       string    `json:"key"`
	Value     any       `json:"value"`
	UpdatedAt time.Time `json:"updated_at"`
}

type nativeGitFile struct {
	Path      string `json:"path"`
	Mode      string `json:"mode"`
	BlobHash  string `json:"blobHash"`
	SizeBytes int64  `json:"sizeBytes"`
}

type nativeGitWorkspaceRef struct {
	ID        string
	RepoID    string
	RemoteURL string
	Ref       string
	Path      string
}

// RunNativeFunction executes PicoClaw built-ins available to workflow
// `function/...` steps. The boolean reports whether name is a native function.
func RunNativeFunction(
	ctx context.Context,
	name string,
	args map[string]any,
	exec ExecutionContext,
) (map[string]any, bool, error) {
	name = strings.TrimSpace(name)
	if !IsNativeFunction(name) {
		return nil, false, nil
	}
	switch name {
	case "workflow.state":
		out, err := nativeWorkflowState(ctx, args, exec)
		return out, true, err
	case "workflow.artifact":
		out, err := nativeWorkflowArtifact(ctx, args, exec)
		return out, true, err
	case "git.inventory":
		out, err := nativeGitInventory(ctx, args, exec)
		return out, true, err
	case "git.diff":
		out, err := nativeGitDiff(ctx, args, exec)
		return out, true, err
	case "git.filter":
		out, err := nativeGitFilter(ctx, args, exec)
		return out, true, err
	default:
		return nil, true, fmt.Errorf("unsupported native function %q", name)
	}
}

func nativeWorkflowState(ctx context.Context, args map[string]any, exec ExecutionContext) (map[string]any, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	action := strings.ToLower(strings.TrimSpace(nativeString(args, "action")))
	if action == "" {
		action = "get"
	}
	namespace := nativeNamespace(args, exec)
	key := strings.TrimSpace(nativeString(args, "key"))
	switch action {
	case "get":
		if key == "" {
			return nil, fmt.Errorf("key is required")
		}
		value, exists, err := readNativeStateValue(exec, namespace, key)
		if err != nil {
			return nil, err
		}
		return map[string]any{
			"namespace": namespace,
			"key":       key,
			"exists":    exists,
			"value":     value,
		}, nil
	case "set":
		if key == "" {
			return nil, fmt.Errorf("key is required")
		}
		value, ok := args["value"]
		if !ok {
			return nil, fmt.Errorf("value is required")
		}
		if err := writeNativeStateValue(exec, namespace, key, value); err != nil {
			return nil, err
		}
		return map[string]any{
			"namespace": namespace,
			"key":       key,
			"value":     value,
			"updated":   true,
		}, nil
	case "delete":
		if key == "" {
			return nil, fmt.Errorf("key is required")
		}
		deleted, err := deleteNativeStateValue(exec, namespace, key)
		if err != nil {
			return nil, err
		}
		return map[string]any{
			"namespace": namespace,
			"key":       key,
			"deleted":   deleted,
		}, nil
	case "list":
		includeValues := nativeBoolAny(args, "include_values", "includeValues")
		keys, values, err := listNativeStateValues(exec, namespace, includeValues)
		if err != nil {
			return nil, err
		}
		return map[string]any{
			"namespace": namespace,
			"keys":      keys,
			"values":    values,
		}, nil
	default:
		return nil, fmt.Errorf("unsupported workflow.state action %q", action)
	}
}

func nativeWorkflowArtifact(ctx context.Context, args map[string]any, exec ExecutionContext) (map[string]any, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	action := strings.ToLower(strings.TrimSpace(nativeString(args, "action")))
	if action == "" {
		if _, ok := args["content"]; ok {
			action = "write"
		} else if _, ok := args["value"]; ok {
			action = "write"
		} else {
			action = "list"
		}
	}
	namespace := nativeNamespace(args, exec)
	runID := strings.TrimSpace(nativeStringAny(args, "run_id", "runId"))
	if runID == "" {
		runID = exec.RunID
	}
	switch action {
	case "write":
		name := strings.TrimSpace(nativeString(args, "name"))
		content, err := nativeArtifactContent(args)
		if err != nil {
			return nil, err
		}
		if name == "" {
			name = defaultArtifactName(args)
		}
		return writeNativeArtifact(exec, namespace, runID, name, content)
	case "read":
		name := strings.TrimSpace(nativeString(args, "name"))
		if name == "" {
			return nil, fmt.Errorf("name is required")
		}
		return readNativeArtifact(exec, namespace, runID, name)
	case "list":
		return listNativeArtifacts(exec, namespace, runID)
	default:
		return nil, fmt.Errorf("unsupported workflow.artifact action %q", action)
	}
}

func nativeGitInventory(ctx context.Context, args map[string]any, exec ExecutionContext) (map[string]any, error) {
	repo, workspace, commit, inventory, inventoryHash, err := nativeGitInventoryData(ctx, args, exec)
	if err != nil {
		return nil, err
	}
	target := normalizeFileTarget(nativeStringDefault(args, "target", "all"))
	files, err := nativeGitInventoryOutputFiles(workspace, inventory, target, true)
	if err != nil {
		return nil, err
	}
	selected := make([]map[string]any, 0, len(files))
	excluded := 0
	for _, file := range files {
		if file["selected"] == true {
			selected = append(selected, file)
		} else {
			excluded++
		}
	}
	return map[string]any{
		"workingDirectory": repo,
		"workspace":        workspace.Map(),
		"commit":           commit,
		"target":           target,
		"inventoryHash":    inventoryHash,
		"files":            files,
		"selectedFiles":    selected,
		"counts": map[string]any{
			"totalFiles":         len(files),
			"totalSelectedFiles": len(selected),
			"filesExcluded":      excluded,
		},
	}, nil
}

type nativeGitDiffEntry struct {
	Status       string `json:"status"`
	Path         string `json:"path"`
	PreviousPath string `json:"previousPath,omitempty"`
}

type nativeGitSelectedDiff struct {
	Entry       nativeGitDiffEntry `json:"entry"`
	UnifiedDiff string             `json:"unifiedDiff"`
}

func nativeGitDiff(
	ctx context.Context,
	args map[string]any,
	exec ExecutionContext,
) (map[string]any, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	repo, workspace, err := nativeResolveGitWorkspace(exec, args)
	if err != nil {
		return nil, err
	}
	baseRevision := strings.TrimSpace(nativeStringAny(args, "base", "base_commit", "baseCommit"))
	if baseRevision == "" {
		return nil, fmt.Errorf("base commit is required")
	}
	headRevision := strings.TrimSpace(nativeStringAny(args, "head", "head_commit", "headCommit"))
	mode := strings.ToLower(strings.TrimSpace(nativeString(args, "mode")))
	if mode == "" {
		mode = "direct"
	}
	baseCommit, headCommit, comparisonBaseCommit, err := nativeGitDiffCommits(
		ctx,
		repo,
		workspace,
		baseRevision,
		headRevision,
		mode,
		args,
		exec,
	)
	if err != nil {
		return nil, err
	}
	raw, err := nativeGit(
		ctx,
		repo,
		"diff",
		"--name-status",
		"-z",
		"--find-renames",
		comparisonBaseCommit,
		headCommit,
		"--",
	)
	if err != nil {
		return nil, err
	}
	entries, err := parseNativeGitDiffEntries(raw)
	if err != nil {
		return nil, err
	}
	if len(entries) > maxNativeGitDiffFiles {
		return nil, fmt.Errorf(
			"git diff contains %d paths; maximum is %d",
			len(entries),
			maxNativeGitDiffFiles,
		)
	}

	headInventory, err := nativeCollectInventory(ctx, repo, headCommit)
	if err != nil {
		return nil, err
	}
	headFiles := make(map[string]nativeGitFile, len(headInventory))
	for _, file := range headInventory {
		headFiles[file.Path] = file
	}
	comparisonBaseInventory, err := nativeCollectInventory(ctx, repo, comparisonBaseCommit)
	if err != nil {
		return nil, err
	}
	comparisonBaseFiles := make(map[string]nativeGitFile, len(comparisonBaseInventory))
	for _, file := range comparisonBaseInventory {
		comparisonBaseFiles[file.Path] = file
	}
	target := normalizeFileTarget(nativeStringDefault(args, "target", "code"))
	files := make([]map[string]any, 0, len(entries))
	selected := make([]map[string]any, 0, len(entries))
	selectedDiffs := make([]nativeGitSelectedDiff, 0, len(entries))
	deletedPaths := make([]string, 0)
	totalDiffBytes := 0
	for _, entry := range entries {
		var inventoryFile nativeGitFile
		var exists bool
		if strings.HasPrefix(entry.Status, "D") {
			deletedPaths = append(deletedPaths, entry.Path)
			inventoryFile, exists = comparisonBaseFiles[entry.Path]
		} else {
			inventoryFile, exists = headFiles[entry.Path]
		}
		if !exists {
			return nil, fmt.Errorf(
				"changed path %q is absent from its trusted comparison inventory",
				entry.Path,
			)
		}
		projected, projectErr := nativeGitInventoryOutputFiles(
			workspace,
			[]nativeGitFile{inventoryFile},
			target,
			true,
		)
		if projectErr != nil {
			return nil, projectErr
		}
		file := projected[0]
		delete(file, "source")
		file["changeStatus"] = entry.Status
		if entry.PreviousPath != "" {
			file["previousPath"] = entry.PreviousPath
		}
		if file["selected"] == true {
			diffText, diffErr := nativeGitUnifiedDiff(
				ctx,
				repo,
				comparisonBaseCommit,
				headCommit,
				entry,
			)
			if diffErr != nil {
				return nil, diffErr
			}
			if len(diffText) > maxNativeGitDiffFileBytes {
				return nil, fmt.Errorf(
					"unified diff for %q is %d bytes; per-file maximum is %d",
					entry.Path,
					len(diffText),
					maxNativeGitDiffFileBytes,
				)
			}
			if totalDiffBytes > maxNativeGitDiffAggregateBytes-len(diffText) {
				return nil, fmt.Errorf(
					"selected unified diffs exceed aggregate maximum of %d bytes",
					maxNativeGitDiffAggregateBytes,
				)
			}
			totalDiffBytes += len(diffText)
			file["unifiedDiff"] = diffText
			file["diffBytes"] = len(diffText)
			selected = append(selected, file)
			selectedDiffs = append(selectedDiffs, nativeGitSelectedDiff{
				Entry:       entry,
				UnifiedDiff: diffText,
			})
		}
		files = append(files, file)
	}
	diffHash, err := nativeStableHash(map[string]any{
		"baseCommit":           baseCommit,
		"headCommit":           headCommit,
		"comparisonBaseCommit": comparisonBaseCommit,
		"entries":              entries,
		"selectedDiffs":        selectedDiffs,
	})
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"workingDirectory":     repo,
		"workspace":            workspace.Map(),
		"mode":                 mode,
		"baseCommit":           baseCommit,
		"headCommit":           headCommit,
		"comparisonBaseCommit": comparisonBaseCommit,
		"diffHash":             diffHash,
		"diffBytes":            totalDiffBytes,
		"files":                files,
		"selectedFiles":        selected,
		"deletedPaths":         deletedPaths,
		"counts": map[string]any{
			"totalChangedFiles":  len(entries),
			"totalSelectedFiles": len(selected),
			"deletedFiles":       len(deletedPaths),
		},
	}, nil
}

func nativeGitDiffCommits(
	ctx context.Context,
	repo string,
	workspace nativeGitWorkspaceRef,
	baseRevision string,
	headRevision string,
	mode string,
	args map[string]any,
	exec ExecutionContext,
) (string, string, string, error) {
	switch mode {
	case "direct":
		baseCommit, err := nativeResolveCommit(ctx, repo, baseRevision+"^{commit}")
		if err != nil {
			return "", "", "", fmt.Errorf("resolve base commit: %w", err)
		}
		if headRevision == "" {
			headRevision = "HEAD"
		}
		headCommit, err := nativeResolveCommit(ctx, repo, headRevision+"^{commit}")
		if err != nil {
			return "", "", "", fmt.Errorf("resolve head commit: %w", err)
		}
		return baseCommit, headCommit, baseCommit, nil
	case "pull_request":
		if !nativeValidGitObjectID(baseRevision) {
			return "", "", "", fmt.Errorf("pull-request base must be an exact Git object ID")
		}
		if !nativeValidGitObjectID(headRevision) {
			return "", "", "", fmt.Errorf("pull-request head must be an exact Git object ID")
		}
		if workspace.Ref != "" && strings.TrimSpace(workspace.Ref) != headRevision {
			return "", "", "", fmt.Errorf(
				"workspace ref %q does not match pull-request head %q",
				workspace.Ref,
				headRevision,
			)
		}
		headCommit, err := nativeResolveCommit(ctx, repo, headRevision+"^{commit}")
		if err != nil {
			return "", "", "", fmt.Errorf("resolve pull-request head commit: %w", err)
		}
		if headCommit != headRevision {
			return "", "", "", fmt.Errorf(
				"resolved pull-request head %q does not match requested commit %q",
				headCommit,
				headRevision,
			)
		}
		checkedOutHead, err := nativeResolveCommit(ctx, repo, "HEAD^{commit}")
		if err != nil {
			return "", "", "", fmt.Errorf("resolve checked-out workspace head: %w", err)
		}
		if checkedOutHead != headCommit {
			return "", "", "", fmt.Errorf(
				"checked-out workspace head %q does not match pull-request head %q",
				checkedOutHead,
				headCommit,
			)
		}
		baseRepository, err := nativePullRequestBaseRepository(
			exec,
			nativeStringAny(
				args,
				"base_repository",
				"baseRepository",
				"base_repository_url",
				"baseRepositoryURL",
			),
		)
		if err != nil {
			return "", "", "", err
		}
		if err := nativeFetchExactGitCommit(
			ctx,
			repo,
			baseRepository,
			baseRevision,
			exec,
		); err != nil {
			return "", "", "", err
		}
		baseCommit, err := nativeResolveCommit(ctx, repo, baseRevision+"^{commit}")
		if err != nil {
			return "", "", "", fmt.Errorf("resolve fetched pull-request base commit: %w", err)
		}
		if baseCommit != baseRevision {
			return "", "", "", fmt.Errorf(
				"fetched pull-request base %q does not match requested commit %q",
				baseCommit,
				baseRevision,
			)
		}
		mergeBase, err := nativeGit(ctx, repo, "merge-base", baseCommit, headCommit)
		if err != nil {
			return "", "", "", fmt.Errorf("resolve pull-request merge base: %w", err)
		}
		mergeBase = strings.TrimSpace(mergeBase)
		if !nativeValidGitObjectID(mergeBase) || strings.ContainsAny(mergeBase, "\r\n") {
			return "", "", "", fmt.Errorf("pull-request merge base is not one exact Git object ID")
		}
		mergeBase, err = nativeResolveCommit(ctx, repo, mergeBase+"^{commit}")
		if err != nil {
			return "", "", "", fmt.Errorf("resolve pull-request merge-base commit: %w", err)
		}
		return baseCommit, headCommit, mergeBase, nil
	default:
		return "", "", "", fmt.Errorf("unsupported git.diff mode %q", mode)
	}
}

func nativeFetchExactGitCommit(
	ctx context.Context,
	repo string,
	remote string,
	commit string,
	exec ExecutionContext,
) error {
	tempRoot := strings.TrimSpace(exec.WorkspaceDir)
	if tempRoot == "" {
		tempRoot = "."
	}
	validationRepo, err := os.MkdirTemp(tempRoot, ".picoclaw-git-fetch-")
	if err != nil {
		return fmt.Errorf("create pull-request base validation repository: %w", err)
	}
	defer func() {
		_ = os.RemoveAll(validationRepo)
	}()
	if _, err := nativeGit(ctx, validationRepo, "init", "--quiet", "--bare"); err != nil {
		return fmt.Errorf("initialize pull-request base validation repository: %w", err)
	}
	if _, err := nativeGit(
		ctx,
		validationRepo,
		"fetch",
		"--quiet",
		"--no-tags",
		"--no-write-fetch-head",
		"--no-recurse-submodules",
		"--no-auto-maintenance",
		"--",
		remote,
		commit,
	); err != nil {
		return fmt.Errorf("fetch exact pull-request base commit: %w", err)
	}
	validatedCommit, err := nativeResolveCommit(ctx, validationRepo, commit+"^{commit}")
	if err != nil {
		return fmt.Errorf("resolve validated pull-request base commit: %w", err)
	}
	if validatedCommit != commit {
		return fmt.Errorf(
			"validated pull-request base %q does not match requested commit %q",
			validatedCommit,
			commit,
		)
	}
	const validationRef = "refs/picoclaw/validated-base"
	if _, err := nativeGit(ctx, validationRepo, "update-ref", validationRef, validatedCommit); err != nil {
		return fmt.Errorf("pin validated pull-request base commit: %w", err)
	}
	if _, err := nativeGit(
		ctx,
		repo,
		"fetch",
		"--quiet",
		"--no-tags",
		"--no-write-fetch-head",
		"--no-recurse-submodules",
		"--no-auto-maintenance",
		"--",
		validationRepo,
		validationRef,
	); err != nil {
		return fmt.Errorf("import validated pull-request base commit: %w", err)
	}
	return nil
}

func nativeValidGitObjectID(value string) bool {
	if len(value) != 40 && len(value) != 64 {
		return false
	}
	for index := 0; index < len(value); index++ {
		char := value[index]
		if char >= '0' && char <= '9' || char >= 'a' && char <= 'f' {
			continue
		}
		return false
	}
	return true
}

func nativePullRequestBaseRepository(exec ExecutionContext, value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", fmt.Errorf("base repository is required in pull-request mode")
	}
	if len(value) > maxNativeGitRemoteBytes || !utf8.ValidString(value) {
		return "", fmt.Errorf("base repository is invalid")
	}
	parsed, err := url.Parse(value)
	if err != nil {
		return "", fmt.Errorf("base repository is invalid")
	}
	if parsed.Scheme == "" {
		repo, resolveErr := nativeResolveRepo(exec, value)
		if resolveErr != nil {
			return "", fmt.Errorf("resolve local base repository: %w", resolveErr)
		}
		return repo, nil
	}
	if parsed.Scheme != "https" ||
		parsed.Host == "" ||
		parsed.User != nil ||
		parsed.Opaque != "" ||
		parsed.RawQuery != "" ||
		parsed.Fragment != "" ||
		strings.Trim(parsed.EscapedPath(), "/") == "" {
		return "", fmt.Errorf("base repository must be an HTTPS URL or a local repository inside the workflow workspace")
	}
	return value, nil
}

func nativeGitUnifiedDiff(
	ctx context.Context,
	repo string,
	baseCommit string,
	headCommit string,
	entry nativeGitDiffEntry,
) (string, error) {
	args := []string{
		"--literal-pathspecs",
		"-c", "core.quotePath=true",
		"-c", "diff.noprefix=false",
		"-c", "diff.mnemonicPrefix=false",
		"diff",
		"--no-ext-diff",
		"--no-textconv",
		"--no-color",
		"--find-renames",
		"--full-index",
		"--diff-algorithm=histogram",
		fmt.Sprintf("--unified=%d", nativeGitDiffContextLines),
		fmt.Sprintf("--inter-hunk-context=%d", nativeGitDiffInterHunkContextLines),
		"--src-prefix=a/",
		"--dst-prefix=b/",
		baseCommit,
		headCommit,
		"--",
	}
	if entry.PreviousPath != "" {
		args = append(args, entry.PreviousPath)
	}
	args = append(args, entry.Path)
	diffText, exceeded, err := nativeGitBoundedOutput(
		ctx,
		repo,
		maxNativeGitDiffFileBytes,
		args...,
	)
	if err != nil {
		return "", fmt.Errorf("generate unified diff for %q: %w", entry.Path, err)
	}
	if exceeded {
		return "", fmt.Errorf(
			"unified diff for %q exceeds per-file maximum of %d bytes",
			entry.Path,
			maxNativeGitDiffFileBytes,
		)
	}
	if diffText == "" {
		return "", fmt.Errorf("unified diff for %q is empty", entry.Path)
	}
	if !utf8.ValidString(diffText) {
		return "", fmt.Errorf("unified diff for %q is not valid UTF-8", entry.Path)
	}
	return diffText, nil
}

func parseNativeGitDiffEntries(raw string) ([]nativeGitDiffEntry, error) {
	if raw == "" {
		return nil, nil
	}
	fields := strings.Split(raw, "\x00")
	if fields[len(fields)-1] == "" {
		fields = fields[:len(fields)-1]
	}
	entries := make([]nativeGitDiffEntry, 0, len(fields)/2)
	for index := 0; index < len(fields); {
		status := fields[index]
		index++
		if status == "" {
			return nil, fmt.Errorf("git diff contains an empty status")
		}
		statusKind := status[0]
		switch statusKind {
		case 'A', 'M', 'D', 'T', 'U', 'X', 'B':
			if index >= len(fields) {
				return nil, fmt.Errorf("git diff status %q is missing its path", status)
			}
			filePath, err := nativeCleanRepoFilePath(fields[index])
			if err != nil {
				return nil, err
			}
			index++
			entries = append(entries, nativeGitDiffEntry{
				Status: status,
				Path:   filePath,
			})
		case 'R', 'C':
			if index+1 >= len(fields) {
				return nil, fmt.Errorf("git diff status %q is missing rename paths", status)
			}
			previousPath, err := nativeCleanRepoFilePath(fields[index])
			if err != nil {
				return nil, err
			}
			filePath, err := nativeCleanRepoFilePath(fields[index+1])
			if err != nil {
				return nil, err
			}
			index += 2
			entries = append(entries, nativeGitDiffEntry{
				Status:       status,
				Path:         filePath,
				PreviousPath: previousPath,
			})
		default:
			return nil, fmt.Errorf("git diff contains unsupported status %q", status)
		}
	}
	return entries, nil
}

func nativeGitInventoryData(
	ctx context.Context,
	args map[string]any,
	exec ExecutionContext,
) (string, nativeGitWorkspaceRef, string, []nativeGitFile, string, error) {
	repo, workspace, err := nativeResolveGitWorkspace(exec, args)
	if err != nil {
		return "", nativeGitWorkspaceRef{}, "", nil, "", err
	}
	commit, err := nativeResolveCommit(ctx, repo, nativeString(args, "commit"))
	if err != nil {
		return "", nativeGitWorkspaceRef{}, "", nil, "", err
	}
	inventory, err := nativeCollectInventory(ctx, repo, commit)
	if err != nil {
		return "", nativeGitWorkspaceRef{}, "", nil, "", err
	}
	hash, err := nativeStableHash(inventory)
	if err != nil {
		return "", nativeGitWorkspaceRef{}, "", nil, "", err
	}
	return repo, workspace, commit, inventory, hash, nil
}

func nativeResolveGitWorkspace(exec ExecutionContext, args map[string]any) (string, nativeGitWorkspaceRef, error) {
	if workspaceValue, ok := args["workspace"]; ok && workspaceValue != nil {
		workspaceMap := nativeMapValue(workspaceValue)
		workspace := nativeGitWorkspaceRefFromMap(workspaceMap)
		if workspace.Path == "" && workspaceMap == nil {
			workspace.Path = strings.TrimSpace(nativeAnyString(workspaceValue))
		}
		if strings.TrimSpace(workspace.Path) == "" {
			return "", nativeGitWorkspaceRef{}, fmt.Errorf("workspace.path is required")
		}
		repo, err := nativeResolveRepo(exec, workspace.Path)
		if err != nil {
			return "", nativeGitWorkspaceRef{}, err
		}
		workspace.Path = repo
		return repo, workspace, nil
	}
	repo, err := nativeResolveRepo(exec, nativeStringAny(args, "working_directory", "workingDirectory"))
	if err != nil {
		return "", nativeGitWorkspaceRef{}, err
	}
	return repo, nativeGitWorkspaceRef{Path: repo}, nil
}

func nativeGitWorkspaceRefFromMap(value map[string]any) nativeGitWorkspaceRef {
	if value == nil {
		return nativeGitWorkspaceRef{}
	}
	return nativeGitWorkspaceRef{
		ID:        strings.TrimSpace(nativeAnyString(value["id"])),
		RepoID:    strings.TrimSpace(nativeAnyString(value["repo_id"])),
		RemoteURL: strings.TrimSpace(nativeAnyString(value["remote_url"])),
		Ref:       strings.TrimSpace(nativeAnyString(value["ref"])),
		Path:      strings.TrimSpace(nativeAnyString(value["path"])),
	}
}

func (w nativeGitWorkspaceRef) Map() map[string]any {
	out := make(map[string]any)
	if strings.TrimSpace(w.ID) != "" {
		out["id"] = strings.TrimSpace(w.ID)
	}
	if strings.TrimSpace(w.RepoID) != "" {
		out["repo_id"] = strings.TrimSpace(w.RepoID)
	}
	if strings.TrimSpace(w.RemoteURL) != "" {
		out["remote_url"] = strings.TrimSpace(w.RemoteURL)
	}
	if strings.TrimSpace(w.Ref) != "" {
		out["ref"] = strings.TrimSpace(w.Ref)
	}
	if strings.TrimSpace(w.Path) != "" {
		out["path"] = strings.TrimSpace(w.Path)
	}
	return out
}

func nativeGitFileSource(workspace nativeGitWorkspaceRef, filePath string) (map[string]any, error) {
	cleanPath, err := nativeCleanRepoFilePath(filePath)
	if err != nil {
		return nil, err
	}
	source := map[string]any{
		"type":     "workspace_file",
		"filePath": cleanPath,
	}
	if strings.TrimSpace(workspace.ID) != "" {
		source["workspaceId"] = strings.TrimSpace(workspace.ID)
	}
	workspacePath := strings.TrimSpace(workspace.Path)
	if workspacePath != "" {
		source["workspacePath"] = workspacePath
		source["path"] = filepath.Join(workspacePath, filepath.FromSlash(cleanPath))
	}
	return source, nil
}

func nativeCleanRepoFilePath(value string) (string, error) {
	clean := path.Clean(strings.TrimSpace(filepath.ToSlash(value)))
	if clean == "." || clean == "" {
		return "", fmt.Errorf("file path is required")
	}
	if path.IsAbs(clean) || clean == ".." || strings.HasPrefix(clean, "../") {
		return "", fmt.Errorf("file path %q must stay inside repository", value)
	}
	return clean, nil
}

func nativeResolveRepo(exec ExecutionContext, value string) (string, error) {
	workspace := strings.TrimSpace(exec.WorkspaceDir)
	if workspace == "" {
		workspace = "."
	}
	workspaceAbs, err := filepath.Abs(workspace)
	if err != nil {
		return "", err
	}
	value = strings.TrimSpace(value)
	if value == "" {
		value = "."
	}
	var candidate string
	if filepath.IsAbs(value) {
		candidate = value
	} else {
		candidate = filepath.Join(workspaceAbs, filepath.FromSlash(value))
	}
	candidateAbs, err := filepath.Abs(candidate)
	if err != nil {
		return "", err
	}
	if err := nativeEnsureInside(workspaceAbs, candidateAbs); err != nil {
		return "", fmt.Errorf("working_directory must stay inside workflow workspace: %w", err)
	}
	if _, err := os.Stat(filepath.Join(candidateAbs, ".git")); err != nil {
		if os.IsNotExist(err) {
			return "", fmt.Errorf("working_directory is not a git repo: %s", candidateAbs)
		}
		return "", err
	}
	return candidateAbs, nil
}

func nativeEnsureInside(root, target string) error {
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return err
	}
	targetAbs, err := filepath.Abs(target)
	if err != nil {
		return err
	}
	rootEval, err := evalWorkflowPathPrefix(rootAbs)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	if rootEval == "" {
		rootEval = rootAbs
	}
	targetEval, err := evalWorkflowPathPrefix(targetAbs)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	if targetEval == "" {
		targetEval = targetAbs
	}
	rel, err := filepath.Rel(rootEval, targetEval)
	if err != nil {
		return err
	}
	if rel == "." || rel == "" {
		return nil
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return fmt.Errorf("path escapes workspace")
	}
	return nil
}

func nativeResolveCommit(ctx context.Context, repo, commit string) (string, error) {
	commit = strings.TrimSpace(commit)
	if commit != "" {
		out, err := nativeGit(ctx, repo, "rev-parse", "--verify", commit)
		if err != nil {
			return "", err
		}
		return strings.TrimSpace(out), nil
	}
	for _, candidate := range []string{"HEAD", "main", "master"} {
		out, err := nativeGit(ctx, repo, "rev-parse", "--verify", candidate)
		if err != nil {
			continue
		}
		if strings.TrimSpace(out) != "" {
			return strings.TrimSpace(out), nil
		}
	}
	return "", fmt.Errorf("could not resolve default commit HEAD/main/master")
}

func nativeCollectInventory(ctx context.Context, repo, commit string) ([]nativeGitFile, error) {
	output, err := nativeGit(ctx, repo, "ls-tree", "-r", "-l", "--full-tree", commit)
	if err != nil {
		return nil, err
	}
	inventory := make([]nativeGitFile, 0)
	for _, line := range strings.Split(output, "\n") {
		if strings.TrimSpace(line) == "" || !strings.Contains(line, "\t") {
			continue
		}
		head, filePath, ok := strings.Cut(line, "\t")
		if !ok {
			continue
		}
		parts := strings.Fields(head)
		if len(parts) < 4 || parts[1] != "blob" {
			continue
		}
		size := int64(0)
		if parts[3] != "-" {
			parsed, err := strconv.ParseInt(parts[3], 10, 64)
			if err != nil {
				return nil, err
			}
			size = parsed
		}
		inventory = append(inventory, nativeGitFile{
			Path:      filePath,
			Mode:      parts[0],
			BlobHash:  parts[2],
			SizeBytes: size,
		})
	}
	sort.Slice(inventory, func(i, j int) bool {
		return inventory[i].Path < inventory[j].Path
	})
	return inventory, nil
}

func nativeGit(ctx context.Context, repo string, args ...string) (string, error) {
	cmd := osexec.CommandContext(ctx, "git", args...)
	cmd.Dir = repo
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", nativeGitError{err: err, output: strings.TrimSpace(string(out)), args: args}
	}
	return string(out), nil
}

type nativeBoundedBuffer struct {
	data     []byte
	limit    int
	exceeded bool
}

func (buffer *nativeBoundedBuffer) Write(value []byte) (int, error) {
	remaining := buffer.limit - len(buffer.data)
	if len(value) > remaining {
		buffer.exceeded = true
	}
	if remaining > 0 {
		if remaining > len(value) {
			remaining = len(value)
		}
		buffer.data = append(buffer.data, value[:remaining]...)
	}
	return len(value), nil
}

func nativeGitBoundedOutput(
	ctx context.Context,
	repo string,
	maxStdoutBytes int,
	args ...string,
) (string, bool, error) {
	stdout := nativeBoundedBuffer{limit: maxStdoutBytes}
	stderr := nativeBoundedBuffer{limit: maxNativeGitErrorOutputBytes}
	cmd := osexec.CommandContext(ctx, "git", args...)
	cmd.Dir = repo
	cmd.Env = append(os.Environ(), "LC_ALL=C", "GIT_PAGER=cat")
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		output := strings.TrimSpace(string(stderr.data))
		if output == "" {
			output = strings.TrimSpace(string(stdout.data))
		}
		return "", stdout.exceeded, nativeGitError{err: err, output: output, args: args}
	}
	return string(stdout.data), stdout.exceeded, nil
}

type nativeGitError struct {
	err    error
	output string
	args   []string
}

func (e nativeGitError) Error() string {
	if e.output != "" {
		return e.output
	}
	return fmt.Sprintf("git %s failed: %v", strings.Join(e.args, " "), e.err)
}

func nativeGitInventoryOutputFiles(
	workspace nativeGitWorkspaceRef,
	inventory []nativeGitFile,
	target string,
	includeModes bool,
) ([]map[string]any, error) {
	files := make([]map[string]any, 0, len(inventory))
	for _, entry := range inventory {
		category := nativeCategorizePath(entry.Path)
		selected := nativeTargetSelects(target, category)
		source, err := nativeGitFileSource(workspace, entry.Path)
		if err != nil {
			return nil, err
		}
		file := map[string]any{
			"path":      entry.Path,
			"fileHash":  entry.BlobHash,
			"category":  category,
			"selected":  selected,
			"sizeBytes": entry.SizeBytes,
			"source":    source,
		}
		if includeModes {
			file["mode"] = entry.Mode
		}
		files = append(files, file)
	}
	return files, nil
}

func nativeGitFilter(ctx context.Context, args map[string]any, exec ExecutionContext) (map[string]any, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	repo, workspace, err := nativeResolveGitWorkspace(exec, args)
	if err != nil {
		return nil, err
	}
	target := normalizeFileTarget(nativeStringDefault(args, "target", "code"))
	files, err := nativeMapSlice(args["files"])
	if err != nil {
		return nil, fmt.Errorf("files: %w", err)
	}
	filter := nativeMapValue(args["filter"])
	includeGlobs := nativeStringSliceAny(
		filter,
		"include_globs",
		"includeGlobs",
		"include",
		"includes",
	)
	excludeGlobs := nativeStringSliceAny(
		filter,
		"exclude_globs",
		"excludeGlobs",
		"exclude",
		"excludes",
	)
	selectedPaths := nativeStringSet(nativeStringSliceAny(
		filter,
		"selected_paths",
		"selectedPaths",
		"paths",
	))

	filtered := make([]map[string]any, 0, len(files))
	selected := make([]map[string]any, 0, len(files))
	for _, original := range files {
		file := cloneMap(original)
		filePath := strings.TrimSpace(nativeAnyString(file["path"]))
		category := strings.TrimSpace(nativeAnyString(file["category"]))
		if category == "" {
			category = nativeCategorizePath(filePath)
			file["category"] = category
		}
		cleanPath, err := nativeCleanRepoFilePath(filePath)
		if err != nil {
			return nil, err
		}
		filePath = cleanPath
		file["path"] = filePath
		baseSelected := nativeTargetSelects(target, category)
		matchesInclude := len(includeGlobs) == 0 && len(selectedPaths) == 0
		if !matchesInclude {
			matchesInclude = selectedPaths[nativeNormalizeRepoPath(filePath)] ||
				nativeAnyGlobMatches(includeGlobs, filePath)
		}
		matchesExclude := nativeAnyGlobMatches(excludeGlobs, filePath)
		isSelected := baseSelected && matchesInclude && !matchesExclude
		file["selected"] = isSelected
		source, err := nativeGitFileSource(workspace, filePath)
		if err != nil {
			return nil, err
		}
		file["source"] = source
		filtered = append(filtered, file)
		if isSelected {
			selected = append(selected, file)
		}
	}
	return map[string]any{
		"workingDirectory": repo,
		"workspace":        workspace.Map(),
		"commit":           nativeString(args, "commit"),
		"target":           target,
		"inventoryHash":    nativeStringAny(args, "inventory_hash", "inventoryHash"),
		"filter": map[string]any{
			"includeGlobs":  includeGlobs,
			"excludeGlobs":  excludeGlobs,
			"selectedPaths": nativeSortedSetValues(selectedPaths),
			"rationale":     nativeAnyString(filter["rationale"]),
		},
		"files":         filtered,
		"selectedFiles": selected,
		"counts": map[string]any{
			"totalFiles":         len(filtered),
			"totalSelectedFiles": len(selected),
			"filesExcluded":      len(filtered) - len(selected),
		},
	}, nil
}

func nativeCategorizePath(filePath string) string {
	low := strings.ToLower(filepath.ToSlash(filePath))
	for _, pattern := range nativeExcludePatterns {
		if ok, _ := path.Match(pattern, low); ok {
			return "excluded"
		}
	}
	parts := strings.FieldsFunc(low, func(r rune) bool {
		return r == '/' || r == '_' || r == '.' || r == '-'
	})
	for _, marker := range nativeTestMarkers {
		for _, part := range parts {
			if part == marker {
				return "tests"
			}
		}
		if strings.Contains(low, marker) {
			return "tests"
		}
	}
	ext := path.Ext(low)
	if _, ok := nativeCodeExtensions[ext]; ok {
		return "code"
	}
	if _, ok := nativeConfigExtensions[ext]; ok && !strings.HasSuffix(low, ".lock") {
		return "code"
	}
	return "excluded"
}

func normalizeFileTarget(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "code", "tests", "all":
		return strings.ToLower(strings.TrimSpace(value))
	default:
		return "code"
	}
}

func nativeTargetSelects(target, category string) bool {
	switch target {
	case "all":
		return category == "code" || category == "tests"
	case "code":
		return category == "code"
	case "tests":
		return category == "tests"
	default:
		return false
	}
}

func nativeAnyGlobMatches(patterns []string, filePath string) bool {
	for _, pattern := range patterns {
		if nativeGlobMatches(pattern, filePath) {
			return true
		}
	}
	return false
}

func nativeGlobMatches(pattern, filePath string) bool {
	pattern = nativeNormalizeRepoPath(pattern)
	filePath = nativeNormalizeRepoPath(filePath)
	if pattern == "" || filePath == "" {
		return false
	}
	if !strings.Contains(pattern, "/") {
		base := path.Base(filePath)
		if ok, _ := path.Match(pattern, base); ok {
			return true
		}
		return filePath == pattern || strings.Contains(filePath, "/"+pattern+"/")
	}
	if strings.HasSuffix(pattern, "/") {
		pattern += "**"
	}
	re, err := regexp.Compile(nativeGlobRegexp(pattern))
	if err != nil {
		return false
	}
	return re.MatchString(filePath)
}

func nativeGlobRegexp(pattern string) string {
	var b strings.Builder
	b.WriteString("^")
	for i := 0; i < len(pattern); i++ {
		ch := pattern[i]
		switch ch {
		case '*':
			if i+1 < len(pattern) && pattern[i+1] == '*' {
				i++
				if i+1 < len(pattern) && pattern[i+1] == '/' {
					i++
					b.WriteString("(?:.*/)?")
				} else {
					b.WriteString(".*")
				}
				continue
			}
			b.WriteString("[^/]*")
		case '?':
			b.WriteString("[^/]")
		default:
			b.WriteString(regexp.QuoteMeta(string(ch)))
		}
	}
	b.WriteString("$")
	return b.String()
}

func nativeNormalizeRepoPath(value string) string {
	value = strings.TrimSpace(filepath.ToSlash(value))
	value = strings.TrimPrefix(value, "./")
	return strings.ToLower(value)
}

func readNativeStateValue(exec ExecutionContext, namespace, key string) (any, bool, error) {
	statePath, err := nativeStatePath(exec, namespace, key)
	if err != nil {
		return nil, false, err
	}
	data, err := os.ReadFile(statePath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, false, nil
		}
		return nil, false, err
	}
	var env nativeStateEnvelope
	if err := json.Unmarshal(data, &env); err != nil {
		return nil, false, err
	}
	return env.Value, true, nil
}

func writeNativeStateValue(exec ExecutionContext, namespace, key string, value any) error {
	statePath, err := nativeStatePath(exec, namespace, key)
	if err != nil {
		return err
	}
	stateDir := filepath.Dir(statePath)
	if mkdirErr := fileutil.MkdirAllDurable(stateDir, 0o755); mkdirErr != nil {
		return mkdirErr
	}
	env := nativeStateEnvelope{Key: key, Value: value, UpdatedAt: time.Now().UTC()}
	data, err := json.MarshalIndent(env, "", "  ")
	if err != nil {
		return err
	}
	return fileutil.WriteFileAtomic(statePath, data, 0o600)
}

func deleteNativeStateValue(exec ExecutionContext, namespace, key string) (bool, error) {
	statePath, err := nativeStatePath(exec, namespace, key)
	if err != nil {
		return false, err
	}
	if err := fileutil.RemoveDurable(statePath); err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

func listNativeStateValues(
	exec ExecutionContext,
	namespace string,
	includeValues bool,
) ([]string, map[string]any, error) {
	root, err := nativeConfinedPath(exec, workflowStateDir, safeStorageSegment(namespace))
	if err != nil {
		return nil, nil, err
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil, nil
		}
		return nil, nil, err
	}
	keys := make([]string, 0, len(entries))
	values := map[string]any(nil)
	if includeValues {
		values = make(map[string]any)
	}
	for _, entry := range entries {
		if entry.IsDir() || path.Ext(entry.Name()) != ".json" {
			continue
		}
		if entry.Type()&fs.ModeSymlink != 0 {
			return nil, nil, fmt.Errorf("state file %q must not be a symlink", entry.Name())
		}
		data, err := os.ReadFile(filepath.Join(root, entry.Name()))
		if err != nil {
			return nil, nil, err
		}
		var env nativeStateEnvelope
		if err := json.Unmarshal(data, &env); err != nil {
			return nil, nil, err
		}
		keys = append(keys, env.Key)
		if includeValues {
			values[env.Key] = env.Value
		}
	}
	sort.Strings(keys)
	return keys, values, nil
}

func nativeStatePath(exec ExecutionContext, namespace, key string) (string, error) {
	key = strings.TrimSpace(key)
	if key == "" {
		return "", fmt.Errorf("state key is required")
	}
	return nativeConfinedPath(
		exec,
		workflowStateDir,
		safeStorageSegment(namespace),
		safeStorageSegment(key)+".json",
	)
}

func writeNativeArtifact(exec ExecutionContext, namespace, runID, name string, content string) (map[string]any, error) {
	artifactPath, relPath, err := nativeArtifactPath(exec, namespace, runID, name)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(artifactPath), 0o755); err != nil {
		return nil, err
	}
	data := []byte(content)
	if err := os.WriteFile(artifactPath, data, 0o600); err != nil {
		return nil, err
	}
	sum := sha256.Sum256(data)
	return map[string]any{
		"namespace":    namespace,
		"runId":        runID,
		"name":         filepath.ToSlash(name),
		"relativePath": relPath,
		"path":         artifactPath,
		"bytes":        len(data),
		"sha256":       hex.EncodeToString(sum[:]),
	}, nil
}

func readNativeArtifact(exec ExecutionContext, namespace, runID, name string) (map[string]any, error) {
	artifactPath, relPath, err := nativeArtifactPath(exec, namespace, runID, name)
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(artifactPath)
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(data)
	out := map[string]any{
		"namespace":    namespace,
		"runId":        runID,
		"name":         filepath.ToSlash(name),
		"relativePath": relPath,
		"path":         artifactPath,
		"bytes":        len(data),
		"sha256":       hex.EncodeToString(sum[:]),
		"content":      string(data),
	}
	var value any
	if err := json.Unmarshal(data, &value); err == nil {
		out["value"] = value
	}
	return out, nil
}

func listNativeArtifacts(exec ExecutionContext, namespace, runID string) (map[string]any, error) {
	parts := []string{workflowArtifactsDir, safeStorageSegment(namespace)}
	if strings.TrimSpace(runID) != "" {
		parts = append(parts, safeStorageSegment(runID))
	}
	root, err := nativeConfinedPath(exec, parts...)
	if err != nil {
		return nil, err
	}
	artifacts := make([]map[string]any, 0)
	err = filepath.WalkDir(root, func(item string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() {
			return nil
		}
		itemRel, relErr := filepath.Rel(nativeWorkspace(exec), item)
		if relErr != nil {
			return relErr
		}
		info, infoErr := d.Info()
		if infoErr != nil {
			return infoErr
		}
		artifacts = append(artifacts, map[string]any{
			"relativePath": filepath.ToSlash(itemRel),
			"path":         item,
			"bytes":        info.Size(),
		})
		return nil
	})
	if err != nil {
		if os.IsNotExist(err) {
			return map[string]any{"namespace": namespace, "runId": runID, "artifacts": artifacts}, nil
		}
		return nil, err
	}
	sort.Slice(artifacts, func(i, j int) bool {
		return fmt.Sprint(artifacts[i]["relativePath"]) < fmt.Sprint(artifacts[j]["relativePath"])
	})
	return map[string]any{"namespace": namespace, "runId": runID, "artifacts": artifacts}, nil
}

func nativeArtifactPath(exec ExecutionContext, namespace, runID, name string) (string, string, error) {
	if strings.TrimSpace(runID) == "" {
		runID = "manual"
	}
	relName, err := safeArtifactRel(name)
	if err != nil {
		return "", "", err
	}
	rel := filepath.Join(
		workflowArtifactsDir,
		safeStorageSegment(namespace),
		safeStorageSegment(runID),
		filepath.FromSlash(relName),
	)
	parts := make([]string, 0, 3+strings.Count(relName, "/")+1)
	parts = append(parts, workflowArtifactsDir, safeStorageSegment(namespace), safeStorageSegment(runID))
	parts = append(parts, strings.Split(relName, "/")...)
	target, err := nativeConfinedPath(exec, parts...)
	if err != nil {
		return "", "", err
	}
	return target, filepath.ToSlash(rel), nil
}

func nativeConfinedPath(exec ExecutionContext, parts ...string) (string, error) {
	workspace := nativeWorkspace(exec)
	target := filepath.Join(append([]string{workspace}, parts...)...)
	if len(parts) > 0 && parts[0] == workflowStateDir {
		var err error
		if len(parts) == 1 {
			target, err = resolveWorkflowInternalRoot(workspace, workflowStateDir)
		} else {
			target, err = resolveWorkflowInternalPath(
				workspace,
				workflowStateDir,
				parts[1:]...,
			)
		}
		if err != nil {
			return "", fmt.Errorf(
				"path must stay inside workflow workspace storage root: %w",
				err,
			)
		}
	}
	if err := nativeEnsureInside(workspace, target); err != nil {
		return "", fmt.Errorf("path must stay inside workflow workspace: %w", err)
	}
	if err := nativeEnsureInsideStorageRoot(workspace, target, parts...); err != nil {
		return "", fmt.Errorf("path must stay inside workflow workspace storage root: %w", err)
	}
	return target, nil
}

func nativeEnsureInsideStorageRoot(workspace, target string, parts ...string) error {
	if len(parts) < 2 {
		return nil
	}
	if parts[0] != workflowStateDir && parts[0] != workflowArtifactsDir {
		return nil
	}
	workspaceEval, err := evalWorkflowPathPrefix(workspace)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	if workspaceEval == "" {
		workspaceEval = filepath.Clean(workspace)
	}
	root := filepath.Join(workspace, parts[0], parts[1])
	rootEval, err := evalWorkflowPathPrefix(root)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	if rootEval == "" {
		rootEval = filepath.Clean(root)
	}
	intendedRoot := filepath.Join(workspaceEval, parts[0], parts[1])
	relRoot, err := filepath.Rel(filepath.Clean(intendedRoot), filepath.Clean(rootEval))
	if err != nil {
		return err
	}
	if relRoot != "." && relRoot != "" {
		return fmt.Errorf("storage root is a symlink")
	}
	if err := nativeEnsureInside(rootEval, target); err != nil {
		return fmt.Errorf("path escapes storage root: %w", err)
	}
	return nil
}

func safeArtifactRel(name string) (string, error) {
	clean := path.Clean(strings.TrimSpace(filepath.ToSlash(name)))
	if clean == "." || clean == "" {
		return "", fmt.Errorf("artifact name is required")
	}
	if path.IsAbs(clean) {
		return "", fmt.Errorf("artifact name must be relative")
	}
	for _, part := range strings.Split(clean, "/") {
		if part == "" || part == "." || part == ".." {
			return "", fmt.Errorf("artifact name contains unsafe path component")
		}
	}
	return clean, nil
}

func nativeArtifactContent(args map[string]any) (string, error) {
	if value, ok := args["content"]; ok {
		return fmt.Sprint(value), nil
	}
	value, ok := args["value"]
	if !ok {
		return "", fmt.Errorf("content or value is required")
	}
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return "", err
	}
	return string(data), nil
}

func defaultArtifactName(args map[string]any) string {
	format := strings.ToLower(strings.TrimSpace(nativeString(args, "format")))
	ext := ".txt"
	if format == "json" {
		ext = ".json"
	} else if format == "markdown" || format == "md" {
		ext = ".md"
	}
	return fmt.Sprintf("artifact-%d%s", time.Now().UTC().UnixNano(), ext)
}

func nativeNamespace(args map[string]any, exec ExecutionContext) string {
	namespace := strings.TrimSpace(nativeString(args, "namespace"))
	if namespace != "" {
		return namespace
	}
	if strings.TrimSpace(exec.WorkflowRef) != "" {
		return strings.TrimSpace(exec.WorkflowRef)
	}
	return "default"
}

func nativeWorkspace(exec ExecutionContext) string {
	workspace := strings.TrimSpace(exec.WorkspaceDir)
	if workspace == "" {
		workspace = "."
	}
	abs, err := filepath.Abs(workspace)
	if err != nil {
		return workspace
	}
	return abs
}

func safeStorageSegment(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		value = "default"
	}
	clean := safeStorageSegmentPattern.ReplaceAllString(value, "-")
	clean = strings.Trim(clean, ".-_")
	if clean == "" {
		clean = "value"
	}
	if len(clean) > 80 {
		clean = clean[:80]
	}
	sum := sha256.Sum256([]byte(value))
	return clean + "-" + hex.EncodeToString(sum[:])[:12]
}

func nativeString(args map[string]any, key string) string {
	if args == nil {
		return ""
	}
	value, ok := args[key]
	if !ok || value == nil {
		return ""
	}
	return fmt.Sprint(value)
}

func nativeAnyString(value any) string {
	if value == nil {
		return ""
	}
	return fmt.Sprint(value)
}

func nativeStringAny(args map[string]any, keys ...string) string {
	for _, key := range keys {
		if value := strings.TrimSpace(nativeString(args, key)); value != "" {
			return value
		}
	}
	return ""
}

func nativeStringDefault(args map[string]any, key, fallback string) string {
	value := strings.TrimSpace(nativeString(args, key))
	if value == "" {
		return fallback
	}
	return value
}

func nativeMapValue(value any) map[string]any {
	switch v := value.(type) {
	case nil:
		return nil
	case map[string]any:
		return v
	case map[string]string:
		out := make(map[string]any, len(v))
		for key, item := range v {
			out[key] = item
		}
		return out
	case string:
		var out map[string]any
		if err := json.Unmarshal([]byte(v), &out); err == nil {
			return out
		}
	}
	return nil
}

func nativeMapSlice(value any) ([]map[string]any, error) {
	switch v := value.(type) {
	case nil:
		return nil, fmt.Errorf("required")
	case []map[string]any:
		return v, nil
	case []any:
		out := make([]map[string]any, 0, len(v))
		for i, item := range v {
			obj, ok := item.(map[string]any)
			if !ok {
				return nil, fmt.Errorf("item %d must be an object", i)
			}
			out = append(out, obj)
		}
		return out, nil
	case string:
		var out []map[string]any
		if err := json.Unmarshal([]byte(v), &out); err != nil {
			return nil, err
		}
		return out, nil
	default:
		return nil, fmt.Errorf("must be an array of objects")
	}
}

func nativeStringSliceAny(args map[string]any, keys ...string) []string {
	for _, key := range keys {
		values := nativeStringSlice(args[key])
		if len(values) > 0 {
			return values
		}
	}
	return nil
}

func nativeStringSlice(value any) []string {
	switch v := value.(type) {
	case nil:
		return nil
	case []string:
		return nativeCleanStringSlice(v)
	case []any:
		out := make([]string, 0, len(v))
		for _, item := range v {
			if text := strings.TrimSpace(nativeAnyString(item)); text != "" {
				out = append(out, text)
			}
		}
		return nativeCleanStringSlice(out)
	case string:
		text := strings.TrimSpace(v)
		if text == "" {
			return nil
		}
		var decoded []string
		if err := json.Unmarshal([]byte(text), &decoded); err == nil {
			return nativeCleanStringSlice(decoded)
		}
		return nativeCleanStringSlice(strings.Split(text, ","))
	default:
		return nil
	}
}

func nativeCleanStringSlice(values []string) []string {
	out := make([]string, 0, len(values))
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			out = append(out, value)
		}
	}
	return out
}

func nativeStringSet(values []string) map[string]bool {
	out := make(map[string]bool, len(values))
	for _, value := range values {
		if value = nativeNormalizeRepoPath(value); value != "" {
			out[value] = true
		}
	}
	return out
}

func nativeSortedSetValues(values map[string]bool) []string {
	out := make([]string, 0, len(values))
	for value := range values {
		out = append(out, value)
	}
	sort.Strings(out)
	return out
}

func nativeBool(args map[string]any, key string) bool {
	if args == nil {
		return false
	}
	switch value := args[key].(type) {
	case bool:
		return value
	case string:
		switch strings.ToLower(strings.TrimSpace(value)) {
		case "1", "true", "yes", "on":
			return true
		default:
			return false
		}
	default:
		return false
	}
}

func nativeBoolAny(args map[string]any, keys ...string) bool {
	for _, key := range keys {
		if nativeBool(args, key) {
			return true
		}
	}
	return false
}

func nativeInt(args map[string]any, key string, fallback int) int {
	if args == nil {
		return fallback
	}
	switch value := args[key].(type) {
	case int:
		return value
	case int64:
		return int(value)
	case float64:
		return int(value)
	case string:
		parsed, err := strconv.Atoi(strings.TrimSpace(value))
		if err == nil {
			return parsed
		}
	}
	return fallback
}

func nativeStableHash(value any) (string, error) {
	data, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}
