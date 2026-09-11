package storecatalog

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"sort"
	"strings"

	"github.com/sipeed/picoclaw/pkg/database"
)

const (
	reviewScopeFingerprintVersion = "picoclaw/database-review-scope/v1"
	reviewScopeFixedCount         = 5
	reviewScopeWorkspaceCount     = 3
)

var errReviewScopeInvalid = errors.New("database review scope is invalid")

// ReviewBinding is the path-free logical projection of one selected review
// store. Required retains the complete catalog's configuration-derived policy.
type ReviewBinding struct {
	ID       database.StoreID
	Domain   string
	Required bool
}

// ReviewScope is one immutable closed-policy selection bound to the complete
// projected physical catalog and its exact configuration revision. Its physical
// catalogs remain opaque outside the single privileged claims bridge.
type ReviewScope struct {
	configRevision  string
	fullFingerprint string
	fingerprint     string
	full            *Catalog
	selected        *Catalog
	bindings        []ReviewBinding
	requiredStores  []database.StoreID
}

// NewReviewScope projects and validates the complete trusted catalog before
// deriving the fixed review selection. Callers cannot supply IDs, domains,
// specs, or paths independently from Options.
func NewReviewScope(options Options, configRevision string) (*ReviewScope, error) {
	full, err := Project(options)
	if err != nil {
		return nil, err
	}
	return newReviewScope(full, configRevision)
}

// RevalidateReviewScope strictly revalidates the complete retained catalog,
// then reconstructs the selection and requires both fingerprints to remain
// identical. It never rereads the former configuration object.
func RevalidateReviewScope(scope *ReviewScope) (*ReviewScope, error) {
	validated, err := validatedReviewScope(scope)
	if err != nil {
		return nil, err
	}
	strict, err := Revalidate(validated.full)
	if err != nil {
		return nil, err
	}
	revalidated, err := newReviewScope(strict, validated.configRevision)
	if err != nil || !sameReviewScopeGeneration(validated, revalidated) {
		return nil, errors.Join(errReviewScopeInvalid, err)
	}
	return revalidated, nil
}

// Fingerprint returns the versioned review-scope equality token. It binds the
// complete catalog fingerprint and every selected logical binding.
func (scope *ReviewScope) Fingerprint() string {
	if scope == nil {
		return ""
	}
	return scope.fingerprint
}

// FullFingerprint returns the complete configuration-bound catalog fingerprint
// from which this review scope was selected.
func (scope *ReviewScope) FullFingerprint() string {
	if scope == nil {
		return ""
	}
	return scope.fullFingerprint
}

// Bindings returns a detached, StoreID-sorted logical scope snapshot.
func (scope *ReviewScope) Bindings() []ReviewBinding {
	if scope == nil {
		return nil
	}
	return append([]ReviewBinding(nil), scope.bindings...)
}

// RequiredStores returns the detached, StoreID-sorted selected subset whose
// complete-catalog policy marks it required.
func (scope *ReviewScope) RequiredStores() []database.StoreID {
	if scope == nil {
		return nil
	}
	return append([]database.StoreID(nil), scope.requiredStores...)
}

// ReviewScopeCatalogs returns detached complete and selected physical catalogs
// for the exact privileged claims bridge. Logical or application code must use
// Bindings instead.
func ReviewScopeCatalogs(scope *ReviewScope) (full, selected *Catalog, err error) {
	validated, err := validatedReviewScope(scope)
	if err != nil {
		return nil, nil, err
	}
	return cloneCatalog(validated.full), cloneCatalog(validated.selected), nil
}

