//nolint:govet // Independent failure-boundary assertions intentionally reuse narrow error names.
package database

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestCoverageManifestValidationAndLifecycle(t *testing.T) {
	home := t.TempDir()
	stateDir, err := prepareStateDirectory(home)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ReadManifest(home); CodeOf(err) != CodeUnavailable {
		t.Fatalf("missing manifest = %v", err)
	}
	token, _ := randomHex(tokenBytes)
	epoch, _ := randomHex(epochBytes)
	valid := Manifest{
		PID: os.Getpid(), Protocol: ProtocolVersion, Token: token,
		Endpoint: endpointForStateDirectory(stateDir), Epoch: epoch,
	}
	for _, candidate := range []Manifest{
		{},
		{PID: 1, Protocol: 2, Token: token, Epoch: epoch, Endpoint: valid.Endpoint},
		{PID: 1, Protocol: 1, Token: "BAD", Epoch: epoch, Endpoint: valid.Endpoint},
		{PID: 1, Protocol: 1, Token: token, Epoch: "BAD", Endpoint: valid.Endpoint},
		{PID: 1, Protocol: 1, Token: token, Epoch: epoch, Endpoint: "foreign"},
	} {
		if err := validateManifest(candidate, stateDir); err == nil {
			t.Fatalf("invalid manifest accepted: %#v", candidate)
		}
	}
	if err := writeManifest(stateDir, valid); err != nil {
		t.Fatal(err)
	}
	read, err := ReadManifest(home)
	if err != nil || read != valid {
		t.Fatalf("read manifest = %#v, %v", read, err)
	}
	if err := removeManifestForEpoch(home, "wrong"); CodeOf(err) != CodeConflict {
		t.Fatalf("wrong epoch removal = %v", err)
	}
	if err := removeManifestForEpoch(home, epoch); err != nil {
		t.Fatal(err)
	}
	if err := removeManifestForEpoch(home, epoch); err != nil {
		t.Fatalf("repeat manifest removal = %v", err)
	}
	if _, err := randomHex(0); CodeOf(err) != CodeInvalid {
		t.Fatalf("invalid random size = %v", err)
	}
	if validLowerHex("ABCDEF", 6) || validLowerHex("abcd", 6) || !validLowerHex("abcdef", 6) {
		t.Fatal("lower hex validation mismatch")
	}
}

func TestCoverageManifestRejectsUnsafeFiles(t *testing.T) {
	for _, test := range []struct {
		name    string
		payload []byte
		mode    os.FileMode
	}{
		{name: "empty", payload: nil, mode: 0o600},
		{name: "noncanonical", payload: []byte(" {}"), mode: 0o600},
		{name: "oversize", payload: bytes.Repeat([]byte("x"), int(manifestMaxBytes)+1), mode: 0o600},
		{name: "public mode", payload: []byte(`{}`), mode: 0o644},
	} {
		t.Run(test.name, func(t *testing.T) {
			home := t.TempDir()
			stateDir, err := prepareStateDirectory(home)
			if err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(stateDir, manifestFileName)
			if err := os.WriteFile(path, test.payload, test.mode); err != nil {
				t.Fatal(err)
			}
			if _, err := ReadManifest(home); err == nil {
				t.Fatal("unsafe manifest accepted")
			}
		})
	}
	t.Run("symlink", func(t *testing.T) {
		home := t.TempDir()
		stateDir, err := prepareStateDirectory(home)
		if err != nil {
			t.Fatal(err)
		}
		target := filepath.Join(stateDir, "target")
		if err := os.WriteFile(target, []byte(`{}`), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(target, filepath.Join(stateDir, manifestFileName)); err != nil {
			t.Skipf("symlinks unavailable: %v", err)
		}
		if _, err := ReadManifest(home); CodeOf(err) != CodeIntegrity {
			t.Fatalf("symlink manifest = %v", err)
		}
	})
}

func TestCoverageInheritedAuthorityLifecycle(t *testing.T) {
	original := RuntimeClient()
	t.Cleanup(func() { InstallProcessClient(original) })
	InstallProcessClient(nil)
	if RuntimeClient() != nil {
		t.Fatal("nil process client was not installed")
	}
	if _, err := InheritedAuthorityEnvironment("bad\x00home"); err == nil {
		t.Fatal("invalid inherited authority home accepted")
	}
	if _, _, err := ConnectInherited(t.Context()); CodeOf(err) != CodeUnauthorized {
		t.Fatalf("missing inherited authority = %v", err)
	}
	for _, encoded := range []string{
		"%%%", base64.RawURLEncoding.EncodeToString([]byte("not json")),
		base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte("x"), (64<<10)+1)),
	} {
		t.Setenv(inheritedAuthorityEnvironment, encoded)
		if _, _, err := ConnectInherited(t.Context()); CodeOf(err) != CodeUnauthorized {
			t.Fatalf("invalid inherited authority = %v", err)
		}
	}

	home := t.TempDir()
	server, err := StartServer(context.Background(), ServerOptions{Home: home})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { closeServer(t, server) })
	environment, err := InheritedAuthorityEnvironment(home)
	if err != nil {
		t.Fatal(err)
	}
	_, encoded, found := strings.Cut(environment, "=")
	if !found {
		t.Fatalf("authority environment = %q", environment)
	}
	t.Setenv(inheritedAuthorityEnvironment, encoded)
	client, canonicalHome, err := ConnectInherited(nil)
	if err != nil || client == nil || canonicalHome == "" || RuntimeClient() != client {
		t.Fatalf("inherited connection = %p, %q, %v", client, canonicalHome, err)
	}
	if os.Getenv(inheritedAuthorityEnvironment) != "" {
		t.Fatal("inherited authority was not consumed")
	}
}

