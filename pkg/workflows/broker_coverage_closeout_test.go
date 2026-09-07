//nolint:govet // Independent broker assertions intentionally reuse err.
package workflows

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sipeed/picoclaw/internal/sqlitestore"
	"github.com/sipeed/picoclaw/pkg/database"
)

func TestWorkflowBrokerErrorCodecMatrix(t *testing.T) {
	cases := []struct {
		err     error
		code    database.ErrorCode
		message string
		decoded error
	}{
		{context.Canceled, database.CodeDeadline, "", context.DeadlineExceeded},
		{os.ErrInvalid, database.CodeInvalid, workflowErrorInvalidRequest, os.ErrInvalid},
		{ErrRunAlreadyExists, database.CodeAlreadyExists, workflowErrorRunAlreadyExists, ErrRunAlreadyExists},
		{ErrRunConcurrencyLimit, database.CodeConflict, workflowErrorConcurrencyLimit, ErrRunConcurrencyLimit},
		{ErrRunVersionConflict, database.CodeConflict, workflowErrorVersionConflict, ErrRunVersionConflict},
		{ErrRunCanceled, database.CodeConflict, workflowErrorRunCanceled, ErrRunCanceled},
		{ErrInvalidCancelReason, database.CodeInvalid, workflowErrorInvalidCancelReason, ErrInvalidCancelReason},
		{ErrPrivateWorkflowContext, database.CodeUnauthorized, workflowErrorPrivateContext, ErrPrivateWorkflowContext},
		{ErrHumanTaskNotFound, database.CodeNotFound, workflowErrorHumanNotFound, ErrHumanTaskNotFound},
		{ErrHumanTaskConflict, database.CodeConflict, workflowErrorHumanConflict, ErrHumanTaskConflict},
		{ErrHumanTaskStale, database.CodeConflict, workflowErrorHumanStale, ErrHumanTaskStale},
		{ErrHumanTaskResponseInvalid, database.CodeInvalid, workflowErrorHumanInvalid, ErrHumanTaskResponseInvalid},
		{ErrHumanTaskUnsupported, database.CodeUnsupported, workflowErrorHumanUnsupported, ErrHumanTaskUnsupported},
		{os.ErrNotExist, database.CodeNotFound, workflowErrorRunNotFound, os.ErrNotExist},
		{sqlitestore.ErrTooNew, database.CodeUnsupported, "", nil},
		{sqlitestore.ErrInvalidSchema, database.CodeIntegrity, "", nil},
		{sqlitestore.ErrIntegrity, database.CodeIntegrity, "", nil},
		{ErrWorkflowStorageUnavailable, database.CodeUnavailable, "", nil},
		{errors.New("boom"), database.CodeInternal, "", nil},
	}
	if workflowRPCError(nil) != nil || decodeWorkflowRPCError(nil) != nil {
		t.Fatal("nil workflow error mapped non-nil")
	}
	for _, test := range cases {
		mapped := workflowRPCError(test.err)
		if got := database.CodeOf(mapped); got != test.code {
			t.Errorf("map(%v) = %s, want %s", test.err, got, test.code)
		}
		if test.message != "" {
			var structured *database.Error
			if !errors.As(mapped, &structured) || structured.Message != test.message {
				t.Errorf("map(%v) message = %#v", test.err, structured)
			}
		}
		if test.decoded != nil && !errors.Is(decodeWorkflowRPCError(mapped), test.decoded) {
			t.Errorf("decode(%v) = %v", mapped, decodeWorkflowRPCError(mapped))
		}
	}
	structured := database.NewError(database.CodeConflict, "structured")
	if mapped := workflowRPCError(structured); mapped == structured ||
		database.CodeOf(mapped) != database.CodeConflict {
		t.Fatalf("structured map = %#v", mapped)
	}
	outcomeUnknown := database.NewError(database.CodeOutcomeUnknown, "unknown")
	if decodeWorkflowRPCError(outcomeUnknown) != outcomeUnknown {
		t.Fatal("OutcomeUnknown was decoded")
	}
	plain := errors.New("plain")
	if decodeWorkflowRPCError(plain) != plain {
		t.Fatal("plain error was replaced")
	}
	for _, code := range []database.ErrorCode{database.CodeUnavailable, database.CodeInternal} {
		decoded := decodeWorkflowRPCError(database.NewError(code, "other"))
		if !errors.Is(decoded, ErrWorkflowStorageUnavailable) {
			t.Errorf("%s decoded error = %v", code, decoded)
		}
	}
}

