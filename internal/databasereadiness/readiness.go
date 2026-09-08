// Package databasereadiness classifies trusted catalog generations without
// initializing stores or applying application schema changes.
package databasereadiness

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/sipeed/picoclaw/internal/databaseadapter"
	"github.com/sipeed/picoclaw/internal/databaseclaims"
	"github.com/sipeed/picoclaw/internal/fileidentity"
	"github.com/sipeed/picoclaw/internal/sqliteprovider"
	"github.com/sipeed/picoclaw/internal/storecatalog"
	"github.com/sipeed/picoclaw/pkg/database"
)

const (
	inspectionTimeout         = 5 * time.Second
	legacyDiscoveryMaxEntries = 65_536
	legacyDiscoveryMaxFiles   = 32_768
	legacyDiscoveryMaxDepth   = 64
	legacyDiscoveryReadBatch  = 256
	legacyPathMaxBytes        = 16 << 10
	legacyComponentMaxBytes   = 255
)

// Snapshot retains pools for generations classified ready. A later owner may
// adopt those pools through sqliteprovider.OpenStore. Close releases every
// retained pool that was not adopted.
type Snapshot struct {
	mu             sync.Mutex
	statuses       []database.StoreStatus
	inspections    []sqliteprovider.Inspection
	inspectionByID map[database.StoreID]int
	lease          *databaseclaims.Lease
	once           sync.Once
	err            error
}

// Adopt transfers the exact ready inspection for id to a trusted owner while
// holding the originating claim lease and fence live across the handoff.
func (snapshot *Snapshot) Adopt(
	id database.StoreID,
	busyTimeout time.Duration,
) (*sql.DB, error) {
	if snapshot == nil || snapshot.lease == nil || !id.Valid() {
		return nil, database.NewError(database.CodeInvalid, "database readiness adoption is invalid")
	}
	release, err := snapshot.lease.Guard()
	if err != nil {
		return nil, err
	}
	defer release()
	snapshot.mu.Lock()
	defer snapshot.mu.Unlock()
	index, ok := snapshot.inspectionByID[id]
	if !ok || index < 0 || index >= len(snapshot.inspections) {
		return nil, database.NewError(database.CodeUnavailable, "database readiness inspection is unavailable")
	}
	database, err := snapshot.inspections[index].Adopt(busyTimeout)
	if err == nil {
		delete(snapshot.inspectionByID, id)
	}
	return database, err
}

// Statuses returns a detached, ID-sorted readiness snapshot.
func (snapshot *Snapshot) Statuses() []database.StoreStatus {
	if snapshot == nil {
		return nil
	}
	if snapshot.lease != nil {
		release, err := snapshot.lease.Guard()
		if err != nil {
			return nil
		}
		defer release()
	}
	snapshot.mu.Lock()
	defer snapshot.mu.Unlock()
	statuses, err := database.ValidateStoreStatuses(snapshot.statuses)
	if err != nil {
		return nil
	}
	return statuses
}

// Close releases inspection pools not adopted by a typed store owner. It is
// safe to call more than once.
func (snapshot *Snapshot) Close() error {
	if snapshot == nil {
		return nil
	}
	snapshot.once.Do(func() {
		snapshot.mu.Lock()
		defer snapshot.mu.Unlock()
		for _, inspection := range snapshot.inspections {
			snapshot.err = errors.Join(snapshot.err, inspection.Release())
		}
		snapshot.inspections = nil
		snapshot.inspectionByID = nil
	})
	return snapshot.err
}

