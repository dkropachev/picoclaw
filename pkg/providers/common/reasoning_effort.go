package common

import (
	"fmt"
	"strings"
)

// NormalizeReasoningEffort resolves user-facing aliases to the request value
// accepted by OpenAI-style reasoning controls.
func NormalizeReasoningEffort(raw string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "", "default", "provider_default", "provider-default":
		return "", nil
	case "off":
		return "none", nil
	case "none", "minimal", "low", "medium", "high", "xhigh":
		return strings.ToLower(strings.TrimSpace(raw)), nil
	default:
		return "", fmt.Errorf(
			"unsupported reasoning_effort %q (supported: none, minimal, low, medium, high, xhigh)",
			raw,
		)
	}
}
