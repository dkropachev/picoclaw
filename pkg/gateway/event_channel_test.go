//go:build !mipsle && !netbsd && !(freebsd && arm)

package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sipeed/picoclaw/pkg/agent"
	"github.com/sipeed/picoclaw/pkg/bus"
	"github.com/sipeed/picoclaw/pkg/channels"
	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/eventing"
	eventchannel "github.com/sipeed/picoclaw/pkg/eventing/channelmessage"
	eventwebhook "github.com/sipeed/picoclaw/pkg/eventing/webhook"
	"github.com/sipeed/picoclaw/pkg/health"
	"github.com/sipeed/picoclaw/pkg/providers"
	"github.com/sipeed/picoclaw/pkg/workflows"
)

const (
	gatewayChannelConnector   = "support-chat"
	gatewayChannelWorkflowRef = "workflows/channel-native.yml"
)

type gatewayInboundAdmissionFunc func(
	context.Context,
	bus.InboundMessage,
) (bool, error)

func (function gatewayInboundAdmissionFunc) AdmitInbound(
	ctx context.Context,
	message bus.InboundMessage,
) (bool, error) {
	return function(ctx, message)
}

func configureGatewayEventChannel(
	t *testing.T,
	cfg *config.Config,
	mode string,
	secret string,
) {
	t.Helper()
	configureGatewayNamedEventChannel(
		t,
		cfg,
		gatewayChannelConnector,
		mode,
		secret,
	)
}

func configureGatewayNamedEventChannel(
	t *testing.T,
	cfg *config.Config,
	connector string,
	mode string,
	secret string,
) {
	t.Helper()
	channel := &config.Channel{
		Enabled: true,
		Type:    config.ChannelDeltaChat,
	}
	channel.SetName(connector)
	if err := channel.Decode(&config.DeltaChatSettings{
		Email:    "events@example.org",
		Password: *config.NewSecureString(secret),
	}); err != nil {
		t.Fatalf("Decode(DeltaChatSettings) error = %v", err)
	}
	cfg.Channels[connector] = channel
	cfg.Events.Ingress.Channels = map[string]config.ChannelEventIngressConfig{
		connector: {
			Enabled: true,
			Source:  config.EventChannelSourceEmail,
			Mode:    mode,
		},
	}
}

func gatewayChannelInbound(messageID string) bus.InboundContext {
	return bus.InboundContext{
		Account:                     "workspace-a",
		ChatID:                      "room-7",
		ChatType:                    "group",
		SenderID:                    "user-42",
		MessageID:                   messageID,
		ConversationName:            "Support Room",
		EventSenderVerified:         true,
		EventTransportAuthenticated: true,
	}
}

func waitForGatewayChannelPublish(t *testing.T, result <-chan error) {
	t.Helper()
	select {
	case err := <-result:
		if err != nil {
			t.Fatalf("HandleInboundContext() error = %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for channel publish")
	}
}

func assertGatewayChannelPipelineStable(
	t *testing.T,
	store *eventing.Store,
	runStore workflows.RunStore,
	eventID string,
	dispatchID string,
	runID string,
) {
	t.Helper()
	time.Sleep(3 * workflows.DefaultEventWorkerPollInterval)

	eventPage, err := store.List(context.Background(), eventing.EventFilter{
		Source:    config.EventChannelSourceEmail,
		Connector: gatewayChannelConnector,
		Limit:     10,
	})
	if err != nil {
		t.Fatalf("List(events) error = %v", err)
	}
	dispatchPage, err := store.ListDispatches(context.Background(), eventing.DispatchFilter{
		EventID: eventID,
		Limit:   10,
	})
	if err != nil {
		t.Fatalf("ListDispatches() error = %v", err)
	}
	runs, err := runStore.ListRuns(context.Background())
	if err != nil {
		t.Fatalf("ListRuns() error = %v", err)
	}
	if len(eventPage.Events) != 1 ||
		eventPage.Events[0].Envelope.ID != eventID ||
		len(dispatchPage.Dispatches) != 1 ||
		dispatchPage.Dispatches[0].ID != dispatchID ||
		len(runs) != 1 ||
		runs[0].ID != runID {
		t.Fatalf(
			"duplicate changed pipeline cardinality: events=%#v dispatches=%#v runs=%#v",
			eventPage.Events,
			dispatchPage.Dispatches,
			runs,
		)
	}
}

func TestEventChannelStartupFencePersistsBeforeMirrorDecision(t *testing.T) {
	tests := []struct {
		name      string
		mode      string
		wantQueue int
	}{
		{
			name:      "event only",
			mode:      config.EventChannelModeEventOnly,
			wantQueue: 0,
		},
		{
			name:      "mirror",
			mode:      config.EventChannelModeMirror,
			wantQueue: 1,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			workspace := t.TempDir()
			cfg := eventAutomationTestConfig(
				workspace,
				filepath.Join(workspace, "eventing", "events.db"),
				true,
				false,
			)
			channelSecret := "deltachat-secret-" + test.mode
			configureGatewayEventChannel(t, cfg, test.mode, channelSecret)

			service, err := setupEventAutomationService(context.Background(), cfg, nil)
			if err != nil {
				t.Fatalf("setupEventAutomationService() error = %v", err)
			}
			messageBus := bus.NewMessageBus()
			runningServices := &services{EventAutomation: service}
			if err = setupEventChannelController(runningServices, messageBus, cfg); err != nil {
				t.Fatalf("setupEventChannelController() error = %v", err)
			}
			t.Cleanup(func() {
				drainCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				_ = deactivateEventChannel(drainCtx, runningServices, nil)
				_ = closeEventAutomationService(drainCtx, &runningServices.EventAutomation)
				_ = closeEventChannelAdmission(drainCtx, runningServices)
				messageBus.Close()
			})

			base := channels.NewBaseChannel(
				gatewayChannelConnector,
				nil,
				messageBus,
				[]string{"user-42"},
			)
			published := make(chan error, 1)
			go func() {
				published <- base.HandleInboundContext(
					context.Background(),
					"room-7",
					"credential="+channelSecret,
					nil,
					gatewayChannelInbound("message-1"),
				)
			}()
			select {
			case publishErr := <-published:
				t.Fatalf(
					"configured message crossed startup fence before activation: %v",
					publishErr,
				)
			case <-time.After(25 * time.Millisecond):
			}

			if err = activateEventChannel(runningServices); err != nil {
				t.Fatalf("activateEventChannel() error = %v", err)
			}
			waitForGatewayChannelPublish(t, published)

			page, err := service.store.List(context.Background(), eventing.EventFilter{
				Source:    config.EventChannelSourceEmail,
				Connector: gatewayChannelConnector,
				Type:      eventchannel.EventTypeMessageReceived,
				Limit:     10,
			})
			if err != nil {
				t.Fatalf("List(channel events) error = %v", err)
			}
			if len(page.Events) != 1 {
				t.Fatalf("stored channel events = %d, want 1", len(page.Events))
			}
			stored := page.Events[0].Envelope
			if stored.DedupeKey == "message-1" {
				t.Fatal("provider message identity was persisted without hashing")
			}
			if bytes.Contains(stored.Payload, []byte(channelSecret)) ||
				!bytes.Contains(stored.Payload, []byte("[REDACTED]")) {
				t.Fatalf("stored payload was not channel-secret redacted: %s", stored.Payload)
			}
			if got := len(messageBus.InboundChan()); got != test.wantQueue {
				t.Fatalf("inbound queue depth = %d, want %d", got, test.wantQueue)
			}
			if test.wantQueue == 1 {
				queued := <-messageBus.InboundChan()
				if queued.Content != "credential="+channelSecret {
					t.Fatalf("mirrored chat content = %q", queued.Content)
				}
			}
		})
	}
}

