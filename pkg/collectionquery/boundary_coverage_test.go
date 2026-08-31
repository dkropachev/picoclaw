package collectionquery

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type unsupportedExpression struct{}

func (unsupportedExpression) collectionQueryExpression() {}

func TestEvaluationRejectsInvalidResolversAndResolvedValues(t *testing.T) {
	schema := testSchema(t)
	now := time.Date(2026, 8, 25, 12, 0, 0, 0, time.FixedZone("test", 2*60*60))
	query, err := Parse("name = alpha", schema)
	require.NoError(t, err)

	for _, test := range []struct {
		name    string
		at      time.Time
		resolve Resolver
	}{
		{name: "zero evaluation time", resolve: func(Field, time.Time) (FieldValue, bool) { return StringValue("alpha"), true }},
		{name: "nil resolver", at: now},
		{name: "missing field", at: now, resolve: func(Field, time.Time) (FieldValue, bool) { return FieldValue{}, false }},
		{name: "wrong field type", at: now, resolve: func(Field, time.Time) (FieldValue, bool) { return NumberValue(1), true }},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, matchErr := query.Match(test.at, test.resolve)
			assert.ErrorIs(t, matchErr, ErrFieldResolution)
		})
	}

	all, err := Parse("", schema)
	require.NoError(t, err)
	matched, err := all.Match(now, func(Field, time.Time) (FieldValue, bool) {
		t.Fatal("an ALL query must not resolve fields")
		return FieldValue{}, false
	})
	require.NoError(t, err)
	assert.True(t, matched)

	_, err = evaluateExpression(schema, Predicate{
		Field: "missing", Operator: OperatorEqual, Values: []Value{{Kind: ValueString, Text: "x"}},
	}, now.UTC(), func(Field, time.Time) (FieldValue, bool) { return StringValue("x"), true })
	assert.ErrorIs(t, err, ErrFieldResolution)
	_, err = evaluateExpression(schema, unsupportedExpression{}, now.UTC(), func(Field, time.Time) (FieldValue, bool) {
		return StringValue("x"), true
	})
	assert.ErrorIs(t, err, ErrFieldResolution)
}

func TestEvaluationShortCircuitAndErrorPropagation(t *testing.T) {
	schema := testSchema(t)
	now := time.Date(2026, 8, 25, 12, 0, 0, 0, time.UTC)
	for _, test := range []struct {
		name      string
		input     string
		values    map[Field]FieldValue
		want      bool
		wantCalls int
		wantErr   bool
	}{
		{
			name: "and short circuits false left", input: "enabled = false AND name = alpha",
			values: map[Field]FieldValue{"enabled": BooleanValue(true)}, wantCalls: 1,
		},
		{
			name: "or short circuits true left", input: "enabled = true OR name = alpha",
			values: map[Field]FieldValue{"enabled": BooleanValue(true)}, want: true, wantCalls: 1,
		},
		{
			name: "right side error propagates", input: "enabled = true AND name = alpha",
			values: map[Field]FieldValue{"enabled": BooleanValue(true)}, wantCalls: 2, wantErr: true,
		},
		{
			name: "left side error propagates", input: "name = alpha AND enabled = true",
			values: map[Field]FieldValue{}, wantCalls: 1, wantErr: true,
		},
		{
			name: "negated error propagates", input: "NOT name = alpha",
			values: map[Field]FieldValue{}, wantCalls: 1, wantErr: true,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			query, err := Parse(test.input, schema)
			require.NoError(t, err)
			calls := 0
			matched, err := query.Match(now, func(field Field, _ time.Time) (FieldValue, bool) {
				calls++
				value, ok := test.values[field]
				return value, ok
			})
			if test.wantErr {
				assert.ErrorIs(t, err, ErrFieldResolution)
			} else {
				require.NoError(t, err)
				assert.Equal(t, test.want, matched)
			}
			assert.Equal(t, test.wantCalls, calls)
		})
	}
}

