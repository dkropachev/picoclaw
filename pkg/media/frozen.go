package media

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/h2non/filetype"
)

const (
	// FrozenSetVersion identifies the canonical embedded-media representation.
	FrozenSetVersion = 1

	// MaxFrozenMediaOccurrences bounds locator work per operation.
	MaxFrozenMediaOccurrences = 32
	// MaxFrozenMediaAssets bounds distinct metadata-bound assets.
	MaxFrozenMediaAssets = 16
	// MaxFrozenMediaAssetBytes bounds one decoded asset.
	MaxFrozenMediaAssetBytes = 2 << 20
	// MaxFrozenMediaTotalBytes bounds occurrence-weighted decoded bytes.
	MaxFrozenMediaTotalBytes = 3 << 20
	// MaxFrozenMediaEncodedBytes bounds occurrence-weighted data URIs.
	MaxFrozenMediaEncodedBytes = 5 << 20
	// MaxFrozenSetJSONBytes bounds the serialized self-contained set.
	MaxFrozenSetJSONBytes = 5 << 20

	maxFrozenMediaRefBytes           = 4 << 10
	maxFrozenMediaTypeBytes          = 127
	maxFrozenMediaTypeInputBytes     = 1 << 10
	maxFrozenMediaFilenameBytes      = 255
	maxFrozenMediaFilenameInputBytes = 4 << 10
	maxFrozenDataURIBytes            = 3 << 20
)

const (
	frozenReferencePrefix = "frozen-media://sha256/"
	frozenAssetDomain     = "picoclaw/frozen-media/asset/v1\x00"
)

var (
	ErrFrozenMediaInvalid = errors.New("frozen media is invalid")
	ErrFrozenMediaScheme  = errors.New(
		"session media must be an available media reference or canonical data URI",
	)
	ErrFrozenMediaUnavailable = errors.New(
		"session media is unavailable; reattach it and retry",
	)
	ErrFrozenMediaLimit    = errors.New("frozen media exceeds safety limits")
	ErrFrozenMediaTampered = errors.New(
		"frozen media failed integrity validation",
	)
)

// FreezeInput is one structured message locator and its model-visible metadata.
// Locator itself is never retained in FrozenSet.
type FreezeInput struct {
	Locator     string
	ContentType string
	Filename    string
}

// FrozenReference is the immutable replacement for one FreezeInput.
type FrozenReference struct {
	Ref         string
	ContentType string
	Filename    string
	Size        int64
}

// Materialized is one integrity-checked, self-contained provider locator.
type Materialized struct {
	URI         string
	ContentType string
	Filename    string
	Size        int64
	SHA256      string
}

// FrozenSet is a canonical, self-contained collection of immutable assets.
// Blobs are deduplicated by byte digest; assets separately bind MIME type and
// safe filename so metadata-only changes retain distinct identities.
//
//nolint:recvcheck // JSON needs pointer unmarshal; validation and read APIs use values.
type FrozenSet struct {
	Version int           `json:"version"`
	Assets  []FrozenAsset `json:"assets,omitempty"`
	Blobs   []FrozenBlob  `json:"blobs,omitempty"`
}

type FrozenAsset struct {
	ID          string `json:"id"`
	BlobSHA256  string `json:"blob_sha256"`
	ContentType string `json:"content_type"`
	Filename    string `json:"filename,omitempty"`
	Size        int64  `json:"size"`
}

type FrozenBlob struct {
	SHA256 string `json:"sha256"`
	Data   []byte `json:"data"`
}

type frozenRawMedia struct {
	data        []byte
	contentType string
	filename    string
}

var frozenCaptureSlots = make(chan struct{}, 4)

