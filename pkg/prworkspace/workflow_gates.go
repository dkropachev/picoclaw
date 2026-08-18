package prworkspace

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
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

// WorkflowGateEvaluator executes the application-owned PR gate selected by a
// lifecycle decision point. A repository gate configuration can atomically
// replace the workflow's default action, but cannot replace the form, prompt,
// gate identity, or application interpretation of returned field values.
type WorkflowGateEvaluator struct {
	Config         config.PRLifecycleConfig
	Executor       *workflows.Executor
	WorkingContext GateWorkingContextBinder
	Now            func() time.Time
}

type pinnedPRLifecycleGateV3 struct {
	Version          string                   `json:"version"`
	WorkflowRef      string                   `json:"workflow-ref"`
	WorkflowRevision string                   `json:"workflow-revision"`
	GateRef          string                   `json:"gate-ref"`
	Gate             gatetypes.GateDefinition `json:"gate"`
	ConfigID         string                   `json:"config-id"`
	ConfigRevision   string                   `json:"config-revision"`
	ActionRevision   string                   `json:"action-revision"`
}

type fixedPRLifecycleGateActionResolver struct {
	workflowRef    string
	gateRef        string
	action         *workflows.GateAction
	actionRevision string
}

func (resolver fixedPRLifecycleGateActionResolver) ResolveGateAction(
	_ context.Context,
	request workflows.GateActionResolveRequest,
) (workflows.GateActionResolution, error) {
	if request.WorkflowRef != resolver.workflowRef || request.GateRef != resolver.gateRef {
		return workflows.GateActionResolution{}, fmt.Errorf(
			"gate action resolution escaped pinned workflow %q gate %q",
			resolver.workflowRef,
			resolver.gateRef,
		)
	}
	resolution := workflows.GateActionResolution{Revision: resolver.actionRevision}
	if resolver.action != nil {
		action := *resolver.action
		action.Fields = cloneAnyMap(action.Fields)
		resolution.Action = &action
	}
	return resolution, nil
}

