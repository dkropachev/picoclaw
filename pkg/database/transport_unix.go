//go:build unix

//nolint:govet // Endpoint setup stages intentionally use narrow error scopes.
package database

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
)

func endpointForStateDirectory(stateDir string) string {
	// Unix-domain socket path limits are commonly 104-108 bytes, while a
	// canonical PicoClaw home may be much longer. Derive a collision-resistant
	// name inside a short, owner-only runtime directory instead of truncating
	// the trusted home identity.
	digest := sha256.Sum256([]byte(filepath.Clean(stateDir)))
	root := filepath.Join(
		shortSocketTemporaryRoot(),
		"picoclaw-database-"+strconv.Itoa(os.Geteuid()),
	)
	return filepath.Join(root, hex.EncodeToString(digest[:12])+".sock")
}

func listenLocal(endpoint string) (net.Listener, error) {
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: endpoint, Net: "unix"})
	if err != nil {
		return nil, fmt.Errorf("listen on database broker socket: %w", err)
	}
	listener.SetUnlinkOnClose(true)
	if err := os.Chmod(endpoint, 0o600); err != nil {
		_ = listener.Close()
		return nil, fmt.Errorf("secure database broker socket: %w", err)
	}
	info, err := os.Lstat(endpoint)
	if err != nil {
		_ = listener.Close()
		return nil, fmt.Errorf("inspect database broker socket: %w", err)
	}
	if err := validateOwnerOnlySocket(endpoint, info); err != nil {
		_ = listener.Close()
		return nil, err
	}
	return listener, nil
}

func dialLocal(ctx context.Context, endpoint string) (net.Conn, error) {
	dialer := net.Dialer{}
	connection, err := dialer.DialContext(ctx, "unix", endpoint)
	if err != nil {
		return nil, fmt.Errorf("connect to database broker: %w", err)
	}
	return connection, nil
}

func prepareEndpoint(endpoint string) error {
	parent := filepath.Dir(endpoint)
	parentInfo, err := os.Lstat(parent)
	if errors.Is(err, os.ErrNotExist) {
		if err := os.Mkdir(parent, 0o700); err != nil {
			return fmt.Errorf("create database broker socket directory: %w", err)
		}
		parentInfo, err = os.Lstat(parent)
	}
	if err != nil {
		return fmt.Errorf("inspect database broker socket directory: %w", err)
	}
	if parentInfo.Mode()&os.ModeSymlink != 0 || !parentInfo.IsDir() {
		return NewError(CodeIntegrity, "database broker socket directory is unsafe")
	}
	if err := validateOwnerOnlyDirectory(parent, parentInfo); err != nil {
		return err
	}
	info, err := os.Lstat(endpoint)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("inspect database broker socket: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || info.Mode()&os.ModeSocket == 0 {
		return NewError(CodeIntegrity, "database broker endpoint boundary is invalid")
	}
	if err := validateOwnerOnlySocket(endpoint, info); err != nil {
		return err
	}
	if err := os.Remove(endpoint); err != nil {
		return fmt.Errorf("remove stale database broker socket: %w", err)
	}
	return nil
}

func shortSocketTemporaryRoot() string {
	for _, candidate := range []string{"/tmp", os.TempDir()} {
		info, err := os.Stat(candidate)
		if err == nil && info.IsDir() {
			return candidate
		}
	}
	return os.TempDir()
}

func cleanupEndpoint(endpoint string) error {
	info, err := os.Lstat(endpoint)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("inspect database broker socket during shutdown: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || info.Mode()&os.ModeSocket == 0 {
		return NewError(CodeIntegrity, "database broker endpoint changed during shutdown")
	}
	if err := os.Remove(endpoint); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove database broker socket: %w", err)
	}
	return nil
}
