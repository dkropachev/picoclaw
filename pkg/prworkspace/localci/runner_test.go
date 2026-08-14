package localci

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sipeed/picoclaw/pkg/gitworkspace"
)

type fakeSandbox struct {
	mu               sync.Mutex
	environment      string
	environmentErr   error
	status           Status
	environmentCalls int
	stepCalls        int
	runStepHook      func()
}

func (*fakeSandbox) localCISandbox() {}

func (sandbox *fakeSandbox) EnvironmentDigest(context.Context, Plan) (string, error) {
	sandbox.mu.Lock()
	defer sandbox.mu.Unlock()
	sandbox.environmentCalls++
	return sandbox.environment, sandbox.environmentErr
}

func (sandbox *fakeSandbox) RunStep(
	_ context.Context,
	_ string,
	step Step,
	_ Limits,
) (StepResult, error) {
	sandbox.mu.Lock()
	sandbox.stepCalls++
	status := sandbox.status
	hook := sandbox.runStepHook
	sandbox.runStepHook = nil
	sandbox.mu.Unlock()
	if hook != nil {
		hook()
	}
	if status == "" {
		status = StatusPassed
	}
	exitCode := 0
	if status != StatusPassed {
		exitCode = 1
	}
	return StepResult{
		StepID:       step.ID,
		Status:       status,
		ExitCode:     exitCode,
		OutputDigest: digestParts("picoclaw-local-ci-output-v1", nil),
	}, nil
}

func (sandbox *fakeSandbox) counts() (int, int) {
	sandbox.mu.Lock()
	defer sandbox.mu.Unlock()
	return sandbox.environmentCalls, sandbox.stepCalls
}

func (sandbox *fakeSandbox) PassingCacheAllowed() bool { return true }

func TestRunnerRunPinnedBindsManagerEvidence(t *testing.T) {
	fixture := newPinnedRunnerFixture(t, "run-pinned-success")
	store, err := OpenFileEvidenceStore(filepath.Join(t.TempDir(), "evidence"))
	if err != nil {
		t.Fatal(err)
	}
	runner := Runner{
		Sandbox:           &fakeSandbox{environment: strings.Repeat("e", 64)},
		Store:             store,
		allowTestBackends: true,
	}

	result, err := runner.RunPinned(context.Background(), fixture.manager, PinnedRunRequest{
		AttestationID: "lcatt_pinned_success",
		OwnerID:       "attempt_owner",
		Candidate:     fixture.validation,
	})
	if err != nil {
		t.Fatalf("RunPinned() error = %v", err)
	}
	if result.Execution.Status != StatusPassed || result.Attestation.Status != StatusPassed ||
		result.Execution.Evidence.Repository != fixture.repository ||
		result.Execution.Evidence.ParentCommit != fixture.validation.ExpectedParent ||
		result.Execution.Evidence.Tree != fixture.validation.ExpectedTree ||
		result.Execution.Evidence.CandidateDigest != fixture.validation.ExpectedCandidateDigest ||
		!validDigest(result.Execution.Evidence.ParentManifestDigest) ||
		!validDigest(result.Execution.Evidence.CandidateManifestDigest) {
		t.Fatalf("RunPinned() result = %#v", result)
	}
}

