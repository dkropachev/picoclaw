package wecom

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sipeed/picoclaw/pkg/database"
)

func TestReqIDStoreUsesTypedBrokerWithoutLocalFallback(t *testing.T) {
	home := t.TempDir()
	handler := NewBrokerHandler(home)
	server := startWecomBroker(t, home, handler)
	client, err := database.Connect(home)
	if err != nil {
		t.Fatal(err)
	}
	setWecomBrokerClientForTest(t, client)
	decoy := filepath.Join(t.TempDir(), "must-not-open.json")
	store := newReqIDStore(decoy)
	if err := store.initializationError(); err != nil {
		t.Fatal(err)
	}
	if store.path != "" || store.broker != client || store.StoreID() != WeComStoreID {
		t.Fatalf("broker store leaked local authority: %#v", store)
	}
	if err := store.Put("chat", "request", 7, time.Hour); err != nil {
		t.Fatal(err)
	}
	if err := store.Put("bad\x00chat", "request", 1, time.Hour); database.CodeOf(err) != database.CodeInvalid {
		t.Fatalf("invalid route error = %v, code=%s", err, database.CodeOf(err))
	}
	route, found := store.Get("chat")
	if !found || route.ReqID != "request" || route.ChatType != 7 {
		t.Fatalf("broker route = %#v, found=%v", route, found)
	}
	if err := store.Delete("chat"); err != nil {
		t.Fatal(err)
	}
	if _, found := store.Get("chat"); found {
		t.Fatal("deleted broker route still exists")
	}
	if handler.store == nil || handler.store.retained == nil {
		t.Fatal("broker handler did not retain its local pool")
	}
	closeWecomBroker(t, server)

	if err := store.Put("chat-2", "request-2", 1, time.Hour); err == nil {
		t.Fatal("broker loss fell back to local SQLite")
	}
	if _, found := store.Get("chat-2"); found {
		t.Fatal("broker loss returned a local route")
	}
	if _, err := os.Stat(strings.TrimSuffix(decoy, filepath.Ext(decoy)) + ".db"); !os.IsNotExist(err) {
		t.Fatalf("broker client created decoy database: %v", err)
	}
}

func TestReqIDBrokerMultiClientConcurrency(t *testing.T) {
	home := t.TempDir()
	handler := NewBrokerHandler(home)
	server := startWecomBroker(t, home, handler)
	defer closeWecomBroker(t, server)
	firstClient, connectErr := database.Connect(home)
	if connectErr != nil {
		t.Fatal(connectErr)
	}
	setWecomBrokerClientForTest(t, firstClient)
	first := newReqIDStore("")
	if err := first.initializationError(); err != nil {
		t.Fatal(err)
	}
	secondClient, err := database.Connect(home)
	if err != nil {
		t.Fatal(err)
	}
	wecomBrokerClient = func() *database.Client { return secondClient }
	second := newReqIDStore("")
	if err := second.initializationError(); err != nil {
		t.Fatal(err)
	}

	const writers = 32
	var wait sync.WaitGroup
	errorsByWriter := make(chan error, writers)
	for index := 0; index < writers; index++ {
		wait.Add(1)
		go func(index int) {
			defer wait.Done()
			store := first
			if index%2 != 0 {
				store = second
			}
			errorsByWriter <- store.Put(
				fmt.Sprintf("chat-%02d", index), fmt.Sprintf("request-%02d", index), uint32(index), time.Hour,
			)
		}(index)
	}
	wait.Wait()
	close(errorsByWriter)
	for err := range errorsByWriter {
		if err != nil {
			t.Errorf("concurrent route write: %v", err)
		}
	}
	for index := 0; index < writers; index++ {
		route, found := first.Get(fmt.Sprintf("chat-%02d", index))
		if !found || route.ReqID != fmt.Sprintf("request-%02d", index) {
			t.Errorf("route %d = %#v, found=%v", index, route, found)
		}
	}
}

func TestReqIDBrokerHandlerCannotReenterRuntimeClient(t *testing.T) {
	previous := wecomBrokerClient
	wecomBrokerClient = func() *database.Client { panic("broker handler consulted runtime client") }
	t.Cleanup(func() { wecomBrokerClient = previous })
	handler := NewBrokerHandler(t.TempDir())
	store, err := handler.open()
	if err != nil {
		t.Fatal(err)
	}
	if store.broker != nil || store.retained == nil {
		t.Fatalf("handler store = %#v", store)
	}
	if err := handler.Close(); err != nil {
		t.Fatal(err)
	}
}

func startWecomBroker(t *testing.T, home string, handler *BrokerHandler) *database.Server {
	t.Helper()
	server, err := database.StartServer(context.Background(), database.ServerOptions{
		Home: home, Handler: handler, CloseHandler: handler.Close,
	})
	if err != nil {
		t.Fatal(err)
	}
	return server
}

func closeWecomBroker(t *testing.T, server *database.Server) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := server.Close(ctx); err != nil {
		t.Fatal(err)
	}
}

func setWecomBrokerClientForTest(t *testing.T, client *database.Client) {
	t.Helper()
	previous := wecomBrokerClient
	wecomBrokerClient = func() *database.Client { return client }
	t.Cleanup(func() { wecomBrokerClient = previous })
}
