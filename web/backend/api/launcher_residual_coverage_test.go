//nolint:govet // Independent launcher boundary assertions intentionally use narrow errors.
package api

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sipeed/picoclaw/pkg/database"
)

type coveragePasswordStore struct {
	initialized bool
	verified    bool
	initErr     error
	verifyErr   error
	setErr      error
}

func (store *coveragePasswordStore) IsInitialized(context.Context) (bool, error) {
	return store.initialized, store.initErr
}

func (store *coveragePasswordStore) SetPassword(context.Context, string) error { return store.setErr }

func (store *coveragePasswordStore) VerifyPassword(context.Context, string) (bool, error) {
	return store.verified, store.verifyErr
}

func TestCoverageLauncherVersionCacheAndFallbackEdges(t *testing.T) {
	setupVersionTestIsolation(t)
	cache := newSystemVersionCache()
	if value, ok := cache.get(1, true); ok || value != (systemVersionResponse{}) {
		t.Fatalf("empty version cache = %#v, %v", value, ok)
	}
	value := systemVersionResponse{Version: "v1", GoVersion: "go1"}
	cache.finishResolve(value, 7, true)
	if got, ok := cache.get(7, true); !ok || got != value {
		t.Fatalf("version cache hit = %#v, %v", got, ok)
	}
	if _, ok := cache.get(8, true); ok {
		t.Fatal("version cache survived PID change")
	}
	cache.finishResolve(value, 0, false)
	if cache.hasCurrent {
		t.Fatal("dead gateway version was cached")
	}
	leader, ok := cache.waitOrStart(nil)
	if !leader || !ok {
		t.Fatalf("version cache leader = %v, %v", leader, ok)
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if leader, ok := cache.waitOrStart(canceled); leader || ok {
		t.Fatalf("canceled cache waiter = %v, %v", leader, ok)
	}
	cache.finishResolve(value, 3, true)
	cache.resetForTest()
	cache.resetForTest()

	handler := NewHandler("")
	launcherBuildInfoForVersion = func() systemVersionResponse {
		return systemVersionResponse{Version: "fallback", GoVersion: "fallback-go"}
	}
	findPicoclawBinaryForInfo = func() string { return " " }
	if got := handler.resolveSystemVersionInfoUncached(nil); got.Version != "fallback" {
		t.Fatalf("blank binary fallback = %#v", got)
	}
	findPicoclawBinaryForInfo = func() string { return "picoclaw" }
	runPicoclawVersionOutput = func(context.Context, string) (string, error) { return "not version", nil }
	if got := handler.resolveSystemVersionInfoUncached(t.Context()); got.Version != "fallback" {
		t.Fatalf("unparseable version fallback = %#v", got)
	}
	if _, err := executePicoclawVersion(t.Context(), filepath.Join(t.TempDir(), "missing")); err == nil {
		t.Fatal("missing version binary succeeded")
	}
	for _, candidate := range []string{"", "release", "abc", "xyzxyzx"} {
		if isLikelyVersionValue(candidate) {
			t.Fatalf("unlikely version %q accepted", candidate)
		}
	}
	if !isLikelyVersionValue("dev") || !isLikelyVersionValue("v1.2.3") ||
		!isLikelyVersionValue("abcdefa") {
		t.Fatal("likely version rejected")
	}
}

func TestCoverageLauncherLogRingAndConfigFallback(t *testing.T) {
	buffer := NewLogBuffer(2)
	if lines, total, runID := buffer.LinesSince(0); lines != nil || total != 0 || runID != 0 {
		t.Fatalf("empty log = %#v, %d, %d", lines, total, runID)
	}
	buffer.Append("one")
	buffer.Append("two")
	buffer.Append("three")
	if lines, total, _ := buffer.LinesSince(0); total != 3 ||
		len(lines) != 2 || lines[0] != "two" || lines[1] != "three" {
		t.Fatalf("wrapped log = %#v, %d", lines, total)
	}
	if lines, total, _ := buffer.LinesSince(2); total != 3 || len(lines) != 1 || lines[0] != "three" {
		t.Fatalf("incremental log = %#v, %d", lines, total)
	}
	buffer.Clear()
	if buffer.RunID() != 1 {
		t.Fatalf("cleared log run ID = %d", buffer.RunID())
	}
	handler := &Handler{
		serverPort: -1, serverPublic: true, serverCIDRs: []string{"10.0.0.0/8"},
		serverAllowLocalhostBypass: true, serverTrustedProxyCIDRs: []string{"127.0.0.0/8"},
	}
	fallback := handler.launcherFallbackConfig()
	if fallback.Port <= 0 || !fallback.Public || len(fallback.AllowedCIDRs) != 1 ||
		!fallback.AllowLocalhostBypass || len(fallback.TrustedProxyCIDRs) != 1 {
		t.Fatalf("launcher fallback = %#v", fallback)
	}
}

func TestCoverageLauncherProcessOperations(t *testing.T) {
	operations := systemGatewayProcessOperations{}
	if process, err := operations.Find(os.Getpid()); err != nil || process == nil {
		t.Fatalf("find current process = %#v, %v", process, err)
	}
	if operations.Alive(nil) || operations.Alive(&exec.Cmd{}) {
		t.Fatal("empty command reported alive")
	}
	process, err := os.FindProcess(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	if !operations.Alive(&exec.Cmd{Process: process}) {
		t.Fatal("current process reported dead")
	}
	if err := operations.Signal(nil, os.Interrupt); !errors.Is(err, os.ErrInvalid) {
		t.Fatalf("nil signal = %v", err)
	}
	if err := operations.Kill(nil); !errors.Is(err, os.ErrInvalid) {
		t.Fatalf("nil kill = %v", err)
	}
	operations.Track(nil)
	operations.Forget(nil)
}

func TestCoverageLauncherOriginAndUpdateRequestEdges(t *testing.T) {
	request := httptest.NewRequest(http.MethodGet, "https://launcher.example/api", nil)
	request.Host = "launcher.example"
	for _, test := range []struct {
		name   string
		header string
		value  string
		cross  bool
	}{
		{name: "fetch cross", header: "Sec-Fetch-Site", value: "cross-site", cross: true},
		{name: "origin same", header: "Origin", value: "https://launcher.example", cross: false},
		{name: "origin foreign", header: "Origin", value: "https://foreign.example", cross: true},
		{name: "referer same", header: "Referer", value: "https://launcher.example/page", cross: false},
		{name: "invalid origin", header: "Origin", value: "bad origin", cross: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			candidate := request.Clone(request.Context())
			candidate.Header.Set(test.header, test.value)
			if got := launcherSetupCrossSite(candidate); got != test.cross {
				t.Fatalf("cross-site = %v, want %v", got, test.cross)
			}
		})
	}
	request.Header.Set("X-Forwarded-Proto", "HTTPS, http")
	if scheme := launcherRequestScheme(request); scheme != "https" {
		t.Fatalf("forwarded scheme = %q", scheme)
	}
	request.Header.Set("X-Forwarded-Proto", "invalid")
	request.TLS = nil
	request.URL.Scheme = "custom"
	if scheme := launcherRequestScheme(request); scheme != "custom" {
		t.Fatalf("URL scheme = %q", scheme)
	}

	handler := NewHandler("")
	for _, test := range []struct {
		method string
		body   string
		status int
	}{
		{method: http.MethodGet, body: `{}`, status: http.StatusMethodNotAllowed},
		{method: http.MethodPost, body: `{`, status: http.StatusBadRequest},
	} {
		recorder := httptest.NewRecorder()
		handler.handleUpdate(recorder, httptest.NewRequest(test.method, "/api/update", strings.NewReader(test.body)))
		if recorder.Code != test.status {
			t.Fatalf("update status = %d, want %d", recorder.Code, test.status)
		}
	}
}

func TestCoverageLauncherAutostartPureAndFilesystemEdges(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	if exists, err := fileExists(filepath.Join(home, "missing")); err != nil || exists {
		t.Fatalf("missing file = %v, %v", exists, err)
	}
	file := filepath.Join(home, "present")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if exists, err := fileExists(file); err != nil || !exists {
		t.Fatalf("present file = %v, %v", exists, err)
	}
	if got := xmlEscape(`&<>"'`); got != "&amp;&lt;&gt;&quot;&apos;" {
		t.Fatalf("XML escape = %q", got)
	}
	plist := buildDarwinPlist("/path with space/app", []string{"--arg", "<&"})
	if !strings.Contains(plist, "&lt;&amp;") || !strings.Contains(plist, launchAgentLabel) {
		t.Fatalf("Darwin plist = %s", plist)
	}
	for input, want := range map[string]string{
		"": "''", "plain": "plain", "has space": "'has space'", "it's": `'it'"'"'s'`,
	} {
		if got := shellQuote(input); got != want {
			t.Fatalf("shellQuote(%q) = %q, want %q", input, got, want)
		}
	}
	if line := buildLinuxExecLine(
		"/path with space/app",
		[]string{"--arg", "two words"},
	); !strings.Contains(line, "'/path with space/app'") ||
		!strings.Contains(line, "'two words'") {
		t.Fatalf("Linux exec line = %q", line)
	}
	if line := windowsCommandLine(
		`C:\Program Files\PicoClaw.exe`,
		[]string{"--arg"},
	); !strings.HasPrefix(line, `"`) || !strings.Contains(line, "Program Files") ||
		!strings.Contains(line, `PicoClaw.exe"`) {
		t.Fatalf("Windows command line = %q", line)
	}
	if err := setDarwinAutoStart(true, "/app", []string{"--arg"}); err != nil {
		t.Fatal(err)
	}
	if err := setDarwinAutoStart(false, "/app", nil); err != nil {
		t.Fatal(err)
	}
	if err := setLinuxAutoStart(true, "/app", []string{"--arg"}); err != nil {
		t.Fatal(err)
	}
	if err := setLinuxAutoStart(false, "/app", nil); err != nil {
		t.Fatal(err)
	}
	handler := NewHandler("")
	for _, test := range []struct {
		method string
		body   string
		want   int
		get    bool
	}{
		{method: http.MethodGet, want: http.StatusOK, get: true},
		{method: http.MethodPut, body: `{`, want: http.StatusBadRequest},
		{method: http.MethodPut, body: `{"enabled":true}`, want: http.StatusOK},
		{method: http.MethodPut, body: `{"enabled":false}`, want: http.StatusOK},
	} {
		recorder := httptest.NewRecorder()
		request := httptest.NewRequest(test.method, "/api/system/autostart", strings.NewReader(test.body))
		if test.get {
			handler.handleGetAutoStart(recorder, request)
		} else {
			handler.handleSetAutoStart(recorder, request)
		}
		if recorder.Code != test.want {
			t.Fatalf(
				"autostart %s status = %d, want %d: %s",
				test.body,
				recorder.Code,
				test.want,
				recorder.Body.String(),
			)
		}
	}
}

func TestCoverageLauncherAuthHandlerFailureAndSuccessMatrix(t *testing.T) {
	if initialized, err := (&launcherAuthHandlers{}).isStoreInitialized(t.Context()); err == nil || initialized {
		t.Fatalf("unconfigured auth store = %v, %v", initialized, err)
	}
	if initialized, err := (&launcherAuthHandlers{storeErr: errors.New("open")}).isStoreInitialized(
		t.Context(),
	); err == nil || initialized || !strings.Contains(err.Error(), "recover") {
		t.Fatalf("failed auth store = %v, %v", initialized, err)
	}

	login := func(store *coveragePasswordStore, body string, limiter *loginRateLimiter) int {
		handler := &launcherAuthHandlers{
			sessionCookie: "session", secureCookie: func(*http.Request) bool { return false },
			store: store, loginLimit: limiter,
		}
		recorder := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodPost, "/api/auth/login", strings.NewReader(body))
		request.RemoteAddr = "127.0.0.1:1234"
		handler.handleLogin(recorder, request)
		return recorder.Code
	}
	if status := login(&coveragePasswordStore{}, `{`, newLoginRateLimiter()); status != http.StatusBadRequest {
		t.Fatalf("malformed login status = %d", status)
	}
	if status := login(&coveragePasswordStore{initErr: errors.New("offline")},
		`{"password":"password"}`, newLoginRateLimiter()); status != http.StatusServiceUnavailable {
		t.Fatalf("offline login status = %d", status)
	}
	if status := login(&coveragePasswordStore{},
		`{"password":"password"}`, newLoginRateLimiter()); status != http.StatusConflict {
		t.Fatalf("uninitialized login status = %d", status)
	}
	if status := login(&coveragePasswordStore{initialized: true, verifyErr: errors.New("verify")},
		`{"password":"password"}`, newLoginRateLimiter()); status != http.StatusInternalServerError {
		t.Fatalf("verify-error login status = %d", status)
	}
	if status := login(&coveragePasswordStore{initialized: true},
		`{"password":"password"}`, newLoginRateLimiter()); status != http.StatusUnauthorized {
		t.Fatalf("invalid login status = %d", status)
	}
	if status := login(&coveragePasswordStore{initialized: true, verified: true},
		`{"password":"password"}`, newLoginRateLimiter()); status != http.StatusOK {
		t.Fatalf("valid login status = %d", status)
	}
	limiter := newLoginRateLimiter()
	limiter.byIP["127.0.0.1"] = make([]time.Time, loginAttemptsPerIP)
	for index := range limiter.byIP["127.0.0.1"] {
		limiter.byIP["127.0.0.1"][index] = time.Now()
	}
	if status := login(&coveragePasswordStore{initialized: true, verified: true},
		`{"password":"password"}`, limiter); status != http.StatusTooManyRequests {
		t.Fatalf("rate-limited login status = %d", status)
	}

	handler := &launcherAuthHandlers{secureCookie: func(*http.Request) bool { return false }}
	for _, test := range []struct {
		method      string
		contentType string
		body        string
		status      int
	}{
		{method: http.MethodGet, contentType: "application/json", status: http.StatusMethodNotAllowed},
		{method: http.MethodPost, body: `{}`, status: http.StatusUnsupportedMediaType},
		{method: http.MethodPost, contentType: "application/json", body: `{`, status: http.StatusBadRequest},
		{method: http.MethodPost, contentType: "application/json", body: `{} {}`, status: http.StatusBadRequest},
		{method: http.MethodPost, contentType: "application/json", body: `{}`, status: http.StatusOK},
	} {
		recorder := httptest.NewRecorder()
		request := httptest.NewRequest(test.method, "/api/auth/logout", strings.NewReader(test.body))
		request.Header.Set("Content-Type", test.contentType)
		handler.handleLogout(recorder, request)
		if recorder.Code != test.status {
			t.Fatalf("logout status = %d, want %d", recorder.Code, test.status)
		}
	}

	setup := func(store *coveragePasswordStore, body string, mutate func(*http.Request)) int {
		handler := &launcherAuthHandlers{
			sessionCookie: "session", secureCookie: func(*http.Request) bool { return false }, store: store,
		}
		recorder := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodPost, "http://launcher/api/auth/setup", strings.NewReader(body))
		request.Host = "launcher"
		if mutate != nil {
			mutate(request)
		}
		handler.handleSetup(recorder, request)
		return recorder.Code
	}
	if status := setup(&coveragePasswordStore{}, `{}`, func(request *http.Request) {
		request.Header.Set("Origin", "https://foreign")
	}); status != http.StatusForbidden {
		t.Fatalf("cross-site setup status = %d", status)
	}
	if status := setup(
		&coveragePasswordStore{initErr: errors.New("offline")},
		`{}`,
		nil,
	); status != http.StatusServiceUnavailable {
		t.Fatalf("offline setup status = %d", status)
	}
	if status := setup(&coveragePasswordStore{initialized: true}, `{}`, nil); status != http.StatusUnauthorized {
		t.Fatalf("unauthorized change status = %d", status)
	}
	for body, want := range map[string]int{
		`{`:                            http.StatusBadRequest,
		`{"password":"","confirm":""}`: http.StatusBadRequest,
		`{"password":"password","confirm":"different"}`: http.StatusBadRequest,
		`{"password":"short","confirm":"short"}`:        http.StatusBadRequest,
	} {
		if status := setup(&coveragePasswordStore{}, body, nil); status != want {
			t.Fatalf("setup body %q status = %d, want %d", body, status, want)
		}
	}
	if status := setup(&coveragePasswordStore{setErr: errors.New("save")},
		`{"password":"password","confirm":"password"}`, nil); status != http.StatusInternalServerError {
		t.Fatalf("save-error setup status = %d", status)
	}
	if status := setup(&coveragePasswordStore{},
		`{"password":"password","confirm":"password"}`, nil); status != http.StatusOK {
		t.Fatalf("valid setup status = %d", status)
	}
}