func TestEventAdmissionStagingFailureKeepsChannelCandidateFenced(t *testing.T) {
	for _, test := range []struct {
		name                    string
		candidateWebhookEnabled bool
	}{
		{
			name:                    "enabled webhook invariant",
			candidateWebhookEnabled: true,
		},
		{
			name:                    "disabled webhook invariant",
			candidateWebhookEnabled: false,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			workspace := t.TempDir()
			cfg := eventAutomationTestConfig(
				workspace,
				filepath.Join(workspace, "eventing", "events.db"),
				true,
				false,
			)
			configureGatewayEventChannel(
				t,
				cfg,
				config.EventChannelModeEventOnly,
				"two-phase-channel-secret",
			)
			if test.candidateWebhookEnabled {
				configureGatewayWebhook(cfg, gatewayWebhookSecret(0x71))
			}
			service, err := setupEventAutomationService(
				context.Background(),
				cfg,
				nil,
			)
			if err != nil {
				t.Fatalf("setupEventAutomationService() error = %v", err)
			}

			blockingWebhookBackend := service.webhookBackend
			if blockingWebhookBackend == nil {
				ingress := config.EffectiveEventIngressConfig(
					cfg,
					cfg.WorkspacePath(),
				)
				blockingWebhookBackend, err = eventwebhook.NewBackend(
					eventwebhook.BackendConfig{
						Store: service.store,
						ConnectorSecrets: map[string]string{
							"blocking": gatewayWebhookSecret(0x72),
						},
						MaxPayloadBytes: ingress.MaxPayloadBytes,
					},
				)
				if err != nil {
					t.Fatalf("NewBackend(blocking webhook) error = %v", err)
				}
			}
			webhookController := eventwebhook.NewController()
			blockingGeneration, err := webhookController.Activate(
				blockingWebhookBackend,
			)
			if err != nil {
				t.Fatalf("Activate(blocking webhook) error = %v", err)
			}

			messageBus := bus.NewMessageBus()
			runningServices := &services{
				EventAutomation:        service,
				eventWebhookController: webhookController,
				eventWebhookGeneration: blockingGeneration,
				// The route is already prepared for this focused controller
				// invariant fault; no listener is required.
				eventWebhookRelease: func() {},
			}
			if err = setupEventChannelController(
				runningServices,
				messageBus,
				cfg,
			); err != nil {
				t.Fatalf("setupEventChannelController() error = %v", err)
			}
			t.Cleanup(func() {
				drainCtx, cancel := context.WithTimeout(
					context.Background(),
					5*time.Second,
				)
				defer cancel()
				_ = cancelEventChannelPreparation(runningServices)
				_ = deactivateEventWebhook(drainCtx, runningServices)
				releaseEventWebhookRoute(runningServices)
				_ = closeEventChannelAdmission(drainCtx, runningServices)
				_ = closeEventAutomationService(
					drainCtx,
					&runningServices.EventAutomation,
				)
				messageBus.Close()
			})

			published := make(chan error, 1)
			go func() {
				inboundContext := gatewayChannelInbound("two-phase-message")
				inboundContext.Channel = gatewayChannelConnector
				published <- messageBus.PublishInbound(
					context.Background(),
					bus.InboundMessage{
						Context:       inboundContext,
						Content:       "candidate event",
						ChannelOrigin: true,
					},
				)
			}()
			select {
			case publishErr := <-published:
				t.Fatalf(
					"configured message crossed preparation before staging: %v",
					publishErr,
				)
			case <-time.After(25 * time.Millisecond):
			}

			err = activateEventAdmissions(runningServices)
			if !errors.Is(err, eventwebhook.ErrActiveGeneration) {
				t.Fatalf(
					"activateEventAdmissions() error = %v, want %v",
					err,
					eventwebhook.ErrActiveGeneration,
				)
			}
			if runningServices.eventChannelGeneration != (eventchannel.Generation{}) {
				t.Fatal("failed aggregate staging published a channel generation")
			}
			select {
			case publishErr := <-published:
				t.Fatalf(
					"configured message crossed fence after webhook stage failure: %v",
					publishErr,
				)
			case <-time.After(25 * time.Millisecond):
			}

			page, listErr := service.store.List(
				context.Background(),
				eventing.EventFilter{
					Source:    config.EventChannelSourceEmail,
					Connector: gatewayChannelConnector,
					Type:      eventchannel.EventTypeMessageReceived,
					Limit:     10,
				},
			)
			if listErr != nil {
				t.Fatalf("List(channel events) error = %v", listErr)
			}
			if len(page.Events) != 0 {
				t.Fatalf(
					"failed aggregate staging inserted %d candidate channel events",
					len(page.Events),
				)
			}

			if err = cancelEventChannelPreparation(runningServices); err != nil {
				t.Fatalf("cancelEventChannelPreparation() error = %v", err)
			}
			waitForGatewayChannelPublish(t, published)
			if got := len(messageBus.InboundChan()); got != 1 {
				t.Fatalf("rollback direct queue depth = %d, want 1", got)
			}
		})
	}
}

