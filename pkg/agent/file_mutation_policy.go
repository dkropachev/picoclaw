package agent

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/sipeed/picoclaw/pkg/config"
)

const agentLauncherAuthDatabaseName = "launcher-auth.db"

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

	database := filepath.Join(home, agentLauncherAuthDatabaseName)
	archiveRoot := filepath.Join(filepath.Dir(configPath), "legacy-json")
	return []string{
		database,
		database + "-wal",
		database + "-shm",
		// Protect the archive namespace, rather than only the version leaf, so
		// a model cannot pre-create legacy-json as a file and poison migration.
		archiveRoot,
		// The exact retained credential source also catches hardlink aliases
		// whose lexical path lies outside the archive namespace.
		filepath.Join(
			archiveRoot,
			"launcher-auth-v1",
			"launcher-config.json",
		),
	}, nil
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
