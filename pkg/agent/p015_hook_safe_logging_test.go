package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	runtimeevents "github.com/sipeed/picoclaw/pkg/events"
	"github.com/sipeed/picoclaw/pkg/logger"
)

const (
	p015HookNameCanary   = "P015_HOOK_NAME_8ad0b765"
	p015HookStageCanary  = "P015_HOOK_STAGE_552f7283"
	p015HookActionCanary = "P015_HOOK_ACTION_a242a1d1"
	p015HookEventCanary  = "P015_HOOK_EVENT_73cc7c1e"
	p015HookErrorCanary  = "P015_HOOK_ERROR_f960e9c7"
	p015HookStderrCanary = "P015_HOOK_STDERR_53aac47e"
	p015HookRPCcanary    = "P015_HOOK_RPC_30042cf1"
)

type p015HostileHookError struct {
	calls  *atomic.Int64
	secret string
}

func (err *p015HostileHookError) invoked() {
	err.calls.Add(1)
	panic(err.secret)
}

func (err *p015HostileHookError) Error() string {
	err.invoked()
	return ""
}

func (err *p015HostileHookError) Unwrap() error {
	err.invoked()
	return nil
}

func (err *p015HostileHookError) Is(error) bool {
	err.invoked()
	return false
}

func (err *p015HostileHookError) As(any) bool {
	err.invoked()
	return false
}

type p015ErrorEventChannel struct {
	err error
}

func (channel *p015ErrorEventChannel) Filter(runtimeevents.Filter) runtimeevents.EventChannel {
	return channel
}

func (channel *p015ErrorEventChannel) OfKind(...runtimeevents.Kind) runtimeevents.EventChannel {
	return channel
}

func (channel *p015ErrorEventChannel) KindPrefix(string) runtimeevents.EventChannel {
	return channel
}

func (channel *p015ErrorEventChannel) Source(string, ...string) runtimeevents.EventChannel {
	return channel
}

func (channel *p015ErrorEventChannel) Scope(runtimeevents.ScopeFilter) runtimeevents.EventChannel {
	return channel
}

func (channel *p015ErrorEventChannel) Subscribe(
	context.Context,
	runtimeevents.SubscribeOptions,
	runtimeevents.Handler,
) (runtimeevents.Subscription, error) {
	return nil, channel.err
}

func (channel *p015ErrorEventChannel) SubscribeChan(
	context.Context,
	runtimeevents.SubscribeOptions,
) (runtimeevents.Subscription, <-chan runtimeevents.Event, error) {
	return nil, nil, channel.err
}

func (channel *p015ErrorEventChannel) SubscribeOnce(
	context.Context,
	runtimeevents.SubscribeOptions,
	runtimeevents.Handler,
) (runtimeevents.Subscription, error) {
	return nil, channel.err
}

type p015ErrorSubscription struct {
	err  error
	done chan struct{}
}

func (subscription *p015ErrorSubscription) ID() uint64 {
	return 1
}

func (subscription *p015ErrorSubscription) Name() string {
	return "p015-subscription"
}

func (subscription *p015ErrorSubscription) Close() error {
	return subscription.err
}

func (subscription *p015ErrorSubscription) Done() <-chan struct{} {
	return subscription.done
}

func (subscription *p015ErrorSubscription) Stats() runtimeevents.SubscriberStats {
	return runtimeevents.SubscriberStats{}
}

type p015ReentrantHookSubscription struct {
	calls   atomic.Int64
	done    chan struct{}
	reenter func()
}

func (*p015ReentrantHookSubscription) ID() uint64 {
	return 2
}

func (*p015ReentrantHookSubscription) Name() string {
	return "p015-reentrant-subscription"
}

func (subscription *p015ReentrantHookSubscription) Close() error {
	subscription.calls.Add(1)
	if subscription.reenter != nil {
		subscription.reenter()
	}
	return nil
}

func (subscription *p015ReentrantHookSubscription) Done() <-chan struct{} {
	return subscription.done
}

func (*p015ReentrantHookSubscription) Stats() runtimeevents.SubscriberStats {
	return runtimeevents.SubscriberStats{}
}

type p015ErrorObserver struct {
	err error
}

func (observer p015ErrorObserver) OnRuntimeEvent(
	context.Context,
	runtimeevents.Event,
) error {
	return observer.err
}

