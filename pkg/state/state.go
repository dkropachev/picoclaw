// Package state stores the workspace's last active delivery context.
package state

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"hash/fnv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/sipeed/picoclaw/internal/sqlitestore"
	"github.com/sipeed/picoclaw/pkg/database"
	"github.com/sipeed/picoclaw/pkg/logger"
)

const maxRuntimeStateValueBytes = 64 << 10

const runtimeDatabaseLockShards = 64

// State represents the persistent runtime delivery state for a workspace.
type State struct {
	// LastChannel is the last channel used for communication.
	LastChannel string `json:"last_channel,omitempty"`

	// LastChatID is the last chat ID used for communication.
	LastChatID string `json:"last_chat_id,omitempty"`

	// Timestamp is the last time this state was updated.
	Timestamp time.Time `json:"timestamp"`
}

// Manager manages workspace runtime state through either the authenticated
// broker or the standalone local compatibility backend.
//
// Each operation reads or updates the authoritative database instead of
// retaining a whole-state cache. Independently constructed managers therefore
// cannot overwrite one another's fields with stale snapshots.
type Manager struct {
	workspace    string
	databasePath string
	now          func() time.Time

	broker    *database.Client
	brokerErr error
	storeID   database.StoreID
	retained  *retainedRuntimeDatabase
}

type retainedRuntimeDatabase struct {
	mu     sync.RWMutex
	db     *sql.DB
	unlock func()
	closed bool
}

var runtimeDatabaseLocks [runtimeDatabaseLockShards]sync.Mutex

var closeInitializedRuntimeDatabase = func(db *sql.DB) error { return db.Close() }

var allowUnfencedRuntimeStateProviderForTests atomic.Bool

// NewSQLiteManager creates and validates a SQLite runtime-state manager.
func NewSQLiteManager(workspace string) (*Manager, error) {
	if client := runtimeStateBrokerClient(); client != nil {
		manager, err := newBrokerManager(workspace, client)
		if err != nil {
			return nil, err
		}
		if _, err := manager.brokerSnapshot(context.Background()); err != nil {
			return nil, err
		}
		return manager, nil
	}
	if !database.ProviderTestAuthorityHeld() && !allowUnfencedRuntimeStateProviderForTests.Load() {
		return nil, database.NewError(
			database.CodeUnavailable,
			"runtime-state database broker client is unavailable",
		)
	}
	return newSQLiteManagerLocal(workspace)
}

func newSQLiteManagerLocal(workspace string) (*Manager, error) {
	if !database.MigrationFenceHeld() && !database.ProviderTestAuthorityHeld() &&
		!allowUnfencedRuntimeStateProviderForTests.Load() {
		return nil, database.NewError(
			database.CodeUnauthorized,
			"runtime-state offline store requires exclusive migration fencing",
		)
	}
	manager, err := newManager(workspace)
	if err != nil {
		return nil, err
	}
	db, unlock, err := manager.openDatabase(context.Background())
	if err != nil {
		return nil, err
	}
	closeErr := closeInitializedRuntimeDatabase(db)
	unlock()
	if closeErr != nil {
		return nil, fmt.Errorf("close runtime-state database: %w", closeErr)
	}
	return manager, nil
}

// NewManager creates a state manager while retaining the historical no-error
// constructor. Initialization failures are logged; later operations retry the
// same authoritative broker/store, and setters return any failure to callers.
func NewManager(workspace string) *Manager {
	if client := runtimeStateBrokerClient(); client != nil {
		manager, err := newBrokerManager(workspace, client)
		if err != nil {
			logger.WarnCF("state", "failed to configure runtime-state broker", map[string]any{
				"error": err.Error(),
			})
			return &Manager{
				workspace: workspace, now: time.Now, broker: client,
				brokerErr: err,
			}
		}
		if _, err := manager.brokerSnapshot(context.Background()); err != nil {
			manager.logReadFailure(err)
		}
		return manager
	}
	if !database.ProviderTestAuthorityHeld() && !allowUnfencedRuntimeStateProviderForTests.Load() {
		err := database.NewError(
			database.CodeUnavailable,
			"runtime-state database broker client is unavailable",
		)
		return &Manager{workspace: workspace, now: time.Now, brokerErr: err}
	}
	return newManagerCompatibilityLocal(workspace)
}

func newManagerCompatibilityLocal(workspace string) *Manager {
	manager, err := newManager(workspace)
	if err != nil {
		logger.WarnCF("state", "failed to resolve runtime-state database", map[string]any{
			"error": err.Error(),
		})
		return &Manager{workspace: workspace, now: time.Now, storeID: RuntimeStateStoreID}
	}
	db, unlock, openErr := manager.openDatabase(context.Background())
	if openErr != nil {
		logger.WarnCF("state", "failed to initialize runtime-state database", map[string]any{
			"error": openErr.Error(),
		})
		return manager
	}
	if closeErr := closeInitializedRuntimeDatabase(db); closeErr != nil {
		logger.WarnCF("state", "failed to close initialized runtime-state database", map[string]any{
			"error": closeErr.Error(),
		})
	}
	unlock()
	return manager
}

