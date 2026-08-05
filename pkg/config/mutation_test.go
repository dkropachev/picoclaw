package config

import (
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/sipeed/picoclaw/pkg/workflows/gatetypes"
)

func TestLoadCurrentConfigSnapshotRejectsLegacyWithoutMigration(t *testing.T) {
	for _, version := range []int{0, 1, 2, 5} {
		t.Run(strconv.Itoa(version), func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.json")
			publicBefore := []byte(`{"version":` + strconv.Itoa(version) + `}`)
			securityBefore := []byte("legacy-security-canary")
			if err := os.WriteFile(path, publicBefore, 0o600); err != nil {
				t.Fatalf("WriteFile(config) error = %v", err)
			}
			if err := os.WriteFile(
				securityPath(path),
				securityBefore,
				0o600,
			); err != nil {
				t.Fatalf("WriteFile(security) error = %v", err)
			}

			if _, _, err := LoadCurrentConfigSnapshot(path); !errors.Is(
				err,
				ErrConfigMigrationRequired,
			) {
				t.Fatalf("LoadCurrentConfigSnapshot() error = %v", err)
			}
			if _, _, err := LoadCurrentConfigForUpdateSnapshot(path); !errors.Is(
				err,
				ErrConfigMigrationRequired,
			) {
				t.Fatalf("LoadCurrentConfigForUpdateSnapshot() error = %v", err)
			}
			revision, err := ConfigRevision(path)
			if err != nil {
				t.Fatalf("ConfigRevision() error = %v", err)
			}
			if _, err = SaveReviewAttentionIfRevision(
				path,
				mutationTestReviewAttention("legacy"),
				revision,
			); !errors.Is(err, ErrConfigMigrationRequired) {
				t.Fatalf("SaveReviewAttentionIfRevision() error = %v", err)
			}
			publicAfter, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("ReadFile(config) error = %v", err)
			}
			securityAfter, err := os.ReadFile(securityPath(path))
			if err != nil {
				t.Fatalf("ReadFile(security) error = %v", err)
			}
			if string(publicAfter) != string(publicBefore) ||
				string(securityAfter) != string(securityBefore) {
				t.Fatalf(
					"read-only snapshot changed legacy files: public=%q security=%q",
					publicAfter,
					securityAfter,
				)
			}
			backups, err := filepath.Glob(path + ".*.bak")
			if err != nil {
				t.Fatalf("Glob(config backups) error = %v", err)
			}
			securityBackups, err := filepath.Glob(
				securityPath(path) + ".*.bak",
			)
			if err != nil {
				t.Fatalf("Glob(security backups) error = %v", err)
			}
			if len(backups) != 0 || len(securityBackups) != 0 {
				t.Fatalf(
					"read-only snapshot created backups: config=%v security=%v",
					backups,
					securityBackups,
				)
			}
		})
	}
}

func TestLoadCurrentConfigSnapshotKeepsCurrentBytes(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	cfg := DefaultConfig()
	cfg.Workflows.Enabled = true
	if err := SaveConfig(path, cfg); err != nil {
		t.Fatalf("SaveConfig() error = %v", err)
	}
	publicBefore, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile(before) error = %v", err)
	}
	revisionBefore, err := ConfigRevision(path)
	if err != nil {
		t.Fatalf("ConfigRevision(before) error = %v", err)
	}

	loaded, revision, err := LoadCurrentConfigSnapshot(path)
	if err != nil {
		t.Fatalf("LoadCurrentConfigSnapshot() error = %v", err)
	}
	if loaded == nil || !loaded.Workflows.Enabled {
		t.Fatalf("loaded config = %#v", loaded)
	}
	if revision != revisionBefore {
		t.Fatalf("revision = %q, want %q", revision, revisionBefore)
	}
	updateLoaded, updateRevision, err := LoadCurrentConfigForUpdateSnapshot(path)
	if err != nil {
		t.Fatalf("LoadCurrentConfigForUpdateSnapshot() error = %v", err)
	}
	if updateLoaded == nil || !updateLoaded.Workflows.Enabled ||
		updateRevision != revisionBefore {
		t.Fatalf("update snapshot = (%#v, %q)", updateLoaded, updateRevision)
	}
	publicAfter, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile(after) error = %v", err)
	}
	if string(publicAfter) != string(publicBefore) {
		t.Fatal("read-only snapshot changed current config bytes")
	}
}

