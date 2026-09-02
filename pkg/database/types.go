package database

import (
	"fmt"
	"sort"
	"strings"
)

const maxStoreIDBytes = 128

// StoreID is an opaque logical catalog identity. It is never a filesystem path,
// URI, DSN, driver name, or database filename.
type StoreID string

// String returns the protocol-safe logical identity.
func (id StoreID) String() string { return string(id) }

// IsZero reports whether no logical identity is present.
func (id StoreID) IsZero() bool { return id == "" }

// ParseStoreID validates and returns one canonical logical store identity.
// Lowercase slash-separated segments allow catalog namespaces without allowing
// path traversal or platform path syntax.
func ParseStoreID(value string) (StoreID, error) {
	if value == "" || len(value) > maxStoreIDBytes || value != strings.TrimSpace(value) {
		return "", NewError(CodeInvalid, "store ID is invalid")
	}
	segments := strings.Split(value, "/")
	for _, segment := range segments {
		if !validStoreIDSegment(segment) {
			return "", NewError(CodeInvalid, "store ID is invalid")
		}
	}
	return StoreID(value), nil
}

func validStoreIDSegment(segment string) bool {
	if segment == "" || segment == "." || segment == ".." {
		return false
	}
	for index := 0; index < len(segment); index++ {
		character := segment[index]
		alphaNumeric := character >= 'a' && character <= 'z' || character >= '0' && character <= '9'
		if index == 0 {
			if !alphaNumeric {
				return false
			}
			continue
		}
		if !alphaNumeric && character != '-' && character != '_' && character != '.' {
			return false
		}
	}
	return true
}

// Valid reports whether id is a canonical logical identity.
func (id StoreID) Valid() bool {
	parsed, err := ParseStoreID(string(id))
	return err == nil && parsed == id
}

// StoreReadiness is the broker's readiness state for one logical store.
type StoreReadiness string

const (
	StoreReady             StoreReadiness = "ready"
	StoreMigrationRequired StoreReadiness = "migration_required"
	StoreIntegrityFailed   StoreReadiness = "integrity_failed"
	StoreUnavailable       StoreReadiness = "unavailable"
)

func (readiness StoreReadiness) Valid() bool {
	switch readiness {
	case StoreReady, StoreMigrationRequired, StoreIntegrityFailed, StoreUnavailable:
		return true
	default:
		return false
	}
}

// StoreStatus is a backend-neutral readiness projection for a catalog store.
type StoreStatus struct {
	ID        StoreID        `json:"id"`
	Readiness StoreReadiness `json:"readiness"`
	Error     *Error         `json:"error,omitempty"`
}

// ValidateStoreStatuses validates identities, readiness, structured errors,
// and duplicate catalog registrations. It returns a detached ID-sorted copy so
// status responses have deterministic ordering.
func ValidateStoreStatuses(statuses []StoreStatus) ([]StoreStatus, error) {
	detached := append(make([]StoreStatus, 0, len(statuses)), statuses...)
	seen := make(map[StoreID]struct{}, len(detached))
	for index := range detached {
		status := &detached[index]
		if !status.ID.Valid() {
			return nil, NewError(CodeInvalid, "store status contains an invalid store ID")
		}
		if _, duplicate := seen[status.ID]; duplicate {
			return nil, NewError(CodeIntegrity, "store catalog contains a duplicate logical ID")
		}
		seen[status.ID] = struct{}{}
		if !status.Readiness.Valid() {
			return nil, NewError(CodeInvalid, "store status contains an invalid readiness state")
		}
		if status.Readiness == StoreReady && status.Error != nil {
			return nil, NewError(CodeIntegrity, "ready store status cannot contain an error")
		}
		if status.Error != nil {
			if !status.Error.Code.Valid() || strings.TrimSpace(status.Error.Message) == "" {
				return nil, NewError(CodeIntegrity, "store status contains an invalid structured error")
			}
			status.Error = NewError(status.Error.Code, status.Error.Message)
		}
	}
	sort.Slice(detached, func(i, j int) bool { return detached[i].ID < detached[j].ID })
	return detached, nil
}

func (status StoreStatus) String() string {
	return fmt.Sprintf("%s:%s", status.ID, status.Readiness)
}

// RequireBrokerReady fails closed unless the broker status identifies every
// required logical store and reports it ready. The application never needs a
// physical catalog or configuration-derived store path to perform admission.
func RequireBrokerReady(status BrokerStatus) error {
	if len(status.RequiredStores) == 0 {
		return NewError(CodeUnavailable, "database broker required-store catalog is unavailable")
	}
	byID := make(map[StoreID]StoreStatus, len(status.Stores))
	for _, store := range status.Stores {
		byID[store.ID] = store
	}
	seen := make(map[StoreID]struct{}, len(status.RequiredStores))
	for _, id := range status.RequiredStores {
		if !id.Valid() {
			return NewError(CodeIntegrity, "database broker required-store catalog is invalid")
		}
		if _, duplicate := seen[id]; duplicate {
			return NewError(CodeIntegrity, "database broker required-store catalog contains a duplicate")
		}
		seen[id] = struct{}{}
		store, found := byID[id]
		if !found {
			return NewError(CodeUnavailable, "required database store has no readiness status")
		}
		if store.Readiness == StoreReady {
			continue
		}
		if store.Error != nil {
			return NewError(store.Error.Code, store.Error.Message)
		}
		return NewError(CodeUnavailable, "required database store is not ready")
	}
	return nil
}