func (evaluator *WorkflowGateEvaluator) Start(ctx context.Context, request GateRequest) (GateRun, error) {
	if evaluator == nil || evaluator.Executor == nil || request.WorkspaceID == "" ||
		request.DecisionPoint == "" || request.SubjectDigest == "" {
		return GateRun{}, ErrInvalid
	}
	if !prlifecycle.IsDecisionPoint(request.DecisionPoint) {
		return GateRun{}, fmt.Errorf("%w: unknown PR lifecycle decision point %q", ErrInvalid, request.DecisionPoint)
	}
	configured := evaluator.Config.Effective()
	configID, gateConfig, configRevision, err := configured.ConfigForRepository(
		request.ProviderOrigin,
		request.RepositoryID,
	)
	if err != nil {
		return GateRun{}, err
	}
	entry, err := prLifecycleGateCatalogEntry(request.DecisionPoint)
	if err != nil {
		return GateRun{}, err
	}
	override, err := configuredPRLifecycleGateAction(gateConfig, entry.WorkflowRef, entry.GateRef)
	if err != nil {
		return GateRun{}, err
	}
	effectiveAction := entry.Gate.DefaultAction
	if override != nil {
		effectiveAction = override
	}
	if effectiveAction == nil {
		return GateRun{}, fmt.Errorf("gate %q has no configured action or default-action", entry.GateRef)
	}
	actionRevision, err := prLifecycleActionRevision(entry, configID, configRevision, effectiveAction)
	if err != nil {
		return GateRun{}, err
	}

	subjectJSON, err := json.Marshal(request.Subject)
	if err != nil {
		return GateRun{}, err
	}
	normalizedSubject, err := decodeWorkflowGateSubject(subjectJSON)
	if err != nil {
		return GateRun{}, err
	}
	workflow, workflowRevision, err := loadPRLifecycleGateWorkflow()
	if err != nil {
		return GateRun{}, err
	}
	if workflowRevision != entry.WorkflowRevision {
		return GateRun{}, errors.New("PR lifecycle gate workflow changed during admission")
	}
	compilation, err := workflows.CompileGateWorkflowV3(
		workflow,
		entry.GateRef,
		map[string]any{"gate-subject": normalizedSubject},
	)
	if err != nil {
		return GateRun{}, err
	}
	if effectiveAction.Type == gatetypes.GateActionAI && effectiveAction.Session == workflows.AgentSessionPrivate {
		if evaluator.WorkingContext == nil {
			return GateRun{}, errors.New("configured private-session gate has no PR workspace session binder")
		}
		ref, bindErr := evaluator.WorkingContext.Bind(ctx, GateWorkingContextRequest{
			WorkspaceID: request.WorkspaceID, WorkspaceVersion: request.WorkspaceVersion,
			AgentID: effectiveAction.AgentID, Context: request.WorkingContext,
		})
		if bindErr != nil {
			return GateRun{}, bindErr
		}
		compilation.PrivateRoot.ReadOnlySession = &ref
	}
	if effectiveAction.Type == gatetypes.GateActionAI && effectiveAction.Session == workflows.AgentSessionSource {
		source, sourceErr := sourceForGateSubject(request.Subject, request.WorkspaceID)
		if sourceErr != nil {
			return GateRun{}, sourceErr
		}
		compilation.PrivateRoot.ReadOnlySession = &workflows.ReadOnlySessionRef{
			AgentID: source.AgentID, Session: source.Session,
			ExpectedRevision: source.SessionRevision,
		}
	}
	pinned := pinnedPRLifecycleGateV3{
		Version: "3", WorkflowRef: entry.WorkflowRef, WorkflowRevision: entry.WorkflowRevision,
		GateRef: entry.GateRef, Gate: entry.Gate, ConfigID: configID,
		ConfigRevision: configRevision, ActionRevision: actionRevision,
	}
	pinnedPolicy, err := json.Marshal(pinned)
	if err != nil {
		return GateRun{}, err
	}
	policyRevision := prLifecyclePolicyRevision(entry.WorkflowRevision, configRevision, actionRevision)
	now := evaluator.now()
	gate := GateRun{
		ID: stableID(
			"pgr_", request.WorkspaceID, request.DecisionPoint, request.SubjectDigest, policyRevision,
		),
		DecisionPoint: request.DecisionPoint,
		State:         ExecutionRunning, PolicyRevision: policyRevision,
		SubjectRevision: request.SubjectDigest, Evidence: projectGateEvidence(normalizedSubject), CreatedAt: now,
		runtime: &gateRuntime{
			ConfigID: configID, PinnedPolicy: pinnedPolicy, PinnedSubject: subjectJSON,
		},
	}
	runtimeExecutor := *evaluator.Executor
	runtimeExecutor.GateActions = fixedPRLifecycleGateActionResolver{
		workflowRef: entry.WorkflowRef, gateRef: entry.GateRef,
		action: override, actionRevision: actionRevision,
	}
	workflowRunID := stableID("wr_", gate.ID, entry.WorkflowRevision, actionRevision)
	result, err := runtimeExecutor.Run(ctx, workflows.RunRequest{
		RunID:    workflowRunID,
		Workflow: compilation.Workflow, WorkflowRef: entry.WorkflowRef,
		PrivateRoot: compilation.PrivateRoot,
	})
	if err != nil {
		return GateRun{}, err
	}
	gate.runtime.WorkflowRunID = result.RunID
	projected, err := evaluator.project(ctx, gate)
	if err != nil {
		return GateRun{}, err
	}
	resolvedRevision := latestGateActionRevision(projected.Turns)
	if resolvedRevision != "" && resolvedRevision != actionRevision {
		pinned.ActionRevision = resolvedRevision
		projected.runtime.PinnedPolicy, err = json.Marshal(pinned)
		if err != nil {
			return GateRun{}, err
		}
		projected.PolicyRevision = prLifecyclePolicyRevision(
			entry.WorkflowRevision,
			configRevision,
			resolvedRevision,
		)
		projected.ID = stableID(
			"pgr_", request.WorkspaceID, request.DecisionPoint, request.SubjectDigest, projected.PolicyRevision,
		)
	}
	return projected, nil
}

func latestGateActionRevision(turns []GateTurn) string {
	for index := len(turns) - 1; index >= 0; index-- {
		if turns[index].ActionRevision != "" {
			return turns[index].ActionRevision
		}
	}
	return ""
}

