//go:build windows

package database

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net"
	"path/filepath"
	"strings"

	"github.com/Microsoft/go-winio"
	"golang.org/x/sys/windows"
)

func endpointForStateDirectory(stateDir string) string {
	// Windows resolves these physical paths case-insensitively. Hash the same
	// normalized identity used by sameCanonicalPath so aliases discover one
	// named pipe instead of producing an invalid manifest endpoint.
	normalized := strings.ToLower(filepath.Clean(stateDir))
	digest := sha256.Sum256([]byte(normalized))
	return `\\.\pipe\picoclaw-database-` + hex.EncodeToString(digest[:12])
}

func listenLocal(endpoint string) (net.Listener, error) {
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil || user == nil || user.User.Sid == nil {
		return nil, fmt.Errorf("resolve database broker pipe owner: %w", err)
	}
	securityDescriptor := "D:P(A;;GA;;;" + user.User.Sid.String() + ")"
	listener, err := winio.ListenPipe(endpoint, &winio.PipeConfig{
		SecurityDescriptor: securityDescriptor,
		MessageMode:        false,
		InputBufferSize:    64 << 10,
		OutputBufferSize:   64 << 10,
	})
	if err != nil {
		return nil, fmt.Errorf("listen on database broker named pipe: %w", err)
	}
	return listener, nil
}

func dialLocal(ctx context.Context, endpoint string) (net.Conn, error) {
	connection, err := winio.DialPipeContext(ctx, endpoint)
	if err != nil {
		return nil, fmt.Errorf("connect to database broker named pipe: %w", err)
	}
	return connection, nil
}

func prepareEndpoint(string) error { return nil }
func cleanupEndpoint(string) error { return nil }
