package auth

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/sipeed/picoclaw/internal/sqlitestore"
	"github.com/sipeed/picoclaw/pkg/database"
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
	authDatabaseAccessMu              sync.Mutex
	credentialRefreshLocks            sync.Map
	allowUnfencedAuthProviderForTests atomic.Bool
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

	credentialIDs := make([]string, 0, len(store.Credentials))
	for credentialID := range store.Credentials {
		credentialIDs = append(credentialIDs, credentialID)
	}
	sort.Strings(credentialIDs)
	for _, provider := range credentialIDs {
		cred := store.Credentials[provider]
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
	if useAuthBroker() {
		store := &AuthStore{Credentials: make(map[string]*AuthCredential)}
		cursor, revision := "", ""
		for {
			var response authLoadPageResponse
			err := callAuthBroker(
				"load-page",
				authLoadPageRequest{
					StoreID: GlobalAuthStoreID, Cursor: cursor, Revision: revision,
				},
				&response,
				false,
			)
			if err != nil {
				return nil, err
			}
			if !validAuthRevision(response.Revision) ||
				(revision != "" && response.Revision != revision) || len(response.Items) > authBrokerPageItems {
				return nil, database.NewError(database.CodeIntegrity, "auth page is invalid")
			}
			for _, item := range response.Items {
				if item.CredentialID == "" || item.Credential == nil ||
					canonicalCredentialID(item.CredentialID) != item.CredentialID {
					return nil, database.NewError(database.CodeIntegrity, "auth page is invalid")
				}
				if _, duplicate := store.Credentials[item.CredentialID]; duplicate ||
					len(store.Credentials) >= maximumAuthCredentials {
					return nil, database.NewError(database.CodeIntegrity, "auth page exceeds limits")
				}
				store.Credentials[item.CredentialID] = cloneCredential(item.Credential)
			}
			if response.Done {
				if response.Next != "" {
					return nil, database.NewError(database.CodeIntegrity, "auth page is invalid")
				}
				return store, nil
			}
			if response.Next == "" || response.Next <= cursor || len(response.Items) == 0 {
				return nil, database.NewError(database.CodeIntegrity, "auth page is invalid")
			}
			cursor, revision = response.Next, response.Revision
		}
	}
	ctx := context.Background()
	db, err := openAuthDatabase(ctx)
	if err != nil {
		return nil, err
	}
	defer closeAuthDatabase(db)
	return loadAuthStore(ctx, db)
}

func SaveStore(store *AuthStore) error {
	if useAuthBroker() {
		var response authMutationResponse
		request := authStoreRequest{StoreID: GlobalAuthStoreID, Store: store}
		raw, err := database.MarshalCanonical(request)
		if err != nil || len(raw) > authBrokerWriteBytes {
			return database.NewError(database.CodeInvalid, "auth mutation exceeds transport limits")
		}
		return callAuthBroker(
			"save", request, &response, true,
		)
	}
	normalized, err := normalizedAuthStore(store)
	if err != nil {
		return err
	}
	ctx := context.Background()
	db, unlock, err := openAuthDatabaseForWrite(ctx)
	if err != nil {
		return err
	}
	defer unlock()
	defer closeAuthDatabase(db)
	return sqlitestore.Immediate(ctx, db, func(conn *sql.Conn) error {
		return replaceAuthStore(ctx, conn, normalized, maximumAuthCredentials)
	})
}

func replaceAuthStore(
	ctx context.Context,
	conn *sql.Conn,
	store *AuthStore,
	maximum int,
) error {
	if store == nil || maximum < 1 || len(store.Credentials) > maximum {
		return errors.New("auth store exceeds its credential limit")
	}
	existing, err := existingCredentialIDs(ctx, conn)
	if err != nil {
		return err
	}
	credentialIDs := make([]string, 0, len(store.Credentials))
	for credentialID := range store.Credentials {
		credentialIDs = append(credentialIDs, credentialID)
	}
	sort.Strings(credentialIDs)
	retained := make(map[string]struct{}, len(credentialIDs))
	for _, credentialID := range credentialIDs {
		retained[credentialID] = struct{}{}
	}
	// Delete stale identities first so replacing a store already at its bound
	// never needs a transient over-limit state. The surrounding immediate
	// transaction restores these rows if a later validated upsert fails.
	for _, credentialID := range existing {
		if _, ok := retained[credentialID]; ok {
			continue
		}
		if _, err := conn.ExecContext(
			ctx,
			`DELETE FROM auth_credentials WHERE credential_id = ?`,
			credentialID,
		); err != nil {
			return err
		}
	}
	for _, credentialID := range credentialIDs {
		if err := upsertCredentialUnchecked(
			ctx,
			conn,
			credentialID,
			store.Credentials[credentialID],
		); err != nil {
			return err
		}
	}
	return nil
}

