//go:build (goolm || cgo) && !mipsle && !netbsd && !(freebsd && arm) && !android

package migration

import (
	"context"

	matrixsqlite "github.com/sipeed/picoclaw/internal/channelstore/matrixstore/sqliteadapter"
)

func migrateMatrixDatabase(ctx context.Context, path string) error {
	return matrixsqlite.MigrateDatabase(ctx, path)
}
