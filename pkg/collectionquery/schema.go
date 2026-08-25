// Package collectionquery provides a bounded, schema-driven query language
// for in-memory collection filtering and stable keyset pagination. Feature
// stores remain responsible for resolving allowlisted fields; this package
// never constructs SQL or reaches into resource values by reflection.
package collectionquery

import (
	"errors"
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"
)

const (
	MaxQueryBytes      = 4 << 10
	MaxQueryDepth      = 16
	MaxQueryPredicates = 50
	MaxQueryINValues   = 100
	MaxQuerySortFields = 3

	DefaultPageSize = 50
	MaxPageSize     = 200

	MaxSchemaFields         = 128
	MaxFieldNameBytes       = 64
	MaxSuggestedValues      = 100
	MaxSuggestedValueBytes  = 256
	MaxQueryErrorMessageLen = 256
)

var ErrInvalidSchema = errors.New("invalid collection query schema")

// FieldType is the typed value exposed by one allowlisted collection field.
type FieldType string

const (
	TypeString    FieldType = "string"
	TypeEnum      FieldType = "enum"
	TypeBoolean   FieldType = "boolean"
	TypeNumber    FieldType = "number"
	TypeTimestamp FieldType = "timestamp"
)

func (fieldType FieldType) valid() bool {
	switch fieldType {
	case TypeString, TypeEnum, TypeBoolean, TypeNumber, TypeTimestamp:
		return true
	default:
		return false
	}
}

// Field is a canonical, case-insensitive schema field name.
type Field string

// FieldSchema describes the query contract for one resource field. For enum
// fields SuggestedValues is also the complete allowlist of accepted values.
// For other field types it is an autocomplete hint only.
type FieldSchema struct {
	Name            Field      `json:"name"`
	Type            FieldType  `json:"type"`
	Operators       []Operator `json:"operators"`
	Sortable        bool       `json:"sortable"`
	SuggestedValues []string   `json:"suggested_values,omitempty"`
}

// Schema is the JSON-projectable query contract returned by collection list
// APIs. NewSchema validates, normalizes, and detaches all slices.
type Schema struct {
	Fields       []FieldSchema `json:"fields"`
	DefaultOrder []SortField   `json:"default_order"`
}

// NewSchema builds an immutable-in-practice detached schema. Empty operator
// lists are expanded to the meaningful defaults for each field type.
func NewSchema(fields []FieldSchema, defaultOrder []SortField) (Schema, error) {
	candidate := Schema{
		Fields:       cloneFieldSchemas(fields),
		DefaultOrder: append([]SortField(nil), defaultOrder...),
	}
	for index := range candidate.Fields {
		field := &candidate.Fields[index]
		field.Name = Field(strings.ToLower(strings.TrimSpace(string(field.Name))))
		if len(field.Operators) == 0 {
			field.Operators = defaultOperators(field.Type)
		}
		for valueIndex := range field.SuggestedValues {
			field.SuggestedValues[valueIndex] = strings.TrimSpace(field.SuggestedValues[valueIndex])
			if field.Type == TypeEnum {
				field.SuggestedValues[valueIndex] = strings.ToLower(field.SuggestedValues[valueIndex])
			}
		}
	}
	if err := candidate.Validate(); err != nil {
		return Schema{}, err
	}
	return candidate, nil
}