func GetCredential(provider string) (*AuthCredential, error) {
	if useAuthBroker() {
		var response authCredentialResponse
		err := callAuthBroker(
			"get",
			authCredentialRequest{
				StoreID: GlobalAuthStoreID, CredentialID: canonicalCredentialID(provider),
			},
			&response,
			false,
		)
		return response.Credential, err
	}
	ctx := context.Background()
	db, err := openAuthDatabase(ctx)
	if err != nil {
		return nil, err
	}
	defer closeAuthDatabase(db)
	credentialID := canonicalCredentialID(provider)
	_, credential, _, err := scanCredential(db.QueryRowContext(
		ctx,
		selectCredentialSQL+" WHERE credential_id = ?",
		credentialID,
	))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return credential, nil
}

func getCredentialAfterWriters(credentialID string) (*AuthCredential, error) {
	return GetCredential(credentialID)
}

func SetCredential(provider string, cred *AuthCredential) error {
	if useAuthBroker() {
		var response authMutationResponse
		return callAuthBroker(
			"set",
			authCredentialRequest{
				StoreID: GlobalAuthStoreID, CredentialID: canonicalCredentialID(provider), Credential: cred,
			},
			&response,
			true,
		)
	}
	canonical := canonicalCredentialID(provider)
	normalized, err := normalizeCredentialForStorage(canonical, cred)
	if err != nil {
		return err
	}
	ctx := context.Background()
	db, unlock, err := openAuthDatabaseForWrite(ctx)
	if err != nil {
		return err
	}
	defer unlock()
	defer closeAuthDatabase(db)
	return sqlitestore.Immediate(ctx, db, func(conn *sql.Conn) error {
		return upsertCredential(ctx, conn, canonical, normalized)
	})
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
	if useAuthBroker() {
		var response authCredentialResponse
		err := callAuthBroker(
			"compare-and-set",
			authCASRequest{
				StoreID: GlobalAuthStoreID, CredentialID: canonicalCredentialID(credentialID),
				Source: source, Replacement: replacement,
			},
			&response,
			true,
		)
		return response.Credential, response.Committed, err
	}

	ctx := context.Background()
	db, unlock, err := openAuthDatabaseForWrite(ctx)
	if err != nil {
		return nil, false, err
	}
	defer unlock()
	defer closeAuthDatabase(db)
	canonical := canonicalCredentialID(credentialID)
	var authoritative *AuthCredential
	committed := false
	err = sqlitestore.Immediate(ctx, db, func(conn *sql.Conn) error {
		current, version, _, loadErr := loadCredentialFromConn(ctx, conn, canonical)
		if loadErr != nil {
			return loadErr
		}
		if !reflect.DeepEqual(current, source) {
			authoritative = cloneCredential(current)
			return nil
		}
		normalized, normalizeErr := normalizeCredentialReplacement(canonical, current, replacement)
		if normalizeErr != nil {
			return normalizeErr
		}
		normalized, normalizeErr = normalizeCredentialForStorage(canonical, normalized)
		if normalizeErr != nil {
			return normalizeErr
		}
		if reflect.DeepEqual(current, normalized) {
			authoritative = cloneCredential(normalized)
			committed = true
			return nil
		}
		if updateErr := updateCredentialVersioned(
			ctx,
			conn,
			canonical,
			normalized,
			version,
		); updateErr != nil {
			return updateErr
		}
		authoritative = cloneCredential(normalized)
		committed = true
		return nil
	})
	if err != nil {
		return nil, false, err
	}
	return authoritative, committed, nil
}

func lockCredentialRefresh(credentialID string) (func(), error) {
	canonical := canonicalCredentialID(credentialID)
	localLockValue, _ := credentialRefreshLocks.LoadOrStore(canonical, &sync.Mutex{})
	localLock := localLockValue.(*sync.Mutex)
	localLock.Lock()
	return func() {
		localLock.Unlock()
	}, nil
}

