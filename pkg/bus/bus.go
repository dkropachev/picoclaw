package bus

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sync"
	"sync/atomic"
	"time"

	runtimeevents "github.com/sipeed/picoclaw/pkg/events"
	"github.com/sipeed/picoclaw/pkg/logger"
)

// ErrBusClosed is returned when publishing to a closed MessageBus.
var ErrBusClosed = errors.New("message bus closed")

// ErrBusBackpressure is returned when a publish attempt exceeds the configured
// backpressure wait budget and the message is dropped.
var ErrBusBackpressure = errors.New("message bus backpressure")

var (
	ErrMissingInboundContext       = errors.New("inbound message context is required")
	ErrMissingOutboundContext      = errors.New("outbound message context is required")
	ErrMissingOutboundMediaContext = errors.New("outbound media context is required")
	ErrInboundAdmissionRegistered  = errors.New("inbound admission is already registered")
	ErrInvalidInboundAdmission     = errors.New("inbound admission is required")
)

const defaultBusBufferSize = 64

const (
	defaultAudioPublishTimeout = 150 * time.Millisecond
)

type publishPolicy struct {
	stream string
	// timeout is the backpressure drop budget. When positive, a full channel
	// causes the message to be dropped after this duration. When zero, the
	// publish blocks until context cancellation or bus close (no drop).
	timeout time.Duration
}

type streamStats struct {
	dropped       atomic.Uint64
	lastDropped   atomic.Int64
	lastWaitNanos atomic.Int64
}

type MessageBusStats struct {
	Inbound       StreamStats `json:"inbound"`
	Outbound      StreamStats `json:"outbound"`
	OutboundMedia StreamStats `json:"outbound_media"`
	AudioChunks   StreamStats `json:"audio_chunks"`
	VoiceControls StreamStats `json:"voice_controls"`
}

type StreamStats struct {
	Depth              int       `json:"depth"`
	Capacity           int       `json:"capacity"`
	DroppedTotal       uint64    `json:"dropped_total"`
	LastDroppedAt      time.Time `json:"last_dropped_at,omitempty"`
	LastDropWait       string    `json:"last_drop_wait,omitempty"`
	LastDropWaitMillis int64     `json:"last_drop_wait_ms,omitempty"`
}

// StreamDelegate is implemented by the channel Manager to provide streaming
// capabilities to the agent loop without tight coupling.
type StreamDelegate interface {
	// GetStreamer returns a Streamer for the given channel+chatID if the channel
	// supports streaming. Returns nil, false if streaming is unavailable.
	GetStreamer(
		ctx context.Context,
		channel, chatID, sessionKey string,
	) (Streamer, bool)
}

// TurnScopedStreamDelegate is an additive streaming capability for delegates
// that can bind finalization markers and transient channel UX to one exact
// inbound turn. Legacy StreamDelegate implementations remain supported.
type TurnScopedStreamDelegate interface {
	GetStreamerForTurn(
		ctx context.Context,
		channel, chatID, sessionKey, turnUXID string,
	) (Streamer, bool)
}

// Streamer pushes incremental content to a streaming-capable channel.
// Defined here so the agent loop can use it without importing pkg/channels.
type Streamer interface {
	Update(ctx context.Context, content string) error
	Finalize(ctx context.Context, content string) error
	Cancel(ctx context.Context)
}

// ContextUsageStreamer can attach final context-window usage metadata when a
// streaming channel's final message replaces the normal outbound response.
type ContextUsageStreamer interface {
	Streamer
	FinalizeWithContext(ctx context.Context, content string, usage *ContextUsage) error
}

// ReasoningStreamer can show incremental model reasoning/thought content
// separately from the final user-visible answer stream.
type ReasoningStreamer interface {
	UpdateReasoning(ctx context.Context, content string) error
	FinalizeReasoning(ctx context.Context, content string) error
}

