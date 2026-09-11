package database

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/sipeed/picoclaw/pkg/config"
	dblayer "github.com/sipeed/picoclaw/pkg/database"
	dbcatalog "github.com/sipeed/picoclaw/pkg/database/catalog"
)

const (
	supervisorCommandChildEnvironment = "PICOCLAW_DATABASE_COMMAND_TEST_CHILD"
	testConfigRevision                = "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	testCatalogFingerprint            = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
)

func init() {
	if os.Getenv(supervisorCommandChildEnvironment) != "1" {
		return
	}
	if len(os.Args) < 3 || os.Args[1] != "database" || os.Args[2] != "__serve" {
		_, _ = fmt.Fprintln(os.Stderr, "invalid database command child arguments")
		os.Exit(1)
	}
	command := NewDatabaseCommand()
	command.SetArgs(os.Args[2:])
	command.SetOut(io.Discard)
	command.SetErr(io.Discard)
	if err := command.Execute(); err != nil {
		_, _ = fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	os.Exit(0)
}

type testServeGuard struct {
	calls    []string
	failCall int
	err      error
}

func (guard *testServeGuard) Validate(home string) error {
	guard.calls = append(guard.calls, home)
	if len(guard.calls) == guard.failCall {
		return guard.err
	}
	return nil
}

type testServeCatalog struct {
	entries       []dbcatalog.Entry
	bindings      []dblayer.StoreBinding
	required      []dblayer.StoreID
	entryCalls    int
	bindingCalls  int
	requiredCalls int
}

func (catalog *testServeCatalog) Entries() []dbcatalog.Entry {
	catalog.entryCalls++
	return append([]dbcatalog.Entry(nil), catalog.entries...)
}

func (catalog *testServeCatalog) Bindings() []dblayer.StoreBinding {
	catalog.bindingCalls++
	return append([]dblayer.StoreBinding(nil), catalog.bindings...)
}

func (catalog *testServeCatalog) RequiredStores() []dblayer.StoreID {
	catalog.requiredCalls++
	return append([]dblayer.StoreID(nil), catalog.required...)
}

type testServeRegistry struct {
	closeCalls int
	closeErr   error
}

func (*testServeRegistry) Handle(context.Context, dblayer.Request) (any, error) {
	return nil, dblayer.NewError(dblayer.CodeUnsupported, "test registry is empty")
}

func (registry *testServeRegistry) Close() error {
	registry.closeCalls++
	return registry.closeErr
}

type testServeServer struct {
	done       chan struct{}
	closeCalls int
	closeErr   error
	closeHook  func(context.Context) error
}

func (server *testServeServer) Done() <-chan struct{} {
	return server.done
}

func (server *testServeServer) Close(ctx context.Context) error {
	server.closeCalls++
	if server.closeHook != nil {
		return server.closeHook(ctx)
	}
	return server.closeErr
}

type supervisorServeHarness struct {
	now        time.Time
	home       string
	configPath string
	userHome   string
	guard      *testServeGuard
	catalog    *testServeCatalog
	registry   *testServeRegistry
	server     *testServeServer
	trace      []string

	loadCalls     int
	snapshotCalls int
	revisionCalls int
	lockCalls     int
	startCalls    int
	stopCalls     int
	serverOptions dblayer.ServerOptions
	statuses      [][]dblayer.StoreStatus
}

func newSupervisorServeHarness(t *testing.T) (*supervisorServeHarness, supervisorServeOps) {
	t.Helper()
	root := filepath.Join(t.TempDir(), "trusted")
	done := make(chan struct{})
	close(done)
	harness := &supervisorServeHarness{
		now:        time.Unix(2_000_000_000, 0),
		home:       filepath.Join(root, "home"),
		configPath: filepath.Join(root, "config.json"),
		userHome:   filepath.Join(root, "user"),
		guard:      &testServeGuard{},
		catalog: &testServeCatalog{
			entries: []dbcatalog.Entry{
				{ID: "global/git-workspace-inventory", Domain: "git-workspace-inventory", Required: true},
				{ID: "workspace/local-ci", Domain: "local-ci"},
			},
			bindings: []dblayer.StoreBinding{
				{ID: "global/git-workspace-inventory", Domain: "git-workspace-inventory"},
				{ID: "workspace/local-ci", Domain: "local-ci"},
			},
			required: []dblayer.StoreID{"global/git-workspace-inventory"},
		},
		registry: &testServeRegistry{},
		server:   &testServeServer{done: done},
	}
	ops := supervisorServeOps{
		home: func() string {
			harness.trace = append(harness.trace, "home")
			return harness.home
		},
		consumeGuard: func(home string) (supervisorServeGuard, error) {
			harness.trace = append(harness.trace, "consume:"+home)
			return harness.guard, nil
		},
		canonicalHome: func(home string) (string, error) {
			harness.trace = append(harness.trace, "canonical:"+home)
			return home, nil
		},
		configPath: func() string {
			harness.trace = append(harness.trace, "config-path")
			return harness.configPath
		},
		loadConfigSnapshot: func(path string) (*config.Config, string, error) {
			harness.trace = append(harness.trace, "load:"+path)
			harness.loadCalls++
			return config.DefaultConfig(), testConfigRevision, nil
		},
		userHome: func() (string, error) {
			harness.trace = append(harness.trace, "user-home")
			return harness.userHome, nil
		},
		newReviewSnapshot: func(
			options dbcatalog.Options,
			revision string,
		) (supervisorServeCatalog, string, error) {
			harness.trace = append(harness.trace, "snapshot:"+revision)
			harness.snapshotCalls++
			if options.Home != harness.home || options.Config == nil ||
				options.ConfigPath != harness.configPath || options.UserHome != harness.userHome {
				return nil, "", errors.New("snapshot received the wrong generation")
			}
			return harness.catalog, testCatalogFingerprint, nil
		},
		configRevision: func(path string) (string, error) {
			harness.trace = append(harness.trace, "revision:"+path)
			harness.revisionCalls++
			return testConfigRevision, nil
		},
		withConfigLock: func(path string, operation func() error) error {
			harness.trace = append(harness.trace, "lock:"+path)
			harness.lockCalls++
			return operation()
		},
		newRegistry: func() supervisorServeRegistry {
			harness.trace = append(harness.trace, "registry")
			return harness.registry
		},
		startServer: func(
			ctx context.Context,
			options dblayer.ServerOptions,
		) (supervisorServeServer, error) {
			harness.trace = append(harness.trace, "start")
			harness.startCalls++
			harness.serverOptions = options
			if _, hasDeadline := ctx.Deadline(); hasDeadline {
				return nil, errors.New("startup deadline became server lifetime")
			}
			if err := options.StartupGuard(); err != nil {
				return nil, err
			}
			first, err := options.StatusProvider(ctx)
			if err != nil {
				return nil, err
			}
			first[0].ID = "mutated/status"
			second, err := options.StatusProvider(ctx)
			if err != nil {
				return nil, err
			}
			harness.statuses = append(harness.statuses, first, second)
			harness.server.closeHook = func(closeContext context.Context) error {
				if _, hasDeadline := closeContext.Deadline(); hasDeadline {
					return errors.New("normal close used a deadline")
				}
				return options.CloseHandler()
			}
			return harness.server, nil
		},
		notifyContext: func(
			ctx context.Context,
			signals ...os.Signal,
		) (context.Context, context.CancelFunc) {
			harness.trace = append(harness.trace, "notify:"+strconv.Itoa(len(signals)))
			child, cancel := context.WithCancel(ctx)
			return child, func() {
				harness.stopCalls++
				cancel()
			}
		},
		now: func() time.Time { return harness.now },
	}
	return harness, ops
}

func (harness *supervisorServeHarness) arguments() []string {
	return []string{
		"--home", harness.home,
		"--expected-catalog-fingerprint", testCatalogFingerprint,
		"--startup-deadline-unix-ns",
		strconv.FormatInt(harness.now.Add(time.Hour).UnixNano(), 10),
	}
}

func TestDatabaseCommandIsHiddenAndRunsOneGuardedSnapshot(t *testing.T) {
	harness, ops := newSupervisorServeHarness(t)
	command := newDatabaseCommand(ops)
	children := command.Commands()
	if command.Name() != "database" || !command.Hidden || len(children) != 1 ||
		children[0].Name() != "__serve" || !children[0].Hidden ||
		!children[0].DisableFlagParsing {
		t.Fatalf("private database command = %#v, children %#v", command, children)
	}
	command.SetArgs(append([]string{"__serve"}, harness.arguments()...))
	if err := command.ExecuteContext(context.Background()); err != nil {
		t.Fatal(err)
	}
	if harness.loadCalls != 1 || harness.snapshotCalls != 1 ||
		harness.catalog.entryCalls != 1 || harness.catalog.bindingCalls != 1 ||
		harness.catalog.requiredCalls != 1 ||
		harness.revisionCalls != 3 || harness.lockCalls != 1 || harness.startCalls != 1 {
		t.Fatalf(
			"composition calls load=%d snapshot=%d entries=%d bindings=%d required=%d revision=%d lock=%d start=%d",
			harness.loadCalls,
			harness.snapshotCalls,
			harness.catalog.entryCalls,
			harness.catalog.bindingCalls,
			harness.catalog.requiredCalls,
			harness.revisionCalls,
			harness.lockCalls,
			harness.startCalls,
		)
	}
	if len(harness.guard.calls) != 4 {
		t.Fatalf("guard calls = %#v, want four checkpoints", harness.guard.calls)
	}
	for _, home := range harness.guard.calls {
		if home != harness.home {
			t.Fatalf("guard home = %q, want %q", home, harness.home)
		}
	}
	if harness.serverOptions.Home != harness.home ||
		harness.serverOptions.CatalogFingerprint != testCatalogFingerprint ||
		len(harness.serverOptions.RequiredStores) != 1 ||
		harness.serverOptions.RequiredStores[0] != "global/git-workspace-inventory" ||
		len(harness.serverOptions.ServedStores) != 2 ||
		harness.serverOptions.ServedStores[0] != harness.catalog.bindings[0] ||
		harness.serverOptions.ServedStores[1] != harness.catalog.bindings[1] ||
		harness.serverOptions.Handler != harness.registry {
		t.Fatalf("server options = %#v", harness.serverOptions)
	}
	if len(harness.statuses) != 2 || len(harness.statuses[0]) != 2 ||
		len(harness.statuses[1]) != 2 ||
		harness.statuses[1][0].ID != "global/git-workspace-inventory" {
		t.Fatalf("detached complete statuses = %#v", harness.statuses)
	}
	for _, status := range harness.statuses[1] {
		if status.Readiness != dblayer.StoreUnavailable || status.Error == nil ||
			status.Error.Code != dblayer.CodeUnavailable {
			t.Fatalf("uncomposed status = %#v", status)
		}
	}
	if harness.registry.closeCalls != 1 || harness.server.closeCalls != 1 || harness.stopCalls != 1 {
		t.Fatalf(
			"shutdown calls registry=%d server=%d stop=%d",
			harness.registry.closeCalls,
			harness.server.closeCalls,
			harness.stopCalls,
		)
	}
	wantPrefix := []string{
		"home",
		"consume:" + harness.home,
		"canonical:" + harness.home,
		"config-path",
		"notify:2",
		"load:" + harness.configPath,
	}
	if len(harness.trace) < len(wantPrefix) {
		t.Fatalf("composition trace = %#v", harness.trace)
	}
	for index := range wantPrefix {
		if harness.trace[index] != wantPrefix[index] {
			t.Fatalf("composition trace = %#v, want prefix %#v", harness.trace, wantPrefix)
		}
	}
}

func TestValidateReviewPublicationRequiresOneExactLogicalScope(t *testing.T) {
	entries := []dbcatalog.Entry{
		{ID: "global/git-workspace-inventory", Domain: "git-workspace-inventory", Required: true},
		{ID: "workspace/local-ci", Domain: "local-ci"},
	}
	bindings := []dblayer.StoreBinding{
		{ID: entries[0].ID, Domain: entries[0].Domain},
		{ID: entries[1].ID, Domain: entries[1].Domain},
	}
	required := []dblayer.StoreID{entries[0].ID}
	if err := validateReviewPublication(entries, bindings, required); err != nil {
		t.Fatal(err)
	}

	for _, test := range []struct {
		name     string
		entries  []dbcatalog.Entry
		bindings []dblayer.StoreBinding
		required []dblayer.StoreID
	}{
		{name: "empty"},
		{name: "missing binding", entries: entries, bindings: bindings[:1], required: required},
		{
			name: "wrong binding ID", entries: entries,
			bindings: []dblayer.StoreBinding{{ID: "global/other", Domain: entries[0].Domain}, bindings[1]},
			required: required,
		},
		{
			name: "wrong binding domain", entries: entries,
			bindings: []dblayer.StoreBinding{{ID: entries[0].ID, Domain: "other"}, bindings[1]},
			required: required,
		},
		{
			name: "unsorted entries", entries: []dbcatalog.Entry{entries[1], entries[0]},
			bindings: []dblayer.StoreBinding{bindings[1], bindings[0]}, required: required,
		},
		{
			name: "invalid entry", entries: []dbcatalog.Entry{{ID: "bad id", Domain: "bad", Required: true}},
			bindings: []dblayer.StoreBinding{{ID: "bad id", Domain: "bad"}}, required: []dblayer.StoreID{"bad id"},
		},
		{name: "missing required", entries: entries, bindings: bindings},
		{
			name: "wrong required", entries: entries, bindings: bindings,
			required: []dblayer.StoreID{entries[1].ID},
		},
		{
			name: "extra required", entries: entries, bindings: bindings,
			required: []dblayer.StoreID{entries[0].ID, entries[1].ID},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			if err := validateReviewPublication(
				test.entries,
				test.bindings,
				test.required,
			); dblayer.CodeOf(
				err,
			) != dblayer.CodeIntegrity {
				t.Fatalf("invalid publication error = %v, want Integrity", err)
			}
		})
	}
}

