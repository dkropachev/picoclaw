package developmentnotifications

import (
	"encoding/base64"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPageNotificationsStableKeyset(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 8, 24, 12, 0, 0, 0, time.UTC)
	all := []Notification{
		mustNotification(t, "dnt_a", now.Add(-5*time.Hour)),
		mustNotification(t, "dnt_b", now.Add(-4*time.Hour)),
		mustNotification(t, "dnt_c", now.Add(-3*time.Hour)),
		mustNotification(t, "dnt_d", now.Add(-2*time.Hour)),
		mustNotification(t, "dnt_e", now.Add(-time.Hour)),
	}
	query, err := ParseQuery("")
	require.NoError(t, err)

	first, err := PageNotifications(all, query, "", 2, now)
	require.NoError(t, err)
	require.Len(t, first.Notifications, 2)
	assert.Equal(t, []string{"dnt_e", "dnt_d"}, notificationIDs(first.Notifications))
	require.NotEmpty(t, first.Next)

	second, err := PageNotifications(all, query, first.Next, 2, now)
	require.NoError(t, err)
	assert.Equal(t, []string{"dnt_c", "dnt_b"}, notificationIDs(second.Notifications))
	require.NotEmpty(t, second.Next)

	third, err := PageNotifications(all, query, second.Next, 2, now)
	require.NoError(t, err)
	assert.Equal(t, []string{"dnt_a"}, notificationIDs(third.Notifications))
	assert.Empty(t, third.Next)

	// Cursor remains a true keyset if its boundary row was concurrently removed.
	withoutBoundary := []Notification{all[0], all[1], all[2], all[4]}
	afterRemoval, err := PageNotifications(withoutBoundary, query, first.Next, 10, now)
	require.NoError(t, err)
	assert.Equal(t, []string{"dnt_c", "dnt_b", "dnt_a"}, notificationIDs(afterRemoval.Notifications))
}

func TestPageNotificationsFiltersAndUsesIDTieBreak(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 8, 24, 12, 0, 0, 0, time.UTC)
	all := []Notification{
		mustNotification(t, "dnt_a", now),
		mustNotification(t, "dnt_c", now),
		mustNotification(t, "dnt_b", now),
	}
	all[0].Read = true
	query, err := ParseQuery("read = false ORDER BY updated DESC")
	require.NoError(t, err)
	page, err := PageNotifications(all, query, "", 10, now)
	require.NoError(t, err)
	assert.Equal(t, []string{"dnt_c", "dnt_b"}, notificationIDs(page.Notifications))
	assert.Empty(t, page.Next)
}

func TestSortNotificationsPrioritySemantics(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 8, 24, 12, 0, 0, 0, time.UTC)
	priorities := []Priority{PriorityLow, PriorityCritical, PriorityMedium, PriorityHigh}
	all := make([]Notification, len(priorities))
	for index, priority := range priorities {
		all[index] = mustNotification(t, "dnt_priority_"+string(rune('a'+index)), now)
		all[index].Priority = priority
	}
	query, err := ParseQuery("ORDER BY priority DESC, updated DESC")
	require.NoError(t, err)
	sorted, err := SortNotifications(all, query, now)
	require.NoError(t, err)
	assert.Equal(t, []Priority{
		PriorityCritical, PriorityHigh, PriorityMedium, PriorityLow,
	}, notificationPriorities(sorted))
}

func TestCompositeSortAcrossBooleanStringAndTime(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 8, 24, 12, 0, 0, 0, time.UTC)
	first := mustNotification(t, "dnt_composite_a", now.Add(-3*time.Hour))
	first.Repository = "z/repo"
	second := mustNotification(t, "dnt_composite_b", now.Add(-time.Hour))
	second.Repository = "a/repo"
	third := mustNotification(t, "dnt_composite_c", now.Add(-2*time.Hour))
	third.Repository = "a/repo"
	fourth := mustNotification(t, "dnt_composite_d", now)
	fourth.Repository = "a/repo"
	fourth.Read = true
	query, err := ParseQuery("ORDER BY read ASC, repository ASC, created ASC")
	require.NoError(t, err)
	all := []Notification{first, second, third, fourth}

	page, err := PageNotifications(all, query, "", 2, now)
	require.NoError(t, err)
	assert.Equal(t, []string{"dnt_composite_c", "dnt_composite_b"}, notificationIDs(page.Notifications))
	require.NotEmpty(t, page.Next)
	next, err := PageNotifications(all, query, page.Next, 2, now)
	require.NoError(t, err)
	assert.Equal(t, []string{"dnt_composite_a", "dnt_composite_d"}, notificationIDs(next.Notifications))
}

