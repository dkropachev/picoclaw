//nolint:govet // Independent storage and broker assertions intentionally reuse err.
package cron

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/adhocore/gronx"

	"github.com/sipeed/picoclaw/internal/sqlitestore"
	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/database"
)

func TestCronBrokerResidualHandlerAndWorkspaceBranches(t *testing.T) {
	home := t.TempDir()
	cfg := config.DefaultConfig()
	cfg.Agents.Defaults.Workspace = filepath.Join(home, "workspace")
	handler, err := NewBrokerHandler(home, cfg)
	if err != nil {
		t.Fatal(err)
	}
	request := func(operation string, input any) database.Request {
		t.Helper()
		return cronBrokerTestRequest(t, operation, input)
	}
	if _, err := handler.Handle(nil, request(cronOperationResolve, cronResolveStoreRequest{
		WorkspaceSelector: handler.workspaces[BrokerStoreID].selector,
	})); err != nil {
		t.Fatalf("nil-context resolve error = %v", err)
	}
	if _, err := handler.Handle(t.Context(), request(
		cronOperationPreflight, cronStoreRequest{StoreID: "global.auth"},
	)); database.CodeOf(err) != database.CodeUnauthorized {
		t.Fatalf("foreign preflight error = %v", err)
	}
	if _, err := handler.Handle(t.Context(), request(
		cronOperationPreflight, cronStoreRequest{StoreID: BrokerStoreID},
	)); err != nil {
		t.Fatal(err)
	}
	if _, err := handler.Handle(t.Context(), request(
		cronOperationList, cronListRequest{StoreID: BrokerStoreID, Cursor: 1},
	)); database.CodeOf(err) != database.CodeInvalid {
		t.Fatalf("past-end cursor error = %v", err)
	}
	if err := handler.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := handler.Handle(t.Context(), request(
		cronOperationList, cronListRequest{StoreID: BrokerStoreID},
	)); database.CodeOf(err) != database.CodeUnavailable {
		t.Fatalf("closed handler error = %v", err)
	}

	injected := errors.New("injected cron storage failure")
	failedService := &CronService{
		initErr: injected, store: &CronStore{Version: 1}, gronx: gronx.New(), wakeChan: make(chan struct{}, 1),
	}
	failedHandler := &BrokerHandler{workspaces: map[database.StoreID]*cronBrokerWorkspace{
		BrokerStoreID: {service: failedService},
	}}
	every := int64(time.Minute / time.Millisecond)
	validJob := &CronJob{
		ID: "job", Name: "job", Enabled: true,
		Schedule: CronSchedule{Kind: "every", EveryMS: &every}, Payload: CronPayload{Kind: "agent_turn"},
	}
	failureCases := []struct {
		operation string
		input     any
	}{
		{cronOperationList, cronListRequest{StoreID: BrokerStoreID}},
		{cronOperationGet, cronJobRequest{StoreID: BrokerStoreID, JobID: "job"}},
		{cronOperationAdd, cronAddRequest{
			StoreID: BrokerStoreID, Name: "job", Schedule: validJob.Schedule,
		}},
		{cronOperationUpdate, cronUpdateRequest{StoreID: BrokerStoreID, Job: validJob}},
		{cronOperationRemove, cronJobRequest{StoreID: BrokerStoreID, JobID: "job"}},
		{cronOperationEnable, cronEnableRequest{StoreID: BrokerStoreID, JobID: "job", Enabled: true}},
		{cronOperationStatus, cronStatusRequest{StoreID: BrokerStoreID}},
		{cronOperationInitialize, cronStoreRequest{StoreID: BrokerStoreID}},
		{cronOperationClaim, cronClaimRequest{StoreID: BrokerStoreID, JobID: "job"}},
		{cronOperationComplete, cronCompleteRequest{
			StoreID: BrokerStoreID, JobID: "job", StartedMS: 1, FinishedMS: 2, Succeeded: true,
		}},
	}
	for _, test := range failureCases {
		if _, err := failedHandler.Handle(t.Context(), request(test.operation, test.input)); database.CodeOf(
			err,
		) != database.CodeInternal {
			t.Errorf("%s injected error = %v", test.operation, err)
		}
	}

	for _, configured := range []string{"", "~", "~/cron-coverage", filepath.Join(home, "absolute")} {
		resolved, err := resolveConfiguredCronWorkspace(home, configured)
		if err != nil || !filepath.IsAbs(resolved) {
			t.Errorf("resolve configured %q = %q, %v", configured, resolved, err)
		}
	}
	if workspace, err := cronWorkspaceFromLocator("~/cron"); err != nil || !filepath.IsAbs(workspace) {
		t.Fatalf("home cron locator = %q, %v", workspace, err)
	}
	for _, locator := range []string{"~", filepath.Join(home, "cron", "other.json")} {
		_, err := cronWorkspaceFromLocator(locator)
		if database.CodeOf(err) != database.CodeInvalid {
			t.Errorf("invalid locator %q error = %v", locator, err)
		}
	}
	for _, workspace := range []string{"", " space ", "bad\x00workspace"} {
		if _, err := canonicalCronWorkspace(workspace); database.CodeOf(err) != database.CodeInvalid {
			t.Errorf("invalid workspace %q error = %v", workspace, err)
		}
	}
	if _, err := cronWorkspaceSelector("bad\x00workspace"); database.CodeOf(err) != database.CodeInvalid {
		t.Fatalf("invalid selector source error = %v", err)
	}

	func() {
		restoreAuthority := database.SuspendProviderTestAuthority()
		allowUnfencedCronProviderForTests.Store(false)
		defer func() {
			allowUnfencedCronProviderForTests.Store(true)
			restoreAuthority()
		}()
		if _, err := NewBrokerHandler(home, cfg); database.CodeOf(err) != database.CodeUnauthorized {
			t.Fatalf("unfenced broker constructor error = %v", err)
		}
	}()
}