func newRetainedSQLiteManager(workspace string) (*Manager, error) {
	if !database.BrokerAuthorityHeld() && !database.ProviderTestAuthorityHeld() &&
		!allowUnfencedRuntimeStateProviderForTests.Load() {
		return nil, database.NewError(
			database.CodeUnauthorized,
			"runtime-state retained store requires online database fencing",
		)
	}
	manager, err := newManager(workspace)
	if err != nil {
		return nil, err
	}
	db, unlock, err := manager.openDatabase(context.Background())
	if err != nil {
		return nil, err
	}
	manager.retained = &retainedRuntimeDatabase{db: db, unlock: unlock}
	return manager, nil
}

func newManager(workspace string) (*Manager, error) {
	if !runtimeStateLocalProviderAuthorized() {
		return nil, database.NewError(
			database.CodeUnauthorized,
			"runtime-state provider access requires database owner fencing",
		)
	}
	databasePath, err := resolveRuntimeDatabasePath(workspace)
	if err != nil {
		return nil, fmt.Errorf("resolve runtime-state database: %w", err)
	}
	return &Manager{
		workspace:    workspace,
		databasePath: databasePath,
		now:          time.Now,
		storeID:      RuntimeStateStoreID,
	}, nil
}

// SetLastChannel atomically updates the last channel and shared timestamp.
func (sm *Manager) SetLastChannel(channel string) error {
	return sm.updateValue(context.Background(), "last_channel", channel)
}

// SetLastChatID atomically updates the last chat ID and shared timestamp.
func (sm *Manager) SetLastChatID(chatID string) error {
	return sm.updateValue(context.Background(), "last_chat_id", chatID)
}

