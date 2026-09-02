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
	"github.com/sipeed/picoclaw/pkg/database/artifacts"
	"github.com/sipeed/picoclaw/pkg/tools"
)

const (
	agentRepositoryReviewStateDir = "repository_reviews"
	agentRepositoryEvalStateDir   = "repository_evaluations"
	agentCheckpointMaximumSources = 10_000
	agentCheckpointArchiveEntries = agentCheckpointMaximumSources + 16
	agentCheckpointArchiveDepth   = 4
	agentCheckpointReadBatch      = 128
)

const (
	agentFileIdentityDirectoryEntryLimit = 131_072
	agentAccountRouterLegacySidecarLimit = 100_000
	agentFileIdentityDirectoryReadBatch  = 256
	agentAccountRouterInvalidationPrefix = "account_router_state.json.auth-invalidation."
)

func agentWorkflowRuntimeFileMutationProtectedRoots(workspace string) ([]string, error) {
	workspace, err := filepath.Abs(filepath.Clean(workspace))
	if err != nil {
		return nil, fmt.Errorf("resolve workflow workspace: %w", err)
	}
	roots, err := agentProviderArtifactRoots(workspace, nil, "workflows")
	if err != nil {
		return nil, err
	}
	return normalizeProtectedRoots(append(roots,
		filepath.Join(workspace, "state"),
		filepath.Join(workspace, "legacy-json"),
		filepath.Join(workspace, "workflow_runs"),
		filepath.Join(workspace, "workflow_validations"),
		filepath.Join(workspace, "workflow_dev"),
		filepath.Join(workspace, "workflow_state"),
		filepath.Join(workspace, "workflow_state", "mutation.lock"),
		filepath.Join(workspace, "workflow_state", "publish-transaction.json"),
		filepath.Join(workspace, "workflow_state", "template-transaction.json"),
	)), nil
}

func normalizeProtectedRoots(roots []string) []string {
	slices.Sort(roots)
	return slices.Compact(roots)
}

func agentProviderArtifactRoots(
	workspace string,
	configure func(*config.Config),
	domains ...string,
) ([]string, error) {
	home, err := filepath.Abs(filepath.Clean(config.GetHome()))
	if err != nil {
		return nil, fmt.Errorf("resolve provider catalog home: %w", err)
	}
	cfg := &config.Config{Agents: config.AgentsConfig{
		Defaults: config.AgentDefaults{Workspace: workspace},
	}}
	if configure != nil {
		configure(cfg)
	}
	catalog, err := artifacts.New(home, cfg)
	if err != nil {
		return nil, fmt.Errorf("build workspace provider artifact catalog: %w", err)
	}
	return catalog.ProtectedRootsForDomains(domains...), nil
}

func mustAgentProviderArtifactRoots(
	workspace string,
	configure func(*config.Config),
	domains ...string,
) []string {
	roots, err := agentProviderArtifactRoots(workspace, configure, domains...)
	if err != nil {
		panic(fmt.Sprintf("build provider artifact policy: %v", err))
	}
	return roots
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

	var activeConfig *config.Config
	if len(activeConfigs) > 0 {
		activeConfig = activeConfigs[0]
	}
	catalogConfig := activeConfig
	if catalogConfig == nil {
		catalogConfig, err = config.LoadConfig(configPath)
	}
	if catalogConfig == nil || err != nil {
		// Filesystem mutation protection must remain constructible while the
		// active config is being atomically replaced or repaired. Fall back to
		// the canonical-home catalog; caller-specific workspace roots are added
		// from the already validated in-memory config when one is available.
		catalogConfig = &config.Config{}
	}
	providerArtifacts, err := artifacts.New(home, catalogConfig)
	if err != nil {
		return nil, fmt.Errorf("build provider artifact catalog: %w", err)
	}
	protected := providerArtifacts.ProtectedRoots()

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

	wecomArchiveRoot := filepath.Join(home, "legacy-json", "wecom-reqid-v1")
	protected = append(protected,
		filepath.Join(home, "wecom", "reqid-store.json"),
		wecomArchiveRoot,
		filepath.Join(wecomArchiveRoot, "wecom", "reqid-store.json"),
	)

	weixinRoot := filepath.Join(home, "channels", "weixin")
	weixinArchiveRoot := filepath.Join(weixinRoot, "legacy-json", "weixin-state-v1")
	protected = append(protected,
		filepath.Join(weixinRoot, "sync"),
		filepath.Join(weixinRoot, "context-tokens"),
		weixinArchiveRoot,
	)
	gitRoots, err := agentGitWorkspaceFileMutationProtectedRoots(catalogConfig)
	if err != nil {
		return nil, err
	}
	return append(protected, gitRoots...), nil
}

