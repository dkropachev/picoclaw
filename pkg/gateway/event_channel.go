package gateway

import (
	"context"
	"errors"
	"fmt"
	"sort"

	"github.com/sipeed/picoclaw/pkg/bus"
	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/eventing"
	eventchannel "github.com/sipeed/picoclaw/pkg/eventing/channelmessage"
)

// setupEventChannelController installs one process-stable admission seam on
// the existing message bus. Generation-specific stores are activated
// separately only after startup or reload has committed.
func setupEventChannelController(
	runningServices *services,
	messageBus *bus.MessageBus,
	cfg *config.Config,
) error {
	if runningServices == nil {
		return fmt.Errorf("gateway services are required for channel event admission")
	}
	if messageBus == nil {
		return fmt.Errorf("message bus is required for channel event admission")
	}
	runningServices.eventChannelBus = messageBus
	if len(eventChannelConnectorNames(cfg)) == 0 {
		// Preserve the original direct-to-queue path for installations that have
		// never enabled channel event admission. A later enabling reload installs
		// the stable controller before it publishes its candidate fence.
		return nil
	}
	return prepareEventChannelAdmission(runningServices, cfg)
}

// prepareEventChannelAdmission fences configured connectors before their
// channels can start publishing. Messages for a candidate-only connector wait
// for commit; unrelated and internal messages retain their existing path.
func prepareEventChannelAdmission(
	runningServices *services,
	cfg *config.Config,
) error {
	if runningServices == nil {
		return nil
	}
	connectors := eventChannelConnectorNames(cfg)
	if runningServices.eventChannelController == nil {
		if len(connectors) == 0 {
			return nil
		}
		runningServices.eventChannelController = eventchannel.NewController()
	}
	if len(connectors) == 0 &&
		!runningServices.eventChannelInstalled &&
		runningServices.eventChannelGeneration == (eventchannel.Generation{}) {
		return nil
	}
	if err := runningServices.eventChannelController.Prepare(connectors); err != nil {
		return fmt.Errorf("prepare channel event admission: %w", err)
	}
	if len(connectors) > 0 && !runningServices.eventChannelInstalled {
		// Prepare the controller before publishing it to the bus. A live channel
		// can publish while a disabled installation is being enabled on reload;
		// exposing an unprepared controller would let that message pass through
		// the otherwise-required candidate connector fence.
		if err := installEventChannelAdmission(runningServices); err != nil {
			cancelErr := runningServices.eventChannelController.CancelPreparation()
			if cancelErr != nil {
				cancelErr = fmt.Errorf(
					"cancel failed channel event admission installation: %w",
					cancelErr,
				)
			}
			return errors.Join(err, cancelErr)
		}
	}
	return nil
}

func installEventChannelAdmission(runningServices *services) error {
	if runningServices == nil || runningServices.eventChannelController == nil {
		return fmt.Errorf("channel event admission controller is required")
	}
	if runningServices.eventChannelInstalled {
		return nil
	}
	if runningServices.eventChannelBus == nil {
		return fmt.Errorf("message bus is required for channel event admission")
	}
	release, err := runningServices.eventChannelBus.RegisterInboundAdmission(
		runningServices.eventChannelController,
	)
	if err != nil {
		return fmt.Errorf("register channel event admission: %w", err)
	}
	runningServices.eventChannelRelease = release
	runningServices.eventChannelInstalled = true
	return nil
}

func uninstallEventChannelAdmission(runningServices *services) {
	if runningServices == nil ||
		!runningServices.eventChannelInstalled ||
		runningServices.eventChannelBus == nil {
		return
	}
	if runningServices.eventChannelRelease != nil {
		runningServices.eventChannelRelease()
		runningServices.eventChannelRelease = nil
	}
	runningServices.eventChannelInstalled = false
}

// cancelEventChannelPreparation restores the already-active generation after a
// reload fails before channel admission deactivation. It must not be used once
// a generation is retiring.
func cancelEventChannelPreparation(runningServices *services) error {
	if runningServices == nil || runningServices.eventChannelController == nil {
		return nil
	}
	if err := runningServices.eventChannelController.CancelPreparation(); err != nil {
		return fmt.Errorf("cancel channel event admission preparation: %w", err)
	}
	if runningServices.eventChannelGeneration == (eventchannel.Generation{}) {
		uninstallEventChannelAdmission(runningServices)
	}
	return nil
}

