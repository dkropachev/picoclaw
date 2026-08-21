package auth

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"time"

	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/fileutil"
)

type AuthCredential struct {
	AccessToken       string    `json:"access_token"`
	RefreshToken      string    `json:"refresh_token,omitempty"`
	TokenType         string    `json:"token_type,omitempty"`
	OAuthTokenURL     string    `json:"oauth_token_url,omitempty"`
	OAuthClientID     string    `json:"oauth_client_id,omitempty"`
	OAuthClientSecret string    `json:"oauth_client_secret,omitempty"`
	OAuthAuthStyle    string    `json:"oauth_auth_style,omitempty"`
	AccountID         string    `json:"account_id,omitempty"`
	ExpiresAt         time.Time `json:"expires_at,omitempty"`
	Provider          string    `json:"provider"`
	AuthMethod        string    `json:"auth_method"`
	Email             string    `json:"email,omitempty"`
	ProjectID         string    `json:"project_id,omitempty"`
}

type AuthStore struct {
	Credentials map[string]*AuthCredential `json:"credentials"`
}

// RefreshCredentialFunc performs provider network work for one exact
// credential snapshot.
type RefreshCredentialFunc func(*AuthCredential) (*AuthCredential, error)

// RefreshCredentialResult reports the authoritative credential and whether
// this caller's replacement was the value committed to the store.
type RefreshCredentialResult struct {
	Credential *AuthCredential
	Committed  bool
}

const (
	providerGoogleAntigravity = "google-antigravity"
	providerAntigravityAlias  = "antigravity"
	providerGitHubCopilot     = "github-copilot"
	providerCopilotAlias      = "copilot"
)

var (
	authStoreWriteMu       sync.Mutex
	credentialRefreshLocks sync.Map
)

func (c *AuthCredential) IsExpired() bool {
	if c.ExpiresAt.IsZero() {
		return false
	}
	return time.Now().After(c.ExpiresAt)
}

func (c *AuthCredential) NeedsRefresh() bool {
	if c.ExpiresAt.IsZero() {
		return false
	}
	return time.Now().Add(5 * time.Minute).After(c.ExpiresAt)
}

func authFilePath() string {
	return filepath.Join(config.GetHome(), "auth.json")
}

func canonicalProvider(provider string) string {
	normalized := strings.ToLower(strings.TrimSpace(provider))
	switch normalized {
	case providerAntigravityAlias:
		return providerGoogleAntigravity
	case providerCopilotAlias:
		return providerGitHubCopilot
	default:
		return normalized
	}
}

func canonicalCredentialID(credentialID string) string {
	normalized := strings.ToLower(strings.TrimSpace(credentialID))
	provider, suffix, qualified := strings.Cut(normalized, ":")
	provider = canonicalProvider(provider)
	if !qualified {
		return provider
	}
	return provider + ":" + suffix
}

func validCredentialIDPart(part string) bool {
	if part == "" {
		return false
	}
	for _, r := range part {
		switch {
		case r >= 'a' && r <= 'z':
		case r >= '0' && r <= '9':
		case r == '-' || r == '_' || r == '.':
		default:
			return false
		}
	}
	return true
}

// NormalizeCredentialID returns the auth-store key for a provider credential.
// An empty credentialID maps to the provider default ("openai"). A bare name
// maps to a provider-scoped key ("openai:work"). A fully-qualified key must use
// the same provider prefix.
func NormalizeCredentialID(provider, credentialID string) (string, error) {
	canonical := canonicalProvider(provider)
	if canonical == "" {
		return "", fmt.Errorf("provider is required")
	}

	raw := strings.ToLower(strings.TrimSpace(credentialID))
	if raw == "" || raw == canonical ||
		(!strings.Contains(raw, ":") && canonicalProvider(raw) == canonical) {
		return canonical, nil
	}

	if strings.Contains(raw, ":") {
		prefix, name, ok := strings.Cut(raw, ":")
		if !ok || name == "" {
			return "", fmt.Errorf("invalid credential_id %q", credentialID)
		}
		prefix = canonicalProvider(prefix)
		if prefix != canonical {
			return "", fmt.Errorf("credential_id %q does not belong to provider %q", credentialID, provider)
		}
		if !validCredentialIDPart(name) {
			return "", fmt.Errorf("credential_id %q contains invalid characters", credentialID)
		}
		return prefix + ":" + name, nil
	}

	if !validCredentialIDPart(raw) {
		return "", fmt.Errorf("credential_id %q contains invalid characters", credentialID)
	}
	return canonical + ":" + raw, nil
}

