package ledger

import "errors"

var (
	ErrNotImplemented    = errors.New("transfer is not implemented")
	ErrInvalidRequest    = errors.New("invalid transfer request")
	ErrInvalidAmount     = errors.New("invalid transfer amount")
	ErrAccountNotFound   = errors.New("ledger account not found")
	ErrInsufficientFunds = errors.New("insufficient funds")
	ErrBalanceOverflow   = errors.New("destination balance overflow")
	ErrRequestConflict   = errors.New("transfer request ID conflict")
)
