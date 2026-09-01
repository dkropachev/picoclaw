package agent

import (
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"

	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/tools"
)

const (
	agentLauncherAuthDatabaseName = "launcher-auth.db"
	agentAuthDatabaseName         = "auth.db"
	agentModelCatalogDatabaseName = "model-catalogs.db"
	agentToolAdaptationDatabase   = "tool-adaptation.db"
	agentLocalCICacheDatabaseName = "cache.db"
	agentRepositoryReviewStateDir = "repository_reviews"
	agentRepositoryEvalStateDir   = "repository_evaluations"
	agentGitInventoryDatabase     = "inventory.db"
	agentCheckpointDatabase       = "checkpoints.db"
	agentCheckpointMaximumSources = 10_000
	agentCheckpointArchiveEntries = agentCheckpointMaximumSources + 16
	agentCheckpointArchiveDepth   = 4
	agentCheckpointReadBatch      = 128
)

const (
	agentWecomReqIDDatabaseName  = "reqid-store.db"
	agentWeixinStateDatabaseName = "state.db"
)

func agentWorkflowRuntimeFileMutationProtectedRoots(workspace string) ([]string, error) {
	workspace, err := filepath.Abs(filepath.Clean(workspace))
	if err != nil {
		return nil, fmt.Errorf("resolve workflow workspace: %w", err)
	}
	database := filepath.Join(workspace, "state", "workflows.db")
	return []string{
		filepath.Join(workspace, "state"),
		database,
		database + "-wal",
		database + "-shm",
		filepath.Join(workspace, "legacy-json"),
		filepath.Join(workspace, "workflow_runs"),
		filepath.Join(workspace, "workflow_validations"),
		filepath.Join(workspace, "workflow_dev"),
		filepath.Join(workspace, "workflow_state"),
		filepath.Join(workspace, "workflow_state", "mutation.lock"),
		filepath.Join(workspace, "workflow_state", "publish-transaction.json"),
		filepath.Join(workspace, "workflow_state", "template-transaction.json"),
	}, nil
}

