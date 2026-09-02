//go:build !unix && !windows

package database

import (
	"context"
	"net"
	"path/filepath"
)

func endpointForStateDirectory(stateDir string) string {
	return filepath.Join(stateDir, socketFileName)
}

func listenLocal(string) (net.Listener, error) {
	return nil, NewError(CodeUnsupported, "local database broker transport is unsupported")
}

func dialLocal(context.Context, string) (net.Conn, error) {
	return nil, NewError(CodeUnsupported, "local database broker transport is unsupported")
}

func prepareEndpoint(string) error { return nil }
func cleanupEndpoint(string) error { return nil }
