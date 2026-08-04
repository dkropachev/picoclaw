package session

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/sipeed/picoclaw/pkg/media"
	"github.com/sipeed/picoclaw/pkg/providers"
)

const (
	sessionFrozenRefOne   = "media://11111111-1111-4111-8111-111111111111"
	sessionFrozenRefTwo   = "media://22222222-2222-4222-8222-222222222222"
	sessionFrozenRefThree = "media://33333333-3333-4333-8333-333333333333"
)

type sessionFrozenReader struct {
	snapshots map[string]media.Snapshot
	errors    map[string]error
	calls     []string
}

func (reader *sessionFrozenReader) ReadSnapshot(
	_ context.Context,
	ref string,
	maxBytes int64,
) (media.Snapshot, error) {
	reader.calls = append(reader.calls, ref)
	if maxBytes != media.MaxFrozenMediaAssetBytes {
		return media.Snapshot{}, fmt.Errorf("unexpected bound %d", maxBytes)
	}
	if err := reader.errors[ref]; err != nil {
		return media.Snapshot{}, err
	}
	snapshot, ok := reader.snapshots[ref]
	if !ok {
		return media.Snapshot{}, media.ErrSnapshotUnavailable
	}
	return snapshot, nil
}

func TestFreezeAndMaterializeSessionSnapshotMediaCoversEveryLocator(t *testing.T) {
	t.Parallel()

	source := sessionFrozenTestSnapshot()
	wantSource := sessionFrozenTestSnapshot()
	reader := &sessionFrozenReader{snapshots: map[string]media.Snapshot{
		sessionFrozenRefOne: {
			Bytes: []byte("message-media"),
			Meta:  media.MediaMeta{ContentType: "text/plain", Filename: "message.txt"},
		},
		sessionFrozenRefTwo: {
			Bytes: []byte("attachment-ref"),
			Meta:  media.MediaMeta{ContentType: "application/octet-stream", Filename: "ref.bin"},
		},
		sessionFrozenRefThree: {
			Bytes: []byte("part-uri"),
			Meta:  media.MediaMeta{ContentType: "image/png", Filename: "part.png"},
		},
	}}

	frozen, set, err := FreezeSessionSnapshotMedia(context.Background(), source, reader)
	if err != nil {
		t.Fatalf("FreezeSessionSnapshotMedia() error = %v", err)
	}
	if !reflect.DeepEqual(source, wantSource) {
		t.Fatalf("freeze mutated source\n got: %#v\nwant: %#v", source, wantSource)
	}
	if !reflect.DeepEqual(reader.calls, []string{
		sessionFrozenRefOne,
		sessionFrozenRefTwo,
		sessionFrozenRefThree,
	}) {
		t.Fatalf("ReadSnapshot() calls = %#v", reader.calls)
	}

	message := frozen.History[0]
	locators := []string{
		message.Media[0],
		message.Attachments[0].Ref,
		message.Attachments[0].URL,
		message.Parts[0].URI,
	}
	for index, locator := range locators {
		if !strings.HasPrefix(locator, "frozen-media://sha256/") {
			t.Fatalf("frozen locator %d = %q", index, locator)
		}
	}
	// URL is provider-effective and therefore wins the shared attachment
	// metadata after both Ref and URL have been frozen.
	if message.Attachments[0].ContentType != "text/plain" ||
		message.Attachments[0].Filename != "" {
		t.Fatalf("frozen attachment metadata = %#v", message.Attachments[0])
	}
	if message.Parts[0].MIMEType != "image/png" || message.Parts[0].Filename != "part.png" {
		t.Fatalf("frozen part metadata = %#v", message.Parts[0])
	}

	materialized, err := MaterializeSessionSnapshotMedia(context.Background(), frozen, set)
	if err != nil {
		t.Fatalf("MaterializeSessionSnapshotMedia() error = %v", err)
	}
	got := materialized.History[0]
	wantLocators := []string{
		sessionFrozenDataURI("text/plain", []byte("message-media")),
		sessionFrozenDataURI("application/octet-stream", []byte("attachment-ref")),
		sessionFrozenDataURI("text/plain", []byte("attachment-url")),
		sessionFrozenDataURI("image/png", []byte("part-uri")),
	}
	gotLocators := []string{got.Media[0], got.Attachments[0].Ref, got.Attachments[0].URL, got.Parts[0].URI}
	if !reflect.DeepEqual(gotLocators, wantLocators) {
		t.Fatalf("materialized locators\n got: %#v\nwant: %#v", gotLocators, wantLocators)
	}
	if got.Attachments[0].ContentType != "text/plain" || got.Attachments[0].Filename != "" {
		t.Fatalf("materialized attachment metadata = %#v", got.Attachments[0])
	}
	if materialized.Key != source.Key || materialized.Summary != source.Summary ||
		materialized.Revision != source.Revision ||
		!reflect.DeepEqual(materialized.Scope, source.Scope) ||
		!reflect.DeepEqual(materialized.Aliases, source.Aliases) {
		t.Fatalf("materialized snapshot lost non-media fields: %#v", materialized)
	}

	// Both transformations must detach the complete returned graph.
	frozen.History[0].Content = "mutated"
	frozen.History[0].ToolCalls[0].Arguments["nested"].(map[string]any)["value"] = "mutated"
	frozen.Scope.Dimensions[0] = "mutated"
	frozen.Scope.Values["repo"] = "mutated"
	frozen.Aliases[0] = "mutated"
	materialized.History[0].Parts[1].Text = "mutated"
	if !reflect.DeepEqual(source, wantSource) {
		t.Fatal("mutating transformed snapshots changed the source")
	}
}

