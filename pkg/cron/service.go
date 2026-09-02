package cron

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log"
	"sync"
	"sync/atomic"
	"time"

	"github.com/adhocore/gronx"

	"github.com/sipeed/picoclaw/pkg/database"
)

type CronSchedule struct {
	Kind    string `json:"kind"`
	AtMS    *int64 `json:"atMs,omitempty"`
	EveryMS *int64 `json:"everyMs,omitempty"`
	Expr    string `json:"expr,omitempty"`
	TZ      string `json:"tz,omitempty"`
}

type CronPayload struct {
	Kind    string `json:"kind"`
	Message string `json:"message"`
	Command string `json:"command,omitempty"`
	Channel string `json:"channel,omitempty"`
	To      string `json:"to,omitempty"`
}

type CronJobState struct {
	NextRunAtMS *int64 `json:"nextRunAtMs,omitempty"`
	LastRunAtMS *int64 `json:"lastRunAtMs,omitempty"`
	LastStatus  string `json:"lastStatus,omitempty"`
	LastError   string `json:"lastError,omitempty"`
}

type CronJob struct {
	ID             string       `json:"id"`
	Name           string       `json:"name"`
	Enabled        bool         `json:"enabled"`
	Schedule       CronSchedule `json:"schedule"`
	Payload        CronPayload  `json:"payload"`
	State          CronJobState `json:"state"`
	CreatedAtMS    int64        `json:"createdAtMs"`
	UpdatedAtMS    int64        `json:"updatedAtMs"`
	DeleteAfterRun bool         `json:"deleteAfterRun"`
}

type CronStore struct {
	Version int       `json:"version"`
	Jobs    []CronJob `json:"jobs"`
}

type JobHandler func(job *CronJob) (string, error)

type ContextJobHandler func(ctx context.Context, job *CronJob) (string, error)

// JobAdmission acquires any runtime generation or lifecycle lease required
// before a due job is durably claimed. The release function remains held
// through the callback and final state save.
type JobAdmission func(
	ctx context.Context,
	job *CronJob,
) (context.Context, func(), error)

type CronService struct {
	storePath    string
	storage      *cronSQLiteStorage
	brokerClient *database.Client
	storeID      database.StoreID
	brokerErr    error
	store        *CronStore
	initErr      error
	onJob        JobHandler
	onJobContext ContextJobHandler
	admitJob     JobAdmission
	mu           sync.RWMutex
	running      bool
	stopChan     chan struct{}
	runCancel    context.CancelFunc
	runDone      chan struct{}
	wakeChan     chan struct{}
	gronx        *gronx.Gronx
}

var allowUnfencedCronProviderForTests atomic.Bool

// NewForWorkspace returns the typed cron client for an opaque configured
// workspace selector. Production callers never construct a database filename.
func NewForWorkspace(workspace string, onJob JobHandler) *CronService {
	service, err := newCronService(workspace, onJob)
	service.initErr = err
	return service
}

// NewOfflineService opens a trusted cron store for the exclusively fenced
// migration adapter. Runtime callers use NewForWorkspace and opaque StoreIDs.
func NewOfflineService(databasePath string, onJob JobHandler) (*CronService, error) {
	if !database.MigrationFenceHeld() {
		return nil, database.NewError(
			database.CodeConflict,
			"cron migration requires the exclusive database fence",
		)
	}
	return newLocalCronService(databasePath, onJob)
}

func newCronService(storePath string, onJob JobHandler) (*CronService, error) {
	return newCronServiceWithClient(storePath, onJob, database.RuntimeClient())
}

func newLocalCronService(storePath string, onJob JobHandler) (*CronService, error) {
	if !cronLocalProviderAuthorized() {
		return nil, database.NewError(
			database.CodeUnauthorized,
			"cron local store requires database owner fencing",
		)
	}
	return newCronServiceWithClient(storePath, onJob, nil)
}

