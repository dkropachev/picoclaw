package collectionquery

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type pageTestItem struct {
	ID      string
	Name    string
	Status  string
	Enabled bool
	Score   float64
	Created time.Time
	Expiry  time.Time
	Tags    []string
}

func pageTestOptions() PageOptions[pageTestItem] {
	return PageOptions[pageTestItem]{
		Resolve: func(item pageTestItem, field Field, now time.Time) (FieldValue, bool) {
			switch field {
			case "name":
				return StringValue(item.Name), true
			case "status":
				return EnumValue(item.Status), true
			case "enabled":
				return BooleanValue(item.Enabled), true
			case "score":
				return NumberValue(item.Score), true
			case "created":
				return TimestampValue(item.Created), true
			case "current":
				return BooleanValue(item.Expiry.After(now)), true
			default:
				return FieldValue{}, false
			}
		},
		ID: func(item pageTestItem) (string, error) { return item.ID, nil },
		Clone: func(item pageTestItem) pageTestItem {
			item.Tags = append([]string(nil), item.Tags...)
			return item
		},
	}
}

func pageIDs(items []pageTestItem) []string {
	ids := make([]string, len(items))
	for index := range items {
		ids[index] = items[index].ID
	}
	return ids
}

func TestPaginateStableKeysetTotalDefaultAndBoundaryRemoval(t *testing.T) {
	schema := testSchema(t)
	query, err := Parse("ORDER BY created DESC", schema)
	require.NoError(t, err)
	now := time.Date(2026, 8, 25, 12, 0, 0, 0, time.UTC)
	items := make([]pageTestItem, 55)
	for index := range items {
		items[index] = pageTestItem{
			ID: fmt.Sprintf("item_%02d", index), Name: fmt.Sprintf("Item %02d", index),
			Status: "draft", Created: now.Add(time.Duration(index) * time.Minute),
		}
	}

	first, err := Paginate(items, query, "", 0, now, pageTestOptions())
	require.NoError(t, err)
	assert.Len(t, first.Items, DefaultPageSize)
	assert.Equal(t, 55, first.Total)
	require.NotEmpty(t, first.NextCursor)
	assert.Equal(t, "item_54", first.Items[0].ID)
	assert.Equal(t, "item_05", first.Items[len(first.Items)-1].ID)

	withoutBoundary := append([]pageTestItem(nil), items[:5]...)
	withoutBoundary = append(withoutBoundary, items[6:]...)
	second, err := Paginate(withoutBoundary, query, first.NextCursor, 50, now.Add(time.Hour), pageTestOptions())
	require.NoError(t, err)
	assert.Equal(t, []string{"item_04", "item_03", "item_02", "item_01", "item_00"}, pageIDs(second.Items))
	assert.Empty(t, second.NextCursor)
	assert.Equal(t, 54, second.Total)

	first.Items[0].Tags = append(first.Items[0].Tags, "mutated")
	assert.Empty(t, items[54].Tags, "page results use the feature clone callback")
}

func TestPaginateAcceptsStandardMaximum(t *testing.T) {
	schema := testSchema(t)
	query, err := Parse("", schema)
	require.NoError(t, err)
	now := time.Date(2026, 8, 25, 12, 0, 0, 0, time.UTC)
	items := make([]pageTestItem, MaxPageSize+1)
	for index := range items {
		items[index] = pageTestItem{
			ID: fmt.Sprintf("max_%03d", index), Status: "draft", Created: now.Add(time.Duration(index) * time.Second),
		}
	}
	page, err := Paginate(items, query, "", MaxPageSize, now, pageTestOptions())
	require.NoError(t, err)
	assert.Len(t, page.Items, MaxPageSize)
	assert.Equal(t, MaxPageSize+1, page.Total)
	assert.NotEmpty(t, page.NextCursor)
	_, err = Paginate(items, query, "", MaxPageSize+1, now, pageTestOptions())
	assert.ErrorIs(t, err, ErrInvalidPage)
}