func agentGitWorkspaceFileMutationProtectedRoots(activeConfig *config.Config) ([]string, error) {
	home, err := filepath.Abs(filepath.Clean(config.GetHome()))
	if err != nil {
		return nil, fmt.Errorf("resolve Git workspace provider home: %w", err)
	}
	providerCatalog, err := artifacts.New(home, activeConfig)
	if err != nil {
		return nil, fmt.Errorf("build Git workspace provider artifact catalog: %w", err)
	}
	gitWorkspaceRoot := activeConfig.GitWorkspaceRootPath()
	gitWorkspaceRoot, err = filepath.Abs(filepath.Clean(gitWorkspaceRoot))
	if err != nil {
		return nil, fmt.Errorf("resolve Git workspace runtime root: %w", err)
	}
	checkpointRoot := filepath.Join(
		gitWorkspaceRoot,
		".pr-workspace-implementation",
		"active",
	)
	checkpointArchiveRoot := filepath.Join(checkpointRoot, "legacy-json")
	protected := providerCatalog.ProtectedRootsForDomains(
		"git-workspace-inventory",
		"pr-workspace-checkpoints",
	)
	protected = append(protected,
		filepath.Join(gitWorkspaceRoot, "inventory.lock"),
		filepath.Join(gitWorkspaceRoot, ".locks"),
		filepath.Join(gitWorkspaceRoot, "legacy-json"),
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
	return normalizeProtectedRoots(protected), nil
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
	snapshot, err := agentCheckpointRetainedStateSnapshotBounded(
		checkpointRoot,
		archiveRoot,
		maximumRootEntries,
		maximumSources,
		maximumArchiveEntries,
		maximumArchiveDepth,
	)
	if err != nil {
		return nil, err
	}
	result := make([]string, 0, len(snapshot))
	for path, entry := range snapshot {
		if entry.protect {
			result = append(result, path)
		}
	}
	slices.Sort(result)
	return result, nil
}

type agentCheckpointStateEntry struct {
	info    os.FileInfo
	missing bool
	protect bool
}

type agentCheckpointStateSnapshot map[string]agentCheckpointStateEntry

// Package-test seams for deterministic namespace-race coverage. Production
// leaves both nil.
var (
	agentCheckpointDuringRootEnumeration                func()
	agentFileMutationIdentityBetweenCheckpointSnapshots func()
)

func agentCheckpointRetainedStateSnapshotBounded(
	checkpointRoot,
	archiveRoot string,
	maximumRootEntries,
	maximumSources,
	maximumArchiveEntries,
	maximumArchiveDepth int,
) (agentCheckpointStateSnapshot, error) {
	if maximumRootEntries < 1 || maximumSources < 1 || maximumArchiveEntries < 1 ||
		maximumArchiveDepth < 1 {
		return nil, errors.New("enumerate PR workspace checkpoint state: bounds are invalid")
	}
	snapshot := make(agentCheckpointStateSnapshot)
	rootInfo, err := os.Lstat(checkpointRoot)
	switch {
	case os.IsNotExist(err):
		snapshot[checkpointRoot] = agentCheckpointStateEntry{missing: true}
	case err != nil:
		return nil, errors.New("enumerate PR workspace checkpoint state: root cannot be inspected")
	case !privateAgentCheckpointDirectory(checkpointRoot, rootInfo) || rootInfo.Mode()&os.ModeSymlink != 0:
		return nil, errors.New("enumerate PR workspace checkpoint state: root is unsafe")
	default:
		root, openErr := os.OpenRoot(checkpointRoot)
		if openErr != nil {
			return nil, errors.New("enumerate PR workspace checkpoint state: root cannot be opened")
		}
		openedInfo, statErr := root.Stat(".")
		if statErr != nil || !stableAgentCheckpointDirectory(rootInfo, openedInfo) {
			_ = root.Close()
			return nil, errors.New("enumerate PR workspace checkpoint state: root changed")
		}
		snapshot[checkpointRoot] = agentCheckpointStateEntry{info: openedInfo}
		directory, directoryErr := root.Open(".")
		if directoryErr != nil {
			_ = root.Close()
			return nil, errors.New("enumerate PR workspace checkpoint state: root cannot be opened")
		}
		directoryInfo, directoryStatErr := directory.Stat()
		if directoryStatErr != nil || !stableAgentCheckpointDirectory(openedInfo, directoryInfo) {
			_ = directory.Close()
			_ = root.Close()
			return nil, errors.New("enumerate PR workspace checkpoint state: root changed")
		}
		entries, sources := 0, 0
		if agentCheckpointDuringRootEnumeration != nil {
			agentCheckpointDuringRootEnumeration()
		}
		readErr := forEachCheckpointDirectoryEntry(directory, func(entry os.DirEntry) error {
			entries++
			if entries > maximumRootEntries {
				return errors.New("entry limit exceeded")
			}
			info, infoErr := root.Lstat(entry.Name())
			entryInfo, entryErr := entry.Info()
			if infoErr == nil && (entry.Type()&os.ModeSymlink != 0 ||
				info.Mode()&os.ModeSymlink != 0) {
				return errors.New("entry is unsafe")
			}
			if infoErr != nil || entryErr != nil || info.Mode() != entryInfo.Mode() ||
				!os.SameFile(info, entryInfo) {
				return errors.New("entry changed during inspection")
			}
			if !strings.HasSuffix(entry.Name(), ".json") {
				return nil
			}
			sources++
			if sources > maximumSources {
				return errors.New("source limit exceeded")
			}
			path := filepath.Join(checkpointRoot, entry.Name())
			if !privateAgentCheckpointFile(path, info) || info.Mode()&os.ModeSymlink != 0 {
				return errors.New("source is unsafe")
			}
			snapshot[path] = agentCheckpointStateEntry{info: info, protect: true}
			predictedArchive := filepath.Join(
				archiveRoot,
				"pr-workspace-checkpoints-v1",
				entry.Name(),
			)
			predictedInfo, predictedErr := os.Lstat(predictedArchive)
			switch {
			case os.IsNotExist(predictedErr):
				snapshot[predictedArchive] = agentCheckpointStateEntry{missing: true, protect: true}
			case predictedErr != nil:
				return errors.New("predicted archive cannot be inspected")
			case predictedInfo.Mode()&os.ModeSymlink != 0 ||
				!privateAgentCheckpointFile(predictedArchive, predictedInfo):
				return errors.New("predicted archive is unsafe")
			default:
				snapshot[predictedArchive] = agentCheckpointStateEntry{
					info: predictedInfo, protect: true,
				}
			}
			return nil
		})
		directoryCloseErr := directory.Close()
		after, afterErr := root.Stat(".")
		current, currentErr := os.Lstat(checkpointRoot)
		rootCloseErr := root.Close()
		if readErr != nil {
			return nil, safeAgentCheckpointEnumerationError(
				"enumerate PR workspace checkpoint state",
				readErr,
			)
		}
		if directoryCloseErr != nil || rootCloseErr != nil || afterErr != nil || currentErr != nil ||
			!stableAgentCheckpointDirectory(rootInfo, after) ||
			!stableAgentCheckpointDirectory(rootInfo, current) {
			return nil, errors.New("enumerate PR workspace checkpoint state: root changed")
		}
	}

	archiveEntries := 0
	err = snapshotCheckpointArchive(
		archiveRoot,
		maximumArchiveDepth,
		maximumArchiveEntries,
		&archiveEntries,
		snapshot,
	)
	if err != nil && !os.IsNotExist(err) {
		return nil, safeAgentCheckpointEnumerationError(
			"enumerate PR workspace checkpoint archives",
			err,
		)
	}
	if os.IsNotExist(err) {
		snapshot[archiveRoot] = agentCheckpointStateEntry{missing: true}
	}
	return snapshot, nil
}

func snapshotCheckpointArchive(
	archiveRoot string,
	maximumDepth,
	maximumEntries int,
	entries *int,
	snapshot agentCheckpointStateSnapshot,
) error {
	info, err := os.Lstat(archiveRoot)
	if os.IsNotExist(err) {
		return os.ErrNotExist
	}
	if err != nil {
		return errors.New("checkpoint archive cannot be inspected")
	}
	if !privateAgentCheckpointDirectory(archiveRoot, info) || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("checkpoint archive contains an unsafe directory")
	}
	root, err := os.OpenRoot(archiveRoot)
	if err != nil {
		return errors.New("checkpoint archive cannot be opened")
	}
	openedInfo, statErr := root.Stat(".")
	if statErr != nil || !stableAgentCheckpointDirectory(info, openedInfo) {
		_ = root.Close()
		return errors.New("checkpoint archive directory changed during enumeration")
	}
	snapshot[archiveRoot] = agentCheckpointStateEntry{info: openedInfo}
	collectErr := collectPinnedCheckpointArchive(
		root,
		archiveRoot,
		0,
		maximumDepth,
		maximumEntries,
		entries,
		snapshot,
	)
	after, afterErr := root.Stat(".")
	current, currentErr := os.Lstat(archiveRoot)
	closeErr := root.Close()
	if collectErr != nil {
		return collectErr
	}
	if afterErr != nil || currentErr != nil || closeErr != nil ||
		!stableAgentCheckpointDirectory(info, after) ||
		!stableAgentCheckpointDirectory(info, current) {
		return errors.New("checkpoint archive directory changed during enumeration")
	}
	return nil
}

