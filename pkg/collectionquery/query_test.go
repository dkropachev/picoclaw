package collectionquery

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func testSchema(t *testing.T) Schema {
	t.Helper()
	schema, err := NewSchema([]FieldSchema{
		{Name: "name", Type: TypeString, Sortable: true, SuggestedValues: []string{"alpha", "beta"}},
		{Name: "status", Type: TypeEnum, Sortable: true, SuggestedValues: []string{"draft", "active", "done"}},
		{Name: "enabled", Type: TypeBoolean, Sortable: true},
		{Name: "score", Type: TypeNumber, Sortable: true},
		{Name: "created", Type: TypeTimestamp, Sortable: true},
		{Name: "text", Type: TypeString, Sortable: false},
	}, []SortField{{Field: "created", Direction: Descending}})
	require.NoError(t, err)
	return schema
}

func TestSchemaProjectionNormalizationAndDetachment(t *testing.T) {
	fields := []FieldSchema{{
		Name: " Status ", Type: TypeEnum, Sortable: true,
		SuggestedValues: []string{" DRAFT ", "ACTIVE"},
	}}
	order := []SortField{{Field: "status", Direction: Ascending}}
	schema, err := NewSchema(fields, order)
	require.NoError(t, err)
	fields[0].Name = "mutated"
	order[0].Field = "mutated"

	assert.Equal(t, Field("status"), schema.Fields[0].Name)
	assert.Equal(t, []string{"draft", "active"}, schema.Fields[0].SuggestedValues)
	assert.Equal(t, defaultOperators(TypeEnum), schema.Fields[0].Operators)
	clone := schema.Clone()
	clone.Fields[0].SuggestedValues[0] = "changed"
	assert.Equal(t, "draft", schema.Fields[0].SuggestedValues[0])

	raw, err := json.Marshal(schema)
	require.NoError(t, err)
	assert.JSONEq(t, `{
      "fields":[{
        "name":"status","type":"enum",
        "operators":["=","!=","IN","NOT IN"],
        "sortable":true,"suggested_values":["draft","active"]
      }],
      "default_order":[{"field":"status","direction":"ASC"}]
    }`, string(raw))
}

func TestSchemaRejectsInvalidDeclarations(t *testing.T) {
	valid := FieldSchema{Name: "name", Type: TypeString, Sortable: true}
	tests := []struct {
		name   string
		fields []FieldSchema
		order  []SortField
	}{
		{name: "empty", order: []SortField{{Field: "name", Direction: Ascending}}},
		{
			name:   "bad name",
			fields: []FieldSchema{{Name: "1name", Type: TypeString}},
			order:  []SortField{{Field: "1name", Direction: Ascending}},
		},
		{
			name:   "reserved name",
			fields: []FieldSchema{{Name: "all", Type: TypeString, Sortable: true}},
			order:  []SortField{{Field: "all", Direction: Ascending}},
		},
		{
			name:   "unknown type",
			fields: []FieldSchema{{Name: "name", Type: "object"}},
			order:  []SortField{{Field: "name", Direction: Ascending}},
		},
		{
			name:   "duplicate field",
			fields: []FieldSchema{valid, valid},
			order:  []SortField{{Field: "name", Direction: Ascending}},
		},
		{
			name: "duplicate operator",
			fields: []FieldSchema{
				{Name: "name", Type: TypeString, Operators: []Operator{OperatorEqual, OperatorEqual}, Sortable: true},
			},
			order: []SortField{{Field: "name", Direction: Ascending}},
		},
		{
			name: "wrong operator",
			fields: []FieldSchema{
				{Name: "enabled", Type: TypeBoolean, Operators: []Operator{OperatorContains}, Sortable: true},
			},
			order: []SortField{{Field: "enabled", Direction: Ascending}},
		},
		{
			name:   "enum without values",
			fields: []FieldSchema{{Name: "status", Type: TypeEnum, Sortable: true}},
			order:  []SortField{{Field: "status", Direction: Ascending}},
		},
		{
			name: "duplicate values",
			fields: []FieldSchema{
				{Name: "status", Type: TypeEnum, Sortable: true, SuggestedValues: []string{"x", "X"}},
			},
			order: []SortField{{Field: "status", Direction: Ascending}},
		},
		{
			name: "control value",
			fields: []FieldSchema{
				{Name: "status", Type: TypeEnum, Sortable: true, SuggestedValues: []string{"bad\nvalue"}},
			},
			order: []SortField{{Field: "status", Direction: Ascending}},
		},
		{
			name:   "unsortable default",
			fields: []FieldSchema{{Name: "name", Type: TypeString}},
			order:  []SortField{{Field: "name", Direction: Ascending}},
		},
		{name: "missing default", fields: []FieldSchema{valid}},
		{
			name:   "bad direction",
			fields: []FieldSchema{valid},
			order:  []SortField{{Field: "name", Direction: "SIDEWAYS"}},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := NewSchema(test.fields, test.order)
			assert.ErrorIs(t, err, ErrInvalidSchema)
		})
	}

	manyFields := make([]FieldSchema, MaxSchemaFields+1)
	for index := range manyFields {
		manyFields[index] = FieldSchema{Name: Field(fmt.Sprintf("field_%d", index)), Type: TypeString, Sortable: true}
	}
	_, err := NewSchema(manyFields, []SortField{{Field: manyFields[0].Name, Direction: Ascending}})
	assert.ErrorIs(t, err, ErrInvalidSchema)

	manyValues := make([]string, MaxSuggestedValues+1)
	for index := range manyValues {
		manyValues[index] = fmt.Sprintf("v%d", index)
	}
	_, err = NewSchema(
		[]FieldSchema{{Name: "status", Type: TypeEnum, Sortable: true, SuggestedValues: manyValues}},
		[]SortField{{Field: "status", Direction: Ascending}},
	)
	assert.ErrorIs(t, err, ErrInvalidSchema)
}