func TestRunnerRunPinnedRequiresExplicitExactNoChangeEvidence(t *testing.T) {
	ctx := context.Background()
	fixture := newPinnedRunnerFixture(t, "run-pinned-no-change")
	store, err := OpenFileEvidenceStore(filepath.Join(t.TempDir(), "evidence"))
	if err != nil {
		t.Fatal(err)
	}
	runner := Runner{
		Sandbox:           &fakeSandbox{environment: strings.Repeat("c", 64)},
		Store:             store,
		allowTestBackends: true,
	}
	changedDeclaredClean := fixture.validation
	changedDeclaredClean.NoChanges = true
	if _, runErr := runner.RunPinned(ctx, fixture.manager, PinnedRunRequest{
		AttestationID: "lcatt_changed_declared_clean",
		OwnerID:       "attempt_owner",
		Candidate:     changedDeclaredClean,
	}); runErr == nil || !errors.Is(runErr, gitworkspace.ErrPinnedCommitConflict) {
		t.Fatalf("RunPinned(changed declared clean) error = %v", runErr)
	}

	if removeErr := os.Remove(filepath.Join(fixture.workspace, "repair.txt")); removeErr != nil {
		t.Fatal(removeErr)
	}
	candidate, err := fixture.manager.SnapshotPinnedValidationCandidate(
		ctx,
		gitworkspace.PinnedCandidateRequest{
			Pin:         fixture.validation.Pin,
			WorkspaceID: fixture.validation.WorkspaceID,
		},
	)
	if err != nil {
		t.Fatalf("SnapshotPinnedValidationCandidate() error = %v", err)
	}
	validation := gitworkspace.PinnedCandidateValidationRequest{
		Pin:                     fixture.validation.Pin,
		WorkspaceID:             fixture.validation.WorkspaceID,
		ExpectedParent:          candidate.ParentCommit,
		ExpectedTree:            candidate.Tree,
		ExpectedCandidateDigest: candidate.CandidateDigest,
	}
	if _, runErr := runner.RunPinned(ctx, fixture.manager, PinnedRunRequest{
		AttestationID: "lcatt_no_change_implicit",
		OwnerID:       "attempt_owner",
		Candidate:     validation,
	}); runErr == nil || !errors.Is(runErr, gitworkspace.ErrPinnedCommitConflict) {
		t.Fatalf("RunPinned(implicit no-change) error = %v", runErr)
	}
	validation.NoChanges = true
	result, err := runner.RunPinned(ctx, fixture.manager, PinnedRunRequest{
		AttestationID: "lcatt_no_change_explicit",
		OwnerID:       "attempt_owner",
		Candidate:     validation,
	})
	if err != nil {
		t.Fatalf("RunPinned(explicit no-change) error = %v", err)
	}
	if candidate.ChangedFiles != 0 || result.Execution.Status != StatusPassed ||
		result.Execution.Evidence.Tree != candidate.Tree ||
		result.Execution.Evidence.ParentManifestDigest !=
			result.Execution.Evidence.CandidateManifestDigest {
		t.Fatalf("RunPinned(explicit no-change) result = %#v, candidate = %#v", result, candidate)
	}
}

func TestRunnerRunPinnedRejectsNonProductionSandbox(t *testing.T) {
	fixture := newPinnedRunnerFixture(t, "run-pinned-fake-rejected")
	store, err := OpenFileEvidenceStore(filepath.Join(t.TempDir(), "evidence"))
	if err != nil {
		t.Fatal(err)
	}
	sandbox := &fakeSandbox{environment: strings.Repeat("0", 64)}
	runner := Runner{Sandbox: sandbox, Store: store}

	_, err = runner.RunPinned(context.Background(), fixture.manager, PinnedRunRequest{
		AttestationID: "lcatt_fake_rejected",
		OwnerID:       "attempt_owner",
		Candidate:     fixture.validation,
	})
	if !errors.Is(err, ErrInvalid) {
		t.Fatalf("RunPinned(fake sandbox) error = %v, want invalid", err)
	}
	if environment, steps := sandbox.counts(); environment != 0 || steps != 0 {
		t.Fatalf("fake sandbox calls = (%d, %d), want zero", environment, steps)
	}
}

