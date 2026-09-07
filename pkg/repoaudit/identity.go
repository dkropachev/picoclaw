package repoaudit

import (
	"net/url"
	"path/filepath"
	"strings"
)

// RepositoryLedgerIdentities returns the sole canonical ledger identity.
func RepositoryLedgerIdentities(repository string) []string {
	repository = strings.TrimSpace(repository)
	if repository == "" {
		return nil
	}
	if filepath.IsAbs(repository) {
		return []string{filepath.Clean(repository)}
	}
	if github := GitHubRepositoryIdentity(repository); github != "" {
		return []string{github}
	}
	if parsed, err := url.Parse(repository); err == nil && parsed.Scheme != "" && parsed.Host != "" {
		parsed.User = nil
		parsed.RawQuery = ""
		parsed.Fragment = ""
		return []string{parsed.String()}
	}
	return []string{repository}
}

// CanonicalRepositoryIdentity returns the sole normalized ledger identity.
func CanonicalRepositoryIdentity(repository string) string {
	identities := RepositoryLedgerIdentities(repository)
	if len(identities) == 0 {
		return ""
	}
	return identities[0]
}

// GitHubRepositoryIdentity derives a safe canonical owner/repository identity
// from GitHub HTTPS/git/SSH URLs, SCP syntax, or shorthand. Invalid and
// non-GitHub remotes return an empty identity.
func GitHubRepositoryIdentity(repository string) string {
	repository = strings.TrimSpace(repository)
	if repository == "" || filepath.IsAbs(repository) {
		return ""
	}
	var pathValue string
	if strings.Contains(repository, ":") && !strings.Contains(repository, "://") {
		identity, remotePath, ok := strings.Cut(repository, ":")
		host := identity
		if user, parsedHost, hasUser := strings.Cut(identity, "@"); hasUser {
			if user == "" {
				return ""
			}
			host = parsedHost
		}
		if !ok || !strings.EqualFold(host, "github.com") {
			return ""
		}
		pathValue = remotePath
	} else if parsed, err := url.Parse(repository); err == nil && parsed.Scheme != "" {
		if !strings.EqualFold(parsed.Hostname(), "github.com") || parsed.Path == "" {
			return ""
		}
		pathValue = parsed.Path
	} else {
		pathValue = repository
	}
	pathValue = strings.Trim(strings.TrimSpace(pathValue), "/")
	pathValue = strings.TrimSuffix(pathValue, ".git")
	owner, name, ok := strings.Cut(pathValue, "/")
	if !ok || owner == "" || name == "" || strings.Contains(name, "/") ||
		!validGitHubIdentitySegment(owner) || !validGitHubIdentitySegment(name) {
		return ""
	}
	return strings.ToLower(owner + "/" + name)
}

func validGitHubIdentitySegment(value string) bool {
	if value == "" || value == "." || value == ".." || len(value) > 100 {
		return false
	}
	for _, character := range value {
		if character >= 'a' && character <= 'z' || character >= 'A' && character <= 'Z' ||
			character >= '0' && character <= '9' || character == '-' || character == '_' ||
			character == '.' {
			continue
		}
		return false
	}
	return true
}

func (s Store) ResolveRepositoryState(repository string) (RepositoryState, bool, error) {
	return s.Get(CanonicalRepositoryIdentity(repository))
}
