package gateway

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/sipeed/picoclaw/pkg/agent"
	"github.com/sipeed/picoclaw/pkg/gitworkspace"
	"github.com/sipeed/picoclaw/pkg/logger"
	"github.com/sipeed/picoclaw/pkg/prworkspace"
	"github.com/sipeed/picoclaw/pkg/prworkspace/localci"
	"github.com/sipeed/picoclaw/pkg/reviews"
)

type prWorkspaceCandidate struct {
	pin                gitworkspace.PinnedAcquireRequest
	candidate          gitworkspace.PinnedCandidate
	charter            prworkspace.Charter
	lineID             string
	lease              gitworkspace.PinnedLineLease
	parked             *gitworkspace.PinnedLineParkResult
	checkpointRevision prWorkspaceCandidateCheckpointRevision
}

type prWorkspaceCandidateKey struct {
	workspaceID string
	tree        string
}

// prWorkspaceImplementationRuntime adapts the existing pinned edit-only agent,
// content-addressed candidate, and sandboxed local-CI infrastructure. It never
// exposes a checkout path to the workspace domain or HTTP layer.
type prWorkspaceImplementationRuntime struct {
	loop           *agent.AgentLoop
	manager        *gitworkspace.Manager
	ci             *localci.Runner
	agentID        string
	acquireRuntime eventAutomationRuntimeAcquire
	checkpoints    *prWorkspaceCandidateCheckpointStore
	provider       *reviews.GitHubProvider

	mu         sync.Mutex
	candidates map[prWorkspaceCandidateKey]prWorkspaceCandidate
	active     map[string]prWorkspaceCandidate
}

func newPRWorkspaceImplementationRuntime(
	loop *agent.AgentLoop,
	ci *localci.Runner,
	agentID string,
	acquire eventAutomationRuntimeAcquire,
) (*prWorkspaceImplementationRuntime, error) {
	if loop == nil || ci == nil || agentID == "" {
		return nil, errors.New("PR workspace implementation runtime is incomplete")
	}
	manager, err := loop.ControllerGitWorkspaceManager()
	if err != nil {
		return nil, err
	}
	checkpoints, err := newPRWorkspaceCandidateCheckpointStore(
		filepath.Join(manager.RootDir(), ".pr-workspace-implementation", "active"),
	)
	if err != nil {
		return nil, err
	}
	return &prWorkspaceImplementationRuntime{
		loop: loop, manager: manager, ci: ci, agentID: agentID,
		acquireRuntime: acquire, checkpoints: checkpoints,
		candidates: make(map[prWorkspaceCandidateKey]prWorkspaceCandidate),
		active:     make(map[string]prWorkspaceCandidate),
	}, nil
}

