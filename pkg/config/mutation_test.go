package config

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
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

func TestLoadCurrentConfigForUpdateSnapshotIfRevisionComparesBeforeParsing(
	t *testing.T,
) {
	tests := []struct {
		name             string
		withSecurity     bool
		mutateWinner     func(*testing.T, string)
		currentErrorText string
	}{
		{
			name: "malformed public winner",
			mutateWinner: func(t *testing.T, path string) {
				t.Helper()
				if err := os.WriteFile(path, []byte("{"), 0o600); err != nil {
					t.Fatalf("WriteFile(malformed public) error = %v", err)
				}
			},
			currentErrorText: "syntax error",
		},
		{
			name: "legacy public winner",
			mutateWinner: func(t *testing.T, path string) {
				t.Helper()
				if err := os.WriteFile(
					path,
					[]byte(`{"version":`+strconv.Itoa(CurrentVersion-1)+`}`),
					0o600,
				); err != nil {
					t.Fatalf("WriteFile(legacy public) error = %v", err)
				}
			},
			currentErrorText: ErrConfigMigrationRequired.Error(),
		},
		{
			name:         "malformed security winner",
			withSecurity: true,
			mutateWinner: func(t *testing.T, path string) {
				t.Helper()
				if err := os.WriteFile(securityPath(path), []byte(":\n"), 0o600); err != nil {
					t.Fatalf("WriteFile(malformed security) error = %v", err)
				}
			},
			currentErrorText: "parse security config",
		},
		{
			name:         "orphaned security winner",
			withSecurity: true,
			mutateWinner: func(t *testing.T, path string) {
				t.Helper()
				if err := os.Remove(path); err != nil {
					t.Fatalf("Remove(public) error = %v", err)
				}
			},
			currentErrorText: "without public config",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.json")
			cfg := DefaultConfig()
			cfg.Workflows.Enabled = true
			if err := SaveConfig(path, cfg); err != nil {
				t.Fatalf("SaveConfig() error = %v", err)
			}
			if test.withSecurity {
				if err := os.WriteFile(securityPath(path), []byte("{}\n"), 0o600); err != nil {
					t.Fatalf("WriteFile(initial security) error = %v", err)
				}
			}
			staleRevision, err := ConfigRevision(path)
			if err != nil {
				t.Fatalf("ConfigRevision(stale) error = %v", err)
			}
			loaded, loadedRevision, err := LoadCurrentConfigForUpdateSnapshotIfRevision(
				path,
				staleRevision,
			)
			if err != nil || loaded == nil || !loaded.Workflows.Enabled ||
				loadedRevision != staleRevision {
				t.Fatalf("matching snapshot = (%#v, %q, %v)", loaded, loadedRevision, err)
			}

			test.mutateWinner(t, path)
			winnerRevision, err := ConfigRevision(path)
			if err != nil {
				t.Fatalf("ConfigRevision(winner) error = %v", err)
			}
			if winnerRevision == staleRevision {
				t.Fatal("winner mutation did not change revision")
			}
			if _, _, err = LoadCurrentConfigForUpdateSnapshotIfRevision(
				path,
				staleRevision,
			); !errors.Is(err, ErrConfigRevisionMismatch) {
				t.Fatalf("stale snapshot error = %v", err)
			}
			if _, _, err = LoadCurrentConfigForUpdateSnapshotIfRevision(
				path,
				winnerRevision,
			); err == nil || !strings.Contains(err.Error(), test.currentErrorText) {
				t.Fatalf("current invalid snapshot error = %v", err)
			}
		})
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

			if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("snapshot created public config: %v", err)
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
