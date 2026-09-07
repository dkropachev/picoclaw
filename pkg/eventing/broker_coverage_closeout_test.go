//go:build !mipsle && !netbsd && !(freebsd && arm)

//nolint:govet // Independent broker assertions intentionally reuse err.
package eventing

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sipeed/picoclaw/pkg/database"
	developmentnotifications "github.com/sipeed/picoclaw/pkg/developmentnotifications"
)

func TestEventingBrokerErrorCodecMatrix(t *testing.T) {
	cases := []struct {
		err  error
		code database.ErrorCode
		text string
	}{
		{context.Canceled, database.CodeDeadline, ""},
		{ErrSchemaTooNew, database.CodeUnsupported, ""},
		{ErrSchemaInvalid, database.CodeIntegrity, ""},
		{ErrNotFound, database.CodeNotFound, "eventing_not_found"},
		{ErrStaleLease, database.CodeConflict, "eventing_stale_lease"},
		{ErrInvalidTransition, database.CodeConflict, "eventing_invalid_transition"},
		{ErrRunIDMismatch, database.CodeConflict, "eventing_run_id_mismatch"},
		{ErrPayloadTooLarge, database.CodeInvalid, "eventing_payload_too_large"},
		{ErrInvalidEnvelope, database.CodeInvalid, "eventing_invalid_envelope"},
		{ErrInvalidPRWorkspace, database.CodeInvalid, "eventing_invalid_pr_workspace"},
		{ErrPRWorkspaceConflict, database.CodeConflict, "eventing_pr_workspace_conflict"},
		{developmentnotifications.ErrInvalidNotification, database.CodeInvalid, "eventing_invalid_notification"},
		{developmentnotifications.ErrInvalidTransition, database.CodeConflict, "eventing_notification_transition"},
		{developmentnotifications.ErrStaleGeneration, database.CodeConflict, "eventing_notification_stale"},
		{developmentnotifications.ErrInvalidSavedView, database.CodeInvalid, "eventing_invalid_saved_view"},
		{developmentnotifications.ErrStaleViewVersion, database.CodeConflict, "eventing_saved_view_stale"},
		{ErrClosed, database.CodeUnavailable, "eventing_closed"},
		{errors.New("boom"), database.CodeInternal, ""},
	}
	if mapEventingBrokerError(nil) != nil || decodeEventingBrokerError(nil) != nil {
		t.Fatal("nil eventing error mapped non-nil")
	}
	for _, test := range cases {
		mapped := mapEventingBrokerError(test.err)
		if got := database.CodeOf(mapped); got != test.code {
			t.Errorf("map(%v) = %s, want %s", test.err, got, test.code)
		}
		if test.text == "" {
			continue
		}
		decoded := decodeEventingBrokerError(mapped)
		if !errors.Is(decoded, test.err) {
			t.Errorf("decode(%v) = %v", mapped, decoded)
		}
	}
	structured := database.NewError(database.CodeConflict, "structured")
	if mapped := mapEventingBrokerError(structured); mapped == structured ||
		database.CodeOf(mapped) != database.CodeConflict {
		t.Fatalf("structured map = %#v", mapped)
	}
	outcomeUnknown := database.NewError(database.CodeOutcomeUnknown, "unknown")
	if decodeEventingBrokerError(outcomeUnknown) != outcomeUnknown {
		t.Fatal("OutcomeUnknown was decoded")
	}
	plain := errors.New("plain")
	if decodeEventingBrokerError(plain) != plain {
		t.Fatal("plain error was replaced")
	}
	deadline := database.NewError(database.CodeDeadline, "other")
	if !errors.Is(decodeEventingBrokerError(deadline), context.DeadlineExceeded) {
		t.Fatal("deadline did not decode")
	}
}

