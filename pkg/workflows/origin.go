package workflows

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
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
		if parentRunID != "" || retryOfRunID != "" {
			return nil, fmt.Errorf(
				"%w: descendant is missing root run id",
				ErrInvalidRunOrigin,
			)
		}
		normalized.RootRunID = runID
	}
	if validationErr := validateNormalizedRunOrigin(
		normalized,
		runID,
		parentRunID,
		retryOfRunID,
		event,
		inputs,
	); validationErr != nil {
		return nil, validationErr
	}
	return normalized, nil
}

func trustedRunOrigin(run *Run) (*RunOrigin, bool) {
	if run == nil || run.Origin == nil {
		return nil, false
	}
	origin := cloneRunOrigin(run.Origin)
	if validateErr := validateNormalizedRunOrigin(
		origin,
		run.ID,
		run.ParentRunID,
		run.RetryOfRunID,
		run.Event,
		run.Inputs,
	); validateErr != nil {
		return nil, false
	}
	return origin, true
}

func validateNormalizedRunOrigin(
	origin *RunOrigin,
	runID string,
	parentRunID string,
	retryOfRunID string,
	event map[string]any,
	inputs map[string]any,
) error {
	if !validRunOriginFields(origin) {
		return ErrInvalidRunOrigin
	}
	if !validOptionalRunOriginLink(parentRunID) ||
		!validOptionalRunOriginLink(retryOfRunID) {
		return fmt.Errorf("%w: invalid ancestry link", ErrInvalidRunOrigin)
	}
	if parentRunID == "" &&
		retryOfRunID == "" &&
		origin.RootRunID != runID {
		return fmt.Errorf("%w: root run id does not match initial run", ErrInvalidRunOrigin)
	}
	if !isExternalEventContext(event) || event["id"] != origin.EventID {
		return fmt.Errorf("%w: event context does not match event id", ErrInvalidRunOrigin)
	}
	if parentRunID != "" {
		return nil
	}
	inputEventID, _ := inputs["event_id"].(string)
	if inputEventID != origin.EventID {
		return fmt.Errorf("%w: top-level input event id does not match", ErrInvalidRunOrigin)
	}
	switch origin.Kind {
	case RunOriginExternalEvent:
		inputDispatchID, _ := inputs["dispatch_id"].(string)
		if inputDispatchID != origin.DispatchID {
			return fmt.Errorf(
				"%w: top-level input dispatch id does not match",
				ErrInvalidRunOrigin,
			)
		}
	case RunOriginExternalEventDraftTest:
		if _, present := inputs["dispatch_id"]; present {
			return fmt.Errorf(
				"%w: draft test cannot contain a dispatch id",
				ErrInvalidRunOrigin,
			)
		}
	}
	return nil
}

func validOptionalRunOriginLink(runID string) bool {
	return runID == "" || validRunOriginRunID(runID)
}

type runOriginLookup func(context.Context, string) (*Run, error)

type runOriginTrustResult struct {
	origin  *RunOrigin
	trusted bool
}

func trustedRunOriginWithStore(
	ctx context.Context,
	store RunStore,
	run *Run,
) (*RunOrigin, bool) {
	if store == nil {
		return trustedRunOrigin(run)
	}
	return trustedRunOriginWithLookup(ctx, run, store.GetRun)
}

func trustedRunOriginWithLookup(
	ctx context.Context,
	run *Run,
	lookup runOriginLookup,
) (*RunOrigin, bool) {
	origin, trusted := trustedRunOrigin(run)
	if !trusted || lookup == nil {
		return origin, trusted
	}

	const (
		runOriginLineageVisiting = iota + 1
		runOriginLineageValidated
	)
	type lineageFrame struct {
		run         *Run
		ancestorIDs []string
		next        int
	}
	validateRun := func(current *Run) bool {
		if current == nil ||
			!validRunOriginRunID(current.ID) ||
			!sameRunOrigin(current.Origin, origin) {
			return false
		}
		currentOrigin, currentTrusted := trustedRunOrigin(current)
		return currentTrusted && sameRunOrigin(currentOrigin, origin)
	}
	if !validateRun(run) {
		return nil, false
	}

	// Use an iterative depth-first walk so retained lineages are not rejected
	// at an arbitrary depth and malicious cycles cannot grow the call stack.
	// A pruned ancestor is an independent-retention boundary. Every ancestor
	// still available through the store remains part of the validation graph.
	states := map[string]int{run.ID: runOriginLineageVisiting}
	stack := []lineageFrame{{
		run:         run,
		ancestorIDs: runOriginAncestorIDs(run),
	}}
	for len(stack) != 0 {
		frame := &stack[len(stack)-1]
		if frame.next >= len(frame.ancestorIDs) {
			states[frame.run.ID] = runOriginLineageValidated
			stack = stack[:len(stack)-1]
			continue
		}

		ancestorID := frame.ancestorIDs[frame.next]
		frame.next++
		if !validRunOriginRunID(ancestorID) || ancestorID == frame.run.ID {
			return nil, false
		}
		switch states[ancestorID] {
		case runOriginLineageVisiting:
			return nil, false
		case runOriginLineageValidated:
			continue
		}
		if err := ctx.Err(); err != nil {
			return nil, false
		}
		ancestor, err := lookup(ctx, ancestorID)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil || ancestor == nil || ancestor.ID != ancestorID {
			return nil, false
		}
		if !validateRun(ancestor) {
			return nil, false
		}
		states[ancestor.ID] = runOriginLineageVisiting
		stack = append(stack, lineageFrame{
			run:         ancestor,
			ancestorIDs: runOriginAncestorIDs(ancestor),
		})
	}
	return origin, true
}

