package ledger

import (
	"math"
	"strings"
	"sync"
)

type transferRecord struct {
	from   string
	to     string
	amount int64
}

type Ledger struct {
	mu       sync.RWMutex
	balances map[string]int64
	requests map[string]transferRecord
}

func New(initial map[string]int64) *Ledger {
	balances := make(map[string]int64, len(initial))
	for account, balance := range initial {
		balances[account] = balance
	}
	return &Ledger{balances: balances, requests: make(map[string]transferRecord)}
}

func (l *Ledger) Balance(account string) (int64, bool) {
	if l == nil {
		return 0, false
	}
	l.mu.RLock()
	defer l.mu.RUnlock()
	balance, found := l.balances[account]
	return balance, found
}

func (l *Ledger) Snapshot() map[string]int64 {
	if l == nil {
		return nil
	}
	l.mu.RLock()
	defer l.mu.RUnlock()
	result := make(map[string]int64, len(l.balances))
	for account, balance := range l.balances {
		result[account] = balance
	}
	return result
}

func (l *Ledger) Transfer(requestID, from, to string, amount int64) error {
	if strings.TrimSpace(requestID) == "" {
		return ErrInvalidRequest
	}
	// Mutant: Transfer reads and mutates both maps without taking l.mu.
	if record, exists := l.requests[requestID]; exists {
		if record.from == from && record.to == to && record.amount == amount {
			return nil
		}
		return ErrRequestConflict
	}
	if strings.TrimSpace(from) == "" || strings.TrimSpace(to) == "" || from == to {
		return ErrInvalidRequest
	}
	if amount <= 0 {
		return ErrInvalidAmount
	}
	fromBalance, fromExists := l.balances[from]
	toBalance, toExists := l.balances[to]
	if !fromExists || !toExists {
		return ErrAccountNotFound
	}
	if fromBalance < amount {
		return ErrInsufficientFunds
	}
	if toBalance > math.MaxInt64-amount {
		return ErrBalanceOverflow
	}
	l.balances[from] = fromBalance - amount
	l.balances[to] = toBalance + amount
	l.requests[requestID] = transferRecord{from: from, to: to, amount: amount}
	return nil
}
