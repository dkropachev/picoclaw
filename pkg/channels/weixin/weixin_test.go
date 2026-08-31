package weixin

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sipeed/picoclaw/pkg/bus"
	basechannels "github.com/sipeed/picoclaw/pkg/channels"
	"github.com/sipeed/picoclaw/pkg/config"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func TestParseWeixinMediaAESKey(t *testing.T) {
	raw := []byte("1234567890abcdef")

	got, err := parseWeixinMediaAESKey(base64.StdEncoding.EncodeToString(raw))
	if err != nil {
		t.Fatalf("parseWeixinMediaAESKey(raw) error = %v", err)
	}
	if !bytes.Equal(got, raw) {
		t.Fatalf("parseWeixinMediaAESKey(raw) = %x, want %x", got, raw)
	}

	hexEncoded := base64.StdEncoding.EncodeToString([]byte("31323334353637383930616263646566"))
	got, err = parseWeixinMediaAESKey(hexEncoded)
	if err != nil {
		t.Fatalf("parseWeixinMediaAESKey(hex-string) error = %v", err)
	}
	if !bytes.Equal(got, raw) {
		t.Fatalf("parseWeixinMediaAESKey(hex-string) = %x, want %x", got, raw)
	}
}

func TestDownloadAndDecryptCDNBuffer(t *testing.T) {
	key := []byte("1234567890abcdef")
	plaintext := []byte("hello weixin")
	ciphertext, err := encryptAESECB(plaintext, key)
	if err != nil {
		t.Fatalf("encryptAESECB() error = %v", err)
	}

	ch := &WeixinChannel{
		api: &ApiClient{
			HttpClient: &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
				if r.URL.Path != "/download" {
					t.Fatalf("download path = %q, want /download", r.URL.Path)
				}
				if r.URL.Query().Get("encrypted_query_param") != "token" {
					t.Fatalf("encrypted_query_param = %q, want token", r.URL.Query().Get("encrypted_query_param"))
				}
				return &http.Response{
					StatusCode: http.StatusOK,
					Body:       io.NopCloser(bytes.NewReader(ciphertext)),
					Header:     make(http.Header),
				}, nil
			})},
		},
		config: &config.WeixinSettings{
			CDNBaseURL: "https://cdn.example.com",
		},
		typingCache: make(map[string]typingTicketCacheEntry),
	}

	got, err := ch.downloadAndDecryptCDNBuffer(context.Background(), "token", "", key)
	if err != nil {
		t.Fatalf("downloadAndDecryptCDNBuffer() error = %v", err)
	}
	if !bytes.Equal(got, plaintext) {
		t.Fatalf("downloadAndDecryptCDNBuffer() = %q, want %q", got, plaintext)
	}
}

func TestDownloadAndDecryptCDNBufferUsesFullURLWhenProvided(t *testing.T) {
	key := []byte("1234567890abcdef")
	plaintext := []byte("hello weixin")
	ciphertext, err := encryptAESECB(plaintext, key)
	if err != nil {
		t.Fatalf("encryptAESECB() error = %v", err)
	}

	fullURLAttempts := 0
	ch := &WeixinChannel{
		api: &ApiClient{
			HttpClient: &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
				if r.URL.String() == "https://full.example.com/download" {
					fullURLAttempts++
					return &http.Response{
						StatusCode: http.StatusOK,
						Body:       io.NopCloser(bytes.NewReader(ciphertext)),
						Header:     make(http.Header),
					}, nil
				}
				t.Fatalf("unexpected fallback request: %s", r.URL.String())
				return nil, nil
			})},
		},
		config: &config.WeixinSettings{
			CDNBaseURL: "https://cdn.example.com",
		},
		typingCache: make(map[string]typingTicketCacheEntry),
	}

	got, err := ch.downloadAndDecryptCDNBuffer(context.Background(), "token", "https://full.example.com/download", key)
	if err != nil {
		t.Fatalf("downloadAndDecryptCDNBuffer() error = %v", err)
	}
	if !bytes.Equal(got, plaintext) {
		t.Fatalf("downloadAndDecryptCDNBuffer() = %q, want %q", got, plaintext)
	}
	if fullURLAttempts == 0 {
		t.Fatalf("fullURLAttempts = %d, want > 0", fullURLAttempts)
	}
}

