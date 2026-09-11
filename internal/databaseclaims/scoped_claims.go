package databaseclaims

import (
	"slices"
	"sort"
	"testing"

	"github.com/sipeed/picoclaw/internal/storecatalog"
	"github.com/sipeed/picoclaw/pkg/database"
)

type reviewScopeValidationOps struct {
	revalidate func(*storecatalog.ReviewScope) (*storecatalog.ReviewScope, error)
	catalogs   func(*storecatalog.ReviewScope) (*storecatalog.Catalog, *storecatalog.Catalog, error)
	observe    func(*storecatalog.Catalog) ([]memberObservation, error)
}

func defaultReviewScopeValidationOps() reviewScopeValidationOps {
	return reviewScopeValidationOps{
		revalidate: storecatalog.RevalidateReviewScope,
		catalogs:   storecatalog.ReviewScopeCatalogs,
		observe:    observeCatalog,
	}
}

type reviewScopeLeaseState struct {
	scope                        *storecatalog.ReviewScope
	fullCatalog                  *storecatalog.Catalog
	fingerprint                  string
	fullFingerprint              string
	bindings                     []storecatalog.ReviewBinding
	storeIDs                     []database.StoreID
	selectedIDs                  map[database.StoreID]struct{}
	currentSelected              *storecatalog.Catalog
	selectedObservation          []memberObservation
	currentUnselectedClaims      map[string]struct{}
	currentUnselectedAssignments map[string]string
	ops                          reviewScopeValidationOps
}

type reviewScopeObservation struct {
	selected              *storecatalog.Catalog
	selectedMembers       []memberObservation
	unselectedClaims      map[string]struct{}
	unselectedAssignments map[string]string
}

// AcquireReviewScope acquires the exact unsalted lexical and physical claims
// for the closed review selection. The complete retained catalog is revalidated
// only as a deny-set; unselected stores are never claimed or exposed.
func AcquireReviewScope(
	scope *storecatalog.ReviewScope,
	fence *database.Fence,
) (*Lease, error) {
	return acquireReviewScopeWithOps(
		scope,
		fence,
		defaultClaimAcquireOps(),
		defaultReviewScopeValidationOps(),
	)
}

// AcquireReviewScopeForTesting routes selected review claims to one explicit
// private test root. It is unavailable outside a Go test process.
func AcquireReviewScopeForTesting(
	scope *storecatalog.ReviewScope,
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
	claimOps := defaultClaimAcquireOps()
	claimOps.claimRoot = func() (string, error) { return canonical, nil }
	claimOps.acquire = acquireClaimForTesting
	return acquireReviewScopeWithOps(
		scope,
		fence,
		claimOps,
		defaultReviewScopeValidationOps(),
	)
}

func acquireReviewScopeWithOps(
	scope *storecatalog.ReviewScope,
	fence *database.Fence,
	claimOps claimAcquireOps,
	scopeOps reviewScopeValidationOps,
) (*Lease, error) {
	state, selected, err := newReviewScopeLeaseState(scope, scopeOps)
	if err != nil {
		return nil, err
	}
	return acquireOwnedCatalogWithOps(
		storecatalog.Options{Home: selected.Home()},
		selected,
		fence,
		claimOps,
		state,
	)
}

func newReviewScopeLeaseState(
	scope *storecatalog.ReviewScope,
	ops reviewScopeValidationOps,
) (*reviewScopeLeaseState, *storecatalog.Catalog, error) {
	if scope == nil || ops.revalidate == nil || ops.catalogs == nil || ops.observe == nil {
		return nil, nil, database.NewError(
			database.CodeInvalid,
			"database review claim scope is invalid",
		)
	}
	full, selected, err := ops.catalogs(scope)
	if err != nil || full == nil || selected == nil || full.Home() == "" ||
		selected.Home() != full.Home() {
		return nil, nil, database.NewError(
			database.CodeInvalid,
			"database review claim scope is invalid",
		)
	}
	bindings := scope.Bindings()
	if !reviewBindingsMatchSelected(bindings, selected) || scope.Fingerprint() == "" ||
		scope.FullFingerprint() == "" {
		return nil, nil, database.NewError(
			database.CodeInvalid,
			"database review claim scope is invalid",
		)
	}
	storeIDs := make([]database.StoreID, len(bindings))
	selectedIDs := make(map[database.StoreID]struct{}, len(bindings))
	for index, binding := range bindings {
		storeIDs[index] = binding.ID
		selectedIDs[binding.ID] = struct{}{}
	}
	state := &reviewScopeLeaseState{
		scope:           scope,
		fullCatalog:     full,
		fingerprint:     scope.Fingerprint(),
		fullFingerprint: scope.FullFingerprint(),
		bindings:        append([]storecatalog.ReviewBinding(nil), bindings...),
		storeIDs:        storeIDs,
		selectedIDs:     selectedIDs,
		ops:             ops,
	}
	return state, selected, nil
}