func TestRunnerRunPinnedDoesNotPersistGreenAfterWorkspacePostflightDrift(t *testing.T) {
	fixture := newPinnedRunnerFixture(t, "run-pinned-postflight-drift")
	evidenceRoot := filepath.Join(t.TempDir(), "evidence")
	store, err := OpenFileEvidenceStore(evidenceRoot)
	if err != nil {
		t.Fatal(err)
	}
	sandbox := &fakeSandbox{environment: strings.Repeat("f", 64)}
	sandbox.runStepHook = func() {
		if writeErr := os.WriteFile(
			filepath.Join(fixture.workspace, "postflight-drift.txt"),
			[]byte("unvalidated drift\n"),
			0o600,
		); writeErr != nil {
			t.Errorf("write postflight drift: %v", writeErr)
		}
	}
	runner := Runner{Sandbox: sandbox, Store: store, allowTestBackends: true}

	_, err = runner.RunPinned(context.Background(), fixture.manager, PinnedRunRequest{
		AttestationID: "lcatt_pinned_drift",
		OwnerID:       "attempt_owner",
		Candidate:     fixture.validation,
	})
	if err == nil {
		t.Fatal("RunPinned(postflight drift) error = nil")
	}
	if _, found, getErr := store.GetAttestation(context.Background(), "lcatt_pinned_drift"); getErr != nil || found {
		t.Fatalf("postflight-drift attestation found = %v, error = %v", found, getErr)
	}
	for _, directory := range []string{"plans", "discovery", "executions", "attestations", "cache"} {
		entries, readErr := os.ReadDir(filepath.Join(evidenceRoot, directory))
		if readErr != nil || len(entries) != 0 {
			t.Fatalf("evidence directory %s contains %d entries, error = %v", directory, len(entries), readErr)
		}
	}
}

func TestRunnerCachesOnlyExactPassingExecutionAndReplaysAttestation(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, root, ".picoclaw/ci.yml", testExplicitPlan)
	store, err := OpenFileEvidenceStore(filepath.Join(t.TempDir(), "evidence"))
	if err != nil {
		t.Fatal(err)
	}
	sandbox := &fakeSandbox{environment: strings.Repeat("a", 64), status: StatusPassed}
	now := time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC)
	runner := Runner{
		Sandbox: sandbox,
		Store:   store,
		Now:     func() time.Time { return now },
	}
	request := validRunRequest(root, "lcatt_first")
	first, err := runner.runMaterialized(context.Background(), request)
	if err != nil {
		t.Fatalf("Run(first) error = %v", err)
	}
	if first.Execution.Status != StatusPassed || first.Attestation.CacheHit {
		t.Fatalf("first result = %#v", first)
	}
	if _, calls := sandbox.counts(); calls != 1 {
		t.Fatalf("sandbox step calls = %d, want 1", calls)
	}

	request.AttestationID = "lcatt_second"
	second, err := runner.runMaterialized(context.Background(), request)
	if err != nil {
		t.Fatalf("Run(second) error = %v", err)
	}
	if !second.Attestation.CacheHit || second.Execution.Digest != first.Execution.Digest {
		t.Fatalf("second result = %#v, want exact cache hit", second)
	}
	if _, calls := sandbox.counts(); calls != 1 {
		t.Fatalf("sandbox step calls after cache = %d, want 1", calls)
	}

	replay, err := runner.runMaterialized(context.Background(), request)
	if err != nil {
		t.Fatalf("Run(replay) error = %v", err)
	}
	if replay.Attestation.Digest != second.Attestation.Digest {
		t.Fatalf("replay attestation = %#v, want %#v", replay.Attestation, second.Attestation)
	}
	if _, calls := sandbox.counts(); calls != 1 {
		t.Fatalf("sandbox step calls after replay = %d, want 1", calls)
	}
}