func TestDownloadAndDecryptCDNBufferFallsBackToConstructedURLWhenFullURLFails(t *testing.T) {
	key := []byte("1234567890abcdef")
	plaintext := []byte("hello weixin")
	ciphertext, err := encryptAESECB(plaintext, key)
	if err != nil {
		t.Fatalf("encryptAESECB() error = %v", err)
	}

	fullURLAttempts := 0
	constructedAttempts := 0
	ch := &WeixinChannel{
		api: &ApiClient{
			HttpClient: &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
				if r.URL.String() == "https://full.example.com/download?encrypted_query_param=token&taskid=123" {
					fullURLAttempts++
					return &http.Response{
						StatusCode: http.StatusInternalServerError,
						Body:       io.NopCloser(bytes.NewReader(nil)),
						Header:     make(http.Header),
					}, nil
				}
				if r.URL.String() != "https://cdn.example.com/download?encrypted_query_param=token" {
					t.Fatalf("unexpected fallback request: %s", r.URL.String())
				}
				constructedAttempts++
				return &http.Response{
					StatusCode: http.StatusOK,
					Body:       io.NopCloser(bytes.NewReader(ciphertext)),
					Header:     make(http.Header),
				}, nil
			})},
		},
		config: &config.WeixinSettings{
			CDNBaseURL: "https://cdn.example.com",
		},
		typingCache: make(map[string]typingTicketCacheEntry),
	}

	got, err := ch.downloadAndDecryptCDNBuffer(
		context.Background(),
		"token",
		"https://full.example.com/download?encrypted_query_param=token&taskid=123",
		key,
	)
	if err != nil {
		t.Fatalf("downloadAndDecryptCDNBuffer() error = %v", err)
	}
	if !bytes.Equal(got, plaintext) {
		t.Fatalf("downloadAndDecryptCDNBuffer() = %q, want %q", got, plaintext)
	}
	if fullURLAttempts == 0 {
		t.Fatalf("fullURLAttempts = %d, want > 0", fullURLAttempts)
	}
	if constructedAttempts == 0 {
		t.Fatalf("constructedAttempts = %d, want > 0", constructedAttempts)
	}
}

func TestBuildCDNDownloadURLEscapesOpaqueToken(t *testing.T) {
	token := "MFcCAQAESzBJAgEAAgSieMV9AgM9CcwCBEoKPqICBGnHZB0EJDk4OWY5YWU0LTc4OGItNGQ5Ni1iMjZhLWU4YjhlMmEwOWVkZgIEIR0IAgIBAAQFAExUPQA%3D"

	got := buildCDNDownloadURL("https://cdn.example.com", token)

	if got != "https://cdn.example.com/download?encrypted_query_param=MFcCAQAESzBJAgEAAgSieMV9AgM9CcwCBEoKPqICBGnHZB0EJDk4OWY5YWU0LTc4OGItNGQ5Ni1iMjZhLWU4YjhlMmEwOWVkZgIEIR0IAgIBAAQFAExUPQA%253D" {
		t.Fatalf("buildCDNDownloadURL() = %q", got)
	}
}

func TestUploadBufferToCDN(t *testing.T) {
	key := []byte("1234567890abcdef")
	plaintext := []byte("upload me")
	wantCipher, err := encryptAESECB(plaintext, key)
	if err != nil {
		t.Fatalf("encryptAESECB() error = %v", err)
	}

	ch := &WeixinChannel{
		api: &ApiClient{
			HttpClient: &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
				if r.URL.Path != "/upload" {
					t.Fatalf("upload path = %q, want /upload", r.URL.Path)
				}
				if got := r.URL.Query().Get("encrypted_query_param"); got != "upload-param" {
					t.Fatalf("encrypted_query_param = %q, want upload-param", got)
				}
				if got := r.URL.Query().Get("filekey"); got != "file-key" {
					t.Fatalf("filekey = %q, want file-key", got)
				}
				body, _ := io.ReadAll(r.Body)
				if !bytes.Equal(body, wantCipher) {
					t.Fatalf("upload body = %x, want %x", body, wantCipher)
				}
				return &http.Response{
					StatusCode: http.StatusOK,
					Body:       io.NopCloser(bytes.NewReader(nil)),
					Header: http.Header{
						"X-Encrypted-Param": []string{"download-param"},
					},
				}, nil
			})},
		},
		config: &config.WeixinSettings{
			CDNBaseURL: "https://cdn.example.com",
		},
		typingCache: make(map[string]typingTicketCacheEntry),
	}

	got, err := ch.uploadBufferToCDN(context.Background(), plaintext, "upload-param", "", "file-key", key)
	if err != nil {
		t.Fatalf("uploadBufferToCDN() error = %v", err)
	}
	if got != "download-param" {
		t.Fatalf("uploadBufferToCDN() = %q, want download-param", got)
	}
}

func TestLoadSaveGetUpdatesBuf(t *testing.T) {
	setWeixinPersistenceHome(t)
	path := filepath.Join(t.TempDir(), "sync.json")

	if err := saveGetUpdatesBuf(path, "cursor-123"); err != nil {
		t.Fatalf("saveGetUpdatesBuf() error = %v", err)
	}

	got, err := loadGetUpdatesBuf(path)
	if err != nil {
		t.Fatalf("loadGetUpdatesBuf() error = %v", err)
	}
	if got != "cursor-123" {
		t.Fatalf("loadGetUpdatesBuf() = %q, want cursor-123", got)
	}
}

