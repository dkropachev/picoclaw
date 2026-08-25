package developmentnotifications

import (
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	collectionquery "github.com/sipeed/picoclaw/pkg/collectionquery"
)

var relativeTimePattern = regexp.MustCompile(`^-[1-9][0-9]*(m|h|d|w)$`)

var ErrInvalidQuery = errors.New("invalid development notification query")

// QueryError retains the notification API's public error identity while the
// shared parser supplies its safe bounded message and zero-based byte offset.
type QueryError struct {
	Position int
	Message  string
}

func (err *QueryError) Error() string {
	return fmt.Sprintf("%v at byte %d: %s", ErrInvalidQuery, err.Position, err.Message)
}

func (err *QueryError) Unwrap() error { return ErrInvalidQuery }

// ParseQuery is the compatibility adapter for the shared collection parser.
func ParseQuery(input string) (Query, error) {
	parsed, err := collectionquery.Parse(input, notificationCollectionQuerySchema)
	if err != nil {
		var queryErr *collectionquery.QueryError
		if errors.As(err, &queryErr) {
			return Query{}, &QueryError{Position: queryErr.Position, Message: queryErr.Message}
		}
		return Query{}, &QueryError{Position: 0, Message: "invalid query"}
	}
	return notificationQueryFromCollection(parsed)
}

// parseTimeValue is retained for validation of programmatically constructed
// notification AST values.
func parseTimeValue(raw string, position int) (Value, error) {
	if relativeTimePattern.MatchString(strings.ToLower(raw)) {
		unit := raw[len(raw)-1]
		amount, err := strconv.ParseInt(raw[1:len(raw)-1], 10, 64)
		if err != nil {
			return Value{}, &QueryError{Position: position, Message: "relative date is too large"}
		}
		multiplier := time.Minute
		switch unit {
		case 'h', 'H':
			multiplier = time.Hour
		case 'd', 'D':
			multiplier = 24 * time.Hour
		case 'w', 'W':
			multiplier = 7 * 24 * time.Hour
		}
		if amount > int64((1<<63-1)/multiplier) {
			return Value{}, &QueryError{Position: position, Message: "relative date is too large"}
		}
		return Value{
			Kind: ValueRelativeTime, Text: strings.ToLower(raw),
			TimeOffset: -time.Duration(amount) * multiplier,
		}, nil
	}
	parsed, err := time.Parse(time.RFC3339Nano, raw)
	if err != nil {
		if date, dateErr := time.Parse("2006-01-02", raw); dateErr == nil {
			parsed = date
		} else {
			return Value{}, &QueryError{Position: position, Message: "expected ISO timestamp or relative date"}
		}
	}
	return Value{Kind: ValueTime, Time: parsed.UTC()}, nil
}
