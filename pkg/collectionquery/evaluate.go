package collectionquery

import (
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

var ErrFieldResolution = errors.New("collection query field resolution failed")

// FieldValue is the typed value returned by a feature-owned allowlist
// resolver. Use the constructors below so type mismatches fail closed.
type FieldValue struct {
	Type      FieldType
	Text      string
	Boolean   bool
	Number    float64
	Timestamp time.Time
}

// StringValue constructs a resolved string value.
func StringValue(value string) FieldValue {
	return FieldValue{Type: TypeString, Text: value}
}

// EnumValue constructs a resolved allowlisted enum value.
func EnumValue(value string) FieldValue {
	return FieldValue{Type: TypeEnum, Text: value}
}

// BooleanValue constructs a resolved boolean value.
func BooleanValue(value bool) FieldValue {
	return FieldValue{Type: TypeBoolean, Boolean: value}
}

// NumberValue constructs a resolved finite number value.
func NumberValue(value float64) FieldValue {
	return FieldValue{Type: TypeNumber, Number: value}
}

// TimestampValue constructs a resolved timestamp value.
func TimestampValue(value time.Time) FieldValue {
	return FieldValue{Type: TypeTimestamp, Timestamp: value}
}

// Resolver is implemented by each feature and is called only with fields
// declared by the query schema. The evaluation time is cursor-anchored during
// pagination, allowing derived values such as "currently active" to remain
// stable across pages.
type Resolver func(field Field, evaluatedAt time.Time) (FieldValue, bool)

// Match evaluates this query's filter through an allowlist resolver.
func (query Query) Match(evaluatedAt time.Time, resolve Resolver) (bool, error) {
	if err := query.Validate(); err != nil || evaluatedAt.IsZero() || resolve == nil {
		return false, ErrFieldResolution
	}
	if query.Filter == nil {
		return true, nil
	}
	return evaluateExpression(query.schema, query.Filter, evaluatedAt.UTC(), resolve)
}

func evaluateExpression(
	schema Schema,
	expression Expression,
	evaluatedAt time.Time,
	resolve Resolver,
) (bool, error) {
	switch node := expression.(type) {
	case Predicate:
		field, ok := schema.lookup(node.Field)
		if !ok {
			return false, ErrFieldResolution
		}
		actual, ok := resolve(node.Field, evaluatedAt)
		if !ok {
			return false, fmt.Errorf("%w: %s", ErrFieldResolution, node.Field)
		}
		actual, err := normalizeFieldValue(field, actual)
		if err != nil {
			return false, err
		}
		return comparePredicate(actual, node, evaluatedAt), nil
	case LogicalExpression:
		left, err := evaluateExpression(schema, node.Left, evaluatedAt, resolve)
		if err != nil {
			return false, err
		}
		if node.Operator == LogicalAnd && !left {
			return false, nil
		}
		if node.Operator == LogicalOr && left {
			return true, nil
		}
		return evaluateExpression(schema, node.Right, evaluatedAt, resolve)
	case Negation:
		matched, err := evaluateExpression(schema, node.Expression, evaluatedAt, resolve)
		return !matched, err
	default:
		return false, ErrFieldResolution
	}
}

func normalizeFieldValue(field FieldSchema, value FieldValue) (FieldValue, error) {
	if value.Type != field.Type {
		return FieldValue{}, fmt.Errorf("%w: field %s has the wrong type", ErrFieldResolution, field.Name)
	}
	switch field.Type {
	case TypeString:
		if !utf8.ValidString(value.Text) {
			return FieldValue{}, fmt.Errorf("%w: field %s is not UTF-8", ErrFieldResolution, field.Name)
		}
		value.Text = strings.ToLower(value.Text)
	case TypeEnum:
		value.Text = strings.ToLower(strings.TrimSpace(value.Text))
		if !enumValueAllowed(field, value.Text) {
			return FieldValue{}, fmt.Errorf("%w: field %s has an invalid enum", ErrFieldResolution, field.Name)
		}
	case TypeNumber:
		if math.IsNaN(value.Number) || math.IsInf(value.Number, 0) {
			return FieldValue{}, fmt.Errorf("%w: field %s is not finite", ErrFieldResolution, field.Name)
		}
		if value.Number == 0 {
			value.Number = 0
		}
	case TypeTimestamp:
		if value.Timestamp.IsZero() {
			return FieldValue{}, fmt.Errorf("%w: field %s has a zero timestamp", ErrFieldResolution, field.Name)
		}
		value.Timestamp = value.Timestamp.UTC()
	case TypeBoolean:
	default:
		return FieldValue{}, ErrFieldResolution
	}
	return value, nil
}

func comparePredicate(actual FieldValue, predicate Predicate, evaluatedAt time.Time) bool {
	equal := func(value Value) bool {
		switch actual.Type {
		case TypeString, TypeEnum:
			return actual.Text == value.Text
		case TypeBoolean:
			return actual.Boolean == value.Boolean
		case TypeNumber:
			return actual.Number == value.Number
		case TypeTimestamp:
			wanted := value.Timestamp
			if value.Kind == ValueRelativeTimestamp {
				wanted = evaluatedAt.Add(value.TimeOffset)
			}
			return actual.Timestamp.Equal(wanted.UTC())
		default:
			return false
		}
	}

	switch predicate.Operator {
	case OperatorEqual:
		return equal(predicate.Values[0])
	case OperatorNotEqual:
		return !equal(predicate.Values[0])
	case OperatorContains:
		return strings.Contains(actual.Text, predicate.Values[0].Text)
	case OperatorNotContains:
		return !strings.Contains(actual.Text, predicate.Values[0].Text)
	case OperatorGreater, OperatorGreaterEq, OperatorLess, OperatorLessEq:
		if actual.Type == TypeNumber {
			return compareOrderedFloat(actual.Number, predicate.Values[0].Number, predicate.Operator)
		}
		if actual.Type == TypeTimestamp {
			wanted := predicate.Values[0].Timestamp
			if predicate.Values[0].Kind == ValueRelativeTimestamp {
				wanted = evaluatedAt.Add(predicate.Values[0].TimeOffset)
			}
			return compareOrderedTime(actual.Timestamp, wanted.UTC(), predicate.Operator)
		}
		return false
	case OperatorIn, OperatorNotIn:
		found := false
		for _, value := range predicate.Values {
			found = found || equal(value)
		}
		if predicate.Operator == OperatorNotIn {
			return !found
		}
		return found
	default:
		return false
	}
}

func compareOrderedFloat(actual, wanted float64, operator Operator) bool {
	switch operator {
	case OperatorEqual:
		return actual == wanted
	case OperatorNotEqual:
		return actual != wanted
	case OperatorGreater:
		return actual > wanted
	case OperatorGreaterEq:
		return actual >= wanted
	case OperatorLess:
		return actual < wanted
	case OperatorLessEq:
		return actual <= wanted
	default:
		return false
	}
}

func compareOrderedTime(actual, wanted time.Time, operator Operator) bool {
	switch operator {
	case OperatorEqual:
		return actual.Equal(wanted)
	case OperatorNotEqual:
		return !actual.Equal(wanted)
	case OperatorGreater:
		return actual.After(wanted)
	case OperatorGreaterEq:
		return actual.After(wanted) || actual.Equal(wanted)
	case OperatorLess:
		return actual.Before(wanted)
	case OperatorLessEq:
		return actual.Before(wanted) || actual.Equal(wanted)
	default:
		return false
	}
}

// CompareValues applies the generic stable sort semantics for a schema field.
func CompareValues(field FieldSchema, left, right FieldValue) (int, error) {
	left, err := normalizeFieldValue(field, left)
	if err != nil {
		return 0, err
	}
	right, err = normalizeFieldValue(field, right)
	if err != nil {
		return 0, err
	}
	switch field.Type {
	case TypeString, TypeEnum:
		return strings.Compare(left.Text, right.Text), nil
	case TypeBoolean:
		if left.Boolean == right.Boolean {
			return 0, nil
		}
		if !left.Boolean {
			return -1, nil
		}
		return 1, nil
	case TypeNumber:
		if left.Number == right.Number {
			return 0, nil
		}
		if left.Number < right.Number {
			return -1, nil
		}
		return 1, nil
	case TypeTimestamp:
		if left.Timestamp.Equal(right.Timestamp) {
			return 0, nil
		}
		if left.Timestamp.Before(right.Timestamp) {
			return -1, nil
		}
		return 1, nil
	default:
		return 0, ErrFieldResolution
	}
}

func sortableString(field FieldSchema, value FieldValue) (string, error) {
	value, err := normalizeFieldValue(field, value)
	if err != nil {
		return "", err
	}
	switch field.Type {
	case TypeString, TypeEnum:
		return value.Text, nil
	case TypeBoolean:
		return strconv.FormatBool(value.Boolean), nil
	case TypeNumber:
		return strconv.FormatFloat(value.Number, 'g', -1, 64), nil
	case TypeTimestamp:
		return value.Timestamp.UTC().Format(time.RFC3339Nano), nil
	default:
		return "", ErrFieldResolution
	}
}

func fieldValueFromSortable(field FieldSchema, encoded string) (FieldValue, error) {
	switch field.Type {
	case TypeString:
		if !utf8.ValidString(encoded) || encoded != strings.ToLower(encoded) {
			return FieldValue{}, ErrInvalidCursor
		}
		return StringValue(encoded), nil
	case TypeEnum:
		if encoded == "" || encoded != strings.ToLower(encoded) || !enumValueAllowed(field, encoded) {
			return FieldValue{}, ErrInvalidCursor
		}
		return EnumValue(encoded), nil
	case TypeBoolean:
		parsed, err := strconv.ParseBool(encoded)
		if err != nil || strconv.FormatBool(parsed) != encoded {
			return FieldValue{}, ErrInvalidCursor
		}
		return BooleanValue(parsed), nil
	case TypeNumber:
		parsed, err := strconv.ParseFloat(encoded, 64)
		if err != nil || math.IsNaN(parsed) || math.IsInf(parsed, 0) ||
			strconv.FormatFloat(parsed, 'g', -1, 64) != encoded {
			return FieldValue{}, ErrInvalidCursor
		}
		return NumberValue(parsed), nil
	case TypeTimestamp:
		parsed, err := time.Parse(time.RFC3339Nano, encoded)
		if err != nil || parsed.Location() != time.UTC || parsed.UTC().Format(time.RFC3339Nano) != encoded {
			return FieldValue{}, ErrInvalidCursor
		}
		return TimestampValue(parsed), nil
	default:
		return FieldValue{}, ErrInvalidCursor
	}
}