func TestWeixinStartFailsClosedOnInvalidSQLiteState(t *testing.T) {
	home := setWeixinPersistenceHome(t)
	stateRoot := filepath.Join(home, "channels", "weixin")
	if err := os.MkdirAll(stateRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		filepath.Join(stateRoot, weixinStateDatabaseFilename),
		[]byte("not SQLite"),
		0o600,
	); err != nil {
		t.Fatal(err)
	}
	cfg := &config.WeixinSettings{BaseURL: "https://ilink.example/"}
	cfg.SetToken("token")
	messageBus := bus.NewMessageBus()
	defer messageBus.Close()
	channel, err := NewWeixinChannel(
		&config.Channel{Type: config.ChannelWeixin, Enabled: true},
		cfg,
		messageBus,
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := channel.Start(t.Context()); err == nil ||
		!strings.Contains(err.Error(), "cursor state") {
		t.Fatalf("invalid-state startup error = %v", err)
	}
	if channel.IsRunning() {
		t.Fatal("Weixin channel marked running after state initialization failure")
	}
}

func TestBuildWeixinSyncBufPathUsesPicoclawHome(t *testing.T) {
	home := t.TempDir()
	t.Setenv(config.EnvHome, home)

	wxCfg := &config.WeixinSettings{
		BaseURL: "https://ilinkai.weixin.qq.com/",
	}
	wxCfg.SetToken("token-123")
	got := buildWeixinSyncBufPath(wxCfg)
	if filepath.Dir(got) != filepath.Join(home, "channels", "weixin", "sync") {
		t.Fatalf("sync path dir = %q", filepath.Dir(got))
	}
}

func TestWeixinStateRuntimeCoverageBoundaries(t *testing.T) {
	if !isSessionExpiredStatus(weixinSessionExpiredCode, 0) ||
		!isSessionExpiredStatus(0, weixinSessionExpiredCode) ||
		isSessionExpiredStatus(0, 0) {
		t.Fatal("isSessionExpiredStatus() classification is wrong")
	}
	defaultCfg := &config.WeixinSettings{}
	if got := genWeixinAccountKey(defaultCfg); got != "default" {
		t.Fatalf("default account key = %q", got)
	}
	channel := &WeixinChannel{
		config:      defaultCfg,
		typingCache: make(map[string]typingTicketCacheEntry),
	}
	if got := channel.cdnBaseURL(); got != weixinDefaultCDNBaseURL {
		t.Fatalf("default CDN URL = %q", got)
	}
	channel.config.CDNBaseURL = " https://cdn.example.test/// "
	if got := channel.cdnBaseURL(); got != "https://cdn.example.test" {
		t.Fatalf("custom CDN URL = %q", got)
	}
	if err := channel.waitWhileSessionPaused(context.Background()); err != nil {
		t.Fatalf("unpaused wait error = %v", err)
	}
	channel.pauseUntil = time.Now().Add(time.Millisecond)
	if err := channel.waitWhileSessionPaused(context.Background()); err != nil {
		t.Fatalf("short paused wait error = %v", err)
	}
	channel.pauseUntil = time.Now().Add(time.Hour)
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if err := channel.waitWhileSessionPaused(canceled); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled paused wait = %v", err)
	}

	requests := 0
	channel.api = &ApiClient{
		BaseURL: "https://api.example.test/",
		Token:   "token",
		HttpClient: &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
			requests++
			if request.URL.Path != "/ilink/bot/getconfig" {
				t.Fatalf("GetConfig path = %q", request.URL.Path)
			}
			return &http.Response{
				StatusCode: http.StatusOK,
				Status:     "200 OK",
				Header:     make(http.Header),
				Body: io.NopCloser(strings.NewReader(
					`{"ret":0,"errcode":0,"typing_ticket":" ticket "}`,
				)),
			}, nil
		})},
	}
	channel.contextTokens.Store("user", "context-token")
	ticket, err := channel.getTypingTicket(context.Background(), "user")
	if err != nil || ticket != "ticket" {
		t.Fatalf("getTypingTicket(success) = %q, %v", ticket, err)
	}
	ticket, err = channel.getTypingTicket(context.Background(), "user")
	if err != nil || ticket != "ticket" || requests != 1 {
		t.Fatalf("getTypingTicket(cache) = %q, %v, requests=%d", ticket, err, requests)
	}

	transportFailure := errors.New("transport failed")
	channel.typingCache["failure"] = typingTicketCacheEntry{ticket: "cached"}
	channel.api.HttpClient = &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return nil, transportFailure
	})}
	ticket, err = channel.getTypingTicket(context.Background(), "failure")
	if ticket != "cached" || !errors.Is(err, transportFailure) {
		t.Fatalf("getTypingTicket(transport) = %q, %v", ticket, err)
	}

	channel.typingCache["expired"] = typingTicketCacheEntry{
		ticket: "old", retryDelay: weixinConfigRetryInitial,
	}
	channel.api.HttpClient = &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Status:     "200 OK",
			Header:     make(http.Header),
			Body: io.NopCloser(strings.NewReader(
				`{"ret":-14,"errcode":0,"errmsg":"expired"}`,
			)),
		}, nil
	})}
	ticket, err = channel.getTypingTicket(context.Background(), "expired")
	if ticket != "old" || err == nil || channel.remainingPause() <= 0 {
		t.Fatalf("getTypingTicket(expired) = %q, %v", ticket, err)
	}
}