func TestCoverageLauncherModelCatalogOpenPaginationAndMigrationEdges(t *testing.T) {
	blocker := filepath.Join(t.TempDir(), "blocker")
	if err := os.WriteFile(blocker, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	handler := &ModelCatalogBrokerHandler{path: filepath.Join(blocker, "catalog.db")}
	call := func(operation string, input any) error {
		raw, err := database.MarshalCanonical(input)
		if err != nil {
			t.Fatal(err)
		}
		_, err = handler.Handle(t.Context(), database.Request{
			Domain: modelCatalogBrokerDomain, Version: modelCatalogBrokerVersion,
			Operation: operation, Payload: raw,
		})
		return err
	}
	for _, request := range []struct {
		operation string
		input     any
	}{
		{modelCatalogOperationPreflight, struct {
			StoreID database.StoreID `json:"store_id"`
		}{ModelCatalogStoreID}},
		{modelCatalogOperationLoadPage, modelCatalogPageRequest{StoreID: ModelCatalogStoreID}},
		{modelCatalogOperationSaveAll, modelCatalogSaveAllRequest{
			StoreID: ModelCatalogStoreID, Store: &CatalogStore{Entries: map[string]*CatalogEntry{}},
		}},
		{modelCatalogOperationSave, modelCatalogSaveRequest{
			StoreID: ModelCatalogStoreID, Provider: "openai", APIBase: "https://example.invalid",
		}},
		{modelCatalogOperationDelete, modelCatalogDeleteRequest{StoreID: ModelCatalogStoreID, ID: "catalog"}},
	} {
		if err := call(request.operation, request.input); err == nil {
			t.Fatalf("blocked model catalog %s succeeded", request.operation)
		}
	}
	if _, err := handler.open(t.Context()); err == nil {
		t.Fatal("blocked model catalog open succeeded")
	}

	emptyStore := &CatalogStore{Entries: map[string]*CatalogEntry{
		"empty": {ID: "empty"},
	}}
	page, err := pageModelCatalogStore(emptyStore, modelCatalogPageRequest{})
	if err != nil || !page.Done || page.NextCatalogCursor != 1 || len(page.Entries) != 1 {
		t.Fatalf("empty-model catalog page = %#v, %v", page, err)
	}
	oversizeStore := &CatalogStore{Entries: map[string]*CatalogEntry{
		"large": {ID: "large", Models: []CatalogModel{{ID: strings.Repeat("x", modelCatalogPageBytes+1)}}},
	}}
	if _, err := pageModelCatalogStore(oversizeStore, modelCatalogPageRequest{}); database.CodeOf(
		err,
	) != database.CodeIntegrity {
		t.Fatalf("oversize model catalog page = %v", err)
	}

	home := t.TempDir()
	fence, err := database.AcquireMigrationFence(home)
	if err != nil {
		t.Fatal(err)
	}
	if err := RunOfflineModelCatalogMigration(t.Context(), home); err != nil {
		_ = fence.Close()
		t.Fatal(err)
	}
	if err := fence.Close(); err != nil {
		t.Fatal(err)
	}
}
