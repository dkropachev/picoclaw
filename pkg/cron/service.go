package cron

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/adhocore/gronx"
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

func NewCronService(storePath string, onJob JobHandler) *CronService {
	service, err := newCronService(storePath, onJob)
	service.initErr = err
	return service
}

// NewSQLiteCronService opens a primary SQLite cron service and reports schema
// or legacy-migration failure directly.
func NewSQLiteCronService(databasePath string, onJob JobHandler) (*CronService, error) {
	return newCronService(databasePath, onJob)
}

func newCronService(storePath string, onJob JobHandler) (*CronService, error) {
	cs := &CronService{
		storePath: storePath,
		store:     &CronStore{Version: 1, Jobs: []CronJob{}},
		onJob:     onJob,
		gronx:     gronx.New(),
		wakeChan:  make(chan struct{}),
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

func (cs *CronService) Start() error {
	cs.mu.Lock()
	defer cs.mu.Unlock()

	if cs.running {
		return nil
	}

	if err := cs.mutateStoreUnsafe(func(store *CronStore) error {
		cs.store = store
		cs.recomputeNextRuns()
		return nil
	}); err != nil {
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

// Close stops scheduling and releases the SQLite handle. Stop remains the
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
	claimed := false
	if err := cs.mutateStoreUnsafe(func(store *CronStore) error {
		for i := range store.Jobs {
			job := &store.Jobs[i]
			if job.ID == jobID &&
				job.Enabled &&
				job.State.NextRunAtMS != nil &&
				*job.State.NextRunAtMS <= time.Now().UnixMilli() {
				job.State.NextRunAtMS = nil
				claimed = true
				break
			}
		}
		return nil
	}); err != nil {
		cs.mu.Unlock()
		log.Printf("[cron] failed to persist job %s claim: %v", jobID, err)
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
	var jobName, nextRunStr string
	found := false
	persistErr := cs.mutateStoreUnsafe(func(store *CronStore) error {
		var job *CronJob
		for i := range store.Jobs {
			if store.Jobs[i].ID == jobID {
				job = &store.Jobs[i]
				break
			}
		}
		if job == nil {
			return nil
		}
		found = true
		jobName = job.Name
		job.State.LastRunAtMS = &startTime
		job.UpdatedAtMS = time.Now().UnixMilli()
		if err != nil {
			job.State.LastStatus = "error"
			job.State.LastError = err.Error()
		} else {
			job.State.LastStatus = "ok"
			job.State.LastError = ""
		}

		if job.Schedule.Kind == "at" {
			if job.DeleteAfterRun {
				removeJobFromStore(store, job.ID)
				nextRunStr = "(deleted)"
			} else {
				job.Enabled = false
				job.State.NextRunAtMS = nil
				nextRunStr = "(disabled)"
			}
			return nil
		}
		nextRun := cs.computeNextRun(&job.Schedule, time.Now().UnixMilli())
		job.State.NextRunAtMS = nextRun
		if nextRun != nil {
			nextRunStr = time.UnixMilli(*nextRun).Format("2006-01-02 15:04:05")
		} else {
			nextRunStr = "(none)"
		}
		return nil
	})
	cs.mu.Unlock()
	if persistErr != nil {
		log.Printf("[cron] failed to save store: %v", persistErr)
		return
	}
	if !found {
		log.Printf("[cron] job %s disappeared before state update", jobID)
		return
	}
	if err != nil {
		log.Printf("[cron] ✗ job '%s' failed after %dms: %v", jobName, execDuration, err)
	}
	if err == nil {
		log.Printf("[cron] ✓ job '%s' completed in %dms, next run: %s", jobName, execDuration, nextRunStr)
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
	if cs == nil || cs.storage == nil {
		if cs != nil && cs.initErr != nil {
			return cs.initErr
		}
		return fmt.Errorf("cron SQLite store is unavailable")
	}
	store, err := cs.storage.load(context.Background())
	if err != nil {
		return err
	}
	cs.store = store
	cs.initErr = nil
	return nil
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
		return err
	}
	cs.store = committed
	return nil
}

func (cs *CronService) AddJob(
	name string,
	schedule CronSchedule,
	message string,
	channel, to string,
) (*CronJob, error) {
	cs.mu.Lock()
	defer cs.mu.Unlock()

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