type p015ErrorCloser struct {
	err error
}

func (closer p015ErrorCloser) Close() error {
	return closer.err
}

type p015ReentrantHookCloser struct {
	calls   atomic.Int64
	reenter func()
}

func (closer *p015ReentrantHookCloser) Close() error {
	closer.calls.Add(1)
	if closer.reenter != nil {
		closer.reenter()
	}
	return nil
}

func TestP015HookArbitraryErrorMethodsAreNeverCalled(t *testing.T) {
	var calls atomic.Int64
	hostile := &p015HostileHookError{calls: &calls, secret: p015HookErrorCanary}

	records, raw := captureP015HookRecords(t, func() {
		subscribeManager := NewHookManager(&p015ErrorEventChannel{err: hostile})
		subscribeManager.Close()

		closeManager := NewHookManager(nil)
		closeManager.runtimeSub = &p015ErrorSubscription{
			err:  hostile,
			done: closedP015HookChannel(),
		}
		closeManager.Close()

		observerManager := NewHookManager(nil)
		observerManager.runRuntimeObserver(
			p015HookNameCanary,
			p015ErrorObserver{err: hostile},
			runtimeevents.Event{Kind: runtimeevents.Kind(p015HookEventCanary)},
		)
		observerManager.Close()

		_, _, _ = runInterceptorHook(
			context.Background(),
			time.Second,
			p015HookNameCanary,
			p015HookStageCanary,
			func(context.Context) (struct{}, HookDecision, error) {
				return struct{}{}, HookDecision{}, hostile
			},
		)
		_, _ = runApprovalHook(
			context.Background(),
			time.Second,
			p015HookNameCanary,
			p015HookStageCanary,
			func(context.Context) (ApprovalDecision, error) {
				return ApprovalDecision{}, hostile
			},
		)
		closeHookIfPossible(p015ErrorCloser{err: hostile})
	})

	if got := calls.Load(); got != 0 {
		t.Fatalf("hostile error method calls = %d; want 0", got)
	}
	if len(records) != 6 {
		t.Fatalf("safe error record count = %d; want 6; raw=%s", len(records), raw)
	}
	assertP015CanariesAbsent(t, raw,
		p015HookNameCanary,
		p015HookStageCanary,
		p015HookEventCanary,
		p015HookErrorCanary,
	)
	for index, record := range records {
		if record["error_state"] != "complete" || record["error_digest"] == "" {
			t.Fatalf("record %d missing complete error observation: %#v", index, record)
		}
		wantClass := "unknown"
		if index < 2 {
			wantClass = "internal"
		}
		if record["error_class"] != wantClass {
			t.Fatalf("record %d error class = %#v; want %q", index, record["error_class"], wantClass)
		}
	}
}

func TestP015HookProcessBytesNeverPreviewAcrossPolicies(t *testing.T) {
	stderrBytes := append([]byte(p015HookStderrCanary), 0xff, 0xfe)
	malformedBytes := append([]byte(`{"value":`+p015HookRPCcanary), 0xff, 0xfe)

	policies := []logger.DiagnosticPolicy{
		logger.NewDiagnosticPolicy(false, logger.DEBUG),
		logger.NewDiagnosticPolicy(true, logger.DEBUG),
	}
	records, raw := captureP015HookRecords(t, func() {
		for _, policy := range policies {
			ctx, revoke := logger.BindRootDiagnosticPolicy(context.Background(), policy)
			emitP015ProcessHookDiagnostics(ctx, stderrBytes, malformedBytes)
			revoke()
		}
	})

	if len(records) != 4 {
		t.Fatalf("process safe record count = %d; want 4; raw=%s", len(records), raw)
	}
	assertP015CanariesAbsent(t, raw,
		p015HookNameCanary,
		p015HookStderrCanary,
		p015HookRPCcanary,
	)
	for index, record := range records {
		if _, exists := record["sensitive_preview"]; exists {
			t.Fatalf("record %d contains a sensitive preview: %#v", index, record)
		}
		if record["identity_hook_state"] != "complete" ||
			record["identity_hook_digest"] == "" {
			t.Fatalf("record %d missing hook identity observation: %#v", index, record)
		}
	}
	for _, index := range []int{0, 2} {
		record := records[index]
		if record["message"] != "Process hook stderr" ||
			record["process_stderr_utf8_valid"] != false ||
			record["process_stderr_bytes"] != float64(len(stderrBytes)) ||
			record["process_stderr_digest"] == "" {
			t.Fatalf("stderr record %d = %#v", index, record)
		}
	}
	for _, index := range []int{1, 3} {
		record := records[index]
		if record["message"] != "Failed to decode process hook message" ||
			record["hook_message_utf8_valid"] != false ||
			record["hook_message_bytes"] != float64(len(malformedBytes)) ||
			record["hook_message_digest"] == "" ||
			record["error_class"] != "validation" {
			t.Fatalf("decode record %d = %#v", index, record)
		}
	}
	for _, field := range []string{
		"identity_hook_digest",
		"process_stderr_digest",
		"hook_message_digest",
	} {
		left := records[0][field]
		right := records[2][field]
		if field == "hook_message_digest" {
			left = records[1][field]
			right = records[3][field]
		}
		if left != right {
			t.Fatalf("%s changed with diagnostic policy: %#v != %#v", field, left, right)
		}
	}
}

