// Package databaseclaims acquires process-external ownership of every physical
// namespace in a trusted store catalog. It is infrastructure-only so the
// public database protocol never depends on physical catalog construction.
package databaseclaims

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/sipeed/picoclaw/internal/fileidentity"
	"github.com/sipeed/picoclaw/internal/storecatalog"
	"github.com/sipeed/picoclaw/pkg/database"
)

const (
	physicalClaimIdentityVersion = "picoclaw/database-physical-object-claim/v1\x00"
	maxCatalogClaimPaths         = 32 * 1024
	maxLeaseClaims               = 64 * 1024
	maxLeaseResources            = maxLeaseClaims + maxCatalogClaimPaths/4 + 1
)

// Lease holds all physical claims for one immutable, fully validated catalog.
type Lease struct {
	mu                sync.RWMutex
	home              string
	root              string
	catalog           *storecatalog.Catalog
	stores            []storecatalog.Spec
	byID              map[database.StoreID]int
	claims            []claimHandle
	resources         []claimHandle
	acquireClaim      func(string, string) (claimHandle, error)
	identities        map[string]struct{}
	catalogIdentities map[string]struct{}
	lexicalIdentities []string
	observations      []memberObservation
	assignments       map[string]string
	pinAssignments    map[string]string
	replacementPins   map[database.StoreID]*replacementPin
	approvedMains     map[database.StoreID]fileidentity.Identity
	fence             *database.Fence
	reviewScope       *reviewScopeLeaseState
	closed            atomic.Bool
	poisoned          atomic.Bool
	once              sync.Once
}

type claimHandle interface {
	close() error
}

type claimValidator interface {
	valid() bool
}

type replacementHandle interface {
	close() error
	valid() bool
	matches(identity fileidentity.Identity) bool
}

type replacementPin struct {
	identity      fileidentity.Identity
	claimIdentity string
	path          string
	handle        replacementHandle
}

func (pin *replacementPin) close() error {
	if pin == nil || pin.handle == nil {
		return nil
	}
	handle := pin.handle
	pin.handle = nil
	return handle.close()
}

func (lease *Lease) removePinResource(target *replacementPin) {
	for index, resource := range lease.resources {
		pin, ok := resource.(*replacementPin)
		if ok && pin == target {
			copy(lease.resources[index:], lease.resources[index+1:])
			lease.resources[len(lease.resources)-1] = nil
			lease.resources = lease.resources[:len(lease.resources)-1]
			return
		}
	}
}

type memberRole string

const (
	memberMain    memberRole = "main"
	memberWAL     memberRole = "wal"
	memberSHM     memberRole = "shm"
	memberJournal memberRole = "journal"
	memberLegacy  memberRole = "legacy"
)

type memberType string

const (
	memberRegular   memberType = "regular"
	memberDirectory memberType = "directory"
)

// memberObservation binds an opaque physical identity to its exact logical
// assignment. Keeping the assignment prevents a flattened, deduplicated claim
// set from hiding hard links between two catalog members.
type memberObservation struct {
	StoreID       database.StoreID
	Role          memberRole
	Path          string
	Type          memberType
	Identity      fileidentity.Identity
	ClaimIdentity string
}

// claimAcquireOps keeps defensive transition fault injection local to one
// private call. Production uses only the exact catalog, fence, and lock
// operations returned below.
type claimAcquireOps struct {
	authorizes func(*database.Fence, string) bool
	project    func(storecatalog.Options) (*storecatalog.Catalog, error)
	claimIDs   func(*storecatalog.Catalog) ([]storecatalog.ClaimID, error)
	lexical    func([]storecatalog.ClaimID) ([]string, error)
	claimRoot  func() (string, error)
	acquire    func(string, string) (claimHandle, error)
	observe    func(*storecatalog.Catalog) ([]memberObservation, error)
	revalidate func(*storecatalog.Catalog) (*storecatalog.Catalog, error)
}

type claimRefreshOps struct {
	revalidate       func(*storecatalog.Catalog) (*storecatalog.Catalog, error)
	observe          func(*storecatalog.Catalog) ([]memberObservation, error)
	acquire          func(string, string) (claimHandle, error)
	guardedAuthority func() bool
}

func defaultClaimRefreshOps() claimRefreshOps {
	return claimRefreshOps{
		revalidate:       storecatalog.Revalidate,
		observe:          observeCatalog,
		acquire:          acquireClaim,
		guardedAuthority: func() bool { return true },
	}
}

func defaultClaimAcquireOps() claimAcquireOps {
	return claimAcquireOps{
		authorizes: func(fence *database.Fence, home string) bool { return fence.Authorizes(home) },
		project:    storecatalog.Project,
		claimIDs:   func(catalog *storecatalog.Catalog) ([]storecatalog.ClaimID, error) { return catalog.ClaimIDs() },
		lexical:    checkedLexicalClaimIdentities,
		claimRoot:  prepareClaimRoot,
		acquire:    acquireClaim,
		observe:    observeCatalog,
		revalidate: storecatalog.Revalidate,
	}
}

// Acquire projects every candidate namespace, acquires its central lock, then
// performs strict catalog construction. Callers must already hold an online or
// migration fence for the storage home.
func Acquire(options storecatalog.Options, fence *database.Fence) (*Lease, error) {
	return acquireCatalogWithOps(options, nil, fence, defaultClaimAcquireOps())
}

// AcquireProjected claims and strictly revalidates an immutable projected
// inventory without retaining or rereading its former configuration object.
func AcquireProjected(projected *storecatalog.Catalog, fence *database.Fence) (*Lease, error) {
	if projected == nil {
		return nil, database.NewError(database.CodeInvalid, "physical database catalog is unavailable")
	}
	return acquireCatalogWithOps(
		storecatalog.Options{Home: projected.Home()}, projected, fence, defaultClaimAcquireOps(),
	)
}

// PrepareRootForTesting canonicalizes and, where required by the platform,
// makes an existing test-owned directory an exact private claim root.
// Repository architecture tests prohibit production callers.
func PrepareRootForTesting(root string) (string, error) {
	if err := requireTestingProcess(testing.Testing()); err != nil {
		return "", err
	}
	return prepareExplicitTestClaimRoot(root)
}

// AcquireForTesting routes real claim locks to an explicit private test root.
// It is runtime-gated to Go test binaries, and repository architecture tests
// require every caller to live in a _test.go file.
func AcquireForTesting(
	options storecatalog.Options,
	projected *storecatalog.Catalog,
	fence *database.Fence,
	root string,
) (*Lease, error) {
	if err := requireTestingProcess(testing.Testing()); err != nil {
		return nil, err
	}
	canonical, err := validateExplicitTestClaimRoot(root)
	if err != nil {
		return nil, err
	}
	if projected != nil {
		options = storecatalog.Options{Home: projected.Home()}
	}
	ops := defaultClaimAcquireOps()
	ops.claimRoot = func() (string, error) { return canonical, nil }
	ops.acquire = acquireClaimForTesting
	return acquireCatalogWithOps(options, projected, fence, ops)
}

func requireTestingProcess(running bool) error {
	if running {
		return nil
	}
	return database.NewError(database.CodeUnauthorized, "test-only physical claim operation is unavailable")
}

func acquireWithOps(
	options storecatalog.Options,
	fence *database.Fence,
	ops claimAcquireOps,
) (*Lease, error) {
	return acquireCatalogWithOps(options, nil, fence, ops)
}

func acquireCatalogWithOps(
	options storecatalog.Options,
	projected *storecatalog.Catalog,
	fence *database.Fence,
	ops claimAcquireOps,
) (*Lease, error) {
	return acquireOwnedCatalogWithOps(options, projected, fence, ops, nil)
}

