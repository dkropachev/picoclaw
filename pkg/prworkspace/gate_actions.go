package prworkspace

import (
	"fmt"

	"github.com/sipeed/picoclaw/pkg/workflows"
)

var prLifecycleApplicationActions = map[string][]string{
	"pr.charter.confirm":            {"approve", "revise", "stop"},
	"pr.charter.reconfirm":          {"approve", "revise", "stop"},
	"pr.review.start":               {"continue", "stop"},
	"pr.review.complete":            {"accept", "revise", "stop"},
	"pr.finding.classify":           {"keep-in-pr", "defer-follow-up", "dismiss", "revise-charter"},
	"pr.implementation.eligibility": {"authorize", "decline", "stop"},
	"pr.implementation.start":       {"continue", "stop"},
	"pr.implementation.scope":       {"approve", "defer-follow-up", "revise-charter", "stop"},
	"pr.implementation.complete":    {"accept", "revise", "stop"},
	"pr.review.publish":             {"publish", "revise", "stop"},
	"pr.implementation.publish":     {"publish", "revise", "stop"},
	"pr.deferred.publish":           {"publish", "revise", "stop"},
	"pr.correction.promote":         {"promote", "revise", "stop"},
	"pr.publication.reconcile":      {"recheck-provider", "assume-failed"},
	"pr.implementation.hard-scope":  {"defer-follow-up", "revise-charter", "stop"},
}

func applicationGateActions(decisionPoint string) []string {
	return append([]string(nil), prLifecycleApplicationActions[decisionPoint]...)
}

func gateAction(gate GateRun) string {
	for index := len(gate.Turns) - 1; index >= 0; index-- {
		if action, ok := gate.Turns[index].FieldValues["action"].(string); ok {
			return action
		}
	}
	return ""
}

func gateCompletedWith(gate GateRun, actions ...string) bool {
	if gate.State != ExecutionSucceeded {
		return false
	}
	actual := gateAction(gate)
	for _, action := range actions {
		if actual == action {
			return true
		}
	}
	return false
}

func gateProgressAction(decisionPoint string) string {
	switch decisionPoint {
	case "pr.charter.confirm", "pr.charter.reconfirm", "pr.implementation.scope":
		return "approve"
	case "pr.review.start", "pr.implementation.start":
		return "continue"
	case "pr.review.complete", "pr.implementation.complete":
		return "accept"
	case "pr.finding.classify":
		return "keep-in-pr"
	case "pr.implementation.eligibility":
		return "authorize"
	case "pr.review.publish", "pr.implementation.publish", "pr.deferred.publish":
		return "publish"
	case "pr.correction.promote":
		return "promote"
	case "pr.publication.reconcile":
		return "recheck-provider"
	default:
		return ""
	}
}

func gateAllowsProgress(gate GateRun) bool {
	action := gateProgressAction(gate.DecisionPoint)
	return action != "" && gateCompletedWith(gate, action)
}

func validateSubmittedGateFieldValues(gate GateRun, values map[string]any) (map[string]any, error) {
	if values == nil {
		return nil, fmt.Errorf("field-values are required")
	}
	for _, turn := range gate.Turns {
		if turn.Status != "waiting" || turn.GateForm == nil {
			continue
		}
		return workflows.ValidateGateFieldValues(turn.GateForm.Fields, values)
	}
	return nil, fmt.Errorf("gate has no waiting form")
}