func TestFreezeSessionSnapshotMediaFailsAtomicallyAndRedacts(t *testing.T) {
	t.Parallel()

	source := sessionFrozenTestSnapshot()
	source.History[0].Media = []string{sessionFrozenRefOne, sessionFrozenRefTwo}
	source.History[0].Attachments = nil
	source.History[0].Parts = nil
	wantSource := cloneSnapshotForMedia(source)
	secret := "/private/media/capture-canary"
	reader := &sessionFrozenReader{
		snapshots: map[string]media.Snapshot{
			sessionFrozenRefOne: {Bytes: []byte("first"), Meta: media.MediaMeta{ContentType: "text/plain"}},
		},
		errors: map[string]error{
			sessionFrozenRefTwo: fmt.Errorf("failed reading %s for %s", secret, sessionFrozenRefTwo),
		},
	}

	frozen, set, err := FreezeSessionSnapshotMedia(context.Background(), source, reader)
	if !errors.Is(err, media.ErrFrozenMediaUnavailable) {
		t.Fatalf("FreezeSessionSnapshotMedia() error = %v", err)
	}
	if frozen.Key != "" || len(frozen.History) != 0 || set.Version != 0 ||
		len(set.Assets) != 0 || len(set.Blobs) != 0 {
		t.Fatalf("partial result escaped: snapshot=%#v set=%#v", frozen, set)
	}
	if strings.Contains(err.Error(), secret) || strings.Contains(err.Error(), sessionFrozenRefTwo) {
		t.Fatalf("error disclosed capture details: %q", err)
	}
	if !reflect.DeepEqual(source, wantSource) {
		t.Fatal("failed freeze mutated source")
	}
}