func TestCoverageServerDirectControlAndDomainBoundaries(t *testing.T) {
	if (*Server)(nil).Manifest() != (Manifest{}) {
		t.Fatal("nil server returned a manifest")
	}
	select {
	case <-(*Server)(nil).Done():
	default:
		t.Fatal("nil server Done channel remained open")
	}
	if err := (*Server)(nil).Close(nil); err != nil {
		t.Fatal(err)
	}
	(*Server)(nil).initiateShutdown()

	if _, err := StartServer(nil, ServerOptions{Home: "bad\x00home"}); err == nil {
		t.Fatal("server accepted invalid home")
	}
	if _, err := StartServer(
		nil,
		ServerOptions{Home: t.TempDir(), CatalogFingerprint: "bad"},
	); CodeOf(err) != CodeInvalid {
		t.Fatalf("invalid catalog fingerprint = %v", err)
	}
	if _, err := StartServer(nil, ServerOptions{
		Home: t.TempDir(), RequiredStores: []StoreID{"bad id"},
	}); CodeOf(err) != CodeInvalid {
		t.Fatalf("invalid required store = %v", err)
	}
	if _, err := StartServer(nil, ServerOptions{
		Home: t.TempDir(), RequiredStores: []StoreID{"global/auth", "global/auth"},
	}); CodeOf(err) != CodeIntegrity {
		t.Fatalf("duplicate required store = %v", err)
	}

	server := &Server{
		manifest: Manifest{PID: 42, Epoch: "epoch"}, startedAt: time.Now(),
		requiredStores: []StoreID{"global/auth"}, now: time.Now,
	}
	for _, request := range []Request{
		{Domain: ControlDomain, Version: 2, Operation: ControlOperationPing, Payload: []byte(`{}`)},
		{Domain: ControlDomain, Version: 1, Operation: ControlOperationPing, Payload: []byte(` {}`)},
		{Domain: ControlDomain, Version: 1, Operation: "unknown", Payload: []byte(`{}`)},
		{Domain: "unknown", Version: 1, Operation: "read", Payload: []byte(`{}`)},
	} {
		if _, _, err := server.handle(t.Context(), request); err == nil {
			t.Fatalf("invalid direct request accepted: %#v", request)
		}
	}
	result, shutdown, err := server.handle(t.Context(), Request{
		Domain: ControlDomain, Version: 1, Operation: ControlOperationPing, Payload: []byte(`{}`),
	})
	if err != nil || shutdown || result.(PingResponse).PID != 42 {
		t.Fatalf("direct ping = %#v, %v, %v", result, shutdown, err)
	}
	result, shutdown, err = server.handle(t.Context(), Request{
		Domain: ControlDomain, Version: 1, Operation: ControlOperationShutdown, Payload: []byte(`{}`),
	})
	if err != nil || !shutdown || !result.(ShutdownResponse).Accepted {
		t.Fatalf("direct shutdown = %#v, %v, %v", result, shutdown, err)
	}

	server.statusProvider = func(context.Context) ([]StoreStatus, error) {
		return nil, errors.New("status canary")
	}
	if _, _, err := server.handle(t.Context(), Request{
		Domain: ControlDomain, Version: 1, Operation: ControlOperationStatus, Payload: []byte(`{}`),
	}); err == nil {
		t.Fatal("status provider error ignored")
	}
	server.statusProvider = func(context.Context) ([]StoreStatus, error) {
		return []StoreStatus{{ID: "bad id", Readiness: StoreReady}}, nil
	}
	if _, _, err := server.handle(t.Context(), Request{
		Domain: ControlDomain, Version: 1, Operation: ControlOperationStatus, Payload: []byte(`{}`),
	}); err == nil {
		t.Fatal("invalid status provider result accepted")
	}
	server.statusProvider = func(context.Context) ([]StoreStatus, error) { return nil, nil }
	if _, _, err := server.handle(t.Context(), Request{
		Domain: ControlDomain, Version: 1, Operation: ControlOperationStatus, Payload: []byte(`{}`),
	}); err == nil {
		t.Fatal("missing required status accepted")
	}
	server.statusProvider = func(context.Context) ([]StoreStatus, error) {
		return []StoreStatus{{ID: "global/auth", Readiness: StoreReady}}, nil
	}
	if _, _, err := server.handle(t.Context(), Request{
		Domain: ControlDomain, Version: 1, Operation: ControlOperationStatus, Payload: []byte(`{}`),
	}); err != nil {
		t.Fatal(err)
	}

	server.handler = HandlerFunc(func(context.Context, Request) (any, error) { panic("handler panic") })
	if _, _, err := server.handle(t.Context(), Request{Domain: "domain"}); CodeOf(err) != CodeInternal {
		t.Fatalf("handler panic = %v", err)
	}
	server.handler = HandlerFunc(func(context.Context, Request) (any, error) {
		return EmptyPayload{}, errors.New("domain canary")
	})
	if _, _, err := server.handle(t.Context(), Request{Domain: "domain"}); err == nil {
		t.Fatal("handler error ignored")
	}

	if validated, err := validateRequiredStores(
		[]StoreID{"workspace/x", "global/auth"},
	); err != nil ||
		validated[0] != "global/auth" {
		t.Fatalf("required store sorting = %#v, %v", validated, err)
	}
	if err := requiredStoresHaveStatuses(nil, nil); err != nil {
		t.Fatal(err)
	}
	if err := requiredStoresHaveStatuses([]StoreID{"global/auth"}, nil); CodeOf(err) != CodeIntegrity {
		t.Fatalf("missing required status = %v", err)
	}
	if safeRequestID("bad id") != "invalid" || safeRequestID("good_id") != "good_id" {
		t.Fatal("safe request ID mismatch")
	}
	if payload, err := marshalPayload(nil); err != nil || string(payload) != `{}` {
		t.Fatalf("nil payload = %s, %v", payload, err)
	}
	if _, err := marshalPayload([]string{"not", "object"}); CodeOf(err) != CodeInvalid {
		t.Fatalf("array payload = %v", err)
	}
	if _, err := marshalPayload(func() {}); err == nil {
		t.Fatal("unsupported payload marshaled")
	}
	if err := callCloseHandler(func() error { panic("close panic") }); CodeOf(err) != CodeInternal {
		t.Fatalf("close handler panic = %v", err)
	}
	if canary := errors.New("close canary"); !errors.Is(callCloseHandler(func() error { return canary }), canary) {
		t.Fatal("close handler error changed")
	}
}

