package storecatalog

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/database"
)

func decodedTestChannel(t *testing.T, enabled bool, channelType string, settings any) *config.Channel {
	t.Helper()
	channel := &config.Channel{Enabled: enabled, Type: channelType}
	if err := channel.Decode(settings); err != nil {
		t.Fatalf("decode test channel: %v", err)
	}
	return channel
}

func TestCatalogOptionsResolveWithoutAmbientPathState(t *testing.T) {
	home := t.TempDir()
	userHome := t.TempDir()
	firstCWD := t.TempDir()
	secondCWD := t.TempDir()
	workspace := filepath.Join(userHome, "workspace")
	cfg := &config.Config{
		Agents:        config.AgentsConfig{Defaults: config.AgentDefaults{Workspace: "~/workspace"}},
		GitWorkspaces: config.GitWorkspacesConfig{RootDir: "~/git-workspaces"},
		Events: config.EventsConfig{Ingress: config.EventIngressConfig{
			DatabasePath: "~/event-state/events.db",
		}},
		Evolution: config.EvolutionConfig{StateDir: "~/evolution-state"},
	}
	options := catalogTestOptions(t, home, cfg)
	options.ConfigPath = filepath.Join("settings", "config.json")
	options.UserHome = userHome

	t.Setenv("HOME", filepath.Join(t.TempDir(), "ambient-home-one"))
	t.Setenv(config.EnvConfig, filepath.Join(t.TempDir(), "ambient-config-one.json"))
	t.Chdir(firstCWD)
	first, err := Project(options)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", filepath.Join(t.TempDir(), "ambient-home-two"))
	t.Setenv(config.EnvConfig, filepath.Join(t.TempDir(), "ambient-config-two.json"))
	t.Chdir(secondCWD)
	second, err := Project(options)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(first.All(), second.All()) {
		t.Fatal("catalog changed with ambient home, config, or working directory")
	}
	launcher, ok := first.Lookup("launcher/auth")
	if !ok || len(launcher.LegacyRoots) != 1 || launcher.LegacyRoots[0] !=
		filepath.Join(home, "settings", "launcher-config.json") {
		t.Fatalf("launcher config projection = %#v", launcher)
	}
	for id, want := range map[database.StoreID]string{
		"workspace/eventing":  filepath.Join(userHome, "event-state", "events.db"),
		"workspace/evolution": filepath.Join(userHome, "evolution-state", "evolution.db"),
		"workspace/workflows": filepath.Join(workspace, "state", "workflows.db"),
	} {
		spec, found := first.Lookup(id)
		if !found || spec.Path != want {
			t.Errorf("%s path = %q, want %q", id, spec.Path, want)
		}
	}
	gitStore, ok := first.Lookup("global/git-workspace-inventory")
	if !ok || gitStore.Path != filepath.Join(userHome, "git-workspaces", "inventory.db") {
		t.Fatalf("Git workspace store = %#v", gitStore)
	}
}