func TestWorkflowBrokerWireClientAndRequestBoundaries(t *testing.T) {
	now := time.Now().UTC()
	run := &Run{
		ID: "wr_coverage_wire", WorkflowRef: "workflows/coverage.yml",
		Status: RunStatusRunning, CreatedAt: now, Inputs: map[string]any{"key": "value"},
	}
	run.storeVersion = 7
	wire, err := encodeWorkflowRunWire(run)
	if err != nil || len(wire.Document) == 0 || wire.StoreVersion != 7 {
		t.Fatalf("encoded wire = %#v, %v", wire, err)
	}
	decoded, err := decodeWorkflowRunWire(wire)
	if err != nil || decoded.ID != run.ID || decoded.storeVersion != 7 {
		t.Fatalf("decoded wire = %#v, %v", decoded, err)
	}
	if _, err := decodeWorkflowRunWire(workflowRunWire{}); err == nil {
		t.Fatal("empty workflow wire accepted")
	}
	if _, err := decodeWorkflowRunWire(workflowRunWire{Document: []byte(`{"id":`)}); err == nil {
		t.Fatal("invalid workflow wire accepted")
	}
	if (&FileRunStore{}).usesWorkflowBroker() || !(&FileRunStore{brokerErr: errors.New("x")}).usesWorkflowBroker() {
		t.Fatal("broker-use classification mismatch")
	}
	if _, err := (*FileRunStore)(nil).workflowBrokerClient(); database.CodeOf(err) != database.CodeUnavailable {
		t.Fatalf("nil broker client error = %v", err)
	}
	storedErr := errors.New("stored broker error")
	if _, err := (&FileRunStore{brokerErr: storedErr}).workflowBrokerClient(); !errors.Is(err, storedErr) {
		t.Fatalf("stored broker error = %v", err)
	}
	if _, err := (&FileRunStore{broker: &database.Client{}}).workflowBrokerClient(); database.CodeOf(
		err,
	) != database.CodeInvalid {
		t.Fatalf("invalid StoreID error = %v", err)
	}
	if err := (&FileRunStore{brokerErr: storedErr}).Preflight(nil); !errors.Is(err, storedErr) {
		t.Fatalf("stored preflight error = %v", err)
	}
	if _, err := resolveWorkflowBrokerStoreID(t.Context(), nil, t.TempDir()); database.CodeOf(
		err,
	) != database.CodeUnavailable {
		t.Fatalf("nil resolver client error = %v", err)
	}
	if _, err := resolveWorkflowBrokerStoreID(t.Context(), &database.Client{}, "bad\x00workspace"); database.CodeOf(
		err,
	) != database.CodeInvalid {
		t.Fatalf("invalid resolver workspace error = %v", err)
	}
}