// FreezeInputs captures every input or fails without a partial result. Only
// media:// references and canonical padded-base64 data URIs are accepted.
func FreezeInputs(
	ctx context.Context,
	inputs []FreezeInput,
	reader SnapshotReader,
) ([]FrozenReference, FrozenSet, error) {
	if err := ctx.Err(); err != nil {
		return nil, FrozenSet{}, err
	}
	if err := preflightFreezeInputs(inputs); err != nil {
		return nil, FrozenSet{}, err
	}
	select {
	case frozenCaptureSlots <- struct{}{}:
		defer func() { <-frozenCaptureSlots }()
	case <-ctx.Done():
		return nil, FrozenSet{}, ctx.Err()
	}

	rawByLocator, err := preflightFrozenInlineInputs(ctx, inputs)
	if err != nil {
		return nil, FrozenSet{}, err
	}
	blobs := make(map[string]FrozenBlob, len(inputs))
	assets := make(map[string]FrozenAsset, len(inputs))
	results := make([]FrozenReference, len(inputs))
	var occurrenceBytes int64
	var encodedBytes int64

	for index, input := range inputs {
		if err := ctx.Err(); err != nil {
			return nil, FrozenSet{}, err
		}
		raw, ok := rawByLocator[input.Locator]
		if !ok {
			var err error
			raw, err = captureFrozenInput(ctx, input.Locator, reader)
			if err != nil {
				return nil, FrozenSet{}, fmt.Errorf(
					"freeze session media locator %d: %w",
					index,
					err,
				)
			}
			raw.data = append([]byte(nil), raw.data...)
			rawByLocator[input.Locator] = raw
		}

		if len(raw.data) == 0 || len(raw.data) > MaxFrozenMediaAssetBytes {
			return nil, FrozenSet{}, fmt.Errorf(
				"freeze session media locator %d: %w",
				index,
				ErrFrozenMediaLimit,
			)
		}
		occurrenceBytes += int64(len(raw.data))
		if occurrenceBytes > MaxFrozenMediaTotalBytes {
			return nil, FrozenSet{}, ErrFrozenMediaLimit
		}

		contentType, err := chooseFrozenContentType(
			input.ContentType,
			raw.contentType,
			raw.data,
		)
		if err != nil {
			if errors.Is(err, ErrFrozenMediaLimit) {
				return nil, FrozenSet{}, ErrFrozenMediaLimit
			}
			return nil, FrozenSet{}, fmt.Errorf(
				"freeze session media locator %d: %w",
				index,
				ErrFrozenMediaInvalid,
			)
		}
		filenameInput := input.Filename
		if filenameInput == "" {
			filenameInput = raw.filename
		}
		filename, err := canonicalFrozenFilename(filenameInput)
		if err != nil {
			if errors.Is(err, ErrFrozenMediaLimit) {
				return nil, FrozenSet{}, ErrFrozenMediaLimit
			}
			return nil, FrozenSet{}, fmt.Errorf(
				"freeze session media locator %d: %w",
				index,
				ErrFrozenMediaInvalid,
			)
		}

		blobID := frozenBlobDigest(raw.data)
		if _, exists := blobs[blobID]; !exists {
			blobs[blobID] = FrozenBlob{
				SHA256: blobID,
				Data:   append([]byte(nil), raw.data...),
			}
		}
		assetID := frozenAssetDigest(blobID, contentType, filename, int64(len(raw.data)))
		if _, exists := assets[assetID]; !exists {
			if len(assets) >= MaxFrozenMediaAssets {
				return nil, FrozenSet{}, ErrFrozenMediaLimit
			}
			assets[assetID] = FrozenAsset{
				ID:          assetID,
				BlobSHA256:  blobID,
				ContentType: contentType,
				Filename:    filename,
				Size:        int64(len(raw.data)),
			}
		}

		encodedBytes += frozenDataURISize(contentType, len(raw.data))
		if encodedBytes > MaxFrozenMediaEncodedBytes {
			return nil, FrozenSet{}, ErrFrozenMediaLimit
		}
		results[index] = FrozenReference{
			Ref:         frozenReferencePrefix + assetID,
			ContentType: contentType,
			Filename:    filename,
			Size:        int64(len(raw.data)),
		}
	}

	set := FrozenSet{
		Version: FrozenSetVersion,
		Assets:  make([]FrozenAsset, 0, len(assets)),
		Blobs:   make([]FrozenBlob, 0, len(blobs)),
	}
	for _, asset := range assets {
		set.Assets = append(set.Assets, asset)
	}
	for _, blob := range blobs {
		set.Blobs = append(set.Blobs, blob)
	}
	sort.Slice(set.Assets, func(i, j int) bool { return set.Assets[i].ID < set.Assets[j].ID })
	sort.Slice(set.Blobs, func(i, j int) bool { return set.Blobs[i].SHA256 < set.Blobs[j].SHA256 })
	if err := set.Validate(); err != nil {
		return nil, FrozenSet{}, err
	}
	return results, set, nil
}