// Probe inspects every store in an already-claimed catalog. Probe neither
// builds a public catalog nor initializes a missing/empty generation.
func Probe(
	ctx context.Context,
	lease *databaseclaims.Lease,
	registry *databaseadapter.Registry,
) (*Snapshot, error) {
	if lease == nil || registry == nil {
		return nil, database.NewError(
			database.CodeInvalid,
			"database readiness boundary is invalid",
		)
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	specs, releaseLease, err := lease.GuardStores()
	if err != nil {
		return nil, database.NewError(database.CodeIntegrity, "database readiness claim authority is unavailable")
	}
	defer releaseLease()

	if len(specs) == 0 {
		return nil, database.NewError(
			database.CodeIntegrity,
			"database readiness catalog is empty",
		)
	}
	// Registry completeness is checked before any generation metadata read or
	// provider open. Partial registries cannot yield a coherent snapshot.
	for _, spec := range specs {
		if _, registered := registry.Lookup(spec.Domain); !registered {
			return nil, database.NewError(
				database.CodeUnsupported,
				"database readiness adapter catalog is incomplete",
			)
		}
	}
	exclusions, err := generationExclusions(specs)
	if err != nil {
		return nil, database.NewError(
			database.CodeIntegrity,
			"database readiness catalog is invalid",
		)
	}

	snapshot := &Snapshot{
		statuses:       make([]database.StoreStatus, 0, len(specs)),
		inspections:    make([]sqliteprovider.Inspection, 0, len(specs)),
		inspectionByID: make(map[database.StoreID]int, len(specs)),
		lease:          lease,
	}
	legacyBudget, err := newLegacyDiscoveryBudget(defaultLegacyDiscoveryLimits())
	if err != nil {
		return nil, err
	}
	complete := false
	defer func() {
		if !complete {
			_ = snapshot.Close()
		}
	}()

	for _, spec := range specs {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		adapter, _ := registry.Lookup(spec.Domain)

		inspection, inspectErr := sqliteprovider.Inspect(ctx, spec.Path, inspectionTimeout)
		if contextErr := ctx.Err(); contextErr != nil {
			_ = inspection.Release()
			return nil, contextErr
		}
		status := classifyInspection(
			ctx, spec, adapter.Contract, inspection, inspectErr, exclusions, legacyBudget,
		)
		if contextErr := ctx.Err(); contextErr != nil {
			_ = inspection.Release()
			return nil, contextErr
		}
		if status.Readiness != database.StoreReady {
			if releaseErr := inspection.Release(); releaseErr != nil {
				status = unavailableStatus(
					spec.ID,
					database.CodeUnavailable,
					"database readiness pool could not be closed",
				)
			}
		} else if inspection.Exists {
			snapshot.inspections = append(snapshot.inspections, inspection)
			snapshot.inspectionByID[spec.ID] = len(snapshot.inspections) - 1
		}
		snapshot.statuses = append(snapshot.statuses, status)
	}

	validated, err := database.ValidateStoreStatuses(snapshot.statuses)
	if err != nil {
		return nil, err
	}
	snapshot.statuses = validated
	complete = true
	return snapshot, nil
}

func classifyInspection(
	ctx context.Context,
	spec storecatalog.Spec,
	contract databaseadapter.Contract,
	inspection sqliteprovider.Inspection,
	inspectErr error,
	exclusions generationSet,
	budgets ...*legacyDiscoveryBudget,
) database.StoreStatus {
	if inspectErr != nil {
		if sqliteprovider.IsInspectionIntegrity(inspectErr) {
			return unavailableStatus(
				spec.ID,
				database.CodeIntegrity,
				"database store integrity check failed",
			)
		}
		if sqliteprovider.IsBusyOrLocked(inspectErr) {
			return unavailableStatus(
				spec.ID,
				database.CodeUnavailable,
				"database store is locked",
			)
		}
		return unavailableStatus(
			spec.ID,
			database.CodeUnavailable,
			"database store is unavailable",
		)
	}

	if !inspection.Exists || inspection.Empty {
		var legacy bool
		var err error
		if len(budgets) == 1 && budgets[0] != nil {
			legacy, err = legacyInputExistsWithBudget(ctx, spec.LegacyRoots, exclusions, budgets[0])
		} else {
			legacy, err = legacyInputExists(ctx, spec.LegacyRoots, exclusions)
		}
		if err != nil {
			if errors.Is(err, errLegacyIntegrity) {
				return unavailableStatus(
					spec.ID,
					database.CodeIntegrity,
					"database legacy input is unsafe",
				)
			}
			return unavailableStatus(
				spec.ID,
				database.CodeUnavailable,
				"database legacy input is unavailable",
			)
		}
		if legacy || contract.EmptyPolicy == databaseadapter.EmptyMigrateOffline {
			return migrationStatus(spec.ID)
		}
		return database.StoreStatus{ID: spec.ID, Readiness: database.StoreReady}
	}

	switch {
	case inspection.Version < contract.CurrentVersion:
		return migrationStatus(spec.ID)
	case inspection.Version > contract.CurrentVersion:
		return unavailableStatus(
			spec.ID,
			database.CodeUnsupported,
			"database schema is newer than supported",
		)
	}

	for _, object := range contract.RequiredObjects {
		objectsReady, err := inspection.HasSchemaObjects(ctx, object.Type, object.Name)
		if err != nil {
			return unavailableStatus(
				spec.ID,
				database.CodeUnavailable,
				"database schema readiness is unavailable",
			)
		}
		if !objectsReady {
			return migrationStatus(spec.ID)
		}
	}
	for _, columns := range contract.RequiredColumns {
		ready, columnErr := inspection.HasTableColumns(ctx, columns.Table, columns.Columns...)
		if columnErr != nil {
			return unavailableStatus(
				spec.ID,
				database.CodeUnavailable,
				"database schema readiness is unavailable",
			)
		}
		if !ready {
			return migrationStatus(spec.ID)
		}
	}
	if contract.ImportHorizon != "" {
		closed, horizonErr := inspection.HasImportHorizon(ctx, contract.ImportHorizon)
		if horizonErr != nil {
			return unavailableStatus(
				spec.ID,
				database.CodeIntegrity,
				"database import readiness is invalid",
			)
		}
		if !closed {
			return migrationStatus(spec.ID)
		}
	}
	return database.StoreStatus{ID: spec.ID, Readiness: database.StoreReady}
}

func migrationStatus(id database.StoreID) database.StoreStatus {
	return database.StoreStatus{
		ID:        id,
		Readiness: database.StoreMigrationRequired,
		Error: database.NewError(
			database.CodeMigrationRequired,
			"database migration is required",
		),
	}
}

func unavailableStatus(
	id database.StoreID,
	code database.ErrorCode,
	message string,
) database.StoreStatus {
	readiness := database.StoreUnavailable
	if code == database.CodeIntegrity {
		readiness = database.StoreIntegrityFailed
	}
	return database.StoreStatus{
		ID: id, Readiness: readiness, Error: database.NewError(code, message),
	}
}

type generationSet struct {
	paths    map[string]struct{}
	physical map[fileidentity.Identity]struct{}
}

var errLegacyIntegrity = errors.New("database legacy input integrity failure")

type legacyDiscoveryLimits struct {
	maxEntries int
	maxFiles   int
	maxDepth   int
}

type legacyDiscoveryBudget struct {
	limits  legacyDiscoveryLimits
	entries int
	files   int
}

func defaultLegacyDiscoveryLimits() legacyDiscoveryLimits {
	return legacyDiscoveryLimits{
		maxEntries: legacyDiscoveryMaxEntries,
		maxFiles:   legacyDiscoveryMaxFiles,
		maxDepth:   legacyDiscoveryMaxDepth,
	}
}

func newLegacyDiscoveryBudget(limits legacyDiscoveryLimits) (*legacyDiscoveryBudget, error) {
	if limits.maxEntries < 1 || limits.maxFiles < 1 || limits.maxDepth < 0 {
		return nil, fmt.Errorf("%w: invalid discovery limits", errLegacyIntegrity)
	}
	return &legacyDiscoveryBudget{limits: limits}, nil
}

func (budget *legacyDiscoveryBudget) enter(depth int, regular bool) error {
	if budget == nil || depth < 0 {
		return fmt.Errorf("%w: invalid discovery budget", errLegacyIntegrity)
	}
	if depth > budget.limits.maxDepth {
		return fmt.Errorf("%w: discovery depth limit exceeded", errLegacyIntegrity)
	}
	if budget.entries >= budget.limits.maxEntries {
		return fmt.Errorf("%w: discovery entry limit exceeded", errLegacyIntegrity)
	}
	budget.entries++
	if regular {
		if budget.files >= budget.limits.maxFiles {
			return fmt.Errorf("%w: discovery file limit exceeded", errLegacyIntegrity)
		}
		budget.files++
	}
	return nil
}

func generationExclusions(specs []storecatalog.Spec) (generationSet, error) {
	set := generationSet{
		paths:    make(map[string]struct{}, len(specs)*4),
		physical: make(map[fileidentity.Identity]struct{}, len(specs)*4),
	}
	for _, spec := range specs {
		for _, path := range generationPaths(spec.Path) {
			clean := filepath.Clean(path)
			set.paths[generationPathKey(clean)] = struct{}{}
			info, statErr := os.Lstat(clean)
			if errors.Is(statErr, os.ErrNotExist) {
				continue
			}
			if statErr != nil {
				return generationSet{}, statErr
			}
			if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
				return generationSet{}, errors.New("database generation is unsafe")
			}
			identity, exists, err := fileidentity.Existing(clean)
			if err != nil {
				return generationSet{}, err
			}
			if !exists {
				return generationSet{}, errors.New("database generation changed during identity lookup")
			}
			after, statErr := os.Lstat(clean)
			if statErr != nil || !os.SameFile(info, after) || !after.Mode().IsRegular() ||
				after.Mode()&os.ModeSymlink != 0 {
				return generationSet{}, errors.Join(
					errors.New("database generation changed during identity lookup"), statErr,
				)
			}
			set.physical[identity] = struct{}{}
		}
	}
	return set, nil
}