func newCronServiceWithClient(
	storePath string,
	onJob JobHandler,
	brokerClient *database.Client,
) (*CronService, error) {
	cs := &CronService{
		storePath: storePath, brokerClient: brokerClient,
		storeID: BrokerStoreID,
		store:   &CronStore{Version: 1, Jobs: []CronJob{}}, onJob: onJob,
		gronx: gronx.New(), wakeChan: make(chan struct{}),
	}
	if brokerClient != nil {
		storeID, resolveErr := resolveCronBrokerStoreID(
			context.Background(), brokerClient, storePath,
		)
		if resolveErr != nil {
			cs.brokerErr = resolveErr
			return cs, resolveErr
		}
		cs.storeID = storeID
		if err := cs.loadStore(); err != nil {
			return cs, err
		}
		return cs, nil
	}
	if !cronLocalProviderAuthorized() {
		err := database.NewError(
			database.CodeUnavailable,
			"cron database broker client is unavailable",
		)
		cs.brokerErr = err
		return cs, err
	}
	storage, err := newCronSQLiteStorage(storePath)
	if err != nil {
		return cs, err
	}
	cs.storage = storage
	cs.storePath = storage.databasePath
	if err := cs.loadStore(); err != nil {
		return cs, err
	}
	return cs, nil
}

func cronLocalProviderAuthorized() bool {
	return database.BrokerAuthorityHeld() || database.MigrationFenceHeld() ||
		database.ProviderTestAuthorityHeld() || allowUnfencedCronProviderForTests.Load()
}

// StoreID returns the opaque broker target. Standalone/offline services retain
// the primary logical identity without consulting the runtime broker.
func (cs *CronService) StoreID() database.StoreID {
	if cs == nil {
		return ""
	}
	return cs.storeID
}

func (cs *CronService) Start() error {
	cs.mu.Lock()
	defer cs.mu.Unlock()

	if cs.running {
		return nil
	}

	if err := cs.initializeSchedulerStoreUnsafe(); err != nil {
		return fmt.Errorf("failed to initialize cron store: %w", err)
	}

	cs.stopChan = make(chan struct{})
	runCtx, runCancel := context.WithCancel(context.Background())
	cs.runCancel = runCancel
	cs.runDone = make(chan struct{})
	if cs.wakeChan == nil {
		cs.wakeChan = make(chan struct{})
	}
	cs.running = true
	go func(done chan struct{}, stopChan chan struct{}) {
		defer close(done)
		cs.runLoop(runCtx, stopChan)
	}(cs.runDone, cs.stopChan)

	return nil
}

func (cs *CronService) Stop() {
	cs.mu.Lock()
	if !cs.running {
		cs.mu.Unlock()
		return
	}

	cs.running = false
	if cs.runCancel != nil {
		cs.runCancel()
		cs.runCancel = nil
	}
	if cs.stopChan != nil {
		close(cs.stopChan)
		cs.stopChan = nil
	}
	done := cs.runDone
	cs.runDone = nil
	cs.mu.Unlock()
	if done != nil {
		<-done
	}
}

// Close stops scheduling and releases a locally owned SQLite handle. A broker
// client never owns or closes the supervisor's pool. Stop remains the
// source-compatible lifecycle call for gateway-owned services.
func (cs *CronService) Close() error {
	cs.Stop()
	cs.mu.Lock()
	defer cs.mu.Unlock()
	if cs.storage == nil {
		return nil
	}
	return cs.storage.close()
}

func (cs *CronService) runLoop(ctx context.Context, stopChan chan struct{}) {
	timer := time.NewTimer(time.Hour)
	if !timer.Stop() {
		<-timer.C
	}
	defer timer.Stop()

	for {
		// Refresh SQLite each loop so CLI writes become visible to a running
		// gateway without sharing an in-memory cache across processes.
		cs.mu.Lock()
		if err := cs.loadStore(); err != nil {
			log.Printf("[cron] failed to refresh store: %v", err)
		}
		nextWake := cs.getNextWakeMS()
		cs.mu.Unlock()

		var delay time.Duration
		now := time.Now().UnixMilli()

		if nextWake == nil {
			// Poll for another process's CLI mutation at a bounded cadence.
			delay = time.Second
		} else {
			diff := *nextWake - now
			if diff <= 0 {
				delay = 0
			} else {
				delay = time.Duration(diff) * time.Millisecond
				if delay > time.Second {
					delay = time.Second
				}
			}
		}

		timer.Reset(delay)

		select {
		case <-ctx.Done():
			return
		case <-stopChan:
			return
		case <-cs.wakeChan: // wake on new job or update
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			continue
		case <-timer.C:
			cs.checkJobs(ctx)
		}
	}
}

