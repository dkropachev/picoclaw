package catalog

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sipeed/picoclaw/internal/sqliteprovider"
	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/database"
)

func TestLogicalCatalogLookupAndSnapshotBoundaries(t *testing.T) {
	if (*Catalog)(nil).Entries() != nil {
		t.Fatal("nil catalog returned entries")
	}
	if _, err := (*Catalog)(nil).Lookup("global/auth"); err == nil {
		t.Fatal("nil catalog resolved an entry")
	}
	if (*Catalog)(nil).Contains("global/auth") || (&Catalog{}).Contains("") {
		t.Fatal("invalid catalog containment succeeded")
	}

	home := t.TempDir()
	matrix := &config.Channel{Enabled: true, Type: config.ChannelMatrix, Settings: config.RawNode(`{}`)}
	whatsapp := &config.Channel{Enabled: true, Type: config.ChannelWhatsAppNative, Settings: config.RawNode(`{}`)}
	cfg := &config.Config{
		Agents:   config.AgentsConfig{Defaults: config.AgentDefaults{Workspace: filepath.Join(home, "workspace")}},
		Channels: config.ChannelsConfig{"matrix main": matrix, "phone": whatsapp},
	}
	logical, err := New(home, cfg)
	if err != nil {
		t.Fatal(err)
	}
	entries := logical.Entries()
	if len(entries) == 0 {
		t.Fatal("catalog entries are empty")
	}
	entries[0].Domain = "mutated"
	if logical.Entries()[0].Domain == "mutated" {
		t.Fatal("catalog entry snapshot aliases internal state")
	}
	for _, test := range []struct{ channelType, name string }{
		{config.ChannelMatrix, "matrix main"},
		{config.ChannelWhatsAppNative, "phone"},
	} {
		id, lookupErr := logical.LookupChannel(test.channelType, test.name)
		if lookupErr != nil || !logical.Contains(id) {
			t.Fatalf("LookupChannel(%q, %q) = %q, %v", test.channelType, test.name, id, lookupErr)
		}
	}
	if _, err := logical.LookupChannel(config.ChannelTelegram, "main"); err == nil {
		t.Fatal("non-database channel resolved")
	}
	if _, err := logical.Lookup(" "); err == nil {
		t.Fatal("blank catalog identity resolved")
	}
}

func TestReadinessDomainVersionAndHorizonTables(t *testing.T) {
	wantedHorizons := map[string]string{
		"auth": "auth", "model-catalogs": "model-catalogs", "tool-adaptation": "tool-adaptation",
		"workflows": "workflows", "sessions": "sessions", "cron": "cron-jobs",
		"runtime-state": "runtime-state", "account-routing": "account-router",
		"repository-reviews": "repository-reviews", "repository-evaluations": "repository-evaluations",
		"evolution": "evolution", "local-ci": "local_ci_cache", "channel-wecom": "wecom-reqid",
		"channel-weixin": "weixin-state", "git-workspace-inventory": "git-workspace-inventory",
		"pr-workspace-checkpoints": "pr-workspace-checkpoints",
	}
	for domain, component := range wantedHorizons {
		if got := importHorizonComponent(domain); got != component {
			t.Errorf("importHorizonComponent(%q) = %q, want %q", domain, got, component)
		}
		if expectedDomainVersion(domain) <= 0 {
			t.Errorf("expectedDomainVersion(%q) is not positive", domain)
		}
	}
	for _, domain := range []string{
		"eventing", "launcher-auth", "channel-matrix", "channel-whatsapp", "seahorse", "unknown",
	} {
		version := expectedDomainVersion(domain)
		if domain == "eventing" && version != 20 || domain == "unknown" && version != 0 ||
			domain != "eventing" && domain != "unknown" && version != 1 {
			t.Errorf("expectedDomainVersion(%q) = %d", domain, version)
		}
	}
	if importHorizonComponent("unknown") != "" {
		t.Fatal("unknown domain received import horizon")
	}
}