func legacyInputExists(
	ctx context.Context,
	roots []string,
	exclusions generationSet,
) (bool, error) {
	return legacyInputExistsWithin(ctx, roots, exclusions, defaultLegacyDiscoveryLimits())
}

func legacyInputExistsWithin(
	ctx context.Context,
	roots []string,
	exclusions generationSet,
	limits legacyDiscoveryLimits,
) (bool, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	budget, err := newLegacyDiscoveryBudget(limits)
	if err != nil {
		return false, err
	}
	return legacyInputExistsWithBudget(ctx, roots, exclusions, budget)
}

func legacyInputExistsWithBudget(
	ctx context.Context,
	roots []string,
	exclusions generationSet,
	budget *legacyDiscoveryBudget,
) (bool, error) {
	if budget == nil {
		return false, fmt.Errorf("%w: invalid discovery budget", errLegacyIntegrity)
	}
	found := false
	for _, root := range roots {
		if err := ctx.Err(); err != nil {
			return false, err
		}
		if !validLegacyPath(root) {
			return false, fmt.Errorf("%w: invalid path", errLegacyIntegrity)
		}
		info, err := os.Lstat(root)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return false, err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return false, fmt.Errorf("%w: symlink", errLegacyIntegrity)
		}
		if info.Mode().IsRegular() {
			if err := budget.enter(0, true); err != nil {
				return false, err
			}
			match, matchErr := excludedLegacyFile(root, info, exclusions)
			if matchErr != nil {
				return false, matchErr
			}
			if !match {
				found = true
			}
			continue
		}
		if !info.IsDir() {
			return false, fmt.Errorf("%w: non-regular input", errLegacyIntegrity)
		}
		rootFound, walkErr := walkLegacyDirectory(ctx, root, info, 0, exclusions, budget)
		if walkErr != nil {
			return false, walkErr
		}
		found = found || rootFound
	}
	if err := ctx.Err(); err != nil {
		return false, err
	}
	return found, nil
}