func newReviewScope(full *Catalog, configRevision string) (*ReviewScope, error) {
	if full == nil {
		return nil, errReviewScopeInvalid
	}
	fullFingerprint, err := full.Fingerprint(configRevision)
	if err != nil {
		return nil, err
	}

	seenFixed := make(map[database.StoreID]bool, reviewScopeFixedCount)
	dynamic := make(map[string]map[string]bool)
	workspaceKeys := make(map[string]bool)
	selected := make([]Spec, 0, reviewScopeFixedCount)
	// Fingerprint validation above already guarantees unique StoreIDs. Each
	// fixed key is its exact ID, and each dynamic workspace/suffix pair maps to
	// one exact ID, so these policy ledgers cannot receive a duplicate key.
	for _, spec := range full.All() {
		if workspaceKey, dynamicWorkspace := reviewScopeWorkspaceKey(spec.ID); dynamicWorkspace {
			workspaceKeys[workspaceKey] = true
		}
		expectedDomain, fixed, workspaceKey, suffix, selectedID := reviewScopePolicy(spec.ID)
		if !selectedID {
			if reviewScopeDomain(spec.Domain) || reviewScopeReservedIdentity(spec.ID) {
				return nil, errReviewScopeInvalid
			}
			continue
		}
		if spec.Domain != expectedDomain {
			return nil, errReviewScopeInvalid
		}
		if fixed {
			seenFixed[spec.ID] = true
		} else {
			if dynamic[workspaceKey] == nil {
				dynamic[workspaceKey] = make(map[string]bool, reviewScopeWorkspaceCount)
			}
			dynamic[workspaceKey][suffix] = true
		}
		selected = append(selected, cloneSpec(spec))
	}
	if len(seenFixed) != reviewScopeFixedCount {
		return nil, errReviewScopeInvalid
	}
	if len(dynamic) != len(workspaceKeys) {
		return nil, errReviewScopeInvalid
	}
	for workspaceKey := range workspaceKeys {
		suffixes := dynamic[workspaceKey]
		if len(suffixes) != reviewScopeWorkspaceCount ||
			!suffixes["repository-reviews"] || !suffixes["repository-evaluations"] ||
			!suffixes["local-ci"] {
			return nil, errReviewScopeInvalid
		}
	}
	if len(selected) != reviewScopeFixedCount+len(dynamic)*reviewScopeWorkspaceCount {
		return nil, errReviewScopeInvalid
	}
	sort.Slice(selected, func(left, right int) bool { return selected[left].ID < selected[right].ID })

	bindings := make([]ReviewBinding, len(selected))
	var required []database.StoreID
	for index, spec := range selected {
		bindings[index] = ReviewBinding{ID: spec.ID, Domain: spec.Domain, Required: spec.Required}
		if spec.Required {
			required = append(required, spec.ID)
		}
	}
	fingerprint, err := reviewScopeFingerprint(fullFingerprint, bindings)
	if err != nil {
		return nil, err
	}
	return &ReviewScope{
		configRevision:  configRevision,
		fullFingerprint: fullFingerprint,
		fingerprint:     fingerprint,
		full:            cloneCatalog(full),
		selected:        catalogFromSpecs(full.Home(), selected),
		bindings:        append([]ReviewBinding(nil), bindings...),
		requiredStores:  append([]database.StoreID(nil), required...),
	}, nil
}

func validatedReviewScope(scope *ReviewScope) (*ReviewScope, error) {
	if scope == nil || scope.full == nil || scope.selected == nil || scope.fingerprint == "" ||
		scope.fullFingerprint == "" || !validFingerprintConfigRevision(scope.configRevision) {
		return nil, errReviewScopeInvalid
	}
	rebuilt, err := newReviewScope(scope.full, scope.configRevision)
	if err != nil || !sameReviewScopeGeneration(scope, rebuilt) ||
		!catalogsHaveSameSpecs(scope.selected, rebuilt.selected) {
		return nil, errors.Join(errReviewScopeInvalid, err)
	}
	return rebuilt, nil
}

func sameReviewScopeGeneration(left, right *ReviewScope) bool {
	if left == nil || right == nil || left.configRevision != right.configRevision ||
		left.fullFingerprint != right.fullFingerprint || left.fingerprint != right.fingerprint ||
		len(left.bindings) != len(right.bindings) || len(left.requiredStores) != len(right.requiredStores) {
		return false
	}
	for index := range left.bindings {
		if left.bindings[index] != right.bindings[index] {
			return false
		}
	}
	for index := range left.requiredStores {
		if left.requiredStores[index] != right.requiredStores[index] {
			return false
		}
	}
	return true
}

func catalogsHaveSameSpecs(left, right *Catalog) bool {
	if left == nil || right == nil || left.Home() != right.Home() {
		return false
	}
	leftSpecs, rightSpecs := left.All(), right.All()
	if len(leftSpecs) != len(rightSpecs) {
		return false
	}
	for index := range leftSpecs {
		if leftSpecs[index].ID != rightSpecs[index].ID ||
			leftSpecs[index].Domain != rightSpecs[index].Domain ||
			leftSpecs[index].Path != rightSpecs[index].Path ||
			leftSpecs[index].Required != rightSpecs[index].Required ||
			!stringSlicesEqual(leftSpecs[index].LegacyRoots, rightSpecs[index].LegacyRoots) {
			return false
		}
	}
	return true
}

