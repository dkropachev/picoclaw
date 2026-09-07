package storecatalog

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/database"
)

func TestCatalogSnapshotsAndChannelIdentities(t *testing.T) {
	if _, ok := (*Catalog)(nil).Lookup("global/auth"); ok {
		t.Fatal("nil catalog lookup succeeded")
	}
	if got := (*Catalog)(nil).All(); got != nil {
		t.Fatalf("nil catalog snapshot = %#v", got)
	}

	home := t.TempDir()
	catalog, err := Project(catalogTestOptions(t, home, &config.Config{}))
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := catalog.Lookup("missing"); ok {
		t.Fatal("unknown catalog entry resolved")
	}
	if catalog.Home() != home || (*Catalog)(nil).Home() != "" {
		t.Fatalf("catalog homes = %q, %q", catalog.Home(), (*Catalog)(nil).Home())
	}
	snapshot := catalog.All()
	if len(snapshot) == 0 {
		t.Fatal("catalog snapshot is empty")
	}
	snapshot[0].LegacyRoots = append(snapshot[0].LegacyRoots, "mutated")
	again := catalog.All()
	if len(again[0].LegacyRoots) == len(snapshot[0].LegacyRoots) {
		t.Fatal("catalog snapshot aliases retained legacy roots")
	}

	for _, test := range []struct {
		channelType string
		name        string
		prefix      string
		ok          bool
	}{
		{config.ChannelMatrix, " Team / Main ", "channel/matrix/team---main-", true},
		{config.ChannelWhatsAppNative, "...", "channel/whatsapp/unnamed-", true},
		{"telegram", "main", "", false},
	} {
		id, ok := ChannelStoreID(test.channelType, test.name)
		if ok != test.ok || (ok && !strings.HasPrefix(id.String(), test.prefix)) {
			t.Fatalf("ChannelStoreID(%q, %q) = (%q, %t)", test.channelType, test.name, id, ok)
		}
	}
	long := logicalComponent(strings.Repeat("a", 100))
	if len(strings.Split(long, "-")[0]) > 72 || !strings.Contains(long, "-") {
		t.Fatalf("bounded logical component = %q", long)
	}
}

