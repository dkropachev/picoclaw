package agent

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/sipeed/picoclaw/pkg/config"
)

const (
	agentLauncherAuthDatabaseName = "launcher-auth.db"
	agentAuthDatabaseName         = "auth.db"
	agentModelCatalogDatabaseName = "model-catalogs.db"
	agentToolAdaptationDatabase   = "tool-adaptation.db"
)

// agentRuntimeFileMutationProtectedRoots freezes model-facing filesystem
// exclusions for mutable runtime state. Keep this component-oriented so later
// SQLite stores can extend the same policy without making active configuration
// or repository source trees read-only.
func agentRuntimeFileMutationProtectedRoots(configPath string) ([]string, error) {
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

	protected := make([]string, 0, 21)
	for _, databaseName := range []string{
		agentLauncherAuthDatabaseName,
		agentAuthDatabaseName,
		agentModelCatalogDatabaseName,
		agentToolAdaptationDatabase,
	} {
		database := filepath.Join(home, databaseName)
		protected = append(protected, database, database+"-wal", database+"-shm")
	}
	protected = append(protected, filepath.Join(home, agentAuthDatabaseName+".locks"))

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
	return protected, nil
}

func mustAgentRuntimeFileMutationProtectedRoots(configPath string) []string {
	roots, err := agentRuntimeFileMutationProtectedRoots(configPath)
	if err != nil {
		panic(fmt.Sprintf("build file-mutation policy: %v", err))
	}
	return roots
}

func cloneAgentRuntimeFileMutationProtectedRoots(roots []string) []string {
	return append([]string(nil), roots...)
}