func collectPinnedCheckpointArchive(
	root *os.Root,
	directory string,
	depth,
	maximumDepth,
	maximumEntries int,
	entries *int,
	snapshot agentCheckpointStateSnapshot,
) error {
	before, err := root.Stat(".")
	if err != nil || !privateAgentCheckpointDirectory(directory, before) {
		return errors.New("checkpoint archive contains an unsafe directory")
	}
	opened, err := root.Open(".")
	if err != nil {
		return errors.New("checkpoint archive directory cannot be opened")
	}
	openedInfo, statErr := opened.Stat()
	if statErr != nil || !stableAgentCheckpointDirectory(before, openedInfo) {
		_ = opened.Close()
		return errors.New("checkpoint archive directory changed during enumeration")
	}
	err = forEachCheckpointDirectoryEntry(opened, func(entry os.DirEntry) error {
		*entries++
		if *entries > maximumEntries {
			return errors.New("checkpoint archive exceeds its safe entry limit")
		}
		path := filepath.Join(directory, entry.Name())
		entryInfo, entryErr := root.Lstat(entry.Name())
		directoryEntryInfo, directoryEntryErr := entry.Info()
		if entryErr != nil || directoryEntryErr != nil ||
			entryInfo.Mode() != directoryEntryInfo.Mode() ||
			!os.SameFile(entryInfo, directoryEntryInfo) {
			return errors.New("checkpoint archive entry changed during inspection")
		}
		if entry.Type()&os.ModeSymlink != 0 || entryInfo.Mode()&os.ModeSymlink != 0 {
			return errors.New("checkpoint archive contains an unsafe symlink")
		}
		if entryInfo.IsDir() {
			if depth >= maximumDepth {
				return errors.New("checkpoint archive exceeds its safe depth limit")
			}
			child, childErr := root.OpenRoot(entry.Name())
			if childErr != nil {
				return errors.New("checkpoint archive child cannot be opened")
			}
			childInfo, childStatErr := child.Stat(".")
			if childStatErr != nil || !stableAgentCheckpointDirectory(entryInfo, childInfo) {
				_ = child.Close()
				return errors.New("checkpoint archive directory changed during enumeration")
			}
			snapshot[path] = agentCheckpointStateEntry{info: childInfo}
			childCollectErr := collectPinnedCheckpointArchive(
				child, path, depth+1, maximumDepth, maximumEntries, entries, snapshot,
			)
			childCloseErr := child.Close()
			if childCollectErr != nil {
				return childCollectErr
			}
			if childCloseErr != nil {
				return errors.New("checkpoint archive child cannot be closed")
			}
			return nil
		}
		if !privateAgentCheckpointFile(path, entryInfo) {
			return errors.New("checkpoint archive contains an unsafe file")
		}
		snapshot[path] = agentCheckpointStateEntry{info: entryInfo, protect: true}
		return nil
	})
	closeErr := opened.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	after, afterErr := root.Stat(".")
	current, currentErr := os.Lstat(directory)
	if afterErr != nil || currentErr != nil ||
		!stableAgentCheckpointDirectory(before, after) ||
		!stableAgentCheckpointDirectory(before, current) {
		return errors.New("checkpoint archive directory changed during enumeration")
	}
	return nil
}