func TestDisabledEventChannelsLeaveExistingBusAdmissionUntouched(t *testing.T) {
	cfg := eventAutomationTestConfig(
		filepath.Join(t.TempDir(), "workspace"),
		filepath.Join(t.TempDir(), "events.db"),
		false,
		false,
	)
	messageBus := bus.NewMessageBus()
	defer messageBus.Close()

	calls := 0
	messageBus.SetInboundAdmission(gatewayInboundAdmissionFunc(
		func(context.Context, bus.InboundMessage) (bool, error) {
			calls++
			return false, nil
		},
	))
	runningServices := &services{}
	if err := setupEventChannelController(runningServices, messageBus, cfg); err != nil {
		t.Fatalf("setupEventChannelController() error = %v", err)
	}
	if runningServices.eventChannelController != nil ||
		runningServices.eventChannelInstalled {
		t.Fatal("disabled channel ingress installed an event admission hook")
	}

	err := messageBus.PublishInbound(context.Background(), bus.InboundMessage{
		Context: bus.InboundContext{
			Channel:   "existing",
			ChatID:    "room",
			SenderID:  "sender",
			MessageID: "message",
		},
		ChannelOrigin: true,
	})
	if err != nil {
		t.Fatalf("PublishInbound() error = %v", err)
	}
	if calls != 1 {
		t.Fatalf("pre-existing admission calls = %d, want 1", calls)
	}
	if got := len(messageBus.InboundChan()); got != 0 {
		t.Fatalf("pre-existing admission no longer consumed message; queue depth = %d", got)
	}
}

func TestEventChannelAdmissionRefusesToReplaceExistingBusAdmission(t *testing.T) {
	workspace := t.TempDir()
	cfg := eventAutomationTestConfig(
		workspace,
		filepath.Join(workspace, "events.db"),
		true,
		false,
	)
	configureGatewayEventChannel(
		t,
		cfg,
		config.EventChannelModeEventOnly,
		"collision-channel-secret",
	)

	messageBus := bus.NewMessageBus()
	existingCalls := 0
	messageBus.SetInboundAdmission(gatewayInboundAdmissionFunc(
		func(context.Context, bus.InboundMessage) (bool, error) {
			existingCalls++
			return false, nil
		},
	))
	runningServices := &services{}
	t.Cleanup(func() {
		closeCtx, cancelClose := context.WithTimeout(context.Background(), time.Second)
		defer cancelClose()
		_ = closeEventChannelAdmission(closeCtx, runningServices)
		uninstallEventChannelAdmission(runningServices)
		messageBus.Close()
	})
	err := setupEventChannelController(runningServices, messageBus, cfg)
	if !errors.Is(err, bus.ErrInboundAdmissionRegistered) {
		t.Fatalf(
			"setupEventChannelController() error = %v, want %v",
			err,
			bus.ErrInboundAdmissionRegistered,
		)
	}
	if runningServices.eventChannelInstalled ||
		runningServices.eventChannelRelease != nil {
		t.Fatal("failed event admission collision claimed the bus seam")
	}

	message := bus.InboundMessage{
		Context: bus.InboundContext{
			Channel:   gatewayChannelConnector,
			ChatID:    "room-7",
			SenderID:  "user-42",
			MessageID: "collision-message",
		},
		ChannelOrigin: true,
	}
	if err = messageBus.PublishInbound(context.Background(), message); err != nil {
		t.Fatalf("PublishInbound(existing) error = %v", err)
	}
	if existingCalls != 1 {
		t.Fatalf("pre-existing admission calls = %d, want 1", existingCalls)
	}

	// Once the conflicting owner leaves, the same controller can recover and
	// is already fenced when it becomes observable through the bus.
	messageBus.SetInboundAdmission(nil)
	if err = prepareEventChannelAdmission(runningServices, cfg); err != nil {
		t.Fatalf("prepareEventChannelAdmission(retry) error = %v", err)
	}
	publishCtx, cancelPublish := context.WithTimeout(
		context.Background(),
		25*time.Millisecond,
	)
	defer cancelPublish()
	if err = messageBus.PublishInbound(publishCtx, message); !errors.Is(
		err,
		context.DeadlineExceeded,
	) {
		t.Fatalf(
			"PublishInbound(prepared fence) error = %v, want deadline",
			err,
		)
	}

	closeCtx, cancelClose := context.WithTimeout(context.Background(), time.Second)
	defer cancelClose()
	if err = closeEventChannelAdmission(closeCtx, runningServices); err != nil {
		t.Fatalf("closeEventChannelAdmission() error = %v", err)
	}
	uninstallEventChannelAdmission(runningServices)
}