func TestCoverageServerDispatchAndStartupFailureBoundaries(t *testing.T) {
	server := newIdempotencyTestServer(HandlerFunc(func(context.Context, Request) (any, error) {
		return func() {}, nil
	}))
	envelope := idempotencyTestEnvelope(t, "stable", "request", "value")
	response, _ := server.dispatchContext(nil, envelope)
	if CodeOf(response.Error) != CodeInternal {
		t.Fatalf("invalid domain result = %#v", response)
	}
	envelope.IdempotencyKey = ""
	envelope.Payload = []byte(`[]`)
	response, _ = server.dispatch(envelope)
	if CodeOf(response.Error) != CodeInvalid {
		t.Fatalf("invalid request payload = %#v", response)
	}

	home := t.TempDir()
	first, err := StartServer(context.Background(), ServerOptions{Home: home})
	if err != nil {
		t.Fatal(err)
	}
	if second, err := StartServer(context.Background(), ServerOptions{Home: home}); second != nil ||
		CodeOf(err) != CodeAlreadyExists {
		t.Fatalf("duplicate server = %#v, %v", second, err)
	}
	closeServer(t, first)

	fencedHome := t.TempDir()
	fence, err := AcquireMigrationFence(fencedHome)
	if err != nil {
		t.Fatal(err)
	}
	if started, err := StartServer(context.Background(), ServerOptions{Home: fencedHome}); started != nil ||
		CodeOf(err) != CodeConflict {
		t.Fatalf("server through migration fence = %#v, %v", started, err)
	}
	_ = fence.Close()

	manifestHome := t.TempDir()
	stateDir, err := prepareStateDirectory(manifestHome)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(stateDir, manifestFileName), 0o700); err != nil {
		t.Fatal(err)
	}
	if started, err := StartServer(context.Background(), ServerOptions{Home: manifestHome}); started != nil ||
		CodeOf(err) != CodeIntegrity {
		t.Fatalf("server with unsafe manifest = %#v, %v", started, err)
	}
}