// trustedRunOriginsWithLookup validates a batch of retained run lineages with
// shared state. Each readable run and ancestry edge is evaluated at most once,
// and cancellation discards partial trust decisions so callers fail closed.
func trustedRunOriginsWithLookup(
	ctx context.Context,
	runs []*Run,
	lookup runOriginLookup,
) map[string]*RunOrigin {
	const (
		runOriginBatchVisiting = iota + 1
		runOriginBatchValidated
	)
	type lineageFrame struct {
		run         *Run
		origin      *RunOrigin
		ancestorIDs []string
		next        int
		trusted     bool
	}

	trustedOrigins := make(map[string]*RunOrigin, len(runs))
	if ctx == nil || ctx.Err() != nil {
		return trustedOrigins
	}
	states := make(map[string]int, len(runs))
	results := make(map[string]runOriginTrustResult, len(runs))
	newFrame := func(run *Run) (lineageFrame, bool) {
		if run == nil || !validRunOriginRunID(run.ID) {
			if run != nil {
				states[run.ID] = runOriginBatchValidated
				results[run.ID] = runOriginTrustResult{}
			}
			return lineageFrame{}, false
		}
		origin, trusted := trustedRunOrigin(run)
		if !trusted {
			states[run.ID] = runOriginBatchValidated
			results[run.ID] = runOriginTrustResult{}
			return lineageFrame{}, false
		}
		states[run.ID] = runOriginBatchVisiting
		return lineageFrame{
			run:         run,
			origin:      origin,
			ancestorIDs: runOriginAncestorIDs(run),
			trusted:     true,
		}, true
	}
	finalize := func(frame lineageFrame) runOriginTrustResult {
		result := runOriginTrustResult{trusted: frame.trusted}
		if frame.trusted {
			result.origin = cloneRunOrigin(frame.origin)
		}
		states[frame.run.ID] = runOriginBatchValidated
		results[frame.run.ID] = result
		return result
	}

	for _, run := range runs {
		if ctx.Err() != nil {
			return map[string]*RunOrigin{}
		}
		if run == nil || states[run.ID] == runOriginBatchValidated {
			continue
		}
		frame, ok := newFrame(run)
		if !ok {
			continue
		}
		stack := []lineageFrame{frame}
		for len(stack) != 0 {
			if ctx.Err() != nil {
				return map[string]*RunOrigin{}
			}
			current := &stack[len(stack)-1]
			if !current.trusted ||
				current.next >= len(current.ancestorIDs) {
				result := finalize(*current)
				stack = stack[:len(stack)-1]
				if len(stack) != 0 &&
					(!result.trusted ||
						!sameRunOrigin(
							result.origin,
							stack[len(stack)-1].origin,
						)) {
					stack[len(stack)-1].trusted = false
				}
				continue
			}

			ancestorID := current.ancestorIDs[current.next]
			current.next++
			if !validRunOriginRunID(ancestorID) ||
				ancestorID == current.run.ID {
				current.trusted = false
				continue
			}
			switch states[ancestorID] {
			case runOriginBatchVisiting:
				// Every frame on the active path depends on this cycle.
				for index := range stack {
					stack[index].trusted = false
				}
				continue
			case runOriginBatchValidated:
				result := results[ancestorID]
				if !result.trusted ||
					!sameRunOrigin(result.origin, current.origin) {
					current.trusted = false
				}
				continue
			}

			if lookup == nil {
				continue
			}
			ancestor, err := lookup(ctx, ancestorID)
			if errors.Is(err, fs.ErrNotExist) {
				continue
			}
			if err != nil || ancestor == nil || ancestor.ID != ancestorID {
				current.trusted = false
				continue
			}
			ancestorFrame, valid := newFrame(ancestor)
			if !valid {
				current.trusted = false
				continue
			}
			stack = append(stack, ancestorFrame)
		}
	}
	for _, run := range runs {
		if run == nil {
			continue
		}
		result := results[run.ID]
		if result.trusted {
			trustedOrigins[run.ID] = cloneRunOrigin(result.origin)
		}
	}
	return trustedOrigins
}

func runOriginAncestorIDs(run *Run) []string {
	if run == nil {
		return nil
	}
	ancestors := make([]string, 0, 2)
	parentRunID := run.ParentRunID
	if parentRunID != "" {
		ancestors = append(ancestors, parentRunID)
	}
	retryOfRunID := run.RetryOfRunID
	if retryOfRunID != "" && retryOfRunID != parentRunID {
		ancestors = append(ancestors, retryOfRunID)
	}
	return ancestors
}

func sameRunOrigin(left, right *RunOrigin) bool {
	return left != nil &&
		right != nil &&
		left.Kind == right.Kind &&
		left.EventID == right.EventID &&
		left.DispatchID == right.DispatchID &&
		left.RootRunID == right.RootRunID
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