// RefreshCredential serializes network work for one credential within this
// client process. Other clients may perform the same network refresh, but the
// broker-side compare-and-swap keeps one authoritative stored result. The
// process-local refresh lock is independent from broker writes, so a UI renewal
// can replace the credential while the network request is in flight.
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

// UpdateCredential applies a broker-versioned read-modify-write operation.
// Concurrent clients may evaluate callbacks, but only a replacement based on
// the authoritative credential version can commit.
func UpdateCredential(
	provider string,
	update func(current *AuthCredential) (*AuthCredential, error),
) (*AuthCredential, error) {
	if update == nil {
		return nil, fmt.Errorf("credential update callback is required")
	}
	if useAuthBroker() {
		current, err := GetCredential(provider)
		if err != nil {
			return nil, err
		}
		replacement, err := update(cloneCredential(current))
		if err != nil {
			return nil, err
		}
		if replacement == nil {
			return nil, fmt.Errorf("credential update returned nil")
		}
		var response authCredentialResponse
		err = callAuthBroker(
			"update",
			authCASRequest{
				StoreID: GlobalAuthStoreID, CredentialID: canonicalCredentialID(provider),
				Source: current, Replacement: replacement,
			},
			&response,
			true,
		)
		return response.Credential, err
	}
	ctx := context.Background()
	db, unlock, err := openAuthDatabaseForWrite(ctx)
	if err != nil {
		return nil, err
	}
	defer unlock()
	defer closeAuthDatabase(db)
	canonical := canonicalCredentialID(provider)
	var authoritative *AuthCredential
	err = sqlitestore.Immediate(ctx, db, func(conn *sql.Conn) error {
		current, version, found, loadErr := loadCredentialFromConn(ctx, conn, canonical)
		if loadErr != nil {
			return loadErr
		}
		replacement, callbackErr := update(cloneCredential(current))
		if callbackErr != nil {
			return callbackErr
		}
		if replacement == nil {
			return fmt.Errorf("credential update returned nil")
		}
		normalized, normalizeErr := normalizeCredentialForStorage(canonical, replacement)
		if normalizeErr != nil {
			return normalizeErr
		}
		if reflect.DeepEqual(current, normalized) {
			authoritative = cloneCredential(normalized)
			return nil
		}
		if !found {
			if _, insertErr := insertCredential(ctx, conn, canonical, normalized); insertErr != nil {
				return insertErr
			}
		} else if updateErr := updateCredentialVersioned(
			ctx,
			conn,
			canonical,
			normalized,
			version,
		); updateErr != nil {
			return updateErr
		}
		authoritative = cloneCredential(normalized)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return authoritative, nil
}

func DeleteCredential(provider string) error {
	if useAuthBroker() {
		var response authMutationResponse
		return callAuthBroker(
			"delete",
			authCredentialRequest{
				StoreID: GlobalAuthStoreID, CredentialID: canonicalCredentialID(provider),
			},
			&response,
			true,
		)
	}
	ctx := context.Background()
	db, unlock, err := openAuthDatabaseForWrite(ctx)
	if err != nil {
		return err
	}
	defer unlock()
	defer closeAuthDatabase(db)
	credentialID := canonicalCredentialID(provider)
	return sqlitestore.Immediate(ctx, db, func(conn *sql.Conn) error {
		_, err := conn.ExecContext(
			ctx,
			`DELETE FROM auth_credentials WHERE credential_id = ?`,
			credentialID,
		)
		return err
	})
}

func DeleteAllCredentials() error {
	if useAuthBroker() {
		var response authMutationResponse
		return callAuthBroker(
			"delete-all", authEmptyRequest{StoreID: GlobalAuthStoreID}, &response, true,
		)
	}
	ctx := context.Background()
	db, unlock, err := openAuthDatabaseForWrite(ctx)
	if err != nil {
		return err
	}
	defer unlock()
	defer closeAuthDatabase(db)
	return sqlitestore.Immediate(ctx, db, func(conn *sql.Conn) error {
		_, err := conn.ExecContext(ctx, `DELETE FROM auth_credentials`)
		return err
	})
}
