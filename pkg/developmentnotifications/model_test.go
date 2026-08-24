package developmentnotifications

import (
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDefaultPriority(t *testing.T) {
	t.Parallel()
	cases := map[Reason]Priority{
		ReasonProviderOutcomeUnknown: PriorityCritical,
		ReasonImplementationBlocked:  PriorityHigh,
		ReasonPublicationApproval:    PriorityHigh,
		ReasonCharterAmbiguity:       PriorityMedium,
		ReasonScopeException:         PriorityMedium,
		ReasonSteeringScopeChange:    PriorityMedium,
	}
	for reason, want := range cases {
		assert.Equal(t, want, DefaultPriority(reason), reason)
	}
	assert.Empty(t, DefaultPriority("unknown"))
}

func TestUpsertGenerationLifecycle(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 8, 24, 10, 0, 0, 0, time.UTC)
	draft := testDraft()

	created, err := Upsert(nil, draft, now)
	require.NoError(t, err)
	assert.True(t, created.Created)
	assert.True(t, created.Changed)
	assert.True(t, created.NewGeneration)
	assert.Equal(t, StatusOpen, created.Notification.Status)
	assert.Equal(t, PriorityMedium, created.Notification.Priority)
	assert.Equal(t, uint64(1), created.Notification.Version)
	require.NoError(t, created.Notification.Validate())

	retry, err := Upsert(&created.Notification, draft, now.Add(time.Minute))
	require.NoError(t, err)
	assert.False(t, retry.Created)
	assert.False(t, retry.Changed)
	assert.False(t, retry.NewGeneration)
	assert.Equal(t, created.Notification, retry.Notification)

	read, changed, err := MarkRead(created.Notification, true, now.Add(time.Minute))
	require.NoError(t, err)
	require.True(t, changed)
	snoozed, changed, err := Snooze(read, now.Add(24*time.Hour), now.Add(2*time.Minute))
	require.NoError(t, err)
	require.True(t, changed)

	metadataDraft := draft
	metadataDraft.Title = "Changed evidence title"
	metadataDraft.Phase = "validation"
	updated, err := Upsert(&snoozed, metadataDraft, now.Add(3*time.Minute))
	require.NoError(t, err)
	assert.True(t, updated.Changed)
	assert.False(t, updated.NewGeneration)
	assert.True(t, updated.Notification.Read, "same generation preserves user state")
	assert.NotNil(t, updated.Notification.SnoozedUntil)
	assert.Equal(t, "Changed evidence title", updated.Notification.Title)

	nextDraft := metadataDraft
	nextDraft.Generation = 2
	reopened, err := Upsert(&updated.Notification, nextDraft, now.Add(4*time.Minute))
	require.NoError(t, err)
	assert.True(t, reopened.Changed)
	assert.True(t, reopened.NewGeneration)
	assert.Equal(t, StatusOpen, reopened.Notification.Status)
	assert.False(t, reopened.Notification.Read)
	assert.Nil(t, reopened.Notification.SnoozedUntil)
	assert.Nil(t, reopened.Notification.ResolvedAt)
	assert.Nil(t, reopened.Notification.ArchivedAt)
	assert.Equal(t, uint64(2), reopened.Notification.Generation)
}

func TestUpsertRejectsStaleOrChangedIdentity(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 8, 24, 10, 0, 0, 0, time.UTC)
	draft := testDraft()
	draft.Generation = 2
	created, err := Upsert(nil, draft, now)
	require.NoError(t, err)

	stale := draft
	stale.Generation = 1
	_, err = Upsert(&created.Notification, stale, now.Add(time.Minute))
	assert.ErrorIs(t, err, ErrStaleGeneration)

	changedIdentity := draft
	changedIdentity.SourceKey = "gate/another"
	_, err = Upsert(&created.Notification, changedIdentity, now.Add(time.Minute))
	assert.ErrorIs(t, err, ErrInvalidNotification)

	_, err = Upsert(&created.Notification, draft, now.Add(-time.Minute))
	assert.ErrorIs(t, err, ErrInvalidNotification)
}