func (runtime *prWorkspaceImplementationRuntime) Repair(
	ctx context.Context,
	request prworkspace.RepairRequest,
) (repairResult prworkspace.RepairResult, repairErr error) {
	defer func() {
		if repairErr != nil {
			logger.ErrorSafeCF(
				logger.ComponentPRWorkspace,
				logger.DiagnosticMessagePRWorkspaceRepairFailed,
				logger.NewSafeFields(
					gatewayDiagnosticWorkspaceField(request.Context.WorkspaceID),
					logger.SafeInt(logger.FieldAttempt, request.Attempt),
					gatewayDiagnosticErrorField(logger.ErrorClassInternal, repairErr),
				),
			)
		}
	}()
	provider := request.Context.Provider
	workspaceID := request.Context.WorkspaceID
	if runtime == nil || runtime.loop == nil || runtime.manager == nil ||
		workspaceID == "" || provider.HeadRepository == "" || provider.HeadRef == "" || provider.HeadSHA == "" ||
		!request.Context.Charter.Confirmed {
		return prworkspace.RepairResult{}, errors.New("PR workspace repair runtime is unavailable")
	}
	leaseCtx, release, err := runtime.acquire(ctx)
	if err != nil {
		return prworkspace.RepairResult{}, err
	}
	defer release()
	runner, err := runtime.loop.NewControllerLocalRepairRunnerWithRuntimeLease(
		leaseCtx,
		runtime.agentID,
		request.Instruction,
	)
	if err != nil {
		return prworkspace.RepairResult{}, err
	}
	pin := gitworkspace.PinnedAcquireRequest{
		Repository: strings.TrimSuffix(provider.ProviderOrigin, "/") + "/" + provider.HeadRepository + ".git",
		SourceRef:  provider.HeadRef, ExpectedCommit: provider.HeadSHA,
		ReservationKey: "pr-workspace:" + workspaceID, AgentID: runtime.agentID,
	}
	workspace, err := runtime.manager.AcquirePinned(leaseCtx, pin)
	if err != nil {
		return prworkspace.RepairResult{}, err
	}
	lineID := stablePRWorkspaceLineID(workspaceID)
	runtime.mu.Lock()
	active, continuing := runtime.active[workspaceID]
	runtime.mu.Unlock()
	var lineLease gitworkspace.PinnedLineLease
	var checkpointRevision prWorkspaceCandidateCheckpointRevision
	if continuing {
		if !samePRWorkspacePin(active.pin, pin) ||
			active.candidate.WorkspaceID != workspace.ID || active.lineID != lineID {
			return prworkspace.RepairResult{}, errors.New("PR workspace active repair identity changed")
		}
		current, snapshotErr := snapshotPRWorkspaceExpectedCandidate(
			leaseCtx, runtime.manager,
			gitworkspace.PinnedCandidateRequest{Pin: pin, WorkspaceID: workspace.ID},
			active.candidate.ChangedFiles,
		)
		if snapshotErr != nil {
			return prworkspace.RepairResult{}, snapshotErr
		}
		if current.ParentCommit != active.candidate.ParentCommit ||
			current.Tree != active.candidate.Tree ||
			current.CandidateDigest != active.candidate.CandidateDigest {
			return prworkspace.RepairResult{}, errors.New("PR workspace active repair candidate changed")
		}
		lineLease = active.lease
	} else {
		restored, found, restoreErr := runtime.restoreCheckpointedCandidate(
			leaseCtx, pin, workspace.ID, lineID, request.Context.Charter,
		)
		if restoreErr != nil {
			return prworkspace.RepairResult{}, restoreErr
		}
		checkpointRevision = restored.checkpointRevision
		if found {
			active, continuing, lineLease = restored, true, restored.lease
		}
	}
	if !continuing {
		baseCandidate, snapshotErr := snapshotPRWorkspaceRepairBaseline(
			leaseCtx,
			runtime.manager,
			gitworkspace.PinnedCandidateRequest{Pin: pin, WorkspaceID: workspace.ID},
		)
		if snapshotErr != nil {
			return prworkspace.RepairResult{}, snapshotErr
		}
		if baseCandidate.ParentCommit != pin.ExpectedCommit || baseCandidate.ChangedFiles != 0 {
			return prworkspace.RepairResult{}, errors.New("PR workspace checkout is not a clean provider candidate")
		}
		lineLease, err = runtime.manager.AdoptPinnedLine(leaseCtx, gitworkspace.PinnedLineAdoptRequest{
			Pin: pin, WorkspaceID: workspace.ID, LineID: lineID,
			ExpectedTree: baseCandidate.Tree,
		})
		if err != nil {
			return prworkspace.RepairResult{}, err
		}
		active = prWorkspaceCandidate{
			pin: pin, candidate: baseCandidate, charter: request.Context.Charter,
			lineID: lineID, lease: lineLease, checkpointRevision: checkpointRevision,
		}
		if err = runtime.saveCandidateCheckpoint(workspaceID, &active); err != nil {
			return prworkspace.RepairResult{}, err
		}
		runtime.mu.Lock()
		runtime.active[workspaceID] = active
		runtime.mu.Unlock()
	}
	capturePartialOnError := false
	defer func() {
		if repairErr == nil || !capturePartialOnError {
			return
		}
		captureCtx := context.Background()
		if ctx != nil {
			captureCtx = context.WithoutCancel(ctx)
		}
		captureCtx, cancel := context.WithTimeout(captureCtx, 30*time.Second)
		defer cancel()
		if captureErr := runtime.capturePartialRepairCandidate(
			captureCtx, workspaceID, active,
		); captureErr != nil {
			repairErr = errors.Join(repairErr, fmt.Errorf("retain partial PR workspace candidate: %w", captureErr))
		}
	}()
	contextJSON, err := json.Marshal(struct {
		SharedContext        prworkspace.PRContextBundle `json:"shared_context"`
		AuthorizedFindingIDs []string                    `json:"authorized_finding_ids"`
	}{request.Context, request.AuthorizedFindingIDs})
	if err != nil {
		return prworkspace.RepairResult{}, err
	}
	capturePartialOnError = true
	result, err := runner.Run(leaseCtx, agent.LocalRepairRequest{
		Pin: pin, Instruction: request.Instruction, Context: string(contextJSON),
	})
	repairResult = projectLocalRepairResult(result)
	if err != nil {
		return repairResult, err
	}
	candidate, err := runtime.manager.SnapshotPinnedCandidate(leaseCtx, gitworkspace.PinnedCandidateRequest{
		Pin: pin, WorkspaceID: result.WorkspaceID,
	})
	if err != nil {
		return repairResult, err
	}
	review, err := runtime.manager.SnapshotPinnedCandidateReview(
		leaseCtx,
		gitworkspace.PinnedCandidateValidationRequest{
			Pin: pin, WorkspaceID: candidate.WorkspaceID,
			ExpectedParent: candidate.ParentCommit, ExpectedTree: candidate.Tree,
			ExpectedCandidateDigest: candidate.CandidateDigest,
		},
	)
	if err != nil {
		return repairResult, err
	}
	storedCandidate := prWorkspaceCandidate{
		pin: pin, candidate: candidate, charter: request.Context.Charter,
		lineID: lineID, lease: lineLease,
	}
	storedCandidate.checkpointRevision = active.checkpointRevision
	if err = runtime.saveCandidateCheckpoint(workspaceID, &storedCandidate); err != nil {
		return repairResult, err
	}
	runtime.mu.Lock()
	if continuing {
		delete(runtime.candidates, prWorkspaceCandidateKey{workspaceID: workspaceID, tree: active.candidate.Tree})
	}
	runtime.candidates[prWorkspaceCandidateKey{workspaceID: workspaceID, tree: candidate.Tree}] = storedCandidate
	runtime.active[workspaceID] = storedCandidate
	runtime.mu.Unlock()
	capturePartialOnError = false
	repairResult.Summary, repairResult.WorkspaceID = result.Content, result.WorkspaceID
	repairResult.ChangedFiles = append([]string(nil), review.ChangedPaths...)
	repairResult.SemanticLines = semanticChangedLines(review.UnifiedDiff)
	repairResult.Modules = changedPathModules(review.ChangedPaths)
	repairResult.CandidateSHA = candidate.Tree
	repairResult.CandidateDiff = review.UnifiedDiff
	return repairResult, nil
}

func projectLocalRepairResult(value agent.LocalRepairResult) prworkspace.RepairResult {
	return prworkspace.RepairResult{
		Summary: value.Content, WorkspaceID: value.WorkspaceID,
		PromptDigest: value.PromptDigest, ProfileDigest: value.ProfileDigest,
		Usage:         localRepairTokenUsage(value.Metrics.Usage),
		UsageComplete: value.Metrics.Complete,
	}
}

func localRepairTokenUsage(value agent.LocalRepairUsage) prworkspace.TokenUsage {
	return prworkspace.TokenUsage{
		ProviderCalls: value.ProviderCalls, UsageReportedCalls: value.UsageReportedCalls,
		PromptTokens: value.PromptTokens, CachedTokens: value.CachedTokens,
		CompletionTokens: value.CompletionTokens, ReasoningTokens: value.ReasoningTokens,
		TotalTokens: value.TotalTokens, LatencyMillis: value.LatencyMillis,
	}
}

func (runtime *prWorkspaceImplementationRuntime) LoadPlanningEvidence(
	ctx context.Context,
	workspaceID string,
	provider prworkspace.ProviderSnapshot,
) (json.RawMessage, error) {
	if runtime == nil || runtime.manager == nil || workspaceID == "" ||
		provider.HeadRepository == "" || provider.HeadRef == "" || provider.HeadSHA == "" {
		return nil, errors.New("repository planning evidence is unavailable")
	}
	leaseCtx, releaseRuntime, err := runtime.acquire(ctx)
	if err != nil {
		return nil, err
	}
	defer releaseRuntime()
	pin := gitworkspace.PinnedAcquireRequest{
		Repository: strings.TrimSuffix(provider.ProviderOrigin, "/") + "/" + provider.HeadRepository + ".git",
		SourceRef:  provider.HeadRef, ExpectedCommit: provider.HeadSHA,
		ReservationKey: "pr-workspace:" + workspaceID, AgentID: runtime.agentID,
	}
	workspace, err := runtime.manager.AcquirePinned(leaseCtx, pin)
	if err != nil {
		return nil, err
	}
	evidence, snapshotErr := runtime.manager.SnapshotPinnedPlanningEvidence(
		leaseCtx, gitworkspace.PinnedCandidateRequest{Pin: pin, WorkspaceID: workspace.ID},
	)
	_, releaseErr := runtime.manager.ReleasePinned(leaseCtx, gitworkspace.PinnedReleaseRequest{
		ReservationKey: pin.ReservationKey, AgentID: pin.AgentID,
	})
	if snapshotErr != nil || releaseErr != nil {
		return nil, errors.Join(snapshotErr, releaseErr)
	}
	encoded, err := json.Marshal(evidence)
	if err != nil {
		return nil, err
	}
	return encoded, nil
}