func TestDatabaseServeAuthenticatesBeforePrivateArgumentValidation(t *testing.T) {
	t.Run("instance seam", func(t *testing.T) {
		harness, ops := newSupervisorServeHarness(t)
		unauthorized := dblayer.NewError(dblayer.CodeUnauthorized, "bootstrap unavailable")
		ops.consumeGuard = func(home string) (supervisorServeGuard, error) {
			harness.trace = append(harness.trace, "consume:"+home)
			return nil, unauthorized
		}
		command := newDatabaseCommand(ops)
		command.SetArgs([]string{"__serve", "--unknown-private-flag"})
		if err := command.Execute(); !errors.Is(err, unauthorized) {
			t.Fatalf("malformed direct invocation error = %v", err)
		}
		if len(harness.trace) != 2 || harness.trace[0] != "home" ||
			harness.trace[1] != "consume:"+harness.home {
			t.Fatalf("pre-authorization operations = %#v", harness.trace)
		}
	})

	t.Run("production operations", func(t *testing.T) {
		t.Setenv("PICOCLAW_DATABASE_SUPERVISOR_BOOTSTRAP", "")
		t.Setenv("PICOCLAW_DATABASE_SUPERVISOR_BOOTSTRAP_IDENTITY", "")
		t.Setenv("PICOCLAW_DATABASE_SUPERVISOR_EXECUTABLE_IDENTITY", "")
		command := NewDatabaseCommand()
		command.SetArgs([]string{"__serve", "--unknown-private-flag"})
		if err := command.Execute(); dblayer.CodeOf(err) != dblayer.CodeUnauthorized {
			t.Fatalf("direct production invocation error = %v, want Unauthorized", err)
		}
	})

	t.Run("authorized malformed arguments", func(t *testing.T) {
		harness, ops := newSupervisorServeHarness(t)
		if err := runServeCommand(
			context.Background(),
			[]string{"--unknown-private-flag"},
			ops,
		); dblayer.CodeOf(err) != dblayer.CodeInvalid {
			t.Fatalf("authorized malformed invocation error = %v", err)
		}
		if len(harness.trace) != 2 || len(harness.guard.calls) != 0 {
			t.Fatalf("authorized malformed invocation trace = %#v, guard = %#v", harness.trace, harness.guard.calls)
		}
	})
}