func acquireOwnedCatalogWithOps(
	options storecatalog.Options,
	projected *storecatalog.Catalog,
	fence *database.Fence,
	ops claimAcquireOps,
	reviewScope *reviewScopeLeaseState,
) (*Lease, error) {
	if ops.authorizes == nil || ops.project == nil || ops.claimIDs == nil ||
		ops.lexical == nil || ops.claimRoot == nil || ops.acquire == nil ||
		ops.observe == nil || ops.revalidate == nil {
		return nil, database.NewError(database.CodeIntegrity, "physical database claim operations are invalid")
	}
	if !ops.authorizes(fence, options.Home) {
		return nil, database.NewError(
			database.CodeUnauthorized,
			"physical database claims require an existing storage fence",
		)
	}
	if projected == nil {
		var err error
		projected, err = ops.project(options)
		if err != nil {
			return nil, err
		}
	}
	if reviewScope != nil {
		var err error
		projected, err = reviewScope.revalidateSelected(projected)
		if err != nil {
			return nil, err
		}
		ops.revalidate = reviewScope.revalidateSelected
	}
	identities, err := ops.claimIDs(projected)
	if err != nil {
		return nil, err
	}
	lexicalIdentities, err := ops.lexical(identities)
	if err != nil {
		return nil, err
	}
	candidateObservation, err := ops.observe(projected)
	if err != nil {
		return nil, err
	}
	claimIdentities := append(
		append([]string(nil), lexicalIdentities...),
		observationClaimIdentities(candidateObservation)...,
	)
	sort.Strings(claimIdentities)
	root, err := ops.claimRoot()
	if err != nil {
		return nil, err
	}

	lease := &Lease{
		claims:      make([]claimHandle, 0, len(lexicalIdentities)),
		reviewScope: reviewScope,
	}
	fail := func(primary error) (*Lease, error) {
		return nil, errors.Join(primary, lease.Close())
	}
	held := make(map[string]struct{}, len(lexicalIdentities))
	acquire := func(claimIdentities []string) error {
		for _, identity := range claimIdentities {
			if _, duplicate := held[identity]; duplicate {
				continue
			}
			if len(held) >= maxLeaseClaims {
				return database.NewError(database.CodeInvalid, "physical database claim limit is exceeded")
			}
			if !validClaimIdentity(identity) {
				return database.NewError(database.CodeIntegrity, "physical database claim identity is invalid")
			}
			if !ops.authorizes(fence, options.Home) {
				return database.NewError(database.CodeUnauthorized, "storage fence expired while acquiring claims")
			}
			claim, claimErr := ops.acquire(root, identity)
			if errors.Is(claimErr, errClaimBusy) {
				return database.NewError(database.CodeConflict, "physical database store is already owned")
			}
			if claimErr != nil {
				return fmt.Errorf("claim physical database store: %w", claimErr)
			}
			if claim == nil {
				return database.NewError(database.CodeIntegrity, "physical database claim handle is unavailable")
			}
			lease.claims = append(lease.claims, claim)
			lease.resources = append(lease.resources, claim)
			held[identity] = struct{}{}
		}
		return nil
	}
	if acquireErr := acquire(claimIdentities); acquireErr != nil {
		return fail(acquireErr)
	}

	// Revalidation consumes the detached projection rather than rereading the
	// caller's mutable configuration. The resulting strict catalog is the sole
	// inventory retained by the lease.
	built, err := ops.revalidate(projected)
	if err != nil {
		return fail(err)
	}
	builtIdentities, err := ops.claimIDs(built)
	if err != nil {
		return fail(err)
	}
	builtLexicalIdentities, err := ops.lexical(builtIdentities)
	if err != nil {
		return fail(err)
	}
	firstObservation, err := ops.observe(built)
	if err != nil {
		return fail(err)
	}
	if !slices.Equal(lexicalIdentities, builtLexicalIdentities) ||
		!observationsEqual(candidateObservation, firstObservation) ||
		!catalogsEqual(projected, built) {
		return fail(database.NewError(
			database.CodeIntegrity,
			"database catalog changed while acquiring physical claims",
		))
	}
	secondObservation, err := ops.observe(built)
	if err != nil || !observationsEqual(firstObservation, secondObservation) {
		return fail(errors.Join(
			database.NewError(
				database.CodeIntegrity,
				"database physical identities changed while acquiring claims",
			),
			err,
		))
	}
	finalCatalog, err := ops.revalidate(built)
	if err != nil || !catalogsEqual(built, finalCatalog) {
		return fail(errors.Join(
			database.NewError(database.CodeIntegrity, "database catalog changed after physical observation"),
			err,
		))
	}
	finalObservation, err := ops.observe(finalCatalog)
	if err != nil || !observationsEqual(secondObservation, finalObservation) {
		return fail(errors.Join(
			database.NewError(database.CodeIntegrity, "database physical identities changed after final validation"),
			err,
		))
	}
	if !allObservationClaimsHeld(held, lexicalIdentities, finalObservation) {
		return fail(database.NewError(
			database.CodeIntegrity,
			"database physical identity was not locked during claim acquisition",
		))
	}
	if !claimHandlesValid(lease.claims) {
		return fail(database.NewError(database.CodeIntegrity, "physical database claim lock lost authority"))
	}
	if !ops.authorizes(fence, finalCatalog.Home()) {
		return fail(database.NewError(database.CodeUnauthorized, "storage fence expired while acquiring claims"))
	}
	lease.home = finalCatalog.Home()
	lease.root = root
	lease.catalog = finalCatalog
	lease.fence = fence
	lease.identities = held
	lease.acquireClaim = ops.acquire
	lease.lexicalIdentities = append([]string(nil), lexicalIdentities...)
	lease.observations = cloneObservations(finalObservation)
	lease.assignments = make(map[string]string, len(finalObservation))
	lease.pinAssignments = make(map[string]string)
	lease.catalogIdentities = make(map[string]struct{}, len(held))
	for _, observation := range finalObservation {
		lease.catalogIdentities[observation.ClaimIdentity] = struct{}{}
		lease.assignments[observation.Identity.String()] = observationMemberKey(observation)
	}
	lease.replacementPins = make(map[database.StoreID]*replacementPin)
	lease.stores = finalCatalog.All()
	lease.byID = make(map[database.StoreID]int, len(lease.stores))
	lease.approvedMains = make(map[database.StoreID]fileidentity.Identity, len(lease.stores))
	for index := range lease.stores {
		id := lease.stores[index].ID
		lease.byID[id] = index
		lease.approvedMains[id] = fileidentity.Identity{}
	}
	for _, observation := range finalObservation {
		if observation.Role == memberMain {
			lease.approvedMains[observation.StoreID] = observation.Identity
		}
	}
	if err := lease.validateReviewScopeLocked(true, "", true); err != nil {
		return fail(err)
	}
	lease.adoptReviewScopeSelectedLocked()
	return lease, nil
}

// Home returns the canonical home bound to this lease.
func (lease *Lease) Home() string {
	release, err := lease.lockValidatedExposure()
	if err != nil {
		return ""
	}
	defer release()
	return lease.home
}

// Stores returns a detached, ID-sorted copy of every claimed store.
func (lease *Lease) Stores() []storecatalog.Spec {
	release, err := lease.lockValidatedExposure()
	if err != nil {
		return nil
	}
	defer release()
	stores := make([]storecatalog.Spec, len(lease.stores))
	for index := range lease.stores {
		stores[index] = cloneSpec(lease.stores[index])
	}
	return stores
}

// Lookup returns a detached claimed store for one typed logical ID.
func (lease *Lease) Lookup(id database.StoreID) (storecatalog.Spec, bool) {
	release, err := lease.lockValidatedExposure()
	if err != nil {
		return storecatalog.Spec{}, false
	}
	defer release()
	index, ok := lease.byID[id]
	if !ok {
		return storecatalog.Spec{}, false
	}
	return cloneSpec(lease.stores[index]), true
}

// Close releases all physical claims in reverse acquisition order. It is safe
// to call more than once.
func (lease *Lease) Close() error {
	if lease == nil {
		return nil
	}
	var result error
	lease.once.Do(func() {
		lease.mu.Lock()
		defer lease.mu.Unlock()
		lease.closed.Store(true)
		clear(lease.replacementPins)
		for index := len(lease.resources) - 1; index >= 0; index-- {
			result = errors.Join(result, lease.resources[index].close())
		}
		lease.resources = nil
		lease.claims = nil
		lease.approvedMains = nil
	})
	return result
}

// Check verifies that the retained fence and every replaceable claim pathname
// still name the exact locked objects. Failure permanently poisons the lease.
func (lease *Lease) Check() error {
	release, err := lease.lockValidatedExposure()
	if err != nil {
		return err
	}
	release()
	return nil
}

// Guard holds the lease live across one short ownership transfer. The returned
// release function must be called exactly once when err is nil.
func (lease *Lease) Guard() (func(), error) {
	releaseLease, err := lease.lockValidatedExposure()
	if err != nil {
		return nil, err
	}
	guardedFence, releaseFence, err := lease.fence.GuardChecked(lease.home)
	if err != nil {
		lease.poisoned.Store(true)
		releaseLease()
		return nil, errors.Join(database.NewError(
			database.CodeIntegrity,
			"physical database lease lost fence authority",
		), err)
	}
	return func() {
		if lease.closed.Load() || lease.poisoned.Load() ||
			!claimHandlesValid(lease.claims) || !lease.replacementPinsValid() ||
			!guardedFence() {
			lease.poisoned.Store(true)
		} else if scopeErr := lease.validateReviewScopeLocked(true, "", true); scopeErr == nil {
			lease.adoptReviewScopeSelectedLocked()
		}
		releaseFence()
		releaseLease()
	}, nil
}