func TestRequireReadyAndInitializationFaultBoundaries(t *testing.T) {
	if err := RequireReady(nil, nil); database.CodeOf(err) != database.CodeUnavailable {
		t.Fatalf("nil readiness catalog error = %v", err)
	}
	logical := &Catalog{entries: []Entry{{ID: "required.store", Required: true}, {ID: "optional.store"}}}
	if err := RequireReady(logical, nil); database.CodeOf(err) != database.CodeUnavailable {
		t.Fatalf("missing required status error = %v", err)
	}
	if err := RequireReady(logical, []database.StoreStatus{{
		ID: "required.store", Readiness: database.StoreMigrationRequired,
	}}); database.CodeOf(err) != database.CodeUnavailable {
		t.Fatalf("unclassified not-ready error = %v", err)
	}
	if err := RequireReady(logical, []database.StoreStatus{{
		ID: "required.store", Readiness: database.StoreIntegrityFailed,
		Error: database.NewError(database.CodeIntegrity, "broken"),
	}}); database.CodeOf(err) != database.CodeIntegrity {
		t.Fatalf("classified readiness error = %v", err)
	}

	if _, err := InitializeRequired(
		t.Context(), nil, nil, func(context.Context, Entry) error { return nil },
	); database.CodeOf(err) != database.CodeInvalid {
		t.Fatalf("nil initialization catalog error = %v", err)
	}
	if _, err := InitializeRequired(t.Context(), logical, nil, nil); database.CodeOf(err) != database.CodeInvalid {
		t.Fatalf("nil initializer error = %v", err)
	}
	if _, err := InitializeRequired(t.Context(), logical, []database.StoreStatus{{
		ID: "other.store", Readiness: database.StoreReady,
	}}, func(context.Context, Entry) error { return nil }); database.CodeOf(err) != database.CodeIntegrity {
		t.Fatalf("missing entry status error = %v", err)
	}

	statuses := []database.StoreStatus{
		{ID: "required.store", Readiness: database.StoreReady},
		{ID: "optional.store", Readiness: database.StoreReady},
	}
	for _, code := range []database.ErrorCode{
		database.CodeUnsupported, database.CodeUnavailable, database.CodeInvalid,
	} {
		initialized, err := InitializeRequired(
			t.Context(), logical, statuses,
			func(context.Context, Entry) error { return database.NewError(code, "fault") },
		)
		if err != nil {
			t.Fatal(err)
		}
		want := code
		if code == database.CodeInvalid {
			want = database.CodeUnavailable
		}
		if initialized[0].Readiness != database.StoreUnavailable || database.CodeOf(initialized[0].Error) != want {
			t.Fatalf("initializer code %q status = %#v", code, initialized[0])
		}
	}
}