// InboundAdmission synchronously decides whether a normalized channel-origin
// message should continue to the agent queue. Implementations may durably
// consume the message as an event and return false without treating that as a
// publish failure.
type InboundAdmission interface {
	AdmitInbound(ctx context.Context, msg InboundMessage) (forward bool, err error)
}

type inboundAdmissionHolder struct {
	admission InboundAdmission
}

type MessageBus struct {
	inbound       chan InboundMessage
	outbound      chan OutboundMessage
	outboundMedia chan OutboundMediaMessage
	audioChunks   chan AudioChunk
	voiceControls chan VoiceControl

	closeOnce        sync.Once
	done             chan struct{}
	shutdownCtx      context.Context
	shutdownCancel   context.CancelFunc
	closed           atomic.Bool
	wg               sync.WaitGroup
	publishMu        sync.Mutex
	streamDelegate   atomic.Value // stores StreamDelegate
	eventPublisher   atomic.Value // stores EventPublisher
	inboundAdmission atomic.Pointer[inboundAdmissionHolder]
	inboundStats     streamStats
	outboundStats    streamStats
	mediaStats       streamStats
	audioStats       streamStats
	voiceStats       streamStats
}

// EventPublisher is the minimal runtime event publisher used by MessageBus.
type EventPublisher interface {
	Publish(ctx context.Context, evt runtimeevents.Event) runtimeevents.PublishResult
	PublishNonBlocking(evt runtimeevents.Event) runtimeevents.PublishResult
}

func NewMessageBus() *MessageBus {
	shutdownCtx, shutdownCancel := context.WithCancel(context.Background())
	return &MessageBus{
		inbound:        make(chan InboundMessage, defaultBusBufferSize),
		outbound:       make(chan OutboundMessage, defaultBusBufferSize),
		outboundMedia:  make(chan OutboundMediaMessage, defaultBusBufferSize),
		audioChunks:    make(chan AudioChunk, defaultBusBufferSize*4), // Audio chunks need more buffer.
		voiceControls:  make(chan VoiceControl, defaultBusBufferSize),
		done:           make(chan struct{}),
		shutdownCtx:    shutdownCtx,
		shutdownCancel: shutdownCancel,
	}
}

func (mb *MessageBus) enterPublish(ctx context.Context) error {
	mb.publishMu.Lock()
	defer mb.publishMu.Unlock()

	if mb.closed.Load() {
		return ErrBusClosed
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-mb.done:
		return ErrBusClosed
	default:
	}

	mb.wg.Add(1)
	return nil
}

func publish[T any](
	ctx context.Context,
	mb *MessageBus,
	ch chan T,
	msg T,
	policy publishPolicy,
	stats *streamStats,
	scope runtimeevents.Scope,
) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := mb.enterPublish(ctx); err != nil {
		return err
	}
	defer mb.wg.Done()

	// timeout == 0 means no backpressure drop budget; block until context
	// cancellation or bus close. This is the default for critical streams
	// (inbound, outbound, outboundMedia, voiceControl) where dropping
	// messages silently is undesirable.
	if policy.timeout > 0 {
		timer := time.NewTimer(policy.timeout)
		defer timer.Stop()

		select {
		case ch <- msg:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		case <-timer.C:
			droppedTotal := stats.dropped.Add(1)
			now := time.Now()
			stats.lastDropped.Store(now.UnixNano())
			stats.lastWaitNanos.Store(policy.timeout.Nanoseconds())
			queueDepth := len(ch)
			queueCap := cap(ch)
			mb.publishDrop(
				policy.stream, scope, "queue_full_timeout",
				policy.timeout, queueDepth, queueCap, droppedTotal,
			)
			logger.WarnCF("bus", "Dropped bus message due to backpressure", map[string]any{
				"stream":         policy.stream,
				"wait_ms":        policy.timeout.Milliseconds(),
				"queue_depth":    queueDepth,
				"queue_capacity": queueCap,
				"dropped_total":  droppedTotal,
			})
			return fmt.Errorf("%w: %s queue full after %s", ErrBusBackpressure, policy.stream, policy.timeout)
		case <-mb.done:
			return ErrBusClosed
		}
	}

	select {
	case ch <- msg:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	case <-mb.done:
		return ErrBusClosed
	}
}