func TestP015HookDynamicFieldsAreObservedAndPolicyIndependent(t *testing.T) {
	var calls atomic.Int64
	hostile := &p015HostileHookError{calls: &calls, secret: p015HookErrorCanary}
	manager := NewHookManager(nil)
	defer manager.Close()

	policies := []logger.DiagnosticPolicy{
		logger.NewDiagnosticPolicy(false, logger.DEBUG),
		logger.NewDiagnosticPolicy(true, logger.DEBUG),
	}
	records, raw := captureP015HookRecords(t, func() {
		for _, policy := range policies {
			ctx, revoke := logger.BindRootDiagnosticPolicy(context.Background(), policy)
			manager.logUntrustedMutation(
				HookRegistration{Name: p015HookNameCanary, Source: HookSource(255)},
				p015HookStageCanary,
				HookAction(p015HookActionCanary),
			)
			manager.runRuntimeObserver(
				p015HookNameCanary,
				p015ErrorObserver{err: hostile},
				runtimeevents.Event{Kind: runtimeevents.Kind(p015HookEventCanary)},
			)
			_, _, _ = runInterceptorHook(
				ctx,
				time.Second,
				p015HookNameCanary,
				p015HookStageCanary,
				func(context.Context) (struct{}, HookDecision, error) {
					return struct{}{}, HookDecision{}, hostile
				},
			)
			revoke()
		}
	})

	if got := calls.Load(); got != 0 {
		t.Fatalf("hostile error method calls = %d; want 0", got)
	}
	if len(records) != 6 {
		t.Fatalf("dynamic safe record count = %d; want 6; raw=%s", len(records), raw)
	}
	assertP015CanariesAbsent(t, raw,
		p015HookNameCanary,
		p015HookStageCanary,
		p015HookActionCanary,
		p015HookEventCanary,
		p015HookErrorCanary,
	)
	for index, record := range records {
		if _, exists := record["sensitive_preview"]; exists {
			t.Fatalf("record %d contains a sensitive preview: %#v", index, record)
		}
		if record["identity_hook_state"] != "complete" ||
			record["identity_hook_digest"] == "" {
			t.Fatalf("record %d missing hook observation: %#v", index, record)
		}
	}
	for offset := 0; offset < 3; offset++ {
		left := records[offset]
		right := records[offset+3]
		for _, field := range []string{
			"message",
			"identity_hook_digest",
			"identity_hook_stage_digest",
			"identity_hook_action_digest",
			"identity_runtime_event_kind_digest",
			"error_digest",
			"source",
		} {
			if left[field] != right[field] {
				t.Fatalf("record %d field %s changed with policy: %#v != %#v", offset, field, left[field], right[field])
			}
		}
	}
	if records[0]["source"] != "unknown" {
		t.Fatalf("invalid hook source projection = %#v", records[0]["source"])
	}
}

