package migration

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/sipeed/picoclaw/pkg/accountrouter"
	"github.com/sipeed/picoclaw/pkg/auth"
	"github.com/sipeed/picoclaw/pkg/channels/wecom"
	"github.com/sipeed/picoclaw/pkg/channels/weixin"
	"github.com/sipeed/picoclaw/pkg/cron"
	"github.com/sipeed/picoclaw/pkg/database"
	"github.com/sipeed/picoclaw/pkg/eventing"
	"github.com/sipeed/picoclaw/pkg/evolution"
	"github.com/sipeed/picoclaw/pkg/gateway"
	"github.com/sipeed/picoclaw/pkg/gitworkspace"
	"github.com/sipeed/picoclaw/pkg/memory"
	"github.com/sipeed/picoclaw/pkg/prworkspace/localci"
	"github.com/sipeed/picoclaw/pkg/repoaudit"
	"github.com/sipeed/picoclaw/pkg/repoeval"
	"github.com/sipeed/picoclaw/pkg/seahorse"
	"github.com/sipeed/picoclaw/pkg/state"
	"github.com/sipeed/picoclaw/pkg/tools"
	"github.com/sipeed/picoclaw/pkg/workflows"
	backendapi "github.com/sipeed/picoclaw/web/backend/api"
	"github.com/sipeed/picoclaw/web/backend/dashboardauth"
)

func TestExportedOfflineAdaptersRequireMigrationFence(t *testing.T) {
	home := t.TempDir()
	workspace := filepath.Join(home, "workspace")
	ctx := context.Background()
	tests := []struct {
		name string
		run  func() error
	}{
		{name: "auth", run: func() error { return auth.RunOfflineDatabaseMigration(ctx, home) }},
		{
			name: "launcher auth",
			run: func() error {
				return dashboardauth.RunOfflineDatabaseMigration(
					home,
					filepath.Join(home, "launcher-config.json"),
				)
			},
		},
		{
			name: "model catalogs",
			run:  func() error { return backendapi.RunOfflineModelCatalogMigration(ctx, home) },
		},
		{name: "tool adaptation", run: func() error { return tools.RunOfflineDatabaseMigration(ctx, home) }},
		{name: "workflows", run: func() error { return workflows.RunOfflineDatabaseMigration(ctx, workspace) }},
		{name: "sessions", run: func() error { return memory.RunOfflineDatabaseMigration(ctx, workspace) }},
		{
			name: "eventing",
			run: func() error {
				return eventing.RunOfflineDatabaseMigration(
					ctx,
					filepath.Join(workspace, "eventing", "events.db"),
				)
			},
		},
		{
			name: "cron",
			run: func() error {
				service, err := cron.NewOfflineService(filepath.Join(workspace, "cron", "jobs.db"), nil)
				if service != nil {
					err = errors.Join(err, service.Close())
				}
				return err
			},
		},
		{name: "runtime state", run: func() error { return state.RunOfflineDatabaseMigration(workspace) }},
		{
			name: "account routing",
			run:  func() error { return accountrouter.RunOfflineDatabaseMigration(workspace) },
		},
		{
			name: "repository reviews",
			run:  func() error { return repoaudit.RunOfflineDatabaseMigration(ctx, workspace) },
		},
		{
			name: "repository evaluations",
			run:  func() error { return repoeval.RunOfflineDatabaseMigration(ctx, workspace) },
		},
		{
			name: "evolution",
			run: func() error {
				return evolution.RunOfflineDatabaseMigration(
					ctx,
					filepath.Join(workspace, "evolution", "store.db"),
				)
			},
		},
		{name: "local CI", run: func() error { return localci.RunOfflineDatabaseMigration(workspace) }},
		{
			name: "Seahorse",
			run: func() error {
				return seahorse.RunOfflineDatabaseMigration(
					filepath.Join(workspace, "sessions", "seahorse.db"),
				)
			},
		},
		{name: "WeCom", run: func() error { return wecom.RunOfflineDatabaseMigration(home) }},
		{name: "Weixin", run: func() error { return weixin.RunOfflineDatabaseMigration(home) }},
		{
			name: "git workspace inventory",
			run:  func() error { return gitworkspace.RunOfflineDatabaseMigration(ctx, filepath.Join(home, "git")) },
		},
		{
			name: "PR workspace checkpoints",
			run: func() error {
				return gateway.RunOfflinePRWorkspaceCheckpointMigration(ctx, filepath.Join(home, "checkpoints"))
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := test.run()
			if database.CodeOf(err) != database.CodeConflict {
				t.Fatalf("offline adapter error = %v, want structured Conflict", err)
			}
		})
	}
}