func (runtime *prWorkspaceImplementationRuntime) capturePartialRepairCandidate(
	ctx context.Context,
	workspaceID string,
	active prWorkspaceCandidate,
) error {
	if runtime == nil || runtime.manager == nil || workspaceID == "" ||
		resultWorkspaceID(active) != workspaceID {
		return errors.New("PR workspace partial candidate identity is unavailable")
	}
	current, err := runtime.manager.SnapshotPinnedValidationCandidate(ctx, gitworkspace.PinnedCandidateRequest{
		Pin: active.pin, WorkspaceID: active.candidate.WorkspaceID,
	})
	if err != nil {
		return err
	}
	if current.WorkspaceID != active.candidate.WorkspaceID ||
		current.ParentCommit != active.candidate.ParentCommit {
		return errors.New("PR workspace partial candidate parent changed")
	}
	retained := active
	retained.candidate = current
	if err = runtime.saveCandidateCheckpoint(workspaceID, &retained); err != nil {
		return err
	}
	runtime.mu.Lock()
	delete(runtime.candidates, prWorkspaceCandidateKey{workspaceID: workspaceID, tree: active.candidate.Tree})
	if current.ChangedFiles > 0 {
		runtime.candidates[prWorkspaceCandidateKey{workspaceID: workspaceID, tree: current.Tree}] = retained
	}
	runtime.active[workspaceID] = retained
	runtime.mu.Unlock()
	return nil
}

type prWorkspaceRepairBaselineSnapshotter interface {
	SnapshotPinnedValidationCandidate(
		ctx context.Context,
		request gitworkspace.PinnedCandidateRequest,
	) (gitworkspace.PinnedCandidate, error)
}

// snapshotPRWorkspaceRepairBaseline permits the exact clean provider checkout
// that exists before the first repair adopts its durable development line.
// Later snapshots remain strict and require an actual candidate change.
func snapshotPRWorkspaceRepairBaseline(
	ctx context.Context,
	manager prWorkspaceRepairBaselineSnapshotter,
	request gitworkspace.PinnedCandidateRequest,
) (gitworkspace.PinnedCandidate, error) {
	return manager.SnapshotPinnedValidationCandidate(ctx, request)
}

func (runtime *prWorkspaceImplementationRuntime) Validate(
	ctx context.Context,
	request prworkspace.ValidationRequest,
) (prworkspace.ValidationRun, error) {
	candidate, ok := runtime.lookup(request.WorkspaceID, request.CandidateSHA)
	if runtime == nil || runtime.ci == nil || runtime.manager == nil || request.ID == "" || !ok {
		return prworkspace.ValidationRun{}, errors.New("PR workspace candidate is unavailable")
	}
	started := time.Now().UTC()
	result, err := runtime.ci.RunPinned(ctx, runtime.manager, localci.PinnedRunRequest{
		AttestationID: "lcatt:" + request.ID,
		OwnerID:       request.WorkspaceID,
		Candidate: gitworkspace.PinnedCandidateValidationRequest{
			Pin: candidate.pin, WorkspaceID: candidate.candidate.WorkspaceID,
			ExpectedParent: candidate.candidate.ParentCommit, ExpectedTree: candidate.candidate.Tree,
			ExpectedCandidateDigest: candidate.candidate.CandidateDigest,
		},
	})
	finished := time.Now().UTC()
	run := prworkspace.ValidationRun{
		ID: request.ID, State: prworkspace.ExecutionFailed, CandidateSHA: request.CandidateSHA,
		StartedAt: started, FinishedAt: &finished,
	}
	stepNames := make(map[string]string, len(result.Plan.Effective.Steps))
	for _, step := range result.Plan.Effective.Steps {
		stepNames[step.ID] = step.Name
	}
	for _, step := range result.Execution.Steps {
		exitCode := step.ExitCode
		name := strings.TrimSpace(stepNames[step.StepID])
		if name == "" {
			name = step.StepID
		}
		run.Checks = append(run.Checks, prworkspace.ValidationCheck{
			ID:     step.StepID,
			Name:   name,
			Status: string(step.Status),
			Summary: publicLocalCISummary(
				step.Output,
				step.Status,
			),
			ExitCode:   &exitCode,
			DurationMS: step.DurationMillis,
		})
	}
	if err == nil && result.Execution.Status == localci.StatusPassed {
		run.State = prworkspace.ExecutionSucceeded
	}
	if len(run.Checks) == 0 {
		status := string(result.Execution.Status)
		if status == "" {
			status = "failed"
		}
		run.Checks = []prworkspace.ValidationCheck{{ID: "local-ci", Name: "Local CI", Status: status}}
	}
	return run, err
}

func publicLocalCISummary(value string, status localci.Status) string {
	value = strings.ToValidUTF8(value, "�")
	value = strings.ReplaceAll(value, "\r\n", "\n")
	value = strings.ReplaceAll(value, "\r", "\n")
	value = strings.Map(func(character rune) rune {
		if character == '\n' || character == '\t' ||
			!unicode.IsControl(character) && !unicode.Is(unicode.Cf, character) {
			return character
		}
		return -1
	}, value)
	limit := 16 << 10
	if status == localci.StatusInfrastructureError || status == localci.StatusEnvironmentUnavailable {
		limit = 4 << 10
		if index := publicLocalCIStackStart(value); index >= 0 {
			value = value[:index]
		}
	}
	if len(value) <= limit {
		return strings.TrimSpace(value)
	}
	prefix := value[:limit]
	for !utf8.ValidString(prefix) {
		prefix = prefix[:len(prefix)-1]
	}
	return strings.TrimSpace(prefix) + "\n… output truncated …"
}

func publicLocalCIStackStart(value string) int {
	lineStart := 0
	for lineStart <= len(value) {
		lineEnd := strings.IndexByte(value[lineStart:], '\n')
		if lineEnd < 0 {
			lineEnd = len(value)
		} else {
			lineEnd += lineStart
		}
		line := strings.TrimLeft(value[lineStart:lineEnd], " \t")
		if strings.Contains(line, "runtime stack:") ||
			strings.Contains(line, "fatal error: newosproc") ||
			strings.Contains(line, "goroutine ") && strings.Contains(line, "[") {
			return lineStart
		}
		if lineEnd == len(value) {
			break
		}
		lineStart = lineEnd + 1
	}
	return -1
}

