package tools

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/sipeed/picoclaw/internal/sqlitestore"
	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/database"
	"github.com/sipeed/picoclaw/pkg/providers"
)

func TestAdaptationBrokerHandlerOperationMatrix(t *testing.T) {
	handler := NewAdaptationBrokerHandler(t.TempDir())
	t.Cleanup(func() { _ = handler.Close() })
	call := func(operation string, input any) (any, error) {
		t.Helper()
		payload, err := database.MarshalCanonical(input)
		if err != nil {
			t.Fatal(err)
		}
		return handler.Handle(t.Context(), database.Request{
			Domain: adaptationBrokerDomain, Version: adaptationBrokerVersion,
			Operation: operation, Payload: payload,
		})
	}
	profile := ToolAdaptationProfile{Provider: "openai", Model: "gpt-test"}
	if value, err := call("preflight", adaptationProfileRequest{
		StoreID: ToolAdaptationStoreID, Profile: profile,
	}); err != nil || value.(adaptationObservationResponse).Found {
		t.Fatalf("preflight = %#v, %v", value, err)
	}
	value, err := call("observe-cache", adaptationObservationRequest{
		StoreID: ToolAdaptationStoreID, Profile: profile,
		VisibleToolSurface: config.ToolSurfacePicoClaw,
		ToolDefinitions: []providers.ToolDefinition{{
			Type: "function", Function: providers.ToolFunctionDefinition{Name: "shell", Description: "run"},
		}},
		Usage: &providers.UsageInfo{PromptTokens: minCacheSniffPromptTokens, CachedTokens: 64},
	})
	if err != nil || !value.(adaptationObservationResponse).Found {
		t.Fatalf("observe-cache = %#v, %v", value, err)
	}
	if value, err = call("latest-observation", adaptationProfileRequest{
		StoreID: ToolAdaptationStoreID, Profile: profile,
	}); err != nil || !value.(adaptationObservationResponse).Found {
		t.Fatalf("latest-observation = %#v, %v", value, err)
	}
	value, err = call("observe-outcome", adaptationOutcomeRequest{
		StoreID: ToolAdaptationStoreID, Profile: profile,
		VisibleToolSurface: config.ToolSurfacePicoClaw, ToolName: "shell",
		Success: false, ErrorSummary: "failed", DurationNanosecond: int64(1500 * time.Millisecond),
	})
	if err != nil || !value.(adaptationOutcomeResponse).Found ||
		value.(adaptationOutcomeResponse).Outcome.Failures != 1 {
		t.Fatalf("observe-outcome = %#v, %v", value, err)
	}
	value, err = call("latest-outcomes-page", adaptationOutcomesPageRequest{
		StoreID: ToolAdaptationStoreID, Profile: profile,
	})
	if err != nil {
		t.Fatal(err)
	}
	page := value.(adaptationOutcomesResponse)
	if !page.Done || page.Next != 1 || len(page.Outcomes) != 1 || page.Revision == "" {
		t.Fatalf("outcomes page = %#v", page)
	}
	if _, err := call("latest-outcomes-page", adaptationOutcomesPageRequest{
		StoreID: ToolAdaptationStoreID, Profile: profile, Offset: 2, Revision: page.Revision,
	}); database.CodeOf(err) != database.CodeInvalid {
		t.Fatalf("invalid cursor error = %v", err)
	}
	if _, err := call("latest-outcomes-page", adaptationOutcomesPageRequest{
		StoreID: ToolAdaptationStoreID, Profile: profile,
		Revision: strings.Repeat("0", 64),
	}); database.CodeOf(err) != database.CodeConflict {
		t.Fatalf("stale revision error = %v", err)
	}

	invalid := []struct {
		operation string
		input     any
	}{
		{"preflight", adaptationProfileRequest{StoreID: "workspace.bad"}},
		{"observe-cache", adaptationObservationRequest{StoreID: "workspace.bad"}},
		{"latest-observation", adaptationProfileRequest{StoreID: "workspace.bad"}},
		{"observe-outcome", adaptationOutcomeRequest{StoreID: "workspace.bad"}},
		{"latest-outcomes-page", adaptationOutcomesPageRequest{StoreID: ToolAdaptationStoreID, Offset: -1}},
		{"latest-outcomes-page", adaptationOutcomesPageRequest{StoreID: ToolAdaptationStoreID, Revision: "BAD"}},
	}
	for _, test := range invalid {
		if _, err := call(test.operation, test.input); database.CodeOf(err) != database.CodeInvalid {
			t.Errorf("%s invalid error = %v", test.operation, err)
		}
	}
	if _, err := call("unknown", adaptationProfileRequest{StoreID: ToolAdaptationStoreID}); database.CodeOf(
		err,
	) != database.CodeUnsupported {
		t.Fatalf("unknown operation error = %v", err)
	}
	if _, err := (*adaptationBrokerHandler)(nil).Handle(t.Context(), database.Request{}); database.CodeOf(
		err,
	) != database.CodeUnsupported {
		t.Fatalf("nil handler error = %v", err)
	}
}

