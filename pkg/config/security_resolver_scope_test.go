package config

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

type resolverScopeLoadResult struct {
	config *Config
	err    error
}

func TestSecurityCopyFromUsesItsExplicitConfigDirectory(t *testing.T) {
	root := t.TempDir()
	t.Setenv(EnvHome, filepath.Join(root, "home"))
	firstDirectory := filepath.Join(root, "first")
	secondDirectory := filepath.Join(root, "second")
	for directory, value := range map[string]string{
		firstDirectory:  "first-local\n",
		secondDirectory: "second-local\n",
	} {
		if err := os.MkdirAll(directory, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(directory, "value.key"), []byte(value), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	firstPath := writeResolverScopeConfig(t, firstDirectory, "file://value.key")
	secondPath := writeResolverScopeConfig(t, secondDirectory, "file://value.key")

	for _, test := range []struct {
		name string
		copy func(*Config, string) error
	}{
		{name: "runtime", copy: (*Config).SecurityCopyFrom},
		{name: "update", copy: (*Config).SecurityCopyFromForUpdate},
	} {
		t.Run(test.name, func(t *testing.T) {
			loaded, err := LoadConfig(firstPath)
			if err != nil {
				t.Fatal(err)
			}
			if values := resolverScopeModelKeys(loaded); !slices.Equal(values, []string{"first-local"}) {
				t.Fatalf("first config keys = %#v", values)
			}

			target := loaded
			if err := test.copy(target, secondPath); err != nil {
				t.Fatal(err)
			}
			if values := resolverScopeModelKeys(target); !slices.Equal(values, []string{"second-local"}) {
				t.Fatalf("security copy used stale resolver: %#v", values)
			}
		})
	}
}

func TestSaveConfigUsesItsExplicitResolverDirectory(t *testing.T) {
	root := t.TempDir()
	t.Setenv(EnvHome, filepath.Join(root, "home"))
	firstDirectory := filepath.Join(root, "first")
	secondDirectory := filepath.Join(root, "second")
	if err := os.MkdirAll(firstDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(secondDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	firstPath := writeResolverScopeConfig(t, firstDirectory)
	if _, err := LoadConfig(firstPath); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		filepath.Join(secondDirectory, "value.key"),
		[]byte("second-local\n"),
		0o600,
	); err != nil {
		t.Fatal(err)
	}

	secondPath := filepath.Join(secondDirectory, "config.json")
	cfg := DefaultConfig()
	cfg.Channels = ChannelsConfig{"telegram": {
		Enabled:  true,
		Type:     ChannelTelegram,
		Settings: RawNode(`{"token":"file://value.key"}`),
	}}
	if err := SaveConfig(secondPath, cfg); err != nil {
		t.Fatal(err)
	}
	security, err := os.ReadFile(securityPath(secondPath))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(security), "file://value.key") {
		t.Fatalf("saved security config lost directory-scoped reference:\n%s", security)
	}
	loaded, err := LoadConfig(secondPath)
	if err != nil {
		t.Fatal(err)
	}
	channel := loaded.Channels["telegram"]
	if channel == nil {
		t.Fatal("saved Telegram channel is missing")
	}
	var settings TelegramSettings
	if err := channel.Decode(&settings); err != nil {
		t.Fatal(err)
	}
	if settings.Token.String() != "second-local" {
		t.Fatalf("saved Telegram token = %q", settings.Token.String())
	}
}

func TestLegacyConfigMigrationReleasesResolverScopeBeforeSave(t *testing.T) {
	for _, test := range []struct {
		name string
		load func(string) (*Config, string, error)
	}{
		{
			name: "runtime",
			load: func(path string) (*Config, string, error) {
				cfg, err := LoadConfig(path)
				return cfg, "", err
			},
		},
		{
			name: "snapshot",
			load: LoadConfigSnapshot,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			t.Setenv(EnvHome, filepath.Join(root, "home"))
			path := filepath.Join(root, "config.json")
			if err := os.WriteFile(path, []byte(`{"version":5}`), 0o600); err != nil {
				t.Fatal(err)
			}
			done := make(chan resolverScopeLoadResult, 1)
			go func() {
				cfg, _, err := test.load(path)
				done <- resolverScopeLoadResult{config: cfg, err: err}
			}()
			loaded := awaitResolverScopeLoad(t, done)
			if loaded.config.Version != CurrentVersion {
				t.Fatalf("migrated config version = %d", loaded.config.Version)
			}
		})
	}
}

func writeResolverScopeConfig(t *testing.T, directory string, references ...string) string {
	t.Helper()

	keys := make(SecureStrings, 0, len(references))
	for _, reference := range references {
		keys = append(keys, &SecureString{raw: reference, resolved: "fixture"})
	}
	cfg := DefaultConfig()
	cfg.ModelList = []*ModelConfig{{
		ModelName: "resolver-scope",
		Provider:  "openai",
		Model:     "openai/resolver-scope",
		APIKeys:   keys,
		Enabled:   true,
	}}
	path := filepath.Join(directory, "config.json")
	if err := SaveConfig(path, cfg); err != nil {
		t.Fatal(err)
	}
	return path
}

func resolverScopeModelKeys(cfg *Config) []string {
	if cfg == nil {
		return nil
	}
	var result []string
	for _, model := range cfg.ModelList {
		if model != nil {
			result = append(result, model.APIKeys.Values()...)
		}
	}
	return result
}

func awaitResolverScopeLoad(t *testing.T, result <-chan resolverScopeLoadResult) resolverScopeLoadResult {
	t.Helper()

	select {
	case loaded := <-result:
		if loaded.err != nil || loaded.config == nil {
			t.Fatalf("config load = %#v, %v", loaded.config, loaded.err)
		}
		return loaded
	case <-time.After(5 * time.Second):
		t.Fatal("config load did not finish")
		return resolverScopeLoadResult{}
	}
}
