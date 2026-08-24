package developmentnotifications

import (
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	MaxQueryBytes      = 4 << 10
	MaxQueryDepth      = 16
	MaxQueryPredicates = 50
	MaxQueryINValues   = 100
	MaxQuerySortFields = 3
)

// Field is an allowlisted filter and sort attribute.
type Field string

const (
	FieldStatus     Field = "status"
	FieldRead       Field = "read"
	FieldSnoozed    Field = "snoozed"
	FieldPriority   Field = "priority"
	FieldReason     Field = "reason"
	FieldRepository Field = "repository"
	FieldWorkspace  Field = "workspace"
	FieldIntent     Field = "intent"
	FieldSource     Field = "source"
	FieldPhase      Field = "phase"
	FieldCreated    Field = "created"
	FieldUpdated    Field = "updated"
	FieldText       Field = "text"
)

// Operator is a type-checked predicate operation.
type Operator string

const (
	OperatorEqual       Operator = "="
	OperatorNotEqual    Operator = "!="
	OperatorIn          Operator = "IN"
	OperatorNotIn       Operator = "NOT IN"
	OperatorContains    Operator = "~"
	OperatorNotContains Operator = "!~"
	OperatorGreater     Operator = ">"
	OperatorGreaterEq   Operator = ">="
	OperatorLess        Operator = "<"
	OperatorLessEq      Operator = "<="
)

// LogicalOperator combines two filter expressions.
type LogicalOperator string

const (
	LogicalAnd LogicalOperator = "AND"
	LogicalOr  LogicalOperator = "OR"
)

// ValueKind distinguishes parsed string, boolean, and time operands.
type ValueKind uint8

const (
	ValueString ValueKind = iota + 1
	ValueBool
	ValueTime
	ValueRelativeTime
)

// Value is a type-checked predicate value. Relative time offsets are applied
// to the caller-supplied evaluation time, making tests and pagination stable.
type Value struct {
	Kind       ValueKind
	Text       string
	Bool       bool
	Time       time.Time
	TimeOffset time.Duration
}

// Expression is a closed query AST implemented by this package's node types.
type Expression interface {
	evaluate(notification Notification, now time.Time) bool
	canonical() string
}

// Predicate compares one allowlisted field with one or more typed values.
type Predicate struct {
	Field    Field
	Operator Operator
	Values   []Value
}

func (p Predicate) evaluate(n Notification, now time.Time) bool {
	switch fieldTypeOf(p.Field) {
	case fieldTypeBool:
		actual := n.Read
		if p.Field == FieldSnoozed {
			actual = n.IsSnoozed(now)
		}
		return compareBool(actual, p.Operator, p.Values)
	case fieldTypeTime:
		actual := n.CreatedAt
		if p.Field == FieldUpdated {
			actual = n.UpdatedAt
		}
		return compareTime(actual, p.Operator, p.Values[0], now)
	default:
		return compareString(notificationString(n, p.Field), p.Operator, p.Values)
	}
}

func (p Predicate) canonical() string {
	if p.Operator == OperatorIn || p.Operator == OperatorNotIn {
		parts := make([]string, len(p.Values))
		for index := range p.Values {
			parts[index] = canonicalValue(p.Values[index])
		}
		return string(p.Field) + " " + string(p.Operator) + " (" + strings.Join(parts, ", ") + ")"
	}
	return string(p.Field) + " " + string(p.Operator) + " " + canonicalValue(p.Values[0])
}

// LogicalExpression is an AND or OR AST node.
type LogicalExpression struct {
	Operator LogicalOperator
	Left     Expression
	Right    Expression
}

func (e LogicalExpression) evaluate(n Notification, now time.Time) bool {
	if e.Operator == LogicalAnd {
		return e.Left.evaluate(n, now) && e.Right.evaluate(n, now)
	}
	return e.Left.evaluate(n, now) || e.Right.evaluate(n, now)
}

func (e LogicalExpression) canonical() string {
	return "(" + e.Left.canonical() + " " + string(e.Operator) + " " + e.Right.canonical() + ")"
}

// Negation is a NOT AST node.
type Negation struct {
	Expression Expression
}

func (e Negation) evaluate(n Notification, now time.Time) bool {
	return !e.Expression.evaluate(n, now)
}

func (e Negation) canonical() string {
	return "NOT (" + e.Expression.canonical() + ")"
}

// Direction is an ascending or descending sort direction.
type Direction string