func TestUserStateTransitions(t *testing.T) {
	t.Parallel()
	createdAt := time.Date(2026, 8, 24, 10, 0, 0, 0, time.UTC)
	result, err := Upsert(nil, testDraft(), createdAt)
	require.NoError(t, err)
	n := result.Notification

	marked, changed, err := MarkRead(n, true, createdAt.Add(time.Minute))
	require.NoError(t, err)
	assert.True(t, changed)
	assert.True(t, marked.Read)
	version := marked.Version
	marked, changed, err = MarkRead(marked, true, createdAt.Add(2*time.Minute))
	require.NoError(t, err)
	assert.False(t, changed)
	assert.Equal(t, version, marked.Version)

	_, _, err = Snooze(marked, createdAt.Add(time.Minute), createdAt.Add(2*time.Minute))
	assert.ErrorIs(t, err, ErrInvalidTransition)
	snoozed, changed, err := Snooze(marked, createdAt.Add(time.Hour), createdAt.Add(2*time.Minute))
	require.NoError(t, err)
	assert.True(t, changed)
	assert.True(t, snoozed.IsSnoozed(createdAt.Add(3*time.Minute)))
	assert.False(t, snoozed.IsSnoozed(createdAt.Add(2*time.Hour)))

	cleared, changed, err := ClearSnooze(snoozed, createdAt.Add(3*time.Minute))
	require.NoError(t, err)
	assert.True(t, changed)
	assert.Nil(t, cleared.SnoozedUntil)

	_, _, err = Archive(cleared, createdAt.Add(4*time.Minute))
	assert.ErrorIs(t, err, ErrInvalidTransition)
	resolved, changed, err := Resolve(cleared, createdAt.Add(4*time.Minute))
	require.NoError(t, err)
	assert.True(t, changed)
	assert.Equal(t, StatusResolved, resolved.Status)
	assert.NotNil(t, resolved.ResolvedAt)

	_, _, err = Snooze(resolved, createdAt.Add(2*time.Hour), createdAt.Add(5*time.Minute))
	assert.ErrorIs(t, err, ErrInvalidTransition)
	resolvedAgain, changed, err := ReconcileActive(resolved, false, createdAt.Add(5*time.Minute))
	require.NoError(t, err)
	assert.False(t, changed)
	assert.Equal(t, resolved, resolvedAgain)

	archived, changed, err := Archive(resolved, createdAt.Add(6*time.Minute))
	require.NoError(t, err)
	assert.True(t, changed)
	assert.Equal(t, StatusArchived, archived.Status)
	assert.NotNil(t, archived.ArchivedAt)
	archivedAgain, changed, err := Archive(archived, createdAt.Add(7*time.Minute))
	require.NoError(t, err)
	assert.False(t, changed)
	assert.Equal(t, archived, archivedAgain)
}

func TestReconcileActive(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 8, 24, 10, 0, 0, 0, time.UTC)
	result, err := Upsert(nil, testDraft(), now)
	require.NoError(t, err)

	active, changed, err := ReconcileActive(result.Notification, true, now.Add(time.Minute))
	require.NoError(t, err)
	assert.False(t, changed)
	assert.Equal(t, result.Notification, active)

	resolved, changed, err := ReconcileActive(result.Notification, false, now.Add(time.Minute))
	require.NoError(t, err)
	assert.True(t, changed)
	assert.Equal(t, StatusResolved, resolved.Status)
}

func TestRetention(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 8, 24, 10, 0, 0, 0, time.UTC)
	created, err := Upsert(nil, testDraft(), now)
	require.NoError(t, err)

	deadline, err := RetentionDeadline(created.Notification, DefaultResolvedRetention)
	require.NoError(t, err)
	assert.Nil(t, deadline, "open items persist indefinitely")

	resolved, _, err := Resolve(created.Notification, now.Add(time.Hour))
	require.NoError(t, err)
	deadline, err = RetentionDeadline(resolved, DefaultResolvedRetention)
	require.NoError(t, err)
	require.NotNil(t, deadline)
	assert.Equal(t, now.Add(time.Hour+DefaultResolvedRetention), *deadline)
	eligible, err := EligibleForDeletion(resolved, deadline.Add(-time.Nanosecond), DefaultResolvedRetention)
	require.NoError(t, err)
	assert.False(t, eligible)
	eligible, err = EligibleForDeletion(resolved, *deadline, DefaultResolvedRetention)
	require.NoError(t, err)
	assert.True(t, eligible)

	archived, _, err := Archive(resolved, now.Add(2*time.Hour))
	require.NoError(t, err)
	eligible, err = EligibleForDeletion(archived, now.Add(2*time.Hour), DefaultResolvedRetention)
	require.NoError(t, err)
	assert.True(t, eligible, "archive opts into immediate deletion")

	_, err = RetentionDeadline(resolved, -time.Second)
	assert.ErrorIs(t, err, ErrInvalidNotification)
}

func TestNotificationValidationIntentAndTerminalInvariants(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 8, 24, 10, 0, 0, 0, time.UTC)
	result, err := Upsert(nil, testDraft(), now)
	require.NoError(t, err)

	invalid := result.Notification
	invalid.SourceKind = SourcePullRequest
	assert.ErrorIs(t, invalid.Validate(), ErrInvalidNotification)

	invalid = result.Notification
	invalid.Status = StatusResolved
	assert.ErrorIs(t, invalid.Validate(), ErrInvalidNotification)

	invalid = result.Notification
	invalid.ResolvedAt = ptrTime(now)
	assert.ErrorIs(t, invalid.Validate(), ErrInvalidNotification)

	pickup := testDraft()
	pickup.Intent = IntentPickupPR
	pickup.SourceKind = SourcePullRequest
	pickup.SourceKey = "pr/123"
	_, err = Upsert(nil, pickup, now)
	require.NoError(t, err)

	pickup.SourceKind = SourceIssue
	_, err = Upsert(nil, pickup, now)
	assert.True(t, errors.Is(err, ErrInvalidNotification))
}

func testDraft() Draft {
	return Draft{
		ID:          "dnt_01J00000000000000000000000",
		SourceKey:   "workspace/devw_01/gate/scope/42",
		Generation:  1,
		WorkspaceID: "devw_01J00000000000000000000000",
		Repository:  "sipeed/picoclaw",
		Intent:      IntentImplementFeature,
		SourceKind:  SourceIssue,
		Phase:       "triage",
		Reason:      ReasonScopeException,
		Title:       "Scope decision required",
		Summary:     "Candidate touches a neighboring subsystem.",
		Target:      Target{Panel: "scope", EntityID: "finding_42"},
	}
}

func ptrTime(value time.Time) *time.Time { return &value }