func reviewBindingsMatchSelected(
	bindings []storecatalog.ReviewBinding,
	selected *storecatalog.Catalog,
) bool {
	if selected == nil || len(bindings) == 0 {
		return false
	}
	specs := selected.All()
	if len(bindings) != len(specs) {
		return false
	}
	// Catalog.All is canonically ID-sorted, so exact per-index equality also
	// proves that bindings are sorted and unique.
	for index := range bindings {
		if bindings[index].ID != specs[index].ID ||
			bindings[index].Domain != specs[index].Domain ||
			bindings[index].Required != specs[index].Required {
			return false
		}
	}
	return true
}

func (state *reviewScopeLeaseState) revalidateSelected(
	expected *storecatalog.Catalog,
) (*storecatalog.Catalog, error) {
	observation, err := state.observeGeneration(expected)
	if err != nil {
		return nil, err
	}
	state.commitObservation(observation)
	return observation.selected, nil
}

func (state *reviewScopeLeaseState) observeGeneration(
	expected *storecatalog.Catalog,
) (reviewScopeObservation, error) {
	if state == nil || state.scope == nil || expected == nil || state.ops.revalidate == nil ||
		state.ops.catalogs == nil || state.ops.observe == nil || len(state.selectedIDs) == 0 {
		return reviewScopeObservation{}, database.NewError(
			database.CodeIntegrity,
			"database review claim scope lost authority",
		)
	}
	validated, err := state.ops.revalidate(state.scope)
	if err != nil || validated == nil || validated.Fingerprint() != state.fingerprint ||
		validated.FullFingerprint() != state.fullFingerprint ||
		!slices.Equal(validated.Bindings(), state.bindings) {
		return reviewScopeObservation{}, database.NewError(
			database.CodeIntegrity,
			"database review claim scope changed",
		)
	}
	full, selected, err := state.ops.catalogs(validated)
	if err != nil || !catalogsEqual(state.fullCatalog, full) ||
		!catalogsEqual(expected, selected) || !reviewBindingsMatchSelected(state.bindings, selected) {
		return reviewScopeObservation{}, database.NewError(
			database.CodeIntegrity,
			"database review claim catalog changed",
		)
	}
	observed, err := state.ops.observe(full)
	if err != nil {
		return reviewScopeObservation{}, database.NewError(
			database.CodeIntegrity,
			"database review claim catalog observation failed",
		)
	}
	result := reviewScopeObservation{
		selected:              selected,
		selectedMembers:       make([]memberObservation, 0, len(observed)),
		unselectedClaims:      make(map[string]struct{}),
		unselectedAssignments: make(map[string]string),
	}
	for _, member := range observed {
		if _, selectedID := state.selectedIDs[member.StoreID]; selectedID {
			result.selectedMembers = append(result.selectedMembers, member)
			continue
		}
		result.unselectedClaims[member.ClaimIdentity] = struct{}{}
		result.unselectedAssignments[member.Identity.String()] = observationMemberKey(member)
	}
	return result, nil
}

func (state *reviewScopeLeaseState) commitObservation(observation reviewScopeObservation) {
	state.currentSelected = observation.selected
	state.selectedObservation = cloneObservations(observation.selectedMembers)
	state.currentUnselectedClaims = observation.unselectedClaims
	state.currentUnselectedAssignments = observation.unselectedAssignments
}

