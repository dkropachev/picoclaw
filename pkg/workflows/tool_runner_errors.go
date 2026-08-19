package workflows

import "errors"

// ErrToolCallNotDispatched marks deterministic failures that happened before
// a tool implementation was invoked. Write adapters may safely retry these
// failures without external-state reconciliation.
var ErrToolCallNotDispatched = errors.New("tool call was not dispatched")
