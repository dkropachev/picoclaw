package prworkspace

import (
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"fmt"
	"strings"

	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/prlifecycle"
	"github.com/sipeed/picoclaw/pkg/workflows"
	"github.com/sipeed/picoclaw/pkg/workflows/gatetypes"
)

const (
	PRLifecycleWorkflowRef      = config.PRLifecycleWorkflowRef
	prLifecycleHardScopeGateRef = "gates.implementation-hard-scope"
)

// prLifecycleGateWorkflowYAML is the built-in application-owned gate catalog.
// Each PR lifecycle invocation compiles exactly one referenced gate from this
// immutable source; configuration may replace only that gate's action.
//
//go:embed pr_lifecycle_gates_v3.yaml
var prLifecycleGateWorkflowYAML []byte

// PRLifecycleGateCatalogEntry joins the existing domain decision point to the
// static workflow gate it executes. DecisionPoint remains application routing
// metadata; GateRef is the only configuration identity.
type PRLifecycleGateCatalogEntry struct {
	DecisionPoint     string
	WorkflowRef       string
	WorkflowRevision  string
	GateRef           string
	Gate              gatetypes.GateDefinition
	SourceAISupported bool
}

var prLifecycleDecisionGateRefs = map[string]string{
	"pr.charter.confirm":            "gates.charter-confirm",
	"pr.charter.reconfirm":          "gates.charter-reconfirm",
	"pr.review.start":               "gates.review-start",
	"pr.review.complete":            "gates.review-complete",
	"pr.finding.classify":           "gates.finding-classify",
	"pr.implementation.eligibility": "gates.implementation-eligibility",
	"pr.implementation.start":       "gates.implementation-start",
	"pr.implementation.scope":       "gates.implementation-scope",
	"pr.implementation.complete":    "gates.implementation-complete",
	"pr.review.publish":             "gates.review-publish",
	"pr.implementation.publish":     "gates.implementation-publish",
	"pr.deferred.publish":           "gates.deferred-publish",
	"pr.correction.promote":         "gates.correction-promote",
	"pr.publication.reconcile":      "gates.publication-reconcile",
	"pr.implementation.hard-scope":  "gates.implementation-hard-scope",
}

func loadPRLifecycleGateWorkflow() (*workflows.Workflow, string, error) {
	workflow, err := workflows.Parse(prLifecycleGateWorkflowYAML)
	if err != nil {
		return nil, "", fmt.Errorf("parse built-in PR lifecycle gate workflow: %w", err)
	}
	digest := sha256.Sum256(prLifecycleGateWorkflowYAML)
	revision := "sha256:" + hex.EncodeToString(digest[:])
	if err := validatePRLifecycleGateCatalog(workflow); err != nil {
		return nil, "", err
	}
	if _, err := workflows.CompileGateWorkflowV3(workflow, "gates.charter-confirm", map[string]any{}); err != nil {
		return nil, "", fmt.Errorf("validate built-in PR lifecycle gate workflow: %w", err)
	}
	return workflow, revision, nil
}

func validatePRLifecycleGateCatalog(workflow *workflows.Workflow) error {
	if workflow == nil || len(workflow.Gates) == 0 {
		return fmt.Errorf("built-in PR lifecycle workflow has no gates")
	}
	points := prlifecycle.DecisionPoints()
	if len(points) != len(prLifecycleDecisionGateRefs) || len(workflow.Gates) != len(points) {
		return fmt.Errorf("built-in PR lifecycle gate catalog is incomplete")
	}
	seen := make(map[string]struct{}, len(points))
	for _, point := range points {
		gateRef, exists := prLifecycleDecisionGateRefs[point.ID]
		if !exists || !strings.HasPrefix(gateRef, "gates.") {
			return fmt.Errorf("built-in PR lifecycle decision point %q has no static gate-ref", point.ID)
		}
		gateID := strings.TrimPrefix(gateRef, "gates.")
		gate, exists := workflow.Gates[gateID]
		if !exists {
			return fmt.Errorf("built-in PR lifecycle gate-ref %q does not exist", gateRef)
		}
		if _, duplicate := seen[gateID]; duplicate {
			return fmt.Errorf("built-in PR lifecycle gate-ref %q is reused", gateRef)
		}
		seen[gateID] = struct{}{}
		if strings.TrimSpace(gate.Prompt) == "" || gate.DefaultAction == nil {
			return fmt.Errorf("built-in PR lifecycle gate-ref %q lacks prompt or default-action", gateRef)
		}
		if err := gatetypes.ValidateGateAction(*gate.DefaultAction); err != nil {
			return fmt.Errorf("built-in PR lifecycle gate-ref %q default-action: %w", gateRef, err)
		}
		if gate.DefaultAction.Type == gatetypes.GateActionDeterministic {
			if _, err := workflows.ValidateGateFieldValues(gate.Fields, gate.DefaultAction.Fields); err != nil {
				return fmt.Errorf("built-in PR lifecycle gate-ref %q deterministic default: %w", gateRef, err)
			}
		}
		if err := validatePRLifecycleApplicationActionField(point.ID, gateRef, gate.Fields); err != nil {
			return err
		}
	}
	return nil
}

