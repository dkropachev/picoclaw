//go:build unix

package sqliteprovider

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

func TestSecureUnixProviderInfoRejectsForeignOwnerAndPropagatesChmod(t *testing.T) {
	path := filepath.Join(t.TempDir(), "store.db")
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		t.Skip("Unix stat identity unavailable")
	}
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	if err := secureUnixProviderHandle(
		path, false, info, file, stat.Uid+1,
	); err == nil {
		t.Fatal("foreign owner was accepted")
	}
	if err := secureUnixProviderHandle(path, false, info, file, stat.Uid); err != nil {
		t.Fatalf("secure handle = %v", err)
	}
}