func (sm *Manager) updateValue(ctx context.Context, field, value string) error {
	if sm == nil {
		return nil
	}
	if sm.brokerErr != nil {
		return fmt.Errorf("failed to save state atomically: %w", sm.brokerErr)
	}
	if err := validateRuntimeStateValue(value); err != nil {
		if sm.broker != nil {
			return fmt.Errorf(
				"failed to save state atomically: %w",
				database.NewError(database.CodeInvalid, "runtime-state value is invalid"),
			)
		}
		return fmt.Errorf("failed to save state atomically: %w", err)
	}
	if sm.broker != nil {
		if err := sm.brokerUpdate(ctx, field, value); err != nil {
			return fmt.Errorf("failed to save state atomically: %w", err)
		}
		return nil
	}
	now := sm.now().UTC()
	seconds, nanoseconds, err := runtimeTimestampValues(now)
	if err != nil {
		return fmt.Errorf("failed to save state atomically: %w", err)
	}
	if ctx == nil {
		ctx = context.Background()
	}
	db, release, err := sm.acquireDatabase(ctx)
	if err != nil {
		return fmt.Errorf("failed to save state atomically: %w", err)
	}
	defer release()
	err = sqlitestore.Immediate(ctx, db, func(conn *sql.Conn) error {
		var version int64
		if loadErr := conn.QueryRowContext(
			ctx,
			`SELECT version FROM runtime_state WHERE id = 1`,
		).Scan(&version); loadErr != nil {
			return loadErr
		}
		query := updateLastChannelSQL
		if field == "last_chat_id" {
			query = updateLastChatIDSQL
		}
		result, updateErr := conn.ExecContext(ctx, query, value, seconds, nanoseconds, version)
		if updateErr != nil {
			return updateErr
		}
		changed, rowsErr := result.RowsAffected()
		if rowsErr != nil {
			return rowsErr
		}
		if changed != 1 {
			return errRuntimeStateVersionChanged
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("failed to save state atomically: %w", err)
	}
	return nil
}

// GetLastChannel returns the last channel from authoritative state.
func (sm *Manager) GetLastChannel() string {
	return sm.snapshot().LastChannel
}

// GetLastChatID returns the last chat ID from authoritative state.
func (sm *Manager) GetLastChatID() string {
	return sm.snapshot().LastChatID
}

// GetTimestamp returns the timestamp of the last state update.
func (sm *Manager) GetTimestamp() time.Time {
	return sm.snapshot().Timestamp
}

func (sm *Manager) snapshot() State {
	if sm == nil {
		return State{}
	}
	state, err := sm.snapshotContext(context.Background())
	if err != nil {
		sm.logReadFailure(err)
		return State{}
	}
	return state
}

func (sm *Manager) snapshotContext(ctx context.Context) (State, error) {
	if sm == nil {
		return State{}, nil
	}
	if sm.brokerErr != nil {
		return State{}, sm.brokerErr
	}
	if sm.broker != nil {
		return sm.brokerSnapshot(ctx)
	}
	if ctx == nil {
		ctx = context.Background()
	}
	db, release, err := sm.acquireDatabase(ctx)
	if err != nil {
		return State{}, err
	}
	defer release()
	state, _, _, err := scanRuntimeState(db.QueryRowContext(ctx, selectRuntimeStateSQL))
	if err != nil {
		return State{}, err
	}
	return state, nil
}

func (sm *Manager) logReadFailure(err error) {
	logger.WarnCF("state", "failed to load runtime state", map[string]any{
		"error": err.Error(),
	})
}

func (sm *Manager) openDatabase(ctx context.Context) (*sql.DB, func(), error) {
	if sm != nil && sm.brokerErr != nil {
		return nil, nil, sm.brokerErr
	}
	if !runtimeStateLocalProviderAuthorized() {
		return nil, nil, database.NewError(
			database.CodeUnauthorized,
			"runtime-state provider access requires database owner fencing",
		)
	}
	if sm == nil || strings.TrimSpace(sm.databasePath) == "" {
		return nil, nil, fmt.Errorf("runtime-state database path is unavailable")
	}
	localLock := runtimeLocalDatabaseLock(sm.databasePath)
	localLock.Lock()
	unlockFile, err := lockRuntimeDatabase(sm.databasePath)
	if err != nil {
		localLock.Unlock()
		return nil, nil, err
	}
	unlock := func() {
		unlockFile()
		localLock.Unlock()
	}
	options, err := runtimeStoreOptions(sm.workspace)
	if err != nil {
		unlock()
		return nil, nil, err
	}
	db, err := sqlitestore.Open(ctx, sm.databasePath, options)
	if err != nil {
		unlock()
		return nil, nil, err
	}
	return db, unlock, nil
}

func runtimeStateLocalProviderAuthorized() bool {
	return database.BrokerAuthorityHeld() || database.MigrationFenceHeld() ||
		database.ProviderTestAuthorityHeld() || allowUnfencedRuntimeStateProviderForTests.Load()
}

func (sm *Manager) acquireDatabase(ctx context.Context) (*sql.DB, func(), error) {
	if sm != nil && sm.brokerErr != nil {
		return nil, nil, sm.brokerErr
	}
	if !runtimeStateLocalProviderAuthorized() {
		return nil, nil, database.NewError(
			database.CodeUnauthorized,
			"runtime-state provider access requires database owner fencing",
		)
	}
	if sm != nil && sm.retained != nil {
		return sm.retained.acquire()
	}
	db, unlock, err := sm.openDatabase(ctx)
	if err != nil {
		return nil, nil, err
	}
	release := func() {
		_ = db.Close()
		unlock()
	}
	return db, release, nil
}

func (retained *retainedRuntimeDatabase) acquire() (*sql.DB, func(), error) {
	if retained == nil {
		return nil, nil, errors.New("runtime-state database is unavailable")
	}
	retained.mu.RLock()
	if retained.closed || retained.db == nil {
		retained.mu.RUnlock()
		return nil, nil, errors.New("runtime-state database is closed")
	}
	return retained.db, retained.mu.RUnlock, nil
}

func (retained *retainedRuntimeDatabase) close() error {
	if retained == nil {
		return nil
	}
	retained.mu.Lock()
	defer retained.mu.Unlock()
	if retained.closed {
		return nil
	}
	retained.closed = true
	var closeErr error
	if retained.db != nil {
		closeErr = retained.db.Close()
		retained.db = nil
	}
	if retained.unlock != nil {
		retained.unlock()
		retained.unlock = nil
	}
	return closeErr
}

// Close releases a broker-side retained pool. Standalone and broker-client
// managers do not retain a physical handle, so Close is a no-op for them.
func (sm *Manager) Close() error {
	if sm == nil || sm.retained == nil {
		return nil
	}
	return sm.retained.close()
}

// StoreID returns the opaque logical identity used by broker-backed managers.
func (sm *Manager) StoreID() database.StoreID {
	if sm == nil {
		return ""
	}
	return sm.storeID
}

func runtimeLocalDatabaseLock(databasePath string) *sync.Mutex {
	hash := fnv.New32a()
	_, _ = hash.Write([]byte(databasePath))
	return &runtimeDatabaseLocks[hash.Sum32()%runtimeDatabaseLockShards]
}

func validateRuntimeStateValue(value string) error {
	if !utf8.ValidString(value) {
		return fmt.Errorf("runtime-state value is not valid UTF-8")
	}
	if len(value) > maxRuntimeStateValueBytes {
		return fmt.Errorf("runtime-state value exceeds its size limit")
	}
	if strings.ContainsRune(value, '\x00') {
		return fmt.Errorf("runtime-state value contains a NUL byte")
	}
	return nil
}
