package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/memory"
	"github.com/sipeed/picoclaw/pkg/providers"
	"github.com/sipeed/picoclaw/pkg/session"
	"github.com/sipeed/picoclaw/pkg/utils"
)

func sessionsTestDir(t *testing.T, configPath string) string {
	t.Helper()

	cfg, err := config.LoadConfig(configPath)
	if err != nil {
		t.Fatalf("LoadConfig() error = %v", err)
	}

	dir := filepath.Join(cfg.Agents.Defaults.Workspace, "sessions")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	return dir
}

func writeSessionMessageFile(t *testing.T, path string, messages ...providers.Message) {
	t.Helper()

	data := make([]byte, 0, len(messages)*128)
	for _, message := range messages {
		encoded, err := json.Marshal(message)
		if err != nil {
			t.Fatalf("Marshal(message) error = %v", err)
		}
		data = append(data, encoded...)
		data = append(data, '\n')
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("WriteFile(%s) error = %v", filepath.Base(path), err)
	}
}

func writeSessionMetaFile(t *testing.T, path string, meta memory.SessionMeta) {
	t.Helper()

	data, err := json.Marshal(meta)
	if err != nil {
		t.Fatalf("Marshal(meta) error = %v", err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("WriteFile(%s) error = %v", filepath.Base(path), err)
	}
}

func assertVisibleToolCallMessage(
	t *testing.T,
	msg sessionChatMessage,
	toolName string,
) utils.VisibleToolCall {
	t.Helper()

	if msg.Role != "assistant" || msg.Kind != "tool_calls" {
		t.Fatalf("message = %#v, want assistant/tool_calls", msg)
	}
	if len(msg.ToolCalls) != 1 {
		t.Fatalf("len(message.ToolCalls) = %d, want 1", len(msg.ToolCalls))
	}
	if got := msg.ToolCalls[0].Function; got == nil || got.Name != toolName {
		t.Fatalf("tool call = %#v, want function %q", msg.ToolCalls[0], toolName)
	}
	return msg.ToolCalls[0]
}

func TestHandleListSessions_JSONLStorage(t *testing.T) {
	configPath, cleanup := setupOAuthTestEnv(t)
	defer cleanup()

	dir := sessionsTestDir(t, configPath)
	store, storeErr := memory.NewJSONLStore(dir)
	if storeErr != nil {
		t.Fatalf("NewJSONLStore() error = %v", storeErr)
	}

	sessionKey := legacyPicoSessionPrefix + "history-jsonl"
	if err := store.AddFullMessage(nil, sessionKey, providers.Message{
		Role:    "user",
		Content: "Explain why the history API is empty after migration.",
	}); err != nil {
		t.Fatalf("AddFullMessage(user) error = %v", err)
	}
	if err := store.AddFullMessage(nil, sessionKey, providers.Message{
		Role:    "assistant",
		Content: "Because the API still reads only legacy JSON session files.",
	}); err != nil {
		t.Fatalf("AddFullMessage(assistant) error = %v", err)
	}
	if err := store.AddFullMessage(nil, sessionKey, providers.Message{
		Role:    "tool",
		Content: "ignored",
	}); err != nil {
		t.Fatalf("AddFullMessage(tool) error = %v", err)
	}
	if err := store.SetSummary(nil, sessionKey, "JSONL-backed session"); err != nil {
		t.Fatalf("SetSummary() error = %v", err)
	}

	h := NewHandler(configPath)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/sessions", nil)
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}

	var items []sessionListItem
	if err := json.Unmarshal(rec.Body.Bytes(), &items); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("len(items) = %d, want 1", len(items))
	}
	if items[0].ID != "history-jsonl" {
		t.Fatalf("items[0].ID = %q, want %q", items[0].ID, "history-jsonl")
	}
	if items[0].MessageCount != 2 {
		t.Fatalf("items[0].MessageCount = %d, want 2", items[0].MessageCount)
	}
	if items[0].Title != "Explain why the history API is empty after migration." {
		t.Fatalf(
			"items[0].Title = %q, want %q",
			items[0].Title,
			"Explain why the history API is empty after migration.",
		)
	}
	if items[0].Preview != "Explain why the history API is empty after migration." {
		t.Fatalf("items[0].Preview = %q", items[0].Preview)
	}
}

func TestHandleSessions_UsesCommittedHistorySlot(t *testing.T) {
	configPath, cleanup := setupOAuthTestEnv(t)
	defer cleanup()

	dir := sessionsTestDir(t, configPath)
	sessionKey := legacyPicoSessionPrefix + "committed-slot"
	base := filepath.Join(dir, sanitizeSessionKey(sessionKey))
	writeSessionMessageFile(t, base+".jsonl", providers.Message{
		Role: "user", Content: "poisoned legacy history",
	})
	writeSessionMessageFile(t, base+".history-a",
		providers.Message{Role: "user", Content: "committed active history"},
		providers.Message{Role: "assistant", Content: "committed active reply"},
	)
	active, err := os.OpenFile(base+".history-a", os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatalf("OpenFile(active slot) error = %v", err)
	}
	if _, err := active.WriteString("not-json\n"); err != nil {
		_ = active.Close()
		t.Fatalf("append corrupt active record error = %v", err)
	}
	if err := active.Close(); err != nil {
		t.Fatalf("Close(active slot) error = %v", err)
	}
	writeSessionMessageFile(t, base+".history-b", providers.Message{
		Role: "user", Content: "poisoned inactive history",
	})
	writeSessionMetaFile(t, base+".meta.json", memory.SessionMeta{
		Key:         sessionKey,
		Summary:     "committed summary",
		Count:       3,
		HistorySlot: "a",
	})

	activeTime := time.Date(2026, time.August, 2, 13, 14, 15, 0, time.UTC)
	if err := os.Chtimes(base+".history-a", activeTime, activeTime); err != nil {
		t.Fatalf("Chtimes(active slot) error = %v", err)
	}
	writeSessionMessageFile(
		t,
		filepath.Join(dir, sanitizeSessionKey(legacyPicoSessionPrefix+"orphan")+".history-a"),
		providers.Message{Role: "user", Content: "orphaned slot"},
	)

	h := NewHandler(configPath)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	listRec := httptest.NewRecorder()
	listReq := httptest.NewRequest(http.MethodGet, "/api/sessions", nil)
	mux.ServeHTTP(listRec, listReq)
	if listRec.Code != http.StatusOK {
		t.Fatalf("list status = %d, body=%s", listRec.Code, listRec.Body.String())
	}
	var items []sessionListItem
	if err := json.Unmarshal(listRec.Body.Bytes(), &items); err != nil {
		t.Fatalf("Unmarshal(list) error = %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("list items = %#v, want only committed session", items)
	}
	createdAt, createdErr := time.Parse(time.RFC3339, items[0].Created)
	updatedAt, updatedErr := time.Parse(time.RFC3339, items[0].Updated)
	if createdErr != nil || updatedErr != nil {
		t.Fatalf("list timestamps = (%q, %q)", items[0].Created, items[0].Updated)
	}
	if items[0].ID != "committed-slot" || items[0].MessageCount != 2 ||
		items[0].Title != "committed active history" ||
		items[0].Preview != "committed active history" ||
		!createdAt.Equal(activeTime) || !updatedAt.Equal(activeTime) {
		t.Fatalf("committed session list item = %#v", items[0])
	}

	detailRec := httptest.NewRecorder()
	detailReq := httptest.NewRequest(http.MethodGet, "/api/sessions/committed-slot", nil)
	mux.ServeHTTP(detailRec, detailReq)
	if detailRec.Code != http.StatusOK {
		t.Fatalf("detail status = %d, body=%s", detailRec.Code, detailRec.Body.String())
	}
	var detail struct {
		Summary  string               `json:"summary"`
		Messages []sessionChatMessage `json:"messages"`
	}
	if err := json.Unmarshal(detailRec.Body.Bytes(), &detail); err != nil {
		t.Fatalf("Unmarshal(detail) error = %v", err)
	}
	if detail.Summary != "committed summary" || len(detail.Messages) != 2 ||
		detail.Messages[0].Content != "committed active history" ||
		detail.Messages[1].Content != "committed active reply" {
		t.Fatalf("committed session detail = %#v", detail)
	}
	if strings.Contains(detailRec.Body.String(), "poisoned") ||
		strings.Contains(listRec.Body.String(), "poisoned") ||
		strings.Contains(listRec.Body.String(), "orphaned") {
		t.Fatal("session API exposed legacy, inactive, or orphan slot history")
	}
}

func TestHandleSessions_PrefersPromotedCanonicalAndDeletesOwnedShadow(t *testing.T) {
	configPath, cleanup := setupOAuthTestEnv(t)
	defer cleanup()

	dir := sessionsTestDir(t, configPath)
	store, err := memory.NewJSONLStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	const sessionID = "promoted-canonical"
	legacyKey := "agent:reviewer:pico:work:direct:pico:" + sessionID
	scope := session.SessionScope{
		Version:    session.ScopeVersionV1,
		AgentID:    "reviewer",
		Channel:    "pico",
		Account:    "work",
		Dimensions: []string{"sender"},
		Values:     map[string]string{"sender": "pico:" + sessionID},
	}
	canonicalKey := session.BuildSessionKey(scope)
	rawScope, err := json.Marshal(scope)
	if err != nil {
		t.Fatal(err)
	}
	if addErr := store.AddMessage(
		context.Background(),
		legacyKey,
		"user",
		"stale legacy preview",
	); addErr != nil {
		t.Fatal(addErr)
	}
	if upsertErr := store.UpsertSessionMeta(
		context.Background(),
		canonicalKey,
		rawScope,
		[]string{legacyKey},
	); upsertErr != nil {
		t.Fatal(upsertErr)
	}
	if promoted, promoteErr := store.PromoteAliasHistory(
		context.Background(),
		canonicalKey,
		rawScope,
		[]string{legacyKey},
	); promoteErr != nil || !promoted {
		t.Fatalf("PromoteAliasHistory() = (%v, %v)", promoted, promoteErr)
	}
	_, _, before, found, err := store.ReadSessionSnapshot(context.Background(), canonicalKey)
	if err != nil || !found {
		t.Fatalf("ReadSessionSnapshot() = (found=%v, err=%v)", found, err)
	}
	if err := store.ReplaceSessionSnapshot(context.Background(), memory.SessionSnapshotReplacement{
		Key:              canonicalKey,
		History:          []providers.Message{{Role: "user", Content: "new canonical preview"}},
		Summary:          "new canonical summary",
		Scope:            rawScope,
		Aliases:          []string{legacyKey},
		ExpectedRevision: before.Revision,
	}); err != nil {
		t.Fatal(err)
	}

	h := NewHandler(configPath)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	listRec := httptest.NewRecorder()
	mux.ServeHTTP(listRec, httptest.NewRequest(http.MethodGet, "/api/sessions", nil))
	if listRec.Code != http.StatusOK {
		t.Fatalf("list status = %d, body=%s", listRec.Code, listRec.Body.String())
	}
	var items []sessionListItem
	if err := json.Unmarshal(listRec.Body.Bytes(), &items); err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].ID != sessionID ||
		items[0].Preview != "new canonical preview" {
		t.Fatalf("promoted list items = %#v", items)
	}

	detailRec := httptest.NewRecorder()
	mux.ServeHTTP(
		detailRec,
		httptest.NewRequest(http.MethodGet, "/api/sessions/"+sessionID, nil),
	)
	if detailRec.Code != http.StatusOK ||
		!strings.Contains(detailRec.Body.String(), "new canonical preview") ||
		strings.Contains(detailRec.Body.String(), "stale legacy preview") {
		t.Fatalf("promoted detail status=%d body=%s", detailRec.Code, detailRec.Body.String())
	}

	deleteRec := httptest.NewRecorder()
	mux.ServeHTTP(
		deleteRec,
		httptest.NewRequest(http.MethodDelete, "/api/sessions/"+sessionID, nil),
	)
	if deleteRec.Code != http.StatusNoContent {
		t.Fatalf("delete status = %d, body=%s", deleteRec.Code, deleteRec.Body.String())
	}
	for _, key := range []string{canonicalKey, legacyKey} {
		if _, _, _, found, readErr := store.ReadSessionSnapshot(
			context.Background(),
			key,
		); readErr != nil || found {
			t.Fatalf("deleted key %q = (found=%v, err=%v)", key, found, readErr)
		}
	}
	listAfterDelete := httptest.NewRecorder()
	mux.ServeHTTP(listAfterDelete, httptest.NewRequest(http.MethodGet, "/api/sessions", nil))
	if listAfterDelete.Code != http.StatusOK || listAfterDelete.Body.String() != "[]\n" {
		t.Fatalf(
			"list after delete status=%d body=%s",
			listAfterDelete.Code,
			listAfterDelete.Body.String(),
		)
	}
}

