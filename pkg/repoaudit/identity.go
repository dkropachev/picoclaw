package repoaudit

import (
	"errors"
	"net/url"
	"path/filepath"
	"slices"
	"strings"
)

// RepositoryLedgerIdentities returns the canonical repository-ledger lookup
// order shared by the launcher API, controller, and gateway. GitHub clone URL,
// SSH/SCP URL, and owner/repository spellings all resolve to the same lower-case
// owner/repository ledger while retaining the original identity as a legacy
// fallback. Absolute local repositories resolve to their cleaned path.
func RepositoryLedgerIdentities(repository string) []string {
	repository = strings.TrimSpace(repository)
	if repository == "" {
		return nil
	}
	identities := make([]string, 0, 2)
	add := func(value string) {
		value = strings.TrimSpace(value)
		if value != "" && !slices.Contains(identities, value) {
			identities = append(identities, value)
		}
	}
	if filepath.IsAbs(repository) {
		add(filepath.Clean(repository))
		return identities
	}
	if github := GitHubRepositoryIdentity(repository); github != "" {
		add(github)
	}
	if parsed, err := url.Parse(repository); err == nil && parsed.Scheme != "" && parsed.Host != "" {
		parsed.User = nil
		parsed.RawQuery = ""
		parsed.Fragment = ""
		add(parsed.String())
	} else {
		add(repository)
	}
	return identities
}

// CanonicalRepositoryIdentity returns the preferred ledger identity. It is
// intentionally tolerant of legacy repository spellings because validation of
// new configuration happens at the configuration boundary.
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

// ResolveRepositoryState applies canonical identity lookup first and then a
// run-ID fallback for legacy ledgers. More than one run-matching ledger is an
// integrity error rather than an arbitrary selection.
func (s Store) ResolveRepositoryState(
	repository string,
	runIDs []string,
) (RepositoryState, bool, error) {
	for _, identity := range RepositoryLedgerIdentities(repository) {
		state, found, err := s.Get(identity)
		if err != nil || found {
			return state, found, err
		}
	}
	if len(runIDs) == 0 {
		return RepositoryState{}, false, nil
	}
	wanted := make(map[string]struct{}, len(runIDs))
	for _, runID := range runIDs {
		if runID = strings.TrimSpace(runID); runID != "" {
			wanted[runID] = struct{}{}
		}
	}
	if len(wanted) == 0 {
		return RepositoryState{}, false, nil
	}
	states, err := s.List()
	if err != nil {
		return RepositoryState{}, false, err
	}
	var selected RepositoryState
	found := false
	for _, state := range states {
		matched := false
		for _, run := range state.Runs {
			if _, ok := wanted[run.ID]; ok {
				matched = true
				break
			}
		}
		if !matched {
			continue
		}
		if found && selected.Repository != state.Repository {
			return RepositoryState{}, false, errors.New("ambiguous repository review ledger")
		}
		selected, found = state, true
	}
	return selected, found, nil
}
