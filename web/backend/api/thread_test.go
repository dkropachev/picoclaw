package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/sipeed/picoclaw/pkg/config"
	threadstore "github.com/sipeed/picoclaw/pkg/threads"
)

func TestHandleThreads_CreateListAndOpenSession(t *testing.T) {
	configPath, cleanup := setupOAuthTestEnv(t)
	defer cleanup()

	cfg, err := config.LoadConfig(configPath)
	if err != nil {
		t.Fatalf("LoadConfig() error = %v", err)
	}
	cfg.Agents.Defaults.Workspace = filepath.Join(t.TempDir(), "workspace")
	if err := config.SaveConfig(configPath, cfg); err != nil {
		t.Fatalf("SaveConfig() error = %v", err)
	}

	h := NewHandler(configPath)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	body := []byte(`{
		"type": "coding",
		"title": "Implement threads",
		"source_query": "code in /extra/dkropachev/picoclaw repo: git@github.com:dkropachev/picoclaw.git",
		"context": {"branch": "main"}
	}`)
	createRec := httptest.NewRecorder()
	createReq := httptest.NewRequest(http.MethodPost, "/api/threads", bytes.NewReader(body))
	mux.ServeHTTP(createRec, createReq)
	if createRec.Code != http.StatusCreated {
		t.Fatalf("create status = %d, body=%s", createRec.Code, createRec.Body.String())
	}

	var created threadstore.Thread
	if err := json.Unmarshal(createRec.Body.Bytes(), &created); err != nil {
		t.Fatalf("Unmarshal(create) error = %v", err)
	}
	if created.ID == "" {
		t.Fatal("created thread ID is empty")
	}
	if created.Type != threadstore.TypeCoding {
		t.Fatalf("created.Type = %q", created.Type)
	}

	listRec := httptest.NewRecorder()
	listReq := httptest.NewRequest(http.MethodGet, "/api/threads?query=/extra/dkropachev/picoclaw&type=coding", nil)
	mux.ServeHTTP(listRec, listReq)
	if listRec.Code != http.StatusOK {
		t.Fatalf("list status = %d, body=%s", listRec.Code, listRec.Body.String())
	}

	var items []threadstore.Thread
	if err := json.Unmarshal(listRec.Body.Bytes(), &items); err != nil {
		t.Fatalf("Unmarshal(list) error = %v", err)
	}
	if len(items) != 1 || items[0].ID != created.ID {
		t.Fatalf("items = %#v, want created thread", items)
	}
	if got := items[0].Context["repo"]; got != "git@github.com:dkropachev/picoclaw.git" {
		t.Fatalf("repo context = %q", got)
	}

	detailRec := httptest.NewRecorder()
	detailReq := httptest.NewRequest(http.MethodGet, "/api/sessions/"+created.ID, nil)
	mux.ServeHTTP(detailRec, detailReq)
	if detailRec.Code != http.StatusOK {
		t.Fatalf("detail status = %d, body=%s", detailRec.Code, detailRec.Body.String())
	}
	var detail struct {
		Messages []any  `json:"messages"`
		Summary  string `json:"summary"`
	}
	if err := json.Unmarshal(detailRec.Body.Bytes(), &detail); err != nil {
		t.Fatalf("Unmarshal(detail) error = %v", err)
	}
	if len(detail.Messages) != 0 {
		t.Fatalf("len(detail.Messages) = %d, want empty thread", len(detail.Messages))
	}
	if detail.Summary != "Implement threads" {
		t.Fatalf("detail.Summary = %q, want thread title", detail.Summary)
	}

	threadBySessionRec := httptest.NewRecorder()
	threadBySessionReq := httptest.NewRequest(http.MethodGet, "/api/threads/"+created.UISessionID, nil)
	mux.ServeHTTP(threadBySessionRec, threadBySessionReq)
	if threadBySessionRec.Code != http.StatusOK {
		t.Fatalf(
			"thread by session status = %d, body=%s",
			threadBySessionRec.Code,
			threadBySessionRec.Body.String(),
		)
	}
}

func TestHandleThreads_SearchContextFilter(t *testing.T) {
	configPath, cleanup := setupOAuthTestEnv(t)
	defer cleanup()

	cfg, err := config.LoadConfig(configPath)
	if err != nil {
		t.Fatalf("LoadConfig() error = %v", err)
	}
	cfg.Agents.Defaults.Workspace = filepath.Join(t.TempDir(), "workspace")
	if err := config.SaveConfig(configPath, cfg); err != nil {
		t.Fatalf("SaveConfig() error = %v", err)
	}

	store := threadstore.NewStoreFromWorkspace(cfg.Agents.Defaults.Workspace)
	if _, err := store.CreatePicoThread(nil, cfg, threadstore.CreateRequest{
		Type:    threadstore.TypeReviewing,
		Title:   "Review PR 42",
		Context: map[string]string{"pr": "42"},
	}); err != nil {
		t.Fatalf("CreatePicoThread(review) error = %v", err)
	}
	if _, err := store.CreatePicoThread(nil, cfg, threadstore.CreateRequest{
		Type:    threadstore.TypeCoding,
		Title:   "Implement PR 43 follow-up",
		Context: map[string]string{"pr": "43"},
	}); err != nil {
		t.Fatalf("CreatePicoThread(coding) error = %v", err)
	}

	h := NewHandler(configPath)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/threads?query=pr:42", nil)
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
	}

	var items []threadstore.Thread
	if err := json.Unmarshal(rec.Body.Bytes(), &items); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}
	if len(items) != 1 || items[0].Context["pr"] != "42" {
		t.Fatalf("items = %#v, want only PR 42", items)
	}
}
