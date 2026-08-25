package collectionquery

import (
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

var ErrInvalidQuery = errors.New("invalid collection query")

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

var allOperators = []Operator{
	OperatorEqual, OperatorNotEqual, OperatorIn, OperatorNotIn,
	OperatorContains, OperatorNotContains, OperatorGreater, OperatorGreaterEq,
	OperatorLess, OperatorLessEq,
}

func (operator Operator) valid() bool {
	for _, candidate := range allOperators {
		if candidate == operator {
			return true
		}
	}
	return false
}

// LogicalOperator combines two filter expressions.
type LogicalOperator string

const (
	LogicalAnd LogicalOperator = "AND"
	LogicalOr  LogicalOperator = "OR"
)

// ValueKind distinguishes parsed operand representations.
type ValueKind uint8

const (
	ValueString ValueKind = iota + 1
	ValueBoolean
	ValueNumber
	ValueTimestamp
	ValueRelativeTimestamp
)

// Value is one typed predicate operand.
type Value struct {
	Kind       ValueKind
	Text       string
	Boolean    bool
	Number     float64
	Timestamp  time.Time
	TimeOffset time.Duration
}

// Expression is a closed query AST implemented by this package's node types.
type Expression interface {
	collectionQueryExpression()
}

// Predicate compares one allowlisted field with one or more typed values.
type Predicate struct {
	Field    Field
	Operator Operator
	Values   []Value
}

func (Predicate) collectionQueryExpression() {}

// LogicalExpression is an AND or OR AST node.
type LogicalExpression struct {
	Operator LogicalOperator
	Left     Expression
	Right    Expression
}

func (LogicalExpression) collectionQueryExpression() {}

// Negation is a NOT AST node.
type Negation struct {
	Expression Expression
}

func (Negation) collectionQueryExpression() {}

// Direction is an ascending or descending sort direction.
type Direction string

const (
	Ascending  Direction = "ASC"
	Descending Direction = "DESC"
)

// SortField is one requested ordering component.
type SortField struct {
	Field     Field     `json:"field"`
	Direction Direction `json:"direction"`
}

// Query is a bounded typed query bound to a detached validated Schema.
type Query struct {
	Filter Expression
	Order  []SortField
	schema Schema
}

// NewQuery validates a programmatically constructed AST against schema.
func NewQuery(schema Schema, filter Expression, order []SortField) (Query, error) {
	normalized, err := NewSchema(schema.Fields, schema.DefaultOrder)
	if err != nil {
		return Query{}, err
	}
	query := Query{Filter: filter, Order: append([]SortField(nil), order...), schema: normalized}
	if err := query.Validate(); err != nil {
		return Query{}, err
	}
	return query, nil
}

// Schema returns a detached projection of this query's allowlist.
func (query Query) Schema() Schema { return query.schema.Clone() }

// EffectiveOrder returns the explicit order or the schema default.
func (query Query) EffectiveOrder() []SortField {
	if len(query.Order) == 0 {
		return append([]SortField(nil), query.schema.DefaultOrder...)
	}
	return append([]SortField(nil), query.Order...)
}

// Validate permits stores to accept programmatic Query values safely.
func (query Query) Validate() error {
	if err := query.schema.Validate(); err != nil {
		return fmt.Errorf("%w: schema is unavailable", ErrInvalidQuery)
	}
	if len(query.Order) > MaxQuerySortFields {
		return fmt.Errorf("%w: too many sort fields", ErrInvalidQuery)
	}
	seenSort := make(map[Field]struct{}, len(query.Order))
	for _, order := range query.Order {
		field, ok := query.schema.lookup(order.Field)
		if !ok || !field.Sortable || (order.Direction != Ascending && order.Direction != Descending) {
			return fmt.Errorf("%w: invalid sort field", ErrInvalidQuery)
		}
		if _, duplicate := seenSort[order.Field]; duplicate {
			return fmt.Errorf("%w: duplicate sort field", ErrInvalidQuery)
		}
		seenSort[order.Field] = struct{}{}
	}
	if query.Filter == nil {
		return nil
	}
	predicates, nodes := 0, 0
	if err := validateExpression(query.schema, query.Filter, 0, &predicates, &nodes); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidQuery, err)
	}
	return nil
}

func validateExpression(schema Schema, expression Expression, depth int, predicates, nodes *int) error {
	(*nodes)++
	if *nodes > MaxQueryPredicates*(MaxQueryDepth+2) {
		return errors.New("expression is too complex")
	}
	switch node := expression.(type) {
	case Predicate:
		(*predicates)++
		if *predicates > MaxQueryPredicates {
			return fmt.Errorf("more than %d predicates", MaxQueryPredicates)
		}
		return validatePredicate(schema, node)
	case LogicalExpression:
		if node.Operator != LogicalAnd && node.Operator != LogicalOr || node.Left == nil || node.Right == nil {
			return errors.New("invalid logical expression")
		}
		if err := validateExpression(schema, node.Left, depth, predicates, nodes); err != nil {
			return err
		}
		return validateExpression(schema, node.Right, depth, predicates, nodes)
	case Negation:
		if node.Expression == nil || depth >= MaxQueryDepth {
			return fmt.Errorf("negation nesting exceeds %d", MaxQueryDepth)
		}
		return validateExpression(schema, node.Expression, depth+1, predicates, nodes)
	default:
		return errors.New("unknown expression")
	}
}

