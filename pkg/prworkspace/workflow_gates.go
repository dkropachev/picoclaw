package prworkspace

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/prlifecycle"
	"github.com/sipeed/picoclaw/pkg/workflows"
	"github.com/sipeed/picoclaw/pkg/workflows/gatetypes"
)

// WorkflowGateEvaluator executes configured staged gates on the existing
// workflow engine. The browser receives only GateRun/turn projections; pinned
// policy, subject, and private workflow state remain runtime-only.
type WorkflowGateEvaluator struct {
	Config         config.PRLifecycleConfig
	Executor       *workflows.Executor
	WorkingContext GateWorkingContextBinder
	Now            func() time.Time
}

func (evaluator *WorkflowGateEvaluator) Start(ctx context.Context, request GateRequest) (GateRun, error) {
	if evaluator == nil || request.WorkspaceID == "" || request.DecisionPoint == "" || request.SubjectDigest == "" {
		return GateRun{}, ErrInvalid
	}
	if err := validatePRLifecycleGateIdentity(request.DecisionPoint, request.Purpose); err != nil {
		return GateRun{}, err
	}
	configured := evaluator.Config.Effective()
	profileID, profile, profileRevision, err := configured.ProfileForRepository(request.ProviderOrigin, request.RepositoryID)
	if err != nil {
		return GateRun{}, err
	}
	spec, configuredPoint := profile.Workflows[request.DecisionPoint]
	if !configuredPoint {
		return fallbackConfiguredHumanGate(evaluator.now(), request, profileID, profileRevision), nil
	}
	if string(spec.Purpose) != request.Purpose {
		return GateRun{}, fmt.Errorf("gate purpose mismatch for %s", request.DecisionPoint)
	}
	subjectJSON, err := json.Marshal(request.Subject)
	if err != nil {
		return GateRun{}, err
	}
	normalizedSubject, err := decodeWorkflowGateSubject(subjectJSON)
	if err != nil {
		return GateRun{}, err
	}
	compilation, err := workflows.CompileGateWorkflowV2(spec, normalizedSubject)
	if err != nil {
		return GateRun{}, err
	}
	policyJSON, _ := gatetypes.CanonicalGateWorkflowSpecJSON(spec)
	now := evaluator.now()
	gate := GateRun{
		ID:            stableID("pgr_", request.WorkspaceID, request.DecisionPoint, request.SubjectDigest, profileRevision),
		DecisionPoint: request.DecisionPoint, Purpose: request.Purpose,
		State: ExecutionRunning, PolicyRevision: profileRevision,
		SubjectRevision: request.SubjectDigest, Evidence: projectGateEvidence(normalizedSubject), CreatedAt: now,
		runtime: &gateRuntime{ProfileID: profileID, PinnedPolicy: policyJSON, PinnedSubject: subjectJSON},
	}
	if compilation.ImmediateOutcome != "" {
		gate.Outcome = GateOutcome(compilation.ImmediateOutcome)
		gate.State, gate.FinishedAt = ExecutionSucceeded, &now
		gate.Turns = turnsFromCompilation(compilation, nil)
		return gate, nil
	}
	if evaluator.Executor == nil {
		return fallbackConfiguredHumanGate(now, request, profileID, profileRevision), nil
	}
	if compilation.RequiresSession {
		if evaluator.WorkingContext == nil {
			return GateRun{}, errors.New("configured working-context gate has no PR workspace session binder")
		}
		ref, bindErr := evaluator.WorkingContext.Bind(ctx, GateWorkingContextRequest{
			WorkspaceID: request.WorkspaceID, WorkspaceVersion: request.WorkspaceVersion,
			AgentID: compilation.RequiredSessionAgentID, Context: request.WorkingContext,
		})
		if bindErr != nil {
			return GateRun{}, bindErr
		}
		compilation.PrivateRoot.ReadOnlySession = &ref
	}
	result, err := evaluator.Executor.Run(ctx, workflows.RunRequest{
		Workflow:    compilation.Workflow,
		WorkflowRef: "inline/pr-lifecycle/" + request.DecisionPoint + "/" + strings.TrimPrefix(compilation.SpecDigest, "sha256:"),
		PrivateRoot: compilation.PrivateRoot,
	})
	if err != nil {
		return GateRun{}, err
	}
	gate.runtime.WorkflowRunID = result.RunID
	return evaluator.project(ctx, gate, compilation)
}

