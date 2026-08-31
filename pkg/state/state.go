// Package state stores the workspace's last active delivery context.
package state

import (
	"context"
	"database/sql"
	"fmt"
	"hash/fnv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/sipeed/picoclaw/pkg/logger"
	"github.com/sipeed/picoclaw/pkg/sqlitestore"
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

// Manager manages the workspace-local SQLite runtime-state database.
//
// Each operation reads or updates the authoritative database instead of
// retaining a whole-state cache. Independently constructed managers therefore
// cannot overwrite one another's fields with stale snapshots.
type Manager struct {
	workspace    string
	databasePath string
	now          func() time.Time
}

var runtimeDatabaseLocks [runtimeDatabaseLockShards]sync.Mutex

var closeInitializedRuntimeDatabase = func(db *sql.DB) error { return db.Close() }

// NewSQLiteManager creates and validates a SQLite runtime-state manager.
func NewSQLiteManager(workspace string) (*Manager, error) {
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
// same authoritative SQLite path, and setters return any failure to callers.
func NewManager(workspace string) *Manager {
	manager, err := newManager(workspace)
	if err != nil {
		logger.WarnCF("state", "failed to resolve runtime-state database", map[string]any{
			"error": err.Error(),
		})
		return &Manager{workspace: workspace, now: time.Now}
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

func newManager(workspace string) (*Manager, error) {
	databasePath, err := resolveRuntimeDatabasePath(workspace)
	if err != nil {
		return nil, fmt.Errorf("resolve runtime-state database: %w", err)
	}
	return &Manager{
		workspace:    workspace,
		databasePath: databasePath,
		now:          time.Now,
	}, nil
}

// SetLastChannel atomically updates the last channel and shared timestamp.
func (sm *Manager) SetLastChannel(channel string) error {
	return sm.updateValue("last_channel", channel)
}

// SetLastChatID atomically updates the last chat ID and shared timestamp.
func (sm *Manager) SetLastChatID(chatID string) error {
	return sm.updateValue("last_chat_id", chatID)
}

func (sm *Manager) updateValue(field, value string) error {
	if sm == nil {
		return nil
	}
	if err := validateRuntimeStateValue(value); err != nil {
		return fmt.Errorf("failed to save state atomically: %w", err)
	}
	now := sm.now().UTC()
	seconds, nanoseconds, err := runtimeTimestampValues(now)
	if err != nil {
		return fmt.Errorf("failed to save state atomically: %w", err)
	}
	ctx := context.Background()
	db, unlock, err := sm.openDatabase(ctx)
	if err != nil {
		return fmt.Errorf("failed to save state atomically: %w", err)
	}
	defer unlock()
	defer db.Close()
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
	ctx := context.Background()
	db, unlock, err := sm.openDatabase(ctx)
	if err != nil {
		sm.logReadFailure(err)
		return State{}
	}
	defer unlock()
	defer db.Close()
	state, _, _, err := scanRuntimeState(db.QueryRowContext(ctx, selectRuntimeStateSQL))
	if err != nil {
		sm.logReadFailure(err)
		return State{}
	}
	return state
}

func (sm *Manager) logReadFailure(err error) {
	logger.WarnCF("state", "failed to load runtime state", map[string]any{
		"error": err.Error(),
	})
}

func (sm *Manager) openDatabase(ctx context.Context) (*sql.DB, func(), error) {
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
