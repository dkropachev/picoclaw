package collectionquery

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

const (
	MaxCursorBytes      = 4 << 10
	MaxCursorValueBytes = 4 << 10
	MaxStableIDBytes    = 1024
	cursorVersion       = 1
)

var (
	ErrInvalidCursor = errors.New("invalid collection query cursor")
	ErrInvalidPage   = errors.New("invalid collection query page")
)

// Cursor is the decoded stable keyset position. Encoded cursor strings remain
// opaque API values; this form is exposed for compatibility adapters and
// durable stores implementing the same semantics.
type Cursor struct {
	QueryFingerprint string
	EvaluatedAt      time.Time
	Values           []string
	ID               string
}

// ItemResolver resolves one allowlisted field for one collection item.
type ItemResolver[T any] func(item T, field Field, evaluatedAt time.Time) (FieldValue, bool)

// ItemID returns the stable identity used as the final deterministic tie-break.
type ItemID[T any] func(item T) (string, error)

// FieldComparator can override sorting for a field with domain semantics, for
// example a severity rank. Return handled=false to use CompareValues.
type FieldComparator func(field Field, left, right FieldValue) (comparison int, handled bool)

// PageOptions supplies only feature-owned callbacks; no field is accessed by
// reflection. ValidateID defaults to a bounded printable UTF-8 check.
type PageOptions[T any] struct {
	Resolve    ItemResolver[T]
	ID         ItemID[T]
	ValidateID func(string) bool
	Clone      func(T) T
	Compare    FieldComparator
}

// PageResult is one filtered, stably ordered keyset page. Total is the number
// of matching items before applying the cursor.
type PageResult[T any] struct {
	Items      []T    `json:"items"`
	Total      int    `json:"total"`
	NextCursor string `json:"next_cursor,omitempty"`
}

type preparedItem[T any] struct {
	item   T
	id     string
	values []FieldValue
}

// Paginate filters and keyset-pages an in-memory collection. A zero limit uses
// DefaultPageSize; limits above MaxPageSize fail closed.
func Paginate[T any](
	all []T,
	query Query,
	encodedCursor string,
	limit int,
	now time.Time,
	options PageOptions[T],
) (PageResult[T], error) {
	if limit == 0 {
		limit = DefaultPageSize
	}
	if limit < 1 || limit > MaxPageSize || now.IsZero() || query.Validate() != nil ||
		options.Resolve == nil || options.ID == nil {
		return PageResult[T]{}, ErrInvalidPage
	}
	now = now.UTC()
	var after *Cursor
	if encodedCursor != "" {
		cursor, err := DecodeCursor(encodedCursor, query, options.ValidateID)
		if err != nil {
			return PageResult[T]{}, err
		}
		after = &cursor
		now = cursor.EvaluatedAt
	}
	selected := make([]preparedItem[T], 0, len(all))
	for _, item := range all {
		prepared, err := prepareItem(item, query, now, options)
		if err != nil {
			return PageResult[T]{}, fmt.Errorf("%w: %v", ErrInvalidPage, err)
		}
		matched, err := query.Match(now, func(field Field, evaluatedAt time.Time) (FieldValue, bool) {
			return options.Resolve(item, field, evaluatedAt)
		})
		if err != nil {
			return PageResult[T]{}, fmt.Errorf("%w: %v", ErrInvalidPage, err)
		}
		if matched {
			selected = append(selected, prepared)
		}
	}
	sortPrepared(selected, query, options.Compare)
	total := len(selected)
	if after != nil {
		cursorValues, err := decodeCursorValues(*after, query)
		if err != nil {
			return PageResult[T]{}, err
		}
		write := 0
		for _, item := range selected {
			if comparePreparedCursor(item, after.ID, cursorValues, query, options.Compare) > 0 {
				selected[write] = item
				write++
			}
		}
		selected = selected[:write]
	}

	hasMore := len(selected) > limit
	if hasMore {
		selected = selected[:limit]
	}
	page := PageResult[T]{Items: make([]T, len(selected)), Total: total}
	for index := range selected {
		page.Items[index] = cloneItem(selected[index].item, options.Clone)
	}
	if hasMore {
		next, err := encodePreparedCursor(query, selected[len(selected)-1], now, options.ValidateID)
		if err != nil {
			return PageResult[T]{}, err
		}
		page.NextCursor = next
	}
	return page, nil
}

