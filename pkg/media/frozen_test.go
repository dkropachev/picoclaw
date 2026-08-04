package media

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

type frozenSnapshotReaderFunc func(context.Context, string, int64) (Snapshot, error)

const (
	frozenTestMediaRef    = "media://11111111-1111-4111-8111-111111111111"
	frozenTestMediaRefTwo = "media://22222222-2222-4222-8222-222222222222"
)

func (read frozenSnapshotReaderFunc) ReadSnapshot(
	ctx context.Context,
	ref string,
	maxBytes int64,
) (Snapshot, error) {
	return read(ctx, ref, maxBytes)
}

func TestFreezeInputsRoundTripSurvivesSourceDeletionAndRestart(t *testing.T) {
	t.Parallel()

	sourceBytes := []byte("durable media bytes")
	sourcePath := filepath.Join(t.TempDir(), "source.png")
	if err := os.WriteFile(sourcePath, sourceBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	store := NewFileMediaStore()
	locator, err := store.Store(sourcePath, MediaMeta{
		Filename:    "source.png",
		ContentType: "image/png",
		Source:      "test-secret-source",
	}, "capture")
	if err != nil {
		t.Fatalf("Store() error = %v", err)
	}

	references, set, err := FreezeInputs(context.Background(), []FreezeInput{{
		Locator:     locator,
		ContentType: "image/png",
		Filename:    `unsafe\path/nested/final.png`,
	}}, store)
	if err != nil {
		t.Fatalf("FreezeInputs() error = %v", err)
	}
	if len(references) != 1 {
		t.Fatalf("FreezeInputs() references = %d, want 1", len(references))
	}
	if !strings.HasPrefix(references[0].Ref, frozenReferencePrefix) ||
		references[0].ContentType != "image/png" ||
		references[0].Filename != "final.png" ||
		references[0].Size != int64(len(sourceBytes)) {
		t.Fatalf("FreezeInputs() reference = %#v", references[0])
	}

	encoded, err := json.Marshal(set)
	if err != nil {
		t.Fatalf("json.Marshal(FrozenSet) error = %v", err)
	}
	if releaseErr := store.ReleaseAll("capture"); releaseErr != nil {
		t.Fatalf("ReleaseAll() error = %v", releaseErr)
	}
	if _, statErr := os.Stat(sourcePath); !os.IsNotExist(statErr) {
		t.Fatalf("source still exists after cleanup: %v", statErr)
	}
	_ = NewFileMediaStore() // Simulate a restart with no process-local mapping.

	var restored FrozenSet
	if unmarshalErr := json.Unmarshal(encoded, &restored); unmarshalErr != nil {
		t.Fatalf("json.Unmarshal(FrozenSet) error = %v", unmarshalErr)
	}
	reencoded, err := json.Marshal(restored)
	if err != nil {
		t.Fatalf("second json.Marshal(FrozenSet) error = %v", err)
	}
	if !bytes.Equal(reencoded, encoded) {
		t.Fatalf("FrozenSet JSON changed across round trip\n got: %s\nwant: %s", reencoded, encoded)
	}

	materialized, err := restored.Materialize(context.Background(), []string{references[0].Ref})
	if err != nil {
		t.Fatalf("Materialize() after source deletion error = %v", err)
	}
	wantURI := frozenTestDataURI("image/png", sourceBytes)
	if len(materialized) != 1 || materialized[0].URI != wantURI ||
		materialized[0].ContentType != "image/png" ||
		materialized[0].Filename != "final.png" ||
		materialized[0].Size != int64(len(sourceBytes)) ||
		materialized[0].SHA256 == "" {
		t.Fatalf("Materialize() = %#v", materialized)
	}
}

func TestFreezeInputsDeduplicatesBytesAndBindsMetadata(t *testing.T) {
	t.Parallel()

	source := []byte("same bytes")
	readCalls := 0
	reader := frozenSnapshotReaderFunc(func(
		_ context.Context,
		ref string,
		maxBytes int64,
	) (Snapshot, error) {
		readCalls++
		if ref != frozenTestMediaRef || maxBytes != MaxFrozenMediaAssetBytes {
			t.Fatalf("ReadSnapshot(%q, %d)", ref, maxBytes)
		}
		return Snapshot{
			Bytes: source,
			Meta:  MediaMeta{ContentType: "text/plain; charset=utf-8", Filename: "captured.txt"},
		}, nil
	})
	inputs := []FreezeInput{
		{Locator: frozenTestMediaRef},
		{Locator: frozenTestMediaRef, Filename: "captured.txt"},
		{Locator: frozenTestMediaRef, Filename: "other.txt"},
	}

	references, set, err := FreezeInputs(context.Background(), inputs, reader)
	if err != nil {
		t.Fatalf("FreezeInputs() error = %v", err)
	}
	if readCalls != 1 {
		t.Fatalf("ReadSnapshot() calls = %d, want 1", readCalls)
	}
	if len(set.Blobs) != 1 || len(set.Assets) != 2 {
		t.Fatalf("FrozenSet blobs/assets = %d/%d, want 1/2", len(set.Blobs), len(set.Assets))
	}
	if references[0] != references[1] {
		t.Fatalf("equivalent references differ: %#v != %#v", references[0], references[1])
	}
	if references[2].Ref == references[0].Ref || references[2].Filename != "other.txt" {
		t.Fatalf("metadata-distinct reference = %#v", references[2])
	}

	// Capture must detach from caller-owned bytes and input values.
	source[0] = 'X'
	inputs[0].Locator = "media://mutated"
	references[0].Ref = "frozen-media://mutated"
	materialized, err := set.Materialize(context.Background(), []string{
		references[1].Ref,
		references[2].Ref,
	})
	if err != nil {
		t.Fatalf("Materialize() error = %v", err)
	}
	for index, value := range materialized {
		decoded := frozenTestDecodeDataURI(t, value.URI)
		if string(decoded) != "same bytes" {
			t.Fatalf("Materialize()[%d] bytes = %q", index, decoded)
		}
	}
}

func TestFreezeInputsAcceptsCanonicalDataURIWithoutReader(t *testing.T) {
	t.Parallel()

	data := []byte("inline")
	references, set, err := FreezeInputs(context.Background(), []FreezeInput{{
		Locator:  frozenTestDataURI("text/plain", data),
		Filename: "inline.txt",
	}}, nil)
	if err != nil {
		t.Fatalf("FreezeInputs() error = %v", err)
	}
	materialized, err := set.Materialize(context.Background(), []string{references[0].Ref})
	if err != nil {
		t.Fatalf("Materialize() error = %v", err)
	}
	if len(materialized) != 1 || materialized[0].URI != frozenTestDataURI("text/plain", data) {
		t.Fatalf("Materialize() = %#v", materialized)
	}
}

func TestFreezeInputsRejectsUnsafeOrNoncanonicalLocators(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		locator string
		want    error
	}{
		{name: "empty", locator: "", want: ErrFrozenMediaInvalid},
		{name: "http", locator: "https://secret.example/image.png", want: ErrFrozenMediaScheme},
		{name: "file", locator: "file:///secret/image.png", want: ErrFrozenMediaScheme},
		{name: "path", locator: "/secret/image.png", want: ErrFrozenMediaScheme},
		{name: "unpadded base64", locator: "data:text/plain;base64,WA", want: ErrFrozenMediaInvalid},
		{name: "invalid base64", locator: "data:text/plain;base64,@@==", want: ErrFrozenMediaInvalid},
		{name: "noncanonical type", locator: "data:TEXT/PLAIN;base64,WA==", want: ErrFrozenMediaInvalid},
		{name: "parameters", locator: "data:text/plain;charset=utf-8;base64,WA==", want: ErrFrozenMediaInvalid},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			_, _, err := FreezeInputs(context.Background(), []FreezeInput{{
				Locator: test.locator,
			}}, nil)
			if !errors.Is(err, test.want) {
				t.Fatalf("FreezeInputs() error = %v, want %v", err, test.want)
			}
			assertFrozenErrorRedacted(t, err, test.locator)
		})
	}
}