func (mb *MessageBus) PublishInbound(ctx context.Context, msg InboundMessage) error {
	_, err := mb.PublishInboundWithPreparationResult(ctx, msg, nil)
	return err
}

// PublishInboundWithPreparation publishes one inbound message and invokes
// prepare immediately before a successfully admitted message enters the
// inbound queue. The callback is skipped when admission consumes or rejects
// the message. Channels use this boundary for typing, reaction, and placeholder
// UX that must exist only when an ordinary agent turn will follow.
func (mb *MessageBus) PublishInboundWithPreparation(
	ctx context.Context,
	msg InboundMessage,
	prepare func(),
) error {
	_, err := mb.PublishInboundWithPreparationResult(ctx, msg, prepare)
	return err
}

// PublishInboundWithPreparationResult is the result-bearing form of
// PublishInboundWithPreparation. forwarded is true only after the message has
// entered the inbound queue; callers can release turn-scoped resources when it
// is false.
func (mb *MessageBus) PublishInboundWithPreparationResult(
	ctx context.Context,
	msg InboundMessage,
	prepare func(),
) (forwarded bool, err error) {
	var transactionalPrepare func() func()
	if prepare != nil {
		transactionalPrepare = func() func() {
			prepare()
			return nil
		}
	}
	return mb.publishInboundWithTransactionalPreparationResult(
		ctx,
		msg,
		transactionalPrepare,
	)
}

// PublishInboundWithTransactionalPreparationResult is equivalent to
// PublishInboundWithPreparationResult, but preparation may return a rollback
// callback. Rollback runs exactly once when cancellation or bus closure wins
// after preparation and before queue ownership transfers.
func (mb *MessageBus) PublishInboundWithTransactionalPreparationResult(
	ctx context.Context,
	msg InboundMessage,
	prepare func() (rollback func()),
) (forwarded bool, err error) {
	return mb.publishInboundWithTransactionalPreparationResult(ctx, msg, prepare)
}

func (mb *MessageBus) publishInboundWithTransactionalPreparationResult(
	ctx context.Context,
	msg InboundMessage,
	prepare func() (rollback func()),
) (forwarded bool, err error) {
	if ctx == nil {
		ctx = context.Background()
	}
	msg = NormalizeInboundMessage(msg)
	if msg.Context.isZero() {
		mb.publishFailure("inbound", runtimeScopeFromInboundContext(msg.Context), ErrMissingInboundContext)
		return false, ErrMissingInboundContext
	}
	scope := runtimeScopeFromInboundContext(msg.Context)
	// Hold one publish lease across admission, conditional channel UX, and
	// queueing. Close therefore cannot finish and close the queue between a
	// durable insert and the corresponding mirror decision.
	if err := mb.enterPublish(ctx); err != nil {
		mb.publishFailure("inbound", scope, err)
		return false, err
	}
	leaseHeld := true
	releaseLease := func() {
		if leaseHeld {
			mb.wg.Done()
			leaseHeld = false
		}
	}
	defer releaseLease()

	if msg.ChannelOrigin {
		if holder := mb.inboundAdmission.Load(); holder != nil && holder.admission != nil {
			// Give admission its own deep clone. The hook can inspect or even
			// mutate nested metadata without changing caller-owned values or
			// the message eventually published to the queue.
			admissionCtx := ctx
			var cancelAdmission context.CancelFunc
			var stopShutdownCancel func() bool
			if mb.shutdownCtx != nil {
				admissionCtx, cancelAdmission = context.WithCancel(ctx)
				stopShutdownCancel = context.AfterFunc(mb.shutdownCtx, cancelAdmission)
			}
			forward, err := holder.admission.AdmitInbound(
				admissionCtx,
				NormalizeInboundMessage(msg),
			)
			if stopShutdownCancel != nil {
				stopShutdownCancel()
			}
			if cancelAdmission != nil {
				cancelAdmission()
			}
			if err != nil &&
				ctx.Err() == nil &&
				mb.shutdownCtx != nil &&
				mb.shutdownCtx.Err() != nil &&
				errors.Is(err, context.Canceled) {
				err = ErrBusClosed
			}
			if err != nil {
				mb.publishFailure("inbound", scope, err)
				return false, err
			}
			if !forward {
				return false, nil
			}
		}
	}
	var rollback func()
	if prepare != nil {
		rollback = prepare()
	}
	select {
	case mb.inbound <- msg:
		return true, nil
	case <-ctx.Done():
		// Queue ownership did not transfer. Release the bus-close lease before
		// potentially slow provider cleanup so MessageBus.Close cannot hang on
		// a contextless platform callback.
		releaseLease()
		if rollback != nil {
			rollback()
		}
		err := ctx.Err()
		mb.publishFailure("inbound", scope, err)
		return false, err
	case <-mb.done:
		releaseLease()
		if rollback != nil {
			rollback()
		}
		mb.publishFailure("inbound", scope, ErrBusClosed)
		return false, ErrBusClosed
	}
}

