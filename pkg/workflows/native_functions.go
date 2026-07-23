package workflows

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	osexec "os/exec"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

const (
	workflowStateDir     = "workflow_state"
	workflowArtifactsDir = "workflow_artifacts"
)

var safeStorageSegmentPattern = regexp.MustCompile(`[^A-Za-z0-9._-]+`)

var nativeFunctionNames = map[string]struct{}{
	"workflow.state":    {},
	"workflow.artifact": {},
	"git.inventory":     {},
	"git.filter":        {},
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
	if _, ok := nativeFunctionNames[name]; !ok {
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
	if mkdirErr := os.MkdirAll(stateDir, 0o755); mkdirErr != nil {
		return mkdirErr
	}
	env := nativeStateEnvelope{Key: key, Value: value, UpdatedAt: time.Now().UTC()}
	data, err := json.MarshalIndent(env, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(stateDir, "."+filepath.Base(statePath)+".tmp-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer func() {
		_ = os.Remove(tmpPath)
	}()
	if err := nativeEnsureInside(nativeWorkspace(exec), tmpPath); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("temporary state path must stay inside workflow workspace: %w", err)
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpPath, statePath)
}

func deleteNativeStateValue(exec ExecutionContext, namespace, key string) (bool, error) {
	statePath, err := nativeStatePath(exec, namespace, key)
	if err != nil {
		return false, err
	}
	if err := os.Remove(statePath); err != nil {
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