func TestFreezeInputsRedactsReaderFailures(t *testing.T) {
	t.Parallel()

	locator := frozenTestMediaRef
	readerSecret := "/private/path/reader-canary"
	reader := frozenSnapshotReaderFunc(func(
		context.Context,
		string,
		int64,
	) (Snapshot, error) {
		return Snapshot{}, fmt.Errorf("reader failed at %s", readerSecret)
	})
	_, _, err := FreezeInputs(context.Background(), []FreezeInput{{Locator: locator}}, reader)
	if !errors.Is(err, ErrFrozenMediaUnavailable) {
		t.Fatalf("FreezeInputs() error = %v, want %v", err, ErrFrozenMediaUnavailable)
	}
	assertFrozenErrorRedacted(t, err, locator, readerSecret)

	_, _, err = FreezeInputs(context.Background(), []FreezeInput{{Locator: locator}}, nil)
	if !errors.Is(err, ErrFrozenMediaUnavailable) {
		t.Fatalf("FreezeInputs(nil reader) error = %v, want %v", err, ErrFrozenMediaUnavailable)
	}
	assertFrozenErrorRedacted(t, err, locator)
}

func TestFreezeInputsFixedBounds(t *testing.T) {
	t.Run("occurrences", func(t *testing.T) {
		inputs := make([]FreezeInput, MaxFrozenMediaOccurrences+1)
		for index := range inputs {
			inputs[index].Locator = frozenTestMediaRef
		}
		calls := 0
		reader := frozenSnapshotReaderFunc(func(
			context.Context,
			string,
			int64,
		) (Snapshot, error) {
			calls++
			return Snapshot{Bytes: []byte("x")}, nil
		})
		_, _, err := FreezeInputs(context.Background(), inputs, reader)
		if !errors.Is(err, ErrFrozenMediaLimit) || calls != 0 {
			t.Fatalf("FreezeInputs() error/calls = %v/%d", err, calls)
		}
	})

	t.Run("single asset", func(t *testing.T) {
		tooLarge := make([]byte, MaxFrozenMediaAssetBytes+1)
		reader := frozenSnapshotReaderFunc(func(
			context.Context,
			string,
			int64,
		) (Snapshot, error) {
			return Snapshot{Bytes: tooLarge, Meta: MediaMeta{ContentType: "application/octet-stream"}}, nil
		})
		_, _, err := FreezeInputs(context.Background(), []FreezeInput{{Locator: frozenTestMediaRef}}, reader)
		if !errors.Is(err, ErrFrozenMediaLimit) {
			t.Fatalf("FreezeInputs() error = %v, want %v", err, ErrFrozenMediaLimit)
		}
	})

	t.Run("aggregate counts occurrences", func(t *testing.T) {
		chunk := make([]byte, MaxFrozenMediaTotalBytes/3+1)
		reader := frozenSnapshotReaderFunc(func(
			context.Context,
			string,
			int64,
		) (Snapshot, error) {
			return Snapshot{Bytes: chunk, Meta: MediaMeta{ContentType: "application/octet-stream"}}, nil
		})
		inputs := []FreezeInput{
			{Locator: frozenTestMediaRef},
			{Locator: frozenTestMediaRef},
			{Locator: frozenTestMediaRef},
		}
		_, _, err := FreezeInputs(context.Background(), inputs, reader)
		if !errors.Is(err, ErrFrozenMediaLimit) {
			t.Fatalf("FreezeInputs() error = %v, want %v", err, ErrFrozenMediaLimit)
		}
	})

	t.Run("distinct metadata assets", func(t *testing.T) {
		reader := frozenSnapshotReaderFunc(func(
			context.Context,
			string,
			int64,
		) (Snapshot, error) {
			return Snapshot{Bytes: []byte("x"), Meta: MediaMeta{ContentType: "text/plain"}}, nil
		})
		inputs := make([]FreezeInput, MaxFrozenMediaAssets+1)
		for index := range inputs {
			inputs[index] = FreezeInput{
				Locator:  frozenTestMediaRef,
				Filename: fmt.Sprintf("asset-%02d.txt", index),
			}
		}
		_, set, err := FreezeInputs(
			context.Background(),
			inputs[:MaxFrozenMediaAssets],
			reader,
		)
		if err != nil || len(set.Assets) != MaxFrozenMediaAssets {
			t.Fatalf("FreezeInputs(at limit) set/error = %d/%v", len(set.Assets), err)
		}
		_, _, err = FreezeInputs(context.Background(), inputs, reader)
		if !errors.Is(err, ErrFrozenMediaLimit) {
			t.Fatalf("FreezeInputs(over limit) error = %v, want %v", err, ErrFrozenMediaLimit)
		}
	})
}

