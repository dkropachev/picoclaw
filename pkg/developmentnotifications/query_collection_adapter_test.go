package developmentnotifications

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	collectionquery "github.com/sipeed/picoclaw/pkg/collectionquery"
)

func TestNotificationQuerySchemaProjectionAndCanonicalRoundTrip(t *testing.T) {
	schema := QuerySchema()
	require.NoError(t, schema.Validate())
	assert.Len(t, schema.Fields, 13)
	assert.Equal(t, []collectionquery.SortField{{
		Field: collectionquery.Field(FieldUpdated), Direction: collectionquery.Descending,
	}}, schema.DefaultOrder)
	raw, err := json.Marshal(schema)
	require.NoError(t, err)
	assert.NotContains(t, string(raw), "text\":\"number")
	assert.Contains(t, string(raw), `"suggested_values":["open","resolved","archived"]`)

	query, err := ParseQuery("")
	require.NoError(t, err)
	roundTrip, err := ParseQuery(query.Canonical())
	require.NoError(t, err)
	assert.Equal(t, query.Canonical(), roundTrip.Canonical())
	assert.Equal(t, query.Fingerprint(), roundTrip.Fingerprint())

	// Returned schema data is detached from the package-level allowlist.
	schema.Fields[0].SuggestedValues[0] = "mutated"
	assert.Equal(t, "open", QuerySchema().Fields[0].SuggestedValues[0])
}

func TestNotificationAdapterKeepsHundredItemLimit(t *testing.T) {
	query, err := ParseQuery("")
	require.NoError(t, err)
	_, err = PageNotifications(nil, query, "", MaxPageSize+1, testTime())
	assert.ErrorIs(t, err, ErrInvalidPage)
	assert.Equal(t, 100, MaxPageSize)
	assert.Equal(t, 200, collectionquery.MaxPageSize)
}

func testTime() (value time.Time) {
	return time.Date(2026, 8, 25, 12, 0, 0, 0, time.UTC)
}
