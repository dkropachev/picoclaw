package tools

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/sipeed/picoclaw/internal/sqlitestore"
	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/database"
	"github.com/sipeed/picoclaw/pkg/providers"
)

func TestToolAdaptationBrokerRejectsStoreBeforeOpen(t *testing.T) {
	home := t.TempDir()
	handler := NewAdaptationBrokerHandler(home)
	payload, err := database.MarshalCanonical(adaptationProfileRequest{
		StoreID: "workspace.tool-adaptation",
		Profile: ToolAdaptationProfile{Provider: "openai", Model: "model"},
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = handler.Handle(t.Context(), database.Request{
		Domain: adaptationBrokerDomain, Version: adaptationBrokerVersion,
		Operation: "latest-observation", Payload: payload,
	})
	if database.CodeOf(err) != database.CodeInvalid {
		t.Fatalf("Handle() error = %v, want Invalid", err)
	}
	if handler.store.database != nil {
		t.Fatal("invalid store request opened broker database")
	}
	if _, err := os.Stat(filepath.Join(home, toolAdaptationDatabaseFilename)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("invalid store request touched database: %v", err)
	}
}

func TestToolAdaptationRuntimeUsesBrokerWithoutLocalFallback(t *testing.T) {
	brokerHome := t.TempDir()
	poisonHome := t.TempDir()
	t.Setenv(config.EnvHome, poisonHome)
	handler := NewAdaptationBrokerHandler(brokerHome)
	server, err := database.StartServer(context.Background(), database.ServerOptions{
		Home: brokerHome, Handler: handler, CloseHandler: handler.Close,
	})
	if err != nil {
		t.Fatal(err)
	}
	client, err := database.Connect(brokerHome)
	if err != nil {
		t.Fatal(err)
	}
	database.InstallProcessClient(client)
	t.Cleanup(func() {
		database.InstallProcessClient(nil)
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = server.Close(ctx)
	})
	profile := ToolAdaptationProfile{Provider: "openai", Model: "model"}
	observed, ok := ObserveToolAdaptationCache(
		profile, config.ToolSurfacePicoClaw, nil,
		&providers.UsageInfo{PromptTokens: minCacheSniffPromptTokens, CachedTokens: 32},
	)
	if !ok || observed.Profile != profile {
		t.Fatalf("ObserveToolAdaptationCache() = %#v, %v", observed, ok)
	}
	latest, ok := LatestToolAdaptationObservation(profile)
	if !ok || latest.ToolSchemaHash != observed.ToolSchemaHash {
		t.Fatalf("LatestToolAdaptationObservation() = %#v, %v", latest, ok)
	}
	if _, err := os.Stat(filepath.Join(poisonHome, toolAdaptationDatabaseFilename)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("runtime opened poison adaptation store: %v", err)
	}
	if _, err := os.Stat(filepath.Join(brokerHome, toolAdaptationDatabaseFilename)); err != nil {
		t.Fatalf("broker adaptation store missing: %v", err)
	}
}

func TestToolAdaptationBrokerDoesNotReportFailedMutationAsCommitted(t *testing.T) {
	previous := adaptationOpenSQLite
	adaptationOpenSQLite = func(context.Context, string, sqlitestore.Options) (*sql.DB, error) {
		return nil, os.ErrPermission
	}
	t.Cleanup(func() { adaptationOpenSQLite = previous })
	handler := NewAdaptationBrokerHandler(t.TempDir())
	payload, err := database.MarshalCanonical(adaptationObservationRequest{
		StoreID: ToolAdaptationStoreID,
		Profile: ToolAdaptationProfile{Provider: "openai", Model: "model"},
		Usage:   &providers.UsageInfo{PromptTokens: minCacheSniffPromptTokens},
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = handler.Handle(t.Context(), database.Request{
		Domain: adaptationBrokerDomain, Version: adaptationBrokerVersion,
		Operation: "observe-cache", Payload: payload,
	})
	if database.CodeOf(err) != database.CodeOutcomeUnknown {
		t.Fatalf("failed mutation error = %v, want OutcomeUnknown", err)
	}
}
