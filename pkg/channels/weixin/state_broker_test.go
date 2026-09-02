package weixin

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/sipeed/picoclaw/pkg/database"
)

const brokerTestAccount = "0011223344556677"

func TestWeixinStateUsesTypedBrokerWithoutLocalFallback(t *testing.T) {
	home := t.TempDir()
	handler := NewBrokerHandler(home)
	server := startWeixinBroker(t, home, handler)
	client, connectErr := database.Connect(home)
	if connectErr != nil {
		t.Fatal(connectErr)
	}
	setWeixinBrokerClientForTest(t, client)
	decoyRoot := t.TempDir()
	cursorLocator := filepath.Join(decoyRoot, "sync", brokerTestAccount+".json")
	tokenLocator := filepath.Join(decoyRoot, "context-tokens", brokerTestAccount+".json")
	cursorStore, err := newWeixinStateStore(cursorLocator, weixinStateKindCursor)
	if err != nil {
		t.Fatal(err)
	}
	tokenStore, err := newWeixinStateStore(tokenLocator, weixinStateKindTokens)
	if err != nil {
		t.Fatal(err)
	}
	for _, store := range []*weixinStateStore{cursorStore, tokenStore} {
		if store.path != "" || store.root != "" || store.broker != client || store.StoreID() != WeixinStoreID {
			t.Fatalf("broker store leaked local authority: %#v", store)
		}
	}
	if saveErr := cursorStore.saveCursor(t.Context(), "cursor-1"); saveErr != nil {
		t.Fatal(saveErr)
	}
	if got, loadErr := cursorStore.loadCursor(t.Context()); loadErr != nil || got != "cursor-1" {
		t.Fatalf("cursor = %q, error=%v", got, loadErr)
	}
	if saveErr := tokenStore.saveTokens(t.Context(), map[string]string{"user-a": "token-a"}); saveErr != nil {
		t.Fatal(saveErr)
	}
	if saveErr := tokenStore.saveToken(t.Context(), "user-b", "token-b"); saveErr != nil {
		t.Fatal(saveErr)
	}
	tokens, err := tokenStore.loadTokens(t.Context())
	if err != nil || tokens["user-a"] != "token-a" || tokens["user-b"] != "token-b" {
		t.Fatalf("tokens = %#v, error=%v", tokens, err)
	}
	bulk := make(map[string]string, weixinBrokerTokenPageSize*2+7)
	for index := 0; index < weixinBrokerTokenPageSize*2+7; index++ {
		bulk[fmt.Sprintf("bulk-%03d", index)] = fmt.Sprintf("token-%03d", index)
	}
	if saveErr := tokenStore.saveTokens(t.Context(), bulk); saveErr != nil {
		t.Fatal(saveErr)
	}
	bulkLoaded, err := tokenStore.loadTokens(t.Context())
	if err != nil || len(bulkLoaded) != len(bulk) || bulkLoaded["bulk-134"] != "token-134" {
		t.Fatalf("paginated tokens = %d, error=%v", len(bulkLoaded), err)
	}
	if handler.store == nil || handler.store.retained == nil {
		t.Fatal("broker handler did not retain its local pool")
	}
	closeWeixinBroker(t, server)

	if err := cursorStore.saveCursor(t.Context(), "must-not-fallback"); err == nil {
		t.Fatal("broker loss fell back to local SQLite")
	}
	if _, err := tokenStore.loadTokens(t.Context()); err == nil {
		t.Fatal("broker loss loaded local token state")
	}
	if _, err := os.Stat(filepath.Join(decoyRoot, weixinStateDatabaseFilename)); !os.IsNotExist(err) {
		t.Fatalf("broker client created decoy database: %v", err)
	}
}

