package workflows

import (
	"errors"
	"fmt"
	"strings"
)

const (
	// RunOriginExternalEvent identifies a run started by the durable external
	// event dispatcher.
	RunOriginExternalEvent = "external_event"
	// RunOriginExternalEventDraftTest identifies a draft test started from a
	// server-resolved durable external event.
	RunOriginExternalEventDraftTest = "external_event_draft_test"

	maxRunOriginRunIDBytes = 1024
)

var ErrInvalidRunOrigin = errors.New("invalid workflow run origin")

// RunOrigin is server-owned provenance for an external-event run family. It
// intentionally contains only durable identifiers that are safe to project to
// authenticated browser clients.
type RunOrigin struct {
	Kind       string `json:"kind"`
	EventID    string `json:"event_id"`
	DispatchID string `json:"dispatch_id,omitempty"`
	RootRunID  string `json:"root_run_id"`
}

func normalizeRunOrigin(
	origin *RunOrigin,
	runID string,
	parentRunID string,
	retryOfRunID string,
	event map[string]any,
	inputs map[string]any,
) (*RunOrigin, error) {
	if origin == nil {
		return nil, nil
	}
	normalized := cloneRunOrigin(origin)
	if normalized.RootRunID == "" {
		if strings.TrimSpace(parentRunID) != "" {
			return nil, fmt.Errorf("%w: reusable child is missing root run id", ErrInvalidRunOrigin)
		}
		normalized.RootRunID = runID
	}
	if !validRunOriginFields(normalized) {
		return nil, ErrInvalidRunOrigin
	}
	if strings.TrimSpace(parentRunID) == "" &&
		strings.TrimSpace(retryOfRunID) == "" &&
		normalized.RootRunID != runID {
		return nil, fmt.Errorf("%w: root run id does not match initial run", ErrInvalidRunOrigin)
	}
	if !isExternalEventContext(event) || event["id"] != normalized.EventID {
		return nil, fmt.Errorf("%w: event context does not match event id", ErrInvalidRunOrigin)
	}
	if strings.TrimSpace(parentRunID) == "" {
		inputEventID, _ := inputs["event_id"].(string)
		if inputEventID != normalized.EventID {
			return nil, fmt.Errorf("%w: top-level input event id does not match", ErrInvalidRunOrigin)
		}
		switch normalized.Kind {
		case RunOriginExternalEvent:
			inputDispatchID, _ := inputs["dispatch_id"].(string)
			if inputDispatchID != normalized.DispatchID {
				return nil, fmt.Errorf(
					"%w: top-level input dispatch id does not match",
					ErrInvalidRunOrigin,
				)
			}
		case RunOriginExternalEventDraftTest:
			if _, present := inputs["dispatch_id"]; present {
				return nil, fmt.Errorf(
					"%w: draft test cannot contain a dispatch id",
					ErrInvalidRunOrigin,
				)
			}
		}
	}
	return normalized, nil
}

func trustedRunOrigin(run *Run) (*RunOrigin, bool) {
	if run == nil || !validRunOriginFields(run.Origin) {
		return nil, false
	}
	if !isExternalEventContext(run.Event) || run.Event["id"] != run.Origin.EventID {
		return nil, false
	}
	return cloneRunOrigin(run.Origin), true
}

func validRunOriginFields(origin *RunOrigin) bool {
	if origin == nil ||
		!isExternalEventID(origin.EventID) ||
		!validRunOriginRunID(origin.RootRunID) {
		return false
	}
	switch origin.Kind {
	case RunOriginExternalEvent:
		return isExternalDispatchID(origin.DispatchID)
	case RunOriginExternalEventDraftTest:
		return origin.DispatchID == ""
	default:
		return false
	}
}

func validRunOriginRunID(id string) bool {
	if len(id) <= len("wr_") ||
		len(id) > maxRunOriginRunIDBytes ||
		!strings.HasPrefix(id, "wr_") {
		return false
	}
	for _, character := range id[len("wr_"):] {
		if (character < 'a' || character > 'z') &&
			(character < 'A' || character > 'Z') &&
			(character < '0' || character > '9') &&
			character != '_' &&
			character != '-' {
			return false
		}
	}
	return true
}

func cloneRunOrigin(origin *RunOrigin) *RunOrigin {
	if origin == nil {
		return nil
	}
	cloned := *origin
	return &cloned
}
