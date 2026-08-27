package tools

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
)

type applyPatchTxnIntent struct {
	index   int
	planned plannedApplyPatchOp

	source             *applyPatchTxnEndpoint
	sourceWitnessName  string
	sourceProbeWitness string
	sourceQuarantine   string
	sourceRestoreStage string
	backupName         string

	targetLayout    applyPatchTxnTargetLayout
	targetAnchor    *applyPatchTxnAnchor
	stageName       string
	postWitnessName string
	targetRollback  string
	forest          *applyPatchTxnForestIntent
	beforeSHA256    [sha256.Size]byte
	afterSHA256     [sha256.Size]byte
}

type applyPatchTxnForestIntent struct {
	id                   string
	anchorPath           string
	anchor               *applyPatchTxnAnchor
	publicRoot           string
	stageRoot            string
	rollbackRoot         string
	sentinelRelativePath string
	sentinelWitnessName  string
	operations           []*applyPatchTxnIntent
}

type applyPatchTxnIntentPlan struct {
	id            string
	activeName    string
	committedName string
	plan          *applyPatchPlan
	operations    []*applyPatchTxnIntent
	forests       []*applyPatchTxnForestIntent
}

func (intent *applyPatchTxnIntentPlan) Close() error {
	if intent == nil {
		return nil
	}
	var closeErr error
	for _, operation := range intent.operations {
		if operation == nil {
			continue
		}
		if operation.source != nil {
			closeErr = errors.Join(closeErr, operation.source.Close())
			operation.source = nil
		}
		if operation.targetAnchor != nil {
			closeErr = errors.Join(closeErr, operation.targetAnchor.Close())
			operation.targetAnchor = nil
		}
	}
	for _, forest := range intent.forests {
		if forest != nil && forest.anchor != nil {
			closeErr = errors.Join(closeErr, forest.anchor.Close())
			forest.anchor = nil
		}
	}
	return closeErr
}

func buildApplyPatchTxnIntent(
	ctx context.Context,
	plan *applyPatchPlan,
) (*applyPatchTxnIntentPlan, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if plan == nil || len(plan.ops) == 0 ||
		len(plan.ops) > applyPatchCandidateMaxOperations {
		return nil, errors.New("apply-patch transaction plan is invalid")
	}
	transactionID, err := newApplyPatchTxnID()
	if err != nil {
		return nil, err
	}
	intent := &applyPatchTxnIntentPlan{
		id:            transactionID,
		activeName:    "active-" + transactionID,
		committedName: "committed-" + transactionID,
		plan:          plan,
		operations:    make([]*applyPatchTxnIntent, 0, len(plan.ops)),
	}
	failed := true
	defer func() {
		if failed {
			_ = intent.Close()
		}
	}()

	forestByKey := make(map[string]*applyPatchTxnForestIntent)
	for index := range plan.ops {
		if contextErr := ctx.Err(); contextErr != nil {
			return nil, contextErr
		}
		operation, buildErr := buildApplyPatchTxnOperationIntent(
			ctx,
			index,
			plan.ops[index],
		)
		if buildErr != nil {
			return nil, buildErr
		}
		intent.operations = append(intent.operations, operation)
		if len(operation.targetLayout.components) <= 1 {
			continue
		}
		key, keyErr := applyPatchTxnLayoutGroupKey(operation.targetLayout)
		if keyErr != nil {
			return nil, keyErr
		}
		forest := forestByKey[key]
		if forest == nil {
			forest, keyErr = buildApplyPatchTxnForestIntent(operation.targetLayout)
			if keyErr != nil {
				return nil, keyErr
			}
			forestByKey[key] = forest
			intent.forests = append(intent.forests, forest)
		}
		operation.forest = forest
		forest.operations = append(forest.operations, operation)
	}
	for _, forest := range intent.forests {
		sort.SliceStable(forest.operations, func(left, right int) bool {
			return forest.operations[left].index < forest.operations[right].index
		})
		sentinel := forest.operations[0]
		forest.sentinelRelativePath = filepath.ToSlash(filepath.Join(
			sentinel.targetLayout.components[1:]...,
		))
	}
	failed = false
	return intent, nil
}

