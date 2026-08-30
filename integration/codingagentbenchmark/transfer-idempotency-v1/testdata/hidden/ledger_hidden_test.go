package ledger_test

import (
	"errors"
	"math"
	"reflect"
	"strconv"
	"sync"
	"testing"

	"benchmark.local/transferidempotency/ledger"
)

func requireBalances(t *testing.T, value *ledger.Ledger, want map[string]int64) {
	t.Helper()
	if got := value.Snapshot(); !reflect.DeepEqual(got, want) {
		t.Fatalf("balances = %#v, want %#v", got, want)
	}
}

func requireTransferError(
	t *testing.T,
	value *ledger.Ledger,
	requestID, from, to string,
	amount int64,
	want error,
) {
	t.Helper()
	before := value.Snapshot()
	if err := value.Transfer(requestID, from, to, amount); !errors.Is(err, want) {
		t.Fatalf("Transfer(%q, %q, %q, %d) error = %v, want %v", requestID, from, to, amount, err, want)
	}
	requireBalances(t, value, before)
}

func TestHiddenValidationAndFailureAtomicity(t *testing.T) {
	tests := []struct {
		name string
		id   string
		from string
		to   string
		amt  int64
		want error
	}{
		{name: "blank id", id: " ", from: "a", to: "b", amt: 1, want: ledger.ErrInvalidRequest},
		{name: "unicode blank id", id: "\u2003", from: "a", to: "b", amt: 1, want: ledger.ErrInvalidRequest},
		{name: "blank source", id: "x", from: "", to: "b", amt: 1, want: ledger.ErrInvalidRequest},
		{name: "blank target", id: "x", from: "a", to: "\t", amt: 1, want: ledger.ErrInvalidRequest},
		{name: "same account", id: "x", from: "a", to: "a", amt: 1, want: ledger.ErrInvalidRequest},
		{name: "invalid request precedes amount", id: "x", from: " ", to: "b", amt: 0, want: ledger.ErrInvalidRequest},
		{name: "zero", id: "x", from: "a", to: "b", amt: 0, want: ledger.ErrInvalidAmount},
		{name: "negative", id: "x", from: "a", to: "b", amt: -1, want: ledger.ErrInvalidAmount},
		{name: "missing source", id: "x", from: "missing", to: "b", amt: 1, want: ledger.ErrAccountNotFound},
		{name: "missing target", id: "x", from: "a", to: "missing", amt: 1, want: ledger.ErrAccountNotFound},
		{name: "insufficient", id: "x", from: "a", to: "b", amt: 11, want: ledger.ErrInsufficientFunds},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			value := ledger.New(map[string]int64{"a": 10, "b": 2})
			requireTransferError(t, value, test.id, test.from, test.to, test.amt, test.want)
		})
	}
}

func TestHiddenReplayPrecedenceAndConflict(t *testing.T) {
	value := ledger.New(map[string]int64{"a": 2, "b": math.MaxInt64 - 1, "c": 1})
	if err := value.Transfer("settled", "a", "b", 1); err != nil {
		t.Fatalf("initial transfer error = %v", err)
	}
	if err := value.Transfer("drain", "a", "c", 1); err != nil {
		t.Fatalf("balance-changing transfer error = %v", err)
	}
	want := map[string]int64{"a": 0, "b": math.MaxInt64, "c": 2}
	requireBalances(t, value, want)

	// Exact replay wins even though revalidating the current balances would now
	// report both insufficient funds and destination overflow.
	if err := value.Transfer("settled", "a", "b", 1); err != nil {
		t.Fatalf("exact replay after balance changes error = %v", err)
	}
	requireBalances(t, value, want)

	conflicts := []struct {
		from   string
		to     string
		amount int64
	}{
		{from: "a", to: "b", amount: 2},
		{from: "", to: "b", amount: 1},
		{from: "a", to: "a", amount: 0},
		{from: "missing", to: "also-missing", amount: -1},
	}
	for _, conflict := range conflicts {
		err := value.Transfer("settled", conflict.from, conflict.to, conflict.amount)
		if !errors.Is(err, ledger.ErrRequestConflict) {
			t.Fatalf("existing ID with tuple (%q, %q, %d) error = %v, want conflict", conflict.from, conflict.to, conflict.amount, err)
		}
		requireBalances(t, value, want)
	}
}

