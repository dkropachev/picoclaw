package channels

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/time/rate"

	"github.com/sipeed/picoclaw/pkg/bus"
	"github.com/sipeed/picoclaw/pkg/config"
	runtimeevents "github.com/sipeed/picoclaw/pkg/events"
	"github.com/sipeed/picoclaw/pkg/health"
	"github.com/sipeed/picoclaw/pkg/media"
	"github.com/sipeed/picoclaw/pkg/utils"
)

// mockChannel is a test double that delegates Send to a configurable function.
type mockChannel struct {
	BaseChannel
	sendFn            func(ctx context.Context, msg bus.OutboundMessage) error
	startFn           func(ctx context.Context) error
	stopFn            func(ctx context.Context) error
	sentMessages      []bus.OutboundMessage
	placeholdersSent  int
	editedMessages    int
	lastPlaceholderID string
}

type mockWebhookRouteChannel struct {
	mockChannel
	path string
}

func (m *mockWebhookRouteChannel) WebhookPath() string {
	return m.path
}

func (m *mockWebhookRouteChannel) ServeHTTP(
	w http.ResponseWriter,
	_ *http.Request,
) {
	w.WriteHeader(http.StatusTeapot)
}

func (m *mockChannel) Send(ctx context.Context, msg bus.OutboundMessage) ([]string, error) {
	m.sentMessages = append(m.sentMessages, msg)
	if m.sendFn == nil {
		return nil, nil
	}
	return nil, m.sendFn(ctx, msg)
}

func (m *mockChannel) Start(ctx context.Context) error {
	if m.startFn != nil {
		return m.startFn(ctx)
	}
	return nil
}

func (m *mockChannel) Stop(ctx context.Context) error {
	if m.stopFn != nil {
		return m.stopFn(ctx)
	}
	return nil
}

func (m *mockChannel) SendPlaceholder(ctx context.Context, chatID string) (string, error) {
	m.placeholdersSent++
	m.lastPlaceholderID = "mock-ph-123"
	return m.lastPlaceholderID, nil
}

func (m *mockChannel) EditMessage(ctx context.Context, chatID, messageID, content string) error {
	m.editedMessages++
	return nil
}

type mockMediaChannel struct {
	mockChannel
	sendMediaFn       func(ctx context.Context, msg bus.OutboundMediaMessage) ([]string, error)
	sentMediaMessages []bus.OutboundMediaMessage
}

func (m *mockMediaChannel) SendMedia(ctx context.Context, msg bus.OutboundMediaMessage) ([]string, error) {
	m.sentMediaMessages = append(m.sentMediaMessages, msg)
	if m.sendMediaFn != nil {
		return m.sendMediaFn(ctx, msg)
	}
	return nil, nil
}

type mockDeletingMediaChannel struct {
	mockMediaChannel
	deleteCalls     int
	dismissedChatID string
	lastDeleted     struct {
		chatID    string
		messageID string
	}
}

func (m *mockDeletingMediaChannel) DeleteMessage(
	_ context.Context,
	chatID string,
	messageID string,
) error {
	m.deleteCalls++
	m.lastDeleted.chatID = chatID
	m.lastDeleted.messageID = messageID
	return nil
}

func (m *mockDeletingMediaChannel) DismissToolFeedbackMessage(_ context.Context, chatID string) {
	m.dismissedChatID = chatID
}

type turnUXDeadlineDelete struct {
	chatID    string
	messageID string
}

type turnUXDeadlineChannel struct {
	mockChannel
	deleted chan turnUXDeadlineDelete
}

func newTurnUXDeadlineChannel() *turnUXDeadlineChannel {
	return &turnUXDeadlineChannel{
		deleted: make(chan turnUXDeadlineDelete, 4),
	}
}

func (channel *turnUXDeadlineChannel) DeleteMessage(
	_ context.Context,
	chatID, messageID string,
) error {
	channel.deleted <- turnUXDeadlineDelete{
		chatID:    chatID,
		messageID: messageID,
	}
	return nil
}

type mockStreamer struct {
	finalizeFn            func(context.Context, string) error
	finalizeWithContextFn func(context.Context, string, *bus.ContextUsage) error
}

func (m *mockStreamer) Update(context.Context, string) error { return nil }

func (m *mockStreamer) Finalize(ctx context.Context, content string) error {
	if m.finalizeFn != nil {
		return m.finalizeFn(ctx, content)
	}
	return nil
}

func (m *mockStreamer) FinalizeWithContext(ctx context.Context, content string, usage *bus.ContextUsage) error {
	if m.finalizeWithContextFn != nil {
		return m.finalizeWithContextFn(ctx, content, usage)
	}
	return m.Finalize(ctx, content)
}

func (m *mockStreamer) Cancel(context.Context) {}

type mockReasoningStreamer struct {
	mockStreamer
	reasoningUpdates []string
	reasoningFinal   string
}

func (m *mockReasoningStreamer) UpdateReasoning(_ context.Context, content string) error {
	m.reasoningUpdates = append(m.reasoningUpdates, content)
	return nil
}

func (m *mockReasoningStreamer) FinalizeReasoning(_ context.Context, content string) error {
	m.reasoningFinal = content
	return nil
}

type modelTrackingReasoningStreamer struct {
	mockReasoningStreamer
	modelNames []string
}

func (m *modelTrackingReasoningStreamer) SetModelName(modelName string) {
	m.modelNames = append(m.modelNames, strings.TrimSpace(modelName))
}

type recordingStreamSegment struct {
	updates       []string
	finals        []string
	finalUsage    *bus.ContextUsage
	canceledCount int
	modelNames    []string
}

func (s *recordingStreamSegment) Update(_ context.Context, content string) error {
	s.updates = append(s.updates, content)
	return nil
}

func (s *recordingStreamSegment) Finalize(ctx context.Context, content string) error {
	return s.FinalizeWithContext(ctx, content, nil)
}

func (s *recordingStreamSegment) FinalizeWithContext(_ context.Context, content string, usage *bus.ContextUsage) error {
	s.finals = append(s.finals, content)
	s.finalUsage = usage
	return nil
}

func (s *recordingStreamSegment) Cancel(context.Context) {
	s.canceledCount++
}

func (s *recordingStreamSegment) SetModelName(modelName string) {
	s.modelNames = append(s.modelNames, strings.TrimSpace(modelName))
}

type mockStreamingChannel struct {
	mockMessageEditor
	streamer        Streamer
	beginStreamFn   func(context.Context, string) (Streamer, error)
	resolveChatIDFn func(chatID string, outboundCtx *bus.InboundContext) string
}

func (m *mockStreamingChannel) BeginStream(ctx context.Context, chatID string) (Streamer, error) {
	if m.beginStreamFn != nil {
		return m.beginStreamFn(ctx, chatID)
	}
	if m.streamer == nil {
		return nil, errors.New("missing streamer")
	}
	return m.streamer, nil
}

func (m *mockStreamingChannel) ToolFeedbackMessageChatID(
	chatID string,
	outboundCtx *bus.InboundContext,
) string {
	if m.resolveChatIDFn != nil {
		return m.resolveChatIDFn(chatID, outboundCtx)
	}
	return chatID
}

// newTestManager creates a minimal Manager suitable for unit tests.
func newTestManager() *Manager {
	return &Manager{
		channels: make(map[string]Channel),
		workers:  make(map[string]*channelWorker),
		bus:      bus.NewMessageBus(),
	}
}

func TestManagerInitChannelUsesConfiguredInstanceName(t *testing.T) {
	const (
		channelType  = "test-instance-name-type"
		instanceName = "notifications-primary"
	)
	RegisterFactory(
		channelType,
		func(
			_ string,
			_ string,
			_ *config.Config,
			messageBus *bus.MessageBus,
		) (Channel, error) {
			return &mockChannel{
				BaseChannel: *NewBaseChannel(channelType, nil, messageBus, nil),
			}, nil
		},
	)

	manager := newTestManager()
	defer manager.bus.Close()
	manager.config = &config.Config{
		Channels: config.ChannelsConfig{
			instanceName: {
				Enabled: true,
				Type:    channelType,
			},
		},
	}
	manager.initChannel(channelType, instanceName)

	channel, ok := manager.channels[instanceName]
	if !ok {
		t.Fatal("configured channel instance was not initialized")
	}
	if channel.Name() != instanceName {
		t.Fatalf("channel.Name() = %q, want %q", channel.Name(), instanceName)
	}
}

func TestManagerRegisterHTTPRouteLifecycle(t *testing.T) {
	var nilManager *Manager
	if release, err := nilManager.RegisterHTTPRoute(
		"/events",
		http.NotFoundHandler(),
	); !errors.Is(err, ErrHTTPRouteUnavailable) || release != nil {
		t.Fatalf(
			"nil manager registration = (%p, %v), want ErrHTTPRouteUnavailable",
			release,
			err,
		)
	}

	m := newTestManager()
	if release, err := m.RegisterHTTPRoute(
		"/events",
		http.NotFoundHandler(),
	); !errors.Is(err, ErrHTTPRouteUnavailable) || release != nil {
		t.Fatalf(
			"registration before setup = (%p, %v), want ErrHTTPRouteUnavailable",
			release,
			err,
		)
	}

	m.SetupHTTPServer("127.0.0.1:0", nil)
	release, err := m.RegisterHTTPRoute("/events", http.HandlerFunc(
		func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusCreated)
		},
	))
	if err != nil {
		t.Fatalf("RegisterHTTPRoute() error = %v", err)
	}

	rec := httptest.NewRecorder()
	m.mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/events", nil))
	if rec.Code != http.StatusCreated {
		t.Fatalf("registered route status = %d, want %d", rec.Code, http.StatusCreated)
	}

	if duplicateRelease, duplicateErr := m.RegisterHTTPRoute(
		"/events",
		http.NotFoundHandler(),
	); !errors.Is(duplicateErr, ErrHTTPRouteConflict) || duplicateRelease != nil {
		t.Fatalf(
			"duplicate registration = (%p, %v), want ErrHTTPRouteConflict",
			duplicateRelease,
			duplicateErr,
		)
	}

	release()
	release()
	rec = httptest.NewRecorder()
	m.mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/events", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("released route status = %d, want %d", rec.Code, http.StatusNotFound)
	}
}

func TestManagerSharedHTTPWriteTimeoutCoversProtectedAIBudget(t *testing.T) {
	m := newTestManager()
	m.SetupHTTPServer("127.0.0.1:0", nil)

	if m.httpServer == nil {
		t.Fatal("SetupHTTPServer() did not create a server")
	}
	if m.httpServer.WriteTimeout != sharedHTTPWriteTimeout ||
		m.httpServer.WriteTimeout <= 120*time.Second {
		t.Fatalf(
			"shared WriteTimeout = %s, want %s and greater than 120s",
			m.httpServer.WriteTimeout,
			sharedHTTPWriteTimeout,
		)
	}
}

func TestManagerRegisterHTTPRouteDoesNotReplaceHealthRoute(t *testing.T) {
	m := newTestManager()
	healthServer := health.NewServer("127.0.0.1", 0, "")
	m.SetupHTTPServer("127.0.0.1:0", healthServer)

	release, err := m.RegisterHTTPRoute("/health", http.NotFoundHandler())
	if !errors.Is(err, ErrHTTPRouteConflict) || release != nil {
		t.Fatalf(
			"health route registration = (%p, %v), want ErrHTTPRouteConflict",
			release,
			err,
		)
	}

	rec := httptest.NewRecorder()
	m.mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/health", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("health route status after conflict = %d, want %d", rec.Code, http.StatusOK)
	}
}

func TestManagerReloadRejectsChannelOverlapWithOwnedRoute(t *testing.T) {
	for _, collisionPath := range []string{
		"/webhooks/events/",
		"/webhooks/events/build-system",
	} {
		t.Run(collisionPath, func(t *testing.T) {
			channelType := "owned-route-collision-" +
				strings.NewReplacer("/", "-", "_", "-").Replace(collisionPath)
			installTestChannelFactory(
				t,
				channelType,
				func(
					_, _ string,
					_ *config.Config,
					_ *bus.MessageBus,
				) (Channel, error) {
					return &mockWebhookRouteChannel{path: collisionPath}, nil
				},
			)

			initial := config.DefaultConfig()
			messageBus := bus.NewMessageBus()
			manager, err := NewManager(initial, messageBus, nil)
			if err != nil {
				t.Fatalf("NewManager() error = %v", err)
			}
			manager.SetupHTTPServer("127.0.0.1:0", nil)
			if startErr := manager.StartAll(context.Background()); startErr != nil {
				t.Fatalf("StartAll() error = %v", startErr)
			}
			t.Cleanup(func() {
				stopCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				_ = manager.StopAll(stopCtx)
				messageBus.Close()
			})

			release, err := manager.RegisterHTTPRoute(
				"/webhooks/events/",
				http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					w.WriteHeader(http.StatusAccepted)
				}),
			)
			if err != nil {
				t.Fatalf("RegisterHTTPRoute() error = %v", err)
			}
			t.Cleanup(release)

			replacement := config.DefaultConfig()
			replacement.Channels["collision"] = &config.Channel{
				Enabled: true,
				Type:    channelType,
			}
			err = manager.Reload(context.Background(), replacement)
			if !errors.Is(err, ErrHTTPRouteConflict) {
				t.Fatalf("Reload() error = %v, want ErrHTTPRouteConflict", err)
			}
			if _, exists := manager.GetChannel("collision"); exists {
				t.Fatal("route-colliding candidate was published")
			}

			response := httptest.NewRecorder()
			manager.mux.ServeHTTP(
				response,
				httptest.NewRequest(
					http.MethodPost,
					"/webhooks/events/build-system",
					nil,
				),
			)
			if response.Code != http.StatusAccepted {
				t.Fatalf(
					"owned route after reload collision = %d, want %d",
					response.Code,
					http.StatusAccepted,
				)
			}
		})
	}
}

func installTestChannelFactory(t *testing.T, channelType string, factory ChannelFactory) {
	t.Helper()

	factoriesMu.Lock()
	previousFactory, hadPreviousFactory := factories[channelType]
	factories[channelType] = factory
	factoriesMu.Unlock()
	t.Cleanup(func() {
		factoriesMu.Lock()
		if hadPreviousFactory {
			factories[channelType] = previousFactory
		} else {
			delete(factories, channelType)
		}
		factoriesMu.Unlock()
	})
}

func TestSetMediaStorePropagatesToExistingChannels(t *testing.T) {
	oldStore := media.NewFileMediaStore()
	newStore := media.NewFileMediaStore()
	ch := &mockChannel{}
	ch.SetMediaStore(oldStore)

	m := newTestManager()
	m.mediaStore = oldStore
	m.channels["telegram"] = ch

	m.SetMediaStore(newStore)

	if m.mediaStore != newStore {
		t.Fatal("manager media store was not updated")
	}
	if got := ch.GetMediaStore(); got != newStore {
		t.Fatalf("channel media store = %p, want %p", got, newStore)
	}
}

func TestStartAll_AllChannelsFail_ReturnsJoinedError(t *testing.T) {
	m := newTestManager()
	errA := errors.New("channel-a start failed")
	errB := errors.New("channel-b start failed")

	m.channels["a"] = &mockChannel{
		startFn: func(_ context.Context) error { return errA },
	}
	m.channels["b"] = &mockChannel{
		startFn: func(_ context.Context) error { return errB },
	}

	err := m.StartAll(t.Context())
	if err == nil {
		t.Fatal("expected StartAll to fail when all channels fail")
	}
	if !strings.Contains(err.Error(), "failed to start any enabled channels") {
		t.Fatalf("unexpected error: %v", err)
	}
	if !errors.Is(err, errA) {
		t.Fatalf("expected error to wrap errA, got: %v", err)
	}
	if !errors.Is(err, errB) {
		t.Fatalf("expected error to wrap errB, got: %v", err)
	}
	if len(m.workers) != 0 {
		t.Fatalf("expected no workers on full startup failure, got %d", len(m.workers))
	}
	if m.dispatchTask != nil {
		t.Fatal("expected dispatch task to be cleared on full startup failure")
	}
}

func TestStartAll_PartialFailure_StartsSuccessfulWorkers(t *testing.T) {
	m := newTestManager()
	errBad := errors.New("bad channel start failed")
	processed := make(chan struct{}, 1)

	m.channels["good"] = &mockChannel{
		sendFn: func(_ context.Context, msg bus.OutboundMessage) error {
			if msg.Channel == "good" {
				select {
				case processed <- struct{}{}:
				default:
				}
			}
			return nil
		},
	}
	m.channels["bad"] = &mockChannel{
		startFn: func(_ context.Context) error { return errBad },
	}

	err := m.StartAll(t.Context())
	if err != nil {
		t.Fatalf("expected StartAll to succeed with partial channel failures, got: %v", err)
	}
	if len(m.workers) != 1 {
		t.Fatalf("expected exactly 1 active worker, got %d", len(m.workers))
	}
	if _, ok := m.workers["good"]; !ok {
		t.Fatal("expected worker for successful channel 'good'")
	}
	if _, ok := m.workers["bad"]; ok {
		t.Fatal("did not expect worker for failed channel 'bad'")
	}
	if m.dispatchTask == nil {
		t.Fatal("expected dispatch task to run when at least one channel starts")
	}

	pubCtx, pubCancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer pubCancel()
	if err := m.bus.PublishOutbound(pubCtx, testOutboundMessage(bus.OutboundMessage{
		Channel: "good",
		ChatID:  "chat-1",
		Content: "hello",
	})); err != nil {
		t.Fatalf("PublishOutbound() error = %v", err)
	}

	select {
	case <-processed:
		// worker processed outbound message as expected
	case <-time.After(2 * time.Second):
		t.Fatal("expected successful channel worker to process outbound message")
	}

	stopCtx, stopCancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer stopCancel()
	if err := m.StopAll(stopCtx); err != nil {
		t.Fatalf("StopAll() error = %v", err)
	}
}

func TestStopAllReturnsChannelErrorsAndCanRetry(t *testing.T) {
	m := newTestManager()
	stopFailure := errors.New("injected channel stop failure")
	var stopCalls atomic.Int32
	var successfulStopCalls atomic.Int32
	m.channels["failing"] = &mockChannel{
		stopFn: func(context.Context) error {
			if stopCalls.Add(1) == 1 {
				return stopFailure
			}
			return nil
		},
	}
	m.channels["successful"] = &mockChannel{
		stopFn: func(context.Context) error {
			successfulStopCalls.Add(1)
			return nil
		},
	}
	if err := m.StartAll(context.Background()); err != nil {
		t.Fatalf("StartAll() error = %v", err)
	}

	stopCtx, stopCancel := context.WithTimeout(context.Background(), 2*time.Second)
	err := m.StopAll(stopCtx)
	stopCancel()
	if !errors.Is(err, stopFailure) {
		t.Fatalf("first StopAll() error = %v, want injected failure", err)
	}

	retryCtx, retryCancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer retryCancel()
	if err = m.StopAll(retryCtx); err != nil {
		t.Fatalf("retry StopAll() error = %v", err)
	}
	if got := stopCalls.Load(); got != 2 {
		t.Fatalf("channel Stop() calls = %d, want 2", got)
	}
	if got := successfulStopCalls.Load(); got != 1 {
		t.Fatalf("successful channel Stop() calls = %d, want 1 across retry", got)
	}
}

func TestStopAllContinuesCurrentChannelsWhenPendingRetirementFails(t *testing.T) {
	m := newTestManager()
	stopFailure := errors.New("injected pending retirement stop failure")
	var retirementStopCalls atomic.Int32
	var currentStopCalls atomic.Int32
	m.pendingRetirements = map[string]*channelRetirement{
		"retiring": {
			name: "retiring",
			channel: &mockChannel{
				stopFn: func(context.Context) error {
					if retirementStopCalls.Add(1) == 1 {
						return stopFailure
					}
					return nil
				},
			},
		},
	}
	m.channels["current"] = &mockChannel{
		stopFn: func(context.Context) error {
			currentStopCalls.Add(1)
			return nil
		},
	}

	err := m.StopAll(t.Context())
	if !errors.Is(err, stopFailure) {
		t.Fatalf("first StopAll() error = %v, want pending retirement failure", err)
	}
	if got := retirementStopCalls.Load(); got != 1 {
		t.Fatalf("pending channel Stop() calls = %d, want 1", got)
	}
	if got := currentStopCalls.Load(); got != 1 {
		t.Fatalf("current channel Stop() calls = %d, want 1 despite pending failure", got)
	}
	m.mu.RLock()
	_, pending := m.pendingRetirements["retiring"]
	m.mu.RUnlock()
	if !pending {
		t.Fatal("failed pending retirement was not retained for retry")
	}

	if err = m.StopAll(t.Context()); err != nil {
		t.Fatalf("retry StopAll() error = %v", err)
	}
	if got := retirementStopCalls.Load(); got != 2 {
		t.Fatalf("pending channel Stop() calls = %d, want 2", got)
	}
	if got := currentStopCalls.Load(); got != 1 {
		t.Fatalf("current channel Stop() calls = %d, want 1 across retry", got)
	}
	m.mu.RLock()
	pendingCount := len(m.pendingRetirements)
	m.mu.RUnlock()
	if pendingCount != 0 {
		t.Fatalf("pending retirements after successful retry = %d, want 0", pendingCount)
	}
}

func TestStopAllTimeoutWaitingForWorkerIsRetryable(t *testing.T) {
	m := newTestManager()
	sendEntered := make(chan struct{})
	allowSend := make(chan struct{})
	var stopCalls atomic.Int32
	m.channels["blocked"] = &mockChannel{
		sendFn: func(context.Context, bus.OutboundMessage) error {
			close(sendEntered)
			<-allowSend
			return nil
		},
		stopFn: func(context.Context) error {
			stopCalls.Add(1)
			return nil
		},
	}
	if err := m.StartAll(context.Background()); err != nil {
		t.Fatalf("StartAll() error = %v", err)
	}
	if err := m.bus.PublishOutbound(context.Background(), testOutboundMessage(bus.OutboundMessage{
		Channel: "blocked",
		ChatID:  "chat-1",
		Content: "hold worker",
	})); err != nil {
		t.Fatalf("PublishOutbound() error = %v", err)
	}
	select {
	case <-sendEntered:
	case <-time.After(2 * time.Second):
		t.Fatal("worker did not enter blocking send")
	}

	stopCtx, stopCancel := context.WithTimeout(context.Background(), 25*time.Millisecond)
	err := m.StopAll(stopCtx)
	stopCancel()
	if !errors.Is(err, context.DeadlineExceeded) {
		close(allowSend)
		t.Fatalf("first StopAll() error = %v, want deadline exceeded", err)
	}
	if got := stopCalls.Load(); got != 0 {
		close(allowSend)
		t.Fatalf("channel Stop() called %d times before worker joined", got)
	}

	close(allowSend)
	retryCtx, retryCancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer retryCancel()
	if err = m.StopAll(retryCtx); err != nil {
		t.Fatalf("retry StopAll() error = %v", err)
	}
	if got := stopCalls.Load(); got != 1 {
		t.Fatalf("channel Stop() calls = %d, want 1", got)
	}
}