func TestFreezeInputsExactPerItemAndDataURIBoundary(t *testing.T) {
	t.Run("snapshot reader exact limit succeeds", func(t *testing.T) {
		data := bytes.Repeat([]byte{'r'}, MaxFrozenMediaAssetBytes)
		reader := frozenSnapshotReaderFunc(func(
			context.Context,
			string,
			int64,
		) (Snapshot, error) {
			return Snapshot{
				Bytes: data,
				Meta:  MediaMeta{ContentType: "application/octet-stream"},
			}, nil
		})
		references, set, err := FreezeInputs(
			context.Background(),
			[]FreezeInput{{Locator: frozenTestMediaRef}},
			reader,
		)
		if err != nil {
			t.Fatalf("FreezeInputs(exact item limit) error = %v", err)
		}
		if references[0].Size != MaxFrozenMediaAssetBytes ||
			len(set.Blobs) != 1 || len(set.Blobs[0].Data) != MaxFrozenMediaAssetBytes {
			t.Fatalf("exact-limit reference/set = %#v/%d", references[0], len(set.Blobs[0].Data))
		}
	})

	t.Run("canonical data URI exact limit succeeds and plus one fails", func(t *testing.T) {
		exact := bytes.Repeat([]byte{'d'}, MaxFrozenMediaAssetBytes)
		references, set, err := FreezeInputs(context.Background(), []FreezeInput{{
			Locator: frozenTestDataURI("application/octet-stream", exact),
		}}, nil)
		if err != nil {
			t.Fatalf("FreezeInputs(exact data URI limit) error = %v", err)
		}
		if references[0].Size != MaxFrozenMediaAssetBytes || len(set.Blobs[0].Data) != MaxFrozenMediaAssetBytes {
			t.Fatalf("exact data URI reference/set = %#v/%d", references[0], len(set.Blobs[0].Data))
		}

		over := make([]byte, MaxFrozenMediaAssetBytes+1)
		copy(over, exact)
		over[len(over)-1] = 'x'
		gotRefs, gotSet, err := FreezeInputs(context.Background(), []FreezeInput{{
			Locator: frozenTestDataURI("application/octet-stream", over),
		}}, nil)
		if err == nil {
			t.Fatal("FreezeInputs(data URI limit+1) succeeded")
		}
		if gotRefs != nil || gotSet.Version != 0 || len(gotSet.Assets) != 0 || len(gotSet.Blobs) != 0 {
			t.Fatalf("data URI limit+1 returned partial state: %#v/%#v", gotRefs, gotSet)
		}
	})
}