func walkLegacyDirectory(
	ctx context.Context,
	path string,
	expected os.FileInfo,
	depth int,
	exclusions generationSet,
	budget *legacyDiscoveryBudget,
) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	if expected == nil || !expected.IsDir() || expected.Mode()&os.ModeSymlink != 0 {
		return false, fmt.Errorf("%w: unsafe directory", errLegacyIntegrity)
	}
	if err := budget.enter(depth, false); err != nil {
		return false, err
	}
	beforeIdentity, exists, identityErr := fileidentity.Existing(path)
	if identityErr != nil || !exists {
		return false, errors.Join(
			fmt.Errorf("%w: directory physical identity unavailable", errLegacyIntegrity),
			identityErr,
		)
	}

	directory, err := os.Open(path)
	if err != nil {
		return false, err
	}
	defer directory.Close()
	opened, err := directory.Stat()
	if err != nil {
		return false, err
	}
	current, err := os.Lstat(path)
	if err != nil {
		return false, err
	}
	if current.Mode()&os.ModeSymlink != 0 || !current.IsDir() ||
		!os.SameFile(expected, opened) || !os.SameFile(opened, current) {
		return false, fmt.Errorf("%w: directory identity changed", errLegacyIntegrity)
	}

	entries, err := readLegacyDirectory(ctx, directory, budget.limits.maxEntries-budget.entries)
	if err != nil {
		return false, err
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })

	found := false
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return false, err
		}
		if !validLegacyComponent(entry.Name()) {
			return false, fmt.Errorf("%w: invalid path component", errLegacyIntegrity)
		}
		entryInfo, infoErr := entry.Info()
		if infoErr != nil {
			return false, infoErr
		}
		if entryInfo.Mode()&os.ModeSymlink != 0 {
			return false, fmt.Errorf("%w: tree symlink", errLegacyIntegrity)
		}
		entryPath := filepath.Join(path, entry.Name())
		if entryInfo.IsDir() {
			if depth == 0 && (entry.Name() == "legacy-json" ||
				entry.Name() == "backups" || entry.Name() == ".database") {
				if err := budget.enter(depth+1, false); err != nil {
					return false, err
				}
				continue
			}
			childFound, childErr := walkLegacyDirectory(
				ctx, entryPath, entryInfo, depth+1, exclusions, budget,
			)
			if childErr != nil {
				return false, childErr
			}
			found = found || childFound
			continue
		}
		if !entryInfo.Mode().IsRegular() {
			return false, fmt.Errorf("%w: tree non-regular input", errLegacyIntegrity)
		}
		if err := budget.enter(depth+1, true); err != nil {
			return false, err
		}
		match, matchErr := excludedLegacyFile(entryPath, entryInfo, exclusions)
		if matchErr != nil {
			return false, matchErr
		}
		found = found || !match
	}
	after, err := os.Lstat(path)
	if err != nil || after == nil || !after.IsDir() || after.Mode()&os.ModeSymlink != 0 ||
		!os.SameFile(expected, after) {
		return false, errors.Join(
			fmt.Errorf("%w: directory identity changed during discovery", errLegacyIntegrity),
			err,
		)
	}
	afterIdentity, exists, identityErr := fileidentity.Existing(path)
	if identityErr != nil || !exists || afterIdentity != beforeIdentity ||
		!opened.ModTime().Equal(after.ModTime()) {
		return false, errors.Join(
			fmt.Errorf("%w: directory changed during discovery", errLegacyIntegrity),
			identityErr,
		)
	}
	return found, nil
}