func TestResolvedValueNormalizationAndStableComparisonBoundaries(t *testing.T) {
	schema := testSchema(t)
	declaration := func(name Field) FieldSchema {
		field, ok := schema.lookup(name)
		require.True(t, ok)
		return field
	}

	normalized, err := normalizeFieldValue(declaration("name"), StringValue("MiXeD"))
	require.NoError(t, err)
	assert.Equal(t, "mixed", normalized.Text)
	normalized, err = normalizeFieldValue(declaration("status"), EnumValue(" ACTIVE "))
	require.NoError(t, err)
	assert.Equal(t, "active", normalized.Text)
	normalized, err = normalizeFieldValue(declaration("score"), NumberValue(math.Copysign(0, -1)))
	require.NoError(t, err)
	assert.False(t, math.Signbit(normalized.Number))
	offsetTime := time.Date(2026, 8, 25, 14, 0, 0, 0, time.FixedZone("plus-two", 2*60*60))
	normalized, err = normalizeFieldValue(declaration("created"), TimestampValue(offsetTime))
	require.NoError(t, err)
	assert.Equal(t, time.UTC, normalized.Timestamp.Location())

	invalidUTF8 := string([]byte{0xff})
	for _, test := range []struct {
		name  string
		field FieldSchema
		value FieldValue
	}{
		{name: "wrong type", field: declaration("name"), value: NumberValue(1)},
		{name: "invalid UTF-8", field: declaration("name"), value: StringValue(invalidUTF8)},
		{name: "unknown enum", field: declaration("status"), value: EnumValue("missing")},
		{name: "NaN", field: declaration("score"), value: NumberValue(math.NaN())},
		{name: "infinity", field: declaration("score"), value: NumberValue(math.Inf(1))},
		{name: "zero timestamp", field: declaration("created"), value: TimestampValue(time.Time{})},
		{name: "unknown field type", field: FieldSchema{Name: "other", Type: "other"}, value: FieldValue{Type: "other"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, normalizeErr := normalizeFieldValue(test.field, test.value)
			assert.ErrorIs(t, normalizeErr, ErrFieldResolution)
		})
	}

	comparisonCases := []struct {
		name        string
		field       FieldSchema
		left, right FieldValue
		want        int
	}{
		{
			name:  "string case normalization",
			field: declaration("name"),
			left:  StringValue("Alpha"),
			right: StringValue("BETA"),
			want:  -1,
		},
		{
			name:  "enum equality",
			field: declaration("status"),
			left:  EnumValue("active"),
			right: EnumValue("ACTIVE"),
			want:  0,
		},
		{
			name:  "boolean equality",
			field: declaration("enabled"),
			left:  BooleanValue(true),
			right: BooleanValue(true),
			want:  0,
		},
		{
			name:  "false before true",
			field: declaration("enabled"),
			left:  BooleanValue(false),
			right: BooleanValue(true),
			want:  -1,
		},
		{
			name:  "true after false",
			field: declaration("enabled"),
			left:  BooleanValue(true),
			right: BooleanValue(false),
			want:  1,
		},
		{name: "number equality", field: declaration("score"), left: NumberValue(1), right: NumberValue(1), want: 0},
		{name: "number before", field: declaration("score"), left: NumberValue(1), right: NumberValue(2), want: -1},
		{name: "number after", field: declaration("score"), left: NumberValue(2), right: NumberValue(1), want: 1},
		{
			name:  "time equality",
			field: declaration("created"),
			left:  TimestampValue(offsetTime),
			right: TimestampValue(offsetTime.UTC()),
			want:  0,
		},
		{
			name:  "time before",
			field: declaration("created"),
			left:  TimestampValue(offsetTime.Add(-time.Second)),
			right: TimestampValue(offsetTime),
			want:  -1,
		},
		{
			name:  "time after",
			field: declaration("created"),
			left:  TimestampValue(offsetTime),
			right: TimestampValue(offsetTime.Add(-time.Second)),
			want:  1,
		},
	}
	for _, test := range comparisonCases {
		t.Run(test.name, func(t *testing.T) {
			comparison, compareErr := CompareValues(test.field, test.left, test.right)
			require.NoError(t, compareErr)
			assert.Equal(t, test.want, comparison)
		})
	}
	_, err = CompareValues(declaration("name"), NumberValue(1), StringValue("x"))
	assert.ErrorIs(t, err, ErrFieldResolution)
	_, err = CompareValues(declaration("name"), StringValue("x"), NumberValue(1))
	assert.ErrorIs(t, err, ErrFieldResolution)
	_, err = CompareValues(
		FieldSchema{Name: "other", Type: "other"},
		FieldValue{Type: "other"},
		FieldValue{Type: "other"},
	)
	assert.ErrorIs(t, err, ErrFieldResolution)
}