func TestAdaptationBrokerCodecAndErrorMatrix(t *testing.T) {
	outcomes := []ToolAdaptationToolOutcome{{
		Profile:            ToolAdaptationProfile{Provider: "openai", Model: "model"},
		VisibleToolSurface: "pico", ToolName: "shell", Successes: 1,
	}}
	first, err := adaptationOutcomesRevision(outcomes)
	if err != nil || len(first) != 64 {
		t.Fatalf("revision = %q, %v", first, err)
	}
	outcomes[0].Successes++
	second, err := adaptationOutcomesRevision(outcomes)
	if err != nil || first == second {
		t.Fatalf("changed revision = %q, %v", second, err)
	}
	for _, value := range []string{"", first} {
		if !validAdaptationRevision(value) {
			t.Errorf("valid revision rejected: %q", value)
		}
	}
	for _, value := range []string{"BAD", strings.ToUpper(first), strings.Repeat("z", 64)} {
		if validAdaptationRevision(value) {
			t.Errorf("invalid revision accepted: %q", value)
		}
	}
	cases := []struct {
		err      error
		mutation bool
		code     database.ErrorCode
	}{
		{database.NewError(database.CodeConflict, "conflict"), false, database.CodeConflict},
		{errors.New("write failed"), true, database.CodeOutcomeUnknown},
		{context.Canceled, false, database.CodeDeadline},
		{sqlitestore.ErrTooNew, false, database.CodeUnsupported},
		{sqlitestore.ErrInvalidSchema, false, database.CodeIntegrity},
		{sqlitestore.ErrIntegrity, false, database.CodeIntegrity},
		{os.ErrPermission, false, database.CodeUnavailable},
		{errors.New("boom"), false, database.CodeInternal},
	}
	if mapAdaptationBrokerError(nil, false) != nil {
		t.Fatal("nil adaptation error mapped non-nil")
	}
	for _, test := range cases {
		if got := database.CodeOf(mapAdaptationBrokerError(test.err, test.mutation)); got != test.code {
			t.Errorf("map(%v, %v) = %s, want %s", test.err, test.mutation, got, test.code)
		}
	}
	if database.CodeOf(RunOfflineDatabaseMigration(t.Context(), t.TempDir())) != database.CodeConflict {
		t.Fatal("unfenced adaptation migration accepted")
	}
	if err := (*adaptationBrokerHandler)(nil).Close(); err != nil {
		t.Fatalf("nil close error = %v", err)
	}
	database.InstallProcessClient(nil)
	if database.CodeOf(adaptationBrokerCall("preflight", nil, nil, false)) != database.CodeUnavailable {
		t.Fatal("missing adaptation broker accepted")
	}
}
