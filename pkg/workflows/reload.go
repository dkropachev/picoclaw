package workflows

import (
	"context"
	"time"
)

type ReloadResult struct {
	ReloadedAt time.Time     `json:"reloaded_at"`
	Workflows  []Definition  `json:"workflows"`
	Errors     []ReloadError `json:"errors"`
}

type ReloadError struct {
	Ref   string `json:"ref"`
	Error string `json:"error"`
}

func ReloadLocal(ctx context.Context, workspace string, opts ...LocalOption) (*ReloadResult, error) {
	defs, err := ListLocal(ctx, workspace, opts...)
	if err != nil {
		return nil, err
	}
	result := &ReloadResult{
		ReloadedAt: time.Now().UTC(),
		Workflows:  defs,
		Errors:     make([]ReloadError, 0),
	}
	for _, def := range defs {
		workflow, err := LoadLocal(ctx, workspace, def.Ref, opts...)
		if err != nil {
			result.Errors = append(result.Errors, ReloadError{Ref: def.Ref, Error: err.Error()})
			continue
		}
		if err := Validate(workflow); err != nil {
			result.Errors = append(result.Errors, ReloadError{Ref: def.Ref, Error: err.Error()})
		}
	}
	return result, nil
}
