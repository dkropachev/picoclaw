package ledger

import (
	"reflect"
	"testing"
)

func TestNewCopiesInputAndSnapshot(t *testing.T) {
	initial := map[string]int64{"alice": 10, "bob": 2}
	value := New(initial)
	initial["alice"] = 99
	if balance, ok := value.Balance("alice"); !ok || balance != 10 {
		t.Fatalf("Balance(alice) = %d, %v", balance, ok)
	}
	if got := value.Snapshot(); !reflect.DeepEqual(got, map[string]int64{"alice": 10, "bob": 2}) {
		t.Fatalf("Snapshot() = %#v", got)
	}
}

func TestNilLedgerReadsAreSafe(t *testing.T) {
	var value *Ledger
	if balance, ok := value.Balance("alice"); ok || balance != 0 {
		t.Fatalf("nil Balance() = %d, %v", balance, ok)
	}
	if value.Snapshot() != nil {
		t.Fatal("nil Snapshot() was non-nil")
	}
}