func TestParseCanonicalTypingAndFingerprint(t *testing.T) {
	schema := testSchema(t)
	first, err := Parse(
		`STATUS = ACTIVE and SCORE >= 1.50 AND enabled IN (TRUE, false) ORDER BY SCORE desc, NAME asc`,
		schema,
	)
	require.NoError(t, err)
	second, err := Parse(
		`status=active AND score>=1.5 and enabled in (true,false) order by score DESC,name ASC`,
		schema,
	)
	require.NoError(t, err)
	assert.Equal(t,
		`((status = "active" AND score >= 1.5) AND enabled IN (true, false)) ORDER BY score DESC, name ASC`,
		first.Canonical(),
	)
	assert.Equal(t, first.Canonical(), second.Canonical())
	assert.Equal(t, first.Fingerprint(), second.Fingerprint())

	empty, err := Parse("", schema)
	require.NoError(t, err)
	explicit, err := Parse("ORDER BY created DESC", schema)
	require.NoError(t, err)
	assert.Equal(t, "ALL ORDER BY created DESC", empty.Canonical())
	assert.Equal(t, empty.Fingerprint(), explicit.Fingerprint())
	assert.Equal(t, schema, empty.Schema())
	reapplied, err := Parse(empty.Canonical(), schema)
	require.NoError(t, err)
	assert.Equal(t, empty.Fingerprint(), reapplied.Fingerprint())
	_, err = Parse("ALL AND status = active", schema)
	assert.ErrorIs(t, err, ErrInvalidQuery)

	negativeZero, err := Parse("score = -0", schema)
	require.NoError(t, err)
	positiveZero, err := Parse("score = 0", schema)
	require.NoError(t, err)
	assert.Equal(t, positiveZero.Fingerprint(), negativeZero.Fingerprint())
}

func TestQueryEvaluationAllTypesOperatorsAndPrecedence(t *testing.T) {
	schema := testSchema(t)
	now := time.Date(2026, 8, 25, 12, 0, 0, 0, time.UTC)
	values := map[Field]FieldValue{
		"name":    StringValue("Alpha Project"),
		"status":  EnumValue("active"),
		"enabled": BooleanValue(true),
		"score":   NumberValue(12.5),
		"created": TimestampValue(now.Add(-48 * time.Hour)),
		"text":    StringValue("Release candidate is ready"),
	}
	resolve := func(field Field, _ time.Time) (FieldValue, bool) {
		value, ok := values[field]
		return value, ok
	}
	tests := []struct {
		input string
		want  bool
	}{
		{`name = "ALPHA PROJECT"`, true},
		{`name != beta`, true},
		{`name ~ project`, true},
		{`name !~ beta`, true},
		{`name IN (beta, "alpha project")`, true},
		{`status NOT IN (draft, done)`, true},
		{`enabled = true`, true},
		{`enabled != false`, true},
		{`score = 12.5`, true},
		{`score != 10`, true},
		{`score > 12`, true},
		{`score >= 12.5`, true},
		{`score < 13`, true},
		{`score <= 12.5`, true},
		{`score IN (1, 12.5)`, true},
		{`created >= -7d`, true},
		{`created < -24h`, true},
		{`created IN (2026-08-23T12:00:00Z)`, true},
		{`text ~ ready`, true},
		{`status = draft OR status = active AND enabled = true`, true},
		{`NOT (status = done OR enabled = false)`, true},
		{`score > 20 OR enabled = false`, false},
	}
	for _, test := range tests {
		query, err := Parse(test.input, schema)
		require.NoError(t, err, test.input)
		matched, err := query.Match(now, resolve)
		require.NoError(t, err, test.input)
		assert.Equal(t, test.want, matched, test.input)
	}

	query, err := Parse("name = alpha", schema)
	require.NoError(t, err)
	_, err = query.Match(now, func(Field, time.Time) (FieldValue, bool) { return FieldValue{}, false })
	assert.ErrorIs(t, err, ErrFieldResolution)
	_, err = query.Match(now, func(Field, time.Time) (FieldValue, bool) { return NumberValue(1), true })
	assert.ErrorIs(t, err, ErrFieldResolution)
}