func TestLoadCurrentConfigSnapshotMissingUsesDefaultsWithoutConfigFiles(
	t *testing.T,
) {
	path := filepath.Join(t.TempDir(), "config.json")
	cfg, revision, err := LoadCurrentConfigSnapshot(path)
	if err != nil {
		t.Fatalf("LoadCurrentConfigSnapshot() error = %v", err)
	}
	if cfg == nil || revision != "missing" {
		t.Fatalf("snapshot = (%#v, %q)", cfg, revision)
	}
	updateCfg, updateRevision, err := LoadCurrentConfigForUpdateSnapshot(path)
	if err != nil {
		t.Fatalf("LoadCurrentConfigForUpdateSnapshot() error = %v", err)
	}
	if updateCfg == nil || updateRevision != "missing" {
		t.Fatalf("update snapshot = (%#v, %q)", updateCfg, updateRevision)
	}
	for _, candidate := range []string{path, securityPath(path)} {
		if _, statErr := os.Stat(candidate); !errors.Is(statErr, os.ErrNotExist) {
			t.Fatalf("read-only snapshot created %s: %v", candidate, statErr)
		}
	}
}

func TestCurrentConfigSnapshotsRejectSecuritySidecarWithoutPublicConfig(
	t *testing.T,
) {
	for _, security := range [][]byte{[]byte("{}\n"), []byte("not: [valid")} {
		name := "valid"
		if strings.Contains(string(security), "[") {
			name = "malformed"
		}
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.json")
			if err := os.WriteFile(securityPath(path), security, 0o600); err != nil {
				t.Fatalf("WriteFile(security) error = %v", err)
			}

			for _, load := range []struct {
				name string
				call func(string) (*Config, string, error)
			}{
				{name: "runtime", call: LoadCurrentConfigSnapshot},
				{name: "update", call: LoadCurrentConfigForUpdateSnapshot},
			} {
				t.Run(load.name, func(t *testing.T) {
					if _, _, err := load.call(path); err == nil ||
						!strings.Contains(err.Error(), "without public config") {
						t.Fatalf("snapshot error = %v", err)
					}
				})
			}

			revision, err := ConfigRevision(path)
			if err != nil {
				t.Fatalf("ConfigRevision() error = %v", err)
			}
			if _, err = SaveReviewAttentionIfRevision(
				path,
				mutationTestReviewAttention("orphaned-sidecar"),
				revision,
			); err == nil || !strings.Contains(err.Error(), "without public config") {
				t.Fatalf("SaveReviewAttentionIfRevision() error = %v", err)
			}
			if _, err = os.Stat(path); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("scoped save created public config: %v", err)
			}
			after, err := os.ReadFile(securityPath(path))
			if err != nil {
				t.Fatalf("ReadFile(security) error = %v", err)
			}
			if string(after) != string(security) {
				t.Fatalf("security sidecar changed: before=%q after=%q", security, after)
			}
		})
	}
}