func TestCursorBoundToNormalizedQuery(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 8, 24, 12, 0, 0, 0, time.UTC)
	n := mustNotification(t, "dnt_cursor", now)
	query, err := ParseQuery("STATUS = open ORDER BY UPDATED desc")
	require.NoError(t, err)
	cursor, err := CursorFor(query, n, now)
	require.NoError(t, err)

	equivalent, err := ParseQuery("status=open order by updated DESC")
	require.NoError(t, err)
	decoded, err := DecodeCursor(cursor, equivalent)
	require.NoError(t, err)
	assert.Equal(t, n.ID, decoded.ID)
	assert.Equal(t, equivalent.Fingerprint(), decoded.QueryFingerprint)
	assert.Equal(t, now, decoded.EvaluatedAt)

	different, err := ParseQuery("status = resolved ORDER BY updated DESC")
	require.NoError(t, err)
	_, err = DecodeCursor(cursor, different)
	assert.ErrorIs(t, err, ErrInvalidCursor)

	raw, err := base64.RawURLEncoding.DecodeString(cursor)
	require.NoError(t, err)
	nonCanonical := base64.RawURLEncoding.EncodeToString(append(raw, ' '))
	_, err = DecodeCursor(nonCanonical, query)
	assert.ErrorIs(t, err, ErrInvalidCursor)

	invalidWire, err := json.Marshal(cursorWire{
		Version: cursorVersion, QueryFingerprint: query.Fingerprint(),
		EvaluatedAt: now, Values: []string{"not-a-time"}, ID: n.ID,
	})
	require.NoError(t, err)
	_, err = DecodeCursor(base64.RawURLEncoding.EncodeToString(invalidWire), query)
	assert.ErrorIs(t, err, ErrInvalidCursor)

	priorityQuery, err := ParseQuery("ORDER BY priority DESC")
	require.NoError(t, err)
	invalidEnumWire, err := json.Marshal(cursorWire{
		Version: cursorVersion, QueryFingerprint: priorityQuery.Fingerprint(),
		EvaluatedAt: now, Values: []string{"made_up"}, ID: n.ID,
	})
	require.NoError(t, err)
	_, err = DecodeCursor(base64.RawURLEncoding.EncodeToString(invalidEnumWire), priorityQuery)
	assert.ErrorIs(t, err, ErrInvalidCursor)
}

func TestKeysetCursorAnchorsRelativeTimeAndSnoozeEvaluation(t *testing.T) {
	t.Parallel()
	firstEvaluation := time.Date(2026, 8, 24, 12, 0, 0, 0, time.UTC)
	first := mustNotification(t, "dnt_anchor_first", firstEvaluation.Add(-time.Hour))
	second := mustNotification(t, "dnt_anchor_second", firstEvaluation.Add(-2*time.Hour))
	var err error
	first, _, err = Snooze(first, firstEvaluation.Add(30*time.Minute), firstEvaluation.Add(-30*time.Minute))
	require.NoError(t, err)
	second, _, err = Snooze(second, firstEvaluation.Add(30*time.Minute), firstEvaluation.Add(-30*time.Minute))
	require.NoError(t, err)
	query, err := ParseQuery("snoozed = true AND created >= -1d ORDER BY updated DESC")
	require.NoError(t, err)

	page, err := PageNotifications([]Notification{first, second}, query, "", 1, firstEvaluation)
	require.NoError(t, err)
	require.Len(t, page.Notifications, 1)
	require.NotEmpty(t, page.Next)

	// Wall clock passes beyond SnoozedUntil. Traversal still uses first page's
	// evaluation anchor, so second row cannot disappear between pages.
	next, err := PageNotifications(
		[]Notification{first, second}, query, page.Next, 1, firstEvaluation.Add(time.Hour),
	)
	require.NoError(t, err)
	assert.Len(t, next.Notifications, 1)
}

func TestPageNotificationsRejectsInvalidInputs(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 8, 24, 12, 0, 0, 0, time.UTC)
	query, err := ParseQuery("")
	require.NoError(t, err)
	valid := mustNotification(t, "dnt_valid", now)

	_, err = PageNotifications([]Notification{valid}, query, "", 0, now)
	assert.ErrorIs(t, err, ErrInvalidPage)
	_, err = PageNotifications([]Notification{valid}, query, "", MaxPageSize+1, now)
	assert.ErrorIs(t, err, ErrInvalidPage)
	_, err = PageNotifications([]Notification{valid}, query, "", 1, time.Time{})
	assert.ErrorIs(t, err, ErrInvalidPage)
	_, err = PageNotifications([]Notification{valid}, query, "not_base64", 1, now)
	assert.ErrorIs(t, err, ErrInvalidCursor)

	invalid := valid
	invalid.ID = "invalid id"
	_, err = PageNotifications([]Notification{invalid}, query, "", 1, now)
	assert.ErrorIs(t, err, ErrInvalidPage)
}

func TestKeysetExcludesRowsBeforeBoundaryAndIncludesRowsAfter(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 8, 24, 12, 0, 0, 0, time.UTC)
	boundary := mustNotification(t, "dnt_boundary", now.Add(-2*time.Hour))
	query, err := ParseQuery("ORDER BY updated DESC")
	require.NoError(t, err)
	cursor, err := CursorFor(query, boundary, now)
	require.NoError(t, err)
	all := []Notification{
		mustNotification(t, "dnt_new", now.Add(-time.Hour)),
		mustNotification(t, "dnt_old", now.Add(-3*time.Hour)),
	}
	page, err := PageNotifications(all, query, cursor, 10, now)
	require.NoError(t, err)
	assert.Equal(t, []string{"dnt_old"}, notificationIDs(page.Notifications))
}

func notificationIDs(notifications []Notification) []string {
	result := make([]string, len(notifications))
	for index := range notifications {
		result[index] = notifications[index].ID
	}
	return result
}

func notificationPriorities(notifications []Notification) []Priority {
	result := make([]Priority, len(notifications))
	for index := range notifications {
		result[index] = notifications[index].Priority
	}
	return result
}
