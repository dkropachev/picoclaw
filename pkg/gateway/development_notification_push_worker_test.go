package gateway

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
)

type developmentNotificationPushServiceStub struct {
	processed int
	err       error
	limit     int
}

func (stub *developmentNotificationPushServiceStub) DeliverPendingDevelopmentPush(
	_ context.Context, limit int,
) (int, error) {
	stub.limit = limit
	return stub.processed, stub.err
}

func TestDevelopmentNotificationPushWorkerUsesBoundedBatch(t *testing.T) {
	stub := &developmentNotificationPushServiceStub{processed: 2}
	processed, err := (&developmentNotificationPushWorker{service: stub}).ProcessOne(context.Background())
	require.NoError(t, err)
	require.True(t, processed)
	require.Equal(t, developmentNotificationPushBatchSize, stub.limit)

	stub.processed = 0
	stub.err = errors.New("delivery unavailable")
	processed, err = (&developmentNotificationPushWorker{service: stub}).ProcessOne(context.Background())
	require.False(t, processed)
	require.Error(t, err)
}
