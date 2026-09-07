//go:build unix && !aix

package config

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

type configSnapshotDriftResult struct {
	config   *Config
	revision string
	err      error
}

func TestConfigSnapshotsRejectPublicAndSecurityRevisionDrift(t *testing.T) {
	loaders := []struct {
		name string
		load func(string, string) (*Config, string, error)
	}{
		{
			name: "current runtime",
			load: func(path, _ string) (*Config, string, error) {
				return LoadCurrentConfigSnapshot(path)
			},
		},
		{
			name: "current update",
			load: func(path, _ string) (*Config, string, error) {
				return LoadCurrentConfigForUpdateSnapshot(path)
			},
		},
		{
			name: "current update expected revision",
			load: func(path, revision string) (*Config, string, error) {
				return LoadCurrentConfigForUpdateSnapshotIfRevision(path, revision)
			},
		},
		{
			name: "migrating runtime",
			load: func(path, _ string) (*Config, string, error) {
				return LoadConfigSnapshot(path)
			},
		},
		{
			name: "migrating update",
			load: func(path, _ string) (*Config, string, error) {
				return LoadConfigForUpdateSnapshot(path)
			},
		},
	}
	for _, target := range []string{"public", "security"} {
		for _, loader := range loaders {
			t.Run(target+"/"+loader.name, func(t *testing.T) {
				path, fifoPath := configSnapshotDriftFixture(t)
				publicBefore := readConfigSnapshotDriftFile(t, path)
				securityFile := securityPath(path)
				securityBefore := readConfigSnapshotDriftFile(t, securityFile)
				expectedRevision, err := ConfigRevision(path)
				if err != nil {
					t.Fatal(err)
				}

				result := make(chan configSnapshotDriftResult, 1)
				go func() {
					loaded, revision, loadErr := loader.load(path, expectedRevision)
					result <- configSnapshotDriftResult{config: loaded, revision: revision, err: loadErr}
				}()

				writer := awaitConfigSnapshotFIFOReader(t, fifoPath, result)
				var targetPath string
				var winner []byte
				switch target {
				case "public":
					targetPath = path
					winner = append(append([]byte(nil), publicBefore...), []byte("\n ")...)
				case "security":
					targetPath = securityFile
					winner = append(append([]byte(nil), securityBefore...), []byte("\n# concurrent winner\n")...)
				default:
					t.Fatalf("unknown drift target %q", target)
				}
				replaceConfigSnapshotDriftFile(t, targetPath, winner)
				if _, writeErr := writer.WriteString("resolved-fifo-secret\n"); writeErr != nil {
					t.Fatal(writeErr)
				}
				if closeErr := writer.Close(); closeErr != nil {
					t.Fatal(closeErr)
				}

				var loaded configSnapshotDriftResult
				select {
				case loaded = <-result:
				case <-time.After(5 * time.Second):
					t.Fatal("snapshot did not finish after FIFO release")
				}
				if loaded.config != nil || loaded.revision != "" ||
					!errors.Is(loaded.err, ErrConfigRevisionMismatch) {
					t.Fatalf(
						"drifted snapshot = (%#v, %q, %v), want nil, empty, revision mismatch",
						loaded.config, loaded.revision, loaded.err,
					)
				}

				if got := readConfigSnapshotDriftFile(t, targetPath); string(got) != string(winner) {
					t.Fatalf("winner bytes changed: got %q, want %q", got, winner)
				}
				if target == "public" {
					if got := readConfigSnapshotDriftFile(t, securityFile); string(got) != string(securityBefore) {
						t.Fatalf("security bytes changed: got %q, want %q", got, securityBefore)
					}
				} else if got := readConfigSnapshotDriftFile(t, path); string(got) != string(publicBefore) {
					t.Fatalf("public bytes changed: got %q, want %q", got, publicBefore)
				}
				winnerRevision, err := ConfigRevision(path)
				if err != nil {
					t.Fatal(err)
				}
				if winnerRevision == expectedRevision {
					t.Fatalf("winner retained old revision %q", winnerRevision)
				}
				assertNoConfigSnapshotDriftBackups(t, path)
			})
		}
	}
}

