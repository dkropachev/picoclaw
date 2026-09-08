// Package databaseclaims acquires process-external ownership of every physical
// namespace in a trusted store catalog. It is infrastructure-only so the
// public database protocol never depends on physical catalog construction.
package databaseclaims

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"slices"
	"sort"
	"sync"
	"sync/atomic"

	"github.com/sipeed/picoclaw/internal/fileidentity"
	"github.com/sipeed/picoclaw/internal/storecatalog"
	"github.com/sipeed/picoclaw/pkg/database"
)

const (
	physicalClaimIdentityVersion = "picoclaw/database-physical-object-claim/v1\x00"
	maxCatalogClaimPaths         = 32 * 1024
	maxLeaseClaims               = 64 * 1024
)

// Lease holds all physical claims for one immutable, fully validated catalog.
type Lease struct {
	mu                sync.RWMutex
	home              string
	root              string
	stores            []storecatalog.Spec
	byID              map[database.StoreID]int
	claims            []claimHandle
	identities        map[string]struct{}
	catalogIdentities map[string]struct{}
	mainIdentities    map[string]fileidentity.Identity
	replacementPins   map[database.StoreID]fileidentity.Identity
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
	physical   func(*storecatalog.Catalog) ([]string, error)
	build      func(storecatalog.Options) (*storecatalog.Catalog, error)
	revalidate func(*storecatalog.Catalog) (*storecatalog.Catalog, error)
}

func defaultClaimAcquireOps() claimAcquireOps {
	return claimAcquireOps{
		authorizes: func(fence *database.Fence, home string) bool { return fence.Authorizes(home) },
		project:    storecatalog.Project,
		claimIDs:   func(catalog *storecatalog.Catalog) ([]storecatalog.ClaimID, error) { return catalog.ClaimIDs() },
		lexical:    checkedLexicalClaimIdentities,
		claimRoot:  prepareClaimRoot,
		acquire:    acquireClaim,
		physical:   catalogPhysicalClaimIdentities,
		build:      storecatalog.Build,
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
		ops.physical == nil || ops.build == nil || ops.revalidate == nil {
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
	physicalIdentities, err := ops.physical(projected)
	if err != nil {
		return nil, err
	}
	claimIdentities := append(append([]string(nil), lexicalIdentities...), physicalIdentities...)
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
			lease.claims = append(lease.claims, claim)
			held[identity] = struct{}{}
		}
		return nil
	}
	if err := acquire(claimIdentities); err != nil {
		return fail(err)
	}

	var built *storecatalog.Catalog
	if options.Config == nil {
		built, err = ops.revalidate(projected)
	} else {
		built, err = ops.build(options)
	}
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
	builtPhysicalIdentities, err := ops.physical(built)
	if err != nil {
		return fail(err)
	}
	if !slices.Equal(lexicalIdentities, builtLexicalIdentities) ||
		!slices.Equal(physicalIdentities, builtPhysicalIdentities) ||
		!catalogsEqual(projected, built) {
		return fail(database.NewError(
			database.CodeIntegrity,
			"database catalog changed while acquiring physical claims",
		))
	}
	finalPhysicalIdentities, err := ops.physical(built)
	if err != nil || !slices.Equal(builtPhysicalIdentities, finalPhysicalIdentities) {
		return fail(errors.Join(
			database.NewError(
				database.CodeIntegrity,
				"database physical identities changed while acquiring claims",
			),
			err,
		))
	}

	if !ops.authorizes(fence, built.Home()) {
		return fail(database.NewError(database.CodeUnauthorized, "storage fence expired while acquiring claims"))
	}
	mainIdentities, err := catalogMainIdentities(built.All())
	if err != nil {
		return fail(err)
	}
	lease.home = built.Home()
	lease.root = root
	lease.fence = fence
	lease.identities = held
	lease.catalogIdentities = make(map[string]struct{}, len(held))
	for identity := range held {
		lease.catalogIdentities[identity] = struct{}{}
	}
	lease.mainIdentities = mainIdentities
	lease.replacementPins = make(map[database.StoreID]fileidentity.Identity)
	lease.stores = built.All()
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
		for index := len(lease.claims) - 1; index >= 0; index-- {
			result = errors.Join(result, lease.claims[index].close())
		}
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
		lease.mu.RUnlock()
		return nil, err
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
	return lease.refresh(id)
}

