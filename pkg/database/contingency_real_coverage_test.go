package database

import (
	"strings"
	"testing"
)

type oversizedExponentJSON struct{}

func (oversizedExponentJSON) MarshalJSON() ([]byte, error) {
	return []byte("1e2147483648"), nil
}

func TestCanonicalMarshalRejectsRealOversizedExponent(t *testing.T) {
	if _, err := MarshalCanonical(map[string]any{"number": oversizedExponentJSON{}}); err == nil ||
		!strings.Contains(err.Error(), "exponent") {
		t.Fatalf("oversized canonical exponent error = %v", err)
	}
}