func (runtime *prWorkspaceImplementationRuntime) FinalizeRepair(
	ctx context.Context,
	workspaceID string,
	result prworkspace.RepairResult,
) (prworkspace.RepairResult, error) {
	candidate, ok := runtime.lookup(workspaceID, result.CandidateSHA)
	if !ok || resultWorkspaceID(candidate) != workspaceID || result.WorkspaceID != candidate.candidate.WorkspaceID {
		return result, errors.New("PR workspace candidate is unavailable")
	}
	committed, err := runtime.manager.CommitPinned(ctx, gitworkspace.PinnedCommitRequest{
		Pin: candidate.pin, WorkspaceID: candidate.candidate.WorkspaceID,
		IntentID:       stablePRWorkspaceCommitIntentID(workspaceID, result.CandidateSHA),
		ExpectedParent: candidate.candidate.ParentCommit, ExpectedTree: candidate.candidate.Tree,
		ExpectedCandidateDigest: candidate.candidate.CandidateDigest,
		Message:                 "PicoClaw: implement confirmed PR charter",
		AuthoredAt:              candidate.charter.CreatedAt.UTC().Truncate(time.Second),
	})
	if err != nil {
		return result, err
	}
	result.CandidateSHA = committed.Commit
	result.Summary = strings.TrimSpace(result.Summary)
	if result.Summary == "" {
		result.Summary = fmt.Sprintf("Created retained local commit %s", committed.Commit)
	}
	parked, parkErr := runtime.manager.ParkPinnedLine(ctx, gitworkspace.PinnedLineParkRequest{
		Pin: candidate.pin, WorkspaceID: candidate.candidate.WorkspaceID, LineID: candidate.lineID,
		IntentID:        "pr-workspace-park:" + result.CandidateSHA,
		ExpectedVersion: candidate.lease.Version, MutationEpoch: candidate.lease.MutationEpoch,
		PreviousTip: candidate.lease.Tip, Tip: committed.Commit, Tree: committed.Tree,
	})
	if parkErr != nil {
		return result, parkErr
	}
	result.PublicationFence = &prworkspace.ImplementationPublicationFence{
		GitWorkspaceID: candidate.candidate.WorkspaceID,
		LineID:         candidate.lineID, LineVersion: parked.Version,
		MutationEpoch: parked.MutationEpoch,
		ParkIntentID:  "pr-workspace-park:" + result.CandidateSHA,
		BaseCommit:    candidate.pin.ExpectedCommit, Tip: committed.Commit, Tree: committed.Tree,
	}
	candidate.parked = &parked
	if checkpointErr := runtime.saveFinalizedCandidateCheckpoint(
		workspaceID,
		&candidate,
		result.PublicationFence,
	); checkpointErr != nil {
		return result, checkpointErr
	}
	runtime.mu.Lock()
	delete(runtime.active, workspaceID)
	runtime.candidates[prWorkspaceCandidateKey{workspaceID: workspaceID, tree: candidate.candidate.Tree}] = candidate
	runtime.mu.Unlock()
	return result, nil
}

func (runtime *prWorkspaceImplementationRuntime) AcknowledgeFinalizedRepair(
	ctx context.Context,
	workspaceID string,
	result prworkspace.RepairResult,
) error {
	if runtime == nil || runtime.checkpoints == nil || result.PublicationFence == nil {
		return errors.New("PR workspace finalized candidate acknowledgement is unavailable")
	}
	fence := result.PublicationFence
	if result.CandidateSHA != fence.Tip {
		return errors.New("PR workspace finalized candidate acknowledgement changed")
	}
	checkpoint, revision, found, err := runtime.checkpoints.Load(workspaceID)
	if err != nil {
		return err
	}
	if !found {
		return nil
	}
	if checkpoint.State != prWorkspaceCandidateCheckpointParked || checkpoint.Fence == nil ||
		checkpoint.GitWorkspaceID != result.WorkspaceID || *checkpoint.Fence != *fence {
		return errors.New("PR workspace finalized candidate acknowledgement changed")
	}
	acknowledgeCtx := context.Background()
	if ctx != nil {
		acknowledgeCtx = context.WithoutCancel(ctx)
	}
	acknowledgeCtx, cancel := context.WithTimeout(acknowledgeCtx, 30*time.Second)
	defer cancel()
	review, err := runtime.manager.SnapshotPinnedLineReview(acknowledgeCtx, gitworkspace.PinnedLineReviewRequest{
		LineID: fence.LineID, ExpectedVersion: fence.LineVersion,
		ExpectedBase: fence.BaseCommit, ExpectedTip: fence.Tip, ExpectedTree: fence.Tree,
	})
	if err != nil || review.Version != fence.LineVersion || review.MutationEpoch != fence.MutationEpoch ||
		review.ParkIntentID != fence.ParkIntentID || review.BaseCommit != fence.BaseCommit ||
		review.Commit != fence.Tip || review.Tree != fence.Tree {
		if err == nil {
			err = errors.New("parked line evidence changed")
		}
		return fmt.Errorf("verify finalized PR workspace acknowledgement: %w", err)
	}
	if err := runtime.removeAcknowledgedCandidateCheckpoint(workspaceID, revision); err != nil {
		return err
	}
	runtime.mu.Lock()
	delete(runtime.candidates, prWorkspaceCandidateKey{workspaceID: workspaceID, tree: fence.Tree})
	runtime.mu.Unlock()
	return nil
}

func snapshotPRWorkspaceExpectedCandidate(
	ctx context.Context,
	manager *gitworkspace.Manager,
	request gitworkspace.PinnedCandidateRequest,
	expectedChangedFiles int,
) (gitworkspace.PinnedCandidate, error) {
	if expectedChangedFiles == 0 {
		return manager.SnapshotPinnedValidationCandidate(ctx, request)
	}
	return manager.SnapshotPinnedCandidate(ctx, request)
}

func (runtime *prWorkspaceImplementationRuntime) saveCandidateCheckpoint(
	workspaceID string,
	candidate *prWorkspaceCandidate,
) error {
	if runtime == nil || runtime.checkpoints == nil || candidate == nil {
		return errors.New("PR workspace candidate checkpoint store is unavailable")
	}
	checkpoint := prWorkspaceCandidateCheckpoint{
		Version: prWorkspaceCandidateCheckpointVersion, State: prWorkspaceCandidateCheckpointActive,
		WorkspaceID: workspaceID,
		Repository:  candidate.pin.Repository, SourceRef: candidate.pin.SourceRef,
		HeadSHA: candidate.pin.ExpectedCommit, CharterID: candidate.charter.ID,
		CharterHeadSHA: candidate.charter.HeadSHA,
		GitWorkspaceID: candidate.candidate.WorkspaceID, LineID: candidate.lineID,
		Lease: candidate.lease, Candidate: candidate.candidate,
	}
	revision, err := runtime.checkpoints.Save(checkpoint, candidate.checkpointRevision)
	if err != nil {
		return err
	}
	candidate.checkpointRevision = revision
	return nil
}

