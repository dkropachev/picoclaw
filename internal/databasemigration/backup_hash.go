package databasemigration

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
)

func hashPreparedGenerationMember(ctx context.Context, file *os.File, limit int64) (string, error) {
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return "", err
	}
	digest := sha256.New()
	size, err := copyWithContext(ctx, io.Discard, digest, file, limit)
	if err != nil {
		return "", err
	}
	if size != limit {
		return "", errors.New("prepared generation member size changed")
	}
	return hex.EncodeToString(digest.Sum(nil)), nil
}
