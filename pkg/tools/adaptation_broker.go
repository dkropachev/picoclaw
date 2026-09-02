package tools

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/sipeed/picoclaw/pkg/database"
	"github.com/sipeed/picoclaw/pkg/providers"
)

const (
	adaptationPageItems = 128
	adaptationPageBytes = 8 << 20
)

const (
	adaptationBrokerDomain  = "tool-adaptation"
	adaptationBrokerVersion = 1
)

type adaptationBrokerHandler struct {
	store *toolAdaptationStateStore
	mu    sync.Mutex
	err   error
}

// NewAdaptationBrokerHandler returns the broker-side typed adaptation adapter.
func NewAdaptationBrokerHandler(home string) *adaptationBrokerHandler {
	if !database.BrokerAuthorityHeld() && !database.ProviderTestAuthorityHeld() {
		return &adaptationBrokerHandler{err: database.NewError(
			database.CodeUnauthorized,
			"tool-adaptation broker handler requires database broker authority",
		)}
	}
	return &adaptationBrokerHandler{store: &toolAdaptationStateStore{
		pathOverride: filepath.Join(home, toolAdaptationDatabaseFilename),
		persistent:   true,
	}}
}

type adaptationObservationRequest struct {
	StoreID            database.StoreID           `json:"store_id"`
	Profile            ToolAdaptationProfile      `json:"profile"`
	VisibleToolSurface string                     `json:"visible_tool_surface"`
	ToolDefinitions    []providers.ToolDefinition `json:"tool_definitions"`
	Usage              *providers.UsageInfo       `json:"usage,omitempty"`
}

type adaptationProfileRequest struct {
	StoreID database.StoreID      `json:"store_id"`
	Profile ToolAdaptationProfile `json:"profile"`
}

type adaptationOutcomeRequest struct {
	StoreID            database.StoreID      `json:"store_id"`
	Profile            ToolAdaptationProfile `json:"profile"`
	VisibleToolSurface string                `json:"visible_tool_surface"`
	ToolName           string                `json:"tool_name"`
	Success            bool                  `json:"success"`
	ErrorSummary       string                `json:"error_summary,omitempty"`
	DurationNanosecond int64                 `json:"duration_nanosecond"`
}

type adaptationObservationResponse struct {
	Observation ToolAdaptationObservation `json:"observation"`
	Found       bool                      `json:"found"`
}

type adaptationOutcomeResponse struct {
	Outcome ToolAdaptationToolOutcome `json:"outcome"`
	Found   bool                      `json:"found"`
}

type adaptationOutcomesPageRequest struct {
	StoreID  database.StoreID      `json:"store_id"`
	Profile  ToolAdaptationProfile `json:"profile"`
	Offset   int                   `json:"offset"`
	Revision string                `json:"revision,omitempty"`
}

type adaptationOutcomesResponse struct {
	Outcomes []ToolAdaptationToolOutcome `json:"outcomes"`
	Next     int                         `json:"next"`
	Revision string                      `json:"revision"`
	Done     bool                        `json:"done"`
}