func TestP015HookSafeLoggingConcurrentLifecycle(t *testing.T) {
	oldLevel := logger.GetLevel()
	logger.SetLevel(logger.FATAL)
	defer logger.SetLevel(oldLevel)

	var calls atomic.Int64
	hostile := &p015HostileHookError{calls: &calls, secret: p015HookErrorCanary}
	manager := NewHookManager(nil)
	observer := p015ErrorObserver{err: hostile}
	closer := p015ErrorCloser{err: hostile}

	const (
		workers    = 12
		iterations = 40
	)
	start := make(chan struct{})
	var group sync.WaitGroup
	for worker := 0; worker < workers; worker++ {
		group.Add(1)
		go func() {
			defer group.Done()
			<-start
			for iteration := 0; iteration < iterations; iteration++ {
				name := p015HookNameCanary + string(rune('a'+worker))
				manager.ConfigureTimeouts(time.Second, time.Second, time.Second)
				manager.runRuntimeObserver(
					name,
					observer,
					runtimeevents.Event{Kind: runtimeevents.Kind(p015HookEventCanary)},
				)
				if iteration%4 == 0 {
					_ = manager.Mount(NamedHook(name, closer))
					manager.Unmount(name)
				}
			}
		}()
	}
	for index := 0; index < 4; index++ {
		group.Add(1)
		go func() {
			defer group.Done()
			<-start
			for iteration := 0; iteration < iterations; iteration++ {
				manager.Close()
			}
		}()
	}
	close(start)
	group.Wait()
	manager.Close()

	if got := calls.Load(); got != 0 {
		t.Fatalf("hostile error method calls under lifecycle stress = %d; want 0", got)
	}
}

func TestP015HookCloserCanReenterManagerLifecycle(t *testing.T) {
	t.Run("replacement", func(t *testing.T) {
		manager := NewHookManager(nil)
		defer manager.Close()
		var reentryErr error
		retired := &p015ReentrantHookCloser{
			reenter: func() {
				reentryErr = manager.Mount(NamedHook("reentrant-replace-side", struct{}{}))
			},
		}
		if err := manager.Mount(NamedHook("reentrant-replace", retired)); err != nil {
			t.Fatal(err)
		}
		var replaceErr error
		runP015HookLifecycleBounded(t, func() {
			replaceErr = manager.Mount(NamedHook("reentrant-replace", struct{}{}))
		})
		if replaceErr != nil || reentryErr != nil {
			t.Fatalf("replacement/reentry errors = %v / %v", replaceErr, reentryErr)
		}
		if retired.calls.Load() != 1 {
			t.Fatalf("retired replacement closes = %d, want 1", retired.calls.Load())
		}
		assertP015HookNames(t, manager, "reentrant-replace", "reentrant-replace-side")
	})

	t.Run("unmount", func(t *testing.T) {
		manager := NewHookManager(nil)
		defer manager.Close()
		side := &p015ReentrantHookCloser{}
		retired := &p015ReentrantHookCloser{
			reenter: func() {
				manager.Unmount("reentrant-unmount-side")
			},
		}
		if err := manager.Mount(NamedHook("reentrant-unmount-side", side)); err != nil {
			t.Fatal(err)
		}
		if err := manager.Mount(NamedHook("reentrant-unmount", retired)); err != nil {
			t.Fatal(err)
		}
		runP015HookLifecycleBounded(t, func() {
			manager.Unmount("reentrant-unmount")
		})
		if retired.calls.Load() != 1 || side.calls.Load() != 1 {
			t.Fatalf(
				"unmount close calls = retired:%d side:%d, want 1 each",
				retired.calls.Load(),
				side.calls.Load(),
			)
		}
		assertP015HookNames(t, manager)
	})

	t.Run("close", func(t *testing.T) {
		manager := NewHookManager(nil)
		side := &p015ReentrantHookCloser{}
		retired := &p015ReentrantHookCloser{
			reenter: func() {
				manager.Unmount("reentrant-close-side")
			},
		}
		if err := manager.Mount(NamedHook("reentrant-close-side", side)); err != nil {
			t.Fatal(err)
		}
		if err := manager.Mount(NamedHook("reentrant-close", retired)); err != nil {
			t.Fatal(err)
		}
		runP015HookLifecycleBounded(t, manager.Close)
		if retired.calls.Load() != 1 || side.calls.Load() != 1 {
			t.Fatalf(
				"manager close calls = retired:%d side:%d, want 1 each",
				retired.calls.Load(),
				side.calls.Load(),
			)
		}
		assertP015HookNames(t, manager)
	})
}

