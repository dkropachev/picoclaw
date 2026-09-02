// Package artifacts exposes the privileged provider-artifact projection used
// by model-facing filesystem policy. It is not a database operation API.
package artifacts

import (
	"path/filepath"
	"sort"

	"github.com/sipeed/picoclaw/internal/storecatalog"
	"github.com/sipeed/picoclaw/pkg/config"
)

// Catalog is an immutable protected-artifact projection.
type Catalog struct {
	roots    []string
	byDomain map[string][]string
}

// New derives database generations, lock namespaces, and retained legacy
// inputs from the same trusted catalog used by the broker.
func New(home string, cfg *config.Config) (*Catalog, error) {
	physical, err := storecatalog.Project(home, cfg)
	if err != nil {
		return nil, err
	}
	seen := make(map[string]struct{}, len(physical.Specs)*8)
	byDomainSets := make(map[string]map[string]struct{})
	for _, spec := range physical.Specs {
		specRoots := []string{
			spec.Path,
			spec.Path + "-wal",
			spec.Path + "-shm",
			spec.Path + "-journal",
			spec.Path + ".locks",
			filepath.Join(spec.Path+".locks", "store.lock"),
		}
		for _, legacy := range spec.LegacyRoots {
			specRoots = append(specRoots, legacy)
		}
		specRoots = append(specRoots, providerArchiveRoots(spec.Path, spec.Domain)...)
		domainSet := byDomainSets[spec.Domain]
		if domainSet == nil {
			domainSet = make(map[string]struct{})
			byDomainSets[spec.Domain] = domainSet
		}
		for _, root := range specRoots {
			root = filepath.Clean(root)
			seen[root] = struct{}{}
			domainSet[root] = struct{}{}
		}
	}
	roots := make([]string, 0, len(seen))
	for root := range seen {
		roots = append(roots, root)
	}
	sort.Strings(roots)
	byDomain := make(map[string][]string, len(byDomainSets))
	for domain, domainSet := range byDomainSets {
		values := make([]string, 0, len(domainSet))
		for root := range domainSet {
			values = append(values, root)
		}
		sort.Strings(values)
		byDomain[domain] = values
	}
	return &Catalog{roots: roots, byDomain: byDomain}, nil
}

// ProtectedRootsForDomains returns only provider-owned artifacts belonging to
// trusted catalog domains. Callers select logical domains and never construct
// a database filename or generation member.
func (catalog *Catalog) ProtectedRootsForDomains(domains ...string) []string {
	if catalog == nil || len(domains) == 0 {
		return nil
	}
	seen := make(map[string]struct{})
	for _, domain := range domains {
		for _, root := range catalog.byDomain[domain] {
			seen[root] = struct{}{}
		}
	}
	result := make([]string, 0, len(seen))
	for root := range seen {
		result = append(result, root)
	}
	sort.Strings(result)
	return result
}

func providerArchiveRoots(storePath, domain string) []string {
	directory := filepath.Dir(storePath)
	switch domain {
	case "auth":
		return []string{filepath.Join(directory, "legacy-json", "auth-v1")}
	case "launcher-auth":
		return []string{filepath.Join(directory, "legacy-json", "launcher-auth-v1")}
	case "model-catalogs":
		return []string{filepath.Join(directory, "legacy-json", "model-catalogs-v1")}
	case "tool-adaptation":
		return []string{filepath.Join(directory, "legacy-json", "tool-adaptation-v1")}
	case "channel-wecom":
		return []string{filepath.Join(filepath.Dir(directory), "legacy-json", "wecom-reqid-v1")}
	case "channel-weixin":
		return []string{filepath.Join(directory, "legacy-json", "weixin-state-v1")}
	case "workflows":
		return []string{filepath.Join(filepath.Dir(directory), "legacy-json")}
	case "sessions":
		return []string{filepath.Join(filepath.Dir(directory), "legacy-json")}
	case "cron":
		return []string{filepath.Join(directory, "legacy-json", "cron-jobs-v1")}
	case "runtime-state":
		return []string{filepath.Join(directory, "legacy-json", "runtime-state-v1")}
	case "account-routing":
		return []string{filepath.Join(directory, "legacy-json", "account-router-v1")}
	case "evolution":
		return []string{filepath.Join(directory, "legacy-json")}
	case "local-ci":
		return []string{directory, filepath.Join(directory, "legacy-json")}
	case "git-workspace-inventory":
		archive := filepath.Join(directory, "legacy-json", "git-workspaces-v1")
		return []string{archive, filepath.Join(archive, "inventory.json")}
	case "pr-workspace-checkpoints":
		return []string{filepath.Join(directory, "legacy-json", "pr-workspace-checkpoints-v1")}
	default:
		return nil
	}
}

// ProtectedRoots returns a detached provider-artifact projection for the
// trusted filesystem mutation policy.
func (catalog *Catalog) ProtectedRoots() []string {
	if catalog == nil {
		return nil
	}
	return append([]string(nil), catalog.roots...)
}