func TestParseSupervisorServeArgumentsIsStrict(t *testing.T) {
	now := time.Unix(2_000_000_000, 0)
	deadline := strconv.FormatInt(now.UnixNano(), 10)
	valid := []string{
		"--home", "/home",
		"--expected-catalog-fingerprint", testCatalogFingerprint,
		"--startup-deadline-unix-ns", deadline,
	}
	parsed, err := parseSupervisorServeArguments(valid)
	if err != nil || parsed.home != "/home" ||
		parsed.expectedCatalogFingerprint != testCatalogFingerprint ||
		!parsed.startupDeadline.Equal(now) {
		t.Fatalf("valid private arguments = %#v, %v", parsed, err)
	}
	for _, test := range []struct {
		name string
		args []string
	}{
		{name: "wrong count", args: valid[:5]},
		{name: "empty value", args: []string{
			"--home", "", "--expected-catalog-fingerprint", testCatalogFingerprint,
			"--startup-deadline-unix-ns", deadline,
		}},
		{name: "wrong order", args: []string{
			"--expected-catalog-fingerprint", testCatalogFingerprint, "--home", "/home",
			"--startup-deadline-unix-ns", deadline,
		}},
		{name: "unknown", args: []string{
			"--home", "/home", "--private-new", "value", "--startup-deadline-unix-ns", deadline,
		}},
		{name: "noninteger", args: []string{
			"--home", "/home", "--expected-catalog-fingerprint", testCatalogFingerprint,
			"--startup-deadline-unix-ns", "later",
		}},
		{name: "nonpositive", args: []string{
			"--home", "/home", "--expected-catalog-fingerprint", testCatalogFingerprint,
			"--startup-deadline-unix-ns", "0",
		}},
		{name: "noncanonical integer", args: []string{
			"--home", "/home", "--expected-catalog-fingerprint", testCatalogFingerprint,
			"--startup-deadline-unix-ns", "+1",
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			if parsed, err := parseSupervisorServeArguments(test.args); parsed != (supervisorServeArguments{}) ||
				dblayer.CodeOf(err) != dblayer.CodeInvalid {
				t.Fatalf("private arguments = %#v, %v", parsed, err)
			}
		})
	}
}

