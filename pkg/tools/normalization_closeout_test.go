package tools

import (
	"encoding/base64"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/sipeed/picoclaw/pkg/media"
)

type closeoutMediaStore struct {
	storeErr error
	paths    []string
	metas    []media.MediaMeta
	scopes   []string
}

func (store *closeoutMediaStore) Store(
	path string,
	meta media.MediaMeta,
	scope string,
) (string, error) {
	store.paths = append(store.paths, path)
	store.metas = append(store.metas, meta)
	store.scopes = append(store.scopes, scope)
	if store.storeErr != nil {
		return "", store.storeErr
	}
	return "media://closeout", nil
}

func (*closeoutMediaStore) Resolve(string) (string, error) {
	return "", errors.New("unused")
}

func (*closeoutMediaStore) ResolveWithMeta(string) (string, media.MediaMeta, error) {
	return "", media.MediaMeta{}, errors.New("unused")
}

func (*closeoutMediaStore) ReleaseAll(string) error { return nil }

func TestCloseoutNormalizationSanitizesTextAndBase64(t *testing.T) {
	if normalizeToolResult(nil, "tool", nil, "", "") != nil {
		t.Fatal("nil tool result was not preserved")
	}
	for name, test := range map[string]struct {
		input string
		want  string
	}{
		"blank":            {input: " \n ", want: " \n "},
		"ordinary":         {input: "ordinary output", want: "ordinary output"},
		"markdown only":    {input: "![x](data:image/png;base64,YQ==)", want: inlineMediaOmittedMessage},
		"raw with content": {input: "before data:image/png;base64,YQ== after", want: "before  after\n" + inlineMediaOmittedMessage},
		"large base64":     {input: strings.Repeat("QUJD", 300), want: largeBase64OmittedMessage},
	} {
		t.Run(name, func(t *testing.T) {
			if got := sanitizeToolLLMContent(test.input); got != test.want {
				t.Fatalf("sanitizeToolLLMContent() = %q, want %q", got, test.want)
			}
		})
	}
	if looksLikeLargeBase64Payload(strings.Repeat("a", 100)) {
		t.Fatal("short payload classified as large base64")
	}
	if looksLikeLargeBase64Payload(strings.Repeat("!", 1100)) {
		t.Fatal("non-base64 payload classified as base64")
	}
	spaced := strings.Repeat("A ", 600)
	if looksLikeLargeBase64Payload(spaced) {
		t.Fatal("heavily spaced payload classified as base64")
	}
	if !looksLikeLargeBase64Payload(strings.Repeat("A", 1100)) {
		t.Fatal("large base64 payload was not recognized")
	}
}

func TestCloseoutNormalizationExtractsAndDeduplicatesInlineMedia(t *testing.T) {
	store := &closeoutMediaStore{}
	dataURL := "data:image/png;base64," + base64.StdEncoding.EncodeToString([]byte("png-data"))
	result := &ToolResult{
		ForLLM:  "prefix ![image](" + dataURL + ") suffix " + dataURL,
		ForUser: dataURL,
	}
	normalized := normalizeToolResult(result, "image tool", store, "test", "chat")
	if normalized != result || len(result.Media) != 1 || result.Media[0] != "media://closeout" {
		t.Fatalf("normalized media result = %#v", normalized)
	}
	if len(store.paths) != 1 || len(store.metas) != 1 || len(store.scopes) != 1 {
		t.Fatalf("media store calls = paths:%v metas:%v scopes:%v", store.paths, store.metas, store.scopes)
	}
	if store.metas[0].ContentType != "image/png" || store.metas[0].Filename != "image_tool.png" {
		t.Fatalf("stored media metadata = %#v", store.metas[0])
	}
	if !strings.Contains(result.ForLLM, "registered as a media attachment") ||
		strings.Contains(result.ForLLM, "base64") {
		t.Fatalf("normalized model content = %q", result.ForLLM)
	}
	if err := os.Remove(store.paths[0]); err != nil && !errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
	}

	blank := normalizeToolResult(
		&ToolResult{Media: []string{"media://existing"}},
		"tool",
		nil,
		"",
		"",
	)
	if blank.ForLLM == "" {
		t.Fatal("media-only result did not receive a model-safe placeholder")
	}
}

func TestCloseoutNormalizationDataURLFailureAndExtensionBranches(t *testing.T) {
	seen := map[string]struct{}{}
	store := &closeoutMediaStore{}
	tests := []struct {
		name    string
		dataURL string
		wantRef bool
		want    string
	}{
		{name: "not data", dataURL: "https://example.invalid/a", want: ""},
		{name: "missing comma", dataURL: "data:image/png;base64", want: "could not be parsed"},
		{name: "not base64", dataURL: "data:text/plain,hello", want: "not base64-encoded"},
		{name: "bad payload", dataURL: "data:image/png;base64,%%%", want: "could not be decoded"},
		{name: "default mime", dataURL: "data:;base64,YQ==", wantRef: true, want: "application/octet-stream"},
		{name: "whitespace", dataURL: " data:text/plain;base64, YQ==\n ", wantRef: true, want: "text/plain"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			ref, note := storeInlineDataURL(
				"tool",
				store,
				"channel",
				"chat",
				test.dataURL,
				seen,
			)
			if (ref != "") != test.wantRef || !strings.Contains(note, test.want) {
				t.Fatalf("storeInlineDataURL() = %q, %q", ref, note)
			}
			if ref != "" {
				path := store.paths[len(store.paths)-1]
				if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
					t.Fatal(err)
				}
			}
		})
	}
	ref, note := storeInlineDataURL(
		"tool", store, "channel", "chat", "data:text/plain;base64, YQ==\n ", seen,
	)
	if ref != "" || note != "" {
		t.Fatalf("duplicate data URL = %q, %q", ref, note)
	}

	failing := &closeoutMediaStore{storeErr: errors.New("registration failed")}
	ref, note = storeInlineDataURL(
		"tool",
		failing,
		"channel",
		"chat",
		"data:application/x-closeout;base64,YQ==",
		map[string]struct{}{},
	)
	if ref != "" || !strings.Contains(note, "could not be registered") {
		t.Fatalf("registration failure = %q, %q", ref, note)
	}
	if len(failing.paths) == 1 {
		if _, err := os.Lstat(failing.paths[0]); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("failed registration temp file remained: %v", err)
		}
	}

	for mimeType, wantNonEmpty := range map[string]bool{
		"":                       true,
		"image/jpeg":             true,
		"image/png":              true,
		"image/gif":              true,
		"image/webp":             true,
		"audio/x-wav":            true,
		"audio/mpeg":             true,
		"audio/ogg":              true,
		"video/mp4":              true,
		"application/x-closeout": false,
	} {
		if got := extensionForMIMEType(mimeType); (got != "") != wantNonEmpty {
			t.Errorf("extensionForMIMEType(%q) = %q", mimeType, got)
		}
	}
}