func cloneCredential(cred *AuthCredential) *AuthCredential {
	if cred == nil {
		return nil
	}
	cp := *cred
	return &cp
}

func credentialProvider(credentialID string) string {
	provider, _, _ := strings.Cut(canonicalCredentialID(credentialID), ":")
	return canonicalProvider(provider)
}

func normalizeCredentialReplacement(
	credentialID string,
	current *AuthCredential,
	replacement *AuthCredential,
) (*AuthCredential, error) {
	normalized := cloneCredential(replacement)
	if normalized == nil {
		return nil, fmt.Errorf("replacement credential is required")
	}

	expectedProvider := ""
	if current != nil {
		expectedProvider = canonicalProvider(current.Provider)
	}
	if expectedProvider == "" {
		expectedProvider = credentialProvider(credentialID)
	}
	normalized.Provider = canonicalProvider(normalized.Provider)
	if normalized.Provider == "" {
		normalized.Provider = expectedProvider
	}
	if expectedProvider != "" && normalized.Provider != expectedProvider {
		return nil, fmt.Errorf(
			"replacement credential belongs to provider %q, want %q",
			normalized.Provider,
			expectedProvider,
		)
	}
	return normalized, nil
}

func shouldPreferCredential(
	candidate *AuthCredential,
	candidateCanonical bool,
	current *AuthCredential,
	currentCanonical bool,
) bool {
	if candidate == nil {
		return false
	}
	if current == nil {
		return true
	}

	switch {
	case candidate.ExpiresAt.After(current.ExpiresAt):
		return true
	case current.ExpiresAt.After(candidate.ExpiresAt):
		return false
	case candidateCanonical != currentCanonical:
		return candidateCanonical
	default:
		return false
	}
}

func normalizeStore(store *AuthStore) {
	if store == nil {
		return
	}
	if store.Credentials == nil {
		store.Credentials = make(map[string]*AuthCredential)
		return
	}

	normalized := make(map[string]*AuthCredential, len(store.Credentials))
	canonicalFlags := make(map[string]bool, len(store.Credentials))

	for provider, cred := range store.Credentials {
		normalizedProvider := strings.ToLower(strings.TrimSpace(provider))
		canonical := canonicalCredentialID(provider)
		normalizedCred := cloneCredential(cred)
		if normalizedCred != nil {
			normalizedCred.Provider = canonicalProvider(normalizedCred.Provider)
			if normalizedCred.Provider == "" {
				normalizedCred.Provider = credentialProvider(canonical)
			}
		}

		current := normalized[canonical]
		currentCanonical := canonicalFlags[canonical]
		candidateCanonical := normalizedProvider == canonical

		if shouldPreferCredential(normalizedCred, candidateCanonical, current, currentCanonical) {
			normalized[canonical] = cloneCredential(normalizedCred)
			canonicalFlags[canonical] = candidateCanonical
			continue
		}

		normalized[canonical] = cloneCredential(current)
	}

	store.Credentials = normalized
}

func LoadStore() (*AuthStore, error) {
	path := authFilePath()
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return &AuthStore{Credentials: make(map[string]*AuthCredential)}, nil
		}
		return nil, err
	}

	var store AuthStore
	if err := json.Unmarshal(data, &store); err != nil {
		return nil, err
	}
	normalizeStore(&store)
	return &store, nil
}

func SaveStore(store *AuthStore) error {
	unlock, err := lockAuthStoreForWrite()
	if err != nil {
		return err
	}
	defer unlock()

	return saveStore(store)
}

func lockAuthStoreForWrite() (func(), error) {
	authStoreWriteMu.Lock()
	unlockFile, err := lockAuthStore(authFilePath())
	if err != nil {
		authStoreWriteMu.Unlock()
		return nil, err
	}
	return func() {
		unlockFile()
		authStoreWriteMu.Unlock()
	}, nil
}

