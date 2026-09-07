//go:build darwin

package storecatalog

import "testing"

func TestDarwinResolvedPathEqualityRemainsExact(t *testing.T) {
	if catalogPathKey("/runtime/A") != catalogPathKey("/runtime/a") {
		t.Fatal("Darwin collision key did not conservatively fold case")
	}
	if sameCatalogResolvedPath("/runtime/A", "/runtime/a") {
		t.Fatal("Darwin resolved-path equality hid a case-only symlink target")
	}
}