const (
	Ascending  Direction = "ASC"
	Descending Direction = "DESC"
)

// SortField is one user-requested ordering component.
type SortField struct {
	Field     Field
	Direction Direction
}

// Query is a bounded, parsed notification query. A missing filter matches all
// notifications. EffectiveOrder always adds the ID tie-break internally.
type Query struct {
	Filter Expression
	Order  []SortField
}

// Match evaluates a valid query against one notification.
func (q Query) Match(n Notification, now time.Time) bool {
	if q.Validate() != nil {
		return false
	}
	return q.matchValidated(n, now)
}

func (q Query) matchValidated(n Notification, now time.Time) bool {
	return q.Filter == nil || q.Filter.evaluate(n, now.UTC())
}

// EffectiveOrder supplies updated DESC when ORDER BY was omitted.
func (q Query) EffectiveOrder() []SortField {
	if len(q.Order) == 0 {
		return []SortField{{Field: FieldUpdated, Direction: Descending}}
	}
	return append([]SortField(nil), q.Order...)
}

// Canonical returns a formatting-independent representation suitable for
// saved-view comparison and cursor binding.
func (q Query) Canonical() string {
	if q.Validate() != nil {
		return ""
	}
	filter := "ALL"
	if q.Filter != nil {
		filter = q.Filter.canonical()
	}
	order := q.EffectiveOrder()
	parts := make([]string, len(order))
	for index, field := range order {
		parts[index] = string(field.Field) + " " + string(field.Direction)
	}
	return filter + " ORDER BY " + strings.Join(parts, ", ")
}

// Fingerprint binds cursors to canonical filter and sort semantics.
func (q Query) Fingerprint() string {
	canonical := q.Canonical()
	if canonical == "" {
		return ""
	}
	digest := sha256.Sum256([]byte(canonical))
	return base64.RawURLEncoding.EncodeToString(digest[:])
}

// Validate permits stores to accept Query values from callers other than
// ParseQuery without risking malformed AST panics or unbounded evaluation.
func (q Query) Validate() error {
	if len(q.Order) > MaxQuerySortFields {
		return fmt.Errorf("%w: too many sort fields", ErrInvalidQuery)
	}
	seenSort := make(map[Field]struct{}, len(q.Order))
	for _, order := range q.Order {
		field, valid := parseField(string(order.Field))
		if !valid || field != order.Field || !validSortField(field) ||
			(order.Direction != Ascending && order.Direction != Descending) {
			return fmt.Errorf("%w: invalid sort field", ErrInvalidQuery)
		}
		if _, duplicate := seenSort[field]; duplicate {
			return fmt.Errorf("%w: duplicate sort field", ErrInvalidQuery)
		}
		seenSort[field] = struct{}{}
	}
	if q.Filter == nil {
		return nil
	}
	predicateCount := 0
	nodeCount := 0
	if err := validateExpression(q.Filter, 0, &predicateCount, &nodeCount); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidQuery, err)
	}
	return nil
}

func validateExpression(expression Expression, negationDepth int, predicateCount, nodeCount *int) error {
	(*nodeCount)++
	if *nodeCount > MaxQueryPredicates*(MaxQueryDepth+2) {
		return fmt.Errorf("expression is too complex")
	}
	switch node := expression.(type) {
	case Predicate:
		return validatePredicateNode(node, predicateCount)
	case *Predicate:
		if node == nil {
			return fmt.Errorf("nil predicate")
		}
		return validatePredicateNode(*node, predicateCount)
	case LogicalExpression:
		return validateLogicalNode(node, negationDepth, predicateCount, nodeCount)
	case *LogicalExpression:
		if node == nil {
			return fmt.Errorf("nil logical expression")
		}
		return validateLogicalNode(*node, negationDepth, predicateCount, nodeCount)
	case Negation:
		return validateNegationNode(node, negationDepth, predicateCount, nodeCount)
	case *Negation:
		if node == nil {
			return fmt.Errorf("nil negation")
		}
		return validateNegationNode(*node, negationDepth, predicateCount, nodeCount)
	default:
		return fmt.Errorf("unknown expression")
	}
}

func validatePredicateNode(predicate Predicate, predicateCount *int) error {
	field, valid := parseField(string(predicate.Field))
	if !valid || field != predicate.Field {
		return fmt.Errorf("invalid predicate field")
	}
	(*predicateCount)++
	if *predicateCount > MaxQueryPredicates {
		return fmt.Errorf("more than %d predicates", MaxQueryPredicates)
	}
	if len(predicate.Values) > MaxQueryINValues {
		return fmt.Errorf("more than %d predicate values", MaxQueryINValues)
	}
	return validatePredicate(predicate)
}

