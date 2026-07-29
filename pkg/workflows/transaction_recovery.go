package workflows

import (
	"bytes"
	"errors"
	"fmt"
)

// ErrWorkflowRecoveryConflict means crash recovery found a file state that
// belongs to neither side of the interrupted transaction. The journal and all
// current files are left in place for explicit operator reconciliation.
var ErrWorkflowRecoveryConflict = errors.New("workflow transaction recovery conflict")

type workflowFileRecoveryTransition struct {
	label     string
	path      string
	preimage  workflowTemplateFileSnapshot
	postimage workflowTemplateFileSnapshot
}

type workflowFileRecoveryPlan struct {
	transition workflowFileRecoveryTransition
	current    workflowTemplateFileSnapshot
	restore    bool
}

func recoverWorkflowFileTransitions(
	transitions ...workflowFileRecoveryTransition,
) error {
	plans := make([]workflowFileRecoveryPlan, 0, len(transitions))
	for _, transition := range transitions {
		current, err := captureWorkflowTemplateFile(transition.path)
		if err != nil {
			return workflowRecoveryConflict(transition.label)
		}
		matchesPreimage := workflowTemplateFileSnapshotsEqual(
			current,
			transition.preimage,
		)
		if !matchesPreimage &&
			!workflowTemplateFileSnapshotsEqual(current, transition.postimage) {
			return workflowRecoveryConflict(transition.label)
		}
		plans = append(plans, workflowFileRecoveryPlan{
			transition: transition,
			current:    current,
			restore:    !matchesPreimage,
		})
	}

	// Recheck every path before changing any of them. This cannot turn a set of
	// filesystem writes into a single atomic operation, but it closes the
	// ordinary inspect-then-restore window for non-cooperating editors.
	for _, plan := range plans {
		current, err := captureWorkflowTemplateFile(plan.transition.path)
		if err != nil ||
			!workflowTemplateFileSnapshotsEqual(current, plan.current) {
			return workflowRecoveryConflict(plan.transition.label)
		}
	}

	var restoreErrors []error
	for _, plan := range plans {
		if !plan.restore {
			continue
		}
		preimage := plan.transition.preimage
		preimage.path = plan.transition.path
		if err := restoreWorkflowTemplateFile(preimage); err != nil {
			restoreErrors = append(restoreErrors, err)
		}
	}
	return errors.Join(restoreErrors...)
}

func workflowTemplateFileSnapshotsEqual(
	left workflowTemplateFileSnapshot,
	right workflowTemplateFileSnapshot,
) bool {
	if left.exists != right.exists {
		return false
	}
	if !left.exists {
		return true
	}
	return normalizeWorkflowTransactionFileMode(left.mode) ==
		normalizeWorkflowTransactionFileMode(right.mode) &&
		bytes.Equal(left.data, right.data)
}

func workflowRecoveryConflict(label string) error {
	return fmt.Errorf("%w: %s state changed", ErrWorkflowRecoveryConflict, label)
}