func validatePredicate(schema Schema, predicate Predicate) error {
	field, ok := schema.lookup(predicate.Field)
	if !ok {
		return errors.New("invalid predicate field")
	}
	allowed := false
	for _, operator := range field.Operators {
		allowed = allowed || operator == predicate.Operator
	}
	if !allowed {
		return fmt.Errorf("operator %s is not valid for field %s", predicate.Operator, predicate.Field)
	}
	if len(predicate.Values) == 0 || len(predicate.Values) > MaxQueryINValues {
		return errors.New("predicate requires bounded values")
	}
	if predicate.Operator != OperatorIn && predicate.Operator != OperatorNotIn && len(predicate.Values) != 1 {
		return fmt.Errorf("operator %s requires exactly one value", predicate.Operator)
	}
	for _, value := range predicate.Values {
		if err := validateValue(field, value); err != nil {
			return err
		}
	}
	return nil
}

func validateValue(field FieldSchema, value Value) error {
	switch field.Type {
	case TypeString, TypeEnum:
		if value.Kind != ValueString || value.Text == "" || len(value.Text) > MaxQueryBytes || !utf8.ValidString(value.Text) {
			return fmt.Errorf("field %s requires a non-empty string", field.Name)
		}
		if value.Text != strings.ToLower(value.Text) {
			return fmt.Errorf("field %s requires a canonical string", field.Name)
		}
		if field.Type == TypeEnum && !enumValueAllowed(field, value.Text) {
			return fmt.Errorf("invalid value %q for field %s", value.Text, field.Name)
		}
	case TypeBoolean:
		if value.Kind != ValueBoolean {
			return fmt.Errorf("field %s requires true or false", field.Name)
		}
	case TypeNumber:
		if value.Kind != ValueNumber || math.IsNaN(value.Number) || math.IsInf(value.Number, 0) {
			return fmt.Errorf("field %s requires a finite number", field.Name)
		}
	case TypeTimestamp:
		if value.Kind != ValueTimestamp && value.Kind != ValueRelativeTimestamp {
			return fmt.Errorf("field %s requires an ISO timestamp or relative date", field.Name)
		}
		if value.Kind == ValueTimestamp && value.Timestamp.IsZero() {
			return fmt.Errorf("field %s requires a non-zero timestamp", field.Name)
		}
		if value.Kind == ValueRelativeTimestamp {
			parsed, err := parseTimestampValue(value.Text, 0)
			if err != nil || parsed.Kind != ValueRelativeTimestamp || parsed.TimeOffset != value.TimeOffset {
				return fmt.Errorf("field %s has an invalid relative date", field.Name)
			}
		}
	default:
		return errors.New("invalid field type")
	}
	return nil
}

func enumValueAllowed(field FieldSchema, value string) bool {
	for _, candidate := range field.SuggestedValues {
		if candidate == value {
			return true
		}
	}
	return false
}

// Canonical returns a formatting-independent representation used for cursor
// binding. The stable ID tie-break is intentionally internal.
func (query Query) Canonical() string {
	if query.Validate() != nil {
		return ""
	}
	filter := "ALL"
	if query.Filter != nil {
		filter = canonicalExpression(query.Filter)
	}
	order := query.EffectiveOrder()
	parts := make([]string, len(order))
	for index, field := range order {
		parts[index] = string(field.Field) + " " + string(field.Direction)
	}
	return filter + " ORDER BY " + strings.Join(parts, ", ")
}

func canonicalExpression(expression Expression) string {
	switch node := expression.(type) {
	case Predicate:
		if node.Operator == OperatorIn || node.Operator == OperatorNotIn {
			parts := make([]string, len(node.Values))
			for index := range node.Values {
				parts[index] = canonicalValue(node.Values[index])
			}
			return string(node.Field) + " " + string(node.Operator) + " (" + strings.Join(parts, ", ") + ")"
		}
		return string(node.Field) + " " + string(node.Operator) + " " + canonicalValue(node.Values[0])
	case LogicalExpression:
		return "(" + canonicalExpression(node.Left) + " " + string(node.Operator) + " " + canonicalExpression(node.Right) + ")"
	case Negation:
		return "NOT (" + canonicalExpression(node.Expression) + ")"
	default:
		return ""
	}
}

func canonicalValue(value Value) string {
	switch value.Kind {
	case ValueBoolean:
		return strconv.FormatBool(value.Boolean)
	case ValueNumber:
		return strconv.FormatFloat(value.Number, 'g', -1, 64)
	case ValueTimestamp:
		return strconv.Quote(value.Timestamp.UTC().Format(time.RFC3339Nano))
	case ValueRelativeTimestamp:
		return value.Text
	default:
		return strconv.Quote(value.Text)
	}
}

// Fingerprint binds cursors to canonical filter and ordering semantics.
func (query Query) Fingerprint() string {
	canonical := query.Canonical()
	if canonical == "" {
		return ""
	}
	digest := sha256.Sum256([]byte(canonical))
	return base64.RawURLEncoding.EncodeToString(digest[:])
}
