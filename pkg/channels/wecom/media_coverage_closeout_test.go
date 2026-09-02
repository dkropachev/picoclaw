//nolint:govet // Independent media assertions intentionally reuse err.
package wecom

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"encoding/base64"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sipeed/picoclaw/pkg/bus"
)

func TestWeComMediaCodecAndClassificationBoundaries(t *testing.T) {
	for _, kind := range []string{"file", "image", "voice", "video"} {
		media := &wecomOutboundMedia{MsgType: kind, MediaID: "media", Title: "title", Description: "description"}
		respond := media.respondBody()
		send := media.sendBody("chat", 2)
		if respond.MsgType != kind || send.MsgType != kind || send.ChatID != "chat" {
			t.Errorf("%s bodies = %#v / %#v", kind, respond, send)
		}
	}
	if key, err := decodeMediaAESKey(""); err != nil || key != nil {
		t.Fatalf("empty key = %x, %v", key, err)
	}
	key := bytes.Repeat([]byte{7}, 32)
	for _, encoded := range []string{
		base64.StdEncoding.EncodeToString(key),
		strings.TrimSuffix(base64.StdEncoding.EncodeToString(key), "="),
	} {
		got, err := decodeMediaAESKey(encoded)
		if err != nil || !bytes.Equal(got, key) {
			t.Errorf("decode key = %x, %v", got, err)
		}
	}
	for _, encoded := range []string{"%%%", base64.StdEncoding.EncodeToString([]byte("short"))} {
		if _, err := decodeMediaAESKey(encoded); err == nil {
			t.Errorf("invalid key %q accepted", encoded)
		}
	}
	plaintext := []byte("wecom media")
	padding := aes.BlockSize - len(plaintext)%aes.BlockSize
	padded := append(append([]byte(nil), plaintext...), bytes.Repeat([]byte{byte(padding)}, padding)...)
	block, err := aes.NewCipher(key)
	if err != nil {
		t.Fatal(err)
	}
	ciphertext := make([]byte, len(padded))
	cipher.NewCBCEncrypter(block, key[:aes.BlockSize]).CryptBlocks(ciphertext, padded)
	if got, err := decryptAESCBC(key, ciphertext); err != nil || !bytes.Equal(got, plaintext) {
		t.Fatalf("decrypt = %q, %v", got, err)
	}
	for _, ciphertext := range [][]byte{nil, {1}, bytes.Repeat([]byte{0}, aes.BlockSize)} {
		if _, err := decryptAESCBC([]byte("short"), ciphertext); err == nil {
			t.Errorf("invalid ciphertext length=%d accepted", len(ciphertext))
		}
	}
	for _, padded := range [][]byte{nil, {0}, {33}, {1, 2}} {
		if _, err := pkcs7Unpad(padded); err == nil {
			t.Errorf("invalid padding %v accepted", padded)
		}
	}

	for contentType, want := range map[string]string{
		"image/jpeg": ".jpg", "image/png": ".png",
		"video/mp4": ".mp4", "application/pdf": ".pdf",
	} {
		if got := inferMediaExt(contentType, ""); got != want {
			t.Errorf("inferMediaExt(%q) = %q, want %q", contentType, got, want)
		}
	}
	if got := inferMediaExt("application/x-unknown", ".fallback"); got != ".fallback" {
		t.Fatalf("fallback extension = %q", got)
	}
	for _, value := range []string{"application/octet-stream", "binary/octet-stream", ""} {
		if !isGenericWeComContentType(value) {
			t.Errorf("generic type %q rejected", value)
		}
	}
	if sanitizeWeComFilename(" ../bad/name\x00.txt ") != "name\x00.txt" {
		t.Fatalf("sanitized filename = %q", sanitizeWeComFilename(" ../bad/name\x00.txt "))
	}
	if got := candidateWeComFilename(
		"https://example.test/path/photo.png", `attachment; filename="server.jpg"`, "fallback.bin",
	); got != "server.jpg" {
		t.Fatalf("candidate filename = %q", got)
	}
	if got := trimWeComBytes("ééé", 3); got != "é" {
		t.Fatalf("UTF-8 trim = %q", got)
	}
	if got := ensureWeComOutboundFilename("", "/tmp/report", "application/pdf"); got != "report.pdf" {
		t.Fatalf("ensured filename = %q", got)
	}
	video := buildWeComVideoContent("media", ".mp4", strings.Repeat("x", 600))
	if video.Title != ".mp4" && video.Title != "video" {
		t.Fatalf("video title = %q", video.Title)
	}
	if len(video.Description) > 512 {
		t.Fatalf("video description bytes = %d", len(video.Description))
	}
	if _, err := decodeWeComEnvelopeBody[wecomUploadMediaInitResponse](wecomEnvelope{}); err == nil {
		t.Fatal("empty envelope body accepted")
	}
	if _, err := decodeWeComEnvelopeBody[wecomUploadMediaInitResponse](wecomEnvelope{Body: []byte("{")}); err == nil {
		t.Fatal("malformed envelope body accepted")
	}

	mediaKinds := []struct {
		partType, filename, contentType string
		size                            int64
		want                            string
	}{
		{"image", "photo.jpg", "image/jpeg", 100, "image"},
		{"voice", "voice.amr", "audio/amr", 100, "voice"},
		{"video", "movie.mp4", "video/mp4", 100, "video"},
		{"file", "report.bin", "application/octet-stream", 100, "file"},
		{"image", "photo.jpg", "image/jpeg", wecomOutboundImageMaxBytes + 1, "file"},
		{"file", "huge", "application/octet-stream", wecomOutboundMediaMaxBytes + 1, ""},
		{"file", "tiny", "application/octet-stream", wecomUploadMinBytes - 1, ""},
	}
	for _, test := range mediaKinds {
		if got := outboundWeComMediaKind(test.partType, test.filename, test.contentType, test.size); got != test.want {
			t.Errorf("outbound kind %#v = %q, want %q", test, got, test.want)
		}
	}
	fallback := fallbackWeComMediaText(bus.MediaPart{
		Ref: "https://example.test/file", Caption: "caption",
	}, "file", "report.pdf")
	if !strings.Contains(fallback, "caption") || !strings.Contains(fallback, "report.pdf") ||
		!strings.Contains(fallback, "https://") {
		t.Fatalf("fallback text = %q", fallback)
	}
}