func TestHandleListSessions_TransientThoughtDoesNotInflateMessageCount(t *testing.T) {
	configPath, cleanup := setupOAuthTestEnv(t)
	defer cleanup()

	dir := sessionsTestDir(t, configPath)
	sessionKey := legacyPicoSessionPrefix + "history-jsonl-transient"
	base := filepath.Join(dir, sanitizeSessionKey(sessionKey))
	now := time.Now().UTC()

	rawJSONL := strings.Join([]string{
		`{"role":"user","content":"keep me"}`,
		`{"role":"assistant","content":"","reasoning_content":"dangling thought"}`,
		`{"role":"assistant","content":"and me"}`,
	}, "\n") + "\n"
	if err := os.WriteFile(base+".jsonl", []byte(rawJSONL), 0o644); err != nil {
		t.Fatalf("WriteFile(jsonl) error = %v", err)
	}
	metaData, err := json.Marshal(memory.SessionMeta{
		Key:       sessionKey,
		Count:     3,
		Skip:      0,
		CreatedAt: now,
		UpdatedAt: now,
	})
	if err != nil {
		t.Fatalf("Marshal(meta) error = %v", err)
	}
	if err := os.WriteFile(base+".meta.json", metaData, 0o644); err != nil {
		t.Fatalf("WriteFile(meta) error = %v", err)
	}

	h := NewHandler(configPath)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/sessions", nil)
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}

	var items []sessionListItem
	if err := json.Unmarshal(rec.Body.Bytes(), &items); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("len(items) = %d, want 1", len(items))
	}
	if items[0].ID != "history-jsonl-transient" {
		t.Fatalf("items[0].ID = %q, want %q", items[0].ID, "history-jsonl-transient")
	}
	if items[0].MessageCount != 2 {
		t.Fatalf("items[0].MessageCount = %d, want 2 after dropping transient thought", items[0].MessageCount)
	}
}

func TestHandleListSessions_TitleUsesFirstUserMessage(t *testing.T) {
	configPath, cleanup := setupOAuthTestEnv(t)
	defer cleanup()

	dir := sessionsTestDir(t, configPath)
	store, storeErr := memory.NewJSONLStore(dir)
	if storeErr != nil {
		t.Fatalf("NewJSONLStore() error = %v", storeErr)
	}

	sessionKey := legacyPicoSessionPrefix + "summary-title"
	if err := store.AddFullMessage(nil, sessionKey, providers.Message{
		Role:    "user",
		Content: "fallback preview",
	}); err != nil {
		t.Fatalf("AddFullMessage() error = %v", err)
	}
	if err := store.SetSummary(
		nil,
		sessionKey,
		"  This summary is intentionally longer than sixty characters so it must be truncated in the history menu.  ",
	); err != nil {
		t.Fatalf("SetSummary() error = %v", err)
	}

	h := NewHandler(configPath)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/sessions", nil)
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}

	var items []sessionListItem
	if err := json.Unmarshal(rec.Body.Bytes(), &items); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("len(items) = %d, want 1", len(items))
	}
	expectedTitle := truncateRunes("fallback preview", maxSessionTitleRunes)
	if items[0].Title != expectedTitle {
		t.Fatalf("items[0].Title = %q", items[0].Title)
	}
	if items[0].Preview != "fallback preview" {
		t.Fatalf("items[0].Preview = %q, want %q", items[0].Preview, "fallback preview")
	}
}

func TestHandleGetSession_JSONLStorage(t *testing.T) {
	configPath, cleanup := setupOAuthTestEnv(t)
	defer cleanup()

	dir := sessionsTestDir(t, configPath)
	store, err := memory.NewJSONLStore(dir)
	if err != nil {
		t.Fatalf("NewJSONLStore() error = %v", err)
	}

	sessionKey := legacyPicoSessionPrefix + "detail-jsonl"
	for _, msg := range []providers.Message{
		{Role: "user", Content: "first"},
		{Role: "assistant", Content: "second"},
		{Role: "tool", Content: "ignored"},
	} {
		if err := store.AddFullMessage(nil, sessionKey, msg); err != nil {
			t.Fatalf("AddFullMessage() error = %v", err)
		}
	}
	if err := store.SetSummary(nil, sessionKey, "detail summary"); err != nil {
		t.Fatalf("SetSummary() error = %v", err)
	}

	h := NewHandler(configPath)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/sessions/detail-jsonl", nil)
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}

	var resp struct {
		ID       string `json:"id"`
		Summary  string `json:"summary"`
		Messages []struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}
	if resp.ID != "detail-jsonl" {
		t.Fatalf("resp.ID = %q, want %q", resp.ID, "detail-jsonl")
	}
	if resp.Summary != "detail summary" {
		t.Fatalf("resp.Summary = %q, want %q", resp.Summary, "detail summary")
	}
	if len(resp.Messages) != 2 {
		t.Fatalf("len(resp.Messages) = %d, want 2", len(resp.Messages))
	}
	if resp.Messages[0].Role != "user" || resp.Messages[0].Content != "first" {
		t.Fatalf("first message = %#v, want user/first", resp.Messages[0])
	}
	if resp.Messages[1].Role != "assistant" || resp.Messages[1].Content != "second" {
		t.Fatalf("second message = %#v, want assistant/second", resp.Messages[1])
	}
}

func TestHandleGetSession_HidesHandledToolAttachmentsBackedByMediaRefs(t *testing.T) {
	configPath, cleanup := setupOAuthTestEnv(t)
	defer cleanup()

	dir := sessionsTestDir(t, configPath)
	store, err := memory.NewJSONLStore(dir)
	if err != nil {
		t.Fatalf("NewJSONLStore() error = %v", err)
	}

	sessionKey := legacyPicoSessionPrefix + "attachment-history"
	for _, msg := range []providers.Message{
		{Role: "user", Content: "send me the report"},
		{
			Role:    "assistant",
			Content: handledToolResponseSummaryText,
			Attachments: []providers.Attachment{{
				Type:        "file",
				Ref:         "media://attachment-1",
				Filename:    "report.txt",
				ContentType: "text/plain",
			}},
		},
	} {
		if err := store.AddFullMessage(nil, sessionKey, msg); err != nil {
			t.Fatalf("AddFullMessage() error = %v", err)
		}
	}

	h := NewHandler(configPath)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/sessions/attachment-history", nil)
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}

	var resp struct {
		Messages []sessionChatMessage `json:"messages"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}

	if len(resp.Messages) != 1 {
		t.Fatalf("len(resp.Messages) = %d, want 1", len(resp.Messages))
	}
	if resp.Messages[0].Role != "user" || resp.Messages[0].Content != "send me the report" {
		t.Fatalf("message = %#v, want only user request", resp.Messages[0])
	}
}

func TestHandleGetSession_ExposesHandledToolAttachmentsWithDurableURL(t *testing.T) {
	configPath, cleanup := setupOAuthTestEnv(t)
	defer cleanup()

	dir := sessionsTestDir(t, configPath)
	store, err := memory.NewJSONLStore(dir)
	if err != nil {
		t.Fatalf("NewJSONLStore() error = %v", err)
	}

	sessionKey := legacyPicoSessionPrefix + "attachment-history-durable"
	for _, msg := range []providers.Message{
		{Role: "user", Content: "send me the report"},
		{
			Role:    "assistant",
			Content: handledToolResponseSummaryText,
			Attachments: []providers.Attachment{{
				Type:        "file",
				URL:         "https://example.com/report.txt",
				Filename:    "report.txt",
				ContentType: "text/plain",
			}},
		},
	} {
		if err := store.AddFullMessage(nil, sessionKey, msg); err != nil {
			t.Fatalf("AddFullMessage() error = %v", err)
		}
	}

	h := NewHandler(configPath)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/sessions/attachment-history-durable", nil)
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}

	var resp struct {
		Messages []sessionChatMessage `json:"messages"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}

	if len(resp.Messages) != 2 {
		t.Fatalf("len(resp.Messages) = %d, want 2", len(resp.Messages))
	}

	assistant := resp.Messages[1]
	if assistant.Role != "assistant" {
		t.Fatalf("assistant role = %q, want assistant", assistant.Role)
	}
	if assistant.Content != "" {
		t.Fatalf("assistant content = %q, want empty string", assistant.Content)
	}
	if len(assistant.Attachments) != 1 {
		t.Fatalf("len(assistant.Attachments) = %d, want 1", len(assistant.Attachments))
	}
	if assistant.Attachments[0].URL != "https://example.com/report.txt" {
		t.Fatalf(
			"attachment url = %q, want %q",
			assistant.Attachments[0].URL,
			"https://example.com/report.txt",
		)
	}
	if assistant.Attachments[0].Filename != "report.txt" {
		t.Fatalf("attachment filename = %q, want %q", assistant.Attachments[0].Filename, "report.txt")
	}
}