func TestCronResidualLocalFailureAndLoopBranches(t *testing.T) {
	injected := errors.New("injected cron storage failure")
	service := &CronService{
		initErr: injected, store: &CronStore{Version: 1}, gronx: gronx.New(), wakeChan: make(chan struct{}, 1),
	}
	if err := service.loadStore(); !errors.Is(err, injected) {
		t.Fatalf("loadStore stored error = %v", err)
	}
	service.initErr = nil
	if err := service.loadStore(); err == nil {
		t.Fatal("loadStore without storage succeeded")
	}
	service.initErr = injected
	if err := service.mutateStoreUnsafe(nil); !errors.Is(err, injected) {
		t.Fatalf("mutate stored error = %v", err)
	}
	if err := service.initializeSchedulerStoreUnsafe(); !errors.Is(err, injected) {
		t.Fatalf("initialize stored error = %v", err)
	}
	if claimed, err := service.claimDueJobUnsafe("job", 1); claimed || !errors.Is(err, injected) {
		t.Fatalf("claim stored error = %v/%v", claimed, err)
	}
	if result, err := service.completeJobUnsafe(cronCompleteRequest{JobID: "job"}); result.found ||
		!errors.Is(err, injected) {
		t.Fatalf("complete stored error = %#v/%v", result, err)
	}

	now := time.Now().UnixMilli()
	service.store = &CronStore{Version: 1, Jobs: []CronJob{{
		ID: "due", Enabled: true, Schedule: CronSchedule{Kind: "every"},
		Payload: CronPayload{Kind: "agent_turn"}, State: CronJobState{NextRunAtMS: &now},
	}}}
	service.running = true
	service.executeDueJob(t.Context(), "due")
	service.onJob = func(*CronJob) (string, error) { return "", nil }
	service.executeJobByID(t.Context(), "due")
	service.executeJobByID(t.Context(), "missing")

	for _, setup := range []func(*CronService, chan struct{}, context.CancelFunc){
		func(_ *CronService, stop chan struct{}, _ context.CancelFunc) { close(stop) },
		func(_ *CronService, _ chan struct{}, cancel context.CancelFunc) { cancel() },
		func(value *CronService, stop chan struct{}, _ context.CancelFunc) {
			value.wakeChan <- struct{}{}
			go func() {
				time.Sleep(time.Millisecond)
				close(stop)
			}()
		},
	} {
		loopService := &CronService{
			initErr: injected, store: &CronStore{Version: 1}, gronx: gronx.New(), wakeChan: make(chan struct{}, 1),
		}
		ctx, cancel := context.WithCancel(context.Background())
		stop := make(chan struct{})
		setup(loopService, stop, cancel)
		loopService.runLoop(ctx, stop)
		cancel()
	}
}