func TestWeComRemoteResolutionAndUploadFailureBoundaries(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/ok":
			writer.Header().Set("Content-Type", "text/plain")
			writer.Header().Set("Content-Disposition", `attachment; filename="remote.txt"`)
			_, _ = writer.Write([]byte("remote media"))
		case "/fail":
			http.Error(writer, "no", http.StatusBadGateway)
		default:
			http.NotFound(writer, request)
		}
	}))
	defer server.Close()
	channel := newTestWeComChannel(t, nil)
	channel.mediaClient = server.Client()
	path, name, contentType, err := channel.downloadRemoteMediaToTemp(t.Context(), server.URL+"/ok", "fallback.bin")
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(path)
	if name != "remote.txt" || contentType != "text/plain" {
		t.Fatalf("remote metadata = %q, %q", name, contentType)
	}
	if _, _, _, err := channel.downloadRemoteMediaToTemp(t.Context(), server.URL+"/fail", "x"); err == nil {
		t.Fatal("HTTP failure accepted")
	}
	if path, _, _, cleanup, err := channel.resolveOutboundPart(t.Context(), bus.MediaPart{}); err != nil ||
		path != "" ||
		cleanup == nil {
		t.Fatalf("empty part = %q, %v", path, err)
	}
	remotePath, remoteName, _, cleanup, err := channel.resolveOutboundPart(t.Context(), bus.MediaPart{
		Ref: server.URL + "/ok",
	})
	if err != nil || remoteName != "remote.txt" {
		t.Fatalf("remote part = %q/%q, %v", remotePath, remoteName, err)
	}
	cleanup()
	if _, err := os.Stat(remotePath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("remote cleanup error = %v", err)
	}
	localPath := filepath.Join(t.TempDir(), "plain.txt")
	if err := os.WriteFile(localPath, []byte("plain local media"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, ref := range []string{localPath, "file://" + localPath} {
		resolved, filename, contentType, _, err := channel.resolveOutboundPart(t.Context(), bus.MediaPart{Ref: ref})
		if err != nil || resolved != localPath || filename != "plain.txt" || contentType == "" {
			t.Errorf("local %q = %q/%q/%q, %v", ref, resolved, filename, contentType, err)
		}
	}
	if _, _, _, _, err := channel.resolveOutboundPart(t.Context(), bus.MediaPart{Ref: "media://missing"}); err == nil {
		t.Fatal("missing media store accepted")
	}

	channel.commandSend = func(command wecomCommand, _ time.Duration) (wecomEnvelope, error) {
		switch command.Cmd {
		case wecomCmdUploadMediaInit:
			return wecomTestAck(wecomUploadMediaInitResponse{}), nil
		default:
			return wecomTestAck(nil), nil
		}
	}
	_, uploadErr := channel.uploadOutboundMedia(
		t.Context(), localPath, "plain.txt", "text/plain", bus.MediaPart{Type: "file"},
	)
	if uploadErr == nil || !strings.Contains(uploadErr.Error(), "upload_id") {
		t.Fatalf("empty upload ID error = %v", uploadErr)
	}
	channel.commandSend = func(command wecomCommand, _ time.Duration) (wecomEnvelope, error) {
		if command.Cmd == wecomCmdUploadMediaInit {
			return wecomTestAck(wecomUploadMediaInitResponse{UploadID: "upload"}), nil
		}
		if command.Cmd == wecomCmdUploadMediaChunk {
			return wecomEnvelope{}, errors.New("chunk failed")
		}
		return wecomTestAck(nil), nil
	}
	_, chunkErr := channel.uploadOutboundMedia(
		t.Context(), localPath, "plain.txt", "text/plain", bus.MediaPart{Type: "file"},
	)
	if chunkErr == nil {
		t.Fatal("chunk failure accepted")
	}
	channel.queueTurn(
		"expired",
		wecomTurn{ChatID: "expired", CreatedAt: time.Now().Add(-wecomStreamMaxDuration - time.Second)},
	)
	if route, chatType, turn := channel.resolveMediaRoute("expired"); turn || route.ChatID != "expired" ||
		chatType != 0 {
		t.Fatalf("expired route = %#v/%d/%v", route, chatType, turn)
	}
}

func TestWeComTempFileAndLocalDetection(t *testing.T) {
	path, err := writeWeComTempFile("coverage", "sample.txt", []byte("hello"))
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(path)
	if got := detectLocalWeComContentType(path, "application/octet-stream"); got != "text/plain; charset=utf-8" &&
		got != "text/plain" {
		t.Fatalf("detected type = %q", got)
	}
	missingType := detectLocalWeComContentType(
		filepath.Join(t.TempDir(), "missing.bin"), "application/octet-stream",
	)
	if missingType != "application/octet-stream" {
		t.Fatalf("missing-file type = %q", missingType)
	}
}
