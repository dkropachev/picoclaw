package session

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/sipeed/picoclaw/pkg/providers"
)

var _ SnapshotReader = (*SessionManager)(nil)

func TestSanitizeFilename(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"simple", "simple"},
		{"telegram:123456", "telegram_123456"},
		{"discord:987654321", "discord_987654321"},
		{"slack:C01234", "slack_C01234"},
		{"no-colons-here", "no-colons-here"},
		{"multiple:colons:here", "multiple_colons_here"},
		{"agent:main:telegram:group:-1003822706455/12", "agent_main_telegram_group_-1003822706455_12"},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := sanitizeFilename(tt.input)
			if got != tt.expected {
				t.Errorf("sanitizeFilename(%q) = %q, want %q", tt.input, got, tt.expected)
			}
		})
	}
}

func TestSave_WithColonInKey(t *testing.T) {
	tmpDir := t.TempDir()
	sm := NewSessionManager(tmpDir)

	// Create a session with a key containing colon (typical channel session key).
	key := "telegram:123456"
	sm.GetOrCreate(key)
	sm.AddMessage(key, "user", "hello")

	// Save should succeed even though the key contains ':'
	if err := sm.Save(key); err != nil {
		t.Fatalf("Save(%q) failed: %v", key, err)
	}

	// The file on disk should use sanitized name.
	expectedFile := filepath.Join(tmpDir, "telegram_123456.json")
	if _, err := os.Stat(expectedFile); os.IsNotExist(err) {
		t.Fatalf("expected session file %s to exist", expectedFile)
	}

	// Load into a fresh manager and verify the session round-trips.
	sm2 := NewSessionManager(tmpDir)
	history := sm2.GetHistory(key)
	if len(history) != 1 {
		t.Fatalf("expected 1 message after reload, got %d", len(history))
	}
	if history[0].Content != "hello" {
		t.Errorf("expected message content %q, got %q", "hello", history[0].Content)
	}
}

func TestSave_RejectsPathTraversal(t *testing.T) {
	tmpDir := t.TempDir()
	sm := NewSessionManager(tmpDir)

	// Invalid names that must still be rejected.
	badKeys := []string{"", ".", ".."}
	for _, key := range badKeys {
		sm.GetOrCreate(key)
		if err := sm.Save(key); err == nil {
			t.Errorf("Save(%q) should have failed but didn't", key)
		}
	}

	// Keys containing path separators are sanitized (no subdirs created).
	sm.GetOrCreate("foo/bar")
	if err := sm.Save("foo/bar"); err != nil {
		t.Fatalf("Save(\"foo/bar\") after sanitize should succeed: %v", err)
	}
	if _, err := os.Stat(filepath.Join(tmpDir, "foo_bar.json")); os.IsNotExist(err) {
		t.Errorf("expected foo_bar.json in storage (sanitized from foo/bar)")
	}
}