func TestParseRejectsSyntaxTypesAndReportsBytePositions(t *testing.T) {
	schema := testSchema(t)
	tests := []string{
		"unknown = value",
		"status ~ active",
		"enabled > true",
		"score ~ 1",
		"created ~ 2026-08-25",
		"status = missing",
		"status IN ()",
		"status = active trailing",
		"status = active ORDER BY text ASC",
		"status = active ORDER BY created",
		"status = active ORDER BY created DESC, created ASC",
		"created >= yesterday",
		"score = NaN",
		"name = ''",
		"status = active; DROP TABLE items",
		"text ~ \"unterminated",
		"text ~ \"bad\\qescape\"",
		"! status = active",
	}
	for _, input := range tests {
		_, err := Parse(input, schema)
		assert.ErrorIs(t, err, ErrInvalidQuery, input)
		var queryErr *QueryError
		assert.True(t, errors.As(err, &queryErr), input)
		assert.LessOrEqual(t, len(queryErr.Message), MaxQueryErrorMessageLen)
	}

	unicodeInput := "name = café AND nope = x"
	_, err := Parse(unicodeInput, schema)
	require.Error(t, err)
	var queryErr *QueryError
	require.ErrorAs(t, err, &queryErr)
	assert.Equal(t, strings.Index(unicodeInput, "nope"), queryErr.Position)
	assert.Contains(t, queryErr.Error(), fmt.Sprintf("byte %d", queryErr.Position))

	invalidUTF8 := "name = a" + string([]byte{0xff})
	_, err = Parse(invalidUTF8, schema)
	require.ErrorAs(t, err, &queryErr)
	assert.Equal(t, len("name = a"), queryErr.Position)
	assert.True(t, utf8.ValidString(queryErr.Message))

	safe := newQueryError(4, strings.Repeat("unsafe\n\x00é", 100))
	assert.LessOrEqual(t, len(safe.Message), MaxQueryErrorMessageLen)
	assert.NotContains(t, safe.Message, "\n")
	assert.NotContains(t, safe.Message, "\x00")
	assert.True(t, utf8.ValidString(safe.Message))
}

func TestParseAndProgrammaticQueryBounds(t *testing.T) {
	schema := testSchema(t)
	_, err := Parse(strings.Repeat("x", MaxQueryBytes+1), schema)
	assert.ErrorIs(t, err, ErrInvalidQuery)
	_, err = Parse(strings.Repeat("NOT ", MaxQueryDepth+1)+"status = active", schema)
	assert.ErrorIs(t, err, ErrInvalidQuery)
	_, err = Parse(strings.Repeat("(", MaxQueryDepth+1)+"status = active"+strings.Repeat(")", MaxQueryDepth+1), schema)
	assert.ErrorIs(t, err, ErrInvalidQuery)
	_, err = Parse(strings.Repeat("(", MaxQueryDepth)+"status = active"+strings.Repeat(")", MaxQueryDepth), schema)
	require.NoError(t, err)

	predicates := make([]string, MaxQueryPredicates+1)
	for index := range predicates {
		predicates[index] = "status = active"
	}
	_, err = Parse(strings.Join(predicates, " AND "), schema)
	assert.ErrorIs(t, err, ErrInvalidQuery)

	values := make([]string, MaxQueryINValues+1)
	for index := range values {
		values[index] = "active"
	}
	_, err = Parse("status IN ("+strings.Join(values, ",")+")", schema)
	assert.ErrorIs(t, err, ErrInvalidQuery)
	_, err = Parse("ORDER BY status ASC, score DESC, created DESC, name ASC", schema)
	assert.ErrorIs(t, err, ErrInvalidQuery)
	_, err = Parse("created >= -999999999999999999999999999w", schema)
	assert.ErrorIs(t, err, ErrInvalidQuery)

	acceptedPredicates := predicates[:MaxQueryPredicates]
	_, err = Parse(strings.Join(acceptedPredicates, " AND "), schema)
	require.NoError(t, err)
	acceptedValues := values[:MaxQueryINValues]
	_, err = Parse("status IN ("+strings.Join(acceptedValues, ",")+")", schema)
	require.NoError(t, err)
	_, err = Parse("ORDER BY status ASC, score DESC, created DESC", schema)
	require.NoError(t, err)

	_, err = NewQuery(schema, Predicate{
		Field: "missing", Operator: OperatorEqual,
		Values: []Value{{Kind: ValueString, Text: "x"}},
	}, nil)
	assert.ErrorIs(t, err, ErrInvalidQuery)
	_, err = NewQuery(schema, LogicalExpression{Operator: "X"}, nil)
	assert.ErrorIs(t, err, ErrInvalidQuery)
	_, err = NewQuery(schema, Negation{}, nil)
	assert.ErrorIs(t, err, ErrInvalidQuery)
	_, err = NewQuery(schema, nil, []SortField{{Field: "text", Direction: Ascending}})
	assert.ErrorIs(t, err, ErrInvalidQuery)
}