func validateLogicalNode(
	expression LogicalExpression,
	negationDepth int,
	predicateCount, nodeCount *int,
) error {
	if expression.Operator != LogicalAnd && expression.Operator != LogicalOr {
		return fmt.Errorf("invalid logical operator")
	}
	if expression.Left == nil || expression.Right == nil {
		return fmt.Errorf("logical operands are required")
	}
	if err := validateExpression(expression.Left, negationDepth, predicateCount, nodeCount); err != nil {
		return err
	}
	return validateExpression(expression.Right, negationDepth, predicateCount, nodeCount)
}

func validateNegationNode(
	expression Negation,
	negationDepth int,
	predicateCount, nodeCount *int,
) error {
	if expression.Expression == nil {
		return fmt.Errorf("negated expression is required")
	}
	if negationDepth >= MaxQueryDepth {
		return fmt.Errorf("negation nesting exceeds %d", MaxQueryDepth)
	}
	return validateExpression(expression.Expression, negationDepth+1, predicateCount, nodeCount)
}

func notificationString(n Notification, field Field) string {
	switch field {
	case FieldStatus:
		return string(n.Status)
	case FieldPriority:
		return string(n.Priority)
	case FieldReason:
		return string(n.Reason)
	case FieldRepository:
		return n.Repository
	case FieldWorkspace:
		return n.WorkspaceID
	case FieldIntent:
		return string(n.Intent)
	case FieldSource:
		return string(n.SourceKind)
	case FieldPhase:
		return n.Phase
	case FieldText:
		return strings.TrimSpace(n.Title + " " + n.Summary)
	default:
		return ""
	}
}

func compareBool(actual bool, operator Operator, values []Value) bool {
	match := func(value Value) bool { return actual == value.Bool }
	switch operator {
	case OperatorEqual:
		return match(values[0])
	case OperatorNotEqual:
		return !match(values[0])
	case OperatorIn, OperatorNotIn:
		found := false
		for _, value := range values {
			found = found || match(value)
		}
		if operator == OperatorNotIn {
			return !found
		}
		return found
	default:
		return false
	}
}

func compareString(actual string, operator Operator, values []Value) bool {
	equal := func(value Value) bool { return strings.EqualFold(actual, value.Text) }
	contains := func(value Value) bool {
		return strings.Contains(strings.ToLower(actual), strings.ToLower(value.Text))
	}
	switch operator {
	case OperatorEqual:
		return equal(values[0])
	case OperatorNotEqual:
		return !equal(values[0])
	case OperatorContains:
		return contains(values[0])
	case OperatorNotContains:
		return !contains(values[0])
	case OperatorIn, OperatorNotIn:
		found := false
		for _, value := range values {
			found = found || equal(value)
		}
		if operator == OperatorNotIn {
			return !found
		}
		return found
	default:
		return false
	}
}

func compareTime(actual time.Time, operator Operator, value Value, now time.Time) bool {
	want := value.Time
	if value.Kind == ValueRelativeTime {
		want = now.Add(value.TimeOffset)
	}
	actual = actual.UTC()
	want = want.UTC()
	switch operator {
	case OperatorEqual:
		return actual.Equal(want)
	case OperatorNotEqual:
		return !actual.Equal(want)
	case OperatorGreater:
		return actual.After(want)
	case OperatorGreaterEq:
		return actual.After(want) || actual.Equal(want)
	case OperatorLess:
		return actual.Before(want)
	case OperatorLessEq:
		return actual.Before(want) || actual.Equal(want)
	default:
		return false
	}
}

func canonicalValue(value Value) string {
	switch value.Kind {
	case ValueBool:
		return strconv.FormatBool(value.Bool)
	case ValueTime:
		return strconv.Quote(value.Time.UTC().Format(time.RFC3339Nano))
	case ValueRelativeTime:
		return value.Text
	default:
		return strconv.Quote(value.Text)
	}
}

type fieldType uint8

const (
	fieldTypeString fieldType = iota + 1
	fieldTypeEnum
	fieldTypeBool
	fieldTypeTime
)

