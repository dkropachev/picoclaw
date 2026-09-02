//nolint:govet // Independent media assertions intentionally reuse err.
package weixin

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/sipeed/picoclaw/pkg/bus"
	basechannels "github.com/sipeed/picoclaw/pkg/channels"
	"github.com/sipeed/picoclaw/pkg/config"
)

func TestWeixinMediaCodecSelectionAndFileBoundaries(t *testing.T) {
	key := []byte("1234567890abcdef")
	plaintext := []byte("coverage payload")
	ciphertext, err := encryptAESECB(plaintext, key)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := decryptAESECB(ciphertext, key); err != nil || !bytes.Equal(got, plaintext) {
		t.Fatalf("decrypt = %q, %v", got, err)
	}
	for _, padded := range [][]byte{nil, {1}, bytes.Repeat([]byte{17}, 16), {1, 2}} {
		if _, err := pkcs7Unpad(padded, 16); err == nil {
			t.Errorf("invalid padding %v accepted", padded)
		}
	}
	if _, err := decryptAESECB([]byte{1}, key); err == nil {
		t.Fatal("short ciphertext accepted")
	}
	if _, err := encryptAESECB([]byte("x"), []byte("short")); err == nil {
		t.Fatal("short AES key accepted")
	}
	for _, value := range []string{"%%%", base64.StdEncoding.EncodeToString([]byte("short"))} {
		if _, err := parseWeixinMediaAESKey(value); err == nil {
			t.Errorf("invalid media key %q accepted", value)
		}
	}
	if got, found, err := imageAESKey(nil); err != nil || found || got != nil {
		t.Fatalf("nil image key = %x/%v/%v", got, found, err)
	}
	if got, found, err := imageAESKey(&ImageItem{Aeskey: "zz"}); err == nil || found || got != nil {
		t.Fatalf("invalid image key = %x/%v/%v", got, found, err)
	}
	if got, found, err := imageAESKey(&ImageItem{Aeskey: "31323334353637383930616263646566"}); err != nil || !found ||
		!bytes.Equal(got, key) {
		t.Fatalf("direct image key = %x/%v/%v", got, found, err)
	}
	encoded := base64.StdEncoding.EncodeToString(key)
	if got, found, err := imageAESKey(&ImageItem{Media: &CDNMedia{AesKey: encoded}}); err != nil || !found ||
		!bytes.Equal(got, key) {
		t.Fatalf("media image key = %x/%v/%v", got, found, err)
	}
	if _, err := genericMediaAESKey(nil); err == nil {
		t.Fatal("nil generic key accepted")
	}
	if got, err := genericMediaAESKey(&CDNMedia{AesKey: encoded}); err != nil || !bytes.Equal(got, key) {
		t.Fatalf("generic key = %x, %v", got, err)
	}
	if aesEcbPaddedSize(0) != 16 || aesEcbPaddedSize(16) != 32 || aesEcbPaddedSize(17) != 32 {
		t.Fatal("AES padded size mismatch")
	}
	if value, err := randomHex(8); err != nil || len(value) != 16 {
		t.Fatalf("random hex = %q, %v", value, err)
	}
	if got := buildCDNUploadURL("https://cdn/", "a b", "key+"); !strings.Contains(got, "encrypted_query_param=a+b") ||
		!strings.Contains(got, "filekey=key%2B") {
		t.Fatalf("upload URL = %q", got)
	}
	if got := uniqCDNURLs([]string{" a ", "", "a", "b"}); len(got) != 2 || got[0] != "a" {
		t.Fatalf("unique URLs = %#v", got)
	}
	for code, want := range map[int]bool{0: true, 429: true, 500: true, 404: false, 200: false} {
		if shouldRetryCDNDownload(code) != want {
			t.Errorf("retry(%d) mismatch", code)
		}
	}

	filename, contentType := detectMediaMetadata([]byte("plain text"), "", "")
	if filename == "" || contentType == "" {
		t.Fatalf("metadata = %q/%q", filename, contentType)
	}
	if sanitizeFilename(" ../folder/file.txt ") != "file.txt" || sanitizeFilename(".") != "" {
		t.Fatal("filename sanitization mismatch")
	}
	path, err := writeManagedTempFile("coverage", "note.txt", []byte("hello"))
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(path)
	if got := detectLocalContentType(path, "text/custom"); got != "text/custom" {
		t.Fatalf("hint content type = %q", got)
	}
	if got := downloadFilenameFromURL("https://example.test/path/image.png", ""); got != "image.png" {
		t.Fatalf("download filename = %q", got)
	}
	if got := downloadFilenameFromURL(":bad", ""); got != "remote-media" {
		t.Fatalf("fallback download filename = %q", got)
	}
	for _, test := range []struct {
		partType, filename, contentType string
		want                            int
	}{
		{"image", "", "", UploadMediaTypeImage},
		{"video", "", "", UploadMediaTypeVideo},
		{"", "x", "image/png", UploadMediaTypeImage},
		{"", "x", "video/mp4", UploadMediaTypeVideo},
		{"file", "x", "application/pdf", UploadMediaTypeFile},
	} {
		if got := outboundMediaKind(test.partType, test.filename, test.contentType); got != test.want {
			t.Errorf("outbound kind %#v = %d", test, got)
		}
	}

	items := []*MessageItem{
		nil,
		{Type: MessageItemTypeImage},
		{Type: MessageItemTypeImage, ImageItem: &ImageItem{Media: &CDNMedia{FullURL: "https://image"}}},
		{Type: MessageItemTypeVideo, VideoItem: &VideoItem{Media: &CDNMedia{EncryptQueryParam: "video"}}},
		{Type: MessageItemTypeFile, FileItem: &FileItem{Media: &CDNMedia{FullURL: "https://file"}}},
		{Type: MessageItemTypeVoice, VoiceItem: &VoiceItem{Media: &CDNMedia{FullURL: "https://voice"}}},
		{
			Type:      MessageItemTypeVoice,
			VoiceItem: &VoiceItem{Text: "transcript", Media: &CDNMedia{FullURL: "https://voice"}},
		},
	}
	wants := []bool{false, false, true, true, true, true, false}
	for index, item := range items {
		if got := isDownloadableMediaItem(item); got != wants[index] {
			t.Errorf("downloadable[%d] = %v", index, got)
		}
	}
	message := WeixinMessage{ItemList: []MessageItem{*items[3], *items[2]}}
	if got := selectInboundMediaItem(message); got == nil || got.Type != MessageItemTypeImage {
		t.Fatalf("selected item = %#v", got)
	}
	if got := selectInboundMediaItem(WeixinMessage{}); got != nil {
		t.Fatalf("empty selection = %#v", got)
	}
}