func (mb *MessageBus) InboundChan() <-chan InboundMessage {
	return mb.inbound
}

func (mb *MessageBus) PublishOutbound(ctx context.Context, msg OutboundMessage) error {
	msg = NormalizeOutboundMessage(msg)
	if msg.Context.isZero() {
		mb.publishFailure("outbound", runtimeScopeFromInboundContext(msg.Context), ErrMissingOutboundContext)
		return ErrMissingOutboundContext
	}
	if err := publish(ctx, mb, mb.outbound, msg, publishPolicy{
		stream: "outbound",
	}, &mb.outboundStats, runtimeScopeFromInboundContext(msg.Context)); err != nil {
		scope := runtimeScopeFromInboundContext(msg.Context)
		if !errors.Is(err, ErrBusBackpressure) {
			mb.publishFailure("outbound", scope, err)
		}
		return err
	}
	return nil
}

func (mb *MessageBus) OutboundChan() <-chan OutboundMessage {
	return mb.outbound
}

func (mb *MessageBus) PublishOutboundMedia(ctx context.Context, msg OutboundMediaMessage) error {
	msg = NormalizeOutboundMediaMessage(msg)
	if msg.Context.isZero() {
		mb.publishFailure("outbound_media", runtimeScopeFromInboundContext(msg.Context), ErrMissingOutboundMediaContext)
		return ErrMissingOutboundMediaContext
	}
	if err := publish(ctx, mb, mb.outboundMedia, msg, publishPolicy{
		stream: "outbound_media",
	}, &mb.mediaStats, runtimeScopeFromInboundContext(msg.Context)); err != nil {
		scope := runtimeScopeFromInboundContext(msg.Context)
		if !errors.Is(err, ErrBusBackpressure) {
			mb.publishFailure("outbound_media", scope, err)
		}
		return err
	}
	return nil
}

func (mb *MessageBus) OutboundMediaChan() <-chan OutboundMediaMessage {
	return mb.outboundMedia
}

func (mb *MessageBus) PublishAudioChunk(ctx context.Context, chunk AudioChunk) error {
	if err := publish(ctx, mb, mb.audioChunks, chunk, publishPolicy{
		stream:  "audio_chunk",
		timeout: defaultAudioPublishTimeout,
	}, &mb.audioStats, runtimeScopeFromAudioChunk(chunk)); err != nil {
		scope := runtimeScopeFromAudioChunk(chunk)
		if !errors.Is(err, ErrBusBackpressure) {
			mb.publishFailure("audio_chunk", scope, err)
		}
		return err
	}
	return nil
}

func (mb *MessageBus) AudioChunksChan() <-chan AudioChunk {
	return mb.audioChunks
}

