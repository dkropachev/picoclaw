package sqliteprovider

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"sync"

	moderncsqlite "modernc.org/sqlite"

	dblayer "github.com/sipeed/picoclaw/pkg/database"
)

var errTransactionBoundary = dblayer.NewError(
	dblayer.CodeIntegrity,
	"database transaction boundary was violated",
)

type transactionBoundaryState uint8

const (
	transactionBoundaryInstalled transactionBoundaryState = iota + 1
	transactionBoundaryBeginning
	transactionBoundaryStarted
	transactionBoundaryCommitArmed
	transactionBoundaryCommitObserved
	transactionBoundaryCommitted
	transactionBoundaryClosed
)

// TransactionBoundary is an opaque, connection-local capability that denies
// callback-controlled COMMIT and records callback-controlled ROLLBACK. The
// owner must mark a successful BEGIN, then either Commit or Close the boundary
// before releasing its sql.Conn.
type TransactionBoundary struct {
	operation sync.Mutex
	stateMu   sync.Mutex
	conn      *sql.Conn
	state     transactionBoundaryState
	commits   uint8
	rollbacks uint8
	violation bool
	closeErr  error
}

type transactionHookRegistrar interface {
	RegisterCommitHook(callback moderncsqlite.CommitHookFn)
	RegisterRollbackHook(callback moderncsqlite.RollbackHookFn)
}

// NewTransactionBoundary installs commit and rollback hooks before BEGIN. The
// concrete driver connection is used only inside sql.Conn.Raw callbacks.
func NewTransactionBoundary(conn *sql.Conn) (*TransactionBoundary, error) {
	if conn == nil {
		return nil, errors.New("SQLite transaction boundary connection is unavailable")
	}
	boundary := &TransactionBoundary{
		conn:  conn,
		state: transactionBoundaryInstalled,
	}
	if err := conn.Raw(func(driverConn any) error {
		registrar, ok := driverConn.(transactionHookRegistrar)
		if !ok {
			return errors.New("shipped SQLite transaction hooks are unavailable")
		}
		registrar.RegisterCommitHook(boundary.commitHook)
		registrar.RegisterRollbackHook(boundary.rollbackHook)
		return nil
	}); err != nil {
		boundary.conn = nil
		boundary.state = transactionBoundaryClosed
		boundary.closeErr = err
		return nil, err
	}
	return boundary, nil
}

// BeginAttempted marks the interval in which BEGIN may have reached SQLite but
// its caller has not yet observed a successful result. Close treats this state
// as ambiguous and discards the physical connection after hook removal and a
// best-effort raw rollback.
func (boundary *TransactionBoundary) BeginAttempted() error {
	if boundary == nil {
		return errors.New("SQLite transaction boundary is unavailable")
	}
	boundary.operation.Lock()
	defer boundary.operation.Unlock()
	boundary.stateMu.Lock()
	defer boundary.stateMu.Unlock()
	if boundary.conn == nil || boundary.state != transactionBoundaryInstalled ||
		boundary.violation || boundary.commits != 0 || boundary.rollbacks != 0 {
		boundary.violation = true
		return errTransactionBoundary
	}
	boundary.state = transactionBoundaryBeginning
	return nil
}

// Started binds the recorded attempt to the owner's unambiguously successful
// BEGIN. It is one-use and must run before any callback receives the conn.
func (boundary *TransactionBoundary) Started() error {
	if boundary == nil {
		return errors.New("SQLite transaction boundary is unavailable")
	}
	boundary.operation.Lock()
	defer boundary.operation.Unlock()
	boundary.stateMu.Lock()
	defer boundary.stateMu.Unlock()
	if boundary.conn == nil || boundary.state != transactionBoundaryBeginning ||
		boundary.violation || boundary.commits != 0 || boundary.rollbacks != 0 {
		boundary.violation = true
		return errTransactionBoundary
	}
	boundary.state = transactionBoundaryStarted
	return nil
}

