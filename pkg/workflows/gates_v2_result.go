package workflows

import (
	"errors"
	"fmt"
)

// ErrGateWorkflowV2Incomplete means a staged workflow has not produced a
// terminal typed outcome yet.
var ErrGateWorkflowV2Incomplete = errors.New("gate v2 workflow outcome is incomplete")

// ResolveGateWorkflowV2Outcome reads one compiler result and its exact private
// run. It returns the first non-pass stage or the final all-of pass. Product
// domains persist that typed result separately before changing lifecycle state.
func ResolveGateWorkflowV2Outcome(
	compilation *GateWorkflowV2Compilation,
	run *Run,
) (GateOutcome, string, error) {
	if compilation == nil {
		return "", "", fmt.Errorf("gate v2 compilation is required")
	}
	if compilation.ImmediateOutcome != "" {
		if !validGateWorkflowV2Outcome(compilation.ImmediateOutcome) {
			return "", "", fmt.Errorf("gate v2 immediate outcome is invalid")
		}
		return compilation.ImmediateOutcome, compilation.ImmediateStageID, nil
	}
	if run == nil || run.privateRoot == nil {
		return "", "", ErrGateWorkflowV2Incomplete
	}
	for _, stage := range compilation.Stages {
		switch stage.Kind {
		case GateZero:
			continue
		case GateDeterministic:
			passed, err := evalIf(
				stage.PassCondition,
				expressionContext{Private: run.privateRoot.Values},
			)
			if err != nil {
				return "", "", fmt.Errorf("resolve gate v2 deterministic stage %q: %w", stage.ID, err)
			}
			if !passed {
				return GateOutcomeBlock, stage.ID, nil
			}
		case GateAIWorkingContext, GateAIIsolatedContext, GateHuman:
			step, exists := run.Steps[workflowGateJobID+"/"+stage.StepID]
			if !exists || step.Status == RunStatusWaiting || step.Status == RunStatusRunning {
				return "", "", ErrGateWorkflowV2Incomplete
			}
			if step.Status != RunStatusSucceeded {
				return "", "", fmt.Errorf(
					"gate v2 stage %q has terminal workflow status %q",
					stage.ID,
					step.Status,
				)
			}
			outcome, err := gateWorkflowV2StepOutcome(stage.Kind, step.Outputs)
			if err != nil {
				return "", "", fmt.Errorf("resolve gate v2 stage %q: %w", stage.ID, err)
			}
			if outcome != GateOutcomePass {
				return outcome, stage.ID, nil
			}
		default:
			return "", "", fmt.Errorf("gate v2 stage %q has unsupported kind %q", stage.ID, stage.Kind)
		}
	}
	job, exists := run.Jobs[workflowGateJobID]
	if !exists || job.Outputs[workflowGateV2PassedJobOutput] != true {
		return "", "", ErrGateWorkflowV2Incomplete
	}
	return GateOutcomePass, "", nil
}

func gateWorkflowV2StepOutcome(kind GateKind, outputs map[string]any) (GateOutcome, error) {
	var value any
	switch kind {
	case GateAIWorkingContext, GateAIIsolatedContext:
		structured, ok := outputs["structured"].(map[string]any)
		if !ok {
			return "", errors.New("AI stage structured output is missing")
		}
		value = structured["outcome"]
	case GateHuman:
		response, ok := outputs["response"].(map[string]any)
		if !ok {
			return "", errors.New("human stage response is missing")
		}
		value = response["decision"]
	default:
		return "", fmt.Errorf("stage kind %q has no typed output", kind)
	}
	outcome, ok := value.(string)
	if !ok || !validGateWorkflowV2Outcome(GateOutcome(outcome)) {
		return "", errors.New("stage outcome is invalid")
	}
	return GateOutcome(outcome), nil
}

func validGateWorkflowV2Outcome(outcome GateOutcome) bool {
	switch outcome {
	case GateOutcomePass, GateOutcomeRevise, GateOutcomeDefer, GateOutcomeBlock:
		return true
	default:
		return false
	}
}
