// Package sessiondb is a repository-internal broker-side adapter seam shared
// by the session and thread domains. Application-facing packages receive only
// an opaque Handle; database/sql never appears in their public API.
package sessiondb

import (
	"context"
	"database/sql"
	"errors"
	"sync"
	"sync/atomic"

	"github.com/sipeed/picoclaw/internal/sqlitestore"
)

// Handle is an opaque process-local capability for one registered pool.
type Handle struct {
	id uint64
}

var (
	nextID atomic.Uint64
	pools  sync.Map
)

// Register publishes a broker-side pool to adjacent domain adapters.
func Register(database *sql.DB) Handle {
	if database == nil {
		return Handle{}
	}
	handle := Handle{id: nextID.Add(1)}
	pools.Store(handle.id, database)
	return handle
}

// Unregister revokes an adapter capability without closing the owning pool.
func Unregister(handle Handle) { pools.Delete(handle.id) }

// Database resolves an internal adapter capability. It is inaccessible to
// external consumers because this package is below pkg/internal.
func requireDatabase(handle Handle) (*sql.DB, error) {
	if handle.id == 0 {
		return nil, errors.New("session database capability is unavailable")
	}
	value, ok := pools.Load(handle.id)
	if !ok {
		return nil, errors.New("session database capability is unavailable")
	}
	database, ok := value.(*sql.DB)
	if !ok || database == nil {
		return nil, errors.New("session database capability is invalid")
	}
	return database, nil
}

// Adapter is an internal broker-side session/thread SQL adapter. Its raw
// methods cannot cross the module's pkg/internal boundary.
type Adapter struct{ handle Handle }

// Bind resolves an opaque capability for adjacent domain implementation code.
func Bind(handle Handle) Adapter { return Adapter{handle: handle} }

// Database returns the live broker-owned pool to internal adapter code.
func (adapter Adapter) Database() *sql.DB {
	database, _ := requireDatabase(adapter.handle)
	return database
}

// Immediate executes one adjacent-domain command atomically.
func (adapter Adapter) Immediate(
	ctx context.Context,
	callback func(context.Context, *sql.Conn) error,
) error {
	if callback == nil {
		return errors.New("session database callback is required")
	}
	database, err := requireDatabase(adapter.handle)
	if err != nil {
		return err
	}
	if ctx == nil {
		ctx = context.Background()
	}
	return sqlitestore.Immediate(ctx, database, func(conn *sql.Conn) error {
		return callback(ctx, conn)
	})
}

// Immediate executes one broker-side adjacent-domain command atomically.
func Immediate(ctx context.Context, handle Handle, callback func(*sql.Conn) error) error {
	database, err := requireDatabase(handle)
	if err != nil {
		return err
	}
	return sqlitestore.Immediate(ctx, database, callback)
}