func TestHandleSessions_JSONLScopeDiscovery(t *testing.T) {
	configPath, cleanup := setupOAuthTestEnv(t)
	defer cleanup()

	dir := sessionsTestDir(t, configPath)
	store, storeErr := memory.NewJSONLStore(dir)
	if storeErr != nil {
		t.Fatalf("NewJSONLStore() error = %v", storeErr)
	}

	sessionKey := "sk_v1_scope_discovery"
	if err := store.AddFullMessage(nil, sessionKey, providers.Message{
		Role:    "user",
		Content: "scope discovered session",
	}); err != nil {
		t.Fatalf("AddFullMessage() error = %v", err)
	}
	if err := store.SetSummary(nil, sessionKey, "scope summary"); err != nil {
		t.Fatalf("SetSummary() error = %v", err)
	}

	scopeData, err := json.Marshal(session.SessionScope{
		Version:    session.ScopeVersionV1,
		AgentID:    "main",
		Channel:    "pico",
		Account:    "default",
		Dimensions: []string{"sender"},
		Values: map[string]string{
			"sender": "pico:scope-jsonl",
		},
	})
	if err != nil {
		t.Fatalf("Marshal(scope) error = %v", err)
	}
	if err := store.UpsertSessionMeta(nil, sessionKey, scopeData, nil); err != nil {
		t.Fatalf("UpsertSessionMeta() error = %v", err)
	}

	h := NewHandler(configPath)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	listRec := httptest.NewRecorder()
	listReq := httptest.NewRequest(http.MethodGet, "/api/sessions", nil)
	mux.ServeHTTP(listRec, listReq)
	if listRec.Code != http.StatusOK {
		t.Fatalf("list status = %d, want %d, body=%s", listRec.Code, http.StatusOK, listRec.Body.String())
	}

	var items []sessionListItem
	if err := json.Unmarshal(listRec.Body.Bytes(), &items); err != nil {
		t.Fatalf("Unmarshal(list) error = %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("len(items) = %d, want 1", len(items))
	}
	if items[0].ID != "scope-jsonl" {
		t.Fatalf("items[0].ID = %q, want %q", items[0].ID, "scope-jsonl")
	}

	detailRec := httptest.NewRecorder()
	detailReq := httptest.NewRequest(http.MethodGet, "/api/sessions/scope-jsonl", nil)
	mux.ServeHTTP(detailRec, detailReq)
	if detailRec.Code != http.StatusOK {
		t.Fatalf("detail status = %d, want %d, body=%s", detailRec.Code, http.StatusOK, detailRec.Body.String())
	}

	deleteRec := httptest.NewRecorder()
	deleteReq := httptest.NewRequest(http.MethodDelete, "/api/sessions/scope-jsonl", nil)
	mux.ServeHTTP(deleteRec, deleteReq)
	if deleteRec.Code != http.StatusNoContent {
		t.Fatalf("delete status = %d, want %d, body=%s", deleteRec.Code, http.StatusNoContent, deleteRec.Body.String())
	}
}

func TestHandleSessions_StructuredNonPicoScopeCannotFallBackToPicoAlias(t *testing.T) {
	configPath, cleanup := setupOAuthTestEnv(t)
	defer cleanup()

	dir := sessionsTestDir(t, configPath)
	store, err := memory.NewJSONLStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	const sessionID = "victim"
	scope := session.SessionScope{
		Version:    session.ScopeVersionV1,
		AgentID:    "main",
		Channel:    "slack",
		Account:    "default",
		Dimensions: []string{"sender"},
		Values:     map[string]string{"sender": "pico:" + sessionID},
	}
	canonicalKey := session.BuildSessionKey(scope)
	legacyLookingAlias := "agent:main:direct:pico:" + sessionID
	rawScope, err := json.Marshal(scope)
	if err != nil {
		t.Fatal(err)
	}
	if addErr := store.AddMessage(
		context.Background(),
		canonicalKey,
		"user",
		"private Slack history",
	); addErr != nil {
		t.Fatal(addErr)
	}
	if upsertErr := store.UpsertSessionMeta(
		context.Background(),
		canonicalKey,
		rawScope,
		[]string{legacyLookingAlias},
	); upsertErr != nil {
		t.Fatal(upsertErr)
	}

	h := NewHandler(configPath)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	listRec := httptest.NewRecorder()
	mux.ServeHTTP(listRec, httptest.NewRequest(http.MethodGet, "/api/sessions", nil))
	if listRec.Code != http.StatusOK || listRec.Body.String() != "[]\n" {
		t.Fatalf("list status=%d body=%s", listRec.Code, listRec.Body.String())
	}

	for _, method := range []string{http.MethodGet, http.MethodDelete} {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(
			rec,
			httptest.NewRequest(method, "/api/sessions/"+sessionID, nil),
		)
		if rec.Code != http.StatusNotFound {
			t.Fatalf("%s status=%d body=%s", method, rec.Code, rec.Body.String())
		}
	}

	_, history, _, found, err := store.ReadSessionSnapshot(
		context.Background(),
		canonicalKey,
	)
	if err != nil || !found || len(history) != 1 || history[0].Content != "private Slack history" {
		t.Fatalf("Slack snapshot = (found=%v, history=%#v, err=%v)", found, history, err)
	}
}

func TestHandleDeleteSession_RevalidatesOwnerAfterLookup(t *testing.T) {
	configPath, cleanup := setupOAuthTestEnv(t)
	defer cleanup()

	dir := sessionsTestDir(t, configPath)
	store, err := memory.NewJSONLStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()

	const sessionID = "rebound-owner"
	oldScope := session.SessionScope{
		Version:    session.ScopeVersionV1,
		AgentID:    "main",
		Channel:    "pico",
		Account:    "default",
		Dimensions: []string{"sender"},
		Values:     map[string]string{"sender": "pico:" + sessionID},
	}
	oldKey := session.BuildSessionKey(oldScope)
	legacyAlias := "agent:main:direct:pico:" + sessionID
	oldScopeJSON, err := json.Marshal(oldScope)
	if err != nil {
		t.Fatal(err)
	}
	if addErr := store.AddMessage(ctx, oldKey, "user", "old owner"); addErr != nil {
		t.Fatal(addErr)
	}
	if upsertErr := store.UpsertSessionMeta(
		ctx,
		oldKey,
		oldScopeJSON,
		[]string{legacyAlias},
	); upsertErr != nil {
		t.Fatal(upsertErr)
	}

	h := NewHandler(configPath)
	var reboundKey string
	h.sessionDeleteAfterLookup = func() {
		h.sessionDeleteAfterLookup = nil
		if deleted, deleteErr := store.DeleteSession(ctx, oldKey); deleteErr != nil || !deleted {
			t.Fatalf("replace old owner delete = (deleted=%v, err=%v)", deleted, deleteErr)
		}

		slackScope := oldScope
		slackScope.Channel = "slack"
		slackScopeJSON, marshalErr := json.Marshal(slackScope)
		if marshalErr != nil {
			t.Fatal(marshalErr)
		}
		if addErr := store.AddMessage(ctx, oldKey, "user", "replacement Slack resource"); addErr != nil {
			t.Fatal(addErr)
		}
		if upsertErr := store.UpsertSessionMeta(ctx, oldKey, slackScopeJSON, nil); upsertErr != nil {
			t.Fatal(upsertErr)
		}

		newScope := oldScope
		newScope.AgentID = "reviewer"
		newKey := session.BuildSessionKey(newScope)
		reboundKey = newKey
		newScopeJSON, marshalErr := json.Marshal(newScope)
		if marshalErr != nil {
			t.Fatal(marshalErr)
		}
		if addErr := store.AddMessage(ctx, newKey, "user", "new Pico owner"); addErr != nil {
			t.Fatal(addErr)
		}
		if upsertErr := store.UpsertSessionMeta(
			ctx,
			newKey,
			newScopeJSON,
			[]string{legacyAlias},
		); upsertErr != nil {
			t.Fatal(upsertErr)
		}
	}

	mux := http.NewServeMux()
	h.RegisterRoutes(mux)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(
		rec,
		httptest.NewRequest(http.MethodDelete, "/api/sessions/"+sessionID, nil),
	)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("delete status=%d body=%s", rec.Code, rec.Body.String())
	}

	_, oldHistory, oldMeta, oldFound, oldErr := store.ReadSessionSnapshot(ctx, oldKey)
	if oldErr != nil || !oldFound || len(oldHistory) != 1 ||
		oldHistory[0].Content != "replacement Slack resource" {
		t.Fatalf("replacement resource = (found=%v, history=%#v, err=%v)", oldFound, oldHistory, oldErr)
	}
	var currentOldScope session.SessionScope
	if decodeErr := json.Unmarshal(oldMeta.Scope, &currentOldScope); decodeErr != nil ||
		currentOldScope.Channel != "slack" {
		t.Fatalf("replacement scope = %#v, err=%v", currentOldScope, decodeErr)
	}
	resolvedKey, newHistory, newMeta, newFound, newErr := store.ReadSessionSnapshot(ctx, reboundKey)
	if newErr != nil || newFound || resolvedKey != "" || len(newHistory) != 0 || newMeta.Key != "" {
		t.Fatalf(
			"rebound Pico owner = (key=%q, found=%v, history=%#v, meta=%#v, err=%v), want deleted",
			resolvedKey,
			newFound,
			newHistory,
			newMeta,
			newErr,
		)
	}
	if currentRef, findErr := h.findPicoJSONLSession(ctx, dir, sessionID); !errors.Is(findErr, os.ErrNotExist) {
		t.Fatalf("current Pico owner = %#v, err=%v", currentRef, findErr)
	}
}