// Validate rejects ambiguous or unbounded schemas.
func (schema Schema) Validate() error {
	if len(schema.Fields) == 0 || len(schema.Fields) > MaxSchemaFields {
		return fmt.Errorf("%w: fields must contain between 1 and %d entries", ErrInvalidSchema, MaxSchemaFields)
	}
	seenFields := make(map[Field]struct{}, len(schema.Fields))
	for _, field := range schema.Fields {
		if !validFieldName(field.Name) || !field.Type.valid() {
			return fmt.Errorf("%w: invalid field declaration", ErrInvalidSchema)
		}
		canonicalName := Field(strings.ToLower(string(field.Name)))
		if canonicalName != field.Name {
			return fmt.Errorf("%w: field names must be canonical lowercase", ErrInvalidSchema)
		}
		if _, duplicate := seenFields[field.Name]; duplicate {
			return fmt.Errorf("%w: duplicate field %q", ErrInvalidSchema, field.Name)
		}
		seenFields[field.Name] = struct{}{}
		if len(field.Operators) == 0 || len(field.Operators) > len(allOperators) {
			return fmt.Errorf("%w: field %q has invalid operators", ErrInvalidSchema, field.Name)
		}
		seenOperators := make(map[Operator]struct{}, len(field.Operators))
		for _, operator := range field.Operators {
			if !operator.valid() || !operatorMeaningful(field.Type, operator) {
				return fmt.Errorf("%w: operator %q is invalid for field %q", ErrInvalidSchema, operator, field.Name)
			}
			if _, duplicate := seenOperators[operator]; duplicate {
				return fmt.Errorf("%w: duplicate operator for field %q", ErrInvalidSchema, field.Name)
			}
			seenOperators[operator] = struct{}{}
		}
		if len(field.SuggestedValues) > MaxSuggestedValues {
			return fmt.Errorf("%w: field %q has too many suggested values", ErrInvalidSchema, field.Name)
		}
		seenValues := make(map[string]struct{}, len(field.SuggestedValues))
		for _, value := range field.SuggestedValues {
			if value == "" || len(value) > MaxSuggestedValueBytes || !utf8.ValidString(value) ||
				strings.IndexFunc(value, unicode.IsControl) >= 0 {
				return fmt.Errorf("%w: invalid suggested value for field %q", ErrInvalidSchema, field.Name)
			}
			key := strings.ToLower(value)
			if field.Type == TypeEnum && key != value {
				return fmt.Errorf("%w: enum values must be canonical lowercase", ErrInvalidSchema)
			}
			if _, duplicate := seenValues[key]; duplicate {
				return fmt.Errorf("%w: duplicate suggested value for field %q", ErrInvalidSchema, field.Name)
			}
			seenValues[key] = struct{}{}
		}
		if field.Type == TypeEnum && len(field.SuggestedValues) == 0 {
			return fmt.Errorf("%w: enum field %q requires values", ErrInvalidSchema, field.Name)
		}
	}
	if len(schema.DefaultOrder) == 0 || len(schema.DefaultOrder) > MaxQuerySortFields {
		return fmt.Errorf(
			"%w: default order must contain between 1 and %d fields",
			ErrInvalidSchema,
			MaxQuerySortFields,
		)
	}
	seenOrder := make(map[Field]struct{}, len(schema.DefaultOrder))
	for _, order := range schema.DefaultOrder {
		field, ok := schema.lookup(order.Field)
		if !ok || !field.Sortable || (order.Direction != Ascending && order.Direction != Descending) {
			return fmt.Errorf("%w: invalid default order", ErrInvalidSchema)
		}
		if _, duplicate := seenOrder[order.Field]; duplicate {
			return fmt.Errorf("%w: duplicate default order field", ErrInvalidSchema)
		}
		seenOrder[order.Field] = struct{}{}
	}
	return nil
}

// Clone returns a detached JSON-projectable schema value.
func (schema Schema) Clone() Schema {
	return Schema{
		Fields:       cloneFieldSchemas(schema.Fields),
		DefaultOrder: append([]SortField(nil), schema.DefaultOrder...),
	}
}

func (schema Schema) lookup(field Field) (FieldSchema, bool) {
	for _, declaration := range schema.Fields {
		if declaration.Name == field {
			cloned := declaration
			cloned.Operators = append([]Operator(nil), declaration.Operators...)
			cloned.SuggestedValues = append([]string(nil), declaration.SuggestedValues...)
			return cloned, true
		}
	}
	return FieldSchema{}, false
}

func cloneFieldSchemas(fields []FieldSchema) []FieldSchema {
	cloned := make([]FieldSchema, len(fields))
	for index, field := range fields {
		cloned[index] = field
		cloned[index].Operators = append([]Operator(nil), field.Operators...)
		cloned[index].SuggestedValues = append([]string(nil), field.SuggestedValues...)
	}
	return cloned
}

func validFieldName(field Field) bool {
	value := string(field)
	if value == "" || len(value) > MaxFieldNameBytes || !utf8.ValidString(value) {
		return false
	}
	if value == "all" || value == "not" {
		return false
	}
	for index, character := range value {
		switch {
		case character >= 'a' && character <= 'z':
		case index > 0 && character >= '0' && character <= '9':
		case index > 0 && (character == '_' || character == '.' || character == '-'):
		default:
			return false
		}
	}
	return true
}

func defaultOperators(fieldType FieldType) []Operator {
	switch fieldType {
	case TypeString:
		return []Operator{
			OperatorEqual,
			OperatorNotEqual,
			OperatorContains,
			OperatorNotContains,
			OperatorIn,
			OperatorNotIn,
		}
	case TypeEnum, TypeBoolean:
		return []Operator{OperatorEqual, OperatorNotEqual, OperatorIn, OperatorNotIn}
	case TypeNumber, TypeTimestamp:
		return []Operator{
			OperatorEqual, OperatorNotEqual, OperatorGreater, OperatorGreaterEq,
			OperatorLess, OperatorLessEq, OperatorIn, OperatorNotIn,
		}
	default:
		return nil
	}
}

func operatorMeaningful(fieldType FieldType, operator Operator) bool {
	for _, candidate := range defaultOperators(fieldType) {
		if candidate == operator {
			return true
		}
	}
	return false
}
