//nolint:govet // Independent transport assertions intentionally reuse err.
package matrix

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"

	"github.com/sipeed/picoclaw/pkg/bus"
	"github.com/sipeed/picoclaw/pkg/channels"
	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/database"
	"github.com/sipeed/picoclaw/pkg/media"
)

func TestMatrixRuntimeHTTPAndMediaOperationMatrix(t *testing.T) {
	var (
		mu       sync.Mutex
		requests []string
	)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		mu.Lock()
		requests = append(requests, request.Method+" "+request.URL.Path)
		mu.Unlock()
		writer.Header().Set("Content-Type", "application/json")
		switch {
		case strings.Contains(request.URL.Path, "/media/") && strings.HasSuffix(request.URL.Path, "/upload"):
			_ = json.NewEncoder(writer).Encode(map[string]any{"content_uri": "mxc://matrix.test/uploaded"})
		case strings.Contains(request.URL.Path, "/send/"):
			_ = json.NewEncoder(writer).Encode(map[string]any{"event_id": "$sent:matrix.test"})
		case strings.Contains(request.URL.Path, "/redact/"):
			_ = json.NewEncoder(writer).Encode(map[string]any{"event_id": "$redacted:matrix.test"})
		case strings.Contains(request.URL.Path, "/typing/"):
			_, _ = writer.Write([]byte(`{}`))
		case strings.Contains(request.URL.Path, "/join/"):
			_ = json.NewEncoder(writer).Encode(map[string]any{"room_id": "!room:matrix.test"})
		case strings.HasSuffix(request.URL.Path, "/joined_members"):
			_ = json.NewEncoder(writer).Encode(map[string]any{"joined": map[string]any{
				"@bot:matrix.test": map[string]any{}, "@other:matrix.test": map[string]any{},
				"@third:matrix.test": map[string]any{},
			}})
		default:
			http.NotFound(writer, request)
		}
	}))
	defer server.Close()
	settings := &config.MatrixSettings{
		Homeserver: server.URL, UserID: "@bot:matrix.test", MessageFormat: "plain", JoinOnInvite: true,
	}
	settings.AccessToken.Set("token")
	baseConfig := &config.Channel{Type: config.ChannelMatrix, Enabled: true}
	baseConfig.Placeholder.Enabled = true
	baseConfig.Placeholder.Text = config.FlexibleStringSlice{"working"}
	messageBus := bus.NewMessageBus()
	defer messageBus.Close()
	channel, err := newMatrixChannel(
		baseConfig, settings, messageBus,
		mustMatrixStoreID(t, "channel.matrix.coverage"),
	)
	if err != nil {
		t.Fatal(err)
	}
	channel.SetRunning(true)
	IDs, sendErr := channel.Send(
		t.Context(),
		bus.OutboundMessage{ChatID: "!room:matrix.test", Content: "hello"},
	)
	if sendErr != nil ||
		len(IDs) != 1 ||
		IDs[0] != "$sent:matrix.test" {
		t.Fatalf("Send = %#v, %v", IDs, sendErr)
	}
	if IDs, err := channel.Send(t.Context(), bus.OutboundMessage{ChatID: "!room:matrix.test"}); err != nil ||
		IDs != nil {
		t.Fatalf("empty Send = %#v, %v", IDs, err)
	}
	if _, err := channel.Send(t.Context(), bus.OutboundMessage{Content: "bad"}); err == nil {
		t.Fatal("empty room Send accepted")
	}
	placeholder, err := channel.SendPlaceholder(t.Context(), "!room:matrix.test")
	if err != nil || placeholder != "$sent:matrix.test" {
		t.Fatalf("placeholder = %q, %v", placeholder, err)
	}
	if err := channel.EditMessage(t.Context(), "!room:matrix.test", "$old:matrix.test", "edited"); err != nil {
		t.Fatal(err)
	}
	if err := channel.DeleteMessage(t.Context(), "!room:matrix.test", "$old:matrix.test"); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		edit bool
		room string
		ID   string
	}{
		{true, "", "$id"}, {true, "!room:test", ""}, {false, "", "$id"}, {false, "!room:test", ""},
	} {
		if test.edit {
			if err := channel.EditMessage(t.Context(), test.room, test.ID, "x"); err == nil {
				t.Errorf("invalid edit %#v accepted", test)
			}
		} else if err := channel.DeleteMessage(t.Context(), test.room, test.ID); err == nil {
			t.Errorf("invalid delete %#v accepted", test)
		}
	}

	store := media.NewFileMediaStore()
	channel.SetMediaStore(store)
	localPath := filepath.Join(t.TempDir(), "document.txt")
	if err := os.WriteFile(localPath, []byte("matrix media"), 0o600); err != nil {
		t.Fatal(err)
	}
	ref, err := store.Store(localPath, media.MediaMeta{
		Filename: "document.txt", ContentType: "text/plain", Source: "test",
		CleanupPolicy: media.CleanupPolicyForgetOnly,
	}, "scope")
	if err != nil {
		t.Fatal(err)
	}
	mediaIDs, mediaErr := channel.SendMedia(t.Context(), bus.OutboundMediaMessage{
		ChatID: "!room:matrix.test", Parts: []bus.MediaPart{{Ref: ref, Type: "file"}},
	})
	if mediaErr != nil || len(mediaIDs) != 1 || mediaIDs[0] != "$sent:matrix.test" {
		t.Fatalf("SendMedia = %#v, %v", mediaIDs, mediaErr)
	}
	if _, err := channel.SendMedia(t.Context(), bus.OutboundMediaMessage{ChatID: ""}); err == nil {
		t.Fatal("empty room SendMedia accepted")
	}
	channel.SetMediaStore(nil)
	if _, err := channel.SendMedia(t.Context(), bus.OutboundMediaMessage{ChatID: "!room:matrix.test"}); err == nil {
		t.Fatal("missing media store accepted")
	}
	channel.SetMediaStore(store)

	stop, err := channel.StartTyping(t.Context(), "!room:matrix.test")
	if err != nil {
		t.Fatal(err)
	}
	stop()
	stop()
	if _, err := channel.StartTyping(t.Context(), ""); err == nil {
		t.Fatal("empty typing room accepted")
	}
	if group := channel.isGroupRoom(t.Context(), id.RoomID("!group:matrix.test")); !group {
		t.Fatal("two-member room classified direct")
	}
	if group := channel.isGroupRoom(t.Context(), id.RoomID("!group:matrix.test")); !group {
		t.Fatal("cached group classification changed")
	}

	stateKey := "@bot:matrix.test"
	channel.handleMemberEvent(t.Context(), nil)
	channel.handleMemberEvent(t.Context(), &event.Event{
		Type: event.StateMember, RoomID: id.RoomID("!room:matrix.test"), StateKey: &stateKey,
		Content: event.Content{Parsed: &event.MemberEventContent{Membership: event.MembershipInvite}},
	})
	channel.config.JoinOnInvite = false
	channel.handleMemberEvent(t.Context(), &event.Event{})
	channel.config.JoinOnInvite = true

	if err := channel.Stop(t.Context()); err != nil || channel.IsRunning() {
		t.Fatalf("Stop = %v, running=%v", err, channel.IsRunning())
	}
	_, stoppedSendErr := channel.Send(
		t.Context(), bus.OutboundMessage{ChatID: "!room:test", Content: "x"},
	)
	if stoppedSendErr != channels.ErrNotRunning {
		t.Fatalf("stopped Send error = %v", stoppedSendErr)
	}
	if _, err := channel.SendMedia(t.Context(), bus.OutboundMediaMessage{}); err != channels.ErrNotRunning {
		t.Fatalf("stopped SendMedia error = %v", err)
	}
	mu.Lock()
	requestCount := len(requests)
	mu.Unlock()
	if requestCount < 8 {
		t.Fatalf("Matrix request count = %d", requestCount)
	}
}