func TestHiddenEveryFailedTransferLeavesIDReusable(t *testing.T) {
	tests := []struct {
		name string
		from string
		to   string
		amt  int64
		want error
	}{
		{name: "invalid source", from: " ", to: "b", amt: 1, want: ledger.ErrInvalidRequest},
		{name: "invalid amount", from: "a", to: "b", amt: 0, want: ledger.ErrInvalidAmount},
		{name: "missing account", from: "missing", to: "b", amt: 1, want: ledger.ErrAccountNotFound},
		{name: "insufficient funds", from: "a", to: "b", amt: 11, want: ledger.ErrInsufficientFunds},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			value := ledger.New(map[string]int64{"a": 10, "b": 0, "c": 5})
			requireTransferError(t, value, "reusable", test.from, test.to, test.amt, test.want)
			if err := value.Transfer("reusable", "c", "b", 2); err != nil {
				t.Fatalf("failed request ID was consumed: %v", err)
			}
			requireBalances(t, value, map[string]int64{"a": 10, "b": 2, "c": 3})
		})
	}

	value := ledger.New(map[string]int64{"a": 3, "b": math.MaxInt64 - 1, "c": 4})
	requireTransferError(t, value, "reusable", "a", "b", 2, ledger.ErrBalanceOverflow)
	if err := value.Transfer("reusable", "c", "a", 1); err != nil {
		t.Fatalf("overflow consumed request ID: %v", err)
	}
	requireBalances(t, value, map[string]int64{"a": 4, "b": math.MaxInt64 - 1, "c": 3})
}

func TestHiddenOverflowPreservesBalances(t *testing.T) {
	value := ledger.New(map[string]int64{"a": 3, "b": math.MaxInt64 - 1})
	requireTransferError(t, value, "overflow", "a", "b", 2, ledger.ErrBalanceOverflow)
}

func TestHiddenConcurrentDuplicateConflictAndIndependentTransfers(t *testing.T) {
	value := ledger.New(map[string]int64{"a": 1000, "b": 0, "c": 1000, "d": 0})
	if err := value.Transfer("duplicate", "a", "b", 7); err != nil {
		t.Fatal(err)
	}
	want := map[string]int64{"a": 993, "b": 7, "c": 900, "d": 100}
	start := make(chan struct{})
	errorsOut := make(chan error, 300)
	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(3)
		go func() {
			defer wg.Done()
			<-start
			errorsOut <- value.Transfer("duplicate", "a", "b", 7)
		}()
		go func() {
			defer wg.Done()
			<-start
			err := value.Transfer("duplicate", "a", "b", 8)
			if !errors.Is(err, ledger.ErrRequestConflict) {
				errorsOut <- errors.New("conflicting concurrent replay did not return ErrRequestConflict")
				return
			}
			errorsOut <- nil
		}()
		go func(index int) {
			defer wg.Done()
			<-start
			errorsOut <- value.Transfer("independent-"+strconv.Itoa(index), "c", "d", 1)
		}(i)
	}
	close(start)
	wg.Wait()
	close(errorsOut)
	for err := range errorsOut {
		if err != nil {
			t.Fatalf("concurrent Transfer() error = %v", err)
		}
	}
	requireBalances(t, value, want)
}

func TestHiddenConcurrentOverspendIsAtomicAndFailedIDsRemainReusable(t *testing.T) {
	value := ledger.New(map[string]int64{"a": 64, "b": 0, "c": 10, "d": 0})
	type outcome struct {
		id  string
		err error
	}
	start := make(chan struct{})
	outcomes := make(chan outcome, 128)
	var wg sync.WaitGroup
	for i := 0; i < 128; i++ {
		id := "spend-" + strconv.Itoa(i)
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			outcomes <- outcome{id: id, err: value.Transfer(id, "a", "b", 1)}
		}()
	}
	close(start)
	wg.Wait()
	close(outcomes)

	succeeded := 0
	failedID := ""
	for result := range outcomes {
		switch {
		case result.err == nil:
			succeeded++
		case errors.Is(result.err, ledger.ErrInsufficientFunds):
			if failedID == "" {
				failedID = result.id
			}
		default:
			t.Fatalf("concurrent overspend error = %v", result.err)
		}
	}
	if succeeded != 64 || failedID == "" {
		t.Fatalf("successful transfers = %d, failed ID = %q; want 64 and a reusable failure", succeeded, failedID)
	}
	requireBalances(t, value, map[string]int64{"a": 0, "b": 64, "c": 10, "d": 0})
	if err := value.Transfer(failedID, "c", "d", 1); err != nil {
		t.Fatalf("concurrently failed request ID was consumed: %v", err)
	}
	requireBalances(t, value, map[string]int64{"a": 0, "b": 64, "c": 9, "d": 1})
}
