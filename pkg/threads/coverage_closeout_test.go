package threads

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/sipeed/picoclaw/pkg/providers"
	"github.com/sipeed/picoclaw/pkg/session"
)

func TestThreadCompatibilityPathPreviewAndParsingBoundaries(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	if got := ResolveWorkspace(""); got != filepath.Join(home, ".picoclaw", "workspace") {
		t.Fatalf("ResolveWorkspace(empty) = %q", got)
	}
	if got := ResolveWorkspace("~"); got != home {
		t.Fatalf("ResolveWorkspace(~) = %q", got)
	}
	if got := ResolveWorkspace("~/custom"); got != filepath.Join(home, "custom") {
		t.Fatalf("ResolveWorkspace(~/custom) = %q", got)
	}
	workspace := filepath.Join(home, "workspace")
	if ResolveSessionsDir(workspace) != filepath.Join(workspace, "sessions") ||
		ResolveThreadsDir(workspace) != filepath.Join(workspace, "threads") ||
		ResolveHandoffsDir(workspace) != filepath.Join(workspace, "threads", "handoffs") {
		t.Fatal("compatibility path helpers disagreed")
	}

	for name, test := range map[string]struct {
		message providers.Message
		want    string
	}{
		"content":    {providers.Message{Content: " text "}, "text"},
		"attachment": {providers.Message{Media: []string{"image"}}, "[attachment]"},
		"tool":       {providers.Message{ToolCalls: []providers.ToolCall{{ID: "call"}}}, "[tool call]"},
		"empty":      {providers.Message{}, ""},
	} {
		t.Run(name, func(t *testing.T) {
			if got := messagePreview(test.message); got != test.want {
				t.Fatalf("messagePreview() = %q, want %q", got, test.want)
			}
		})
	}
	if ParsePositiveInt(" 4 ", 9) != 4 || ParsePositiveInt("-1", 9) != 9 ||
		ParsePositiveInt("bad", 9) != 9 {
		t.Fatal("ParsePositiveInt compatibility boundary failed")
	}
	actual := errors.New("actual")
	if firstThreadError(actual, errors.New("fallback")) != actual {
		t.Fatal("firstThreadError did not preserve actual error")
	}
	fallback := errors.New("fallback")
	if firstThreadError(nil, fallback) != fallback {
		t.Fatal("firstThreadError did not use fallback")
	}
	if firstNonEmpty(" ", " chosen ", "later") != "chosen" || firstNonEmpty(" ") != "" {
		t.Fatal("firstNonEmpty boundary failed")
	}
	_ = os.Getenv("HOME")
}

