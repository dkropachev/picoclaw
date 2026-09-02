package database

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
)

// MaxFrameSize is the protocol v1 hard ceiling, including the JSON envelope
// but excluding the four-byte length prefix.
const MaxFrameSize uint32 = 128 << 20

var (
	ErrEmptyFrame    = errors.New("database protocol frame is empty")
	ErrFrameTooLarge = errors.New("database protocol frame exceeds 128 MiB")
)

// WriteFrame writes one four-byte big-endian length followed by canonical JSON.
func WriteFrame(writer io.Writer, value any) error {
	raw, err := MarshalCanonical(value)
	if err != nil {
		return err
	}
	return writeFrameBytes(writer, raw)
}

// ReadFrame reads and canonicality-checks one bounded length-prefixed JSON
// value. It never allocates from an unvalidated peer length.
func ReadFrame(reader io.Reader, destination any) error {
	raw, err := readFrameBytes(reader)
	if err != nil {
		return err
	}
	return UnmarshalCanonical(raw, destination)
}

func readFrameStrict(reader io.Reader, destination any) error {
	raw, err := readFrameBytes(reader)
	if err != nil {
		return err
	}
	return unmarshalCanonicalStrict(raw, destination)
}

func writeFrameBytes(writer io.Writer, raw []byte) error {
	if writer == nil {
		return NewError(CodeInvalid, "database protocol writer is nil")
	}
	if len(raw) == 0 {
		return ErrEmptyFrame
	}
	if uint64(len(raw)) > uint64(MaxFrameSize) {
		return ErrFrameTooLarge
	}
	var prefix [4]byte
	binary.BigEndian.PutUint32(prefix[:], uint32(len(raw)))
	if err := writeAll(writer, prefix[:]); err != nil {
		return fmt.Errorf("write database protocol frame length: %w", err)
	}
	if err := writeAll(writer, raw); err != nil {
		return fmt.Errorf("write database protocol frame body: %w", err)
	}
	return nil
}

func readFrameBytes(reader io.Reader) ([]byte, error) {
	if reader == nil {
		return nil, NewError(CodeInvalid, "database protocol reader is nil")
	}
	var prefix [4]byte
	if _, err := io.ReadFull(reader, prefix[:]); err != nil {
		return nil, fmt.Errorf("read database protocol frame length: %w", err)
	}
	size := binary.BigEndian.Uint32(prefix[:])
	if size == 0 {
		return nil, ErrEmptyFrame
	}
	if size > MaxFrameSize {
		return nil, ErrFrameTooLarge
	}
	raw := make([]byte, int(size))
	if _, err := io.ReadFull(reader, raw); err != nil {
		return nil, fmt.Errorf("read database protocol frame body: %w", err)
	}
	return raw, nil
}

func writeAll(writer io.Writer, data []byte) error {
	for len(data) > 0 {
		written, err := writer.Write(data)
		if err != nil {
			return err
		}
		if written <= 0 || written > len(data) {
			return io.ErrShortWrite
		}
		data = data[written:]
	}
	return nil
}