func TestFreezeInputsExactAggregateOccurrenceBoundary(t *testing.T) {
	unit := bytes.Repeat([]byte{'a'}, MaxFrozenMediaTotalBytes/3)
	inputs := []FreezeInput{
		{Locator: frozenTestMediaRef},
		{Locator: frozenTestMediaRef},
		{Locator: frozenTestMediaRefTwo},
	}

	t.Run("exact succeeds including duplicate occurrence", func(t *testing.T) {
		reader := frozenSnapshotReaderFunc(func(
			_ context.Context,
			ref string,
			_ int64,
		) (Snapshot, error) {
			if ref != frozenTestMediaRef && ref != frozenTestMediaRefTwo {
				t.Fatalf("unexpected ref %q", ref)
			}
			return Snapshot{
				Bytes: unit,
				Meta:  MediaMeta{ContentType: "application/octet-stream"},
			}, nil
		})
		references, set, err := FreezeInputs(context.Background(), inputs, reader)
		if err != nil {
			t.Fatalf("FreezeInputs(exact aggregate) error = %v", err)
		}
		refs := []string{references[0].Ref, references[1].Ref, references[2].Ref}
		if _, err := set.Materialize(context.Background(), refs); err != nil {
			t.Fatalf("Materialize(exact aggregate) error = %v", err)
		}
	})

	t.Run("plus one fails atomically", func(t *testing.T) {
		over := make([]byte, len(unit)+1)
		copy(over, unit)
		over[len(over)-1] = 'x'
		reader := frozenSnapshotReaderFunc(func(
			_ context.Context,
			ref string,
			_ int64,
		) (Snapshot, error) {
			data := unit
			if ref == frozenTestMediaRefTwo {
				data = over
			}
			return Snapshot{
				Bytes: data,
				Meta:  MediaMeta{ContentType: "application/octet-stream"},
			}, nil
		})
		references, set, err := FreezeInputs(context.Background(), inputs, reader)
		if !errors.Is(err, ErrFrozenMediaLimit) {
			t.Fatalf("FreezeInputs(aggregate+1) error = %v, want %v", err, ErrFrozenMediaLimit)
		}
		if references != nil || set.Version != 0 || len(set.Assets) != 0 || len(set.Blobs) != 0 {
			t.Fatalf("aggregate+1 returned partial state: %#v/%#v", references, set)
		}
	})
}

func TestFrozenSetRejectsTamperingAndUnusedOrInjectedReferences(t *testing.T) {
	t.Parallel()

	references, set := frozenTestSet(t, []FreezeInput{{
		Locator:  frozenTestDataURI("text/plain", []byte("integrity canary")),
		Filename: "canary.txt",
	}})

	tests := []struct {
		name   string
		mutate func(*FrozenSet)
		want   error
	}{
		{
			name: "blob bytes",
			mutate: func(candidate *FrozenSet) {
				candidate.Blobs[0].Data[0] ^= 0xff
			},
			want: ErrFrozenMediaTampered,
		},
		{
			name: "asset metadata",
			mutate: func(candidate *FrozenSet) {
				candidate.Assets[0].Filename = "changed.txt"
			},
			want: ErrFrozenMediaTampered,
		},
		{
			name: "duplicate asset id",
			mutate: func(candidate *FrozenSet) {
				candidate.Assets = append(candidate.Assets, candidate.Assets[0])
			},
			want: ErrFrozenMediaTampered,
		},
		{
			name: "version",
			mutate: func(candidate *FrozenSet) {
				candidate.Version++
			},
			want: ErrFrozenMediaInvalid,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			candidate := set.Clone()
			test.mutate(&candidate)
			if err := candidate.Validate(); !errors.Is(err, test.want) {
				t.Fatalf("Validate() error = %v, want %v", err, test.want)
			}
			_, err := candidate.Materialize(
				context.Background(),
				[]string{references[0].Ref},
			)
			if !errors.Is(err, test.want) {
				t.Fatalf("Materialize() error = %v, want %v", err, test.want)
			}
		})
	}

	if _, err := set.Materialize(context.Background(), nil); !errors.Is(err, ErrFrozenMediaTampered) {
		t.Fatalf("Materialize(unused asset) error = %v, want %v", err, ErrFrozenMediaTampered)
	}
	injected := frozenReferencePrefix + strings.Repeat("0", 64)
	if _, err := set.Materialize(context.Background(), []string{injected}); !errors.Is(err, ErrFrozenMediaTampered) {
		t.Fatalf("Materialize(injected ref) error = %v, want %v", err, ErrFrozenMediaTampered)
	}

	clone := set.Clone()
	clone.Blobs[0].Data[0] ^= 0xff
	if _, err := set.Materialize(context.Background(), []string{references[0].Ref}); err != nil {
		t.Fatalf("mutating Clone changed source set: %v", err)
	}
}

