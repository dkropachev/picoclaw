package gateway

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/sipeed/picoclaw/pkg/agent"
	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/eventing"
	eventchannel "github.com/sipeed/picoclaw/pkg/eventing/channelmessage"
	eventwebhook "github.com/sipeed/picoclaw/pkg/eventing/webhook"
	runtimeevents "github.com/sipeed/picoclaw/pkg/events"
	"github.com/sipeed/picoclaw/pkg/logger"
	"github.com/sipeed/picoclaw/pkg/workflows"
)

type eventAutomationService struct {
	store          *eventing.Store
	webhookBackend *eventwebhook.Backend
	channelBackend *eventchannel.Backend
	cancel         context.CancelFunc
	done           chan struct{}

	stopOnce  sync.Once
	closeOnce sync.Once
	closeErr  error
}

type eventAutomationRuntimeAcquire func(context.Context) (context.Context, func(), error)

func setupEventAutomationService(
	ctx context.Context,
	cfg *config.Config,
	agentLoop *agent.AgentLoop,
) (*eventAutomationService, error) {
	if cfg == nil || !cfg.Events.Ingress.Enabled {
		return nil, nil
	}
	var executor *workflows.Executor
	var runtimeEvents runtimeevents.Bus
	if agentLoop != nil {
		runtimeEvents = agentLoop.RuntimeEventBus()
	}
	if cfg.Workflows.Enabled {
		var err error
		executor, err = agent.NewEventWorkflowExecutor(ctx, agentLoop)
		if err != nil {
			return nil, err
		}
	}
	var acquireRuntime eventAutomationRuntimeAcquire
	if cfg.Workflows.Enabled {
		acquireRuntime = func(workerCtx context.Context) (context.Context, func(), error) {
			return agentLoop.AcquireRuntimeGeneration(workerCtx, cfg)
		}
	}
	return newEventAutomationService(ctx, cfg, executor, runtimeEvents, acquireRuntime)
}

