package fileutil

import (
	"os"
	"runtime"
	"testing"
)

func TestSecurePrivatePathsPropagateReadOnlyFilesystemFailures(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("requires read-only procfs nodes")
	}
	for _, test := range []struct {
		name string
		path string
		call func(string) (os.FileInfo, error)
	}{
		{name: "directory", path: "/proc/self/fd", call: SecurePrivateDirectory},
		{name: "file", path: "/proc/self/status", call: SecurePrivateFile},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := os.Lstat(test.path); err != nil {
				t.Skipf("procfs node unavailable: %v", err)
			}
			if info, err := test.call(test.path); err == nil || info != nil {
				t.Fatalf("secure read-only procfs path = %#v, %v", info, err)
			}
		})
	}
}