// GuardStores returns a detached catalog while retaining the same live guard
// until release. It avoids a second RWMutex acquisition after a writer queues.
func (lease *Lease) GuardStores() ([]storecatalog.Spec, func(), error) {
	release, err := lease.Guard()
	if err != nil {
		return nil, nil, err
	}
	stores := make([]storecatalog.Spec, len(lease.stores))
	for index := range lease.stores {
		stores[index] = cloneSpec(lease.stores[index])
	}
	return stores, release, nil
}

// GuardStoresRefreshing exclusively holds the lease and fence across a short
// filesystem operation. It refreshes physical identities before returning the
// detached catalog; reconcile must be called before release to claim members
// materialized by that operation and to reject replacement of existing ones.
// The returned functions are non-reentrant and release must be called exactly
// once when err is nil.
func (lease *Lease) GuardStoresRefreshing() (
	stores []storecatalog.Spec,
	reconcile func() error,
	release func(),
	err error,
) {
	if lease == nil {
		return nil, nil, nil, database.NewError(
			database.CodeUnavailable,
			"physical database lease is unavailable",
		)
	}
	lease.mu.Lock()
	if !lease.authorized() || lease.root == "" || lease.catalog == nil {
		lease.mu.Unlock()
		return nil, nil, nil, database.NewError(
			database.CodeIntegrity,
			"physical database lease lost authority",
		)
	}
	if err := lease.validateReviewScopeLocked(true, "", true); err != nil {
		lease.mu.Unlock()
		return nil, nil, nil, err
	}
	lease.adoptReviewScopeSelectedLocked()
	guardedFence, releaseFence, guardErr := lease.fence.GuardChecked(lease.home)
	if guardErr != nil {
		lease.poisoned.Store(true)
		lease.mu.Unlock()
		return nil, nil, nil, errors.Join(database.NewError(
			database.CodeIntegrity,
			"physical database lease lost fence authority",
		), guardErr)
	}
	var callbackMu sync.Mutex
	active := true
	releaseAll := func() {
		callbackMu.Lock()
		defer callbackMu.Unlock()
		if !active {
			return
		}
		active = false
		if lease.closed.Load() || lease.poisoned.Load() ||
			!claimHandlesValid(lease.claims) || !lease.replacementPinsValid() ||
			!guardedFence() {
			lease.poisoned.Store(true)
		} else if scopeErr := lease.validateReviewScopeLocked(true, "", true); scopeErr == nil {
			lease.adoptReviewScopeSelectedLocked()
		}
		releaseFence()
		lease.mu.Unlock()
	}
	ops := defaultClaimRefreshOps()
	if lease.acquireClaim != nil {
		ops.acquire = lease.acquireClaim
	}
	ops.guardedAuthority = guardedFence
	if refreshErr := lease.refreshLockedWithOps("", ops); refreshErr != nil {
		releaseAll()
		return nil, nil, nil, refreshErr
	}
	stores = make([]storecatalog.Spec, len(lease.stores))
	for index := range lease.stores {
		stores[index] = cloneSpec(lease.stores[index])
	}
	reconcile = func() error {
		callbackMu.Lock()
		defer callbackMu.Unlock()
		if !active || lease.closed.Load() || lease.poisoned.Load() ||
			!claimHandlesValid(lease.claims) || !lease.replacementPinsValid() ||
			!guardedFence() {
			lease.poisoned.Store(true)
			return database.NewError(
				database.CodeIntegrity,
				"physical database refreshing guard is unavailable",
			)
		}
		return lease.refreshLockedWithOps("", ops)
	}
	return stores, reconcile, releaseAll, nil
}

// MigrationRefreshingGuard holds one migration fence and the lease's
// exclusive mutex continuously across an offline filesystem operation. Its
// methods are the only reentrant-safe way to reconcile or authorize a staged
// main replacement while that ownership boundary is held.
type MigrationRefreshingGuard struct {
	state *migrationRefreshingGuardState
}

type migrationRefreshingGuardState struct {
	mu            sync.Mutex
	lease         *Lease
	stores        []storecatalog.Spec
	ops           claimRefreshOps
	releaseFence  func()
	pinned        map[database.StoreID]struct{}
	expectedMain  map[database.StoreID]fileidentity.Identity
	active        bool
	releaseErr    error
	providerChild *providerLeaseChild
	releasing     bool
	releaseDone   chan struct{}
}

// GuardStoresMigrating acquires a migration-only refreshing guard and returns
// only after the complete catalog has been reconciled under the same live
// fence. Release must be called exactly once and its error must be observed.
func (lease *Lease) GuardStoresMigrating() (*MigrationRefreshingGuard, error) {
	if lease == nil {
		return nil, database.NewError(
			database.CodeUnavailable,
			"physical database lease is unavailable",
		)
	}
	lease.mu.Lock()
	if !lease.authorized() || lease.root == "" || lease.catalog == nil ||
		lease.acquireClaim == nil || len(lease.replacementPins) != 0 ||
		!approvedMainBaselineValid(lease.stores, lease.approvedMains) {
		lease.poisoned.Store(true)
		lease.mu.Unlock()
		return nil, database.NewError(
			database.CodeIntegrity,
			"physical database lease lost authority",
		)
	}
	if err := lease.validateReviewScopeLocked(false, "", true); err != nil {
		lease.mu.Unlock()
		return nil, err
	}
	guardedFence, releaseFence, guardErr := lease.fence.GuardMigrationChecked(lease.home)
	if guardErr != nil {
		lease.poisoned.Store(true)
		lease.mu.Unlock()
		return nil, guardErr
	}
	ops := defaultClaimRefreshOps()
	ops.acquire = lease.acquireClaim
	ops.guardedAuthority = guardedFence
	expectedMain := make(map[database.StoreID]fileidentity.Identity, len(lease.approvedMains))
	for id, identity := range lease.approvedMains {
		expectedMain[id] = identity
	}
	state := &migrationRefreshingGuardState{
		lease: lease, ops: ops, releaseFence: releaseFence,
		pinned: make(map[database.StoreID]struct{}), expectedMain: expectedMain,
		active: true, releaseDone: make(chan struct{}),
	}
	fail := func(err error) (*MigrationRefreshingGuard, error) {
		releaseFence()
		lease.mu.Unlock()
		return nil, err
	}
	// Validate the lease's approved main baseline before ordinary refresh can
	// adopt newly materialized catalog members. The second validation below
	// closes the observation/refresh window while the same lease and fence
	// remain continuously held.
	if err := state.validateExpectedMainsLocked(); err != nil {
		return fail(err)
	}
	if refreshErr := lease.refreshLockedWithOps("", ops); refreshErr != nil {
		return fail(refreshErr)
	}
	if err := state.validateFinalMigrationObservationsLocked(); err != nil {
		return fail(err)
	}
	stores := make([]storecatalog.Spec, len(lease.stores))
	for index := range lease.stores {
		stores[index] = cloneSpec(lease.stores[index])
	}
	state.stores = stores
	return &MigrationRefreshingGuard{state: state}, nil
}

// Stores returns a detached, complete catalog only while the migration guard
// retains the exact lease and fence authority.
func (guard *MigrationRefreshingGuard) Stores() ([]storecatalog.Spec, error) {
	if guard == nil || guard.state == nil {
		return nil, database.NewError(
			database.CodeUnavailable,
			"physical database migration guard is unavailable",
		)
	}
	state := guard.state
	state.mu.Lock()
	defer state.mu.Unlock()
	if err := state.checkLocked(); err != nil {
		return nil, err
	}
	stores := make([]storecatalog.Spec, len(state.stores))
	for index := range state.stores {
		stores[index] = cloneSpec(state.stores[index])
	}
	return stores, nil
}

// Check revalidates the retained fence and every held claim/pin without
// releasing the exclusive migration boundary.
func (guard *MigrationRefreshingGuard) Check() error {
	if guard == nil || guard.state == nil {
		return database.NewError(
			database.CodeUnavailable,
			"physical database migration guard is unavailable",
		)
	}
	state := guard.state
	state.mu.Lock()
	defer state.mu.Unlock()
	return state.checkLocked()
}