func TestSupervisorServeRejectsEveryPrecompositionFailure(t *testing.T) {
	canary := errors.New("secret path canary")
	for _, test := range []struct {
		name      string
		configure func(*supervisorServeHarness, *supervisorServeOps)
		wantCode  dblayer.ErrorCode
		contains  string
	}{
		{
			name: "invalid operations", wantCode: dblayer.CodeInternal,
			configure: func(_ *supervisorServeHarness, ops *supervisorServeOps) {
				ops.now = nil
			},
		},
		{
			name: "canonical home", wantCode: dblayer.CodeInvalid,
			configure: func(_ *supervisorServeHarness, ops *supervisorServeOps) {
				ops.canonicalHome = func(string) (string, error) {
					return "", dblayer.NewError(dblayer.CodeInvalid, "secret home")
				}
			},
		},
		{
			name: "missing guard", wantCode: dblayer.CodeInternal,
			configure: func(_ *supervisorServeHarness, ops *supervisorServeOps) {
				ops.consumeGuard = func(string) (supervisorServeGuard, error) {
					return nil, nil
				}
			},
		},
		{
			name: "immediate guard", wantCode: dblayer.CodeIntegrity,
			configure: func(harness *supervisorServeHarness, _ *supervisorServeOps) {
				harness.guard.failCall = 1
				harness.guard.err = dblayer.NewError(dblayer.CodeIntegrity, "guard changed")
			},
		},
		{
			name: "invalid config path", wantCode: dblayer.CodeInvalid,
			configure: func(harness *supervisorServeHarness, _ *supervisorServeOps) {
				harness.configPath = "relative.json"
			},
		},
		{
			name: "canceled before load", wantCode: dblayer.CodeDeadline,
			configure: func(_ *supervisorServeHarness, ops *supervisorServeOps) {
				ops.notifyContext = func(
					ctx context.Context,
					_ ...os.Signal,
				) (context.Context, context.CancelFunc) {
					child, cancel := context.WithCancel(ctx)
					cancel()
					return child, func() {}
				}
			},
		},
		{
			name: "nil signal context", wantCode: dblayer.CodeInternal,
			configure: func(_ *supervisorServeHarness, ops *supervisorServeOps) {
				ops.notifyContext = func(
					context.Context,
					...os.Signal,
				) (context.Context, context.CancelFunc) {
					return nil, func() {}
				}
			},
		},
		{
			name: "nil signal stop", wantCode: dblayer.CodeInternal,
			configure: func(_ *supervisorServeHarness, ops *supervisorServeOps) {
				ops.notifyContext = func(
					ctx context.Context,
					_ ...os.Signal,
				) (context.Context, context.CancelFunc) {
					return ctx, nil
				}
			},
		},
		{
			name: "expired before load", wantCode: dblayer.CodeDeadline,
			configure: func(harness *supervisorServeHarness, ops *supervisorServeOps) {
				originalNow := harness.now
				ops.now = func() time.Time { return originalNow.Add(2 * time.Hour) }
			},
		},
		{
			name: "migration required", wantCode: dblayer.CodeMigrationRequired,
			configure: func(_ *supervisorServeHarness, ops *supervisorServeOps) {
				ops.loadConfigSnapshot = func(string) (*config.Config, string, error) {
					return nil, "", config.ErrConfigMigrationRequired
				}
			},
		},
		{
			name: "config load unavailable", wantCode: dblayer.CodeUnavailable,
			configure: func(_ *supervisorServeHarness, ops *supervisorServeOps) {
				ops.loadConfigSnapshot = func(string) (*config.Config, string, error) {
					return nil, "", canary
				}
			},
		},
		{
			name: "user home unavailable", wantCode: dblayer.CodeUnavailable,
			configure: func(_ *supervisorServeHarness, ops *supervisorServeOps) {
				ops.userHome = func() (string, error) { return "", canary }
			},
		},
		{
			name: "catalog snapshot", wantCode: dblayer.CodeIntegrity,
			configure: func(_ *supervisorServeHarness, ops *supervisorServeOps) {
				ops.newReviewSnapshot = func(
					dbcatalog.Options,
					string,
				) (supervisorServeCatalog, string, error) {
					return nil, "", dblayer.NewError(dblayer.CodeIntegrity, "snapshot failed")
				}
			},
		},
		{
			name: "missing catalog", wantCode: dblayer.CodeInternal,
			configure: func(_ *supervisorServeHarness, ops *supervisorServeOps) {
				ops.newReviewSnapshot = func(
					dbcatalog.Options,
					string,
				) (supervisorServeCatalog, string, error) {
					return nil, testCatalogFingerprint, nil
				}
			},
		},
		{
			name: "invalid review publication", wantCode: dblayer.CodeIntegrity,
			configure: func(harness *supervisorServeHarness, _ *supervisorServeOps) {
				harness.catalog.bindings = nil
			},
		},
		{
			name: "prelock home replacement", wantCode: dblayer.CodeIntegrity,
			configure: func(harness *supervisorServeHarness, _ *supervisorServeOps) {
				harness.guard.failCall = 2
				harness.guard.err = dblayer.NewError(dblayer.CodeIntegrity, "guard changed")
			},
		},
		{
			name: "prelock revision failure", wantCode: dblayer.CodeConflict,
			configure: func(_ *supervisorServeHarness, ops *supervisorServeOps) {
				ops.configRevision = func(string) (string, error) { return "", canary }
			},
		},
		{
			name: "prelock revision drift", wantCode: dblayer.CodeConflict,
			configure: func(_ *supervisorServeHarness, ops *supervisorServeOps) {
				ops.configRevision = func(string) (string, error) { return "changed", nil }
			},
		},
		{
			name: "empty catalog fingerprint", wantCode: dblayer.CodeConflict,
			configure: func(_ *supervisorServeHarness, ops *supervisorServeOps) {
				ops.newReviewSnapshot = func(
					options dbcatalog.Options,
					revision string,
				) (supervisorServeCatalog, string, error) {
					catalog := &testServeCatalog{}
					_ = options
					_ = revision
					return catalog, "", nil
				}
			},
		},
		{
			name: "catalog fingerprint drift", wantCode: dblayer.CodeConflict,
			configure: func(harness *supervisorServeHarness, _ *supervisorServeOps) {
				harness.catalog.required = nil
			},
			contains: "mismatch-argument",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			harness, ops := newSupervisorServeHarness(t)
			test.configure(harness, &ops)
			arguments := harness.arguments()
			if test.contains != "" {
				arguments[3] = test.contains
			}
			err := runServeCommand(context.Background(), arguments, ops)
			if dblayer.CodeOf(err) != test.wantCode {
				t.Fatalf("precomposition error = %v, want %s", err, test.wantCode)
			}
			if strings.Contains(fmt.Sprint(err), canary.Error()) {
				t.Fatalf("precomposition error exposed private cause: %v", err)
			}
			if harness.startCalls != 0 {
				t.Fatalf("precomposition failure started %d servers", harness.startCalls)
			}
		})
	}
}