func TestCoverageServerCloseDeadlineAndHandlerErrors(t *testing.T) {
	home := t.TempDir()
	release := make(chan struct{})
	server, err := StartServer(context.Background(), ServerOptions{
		Home: home,
		CloseHandler: func() error {
			<-release
			return errors.New("close handler canary")
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := server.Close(ctx); CodeOf(err) != CodeDeadline {
		t.Fatalf("canceled server close = %v", err)
	}
	close(release)
	select {
	case <-server.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("server close handler did not finish")
	}
	if err := server.Close(nil); err == nil || !strings.Contains(err.Error(), "close handler canary") {
		t.Fatalf("server close error = %v", err)
	}
}

func TestCoverageServerShutdownCallbackPanicIsContained(t *testing.T) {
	server, err := StartServer(context.Background(), ServerOptions{
		Home: t.TempDir(),
		OnShutdownRequested: func() {
			panic("shutdown callback")
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	client, err := ConnectWithManifest(server.home, server.Manifest())
	if err != nil {
		t.Fatal(err)
	}
	if err := client.Shutdown(t.Context()); err != nil {
		t.Fatal(err)
	}
	select {
	case <-server.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("panic shutdown callback did not finish")
	}
	if err := server.Close(nil); CodeOf(err) != CodeInternal {
		t.Fatalf("shutdown callback close error = %v", err)
	}
}

func TestCoverageClientLocalValidation(t *testing.T) {
	var client *Client
	if client.Epoch() != "" || client.Refresh() == nil || client.Call(nil, "d", 1, "o", nil, nil) == nil ||
		client.CallWithOptions(nil, "d", 1, "o", nil, nil, CallOptions{}) == nil {
		t.Fatal("nil client validation mismatch")
	}
	if _, err := Connect("bad\x00home"); CodeOf(err) == "" {
		t.Fatal("invalid client home accepted")
	}
	if _, err := Connect(t.TempDir()); CodeOf(err) != CodeUnavailable {
		t.Fatalf("missing broker connect = %v", err)
	}
	if _, err := ConnectWithManifest("bad\x00home", Manifest{}); err == nil {
		t.Fatal("inherited client accepted invalid home")
	}
	if err := safeDiscoveryError(nil); err != nil {
		t.Fatal(err)
	}
	if CodeOf(safeDiscoveryError(os.ErrPermission)) != CodeUnauthorized ||
		CodeOf(safeDiscoveryError(NewError(CodeIntegrity, "bad"))) != CodeIntegrity ||
		CodeOf(safeDiscoveryError(errors.New("plain"))) != CodeUnavailable {
		t.Fatal("safe discovery error mapping mismatch")
	}
	if CodeOf(dispatchedCallError(true, nil)) != CodeOutcomeUnknown ||
		CodeOf(dispatchedCallError(false, context.Canceled)) != CodeDeadline ||
		CodeOf(dispatchedCallError(false, nil)) != CodeUnavailable {
		t.Fatal("dispatched call error mapping mismatch")
	}

	home := t.TempDir()
	server, err := StartServer(context.Background(), ServerOptions{Home: home})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { closeServer(t, server) })
	client, err = ConnectWithManifest(home, server.Manifest())
	if err != nil {
		t.Fatal(err)
	}
	if err := client.CallWithOptions(
		t.Context(),
		"Bad",
		1,
		"read",
		EmptyPayload{},
		nil,
		CallOptions{},
	); CodeOf(
		err,
	) != CodeInvalid {
		t.Fatalf("invalid domain call = %v", err)
	}
	if err := client.CallWithOptions(
		t.Context(),
		"domain",
		1,
		"read",
		func() {},
		nil,
		CallOptions{},
	); CodeOf(
		err,
	) != CodeInvalid {
		t.Fatalf("invalid payload call = %v", err)
	}
	expired, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()
	if err := client.CallWithOptions(
		expired,
		"domain",
		1,
		"read",
		EmptyPayload{},
		nil,
		CallOptions{},
	); CodeOf(
		err,
	) != CodeDeadline {
		t.Fatalf("expired call = %v", err)
	}
}

func TestCoverageHomeAndStateValidation(t *testing.T) {
	for _, invalid := range []string{"", " padded ", "bad\x00home"} {
		if _, err := CanonicalHome(invalid); CodeOf(err) != CodeInvalid {
			t.Fatalf("invalid canonical home %q = %v", invalid, err)
		}
	}
	missing := filepath.Join(t.TempDir(), "missing")
	if _, err := CanonicalHome(missing); CodeOf(err) != CodeUnavailable {
		t.Fatalf("missing canonical home = %v", err)
	}
	prepared, err := PrepareHome(missing)
	if err != nil || prepared != missing {
		t.Fatalf("prepared home = %q, %v", prepared, err)
	}
	if canonical, err := CanonicalHome(missing); err != nil || canonical != prepared {
		t.Fatalf("canonical home = %q, %v", canonical, err)
	}
	fileHome := filepath.Join(t.TempDir(), "home-file")
	if err := os.WriteFile(fileHome, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := PrepareHome(fileHome); CodeOf(err) != CodeInvalid {
		t.Fatalf("file home = %v", err)
	}
	alias := filepath.Join(t.TempDir(), "alias")
	if err := os.Symlink(missing, alias); err == nil {
		if _, err := CanonicalHome(alias); CodeOf(err) != CodeInvalid {
			t.Fatalf("symlink home = %v", err)
		}
		if err := rejectExistingAncestorAlias(filepath.Join(alias, "child")); CodeOf(err) != CodeInvalid {
			t.Fatalf("symlink ancestor = %v", err)
		}
	}
	if _, err := StateDirectory(missing); CodeOf(err) != CodeUnavailable {
		t.Fatalf("missing state directory = %v", err)
	}
	stateDir, err := prepareStateDirectory(missing)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := StateDirectory(missing); err != nil || got != stateDir {
		t.Fatalf("state directory = %q, %v", got, err)
	}
	if err := os.Chmod(stateDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := StateDirectory(missing); CodeOf(err) != CodeIntegrity {
		t.Fatalf("public state directory = %v", err)
	}
	if err := os.Chmod(stateDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(stateDir); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(stateDir, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := prepareStateDirectory(missing); CodeOf(err) != CodeIntegrity {
		t.Fatalf("file state boundary = %v", err)
	}
}

func TestCoverageManifestExistingBoundaryAndInvalidContent(t *testing.T) {
	home := t.TempDir()
	stateDir, err := prepareStateDirectory(home)
	if err != nil {
		t.Fatal(err)
	}
	token, _ := randomHex(tokenBytes)
	epoch, _ := randomHex(epochBytes)
	valid := Manifest{
		PID: os.Getpid(), Protocol: ProtocolVersion, Token: token, Epoch: epoch,
		Endpoint: endpointForStateDirectory(stateDir),
	}
	if err := writeManifest(stateDir, Manifest{}); err == nil {
		t.Fatal("invalid manifest published")
	}
	path := filepath.Join(stateDir, manifestFileName)
	if err := os.Mkdir(path, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := writeManifest(stateDir, valid); CodeOf(err) != CodeIntegrity {
		t.Fatalf("directory manifest boundary = %v", err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	invalidRaw, err := MarshalCanonical(Manifest{
		PID: 1, Protocol: ProtocolVersion, Token: "bad", Epoch: epoch, Endpoint: valid.Endpoint,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, invalidRaw, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadManifest(home); CodeOf(err) != CodeIntegrity {
		t.Fatalf("canonical invalid manifest = %v", err)
	}
	if err := removeManifestForEpoch(home, epoch); CodeOf(err) != CodeIntegrity {
		t.Fatalf("invalid manifest removal = %v", err)
	}
	if validLowerHex("gg", 2) {
		t.Fatal("non-hex lowercase identity accepted")
	}
}

func closeServer(t *testing.T, server *Server) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := server.Close(ctx); err != nil {
		t.Fatalf("close server: %v", err)
	}
}
