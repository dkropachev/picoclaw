//go:build windows

package fstools

import "testing"

func TestFileMutationPlatformPathRejectsWindowsAliases(t *testing.T) {
	for _, path := range []string{
		`C:\runtime\launcher-auth.db:$DATA`,
		`C:\runtime\LAUNCH~1.DB`,
		`C:\runtime\launcher-auth.db.`,
		`C:\runtime\CON`,
	} {
		if err := validateFileMutationPlatformPath(path); err == nil {
			t.Fatalf("ambiguous Windows path %q was accepted", path)
		}
	}
	if err := validateFileMutationPlatformPath(
		`C:\runtime\launcher-auth.db`,
	); err != nil {
		t.Fatalf("ordinary Windows path rejected: %v", err)
	}
}