// SortItems returns a detached matching-agnostic stable ordering.
func SortItems[T any](
	all []T,
	query Query,
	now time.Time,
	options PageOptions[T],
) ([]T, error) {
	if now.IsZero() || query.Validate() != nil || options.Resolve == nil || options.ID == nil {
		return nil, ErrInvalidPage
	}
	now = now.UTC()
	prepared := make([]preparedItem[T], len(all))
	for index, item := range all {
		value, err := prepareItem(item, query, now, options)
		if err != nil {
			return nil, fmt.Errorf("%w: %v", ErrInvalidPage, err)
		}
		prepared[index] = value
	}
	sortPrepared(prepared, query, options.Compare)
	result := make([]T, len(prepared))
	for index := range prepared {
		result[index] = cloneItem(prepared[index].item, options.Clone)
	}
	return result, nil
}

// CursorFor creates a cursor at one item using the query's effective order.
func CursorFor[T any](
	query Query,
	item T,
	now time.Time,
	options PageOptions[T],
) (string, error) {
	if now.IsZero() || query.Validate() != nil || options.Resolve == nil || options.ID == nil {
		return "", ErrInvalidCursor
	}
	prepared, err := prepareItem(item, query, now.UTC(), options)
	if err != nil {
		return "", fmt.Errorf("%w: %v", ErrInvalidCursor, err)
	}
	return encodePreparedCursor(query, prepared, now.UTC(), options.ValidateID)
}

// DecodeCursor validates canonical encoding, query binding, ordering values,
// timestamp anchoring, and stable identity.
func DecodeCursor(encoded string, query Query, validateID func(string) bool) (Cursor, error) {
	if encoded == "" || len(encoded) > MaxCursorBytes || query.Validate() != nil {
		return Cursor{}, ErrInvalidCursor
	}
	raw, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil || base64.RawURLEncoding.EncodeToString(raw) != encoded ||
		len(raw) == 0 || len(raw) > MaxCursorBytes {
		return Cursor{}, ErrInvalidCursor
	}
	var wire cursorWire
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&wire); err != nil {
		return Cursor{}, ErrInvalidCursor
	}
	var trailing json.RawMessage
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return Cursor{}, ErrInvalidCursor
	}
	canonical, err := json.Marshal(wire)
	if err != nil || !bytes.Equal(raw, canonical) || wire.Version != cursorVersion ||
		wire.QueryFingerprint != query.Fingerprint() || !validStableID(wire.ID, validateID) ||
		len(wire.Values) != len(query.EffectiveOrder()) || !validCursorTime(wire.EvaluatedAt) {
		return Cursor{}, ErrInvalidCursor
	}
	cursor := Cursor{
		QueryFingerprint: wire.QueryFingerprint,
		EvaluatedAt:      wire.EvaluatedAt,
		Values:           append([]string(nil), wire.Values...),
		ID:               wire.ID,
	}
	if _, err := decodeCursorValues(cursor, query); err != nil {
		return Cursor{}, err
	}
	return cursor, nil
}

type cursorWire struct {
	Version          int       `json:"v"`
	QueryFingerprint string    `json:"query"`
	EvaluatedAt      time.Time `json:"evaluated_at"`
	Values           []string  `json:"values"`
	ID               string    `json:"id"`
}

func prepareItem[T any](
	item T,
	query Query,
	now time.Time,
	options PageOptions[T],
) (preparedItem[T], error) {
	id, err := options.ID(item)
	if err != nil || !validStableID(id, options.ValidateID) {
		return preparedItem[T]{}, errors.New("invalid stable item ID")
	}
	order := query.EffectiveOrder()
	values := make([]FieldValue, len(order))
	for index, sortField := range order {
		declaration, ok := query.schema.lookup(sortField.Field)
		if !ok {
			return preparedItem[T]{}, ErrFieldResolution
		}
		value, ok := options.Resolve(item, sortField.Field, now)
		if !ok {
			return preparedItem[T]{}, fmt.Errorf("%w: %s", ErrFieldResolution, sortField.Field)
		}
		value, err = normalizeFieldValue(declaration, value)
		if err != nil {
			return preparedItem[T]{}, err
		}
		encoded, err := sortableString(declaration, value)
		if err != nil || len(encoded) > MaxCursorValueBytes {
			return preparedItem[T]{}, ErrFieldResolution
		}
		values[index] = value
	}
	return preparedItem[T]{item: item, id: id, values: values}, nil
}

