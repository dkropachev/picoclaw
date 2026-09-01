package workflows

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func TestAdmitWorkflowDevelopmentTestRunPersistsOnlyFinalCandidate(t *testing.T) {
	workspace, original, activePath, before := newDevelopmentAdmissionFixture(t)
	targetRef := "workflows/existing.yml"
	targetData := []byte(GenerateWorkflowDraftYAML("existing target"))
	if err := os.WriteFile(
		filepath.Join(workspace, targetRef),
		targetData,
		0o600,
	); err != nil {
		t.Fatalf("os.WriteFile(target) error = %v", err)
	}
	candidateYAML := GenerateWorkflowDraftYAML("candidate workflow")
	admission := developmentTestRunAdmissionFor(original)
	admission.Prompt = "  revised prompt  "
	admission.TargetWorkflowRef = targetRef
	admission.YAML = candidateYAML
	admission.RunID = "wr_admitted"

	callbacks := 0
	session, recorded, started, err := AdmitWorkflowDevelopmentTestRun(
		workspace,
		admission,
		func() (string, error) {
			callbacks++
			assertDevelopmentAdmissionBytes(
				t,
				activePath,
				before,
				"before durable start",
			)
			assertWorkflowMutationLockHeld(t, workspace)
			return "durable-run", nil
		},
	)
	if err != nil || !recorded || started != "durable-run" {
		t.Fatalf(
			"AdmitWorkflowDevelopmentTestRun() recorded=%v started=%q error=%v",
			recorded,
			started,
			err,
		)
	}
	if callbacks != 1 {
		t.Fatalf("start callbacks = %d, want 1", callbacks)
	}
	if session == nil ||
		session.Prompt != "revised prompt" ||
		session.TargetWorkflowRef != targetRef ||
		session.YAML != candidateYAML ||
		session.BaseTargetRevision != workflowContentRevision(targetData) ||
		session.DraftRevision != WorkflowDevelopmentDraftRevision(
			targetRef,
			candidateYAML,
		) ||
		session.Validation == nil ||
		!session.Validation.Valid ||
		session.Status != WorkflowDevelopmentStatusTesting ||
		session.LastTest == nil ||
		session.LastTest.RunID != admission.RunID ||
		session.LastTest.Status != RunStatusRunning ||
		session.LastTest.DraftRevision != session.DraftRevision {
		t.Fatalf("admitted session = %#v", session)
	}
	persisted, err := GetWorkflowDevelopmentSession(workspace)
	if err != nil {
		t.Fatalf("GetWorkflowDevelopmentSession() error = %v", err)
	}
	if persisted == nil ||
		persisted.SessionRevision != session.SessionRevision ||
		persisted.LastTest == nil ||
		persisted.LastTest.RunID != admission.RunID ||
		persisted.LastTest.Status != RunStatusRunning {
		t.Fatalf("persisted session = %#v", persisted)
	}
}

func TestAdmitWorkflowDevelopmentTestRunPromptRules(t *testing.T) {
	tests := []struct {
		name       string
		prompt     string
		wantPrompt string
	}{
		{
			name:       "nonblank prompt is trimmed",
			prompt:     "  replacement prompt  ",
			wantPrompt: "replacement prompt",
		},
		{
			name:       "blank prompt preserves current prompt",
			prompt:     " \t ",
			wantPrompt: "original prompt",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			workspace, original, _, _ := newDevelopmentAdmissionFixture(t)
			admission := developmentTestRunAdmissionFor(original)
			admission.Prompt = test.prompt
			session, recorded, _, err := AdmitWorkflowDevelopmentTestRun(
				workspace,
				admission,
				func() (struct{}, error) {
					return struct{}{}, nil
				},
			)
			if err != nil || !recorded {
				t.Fatalf(
					"AdmitWorkflowDevelopmentTestRun() recorded=%v error=%v",
					recorded,
					err,
				)
			}
			if session.Prompt != test.wantPrompt {
				t.Fatalf(
					"session prompt = %q, want %q",
					session.Prompt,
					test.wantPrompt,
				)
			}
		})
	}
}