func TestEventChannelAdmissionInstallsOnEnableAndDetachesOnDisabledCommit(t *testing.T) {
	workspace := t.TempDir()
	disabledCfg := eventAutomationTestConfig(
		workspace,
		filepath.Join(workspace, "disabled.db"),
		false,
		false,
	)
	enabledCfg := eventAutomationTestConfig(
		workspace,
		filepath.Join(workspace, "enabled.db"),
		true,
		false,
	)
	configureGatewayEventChannel(
		t,
		enabledCfg,
		config.EventChannelModeEventOnly,
		"lazy-install-channel-secret",
	)

	messageBus := bus.NewMessageBus()
	runningServices := &services{}
	if err := setupEventChannelController(
		runningServices,
		messageBus,
		disabledCfg,
	); err != nil {
		t.Fatalf("disabled setupEventChannelController() error = %v", err)
	}
	if runningServices.eventChannelController != nil ||
		runningServices.eventChannelInstalled {
		t.Fatal("disabled startup created channel event admission")
	}

	service, err := setupEventAutomationService(
		context.Background(),
		enabledCfg,
		nil,
	)
	if err != nil {
		t.Fatalf("setupEventAutomationService(enabled) error = %v", err)
	}
	runningServices.EventAutomation = service
	if err = prepareEventChannelAdmission(runningServices, enabledCfg); err != nil {
		t.Fatalf("prepareEventChannelAdmission(enabled) error = %v", err)
	}
	if runningServices.eventChannelController == nil ||
		!runningServices.eventChannelInstalled {
		t.Fatal("enabling reload did not install channel event admission")
	}

	published := make(chan error, 1)
	go func() {
		published <- messageBus.PublishInbound(
			context.Background(),
			bus.InboundMessage{
				Context: bus.InboundContext{
					Channel:                     gatewayChannelConnector,
					ChatID:                      "room-7",
					SenderID:                    "user-42",
					MessageID:                   "enabled-message",
					EventSenderVerified:         true,
					EventTransportAuthenticated: true,
				},
				Content:       "enabled event",
				ChannelOrigin: true,
			},
		)
	}()
	select {
	case publishErr := <-published:
		t.Fatalf("message crossed enabling reload fence before commit: %v", publishErr)
	case <-time.After(25 * time.Millisecond):
	}
	if err = activateEventChannel(runningServices); err != nil {
		t.Fatalf("activateEventChannel(enabled) error = %v", err)
	}
	waitForGatewayChannelPublish(t, published)
	if got := len(messageBus.InboundChan()); got != 0 {
		t.Fatalf("event-only enabling generation queued %d turns, want 0", got)
	}

	if err = prepareEventChannelAdmission(runningServices, disabledCfg); err != nil {
		t.Fatalf("prepareEventChannelAdmission(disabled) error = %v", err)
	}
	drainCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err = deactivateEventChannel(drainCtx, runningServices, disabledCfg); err != nil {
		t.Fatalf("deactivateEventChannel(disabled) error = %v", err)
	}
	if err = closeEventAutomationService(
		drainCtx,
		&runningServices.EventAutomation,
	); err != nil {
		t.Fatalf("closeEventAutomationService() error = %v", err)
	}
	if err = activateEventChannel(runningServices); err != nil {
		t.Fatalf("activateEventChannel(disabled) error = %v", err)
	}
	if runningServices.eventChannelInstalled {
		t.Fatal("disabled commit left channel event admission installed")
	}

	if err = messageBus.PublishInbound(
		context.Background(),
		bus.InboundMessage{
			Context: bus.InboundContext{
				Channel:   gatewayChannelConnector,
				ChatID:    "room-7",
				SenderID:  "user-42",
				MessageID: "disabled-message",
			},
			Content:       "ordinary chat",
			ChannelOrigin: true,
		},
	); err != nil {
		t.Fatalf("PublishInbound(disabled) error = %v", err)
	}
	if got := <-messageBus.InboundChan(); got.Content != "ordinary chat" {
		t.Fatalf("disabled commit queued content = %q, want ordinary chat", got.Content)
	}

	if err = closeEventChannelAdmission(drainCtx, runningServices); err != nil {
		t.Fatalf("closeEventChannelAdmission() error = %v", err)
	}
	messageBus.Close()
}