func TestStopAllUnblocksBackgroundSendToFullWorkerQueue(t *testing.T) {
	m := newTestManager()
	sendEntered := make(chan struct{})
	var enterOnce sync.Once
	var stopCalls atomic.Int32
	m.channels["full"] = &mockChannel{
		sendFn: func(ctx context.Context, _ bus.OutboundMessage) error {
			enterOnce.Do(func() { close(sendEntered) })
			<-ctx.Done()
			return ctx.Err()
		},
		stopFn: func(context.Context) error {
			stopCalls.Add(1)
			return nil
		},
	}
	if err := m.StartAll(context.Background()); err != nil {
		t.Fatalf("StartAll() error = %v", err)
	}

	m.mu.RLock()
	worker := m.workers["full"]
	m.mu.RUnlock()
	if worker == nil {
		t.Fatal("worker was not created")
	}
	worker.queue <- testOutboundMessage(bus.OutboundMessage{
		Channel: "full",
		ChatID:  "chat-active",
		Content: "block active worker send",
	})
	select {
	case <-sendEntered:
	case <-time.After(2 * time.Second):
		t.Fatal("worker did not enter blocking send")
	}
	for i := 0; i < cap(worker.queue); i++ {
		worker.queue <- testOutboundMessage(bus.OutboundMessage{
			Channel: "full",
			ChatID:  "chat-queued",
			Content: fmt.Sprintf("queued-%d", i),
		})
	}

	blockedSendDone := make(chan error, 1)
	go func() {
		blockedSendDone <- m.SendToChannel(
			context.Background(),
			"full",
			"chat-blocked",
			"must be released by retirement",
		)
	}()
	deadline := time.Now().Add(2 * time.Second)
	for {
		m.mu.RLock()
		active := m.activeSendsByChannel["full"]
		m.mu.RUnlock()
		if active == 1 {
			break
		}
		if time.Now().After(deadline) {
			worker.stop()
			t.Fatal("SendToChannel() did not block on the full queue")
		}
		time.Sleep(time.Millisecond)
	}
	select {
	case err := <-blockedSendDone:
		worker.stop()
		t.Fatalf("SendToChannel() returned before shutdown: %v", err)
	default:
	}

	stopCtx, stopCancel := context.WithTimeout(context.Background(), 2*time.Second)
	err := m.StopAll(stopCtx)
	stopCancel()
	if err != nil {
		// Ensure a regression failure does not strand the test goroutines.
		worker.stop()
	}
	select {
	case sendErr := <-blockedSendDone:
		if !errors.Is(sendErr, ErrNotRunning) {
			t.Fatalf("SendToChannel() error = %v, want ErrNotRunning", sendErr)
		}
	case <-time.After(2 * time.Second):
		worker.stop()
		t.Fatal("SendToChannel() remained blocked after worker retirement")
	}
	if err != nil {
		retryCtx, retryCancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer retryCancel()
		_ = m.StopAll(retryCtx)
		t.Fatalf("StopAll() error = %v", err)
	}
	if got := stopCalls.Load(); got != 1 {
		t.Fatalf("channel Stop() calls = %d, want 1", got)
	}
}