// Reconcile claims non-main members materialized by the preceding provider or
// filesystem operation. Every missing-main appearance or existing-main change
// is rejected; those transitions require PinReplacement followed by
// ReconcileReplacement.
func (guard *MigrationRefreshingGuard) Reconcile() error {
	if guard == nil || guard.state == nil {
		return database.NewError(
			database.CodeUnavailable,
			"physical database migration guard is unavailable",
		)
	}
	state := guard.state
	state.mu.Lock()
	defer state.mu.Unlock()
	if err := state.checkForReconcileLocked(""); err != nil {
		return err
	}
	if err := state.validateExpectedMainsLocked(); err != nil {
		return err
	}
	if err := state.lease.refreshLockedWithOps("", state.ops); err != nil {
		return err
	}
	return state.validateFinalMigrationObservationsLocked()
}

// PinReplacement binds one exact staged main identity to its canonical
// StoreID without attempting to reacquire the already-held lease mutex.
func (guard *MigrationRefreshingGuard) PinReplacement(
	id database.StoreID,
	path string,
) error {
	if guard == nil || guard.state == nil || !id.Valid() || path == "" {
		return database.NewError(
			database.CodeInvalid,
			"physical database migration replacement pin is invalid",
		)
	}
	state := guard.state
	state.mu.Lock()
	defer state.mu.Unlock()
	if err := state.checkForReconcileLocked(""); err != nil {
		return err
	}
	if err := state.validateExpectedMainsLocked(); err != nil {
		return err
	}
	index, claimed := state.lease.byID[id]
	if !claimed {
		return database.NewError(database.CodeInvalid, "replacement store is not claimed")
	}
	if err := state.lease.pinReplacementLocked(id, path, index, state.ops); err != nil {
		return err
	}
	state.pinned[id] = struct{}{}
	return state.validateFinalMigrationObservationsLocked()
}

// CheckReplacement proves that path still names the exact main identity held
// by the active replacement pin for id. Unlike PinReplacement it cannot mint,
// replace, or refresh a capability.
func (guard *MigrationRefreshingGuard) CheckReplacement(
	id database.StoreID,
	path string,
) error {
	if guard == nil || guard.state == nil || !id.Valid() || path == "" {
		return database.NewError(
			database.CodeInvalid,
			"physical database migration replacement check is invalid",
		)
	}
	state := guard.state
	state.mu.Lock()
	defer state.mu.Unlock()
	if err := state.checkLocked(); err != nil {
		return err
	}
	if err := state.validateExpectedMainsLocked(); err != nil {
		return err
	}
	if _, pinned := state.pinned[id]; !pinned {
		return database.NewError(
			database.CodeIntegrity,
			"physical database migration replacement was not pinned",
		)
	}
	if err := state.lease.checkReplacementLocked(id, path); err != nil {
		return err
	}
	return state.validateFinalMigrationObservationsLocked()
}

// DiscardReplacement retires an exact unused replacement pin before its
// provider-owned stage is removed. A target with no published pin is an
// idempotent no-op; physical claims and assignment history remain monotonic.
func (guard *MigrationRefreshingGuard) DiscardReplacement(id database.StoreID) error {
	if guard == nil || guard.state == nil || !id.Valid() {
		return database.NewError(
			database.CodeInvalid,
			"physical database migration replacement discard is invalid",
		)
	}
	state := guard.state
	state.mu.Lock()
	defer state.mu.Unlock()
	if err := state.checkLocked(); err != nil {
		return err
	}
	if err := state.validateExpectedMainsLocked(); err != nil {
		return err
	}
	if _, claimed := state.lease.byID[id]; !claimed {
		return database.NewError(database.CodeInvalid, "replacement store is not claimed")
	}
	if _, pinned := state.pinned[id]; !pinned {
		return nil
	}
	if err := state.lease.retireReplacementPinsLocked(
		map[database.StoreID]struct{}{id: {}},
	); err != nil {
		return err
	}
	delete(state.pinned, id)
	return state.validateFinalMigrationObservationsLocked()
}

// ReconcileReplacement proves that the active main now names the exact pinned
// staged identity, promotes that assignment, and consumes its retained pin.
func (guard *MigrationRefreshingGuard) ReconcileReplacement(id database.StoreID) error {
	if guard == nil || guard.state == nil || !id.Valid() {
		return database.NewError(
			database.CodeInvalid,
			"physical database migration replacement is invalid",
		)
	}
	state := guard.state
	state.mu.Lock()
	defer state.mu.Unlock()
	if err := state.checkForReconcileLocked(id); err != nil {
		return err
	}
	if _, pinned := state.pinned[id]; !pinned {
		return state.lease.poison(database.NewError(
			database.CodeIntegrity,
			"physical database migration replacement was not pinned",
		))
	}
	pin := state.lease.replacementPins[id]
	if pin == nil || !pin.identity.Valid() {
		return state.lease.poison(database.NewError(
			database.CodeIntegrity,
			"physical database migration replacement pin is unavailable",
		))
	}
	replacementIdentity := pin.identity
	if err := state.lease.refreshLockedWithOps(id, state.ops); err != nil {
		return err
	}
	state.expectedMain[id] = replacementIdentity
	delete(state.pinned, id)
	return state.validateFinalMigrationObservationsLocked()
}

// Release first revokes and drains its registered provider child without
// dropping the migration fence or lease mutex, then performs one final ordinary
// reconciliation, retires every unused stage pin, and releases ownership. It
// is idempotent so deferred cleanup can safely join an earlier explicit result.
func (guard *MigrationRefreshingGuard) Release() error {
	if guard == nil || guard.state == nil {
		return nil
	}
	state := guard.state
	state.mu.Lock()
	if !state.active {
		result := state.releaseErr
		state.mu.Unlock()
		return result
	}
	if state.releasing {
		done := state.releaseDone
		state.mu.Unlock()
		<-done
		state.mu.Lock()
		result := state.releaseErr
		state.mu.Unlock()
		return result
	}
	state.releasing = true
	child := state.providerChild
	state.mu.Unlock()

	var childDrainErr error
	if child != nil {
		childDrainErr = child.drain(context.Background(), errProviderGuardReleased)
	}

	state.mu.Lock()
	defer state.mu.Unlock()
	state.releasing = false
	state.releaseErr = errors.Join(state.releaseErr, childDrainErr)
	lease := state.lease
	if checkErr := state.checkForReconcileLocked(""); checkErr != nil {
		state.releaseErr = errors.Join(state.releaseErr, checkErr)
	} else if stateErr := state.validateExpectedMainsLocked(); stateErr != nil {
		state.releaseErr = errors.Join(state.releaseErr, stateErr)
	} else {
		state.releaseErr = errors.Join(
			state.releaseErr,
			lease.refreshLockedWithOps("", state.ops),
		)
		if state.releaseErr == nil {
			state.releaseErr = state.validateFinalMigrationObservationsLocked()
		}
	}
	state.releaseErr = errors.Join(
		state.releaseErr,
		lease.retireReplacementPinsLocked(state.pinned),
	)
	clear(state.pinned)
	finalCheckErr := state.checkLocked()
	state.releaseErr = errors.Join(state.releaseErr, finalCheckErr)
	if finalCheckErr == nil {
		state.releaseErr = errors.Join(
			state.releaseErr,
			state.validateFinalMigrationObservationsLocked(),
		)
	}
	state.active = false
	state.releaseFence()
	lease.mu.Unlock()
	state.releaseFence = nil
	state.lease = nil
	state.stores = nil
	state.expectedMain = nil
	close(state.releaseDone)
	return state.releaseErr
}

func (state *migrationRefreshingGuardState) checkLocked() error {
	return state.checkReviewScopeLocked(true, "")
}

// checkForReconcileLocked validates every selected transition while allowing
// only unclaimed non-main members that the immediately following refresh will
// claim. A nonzero replacement permits exactly its active pinned main.
func (state *migrationRefreshingGuardState) checkForReconcileLocked(
	replacement database.StoreID,
) error {
	return state.checkReviewScopeLocked(false, replacement)
}

func (state *migrationRefreshingGuardState) checkReviewScopeLocked(
	requireSelectedClaims bool,
	replacement database.StoreID,
) error {
	if state == nil || !state.active || state.lease == nil {
		return database.NewError(
			database.CodeIntegrity,
			"physical database migration guard lost authority",
		)
	}
	if state.releasing {
		return database.NewError(
			database.CodeUnavailable,
			"physical database migration guard is releasing",
		)
	}
	lease := state.lease
	if state.ops.guardedAuthority == nil || !state.ops.guardedAuthority() {
		lease.poisoned.Store(true)
		return database.NewError(
			database.CodeIntegrity,
			"physical database migration guard lost authority",
		)
	}
	if lease.closed.Load() || lease.poisoned.Load() ||
		!claimHandlesValid(lease.claims) || !lease.replacementPinsValid() {
		lease.poisoned.Store(true)
		return database.NewError(
			database.CodeIntegrity,
			"physical database migration guard lost authority",
		)
	}
	if !replacement.IsZero() {
		if _, claimed := lease.byID[replacement]; !claimed {
			return database.NewError(database.CodeInvalid, "replacement store is not claimed")
		}
	}
	if err := lease.validateReviewScopeLocked(
		requireSelectedClaims,
		replacement,
		true,
	); err != nil {
		return err
	}
	if requireSelectedClaims {
		lease.adoptReviewScopeSelectedLocked()
	}
	return nil
}