func configuredPRLifecycleGateAction(
	gateConfig config.PRLifecycleGateConfig,
	workflowRef string,
	gateRef string,
) (*workflows.GateAction, error) {
	for _, binding := range gateConfig.Bindings {
		if binding.WorkflowRef != workflowRef || binding.GateRef != gateRef {
			continue
		}
		if binding.Action == nil {
			return nil, nil
		}
		action := workflows.GateAction(*binding.Action)
		action.Fields = cloneAnyMap(action.Fields)
		return &action, nil
	}
	return nil, nil
}

func prLifecycleActionRevision(
	entry PRLifecycleGateCatalogEntry,
	configID string,
	configRevision string,
	action *gatetypes.GateAction,
) (string, error) {
	encoded, err := json.Marshal(struct {
		WorkflowRevision string                `json:"workflow-revision"`
		GateRef          string                `json:"gate-ref"`
		ConfigID         string                `json:"config-id"`
		ConfigRevision   string                `json:"config-revision"`
		Action           *gatetypes.GateAction `json:"action"`
	}{entry.WorkflowRevision, entry.GateRef, configID, configRevision, action})
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(encoded)
	return "sha256:" + hex.EncodeToString(digest[:]), nil
}

func prLifecyclePolicyRevision(parts ...string) string {
	digest := sha256.New()
	for _, part := range parts {
		_, _ = digest.Write([]byte(part))
		_, _ = digest.Write([]byte{0})
	}
	return "sha256:" + hex.EncodeToString(digest.Sum(nil))
}

func (evaluator *WorkflowGateEvaluator) Respond(
	ctx context.Context,
	gate GateRun,
	fieldValues map[string]any,
) (GateRun, error) {
	if evaluator == nil || evaluator.Executor == nil || gate.runtime == nil ||
		gate.runtime.WorkflowRunID == "" || fieldValues == nil {
		return GateRun{}, ErrInvalid
	}
	tasks, err := evaluator.Executor.ListHumanTasks(ctx, gate.runtime.WorkflowRunID)
	if err != nil {
		return GateRun{}, err
	}
	var waiting *workflows.WorkflowHumanTask
	for index := range tasks {
		if tasks[index].Status == workflows.HumanTaskStatusWaiting && tasks[index].GateForm != nil {
			waiting = &tasks[index]
			break
		}
	}
	if waiting == nil {
		if projected, terminal, projectErr := evaluator.projectTerminal(ctx, gate); projectErr != nil {
			return GateRun{}, projectErr
		} else if terminal {
			return projected, nil
		}
		return GateRun{}, workflows.ErrHumanTaskConflict
	}
	encodedValues, err := json.Marshal(fieldValues)
	if err != nil {
		return GateRun{}, ErrInvalid
	}
	responseID := stableID(
		"phr_", gate.ID, waiting.ID, fmt.Sprint(waiting.Revision), string(encodedValues),
	)
	_, err = evaluator.Executor.ResumeHumanTask(
		ctx,
		gate.runtime.WorkflowRunID,
		waiting.ID,
		workflows.HumanTaskResumeRequest{
			ExpectedRevision: waiting.Revision, InputHash: waiting.InputHash, ResponseID: responseID,
			Response: map[string]any{"field-values": fieldValues},
		},
	)
	if err != nil {
		if errors.Is(err, workflows.ErrHumanTaskConflict) {
			if projected, terminal, projectErr := evaluator.projectTerminal(ctx, gate); projectErr != nil {
				return GateRun{}, projectErr
			} else if terminal {
				return projected, nil
			}
		}
		return GateRun{}, err
	}
	return evaluator.project(ctx, gate)
}

