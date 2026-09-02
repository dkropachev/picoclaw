//go:build windows

package sqliteprovider

import (
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/windows"
)

func TestSecureProviderPathsInstallCurrentUserProtectedDACL(t *testing.T) {
	root := t.TempDir()
	directory := filepath.Join(root, "store")
	if err := os.Mkdir(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := secureProviderDirectory(directory); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(directory, "store.db")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := secureProviderFile(file); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{directory, file} {
		descriptor, err := windows.GetNamedSecurityInfo(
			path,
			windows.SE_FILE_OBJECT,
			windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION,
		)
		if err != nil {
			t.Fatal(err)
		}
		control, _, err := descriptor.Control()
		if err != nil || control&windows.SE_DACL_PROTECTED == 0 {
			t.Fatalf("path %q has unprotected DACL: control=%v err=%v", path, control, err)
		}
		dacl, _, err := descriptor.DACL()
		if err != nil || dacl == nil || dacl.AceCount != 1 {
			t.Fatalf("path %q owner DACL = %#v err=%v", path, dacl, err)
		}
	}
}