// PinReplacement claims the exact staged physical identity intended to
// replace one claimed store. Only an exclusive migration lease can mint this
// capability, and RefreshReplacement consumes the association after cutover.
func (lease *Lease) PinReplacement(id database.StoreID, path string) error {
	if lease == nil || !id.Valid() || path == "" {
		return database.NewError(database.CodeInvalid, "physical database replacement pin is invalid")
	}
	lease.mu.Lock()
	defer lease.mu.Unlock()
	if !lease.authorized() || lease.root == "" {
		return database.NewError(database.CodeIntegrity, "physical database lease lost authority")
	}
	releaseFence, guardErr := lease.fence.GuardMigration(lease.home)
	if guardErr != nil {
		return guardErr
	}
	defer releaseFence()
	index, claimed := lease.byID[id]
	if !claimed {
		return database.NewError(database.CodeInvalid, "replacement store is not claimed")
	}
	key, exists, err := physicalClaimKey(path)
	if err != nil || !exists {
		lease.poisoned.Store(true)
		return errors.Join(
			database.NewError(database.CodeIntegrity, "physical database pin identity is unavailable"),
			err,
		)
	}
	digest := sha256.Sum256([]byte(physicalClaimIdentityVersion + key))
	identity := hex.EncodeToString(digest[:])
	if _, catalogued := lease.catalogIdentities[identity]; catalogued {
		lease.poisoned.Store(true)
		return database.NewError(
			database.CodeIntegrity,
			"physical database replacement aliases a catalogued object",
		)
	}
	if _, held := lease.identities[identity]; !held {
		if len(lease.identities) >= maxLeaseClaims {
			lease.poisoned.Store(true)
			return database.NewError(database.CodeIntegrity, "physical database claim limit is exceeded")
		}
		claim, claimErr := acquireClaim(lease.root, identity)
		if claimErr != nil {
			lease.poisoned.Store(true)
			if errors.Is(claimErr, errClaimBusy) {
				return database.NewError(database.CodeConflict, "physical database store is already owned")
			}
			return fmt.Errorf("pin physical database store: %w", claimErr)
		}
		lease.claims = append(lease.claims, claim)
		lease.identities[identity] = struct{}{}
	}
	verified, stillExists, verifyErr := physicalClaimKey(path)
	if verifyErr != nil || !stillExists || verified != key {
		lease.poisoned.Store(true)
		return errors.Join(
			database.NewError(database.CodeIntegrity, "physical database pin changed while claiming"),
			verifyErr,
		)
	}
	stagedIdentity, identityExists, identityErr := fileidentity.Existing(path)
	liveIdentity, liveExists, liveIdentityErr := fileidentity.Existing(lease.stores[index].Path)
	if identityErr != nil || liveIdentityErr != nil || !identityExists ||
		stagedIdentity.String() != key || liveExists && liveIdentity == stagedIdentity {
		lease.poisoned.Store(true)
		return errors.Join(
			database.NewError(database.CodeIntegrity, "physical database replacement pin identity is invalid"),
			identityErr,
			liveIdentityErr,
		)
	}
	for pinnedID, pinnedIdentity := range lease.replacementPins {
		if pinnedID != id && pinnedIdentity == stagedIdentity {
			lease.poisoned.Store(true)
			return database.NewError(
				database.CodeIntegrity,
				"physical database replacement is pinned for another store",
			)
		}
	}
	lease.replacementPins[id] = stagedIdentity
	return nil
}

func (lease *Lease) refresh(replacement database.StoreID) error {
	if lease == nil {
		return database.NewError(database.CodeUnavailable, "physical database lease is unavailable")
	}
	lease.mu.Lock()
	defer lease.mu.Unlock()
	if !lease.authorized() || lease.root == "" {
		return database.NewError(database.CodeIntegrity, "physical database lease lost authority")
	}
	if !replacement.IsZero() {
		releaseFence, guardErr := lease.fence.GuardMigration(lease.home)
		if guardErr != nil {
			return guardErr
		}
		defer releaseFence()
	}
	observed, err := physicalClaimIdentitiesForStores(lease.stores)
	if err != nil {
		lease.poisoned.Store(true)
		return err
	}
	mains, err := catalogMainIdentities(lease.stores)
	replacementPath := ""
	if !replacement.IsZero() {
		index, exists := lease.byID[replacement]
		if !exists {
			lease.poisoned.Store(true)
			return database.NewError(database.CodeInvalid, "replacement store is not claimed")
		}
		replacementPath = lease.stores[index].Path
		if _, exists := mains[replacementPath]; !exists {
			lease.poisoned.Store(true)
			return database.NewError(database.CodeIntegrity, "replacement main generation is unavailable")
		}
	}
	if err != nil || !stableMainIdentities(lease.mainIdentities, mains, replacementPath) {
		lease.poisoned.Store(true)
		return errors.Join(
			database.NewError(database.CodeIntegrity, "database main generation changed while claimed"),
			err,
		)
	}
	if replacementPath != "" {
		replacementIdentity := mains[replacementPath]
		pinnedIdentity, pinned := lease.replacementPins[replacement]
		if !pinned || pinnedIdentity != replacementIdentity {
			lease.poisoned.Store(true)
			return database.NewError(
				database.CodeIntegrity,
				"replacement main generation was not pinned before cutover",
			)
		}
	}
	for _, identity := range observed {
		if _, held := lease.identities[identity]; held {
			continue
		}
		if len(lease.identities) >= maxLeaseClaims || !validClaimIdentity(identity) {
			lease.poisoned.Store(true)
			return database.NewError(database.CodeIntegrity, "physical database claim limit is exceeded")
		}
		claim, claimErr := acquireClaim(lease.root, identity)
		if claimErr != nil {
			lease.poisoned.Store(true)
			if errors.Is(claimErr, errClaimBusy) {
				return database.NewError(database.CodeConflict, "physical database store is already owned")
			}
			return fmt.Errorf("claim materialized physical database store: %w", claimErr)
		}
		lease.claims = append(lease.claims, claim)
		lease.identities[identity] = struct{}{}
	}
	verified, verifyErr := physicalClaimIdentitiesForStores(lease.stores)
	verifiedMains, mainErr := catalogMainIdentities(lease.stores)
	if verifyErr != nil || mainErr != nil || !slices.Equal(observed, verified) ||
		!stableMainIdentities(mains, verifiedMains, "") {
		lease.poisoned.Store(true)
		return errors.Join(
			database.NewError(database.CodeIntegrity, "physical database identities changed while refreshing claims"),
			verifyErr,
			mainErr,
		)
	}
	for path, identity := range mains {
		lease.mainIdentities[path] = identity
	}
	for _, identity := range observed {
		lease.catalogIdentities[identity] = struct{}{}
	}
	if !replacement.IsZero() {
		delete(lease.replacementPins, replacement)
	}
	return nil
}