func (runtime *prWorkspaceImplementationRuntime) saveFinalizedCandidateCheckpoint(
	workspaceID string,
	candidate *prWorkspaceCandidate,
	fence *prworkspace.ImplementationPublicationFence,
) error {
	if runtime == nil || runtime.checkpoints == nil || candidate.parked == nil || fence == nil {
		return errors.New("PR workspace finalized candidate checkpoint is unavailable")
	}
	fenceCopy := *fence
	checkpoint := prWorkspaceCandidateCheckpoint{
		Version: prWorkspaceCandidateCheckpointVersion, State: prWorkspaceCandidateCheckpointParked,
		WorkspaceID: workspaceID,
		Repository:  candidate.pin.Repository, SourceRef: candidate.pin.SourceRef,
		HeadSHA: candidate.pin.ExpectedCommit, CharterID: candidate.charter.ID,
		CharterHeadSHA: candidate.charter.HeadSHA,
		GitWorkspaceID: candidate.candidate.WorkspaceID, LineID: candidate.lineID,
		Lease: candidate.lease, Candidate: candidate.candidate, Fence: &fenceCopy,
	}
	return runtime.saveCandidateCheckpointRevision(checkpoint, candidate)
}

func (runtime *prWorkspaceImplementationRuntime) saveCandidateCheckpointRevision(
	checkpoint prWorkspaceCandidateCheckpoint,
	candidate *prWorkspaceCandidate,
) error {
	if checkpoint.State != prWorkspaceCandidateCheckpointParked || checkpoint.Fence == nil {
		return errors.New("PR workspace finalized candidate checkpoint is invalid")
	}
	revision, err := runtime.checkpoints.Save(checkpoint, candidate.checkpointRevision)
	if errors.Is(err, errPRWorkspaceCandidateCheckpointConflict) {
		currentRevision, matched, reconcileErr := runtime.checkpoints.reconcileFinalized(
			checkpoint, candidate.checkpointRevision,
		)
		if reconcileErr != nil {
			return reconcileErr
		}
		if !matched {
			return err
		}
		revision, err = currentRevision, nil
	}
	if err != nil {
		return err
	}
	candidate.checkpointRevision = revision
	return nil
}

func (runtime *prWorkspaceImplementationRuntime) removeAcknowledgedCandidateCheckpoint(
	workspaceID string,
	revision prWorkspaceCandidateCheckpointRevision,
) error {
	err := runtime.checkpoints.Remove(workspaceID, revision)
	if !errors.Is(err, errPRWorkspaceCandidateCheckpointConflict) {
		return err
	}
	matches, matchErr := runtime.checkpoints.removalMatches(workspaceID, revision)
	if matchErr != nil {
		return matchErr
	}
	if matches {
		return nil
	}
	return err
}

func (runtime *prWorkspaceImplementationRuntime) restoreCheckpointedCandidate(
	ctx context.Context,
	pin gitworkspace.PinnedAcquireRequest,
	gitWorkspaceID, lineID string,
	charter prworkspace.Charter,
) (prWorkspaceCandidate, bool, error) {
	if runtime == nil || runtime.checkpoints == nil || runtime.manager == nil {
		return prWorkspaceCandidate{}, false, errors.New("PR workspace candidate restore is unavailable")
	}
	workspaceID := strings.TrimPrefix(pin.ReservationKey, "pr-workspace:")
	checkpoint, revision, found, err := runtime.checkpoints.Load(workspaceID)
	if err != nil || !found {
		return prWorkspaceCandidate{checkpointRevision: revision}, found, err
	}
	if checkpoint.Repository != pin.Repository || checkpoint.SourceRef != pin.SourceRef ||
		checkpoint.HeadSHA != pin.ExpectedCommit || checkpoint.CharterID != charter.ID ||
		checkpoint.CharterHeadSHA != charter.HeadSHA || checkpoint.GitWorkspaceID != gitWorkspaceID ||
		checkpoint.LineID != lineID {
		return prWorkspaceCandidate{}, false, errors.New("PR workspace candidate checkpoint context changed")
	}
	if checkpoint.State == prWorkspaceCandidateCheckpointParked {
		return prWorkspaceCandidate{}, false, errors.New(
			"PR workspace candidate is already finalized and awaits aggregate reconciliation",
		)
	}
	var lease gitworkspace.PinnedLineLease
	if checkpoint.Lease.Version == 0 {
		lease, err = runtime.manager.AdoptPinnedLine(ctx, gitworkspace.PinnedLineAdoptRequest{
			Pin: pin, WorkspaceID: gitWorkspaceID, LineID: lineID,
			ExpectedTree: checkpoint.Lease.Tree,
		})
	} else {
		lease, err = runtime.manager.ResumePinnedLine(ctx, gitworkspace.PinnedLineResumeRequest{
			Pin: pin, WorkspaceID: gitWorkspaceID, LineID: lineID,
			ExpectedVersion: checkpoint.Lease.Version,
			ExpectedEpoch:   checkpoint.Lease.MutationEpoch - 1,
			ExpectedTip:     checkpoint.Lease.Tip, ExpectedTree: checkpoint.Lease.Tree,
		})
	}
	if err != nil {
		return prWorkspaceCandidate{}, false, fmt.Errorf("reattach PR workspace candidate line: %w", err)
	}
	lease.AlreadyOwned = checkpoint.Lease.AlreadyOwned
	if lease != checkpoint.Lease {
		return prWorkspaceCandidate{}, false, errors.New("PR workspace candidate lease changed")
	}
	current, err := snapshotPRWorkspaceExpectedCandidate(
		ctx, runtime.manager,
		gitworkspace.PinnedCandidateRequest{Pin: pin, WorkspaceID: gitWorkspaceID},
		checkpoint.Candidate.ChangedFiles,
	)
	if err != nil {
		return prWorkspaceCandidate{}, false, err
	}
	if current != checkpoint.Candidate {
		return prWorkspaceCandidate{}, false, errors.New("PR workspace checkpointed candidate changed")
	}
	restored := prWorkspaceCandidate{
		pin: pin, candidate: current, charter: charter, lineID: lineID, lease: lease,
		checkpointRevision: revision,
	}
	runtime.mu.Lock()
	if current.ChangedFiles > 0 {
		runtime.candidates[prWorkspaceCandidateKey{workspaceID: workspaceID, tree: current.Tree}] = restored
	}
	runtime.active[workspaceID] = restored
	runtime.mu.Unlock()
	return restored, true, nil
}

func samePRWorkspacePin(left, right gitworkspace.PinnedAcquireRequest) bool {
	return left.Repository == right.Repository && left.SourceRef == right.SourceRef &&
		left.ExpectedCommit == right.ExpectedCommit &&
		left.ReservationKey == right.ReservationKey && left.AgentID == right.AgentID
}

func resultWorkspaceID(candidate prWorkspaceCandidate) string {
	return strings.TrimPrefix(candidate.pin.ReservationKey, "pr-workspace:")
}