func TestFrozenSetJSONIsStrictVersionedAndAtomic(t *testing.T) {
	t.Parallel()

	_, set := frozenTestSet(t, []FreezeInput{{
		Locator:  frozenTestDataURI("text/plain", []byte("json payload")),
		Filename: "payload.txt",
	}})
	encoded, err := json.Marshal(set)
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}
	valid := string(encoded)
	malformedBase64 := strings.Replace(
		valid,
		base64.StdEncoding.EncodeToString([]byte("json payload")),
		"not-base64!",
		1,
	)

	tests := []struct {
		name string
		json string
	}{
		{name: "null", json: `null`},
		{name: "case alias", json: strings.Replace(valid, `"version":1`, `"Version":1`, 1)},
		{name: "unknown root field", json: strings.TrimSuffix(valid, "}") + `,"unknown":true}`},
		{name: "unknown asset field", json: strings.Replace(valid, `"size":`, `"unknown":true,"size":`, 1)},
		{name: "trailing value", json: valid + `{}`},
		{name: "wrong version", json: strings.Replace(valid, `"version":1`, `"version":2`, 1)},
		{name: "malformed base64", json: malformedBase64},
		{name: "duplicate version", json: strings.Replace(valid, `"version":1`, `"version":1,"version":1`, 1)},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			before := set.Clone()
			candidate := set.Clone()
			if err := json.Unmarshal([]byte(test.json), &candidate); err == nil {
				t.Fatal("json.Unmarshal() succeeded, want strict rejection")
			}
			if !reflect.DeepEqual(candidate, before) {
				t.Fatalf("failed decode mutated receiver\n got: %#v\nwant: %#v", candidate, before)
			}
		})
	}
}

func TestFrozenSetJSONRejectsOrderingDigestAndSizeTampering(t *testing.T) {
	t.Parallel()

	_, set := frozenTestSet(t, []FreezeInput{
		{Locator: frozenTestDataURI("text/plain", []byte("first payload")), Filename: "first.txt"},
		{Locator: frozenTestDataURI("image/png", []byte("second payload")), Filename: "second.png"},
	})
	if len(set.Assets) != 2 || len(set.Blobs) != 2 {
		t.Fatalf("fixture assets/blobs = %d/%d", len(set.Assets), len(set.Blobs))
	}

	tests := []struct {
		name   string
		mutate func(*FrozenSet)
	}{
		{
			name: "reordered assets",
			mutate: func(candidate *FrozenSet) {
				candidate.Assets[0], candidate.Assets[1] = candidate.Assets[1], candidate.Assets[0]
			},
		},
		{
			name: "reordered blobs",
			mutate: func(candidate *FrozenSet) {
				candidate.Blobs[0], candidate.Blobs[1] = candidate.Blobs[1], candidate.Blobs[0]
			},
		},
		{
			name: "asset digest",
			mutate: func(candidate *FrozenSet) {
				candidate.Assets[0].ID = frozenTestAlterDigest(candidate.Assets[0].ID)
			},
		},
		{
			name: "blob digest",
			mutate: func(candidate *FrozenSet) {
				candidate.Blobs[0].SHA256 = frozenTestAlterDigest(candidate.Blobs[0].SHA256)
			},
		},
		{
			name: "asset size",
			mutate: func(candidate *FrozenSet) {
				candidate.Assets[0].Size++
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			candidate := set.Clone()
			test.mutate(&candidate)
			encoded, err := frozenTestMarshalWire(candidate)
			if err != nil {
				t.Fatalf("marshal tampered wire: %v", err)
			}
			var decoded FrozenSet
			if err := json.Unmarshal(encoded, &decoded); !errors.Is(err, ErrFrozenMediaTampered) {
				t.Fatalf("json.Unmarshal(tampered) error = %v, want %v", err, ErrFrozenMediaTampered)
			}
		})
	}
}

