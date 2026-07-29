package channelmessage

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sipeed/picoclaw/pkg/bus"
)

type admissionResult struct {
	forward bool
	err     error
}

func controllerBackend(
	t *testing.T,
	store Inserter,
	adapters map[string]AdapterConfig,
) *Backend {
	t.Helper()
	return testBackend(t, store, adapters, 4096)
}

func receiveAdmission(
	t *testing.T,
	results <-chan admissionResult,
) admissionResult {
	t.Helper()
	select {
	case result := <-results:
		return result
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for channel admission")
		return admissionResult{}
	}
}

func assertAdmissionWaiting(
	t *testing.T,
	results <-chan admissionResult,
) {
	t.Helper()
	select {
	case result := <-results:
		t.Fatalf("channel admission returned early: %#v", result)
	case <-time.After(20 * time.Millisecond):
	}
}

func asyncAdmission(
	controller *Controller,
	ctx context.Context,
	message bus.InboundMessage,
) <-chan admissionResult {
	result := make(chan admissionResult, 1)
	go func() {
		forward, err := controller.AdmitInbound(ctx, message)
		result <- admissionResult{forward: forward, err: err}
	}()
	return result
}

func TestControllerInactiveAndUnrelatedMessagesPass(t *testing.T) {
	t.Parallel()
	controller := NewController()
	for _, message := range []bus.InboundMessage{
		testMessage("not-yet-configured", ""),
		func() bus.InboundMessage {
			message := testMessage("not-yet-configured", "")
			message.ChannelOrigin = false
			return message
		}(),
	} {
		forward, err := controller.AdmitInbound(context.Background(), message)
		require.NoError(t, err)
		assert.True(t, forward)
	}
}

func TestControllerPrepareWaitsUntilActivate(t *testing.T) {
	t.Parallel()
	controller := NewController()
	require.NoError(t, controller.Prepare([]string{"configured"}))

	configuredResult := asyncAdmission(
		controller,
		context.Background(),
		testMessage("configured", "prepared-message"),
	)
	assertAdmissionWaiting(t, configuredResult)

	unrelated, err := controller.AdmitInbound(
		context.Background(),
		testMessage("unrelated", ""),
	)
	require.NoError(t, err)
	assert.True(t, unrelated)
	internal := testMessage("configured", "")
	internal.ChannelOrigin = false
	forward, err := controller.AdmitInbound(context.Background(), internal)
	require.NoError(t, err)
	assert.True(t, forward)

	store := newRecordingInserter()
	backend := controllerBackend(t, store, map[string]AdapterConfig{
		"configured": {
			Source:      SourceChat,
			Mode:        ModeEventOnly,
			ChannelType: "test",
		},
	})
	generation, err := controller.Activate(backend)
	require.NoError(t, err)
	assert.True(t, controller.IsActive(generation))

	result := receiveAdmission(t, configuredResult)
	require.NoError(t, result.err)
	assert.False(t, result.forward)
	assert.Len(t, store.recordedInserts(), 1)

	require.NoError(t, controller.Deactivate(context.Background(), generation, nil))
	require.NoError(t, controller.CommitDisabled())
}

func TestControllerPrepareWaitIsContextAwareAndCommitDisabledPasses(t *testing.T) {
	t.Parallel()
	controller := NewController()
	require.NoError(t, controller.Prepare([]string{"configured"}))

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	forward, err := controller.AdmitInbound(ctx, testMessage("configured", "canceled"))
	assert.False(t, forward)
	require.ErrorIs(t, err, context.DeadlineExceeded)

	result := asyncAdmission(
		controller,
		context.Background(),
		testMessage("configured", "disabled"),
	)
	assertAdmissionWaiting(t, result)
	require.NoError(t, controller.CommitDisabled())
	got := receiveAdmission(t, result)
	require.NoError(t, got.err)
	assert.True(t, got.forward)
}