func (runtime *prWorkspaceImplementationRuntime) lookup(workspaceID, tree string) (prWorkspaceCandidate, bool) {
	if runtime == nil {
		return prWorkspaceCandidate{}, false
	}
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	candidate, ok := runtime.candidates[prWorkspaceCandidateKey{workspaceID: workspaceID, tree: tree}]
	return candidate, ok
}

func (runtime *prWorkspaceImplementationRuntime) LoadCandidateEvidence(
	ctx context.Context,
	repair prworkspace.RepairAttempt,
) (prworkspace.CandidateEvidence, error) {
	if runtime == nil || runtime.manager == nil || repair.PublicationFence == nil || repair.CandidateSHA == "" {
		return prworkspace.CandidateEvidence{}, errors.New("PR workspace candidate evidence is unavailable")
	}
	fence := repair.PublicationFence
	if repair.CandidateSHA != fence.Tip || fence.LineVersion <= 0 || fence.MutationEpoch <= 0 {
		return prworkspace.CandidateEvidence{}, errors.New("PR workspace candidate evidence fence is invalid")
	}
	review, err := runtime.manager.SnapshotPinnedLineReview(ctx, gitworkspace.PinnedLineReviewRequest{
		LineID: fence.LineID, ExpectedVersion: fence.LineVersion,
		ExpectedBase: fence.BaseCommit, ExpectedTip: fence.Tip, ExpectedTree: fence.Tree,
	})
	if err != nil {
		return prworkspace.CandidateEvidence{}, err
	}
	if review.Version != fence.LineVersion || review.MutationEpoch != fence.MutationEpoch ||
		review.ParkIntentID != fence.ParkIntentID || review.BaseCommit != fence.BaseCommit ||
		review.Commit != fence.Tip || review.Tree != fence.Tree {
		return prworkspace.CandidateEvidence{}, errors.New("PR workspace candidate evidence fence changed")
	}
	return prworkspace.CandidateEvidence{
		CandidateSHA: repair.CandidateSHA, CandidateDiff: review.UnifiedDiff,
		Metrics: prworkspace.CandidateMetrics{
			Files:         len(review.ChangedPaths),
			SemanticLines: semanticChangedLines(review.UnifiedDiff),
			Modules: changedPathModules(
				review.ChangedPaths,
			),
			ChangedFiles: append([]string(nil), review.ChangedPaths...),
		},
		EvidenceDigest: review.ReviewDigest,
	}, nil
}

func (runtime *prWorkspaceImplementationRuntime) ListCodeTree(
	ctx context.Context,
	repair prworkspace.RepairAttempt,
	revision, path, after string,
) (prworkspace.CodeTreePage, error) {
	if runtime == nil || runtime.manager == nil || repair.PublicationFence == nil {
		return prworkspace.CodeTreePage{}, errors.New("candidate code browser is unavailable")
	}
	fence := repair.PublicationFence
	page, err := runtime.manager.ListPinnedLineTree(ctx, gitworkspace.PinnedLineTreeRequest{
		PinnedLineBrowseFence: gitworkspace.PinnedLineBrowseFence{
			LineID: fence.LineID, ExpectedVersion: fence.LineVersion,
			ExpectedBase: fence.BaseCommit, ExpectedTip: fence.Tip, ExpectedTree: fence.Tree,
		},
		Revision: revision, Path: path, After: after,
	})
	if err != nil {
		return prworkspace.CodeTreePage{}, err
	}
	result := prworkspace.CodeTreePage{Revision: repair.CandidateSHA, Path: path, Next: page.Next}
	for _, entry := range page.Entries {
		result.Entries = append(result.Entries, prworkspace.CodeTreeEntry{
			Name: filepath.Base(entry.Path), Path: entry.Path, Type: entry.Type,
		})
	}
	return result, nil
}

func (runtime *prWorkspaceImplementationRuntime) ReadCodeBlob(
	ctx context.Context,
	repair prworkspace.RepairAttempt,
	revision, path string,
) (prworkspace.CodeBlob, error) {
	if runtime == nil || runtime.manager == nil || repair.PublicationFence == nil {
		return prworkspace.CodeBlob{}, errors.New("candidate code browser is unavailable")
	}
	fence := repair.PublicationFence
	blob, err := runtime.manager.ReadPinnedLineBlob(ctx, gitworkspace.PinnedLineBlobRequest{
		PinnedLineBrowseFence: gitworkspace.PinnedLineBrowseFence{
			LineID: fence.LineID, ExpectedVersion: fence.LineVersion,
			ExpectedBase: fence.BaseCommit, ExpectedTip: fence.Tip, ExpectedTree: fence.Tree,
		},
		Revision: revision, Path: path,
	})
	if err != nil {
		return prworkspace.CodeBlob{}, err
	}
	return prworkspace.CodeBlob{Revision: repair.CandidateSHA, Path: blob.Path, Content: blob.Content}, nil
}

func (runtime *prWorkspaceImplementationRuntime) acquire(ctx context.Context) (context.Context, func(), error) {
	if runtime.acquireRuntime == nil {
		return ctx, func() {}, nil
	}
	return runtime.acquireRuntime(ctx)
}

func semanticChangedLines(diff string) int {
	count := 0
	inHunk := false
	for _, line := range strings.Split(diff, "\n") {
		if strings.HasPrefix(line, "diff --git ") {
			inHunk = false
			continue
		}
		if strings.HasPrefix(line, "@@") {
			inHunk = true
			continue
		}
		if inHunk && (strings.HasPrefix(line, "+") || strings.HasPrefix(line, "-")) {
			count++
		}
	}
	return count
}

func changedPathModules(paths []string) int {
	modules := make(map[string]struct{})
	for _, path := range paths {
		module, _, _ := strings.Cut(strings.TrimPrefix(path, "./"), "/")
		if module != "" {
			modules[module] = struct{}{}
		}
	}
	return len(modules)
}

func stablePRWorkspaceLineID(workspaceID string) string {
	value := strings.TrimPrefix(workspaceID, "devw_")
	if len(value) != 32 {
		return "pdln_00000000000000000000000000000000"
	}
	return "pdln_" + value
}

func stablePRWorkspaceCommitIntentID(workspaceID, tree string) string {
	digest := sha256.Sum256([]byte("picoclaw-pr-workspace-commit-intent-v1\x00" + workspaceID + "\x00" + tree))
	return "pdcmt_" + hex.EncodeToString(digest[:16])
}

func (runtime *prWorkspaceImplementationRuntime) PublishBranch(
	ctx context.Context,
	request prworkspace.BranchPublicationRequest,
) (prworkspace.BranchPublicationResult, error) {
	return runtime.publishOrReconcileBranch(ctx, request, true)
}