func TestFrozenSetJSONRejectsInvalidUTF8AndSurrogatesAtomically(t *testing.T) {
	t.Parallel()

	_, set := frozenTestSet(t, []FreezeInput{{
		Locator:  frozenTestDataURI("text/plain", []byte("unicode payload")),
		Filename: "unicode-marker.txt",
	}})
	encoded, err := json.Marshal(set)
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}
	marker := []byte("unicode-marker.txt")
	replaceMarker := func(replacement []byte) []byte {
		t.Helper()
		result := bytes.Replace(encoded, marker, replacement, 1)
		if bytes.Equal(result, encoded) {
			t.Fatal("fixture filename marker not found")
		}
		return result
	}
	tests := []struct {
		name string
		json []byte
	}{
		{name: "raw invalid UTF-8", json: replaceMarker([]byte{'x', 0xff, 'y'})},
		{name: "lone high surrogate", json: replaceMarker([]byte(`\uD800`))},
		{name: "lone low surrogate", json: replaceMarker([]byte(`\uDC00`))},
		{name: "high surrogate followed by scalar", json: replaceMarker([]byte(`\uD800\u0061`))},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			before := set.Clone()
			candidate := set.Clone()
			if err := json.Unmarshal(test.json, &candidate); !errors.Is(err, ErrFrozenMediaInvalid) {
				t.Fatalf("json.Unmarshal() error = %v, want %v", err, ErrFrozenMediaInvalid)
			}
			if !reflect.DeepEqual(candidate, before) {
				t.Fatalf("failed decode mutated receiver\n got: %#v\nwant: %#v", candidate, before)
			}
		})
	}
}

func TestFrozenSetRejectsReferenceCaseShapeAndDigestTampering(t *testing.T) {
	t.Parallel()

	references, set := frozenTestSet(t, []FreezeInput{
		{Locator: frozenTestDataURI("text/plain", []byte("first reference"))},
		{Locator: frozenTestDataURI("image/png", []byte("second reference"))},
	})
	digest := strings.TrimPrefix(references[0].Ref, frozenReferencePrefix)
	tests := []struct {
		name string
		ref  string
		want error
	}{
		{name: "prefix case", ref: "Frozen-media://sha256/" + digest, want: ErrFrozenMediaInvalid},
		{name: "digest case", ref: frozenReferencePrefix + strings.ToUpper(digest), want: ErrFrozenMediaInvalid},
		{name: "short digest", ref: frozenReferencePrefix + digest[:len(digest)-1], want: ErrFrozenMediaInvalid},
		{
			name: "changed digest",
			ref:  frozenReferencePrefix + frozenTestAlterDigest(digest),
			want: ErrFrozenMediaTampered,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			refs := []string{test.ref, references[1].Ref}
			if _, err := set.Materialize(context.Background(), refs); !errors.Is(err, test.want) {
				t.Fatalf("Materialize(%s) error = %v, want %v", test.name, err, test.want)
			}
		})
	}
}

func TestFrozenSetEmptyRoundTrip(t *testing.T) {
	t.Parallel()

	references, set, err := FreezeInputs(context.Background(), nil, nil)
	if err != nil {
		t.Fatalf("FreezeInputs(empty) error = %v", err)
	}
	if len(references) != 0 || set.Version != FrozenSetVersion ||
		len(set.Assets) != 0 || len(set.Blobs) != 0 {
		t.Fatalf("FreezeInputs(empty) = %#v/%#v", references, set)
	}
	encoded, err := json.Marshal(set)
	if err != nil {
		t.Fatalf("json.Marshal(empty) error = %v", err)
	}
	if string(encoded) != `{"version":1}` {
		t.Fatalf("json.Marshal(empty) = %s", encoded)
	}
	var restored FrozenSet
	if unmarshalErr := json.Unmarshal(encoded, &restored); unmarshalErr != nil {
		t.Fatalf("json.Unmarshal(empty) error = %v", unmarshalErr)
	}
	materialized, err := restored.Materialize(context.Background(), nil)
	if err != nil {
		t.Fatalf("Materialize(empty) error = %v", err)
	}
	if len(materialized) != 0 || restored.Version != set.Version ||
		len(restored.Assets) != 0 || len(restored.Blobs) != 0 {
		t.Fatalf("empty round trip = %#v/%#v", materialized, restored)
	}
}