func TestLoadSessions_NormalizesMissingCreatedAt(t *testing.T) {
	tmpDir := t.TempDir()
	sessionPath := filepath.Join(tmpDir, "telegram_legacy.json")
	legacy := `{
  "key": "telegram:legacy",
  "messages": [
    {
      "role": "user",
      "content": "hello"
    }
  ],
  "created": "2026-01-01T00:00:00Z",
  "updated": "2026-01-01T00:00:00Z"
}`

	if err := os.WriteFile(sessionPath, []byte(legacy), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	sm := NewSessionManager(tmpDir)
	history := sm.GetHistory("telegram:legacy")
	if len(history) != 1 {
		t.Fatalf("history = %d, want 1", len(history))
	}
	if history[0].CreatedAt == nil || history[0].CreatedAt.IsZero() {
		t.Fatalf("history[0].CreatedAt = %v, want non-zero timestamp", history[0].CreatedAt)
	}
}

func TestSessionManagerReadSessionSnapshot_ExactDeepCopy(t *testing.T) {
	sm := NewSessionManager("")
	key := " exact-session "
	createdAt := time.Date(2026, time.August, 1, 12, 0, 0, 0, time.UTC)
	cyclic := map[string]any{}
	cyclic["self"] = cyclic
	sm.AddFullMessage(key, providers.Message{
		Role:      "assistant",
		Content:   "decision context",
		CreatedAt: &createdAt,
		Media:     []string{"image.png"},
		SystemParts: []providers.ContentBlock{{
			Type:         "text",
			Text:         "system",
			CacheControl: &providers.CacheControl{Type: "ephemeral"},
		}},
		ToolCalls: []providers.ToolCall{{
			ID:        "call-1",
			Function:  &providers.FunctionCall{Name: "inspect", Arguments: `{}`},
			Arguments: map[string]any{"nested": []any{map[string]any{"value": "original"}}, "cycle": cyclic},
		}},
	})
	sm.SetSummary(key, "summary")

	if _, found, err := sm.ReadSessionSnapshot(context.Background(), "exact-session"); err != nil || found {
		t.Fatalf("trimmed lookup = (found=%v, err=%v), want exact-key miss", found, err)
	}
	snapshot, found, err := sm.ReadSessionSnapshot(context.Background(), key)
	if err != nil || !found {
		t.Fatalf("ReadSessionSnapshot() = (found=%v, err=%v)", found, err)
	}
	if snapshot.Key != key || snapshot.Summary != "summary" || len(snapshot.History) != 1 {
		t.Fatalf("snapshot = %+v", snapshot)
	}

	originalCycle := cyclic
	clonedCycle := snapshot.History[0].ToolCalls[0].Arguments["cycle"].(map[string]any)
	if reflect.ValueOf(originalCycle).Pointer() == reflect.ValueOf(clonedCycle).Pointer() {
		t.Fatal("cyclic tool arguments were not cloned")
	}
	if reflect.ValueOf(clonedCycle).Pointer() != reflect.ValueOf(clonedCycle["self"]).Pointer() {
		t.Fatal("cyclic tool argument graph was not preserved")
	}

	snapshot.History[0].Content = "mutated"
	*snapshot.History[0].CreatedAt = time.Time{}
	snapshot.History[0].Media[0] = "mutated.png"
	snapshot.History[0].SystemParts[0].CacheControl.Type = "mutated"
	snapshot.History[0].ToolCalls[0].Function.Name = "mutated"
	nested := snapshot.History[0].ToolCalls[0].Arguments["nested"].([]any)
	nested[0].(map[string]any)["value"] = "mutated"

	again, found, err := sm.ReadSessionSnapshot(context.Background(), key)
	if err != nil || !found {
		t.Fatalf("second ReadSessionSnapshot() = (found=%v, err=%v)", found, err)
	}
	message := again.History[0]
	if message.Content != "decision context" || message.CreatedAt.IsZero() || message.Media[0] != "image.png" {
		t.Fatalf("live message changed through snapshot: %+v", message)
	}
	if message.SystemParts[0].CacheControl.Type != "ephemeral" || message.ToolCalls[0].Function.Name != "inspect" {
		t.Fatalf("live nested fields changed through snapshot: %+v", message)
	}
	gotNested := message.ToolCalls[0].Arguments["nested"].([]any)[0].(map[string]any)["value"]
	if gotNested != "original" {
		t.Fatalf("live tool arguments changed through snapshot: %v", gotNested)
	}
}

func TestSessionManagerReadSessionSnapshot_MissingBlankAndCanceled(t *testing.T) {
	sm := NewSessionManager("")
	for _, key := range []string{"", "   ", "missing"} {
		if _, found, err := sm.ReadSessionSnapshot(context.Background(), key); err != nil || found {
			t.Fatalf("ReadSessionSnapshot(%q) = (found=%v, err=%v), want miss", key, found, err)
		}
	}
	if sessions := sm.ListSessions(); len(sessions) != 0 {
		t.Fatalf("strict reads created sessions: %v", sessions)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, found, err := sm.ReadSessionSnapshot(ctx, "missing"); err != context.Canceled || found {
		t.Fatalf("canceled read = (found=%v, err=%v), want context.Canceled", found, err)
	}
}

func TestSessionManagerCompatibilitySummaryAndHistoryMutators(t *testing.T) {
	sm := NewSessionManager("")
	if got := sm.GetSummary("missing"); got != "" {
		t.Fatalf("GetSummary(missing) = %q", got)
	}
	sm.SetSummary("missing", "ignored")
	sm.SetHistory("missing", []providers.Message{{Role: "user", Content: "ignored"}})
	sm.TruncateHistory("missing", 1)

	const key = "compatibility"
	sm.GetOrCreate(key)
	sm.SetSummary(key, "summary")
	if got := sm.GetSummary(key); got != "summary" {
		t.Fatalf("GetSummary() = %q", got)
	}

	history := []providers.Message{
		{Role: "user", Content: "one"},
		{Role: "assistant", Content: "two"},
		{Role: "user", Content: "three"},
	}
	sm.SetHistory(key, history)
	history[0].Content = "caller mutation"
	if got := sm.GetHistory(key); len(got) != 3 || got[0].Content != "one" {
		t.Fatalf("SetHistory() stored history = %#v", got)
	}

	sm.TruncateHistory(key, 3)
	if got := sm.GetHistory(key); len(got) != 3 {
		t.Fatalf("no-op TruncateHistory() length = %d", len(got))
	}
	sm.TruncateHistory(key, 2)
	if got := sm.GetHistory(key); len(got) != 2 || got[0].Content != "two" {
		t.Fatalf("TruncateHistory(2) = %#v", got)
	}
	sm.TruncateHistory(key, 0)
	if got := sm.GetHistory(key); len(got) != 0 {
		t.Fatalf("TruncateHistory(0) = %#v", got)
	}
	if err := sm.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
}
