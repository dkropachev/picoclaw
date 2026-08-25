package developmentnotifications

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	collectionquery "github.com/sipeed/picoclaw/pkg/collectionquery"
)

type unsupportedNotificationExpression struct{}

func (unsupportedNotificationExpression) evaluate(Notification, time.Time) bool { return false }
func (unsupportedNotificationExpression) canonical() string                     { return "unsupported" }

func TestNotificationCollectionAdapterConvertsEveryExpressionAndValueKind(t *testing.T) {
	now := testTime()
	predicate := Predicate{
		Field: FieldRead, Operator: OperatorIn,
		Values: []Value{{Kind: ValueBool, Bool: true}, {Kind: ValueBool}},
	}
	timePredicate := Predicate{
		Field: FieldCreated, Operator: OperatorGreaterEq,
		Values: []Value{{Kind: ValueTime, Time: now}},
	}
	relativePredicate := Predicate{
		Field: FieldUpdated, Operator: OperatorGreaterEq,
		Values: []Value{{Kind: ValueRelativeTime, Text: "-1h", TimeOffset: -time.Hour}},
	}
	stringPredicate := Predicate{
		Field: FieldRepository, Operator: OperatorContains,
		Values: []Value{{Kind: ValueString, Text: "repo"}},
	}
	legacy := Query{
		Filter: LogicalExpression{
			Operator: LogicalAnd,
			Left:     &predicate,
			Right: LogicalExpression{
				Operator: LogicalOr,
				Left:     Negation{Expression: &timePredicate},
				Right:    &LogicalExpression{Operator: LogicalAnd, Left: relativePredicate, Right: stringPredicate},
			},
		},
		Order: []SortField{{Field: FieldRepository, Direction: Ascending}},
	}
	require.NoError(t, legacy.Validate())
	shared, err := collectionQueryFromNotification(legacy)
	require.NoError(t, err)
	assert.Equal(t, []collectionquery.SortField{{
		Field: collectionquery.Field(FieldRepository), Direction: collectionquery.Ascending,
	}}, shared.Order)

	roundTrip, err := notificationQueryFromCollection(shared)
	require.NoError(t, err)
	assert.Equal(t, legacy.Canonical(), roundTrip.Canonical())
	assert.Equal(t, legacy.Fingerprint(), roundTrip.Fingerprint())

	values := []Value{
		{Kind: ValueBool, Bool: true},
		{Kind: ValueTime, Time: now},
		{Kind: ValueRelativeTime, Text: "-1h", TimeOffset: -time.Hour},
		{Kind: ValueString, Text: "repo"},
	}
	for _, value := range values {
		converted := collectionValueFromNotification(value)
		assert.Equal(t, value, notificationValueFromCollection(converted))
	}
	assert.Equal(t, Value{Kind: ValueString, Text: "ignored-number"}, notificationValueFromCollection(
		collectionquery.Value{Kind: collectionquery.ValueNumber, Text: "ignored-number", Number: 12},
	))

	for _, expression := range []Expression{
		predicate,
		&predicate,
		Negation{Expression: predicate},
		&Negation{Expression: predicate},
		LogicalExpression{Operator: LogicalOr, Left: predicate, Right: stringPredicate},
		&LogicalExpression{Operator: LogicalAnd, Left: predicate, Right: stringPredicate},
	} {
		convertedExpression, conversionErr := collectionExpressionFromNotification(expression)
		require.NoError(t, conversionErr)
		_, conversionErr = notificationExpressionFromCollection(convertedExpression)
		require.NoError(t, conversionErr)
	}
	converted, err := collectionExpressionFromNotification(nil)
	require.NoError(t, err)
	assert.Nil(t, converted)
}