func (cs *CronService) checkJobs(ctx context.Context) {
	cs.mu.RLock()
	if !cs.running {
		cs.mu.RUnlock()
		return
	}

	now := time.Now().UnixMilli()
	var dueJobIDs []string

	// Discover due work without changing durable state. A runtime admission
	// guard must win before NextRunAtMS is cleared, otherwise a reload can stop
	// the old service after it consumed a job but before its callback starts.
	for i := range cs.store.Jobs {
		job := &cs.store.Jobs[i]
		if job.Enabled && job.State.NextRunAtMS != nil && *job.State.NextRunAtMS <= now {
			dueJobIDs = append(dueJobIDs, job.ID)
		}
	}
	cs.mu.RUnlock()

	for _, jobID := range dueJobIDs {
		cs.executeDueJob(ctx, jobID)
	}
}

func (cs *CronService) executeDueJob(ctx context.Context, jobID string) {
	cs.mu.RLock()
	if !cs.running {
		cs.mu.RUnlock()
		return
	}
	var candidate *CronJob
	for i := range cs.store.Jobs {
		job := &cs.store.Jobs[i]
		if job.ID == jobID &&
			job.Enabled &&
			job.State.NextRunAtMS != nil &&
			*job.State.NextRunAtMS <= time.Now().UnixMilli() {
			jobCopy := cloneCronJob(*job)
			candidate = &jobCopy
			break
		}
	}
	admitJob := cs.admitJob
	cs.mu.RUnlock()
	if candidate == nil {
		return
	}

	execCtx := ctx
	releaseAdmission := func() {}
	if admitJob != nil {
		var err error
		execCtx, releaseAdmission, err = admitJob(ctx, candidate)
		if err != nil {
			if ctx.Err() == nil {
				log.Printf("[cron] job %s runtime admission failed: %v", jobID, err)
			}
			return
		}
	}
	defer releaseAdmission()

	cs.mu.Lock()
	if !cs.running {
		cs.mu.Unlock()
		return
	}
	claimed, claimErr := cs.claimDueJobUnsafe(jobID, time.Now().UnixMilli())
	if claimErr != nil {
		cs.mu.Unlock()
		log.Printf("[cron] failed to persist job %s claim: %v", jobID, claimErr)
		return
	}
	cs.mu.Unlock()
	if !claimed {
		return
	}

	cs.executeJobByID(execCtx, jobID)
}

func (cs *CronService) executeJobByID(ctx context.Context, jobID string) {
	startTime := time.Now().UnixMilli()

	cs.mu.RLock()
	var callbackJob *CronJob
	onJob := cs.onJob
	onJobContext := cs.onJobContext
	for i := range cs.store.Jobs {
		job := &cs.store.Jobs[i]
		if job.ID == jobID {
			jobCopy := cloneCronJob(*job)
			callbackJob = &jobCopy
			break
		}
	}
	cs.mu.RUnlock()

	if callbackJob == nil {
		log.Printf("[cron] job %s not found, skipping", jobID)
		return
	}

	// Log job execution start
	log.Printf("[cron] ▶ executing job '%s' (id: %s, schedule: %s, channel: %s)",
		callbackJob.Name, jobID, callbackJob.Schedule.Kind, callbackJob.Payload.Channel)

	var err error
	if onJobContext != nil {
		_, err = onJobContext(ctx, callbackJob)
	} else if onJob != nil {
		_, err = onJob(callbackJob)
	}

	execDuration := time.Now().UnixMilli() - startTime

	// Now acquire lock to update state
	cs.mu.Lock()
	finishedMS := time.Now().UnixMilli()
	completion := cronCompleteRequest{
		StoreID: cs.storeID, JobID: jobID, StartedMS: startTime,
		FinishedMS: finishedMS, Succeeded: err == nil,
	}
	if err != nil {
		completion.Failure = err.Error()
	}
	result, persistErr := cs.completeJobUnsafe(completion)
	cs.mu.Unlock()
	if persistErr != nil {
		log.Printf("[cron] failed to save store: %v", persistErr)
		return
	}
	if !result.found {
		log.Printf("[cron] job %s disappeared before state update", jobID)
		return
	}
	if err != nil {
		log.Printf("[cron] ✗ job '%s' failed after %dms: %v", result.jobName, execDuration, err)
	}
	if err == nil {
		log.Printf("[cron] ✓ job '%s' completed in %dms, next run: %s", result.jobName, execDuration, result.nextRun)
	}
}

