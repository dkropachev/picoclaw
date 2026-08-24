package developmentnotifications

import (
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBuiltinViewsAreValidQueries(t *testing.T) {
	t.Parallel()
	views := BuiltinViews()
	require.Len(t, views, 5)
	seen := make(map[string]struct{}, len(views))
	for _, view := range views {
		assert.NotEmpty(t, view.ID)
		assert.NotEmpty(t, view.Name)
		_, duplicate := seen[view.ID]
		assert.False(t, duplicate)
		seen[view.ID] = struct{}{}
		_, err := ParseQuery(view.Query)
		require.NoError(t, err, view.ID)
	}
}

func TestNewAndUpdateSavedView(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 8, 24, 12, 0, 0, 0, time.UTC)
	draft := SavedViewDraft{
		ID: "view_team", Name: "  Team blockers  ",
		Query:  "  status = open ORDER BY priority DESC  ",
		Pinned: true, Default: true, Position: 2,
	}
	view, err := NewSavedView(draft, now)
	require.NoError(t, err)
	assert.Equal(t, "Team blockers", view.Name)
	assert.Equal(t, "status = open ORDER BY priority DESC", view.Query)
	assert.Equal(t, uint64(1), view.Version)

	noOpDraft := draft
	updated, changed, err := UpdateSavedView(view, noOpDraft, 1, now.Add(time.Minute))
	require.NoError(t, err)
	assert.False(t, changed)
	assert.Equal(t, view, updated)

	changedDraft := SavedViewDraft{
		ID: view.ID, Name: "Critical only",
		Query:  "priority = critical ORDER BY updated DESC",
		Pinned: true, Position: 0,
	}
	updated, changed, err = UpdateSavedView(view, changedDraft, 1, now.Add(time.Minute))
	require.NoError(t, err)
	assert.True(t, changed)
	assert.Equal(t, uint64(2), updated.Version)
	assert.Equal(t, "Critical only", updated.Name)
	assert.False(t, updated.Default)

	_, _, err = UpdateSavedView(updated, changedDraft, 1, now.Add(2*time.Minute))
	assert.ErrorIs(t, err, ErrStaleViewVersion)

	changedDraft.ID = "view_other"
	_, _, err = UpdateSavedView(updated, changedDraft, updated.Version, now.Add(2*time.Minute))
	assert.ErrorIs(t, err, ErrInvalidSavedView)

	overflow := updated
	overflow.Version = ^uint64(0)
	changedDraft.ID = overflow.ID
	changedDraft.Name = "Overflow"
	_, _, err = UpdateSavedView(overflow, changedDraft, overflow.Version, now.Add(2*time.Minute))
	assert.ErrorIs(t, err, ErrInvalidSavedView)
}

func TestSavedViewValidation(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 8, 24, 12, 0, 0, 0, time.UTC)
	valid, err := NewSavedView(SavedViewDraft{
		ID: "view_valid", Name: "Valid", Query: "status = open", Position: 0,
	}, now)
	require.NoError(t, err)

	tests := []SavedView{
		withSavedView(valid, func(view *SavedView) { view.ID = "bad id" }),
		withSavedView(valid, func(view *SavedView) { view.Name = "" }),
		withSavedView(valid, func(view *SavedView) { view.Query = "status = unknown" }),
		withSavedView(valid, func(view *SavedView) { view.Position = -1 }),
		withSavedView(valid, func(view *SavedView) { view.Position = MaxSavedViews }),
		withSavedView(valid, func(view *SavedView) { view.Version = 0 }),
		withSavedView(valid, func(view *SavedView) { view.UpdatedAt = now.Add(-time.Hour) }),
	}
	for _, view := range tests {
		_, normalizeErr := NormalizeSavedView(view)
		assert.ErrorIs(t, normalizeErr, ErrInvalidSavedView)
	}
}

func TestValidateSavedViewsCollection(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 8, 24, 12, 0, 0, 0, time.UTC)
	first, err := NewSavedView(SavedViewDraft{
		ID: "view_z", Name: "Zulu", Query: "", Position: 2,
	}, now)
	require.NoError(t, err)
	second, err := NewSavedView(SavedViewDraft{
		ID: "view_a", Name: "Alpha", Query: "", Default: true, Position: 0,
	}, now)
	require.NoError(t, err)
	third, err := NewSavedView(SavedViewDraft{
		ID: "view_b", Name: "Beta", Query: "", Position: 0,
	}, now)
	require.NoError(t, err)

	normalized, err := ValidateSavedViews([]SavedView{first, third, second})
	require.NoError(t, err)
	assert.Equal(t, []string{"view_a", "view_b", "view_z"}, savedViewIDs(normalized))

	duplicateID := third
	duplicateID.ID = second.ID
	_, err = ValidateSavedViews([]SavedView{second, duplicateID})
	assert.ErrorIs(t, err, ErrInvalidSavedView)

	duplicateName := third
	duplicateName.Name = "ALPHA"
	_, err = ValidateSavedViews([]SavedView{second, duplicateName})
	assert.ErrorIs(t, err, ErrInvalidSavedView)

	twoDefaults := third
	twoDefaults.Default = true
	_, err = ValidateSavedViews([]SavedView{second, twoDefaults})
	assert.ErrorIs(t, err, ErrInvalidSavedView)
}

func TestValidateSavedViewsBound(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 8, 24, 12, 0, 0, 0, time.UTC)
	views := make([]SavedView, MaxSavedViews+1)
	for index := range views {
		views[index] = SavedView{
			ID: fmt.Sprintf("view_%03d", index), Name: fmt.Sprintf("View %03d", index),
			Position: 0, Version: 1, CreatedAt: now, UpdatedAt: now,
		}
	}
	_, err := ValidateSavedViews(views)
	assert.ErrorIs(t, err, ErrInvalidSavedView)
}

func withSavedView(view SavedView, change func(*SavedView)) SavedView {
	change(&view)
	return view
}

func savedViewIDs(views []SavedView) []string {
	result := make([]string, len(views))
	for index := range views {
		result[index] = views[index].ID
	}
	return result
}