func TestHandleDeleteSession_DeletesEveryCurrentOwnerForProjectedID(t *testing.T) {
	configPath, cleanup := setupOAuthTestEnv(t)
	defer cleanup()

	dir := sessionsTestDir(t, configPath)
	store, err := memory.NewJSONLStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()

	const sessionID = "shared-projection"
	keys := make([]string, 0, 2)
	aliases := make([]string, 0, 2)
	for _, agentID := range []string{"main", "reviewer"} {
		scope := session.SessionScope{
			Version:    session.ScopeVersionV1,
			AgentID:    agentID,
			Channel:    "pico",
			Account:    "default",
			Dimensions: []string{"sender"},
			Values:     map[string]string{"sender": "pico:" + sessionID},
		}
		key := session.BuildSessionKey(scope)
		alias := "agent:" + agentID + ":direct:pico:" + sessionID
		rawScope, marshalErr := json.Marshal(scope)
		if marshalErr != nil {
			t.Fatal(marshalErr)
		}
		if addErr := store.AddMessage(ctx, key, "user", agentID+" owner"); addErr != nil {
			t.Fatal(addErr)
		}
		if upsertErr := store.UpsertSessionMeta(ctx, key, rawScope, []string{alias}); upsertErr != nil {
			t.Fatal(upsertErr)
		}
		keys = append(keys, key)
		aliases = append(aliases, alias)
	}

	h := NewHandler(configPath)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(
		rec,
		httptest.NewRequest(http.MethodDelete, "/api/sessions/"+sessionID, nil),
	)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("delete status=%d body=%s", rec.Code, rec.Body.String())
	}
	for _, key := range append(keys, aliases...) {
		_, _, _, found, readErr := store.ReadSessionSnapshot(ctx, key)
		if readErr != nil || found {
			t.Fatalf("deleted owner %q = (found=%v, err=%v)", key, found, readErr)
		}
	}
	listRec := httptest.NewRecorder()
	mux.ServeHTTP(listRec, httptest.NewRequest(http.MethodGet, "/api/sessions", nil))
	if listRec.Code != http.StatusOK || listRec.Body.String() != "[]\n" {
		t.Fatalf("list after delete status=%d body=%s", listRec.Code, listRec.Body.String())
	}
}

func TestHandleDeleteSession_MissingSessionsDirectoryReturnsNotFound(t *testing.T) {
	configPath, cleanup := setupOAuthTestEnv(t)
	defer cleanup()

	h := NewHandler(configPath)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(
		rec,
		httptest.NewRequest(http.MethodDelete, "/api/sessions/unknown", nil),
	)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("delete status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestHandleDeleteSession_DeletesSameIDMetadataLessShadow(t *testing.T) {
	configPath, cleanup := setupOAuthTestEnv(t)
	defer cleanup()

	dir := sessionsTestDir(t, configPath)
	store, err := memory.NewJSONLStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()

	const sessionID = "orphan-shadow"
	scope := session.SessionScope{
		Version:    session.ScopeVersionV1,
		AgentID:    "main",
		Channel:    "pico",
		Account:    "default",
		Dimensions: []string{"sender"},
		Values:     map[string]string{"sender": "pico:" + sessionID},
	}
	canonicalKey := session.BuildSessionKey(scope)
	rawScope, err := json.Marshal(scope)
	if err != nil {
		t.Fatal(err)
	}
	if addErr := store.AddMessage(ctx, canonicalKey, "user", "canonical owner"); addErr != nil {
		t.Fatal(addErr)
	}
	if upsertErr := store.UpsertSessionMeta(ctx, canonicalKey, rawScope, nil); upsertErr != nil {
		t.Fatal(upsertErr)
	}

	shadowKey := legacyPicoSessionPrefix + sessionID
	shadowPath := filepath.Join(dir, sanitizeSessionKey(shadowKey)+".jsonl")
	line, err := json.Marshal(providers.Message{Role: "user", Content: "detached shadow"})
	if err != nil {
		t.Fatal(err)
	}
	if writeErr := os.WriteFile(shadowPath, append(line, '\n'), 0o644); writeErr != nil {
		t.Fatal(writeErr)
	}

	h := NewHandler(configPath)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)
	listRec := httptest.NewRecorder()
	mux.ServeHTTP(listRec, httptest.NewRequest(http.MethodGet, "/api/sessions", nil))
	if listRec.Code != http.StatusOK {
		t.Fatalf("list status=%d body=%s", listRec.Code, listRec.Body.String())
	}
	var items []sessionListItem
	if decodeErr := json.Unmarshal(listRec.Body.Bytes(), &items); decodeErr != nil || len(items) != 1 {
		t.Fatalf("deduplicated list = %#v, err=%v", items, decodeErr)
	}

	deleteRec := httptest.NewRecorder()
	mux.ServeHTTP(
		deleteRec,
		httptest.NewRequest(http.MethodDelete, "/api/sessions/"+sessionID, nil),
	)
	if deleteRec.Code != http.StatusNoContent {
		t.Fatalf("delete status=%d body=%s", deleteRec.Code, deleteRec.Body.String())
	}
	if _, err := os.Stat(shadowPath); !os.IsNotExist(err) {
		t.Fatalf("detached shadow stat error = %v", err)
	}
	resolvedKey, history, meta, found, readErr := store.ReadSessionSnapshot(ctx, canonicalKey)
	if readErr != nil || found || resolvedKey != "" || len(history) != 0 || meta.Key != "" {
		t.Fatalf(
			"canonical owner = (key=%q, found=%v, history=%#v, meta=%#v, err=%v), want deleted",
			resolvedKey,
			found,
			history,
			meta,
			readErr,
		)
	}
}

func TestHandleGetSession_UsesFirstReadableOwnerShownByList(t *testing.T) {
	configPath, cleanup := setupOAuthTestEnv(t)
	defer cleanup()

	dir := sessionsTestDir(t, configPath)
	store, err := memory.NewJSONLStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()

	const sessionID = "readable-owner"
	scopes := []session.SessionScope{
		{
			Version:    session.ScopeVersionV1,
			AgentID:    "main",
			Channel:    "pico",
			Account:    "default",
			Dimensions: []string{"sender"},
			Values:     map[string]string{"sender": "pico:" + sessionID},
		},
		{
			Version:    session.ScopeVersionV1,
			AgentID:    "reviewer",
			Channel:    "pico",
			Account:    "default",
			Dimensions: []string{"sender"},
			Values:     map[string]string{"sender": "pico:" + sessionID},
		},
	}
	if session.BuildSessionKey(scopes[1]) < session.BuildSessionKey(scopes[0]) {
		scopes[0], scopes[1] = scopes[1], scopes[0]
	}
	for index, scope := range scopes {
		key := session.BuildSessionKey(scope)
		rawScope, marshalErr := json.Marshal(scope)
		if marshalErr != nil {
			t.Fatal(marshalErr)
		}
		if index == 1 {
			if addErr := store.AddMessage(ctx, key, "user", "readable second owner"); addErr != nil {
				t.Fatal(addErr)
			}
		}
		if upsertErr := store.UpsertSessionMeta(ctx, key, rawScope, nil); upsertErr != nil {
			t.Fatal(upsertErr)
		}
	}

	h := NewHandler(configPath)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)
	listRec := httptest.NewRecorder()
	mux.ServeHTTP(listRec, httptest.NewRequest(http.MethodGet, "/api/sessions", nil))
	if listRec.Code != http.StatusOK {
		t.Fatalf("list status=%d body=%s", listRec.Code, listRec.Body.String())
	}
	var items []sessionListItem
	if decodeErr := json.Unmarshal(listRec.Body.Bytes(), &items); decodeErr != nil ||
		len(items) != 1 || items[0].Preview != "readable second owner" {
		t.Fatalf("list items = %#v, err=%v", items, decodeErr)
	}

	detailRec := httptest.NewRecorder()
	mux.ServeHTTP(
		detailRec,
		httptest.NewRequest(http.MethodGet, "/api/sessions/"+sessionID, nil),
	)
	if detailRec.Code != http.StatusOK ||
		!strings.Contains(detailRec.Body.String(), "readable second owner") {
		t.Fatalf("detail status=%d body=%s", detailRec.Code, detailRec.Body.String())
	}
}

func TestHandleSessions_ReadProjectionRejectsLookupReboundToSlack(t *testing.T) {
	for _, endpoint := range []string{"list", "detail"} {
		t.Run(endpoint, func(t *testing.T) {
			configPath, cleanup := setupOAuthTestEnv(t)
			defer cleanup()

			dir := sessionsTestDir(t, configPath)
			store, err := memory.NewJSONLStore(dir)
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			ctx := context.Background()

			const sessionID = "read-rebound"
			picoScope := session.SessionScope{
				Version:    session.ScopeVersionV1,
				AgentID:    "main",
				Channel:    "pico",
				Account:    "default",
				Dimensions: []string{"sender"},
				Values:     map[string]string{"sender": "pico:" + sessionID},
			}
			key := session.BuildSessionKey(picoScope)
			picoScopeJSON, err := json.Marshal(picoScope)
			if err != nil {
				t.Fatal(err)
			}
			if addErr := store.AddMessage(ctx, key, "user", "old Pico history"); addErr != nil {
				t.Fatal(addErr)
			}
			if upsertErr := store.UpsertSessionMeta(ctx, key, picoScopeJSON, nil); upsertErr != nil {
				t.Fatal(upsertErr)
			}

			h := NewHandler(configPath)
			h.sessionReadAfterLookup = func() {
				h.sessionReadAfterLookup = nil
				if deleted, deleteErr := store.DeleteSession(ctx, key); deleteErr != nil || !deleted {
					t.Fatalf("replace Pico delete = (deleted=%v, err=%v)", deleted, deleteErr)
				}
				slackScope := picoScope
				slackScope.Channel = "slack"
				slackScopeJSON, marshalErr := json.Marshal(slackScope)
				if marshalErr != nil {
					t.Fatal(marshalErr)
				}
				if addErr := store.AddMessage(
					ctx,
					key,
					"user",
					"private Slack replacement",
				); addErr != nil {
					t.Fatal(addErr)
				}
				if upsertErr := store.UpsertSessionMeta(ctx, key, slackScopeJSON, nil); upsertErr != nil {
					t.Fatal(upsertErr)
				}
			}

			mux := http.NewServeMux()
			h.RegisterRoutes(mux)
			rec := httptest.NewRecorder()
			path := "/api/sessions"
			if endpoint == "detail" {
				path += "/" + sessionID
			}
			mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
			if endpoint == "list" {
				if rec.Code != http.StatusOK || rec.Body.String() != "[]\n" {
					t.Fatalf("list status=%d body=%s", rec.Code, rec.Body.String())
				}
			} else if rec.Code != http.StatusNotFound {
				t.Fatalf("detail status=%d body=%s", rec.Code, rec.Body.String())
			}
			if strings.Contains(rec.Body.String(), "private Slack replacement") {
				t.Fatal("read projection exposed rebound Slack history")
			}
		})
	}
}