func newEventAutomationService(
	ctx context.Context,
	cfg *config.Config,
	executor *workflows.Executor,
	runtimeEvents runtimeevents.Bus,
	acquireRuntime eventAutomationRuntimeAcquire,
) (*eventAutomationService, error) {
	if cfg == nil || !cfg.Events.Ingress.Enabled {
		return nil, nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if cfg.Workflows.Enabled && executor == nil {
		return nil, fmt.Errorf("workflow executor is required when event workflows are enabled")
	}

	workspace := cfg.WorkspacePath()
	store, err := openEventAutomationStore(ctx, cfg)
	if err != nil {
		return nil, err
	}

	webhookBackend, err := newEventWebhookBackend(cfg, store)
	if err != nil {
		return nil, errors.Join(err, store.Close())
	}
	channelBackend, err := newEventChannelBackend(cfg, store)
	if err != nil {
		return nil, errors.Join(err, store.Close())
	}

	service := &eventAutomationService{
		store:          store,
		webhookBackend: webhookBackend,
		channelBackend: channelBackend,
		done:           make(chan struct{}),
	}
	if !cfg.Workflows.Enabled {
		close(service.done)
		return service, nil
	}

	runStore := executor.Store
	if runStore == nil {
		runStore = workflows.NewFileRunStore(workspace)
		executor.Store = runStore
	}
	workerCtx, cancel := context.WithCancel(context.Background())
	service.cancel = cancel
	router := &workflows.EventWorkflowRouter{
		Inbox:                store,
		WorkspaceDir:         workspace,
		DefinitionsDir:       executor.DefinitionsDir,
		RuntimeCompatibility: executor.RuntimeCompatibility,
		RuntimeEvents:        runtimeEvents,
		WorkerLabel:          "gateway-workflow-router",
	}
	dispatcher := &workflows.EventWorkflowDispatcher{
		Inbox:                store,
		Executor:             executor,
		RunStore:             runStore,
		WorkspaceDir:         workspace,
		DefinitionsDir:       executor.DefinitionsDir,
		RuntimeCompatibility: executor.RuntimeCompatibility,
		WorkerLabel:          "gateway-workflow-dispatcher",
	}

	var workers sync.WaitGroup
	workers.Add(2)
	go runEventAutomationWorker(
		workerCtx,
		&workers,
		"router",
		withEventAutomationRuntime(acquireRuntime, router.ProcessOne),
	)
	go runEventAutomationWorker(
		workerCtx,
		&workers,
		"dispatcher",
		withEventAutomationRuntime(acquireRuntime, dispatcher.ProcessOne),
	)
	go func() {
		workers.Wait()
		close(service.done)
	}()
	return service, nil
}

func validateEventAutomationRuntime(
	ctx context.Context,
	cfg *config.Config,
	agentLoop *agent.AgentLoop,
) error {
	if cfg == nil || !cfg.Events.Ingress.Enabled || !cfg.Workflows.Enabled {
		return nil
	}
	if _, err := agent.NewEventWorkflowExecutor(ctx, agentLoop); err != nil {
		return fmt.Errorf("initialize event workflow runtime: %w", err)
	}
	return nil
}

func withEventAutomationRuntime(
	acquire eventAutomationRuntimeAcquire,
	process func(context.Context) (bool, error),
) func(context.Context) (bool, error) {
	if acquire == nil {
		return process
	}
	return func(ctx context.Context) (bool, error) {
		leaseCtx, releaseRuntime, err := acquire(ctx)
		if err != nil {
			return false, err
		}
		defer releaseRuntime()
		return process(leaseCtx)
	}
}

func openEventAutomationStore(ctx context.Context, cfg *config.Config) (*eventing.Store, error) {
	if cfg == nil || !cfg.Events.Ingress.Enabled {
		return nil, nil
	}
	if err := cfg.Events.Ingress.Validate(); err != nil {
		return nil, fmt.Errorf("validate event ingress: %w", err)
	}
	if err := cfg.Events.Ingress.ValidateEventChannelAdapters(
		cfg.Channels,
		cfg.SensitiveDataValues()...,
	); err != nil {
		return nil, fmt.Errorf("validate event channel adapters: %w", err)
	}
	if ctx == nil {
		ctx = context.Background()
	}
	workspace := cfg.WorkspacePath()
	ingress := config.EffectiveEventIngressConfig(cfg, workspace)
	store, err := eventing.Open(
		ctx,
		ingress.DatabasePath,
		eventing.WithMaxPayloadBytes(ingress.MaxPayloadBytes),
		eventing.WithRedaction(
			ingress.RedactFields,
			eventRedactionSecretValues(cfg, ingress),
		),
	)
	if err != nil {
		return nil, fmt.Errorf("open durable event inbox: %w", err)
	}
	return store, nil
}

func newEventWebhookBackend(
	cfg *config.Config,
	store eventwebhook.Inserter,
) (*eventwebhook.Backend, error) {
	if cfg == nil || !cfg.Events.Ingress.Enabled || store == nil {
		return nil, nil
	}
	ingress := config.EffectiveEventIngressConfig(cfg, cfg.WorkspacePath())
	secrets := make(map[string]string)
	formats := make(map[string]string)
	for name, connector := range ingress.Webhooks {
		if !connector.Enabled {
			continue
		}
		secrets[name] = connector.Secret.String()
		formats[name] = config.EffectiveEventWebhookFormat(connector)
	}
	if len(secrets) == 0 {
		return nil, nil
	}
	backend, err := eventwebhook.NewBackend(eventwebhook.BackendConfig{
		Store:            store,
		ConnectorSecrets: secrets,
		ConnectorFormats: formats,
		MaxPayloadBytes:  ingress.MaxPayloadBytes,
	})
	if err != nil {
		return nil, fmt.Errorf("prepare generic event webhook ingress: %w", err)
	}
	return backend, nil
}

func eventWebhookSecretValues(ingress config.EventIngressConfig) []string {
	secrets := make([]string, 0, len(ingress.Webhooks))
	for _, connector := range ingress.Webhooks {
		if !connector.Enabled {
			continue
		}
		if secret := connector.Secret.String(); secret != "" {
			secrets = append(secrets, secret)
		}
	}
	return secrets
}

func eventRedactionSecretValues(
	cfg *config.Config,
	ingress config.EventIngressConfig,
) []string {
	secrets := eventWebhookSecretValues(ingress)
	seen := make(map[string]struct{}, len(secrets))
	for _, secret := range secrets {
		seen[secret] = struct{}{}
	}
	if cfg == nil {
		return secrets
	}
	for _, secret := range cfg.SensitiveDataValues() {
		// Very short credentials would redact common characters throughout
		// otherwise-safe message text. Match the existing LLM-boundary policy:
		// keys and tokens longer than three characters are exact-value secrets.
		if len(secret) <= 3 {
			continue
		}
		if _, exists := seen[secret]; exists {
			continue
		}
		seen[secret] = struct{}{}
		secrets = append(secrets, secret)
	}
	return secrets
}

// validateEventAutomationStorage proves the prospective durable inbox can be
// opened before a hot reload stops or commits the currently running services.
func validateEventAutomationStorage(ctx context.Context, cfg *config.Config) error {
	store, err := openEventAutomationStore(ctx, cfg)
	if err != nil {
		return err
	}
	if store == nil {
		return nil
	}
	if err := store.Close(); err != nil {
		return fmt.Errorf("close validated durable event inbox: %w", err)
	}
	return nil
}

func runEventAutomationWorker(
	ctx context.Context,
	workers *sync.WaitGroup,
	name string,
	process func(context.Context) (bool, error),
) {
	defer workers.Done()
	for {
		processed, err := process(ctx)
		if err != nil && ctx.Err() == nil {
			logger.WarnCF("eventing", "Event workflow worker iteration failed", map[string]any{
				"worker": name,
				"error":  err.Error(),
			})
		}
		if ctx.Err() != nil {
			return
		}
		if processed && err == nil {
			continue
		}
		timer := time.NewTimer(workflows.DefaultEventWorkerPollInterval)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				<-timer.C
			}
			return
		case <-timer.C:
		}
	}
}

// Close stops and drains workers before closing the inbox. A timed-out call
// can be retried; the store is never closed while workers may still use it.
func (s *eventAutomationService) Close(ctx context.Context) error {
	if s == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	s.stopOnce.Do(func() {
		if s.cancel != nil {
			s.cancel()
		}
	})
	select {
	case <-s.done:
	case <-ctx.Done():
		return ctx.Err()
	}
	s.closeOnce.Do(func() {
		s.closeErr = s.store.Close()
	})
	return s.closeErr
}

func closeEventAutomationService(
	ctx context.Context,
	service **eventAutomationService,
) error {
	if service == nil || *service == nil {
		return nil
	}
	err := (*service).Close(ctx)
	if err == nil {
		*service = nil
	}
	return err
}
