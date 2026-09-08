package storecatalog

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"sort"
)

const physicalClaimIdentityVersion = "picoclaw/database-physical-claim/v1\x00"

// ClaimID is an opaque, deterministic identity for one physical namespace.
// It deliberately exposes neither a path nor a logical StoreID.
type ClaimID string

// String returns the filesystem-safe opaque identity.
func (id ClaimID) String() string { return string(id) }

// Valid reports whether id has the canonical digest encoding.
func (id ClaimID) Valid() bool {
	value := string(id)
	if len(value) != sha256.Size*2 {
		return false
	}
	for _, character := range value {
		if character < '0' || character > '9' && character < 'a' || character > 'f' {
			return false
		}
	}
	return true
}

// ClaimIDs returns sorted, deduplicated identities for every catalog store's
// main, WAL, SHM, rollback-journal, and retained legacy namespaces.
func (catalog *Catalog) ClaimIDs() ([]ClaimID, error) {
	if catalog == nil || catalog.home == "" || len(catalog.specs) == 0 {
		return nil, errors.New("database catalog claims are unavailable")
	}
	identities := make(map[ClaimID]struct{}, len(catalog.specs)*4)
	for _, spec := range catalog.specs {
		if !spec.ID.Valid() || spec.Path == "" {
			return nil, errors.New("database catalog claim input is invalid")
		}
		for _, path := range []string{
			spec.Path,
			spec.Path + "-wal",
			spec.Path + "-shm",
			spec.Path + "-journal",
		} {
			identities[claimIDForCatalogPath(path)] = struct{}{}
		}
		for _, path := range spec.LegacyRoots {
			if path == "" {
				return nil, errors.New("database catalog legacy claim input is invalid")
			}
			identities[claimIDForCatalogPath(path)] = struct{}{}
		}
	}
	result := make([]ClaimID, 0, len(identities))
	for identity := range identities {
		result = append(result, identity)
	}
	sort.Slice(result, func(left, right int) bool { return result[left] < result[right] })
	return result, nil
}

func claimIDForCatalogPath(path string) ClaimID {
	digest := sha256.Sum256([]byte(physicalClaimIdentityVersion + catalogPathKey(path)))
	return ClaimID(hex.EncodeToString(digest[:]))
}