func TestHandleGetSession_SkipsTransientThoughtMessages(t *testing.T) {
	configPath, cleanup := setupOAuthTestEnv(t)
	defer cleanup()

	dir := sessionsTestDir(t, configPath)
	store, err := memory.NewJSONLStore(dir)
	if err != nil {
		t.Fatalf("NewJSONLStore() error = %v", err)
	}

	sessionKey := picoSessionPrefix + "detail-transient-thought"
	for _, msg := range []providers.Message{
		{Role: "user", Content: "hello"},
		{Role: "assistant", ReasoningContent: "internal chain of thought"},
		{Role: "assistant", Content: "final visible answer"},
	} {
		if err := store.AddFullMessage(nil, sessionKey, msg); err != nil {
			t.Fatalf("AddFullMessage() error = %v", err)
		}
	}

	h := NewHandler(configPath)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/sessions/detail-transient-thought", nil)
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}

	var resp struct {
		Messages []sessionChatMessage `json:"messages"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}
	if len(resp.Messages) != 2 {
		t.Fatalf("len(resp.Messages) = %d, want 2", len(resp.Messages))
	}
	if resp.Messages[0].Role != "user" || resp.Messages[0].Content != "hello" {
		t.Fatalf("first message = %#v, want user/hello", resp.Messages[0])
	}
	if resp.Messages[1].Role != "assistant" || resp.Messages[1].Content != "final visible answer" {
		t.Fatalf("second message = %#v, want assistant/final visible answer", resp.Messages[1])
	}
}

func TestHandleGetSession_ReconstructsThoughtFromAssistantReasoningContent(t *testing.T) {
	configPath, cleanup := setupOAuthTestEnv(t)
	defer cleanup()

	dir := sessionsTestDir(t, configPath)
	store, err := memory.NewJSONLStore(dir)
	if err != nil {
		t.Fatalf("NewJSONLStore() error = %v", err)
	}

	sessionKey := picoSessionPrefix + "detail-reasoning-content"
	for _, msg := range []providers.Message{
		{Role: "user", Content: "hello"},
		{Role: "assistant", Content: "final visible answer", ModelName: "gpt-5.4", ReasoningContent: "internal chain of thought"},
	} {
		if err := store.AddFullMessage(nil, sessionKey, msg); err != nil {
			t.Fatalf("AddFullMessage() error = %v", err)
		}
	}

	h := NewHandler(configPath)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/sessions/detail-reasoning-content", nil)
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}

	var resp struct {
		Messages []sessionChatMessage `json:"messages"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}
	if len(resp.Messages) != 3 {
		t.Fatalf("len(resp.Messages) = %d, want 3", len(resp.Messages))
	}
	if resp.Messages[1].Role != "assistant" ||
		resp.Messages[1].Content != "internal chain of thought" ||
		resp.Messages[1].Kind != "thought" {
		t.Fatalf("thought message = %#v, want assistant thought/internal chain of thought", resp.Messages[1])
	}
	if resp.Messages[1].ModelName != "gpt-5.4" {
		t.Fatalf("thought model_name = %q, want %q", resp.Messages[1].ModelName, "gpt-5.4")
	}
	if resp.Messages[2].Role != "assistant" || resp.Messages[2].Content != "final visible answer" {
		t.Fatalf("final message = %#v, want assistant/final visible answer", resp.Messages[2])
	}
	if resp.Messages[2].ModelName != "gpt-5.4" {
		t.Fatalf("final model_name = %q, want %q", resp.Messages[2].ModelName, "gpt-5.4")
	}
}

func TestHandleGetSession_ReconstructsRefreshMatrixForThoughtAndToolSummary(t *testing.T) {
	configPath, cleanup := setupOAuthTestEnv(t)
	defer cleanup()

	dir := sessionsTestDir(t, configPath)
	store, err := memory.NewJSONLStore(dir)
	if err != nil {
		t.Fatalf("NewJSONLStore() error = %v", err)
	}

	sessionKey := picoSessionPrefix + "detail-refresh-matrix"
	for _, msg := range []providers.Message{
		{Role: "user", Content: "turn1"},
		{Role: "assistant", Content: "plain visible", ReasoningContent: "plain thought"},
		{Role: "user", Content: "turn2"},
		{
			Role:             "assistant",
			ReasoningContent: "tool thought",
			ToolCalls: []providers.ToolCall{{
				ID:   "call_read_file",
				Type: "function",
				Function: &providers.FunctionCall{
					Name:      "read_file",
					Arguments: `{"path":"README.md"}`,
				},
			}},
		},
		{Role: "tool", ToolCallID: "call_read_file", Content: "file result"},
		{Role: "user", Content: "turn3"},
		{
			Role:    "assistant",
			Content: "tool visible only",
			ToolCalls: []providers.ToolCall{{
				ID:   "call_list_dir",
				Type: "function",
				Function: &providers.FunctionCall{
					Name:      "list_dir",
					Arguments: `{"path":"."}`,
				},
			}},
		},
		{Role: "tool", ToolCallID: "call_list_dir", Content: "dir result"},
		{Role: "user", Content: "turn4"},
		{
			Role:             "assistant",
			Content:          "tool visible and thought",
			ReasoningContent: "tool mixed thought",
			ToolCalls: []providers.ToolCall{{
				ID:   "call_exec",
				Type: "function",
				Function: &providers.FunctionCall{
					Name:      "exec",
					Arguments: `{"command":"pwd"}`,
				},
			}},
		},
		{Role: "tool", ToolCallID: "call_exec", Content: "pwd result"},
	} {
		if err := store.AddFullMessage(nil, sessionKey, msg); err != nil {
			t.Fatalf("AddFullMessage() error = %v", err)
		}
	}

	h := NewHandler(configPath)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/sessions/detail-refresh-matrix", nil)
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}

	var resp struct {
		Messages []sessionChatMessage `json:"messages"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}

	if len(resp.Messages) != 13 {
		t.Fatalf("len(resp.Messages) = %d, want 13", len(resp.Messages))
	}

	assertMessage := func(index int, role, kind, content string) {
		t.Helper()
		msg := resp.Messages[index]
		if msg.Role != role || msg.Kind != kind || msg.Content != content {
			t.Fatalf("messages[%d] = %#v, want role=%q kind=%q content=%q", index, msg, role, kind, content)
		}
	}

	assertMessage(0, "user", "", "turn1")
	assertMessage(1, "assistant", "thought", "plain thought")
	assertMessage(2, "assistant", "", "plain visible")
	assertMessage(3, "user", "", "turn2")
	assertMessage(4, "assistant", "thought", "tool thought")
	assertVisibleToolCallMessage(t, resp.Messages[5], "read_file")
	assertMessage(6, "user", "", "turn3")
	assertMessage(7, "assistant", "", "tool visible only")
	assertVisibleToolCallMessage(t, resp.Messages[8], "list_dir")
	assertMessage(9, "user", "", "turn4")
	assertMessage(10, "assistant", "thought", "tool mixed thought")
	assertMessage(11, "assistant", "", "tool visible and thought")
	assertVisibleToolCallMessage(t, resp.Messages[12], "exec")
}

func TestHandleGetSession_ReconstructsVisibleMessageToolOutputWithoutDuplicateSummary(t *testing.T) {
	configPath, cleanup := setupOAuthTestEnv(t)
	defer cleanup()

	dir := sessionsTestDir(t, configPath)
	store, err := memory.NewJSONLStore(dir)
	if err != nil {
		t.Fatalf("NewJSONLStore() error = %v", err)
	}

	sessionKey := picoSessionPrefix + "detail-message-tool"
	for _, msg := range []providers.Message{
		{Role: "user", Content: "test"},
		{
			Role:      "assistant",
			Content:   "",
			ModelName: "gpt-5.4-mini",
			ToolCalls: []providers.ToolCall{
				{
					ID:   "call_1",
					Type: "function",
					Function: &providers.FunctionCall{
						Name:      "message",
						Arguments: `{"content":"visible tool output"}`,
					},
				},
			},
		},
		{Role: "tool", Content: "Message sent to pico:pico:detail-message-tool", ToolCallID: "call_1"},
		{Role: "assistant", Content: handledToolResponseSummaryText},
	} {
		if err := store.AddFullMessage(nil, sessionKey, msg); err != nil {
			t.Fatalf("AddFullMessage() error = %v", err)
		}
	}

	h := NewHandler(configPath)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/sessions/detail-message-tool", nil)
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}

	var resp struct {
		Messages []sessionChatMessage `json:"messages"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}
	if len(resp.Messages) != 3 {
		t.Fatalf("len(resp.Messages) = %d, want 3", len(resp.Messages))
	}
	if resp.Messages[0].Role != "user" || resp.Messages[0].Content != "test" {
		t.Fatalf("first message = %#v, want user/test", resp.Messages[0])
	}
	assertVisibleToolCallMessage(t, resp.Messages[1], "message")
	if resp.Messages[1].ModelName != "gpt-5.4-mini" {
		t.Fatalf("tool_calls model_name = %q, want %q", resp.Messages[1].ModelName, "gpt-5.4-mini")
	}
	if resp.Messages[2].Role != "assistant" || resp.Messages[2].Content != "visible tool output" {
		t.Fatalf("assistant message = %#v, want visible tool output", resp.Messages[2])
	}
	if resp.Messages[2].ModelName != "gpt-5.4-mini" {
		t.Fatalf("visible tool output model_name = %q, want %q", resp.Messages[2].ModelName, "gpt-5.4-mini")
	}
}