func TestSessionPauseGuard(t *testing.T) {
	ch := &WeixinChannel{
		typingCache: make(map[string]typingTicketCacheEntry),
	}

	ch.pauseSession("getupdates", 0, weixinSessionExpiredCode, "expired")

	if err := ch.ensureSessionActive(); !errors.Is(err, basechannels.ErrSendFailed) {
		t.Fatalf("ensureSessionActive() error = %v, want ErrSendFailed", err)
	}

	ch.pauseMu.Lock()
	ch.pauseUntil = time.Now().Add(-time.Second)
	ch.pauseMu.Unlock()

	if err := ch.ensureSessionActive(); err != nil {
		t.Fatalf("ensureSessionActive() after expiry error = %v, want nil", err)
	}
}

func TestSelectInboundMediaItemFallsBackToRefMessage(t *testing.T) {
	msg := WeixinMessage{
		ItemList: []MessageItem{
			{
				Type: MessageItemTypeText,
				TextItem: &TextItem{
					Text: "look",
				},
				RefMsg: &RefMessage{
					MessageItem: &MessageItem{
						Type: MessageItemTypeImage,
						ImageItem: &ImageItem{
							Media: &CDNMedia{
								EncryptQueryParam: "abc",
							},
						},
					},
				},
			},
		},
	}

	item := selectInboundMediaItem(msg)
	if item == nil {
		t.Fatal("selectInboundMediaItem() = nil, want ref media item")
	}
	if item.Type != MessageItemTypeImage {
		t.Fatalf("selectInboundMediaItem().Type = %d, want %d", item.Type, MessageItemTypeImage)
	}
}

func TestSendUploadedMedia_SendsCaptionAsSeparateTextBeforeMedia(t *testing.T) {
	var requests []SendMessageReq
	ch := &WeixinChannel{
		api: &ApiClient{
			BaseURL: "https://ilinkai.weixin.qq.com/",
			HttpClient: &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
				if r.URL.Path != "/ilink/bot/sendmessage" {
					t.Fatalf("sendmessage path = %q, want /ilink/bot/sendmessage", r.URL.Path)
				}
				var req SendMessageReq
				if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
					t.Fatalf("decode sendmessage req: %v", err)
				}
				requests = append(requests, req)
				return &http.Response{
					StatusCode: http.StatusOK,
					Body:       io.NopCloser(bytes.NewReader([]byte(`{"ret":0,"errcode":0}`))),
					Header:     make(http.Header),
				}, nil
			})},
		},
		typingCache: make(map[string]typingTicketCacheEntry),
	}

	err := ch.sendUploadedMedia(
		context.Background(),
		"user-1",
		"ctx-1",
		"recipe translation",
		UploadMediaTypeImage,
		&uploadedFileInfo{
			downloadParam: "download-token",
			aesKeyHex:     "31323334353637383930616263646566",
			fileSize:      11,
			cipherSize:    16,
			filename:      "photo.png",
		},
	)
	if err != nil {
		t.Fatalf("sendUploadedMedia() error = %v", err)
	}
	if len(requests) != 2 {
		t.Fatalf("sendUploadedMedia() sent %d requests, want 2", len(requests))
	}
	if len(requests[0].Msg.ItemList) != 1 || requests[0].Msg.ItemList[0].Type != MessageItemTypeText {
		t.Fatalf("first request item = %+v, want text item", requests[0].Msg.ItemList)
	}
	if got := requests[0].Msg.ItemList[0].TextItem.Text; got != "recipe translation" {
		t.Fatalf("first request text = %q, want recipe translation", got)
	}
	if len(requests[1].Msg.ItemList) != 1 || requests[1].Msg.ItemList[0].Type != MessageItemTypeImage {
		t.Fatalf("second request item = %+v, want image item", requests[1].Msg.ItemList)
	}
	if requests[1].Msg.ItemList[0].ImageItem == nil || requests[1].Msg.ItemList[0].ImageItem.Media == nil {
		t.Fatalf("second request image media = %+v, want media ref", requests[1].Msg.ItemList[0].ImageItem)
	}
}