func TestControllerCancelPreparationPreservesActiveGenerationAndWakesCandidate(t *testing.T) {
	t.Parallel()
	controller := NewController()
	store := newRecordingInserter()
	backend := controllerBackend(t, store, map[string]AdapterConfig{
		"active": {
			Source:      SourceChat,
			Mode:        ModeEventOnly,
			ChannelType: "test",
		},
	})
	require.NoError(t, controller.Prepare(backend.ConnectorNames()))
	generation, err := controller.Activate(backend)
	require.NoError(t, err)

	require.NoError(t, controller.Prepare([]string{"active", "candidate"}))
	candidate := asyncAdmission(
		controller,
		context.Background(),
		testMessage("candidate", "candidate-message"),
	)
	assertAdmissionWaiting(t, candidate)

	require.NoError(t, controller.CancelPreparation())
	assert.True(t, controller.IsActive(generation))
	candidateResult := receiveAdmission(t, candidate)
	require.NoError(t, candidateResult.err)
	assert.True(t, candidateResult.forward)

	active, err := controller.AdmitInbound(
		context.Background(),
		testMessage("active", "active-message"),
	)
	require.NoError(t, err)
	assert.False(t, active)
	assert.Len(t, store.recordedInserts(), 1)

	// Cancellation must clear the candidate preparation completely. In
	// particular, a later terminal shutdown can establish its empty next-set
	// fence without tripping a stale prepared-connectors mismatch.
	require.NoError(t, controller.Deactivate(context.Background(), generation, nil))
	require.NoError(t, controller.CommitDisabled())
}

func TestControllerCancelPreparationValidation(t *testing.T) {
	t.Parallel()

	t.Run("retiring", func(t *testing.T) {
		t.Parallel()
		controller := NewController()
		store := newRecordingInserter()
		store.started = make(chan struct{})
		store.release = make(chan struct{})
		backend := controllerBackend(t, store, map[string]AdapterConfig{
			"configured": {
				Source:      SourceChat,
				Mode:        ModeMirror,
				ChannelType: "test",
			},
		})
		require.NoError(t, controller.Prepare(backend.ConnectorNames()))
		generation, err := controller.Activate(backend)
		require.NoError(t, err)
		inflight := asyncAdmission(
			controller,
			context.Background(),
			testMessage("configured", "inflight-cancel"),
		)
		select {
		case <-store.started:
		case <-time.After(2 * time.Second):
			t.Fatal("in-flight insert did not start")
		}
		drainCtx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
		defer cancel()
		require.ErrorIs(
			t,
			controller.Deactivate(drainCtx, generation, nil),
			context.DeadlineExceeded,
		)
		require.ErrorIs(t, controller.CancelPreparation(), ErrGenerationDraining)
		close(store.release)
		require.NoError(t, receiveAdmission(t, inflight).err)
		require.NoError(t, controller.Deactivate(context.Background(), generation, nil))
		require.NoError(t, controller.CommitDisabled())
	})

	t.Run("closed", func(t *testing.T) {
		t.Parallel()
		controller := NewController()
		require.NoError(t, controller.Close(context.Background()))
		require.ErrorIs(t, controller.CancelPreparation(), ErrControllerClosed)
	})
}

func TestControllerStageKeepsPreparedPublishersFencedUntilCommit(t *testing.T) {
	t.Parallel()
	controller := NewController()
	store := newRecordingInserter()
	backend := controllerBackend(t, store, map[string]AdapterConfig{
		"configured": {
			Source:      SourceChat,
			Mode:        ModeEventOnly,
			ChannelType: "test",
		},
	})
	require.NoError(t, controller.Prepare(backend.ConnectorNames()))
	staged, err := controller.Stage(backend)
	require.NoError(t, err)
	generation := staged.Generation()
	assert.False(t, controller.IsActive(generation))
	_, err = controller.Stage(backend)
	require.ErrorIs(t, err, ErrActivationStaged)
	require.ErrorIs(t, controller.CancelPreparation(), ErrActivationStaged)
	require.ErrorIs(
		t,
		controller.Deactivate(context.Background(), Generation{}, nil),
		ErrActivationStaged,
	)
	require.ErrorIs(t, controller.Close(context.Background()), ErrActivationStaged)

	waiting := asyncAdmission(
		controller,
		context.Background(),
		testMessage("configured", "staged-message"),
	)
	assertAdmissionWaiting(t, waiting)
	assert.Empty(t, store.recordedAttempts())

	staged.Commit()
	assert.True(t, controller.IsActive(generation))
	result := receiveAdmission(t, waiting)
	require.NoError(t, result.err)
	assert.False(t, result.forward)
	assert.Len(t, store.recordedAttempts(), 1)

	// A stale abort cannot affect the committed generation.
	staged.Abort()
	assert.True(t, controller.IsActive(generation))
	require.NoError(t, controller.Deactivate(context.Background(), generation, nil))
	require.NoError(t, controller.CommitDisabled())
}

