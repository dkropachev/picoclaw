//go:build !mipsle && !netbsd && !(freebsd && arm)

package prdevelopment

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	sharedattention "github.com/sipeed/picoclaw/pkg/attention"
	"github.com/sipeed/picoclaw/pkg/eventing"
	"github.com/sipeed/picoclaw/pkg/gitworkspace"
	"github.com/sipeed/picoclaw/pkg/prdevelopment/localci"
	"github.com/sipeed/picoclaw/pkg/workflows"
)

func TestPublicationWorkerRealSQLiteZeroAndActiveGatesSharePushReadyHandler(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		gates          []workflows.GateSpec
		wantRun        bool
		wantAgentCalls int32
	}{
		{
			name: "zero gate",
			gates: []workflows.GateSpec{{
				ID: "publication-disabled", Kind: workflows.GateZero,
			}},
		},
		{
			name: "active isolated AI gate",
			gates: []workflows.GateSpec{{
				ID:       "publication-review",
				Kind:     workflows.GateAIIsolatedContext,
				AgentID:  "sqlite-publication-reviewer",
				Criteria: "Approve only the exact locally validated candidate when no user decision is needed.",
				Title:    "Review publication readiness",
			}},
			wantRun:        true,
			wantAgentCalls: 1,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			ctx := context.Background()
			store, now, prCase, identity, reviewDigest, seeded := newPublicationPushSQLiteLifecycleWithPublication(t)
			provider := &publicationPushSQLiteProvider{
				caseID:   prCase.ID,
				identity: identity,
				observation: eventing.PRDevelopmentPublicationProviderObservation{
					Repository: prCase.Repository, PullNumber: prCase.PullNumber,
					HeadRepository:     prCase.HeadRepository,
					HeadRef:            seeded.SourceRef,
					HeadSHA:            seeded.SourceCommit,
					HeadCloneURL:       seeded.SourceCloneURL,
					CurrentReviewState: prCase.CurrentReviewState,
					ReviewDigest:       reviewDigest,
				},
				now: func() time.Time {
					*now = now.Add(time.Second)
					return *now
				},
			}
			var policyCalls atomic.Int32
			policies := sharedattention.PolicySourceFunc(func(
				ctx context.Context,
				selector sharedattention.PolicySelector,
				use sharedattention.PolicyUse,
			) error {
				policyCalls.Add(1)
				if selector.Repository != prCase.Repository ||
					selector.DecisionPoint != eventing.PRDevelopmentPublicationDecisionBeforePush {
					return fmt.Errorf("unexpected publication selector: %#v", selector)
				}
				return use(ctx, sharedattention.PolicySnapshot{
					Revision: "sqlite-publication-worker-" + strings.ReplaceAll(test.name, " ", "-"),
					Global:   append([]workflows.GateSpec(nil), test.gates...),
				})
			})
			processor, err := NewPublicationGateProcessor(PublicationGateProcessorConfig{
				Store: store, Policies: policies, Provider: provider,
			})
			require.NoError(t, err)

			evidence := newPublicationWorkerSQLiteEvidence(seeded)
			workspace := &publicationWorkerSQLiteWorkspace{
				publication:  seeded,
				reviewDigest: strings.Repeat("f", 64),
			}
			runRoot := t.TempDir()
			runs := workflows.NewFileRunStore(runRoot)
			agent := &publicationWorkerSQLiteAgent{wantAgentID: "sqlite-publication-reviewer"}
			gateExecutor, err := NewPublicationGateExecutor(PublicationGateExecutorConfig{
				Store: store,
				Executor: &workflows.Executor{
					WorkspaceDir: runRoot, Store: runs, Agents: agent,
				},
				Runs: runs, Evidence: evidence,
				Workspaces: func() (AttentionReviewWorkspace, error) {
					return workspace, nil
				},
				Provider: provider,
			})
			require.NoError(t, err)
			pending, err := NewPublicationPendingGateHandler(PublicationPendingGateHandlerConfig{
				Store: store, Processor: processor, Executor: gateExecutor,
			})
			require.NoError(t, err)
			waiting, err := NewPublicationGateWaitingHandler(PublicationGateWaitingHandlerConfig{
				Store: store, Runs: runs,
			})
			require.NoError(t, err)
			pusher := &publicationPushSQLitePusher{}
			pushReady, err := NewPublicationPushReadyHandler(PublicationPushReadyHandlerConfig{
				Store: store, Provider: provider, Pusher: pusher,
				LeaseDuration: 10 * time.Minute,
				Now:           func() time.Time { return *now },
			})
			require.NoError(t, err)
			dispatcher, err := NewPublicationDispatcher(PublicationDispatcherConfig{
				Pending: pending, GateWaiting: waiting, PushReady: pushReady,
			})
			require.NoError(t, err)
			require.Same(t, pushReady, dispatcher.pushReady)
			worker, err := NewPublicationWorker(PublicationWorkerConfig{
				Queue: store, Dispatcher: dispatcher,
			})
			require.NoError(t, err)

			handled, err := worker.ProcessOne(ctx)
			require.NoError(t, err)
			require.True(t, handled)
			ready, err := store.GetPRDevelopmentPublication(ctx, seeded.ID)
			require.NoError(t, err)
			assert.Equal(t, eventing.PRDevelopmentPublicationPushReady, ready.Status)
			assert.Empty(t, ready.ClaimFrom)
			assert.Empty(t, ready.ClaimToken)
			assert.NotEmpty(t, ready.PolicyRevision)
			assert.NotEmpty(t, ready.SubjectRevision)
			assert.NotEmpty(t, ready.ProviderObservationHash)
			assert.Equal(t, 0, pusher.callCount(), "push_ready must be observable before Git")
			if test.wantRun {
				require.NotEmpty(t, ready.DecisionRunID)
				run, runErr := runs.GetRun(ctx, ready.DecisionRunID)
				require.NoError(t, runErr)
				assert.Equal(t, workflows.RunStatusSucceeded, run.Status)
				assert.Equal(t, sharedattention.WorkflowRef, run.WorkflowRef)
			} else {
				assert.Empty(t, ready.DecisionRunID)
			}
			assert.Equal(t, test.wantAgentCalls, agent.calls.Load())

			handled, err = worker.ProcessOne(ctx)
			require.NoError(t, err)
			require.True(t, handled)
			published, err := store.GetPRDevelopmentPublication(ctx, seeded.ID)
			require.NoError(t, err)
			assert.Equal(t, eventing.PRDevelopmentPublicationPublished, published.Status)
			assert.Equal(
				t,
				eventing.PRDevelopmentPublicationPushAlreadyCurrent,
				published.PushDisposition,
			)
			assert.NotEmpty(t, published.PushRequestHash)
			assert.NotEmpty(t, published.PushResultHash)
			assert.NotNil(t, published.EffectStartedAt)
			assert.NotNil(t, published.CompletedAt)
			assert.Equal(t, 1, pusher.callCount())
			assert.Equal(t, 2, provider.callCount())
			assert.Equal(t, int32(1), policyCalls.Load())
			if test.wantRun {
				assert.Equal(t, int32(1), evidence.planCalls.Load())
				assert.Equal(t, int32(1), evidence.executionCalls.Load())
				assert.Equal(t, int32(1), workspace.calls.Load())
			} else {
				assert.Zero(t, evidence.planCalls.Load())
				assert.Zero(t, evidence.executionCalls.Load())
				assert.Zero(t, workspace.calls.Load())
			}

			handled, err = worker.ProcessOne(ctx)
			require.NoError(t, err)
			assert.False(t, handled)
			assert.Equal(t, 1, pusher.callCount(), "terminal replay must not repeat Git")
		})
	}
}