func TestP015HookManagerCloseReentryAndConcurrentExactOnce(t *testing.T) {
	t.Run("mounted hook Close reenters Close", func(t *testing.T) {
		manager := NewHookManager(nil)
		closer := &p015ReentrantHookCloser{reenter: manager.Close}
		if err := manager.Mount(NamedHook("reentrant-manager-close", closer)); err != nil {
			t.Fatal(err)
		}
		runP015HookLifecycleBounded(t, manager.Close)
		if closer.calls.Load() != 1 {
			t.Fatalf("reentrant mounted hook closes = %d, want 1", closer.calls.Load())
		}
	})

	t.Run("subscription Close reenters Close", func(t *testing.T) {
		manager := NewHookManager(nil)
		subscription := &p015ReentrantHookSubscription{
			done:    closedP015HookChannel(),
			reenter: manager.Close,
		}
		manager.runtimeSub = subscription
		runP015HookLifecycleBounded(t, manager.Close)
		if subscription.calls.Load() != 1 {
			t.Fatalf("reentrant subscription closes = %d, want 1", subscription.calls.Load())
		}
	})

	t.Run("overlapping callers elect one primary cleanup", func(t *testing.T) {
		manager := NewHookManager(nil)
		closer := &p015ReentrantHookCloser{reenter: manager.Close}
		if err := manager.Mount(NamedHook("concurrent-manager-close", closer)); err != nil {
			t.Fatal(err)
		}
		entered := make(chan struct{})
		release := make(chan struct{})
		var enteredOnce sync.Once
		var releaseOnce sync.Once
		releasePrimary := func() { releaseOnce.Do(func() { close(release) }) }
		defer releasePrimary()
		subscription := &p015ReentrantHookSubscription{
			done: closedP015HookChannel(),
			reenter: func() {
				manager.Close()
				enteredOnce.Do(func() { close(entered) })
				<-release
			},
		}
		manager.runtimeSub = subscription

		primaryDone := make(chan struct{})
		go func() {
			defer close(primaryDone)
			manager.Close()
		}()
		select {
		case <-entered:
		case <-time.After(time.Second):
			t.Fatal("primary Close did not enter subscription cleanup")
		}

		const secondaryCallers = 32
		var secondary sync.WaitGroup
		secondary.Add(secondaryCallers)
		for range secondaryCallers {
			go func() {
				defer secondary.Done()
				manager.Close()
			}()
		}
		runP015HookLifecycleBounded(t, secondary.Wait)
		if closer.calls.Load() != 0 {
			t.Fatal("secondary Close waited for or performed primary hook cleanup")
		}
		releasePrimary()
		select {
		case <-primaryDone:
		case <-time.After(time.Second):
			t.Fatal("primary Close did not finish after subscription release")
		}
		if subscription.calls.Load() != 1 || closer.calls.Load() != 1 {
			t.Fatalf(
				"primary cleanup calls = subscription:%d hook:%d, want 1 each",
				subscription.calls.Load(),
				closer.calls.Load(),
			)
		}
	})
}

