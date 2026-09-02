//go:build (!goolm && !cgo) || mipsle || netbsd || (freebsd && arm) || android

package migration

import (
	"context"

	"github.com/sipeed/picoclaw/pkg/database"
)

func migrateMatrixDatabase(context.Context, string) error {
	return database.NewError(database.CodeUnsupported, "Matrix database migration is unavailable on this target")
}