func safeAgentCheckpointEnumerationError(prefix string, err error) error {
	if prefix == "" || err == nil {
		return errors.New("checkpoint enumeration failed")
	}
	message := err.Error()
	for _, safe := range []string{
		"entry limit exceeded",
		"source limit exceeded",
		"entry is unsafe",
		"entry changed during inspection",
		"source is unsafe",
		"predicted archive cannot be inspected",
		"predicted archive is unsafe",
		"checkpoint archive exceeds its safe entry limit",
		"checkpoint archive entry changed during inspection",
		"checkpoint archive contains an unsafe symlink",
		"checkpoint archive exceeds its safe depth limit",
		"checkpoint archive contains an unsafe directory",
		"checkpoint archive contains an unsafe file",
		"checkpoint archive directory changed during enumeration",
		"checkpoint archive directory cannot be opened",
		"checkpoint archive child cannot be opened",
		"checkpoint archive child cannot be closed",
	} {
		if message == safe {
			return errors.New(prefix + ": " + safe)
		}
	}
	return errors.New(prefix + ": enumeration failed")
}

func stableAgentCheckpointDirectory(before, after os.FileInfo) bool {
	return before != nil && after != nil && before.IsDir() && after.IsDir() &&
		before.Mode() == after.Mode() && before.Size() == after.Size() &&
		before.ModTime().Equal(after.ModTime()) && os.SameFile(before, after)
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
	archiveRoot := filepath.Join(stateRoot, "legacy-json", "runtime-state-v1")
	roots, err := agentProviderArtifactRoots(workspace, nil, "runtime-state")
	if err != nil {
		return nil, err
	}
	roots = append(roots,
		// The root-level legacy source lies outside stateRoot and must remain
		// protected until its first authoritative SQLite open archives it.
		filepath.Join(workspace, "state.json"),
		filepath.Join(stateRoot, "state.json"),
		archiveRoot,
		filepath.Join(archiveRoot, "state.json"),
		filepath.Join(archiveRoot, "state", "state.json"),
	)
	return normalizeProtectedRoots(roots), nil
}

func mustAgentWorkspaceFileMutationProtectedRoots(workspace string) []string {
	roots, err := agentWorkspaceFileMutationProtectedRoots(workspace)
	if err != nil {
		panic(fmt.Sprintf("build workspace file-mutation policy: %v", err))
	}
	return roots
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
	roots, err := agentProviderArtifactRoots(workspace, func(cfg *config.Config) {
		cfg.Evolution.StateDir = stateDir
	}, "evolution")
	if err != nil {
		return nil, err
	}
	return normalizeProtectedRoots(append(roots,
		filepath.Join(root, "legacy-json"),
		filepath.Join(root, "learning-records.jsonl"),
		filepath.Join(root, "task-records.jsonl"),
		filepath.Join(root, "pattern-records.jsonl"),
		filepath.Join(root, "skill-drafts.json"),
		filepath.Join(root, "profiles"),
		filepath.Join(root, "backups"),
	)), nil
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
	home, err := filepath.Abs(filepath.Clean(config.GetHome()))
	if err != nil {
		return nil, err
	}
	providerCatalog, err := artifacts.New(home, cfg)
	if err != nil {
		return nil, err
	}
	return normalizeProtectedRoots(providerCatalog.ProtectedRootsForDomains("local-ci")), nil
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
	legacySource := filepath.Join(workspace, "account_router_state.json")
	archiveRoot := filepath.Join(workspace, "state", "legacy-json", "account-router-v1")
	roots, err := agentProviderArtifactRoots(workspace, nil, "account-routing")
	if err != nil {
		return nil, err
	}
	roots = append(roots,
		legacySource,
		archiveRoot,
		filepath.Join(archiveRoot, "account_router_state.json"),
	)
	return normalizeProtectedRoots(roots), nil
}

func agentAccountRouterLegacySidecarName(name string) bool {
	suffix, ok := strings.CutPrefix(name, agentAccountRouterInvalidationPrefix)
	if !ok || len(suffix) != 32 || suffix != strings.ToLower(suffix) {
		return false
	}
	_, err := hex.DecodeString(suffix)
	return err == nil
}

func agentAccountRouterFileMutationProtectedPrefixes(
	workspaces []string,
) ([]tools.FileMutationSiblingPrefix, error) {
	normalized, err := normalizeAgentFileMutationWorkspaces(workspaces)
	if err != nil {
		return nil, err
	}
	prefixes := make([]tools.FileMutationSiblingPrefix, 0, len(normalized))
	for _, workspace := range normalized {
		prefixes = append(prefixes, tools.FileMutationSiblingPrefix{
			Parent: workspace,
			Prefix: agentAccountRouterInvalidationPrefix,
		})
	}
	return prefixes, nil
}

func mustAgentWorkspaceAccountRouterProtectedRoots(workspace string) []string {
	roots, err := agentWorkspaceAccountRouterProtectedRoots(workspace)
	if err != nil {
		panic(fmt.Sprintf("build account-router file-mutation policy: %v", err))
	}
	return roots
}

func agentSessionFileMutationProtectedRoots(workspace string) []string {
	return normalizeProtectedRoots(append(mustAgentProviderArtifactRoots(workspace, nil, "sessions"),
		// Protect the namespace so a model cannot pre-create the archive parent
		// as a file before the first migration needs it.
		filepath.Join(workspace, "legacy-json"),
	))
}