func TestControllerAbortStagedGenerationRetainsPreparationFence(t *testing.T) {
	t.Parallel()
	controller := NewController()
	backend := controllerBackend(t, newRecordingInserter(), map[string]AdapterConfig{
		"configured": {
			Source:      SourceChat,
			Mode:        ModeEventOnly,
			ChannelType: "test",
		},
	})
	require.NoError(t, controller.Prepare(backend.ConnectorNames()))
	staged, err := controller.Stage(backend)
	require.NoError(t, err)
	waiting := asyncAdmission(
		controller,
		context.Background(),
		testMessage("configured", "aborted-message"),
	)
	assertAdmissionWaiting(t, waiting)

	staged.Abort()
	staged.Abort()
	assertAdmissionWaiting(t, waiting)
	require.NoError(t, controller.CancelPreparation())
	result := receiveAdmission(t, waiting)
	require.NoError(t, result.err)
	assert.True(t, result.forward)
}

func TestControllerDeactivateFencesOldAndNextAndDrains(t *testing.T) {
	t.Parallel()
	oldStore := newRecordingInserter()
	oldStore.started = make(chan struct{})
	oldStore.release = make(chan struct{})
	controller := NewController()
	oldBackend := controllerBackend(t, oldStore, map[string]AdapterConfig{
		"old": {
			Source:      SourceChat,
			Mode:        ModeMirror,
			ChannelType: "test",
		},
	})
	require.NoError(t, controller.Prepare(oldBackend.ConnectorNames()))
	oldGeneration, err := controller.Activate(oldBackend)
	require.NoError(t, err)

	inflight := asyncAdmission(
		controller,
		context.Background(),
		testMessage("old", "inflight"),
	)
	select {
	case <-oldStore.started:
	case <-time.After(2 * time.Second):
		t.Fatal("in-flight insert did not start")
	}

	deactivated := make(chan error, 1)
	go func() {
		deactivated <- controller.Deactivate(
			context.Background(),
			oldGeneration,
			[]string{"next"},
		)
	}()
	require.Eventually(t, func() bool {
		return !controller.IsActive(oldGeneration)
	}, time.Second, time.Millisecond)
	select {
	case deactivateErr := <-deactivated:
		t.Fatalf("deactivation returned before insert drain: %v", deactivateErr)
	default:
	}

	oldWaiting := asyncAdmission(
		controller,
		context.Background(),
		testMessage("old", "after-fence"),
	)
	nextWaiting := asyncAdmission(
		controller,
		context.Background(),
		testMessage("next", "after-fence"),
	)
	assertAdmissionWaiting(t, oldWaiting)
	assertAdmissionWaiting(t, nextWaiting)

	unrelated, err := controller.AdmitInbound(
		context.Background(),
		testMessage("unrelated", ""),
	)
	require.NoError(t, err)
	assert.True(t, unrelated)
	internal := testMessage("old", "")
	internal.ChannelOrigin = false
	forward, err := controller.AdmitInbound(context.Background(), internal)
	require.NoError(t, err)
	assert.True(t, forward)

	nextBackend := controllerBackend(t, newRecordingInserter(), map[string]AdapterConfig{
		"next": {
			Source:      SourceChat,
			Mode:        ModeEventOnly,
			ChannelType: "test",
		},
	})
	_, err = controller.Activate(nextBackend)
	require.ErrorIs(t, err, ErrGenerationDraining)

	close(oldStore.release)
	inflightResult := receiveAdmission(t, inflight)
	require.NoError(t, inflightResult.err)
	assert.True(t, inflightResult.forward)
	require.NoError(t, <-deactivated)

	nextStore := newRecordingInserter()
	nextBackend = controllerBackend(t, nextStore, map[string]AdapterConfig{
		"next": {
			Source:      SourceChat,
			Mode:        ModeEventOnly,
			ChannelType: "test",
		},
	})
	nextGeneration, err := controller.Activate(nextBackend)
	require.NoError(t, err)

	oldResult := receiveAdmission(t, oldWaiting)
	require.NoError(t, oldResult.err)
	assert.True(t, oldResult.forward, "old-only connector must pass after replacement")
	nextResult := receiveAdmission(t, nextWaiting)
	require.NoError(t, nextResult.err)
	assert.False(t, nextResult.forward)
	assert.Len(t, nextStore.recordedInserts(), 1)

	require.NoError(t, controller.Deactivate(context.Background(), nextGeneration, nil))
	require.NoError(t, controller.CommitDisabled())
}