func TestWeixinBrokerMultiClientAndAccountConcurrency(t *testing.T) {
	home := t.TempDir()
	handler := NewBrokerHandler(home)
	server := startWeixinBroker(t, home, handler)
	defer closeWeixinBroker(t, server)
	firstClient, err := database.Connect(home)
	if err != nil {
		t.Fatal(err)
	}
	setWeixinBrokerClientForTest(t, firstClient)
	first, err := newWeixinStateStore(
		filepath.Join("sync", "aaaaaaaaaaaaaaaa.json"), weixinStateKindCursor,
	)
	if err != nil {
		t.Fatal(err)
	}
	secondClient, err := database.Connect(home)
	if err != nil {
		t.Fatal(err)
	}
	weixinBrokerClient = func() *database.Client { return secondClient }
	second, err := newWeixinStateStore(
		filepath.Join("sync", "bbbbbbbbbbbbbbbb.json"), weixinStateKindCursor,
	)
	if err != nil {
		t.Fatal(err)
	}

	const writes = 32
	var wait sync.WaitGroup
	errorsByWriter := make(chan error, writes)
	for index := 0; index < writes; index++ {
		wait.Add(1)
		go func(index int) {
			defer wait.Done()
			store := first
			if index%2 != 0 {
				store = second
			}
			errorsByWriter <- store.saveCursor(t.Context(), fmt.Sprintf("cursor-%02d", index))
		}(index)
	}
	wait.Wait()
	close(errorsByWriter)
	for err := range errorsByWriter {
		if err != nil {
			t.Errorf("concurrent cursor write: %v", err)
		}
	}
	firstCursor, firstErr := first.loadCursor(t.Context())
	secondCursor, secondErr := second.loadCursor(t.Context())
	if firstErr != nil || secondErr != nil || firstCursor == "" || secondCursor == "" || firstCursor == secondCursor {
		t.Fatalf(
			"account cursors: first=%q/%v second=%q/%v",
			firstCursor, firstErr, secondCursor, secondErr,
		)
	}
}

func TestWeixinBrokerStructuredInvalidAndNoRecursion(t *testing.T) {
	home := t.TempDir()
	handler := NewBrokerHandler(home)
	server := startWeixinBroker(t, home, handler)
	client, connectErr := database.Connect(home)
	if connectErr != nil {
		t.Fatal(connectErr)
	}
	setWeixinBrokerClientForTest(t, client)
	store, err := newWeixinStateStore(
		filepath.Join("context-tokens", "default.json"), weixinStateKindTokens,
	)
	if err != nil {
		t.Fatal(err)
	}
	if saveErr := store.saveToken(
		t.Context(),
		"bad\x00user",
		"token",
	); database.CodeOf(
		saveErr,
	) != database.CodeInvalid {
		t.Fatalf("invalid token error = %v, code=%s", saveErr, database.CodeOf(saveErr))
	}
	closeWeixinBroker(t, server)

	previous := weixinBrokerClient
	weixinBrokerClient = func() *database.Client { panic("broker handler consulted runtime client") }
	t.Cleanup(func() { weixinBrokerClient = previous })
	localHandler := NewBrokerHandler(t.TempDir())
	localStore, err := localHandler.open()
	if err != nil {
		t.Fatal(err)
	}
	if localStore.broker != nil || localStore.retained == nil {
		t.Fatalf("handler store = %#v", localStore)
	}
	if err := localHandler.Close(); err != nil {
		t.Fatal(err)
	}
}

func startWeixinBroker(t *testing.T, home string, handler *BrokerHandler) *database.Server {
	t.Helper()
	server, err := database.StartServer(context.Background(), database.ServerOptions{
		Home: home, Handler: handler, CloseHandler: handler.Close,
	})
	if err != nil {
		t.Fatal(err)
	}
	return server
}

func closeWeixinBroker(t *testing.T, server *database.Server) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := server.Close(ctx); err != nil {
		t.Fatal(err)
	}
}

func setWeixinBrokerClientForTest(t *testing.T, client *database.Client) {
	t.Helper()
	previous := weixinBrokerClient
	weixinBrokerClient = func() *database.Client { return client }
	t.Cleanup(func() { weixinBrokerClient = previous })
}
