//go:build !unix && !windows

package sqliteprovider

import "os"

func replaceStagedGeneration(stage, target string) error { return os.Rename(stage, target) }

func syncStagedMigrationDirectory(string) error { return nil }