func TestControllerStaleGenerationCannotAffectReplacement(t *testing.T) {
	t.Parallel()
	controller := NewController()
	firstStore := newRecordingInserter()
	first := controllerBackend(t, firstStore, map[string]AdapterConfig{
		"same": {Source: SourceChat, Mode: ModeMirror, ChannelType: "first"},
	})
	require.NoError(t, controller.Prepare(first.ConnectorNames()))
	firstGeneration, err := controller.Activate(first)
	require.NoError(t, err)
	require.NoError(t, controller.Deactivate(
		context.Background(),
		firstGeneration,
		[]string{"same"},
	))

	secondStore := newRecordingInserter()
	second := controllerBackend(t, secondStore, map[string]AdapterConfig{
		"same": {Source: SourceChat, Mode: ModeEventOnly, ChannelType: "second"},
	})
	secondGeneration, err := controller.Activate(second)
	require.NoError(t, err)

	require.NoError(t, controller.Deactivate(
		context.Background(),
		firstGeneration,
		[]string{"stale-trap"},
	))
	assert.True(t, controller.IsActive(secondGeneration))

	forward, err := controller.AdmitInbound(
		context.Background(),
		testMessage("same", "replacement"),
	)
	require.NoError(t, err)
	assert.False(t, forward)
	assert.Len(t, firstStore.recordedInserts(), 0)
	assert.Len(t, secondStore.recordedInserts(), 1)

	staleTrap, err := controller.AdmitInbound(
		context.Background(),
		testMessage("stale-trap", ""),
	)
	require.NoError(t, err)
	assert.True(t, staleTrap, "stale cleanup must not publish a new wait fence")

	otherController := NewController()
	require.NoError(t, otherController.Prepare(second.ConnectorNames()))
	otherGeneration, err := otherController.Activate(second)
	require.NoError(t, err)
	require.ErrorIs(
		t,
		controller.Deactivate(context.Background(), otherGeneration, nil),
		ErrGenerationNotOwned,
	)
	require.NoError(t, otherController.Deactivate(
		context.Background(),
		otherGeneration,
		nil,
	))
	require.NoError(t, otherController.CommitDisabled())

	require.NoError(t, controller.Deactivate(
		context.Background(),
		secondGeneration,
		nil,
	))
	require.NoError(t, controller.CommitDisabled())
}

func TestControllerCloseReleasesWaitingConfiguredMessagesWithError(t *testing.T) {
	t.Parallel()
	controller := NewController()
	require.NoError(t, controller.Prepare([]string{"configured"}))
	waiting := asyncAdmission(
		controller,
		context.Background(),
		testMessage("configured", "waiting"),
	)
	assertAdmissionWaiting(t, waiting)

	require.NoError(t, controller.Close(context.Background()))
	result := receiveAdmission(t, waiting)
	assert.False(t, result.forward)
	require.ErrorIs(t, result.err, ErrControllerClosed)

	forward, err := controller.AdmitInbound(
		context.Background(),
		testMessage("configured", "late"),
	)
	assert.False(t, forward)
	require.ErrorIs(t, err, ErrControllerClosed)

	forward, err = controller.AdmitInbound(
		context.Background(),
		testMessage("unrelated", ""),
	)
	require.NoError(t, err)
	assert.True(t, forward)
	internal := testMessage("configured", "")
	internal.ChannelOrigin = false
	forward, err = controller.AdmitInbound(context.Background(), internal)
	require.NoError(t, err)
	assert.True(t, forward)

	_, err = controller.Activate(controllerBackend(
		t,
		newRecordingInserter(),
		map[string]AdapterConfig{
			"configured": {Source: SourceChat, Mode: ModeMirror, ChannelType: "test"},
		},
	))
	require.ErrorIs(t, err, ErrControllerClosed)
	require.ErrorIs(t, controller.Prepare([]string{"configured"}), ErrControllerClosed)
	require.ErrorIs(t, controller.CommitDisabled(), ErrControllerClosed)
}