func (state *migrationRefreshingGuardState) validateExpectedMainsLocked() error {
	return state.validateMigrationObservationsLocked(false)
}

func (state *migrationRefreshingGuardState) validateFinalMigrationObservationsLocked() error {
	return state.validateMigrationObservationsLocked(true)
}

func (state *migrationRefreshingGuardState) validateMigrationObservationsLocked(
	requireClaimed bool,
) error {
	if state == nil || state.lease == nil ||
		len(state.expectedMain) != len(state.lease.stores) ||
		!approvedMainBaselineValid(state.lease.stores, state.lease.approvedMains) ||
		state.ops.revalidate == nil || state.ops.observe == nil {
		if state != nil && state.lease != nil {
			state.lease.poisoned.Store(true)
		}
		return database.NewError(
			database.CodeIntegrity,
			"physical database migration main-state guard is invalid",
		)
	}
	for id, expected := range state.expectedMain {
		approved, ok := state.lease.approvedMains[id]
		if !ok || approved != expected {
			return state.lease.poison(database.NewError(
				database.CodeIntegrity,
				"database migration approved main baseline changed unexpectedly",
			))
		}
	}
	var observed []memberObservation
	if state.lease.reviewScope != nil {
		if err := state.lease.validateReviewScopeLocked(
			requireClaimed,
			"",
			true,
		); err != nil {
			return err
		}
		observed = cloneObservations(state.lease.reviewScope.selectedObservation)
		if requireClaimed {
			state.lease.adoptReviewScopeSelectedLocked()
		}
	} else {
		strict, err := state.ops.revalidate(state.lease.catalog)
		if err != nil || !catalogsEqual(state.lease.catalog, strict) {
			return state.lease.poison(errors.Join(
				database.NewError(
					database.CodeIntegrity,
					"database catalog changed while validating migration state",
				),
				err,
			))
		}
		observed, err = state.ops.observe(strict)
		if err != nil {
			return state.lease.poison(err)
		}
	}
	current := make(map[database.StoreID]fileidentity.Identity, len(state.expectedMain))
	for _, observation := range observed {
		if observation.Role != memberMain {
			continue
		}
		if _, duplicate := current[observation.StoreID]; duplicate {
			return state.lease.poison(database.NewError(
				database.CodeIntegrity,
				"database migration observed duplicate main assignments",
			))
		}
		current[observation.StoreID] = observation.Identity
	}
	for id, expected := range state.expectedMain {
		if current[id] != expected {
			return state.lease.poison(database.NewError(
				database.CodeIntegrity,
				"database main generation changed outside a reconciled migration replacement",
			))
		}
	}
	if len(current) != countValidMigrationMainIdentities(state.expectedMain) {
		return state.lease.poison(database.NewError(
			database.CodeIntegrity,
			"database migration observed an unknown main assignment",
		))
	}
	if requireClaimed &&
		(!observationsEqual(state.lease.observations, observed) ||
			!allObservationClaimsHeld(
				state.lease.identities, state.lease.lexicalIdentities, observed,
			)) {
		return state.lease.poison(database.NewError(
			database.CodeIntegrity,
			"database migration observed an unclaimed physical assignment",
		))
	}
	if requireClaimed {
		return state.checkLocked()
	}
	return state.checkForReconcileLocked("")
}

func countValidMigrationMainIdentities(values map[database.StoreID]fileidentity.Identity) int {
	count := 0
	for _, identity := range values {
		if identity.Valid() {
			count++
		}
	}
	return count
}

func approvedMainBaselineValid(
	stores []storecatalog.Spec,
	baseline map[database.StoreID]fileidentity.Identity,
) bool {
	if len(baseline) != len(stores) {
		return false
	}
	for _, store := range stores {
		if _, ok := baseline[store.ID]; !ok {
			return false
		}
	}
	return true
}

func (lease *Lease) retireReplacementPinsLocked(
	ids map[database.StoreID]struct{},
) error {
	if len(ids) == 0 {
		return nil
	}
	owned := make(map[*replacementPin][]database.StoreID, len(ids))
	var result error
	for id := range ids {
		pin := lease.replacementPins[id]
		if pin == nil {
			lease.poisoned.Store(true)
			result = errors.Join(result, database.NewError(
				database.CodeIntegrity,
				"physical database migration replacement pin ledger is invalid",
			))
			continue
		}
		owned[pin] = append(owned[pin], id)
	}
	retire := func(pin *replacementPin) {
		pinIDs := owned[pin]
		if len(pinIDs) == 0 {
			return
		}
		for _, id := range pinIDs {
			if lease.replacementPins[id] != pin {
				lease.poisoned.Store(true)
				result = errors.Join(result, database.NewError(
					database.CodeIntegrity,
					"physical database migration replacement pin ownership changed",
				))
				continue
			}
			delete(lease.replacementPins, id)
		}
		closeErr := pin.close()
		lease.removePinResource(pin)
		if closeErr != nil {
			lease.poisoned.Store(true)
			result = errors.Join(
				result,
				fmt.Errorf("retire physical database replacement pin: %w", closeErr),
			)
		}
		delete(owned, pin)
	}
	for index := len(lease.resources) - 1; index >= 0; index-- {
		pin, ok := lease.resources[index].(*replacementPin)
		if !ok {
			continue
		}
		retire(pin)
	}
	if len(owned) != 0 {
		lease.poisoned.Store(true)
		result = errors.Join(result, database.NewError(
			database.CodeIntegrity,
			"physical database migration replacement resource ledger is invalid",
		))
		remaining := make([]database.StoreID, 0, len(owned))
		for _, pinIDs := range owned {
			sort.Slice(pinIDs, func(left, right int) bool { return pinIDs[left] < pinIDs[right] })
			remaining = append(remaining, pinIDs[0])
		}
		sort.Slice(remaining, func(left, right int) bool { return remaining[left] > remaining[right] })
		for _, id := range remaining {
			retire(lease.replacementPins[id])
		}
	}
	return result
}

// Refresh monotonically claims physical identities materialized after the
// lexical catalog was acquired. Replacing an existing main poisons the lease.
func (lease *Lease) Refresh() error {
	return lease.refresh("")
}

// RefreshReplacement promotes the exact main identity installed by a
// controlled, already-completed staged cutover.
func (lease *Lease) RefreshReplacement(id database.StoreID) error {
	if !id.Valid() {
		return database.NewError(database.CodeInvalid, "replacement store identity is invalid")
	}
	if lease == nil {
		return database.NewError(database.CodeUnavailable, "physical database lease is unavailable")
	}
	lease.mu.RLock()
	_, claimed := lease.byID[id]
	lease.mu.RUnlock()
	if !claimed {
		return database.NewError(database.CodeInvalid, "replacement store is not claimed")
	}
	return lease.refresh(id)
}

// PinReplacement claims the exact staged physical identity intended to
// replace one claimed store. Only an exclusive migration lease can mint this
// capability, and RefreshReplacement consumes the association after cutover.
func (lease *Lease) PinReplacement(id database.StoreID, path string) (resultErr error) {
	if lease == nil || !id.Valid() || path == "" {
		return database.NewError(database.CodeInvalid, "physical database replacement pin is invalid")
	}
	lease.mu.Lock()
	defer lease.mu.Unlock()
	if !lease.authorized() || lease.root == "" || lease.catalog == nil || lease.acquireClaim == nil {
		return database.NewError(database.CodeIntegrity, "physical database lease lost authority")
	}
	if err := lease.validateReviewScopeLocked(false, "", true); err != nil {
		return err
	}
	index, claimed := lease.byID[id]
	if !claimed {
		return database.NewError(database.CodeInvalid, "replacement store is not claimed")
	}
	guardedFence, releaseFence, guardErr := lease.fence.GuardMigrationChecked(lease.home)
	if guardErr != nil {
		lease.poisoned.Store(true)
		return guardErr
	}
	defer releaseFence()
	refreshOps := defaultClaimRefreshOps()
	refreshOps.acquire = lease.acquireClaim
	refreshOps.guardedAuthority = guardedFence
	return lease.pinReplacementLocked(id, path, index, refreshOps)
}