func (evaluator *WorkflowGateEvaluator) Respond(ctx context.Context, gate GateRun, decision GateOutcome, answers map[string]any, comment string) (GateRun, error) {
	if evaluator == nil || gate.runtime == nil || !validGateOutcome(decision) {
		return GateRun{}, ErrInvalid
	}
	if gate.runtime.WorkflowRunID == "" {
		return answerFallbackGate(gate, decision, answers, comment, evaluator.now()), nil
	}
	if evaluator.Executor == nil {
		return GateRun{}, ErrInvalid
	}
	var spec workflows.GateWorkflowSpec
	if err := json.Unmarshal(gate.runtime.PinnedPolicy, &spec); err != nil {
		return GateRun{}, errors.New("pinned gate policy is invalid")
	}
	subject, err := decodeWorkflowGateSubject(gate.runtime.PinnedSubject)
	if err != nil {
		return GateRun{}, errors.New("pinned gate subject is invalid")
	}
	compilation, err := workflows.CompileGateWorkflowV2(spec, subject)
	if err != nil {
		return GateRun{}, err
	}
	tasks, err := evaluator.Executor.ListHumanTasks(ctx, gate.runtime.WorkflowRunID)
	if err != nil {
		return GateRun{}, err
	}
	var waiting *workflows.WorkflowHumanTask
	for index := range tasks {
		if tasks[index].Status == workflows.HumanTaskStatusWaiting {
			waiting = &tasks[index]
			break
		}
	}
	if waiting == nil {
		if projected, terminal, projectErr := evaluator.projectTerminal(ctx, gate, compilation); projectErr != nil {
			return GateRun{}, projectErr
		} else if terminal {
			return projected, nil
		}
		return GateRun{}, workflows.ErrHumanTaskConflict
	}
	responseID := stableID("phr_", gate.ID, waiting.ID, fmt.Sprint(waiting.Revision), string(decision), comment)
	_, err = evaluator.Executor.ResumeHumanTask(ctx, gate.runtime.WorkflowRunID, waiting.ID, workflows.HumanTaskResumeRequest{
		ExpectedRevision: waiting.Revision, InputHash: waiting.InputHash, ResponseID: responseID,
		Response: map[string]any{"decision": string(decision), "answers": answers, "comment": comment},
	})
	if err != nil {
		// Another responder or a prior request whose aggregate mutation failed
		// may have committed the workflow outcome after this task projection.
		// Reconcile the pinned terminal run instead of requiring an impossible
		// second human response. The durable workflow outcome remains authoritative.
		if errors.Is(err, workflows.ErrHumanTaskConflict) {
			if projected, terminal, projectErr := evaluator.projectTerminal(ctx, gate, compilation); projectErr != nil {
				return GateRun{}, projectErr
			} else if terminal {
				return projected, nil
			}
		}
		return GateRun{}, err
	}
	return evaluator.project(ctx, gate, compilation)
}

func (evaluator *WorkflowGateEvaluator) projectTerminal(
	ctx context.Context,
	gate GateRun,
	compilation *workflows.GateWorkflowV2Compilation,
) (GateRun, bool, error) {
	projected, err := evaluator.project(ctx, gate, compilation)
	if err != nil {
		return GateRun{}, false, err
	}
	if projected.State != ExecutionSucceeded || !validGateOutcome(projected.Outcome) {
		return GateRun{}, false, nil
	}
	return projected, true, nil
}

func decodeWorkflowGateSubject(raw []byte) (map[string]any, error) {
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.UseNumber()
	var subject map[string]any
	if err := decoder.Decode(&subject); err != nil || subject == nil {
		return nil, errors.New("gate subject is invalid")
	}
	return subject, nil
}

func (evaluator *WorkflowGateEvaluator) project(ctx context.Context, gate GateRun, compilation *workflows.GateWorkflowV2Compilation) (GateRun, error) {
	if evaluator.Executor.Store == nil {
		return GateRun{}, errors.New("gate workflow run store is required")
	}
	run, err := evaluator.Executor.Store.GetRun(ctx, gate.runtime.WorkflowRunID)
	if err != nil {
		return GateRun{}, err
	}
	gate.Turns = turnsFromCompilation(compilation, run)
	outcome, _, outcomeErr := workflows.ResolveGateWorkflowV2Outcome(compilation, run)
	if errors.Is(outcomeErr, workflows.ErrGateWorkflowV2Incomplete) {
		gate.State = ExecutionWaitingGate
		for _, turn := range gate.Turns {
			if turn.Kind == string(workflows.GateHuman) && turn.Status == "waiting" {
				gate.State = ExecutionWaitingUser
				break
			}
		}
		return gate, nil
	}
	if outcomeErr != nil {
		return GateRun{}, outcomeErr
	}
	now := evaluator.now()
	gate.Outcome, gate.State, gate.FinishedAt = GateOutcome(outcome), ExecutionSucceeded, &now
	return gate, nil
}