func (cs *CronService) computeNextRun(schedule *CronSchedule, nowMS int64) *int64 {
	switch schedule.Kind {
	case "at":
		if schedule.AtMS != nil && *schedule.AtMS > nowMS {
			return schedule.AtMS
		}
		return nil
	case "every":
		if schedule.EveryMS == nil || *schedule.EveryMS <= 0 {
			return nil
		}
		next := nowMS + *schedule.EveryMS
		return &next
	case "cron":
		if schedule.Expr == "" {
			return nil
		}

		// Use gronx to calculate next run time
		now := time.UnixMilli(nowMS)
		nextTime, err := gronx.NextTickAfter(schedule.Expr, now, false)
		if err != nil {
			log.Printf("[cron] failed to compute next run for expr '%s': %v", schedule.Expr, err)
			return nil
		}

		nextMS := nextTime.UnixMilli()
		return &nextMS
	default:
		log.Printf("[cron] unknown schedule kind '%s'", schedule.Kind)
		return nil
	}
}

// wake up the loop to re-evaluate next wake time immediately (e.g. after add/update/remove jobs)
func (cs *CronService) notify() {
	select {
	case cs.wakeChan <- struct{}{}:
	default:
		// if the channel is full, it means the loop will wake up soon anyway, so we can skip sending
	}
}

func (cs *CronService) recomputeNextRuns() {
	now := time.Now().UnixMilli()
	for i := range cs.store.Jobs {
		job := &cs.store.Jobs[i]
		if job.Enabled {
			// Preserve an already-due durable occurrence so restart/reload runs
			// it immediately. Recomputing an overdue one-shot returns nil and
			// would otherwise consume it without ever invoking its handler.
			if job.State.NextRunAtMS != nil && *job.State.NextRunAtMS <= now {
				continue
			}
			job.State.NextRunAtMS = cs.computeNextRun(&job.Schedule, now)
		}
	}
}

func (cs *CronService) getNextWakeMS() *int64 {
	var nextWake *int64
	for _, job := range cs.store.Jobs {
		if job.Enabled && job.State.NextRunAtMS != nil {
			if nextWake == nil || *job.State.NextRunAtMS < *nextWake {
				nextWake = job.State.NextRunAtMS
			}
		}
	}
	return nextWake
}

func (cs *CronService) statusSnapshot(running bool) (cronStatusResponse, error) {
	cs.mu.Lock()
	defer cs.mu.Unlock()
	if err := cs.loadStore(); err != nil {
		return cronStatusResponse{}, err
	}
	nextWake := cs.getNextWakeMS()
	if nextWake != nil {
		nextWakeCopy := *nextWake
		nextWake = &nextWakeCopy
	}
	return cronStatusResponse{
		Enabled: running, Jobs: len(cs.store.Jobs), NextWakeAtMS: nextWake,
	}, nil
}

func (cs *CronService) Load() error {
	cs.mu.Lock()
	defer cs.mu.Unlock()
	return cs.loadStore()
}

func (cs *CronService) SetOnJob(handler JobHandler) {
	cs.mu.Lock()
	defer cs.mu.Unlock()
	cs.onJob = handler
}

func (cs *CronService) SetOnJobContext(handler ContextJobHandler) {
	cs.mu.Lock()
	defer cs.mu.Unlock()
	cs.onJobContext = handler
}

func (cs *CronService) SetJobAdmission(admission JobAdmission) {
	cs.mu.Lock()
	defer cs.mu.Unlock()
	cs.admitJob = admission
}

func (cs *CronService) loadStore() error {
	if cs != nil && cs.brokerClient != nil {
		jobs, err := cs.loadBrokerJobs(context.Background())
		if err != nil {
			cs.initErr = err
			return err
		}
		cs.store = &CronStore{Version: 1, Jobs: jobs}
		cs.initErr = nil
		return nil
	}
	if cs == nil || cs.storage == nil {
		if cs != nil && cs.initErr != nil {
			return cs.initErr
		}
		return fmt.Errorf("cron SQLite store is unavailable")
	}
	store, err := cs.storage.load(context.Background())
	if err != nil {
		cs.initErr = err
		return err
	}
	cs.store = store
	cs.initErr = nil
	return nil
}