func TestOrderedComparisonAndSortableCursorRepresentations(t *testing.T) {
	now := time.Date(2026, 8, 25, 12, 0, 0, 123, time.UTC)
	operators := []Operator{
		OperatorEqual, OperatorNotEqual, OperatorGreater, OperatorGreaterEq,
		OperatorLess, OperatorLessEq, Operator("invalid"),
	}
	want := []bool{false, true, true, true, false, false, false}
	for index, operator := range operators {
		assert.Equal(t, want[index], compareOrderedFloat(2, 1, operator), operator)
		assert.Equal(t, want[index], compareOrderedTime(now, now.Add(-time.Second), operator), operator)
	}
	assert.False(t, comparePredicate(StringValue("a"), Predicate{
		Operator: OperatorGreater, Values: []Value{{Kind: ValueString, Text: "b"}},
	}, now))
	assert.False(t, comparePredicate(FieldValue{Type: "other"}, Predicate{
		Operator: OperatorEqual, Values: []Value{{Kind: ValueString, Text: "b"}},
	}, now))
	assert.False(t, comparePredicate(StringValue("a"), Predicate{
		Operator: Operator("invalid"), Values: []Value{{Kind: ValueString, Text: "a"}},
	}, now))
	assert.True(t, comparePredicate(TimestampValue(now.Add(-time.Hour)), Predicate{
		Operator: OperatorEqual,
		Values:   []Value{{Kind: ValueRelativeTimestamp, Text: "-1h", TimeOffset: -time.Hour}},
	}, now))

	schema := testSchema(t)
	fields := map[Field]FieldValue{
		"name": StringValue("Alpha"), "status": EnumValue("active"),
		"enabled": BooleanValue(true), "score": NumberValue(1.25), "created": TimestampValue(now),
	}
	for fieldName, value := range fields {
		field, ok := schema.lookup(fieldName)
		require.True(t, ok)
		encoded, err := sortableString(field, value)
		require.NoError(t, err)
		decoded, err := fieldValueFromSortable(field, encoded)
		require.NoError(t, err)
		normalized, err := normalizeFieldValue(field, value)
		require.NoError(t, err)
		assert.Equal(t, normalized, decoded, fieldName)
	}

	invalidType := FieldSchema{Name: "other", Type: "other"}
	_, err := sortableString(invalidType, FieldValue{Type: "other"})
	assert.ErrorIs(t, err, ErrFieldResolution)
	invalidCases := []struct {
		field   FieldSchema
		encoded string
	}{
		{field: mustField(t, schema, "name"), encoded: "Upper"},
		{field: mustField(t, schema, "name"), encoded: string([]byte{0xff})},
		{field: mustField(t, schema, "status"), encoded: ""},
		{field: mustField(t, schema, "status"), encoded: "ACTIVE"},
		{field: mustField(t, schema, "status"), encoded: "missing"},
		{field: mustField(t, schema, "enabled"), encoded: "TRUE"},
		{field: mustField(t, schema, "score"), encoded: "1.0"},
		{field: mustField(t, schema, "score"), encoded: "NaN"},
		{field: mustField(t, schema, "created"), encoded: "2026-08-25T14:00:00+02:00"},
		{field: invalidType, encoded: "value"},
	}
	for _, test := range invalidCases {
		_, err := fieldValueFromSortable(test.field, test.encoded)
		assert.ErrorIs(t, err, ErrInvalidCursor, test.encoded)
	}
}

func mustField(t *testing.T, schema Schema, name Field) FieldSchema {
	t.Helper()
	field, ok := schema.lookup(name)
	require.True(t, ok)
	return field
}