func (runtime *prWorkspaceImplementationRuntime) ReconcileBranch(
	ctx context.Context,
	request prworkspace.BranchPublicationRequest,
) (prworkspace.BranchPublicationResult, bool, error) {
	result, err := runtime.publishOrReconcileBranch(ctx, request, false)
	if err != nil {
		return result, false, err
	}
	found := result.ExternalID == request.Repair.CandidateSHA
	if request.Provider.Intent == prworkspace.IntentImplementFeature {
		found = result.ExternalID != "" && result.ExternalURL != ""
	}
	return result, found, nil
}

func (runtime *prWorkspaceImplementationRuntime) publishOrReconcileBranch(
	ctx context.Context,
	request prworkspace.BranchPublicationRequest,
	allowPRCreate bool,
) (prworkspace.BranchPublicationResult, error) {
	if runtime == nil || runtime.manager == nil || request.Repair.PublicationFence == nil ||
		request.Provider.HeadSHA == "" || request.Repair.CandidateSHA == "" {
		return prworkspace.BranchPublicationResult{}, errors.New("PR branch publisher is unavailable")
	}
	fence := request.Repair.PublicationFence
	pushRequest := gitworkspace.PinnedLinePushRequest{
		Repository: strings.TrimSuffix(
			request.Provider.ProviderOrigin,
			"/",
		) + "/" + request.Provider.HeadRepository + ".git",
		SourceRef:             request.Provider.HeadRef,
		ExpectedSourceCommit:  fence.BaseCommit,
		WorkspaceID:           fence.GitWorkspaceID,
		LineID:                fence.LineID,
		ExpectedVersion:       fence.LineVersion,
		ExpectedMutationEpoch: fence.MutationEpoch,
		ExpectedParkIntentID:  fence.ParkIntentID,
		ExpectedBase:          fence.BaseCommit,
		ExpectedTip:           fence.Tip,
		ExpectedTree:          fence.Tree,
		ExpectedRemoteTip:     request.Provider.HeadSHA,
	}
	if request.Provider.Intent == prworkspace.IntentImplementFeature {
		pushRequest.DestinationRef = developmentFeatureBranch(request.Provider)
		pushRequest.CreateDestination = true
	}
	result, err := runtime.manager.PushPinnedLine(ctx, pushRequest)
	publication := prworkspace.BranchPublicationResult{
		ExternalID: result.RemoteTip, ExternalURL: prWorkspacePullURL(request.Provider),
	}
	if request.Provider.Intent == prworkspace.IntentImplementFeature {
		publication.ExternalURL = ""
	}
	if result.RemoteTip == fence.Tip && request.Provider.Intent == prworkspace.IntentImplementFeature {
		if runtime.provider == nil || !request.Provider.CanCreatePullRequest {
			return prworkspace.BranchPublicationResult{}, errors.New("draft pull request publisher is unavailable")
		}
		pull, found, pullErr := runtime.findOrCreateDevelopmentPullRequest(
			ctx, request, pushRequest.DestinationRef, fence.Tip, allowPRCreate,
		)
		if pullErr != nil {
			if !errors.Is(pullErr, reviews.ErrWorkspaceProviderCallNotDispatched) {
				publication.Ambiguous = true
			}
			return publication, pullErr
		}
		if !found {
			return prworkspace.BranchPublicationResult{}, nil
		}
		return pull, nil
	}
	if result.RemoteTip == fence.Tip {
		return publication, nil
	}
	if errors.Is(err, gitworkspace.ErrPinnedLinePushOutcomeUnknown) {
		publication.Ambiguous = true
	}
	return publication, err
}

func developmentFeatureBranch(provider prworkspace.ProviderSnapshot) string {
	digest := sha256.Sum256(
		[]byte(provider.ProviderOrigin + "\x00" + provider.RepositoryID + "\x00" + provider.SourceID),
	)
	suffix := hex.EncodeToString(digest[:6])
	if provider.SourceKind == prworkspace.SourceIssue && provider.SourceNumber > 0 {
		return fmt.Sprintf("picoclaw/issue-%d-%s", provider.SourceNumber, suffix)
	}
	return "picoclaw/feature-" + suffix
}

func (runtime *prWorkspaceImplementationRuntime) findOrCreateDevelopmentPullRequest(
	ctx context.Context,
	request prworkspace.BranchPublicationRequest,
	branch, candidateSHA string,
	allowCreate bool,
) (prworkspace.BranchPublicationResult, bool, error) {
	provider := request.Provider
	marker := "<!-- picoclaw-development:" + provider.SourceID + ":" + candidateSHA + " -->"
	raw, err := runtime.provider.ListWorkspacePullRequestsJSON(ctx, provider.Repository, branch, provider.BaseRef)
	if err != nil {
		return prworkspace.BranchPublicationResult{}, false, err
	}
	pulls, err := decodeDevelopmentPullList(raw)
	if err != nil {
		return prworkspace.BranchPublicationResult{}, false, err
	}
	var matched []prWorkspaceGitHubPull
	for _, pull := range pulls {
		if strings.Contains(pull.Body, marker) && pull.Head != nil && pull.Base != nil &&
			pull.Head.Ref == branch && pull.Base.Ref == provider.BaseRef {
			if validationErr := validateDevelopmentPull(
				pull, provider, branch, candidateSHA, marker, developmentPullTitle(provider),
			); validationErr != nil {
				return prworkspace.BranchPublicationResult{}, false, validationErr
			}
			matched = append(matched, pull)
		}
	}
	if len(matched) > 1 {
		return prworkspace.BranchPublicationResult{}, false, errors.New(
			"multiple draft pull requests match development marker",
		)
	}
	if len(matched) == 1 {
		return developmentPullResult(matched[0], branch, candidateSHA), true, nil
	}
	if !allowCreate {
		return prworkspace.BranchPublicationResult{}, false, nil
	}
	owner, repo, ok := strings.Cut(provider.Repository, "/")
	if !ok {
		return prworkspace.BranchPublicationResult{}, false, errors.New("development repository is invalid")
	}
	title := developmentPullTitle(provider)
	body := developmentPullBody(provider, request.Repair, request.Validation, marker)
	raw, err = runtime.provider.CreateWorkspacePullRequestJSON(ctx, map[string]any{
		"owner": owner, "repo": repo, "title": title, "body": body,
		"head": branch, "base": provider.BaseRef, "draft": true, "maintainer_can_modify": true,
	})
	if err != nil {
		return prworkspace.BranchPublicationResult{}, false, err
	}
	var pull prWorkspaceGitHubPull
	if err := json.Unmarshal(raw, &pull); err != nil {
		return prworkspace.BranchPublicationResult{}, false, errors.New("created pull request response is invalid")
	}
	if err := validateDevelopmentPull(pull, provider, branch, candidateSHA, marker, title); err != nil {
		return prworkspace.BranchPublicationResult{}, false, err
	}
	return developmentPullResult(pull, branch, candidateSHA), true, nil
}