func stringSlicesEqual(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func reviewScopePolicy(id database.StoreID) (
	domain string,
	fixed bool,
	workspaceKey string,
	suffix string,
	selected bool,
) {
	if domain, selected = reviewScopeFixedDomain(id); selected {
		return domain, true, "", "", true
	}
	parts := strings.Split(id.String(), "/")
	if len(parts) != 3 || parts[0] != "workspace" || !validReviewWorkspaceKey(parts[1]) {
		return "", false, "", "", false
	}
	domain, selected = reviewScopeWorkspaceDomain(parts[2])
	return domain, false, parts[1], parts[2], selected
}

func reviewScopeWorkspaceKey(id database.StoreID) (string, bool) {
	parts := strings.Split(id.String(), "/")
	if len(parts) != 3 || parts[0] != "workspace" || !validReviewWorkspaceKey(parts[1]) {
		return "", false
	}
	return parts[1], true
}

func validReviewWorkspaceKey(value string) bool {
	if len(value) != 16 {
		return false
	}
	for _, character := range value {
		if character < '0' || character > '9' && character < 'a' || character > 'f' {
			return false
		}
	}
	return true
}

func reviewScopeDomain(domain string) bool {
	switch domain {
	case "git-workspace-inventory", "pr-workspace-checkpoints",
		"repository-reviews", "repository-evaluations", "local-ci":
		return true
	default:
		return false
	}
}

func reviewScopeReservedIdentity(id database.StoreID) bool {
	parts := strings.Split(id.String(), "/")
	_, reserved := reviewScopeWorkspaceDomain(parts[len(parts)-1])
	return reserved
}

func reviewScopeFixedDomain(id database.StoreID) (string, bool) {
	switch id {
	case "global/git-workspace-inventory":
		return "git-workspace-inventory", true
	case "global/pr-workspace-checkpoints":
		return "pr-workspace-checkpoints", true
	case "workspace/repository-reviews":
		return "repository-reviews", true
	case "workspace/repository-evaluations":
		return "repository-evaluations", true
	case "workspace/local-ci":
		return "local-ci", true
	default:
		return "", false
	}
}

func reviewScopeWorkspaceDomain(suffix string) (string, bool) {
	switch suffix {
	case "repository-reviews", "repository-evaluations", "local-ci":
		return suffix, true
	default:
		return "", false
	}
}

func reviewScopeFingerprint(fullFingerprint string, bindings []ReviewBinding) (string, error) {
	if !validFingerprintConfigRevision(fullFingerprint) || fullFingerprint == "missing" || len(bindings) == 0 {
		return "", errReviewScopeInvalid
	}
	digest := sha256.New()
	writeString := func(value string) {
		var size [8]byte
		binary.BigEndian.PutUint64(size[:], uint64(len(value)))
		_, _ = digest.Write(size[:])
		_, _ = digest.Write([]byte(value))
	}
	writeCount := func(value int) {
		var count [8]byte
		binary.BigEndian.PutUint64(count[:], uint64(value))
		_, _ = digest.Write(count[:])
	}
	writeString(reviewScopeFingerprintVersion)
	writeString(fullFingerprint)
	writeCount(len(bindings))
	previous := database.StoreID("")
	for _, binding := range bindings {
		if !binding.ID.Valid() || !validFingerprintDomain(binding.Domain) ||
			(!previous.IsZero() && binding.ID <= previous) {
			return "", errReviewScopeInvalid
		}
		writeString(binding.ID.String())
		writeString(binding.Domain)
		required := byte(0)
		if binding.Required {
			required = 1
		}
		_, _ = digest.Write([]byte{required})
		previous = binding.ID
	}
	return catalogFingerprintPrefix + hex.EncodeToString(digest.Sum(nil)), nil
}

func cloneCatalog(catalog *Catalog) *Catalog {
	if catalog == nil {
		return nil
	}
	return catalogFromSpecs(catalog.Home(), catalog.All())
}

func catalogFromSpecs(home string, specs []Spec) *Catalog {
	detached := make([]Spec, len(specs))
	for index := range specs {
		detached[index] = cloneSpec(specs[index])
	}
	sort.Slice(detached, func(left, right int) bool { return detached[left].ID < detached[right].ID })
	byID := make(map[database.StoreID]int, len(detached))
	for index := range detached {
		byID[detached[index].ID] = index
	}
	return &Catalog{home: home, specs: detached, byID: byID}
}