func (handler *adaptationBrokerHandler) Handle(
	_ context.Context,
	request database.Request,
) (any, error) {
	if handler != nil && handler.err != nil {
		return nil, handler.err
	}
	if handler == nil || handler.store == nil ||
		request.Domain != adaptationBrokerDomain || request.Version != adaptationBrokerVersion {
		return nil, database.NewError(database.CodeUnsupported, "database domain is unsupported")
	}
	handler.mu.Lock()
	defer handler.mu.Unlock()
	handler.store.clearError()
	result := func(value any, mutation bool) (any, error) {
		if err := handler.store.consumeError(); err != nil {
			return nil, mapAdaptationBrokerError(err, mutation)
		}
		return value, nil
	}
	switch request.Operation {
	case "observe-cache":
		var input adaptationObservationRequest
		if err := request.DecodePayload(&input); err != nil || input.StoreID != ToolAdaptationStoreID {
			return nil, database.NewError(database.CodeInvalid, "tool adaptation request is invalid")
		}
		observation, found := handler.store.observe(
			input.Profile, input.VisibleToolSurface, input.ToolDefinitions, input.Usage,
		)
		return result(adaptationObservationResponse{Observation: observation, Found: found}, true)
	case "latest-observation":
		var input adaptationProfileRequest
		if err := request.DecodePayload(&input); err != nil || input.StoreID != ToolAdaptationStoreID {
			return nil, database.NewError(database.CodeInvalid, "tool adaptation request is invalid")
		}
		observation, found := handler.store.latest(input.Profile)
		return result(adaptationObservationResponse{Observation: observation, Found: found}, false)
	case "observe-outcome":
		var input adaptationOutcomeRequest
		if err := request.DecodePayload(&input); err != nil || input.StoreID != ToolAdaptationStoreID {
			return nil, database.NewError(database.CodeInvalid, "tool adaptation request is invalid")
		}
		outcome, found := handler.store.observeToolOutcome(
			input.Profile,
			input.VisibleToolSurface,
			input.ToolName,
			input.Success,
			input.ErrorSummary,
			time.Duration(input.DurationNanosecond),
		)
		return result(adaptationOutcomeResponse{Outcome: outcome, Found: found}, true)
	case "latest-outcomes-page":
		var input adaptationOutcomesPageRequest
		if err := request.DecodePayload(&input); err != nil || input.StoreID != ToolAdaptationStoreID ||
			input.Offset < 0 || input.Offset > maximumAdaptationOutcomes ||
			!validAdaptationRevision(input.Revision) {
			return nil, database.NewError(database.CodeInvalid, "tool adaptation request is invalid")
		}
		outcomes := handler.store.latestToolOutcomes(input.Profile)
		if input.Offset > len(outcomes) {
			return nil, database.NewError(database.CodeInvalid, "tool adaptation cursor is invalid")
		}
		revision, err := adaptationOutcomesRevision(outcomes)
		if err != nil {
			return nil, database.NewError(database.CodeIntegrity, "tool adaptation revision is invalid")
		}
		if input.Revision != "" && input.Revision != revision {
			return nil, database.NewError(database.CodeConflict, "tool adaptation outcomes changed")
		}
		response := adaptationOutcomesResponse{
			Outcomes: make([]ToolAdaptationToolOutcome, 0, min(adaptationPageItems, len(outcomes)-input.Offset)),
			Next:     input.Offset, Revision: revision,
		}
		pageBytes := 0
		for response.Next < len(outcomes) && len(response.Outcomes) < adaptationPageItems {
			outcome := outcomes[response.Next]
			raw, err := database.MarshalCanonical(outcome)
			if err != nil || len(raw) > adaptationPageBytes {
				return nil, database.NewError(database.CodeIntegrity, "tool adaptation outcome is too large")
			}
			if len(response.Outcomes) > 0 && pageBytes+len(raw) > adaptationPageBytes {
				break
			}
			response.Outcomes = append(response.Outcomes, outcome)
			response.Next++
			pageBytes += len(raw)
		}
		response.Done = response.Next == len(outcomes)
		return result(response, false)
	default:
		return nil, database.NewError(database.CodeUnsupported, "tool adaptation operation is unsupported")
	}
}

func mapAdaptationBrokerError(err error, mutation bool) error {
	if err == nil {
		return nil
	}
	var structured *database.Error
	if errors.As(err, &structured) {
		return database.NewError(structured.Code, structured.Message)
	}
	if mutation {
		return database.NewError(database.CodeOutcomeUnknown, "tool adaptation mutation outcome is unknown")
	}
	switch {
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return database.NewError(database.CodeDeadline, "tool adaptation request deadline was exceeded")
	case errors.Is(err, os.ErrPermission):
		return database.NewError(database.CodeUnavailable, "tool adaptation store is unavailable")
	default:
		return database.NewError(database.CodeInternal, "tool adaptation operation failed")
	}
}

func adaptationOutcomesRevision(outcomes []ToolAdaptationToolOutcome) (string, error) {
	digest := sha256.New()
	for _, outcome := range outcomes {
		raw, err := database.MarshalCanonical(outcome)
		if err != nil {
			return "", err
		}
		_, _ = digest.Write(raw)
	}
	return hex.EncodeToString(digest.Sum(nil)), nil
}

func validAdaptationRevision(value string) bool {
	if value == "" {
		return true
	}
	if len(value) != sha256.Size*2 || value != strings.ToLower(value) {
		return false
	}
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == sha256.Size
}

// Close drains the broker-owned pool during controlled supervisor shutdown.
func (handler *adaptationBrokerHandler) Close() error {
	if handler == nil {
		return nil
	}
	return handler.store.close()
}

// RunOfflineDatabaseMigration initializes or upgrades the trusted adaptation
// store while the migration command owns exclusive storage fencing.
func RunOfflineDatabaseMigration(ctx context.Context, home string) error {
	if !database.MigrationFenceHeld() {
		return database.NewError(
			database.CodeConflict,
			"tool adaptation migration requires the exclusive database fence",
		)
	}
	store := &toolAdaptationStateStore{
		pathOverride: filepath.Join(home, toolAdaptationDatabaseFilename),
	}
	store.mu.Lock()
	db, err := store.openLocked(ctx)
	if err == nil {
		err = db.Close()
	}
	store.mu.Unlock()
	return err
}

var _ database.Handler = (*adaptationBrokerHandler)(nil)

func adaptationBrokerCall(operation string, input, output any, mutation bool) error {
	client := database.RuntimeClient()
	if client == nil {
		return database.NewError(database.CodeUnavailable, "tool adaptation broker is unavailable")
	}
	if mutation {
		return client.CallWithOptions(
			context.Background(),
			adaptationBrokerDomain,
			adaptationBrokerVersion,
			operation,
			input,
			output,
			database.CallOptions{Mutation: true},
		)
	}
	return client.Call(
		context.Background(), adaptationBrokerDomain, adaptationBrokerVersion, operation, input, output,
	)
}
