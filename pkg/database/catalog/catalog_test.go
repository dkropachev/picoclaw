//nolint:govet // Independent assertions intentionally use narrow error scopes.
package catalog

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/sipeed/picoclaw/internal/sqliteprovider"
	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/database"
)

func TestCatalogProjectionAndReadinessRequireOwnerAuthority(t *testing.T) {
	restoreAuthority := database.SuspendProviderTestAuthority()
	t.Cleanup(restoreAuthority)
	home := t.TempDir()
	cfg := &config.Config{}
	if value, err := New(home, cfg); value != nil || database.CodeOf(err) != database.CodeUnauthorized {
		t.Fatalf("unfenced catalog = %#v, %v", value, err)
	}
	if statuses, err := ProbeStatuses(t.Context(), home, cfg); statuses != nil ||
		database.CodeOf(err) != database.CodeUnauthorized {
		t.Fatalf("unfenced readiness = %#v, %v", statuses, err)
	}
}

func TestProbeStatusesClassifiesFreshMigrationAndIntegrity(t *testing.T) {
	home := t.TempDir()
	workspace := filepath.Join(home, "workspace")
	if err := os.MkdirAll(filepath.Join(workspace, "state"), 0o700); err != nil {
		t.Fatal(err)
	}
	cfg := config.DefaultConfig()
	cfg.Agents.Defaults.Workspace = workspace
	cfg.Workflows.Enabled = true

	statuses, err := ProbeStatuses(context.Background(), home, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if statusByID(statuses, "workspace.workflows").Readiness != database.StoreReady {
		t.Fatalf("missing empty workflow store is not ready: %#v", statuses)
	}

	workflowPath := filepath.Join(workspace, "state", "workflows.db")
	pool, err := sqliteprovider.OpenStore(workflowPath, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(`CREATE TABLE retained (id TEXT PRIMARY KEY)`); err != nil {
		t.Fatal(err)
	}
	if err := pool.Close(); err != nil {
		t.Fatal(err)
	}
	statuses, err = ProbeStatuses(context.Background(), home, cfg)
	if err != nil {
		t.Fatal(err)
	}
	workflowStatus := statusByID(statuses, "workspace.workflows")
	if workflowStatus.Readiness != database.StoreMigrationRequired {
		t.Fatalf("version-zero workflow readiness = %#v", workflowStatus)
	}
	logical, err := New(home, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := RequireReady(logical, statuses); database.CodeOf(err) != database.CodeMigrationRequired {
		t.Fatalf("RequireReady() error = %v", err)
	}

	if err := os.WriteFile(workflowPath, []byte("not a database"), 0o600); err != nil {
		t.Fatal(err)
	}
	statuses, err = ProbeStatuses(context.Background(), home, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if got := statusByID(statuses, "workspace.workflows").Readiness; got != database.StoreIntegrityFailed {
		t.Fatalf("corrupt workflow readiness = %q", got)
	}
}

func statusByID(statuses []database.StoreStatus, id database.StoreID) database.StoreStatus {
	for _, status := range statuses {
		if status.ID == id {
			return status
		}
	}
	return database.StoreStatus{}
}

func TestLegacyReadinessIgnoresEmptyAndDatabaseOnlyDirectories(t *testing.T) {
	root := t.TempDir()
	if found, err := legacyInputExists([]string{root}); err != nil || found {
		t.Fatalf("empty legacy directory = %v, %v", found, err)
	}
	if err := os.WriteFile(filepath.Join(root, "other.db"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if found, err := legacyInputExists([]string{root}); err != nil || found {
		t.Fatalf("database-only legacy directory = %v, %v", found, err)
	}
	if err := os.WriteFile(filepath.Join(root, "retained.json"), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	if found, err := legacyInputExists([]string{root}); err != nil || !found {
		t.Fatalf("retained legacy directory = %v, %v", found, err)
	}
}

func TestCatalogExposesOnlyLogicalStores(t *testing.T) {
	home := t.TempDir()
	workspace := filepath.Join(home, "workspace")
	cfg := &config.Config{
		Agents:    config.AgentsConfig{Defaults: config.AgentDefaults{Workspace: workspace}},
		Workflows: config.WorkflowsConfig{Enabled: true},
	}
	catalog, err := New(home, cfg)
	if err != nil {
		t.Fatal(err)
	}

	for _, name := range []string{
		"global.auth",
		"launcher.auth",
		"workspace.workflows",
		"workspace.sessions",
		"workspace.eventing",
		"workspace.seahorse",
		"channel.wecom",
		"channel.weixin",
	} {
		id, lookupErr := catalog.Lookup(name)
		if lookupErr != nil {
			t.Errorf("Lookup(%q): %v", name, lookupErr)
			continue
		}
		if id.String() != name {
			t.Errorf("Lookup(%q) = %q", name, id.String())
		}
		if strings.Contains(id.String(), string(os.PathSeparator)) || strings.Contains(id.String(), ".db") {
			t.Errorf("logical store ID leaks a physical location: %q", id.String())
		}
	}
	if _, err := catalog.Lookup(filepath.Join(home, "auth.db")); err == nil {
		t.Fatal("catalog accepted a physical path as a store ID")
	}
	if _, err := catalog.Lookup("unknown.store"); err == nil {
		t.Fatal("catalog accepted an unknown logical store ID")
	}
}

func TestCatalogLoadsDynamicChannelStores(t *testing.T) {
	home := t.TempDir()
	workspace := filepath.Join(home, "workspace")
	matrix := &config.Channel{Enabled: true, Type: config.ChannelMatrix}
	if err := matrix.Decode(&config.MatrixSettings{
		CryptoDatabasePath: filepath.Join(home, "matrix-data"),
		CryptoPassphrase:   "configured",
	}); err != nil {
		t.Fatal(err)
	}
	whatsapp := &config.Channel{Enabled: true, Type: config.ChannelWhatsAppNative}
	if err := whatsapp.Decode(&config.WhatsAppSettings{
		SessionStorePath: filepath.Join(home, "whatsapp-data"),
	}); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{
		Agents:   config.AgentsConfig{Defaults: config.AgentDefaults{Workspace: workspace}},
		Channels: config.ChannelsConfig{"secure matrix": matrix, "work-phone": whatsapp},
	}
	catalog, err := New(home, cfg)
	if err != nil {
		t.Fatal(err)
	}
	var matrixFound, whatsappFound bool
	for _, entry := range catalog.Entries() {
		switch entry.Domain {
		case "channel-matrix":
			matrixFound = entry.Required
		case "channel-whatsapp":
			whatsappFound = entry.Required
		}
	}
	if !matrixFound || !whatsappFound {
		t.Fatalf("dynamic stores missing: matrix=%v whatsapp=%v", matrixFound, whatsappFound)
	}
}

func TestCatalogRejectsStoreCollision(t *testing.T) {
	home := t.TempDir()
	cfg := &config.Config{
		Agents: config.AgentsConfig{Defaults: config.AgentDefaults{
			Workspace: filepath.Join(home, "workspace"),
		}},
		Events: config.EventsConfig{Ingress: config.EventIngressConfig{
			Enabled:      true,
			DatabasePath: filepath.Join(home, "auth.db"),
		}},
	}
	if _, err := New(home, cfg); err == nil || !strings.Contains(err.Error(), "resolve to one path") {
		t.Fatalf("collision error = %v", err)
	}
}

func TestLogicalCatalogDoesNotInspectExistingHardlinkAlias(t *testing.T) {
	home := t.TempDir()
	workspace := filepath.Join(home, "workspace")
	if err := os.MkdirAll(filepath.Join(workspace, "eventing"), 0o700); err != nil {
		t.Fatal(err)
	}
	authPath := filepath.Join(home, "auth.db")
	if err := os.WriteFile(authPath, []byte("physical identity"), 0o600); err != nil {
		t.Fatal(err)
	}
	eventPath := filepath.Join(workspace, "eventing", "events.db")
	if err := os.Link(authPath, eventPath); err != nil {
		t.Skipf("hard links unavailable: %v", err)
	}
	cfg := &config.Config{
		Agents: config.AgentsConfig{Defaults: config.AgentDefaults{Workspace: workspace}},
		Events: config.EventsConfig{Ingress: config.EventIngressConfig{
			Enabled: true,
		}},
	}
	if _, err := New(home, cfg); err != nil {
		t.Fatalf("logical catalog inspected a physical hardlink alias: %v", err)
	}
}

func TestCatalogRejectsSymlinkedHomeWithoutInspectingSidecar(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation is not generally available to unprivileged Windows tests")
	}
	realHome := t.TempDir()
	alias := filepath.Join(t.TempDir(), "home-link")
	if err := os.Symlink(realHome, alias); err != nil {
		t.Fatal(err)
	}
	if _, err := New(alias, &config.Config{}); err == nil {
		t.Fatal("catalog accepted a symlinked home")
	}

	target := filepath.Join(t.TempDir(), "wal")
	if err := os.WriteFile(target, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(realHome, "auth.db-wal")); err != nil {
		t.Fatal(err)
	}
	if _, err := New(realHome, &config.Config{}); err != nil {
		t.Fatalf("logical catalog inspected a SQLite sidecar: %v", err)
	}
}

func TestInitializeRequiredInitializesOnlyReadyRequiredStores(t *testing.T) {
	logical := &Catalog{entries: []Entry{
		{ID: "required.missing", Domain: "missing", Required: true},
		{ID: "required.unwritable", Domain: "unwritable", Required: true},
		{ID: "required.legacy", Domain: "legacy", Required: true},
		{ID: "optional.missing", Domain: "optional"},
	}}
	statuses := []database.StoreStatus{
		{ID: "required.missing", Readiness: database.StoreReady},
		{ID: "required.unwritable", Readiness: database.StoreReady},
		{
			ID: "required.legacy", Readiness: database.StoreMigrationRequired,
			Error: database.NewError(database.CodeMigrationRequired, "migration required"),
		},
		{ID: "optional.missing", Readiness: database.StoreReady},
	}
	called := make(map[database.StoreID]int)
	initialized, err := InitializeRequired(
		t.Context(), logical, statuses,
		func(_ context.Context, entry Entry) error {
			called[entry.ID]++
			if entry.ID == "required.unwritable" {
				return database.NewError(database.CodeUnavailable, "unwritable")
			}
			return nil
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if called["required.missing"] != 1 || called["required.unwritable"] != 1 ||
		called["required.legacy"] != 0 || called["optional.missing"] != 0 {
		t.Fatalf("readiness initialization calls = %#v", called)
	}
	byID := make(map[database.StoreID]database.StoreStatus, len(initialized))
	for _, status := range initialized {
		byID[status.ID] = status
	}
	if byID["required.missing"].Readiness != database.StoreReady {
		t.Fatalf("missing required status = %#v", byID["required.missing"])
	}
	if byID["required.unwritable"].Readiness != database.StoreUnavailable ||
		byID["required.unwritable"].Error == nil ||
		byID["required.unwritable"].Error.Code != database.CodeUnavailable {
		t.Fatalf("unwritable required status = %#v", byID["required.unwritable"])
	}
	if byID["required.legacy"].Readiness != database.StoreMigrationRequired {
		t.Fatalf("legacy required status = %#v", byID["required.legacy"])
	}
	if byID["optional.missing"].Readiness != database.StoreReady {
		t.Fatalf("optional status = %#v", byID["optional.missing"])
	}
	if err := RequireReady(logical, initialized); database.CodeOf(err) != database.CodeUnavailable {
		t.Fatalf("initialized readiness admission error = %v, want Unavailable", err)
	}
}

func TestInitializeRequiredMapsIntegrityAndMigrationFailures(t *testing.T) {
	logical := &Catalog{entries: []Entry{
		{ID: "required.integrity", Required: true},
		{ID: "required.migration", Required: true},
	}}
	statuses := []database.StoreStatus{
		{ID: "required.integrity", Readiness: database.StoreReady},
		{ID: "required.migration", Readiness: database.StoreReady},
	}
	initialized, err := InitializeRequired(
		t.Context(), logical, statuses,
		func(_ context.Context, entry Entry) error {
			if entry.ID == "required.integrity" {
				return database.NewError(database.CodeIntegrity, "bad")
			}
			return database.NewError(database.CodeMigrationRequired, "old")
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if initialized[0].Readiness != database.StoreIntegrityFailed ||
		initialized[1].Readiness != database.StoreMigrationRequired {
		t.Fatalf("classified statuses = %#v", initialized)
	}
}