func TestHandleGetSession_PreservesFinalAssistantReplyAfterMessageToolOutput(t *testing.T) {
	configPath, cleanup := setupOAuthTestEnv(t)
	defer cleanup()

	dir := sessionsTestDir(t, configPath)
	store, err := memory.NewJSONLStore(dir)
	if err != nil {
		t.Fatalf("NewJSONLStore() error = %v", err)
	}

	sessionKey := picoSessionPrefix + "detail-message-tool-final-reply"
	for _, msg := range []providers.Message{
		{Role: "user", Content: "test"},
		{
			Role: "assistant",
			ToolCalls: []providers.ToolCall{
				{
					ID:   "call_1",
					Type: "function",
					Function: &providers.FunctionCall{
						Name:      "message",
						Arguments: `{"content":"visible tool output"}`,
					},
				},
			},
		},
		{Role: "tool", Content: "Message sent to pico:pico:detail-message-tool-final-reply", ToolCallID: "call_1"},
		{Role: "assistant", Content: "final assistant reply"},
	} {
		if err := store.AddFullMessage(nil, sessionKey, msg); err != nil {
			t.Fatalf("AddFullMessage() error = %v", err)
		}
	}

	h := NewHandler(configPath)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/sessions/detail-message-tool-final-reply", nil)
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}

	var resp struct {
		Messages []sessionChatMessage `json:"messages"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}
	if len(resp.Messages) != 4 {
		t.Fatalf("len(resp.Messages) = %d, want 4", len(resp.Messages))
	}
	if resp.Messages[0].Role != "user" || resp.Messages[0].Content != "test" {
		t.Fatalf("first message = %#v, want user/test", resp.Messages[0])
	}
	assertVisibleToolCallMessage(t, resp.Messages[1], "message")
	if resp.Messages[2].Role != "assistant" || resp.Messages[2].Content != "visible tool output" {
		t.Fatalf("interim assistant message = %#v, want visible tool output", resp.Messages[2])
	}
	if resp.Messages[3].Role != "assistant" || resp.Messages[3].Content != "final assistant reply" {
		t.Fatalf("final assistant message = %#v, want final assistant reply", resp.Messages[3])
	}
}

func TestHandleListSessions_MessageCountUsesVisibleTranscript(t *testing.T) {
	configPath, cleanup := setupOAuthTestEnv(t)
	defer cleanup()

	dir := sessionsTestDir(t, configPath)
	store, err := memory.NewJSONLStore(dir)
	if err != nil {
		t.Fatalf("NewJSONLStore() error = %v", err)
	}

	sessionKey := picoSessionPrefix + "list-visible-count"
	for _, msg := range []providers.Message{
		{Role: "user", Content: "test"},
		{
			Role: "assistant",
			ToolCalls: []providers.ToolCall{
				{
					ID:   "call_1",
					Type: "function",
					Function: &providers.FunctionCall{
						Name:      "message",
						Arguments: `{"content":"visible tool output"}`,
					},
				},
			},
		},
		{Role: "tool", Content: "Message sent to pico:pico:list-visible-count", ToolCallID: "call_1"},
		{Role: "assistant", Content: handledToolResponseSummaryText},
	} {
		if err := store.AddFullMessage(nil, sessionKey, msg); err != nil {
			t.Fatalf("AddFullMessage() error = %v", err)
		}
	}

	h := NewHandler(configPath)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/sessions", nil)
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}

	var items []sessionListItem
	if err := json.Unmarshal(rec.Body.Bytes(), &items); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("len(items) = %d, want 1", len(items))
	}
	if items[0].MessageCount != 3 {
		t.Fatalf("items[0].MessageCount = %d, want 3", items[0].MessageCount)
	}
}

func TestHandleListSessions_DeduplicatesAssistantToolCallContentFromVisibleTranscript(t *testing.T) {
	configPath, cleanup := setupOAuthTestEnv(t)
	defer cleanup()

	dir := sessionsTestDir(t, configPath)
	store, err := memory.NewJSONLStore(dir)
	if err != nil {
		t.Fatalf("NewJSONLStore() error = %v", err)
	}

	sessionKey := picoSessionPrefix + "list-deduped-tool-content"
	for _, msg := range []providers.Message{
		{Role: "user", Content: "check file"},
		{
			Role:    "assistant",
			Content: "Read the file before replying.",
			ToolCalls: []providers.ToolCall{
				{
					ID:   "call_1",
					Type: "function",
					Function: &providers.FunctionCall{
						Name:      "read_file",
						Arguments: `{"path":"README.md"}`,
					},
					ExtraContent: &providers.ExtraContent{
						ToolFeedbackExplanation: "Read the file before replying.",
					},
				},
			},
		},
		{Role: "tool", Content: "raw read_file result", ToolCallID: "call_1"},
	} {
		if err := store.AddFullMessage(nil, sessionKey, msg); err != nil {
			t.Fatalf("AddFullMessage() error = %v", err)
		}
	}

	h := NewHandler(configPath)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/sessions", nil)
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}

	var items []sessionListItem
	if err := json.Unmarshal(rec.Body.Bytes(), &items); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("len(items) = %d, want 1", len(items))
	}
	if items[0].MessageCount != 2 {
		t.Fatalf("items[0].MessageCount = %d, want 2", items[0].MessageCount)
	}
}

func TestHandleGetSession_DoesNotDuplicateAssistantToolCallContent(t *testing.T) {
	configPath, cleanup := setupOAuthTestEnv(t)
	defer cleanup()

	dir := sessionsTestDir(t, configPath)
	store, err := memory.NewJSONLStore(dir)
	if err != nil {
		t.Fatalf("NewJSONLStore() error = %v", err)
	}

	sessionKey := picoSessionPrefix + "detail-tool-summary-and-content"
	for _, msg := range []providers.Message{
		{Role: "user", Content: "check file"},
		{
			Role:    "assistant",
			Content: "Read the file before replying.",
			ToolCalls: []providers.ToolCall{
				{
					ID:   "call_1",
					Type: "function",
					Function: &providers.FunctionCall{
						Name:      "read_file",
						Arguments: `{"path":"README.md","start_line":1,"end_line":10}`,
					},
					ExtraContent: &providers.ExtraContent{
						ToolFeedbackExplanation: "Read the file before replying.",
					},
				},
			},
		},
		{Role: "tool", Content: "raw read_file result", ToolCallID: "call_1"},
	} {
		if err := store.AddFullMessage(nil, sessionKey, msg); err != nil {
			t.Fatalf("AddFullMessage() error = %v", err)
		}
	}

	h := NewHandler(configPath)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/sessions/detail-tool-summary-and-content", nil)
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}

	var resp struct {
		Messages []sessionChatMessage `json:"messages"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}
	if len(resp.Messages) != 2 {
		t.Fatalf("len(resp.Messages) = %d, want 2", len(resp.Messages))
	}
	if resp.Messages[0].Role != "user" || resp.Messages[0].Content != "check file" {
		t.Fatalf("first message = %#v, want user/check file", resp.Messages[0])
	}
	toolCall := assertVisibleToolCallMessage(t, resp.Messages[1], "read_file")
	if toolCall.ExtraContent == nil ||
		toolCall.ExtraContent.ToolFeedbackExplanation != "Read the file before replying." {
		t.Fatalf("tool call = %#v, want explanation", toolCall)
	}
}

func TestHandleGetSession_PreservesDistinctAssistantToolCallContent(t *testing.T) {
	configPath, cleanup := setupOAuthTestEnv(t)
	defer cleanup()

	dir := sessionsTestDir(t, configPath)
	store, err := memory.NewJSONLStore(dir)
	if err != nil {
		t.Fatalf("NewJSONLStore() error = %v", err)
	}

	sessionKey := picoSessionPrefix + "detail-tool-summary-distinct-content"
	for _, msg := range []providers.Message{
		{Role: "user", Content: "check file"},
		{
			Role:    "assistant",
			Content: "I will summarize the findings after reading the file.",
			ToolCalls: []providers.ToolCall{
				{
					ID:   "call_1",
					Type: "function",
					Function: &providers.FunctionCall{
						Name:      "read_file",
						Arguments: `{"path":"README.md","start_line":1,"end_line":10}`,
					},
					ExtraContent: &providers.ExtraContent{
						ToolFeedbackExplanation: "Read the file before replying.",
					},
				},
			},
		},
	} {
		if err := store.AddFullMessage(nil, sessionKey, msg); err != nil {
			t.Fatalf("AddFullMessage() error = %v", err)
		}
	}

	h := NewHandler(configPath)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/sessions/detail-tool-summary-distinct-content", nil)
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}

	var resp struct {
		Messages []sessionChatMessage `json:"messages"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}
	if len(resp.Messages) != 3 {
		t.Fatalf("len(resp.Messages) = %d, want 3", len(resp.Messages))
	}
	if resp.Messages[1].Role != "assistant" ||
		resp.Messages[1].Content != "I will summarize the findings after reading the file." {
		t.Fatalf("assistant content = %#v, want preserved distinct content", resp.Messages[1])
	}
	assertVisibleToolCallMessage(t, resp.Messages[2], "read_file")
}

func TestHandleGetSession_PreservesMediaWhenAssistantToolCallContentDuplicatesSummary(t *testing.T) {
	configPath, cleanup := setupOAuthTestEnv(t)
	defer cleanup()

	dir := sessionsTestDir(t, configPath)
	store, err := memory.NewJSONLStore(dir)
	if err != nil {
		t.Fatalf("NewJSONLStore() error = %v", err)
	}

	sessionKey := picoSessionPrefix + "detail-tool-summary-duplicate-content-with-media"
	for _, msg := range []providers.Message{
		{Role: "user", Content: "check screenshot"},
		{
			Role:    "assistant",
			Content: "Reviewing the generated screenshot.",
			Media:   []string{"data:image/png;base64,abc123"},
			ToolCalls: []providers.ToolCall{
				{
					ID:   "call_1",
					Type: "function",
					Function: &providers.FunctionCall{
						Name:      "view_image",
						Arguments: `{"path":"artifact.png"}`,
					},
					ExtraContent: &providers.ExtraContent{
						ToolFeedbackExplanation: "Reviewing the generated screenshot.",
					},
				},
			},
		},
	} {
		if err := store.AddFullMessage(nil, sessionKey, msg); err != nil {
			t.Fatalf("AddFullMessage() error = %v", err)
		}
	}

	h := NewHandler(configPath)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/sessions/detail-tool-summary-duplicate-content-with-media", nil)
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}

	var resp struct {
		Messages []sessionChatMessage `json:"messages"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}
	if len(resp.Messages) != 3 {
		t.Fatalf("len(resp.Messages) = %d, want 3", len(resp.Messages))
	}
	if resp.Messages[1].Role != "assistant" {
		t.Fatalf("assistant message role = %q, want assistant", resp.Messages[1].Role)
	}
	if resp.Messages[1].Content != "" {
		t.Fatalf("assistant content = %q, want duplicate content suppressed", resp.Messages[1].Content)
	}
	if len(resp.Messages[1].Media) != 1 || resp.Messages[1].Media[0] != "data:image/png;base64,abc123" {
		t.Fatalf("assistant media = %#v, want preserved media", resp.Messages[1].Media)
	}
	assertVisibleToolCallMessage(t, resp.Messages[2], "view_image")
}

