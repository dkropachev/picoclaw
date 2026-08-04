package reviews

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/sipeed/picoclaw/pkg/media"
	"github.com/sipeed/picoclaw/pkg/session"
	"github.com/sipeed/picoclaw/pkg/workflows"
)

type sessionBridgeIntegrationAgent struct {
	reader           session.SnapshotReader
	captureRefs      []workflows.ReadOnlySessionRef
	captureRevisions []string
	runCalls         int
}

func (agent *sessionBridgeIntegrationAgent) CaptureReadOnlySession(
	ctx context.Context,
	ref workflows.ReadOnlySessionRef,
) (*workflows.FrozenReadOnlySession, error) {
	agent.captureRefs = append(agent.captureRefs, ref)
	snapshot, found, err := agent.reader.ReadSessionSnapshot(ctx, ref.Session)
	if err != nil {
		return nil, fmt.Errorf("capture current session snapshot: %w", err)
	}
	if !found {
		return nil, errors.New("capture current session snapshot: not found")
	}
	agent.captureRevisions = append(agent.captureRevisions, snapshot.Revision)

	// Deliberately do not compare ref.ExpectedRevision here. This integration
	// fixture proves the workflow private-root boundary independently enforces
	// the bridge's revision fence before durable creation.
	return &workflows.FrozenReadOnlySession{
		AgentID:         ref.AgentID,
		Snapshot:        snapshot,
		HistoryRevision: "sha256:review-session-bridge-integration",
		FrozenMedia:     media.FrozenSet{Version: media.FrozenSetVersion},
	}, nil
}

func (agent *sessionBridgeIntegrationAgent) RunAgent(
	_ context.Context,
	req workflows.AgentRequest,
) (map[string]any, error) {
	agent.runCalls++
	if req.FrozenReadOnlySession == nil {
		return nil, errors.New("working-context gate omitted its frozen session")
	}
	const response = `{"ask_user":false,"reason":"review context is sufficient","questions":[]}`
	structured := workflows.ValidateAgentStructuredOutput(response, req.Output)
	if !structured.Valid {
		return nil, fmt.Errorf("validate integration gate output: %s", structured.Error)
	}
	return map[string]any{
		"text":             response,
		"structured":       structured.Structured,
		"structured_json":  structured.RawJSON,
		"structured_valid": true,
	}, nil
}

func TestReviewSessionBridgeGateRevisionFenceEndToEnd(t *testing.T) {
	tests := []struct {
		name  string
		stale bool
	}{
		{name: "exact projected revision"},
		{name: "stale revision before run", stale: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			resolution, err := workflows.ResolveGatePolicy(
				[]workflows.GateSpec{{ID: "global_off", Kind: workflows.GateZero}},
				&workflows.RepositoryGatePolicy{
					Mode: workflows.GatePolicyOverlay,
					Gates: []workflows.GateSpec{{
						ID:       "review_discussion",
						Kind:     workflows.GateAIWorkingContext,
						AgentID:  "main",
						Criteria: "Ask only when the review discussion cannot resolve the finding.",
						Title:    "Resolve review finding",
					}},
				},
			)
			if err != nil {
				t.Fatalf("ResolveGatePolicy() error = %v", err)
			}

			detail := workingContextTestDetail(serviceTestCaseID, 12)
			backend := newWorkingContextBackend(t)
			service := newWorkingContextService(
				t,
				newWorkingContextReviewStore(detail),
				backend,
			)
			workspace := t.TempDir()
			runStore := workflows.NewFileRunStore(workspace)
			agent := &sessionBridgeIntegrationAgent{reader: backend}
			executor := &workflows.Executor{
				WorkspaceDir: workspace,
				Store:        runStore,
				Agents:       agent,
			}

			var projected WorkingContext
			var result *workflows.RunResult
			err = service.WithWorkingContext(
				context.Background(),
				WorkingContextRequest{CaseID: serviceTestCaseID, AgentID: "main"},
				func(ctx context.Context, working WorkingContext) error {
					projected = working
					compilation, compileErr := workflows.CompileGateWorkflow(
						"Review working-context gate",
						resolution.Effective,
						working.GateSubject,
					)
					if compileErr != nil {
						return fmt.Errorf("CompileGateWorkflow(): %w", compileErr)
					}
					if !compilation.RequiresSession ||
						compilation.RequiredSessionAgentID != working.AgentID {
						return errors.New("compiled gate omitted its working-context requirement")
					}
					compilation.PrivateRoot.ReadOnlySession = &workflows.ReadOnlySessionRef{
						AgentID:          working.AgentID,
						Session:          working.SessionKey,
						ExpectedRevision: working.SessionRevision,
					}

					if test.stale {
						backend.AddMessage(
							working.SessionKey,
							"user",
							"mutation after review projection",
						)
						current, found, readErr := backend.ReadSessionSnapshot(
							ctx,
							working.SessionKey,
						)
						if readErr != nil || !found || current.Revision == working.SessionRevision {
							return fmt.Errorf(
								"mutated session snapshot = (found=%v, revision=%q, err=%v)",
								found,
								current.Revision,
								readErr,
							)
						}
					}

					var runErr error
					result, runErr = executor.Run(ctx, workflows.RunRequest{
						Workflow:    compilation.Workflow,
						WorkflowRef: "inline/review-working-context-gate",
						PrivateRoot: compilation.PrivateRoot,
					})
					return runErr
				},
			)

			if projected.SessionKey == "" || projected.SessionRevision == "" {
				t.Fatalf("WithWorkingContext() projection = %#v", projected)
			}
			if len(agent.captureRefs) != 1 || len(agent.captureRevisions) != 1 {
				t.Fatalf(
					"capture calls = refs %#v, revisions %#v; want one",
					agent.captureRefs,
					agent.captureRevisions,
				)
			}
			captureRef := agent.captureRefs[0]
			if captureRef.AgentID != projected.AgentID ||
				captureRef.Session != projected.SessionKey ||
				captureRef.ExpectedRevision != projected.SessionRevision {
				t.Fatalf("capture ref = %#v, projection = %#v", captureRef, projected)
			}

			runs, listErr := runStore.ListRuns(context.Background())
			if listErr != nil {
				t.Fatalf("ListRuns() error = %v", listErr)
			}
			if test.stale {
				if result != nil || !errors.Is(err, workflows.ErrPrivateWorkflowContext) {
					t.Fatalf("stale bridge run = (%#v, %v), want private-context failure", result, err)
				}
				if agent.captureRevisions[0] == projected.SessionRevision {
					t.Fatalf("stale capture revision = projected revision %q", projected.SessionRevision)
				}
				if len(runs) != 0 || agent.runCalls != 0 {
					t.Fatalf(
						"stale bridge effects = %d durable runs, %d agent runs; want zero",
						len(runs),
						agent.runCalls,
					)
				}
				return
			}

			if err != nil || result == nil || result.Status != workflows.RunStatusSucceeded {
				t.Fatalf("exact bridge run = (%#v, %v), want succeeded", result, err)
			}
			if agent.captureRevisions[0] != projected.SessionRevision {
				t.Fatalf(
					"exact capture revision = %q, want %q",
					agent.captureRevisions[0],
					projected.SessionRevision,
				)
			}
			if len(runs) != 1 || agent.runCalls != 1 {
				t.Fatalf(
					"exact bridge effects = %d durable runs, %d agent runs; want one",
					len(runs),
					agent.runCalls,
				)
			}
		})
	}
}
