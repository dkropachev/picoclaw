package developmentnotifications

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
	"time"
)

const (
	MaxPageSize    = 100
	maxCursorBytes = 4096
	cursorVersion  = 1
)

var (
	ErrInvalidCursor = errors.New("invalid development notification cursor")
	ErrInvalidPage   = errors.New("invalid development notification page")
)

// Cursor is an opaque-in-spirit stable keyset position. QueryFingerprint
// prevents a cursor from being reused with different filters or ordering.
type Cursor struct {
	QueryFingerprint string
	EvaluatedAt      time.Time
	Values           []string
	ID               string
}

// Page is one filtered, stably ordered notification result page.
type Page struct {
	Notifications []Notification `json:"notifications"`
	Next          string         `json:"next_cursor,omitempty"`
}

// PageNotifications is an in-memory reference implementation of query,
// ordering, and keyset semantics. Durable stores can use CursorFor and
// DecodeCursor while translating the same typed order into parameterized SQL.
func PageNotifications(
	all []Notification,
	query Query,
	encodedCursor string,
	limit int,
	now time.Time,
) (Page, error) {
	if limit < 1 || limit > MaxPageSize || now.IsZero() || query.Validate() != nil {
		return Page{}, ErrInvalidPage
	}
	now = now.UTC()
	var after *Cursor
	if encodedCursor != "" {
		cursor, err := DecodeCursor(encodedCursor, query)
		if err != nil {
			return Page{}, err
		}
		after = &cursor
		// Relative-time and snooze predicates remain fixed throughout one
		// keyset traversal, even if wall clock time advances between requests.
		now = cursor.EvaluatedAt
	}
	selected := make([]Notification, 0, len(all))
	for _, notification := range all {
		if err := notification.Validate(); err != nil {
			return Page{}, fmt.Errorf("%w: %v", ErrInvalidPage, err)
		}
		if query.matchValidated(notification, now) {
			selected = append(selected, cloneNotification(notification))
		}
	}
	sort.SliceStable(selected, func(i, j int) bool {
		return compareNotifications(selected[i], selected[j], query, now) < 0
	})
	if after != nil {
		write := 0
		for _, notification := range selected {
			if compareNotificationCursor(notification, *after, query, now) > 0 {
				selected[write] = notification
				write++
			}
		}
		selected = selected[:write]
	}

	hasMore := len(selected) > limit
	if hasMore {
		selected = selected[:limit]
	}
	page := Page{Notifications: selected}
	if hasMore {
		next, err := CursorFor(query, selected[len(selected)-1], now)
		if err != nil {
			return Page{}, err
		}
		page.Next = next
	}
	return page, nil
}

// SortNotifications returns a detached stable ordering of matching-agnostic
// notifications. ID DESC is always the final tie-break.
func SortNotifications(all []Notification, query Query, now time.Time) ([]Notification, error) {
	if now.IsZero() || query.Validate() != nil {
		return nil, ErrInvalidPage
	}
	now = now.UTC()
	result := make([]Notification, len(all))
	for index, notification := range all {
		if err := notification.Validate(); err != nil {
			return nil, err
		}
		result[index] = cloneNotification(notification)
	}
	sort.SliceStable(result, func(i, j int) bool {
		return compareNotifications(result[i], result[j], query, now) < 0
	})
	return result, nil
}

func CursorFor(query Query, notification Notification, now time.Time) (string, error) {
	if err := query.Validate(); err != nil || now.IsZero() {
		return "", ErrInvalidCursor
	}
	if err := notification.Validate(); err != nil {
		return "", fmt.Errorf("%w: %v", ErrInvalidCursor, err)
	}
	now = now.UTC()
	values := make([]string, len(query.EffectiveOrder()))
	for index, order := range query.EffectiveOrder() {
		values[index] = sortableValue(notification, order.Field, now)
	}
	return encodeCursor(Cursor{
		QueryFingerprint: query.Fingerprint(),
		EvaluatedAt:      now,
		Values:           values,
		ID:               notification.ID,
	})
}

func DecodeCursor(encoded string, query Query) (Cursor, error) {
	if encoded == "" || len(encoded) > maxCursorBytes || query.Validate() != nil {
		return Cursor{}, ErrInvalidCursor
	}
	raw, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil || base64.RawURLEncoding.EncodeToString(raw) != encoded ||
		len(raw) == 0 || len(raw) > maxCursorBytes {
		return Cursor{}, ErrInvalidCursor
	}
	var wire cursorWire
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if decodeErr := decoder.Decode(&wire); decodeErr != nil {
		return Cursor{}, ErrInvalidCursor
	}
	var trailing json.RawMessage
	if trailingErr := decoder.Decode(&trailing); !errors.Is(trailingErr, io.EOF) {
		return Cursor{}, ErrInvalidCursor
	}
	canonical, err := json.Marshal(wire)
	if err != nil || !bytes.Equal(raw, canonical) || wire.Version != cursorVersion ||
		wire.QueryFingerprint != query.Fingerprint() || !validIdentifier(wire.ID, maxIDBytes) ||
		len(wire.Values) != len(query.EffectiveOrder()) || !validCursorTime(wire.EvaluatedAt) {
		return Cursor{}, ErrInvalidCursor
	}
	for index, value := range wire.Values {
		if len(value) > maxRepositoryBytes || !validSortableValue(query.EffectiveOrder()[index].Field, value) {
			return Cursor{}, ErrInvalidCursor
		}
	}
	return Cursor{
		QueryFingerprint: wire.QueryFingerprint,
		EvaluatedAt:      wire.EvaluatedAt,
		Values:           append([]string(nil), wire.Values...),
		ID:               wire.ID,
	}, nil
}