// agentRuntimeFileMutationProtectedRoots freezes model-facing filesystem
// exclusions for mutable runtime state. Keep this component-oriented so later
// SQLite stores can extend the same policy without making active configuration
// or repository source trees read-only.
func agentRuntimeFileMutationProtectedRoots(
	configPath string,
	activeConfigs ...*config.Config,
) ([]string, error) {
	home, err := filepath.Abs(filepath.Clean(config.GetHome()))
	if err != nil {
		return nil, fmt.Errorf("resolve PicoClaw home: %w", err)
	}

	if configPath == "" {
		configPath = os.Getenv(config.EnvConfig)
	}
	if configPath == "" {
		configPath = filepath.Join(home, "config.json")
	}
	configPath, err = filepath.Abs(filepath.Clean(configPath))
	if err != nil {
		return nil, fmt.Errorf("resolve active config path: %w", err)
	}

	protected := make([]string, 0, 64)
	protected = append(protected,
		filepath.Join(home, "auth.json"),
		filepath.Join(home, "model_catalogs.json"),
		filepath.Join(home, "tool_adaptation_state.json"),
	)
	for _, databaseName := range []string{
		agentLauncherAuthDatabaseName,
		agentAuthDatabaseName,
		agentModelCatalogDatabaseName,
		agentToolAdaptationDatabase,
	} {
		database := filepath.Join(home, databaseName)
		protected = append(protected, database, database+"-wal", database+"-shm")
	}
	authLockDirectory := filepath.Join(home, agentAuthDatabaseName+".locks")
	protected = append(
		protected,
		authLockDirectory,
		filepath.Join(authLockDirectory, "store.lock"),
	)

	activeArchiveRoot := filepath.Join(filepath.Dir(configPath), "legacy-json")
	homeArchiveRoot := filepath.Join(home, "legacy-json")
	protected = append(protected,
		// Protect the archive namespace, rather than only the version leaf, so
		// a model cannot pre-create legacy-json as a file and poison migration.
		activeArchiveRoot,
		// The exact retained credential source also catches hardlink aliases
		// whose lexical path lies outside the archive namespace.
		filepath.Join(
			activeArchiveRoot,
			"launcher-auth-v1",
			"launcher-config.json",
		),
	)
	if homeArchiveRoot != activeArchiveRoot {
		protected = append(protected, homeArchiveRoot)
	}
	protected = append(protected,
		filepath.Join(homeArchiveRoot, "auth-v1", "auth.json"),
		filepath.Join(homeArchiveRoot, "model-catalogs-v1", "model_catalogs.json"),
		filepath.Join(
			homeArchiveRoot,
			"tool-adaptation-v1",
			"tool_adaptation_state.json",
		),
	)

	wecomDatabase := filepath.Join(home, "channels", "wecom", agentWecomReqIDDatabaseName)
	wecomArchiveRoot := filepath.Join(home, "legacy-json", "wecom-reqid-v1")
	protected = append(protected, agentSQLiteFileMutationProtectedRoots(wecomDatabase)...)
	protected = append(protected,
		filepath.Join(home, "wecom", "reqid-store.json"),
		wecomArchiveRoot,
		filepath.Join(wecomArchiveRoot, "wecom", "reqid-store.json"),
	)

	weixinRoot := filepath.Join(home, "channels", "weixin")
	weixinDatabase := filepath.Join(weixinRoot, agentWeixinStateDatabaseName)
	weixinArchiveRoot := filepath.Join(weixinRoot, "legacy-json", "weixin-state-v1")
	protected = append(protected, agentSQLiteFileMutationProtectedRoots(weixinDatabase)...)
	protected = append(protected,
		filepath.Join(weixinRoot, "sync"),
		filepath.Join(weixinRoot, "context-tokens"),
		weixinArchiveRoot,
	)
	weixinFiles, err := agentWeixinRetainedStateFiles(weixinRoot, weixinArchiveRoot)
	if err != nil {
		return nil, err
	}
	protected = append(protected, weixinFiles...)

	var activeConfig *config.Config
	if len(activeConfigs) > 0 {
		activeConfig = activeConfigs[0]
	}
	gitRoots, err := agentGitWorkspaceFileMutationProtectedRoots(activeConfig)
	if err != nil {
		return nil, err
	}
	return append(protected, gitRoots...), nil
}

func agentGitWorkspaceFileMutationProtectedRoots(activeConfig *config.Config) ([]string, error) {
	gitWorkspaceRoot := activeConfig.GitWorkspaceRootPath()
	gitWorkspaceRoot, err := filepath.Abs(filepath.Clean(gitWorkspaceRoot))
	if err != nil {
		return nil, fmt.Errorf("resolve Git workspace runtime root: %w", err)
	}
	gitInventory := filepath.Join(gitWorkspaceRoot, agentGitInventoryDatabase)
	checkpointRoot := filepath.Join(
		gitWorkspaceRoot,
		".pr-workspace-implementation",
		"active",
	)
	checkpointDatabase := filepath.Join(checkpointRoot, agentCheckpointDatabase)
	checkpointArchiveRoot := filepath.Join(checkpointRoot, "legacy-json")
	protected := make([]string, 0, 18)
	for _, database := range []string{gitInventory, checkpointDatabase} {
		protected = append(protected, database, database+"-wal", database+"-shm")
	}
	protected = append(protected,
		filepath.Join(gitWorkspaceRoot, "inventory.json"),
		filepath.Join(gitWorkspaceRoot, "inventory.lock"),
		filepath.Join(gitWorkspaceRoot, ".locks"),
		filepath.Join(gitWorkspaceRoot, "legacy-json"),
		filepath.Join(
			gitWorkspaceRoot,
			"legacy-json",
			"git-workspaces-v1",
			"inventory.json",
		),
		checkpointRoot,
		checkpointArchiveRoot,
	)
	checkpointFiles, err := agentCheckpointRetainedStateFiles(
		checkpointRoot,
		checkpointArchiveRoot,
	)
	if err != nil {
		return nil, err
	}
	protected = append(protected, checkpointFiles...)
	return protected, nil
}