func TestAdmitWorkflowDevelopmentTestRunRejectsStaleFenceWithoutStartOrWrite(
	t *testing.T,
) {
	workspace, original, activePath, before := newDevelopmentAdmissionFixture(t)
	exact := developmentTestRunAdmissionFor(original)
	tests := []struct {
		name   string
		mutate func(*WorkflowDevelopmentTestRunAdmission)
	}{
		{
			name: "session ID",
			mutate: func(admission *WorkflowDevelopmentTestRunAdmission) {
				admission.SessionID = "dev_stale"
			},
		},
		{
			name: "session revision",
			mutate: func(admission *WorkflowDevelopmentTestRunAdmission) {
				admission.ExpectedSessionRevision = "sha256:stale"
			},
		},
		{
			name: "draft revision",
			mutate: func(admission *WorkflowDevelopmentTestRunAdmission) {
				admission.ExpectedDraftRevision = "sha256:stale"
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			admission := exact
			test.mutate(&admission)
			callbacks := 0
			session, recorded, _, err := AdmitWorkflowDevelopmentTestRun(
				workspace,
				admission,
				func() (struct{}, error) {
					callbacks++
					return struct{}{}, nil
				},
			)
			if !errors.Is(err, ErrWorkflowDevelopmentFenceMismatch) ||
				recorded ||
				session == nil ||
				session.ID != original.ID ||
				callbacks != 0 {
				t.Fatalf(
					"stale admission session=%#v recorded=%v callbacks=%d error=%v",
					session,
					recorded,
					callbacks,
					err,
				)
			}
			assertDevelopmentAdmissionBytes(
				t,
				activePath,
				before,
				"after stale fence",
			)
		})
	}
}

func TestAdmitWorkflowDevelopmentTestRunRejectsInvalidCandidateWithoutWrite(
	t *testing.T,
) {
	workspace, original, activePath, before := newDevelopmentAdmissionFixture(t)
	admission := developmentTestRunAdmissionFor(original)
	admission.YAML = "name: [invalid\n"
	callbacks := 0
	session, recorded, _, err := AdmitWorkflowDevelopmentTestRun(
		workspace,
		admission,
		func() (struct{}, error) {
			callbacks++
			return struct{}{}, nil
		},
	)
	if !errors.Is(err, ErrWorkflowDevelopmentDraftNotReady) ||
		recorded ||
		callbacks != 0 ||
		session == nil ||
		session.Validation == nil ||
		session.Validation.Valid {
		t.Fatalf(
			"invalid admission session=%#v recorded=%v callbacks=%d error=%v",
			session,
			recorded,
			callbacks,
			err,
		)
	}
	assertDevelopmentAdmissionBytes(
		t,
		activePath,
		before,
		"after invalid candidate",
	)
}

func TestAdmitWorkflowDevelopmentTestRunStartErrorLeavesDraftUntouched(
	t *testing.T,
) {
	workspace, original, activePath, before := newDevelopmentAdmissionFixture(t)
	admission := developmentTestRunAdmissionFor(original)
	admission.Prompt = "candidate prompt"
	admission.TargetWorkflowRef = "workflows/candidate.yml"
	admission.YAML = GenerateWorkflowDraftYAML("candidate")
	startErr := errors.New("config generation changed")

	session, recorded, started, err := AdmitWorkflowDevelopmentTestRun(
		workspace,
		admission,
		func() (string, error) {
			assertDevelopmentAdmissionBytes(
				t,
				activePath,
				before,
				"inside failed start",
			)
			return "attempted-start", startErr
		},
	)
	if !errors.Is(err, startErr) ||
		recorded ||
		started != "attempted-start" ||
		session == nil ||
		session == original ||
		session.SessionRevision != original.SessionRevision ||
		session.DraftRevision != original.DraftRevision ||
		session.Prompt != original.Prompt ||
		session.TargetWorkflowRef != original.TargetWorkflowRef ||
		session.YAML != original.YAML ||
		session.LastTest != nil {
		t.Fatalf(
			"failed start session=%#v recorded=%v started=%q error=%v",
			session,
			recorded,
			started,
			err,
		)
	}
	assertDevelopmentAdmissionBytes(
		t,
		activePath,
		before,
		"after failed start",
	)
}

