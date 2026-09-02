//nolint:govet // Independent broker assertions intentionally reuse err.
package cron

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

func TestCronBrokerHandlerInvalidOperationMatrix(t *testing.T) {
	home := t.TempDir()
	databasePath := filepath.Join(home, "workspace", "cron", cronDatabaseFilename)
	handler, _, _ := startCronBroker(t, home, databasePath)
	request := func(operation string, input any) database.Request {
		t.Helper()
		return cronBrokerTestRequest(t, operation, input)
	}
	if _, err := handler.Handle(t.Context(), request(
		cronOperationPreflight, cronStoreRequest{StoreID: BrokerStoreID},
	)); err != nil {
		t.Fatalf("preflight error = %v", err)
	}
	if _, err := (*BrokerHandler)(nil).Handle(t.Context(), database.Request{}); database.CodeOf(
		err,
	) != database.CodeUnsupported {
		t.Fatalf("nil handler error = %v", err)
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := handler.Handle(canceled, request(cronOperationList, cronListRequest{
		StoreID: BrokerStoreID,
	})); database.CodeOf(err) != database.CodeDeadline {
		t.Fatalf("canceled request error = %v", err)
	}
	if _, err := handler.Handle(t.Context(), request(cronOperationResolve, cronResolveStoreRequest{
		WorkspaceSelector: "BAD",
	})); database.CodeOf(err) != database.CodeInvalid {
		t.Fatalf("invalid selector error = %v", err)
	}
	if _, err := handler.Handle(t.Context(), request(cronOperationResolve, cronResolveStoreRequest{
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
		cronOperationResolve, cronResolveStoreRequest{WorkspaceSelector: selector},
	))
	if err != nil || resolved.(cronResolveStoreResponse).StoreID != BrokerStoreID {
		t.Fatalf("resolved store = %#v, %v", resolved, err)
	}

	every := int64(time.Minute / time.Millisecond)
	invalid := []struct {
		operation string
		input     any
	}{
		{cronOperationList, cronListRequest{StoreID: BrokerStoreID, Cursor: -1}},
		{cronOperationGet, cronJobRequest{StoreID: BrokerStoreID}},
		{cronOperationAdd, cronAddRequest{
			StoreID: BrokerStoreID, Name: "bad\x00", Schedule: CronSchedule{Kind: "every", EveryMS: &every},
		}},
		{cronOperationUpdate, cronUpdateRequest{StoreID: BrokerStoreID}},
		{cronOperationRemove, cronJobRequest{StoreID: BrokerStoreID}},
		{cronOperationEnable, cronEnableRequest{StoreID: BrokerStoreID}},
		{cronOperationClaim, cronClaimRequest{StoreID: BrokerStoreID, JobID: "id", NowMS: -1}},
		{cronOperationComplete, cronCompleteRequest{StoreID: BrokerStoreID, JobID: "id", StartedMS: 2, FinishedMS: 1}},
	}
	for _, test := range invalid {
		if _, err := handler.Handle(t.Context(), request(test.operation, test.input)); database.CodeOf(
			err,
		) != database.CodeInvalid {
			t.Errorf("%s invalid error = %v", test.operation, err)
		}
	}
	jobValue, err := handler.Handle(t.Context(), request(cronOperationAdd, cronAddRequest{
		StoreID: BrokerStoreID, Name: "coverage", Schedule: CronSchedule{Kind: "every", EveryMS: &every},
	}))
	if err != nil || jobValue.(cronBrokerResponse).Job == nil {
		t.Fatalf("add = %#v, %v", jobValue, err)
	}
	job := jobValue.(cronBrokerResponse).Job
	missing := cloneCronJob(*job)
	missing.ID = "missing"
	if _, err := handler.Handle(t.Context(), request(cronOperationUpdate, cronUpdateRequest{
		StoreID: BrokerStoreID, Job: &missing,
	})); database.CodeOf(err) != database.CodeNotFound {
		t.Fatalf("missing update error = %v", err)
	}
	if status, err := handler.Handle(t.Context(), request(
		cronOperationStatus, cronStatusRequest{StoreID: BrokerStoreID, Running: true},
	)); err != nil || status.(cronBrokerResponse).Status == nil {
		t.Fatalf("status = %#v, %v", status, err)
	}
	if _, err := handler.Handle(t.Context(), request(
		cronOperationInitialize, cronStoreRequest{StoreID: BrokerStoreID},
	)); err != nil {
		t.Fatalf("initialize error = %v", err)
	}
	if _, err := handler.Handle(t.Context(), request(
		"unknown", cronStoreRequest{StoreID: BrokerStoreID},
	)); database.CodeOf(err) != database.CodeUnsupported {
		t.Fatalf("unknown operation error = %v", err)
	}
	if _, err := cronRequestStoreID(database.Request{Payload: []byte("{")}); database.CodeOf(
		err,
	) != database.CodeInvalid {
		t.Fatalf("malformed header error = %v", err)
	}
}

func TestCronBrokerCodecClientAndErrorBoundaries(t *testing.T) {
	cases := []struct {
		err  error
		code database.ErrorCode
	}{
		{database.NewError(database.CodeConflict, "conflict"), database.CodeConflict},
		{context.Canceled, database.CodeDeadline},
		{sqlitestore.ErrTooNew, database.CodeUnsupported},
		{sqlitestore.ErrInvalidSchema, database.CodeIntegrity},
		{sqlitestore.ErrIntegrity, database.CodeIntegrity},
		{errors.New("boom"), database.CodeInternal},
	}
	if mapCronBrokerError(nil) != nil {
		t.Fatal("nil cron error mapped non-nil")
	}
	for _, test := range cases {
		if got := database.CodeOf(mapCronBrokerError(test.err)); got != test.code {
			t.Errorf("map(%v) = %s, want %s", test.err, got, test.code)
		}
	}
	if !isCronNotFound(errors.New("JOB NOT FOUND")) || isCronNotFound(nil) || isCronNotFound(errors.New("boom")) {
		t.Fatal("not-found classification mismatch")
	}
	if validCronJobID("") || validCronJobID("bad\x00id") ||
		validCronJobID(strings.Repeat("x", maximumCronIDBytes+1)) || !validCronJobID("valid-id") {
		t.Fatal("job ID validation mismatch")
	}
	if validCronStoreID("") || !validCronStoreID(BrokerStoreID) {
		t.Fatal("store ID validation mismatch")
	}
	if validateCronAddInput(cronAddRequest{
		Name: "valid", Schedule: CronSchedule{Kind: "every", EveryMS: ptrInt64(time.Minute.Milliseconds())},
	}) != nil {
		t.Fatal("valid add input rejected")
	}
	if database.CodeOf(invalidCronRequest()) != database.CodeInvalid {
		t.Fatal("invalid request code mismatch")
	}
	if err := (*CronService)(nil).callBroker(t.Context(), "op", nil, nil, false); database.CodeOf(
		err,
	) != database.CodeUnavailable {
		t.Fatalf("nil service broker error = %v", err)
	}
	storedErr := database.NewError(database.CodeConflict, "stored")
	service := &CronService{brokerClient: &database.Client{}, brokerErr: storedErr}
	if err := service.callBroker(nil, "op", nil, nil, true); err != storedErr {
		t.Fatalf("stored service error = %v", err)
	}
	if err := (*CronService)(nil).acceptBrokerResponse(cronBrokerResponse{}); database.CodeOf(
		err,
	) != database.CodeUnavailable {
		t.Fatalf("nil response acceptance error = %v", err)
	}
	service.brokerErr = nil
	service.initErr = errors.New("old")
	if err := service.acceptBrokerResponse(cronBrokerResponse{}); err != nil || service.initErr != nil {
		t.Fatalf("response acceptance = %v/%v", err, service.initErr)
	}
	job := CronJob{ID: "id", Name: "name", Schedule: CronSchedule{Kind: "every"}}
	handler := &BrokerHandler{}
	response := handler.response([]CronJob{job}, &job)
	job.Name = "changed"
	if response.Jobs[0].Name != "name" || response.Job.Name != "name" {
		t.Fatalf("broker response aliased source: %#v", response)
	}
}

func TestCronBrokerWorkspaceMigrationAndLifecycleBoundaries(t *testing.T) {
	if _, err := NewBrokerHandler(t.TempDir(), nil); database.CodeOf(err) != database.CodeInvalid {
		t.Fatalf("nil config error = %v", err)
	}
	for _, value := range []string{"", "BAD", strings.Repeat("g", 16), strings.Repeat("0", 15)} {
		if validCronWorkspaceSelector(value) {
			t.Errorf("invalid selector accepted: %q", value)
		}
	}
	if !validCronWorkspaceSelector(strings.Repeat("a", 16)) {
		t.Fatal("valid selector rejected")
	}
	home := t.TempDir()
	configured, err := resolveConfiguredCronWorkspace(home, "relative")
	if err != nil || !filepath.IsAbs(configured) {
		t.Fatalf("configured workspace = %q, %v", configured, err)
	}
	if _, err := resolveConfiguredCronWorkspace(home, "bad\x00path"); database.CodeOf(err) != database.CodeInvalid {
		t.Fatalf("invalid configured workspace error = %v", err)
	}
	selector, err := cronWorkspaceSelector(filepath.Join(home, "workspace"))
	if err != nil || !validCronWorkspaceSelector(selector) {
		t.Fatalf("workspace selector = %q, %v", selector, err)
	}
	for _, locator := range []string{
		filepath.Join(home, "cron"), filepath.Join(home, "cron", cronDatabaseFilename),
		filepath.Join(home, "cron", cronLegacyFilename),
	} {
		workspace, err := cronWorkspaceFromLocator(locator)
		if err != nil || workspace != home {
			t.Errorf("locator %q = %q, %v", locator, workspace, err)
		}
	}
	for _, locator := range []string{
		"", " leading", filepath.Join(home, "not-cron"), filepath.Join(home, "cron", "other.db"),
		filepath.Join(home, "cron", "jobs.sqlite"),
	} {
		if _, err := cronWorkspaceFromLocator(locator); database.CodeOf(err) != database.CodeInvalid {
			t.Errorf("invalid locator %q error = %v", locator, err)
		}
	}
	if _, err := resolveCronBrokerStoreID(t.Context(), nil, filepath.Join(home, "cron")); database.CodeOf(
		err,
	) != database.CodeUnavailable {
		t.Fatalf("nil resolver error = %v", err)
	}
	if _, err := NewOfflineService(filepath.Join(home, "cron"), nil); database.CodeOf(err) != database.CodeConflict {
		t.Fatal("unfenced cron migration accepted")
	}
	if err := (*BrokerHandler)(nil).Close(); err != nil {
		t.Fatalf("nil close = %v", err)
	}
	if _, err := os.Stat(filepath.Join(home, "cron", cronDatabaseFilename)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("unfenced migration touched store: %v", err)
	}
}

func ptrInt64(value int64) *int64 { return &value }
