//go:build darwin

package databasereadiness

import "testing"

func TestDarwinGenerationPathKeyPreservesCase(t *testing.T) {
	if generationPathKey("/tmp/Store.db") == generationPathKey("/tmp/store.db") {
		t.Fatal("Darwin lexical exclusion key collapsed case-sensitive paths")
	}
}