func TestSaveReviewAttentionIfRevisionPreservesPersistedEnvironmentAndDefaults(
	t *testing.T,
) {
	t.Setenv("PICOCLAW_GATEWAY_PORT", "34567")
	t.Setenv("PICOCLAW_AGENTS_DEFAULTS_WORKSPACE", "/environment/workspace")
	path := filepath.Join(t.TempDir(), "config.json")
	publicBefore := []byte(`{
  "version": ` + strconv.Itoa(CurrentVersion) + `,
  "agents": {
    "defaults": {
      "workspace": "/persisted/workspace",
      "context_manager_config": {"exact": 9007199254740993}
    }
  },
  "gateway": {"host": "localhost", "port": 23456},
  "reviews": {"attention": {}}
}`)
	securityBefore := []byte("{}\n")
	if err := os.WriteFile(path, publicBefore, 0o600); err != nil {
		t.Fatalf("WriteFile(config) error = %v", err)
	}
	if err := os.WriteFile(securityPath(path), securityBefore, 0o600); err != nil {
		t.Fatalf("WriteFile(security) error = %v", err)
	}

	loaded, revision, err := LoadCurrentConfigForUpdateSnapshot(path)
	if err != nil {
		t.Fatalf("LoadCurrentConfigForUpdateSnapshot() error = %v", err)
	}
	if loaded.Gateway.Port != 34567 ||
		loaded.Agents.Defaults.Workspace != "/environment/workspace" {
		t.Fatalf(
			"management view did not apply environment: gateway=%d workspace=%q",
			loaded.Gateway.Port,
			loaded.Agents.Defaults.Workspace,
		)
	}

	next := mutationTestReviewAttention("scoped")
	savedRevision, err := SaveReviewAttentionIfRevision(path, next, revision)
	if err != nil {
		t.Fatalf("SaveReviewAttentionIfRevision() error = %v", err)
	}
	currentRevision, err := ConfigRevision(path)
	if err != nil {
		t.Fatalf("ConfigRevision() error = %v", err)
	}
	if savedRevision == revision || savedRevision != currentRevision {
		t.Fatalf(
			"saved revision = %q, initial = %q, current = %q",
			savedRevision,
			revision,
			currentRevision,
		)
	}

	publicAfter, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile(config) error = %v", err)
	}
	securityAfter, err := os.ReadFile(securityPath(path))
	if err != nil {
		t.Fatalf("ReadFile(security) error = %v", err)
	}
	if string(securityAfter) != string(securityBefore) {
		t.Fatalf("security sidecar changed: before=%q after=%q", securityBefore, securityAfter)
	}

	var document map[string]json.RawMessage
	if err = json.Unmarshal(publicAfter, &document); err != nil {
		t.Fatalf("Unmarshal(config) error = %v", err)
	}
	if len(document) != 4 {
		t.Fatalf("scoped save materialized unrelated top-level defaults: %s", publicAfter)
	}
	var gateway map[string]json.RawMessage
	if err = json.Unmarshal(document["gateway"], &gateway); err != nil {
		t.Fatalf("Unmarshal(gateway) error = %v", err)
	}
	if string(gateway["port"]) != "23456" {
		t.Fatalf("persisted gateway port = %s, want 23456", gateway["port"])
	}
	if _, materialized := gateway["hot_reload"]; materialized {
		t.Fatalf("scoped save materialized gateway.hot_reload: %s", document["gateway"])
	}

	var agents map[string]json.RawMessage
	if err = json.Unmarshal(document["agents"], &agents); err != nil {
		t.Fatalf("Unmarshal(agents) error = %v", err)
	}
	var defaults map[string]json.RawMessage
	if err = json.Unmarshal(agents["defaults"], &defaults); err != nil {
		t.Fatalf("Unmarshal(agent defaults) error = %v", err)
	}
	if string(defaults["workspace"]) != `"/persisted/workspace"` {
		t.Fatalf("persisted workspace = %s", defaults["workspace"])
	}
	if !strings.Contains(
		string(defaults["context_manager_config"]),
		"9007199254740993",
	) {
		t.Fatalf(
			"unrelated exact JSON number changed: %s",
			defaults["context_manager_config"],
		)
	}
	if !strings.Contains(string(document["reviews"]), `"id": "scoped"`) {
		t.Fatalf("reviews.attention was not replaced: %s", document["reviews"])
	}
}