func TestCronResidualSchemaAndLegacyImportBranches(t *testing.T) {
	openConn := func(t *testing.T) (*cronSQLiteStorage, *sql.DB, func()) {
		t.Helper()
		storage, err := newCronSQLiteStorage(filepath.Join(t.TempDir(), cronDatabaseFilename))
		if err != nil {
			t.Fatal(err)
		}
		db, err := storage.open(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		return storage, db, func() { _ = db.Close() }
	}

	t.Run("unexpected object", func(t *testing.T) {
		_, db, cleanup := openConn(t)
		defer cleanup()
		if _, err := db.ExecContext(t.Context(), `CREATE TABLE unexpected (id INTEGER)`); err != nil {
			t.Fatal(err)
		}
		conn, err := db.Conn(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		defer conn.Close()
		if err := validateCronSchema(t.Context(), conn); err == nil {
			t.Fatal("unexpected schema object accepted")
		}
	})
	t.Run("missing singleton", func(t *testing.T) {
		_, db, cleanup := openConn(t)
		defer cleanup()
		if _, err := db.ExecContext(t.Context(), `DELETE FROM cron_store`); err != nil {
			t.Fatal(err)
		}
		conn, err := db.Conn(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		defer conn.Close()
		if err := validateCronSchema(t.Context(), conn); err == nil {
			t.Fatal("missing singleton accepted")
		}
	})
	t.Run("unexpected unique index", func(t *testing.T) {
		_, db, cleanup := openConn(t)
		defer cleanup()
		if _, err := db.ExecContext(
			t.Context(),
			`CREATE UNIQUE INDEX unexpected_unique ON cron_jobs(name)`,
		); err != nil {
			t.Fatal(err)
		}
		conn, err := db.Conn(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		defer conn.Close()
		if err := validateCronSchema(t.Context(), conn); err == nil {
			t.Fatal("unexpected unique index accepted")
		}
	})

	tooMany := legacyCronStore{Version: 1, Jobs: make([]json.RawMessage, maximumCronJobs+1)}
	for index := range tooMany.Jobs {
		tooMany.Jobs[index] = json.RawMessage(`{}`)
	}
	data, err := json.Marshal(tooMany)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := importLegacyCronStore(t.Context(), nil, sqlitestore.LegacyInput{Data: data}); err == nil {
		t.Fatal("oversized legacy store accepted")
	}

	_, db, cleanup := openConn(t)
	defer cleanup()
	conn, err := db.Conn(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	every := int64(time.Minute / time.Millisecond)
	job := CronJob{ID: "legacy", Schedule: CronSchedule{Kind: "every", EveryMS: &every}}
	raw, err := json.Marshal(job)
	if err != nil {
		t.Fatal(err)
	}
	legacyData, err := json.Marshal(legacyCronStore{Version: 1, Jobs: []json.RawMessage{raw}})
	if err != nil {
		t.Fatal(err)
	}
	input := sqlitestore.LegacyInput{Data: legacyData, Digest: sha256.Sum256(legacyData)}
	first, err := importLegacyCronStore(t.Context(), conn, input)
	if err != nil || first.Imported != 1 {
		t.Fatalf("first legacy import = %#v, %v", first, err)
	}
	second, err := importLegacyCronStore(t.Context(), conn, input)
	if err != nil || second.Skipped != 1 {
		t.Fatalf("authoritative legacy replay = %#v, %v", second, err)
	}
}

func TestCronResidualProviderAuthorityBoundaries(t *testing.T) {
	restoreAuthority := database.SuspendProviderTestAuthority()
	allowUnfencedCronProviderForTests.Store(false)
	t.Cleanup(func() {
		allowUnfencedCronProviderForTests.Store(true)
		restoreAuthority()
	})
	if _, err := newCronSQLiteStorage("store"); database.CodeOf(err) != database.CodeUnauthorized {
		t.Fatalf("unfenced storage constructor error = %v", err)
	}
	storage := &cronSQLiteStorage{databasePath: filepath.Join(t.TempDir(), cronDatabaseFilename)}
	if _, err := storage.open(t.Context()); database.CodeOf(err) != database.CodeUnauthorized {
		t.Fatalf("unfenced storage open error = %v", err)
	}
	if _, err := newLocalCronService("store", nil); database.CodeOf(err) != database.CodeUnauthorized {
		t.Fatalf("unfenced local service error = %v", err)
	}
	if _, err := NewBrokerHandler(t.TempDir(), config.DefaultConfig()); database.CodeOf(
		err,
	) != database.CodeUnauthorized {
		t.Fatalf("unfenced broker error = %v", err)
	}
	if _, err := os.Stat(storage.databasePath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("unfenced access touched database: %v", err)
	}
}
