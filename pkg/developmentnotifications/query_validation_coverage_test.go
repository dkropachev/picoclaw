package developmentnotifications

import (
	"strings"
	"testing"
	"time"
)

type unknownQueryExpression struct{}

func (unknownQueryExpression) evaluate(Notification, time.Time) bool { return true }
func (unknownQueryExpression) canonical() string                     { return "UNKNOWN" }

func TestProgrammaticQueryASTValidationAndCanonicalization(t *testing.T) {
	now := time.Date(2026, 8, 24, 12, 0, 0, 0, time.UTC)
	query := Query{
		Filter: LogicalExpression{
			Operator: LogicalAnd,
			Left: &Predicate{
				Field:    FieldPriority,
				Operator: OperatorIn,
				Values: []Value{
					{Kind: ValueString, Text: string(PriorityHigh)},
					{Kind: ValueString, Text: string(PriorityCritical)},
				},
			},
			Right: &Negation{Expression: Predicate{
				Field: FieldUpdated, Operator: OperatorLess,
				Values: []Value{{Kind: ValueRelativeTime, Text: "-1d", TimeOffset: -24 * time.Hour}},
			}},
		},
		Order: []SortField{{Field: FieldCreated, Direction: Ascending}},
	}
	if err := query.Validate(); err != nil {
		t.Fatal(err)
	}
	canonical := query.Canonical()
	for _, fragment := range []string{
		`priority IN ("high", "critical")`,
		"NOT (updated < -1d)",
		"ORDER BY created ASC",
	} {
		if !strings.Contains(canonical, fragment) {
			t.Fatalf("canonical query %q omits %q", canonical, fragment)
		}
	}
	if query.Fingerprint() == "" {
		t.Fatal("valid query has no fingerprint")
	}
	notification := Notification{Priority: PriorityHigh, UpdatedAt: now, CreatedAt: now}
	if !query.Match(notification, now) {
		t.Fatal("programmatic query did not match")
	}

	for _, value := range []Value{
		{Kind: ValueBool, Bool: true},
		{Kind: ValueTime, Time: now},
		{Kind: ValueRelativeTime, Text: "-1d", TimeOffset: -24 * time.Hour},
		{Kind: ValueString, Text: "quoted"},
	} {
		if canonicalValue(value) == "" {
			t.Fatalf("canonicalValue(%#v) is empty", value)
		}
	}
}

func TestProgrammaticQueryRejectsMalformedASTNodes(t *testing.T) {
	valid := Predicate{
		Field: FieldStatus, Operator: OperatorEqual,
		Values: []Value{{Kind: ValueString, Text: string(StatusOpen)}},
	}
	invalid := []Expression{
		(*Predicate)(nil),
		(*LogicalExpression)(nil),
		(*Negation)(nil),
		unknownQueryExpression{},
		LogicalExpression{Operator: "X", Left: valid, Right: valid},
		LogicalExpression{Operator: LogicalAnd},
		Negation{},
		Predicate{Field: "unknown", Operator: OperatorEqual, Values: valid.Values},
		Predicate{Field: FieldStatus, Operator: OperatorEqual},
		Predicate{Field: FieldStatus, Operator: OperatorEqual, Values: []Value{
			{Kind: ValueString, Text: string(StatusOpen)},
			{Kind: ValueString, Text: string(StatusResolved)},
		}},
		Predicate{Field: FieldRead, Operator: OperatorContains, Values: []Value{{Kind: ValueBool, Bool: true}}},
		Predicate{Field: FieldCreated, Operator: OperatorIn, Values: []Value{{Kind: ValueTime, Time: time.Now()}}},
		Predicate{
			Field:    FieldPriority,
			Operator: OperatorGreater,
			Values:   []Value{{Kind: ValueString, Text: string(PriorityHigh)}},
		},
		Predicate{Field: FieldText, Operator: OperatorGreater, Values: []Value{{Kind: ValueString, Text: "text"}}},
		Predicate{Field: FieldRead, Operator: OperatorEqual, Values: []Value{{Kind: ValueString, Text: "true"}}},
		Predicate{Field: FieldCreated, Operator: OperatorEqual, Values: []Value{{Kind: ValueTime}}},
		Predicate{Field: FieldCreated, Operator: OperatorEqual, Values: []Value{{Kind: ValueString, Text: "now"}}},
		Predicate{
			Field:    FieldCreated,
			Operator: OperatorEqual,
			Values:   []Value{{Kind: ValueRelativeTime, Text: "bad"}},
		},
		Predicate{Field: FieldRepository, Operator: OperatorEqual, Values: []Value{{Kind: ValueString}}},
		Predicate{Field: FieldStatus, Operator: OperatorEqual, Values: []Value{{Kind: ValueString, Text: "invalid"}}},
	}
	for index, expression := range invalid {
		query := Query{Filter: expression}
		if err := query.Validate(); err == nil || query.Canonical() != "" || query.Fingerprint() != "" {
			t.Fatalf("invalid expression %d accepted: %#v", index, expression)
		}
	}

	tooManyValues := make([]Value, MaxQueryINValues+1)
	for index := range tooManyValues {
		tooManyValues[index] = Value{Kind: ValueString, Text: string(StatusOpen)}
	}
	if err := (Query{Filter: Predicate{
		Field: FieldStatus, Operator: OperatorIn, Values: tooManyValues,
	}}).Validate(); err == nil {
		t.Fatal("oversized IN predicate was accepted")
	}

	deep := Expression(valid)
	for range MaxQueryDepth + 1 {
		deep = Negation{Expression: deep}
	}
	if err := (Query{Filter: deep}).Validate(); err == nil {
		t.Fatal("overly deep negation was accepted")
	}
}

func TestProgrammaticQuerySortValidation(t *testing.T) {
	for _, query := range []Query{
		{Order: []SortField{{Field: FieldText, Direction: Ascending}}},
		{Order: []SortField{{Field: FieldUpdated, Direction: "SIDEWAYS"}}},
		{Order: []SortField{{Field: FieldUpdated, Direction: Ascending}, {Field: FieldUpdated, Direction: Descending}}},
		{Order: []SortField{
			{Field: FieldUpdated, Direction: Ascending},
			{Field: FieldCreated, Direction: Ascending},
			{Field: FieldPriority, Direction: Ascending},
			{Field: FieldStatus, Direction: Ascending},
		}},
	} {
		if err := query.Validate(); err == nil {
			t.Fatalf("invalid sort query accepted: %#v", query)
		}
	}
}