type cursorWire struct {
	Version          int       `json:"v"`
	QueryFingerprint string    `json:"query"`
	EvaluatedAt      time.Time `json:"evaluated_at"`
	Values           []string  `json:"values"`
	ID               string    `json:"id"`
}

func encodeCursor(cursor Cursor) (string, error) {
	wire := cursorWire{
		Version: cursorVersion, QueryFingerprint: cursor.QueryFingerprint,
		EvaluatedAt: cursor.EvaluatedAt.UTC(),
		Values:      append([]string(nil), cursor.Values...), ID: cursor.ID,
	}
	raw, err := json.Marshal(wire)
	if err != nil {
		return "", err
	}
	if len(raw) > maxCursorBytes {
		return "", ErrInvalidCursor
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}

func compareNotifications(left, right Notification, query Query, now time.Time) int {
	order := query.EffectiveOrder()
	for _, field := range order {
		comparison := compareSortable(
			field.Field,
			sortableValue(left, field.Field, now),
			sortableValue(right, field.Field, now),
		)
		if comparison != 0 {
			if field.Direction == Descending {
				comparison = -comparison
			}
			return comparison
		}
	}
	// Newer opaque IDs sort first for a deterministic tie-break. Correctness
	// requires uniqueness, not timestamp-shaped IDs.
	return -strings.Compare(left.ID, right.ID)
}

func compareNotificationCursor(notification Notification, cursor Cursor, query Query, now time.Time) int {
	for index, field := range query.EffectiveOrder() {
		comparison := compareSortable(
			field.Field,
			sortableValue(notification, field.Field, now),
			cursor.Values[index],
		)
		if comparison != 0 {
			if field.Direction == Descending {
				comparison = -comparison
			}
			return comparison
		}
	}
	return -strings.Compare(notification.ID, cursor.ID)
}

func sortableValue(n Notification, field Field, now time.Time) string {
	switch field {
	case FieldRead:
		return strconv.FormatBool(n.Read)
	case FieldSnoozed:
		return strconv.FormatBool(n.IsSnoozed(now))
	case FieldCreated:
		return n.CreatedAt.UTC().Format(time.RFC3339Nano)
	case FieldUpdated:
		return n.UpdatedAt.UTC().Format(time.RFC3339Nano)
	default:
		return strings.ToLower(notificationString(n, field))
	}
}

func validSortableValue(field Field, value string) bool {
	switch field {
	case FieldRead, FieldSnoozed:
		_, err := strconv.ParseBool(value)
		return err == nil && (value == "true" || value == "false")
	case FieldCreated, FieldUpdated:
		parsed, err := time.Parse(time.RFC3339Nano, value)
		return err == nil && parsed.UTC().Format(time.RFC3339Nano) == value
	default:
		switch field {
		case FieldStatus, FieldPriority, FieldReason, FieldIntent, FieldSource:
			return validEnumQueryValue(field, value)
		case FieldWorkspace:
			return validIdentifier(value, maxWorkspaceBytes)
		case FieldRepository:
			return utf8ValidBounded(value, maxRepositoryBytes)
		case FieldPhase:
			return utf8ValidBounded(value, maxPhaseBytes)
		default:
			return false
		}
	}
}

func compareSortable(field Field, left, right string) int {
	switch field {
	case FieldRead, FieldSnoozed:
		leftBool, _ := strconv.ParseBool(left)
		rightBool, _ := strconv.ParseBool(right)
		if leftBool == rightBool {
			return 0
		}
		if !leftBool {
			return -1
		}
		return 1
	case FieldCreated, FieldUpdated:
		leftTime, _ := time.Parse(time.RFC3339Nano, left)
		rightTime, _ := time.Parse(time.RFC3339Nano, right)
		if leftTime.Equal(rightTime) {
			return 0
		}
		if leftTime.Before(rightTime) {
			return -1
		}
		return 1
	case FieldPriority:
		return priorityRank(Priority(left)) - priorityRank(Priority(right))
	default:
		return strings.Compare(left, right)
	}
}

func priorityRank(priority Priority) int {
	switch priority {
	case PriorityCritical:
		return 4
	case PriorityHigh:
		return 3
	case PriorityMedium:
		return 2
	case PriorityLow:
		return 1
	default:
		return 0
	}
}

func utf8ValidBounded(value string, maximum int) bool {
	return value != "" && len(value) <= maximum && strings.ToValidUTF8(value, "") == value
}

func validCursorTime(value time.Time) bool {
	return !value.IsZero() && value.Location() == time.UTC
}