func writeGatewayChannelNativeWorkflow(
	t *testing.T,
	workspace string,
	definitionsDir string,
) {
	t.Helper()
	path := filepath.Join(
		workspace,
		filepath.FromSlash(definitionsDir),
		"channel-native.yml",
	)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("MkdirAll(workflow definitions) error = %v", err)
	}
	contents := `name: Channel native integration
on:
  event:
    sources: [email]
    connectors: [support-chat]
    types: [message.received]
jobs:
  main:
    runs-on: picoclaw
    steps:
      - id: remember
        uses: function/workflow.state
        with:
          action: set
          namespace: channel-integration
          key: handled
          value: complete
`
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		t.Fatalf("WriteFile(workflow) error = %v", err)
	}
}

func TestEventChannelMessageRunsOneNativeEventWorkflow(t *testing.T) {
	workspace := t.TempDir()
	cfg := eventAutomationTestConfig(
		workspace,
		filepath.Join(workspace, "eventing", "events.db"),
		true,
		true,
	)
	configureGatewayEventChannel(
		t,
		cfg,
		config.EventChannelModeEventOnly,
		"workflow-channel-secret",
	)
	definitionsDir := cfg.Workflows.EffectiveDefinitionsDir()
	writeGatewayChannelNativeWorkflow(t, workspace, definitionsDir)

	runStore := workflows.NewFileRunStore(workspace)
	executor := &workflows.Executor{
		WorkspaceDir:   workspace,
		DefinitionsDir: definitionsDir,
		Store:          runStore,
		DefaultTimeout: 2 * time.Second,
	}
	service, err := newEventAutomationService(
		context.Background(),
		cfg,
		executor,
		nil,
		nil,
	)
	if err != nil {
		t.Fatalf("newEventAutomationService() error = %v", err)
	}
	controller := eventchannel.NewController()
	if err = controller.Prepare([]string{gatewayChannelConnector}); err != nil {
		t.Fatalf("Prepare() error = %v", err)
	}
	generation, err := controller.Activate(service.channelBackend)
	if err != nil {
		t.Fatalf("Activate() error = %v", err)
	}
	messageBus := bus.NewMessageBus()
	messageBus.SetInboundAdmission(controller)
	t.Cleanup(func() {
		drainCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = controller.Deactivate(drainCtx, generation, nil)
		_ = controller.Close(drainCtx)
		_ = service.Close(drainCtx)
		messageBus.Close()
	})

	base := channels.NewBaseChannel(
		gatewayChannelConnector,
		nil,
		messageBus,
		[]string{"user-42"},
	)
	for attempt := 0; attempt < 2; attempt++ {
		if handleErr := base.HandleInboundContext(
			context.Background(),
			"room-7",
			"run the workflow",
			nil,
			gatewayChannelInbound("stable-message-1"),
		); handleErr != nil {
			t.Fatalf(
				"HandleInboundContext(attempt %d) error = %v",
				attempt+1,
				handleErr,
			)
		}
	}
	if got := len(messageBus.InboundChan()); got != 0 {
		t.Fatalf("event-only message queued %d agent turns, want 0", got)
	}

	eventPage, err := service.store.List(context.Background(), eventing.EventFilter{
		Source:    config.EventChannelSourceEmail,
		Connector: gatewayChannelConnector,
		Type:      eventchannel.EventTypeMessageReceived,
		Limit:     10,
	})
	if err != nil {
		t.Fatalf("List(channel events) error = %v", err)
	}
	if len(eventPage.Events) != 1 {
		t.Fatalf("deduplicated channel events = %d, want 1", len(eventPage.Events))
	}
	eventID := eventPage.Events[0].Envelope.ID
	dispatch, run := waitForGatewayWebhookWorkflow(
		t,
		service.store,
		runStore,
		eventID,
	)
	if dispatch.WorkflowRef != gatewayChannelWorkflowRef || dispatch.RunID != run.ID {
		t.Fatalf("dispatch/run identity mismatch: dispatch=%#v run=%#v", dispatch, run)
	}
	step, exists := run.Steps["main/remember"]
	if !exists ||
		step.Status != workflows.RunStatusSucceeded ||
		step.Outputs["updated"] != true {
		t.Fatalf("native workflow.state step = %#v, want successful update", step)
	}
	assertGatewayChannelPipelineStable(
		t,
		service.store,
		runStore,
		eventID,
		dispatch.ID,
		run.ID,
	)
}