func TestRunnerDoesNotCacheFailure(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, root, ".picoclaw/ci.yml", testExplicitPlan)
	store, err := OpenFileEvidenceStore(filepath.Join(t.TempDir(), "evidence"))
	if err != nil {
		t.Fatal(err)
	}
	sandbox := &fakeSandbox{environment: strings.Repeat("b", 64), status: StatusFailed}
	runner := Runner{Sandbox: sandbox, Store: store}
	first := validRunRequest(root, "lcatt_failed_first")
	result, err := runner.runMaterialized(context.Background(), first)
	if err != nil || result.Execution.Status != StatusFailed {
		t.Fatalf("Run(failure) = (%#v, %v)", result, err)
	}
	second := first
	second.AttestationID = "lcatt_failed_second"
	result, err = runner.runMaterialized(context.Background(), second)
	if err != nil || result.Execution.Status != StatusFailed || result.Attestation.CacheHit {
		t.Fatalf("Run(second failure) = (%#v, %v)", result, err)
	}
	if _, calls := sandbox.counts(); calls != 2 {
		t.Fatalf("sandbox step calls = %d, want 2", calls)
	}
}

func TestRunnerPlanChangeAndIncompleteNeverExecute(t *testing.T) {
	baseline := t.TempDir()
	candidate := t.TempDir()
	writeTestFile(t, baseline, ".picoclaw/ci.yml", testExplicitPlan)
	writeTestFile(t, candidate, ".picoclaw/ci.yml", strings.Replace(testExplicitPlan, "true", "false", 1))
	store, err := OpenFileEvidenceStore(filepath.Join(t.TempDir(), "evidence"))
	if err != nil {
		t.Fatal(err)
	}
	sandbox := &fakeSandbox{environment: strings.Repeat("c", 64)}
	runner := Runner{Sandbox: sandbox, Store: store}
	request := validRunRequest(candidate, "lcatt_changed")
	request.ParentRoot = baseline
	result, err := runner.runMaterialized(context.Background(), request)
	if err != nil || result.Execution.Status != StatusPlanChanged {
		t.Fatalf("Run(plan changed) = (%#v, %v)", result, err)
	}
	if environment, steps := sandbox.counts(); environment != 0 || steps != 0 {
		t.Fatalf("sandbox calls = (%d, %d), want zero", environment, steps)
	}

	empty := t.TempDir()
	request = validRunRequest(empty, "lcatt_empty")
	request.ParentManifestDigest = strings.Repeat("6", 64)
	request.CandidateManifestDigest = strings.Repeat("7", 64)
	result, err = runner.runMaterialized(context.Background(), request)
	if err != nil || result.Execution.Status != StatusIncomplete {
		t.Fatalf("Run(incomplete) = (%#v, %v)", result, err)
	}
}

func TestRunnerEnvironmentFailureIsEvidenceNotPassingCache(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, root, ".picoclaw/ci.yml", testExplicitPlan)
	store, err := OpenFileEvidenceStore(filepath.Join(t.TempDir(), "evidence"))
	if err != nil {
		t.Fatal(err)
	}
	sandbox := &fakeSandbox{environmentErr: ErrSandboxUnavailable}
	runner := Runner{Sandbox: sandbox, Store: store}
	result, err := runner.runMaterialized(context.Background(), validRunRequest(root, "lcatt_unavailable"))
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if result.Execution.Status != StatusEnvironmentUnavailable || result.Attestation.CacheHit {
		t.Fatalf("result = %#v", result)
	}
	if _, steps := sandbox.counts(); steps != 0 {
		t.Fatalf("sandbox steps = %d, want zero", steps)
	}
}

func TestRunnerRejectsChangedAttestationReplay(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, root, ".picoclaw/ci.yml", testExplicitPlan)
	store, err := OpenFileEvidenceStore(filepath.Join(t.TempDir(), "evidence"))
	if err != nil {
		t.Fatal(err)
	}
	runner := Runner{
		Sandbox: &fakeSandbox{environment: strings.Repeat("d", 64)},
		Store:   store,
	}
	request := validRunRequest(root, "lcatt_conflict")
	if _, err = runner.runMaterialized(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	request.Tree = strings.Repeat("9", 40)
	if _, err = runner.runMaterialized(context.Background(), request); !errors.Is(err, ErrEvidenceConflict) {
		t.Fatalf("Run(changed replay) error = %v, want conflict", err)
	}
}