func TestPaginateFiltersCompositeSortAndIDTieBreak(t *testing.T) {
	schema := testSchema(t)
	query, err := Parse("status = active ORDER BY enabled ASC, score DESC, name ASC", schema)
	require.NoError(t, err)
	now := time.Date(2026, 8, 25, 12, 0, 0, 0, time.UTC)
	items := []pageTestItem{
		{ID: "z", Name: "same", Status: "active", Score: 2, Created: now},
		{ID: "a", Name: "same", Status: "active", Score: 2, Created: now},
		{ID: "x", Name: "alpha", Status: "active", Score: 3, Created: now},
		{ID: "y", Name: "beta", Status: "active", Enabled: true, Score: 100, Created: now},
		{ID: "ignored", Name: "top", Status: "done", Score: 999, Created: now},
	}
	page, err := Paginate(items, query, "", 10, now, pageTestOptions())
	require.NoError(t, err)
	assert.Equal(t, []string{"x", "z", "a", "y"}, pageIDs(page.Items))
	assert.Equal(t, 4, page.Total)
}

func TestPaginateAllowsEmptySortableStringCursor(t *testing.T) {
	schema := testSchema(t)
	query, err := Parse("ORDER BY name ASC", schema)
	require.NoError(t, err)
	now := time.Date(2026, 8, 25, 12, 0, 0, 0, time.UTC)
	items := []pageTestItem{
		{ID: "b", Name: "", Status: "draft", Created: now},
		{ID: "a", Name: "", Status: "draft", Created: now},
		{ID: "c", Name: "value", Status: "draft", Created: now},
	}
	first, err := Paginate(items, query, "", 1, now, pageTestOptions())
	require.NoError(t, err)
	assert.Equal(t, []string{"b"}, pageIDs(first.Items))
	require.NotEmpty(t, first.NextCursor)
	decoded, err := DecodeCursor(first.NextCursor, query, nil)
	require.NoError(t, err)
	assert.Equal(t, []string{""}, decoded.Values)
	second, err := Paginate(items, query, first.NextCursor, 2, now, pageTestOptions())
	require.NoError(t, err)
	assert.Equal(t, []string{"a", "c"}, pageIDs(second.Items))
}

func TestCursorAnchorsEvaluationTimeAndRejectsQueryMismatch(t *testing.T) {
	schema, err := NewSchema([]FieldSchema{
		{Name: "current", Type: TypeBoolean, Sortable: true},
		{Name: "created", Type: TypeTimestamp, Sortable: true},
	}, []SortField{{Field: "created", Direction: Descending}})
	require.NoError(t, err)
	query, err := Parse("current = true ORDER BY created DESC", schema)
	require.NoError(t, err)
	start := time.Date(2026, 8, 25, 12, 0, 0, 0, time.UTC)
	items := []pageTestItem{
		{ID: "new", Created: start.Add(-time.Minute), Expiry: start.Add(30 * time.Minute)},
		{ID: "old", Created: start.Add(-time.Hour), Expiry: start.Add(30 * time.Minute)},
	}
	first, err := Paginate(items, query, "", 1, start, pageTestOptions())
	require.NoError(t, err)
	require.NotEmpty(t, first.NextCursor)
	second, err := Paginate(items, query, first.NextCursor, 1, start.Add(time.Hour), pageTestOptions())
	require.NoError(t, err)
	assert.Equal(t, []string{"old"}, pageIDs(second.Items), "cursor evaluation time stays fixed")

	different, err := Parse("current = false ORDER BY created DESC", schema)
	require.NoError(t, err)
	_, err = DecodeCursor(first.NextCursor, different, nil)
	assert.ErrorIs(t, err, ErrInvalidCursor)
}

func TestCursorCanonicalWireAndHostileValues(t *testing.T) {
	schema := testSchema(t)
	query, err := Parse("ORDER BY score DESC, created ASC", schema)
	require.NoError(t, err)
	now := time.Date(2026, 8, 25, 12, 0, 0, 123, time.UTC)
	item := pageTestItem{ID: "stable:id", Status: "draft", Score: 1.25, Created: now}
	cursor, err := CursorFor(query, item, now, pageTestOptions())
	require.NoError(t, err)
	decoded, err := DecodeCursor(cursor, query, func(id string) bool { return strings.HasPrefix(id, "stable:") })
	require.NoError(t, err)
	assert.Equal(t, []string{"1.25", now.Format(time.RFC3339Nano)}, decoded.Values)
	assert.Equal(t, now, decoded.EvaluatedAt)

	raw, err := base64.RawURLEncoding.DecodeString(cursor)
	require.NoError(t, err)
	nonCanonical := base64.RawURLEncoding.EncodeToString(append(raw, ' '))
	_, err = DecodeCursor(nonCanonical, query, nil)
	assert.ErrorIs(t, err, ErrInvalidCursor)

	invalidCases := []cursorWire{
		{Version: cursorVersion, QueryFingerprint: query.Fingerprint(), EvaluatedAt: now, Values: []string{"NaN", now.Format(time.RFC3339Nano)}, ID: item.ID},
		{Version: cursorVersion, QueryFingerprint: query.Fingerprint(), EvaluatedAt: now, Values: []string{"1.25", "not-a-time"}, ID: item.ID},
		{Version: cursorVersion, QueryFingerprint: query.Fingerprint(), EvaluatedAt: now, Values: []string{"1.25"}, ID: item.ID},
		{Version: cursorVersion + 1, QueryFingerprint: query.Fingerprint(), EvaluatedAt: now, Values: decoded.Values, ID: item.ID},
	}
	for _, wire := range invalidCases {
		raw, marshalErr := json.Marshal(wire)
		require.NoError(t, marshalErr)
		_, decodeErr := DecodeCursor(base64.RawURLEncoding.EncodeToString(raw), query, nil)
		assert.ErrorIs(t, decodeErr, ErrInvalidCursor)
	}
	assert.LessOrEqual(t, len(cursor), base64.RawURLEncoding.EncodedLen(MaxCursorBytes))
}