func agentCheckpointRetainedStateFiles(checkpointRoot, archiveRoot string) ([]string, error) {
	return agentCheckpointRetainedStateFilesBounded(
		checkpointRoot,
		archiveRoot,
		agentCheckpointMaximumSources+4,
		agentCheckpointMaximumSources,
		agentCheckpointArchiveEntries,
		agentCheckpointArchiveDepth,
	)
}

func agentCheckpointRetainedStateFilesBounded(
	checkpointRoot,
	archiveRoot string,
	maximumRootEntries,
	maximumSources,
	maximumArchiveEntries,
	maximumArchiveDepth int,
) ([]string, error) {
	if maximumRootEntries < 1 || maximumSources < 1 || maximumArchiveEntries < 1 ||
		maximumArchiveDepth < 1 {
		return nil, errors.New("enumerate PR workspace checkpoint state: bounds are invalid")
	}
	files := make(map[string]struct{})
	rootInfo, err := os.Lstat(checkpointRoot)
	switch {
	case os.IsNotExist(err):
	case err != nil:
		return nil, fmt.Errorf("enumerate PR workspace checkpoint state: %w", err)
	case !privateAgentCheckpointDirectory(checkpointRoot, rootInfo) || rootInfo.Mode()&os.ModeSymlink != 0:
		return nil, errors.New("enumerate PR workspace checkpoint state: root is unsafe")
	default:
		root, openErr := os.Open(checkpointRoot)
		if openErr != nil {
			return nil, fmt.Errorf("enumerate PR workspace checkpoint state: %w", openErr)
		}
		openedInfo, statErr := root.Stat()
		if statErr != nil || !os.SameFile(rootInfo, openedInfo) {
			_ = root.Close()
			return nil, errors.New("enumerate PR workspace checkpoint state: root changed")
		}
		entries, sources := 0, 0
		readErr := forEachCheckpointDirectoryEntry(root, func(entry os.DirEntry) error {
			entries++
			if entries > maximumRootEntries {
				return errors.New("entry limit exceeded")
			}
			if !strings.HasSuffix(entry.Name(), ".json") {
				return nil
			}
			sources++
			if sources > maximumSources {
				return errors.New("source limit exceeded")
			}
			path := filepath.Join(checkpointRoot, entry.Name())
			info, statErr := os.Lstat(path)
			if statErr != nil {
				return statErr
			}
			if !privateAgentCheckpointFile(path, info) || info.Mode()&os.ModeSymlink != 0 {
				return errors.New("source is unsafe")
			}
			files[path] = struct{}{}
			files[filepath.Join(
				archiveRoot,
				"pr-workspace-checkpoints-v1",
				entry.Name(),
			)] = struct{}{}
			return nil
		})
		closeErr := root.Close()
		if readErr != nil {
			return nil, fmt.Errorf("enumerate PR workspace checkpoint state: %w", readErr)
		}
		if closeErr != nil {
			return nil, fmt.Errorf("enumerate PR workspace checkpoint state: %w", closeErr)
		}
	}

	archiveEntries := 0
	err = enumerateCheckpointArchive(
		archiveRoot,
		archiveRoot,
		0,
		maximumArchiveDepth,
		maximumArchiveEntries,
		&archiveEntries,
		files,
	)
	if err != nil && !os.IsNotExist(err) {
		return nil, fmt.Errorf("enumerate PR workspace checkpoint archives: %w", err)
	}
	result := make([]string, 0, len(files))
	for path := range files {
		result = append(result, path)
	}
	slices.Sort(result)
	return result, nil
}