func TestHandleGetSession_PreservesAttachmentsWhenAssistantToolCallContentDuplicatesSummary(t *testing.T) {
	configPath, cleanup := setupOAuthTestEnv(t)
	defer cleanup()

	dir := sessionsTestDir(t, configPath)
	store, err := memory.NewJSONLStore(dir)
	if err != nil {
		t.Fatalf("NewJSONLStore() error = %v", err)
	}

	sessionKey := picoSessionPrefix + "detail-tool-summary-duplicate-content-with-attachments"
	for _, msg := range []providers.Message{
		{Role: "user", Content: "check report"},
		{
			Role:    "assistant",
			Content: "Reviewing the generated report.",
			Attachments: []providers.Attachment{{
				Type:        "file",
				URL:         "https://example.com/report.txt",
				Filename:    "report.txt",
				ContentType: "text/plain",
			}},
			ToolCalls: []providers.ToolCall{
				{
					ID:   "call_1",
					Type: "function",
					Function: &providers.FunctionCall{
						Name:      "read_file",
						Arguments: `{"path":"report.txt"}`,
					},
					ExtraContent: &providers.ExtraContent{
						ToolFeedbackExplanation: "Reviewing the generated report.",
					},
				},
			},
		},
	} {
		if err := store.AddFullMessage(nil, sessionKey, msg); err != nil {
			t.Fatalf("AddFullMessage() error = %v", err)
		}
	}

	h := NewHandler(configPath)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(
		http.MethodGet,
		"/api/sessions/detail-tool-summary-duplicate-content-with-attachments",
		nil,
	)
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}

	var resp struct {
		Messages []sessionChatMessage `json:"messages"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}
	if len(resp.Messages) != 3 {
		t.Fatalf("len(resp.Messages) = %d, want 3", len(resp.Messages))
	}
	if resp.Messages[1].Role != "assistant" {
		t.Fatalf("assistant message role = %q, want assistant", resp.Messages[1].Role)
	}
	if resp.Messages[1].Content != "" {
		t.Fatalf("assistant content = %q, want duplicate content suppressed", resp.Messages[1].Content)
	}
	if len(resp.Messages[1].Attachments) != 1 {
		t.Fatalf("len(assistant.Attachments) = %d, want 1", len(resp.Messages[1].Attachments))
	}
	if resp.Messages[1].Attachments[0].URL != "https://example.com/report.txt" {
		t.Fatalf("attachment url = %q, want report URL", resp.Messages[1].Attachments[0].URL)
	}
	assertVisibleToolCallMessage(t, resp.Messages[2], "read_file")
}

func TestHandleGetSession_UsesConfiguredToolFeedbackMaxArgsLength(t *testing.T) {
	configPath, cleanup := setupOAuthTestEnv(t)
	defer cleanup()

	cfg, err := config.LoadConfig(configPath)
	if err != nil {
		t.Fatalf("LoadConfig() error = %v", err)
	}
	cfg.Agents.Defaults.ToolFeedback.MaxArgsLength = 20
	err = config.SaveConfig(configPath, cfg)
	if err != nil {
		t.Fatalf("SaveConfig() error = %v", err)
	}

	dir := sessionsTestDir(t, configPath)
	store, err := memory.NewJSONLStore(dir)
	if err != nil {
		t.Fatalf("NewJSONLStore() error = %v", err)
	}

	argsJSON := `{"path":"README.md","start_line":1,"end_line":10,"extra":"abcdefghijklmnopqrstuvwxyz"}`
	explanation := "Read README.md first to confirm the current project structure before editing the config example."
	sessionKey := picoSessionPrefix + "detail-tool-summary-max-args"
	err = store.AddFullMessage(nil, sessionKey, providers.Message{Role: "user", Content: "check file"})
	if err != nil {
		t.Fatalf("AddFullMessage(user) error = %v", err)
	}
	err = store.AddFullMessage(nil, sessionKey, providers.Message{
		Role: "assistant",
		ToolCalls: []providers.ToolCall{{
			ID:   "call_1",
			Type: "function",
			Function: &providers.FunctionCall{
				Name:      "read_file",
				Arguments: argsJSON,
			},
			ExtraContent: &providers.ExtraContent{
				ToolFeedbackExplanation: explanation,
			},
		}},
	})
	if err != nil {
		t.Fatalf("AddFullMessage(assistant) error = %v", err)
	}

	h := NewHandler(configPath)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/sessions/detail-tool-summary-max-args", nil)
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}

	var resp struct {
		Messages []sessionChatMessage `json:"messages"`
	}
	err = json.Unmarshal(rec.Body.Bytes(), &resp)
	if err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}
	if len(resp.Messages) < 2 {
		t.Fatalf("len(resp.Messages) = %d, want at least 2", len(resp.Messages))
	}

	wantArgsPreview := visibleAssistantToolArgsPreview(providers.ToolCall{
		Function: &providers.FunctionCall{Arguments: argsJSON},
	}, 20)
	toolCall := assertVisibleToolCallMessage(t, resp.Messages[1], "read_file")
	if toolCall.ExtraContent == nil || toolCall.ExtraContent.ToolFeedbackExplanation != explanation {
		t.Fatalf("tool call = %#v, want full explanation %q", toolCall, explanation)
	}
	if toolCall.Function == nil || toolCall.Function.Arguments != wantArgsPreview {
		t.Fatalf("tool call = %#v, want args preview %q", toolCall, wantArgsPreview)
	}
}

func TestHandleGetSession_FallsBackToLegacyToolArgumentsWhenExplanationMissing(t *testing.T) {
	configPath, cleanup := setupOAuthTestEnv(t)
	defer cleanup()

	cfg, err := config.LoadConfig(configPath)
	if err != nil {
		t.Fatalf("LoadConfig() error = %v", err)
	}
	cfg.Agents.Defaults.ToolFeedback.MaxArgsLength = 20
	err = config.SaveConfig(configPath, cfg)
	if err != nil {
		t.Fatalf("SaveConfig() error = %v", err)
	}

	dir := sessionsTestDir(t, configPath)
	store, err := memory.NewJSONLStore(dir)
	if err != nil {
		t.Fatalf("NewJSONLStore() error = %v", err)
	}

	argsJSON := `{"path":"README.md","start_line":1,"end_line":10,"extra":"abcdefghijklmnopqrstuvwxyz"}`
	sessionKey := picoSessionPrefix + "detail-tool-summary-legacy-args"
	if err := store.AddFullMessage(
		nil,
		sessionKey,
		providers.Message{Role: "user", Content: "check file"},
	); err != nil {
		t.Fatalf("AddFullMessage(user) error = %v", err)
	}
	if err := store.AddFullMessage(nil, sessionKey, providers.Message{
		Role: "assistant",
		ToolCalls: []providers.ToolCall{{
			ID:   "call_1",
			Type: "function",
			Function: &providers.FunctionCall{
				Name:      "read_file",
				Arguments: argsJSON,
			},
		}},
	}); err != nil {
		t.Fatalf("AddFullMessage(assistant) error = %v", err)
	}

	h := NewHandler(configPath)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/sessions/detail-tool-summary-legacy-args", nil)
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}

	var resp struct {
		Messages []sessionChatMessage `json:"messages"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}
	if len(resp.Messages) < 2 {
		t.Fatalf("len(resp.Messages) = %d, want at least 2", len(resp.Messages))
	}

	wantPreview := visibleAssistantToolArgsPreview(providers.ToolCall{
		Function: &providers.FunctionCall{Arguments: argsJSON},
	}, 20)
	toolCall := assertVisibleToolCallMessage(t, resp.Messages[1], "read_file")
	if toolCall.Function == nil || toolCall.Function.Arguments != wantPreview {
		t.Fatalf("tool call = %#v, want legacy args preview %q", toolCall, wantPreview)
	}
}

