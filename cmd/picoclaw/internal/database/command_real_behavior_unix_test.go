//go:build unix

package database

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/sipeed/picoclaw/pkg/config"
	dblayer "github.com/sipeed/picoclaw/pkg/database"
)

func TestDatabaseServeRejectsUnsafePublishedSocketBoundary(t *testing.T) {
	home := t.TempDir()
	workspace := filepath.Join(home, "workspace")
	if err := os.MkdirAll(workspace, 0o700); err != nil {
		t.Fatal(err)
	}
	configPath := writeDatabaseCommandConfig(t, home, workspace)
	t.Setenv(config.EnvHome, home)
	t.Setenv(config.EnvConfig, configPath)
	prepareDatabaseServeBootstrap(t, home)

	stateDir := filepath.Join(home, dblayer.StateDirectoryName)
	digest := sha256.Sum256([]byte(filepath.Clean(stateDir)))
	socketRoot := filepath.Join("/tmp", "picoclaw-database-"+strconv.Itoa(os.Geteuid()))
	if err := os.MkdirAll(socketRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	endpoint := filepath.Join(socketRoot, hex.EncodeToString(digest[:12])+".sock")
	if err := os.WriteFile(endpoint, []byte("not a socket"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Remove(endpoint) })

	command := NewDatabaseCommand()
	command.SetArgs([]string{"__serve", "--home", home})
	command.SetOut(io.Discard)
	command.SetErr(io.Discard)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	command.SetContext(ctx)
	if err := command.Execute(); err == nil {
		t.Fatal("serve accepted a regular file at its socket boundary")
	}
}
