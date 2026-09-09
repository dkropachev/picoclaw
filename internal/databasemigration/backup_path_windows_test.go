//go:build windows

package databasemigration

import "testing"

func TestWindowsSafeRelativePathsApplyPlatformGrammar(t *testing.T) {
	for _, path := range []string{
		`safe\file:stream`, `safe\PROGRA~1\file`, "safe\\nul\x00file", `\rooted`, `/rooted`,
	} {
		if safeBackupRelative(path) {
			t.Errorf("unsafe relative path accepted: %q", path)
		}
		if err := newBackupBudget().reservePreparedLegacyPath(path); err == nil {
			t.Errorf("unsafe prepared legacy path accepted: %q", path)
		}
	}
	if !safeBackupRelative(`safe\release~candidate\file`) {
		t.Fatal("safe relative Windows path rejected")
	}
}