func (cs *CronService) loadBrokerJobs(ctx context.Context) ([]CronJob, error) {
	if cs == nil || cs.brokerClient == nil {
		return nil, database.NewError(database.CodeUnavailable, "cron broker client is unavailable")
	}
	jobs := make([]CronJob, 0)
	cursor := 0
	for {
		var response cronBrokerResponse
		err := cs.callBroker(
			ctx,
			cronOperationList,
			cronListRequest{
				StoreID: cs.storeID, IncludeDisabled: true, Cursor: cursor,
			},
			&response,
			false,
		)
		if err != nil {
			return nil, err
		}
		if len(response.Jobs) > cronBrokerPageItems ||
			response.NextCursor != cursor+len(response.Jobs) ||
			(!response.Done && response.NextCursor <= cursor) ||
			response.NextCursor > maximumCronJobs || len(jobs) > maximumCronJobs-len(response.Jobs) {
			return nil, database.NewError(database.CodeIntegrity, "cron list pagination is invalid")
		}
		for index := range response.Jobs {
			if validateCronJob(&response.Jobs[index]) != nil {
				return nil, database.NewError(database.CodeIntegrity, "cron broker response is invalid")
			}
			jobs = append(jobs, cloneCronJob(response.Jobs[index]))
		}
		if response.Done {
			return jobs, nil
		}
		cursor = response.NextCursor
	}
}

func (cs *CronService) mutateStoreUnsafe(mutation func(*CronStore) error) error {
	if cs == nil || cs.storage == nil {
		if cs != nil && cs.initErr != nil {
			return cs.initErr
		}
		return fmt.Errorf("cron SQLite store is unavailable")
	}
	committed, err := cs.storage.mutate(context.Background(), mutation)
	if err != nil {
		cs.initErr = err
		return err
	}
	cs.store = committed
	cs.initErr = nil
	return nil
}

func (cs *CronService) initializeSchedulerStore() error {
	cs.mu.Lock()
	defer cs.mu.Unlock()
	return cs.initializeSchedulerStoreUnsafe()
}

func (cs *CronService) initializeSchedulerStoreUnsafe() error {
	if cs != nil && cs.brokerClient != nil {
		var response cronBrokerResponse
		err := cs.callBroker(
			context.Background(), cronOperationInitialize,
			cronStoreRequest{StoreID: cs.storeID}, &response, true,
		)
		if err != nil {
			cs.initErr = err
			return err
		}
		return cs.acceptBrokerResponse(response)
	}
	return cs.mutateStoreUnsafe(func(store *CronStore) error {
		cs.store = store
		cs.recomputeNextRuns()
		return nil
	})
}

func (cs *CronService) claimDueJob(jobID string, nowMS int64) (bool, error) {
	cs.mu.Lock()
	defer cs.mu.Unlock()
	return cs.claimDueJobUnsafe(jobID, nowMS)
}

func (cs *CronService) claimDueJobUnsafe(jobID string, nowMS int64) (bool, error) {
	if cs != nil && cs.brokerClient != nil {
		var response cronBrokerResponse
		err := cs.callBroker(
			context.Background(), cronOperationClaim,
			cronClaimRequest{StoreID: cs.storeID, JobID: jobID, NowMS: nowMS},
			&response, true,
		)
		if err != nil {
			cs.initErr = err
			return false, err
		}
		if err := cs.acceptBrokerResponse(response); err != nil {
			return false, err
		}
		return response.Claimed, nil
	}
	claimed := false
	err := cs.mutateStoreUnsafe(func(store *CronStore) error {
		for index := range store.Jobs {
			job := &store.Jobs[index]
			if job.ID == jobID && job.Enabled && job.State.NextRunAtMS != nil &&
				*job.State.NextRunAtMS <= nowMS {
				job.State.NextRunAtMS = nil
				claimed = true
				break
			}
		}
		return nil
	})
	return claimed, err
}

type cronCompletionResult struct {
	found   bool
	jobName string
	nextRun string
}

func (cs *CronService) completeJob(input cronCompleteRequest) (cronCompletionResult, error) {
	cs.mu.Lock()
	defer cs.mu.Unlock()
	return cs.completeJobUnsafe(input)
}