func TestLegacyInputInspectionFaultBoundaries(t *testing.T) {
	root := t.TempDir()
	plain := filepath.Join(root, "legacy.json")
	if err := os.WriteFile(plain, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	if found, err := legacyInputExists([]string{filepath.Join(root, "missing"), plain}); err != nil || !found {
		t.Fatalf("regular legacy input = %t, %v", found, err)
	}
	if found, err := legacyInputExists([]string{filepath.Join(root, "only.db-wal")}); err != nil || found {
		t.Fatalf("missing generation input = %t, %v", found, err)
	}

	archiveTree := filepath.Join(root, "tree")
	if err := os.MkdirAll(filepath.Join(archiveTree, "legacy-json"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(archiveTree, "legacy-json", "ignored.json"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if found, err := legacyInputExists([]string{archiveTree}); err != nil || found {
		t.Fatalf("archived legacy tree = %t, %v", found, err)
	}

	symlink := filepath.Join(root, "legacy-link")
	if err := os.Symlink(plain, symlink); err == nil {
		if _, err := legacyInputExists([]string{symlink}); err == nil {
			t.Fatal("legacy symlink accepted")
		}
		if err := os.Remove(symlink); err != nil {
			t.Fatal(err)
		}
		nested := filepath.Join(root, "nested")
		if mkdirErr := os.Mkdir(nested, 0o700); mkdirErr != nil {
			t.Fatal(mkdirErr)
		}
		if linkErr := os.Symlink(plain, filepath.Join(nested, "link")); linkErr != nil {
			t.Fatal(linkErr)
		}
		if _, err := legacyInputExists([]string{nested}); err == nil {
			t.Fatal("nested legacy symlink accepted")
		}
		if err := os.Remove(filepath.Join(nested, "link")); err != nil {
			t.Fatal(err)
		}
	}
	if found, err := legacyInputExists([]string{root}); err != nil || !found {
		t.Fatalf("legacy tree detection = %t, %v", found, err)
	}

	for _, name := range []string{"state.db", "state.db-wal", "state.db-shm", "state.lock", "store"} {
		if !databaseGenerationLike(name) {
			t.Errorf("generation member %q not recognized", name)
		}
	}
	if databaseGenerationLike("legacy.json") {
		t.Fatal("legacy JSON treated as database generation")
	}
}

func TestProbeStatusesClassifiesVersionsAndUnversionedDomains(t *testing.T) {
	home := t.TempDir()
	workspace := filepath.Join(home, "workspace")
	matrix := &config.Channel{
		Enabled: true, Type: config.ChannelMatrix,
		Settings: config.RawNode(
			`{"crypto_database_path":"matrix","crypto_passphrase":"secret"}`,
		),
	}
	whatsapp := &config.Channel{
		Enabled: true, Type: config.ChannelWhatsAppNative,
		Settings: config.RawNode(`{"session_store_path":"whatsapp"}`),
	}
	cfg := &config.Config{
		Agents: config.AgentsConfig{Defaults: config.AgentDefaults{
			Workspace: workspace, ContextManager: "seahorse",
		}},
		Channels: config.ChannelsConfig{"matrix": matrix, "phone": whatsapp},
	}
	t.Cleanup(func() { _ = CloseProbePools(home) })

	statuses, err := ProbeStatuses(t.Context(), home, cfg)
	if err != nil {
		t.Fatal(err)
	}
	for _, domain := range []string{"channel-matrix", "channel-whatsapp"} {
		if statusByDomain(t, cfg, home, statuses, domain).Readiness != database.StoreMigrationRequired {
			t.Fatalf("fresh %s store was not migration-required", domain)
		}
	}

	createCatalogStore(t, filepath.Join(workspace, "matrix", "store.db"), `
		CREATE TABLE crypto_version (version INTEGER);
		CREATE TABLE mx_version (version INTEGER);
		PRAGMA user_version = 1;
	`)
	createCatalogStore(t, filepath.Join(workspace, "whatsapp", "store.db"), `
		CREATE TABLE whatsmeow_version (version INTEGER);
		PRAGMA user_version = 1;
	`)
	createCatalogStore(t, filepath.Join(workspace, "sessions", "seahorse.db"), `
		CREATE TABLE conversations (id TEXT);
		CREATE TABLE messages (id TEXT, model_name TEXT, reasoning_content TEXT);
		CREATE TABLE message_parts (id TEXT);
		CREATE TABLE summaries (id TEXT);
		CREATE TABLE summary_parents (id TEXT);
		CREATE TABLE summary_messages (id TEXT);
		CREATE TABLE context_items (id TEXT);
		CREATE TABLE summaries_fts (id TEXT);
		CREATE TABLE messages_fts (id TEXT);
		PRAGMA user_version = 1;
	`)
	statuses, err = ProbeStatuses(t.Context(), home, cfg)
	if err != nil {
		t.Fatal(err)
	}
	for _, domain := range []string{"channel-matrix", "channel-whatsapp", "seahorse"} {
		if status := statusByDomain(t, cfg, home, statuses, domain); status.Readiness != database.StoreReady {
			t.Fatalf("current %s readiness = %#v", domain, status)
		}
	}

	workflowPath := filepath.Join(workspace, "state", "workflows.db")
	createCatalogStore(t, workflowPath, `PRAGMA user_version = 2;`)
	statuses, err = ProbeStatuses(t.Context(), home, cfg)
	if err != nil {
		t.Fatal(err)
	}
	workflow := statusByID(statuses, "workspace/workflows")
	if workflow.Readiness != database.StoreUnavailable || database.CodeOf(workflow.Error) != database.CodeUnsupported {
		t.Fatalf("too-new workflow status = %#v", workflow)
	}
}

func TestProbeStatusesImportHorizonAndLegacyTransitions(t *testing.T) {
	for name, schema := range map[string]string{
		"missing import schema": `PRAGMA user_version = 1;`,
		"closed import horizon": `
			CREATE TABLE storage_imports (component TEXT, source_id TEXT, archive_status TEXT);
			CREATE TABLE storage_import_issues (component TEXT, source_id TEXT);
			CREATE TABLE storage_import_horizons (component TEXT PRIMARY KEY, completed_at INTEGER NOT NULL);
			CREATE INDEX storage_imports_archive_status_idx ON storage_imports(component, archive_status);
			INSERT INTO storage_import_horizons(component, completed_at) VALUES ('workflows', 1);
			PRAGMA user_version = 1;
		`,
	} {
		t.Run(name, func(t *testing.T) {
			home := t.TempDir()
			workspace := filepath.Join(home, "workspace")
			cfg := &config.Config{Agents: config.AgentsConfig{Defaults: config.AgentDefaults{Workspace: workspace}}}
			createCatalogStore(t, filepath.Join(workspace, "state", "workflows.db"), schema)
			statuses, err := ProbeStatuses(t.Context(), home, cfg)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = CloseProbePools(home) })
			status := statusByID(statuses, "workspace/workflows")
			want := database.StoreMigrationRequired
			if name == "closed import horizon" {
				want = database.StoreReady
			}
			if status.Readiness != want {
				t.Fatalf("workflow readiness = %#v, want %q", status, want)
			}
		})
	}

	home := t.TempDir()
	if err := os.WriteFile(filepath.Join(home, "auth.json"), []byte(`{}`), 0o600); err != nil {
		t.Fatal(err)
	}
	statuses, err := ProbeStatuses(t.Context(), home, &config.Config{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = CloseProbePools(home) })
	auth := statusByID(statuses, "global/auth")
	if auth.Readiness != database.StoreMigrationRequired ||
		database.CodeOf(auth.Error) != database.CodeMigrationRequired {
		t.Fatalf("legacy auth readiness = %#v", auth)
	}
}

func TestProbeStatusesRejectsInvalidCatalogAndImportHorizon(t *testing.T) {
	if statuses, err := ProbeStatuses(t.Context(), " invalid-home ", &config.Config{}); err == nil || statuses != nil {
		t.Fatalf("invalid catalog probe = %#v, %v", statuses, err)
	}

	home := t.TempDir()
	workspace := filepath.Join(home, "workspace")
	cfg := &config.Config{Agents: config.AgentsConfig{Defaults: config.AgentDefaults{Workspace: workspace}}}
	createCatalogStore(t, filepath.Join(workspace, "state", "workflows.db"), `
		CREATE TABLE storage_imports (component TEXT, source_id TEXT, archive_status TEXT);
		CREATE TABLE storage_import_issues (component TEXT, source_id TEXT);
		CREATE TABLE storage_import_horizons (wrong_column TEXT);
		CREATE INDEX storage_imports_archive_status_idx ON storage_imports(component, archive_status);
		PRAGMA user_version = 1;
	`)
	statuses, err := ProbeStatuses(t.Context(), home, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = CloseProbePools(home) })
	status := statusByID(statuses, "workspace/workflows")
	if status.Readiness != database.StoreIntegrityFailed || database.CodeOf(status.Error) != database.CodeIntegrity {
		t.Fatalf("invalid import horizon status = %#v", status)
	}

	if _, err := InitializeRequired(t.Context(), &Catalog{entries: []Entry{{ID: "one"}}}, []database.StoreStatus{
		{ID: "one", Readiness: database.StoreReady},
		{ID: "one", Readiness: database.StoreReady},
	}, func(context.Context, Entry) error { return nil }); err == nil {
		t.Fatal("duplicate readiness statuses initialized")
	}
}

func createCatalogStore(t *testing.T, path, schema string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	pool, err := sqliteprovider.OpenStore(path, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(schema); err != nil {
		_ = pool.Close()
		t.Fatal(err)
	}
	if err := pool.Close(); err != nil {
		t.Fatal(err)
	}
}

func statusByDomain(
	t *testing.T,
	cfg *config.Config,
	home string,
	statuses []database.StoreStatus,
	domain string,
) database.StoreStatus {
	t.Helper()
	logical, err := New(home, cfg)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range logical.Entries() {
		if entry.Domain == domain {
			return statusByID(statuses, entry.ID)
		}
	}
	t.Fatalf("domain %q is absent", domain)
	return database.StoreStatus{}
}

func TestCloseProbePoolsUnknownHomeAndHomeKey(t *testing.T) {
	if err := CloseProbePools(filepath.Join(t.TempDir(), "missing")); err != nil {
		t.Fatal(err)
	}
	clean := filepath.Join(t.TempDir(), "a", "..", "b")
	if got := readinessHomeKey(clean); !strings.HasSuffix(got, string(os.PathSeparator)+"b") {
		t.Fatalf("readiness home key = %q", got)
	}
}

func TestReadinessAdditionalRealTransitions(t *testing.T) {
	t.Run("authority", func(t *testing.T) {
		restore := database.SuspendProviderTestAuthority()
		statuses, err := ProbeStatuses(t.Context(), t.TempDir(), config.DefaultConfig())
		restore()
		if statuses != nil || database.CodeOf(err) != database.CodeUnauthorized {
			t.Fatalf("unfenced readiness probe = %#v, %v", statuses, err)
		}
	})

	t.Run("canceled inspection", func(t *testing.T) {
		home := t.TempDir()
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		statuses, err := ProbeStatuses(ctx, home, config.DefaultConfig())
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = CloseProbePools(home) })
		failed := false
		for _, status := range statuses {
			failed = failed || status.Readiness == database.StoreIntegrityFailed
		}
		if len(statuses) == 0 || !failed {
			t.Fatalf("canceled readiness statuses = %#v", statuses)
		}
	})

	for name, schema := range map[string]string{
		"old version": `CREATE TABLE marker(id INTEGER); PRAGMA user_version = 0;`,
		"open horizon": `
			CREATE TABLE storage_imports (component TEXT, source_id TEXT, archive_status TEXT);
			CREATE TABLE storage_import_issues (component TEXT, source_id TEXT);
			CREATE TABLE storage_import_horizons (component TEXT PRIMARY KEY, completed_at INTEGER NOT NULL);
			CREATE INDEX storage_imports_archive_status_idx ON storage_imports(component, archive_status);
			PRAGMA user_version = 1;
		`,
	} {
		t.Run(name, func(t *testing.T) {
			home := t.TempDir()
			workspace := filepath.Join(home, "workspace")
			cfg := config.DefaultConfig()
			cfg.Agents.Defaults.Workspace = workspace
			createCatalogStore(t, filepath.Join(workspace, "state", "workflows.db"), schema)
			statuses, err := ProbeStatuses(t.Context(), home, cfg)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = CloseProbePools(home) })
			status := statusByID(statuses, "workspace/workflows")
			if status.Readiness != database.StoreMigrationRequired {
				t.Fatalf("workflow readiness = %#v", status)
			}
		})
	}
}