func TestMatrixPureRuntimeBoundaries(t *testing.T) {
	if outboundMessageIsToolFeedback(bus.OutboundMessage{}) || !outboundMessageIsToolFeedback(bus.OutboundMessage{
		Context: bus.InboundContext{Raw: map[string]string{"message_kind": " Tool_Feedback "}},
	}) {
		t.Fatal("tool feedback classification mismatch")
	}
	cache := newRoomKindCache(1, time.Second)
	if cache.evictOldestLocked() {
		t.Fatal("empty cache evicted entry")
	}
	now := time.Now()
	cache.set("room", true, now)
	cache.set("room", false, now.Add(time.Millisecond))
	if group, ok := cache.get("room", now.Add(2*time.Millisecond)); !ok || group {
		t.Fatalf("updated cache = %v/%v", group, ok)
	}
	if removed := cache.cleanupExpired(now.Add(2 * time.Second)); removed != 1 {
		t.Fatalf("expired removals = %d", removed)
	}
	session, _ := newTypingSession(context.Background())
	session.stop()
	session.stop()
	channel := &MatrixChannel{}
	if _, ok := channel.currentToolFeedbackMessage("chat"); ok {
		t.Fatal("nil progress returned current message")
	}
	if _, _, ok := channel.takeToolFeedbackMessage("chat"); ok {
		t.Fatal("nil progress returned taken message")
	}
	channel.RecordToolFeedbackMessage("chat", "id", "content")
	channel.ClearToolFeedbackMessage("chat")
	channel.DismissToolFeedbackMessage(t.Context(), "chat")
	if got := matrixMediaLabel(&event.MessageEventContent{FileName: "file.txt"}, "fallback"); got != "file.txt" {
		t.Fatalf("media label = %q", got)
	}
	if got := matrixMediaFilename("", "video", "video/mp4"); !strings.HasPrefix(got, "video.") {
		t.Fatalf("media filename = %q", got)
	}
	for input, want := range map[string]string{
		"https://matrix.to/#/%40user%3Amatrix.test": "https://matrix.to/#/@user:matrix.test",
		"https://example.test/user":                 "https://example.test/user",
	} {
		if got := decodeMatrixMentionHref(input); got != want {
			t.Errorf("decode href %q = %q, want %q", input, got, want)
		}
	}
}

func mustMatrixStoreID(t *testing.T, raw string) database.StoreID {
	t.Helper()
	value, err := database.ParseStoreID(raw)
	if err != nil {
		t.Fatal(err)
	}
	return value
}