func TestFrozenMediaOperationsHonorCancellation(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	readerCalled := false
	reader := frozenSnapshotReaderFunc(func(
		context.Context,
		string,
		int64,
	) (Snapshot, error) {
		readerCalled = true
		return Snapshot{Bytes: []byte("x")}, nil
	})
	_, _, err := FreezeInputs(ctx, []FreezeInput{{Locator: frozenTestMediaRef}}, reader)
	if !errors.Is(err, context.Canceled) || readerCalled {
		t.Fatalf("FreezeInputs() error/called = %v/%v", err, readerCalled)
	}

	references, set := frozenTestSet(t, []FreezeInput{{
		Locator: frozenTestDataURI("text/plain", []byte("x")),
	}})
	_, err = set.Materialize(ctx, []string{references[0].Ref})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Materialize() error = %v, want context cancellation", err)
	}

	wrapped := []struct {
		name   string
		cause  error
		canary string
	}{
		{name: "canceled", cause: context.Canceled, canary: "/private/canceled-canary"},
		{name: "deadline", cause: context.DeadlineExceeded, canary: "/private/deadline-canary"},
	}
	for _, test := range wrapped {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			wrappedReader := frozenSnapshotReaderFunc(func(
				context.Context,
				string,
				int64,
			) (Snapshot, error) {
				return Snapshot{}, fmt.Errorf("reader failed at %s: %w", test.canary, test.cause)
			})
			_, _, err := FreezeInputs(
				context.Background(),
				[]FreezeInput{{Locator: frozenTestMediaRef}},
				wrappedReader,
			)
			if !errors.Is(err, test.cause) {
				t.Fatalf("FreezeInputs(reader %s) error = %v", test.name, err)
			}
			assertFrozenErrorRedacted(t, err, test.canary, frozenTestMediaRef)
		})
	}
}

func TestFreezeInputsCaptureConcurrencyIsBoundedAndCancellable(t *testing.T) {
	if capacity := cap(frozenCaptureSlots); capacity != 4 {
		t.Fatalf("frozenCaptureSlots capacity = %d, want 4", capacity)
	}

	started := make(chan struct{}, cap(frozenCaptureSlots))
	release := make(chan struct{})
	released := false
	defer func() {
		if !released {
			close(release)
		}
	}()
	reader := frozenSnapshotReaderFunc(func(
		_ context.Context,
		_ string,
		_ int64,
	) (Snapshot, error) {
		started <- struct{}{}
		<-release
		return Snapshot{
			Bytes: []byte("bounded capture"),
			Meta:  MediaMeta{ContentType: "text/plain"},
		}, nil
	})
	workerResults := make(chan error, cap(frozenCaptureSlots))
	for index := 0; index < cap(frozenCaptureSlots); index++ {
		ref := fmt.Sprintf("media://00000000-0000-4000-8000-%012d", index+1)
		go func() {
			_, _, err := FreezeInputs(
				context.Background(),
				[]FreezeInput{{Locator: ref}},
				reader,
			)
			workerResults <- err
		}()
	}
	for range cap(frozenCaptureSlots) {
		<-started
	}

	overflowReaderCalled := false
	overflowReader := frozenSnapshotReaderFunc(func(
		context.Context,
		string,
		int64,
	) (Snapshot, error) {
		overflowReaderCalled = true
		return Snapshot{}, nil
	})
	waitCtx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	_, _, err := FreezeInputs(
		waitCtx,
		[]FreezeInput{{Locator: "media://00000000-0000-4000-8000-000000000005"}},
		overflowReader,
	)
	if !errors.Is(err, context.DeadlineExceeded) || overflowReaderCalled {
		t.Fatalf(
			"saturated FreezeInputs() error/called = %v/%v, want deadline/false",
			err,
			overflowReaderCalled,
		)
	}

	close(release)
	released = true
	for range cap(frozenCaptureSlots) {
		if workerErr := <-workerResults; workerErr != nil {
			t.Fatalf("admitted FreezeInputs() error = %v", workerErr)
		}
	}
}

func TestFreezeInputsRejectsEmptyCapturedAsset(t *testing.T) {
	t.Parallel()

	reader := frozenSnapshotReaderFunc(func(
		context.Context,
		string,
		int64,
	) (Snapshot, error) {
		return Snapshot{
			Bytes: nil,
			Meta:  MediaMeta{ContentType: "application/octet-stream"},
		}, nil
	})
	references, set, err := FreezeInputs(
		context.Background(),
		[]FreezeInput{{Locator: frozenTestMediaRef}},
		reader,
	)
	if !errors.Is(err, ErrFrozenMediaLimit) {
		t.Fatalf("FreezeInputs(empty asset) error = %v, want %v", err, ErrFrozenMediaLimit)
	}
	if references != nil || set.Version != 0 || len(set.Assets) != 0 || len(set.Blobs) != 0 {
		t.Fatalf("empty asset returned partial state: %#v/%#v", references, set)
	}
}

