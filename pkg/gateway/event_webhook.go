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

// activateEventWebhook commits the backend currently owned by EventAutomation.
// A configuration with no enabled generic connector removes the stable route
// only after the previous generation has been deactivated and drained.
func activateEventWebhook(runningServices *services) error {
	if runningServices == nil {
		return nil
	}
	backend := (*eventwebhook.Backend)(nil)
	if runningServices.EventAutomation != nil {
		backend = runningServices.EventAutomation.webhookBackend
	}
	if backend == nil || backend.ConnectorCount() == 0 {
		releaseEventWebhookRoute(runningServices)
		return nil
	}
	if err := prepareEventWebhookRoute(runningServices); err != nil {
		return err
	}
	generation, err := runningServices.eventWebhookController.Activate(backend)
	if err != nil {
		return fmt.Errorf("activate generic event webhook ingress: %w", err)
	}
	runningServices.eventWebhookGeneration = generation
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