func TestHandleGetSession_IncludesMediaOnlyMessages(t *testing.T) {
	configPath, cleanup := setupOAuthTestEnv(t)
	defer cleanup()

	dir := sessionsTestDir(t, configPath)
	store, err := memory.NewJSONLStore(dir)
	if err != nil {
		t.Fatalf("NewJSONLStore() error = %v", err)
	}

	sessionKey := picoSessionPrefix + "detail-media-only"
	if err := store.AddFullMessage(nil, sessionKey, providers.Message{
		Role:  "user",
		Media: []string{"data:image/png;base64,abc123"},
	}); err != nil {
		t.Fatalf("AddFullMessage(user) error = %v", err)
	}

	h := NewHandler(configPath)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/sessions/detail-media-only", nil)
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}

	var resp struct {
		Messages []struct {
			Role    string   `json:"role"`
			Content string   `json:"content"`
			Media   []string `json:"media"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}
	if len(resp.Messages) != 1 {
		t.Fatalf("len(resp.Messages) = %d, want 1", len(resp.Messages))
	}
	if resp.Messages[0].Role != "user" || len(resp.Messages[0].Media) != 1 {
		t.Fatalf("message = %#v, want user message with media", resp.Messages[0])
	}
}

func TestHandleSessions_SupportsJSONLMessagesUpToStoreCap(t *testing.T) {
	configPath, cleanup := setupOAuthTestEnv(t)
	defer cleanup()

	dir := sessionsTestDir(t, configPath)
	store, err := memory.NewJSONLStore(dir)
	if err != nil {
		t.Fatalf("NewJSONLStore() error = %v", err)
	}

	sessionKey := picoSessionPrefix + "detail-large-jsonl"
	largeContent := strings.Repeat("x", 9*1024*1024)
	if err := store.AddFullMessage(nil, sessionKey, providers.Message{
		Role:    "user",
		Content: largeContent,
	}); err != nil {
		t.Fatalf("AddFullMessage() error = %v", err)
	}

	h := NewHandler(configPath)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	listRec := httptest.NewRecorder()
	listReq := httptest.NewRequest(http.MethodGet, "/api/sessions", nil)
	mux.ServeHTTP(listRec, listReq)

	if listRec.Code != http.StatusOK {
		t.Fatalf("list status = %d, want %d, body=%s", listRec.Code, http.StatusOK, listRec.Body.String())
	}

	var items []sessionListItem
	if err := json.Unmarshal(listRec.Body.Bytes(), &items); err != nil {
		t.Fatalf("list Unmarshal() error = %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("len(items) = %d, want 1", len(items))
	}

	detailRec := httptest.NewRecorder()
	detailReq := httptest.NewRequest(http.MethodGet, "/api/sessions/detail-large-jsonl", nil)
	mux.ServeHTTP(detailRec, detailReq)

	if detailRec.Code != http.StatusOK {
		t.Fatalf(
			"detail status = %d, want %d, body=%s",
			detailRec.Code,
			http.StatusOK,
			detailRec.Body.String(),
		)
	}

	var resp struct {
		Messages []struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(detailRec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("detail Unmarshal() error = %v", err)
	}
	if len(resp.Messages) != 1 {
		t.Fatalf("len(resp.Messages) = %d, want 1", len(resp.Messages))
	}
	if resp.Messages[0].Role != "user" {
		t.Fatalf("resp.Messages[0].Role = %q, want %q", resp.Messages[0].Role, "user")
	}
	if got := len(resp.Messages[0].Content); got != len(largeContent) {
		t.Fatalf("len(resp.Messages[0].Content) = %d, want %d", got, len(largeContent))
	}
}

func TestHandleListSessions_UsesImagePreviewForMediaOnlyMessage(t *testing.T) {
	configPath, cleanup := setupOAuthTestEnv(t)
	defer cleanup()

	dir := sessionsTestDir(t, configPath)
	store, err := memory.NewJSONLStore(dir)
	if err != nil {
		t.Fatalf("NewJSONLStore() error = %v", err)
	}

	sessionKey := picoSessionPrefix + "preview-media-only"
	if err := store.AddFullMessage(nil, sessionKey, providers.Message{
		Role:  "user",
		Media: []string{"data:image/png;base64,abc123"},
	}); err != nil {
		t.Fatalf("AddFullMessage() error = %v", err)
	}

	h := NewHandler(configPath)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/sessions", nil)
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}

	var items []sessionListItem
	if err := json.Unmarshal(rec.Body.Bytes(), &items); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("len(items) = %d, want 1", len(items))
	}
	if items[0].Preview != "[image]" {
		t.Fatalf("items[0].Preview = %q, want %q", items[0].Preview, "[image]")
	}
	if items[0].MessageCount != 1 {
		t.Fatalf("items[0].MessageCount = %d, want 1", items[0].MessageCount)
	}
}

func TestHandleDeleteSession_JSONLStorage(t *testing.T) {
	configPath, cleanup := setupOAuthTestEnv(t)
	defer cleanup()

	dir := sessionsTestDir(t, configPath)
	store, err := memory.NewJSONLStore(dir)
	if err != nil {
		t.Fatalf("NewJSONLStore() error = %v", err)
	}

	sessionKey := legacyPicoSessionPrefix + "delete-jsonl"
	if addErr := store.AddFullMessage(nil, sessionKey, providers.Message{
		Role:    "user",
		Content: "delete me",
	}); addErr != nil {
		t.Fatalf("AddFullMessage() error = %v", addErr)
	}
	if summaryErr := store.SetSummary(nil, sessionKey, "delete summary"); summaryErr != nil {
		t.Fatalf("SetSummary() error = %v", summaryErr)
	}
	base := filepath.Join(dir, sanitizeSessionKey(sessionKey))
	writeSessionMessageFile(t, base+".history-a", providers.Message{
		Role: "user", Content: "delete active slot",
	})
	writeSessionMessageFile(t, base+".history-b", providers.Message{
		Role: "user", Content: "delete inactive slot",
	})
	meta, err := store.GetSessionMeta(nil, sessionKey)
	if err != nil {
		t.Fatalf("GetSessionMeta() error = %v", err)
	}
	meta.HistorySlot = "a"
	meta.Skip = 0
	meta.Count = 1
	writeSessionMetaFile(t, base+".meta.json", meta)

	h := NewHandler(configPath)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodDelete, "/api/sessions/delete-jsonl", nil)
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want %d, body=%s", rec.Code, http.StatusNoContent, rec.Body.String())
	}

	for _, path := range []string{
		base + ".jsonl",
		base + ".history-a",
		base + ".history-b",
		base + ".meta.json",
	} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("expected %s to be removed, stat err = %v", path, err)
		}
	}
}

func TestHandleGetSession_LegacyJSONFallback(t *testing.T) {
	configPath, cleanup := setupOAuthTestEnv(t)
	defer cleanup()

	dir := sessionsTestDir(t, configPath)
	manager := session.NewSessionManager(dir)
	sessionKey := legacyPicoSessionPrefix + "legacy-json"
	manager.AddMessage(sessionKey, "user", "legacy user")
	manager.AddMessage(sessionKey, "assistant", "legacy assistant")
	if err := manager.Save(sessionKey); err != nil {
		t.Fatalf("Save() error = %v", err)
	}

	h := NewHandler(configPath)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/sessions/legacy-json", nil)
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
}

func TestHandleSessions_FiltersEmptyJSONLFiles(t *testing.T) {
	configPath, cleanup := setupOAuthTestEnv(t)
	defer cleanup()

	dir := sessionsTestDir(t, configPath)
	base := filepath.Join(dir, sanitizeSessionKey(legacyPicoSessionPrefix+"empty-jsonl"))
	if err := os.WriteFile(base+".jsonl", []byte{}, 0o644); err != nil {
		t.Fatalf("WriteFile(jsonl) error = %v", err)
	}

	h := NewHandler(configPath)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	listRec := httptest.NewRecorder()
	listReq := httptest.NewRequest(http.MethodGet, "/api/sessions", nil)
	mux.ServeHTTP(listRec, listReq)

	if listRec.Code != http.StatusOK {
		t.Fatalf("list status = %d, want %d, body=%s", listRec.Code, http.StatusOK, listRec.Body.String())
	}

	var items []sessionListItem
	if err := json.Unmarshal(listRec.Body.Bytes(), &items); err != nil {
		t.Fatalf("Unmarshal(list) error = %v", err)
	}
	if len(items) != 0 {
		t.Fatalf("len(items) = %d, want 0", len(items))
	}

	detailRec := httptest.NewRecorder()
	detailReq := httptest.NewRequest(http.MethodGet, "/api/sessions/empty-jsonl", nil)
	mux.ServeHTTP(detailRec, detailReq)

	if detailRec.Code != http.StatusNotFound {
		t.Fatalf("detail status = %d, want %d, body=%s", detailRec.Code, http.StatusNotFound, detailRec.Body.String())
	}
}

func TestHandleSessions_ListsLegacyJSONLWithoutMeta(t *testing.T) {
	configPath, cleanup := setupOAuthTestEnv(t)
	defer cleanup()

	dir := sessionsTestDir(t, configPath)
	sessionKey := legacyPicoSessionPrefix + "missing-meta"
	base := filepath.Join(dir, sanitizeSessionKey(sessionKey))
	line, err := json.Marshal(providers.Message{Role: "user", Content: "recover me"})
	if err != nil {
		t.Fatalf("Marshal(message) error = %v", err)
	}
	if err := os.WriteFile(base+".jsonl", append(line, '\n'), 0o644); err != nil {
		t.Fatalf("WriteFile(jsonl) error = %v", err)
	}

	h := NewHandler(configPath)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	listRec := httptest.NewRecorder()
	listReq := httptest.NewRequest(http.MethodGet, "/api/sessions", nil)
	mux.ServeHTTP(listRec, listReq)

	if listRec.Code != http.StatusOK {
		t.Fatalf("list status = %d, want %d, body=%s", listRec.Code, http.StatusOK, listRec.Body.String())
	}

	var items []sessionListItem
	if err := json.Unmarshal(listRec.Body.Bytes(), &items); err != nil {
		t.Fatalf("Unmarshal(list) error = %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("len(items) = %d, want 1", len(items))
	}
	if items[0].ID != "missing-meta" {
		t.Fatalf("items[0].ID = %q, want %q", items[0].ID, "missing-meta")
	}

	detailRec := httptest.NewRecorder()
	detailReq := httptest.NewRequest(http.MethodGet, "/api/sessions/missing-meta", nil)
	mux.ServeHTTP(detailRec, detailReq)

	if detailRec.Code != http.StatusOK {
		t.Fatalf("detail status = %d, want %d, body=%s", detailRec.Code, http.StatusOK, detailRec.Body.String())
	}
}

func TestHandleSessions_MetadataLessOpaqueJSONLCanBeDeleted(t *testing.T) {
	configPath, cleanup := setupOAuthTestEnv(t)
	defer cleanup()

	dir := sessionsTestDir(t, configPath)
	const sessionKey = "sk_v1_metadata-less"
	historyPath := filepath.Join(dir, sessionKey+".jsonl")
	line, err := json.Marshal(providers.Message{Role: "user", Content: "orphan history"})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(historyPath, append(line, '\n'), 0o644); err != nil {
		t.Fatal(err)
	}

	h := NewHandler(configPath)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	listRec := httptest.NewRecorder()
	mux.ServeHTTP(listRec, httptest.NewRequest(http.MethodGet, "/api/sessions", nil))
	if listRec.Code != http.StatusOK || !strings.Contains(listRec.Body.String(), sessionKey) {
		t.Fatalf("list status=%d body=%s", listRec.Code, listRec.Body.String())
	}
	detailRec := httptest.NewRecorder()
	mux.ServeHTTP(
		detailRec,
		httptest.NewRequest(http.MethodGet, "/api/sessions/"+sessionKey, nil),
	)
	if detailRec.Code != http.StatusOK || !strings.Contains(detailRec.Body.String(), "orphan history") {
		t.Fatalf("detail status=%d body=%s", detailRec.Code, detailRec.Body.String())
	}
	deleteRec := httptest.NewRecorder()
	mux.ServeHTTP(
		deleteRec,
		httptest.NewRequest(http.MethodDelete, "/api/sessions/"+sessionKey, nil),
	)
	if deleteRec.Code != http.StatusNoContent {
		t.Fatalf("delete status=%d body=%s", deleteRec.Code, deleteRec.Body.String())
	}
	if _, err := os.Stat(historyPath); !os.IsNotExist(err) {
		t.Fatalf("deleted orphan history stat error = %v", err)
	}
}

func TestHandleSessions_IgnoresMetaJSONInLegacyFallback(t *testing.T) {
	configPath, cleanup := setupOAuthTestEnv(t)
	defer cleanup()

	dir := sessionsTestDir(t, configPath)
	metaOnly := filepath.Join(dir, "agent_main_pico_direct_pico_meta-only.meta.json")
	metaOnlyContent := []byte(`{"key":"agent:main:pico:direct:pico:meta-only","summary":"meta only"}`)
	if err := os.WriteFile(metaOnly, metaOnlyContent, 0o644); err != nil {
		t.Fatalf("WriteFile(meta) error = %v", err)
	}

	h := NewHandler(configPath)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	listRec := httptest.NewRecorder()
	listReq := httptest.NewRequest(http.MethodGet, "/api/sessions", nil)
	mux.ServeHTTP(listRec, listReq)

	if listRec.Code != http.StatusOK {
		t.Fatalf("list status = %d, want %d, body=%s", listRec.Code, http.StatusOK, listRec.Body.String())
	}

	var items []sessionListItem
	if err := json.Unmarshal(listRec.Body.Bytes(), &items); err != nil {
		t.Fatalf("Unmarshal(list) error = %v", err)
	}
	if len(items) != 0 {
		t.Fatalf("len(items) = %d, want 0", len(items))
	}
}