func TestStopRuntimeProducersRetriesChannelDrainBeforeStoreClose(t *testing.T) {
	workspace := t.TempDir()
	cfg := eventAutomationTestConfig(
		workspace,
		filepath.Join(workspace, "eventing", "events.db"),
		true,
		false,
	)
	configureGatewayEventChannel(
		t,
		cfg,
		config.EventChannelModeEventOnly,
		"drain-channel-secret",
	)
	service, err := setupEventAutomationService(context.Background(), cfg, nil)
	if err != nil {
		t.Fatalf("setupEventAutomationService() error = %v", err)
	}
	blocker := &gatewayBlockingInserter{
		entered: make(chan struct{}),
		release: make(chan struct{}),
	}
	backend, err := eventchannel.NewBackend(eventchannel.BackendConfig{
		Store: blocker,
		Adapters: map[string]eventchannel.AdapterConfig{
			gatewayChannelConnector: {
				Source:      eventchannel.SourceEmail,
				Mode:        eventchannel.ModeEventOnly,
				ChannelType: config.ChannelDeltaChat,
			},
		},
		MaxPayloadBytes: 1 << 20,
	})
	if err != nil {
		t.Fatalf("NewBackend() error = %v", err)
	}
	controller := eventchannel.NewController()
	if err = controller.Prepare([]string{gatewayChannelConnector}); err != nil {
		t.Fatalf("Prepare() error = %v", err)
	}
	generation, err := controller.Activate(backend)
	if err != nil {
		t.Fatalf("Activate() error = %v", err)
	}
	messageBus := bus.NewMessageBus()
	messageBus.SetInboundAdmission(controller)
	runningServices := &services{
		EventAutomation:        service,
		eventChannelController: controller,
		eventChannelGeneration: generation,
	}
	releaseBlocker := func() {
		select {
		case <-blocker.release:
		default:
			close(blocker.release)
		}
	}
	t.Cleanup(func() {
		releaseBlocker()
		_ = stopRuntimeProducers(runningServices, 5*time.Second)
		_ = closeEventChannelAdmission(context.Background(), runningServices)
		messageBus.Close()
	})

	publishDone := make(chan error, 1)
	go func() {
		publishDone <- messageBus.PublishInbound(
			context.Background(),
			bus.InboundMessage{
				Context: bus.InboundContext{
					Channel:                     gatewayChannelConnector,
					ChatID:                      "room-7",
					SenderID:                    "user-42",
					MessageID:                   "blocked-message",
					EventSenderVerified:         true,
					EventTransportAuthenticated: true,
				},
				Content:       "blocked",
				ChannelOrigin: true,
			},
		)
	}()
	select {
	case <-blocker.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("channel insert did not enter blocking store")
	}

	err = stopRuntimeProducers(runningServices, 25*time.Millisecond)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("first stopRuntimeProducers() error = %v, want deadline", err)
	}
	if runningServices.EventAutomation != service {
		t.Fatal("timed-out channel drain released the event service")
	}
	inserted, err := service.store.Insert(
		context.Background(),
		eventAutomationTestEnvelope("channel-store-stays-open"),
	)
	if err != nil {
		t.Fatalf("store was closed after timed-out admission drain: %v", err)
	}

	releaseBlocker()
	select {
	case publishErr := <-publishDone:
		if publishErr != nil {
			t.Fatalf("blocked channel publish error = %v", publishErr)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("admitted channel message did not drain")
	}
	if err := stopRuntimeProducers(runningServices, 5*time.Second); err != nil {
		t.Fatalf("retry stopRuntimeProducers() error = %v", err)
	}
	if runningServices.EventAutomation != nil {
		t.Fatal("successful retry did not release event service")
	}
	if _, err := service.store.Get(
		context.Background(),
		inserted.Event.Envelope.ID,
	); !errors.Is(err, eventing.ErrClosed) {
		t.Fatalf("Get() after successful retry error = %v, want ErrClosed", err)
	}
}

func TestEventChannelPayloadContainsOnlySafeJSONFields(t *testing.T) {
	workspace := t.TempDir()
	cfg := eventAutomationTestConfig(
		workspace,
		filepath.Join(workspace, "eventing", "events.db"),
		true,
		false,
	)
	configureGatewayEventChannel(
		t,
		cfg,
		config.EventChannelModeEventOnly,
		"safe-fields-secret",
	)
	service, err := setupEventAutomationService(context.Background(), cfg, nil)
	if err != nil {
		t.Fatalf("setupEventAutomationService() error = %v", err)
	}
	defer func() { _ = service.Close(context.Background()) }()

	message := bus.InboundMessage{
		Context: bus.InboundContext{
			Channel:   gatewayChannelConnector,
			Account:   "workspace-a",
			ChatID:    "room-7",
			SenderID:  "user-42",
			MessageID: "local-42",
			Raw: map[string]string{
				"private_path": "/secret/account/blob",
			},
		},
		Content:                     "hello",
		Media:                       []string{"media://private-reference"},
		MediaScope:                  "private/scope",
		ChannelOrigin:               true,
		EventDedupeID:               "local:private-provider-id",
		EventSenderVerified:         true,
		EventTransportAuthenticated: true,
		Attachments: []bus.InboundAttachment{{
			Filename:    "report.pdf",
			ContentType: "application/pdf",
			Kind:        "file",
			SizeBytes:   123,
		}},
	}
	if _, err = service.channelBackend.AdmitInbound(context.Background(), message); err != nil {
		t.Fatalf("AdmitInbound() error = %v", err)
	}
	page, err := service.store.List(context.Background(), eventing.EventFilter{
		Connector: gatewayChannelConnector,
		Limit:     1,
	})
	if err != nil || len(page.Events) != 1 {
		t.Fatalf("List() events = %#v, error = %v", page.Events, err)
	}
	var payload map[string]any
	if err = json.Unmarshal(page.Events[0].Envelope.Payload, &payload); err != nil {
		t.Fatalf("Unmarshal(payload) error = %v", err)
	}
	encoded := string(page.Events[0].Envelope.Payload)
	for _, forbidden := range []string{
		"/secret/account/blob",
		"media://private-reference",
		"private/scope",
		"private-provider-id",
		"private_path",
	} {
		if strings.Contains(encoded, forbidden) {
			t.Fatalf("payload contains private field %q: %s", forbidden, encoded)
		}
	}
	if payload["message_id"] != "local-42" {
		t.Fatalf("safe provider message ID = %#v", payload["message_id"])
	}
}