func enumerateCheckpointArchive(
	archiveRoot,
	directory string,
	depth,
	maximumDepth,
	maximumEntries int,
	entries *int,
	files map[string]struct{},
) error {
	info, err := os.Lstat(directory)
	if err != nil {
		return err
	}
	if !privateAgentCheckpointDirectory(directory, info) || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("checkpoint archive contains an unsafe directory")
	}
	opened, err := os.Open(directory)
	if err != nil {
		return err
	}
	openedInfo, statErr := opened.Stat()
	if statErr != nil || !os.SameFile(info, openedInfo) {
		_ = opened.Close()
		return errors.New("checkpoint archive directory changed during enumeration")
	}
	err = forEachCheckpointDirectoryEntry(opened, func(entry os.DirEntry) error {
		*entries++
		if *entries > maximumEntries {
			return errors.New("checkpoint archive exceeds its safe entry limit")
		}
		path := filepath.Join(directory, entry.Name())
		entryInfo, entryErr := os.Lstat(path)
		if entryErr != nil {
			return entryErr
		}
		if entryInfo.Mode()&os.ModeSymlink != 0 {
			return errors.New("checkpoint archive contains an unsafe symlink")
		}
		if entryInfo.IsDir() {
			if depth >= maximumDepth {
				return errors.New("checkpoint archive exceeds its safe depth limit")
			}
			return enumerateCheckpointArchive(
				archiveRoot, path, depth+1, maximumDepth, maximumEntries, entries, files,
			)
		}
		if !privateAgentCheckpointFile(path, entryInfo) {
			return errors.New("checkpoint archive contains an unsafe file")
		}
		files[path] = struct{}{}
		return nil
	})
	closeErr := opened.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	return nil
}

func forEachCheckpointDirectoryEntry(
	directory *os.File,
	visit func(os.DirEntry) error,
) error {
	for {
		entries, err := directory.ReadDir(agentCheckpointReadBatch)
		for _, entry := range entries {
			if visitErr := visit(entry); visitErr != nil {
				return visitErr
			}
		}
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
	}
}

func agentWorkspaceFileMutationProtectedRoots(workspace string) ([]string, error) {
	workspace, err := filepath.Abs(filepath.Clean(workspace))
	if err != nil {
		return nil, fmt.Errorf("resolve agent workspace: %w", err)
	}
	stateRoot := filepath.Join(workspace, "state")
	database := filepath.Join(stateRoot, "runtime.db")
	archiveRoot := filepath.Join(stateRoot, "legacy-json", "runtime-state-v1")
	roots := make([]string, 0, 11)
	roots = append(roots, agentSQLiteFileMutationProtectedRoots(database)...)
	roots = append(roots,
		// The root-level legacy source lies outside stateRoot and must remain
		// protected until its first authoritative SQLite open archives it.
		filepath.Join(workspace, "state.json"),
		filepath.Join(stateRoot, "state.json"),
		archiveRoot,
		filepath.Join(archiveRoot, "state.json"),
		filepath.Join(archiveRoot, "state", "state.json"),
	)
	return roots, nil
}

func agentSQLiteFileMutationProtectedRoots(database string) []string {
	lockDirectory := database + ".locks"
	return []string{
		database,
		database + "-wal",
		database + "-shm",
		lockDirectory,
		filepath.Join(lockDirectory, "store.lock"),
	}
}

func mustAgentWorkspaceFileMutationProtectedRoots(workspace string) []string {
	roots, err := agentWorkspaceFileMutationProtectedRoots(workspace)
	if err != nil {
		panic(fmt.Sprintf("build workspace file-mutation policy: %v", err))
	}
	return roots
}

func agentWeixinRetainedStateFiles(weixinRoot, archiveRoot string) ([]string, error) {
	files := make(map[string]struct{})
	for _, directory := range []string{"sync", "context-tokens"} {
		sourceRoot := filepath.Join(weixinRoot, directory)
		info, err := os.Lstat(sourceRoot)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("enumerate Weixin state roots: %w", err)
		}
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return nil, errors.New("enumerate Weixin state roots: legacy directory is unsafe")
		}
		entries, err := os.ReadDir(sourceRoot)
		if err != nil {
			return nil, fmt.Errorf("enumerate Weixin state roots: %w", err)
		}
		for _, entry := range entries {
			if strings.ToLower(filepath.Ext(entry.Name())) != ".json" {
				continue
			}
			files[filepath.Join(sourceRoot, entry.Name())] = struct{}{}
			files[filepath.Join(archiveRoot, directory, entry.Name())] = struct{}{}
		}
	}
	err := filepath.WalkDir(archiveRoot, func(path string, entry os.DirEntry, walkErr error) error {
		if os.IsNotExist(walkErr) {
			return nil
		}
		if walkErr != nil {
			return walkErr
		}
		if path != archiveRoot && !entry.IsDir() {
			files[path] = struct{}{}
		}
		return nil
	})
	if err != nil && !os.IsNotExist(err) {
		return nil, fmt.Errorf("enumerate Weixin retained state: %w", err)
	}
	result := make([]string, 0, len(files))
	for path := range files {
		result = append(result, path)
	}
	slices.Sort(result)
	return result, nil
}

