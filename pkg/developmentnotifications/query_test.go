package developmentnotifications

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseQueryCanonicalAndFingerprint(t *testing.T) {
	t.Parallel()
	first, err := ParseQuery(`STATUS = OPEN and PRIORITY in (HIGH, medium) order by UPDATED desc`)
	require.NoError(t, err)
	second, err := ParseQuery(`status=open AND priority IN (high,medium) ORDER BY updated DESC`)
	require.NoError(t, err)
	assert.Equal(t, first.Canonical(), second.Canonical())
	assert.Equal(t, first.Fingerprint(), second.Fingerprint())
	assert.Equal(t, []SortField{{Field: FieldUpdated, Direction: Descending}}, first.EffectiveOrder())

	empty, err := ParseQuery("")
	require.NoError(t, err)
	explicitDefault, err := ParseQuery("ORDER BY updated DESC")
	require.NoError(t, err)
	assert.Equal(t, empty.Fingerprint(), explicitDefault.Fingerprint())

	caseVariant, err := ParseQuery(`repository ~ "SIPEED/"`)
	require.NoError(t, err)
	lowerVariant, err := ParseQuery(`repository ~ "sipeed/"`)
	require.NoError(t, err)
	assert.Equal(t, caseVariant.Fingerprint(), lowerVariant.Fingerprint())
}

func TestQueryBooleanPrecedenceAndNegation(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 8, 24, 12, 0, 0, 0, time.UTC)
	openRead := mustNotification(t, "dnt_open_read", now.Add(-time.Hour))
	openRead.Read = true
	resolvedUnread, _, err := Resolve(mustNotification(t, "dnt_resolved", now.Add(-2*time.Hour)), now.Add(-time.Hour))
	require.NoError(t, err)
	resolvedRead := resolvedUnread
	resolvedRead.ID = "dnt_resolved_read"
	resolvedRead.SourceKey = "source/dnt_resolved_read"
	resolvedRead.Read = true

	query, err := ParseQuery("status = open OR status = resolved AND read = false")
	require.NoError(t, err)
	assert.True(t, query.Match(openRead, now), "AND binds more tightly than OR")
	assert.True(t, query.Match(resolvedUnread, now))
	assert.False(t, query.Match(resolvedRead, now))

	query, err = ParseQuery("NOT (status = resolved OR read = true)")
	require.NoError(t, err)
	assert.False(t, query.Match(openRead, now))
	assert.False(t, query.Match(resolvedUnread, now))
	openUnread := mustNotification(t, "dnt_open_unread", now)
	assert.True(t, query.Match(openUnread, now))
}

func TestQueryEvaluatorFieldsAndOperators(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 8, 24, 12, 0, 0, 0, time.UTC)
	n := mustNotification(t, "dnt_fields", now.Add(-48*time.Hour))
	n.Repository = "Sipeed/PicoClaw"
	n.Phase = "validation"
	n.Priority = PriorityHigh
	n.Title = "CI validation needs approval"
	n.Summary = "Review candidate before publication"
	n.Read = true
	snoozed, _, err := Snooze(n, now.Add(time.Hour), now.Add(-time.Hour))
	require.NoError(t, err)
	n = snoozed

	cases := []struct {
		query string
		want  bool
	}{
		{`status = open`, true},
		{`status != resolved`, true},
		{`priority IN (critical, high)`, true},
		{`priority NOT IN (high, medium)`, false},
		{`reason = scope_exception`, true},
		{`repository ~ "sipeed/"`, true},
		{`repository !~ "other"`, true},
		{`workspace = devw_test`, true},
		{`intent = implement_feature`, true},
		{`source = issue`, true},
		{`phase = VALIDATION`, true},
		{`read = true`, true},
		{`read IN (false, true)`, true},
		{`read NOT IN (true)`, false},
		{`snoozed = true`, true},
		{`text ~ "approval"`, true},
		{`created >= -7d`, true},
		{`created < -24h`, true},
		{`updated <= "2026-08-24T11:00:00Z"`, true},
		{`created >= 2026-08-22`, true},
	}
	for _, test := range cases {
		query, parseErr := ParseQuery(test.query)
		require.NoError(t, parseErr, test.query)
		assert.Equal(t, test.want, query.Match(n, now), test.query)
	}
}