func turnsFromCompilation(compilation *workflows.GateWorkflowV2Compilation, run *workflows.Run) []GateTurn {
	if compilation == nil {
		return nil
	}
	turns := make([]GateTurn, 0, len(compilation.Stages))
	configured := make(map[string]workflows.GateStageSpec, len(compilation.Stages))
	var spec workflows.GateWorkflowSpec
	if json.Unmarshal(compilation.CanonicalSpec, &spec) == nil {
		for _, stage := range spec.Stages {
			configured[stage.ID] = stage
		}
	}
	for _, stage := range compilation.Stages {
		source := configured[stage.ID]
		turn := GateTurn{
			StageID: stage.ID, Kind: string(stage.Kind), Title: source.Title,
			Status: "pending", Questions: projectGateQuestions(source.Questions),
		}
		if stage.ImmediateOutcome != "" {
			turn.Status, turn.Outcome = "answered", GateOutcome(stage.ImmediateOutcome)
		} else if run != nil && stage.StepID != "" {
			if execution, ok := gateStageExecution(run, stage.StepID); ok {
				turn.Status = execution.Status
				switch execution.Status {
				case workflows.RunStatusWaiting:
					turn.Status = "waiting"
				case workflows.RunStatusSucceeded:
					turn.Status = "answered"
				}
				projectGateTurnOutputs(&turn, execution.Outputs)
			}
		}
		turns = append(turns, turn)
	}
	return turns
}

func gateStageExecution(run *workflows.Run, stepID string) (workflows.StepExecution, bool) {
	if run == nil || stepID == "" {
		return workflows.StepExecution{}, false
	}
	for _, execution := range run.Steps {
		if execution.ID == stepID {
			return execution, true
		}
	}
	return workflows.StepExecution{}, false
}

func projectGateTurnOutputs(turn *GateTurn, outputs map[string]any) {
	if turn == nil || len(outputs) == 0 {
		return
	}
	if response, ok := outputs["response"].(map[string]any); ok {
		if decision, ok := response["decision"].(string); ok {
			turn.Outcome = GateOutcome(decision)
		}
		if answers, ok := response["answers"].(map[string]any); ok {
			turn.Answers = answers
		}
		if comment, ok := response["comment"].(string); ok {
			turn.Comment = comment
		}
		return
	}
	if structured, ok := outputs["structured"].(map[string]any); ok {
		if outcome, ok := structured["outcome"].(string); ok {
			turn.Outcome = GateOutcome(outcome)
		}
	}
}

func projectGateQuestions(value any) []string {
	var result []string
	var walk func(any)
	walk = func(candidate any) {
		switch typed := candidate.(type) {
		case string:
			if text := strings.TrimSpace(typed); text != "" {
				result = append(result, text)
			}
		case []any:
			for _, item := range typed {
				walk(item)
			}
		case map[string]any:
			for _, key := range []string{"question", "prompt", "label", "title"} {
				if item, ok := typed[key]; ok {
					walk(item)
				}
			}
		}
	}
	walk(value)
	return result
}

func fallbackConfiguredHumanGate(now time.Time, request GateRequest, profileID, revision string) GateRun {
	return GateRun{
		ID:            stableID("pgr_", request.WorkspaceID, request.DecisionPoint, request.SubjectDigest, "fallback"),
		DecisionPoint: request.DecisionPoint, Purpose: request.Purpose,
		State: ExecutionWaitingUser, PolicyRevision: revision,
		SubjectRevision: request.SubjectDigest, Evidence: projectGateEvidence(request.Subject), CreatedAt: now,
		Turns:   []GateTurn{{StageID: "fallback-human", Kind: "human", Title: "Decision required", Status: "waiting", Questions: []string{"Review the evidence and choose an available outcome."}}},
		runtime: &gateRuntime{ProfileID: profileID},
	}
}

func validatePRLifecycleGateIdentity(point, purpose string) error {
	expected, exists := prlifecycle.DecisionPointPurpose(point)
	if !exists {
		return fmt.Errorf("%w: unknown PR lifecycle decision point %q", ErrInvalid, point)
	}
	if string(expected) != purpose {
		return fmt.Errorf(
			"%w: PR lifecycle decision point %q requires purpose %q",
			ErrInvalid, point, expected,
		)
	}
	return nil
}

func (evaluator *WorkflowGateEvaluator) now() time.Time {
	if evaluator.Now != nil {
		return evaluator.Now().UTC()
	}
	return time.Now().UTC()
}

var _ GateEvaluator = (*WorkflowGateEvaluator)(nil)
