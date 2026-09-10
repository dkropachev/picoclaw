package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func main() {
	if err := run(context.Background()); err != nil {
		log.Fatal(err)
	}
}

func run(ctx context.Context) error {
	listenAddress := envString("STREAMABLE_LISTEN_ADDRESS", "127.0.0.1:8080")
	if err := validateListenAddress(listenAddress); err != nil {
		return fmt.Errorf("STREAMABLE_LISTEN_ADDRESS must use IPv4 loopback %q: %w", listenAddress, err)
	}
	listener, err := net.Listen("tcp4", listenAddress)
	if err != nil {
		return fmt.Errorf("listen on %s: %w", listenAddress, err)
	}
	defer listener.Close()
	if err = publishReadyAddress(listener.Addr().String()); err != nil {
		return fmt.Errorf("publish ready address: %w", err)
	}

	server := mcp.NewServer(&mcp.Implementation{
		Name:    "picoclaw-integration-streamable-server",
		Version: "1.0.0",
	}, nil)

	mcp.AddTool(server, &mcp.Tool{
		Name:        "echo",
		Description: "Echo back the provided message",
	}, func(ctx context.Context, req *mcp.CallToolRequest, args map[string]any) (*mcp.CallToolResult, any, error) {
		message, _ := args["message"].(string)
		return &mcp.CallToolResult{
			Content: []mcp.Content{
				&mcp.TextContent{Text: message},
			},
		}, nil, nil
	})

	streamable := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server {
		return server
	}, &mcp.StreamableHTTPOptions{
		JSONResponse: envBool("STREAMABLE_JSON_RESPONSE", true),
	})

	mux := http.NewServeMux()
	mux.Handle("/mcp", streamable)
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})

	srv := &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}
	serveDone := make(chan struct{})
	defer close(serveDone)
	go func() {
		select {
		case <-ctx.Done():
			_ = srv.Close()
		case <-serveDone:
		}
	}()

	log.Printf("streamable MCP integration server listening on %s", listener.Addr())
	if err = srv.Serve(listener); err != nil && err != http.ErrServerClosed {
		return err
	}
	return nil
}

func envString(name, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(name)); value != "" {
		return value
	}
	return fallback
}

func validateListenAddress(address string) error {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return fmt.Errorf("parse listen address: %w", err)
	}
	if host != "127.0.0.1" {
		return errors.New("listen address is not IPv4 loopback")
	}
	return nil
}

func publishReadyAddress(address string) error {
	path := strings.TrimSpace(os.Getenv("STREAMABLE_READY_FILE"))
	if path == "" {
		return nil
	}
	temporary := path + ".tmp"
	if err := os.WriteFile(temporary, []byte(address+"\n"), 0o600); err != nil {
		return err
	}
	if err := os.Rename(temporary, path); err != nil {
		_ = os.Remove(temporary)
		return fmt.Errorf("publish readiness: %w", err)
	}
	return nil
}

func envBool(name string, fallback bool) bool {
	value := strings.TrimSpace(strings.ToLower(os.Getenv(name)))
	if value == "" {
		return fallback
	}

	switch value {
	case "1", "true", "yes", "on":
		return true
	case "0", "false", "no", "off":
		return false
	default:
		return fallback
	}
}