func validRunRequest(root, attestationID string) materializedRunRequest {
	return materializedRunRequest{
		AttestationID:           attestationID,
		OwnerID:                 "attempt_owner",
		Repository:              "github.com/example/repository",
		ParentCommit:            strings.Repeat("1", 40),
		Tree:                    strings.Repeat("2", 40),
		CandidateDigest:         strings.Repeat("3", 64),
		ParentManifestDigest:    strings.Repeat("4", 64),
		CandidateManifestDigest: strings.Repeat("5", 64),
		ParentRoot:              root,
		CandidateRoot:           root,
	}
}

const testExplicitPlan = `version: 1
steps:
  - id: focused
    name: Focused check
    kind: test
    command: ["true"]
    timeout-seconds: 30
`

type pinnedRunnerFixture struct {
	manager    *gitworkspace.Manager
	repository string
	workspace  string
	validation gitworkspace.PinnedCandidateValidationRequest
}

func newPinnedRunnerFixture(t *testing.T, reservation string) pinnedRunnerFixture {
	t.Helper()
	ctx := context.Background()
	repository := t.TempDir()
	runRunnerGit(t, repository, "init", "-b", "main")
	writeTestFile(t, repository, ".picoclaw/ci.yml", testExplicitPlan)
	writeTestFile(t, repository, "README.md", "# local CI fixture\n")
	runRunnerGit(t, repository, "add", ".")
	runRunnerGit(
		t,
		repository,
		"-c", "user.name=PicoClaw",
		"-c", "user.email=picoclaw@localhost",
		"commit", "-m", "initial",
	)
	parent := strings.TrimSpace(runRunnerGit(t, repository, "rev-parse", "HEAD^{commit}"))
	manager, err := gitworkspace.NewManager(gitworkspace.Options{
		RootDir: filepath.Join(t.TempDir(), "workspaces"),
	})
	if err != nil {
		t.Fatalf("NewManager() error = %v", err)
	}
	pin := gitworkspace.PinnedAcquireRequest{
		Repository:     repository,
		SourceRef:      "main",
		ExpectedCommit: parent,
		ReservationKey: "pr-workspace/" + reservation,
		AgentID:        "controller",
	}
	workspace, err := manager.AcquirePinned(ctx, pin)
	if err != nil {
		t.Fatalf("AcquirePinned() error = %v", err)
	}
	writeTestFile(t, workspace.Path, "repair.txt", "candidate change\n")
	candidate, err := manager.SnapshotPinnedCandidate(ctx, gitworkspace.PinnedCandidateRequest{
		Pin:         pin,
		WorkspaceID: workspace.ID,
	})
	if err != nil {
		t.Fatalf("SnapshotPinnedCandidate() error = %v", err)
	}
	return pinnedRunnerFixture{
		manager:    manager,
		repository: repository,
		workspace:  workspace.Path,
		validation: gitworkspace.PinnedCandidateValidationRequest{
			Pin:                     pin,
			WorkspaceID:             workspace.ID,
			ExpectedParent:          candidate.ParentCommit,
			ExpectedTree:            candidate.Tree,
			ExpectedCandidateDigest: candidate.CandidateDigest,
		},
	}
}

func runRunnerGit(t *testing.T, directory string, arguments ...string) string {
	t.Helper()
	command := exec.CommandContext(context.Background(), "git", arguments...)
	command.Dir = directory
	command.Env = append(
		os.Environ(),
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_TERMINAL_PROMPT=0",
		"GIT_AUTHOR_DATE=2026-08-09T12:00:00Z",
		"GIT_COMMITTER_DATE=2026-08-09T12:00:00Z",
	)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v: %s", strings.Join(arguments, " "), err, output)
	}
	return string(output)
}
