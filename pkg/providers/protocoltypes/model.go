package protocoltypes

import (
	"errors"
	"strings"
)

// ErrNoModelConfigured is returned before provider I/O when a model is absent.
var ErrNoModelConfigured = errors.New("no model configured")

// RequireModel trims and validates a concrete provider model identifier.
func RequireModel(model string) (string, error) {
	model = strings.TrimSpace(model)
	if model == "" {
		return "", ErrNoModelConfigured
	}
	return model, nil
}
