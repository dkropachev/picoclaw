package cron

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sipeed/picoclaw/pkg/sqlitestore"
)

type cronQueryerFunc func(context.Context, string, ...any) (*sql.Rows, error)

func (f cronQueryerFunc) QueryContext(
	ctx context.Context,
	query string,
	args ...any,
) (*sql.Rows, error) {
	return f(ctx, query, args...)
}

//nolint:gocognit,govet // Boundary matrix intentionally uses independent error scopes.
func TestCronSQLiteStorageAndValidationBoundaryMatrix(t *testing.T) {
	if _, err := newCronSQLiteStorage(""); err == nil {
		t.Fatal("blank cron path was accepted")
	}
	if _, err := newCronSQLiteStorage("bad\x00path"); err == nil {
		t.Fatal("NUL cron path was accepted")
	}
	root := t.TempDir()
	storage, err := newCronSQLiteStorage(root)
	if err != nil || storage.databasePath != filepath.Join(root, cronDatabaseFilename) {
		t.Fatalf("directory storage = %#v, %v", storage, err)
	}
	if err := storage.close(); err != nil {
		t.Fatal(err)
	}
	blockingParent := filepath.Join(root, "blocking")
	if err := os.WriteFile(blockingParent, []byte("file"), 0o600); err != nil {
		t.Fatal(err)
	}
	blocked, err := newCronSQLiteStorage(filepath.Join(blockingParent, "jobs.db"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := blocked.database(t.Context()); err == nil {
		t.Fatal("database accepted file parent")
	}
	if _, err := blocked.load(t.Context()); err == nil {
		t.Fatal("load accepted file parent")
	}
	if _, err := blocked.mutate(t.Context(), nil); err == nil {
		t.Fatal("mutate accepted file parent")
	}

	service, err := NewSQLiteCronService(filepath.Join(root, "valid.db"), nil)
	if err != nil {
		t.Fatal(err)
	}
	db, err := service.storage.open(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	query := cronQueryerFunc(func(context.Context, string, ...any) (*sql.Rows, error) {
		return db.Query(`SELECT 1`)
	})
	if _, err := loadCronStore(t.Context(), query); err == nil {
		t.Fatal("cron loader accepted malformed row")
	}
	conn, err := db.Conn(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if err := conn.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := loadCronStore(t.Context(), conn); err == nil {
		t.Fatal("cron loader accepted closed connection")
	}
	if err := writeCronStore(t.Context(), conn, &CronStore{}); err == nil {
		t.Fatal("cron writer accepted closed connection")
	}
	if err := validateCronSchema(t.Context(), conn); err == nil {
		t.Fatal("cron schema accepted closed connection")
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	injected := errors.New("injected mutation")
	if _, err := service.storage.mutate(t.Context(), func(*CronStore) error {
		return injected
	}); !errors.Is(err, injected) {
		t.Fatalf("mutation error = %v", err)
	}
	if committed, err := service.storage.mutate(t.Context(), nil); err != nil || committed == nil {
		t.Fatalf("nil mutation = (%#v, %v)", committed, err)
	}

	if err := validateCronStore(nil); err == nil {
		t.Fatal("nil cron store was accepted")
	}
	if err := validateCronStore(&CronStore{Jobs: []CronJob{{ID: "same"}, {ID: "same"}}}); err == nil {
		t.Fatal("duplicate cron IDs were accepted")
	}
	if err := validateCronJob(nil); err == nil {
		t.Fatal("nil cron job was accepted")
	}
	for name, job := range map[string]CronJob{
		"missing id":   {},
		"invalid utf8": {ID: "id", Name: string([]byte{0xff})},
		"NUL":          {ID: "id", Payload: CronPayload{Message: "bad\x00value"}},
		"oversized":    {ID: "id", Name: strings.Repeat("x", maximumCronNameBytes+1)},
	} {
		t.Run(name, func(t *testing.T) {
			if err := validateCronJob(&job); err == nil {
				t.Fatalf("invalid job accepted: %#v", job)
			}
		})
	}
	if cloneCronStore(nil) != nil {
		t.Fatal("nil cron store clone is non-nil")
	}
	result := sqlitestore.ImportResult{Issues: make([]sqlitestore.ImportIssue, 512)}
	appendCronImportIssue(&result, "bounded", sha256.Sum256([]byte("record")))
	if result.Skipped != 1 || len(result.Issues) != 512 {
		t.Fatalf("bounded cron issues = %#v", result)
	}
}

//nolint:govet // Schema cases intentionally use independent error scopes.
func TestCronLegacyImportAndSchemaFailureMatrix(t *testing.T) {
	root := t.TempDir()
	service, err := NewSQLiteCronService(filepath.Join(root, "jobs.db"), nil)
	if err != nil {
		t.Fatal(err)
	}
	db, err := service.storage.open(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	conn, err := db.Conn(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	for name, data := range map[string]string{
		"malformed": "{",
		"trailing":  `{"version":1,"jobs":[]} {}`,
		"version":   `{"version":2,"jobs":[]}`,
	} {
		t.Run(name, func(t *testing.T) {
			result, err := importLegacyCronStore(t.Context(), conn, sqlitestore.LegacyInput{
				Data: []byte(data), Digest: sha256.Sum256([]byte(data)),
			})
			if err != nil || result.Skipped != 1 {
				t.Fatalf("legacy %s = (%#v, %v)", name, result, err)
			}
		})
	}
	if err := conn.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := importLegacyCronStore(t.Context(), conn, sqlitestore.LegacyInput{
		Data: []byte(`{"version":1,"jobs":[]}`),
	}); err == nil {
		t.Fatal("legacy import accepted closed connection")
	}

	for _, index := range []string{"cron_jobs_position_idx", "cron_jobs_due_idx"} {
		t.Run(index, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), index+".db")
			instance, err := NewSQLiteCronService(path, nil)
			if err != nil {
				t.Fatal(err)
			}
			raw, err := instance.storage.open(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			defer raw.Close()
			if _, err := raw.Exec(`DROP INDEX ` + index); err != nil {
				t.Fatal(err)
			}
			check, err := raw.Conn(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			defer check.Close()
			if err := validateCronSchema(t.Context(), check); err == nil {
				t.Fatalf("missing %s was accepted", index)
			}
		})
	}
}

//nolint:gocognit // Lifecycle matrix is intentionally linear.
func TestCronServiceLifecycleAndPublicBoundaryMatrix(t *testing.T) {
	var nilService *CronService
	if err := nilService.loadStore(); err == nil {
		t.Fatal("nil service load error = nil")
	}
	if err := nilService.mutateStoreUnsafe(nil); err == nil {
		t.Fatal("nil service mutation error = nil")
	}
	initErr := errors.New("initialization failed")
	failed := &CronService{initErr: initErr}
	if err := failed.loadStore(); !errors.Is(err, initErr) {
		t.Fatalf("initialization load error = %v", err)
	}
	if err := failed.mutateStoreUnsafe(nil); !errors.Is(err, initErr) {
		t.Fatalf("initialization mutation error = %v", err)
	}
	if err := (&CronService{}).Close(); err != nil {
		t.Fatal(err)
	}

	service, err := NewSQLiteCronService(filepath.Join(t.TempDir(), "jobs.db"), nil)
	if err != nil {
		t.Fatal(err)
	}
	service.SetOnJob(func(*CronJob) (string, error) { return "", nil })
	service.SetOnJobContext(func(context.Context, *CronJob) (string, error) { return "", nil })
	service.SetJobAdmission(nil)
	service.checkJobs(t.Context())
	service.executeDueJob(t.Context(), "missing")
	service.running = true
	service.executeDueJob(t.Context(), "missing")
	service.executeJobByID(t.Context(), "missing")
	service.running = false

	now := time.Now().UnixMilli()
	future := now + 60_000
	zero := int64(0)
	for name, schedule := range map[string]CronSchedule{
		"future at":  {Kind: "at", AtMS: &future},
		"past at":    {Kind: "at", AtMS: &zero},
		"bad every":  {Kind: "every", EveryMS: &zero},
		"every":      {Kind: "every", EveryMS: &future},
		"empty cron": {Kind: "cron"},
		"bad cron":   {Kind: "cron", Expr: "invalid"},
		"cron":       {Kind: "cron", Expr: "* * * * *"},
		"unknown":    {Kind: "other"},
	} {
		t.Run(name, func(t *testing.T) {
			_ = service.computeNextRun(&schedule, now)
		})
	}
	service.SetOnJob(nil)
	service.SetOnJobContext(nil)
	enabled, err := service.AddJob("enabled", CronSchedule{Kind: "at", AtMS: &future}, "", "", "")
	if err != nil {
		t.Fatal(err)
	}
	disabled, err := service.AddJob("disabled", CronSchedule{Kind: "at", AtMS: &future}, "", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if service.EnableJob(disabled.ID, false) == nil {
		t.Fatal("failed to disable job")
	}
	if jobs := service.ListJobs(false); len(jobs) != 1 || jobs[0].ID != enabled.ID {
		t.Fatalf("enabled jobs = %#v", jobs)
	}
	if _, ok := service.GetJob("missing"); ok {
		t.Fatal("missing job was found")
	}
	if err := service.UpdateJob(nil); err == nil {
		t.Fatal("nil update was accepted")
	}
	if err := service.UpdateJob(&CronJob{ID: "missing"}); err == nil {
		t.Fatal("missing update was accepted")
	}
	if service.RemoveJob("missing") {
		t.Fatal("missing job was removed")
	}
	if service.EnableJob("missing", true) != nil {
		t.Fatal("missing job was enabled")
	}
	status := service.Status()
	if status["jobs"] != 2 {
		t.Fatalf("status = %#v", status)
	}
	service.wakeChan = nil
	if err := service.Start(); err != nil {
		t.Fatal(err)
	}
	if err := service.Start(); err != nil {
		t.Fatal(err)
	}
	service.Stop()
	if err := service.Close(); err != nil {
		t.Fatal(err)
	}
}

//nolint:govet // Terminal cases intentionally use independent error scopes.
func TestCronExecuteJobTerminalStateBoundaryMatrix(t *testing.T) {
	service, err := NewSQLiteCronService(filepath.Join(t.TempDir(), "jobs.db"), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer service.Close()
	now := time.Now().UnixMilli()
	future := now + 60_000

	atJob, err := service.AddJob(
		"retained at", CronSchedule{Kind: "at", AtMS: &future}, "", "", "",
	)
	if err != nil {
		t.Fatal(err)
	}
	atJob.DeleteAfterRun = false
	if err := service.UpdateJob(atJob); err != nil {
		t.Fatal(err)
	}
	contextCalls := 0
	service.SetOnJobContext(func(context.Context, *CronJob) (string, error) {
		contextCalls++
		return "ok", nil
	})
	service.executeJobByID(t.Context(), atJob.ID)
	retained, ok := service.GetJob(atJob.ID)
	if !ok || retained.Enabled || retained.State.LastStatus != "ok" || contextCalls != 1 {
		t.Fatalf("retained at job = (%#v, %v), calls=%d", retained, ok, contextCalls)
	}

	everyMS := int64(1000)
	everyJob, err := service.AddJob(
		"recurring", CronSchedule{Kind: "every", EveryMS: &everyMS}, "", "", "",
	)
	if err != nil {
		t.Fatal(err)
	}
	service.SetOnJobContext(nil)
	service.SetOnJob(func(*CronJob) (string, error) { return "ok", nil })
	service.executeJobByID(t.Context(), everyJob.ID)
	recurring, ok := service.GetJob(everyJob.ID)
	if !ok || recurring.State.NextRunAtMS == nil || recurring.State.LastStatus != "ok" {
		t.Fatalf("recurring job = (%#v, %v)", recurring, ok)
	}

	zero := int64(0)
	recurring.Schedule.EveryMS = &zero
	if err := service.UpdateJob(recurring); err != nil {
		t.Fatal(err)
	}
	service.executeJobByID(t.Context(), recurring.ID)
	recurring, ok = service.GetJob(recurring.ID)
	if !ok || recurring.State.NextRunAtMS != nil {
		t.Fatalf("invalid recurring terminal state = (%#v, %v)", recurring, ok)
	}

	errorMS := int64(2000)
	errorJob, err := service.AddJob(
		"failing", CronSchedule{Kind: "every", EveryMS: &errorMS}, "", "", "",
	)
	if err != nil {
		t.Fatal(err)
	}
	callbackErr := errors.New("injected callback failure")
	service.SetOnJob(func(*CronJob) (string, error) { return "", callbackErr })
	service.executeJobByID(t.Context(), errorJob.ID)
	failed, ok := service.GetJob(errorJob.ID)
	if !ok || failed.State.LastStatus != "error" || failed.State.LastError != callbackErr.Error() {
		t.Fatalf("failed recurring job = (%#v, %v)", failed, ok)
	}
}

//nolint:gocognit,govet // Failure matrix intentionally uses independent error scopes.
func TestCronServiceFailureAndAdmissionBoundaryMatrix(t *testing.T) {
	initErr := errors.New("injected storage failure")
	failed := &CronService{
		initErr:  initErr,
		store:    &CronStore{Version: 1, Jobs: []CronJob{}},
		gronx:    nil,
		wakeChan: make(chan struct{}, 1),
	}
	if _, err := newCronService("", nil); err == nil {
		t.Fatal("newCronService accepted blank path")
	}
	if err := failed.Start(); err == nil {
		t.Fatal("failed service started")
	}
	future := time.Now().UnixMilli() + 60_000
	validJob := &CronJob{
		ID: "job", Name: "job", Enabled: true,
		Schedule: CronSchedule{Kind: "at", AtMS: &future},
		Payload:  CronPayload{Kind: "agent_turn"},
	}
	if _, err := failed.AddJob("job", validJob.Schedule, "", "", ""); err == nil {
		t.Fatal("failed service added job")
	}
	if _, ok := failed.GetJob("job"); ok {
		t.Fatal("failed service returned job")
	}
	if err := failed.UpdateJob(validJob); !errors.Is(err, initErr) {
		t.Fatalf("failed service update = %v", err)
	}
	if failed.RemoveJob("job") {
		t.Fatal("failed service removed job")
	}
	if failed.EnableJob("job", true) != nil {
		t.Fatal("failed service enabled job")
	}
	if failed.ListJobs(true) != nil {
		t.Fatal("failed service listed jobs")
	}

	blockedRoot := t.TempDir()
	blockingParent := filepath.Join(blockedRoot, "blocking")
	if err := os.WriteFile(blockingParent, []byte("file"), 0o600); err != nil {
		t.Fatal(err)
	}
	blockedStorage, err := newCronSQLiteStorage(filepath.Join(blockingParent, "jobs.db"))
	if err != nil {
		t.Fatal(err)
	}
	blockedService := &CronService{
		storage:  blockedStorage,
		store:    &CronStore{Version: 1},
		wakeChan: make(chan struct{}, 1),
	}
	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	blockedService.runLoop(canceled, make(chan struct{}))

	service, err := NewSQLiteCronService(filepath.Join(t.TempDir(), "jobs.db"), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer service.Close()
	due := time.Now().UnixMilli() - 1
	job, err := service.AddJob(
		"due", CronSchedule{Kind: "at", AtMS: &future}, "", "", "",
	)
	if err != nil {
		t.Fatal(err)
	}
	job.State.NextRunAtMS = &due
	if err := service.UpdateJob(job); err != nil {
		t.Fatal(err)
	}
	service.running = true
	service.admitJob = func(context.Context, *CronJob) (context.Context, func(), error) {
		return nil, nil, initErr
	}
	service.executeDueJob(t.Context(), job.ID)
	if stored, ok := service.GetJob(job.ID); !ok || stored.State.NextRunAtMS == nil {
		t.Fatalf("admission failure consumed due job: (%#v, %v)", stored, ok)
	}

	service.admitJob = func(ctx context.Context, _ *CronJob) (context.Context, func(), error) {
		service.running = false
		return ctx, func() {}, nil
	}
	service.executeDueJob(t.Context(), job.ID)
	service.running = true
	service.admitJob = nil

	// Candidate comes from the in-memory snapshot, while transactional reload
	// sees no row and therefore cannot claim it.
	if !service.RemoveJob(job.ID) {
		t.Fatal("failed to remove due job")
	}
	service.store.Jobs = []CronJob{*job}
	service.executeDueJob(t.Context(), job.ID)
	service.store.Jobs = nil

	disappearing, err := service.AddJob(
		"disappearing", CronSchedule{Kind: "at", AtMS: &future}, "", "", "",
	)
	if err != nil {
		t.Fatal(err)
	}
	service.SetOnJobContext(func(context.Context, *CronJob) (string, error) {
		service.RemoveJob(disappearing.ID)
		return "", nil
	})
	service.executeJobByID(t.Context(), disappearing.ID)
	if _, ok := service.GetJob(disappearing.ID); ok {
		t.Fatal("disappearing job remains")
	}
}