func readLegacyDirectory(
	ctx context.Context,
	directory *os.File,
	remaining int,
) ([]os.DirEntry, error) {
	if directory == nil || remaining < 0 {
		return nil, fmt.Errorf("%w: invalid directory budget", errLegacyIntegrity)
	}
	entries := make([]os.DirEntry, 0, min(remaining, legacyDiscoveryReadBatch))
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		limit := min(legacyDiscoveryReadBatch, remaining-len(entries)+1)
		if limit < 1 {
			return nil, fmt.Errorf("%w: discovery entry limit exceeded", errLegacyIntegrity)
		}
		batch, err := directory.ReadDir(limit)
		entries = append(entries, batch...)
		if len(entries) > remaining {
			return nil, fmt.Errorf("%w: discovery entry limit exceeded", errLegacyIntegrity)
		}
		if len(batch) == 0 && err == nil {
			return nil, errors.New("database legacy directory read made no progress")
		}
		if errors.Is(err, io.EOF) {
			return entries, nil
		}
		if err != nil {
			return nil, err
		}
	}
}

func excludedLegacyFile(path string, info os.FileInfo, exclusions generationSet) (bool, error) {
	if _, exact := exclusions.paths[generationPathKey(path)]; exact {
		return true, nil
	}
	if info == nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return false, fmt.Errorf("%w: unsafe legacy file", errLegacyIntegrity)
	}
	current, err := os.Lstat(path)
	if err != nil || !os.SameFile(info, current) || !current.Mode().IsRegular() ||
		current.Mode()&os.ModeSymlink != 0 {
		return false, errors.Join(
			fmt.Errorf("%w: legacy file identity changed", errLegacyIntegrity), err,
		)
	}
	identity, exists, err := fileidentity.Existing(path)
	if err != nil || !exists {
		return false, errors.Join(
			fmt.Errorf("%w: legacy file identity unavailable", errLegacyIntegrity), err,
		)
	}
	after, err := os.Lstat(path)
	if err != nil || !os.SameFile(current, after) {
		return false, errors.Join(
			fmt.Errorf("%w: legacy file identity changed", errLegacyIntegrity), err,
		)
	}
	if _, aliasesGeneration := exclusions.physical[identity]; aliasesGeneration {
		return false, fmt.Errorf("%w: generation alias", errLegacyIntegrity)
	}
	return false, nil
}

func validLegacyPath(path string) bool {
	if path == "" || len(path) > legacyPathMaxBytes || !utf8.ValidString(path) ||
		strings.ContainsRune(path, 0) {
		return false
	}
	clean := filepath.Clean(path)
	for _, component := range strings.Split(clean, string(os.PathSeparator)) {
		if component == "" || component == "." || component == filepath.VolumeName(clean) {
			continue
		}
		if !validLegacyComponent(component) {
			return false
		}
	}
	return true
}

func validLegacyComponent(component string) bool {
	return component != "" && component != "." && component != ".." &&
		len(component) <= legacyComponentMaxBytes && utf8.ValidString(component) &&
		!strings.ContainsRune(component, 0) &&
		!strings.ContainsRune(component, os.PathSeparator)
}

func generationPaths(path string) []string {
	return []string{path, path + "-wal", path + "-shm", path + "-journal"}
}

func generationPathKey(path string) string {
	path = filepath.Clean(path)
	if runtime.GOOS == "windows" {
		return strings.ToLower(path)
	}
	return path
}