func agentEvolutionFileMutationProtectedRoots(workspace, stateDir string) ([]string, error) {
	root := strings.TrimSpace(stateDir)
	if root == "" {
		if strings.TrimSpace(workspace) == "" {
			return nil, nil
		}
		root = filepath.Join(workspace, "state", "evolution")
	}
	root, err := filepath.Abs(filepath.Clean(root))
	if err != nil {
		return nil, fmt.Errorf("resolve evolution state directory: %w", err)
	}
	database := filepath.Join(root, "evolution.db")
	return []string{
		database,
		database + "-wal",
		database + "-shm",
		filepath.Join(root, "legacy-json"),
		filepath.Join(root, "learning-records.jsonl"),
		filepath.Join(root, "task-records.jsonl"),
		filepath.Join(root, "pattern-records.jsonl"),
		filepath.Join(root, "skill-drafts.json"),
		filepath.Join(root, "profiles"),
		filepath.Join(root, "backups"),
	}, nil
}

func mustAgentRuntimeFileMutationProtectedRoots(
	configPath string,
	activeConfigs ...*config.Config,
) []string {
	roots, err := agentRuntimeFileMutationProtectedRoots(configPath, activeConfigs...)
	if err != nil {
		panic(fmt.Sprintf("build file-mutation policy: %v", err))
	}
	return roots
}

func agentLocalCIEvidenceFileMutationProtectedRoots(cfg *config.Config) ([]string, error) {
	if cfg == nil || !cfg.Events.Ingress.Enabled {
		return nil, nil
	}
	ingress := config.EffectiveEventIngressConfig(cfg, cfg.WorkspacePath())
	databasePath, err := filepath.Abs(filepath.Clean(ingress.DatabasePath))
	if err != nil {
		return nil, fmt.Errorf("resolve local CI event database path: %w", err)
	}
	evidenceRoot := filepath.Join(
		filepath.Dir(databasePath),
		"pr-workspace-local-ci",
		"evidence",
	)
	cacheDatabase := filepath.Join(evidenceRoot, agentLocalCICacheDatabaseName)
	return []string{
		// The namespace protects immutable evidence plus active and archived
		// legacy cache indexes. Exact database paths additionally catch an
		// existing hardlink alias outside this namespace.
		evidenceRoot,
		cacheDatabase,
		cacheDatabase + "-wal",
		cacheDatabase + "-shm",
		filepath.Join(evidenceRoot, "cache"),
		filepath.Join(evidenceRoot, "legacy-json"),
	}, nil
}

func mustAgentLocalCIEvidenceFileMutationProtectedRoots(cfg *config.Config) []string {
	roots, err := agentLocalCIEvidenceFileMutationProtectedRoots(cfg)
	if err != nil {
		panic(fmt.Sprintf("build local CI file-mutation policy: %v", err))
	}
	return roots
}

func cloneAgentRuntimeFileMutationProtectedRoots(roots []string) []string {
	return append([]string(nil), roots...)
}