func TestControllerCloseDrainsActiveInsert(t *testing.T) {
	t.Parallel()
	store := newRecordingInserter()
	store.started = make(chan struct{})
	store.release = make(chan struct{})
	controller := NewController()
	backend := controllerBackend(t, store, map[string]AdapterConfig{
		"configured": {Source: SourceChat, Mode: ModeMirror, ChannelType: "test"},
	})
	require.NoError(t, controller.Prepare(backend.ConnectorNames()))
	generation, err := controller.Activate(backend)
	require.NoError(t, err)
	inflight := asyncAdmission(
		controller,
		context.Background(),
		testMessage("configured", "inflight-close"),
	)
	select {
	case <-store.started:
	case <-time.After(2 * time.Second):
		t.Fatal("in-flight insert did not start")
	}

	closed := make(chan error, 1)
	go func() { closed <- controller.Close(context.Background()) }()
	require.Eventually(t, func() bool {
		return !controller.IsActive(generation)
	}, time.Second, time.Millisecond)
	select {
	case err := <-closed:
		t.Fatalf("close returned before insert drain: %v", err)
	default:
	}

	late := asyncAdmission(
		controller,
		context.Background(),
		testMessage("configured", "late-close"),
	)
	lateResult := receiveAdmission(t, late)
	assert.False(t, lateResult.forward)
	require.ErrorIs(t, lateResult.err, ErrControllerClosed)

	close(store.release)
	inflightResult := receiveAdmission(t, inflight)
	require.NoError(t, inflightResult.err)
	assert.True(t, inflightResult.forward)
	require.NoError(t, <-closed)
	require.NoError(t, controller.Close(context.Background()))
}

func TestControllerValidation(t *testing.T) {
	t.Parallel()
	controller := NewController()
	require.Error(t, controller.Prepare([]string{""}))
	_, err := controller.Activate(nil)
	require.Error(t, err)
	require.NoError(t, controller.Deactivate(context.Background(), Generation{}, nil))

	backend := controllerBackend(t, newRecordingInserter(), map[string]AdapterConfig{
		"configured": {Source: SourceChat, Mode: ModeMirror, ChannelType: "test"},
	})
	_, err = controller.Activate(backend)
	require.ErrorIs(t, err, ErrPreparedConnectorsMismatch)
	require.NoError(t, controller.Prepare([]string{"other"}))
	_, err = controller.Activate(backend)
	require.ErrorIs(t, err, ErrPreparedConnectorsMismatch)
	require.NoError(t, controller.Prepare(backend.ConnectorNames()))
	generation, err := controller.Activate(backend)
	require.NoError(t, err)
	_, err = controller.Activate(backend)
	require.ErrorIs(t, err, ErrActiveGeneration)
	require.ErrorIs(t, controller.CommitDisabled(), ErrActiveGeneration)
	require.NoError(t, controller.Prepare([]string{"other"}))
	require.ErrorIs(
		t,
		controller.Deactivate(context.Background(), generation, nil),
		ErrPreparedConnectorsMismatch,
	)
	assert.True(t, controller.IsActive(generation))
	require.NoError(t, controller.Prepare(nil))
	require.NoError(t, controller.Deactivate(context.Background(), generation, nil))
	require.NoError(t, controller.CommitDisabled())

	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	require.NoError(t, controller.Prepare([]string{"configured"}))
	forward, err := controller.AdmitInbound(
		canceled,
		testMessage("configured", "canceled"),
	)
	assert.False(t, forward)
	require.True(t, errors.Is(err, context.Canceled))
	require.NoError(t, controller.CommitDisabled())
}
