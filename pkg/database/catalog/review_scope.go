package catalog

import (
	"github.com/sipeed/picoclaw/internal/storecatalog"
	"github.com/sipeed/picoclaw/pkg/database"
)

// NewReviewSnapshot atomically derives the closed review-only logical catalog
// and its scope fingerprint from one complete physical catalog projection. No
// physical catalog, spec, path, or full fingerprint escapes this facade.
func NewReviewSnapshot(options Options, configRevision string) (*Catalog, string, error) {
	scope, err := storecatalog.NewReviewScope(storecatalog.Options{
		Home: options.Home, Config: options.Config,
		ConfigPath: options.ConfigPath, UserHome: options.UserHome,
	}, configRevision)
	if err != nil {
		return nil, "", sanitizeSnapshotError(err)
	}
	return newReviewCatalogSnapshot(scope.Bindings(), scope.Fingerprint())
}

func newReviewCatalogSnapshot(
	bindings []storecatalog.ReviewBinding,
	fingerprint string,
) (*Catalog, string, error) {
	entries := make([]Entry, len(bindings))
	for index, binding := range bindings {
		entries[index] = Entry{
			ID: binding.ID, Domain: binding.Domain, Required: binding.Required,
		}
	}
	logical, err := newLogicalCatalog(entries)
	if err != nil {
		return nil, "", database.NewError(database.CodeOf(err), catalogSnapshotFailureMessage)
	}
	if fingerprint == "" {
		return nil, "", database.NewError(database.CodeIntegrity, catalogSnapshotFailureMessage)
	}
	return logical, fingerprint, nil
}