func (evaluator *WorkflowGateEvaluator) projectTerminal(
	ctx context.Context,
	gate GateRun,
) (GateRun, bool, error) {
	projected, err := evaluator.project(ctx, gate)
	if err != nil {
		return GateRun{}, false, err
	}
	if projected.State != ExecutionSucceeded {
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

func (evaluator *WorkflowGateEvaluator) project(ctx context.Context, gate GateRun) (GateRun, error) {
	if evaluator.Executor.Store == nil || gate.runtime == nil || gate.runtime.WorkflowRunID == "" {
		return GateRun{}, errors.New("gate workflow run store is required")
	}
	run, err := evaluator.Executor.Store.GetRun(ctx, gate.runtime.WorkflowRunID)
	if err != nil {
		return GateRun{}, err
	}
	var pinned pinnedPRLifecycleGateV3
	if err := json.Unmarshal(gate.runtime.PinnedPolicy, &pinned); err != nil || pinned.Version != "3" {
		return GateRun{}, errors.New("pinned gate policy is invalid")
	}
	resultTurn := GateTurn{StageID: "gate", Kind: "gate", Title: pinned.Gate.Prompt, Status: "pending"}
	step, hasStep := onlyGateStepExecution(run)
	if hasStep {
		resultTurn.Status = step.Status
		projectGateV3Outputs(&resultTurn, step.Outputs)
	}
	tasks, taskErr := evaluator.Executor.ListHumanTasks(ctx, run.ID)
	if taskErr != nil {
		return GateRun{}, taskErr
	}
	turns := projectGateV3HumanTasks(tasks)
	if run.Status == workflows.RunStatusWaiting {
		for _, turn := range turns {
			if turn.Status == "waiting" {
				gate.State, gate.Turns = ExecutionWaitingUser, turns
				return gate, nil
			}
		}
		if len(turns) == 0 {
			turns = []GateTurn{resultTurn}
		}
		gate.State, gate.Turns = ExecutionWaitingGate, turns
		return gate, nil
	}
	if run.Status != workflows.RunStatusSucceeded {
		return GateRun{}, fmt.Errorf("gate workflow %s ended in state %s", run.ID, run.Status)
	}
	if !hasStep || len(resultTurn.FieldValues) == 0 {
		return GateRun{}, errors.New("gate workflow completed without field-values")
	}
	now := evaluator.now()
	resultTurn.Status = "answered"
	if len(tasks) == 1 && tasks[0].GateWorkflow == nil && len(turns) == 1 {
		projectGateV3Outputs(&turns[0], step.Outputs)
		turns[0].Status = "answered"
	} else {
		turns = append(turns, resultTurn)
	}
	gate.State, gate.FinishedAt = ExecutionSucceeded, &now
	gate.Turns = turns
	return gate, nil
}

func projectGateV3HumanTasks(tasks []workflows.WorkflowHumanTask) []GateTurn {
	turns := make([]GateTurn, 0, len(tasks))
	for _, task := range tasks {
		if task.GateForm == nil {
			continue
		}
		status := task.Status
		if status == workflows.HumanTaskStatusAnswered {
			status = "answered"
		}
		turn := GateTurn{
			StageID: task.StepID, Kind: task.ActorKind, Title: task.Title, Status: status,
			ActorKind: task.ActorKind, ExecutionID: task.ExecutionID,
			ActionRevision: task.ActionRevision, InputHash: task.InputHash,
			GateForm: &GateForm{
				GateRef: task.GateForm.GateRef, Prompt: task.GateForm.Prompt,
				Fields: clonePRLifecycleGateDefinition(gatetypes.GateDefinition{
					Fields: task.GateForm.Fields,
				}).Fields,
			},
		}
		if response, ok := task.Response.(map[string]any); ok {
			if values, ok := response["field-values"].(map[string]any); ok {
				turn.FieldValues = cloneAnyMap(values)
			}
		}
		turns = append(turns, turn)
	}
	return turns
}

func onlyGateStepExecution(run *workflows.Run) (workflows.StepExecution, bool) {
	if run == nil {
		return workflows.StepExecution{}, false
	}
	for _, step := range run.Steps {
		return step, true
	}
	return workflows.StepExecution{}, false
}

func projectGateV3Outputs(turn *GateTurn, outputs map[string]any) {
	if turn == nil || len(outputs) == 0 {
		return
	}
	if fieldValues, ok := outputs["field-values"].(map[string]any); ok {
		turn.FieldValues = cloneAnyMap(fieldValues)
	}
	turn.ActorKind, _ = outputs["actor-kind"].(string)
	turn.ExecutionID, _ = outputs["execution-id"].(string)
	turn.ActionRevision, _ = outputs["action-revision"].(string)
	turn.InputHash, _ = outputs["input-hash"].(string)
	if turn.ActorKind != "" {
		turn.Kind = turn.ActorKind
	}
}

func (evaluator *WorkflowGateEvaluator) now() time.Time {
	if evaluator.Now != nil {
		return evaluator.Now().UTC()
	}
	return time.Now().UTC()
}

var _ workflows.GateActionResolver = fixedPRLifecycleGateActionResolver{}
var _ GateEvaluator = (*WorkflowGateEvaluator)(nil)