func TestMigratingConfigSnapshotsRejectInitialRevisionReadFailure(t *testing.T) {
	loaders := []struct {
		name string
		load func(string) (*Config, string, error)
	}{
		{name: "runtime", load: LoadConfigSnapshot},
		{name: "update", load: LoadConfigForUpdateSnapshot},
	}
	for _, loader := range loaders {
		t.Run(loader.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.json")
			if err := unix.Mkfifo(path, 0o600); err != nil {
				t.Fatal(err)
			}

			result := make(chan configSnapshotDriftResult, 1)
			go func() {
				loaded, revision, loadErr := loader.load(path)
				result <- configSnapshotDriftResult{config: loaded, revision: revision, err: loadErr}
			}()

			writer := awaitConfigSnapshotFIFOReader(t, path, result)
			if err := os.Remove(path); err != nil {
				t.Fatal(err)
			}
			if err := os.Mkdir(path, 0o700); err != nil {
				t.Fatal(err)
			}
			if _, err := writer.WriteString("{}\n"); err != nil {
				t.Fatal(err)
			}
			if err := writer.Close(); err != nil {
				t.Fatal(err)
			}

			select {
			case loaded := <-result:
				if loaded.config != nil || loaded.revision != "" || loaded.err == nil ||
					errors.Is(loaded.err, ErrConfigRevisionMismatch) {
					t.Fatalf(
						"snapshot initial revision failure = (%#v, %q, %v)",
						loaded.config, loaded.revision, loaded.err,
					)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("snapshot did not return after FIFO release")
			}
		})
	}
}

func configSnapshotDriftFixture(t *testing.T) (string, string) {
	t.Helper()

	root := t.TempDir()
	t.Setenv(EnvHome, filepath.Join(root, "home"))
	path := filepath.Join(root, "config.json")
	fifoPath := filepath.Join(root, "blocking.key")
	secret := &SecureString{raw: "file://blocking.key", resolved: "initial-secret"}
	cfg := DefaultConfig()
	cfg.ModelList = []*ModelConfig{{
		ModelName: "revision-drift",
		Provider:  "openai",
		Model:     "openai/revision-drift",
		APIKeys:   SecureStrings{secret},
		Enabled:   true,
	}}
	if err := SaveConfig(path, cfg); err != nil {
		t.Fatal(err)
	}
	security := readConfigSnapshotDriftFile(t, securityPath(path))
	if !strings.Contains(string(security), "file://blocking.key") {
		t.Fatalf("security fixture omits file reference: %s", security)
	}
	if err := unix.Mkfifo(fifoPath, 0o600); err != nil {
		t.Fatal(err)
	}
	return path, fifoPath
}

func awaitConfigSnapshotFIFOReader(
	t *testing.T,
	path string,
	result <-chan configSnapshotDriftResult,
) *os.File {
	t.Helper()

	deadline := time.Now().Add(5 * time.Second)
	for {
		select {
		case loaded := <-result:
			t.Fatalf("snapshot returned before resolving FIFO: %#v", loaded)
		default:
		}
		fd, err := unix.Open(path, unix.O_WRONLY|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
		if err == nil {
			file := os.NewFile(uintptr(fd), path)
			if file == nil {
				_ = unix.Close(fd)
				t.Fatal("wrap FIFO writer")
			}
			return file
		}
		if !errors.Is(err, unix.ENXIO) {
			t.Fatalf("open FIFO writer: %v", err)
		}
		if time.Now().After(deadline) {
			t.Fatal("snapshot did not reach FIFO credential resolution")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func replaceConfigSnapshotDriftFile(t *testing.T, path string, data []byte) {
	t.Helper()

	temporary := path + ".concurrent-winner"
	if err := os.WriteFile(temporary, data, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(temporary, path); err != nil {
		t.Fatal(err)
	}
}

func readConfigSnapshotDriftFile(t *testing.T, path string) []byte {
	t.Helper()

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func assertNoConfigSnapshotDriftBackups(t *testing.T, path string) {
	t.Helper()

	for _, pattern := range []string{path + ".*.bak", securityPath(path) + ".*.bak"} {
		matches, err := filepath.Glob(pattern)
		if err != nil {
			t.Fatal(err)
		}
		if len(matches) != 0 {
			t.Fatalf("snapshot created backup files: %v", matches)
		}
	}
}