func TestSaveReviewAttentionIfRevisionPreservesOtherReviewMembers(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	publicBefore := []byte(`{"version":` + strconv.Itoa(CurrentVersion) +
		`,"reviews":{"attention":{},"future":{"exact":9007199254740993}}}`)
	if err := os.WriteFile(path, publicBefore, 0o600); err != nil {
		t.Fatalf("WriteFile(config) error = %v", err)
	}
	revision, err := ConfigRevision(path)
	if err != nil {
		t.Fatalf("ConfigRevision() error = %v", err)
	}
	if _, err = SaveReviewAttentionIfRevision(
		path,
		mutationTestReviewAttention("nested"),
		revision,
	); err != nil {
		t.Fatalf("SaveReviewAttentionIfRevision() error = %v", err)
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile(config) error = %v", err)
	}
	var document map[string]json.RawMessage
	if err = json.Unmarshal(raw, &document); err != nil {
		t.Fatalf("Unmarshal(config) error = %v", err)
	}
	var reviews map[string]json.RawMessage
	if err = json.Unmarshal(document["reviews"], &reviews); err != nil {
		t.Fatalf("Unmarshal(reviews) error = %v", err)
	}
	if !strings.Contains(string(reviews["future"]), "9007199254740993") {
		t.Fatalf("unrelated reviews member changed: %s", reviews["future"])
	}
	if !strings.Contains(string(reviews["attention"]), `"id": "nested"`) {
		t.Fatalf("attention member was not replaced: %s", reviews["attention"])
	}
}

func TestSaveReviewAttentionIfRevisionFencesPublicAndSecurityWriters(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(path string) error
	}{
		{
			name: "public writer",
			mutate: func(path string) error {
				return os.WriteFile(
					path,
					[]byte(`{"version":`+strconv.Itoa(CurrentVersion)+`,"gateway":{"port":32123}}`),
					0o600,
				)
			},
		},
		{
			name: "security writer",
			mutate: func(path string) error {
				return os.WriteFile(securityPath(path), []byte("tools: {}\n"), 0o600)
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.json")
			initial := []byte(`{"version":` + strconv.Itoa(CurrentVersion) + `}`)
			if err := os.WriteFile(path, initial, 0o600); err != nil {
				t.Fatalf("WriteFile(config) error = %v", err)
			}
			if err := os.WriteFile(securityPath(path), []byte("{}\n"), 0o600); err != nil {
				t.Fatalf("WriteFile(security) error = %v", err)
			}
			staleRevision, err := ConfigRevision(path)
			if err != nil {
				t.Fatalf("ConfigRevision() error = %v", err)
			}
			if err = test.mutate(path); err != nil {
				t.Fatalf("mutate() error = %v", err)
			}
			publicWinner, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("ReadFile(config winner) error = %v", err)
			}
			securityWinner, err := os.ReadFile(securityPath(path))
			if err != nil {
				t.Fatalf("ReadFile(security winner) error = %v", err)
			}

			if _, err = SaveReviewAttentionIfRevision(
				path,
				mutationTestReviewAttention("stale"),
				staleRevision,
			); !errors.Is(err, ErrConfigRevisionMismatch) {
				t.Fatalf("SaveReviewAttentionIfRevision() error = %v", err)
			}
			publicAfter, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("ReadFile(config after stale save) error = %v", err)
			}
			securityAfter, err := os.ReadFile(securityPath(path))
			if err != nil {
				t.Fatalf("ReadFile(security after stale save) error = %v", err)
			}
			if string(publicAfter) != string(publicWinner) ||
				string(securityAfter) != string(securityWinner) {
				t.Fatalf(
					"stale scoped save changed winner: public=%q security=%q",
					publicAfter,
					securityAfter,
				)
			}
		})
	}
}

func TestSaveReviewAttentionIfRevisionCreatesMinimalMissingConfig(t *testing.T) {
	t.Setenv("PICOCLAW_GATEWAY_PORT", "45678")
	path := filepath.Join(t.TempDir(), "config.json")
	next := mutationTestReviewAttention("missing")
	revision, err := SaveReviewAttentionIfRevision(path, next, "missing")
	if err != nil {
		t.Fatalf("SaveReviewAttentionIfRevision() error = %v", err)
	}
	if revision == "missing" {
		t.Fatal("scoped save retained the missing revision")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile(config) error = %v", err)
	}
	var document map[string]json.RawMessage
	if err = json.Unmarshal(raw, &document); err != nil {
		t.Fatalf("Unmarshal(config) error = %v", err)
	}
	if len(document) != 2 || string(document["version"]) != strconv.Itoa(CurrentVersion) {
		t.Fatalf("missing save was not minimal current schema: %s", raw)
	}
	if _, persistedEnvironment := document["gateway"]; persistedEnvironment {
		t.Fatalf("missing save persisted environment overrides: %s", raw)
	}
	if _, err = os.Stat(securityPath(path)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing save created security sidecar: %v", err)
	}

	loaded, loadedRevision, err := LoadCurrentConfigForUpdateSnapshot(path)
	if err != nil {
		t.Fatalf("LoadCurrentConfigForUpdateSnapshot() error = %v", err)
	}
	if loadedRevision != revision || loaded.Gateway.Port != 45678 ||
		len(loaded.Reviews.Attention.Global["review.submitted"]) != 1 ||
		loaded.Reviews.Attention.Global["review.submitted"][0].ID != "missing" {
		t.Fatalf("loaded missing-config result = %#v revision=%q", loaded, loadedRevision)
	}
}

