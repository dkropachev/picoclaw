package media

import (
	"context"
	"errors"
	"io"
	"strings"

	"github.com/google/uuid"
)

var (
	// ErrSnapshotUnavailable reports that a media reference cannot be captured.
	// It deliberately carries no reference or local-path diagnostics.
	ErrSnapshotUnavailable = errors.New("media snapshot is unavailable")
	// ErrSnapshotNotRegular rejects symbolic links and non-regular files.
	ErrSnapshotNotRegular = errors.New("media snapshot source is not a regular file")
	// ErrSnapshotTooLarge reports that a source exceeds the caller's exact limit.
	ErrSnapshotTooLarge = errors.New("media snapshot exceeds size limit")
	// ErrSnapshotChanged reports that the source changed while it was captured.
	ErrSnapshotChanged = errors.New("media snapshot source changed during capture")
)

// Snapshot is one detached, bounded copy of a media reference. Bytes never
// alias store-owned state, and Meta contains no local path.
type Snapshot struct {
	Bytes []byte
	Meta  MediaMeta
}

// SnapshotReader is the optional strict capture capability used by consumers
// that must retain exact media bytes after MediaStore cleanup or restart.
// Implementations must not expose a source reference or path through errors.
type SnapshotReader interface {
	ReadSnapshot(ctx context.Context, ref string, maxBytes int64) (Snapshot, error)
}

var _ SnapshotReader = (*FileMediaStore)(nil)

// ReadSnapshot captures one FileMediaStore reference while retaining the
// store's read lock. Cleanup cannot remove its mapping or managed file during
// the bounded read.
func (s *FileMediaStore) ReadSnapshot(
	ctx context.Context,
	ref string,
	maxBytes int64,
) (Snapshot, error) {
	if err := ctx.Err(); err != nil {
		return Snapshot{}, err
	}
	if s == nil || maxBytes <= 0 || !canonicalFileMediaRef(ref) {
		return Snapshot{}, ErrSnapshotUnavailable
	}

	s.mu.RLock()
	defer s.mu.RUnlock()

	entry, ok := s.refs[ref]
	if !ok {
		return Snapshot{}, ErrSnapshotUnavailable
	}
	file, err := openSnapshotFileNoFollow(entry.path)
	if err != nil {
		if errors.Is(err, ErrSnapshotNotRegular) {
			return Snapshot{}, ErrSnapshotNotRegular
		}
		return Snapshot{}, ErrSnapshotUnavailable
	}

	before, statErr := file.Stat()
	if statErr != nil {
		_ = file.Close()
		return Snapshot{}, ErrSnapshotUnavailable
	}
	if !before.Mode().IsRegular() {
		_ = file.Close()
		return Snapshot{}, ErrSnapshotNotRegular
	}
	if before.Size() < 0 || before.Size() > maxBytes {
		_ = file.Close()
		return Snapshot{}, ErrSnapshotTooLarge
	}
	beforeChange, changeErr := snapshotFileChangeToken(file, before)
	if changeErr != nil {
		_ = file.Close()
		return Snapshot{}, ErrSnapshotUnavailable
	}

	data, readErr := readSnapshotBytes(ctx, file, maxBytes, before.Size())
	after, afterErr := file.Stat()
	var afterChange any
	if afterErr == nil {
		afterChange, changeErr = snapshotFileChangeToken(file, after)
	}
	closeErr := file.Close()
	if readErr != nil {
		return Snapshot{}, readErr
	}
	if afterErr != nil || changeErr != nil || closeErr != nil {
		return Snapshot{}, ErrSnapshotUnavailable
	}
	if !after.Mode().IsRegular() ||
		!before.ModTime().Equal(after.ModTime()) ||
		before.Size() != after.Size() ||
		!sameSnapshotFile(before, after) ||
		!sameSnapshotChangeToken(beforeChange, afterChange) {
		return Snapshot{}, ErrSnapshotChanged
	}
	if err := ctx.Err(); err != nil {
		return Snapshot{}, err
	}

	return Snapshot{
		Bytes: append([]byte(nil), data...),
		Meta:  entry.meta,
	}, nil
}

func canonicalFileMediaRef(ref string) bool {
	identifier, ok := strings.CutPrefix(ref, "media://")
	if !ok || identifier == "" {
		return false
	}
	parsed, err := uuid.Parse(identifier)
	return err == nil && parsed.String() == identifier
}

func readSnapshotBytes(
	ctx context.Context,
	reader io.Reader,
	maxBytes int64,
	sizeHint int64,
) ([]byte, error) {
	capacity := int64(0)
	maxInt := int64(^uint(0) >> 1)
	if sizeHint > 0 && sizeHint <= maxBytes && sizeHint <= maxInt {
		capacity = sizeHint
	}
	data := make([]byte, 0, int(capacity))
	buffer := make([]byte, 32<<10)
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		n, err := reader.Read(buffer)
		if n > 0 {
			if int64(n) > maxBytes-int64(len(data)) {
				return nil, ErrSnapshotTooLarge
			}
			data = append(data, buffer[:n]...)
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return nil, ErrSnapshotUnavailable
		}
		if n == 0 {
			return nil, ErrSnapshotUnavailable
		}
	}
	return data, nil
}