func (mb *MessageBus) PublishVoiceControl(ctx context.Context, ctrl VoiceControl) error {
	if err := publish(ctx, mb, mb.voiceControls, ctrl, publishPolicy{
		stream: "voice_control",
	}, &mb.voiceStats, runtimeScopeFromVoiceControl(ctrl)); err != nil {
		scope := runtimeScopeFromVoiceControl(ctrl)
		if !errors.Is(err, ErrBusBackpressure) {
			mb.publishFailure("voice_control", scope, err)
		}
		return err
	}
	return nil
}

func (mb *MessageBus) VoiceControlsChan() <-chan VoiceControl {
	return mb.voiceControls
}

// SetStreamDelegate registers a StreamDelegate (typically the channel Manager).
func (mb *MessageBus) SetStreamDelegate(d StreamDelegate) {
	mb.streamDelegate.Store(d)
}

// SetEventPublisher registers a runtime event publisher for bus errors and lifecycle events.
func (mb *MessageBus) SetEventPublisher(p EventPublisher) {
	mb.eventPublisher.Store(p)
}

// SetInboundAdmission atomically replaces the channel-origin admission hook.
// Passing nil restores the original direct-to-queue behavior.
func (mb *MessageBus) SetInboundAdmission(admission InboundAdmission) {
	if invalidInboundAdmission(admission) {
		mb.inboundAdmission.Store(nil)
		return
	}
	mb.inboundAdmission.Store(&inboundAdmissionHolder{admission: admission})
}

// RegisterInboundAdmission collision-safely installs one independently owned
// channel-origin admission hook. The returned release function is idempotent
// and removes only this registration, even if another caller has since used
// SetInboundAdmission to replace it.
func (mb *MessageBus) RegisterInboundAdmission(
	admission InboundAdmission,
) (release func(), err error) {
	if mb == nil || invalidInboundAdmission(admission) {
		return nil, ErrInvalidInboundAdmission
	}

	holder := &inboundAdmissionHolder{admission: admission}
	mb.publishMu.Lock()
	defer mb.publishMu.Unlock()
	if mb.closed.Load() {
		return nil, ErrBusClosed
	}
	if !mb.inboundAdmission.CompareAndSwap(nil, holder) {
		return nil, ErrInboundAdmissionRegistered
	}
	return func() {
		mb.inboundAdmission.CompareAndSwap(holder, nil)
	}, nil
}

func invalidInboundAdmission(admission InboundAdmission) bool {
	if admission == nil {
		return true
	}
	value := reflect.ValueOf(admission)
	switch value.Kind() {
	case reflect.Chan,
		reflect.Func,
		reflect.Interface,
		reflect.Map,
		reflect.Ptr,
		reflect.Slice:
		return value.IsNil()
	default:
		return false
	}
}

// GetStreamer returns a Streamer for the given channel+chatID+session via the
// legacy delegate contract.
func (mb *MessageBus) GetStreamer(
	ctx context.Context,
	channel, chatID, sessionKey string,
) (Streamer, bool) {
	if d, ok := mb.streamDelegate.Load().(StreamDelegate); ok && d != nil {
		return d.GetStreamer(ctx, channel, chatID, sessionKey)
	}
	return nil, false
}

// GetStreamerForTurn prefers a turn-scoped delegate and falls back to the
// source-compatible legacy contract when the registered delegate predates
// turn-scoped channel UX.
func (mb *MessageBus) GetStreamerForTurn(
	ctx context.Context,
	channel, chatID, sessionKey, turnUXID string,
) (Streamer, bool) {
	delegate := mb.streamDelegate.Load()
	if d, ok := delegate.(TurnScopedStreamDelegate); ok && d != nil {
		return d.GetStreamerForTurn(
			ctx,
			channel,
			chatID,
			sessionKey,
			turnUXID,
		)
	}
	if d, ok := delegate.(StreamDelegate); ok && d != nil {
		return d.GetStreamer(ctx, channel, chatID, sessionKey)
	}
	return nil, false
}