func mutationTestReviewAttention(id string) ReviewAttentionConfig {
	return ReviewAttentionConfig{
		Global: map[string][]gatetypes.GateSpec{
			"review.submitted": {{ID: id, Kind: gatetypes.GateZero}},
		},
		Repositories: map[string]map[string]gatetypes.RepositoryGatePolicy{},
	}
}

func TestSaveConfigIfRevisionRejectsStaleWriter(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	initial := DefaultConfig()
	initial.Workflows.RetentionDays = 10
	if err := SaveConfig(path, initial); err != nil {
		t.Fatalf("SaveConfig(initial) error = %v", err)
	}
	shownRevision, err := ConfigRevision(path)
	if err != nil {
		t.Fatalf("ConfigRevision(initial) error = %v", err)
	}

	winner := DefaultConfig()
	winner.Workflows.RetentionDays = 20
	if saveErr := SaveConfig(path, winner); saveErr != nil {
		t.Fatalf("SaveConfig(winner) error = %v", saveErr)
	}
	stale := DefaultConfig()
	stale.Workflows.RetentionDays = 30
	if _, saveErr := SaveConfigIfRevision(
		path,
		stale,
		shownRevision,
	); !errors.Is(saveErr, ErrConfigRevisionMismatch) {
		t.Fatalf("SaveConfigIfRevision(stale) error = %v", saveErr)
	}

	got, err := LoadConfigForUpdate(path)
	if err != nil {
		t.Fatalf("LoadConfigForUpdate() error = %v", err)
	}
	if got.Workflows.RetentionDays != 20 {
		t.Fatalf(
			"retention days = %d, stale writer overwrote winner",
			got.Workflows.RetentionDays,
		)
	}
}

func TestSaveConfigIfRevisionReturnsExactNewRevision(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	if revision, err := ConfigRevision(path); err != nil || revision != "missing" {
		t.Fatalf("missing ConfigRevision() = %q, %v", revision, err)
	}
	cfg := DefaultConfig()
	cfg.Workflows.Enabled = true
	revision, err := SaveConfigIfRevision(path, cfg, "missing")
	if err != nil {
		t.Fatalf("SaveConfigIfRevision() error = %v", err)
	}
	current, err := ConfigRevision(path)
	if err != nil {
		t.Fatalf("ConfigRevision() error = %v", err)
	}
	if revision == "missing" || revision != current {
		t.Fatalf("saved revision = %q, current = %q", revision, current)
	}
}

func TestSaveConfigIfRevisionFencesSecuritySidecarChanges(t *testing.T) {
	mustSetupSSHKey(t)
	path := filepath.Join(t.TempDir(), "config.json")
	initial := mutationTestConfigWithKey("sk-initial-security-key")
	if err := SaveConfig(path, initial); err != nil {
		t.Fatalf("SaveConfig(initial) error = %v", err)
	}
	shownRevision, err := ConfigRevision(path)
	if err != nil {
		t.Fatalf("ConfigRevision(initial) error = %v", err)
	}
	publicBefore, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile(public initial) error = %v", err)
	}

	winner := mutationTestConfigWithKey("sk-winning-security-key")
	if saveErr := SaveConfig(path, winner); saveErr != nil {
		t.Fatalf("SaveConfig(winner) error = %v", saveErr)
	}
	publicAfter, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile(public winner) error = %v", err)
	}
	if string(publicAfter) != string(publicBefore) {
		t.Fatal("test requires a security-only update with identical public bytes")
	}
	stale := mutationTestConfigWithKey("sk-stale-security-key")
	if _, saveErr := SaveConfigIfRevision(
		path,
		stale,
		shownRevision,
	); !errors.Is(saveErr, ErrConfigRevisionMismatch) {
		t.Fatalf("SaveConfigIfRevision(stale security) error = %v", saveErr)
	}
	got, err := LoadConfigForUpdate(path)
	if err != nil {
		t.Fatalf("LoadConfigForUpdate() error = %v", err)
	}
	if len(got.ModelList) != 1 ||
		got.ModelList[0].APIKey() != "sk-winning-security-key" {
		t.Fatalf("stale writer replaced security sidecar: %#v", got.ModelList)
	}
}

