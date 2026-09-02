package database

import (
	"context"
	"os"
	"testing"
	"time"
)

func TestSupervisorEpochPersistsUntilServerReplacement(t *testing.T) {
	home := t.TempDir()
	first, err := StartServer(context.Background(), ServerOptions{Home: home})
	if err != nil {
		t.Fatal(err)
	}
	client, err := Connect(home)
	if err != nil {
		t.Fatal(err)
	}
	firstEpoch := client.Epoch()
	for range 2 {
		ping, pingErr := client.Ping(context.Background())
		if pingErr != nil || ping.Epoch != firstEpoch {
			t.Fatalf("stable supervisor ping = %#v, %v", ping, pingErr)
		}
	}
	closeServer(t, first)

	second, err := StartServer(context.Background(), ServerOptions{
		Home: home,
		Handler: HandlerFunc(func(context.Context, Request) (any, error) {
			return EmptyPayload{}, nil
		}),
	})
	if err != nil {
		t.Fatal(err)
	}
	defer closeServer(t, second)
	if second.Manifest().Epoch == firstEpoch {
		t.Fatal("replacement supervisor reused broker epoch")
	}
	if _, err := client.Ping(context.Background()); err != nil {
		t.Fatalf("discovering client did not reconnect after replacement: %v", err)
	}
	if client.Epoch() != second.Manifest().Epoch {
		t.Fatalf("refreshed client epoch = %q, want %q", client.Epoch(), second.Manifest().Epoch)
	}
	var mutationResponse EmptyPayload
	if err := client.CallWithOptions(
		context.Background(),
		"test",
		1,
		"mutate",
		EmptyPayload{},
		&mutationResponse,
		CallOptions{Mutation: true},
	); err != nil {
		t.Fatalf("mutation did not bind replacement epoch before dispatch: %v", err)
	}
}

func TestSupervisorBootstrapIsOneTimeAndHomeBound(t *testing.T) {
	home := t.TempDir()
	token, err := randomHex(tokenBytes)
	if err != nil {
		t.Fatal(err)
	}
	path, err := prepareSupervisorBootstrap(home, token)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatal(err)
	}
	t.Setenv(supervisorBootstrapEnvironment, token)
	if !ConsumeSupervisorBootstrap(home) {
		t.Fatal("valid supervisor bootstrap was rejected")
	}
	t.Setenv(supervisorBootstrapEnvironment, token)
	if ConsumeSupervisorBootstrap(home) {
		t.Fatal("supervisor bootstrap replay was accepted")
	}
	other := t.TempDir()
	otherToken, _ := randomHex(tokenBytes)
	if _, err := prepareSupervisorBootstrap(home, otherToken); err != nil {
		t.Fatal(err)
	}
	t.Setenv(supervisorBootstrapEnvironment, otherToken)
	if ConsumeSupervisorBootstrap(other) {
		t.Fatal("supervisor bootstrap crossed canonical homes")
	}
}

func closeServer(t *testing.T, server *Server) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := server.Close(ctx); err != nil {
		t.Fatal(err)
	}
}