func agentWorkspaceAccountRouterProtectedRoots(workspace string) ([]string, error) {
	workspace, err := filepath.Abs(filepath.Clean(workspace))
	if err != nil {
		return nil, fmt.Errorf("resolve account-router workspace: %w", err)
	}
	database := filepath.Join(workspace, "state", "account-router.db")
	lockDirectory := database + ".locks"
	legacySource := filepath.Join(workspace, "account_router_state.json")
	archiveRoot := filepath.Join(workspace, "state", "legacy-json", "account-router-v1")
	roots := []string{
		database,
		database + "-wal",
		database + "-shm",
		lockDirectory,
		filepath.Join(lockDirectory, "store.lock"),
		legacySource,
		archiveRoot,
		filepath.Join(archiveRoot, "account_router_state.json"),
	}
	entries, err := os.ReadDir(workspace)
	if os.IsNotExist(err) {
		entries = nil
	} else if err != nil {
		return nil, fmt.Errorf("enumerate account-router legacy state: %w", err)
	}
	for _, entry := range entries {
		if agentAccountRouterLegacySidecarName(entry.Name()) {
			roots = append(roots, filepath.Join(workspace, entry.Name()))
		}
	}
	if err := filepath.WalkDir(archiveRoot, func(path string, entry os.DirEntry, walkErr error) error {
		if os.IsNotExist(walkErr) {
			return nil
		}
		if walkErr != nil {
			return walkErr
		}
		if path != archiveRoot && !entry.IsDir() {
			roots = append(roots, path)
		}
		return nil
	}); err != nil && !os.IsNotExist(err) {
		return nil, fmt.Errorf("enumerate account-router archives: %w", err)
	}
	slices.Sort(roots)
	return slices.Compact(roots), nil
}

func agentAccountRouterLegacySidecarName(name string) bool {
	suffix, ok := strings.CutPrefix(name, "account_router_state.json.auth-invalidation.")
	if !ok || len(suffix) != 32 || suffix != strings.ToLower(suffix) {
		return false
	}
	_, err := hex.DecodeString(suffix)
	return err == nil
}

func mustAgentWorkspaceAccountRouterProtectedRoots(workspace string) []string {
	roots, err := agentWorkspaceAccountRouterProtectedRoots(workspace)
	if err != nil {
		panic(fmt.Sprintf("build account-router file-mutation policy: %v", err))
	}
	return roots
}

func agentSessionFileMutationProtectedRoots(workspace string) []string {
	database := filepath.Join(workspace, "sessions", "sessions.db")
	return []string{
		filepath.Join(workspace, "sessions"),
		filepath.Join(workspace, "threads"),
		database,
		database + "-wal",
		database + "-shm",
		// Protect the namespace so a model cannot pre-create the archive parent
		// as a file before the first migration needs it.
		filepath.Join(workspace, "legacy-json"),
	}
}

func agentCronFileMutationProtectedRoots(workspace string) []string {
	root := filepath.Join(workspace, "cron")
	database := filepath.Join(root, "jobs.db")
	archiveRoot := filepath.Join(root, "legacy-json")
	return []string{
		root,
		database,
		database + "-wal",
		database + "-shm",
		filepath.Join(root, "jobs.json"),
		archiveRoot,
		filepath.Join(archiveRoot, "cron-jobs-v1", "jobs.json"),
	}
}

func appendAgentWorkspaceSQLiteProtectedRoots(
	roots []string,
	cfg *config.Config,
) ([]string, error) {
	if cfg == nil {
		return roots, nil
	}
	workspace, err := filepath.Abs(filepath.Clean(cfg.WorkspacePath()))
	if err != nil {
		return nil, fmt.Errorf("resolve PicoClaw workspace: %w", err)
	}
	return appendAgentRepositoryFileMutationProtectedRoots(roots, workspace), nil
}

func appendAgentRepositoryFileMutationProtectedRoots(roots []string, workspace string) []string {
	for _, directory := range []string{agentRepositoryReviewStateDir, agentRepositoryEvalStateDir} {
		candidate := filepath.Join(workspace, directory)
		duplicate := false
		for _, existing := range roots {
			if existing == candidate {
				duplicate = true
				break
			}
		}
		if !duplicate {
			roots = append(roots, candidate)
		}
	}
	return roots
}