func stableMainIdentities(
	baseline,
	current map[string]fileidentity.Identity,
	replacementPath string,
) bool {
	for path, identity := range baseline {
		if path == replacementPath {
			continue
		}
		if current[path] != identity {
			return false
		}
	}
	return true
}

func catalogMainIdentities(stores []storecatalog.Spec) (map[string]fileidentity.Identity, error) {
	result := make(map[string]fileidentity.Identity, len(stores))
	for _, store := range stores {
		identity, exists, err := fileidentity.Existing(store.Path)
		if err != nil {
			return nil, err
		}
		if exists {
			result[store.Path] = identity
		}
	}
	return result, nil
}

func (lease *Lease) authorized() bool {
	if lease.closed.Load() || lease.poisoned.Load() || lease.fence == nil ||
		!lease.fence.Authorizes(lease.home) {
		return false
	}
	for _, claim := range lease.claims {
		validator, ok := claim.(claimValidator)
		if ok && !validator.valid() {
			lease.poisoned.Store(true)
			return false
		}
	}
	return true
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
	return physicalClaimIdentitiesForStores(catalog.All())
}

func physicalClaimIdentitiesForStores(stores []storecatalog.Spec) ([]string, error) {
	if len(stores) == 0 || len(stores) > maxCatalogClaimPaths/4 {
		return nil, database.NewError(database.CodeInvalid, "physical database claim limit is exceeded")
	}
	paths := make([]string, 0, len(stores)*4)
	for _, store := range stores {
		members := [...]string{store.Path, store.Path + "-wal", store.Path + "-shm", store.Path + "-journal"}
		for _, path := range members {
			if len(paths) >= maxCatalogClaimPaths {
				return nil, database.NewError(database.CodeInvalid, "physical database claim limit is exceeded")
			}
			paths = append(paths, path)
		}
		for _, path := range store.LegacyRoots {
			if len(paths) >= maxCatalogClaimPaths {
				return nil, database.NewError(database.CodeInvalid, "physical database claim limit is exceeded")
			}
			paths = append(paths, path)
		}
	}

	identities := make(map[string]struct{}, len(paths))
	for _, path := range paths {
		key, exists, err := physicalClaimKey(path)
		if err != nil {
			return nil, err
		}
		if !exists {
			continue
		}
		digest := sha256.Sum256([]byte(physicalClaimIdentityVersion + key))
		identities[hex.EncodeToString(digest[:])] = struct{}{}
	}
	result := make([]string, 0, len(identities))
	for identity := range identities {
		result = append(result, identity)
	}
	sort.Strings(result)
	return result, nil
}

func physicalClaimKey(path string) (string, bool, error) {
	identity, exists, err := fileidentity.Existing(path)
	if err == nil {
		return identity.String(), exists, nil
	}
	switch {
	case errors.Is(err, fileidentity.ErrInvalidPath):
		return "", false, database.NewError(
			database.CodeIntegrity,
			"physical database claim member path is invalid",
		)
	case errors.Is(err, fileidentity.ErrUnsafeType):
		return "", false, database.NewError(
			database.CodeIntegrity,
			"physical database claim member has an unsafe type",
		)
	case errors.Is(err, fileidentity.ErrUnsupported):
		return "", false, database.NewError(
			database.CodeUnsupported,
			"physical database claims are unsupported on this platform",
		)
	default:
		return "", false, fmt.Errorf("inspect physical database claim member: %w", err)
	}
}