func TestInitializeRequiredMapsRemainingReadinessCodes(t *testing.T) {
	logical := &Catalog{entries: []Entry{{ID: "required.store", Required: true}}}
	ready := []database.StoreStatus{{ID: "required.store", Readiness: database.StoreReady}}
	if err := RequireReady(logical, ready); err != nil {
		t.Fatal(err)
	}

	for _, code := range []database.ErrorCode{database.CodeMigrationRequired, database.CodeIntegrity} {
		initialized, err := InitializeRequired(
			t.Context(), logical, ready,
			func(context.Context, Entry) error { return database.NewError(code, "fault") },
		)
		if err != nil {
			t.Fatal(err)
		}
		if database.CodeOf(initialized[0].Error) != code {
			t.Fatalf("initializer code %q status = %#v", code, initialized[0])
		}
	}

	notReady := []database.StoreStatus{{
		ID: "required.store", Readiness: database.StoreMigrationRequired,
		Error: database.NewError(database.CodeMigrationRequired, "pending"),
	}}
	called := false
	initialized, err := InitializeRequired(t.Context(), logical, notReady, func(context.Context, Entry) error {
		called = true
		return nil
	})
	if err != nil || called || initialized[0].Readiness != database.StoreMigrationRequired {
		t.Fatalf("non-ready initialization = %#v, called=%t, err=%v", initialized, called, err)
	}
}