func TestStopAllDrainsAdmittedSynchronousSendBeforeChannelStop(t *testing.T) {
	m := newTestManager()
	sendEntered := make(chan struct{})
	allowSend := make(chan struct{})
	stopEntered := make(chan struct{}, 1)
	m.channels["synchronous"] = &mockChannel{
		sendFn: func(context.Context, bus.OutboundMessage) error {
			close(sendEntered)
			<-allowSend
			return nil
		},
		stopFn: func(context.Context) error {
			stopEntered <- struct{}{}
			return nil
		},
	}
	if err := m.StartAll(context.Background()); err != nil {
		t.Fatalf("StartAll() error = %v", err)
	}

	sendDone := make(chan error, 1)
	go func() {
		sendDone <- m.SendMessage(context.Background(), testOutboundMessage(bus.OutboundMessage{
			Channel: "synchronous",
			ChatID:  "chat-1",
			Content: "hold direct send",
		}))
	}()
	select {
	case <-sendEntered:
	case <-time.After(2 * time.Second):
		t.Fatal("synchronous send did not enter channel")
	}

	stopDone := make(chan error, 1)
	go func() {
		stopCtx, stopCancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer stopCancel()
		stopDone <- m.StopAll(stopCtx)
	}()
	deadline := time.Now().Add(2 * time.Second)
	for {
		m.mu.RLock()
		stopping := m.stopping
		m.mu.RUnlock()
		if stopping {
			break
		}
		if time.Now().After(deadline) {
			close(allowSend)
			t.Fatal("StopAll() did not publish stopping state")
		}
		time.Sleep(time.Millisecond)
	}
	select {
	case <-stopEntered:
		close(allowSend)
		t.Fatal("channel Stop() ran while synchronous send was active")
	default:
	}

	close(allowSend)
	select {
	case err := <-sendDone:
		if err != nil {
			t.Fatalf("SendMessage() error = %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("synchronous send did not finish")
	}
	select {
	case err := <-stopDone:
		if err != nil {
			t.Fatalf("StopAll() error = %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("StopAll() did not finish after synchronous send drained")
	}
	select {
	case <-stopEntered:
	default:
		t.Fatal("channel Stop() did not run after synchronous send drained")
	}
}

func TestReloadChangedChannelRetiresOldIdentityAndKeepsReplacementWorker(t *testing.T) {
	const (
		channelName = "reload-identity"
		channelType = config.ChannelMaixCam
	)
	var (
		factoryMu sync.Mutex
		created   []*mockChannel
		stopCalls []int
	)
	oldSendEntered := make(chan struct{})
	allowOldSend := make(chan struct{})
	replacementSent := make(chan struct{}, 1)
	factory := func(
		_, _ string,
		_ *config.Config,
		_ *bus.MessageBus,
	) (Channel, error) {
		factoryMu.Lock()
		defer factoryMu.Unlock()
		index := len(created)
		stopCalls = append(stopCalls, 0)
		channel := &mockChannel{
			stopFn: func(context.Context) error {
				factoryMu.Lock()
				stopCalls[index]++
				factoryMu.Unlock()
				return nil
			},
		}
		if index == 0 {
			channel.sendFn = func(context.Context, bus.OutboundMessage) error {
				close(oldSendEntered)
				<-allowOldSend
				return nil
			}
		} else if index == 1 {
			channel.sendFn = func(context.Context, bus.OutboundMessage) error {
				replacementSent <- struct{}{}
				return nil
			}
		}
		created = append(created, channel)
		return channel, nil
	}
	factoriesMu.Lock()
	previousFactory, hadPreviousFactory := factories[channelType]
	factories[channelType] = factory
	factoriesMu.Unlock()
	t.Cleanup(func() {
		factoriesMu.Lock()
		if hadPreviousFactory {
			factories[channelType] = previousFactory
		} else {
			delete(factories, channelType)
		}
		factoriesMu.Unlock()
	})

	makeConfig := func(version string) *config.Config {
		cfg := config.DefaultConfig()
		cfg.Channels[channelName] = &config.Channel{
			Enabled:  true,
			Type:     channelType,
			Settings: config.RawNode(fmt.Sprintf(`{"enabled":true,"version":%q}`, version)),
		}
		return cfg
	}

	msgBus := bus.NewMessageBus()
	manager, err := NewManager(makeConfig("old"), msgBus, nil)
	if err != nil {
		t.Fatalf("NewManager() error = %v", err)
	}
	if err = manager.StartAll(context.Background()); err != nil {
		t.Fatalf("StartAll() error = %v", err)
	}
	t.Cleanup(func() {
		stopCtx, stopCancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer stopCancel()
		if stopErr := manager.StopAll(stopCtx); stopErr != nil {
			t.Errorf("StopAll() error = %v", stopErr)
		}
		msgBus.Close()
	})

	if err = msgBus.PublishOutbound(context.Background(), testOutboundMessage(bus.OutboundMessage{
		Channel: channelName,
		ChatID:  "chat-old",
		Content: "block old worker",
	})); err != nil {
		t.Fatalf("PublishOutbound(old) error = %v", err)
	}
	select {
	case <-oldSendEntered:
	case <-time.After(2 * time.Second):
		t.Fatal("old worker did not enter blocking send")
	}
	firstReloadCtx, firstReloadCancel := context.WithTimeout(context.Background(), 25*time.Millisecond)
	err = manager.Reload(firstReloadCtx, makeConfig("new"))
	firstReloadCancel()
	if !errors.Is(err, context.DeadlineExceeded) {
		close(allowOldSend)
		t.Fatalf("first Reload() error = %v, want deadline exceeded", err)
	}
	factoryMu.Lock()
	createdAfterTimeout := len(created)
	factoryMu.Unlock()
	if createdAfterTimeout != 1 {
		close(allowOldSend)
		t.Fatalf("channels created before retirement retry = %d, want 1", createdAfterTimeout)
	}
	manager.mu.RLock()
	pendingAfterTimeout := len(manager.pendingRetirements)
	manager.mu.RUnlock()
	if pendingAfterTimeout != 1 {
		close(allowOldSend)
		t.Fatalf("pending retirements after timeout = %d, want 1", pendingAfterTimeout)
	}
	close(allowOldSend)

	reloadCtx, reloadCancel := context.WithCancel(context.Background())
	if err = manager.Reload(reloadCtx, makeConfig("new")); err != nil {
		reloadCancel()
		t.Fatalf("Reload() error = %v", err)
	}
	reloadCancel()

	factoryMu.Lock()
	if len(created) != 2 {
		factoryMu.Unlock()
		t.Fatalf("created channels = %d, want 2", len(created))
	}
	oldChannel := created[0]
	newChannel := created[1]
	oldStopCalls := stopCalls[0]
	factoryMu.Unlock()
	if oldStopCalls != 1 {
		t.Fatalf("old channel Stop() calls = %d, want 1", oldStopCalls)
	}

	manager.mu.RLock()
	currentChannel := manager.channels[channelName]
	currentWorker := manager.workers[channelName]
	manager.mu.RUnlock()
	if currentChannel != newChannel {
		t.Fatalf("current channel = %p, want replacement %p", currentChannel, newChannel)
	}
	if currentWorker == nil || currentWorker.ch != newChannel {
		t.Fatalf("replacement worker = %#v, want worker bound to %p", currentWorker, newChannel)
	}
	if currentChannel == oldChannel {
		t.Fatal("old channel identity remained installed after reload")
	}

	if err = msgBus.PublishOutbound(context.Background(), testOutboundMessage(bus.OutboundMessage{
		Channel: channelName,
		ChatID:  "chat-1",
		Content: "replacement delivery",
	})); err != nil {
		t.Fatalf("PublishOutbound() error = %v", err)
	}
	select {
	case <-replacementSent:
	case <-time.After(2 * time.Second):
		t.Fatal("replacement worker did not deliver outbound message")
	}
}

func TestReloadDoesNotPublishReplacementBeforeStartSucceeds(t *testing.T) {
	const (
		channelName = "reload-start-barrier"
		channelType = config.ChannelMaixCam
	)
	startFailure := errors.New("injected replacement start failure")
	candidateStartEntered := make(chan struct{})
	allowCandidateStart := make(chan struct{})
	candidateSendEntered := make(chan struct{})
	allowCandidateSend := make(chan struct{})
	candidateStopped := make(chan struct{})
	var candidateSendActive atomic.Int32
	var candidateSendCalls atomic.Int32
	var cleanupOverlappedSend atomic.Bool
	var factoryCalls atomic.Int32

	factory := func(
		_, _ string,
		_ *config.Config,
		_ *bus.MessageBus,
	) (Channel, error) {
		switch factoryCalls.Add(1) {
		case 1:
			return &mockChannel{}, nil
		case 2:
			return &mockChannel{
				startFn: func(context.Context) error {
					close(candidateStartEntered)
					<-allowCandidateStart
					return startFailure
				},
				sendFn: func(context.Context, bus.OutboundMessage) error {
					candidateSendCalls.Add(1)
					candidateSendActive.Add(1)
					close(candidateSendEntered)
					<-allowCandidateSend
					candidateSendActive.Add(-1)
					return nil
				},
				stopFn: func(context.Context) error {
					if candidateSendActive.Load() != 0 {
						cleanupOverlappedSend.Store(true)
					}
					close(candidateStopped)
					return nil
				},
			}, nil
		default:
			return nil, fmt.Errorf("unexpected factory call %d", factoryCalls.Load())
		}
	}
	installTestChannelFactory(t, channelType, factory)

	makeConfig := func(version string) *config.Config {
		cfg := config.DefaultConfig()
		cfg.Channels[channelName] = &config.Channel{
			Enabled:  true,
			Type:     channelType,
			Settings: config.RawNode(fmt.Sprintf(`{"enabled":true,"version":%q}`, version)),
		}
		return cfg
	}

	msgBus := bus.NewMessageBus()
	manager, err := NewManager(makeConfig("old"), msgBus, nil)
	if err != nil {
		t.Fatalf("NewManager() error = %v", err)
	}
	if err = manager.StartAll(context.Background()); err != nil {
		t.Fatalf("StartAll() error = %v", err)
	}
	t.Cleanup(func() {
		stopCtx, stopCancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer stopCancel()
		if stopErr := manager.StopAll(stopCtx); stopErr != nil {
			t.Errorf("StopAll() error = %v", stopErr)
		}
		msgBus.Close()
	})

	reloadDone := make(chan error, 1)
	go func() {
		reloadDone <- manager.Reload(context.Background(), makeConfig("new"))
	}()
	select {
	case <-candidateStartEntered:
	case <-time.After(2 * time.Second):
		t.Fatal("replacement did not enter Start()")
	}

	type getChannelResult struct {
		channel Channel
		ok      bool
	}
	getDone := make(chan getChannelResult, 1)
	go func() {
		channel, ok := manager.GetChannel(channelName)
		getDone <- getChannelResult{channel: channel, ok: ok}
	}()
	sendDone := make(chan error, 1)
	go func() {
		sendDone <- manager.SendToChannel(
			context.Background(),
			channelName,
			"chat-1",
			"must not reach unstarted candidate",
		)
	}()

	var getResult getChannelResult
	select {
	case getResult = <-getDone:
	case <-time.After(2 * time.Second):
		close(allowCandidateStart)
		t.Fatal("GetChannel() blocked while replacement Start() was in progress")
	}

	var sendErr error
	sendFinished := false
	candidateWasReached := false
	select {
	case sendErr = <-sendDone:
		sendFinished = true
	case <-candidateSendEntered:
		candidateWasReached = true
	case <-time.After(2 * time.Second):
		close(allowCandidateStart)
		t.Fatal("SendToChannel() neither rejected nor reached the candidate")
	}

	close(allowCandidateStart)
	select {
	case <-candidateStopped:
	case <-time.After(2 * time.Second):
		close(allowCandidateSend)
		t.Fatal("failed replacement was not cleaned up")
	}
	close(allowCandidateSend)
	if !sendFinished {
		select {
		case sendErr = <-sendDone:
		case <-time.After(2 * time.Second):
			t.Fatal("SendToChannel() did not finish after candidate cleanup")
		}
	}
	select {
	case err = <-reloadDone:
	case <-time.After(2 * time.Second):
		t.Fatal("Reload() did not finish after replacement Start() failed")
	}

	if !errors.Is(err, startFailure) {
		t.Fatalf("Reload() error = %v, want replacement start failure", err)
	}
	if getResult.ok || getResult.channel != nil {
		t.Fatalf("GetChannel() exposed unstarted replacement: channel=%p ok=%v", getResult.channel, getResult.ok)
	}
	if sendErr == nil || !strings.Contains(sendErr.Error(), "not found") {
		t.Fatalf("SendToChannel() error = %v, want channel-not-found rejection", sendErr)
	}
	if candidateWasReached || candidateSendCalls.Load() != 0 {
		t.Fatal("SendToChannel() reached replacement before Start() succeeded")
	}
	if cleanupOverlappedSend.Load() {
		t.Fatal("failed replacement cleanup overlapped an admitted send")
	}
}

func TestReloadRetainsFailedCandidateCleanupUntilRetry(t *testing.T) {
	const (
		channelName = "reload-cleanup-retry"
		channelType = config.ChannelMaixCam
	)
	startFailure := errors.New("injected candidate start failure")
	stopFailure := errors.New("injected candidate cleanup failure")
	var factoryCalls atomic.Int32
	var failedCandidateStopCalls atomic.Int32
	var replacement *mockChannel
	factory := func(
		_, _ string,
		_ *config.Config,
		_ *bus.MessageBus,
	) (Channel, error) {
		switch factoryCalls.Add(1) {
		case 1:
			return &mockChannel{
				startFn: func(context.Context) error {
					return startFailure
				},
				stopFn: func(context.Context) error {
					if failedCandidateStopCalls.Add(1) < 3 {
						return stopFailure
					}
					return nil
				},
			}, nil
		case 2:
			replacement = &mockChannel{}
			return replacement, nil
		default:
			return nil, fmt.Errorf("unexpected factory call %d", factoryCalls.Load())
		}
	}
	installTestChannelFactory(t, channelType, factory)

	oldConfig := config.DefaultConfig()
	oldConfig.Channels = make(config.ChannelsConfig)
	newConfig := config.DefaultConfig()
	newConfig.Channels = make(config.ChannelsConfig)
	newConfig.Channels[channelName] = &config.Channel{
		Enabled:  true,
		Type:     channelType,
		Settings: config.RawNode(`{"enabled":true,"version":"new"}`),
	}

	msgBus := bus.NewMessageBus()
	manager, err := NewManager(oldConfig, msgBus, nil)
	if err != nil {
		t.Fatalf("NewManager() error = %v", err)
	}
	t.Cleanup(func() {
		stopCtx, stopCancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer stopCancel()
		if stopErr := manager.StopAll(stopCtx); stopErr != nil {
			t.Errorf("StopAll() error = %v", stopErr)
		}
		msgBus.Close()
	})

	err = manager.Reload(context.Background(), newConfig)
	if !errors.Is(err, startFailure) || !errors.Is(err, stopFailure) {
		t.Fatalf("first Reload() error = %v, want start and cleanup failures", err)
	}
	if got := factoryCalls.Load(); got != 1 {
		t.Fatalf("factory calls after failed cleanup = %d, want 1", got)
	}
	manager.mu.RLock()
	firstPending := manager.pendingRetirements[channelName]
	manager.mu.RUnlock()
	if firstPending == nil {
		t.Fatal("failed candidate cleanup was not retained")
	}

	err = manager.Reload(context.Background(), newConfig)
	if !errors.Is(err, stopFailure) {
		t.Fatalf("second Reload() error = %v, want pending cleanup failure", err)
	}
	if got := factoryCalls.Load(); got != 1 {
		t.Fatalf("factory calls before cleanup succeeded = %d, want 1", got)
	}
	manager.mu.RLock()
	secondPending := manager.pendingRetirements[channelName]
	manager.mu.RUnlock()
	if secondPending != firstPending {
		t.Fatal("pending cleanup identity changed before Stop() succeeded")
	}

	if err = manager.Reload(context.Background(), newConfig); err != nil {
		t.Fatalf("third Reload() error = %v", err)
	}
	if got := failedCandidateStopCalls.Load(); got != 3 {
		t.Fatalf("failed candidate Stop() calls = %d, want 3", got)
	}
	if got := factoryCalls.Load(); got != 2 {
		t.Fatalf("factory calls after cleanup retry = %d, want 2", got)
	}
	manager.mu.RLock()
	current := manager.channels[channelName]
	worker := manager.workers[channelName]
	pendingCount := len(manager.pendingRetirements)
	manager.mu.RUnlock()
	if current != replacement {
		t.Fatalf("current channel = %p, want replacement %p", current, replacement)
	}
	if worker == nil || worker.ch != replacement {
		t.Fatalf("current worker = %#v, want worker for replacement %p", worker, replacement)
	}
	if pendingCount != 0 {
		t.Fatalf("pending retirements after cleanup succeeded = %d, want 0", pendingCount)
	}
}

func TestDispatcherContinuesAfterCapturedRetiredWorkerQueueIsFull(t *testing.T) {
	manager := newTestManager()
	retired := newChannelWorker("retired", &mockChannel{}, "retired")
	for range cap(retired.queue) {
		retired.queue <- testOutboundMessage(bus.OutboundMessage{
			Channel: "retired",
			ChatID:  "full",
			Content: "queued",
		})
	}
	active := newChannelWorker("active", &mockChannel{}, "active")
	manager.channels["retired"] = retired.ch
	manager.channels["active"] = active.ch
	manager.workers["retired"] = retired
	manager.workers["active"] = active

	dispatchCtx, dispatchCancel := context.WithCancel(context.Background())
	dispatchDone := make(chan struct{})
	go func() {
		defer close(dispatchDone)
		manager.dispatchOutbound(dispatchCtx)
	}()
	defer func() {
		dispatchCancel()
		select {
		case <-dispatchDone:
		case <-time.After(2 * time.Second):
			t.Error("dispatcher did not stop")
		}
	}()

	if err := manager.bus.PublishOutbound(context.Background(), testOutboundMessage(bus.OutboundMessage{
		Channel: "retired",
		ChatID:  "chat-1",
		Content: "would block",
	})); err != nil {
		t.Fatalf("PublishOutbound(retired) error = %v", err)
	}
	retired.markRetired()

	if err := manager.bus.PublishOutbound(context.Background(), testOutboundMessage(bus.OutboundMessage{
		Channel: "active",
		ChatID:  "chat-2",
		Content: "must continue",
	})); err != nil {
		t.Fatalf("PublishOutbound(active) error = %v", err)
	}
	select {
	case msg := <-active.queue:
		if msg.Content != "must continue" {
			t.Fatalf("active queue content = %q", msg.Content)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("dispatcher stopped or remained blocked on retired worker")
	}
}

func TestStartAllPublishesLifecycleRuntimeEvents(t *testing.T) {
	eventBus := runtimeevents.NewBus()
	defer func() {
		if err := eventBus.Close(); err != nil {
			t.Errorf("event bus close failed: %v", err)
		}
	}()

	_, eventsCh, err := eventBus.Channel().SubscribeChan(
		t.Context(),
		runtimeevents.SubscribeOptions{Name: "channel-lifecycle", Buffer: 4},
	)
	if err != nil {
		t.Fatalf("SubscribeChan failed: %v", err)
	}

	m := newTestManager()
	m.runtimeEvents = eventBus
	m.config = &config.Config{Channels: config.ChannelsConfig{}}
	m.channels["good"] = &mockChannel{}
	m.channels["bad"] = &mockChannel{
		startFn: func(_ context.Context) error { return errors.New("bad start") },
	}

	if err := m.StartAll(t.Context()); err != nil {
		t.Fatalf("StartAll() error = %v", err)
	}
	t.Cleanup(func() {
		stopCtx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if err := m.StopAll(stopCtx); err != nil {
			t.Errorf("StopAll() error = %v", err)
		}
	})

	events := []runtimeevents.Event{
		receiveChannelRuntimeEvent(t, eventsCh),
		receiveChannelRuntimeEvent(t, eventsCh),
	}
	seen := map[runtimeevents.Kind]runtimeevents.Event{}
	for _, evt := range events {
		seen[evt.Kind] = evt
	}
	if evt, ok := seen[runtimeevents.KindChannelLifecycleStarted]; !ok || evt.Scope.Channel != "good" {
		t.Fatalf("missing started event for good channel: %+v", events)
	}
	if evt, ok := seen[runtimeevents.KindChannelLifecycleStartFailed]; !ok || evt.Scope.Channel != "bad" {
		t.Fatalf("missing failed event for bad channel: %+v", events)
	}
}

func testOutboundMessage(msg bus.OutboundMessage) bus.OutboundMessage {
	if msg.Context.Channel == "" && msg.Context.ChatID == "" {
		msg.Context = bus.NewOutboundContext(msg.Channel, msg.ChatID, msg.ReplyToMessageID)
	}
	return bus.NormalizeOutboundMessage(msg)
}

func testOutboundMediaMessage(msg bus.OutboundMediaMessage) bus.OutboundMediaMessage {
	if msg.Context.Channel == "" && msg.Context.ChatID == "" {
		msg.Context = bus.NewOutboundContext(msg.Channel, msg.ChatID, "")
	}
	return bus.NormalizeOutboundMediaMessage(msg)
}

func receiveChannelRuntimeEvent(t *testing.T, ch <-chan runtimeevents.Event) runtimeevents.Event {
	t.Helper()

	select {
	case evt, ok := <-ch:
		if !ok {
			t.Fatal("runtime event channel closed before expected event")
		}
		return evt
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for runtime event")
		return runtimeevents.Event{}
	}
}

func TestSendWithRetry_Success(t *testing.T) {
	m := newTestManager()
	var callCount int
	ch := &mockChannel{
		sendFn: func(_ context.Context, _ bus.OutboundMessage) error {
			callCount++
			return nil
		},
	}
	w := &channelWorker{
		ch:      ch,
		limiter: rate.NewLimiter(rate.Inf, 1),
	}

	ctx := context.Background()
	msg := testOutboundMessage(bus.OutboundMessage{Channel: "test", ChatID: "1", Content: "hello"})

	m.sendWithRetry(ctx, "test", w, msg)

	if callCount != 1 {
		t.Fatalf("expected 1 Send call, got %d", callCount)
	}
}

func TestSendWithRetryPublishesOutboundRuntimeEvents(t *testing.T) {
	eventBus := runtimeevents.NewBus()
	defer func() {
		if err := eventBus.Close(); err != nil {
			t.Errorf("event bus close failed: %v", err)
		}
	}()

	_, eventsCh, err := eventBus.Channel().OfKind(
		runtimeevents.KindChannelMessageOutboundSent,
		runtimeevents.KindChannelMessageOutboundFailed,
	).SubscribeChan(t.Context(), runtimeevents.SubscribeOptions{Name: "channel-outbound", Buffer: 2})
	if err != nil {
		t.Fatalf("SubscribeChan failed: %v", err)
	}

	m := newTestManager()
	m.runtimeEvents = eventBus

	successWorker := &channelWorker{
		ch:      &mockChannel{},
		limiter: rate.NewLimiter(rate.Inf, 1),
	}
	m.sendWithRetry(
		context.Background(),
		"test",
		successWorker,
		testOutboundMessage(bus.OutboundMessage{Channel: "test", ChatID: "chat-1", Content: "hello"}),
	)
	sent := receiveChannelRuntimeEvent(t, eventsCh)
	if sent.Kind != runtimeevents.KindChannelMessageOutboundSent || sent.Scope.ChatID != "chat-1" {
		t.Fatalf("sent event = %+v", sent)
	}
	if sent.Attrs["content_len"] != 5 {
		t.Fatalf("sent attrs = %#v, want content_len", sent.Attrs)
	}

	failWorker := &channelWorker{
		ch: &mockChannel{
			sendFn: func(context.Context, bus.OutboundMessage) error {
				return fmt.Errorf("send failed: %w", ErrSendFailed)
			},
		},
		limiter: rate.NewLimiter(rate.Inf, 1),
	}
	m.sendWithRetry(
		context.Background(),
		"test",
		failWorker,
		testOutboundMessage(bus.OutboundMessage{Channel: "test", ChatID: "chat-2", Content: "hello"}),
	)
	failed := receiveChannelRuntimeEvent(t, eventsCh)
	if failed.Kind != runtimeevents.KindChannelMessageOutboundFailed || failed.Scope.ChatID != "chat-2" {
		t.Fatalf("failed event = %+v", failed)
	}
	if failed.Severity != runtimeevents.SeverityError {
		t.Fatalf("failed severity = %q", failed.Severity)
	}
	if failed.Attrs["error"] == "" || failed.Attrs["retries"] != maxRetries {
		t.Fatalf("failed attrs = %#v, want error and retries", failed.Attrs)
	}
}

func TestSendWithRetry_TemporaryThenSuccess(t *testing.T) {
	m := newTestManager()
	var callCount int
	ch := &mockChannel{
		sendFn: func(_ context.Context, _ bus.OutboundMessage) error {
			callCount++
			if callCount <= 2 {
				return fmt.Errorf("network error: %w", ErrTemporary)
			}
			return nil
		},
	}
	w := &channelWorker{
		ch:      ch,
		limiter: rate.NewLimiter(rate.Inf, 1),
	}

	ctx := context.Background()
	msg := testOutboundMessage(bus.OutboundMessage{Channel: "test", ChatID: "1", Content: "hello"})

	m.sendWithRetry(ctx, "test", w, msg)

	if callCount != 3 {
		t.Fatalf("expected 3 Send calls (2 failures + 1 success), got %d", callCount)
	}
}

func TestSendWithRetry_PermanentFailure(t *testing.T) {
	m := newTestManager()
	var callCount int
	ch := &mockChannel{
		sendFn: func(_ context.Context, _ bus.OutboundMessage) error {
			callCount++
			return fmt.Errorf("bad chat ID: %w", ErrSendFailed)
		},
	}
	w := &channelWorker{
		ch:      ch,
		limiter: rate.NewLimiter(rate.Inf, 1),
	}

	ctx := context.Background()
	msg := testOutboundMessage(bus.OutboundMessage{Channel: "test", ChatID: "1", Content: "hello"})

	m.sendWithRetry(ctx, "test", w, msg)

	if callCount != 1 {
		t.Fatalf("expected 1 Send call (no retry for permanent failure), got %d", callCount)
	}
}

func TestSendWithRetry_NotRunning(t *testing.T) {
	m := newTestManager()
	var callCount int
	ch := &mockChannel{
		sendFn: func(_ context.Context, _ bus.OutboundMessage) error {
			callCount++
			return ErrNotRunning
		},
	}
	w := &channelWorker{
		ch:      ch,
		limiter: rate.NewLimiter(rate.Inf, 1),
	}

	ctx := context.Background()
	msg := testOutboundMessage(bus.OutboundMessage{Channel: "test", ChatID: "1", Content: "hello"})

	m.sendWithRetry(ctx, "test", w, msg)

	if callCount != 1 {
		t.Fatalf("expected 1 Send call (no retry for ErrNotRunning), got %d", callCount)
	}
}

func TestSendWithRetry_RateLimitRetry(t *testing.T) {
	m := newTestManager()
	var callCount int
	ch := &mockChannel{
		sendFn: func(_ context.Context, _ bus.OutboundMessage) error {
			callCount++
			if callCount == 1 {
				return fmt.Errorf("429: %w", ErrRateLimit)
			}
			return nil
		},
	}
	w := &channelWorker{
		ch:      ch,
		limiter: rate.NewLimiter(rate.Inf, 1),
	}

	ctx := context.Background()
	msg := testOutboundMessage(bus.OutboundMessage{Channel: "test", ChatID: "1", Content: "hello"})

	start := time.Now()
	m.sendWithRetry(ctx, "test", w, msg)
	elapsed := time.Since(start)

	if callCount != 2 {
		t.Fatalf("expected 2 Send calls (1 rate limit + 1 success), got %d", callCount)
	}
	// Should have waited at least rateLimitDelay (1s) but allow some slack
	if elapsed < 900*time.Millisecond {
		t.Fatalf("expected at least ~1s delay for rate limit retry, got %v", elapsed)
	}
}

func TestSendWithRetry_MaxRetriesExhausted(t *testing.T) {
	m := newTestManager()
	var callCount int
	ch := &mockChannel{
		sendFn: func(_ context.Context, _ bus.OutboundMessage) error {
			callCount++
			return fmt.Errorf("timeout: %w", ErrTemporary)
		},
	}
	w := &channelWorker{
		ch:      ch,
		limiter: rate.NewLimiter(rate.Inf, 1),
	}

	ctx := context.Background()
	msg := testOutboundMessage(bus.OutboundMessage{Channel: "test", ChatID: "1", Content: "hello"})

	m.sendWithRetry(ctx, "test", w, msg)

	expected := maxRetries + 1 // initial attempt + maxRetries retries
	if callCount != expected {
		t.Fatalf("expected %d Send calls, got %d", expected, callCount)
	}
}

func TestSendMedia_Success(t *testing.T) {
	m := newTestManager()
	var callCount int
	ch := &mockMediaChannel{
		sendMediaFn: func(_ context.Context, _ bus.OutboundMediaMessage) ([]string, error) {
			callCount++
			return nil, nil
		},
	}
	w := &channelWorker{
		ch:      ch,
		limiter: rate.NewLimiter(rate.Inf, 1),
	}
	m.channels["test"] = ch
	m.workers["test"] = w

	err := m.SendMedia(context.Background(), testOutboundMediaMessage(bus.OutboundMediaMessage{
		Channel: "test",
		ChatID:  "chat1",
		Parts:   []bus.MediaPart{{Ref: "media://abc"}},
	}))
	if err != nil {
		t.Fatalf("SendMedia() error = %v", err)
	}
	if callCount != 1 {
		t.Fatalf("expected 1 SendMedia call, got %d", callCount)
	}
}

func TestSendMedia_PropagatesFailure(t *testing.T) {
	m := newTestManager()
	ch := &mockMediaChannel{
		sendMediaFn: func(_ context.Context, _ bus.OutboundMediaMessage) ([]string, error) {
			return nil, fmt.Errorf("bad upload: %w", ErrSendFailed)
		},
	}
	w := &channelWorker{
		ch:      ch,
		limiter: rate.NewLimiter(rate.Inf, 1),
	}
	m.channels["test"] = ch
	m.workers["test"] = w

	err := m.SendMedia(context.Background(), testOutboundMediaMessage(bus.OutboundMediaMessage{
		Channel: "test",
		ChatID:  "chat1",
		Parts:   []bus.MediaPart{{Ref: "media://abc"}},
	}))
	if err == nil {
		t.Fatal("expected SendMedia to return error")
	}
	if !errors.Is(err, ErrSendFailed) {
		t.Fatalf("expected ErrSendFailed, got %v", err)
	}
}

func TestSendMedia_UnsupportedChannelReturnsError(t *testing.T) {
	m := newTestManager()
	ch := &mockChannel{
		sendFn: func(_ context.Context, _ bus.OutboundMessage) error {
			return nil
		},
	}
	w := &channelWorker{
		ch:      ch,
		limiter: rate.NewLimiter(rate.Inf, 1),
	}
	m.channels["test"] = ch
	m.workers["test"] = w

	err := m.SendMedia(context.Background(), testOutboundMediaMessage(bus.OutboundMediaMessage{
		Channel: "test",
		ChatID:  "chat1",
		Parts:   []bus.MediaPart{{Ref: "media://abc"}},
	}))
	if err == nil {
		t.Fatal("expected SendMedia to return error for unsupported channel")
	}
	if !strings.Contains(err.Error(), "does not support media sending") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestSendMedia_DeletesPlaceholderBeforeSending(t *testing.T) {
	m := newTestManager()
	ch := &mockDeletingMediaChannel{
		mockMediaChannel: mockMediaChannel{
			sendMediaFn: func(_ context.Context, _ bus.OutboundMediaMessage) ([]string, error) {
				return nil, nil
			},
		},
	}
	w := &channelWorker{
		ch:      ch,
		limiter: rate.NewLimiter(rate.Inf, 1),
	}
	m.channels["test"] = ch
	m.workers["test"] = w
	m.RecordPlaceholder("test", "chat1", "placeholder-1")

	err := m.SendMedia(context.Background(), testOutboundMediaMessage(bus.OutboundMediaMessage{
		Channel: "test",
		ChatID:  "chat1",
		Parts:   []bus.MediaPart{{Ref: "media://abc"}},
	}))
	if err != nil {
		t.Fatalf("SendMedia() error = %v", err)
	}
	if ch.deleteCalls != 1 {
		t.Fatalf("expected placeholder delete to be called once, got %d", ch.deleteCalls)
	}
	if ch.lastDeleted.chatID != "chat1" || ch.lastDeleted.messageID != "placeholder-1" {
		t.Fatalf("unexpected placeholder deletion target: %+v", ch.lastDeleted)
	}
	if len(ch.sentMediaMessages) != 1 {
		t.Fatalf("expected media to be sent once, got %d", len(ch.sentMediaMessages))
	}
}

func TestSendWithRetry_UnknownError(t *testing.T) {
	m := newTestManager()
	var callCount int
	ch := &mockChannel{
		sendFn: func(_ context.Context, _ bus.OutboundMessage) error {
			callCount++
			if callCount == 1 {
				return errors.New("random unexpected error")
			}
			return nil
		},
	}
	w := &channelWorker{
		ch:      ch,
		limiter: rate.NewLimiter(rate.Inf, 1),
	}

	ctx := context.Background()
	msg := testOutboundMessage(bus.OutboundMessage{Channel: "test", ChatID: "1", Content: "hello"})

	m.sendWithRetry(ctx, "test", w, msg)

	if callCount != 2 {
		t.Fatalf("expected 2 Send calls (unknown error treated as temporary), got %d", callCount)
	}
}

func TestSendWithRetry_ContextCancelled(t *testing.T) {
	m := newTestManager()
	var callCount int
	ch := &mockChannel{
		sendFn: func(_ context.Context, _ bus.OutboundMessage) error {
			callCount++
			return fmt.Errorf("timeout: %w", ErrTemporary)
		},
	}
	w := &channelWorker{
		ch:      ch,
		limiter: rate.NewLimiter(rate.Inf, 1),
	}

	ctx, cancel := context.WithCancel(context.Background())
	msg := testOutboundMessage(bus.OutboundMessage{Channel: "test", ChatID: "1", Content: "hello"})

	// Cancel context after first Send attempt returns
	ch.sendFn = func(_ context.Context, _ bus.OutboundMessage) error {
		callCount++
		cancel()
		return fmt.Errorf("timeout: %w", ErrTemporary)
	}

	m.sendWithRetry(ctx, "test", w, msg)

	// Should have called Send once, then noticed ctx canceled during backoff
	if callCount != 1 {
		t.Fatalf("expected 1 Send call before context cancellation, got %d", callCount)
	}
}

func TestWorkerRateLimiter(t *testing.T) {
	m := newTestManager()

	var mu sync.Mutex
	var sendTimes []time.Time

	ch := &mockChannel{
		sendFn: func(_ context.Context, _ bus.OutboundMessage) error {
			mu.Lock()
			sendTimes = append(sendTimes, time.Now())
			mu.Unlock()
			return nil
		},
	}

	// Create a worker with a low rate: 2 msg/s, burst 1
	w := &channelWorker{
		ch:      ch,
		queue:   make(chan bus.OutboundMessage, 10),
		done:    make(chan struct{}),
		limiter: rate.NewLimiter(2, 1),
	}

	ctx := t.Context()

	go m.runWorker(ctx, "test", w)

	// Enqueue 4 messages
	for i := range 4 {
		w.queue <- testOutboundMessage(bus.OutboundMessage{Channel: "test", ChatID: "1", Content: fmt.Sprintf("msg%d", i)})
	}

	// Wait enough time for all messages to be sent (4 msgs at 2/s = ~2s, give extra margin)
	time.Sleep(3 * time.Second)

	mu.Lock()
	times := make([]time.Time, len(sendTimes))
	copy(times, sendTimes)
	mu.Unlock()

	if len(times) != 4 {
		t.Fatalf("expected 4 sends, got %d", len(times))
	}

	// Verify rate limiting: total duration should be at least 1s
	// (first message immediate, then ~500ms between each subsequent one at 2/s)
	totalDuration := times[len(times)-1].Sub(times[0])
	if totalDuration < 1*time.Second {
		t.Fatalf("expected total duration >= 1s for 4 msgs at 2/s rate, got %v", totalDuration)
	}
}

func TestNewChannelWorker_DefaultRate(t *testing.T) {
	ch := &mockChannel{}
	w := newChannelWorker("unknown_channel", ch, "unknown_channel")

	if w.limiter == nil {
		t.Fatal("expected limiter to be non-nil")
	}
	if w.limiter.Limit() != rate.Limit(defaultRateLimit) {
		t.Fatalf("expected rate limit %v, got %v", rate.Limit(defaultRateLimit), w.limiter.Limit())
	}
}

func TestNewChannelWorker_ConfiguredRate(t *testing.T) {
	ch := &mockChannel{}

	for channelType, expectedRate := range channelRateConfig {
		w := newChannelWorker(channelType, ch, channelType)
		if w.limiter.Limit() != rate.Limit(expectedRate) {
			t.Fatalf("channel %s: expected rate %v, got %v", channelType, expectedRate, w.limiter.Limit())
		}
	}
}

func TestRunWorker_MessageSplitting(t *testing.T) {
	m := newTestManager()

	var mu sync.Mutex
	var received []string

	ch := &mockChannelWithLength{
		mockChannel: mockChannel{
			sendFn: func(_ context.Context, msg bus.OutboundMessage) error {
				mu.Lock()
				received = append(received, msg.Content)
				mu.Unlock()
				return nil
			},
		},
		maxLen: 5,
	}

	w := &channelWorker{
		ch:      ch,
		queue:   make(chan bus.OutboundMessage, 10),
		done:    make(chan struct{}),
		limiter: rate.NewLimiter(rate.Inf, 1),
	}

	ctx := t.Context()

	go m.runWorker(ctx, "test", w)

	// Send a message that should be split
	w.queue <- testOutboundMessage(bus.OutboundMessage{Channel: "test", ChatID: "1", Content: "hello world"})

	time.Sleep(100 * time.Millisecond)

	mu.Lock()
	count := len(received)
	mu.Unlock()

	if count < 2 {
		t.Fatalf("expected message to be split into at least 2 chunks, got %d", count)
	}
}

// mockChannelWithLength implements MessageLengthProvider.
type mockChannelWithLength struct {
	mockChannel
	maxLen int
}

func (m *mockChannelWithLength) MaxMessageLength() int {
	return m.maxLen
}

func TestSendWithRetry_ExponentialBackoff(t *testing.T) {
	m := newTestManager()

	var callTimes []time.Time
	var callCount atomic.Int32
	ch := &mockChannel{
		sendFn: func(_ context.Context, _ bus.OutboundMessage) error {
			callTimes = append(callTimes, time.Now())
			callCount.Add(1)
			return fmt.Errorf("timeout: %w", ErrTemporary)
		},
	}
	w := &channelWorker{
		ch:      ch,
		limiter: rate.NewLimiter(rate.Inf, 1),
	}

	ctx := context.Background()
	msg := testOutboundMessage(bus.OutboundMessage{Channel: "test", ChatID: "1", Content: "hello"})

	start := time.Now()
	m.sendWithRetry(ctx, "test", w, msg)
	totalElapsed := time.Since(start)

	// With maxRetries=3: attempts at 0, ~500ms, ~1.5s, ~3.5s
	// Total backoff: 500ms + 1s + 2s = 3.5s
	// Allow some margin
	if totalElapsed < 3*time.Second {
		t.Fatalf("expected total elapsed >= 3s for exponential backoff, got %v", totalElapsed)
	}

	if int(callCount.Load()) != maxRetries+1 {
		t.Fatalf("expected %d calls, got %d", maxRetries+1, callCount.Load())
	}
}

// --- Phase 10: preSend orchestration tests ---

// mockMessageEditor is a channel that supports MessageEditor.
type mockMessageEditor struct {
	mockChannel
	editFn            func(ctx context.Context, chatID, messageID, content string) error
	finalizeFn        func(ctx context.Context, msg bus.OutboundMessage) ([]string, bool)
	finalizeCalled    bool
	recordedChatID    string
	recordedMessageID string
	recordedContent   string
	clearedChatID     string
	dismissedChatID   string
}

func (m *mockMessageEditor) EditMessage(ctx context.Context, chatID, messageID, content string) error {
	return m.editFn(ctx, chatID, messageID, content)
}

func (m *mockMessageEditor) RecordToolFeedbackMessage(chatID, messageID, content string) {
	m.recordedChatID = chatID
	m.recordedMessageID = messageID
	m.recordedContent = content
}

func (m *mockMessageEditor) ClearToolFeedbackMessage(chatID string) {
	m.clearedChatID = chatID
}

func (m *mockMessageEditor) DismissToolFeedbackMessage(_ context.Context, chatID string) {
	m.dismissedChatID = chatID
}

func (m *mockMessageEditor) FinalizeToolFeedbackMessage(
	ctx context.Context,
	msg bus.OutboundMessage,
) ([]string, bool) {
	m.finalizeCalled = true
	if m.finalizeFn == nil {
		return nil, false
	}
	return m.finalizeFn(ctx, msg)
}

type mockResolvedToolFeedbackEditor struct {
	mockMessageEditor
	resolveChatIDFn func(chatID string, outboundCtx *bus.InboundContext) string
}

type mockDeletingMessageEditor struct {
	mockMessageEditor
	deleteCalls      int
	deletedChatID    string
	deletedMessageID string
}

func (m *mockDeletingMessageEditor) DeleteMessage(_ context.Context, chatID, messageID string) error {
	m.deleteCalls++
	m.deletedChatID = chatID
	m.deletedMessageID = messageID
	return nil
}

func (m *mockResolvedToolFeedbackEditor) ToolFeedbackMessageChatID(
	chatID string,
	outboundCtx *bus.InboundContext,
) string {
	if m.resolveChatIDFn != nil {
		return m.resolveChatIDFn(chatID, outboundCtx)
	}
	return chatID
}

type mockPreparedToolFeedbackEditor struct {
	mockMessageEditor
	prepareFn func(content string) string
}

func (m *mockPreparedToolFeedbackEditor) PrepareToolFeedbackMessageContent(content string) string {
	if m.prepareFn != nil {
		return m.prepareFn(content)
	}
	return content
}

func TestPreSend_PlaceholderEditSuccess(t *testing.T) {
	m := newTestManager()
	var sendCalled bool
	var editCalled bool

	ch := &mockMessageEditor{
		mockChannel: mockChannel{
			sendFn: func(_ context.Context, _ bus.OutboundMessage) error {
				sendCalled = true
				return nil
			},
		},
		editFn: func(_ context.Context, chatID, messageID, content string) error {
			editCalled = true
			if chatID != "123" {
				t.Fatalf("expected chatID 123, got %s", chatID)
			}
			if messageID != "456" {
				t.Fatalf("expected messageID 456, got %s", messageID)
			}
			if content != "hello" {
				t.Fatalf("expected content 'hello', got %s", content)
			}
			return nil
		},
	}

	// Register placeholder
	m.RecordPlaceholder("test", "123", "456")

	msg := testOutboundMessage(bus.OutboundMessage{Channel: "test", ChatID: "123", Content: "hello"})
	_, edited := m.preSend(context.Background(), "test", msg, ch)

	if !edited {
		t.Fatal("expected preSend to return true (placeholder edited)")
	}
	if !editCalled {
		t.Fatal("expected EditMessage to be called")
	}
	if sendCalled {
		t.Fatal("expected Send to NOT be called when placeholder edited")
	}
}

func TestPreSend_ToolFeedbackPlaceholderEditRecordsTrackedMessage(t *testing.T) {
	m := newTestManager()

	ch := &mockMessageEditor{
		editFn: func(_ context.Context, chatID, messageID, content string) error {
			if chatID != "123" || messageID != "456" || content != "hello" {
				t.Fatalf("unexpected edit args: %s %s %s", chatID, messageID, content)
			}
			return nil
		},
	}

	m.RecordPlaceholder("test", "123", "456")

	msg := testOutboundMessage(bus.OutboundMessage{
		Channel: "test",
		ChatID:  "123",
		Content: "hello",
		Context: bus.InboundContext{
			Channel: "test",
			ChatID:  "123",
			Raw: map[string]string{
				"message_kind": "tool_feedback",
			},
		},
	})
	_, edited := m.preSend(context.Background(), "test", msg, ch)
	if !edited {
		t.Fatal("expected preSend to edit placeholder")
	}
	if ch.recordedChatID != "123" || ch.recordedMessageID != "456" {
		t.Fatalf("expected tracked message 123/456, got %q/%q", ch.recordedChatID, ch.recordedMessageID)
	}
}

func TestPreSend_ToolFeedbackPlaceholderEditUsesResolvedTrackedChatID(t *testing.T) {
	m := newTestManager()

	ch := &mockResolvedToolFeedbackEditor{
		mockMessageEditor: mockMessageEditor{
			editFn: func(_ context.Context, chatID, messageID, content string) error {
				if chatID != "-100123" || messageID != "456" || content != "hello" {
					t.Fatalf("unexpected edit args: %s %s %s", chatID, messageID, content)
				}
				return nil
			},
		},
		resolveChatIDFn: func(chatID string, outboundCtx *bus.InboundContext) string {
			if chatID != "-100123" {
				t.Fatalf("expected raw chat ID, got %q", chatID)
			}
			if outboundCtx == nil || outboundCtx.TopicID != "42" {
				t.Fatalf("expected topic-aware outbound context, got %+v", outboundCtx)
			}
			return chatID + "/" + outboundCtx.TopicID
		},
	}

	m.RecordPlaceholder("test", "-100123", "456")

	msg := testOutboundMessage(bus.OutboundMessage{
		Channel: "test",
		ChatID:  "-100123",
		Content: "hello",
		Context: bus.InboundContext{
			Channel: "test",
			ChatID:  "-100123",
			TopicID: "42",
			Raw: map[string]string{
				"message_kind": "tool_feedback",
			},
		},
	})
	_, edited := m.preSend(context.Background(), "test", msg, ch)
	if !edited {
		t.Fatal("expected preSend to edit placeholder")
	}
	if ch.recordedChatID != "-100123/42" || ch.recordedMessageID != "456" {
		t.Fatalf("expected resolved tracked message -100123/42/456, got %q/%q",
			ch.recordedChatID, ch.recordedMessageID)
	}
}

func TestPreSend_ToolFeedbackPlaceholderEditUsesPreparedContent(t *testing.T) {
	m := newTestManager()

	const rawContent = "🔧 `read_file`\n" + "<raw>"
	const preparedContent = "🔧 `read_file`\n&lt;raw&gt;"

	ch := &mockPreparedToolFeedbackEditor{
		mockMessageEditor: mockMessageEditor{
			editFn: func(_ context.Context, chatID, messageID, content string) error {
				if chatID != "123" || messageID != "456" {
					t.Fatalf("unexpected edit target: %s/%s", chatID, messageID)
				}
				if content != InitialAnimatedToolFeedbackContent(preparedContent) {
					t.Fatalf("unexpected prepared content: %q", content)
				}
				return nil
			},
		},
		prepareFn: func(content string) string {
			if content != rawContent {
				t.Fatalf("unexpected raw tool feedback: %q", content)
			}
			return preparedContent
		},
	}

	m.RecordPlaceholder("test", "123", "456")

	msg := testOutboundMessage(bus.OutboundMessage{
		Channel: "test",
		ChatID:  "123",
		Content: rawContent,
		Context: bus.InboundContext{
			Channel: "test",
			ChatID:  "123",
			Raw: map[string]string{
				"message_kind": "tool_feedback",
			},
		},
	})

	_, edited := m.preSend(context.Background(), "test", msg, ch)
	if !edited {
		t.Fatal("expected preSend to edit placeholder")
	}
	if ch.recordedContent != preparedContent {
		t.Fatalf("expected tracked content %q, got %q", preparedContent, ch.recordedContent)
	}
}

func TestPreSend_NonToolFeedbackLeavesTrackedMessageForChannelSend(t *testing.T) {
	m := newTestManager()
	ch := &mockMessageEditor{}

	msg := testOutboundMessage(bus.OutboundMessage{
		Channel: "test",
		ChatID:  "123",
		Content: "final reply",
		Context: bus.InboundContext{
			Channel: "test",
			ChatID:  "123",
		},
	})

	_, edited := m.preSend(context.Background(), "test", msg, ch)
	if edited {
		t.Fatal("expected preSend to fall through when no placeholder exists")
	}
	if ch.dismissedChatID != "" {
		t.Fatalf("expected tracked tool feedback cleanup to be deferred to channel send, got %q", ch.dismissedChatID)
	}
}

func TestPreSend_NonToolFeedbackDefersTrackedMessageFinalizationToChannelSend(t *testing.T) {
	m := newTestManager()
	ch := &mockMessageEditor{
		finalizeFn: func(_ context.Context, msg bus.OutboundMessage) ([]string, bool) {
			if msg.ChatID != "123" || msg.Content != "final reply" {
				t.Fatalf("unexpected finalize msg: %+v", msg)
			}
			return []string{"tool-msg-1"}, true
		},
	}

	msg := testOutboundMessage(bus.OutboundMessage{
		Channel: "test",
		ChatID:  "123",
		Content: "final reply",
		Context: bus.InboundContext{
			Channel: "test",
			ChatID:  "123",
		},
	})

	msgIDs, handled := m.preSend(context.Background(), "test", msg, ch)
	if handled {
		t.Fatalf("expected preSend to defer to channel Send, got msgIDs=%v", msgIDs)
	}
	if len(msgIDs) != 0 {
		t.Fatalf("expected no msgIDs from preSend, got %v", msgIDs)
	}
	if ch.dismissedChatID != "" {
		t.Fatalf("expected tracked cleanup to remain in channel Send, got %q", ch.dismissedChatID)
	}
	if ch.finalizeCalled {
		t.Fatal("expected preSend to skip channel tool feedback finalization")
	}
}

func TestPreSend_ToolFeedbackSeparateMessagesDeletesPlaceholderAndSkipsEdit(t *testing.T) {
	m := newTestManager()
	m.config = &config.Config{
		Agents: config.AgentsConfig{
			Defaults: config.AgentDefaults{
				ToolFeedback: config.ToolFeedbackConfig{
					Enabled:          true,
					SeparateMessages: true,
				},
			},
		},
	}

	ch := &mockDeletingMessageEditor{
		mockMessageEditor: mockMessageEditor{
			editFn: func(_ context.Context, _, _, _ string) error {
				t.Fatal("expected placeholder edit to be skipped in separate message mode")
				return nil
			},
		},
	}

	m.RecordPlaceholder("test", "123", "456")

	msg := testOutboundMessage(bus.OutboundMessage{
		Channel: "test",
		ChatID:  "123",
		Content: "hello",
		Context: bus.InboundContext{
			Channel: "test",
			ChatID:  "123",
			Raw: map[string]string{
				"message_kind": "tool_feedback",
			},
		},
	})

	msgIDs, handled := m.preSend(context.Background(), "test", msg, ch)
	if handled {
		t.Fatalf("expected preSend to fall through so the channel can send a new message, got %v", msgIDs)
	}
	if ch.deleteCalls != 1 {
		t.Fatalf("expected placeholder deletion, got %d delete calls", ch.deleteCalls)
	}
	if ch.deletedChatID != "123" || ch.deletedMessageID != "456" {
		t.Fatalf("unexpected placeholder deletion target: %s/%s", ch.deletedChatID, ch.deletedMessageID)
	}
	if ch.recordedMessageID != "" {
		t.Fatalf("expected no tracked placeholder record, got %q", ch.recordedMessageID)
	}
	if ch.clearedChatID != "123" {
		t.Fatalf("expected tracked tool feedback state to be cleared before sending, got %q", ch.clearedChatID)
	}
}

func TestPreSend_ThoughtPlaceholderDeleteAndSkipsEdit(t *testing.T) {
	m := newTestManager()

	ch := &mockDeletingMessageEditor{
		mockMessageEditor: mockMessageEditor{
			editFn: func(_ context.Context, _, _, _ string) error {
				t.Fatal("expected thought message to bypass placeholder edit")
				return nil
			},
		},
	}

	m.RecordPlaceholder("test", "123", "456")

	msg := testOutboundMessage(bus.OutboundMessage{
		Channel: "test",
		ChatID:  "123",
		Content: "thinking trace",
		Context: bus.InboundContext{
			Channel: "test",
			ChatID:  "123",
			Raw: map[string]string{
				"message_kind": "thought",
			},
		},
	})

	msgIDs, handled := m.preSend(context.Background(), "test", msg, ch)
	if handled {
		t.Fatalf(
			"expected thought message to fall through so the channel can send a structured message, got %v",
			msgIDs,
		)
	}
	if ch.deleteCalls != 1 {
		t.Fatalf("expected placeholder deletion, got %d delete calls", ch.deleteCalls)
	}
	if ch.deletedChatID != "123" || ch.deletedMessageID != "456" {
		t.Fatalf("unexpected placeholder deletion target: %s/%s", ch.deletedChatID, ch.deletedMessageID)
	}
	if _, ok := m.placeholders.Load("test:123"); ok {
		t.Fatal("expected placeholder to be consumed before structured thought send")
	}
}

func TestSendWithRetry_ToolCallsPlaceholderDeleteAndFallsThroughToSend(t *testing.T) {
	m := newTestManager()

	ch := &mockDeletingMessageEditor{
		mockMessageEditor: mockMessageEditor{
			mockChannel: mockChannel{
				sendFn: func(_ context.Context, msg bus.OutboundMessage) error {
					if got := msg.Context.Raw["message_kind"]; got != "tool_calls" {
						t.Fatalf("expected tool_calls message kind, got %q", got)
					}
					if msg.Content != "" {
						t.Fatalf("expected empty tool_calls content, got %q", msg.Content)
					}
					return nil
				},
			},
			editFn: func(_ context.Context, _, _, _ string) error {
				t.Fatal("expected tool_calls message to bypass placeholder edit")
				return nil
			},
		},
	}

	m.RecordPlaceholder("test", "123", "456")

	w := &channelWorker{
		ch:      ch,
		limiter: rate.NewLimiter(rate.Inf, 1),
	}

	msg := testOutboundMessage(bus.OutboundMessage{
		Channel: "test",
		ChatID:  "123",
		Context: bus.InboundContext{
			Channel: "test",
			ChatID:  "123",
			Raw: map[string]string{
				"message_kind": "tool_calls",
				"tool_calls":   `[{"id":"call_1","type":"function","function":{"name":"read_file","arguments":"{}"},"extra_content":{"tool_feedback_explanation":"Looking up config"}}]`,
			},
		},
	})

	m.sendWithRetry(context.Background(), "test", w, msg)

	if ch.deleteCalls != 1 {
		t.Fatalf("expected placeholder deletion, got %d delete calls", ch.deleteCalls)
	}
	if ch.deletedChatID != "123" || ch.deletedMessageID != "456" {
		t.Fatalf("unexpected placeholder deletion target: %s/%s", ch.deletedChatID, ch.deletedMessageID)
	}
	if len(ch.sentMessages) != 1 {
		t.Fatalf("expected structured tool_calls message to be sent once, got %d", len(ch.sentMessages))
	}
}

func TestPreSend_NonToolFeedbackSeparateMessagesClearsTrackedMessageWithoutDismiss(t *testing.T) {
	m := newTestManager()
	m.config = &config.Config{
		Agents: config.AgentsConfig{
			Defaults: config.AgentDefaults{
				ToolFeedback: config.ToolFeedbackConfig{
					Enabled:          true,
					SeparateMessages: true,
				},
			},
		},
	}

	ch := &mockMessageEditor{}

	msg := testOutboundMessage(bus.OutboundMessage{
		Channel: "test",
		ChatID:  "123",
		Content: "final reply",
		Context: bus.InboundContext{
			Channel: "test",
			ChatID:  "123",
		},
	})

	_, handled := m.preSend(context.Background(), "test", msg, ch)
	if handled {
		t.Fatal("expected preSend to leave final delivery to the channel")
	}
	if ch.clearedChatID != "123" {
		t.Fatalf("expected tracked tool feedback state to be cleared, got %q", ch.clearedChatID)
	}
	if ch.dismissedChatID != "" {
		t.Fatalf("expected tracked tool feedback message to be preserved, got dismissal for %q", ch.dismissedChatID)
	}
	if ch.finalizeCalled {
		t.Fatal("expected separate message mode to skip in-place finalization")
	}
}

func TestPreSend_StaleToolFeedbackDoesNotConsumeStreamActiveMarker(t *testing.T) {
	m := newTestManager()
	m.streamActive.Store("test:123", true)
	m.RecordPlaceholder("test", "123", "placeholder-1")

	var editedContent string
	ch := &mockMessageEditor{
		editFn: func(_ context.Context, chatID, messageID, content string) error {
			if chatID != "123" || messageID != "placeholder-1" {
				t.Fatalf("unexpected edit target: %s/%s", chatID, messageID)
			}
			editedContent = content
			return nil
		},
	}

	toolFeedback := testOutboundMessage(bus.OutboundMessage{
		Channel: "test",
		ChatID:  "123",
		Content: "🔧 `read_file`\nReading config",
		Context: bus.InboundContext{
			Channel: "test",
			ChatID:  "123",
			Raw: map[string]string{
				"message_kind": "tool_feedback",
			},
		},
	})

	msgIDs, handled := m.preSend(context.Background(), "test", toolFeedback, ch)
	if !handled {
		t.Fatal("expected stale tool feedback to be dropped after stream finalize")
	}
	if len(msgIDs) != 0 {
		t.Fatalf("expected no delivered message IDs for stale feedback, got %v", msgIDs)
	}
	if _, ok := m.streamActive.Load("test:123"); !ok {
		t.Fatal("expected streamActive marker to remain for the final outbound message")
	}
	if _, ok := m.placeholders.Load("test:123"); !ok {
		t.Fatal("expected placeholder cleanup to remain deferred to the final outbound message")
	}
	if ch.editedMessages != 0 {
		t.Fatalf("expected no placeholder edit for stale feedback, got %d edits", ch.editedMessages)
	}

	finalMsg := testOutboundMessage(bus.OutboundMessage{
		Channel: "test",
		ChatID:  "123",
		Content: "final streamed reply",
		Context: bus.InboundContext{
			Channel: "test",
			ChatID:  "123",
			Raw: map[string]string{
				"outbound_kind": "final",
			},
		},
	})

	_, handled = m.preSend(context.Background(), "test", finalMsg, ch)
	if !handled {
		t.Fatal("expected final outbound message to consume streamActive marker")
	}
	if _, ok := m.streamActive.Load("test:123"); ok {
		t.Fatal("expected streamActive marker to be cleared by final outbound message")
	}
	if _, ok := m.placeholders.Load("test:123"); ok {
		t.Fatal("expected placeholder to be cleaned up by final outbound message")
	}
	if editedContent != "final streamed reply" {
		t.Fatalf("editedContent = %q, want final streamed reply", editedContent)
	}
}

func TestPreSend_StaleThoughtDoesNotConsumeStreamActiveMarker(t *testing.T) {
	m := newTestManager()
	m.streamActive.Store("test:123", true)
	m.streamAuxiliaryTombstones.Store("test:123", time.Now())
	m.RecordPlaceholder("test", "123", "placeholder-1")

	var editedContent string
	ch := &mockMessageEditor{
		editFn: func(_ context.Context, chatID, messageID, content string) error {
			if chatID != "123" || messageID != "placeholder-1" {
				t.Fatalf("unexpected edit target: %s/%s", chatID, messageID)
			}
			editedContent = content
			return nil
		},
	}

	thought := testOutboundMessage(bus.OutboundMessage{
		Channel: "test",
		ChatID:  "123",
		Content: "late reasoning",
		Context: bus.InboundContext{
			Channel: "test",
			ChatID:  "123",
			Raw: map[string]string{
				"message_kind": "thought",
			},
		},
	})

	msgIDs, handled := m.preSend(context.Background(), "test", thought, ch)
	if !handled {
		t.Fatal("expected stale thought to be dropped after stream finalize")
	}
	if len(msgIDs) != 0 {
		t.Fatalf("expected no delivered message IDs for stale thought, got %v", msgIDs)
	}
	if _, ok := m.streamActive.Load("test:123"); !ok {
		t.Fatal("expected streamActive marker to remain for the final outbound message")
	}
	if _, ok := m.placeholders.Load("test:123"); !ok {
		t.Fatal("expected placeholder cleanup to remain deferred to the final outbound message")
	}
	if ch.editedMessages != 0 {
		t.Fatalf("expected no placeholder edit for stale thought, got %d edits", ch.editedMessages)
	}

	finalMsg := testOutboundMessage(bus.OutboundMessage{
		Channel: "test",
		ChatID:  "123",
		Content: "final streamed reply",
		Context: bus.InboundContext{
			Channel: "test",
			ChatID:  "123",
			Raw: map[string]string{
				"outbound_kind": "final",
			},
		},
	})

	_, handled = m.preSend(context.Background(), "test", finalMsg, ch)
	if !handled {
		t.Fatal("expected final outbound message to consume streamActive marker")
	}
	if _, ok := m.streamActive.Load("test:123"); ok {
		t.Fatal("expected streamActive marker to be cleared by final outbound message")
	}
	if _, ok := m.placeholders.Load("test:123"); ok {
		t.Fatal("expected placeholder to be cleaned up by final outbound message")
	}
	if editedContent != "final streamed reply" {
		t.Fatalf("editedContent = %q, want final streamed reply", editedContent)
	}

	lateThought := testOutboundMessage(bus.OutboundMessage{
		Channel: "test",
		ChatID:  "123",
		Content: "later reasoning",
		Context: bus.InboundContext{
			Channel: "test",
			ChatID:  "123",
			Raw: map[string]string{
				"message_kind": "thought",
			},
		},
	})
	msgIDs, handled = m.preSend(context.Background(), "test", lateThought, ch)
	if !handled {
		t.Fatal("expected tombstone to drop late thought after final outbound was suppressed")
	}
	if len(msgIDs) != 0 {
		t.Fatalf("expected no delivered message IDs for late thought, got %v", msgIDs)
	}
}

func TestPreSend_StreamActiveDoesNotConsumeEarlierVisibleMessage(t *testing.T) {
	m := newTestManager()
	m.streamActive.Store("test:123", true)
	m.streamAuxiliaryTombstones.Store("test:123", time.Now())
	m.RecordPlaceholder("test", "123", "placeholder-1")

	editCalls := 0
	ch := &mockMessageEditor{
		editFn: func(_ context.Context, chatID, messageID, content string) error {
			editCalls++
			if chatID != "123" || messageID != "placeholder-1" || content != "final streamed reply" {
				t.Fatalf("unexpected placeholder edit for %s/%s: %q", chatID, messageID, content)
			}
			return nil
		},
	}

	earlierVisible := testOutboundMessage(bus.OutboundMessage{
		Channel: "test",
		ChatID:  "123",
		Content: "earlier visible message",
		Context: bus.InboundContext{
			Channel: "test",
			ChatID:  "123",
		},
	})
	_, handled := m.preSend(context.Background(), "test", earlierVisible, ch)
	if handled {
		t.Fatal("expected earlier visible message to be delivered normally")
	}
	if editCalls != 0 {
		t.Fatalf("placeholder edits after earlier visible message = %d, want 0", editCalls)
	}
	if _, ok := m.streamActive.Load("test:123"); !ok {
		t.Fatal("expected streamActive marker to remain for final outbound")
	}
	if _, ok := m.streamAuxiliaryTombstones.Load("test:123"); !ok {
		t.Fatal("expected auxiliary tombstone to remain")
	}
	if _, ok := m.placeholders.Load("test:123"); !ok {
		t.Fatal("expected placeholder cleanup to remain deferred to final outbound")
	}

	finalMsg := testOutboundMessage(bus.OutboundMessage{
		Channel: "test",
		ChatID:  "123",
		Content: "final streamed reply",
		Context: bus.InboundContext{
			Channel: "test",
			ChatID:  "123",
			Raw: map[string]string{
				"outbound_kind": "final",
			},
		},
	})
	_, handled = m.preSend(context.Background(), "test", finalMsg, ch)
	if !handled {
		t.Fatal("expected final outbound message to consume streamActive marker")
	}
	if _, ok := m.streamActive.Load("test:123"); ok {
		t.Fatal("expected streamActive marker to be cleared by final outbound message")
	}
	if editCalls != 1 {
		t.Fatalf("placeholder edits after final outbound = %d, want 1", editCalls)
	}
}

func TestPreSend_StreamActiveDoesNotConsumeOtherSessionFinal(t *testing.T) {
	m := newTestManager()
	m.streamActive.Store("test:123", true)
	m.RecordPlaceholder("test", "123", "placeholder-1")

	ch := &mockMessageEditor{
		editFn: func(_ context.Context, _, _, _ string) error {
			t.Fatal("placeholder edit should remain deferred for the streaming session")
			return nil
		},
	}

	otherSessionFinal := testOutboundMessage(bus.OutboundMessage{
		Channel:    "test",
		ChatID:     "123",
		SessionKey: "session-other",
		Content:    "other session final",
		Context: bus.InboundContext{
			Channel: "test",
			ChatID:  "123",
			Raw: map[string]string{
				"outbound_kind": "final",
			},
		},
	})

	_, handled := m.preSend(context.Background(), "test", otherSessionFinal, ch)
	if handled {
		t.Fatal("expected final outbound from a different session to be delivered normally")
	}
	if _, ok := m.streamActive.Load("test:123"); !ok {
		t.Fatal("expected streaming marker to remain for the streaming session")
	}
	if _, ok := m.placeholders.Load("test:123"); !ok {
		t.Fatal("expected placeholder cleanup to remain deferred to the streaming session")
	}
}

func TestPreSendMedia_LeavesTrackedMessageForChannelSend(t *testing.T) {
	m := newTestManager()
	ch := &mockDeletingMediaChannel{}

	m.preSendMedia(context.Background(), "test", bus.OutboundMediaMessage{
		ChatID: "123",
		Context: bus.InboundContext{
			Channel: "test",
			ChatID:  "123",
		},
	}, ch)

	if ch.dismissedChatID != "" {
		t.Fatalf(
			"expected tracked tool feedback cleanup to be deferred to channel media send, got %q",
			ch.dismissedChatID,
		)
	}
}

func TestPreSendMedia_SeparateMessagesClearsTrackedMessageWithoutDismiss(t *testing.T) {
	m := newTestManager()
	m.config = &config.Config{
		Agents: config.AgentsConfig{
			Defaults: config.AgentDefaults{
				ToolFeedback: config.ToolFeedbackConfig{
					Enabled:          true,
					SeparateMessages: true,
				},
			},
		},
	}

	ch := &mockMessageEditor{}

	m.preSendMedia(context.Background(), "test", bus.OutboundMediaMessage{
		ChatID: "123",
		Context: bus.InboundContext{
			Channel: "test",
			ChatID:  "123",
		},
	}, ch)

	if ch.clearedChatID != "123" {
		t.Fatalf("expected tracked tool feedback state to be cleared before media delivery, got %q", ch.clearedChatID)
	}
	if ch.dismissedChatID != "" {
		t.Fatalf("expected tracked tool feedback message to be preserved"+
			" for media delivery, got %q", ch.dismissedChatID)
	}
}

func TestSplitOutboundMessageContent_ToolFeedbackTruncatesInsteadOfSplitting(t *testing.T) {
	msg := testOutboundMessage(bus.OutboundMessage{
		Channel: "test",
		ChatID:  "123",
		Content: "\U0001f527 `read_file`\nRead README.md first to confirm the current project structure before editing the config example.",
		Context: bus.InboundContext{
			Channel: "test",
			ChatID:  "123",
			Raw: map[string]string{
				"message_kind": "tool_feedback",
			},
		},
	})

	chunks := splitOutboundMessageContent(msg, 40)
	if len(chunks) != 1 {
		t.Fatalf("len(chunks) = %d, want 1", len(chunks))
	}
	want := utils.FitToolFeedbackMessage(msg.Content, 40-MaxToolFeedbackAnimationFrameLength())
	if chunks[0] != want {
		t.Fatalf("chunk = %q, want %q", chunks[0], want)
	}
}

func TestSplitOutboundMessageContent_ToolFeedbackReservesAnimationFrame(t *testing.T) {
	msg := testOutboundMessage(bus.OutboundMessage{
		Channel: "test",
		ChatID:  "123",
		Content: "🔧 `read_file`\n1234567890",
		Context: bus.InboundContext{
			Channel: "test",
			ChatID:  "123",
			Raw: map[string]string{
				"message_kind": "tool_feedback",
			},
		},
	})

	chunks := splitOutboundMessageContent(msg, len([]rune(msg.Content)))
	if len(chunks) != 1 {
		t.Fatalf("len(chunks) = %d, want 1", len(chunks))
	}

	animated := formatAnimatedToolFeedbackContent(chunks[0], strings.Repeat(".", MaxToolFeedbackAnimationFrameLength()))
	if got, maxLen := len([]rune(animated)), len([]rune(msg.Content)); got > maxLen {
		t.Fatalf("animated len = %d, want <= %d; content=%q", got, maxLen, animated)
	}
}

func TestGetStreamer_FinalizeDismissesTrackedToolFeedback(t *testing.T) {
	m := newTestManager()
	ch := &mockStreamingChannel{
		mockMessageEditor: mockMessageEditor{},
		streamer: &mockStreamer{
			finalizeFn: func(_ context.Context, content string) error {
				if content != "final reply" {
					t.Fatalf("unexpected finalize content: %q", content)
				}
				return nil
			},
		},
	}
	m.channels["test"] = ch

	streamer, ok := m.GetStreamerForTurn(context.Background(), "test", "123", "", "")
	if !ok {
		t.Fatal("expected streamer to be available")
	}
	if err := streamer.Finalize(context.Background(), "final reply"); err != nil {
		t.Fatalf("Finalize() error = %v", err)
	}
	if ch.dismissedChatID != "123" {
		t.Fatalf("expected tracked tool feedback to be dismissed for chat 123, got %q", ch.dismissedChatID)
	}
	if _, ok := m.streamActive.Load("test:123"); !ok {
		t.Fatal("expected streamActive marker to be recorded after finalize")
	}
}

func TestGetStreamer_FinalizeCleansPlaceholderImmediately(t *testing.T) {
	m := newTestManager()
	m.RecordPlaceholder("test", "123", "placeholder-1")
	var editedContent string
	editCalls := 0
	ch := &mockStreamingChannel{
		mockMessageEditor: mockMessageEditor{
			editFn: func(_ context.Context, chatID, messageID, content string) error {
				if chatID != "123" || messageID != "placeholder-1" {
					t.Fatalf("unexpected edit target: %s/%s", chatID, messageID)
				}
				editCalls++
				editedContent = content
				return nil
			},
		},
		streamer: &mockStreamer{},
	}
	m.channels["test"] = ch

	streamer, ok := m.GetStreamerForTurn(context.Background(), "test", "123", "", "")
	if !ok {
		t.Fatal("expected streamer to be available")
	}
	if err := streamer.Finalize(context.Background(), "final reply"); err != nil {
		t.Fatalf("Finalize() error = %v", err)
	}
	if editedContent != "final reply" {
		t.Fatalf("edited placeholder content = %q, want final reply", editedContent)
	}
	if _, placeholderExists := m.placeholders.Load("test:123"); placeholderExists {
		t.Fatal("expected placeholder to be cleaned up during finalize")
	}
	if _, streamActiveExists := m.streamActive.Load("test:123"); !streamActiveExists {
		t.Fatal("expected streamActive marker to be recorded after finalize")
	}
	cleaner, ok := streamer.(interface{ ClearFinalizedStreamMarker() })
	if !ok {
		t.Fatal("expected streamer to expose marker cleanup")
	}
	cleaner.ClearFinalizedStreamMarker()
	if _, streamActiveExists := m.streamActive.Load("test:123"); streamActiveExists {
		t.Fatal("expected streamActive marker to be cleared")
	}
	if _, ok := m.streamAuxiliaryTombstones.Load("test:123"); !ok {
		t.Fatal("expected auxiliary tombstone to remain after final marker cleanup")
	}

	lateThought := testOutboundMessage(bus.OutboundMessage{
		Channel: "test",
		ChatID:  "123",
		Content: "late reasoning",
		Context: bus.InboundContext{
			Channel: "test",
			ChatID:  "123",
			Raw: map[string]string{
				"message_kind": "thought",
			},
		},
	})
	msgIDs, handled := m.preSend(context.Background(), "test", lateThought, ch)
	if !handled {
		t.Fatal("expected auxiliary tombstone to drop late thought")
	}
	if len(msgIDs) != 0 {
		t.Fatalf("expected no delivered message IDs for late thought, got %v", msgIDs)
	}
	if editCalls != 1 {
		t.Fatalf("expected late thought not to edit placeholder, got %d edits", editCalls)
	}

	finalOutbound := testOutboundMessage(bus.OutboundMessage{
		Channel: "test",
		ChatID:  "123",
		Content: "visible final reply",
		Context: bus.InboundContext{
			Channel: "test",
			ChatID:  "123",
		},
	})
	_, handled = m.preSend(context.Background(), "test", finalOutbound, ch)
	if handled {
		t.Fatal("expected cleared final marker to let normal outbound send")
	}
	if _, ok := m.streamAuxiliaryTombstones.Load("test:123"); ok {
		t.Fatal("expected normal outbound to clear auxiliary tombstone")
	}
}

func TestGetStreamerOlderMessagePreservesNewerPlaceholder(t *testing.T) {
	m := newTestManager()
	var edits int
	ch := &mockStreamingChannel{
		mockMessageEditor: mockMessageEditor{
			editFn: func(_ context.Context, _, messageID, _ string) error {
				if messageID != "new-placeholder" {
					t.Fatalf("edited placeholder %q, want new-placeholder", messageID)
				}
				edits++
				return nil
			},
		},
		streamer: &mockStreamer{},
	}
	m.channels["test"] = ch
	m.RecordTurnUX(
		context.Background(),
		"test",
		"123",
		TurnUXRegistration{
			Identity:    "new-turn",
			Placeholder: "new-placeholder",
			Owner:       ch,
		},
	)

	oldStreamer, ok := m.GetStreamerForTurn(
		context.Background(),
		"test",
		"123",
		"old-session",
		"old-turn",
	)
	if !ok {
		t.Fatal("expected old turn streamer to be available")
	}
	if err := oldStreamer.Finalize(context.Background(), "old reply"); err != nil {
		t.Fatalf("old Finalize() error = %v", err)
	}
	if edits != 0 {
		t.Fatalf("old stream edited newer placeholder %d times", edits)
	}
	if _, loaded := m.placeholders.Load("test:123"); !loaded {
		t.Fatal("old stream consumed newer placeholder")
	}

	newStreamer, ok := m.GetStreamerForTurn(
		context.Background(),
		"test",
		"123",
		"new-session",
		"new-turn",
	)
	if !ok {
		t.Fatal("expected new turn streamer to be available")
	}
	if err := newStreamer.Finalize(context.Background(), "new reply"); err != nil {
		t.Fatalf("new Finalize() error = %v", err)
	}
	if edits != 1 {
		t.Fatalf("matching stream edited placeholder %d times, want 1", edits)
	}
	if _, loaded := m.placeholders.Load("test:123"); loaded {
		t.Fatal("matching stream left placeholder registered")
	}
}

func TestGetStreamerSameSessionFinalMarkersAreTurnScoped(t *testing.T) {
	m := newTestManager()
	ch := &mockStreamingChannel{streamer: &mockStreamer{}}
	m.channels["test"] = ch

	const (
		sessionKey = "session-1"
		oldTurnID  = "turn-old"
		newTurnID  = "turn-new"
	)
	oldStreamer, ok := m.GetStreamerForTurn(
		context.Background(),
		"test",
		"123",
		sessionKey,
		oldTurnID,
	)
	if !ok {
		t.Fatal("expected old turn streamer to be available")
	}
	newStreamer, ok := m.GetStreamerForTurn(
		context.Background(),
		"test",
		"123",
		sessionKey,
		newTurnID,
	)
	if !ok {
		t.Fatal("expected new turn streamer to be available")
	}

	if err := oldStreamer.Finalize(context.Background(), "old reply"); err != nil {
		t.Fatalf("old Finalize() error = %v", err)
	}
	oldKey := streamSuppressionKey("test", "123", sessionKey, oldTurnID)
	newKey := streamSuppressionKey("test", "123", sessionKey, newTurnID)
	if oldKey == newKey {
		t.Fatalf("distinct turn stream keys collapsed to %q", oldKey)
	}
	if _, loaded := m.streamActive.Load(oldKey); !loaded {
		t.Fatal("old turn stream marker was not recorded")
	}

	// The old turn's finalized marker and auxiliary tombstone must not classify
	// the next turn's thought as stale.
	newThought := testOutboundMessage(bus.OutboundMessage{
		Channel:    "test",
		ChatID:     "123",
		SessionKey: sessionKey,
		Content:    "new turn thought",
		Context: bus.InboundContext{
			Channel:  "test",
			ChatID:   "123",
			TurnUXID: newTurnID,
			Raw: map[string]string{
				"message_kind": "thought",
			},
		},
	})
	if _, handled := m.preSend(
		context.Background(),
		"test",
		newThought,
		ch,
	); handled {
		t.Fatal("old turn stream state suppressed the new turn's auxiliary output")
	}

	// Simulate turn B finalizing before the rate-limited final outbound for
	// turn A reaches preSend. Both generation markers must coexist.
	if err := newStreamer.Finalize(context.Background(), "new reply"); err != nil {
		t.Fatalf("new Finalize() error = %v", err)
	}
	if _, loaded := m.streamActive.Load(oldKey); !loaded {
		t.Fatal("new turn finalize replaced the old turn marker")
	}
	if _, loaded := m.streamActive.Load(newKey); !loaded {
		t.Fatal("new turn stream marker was not recorded")
	}

	finalMessage := func(turnUXID, content string) bus.OutboundMessage {
		return testOutboundMessage(bus.OutboundMessage{
			Channel:    "test",
			ChatID:     "123",
			SessionKey: sessionKey,
			Content:    content,
			Context: bus.InboundContext{
				Channel:  "test",
				ChatID:   "123",
				TurnUXID: turnUXID,
				Raw: map[string]string{
					"outbound_kind": "final",
				},
			},
		})
	}
	if _, handled := m.preSend(
		context.Background(),
		"test",
		finalMessage(oldTurnID, "old reply"),
		ch,
	); !handled {
		t.Fatal("old turn final outbound did not consume its stream marker")
	}
	if _, loaded := m.streamActive.Load(oldKey); loaded {
		t.Fatal("old turn stream marker remained after its final outbound")
	}
	if _, loaded := m.streamActive.Load(newKey); !loaded {
		t.Fatal("old turn final outbound consumed the new turn marker")
	}

	if _, handled := m.preSend(
		context.Background(),
		"test",
		finalMessage(newTurnID, "new reply"),
		ch,
	); !handled {
		t.Fatal("new turn final outbound would be delivered a second time")
	}
	if _, loaded := m.streamActive.Load(newKey); loaded {
		t.Fatal("new turn stream marker remained after its final outbound")
	}
}

func TestGetStreamer_FinalizeCleansPlaceholderWithSessionKey(t *testing.T) {
	m := newTestManager()
	m.RecordPlaceholder("test", "123", "placeholder-1")
	ch := &mockStreamingChannel{
		mockMessageEditor: mockMessageEditor{
			editFn: func(_ context.Context, chatID, messageID, content string) error {
				if chatID != "123" || messageID != "placeholder-1" || content != "final reply" {
					t.Fatalf("unexpected edit for %s/%s: %q", chatID, messageID, content)
				}
				return nil
			},
		},
		streamer: &mockStreamer{},
	}
	m.channels["test"] = ch

	streamer, ok := m.GetStreamerForTurn(context.Background(), "test", "123", "session-1", "")
	if !ok {
		t.Fatal("expected streamer to be available")
	}
	if err := streamer.Finalize(context.Background(), "final reply"); err != nil {
		t.Fatalf("Finalize() error = %v", err)
	}
	if _, placeholderExists := m.placeholders.Load("test:123"); placeholderExists {
		t.Fatal("expected placeholder to be cleaned up during finalize")
	}
	if _, streamActiveExists := m.streamActive.Load("test:123:session-1"); !streamActiveExists {
		t.Fatal("expected session streamActive marker to be recorded after finalize")
	}
}

func TestGetStreamer_PreservesContextUsageStreamer(t *testing.T) {
	m := newTestManager()
	var gotUsage *bus.ContextUsage
	ch := &mockStreamingChannel{
		streamer: &mockStreamer{
			finalizeWithContextFn: func(_ context.Context, content string, usage *bus.ContextUsage) error {
				if content != "final reply" {
					t.Fatalf("unexpected finalize content: %q", content)
				}
				gotUsage = usage
				return nil
			},
		},
	}
	m.channels["test"] = ch

	streamer, ok := m.GetStreamerForTurn(context.Background(), "test", "123", "", "")
	if !ok {
		t.Fatal("expected streamer to be available")
	}
	contextStreamer, ok := streamer.(bus.ContextUsageStreamer)
	if !ok {
		t.Fatal("manager-wrapped streamer should preserve ContextUsageStreamer")
	}
	usage := &bus.ContextUsage{UsedTokens: 10, TotalTokens: 100, CompressAtTokens: 80, UsedPercent: 10}
	if err := contextStreamer.FinalizeWithContext(context.Background(), "final reply", usage); err != nil {
		t.Fatalf("FinalizeWithContext() error = %v", err)
	}
	if gotUsage != usage {
		t.Fatalf("context usage = %#v, want original usage", gotUsage)
	}
	if _, ok := m.streamActive.Load("test:123"); !ok {
		t.Fatal("expected streamActive marker to be recorded after finalize with context")
	}
}

func TestGetStreamer_PreservesReasoningStreamer(t *testing.T) {
	m := newTestManager()
	inner := &mockReasoningStreamer{}
	ch := &mockStreamingChannel{
		streamer: inner,
	}
	m.channels["test"] = ch

	streamer, ok := m.GetStreamerForTurn(context.Background(), "test", "123", "", "")
	if !ok {
		t.Fatal("expected streamer to be available")
	}
	reasoningStreamer, ok := streamer.(bus.ReasoningStreamer)
	if !ok {
		t.Fatal("manager-wrapped streamer should preserve ReasoningStreamer")
	}
	if err := reasoningStreamer.UpdateReasoning(context.Background(), "thinking"); err != nil {
		t.Fatalf("UpdateReasoning() error = %v", err)
	}
	if err := reasoningStreamer.FinalizeReasoning(context.Background(), "final thought"); err != nil {
		t.Fatalf("FinalizeReasoning() error = %v", err)
	}
	if got := inner.reasoningUpdates; len(got) != 1 || got[0] != "thinking" {
		t.Fatalf("reasoning updates = %v, want [thinking]", got)
	}
	if inner.reasoningFinal != "final thought" {
		t.Fatalf("reasoning final = %q, want final thought", inner.reasoningFinal)
	}
}

func TestGetStreamer_PreservesModelNameSetter(t *testing.T) {
	m := newTestManager()
	inner := &modelTrackingReasoningStreamer{}
	ch := &mockStreamingChannel{
		streamer: inner,
	}
	m.channels["test"] = ch

	streamer, ok := m.GetStreamerForTurn(context.Background(), "test", "123", "", "")
	if !ok {
		t.Fatal("expected streamer to be available")
	}
	setter, ok := streamer.(interface{ SetModelName(modelName string) })
	if !ok {
		t.Fatal("manager-wrapped streamer should preserve SetModelName")
	}
	setter.SetModelName("gpt-5.4")
	if err := streamer.Update(context.Background(), "hello"); err != nil {
		t.Fatalf("Update() error = %v", err)
	}
	reasoningStreamer, ok := streamer.(bus.ReasoningStreamer)
	if !ok {
		t.Fatal("manager-wrapped streamer should preserve ReasoningStreamer")
	}
	setter.SetModelName("gpt-5.4")
	if err := reasoningStreamer.UpdateReasoning(context.Background(), "thinking"); err != nil {
		t.Fatalf("UpdateReasoning() error = %v", err)
	}
	if len(inner.modelNames) != 2 {
		t.Fatalf("model name calls = %v, want 2 forwarded calls", inner.modelNames)
	}
	if inner.modelNames[0] != "gpt-5.4" || inner.modelNames[1] != "gpt-5.4" {
		t.Fatalf("model name calls = %v, want both forwarded as gpt-5.4", inner.modelNames)
	}
}

func TestGetStreamer_SplitOnMarkerStreamsSeparateSegments(t *testing.T) {
	m := newTestManager()
	m.config = &config.Config{
		Agents: config.AgentsConfig{
			Defaults: config.AgentDefaults{
				SplitOnMarker: true,
			},
		},
	}

	var segments []*recordingStreamSegment
	ch := &mockStreamingChannel{
		beginStreamFn: func(context.Context, string) (Streamer, error) {
			segment := &recordingStreamSegment{}
			segments = append(segments, segment)
			return segment, nil
		},
	}
	m.channels["test"] = ch

	streamer, ok := m.GetStreamerForTurn(context.Background(), "test", "123", "session-1", "")
	if !ok {
		t.Fatal("expected streamer to be available")
	}
	contextStreamer, ok := streamer.(bus.ContextUsageStreamer)
	if !ok {
		t.Fatal("split streamer should preserve ContextUsageStreamer")
	}

	if err := streamer.Update(context.Background(), "hello"); err != nil {
		t.Fatalf("Update(first) error = %v", err)
	}
	if err := streamer.Update(context.Background(), "hello<|[SPLIT]|>world"); err != nil {
		t.Fatalf("Update(split) error = %v", err)
	}
	if err := streamer.Update(context.Background(), "hello<|[SPLIT]|>world!"); err != nil {
		t.Fatalf("Update(second segment) error = %v", err)
	}
	usage := &bus.ContextUsage{UsedTokens: 10, TotalTokens: 100}
	if err := contextStreamer.FinalizeWithContext(
		context.Background(),
		"hello<|[SPLIT]|>world!",
		usage,
	); err != nil {
		t.Fatalf("FinalizeWithContext() error = %v", err)
	}

	if len(segments) != 2 {
		t.Fatalf("segments = %d, want 2", len(segments))
	}
	if got := segments[0].updates; len(got) != 1 || got[0] != "hello" {
		t.Fatalf("segment 0 updates = %v, want [hello]", got)
	}
	if got := segments[0].finals; len(got) != 1 || got[0] != "hello" {
		t.Fatalf("segment 0 finals = %v, want [hello]", got)
	}
	if got := segments[1].updates; len(got) != 2 || got[0] != "world" || got[1] != "world!" {
		t.Fatalf("segment 1 updates = %v, want [world world!]", got)
	}
	if got := segments[1].finals; len(got) != 1 || got[0] != "world!" {
		t.Fatalf("segment 1 finals = %v, want [world!]", got)
	}
	if segments[1].finalUsage != usage {
		t.Fatalf("final usage = %#v, want original usage", segments[1].finalUsage)
	}
	if _, ok := m.streamActive.Load("test:123:session-1"); !ok {
		t.Fatal("expected streamActive marker to be recorded after split stream finalize")
	}
}

func TestGetStreamer_SplitOnMarkerKeepsReasoningOnInitialStreamer(t *testing.T) {
	m := newTestManager()
	m.config = &config.Config{
		Agents: config.AgentsConfig{
			Defaults: config.AgentDefaults{
				SplitOnMarker: true,
			},
		},
	}

	initial := &mockReasoningStreamer{}
	next := &recordingStreamSegment{}
	callCount := 0
	ch := &mockStreamingChannel{
		beginStreamFn: func(context.Context, string) (Streamer, error) {
			callCount++
			if callCount == 1 {
				return initial, nil
			}
			return next, nil
		},
	}
	m.channels["test"] = ch

	streamer, ok := m.GetStreamerForTurn(context.Background(), "test", "123", "", "")
	if !ok {
		t.Fatal("expected streamer to be available")
	}
	if err := streamer.Update(context.Background(), "hello<|[SPLIT]|>world"); err != nil {
		t.Fatalf("Update() error = %v", err)
	}
	reasoningStreamer, ok := streamer.(bus.ReasoningStreamer)
	if !ok {
		t.Fatal("split streamer should preserve ReasoningStreamer")
	}
	if err := reasoningStreamer.UpdateReasoning(context.Background(), "thinking"); err != nil {
		t.Fatalf("UpdateReasoning() error = %v", err)
	}
	if err := reasoningStreamer.FinalizeReasoning(context.Background(), "final thought"); err != nil {
		t.Fatalf("FinalizeReasoning() error = %v", err)
	}

	if got := initial.reasoningUpdates; len(got) != 1 || got[0] != "thinking" {
		t.Fatalf("initial reasoning updates = %v, want [thinking]", got)
	}
	if initial.reasoningFinal != "final thought" {
		t.Fatalf("initial reasoning final = %q, want final thought", initial.reasoningFinal)
	}
}

func TestGetStreamer_SplitOnMarkerPreservesModelNameSetter(t *testing.T) {
	m := newTestManager()
	m.config = &config.Config{
		Agents: config.AgentsConfig{
			Defaults: config.AgentDefaults{
				SplitOnMarker: true,
			},
		},
	}

	initial := &modelTrackingReasoningStreamer{}
	next := &recordingStreamSegment{}
	callCount := 0
	ch := &mockStreamingChannel{
		beginStreamFn: func(context.Context, string) (Streamer, error) {
			callCount++
			if callCount == 1 {
				return initial, nil
			}
			return next, nil
		},
	}
	m.channels["test"] = ch

	streamer, ok := m.GetStreamerForTurn(context.Background(), "test", "123", "", "")
	if !ok {
		t.Fatal("expected streamer to be available")
	}
	setter, ok := streamer.(interface{ SetModelName(modelName string) })
	if !ok {
		t.Fatal("split streamer should preserve SetModelName")
	}
	setter.SetModelName("gpt-5.4-mini")
	if err := streamer.Update(context.Background(), "hello<|[SPLIT]|>world"); err != nil {
		t.Fatalf("Update() error = %v", err)
	}
	reasoningStreamer, ok := streamer.(bus.ReasoningStreamer)
	if !ok {
		t.Fatal("split streamer should preserve ReasoningStreamer")
	}
	if err := reasoningStreamer.UpdateReasoning(context.Background(), "thinking"); err != nil {
		t.Fatalf("UpdateReasoning() error = %v", err)
	}

	if len(initial.modelNames) == 0 || initial.modelNames[0] != "gpt-5.4-mini" {
		t.Fatalf("initial model names = %v, want forwarded gpt-5.4-mini", initial.modelNames)
	}
	if len(next.modelNames) == 0 || next.modelNames[0] != "gpt-5.4-mini" {
		t.Fatalf("next model names = %v, want forwarded gpt-5.4-mini", next.modelNames)
	}
}

func TestGetStreamer_FinalizeSeparateMessagesClearsTrackedToolFeedback(t *testing.T) {
	m := newTestManager()
	m.config = &config.Config{
		Agents: config.AgentsConfig{
			Defaults: config.AgentDefaults{
				ToolFeedback: config.ToolFeedbackConfig{
					Enabled:          true,
					SeparateMessages: true,
				},
			},
		},
	}
	ch := &mockStreamingChannel{
		mockMessageEditor: mockMessageEditor{},
		streamer: &mockStreamer{
			finalizeFn: func(_ context.Context, content string) error {
				if content != "final reply" {
					t.Fatalf("unexpected finalize content: %q", content)
				}
				return nil
			},
		},
	}
	m.channels["test"] = ch

	streamer, ok := m.GetStreamerForTurn(context.Background(), "test", "123", "", "")
	if !ok {
		t.Fatal("expected streamer to be available")
	}
	if err := streamer.Finalize(context.Background(), "final reply"); err != nil {
		t.Fatalf("Finalize() error = %v", err)
	}
	if ch.clearedChatID != "123" {
		t.Fatalf("expected tracked tool feedback to be cleared for chat 123, got %q", ch.clearedChatID)
	}
	if ch.dismissedChatID != "" {
		t.Fatalf("expected tracked tool feedback message to be preserved, got dismissal for %q", ch.dismissedChatID)
	}
	if _, ok := m.streamActive.Load("test:123"); !ok {
		t.Fatal("expected streamActive marker to be recorded after finalize")
	}
}

func TestGetStreamer_FinalizeDismissesResolvedTrackedToolFeedback(t *testing.T) {
	m := newTestManager()
	ch := &mockStreamingChannel{
		mockMessageEditor: mockMessageEditor{},
		streamer: &mockStreamer{
			finalizeFn: func(_ context.Context, content string) error {
				if content != "final reply" {
					t.Fatalf("unexpected finalize content: %q", content)
				}
				return nil
			},
		},
		resolveChatIDFn: func(chatID string, outboundCtx *bus.InboundContext) string {
			if outboundCtx == nil {
				t.Fatal("expected outbound context during stream finalize")
			}
			if outboundCtx.ChatID != "-100123/42" {
				t.Fatalf("unexpected outbound context: %+v", outboundCtx)
			}
			return outboundCtx.ChatID
		},
	}
	m.channels["test"] = ch

	streamer, ok := m.GetStreamerForTurn(context.Background(), "test", "-100123/42", "", "")
	if !ok {
		t.Fatal("expected streamer to be available")
	}
	if err := streamer.Finalize(context.Background(), "final reply"); err != nil {
		t.Fatalf("Finalize() error = %v", err)
	}
	if ch.dismissedChatID != "-100123/42" {
		t.Fatalf("expected resolved tracked tool feedback dismissal, got %q", ch.dismissedChatID)
	}
	if _, ok := m.streamActive.Load("test:-100123/42"); !ok {
		t.Fatal("expected streamActive marker to be recorded after finalize")
	}
}

func TestPreSend_PlaceholderEditSuccessDismissesResolvedTrackedToolFeedback(t *testing.T) {
	m := newTestManager()

	ch := &mockResolvedToolFeedbackEditor{
		mockMessageEditor: mockMessageEditor{
			editFn: func(_ context.Context, chatID, messageID, content string) error {
				if chatID != "-100123" || messageID != "456" || content != "done" {
					t.Fatalf("unexpected edit args: %s %s %s", chatID, messageID, content)
				}
				return nil
			},
		},
		resolveChatIDFn: func(chatID string, outboundCtx *bus.InboundContext) string {
			if outboundCtx == nil || outboundCtx.TopicID != "42" {
				t.Fatalf("expected topic-aware outbound context, got %+v", outboundCtx)
			}
			return chatID + "/" + outboundCtx.TopicID
		},
	}

	m.RecordPlaceholder("test", "-100123", "456")

	msg := testOutboundMessage(bus.OutboundMessage{
		Channel: "test",
		ChatID:  "-100123",
		Content: "done",
		Context: bus.InboundContext{
			Channel: "test",
			ChatID:  "-100123",
			TopicID: "42",
		},
	})

	_, edited := m.preSend(context.Background(), "test", msg, ch)
	if !edited {
		t.Fatal("expected preSend to edit placeholder")
	}
	if ch.dismissedChatID != "-100123/42" {
		t.Fatalf("expected resolved tracked dismissal, got %q", ch.dismissedChatID)
	}
}

func TestGetStreamer_FinalizeFailureDoesNotDismissTrackedToolFeedback(t *testing.T) {
	m := newTestManager()
	ch := &mockStreamingChannel{
		mockMessageEditor: mockMessageEditor{},
		streamer: &mockStreamer{
			finalizeFn: func(context.Context, string) error {
				return errors.New("finalize failed")
			},
		},
	}
	m.channels["test"] = ch

	streamer, ok := m.GetStreamerForTurn(context.Background(), "test", "123", "", "")
	if !ok {
		t.Fatal("expected streamer to be available")
	}
	if err := streamer.Finalize(context.Background(), "final reply"); err == nil {
		t.Fatal("expected Finalize() to fail")
	}
	if ch.dismissedChatID != "" {
		t.Fatalf("expected no tool feedback dismissal on finalize failure, got %q", ch.dismissedChatID)
	}
	if _, ok := m.streamActive.Load("test:123"); ok {
		t.Fatal("expected no streamActive marker after finalize failure")
	}
}

func TestRunWorker_ToolFeedbackSkipsMarkerSplitting(t *testing.T) {
	m := newTestManager()
	m.config = &config.Config{
		Agents: config.AgentsConfig{
			Defaults: config.AgentDefaults{
				SplitOnMarker: true,
			},
		},
	}

	var (
		mu       sync.Mutex
		received []string
	)
	ch := &mockChannelWithLength{
		mockChannel: mockChannel{
			sendFn: func(_ context.Context, msg bus.OutboundMessage) error {
				mu.Lock()
				received = append(received, msg.Content)
				mu.Unlock()
				return nil
			},
		},
		maxLen: 200,
	}

	w := &channelWorker{
		ch:      ch,
		queue:   make(chan bus.OutboundMessage, 1),
		done:    make(chan struct{}),
		limiter: rate.NewLimiter(rate.Inf, 1),
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go m.runWorker(ctx, "test", w)

	content := "🔧 `read_file`\nRead current config first.<|[SPLIT]|>Then update the example."
	w.queue <- testOutboundMessage(bus.OutboundMessage{
		Channel: "test",
		ChatID:  "123",
		Content: content,
		Context: bus.InboundContext{
			Channel: "test",
			ChatID:  "123",
			Raw: map[string]string{
				"message_kind": "tool_feedback",
			},
		},
	})

	time.Sleep(100 * time.Millisecond)

	mu.Lock()
	defer mu.Unlock()
	if len(received) != 1 {
		t.Fatalf("len(received) = %d, want 1", len(received))
	}
	if received[0] != content {
		t.Fatalf("received[0] = %q, want %q", received[0], content)
	}
}

func TestRunWorker_FinalizedStreamSuppressesMarkerSplitBeforeSending(t *testing.T) {
	m := newTestManager()
	m.config = &config.Config{
		Agents: config.AgentsConfig{
			Defaults: config.AgentDefaults{
				SplitOnMarker: true,
			},
		},
	}

	var (
		mu       sync.Mutex
		received []string
	)
	ch := &mockChannel{
		sendFn: func(_ context.Context, msg bus.OutboundMessage) error {
			mu.Lock()
			received = append(received, msg.Content)
			mu.Unlock()
			return nil
		},
	}

	w := &channelWorker{
		ch:      ch,
		queue:   make(chan bus.OutboundMessage, 1),
		done:    make(chan struct{}),
		limiter: rate.NewLimiter(rate.Inf, 1),
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go m.runWorker(ctx, "test", w)

	streamKey := streamSuppressionKey("test", "123", "session-1", "")
	m.streamActive.Store(streamKey, true)
	w.queue <- testOutboundMessage(bus.OutboundMessage{
		Channel:    "test",
		ChatID:     "123",
		SessionKey: "session-1",
		Content:    "streamed full reply<|[SPLIT]|>duplicate chunk",
		Context: bus.InboundContext{
			Channel: "test",
			ChatID:  "123",
			Raw: map[string]string{
				"outbound_kind": "final",
			},
		},
	})

	time.Sleep(100 * time.Millisecond)

	mu.Lock()
	defer mu.Unlock()
	if len(received) != 0 {
		t.Fatalf("received split duplicate messages = %v, want none", received)
	}
	if _, ok := m.streamActive.Load(streamKey); ok {
		t.Fatal("expected finalized stream marker to be consumed")
	}
}

func TestPreSend_PlaceholderEditFails_FallsThrough(t *testing.T) {
	m := newTestManager()

	ch := &mockMessageEditor{
		mockChannel: mockChannel{
			sendFn: func(_ context.Context, _ bus.OutboundMessage) error {
				return nil
			},
		},
		editFn: func(_ context.Context, _, _, _ string) error {
			return fmt.Errorf("edit failed")
		},
	}

	m.RecordPlaceholder("test", "123", "456")

	msg := testOutboundMessage(bus.OutboundMessage{Channel: "test", ChatID: "123", Content: "hello"})
	_, edited := m.preSend(context.Background(), "test", msg, ch)

	if edited {
		t.Fatal("expected preSend to return false when edit fails")
	}
}

func TestInvokeTypingStop_CallsRegisteredStop(t *testing.T) {
	m := newTestManager()
	var stopCalled bool

	m.RecordTypingStop("telegram", "chat123", func() {
		stopCalled = true
	})

	m.InvokeTypingStop("telegram", "chat123")

	if !stopCalled {
		t.Fatal("expected typing stop func to be called")
	}
}

func TestInvokeTypingStop_NoOpWhenNoEntry(t *testing.T) {
	m := newTestManager()
	// Should not panic
	m.InvokeTypingStop("telegram", "nonexistent")
}

func TestInvokeTypingStop_Idempotent(t *testing.T) {
	m := newTestManager()
	var callCount int

	m.RecordTypingStop("telegram", "chat123", func() {
		callCount++
	})

	m.InvokeTypingStop("telegram", "chat123")
	m.InvokeTypingStop("telegram", "chat123") // Second call: entry already removed, no-op

	if callCount != 1 {
		t.Fatalf("expected stop to be called once, got %d", callCount)
	}
}

func TestInvokeTypingStopForMessagePreservesNewerRegistration(t *testing.T) {
	m := newTestManager()
	var calls int
	m.RecordTurnUX(
		context.Background(),
		"telegram",
		"chat123",
		TurnUXRegistration{
			Identity:   "new-turn",
			TypingStop: func() { calls++ },
		},
	)

	m.InvokeTypingStopForMessage("telegram", "chat123", "old-turn")
	if calls != 0 {
		t.Fatalf("mismatched message stopped newer typing %d times", calls)
	}
	if _, loaded := m.typingStops.Load("telegram:chat123"); !loaded {
		t.Fatal("mismatched message consumed newer typing registration")
	}

	m.InvokeTypingStopForMessage("telegram", "chat123", "new-turn")
	if calls != 1 {
		t.Fatalf("matching message stopped typing %d times, want 1", calls)
	}
	if _, loaded := m.typingStops.Load("telegram:chat123"); loaded {
		t.Fatal("matching message left typing registration active")
	}
}

func TestPreSend_TypingStopCalled(t *testing.T) {
	m := newTestManager()
	var stopCalled bool

	ch := &mockChannel{
		sendFn: func(_ context.Context, _ bus.OutboundMessage) error {
			return nil
		},
	}

	m.RecordTypingStop("test", "123", func() {
		stopCalled = true
	})

	msg := testOutboundMessage(bus.OutboundMessage{Channel: "test", ChatID: "123", Content: "hello"})
	m.preSend(context.Background(), "test", msg, ch)

	if !stopCalled {
		t.Fatal("expected typing stop func to be called")
	}
}

func TestPreSend_NoRegisteredState(t *testing.T) {
	m := newTestManager()

	ch := &mockChannel{
		sendFn: func(_ context.Context, _ bus.OutboundMessage) error {
			return nil
		},
	}

	msg := testOutboundMessage(bus.OutboundMessage{Channel: "test", ChatID: "123", Content: "hello"})
	_, edited := m.preSend(context.Background(), "test", msg, ch)

	if edited {
		t.Fatal("expected preSend to return false with no registered state")
	}
}

func TestPreSend_TypingAndPlaceholder(t *testing.T) {
	m := newTestManager()
	var stopCalled bool
	var editCalled bool

	ch := &mockMessageEditor{
		mockChannel: mockChannel{
			sendFn: func(_ context.Context, _ bus.OutboundMessage) error {
				return nil
			},
		},
		editFn: func(_ context.Context, _, _, _ string) error {
			editCalled = true
			return nil
		},
	}

	m.RecordTypingStop("test", "123", func() {
		stopCalled = true
	})
	m.RecordPlaceholder("test", "123", "456")

	msg := testOutboundMessage(bus.OutboundMessage{Channel: "test", ChatID: "123", Content: "hello"})
	_, edited := m.preSend(context.Background(), "test", msg, ch)

	if !stopCalled {
		t.Fatal("expected typing stop to be called")
	}
	if !editCalled {
		t.Fatal("expected EditMessage to be called")
	}
	if !edited {
		t.Fatal("expected preSend to return true")
	}
}

func TestRecordPlaceholder_ConcurrentSafe(t *testing.T) {
	m := newTestManager()

	var wg sync.WaitGroup
	for i := range 100 {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			chatID := fmt.Sprintf("chat_%d", i%10)
			m.RecordPlaceholder("test", chatID, fmt.Sprintf("msg_%d", i))
		}(i)
	}
	wg.Wait()
}

func TestRecordTurnUXRollbackIsScopedToExactRegistration(t *testing.T) {
	manager := newTestManager()
	channel := &mockDeletingMediaChannel{}
	manager.channels["test"] = channel

	var oldStops, oldUndos, newStops, newUndos int
	oldRollback := manager.RecordTurnUX(
		context.Background(),
		"test",
		"chat",
		TurnUXRegistration{
			TypingStop:   func() { oldStops++ },
			ReactionUndo: func() { oldUndos++ },
			Placeholder:  "old-placeholder",
			Owner:        channel,
		},
	)
	newRollback := manager.RecordTurnUX(
		context.Background(),
		"test",
		"chat",
		TurnUXRegistration{
			TypingStop:   func() { newStops++ },
			ReactionUndo: func() { newUndos++ },
			Placeholder:  "new-placeholder",
			Owner:        channel,
		},
	)

	if oldStops != 1 || oldUndos != 1 || channel.deleteCalls != 1 {
		t.Fatalf(
			"replaced UX cleanup = stops:%d undos:%d deletes:%d, want 1/1/1",
			oldStops,
			oldUndos,
			channel.deleteCalls,
		)
	}
	oldRollback(context.Background())
	if _, ok := manager.typingStops.Load("test:chat"); !ok {
		t.Fatal("old rollback removed newer typing registration")
	}
	if _, ok := manager.reactionUndos.Load("test:chat"); !ok {
		t.Fatal("old rollback removed newer reaction registration")
	}
	if _, ok := manager.placeholders.Load("test:chat"); !ok {
		t.Fatal("old rollback removed newer placeholder registration")
	}

	newRollback(context.Background())
	if newStops != 1 || newUndos != 1 || channel.deleteCalls != 2 {
		t.Fatalf(
			"new UX cleanup = stops:%d undos:%d deletes:%d, want 1/1/2",
			newStops,
			newUndos,
			channel.deleteCalls,
		)
	}
	if _, ok := manager.typingStops.Load("test:chat"); ok {
		t.Fatal("new rollback left typing registration")
	}
	if _, ok := manager.reactionUndos.Load("test:chat"); ok {
		t.Fatal("new rollback left reaction registration")
	}
	if _, ok := manager.placeholders.Load("test:chat"); ok {
		t.Fatal("new rollback left placeholder registration")
	}
}

func TestRecordTurnUXPartialRegistrationReplacesEveryArtifactSlot(t *testing.T) {
	manager := newTestManager()
	channel := &mockDeletingMediaChannel{}
	manager.channels["test"] = channel

	var oldStops, oldUndos, newStops int
	manager.RecordTurnUX(
		context.Background(),
		"test",
		"chat",
		TurnUXRegistration{
			Identity:     "old-turn",
			TypingStop:   func() { oldStops++ },
			ReactionUndo: func() { oldUndos++ },
			Placeholder:  "old-placeholder",
			Owner:        channel,
		},
	)
	rollback := manager.RecordTurnUX(
		context.Background(),
		"test",
		"chat",
		TurnUXRegistration{
			Identity:   "new-turn",
			TypingStop: func() { newStops++ },
			Owner:      channel,
		},
	)

	if oldStops != 1 || oldUndos != 1 || channel.deleteCalls != 1 {
		t.Fatalf(
			"partial replacement cleanup = stops:%d undos:%d deletes:%d, want 1/1/1",
			oldStops,
			oldUndos,
			channel.deleteCalls,
		)
	}
	if _, ok := manager.reactionUndos.Load("test:chat"); ok {
		t.Fatal("partial replacement retained the older reaction")
	}
	if _, ok := manager.placeholders.Load("test:chat"); ok {
		t.Fatal("partial replacement retained the older placeholder")
	}
	typingValue, ok := manager.typingStops.Load("test:chat")
	if !ok {
		t.Fatal("partial replacement did not store the new typing entry")
	}
	typing := asTypingEntry(typingValue)
	if typing == nil || typing.currentTurnUXID() != "new-turn" {
		t.Fatalf("stored typing entry = %#v, want new-turn", typing)
	}

	rollback(context.Background())
	if newStops != 1 {
		t.Fatalf("new typing stop calls = %d, want 1", newStops)
	}
	if _, ok := manager.typingStops.Load("test:chat"); ok {
		t.Fatal("new rollback left typing registration")
	}
}

func TestCleanupTurnUXForMessageRemovesOnlyExactRegistration(t *testing.T) {
	manager := newTestManager()
	channel := &mockDeletingMediaChannel{}
	manager.channels["test"] = channel

	var stops, undos int
	manager.RecordTurnUX(
		context.Background(),
		"test",
		"chat",
		TurnUXRegistration{
			Identity:     "current-turn",
			TypingStop:   func() { stops++ },
			ReactionUndo: func() { undos++ },
			Placeholder:  "current-placeholder",
			Owner:        channel,
		},
	)

	manager.CleanupTurnUXForMessage(
		context.Background(),
		"test",
		"chat",
		"older-turn",
	)
	if stops != 0 || undos != 0 || channel.deleteCalls != 0 {
		t.Fatalf(
			"older cleanup touched current UX: stops=%d undos=%d deletes=%d",
			stops,
			undos,
			channel.deleteCalls,
		)
	}
	for name, entries := range map[string]*sync.Map{
		"typing":      &manager.typingStops,
		"reaction":    &manager.reactionUndos,
		"placeholder": &manager.placeholders,
	} {
		if _, ok := entries.Load("test:chat"); !ok {
			t.Fatalf("older cleanup removed current %s registration", name)
		}
	}

	manager.CleanupTurnUXForMessage(
		context.Background(),
		"test",
		"chat",
		"current-turn",
	)
	if stops != 1 || undos != 1 || channel.deleteCalls != 1 {
		t.Fatalf(
			"exact cleanup = stops:%d undos:%d deletes:%d, want 1/1/1",
			stops,
			undos,
			channel.deleteCalls,
		)
	}
	for name, entries := range map[string]*sync.Map{
		"typing":      &manager.typingStops,
		"reaction":    &manager.reactionUndos,
		"placeholder": &manager.placeholders,
	} {
		if _, ok := entries.Load("test:chat"); ok {
			t.Fatalf("exact cleanup left %s registration", name)
		}
	}
}

func TestRecordTurnUXConcurrentRegistrationsCannotMixArtifacts(t *testing.T) {
	manager := newTestManager()
	channel := &mockDeletingMediaChannel{}
	manager.channels["test"] = channel

	oldCleanupStarted := make(chan struct{})
	releaseOldCleanup := make(chan struct{})
	manager.RecordTurnUX(
		context.Background(),
		"test",
		"chat",
		TurnUXRegistration{
			Identity: "old-turn",
			TypingStop: func() {
				close(oldCleanupStarted)
				<-releaseOldCleanup
			},
		},
	)

	aDone := make(chan struct{})
	go func() {
		defer close(aDone)
		manager.RecordTurnUX(
			context.Background(),
			"test",
			"chat",
			TurnUXRegistration{
				Identity:     "turn-a",
				TypingStop:   func() {},
				ReactionUndo: func() {},
				Placeholder:  "placeholder-a",
				Owner:        channel,
			},
		)
	}()
	<-oldCleanupStarted

	bStarted := make(chan struct{})
	bDone := make(chan struct{})
	go func() {
		defer close(bDone)
		close(bStarted)
		manager.RecordTurnUX(
			context.Background(),
			"test",
			"chat",
			TurnUXRegistration{
				Identity:     "turn-b",
				TypingStop:   func() {},
				ReactionUndo: func() {},
				Placeholder:  "placeholder-b",
				Owner:        channel,
			},
		)
	}()
	<-bStarted
	select {
	case <-bDone:
		close(releaseOldCleanup)
		t.Fatal("new registration bypassed the active provider transition")
	case <-time.After(100 * time.Millisecond):
	}
	close(releaseOldCleanup)
	<-aDone
	<-bDone

	typingValue, typingOK := manager.typingStops.Load("test:chat")
	reactionValue, reactionOK := manager.reactionUndos.Load("test:chat")
	placeholderValue, placeholderOK := manager.placeholders.Load("test:chat")
	if !typingOK || !reactionOK || !placeholderOK {
		t.Fatalf(
			"final registration incomplete: typing=%v reaction=%v placeholder=%v",
			typingOK,
			reactionOK,
			placeholderOK,
		)
	}
	typing := asTypingEntry(typingValue)
	reaction := asReactionEntry(reactionValue)
	placeholder := asPlaceholderEntry(placeholderValue)
	if typing == nil || reaction == nil || placeholder == nil {
		t.Fatal("final registration contains invalid entry types")
	}
	if got := typing.currentTurnUXID(); got != "turn-b" {
		t.Fatalf("typing identity = %q, want turn-b", got)
	}
	if got := reaction.currentTurnUXID(); got != "turn-b" {
		t.Fatalf("reaction identity = %q, want turn-b", got)
	}
	if got := placeholder.currentTurnUXID(); got != "turn-b" {
		t.Fatalf("placeholder identity = %q, want turn-b", got)
	}
	if placeholder.id != "placeholder-b" {
		t.Fatalf("placeholder id = %q, want placeholder-b", placeholder.id)
	}
}

func TestReplaceTurnUXCleansPriorGenerationBeforeBuildingNext(t *testing.T) {
	manager := newTestManager()
	cleanupStarted := make(chan struct{})
	releaseCleanup := make(chan struct{})
	manager.RecordTurnUX(
		context.Background(),
		"test",
		"chat",
		TurnUXRegistration{
			Identity: "old-turn",
			TypingStop: func() {
				close(cleanupStarted)
				<-releaseCleanup
			},
		},
	)

	buildStarted := make(chan struct{})
	replaceDone := make(chan struct{})
	go func() {
		defer close(replaceDone)
		manager.ReplaceTurnUX(
			context.Background(),
			"test",
			"chat",
			func() TurnUXRegistration {
				close(buildStarted)
				return TurnUXRegistration{
					Identity:   "new-turn",
					TypingStop: func() {},
				}
			},
		)
	}()

	<-cleanupStarted
	select {
	case <-buildStarted:
		close(releaseCleanup)
		t.Fatal("next provider generation started before prior cleanup returned")
	case <-time.After(100 * time.Millisecond):
	}
	close(releaseCleanup)
	<-replaceDone
	<-buildStarted

	value, loaded := manager.typingStops.Load("test:chat")
	if !loaded {
		t.Fatal("new typing generation was not recorded")
	}
	entry := asTypingEntry(value)
	if entry == nil || entry.currentTurnUXID() != "new-turn" {
		t.Fatalf("stored typing entry = %#v, want new-turn", entry)
	}
}

func holdTurnUXTransitionForDeadlineTest(
	t *testing.T,
	manager *Manager,
	channel, chatID string,
) (release func(), done <-chan struct{}) {
	t.Helper()

	cleanupStarted := make(chan struct{})
	releaseCleanup := make(chan struct{})
	var releaseOnce sync.Once
	release = func() {
		releaseOnce.Do(func() {
			close(releaseCleanup)
		})
	}
	t.Cleanup(release)

	manager.RecordTurnUX(
		context.Background(),
		channel,
		chatID,
		TurnUXRegistration{
			Identity: "deadline-blocker",
			TypingStop: func() {
				close(cleanupStarted)
				<-releaseCleanup
			},
		},
	)

	replaceDone := make(chan struct{})
	go func() {
		defer close(replaceDone)
		manager.ReplaceTurnUX(
			context.Background(),
			channel,
			chatID,
			func() TurnUXRegistration {
				return TurnUXRegistration{Identity: "deadline-holder"}
			},
		)
	}()
	select {
	case <-cleanupStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("provider transition did not reach its blocking cleanup")
	}
	return release, replaceDone
}

func TestReplaceTurnUXFollowerHonorsAcquisitionDeadline(t *testing.T) {
	manager := newTestManager()
	release, holderDone := holdTurnUXTransitionForDeadlineTest(
		t,
		manager,
		"test",
		"deadline-chat",
	)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	buildCalled := make(chan struct{}, 1)
	started := time.Now()
	rollback := manager.ReplaceTurnUX(
		ctx,
		"test",
		"deadline-chat",
		func() TurnUXRegistration {
			buildCalled <- struct{}{}
			return TurnUXRegistration{Identity: "must-not-build"}
		},
	)
	if elapsed := time.Since(started); elapsed > 500*time.Millisecond {
		t.Fatalf("deadline-aware transition acquisition took %v", elapsed)
	}
	if rollback != nil {
		t.Fatal("canceled replacement returned a rollback")
	}
	select {
	case <-buildCalled:
		t.Fatal("canceled replacement invoked its provider build")
	default:
	}

	release()
	select {
	case <-holderDone:
	case <-time.After(2 * time.Second):
		t.Fatal("blocking provider transition did not finish")
	}
}

func TestRecordTurnUXDeadlineCleansUnrecordedArtifacts(t *testing.T) {
	manager := newTestManager()
	owner := newTurnUXDeadlineChannel()
	manager.channels["test"] = owner
	release, holderDone := holdTurnUXTransitionForDeadlineTest(
		t,
		manager,
		"test",
		"record-deadline-chat",
	)

	typingCleaned := make(chan struct{}, 1)
	reactionCleaned := make(chan struct{}, 1)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	rollback := manager.RecordTurnUX(
		ctx,
		"test",
		"record-deadline-chat",
		TurnUXRegistration{
			Identity:     "unrecorded-turn",
			TypingStop:   func() { typingCleaned <- struct{}{} },
			ReactionUndo: func() { reactionCleaned <- struct{}{} },
			Placeholder:  "unrecorded-placeholder",
			Owner:        owner,
		},
	)
	if rollback != nil {
		t.Fatal("canceled registration returned a rollback")
	}

	for name, cleaned := range map[string]<-chan struct{}{
		"typing":   typingCleaned,
		"reaction": reactionCleaned,
	} {
		select {
		case <-cleaned:
		case <-time.After(2 * time.Second):
			t.Fatalf("unrecorded %s artifact was not cleaned", name)
		}
	}
	select {
	case deleted := <-owner.deleted:
		if deleted.chatID != "record-deadline-chat" ||
			deleted.messageID != "unrecorded-placeholder" {
			t.Fatalf("deleted placeholder = %#v", deleted)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("unrecorded placeholder was not deleted")
	}
	for name, entries := range map[string]*sync.Map{
		"typing":      &manager.typingStops,
		"reaction":    &manager.reactionUndos,
		"placeholder": &manager.placeholders,
	} {
		if _, loaded := entries.Load("test:record-deadline-chat"); loaded {
			t.Fatalf("canceled registration left %s state", name)
		}
	}

	release()
	select {
	case <-holderDone:
	case <-time.After(2 * time.Second):
		t.Fatal("blocking provider transition did not finish")
	}
}

func TestSendPlaceholderDeadlineDeletesUnrecordedMessage(t *testing.T) {
	manager := newTestManager()
	owner := newTurnUXDeadlineChannel()
	manager.channels["test"] = owner
	key := "test:placeholder-deadline-chat"
	unlockTransition, acquired := manager.lockTurnUXTransition(
		context.Background(),
		key,
	)
	if !acquired {
		t.Fatal("failed to acquire blocking provider transition")
	}
	defer unlockTransition()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	if recorded := manager.SendPlaceholderForMessage(
		ctx,
		"test",
		"placeholder-deadline-chat",
		"placeholder-deadline-turn",
	); recorded {
		t.Fatal("deadline-expired placeholder reported successful recording")
	}
	select {
	case deleted := <-owner.deleted:
		if deleted.chatID != "placeholder-deadline-chat" ||
			deleted.messageID != "mock-ph-123" {
			t.Fatalf("deleted placeholder = %#v", deleted)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("unrecorded provider placeholder was not deleted")
	}
	if _, loaded := manager.placeholders.Load(key); loaded {
		t.Fatal("deadline-expired placeholder remained recorded")
	}
}

func TestExactTurnUXCleanupDeadlineDetachesBeforeAsyncCleanup(t *testing.T) {
	for _, testCase := range []struct {
		name   string
		invoke func(
			*Manager,
			func(context.Context),
			context.Context,
		)
	}{
		{
			name: "cleanup",
			invoke: func(
				manager *Manager,
				_ func(context.Context),
				ctx context.Context,
			) {
				manager.CleanupTurnUXForMessage(
					ctx,
					"test",
					"exact-deadline-chat",
					"exact-deadline-turn",
				)
			},
		},
		{
			name: "rollback",
			invoke: func(
				_ *Manager,
				rollback func(context.Context),
				ctx context.Context,
			) {
				rollback(ctx)
			},
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			manager := newTestManager()
			owner := newTurnUXDeadlineChannel()
			manager.channels["test"] = owner
			typingCleaned := make(chan struct{}, 1)
			reactionCleaned := make(chan struct{}, 1)
			rollback := manager.RecordTurnUX(
				context.Background(),
				"test",
				"exact-deadline-chat",
				TurnUXRegistration{
					Identity: "exact-deadline-turn",
					TypingStop: func() {
						typingCleaned <- struct{}{}
					},
					ReactionUndo: func() {
						reactionCleaned <- struct{}{}
					},
					Placeholder: "exact-deadline-placeholder",
					Owner:       owner,
				},
			)
			if rollback == nil {
				t.Fatal("registration did not return a rollback")
			}

			key := "test:exact-deadline-chat"
			unlockTransition, acquired := manager.lockTurnUXTransition(
				context.Background(),
				key,
			)
			if !acquired {
				t.Fatal("failed to acquire blocking provider transition")
			}
			defer unlockTransition()

			ctx, cancel := context.WithTimeout(
				context.Background(),
				30*time.Millisecond,
			)
			defer cancel()
			testCase.invoke(manager, rollback, ctx)

			for name, cleaned := range map[string]<-chan struct{}{
				"typing":   typingCleaned,
				"reaction": reactionCleaned,
			} {
				select {
				case <-cleaned:
				case <-time.After(2 * time.Second):
					t.Fatalf("detached %s artifact was not cleaned", name)
				}
			}
			select {
			case deleted := <-owner.deleted:
				if deleted.chatID != "exact-deadline-chat" ||
					deleted.messageID != "exact-deadline-placeholder" {
					t.Fatalf("deleted placeholder = %#v", deleted)
				}
			case <-time.After(2 * time.Second):
				t.Fatal("detached placeholder was not deleted")
			}
			for name, entries := range map[string]*sync.Map{
				"typing":      &manager.typingStops,
				"reaction":    &manager.reactionUndos,
				"placeholder": &manager.placeholders,
			} {
				if _, loaded := entries.Load(key); loaded {
					t.Fatalf("deadline fallback left %s state", name)
				}
			}
		})
	}
}

func TestRecordTypingStopReplacementSerializesProviderTransition(t *testing.T) {
	manager := newTestManager()
	cleanupStarted := make(chan struct{})
	releaseCleanup := make(chan struct{})
	manager.RecordTypingStop("test", "chat", func() {
		close(cleanupStarted)
		<-releaseCleanup
	})

	firstDone := make(chan struct{})
	go func() {
		defer close(firstDone)
		manager.RecordTypingStop("test", "chat", func() {})
	}()
	<-cleanupStarted

	secondDone := make(chan struct{})
	go func() {
		defer close(secondDone)
		manager.RecordTypingStop("test", "chat", func() {})
	}()
	select {
	case <-secondDone:
		close(releaseCleanup)
		t.Fatal("new typing generation bypassed the active provider transition")
	case <-time.After(100 * time.Millisecond):
	}
	close(releaseCleanup)
	<-firstDone
	<-secondDone
}

func TestPreSendOlderMessagePreservesNewerTurnUX(t *testing.T) {
	manager := newTestManager()
	var stops, undos, edits int
	channel := &mockMessageEditor{
		editFn: func(_ context.Context, _, messageID, _ string) error {
			if messageID != "new-placeholder" {
				t.Fatalf("edited placeholder %q, want new-placeholder", messageID)
			}
			edits++
			return nil
		},
	}
	manager.channels["test"] = channel
	manager.RecordTurnUX(
		context.Background(),
		"test",
		"chat",
		TurnUXRegistration{
			Identity:     "new-turn",
			TypingStop:   func() { stops++ },
			ReactionUndo: func() { undos++ },
			Placeholder:  "new-placeholder",
			Owner:        channel,
		},
	)

	oldOutbound := testOutboundMessage(bus.OutboundMessage{
		Channel: "test",
		ChatID:  "chat",
		Content: "old response",
		Context: bus.InboundContext{
			Channel:  "test",
			ChatID:   "chat",
			TurnUXID: "old-turn",
		},
	})
	if _, handled := manager.preSend(
		context.Background(),
		"test",
		oldOutbound,
		channel,
	); handled {
		t.Fatal("old outbound unexpectedly edited newer placeholder")
	}
	if stops != 0 || undos != 0 || edits != 0 {
		t.Fatalf(
			"old outbound cleaned newer UX: stops=%d undos=%d edits=%d",
			stops,
			undos,
			edits,
		)
	}
	if _, ok := manager.typingStops.Load("test:chat"); !ok {
		t.Fatal("old outbound removed newer typing registration")
	}
	if _, ok := manager.reactionUndos.Load("test:chat"); !ok {
		t.Fatal("old outbound removed newer reaction registration")
	}
	if _, ok := manager.placeholders.Load("test:chat"); !ok {
		t.Fatal("old outbound removed newer placeholder registration")
	}

	newOutbound := oldOutbound
	newOutbound.Content = "new response"
	newOutbound.Context.TurnUXID = "new-turn"
	if _, handled := manager.preSend(
		context.Background(),
		"test",
		newOutbound,
		channel,
	); !handled {
		t.Fatal("matching outbound did not edit its placeholder")
	}
	if stops != 1 || undos != 1 || edits != 1 {
		t.Fatalf(
			"matching outbound cleanup = stops:%d undos:%d edits:%d, want 1/1/1",
			stops,
			undos,
			edits,
		)
	}
}

func TestRebindTurnUXForMessageUpdatesWholeRegistration(t *testing.T) {
	manager := newTestManager()
	var stops, undos, edits int
	channel := &mockMessageEditor{
		editFn: func(_ context.Context, _, _, _ string) error {
			edits++
			return nil
		},
	}
	manager.RecordTurnUX(
		context.Background(),
		"test",
		"chat",
		TurnUXRegistration{
			Identity:     "steering-turn",
			TypingStop:   func() { stops++ },
			ReactionUndo: func() { undos++ },
			Placeholder:  "steering-placeholder",
			Owner:        channel,
		},
	)

	manager.RebindTurnUXForMessage(
		"test",
		"chat",
		"steering-turn",
		"active-turn",
	)
	outbound := testOutboundMessage(bus.OutboundMessage{
		Channel: "test",
		ChatID:  "chat",
		Content: "combined response",
		Context: bus.InboundContext{
			Channel:  "test",
			ChatID:   "chat",
			TurnUXID: "active-turn",
		},
	})
	if _, handled := manager.preSend(
		context.Background(),
		"test",
		outbound,
		channel,
	); !handled {
		t.Fatal("active turn did not consume rebound placeholder")
	}
	if stops != 1 || undos != 1 || edits != 1 {
		t.Fatalf(
			"rebound cleanup = stops:%d undos:%d edits:%d, want 1/1/1",
			stops,
			undos,
			edits,
		)
	}
}

func TestRecordTurnUXRollbackUsesCreatingOwnerAcrossReload(t *testing.T) {
	manager := newTestManager()
	creatingOwner := &mockDeletingMediaChannel{}
	replacementOwner := &mockDeletingMediaChannel{}
	manager.channels["test"] = replacementOwner

	rollback := manager.RecordTurnUX(
		context.Background(),
		"test",
		"chat",
		TurnUXRegistration{
			Placeholder: "placeholder-from-old-generation",
			Owner:       creatingOwner,
		},
	)
	rollback(context.Background())

	if creatingOwner.deleteCalls != 1 {
		t.Fatalf("creating owner delete calls = %d, want 1", creatingOwner.deleteCalls)
	}
	if replacementOwner.deleteCalls != 0 {
		t.Fatalf("replacement owner delete calls = %d, want 0", replacementOwner.deleteCalls)
	}
}

func TestDeleteTurnUXEntryIfCurrentPreservesReplacement(t *testing.T) {
	var entries sync.Map
	key := "test:chat"
	stale := &typingEntry{createdAt: time.Now().Add(-typingStopTTL)}
	replacement := &typingEntry{createdAt: time.Now()}
	entries.Store(key, stale)
	entries.Store(key, replacement)

	if deleteTurnUXEntryIfCurrent(&entries, key, stale) {
		t.Fatal("stale cleanup deleted a replacement entry")
	}
	got, ok := entries.Load(key)
	if !ok || got != replacement {
		t.Fatalf("stored entry = %#v, want replacement pointer", got)
	}
}

func TestRecordTypingStop_ConcurrentSafe(t *testing.T) {
	m := newTestManager()

	var wg sync.WaitGroup
	for i := range 100 {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			chatID := fmt.Sprintf("chat_%d", i%10)
			m.RecordTypingStop("test", chatID, func() {})
		}(i)
	}
	wg.Wait()
}

func TestRecordTypingStop_ReplacesExistingStop(t *testing.T) {
	m := newTestManager()
	var oldStopCalls int
	var newStopCalls int

	m.RecordTypingStop("test", "123", func() {
		oldStopCalls++
	})

	m.RecordTypingStop("test", "123", func() {
		newStopCalls++
	})

	if oldStopCalls != 1 {
		t.Fatalf("expected previous typing stop to be called once when replaced, got %d", oldStopCalls)
	}
	if newStopCalls != 0 {
		t.Fatalf("expected replacement typing stop to stay active until preSend, got %d calls", newStopCalls)
	}

	msg := testOutboundMessage(bus.OutboundMessage{Channel: "test", ChatID: "123", Content: "hello"})
	m.preSend(context.Background(), "test", msg, &mockChannel{})

	if newStopCalls != 1 {
		t.Fatalf("expected replacement typing stop to be called by preSend, got %d", newStopCalls)
	}
	if oldStopCalls != 1 {
		t.Fatalf("expected previous typing stop to not be called again, got %d", oldStopCalls)
	}
}

func TestSendWithRetry_PreSendEditsPlaceholder(t *testing.T) {
	m := newTestManager()
	var sendCalled bool

	ch := &mockMessageEditor{
		mockChannel: mockChannel{
			sendFn: func(_ context.Context, _ bus.OutboundMessage) error {
				sendCalled = true
				return nil
			},
		},
		editFn: func(_ context.Context, _, _, _ string) error {
			return nil // edit succeeds
		},
	}

	m.RecordPlaceholder("test", "123", "456")

	w := &channelWorker{
		ch:      ch,
		limiter: rate.NewLimiter(rate.Inf, 1),
	}

	msg := testOutboundMessage(bus.OutboundMessage{Channel: "test", ChatID: "123", Content: "hello"})
	m.sendWithRetry(context.Background(), "test", w, msg)

	if sendCalled {
		t.Fatal("expected Send to NOT be called when placeholder was edited")
	}
}

// --- Dispatcher exit tests (Step 1) ---

func TestDispatcherExitsOnCancel(t *testing.T) {
	mb := bus.NewMessageBus()
	defer mb.Close()

	m := &Manager{
		channels: make(map[string]Channel),
		workers:  make(map[string]*channelWorker),
		bus:      mb,
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})

	go func() {
		m.dispatchOutbound(ctx)
		close(done)
	}()

	// Cancel context and verify the dispatcher exits quickly
	cancel()

	select {
	case <-done:
		// success
	case <-time.After(2 * time.Second):
		t.Fatal("dispatchOutbound did not exit within 2s after context cancel")
	}
}

func TestDispatcherMediaExitsOnCancel(t *testing.T) {
	mb := bus.NewMessageBus()
	defer mb.Close()

	m := &Manager{
		channels: make(map[string]Channel),
		workers:  make(map[string]*channelWorker),
		bus:      mb,
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})

	go func() {
		m.dispatchOutboundMedia(ctx)
		close(done)
	}()

	cancel()

	select {
	case <-done:
		// success
	case <-time.After(2 * time.Second):
		t.Fatal("dispatchOutboundMedia did not exit within 2s after context cancel")
	}
}

// --- TTL Janitor tests (Step 2) ---

func TestRunBoundedTurnUXCallbackReturnsAtDeadline(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	start := time.Now()
	runBoundedTurnUXCallback(ctx, "test", "chat", "typing", func() {
		close(started)
		<-release
	})
	elapsed := time.Since(start)

	select {
	case <-started:
	default:
		t.Fatal("cleanup callback did not start")
	}
	if elapsed > time.Second {
		t.Fatalf("bounded callback returned after %v, want under 1s", elapsed)
	}
	close(release)
}

func TestEvictExpiredTurnUXContainsPanicsAndDeletesPlaceholder(t *testing.T) {
	m := newTestManager()
	channel := &mockDeletingMediaChannel{}
	var undoCalled atomic.Bool
	expiredAt := time.Now().Add(-20 * time.Minute)

	m.typingStops.Store("test:chat", &typingEntry{
		stop:      func() { panic("provider panic") },
		createdAt: expiredAt,
	})
	m.reactionUndos.Store("test:chat", &reactionEntry{
		undo:      func() { undoCalled.Store(true) },
		createdAt: expiredAt,
	})
	m.placeholders.Store("test:chat", &placeholderEntry{
		id:        "old-placeholder",
		createdAt: expiredAt,
		owner:     channel,
	})

	m.evictExpiredTurnUX(time.Now())

	if !undoCalled.Load() {
		t.Fatal("reaction cleanup did not run after a panicking typing callback")
	}
	if channel.deleteCalls != 1 ||
		channel.lastDeleted.chatID != "chat" ||
		channel.lastDeleted.messageID != "old-placeholder" {
		t.Fatalf(
			"placeholder cleanup = calls:%d chat:%q message:%q, want 1/chat/old-placeholder",
			channel.deleteCalls,
			channel.lastDeleted.chatID,
			channel.lastDeleted.messageID,
		)
	}
	for name, entries := range map[string]*sync.Map{
		"typing":      &m.typingStops,
		"reaction":    &m.reactionUndos,
		"placeholder": &m.placeholders,
	} {
		if _, loaded := entries.Load("test:chat"); loaded {
			t.Fatalf("expired %s entry was not deleted", name)
		}
	}
}

func TestTypingStopJanitorEviction(t *testing.T) {
	m := newTestManager()

	var stopCalled atomic.Bool
	// Store a typing entry with a creation time far in the past
	m.typingStops.Store("test:123", &typingEntry{
		stop:      func() { stopCalled.Store(true) },
		createdAt: time.Now().Add(-10 * time.Minute), // well past typingStopTTL
	})

	// Run janitor with a short-lived context
	ctx, cancel := context.WithCancel(context.Background())

	// Manually trigger the janitor logic once by simulating a tick
	go func() {
		// Override janitor to run immediately
		now := time.Now()
		m.typingStops.Range(func(key, value any) bool {
			if entry, ok := value.(*typingEntry); ok {
				if now.Sub(entry.createdAt) > typingStopTTL {
					if m.typingStops.CompareAndDelete(key, value) {
						entry.stop()
					}
				}
			}
			return true
		})
		cancel()
	}()

	<-ctx.Done()

	if !stopCalled.Load() {
		t.Fatal("expected typing stop function to be called by janitor eviction")
	}

	// Verify entry was deleted
	if _, loaded := m.typingStops.Load("test:123"); loaded {
		t.Fatal("expected typing entry to be deleted after eviction")
	}
}

func TestPlaceholderJanitorEviction(t *testing.T) {
	m := newTestManager()

	// Store a placeholder entry with a creation time far in the past
	m.placeholders.Store("test:456", &placeholderEntry{
		id:        "msg_old",
		createdAt: time.Now().Add(-20 * time.Minute), // well past placeholderTTL
	})

	// Simulate janitor logic
	now := time.Now()
	m.placeholders.Range(func(key, value any) bool {
		if entry, ok := value.(*placeholderEntry); ok {
			if now.Sub(entry.createdAt) > placeholderTTL {
				m.placeholders.CompareAndDelete(key, value)
			}
		}
		return true
	})

	// Verify entry was deleted
	if _, loaded := m.placeholders.Load("test:456"); loaded {
		t.Fatal("expected placeholder entry to be deleted after eviction")
	}
}

func TestPreSendStillWorksWithWrappedTypes(t *testing.T) {
	m := newTestManager()
	var stopCalled bool
	var editCalled bool

	ch := &mockMessageEditor{
		mockChannel: mockChannel{
			sendFn: func(_ context.Context, _ bus.OutboundMessage) error {
				return nil
			},
		},
		editFn: func(_ context.Context, chatID, messageID, content string) error {
			editCalled = true
			if messageID != "ph_id" {
				t.Fatalf("expected messageID ph_id, got %s", messageID)
			}
			return nil
		},
	}

	// Use the new wrapped types via the public API
	m.RecordTypingStop("test", "chat1", func() {
		stopCalled = true
	})
	m.RecordPlaceholder("test", "chat1", "ph_id")

	msg := testOutboundMessage(bus.OutboundMessage{Channel: "test", ChatID: "chat1", Content: "response"})
	_, edited := m.preSend(context.Background(), "test", msg, ch)

	if !stopCalled {
		t.Fatal("expected typing stop to be called via wrapped type")
	}
	if !editCalled {
		t.Fatal("expected EditMessage to be called via wrapped type")
	}
	if !edited {
		t.Fatal("expected preSend to return true")
	}
}

// --- Lazy worker creation tests (Step 6) ---

func TestLazyWorkerCreation(t *testing.T) {
	m := newTestManager()

	ch := &mockChannel{
		sendFn: func(_ context.Context, _ bus.OutboundMessage) error {
			return nil
		},
	}

	// RegisterChannel should NOT create a worker
	m.RegisterChannel("lazy", ch)

	m.mu.RLock()
	_, chExists := m.channels["lazy"]
	_, wExists := m.workers["lazy"]
	m.mu.RUnlock()

	if !chExists {
		t.Fatal("expected channel to be registered")
	}
	if wExists {
		t.Fatal("expected worker to NOT be created by RegisterChannel (lazy creation)")
	}
}

// --- FastID uniqueness test (Step 5) ---

func TestBuildMediaScope_FastIDUniqueness(t *testing.T) {
	seen := make(map[string]bool)

	for range 1000 {
		scope := BuildMediaScope("test", "chat1", "")
		if seen[scope] {
			t.Fatalf("duplicate scope generated: %s", scope)
		}
		seen[scope] = true
	}

	// Verify format: "channel:chatID:id"
	scope := BuildMediaScope("telegram", "42", "")
	parts := 0
	for _, c := range scope {
		if c == ':' {
			parts++
		}
	}
	if parts != 2 {
		t.Fatalf("expected scope to have 2 colons (channel:chatID:id), got: %s", scope)
	}
}

func TestBuildMediaScope_WithMessageID(t *testing.T) {
	scope := BuildMediaScope("discord", "chat99", "msg123")
	expected := "discord:chat99:msg123"
	if scope != expected {
		t.Fatalf("expected %s, got %s", expected, scope)
	}
}

func TestManager_PlaceholderConsumedByResponse(t *testing.T) {
	mgr := &Manager{
		channels:     make(map[string]Channel),
		workers:      make(map[string]*channelWorker),
		placeholders: sync.Map{},
	}

	mockCh := &mockChannel{
		sendFn: func(ctx context.Context, msg bus.OutboundMessage) error {
			return nil
		},
	}
	worker := newChannelWorker("mock", mockCh, "mock")
	mgr.channels["mock"] = mockCh
	mgr.workers["mock"] = worker

	ctx := context.Background()
	key := "mock:chat-1"

	// Simulate a placeholder recorded by base.go HandleMessage
	mgr.RecordPlaceholder("mock", "chat-1", "ph-123")

	if _, ok := mgr.placeholders.Load(key); !ok {
		t.Fatal("expected placeholder to be recorded")
	}

	// Transcription feedback arrives first — it should consume the placeholder
	// and be delivered via EditMessage, not Send.
	msgTranscript := testOutboundMessage(bus.OutboundMessage{
		Channel: "mock",
		ChatID:  "chat-1",
		Content: "Transcript: hello",
	})
	mgr.sendWithRetry(ctx, "mock", worker, msgTranscript)

	if mockCh.editedMessages != 1 {
		t.Errorf("expected 1 edited message (placeholder consumed by transcript), got %d", mockCh.editedMessages)
	}
	if len(mockCh.sentMessages) != 0 {
		t.Errorf("expected 0 normal messages (transcript used edit), got %d", len(mockCh.sentMessages))
	}

	// Placeholder should be gone now
	if _, ok := mgr.placeholders.Load(key); ok {
		t.Error("expected placeholder to be removed after being consumed")
	}

	// Final LLM response arrives — no placeholder left, so it goes through Send
	msgFinal := testOutboundMessage(bus.OutboundMessage{
		Channel: "mock",
		ChatID:  "chat-1",
		Content: "Final Answer",
	})
	mgr.sendWithRetry(ctx, "mock", worker, msgFinal)

	if len(mockCh.sentMessages) != 1 {
		t.Errorf("expected 1 normal message sent, got %d", len(mockCh.sentMessages))
	}
}

func TestSendMessage_Synchronous(t *testing.T) {
	m := newTestManager()

	var received []bus.OutboundMessage
	ch := &mockChannel{
		sendFn: func(_ context.Context, msg bus.OutboundMessage) error {
			received = append(received, msg)
			return nil
		},
	}

	w := &channelWorker{
		ch:      ch,
		limiter: rate.NewLimiter(rate.Inf, 1),
	}
	m.channels["test"] = ch
	m.workers["test"] = w

	msg := testOutboundMessage(bus.OutboundMessage{
		Channel:          "test",
		ChatID:           "123",
		Content:          "hello world",
		ReplyToMessageID: "msg-456",
	})

	err := m.SendMessage(context.Background(), msg)
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	// SendMessage is synchronous — message should already be delivered
	if len(received) != 1 {
		t.Fatalf("expected 1 message sent, got %d", len(received))
	}
	if received[0].ReplyToMessageID != "msg-456" {
		t.Fatalf("expected ReplyToMessageID msg-456, got %s", received[0].ReplyToMessageID)
	}
	if received[0].Content != "hello world" {
		t.Fatalf("expected content 'hello world', got %s", received[0].Content)
	}
}

func TestSendMessage_UnknownChannel(t *testing.T) {
	m := newTestManager()

	msg := testOutboundMessage(bus.OutboundMessage{
		Channel: "nonexistent",
		ChatID:  "123",
		Content: "hello",
	})

	err := m.SendMessage(context.Background(), msg)
	if err == nil {
		t.Fatal("expected error for unknown channel")
	}
}

func TestSendMessage_NoWorker(t *testing.T) {
	m := newTestManager()

	ch := &mockChannel{
		sendFn: func(_ context.Context, _ bus.OutboundMessage) error { return nil },
	}
	m.channels["test"] = ch
	// No worker registered

	msg := testOutboundMessage(bus.OutboundMessage{
		Channel: "test",
		ChatID:  "123",
		Content: "hello",
	})

	err := m.SendMessage(context.Background(), msg)
	if err == nil {
		t.Fatal("expected error when no worker exists")
	}
}

func TestSendMessage_WithRetry(t *testing.T) {
	m := newTestManager()

	var callCount int
	ch := &mockChannel{
		sendFn: func(_ context.Context, _ bus.OutboundMessage) error {
			callCount++
			if callCount == 1 {
				return fmt.Errorf("transient: %w", ErrTemporary)
			}
			return nil
		},
	}

	w := &channelWorker{
		ch:      ch,
		limiter: rate.NewLimiter(rate.Inf, 1),
	}
	m.channels["test"] = ch
	m.workers["test"] = w

	msg := testOutboundMessage(bus.OutboundMessage{
		Channel: "test",
		ChatID:  "123",
		Content: "retry me",
	})

	err := m.SendMessage(context.Background(), msg)
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	if callCount != 2 {
		t.Fatalf("expected 2 Send calls (1 failure + 1 success), got %d", callCount)
	}
}

func TestSendMessage_ContextOnlyUsesContextAddressing(t *testing.T) {
	m := newTestManager()

	var received []bus.OutboundMessage
	ch := &mockChannel{
		sendFn: func(_ context.Context, msg bus.OutboundMessage) error {
			received = append(received, msg)
			return nil
		},
	}

	w := &channelWorker{
		ch:      ch,
		limiter: rate.NewLimiter(rate.Inf, 1),
	}
	m.channels["test"] = ch
	m.workers["test"] = w

	msg := testOutboundMessage(bus.OutboundMessage{
		Context: bus.NewOutboundContext("test", "123", "msg-9"),
		Content: "hello",
	})

	if err := m.SendMessage(context.Background(), msg); err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if len(received) != 1 {
		t.Fatalf("expected 1 message sent, got %d", len(received))
	}
	if received[0].Channel != "test" || received[0].ChatID != "123" {
		t.Fatalf("expected mirrored legacy address, got %+v", received[0])
	}
	if received[0].Context.Channel != "test" || received[0].Context.ChatID != "123" {
		t.Fatalf("expected context address to be preserved, got %+v", received[0].Context)
	}
	if received[0].ReplyToMessageID != "msg-9" {
		t.Fatalf("expected reply_to_message_id msg-9, got %q", received[0].ReplyToMessageID)
	}
}

func TestSendMessage_WithSplitting(t *testing.T) {
	m := newTestManager()

	var received []string
	ch := &mockChannelWithLength{
		mockChannel: mockChannel{
			sendFn: func(_ context.Context, msg bus.OutboundMessage) error {
				received = append(received, msg.Content)
				return nil
			},
		},
		maxLen: 5,
	}

	w := &channelWorker{
		ch:      ch,
		limiter: rate.NewLimiter(rate.Inf, 1),
	}
	m.channels["test"] = ch
	m.workers["test"] = w

	msg := testOutboundMessage(bus.OutboundMessage{
		Channel: "test",
		ChatID:  "123",
		Content: "hello world",
	})

	err := m.SendMessage(context.Background(), msg)
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	if len(received) < 2 {
		t.Fatalf("expected message to be split into at least 2 chunks, got %d", len(received))
	}
}

func TestSendMedia_ContextOnlyUsesContextAddressing(t *testing.T) {
	m := newTestManager()

	var received []bus.OutboundMediaMessage
	ch := &mockMediaChannel{
		sendMediaFn: func(_ context.Context, msg bus.OutboundMediaMessage) ([]string, error) {
			received = append(received, msg)
			return nil, nil
		},
	}

	w := &channelWorker{
		ch:      ch,
		limiter: rate.NewLimiter(rate.Inf, 1),
	}
	m.channels["test"] = ch
	m.workers["test"] = w

	msg := testOutboundMediaMessage(bus.OutboundMediaMessage{
		Context: bus.NewOutboundContext("test", "media-chat", ""),
		Parts:   []bus.MediaPart{{Type: "image", Ref: "media://1"}},
	})

	if err := m.SendMedia(context.Background(), msg); err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if len(received) != 1 {
		t.Fatalf("expected 1 media message sent, got %d", len(received))
	}
	if received[0].Channel != "test" || received[0].ChatID != "media-chat" {
		t.Fatalf("expected mirrored legacy media address, got %+v", received[0])
	}
	if received[0].Context.Channel != "test" || received[0].Context.ChatID != "media-chat" {
		t.Fatalf("expected media context address to be preserved, got %+v", received[0].Context)
	}
}

func TestSendMessage_PreservesOrdering(t *testing.T) {
	m := newTestManager()

	var order []string
	ch := &mockChannel{
		sendFn: func(_ context.Context, msg bus.OutboundMessage) error {
			order = append(order, msg.Content)
			return nil
		},
	}

	w := &channelWorker{
		ch:      ch,
		limiter: rate.NewLimiter(rate.Inf, 1),
	}
	m.channels["test"] = ch
	m.workers["test"] = w

	// Send two messages sequentially — they must arrive in order
	_ = m.SendMessage(context.Background(), testOutboundMessage(bus.OutboundMessage{
		Channel: "test", ChatID: "1", Content: "first",
	}))
	_ = m.SendMessage(context.Background(), testOutboundMessage(bus.OutboundMessage{
		Channel: "test", ChatID: "1", Content: "second",
	}))

	if len(order) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(order))
	}
	if order[0] != "first" || order[1] != "second" {
		t.Fatalf("expected [first, second], got %v", order)
	}
}

func TestManager_SendPlaceholder(t *testing.T) {
	mgr := &Manager{
		channels:     make(map[string]Channel),
		workers:      make(map[string]*channelWorker),
		placeholders: sync.Map{},
	}

	mockCh := &mockChannel{
		sendFn: func(ctx context.Context, msg bus.OutboundMessage) error {
			return nil
		},
	}
	mgr.channels["mock"] = mockCh

	ctx := context.Background()

	// SendPlaceholder should send a placeholder and record it
	ok := mgr.SendPlaceholder(ctx, "mock", "chat-1")
	if !ok {
		t.Fatal("expected SendPlaceholder to succeed")
	}
	if mockCh.placeholdersSent != 1 {
		t.Errorf("expected 1 placeholder sent, got %d", mockCh.placeholdersSent)
	}

	key := "mock:chat-1"
	if _, loaded := mgr.placeholders.Load(key); !loaded {
		t.Error("expected placeholder to be recorded in manager")
	}

	// SendPlaceholder on unknown channel should return false
	ok = mgr.SendPlaceholder(ctx, "unknown", "chat-1")
	if ok {
		t.Error("expected SendPlaceholder to fail for unknown channel")
	}
}

// turnUsageTrackingStreamer is a mockStreamer that records SetTurnUsage calls,
// used to verify the manager's streamer wrappers forward per-turn token usage
// to the inner streamer (regression: the wrappers previously dropped it because
// SetTurnUsage is not part of the bus.Streamer interface).
type turnUsageTrackingStreamer struct {
	mockStreamer
	inputTokens  int
	outputTokens int
	usageCalls   int
}

func (m *turnUsageTrackingStreamer) SetTurnUsage(inputTokens, outputTokens int) {
	m.usageCalls++
	m.inputTokens = inputTokens
	m.outputTokens = outputTokens
}

func TestFinalizeHookStreamerForwardsTurnUsage(t *testing.T) {
	inner := &turnUsageTrackingStreamer{}
	wrapper := &finalizeHookStreamer{Streamer: inner}

	setter, ok := any(wrapper).(turnUsageStreamer)
	if !ok {
		t.Fatal("finalizeHookStreamer does not satisfy turnUsageStreamer")
	}
	setter.SetTurnUsage(1234, 567)

	if inner.usageCalls != 1 {
		t.Fatalf("inner SetTurnUsage calls = %d, want 1", inner.usageCalls)
	}
	if inner.inputTokens != 1234 || inner.outputTokens != 567 {
		t.Errorf("inner usage = (%d, %d), want (1234, 567)", inner.inputTokens, inner.outputTokens)
	}
}

func TestSplitMarkerStreamerForwardsTurnUsage(t *testing.T) {
	inner := &turnUsageTrackingStreamer{}
	wrapper := &splitMarkerStreamer{current: inner}

	setter, ok := any(wrapper).(turnUsageStreamer)
	if !ok {
		t.Fatal("splitMarkerStreamer does not satisfy turnUsageStreamer")
	}
	setter.SetTurnUsage(1234, 567)

	if inner.usageCalls != 1 {
		t.Fatalf("inner SetTurnUsage calls = %d, want 1", inner.usageCalls)
	}
	if inner.inputTokens != 1234 || inner.outputTokens != 567 {
		t.Errorf("inner usage = (%d, %d), want (1234, 567)", inner.inputTokens, inner.outputTokens)
	}
}
