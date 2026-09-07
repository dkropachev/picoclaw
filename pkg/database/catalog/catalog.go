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
	projected, err := storecatalog.Project(storecatalog.Options{
		Home:       options.Home,
		Config:     options.Config,
		ConfigPath: options.ConfigPath,
		UserHome:   options.UserHome,
	})
	if err != nil {
		return nil, sanitizeProjectionError(err)
	}

	specs := projected.All()
	return newProjectedCatalog(specs)
}

func newProjectedCatalog(specs []storecatalog.Spec) (*Catalog, error) {
	if len(specs) == 0 {
		return nil, database.NewError(database.CodeIntegrity, catalogInvalidMessage)
	}

	entries := make([]Entry, 0, len(specs))
	byID := make(map[StoreID]Entry, len(specs))
	for _, spec := range specs {
		if !spec.ID.Valid() || !validDomain(spec.Domain) {
			return nil, database.NewError(database.CodeIntegrity, catalogInvalidMessage)
		}
		if _, duplicate := byID[spec.ID]; duplicate {
			return nil, database.NewError(database.CodeIntegrity, catalogInvalidMessage)
		}
		entry := Entry{ID: spec.ID, Domain: spec.Domain, Required: spec.Required}
		entries = append(entries, entry)
		byID[entry.ID] = entry
	}
	sort.Slice(entries, func(left, right int) bool {
		return entries[left].ID < entries[right].ID
	})
	return &Catalog{entries: entries, byID: byID}, nil
}

// Entries returns a detached, ID-sorted snapshot of every logical entry.
func (catalog *Catalog) Entries() []Entry {
	if catalog == nil {
		return nil
	}
	return append([]Entry(nil), catalog.entries...)
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
