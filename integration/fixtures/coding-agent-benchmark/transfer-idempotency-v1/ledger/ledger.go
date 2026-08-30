package ledger

import "sync"

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
	return ErrNotImplemented
}