func (cs *CronService) completeJobUnsafe(
	input cronCompleteRequest,
) (cronCompletionResult, error) {
	if cs != nil && cs.brokerClient != nil {
		var response cronBrokerResponse
		err := cs.callBroker(
			context.Background(), cronOperationComplete, input, &response, true,
		)
		if err != nil {
			cs.initErr = err
			return cronCompletionResult{}, err
		}
		if err := cs.acceptBrokerResponse(response); err != nil {
			return cronCompletionResult{}, err
		}
		return cronCompletionResult{
			found: response.Found, jobName: response.JobName, nextRun: response.NextRun,
		}, nil
	}
	result := cronCompletionResult{}
	err := cs.mutateStoreUnsafe(func(store *CronStore) error {
		var job *CronJob
		for index := range store.Jobs {
			if store.Jobs[index].ID == input.JobID {
				job = &store.Jobs[index]
				break
			}
		}
		if job == nil {
			return nil
		}
		result.found = true
		result.jobName = job.Name
		job.State.LastRunAtMS = &input.StartedMS
		job.UpdatedAtMS = input.FinishedMS
		if input.Succeeded {
			job.State.LastStatus = "ok"
			job.State.LastError = ""
		} else {
			job.State.LastStatus = "error"
			job.State.LastError = input.Failure
		}
		if job.Schedule.Kind == "at" {
			if job.DeleteAfterRun {
				removeJobFromStore(store, job.ID)
				result.nextRun = "(deleted)"
			} else {
				job.Enabled = false
				job.State.NextRunAtMS = nil
				result.nextRun = "(disabled)"
			}
			return nil
		}
		nextRun := cs.computeNextRun(&job.Schedule, input.FinishedMS)
		job.State.NextRunAtMS = nextRun
		if nextRun == nil {
			result.nextRun = "(none)"
		} else {
			result.nextRun = time.UnixMilli(*nextRun).Format("2006-01-02 15:04:05")
		}
		return nil
	})
	return result, err
}

func (cs *CronService) AddJob(
	name string,
	schedule CronSchedule,
	message string,
	channel, to string,
) (*CronJob, error) {
	cs.mu.Lock()
	defer cs.mu.Unlock()
	if cs.brokerClient != nil {
		var response cronBrokerResponse
		err := cs.callBroker(
			context.Background(), cronOperationAdd,
			cronAddRequest{
				StoreID: cs.storeID, Name: name, Schedule: schedule,
				Message: message, Channel: channel, To: to,
			},
			&response,
			true,
		)
		if err != nil {
			cs.initErr = err
			return nil, err
		}
		if err := cs.acceptBrokerResponse(response); err != nil {
			return nil, err
		}
		if response.Job == nil || validateCronJob(response.Job) != nil {
			err := database.NewError(database.CodeIntegrity, "cron broker response is invalid")
			cs.initErr = err
			return nil, err
		}
		cs.notify()
		result := cloneCronJob(*response.Job)
		return &result, nil
	}

	now := time.Now().UnixMilli()

	// One-time tasks (at) should be deleted after execution
	deleteAfterRun := (schedule.Kind == "at")

	job := CronJob{
		ID:       generateID(),
		Name:     name,
		Enabled:  true,
		Schedule: schedule,
		Payload: CronPayload{
			Kind:    "agent_turn",
			Message: message,
			Channel: channel,
			To:      to,
		},
		State: CronJobState{
			NextRunAtMS: cs.computeNextRun(&schedule, now),
		},
		CreatedAtMS:    now,
		UpdatedAtMS:    now,
		DeleteAfterRun: deleteAfterRun,
	}

	if err := cs.mutateStoreUnsafe(func(store *CronStore) error {
		store.Jobs = append(store.Jobs, cloneCronJob(job))
		return nil
	}); err != nil {
		return nil, err
	}

	cs.notify()

	result := cloneCronJob(job)
	return &result, nil
}

func (cs *CronService) GetJob(jobID string) (*CronJob, bool) {
	cs.mu.Lock()
	defer cs.mu.Unlock()
	if cs.brokerClient != nil {
		var response cronBrokerResponse
		err := cs.callBroker(
			context.Background(), cronOperationGet,
			cronJobRequest{StoreID: cs.storeID, JobID: jobID},
			&response,
			false,
		)
		if err != nil {
			cs.initErr = err
			return nil, false
		}
		if err := cs.acceptBrokerResponse(response); err != nil {
			cs.initErr = err
			return nil, false
		}
		if !response.Found {
			if response.Job != nil {
				cs.initErr = database.NewError(database.CodeIntegrity, "cron broker response is invalid")
			}
			return nil, false
		}
		if response.Job == nil || validateCronJob(response.Job) != nil {
			cs.initErr = database.NewError(database.CodeIntegrity, "cron broker response is invalid")
			return nil, false
		}
		job := cloneCronJob(*response.Job)
		return &job, true
	}
	if err := cs.loadStore(); err != nil {
		return nil, false
	}

	for i := range cs.store.Jobs {
		if cs.store.Jobs[i].ID == jobID {
			jobCopy := cloneCronJob(cs.store.Jobs[i])
			return &jobCopy, true
		}
	}
	return nil, false
}