func TestSupervisorLaunchGenerationPredicate(t *testing.T) {
	harness, ops := newSupervisorServeHarness(t)
	generation := supervisorLaunchGeneration{
		guard: harness.guard, home: harness.home, configPath: harness.configPath,
		configRevision: testConfigRevision, catalogFingerprint: testCatalogFingerprint,
		expectedCatalogFingerprint: testCatalogFingerprint,
		startupDeadline:            harness.now.Add(time.Hour),
	}
	if err := generation.validate(context.Background(), ops); err != nil {
		t.Fatal(err)
	}
	if err := generation.validate(nil, ops); dblayer.CodeOf(err) != dblayer.CodeDeadline {
		t.Fatalf("nil context error = %v", err)
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if err := generation.validate(canceled, ops); dblayer.CodeOf(err) != dblayer.CodeDeadline {
		t.Fatalf("canceled context error = %v", err)
	}
	zeroDeadline := generation
	zeroDeadline.startupDeadline = time.Time{}
	if err := zeroDeadline.validate(context.Background(), ops); dblayer.CodeOf(err) != dblayer.CodeDeadline {
		t.Fatalf("zero deadline error = %v", err)
	}
	expired := generation
	expired.startupDeadline = harness.now
	if err := expired.validate(context.Background(), ops); dblayer.CodeOf(err) != dblayer.CodeDeadline {
		t.Fatalf("expired deadline error = %v", err)
	}
	homeChanged := generation
	homeChanged.guard = &testServeGuard{
		failCall: 1,
		err:      dblayer.NewError(dblayer.CodeIntegrity, "home changed"),
	}
	if err := homeChanged.validate(context.Background(), ops); dblayer.CodeOf(err) != dblayer.CodeIntegrity {
		t.Fatalf("home generation error = %v", err)
	}
	revisionFailureOps := ops
	revisionFailureOps.configRevision = func(string) (string, error) {
		return "", errors.New("secret revision path")
	}
	if err := generation.validate(
		context.Background(),
		revisionFailureOps,
	); dblayer.CodeOf(err) != dblayer.CodeConflict {
		t.Fatalf("revision failure error = %v", err)
	}
	revisionDriftOps := ops
	revisionDriftOps.configRevision = func(string) (string, error) { return "changed", nil }
	if err := generation.validate(context.Background(), revisionDriftOps); dblayer.CodeOf(err) != dblayer.CodeConflict {
		t.Fatalf("revision drift error = %v", err)
	}
	emptyFingerprint := generation
	emptyFingerprint.catalogFingerprint = ""
	if err := emptyFingerprint.validate(context.Background(), ops); dblayer.CodeOf(err) != dblayer.CodeConflict {
		t.Fatalf("empty fingerprint error = %v", err)
	}
	mismatchedFingerprint := generation
	mismatchedFingerprint.expectedCatalogFingerprint = "sha256:" + strings.Repeat("c", 64)
	if err := mismatchedFingerprint.validate(context.Background(), ops); dblayer.CodeOf(err) != dblayer.CodeConflict {
		t.Fatalf("mismatched fingerprint error = %v", err)
	}
}

func TestSupervisorServeJoinsCleanupOnEveryStartupFailure(t *testing.T) {
	cleanupErr := errors.New("registry cleanup canary")
	for _, test := range []struct {
		name      string
		configure func(*supervisorServeHarness, *supervisorServeOps)
		wantCode  dblayer.ErrorCode
	}{
		{
			name: "lock unavailable", wantCode: dblayer.CodeUnavailable,
			configure: func(_ *supervisorServeHarness, ops *supervisorServeOps) {
				ops.withConfigLock = func(string, func() error) error {
					return errors.New("secret lock path")
				}
			},
		},
		{
			name: "lock fails after callback", wantCode: dblayer.CodeUnavailable,
			configure: func(_ *supervisorServeHarness, ops *supervisorServeOps) {
				ops.withConfigLock = func(_ string, operation func() error) error {
					if err := operation(); err != nil {
						return err
					}
					return errors.New("secret unlock path")
				}
			},
		},
		{
			name: "under-lock guard", wantCode: dblayer.CodeIntegrity,
			configure: func(harness *supervisorServeHarness, _ *supervisorServeOps) {
				harness.guard.failCall = 3
				harness.guard.err = dblayer.NewError(dblayer.CodeIntegrity, "home changed")
			},
		},
		{
			name: "startup guard", wantCode: dblayer.CodeIntegrity,
			configure: func(harness *supervisorServeHarness, _ *supervisorServeOps) {
				harness.guard.failCall = 4
				harness.guard.err = dblayer.NewError(dblayer.CodeIntegrity, "home changed")
			},
		},
		{
			name: "server startup", wantCode: dblayer.CodeAlreadyExists,
			configure: func(_ *supervisorServeHarness, ops *supervisorServeOps) {
				ops.startServer = func(
					context.Context,
					dblayer.ServerOptions,
				) (supervisorServeServer, error) {
					return nil, dblayer.NewError(dblayer.CodeAlreadyExists, "secret owner")
				}
			},
		},
		{
			name: "unclassified server startup", wantCode: dblayer.CodeInternal,
			configure: func(_ *supervisorServeHarness, ops *supervisorServeOps) {
				ops.startServer = func(
					context.Context,
					dblayer.ServerOptions,
				) (supervisorServeServer, error) {
					return nil, errors.New("secret server path")
				}
			},
		},
		{
			name: "missing server", wantCode: dblayer.CodeInternal,
			configure: func(_ *supervisorServeHarness, ops *supervisorServeOps) {
				ops.startServer = func(
					context.Context,
					dblayer.ServerOptions,
				) (supervisorServeServer, error) {
					return nil, nil
				}
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			harness, ops := newSupervisorServeHarness(t)
			harness.registry.closeErr = cleanupErr
			test.configure(harness, &ops)
			err := runServeCommand(context.Background(), harness.arguments(), ops)
			if dblayer.CodeOf(err) != test.wantCode || !errors.Is(err, cleanupErr) {
				t.Fatalf("startup error = %v, want %s joined with cleanup", err, test.wantCode)
			}
			if harness.registry.closeCalls != 1 {
				t.Fatalf("registry close calls = %d, want 1", harness.registry.closeCalls)
			}
		})
	}

	t.Run("nil registry", func(t *testing.T) {
		harness, ops := newSupervisorServeHarness(t)
		ops.newRegistry = func() supervisorServeRegistry { return nil }
		if err := runServeCommand(
			context.Background(),
			harness.arguments(),
			ops,
		); dblayer.CodeOf(err) != dblayer.CodeInternal {
			t.Fatalf("nil registry error = %v", err)
		}
	})
}

func TestSupervisorServeContextCancellationUsesNormalServerDrain(t *testing.T) {
	harness, ops := newSupervisorServeHarness(t)
	started := make(chan struct{})
	done := make(chan struct{})
	harness.server = &testServeServer{done: done}
	ops.startServer = func(
		ctx context.Context,
		options dblayer.ServerOptions,
	) (supervisorServeServer, error) {
		if err := options.StartupGuard(); err != nil {
			return nil, err
		}
		harness.server.closeHook = func(context.Context) error {
			return options.CloseHandler()
		}
		close(started)
		go func() {
			<-ctx.Done()
			close(done)
		}()
		return harness.server, nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() {
		result <- runServeCommand(ctx, harness.arguments(), ops)
	}()
	<-started
	cancel()
	if err := <-result; err != nil {
		t.Fatal(err)
	}
	if harness.server.closeCalls != 1 || harness.registry.closeCalls != 1 ||
		harness.stopCalls != 1 {
		t.Fatalf(
			"canceled drain calls server=%d registry=%d stop=%d",
			harness.server.closeCalls,
			harness.registry.closeCalls,
			harness.stopCalls,
		)
	}
}

func TestPrivateConfigPathAndFailureSanitizer(t *testing.T) {
	absolute := filepath.Join(string(filepath.Separator), "private", "config.json")
	if !validPrivateConfigPath(absolute) {
		t.Fatalf("valid private config path %q rejected", absolute)
	}
	for _, path := range []string{"", "relative.json", absolute + string(filepath.Separator) + "..", " " + absolute, absolute + "\x00"} {
		if validPrivateConfigPath(path) {
			t.Fatalf("invalid private config path %q accepted", path)
		}
	}
	if err := supervisorServeFailure(nil, "fixed"); dblayer.CodeOf(err) != dblayer.CodeInternal ||
		err.Error() != "Internal: fixed" {
		t.Fatalf("nil failure sanitizer = %v", err)
	}
	private := dblayer.NewError(dblayer.CodeConflict, "secret")
	if err := supervisorServeFailure(private, "fixed"); dblayer.CodeOf(err) != dblayer.CodeConflict ||
		err.Error() != "Conflict: fixed" {
		t.Fatalf("typed failure sanitizer = %v", err)
	}
}

func TestDefaultSupervisorServeOperationAdapters(t *testing.T) {
	ops := defaultSupervisorServeOps()
	if !validSupervisorServeOps(ops) {
		t.Fatal("default supervisor serve operations are incomplete")
	}
	ctx, stop := ops.notifyContext(context.Background(), os.Interrupt)
	stop()
	if ctx.Err() == nil {
		t.Fatal("default signal context stop did not cancel")
	}
	if userHome, err := ops.userHome(); err != nil || userHome == "" {
		t.Fatalf("default user home = %q, %v", userHome, err)
	}

	home := t.TempDir()
	if err := os.Chmod(home, 0o700); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(home, "config.json")
	cfg := config.DefaultConfig()
	cfg.Agents.Defaults.Workspace = filepath.Join(home, "workspace")
	if err := config.SaveConfig(configPath, cfg); err != nil {
		t.Fatal(err)
	}
	loaded, revision, err := ops.loadConfigSnapshot(configPath)
	if err != nil {
		t.Fatal(err)
	}
	userHome, err := ops.userHome()
	if err != nil {
		t.Fatal(err)
	}
	logical, fingerprint, err := ops.newReviewSnapshot(dbcatalog.Options{
		Home: home, Config: loaded, ConfigPath: configPath, UserHome: userHome,
	}, revision)
	if err != nil || logical == nil || fingerprint == "" || len(logical.Entries()) != 5 ||
		len(logical.Bindings()) != 5 || len(logical.RequiredStores()) != 4 {
		t.Fatalf("default snapshot adapter = %#v, %q, %v", logical, fingerprint, err)
	}
	registry := ops.newRegistry()
	if registry == nil || registry.Close() != nil {
		t.Fatalf("default registry adapter = %#v", registry)
	}
	server, err := ops.startServer(context.Background(), dblayer.ServerOptions{Home: home})
	if err != nil || server == nil {
		t.Fatalf("default server adapter = %#v, %v", server, err)
	}
	if err := server.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestEnsureSupervisorRunsCopiedBinaryThroughHiddenCommand(t *testing.T) {
	if testing.Short() {
		t.Skip("real child process test")
	}
	home := t.TempDir()
	if err := os.Chmod(home, 0o700); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(t.TempDir(), "config.json")
	cfg := config.DefaultConfig()
	cfg.Agents.Defaults.Workspace = filepath.Join(home, "workspace")
	firstWorkspace := filepath.Join(home, "agents", "first")
	secondWorkspace := filepath.Join(home, "agents", "second")
	cfg.Agents.List = []config.AgentConfig{
		{ID: "first", Workspace: firstWorkspace},
		{ID: "shared", Workspace: firstWorkspace},
		{ID: "second", Workspace: secondWorkspace},
	}
	if err := config.SaveConfig(configPath, cfg); err != nil {
		t.Fatal(err)
	}
	loaded, revision, err := config.LoadCurrentConfigSnapshot(configPath)
	if err != nil {
		t.Fatal(err)
	}
	userHome, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	options := dbcatalog.Options{
		Home: home, Config: loaded, ConfigPath: configPath, UserHome: userHome,
	}
	fullCatalog, fullFingerprint, err := dbcatalog.NewSnapshot(options, revision)
	if err != nil || fullCatalog == nil || fullFingerprint == "" {
		t.Fatalf("full catalog snapshot = %#v, %q, %v", fullCatalog, fullFingerprint, err)
	}
	logical, fingerprint, err := dbcatalog.NewReviewSnapshot(options, revision)
	if err != nil {
		t.Fatal(err)
	}
	if fingerprint == fullFingerprint || len(logical.Entries()) != 11 ||
		len(logical.Bindings()) != 11 || len(logical.RequiredStores()) != 8 {
		t.Fatalf(
			"review snapshot fingerprint=%q full=%q entries=%d bindings=%d required=%d",
			fingerprint,
			fullFingerprint,
			len(logical.Entries()),
			len(logical.Bindings()),
			len(logical.RequiredStores()),
		)
	}
	oldServer, err := dblayer.StartServer(context.Background(), dblayer.ServerOptions{
		Home: home, CatalogFingerprint: fullFingerprint,
	})
	if err != nil {
		t.Fatalf("start old full-fingerprint broker: %v", err)
	}
	t.Cleanup(func() {
		cleanupContext, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = oldServer.Close(cleanupContext)
	})
	executable := copyCommandTestExecutable(t, home)
	t.Setenv(supervisorCommandChildEnvironment, "1")
	client, err := dblayer.EnsureSupervisor(t.Context(), dblayer.EnsureOptions{
		Home: home, Executable: executable, ConfigPath: configPath,
		CatalogFingerprint: fingerprint, Timeout: 10 * time.Second,
	})
	if err != nil {
		logData, _ := os.ReadFile(filepath.Join(home, "logs", "database-supervisor.log"))
		t.Fatalf("start copied hidden command: %v\n%s", err, logData)
	}
	select {
	case <-oldServer.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("old full-fingerprint broker was not replaced")
	}
	t.Cleanup(func() {
		cleanupContext, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = client.Shutdown(cleanupContext)
	})
	status, err := client.Status(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	entries := logical.Entries()
	if status.CatalogFingerprint != fingerprint || len(status.Stores) != 11 ||
		!slices.Equal(status.RequiredStores, logical.RequiredStores()) ||
		len(status.Stores) != len(entries) {
		t.Fatalf("hidden broker status = %#v", status)
	}
	for index, store := range status.Stores {
		if store.ID != entries[index].ID {
			t.Fatalf("hidden broker store %d = %q, want %q", index, store.ID, entries[index].ID)
		}
		if store.Readiness != dblayer.StoreUnavailable || store.Error == nil ||
			store.Error.Code != dblayer.CodeUnavailable {
			t.Fatalf("hidden broker store status = %#v", store)
		}
	}
	attached, err := dblayer.EnsureSupervisor(t.Context(), dblayer.EnsureOptions{
		Home: home, Executable: executable, ConfigPath: configPath,
		CatalogFingerprint: fingerprint, Timeout: 10 * time.Second,
	})
	if err != nil {
		t.Fatalf("attach same review generation: %v", err)
	}
	attachedStatus, err := attached.Status(t.Context())
	if err != nil || attachedStatus.Epoch != status.Epoch || attachedStatus.PID != status.PID {
		t.Fatalf("same-generation attach status = %#v, %v; original %#v", attachedStatus, err, status)
	}
	assertNoPhysicalStoreFiles(t, home)
	selected := entries[0]
	for _, request := range []struct {
		name string
		call func() error
		code dblayer.ErrorCode
	}{
		{
			name: "valid target reaches empty registry",
			call: func() error {
				return client.CallStore(
					t.Context(), selected.ID, selected.Domain, 1, "read",
					dblayer.EmptyPayload{}, &dblayer.EmptyPayload{},
				)
			},
			code: dblayer.CodeUnsupported,
		},
		{
			name: "omitted target",
			call: func() error {
				return client.Call(
					t.Context(), selected.Domain, 1, "read",
					dblayer.EmptyPayload{}, &dblayer.EmptyPayload{},
				)
			},
			code: dblayer.CodeInvalid,
		},
		{
			name: "unknown target",
			call: func() error {
				return client.CallStore(
					t.Context(), "global/auth", selected.Domain, 1, "read",
					dblayer.EmptyPayload{}, &dblayer.EmptyPayload{},
				)
			},
			code: dblayer.CodeUnsupported,
		},
		{
			name: "wrong domain",
			call: func() error {
				return client.CallStore(
					t.Context(), selected.ID, "auth", 1, "read",
					dblayer.EmptyPayload{}, &dblayer.EmptyPayload{},
				)
			},
			code: dblayer.CodeUnsupported,
		},
		{
			name: "payload target",
			call: func() error {
				return client.CallStore(
					t.Context(), selected.ID, selected.Domain, 1, "read",
					map[string]any{"store_id": "global/auth"}, &dblayer.EmptyPayload{},
				)
			},
			code: dblayer.CodeInvalid,
		},
	} {
		t.Run(request.name, func(t *testing.T) {
			if err := request.call(); dblayer.CodeOf(err) != request.code {
				t.Fatalf("scoped hidden broker call error = %v, want %s", err, request.code)
			}
		})
	}
	if err := client.Shutdown(t.Context()); err != nil &&
		dblayer.CodeOf(err) != dblayer.CodeOutcomeUnknown {
		t.Fatal(err)
	}
}

func assertNoPhysicalStoreFiles(t *testing.T, root string) {
	t.Helper()
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		name := strings.ToLower(entry.Name())
		if strings.HasSuffix(name, ".db") || strings.HasSuffix(name, ".db-wal") ||
			strings.HasSuffix(name, ".db-shm") || strings.HasSuffix(name, ".db-journal") {
			t.Errorf("hidden broker created physical store file %s", path)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func copyCommandTestExecutable(t *testing.T, home string) string {
	t.Helper()
	sourcePath, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	source, err := os.Open(sourcePath)
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close()
	destinationDirectory := filepath.Join(home, "test-bin")
	if mkdirErr := os.Mkdir(destinationDirectory, 0o700); mkdirErr != nil {
		t.Fatal(mkdirErr)
	}
	destinationName := "picoclaw-command-child"
	if strings.EqualFold(filepath.Ext(sourcePath), ".exe") {
		destinationName += ".exe"
	}
	destinationPath := filepath.Join(destinationDirectory, destinationName)
	destination, err := os.OpenFile(destinationPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o700)
	if err != nil {
		t.Fatal(err)
	}
	copyErr := error(nil)
	if _, err = io.Copy(destination, source); err != nil {
		copyErr = err
	}
	copyErr = errors.Join(copyErr, destination.Sync(), destination.Close())
	if copyErr != nil {
		t.Fatal(copyErr)
	}
	return destinationPath
}