func TestNotificationCollectionAdapterRejectsMalformedProgrammaticTrees(t *testing.T) {
	valid := Predicate{
		Field: FieldStatus, Operator: OperatorEqual,
		Values: []Value{{Kind: ValueString, Text: string(StatusOpen)}},
	}
	var nilPredicate *Predicate
	var nilLogical *LogicalExpression
	var nilNegation *Negation
	for _, expression := range []Expression{
		nilPredicate,
		nilLogical,
		nilNegation,
		unsupportedNotificationExpression{},
	} {
		_, err := collectionExpressionFromNotification(expression)
		assert.Error(t, err)
	}

	for _, expression := range []Expression{
		LogicalExpression{Operator: LogicalAnd, Left: unsupportedNotificationExpression{}, Right: valid},
		LogicalExpression{Operator: LogicalAnd, Left: valid, Right: unsupportedNotificationExpression{}},
		Negation{Expression: unsupportedNotificationExpression{}},
	} {
		_, err := collectionExpressionFromNotification(expression)
		assert.Error(t, err)
	}

	_, err := collectionQueryFromNotification(Query{Filter: unsupportedNotificationExpression{}})
	assert.ErrorIs(t, err, ErrInvalidQuery)

	sharedPredicate := collectionquery.Predicate{
		Field: collectionquery.Field(FieldStatus), Operator: collectionquery.OperatorEqual,
		Values: []collectionquery.Value{{Kind: collectionquery.ValueString, Text: string(StatusOpen)}},
	}
	for _, expression := range []collectionquery.Expression{
		&sharedPredicate,
		collectionquery.LogicalExpression{Operator: collectionquery.LogicalAnd, Left: &sharedPredicate, Right: sharedPredicate},
		collectionquery.LogicalExpression{Operator: collectionquery.LogicalAnd, Left: sharedPredicate, Right: &sharedPredicate},
		collectionquery.Negation{Expression: &sharedPredicate},
	} {
		_, conversionErr := notificationExpressionFromCollection(expression)
		assert.Error(t, conversionErr)
	}
	_, err = notificationQueryFromCollection(collectionquery.Query{Filter: &sharedPredicate})
	assert.Error(t, err)
	_, err = notificationQueryFromCollection(collectionquery.Query{Filter: collectionquery.LogicalExpression{
		Operator: "INVALID", Left: sharedPredicate, Right: sharedPredicate,
	}})
	assert.Error(t, err)
}

func TestNotificationPageResolverCoversAllowlistedProjection(t *testing.T) {
	now := testTime()
	notification := mustNotification(t, "dnt_projection", now)
	notification.Read = true
	notification.Repository = "Owner/Repo"
	notification.WorkspaceID = "workspace_1"
	notification.Phase = "implementation"
	notification.Title = "Title"
	notification.Summary = "Summary"
	options := notificationPageOptions()

	tests := []struct {
		field collectionquery.Field
		type_ collectionquery.FieldType
	}{
		{collectionquery.Field(FieldStatus), collectionquery.TypeEnum},
		{collectionquery.Field(FieldRead), collectionquery.TypeBoolean},
		{collectionquery.Field(FieldSnoozed), collectionquery.TypeBoolean},
		{collectionquery.Field(FieldPriority), collectionquery.TypeEnum},
		{collectionquery.Field(FieldReason), collectionquery.TypeEnum},
		{collectionquery.Field(FieldRepository), collectionquery.TypeString},
		{collectionquery.Field(FieldWorkspace), collectionquery.TypeString},
		{collectionquery.Field(FieldIntent), collectionquery.TypeEnum},
		{collectionquery.Field(FieldSource), collectionquery.TypeEnum},
		{collectionquery.Field(FieldPhase), collectionquery.TypeString},
		{collectionquery.Field(FieldCreated), collectionquery.TypeTimestamp},
		{collectionquery.Field(FieldUpdated), collectionquery.TypeTimestamp},
		{collectionquery.Field(FieldText), collectionquery.TypeString},
	}
	for _, test := range tests {
		value, ok := options.Resolve(notification, test.field, now)
		assert.True(t, ok, test.field)
		assert.Equal(t, test.type_, value.Type, test.field)
	}
	_, ok := options.Resolve(notification, "unknown", now)
	assert.False(t, ok)

	id, err := options.ID(notification)
	require.NoError(t, err)
	assert.Equal(t, notification.ID, id)
	invalid := notification
	invalid.ID = "invalid id"
	_, err = options.ID(invalid)
	assert.Error(t, err)
	assert.True(t, options.ValidateID(notification.ID))
	assert.False(t, options.ValidateID("invalid id"))

	comparison, handled := options.Compare(collectionquery.Field(FieldPriority),
		collectionquery.EnumValue(string(PriorityCritical)), collectionquery.EnumValue(string(PriorityLow)))
	assert.True(t, handled)
	assert.Equal(t, 3, comparison)
	comparison, handled = options.Compare(collectionquery.Field(FieldStatus),
		collectionquery.EnumValue(string(StatusOpen)), collectionquery.EnumValue(string(StatusResolved)))
	assert.False(t, handled)
	assert.Zero(t, comparison)
	assert.Equal(t, 0, priorityRank(Priority("unknown")))
}

