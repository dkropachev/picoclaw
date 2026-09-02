package gateway

import (
	"context"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/database"
)

func TestNewGatewayCommand(t *testing.T) {
	cmd := NewGatewayCommand()

	require.NotNil(t, cmd)

	assert.Equal(t, "gateway", cmd.Use)
	assert.Equal(t, "Start picoclaw gateway", cmd.Short)

	assert.Len(t, cmd.Aliases, 1)
	assert.True(t, cmd.HasAlias("g"))

	assert.Nil(t, cmd.Run)
	assert.NotNil(t, cmd.RunE)

	assert.Nil(t, cmd.PersistentPreRun)
	assert.Nil(t, cmd.PersistentPostRun)

	assert.False(t, cmd.HasSubCommands())

	assert.True(t, cmd.HasFlags())
	assert.NotNil(t, cmd.Flags().Lookup("debug"))
	assert.NotNil(t, cmd.Flags().Lookup("allow-empty"))
	assert.NotNil(t, cmd.Flags().Lookup("host"))
}

func TestGatewayRuntimeChildRejectsDirectInvocation(t *testing.T) {
	t.Setenv(gatewayRuntimeChildEnvironment, "1")
	command := NewGatewayCommand()
	command.SetContext(context.Background())
	if err := command.Execute(); database.CodeOf(err) != database.CodeUnauthorized {
		t.Fatalf("direct runtime invocation error = %v, want Unauthorized", err)
	}
}

func TestGatewayReadinessUsesBrokerStatus(t *testing.T) {
	home := t.TempDir()
	configFile := filepath.Join(home, "config.json")
	configuration := config.DefaultConfig()
	configuration.Agents.Defaults.Workspace = filepath.Join(home, "workspace")
	if err := config.SaveConfig(configFile, configuration); err != nil {
		t.Fatal(err)
	}
	t.Setenv(config.EnvHome, home)
	t.Setenv(config.EnvConfig, configFile)
	// This is deliberately an invalid physical sidecar. Runtime admission must
	// not inspect it; only the broker's readiness snapshot is authoritative.
	if err := os.Mkdir(filepath.Join(home, "auth.db-wal"), 0o700); err != nil {
		t.Fatal(err)
	}
	var migrationRequired atomic.Bool
	server, err := database.StartServer(context.Background(), database.ServerOptions{
		Home: home, RequiredStores: []database.StoreID{"workspace.workflows"},
		StatusProvider: func(context.Context) ([]database.StoreStatus, error) {
			status := database.StoreStatus{ID: "workspace.workflows", Readiness: database.StoreReady}
			if migrationRequired.Load() {
				status.Readiness = database.StoreMigrationRequired
				status.Error = database.NewError(database.CodeMigrationRequired, "offline migration is required")
			}
			return []database.StoreStatus{status}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = server.Close(ctx)
	})
	client, err := database.Connect(home)
	if err != nil {
		t.Fatal(err)
	}
	if err := requireGatewayDatabaseReadiness(context.Background(), client); err != nil {
		t.Fatalf("broker-ready runtime admission failed: %v", err)
	}
	migrationRequired.Store(true)
	if err := requireGatewayDatabaseReadiness(
		context.Background(),
		client,
	); database.CodeOf(
		err,
	) != database.CodeMigrationRequired {
		t.Fatalf("broker migration readiness error = %v, want MigrationRequired", err)
	}
}

func TestResolveGatewayHostOverride(t *testing.T) {
	tests := []struct {
		name     string
		explicit bool
		host     string
		wantHost string
		wantErr  bool
	}{
		{name: "implicit empty host is allowed", explicit: false, host: "", wantHost: "", wantErr: false},
		{name: "explicit empty host rejected", explicit: true, host: "   ", wantHost: "", wantErr: true},
		{name: "explicit localhost kept", explicit: true, host: " localhost ", wantHost: "localhost", wantErr: false},
		{
			name:     "explicit multi host normalized",
			explicit: true,
			host:     " [::1] , 127.0.0.1 ",
			wantHost: "::1,127.0.0.1",
			wantErr:  false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := resolveGatewayHostOverride(tt.explicit, tt.host)
			if (err != nil) != tt.wantErr {
				t.Fatalf("resolveGatewayHostOverride() err = %v, wantErr %t", err, tt.wantErr)
			}
			if got != tt.wantHost {
				t.Fatalf("resolveGatewayHostOverride() host = %q, want %q", got, tt.wantHost)
			}
		})
	}
}