func TestCatalogOptionsRejectImplicitOrInvalidPathContext(t *testing.T) {
	home := t.TempDir()
	base := catalogTestOptions(t, home, &config.Config{})
	uncleanHome := home + string(os.PathSeparator) + "."
	tests := []struct {
		name    string
		options Options
	}{
		{name: "nil config", options: Options{Home: home}},
		{name: "relative home", options: Options{Home: "relative", Config: &config.Config{}}},
		{name: "unclean home", options: Options{Home: uncleanHome, Config: &config.Config{}}},
		{name: "missing home", options: Options{Home: filepath.Join(home, "missing"), Config: &config.Config{}}},
		{name: "relative user home", options: Options{Home: home, Config: &config.Config{}, UserHome: "relative"}},
		{name: "padded user home", options: Options{Home: home, Config: &config.Config{}, UserHome: " " + home}},
		{name: "padded config", options: Options{Home: home, Config: &config.Config{}, ConfigPath: " config.json "}},
		{name: "tilde config without user home", options: Options{
			Home: home, Config: &config.Config{}, ConfigPath: "~/config.json",
		}},
		{name: "named-user expansion", options: Options{
			Home: home, Config: &config.Config{}, ConfigPath: "~other/config.json",
		}},
		{name: "tilde workspace without user home", options: Options{
			Home: home,
			Config: &config.Config{Agents: config.AgentsConfig{
				Defaults: config.AgentDefaults{Workspace: "~/workspace"},
			}},
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := Project(test.options); err == nil {
				t.Fatal("invalid catalog options succeeded")
			}
		})
	}

	for name, mutate := range map[string]func(*config.Config){
		"git root":       func(cfg *config.Config) { cfg.GitWorkspaces.RootDir = "~/git" },
		"event path":     func(cfg *config.Config) { cfg.Events.Ingress.DatabasePath = "~/events.db" },
		"evolution path": func(cfg *config.Config) { cfg.Evolution.StateDir = "~/evolution" },
	} {
		t.Run(name, func(t *testing.T) {
			cfg := &config.Config{}
			mutate(cfg)
			options := base
			options.Config = cfg
			if _, err := Project(options); err == nil {
				t.Fatal("implicit user home succeeded")
			}
		})
	}
}

func TestCatalogRequiredChannelsMatchRuntimeActivation(t *testing.T) {
	home := t.TempDir()
	workspace := filepath.Join(home, "workspace")
	activeMatrix := &config.MatrixSettings{
		Homeserver: "https://matrix.example", UserID: "@bot:example",
		AccessToken: *config.NewSecureString("matrix-token"), CryptoPassphrase: "passphrase",
		CryptoDatabasePath: "matrix-active",
	}
	inactiveMatrix := &config.MatrixSettings{
		Homeserver: "https://matrix.example", UserID: "@bot:example",
		CryptoPassphrase: "passphrase", CryptoDatabasePath: "matrix-inactive",
	}
	cfg := &config.Config{
		Agents: config.AgentsConfig{Defaults: config.AgentDefaults{Workspace: workspace}},
		Channels: config.ChannelsConfig{
			"nil":      nil,
			"disabled": decodedTestChannel(t, false, config.ChannelWeCom, &config.WeComSettings{}),
			"other":    decodedTestChannel(t, true, config.ChannelTelegram, &config.TelegramSettings{}),
			"wecom": decodedTestChannel(t, true, config.ChannelWeCom, &config.WeComSettings{
				BotID: "bot", Secret: *config.NewSecureString("secret"),
			}),
			"weixin": decodedTestChannel(t, true, config.ChannelWeixin, &config.WeixinSettings{
				Token: *config.NewSecureString("token"),
			}),
			"matrix-active":   decodedTestChannel(t, true, config.ChannelMatrix, activeMatrix),
			"matrix-inactive": decodedTestChannel(t, true, config.ChannelMatrix, inactiveMatrix),
			"whatsapp-active": decodedTestChannel(t, true, config.ChannelWhatsAppNative, &config.WhatsAppSettings{
				UseNative: true, SessionStorePath: "whatsapp-active",
			}),
			"whatsapp-inactive": decodedTestChannel(t, true, config.ChannelWhatsAppNative, &config.WhatsAppSettings{
				SessionStorePath: "whatsapp-inactive",
			}),
		},
	}
	catalog, err := Project(catalogTestOptions(t, home, cfg))
	if err != nil {
		t.Fatal(err)
	}
	for id, required := range map[database.StoreID]bool{
		"channel/wecom":  true,
		"channel/weixin": true,
		mustChannelID(t, config.ChannelMatrix, "matrix-active"):             true,
		mustChannelID(t, config.ChannelMatrix, "matrix-inactive"):           false,
		mustChannelID(t, config.ChannelWhatsAppNative, "whatsapp-active"):   true,
		mustChannelID(t, config.ChannelWhatsAppNative, "whatsapp-inactive"): false,
	} {
		spec, ok := catalog.Lookup(id)
		if !ok || spec.Required != required {
			t.Errorf("%s = %#v, want required=%t", id, spec, required)
		}
	}
}