func validateDevelopmentPull(
	pull prWorkspaceGitHubPull,
	provider prworkspace.ProviderSnapshot,
	branch, candidateSHA, marker, expectedTitle string,
) error {
	if pull.Number < 1 || githubPositiveNumericID(pull.ID) == "" ||
		!samePRWorkspacePullURL(
			pull.HTMLURL, provider.ProviderOrigin, provider.Repository, pull.Number,
		) || !strings.EqualFold(strings.TrimSpace(pull.State), "open") || pull.Merged || !pull.Draft ||
		pull.Head == nil || pull.Base == nil || pull.Head.Repo == nil || pull.Base.Repo == nil ||
		pull.Head.Ref != branch || pull.Head.SHA != candidateSHA || pull.Base.Ref != provider.BaseRef ||
		pull.Base.SHA != provider.BaseSHA ||
		!strings.EqualFold(pull.Head.Repo.FullName, provider.Repository) ||
		!strings.EqualFold(pull.Base.Repo.FullName, provider.Repository) ||
		githubPositiveNumericID(pull.Head.Repo.ID) != provider.RepositoryID ||
		githubPositiveNumericID(pull.Base.Repo.ID) != provider.RepositoryID ||
		!samePRWorkspaceRepositoryURL(pull.Head.Repo.HTMLURL, provider.ProviderOrigin, provider.Repository) ||
		!samePRWorkspaceRepositoryURL(pull.Base.Repo.HTMLURL, provider.ProviderOrigin, provider.Repository) ||
		strings.TrimSpace(pull.Title) != expectedTitle || !strings.Contains(pull.Body, marker) {
		return errors.New("development pull request authority is invalid")
	}
	return nil
}

func developmentPullTitle(provider prworkspace.ProviderSnapshot) string {
	value := strings.TrimSpace(provider.Title)
	if provider.SourceKind == prworkspace.SourceBrief || value == "" ||
		strings.EqualFold(value, "feature brief") {
		value = strings.TrimSpace(provider.Body)
		if line, _, ok := strings.Cut(value, "\n"); ok {
			value = strings.TrimSpace(line)
		}
	}
	value = boundedDevelopmentMarkdown(value, 120)
	if value == "" {
		return "Implement requested feature"
	}
	return value
}

func developmentPullBody(
	provider prworkspace.ProviderSnapshot,
	repair prworkspace.RepairAttempt,
	validation prworkspace.ValidationRun,
	marker string,
) string {
	source := strings.TrimSpace(provider.Body)
	if before, _, ok := strings.Cut(source, "\n\nProvider issue comments (untrusted evidence):"); ok {
		source = strings.TrimSpace(before)
	}
	source = boundedDevelopmentMarkdown(source, 4<<10)
	if source == "" {
		source = "No additional source description was provided."
	}
	var builder strings.Builder
	builder.WriteString("## Source\n\n")
	builder.WriteString(source)
	if provider.SourceKind == prworkspace.SourceIssue && provider.SourceNumber > 0 {
		fmt.Fprintf(&builder, "\n\nCloses #%d", provider.SourceNumber)
	}
	builder.WriteString("\n\n## Changes\n\n")
	if summary := boundedDevelopmentMarkdown(repair.ResultSummary, 2<<10); summary != "" {
		builder.WriteString(summary)
	} else {
		builder.WriteString("Implemented the confirmed development charter.")
	}
	if len(repair.ChangedFiles) > 0 {
		builder.WriteString("\n\nChanged files:\n")
		for index, path := range repair.ChangedFiles {
			if index == 50 {
				builder.WriteString("- … additional files omitted\n")
				break
			}
			fmt.Fprintf(&builder, "- `%s`\n", strings.ReplaceAll(path, "`", ""))
		}
	}
	builder.WriteString("\n## Validation\n\n")
	if len(validation.Checks) == 0 {
		builder.WriteString("- Validation completed successfully.\n")
	} else {
		for index, check := range validation.Checks {
			if index == 50 {
				builder.WriteString("- … additional checks omitted\n")
				break
			}
			fmt.Fprintf(&builder, "- `%s`: %s", strings.ReplaceAll(check.Name, "`", ""), check.Status)
			if summary := boundedDevelopmentMarkdown(check.Summary, 512); summary != "" {
				fmt.Fprintf(&builder, " — %s", summary)
			}
			builder.WriteByte('\n')
		}
	}
	return boundedDevelopmentMarkdown(builder.String(), 22<<10) + "\n\n" + marker
}

func boundedDevelopmentMarkdown(value string, limit int) string {
	value = strings.TrimSpace(value)
	if limit < 1 || len(value) <= limit {
		return value
	}
	value = value[:limit]
	for !utf8.ValidString(value) {
		value = value[:len(value)-1]
	}
	return strings.TrimSpace(value) + "…"
}

func decodeDevelopmentPullList(raw []byte) ([]prWorkspaceGitHubPull, error) {
	var pulls []prWorkspaceGitHubPull
	if err := json.Unmarshal(raw, &pulls); err == nil {
		if len(pulls) > 100 {
			return nil, errors.New("pull request list response exceeds bound")
		}
		return pulls, nil
	}
	var envelope struct {
		Items []prWorkspaceGitHubPull `json:"items"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return nil, errors.New("pull request list response is invalid")
	}
	if len(envelope.Items) > 100 {
		return nil, errors.New("pull request list response exceeds bound")
	}
	return envelope.Items, nil
}

func developmentPullResult(
	pull prWorkspaceGitHubPull,
	branch, candidateSHA string,
) prworkspace.BranchPublicationResult {
	id := githubScalarID(pull.ID)
	if id == "" {
		id = strconv.FormatInt(pull.Number, 10)
	}
	return prworkspace.BranchPublicationResult{
		ExternalID: id, ExternalURL: pull.HTMLURL, PullRequestID: id,
		PullNumber: pull.Number, HeadRef: branch, HeadSHA: candidateSHA,
	}
}

var (
	_ prworkspace.RepairExecutor          = (*prWorkspaceImplementationRuntime)(nil)
	_ prworkspace.RepairFinalizer         = (*prWorkspaceImplementationRuntime)(nil)
	_ prworkspace.ValidationExecutor      = (*prWorkspaceImplementationRuntime)(nil)
	_ prworkspace.BranchPublisher         = (*prWorkspaceImplementationRuntime)(nil)
	_ prworkspace.CandidateEvidenceLoader = (*prWorkspaceImplementationRuntime)(nil)
	_ prworkspace.CandidateCodeBrowser    = (*prWorkspaceImplementationRuntime)(nil)
	_ prworkspace.PlanningEvidenceLoader  = (*prWorkspaceImplementationRuntime)(nil)
)
