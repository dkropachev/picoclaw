// Package catalog exposes a provider-neutral logical database catalog. It
// projects only opaque store identities, domain names, and required-store
// policy; physical provider details remain inside the internal catalog.
package catalog

import (
	"sort"

	"github.com/sipeed/picoclaw/internal/storecatalog"
	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/database"
)

const (
	maxDomainBytes                  = 64
	catalogProjectionFailureMessage = "database catalog projection failed"
	catalogInvalidMessage           = "database catalog projection is invalid"
)

// StoreID is the protocol-wide opaque logical store identity.
type StoreID = database.StoreID

// Options supplies every explicit context used to derive a logical catalog.
// Values are consumed synchronously and are not retained by Catalog.
type Options struct {
	Home       string
	Config     *config.Config
	ConfigPath string
	UserHome   string
}

// Entry is the provider-neutral projection of one catalog store.
type Entry struct {
	ID       StoreID
	Domain   string
	Required bool
}

// Catalog is an immutable logical store inventory. Its retained state contains
// no configuration, filesystem location, or provider object.
type Catalog struct {
	entries []Entry
	byID    map[StoreID]Entry
}

// New derives a logical catalog without inspecting database generation
// members. Projection grants no provider, readiness, or migration authority.
func New(options Options) (*Catalog, error) {
	projected, err := project(options)
	if err != nil {
		return nil, err
	}

	return newProjectedCatalog(projected.All())
}

func project(options Options) (*storecatalog.Catalog, error) {
	projected, err := storecatalog.Project(storecatalog.Options{
		Home:       options.Home,
		Config:     options.Config,
		ConfigPath: options.ConfigPath,
		UserHome:   options.UserHome,
	})
	if err != nil {
		return nil, sanitizeProjectionError(err)
	}
	return projected, nil
}

func newProjectedCatalog(specs []storecatalog.Spec) (*Catalog, error) {
	if len(specs) == 0 {
		return nil, database.NewError(database.CodeIntegrity, catalogInvalidMessage)
	}

	entries := make([]Entry, 0, len(specs))
	for _, spec := range specs {
		entries = append(entries, Entry{ID: spec.ID, Domain: spec.Domain, Required: spec.Required})
	}
	return newLogicalCatalog(entries)
}

func newLogicalCatalog(entries []Entry) (*Catalog, error) {
	if len(entries) == 0 {
		return nil, database.NewError(database.CodeIntegrity, catalogInvalidMessage)
	}
	detached := append([]Entry(nil), entries...)
	byID := make(map[StoreID]Entry, len(detached))
	for _, entry := range detached {
		if !entry.ID.Valid() || !validDomain(entry.Domain) {
			return nil, database.NewError(database.CodeIntegrity, catalogInvalidMessage)
		}
		if _, duplicate := byID[entry.ID]; duplicate {
			return nil, database.NewError(database.CodeIntegrity, catalogInvalidMessage)
		}
		byID[entry.ID] = entry
	}
	sort.Slice(detached, func(left, right int) bool {
		return detached[left].ID < detached[right].ID
	})
	return &Catalog{entries: detached, byID: byID}, nil
}

func newCatalogSnapshot(
	specs []storecatalog.Spec,
	fingerprint string,
) (*Catalog, string, error) {
	logical, err := newProjectedCatalog(specs)
	if err != nil {
		return nil, "", sanitizeSnapshotError(err)
	}
	return logical, fingerprint, nil
}

// Entries returns a detached, ID-sorted snapshot of every logical entry.
func (catalog *Catalog) Entries() []Entry {
	if catalog == nil {
		return nil
	}
	return append([]Entry(nil), catalog.entries...)
}

// Bindings returns a detached, ID-sorted snapshot suitable for scoped server
// admission. Bindings contain no required policy or physical catalog metadata.
func (catalog *Catalog) Bindings() []database.StoreBinding {
	if catalog == nil {
		return nil
	}
	bindings := make([]database.StoreBinding, len(catalog.entries))
	for index, entry := range catalog.entries {
		bindings[index] = database.StoreBinding{ID: entry.ID, Domain: entry.Domain}
	}
	return bindings
}

// RequiredStores returns a detached, ID-sorted snapshot of store IDs marked
// required by this catalog's frozen admission policy. Required policy does not
// imply that a store exists, is ready, or is authorized for provider access.
func (catalog *Catalog) RequiredStores() []StoreID {
	if catalog == nil {
		return nil
	}
	var required []StoreID
	for _, entry := range catalog.entries {
		if entry.Required {
			required = append(required, entry.ID)
		}
	}
	return required
}

// Lookup validates value exactly and returns its catalog-owned StoreID.
// Whitespace and case are never normalized.
func (catalog *Catalog) Lookup(value string) (StoreID, error) {
	id, err := database.ParseStoreID(value)
	if err != nil {
		return "", err
	}
	if catalog == nil {
		return "", database.NewError(database.CodeUnavailable, "database catalog is unavailable")
	}
	entry, found := catalog.byID[id]
	if !found {
		return "", database.NewError(database.CodeNotFound, "database store ID is not in the catalog")
	}
	return entry.ID, nil
}

// Entry returns the detached logical entry for an exact StoreID.
func (catalog *Catalog) Entry(id StoreID) (Entry, bool) {
	if catalog == nil || !id.Valid() {
		return Entry{}, false
	}
	entry, found := catalog.byID[id]
	return entry, found
}

// LookupChannel derives a supported channel identity and returns it only when
// that exact identity belongs to this catalog.
func (catalog *Catalog) LookupChannel(channelType, name string) (StoreID, error) {
	id, supported := storecatalog.ChannelStoreID(channelType, name)
	if !supported {
		return "", database.NewError(database.CodeNotFound, "channel has no database store")
	}
	return catalog.Lookup(id.String())
}

// Contains reports whether id belongs to this exact logical snapshot.
func (catalog *Catalog) Contains(id StoreID) bool {
	_, found := catalog.Entry(id)
	return found
}

func sanitizeProjectionError(err error) error {
	code := database.CodeOf(err)
	switch code {
	case database.CodeUnavailable, database.CodeUnauthorized, database.CodeIntegrity:
	default:
		code = database.CodeInvalid
	}
	return database.NewError(code, catalogProjectionFailureMessage)
}

func validDomain(value string) bool {
	if value == "" || len(value) > maxDomainBytes {
		return false
	}
	for index := 0; index < len(value); index++ {
		character := value[index]
		alphaNumeric := character >= 'a' && character <= 'z' ||
			character >= '0' && character <= '9'
		if alphaNumeric {
			continue
		}
		if character != '-' || index == 0 || index == len(value)-1 {
			return false
		}
	}
	return true
}