func mustChannelID(t *testing.T, channelType, name string) database.StoreID {
	t.Helper()
	id, ok := ChannelStoreID(channelType, name)
	if !ok {
		t.Fatalf("channel ID for %s/%s is invalid", channelType, name)
	}
	return id
}

func TestChannelIdentitiesPreserveExactNamesAndDoNotMutateConfig(t *testing.T) {
	upper, _ := ChannelStoreID(config.ChannelMatrix, "Foo")
	lower, _ := ChannelStoreID(config.ChannelMatrix, "foo")
	padded, _ := ChannelStoreID(config.ChannelMatrix, " Foo ")
	if upper == lower || upper == padded || lower == padded {
		t.Fatalf("exact channel names collided: %q, %q, %q", upper, lower, padded)
	}
	if suffix := upper.String()[strings.LastIndexByte(upper.String(), '-')+1:]; len(suffix) != 16 {
		t.Fatalf("channel digest length = %d, want 16", len(suffix))
	}

	home := t.TempDir()
	channel := &config.Channel{
		Enabled: true,
		Type:    config.ChannelMatrix,
		Settings: config.RawNode(`{
			"homeserver":"https://matrix.example",
			"user_id":"@bot:example",
			"access_token":"token",
			"crypto_database_path":"matrix"
		}`),
	}
	cfg := &config.Config{Channels: config.ChannelsConfig{"matrix": channel}}
	if _, err := Project(catalogTestOptions(t, home, cfg)); err != nil {
		t.Fatal(err)
	}
	channel.Settings = config.RawNode(`{`)
	if _, err := Project(catalogTestOptions(t, home, cfg)); err == nil {
		t.Fatal("catalog cached decoded settings into caller config")
	}
}