func TestAdmitWorkflowDevelopmentTestRunReturnsDurableStartOnSQLiteWriteFailure(
	t *testing.T,
) {
	workspace, original, activePath, before := newDevelopmentAdmissionFixture(t)
	admission := developmentTestRunAdmissionFor(original)
	var sabotageDB *sql.DB
	var releaseSabotageDB func()
	triggerCreated := false
	t.Cleanup(func() {
		if sabotageDB == nil {
			return
		}
		if triggerCreated {
			_, _ = sabotageDB.ExecContext(
				context.Background(),
				`DROP TRIGGER workflow_development_admission_write_failure`,
			)
		}
		if releaseSabotageDB != nil {
			releaseSabotageDB()
		}
	})

	session, recorded, started, err := AdmitWorkflowDevelopmentTestRun(
		workspace,
		admission,
		func() (string, error) {
			assertDevelopmentAdmissionBytes(
				t,
				activePath,
				before,
				"before SQLite write failure",
			)
			var openErr error
			sabotageDB, releaseSabotageDB, openErr = borrowWorkflowDatabase(
				t.Context(),
				workspace,
			)
			if openErr != nil {
				t.Fatalf("borrow workflow database: %v", openErr)
			}
			if _, createErr := sabotageDB.ExecContext(
				t.Context(),
				`CREATE TRIGGER workflow_development_admission_write_failure
				 BEFORE UPDATE ON workflow_development_sessions
				 BEGIN SELECT RAISE(ABORT, 'injected development write failure'); END`,
			); createErr != nil {
				t.Fatalf("create development write-failure trigger: %v", createErr)
			}
			triggerCreated = true
			return "durable-start", nil
		},
	)
	if err == nil || recorded || started != "durable-start" ||
		session == nil || session == original ||
		session.SessionRevision != original.SessionRevision ||
		session.DraftRevision != original.DraftRevision ||
		session.TargetWorkflowRef != original.TargetWorkflowRef ||
		session.YAML != original.YAML || session.LastTest != nil {
		t.Fatalf(
			"write failure session=%#v recorded=%v started=%q error=%v",
			session,
			recorded,
			started,
			err,
		)
	}
	if _, restoreErr := sabotageDB.ExecContext(
		t.Context(),
		`DROP TRIGGER workflow_development_admission_write_failure`,
	); restoreErr != nil {
		t.Fatalf("drop development write-failure trigger: %v", restoreErr)
	}
	triggerCreated = false
	if releaseSabotageDB != nil {
		releaseSabotageDB()
		releaseSabotageDB = nil
	}
	sabotageDB = nil
	assertDevelopmentAdmissionBytes(
		t,
		activePath,
		before,
		"after failed SQLite persistence",
	)
}