func TestSuccessfulReloadRotatesEventChannelModeAndStoreGeneration(t *testing.T) {
	oldWorkspace := t.TempDir()
	oldCfg := eventAutomationTestConfig(
		oldWorkspace,
		filepath.Join(oldWorkspace, "eventing", "events.db"),
		true,
		false,
	)
	configureGatewayEventChannel(
		t,
		oldCfg,
		config.EventChannelModeMirror,
		"old-channel-secret",
	)
	newWorkspace := t.TempDir()
	newCfg := eventAutomationTestConfig(
		newWorkspace,
		filepath.Join(newWorkspace, "eventing", "events.db"),
		true,
		false,
	)
	configureGatewayEventChannel(
		t,
		newCfg,
		config.EventChannelModeEventOnly,
		"new-channel-secret",
	)

	messageBus := bus.NewMessageBus()
	oldProvider := &orderedShutdownProvider{closed: make(chan struct{})}
	agentLoop := agent.NewAgentLoop(oldCfg, messageBus, oldProvider)
	oldService, err := setupEventAutomationService(
		context.Background(),
		oldCfg,
		agentLoop,
	)
	if err != nil {
		t.Fatalf("setupEventAutomationService(old) error = %v", err)
	}
	controller := eventchannel.NewController()
	if err = controller.Prepare(eventChannelConnectorNames(oldCfg)); err != nil {
		t.Fatalf("Prepare(old) error = %v", err)
	}
	oldGeneration, err := controller.Activate(oldService.channelBackend)
	if err != nil {
		t.Fatalf("Activate(old) error = %v", err)
	}
	eventChannelRelease, err := messageBus.RegisterInboundAdmission(controller)
	if err != nil {
		t.Fatalf("RegisterInboundAdmission(old) error = %v", err)
	}
	healthServer := health.NewServer("127.0.0.1", 1, "")
	healthServer.SetReady(true)
	runningServices := &services{
		EventAutomation:        oldService,
		HealthServer:           healthServer,
		eventChannelBus:        messageBus,
		eventChannelController: controller,
		eventChannelGeneration: oldGeneration,
		eventChannelInstalled:  true,
		eventChannelRelease:    eventChannelRelease,
	}
	installTestEventOperatorGeneration(t, runningServices)
	t.Cleanup(func() {
		_ = stopRuntimeProducers(runningServices, 5*time.Second)
		_ = closeEventChannelAdmission(context.Background(), runningServices)
		messageBus.Close()
		agentLoop.Close()
		oldProvider.Close()
	})

	base := channels.NewBaseChannel(
		gatewayChannelConnector,
		nil,
		messageBus,
		[]string{"user-42"},
	)
	if err = base.HandleInboundContext(
		context.Background(),
		"room-7",
		"old generation",
		nil,
		gatewayChannelInbound("old-message"),
	); err != nil {
		t.Fatalf("old HandleInboundContext() error = %v", err)
	}
	if got := (<-messageBus.InboundChan()).Content; got != "old generation" {
		t.Fatalf("old mirrored content = %q", got)
	}
	oldPage, err := oldService.store.List(context.Background(), eventing.EventFilter{
		Connector: gatewayChannelConnector,
		Limit:     10,
	})
	if err != nil || len(oldPage.Events) != 1 {
		t.Fatalf("old store events = %#v, error = %v", oldPage.Events, err)
	}
	oldEventID := oldPage.Events[0].Envelope.ID

	serviceOps := configReloadServiceOps{
		stop: stopAndCleanupServices,
		restart: func(
			_ context.Context,
			currentLoop *agent.AgentLoop,
			currentServices *services,
			_ *bus.MessageBus,
		) error {
			service, setupErr := setupEventAutomationService(
				context.Background(),
				currentLoop.GetConfig(),
				currentLoop,
			)
			if setupErr != nil {
				return setupErr
			}
			currentServices.EventAutomation = service
			return nil
		},
	}
	providerRef := providers.LLMProvider(oldProvider)
	err = handleConfigReloadWithServiceOps(
		context.Background(),
		agentLoop,
		newCfg,
		&providerRef,
		runningServices,
		messageBus,
		true,
		false,
		serviceOps,
	)
	if err != nil {
		t.Fatalf("successful reload error = %v", err)
	}
	if agentLoop.GetConfig() != newCfg || providerRef == oldProvider {
		t.Fatal("successful reload did not commit candidate config/provider")
	}
	if !healthServer.IsReady() {
		t.Fatal("successful reload did not restore readiness")
	}
	if _, err = oldService.store.Get(context.Background(), oldEventID); !errors.Is(err, eventing.ErrClosed) {
		t.Fatalf("old store after reload error = %v, want ErrClosed", err)
	}

	if err = base.HandleInboundContext(
		context.Background(),
		"room-7",
		"new generation",
		nil,
		gatewayChannelInbound("new-message"),
	); err != nil {
		t.Fatalf("new HandleInboundContext() error = %v", err)
	}
	if got := len(messageBus.InboundChan()); got != 0 {
		t.Fatalf("new event-only generation queued %d turns, want 0", got)
	}
	newPage, err := runningServices.EventAutomation.store.List(
		context.Background(),
		eventing.EventFilter{
			Connector: gatewayChannelConnector,
			Limit:     10,
		},
	)
	if err != nil || len(newPage.Events) != 1 {
		t.Fatalf("new store events = %#v, error = %v", newPage.Events, err)
	}
	var payload map[string]any
	if err = json.Unmarshal(newPage.Events[0].Envelope.Payload, &payload); err != nil {
		t.Fatalf("Unmarshal(new payload) error = %v", err)
	}
	if payload["text"] != "new generation" {
		t.Fatalf("new store payload = %#v", payload)
	}
}

