package developmentnotifications

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
)

const (
	MaxSavedViews    = 100
	maxSavedViewName = 80
)

var (
	ErrInvalidSavedView = errors.New("invalid development notification saved view")
	ErrStaleViewVersion = errors.New("stale development notification saved view version")
)

// SavedView persists a named user query and its navigation placement.
type SavedView struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	Query     string    `json:"query"`
	Pinned    bool      `json:"pinned"`
	Default   bool      `json:"default"`
	Position  int       `json:"position"`
	Version   uint64    `json:"version"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// SavedViewDraft contains user-controlled saved-view fields.
type SavedViewDraft struct {
	ID       string
	Name     string
	Query    string
	Pinned   bool
	Default  bool
	Position int
}

// BuiltinView describes one immutable launcher-provided notification view.
type BuiltinView struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Query string `json:"query"`
}

// BuiltinViews returns detached definitions for standard inbox views.
func BuiltinViews() []BuiltinView {
	return []BuiltinView{
		{
			ID: "needs-action", Name: "Needs action",
			Query: "status = open AND snoozed = false ORDER BY priority DESC, updated DESC",
		},
		{ID: "unread", Name: "Unread", Query: "read = false ORDER BY updated DESC"},
		{ID: "snoozed", Name: "Snoozed", Query: "status = open AND snoozed = true ORDER BY updated DESC"},
		{ID: "resolved", Name: "Resolved", Query: "status = resolved ORDER BY updated DESC"},
		{ID: "all", Name: "All", Query: "ORDER BY updated DESC"},
	}
}

// NewSavedView validates a new saved view and initializes its revision.
func NewSavedView(draft SavedViewDraft, now time.Time) (SavedView, error) {
	now, err := validSavedViewTime(now)
	if err != nil {
		return SavedView{}, err
	}
	view := SavedView{
		ID: draft.ID, Name: draft.Name, Query: draft.Query,
		Pinned: draft.Pinned, Default: draft.Default, Position: draft.Position,
		Version: 1, CreatedAt: now, UpdatedAt: now,
	}
	return NormalizeSavedView(view)
}

// UpdateSavedView applies an optimistic, ID-preserving saved-view update.
func UpdateSavedView(
	current SavedView,
	draft SavedViewDraft,
	expectedVersion uint64,
	now time.Time,
) (SavedView, bool, error) {
	current, err := NormalizeSavedView(current)
	if err != nil {
		return SavedView{}, false, err
	}
	if expectedVersion != current.Version {
		return SavedView{}, false, ErrStaleViewVersion
	}
	if strings.TrimSpace(draft.ID) != current.ID {
		return SavedView{}, false, fmt.Errorf("%w: ID cannot change", ErrInvalidSavedView)
	}
	next := current
	next.Name = draft.Name
	next.Query = draft.Query
	next.Pinned = draft.Pinned
	next.Default = draft.Default
	next.Position = draft.Position
	next, err = NormalizeSavedView(next)
	if err != nil {
		return SavedView{}, false, err
	}
	if next.Name == current.Name && next.Query == current.Query && next.Pinned == current.Pinned &&
		next.Default == current.Default && next.Position == current.Position {
		return current, false, nil
	}
	now, err = validSavedViewTime(now)
	if err != nil {
		return SavedView{}, false, err
	}
	if now.Before(current.UpdatedAt) {
		return SavedView{}, false, fmt.Errorf("%w: update time predates view", ErrInvalidSavedView)
	}
	next.Version++
	next.UpdatedAt = now
	if next.Version == 0 {
		return SavedView{}, false, fmt.Errorf("%w: version overflow", ErrInvalidSavedView)
	}
	next, err = NormalizeSavedView(next)
	if err != nil {
		return SavedView{}, false, err
	}
	return next, true, nil
}

// NormalizeSavedView validates and returns a detached canonical saved view.
// Query text is whitespace-trimmed but otherwise preserved for user editing;
// ParseQuery supplies its formatting-independent fingerprint.
func NormalizeSavedView(view SavedView) (SavedView, error) {
	view.ID = strings.TrimSpace(view.ID)
	view.Name = strings.TrimSpace(view.Name)
	view.Query = strings.TrimSpace(view.Query)
	view.CreatedAt = view.CreatedAt.UTC()
	view.UpdatedAt = view.UpdatedAt.UTC()
	if !validIdentifier(view.ID, maxIDBytes) || !validSingleLine(view.Name, maxSavedViewName) ||
		view.Position < 0 || view.Position >= MaxSavedViews || view.Version == 0 ||
		view.CreatedAt.IsZero() || view.UpdatedAt.IsZero() || view.UpdatedAt.Before(view.CreatedAt) {
		return SavedView{}, ErrInvalidSavedView
	}
	if _, err := ParseQuery(view.Query); err != nil {
		return SavedView{}, fmt.Errorf("%w: %v", ErrInvalidSavedView, err)
	}
	return view, nil
}

// ValidateSavedViews enforces account-level invariants and returns views in
// deterministic position/name/ID order.
func ValidateSavedViews(views []SavedView) ([]SavedView, error) {
	if len(views) > MaxSavedViews {
		return nil, fmt.Errorf("%w: more than %d views", ErrInvalidSavedView, MaxSavedViews)
	}
	result := make([]SavedView, len(views))
	ids := make(map[string]struct{}, len(views))
	names := make(map[string]struct{}, len(views))
	defaultCount := 0
	for index, view := range views {
		normalized, err := NormalizeSavedView(view)
		if err != nil {
			return nil, err
		}
		nameKey := strings.ToLower(normalized.Name)
		if _, exists := ids[normalized.ID]; exists {
			return nil, fmt.Errorf("%w: duplicate ID", ErrInvalidSavedView)
		}
		if _, exists := names[nameKey]; exists {
			return nil, fmt.Errorf("%w: duplicate name", ErrInvalidSavedView)
		}
		ids[normalized.ID] = struct{}{}
		names[nameKey] = struct{}{}
		if normalized.Default {
			defaultCount++
		}
		result[index] = normalized
	}
	if defaultCount > 1 {
		return nil, fmt.Errorf("%w: multiple default views", ErrInvalidSavedView)
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].Position != result[j].Position {
			return result[i].Position < result[j].Position
		}
		if byName := strings.Compare(strings.ToLower(result[i].Name), strings.ToLower(result[j].Name)); byName != 0 {
			return byName < 0
		}
		return result[i].ID < result[j].ID
	})
	return result, nil
}

func validSavedViewTime(value time.Time) (time.Time, error) {
	value = value.UTC()
	if value.IsZero() {
		return time.Time{}, fmt.Errorf("%w: time is required", ErrInvalidSavedView)
	}
	return value, nil
}