func (lease *Lease) pinReplacementLocked(
	id database.StoreID,
	path string,
	index int,
	refreshOps claimRefreshOps,
) (resultErr error) {
	if lease == nil || !id.Valid() || path == "" || index < 0 || index >= len(lease.stores) ||
		lease.stores[index].ID != id || refreshOps.acquire == nil ||
		refreshOps.guardedAuthority == nil {
		return database.NewError(
			database.CodeIntegrity,
			"physical database replacement guard is invalid",
		)
	}
	// Reconcile before inspecting the stage so a newly materialized catalog
	// object cannot evade the alias set captured at acquisition.
	if err := lease.refreshLockedWithOps("", refreshOps); err != nil {
		return err
	}
	canonical, err := canonicalReplacementPath(path)
	if err != nil {
		lease.poisoned.Store(true)
		return errors.Join(
			database.NewError(database.CodeIntegrity, "physical database replacement path is unsafe"),
			err,
		)
	}
	handle, err := openReplacementHandle(canonical)
	if err != nil {
		lease.poisoned.Store(true)
		return err
	}
	pin := &replacementPin{path: canonical, handle: handle}
	keepPin := false
	defer func() {
		if !keepPin {
			if closeErr := pin.close(); closeErr != nil {
				lease.poisoned.Store(true)
				resultErr = errors.Join(resultErr, closeErr)
			}
			lease.removePinResource(pin)
		}
	}()
	if len(lease.resources) >= maxLeaseResources {
		lease.poisoned.Store(true)
		return database.NewError(database.CodeIntegrity, "physical database retained resource limit is exceeded")
	}
	lease.resources = append(lease.resources, pin)
	stagedIdentity, exists, err := regularPhysicalIdentity(canonical)
	if err != nil || !exists || !handle.valid() || !handle.matches(stagedIdentity) {
		lease.poisoned.Store(true)
		return errors.Join(
			database.NewError(database.CodeIntegrity, "physical database pin identity is unavailable"),
			err,
		)
	}
	claimIdentity := digestPhysicalIdentity(stagedIdentity)
	targetAssignment := observationMemberKey(memberObservation{
		StoreID: id, Role: memberMain, Path: lease.stores[index].Path,
	})
	if _, cataloged := lease.catalogIdentities[claimIdentity]; cataloged ||
		lease.reviewScopeCatalogContains(claimIdentity, stagedIdentity.String()) {
		lease.poisoned.Store(true)
		return database.NewError(
			database.CodeIntegrity,
			"physical database replacement aliases a cataloged object",
		)
	}
	if assignment, assigned := lease.assignments[stagedIdentity.String()]; assigned &&
		assignment != targetAssignment {
		lease.poisoned.Store(true)
		return database.NewError(
			database.CodeIntegrity,
			"physical database replacement identity is bound to another catalog member",
		)
	}
	if assignment, pinned := lease.pinAssignments[stagedIdentity.String()]; pinned &&
		assignment != targetAssignment {
		lease.poisoned.Store(true)
		return database.NewError(
			database.CodeIntegrity,
			"physical database replacement identity was pinned to another catalog member",
		)
	}
	if _, held := lease.identities[claimIdentity]; !held {
		if len(lease.identities) >= maxLeaseClaims {
			lease.poisoned.Store(true)
			return database.NewError(database.CodeIntegrity, "physical database claim limit is exceeded")
		}
		claim, claimErr := lease.acquireClaim(lease.root, claimIdentity)
		if claimErr != nil {
			lease.poisoned.Store(true)
			if errors.Is(claimErr, errClaimBusy) {
				return database.NewError(database.CodeConflict, "physical database store is already owned")
			}
			return fmt.Errorf("pin physical database store: %w", claimErr)
		}
		if claim == nil {
			lease.poisoned.Store(true)
			return database.NewError(database.CodeIntegrity, "physical database claim handle is unavailable")
		}
		lease.claims = append(lease.claims, claim)
		lease.resources = append(lease.resources, claim)
		lease.identities[claimIdentity] = struct{}{}
	}
	// Reconcile again after locking. This closes the window where the pinned
	// inode could be assigned to a catalog pathname while its claim was opened.
	if err := lease.refreshLockedWithOps("", refreshOps); err != nil {
		return err
	}
	verifiedPath, pathErr := canonicalReplacementPath(canonical)
	verified, stillExists, verifyErr := regularPhysicalIdentity(canonical)
	if pathErr != nil || verifiedPath != canonical || verifyErr != nil || !stillExists ||
		verified != stagedIdentity || !handle.valid() || !handle.matches(verified) {
		lease.poisoned.Store(true)
		return errors.Join(
			database.NewError(database.CodeIntegrity, "physical database pin changed while claiming"),
			pathErr,
			verifyErr,
		)
	}
	if _, cataloged := lease.catalogIdentities[claimIdentity]; cataloged ||
		lease.reviewScopeCatalogContains(claimIdentity, stagedIdentity.String()) {
		lease.poisoned.Store(true)
		return database.NewError(
			database.CodeIntegrity,
			"physical database replacement aliases a cataloged object",
		)
	}
	if !lease.replacementPinsValid() ||
		!handle.matches(stagedIdentity) || !refreshOps.guardedAuthority() {
		lease.poisoned.Store(true)
		return database.NewError(
			database.CodeIntegrity,
			"physical database pin authority changed while pinning replacement",
		)
	}
	if previous := lease.replacementPins[id]; previous != nil {
		if previous.identity == stagedIdentity {
			return nil
		}
		closeErr := previous.close()
		lease.removePinResource(previous)
		if closeErr != nil {
			delete(lease.replacementPins, id)
			lease.poisoned.Store(true)
			return fmt.Errorf("replace physical database stage pin: %w", closeErr)
		}
	}
	if !refreshOps.guardedAuthority() {
		lease.poisoned.Store(true)
		return database.NewError(database.CodeIntegrity, "storage migration fence changed while pinning replacement")
	}
	lease.pinAssignments[stagedIdentity.String()] = targetAssignment
	pin.identity = stagedIdentity
	pin.claimIdentity = claimIdentity
	lease.replacementPins[id] = pin
	if !lease.replacementPinsValid() || !refreshOps.guardedAuthority() {
		delete(lease.replacementPins, id)
		lease.poisoned.Store(true)
		return database.NewError(database.CodeIntegrity, "physical database pin authority changed before publication")
	}
	keepPin = true
	return nil
}

func (lease *Lease) checkReplacementLocked(id database.StoreID, path string) error {
	if lease == nil || !id.Valid() || path == "" {
		return database.NewError(
			database.CodeInvalid,
			"physical database replacement check is invalid",
		)
	}
	pin := lease.replacementPins[id]
	if pin == nil || pin.handle == nil || !pin.identity.Valid() || pin.path == "" {
		return lease.poison(database.NewError(
			database.CodeIntegrity,
			"physical database replacement pin is unavailable",
		))
	}
	canonical, err := canonicalReplacementPath(path)
	if err != nil || canonical != pin.path {
		return lease.poison(errors.Join(
			database.NewError(
				database.CodeIntegrity,
				"physical database replacement path does not match its pin",
			),
			err,
		))
	}
	identity, exists, err := regularPhysicalIdentity(canonical)
	if err != nil || !exists || identity != pin.identity ||
		pin.claimIdentity != digestPhysicalIdentity(identity) ||
		!pin.handle.valid() || !pin.handle.matches(identity) {
		return lease.poison(errors.Join(
			database.NewError(
				database.CodeIntegrity,
				"physical database replacement identity changed while pinned",
			),
			err,
		))
	}
	if _, held := lease.identities[pin.claimIdentity]; !held {
		return lease.poison(database.NewError(
			database.CodeIntegrity,
			"physical database replacement claim is unavailable",
		))
	}
	return nil
}

func (lease *Lease) refresh(replacement database.StoreID) error {
	ops := defaultClaimRefreshOps()
	if lease != nil && lease.acquireClaim != nil {
		ops.acquire = lease.acquireClaim
	}
	return lease.refreshWithOps(replacement, ops)
}