func (cs *CronService) UpdateJob(job *CronJob) error {
	cs.mu.Lock()
	defer cs.mu.Unlock()
	if job == nil {
		return fmt.Errorf("job is required")
	}
	if cs.brokerClient != nil {
		var response cronBrokerResponse
		jobCopy := cloneCronJob(*job)
		err := cs.callBroker(
			context.Background(), cronOperationUpdate,
			cronUpdateRequest{StoreID: cs.storeID, Job: &jobCopy},
			&response,
			true,
		)
		if err != nil {
			cs.initErr = err
			return err
		}
		if err := cs.acceptBrokerResponse(response); err != nil {
			return err
		}
		if response.Job == nil || validateCronJob(response.Job) != nil {
			err := database.NewError(database.CodeIntegrity, "cron broker response is invalid")
			cs.initErr = err
			return err
		}
		cs.notify()
		return nil
	}
	found := false
	err := cs.mutateStoreUnsafe(func(store *CronStore) error {
		for i := range store.Jobs {
			if store.Jobs[i].ID != job.ID {
				continue
			}
			found = true
			previous := store.Jobs[i]
			updated := cloneCronJob(*job)
			now := time.Now().UnixMilli()
			updated.UpdatedAtMS = now
			if updated.Enabled {
				if previous.Enabled != updated.Enabled ||
					!sameSchedule(previous.Schedule, updated.Schedule) {
					updated.State.NextRunAtMS = cs.computeNextRun(&updated.Schedule, now)
				}
			} else {
				updated.State.NextRunAtMS = nil
			}
			store.Jobs[i] = updated
			return nil
		}
		return nil
	})
	if err != nil {
		return err
	}
	if !found {
		return fmt.Errorf("job not found")
	}
	cs.notify()
	return nil
}

func cloneCronJob(job CronJob) CronJob {
	clone := job
	if job.Schedule.AtMS != nil {
		atMS := *job.Schedule.AtMS
		clone.Schedule.AtMS = &atMS
	}
	if job.Schedule.EveryMS != nil {
		everyMS := *job.Schedule.EveryMS
		clone.Schedule.EveryMS = &everyMS
	}
	if job.State.NextRunAtMS != nil {
		nextRunAtMS := *job.State.NextRunAtMS
		clone.State.NextRunAtMS = &nextRunAtMS
	}
	if job.State.LastRunAtMS != nil {
		lastRunAtMS := *job.State.LastRunAtMS
		clone.State.LastRunAtMS = &lastRunAtMS
	}
	return clone
}

func sameSchedule(a, b CronSchedule) bool {
	return a.Kind == b.Kind &&
		sameInt64(a.AtMS, b.AtMS) &&
		sameInt64(a.EveryMS, b.EveryMS) &&
		a.Expr == b.Expr &&
		a.TZ == b.TZ
}

func sameInt64(a, b *int64) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}

func (cs *CronService) RemoveJob(jobID string) bool {
	cs.mu.Lock()
	defer cs.mu.Unlock()
	if cs.brokerClient != nil {
		var response cronBrokerResponse
		err := cs.callBroker(
			context.Background(), cronOperationRemove,
			cronJobRequest{StoreID: cs.storeID, JobID: jobID},
			&response,
			true,
		)
		if err != nil {
			cs.initErr = err
			return false
		}
		if err := cs.acceptBrokerResponse(response); err != nil {
			cs.initErr = err
			return false
		}
		cs.notify()
		return response.Removed
	}
	removed := false
	if err := cs.mutateStoreUnsafe(func(store *CronStore) error {
		removed = removeJobFromStore(store, jobID)
		return nil
	}); err != nil {
		log.Printf("[cron] failed to remove job: %v", err)
		return false
	}
	cs.notify()
	return removed
}

func removeJobFromStore(store *CronStore, jobID string) bool {
	before := len(store.Jobs)
	var jobs []CronJob
	for _, job := range store.Jobs {
		if job.ID != jobID {
			jobs = append(jobs, job)
		}
	}
	store.Jobs = jobs
	return len(store.Jobs) < before
}