func mutationTestConfigWithKey(key string) *Config {
	cfg := DefaultConfig()
	cfg.ModelList = []*ModelConfig{{
		ModelName: "security-revision",
		Provider:  "openai",
		Model:     "openai/security-revision",
		APIKeys:   SimpleSecureStrings(key),
		Enabled:   true,
	}}
	return cfg
}

func TestConfigMutationLockSerializesHelperProcess(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	if childPath := os.Getenv("PICOCLAW_CONFIG_LOCK_PATH"); childPath != "" {
		path = childPath
	}
	readyPath := path + ".child-ready"
	releasePath := path + ".child-release"
	if os.Getenv("PICOCLAW_CONFIG_LOCK_CHILD") == "1" {
		unlock, err := lockConfigMutation(path)
		if err != nil {
			t.Fatalf("child lockConfigMutation() error = %v", err)
		}
		defer unlock()
		if err := os.WriteFile(readyPath, []byte("ready"), 0o600); err != nil {
			t.Fatalf("child ready write error = %v", err)
		}
		deadline := time.Now().Add(5 * time.Second)
		for {
			if _, err := os.Stat(releasePath); err == nil {
				return
			} else if !os.IsNotExist(err) {
				t.Fatalf("child release stat error = %v", err)
			}
			if time.Now().After(deadline) {
				t.Fatal("child timed out waiting for release")
			}
			time.Sleep(10 * time.Millisecond)
		}
	}

	command := exec.Command(
		os.Args[0],
		"-test.run=^TestConfigMutationLockSerializesHelperProcess$",
	)
	command.Env = append(
		os.Environ(),
		"PICOCLAW_CONFIG_LOCK_CHILD=1",
		"PICOCLAW_CONFIG_LOCK_PATH="+path,
	)
	if err := command.Start(); err != nil {
		t.Fatalf("start helper process error = %v", err)
	}
	t.Cleanup(func() {
		_ = os.WriteFile(releasePath, []byte("release"), 0o600)
		_ = command.Process.Kill()
		_ = command.Wait()
	})
	waitForConfigMutationTestFile(t, readyPath)

	acquired := make(chan func(), 1)
	failed := make(chan error, 1)
	go func() {
		unlock, err := lockConfigMutation(path)
		if err != nil {
			failed <- err
			return
		}
		acquired <- unlock
	}()
	select {
	case unlock := <-acquired:
		unlock()
		t.Fatal("parent acquired config lock while helper process held it")
	case err := <-failed:
		t.Fatalf("parent config lock error = %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	if err := os.WriteFile(releasePath, []byte("release"), 0o600); err != nil {
		t.Fatalf("release helper error = %v", err)
	}
	if err := command.Wait(); err != nil {
		t.Fatalf("helper process error = %v", err)
	}
	select {
	case unlock := <-acquired:
		unlock()
	case err := <-failed:
		t.Fatalf("parent config lock after release error = %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("parent did not acquire config lock after helper released it")
	}
}

func waitForConfigMutationTestFile(t *testing.T, path string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, err := os.Stat(path); err == nil {
			return
		} else if !os.IsNotExist(err) {
			t.Fatalf("stat helper ready file error = %v", err)
		}
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting for helper process")
		}
		time.Sleep(10 * time.Millisecond)
	}
}
