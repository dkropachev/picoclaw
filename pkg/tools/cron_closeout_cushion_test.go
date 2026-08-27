package tools

import (
	"context"
	"strings"
	"testing"

	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/cron"
)

func TestCronCloseoutCushionSchemasAndDispatch(t *testing.T) {
	tool := newTestCronTool(t)
	if tool.Name() != "cron" || tool.Description() == "" || len(tool.Parameters()) == 0 {
		t.Fatalf("cron descriptor = %q/%q/%#v", tool.Name(), tool.Description(), tool.Parameters())
	}
	for _, args := range []map[string]any{
		{},
		{"action": 1},
		{"action": "unknown"},
	} {
		if result := tool.Execute(context.Background(), args); result == nil || !result.IsError {
			t.Fatalf("invalid dispatch result = %#v", result)
		}
	}
}

func TestCronCloseoutCushionArgumentParsers(t *testing.T) {
	if _, result := requiredCronJobID(nil, "get"); result == nil || !result.IsError {
		t.Fatalf("missing required job ID = %#v", result)
	}
	if id, result := requiredCronJobID(map[string]any{"job_id": "id"}, "get"); id != "id" || result != nil {
		t.Fatalf("required job ID = %q, %#v", id, result)
	}
	if _, present, result := optionalString(nil, "value"); present || result != nil {
		t.Fatalf("absent optional string = %v, %#v", present, result)
	}
	if _, _, result := optionalString(map[string]any{"value": 1}, "value"); result == nil {
		t.Fatal("non-string optional value accepted")
	}
	if text, present, result := optionalString(
		map[string]any{"value": ""}, "value",
	); text != "" || !present || result != nil {
		t.Fatalf("empty optional string = %q, %v, %#v", text, present, result)
	}
	if _, _, result := optionalNonEmptyString(
		map[string]any{"value": "   "}, "value",
	); result == nil {
		t.Fatal("blank non-empty string accepted")
	}
	if _, _, result := optionalNonEmptyString(
		map[string]any{"value": 1}, "value",
	); result == nil {
		t.Fatal("wrong-type non-empty string accepted")
	}

	validSeconds := []any{float64(3), int(4), int64(5)}
	for _, value := range validSeconds {
		seconds, result := positiveSeconds(map[string]any{"seconds": value}, "seconds")
		if seconds <= 0 || result != nil {
			t.Fatalf("positive seconds %T = %d, %#v", value, seconds, result)
		}
	}
	for _, value := range []any{float64(1.5), 0, int64(-1), "1", nil} {
		if _, result := positiveSeconds(map[string]any{"seconds": value}, "seconds"); result == nil {
			t.Fatalf("invalid seconds %T accepted", value)
		}
	}
}

func TestCronCloseoutCushionSchedulePatchMatrix(t *testing.T) {
	tests := []struct {
		args    map[string]any
		kind    string
		present bool
		bad     bool
	}{
		{args: nil},
		{args: map[string]any{"at_seconds": int64(2)}, kind: "at", present: true},
		{args: map[string]any{"every_seconds": int(3)}, kind: "every", present: true},
		{args: map[string]any{"cron_expr": "0 1 * * *"}, kind: "cron", present: true},
		{args: map[string]any{"cron_expr": 1}, bad: true},
		{args: map[string]any{"cron_expr": " "}, bad: true},
		{args: map[string]any{"at_seconds": 1, "every_seconds": 2}, bad: true},
		{args: map[string]any{"at_seconds": 0}, bad: true},
	}
	for index, test := range tests {
		schedule, present, result := schedulePatch(test.args)
		if test.bad {
			if result == nil {
				t.Fatalf("invalid schedule %d accepted", index)
			}
			continue
		}
		if result != nil || present != test.present || schedule.Kind != test.kind {
			t.Fatalf("schedule %d = %+v, %v, %#v", index, schedule, present, result)
		}
	}
}

func TestCronCloseoutCushionRemoteAndAccessRules(t *testing.T) {
	for _, test := range []struct {
		channel string
		chatID  string
		allow   []string
		want    bool
	}{
		{"", "chat", []string{"*"}, false},
		{"telegram", "chat", []string{"", "other", "telegram"}, true},
		{"telegram", "chat", []string{"telegram:chat"}, true},
		{"telegram", "chat", []string{"*"}, true},
		{"telegram", "chat", []string{"telegram:other"}, false},
	} {
		if got := isCommandAllowedRemote(test.channel, test.chatID, test.allow); got != test.want {
			t.Fatalf("remote rule %+v = %v", test, got)
		}
	}
	tool := &CronTool{commandAllowedRemotes: []string{"telegram:chat"}}
	job := &cron.CronJob{}
	job.Payload.Channel = "telegram"
	job.Payload.To = "chat"
	if tool.canAccessJob(context.Background(), job) {
		t.Fatal("contextless job access succeeded")
	}
	ctx := WithToolContext(context.Background(), "telegram", "chat")
	if !tool.canAccessJob(ctx, job) {
		t.Fatal("owned reminder was inaccessible")
	}
	job.Payload.Command = "echo ok"
	if !tool.canAccessJob(ctx, job) {
		t.Fatal("allowlisted command was inaccessible")
	}
	job.Payload.To = "other"
	if tool.canAccessJob(ctx, job) {
		t.Fatal("foreign job was accessible")
	}
	if !tool.canAccessJob(WithToolContext(context.Background(), "cli", "x"), job) {
		t.Fatal("internal channel could not access job")
	}
}