func TestMaterializeSessionSnapshotMediaRejectsMissingAndInjectedLocators(t *testing.T) {
	t.Parallel()

	source := sessionFrozenTestSnapshot()
	reader := &sessionFrozenReader{snapshots: map[string]media.Snapshot{
		sessionFrozenRefOne:   {Bytes: []byte("one"), Meta: media.MediaMeta{ContentType: "text/plain"}},
		sessionFrozenRefTwo:   {Bytes: []byte("two"), Meta: media.MediaMeta{ContentType: "application/octet-stream"}},
		sessionFrozenRefThree: {Bytes: []byte("three"), Meta: media.MediaMeta{ContentType: "image/png"}},
	}}
	frozen, set, err := FreezeSessionSnapshotMedia(context.Background(), source, reader)
	if err != nil {
		t.Fatalf("FreezeSessionSnapshotMedia() error = %v", err)
	}

	missing := cloneSnapshotForMedia(frozen)
	missing.History[0].Media = nil
	result, err := MaterializeSessionSnapshotMedia(context.Background(), missing, set)
	if !errors.Is(err, media.ErrFrozenMediaTampered) || result.Key != "" {
		t.Fatalf("missing locator result/error = %#v/%v", result, err)
	}

	injected := cloneSnapshotForMedia(frozen)
	injected.History[0].Media = append(
		injected.History[0].Media,
		"frozen-media://sha256/"+strings.Repeat("0", 64),
	)
	result, err = MaterializeSessionSnapshotMedia(context.Background(), injected, set)
	if !errors.Is(err, media.ErrFrozenMediaTampered) || result.Key != "" {
		t.Fatalf("injected locator result/error = %#v/%v", result, err)
	}

	if !reflect.DeepEqual(source, sessionFrozenTestSnapshot()) {
		t.Fatal("failed materialization mutated source")
	}
}

func TestMaterializeSessionSnapshotMediaRejectsAuthoritativeMetadataTampering(t *testing.T) {
	t.Parallel()

	t.Run("Ref-only attachment", func(t *testing.T) {
		t.Parallel()

		source := SessionSnapshot{
			Key: "ref-only",
			History: []providers.Message{{
				Role: "user",
				Attachments: []providers.Attachment{{
					Ref: sessionFrozenRefOne,
				}},
			}},
		}
		reader := &sessionFrozenReader{snapshots: map[string]media.Snapshot{
			sessionFrozenRefOne: {
				Bytes: []byte("ref-only"),
				Meta:  media.MediaMeta{ContentType: "text/plain", Filename: "ref-only.txt"},
			},
		}}
		frozen, set, err := FreezeSessionSnapshotMedia(context.Background(), source, reader)
		if err != nil {
			t.Fatalf("FreezeSessionSnapshotMedia() error = %v", err)
		}
		mutations := []struct {
			name   string
			mutate func(*SessionSnapshot)
		}{
			{
				name: "content type",
				mutate: func(snapshot *SessionSnapshot) {
					snapshot.History[0].Attachments[0].ContentType = "image/png"
				},
			},
			{
				name: "filename",
				mutate: func(snapshot *SessionSnapshot) {
					snapshot.History[0].Attachments[0].Filename = "changed.txt"
				},
			},
		}
		for _, mutation := range mutations {
			t.Run(mutation.name, func(t *testing.T) {
				t.Parallel()

				candidate := cloneSnapshotForMedia(frozen)
				mutation.mutate(&candidate)
				result, err := MaterializeSessionSnapshotMedia(context.Background(), candidate, set)
				if !errors.Is(err, media.ErrFrozenMediaTampered) || result.Key != "" {
					t.Fatalf("MaterializeSessionSnapshotMedia() result/error = %#v/%v", result, err)
				}
			})
		}
	})

	t.Run("URL wins attachment and PromptPart", func(t *testing.T) {
		t.Parallel()

		source := sessionFrozenTestSnapshot()
		reader := &sessionFrozenReader{snapshots: map[string]media.Snapshot{
			sessionFrozenRefOne: {
				Bytes: []byte("message-media"),
				Meta:  media.MediaMeta{ContentType: "text/plain", Filename: "message.txt"},
			},
			sessionFrozenRefTwo: {
				Bytes: []byte("attachment-ref"),
				Meta:  media.MediaMeta{ContentType: "application/octet-stream", Filename: "ref.bin"},
			},
			sessionFrozenRefThree: {
				Bytes: []byte("part-uri"),
				Meta:  media.MediaMeta{ContentType: "image/png", Filename: "part.png"},
			},
		}}
		frozen, set, err := FreezeSessionSnapshotMedia(context.Background(), source, reader)
		if err != nil {
			t.Fatalf("FreezeSessionSnapshotMedia() error = %v", err)
		}
		mutations := []struct {
			name   string
			mutate func(*SessionSnapshot)
		}{
			{
				name: "URL-winning content type",
				mutate: func(snapshot *SessionSnapshot) {
					snapshot.History[0].Attachments[0].ContentType = "application/octet-stream"
				},
			},
			{
				name: "URL-winning filename",
				mutate: func(snapshot *SessionSnapshot) {
					snapshot.History[0].Attachments[0].Filename = "ref.bin"
				},
			},
			{
				name: "PromptPart MIME",
				mutate: func(snapshot *SessionSnapshot) {
					snapshot.History[0].Parts[0].MIMEType = "text/plain"
				},
			},
			{
				name: "PromptPart filename",
				mutate: func(snapshot *SessionSnapshot) {
					snapshot.History[0].Parts[0].Filename = "changed.png"
				},
			},
		}
		for _, mutation := range mutations {
			t.Run(mutation.name, func(t *testing.T) {
				t.Parallel()

				candidate := cloneSnapshotForMedia(frozen)
				mutation.mutate(&candidate)
				result, err := MaterializeSessionSnapshotMedia(context.Background(), candidate, set)
				if !errors.Is(err, media.ErrFrozenMediaTampered) || result.Key != "" {
					t.Fatalf("MaterializeSessionSnapshotMedia() result/error = %#v/%v", result, err)
				}
			})
		}
	})
}

