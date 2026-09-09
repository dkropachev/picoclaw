// Package databaseclaims acquires process-external ownership of every physical
// namespace in a trusted store catalog. It is infrastructure-only so the
// public database protocol never depends on physical catalog construction.
package databaseclaims

import (
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
	fence             *database.Fence
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

	lease := &Lease{claims: make([]claimHandle, 0, len(lexicalIdentities))}
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
	for index := range lease.stores {
		lease.byID[lease.stores[index].ID] = index
	}
	return lease, nil
}

// Home returns the canonical home bound to this lease.
func (lease *Lease) Home() string {
	if lease == nil {
		return ""
	}
	lease.mu.RLock()
	defer lease.mu.RUnlock()
	if !lease.authorized() {
		return ""
	}
	return lease.home
}

// Stores returns a detached, ID-sorted copy of every claimed store.
func (lease *Lease) Stores() []storecatalog.Spec {
	if lease == nil {
		return nil
	}
	lease.mu.RLock()
	defer lease.mu.RUnlock()
	if !lease.authorized() {
		return nil
	}
	stores := make([]storecatalog.Spec, len(lease.stores))
	for index := range lease.stores {
		stores[index] = cloneSpec(lease.stores[index])
	}
	return stores
}

// Lookup returns a detached claimed store for one typed logical ID.
func (lease *Lease) Lookup(id database.StoreID) (storecatalog.Spec, bool) {
	if lease == nil {
		return storecatalog.Spec{}, false
	}
	lease.mu.RLock()
	defer lease.mu.RUnlock()
	if !lease.authorized() {
		return storecatalog.Spec{}, false
	}
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
	})
	return result
}

// Check verifies that the retained fence and every replaceable claim pathname
// still name the exact locked objects. Failure permanently poisons the lease.
func (lease *Lease) Check() error {
	if lease == nil {
		return database.NewError(database.CodeUnavailable, "physical database lease is unavailable")
	}
	lease.mu.RLock()
	defer lease.mu.RUnlock()
	if !lease.authorized() {
		return database.NewError(database.CodeIntegrity, "physical database lease lost authority")
	}
	return nil
}

// Guard holds the lease live across one short ownership transfer. The returned
// release function must be called exactly once when err is nil.
func (lease *Lease) Guard() (func(), error) {
	if lease == nil {
		return nil, database.NewError(database.CodeUnavailable, "physical database lease is unavailable")
	}
	lease.mu.RLock()
	if !lease.authorized() {
		lease.mu.RUnlock()
		return nil, database.NewError(database.CodeIntegrity, "physical database lease lost authority")
	}
	releaseFence, err := lease.fence.Guard(lease.home)
	if err != nil {
		lease.poisoned.Store(true)
		lease.mu.RUnlock()
		return nil, errors.Join(database.NewError(
			database.CodeIntegrity,
			"physical database lease lost fence authority",
		), err)
	}
	return func() {
		releaseFence()
		lease.mu.RUnlock()
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
	pin := &replacementPin{handle: handle}
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
	if _, cataloged := lease.catalogIdentities[claimIdentity]; cataloged {
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
	if _, cataloged := lease.catalogIdentities[claimIdentity]; cataloged {
		lease.poisoned.Store(true)
		return database.NewError(
			database.CodeIntegrity,
			"physical database replacement aliases a cataloged object",
		)
	}
	if !lease.replacementPinsValid() ||
		!handle.matches(stagedIdentity) || !guardedFence() {
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
	if !guardedFence() {
		lease.poisoned.Store(true)
		return database.NewError(database.CodeIntegrity, "storage migration fence changed while pinning replacement")
	}
	lease.pinAssignments[stagedIdentity.String()] = targetAssignment
	pin.identity = stagedIdentity
	pin.claimIdentity = claimIdentity
	lease.replacementPins[id] = pin
	if !lease.replacementPinsValid() || !guardedFence() {
		delete(lease.replacementPins, id)
		lease.poisoned.Store(true)
		return database.NewError(database.CodeIntegrity, "physical database pin authority changed before publication")
	}
	keepPin = true
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
	if !allObservationClaimsHeld(lease.identities, lease.lexicalIdentities, verified) ||
		!claimHandlesValid(lease.claims) || !lease.replacementPinsValid() ||
		!ops.guardedAuthority() {
		return lease.poison(database.NewError(
			database.CodeIntegrity,
			"physical database lease lost authority while refreshing claims",
		))
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
