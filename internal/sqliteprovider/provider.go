//nolint:govet // Filesystem preparation stages intentionally use narrow error scopes.
package sqliteprovider

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	moderncsqlite "modernc.org/sqlite"
)

const driverName = "sqlite"

var memoryDatabaseSequence atomic.Uint64

var providerOpenLocks = struct {
	sync.Mutex
	values map[string]*providerPathOpenLock
}{values: make(map[string]*providerPathOpenLock)}

type providerPathOpenLock struct {
	mutex sync.Mutex
	refs  int
}

// DriverName is intentionally limited to provider and current compatibility
// storage code. Application-facing APIs must not expose it.
func DriverName() string { return driverName }

// open creates a database/sql pool using the shipped provider.
func open(dsn string) (*sql.DB, error) { return sql.Open(driverName, dsn) }

// OpenStore securely prepares a physical store, constructs its private DSN,
// and opens the shipped provider. Storage infrastructure owns its use.
func OpenStore(path string, busyTimeout time.Duration) (*sql.DB, error) {
	if err := validateProviderInput(path, busyTimeout); err != nil {
		return nil, err
	}
	if path != ":memory:" {
		release, err := acquireProviderOpenLock(path)
		if err != nil {
			return nil, err
		}
		defer release()
	}
	return openStore(path, busyTimeout, systemProviderOpenOps())
}

func acquireProviderOpenLock(path string) (func(), error) {
	key, err := inspectedPoolKey(path)
	if err != nil {
		return nil, err
	}
	providerOpenLocks.Lock()
	lock := providerOpenLocks.values[key]
	if lock == nil {
		lock = &providerPathOpenLock{}
		providerOpenLocks.values[key] = lock
	}
	lock.refs++
	providerOpenLocks.Unlock()
	lock.mutex.Lock()
	return func() {
		lock.mutex.Unlock()
		providerOpenLocks.Lock()
		lock.refs--
		if lock.refs == 0 && providerOpenLocks.values[key] == lock {
			delete(providerOpenLocks.values, key)
		}
		providerOpenLocks.Unlock()
	}, nil
}

type providerOpenOps struct {
	validateAncestors  func(string) error
	prepare            func(string) error
	dsn                func(string, time.Duration) (string, error)
	open               func(string) (*sql.DB, error)
	ping               func(*sql.DB, time.Duration) error
	mainIdentity       func(string) (os.FileInfo, error)
	generationIdentity func(string, os.FileInfo) ([4]os.FileInfo, error)
	secure             func(string) error
}

func systemProviderOpenOps() providerOpenOps {
	return providerOpenOps{
		validateAncestors: validateProviderAncestors,
		prepare:           PrepareStore, dsn: DSN,
		open: open,
		ping: func(database *sql.DB, timeout time.Duration) error {
			ctx, cancel := context.WithTimeout(context.Background(), timeout)
			defer cancel()
			return database.PingContext(ctx)
		},
		mainIdentity: func(path string) (os.FileInfo, error) {
			info, err := os.Lstat(path)
			if err != nil || info == nil || !info.Mode().IsRegular() ||
				info.Mode()&os.ModeSymlink != 0 {
				return nil, errors.Join(errors.New("SQLite provider main identity is unsafe"), err)
			}
			return info, nil
		},
		generationIdentity: inspectedGenerationIdentity,
		secure:             SecureGeneration,
	}
}