func TestNotificationCursorCompatibilityValueBoundaries(t *testing.T) {
	now := testTime()
	tests := []struct {
		field Field
		value string
		want  bool
	}{
		{FieldRead, "true", true},
		{FieldRead, "TRUE", false},
		{FieldSnoozed, "false", true},
		{FieldCreated, now.Format(time.RFC3339Nano), true},
		{FieldCreated, "invalid", false},
		{FieldUpdated, "2026-08-25T08:00:00-04:00", false},
		{FieldStatus, string(StatusOpen), true},
		{FieldPriority, "invalid", false},
		{FieldReason, string(ReasonCharterAmbiguity), true},
		{FieldIntent, string(IntentImplementFeature), true},
		{FieldSource, string(SourceIssue), true},
		{FieldWorkspace, "workspace_1", true},
		{FieldWorkspace, "workspace with spaces", false},
		{FieldRepository, "owner/repo", true},
		{FieldRepository, "", false},
		{FieldRepository, string([]byte{0xff}), false},
		{FieldPhase, "implementation", true},
		{FieldPhase, "", false},
		{FieldText, "not-sortable", false},
	}
	for _, test := range tests {
		assert.Equal(
			t,
			test.want,
			validNotificationCursorValue(test.field, test.value),
			"%s=%q",
			test.field,
			test.value,
		)
	}
	assert.False(t, validNotificationCursorValue(FieldRepository, strings.Repeat("x", maxRepositoryBytes+1)))
	assert.True(t, utf8ValidBounded("value", 5))
	assert.False(t, utf8ValidBounded("", 5))
	assert.False(t, utf8ValidBounded("longer", 5))
	assert.False(t, utf8ValidBounded(string([]byte{0xff}), 5))
}

func TestNotificationPaginationAdapterMapsSharedFailures(t *testing.T) {
	now := testTime()
	query, err := ParseQuery("ORDER BY repository ASC")
	require.NoError(t, err)
	valid := mustNotification(t, "dnt_adapter", now)

	invalid := valid
	invalid.ID = "invalid id"
	_, err = PageNotifications([]Notification{invalid}, query, "", 1, now)
	assert.ErrorIs(t, err, ErrInvalidPage)
	_, err = SortNotifications([]Notification{invalid}, query, now)
	assert.ErrorIs(t, err, ErrInvalidPage)
	_, err = CursorFor(query, invalid, now)
	assert.ErrorIs(t, err, ErrInvalidCursor)

	invalidQuery := Query{Filter: unsupportedNotificationExpression{}}
	_, err = SortNotifications(nil, invalidQuery, now)
	assert.ErrorIs(t, err, ErrInvalidPage)
	_, err = CursorFor(invalidQuery, valid, now)
	assert.ErrorIs(t, err, ErrInvalidCursor)
	_, err = DecodeCursor("value", invalidQuery)
	assert.ErrorIs(t, err, ErrInvalidCursor)
	_, err = SortNotifications(nil, query, time.Time{})
	assert.ErrorIs(t, err, ErrInvalidPage)
	_, err = CursorFor(query, valid, time.Time{})
	assert.ErrorIs(t, err, ErrInvalidCursor)
	_, err = DecodeCursor("", query)
	assert.ErrorIs(t, err, ErrInvalidCursor)
	_, err = DecodeCursor(strings.Repeat("x", maxCursorBytes+1), query)
	assert.ErrorIs(t, err, ErrInvalidCursor)

	// An empty string is a valid shared string sort key but notifications put a
	// stricter non-empty bound on repository cursor values. This reaches the
	// compatibility validation after the shared decoder accepts the wire.
	shared, err := collectionQueryFromNotification(query)
	require.NoError(t, err)
	wire := cursorWire{
		Version: cursorVersion, QueryFingerprint: shared.Fingerprint(), EvaluatedAt: now,
		Values: []string{""}, ID: valid.ID,
	}
	raw, err := json.Marshal(wire)
	require.NoError(t, err)
	_, err = DecodeCursor(base64.RawURLEncoding.EncodeToString(raw), query)
	assert.ErrorIs(t, err, ErrInvalidCursor)

	assert.ErrorIs(t, errors.Unwrap(&QueryError{Message: "bad"}), ErrInvalidQuery)
}