func TestQueryValidateRejectsManuallyMalformedAST(t *testing.T) {
	t.Parallel()
	tests := []Query{
		{Filter: Predicate{Field: FieldStatus, Operator: OperatorEqual}},
		{Filter: Predicate{Field: "unknown", Operator: OperatorEqual, Values: []Value{{Kind: ValueString, Text: "x"}}}},
		{Filter: LogicalExpression{Operator: "X", Left: Predicate{}, Right: Predicate{}}},
		{Filter: LogicalExpression{Operator: LogicalAnd}},
		{Filter: Negation{}},
		{Order: []SortField{{Field: FieldUpdated, Direction: "SIDEWAYS"}}},
		{Order: []SortField{{Field: FieldText, Direction: Ascending}}},
	}
	for _, query := range tests {
		assert.ErrorIs(t, query.Validate(), ErrInvalidQuery)
	}
	cycle := &LogicalExpression{Operator: LogicalAnd}
	cycle.Left = cycle
	cycle.Right = Predicate{
		Field: FieldStatus, Operator: OperatorEqual,
		Values: []Value{{Kind: ValueString, Text: "open"}},
	}
	cyclicQuery := Query{Filter: cycle}
	assert.ErrorIs(t, cyclicQuery.Validate(), ErrInvalidQuery)
	assert.Empty(t, cyclicQuery.Canonical())
	assert.Empty(t, cyclicQuery.Fingerprint())

	valid, err := ParseQuery("status = open")
	require.NoError(t, err)
	require.NoError(t, valid.Validate())
}

func TestQueryStringEscapesAndCaseInsensitiveEquality(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 8, 24, 12, 0, 0, 0, time.UTC)
	n := mustNotification(t, "dnt_escape", now)
	n.Summary = "Owner said: \"ship it\"\nAfter validation"
	query, err := ParseQuery(`text ~ "ship it\"\nAfter"`)
	require.NoError(t, err)
	assert.True(t, query.Match(n, now))

	query, err = ParseQuery(`repository = "SIPEED/PICOCLAW"`)
	require.NoError(t, err)
	assert.True(t, query.Match(n, now))
}

func TestParseQueryRejectsInvalidSyntaxAndTypes(t *testing.T) {
	t.Parallel()
	tests := []string{
		"unknown = value",
		"status ~ open",
		"read > true",
		"created IN (2026-08-24)",
		"status = made_up",
		"status IN ()",
		"status = open trailing",
		"status = open ORDER BY text ASC",
		"status = open ORDER BY updated",
		"status = open ORDER BY updated DESC, updated ASC",
		"created >= yesterday",
		"repository = ''",
		"status = open; DROP TABLE notifications",
		"text ~ \"unterminated",
		"text ~ \"bad\\qescape\"",
		"! status = open",
	}
	for _, input := range tests {
		_, err := ParseQuery(input)
		assert.ErrorIs(t, err, ErrInvalidQuery, input)
		var queryErr *QueryError
		assert.True(t, errors.As(err, &queryErr), input)
	}
}

func TestParseQueryBounds(t *testing.T) {
	t.Parallel()
	_, err := ParseQuery(strings.Repeat("x", MaxQueryBytes+1))
	assert.ErrorIs(t, err, ErrInvalidQuery)

	_, err = ParseQuery(strings.Repeat("NOT ", MaxQueryDepth+1) + "status = open")
	assert.ErrorIs(t, err, ErrInvalidQuery)

	predicates := make([]string, MaxQueryPredicates+1)
	for index := range predicates {
		predicates[index] = "status = open"
	}
	_, err = ParseQuery(strings.Join(predicates, " AND "))
	assert.ErrorIs(t, err, ErrInvalidQuery)

	values := make([]string, MaxQueryINValues+1)
	for index := range values {
		values[index] = "open"
	}
	_, err = ParseQuery("status IN (" + strings.Join(values, ",") + ")")
	assert.ErrorIs(t, err, ErrInvalidQuery)

	_, err = ParseQuery("ORDER BY status ASC, priority DESC, updated DESC, created ASC")
	assert.ErrorIs(t, err, ErrInvalidQuery)

	_, err = ParseQuery("created >= -999999999999999999999999999w")
	assert.ErrorIs(t, err, ErrInvalidQuery)
}

func TestQueryErrorPosition(t *testing.T) {
	t.Parallel()
	input := "status = open AND nope = x"
	_, err := ParseQuery(input)
	require.Error(t, err)
	var queryErr *QueryError
	require.ErrorAs(t, err, &queryErr)
	assert.Equal(t, strings.Index(input, "nope"), queryErr.Position)
	assert.Contains(t, queryErr.Error(), fmt.Sprintf("byte %d", queryErr.Position))
}

func mustNotification(t *testing.T, id string, now time.Time) Notification {
	t.Helper()
	draft := testDraft()
	draft.ID = id
	draft.SourceKey = "source/" + id
	draft.WorkspaceID = "devw_test"
	result, err := Upsert(nil, draft, now)
	require.NoError(t, err)
	return result.Notification
}
