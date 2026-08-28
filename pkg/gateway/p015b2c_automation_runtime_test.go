package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sipeed/picoclaw/pkg/agent"
	"github.com/sipeed/picoclaw/pkg/eventing"
	eventgithubpoll "github.com/sipeed/picoclaw/pkg/eventing/githubpoll"
	"github.com/sipeed/picoclaw/pkg/gitworkspace"
	"github.com/sipeed/picoclaw/pkg/prworkspace"
	"github.com/sipeed/picoclaw/pkg/workflows"
)

type p015B2CAutomationHostileError struct {
	calls  atomic.Int32
	canary string
}

func (err *p015B2CAutomationHostileError) Error() string {
	err.calls.Add(1)
	panic(err.canary)
}

type p015B2CAutomationPruner struct {
	calls int
	count int64
	err   error
}

func (pruner *p015B2CAutomationPruner) Prune(
	context.Context,
	time.Time,
	int,
) (int64, error) {
	pruner.calls++
	return pruner.count, pruner.err
}

func TestP015B2CAutomationWorkerDoesNotInvokeHostileError(t *testing.T) {
	const workerCanary = "P015_B2C_WORKER_IDENTITY_RAW_CANARY"
	hostile := &p015B2CAutomationHostileError{canary: "P015_B2C_WORKER_ERROR_CANARY"}
	processCalls := 0
	records, raw := captureGatewaySafeRecords(t, func() {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		var workers sync.WaitGroup
		workers.Add(1)
		go runEventAutomationWorker(ctx, &workers, workerCanary, func(context.Context) (bool, error) {
			processCalls++
			return false, hostile
		})
		timer := time.AfterFunc(100*time.Millisecond, cancel)
		workers.Wait()
		_ = timer.Stop()
	})

	record := p015B2CRequireAutomationRecord(
		t,
		records,
		"eventing",
		"Event workflow worker iteration failed",
	)
	if processCalls != 1 {
		t.Fatalf("process calls = %d, want 1", processCalls)
	}
	if hostile.calls.Load() != 0 {
		t.Fatalf("hostile Error calls = %d, want 0", hostile.calls.Load())
	}
	if record["identity_worker_state"] != "complete" || record["error_class"] != "internal" {
		t.Fatalf("worker safe fields = %#v", record)
	}
	p015B2CRequireRawAbsent(t, raw, workerCanary, hostile.canary)
}

func TestP015B2CRetentionLoggingAndControlParity(t *testing.T) {
	const retentionDays = 30
	now := func() time.Time { return time.Date(2026, time.August, 28, 12, 0, 0, 0, time.UTC) }

	t.Run("runtime acquisition failure", func(t *testing.T) {
		hostile := &p015B2CAutomationHostileError{canary: "P015_B2C_RETENTION_ACQUIRE_ERROR"}
		pruner := &p015B2CAutomationPruner{}
		acquireCalls, releaseCalls := 0, 0
		records, raw := captureGatewaySafeRecords(t, func() {
			ctx, cancel := context.WithCancel(context.Background())
			var workers sync.WaitGroup
			workers.Add(1)
			runEventRetentionWorker(
				ctx,
				&workers,
				pruner,
				func(ctx context.Context) (context.Context, func(), error) {
					acquireCalls++
					if acquireCalls == 1 {
						return ctx, func() { releaseCalls++ }, hostile
					}
					cancel()
					return ctx, func() { releaseCalls++ }, context.Canceled
				},
				retentionDays,
				time.Nanosecond,
				now,
			)
			workers.Wait()
		})
		record := p015B2CRequireAutomationRecord(
			t, records, "eventing", "Event retention maintenance failed",
		)
		if hostile.calls.Load() != 0 || acquireCalls < 2 || releaseCalls != 0 || pruner.calls != 0 {
			t.Fatalf(
				"acquire/error parity = hostile:%d acquire:%d release:%d prune:%d",
				hostile.calls.Load(), acquireCalls, releaseCalls, pruner.calls,
			)
		}
		if record["error_class"] != "internal" {
			t.Fatalf("retention error fields = %#v", record)
		}
		p015B2CRequireRawAbsent(t, raw, hostile.canary)
	})

	t.Run("prune failure", func(t *testing.T) {
		hostile := &p015B2CAutomationHostileError{canary: "P015_B2C_RETENTION_PRUNE_ERROR"}
		pruner := &p015B2CAutomationPruner{err: hostile}
		acquireCalls, releaseCalls := 0, 0
		records, raw := captureGatewaySafeRecords(t, func() {
			ctx, cancel := context.WithCancel(context.Background())
			var workers sync.WaitGroup
			workers.Add(1)
			runEventRetentionWorker(
				ctx,
				&workers,
				pruner,
				func(ctx context.Context) (context.Context, func(), error) {
					acquireCalls++
					if acquireCalls == 1 {
						return ctx, func() { releaseCalls++ }, nil
					}
					cancel()
					return ctx, func() { releaseCalls++ }, context.Canceled
				},
				retentionDays,
				time.Nanosecond,
				now,
			)
			workers.Wait()
		})
		p015B2CRequireAutomationRecord(
			t, records, "eventing", "Event retention maintenance failed",
		)
		if hostile.calls.Load() != 0 || acquireCalls < 2 || releaseCalls != 1 || pruner.calls != 1 {
			t.Fatalf(
				"prune/error parity = hostile:%d acquire:%d release:%d prune:%d",
				hostile.calls.Load(), acquireCalls, releaseCalls, pruner.calls,
			)
		}
		p015B2CRequireRawAbsent(t, raw, hostile.canary)
	})

	t.Run("pruned count", func(t *testing.T) {
		pruner := &p015B2CAutomationPruner{count: 7}
		acquireCalls, releaseCalls := 0, 0
		records, _ := captureGatewaySafeRecords(t, func() {
			ctx, cancel := context.WithCancel(context.Background())
			var workers sync.WaitGroup
			workers.Add(1)
			runEventRetentionWorker(
				ctx,
				&workers,
				pruner,
				func(ctx context.Context) (context.Context, func(), error) {
					acquireCalls++
					if acquireCalls == 1 {
						return ctx, func() { releaseCalls++ }, nil
					}
					cancel()
					return ctx, func() { releaseCalls++ }, context.Canceled
				},
				retentionDays,
				time.Nanosecond,
				now,
			)
			workers.Wait()
		})
		record := p015B2CRequireAutomationRecord(
			t, records, "eventing", "Pruned expired durable events",
		)
		if record["count"] != float64(7) || acquireCalls < 2 ||
			releaseCalls != 1 || pruner.calls != 1 {
			t.Fatalf(
				"pruned count parity = record:%#v acquire:%d release:%d prune:%d",
				record, acquireCalls, releaseCalls, pruner.calls,
			)
		}
	})
}