func TestEventingBrokerDispatchAndRequestMatrix(t *testing.T) {
	fixture := newEventingBrokerFixture(t)
	store := fixture.store
	if _, err := store.Insert(t.Context(), testEnvelope("coverage-dispatch")); err != nil {
		t.Fatal(err)
	}
	operations := []string{
		eventingOpInsert, eventingOpGet, eventingOpList, eventingOpGetEventMetadata,
		eventingOpGetEventPayload, eventingOpListEventMetadata, eventingOpClaimRouting,
		eventingOpRenewRouting, eventingOpAckRouting, eventingOpNackRouting, eventingOpDeadRouting,
		eventingOpCreateDispatchClaim, eventingOpCreateRevisionedDispatchClaim, eventingOpCreateDispatch,
		eventingOpGetDispatch, eventingOpGetDispatchMetadata, eventingOpClaimDispatches,
		eventingOpLinkDispatchRun, eventingOpRenewDispatch, eventingOpFinishDispatch,
		eventingOpNackDispatch, eventingOpListDispatches, eventingOpListDispatchMetadata,
		eventingOpReplay, eventingOpPrune, eventingOpSetPRCutover, eventingOpGetPRCutover,
		eventingOpCreatePRWorkspace, eventingOpGetPRWorkspace, eventingOpListPRWorkspaces,
		eventingOpApplyPRMutation, eventingOpApplyPRPatch, eventingOpClaimPROperations,
		eventingOpFinishPROperation, eventingOpClaimPRPublications, eventingOpFinishPRPublication,
		eventingOpUpsertNotification, eventingOpListNotifications, eventingOpListPushNotifications,
		eventingOpGetNotification, eventingOpMutateNotification, eventingOpMutateNotifications,
		eventingOpGetNotificationViews, eventingOpPutNotificationViews, eventingOpGetPushState,
		eventingOpPutPushState, eventingOpPruneNotifications,
	}
	for _, operation := range operations {
		input := eventingBrokerRequest{Offset: -1, Before: time.Now().UTC(), Limit: 1}
		_, err := fixture.handler.dispatch(t.Context(), operation, input, fixture.handler.store)
		if database.CodeOf(err) == database.CodeUnsupported {
			t.Errorf("recognized dispatch %q returned Unsupported: %v", operation, err)
		}
	}
	_, unknownErr := fixture.handler.dispatch(
		t.Context(), "unknown", eventingBrokerRequest{}, fixture.handler.store,
	)
	if database.CodeOf(unknownErr) != database.CodeUnsupported {
		t.Fatalf("unknown dispatch error = %v", unknownErr)
	}
	values, more, err := fixture.handler.store.listDevelopmentNotificationsPage(t.Context(), -10, 0)
	if err != nil || more || len(values) != 0 {
		t.Fatalf("normalized empty notification page = %#v/%v/%v", values, more, err)
	}

	request := func(operation string, input any) database.Request {
		t.Helper()
		payload, err := database.MarshalCanonical(input)
		if err != nil {
			t.Fatal(err)
		}
		return database.Request{
			Domain: BrokerDomain, Version: BrokerVersion, Operation: operation, Payload: payload,
		}
	}
	if _, err := (*BrokerHandler)(nil).Handle(t.Context(), database.Request{}); database.CodeOf(
		err,
	) != database.CodeUnsupported {
		t.Fatalf("nil handler error = %v", err)
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := fixture.handler.Handle(canceled, request(eventingOpList, eventingBrokerRequest{
		StoreID: fixture.handler.storeID,
	})); database.CodeOf(err) != database.CodeDeadline {
		t.Fatalf("canceled request error = %v", err)
	}
	if _, err := fixture.handler.Handle(t.Context(), request(eventingOpResolveStore, eventingResolveRequest{
		WorkspaceSelector: "BAD",
	})); database.CodeOf(err) != database.CodeInvalid {
		t.Fatalf("invalid selector error = %v", err)
	}
	if _, err := fixture.handler.Handle(t.Context(), request(eventingOpResolveStore, eventingResolveRequest{
		WorkspaceSelector: strings.Repeat("0", 16),
	})); database.CodeOf(err) != database.CodeUnauthorized {
		t.Fatalf("unknown selector error = %v", err)
	}
	var selector string
	for value := range fixture.handler.selectors {
		selector = value
		break
	}
	resolved, err := fixture.handler.Handle(t.Context(), request(
		eventingOpResolveStore, eventingResolveRequest{WorkspaceSelector: selector},
	))
	if err != nil || resolved.(eventingResolveResponse).StoreID != fixture.handler.storeID {
		t.Fatalf("resolved store = %#v, %v", resolved, err)
	}
	if _, err := fixture.handler.Handle(t.Context(), request(
		BrokerPreflightOperation, eventingBrokerTarget{StoreID: "global/auth"},
	)); database.CodeOf(err) != database.CodeUnauthorized {
		t.Fatalf("foreign preflight error = %v", err)
	}
	if _, err := fixture.handler.Handle(t.Context(), request(
		"unknown", eventingBrokerRequest{StoreID: fixture.handler.storeID},
	)); database.CodeOf(err) != database.CodeUnsupported {
		t.Fatalf("unknown operation error = %v", err)
	}
}

func TestEventingBrokerSelectorAndLifecycleBoundaries(t *testing.T) {
	if _, err := NewBrokerHandler(t.TempDir(), nil); database.CodeOf(err) != database.CodeInvalid {
		t.Fatalf("nil config error = %v", err)
	}
	if _, err := (*eventingBrokerWorkspace)(nil).open(); database.CodeOf(err) != database.CodeUnavailable {
		t.Fatalf("nil workspace error = %v", err)
	}
	for _, value := range []string{"", "BAD", strings.Repeat("g", 16), strings.Repeat("0", 15)} {
		if validEventingSelector(value) {
			t.Errorf("invalid selector accepted: %q", value)
		}
	}
	if !validEventingSelector(strings.Repeat("a", 16)) {
		t.Fatal("valid selector rejected")
	}
	home := t.TempDir()
	configured, err := resolveConfiguredEventingWorkspace(home, "relative")
	if err != nil || !filepath.IsAbs(configured) {
		t.Fatalf("configured workspace = %q, %v", configured, err)
	}
	if _, err := resolveConfiguredEventingWorkspace(home, "bad\x00path"); database.CodeOf(err) != database.CodeInvalid {
		t.Fatalf("invalid configured workspace error = %v", err)
	}
	path := filepath.Join(home, "workspace", "eventing", "events.db")
	selector, err := eventingWorkspaceSelector(path)
	if err != nil || !validEventingSelector(selector) {
		t.Fatalf("path selector = %q, %v", selector, err)
	}
	workspaceSelector, err := eventingWorkspaceIDSelector(filepath.Join(home, "workspace"))
	if err != nil || !validEventingSelector(workspaceSelector) {
		t.Fatalf("workspace selector = %q, %v", workspaceSelector, err)
	}
	if _, err := canonicalEventingTargetPath("bad\x00path"); err == nil {
		t.Fatal("NUL target path accepted")
	}
	if _, err := canonicalEventingPathPrefix("bad\x00path"); err == nil {
		t.Fatal("NUL path prefix accepted")
	}
	if err := (*BrokerHandler)(nil).Close(); err != nil {
		t.Fatalf("nil close = %v", err)
	}
	if err := RunOfflineDatabaseMigration(t.Context(), home); database.CodeOf(err) != database.CodeConflict {
		t.Fatalf("unfenced migration error = %v", err)
	}
	if _, err := os.Stat(filepath.Join(home, "eventing", "events.db")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("unfenced migration touched store: %v", err)
	}
}