func validatePRLifecycleApplicationActionField(
	decisionPoint string,
	gateRef string,
	fields []gatetypes.GateField,
) error {
	expected := applicationGateActions(decisionPoint)
	if len(expected) == 0 {
		return fmt.Errorf("built-in PR lifecycle decision point %q has no application actions", decisionPoint)
	}
	var actionField *gatetypes.GateField
	for index := range fields {
		if fields[index].ID == "action" {
			actionField = &fields[index]
			break
		}
	}
	if actionField == nil || actionField.Type != gatetypes.GateFieldSelect ||
		actionField.MinSelections != 1 || actionField.MaxSelections != 1 {
		return fmt.Errorf("built-in PR lifecycle gate-ref %q requires one action select", gateRef)
	}
	actual := make(map[string]struct{}, len(actionField.Options))
	for _, option := range actionField.Options {
		actual[option.ID] = struct{}{}
	}
	if len(actual) != len(expected) {
		return fmt.Errorf("built-in PR lifecycle gate-ref %q action options do not match application routing", gateRef)
	}
	for _, action := range expected {
		if _, exists := actual[action]; !exists {
			return fmt.Errorf("built-in PR lifecycle gate-ref %q lacks application action %q", gateRef, action)
		}
	}
	return nil
}

// PRLifecycleGateCatalog returns a detached, ordered catalog suitable for UI
// metadata and exact configuration binding validation.
func PRLifecycleGateCatalog() ([]PRLifecycleGateCatalogEntry, error) {
	workflow, revision, err := loadPRLifecycleGateWorkflow()
	if err != nil {
		return nil, err
	}
	result := make([]PRLifecycleGateCatalogEntry, 0, len(prLifecycleDecisionGateRefs))
	for _, point := range prlifecycle.DecisionPoints() {
		gateRef := prLifecycleDecisionGateRefs[point.ID]
		gate := workflow.Gates[strings.TrimPrefix(gateRef, "gates.")]
		result = append(result, PRLifecycleGateCatalogEntry{
			DecisionPoint:     point.ID,
			WorkflowRef:       PRLifecycleWorkflowRef,
			WorkflowRevision:  revision,
			GateRef:           gateRef,
			Gate:              clonePRLifecycleGateDefinition(gate),
			SourceAISupported: point.ID == "pr.finding.classify",
		})
	}
	return result, nil
}

func prLifecycleGateCatalogEntry(decisionPoint string) (PRLifecycleGateCatalogEntry, error) {
	catalog, err := PRLifecycleGateCatalog()
	if err != nil {
		return PRLifecycleGateCatalogEntry{}, err
	}
	for _, entry := range catalog {
		if entry.DecisionPoint == decisionPoint {
			return entry, nil
		}
	}
	return PRLifecycleGateCatalogEntry{}, fmt.Errorf("unknown PR lifecycle decision point %q", decisionPoint)
}

func clonePRLifecycleGateDefinition(gate gatetypes.GateDefinition) gatetypes.GateDefinition {
	gate.Fields = append([]gatetypes.GateField(nil), gate.Fields...)
	for index := range gate.Fields {
		gate.Fields[index].Options = append([]gatetypes.GateFieldOption(nil), gate.Fields[index].Options...)
	}
	if gate.DefaultAction != nil {
		action := *gate.DefaultAction
		action.Fields = cloneAnyMap(action.Fields)
		gate.DefaultAction = &action
	}
	return gate
}

func cloneAnyMap(input map[string]any) map[string]any {
	if input == nil {
		return nil
	}
	result := make(map[string]any, len(input))
	for key, value := range input {
		result[key] = value
	}
	return result
}
