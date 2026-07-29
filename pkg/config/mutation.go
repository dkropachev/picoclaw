package config

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

// ErrConfigRevisionMismatch means a compare-and-swap save observed a newer
// configuration generation and did not write either config file.
var ErrConfigRevisionMismatch = errors.New("config revision mismatch")

var configMutationLocks sync.Map

// ConfigRevision returns an opaque revision for the exact public and
// security-managed config bytes. A not-yet-created config has the stable
// "missing" revision. Security bytes are hashed, never returned.
func ConfigRevision(path string) (string, error) {
	publicData, publicMissing, err := configRevisionFile(path)
	if err != nil {
		return "", fmt.Errorf("read public config revision: %w", err)
	}
	securityData, securityMissing, err := configRevisionFile(securityPath(path))
	if err != nil {
		return "", fmt.Errorf("read security config revision: %w", err)
	}
	if publicMissing && securityMissing {
		return "missing", nil
	}
	digest := sha256.New()
	writeConfigRevisionPart(digest, "public", publicData, publicMissing)
	writeConfigRevisionPart(digest, "security", securityData, securityMissing)
	return "sha256:" + hex.EncodeToString(digest.Sum(nil)), nil
}

// LoadConfigSnapshot atomically loads the runtime config and its exact opaque
// public-plus-security revision under the config mutation lock.
func LoadConfigSnapshot(path string) (*Config, string, error) {
	return loadConfigSnapshot(path, true)
}

// LoadConfigForUpdateSnapshot atomically loads the update-safe config and its
// exact opaque public-plus-security revision under the config mutation lock.
func LoadConfigForUpdateSnapshot(path string) (*Config, string, error) {
	return loadConfigSnapshot(path, false)
}

// SaveConfigIfRevision atomically compares the current public-plus-security
// config revision and saves cfg while holding the same process and advisory
// lock used by every SaveConfig caller. It returns the new exact revision.
func SaveConfigIfRevision(
	path string,
	cfg *Config,
	expectedRevision string,
) (string, error) {
	unlock, err := lockConfigMutation(path)
	if err != nil {
		return "", err
	}
	defer unlock()
	currentRevision, err := ConfigRevision(path)
	if err != nil {
		return "", err
	}
	if currentRevision != expectedRevision {
		return "", ErrConfigRevisionMismatch
	}
	if err := saveConfigUnlocked(path, cfg); err != nil {
		return "", err
	}
	return ConfigRevision(path)
}

func lockConfigMutation(path string) (func(), error) {
	key, err := canonicalConfigMutationPath(path)
	if err != nil {
		return nil, err
	}
	actual, _ := configMutationLocks.LoadOrStore(key, &sync.Mutex{})
	mutex := actual.(*sync.Mutex)
	mutex.Lock()
	unlockFile, err := lockConfigMutationFile(key + ".mutation.lock")
	if err != nil {
		mutex.Unlock()
		return nil, err
	}
	return func() {
		unlockFile()
		mutex.Unlock()
	}, nil
}

func canonicalConfigMutationPath(path string) (string, error) {
	if path == "" {
		return "", errors.New("config path is required")
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("resolve config path: %w", err)
	}
	// Keep the lock identity lexical. Atomic replacement can intentionally
	// replace a final symlink; evaluating it here would change the lock key
	// after the first save and let the same configured path bypass its old lock.
	return filepath.Clean(absolute), nil
}

func configRevisionFile(path string) ([]byte, bool, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, true, nil
		}
		return nil, false, err
	}
	return data, false, nil
}

func writeConfigRevisionPart(
	digest interface {
		Write(data []byte) (int, error)
	},
	name string,
	data []byte,
	missing bool,
) {
	_, _ = digest.Write([]byte(name))
	if missing {
		_, _ = digest.Write([]byte{0})
		return
	}
	_, _ = digest.Write([]byte{1})
	var length [8]byte
	binary.BigEndian.PutUint64(length[:], uint64(len(data)))
	_, _ = digest.Write(length[:])
	_, _ = digest.Write(data)
}

func loadConfigSnapshot(
	path string,
	validateEventIngressRuntime bool,
) (*Config, string, error) {
	for attempt := 0; attempt < 3; attempt++ {
		unlock, err := lockConfigMutation(path)
		if err != nil {
			return nil, "", err
		}
		requiresMigration, err := configSnapshotRequiresMigration(path)
		if err != nil {
			unlock()
			return nil, "", err
		}
		if !requiresMigration {
			cfg, loadErr := loadConfigWithOptions(path, validateEventIngressRuntime)
			revision, revisionErr := ConfigRevision(path)
			unlock()
			if loadErr != nil {
				return nil, "", loadErr
			}
			if revisionErr != nil {
				return nil, "", revisionErr
			}
			return cfg, revision, nil
		}
		unlock()

		// Legacy loading persists a migrated config through SaveConfig. Run that
		// path without recursively holding the lock, then retry the current
		// generation atomically.
		if _, err := loadConfigWithOptions(path, validateEventIngressRuntime); err != nil {
			return nil, "", err
		}
	}
	return nil, "", errors.New("config migration did not reach the current version")
}

func configSnapshotRequiresMigration(path string) (bool, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}
	if len(data) <= 10 {
		return false, nil
	}
	var version struct {
		Version int `json:"version"`
	}
	versionEnvelopeValid := json.Unmarshal(data, &version) == nil
	if !versionEnvelopeValid {
		// The normal loader returns its bounded diagnostic for malformed JSON.
		return false, nil
	}
	return version.Version >= 0 && version.Version < CurrentVersion, nil
}