func fieldTypeOf(field Field) fieldType {
	switch field {
	case FieldRead, FieldSnoozed:
		return fieldTypeBool
	case FieldCreated, FieldUpdated:
		return fieldTypeTime
	case FieldStatus, FieldPriority, FieldReason, FieldIntent, FieldSource:
		return fieldTypeEnum
	default:
		return fieldTypeString
	}
}

func parseField(raw string) (Field, bool) {
	field := Field(strings.ToLower(raw))
	switch field {
	case FieldStatus, FieldRead, FieldSnoozed, FieldPriority, FieldReason,
		FieldRepository, FieldWorkspace, FieldIntent, FieldSource, FieldPhase,
		FieldCreated, FieldUpdated, FieldText:
		return field, true
	default:
		return "", false
	}
}

func validatePredicate(predicate Predicate) error {
	if len(predicate.Values) == 0 {
		return fmt.Errorf("predicate requires a value")
	}
	if predicate.Operator != OperatorIn && predicate.Operator != OperatorNotIn && len(predicate.Values) != 1 {
		return fmt.Errorf("operator %s requires exactly one value", predicate.Operator)
	}
	typeOfField := fieldTypeOf(predicate.Field)
	switch typeOfField {
	case fieldTypeBool:
		if predicate.Operator != OperatorEqual && predicate.Operator != OperatorNotEqual &&
			predicate.Operator != OperatorIn && predicate.Operator != OperatorNotIn {
			return fmt.Errorf("operator %s is not valid for boolean field %s", predicate.Operator, predicate.Field)
		}
	case fieldTypeTime:
		if predicate.Operator == OperatorIn || predicate.Operator == OperatorNotIn ||
			predicate.Operator == OperatorContains || predicate.Operator == OperatorNotContains {
			return fmt.Errorf("operator %s is not valid for time field %s", predicate.Operator, predicate.Field)
		}
	case fieldTypeEnum:
		if predicate.Operator == OperatorContains || predicate.Operator == OperatorNotContains ||
			predicate.Operator == OperatorGreater || predicate.Operator == OperatorGreaterEq ||
			predicate.Operator == OperatorLess || predicate.Operator == OperatorLessEq {
			return fmt.Errorf("operator %s is not valid for enum field %s", predicate.Operator, predicate.Field)
		}
	case fieldTypeString:
		if predicate.Operator == OperatorGreater || predicate.Operator == OperatorGreaterEq ||
			predicate.Operator == OperatorLess || predicate.Operator == OperatorLessEq {
			return fmt.Errorf("operator %s is not valid for text field %s", predicate.Operator, predicate.Field)
		}
	}
	for _, value := range predicate.Values {
		if err := validateFieldValue(predicate.Field, value); err != nil {
			return err
		}
	}
	return nil
}

func validateFieldValue(field Field, value Value) error {
	switch fieldTypeOf(field) {
	case fieldTypeBool:
		if value.Kind != ValueBool {
			return fmt.Errorf("field %s requires true or false", field)
		}
	case fieldTypeTime:
		if value.Kind != ValueTime && value.Kind != ValueRelativeTime {
			return fmt.Errorf("field %s requires an ISO timestamp or relative date", field)
		}
		if value.Kind == ValueTime && value.Time.IsZero() {
			return fmt.Errorf("field %s requires a non-zero timestamp", field)
		}
		if value.Kind == ValueRelativeTime {
			parsed, err := parseTimeValue(value.Text, 0)
			if err != nil || parsed.Kind != ValueRelativeTime || parsed.TimeOffset != value.TimeOffset {
				return fmt.Errorf("field %s has an invalid relative date", field)
			}
		}
	default:
		if value.Kind != ValueString || value.Text == "" || len(value.Text) > MaxQueryBytes ||
			!utf8.ValidString(value.Text) {
			return fmt.Errorf("field %s requires a non-empty string", field)
		}
	}
	if fieldTypeOf(field) == fieldTypeEnum && !validEnumQueryValue(field, value.Text) {
		return fmt.Errorf("invalid value %q for field %s", value.Text, field)
	}
	return nil
}

func validEnumQueryValue(field Field, value string) bool {
	switch field {
	case FieldStatus:
		return validStatus(Status(value))
	case FieldPriority:
		return validPriority(Priority(value))
	case FieldReason:
		return validReason(Reason(value))
	case FieldIntent:
		return validIntent(Intent(value))
	case FieldSource:
		return validSourceKind(SourceKind(value))
	default:
		return false
	}
}

func validSortField(field Field) bool {
	return field != FieldText
}