func (mb *MessageBus) Stats() MessageBusStats {
	if mb == nil {
		return MessageBusStats{}
	}
	return MessageBusStats{
		Inbound:       snapshotStreamStats(mb.inbound, &mb.inboundStats),
		Outbound:      snapshotStreamStats(mb.outbound, &mb.outboundStats),
		OutboundMedia: snapshotStreamStats(mb.outboundMedia, &mb.mediaStats),
		AudioChunks:   snapshotStreamStats(mb.audioChunks, &mb.audioStats),
		VoiceControls: snapshotStreamStats(mb.voiceControls, &mb.voiceStats),
	}
}

// HealthCheck returns a snapshot of queue depths and cumulative drop counts
// across all streams. It always reports ok=true: backpressure-induced drops are
// reflected in the message string for telemetry but do not affect the boolean.
// Callers that need readiness semantics (e.g. returning 503 when drops occur)
// should inspect Stats() directly and apply their own threshold logic.
func (mb *MessageBus) HealthCheck() (bool, string) {
	stats := mb.Stats()
	totalDropped := stats.Inbound.DroppedTotal +
		stats.Outbound.DroppedTotal +
		stats.OutboundMedia.DroppedTotal +
		stats.AudioChunks.DroppedTotal +
		stats.VoiceControls.DroppedTotal
	message := fmt.Sprintf(
		"in=%d/%d out=%d/%d media=%d/%d audio=%d/%d voice=%d/%d dropped=%d",
		stats.Inbound.Depth,
		stats.Inbound.Capacity,
		stats.Outbound.Depth,
		stats.Outbound.Capacity,
		stats.OutboundMedia.Depth,
		stats.OutboundMedia.Capacity,
		stats.AudioChunks.Depth,
		stats.AudioChunks.Capacity,
		stats.VoiceControls.Depth,
		stats.VoiceControls.Capacity,
		totalDropped,
	)

	return true, message
}

func snapshotStreamStats[T any](ch chan T, stats *streamStats) StreamStats {
	snapshot := StreamStats{
		DroppedTotal: stats.dropped.Load(),
	}
	if ch != nil {
		snapshot.Depth = len(ch)
		snapshot.Capacity = cap(ch)
	}
	if unixNano := stats.lastDropped.Load(); unixNano > 0 {
		snapshot.LastDroppedAt = time.Unix(0, unixNano)
	}
	if waitNanos := stats.lastWaitNanos.Load(); waitNanos > 0 {
		wait := time.Duration(waitNanos)
		snapshot.LastDropWait = wait.String()
		snapshot.LastDropWaitMillis = wait.Milliseconds()
	}
	return snapshot
}

func (mb *MessageBus) Close() {
	mb.closeOnce.Do(func() {
		mb.publishCloseEvent(runtimeevents.KindBusCloseStarted, 0)

		mb.publishMu.Lock()
		mb.closed.Store(true)
		if mb.shutdownCancel != nil {
			mb.shutdownCancel()
		}
		close(mb.done)
		mb.publishMu.Unlock()

		// wait for all ongoing Publish calls to finish, ensuring all messages have been sent to channels or exited
		mb.wg.Wait()

		// close channels safely
		close(mb.inbound)
		close(mb.outbound)
		close(mb.outboundMedia)
		close(mb.audioChunks)
		close(mb.voiceControls)

		// clean up any remaining messages in channels
		drained := 0
		for range mb.inbound {
			drained++
		}
		for range mb.outbound {
			drained++
		}
		for range mb.outboundMedia {
			drained++
		}
		for range mb.audioChunks {
			drained++
		}
		for range mb.voiceControls {
			drained++
		}

		if drained > 0 {
			logger.DebugCF("bus", "Drained buffered messages during close", map[string]any{
				"count": drained,
			})
			mb.publishCloseEvent(runtimeevents.KindBusCloseDrained, drained)
		}
		mb.publishCloseEvent(runtimeevents.KindBusCloseCompleted, drained)
	})
}
