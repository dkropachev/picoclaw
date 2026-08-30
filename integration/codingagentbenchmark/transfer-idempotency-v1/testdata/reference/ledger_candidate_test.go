package ledger_test

import (
	"errors"
	"math"
	"sync"
	"testing"

	"benchmark.local/transferidempotency/ledger"
)

func TestTransferCandidateReplayAndConflictPrecedence(t *testing.T) {
	value := ledger.New(map[string]int64{"a": 2, "b": math.MaxInt64 - 1, "c": 0})
	if err := value.Transfer("id", "a", "b", 1); err != nil {
		t.Fatal(err)
	}
	if err := value.Transfer("drain", "a", "c", 1); err != nil {
		t.Fatal(err)
	}
	if err := value.Transfer("id", "a", "b", 1); err != nil {
		t.Fatalf("exact replay error = %v", err)
	}
	if err := value.Transfer("id", "", "b", 0); !errors.Is(err, ledger.ErrRequestConflict) {
		t.Fatalf("conflicting replay error = %v", err)
	}
	if got := value.Snapshot(); got["a"] != 0 || got["b"] != math.MaxInt64 || got["c"] != 1 {
		t.Fatalf("balances after replay/conflict = %#v", got)
	}
}

func TestTransferCandidateFailedIDReuseAndOverflow(t *testing.T) {
	value := ledger.New(map[string]int64{"a": 3, "b": math.MaxInt64 - 1, "c": 4})
	if err := value.Transfer("reuse", "a", "b", 2); !errors.Is(err, ledger.ErrBalanceOverflow) {
		t.Fatalf("overflow error = %v", err)
	}
	if err := value.Transfer("reuse", "c", "a", 1); err != nil {
		t.Fatalf("failed ID reuse error = %v", err)
	}
	if got := value.Snapshot(); got["a"] != 4 || got["b"] != math.MaxInt64-1 || got["c"] != 3 {
		t.Fatalf("balances after failed ID reuse = %#v", got)
	}
}

func TestTransferCandidateConcurrentDuplicates(t *testing.T) {
	value := ledger.New(map[string]int64{"a": 1000, "b": 0})
	start := make(chan struct{})
	errorsOut := make(chan error, 64)
	var wg sync.WaitGroup
	for range 64 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			errorsOut <- value.Transfer("same", "a", "b", 7)
		}()
	}
	close(start)
	wg.Wait()
	close(errorsOut)
	for err := range errorsOut {
		if err != nil {
			t.Fatal(err)
		}
	}
	if got := value.Snapshot(); got["a"] != 993 || got["b"] != 7 {
		t.Fatalf("concurrent balances = %#v", got)
	}
}