func (lease *Lease) refreshWithOps(replacement database.StoreID, ops claimRefreshOps) error {
	if lease == nil {
		return database.NewError(database.CodeUnavailable, "physical database lease is unavailable")
	}
	lease.mu.Lock()
	defer lease.mu.Unlock()
	if !lease.authorized() || lease.root == "" || lease.catalog == nil {
		return database.NewError(database.CodeIntegrity, "physical database lease lost authority")
	}
	var (
		guardedFence func() bool
		releaseFence func()
		guardErr     error
	)
	if replacement.IsZero() {
		guardedFence, releaseFence, guardErr = lease.fence.GuardChecked(lease.home)
	} else {
		guardedFence, releaseFence, guardErr = lease.fence.GuardMigrationChecked(lease.home)
	}
	if guardErr != nil {
		lease.poisoned.Store(true)
		return guardErr
	}
	defer releaseFence()
	requestedAuthority := ops.guardedAuthority
	ops.guardedAuthority = func() bool {
		return requestedAuthority != nil && requestedAuthority() && guardedFence()
	}
	return lease.refreshLockedWithOps(replacement, ops)
}

func (lease *Lease) refreshLockedWithOps(replacement database.StoreID, ops claimRefreshOps) error {
	if ops.revalidate == nil || ops.observe == nil || ops.acquire == nil || ops.guardedAuthority == nil {
		return lease.poison(database.NewError(
			database.CodeIntegrity,
			"physical database refresh operations are invalid",
		))
	}
	if err := lease.validateReviewScopeLocked(false, replacement, true); err != nil {
		return err
	}
	strict, err := ops.revalidate(lease.catalog)
	if err != nil || !catalogsEqual(lease.catalog, strict) {
		return lease.poison(errors.Join(
			database.NewError(database.CodeIntegrity, "database catalog changed while claimed"), err,
		))
	}
	observed, err := ops.observe(strict)
	if err != nil {
		return lease.poison(err)
	}
	if err := lease.validateObservationTransition(observed, replacement); err != nil {
		return lease.poison(err)
	}
	for _, identity := range observationClaimIdentities(observed) {
		if _, held := lease.identities[identity]; held {
			continue
		}
		if len(lease.identities) >= maxLeaseClaims || !validClaimIdentity(identity) {
			lease.poisoned.Store(true)
			return database.NewError(database.CodeIntegrity, "physical database claim limit is exceeded")
		}
		claim, claimErr := ops.acquire(lease.root, identity)
		if claimErr != nil {
			lease.poisoned.Store(true)
			if errors.Is(claimErr, errClaimBusy) {
				return database.NewError(database.CodeConflict, "physical database store is already owned")
			}
			return fmt.Errorf("claim materialized physical database store: %w", claimErr)
		}
		if claim == nil {
			return lease.poison(database.NewError(
				database.CodeIntegrity,
				"physical database claim handle is unavailable",
			))
		}
		lease.claims = append(lease.claims, claim)
		lease.resources = append(lease.resources, claim)
		lease.identities[identity] = struct{}{}
	}
	verifiedCatalog, catalogErr := ops.revalidate(strict)
	verified, verifyErr := ops.observe(verifiedCatalog)
	if catalogErr != nil || verifyErr != nil || !catalogsEqual(strict, verifiedCatalog) ||
		!observationsEqual(observed, verified) {
		return lease.poison(errors.Join(
			database.NewError(database.CodeIntegrity, "physical database identities changed while refreshing claims"),
			catalogErr, verifyErr,
		))
	}
	if err := lease.validateObservationTransition(verified, replacement); err != nil {
		return lease.poison(err)
	}
	var replacementMain fileidentity.Identity
	if !replacement.IsZero() {
		main, exists := findObservation(verified, replacement, memberMain)
		_, baselineExists := lease.approvedMains[replacement]
		if !exists || !main.Identity.Valid() || !baselineExists ||
			!approvedMainBaselineValid(lease.stores, lease.approvedMains) {
			return lease.poison(database.NewError(
				database.CodeIntegrity,
				"database replacement main baseline is unavailable",
			))
		}
		replacementMain = main.Identity
	}
	if !allObservationClaimsHeld(lease.identities, lease.lexicalIdentities, verified) ||
		!claimHandlesValid(lease.claims) || !lease.replacementPinsValid() ||
		!ops.guardedAuthority() {
		return lease.poison(database.NewError(
			database.CodeIntegrity,
			"physical database lease lost authority while refreshing claims",
		))
	}
	if err := lease.validateReviewScopeLocked(true, replacement, true); err != nil {
		return err
	}
	if lease.reviewScope != nil {
		verifiedCatalog = lease.reviewScope.currentSelected
		verified = cloneObservations(lease.reviewScope.selectedObservation)
	}
	lease.catalog = verifiedCatalog
	lease.observations = cloneObservations(verified)
	for _, observation := range verified {
		lease.catalogIdentities[observation.ClaimIdentity] = struct{}{}
		lease.assignments[observation.Identity.String()] = observationMemberKey(observation)
	}
	if !replacement.IsZero() {
		pin := lease.replacementPins[replacement]
		delete(lease.replacementPins, replacement)
		if pin != nil && pin.handle != nil {
			closeErr := pin.close()
			lease.removePinResource(pin)
			if closeErr != nil {
				return lease.poison(fmt.Errorf("release physical database replacement pin: %w", closeErr))
			}
		}
		lease.approvedMains[replacement] = replacementMain
	}
	return nil
}

func (lease *Lease) authorized() bool {
	if lease.closed.Load() || lease.poisoned.Load() {
		return false
	}
	if !claimHandlesValid(lease.claims) {
		lease.poisoned.Store(true)
		return false
	}
	if !lease.replacementPinsValid() {
		lease.poisoned.Store(true)
		return false
	}
	if lease.fence == nil || !lease.fence.Authorizes(lease.home) {
		lease.poisoned.Store(true)
		return false
	}
	return true
}

func (lease *Lease) poison(err error) error {
	lease.poisoned.Store(true)
	return err
}

func claimHandlesValid(claims []claimHandle) bool {
	for _, claim := range claims {
		if claim == nil {
			return false
		}
		if validator, ok := claim.(claimValidator); ok && !validator.valid() {
			return false
		}
	}
	return true
}

func (lease *Lease) replacementPinsValid() bool {
	for id, pin := range lease.replacementPins {
		if !id.Valid() || pin == nil || pin.handle == nil || !pin.identity.Valid() ||
			pin.claimIdentity != digestPhysicalIdentity(pin.identity) ||
			!pin.handle.matches(pin.identity) {
			return false
		}
		if _, held := lease.identities[pin.claimIdentity]; !held {
			return false
		}
		index, claimed := lease.byID[id]
		if !claimed || index < 0 || index >= len(lease.stores) ||
			lease.pinAssignments[pin.identity.String()] != observationMemberKey(memberObservation{
				StoreID: id, Role: memberMain, Path: lease.stores[index].Path,
			}) {
			return false
		}
	}
	return true
}

func (lease *Lease) validateObservationTransition(
	current []memberObservation,
	replacement database.StoreID,
) error {
	baselineByMember := observationByMember(lease.observations)
	currentByMember := observationByMember(current)
	for member, baseline := range baselineByMember {
		if baseline.Role != memberMain {
			continue
		}
		observed, exists := currentByMember[member]
		if !exists {
			return database.NewError(database.CodeIntegrity, "database main generation disappeared while claimed")
		}
		if observed.Identity == baseline.Identity {
			continue
		}
		if replacement.IsZero() || baseline.StoreID != replacement {
			return database.NewError(database.CodeIntegrity, "database main generation changed while claimed")
		}
	}
	for member, observed := range currentByMember {
		identity := observed.Identity.String()
		if previous, exists := lease.assignments[identity]; exists {
			if previous != member {
				return database.NewError(
					database.CodeIntegrity,
					"database physical identity changed catalog assignment",
				)
			}
			continue
		}
		if target, wasPinned := lease.pinAssignments[identity]; wasPinned {
			pin := lease.replacementPins[replacement]
			if replacement.IsZero() || target != member || pin == nil || pin.identity != observed.Identity {
				return database.NewError(
					database.CodeIntegrity,
					"staged database identity materialized without its active replacement capability",
				)
			}
		}
	}
	if replacement.IsZero() {
		return nil
	}
	pin := lease.replacementPins[replacement]
	if pin == nil || pin.handle == nil || !pin.handle.matches(pin.identity) {
		return database.NewError(database.CodeIntegrity, "replacement main generation was not pinned before cutover")
	}
	main, exists := findObservation(current, replacement, memberMain)
	if !exists || main.Identity != pin.identity || main.ClaimIdentity != pin.claimIdentity {
		return database.NewError(database.CodeIntegrity, "replacement main generation was not pinned before cutover")
	}
	return nil
}

