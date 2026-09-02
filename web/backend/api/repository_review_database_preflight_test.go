package api

import (
	"context"
	"testing"
	"time"

	"github.com/sipeed/picoclaw/pkg/database"
)

func TestRepositoryReviewDatabasePreflightFailsClosedForMaintenance(t *testing.T) {
	home := t.TempDir()
	readiness := database.StoreMigrationRequired
	server, err := database.StartServer(context.Background(), database.ServerOptions{
		Home: home,
		StatusProvider: func(context.Context) ([]database.StoreStatus, error) {
			statuses := []database.StoreStatus{
				{ID: "workspace.workflows", Readiness: database.StoreReady},
				{ID: "workspace.repository-reviews", Readiness: readiness},
			}
			if readiness != database.StoreReady {
				statuses[1].Error = database.NewError(
					database.CodeMigrationRequired,
					"database migration is required",
				)
			}
			return statuses, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = server.Close(ctx)
	})
	client, err := database.Connect(home)
	if err != nil {
		t.Fatal(err)
	}
	handler := &Handler{databaseClient: client}
	if err := handler.preflightRepositoryReviewDatabases(
		context.Background(),
	); database.CodeOf(
		err,
	) != database.CodeMigrationRequired {
		t.Fatalf("maintenance preflight error = %v", err)
	}
	readiness = database.StoreReady
	if err := handler.preflightRepositoryReviewDatabases(context.Background()); err != nil {
		t.Fatalf("ready preflight error = %v", err)
	}
}