func (lease *Lease) validateReviewScopeLocked(
	requireSelectedClaims bool,
	replacement database.StoreID,
	validateSelected bool,
) error {
	if lease == nil || lease.reviewScope == nil {
		return nil
	}
	observation, err := lease.reviewScope.observeGeneration(lease.catalog)
	if err != nil {
		return lease.poison(err)
	}
	if validateSelected {
		if err := lease.validateObservationTransition(
			observation.selectedMembers,
			replacement,
		); err != nil {
			return lease.poison(err)
		}
	}
	if requireSelectedClaims && !allObservationClaimsHeld(
		lease.identities,
		lease.lexicalIdentities,
		observation.selectedMembers,
	) {
		return lease.poison(database.NewError(
			database.CodeIntegrity,
			"database review claim scope has an unclaimed selected identity",
		))
	}
	for identity, assignment := range observation.unselectedAssignments {
		if selectedAssignment, selectedBefore := lease.assignments[identity]; selectedBefore &&
			selectedAssignment != assignment {
			return lease.poison(database.NewError(
				database.CodeIntegrity,
				"database review claim scope changed physical assignment",
			))
		}
		if selectedAssignment, selectedBefore := lease.pinAssignments[identity]; selectedBefore &&
			selectedAssignment != assignment {
			return lease.poison(database.NewError(
				database.CodeIntegrity,
				"database review claim scope reassigned a selected replacement identity",
			))
		}
	}
	lease.reviewScope.commitObservation(observation)
	return nil
}

func (lease *Lease) adoptReviewScopeSelectedLocked() {
	if lease == nil || lease.reviewScope == nil || lease.reviewScope.currentSelected == nil {
		return
	}
	lease.catalog = lease.reviewScope.currentSelected
	lease.observations = cloneObservations(lease.reviewScope.selectedObservation)
	for _, observation := range lease.observations {
		lease.catalogIdentities[observation.ClaimIdentity] = struct{}{}
		lease.assignments[observation.Identity.String()] = observationMemberKey(observation)
	}
}

func (lease *Lease) reviewScopeCatalogContains(
	claimIdentity string,
	identity string,
) bool {
	if lease == nil || lease.reviewScope == nil {
		return false
	}
	if _, found := lease.reviewScope.currentUnselectedClaims[claimIdentity]; found {
		return true
	}
	_, found := lease.reviewScope.currentUnselectedAssignments[identity]
	return found
}

func (lease *Lease) lockValidatedExposure() (func(), error) {
	if lease == nil {
		return nil, database.NewError(database.CodeUnavailable, "physical database lease is unavailable")
	}
	if lease.reviewScope == nil {
		lease.mu.RLock()
		if !lease.authorized() {
			lease.mu.RUnlock()
			return nil, database.NewError(database.CodeIntegrity, "physical database lease lost authority")
		}
		return lease.mu.RUnlock, nil
	}
	lease.mu.Lock()
	if !lease.authorized() {
		lease.mu.Unlock()
		return nil, database.NewError(database.CodeIntegrity, "physical database lease lost authority")
	}
	if err := lease.validateReviewScopeLocked(true, "", true); err != nil {
		lease.mu.Unlock()
		return nil, err
	}
	lease.adoptReviewScopeSelectedLocked()
	return lease.mu.Unlock, nil
}

// ScopeFingerprint returns the opaque review-scope fingerprint only while the
// complete deny-set and selected claims remain valid.
func (lease *Lease) ScopeFingerprint() string {
	release, err := lease.lockValidatedExposure()
	if err != nil {
		return ""
	}
	defer release()
	if lease.reviewScope == nil {
		return ""
	}
	return lease.reviewScope.fingerprint
}

// FullCatalogFingerprint returns the complete catalog fingerprint retained by
// a valid review-scoped lease. It grants no access to unselected stores.
func (lease *Lease) FullCatalogFingerprint() string {
	release, err := lease.lockValidatedExposure()
	if err != nil {
		return ""
	}
	defer release()
	if lease.reviewScope == nil {
		return ""
	}
	return lease.reviewScope.fullFingerprint
}

// StoreIDs returns the detached sorted logical identities actually claimed by
// this lease. Review-scoped leases never include the full deny-set here.
func (lease *Lease) StoreIDs() []database.StoreID {
	release, err := lease.lockValidatedExposure()
	if err != nil {
		return nil
	}
	defer release()
	if lease.reviewScope != nil {
		return append([]database.StoreID(nil), lease.reviewScope.storeIDs...)
	}
	result := make([]database.StoreID, len(lease.stores))
	for index := range lease.stores {
		result[index] = lease.stores[index].ID
	}
	sort.Slice(result, func(left, right int) bool { return result[left] < result[right] })
	return result
}