func eventChannelConnectorNames(cfg *config.Config) []string {
	adapters := config.EffectiveEventChannelAdapters(cfg)
	names := make([]string, 0, len(adapters))
	for name := range adapters {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func newEventChannelBackend(
	cfg *config.Config,
	store eventchannel.Inserter,
) (*eventchannel.Backend, error) {
	if cfg == nil || !cfg.Events.Ingress.Enabled || store == nil {
		return nil, nil
	}
	effective := config.EffectiveEventChannelAdapters(cfg)
	if len(effective) == 0 {
		return nil, nil
	}
	adapters := make(map[string]eventchannel.AdapterConfig, len(effective))
	for name, adapter := range effective {
		adapters[name] = eventchannel.AdapterConfig{
			Source:               eventchannel.Source(adapter.Source),
			Mode:                 eventchannel.Mode(adapter.Mode),
			ChannelType:          adapter.ChannelType,
			AllowUnverifiedEmail: adapter.AllowUnverifiedEmail,
		}
	}
	ingress := config.EffectiveEventIngressConfig(cfg, cfg.WorkspacePath())
	backend, err := eventchannel.NewBackend(eventchannel.BackendConfig{
		Store:           store,
		Adapters:        adapters,
		MaxPayloadBytes: ingress.MaxPayloadBytes,
	})
	if err != nil {
		return nil, fmt.Errorf("prepare channel event admission: %w", err)
	}
	return backend, nil
}

type stagedEventChannelAdmission struct {
	reservation *eventchannel.StagedGeneration
	enabled     bool
}

// stageEventChannelAdmission completes every fallible controller and bus-seam
// check without making a candidate backend accepting.
func stageEventChannelAdmission(
	runningServices *services,
) (*stagedEventChannelAdmission, error) {
	var backend *eventchannel.Backend
	if runningServices != nil && runningServices.EventAutomation != nil {
		backend = runningServices.EventAutomation.channelBackend
	}
	enabled := backend != nil && backend.ConnectorCount() > 0
	if runningServices == nil || runningServices.eventChannelController == nil {
		if enabled {
			return nil, fmt.Errorf(
				"channel event admission controller is required for configured backend",
			)
		}
		return &stagedEventChannelAdmission{}, nil
	}
	if enabled {
		if err := installEventChannelAdmission(runningServices); err != nil {
			return nil, err
		}
	}
	if !enabled {
		backend = nil
	}
	reservation, err := runningServices.eventChannelController.Stage(backend)
	if err != nil {
		return nil, fmt.Errorf("stage channel event admission: %w", err)
	}
	return &stagedEventChannelAdmission{
		reservation: reservation,
		enabled:     enabled,
	}, nil
}

func (staged *stagedEventChannelAdmission) commit(
	runningServices *services,
) {
	if staged == nil || staged.reservation == nil || runningServices == nil {
		return
	}
	runningServices.eventChannelGeneration = staged.reservation.Generation()
	staged.reservation.Commit()
	if !staged.enabled {
		uninstallEventChannelAdmission(runningServices)
	}
}

func (staged *stagedEventChannelAdmission) abort() {
	if staged != nil && staged.reservation != nil {
		staged.reservation.Abort()
	}
}

// activateEventChannel commits the backend owned by the current event
// automation service. A disabled configuration resolves waiting publishers as
// ordinary chat traffic.
func activateEventChannel(runningServices *services) error {
	staged, err := stageEventChannelAdmission(runningServices)
	if err != nil {
		return err
	}
	staged.commit(runningServices)
	return nil
}

// deactivateEventChannel blocks old and candidate connector identities, then
// drains calls already inside the durable Insert boundary.
func deactivateEventChannel(
	ctx context.Context,
	runningServices *services,
	nextCfg *config.Config,
) error {
	if runningServices == nil || runningServices.eventChannelController == nil {
		return nil
	}
	if err := runningServices.eventChannelController.Deactivate(
		ctx,
		runningServices.eventChannelGeneration,
		eventChannelConnectorNames(nextCfg),
	); err != nil {
		return fmt.Errorf("drain channel event admission: %w", err)
	}
	runningServices.eventChannelGeneration = eventchannel.Generation{}
	return nil
}

func closeEventChannelAdmission(
	ctx context.Context,
	runningServices *services,
) error {
	if runningServices == nil || runningServices.eventChannelController == nil {
		return nil
	}
	if err := runningServices.eventChannelController.Close(ctx); err != nil {
		return fmt.Errorf("close channel event admission: %w", err)
	}
	return nil
}

func activateEventAdmissions(runningServices *services) error {
	channelAdmission, err := stageEventChannelAdmission(runningServices)
	if err != nil {
		return err
	}
	webhookAdmission, err := stageEventWebhookAdmission(runningServices)
	if err != nil {
		channelAdmission.abort()
		return err
	}
	operator, err := stageEventOperator(runningServices)
	if err != nil {
		webhookAdmission.abort()
		channelAdmission.abort()
		return err
	}

	// All reservations have completed every fallible route, bus-seam, and
	// controller invariant check while remaining non-accepting. Reaching this
	// point is the irreversible aggregate commit decision: the serialized
	// lifecycle cannot return another activation error. Publication is
	// sequential, so concurrent traffic may transiently observe the webhook
	// generation before the channel generation while readiness remains false;
	// all use the same committed store and no commit can fail.
	operator.commit(runningServices)
	webhookAdmission.commit(runningServices)
	channelAdmission.commit(runningServices)
	return nil
}

var _ eventchannel.Inserter = (*eventing.Store)(nil)
