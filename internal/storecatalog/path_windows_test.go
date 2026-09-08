//go:build windows

package storecatalog

import "testing"

func TestWindowsCatalogPathValidationRejectsAliases(t *testing.T) {
	for _, path := range []string{
		`C:\runtime\auth.db:$DATA`,
		`C:\runtime\AUTH~1.DB`,
		`C:\runtime\auth.db.`,
		`C:\runtime\CON`,
		`C:\runtime\COM¹.log`,
		`C:\runtime\LPT³`,
		`\\?\C:\runtime\auth.db`,
		`\\.\C:\runtime\auth.db`,
		`\??\C:\runtime\auth.db`,
	} {
		if err := validateCatalogPlatformPath(path); err == nil {
			t.Fatalf("ambiguous Windows path %q was accepted", path)
		}
	}
	if err := validateCatalogPlatformPath(`C:\runtime\auth.db`); err != nil {
		t.Fatalf("ordinary Windows path rejected: %v", err)
	}
	if catalogPathKey(`C:\runtime\AUTH.db`) != catalogPathKey(`c:\runtime\auth.DB`) {
		t.Fatal("Windows path key is case-sensitive")
	}
	if !sameCatalogResolvedPath(`C:\runtime\AUTH.db`, `c:\runtime\auth.DB`) {
		t.Fatal("Windows resolved-path equality is case-sensitive")
	}
}
