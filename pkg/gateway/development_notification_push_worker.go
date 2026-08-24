package gateway

import "context"

const developmentNotificationPushBatchSize = 5

type developmentNotificationPushDeliveryService interface {
	DeliverPendingDevelopmentPush(ctx context.Context, limit int) (int, error)
}

type developmentNotificationPushWorker struct {
	service developmentNotificationPushDeliveryService
}

func (worker *developmentNotificationPushWorker) ProcessOne(ctx context.Context) (bool, error) {
	if worker == nil || worker.service == nil {
		return false, nil
	}
	processed, err := worker.service.DeliverPendingDevelopmentPush(ctx, developmentNotificationPushBatchSize)
	return processed > 0, err
}
