package auth

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/sipeed/picoclaw/pkg/config"
)

func TestSaveWeixinConfigRejectsStaleRevision(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.json")
	t.Setenv(config.EnvConfig, configPath)
	t.Setenv(config.EnvHome, filepath.Dir(configPath))
	if err := config.SaveConfig(configPath, config.DefaultConfig()); err != nil {
		t.Fatalf("SaveConfig() error = %v", err)
	}

	originalSave := authSaveConfigIfRevision
	injected := false
	authSaveConfigIfRevision = func(
		path string,
		candidate *config.Config,
		expectedRevision string,
	) (string, error) {
		if !injected {
			injected = true
			concurrent, revision, err := config.LoadConfigForUpdateSnapshot(path)
			if err != nil {
				return "", err
			}
			concurrent.ModelAliases = append(
				concurrent.ModelAliases,
				config.ModelAliasConfig{Name: "concurrent", Model: "new-model"},
			)
			if _, err := originalSave(path, concurrent, revision); err != nil {
				return "", err
			}
		}
		return originalSave(path, candidate, expectedRevision)
	}
	t.Cleanup(func() {
		authSaveConfigIfRevision = originalSave
	})

	err := saveWeixinConfig("new-token", "", "")
	if err == nil {
		t.Fatal("saveWeixinConfig() error = nil, want stale revision conflict")
	}
	if !strings.Contains(
		err.Error(),
		"config changed while updating authentication settings; reload and retry",
	) {
		t.Fatalf("saveWeixinConfig() error = %v, want clear conflict", err)
	}

	updated, loadErr := config.LoadConfig(configPath)
	if loadErr != nil {
		t.Fatalf("LoadConfig() error = %v", loadErr)
	}
	if len(updated.ModelAliases) != 1 || updated.ModelAliases[0].Name != "concurrent" {
		t.Fatalf("model aliases = %#v, want concurrent update preserved", updated.ModelAliases)
	}
	weixin := updated.Channels.GetByType(config.ChannelWeixin)
	if weixin != nil && weixin.Enabled {
		t.Fatal("stale Weixin update was persisted")
	}
}
