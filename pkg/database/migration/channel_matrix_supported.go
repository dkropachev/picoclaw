//go:build (goolm || cgo) && !mipsle && !netbsd && !(freebsd && arm) && !android

package migration

import (
	"context"

	"github.com/sipeed/picoclaw/pkg/channels/matrix"
)

func migrateMatrixDatabase(ctx context.Context, path string) error {
	return matrix.MigrateCryptoDatabase(ctx, path)
}