func TestCronCloseoutCushionCommandMutationGates(t *testing.T) {
	ctx := WithToolContext(context.Background(), "telegram", "chat")
	for _, test := range []struct {
		tool *CronTool
		args map[string]any
		bad  bool
	}{
		{tool: &CronTool{}, bad: true},
		{tool: &CronTool{execEnabled: true, allowCommand: true}, bad: true},
		{
			tool: &CronTool{execEnabled: true, commandAllowedRemotes: []string{"telegram:chat"}},
			bad:  true,
		},
		{
			tool: &CronTool{execEnabled: true, commandAllowedRemotes: []string{"telegram:chat"}},
			args: map[string]any{"command_confirm": true},
		},
	} {
		result := test.tool.validateCommandMutation(ctx, test.args)
		if test.bad != (result != nil) {
			t.Fatalf("command gate %+v = %#v", test, result)
		}
	}
}

func TestCronCloseoutCushionManagementErrorsAndSchedules(t *testing.T) {
	tool := newTestCronTool(t)
	ctx := WithToolContext(context.Background(), "cli", "direct")
	for _, args := range []map[string]any{
		{"action": "get"},
		{"action": "get", "job_id": "missing"},
		{"action": "update"},
		{"action": "update", "job_id": "missing"},
		{"action": "remove"},
		{"action": "remove", "job_id": "missing"},
		{"action": "enable"},
		{"action": "enable", "job_id": "missing"},
	} {
		if result := tool.Execute(ctx, args); result == nil || !result.IsError {
			t.Fatalf("management error %#v = %#v", args, result)
		}
	}
	everyMS := int64(2_000)
	atMS := int64(1)
	for _, schedule := range []cron.CronSchedule{
		{Kind: "every", EveryMS: &everyMS},
		{Kind: "cron", Expr: "0 1 * * *"},
		{Kind: "at", AtMS: &atMS},
		{Kind: "unknown"},
	} {
		if _, err := tool.cronService.AddJob(
			"job-"+schedule.Kind, schedule, "message", "cli", "direct",
		); err != nil {
			t.Fatal(err)
		}
	}
	result := tool.listJobs(ctx)
	if result.IsError || !strings.Contains(result.ForLLM, "unknown") {
		t.Fatalf("mixed schedule list = %#v", result)
	}
}

func TestCronCloseoutCushionConstructionDefaults(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Tools.Exec.Enabled = false
	tool := newTestCronToolWithConfig(t, cfg)
	if tool.execTool != nil || tool.execEnabled {
		t.Fatalf("disabled exec construction = %#v", tool.execTool)
	}
}

func TestCronCloseoutCushionAddScheduleBranches(t *testing.T) {
	tool := newTestCronTool(t)
	ctx := WithToolContext(context.Background(), "cli", "direct")
	for _, args := range []map[string]any{
		{"action": "add", "at_seconds": float64(1)},
		{"action": "add", "message": "missing schedule"},
		{"action": "add", "message": "every", "every_seconds": float64(2)},
		{"action": "add", "message": "cron", "cron_expr": "0 1 * * *"},
		{"action": "add", "message": "zeros", "at_seconds": float64(0), "every_seconds": float64(0)},
	} {
		result := tool.Execute(ctx, args)
		wantError := args["message"] == nil || args["message"] == "missing schedule" ||
			args["message"] == "zeros"
		if result.IsError != wantError {
			t.Fatalf("add branch %#v = %#v", args, result)
		}
	}
}

func TestCronCloseoutCushionUpdateFieldBranches(t *testing.T) {
	tool := newTestCronTool(t)
	ctx := WithToolContext(context.Background(), "cli", "direct")
	job := addTestCronJob(t, tool, "job", "cli", "direct", "")
	for _, args := range []map[string]any{
		{"action": "update", "job_id": job.ID, "name": 1},
		{"action": "update", "job_id": job.ID, "name": "new name"},
		{"action": "update", "job_id": job.ID, "message": 1},
		{"action": "update", "job_id": job.ID, "message": "new message"},
		{"action": "update", "job_id": job.ID, "every_seconds": "bad"},
		{"action": "update", "job_id": job.ID, "command": 1},
	} {
		result := tool.Execute(ctx, args)
		if result == nil {
			t.Fatalf("nil update result for %#v", args)
		}
	}
}

func TestCronCloseoutCushionRuntimeLeaseAndDefaults(t *testing.T) {
	executor := &guardedStubJobExecutor{stubJobExecutor: &stubJobExecutor{response: "done"}}
	tool := newTestCronToolWithExecutorAndConfig(t, executor, config.DefaultConfig())
	job := &cron.CronJob{ID: "default", Payload: cron.CronPayload{Message: "run"}}
	if result := tool.ExecuteJob(context.Background(), job); result != "ok" || !executor.released {
		t.Fatalf("default runtime execution = %q, released=%v", result, executor.released)
	}
	if executor.lastChan != "cli" || executor.lastChatID != "direct" {
		t.Fatalf("default destination = %q/%q", executor.lastChan, executor.lastChatID)
	}
}
