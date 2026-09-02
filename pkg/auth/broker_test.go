//nolint:govet // Independent broker assertions intentionally reuse err.
package auth

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

func TestCredentialOperationsUseTypedBrokerClient(t *testing.T) {
	home := t.TempDir()
	poisonHome := t.TempDir()
	t.Setenv(config.EnvHome, poisonHome)
	handler := NewBrokerHandler(home)
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
	credential := &AuthCredential{Provider: "openai", AuthMethod: "oauth", AccessToken: "secret"}
	if err := SetCredential("openai:work", credential); err != nil {
		t.Fatal(err)
	}
	loaded, err := GetCredential("openai:work")
	if err != nil || loaded == nil || loaded.AccessToken != credential.AccessToken {
		t.Fatalf("broker credential = %#v, %v", loaded, err)
	}
	updated, err := UpdateCredential("openai:work", func(current *AuthCredential) (*AuthCredential, error) {
		current.AccessToken = "renewed"
		return current, nil
	})
	if err != nil || updated.AccessToken != "renewed" {
		t.Fatalf("broker update = %#v, %v", updated, err)
	}
	if err := DeleteCredential("openai:work"); err != nil {
		t.Fatal(err)
	}
	loaded, err = GetCredential("openai:work")
	if err != nil || loaded != nil {
		t.Fatalf("deleted broker credential = %#v, %v", loaded, err)
	}
	if _, err := os.Stat(filepath.Join(home, "auth.db")); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(poisonHome, "auth.db")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("runtime opened poison auth store: %v", err)
	}
}

func TestAuthBrokerPaginatesCredentialStore(t *testing.T) {
	home := t.TempDir()
	handler := NewBrokerHandler(home)
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
	for index := 0; index < 150; index++ {
		id := fmt.Sprintf("openai:page-%03d", index)
		if err := SetCredential(id, &AuthCredential{
			Provider: "openai", AuthMethod: "oauth", AccessToken: id,
		}); err != nil {
			t.Fatal(err)
		}
	}
	store, err := LoadStore()
	if err != nil {
		t.Fatal(err)
	}
	if len(store.Credentials) != 150 || store.Credentials["openai:page-149"].AccessToken != "openai:page-149" {
		t.Fatalf("paged auth store has %d credentials", len(store.Credentials))
	}
}
