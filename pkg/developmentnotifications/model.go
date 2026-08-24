// Package developmentnotifications defines the durable, provider-neutral core
// for development-workspace attention notifications.
package developmentnotifications

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

const (
	DefaultResolvedRetention = 90 * 24 * time.Hour

	maxIDBytes         = 128
	maxSourceKeyBytes  = 512
	maxWorkspaceBytes  = 128
	maxRepositoryBytes = 1024
	maxPhaseBytes      = 64
	maxTitleBytes      = 256
	maxSummaryBytes    = 8 << 10
	maxPanelBytes      = 64
	maxEntityIDBytes   = 128
)

var (
	ErrInvalidNotification = errors.New("invalid development notification")
	ErrInvalidTransition   = errors.New("invalid development notification transition")
	ErrStaleGeneration     = errors.New("stale development notification generation")
)

var identifierPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.:-]*$`)

// Priority controls inbox ordering and eligibility for selective mobile push.
type Priority string

const (
	PriorityCritical Priority = "critical"
	PriorityHigh     Priority = "high"
	PriorityMedium   Priority = "medium"
	PriorityLow      Priority = "low"
)

// Reason identifies the lifecycle condition requiring user attention.
type Reason string

const (
	ReasonCharterAmbiguity       Reason = "charter_ambiguity"
	ReasonScopeException         Reason = "scope_exception"
	ReasonSteeringScopeChange    Reason = "steering_scope_change"
	ReasonImplementationBlocked  Reason = "implementation_blocked"
	ReasonProviderOutcomeUnknown Reason = "provider_outcome_unknown"
	ReasonPublicationApproval    Reason = "publication_approval"
)

// Status is the durable attention lifecycle.
type Status string

const (
	StatusOpen     Status = "open"
	StatusResolved Status = "resolved"
	StatusArchived Status = "archived"
)

// Intent identifies the mutually exclusive development workflow.
type Intent string

const (
	IntentImplementFeature Intent = "implement_feature"
	IntentPickupPR         Intent = "pickup_pr"
)

// SourceKind identifies the single workspace intake source.
type SourceKind string

const (
	SourceIssue       SourceKind = "issue"
	SourceBrief       SourceKind = "brief"
	SourcePullRequest SourceKind = "pull_request"
)

// Target identifies a safe, application-owned destination inside a workspace.
// It deliberately does not hold an arbitrary URL.
type Target struct {
	Panel    string `json:"panel"`
	EntityID string `json:"entity_id,omitempty"`
}

// Notification is one durable attention item. SourceKey is stable for the
// underlying gate or blocker. Generation increases when changed evidence is
// allowed to reopen that item and notify the user again.
type Notification struct {
	ID           string     `json:"id"`
	SourceKey    string     `json:"source_key"`
	Generation   uint64     `json:"generation"`
	WorkspaceID  string     `json:"workspace_id"`
	Repository   string     `json:"repository"`
	Intent       Intent     `json:"intent"`
	SourceKind   SourceKind `json:"source_kind"`
	Phase        string     `json:"phase"`
	Reason       Reason     `json:"reason"`
	Priority     Priority   `json:"priority"`
	Status       Status     `json:"status"`
	Read         bool       `json:"read"`
	SnoozedUntil *time.Time `json:"snoozed_until,omitempty"`
	Title        string     `json:"title"`
	Summary      string     `json:"summary"`
	Target       Target     `json:"target"`
	Version      uint64     `json:"version"`
	CreatedAt    time.Time  `json:"created_at"`
	UpdatedAt    time.Time  `json:"updated_at"`
	ResolvedAt   *time.Time `json:"resolved_at,omitempty"`
	ArchivedAt   *time.Time `json:"archived_at,omitempty"`
}

// Draft contains lifecycle-produced notification data. User-owned state is
// omitted, so an idempotent upsert cannot accidentally overwrite read/snooze.
type Draft struct {
	ID          string
	SourceKey   string
	Generation  uint64
	WorkspaceID string
	Repository  string
	Intent      Intent
	SourceKind  SourceKind
	Phase       string
	Reason      Reason
	Priority    Priority
	Title       string
	Summary     string
	Target      Target
}

// UpsertResult distinguishes a retry from presentation change or generation.
type UpsertResult struct {
	Notification  Notification
	Created       bool
	Changed       bool
	NewGeneration bool
}

// DefaultPriority centralizes attention severity. Callers may explicitly
// override it in a Draft, but an empty priority always resolves through here.
func DefaultPriority(reason Reason) Priority {
	switch reason {
	case ReasonProviderOutcomeUnknown:
		return PriorityCritical
	case ReasonImplementationBlocked, ReasonPublicationApproval:
		return PriorityHigh
	case ReasonCharterAmbiguity, ReasonScopeException, ReasonSteeringScopeChange:
		return PriorityMedium
	default:
		return ""
	}
}

// Upsert applies one sourceKey+generation draft. Same-generation retries are
// no-ops unless presentation metadata changed. Only a higher generation may
// reopen a resolved or archived item.
func Upsert(current *Notification, draft Draft, now time.Time) (UpsertResult, error) {
	now, err := validMutationTime(now)
	if err != nil {
		return UpsertResult{}, err
	}
	draft, err = normalizeDraft(draft)
	if err != nil {
		return UpsertResult{}, err
	}
	if current == nil {
		n := notificationFromDraft(draft)
		n.Status = StatusOpen
		n.Version = 1
		n.CreatedAt = now
		n.UpdatedAt = now
		if err := n.Validate(); err != nil {
			return UpsertResult{}, err
		}
		return UpsertResult{Notification: n, Created: true, Changed: true, NewGeneration: true}, nil
	}

	n := cloneNotification(*current)
	if err := n.Validate(); err != nil {
		return UpsertResult{}, fmt.Errorf("%w: current record: %v", ErrInvalidNotification, err)
	}
	if draft.SourceKey != n.SourceKey || draft.WorkspaceID != n.WorkspaceID ||
		draft.Intent != n.Intent || draft.SourceKind != n.SourceKind || draft.Reason != n.Reason ||
		(draft.ID != "" && draft.ID != n.ID) {
		return UpsertResult{}, fmt.Errorf("%w: source identity cannot change", ErrInvalidNotification)
	}
	if draft.Generation < n.Generation {
		return UpsertResult{}, ErrStaleGeneration
	}
	if now.Before(n.UpdatedAt) {
		return UpsertResult{}, fmt.Errorf("%w: mutation time predates record", ErrInvalidNotification)
	}

	changed := applyDraftMetadata(&n, draft)
	newGeneration := draft.Generation > n.Generation
	if newGeneration {
		n.Generation = draft.Generation
		n.Status = StatusOpen
		n.Read = false
		n.SnoozedUntil = nil
		n.ResolvedAt = nil
		n.ArchivedAt = nil
		changed = true
	}
	if !changed {
		return UpsertResult{Notification: n}, nil
	}
	bump(&n, now)
	if err := n.Validate(); err != nil {
		return UpsertResult{}, err
	}
	return UpsertResult{Notification: n, Changed: true, NewGeneration: newGeneration}, nil
}

// MarkRead changes read state without altering lifecycle status.
func MarkRead(n Notification, read bool, now time.Time) (Notification, bool, error) {
	return mutate(n, now, func(next *Notification) (bool, error) {
		if next.Read == read {
			return false, nil
		}
		next.Read = read
		return true, nil
	})
}

// Snooze hides an open item until a future instant.
func Snooze(n Notification, until, now time.Time) (Notification, bool, error) {
	return mutate(n, now, func(next *Notification) (bool, error) {
		until = until.UTC()
		if next.Status != StatusOpen {
			return false, fmt.Errorf("%w: only open notifications may be snoozed", ErrInvalidTransition)
		}
		if until.IsZero() || !until.After(now) {
			return false, fmt.Errorf("%w: snooze deadline must be in the future", ErrInvalidTransition)
		}
		if next.SnoozedUntil != nil && next.SnoozedUntil.Equal(until) {
			return false, nil
		}
		next.SnoozedUntil = timePtr(until)
		return true, nil
	})
}

// ClearSnooze makes a previously snoozed item immediately visible again.
func ClearSnooze(n Notification, now time.Time) (Notification, bool, error) {
	return mutate(n, now, func(next *Notification) (bool, error) {
		if next.SnoozedUntil == nil {
			return false, nil
		}
		next.SnoozedUntil = nil
		return true, nil
	})
}

// Resolve is idempotent. Lifecycle reconciliation should call it as soon as
// the underlying gate or blocker stops requiring user action.
func Resolve(n Notification, now time.Time) (Notification, bool, error) {
	return mutate(n, now, func(next *Notification) (bool, error) {
		switch next.Status {
		case StatusResolved, StatusArchived:
			return false, nil
		case StatusOpen:
			next.Status = StatusResolved
			next.SnoozedUntil = nil
			next.ResolvedAt = timePtr(now)
			return true, nil
		default:
			return false, fmt.Errorf("%w: invalid status", ErrInvalidTransition)
		}
	})
}

// ReconcileActive automatically resolves a notification when its underlying
// condition is no longer active.
func ReconcileActive(n Notification, active bool, now time.Time) (Notification, bool, error) {
	if active {
		if err := n.Validate(); err != nil {
			return Notification{}, false, err
		}
		return cloneNotification(n), false, nil
	}
	return Resolve(n, now)
}

// Archive permits explicit early removal only after resolution.
func Archive(n Notification, now time.Time) (Notification, bool, error) {
	return mutate(n, now, func(next *Notification) (bool, error) {
		switch next.Status {
		case StatusArchived:
			return false, nil
		case StatusResolved:
			next.Status = StatusArchived
			next.ArchivedAt = timePtr(now)
			return true, nil
		default:
			return false, fmt.Errorf("%w: only resolved notifications may be archived", ErrInvalidTransition)
		}
	})
}

// RetentionDeadline returns when a terminal item may be physically removed.
// Open notifications have no deadline. Archived notifications are immediately
// eligible; resolved notifications receive the configured retention window.
func RetentionDeadline(n Notification, resolvedRetention time.Duration) (*time.Time, error) {
	if err := n.Validate(); err != nil {
		return nil, err
	}
	if resolvedRetention < 0 {
		return nil, fmt.Errorf("%w: retention cannot be negative", ErrInvalidNotification)
	}
	switch n.Status {
	case StatusOpen:
		return nil, nil
	case StatusResolved:
		deadline := n.ResolvedAt.Add(resolvedRetention)
		return &deadline, nil
	case StatusArchived:
		deadline := *n.ArchivedAt
		return &deadline, nil
	default:
		return nil, fmt.Errorf("%w: invalid status", ErrInvalidNotification)
	}
}

// EligibleForDeletion reports whether terminal retention has elapsed.
func EligibleForDeletion(n Notification, now time.Time, resolvedRetention time.Duration) (bool, error) {
	deadline, err := RetentionDeadline(n, resolvedRetention)
	if err != nil {
		return false, err
	}
	return deadline != nil && !now.UTC().Before(*deadline), nil
}

// IsSnoozed reports active snooze state at one explicit evaluation instant.
func (n Notification) IsSnoozed(now time.Time) bool {
	return n.Status == StatusOpen && n.SnoozedUntil != nil && n.SnoozedUntil.After(now.UTC())
}

// Validate checks persisted identity, bounds, intent, and lifecycle invariants.
func (n Notification) Validate() error {
	if !validIdentifier(n.ID, maxIDBytes) ||
		!validSingleLine(n.SourceKey, maxSourceKeyBytes) || strings.TrimSpace(n.SourceKey) != n.SourceKey ||
		!validIdentifier(n.WorkspaceID, maxWorkspaceBytes) ||
		!validSingleLine(n.Repository, maxRepositoryBytes) || strings.TrimSpace(n.Repository) != n.Repository ||
		!validIntent(n.Intent) || !validSourceKind(n.SourceKind) ||
		!validIdentifier(n.Phase, maxPhaseBytes) ||
		!validReason(n.Reason) || !validPriority(n.Priority) || !validStatus(n.Status) ||
		!validSingleLine(n.Title, maxTitleBytes) || strings.TrimSpace(n.Title) != n.Title ||
		!validOptional(n.Summary, maxSummaryBytes) || strings.TrimSpace(n.Summary) != n.Summary ||
		!validIdentifier(n.Target.Panel, maxPanelBytes) ||
		!validOptionalSingleLine(n.Target.EntityID, maxEntityIDBytes) ||
		strings.TrimSpace(n.Target.EntityID) != n.Target.EntityID ||
		n.Generation == 0 || n.Version == 0 || n.CreatedAt.IsZero() || n.UpdatedAt.IsZero() ||
		n.UpdatedAt.Before(n.CreatedAt) {
		return ErrInvalidNotification
	}
	if n.Intent == IntentPickupPR && n.SourceKind != SourcePullRequest {
		return fmt.Errorf("%w: pickup intent requires pull-request source", ErrInvalidNotification)
	}
	if n.Intent == IntentImplementFeature && n.SourceKind != SourceIssue && n.SourceKind != SourceBrief {
		return fmt.Errorf("%w: implementation intent requires issue or brief source", ErrInvalidNotification)
	}
	if !validOptionalTime(n.SnoozedUntil) || !validOptionalTime(n.ResolvedAt) || !validOptionalTime(n.ArchivedAt) {
		return ErrInvalidNotification
	}
	switch n.Status {
	case StatusOpen:
		if n.ResolvedAt != nil || n.ArchivedAt != nil ||
			(n.SnoozedUntil != nil && n.SnoozedUntil.Before(n.CreatedAt)) {
			return fmt.Errorf("%w: open item cannot be terminal", ErrInvalidNotification)
		}
	case StatusResolved:
		if n.SnoozedUntil != nil || n.ResolvedAt == nil || n.ArchivedAt != nil ||
			n.ResolvedAt.Before(n.CreatedAt) || n.ResolvedAt.After(n.UpdatedAt) {
			return fmt.Errorf("%w: invalid resolved state", ErrInvalidNotification)
		}
	case StatusArchived:
		if n.SnoozedUntil != nil || n.ResolvedAt == nil || n.ArchivedAt == nil ||
			n.ResolvedAt.Before(n.CreatedAt) || n.ArchivedAt.Before(*n.ResolvedAt) ||
			n.ArchivedAt.After(n.UpdatedAt) {
			return fmt.Errorf("%w: invalid archived state", ErrInvalidNotification)
		}
	}
	return nil
}

func normalizeDraft(d Draft) (Draft, error) {
	d.ID = strings.TrimSpace(d.ID)
	d.SourceKey = strings.TrimSpace(d.SourceKey)
	d.WorkspaceID = strings.TrimSpace(d.WorkspaceID)
	d.Repository = strings.TrimSpace(d.Repository)
	d.Phase = strings.TrimSpace(d.Phase)
	d.Title = strings.TrimSpace(d.Title)
	d.Summary = strings.TrimSpace(d.Summary)
	d.Target.Panel = strings.TrimSpace(d.Target.Panel)
	d.Target.EntityID = strings.TrimSpace(d.Target.EntityID)
	if d.Priority == "" {
		d.Priority = DefaultPriority(d.Reason)
	}
	probe := notificationFromDraft(d)
	probe.Status = StatusOpen
	probe.Version = 1
	probe.CreatedAt = time.Unix(1, 0).UTC()
	probe.UpdatedAt = probe.CreatedAt
	if err := probe.Validate(); err != nil {
		return Draft{}, err
	}
	return d, nil
}

func notificationFromDraft(d Draft) Notification {
	return Notification{
		ID: d.ID, SourceKey: d.SourceKey, Generation: d.Generation,
		WorkspaceID: d.WorkspaceID, Repository: d.Repository,
		Intent: d.Intent, SourceKind: d.SourceKind, Phase: d.Phase,
		Reason: d.Reason, Priority: d.Priority, Title: d.Title,
		Summary: d.Summary, Target: d.Target,
	}
}

func applyDraftMetadata(n *Notification, d Draft) bool {
	changed := n.Repository != d.Repository || n.Phase != d.Phase ||
		n.Priority != d.Priority || n.Title != d.Title || n.Summary != d.Summary || n.Target != d.Target
	n.Repository = d.Repository
	n.Phase = d.Phase
	n.Priority = d.Priority
	n.Title = d.Title
	n.Summary = d.Summary
	n.Target = d.Target
	return changed
}

func mutate(n Notification, now time.Time, apply func(*Notification) (bool, error)) (Notification, bool, error) {
	if err := n.Validate(); err != nil {
		return Notification{}, false, err
	}
	n = cloneNotification(n)
	now, err := validMutationTime(now)
	if err != nil {
		return Notification{}, false, err
	}
	if now.Before(n.UpdatedAt) {
		return Notification{}, false, fmt.Errorf("%w: mutation time predates record", ErrInvalidNotification)
	}
	changed, err := apply(&n)
	if err != nil || !changed {
		return n, changed, err
	}
	bump(&n, now)
	if err := n.Validate(); err != nil {
		return Notification{}, false, err
	}
	return n, true, nil
}

func bump(n *Notification, now time.Time) {
	n.Version++
	n.UpdatedAt = now.UTC()
}

func cloneNotification(n Notification) Notification {
	n.SnoozedUntil = cloneTime(n.SnoozedUntil)
	n.ResolvedAt = cloneTime(n.ResolvedAt)
	n.ArchivedAt = cloneTime(n.ArchivedAt)
	return n
}

func cloneTime(value *time.Time) *time.Time {
	if value == nil {
		return nil
	}
	cloned := value.UTC()
	return &cloned
}

func timePtr(value time.Time) *time.Time {
	value = value.UTC()
	return &value
}

func validMutationTime(value time.Time) (time.Time, error) {
	value = value.UTC()
	if value.IsZero() {
		return time.Time{}, fmt.Errorf("%w: mutation time is required", ErrInvalidNotification)
	}
	return value, nil
}

func validOptionalTime(value *time.Time) bool {
	return value == nil || !value.IsZero()
}

func validIdentifier(value string, maximum int) bool {
	return validBounded(value, maximum) && identifierPattern.MatchString(value)
}

func validBounded(value string, maximum int) bool {
	return value != "" && len(value) <= maximum && utf8.ValidString(value)
}

func validOptional(value string, maximum int) bool {
	return value == "" || validBounded(value, maximum)
}

func validSingleLine(value string, maximum int) bool {
	return validBounded(value, maximum) && strings.IndexFunc(value, unicode.IsControl) < 0
}

func validOptionalSingleLine(value string, maximum int) bool {
	return value == "" || validSingleLine(value, maximum)
}

func validPriority(value Priority) bool {
	switch value {
	case PriorityCritical, PriorityHigh, PriorityMedium, PriorityLow:
		return true
	default:
		return false
	}
}

func validReason(value Reason) bool {
	switch value {
	case ReasonCharterAmbiguity, ReasonScopeException, ReasonSteeringScopeChange,
		ReasonImplementationBlocked, ReasonProviderOutcomeUnknown, ReasonPublicationApproval:
		return true
	default:
		return false
	}
}

func validStatus(value Status) bool {
	return value == StatusOpen || value == StatusResolved || value == StatusArchived
}

func validIntent(value Intent) bool {
	return value == IntentImplementFeature || value == IntentPickupPR
}

func validSourceKind(value SourceKind) bool {
	return value == SourceIssue || value == SourceBrief || value == SourcePullRequest
}