func agentCronFileMutationProtectedRoots(workspace string) []string {
	root := filepath.Join(workspace, "cron")
	archiveRoot := filepath.Join(root, "legacy-json")
	return normalizeProtectedRoots(append(mustAgentProviderArtifactRoots(workspace, nil, "cron"),
		root,
		filepath.Join(root, "jobs.json"),
		archiveRoot,
		filepath.Join(archiveRoot, "cron-jobs-v1", "jobs.json"),
	))
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
	return append(roots, mustAgentProviderArtifactRoots(
		workspace,
		nil,
		"repository-reviews",
		"repository-evaluations",
	)...)
}

func appendAgentCompleteWorkspaceFileMutationProtectedRoots(
	roots []string,
	workspace string,
	cfg *config.Config,
) ([]string, error) {
	workspace, err := filepath.Abs(filepath.Clean(workspace))
	if err != nil {
		return nil, fmt.Errorf("resolve complete file-mutation workspace: %w", err)
	}
	workspaceRoots, err := agentWorkspaceFileMutationProtectedRoots(workspace)
	if err != nil {
		return nil, err
	}
	accountRouterRoots, err := agentWorkspaceAccountRouterProtectedRoots(workspace)
	if err != nil {
		return nil, err
	}
	workflowRoots, err := agentWorkflowRuntimeFileMutationProtectedRoots(workspace)
	if err != nil {
		return nil, err
	}
	evolutionStateDir := ""
	if cfg != nil {
		evolutionStateDir = cfg.Evolution.StateDir
	}
	evolutionRoots, err := agentEvolutionFileMutationProtectedRoots(
		workspace,
		evolutionStateDir,
	)
	if err != nil {
		return nil, err
	}
	roots = append(roots, workspaceRoots...)
	roots = append(roots, accountRouterRoots...)
	roots = append(roots, agentSessionFileMutationProtectedRoots(workspace)...)
	roots = append(roots, agentCronFileMutationProtectedRoots(workspace)...)
	roots = append(roots, workflowRoots...)
	roots = append(roots, evolutionRoots...)
	roots = appendAgentRepositoryFileMutationProtectedRoots(roots, workspace)
	return roots, nil
}

func normalizeAgentFileMutationWorkspaces(workspaces []string) ([]string, error) {
	result := make([]string, 0, len(workspaces))
	seen := make(map[string]struct{}, len(workspaces))
	for index, workspace := range workspaces {
		if workspace == "" || workspace != strings.TrimSpace(workspace) ||
			strings.ContainsRune(workspace, '\x00') {
			return nil, fmt.Errorf("file-mutation workspace %d is invalid", index)
		}
		absolute, err := filepath.Abs(filepath.Clean(workspace))
		if err != nil {
			return nil, fmt.Errorf("file-mutation workspace %d is invalid", index)
		}
		key := agentFileMutationWorkspaceKey(absolute)
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		result = append(result, absolute)
	}
	slices.Sort(result)
	return result, nil
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
	return agentFileMutationIdentityCatalogForWorkspaces(
		[]string{workspace},
		cfg,
		exactRoots,
	)
}

func agentFileMutationIdentityCatalogForWorkspaces(
	workspaces []string,
	cfg *config.Config,
	exactRoots []string,
) (*tools.FileIdentityCatalog, error) {
	workspaces = append(
		append([]string(nil), workspaces...),
		agentFileMutationWorkspacesFromProtectedRoots(exactRoots)...,
	)
	workspaces, err := normalizeAgentFileMutationWorkspaces(workspaces)
	if err != nil {
		return nil, err
	}
	if len(workspaces) == 0 {
		return nil, errors.New("file-identity catalog has no workspace")
	}
	trees, exclusions, err := agentFileMutationIdentityCatalogTrees(
		workspaces,
		cfg,
		exactRoots,
	)
	if err != nil {
		return nil, err
	}
	exactIdentities := make([]string, 0, len(exactRoots))
	for _, candidate := range exactRoots {
		base := strings.ToLower(filepath.Base(candidate))
		if strings.Contains(base, ".json") || strings.HasSuffix(base, ".jsonl") ||
			strings.HasSuffix(base, ".history-a") || strings.HasSuffix(base, ".history-b") {
			exactIdentities = append(exactIdentities, candidate)
		}
	}
	beforeSidecars, err := agentAccountRouterLegacySidecarSnapshot(
		workspaces,
		agentFileIdentityDirectoryEntryLimit,
		agentAccountRouterLegacySidecarLimit,
	)
	if err != nil {
		return nil, err
	}
	for path := range beforeSidecars {
		exactIdentities = append(exactIdentities, path)
	}
	beforeCheckpoints, err := agentCheckpointRetainedStateSnapshot(cfg)
	if err != nil {
		return nil, err
	}
	for path, entry := range beforeCheckpoints {
		if entry.protect {
			exactIdentities = append(exactIdentities, path)
		}
	}
	catalog, err := tools.NewFileIdentityCatalog(tools.FileIdentityCatalogOptions{
		ExactPaths:   exactIdentities,
		TreeRoots:    trees,
		ExcludePaths: exclusions,
	})
	if err != nil {
		return nil, err
	}
	if agentFileMutationIdentityBetweenSidecarSnapshots != nil {
		agentFileMutationIdentityBetweenSidecarSnapshots()
	}
	afterSidecars, err := agentAccountRouterLegacySidecarSnapshot(
		workspaces,
		agentFileIdentityDirectoryEntryLimit,
		agentAccountRouterLegacySidecarLimit,
	)
	if err != nil {
		return nil, err
	}
	if !equalAgentAccountRouterLegacySidecarSnapshots(beforeSidecars, afterSidecars) {
		return nil, errors.New("account-router legacy sidecars changed during snapshot")
	}
	if agentFileMutationIdentityBetweenCheckpointSnapshots != nil {
		agentFileMutationIdentityBetweenCheckpointSnapshots()
	}
	afterCheckpoints, err := agentCheckpointRetainedStateSnapshot(cfg)
	if err != nil {
		return nil, err
	}
	if !equalAgentCheckpointStateSnapshots(beforeCheckpoints, afterCheckpoints) {
		return nil, errors.New("PR workspace checkpoint state changed during snapshot")
	}
	return catalog, nil
}

