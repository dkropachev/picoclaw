//nolint:govet // Independent lifecycle assertions intentionally reuse err.
package developmentnotifications

import (
	"errors"
	"math"
	"strings"
	"testing"
	"time"
)

type residualUnknownExpression struct{}

func (residualUnknownExpression) evaluate(Notification, time.Time) bool { return false }
func (residualUnknownExpression) canonical() string                     { return "unknown" }

func TestDevelopmentNotificationLifecycleResidualMatrix(t *testing.T) {
	now := time.Date(2026, 9, 2, 12, 0, 0, 0, time.UTC)
	draft := testDraft()
	if _, err := Upsert(nil, draft, time.Time{}); !errors.Is(err, ErrInvalidNotification) {
		t.Fatalf("zero-time upsert error = %v", err)
	}
	badDraft := draft
	badDraft.ID = "bad id"
	if _, err := Upsert(nil, badDraft, now); !errors.Is(err, ErrInvalidNotification) {
		t.Fatalf("invalid draft error = %v", err)
	}
	created, err := Upsert(nil, draft, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Upsert(&Notification{}, draft, now); !errors.Is(err, ErrInvalidNotification) {
		t.Fatalf("invalid current error = %v", err)
	}
	changedIdentity := draft
	changedIdentity.Intent = IntentPickupPR
	if _, err := Upsert(&created.Notification, changedIdentity, now.Add(time.Minute)); !errors.Is(
		err,
		ErrInvalidNotification,
	) {
		t.Fatalf("changed identity error = %v", err)
	}
	if _, err := Upsert(&created.Notification, draft, now.Add(-time.Minute)); !errors.Is(err, ErrInvalidNotification) {
		t.Fatalf("predating upsert error = %v", err)
	}
	if value, changed, err := ClearSnooze(created.Notification, now.Add(time.Minute)); err != nil || changed ||
		value.SnoozedUntil != nil {
		t.Fatalf("clear empty snooze = %#v/%v/%v", value, changed, err)
	}
	snoozed, _, err := Snooze(created.Notification, now.Add(time.Hour), now.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if value, changed, err := ClearSnooze(snoozed, now.Add(2*time.Minute)); err != nil || !changed ||
		value.SnoozedUntil != nil {
		t.Fatalf("clear snooze = %#v/%v/%v", value, changed, err)
	}
	if _, _, err := ReconcileActive(Notification{}, true, now); !errors.Is(err, ErrInvalidNotification) {
		t.Fatalf("invalid active reconciliation error = %v", err)
	}
	if value, changed, err := ReconcileActive(created.Notification, true, now); err != nil || changed ||
		value.ID != created.Notification.ID {
		t.Fatalf("active reconciliation = %#v/%v/%v", value, changed, err)
	}
	if _, err := RetentionDeadline(created.Notification, -time.Second); !errors.Is(err, ErrInvalidNotification) {
		t.Fatalf("negative retention error = %v", err)
	}
	if eligible, err := EligibleForDeletion(Notification{}, now, 0); eligible ||
		!errors.Is(err, ErrInvalidNotification) {
		t.Fatalf("invalid deletion eligibility = %v/%v", eligible, err)
	}
	applyErr := errors.New("apply")
	if _, changed, err := mutate(created.Notification, now.Add(time.Minute), func(*Notification) (bool, error) {
		return false, applyErr
	}); changed || !errors.Is(err, applyErr) {
		t.Fatalf("mutation apply error = %v/%v", changed, err)
	}
	if validReason("invalid") || validStatus("invalid") || validIntent("invalid") || validSourceKind("invalid") {
		t.Fatal("invalid notification enum accepted")
	}
}

func TestDevelopmentNotificationQueryComparisonResidualMatrix(t *testing.T) {
	now := time.Date(2026, 9, 2, 12, 0, 0, 0, time.UTC)
	notification := mustNotification(t, "dnt_residual", now)
	invalidQuery := Query{Filter: residualUnknownExpression{}}
	if invalidQuery.Match(notification, now) || invalidQuery.Canonical() != "" || invalidQuery.Fingerprint() != "" {
		t.Fatal("invalid query produced a match or identity")
	}
	if err := invalidQuery.Validate(); err == nil {
		t.Fatal("unknown expression validated")
	}
	boolValues := []Value{{Kind: ValueBool, Bool: true}, {Kind: ValueBool, Bool: false}}
	for _, operator := range []Operator{
		OperatorEqual, OperatorNotEqual, OperatorIn, OperatorNotIn, Operator("bad"),
	} {
		_ = compareBool(true, operator, boolValues)
	}
	stringValues := []Value{{Kind: ValueString, Text: "repo"}, {Kind: ValueString, Text: "other"}}
	for _, operator := range []Operator{
		OperatorEqual, OperatorNotEqual, OperatorContains, OperatorNotContains,
		OperatorIn, OperatorNotIn, Operator("bad"),
	} {
		_ = compareString("Repo", operator, stringValues)
	}
	for _, operator := range []Operator{
		OperatorEqual, OperatorNotEqual, OperatorGreater, OperatorGreaterEq,
		OperatorLess, OperatorLessEq, Operator("bad"),
	} {
		_ = compareTime(now, operator, Value{Kind: ValueTime, Time: now}, now)
	}
	_ = compareTime(now, OperatorGreater, Value{Kind: ValueRelativeTime, TimeOffset: -time.Minute}, now)
	if notificationString(notification, Field("bad")) != "" {
		t.Fatal("unknown notification field returned text")
	}
	if validEnumQueryValue(FieldStatus, "bad") || validSortField(FieldText) == true {
		t.Fatal("invalid query enum or sort field accepted")
	}
}

func TestDevelopmentNotificationSavedViewResidualMatrix(t *testing.T) {
	now := time.Date(2026, 9, 2, 12, 0, 0, 0, time.UTC)
	draft := SavedViewDraft{ID: "view", Name: "View", Query: "ORDER BY updated DESC"}
	if _, err := NewSavedView(draft, time.Time{}); !errors.Is(err, ErrInvalidSavedView) {
		t.Fatalf("zero-time view error = %v", err)
	}
	view, err := NewSavedView(draft, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := UpdateSavedView(SavedView{}, draft, 1, now); !errors.Is(err, ErrInvalidSavedView) {
		t.Fatalf("invalid current view error = %v", err)
	}
	if _, _, err := UpdateSavedView(view, draft, 2, now); !errors.Is(err, ErrStaleViewVersion) {
		t.Fatalf("stale view error = %v", err)
	}
	changedID := draft
	changedID.ID = "other"
	if _, _, err := UpdateSavedView(view, changedID, 1, now); !errors.Is(err, ErrInvalidSavedView) {
		t.Fatalf("changed view ID error = %v", err)
	}
	if value, changed, err := UpdateSavedView(view, draft, 1, now.Add(time.Minute)); err != nil || changed ||
		value.ID != view.ID {
		t.Fatalf("unchanged view = %#v/%v/%v", value, changed, err)
	}
	updatedDraft := draft
	updatedDraft.Name = "Updated"
	if _, _, err := UpdateSavedView(view, updatedDraft, 1, time.Time{}); !errors.Is(err, ErrInvalidSavedView) {
		t.Fatalf("zero update time error = %v", err)
	}
	if _, _, err := UpdateSavedView(view, updatedDraft, 1, now.Add(-time.Minute)); !errors.Is(
		err,
		ErrInvalidSavedView,
	) {
		t.Fatalf("predating view update error = %v", err)
	}
	overflow := view
	overflow.Version = math.MaxUint64
	if _, _, err := UpdateSavedView(overflow, updatedDraft, math.MaxUint64, now.Add(time.Minute)); !errors.Is(
		err,
		ErrInvalidSavedView,
	) {
		t.Fatalf("view version overflow error = %v", err)
	}
	duplicateID := view
	duplicateID.Name = "Other"
	duplicateID.Position = 1
	duplicateName := view
	duplicateName.ID = "view-two"
	duplicateName.Position = 2
	for _, values := range [][]SavedView{
		make([]SavedView, MaxSavedViews+1),
		{view, duplicateID},
		{view, duplicateName},
	} {
		if _, err := ValidateSavedViews(values); !errors.Is(err, ErrInvalidSavedView) {
			t.Errorf("invalid saved views error = %v", err)
		}
	}
	secondDefault := view
	secondDefault.ID, secondDefault.Name, secondDefault.Position = "view-two", "Other", 1
	view.Default, secondDefault.Default = true, true
	if _, err := ValidateSavedViews([]SavedView{view, secondDefault}); !errors.Is(err, ErrInvalidSavedView) {
		t.Fatalf("multiple default views error = %v", err)
	}
	if _, err := NormalizeSavedView(SavedView{ID: strings.Repeat("x", maxIDBytes+1)}); !errors.Is(
		err,
		ErrInvalidSavedView,
	) {
		t.Fatalf("invalid normalized view error = %v", err)
	}
}
