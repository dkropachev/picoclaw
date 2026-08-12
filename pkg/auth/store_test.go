package auth

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sipeed/picoclaw/pkg/config"
)

func setTestAuthHome(t *testing.T) string {
	t.Helper()

	tmpDir := t.TempDir()
	t.Setenv(config.EnvHome, filepath.Join(tmpDir, ".picoclaw"))
	return tmpDir
}

func authCredentialsEqual(left, right *AuthCredential) bool {
	if left == nil || right == nil {
		return left == right
	}

	leftCopy := *left
	rightCopy := *right
	expiresAtEqual := leftCopy.ExpiresAt.Equal(rightCopy.ExpiresAt)
	leftCopy.ExpiresAt = time.Time{}
	rightCopy.ExpiresAt = time.Time{}
	return expiresAtEqual && reflect.DeepEqual(leftCopy, rightCopy)
}

func TestAuthCredentialIsExpired(t *testing.T) {
	tests := []struct {
		name      string
		expiresAt time.Time
		want      bool
	}{
		{"zero time", time.Time{}, false},
		{"future", time.Now().Add(time.Hour), false},
		{"past", time.Now().Add(-time.Hour), true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := &AuthCredential{ExpiresAt: tt.expiresAt}
			if got := c.IsExpired(); got != tt.want {
				t.Errorf("IsExpired() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestAuthCredentialNeedsRefresh(t *testing.T) {
	tests := []struct {
		name      string
		expiresAt time.Time
		want      bool
	}{
		{"zero time", time.Time{}, false},
		{"far future", time.Now().Add(time.Hour), false},
		{"within 5 min", time.Now().Add(3 * time.Minute), true},
		{"already expired", time.Now().Add(-time.Minute), true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := &AuthCredential{ExpiresAt: tt.expiresAt}
			if got := c.NeedsRefresh(); got != tt.want {
				t.Errorf("NeedsRefresh() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestStoreRoundtrip(t *testing.T) {
	setTestAuthHome(t)

	cred := &AuthCredential{
		AccessToken:       "test-access-token",
		RefreshToken:      "test-refresh-token",
		TokenType:         "Bearer",
		OAuthTokenURL:     "https://auth.example.test/token",
		OAuthClientID:     "client-id",
		OAuthClientSecret: "client-secret",
		OAuthAuthStyle:    "header",
		AccountID:         "acct-123",
		ExpiresAt:         time.Now().Add(time.Hour).Truncate(time.Second),
		Provider:          "openai",
		AuthMethod:        "oauth",
	}

	if err := SetCredential("openai", cred); err != nil {
		t.Fatalf("SetCredential() error: %v", err)
	}

	loaded, err := GetCredential("openai")
	if err != nil {
		t.Fatalf("GetCredential() error: %v", err)
	}
	if loaded == nil {
		t.Fatal("GetCredential() returned nil")
	}
	if loaded.AccessToken != cred.AccessToken {
		t.Errorf("AccessToken = %q, want %q", loaded.AccessToken, cred.AccessToken)
	}
	if loaded.RefreshToken != cred.RefreshToken {
		t.Errorf("RefreshToken = %q, want %q", loaded.RefreshToken, cred.RefreshToken)
	}
	if loaded.TokenType != cred.TokenType ||
		loaded.OAuthTokenURL != cred.OAuthTokenURL ||
		loaded.OAuthClientID != cred.OAuthClientID ||
		loaded.OAuthClientSecret != cred.OAuthClientSecret ||
		loaded.OAuthAuthStyle != cred.OAuthAuthStyle {
		t.Errorf("OAuth refresh metadata was not preserved: %#v", loaded)
	}
	if loaded.Provider != cred.Provider {
		t.Errorf("Provider = %q, want %q", loaded.Provider, cred.Provider)
	}
}

func TestStoreFilePermissions(t *testing.T) {
	tmpDir := setTestAuthHome(t)

	cred := &AuthCredential{
		AccessToken: "secret-token",
		Provider:    "openai",
		AuthMethod:  "oauth",
	}
	if err := SetCredential("openai", cred); err != nil {
		t.Fatalf("SetCredential() error: %v", err)
	}

	path := filepath.Join(tmpDir, ".picoclaw", "auth.json")
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat() error: %v", err)
	}
	perm := info.Mode().Perm()
	if runtime.GOOS == "windows" {
		return
	}
	if perm != 0o600 {
		t.Errorf("file permissions = %o, want 0600", perm)
	}
}

func TestStoreMultiProvider(t *testing.T) {
	setTestAuthHome(t)

	openaiCred := &AuthCredential{AccessToken: "openai-token", Provider: "openai", AuthMethod: "oauth"}
	anthropicCred := &AuthCredential{AccessToken: "anthropic-token", Provider: "anthropic", AuthMethod: "token"}

	if err := SetCredential("openai", openaiCred); err != nil {
		t.Fatalf("SetCredential(openai) error: %v", err)
	}
	if err := SetCredential("anthropic", anthropicCred); err != nil {
		t.Fatalf("SetCredential(anthropic) error: %v", err)
	}

	loaded, err := GetCredential("openai")
	if err != nil {
		t.Fatalf("GetCredential(openai) error: %v", err)
	}
	if loaded.AccessToken != "openai-token" {
		t.Errorf("openai token = %q, want %q", loaded.AccessToken, "openai-token")
	}

	loaded, err = GetCredential("anthropic")
	if err != nil {
		t.Fatalf("GetCredential(anthropic) error: %v", err)
	}
	if loaded.AccessToken != "anthropic-token" {
		t.Errorf("anthropic token = %q, want %q", loaded.AccessToken, "anthropic-token")
	}
}

func TestStoreMultipleCredentialsForSameProvider(t *testing.T) {
	setTestAuthHome(t)

	if err := SetCredential("openai", &AuthCredential{
		AccessToken: "default-token",
		Provider:    "openai",
		AuthMethod:  "oauth",
	}); err != nil {
		t.Fatalf("SetCredential(openai) error: %v", err)
	}
	if err := SetCredential("openai:work", &AuthCredential{
		AccessToken: "work-token",
		Provider:    "openai",
		AuthMethod:  "oauth",
	}); err != nil {
		t.Fatalf("SetCredential(openai:work) error: %v", err)
	}

	defaultCred, err := GetCredential("openai")
	if err != nil {
		t.Fatalf("GetCredential(openai) error: %v", err)
	}
	if defaultCred.AccessToken != "default-token" {
		t.Fatalf("default token = %q, want default-token", defaultCred.AccessToken)
	}
	workCred, err := GetCredential("openai:work")
	if err != nil {
		t.Fatalf("GetCredential(openai:work) error: %v", err)
	}
	if workCred.AccessToken != "work-token" {
		t.Fatalf("work token = %q, want work-token", workCred.AccessToken)
	}
	if workCred.Provider != "openai" {
		t.Fatalf("work provider = %q, want openai", workCred.Provider)
	}
}

func TestSetCredentialConcurrentUpdatesPreserveAllCredentials(t *testing.T) {
	setTestAuthHome(t)

	const credentialCount = 32
	start := make(chan struct{})
	errs := make(chan error, credentialCount)

	var wg sync.WaitGroup
	wg.Add(credentialCount)
	for i := range credentialCount {
		go func() {
			defer wg.Done()
			<-start

			credentialID := fmt.Sprintf("openai:work-%02d", i)
			errs <- SetCredential(credentialID, &AuthCredential{
				AccessToken: fmt.Sprintf("token-%02d", i),
				Provider:    "openai",
				AuthMethod:  "oauth",
			})
		}()
	}

	close(start)
	wg.Wait()
	close(errs)

	for err := range errs {
		if err != nil {
			t.Fatalf("SetCredential() error: %v", err)
		}
	}

	store, err := LoadStore()
	if err != nil {
		t.Fatalf("LoadStore() error: %v", err)
	}
	if len(store.Credentials) != credentialCount {
		t.Fatalf("credential count = %d, want %d", len(store.Credentials), credentialCount)
	}
	for i := range credentialCount {
		credentialID := fmt.Sprintf("openai:work-%02d", i)
		cred := store.Credentials[credentialID]
		if cred == nil {
			t.Errorf("credential %q is missing", credentialID)
			continue
		}
		wantToken := fmt.Sprintf("token-%02d", i)
		if cred.AccessToken != wantToken {
			t.Errorf("credential %q token = %q, want %q", credentialID, cred.AccessToken, wantToken)
		}
	}
}

func TestPersistCredentialIfCurrentCommitsWhenSourceIsCurrent(t *testing.T) {
	setTestAuthHome(t)

	expiresAt := time.Now().Add(-time.Hour).Round(time.Second)
	if err := SetCredential("openai:work", &AuthCredential{
		AccessToken:  "expired-token",
		RefreshToken: "refresh-token",
		ExpiresAt:    expiresAt,
		Provider:     "openai",
		AuthMethod:   "oauth",
	}); err != nil {
		t.Fatalf("SetCredential() error: %v", err)
	}
	source, err := GetCredential("openai:work")
	if err != nil {
		t.Fatalf("GetCredential() error: %v", err)
	}
	refreshed := &AuthCredential{
		AccessToken:  "refreshed-token",
		RefreshToken: "refresh-token",
		ExpiresAt:    time.Now().Add(time.Hour).Round(time.Second),
		Provider:     "openai",
		AuthMethod:   "oauth",
	}

	authoritative, err := PersistCredentialIfCurrent("openai:work", source, refreshed)
	if err != nil {
		t.Fatalf("PersistCredentialIfCurrent() error: %v", err)
	}
	if !authCredentialsEqual(authoritative, refreshed) {
		t.Fatalf("authoritative credential = %+v, want refreshed credential %+v", authoritative, refreshed)
	}
	stored, err := GetCredential("openai:work")
	if err != nil {
		t.Fatalf("GetCredential() error: %v", err)
	}
	if !authCredentialsEqual(stored, refreshed) {
		t.Fatalf("stored credential = %+v, want refreshed credential %+v", stored, refreshed)
	}
}

func TestPersistCredentialIfCurrentKeepsConcurrentRenewal(t *testing.T) {
	setTestAuthHome(t)

	if err := SetCredential("openai:work", &AuthCredential{
		AccessToken:  "expired-token",
		RefreshToken: "old-refresh-token",
		ExpiresAt:    time.Now().Add(-time.Hour).Round(time.Second),
		Provider:     "openai",
		AuthMethod:   "oauth",
	}); err != nil {
		t.Fatalf("SetCredential(expired) error: %v", err)
	}
	source, err := GetCredential("openai:work")
	if err != nil {
		t.Fatalf("GetCredential() error: %v", err)
	}
	renewed := &AuthCredential{
		AccessToken:  "renewed-token",
		RefreshToken: "new-refresh-token",
		ExpiresAt:    time.Now().Add(2 * time.Hour).Round(time.Second),
		Provider:     "openai",
		AuthMethod:   "oauth",
		AccountID:    "renewed-account",
	}
	if setErr := SetCredential("openai:work", renewed); setErr != nil {
		t.Fatalf("SetCredential(renewed) error: %v", setErr)
	}
	staleRefresh := &AuthCredential{
		AccessToken:  "stale-refreshed-token",
		RefreshToken: "old-refresh-token",
		ExpiresAt:    time.Now().Add(time.Hour).Round(time.Second),
		Provider:     "openai",
		AuthMethod:   "oauth",
	}

	authoritative, err := PersistCredentialIfCurrent("openai:work", source, staleRefresh)
	if err != nil {
		t.Fatalf("PersistCredentialIfCurrent() error: %v", err)
	}
	if !authCredentialsEqual(authoritative, renewed) {
		t.Fatalf("authoritative credential = %+v, want concurrent renewal %+v", authoritative, renewed)
	}
	stored, err := GetCredential("openai:work")
	if err != nil {
		t.Fatalf("GetCredential() error: %v", err)
	}
	if !authCredentialsEqual(stored, renewed) {
		t.Fatalf("stored credential = %+v, want concurrent renewal %+v", stored, renewed)
	}
}

func TestRefreshCredentialDetailedReportsIdenticalConcurrentRenewalNotCommitted(t *testing.T) {
	setTestAuthHome(t)
	credentialID := "openai:work"
	if err := SetCredential(credentialID, &AuthCredential{
		AccessToken:  "expired-token",
		RefreshToken: "old-refresh-token",
		ExpiresAt:    time.Now().Add(-time.Hour).Round(time.Second),
		Provider:     "openai",
		AuthMethod:   "oauth",
	}); err != nil {
		t.Fatalf("SetCredential(expired) error: %v", err)
	}
	renewed := &AuthCredential{
		AccessToken:  "renewed-token",
		RefreshToken: "new-refresh-token",
		ExpiresAt:    time.Now().Add(time.Hour).Round(time.Second),
		Provider:     "openai",
		AuthMethod:   "oauth",
		AccountID:    "renewed-account",
	}

	result, err := RefreshCredentialDetailed(
		credentialID,
		func(current *AuthCredential) bool { return current.NeedsRefresh() },
		func(*AuthCredential) (*AuthCredential, error) {
			if setErr := SetCredential(credentialID, renewed); setErr != nil {
				t.Fatalf("SetCredential(renewed) error: %v", setErr)
			}
			return cloneCredential(renewed), nil
		},
	)
	if err != nil {
		t.Fatalf("RefreshCredentialDetailed() error: %v", err)
	}
	if result.Committed {
		t.Fatal("Committed = true, want false for a concurrent UI replacement")
	}
	if !authCredentialsEqual(result.Credential, renewed) {
		t.Fatalf("credential = %+v, want concurrent renewal %+v", result.Credential, renewed)
	}
}

func TestRefreshCredentialSerializesNetworkRefresh(t *testing.T) {
	setTestAuthHome(t)
	credentialID := "openai:work"
	if err := SetCredential(credentialID, &AuthCredential{
		AccessToken:  "expired-token",
		RefreshToken: "rotating-refresh-token",
		ExpiresAt:    time.Now().Add(-time.Hour),
		Provider:     "openai",
		AuthMethod:   "oauth",
	}); err != nil {
		t.Fatalf("SetCredential() error: %v", err)
	}

	firstRefreshStarted := make(chan struct{})
	releaseFirstRefresh := make(chan struct{})
	var callsMu sync.Mutex
	refreshCalls := 0
	refresh := func(current *AuthCredential) (*AuthCredential, error) {
		callsMu.Lock()
		refreshCalls++
		call := refreshCalls
		callsMu.Unlock()
		if call == 1 {
			close(firstRefreshStarted)
			<-releaseFirstRefresh
		}
		updated := *current
		updated.AccessToken = "refreshed-token"
		updated.RefreshToken = "next-rotating-refresh-token"
		updated.ExpiresAt = time.Now().Add(time.Hour)
		return &updated, nil
	}
	shouldRefresh := func(current *AuthCredential) bool {
		return current != nil && current.NeedsRefresh() && current.RefreshToken != ""
	}

	results := make(chan *AuthCredential, 2)
	errs := make(chan error, 2)
	go func() {
		result, err := RefreshCredential(credentialID, shouldRefresh, refresh)
		results <- result
		errs <- err
	}()
	<-firstRefreshStarted
	go func() {
		result, err := RefreshCredential(credentialID, shouldRefresh, refresh)
		results <- result
		errs <- err
	}()
	close(releaseFirstRefresh)

	for range 2 {
		if err := <-errs; err != nil {
			t.Fatalf("RefreshCredential() error: %v", err)
		}
		result := <-results
		if result == nil || result.AccessToken != "refreshed-token" {
			t.Fatalf("RefreshCredential() result = %+v, want refreshed credential", result)
		}
	}
	callsMu.Lock()
	defer callsMu.Unlock()
	if refreshCalls != 1 {
		t.Fatalf("network refresh calls = %d, want 1", refreshCalls)
	}
}

func TestRefreshCredentialCanonicalizesNamedProviderAliases(t *testing.T) {
	setTestAuthHome(t)
	if err := SetCredential("antigravity:work", &AuthCredential{
		AccessToken:  "expired-token",
		RefreshToken: "refresh-token",
		ExpiresAt:    time.Now().Add(-time.Hour),
		Provider:     "antigravity",
		AuthMethod:   "oauth",
	}); err != nil {
		t.Fatalf("SetCredential() error: %v", err)
	}

	loaded, err := GetCredential("google-antigravity:work")
	if err != nil || loaded == nil || loaded.AccessToken != "expired-token" {
		t.Fatalf("canonical alias lookup = (%+v, %v)", loaded, err)
	}
	store, err := LoadStore()
	if err != nil {
		t.Fatal(err)
	}
	if len(store.Credentials) != 1 || store.Credentials["google-antigravity:work"] == nil {
		t.Fatalf("canonicalized store = %+v", store.Credentials)
	}

	var refreshCalls int
	first, err := RefreshCredential(
		"google-antigravity:work",
		func(current *AuthCredential) bool { return current.NeedsRefresh() },
		func(current *AuthCredential) (*AuthCredential, error) {
			refreshCalls++
			updated := *current
			updated.AccessToken = "refreshed-token"
			updated.ExpiresAt = time.Now().Add(time.Hour)
			return &updated, nil
		},
	)
	if err != nil || first == nil || first.AccessToken != "refreshed-token" {
		t.Fatalf("first refresh = (%+v, %v)", first, err)
	}
	second, err := RefreshCredential(
		"antigravity:work",
		func(current *AuthCredential) bool { return current.NeedsRefresh() },
		func(*AuthCredential) (*AuthCredential, error) {
			refreshCalls++
			return nil, fmt.Errorf("alias used a second refresh lock")
		},
	)
	if err != nil || second == nil || second.AccessToken != "refreshed-token" {
		t.Fatalf("alias refresh = (%+v, %v)", second, err)
	}
	if refreshCalls != 1 {
		t.Fatalf("refresh calls = %d, want 1", refreshCalls)
	}
}

func TestRefreshCredentialSerializesAcrossProcessesAndAliases(t *testing.T) {
	if os.Getenv("PICOCLAW_AUTH_REFRESH_HELPER") == "1" {
		credentialID := os.Getenv("PICOCLAW_AUTH_REFRESH_CREDENTIAL_ID")
		counterPath := os.Getenv("PICOCLAW_AUTH_REFRESH_COUNTER")
		_, err := RefreshCredential(
			credentialID,
			func(current *AuthCredential) bool { return current.NeedsRefresh() },
			func(current *AuthCredential) (*AuthCredential, error) {
				counter, openErr := os.OpenFile(counterPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
				if openErr != nil {
					return nil, openErr
				}
				_, writeErr := counter.WriteString("refresh\n")
				closeErr := counter.Close()
				if writeErr != nil {
					return nil, writeErr
				}
				if closeErr != nil {
					return nil, closeErr
				}
				time.Sleep(150 * time.Millisecond)
				updated := *current
				updated.AccessToken = "process-refreshed-token"
				updated.ExpiresAt = time.Now().Add(time.Hour)
				return &updated, nil
			},
		)
		if err != nil {
			t.Fatalf("helper RefreshCredential() error: %v", err)
		}
		return
	}

	testRoot := setTestAuthHome(t)
	credentialID := "google-antigravity:process-work"
	if err := SetCredential(credentialID, &AuthCredential{
		AccessToken:  "expired-token",
		RefreshToken: "rotating-token",
		ExpiresAt:    time.Now().Add(-time.Hour),
		Provider:     "google-antigravity",
		AuthMethod:   "oauth",
	}); err != nil {
		t.Fatalf("SetCredential() error: %v", err)
	}
	counterPath := filepath.Join(testRoot, "refresh-counter")
	ids := []string{"antigravity:process-work", "google-antigravity:process-work"}
	commands := make([]*exec.Cmd, 0, len(ids))
	outputs := make([]strings.Builder, len(ids))
	for index, id := range ids {
		cmd := exec.Command(os.Args[0], "-test.run=^TestRefreshCredentialSerializesAcrossProcessesAndAliases$")
		cmd.Env = append(os.Environ(),
			"PICOCLAW_AUTH_REFRESH_HELPER=1",
			"PICOCLAW_AUTH_REFRESH_CREDENTIAL_ID="+id,
			"PICOCLAW_AUTH_REFRESH_COUNTER="+counterPath,
		)
		cmd.Stdout = &outputs[index]
		cmd.Stderr = &outputs[index]
		if err := cmd.Start(); err != nil {
			t.Fatalf("start helper: %v", err)
		}
		commands = append(commands, cmd)
	}
	for index, cmd := range commands {
		if err := cmd.Wait(); err != nil {
			t.Fatalf("helper process error: %v\n%s", err, outputs[index].String())
		}
	}
	data, err := os.ReadFile(counterPath)
	if err != nil {
		t.Fatalf("ReadFile(counter) error: %v", err)
	}
	if got := strings.Count(string(data), "refresh\n"); got != 1 {
		t.Fatalf("cross-process refresh calls = %d, want 1; counter=%q", got, data)
	}
}

func TestRefreshCredentialReturnsConcurrentRenewalAfterRefreshError(t *testing.T) {
	setTestAuthHome(t)
	credentialID := "openai:work"
	if err := SetCredential(credentialID, &AuthCredential{
		AccessToken:  "expired-token",
		RefreshToken: "old-refresh-token",
		ExpiresAt:    time.Now().Add(-time.Hour),
		Provider:     "openai",
		AuthMethod:   "oauth",
	}); err != nil {
		t.Fatalf("SetCredential() error: %v", err)
	}
	renewed := &AuthCredential{
		AccessToken:  "renewed-token",
		RefreshToken: "new-refresh-token",
		ExpiresAt:    time.Now().Add(time.Hour).Round(time.Second),
		Provider:     "openai",
		AuthMethod:   "oauth",
	}

	got, err := RefreshCredential(
		credentialID,
		func(current *AuthCredential) bool { return current.NeedsRefresh() },
		func(*AuthCredential) (*AuthCredential, error) {
			if setErr := SetCredential(credentialID, renewed); setErr != nil {
				t.Fatalf("SetCredential(renewed) error: %v", setErr)
			}
			return nil, fmt.Errorf("refresh token was rotated")
		},
	)
	if err != nil {
		t.Fatalf("RefreshCredential() error: %v", err)
	}
	if !authCredentialsEqual(got, renewed) {
		t.Fatalf("RefreshCredential() = %+v, want concurrent renewal %+v", got, renewed)
	}
}

func TestRefreshCredentialReturnsConcurrentRenewalAfterNilRefreshResult(t *testing.T) {
	setTestAuthHome(t)
	credentialID := "openai:work"
	if err := SetCredential(credentialID, &AuthCredential{
		AccessToken:  "expired-token",
		RefreshToken: "old-refresh-token",
		ExpiresAt:    time.Now().Add(-time.Hour),
		Provider:     "openai",
		AuthMethod:   "oauth",
	}); err != nil {
		t.Fatalf("SetCredential() error: %v", err)
	}
	renewed := &AuthCredential{
		AccessToken: "renewed-token",
		Provider:    "openai",
		AuthMethod:  "token",
	}

	got, err := RefreshCredential(
		credentialID,
		func(current *AuthCredential) bool {
			return current.AuthMethod == "oauth" && current.NeedsRefresh()
		},
		func(*AuthCredential) (*AuthCredential, error) {
			if setErr := SetCredential(credentialID, renewed); setErr != nil {
				t.Fatalf("SetCredential(renewed) error: %v", setErr)
			}
			return nil, nil
		},
	)
	if err != nil {
		t.Fatalf("RefreshCredential() error: %v", err)
	}
	if !authCredentialsEqual(got, renewed) {
		t.Fatalf("RefreshCredential() = %+v, want concurrent renewal %+v", got, renewed)
	}
}

func TestRefreshCredentialReturnsChangedAuthoritativeCredentialForCallerRetry(t *testing.T) {
	setTestAuthHome(t)
	credentialID := "openai:work"
	if err := SetCredential(credentialID, &AuthCredential{
		AccessToken:  "expired-token",
		RefreshToken: "old-refresh-token",
		ExpiresAt:    time.Now().Add(-time.Hour),
		Provider:     "openai",
		AuthMethod:   "oauth",
	}); err != nil {
		t.Fatalf("SetCredential() error: %v", err)
	}

	got, err := RefreshCredential(
		credentialID,
		func(current *AuthCredential) bool { return current.NeedsRefresh() },
		func(*AuthCredential) (*AuthCredential, error) {
			if setErr := SetCredential(credentialID, &AuthCredential{
				AccessToken:  "different-expired-token",
				RefreshToken: "different-refresh-token",
				ExpiresAt:    time.Now().Add(-time.Minute),
				Provider:     "openai",
				AuthMethod:   "oauth",
			}); setErr != nil {
				t.Fatalf("SetCredential(replacement) error: %v", setErr)
			}
			return nil, fmt.Errorf("refresh failed")
		},
	)
	if err != nil {
		t.Fatalf("RefreshCredential() error = %v", err)
	}
	if got == nil || got.AccessToken != "different-expired-token" || !got.NeedsRefresh() {
		t.Fatalf("RefreshCredential() = %+v, want changed authoritative credential for retry", got)
	}
}

func TestPersistCredentialIfCurrentNormalizesAndValidatesProvider(t *testing.T) {
	setTestAuthHome(t)
	credentialID := "openai:work"
	if err := SetCredential(credentialID, &AuthCredential{
		AccessToken: "old-token",
		Provider:    "openai",
		AuthMethod:  "token",
	}); err != nil {
		t.Fatalf("SetCredential() error: %v", err)
	}
	source, err := GetCredential(credentialID)
	if err != nil {
		t.Fatalf("GetCredential() error: %v", err)
	}

	authoritative, err := PersistCredentialIfCurrent(credentialID, source, &AuthCredential{
		AccessToken: "new-token",
		AuthMethod:  "token",
	})
	if err != nil {
		t.Fatalf("PersistCredentialIfCurrent() error: %v", err)
	}
	if authoritative.Provider != "openai" {
		t.Fatalf("provider = %q, want openai", authoritative.Provider)
	}

	_, err = PersistCredentialIfCurrent(credentialID, authoritative, &AuthCredential{
		AccessToken: "wrong-provider-token",
		Provider:    "anthropic",
		AuthMethod:  "token",
	})
	if err == nil {
		t.Fatal("PersistCredentialIfCurrent() error = nil, want provider mismatch")
	}
	stored, getErr := GetCredential(credentialID)
	if getErr != nil {
		t.Fatalf("GetCredential() error: %v", getErr)
	}
	if !authCredentialsEqual(stored, authoritative) {
		t.Fatalf("stored credential = %+v, want unchanged %+v", stored, authoritative)
	}
}

func TestNormalizeCredentialID(t *testing.T) {
	tests := []struct {
		name         string
		provider     string
		credentialID string
		want         string
		wantErr      bool
	}{
		{name: "default", provider: "openai", want: "openai"},
		{name: "bare name", provider: "openai", credentialID: "work", want: "openai:work"},
		{name: "qualified", provider: "openai", credentialID: "openai:work", want: "openai:work"},
		{name: "case and whitespace", provider: "OpenAI", credentialID: " Work ", want: "openai:work"},
		{
			name:         "antigravity alias",
			provider:     "antigravity",
			credentialID: "personal",
			want:         "google-antigravity:personal",
		},
		{
			name:         "antigravity alias default",
			provider:     "google-antigravity",
			credentialID: "antigravity",
			want:         "google-antigravity",
		},
		{
			name:         "provider mismatch",
			provider:     "openai",
			credentialID: "anthropic:work",
			wantErr:      true,
		},
		{name: "invalid chars", provider: "openai", credentialID: "work account", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := NormalizeCredentialID(tt.provider, tt.credentialID)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("NormalizeCredentialID() error = nil, want error")
				}
				return
			}
			if err != nil {
				t.Fatalf("NormalizeCredentialID() error = %v", err)
			}
			if got != tt.want {
				t.Fatalf("NormalizeCredentialID() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestDeleteCredential(t *testing.T) {
	setTestAuthHome(t)

	cred := &AuthCredential{AccessToken: "to-delete", Provider: "openai", AuthMethod: "oauth"}
	if err := SetCredential("openai", cred); err != nil {
		t.Fatalf("SetCredential() error: %v", err)
	}

	if err := DeleteCredential("openai"); err != nil {
		t.Fatalf("DeleteCredential() error: %v", err)
	}

	loaded, err := GetCredential("openai")
	if err != nil {
		t.Fatalf("GetCredential() error: %v", err)
	}
	if loaded != nil {
		t.Error("expected nil after delete")
	}
}

func TestLoadStoreEmpty(t *testing.T) {
	setTestAuthHome(t)

	store, err := LoadStore()
	if err != nil {
		t.Fatalf("LoadStore() error: %v", err)
	}
	if store == nil {
		t.Fatal("LoadStore() returned nil")
	}
	if len(store.Credentials) != 0 {
		t.Errorf("expected empty credentials, got %d", len(store.Credentials))
	}
}

func TestGetCredentialCanonicalizesLegacyAntigravityProvider(t *testing.T) {
	tmpDir := setTestAuthHome(t)

	expiresAt := time.Date(2026, 4, 16, 10, 0, 0, 0, time.UTC)
	store := map[string]any{
		"credentials": map[string]any{
			"antigravity": map[string]any{
				"access_token": "legacy-token",
				"expires_at":   expiresAt.Format(time.RFC3339),
				"provider":     "antigravity",
				"auth_method":  "oauth",
				"project_id":   "project-1",
			},
		},
	}
	data, err := json.Marshal(store)
	if err != nil {
		t.Fatalf("json.Marshal() error: %v", err)
	}
	path := filepath.Join(tmpDir, ".picoclaw", "auth.json")
	err = os.MkdirAll(filepath.Dir(path), 0o755)
	if err != nil {
		t.Fatalf("MkdirAll() error: %v", err)
	}
	err = os.WriteFile(path, data, 0o600)
	if err != nil {
		t.Fatalf("WriteFile() error: %v", err)
	}

	cred, err := GetCredential("google-antigravity")
	if err != nil {
		t.Fatalf("GetCredential() error: %v", err)
	}
	if cred == nil {
		t.Fatal("GetCredential() returned nil")
	}
	if cred.Provider != "google-antigravity" {
		t.Fatalf("Provider = %q, want %q", cred.Provider, "google-antigravity")
	}
	if !cred.ExpiresAt.Equal(expiresAt) {
		t.Fatalf("ExpiresAt = %v, want %v", cred.ExpiresAt, expiresAt)
	}
}

func TestLoadStoreKeepsAntigravityTokenBundleAtomicWhenNormalizingAliases(t *testing.T) {
	tmpDir := setTestAuthHome(t)

	legacyExpiry := time.Date(2026, 4, 16, 10, 0, 0, 0, time.UTC)
	refreshedExpiry := time.Date(2026, 4, 16, 12, 0, 0, 0, time.UTC)
	store := map[string]any{
		"credentials": map[string]any{
			"antigravity": map[string]any{
				"access_token":  "legacy-token",
				"refresh_token": "legacy-refresh",
				"expires_at":    legacyExpiry.Format(time.RFC3339),
				"provider":      "antigravity",
				"auth_method":   "oauth",
				"email":         "legacy@example.com",
			},
			"google-antigravity": map[string]any{
				"access_token": "fresh-token",
				"expires_at":   refreshedExpiry.Format(time.RFC3339),
				"provider":     "google-antigravity",
				"auth_method":  "oauth",
				"project_id":   "project-2",
			},
		},
	}
	data, err := json.Marshal(store)
	if err != nil {
		t.Fatalf("json.Marshal() error: %v", err)
	}
	path := filepath.Join(tmpDir, ".picoclaw", "auth.json")
	err = os.MkdirAll(filepath.Dir(path), 0o755)
	if err != nil {
		t.Fatalf("MkdirAll() error: %v", err)
	}
	err = os.WriteFile(path, data, 0o600)
	if err != nil {
		t.Fatalf("WriteFile() error: %v", err)
	}

	loaded, err := LoadStore()
	if err != nil {
		t.Fatalf("LoadStore() error: %v", err)
	}
	if len(loaded.Credentials) != 1 {
		t.Fatalf("credential count = %d, want 1", len(loaded.Credentials))
	}

	cred := loaded.Credentials["google-antigravity"]
	if cred == nil {
		t.Fatal("google-antigravity credential missing")
	}
	if cred.AccessToken != "fresh-token" {
		t.Fatalf("AccessToken = %q, want %q", cred.AccessToken, "fresh-token")
	}
	if cred.RefreshToken != "" {
		t.Fatalf("RefreshToken = %q, want no token from stale alias", cred.RefreshToken)
	}
	if cred.Email != "" {
		t.Fatalf("Email = %q, want no metadata from stale alias", cred.Email)
	}
	if cred.ProjectID != "project-2" {
		t.Fatalf("ProjectID = %q, want %q", cred.ProjectID, "project-2")
	}
	if !cred.ExpiresAt.Equal(refreshedExpiry) {
		t.Fatalf("ExpiresAt = %v, want %v", cred.ExpiresAt, refreshedExpiry)
	}
}

func TestLoadStorePrefersCanonicalKeyWhenExpiryMatchesAlias(t *testing.T) {
	tmpDir := setTestAuthHome(t)

	expiresAt := time.Date(2026, 4, 16, 12, 0, 0, 0, time.UTC)
	store := map[string]any{
		"credentials": map[string]any{
			"antigravity": map[string]any{
				"access_token":  "legacy-token",
				"refresh_token": "legacy-refresh",
				"expires_at":    expiresAt.Format(time.RFC3339),
				"provider":      "antigravity",
				"auth_method":   "oauth",
				"email":         "legacy@example.com",
			},
			" Google-Antigravity ": map[string]any{
				"access_token": "fresh-token",
				"expires_at":   expiresAt.Format(time.RFC3339),
				"provider":     " Google-Antigravity ",
				"auth_method":  "oauth",
				"project_id":   "project-2",
			},
		},
	}
	data, err := json.Marshal(store)
	if err != nil {
		t.Fatalf("json.Marshal() error: %v", err)
	}
	path := filepath.Join(tmpDir, ".picoclaw", "auth.json")
	err = os.MkdirAll(filepath.Dir(path), 0o755)
	if err != nil {
		t.Fatalf("MkdirAll() error: %v", err)
	}
	err = os.WriteFile(path, data, 0o600)
	if err != nil {
		t.Fatalf("WriteFile() error: %v", err)
	}

	loaded, err := LoadStore()
	if err != nil {
		t.Fatalf("LoadStore() error: %v", err)
	}
	if len(loaded.Credentials) != 1 {
		t.Fatalf("credential count = %d, want 1", len(loaded.Credentials))
	}

	cred := loaded.Credentials["google-antigravity"]
	if cred == nil {
		t.Fatal("google-antigravity credential missing")
	}
	if cred.AccessToken != "fresh-token" {
		t.Fatalf("AccessToken = %q, want %q", cred.AccessToken, "fresh-token")
	}
	if cred.RefreshToken != "" {
		t.Fatalf("RefreshToken = %q, want no token from stale alias", cred.RefreshToken)
	}
	if cred.Email != "" {
		t.Fatalf("Email = %q, want no metadata from stale alias", cred.Email)
	}
	if cred.ProjectID != "project-2" {
		t.Fatalf("ProjectID = %q, want %q", cred.ProjectID, "project-2")
	}
}

func TestSetCredentialReplacesLegacyAntigravityEntry(t *testing.T) {
	tmpDir := setTestAuthHome(t)

	legacyStore := map[string]any{
		"credentials": map[string]any{
			"antigravity": map[string]any{
				"access_token": "legacy-token",
				"expires_at":   time.Date(2026, 4, 16, 10, 0, 0, 0, time.UTC).Format(time.RFC3339),
				"provider":     "antigravity",
				"auth_method":  "oauth",
			},
		},
	}
	data, err := json.Marshal(legacyStore)
	if err != nil {
		t.Fatalf("json.Marshal() error: %v", err)
	}
	path := filepath.Join(tmpDir, ".picoclaw", "auth.json")
	err = os.MkdirAll(filepath.Dir(path), 0o755)
	if err != nil {
		t.Fatalf("MkdirAll() error: %v", err)
	}
	err = os.WriteFile(path, data, 0o600)
	if err != nil {
		t.Fatalf("WriteFile() error: %v", err)
	}

	refreshedExpiry := time.Date(2026, 4, 16, 12, 30, 0, 0, time.UTC)
	err = SetCredential("google-antigravity", &AuthCredential{
		AccessToken: "fresh-token",
		ExpiresAt:   refreshedExpiry,
		Provider:    "google-antigravity",
		AuthMethod:  "oauth",
	})
	if err != nil {
		t.Fatalf("SetCredential() error: %v", err)
	}

	loaded, err := LoadStore()
	if err != nil {
		t.Fatalf("LoadStore() error: %v", err)
	}
	if len(loaded.Credentials) != 1 {
		t.Fatalf("credential count = %d, want 1", len(loaded.Credentials))
	}

	cred := loaded.Credentials["google-antigravity"]
	if cred == nil {
		t.Fatal("google-antigravity credential missing")
	}
	if cred.AccessToken != "fresh-token" {
		t.Fatalf("AccessToken = %q, want %q", cred.AccessToken, "fresh-token")
	}
	if !cred.ExpiresAt.Equal(refreshedExpiry) {
		t.Fatalf("ExpiresAt = %v, want %v", cred.ExpiresAt, refreshedExpiry)
	}
}

func TestDeleteCredentialRemovesLegacyAntigravityAlias(t *testing.T) {
	tmpDir := setTestAuthHome(t)

	legacyStore := map[string]any{
		"credentials": map[string]any{
			"antigravity": map[string]any{
				"access_token": "legacy-token",
				"provider":     "antigravity",
				"auth_method":  "oauth",
			},
		},
	}
	data, err := json.Marshal(legacyStore)
	if err != nil {
		t.Fatalf("json.Marshal() error: %v", err)
	}
	path := filepath.Join(tmpDir, ".picoclaw", "auth.json")
	err = os.MkdirAll(filepath.Dir(path), 0o755)
	if err != nil {
		t.Fatalf("MkdirAll() error: %v", err)
	}
	err = os.WriteFile(path, data, 0o600)
	if err != nil {
		t.Fatalf("WriteFile() error: %v", err)
	}

	err = DeleteCredential(" google-antigravity ")
	if err != nil {
		t.Fatalf("DeleteCredential() error: %v", err)
	}

	loaded, err := LoadStore()
	if err != nil {
		t.Fatalf("LoadStore() error: %v", err)
	}
	if len(loaded.Credentials) != 0 {
		t.Fatalf("credential count = %d, want 0", len(loaded.Credentials))
	}
}

func TestSetCredentialCanonicalizesTrimmedMixedCaseProvider(t *testing.T) {
	setTestAuthHome(t)

	expiresAt := time.Date(2026, 4, 16, 13, 0, 0, 0, time.UTC)
	if err := SetCredential("  AnTiGrAvItY  ", &AuthCredential{
		AccessToken: "fresh-token",
		ExpiresAt:   expiresAt,
		Provider:    "  AnTiGrAvItY  ",
		AuthMethod:  "oauth",
	}); err != nil {
		t.Fatalf("SetCredential() error: %v", err)
	}

	loaded, err := LoadStore()
	if err != nil {
		t.Fatalf("LoadStore() error: %v", err)
	}
	if len(loaded.Credentials) != 1 {
		t.Fatalf("credential count = %d, want 1", len(loaded.Credentials))
	}

	cred := loaded.Credentials["google-antigravity"]
	if cred == nil {
		t.Fatal("google-antigravity credential missing")
	}
	if cred.Provider != "google-antigravity" {
		t.Fatalf("Provider = %q, want %q", cred.Provider, "google-antigravity")
	}
	if !cred.ExpiresAt.Equal(expiresAt) {
		t.Fatalf("ExpiresAt = %v, want %v", cred.ExpiresAt, expiresAt)
	}

	got, err := GetCredential("  GoOgLe-AnTiGrAvItY ")
	if err != nil {
		t.Fatalf("GetCredential() error: %v", err)
	}
	if got == nil {
		t.Fatal("GetCredential() returned nil")
	}
	if got.Provider != "google-antigravity" {
		t.Fatalf("GetCredential provider = %q, want %q", got.Provider, "google-antigravity")
	}
}