func observationByMember(observations []memberObservation) map[string]memberObservation {
	result := make(map[string]memberObservation, len(observations))
	for _, observation := range observations {
		result[observationMemberKey(observation)] = observation
	}
	return result
}

func observationMemberKey(observation memberObservation) string {
	return string(observation.StoreID) + "\x00" + string(observation.Role) + "\x00" + observation.Path
}

func findObservation(
	observations []memberObservation,
	id database.StoreID,
	role memberRole,
) (memberObservation, bool) {
	for _, observation := range observations {
		if observation.StoreID == id && observation.Role == role {
			return observation, true
		}
	}
	return memberObservation{}, false
}

func catalogsEqual(projected, built *storecatalog.Catalog) bool {
	if projected == nil || built == nil || projected.Home() != built.Home() {
		return false
	}
	left, right := projected.All(), built.All()
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index].ID != right[index].ID || left[index].Domain != right[index].Domain ||
			left[index].Path != right[index].Path || left[index].Required != right[index].Required ||
			!slices.Equal(left[index].LegacyRoots, right[index].LegacyRoots) {
			return false
		}
	}
	return true
}

func cloneSpec(spec storecatalog.Spec) storecatalog.Spec {
	spec.LegacyRoots = append([]string(nil), spec.LegacyRoots...)
	return spec
}

func checkedLexicalClaimIdentities(identities []storecatalog.ClaimID) ([]string, error) {
	if len(identities) == 0 || len(identities) > maxCatalogClaimPaths {
		return nil, database.NewError(database.CodeInvalid, "physical database claim limit is exceeded")
	}
	result := make([]string, len(identities))
	for index, identity := range identities {
		if !identity.Valid() {
			return nil, database.NewError(database.CodeIntegrity, "physical database claim identity is invalid")
		}
		result[index] = identity.String()
	}
	return result, nil
}

func catalogPhysicalClaimIdentities(catalog *storecatalog.Catalog) ([]string, error) {
	if catalog == nil {
		return nil, database.NewError(database.CodeIntegrity, "physical database catalog is unavailable")
	}
	observations, err := observeCatalog(catalog)
	return observationClaimIdentities(observations), err
}

func physicalClaimIdentitiesForStores(stores []storecatalog.Spec) ([]string, error) {
	observations, err := observeStores(stores)
	return observationClaimIdentities(observations), err
}

func observeCatalog(catalog *storecatalog.Catalog) ([]memberObservation, error) {
	if catalog == nil {
		return nil, database.NewError(database.CodeIntegrity, "physical database catalog is unavailable")
	}
	return observeStores(catalog.All())
}

type memberCandidate struct {
	storeID        database.StoreID
	role           memberRole
	path           string
	allowDirectory bool
}

func observeStores(stores []storecatalog.Spec) ([]memberObservation, error) {
	if len(stores) == 0 || len(stores) > maxCatalogClaimPaths/4 {
		return nil, database.NewError(database.CodeInvalid, "physical database claim limit is exceeded")
	}
	candidates := make([]memberCandidate, 0, len(stores)*4)
	for _, store := range stores {
		members := [...]memberCandidate{
			{storeID: store.ID, role: memberMain, path: store.Path},
			{storeID: store.ID, role: memberWAL, path: store.Path + "-wal"},
			{storeID: store.ID, role: memberSHM, path: store.Path + "-shm"},
			{storeID: store.ID, role: memberJournal, path: store.Path + "-journal"},
		}
		for _, member := range members {
			if len(candidates) >= maxCatalogClaimPaths {
				return nil, database.NewError(database.CodeInvalid, "physical database claim limit is exceeded")
			}
			candidates = append(candidates, member)
		}
		for _, path := range store.LegacyRoots {
			if len(candidates) >= maxCatalogClaimPaths {
				return nil, database.NewError(database.CodeInvalid, "physical database claim limit is exceeded")
			}
			candidates = append(candidates, memberCandidate{
				storeID: store.ID, role: memberLegacy, path: path, allowDirectory: true,
			})
		}
	}

	observations := make([]memberObservation, 0, len(candidates))
	assignments := make(map[string]memberObservation, len(candidates))
	for _, candidate := range candidates {
		identity, objectType, exists, err := observeMember(candidate.path, candidate.allowDirectory)
		if err != nil {
			return nil, err
		}
		if !exists {
			continue
		}
		observation := memberObservation{
			StoreID: candidate.storeID, Role: candidate.role, Path: candidate.path,
			Type: objectType, Identity: identity, ClaimIdentity: digestPhysicalIdentity(identity),
		}
		if previous, duplicate := assignments[identity.String()]; duplicate {
			return nil, database.NewError(
				database.CodeIntegrity,
				fmt.Sprintf(
					"physical database object is assigned to both %s/%s and %s/%s",
					previous.StoreID, previous.Role, observation.StoreID, observation.Role,
				),
			)
		}
		assignments[identity.String()] = observation
		observations = append(observations, observation)
	}
	sort.Slice(observations, func(left, right int) bool {
		return observationMemberKey(observations[left]) < observationMemberKey(observations[right])
	})
	return observations, nil
}

func observeMember(path string, allowDirectory bool) (fileidentity.Identity, memberType, bool, error) {
	identity, identityType, exists, err := fileidentity.ExistingWithType(path)
	if err != nil {
		return fileidentity.Identity{}, "", false, physicalIdentityError(err)
	}
	if !exists {
		return fileidentity.Identity{}, "", false, nil
	}
	var objectType memberType
	switch {
	case identityType == fileidentity.ObjectTypeRegular:
		objectType = memberRegular
	case allowDirectory && identityType == fileidentity.ObjectTypeDirectory:
		objectType = memberDirectory
	default:
		return fileidentity.Identity{}, "", false, database.NewError(
			database.CodeIntegrity,
			"physical database generation member must be a regular file",
		)
	}
	return identity, objectType, true, nil
}

func regularPhysicalIdentity(path string) (fileidentity.Identity, bool, error) {
	identity, _, exists, err := observeMember(path, false)
	if err != nil || !exists {
		return fileidentity.Identity{}, exists, err
	}
	return identity, true, nil
}

func digestPhysicalIdentity(identity fileidentity.Identity) string {
	digest := sha256.Sum256([]byte(physicalClaimIdentityVersion + identity.String()))
	return hex.EncodeToString(digest[:])
}

func observationClaimIdentities(observations []memberObservation) []string {
	if len(observations) == 0 {
		return nil
	}
	result := make([]string, len(observations))
	for index, observation := range observations {
		result[index] = observation.ClaimIdentity
	}
	sort.Strings(result)
	return result
}

func observationsEqual(left, right []memberObservation) bool {
	return slices.Equal(left, right)
}

func cloneObservations(observations []memberObservation) []memberObservation {
	return append([]memberObservation(nil), observations...)
}

func allObservationClaimsHeld(
	held map[string]struct{},
	lexical []string,
	observations []memberObservation,
) bool {
	for _, identity := range lexical {
		if _, exists := held[identity]; !exists {
			return false
		}
	}
	for _, observation := range observations {
		if _, exists := held[observation.ClaimIdentity]; !exists {
			return false
		}
	}
	return true
}

func canonicalReplacementPath(path string) (string, error) {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return "", database.NewError(database.CodeIntegrity, "replacement path must be canonical and absolute")
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", fmt.Errorf("canonicalize physical database replacement path: %w", err)
	}
	if filepath.Clean(resolved) != path {
		return "", database.NewError(database.CodeIntegrity, "replacement path contains an alias")
	}
	info, err := os.Lstat(path)
	if err != nil {
		return "", fmt.Errorf("inspect physical database replacement path: %w", err)
	}
	if !info.Mode().IsRegular() {
		return "", database.NewError(database.CodeIntegrity, "replacement stage must be a regular file")
	}
	return path, nil
}

func physicalClaimKey(path string) (string, bool, error) {
	identity, exists, err := fileidentity.Existing(path)
	if err == nil {
		return identity.String(), exists, nil
	}
	return "", false, physicalIdentityError(err)
}

func physicalIdentityError(err error) error {
	switch {
	case errors.Is(err, fileidentity.ErrInvalidPath):
		return database.NewError(
			database.CodeIntegrity,
			"physical database claim member path is invalid",
		)
	case errors.Is(err, fileidentity.ErrUnsafeType):
		return database.NewError(
			database.CodeIntegrity,
			"physical database claim member has an unsafe type",
		)
	case errors.Is(err, fileidentity.ErrUnsupported):
		return database.NewError(
			database.CodeUnsupported,
			"physical database claims are unsupported on this platform",
		)
	default:
		return fmt.Errorf("inspect physical database claim member: %w", err)
	}
}