func TestWorkflowBrokerHandlerOperationMatrix(t *testing.T) {
	fixture := newWorkflowBrokerFixture(t)
	handler := fixture.handler
	store := fixture.store
	if err := store.Preflight(t.Context()); err != nil {
		t.Fatal(err)
	}
	request := func(operation string, input any) database.Request {
		t.Helper()
		payload, err := database.MarshalCanonical(input)
		if err != nil {
			t.Fatal(err)
		}
		return database.Request{
			Domain: workflowRPCDomain, Version: workflowRPCVersion,
			Operation: operation, Payload: payload,
		}
	}
	if _, err := (*BrokerHandler)(nil).Handle(t.Context(), database.Request{}); database.CodeOf(
		err,
	) != database.CodeUnsupported {
		t.Fatalf("nil handler error = %v", err)
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := handler.Handle(canceled, request(workflowRPCOperationPreflight, workflowTargetRequest{
		StoreID: handler.storeID,
	})); database.CodeOf(err) != database.CodeDeadline {
		t.Fatalf("canceled request error = %v", err)
	}
	if _, err := handler.Handle(t.Context(), request(workflowRPCOperationResolveStore, workflowResolveRequest{
		WorkspaceSelector: "BAD",
	})); database.CodeOf(err) != database.CodeInvalid {
		t.Fatalf("invalid selector error = %v", err)
	}
	if _, err := handler.Handle(t.Context(), request(workflowRPCOperationResolveStore, workflowResolveRequest{
		WorkspaceSelector: strings.Repeat("0", 16),
	})); database.CodeOf(err) != database.CodeUnauthorized {
		t.Fatalf("unknown selector error = %v", err)
	}
	var selector string
	for value := range handler.selectors {
		selector = value
		break
	}
	resolved, err := handler.Handle(t.Context(), request(
		workflowRPCOperationResolveStore, workflowResolveRequest{WorkspaceSelector: selector},
	))
	if err != nil || resolved.(workflowResolveResponse).StoreID != handler.storeID {
		t.Fatalf("resolved store = %#v, %v", resolved, err)
	}
	if _, err := handler.Handle(t.Context(), request(
		workflowRPCOperationPreflight, workflowTargetRequest{StoreID: "global/auth"},
	)); database.CodeOf(err) != database.CodeUnauthorized {
		t.Fatalf("foreign store error = %v", err)
	}

	operations := []string{
		workflowRPCOperationPreflight, workflowRPCOperationCreateRun,
		workflowRPCOperationCreateRunUnderLimit, workflowRPCOperationUpdateRun,
		workflowRPCOperationCancelRun, workflowRPCOperationGetRun,
		workflowRPCOperationGetRunBounded, workflowRPCOperationListRuns,
		workflowRPCOperationListHumanTasks, workflowRPCOperationClaimHumanTask,
		workflowRPCOperationRenewHumanTaskClaim, workflowRPCOperationCancelHumanTask,
		workflowRPCOperationAppendEvent, workflowRPCOperationEvents,
		workflowRPCOperationDeleteRun, workflowRPCOperationPruneTerminalRuns,
	}
	malformed := request("", struct{}{})
	for _, operation := range operations {
		malformed.Operation = operation
		if _, err := handler.handle(t.Context(), malformed, fixture.handler.store); database.CodeOf(
			err,
		) != database.CodeInvalid {
			t.Errorf("%s malformed error = %v", operation, err)
		}
	}
	for _, test := range []struct {
		operation string
		input     workflowRunIDRequest
	}{
		{workflowRPCOperationListRuns, workflowRunIDRequest{StoreID: handler.storeID, Cursor: -1}},
		{workflowRPCOperationListHumanTasks, workflowRunIDRequest{StoreID: handler.storeID, Cursor: -1}},
		{workflowRPCOperationEvents, workflowRunIDRequest{StoreID: handler.storeID, Cursor: -1}},
	} {
		if _, err := handler.Handle(t.Context(), request(test.operation, test.input)); database.CodeOf(
			err,
		) != database.CodeInvalid {
			t.Errorf("%s cursor error = %v", test.operation, err)
		}
	}
	if _, err := handler.Handle(t.Context(), request(
		"unknown", workflowTargetRequest{StoreID: handler.storeID},
	)); database.CodeOf(err) != database.CodeUnsupported {
		t.Fatalf("unknown operation error = %v", err)
	}
	if err := handler.validateStoreID(""); database.CodeOf(err) != database.CodeInvalid {
		t.Fatalf("empty StoreID error = %v", err)
	}
	if err := handler.validateStoreID("global/auth"); database.CodeOf(err) != database.CodeUnauthorized {
		t.Fatalf("foreign StoreID error = %v", err)
	}
	if _, err := workflowRequestStoreID(database.Request{Payload: []byte("{")}); database.CodeOf(
		err,
	) != database.CodeInvalid {
		t.Fatalf("malformed header error = %v", err)
	}
}

func TestWorkflowBrokerSelectorMigrationAndLifecycleBoundaries(t *testing.T) {
	if _, err := NewBrokerHandler(t.TempDir(), nil); database.CodeOf(err) != database.CodeInvalid {
		t.Fatalf("nil config error = %v", err)
	}
	if _, err := (*workflowBrokerWorkspace)(nil).open(t.Context()); database.CodeOf(err) != database.CodeUnavailable {
		t.Fatalf("nil workspace error = %v", err)
	}
	for _, value := range []string{"", "BAD", strings.Repeat("g", 16), strings.Repeat("0", 15)} {
		if validWorkflowWorkspaceSelector(value) {
			t.Errorf("invalid selector accepted: %q", value)
		}
	}
	if !validWorkflowWorkspaceSelector(strings.Repeat("a", 16)) {
		t.Fatal("valid selector rejected")
	}
	home := t.TempDir()
	configured, err := resolveConfiguredWorkflowWorkspace(home, "relative")
	if err != nil || !filepath.IsAbs(configured) {
		t.Fatalf("configured workspace = %q, %v", configured, err)
	}
	if _, err := resolveConfiguredWorkflowWorkspace(home, "bad\x00path"); database.CodeOf(err) != database.CodeInvalid {
		t.Fatalf("invalid configured workspace error = %v", err)
	}
	selector, err := workflowWorkspaceSelector(filepath.Join(home, "workspace"))
	if err != nil || !validWorkflowWorkspaceSelector(selector) {
		t.Fatalf("selector = %q, %v", selector, err)
	}
	for _, value := range []string{"", "bad\x00path"} {
		if _, err := workflowWorkspaceSelector(value); database.CodeOf(err) != database.CodeInvalid {
			t.Errorf("invalid workspace %q error = %v", value, err)
		}
	}
	if database.CodeOf(RunOfflineDatabaseMigration(t.Context(), home)) != database.CodeConflict {
		t.Fatal("unfenced workflow migration accepted")
	}
	if err := (*BrokerHandler)(nil).Close(); err != nil {
		t.Fatalf("nil close = %v", err)
	}
}