func TestThreadMetadataNormalizationDefaultsAndOwnerFallbacks(t *testing.T) {
	workspace := t.TempDir()
	fromDir := (Store{Dir: filepath.Join(workspace, "sessions")}).withDefaults()
	if fromDir.Workspace != workspace || fromDir.ThreadsDir != filepath.Join(workspace, "threads") ||
		fromDir.HandoffsDir != filepath.Join(workspace, "threads", "handoffs") {
		t.Fatalf("directory-derived defaults = %#v", fromDir)
	}
	fromWorkspace := (Store{Workspace: workspace}).withDefaults()
	if fromWorkspace.Dir != filepath.Join(workspace, "sessions") {
		t.Fatalf("workspace-derived defaults = %#v", fromWorkspace)
	}
	if got := fromWorkspace.threadPath(" thread:a/b\\c "); got != filepath.Join(
		workspace, "threads", "thread_a_b_c.json",
	) {
		t.Fatalf("threadPath() = %q", got)
	}

	zero := time.Time{}
	meta := normalizeThreadMeta(ThreadMeta{
		ID: " id ", Title: " ", Type: "unknown", Context: map[string]string{" key ": " value "},
		SessionKeys: []string{" primary ", "", "primary"}, Aliases: []string{" alias ", "alias"},
		PrimarySessionKey: " primary ", DroppedAt: &zero, Registration: "unknown",
	})
	if meta.ID != "id" || meta.UISessionID != "id" || meta.AgentID != "main" ||
		meta.OwnerIdentity != "unknown" || meta.Title != "New thread" || meta.Type != TypeGeneral ||
		meta.Registration != RegistrationManual || meta.DroppedAt != nil || meta.CreatedAt.IsZero() ||
		!meta.UpdatedAt.Equal(meta.CreatedAt) || len(meta.SessionKeys) != 1 || len(meta.Aliases) != 1 {
		t.Fatalf("normalizeThreadMeta() = %#v", meta)
	}
	for input, want := range map[string]string{
		" AUTO ": RegistrationAuto, "tool": RegistrationTool, "manual": RegistrationManual,
		"MIGRATED": RegistrationMigrated, "bad": "",
	} {
		if got := normalizeRegistration(input); got != want {
			t.Fatalf("normalizeRegistration(%q) = %q, want %q", input, got, want)
		}
	}
	if ownerIdentityFromScope(nil) != "unknown" {
		t.Fatal("nil scope owner was not unknown")
	}
	for name, test := range map[string]struct {
		scope *session.SessionScope
		want  string
	}{
		"sender":  {&session.SessionScope{Values: map[string]string{"sender": " USER "}}, "user"},
		"chat":    {&session.SessionScope{Values: map[string]string{"chat": " CHAT "}}, "chat"},
		"account": {&session.SessionScope{Values: map[string]string{}, Account: " ACCOUNT "}, "account"},
		"agent":   {&session.SessionScope{Values: map[string]string{}, AgentID: " Worker "}, "agent:worker"},
		"empty":   {&session.SessionScope{Values: map[string]string{}}, "unknown"},
	} {
		t.Run(name, func(t *testing.T) {
			if got := ownerIdentityFromScope(test.scope); got != test.want {
				t.Fatalf("ownerIdentityFromScope() = %q, want %q", got, test.want)
			}
		})
	}
	if got := routingAgentFromSessionKey("agent:worker:telegram:chat"); got != "worker" {
		t.Fatalf("routingAgentFromSessionKey() = %q", got)
	}
	if got := routingAgentFromSessionKey("opaque"); got != "" {
		t.Fatalf("opaque routing agent = %q", got)
	}
	if got := uniqueStrings([]string{" a ", "", "a", "b"}); len(got) != 2 || got[0] != "a" || got[1] != "b" {
		t.Fatalf("uniqueStrings() = %v", got)
	}
	if contextOrBackground(nil) == nil || contextOrBackground(context.Background()) == nil {
		t.Fatal("contextOrBackground returned nil")
	}
}

//nolint:govet // Independent storage assertions intentionally use narrow error scopes.
func TestThreadStoreGetAliasesAndMissingIdentity(t *testing.T) {
	workspace := t.TempDir()
	store := NewStoreFromWorkspace(workspace)
	if err := os.MkdirAll(store.Dir, 0o700); err != nil {
		t.Fatal(err)
	}
	memoryStore, err := store.openSessionStore()
	if err != nil {
		t.Fatal(err)
	}
	if err := memoryStore.EnsureSessionHistory(context.Background(), "session-coverage"); err != nil {
		t.Fatal(err)
	}
	if err := memoryStore.Close(); err != nil {
		t.Fatal(err)
	}
	created, err := store.CreateThread(context.Background(), CreateRequest{
		ID: "thread-coverage", Title: "Coverage", Type: TypeCoding,
		PrimarySessionKey: "session-coverage", SessionKeys: []string{"session-coverage"},
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{created.ID, created.UISessionID, created.PrimarySessionKey} {
		got, found, err := store.Get(id)
		if err != nil || !found || got.ID != created.ID {
			t.Fatalf("Get(%q) = (%#v, %v, %v)", id, got, found, err)
		}
	}
	if got, found, err := store.Get(" "); err != nil || found || got.ID != "" {
		t.Fatalf("Get(blank) = (%#v, %v, %v)", got, found, err)
	}
	if got, found, err := store.Get("missing"); err != nil || found || got.ID != "" {
		t.Fatalf("Get(missing) = (%#v, %v, %v)", got, found, err)
	}
}