func buildApplyPatchTxnOperationIntent(
	ctx context.Context,
	index int,
	planned plannedApplyPatchOp,
) (*applyPatchTxnIntent, error) {
	operation := &applyPatchTxnIntent{
		index: index, planned: planned,
		beforeSHA256: sha256.Sum256(planned.before),
		afterSHA256:  sha256.Sum256(planned.after),
	}
	failed := true
	defer func() {
		if failed {
			if operation.source != nil {
				_ = operation.source.Close()
			}
			if operation.targetAnchor != nil {
				_ = operation.targetAnchor.Close()
			}
		}
	}()

	if planned.source != nil {
		source, err := openApplyPatchTxnExistingRegular(
			planned.sourcePath,
			planned.source.info,
		)
		if err != nil {
			return nil, fmt.Errorf("prepare %s source %q: %w", planned.kind, planned.sourceLabel, err)
		}
		operation.source = source
		data, mode, identity, err := applyPatchTxnReadRegular(
			source.anchor,
			source.basename,
			int64(len(planned.source.data)),
		)
		if err != nil || !identity.equal(source.state.Identity) ||
			mode.Perm() != planned.source.mode.Perm() ||
			!bytes.Equal(data, planned.source.data) ||
			source.state.Links != planned.source.linkCount {
			return nil, errors.Join(
				fmt.Errorf("patch source %q changed before transaction preparation", planned.sourceLabel),
				err,
			)
		}
		var nameErr error
		operation.sourceWitnessName, nameErr = newApplyPatchTxnPrivateName("source-witness")
		if nameErr != nil {
			return nil, nameErr
		}
		operation.sourceProbeWitness, nameErr = newApplyPatchTxnPrivateName(
			"source-probe-witness",
		)
		if nameErr != nil {
			return nil, nameErr
		}
		operation.sourceQuarantine, nameErr = newApplyPatchTxnPrivateName("source-quarantine")
		if nameErr != nil {
			return nil, nameErr
		}
		operation.sourceRestoreStage, nameErr = newApplyPatchTxnPrivateName("source-restore")
		if nameErr != nil {
			return nil, nameErr
		}
		operation.backupName, nameErr = newApplyPatchTxnPrivateName("backup")
		if nameErr != nil {
			return nil, nameErr
		}
	}

	if planned.targetPath != "" {
		layout, err := resolveApplyPatchTxnTargetLayout(planned.targetPath)
		if err != nil {
			return nil, fmt.Errorf("prepare %s target %q: %w", planned.kind, planned.targetLabel, err)
		}
		operation.targetLayout = layout
		if len(layout.components) == 1 {
			anchor, openErr := openApplyPatchTxnAnchor(layout.anchorPath)
			if openErr != nil {
				return nil, openErr
			}
			operation.targetAnchor = anchor
			if planned.kind != "update" {
				_, _, inspectErr := applyPatchTxnIdentityAt(anchor, layout.components[0])
				if inspectErr == nil || !errors.Is(inspectErr, os.ErrNotExist) {
					return nil, errors.Join(
						fmt.Errorf("patch target %q changed before transaction preparation", planned.targetLabel),
						inspectErr,
					)
				}
			}
			var nameErr error
			operation.stageName, nameErr = newApplyPatchTxnPrivateName("stage")
			if nameErr != nil {
				return nil, nameErr
			}
			operation.postWitnessName, nameErr = newApplyPatchTxnPrivateName("post-witness")
			if nameErr != nil {
				return nil, nameErr
			}
			operation.targetRollback, nameErr = newApplyPatchTxnPrivateName("target-rollback")
			if nameErr != nil {
				return nil, nameErr
			}
		}
	}
	failed = false
	return operation, ctx.Err()
}

func buildApplyPatchTxnForestIntent(
	layout applyPatchTxnTargetLayout,
) (*applyPatchTxnForestIntent, error) {
	if len(layout.components) <= 1 {
		return nil, errors.New("apply-patch transaction forest layout is invalid")
	}
	anchor, err := openApplyPatchTxnAnchor(layout.anchorPath)
	if err != nil {
		return nil, err
	}
	failed := true
	defer func() {
		if failed {
			_ = anchor.Close()
		}
	}()
	_, _, inspectErr := applyPatchTxnIdentityAt(anchor, layout.components[0])
	if inspectErr == nil || !errors.Is(inspectErr, os.ErrNotExist) {
		return nil, errors.Join(
			errors.New("apply-patch transaction forest target changed"),
			inspectErr,
		)
	}
	forestID, err := newApplyPatchTxnID()
	if err != nil {
		return nil, err
	}
	stageRoot, err := newApplyPatchTxnPrivateName("forest-stage")
	if err != nil {
		return nil, err
	}
	rollbackRoot, err := newApplyPatchTxnPrivateName("forest-rollback")
	if err != nil {
		return nil, err
	}
	witness, err := newApplyPatchTxnPrivateName("forest-witness")
	if err != nil {
		return nil, err
	}
	failed = false
	return &applyPatchTxnForestIntent{
		id: forestID, anchorPath: layout.anchorPath, anchor: anchor,
		publicRoot: layout.components[0], stageRoot: stageRoot,
		rollbackRoot: rollbackRoot, sentinelWitnessName: witness,
	}, nil
}
