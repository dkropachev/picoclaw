package localci

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/sipeed/picoclaw/pkg/gitworkspace"
)

type materializedRunRequest struct {
	AttestationID           string
	OwnerID                 string
	Repository              string
	ParentCommit            string
	Tree                    string
	CandidateDigest         string
	ParentManifestDigest    string
	CandidateManifestDigest string
	ParentRoot              string
	CandidateRoot           string
}

type PinnedRunRequest struct {
	AttestationID string
	OwnerID       string
	Candidate     gitworkspace.PinnedCandidateValidationRequest
}

type RunResult struct {
	Plan        ResolvedPlan
	Execution   Execution
	Attestation Attestation
}

type Runner struct {
	Sandbox Sandbox
	Store   EvidenceStore
	Limits  Limits
	Now     func() time.Time

	allowTestBackends bool
}

type preparedRun struct {
	request      materializedRunRequest
	result       RunResult
	discoveryKey string
	cacheHit     bool
	persistPlan  bool
	promote      bool
	persist      bool
}

// RunPinned is the only exported execution entrypoint. It derives every Git,
// manifest, repository, and root field from the Git-workspace callback and
// delays cache promotion plus attempt attestation until its detached cleanup
// and postflight have succeeded.
func (runner *Runner) RunPinned(
	ctx context.Context,
	manager *gitworkspace.Manager,
	request PinnedRunRequest,
) (RunResult, error) {
	if runner == nil || runner.Sandbox == nil || runner.Store == nil || manager == nil {
		return RunResult{}, fmt.Errorf("%w: pinned local CI runner is unavailable", ErrInvalid)
	}
	if !runner.allowTestBackends &&
		(!isProductionSandbox(runner.Sandbox) || !isProductionEvidenceStore(runner.Store)) {
		return RunResult{}, fmt.Errorf(
			"%w: pinned local CI requires package-owned sandbox and evidence backends",
			ErrInvalid,
		)
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if !localCIIDPattern.MatchString(request.AttestationID) ||
		!localCIIDPattern.MatchString(request.OwnerID) {
		return RunResult{}, fmt.Errorf("%w: invalid local CI owner identity", ErrInvalid)
	}
	var prepared preparedRun
	called := false
	err := manager.WithPinnedCandidateValidationRoots(
		ctx,
		request.Candidate,
		func(operationCtx context.Context, roots gitworkspace.PinnedCandidateValidationRoots) error {
			if called {
				return fmt.Errorf("%w: candidate materializer called local CI more than once", ErrInvalid)
			}
			called = true
			if roots.CandidateManifest.Tree != request.Candidate.ExpectedTree ||
				!validObjectID(roots.ParentManifest.Tree) ||
				!validDigest(roots.ParentManifest.Digest) ||
				!validDigest(roots.CandidateManifest.Digest) ||
				strings.TrimSpace(roots.Repository) != roots.Repository || roots.Repository == "" {
				return fmt.Errorf("%w: candidate materializer returned mismatched evidence", ErrInvalid)
			}
			var prepareErr error
			prepared, prepareErr = runner.prepareMaterialized(operationCtx, materializedRunRequest{
				AttestationID:           request.AttestationID,
				OwnerID:                 request.OwnerID,
				Repository:              roots.Repository,
				ParentCommit:            request.Candidate.ExpectedParent,
				Tree:                    request.Candidate.ExpectedTree,
				CandidateDigest:         request.Candidate.ExpectedCandidateDigest,
				ParentManifestDigest:    roots.ParentManifest.Digest,
				CandidateManifestDigest: roots.CandidateManifest.Digest,
				ParentRoot:              roots.ParentRoot,
				CandidateRoot:           roots.CandidateRoot,
			})
			return prepareErr
		},
	)
	if err != nil {
		return RunResult{}, err
	}
	if !called {
		return RunResult{}, fmt.Errorf("%w: candidate materializer did not lend validation roots", ErrInvalid)
	}
	return runner.finishPrepared(ctx, prepared)
}

func (runner *Runner) runMaterialized(
	ctx context.Context,
	request materializedRunRequest,
) (RunResult, error) {
	prepared, err := runner.prepareMaterialized(ctx, request)
	if err != nil {
		return RunResult{}, err
	}
	return runner.finishPrepared(ctx, prepared)
}

func (runner *Runner) prepareMaterialized(
	ctx context.Context,
	request materializedRunRequest,
) (preparedRun, error) {
	if runner == nil || runner.Sandbox == nil || runner.Store == nil {
		return preparedRun{}, fmt.Errorf("%w: local CI runner is unavailable", ErrInvalid)
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if !localCIIDPattern.MatchString(request.AttestationID) ||
		!localCIIDPattern.MatchString(request.OwnerID) {
		return preparedRun{}, fmt.Errorf("%w: invalid local CI owner identity", ErrInvalid)
	}
	discoveryKey, err := discoveryCacheKey(
		request.ParentManifestDigest,
		request.CandidateManifestDigest,
	)
	if err != nil {
		return preparedRun{}, err
	}
	resolved, found, err := runner.Store.GetResolvedPlan(ctx, discoveryKey)
	if err != nil {
		return preparedRun{}, err
	}
	if !found {
		resolved, err = DiscoverPair(ctx, request.ParentRoot, request.CandidateRoot)
		if err != nil {
			return preparedRun{}, err
		}
	}
	persistPlan := !found
	status := StatusPassed
	environmentDigest := ""
	switch {
	case resolved.Changed:
		status = StatusPlanChanged
	case !resolved.Effective.Complete:
		status = StatusIncomplete
	default:
		environmentDigest, err = runner.Sandbox.EnvironmentDigest(ctx, resolved.Effective)
		if err != nil {
			if ctxErr := ctx.Err(); ctxErr != nil {
				return preparedRun{}, ctxErr
			}
			status = StatusEnvironmentUnavailable
			environmentDigest = unavailableEnvironmentDigest(resolved.Effective, err)
		}
	}
	if environmentDigest == "" {
		environmentDigest = unavailableEnvironmentDigest(resolved.Effective, statusError(status))
	}
	limits := normalizeLimits(runner.Limits)
	evidence := CandidateEvidence{
		Repository:              request.Repository,
		ParentCommit:            request.ParentCommit,
		Tree:                    request.Tree,
		CandidateDigest:         request.CandidateDigest,
		ParentManifestDigest:    request.ParentManifestDigest,
		CandidateManifestDigest: request.CandidateManifestDigest,
		DependencyDigest:        resolved.Effective.DependencyDigest,
		PlanDigest:              resolved.Effective.Digest,
		EnvironmentDigest:       environmentDigest,
		Limits:                  limitEvidence(limits),
	}
	resultKey, err := resultCacheKey(evidence)
	if err != nil {
		return preparedRun{}, err
	}
	if replay, found, replayErr := runner.replay(ctx, request, resolved, resultKey); replayErr != nil || found {
		if replayErr != nil {
			return preparedRun{}, replayErr
		}
		return preparedRun{
			request:      request,
			result:       replay,
			discoveryKey: discoveryKey,
			persistPlan:  persistPlan,
		}, nil
	}
	if status == StatusPassed && runner.Sandbox.PassingCacheAllowed() {
		cached, found, cacheErr := runner.Store.LookupPassing(ctx, resultKey)
		if cacheErr != nil {
			return preparedRun{}, cacheErr
		}
		if found {
			return preparedRun{
				request:      request,
				result:       RunResult{Plan: resolved, Execution: cached},
				discoveryKey: discoveryKey,
				cacheHit:     true,
				persistPlan:  persistPlan,
			}, nil
		}
	}
	started := runner.now()
	execution := Execution{
		ResultKey:   resultKey,
		Evidence:    evidence,
		Status:      status,
		StartedAt:   started,
		CompletedAt: started,
	}
	if status == StatusPassed {
		execution = runner.execute(ctx, request.CandidateRoot, resolved.Effective, execution)
	}
	execution, err = finalizeExecution(execution)
	if err != nil {
		return preparedRun{}, err
	}
	return preparedRun{
		request:      request,
		result:       RunResult{Plan: resolved, Execution: execution},
		discoveryKey: discoveryKey,
		persistPlan:  persistPlan,
		promote:      execution.Status == StatusPassed && runner.Sandbox.PassingCacheAllowed(),
		persist:      true,
	}, nil
}

func (runner *Runner) finishPrepared(ctx context.Context, prepared preparedRun) (RunResult, error) {
	if prepared.persistPlan {
		if err := runner.Store.PutResolvedPlan(
			ctx,
			prepared.discoveryKey,
			prepared.result.Plan,
		); err != nil {
			return RunResult{}, err
		}
	}
	if prepared.result.Attestation.ID != "" {
		return prepared.result, nil
	}
	if prepared.persist {
		if err := runner.Store.PutExecution(ctx, prepared.result.Execution); err != nil {
			return RunResult{}, err
		}
	}
	if prepared.promote {
		if err := runner.Store.PromotePassing(
			ctx,
			prepared.result.Execution.ResultKey,
			prepared.result.Execution.Digest,
		); err != nil {
			return RunResult{}, err
		}
	}
	return runner.attest(
		ctx,
		prepared.request,
		prepared.result.Plan,
		prepared.result.Execution,
		prepared.cacheHit,
	)
}

func (runner *Runner) execute(
	ctx context.Context,
	candidateRoot string,
	plan Plan,
	execution Execution,
) Execution {
	limits := normalizeLimits(runner.Limits)
	totalCtx, cancel := context.WithTimeout(ctx, limits.TotalTimeout)
	defer cancel()
	overall := StatusPassed
	remainingOutput := limits.OutputBytes
	for _, step := range plan.Steps {
		if remainingOutput <= 0 {
			overall = worseStatus(overall, StatusOutputLimitExceeded)
			break
		}
		stepLimits := limits
		stepLimits.OutputBytes = remainingOutput
		result, err := runner.Sandbox.RunStep(totalCtx, candidateRoot, step, stepLimits)
		if result.StepID == "" {
			result.StepID = step.ID
		}
		if result.OutputDigest == "" {
			result.OutputDigest = digestParts("picoclaw-local-ci-output-v1", nil)
		}
		if err != nil {
			switch {
			case errors.Is(err, context.Canceled):
				result.Status = StatusCanceled
			case errors.Is(err, context.DeadlineExceeded):
				result.Status = StatusTimedOut
			default:
				result.Status = StatusInfrastructureError
			}
		}
		execution.Steps = append(execution.Steps, result)
		remainingOutput -= int(result.ObservedOutputBytes)
		if remainingOutput < 0 {
			result.Status = StatusOutputLimitExceeded
			execution.Steps[len(execution.Steps)-1] = result
			overall = worseStatus(overall, StatusOutputLimitExceeded)
			break
		}
		overall = worseStatus(overall, result.Status)
		if result.Status == StatusCanceled || result.Status == StatusInfrastructureError {
			break
		}
		if totalCtx.Err() != nil {
			if errors.Is(totalCtx.Err(), context.DeadlineExceeded) {
				overall = worseStatus(overall, StatusTimedOut)
			} else {
				overall = worseStatus(overall, StatusCanceled)
			}
			break
		}
	}
	if len(execution.Steps) != len(plan.Steps) && overall == StatusPassed {
		overall = StatusInfrastructureError
	}
	execution.Status = overall
	execution.CompletedAt = runner.now()
	if execution.CompletedAt.Before(execution.StartedAt) {
		execution.CompletedAt = execution.StartedAt
	}
	return execution
}

func (runner *Runner) replay(
	ctx context.Context,
	request materializedRunRequest,
	plan ResolvedPlan,
	resultKey string,
) (RunResult, bool, error) {
	attestation, found, err := runner.Store.GetAttestation(ctx, request.AttestationID)
	if err != nil || !found {
		return RunResult{}, false, err
	}
	if attestation.OwnerID != request.OwnerID || attestation.ResultKey != resultKey {
		return RunResult{}, false, ErrEvidenceConflict
	}
	execution, found, err := runner.Store.GetExecution(ctx, attestation.ExecutionDigest)
	if err != nil {
		return RunResult{}, false, err
	}
	if !found || execution.ResultKey != resultKey || execution.Status != attestation.Status {
		return RunResult{}, false, ErrEvidenceCorrupt
	}
	return RunResult{Plan: plan, Execution: execution, Attestation: attestation}, true, nil
}

func (runner *Runner) attest(
	ctx context.Context,
	request materializedRunRequest,
	plan ResolvedPlan,
	execution Execution,
	cacheHit bool,
) (RunResult, error) {
	attestation, err := finalizeAttestation(Attestation{
		ID:              request.AttestationID,
		OwnerID:         request.OwnerID,
		ExecutionDigest: execution.Digest,
		ResultKey:       execution.ResultKey,
		Status:          execution.Status,
		CacheHit:        cacheHit,
		CreatedAt:       runner.now(),
	})
	if err != nil {
		return RunResult{}, err
	}
	if err = runner.Store.PutAttestation(ctx, attestation); err != nil {
		if !errors.Is(err, ErrEvidenceConflict) {
			return RunResult{}, err
		}
		replayed, found, replayErr := runner.replay(ctx, request, plan, execution.ResultKey)
		if replayErr != nil || !found {
			return RunResult{}, errors.Join(err, replayErr)
		}
		return replayed, nil
	}
	return RunResult{Plan: plan, Execution: execution, Attestation: attestation}, nil
}

func (runner *Runner) now() time.Time {
	if runner.Now != nil {
		return runner.Now().UTC()
	}
	return time.Now().UTC()
}

func unavailableEnvironmentDigest(plan Plan, err error) string {
	detail := ""
	if err != nil {
		detail = err.Error()
	}
	return digestParts(
		"picoclaw-local-ci-unavailable-environment-v1",
		[]byte(plan.Digest),
		[]byte(detail),
	)
}

func statusError(status Status) error {
	switch status {
	case StatusPlanChanged:
		return ErrPlanChanged
	case StatusIncomplete:
		return ErrIncomplete
	case StatusEnvironmentUnavailable:
		return ErrEnvironmentUnavailable
	default:
		return nil
	}
}

func limitEvidence(limits Limits) LimitEvidence {
	limits = normalizeLimits(limits)
	return LimitEvidence{
		StepTimeoutMillis:  limits.StepTimeout.Milliseconds(),
		TotalTimeoutMillis: limits.TotalTimeout.Milliseconds(),
		OutputBytes:        int64(limits.OutputBytes),
		ResourcePolicy:     "aggregate-resource-policy-v1",
	}
}

func worseStatus(current, candidate Status) Status {
	priority := map[Status]int{
		StatusPassed:                 0,
		StatusFailed:                 1,
		StatusEnvironmentUnavailable: 2,
		StatusOutputLimitExceeded:    3,
		StatusTimedOut:               4,
		StatusCanceled:               5,
		StatusInfrastructureError:    6,
	}
	if priority[candidate] > priority[current] {
		return candidate
	}
	return current
}