func TestFreezeSessionSnapshotMediaRejectsTooManyLocatorsBeforeReader(t *testing.T) {
	t.Parallel()

	source := sessionFrozenTestSnapshot()
	source.History[0].Media = make([]string, media.MaxFrozenMediaOccurrences+1)
	for index := range source.History[0].Media {
		source.History[0].Media[index] = sessionFrozenRefOne
	}
	source.History[0].Attachments = nil
	source.History[0].Parts = nil
	wantSource := cloneSnapshotForMedia(source)
	reader := &sessionFrozenReader{snapshots: map[string]media.Snapshot{
		sessionFrozenRefOne: {
			Bytes: []byte("must not be read"),
			Meta:  media.MediaMeta{ContentType: "text/plain"},
		},
	}}

	frozen, set, err := FreezeSessionSnapshotMedia(context.Background(), source, reader)
	if !errors.Is(err, media.ErrFrozenMediaLimit) {
		t.Fatalf("FreezeSessionSnapshotMedia() error = %v, want %v", err, media.ErrFrozenMediaLimit)
	}
	if len(reader.calls) != 0 {
		t.Fatalf("ReadSnapshot() calls = %#v, want none", reader.calls)
	}
	if frozen.Key != "" || len(frozen.History) != 0 || set.Version != 0 {
		t.Fatalf("over-limit capture returned partial state: %#v/%#v", frozen, set)
	}
	if !reflect.DeepEqual(source, wantSource) {
		t.Fatal("over-limit preflight changed the source graph")
	}
}