func openStore(path string, busyTimeout time.Duration, ops providerOpenOps) (*sql.DB, error) {
	if ops.validateAncestors == nil || ops.prepare == nil ||
		ops.dsn == nil || ops.open == nil || ops.ping == nil ||
		ops.mainIdentity == nil || ops.generationIdentity == nil || ops.secure == nil {
		return nil, errors.New("SQLite provider open operations are unavailable")
	}
	if err := validateProviderInput(path, busyTimeout); err != nil {
		return nil, err
	}
	if path == ":memory:" {
		dsn, err := ops.dsn(path, busyTimeout)
		if err != nil {
			return nil, err
		}
		database, err := ops.open(dsn)
		if err != nil {
			return nil, err
		}
		if err := ops.ping(database, busyTimeout); err != nil {
			_ = database.Close()
			return nil, err
		}
		return database, nil
	}
	if err := ops.validateAncestors(path); err != nil {
		return nil, err
	}
	if err := ops.prepare(path); err != nil {
		return nil, err
	}
	before, err := ops.mainIdentity(path)
	if err != nil {
		return nil, err
	}
	beforeGeneration, err := ops.generationIdentity(path, before)
	if err != nil {
		return nil, err
	}
	dsn, err := ops.dsn(path, busyTimeout)
	if err != nil {
		return nil, err
	}
	database, err := ops.open(dsn)
	if err != nil {
		return nil, err
	}
	if err := ops.ping(database, busyTimeout); err != nil {
		_ = database.Close()
		return nil, err
	}
	if err := ops.secure(path); err != nil {
		_ = database.Close()
		return nil, err
	}
	after, err := ops.mainIdentity(path)
	if err != nil || !os.SameFile(before, after) {
		_ = database.Close()
		return nil, errors.Join(errors.New("SQLite provider main changed while opening"), err)
	}
	afterGeneration, err := ops.generationIdentity(path, after)
	if err != nil || !safeProviderGenerationTransition(beforeGeneration, afterGeneration) {
		_ = database.Close()
		return nil, errors.Join(errors.New("SQLite provider generation changed while opening"), err)
	}
	return database, nil
}

func safeProviderGenerationTransition(before, after [4]os.FileInfo) bool {
	if before[0] == nil || after[0] == nil || !os.SameFile(before[0], after[0]) {
		return false
	}
	if after[1] != nil && after[3] != nil || after[2] != nil && after[1] == nil {
		return false
	}
	// Opening may recover and remove a rollback journal. It may also overlap a
	// cooperating SQLite process that checkpoints and recreates a coherent
	// WAL/SHM pair while the main inode remains stable. Those WAL identities are
	// transient SQLite state, not provider-generation authority.
	if before[3] == nil && after[3] != nil {
		return false
	}
	if before[3] != nil && after[3] != nil && !os.SameFile(before[3], after[3]) {
		return false
	}
	return true
}

