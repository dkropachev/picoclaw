package catalog

import "github.com/sipeed/picoclaw/pkg/database"

const catalogSnapshotFailureMessage = "database catalog snapshot failed"

// NewSnapshot atomically derives a provider-neutral logical catalog and the
// opaque fingerprint of the same complete internal projection. The trusted
// caller must supply the exact immutable configuration snapshot identified by
// configRevision; this function does not load or verify configuration bytes.
// It retains no configuration, physical catalog, path, or fingerprint state.
func NewSnapshot(options Options, configRevision string) (*Catalog, string, error) {
	projected, err := project(options)
	if err != nil {
		return nil, "", sanitizeSnapshotError(err)
	}

	fingerprint, err := projected.Fingerprint(configRevision)
	if err != nil {
		return nil, "", database.NewError(database.CodeInvalid, catalogSnapshotFailureMessage)
	}

	return newCatalogSnapshot(projected.All(), fingerprint)
}

func sanitizeSnapshotError(err error) *database.Error {
	return database.NewError(database.CodeOf(err), catalogSnapshotFailureMessage)
}