func TestFailedCandidateReloadRestoresPreparedEventChannelGeneration(t *testing.T) {
	oldWorkspace := t.TempDir()
	oldCfg := eventAutomationTestConfig(
		oldWorkspace,
		filepath.Join(oldWorkspace, "eventing", "events.db"),
		true,
		false,
	)
	configureGatewayEventChannel(
		t,
		oldCfg,
		config.EventChannelModeEventOnly,
		"rollback-old-secret",
	)
	newConnector := "triage-chat"
	newWorkspace := t.TempDir()
	newCfg := eventAutomationTestConfig(
		newWorkspace,
		filepath.Join(newWorkspace, "eventing", "events.db"),
		true,
		false,
	)
	configureGatewayNamedEventChannel(
		t,
		newCfg,
		newConnector,
		config.EventChannelModeEventOnly,
		"rollback-new-secret",
	)

	messageBus := bus.NewMessageBus()
	oldProvider := &orderedShutdownProvider{closed: make(chan struct{})}
	agentLoop := agent.NewAgentLoop(oldCfg, messageBus, oldProvider)
	oldService, err := setupEventAutomationService(
		context.Background(),
		oldCfg,
		agentLoop,
	)
	if err != nil {
		t.Fatalf("setupEventAutomationService(old) error = %v", err)
	}
	controller := eventchannel.NewController()
	if err = controller.Prepare(eventChannelConnectorNames(oldCfg)); err != nil {
		t.Fatalf("Prepare(old) error = %v", err)
	}
	oldGeneration, err := controller.Activate(oldService.channelBackend)
	if err != nil {
		t.Fatalf("Activate(old) error = %v", err)
	}
	eventChannelRelease, err := messageBus.RegisterInboundAdmission(controller)
	if err != nil {
		t.Fatalf("RegisterInboundAdmission(old) error = %v", err)
	}
	healthServer := health.NewServer("127.0.0.1", 1, "")
	healthServer.SetReady(true)
	runningServices := &services{
		EventAutomation:        oldService,
		HealthServer:           healthServer,
		eventChannelBus:        messageBus,
		eventChannelController: controller,
		eventChannelGeneration: oldGeneration,
		eventChannelInstalled:  true,
		eventChannelRelease:    eventChannelRelease,
	}
	installTestEventOperatorGeneration(t, runningServices)
	t.Cleanup(func() {
		_ = stopRuntimeProducers(runningServices, 5*time.Second)
		_ = closeEventChannelAdmission(context.Background(), runningServices)
		messageBus.Close()
		agentLoop.Close()
		oldProvider.Close()
	})

	forcedRestartErr := errors.New("forced channel candidate restart failure")
	candidateObservedFenced := false
	serviceOps := configReloadServiceOps{
		stop: stopAndCleanupServices,
		restart: func(
			_ context.Context,
			currentLoop *agent.AgentLoop,
			currentServices *services,
			_ *bus.MessageBus,
		) error {
			service, setupErr := setupEventAutomationService(
				context.Background(),
				currentLoop.GetConfig(),
				currentLoop,
			)
			if setupErr != nil {
				return setupErr
			}
			currentServices.EventAutomation = service
			if currentLoop.GetConfig() != newCfg {
				return nil
			}
			waitCtx, cancel := context.WithTimeout(context.Background(), 25*time.Millisecond)
			defer cancel()
			_, admissionErr := controller.AdmitInbound(
				waitCtx,
				bus.InboundMessage{
					Context: bus.InboundContext{
						Channel:   newConnector,
						ChatID:    "triage-room",
						SenderID:  "user-7",
						MessageID: "candidate-message",
					},
					ChannelOrigin: true,
				},
			)
			candidateObservedFenced = errors.Is(admissionErr, context.DeadlineExceeded)
			return forcedRestartErr
		},
	}
	providerRef := providers.LLMProvider(oldProvider)
	err = handleConfigReloadWithServiceOps(
		context.Background(),
		agentLoop,
		newCfg,
		&providerRef,
		runningServices,
		messageBus,
		true,
		false,
		serviceOps,
	)
	if !errors.Is(err, forcedRestartErr) {
		t.Fatalf("reload error = %v, want forced candidate failure", err)
	}
	if !candidateObservedFenced {
		t.Fatal("candidate channel message was not fenced before commit")
	}
	if agentLoop.GetConfig() != oldCfg || providerRef != oldProvider {
		t.Fatal("failed reload did not restore old config/provider")
	}
	if !healthServer.IsReady() {
		t.Fatal("failed reload did not restore readiness")
	}

	base := channels.NewBaseChannel(
		gatewayChannelConnector,
		nil,
		messageBus,
		[]string{"user-42"},
	)
	if err = base.HandleInboundContext(
		context.Background(),
		"room-7",
		"restored generation",
		nil,
		gatewayChannelInbound("restored-message"),
	); err != nil {
		t.Fatalf("restored HandleInboundContext() error = %v", err)
	}
	if got := len(messageBus.InboundChan()); got != 0 {
		t.Fatalf("restored event-only generation queued %d turns, want 0", got)
	}
	page, err := runningServices.EventAutomation.store.List(
		context.Background(),
		eventing.EventFilter{
			Connector: gatewayChannelConnector,
			Limit:     10,
		},
	)
	if err != nil || len(page.Events) != 1 {
		t.Fatalf("restored store events = %#v, error = %v", page.Events, err)
	}
}