// Check is both a database/sql driver-lock barrier and a state check. It waits
// for already-started callback SQL before reporting whether the owner
// transaction remains unviolated.
func (boundary *TransactionBoundary) Check(ctx context.Context) error {
	if boundary == nil || ctx == nil {
		return errors.New("SQLite transaction boundary check is unavailable")
	}
	if err := context.Cause(ctx); err != nil {
		return err
	}
	boundary.operation.Lock()
	defer boundary.operation.Unlock()
	boundary.stateMu.Lock()
	conn := boundary.conn
	boundary.stateMu.Unlock()
	if conn == nil {
		return errTransactionBoundary
	}
	return conn.Raw(func(any) error {
		if err := context.Cause(ctx); err != nil {
			return err
		}
		boundary.stateMu.Lock()
		defer boundary.stateMu.Unlock()
		if boundary.state != transactionBoundaryStarted || boundary.violation ||
			boundary.commits != 0 || boundary.rollbacks != 0 {
			return errTransactionBoundary
		}
		return nil
	})
}

// Commit performs the sole owner-authorized COMMIT. Arming, raw execution,
// hook confirmation, and hook removal share one database/sql driver lock, so
// callback SQL cannot steal the authorization between those steps.
func (boundary *TransactionBoundary) Commit(ctx context.Context) error {
	if boundary == nil || ctx == nil {
		return errors.New("SQLite transaction boundary commit is unavailable")
	}
	if err := context.Cause(ctx); err != nil {
		return err
	}
	boundary.operation.Lock()
	defer boundary.operation.Unlock()
	boundary.stateMu.Lock()
	conn := boundary.conn
	boundary.stateMu.Unlock()
	if conn == nil {
		return errTransactionBoundary
	}

	var resultErr error
	intentionalDiscard := false
	rawErr := conn.Raw(func(driverConn any) error {
		registrar, ok := driverConn.(transactionHookRegistrar)
		if !ok {
			resultErr = errors.New("shipped SQLite transaction control is unavailable")
			return driver.ErrBadConn
		}
		execer, ok := driverConn.(driver.ExecerContext)
		if !ok {
			registrar.RegisterCommitHook(nil)
			registrar.RegisterRollbackHook(nil)
			resultErr = errors.New("shipped SQLite transaction execution is unavailable")
			intentionalDiscard = true
			return driver.ErrBadConn
		}
		cleanup := func(discard bool) error {
			registrar.RegisterCommitHook(nil)
			registrar.RegisterRollbackHook(nil)
			_, rollbackErr := execer.ExecContext(context.Background(), "ROLLBACK", nil)
			if rollbackErr != nil {
				resultErr = errors.Join(resultErr, rollbackErr)
				discard = true
			}
			if discard {
				intentionalDiscard = true
				return driver.ErrBadConn
			}
			return nil
		}
		if cause := context.Cause(ctx); cause != nil {
			resultErr = cause
			return cleanup(false)
		}
		boundary.stateMu.Lock()
		if boundary.state != transactionBoundaryStarted || boundary.violation ||
			boundary.commits != 0 || boundary.rollbacks != 0 {
			violated := boundary.violation || boundary.commits != 0 || boundary.rollbacks != 0
			boundary.stateMu.Unlock()
			resultErr = errTransactionBoundary
			return cleanup(violated)
		}
		boundary.state = transactionBoundaryCommitArmed
		boundary.stateMu.Unlock()

		// Cancellation is decided immediately before arming under the raw driver
		// lock. Once armed, COMMIT must report SQLite's actual outcome: modernc
		// can otherwise return ctx.Err after SQLITE_DONE and falsely describe a
		// durable commit as failed, making retry unsafe.
		_, commitErr := execer.ExecContext(context.WithoutCancel(ctx), "COMMIT", nil)
		boundary.stateMu.Lock()
		validCommit := commitErr == nil && !boundary.violation &&
			boundary.state == transactionBoundaryCommitObserved &&
			boundary.commits == 1 && boundary.rollbacks == 0
		boundary.stateMu.Unlock()

		// Hooks must be removed before either a successful connection reuse or an
		// intentional physical-connection discard.
		registrar.RegisterCommitHook(nil)
		registrar.RegisterRollbackHook(nil)
		if validCommit {
			boundary.stateMu.Lock()
			boundary.state = transactionBoundaryCommitted
			boundary.stateMu.Unlock()
			return nil
		}
		if commitErr != nil {
			resultErr = commitErr
		} else {
			resultErr = errTransactionBoundary
		}
		_, rollbackErr := execer.ExecContext(context.Background(), "ROLLBACK", nil)
		if rollbackErr != nil {
			resultErr = errors.Join(resultErr, rollbackErr)
		}
		intentionalDiscard = true
		return driver.ErrBadConn
	})

	boundary.stateMu.Lock()
	boundary.state = transactionBoundaryClosed
	boundary.conn = nil
	boundary.stateMu.Unlock()
	if intentionalDiscard && errors.Is(rawErr, driver.ErrBadConn) {
		if resultErr == nil {
			return errTransactionBoundary
		}
		boundary.stateMu.Lock()
		violated := boundary.violation
		boundary.stateMu.Unlock()
		if violated {
			return errors.Join(errTransactionBoundary, resultErr)
		}
		return resultErr
	}
	if rawErr != nil {
		return errors.Join(resultErr, rawErr)
	}
	return resultErr
}

