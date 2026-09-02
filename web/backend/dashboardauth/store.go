// Package dashboardauth provides a bcrypt-backed SQLite store for the
// launcher dashboard password. The database contains a single row (id=1)
// with the bcrypt hash; no plaintext is ever persisted.
package dashboardauth

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"sync/atomic"

	"golang.org/x/crypto/bcrypt"

	"github.com/sipeed/picoclaw/internal/sqlitestore"
	"github.com/sipeed/picoclaw/pkg/database"
	"github.com/sipeed/picoclaw/web/backend/launcherconfig"
)

// Store holds a handle to the SQLite database that stores the bcrypt hash.
type Store struct {
	db     *sql.DB
	broker *database.Client
}

const launcherAuthStoreID database.StoreID = "launcher.auth"

var allowUnfencedLauncherAuthProviderForTests atomic.Bool

// NewBroker returns the typed launcher-auth client used by launcher processes.
// The returned store never opens or observes the physical database generation.
func NewBroker(client *database.Client) (*Store, error) {
	if client == nil {
		return nil, errors.New("launcher auth database broker is unavailable")
	}
	return &Store{broker: client}, nil
}

// New opens (or creates) the database inside dir, using the package's
// canonical filename. This is the preferred constructor for most callers.
// Any error is wrapped with the resolved path so callers get actionable output.
func newLocal(dir string) (*Store, error) {
	return newLocalWithLauncherConfig(dir, filepath.Join(dir, launcherconfig.FileName))
}

// NewWithLauncherConfig opens the canonical database in dir and imports any
// removed authentication fields from launcherPath on the first schema open.
func newLocalWithLauncherConfig(dir, launcherPath string) (*Store, error) {
	path := filepath.Join(dir, databaseFilename)
	s, err := openLocalWithLauncherConfig(path, launcherPath)
	if err != nil {
		return nil, fmt.Errorf("open %q: %w", path, err)
	}
	return s, nil
}

// Open opens (or creates) the SQLite database at path and migrates the schema.
func openLocal(path string) (*Store, error) {
	return openLocalWithLauncherConfig(path, filepath.Join(filepath.Dir(path), launcherconfig.FileName))
}

// OpenWithLauncherConfig opens path and performs the retained-source launcher
// config migration. The source is snapshotted before its legacy fields are
// removed; the database is authoritative as soon as its migration commits.
func openLocalWithLauncherConfig(path, launcherPath string) (*Store, error) {
	if !database.BrokerAuthorityHeld() && !database.MigrationFenceHeld() &&
		!database.ProviderTestAuthorityHeld() &&
		!allowUnfencedLauncherAuthProviderForTests.Load() {
		return nil, database.NewError(
			database.CodeUnauthorized,
			"launcher-auth provider access requires database owner authority",
		)
	}
	absPath, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	absLauncherPath, err := filepath.Abs(launcherPath)
	if err != nil {
		return nil, err
	}
	if filepath.Base(absLauncherPath) != launcherconfig.FileName {
		return nil, fmt.Errorf("launcher config must be named %s", launcherconfig.FileName)
	}

	ctx := context.Background()
	db, err := sqlitestore.Open(ctx, absPath, storeOptions(absLauncherPath))
	if err != nil {
		return nil, err
	}
	if err = finishLegacyLauncherConfigMigration(ctx, db, absLauncherPath); err != nil {
		_ = db.Close()
		return nil, err
	}
	return &Store{db: db}, nil
}

// Close releases a broker client reference or the broker-side adapter handle.
func (s *Store) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.Close()
}

// StoreID returns the opaque broker catalog identity for launcher auth.
func (s *Store) StoreID() database.StoreID { return launcherAuthStoreID }

// IsInitialized reports whether a password hash has been stored.
func (s *Store) IsInitialized(ctx context.Context) (bool, error) {
	if s != nil && s.broker != nil {
		var response launcherAuthInitializedResponse
		err := s.broker.Call(
			ctx,
			launcherAuthDomain,
			launcherAuthVersion,
			launcherAuthOperationInitialized,
			launcherAuthEmptyRequest{StoreID: launcherAuthStoreID},
			&response,
		)
		return response.Initialized, err
	}
	if s == nil || s.db == nil {
		return false, errors.New("launcher auth store is unavailable")
	}
	var n int
	err := s.db.QueryRowContext(ctx, sqlCountCredentials).Scan(&n)
	if err != nil {
		return false, err
	}
	return n > 0, nil
}

// SetPassword hashes plain with bcrypt (cost 12) and stores (or replaces) it.
// The plaintext is never written to disk.
func (s *Store) SetPassword(ctx context.Context, plain string) error {
	if len([]rune(plain)) == 0 {
		return errors.New("password must not be empty")
	}
	if s != nil && s.broker != nil {
		var response launcherAuthMutationResponse
		return s.broker.CallWithOptions(
			ctx,
			launcherAuthDomain,
			launcherAuthVersion,
			launcherAuthOperationSetPassword,
			launcherAuthPasswordRequest{StoreID: launcherAuthStoreID, Password: plain},
			&response,
			database.CallOptions{Mutation: true},
		)
	}
	if s == nil || s.db == nil {
		return errors.New("launcher auth store is unavailable")
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(plain), bcryptCost)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, sqlUpsertHash, string(hash))
	return err
}

// VerifyPassword returns true iff plain matches the stored bcrypt hash.
// Returns (false, nil) when no password has been set yet.
func (s *Store) VerifyPassword(ctx context.Context, plain string) (bool, error) {
	if s != nil && s.broker != nil {
		var response launcherAuthVerificationResponse
		err := s.broker.Call(
			ctx,
			launcherAuthDomain,
			launcherAuthVersion,
			launcherAuthOperationVerifyPassword,
			launcherAuthPasswordRequest{StoreID: launcherAuthStoreID, Password: plain},
			&response,
		)
		return response.Verified, err
	}
	if s == nil || s.db == nil {
		return false, errors.New("launcher auth store is unavailable")
	}
	var hash string
	err := s.db.QueryRowContext(ctx, sqlSelectHash).Scan(&hash)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	err = bcrypt.CompareHashAndPassword([]byte(hash), []byte(plain))
	if errors.Is(err, bcrypt.ErrMismatchedHashAndPassword) {
		return false, nil
	}
	return err == nil, err
}