func TestAdmitWorkflowDevelopmentEventRunBlocksCompletionUntilClaimed(
	t *testing.T,
) {
	workspace, original, _, _ := newDevelopmentAdmissionFixture(t)
	const (
		eventID = "ev_0123456789abcdef0123456789abcdef"
		runID   = "wr_event_admitted"
	)
	admission := developmentTestRunAdmissionFor(original)
	admission.EventID = eventID
	admission.RunID = runID
	draftKey := WorkflowDevelopmentDraftKey(
		admission.TargetWorkflowRef,
		admission.YAML,
	)
	type completionResult struct {
		session  *WorkflowDevelopmentSession
		recorded bool
		err      error
	}
	completionStarted := make(chan struct{})
	completed := make(chan completionResult, 1)

	running, recorded, _, err := AdmitWorkflowDevelopmentTestRun(
		workspace,
		admission,
		func() (struct{}, error) {
			assertWorkflowMutationLockHeld(t, workspace)
			go func() {
				close(completionStarted)
				session, applied, completionErr := RecordWorkflowDevelopmentEventTestIfCurrent(
					workspace,
					original.ID,
					draftKey,
					eventID,
					runID,
					&RunResult{
						RunID:  runID,
						Status: RunStatusFailed,
						Error:  eventDraftPrivateDiagnostic,
					},
					errors.New(eventDraftPrivateDiagnostic),
				)
				completed <- completionResult{
					session:  session,
					recorded: applied,
					err:      completionErr,
				}
			}()
			<-completionStarted
			select {
			case result := <-completed:
				t.Fatalf(
					"completion overtook admission: %#v",
					result,
				)
			default:
			}
			return struct{}{}, nil
		},
	)
	if err != nil || !recorded ||
		running == nil ||
		running.LastTest == nil ||
		running.LastTest.EventID != eventID ||
		running.LastTest.RunID != runID ||
		running.LastTest.Status != RunStatusRunning {
		t.Fatalf(
			"running admission session=%#v recorded=%v error=%v",
			running,
			recorded,
			err,
		)
	}
	select {
	case completion := <-completed:
		if completion.err != nil ||
			!completion.recorded ||
			completion.session == nil ||
			completion.session.LastTest == nil ||
			completion.session.LastTest.EventID != eventID ||
			completion.session.LastTest.Status != RunStatusFailed ||
			completion.session.LastTest.Error !=
				EventBackedDraftTestFailureDiagnostic {
			t.Fatalf("completion = %#v", completion)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("terminal completion remained blocked after admission")
	}
}

func TestAdmitWorkflowDevelopmentTestRunRejectsRunningTest(t *testing.T) {
	workspace, _, activePath, _ := newDevelopmentAdmissionFixture(t)
	running, err := RecordWorkflowDevelopmentTest(
		workspace,
		&RunResult{RunID: "wr_existing", Status: RunStatusRunning},
		nil,
	)
	if err != nil {
		t.Fatalf("RecordWorkflowDevelopmentTest() error = %v", err)
	}
	before := readDevelopmentAdmissionSnapshot(t, activePath)
	admission := developmentTestRunAdmissionFor(running)
	callbacks := 0
	session, recorded, _, err := AdmitWorkflowDevelopmentTestRun(
		workspace,
		admission,
		func() (struct{}, error) {
			callbacks++
			return struct{}{}, nil
		},
	)
	if !errors.Is(err, ErrDevelopmentBusy) ||
		recorded ||
		callbacks != 0 ||
		session == nil ||
		session.LastTest == nil ||
		session.LastTest.RunID != "wr_existing" {
		t.Fatalf(
			"busy admission session=%#v recorded=%v callbacks=%d error=%v",
			session,
			recorded,
			callbacks,
			err,
		)
	}
	assertDevelopmentAdmissionBytes(
		t,
		activePath,
		before,
		"after busy admission",
	)
}

func newDevelopmentAdmissionFixture(
	t *testing.T,
) (
	workspace string,
	session *WorkflowDevelopmentSession,
	activePath string,
	activeBytes []byte,
) {
	t.Helper()
	workspace = t.TempDir()
	if err := os.MkdirAll(
		filepath.Join(workspace, DefaultDefinitionsDir),
		0o755,
	); err != nil {
		t.Fatalf("os.MkdirAll(workflows) error = %v", err)
	}
	var err error
	session, err = StartWorkflowDevelopment(
		context.Background(),
		workspace,
		RuntimeCompatibility{},
		WorkflowDevelopmentStartRequest{
			Reason:    WorkflowDevelopmentReasonNew,
			Prompt:    "original prompt",
			TargetRef: "workflows/original.yml",
		},
	)
	if err != nil {
		t.Fatalf("StartWorkflowDevelopment() error = %v", err)
	}
	activePath = workspace
	activeBytes = readDevelopmentAdmissionSnapshot(t, workspace)
	return workspace, session, activePath, activeBytes
}

func developmentTestRunAdmissionFor(
	session *WorkflowDevelopmentSession,
) WorkflowDevelopmentTestRunAdmission {
	return WorkflowDevelopmentTestRunAdmission{
		SessionID:               session.ID,
		ExpectedSessionRevision: session.SessionRevision,
		ExpectedDraftRevision:   session.DraftRevision,
		Prompt:                  session.Prompt,
		TargetWorkflowRef:       session.TargetWorkflowRef,
		YAML:                    session.YAML,
		RunID:                   "wr_admission",
	}
}

func assertDevelopmentAdmissionBytes(
	t *testing.T,
	activePath string,
	want []byte,
	when string,
) {
	t.Helper()
	got := readDevelopmentAdmissionSnapshot(t, activePath)
	if string(got) != string(want) {
		t.Fatalf("active development bytes changed %s", when)
	}
}

func readDevelopmentAdmissionSnapshot(t *testing.T, workspace string) []byte {
	t.Helper()
	db, err := openWorkflowDatabase(t.Context(), workspace)
	if err != nil {
		t.Fatalf("open workflow database: %v", err)
	}
	defer db.Close()
	session, err := loadWorkflowDevelopmentSession(t.Context(), db, "active")
	if err != nil {
		t.Fatalf("load workflow development snapshot: %v", err)
	}
	data, err := json.Marshal(session)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func assertWorkflowMutationLockHeld(t *testing.T, workspace string) {
	t.Helper()
	key, err := canonicalWorkflowWorkspace(workspace)
	if err != nil {
		t.Fatalf("canonicalWorkflowWorkspace() error = %v", err)
	}
	value, ok := workflowMutationLocks.Load(key)
	if !ok {
		t.Fatal("workflow mutation mutex is missing during admission")
	}
	mutex, ok := value.(*sync.Mutex)
	if !ok {
		t.Fatalf("workflow mutation mutex has type %T", value)
	}
	if mutex.TryLock() {
		mutex.Unlock()
		t.Fatal("start callback ran without the workflow mutation lock")
	}
}
