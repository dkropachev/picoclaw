package config

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

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
	if err := SaveConfig(path, winner); err != nil {
		t.Fatalf("SaveConfig(winner) error = %v", err)
	}
	stale := DefaultConfig()
	stale.Workflows.RetentionDays = 30
	if _, err := SaveConfigIfRevision(
		path,
		stale,
		shownRevision,
	); !errors.Is(err, ErrConfigRevisionMismatch) {
		t.Fatalf("SaveConfigIfRevision(stale) error = %v", err)
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
	if err := SaveConfig(path, winner); err != nil {
		t.Fatalf("SaveConfig(winner) error = %v", err)
	}
	publicAfter, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile(public winner) error = %v", err)
	}
	if string(publicAfter) != string(publicBefore) {
		t.Fatal("test requires a security-only update with identical public bytes")
	}
	stale := mutationTestConfigWithKey("sk-stale-security-key")
	if _, err := SaveConfigIfRevision(
		path,
		stale,
		shownRevision,
	); !errors.Is(err, ErrConfigRevisionMismatch) {
		t.Fatalf("SaveConfigIfRevision(stale security) error = %v", err)
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