func saveStore(store *AuthStore) error {
	path := authFilePath()
	data, err := json.MarshalIndent(store, "", "  ")
	if err != nil {
		return err
	}

	// Use unified atomic write utility with explicit sync for flash storage reliability.
	return fileutil.WriteFileAtomic(path, data, 0o600)
}

func GetCredential(provider string) (*AuthCredential, error) {
	store, err := LoadStore()
	if err != nil {
		return nil, err
	}
	cred, ok := store.Credentials[canonicalCredentialID(provider)]
	if !ok {
		return nil, nil
	}
	return cred, nil
}

func getCredentialAfterWriters(credentialID string) (*AuthCredential, error) {
	unlock, err := lockAuthStoreForWrite()
	if err != nil {
		return nil, err
	}
	defer unlock()
	store, err := LoadStore()
	if err != nil {
		return nil, err
	}
	return cloneCredential(store.Credentials[canonicalCredentialID(credentialID)]), nil
}

func SetCredential(provider string, cred *AuthCredential) error {
	unlock, err := lockAuthStoreForWrite()
	if err != nil {
		return err
	}
	defer unlock()

	store, err := LoadStore()
	if err != nil {
		return err
	}

	canonical := canonicalCredentialID(provider)
	normalized := cloneCredential(cred)
	if normalized != nil {
		normalized.Provider = canonicalProvider(normalized.Provider)
		if normalized.Provider == "" {
			normalized.Provider = credentialProvider(canonical)
		}
	}

	store.Credentials[canonical] = normalized
	return saveStore(store)
}

// PersistCredentialIfCurrent saves replacement only when the stored
// credential still matches source. If another writer replaced the credential
// while network work was in flight, that replacement is left untouched and
// returned as the authoritative credential.
func PersistCredentialIfCurrent(
	credentialID string,
	source *AuthCredential,
	replacement *AuthCredential,
) (*AuthCredential, error) {
	authoritative, _, err := persistCredentialIfCurrentDetailed(
		credentialID,
		source,
		replacement,
	)
	return authoritative, err
}

func persistCredentialIfCurrentDetailed(
	credentialID string,
	source *AuthCredential,
	replacement *AuthCredential,
) (*AuthCredential, bool, error) {
	if source == nil {
		return nil, false, fmt.Errorf("source credential is required")
	}
	if replacement == nil {
		return nil, false, fmt.Errorf("replacement credential is required")
	}

	unlock, err := lockAuthStoreForWrite()
	if err != nil {
		return nil, false, err
	}
	defer unlock()

	store, err := LoadStore()
	if err != nil {
		return nil, false, err
	}
	canonical := canonicalCredentialID(credentialID)
	current := store.Credentials[canonical]
	if !reflect.DeepEqual(current, source) {
		return cloneCredential(current), false, nil
	}

	normalized, err := normalizeCredentialReplacement(canonical, current, replacement)
	if err != nil {
		return nil, false, err
	}
	if reflect.DeepEqual(current, normalized) {
		return cloneCredential(normalized), true, nil
	}
	store.Credentials[canonical] = normalized
	if err := saveStore(store); err != nil {
		return nil, false, err
	}
	return cloneCredential(normalized), true, nil
}

func lockCredentialRefresh(credentialID string) (func(), error) {
	canonical := canonicalCredentialID(credentialID)
	localLockValue, _ := credentialRefreshLocks.LoadOrStore(canonical, &sync.Mutex{})
	localLock := localLockValue.(*sync.Mutex)
	localLock.Lock()

	lockID := sha256.Sum256([]byte(canonical))
	unlockFile, err := lockAuthStore(fmt.Sprintf("%s.refresh.%x", authFilePath(), lockID))
	if err != nil {
		localLock.Unlock()
		return nil, err
	}
	return func() {
		unlockFile()
		localLock.Unlock()
	}, nil
}

// RefreshCredential serializes network work for one credential across
// PicoClaw goroutines and processes. Its refresh lock is independent from the
// auth-store write lock, so a UI renewal can replace the credential while the
// network request is in flight. The final compare-and-swap then keeps that UI
// renewal authoritative.
//
// shouldRefresh decides whether the current credential still needs network
// work. If refresh fails after an external writer replaces the credential
// without honoring the advisory lock, the replacement is returned instead of
// surfacing a stale refresh error.
func RefreshCredential(
	credentialID string,
	shouldRefresh func(*AuthCredential) bool,
	refresh RefreshCredentialFunc,
) (*AuthCredential, error) {
	result, err := RefreshCredentialDetailed(credentialID, shouldRefresh, refresh)
	return result.Credential, err
}