type p015B2CAutomationPollRunner struct {
	calls   int
	cancel  context.CancelFunc
	failure error
}

func (runner *p015B2CAutomationPollRunner) RunTool(
	_ context.Context,
	_ workflows.ToolRequest,
) (map[string]any, error) {
	runner.calls++
	if runner.calls > 1 {
		runner.cancel()
		return nil, context.Canceled
	}
	if runner.failure != nil {
		return nil, runner.failure
	}
	notification := []map[string]any{{
		"id":         "p015-b2c-notification-1",
		"reason":     "mention",
		"unread":     true,
		"updated_at": "2026-08-28T12:00:00Z",
		"url":        "https://api.github.com/notifications/threads/1",
		"repository": map[string]any{
			"id": 123, "node_id": "R_123", "name": "PicoClaw",
			"full_name": "ScyllaDB/PicoClaw", "private": false,
			"html_url":       "https://github.com/ScyllaDB/PicoClaw",
			"url":            "https://api.github.com/repos/ScyllaDB/PicoClaw",
			"default_branch": "main",
			"owner":          map[string]any{"login": "ScyllaDB"},
		},
		"subject": map[string]any{
			"title": "Mentioned in an issue",
			"url":   "https://api.github.com/repos/ScyllaDB/PicoClaw/issues/9",
			"type":  "Issue",
		},
	}}
	data, err := json.Marshal(notification)
	if err != nil {
		return nil, err
	}
	return map[string]any{"text": string(data)}, nil
}

type p015B2CAutomationInserter struct {
	calls int
}

func (inserter *p015B2CAutomationInserter) Insert(
	context.Context,
	eventing.Envelope,
) (eventing.InsertResult, error) {
	inserter.calls++
	return eventing.InsertResult{Inserted: true}, nil
}