func sortPrepared[T any](items []preparedItem[T], query Query, compare FieldComparator) {
	sort.SliceStable(items, func(left, right int) bool {
		return comparePrepared(items[left], items[right], query, compare) < 0
	})
}

func comparePrepared[T any](left, right preparedItem[T], query Query, compare FieldComparator) int {
	for index, order := range query.EffectiveOrder() {
		comparison := compareField(query.schema, order.Field, left.values[index], right.values[index], compare)
		if comparison != 0 {
			if order.Direction == Descending {
				comparison = -comparison
			}
			return comparison
		}
	}
	return -strings.Compare(left.id, right.id)
}

func comparePreparedCursor[T any](
	item preparedItem[T],
	cursorID string,
	cursorValues []FieldValue,
	query Query,
	compare FieldComparator,
) int {
	for index, order := range query.EffectiveOrder() {
		comparison := compareField(query.schema, order.Field, item.values[index], cursorValues[index], compare)
		if comparison != 0 {
			if order.Direction == Descending {
				comparison = -comparison
			}
			return comparison
		}
	}
	return -strings.Compare(item.id, cursorID)
}

func compareField(schema Schema, field Field, left, right FieldValue, compare FieldComparator) int {
	if compare != nil {
		if comparison, handled := compare(field, left, right); handled {
			return comparison
		}
	}
	declaration, _ := schema.lookup(field)
	comparison, _ := CompareValues(declaration, left, right)
	return comparison
}

func encodePreparedCursor[T any](
	query Query,
	item preparedItem[T],
	now time.Time,
	validateID func(string) bool,
) (string, error) {
	if !validStableID(item.id, validateID) || now.IsZero() {
		return "", ErrInvalidCursor
	}
	order := query.EffectiveOrder()
	values := make([]string, len(order))
	for index, sortField := range order {
		field, _ := query.schema.lookup(sortField.Field)
		encoded, err := sortableString(field, item.values[index])
		if err != nil || len(encoded) > MaxCursorValueBytes {
			return "", ErrInvalidCursor
		}
		values[index] = encoded
	}
	wire := cursorWire{
		Version: cursorVersion, QueryFingerprint: query.Fingerprint(),
		EvaluatedAt: now.UTC(), Values: values, ID: item.id,
	}
	raw, err := json.Marshal(wire)
	if err != nil || len(raw) > MaxCursorBytes {
		return "", ErrInvalidCursor
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}

func decodeCursorValues(cursor Cursor, query Query) ([]FieldValue, error) {
	order := query.EffectiveOrder()
	if len(cursor.Values) != len(order) {
		return nil, ErrInvalidCursor
	}
	values := make([]FieldValue, len(order))
	for index, sortField := range order {
		if len(cursor.Values[index]) > MaxCursorValueBytes {
			return nil, ErrInvalidCursor
		}
		field, ok := query.schema.lookup(sortField.Field)
		if !ok {
			return nil, ErrInvalidCursor
		}
		value, err := fieldValueFromSortable(field, cursor.Values[index])
		if err != nil {
			return nil, ErrInvalidCursor
		}
		values[index] = value
	}
	return values, nil
}

func validStableID(value string, validate func(string) bool) bool {
	if validate != nil {
		return validate(value)
	}
	if value == "" || len(value) > MaxStableIDBytes || !utf8.ValidString(value) {
		return false
	}
	return strings.IndexFunc(value, unicode.IsControl) < 0
}

func validCursorTime(value time.Time) bool {
	return !value.IsZero() && value.Location() == time.UTC
}

func cloneItem[T any](item T, clone func(T) T) T {
	if clone == nil {
		return item
	}
	return clone(item)
}