// agentFileMutationIdentityCatalog snapshots physical identities for mutable
// legacy trees whose files may be renamed into archives after agent
// construction. The immutable catalog is shared by every factory product in
// this AgentInstance generation rather than copying up to millions of entries
// into each owner.
func agentFileMutationIdentityCatalog(
	workspace string,
	cfg *config.Config,
	exactRoots []string,
) (*tools.FileIdentityCatalog, error) {
	workspace, err := filepath.Abs(filepath.Clean(workspace))
	if err != nil {
		return nil, fmt.Errorf("resolve file-identity workspace: %w", err)
	}
	trees := []string{
		filepath.Join(workspace, "sessions"),
		filepath.Join(workspace, "threads"),
		filepath.Join(workspace, "legacy-json", "sessions-v1"),
		filepath.Join(workspace, "workflow_runs"),
		filepath.Join(workspace, "workflow_validations"),
		filepath.Join(workspace, "workflow_dev"),
		filepath.Join(workspace, "workflow_state"),
		filepath.Join(workspace, "legacy-json", "workflows-v1"),
		filepath.Join(workspace, agentRepositoryReviewStateDir),
		filepath.Join(workspace, agentRepositoryEvalStateDir),
	}
	evolutionRoots, err := agentEvolutionFileMutationProtectedRoots(
		workspace,
		func() string {
			if cfg == nil {
				return ""
			}
			return cfg.Evolution.StateDir
		}(),
	)
	if err != nil {
		return nil, err
	}
	if len(evolutionRoots) != 0 {
		evolutionRoot := filepath.Dir(evolutionRoots[0])
		trees = append(trees,
			filepath.Join(evolutionRoot, "profiles"),
			filepath.Join(evolutionRoot, "backups"),
			filepath.Join(evolutionRoot, "legacy-json", "evolution-v1"),
		)
	}
	if cfg != nil {
		localCIRoots, localCIErr := agentLocalCIEvidenceFileMutationProtectedRoots(cfg)
		if localCIErr != nil {
			return nil, localCIErr
		}
		if len(localCIRoots) != 0 {
			evidenceRoot := localCIRoots[0]
			trees = append(trees,
				filepath.Join(evidenceRoot, "cache"),
				filepath.Join(evidenceRoot, "legacy-json", "local-ci-cache-v1"),
			)
		}
	}
	exactIdentities := make([]string, 0, len(exactRoots))
	for _, candidate := range exactRoots {
		base := strings.ToLower(filepath.Base(candidate))
		if strings.Contains(base, ".json") || strings.HasSuffix(base, ".jsonl") ||
			strings.HasSuffix(base, ".history-a") || strings.HasSuffix(base, ".history-b") {
			exactIdentities = append(exactIdentities, candidate)
		}
	}
	excludePaths := make([]string, 0, 9)
	for _, database := range []string{
		filepath.Join(workspace, "sessions", "sessions.db"),
		filepath.Join(workspace, agentRepositoryReviewStateDir, "repository-reviews.db"),
		filepath.Join(workspace, agentRepositoryEvalStateDir, "evaluations.db"),
	} {
		excludePaths = append(excludePaths, database, database+"-wal", database+"-shm")
	}
	return tools.NewFileIdentityCatalog(tools.FileIdentityCatalogOptions{
		ExactPaths:   exactIdentities,
		TreeRoots:    trees,
		ExcludePaths: excludePaths,
	})
}

type agentFileMutationIdentityGeneration struct {
	mu       sync.Mutex
	catalogs map[string]*tools.FileIdentityCatalog
}

func (generation *agentFileMutationIdentityGeneration) catalog(
	workspace string,
	cfg *config.Config,
	exactRoots []string,
) (*tools.FileIdentityCatalog, error) {
	if generation == nil {
		return agentFileMutationIdentityCatalog(workspace, cfg, exactRoots)
	}
	key := workspace + "\x00" + strings.Join(exactRoots, "\x00")
	if cfg != nil {
		key += "\x00" + cfg.Evolution.StateDir + "\x00" + cfg.Events.Ingress.DatabasePath
	}
	generation.mu.Lock()
	defer generation.mu.Unlock()
	if catalog := generation.catalogs[key]; catalog != nil {
		return catalog, nil
	}
	catalog, err := agentFileMutationIdentityCatalog(workspace, cfg, exactRoots)
	if err != nil {
		return nil, err
	}
	if generation.catalogs == nil {
		generation.catalogs = make(map[string]*tools.FileIdentityCatalog)
	}
	generation.catalogs[key] = catalog
	return catalog, nil
}
