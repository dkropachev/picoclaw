package workflows

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
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
	repo, commit, inventory, inventoryHash, err := nativeGitInventoryData(ctx, args, exec)
	if err != nil {
		return nil, err
	}
	target := normalizeFileTarget(nativeStringDefault(args, "target", "all"))
	includeContent := nativeBoolAny(args, "include_content", "includeContent")
	maxContentBytes := nativeInt(args, "max_content_bytes", 0)
	if maxContentBytes <= 0 {
		maxContentBytes = nativeInt(args, "maxContentBytes", 0)
	}
	if maxContentBytes <= 0 {
		maxContentBytes = 64 * 1024
	}
	files, err := nativeGitInventoryOutputFiles(ctx, repo, inventory, target, true, includeContent, maxContentBytes)
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
) (string, string, []nativeGitFile, string, error) {
	repo, err := nativeResolveRepo(exec, nativeStringAny(args, "working_directory", "workingDirectory"))
	if err != nil {
		return "", "", nil, "", err
	}
	commit, err := nativeResolveCommit(ctx, repo, nativeString(args, "commit"))
	if err != nil {
		return "", "", nil, "", err
	}
	inventory, err := nativeCollectInventory(ctx, repo, commit)
	if err != nil {
		return "", "", nil, "", err
	}
	hash, err := nativeStableHash(inventory)
	if err != nil {
		return "", "", nil, "", err
	}
	return repo, commit, inventory, hash, nil
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
	ctx context.Context,
	repo string,
	inventory []nativeGitFile,
	target string,
	includeModes bool,
	includeContent bool,
	maxContentBytes int,
) ([]map[string]any, error) {
	files := make([]map[string]any, 0, len(inventory))
	for _, entry := range inventory {
		category := nativeCategorizePath(entry.Path)
		selected := nativeTargetSelects(target, category)
		file := map[string]any{
			"path":      entry.Path,
			"fileHash":  entry.BlobHash,
			"category":  category,
			"selected":  selected,
			"sizeBytes": entry.SizeBytes,
		}
		if includeModes {
			file["mode"] = entry.Mode
		}
		if includeContent && selected {
			content, truncated, err := nativeGitBlobContent(ctx, repo, entry.BlobHash, maxContentBytes)
			if err != nil {
				return nil, err
			}
			file["content"] = content
			file["contentBytes"] = len([]byte(content))
			file["contentEncoding"] = "utf-8"
			file["contentTruncated"] = truncated
		}
		files = append(files, file)
	}
	return files, nil
}

func nativeGitBlobContent(ctx context.Context, repo, blobHash string, maxBytes int) (string, bool, error) {
	args := []string{"cat-file", "-p", blobHash}
	cmd := osexec.CommandContext(ctx, "git", args...)
	cmd.Dir = repo
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return "", false, err
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		return "", false, err
	}

	readLimit := int64(maxBytes)
	if readLimit > 0 {
		readLimit++
	}
	var data []byte
	if readLimit > 0 {
		data, err = io.ReadAll(io.LimitReader(stdout, readLimit))
	} else {
		data, err = io.ReadAll(stdout)
	}
	truncated := maxBytes > 0 && len(data) > maxBytes
	if truncated && cmd.Process != nil {
		_ = cmd.Process.Kill()
	}
	waitErr := cmd.Wait()
	if err != nil {
		return "", false, err
	}
	if waitErr != nil && !truncated {
		return "", false, nativeGitError{err: waitErr, output: strings.TrimSpace(stderr.String()), args: args}
	}
	if truncated {
		data = data[:maxBytes]
	}
	return strings.ToValidUTF8(string(data), ""), truncated, nil
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