// Configure applies and verifies the provider's live durability contract.
func Configure(ctx context.Context, database *sql.DB, busyTimeout time.Duration, memory bool) error {
	if ctx == nil || database == nil || !validBusyTimeout(busyTimeout) {
		return errors.New("SQLite provider pool is unavailable")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	journal, err := EnableWAL(ctx, database, busyTimeout)
	if err != nil {
		return fmt.Errorf("configure SQLite provider journal: %w", err)
	}
	if !memory && !strings.EqualFold(journal, "wal") {
		return fmt.Errorf("configure SQLite provider journal: selected %q", journal)
	}
	var foreignKeys, configuredBusy, synchronous int
	if err := database.QueryRowContext(ctx, `
		SELECT fk.foreign_keys, bt.timeout, sm.synchronous
		  FROM pragma_foreign_keys AS fk
		 CROSS JOIN pragma_busy_timeout AS bt
		 CROSS JOIN pragma_synchronous AS sm
	`).Scan(&foreignKeys, &configuredBusy, &synchronous); err != nil {
		return fmt.Errorf("verify SQLite provider configuration: %w", err)
	}
	if foreignKeys != 1 || configuredBusy != int(busyTimeout.Milliseconds()) || synchronous != 2 {
		return fmt.Errorf(
			"verify SQLite provider configuration: foreign_keys=%d busy_timeout=%d synchronous=%d",
			foreignKeys,
			configuredBusy,
			synchronous,
		)
	}
	return nil
}

// EnableWAL selects WAL with a bounded retry for transient provider locking.
// It exists so compatibility layers never carry provider-control statements.
func EnableWAL(ctx context.Context, database *sql.DB, busyTimeout time.Duration) (string, error) {
	if ctx == nil || database == nil || !validBusyTimeout(busyTimeout) {
		return "", errors.New("SQLite provider pool is unavailable")
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	retryCtx, cancel := context.WithTimeout(ctx, busyTimeout)
	defer cancel()
	delay := time.Millisecond
	for {
		var journal string
		err := database.QueryRowContext(retryCtx, "PRAGMA journal_mode = WAL").Scan(&journal)
		if err == nil || !IsBusyOrLocked(err) {
			return journal, err
		}
		timer := time.NewTimer(delay)
		select {
		case <-retryCtx.Done():
			timer.Stop()
			return "", retryCtx.Err()
		case <-timer.C:
		}
		if delay < 50*time.Millisecond {
			delay *= 2
		}
	}
}

// ConfigureOffline establishes the exclusive rollback-journal boundary used
// by domain migrations. Callers must already hold the canonical-home migration
// fence and must constrain the pool to one connection before calling it.
func ConfigureOffline(ctx context.Context, database *sql.DB, busyTimeout time.Duration) error {
	if ctx == nil || database == nil || !validBusyTimeout(busyTimeout) {
		return errors.New("SQLite provider pool is unavailable")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	connection, err := database.Conn(ctx)
	if err != nil {
		return fmt.Errorf("open SQLite offline provider connection: %w", err)
	}
	defer connection.Close()
	var locking, journal string
	if err := connection.QueryRowContext(ctx, "PRAGMA locking_mode = EXCLUSIVE").Scan(&locking); err != nil {
		return fmt.Errorf("configure SQLite offline locking: %w", err)
	}
	if !strings.EqualFold(locking, "exclusive") {
		return fmt.Errorf("configure SQLite offline locking: selected %q", locking)
	}
	if err := connection.QueryRowContext(ctx, "PRAGMA journal_mode = DELETE").Scan(&journal); err != nil {
		return fmt.Errorf("configure SQLite offline journal: %w", err)
	}
	if !strings.EqualFold(journal, "delete") {
		return fmt.Errorf("configure SQLite offline journal: selected %q", journal)
	}
	var foreignKeys, configuredBusy, synchronous int
	if err := connection.QueryRowContext(ctx, `
		SELECT fk.foreign_keys, bt.timeout, sm.synchronous
		  FROM pragma_foreign_keys AS fk
		 CROSS JOIN pragma_busy_timeout AS bt
		 CROSS JOIN pragma_synchronous AS sm
	`).Scan(&foreignKeys, &configuredBusy, &synchronous); err != nil {
		return fmt.Errorf("verify SQLite offline provider configuration: %w", err)
	}
	if foreignKeys != 1 || configuredBusy != int(busyTimeout.Milliseconds()) || synchronous != 2 {
		return fmt.Errorf(
			"verify SQLite offline provider configuration: foreign_keys=%d busy_timeout=%d synchronous=%d",
			foreignKeys,
			configuredBusy,
			synchronous,
		)
	}
	return nil
}

// PrepareStore validates and creates an owner-private empty database endpoint
// without asking SQLite to follow an unsafe path or sidecar.
func PrepareStore(path string) error {
	return prepareStore(path, systemProviderFilesystem())
}

func prepareStore(path string, filesystem providerFilesystem) error {
	if !validProviderFilesystemPath(path) ||
		strings.HasPrefix(strings.ToLower(path), "file:") {
		return errors.New("SQLite provider path is invalid")
	}
	if err := filesystem.validateSyntax(path); err != nil {
		return err
	}
	if err := filesystem.validateAncestors(path); err != nil {
		return err
	}
	parent := filepath.Dir(path)
	if err := filesystem.mkdirAll(parent, 0o700); err != nil {
		return fmt.Errorf("create SQLite provider directory: %w", err)
	}
	parentInfo, err := filesystem.lstat(parent)
	if err != nil || !parentInfo.IsDir() || parentInfo.Mode()&os.ModeSymlink != 0 {
		return errors.New("SQLite provider directory is unsafe")
	}
	if err := filesystem.secureDirectory(parent); err != nil {
		return fmt.Errorf("secure SQLite provider directory: %w", err)
	}
	var prior os.FileInfo
	created := false
	prior, err = filesystem.lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		created = true
		prior = nil
	} else if err != nil || prior == nil || !prior.Mode().IsRegular() ||
		prior.Mode()&os.ModeSymlink != 0 {
		return errors.Join(errors.New("SQLite provider store must be a regular file with a safe identity"), err)
	}
	if err := validateGenerationMembersWithFilesystem(path, false, filesystem); err != nil {
		return err
	}
	flag := os.O_RDWR
	if created {
		flag |= os.O_CREATE | os.O_EXCL
	}
	file, err := filesystem.openFile(path, flag, 0o600)
	if created && errors.Is(err, os.ErrExist) {
		prior, err = filesystem.lstat(path)
		if err != nil || prior == nil || !prior.Mode().IsRegular() ||
			prior.Mode()&os.ModeSymlink != 0 {
			return errors.Join(errors.New("SQLite provider concurrently created store must be a regular file"), err)
		}
		if err := validateGenerationMembersWithFilesystem(path, true, filesystem); err != nil {
			return err
		}
		file, err = filesystem.openFile(path, os.O_RDWR, 0o600)
	}
	if err != nil {
		return fmt.Errorf("prepare SQLite provider store: %w", err)
	}
	info, statErr := file.Stat()
	pathInfo, lstatErr := filesystem.lstat(path)
	if statErr != nil || lstatErr != nil || !info.Mode().IsRegular() ||
		!pathInfo.Mode().IsRegular() || pathInfo.Mode()&os.ModeSymlink != 0 ||
		!os.SameFile(info, pathInfo) || prior != nil && !os.SameFile(prior, info) {
		_ = file.Close()
		return errors.Join(errors.New("SQLite provider store changed while opening"), statErr, lstatErr)
	}
	if err := file.Chmod(0o600); err != nil {
		_ = file.Close()
		return fmt.Errorf("secure SQLite provider store: %w", err)
	}
	if err := filesystem.secureFile(path); err != nil {
		_ = file.Close()
		return fmt.Errorf("secure SQLite provider store ACL: %w", err)
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return fmt.Errorf("sync SQLite provider store: %w", err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("close prepared SQLite provider store: %w", err)
	}
	return filesystem.syncDirectory(parent)
}

// EnsurePrivateDirectory creates and validates an owner-private real
// directory for provider generations and lock namespaces.
func EnsurePrivateDirectory(path string) error {
	return ensurePrivateDirectory(path, systemProviderFilesystem())
}

func ensurePrivateDirectory(path string, filesystem providerFilesystem) error {
	if !validProviderFilesystemPath(path) {
		return errors.New("SQLite provider directory is invalid")
	}
	if err := filesystem.validateSyntax(path); err != nil {
		return err
	}
	if err := filesystem.validateAncestors(filepath.Join(path, ".provider-boundary")); err != nil {
		return err
	}
	if err := filesystem.mkdirAll(path, 0o700); err != nil {
		return err
	}
	info, err := filesystem.lstat(path)
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("SQLite provider directory must be a real directory")
	}
	if err := filesystem.secureDirectory(path); err != nil {
		return err
	}
	return filesystem.syncDirectory(path)
}

// SecureGeneration validates existing database, WAL, SHM, and rollback-journal members with
// non-opening metadata operations and enforces owner-only modes.
func SecureGeneration(path string) error {
	return secureGeneration(path, systemProviderFilesystem())
}

func secureGeneration(path string, filesystem providerFilesystem) error {
	if err := filesystem.validateSyntax(path); err != nil {
		return err
	}
	if err := filesystem.validateAncestors(path); err != nil {
		return err
	}
	return validateGenerationMembersWithFilesystem(path, true, filesystem)
}

func validateGenerationMembers(path string, requireDatabase bool) error {
	return validateGenerationMembersWithFilesystem(
		path, requireDatabase, systemProviderFilesystem(),
	)
}

func validateGenerationMembersWithFilesystem(
	path string,
	requireDatabase bool,
	filesystem providerFilesystem,
) error {
	for index, member := range []string{path, path + "-wal", path + "-shm", path + "-journal"} {
		optional := index > 0
		info, err := filesystem.lstat(member)
		if errors.Is(err, os.ErrNotExist) {
			if requireDatabase && index == 0 {
				return errors.New("SQLite provider store disappeared")
			}
			continue
		}
		if err != nil {
			return fmt.Errorf("inspect SQLite provider generation: %w", err)
		}
		if optional {
			main, mainErr := filesystem.lstat(path)
			if errors.Is(mainErr, os.ErrNotExist) {
				return errors.New("SQLite provider sidecar exists without its database")
			}
			if mainErr != nil || main == nil || !main.Mode().IsRegular() ||
				main.Mode()&os.ModeSymlink != 0 {
				return errors.Join(errors.New("SQLite provider database identity is unsafe"), mainErr)
			}
		}
		if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
			return errors.New("SQLite provider generation member is not a regular file")
		}
		if !filesystem.singleLink(member, info) {
			return errors.New("SQLite provider generation member has a hardlink alias")
		}
		if !filesystem.owned(member, info) {
			return errors.New("SQLite provider generation member is owned by another user")
		}
		if err := filesystem.secureFile(member); err != nil {
			if optional && errors.Is(err, os.ErrNotExist) {
				if _, currentErr := filesystem.lstat(member); errors.Is(currentErr, os.ErrNotExist) {
					continue
				}
			}
			return fmt.Errorf("secure SQLite provider generation: %w", err)
		}
		current, currentErr := filesystem.lstat(member)
		if optional && errors.Is(currentErr, os.ErrNotExist) {
			continue
		}
		if currentErr != nil || current == nil || !current.Mode().IsRegular() ||
			current.Mode()&os.ModeSymlink != 0 || !os.SameFile(info, current) {
			return errors.Join(
				errors.New("SQLite provider generation member changed while securing"),
				currentErr,
			)
		}
	}
	return validateGenerationCoherence(path, filesystem)
}

func validateGenerationCoherence(path string, filesystem providerFilesystem) error {
	present := [3]bool{}
	for index, member := range []string{path + "-wal", path + "-shm", path + "-journal"} {
		info, err := filesystem.lstat(member)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil || info == nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
			return errors.Join(errors.New("SQLite provider sidecar coherence is unavailable"), err)
		}
		present[index] = true
	}
	if present[0] && present[2] || present[1] && !present[0] {
		return errors.New("SQLite provider generation has incoherent sidecars")
	}
	return nil
}