func TestP015HookAgentLoopWrappersRemainCompatible(t *testing.T) {
	var nilLoop *AgentLoop
	if err := nilLoop.MountHook(NamedHook("nil", struct{}{})); err == nil {
		t.Fatal("nil AgentLoop MountHook error = nil")
	}
	nilLoop.UnmountHook("nil")
	if nilLoop.RuntimeEvents() != nil || nilLoop.RuntimeEventBus() != nil {
		t.Fatal("nil AgentLoop exposed runtime events")
	}
	if stats := nilLoop.RuntimeEventStats(); !stats.Closed {
		t.Fatalf("nil AgentLoop runtime stats = %#v", stats)
	}

	manager := NewHookManager(nil)
	loop := &AgentLoop{hooks: manager}
	closer := &p015ReentrantHookCloser{}
	if err := loop.MountHook(NamedHook("wrapper", closer)); err != nil {
		t.Fatal(err)
	}
	loop.UnmountHook("wrapper")
	if closer.calls.Load() != 1 {
		t.Fatalf("wrapper hook closes = %d, want 1", closer.calls.Load())
	}
	manager.Close()

	configured := &AgentLoop{}
	WithRuntimeEvents(nil)(configured)
	bus := runtimeevents.NewBus()
	defer func() { _ = bus.Close() }()
	WithRuntimeEvents(bus)(configured)
	WithConfigPath("  /tmp/p015-config.json  ")(configured)
	WithRuntimeStartupBarrier()(configured)
	WithDeferredEvolutionActivation()(configured)
	configured.SetReloadFunc(func() error { return nil })
	if configured.runtimeEvents != bus || configured.ownsRuntimeEvents ||
		configured.RuntimeEvents() == nil || configured.RuntimeEventBus() != bus ||
		configured.RuntimeEventStats().Closed ||
		configured.configPath != "/tmp/p015-config.json" ||
		!configured.runtimeGatePaused || configured.runtimeGatePauses != 1 ||
		!configured.runtimeStartupBarrier || !configured.deferEvolutionActivation ||
		configured.reloadFunc == nil {
		t.Fatalf("AgentLoop compatibility options = %#v", configured)
	}
	if err := configured.RecordLastChannel("channel"); err != nil {
		t.Fatal(err)
	}
	if err := configured.RecordLastChatID("chat"); err != nil {
		t.Fatal(err)
	}
}

func runP015HookLifecycleBounded(t *testing.T, operation func()) {
	t.Helper()
	done := make(chan struct{})
	go func() {
		defer close(done)
		operation()
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("reentrant hook lifecycle operation deadlocked")
	}
}

func assertP015HookNames(t *testing.T, manager *HookManager, want ...string) {
	t.Helper()
	registrations := manager.snapshotHooks()
	got := make([]string, 0, len(registrations))
	for _, registration := range registrations {
		got = append(got, registration.Name)
	}
	slices.Sort(got)
	slices.Sort(want)
	if !slices.Equal(got, want) {
		t.Fatalf("hook names = %v, want %v", got, want)
	}
}

func emitP015ProcessHookDiagnostics(
	ctx context.Context,
	stderrBytes []byte,
	malformedBytes []byte,
) {
	_ = logger.DiagnosticPolicyFromContext(ctx)
	hook := &ProcessHook{name: p015HookNameCanary}
	hook.readStderr(bytes.NewReader(append(append([]byte(nil), stderrBytes...), '\n')))
	hook.readLoop(bytes.NewReader(append(append([]byte(nil), malformedBytes...), '\n')))
}

func closedP015HookChannel() chan struct{} {
	done := make(chan struct{})
	close(done)
	return done
}

func captureP015HookRecords(
	t *testing.T,
	emit func(),
) ([]map[string]any, []byte) {
	t.Helper()
	oldLevel := logger.GetLevel()
	logger.DisableFileLogging()
	logger.DisableConsole()
	logger.SetLevel(logger.DEBUG)
	defer func() {
		logger.DisableFileLogging()
		logger.EnableConsole()
		logger.SetLevel(oldLevel)
	}()

	path := filepath.Join(t.TempDir(), "hook-safe.jsonl")
	if err := logger.EnableFileLogging(path); err != nil {
		t.Fatalf("enable hook log capture: %v", err)
	}
	emit()
	logger.DisableFileLogging()

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read hook log capture: %v", err)
	}
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return nil, raw
	}
	lines := bytes.Split(trimmed, []byte{'\n'})
	records := make([]map[string]any, 0, len(lines))
	for index, line := range lines {
		var record map[string]any
		if err := json.Unmarshal(line, &record); err != nil {
			t.Fatalf("decode hook log record %d: %v; line=%q", index, err, line)
		}
		records = append(records, record)
	}
	return records, raw
}

func assertP015CanariesAbsent(t *testing.T, raw []byte, canaries ...string) {
	t.Helper()
	for _, canary := range canaries {
		if bytes.Contains(raw, []byte(canary)) {
			t.Fatalf("raw hook log contains canary %q: %s", canary, raw)
		}
	}
	if bytes.Contains(raw, []byte{0xff}) || bytes.Contains(raw, []byte{0xfe}) {
		t.Fatalf("raw hook log contains invalid UTF-8 input bytes: %q", raw)
	}
	if strings.Contains(string(raw), "sensitive_preview") {
		t.Fatalf("raw hook log contains a sensitive preview: %s", raw)
	}
}