// Materialize verifies the complete set and replaces its exact frozen refs
// with canonical data URIs. Every stored asset must be referenced at least once.
func (set FrozenSet) Materialize(
	ctx context.Context,
	refs []string,
) ([]Materialized, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if len(refs) > MaxFrozenMediaOccurrences {
		return nil, ErrFrozenMediaLimit
	}
	if err := set.Validate(); err != nil {
		return nil, err
	}
	assets := make(map[string]FrozenAsset, len(set.Assets))
	for _, asset := range set.Assets {
		assets[asset.ID] = asset
	}
	blobs := make(map[string][]byte, len(set.Blobs))
	for _, blob := range set.Blobs {
		blobs[blob.SHA256] = blob.Data
	}

	results := make([]Materialized, len(refs))
	dataURIs := make(map[string]string, len(set.Assets))
	usedAssets := make(map[string]struct{}, len(set.Assets))
	var occurrenceBytes int64
	var encodedBytes int64
	for index, ref := range refs {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		assetID, ok := parseFrozenReference(ref)
		if !ok {
			return nil, fmt.Errorf(
				"materialize frozen media locator %d: %w",
				index,
				ErrFrozenMediaInvalid,
			)
		}
		asset, ok := assets[assetID]
		if !ok {
			return nil, ErrFrozenMediaTampered
		}
		data := blobs[asset.BlobSHA256]
		occurrenceBytes += int64(len(data))
		encodedBytes += frozenDataURISize(asset.ContentType, len(data))
		if occurrenceBytes > MaxFrozenMediaTotalBytes ||
			encodedBytes > MaxFrozenMediaEncodedBytes {
			return nil, ErrFrozenMediaLimit
		}
		uri, exists := dataURIs[assetID]
		if !exists {
			uri = "data:" + asset.ContentType + ";base64," +
				base64.StdEncoding.EncodeToString(data)
			dataURIs[assetID] = uri
		}
		results[index] = Materialized{
			URI:         uri,
			ContentType: asset.ContentType,
			Filename:    asset.Filename,
			Size:        asset.Size,
			SHA256:      asset.BlobSHA256,
		}
		usedAssets[assetID] = struct{}{}
	}
	if len(usedAssets) != len(set.Assets) {
		return nil, ErrFrozenMediaTampered
	}
	return results, nil
}

// Validate rechecks canonical ordering, bounds, metadata, sizes, and digests.
func (set FrozenSet) Validate() error {
	if set.Version != FrozenSetVersion ||
		len(set.Assets) > MaxFrozenMediaAssets ||
		len(set.Blobs) > MaxFrozenMediaAssets {
		return ErrFrozenMediaInvalid
	}
	if (len(set.Assets) == 0) != (len(set.Blobs) == 0) {
		return ErrFrozenMediaInvalid
	}

	blobs := make(map[string]FrozenBlob, len(set.Blobs))
	var totalBytes int64
	previous := ""
	for _, blob := range set.Blobs {
		if !validFrozenDigest(blob.SHA256) ||
			(previous != "" && blob.SHA256 <= previous) ||
			len(blob.Data) == 0 ||
			len(blob.Data) > MaxFrozenMediaAssetBytes ||
			frozenBlobDigest(blob.Data) != blob.SHA256 {
			return ErrFrozenMediaTampered
		}
		previous = blob.SHA256
		totalBytes += int64(len(blob.Data))
		if totalBytes > MaxFrozenMediaTotalBytes {
			return ErrFrozenMediaLimit
		}
		blobs[blob.SHA256] = blob
	}

	usedBlobs := make(map[string]struct{}, len(blobs))
	previous = ""
	for _, asset := range set.Assets {
		if !validFrozenDigest(asset.ID) ||
			!validFrozenDigest(asset.BlobSHA256) ||
			(previous != "" && asset.ID <= previous) {
			return ErrFrozenMediaTampered
		}
		previous = asset.ID
		contentType, err := canonicalFrozenMediaType(asset.ContentType)
		if err != nil || contentType != asset.ContentType {
			return ErrFrozenMediaTampered
		}
		filename, err := canonicalFrozenFilename(asset.Filename)
		if err != nil || filename != asset.Filename {
			return ErrFrozenMediaTampered
		}
		blob, ok := blobs[asset.BlobSHA256]
		if !ok || asset.Size != int64(len(blob.Data)) ||
			frozenAssetDigest(
				asset.BlobSHA256,
				asset.ContentType,
				asset.Filename,
				asset.Size,
			) != asset.ID {
			return ErrFrozenMediaTampered
		}
		usedBlobs[asset.BlobSHA256] = struct{}{}
	}
	if len(usedBlobs) != len(blobs) {
		return ErrFrozenMediaTampered
	}
	return nil
}