func TestSessionMediaTransformsPreserveAndDetachNonNilEmptyCollections(t *testing.T) {
	t.Parallel()

	source := SessionSnapshot{
		Key: "empty-shapes",
		History: []providers.Message{{
			Role:        "user",
			Media:       make([]string, 0),
			Attachments: make([]providers.Attachment, 0),
			Parts:       make([]providers.PromptPart, 0),
		}},
		Scope: &SessionScope{
			Version:    ScopeVersionV1,
			Dimensions: make([]string, 0),
			Values:     make(map[string]string),
		},
		Aliases: make([]string, 0),
	}
	frozen, set, err := FreezeSessionSnapshotMedia(context.Background(), source, nil)
	if err != nil {
		t.Fatalf("FreezeSessionSnapshotMedia() error = %v", err)
	}
	materialized, err := MaterializeSessionSnapshotMedia(context.Background(), frozen, set)
	if err != nil {
		t.Fatalf("MaterializeSessionSnapshotMedia() error = %v", err)
	}

	for name, snapshot := range map[string]SessionSnapshot{
		"frozen":       frozen,
		"materialized": materialized,
	} {
		if snapshot.Scope == nil || snapshot.Scope.Dimensions == nil || snapshot.Scope.Values == nil ||
			snapshot.Aliases == nil || snapshot.History[0].Media == nil ||
			snapshot.History[0].Attachments == nil || snapshot.History[0].Parts == nil {
			t.Fatalf("%s lost a non-nil empty collection: %#v", name, snapshot)
		}
	}

	frozen.Scope.Dimensions = append(frozen.Scope.Dimensions, "frozen")
	frozen.Scope.Values["frozen"] = "value"
	frozen.Aliases = append(frozen.Aliases, "frozen")
	frozen.History[0].Media = append(frozen.History[0].Media, "frozen")
	frozen.History[0].Attachments = append(frozen.History[0].Attachments, providers.Attachment{Ref: "frozen"})
	frozen.History[0].Parts = append(frozen.History[0].Parts, providers.PromptPart{URI: "frozen"})
	materialized.Scope.Dimensions = append(materialized.Scope.Dimensions, "materialized")
	materialized.Scope.Values["materialized"] = "value"
	materialized.Aliases = append(materialized.Aliases, "materialized")
	materialized.History[0].Media = append(materialized.History[0].Media, "materialized")
	materialized.History[0].Attachments = append(
		materialized.History[0].Attachments,
		providers.Attachment{Ref: "materialized"},
	)
	materialized.History[0].Parts = append(
		materialized.History[0].Parts,
		providers.PromptPart{URI: "materialized"},
	)
	if len(source.Scope.Dimensions) != 0 || len(source.Scope.Values) != 0 ||
		len(source.Aliases) != 0 || len(source.History[0].Media) != 0 ||
		len(source.History[0].Attachments) != 0 || len(source.History[0].Parts) != 0 {
		t.Fatalf("mutating transformed empty collections changed source: %#v", source)
	}
}

func sessionFrozenTestSnapshot() SessionSnapshot {
	createdAt := time.Unix(1_700_000_000, 123).UTC()
	return SessionSnapshot{
		Key: "agent:review:repo",
		History: []providers.Message{{
			Role:      "user",
			Content:   "review these artifacts",
			CreatedAt: &createdAt,
			Media:     []string{sessionFrozenRefOne},
			Attachments: []providers.Attachment{{
				Ref: sessionFrozenRefTwo,
				URL: sessionFrozenDataURI("text/plain", []byte("attachment-url")),
			}},
			Parts: []providers.PromptPart{
				{Type: "image", URI: sessionFrozenRefThree},
				{Type: "text", Text: "preserve me"},
			},
			ToolCalls: []providers.ToolCall{{
				ID:        "call-1",
				Name:      "inspect",
				Arguments: map[string]any{"nested": map[string]any{"value": "original"}},
			}},
			PromptLayer: "conversation",
			PromptSlot:  "request",
		}},
		Summary: "summary",
		Scope: &SessionScope{
			Version:    ScopeVersionV1,
			AgentID:    "review",
			Channel:    "workflow",
			Dimensions: []string{"repo"},
			Values:     map[string]string{"repo": "owner/name"},
		},
		Aliases:  []string{"review-alias"},
		Revision: "revision-1",
	}
}

func sessionFrozenDataURI(contentType string, data []byte) string {
	return "data:" + contentType + ";base64," + base64.StdEncoding.EncodeToString(data)
}