func agentFileMutationIdentityCatalogTrees(
	workspaces []string,
	cfg *config.Config,
	exactRoots []string,
) ([]string, []string, error) {
	home, err := filepath.Abs(filepath.Clean(config.GetHome()))
	if err != nil {
		return nil, nil, fmt.Errorf("resolve file-identity home: %w", err)
	}
	configPath := os.Getenv(config.EnvConfig)
	if configPath == "" {
		configPath = filepath.Join(home, "config.json")
	}
	configPath, err = filepath.Abs(filepath.Clean(configPath))
	if err != nil {
		return nil, nil, fmt.Errorf("resolve file-identity config path: %w", err)
	}
	weixinRoot := filepath.Join(home, "channels", "weixin")
	gitWorkspaceRoot := cfg.GitWorkspaceRootPath()
	gitWorkspaceRoot, err = filepath.Abs(filepath.Clean(gitWorkspaceRoot))
	if err != nil {
		return nil, nil, fmt.Errorf("resolve file-identity Git workspace root: %w", err)
	}
	checkpointRoot := filepath.Join(
		gitWorkspaceRoot,
		".pr-workspace-implementation",
		"active",
	)
	trees := []string{
		filepath.Join(filepath.Dir(configPath), "legacy-json", "launcher-auth-v1"),
		filepath.Join(home, "legacy-json", "auth-v1"),
		filepath.Join(home, "legacy-json", "model-catalogs-v1"),
		filepath.Join(home, "legacy-json", "tool-adaptation-v1"),
		filepath.Join(home, "legacy-json", "wecom-reqid-v1"),
		filepath.Join(weixinRoot, "sync"),
		filepath.Join(weixinRoot, "context-tokens"),
		filepath.Join(weixinRoot, "legacy-json", "weixin-state-v1"),
		filepath.Join(gitWorkspaceRoot, "legacy-json", "git-workspaces-v1"),
		checkpointRoot,
	}
	exclusions := make([]string, 0, len(workspaces)*9)
	evolutionStateDir := ""
	if cfg != nil {
		evolutionStateDir = cfg.Evolution.StateDir
	}
	for _, workspace := range workspaces {
		trees = append(trees,
			filepath.Join(workspace, "sessions"),
			filepath.Join(workspace, "threads"),
			filepath.Join(workspace, "legacy-json", "sessions-v1"),
			filepath.Join(workspace, "workflow_runs"),
			filepath.Join(workspace, "workflow_validations"),
			filepath.Join(workspace, "workflow_dev"),
			filepath.Join(workspace, "workflow_state"),
			filepath.Join(workspace, "legacy-json", "workflows-v1"),
			filepath.Join(workspace, "state", "legacy-json", "runtime-state-v1"),
			filepath.Join(workspace, "state", "legacy-json", "account-router-v1"),
			filepath.Join(workspace, "cron", "legacy-json", "cron-jobs-v1"),
			filepath.Join(workspace, agentRepositoryReviewStateDir),
			filepath.Join(workspace, agentRepositoryEvalStateDir),
		)
		evolutionRoots, evolutionErr := agentEvolutionFileMutationProtectedRoots(
			workspace,
			evolutionStateDir,
		)
		if evolutionErr != nil {
			return nil, nil, evolutionErr
		}
		if len(evolutionRoots) != 0 {
			evolutionRoot := filepath.Dir(evolutionRoots[0])
			trees = append(trees,
				filepath.Join(evolutionRoot, "profiles"),
				filepath.Join(evolutionRoot, "backups"),
				filepath.Join(evolutionRoot, "legacy-json", "evolution-v1"),
			)
		}
		workspaceArtifacts, workspaceErr := agentProviderArtifactRoots(
			workspace,
			nil,
			"sessions",
			"repository-reviews",
			"repository-evaluations",
		)
		if workspaceErr != nil {
			return nil, nil, workspaceErr
		}
		exclusions = append(exclusions, agentProviderDatabaseExclusions(workspaceArtifacts...)...)
	}
	if cfg != nil {
		localCIRoots, localCIErr := agentLocalCIEvidenceFileMutationProtectedRoots(cfg)
		if localCIErr != nil {
			return nil, nil, localCIErr
		}
		if len(localCIRoots) != 0 {
			evidenceRoot := localCIRoots[0]
			trees = append(trees,
				filepath.Join(evidenceRoot, "cache"),
				filepath.Join(evidenceRoot, "legacy-json", "local-ci-cache-v1"),
			)
		}
	}
	providerCatalog, err := artifacts.New(home, cfg)
	if err != nil {
		return nil, nil, fmt.Errorf("build file-identity provider catalog: %w", err)
	}
	exclusions = append(exclusions, agentProviderDatabaseExclusions(
		providerCatalog.ProtectedRootsForDomains(
			"git-workspace-inventory",
			"pr-workspace-checkpoints",
		)...,
	)...)
	retainedTrees, retainedExclusions := agentFileMutationRetainedCatalogRoots(exactRoots)
	trees = append(trees, retainedTrees...)
	exclusions = append(exclusions, retainedExclusions...)
	return trees, exclusions, nil
}