func TestFreezeInputsRejectsOversizedReaderOutputBeforeMetadata(t *testing.T) {
	t.Parallel()

	mimeCanary := "private-oversized-mime-canary"
	filenameCanary := "private-oversized-filename-canary"
	reader := frozenSnapshotReaderFunc(func(
		context.Context,
		string,
		int64,
	) (Snapshot, error) {
		return Snapshot{
			Bytes: make([]byte, MaxFrozenMediaAssetBytes+1),
			Meta: MediaMeta{
				ContentType: strings.Repeat(mimeCanary, 100),
				Filename:    strings.Repeat(filenameCanary, 200),
			},
		}, nil
	})
	references, set, err := FreezeInputs(
		context.Background(),
		[]FreezeInput{{Locator: frozenTestMediaRef}},
		reader,
	)
	if !errors.Is(err, ErrFrozenMediaLimit) {
		t.Fatalf("FreezeInputs(oversized custom reader bytes) error = %v, want %v", err, ErrFrozenMediaLimit)
	}
	if references != nil || set.Version != 0 {
		t.Fatalf("oversized custom reader returned partial state: %#v/%#v", references, set)
	}
	assertFrozenErrorRedacted(t, err, mimeCanary, filenameCanary, frozenTestMediaRef)
}

func TestFreezeInputsRejectsOversizedCapturedMetadataWithoutDisclosure(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		meta   MediaMeta
		canary string
	}{
		{
			name: "raw MIME",
			meta: MediaMeta{ContentType: "application/" +
				strings.Repeat("x", maxFrozenMediaTypeInputBytes) + "private-mime-canary"},
			canary: "private-mime-canary",
		},
		{
			name: "raw filename",
			meta: MediaMeta{Filename: strings.Repeat("x", maxFrozenMediaFilenameInputBytes) +
				"private-filename-canary"},
			canary: "private-filename-canary",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			reader := frozenSnapshotReaderFunc(func(
				context.Context,
				string,
				int64,
			) (Snapshot, error) {
				return Snapshot{Bytes: []byte("x"), Meta: test.meta}, nil
			})
			references, set, err := FreezeInputs(
				context.Background(),
				[]FreezeInput{{Locator: frozenTestMediaRef}},
				reader,
			)
			if !errors.Is(err, ErrFrozenMediaLimit) {
				t.Fatalf("FreezeInputs() error = %v, want %v", err, ErrFrozenMediaLimit)
			}
			if references != nil || set.Version != 0 {
				t.Fatalf("oversized metadata returned partial state: %#v/%#v", references, set)
			}
			assertFrozenErrorRedacted(t, err, test.canary, frozenTestMediaRef)
		})
	}
}

func TestFreezeInputsMalformedLaterDataURIPreventsEarlierLiveRead(t *testing.T) {
	t.Parallel()

	malformed := "data:text/plain;base64,private-invalid-inline-canary!"
	readerCalls := 0
	reader := frozenSnapshotReaderFunc(func(
		context.Context,
		string,
		int64,
	) (Snapshot, error) {
		readerCalls++
		return Snapshot{Bytes: []byte("live"), Meta: MediaMeta{ContentType: "text/plain"}}, nil
	})
	references, set, err := FreezeInputs(context.Background(), []FreezeInput{
		{Locator: frozenTestMediaRef},
		{Locator: malformed},
	}, reader)
	if !errors.Is(err, ErrFrozenMediaInvalid) {
		t.Fatalf("FreezeInputs() error = %v, want %v", err, ErrFrozenMediaInvalid)
	}
	if readerCalls != 0 {
		t.Fatalf("ReadSnapshot() calls = %d, want 0", readerCalls)
	}
	if references != nil || set.Version != 0 {
		t.Fatalf("malformed inline returned partial state: %#v/%#v", references, set)
	}
	assertFrozenErrorRedacted(t, err, malformed, frozenTestMediaRef)
}

func frozenTestSet(t *testing.T, inputs []FreezeInput) ([]FrozenReference, FrozenSet) {
	t.Helper()
	references, set, err := FreezeInputs(context.Background(), inputs, nil)
	if err != nil {
		t.Fatalf("FreezeInputs() error = %v", err)
	}
	return references, set
}

func frozenTestDataURI(contentType string, data []byte) string {
	return "data:" + contentType + ";base64," + base64.StdEncoding.EncodeToString(data)
}

func frozenTestDecodeDataURI(t *testing.T, uri string) []byte {
	t.Helper()
	_, payload, ok := strings.Cut(uri, ",")
	if !ok {
		t.Fatalf("invalid data URI %q", uri)
	}
	decoded, err := base64.StdEncoding.Strict().DecodeString(payload)
	if err != nil {
		t.Fatalf("decode data URI: %v", err)
	}
	return decoded
}

func frozenTestMarshalWire(set FrozenSet) ([]byte, error) {
	type frozenSetWire FrozenSet
	return json.Marshal(frozenSetWire(set))
}

func frozenTestAlterDigest(digest string) string {
	replacement := byte('0')
	if digest[0] == replacement {
		replacement = '1'
	}
	return string(replacement) + digest[1:]
}
