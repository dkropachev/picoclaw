package gateway

import (
	"context"
	"fmt"

	"github.com/sipeed/picoclaw/pkg/config"
	eventwebhook "github.com/sipeed/picoclaw/pkg/eventing/webhook"
)

func prepareEventWebhookRouteForConfig(
	runningServices *services,
	cfg *config.Config,
) error {
	if cfg == nil || !cfg.Events.Ingress.Enabled {
		return nil
	}
	for _, connector := range cfg.Events.Ingress.Webhooks {
		if connector.Enabled {
			return ensureEventWebhookRoute(runningServices)
		}
	}
	return nil
}

// prepareEventWebhookRoute makes a candidate backend reachable only through an
// inactive stable controller. Activation is a separate commit operation so a
// replacement service can finish every fallible startup step without
// acknowledging requests into provisional storage.
func prepareEventWebhookRoute(runningServices *services) error {
	if runningServices == nil ||
		runningServices.EventAutomation == nil ||
		runningServices.EventAutomation.webhookBackend == nil ||
		runningServices.EventAutomation.webhookBackend.ConnectorCount() == 0 {
		return nil
	}
	return ensureEventWebhookRoute(runningServices)
}

func ensureEventWebhookRoute(runningServices *services) error {
	if runningServices == nil {
		return fmt.Errorf("gateway services are required for event webhooks")
	}
	if runningServices.eventWebhookController == nil {
		runningServices.eventWebhookController = eventwebhook.NewController()
	}
	if runningServices.eventWebhookRelease != nil {
		return nil
	}
	if runningServices.ChannelManager == nil {
		return fmt.Errorf("shared HTTP channel manager is required for event webhooks")
	}

	release, err := runningServices.ChannelManager.RegisterHTTPRoute(
		eventwebhook.RoutePrefix,
		runningServices.eventWebhookController,
	)
	if err != nil {
		return fmt.Errorf("register generic event webhook ingress: %w", err)
	}
	runningServices.eventWebhookRelease = release
	return nil
}

type stagedEventWebhookAdmission struct {
	reservation *eventwebhook.StagedGeneration
	enabled     bool
}

// stageEventWebhookAdmission completes every fallible route and controller
// check without making a candidate backend reachable to HTTP requests.
func stageEventWebhookAdmission(
	runningServices *services,
) (*stagedEventWebhookAdmission, error) {
	backend := (*eventwebhook.Backend)(nil)
	if runningServices != nil && runningServices.EventAutomation != nil {
		backend = runningServices.EventAutomation.webhookBackend
	}
	enabled := backend != nil && backend.ConnectorCount() > 0
	if enabled {
		if err := prepareEventWebhookRoute(runningServices); err != nil {
			return nil, err
		}
	}
	if runningServices == nil || runningServices.eventWebhookController == nil {
		if enabled {
			return nil, fmt.Errorf(
				"webhook admission controller is required for configured backend",
			)
		}
		return &stagedEventWebhookAdmission{}, nil
	}
	reservation, err := runningServices.eventWebhookController.Stage(backend)
	if err != nil {
		return nil, fmt.Errorf("stage generic event webhook ingress: %w", err)
	}
	return &stagedEventWebhookAdmission{
		reservation: reservation,
		enabled:     enabled,
	}, nil
}

func (staged *stagedEventWebhookAdmission) commit(
	runningServices *services,
) {
	if staged == nil || runningServices == nil {
		return
	}
	if staged.reservation != nil {
		runningServices.eventWebhookGeneration = staged.reservation.Generation()
		staged.reservation.Commit()
	} else {
		runningServices.eventWebhookGeneration = eventwebhook.Generation{}
	}
	if !staged.enabled {
		releaseEventWebhookRoute(runningServices)
	}
}

// activateEventWebhook commits the backend currently owned by EventAutomation.
// A configuration with no enabled generic connector removes the stable route
// only after the previous generation has been deactivated and drained.
func activateEventWebhook(runningServices *services) error {
	staged, err := stageEventWebhookAdmission(runningServices)
	if err != nil {
		return err
	}
	staged.commit(runningServices)
	return nil
}

// deactivateEventWebhook rejects new requests and waits for requests already
// admitted by the active generation to leave the durable Insert call.
func deactivateEventWebhook(ctx context.Context, runningServices *services) error {
	if runningServices == nil || runningServices.eventWebhookController == nil {
		return nil
	}
	if err := runningServices.eventWebhookController.Deactivate(
		ctx,
		runningServices.eventWebhookGeneration,
	); err != nil {
		return fmt.Errorf("drain generic event webhook ingress: %w", err)
	}
	runningServices.eventWebhookGeneration = eventwebhook.Generation{}
	return nil
}

func releaseEventWebhookRoute(runningServices *services) {
	if runningServices == nil || runningServices.eventWebhookRelease == nil {
		return
	}
	runningServices.eventWebhookRelease()
	runningServices.eventWebhookRelease = nil
}