func agentProviderDatabaseExclusions(roots ...string) []string {
	exclusions := make([]string, 0, len(roots))
	for _, root := range roots {
		base := strings.ToLower(filepath.Base(root))
		if strings.HasSuffix(base, ".db") || strings.HasSuffix(base, ".db-wal") ||
			strings.HasSuffix(base, ".db-shm") || strings.HasSuffix(base, ".db-journal") {
			exclusions = append(exclusions, filepath.Clean(root))
		}
	}
	return normalizeProtectedRoots(exclusions)
}

// agentFileMutationWorkspacesFromProtectedRoots preserves the workspace set
// across reloads. The loop passes every prior generation root into the next
// registry, so old session, workflow, router, review, and evaluation trees
// remain part of the physical-identity snapshot after their config entry is
// removed or redirected.
func agentFileMutationWorkspacesFromProtectedRoots(roots []string) []string {
	workspaces := make([]string, 0)
	for _, root := range roots {
		cleaned := filepath.Clean(root)
		base := strings.ToLower(filepath.Base(cleaned))
		var workspace string
		switch base {
		case "sessions", "threads", "cron", "state", "workflow_runs",
			"workflow_validations", "workflow_dev", "workflow_state",
			agentRepositoryReviewStateDir, agentRepositoryEvalStateDir:
			workspace = filepath.Dir(cleaned)
		case "sessions.db", "jobs.db", "runtime.db", "account-router.db",
			"workflows.db", "repository-reviews.db", "evaluations.db":
			workspace = filepath.Dir(filepath.Dir(cleaned))
			if base == "runtime.db" || base == "account-router.db" || base == "workflows.db" {
				workspace = filepath.Dir(filepath.Dir(cleaned))
			}
		}
		if workspace != "" {
			workspaces = append(workspaces, workspace)
		}
	}
	return workspaces
}

// agentFileMutationRetainedCatalogRoots selects only mutable legacy/runtime
// subtrees from cumulative protected roots. In particular it does not scan the
// immutable local-CI evidence namespace, which can contain large content-
// addressed bodies; its cache and archive children are selected separately.
func agentFileMutationRetainedCatalogRoots(roots []string) ([]string, []string) {
	trees := make([]string, 0)
	exclusions := make([]string, 0)
	for _, root := range roots {
		cleaned := filepath.Clean(root)
		base := strings.ToLower(filepath.Base(cleaned))
		if base == "active" &&
			strings.EqualFold(filepath.Base(filepath.Dir(cleaned)), ".pr-workspace-implementation") {
			trees = append(trees, cleaned)
		}
		switch base {
		case "legacy-json", "sessions", "threads", "workflow_runs",
			"workflow_validations", "workflow_dev", "workflow_state",
			"profiles", "backups", "cache", "sync", "context-tokens",
			agentRepositoryReviewStateDir, agentRepositoryEvalStateDir:
			trees = append(trees, cleaned)
		}
		if strings.HasSuffix(base, ".db") || strings.HasSuffix(base, ".db-wal") ||
			strings.HasSuffix(base, ".db-shm") {
			exclusions = append(exclusions, cleaned)
		}
	}
	return trees, exclusions
}

func agentCheckpointRetainedStateSnapshot(
	cfg *config.Config,
) (agentCheckpointStateSnapshot, error) {
	gitWorkspaceRoot := cfg.GitWorkspaceRootPath()
	gitWorkspaceRoot, err := filepath.Abs(filepath.Clean(gitWorkspaceRoot))
	if err != nil {
		return nil, errors.New("PR workspace checkpoint root cannot be resolved")
	}
	checkpointRoot := filepath.Join(
		gitWorkspaceRoot,
		".pr-workspace-implementation",
		"active",
	)
	archiveRoot := filepath.Join(checkpointRoot, "legacy-json")
	return agentCheckpointRetainedStateSnapshotBounded(
		checkpointRoot,
		archiveRoot,
		agentCheckpointMaximumSources+4,
		agentCheckpointMaximumSources,
		agentCheckpointArchiveEntries,
		agentCheckpointArchiveDepth,
	)
}

func equalAgentCheckpointStateSnapshots(
	left agentCheckpointStateSnapshot,
	right agentCheckpointStateSnapshot,
) bool {
	if len(left) != len(right) {
		return false
	}
	for path, before := range left {
		after, exists := right[path]
		if !exists || before.missing != after.missing || before.protect != after.protect {
			return false
		}
		if before.missing {
			continue
		}
		if before.info == nil || after.info == nil ||
			before.info.Mode() != after.info.Mode() ||
			before.info.Size() != after.info.Size() ||
			!before.info.ModTime().Equal(after.info.ModTime()) ||
			!os.SameFile(before.info, after.info) {
			return false
		}
	}
	return true
}

type agentLegacySidecarSnapshot map[string]os.FileInfo

// Test seam proving that account-router sidecar namespace changes around the
// shared catalog fail generation construction instead of producing a partial
// identity set.
var agentFileMutationIdentityBetweenSidecarSnapshots func()