func (boundary *TransactionBoundary) commitHook() int32 {
	boundary.stateMu.Lock()
	defer boundary.stateMu.Unlock()
	if boundary.commits < ^uint8(0) {
		boundary.commits++
	}
	if boundary.state != transactionBoundaryCommitArmed || boundary.violation ||
		boundary.commits != 1 || boundary.rollbacks != 0 {
		boundary.violation = true
		return 1
	}
	boundary.state = transactionBoundaryCommitObserved
	return 0
}

func (boundary *TransactionBoundary) rollbackHook() {
	boundary.stateMu.Lock()
	defer boundary.stateMu.Unlock()
	if boundary.rollbacks < ^uint8(0) {
		boundary.rollbacks++
	}
	boundary.violation = true
}

// Close removes both hooks and aborts an uncommitted owner transaction under
// one database/sql driver lock. Violated or cleanup-ambiguous connections are
// discarded only after the hooks have been removed. Close is idempotent.
func (boundary *TransactionBoundary) Close() error {
	if boundary == nil {
		return nil
	}
	boundary.operation.Lock()
	defer boundary.operation.Unlock()
	boundary.stateMu.Lock()
	if boundary.state == transactionBoundaryClosed ||
		boundary.state == transactionBoundaryCommitted {
		err := boundary.closeErr
		boundary.stateMu.Unlock()
		return err
	}
	conn := boundary.conn
	boundary.stateMu.Unlock()
	if conn == nil {
		return errTransactionBoundary
	}

	var cleanupErr error
	intentionalDiscard := false
	violated := false
	ambiguousBegin := false
	rawErr := conn.Raw(func(driverConn any) error {
		registrar, ok := driverConn.(transactionHookRegistrar)
		if !ok {
			cleanupErr = errors.New("shipped SQLite transaction cleanup is unavailable")
			return driver.ErrBadConn
		}
		registrar.RegisterCommitHook(nil)
		registrar.RegisterRollbackHook(nil)
		boundary.stateMu.Lock()
		state := boundary.state
		ambiguousBegin = state == transactionBoundaryBeginning
		violated = boundary.violation || boundary.commits != 0 || boundary.rollbacks != 0 ||
			state == transactionBoundaryCommitArmed ||
			state == transactionBoundaryCommitObserved
		boundary.stateMu.Unlock()
		if state == transactionBoundaryStarted || ambiguousBegin || violated {
			execer, ok := driverConn.(driver.ExecerContext)
			if !ok {
				cleanupErr = errors.New("shipped SQLite transaction cleanup execution is unavailable")
			} else {
				_, cleanupErr = execer.ExecContext(context.Background(), "ROLLBACK", nil)
			}
		}
		if violated || ambiguousBegin || cleanupErr != nil {
			intentionalDiscard = true
			return driver.ErrBadConn
		}
		return nil
	})

	boundary.stateMu.Lock()
	boundary.state = transactionBoundaryClosed
	boundary.conn = nil
	if intentionalDiscard && errors.Is(rawErr, driver.ErrBadConn) {
		if violated {
			boundary.closeErr = errors.Join(errTransactionBoundary, cleanupErr)
		} else if !ambiguousBegin {
			boundary.closeErr = errors.Join(errTransactionBoundary, cleanupErr)
		}
	} else {
		if cleanupErr != nil || rawErr != nil {
			boundary.closeErr = errors.Join(errTransactionBoundary, cleanupErr, rawErr)
		}
	}
	result := boundary.closeErr
	boundary.stateMu.Unlock()
	return result
}