// RefreshCredentialDetailed is RefreshCredential with commit provenance. It
// lets callers avoid attaching response-only metadata (for example an ID
// token) to a concurrent UI renewal that won the compare-and-swap.
func RefreshCredentialDetailed(
	credentialID string,
	shouldRefresh func(*AuthCredential) bool,
	refresh RefreshCredentialFunc,
) (RefreshCredentialResult, error) {
	if shouldRefresh == nil {
		return RefreshCredentialResult{}, fmt.Errorf("refresh predicate is required")
	}
	if refresh == nil {
		return RefreshCredentialResult{}, fmt.Errorf("credential refresh callback is required")
	}

	unlock, err := lockCredentialRefresh(credentialID)
	if err != nil {
		return RefreshCredentialResult{}, err
	}
	defer unlock()

	current, err := GetCredential(credentialID)
	if err != nil {
		return RefreshCredentialResult{}, err
	}
	if current == nil || !shouldRefresh(current) {
		return RefreshCredentialResult{Credential: cloneCredential(current)}, nil
	}

	replacement, refreshErr := refresh(cloneCredential(current))
	if refreshErr != nil {
		latest, loadErr := getCredentialAfterWriters(credentialID)
		if loadErr == nil && !reflect.DeepEqual(latest, current) {
			return RefreshCredentialResult{Credential: cloneCredential(latest)}, nil
		}
		return RefreshCredentialResult{}, refreshErr
	}
	if replacement == nil {
		latest, loadErr := getCredentialAfterWriters(credentialID)
		if loadErr == nil && !reflect.DeepEqual(latest, current) {
			return RefreshCredentialResult{Credential: cloneCredential(latest)}, nil
		}
		return RefreshCredentialResult{}, fmt.Errorf("credential refresh returned nil")
	}

	authoritative, committed, err := persistCredentialIfCurrentDetailed(
		credentialID,
		current,
		replacement,
	)
	if err != nil {
		return RefreshCredentialResult{}, err
	}
	return RefreshCredentialResult{
		Credential: authoritative,
		Committed:  committed,
	}, nil
}

// UpdateCredential serializes a credential read-modify-write transaction
// across goroutines and PicoClaw processes. The callback runs while the auth
// store is locked so token refresh cannot race a launcher credential change.
func UpdateCredential(
	provider string,
	update func(current *AuthCredential) (*AuthCredential, error),
) (*AuthCredential, error) {
	if update == nil {
		return nil, fmt.Errorf("credential update callback is required")
	}
	unlock, err := lockAuthStoreForWrite()
	if err != nil {
		return nil, err
	}
	defer unlock()

	store, err := LoadStore()
	if err != nil {
		return nil, err
	}
	canonical := canonicalCredentialID(provider)
	replacement, err := update(cloneCredential(store.Credentials[canonical]))
	if err != nil {
		return nil, err
	}
	if replacement == nil {
		return nil, fmt.Errorf("credential update returned nil")
	}
	normalized := cloneCredential(replacement)
	normalized.Provider = canonicalProvider(normalized.Provider)
	if normalized.Provider == "" {
		normalized.Provider = credentialProvider(canonical)
	}
	if reflect.DeepEqual(store.Credentials[canonical], normalized) {
		return cloneCredential(normalized), nil
	}
	store.Credentials[canonical] = normalized
	if err := saveStore(store); err != nil {
		return nil, err
	}
	return cloneCredential(normalized), nil
}

func DeleteCredential(provider string) error {
	unlock, err := lockAuthStoreForWrite()
	if err != nil {
		return err
	}
	defer unlock()

	store, err := LoadStore()
	if err != nil {
		return err
	}
	delete(store.Credentials, canonicalCredentialID(provider))
	return saveStore(store)
}

func DeleteAllCredentials() error {
	unlock, err := lockAuthStoreForWrite()
	if err != nil {
		return err
	}
	defer unlock()

	path := authFilePath()
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}
