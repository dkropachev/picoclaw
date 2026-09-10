package sqliteprovider

import (
	"context"
	"testing"
	"time"
)

func maintainOfflineFixture(
	t *testing.T,
	ctx context.Context,
	path string,
	timeout time.Duration,
) (MaintenanceResult, error) {
	t.Helper()
	return maintainOffline(ctx, path, timeout, maintenanceOps{
		inspect: inspectAndRecover, boundary: exclusiveRollbackBoundary,
		checkpoint: checkpointGeneration, reopen: reopenAndValidate,
	})
}