func TestCatalogPathResolutionBoundaries(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	workspace := filepath.Join(home, "workspace")

	for name, path := range map[string]string{
		"blank":      " ",
		"nul":        "bad\x00path",
		"surrounded": " relative ",
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := projectedStorePath(path); err == nil {
				t.Fatalf("projectedStorePath(%q) succeeded", path)
			}
		})
	}

	resolved, err := eventDatabasePath(workspace, "~/events.db", home, projectedStorePath)
	if err != nil || resolved != filepath.Join(home, "events.db") {
		t.Fatalf("home event database = %q, %v", resolved, err)
	}
	resolved, err = eventDatabasePath(workspace, "relative/events.db", home, projectedStorePath)
	if err != nil || resolved != filepath.Join(workspace, "relative", "events.db") {
		t.Fatalf("relative event database = %q, %v", resolved, err)
	}
	if _, err := eventDatabasePath(workspace, "bad\x00path", home, projectedStorePath); err == nil {
		t.Fatal("event database accepted NUL")
	}
	for _, configured := range []string{"~", "~/nested", "relative", ""} {
		if _, err := resolveWorkspaceWith(home, configured, home, projectedStorePath); err != nil {
			t.Fatalf("resolve workspace %q: %v", configured, err)
		}
	}

	notDirectory := filepath.Join(home, "not-directory")
	if err := os.WriteFile(notDirectory, []byte("file"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := canonicalDirectoryPath(notDirectory); err == nil {
		t.Fatal("regular file accepted as catalog directory")
	}
	storeDirectory := filepath.Join(home, "store.db")
	if err := os.Mkdir(storeDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := canonicalStorePath(storeDirectory); err == nil {
		t.Fatal("directory accepted as database generation")
	}
	store := filepath.Join(home, "sidecar.db")
	if err := os.Mkdir(store+"-wal", 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := canonicalStorePath(store); err == nil {
		t.Fatal("directory accepted as database sidecar")
	}
}

func TestBuildDynamicChannelFaultsAndRelativeRoots(t *testing.T) {
	home := t.TempDir()
	workspace := filepath.Join(home, "workspace")
	matrix := &config.Channel{
		Enabled: true, Type: config.ChannelMatrix,
		Settings: config.RawNode(`{"crypto_database_path":"matrix"}`),
	}
	whatsapp := &config.Channel{
		Enabled: true, Type: config.ChannelWhatsAppNative,
		Settings: config.RawNode(`{"session_store_path":"whatsapp"}`),
	}
	cfg := &config.Config{
		Agents: config.AgentsConfig{
			Defaults: config.AgentDefaults{Workspace: "workspace"},
			List:     []config.AgentConfig{{ID: "same", Workspace: workspace}},
		},
		GitWorkspaces: config.GitWorkspacesConfig{RootDir: "git-root"},
		Channels:      config.ChannelsConfig{"matrix": matrix, "whatsapp": whatsapp, "off": nil},
	}
	catalog, err := Project(catalogTestOptions(t, home, cfg))
	if err != nil {
		t.Fatal(err)
	}
	matrixID, _ := ChannelStoreID(config.ChannelMatrix, "matrix")
	whatsappID, _ := ChannelStoreID(config.ChannelWhatsAppNative, "whatsapp")
	for _, id := range []database.StoreID{matrixID, whatsappID} {
		if _, ok := catalog.Lookup(id); !ok {
			t.Fatalf("dynamic channel store %q missing", id)
		}
	}

	badDecode := &config.Channel{Enabled: true, Type: config.ChannelMatrix, Settings: config.RawNode(`{`)}
	if _, err := Project(catalogTestOptions(t, home, &config.Config{
		Agents:   config.AgentsConfig{Defaults: config.AgentDefaults{Workspace: workspace}},
		Channels: config.ChannelsConfig{"bad": badDecode},
	})); err == nil || !strings.Contains(err.Error(), "matrix channel") {
		t.Fatalf("matrix decode error = %v", err)
	}
	wrongMatrix := &config.Channel{Enabled: true, Type: config.ChannelMatrix, Settings: config.RawNode(`{}`)}
	if err := wrongMatrix.Decode(&struct{}{}); err != nil {
		t.Fatal(err)
	}
	if _, err := Project(catalogTestOptions(t, home, &config.Config{
		Agents:   config.AgentsConfig{Defaults: config.AgentDefaults{Workspace: workspace}},
		Channels: config.ChannelsConfig{"wrong": wrongMatrix},
	})); err == nil || !strings.Contains(err.Error(), "invalid settings") {
		t.Fatalf("matrix settings error = %v", err)
	}
	wrongWhatsApp := &config.Channel{Enabled: true, Type: config.ChannelWhatsAppNative, Settings: config.RawNode(`{}`)}
	if err := wrongWhatsApp.Decode(&struct{}{}); err != nil {
		t.Fatal(err)
	}
	if _, err := Project(catalogTestOptions(t, home, &config.Config{
		Agents:   config.AgentsConfig{Defaults: config.AgentDefaults{Workspace: workspace}},
		Channels: config.ChannelsConfig{"wrong": wrongWhatsApp},
	})); err == nil || !strings.Contains(err.Error(), "invalid settings") {
		t.Fatalf("WhatsApp settings error = %v", err)
	}
}

func TestCatalogBuildRejectsInvalidConfiguredRoots(t *testing.T) {
	home := t.TempDir()
	workspace := filepath.Join(home, "workspace")
	for name, cfg := range map[string]*config.Config{
		"workspace": {
			Agents: config.AgentsConfig{Defaults: config.AgentDefaults{Workspace: "bad\x00workspace"}},
		},
		"git root": {
			Agents:        config.AgentsConfig{Defaults: config.AgentDefaults{Workspace: workspace}},
			GitWorkspaces: config.GitWorkspacesConfig{RootDir: "bad\x00git"},
		},
		"agent workspace": {
			Agents: config.AgentsConfig{
				Defaults: config.AgentDefaults{Workspace: workspace},
				List:     []config.AgentConfig{{ID: "bad", Workspace: "bad\x00agent"}},
			},
		},
		"event store": {
			Agents: config.AgentsConfig{Defaults: config.AgentDefaults{Workspace: workspace}},
			Events: config.EventsConfig{Ingress: config.EventIngressConfig{DatabasePath: "bad\x00event"}},
		},
		"evolution store": {
			Agents:    config.AgentsConfig{Defaults: config.AgentDefaults{Workspace: workspace}},
			Evolution: config.EvolutionConfig{StateDir: "bad\x00evolution"},
		},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := Project(catalogTestOptions(t, home, cfg)); err == nil {
				t.Fatal("invalid configured root was accepted")
			}
		})
	}
	for _, channelType := range []string{config.ChannelMatrix, config.ChannelWhatsAppNative} {
		channel := &config.Channel{Enabled: true, Type: channelType}
		var settings any
		if channelType == config.ChannelMatrix {
			settings = &config.MatrixSettings{CryptoDatabasePath: "bad\x00matrix"}
		} else {
			settings = &config.WhatsAppSettings{SessionStorePath: "bad\x00whatsapp"}
		}
		if err := channel.Decode(settings); err != nil {
			t.Fatal(err)
		}
		if _, err := Project(catalogTestOptions(t, home, &config.Config{
			Agents:   config.AgentsConfig{Defaults: config.AgentDefaults{Workspace: workspace}},
			Channels: config.ChannelsConfig{"bad": channel},
		})); err == nil {
			t.Fatalf("%s NUL store path was accepted", channelType)
		}
	}
	if _, err := Project(Options{Home: " bad-home ", Config: &config.Config{}}); err == nil {
		t.Fatal("invalid catalog home was accepted")
	}
	if _, err := Build(catalogTestOptions(t, home, &config.Config{
		Agents:        config.AgentsConfig{Defaults: config.AgentDefaults{Workspace: workspace}},
		GitWorkspaces: config.GitWorkspacesConfig{RootDir: "relative-git"},
		Evolution:     config.EvolutionConfig{StateDir: "relative-evolution"},
	})); err != nil {
		t.Fatalf("relative physical roots: %v", err)
	}
}

func TestCatalogCanonicalSymlinkAndLegacyBoundaries(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "target")
	if err := os.Mkdir(target, 0o700); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(root, "alias")
	if err := os.Symlink(target, alias); err == nil {
		if _, _, err := canonicalPath(alias); err == nil {
			t.Fatal("symlink leaf accepted")
		}
		if _, _, err := canonicalPath(filepath.Join(alias, "missing.db")); err == nil {
			t.Fatal("symlink ancestor accepted")
		}
	}
	legacyDir := filepath.Join(root, "legacy")
	if err := os.Mkdir(legacyDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if got, err := canonicalLegacyPath(legacyDir); err != nil || got != legacyDir {
		t.Fatalf("legacy directory = %q, %v", got, err)
	}
	if got, err := canonicalLegacyPath(filepath.Join(root, "missing")); err != nil || got == "" {
		t.Fatalf("missing legacy projection = %q, %v", got, err)
	}

	got := launcherConfigLegacyPath(filepath.Join(root, "config.json"))
	if filepath.Base(got) != "launcher-config.json" || !filepath.IsAbs(got) {
		t.Fatalf("launcher legacy path = %q", got)
	}
	for _, invalid := range []string{" ", "bad\x00path"} {
		if _, _, err := canonicalPath(invalid); err == nil {
			t.Fatalf("canonicalPath(%q) succeeded", invalid)
		}
	}
	if _, err := canonicalStorePath("bad\x00store"); err == nil {
		t.Fatal("canonical store accepted NUL")
	}
	if _, err := canonicalLegacyPath("bad\x00legacy"); err == nil {
		t.Fatal("canonical legacy input accepted NUL")
	}
}

func TestValidateSpecsIdentityAndPhysicalFaults(t *testing.T) {
	root := t.TempDir()
	for name, specs := range map[string][]Spec{
		"invalid ID": {{ID: "Bad", Path: filepath.Join(root, "a.db")}},
		"duplicate ID": {
			{ID: "same", Path: filepath.Join(root, "a.db")},
			{ID: "same", Path: filepath.Join(root, "b.db")},
		},
		"same main": {
			{ID: "first", Path: filepath.Join(root, "same.db")},
			{ID: "second", Path: filepath.Join(root, "same.db")},
		},
	} {
		t.Run(name, func(t *testing.T) {
			if err := validateSpecs(specs); err == nil {
				t.Fatal("invalid catalog specs accepted")
			}
		})
	}
	nonregular := filepath.Join(root, "nonregular.db")
	if err := os.Mkdir(nonregular, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := validateSpecs([]Spec{{ID: "nonregular", Path: nonregular}}); err == nil {
		t.Fatal("nonregular generation member accepted")
	}
}