type publicationWorkerSQLiteEvidence struct {
	plan           localci.Plan
	execution      localci.Execution
	planCalls      atomic.Int32
	executionCalls atomic.Int32
}

func newPublicationWorkerSQLiteEvidence(
	publication eventing.PRDevelopmentPublication,
) *publicationWorkerSQLiteEvidence {
	dependencyDigest := strings.Repeat("d", 64)
	step := localci.Step{
		ID:       "sqlite-targeted-test",
		Name:     "SQLite targeted test",
		Kind:     localci.StepTest,
		Origin:   localci.OriginMake,
		Source:   "Makefile",
		Required: true,
	}
	plan := localci.Plan{
		Version:          localci.EvidenceVersion,
		DiscoveryVersion: localci.DiscoveryVersion,
		DependencyDigest: dependencyDigest,
		Digest:           publication.CIPlanDigest,
		Complete:         true,
		Steps:            []localci.Step{step},
	}
	execution := localci.Execution{
		Version: localci.EvidenceVersion,
		Digest:  publication.CIResultDigest,
		Evidence: localci.CandidateEvidence{
			DependencyDigest: dependencyDigest,
			PlanDigest:       publication.CIPlanDigest,
		},
		Status: localci.StatusPassed,
		Steps: []localci.StepResult{{
			StepID: step.ID, Status: localci.StatusPassed, ExitCode: 0,
		}},
	}
	return &publicationWorkerSQLiteEvidence{plan: plan, execution: execution}
}