func agentAccountRouterLegacySidecarSnapshot(
	workspaces []string,
	maximumDirectoryEntries int,
	maximumSidecars int,
) (agentLegacySidecarSnapshot, error) {
	if maximumDirectoryEntries < 1 || maximumSidecars < 1 {
		return nil, errors.New("account-router legacy sidecar limits are invalid")
	}
	snapshot := make(agentLegacySidecarSnapshot)
	totalSidecars := 0
	totalEntries := 0
	for _, workspace := range workspaces {
		before, err := os.Lstat(workspace)
		switch {
		case os.IsNotExist(err):
			continue
		case err != nil:
			return nil, errors.New("account-router legacy workspace cannot be inspected")
		case before.Mode()&os.ModeSymlink != 0 || !before.IsDir():
			return nil, errors.New("account-router legacy workspace is unsafe")
		}
		root, err := os.OpenRoot(workspace)
		if err != nil {
			return nil, errors.New("account-router legacy workspace cannot be opened")
		}
		opened, openedErr := root.Stat(".")
		if openedErr != nil || !stableAgentLegacyDirectory(before, opened) {
			_ = root.Close()
			return nil, errors.New("account-router legacy workspace changed while opening")
		}
		directory, err := root.Open(".")
		if err != nil {
			_ = root.Close()
			return nil, errors.New("account-router legacy workspace cannot be enumerated")
		}
		for {
			entries, readErr := directory.ReadDir(agentFileIdentityDirectoryReadBatch)
			for _, entry := range entries {
				totalEntries++
				if totalEntries > maximumDirectoryEntries {
					_ = directory.Close()
					_ = root.Close()
					return nil, errors.New("account-router legacy directory entry limit exceeded")
				}
				if !agentAccountRouterLegacySidecarName(entry.Name()) {
					continue
				}
				totalSidecars++
				if totalSidecars > maximumSidecars {
					_ = directory.Close()
					_ = root.Close()
					return nil, errors.New("account-router legacy sidecar limit exceeded")
				}
				info, infoErr := root.Lstat(entry.Name())
				if infoErr != nil || info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
					_ = directory.Close()
					_ = root.Close()
					return nil, errors.New("account-router legacy sidecar is unsafe")
				}
				snapshot[filepath.Join(workspace, entry.Name())] = info
			}
			if readErr != nil {
				if !errors.Is(readErr, io.EOF) {
					_ = directory.Close()
					_ = root.Close()
					return nil, errors.New("account-router legacy workspace cannot be enumerated")
				}
				break
			}
		}
		directoryCloseErr := directory.Close()
		after, afterErr := root.Stat(".")
		current, currentErr := os.Lstat(workspace)
		rootCloseErr := root.Close()
		if directoryCloseErr != nil || rootCloseErr != nil ||
			afterErr != nil || currentErr != nil ||
			!stableAgentLegacyDirectory(before, after) ||
			!stableAgentLegacyDirectory(before, current) {
			return nil, errors.New("account-router legacy workspace changed during enumeration")
		}
	}
	return snapshot, nil
}

func stableAgentLegacyDirectory(before, after os.FileInfo) bool {
	return before != nil && after != nil && before.IsDir() && after.IsDir() &&
		before.Mode() == after.Mode() && os.SameFile(before, after)
}

func equalAgentAccountRouterLegacySidecarSnapshots(
	left agentLegacySidecarSnapshot,
	right agentLegacySidecarSnapshot,
) bool {
	if len(left) != len(right) {
		return false
	}
	for path, leftInfo := range left {
		rightInfo := right[path]
		if rightInfo == nil || !os.SameFile(leftInfo, rightInfo) ||
			leftInfo.Mode() != rightInfo.Mode() || leftInfo.Size() != rightInfo.Size() ||
			!leftInfo.ModTime().Equal(rightInfo.ModTime()) {
			return false
		}
	}
	return true
}

type agentFileMutationIdentityGeneration struct {
	mu                         sync.Mutex
	sharedCatalog              *tools.FileIdentityCatalog
	preparedPolicy             *tools.PreparedFileMutationPolicy
	preparedApplyPatchRoots    *tools.PreparedApplyPatchVolatileRoots
	fileMutationProtectedRoots []string
}

func newAgentFileMutationIdentityGeneration(
	workspaces []string,
	cfg *config.Config,
	exactRoots []string,
	protectedRoots []string,
) (*agentFileMutationIdentityGeneration, error) {
	catalog, err := agentFileMutationIdentityCatalogForWorkspaces(workspaces, cfg, exactRoots)
	if err != nil {
		return nil, err
	}
	prefixWorkspaces := append(
		append([]string(nil), workspaces...),
		agentFileMutationWorkspacesFromProtectedRoots(protectedRoots)...,
	)
	prefixes, err := agentAccountRouterFileMutationProtectedPrefixes(prefixWorkspaces)
	if err != nil {
		return nil, err
	}
	preparedPolicy, err := tools.NewPreparedFileMutationPolicy("", tools.FileMutationPolicy{
		ProtectedRoots:           protectedRoots,
		ProtectedSiblingPrefixes: prefixes,
		ProtectedIdentities:      catalog,
	})
	if err != nil {
		return nil, err
	}
	preparedApplyPatchRoots, err := tools.NewPreparedApplyPatchVolatileRoots(
		"",
		protectedRoots,
	)
	if err != nil {
		return nil, err
	}
	return &agentFileMutationIdentityGeneration{
		sharedCatalog:              catalog,
		preparedPolicy:             preparedPolicy,
		preparedApplyPatchRoots:    preparedApplyPatchRoots,
		fileMutationProtectedRoots: append([]string(nil), protectedRoots...),
	}, nil
}

func (generation *agentFileMutationIdentityGeneration) catalog(
	workspace string,
	cfg *config.Config,
	exactRoots []string,
) (*tools.FileIdentityCatalog, error) {
	if generation == nil {
		return agentFileMutationIdentityCatalog(workspace, cfg, exactRoots)
	}
	generation.mu.Lock()
	defer generation.mu.Unlock()
	if generation.sharedCatalog != nil {
		return generation.sharedCatalog, nil
	}
	catalog, err := agentFileMutationIdentityCatalog(workspace, cfg, exactRoots)
	if err != nil {
		return nil, err
	}
	generation.sharedCatalog = catalog
	return catalog, nil
}