// DSN constructs the durable connection configuration owned by this provider.
func DSN(path string, busyTimeout time.Duration) (string, error) {
	if err := validateProviderInput(path, busyTimeout); err != nil {
		return "", err
	}
	if path == ":memory:" {
		name := "picoclaw-memory-" + strconv.FormatUint(memoryDatabaseSequence.Add(1), 10)
		return "file:" + name + "?mode=memory&cache=shared&_pragma=foreign_keys(1)&_pragma=busy_timeout(" +
			strconv.FormatInt(busyTimeout.Milliseconds(), 10) + ")&_pragma=synchronous(FULL)", nil
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	slashPath := FileURLPath(
		filepath.ToSlash(abs),
		filepath.ToSlash(filepath.VolumeName(abs)),
	)
	u := &url.URL{Scheme: "file", Path: slashPath}
	query := url.Values{}
	query.Add("mode", "rw")
	query.Add("_pragma", "foreign_keys(1)")
	query.Add("_pragma", "busy_timeout("+strconv.FormatInt(busyTimeout.Milliseconds(), 10)+")")
	query.Add("_pragma", "synchronous(FULL)")
	u.RawQuery = query.Encode()
	return u.String(), nil
}

func validBusyTimeout(timeout time.Duration) bool {
	return timeout >= time.Millisecond && timeout <= time.Minute
}

func validateProviderInput(path string, busyTimeout time.Duration) error {
	if path == "" || path != strings.TrimSpace(path) || strings.ContainsRune(path, 0) ||
		!utf8.ValidString(path) || len(path) > 16<<10 ||
		strings.HasPrefix(strings.ToLower(path), "file:") || !validBusyTimeout(busyTimeout) {
		return errors.New("SQLite provider path or busy timeout is invalid")
	}
	if path != ":memory:" {
		if err := validateProviderPathSyntax(path); err != nil {
			return err
		}
	}
	return nil
}

func validProviderFilesystemPath(path string) bool {
	if path == "" || path != strings.TrimSpace(path) || strings.ContainsRune(path, 0) ||
		!utf8.ValidString(path) || len(path) > 16<<10 {
		return false
	}
	for _, component := range strings.Split(filepath.Clean(path), string(os.PathSeparator)) {
		if len(component) > 255 {
			return false
		}
	}
	return true
}

// FileURLPath normalizes a physical path for the provider's file URI syntax.
func FileURLPath(slashPath, slashVolume string) string {
	if slashVolume != "" && !strings.HasPrefix(slashPath, "/") {
		return "/" + slashPath
	}
	return slashPath
}

// IsBusyOrLocked reports the two SQLite primary result codes for which a
// bounded read-side retry is safe.
func IsBusyOrLocked(err error) bool {
	var sqliteErr *moderncsqlite.Error
	if !errors.As(err, &sqliteErr) {
		return false
	}
	switch sqliteErr.Code() & 0xff {
	case 5, 6: // SQLITE_BUSY, SQLITE_LOCKED
		return true
	default:
		return false
	}
}
