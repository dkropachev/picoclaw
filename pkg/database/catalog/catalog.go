// Package catalog exposes provider-neutral logical database identities. It
// deliberately does not expose filesystem locations or SQLite terminology.
package catalog

import (
	"errors"
	"sort"
	"strings"

	"github.com/sipeed/picoclaw/internal/storecatalog"
	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/database"
)

// StoreID is the protocol-wide opaque, comparable logical identity. The alias
// keeps catalog consumers on the same type used by broker envelopes/statuses.
type StoreID = database.StoreID

// Entry describes application-relevant readiness policy without revealing the
// physical provider catalog.
type Entry struct {
	ID       StoreID
	Domain   string
	Required bool
}

// Catalog is the immutable trusted store inventory for one canonical home.
type Catalog struct {
	entries []Entry
	byName  map[string]Entry
}

// New builds a logical catalog from the canonical home and validated
// configuration without inspecting any database generation member. Physical
// validation belongs to broker/provider startup and offline maintenance.
func New(home string, cfg *config.Config) (*Catalog, error) {
	if !database.BrokerAuthorityHeld() && !database.MigrationFenceHeld() &&
		!database.ProviderTestAuthorityHeld() {
		return nil, database.NewError(
			database.CodeUnauthorized,
			"database catalog projection requires owner authority",
		)
	}
	physical, err := storecatalog.Project(home, cfg)
	if err != nil {
		return nil, err
	}
	entries := make([]Entry, 0, len(physical.Specs))
	byName := make(map[string]Entry, len(physical.Specs))
	for _, spec := range physical.Specs {
		id, parseErr := database.ParseStoreID(spec.ID)
		if parseErr != nil {
			return nil, parseErr
		}
		entry := Entry{ID: id, Domain: spec.Domain, Required: spec.Required}
		entries = append(entries, entry)
		byName[spec.ID] = entry
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].ID < entries[j].ID })
	return &Catalog{entries: entries, byName: byName}, nil
}

// Entries returns a detached, stable-ID-sorted snapshot.
func (c *Catalog) Entries() []Entry {
	if c == nil {
		return nil
	}
	return append([]Entry(nil), c.entries...)
}

// Lookup resolves user input only against the trusted catalog. Arbitrary
// paths, DSNs, and unknown logical IDs fail closed.
func (c *Catalog) Lookup(value string) (StoreID, error) {
	if c == nil {
		return "", errors.New("database catalog is unavailable")
	}
	value = strings.TrimSpace(value)
	entry, ok := c.byName[value]
	if !ok || value == "" {
		return "", errors.New("unknown database store ID")
	}
	return entry.ID, nil
}

// LookupChannel resolves an enabled Matrix or WhatsApp channel through the
// trusted catalog without exposing or reconstructing its physical store.
func (c *Catalog) LookupChannel(channelType, name string) (StoreID, error) {
	logicalID, ok := storecatalog.ChannelStoreID(channelType, name)
	if !ok {
		return "", errors.New("channel has no database store")
	}
	return c.Lookup(logicalID)
}

// Contains reports whether id belongs to this exact catalog snapshot.
func (c *Catalog) Contains(id StoreID) bool {
	if c == nil || !id.Valid() {
		return false
	}
	_, ok := c.byName[string(id)]
	return ok
}
