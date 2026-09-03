//go:build unix

//nolint:govet // Independent filesystem boundary assertions intentionally use narrow errors.
package database

import (
	"context"
	"encoding/base64"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

type realTemporaryAcceptError struct{}

func (realTemporaryAcceptError) Error() string   { return "temporary accept failure" }
func (realTemporaryAcceptError) Timeout() bool   { return false }
func (realTemporaryAcceptError) Temporary() bool { return true }

type realFailingListener struct {
	errors []error
}

func (listener *realFailingListener) Accept() (net.Conn, error) {
	err := listener.errors[0]
	listener.errors = listener.errors[1:]
	return nil, err
}

func (*realFailingListener) Close() error   { return nil }
func (*realFailingListener) Addr() net.Addr { return &net.UnixAddr{Name: "test", Net: "unix"} }

func TestRealFilesystemFailureBoundaries(t *testing.T) {
	root := t.TempDir()
	parentFile := filepath.Join(root, "parent-file")
	if err := os.WriteFile(parentFile, []byte("not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}
	child := filepath.Join(parentFile, "child")
	if err := createOwnerOnlyDirectory(child); err == nil {
		t.Fatal("owner-only directory beneath a file succeeded")
	}
	if err := prepareOwnerOnlyLeafDirectory(child); err == nil {
		t.Fatal("owner-only leaf beneath a file succeeded")
	}
	if file, err := createOwnerOnlyTempFile(child, "temporary-", 0o600); err == nil || file != nil {
		t.Fatalf("temporary file beneath a file = %#v, %v", file, err)
	}

	existing := filepath.Join(root, "existing")
	if err := os.WriteFile(existing, []byte("existing"), 0o600); err != nil {
		t.Fatal(err)
	}
	if file, err := openOwnerOnlyExistingFile(filepath.Join(root, "missing"), 0o600); err == nil || file != nil {
		t.Fatalf("missing owner-only file = %#v, %v", file, err)
	}
	if err := os.Chmod(existing, 0o644); err != nil {
		t.Fatal(err)
	}
	if file, err := openOwnerOnlyExistingFile(existing, 0o600); err == nil || file != nil {
		t.Fatalf("public owner-only file = %#v, %v", file, err)
	}

	overlong := filepath.Join(root, strings.Repeat("x", 5000))
	if _, err := CanonicalHome(overlong); err == nil {
		t.Fatal("overlong home was accepted")
	}
	if err := validateStateDirectory(overlong); err == nil {
		t.Fatal("overlong state directory was accepted")
	}
	blockedAncestor := filepath.Join(root, "blocked-ancestor")
	if err := os.Mkdir(blockedAncestor, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(blockedAncestor, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(blockedAncestor, 0o700) })
	if err := rejectExistingAncestorAlias(filepath.Join(blockedAncestor, "child")); err == nil {
		t.Fatal("unsearchable home ancestor was accepted")
	}
	blockedHome := t.TempDir()
	if err := os.Chmod(blockedHome, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(blockedHome, 0o700) })
	if _, err := prepareStateDirectory(blockedHome); err == nil {
		t.Fatal("state directory below an unsearchable home was prepared")
	}
	if _, err := PrepareHome(existing); err == nil {
		t.Fatal("regular file was accepted as a home")
	}

	realAncestor := filepath.Join(root, "real-ancestor")
	if err := os.Mkdir(realAncestor, 0o700); err != nil {
		t.Fatal(err)
	}
	aliasAncestor := filepath.Join(root, "alias-ancestor")
	if err := os.Symlink(realAncestor, aliasAncestor); err != nil {
		t.Fatal(err)
	}
	if _, err := PrepareHome(filepath.Join(aliasAncestor, "new-home")); err == nil {
		t.Fatal("home below a symlinked ancestor was accepted")
	}

	home := t.TempDir()
	stateAlias := filepath.Join(home, StateDirectoryName)
	if err := os.Symlink(realAncestor, stateAlias); err != nil {
		t.Fatal(err)
	}
	if _, err := prepareStateDirectory(home); CodeOf(err) != CodeIntegrity {
		t.Fatalf("symlinked state directory = %v", err)
	}
	if err := validateStateDirectory(stateAlias); CodeOf(err) != CodeIntegrity {
		t.Fatalf("symlinked state validation = %v", err)
	}
}

func TestRealManifestPublicationFailureBoundaries(t *testing.T) {
	root := t.TempDir()
	validFor := func(stateDir string) Manifest {
		token, err := randomHex(tokenBytes)
		if err != nil {
			t.Fatal(err)
		}
		epoch, err := randomHex(epochBytes)
		if err != nil {
			t.Fatal(err)
		}
		return Manifest{
			PID: os.Getpid(), Protocol: ProtocolVersion, Token: token,
			Endpoint: endpointForStateDirectory(stateDir), Epoch: epoch,
		}
	}

	missingState := filepath.Join(root, "missing-state")
	if err := writeManifest(missingState, validFor(missingState)); err == nil {
		t.Fatal("manifest publication in a missing directory succeeded")
	}

	directoryBoundary := filepath.Join(root, "directory-state")
	if err := os.Mkdir(directoryBoundary, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(directoryBoundary, manifestFileName), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := writeManifest(directoryBoundary, validFor(directoryBoundary)); CodeOf(err) != CodeIntegrity {
		t.Fatalf("directory manifest boundary = %v", err)
	}

	publicBoundary := filepath.Join(root, "public-state")
	if err := os.Mkdir(publicBoundary, 0o700); err != nil {
		t.Fatal(err)
	}
	manifestPath := filepath.Join(publicBoundary, manifestFileName)
	if err := os.WriteFile(manifestPath, []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(manifestPath, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := writeManifest(publicBoundary, validFor(publicBoundary)); err == nil {
		t.Fatal("public manifest boundary was overwritten")
	}

	overlongState := filepath.Join(root, strings.Repeat("s", 5000))
	if err := writeManifest(overlongState, validFor(overlongState)); err == nil {
		t.Fatal("manifest publication through an overlong boundary succeeded")
	}
	if err := removeManifestForEpoch(
		filepath.Join(root, "missing-home"),
		strings.Repeat("0", epochBytes*2),
	); err != nil {
		t.Fatalf("missing manifest removal = %v", err)
	}
}

func TestRealUnixEndpointFailureBoundaries(t *testing.T) {
	root := shortCoverageUnixTempDir(t)
	if listener, err := listenLocal(filepath.Join(root, "missing", "broker.sock")); err == nil || listener != nil {
		t.Fatalf("listener with missing parent = %#v, %v", listener, err)
	}

	parentFile := filepath.Join(root, "parent-file")
	if err := os.WriteFile(parentFile, []byte("file"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := prepareEndpoint(filepath.Join(parentFile, "broker.sock")); err == nil {
		t.Fatal("endpoint beneath a file was prepared")
	}
	if err := prepareEndpoint(filepath.Join(parentFile, "child", "broker.sock")); err == nil {
		t.Fatal("endpoint beneath an invalid parent path was prepared")
	}
	if err := prepareEndpoint("/proc/picoclaw-database-uncreatable/broker.sock"); err == nil {
		t.Fatal("endpoint in a read-only system boundary was prepared")
	}

	realParent := filepath.Join(root, "real-parent")
	if err := os.Mkdir(realParent, 0o700); err != nil {
		t.Fatal(err)
	}
	aliasParent := filepath.Join(root, "alias-parent")
	if err := os.Symlink(realParent, aliasParent); err != nil {
		t.Fatal(err)
	}
	if err := prepareEndpoint(filepath.Join(aliasParent, "broker.sock")); CodeOf(err) != CodeIntegrity {
		t.Fatalf("symlinked endpoint parent = %v", err)
	}

	publicParent := filepath.Join(root, "public-parent")
	if err := os.Mkdir(publicParent, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := prepareEndpoint(filepath.Join(publicParent, "broker.sock")); err == nil {
		t.Fatal("public endpoint parent was accepted")
	}

	regularEndpoint := filepath.Join(realParent, "regular")
	if err := os.WriteFile(regularEndpoint, []byte("file"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := prepareEndpoint(regularEndpoint); CodeOf(err) != CodeIntegrity {
		t.Fatalf("regular endpoint = %v", err)
	}
	if err := cleanupEndpoint(regularEndpoint); CodeOf(err) != CodeIntegrity {
		t.Fatalf("regular endpoint cleanup = %v", err)
	}

	aliasEndpoint := filepath.Join(realParent, "alias.sock")
	if err := os.Symlink(regularEndpoint, aliasEndpoint); err != nil {
		t.Fatal(err)
	}
	if err := prepareEndpoint(aliasEndpoint); CodeOf(err) != CodeIntegrity {
		t.Fatalf("symlink endpoint = %v", err)
	}
	if err := cleanupEndpoint(aliasEndpoint); CodeOf(err) != CodeIntegrity {
		t.Fatalf("symlink endpoint cleanup = %v", err)
	}
	overlongEndpoint := filepath.Join(realParent, strings.Repeat("e", 5000))
	if err := prepareEndpoint(overlongEndpoint); err == nil {
		t.Fatal("overlong endpoint was prepared")
	}
	if err := cleanupEndpoint(overlongEndpoint); err == nil {
		t.Fatal("overlong endpoint was cleaned")
	}

	socketEndpoint := filepath.Join(realParent, "public.sock")
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: socketEndpoint, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	listener.SetUnlinkOnClose(false)
	if err := os.Chmod(socketEndpoint, 0o666); err != nil {
		t.Fatal(err)
	}
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	if err := prepareEndpoint(socketEndpoint); err == nil {
		t.Fatal("public stale socket was accepted")
	}
}

func TestRealUnixFileLockFailureBoundaries(t *testing.T) {
	root := shortCoverageUnixTempDir(t)
	homeFile := filepath.Join(root, "home-file")
	if err := os.WriteFile(homeFile, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := AcquireOnlineFence(homeFile); err == nil {
		t.Fatal("online fence for a file home succeeded")
	}
	migrationFence, err := AcquireMigrationFence(filepath.Join(root, "missing-home"))
	if err != nil {
		t.Fatalf("migration fence should securely create its home: %v", err)
	}
	if err := migrationFence.Close(); err != nil {
		t.Fatal(err)
	}
	for _, acquire := range []struct {
		name string
		call func(string) error
	}{
		{name: "online", call: func(home string) error {
			_, err := AcquireOnlineFence(home)
			return err
		}},
		{name: "migration", call: func(home string) error {
			_, err := AcquireMigrationFence(home)
			return err
		}},
	} {
		t.Run(acquire.name+" invalid lock boundary", func(t *testing.T) {
			home := t.TempDir()
			stateDir, err := prepareStateDirectory(home)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.Mkdir(filepath.Join(stateDir, storageLockFileName), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := acquire.call(home); err == nil {
				t.Fatal("directory storage lock was accepted")
			}
		})
	}
	singletonHome := t.TempDir()
	singletonState, err := prepareStateDirectory(singletonHome)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(singletonState, brokerLockFileName), 0o700); err != nil {
		t.Fatal(err)
	}
	if lock, err := acquireBrokerSingleton(singletonState); err == nil || lock != nil {
		t.Fatalf("directory singleton lock = %#v, %v", lock, err)
	}

	if file, err := acquirePlatformFileLock(filepath.Join(root, "missing", "lock"), false); err == nil || file != nil {
		t.Fatalf("lock beneath a missing parent = %#v, %v", file, err)
	}
	lockTarget := filepath.Join(root, "lock-target")
	if err := os.WriteFile(lockTarget, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	lockAlias := filepath.Join(root, "lock-alias")
	if err := os.Symlink(lockTarget, lockAlias); err != nil {
		t.Fatal(err)
	}
	if file, err := acquirePlatformFileLock(lockAlias, false); CodeOf(err) != CodeIntegrity || file != nil {
		t.Fatalf("symlinked lock = %#v, %v", file, err)
	}
	fifoLock := filepath.Join(root, "fifo-lock")
	if err := unix.Mkfifo(fifoLock, 0o600); err != nil {
		t.Fatal(err)
	}
	if file, err := acquirePlatformFileLock(fifoLock, false); CodeOf(err) != CodeIntegrity || file != nil {
		t.Fatalf("FIFO lock = %#v, %v", file, err)
	}
	publicLock := filepath.Join(root, "public-lock")
	if err := os.WriteFile(publicLock, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if file, err := acquirePlatformFileLock(publicLock, false); CodeOf(err) != CodeIntegrity || file != nil {
		t.Fatalf("public lock = %#v, %v", file, err)
	}
	if info, err := os.Lstat(publicLock); err != nil || info.Mode().Perm() != 0o644 {
		t.Fatalf("public lock permissions changed = %#v, %v", info, err)
	}
	if err := releasePlatformFileLock(nil); err != nil {
		t.Fatal(err)
	}

	closed, err := os.Open(lockTarget)
	if err != nil {
		t.Fatal(err)
	}
	if err := closed.Close(); err != nil {
		t.Fatal(err)
	}
	if err := (&Fence{file: closed}).Close(); err == nil {
		t.Fatal("closing a fence around a closed descriptor succeeded")
	}
	if err := (&singletonLock{file: closed}).close(); err == nil {
		t.Fatal("closing a singleton around a closed descriptor succeeded")
	}
	if os.Geteuid() == 0 {
		foreign := filepath.Join(root, "foreign-lock")
		if err := os.WriteFile(foreign, nil, 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chown(foreign, 65534, -1); err != nil {
			t.Fatal(err)
		}
		if file, err := acquirePlatformFileLock(foreign, false); CodeOf(err) != CodeIntegrity || file != nil {
			t.Fatalf("foreign lock = %#v, %v", file, err)
		}

		foreignDirectory := filepath.Join(root, "foreign-directory")
		if err := os.Mkdir(foreignDirectory, 0o700); err != nil {
			t.Fatal(err)
		}
		foreignFile := filepath.Join(root, "foreign-file")
		if err := os.WriteFile(foreignFile, nil, 0o600); err != nil {
			t.Fatal(err)
		}
		foreignSocket := filepath.Join(root, "foreign.sock")
		foreignListener, err := net.ListenUnix("unix", &net.UnixAddr{Name: foreignSocket, Net: "unix"})
		if err != nil {
			t.Fatal(err)
		}
		defer foreignListener.Close()
		for _, path := range []string{foreignDirectory, foreignFile, foreignSocket} {
			if err := os.Chown(path, 65534, -1); err != nil {
				t.Fatal(err)
			}
		}
		directoryInfo, _ := os.Lstat(foreignDirectory)
		fileInfo, _ := os.Lstat(foreignFile)
		socketInfo, _ := os.Lstat(foreignSocket)
		if err := validateOwnerOnlyDirectory(foreignDirectory, directoryInfo); err == nil {
			t.Fatal("foreign directory ownership was accepted")
		}
		if err := validateOwnerOnlyFile(foreignFile, fileInfo, 0o600); err == nil {
			t.Fatal("foreign file ownership was accepted")
		}
		if err := validateOwnerOnlySocket(foreignSocket, socketInfo); err == nil {
			t.Fatal("foreign socket ownership was accepted")
		}
		for _, path := range []string{foreignDirectory, foreignFile, foreignSocket} {
			if err := os.Chown(path, os.Geteuid(), -1); err != nil {
				t.Fatal(err)
			}
		}
	}
	if os.Geteuid() != 0 {
		if info, err := os.Lstat("/root"); err == nil && info.Mode().Perm() == 0o700 {
			if err := validateOwnerOnlyDirectory("/root", info); CodeOf(err) != CodeUnauthorized {
				t.Fatalf("foreign owner-only directory = %v", err)
			}
		}
		if info, err := os.Lstat("/etc/passwd"); err == nil {
			if err := validateOwnerOnlyFile("/etc/passwd", info, info.Mode().Perm()); CodeOf(err) != CodeUnauthorized {
				t.Fatalf("foreign owner-only file = %v", err)
			}
		}
	}
}

func TestRealAuthorityFailureBoundaries(t *testing.T) {
	if _, err := InheritedAuthorityEnvironment("bad\x00home"); err == nil {
		t.Fatal("invalid inherited-authority home was accepted")
	}
	home := t.TempDir()
	if _, err := InheritedAuthorityEnvironment(home); err == nil {
		t.Fatal("missing inherited-authority manifest was accepted")
	}
	stateDir, err := prepareStateDirectory(home)
	if err != nil {
		t.Fatal(err)
	}
	token, err := randomHex(tokenBytes)
	if err != nil {
		t.Fatal(err)
	}
	epoch, err := randomHex(epochBytes)
	if err != nil {
		t.Fatal(err)
	}
	authority := inheritedAuthority{Home: home, Manifest: Manifest{
		PID: os.Getpid(), Protocol: ProtocolVersion, Token: token,
		Endpoint: endpointForStateDirectory(stateDir), Epoch: epoch,
	}}
	raw, err := MarshalCanonical(authority)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv(inheritedAuthorityEnvironment, base64.RawURLEncoding.EncodeToString(raw))
	if _, _, err := ConnectInherited(t.Context()); CodeOf(err) != CodeUnavailable {
		t.Fatalf("inherited authority with unavailable broker = %v", err)
	}
	t.Setenv(inheritedAuthorityEnvironment, base64.RawURLEncoding.EncodeToString([]byte(" {}")))
	if _, _, err := ConnectInherited(t.Context()); CodeOf(err) != CodeUnauthorized {
		t.Fatalf("noncanonical inherited authority = %v", err)
	}
	invalidAuthority, err := MarshalCanonical(inheritedAuthority{Home: "bad\x00home", Manifest: Manifest{}})
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv(inheritedAuthorityEnvironment, base64.RawURLEncoding.EncodeToString(invalidAuthority))
	if _, _, err := ConnectInherited(t.Context()); CodeOf(err) != CodeUnauthorized {
		t.Fatalf("invalid inherited authority target = %v", err)
	}
	if got := safeDiscoveryError(os.ErrPermission); CodeOf(got) != CodeUnauthorized {
		t.Fatalf("permission discovery error = %v", got)
	}
	if got := safeDiscoveryError(errors.New("opaque")); CodeOf(got) != CodeUnavailable {
		t.Fatalf("opaque discovery error = %v", got)
	}
}

func TestRealDirectorySyncAndSocketValidationFailures(t *testing.T) {
	if err := syncDirectory(filepath.Join(t.TempDir(), "missing")); err == nil {
		t.Fatal("missing directory sync succeeded")
	}
	path := filepath.Join(t.TempDir(), "regular")
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := validateOwnerOnlySocket(path, info); CodeOf(err) != CodeIntegrity {
		t.Fatalf("regular file socket validation = %v", err)
	}
}

func TestServerAcceptRetriesTemporaryAndStopsOnFatalListenerError(t *testing.T) {
	serverContext, cancel := context.WithCancel(context.Background())
	listener := &realFailingListener{errors: []error{
		realTemporaryAcceptError{}, errors.New("fatal accept failure"),
	}}
	server := &Server{
		listener: listener,
		ctx:      serverContext,
		cancel:   cancel,
		done:     make(chan struct{}),
	}
	server.workers.Add(1)
	server.accept()
	select {
	case <-server.done:
	case <-time.After(5 * time.Second):
		t.Fatal("server did not finish after fatal listener failure")
	}
	if server.closeErr == nil || !strings.Contains(server.closeErr.Error(), "fatal accept failure") {
		t.Fatalf("fatal listener close error = %v", server.closeErr)
	}
}

func TestClientRediscoveryAndNilContextUseLiveBroker(t *testing.T) {
	calls := 0
	server, err := StartServer(t.Context(), ServerOptions{
		Home: t.TempDir(),
		Handler: HandlerFunc(func(context.Context, Request) (any, error) {
			calls++
			if calls == 1 {
				return nil, NewError(CodeConflict, "replace epoch")
			}
			return EmptyPayload{}, nil
		}),
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = server.Close(context.Background()) })
	client, err := Connect(server.home)
	if err != nil {
		t.Fatal(err)
	}
	var output EmptyPayload
	if err := client.Call(t.Context(), "domain", 1, "read", EmptyPayload{}, &output); err != nil {
		t.Fatal(err)
	}
	if calls != 2 {
		t.Fatalf("rediscovered call count = %d", calls)
	}
	if err := client.CallWithOptions(nil, "domain", 1, "read", EmptyPayload{}, nil, CallOptions{}); err != nil {
		t.Fatal(err)
	}
}

func TestClientRealStateAndCancellationBoundaries(t *testing.T) {
	if client, err := ConnectWithManifest(t.TempDir(), Manifest{}); err == nil || client != nil {
		t.Fatalf("client without state directory = %#v, %v", client, err)
	}
	home := t.TempDir()
	started := make(chan struct{}, 1)
	server, err := StartServer(t.Context(), ServerOptions{
		Home: home,
		Handler: HandlerFunc(func(ctx context.Context, _ Request) (any, error) {
			started <- struct{}{}
			<-ctx.Done()
			return nil, ctx.Err()
		}),
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = server.Close(context.Background()) })
	client, err := ConnectWithManifest(home, server.Manifest())
	if err != nil {
		t.Fatal(err)
	}
	requestContext, cancel := context.WithTimeout(t.Context(), 250*time.Millisecond)
	result := make(chan error, 1)
	go func() {
		result <- client.Call(requestContext, "domain", 1, "wait", EmptyPayload{}, nil)
	}()
	select {
	case <-started:
		cancel()
	case <-time.After(time.Second):
		cancel()
		t.Fatal("live request did not reach its handler")
	}
	if err := <-result; err == nil ||
		(CodeOf(err) != CodeDeadline && CodeOf(err) != CodeUnavailable) {
		t.Fatalf("canceled live request = %v", err)
	}
}

func TestIdempotentClientRetryUsesRealLostResponseBoundary(t *testing.T) {
	for _, test := range []struct {
		name       string
		response   func(RequestEnvelope) ResponseEnvelope
		wantedCode ErrorCode
	}{
		{
			name: "replayed success",
			response: func(request RequestEnvelope) ResponseEnvelope {
				return coverageResponse(request, EmptyPayload{})
			},
		},
		{
			name: "definitive retry rejection",
			response: func(request RequestEnvelope) ResponseEnvelope {
				return ResponseEnvelope{
					Protocol: ProtocolVersion, RequestID: request.RequestID,
					BrokerEpoch: request.BrokerEpoch, Error: NewError(CodeInvalid, "rejected"),
				}
			},
			wantedCode: CodeInvalid,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			home := t.TempDir()
			stateDir, err := prepareStateDirectory(home)
			if err != nil {
				t.Fatal(err)
			}
			endpoint := endpointForStateDirectory(stateDir)
			if err := prepareEndpoint(endpoint); err != nil {
				t.Fatal(err)
			}
			listener, err := listenLocal(endpoint)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = listener.Close() })
			token, _ := randomHex(tokenBytes)
			epoch, _ := randomHex(epochBytes)
			manifest := Manifest{
				PID: os.Getpid(), Protocol: ProtocolVersion, Token: token,
				Endpoint: endpoint, Epoch: epoch,
			}
			done := make(chan struct{})
			go func() {
				defer close(done)
				for attempt := 0; attempt < 2; attempt++ {
					connection, acceptErr := listener.Accept()
					if acceptErr != nil {
						return
					}
					var request RequestEnvelope
					if readFrameStrict(connection, &request) == nil && attempt == 1 {
						_ = WriteFrame(connection, test.response(request))
					}
					_ = connection.Close()
				}
			}()
			client, err := ConnectWithManifest(home, manifest)
			if err != nil {
				t.Fatal(err)
			}
			err = client.CallWithOptions(
				t.Context(), "domain", 1, "write", EmptyPayload{}, nil,
				CallOptions{Mutation: true, IdempotencyKey: "stable-retry"},
			)
			if CodeOf(err) != test.wantedCode {
				t.Fatalf("retry result = %v, want %s", err, test.wantedCode)
			}
			<-done
		})
	}
}

func TestServeConnectionUsesBoundedDeadlineAndHandlesLostPeer(t *testing.T) {
	now := time.Now().UTC()
	newServer := func(handler Handler) *Server {
		return &Server{
			manifest: Manifest{Token: strings.Repeat("a", tokenBytes*2), Epoch: strings.Repeat("b", epochBytes*2)},
			now:      nowFunc(now), handler: handler, idempotency: newIdempotencyRegistry(),
			ctx: context.Background(),
		}
	}
	envelope := RequestEnvelope{
		Protocol: ProtocolVersion, RequestID: "request", Token: strings.Repeat("a", tokenBytes*2),
		BrokerEpoch: strings.Repeat("b", epochBytes*2), Domain: "domain", DomainVersion: 1,
		Operation: "read", DeadlineUnixNs: now.Add(time.Hour).UnixNano(), Payload: []byte(`{}`),
	}

	t.Run("far deadline", func(t *testing.T) {
		server := newServer(HandlerFunc(func(context.Context, Request) (any, error) {
			return EmptyPayload{}, nil
		}))
		serverSide, clientSide := net.Pipe()
		server.workers.Add(1)
		go server.serveConnection(serverSide)
		if err := WriteFrame(clientSide, envelope); err != nil {
			t.Fatal(err)
		}
		var response ResponseEnvelope
		if err := readFrameStrict(clientSide, &response); err != nil || response.Error != nil {
			t.Fatalf("bounded deadline response = %#v, %v", response, err)
		}
		_ = clientSide.Close()
		server.workers.Wait()
	})

	t.Run("lost peer", func(t *testing.T) {
		entered := make(chan struct{})
		release := make(chan struct{})
		server := newServer(HandlerFunc(func(context.Context, Request) (any, error) {
			close(entered)
			<-release
			return EmptyPayload{}, nil
		}))
		serverSide, clientSide := net.Pipe()
		server.workers.Add(1)
		go server.serveConnection(serverSide)
		if err := WriteFrame(clientSide, envelope); err != nil {
			t.Fatal(err)
		}
		<-entered
		_ = clientSide.Close()
		close(release)
		server.workers.Wait()
	})
}

func nowFunc(value time.Time) func() time.Time {
	return func() time.Time { return value }
}
