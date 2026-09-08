//go:build windows

package fileidentity

import "testing"

func TestWindowsIdentityPathsUseExtendedNamespace(t *testing.T) {
	osPath, syscallPath, err := windowsIdentityPaths(`C:\database\store.db`)
	if err != nil || osPath != `C:\database\store.db` || syscallPath != `\\?\C:\database\store.db` {
		t.Fatalf("windowsIdentityPaths(local) = %q, %q, %v", osPath, syscallPath, err)
	}
	osPath, syscallPath, err = windowsIdentityPaths(`\\server\share\database\store.db`)
	if err != nil || osPath != `\\server\share\database\store.db` ||
		syscallPath != `\\?\UNC\server\share\database\store.db` {
		t.Fatalf("windowsIdentityPaths(UNC) = %q, %q, %v", osPath, syscallPath, err)
	}
	for _, path := range []string{`\\?\C:\store.db`, `\\.\C:\store.db`, `\??\C:\store.db`} {
		if _, _, err := windowsIdentityPaths(path); err == nil {
			t.Errorf("windowsIdentityPaths(%q) succeeded", path)
		}
	}
}
