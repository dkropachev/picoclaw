package developmentnotifications

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	collectionquery "github.com/sipeed/picoclaw/pkg/collectionquery"
)

const (
	// MaxPageSize remains 100 for notification API compatibility. The shared
	// collection query package supports the standard 200-item maximum.
	MaxPageSize    = 100
	maxCursorBytes = collectionquery.MaxCursorBytes
	cursorVersion  = 1
)

var (
	ErrInvalidCursor = errors.New("invalid development notification cursor")
	ErrInvalidPage   = errors.New("invalid development notification page")
)

// Cursor is the notification compatibility form of a shared collection
// cursor.
type Cursor struct {
	QueryFingerprint string
	EvaluatedAt      time.Time
	Values           []string
	ID               string
}

// Page is one filtered, stably ordered notification result page.
type Page struct {
	Notifications  []Notification `json:"notifications"`
	Total          int            `json:"total"`
	Next           string         `json:"next_cursor,omitempty"`
	CanonicalQuery string         `json:"canonical_query"`
}

// PageNotifications delegates filtering, ordering, and keyset traversal to
// the shared collection subsystem while preserving notification limits and
// error identities.
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
	shared, err := collectionQueryFromNotification(query)
	if err != nil {
		return Page{}, ErrInvalidPage
	}
	page, err := collectionquery.Paginate(
		all,
		shared,
		encodedCursor,
		limit,
		now,
		notificationPageOptions(),
	)
	if err != nil {
		if errors.Is(err, collectionquery.ErrInvalidCursor) {
			return Page{}, ErrInvalidCursor
		}
		return Page{}, fmt.Errorf("%w: %v", ErrInvalidPage, err)
	}
	return Page{
		Notifications:  page.Items,
		Total:          page.Total,
		Next:           page.NextCursor,
		CanonicalQuery: query.Canonical(),
	}, nil
}

// SortNotifications returns a detached stable ordering without filtering.
func SortNotifications(all []Notification, query Query, now time.Time) ([]Notification, error) {
	if now.IsZero() || query.Validate() != nil {
		return nil, ErrInvalidPage
	}
	shared, err := collectionQueryFromNotification(query)
	if err != nil {
		return nil, ErrInvalidPage
	}
	result, err := collectionquery.SortItems(all, shared, now, notificationPageOptions())
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidPage, err)
	}
	return result, nil
}

func CursorFor(query Query, notification Notification, now time.Time) (string, error) {
	if query.Validate() != nil || now.IsZero() {
		return "", ErrInvalidCursor
	}
	shared, err := collectionQueryFromNotification(query)
	if err != nil {
		return "", ErrInvalidCursor
	}
	encoded, err := collectionquery.CursorFor(shared, notification, now, notificationPageOptions())
	if err != nil {
		return "", fmt.Errorf("%w: %v", ErrInvalidCursor, err)
	}
	return encoded, nil
}

func DecodeCursor(encoded string, query Query) (Cursor, error) {
	if encoded == "" || len(encoded) > maxCursorBytes || query.Validate() != nil {
		return Cursor{}, ErrInvalidCursor
	}
	shared, err := collectionQueryFromNotification(query)
	if err != nil {
		return Cursor{}, ErrInvalidCursor
	}
	decoded, err := collectionquery.DecodeCursor(encoded, shared, notificationCursorIDValid)
	if err != nil {
		return Cursor{}, ErrInvalidCursor
	}
	for index, value := range decoded.Values {
		if !validNotificationCursorValue(query.EffectiveOrder()[index].Field, value) {
			return Cursor{}, ErrInvalidCursor
		}
	}
	return Cursor{
		QueryFingerprint: decoded.QueryFingerprint,
		EvaluatedAt:      decoded.EvaluatedAt,
		Values:           append([]string(nil), decoded.Values...),
		ID:               decoded.ID,
	}, nil
}

// cursorWire remains for package-local compatibility tests and has the exact
// canonical wire shape used by collectionquery.
type cursorWire struct {
	Version          int       `json:"v"`
	QueryFingerprint string    `json:"query"`
	EvaluatedAt      time.Time `json:"evaluated_at"`
	Values           []string  `json:"values"`
	ID               string    `json:"id"`
}

func notificationPageOptions() collectionquery.PageOptions[Notification] {
	return collectionquery.PageOptions[Notification]{
		Resolve: func(notification Notification, field collectionquery.Field, now time.Time) (collectionquery.FieldValue, bool) {
			switch Field(field) {
			case FieldStatus:
				return collectionquery.EnumValue(string(notification.Status)), true
			case FieldRead:
				return collectionquery.BooleanValue(notification.Read), true
			case FieldSnoozed:
				return collectionquery.BooleanValue(notification.IsSnoozed(now)), true
			case FieldPriority:
				return collectionquery.EnumValue(string(notification.Priority)), true
			case FieldReason:
				return collectionquery.EnumValue(string(notification.Reason)), true
			case FieldRepository:
				return collectionquery.StringValue(notification.Repository), true
			case FieldWorkspace:
				return collectionquery.StringValue(notification.WorkspaceID), true
			case FieldIntent:
				return collectionquery.EnumValue(string(notification.Intent)), true
			case FieldSource:
				return collectionquery.EnumValue(string(notification.SourceKind)), true
			case FieldPhase:
				return collectionquery.StringValue(notification.Phase), true
			case FieldCreated:
				return collectionquery.TimestampValue(notification.CreatedAt), true
			case FieldUpdated:
				return collectionquery.TimestampValue(notification.UpdatedAt), true
			case FieldText:
				return collectionquery.StringValue(strings.TrimSpace(notification.Title + " " + notification.Summary)), true
			default:
				return collectionquery.FieldValue{}, false
			}
		},
		ID: func(notification Notification) (string, error) {
			if err := notification.Validate(); err != nil {
				return "", err
			}
			return notification.ID, nil
		},
		ValidateID: notificationCursorIDValid,
		Clone:      cloneNotification,
		Compare: func(field collectionquery.Field, left, right collectionquery.FieldValue) (int, bool) {
			if Field(field) != FieldPriority {
				return 0, false
			}
			return priorityRank(Priority(left.Text)) - priorityRank(Priority(right.Text)), true
		},
	}
}

func notificationCursorIDValid(value string) bool {
	return validIdentifier(value, maxIDBytes)
}

func validNotificationCursorValue(field Field, value string) bool {
	if len(value) > maxRepositoryBytes {
		return false
	}
	switch field {
	case FieldRead, FieldSnoozed:
		parsed, err := strconv.ParseBool(value)
		return err == nil && strconv.FormatBool(parsed) == value
	case FieldCreated, FieldUpdated:
		parsed, err := time.Parse(time.RFC3339Nano, value)
		return err == nil && parsed.UTC().Format(time.RFC3339Nano) == value
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