func TestCanonicalPathAndLegacyCollisionBoundaries(t *testing.T) {
	root := t.TempDir()
	blocker := filepath.Join(root, "blocker")
	if err := os.WriteFile(blocker, []byte("file"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := canonicalPath(filepath.Join(blocker, "child.db")); err == nil {
		t.Fatalf("non-directory ancestor error = %v", err)
	}

	legacyDirectory := filepath.Join(root, "legacy")
	if err := os.Mkdir(legacyDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	contained := filepath.Join(legacyDirectory, "store.db")
	if err := validateSpecs([]Spec{{
		ID: "workspace/test", Path: contained, LegacyRoots: []string{legacyDirectory},
	}}); err != nil {
		t.Fatalf("intentional legacy containment rejected: %v", err)
	}
	if err := validateSpecs([]Spec{{
		ID: "workspace/test", Path: contained, LegacyRoots: []string{contained},
	}}); err == nil || !strings.Contains(err.Error(), "legacy input") {
		t.Fatalf("exact legacy collision error = %v", err)
	}
	sharedLegacy := filepath.Join(root, "shared.json")
	if err := validateSpecs([]Spec{
		{ID: "workspace/first", Path: filepath.Join(root, "first.db"), LegacyRoots: []string{sharedLegacy}},
		{ID: "workspace/second", Path: filepath.Join(root, "second.db"), LegacyRoots: []string{sharedLegacy}},
	}); err == nil || !strings.Contains(err.Error(), "one legacy input") {
		t.Fatalf("duplicate legacy identity error = %v", err)
	}
	firstLegacy := filepath.Join(root, "first.json")
	secondLegacy := filepath.Join(root, "second.json")
	if err := os.WriteFile(firstLegacy, []byte("legacy"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(firstLegacy, secondLegacy); err == nil {
		if err := validateSpecs([]Spec{
			{ID: "workspace/first", Path: filepath.Join(root, "third.db"), LegacyRoots: []string{firstLegacy}},
			{ID: "workspace/second", Path: filepath.Join(root, "fourth.db"), LegacyRoots: []string{secondLegacy}},
		}); err == nil || !strings.Contains(err.Error(), "legacy inputs resolve") {
			t.Fatalf("legacy hard-link alias error = %v", err)
		}
	}

	generation := filepath.Join(root, "generation.db")
	legacy := filepath.Join(root, "legacy.json")
	if err := os.WriteFile(generation, []byte("generation"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(generation, legacy); err != nil {
		t.Skipf("hardlinks unavailable: %v", err)
	}
	if err := validateSpecs([]Spec{{
		ID: "workspace/test", Path: generation, LegacyRoots: []string{legacy},
	}}); err == nil || !strings.Contains(err.Error(), "physical file") {
		t.Fatalf("legacy hardlink collision error = %v", err)
	}
}

func TestCanonicalPathRejectsResolvedSymlinkAncestor(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "target")
	child := filepath.Join(target, "child")
	if err := os.MkdirAll(child, 0o700); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(root, "alias")
	if err := os.Symlink(target, alias); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if _, _, err := canonicalPath(filepath.Join(alias, "child", "missing.db")); err == nil ||
		!strings.Contains(err.Error(), "symlinked ancestor") {
		t.Fatalf("resolved symlink ancestor error = %v", err)
	}
}

func TestBuildPropagatesRealCatalogBoundaryFailures(t *testing.T) {
	t.Run("Git workspace needs explicit user home", func(t *testing.T) {
		home := t.TempDir()
		cfg := &config.Config{GitWorkspaces: config.GitWorkspacesConfig{RootDir: "~/git"}}
		if _, err := Build(catalogTestOptions(t, home, cfg)); err == nil {
			t.Fatal("implicit Git workspace user home succeeded")
		}
	})

	t.Run("Git workspace alias", func(t *testing.T) {
		home := t.TempDir()
		target := filepath.Join(home, "git-target")
		if err := os.Mkdir(target, 0o700); err != nil {
			t.Fatal(err)
		}
		alias := filepath.Join(home, "git-alias")
		if err := os.Symlink(target, alias); err != nil {
			t.Skipf("symlinks unavailable: %v", err)
		}
		cfg := &config.Config{GitWorkspaces: config.GitWorkspacesConfig{RootDir: alias}}
		if _, err := Build(catalogTestOptions(t, home, cfg)); err == nil ||
			!strings.Contains(err.Error(), "git workspace root") {
			t.Fatalf("Git workspace alias error = %v", err)
		}
	})

	t.Run("fixed generation is directory", func(t *testing.T) {
		home := t.TempDir()
		if err := os.Mkdir(filepath.Join(home, "auth.db"), 0o700); err != nil {
			t.Fatal(err)
		}
		if _, err := Build(catalogTestOptions(t, home, &config.Config{})); err == nil ||
			!strings.Contains(err.Error(), "global/auth") {
			t.Fatalf("fixed generation error = %v", err)
		}
	})

	t.Run("legacy input alias", func(t *testing.T) {
		home := t.TempDir()
		target := filepath.Join(home, "outside.json")
		if err := os.WriteFile(target, []byte(`{}`), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(target, filepath.Join(home, "auth.json")); err != nil {
			t.Skipf("symlinks unavailable: %v", err)
		}
		if _, err := Build(catalogTestOptions(t, home, &config.Config{})); err == nil ||
			!strings.Contains(err.Error(), "legacy input") {
			t.Fatalf("legacy alias error = %v", err)
		}
	})

	for _, channelType := range []string{config.ChannelMatrix, config.ChannelWhatsAppNative} {
		t.Run(channelType+" nonregular store", func(t *testing.T) {
			home := t.TempDir()
			root := filepath.Join(home, channelType)
			if err := os.MkdirAll(filepath.Join(root, databaseFilename), 0o700); err != nil {
				t.Fatal(err)
			}
			channel := &config.Channel{Enabled: true, Type: channelType}
			if channelType == config.ChannelMatrix {
				if err := channel.Decode(&config.MatrixSettings{CryptoDatabasePath: root}); err != nil {
					t.Fatal(err)
				}
			} else if err := channel.Decode(&config.WhatsAppSettings{
				UseNative: true, SessionStorePath: root,
			}); err != nil {
				t.Fatal(err)
			}
			cfg := &config.Config{Channels: config.ChannelsConfig{"target": channel}}
			if _, err := Build(catalogTestOptions(t, home, cfg)); err == nil {
				t.Fatal("nonregular channel store succeeded")
			}
		})
	}
}

func TestProjectPropagatesConfiguredChannelAndWorkspaceFailures(t *testing.T) {
	home := t.TempDir()
	workspace := filepath.Join(home, "workspace")
	tests := []struct {
		name string
		cfg  *config.Config
	}{
		{
			name: "blank agent is ignored",
			cfg: &config.Config{Agents: config.AgentsConfig{
				Defaults: config.AgentDefaults{Workspace: workspace},
				List:     []config.AgentConfig{{ID: "blank"}},
			}},
		},
		{
			name: "agent needs user home",
			cfg: &config.Config{Agents: config.AgentsConfig{
				Defaults: config.AgentDefaults{Workspace: workspace},
				List:     []config.AgentConfig{{ID: "agent", Workspace: "~/agent"}},
			}},
		},
		{
			name: "matrix malformed",
			cfg: &config.Config{
				Agents: config.AgentsConfig{Defaults: config.AgentDefaults{Workspace: workspace}},
				Channels: config.ChannelsConfig{"matrix": {
					Enabled: true, Type: config.ChannelMatrix, Settings: config.RawNode(`{`),
				}},
			},
		},
		{
			name: "matrix needs user home",
			cfg: &config.Config{
				Agents: config.AgentsConfig{Defaults: config.AgentDefaults{Workspace: workspace}},
				Channels: config.ChannelsConfig{
					"matrix": decodedTestChannel(
						t, true, config.ChannelMatrix, &config.MatrixSettings{CryptoDatabasePath: "~/matrix"},
					),
				},
			},
		},
		{
			name: "whatsapp malformed",
			cfg: &config.Config{
				Agents: config.AgentsConfig{Defaults: config.AgentDefaults{Workspace: workspace}},
				Channels: config.ChannelsConfig{"whatsapp": {
					Enabled: true, Type: config.ChannelWhatsAppNative, Settings: config.RawNode(`{`),
				}},
			},
		},
		{
			name: "whatsapp needs user home",
			cfg: &config.Config{
				Agents: config.AgentsConfig{Defaults: config.AgentDefaults{Workspace: workspace}},
				Channels: config.ChannelsConfig{
					"whatsapp": decodedTestChannel(
						t, true, config.ChannelWhatsAppNative,
						&config.WhatsAppSettings{SessionStorePath: "~/whatsapp"},
					),
				},
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := Project(catalogTestOptions(t, home, test.cfg))
			if test.name == "blank agent is ignored" {
				if err != nil {
					t.Fatalf("blank agent: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatal("invalid configured path succeeded")
			}
		})
	}
}

func TestFixedChannelActivationRejectsInvalidSettingsDeterministically(t *testing.T) {
	home := t.TempDir()
	for _, channelType := range []string{config.ChannelWeCom, config.ChannelWeixin} {
		t.Run(channelType+" decode", func(t *testing.T) {
			cfg := &config.Config{Channels: config.ChannelsConfig{
				"z-invalid": {Enabled: true, Type: channelType, Settings: config.RawNode(`{`)},
				"a-off":     {Enabled: false, Type: channelType},
			}}
			if _, err := Project(catalogTestOptions(t, home, cfg)); err == nil {
				t.Fatal("malformed fixed channel succeeded")
			}
		})
	}

	wrong := decodedTestChannel(t, true, config.ChannelWeCom, &struct{}{})
	if _, err := Project(catalogTestOptions(t, home, &config.Config{
		Channels: config.ChannelsConfig{"wecom": wrong},
	})); err == nil || !strings.Contains(err.Error(), "do not match") {
		t.Fatalf("wrong fixed channel settings error = %v", err)
	}
	for name, channel := range map[string]*config.Channel{
		"wecom typed as weixin": decodedTestChannel(
			t, true, config.ChannelWeCom, &config.WeixinSettings{},
		),
		"weixin typed as wecom": decodedTestChannel(
			t, true, config.ChannelWeixin, &config.WeComSettings{},
		),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := Project(catalogTestOptions(t, home, &config.Config{
				Channels: config.ChannelsConfig{"mismatch": channel},
			})); err == nil || !strings.Contains(err.Error(), "do not match") {
				t.Fatalf("typed mismatch error = %v", err)
			}
		})
	}

	active := decodedTestChannel(t, true, config.ChannelWeCom, &config.WeComSettings{
		BotID: "bot", Secret: *config.NewSecureString("secret"),
	})
	if _, err := Project(catalogTestOptions(t, home, &config.Config{
		Channels: config.ChannelsConfig{
			"a-active":  active,
			"z-invalid": wrong,
		},
	})); err == nil {
		t.Fatal("active channel hid a later invalid channel")
	}
}

func TestInferredChannelTypeBuildsWithoutMutatingCaller(t *testing.T) {
	home := t.TempDir()
	channel := &config.Channel{
		Enabled: true,
		Settings: config.RawNode(`{
			"homeserver":"https://matrix.example",
			"user_id":"@bot:example",
			"access_token":"token",
			"crypto_database_path":"matrix"
		}`),
	}
	cfg := &config.Config{Channels: config.ChannelsConfig{"matrix": channel}}
	catalog, err := Project(catalogTestOptions(t, home, cfg))
	if err != nil {
		t.Fatal(err)
	}
	if channel.Type != "" {
		t.Fatalf("caller channel type mutated to %q", channel.Type)
	}
	if _, ok := catalog.Lookup(mustChannelID(t, config.ChannelMatrix, "matrix")); !ok {
		t.Fatal("inferred Matrix store missing")
	}
}

func TestConfiguredPathsRejectPadding(t *testing.T) {
	home := t.TempDir()
	for name, cfg := range map[string]*config.Config{
		"workspace": {
			Agents: config.AgentsConfig{Defaults: config.AgentDefaults{Workspace: " workspace "}},
		},
		"git": {GitWorkspaces: config.GitWorkspacesConfig{RootDir: " git "}},
		"event": {Events: config.EventsConfig{Ingress: config.EventIngressConfig{
			DatabasePath: " events.db ",
		}}},
		"evolution": {Evolution: config.EvolutionConfig{StateDir: " evolution "}},
		"agent": {Agents: config.AgentsConfig{List: []config.AgentConfig{{
			ID: "agent", Workspace: " agent ",
		}}}},
		"matrix": {Channels: config.ChannelsConfig{"matrix": decodedTestChannel(
			t, true, config.ChannelMatrix, &config.MatrixSettings{CryptoDatabasePath: " matrix "},
		)}},
		"whatsapp": {Channels: config.ChannelsConfig{"whatsapp": decodedTestChannel(
			t, true, config.ChannelWhatsAppNative, &config.WhatsAppSettings{SessionStorePath: " whatsapp "},
		)}},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := Project(catalogTestOptions(t, home, cfg)); err == nil {
				t.Fatal("padded configured path succeeded")
			}
		})
	}
}

func TestCanonicalStoreAndLegacyRejectUnsafeMembers(t *testing.T) {
	root := t.TempDir()
	store := filepath.Join(root, "store.db")
	target := filepath.Join(root, "sidecar-target")
	if err := os.WriteFile(target, []byte("sidecar"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, store+"-wal"); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if _, err := canonicalStorePath(store); err == nil {
		t.Fatal("symlinked sidecar succeeded")
	}
	if err := validateSpecs([]Spec{{
		ID: "workspace/test", Path: filepath.Join(root, "other.db"),
		LegacyRoots: []string{store + "-wal"},
	}}); err == nil {
		t.Fatal("symlinked legacy reservation succeeded")
	}
}