func (evidence *publicationWorkerSQLiteEvidence) GetPlan(
	ctx context.Context,
	digest string,
) (localci.Plan, bool, error) {
	evidence.planCalls.Add(1)
	if err := ctx.Err(); err != nil {
		return localci.Plan{}, false, err
	}
	if digest != evidence.plan.Digest {
		return localci.Plan{}, false, nil
	}
	return evidence.plan, true, nil
}

func (evidence *publicationWorkerSQLiteEvidence) GetExecution(
	ctx context.Context,
	digest string,
) (localci.Execution, bool, error) {
	evidence.executionCalls.Add(1)
	if err := ctx.Err(); err != nil {
		return localci.Execution{}, false, err
	}
	if digest != evidence.execution.Digest {
		return localci.Execution{}, false, nil
	}
	return evidence.execution, true, nil
}

type publicationWorkerSQLiteWorkspace struct {
	publication  eventing.PRDevelopmentPublication
	reviewDigest string
	calls        atomic.Int32
}

func (workspace *publicationWorkerSQLiteWorkspace) SnapshotPinnedLineReview(
	ctx context.Context,
	request gitworkspace.PinnedLineReviewRequest,
) (gitworkspace.PinnedLineReviewSnapshot, error) {
	workspace.calls.Add(1)
	if err := ctx.Err(); err != nil {
		return gitworkspace.PinnedLineReviewSnapshot{}, err
	}
	want := gitworkspace.PinnedLineReviewRequest{
		LineID:          workspace.publication.LineID,
		ExpectedVersion: workspace.publication.LineVersion,
		ExpectedBase:    workspace.publication.BaseCommit,
		ExpectedTip:     workspace.publication.TipCommit,
		ExpectedTree:    workspace.publication.Tree,
	}
	if request != want {
		return gitworkspace.PinnedLineReviewSnapshot{}, fmt.Errorf(
			"pinned review request = %#v, want %#v",
			request,
			want,
		)
	}
	return gitworkspace.PinnedLineReviewSnapshot{
		Version:       workspace.publication.LineVersion,
		MutationEpoch: workspace.publication.MutationEpoch,
		ParkIntentID:  workspace.publication.ParkIntentID,
		BaseCommit:    workspace.publication.BaseCommit,
		Commit:        workspace.publication.TipCommit,
		Tree:          workspace.publication.Tree,
		ReviewDigest:  workspace.reviewDigest,
	}, nil
}

type publicationWorkerSQLiteAgent struct {
	wantAgentID string
	calls       atomic.Int32
}

func (agent *publicationWorkerSQLiteAgent) RunAgent(
	ctx context.Context,
	request workflows.AgentRequest,
) (map[string]any, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	agent.calls.Add(1)
	if request.AgentID != agent.wantAgentID || !request.PrivateContext ||
		!request.EphemeralSession || request.Session != "" {
		return nil, errors.New("isolated publication gate received a widened agent request")
	}
	response := `{"ask_user":false,"reason":"the exact locally validated candidate is ready","questions":[]}`
	structured := workflows.ValidateAgentStructuredOutput(response, request.Output)
	if !structured.Valid {
		return nil, fmt.Errorf("invalid isolated publication gate output: %s", structured.Error)
	}
	return map[string]any{
		"text":             response,
		"structured":       structured.Structured,
		"structured_json":  structured.RawJSON,
		"structured_valid": true,
	}, nil
}

var (
	_ AttentionEvidenceStore   = (*publicationWorkerSQLiteEvidence)(nil)
	_ AttentionReviewWorkspace = (*publicationWorkerSQLiteWorkspace)(nil)
	_ workflows.AgentRunner    = (*publicationWorkerSQLiteAgent)(nil)
)
