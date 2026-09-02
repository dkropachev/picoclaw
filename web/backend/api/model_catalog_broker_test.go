package api

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/database"
)

func TestModelCatalogBrokerPaginatesLargeModelList(t *testing.T) {
	home := t.TempDir()
	handler := NewModelCatalogBrokerHandler(home)
	server, err := database.StartServer(context.Background(), database.ServerOptions{
		Home: home, Handler: handler, CloseHandler: handler.Close,
	})
	if err != nil {
		t.Fatal(err)
	}
	client, err := database.Connect(home)
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
	models := make([]CatalogModel, 300)
	for index := range models {
		models[index] = CatalogModel{ID: fmt.Sprintf("model-%03d", index)}
	}
	if saveErr := SaveCatalog("openai", "", "page-key", models); saveErr != nil {
		t.Fatal(saveErr)
	}
	store, err := loadCatalogs()
	if err != nil {
		t.Fatal(err)
	}
	if len(store.Entries) != 1 {
		t.Fatalf("catalog count = %d, want 1", len(store.Entries))
	}
	for _, entry := range store.Entries {
		if len(entry.Models) != len(models) {
			t.Fatalf("paged models = %d, want %d", len(entry.Models), len(models))
		}
		if entry.Models[299].ID != "model-299" {
			t.Fatalf("last paged model = %#v", entry.Models[299])
		}
	}
}

func TestModelCatalogRuntimeUsesTypedBrokerWithoutPathFallback(t *testing.T) {
	brokerHome := t.TempDir()
	poisonHome := t.TempDir()
	t.Setenv(config.EnvHome, poisonHome)
	handler := NewModelCatalogBrokerHandler(brokerHome)
	server, err := database.StartServer(context.Background(), database.ServerOptions{
		Home: brokerHome, Handler: handler, CloseHandler: handler.Close,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		database.InstallProcessClient(nil)
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = server.Close(ctx)
	})
	client, err := database.Connect(brokerHome)
	if err != nil {
		t.Fatal(err)
	}
	database.InstallProcessClient(client)

	if saveErr := SaveCatalog(
		"openai", "https://example.invalid/v1/", "secret-key",
		[]CatalogModel{{ID: "model-1", OwnedBy: "owner"}},
	); saveErr != nil {
		t.Fatalf("SaveCatalog() error = %v", saveErr)
	}
	store, err := loadCatalogs()
	if err != nil {
		t.Fatalf("loadCatalogs() error = %v", err)
	}
	if len(store.Entries) != 1 {
		t.Fatalf("catalog entries = %d, want 1", len(store.Entries))
	}
	for id := range store.Entries {
		deleted, err := deleteCatalog(t.Context(), id)
		if err != nil || !deleted {
			t.Fatalf("deleteCatalog() = %v, %v; want true, nil", deleted, err)
		}
	}
	if _, err := os.Stat(filepath.Join(poisonHome, catalogDatabaseFilename)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("runtime created poison local database: %v", err)
	}
	if _, err := os.Stat(filepath.Join(brokerHome, catalogDatabaseFilename)); err != nil {
		t.Fatalf("broker database missing: %v", err)
	}
}

func TestModelCatalogBrokerRejectsWrongStoreID(t *testing.T) {
	handler := NewModelCatalogBrokerHandler(t.TempDir())
	t.Cleanup(func() { _ = handler.Close() })
	payload, err := database.MarshalCanonical(modelCatalogPageRequest{
		StoreID: "workspace.model-catalogs",
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = handler.Handle(t.Context(), database.Request{
		Domain: modelCatalogBrokerDomain, Version: modelCatalogBrokerVersion,
		Operation: modelCatalogOperationLoadPage, Payload: payload,
	})
	if database.CodeOf(err) != database.CodeInvalid {
		t.Fatalf("Handle() error = %v, want Invalid", err)
	}
}