func (cs *CronService) EnableJob(jobID string, enabled bool) *CronJob {
	cs.mu.Lock()
	defer cs.mu.Unlock()
	if cs.brokerClient != nil {
		var response cronBrokerResponse
		err := cs.callBroker(
			context.Background(), cronOperationEnable,
			cronEnableRequest{StoreID: cs.storeID, JobID: jobID, Enabled: enabled},
			&response,
			true,
		)
		if err != nil {
			cs.initErr = err
			return nil
		}
		if err := cs.acceptBrokerResponse(response); err != nil {
			cs.initErr = err
			return nil
		}
		if !response.Found {
			return nil
		}
		if response.Job == nil || validateCronJob(response.Job) != nil {
			cs.initErr = database.NewError(database.CodeIntegrity, "cron broker response is invalid")
			return nil
		}
		cs.notify()
		job := cloneCronJob(*response.Job)
		return &job
	}
	var updated *CronJob
	err := cs.mutateStoreUnsafe(func(store *CronStore) error {
		for i := range store.Jobs {
			job := &store.Jobs[i]
			if job.ID != jobID {
				continue
			}
			job.Enabled = enabled
			job.UpdatedAtMS = time.Now().UnixMilli()

			if enabled {
				job.State.NextRunAtMS = cs.computeNextRun(&job.Schedule, time.Now().UnixMilli())
			} else {
				job.State.NextRunAtMS = nil
			}

			jobCopy := cloneCronJob(*job)
			updated = &jobCopy
			return nil
		}
		return nil
	})
	if err != nil {
		log.Printf("[cron] failed to update job enablement: %v", err)
		return nil
	}
	cs.notify()
	return updated
}

func (cs *CronService) ListJobs(includeDisabled bool) []CronJob {
	cs.mu.Lock()
	defer cs.mu.Unlock()
	if cs.brokerClient != nil {
		jobs, err := cs.loadBrokerJobs(context.Background())
		if err != nil {
			cs.initErr = err
			return nil
		}
		cs.store = &CronStore{Version: 1, Jobs: jobs}
		cs.initErr = nil
		filtered := make([]CronJob, 0, len(jobs))
		for index := range jobs {
			if includeDisabled || jobs[index].Enabled {
				filtered = append(filtered, cloneCronJob(jobs[index]))
			}
		}
		return filtered
	}
	if err := cs.loadStore(); err != nil {
		return nil
	}

	if includeDisabled {
		jobs := make([]CronJob, len(cs.store.Jobs))
		for index := range cs.store.Jobs {
			jobs[index] = cloneCronJob(cs.store.Jobs[index])
		}
		return jobs
	}

	var enabled []CronJob
	for _, job := range cs.store.Jobs {
		if job.Enabled {
			enabled = append(enabled, cloneCronJob(job))
		}
	}

	return enabled
}

func (cs *CronService) Status() map[string]any {
	cs.mu.Lock()
	defer cs.mu.Unlock()
	if cs.brokerClient != nil {
		var response cronBrokerResponse
		err := cs.callBroker(
			context.Background(), cronOperationStatus,
			cronStatusRequest{StoreID: cs.storeID, Running: cs.running},
			&response,
			false,
		)
		if err != nil {
			cs.initErr = err
			return map[string]any{"enabled": cs.running, "jobs": 0, "nextWakeAtMS": (*int64)(nil)}
		}
		if err := cs.acceptBrokerResponse(response); err != nil || response.Status == nil ||
			response.Status.Jobs < 0 {
			if err == nil {
				err = database.NewError(database.CodeIntegrity, "cron broker response is invalid")
			}
			cs.initErr = err
			return map[string]any{"enabled": cs.running, "jobs": 0, "nextWakeAtMS": (*int64)(nil)}
		}
		return map[string]any{
			"enabled": response.Status.Enabled, "jobs": response.Status.Jobs,
			"nextWakeAtMS": response.Status.NextWakeAtMS,
		}
	}
	_ = cs.loadStore()

	var enabledCount int
	for _, job := range cs.store.Jobs {
		if job.Enabled {
			enabledCount++
		}
	}

	return map[string]any{
		"enabled":      cs.running,
		"jobs":         len(cs.store.Jobs),
		"nextWakeAtMS": cs.getNextWakeMS(),
	}
}

func generateID() string {
	// Use crypto/rand for better uniqueness under concurrent access
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		// Fallback to time-based if crypto/rand fails
		return fmt.Sprintf("%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(b)
}