func (set FrozenSet) Clone() FrozenSet {
	clone := FrozenSet{
		Version: set.Version,
		Assets:  append([]FrozenAsset(nil), set.Assets...),
		Blobs:   make([]FrozenBlob, len(set.Blobs)),
	}
	for index, blob := range set.Blobs {
		clone.Blobs[index] = FrozenBlob{
			SHA256: blob.SHA256,
			Data:   append([]byte(nil), blob.Data...),
		}
	}
	return clone
}

func (set FrozenSet) MarshalJSON() ([]byte, error) {
	if err := set.Validate(); err != nil {
		return nil, err
	}
	type frozenSetJSON FrozenSet
	data, err := json.Marshal(frozenSetJSON(set))
	if err != nil {
		return nil, err
	}
	if len(data) > MaxFrozenSetJSONBytes {
		return nil, ErrFrozenMediaLimit
	}
	return data, nil
}

func (set *FrozenSet) UnmarshalJSON(data []byte) error {
	if set == nil || len(data) > MaxFrozenSetJSONBytes {
		return ErrFrozenMediaLimit
	}
	if err := validateFrozenJSONStringEncoding(data); err != nil {
		return err
	}
	if err := validateFrozenSetJSONShape(data); err != nil {
		if errors.Is(err, ErrFrozenMediaLimit) {
			return ErrFrozenMediaLimit
		}
		return ErrFrozenMediaInvalid
	}
	type rawBlob struct {
		SHA256 string `json:"sha256"`
		Data   string `json:"data"`
	}
	type rawSet struct {
		Version int           `json:"version"`
		Assets  []FrozenAsset `json:"assets,omitempty"`
		Blobs   []rawBlob     `json:"blobs,omitempty"`
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var raw rawSet
	if err := decoder.Decode(&raw); err != nil {
		return ErrFrozenMediaInvalid
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return ErrFrozenMediaInvalid
	}

	decoded := FrozenSet{
		Version: raw.Version,
		Assets:  append([]FrozenAsset(nil), raw.Assets...),
		Blobs:   make([]FrozenBlob, len(raw.Blobs)),
	}
	for index, blob := range raw.Blobs {
		if len(blob.Data) > base64.StdEncoding.EncodedLen(MaxFrozenMediaAssetBytes) {
			return ErrFrozenMediaLimit
		}
		bytesValue, err := base64.StdEncoding.Strict().DecodeString(blob.Data)
		if err != nil || base64.StdEncoding.EncodeToString(bytesValue) != blob.Data {
			return ErrFrozenMediaInvalid
		}
		decoded.Blobs[index] = FrozenBlob{SHA256: blob.SHA256, Data: bytesValue}
	}
	if err := decoded.Validate(); err != nil {
		return err
	}
	*set = decoded
	return nil
}

func validateFrozenSetJSONShape(data []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	tokens := 0
	if err := consumeFrozenSetJSONValue(decoder, 0, &tokens); err != nil {
		return err
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return ErrFrozenMediaInvalid
	}
	return nil
}

func validateFrozenJSONStringEncoding(data []byte) error {
	if !utf8.Valid(data) {
		return ErrFrozenMediaInvalid
	}
	for index := 0; index < len(data); index++ {
		if data[index] != '"' {
			continue
		}
		index++
		for {
			if index >= len(data) {
				return ErrFrozenMediaInvalid
			}
			switch data[index] {
			case '"':
				goto stringClosed
			case '\\':
				index++
				if index >= len(data) {
					return ErrFrozenMediaInvalid
				}
				if data[index] != 'u' {
					if !strings.ContainsRune(`"\\/bfnrt`, rune(data[index])) {
						return ErrFrozenMediaInvalid
					}
					index++
					continue
				}
				code, ok := frozenJSONHexQuad(data, index+1)
				if !ok {
					return ErrFrozenMediaInvalid
				}
				index += 5
				switch {
				case code >= 0xd800 && code <= 0xdbff:
					if index+6 > len(data) || data[index] != '\\' || data[index+1] != 'u' {
						return ErrFrozenMediaInvalid
					}
					low, lowOK := frozenJSONHexQuad(data, index+2)
					if !lowOK || low < 0xdc00 || low > 0xdfff {
						return ErrFrozenMediaInvalid
					}
					index += 6
				case code >= 0xdc00 && code <= 0xdfff:
					return ErrFrozenMediaInvalid
				}
				continue
			default:
				if data[index] < 0x20 {
					return ErrFrozenMediaInvalid
				}
				_, size := utf8.DecodeRune(data[index:])
				index += size
			}
		}
	stringClosed:
	}
	return nil
}

func frozenJSONHexQuad(data []byte, offset int) (uint16, bool) {
	if offset < 0 || offset+4 > len(data) {
		return 0, false
	}
	var value uint16
	for _, character := range data[offset : offset+4] {
		value <<= 4
		switch {
		case character >= '0' && character <= '9':
			value |= uint16(character - '0')
		case character >= 'a' && character <= 'f':
			value |= uint16(character-'a') + 10
		case character >= 'A' && character <= 'F':
			value |= uint16(character-'A') + 10
		default:
			return 0, false
		}
	}
	return value, true
}

func consumeFrozenSetJSONValue(decoder *json.Decoder, depth int, tokens *int) error {
	const (
		maxDepth  = 8
		maxTokens = 1024
	)
	if depth > maxDepth {
		return ErrFrozenMediaInvalid
	}
	(*tokens)++
	if *tokens > maxTokens {
		return ErrFrozenMediaLimit
	}
	token, err := decoder.Token()
	if err != nil || token == nil {
		return ErrFrozenMediaInvalid
	}
	delimiter, composite := token.(json.Delim)
	if !composite {
		return nil
	}
	switch delimiter {
	case '{':
		allowed := map[string]struct{}{
			"version": {}, "assets": {}, "blobs": {},
			"id": {}, "blob_sha256": {}, "content_type": {},
			"filename": {}, "size": {}, "sha256": {}, "data": {},
		}
		seen := make(map[string]struct{})
		for decoder.More() {
			keyToken, keyErr := decoder.Token()
			if keyErr != nil {
				return ErrFrozenMediaInvalid
			}
			key, ok := keyToken.(string)
			if !ok {
				return ErrFrozenMediaInvalid
			}
			if _, ok = allowed[key]; !ok {
				return ErrFrozenMediaInvalid
			}
			folded := strings.ToLower(key)
			if _, duplicate := seen[folded]; duplicate {
				return ErrFrozenMediaInvalid
			}
			seen[folded] = struct{}{}
			if err = consumeFrozenSetJSONValue(decoder, depth+1, tokens); err != nil {
				return err
			}
		}
		closing, closeErr := decoder.Token()
		if closeErr != nil || closing != json.Delim('}') {
			return ErrFrozenMediaInvalid
		}
	case '[':
		for decoder.More() {
			if err = consumeFrozenSetJSONValue(decoder, depth+1, tokens); err != nil {
				return err
			}
		}
		closing, closeErr := decoder.Token()
		if closeErr != nil || closing != json.Delim(']') {
			return ErrFrozenMediaInvalid
		}
	default:
		return ErrFrozenMediaInvalid
	}
	return nil
}

func preflightFreezeInputs(inputs []FreezeInput) error {
	if len(inputs) > MaxFrozenMediaOccurrences {
		return ErrFrozenMediaLimit
	}
	for index, input := range inputs {
		if input.Locator == "" {
			return fmt.Errorf(
				"freeze session media locator %d: %w",
				index,
				ErrFrozenMediaInvalid,
			)
		}
		if len(input.Locator) > maxFrozenDataURIBytes {
			return ErrFrozenMediaLimit
		}
		switch {
		case strings.HasPrefix(input.Locator, "media://"):
			if len(input.Locator) > maxFrozenMediaRefBytes {
				return ErrFrozenMediaLimit
			}
			if !canonicalFileMediaRef(input.Locator) {
				return ErrFrozenMediaScheme
			}
		case strings.HasPrefix(input.Locator, "data:"):
		default:
			return fmt.Errorf(
				"freeze session media locator %d: %w",
				index,
				ErrFrozenMediaScheme,
			)
		}
		if strings.TrimSpace(input.Locator) != input.Locator ||
			!utf8.ValidString(input.Locator) {
			return fmt.Errorf(
				"freeze session media locator %d: %w",
				index,
				ErrFrozenMediaInvalid,
			)
		}
		if input.ContentType != "" {
			if _, err := canonicalFrozenMediaType(input.ContentType); err != nil {
				return err
			}
		}
		if _, err := canonicalFrozenFilename(input.Filename); err != nil {
			return err
		}
	}
	return nil
}

func preflightFrozenInlineInputs(
	ctx context.Context,
	inputs []FreezeInput,
) (map[string]frozenRawMedia, error) {
	rawByLocator := make(map[string]frozenRawMedia, len(inputs))
	var occurrenceBytes int64
	for index, input := range inputs {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if !strings.HasPrefix(input.Locator, "data:") {
			continue
		}
		raw, ok := rawByLocator[input.Locator]
		if !ok {
			contentType, data, err := decodeCanonicalDataURI(input.Locator)
			if err != nil {
				return nil, fmt.Errorf(
					"freeze session media locator %d: %w",
					index,
					err,
				)
			}
			raw = frozenRawMedia{data: data, contentType: contentType}
			rawByLocator[input.Locator] = raw
		}
		if _, err := chooseFrozenContentType(input.ContentType, raw.contentType, raw.data); err != nil {
			if errors.Is(err, ErrFrozenMediaLimit) {
				return nil, ErrFrozenMediaLimit
			}
			return nil, fmt.Errorf(
				"freeze session media locator %d: %w",
				index,
				ErrFrozenMediaInvalid,
			)
		}
		occurrenceBytes += int64(len(raw.data))
		if occurrenceBytes > MaxFrozenMediaTotalBytes {
			return nil, ErrFrozenMediaLimit
		}
	}
	return rawByLocator, nil
}

func captureFrozenInput(
	ctx context.Context,
	locator string,
	reader SnapshotReader,
) (frozenRawMedia, error) {
	result := frozenRawMedia{}
	if reader == nil {
		return result, ErrFrozenMediaUnavailable
	}
	snapshot, err := reader.ReadSnapshot(ctx, locator, MaxFrozenMediaAssetBytes)
	if err != nil {
		if errors.Is(err, context.Canceled) {
			return result, context.Canceled
		}
		if errors.Is(err, context.DeadlineExceeded) {
			return result, context.DeadlineExceeded
		}
		if errors.Is(err, ErrSnapshotTooLarge) {
			return result, ErrFrozenMediaLimit
		}
		return result, ErrFrozenMediaUnavailable
	}
	if len(snapshot.Bytes) == 0 || len(snapshot.Bytes) > MaxFrozenMediaAssetBytes {
		return result, ErrFrozenMediaLimit
	}
	result.data = snapshot.Bytes
	result.contentType = snapshot.Meta.ContentType
	result.filename = snapshot.Meta.Filename
	return result, nil
}

func decodeCanonicalDataURI(locator string) (string, []byte, error) {
	header, payload, ok := strings.Cut(strings.TrimPrefix(locator, "data:"), ",")
	if !ok || !strings.HasSuffix(header, ";base64") || payload == "" {
		return "", nil, ErrFrozenMediaInvalid
	}
	mediaTypeInput := strings.TrimSuffix(header, ";base64")
	mediaType, err := canonicalFrozenMediaType(mediaTypeInput)
	if err != nil || mediaType != mediaTypeInput {
		return "", nil, ErrFrozenMediaInvalid
	}
	if len(payload) > base64.StdEncoding.EncodedLen(MaxFrozenMediaAssetBytes) {
		return "", nil, ErrFrozenMediaLimit
	}
	data, err := base64.StdEncoding.Strict().DecodeString(payload)
	if err != nil || len(data) == 0 || len(data) > MaxFrozenMediaAssetBytes ||
		base64.StdEncoding.EncodeToString(data) != payload {
		return "", nil, ErrFrozenMediaInvalid
	}
	return mediaType, data, nil
}

func chooseFrozenContentType(explicit, captured string, data []byte) (string, error) {
	var explicitType string
	if explicit != "" {
		var err error
		explicitType, err = canonicalFrozenMediaType(explicit)
		if err != nil {
			return "", err
		}
	}
	var capturedType string
	if captured != "" {
		if len(captured) > maxFrozenMediaTypeInputBytes {
			return "", ErrFrozenMediaLimit
		}
		mediaType, _, err := mime.ParseMediaType(captured)
		if err != nil {
			return "", err
		}
		capturedType, err = canonicalFrozenMediaType(strings.ToLower(mediaType))
		if err != nil {
			return "", err
		}
	}
	if explicitType != "" && capturedType != "" && explicitType != capturedType {
		return "", ErrFrozenMediaInvalid
	}
	if explicitType != "" {
		return explicitType, nil
	}
	if capturedType != "" {
		return capturedType, nil
	}
	if kind, err := filetype.Match(data); err == nil && kind != filetype.Unknown {
		return canonicalFrozenMediaType(strings.ToLower(kind.MIME.Value))
	}
	detected, _, err := mime.ParseMediaType(http.DetectContentType(data))
	if err != nil {
		return "", err
	}
	return canonicalFrozenMediaType(strings.ToLower(detected))
}

func canonicalFrozenMediaType(value string) (string, error) {
	if value == "" {
		return "", ErrFrozenMediaInvalid
	}
	if len(value) > maxFrozenMediaTypeBytes {
		return "", ErrFrozenMediaLimit
	}
	if strings.TrimSpace(value) != value || !utf8.ValidString(value) {
		return "", ErrFrozenMediaInvalid
	}
	mediaType, params, err := mime.ParseMediaType(value)
	if err != nil || len(params) != 0 || !strings.Contains(mediaType, "/") {
		return "", ErrFrozenMediaInvalid
	}
	mediaType = strings.ToLower(mediaType)
	if len(mediaType) > maxFrozenMediaTypeBytes {
		return "", ErrFrozenMediaLimit
	}
	return mediaType, nil
}

func canonicalFrozenFilename(value string) (string, error) {
	if value == "" {
		return "", nil
	}
	if len(value) > maxFrozenMediaFilenameInputBytes {
		return "", ErrFrozenMediaLimit
	}
	if !utf8.ValidString(value) {
		return "", ErrFrozenMediaInvalid
	}
	value = strings.TrimRight(value, "/\\")
	if separator := strings.LastIndexAny(value, "/\\"); separator >= 0 {
		value = value[separator+1:]
	}
	if value == "." || value == ".." || value == "" {
		return "", ErrFrozenMediaInvalid
	}
	if len(value) > maxFrozenMediaFilenameBytes {
		return "", ErrFrozenMediaLimit
	}
	for _, character := range value {
		if unicode.IsControl(character) {
			return "", ErrFrozenMediaInvalid
		}
	}
	return value, nil
}

func frozenBlobDigest(data []byte) string {
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:])
}

func frozenAssetDigest(blobID, contentType, filename string, size int64) string {
	hash := sha256.New()
	_, _ = hash.Write([]byte(frozenAssetDomain))
	_, _ = hash.Write([]byte(blobID))
	_, _ = hash.Write([]byte{0})
	_, _ = hash.Write([]byte(contentType))
	_, _ = hash.Write([]byte{0})
	_, _ = hash.Write([]byte(filename))
	_, _ = fmt.Fprintf(hash, "\x00%d", size)
	return hex.EncodeToString(hash.Sum(nil))
}

func frozenDataURISize(contentType string, dataBytes int) int64 {
	return int64(len("data:") + len(contentType) + len(";base64,") +
		base64.StdEncoding.EncodedLen(dataBytes))
}

func parseFrozenReference(ref string) (string, bool) {
	assetID, ok := strings.CutPrefix(ref, frozenReferencePrefix)
	return assetID, ok && validFrozenDigest(assetID)
}

func validFrozenDigest(value string) bool {
	if len(value) != sha256.Size*2 || strings.ToLower(value) != value {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}