func TestWeixinOutboundMediaAndTypingEndToEnd(t *testing.T) {
	setWeixinPersistenceHome(t)
	var (
		mu       sync.Mutex
		statuses []int
	)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		switch request.URL.Path {
		case "/remote.txt":
			_, _ = writer.Write([]byte("remote media"))
		case "/ilink/bot/getuploadurl":
			_ = json.NewEncoder(writer).Encode(GetUploadUrlResp{UploadParam: "upload-param"})
		case "/upload":
			writer.Header().Set("X-Encrypted-Param", "download-param")
		case "/ilink/bot/sendmessage":
			_ = json.NewEncoder(writer).Encode(SendMessageResp{})
		case "/ilink/bot/getconfig":
			_ = json.NewEncoder(writer).Encode(GetConfigResp{TypingTicket: "ticket"})
		case "/ilink/bot/sendtyping":
			var input SendTypingReq
			_ = json.NewDecoder(request.Body).Decode(&input)
			mu.Lock()
			statuses = append(statuses, input.Status)
			mu.Unlock()
			_ = json.NewEncoder(writer).Encode(SendTypingResp{})
		default:
			http.NotFound(writer, request)
		}
	}))
	defer server.Close()
	settings := &config.WeixinSettings{BaseURL: server.URL + "/", CDNBaseURL: server.URL}
	settings.SetToken("token")
	messageBus := bus.NewMessageBus()
	defer messageBus.Close()
	channel, err := NewWeixinChannel(
		&config.Channel{Type: config.ChannelWeixin, Enabled: true}, settings, messageBus,
	)
	if err != nil {
		t.Fatal(err)
	}
	channel.api.HttpClient = server.Client()
	channel.SetRunning(true)
	channel.contextTokens.Store("user", "context-token")
	localPath := filepath.Join(t.TempDir(), "report.txt")
	if err := os.WriteFile(localPath, []byte("local media data"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := channel.SendMedia(t.Context(), bus.OutboundMediaMessage{
		ChatID: "user", Parts: []bus.MediaPart{{
			Ref: localPath, Type: "file", Filename: "report.txt", Caption: "caption",
		}},
	}); err != nil {
		t.Fatalf("SendMedia = %v", err)
	}
	remotePath, name, contentType, cleanup, err := channel.resolveOutboundPart(t.Context(), bus.MediaPart{
		Ref: server.URL + "/remote.txt",
	})
	if err != nil || name != "remote.txt" || contentType == "" {
		t.Fatalf("remote resolution = %q/%q/%q, %v", remotePath, name, contentType, err)
	}
	cleanup()
	if _, err := os.Stat(remotePath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("remote cleanup = %v", err)
	}
	if _, _, _, _, err := channel.resolveOutboundPart(t.Context(), bus.MediaPart{Ref: "media://missing"}); err == nil {
		t.Fatal("missing media store accepted")
	}
	for _, ref := range []string{localPath, "file://" + localPath} {
		resolved, _, _, _, err := channel.resolveOutboundPart(t.Context(), bus.MediaPart{Ref: ref})
		if err != nil || resolved != localPath {
			t.Errorf("resolve %q = %q, %v", ref, resolved, err)
		}
	}
	stop, err := channel.StartTyping(t.Context(), "user")
	if err != nil {
		t.Fatal(err)
	}
	stop()
	stop()
	mu.Lock()
	gotStatuses := append([]int(nil), statuses...)
	mu.Unlock()
	if len(gotStatuses) != 2 || gotStatuses[0] != TypingStatusTyping || gotStatuses[1] != TypingStatusCancel {
		t.Fatalf("typing statuses = %#v", gotStatuses)
	}
	if _, err := channel.SendMedia(t.Context(), bus.OutboundMediaMessage{ChatID: "missing"}); !errors.Is(
		err,
		basechannels.ErrSendFailed,
	) {
		t.Fatalf("missing token error = %v", err)
	}
	channel.SetRunning(false)
	if _, err := channel.SendMedia(t.Context(), bus.OutboundMediaMessage{}); !errors.Is(
		err,
		basechannels.ErrNotRunning,
	) {
		t.Fatalf("stopped SendMedia error = %v", err)
	}
}

func TestWeixinDownloadErrorBoundaries(t *testing.T) {
	responses := []struct {
		status int
		body   string
	}{
		{http.StatusNotFound, "missing"},
		{http.StatusOK, "ok"},
	}
	index := 0
	channel := &WeixinChannel{
		config: &config.WeixinSettings{},
		api: &ApiClient{HttpClient: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			response := responses[index]
			index++
			return &http.Response{
				StatusCode: response.status, Header: make(http.Header),
				Body: io.NopCloser(strings.NewReader(response.body)),
			}, nil
		})}},
	}
	if _, _, err := channel.downloadCDNBufferOnce(context.Background(), "https://example.test/missing"); err == nil {
		t.Fatal("non-2xx CDN response accepted")
	}
	data, status, downloadErr := channel.downloadCDNBufferOnce(
		context.Background(), "https://example.test/ok",
	)
	if downloadErr != nil ||
		status != http.StatusOK ||
		string(data) != "ok" {
		t.Fatalf("CDN response = %q/%d/%v", data, status, downloadErr)
	}
	if _, err := channel.downloadCDNBuffer(context.Background(), "", ""); err == nil {
		t.Fatal("empty CDN candidates accepted")
	}
	channel.api.HttpClient = &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return nil, errors.New("transport")
	})}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := channel.downloadCDNBuffer(canceled, "token", ""); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled retry error = %v", err)
	}
}
