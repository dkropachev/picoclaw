//nolint:govet // Durable publication stages intentionally use narrow error scopes.
package database

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

const (
	manifestMaxBytes int64 = 64 << 10
	tokenBytes             = 32
	epochBytes             = 16
)

// Manifest is the owner-only discovery record for one broker epoch.
type Manifest struct {
	PID      int    `json:"pid"`
	Protocol int    `json:"protocol"`
	Token    string `json:"token"`
	Endpoint string `json:"endpoint"`
	Epoch    string `json:"epoch"`
}

// ReadManifest discovers and validates the broker for an existing canonical
// home. It never accepts a caller-supplied endpoint or token.
func ReadManifest(home string) (Manifest, error) {
	stateDir, err := StateDirectory(home)
	if err != nil {
		return Manifest{}, err
	}
	path := filepath.Join(stateDir, manifestFileName)
	info, err := os.Lstat(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return Manifest{}, NewError(CodeUnavailable, "database broker discovery manifest is missing")
		}
		return Manifest{}, fmt.Errorf("inspect database broker manifest: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return Manifest{}, NewError(CodeIntegrity, "database broker manifest cannot be a symlink")
	}
	if err := validateOwnerOnlyFile(path, info, 0o600); err != nil {
		return Manifest{}, err
	}
	if info.Size() <= 0 || info.Size() > manifestMaxBytes {
		return Manifest{}, NewError(CodeIntegrity, "database broker manifest size is invalid")
	}
	file, err := openOwnerOnlyExistingFile(path, 0o600)
	if err != nil {
		return Manifest{}, fmt.Errorf("open database broker manifest: %w", err)
	}
	defer file.Close()
	raw, err := io.ReadAll(io.LimitReader(file, manifestMaxBytes+1))
	if err != nil {
		return Manifest{}, fmt.Errorf("read database broker manifest: %w", err)
	}
	if int64(len(raw)) > manifestMaxBytes {
		return Manifest{}, NewError(CodeIntegrity, "database broker manifest exceeds its size limit")
	}
	var manifest Manifest
	if err := unmarshalCanonicalStrict(raw, &manifest); err != nil {
		return Manifest{}, NewError(CodeIntegrity, "database broker manifest is invalid")
	}
	if err := validateManifest(manifest, stateDir); err != nil {
		return Manifest{}, err
	}
	return manifest, nil
}

func validateManifest(manifest Manifest, stateDir string) error {
	if manifest.PID <= 0 || manifest.Protocol != ProtocolVersion {
		if manifest.Protocol != ProtocolVersion && manifest.Protocol > 0 {
			return NewError(CodeUnsupported, "database broker protocol version is unsupported")
		}
		return NewError(CodeIntegrity, "database broker manifest identity is invalid")
	}
	if !validLowerHex(manifest.Token, tokenBytes*2) {
		return NewError(CodeIntegrity, "database broker manifest token is invalid")
	}
	if !validLowerHex(manifest.Epoch, epochBytes*2) {
		return NewError(CodeIntegrity, "database broker manifest epoch is invalid")
	}
	expectedEndpoint := endpointForStateDirectory(stateDir)
	if manifest.Endpoint != expectedEndpoint {
		return NewError(CodeIntegrity, "database broker manifest endpoint is invalid")
	}
	return nil
}

func writeManifest(stateDir string, manifest Manifest) error {
	if err := validateManifest(manifest, stateDir); err != nil {
		return err
	}
	raw, err := MarshalCanonical(manifest)
	if err != nil {
		return err
	}
	path := filepath.Join(stateDir, manifestFileName)
	if info, statErr := os.Lstat(path); statErr == nil {
		if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
			return NewError(CodeIntegrity, "database broker manifest boundary is invalid")
		}
		if err := validateOwnerOnlyFile(path, info, 0o600); err != nil {
			return err
		}
	} else if !errors.Is(statErr, os.ErrNotExist) {
		return fmt.Errorf("inspect database broker manifest: %w", statErr)
	}
	temporary, err := createOwnerOnlyTempFile(stateDir, ".broker-manifest-", 0o600)
	if err != nil {
		return fmt.Errorf("create database broker manifest: %w", err)
	}
	temporaryPath := temporary.Name()
	cleanup := true
	defer func() {
		_ = temporary.Close()
		if cleanup {
			_ = os.Remove(temporaryPath)
		}
	}()
	if err := writeAll(temporary, raw); err != nil {
		return fmt.Errorf("write database broker manifest: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		return fmt.Errorf("sync database broker manifest: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close database broker manifest: %w", err)
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		return fmt.Errorf("publish database broker manifest: %w", err)
	}
	cleanup = false
	if err := syncDirectory(stateDir); err != nil {
		return fmt.Errorf("sync database broker manifest directory: %w", err)
	}
	return nil
}

func removeManifestForEpoch(home, expectedEpoch string) error {
	manifest, err := ReadManifest(home)
	if err != nil {
		if CodeOf(err) == CodeUnavailable {
			return nil
		}
		return err
	}
	if manifest.Epoch != expectedEpoch || manifest.PID != os.Getpid() {
		return NewError(CodeConflict, "database broker manifest changed before shutdown")
	}
	stateDir, err := StateDirectory(home)
	if err != nil {
		return err
	}
	if err := os.Remove(filepath.Join(stateDir, manifestFileName)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove database broker manifest: %w", err)
	}
	return syncDirectory(stateDir)
}

func randomHex(byteCount int) (string, error) {
	if byteCount <= 0 {
		return "", NewError(CodeInvalid, "random identity size is invalid")
	}
	value := make([]byte, byteCount)
	if _, err := rand.Read(value); err != nil {
		return "", fmt.Errorf("generate database broker identity: %w", err)
	}
	return hex.EncodeToString(value), nil
}

func validLowerHex(value string, length int) bool {
	if len(value) != length || value != strings.ToLower(value) {
		return false
	}
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded)*2 == length
}