func TestParserLexingAndBoundaryErrors(t *testing.T) {
	schema := testSchema(t)
	var nilQueryError *QueryError
	assert.Equal(t, ErrInvalidQuery.Error(), nilQueryError.Error())

	valid := []string{
		`name = "backslash\\quote\"single\'line\nnext\rtab\t"`,
		"name = 'single\\'quote'",
		"created = -1m", "created = -1h", "created = -1d", "created = -1w",
	}
	for _, input := range valid {
		_, err := Parse(input, schema)
		require.NoError(t, err, input)
	}

	invalid := []string{
		"!", "!x", `name = "trailing\`, "status IN active", "status IN (active",
		"()", "name", "enabled = maybe", "status = )", "status = active OR", "(status = active",
		"(status = active OR )",
		"ORDER BY", "ORDER BY status ASC,", "ORDER BY status SIDEWAYS",
		"created = -9223372036854775807w",
	}
	for _, input := range invalid {
		_, err := Parse(input, schema)
		assert.ErrorIs(t, err, ErrInvalidQuery, input)
	}

	_, err := Parse("", Schema{})
	assert.ErrorIs(t, err, ErrInvalidSchema)
	assert.Equal(t, 0, newQueryError(-10, "").Position)
	assert.Equal(t, "invalid query", safeQueryErrorMessage("\n\x00"))
	assert.Equal(t, strings.Repeat("a", MaxQueryErrorMessageLen-1), safeQueryErrorMessage(
		strings.Repeat("a", MaxQueryErrorMessageLen-1)+"é",
	))
	assert.Equal(t, 0, firstInvalidUTF8Byte("valid utf-8"))
	queryParser := parser{inputBytes: 2}
	queryErr, ok := queryParser.errorAt(token{pos: 99}, "bad").(*QueryError)
	require.True(t, ok)
	assert.Equal(t, 2, queryErr.Position)
}

func TestProgrammaticQueryValidationAndCanonicalFailurePaths(t *testing.T) {
	schema := testSchema(t)
	_, err := NewQuery(Schema{}, nil, nil)
	assert.ErrorIs(t, err, ErrInvalidSchema)

	order := []SortField{{Field: "name", Direction: Ascending}}
	query, err := NewQuery(schema, nil, order)
	require.NoError(t, err)
	order[0].Field = "mutated"
	assert.Equal(t, Field("name"), query.Order[0].Field)

	invalidQueries := []Query{
		{},
		{schema: schema, Order: []SortField{{Field: "missing", Direction: Ascending}}},
		{schema: schema, Order: []SortField{{Field: "name", Direction: "SIDEWAYS"}}},
		{
			schema: schema,
			Order:  []SortField{{Field: "name", Direction: Ascending}, {Field: "name", Direction: Descending}},
		},
		{schema: schema, Filter: unsupportedExpression{}},
		{schema: schema, Filter: LogicalExpression{Operator: LogicalAnd, Left: nil, Right: Predicate{}}},
		{schema: schema, Filter: LogicalExpression{Operator: LogicalAnd, Left: Predicate{}, Right: nil}},
	}
	for _, invalid := range invalidQueries {
		assert.ErrorIs(t, invalid.Validate(), ErrInvalidQuery)
		assert.Empty(t, invalid.Canonical())
		assert.Empty(t, invalid.Fingerprint())
	}

	tooManySorts := Query{schema: schema, Order: make([]SortField, MaxQuerySortFields+1)}
	assert.ErrorIs(t, tooManySorts.Validate(), ErrInvalidQuery)
	assert.Equal(t, "", canonicalExpression(unsupportedExpression{}))
	Predicate{}.collectionQueryExpression()
	LogicalExpression{}.collectionQueryExpression()
	Negation{}.collectionQueryExpression()
	assert.Equal(t, `"fallback"`, canonicalValue(Value{Kind: ValueString, Text: "fallback"}))
	assert.Equal(t, `"fallback"`, canonicalValue(Value{Kind: 255, Text: "fallback"}))
	assert.Equal(t, `"2026-08-25T12:00:00Z"`, canonicalValue(Value{
		Kind: ValueTimestamp, Timestamp: time.Date(2026, 8, 25, 12, 0, 0, 0, time.UTC),
	}))
	assert.Equal(t, "-1h", canonicalValue(Value{Kind: ValueRelativeTimestamp, Text: "-1h"}))
	negated, err := Parse("NOT status = active", schema)
	require.NoError(t, err)
	assert.Contains(t, negated.Canonical(), "NOT (")

	values := make([]Value, MaxQueryINValues+1)
	for index := range values {
		values[index] = Value{Kind: ValueString, Text: "x"}
	}
	predicateCases := []Predicate{
		{Field: "name", Operator: OperatorEqual},
		{
			Field:    "name",
			Operator: OperatorEqual,
			Values:   []Value{{Kind: ValueString, Text: "a"}, {Kind: ValueString, Text: "b"}},
		},
		{Field: "name", Operator: OperatorIn, Values: values},
		{Field: "name", Operator: OperatorEqual, Values: []Value{{Kind: ValueString, Text: "UPPER"}}},
		{Field: "status", Operator: OperatorEqual, Values: []Value{{Kind: ValueString, Text: "missing"}}},
		{Field: "enabled", Operator: OperatorEqual, Values: []Value{{Kind: ValueString, Text: "true"}}},
		{Field: "score", Operator: OperatorEqual, Values: []Value{{Kind: ValueNumber, Number: math.NaN()}}},
		{Field: "created", Operator: OperatorEqual, Values: []Value{{Kind: ValueTimestamp}}},
		{Field: "created", Operator: OperatorEqual, Values: []Value{{Kind: ValueString, Text: "today"}}},
		{
			Field:    "created",
			Operator: OperatorEqual,
			Values:   []Value{{Kind: ValueRelativeTimestamp, Text: "-1h", TimeOffset: -time.Minute}},
		},
	}
	for _, predicate := range predicateCases {
		_, newQueryErr := NewQuery(schema, predicate, nil)
		assert.ErrorIs(t, newQueryErr, ErrInvalidQuery)
	}

	deep := Expression(
		Predicate{Field: "status", Operator: OperatorEqual, Values: []Value{{Kind: ValueString, Text: "active"}}},
	)
	for range MaxQueryDepth + 1 {
		deep = Negation{Expression: deep}
	}
	_, err = NewQuery(schema, deep, nil)
	assert.ErrorIs(t, err, ErrInvalidQuery)

	// Shared subtrees are counted on each visit so callers cannot smuggle an
	// unbounded graph through a small number of allocations.
	node := Expression(
		Predicate{Field: "status", Operator: OperatorEqual, Values: []Value{{Kind: ValueString, Text: "active"}}},
	)
	for range 10 {
		node = LogicalExpression{Operator: LogicalAnd, Left: node, Right: node}
	}
	_, err = NewQuery(schema, node, nil)
	assert.ErrorIs(t, err, ErrInvalidQuery)
}

func TestCollectionQueryCoverageCushionValidationEdges(t *testing.T) {
	nodes := MaxQueryPredicates * (MaxQueryDepth + 2)
	predicates := 0
	err := validateExpression(testSchema(t), Predicate{}, 0, &predicates, &nodes)
	assert.EqualError(t, err, "expression is too complex")

	err = validateValue(
		FieldSchema{Name: "opaque", Type: FieldType("opaque")},
		Value{Kind: ValueString, Text: "value"},
	)
	assert.EqualError(t, err, "invalid field type")
}

func TestSchemaAdditionalBoundaries(t *testing.T) {
	valid := FieldSchema{Name: "name", Type: TypeString, Sortable: true}
	tests := []Schema{
		{
			Fields: []FieldSchema{
				{Name: "Not", Type: TypeString, Operators: []Operator{OperatorEqual}, Sortable: true},
			},
			DefaultOrder: []SortField{{Field: "Not", Direction: Ascending}},
		},
		{
			Fields: []FieldSchema{
				{
					Name:      "name",
					Type:      TypeString,
					Operators: append(append([]Operator{}, allOperators...), OperatorEqual),
					Sortable:  true,
				},
			},
			DefaultOrder: []SortField{{Field: "name", Direction: Ascending}},
		},
		{
			Fields: []FieldSchema{
				{Name: "name", Type: TypeString, Operators: []Operator{"invalid"}, Sortable: true},
			},
			DefaultOrder: []SortField{{Field: "name", Direction: Ascending}},
		},
		{
			Fields: []FieldSchema{
				{
					Name:            "name",
					Type:            TypeString,
					Operators:       []Operator{OperatorEqual},
					Sortable:        true,
					SuggestedValues: []string{""},
				},
			},
			DefaultOrder: []SortField{{Field: "name", Direction: Ascending}},
		},
		{
			Fields: []FieldSchema{
				{
					Name:            "name",
					Type:            TypeString,
					Operators:       []Operator{OperatorEqual},
					Sortable:        true,
					SuggestedValues: []string{strings.Repeat("x", MaxSuggestedValueBytes+1)},
				},
			},
			DefaultOrder: []SortField{{Field: "name", Direction: Ascending}},
		},
		{
			Fields: []FieldSchema{
				{
					Name:            "status",
					Type:            TypeEnum,
					Operators:       []Operator{OperatorEqual},
					Sortable:        true,
					SuggestedValues: []string{"ACTIVE"},
				},
			},
			DefaultOrder: []SortField{{Field: "status", Direction: Ascending}},
		},
		{
			Fields:       []FieldSchema{valid},
			DefaultOrder: []SortField{{Field: "name", Direction: Ascending}, {Field: "name", Direction: Descending}},
		},
	}
	for _, schema := range tests {
		assert.ErrorIs(t, schema.Validate(), ErrInvalidSchema)
	}
	_, err := NewSchema([]FieldSchema{valid}, []SortField{
		{Field: "name", Direction: Ascending}, {Field: "name", Direction: Descending},
	})
	assert.ErrorIs(t, err, ErrInvalidSchema)
	assert.False(t, validFieldName(""))
	assert.False(t, validFieldName(Field(strings.Repeat("x", MaxFieldNameBytes+1))))
	assert.False(t, validFieldName(Field(string([]byte{0xff}))))
	assert.False(t, validFieldName("not"))
	assert.False(t, Operator("invalid").valid())
}

func TestCursorWireAndPaginationBoundaryFailures(t *testing.T) {
	schema := testSchema(t)
	query, err := Parse("ORDER BY name ASC", schema)
	require.NoError(t, err)
	now := time.Date(2026, 8, 25, 12, 0, 0, 0, time.UTC)
	item := pageTestItem{ID: "id", Name: "alpha", Status: "draft", Created: now}
	options := pageTestOptions()

	_, err = Paginate([]pageTestItem{item}, query, "%%%", 1, now, options)
	assert.ErrorIs(t, err, ErrInvalidCursor)
	for _, invalidOptions := range []PageOptions[pageTestItem]{
		{},
		{Resolve: options.Resolve},
		{ID: options.ID},
	} {
		_, pageErr := Paginate([]pageTestItem{item}, query, "", 1, now, invalidOptions)
		assert.ErrorIs(t, pageErr, ErrInvalidPage)
		_, sortErr := SortItems([]pageTestItem{item}, query, now, invalidOptions)
		assert.ErrorIs(t, sortErr, ErrInvalidPage)
		_, cursorErr := CursorFor(query, item, now, invalidOptions)
		assert.ErrorIs(t, cursorErr, ErrInvalidCursor)
	}
	_, err = SortItems([]pageTestItem{item}, query, time.Time{}, options)
	assert.ErrorIs(t, err, ErrInvalidPage)
	_, err = CursorFor(query, item, time.Time{}, options)
	assert.ErrorIs(t, err, ErrInvalidCursor)

	badID := options
	badID.ID = func(pageTestItem) (string, error) { return "", errors.New("no ID") }
	_, err = SortItems([]pageTestItem{item}, query, now, badID)
	assert.ErrorIs(t, err, ErrInvalidPage)

	filterQuery, err := Parse("name = alpha ORDER BY created ASC", schema)
	require.NoError(t, err)
	missingFilter := options
	missingFilter.Resolve = func(item pageTestItem, field Field, _ time.Time) (FieldValue, bool) {
		if field == "created" {
			return TimestampValue(item.Created), true
		}
		return FieldValue{}, false
	}
	_, err = Paginate([]pageTestItem{item}, filterQuery, "", 1, now, missingFilter)
	assert.ErrorIs(t, err, ErrInvalidPage)

	oversizedItem := item
	oversizedItem.Name = strings.Repeat("x", MaxCursorValueBytes+1)
	_, err = SortItems([]pageTestItem{oversizedItem}, query, now, options)
	assert.ErrorIs(t, err, ErrInvalidPage)

	validationCalls := 0
	changingValidation := options
	changingValidation.ValidateID = func(string) bool {
		validationCalls++
		return validationCalls <= 2
	}
	secondItem := item
	secondItem.ID = "second"
	secondItem.Name = "beta"
	_, err = Paginate([]pageTestItem{item, secondItem}, query, "", 1, now, changingValidation)
	assert.ErrorIs(t, err, ErrInvalidCursor)

	noClone := options
	noClone.Clone = nil
	sorted, err := SortItems([]pageTestItem{item}, query, now, noClone)
	require.NoError(t, err)
	assert.Equal(t, item, sorted[0])

	cursor, err := CursorFor(query, item, now, options)
	require.NoError(t, err)
	raw, err := base64.RawURLEncoding.DecodeString(cursor)
	require.NoError(t, err)
	var wire cursorWire
	require.NoError(t, json.Unmarshal(raw, &wire))

	invalidWires := []string{
		`{"v":1,"query":"x","evaluated_at":"2026-08-25T12:00:00Z","values":["alpha"],"id":"id","extra":true}`,
		string(raw) + `{}`,
	}
	for _, invalidWire := range invalidWires {
		_, decodeErr := DecodeCursor(base64.RawURLEncoding.EncodeToString([]byte(invalidWire)), query, nil)
		assert.ErrorIs(t, decodeErr, ErrInvalidCursor)
	}

	wire.ID = "bad\nID"
	encodedWire, err := json.Marshal(wire)
	require.NoError(t, err)
	_, err = DecodeCursor(base64.RawURLEncoding.EncodeToString(encodedWire), query, nil)
	assert.ErrorIs(t, err, ErrInvalidCursor)
	wire.ID = "id"
	wire.EvaluatedAt = time.Date(2026, 8, 25, 12, 0, 0, 0, time.FixedZone("offset", 60*60))
	encodedWire, err = json.Marshal(wire)
	require.NoError(t, err)
	_, err = DecodeCursor(base64.RawURLEncoding.EncodeToString(encodedWire), query, nil)
	assert.ErrorIs(t, err, ErrInvalidCursor)

	assert.False(t, validStableID("", nil))
	assert.False(t, validStableID(strings.Repeat("x", MaxStableIDBytes+1), nil))
	assert.False(t, validStableID(string([]byte{0xff}), nil))
	assert.False(t, validStableID("line\nbreak", nil))
	assert.False(t, validCursorTime(time.Time{}))
}

func TestCursorEncodingInternalBoundaries(t *testing.T) {
	schema := testSchema(t)
	query, err := Parse("ORDER BY name ASC", schema)
	require.NoError(t, err)
	now := time.Date(2026, 8, 25, 12, 0, 0, 0, time.UTC)

	_, err = encodePreparedCursor(query, preparedItem[int]{id: ""}, now, nil)
	assert.ErrorIs(t, err, ErrInvalidCursor)
	_, err = encodePreparedCursor(
		query,
		preparedItem[int]{id: "id", values: []FieldValue{StringValue("ok")}},
		time.Time{},
		nil,
	)
	assert.ErrorIs(t, err, ErrInvalidCursor)
	_, err = encodePreparedCursor(query, preparedItem[int]{
		id: "id", values: []FieldValue{StringValue(strings.Repeat("x", MaxCursorValueBytes+1))},
	}, now, nil)
	assert.ErrorIs(t, err, ErrInvalidCursor)

	_, err = decodeCursorValues(Cursor{Values: nil}, query)
	assert.ErrorIs(t, err, ErrInvalidCursor)
	_, err = decodeCursorValues(Cursor{Values: []string{strings.Repeat("x", MaxCursorValueBytes+1)}}, query)
	assert.ErrorIs(t, err, ErrInvalidCursor)
	_, err = decodeCursorValues(Cursor{Values: []string{"Upper"}}, query)
	assert.ErrorIs(t, err, ErrInvalidCursor)

	corrupt := query
	corrupt.Order = []SortField{{Field: "missing", Direction: Ascending}}
	_, err = decodeCursorValues(Cursor{Values: []string{"x"}}, corrupt)
	assert.ErrorIs(t, err, ErrInvalidCursor)
	_, err = prepareItem(1, corrupt, now, PageOptions[int]{
		ID: func(int) (string, error) { return "id", nil },
		Resolve: func(int, Field, time.Time) (FieldValue, bool) {
			return StringValue("x"), true
		},
	})
	assert.ErrorIs(t, err, ErrFieldResolution)

	wideSchema, err := NewSchema([]FieldSchema{
		{Name: "first", Type: TypeString, Sortable: true},
		{Name: "second", Type: TypeString, Sortable: true},
		{Name: "third", Type: TypeString, Sortable: true},
	}, []SortField{
		{Field: "first", Direction: Ascending},
		{Field: "second", Direction: Ascending},
		{Field: "third", Direction: Ascending},
	})
	require.NoError(t, err)
	wideQuery, err := NewQuery(wideSchema, nil, wideSchema.DefaultOrder)
	require.NoError(t, err)
	wideValue := StringValue(strings.Repeat("x", 1400))
	_, err = encodePreparedCursor(wideQuery, preparedItem[int]{
		id: "id", values: []FieldValue{wideValue, wideValue, wideValue},
	}, now, nil)
	assert.ErrorIs(t, err, ErrInvalidCursor)
}
