package gateway

import (
	"context"
	"fmt"
	"strings"

	"github.com/sipeed/picoclaw/pkg/config"
	eventoperator "github.com/sipeed/picoclaw/pkg/eventing/operator"
)

func prepareEventHTTPRoutesForConfig(
	runningServices *services,
	cfg *config.Config,
) error {
	if err := prepareEventWebhookRouteForConfig(runningServices, cfg); err != nil {
		return err
	}
	return prepareEventOperatorRouteForConfig(runningServices, cfg)
}

func prepareEventOperatorRouteForConfig(
	runningServices *services,
	cfg *config.Config,
) error {
	if cfg == nil || !cfg.Events.Ingress.Enabled {
		return nil
	}
	return ensureEventOperatorRoute(runningServices)
}

func prepareEventOperatorRoute(runningServices *services) error {
	if runningServices == nil ||
		runningServices.EventAutomation == nil ||
		runningServices.EventAutomation.operatorBackend == nil {
		return nil
	}
	return ensureEventOperatorRoute(runningServices)
}

func ensureEventOperatorRoute(runningServices *services) error {
	if runningServices == nil {
		return fmt.Errorf("gateway services are required for event operations")
	}
	if runningServices.eventOperatorController == nil {
		runningServices.eventOperatorController = eventoperator.NewController()
	}
	if runningServices.eventOperatorRelease != nil {
		return nil
	}
	if runningServices.ChannelManager == nil {
		return fmt.Errorf("shared HTTP channel manager is required for event operations")
	}
	authToken := strings.TrimSpace(runningServices.authToken)
	if runningServices.HealthServer == nil ||
		authToken == "" ||
		authToken != runningServices.authToken ||
		!runningServices.HealthServer.UsesBearerToken(authToken) {
		return fmt.Errorf("protected gateway runtime is required for event operations")
	}

	release, err := runningServices.ChannelManager.RegisterHTTPRoute(
		eventoperator.RoutePrefix,
		runningServices.HealthServer.Protect(runningServices.eventOperatorController),
	)
	if err != nil {
		return fmt.Errorf("register event operator API: %w", err)
	}
	runningServices.eventOperatorRelease = release
	return nil
}

type stagedEventOperator struct {
	reservation *eventoperator.StagedGeneration
	enabled     bool
}

func stageEventOperator(
	runningServices *services,
) (*stagedEventOperator, error) {
	backend := (*eventoperator.Backend)(nil)
	if runningServices != nil && runningServices.EventAutomation != nil {
		backend = runningServices.EventAutomation.operatorBackend
	}
	enabled := backend != nil
	if enabled {
		if err := prepareEventOperatorRoute(runningServices); err != nil {
			return nil, err
		}
	}
	if runningServices == nil || runningServices.eventOperatorController == nil {
		if enabled {
			return nil, fmt.Errorf(
				"event operator controller is required for configured backend",
			)
		}
		return &stagedEventOperator{}, nil
	}
	reservation, err := runningServices.eventOperatorController.Stage(backend)
	if err != nil {
		return nil, fmt.Errorf("stage event operator API: %w", err)
	}
	return &stagedEventOperator{
		reservation: reservation,
		enabled:     enabled,
	}, nil
}

func (staged *stagedEventOperator) commit(runningServices *services) {
	if staged == nil || runningServices == nil {
		return
	}
	if staged.reservation != nil {
		runningServices.eventOperatorGeneration = staged.reservation.Generation()
		staged.reservation.Commit()
	} else {
		runningServices.eventOperatorGeneration = eventoperator.Generation{}
	}
	if !staged.enabled {
		releaseEventOperatorRoute(runningServices)
	}
}

func activateEventOperator(runningServices *services) error {
	staged, err := stageEventOperator(runningServices)
	if err != nil {
		return err
	}
	staged.commit(runningServices)
	return nil
}

func deactivateEventOperator(
	ctx context.Context,
	runningServices *services,
) error {
	if runningServices == nil || runningServices.eventOperatorController == nil {
		return nil
	}
	if err := runningServices.eventOperatorController.Deactivate(
		ctx,
		runningServices.eventOperatorGeneration,
	); err != nil {
		return fmt.Errorf("drain event operator API: %w", err)
	}
	runningServices.eventOperatorGeneration = eventoperator.Generation{}
	return nil
}

func releaseEventOperatorRoute(runningServices *services) {
	if runningServices == nil || runningServices.eventOperatorRelease == nil {
		return
	}
	runningServices.eventOperatorRelease()
	runningServices.eventOperatorRelease = nil
}

func releaseEventHTTPRoutes(runningServices *services) {
	releaseEventOperatorRoute(runningServices)
	releaseEventWebhookRoute(runningServices)
}

func activateEventHTTPAdmissions(runningServices *services) error {
	webhookAdmission, err := stageEventWebhookAdmission(runningServices)
	if err != nil {
		return err
	}
	operatorAdmission, err := stageEventOperator(runningServices)
	if err != nil {
		webhookAdmission.abort()
		return err
	}

	operatorAdmission.commit(runningServices)
	webhookAdmission.commit(runningServices)
	return nil
}