func TestP015B2CNotificationPollingSafeFieldsAndParity(t *testing.T) {
	t.Run("provider failure", func(t *testing.T) {
		const errorCanary = "P015_B2C_POLL_PROVIDER_ERROR_CANARY"
		runner := &p015B2CAutomationPollRunner{failure: errors.New(errorCanary)}
		inserter := &p015B2CAutomationInserter{}
		records, raw := p015B2CRunPollWorker(t, runner, inserter)
		record := p015B2CRequireAutomationRecord(
			t, records, "eventing", "GitHub notification polling failed",
		)
		if runner.calls < 2 || inserter.calls != 0 || record["error_class"] != "internal" {
			t.Fatalf("poll failure parity = calls:%d inserts:%d record:%#v", runner.calls, inserter.calls, record)
		}
		p015B2CRequireRawAbsent(t, raw, errorCanary)
	})

	t.Run("stored counts", func(t *testing.T) {
		runner := &p015B2CAutomationPollRunner{}
		inserter := &p015B2CAutomationInserter{}
		records, _ := p015B2CRunPollWorker(t, runner, inserter)
		record := p015B2CRequireAutomationRecord(
			t, records, "eventing", "Stored GitHub notifications",
		)
		if runner.calls < 2 || inserter.calls != 1 ||
			record["notification_count"] != float64(1) ||
			record["matched_count"] != float64(1) ||
			record["inserted_count"] != float64(1) {
			t.Fatalf("poll count parity = calls:%d inserts:%d record:%#v", runner.calls, inserter.calls, record)
		}
	})
}

func p015B2CRunPollWorker(
	t *testing.T,
	runner *p015B2CAutomationPollRunner,
	inserter *p015B2CAutomationInserter,
) ([]map[string]any, string) {
	t.Helper()
	var records []map[string]any
	var raw string
	records, raw = captureGatewaySafeRecords(t, func() {
		ctx, cancel := context.WithCancel(context.Background())
		runner.cancel = cancel
		poller, err := eventgithubpoll.New(eventgithubpoll.Config{
			Store: inserter, ToolRunner: runner,
			Connectors: []eventgithubpoll.Connector{{Name: "p015-b2c"}},
		})
		if err != nil {
			t.Fatalf("githubpoll.New() error = %v", err)
		}
		var workers sync.WaitGroup
		workers.Add(1)
		runGitHubNotificationPollWorker(ctx, &workers, poller, time.Nanosecond)
		workers.Wait()
	})
	return records, raw
}

func TestP015B2CPRRepairReturnsHostileAcquireErrorWithoutLoggingItRaw(t *testing.T) {
	const workspaceCanary = "P015_B2C_PR_WORKSPACE_IDENTITY_CANARY"
	hostile := &p015B2CAutomationHostileError{canary: "P015_B2C_PR_REPAIR_ERROR_CANARY"}
	runtime := &prWorkspaceImplementationRuntime{
		loop:    &agent.AgentLoop{},
		manager: &gitworkspace.Manager{},
		acquireRuntime: func(ctx context.Context) (context.Context, func(), error) {
			return ctx, func() {}, hostile
		},
	}
	request := prworkspace.RepairRequest{
		Context: prworkspace.PRContextBundle{
			WorkspaceID: workspaceCanary,
			Provider: prworkspace.ProviderSnapshot{
				ProviderOrigin: "https://github.com",
				HeadRepository: "owner/repository",
				HeadRef:        "feature",
				HeadSHA:        "0123456789abcdef",
			},
			Charter: prworkspace.Charter{Confirmed: true},
		},
		Attempt: 3,
	}

	var result prworkspace.RepairResult
	var repairErr error
	records, raw := captureGatewaySafeRecords(t, func() {
		result, repairErr = runtime.Repair(context.Background(), request)
	})
	if repairErr != hostile {
		t.Fatal("Repair() did not return the exact acquire error")
	}
	if !reflect.DeepEqual(result, prworkspace.RepairResult{}) {
		t.Fatalf("Repair() result = %#v, want zero", result)
	}
	if hostile.calls.Load() != 0 {
		t.Fatalf("hostile Error calls = %d, want 0", hostile.calls.Load())
	}
	record := p015B2CRequireAutomationRecord(
		t, records, "pr-workspace", "PR workspace repair failed",
	)
	if record["attempt"] != float64(3) ||
		record["identity_workspace_state"] != "complete" ||
		record["error_class"] != "internal" {
		t.Fatalf("repair safe fields = %#v", record)
	}
	p015B2CRequireRawAbsent(t, raw, workspaceCanary, hostile.canary)
}

func p015B2CRequireAutomationRecord(
	t *testing.T,
	records []map[string]any,
	component string,
	message string,
) map[string]any {
	t.Helper()
	if len(records) != 1 {
		t.Fatalf("record count = %d, want 1: %#v", len(records), records)
	}
	if records[0]["component"] != component || records[0]["message"] != message {
		t.Fatalf("record envelope = %#v", records[0])
	}
	return records[0]
}

func p015B2CRequireRawAbsent(t *testing.T, raw string, canaries ...string) {
	t.Helper()
	for _, canary := range canaries {
		if strings.Contains(raw, canary) {
			t.Fatalf("safe record contains raw canary %q: %s", canary, raw)
		}
	}
}