func TestPaginationCustomComparatorSortAndValidationFailures(t *testing.T) {
	schema := testSchema(t)
	query, err := Parse("ORDER BY status DESC", schema)
	require.NoError(t, err)
	now := time.Date(2026, 8, 25, 12, 0, 0, 0, time.UTC)
	items := []pageTestItem{
		{ID: "draft", Status: "draft", Created: now},
		{ID: "done", Status: "done", Created: now},
		{ID: "active", Status: "active", Created: now},
	}
	options := pageTestOptions()
	rank := map[string]int{"draft": 1, "active": 2, "done": 3}
	options.Compare = func(field Field, left, right FieldValue) (int, bool) {
		if field != "status" {
			return 0, false
		}
		return rank[left.Text] - rank[right.Text], true
	}
	page, err := Paginate(items, query, "", 10, now, options)
	require.NoError(t, err)
	assert.Equal(t, []string{"done", "active", "draft"}, pageIDs(page.Items))

	_, err = Paginate(items, query, "", MaxPageSize+1, now, options)
	assert.ErrorIs(t, err, ErrInvalidPage)
	_, err = Paginate(items, query, "", -1, now, options)
	assert.ErrorIs(t, err, ErrInvalidPage)
	_, err = Paginate(items, query, "", 1, time.Time{}, options)
	assert.ErrorIs(t, err, ErrInvalidPage)

	missing := options
	missing.Resolve = func(pageTestItem, Field, time.Time) (FieldValue, bool) { return FieldValue{}, false }
	_, err = Paginate(items, query, "", 1, now, missing)
	assert.ErrorIs(t, err, ErrInvalidPage)

	wrongType := options
	wrongType.Resolve = func(pageTestItem, Field, time.Time) (FieldValue, bool) { return NumberValue(1), true }
	_, err = Paginate(items, query, "", 1, now, wrongType)
	assert.ErrorIs(t, err, ErrInvalidPage)

	badID := options
	badID.ID = func(pageTestItem) (string, error) { return "", nil }
	_, err = Paginate(items, query, "", 1, now, badID)
	assert.ErrorIs(t, err, ErrInvalidPage)

}

func TestSortItemsAndCursorIDValidation(t *testing.T) {
	schema := testSchema(t)
	query, err := Parse("name ~ keep ORDER BY name ASC", schema)
	require.NoError(t, err)
	now := time.Date(2026, 8, 25, 12, 0, 0, 0, time.UTC)
	items := []pageTestItem{
		{ID: "2", Name: "zeta", Status: "draft", Created: now},
		{ID: "1", Name: "alpha", Status: "done", Created: now},
	}
	sorted, err := SortItems(items, query, now, pageTestOptions())
	require.NoError(t, err)
	assert.Equal(t, []string{"1", "2"}, pageIDs(sorted), "SortItems intentionally ignores the filter")

	options := pageTestOptions()
	options.ValidateID = func(id string) bool { return strings.HasPrefix(id, "allowed_") }
	_, err = CursorFor(query, items[0], now, options)
	assert.ErrorIs(t, err, ErrInvalidCursor)

	oversized := strings.Repeat("x", MaxCursorBytes+1)
	_, err = DecodeCursor(oversized, query, nil)
	assert.ErrorIs(t, err, ErrInvalidCursor)
}
