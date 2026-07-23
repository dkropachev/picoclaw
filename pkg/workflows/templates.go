package workflows

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const (
	CodeReviewWorkflowName = "code-review"
	CodeReviewWorkflowRef  = "workflows/code-review.yml"
)

const CodeReviewWorkflowYAML = `name: Code Review
on:
  manual: {}
  workflow_call:
    inputs:
      action:
        type: string
        default: plan
      repository:
        type: string
        required: true
      ref:
        type: string
        default: ""
      base_ref:
        type: string
        default: ""
      target:
        type: string
        default: code
      review_focus:
        type: string
        default: "Review correctness, security, test coverage, and maintainability."
    outputs:
      inventory:
        value: ${{ jobs.code_review.outputs.inventory }}
      inventoryJson:
        value: ${{ jobs.code_review.outputs.inventoryJson }}
      filter:
        value: ${{ jobs.code_review.outputs.filter }}
      filterJson:
        value: ${{ jobs.code_review.outputs.filterJson }}
      filterSummary:
        value: ${{ jobs.code_review.outputs.filterSummary }}
      reviewJson:
        value: ${{ jobs.code_review.outputs.reviewJson }}
      managed:
        value: ${{ jobs.code_review.outputs.managed }}
      reviewNeeded:
        value: ${{ jobs.code_review.outputs.reviewNeeded }}
      summary:
        value: ${{ jobs.code_review.outputs.summary }}
      workspacePath:
        value: ${{ jobs.code_review.outputs.workspacePath }}
      inventoryHash:
        value: ${{ jobs.code_review.outputs.inventoryHash }}
jobs:
  code_review:
    name: Inventory and optional review
    runs-on: picoclaw
    outputs:
      inventory: ${{ steps.selection.outputs }}
      inventoryJson: ${{ steps.store_inventory.outputs.relativePath }}
      filter: ${{ steps.plan_filter.outputs.structured }}
      filterJson: ${{ steps.store_filter.outputs.relativePath }}
      filterSummary: ${{ steps.plan_filter.outputs.structured.rationale }}
      reviewJson: ${{ steps.store_review.outputs.relativePath }}
      managed: ${{ steps.review.outputs.managed }}
      reviewNeeded: ${{ inputs.action == 'review' }}
      summary: ${{ steps.review.outputs.structured.summary }}
      workspacePath: ${{ steps.checkout.outputs.workspace.path }}
      inventoryHash: ${{ steps.inventory.outputs.inventoryHash }}
    steps:
      - id: checkout
        name: Acquire git workspace
        uses: tool/git_workspace
        with:
          action: acquire
          repository: ${{ inputs.repository }}
          ref: ${{ inputs.ref }}
      - id: inventory
        name: Build repository structure inventory
        uses: function/git.inventory
        with:
          workspace: ${{ steps.checkout.outputs.workspace }}
          target: all
      - id: selection
        name: Select requested inventory target
        uses: function/git.filter
        with:
          workspace: ${{ steps.checkout.outputs.workspace }}
          files: ${{ steps.inventory.outputs.files }}
          commit: ${{ steps.inventory.outputs.commit }}
          inventory_hash: ${{ steps.inventory.outputs.inventoryHash }}
          target: ${{ inputs.target }}
          filter: {}
      - id: save_inventory_state
        name: Save latest inventory state
        uses: function/workflow.state
        with:
          action: set
          key: code_review:last_inventory
          value: ${{ steps.selection.outputs }}
      - id: store_inventory
        name: Store inventory artifact
        uses: function/workflow.artifact
        with:
          action: write
          format: json
          name: code-review/inventories/${{ steps.inventory.outputs.inventoryHash }}.json
          value: ${{ steps.selection.outputs }}
      - id: release_structure
        name: Release structure workspace
        uses: tool/git_workspace
        with:
          action: release
      - id: plan_filter
        name: Ask AI to plan review filter
        if: ${{ inputs.action == 'review' }}
        uses: agent/main
        with:
          session: key:workflow-code-review-filter
          history: none
          cache: session
          prompt: |
            You are selecting files for a Codex-style code review.

            The assigned scope contains repository-relative file metadata only: path, category, mode, hash, size, source reference, and deterministic selected flag. It does not contain file content.

            Return a path filter as JSON:
            - includeGlobs chooses candidate files for the requested review target.
            - excludeGlobs removes generated files, examples, fixtures, snapshots, vendored code, build outputs, test data, mocks, and other low-signal files.
            - rationale briefly explains the policy.

            Rules:
            - Use glob patterns over repository-relative paths.
            - Prefer broad stable globs over enumerating every file.
            - Use ** for recursive path segments.
            - Do not use tools or inspect file content.
            - Keep production runtime code and important runtime configuration.
            - For target "code", exclude tests, test data, fixtures, mocks, examples, generated files, and documentation unless they are clearly part of runtime behavior.
            - For target "tests", include tests and test helpers but exclude generated snapshots, huge fixtures, and build outputs.
            - For target "all", include useful code and tests while excluding generated or low-signal files.
          context: |
            Repository: ${{ inputs.repository }}
            Requested ref: ${{ inputs.ref }}
            Base ref: ${{ inputs.base_ref }}
            Requested target: ${{ inputs.target }}
            Review focus: ${{ inputs.review_focus }}
            Commit: ${{ steps.inventory.outputs.commit }}
            Total files: ${{ steps.inventory.outputs.counts.totalFiles }}
            Deterministic selected files: ${{ steps.selection.outputs.counts.totalSelectedFiles }}
          scope: ${{ steps.inventory.outputs.files }}
          output:
            format: json
            repair_attempts: 1
            schema:
              type: object
              required: [includeGlobs, excludeGlobs, rationale]
              properties:
                includeGlobs:
                  type: array
                  items:
                    type: string
                excludeGlobs:
                  type: array
                  items:
                    type: string
                selectedPaths:
                  type: array
                  items:
                    type: string
                rationale:
                  type: string
      - id: store_filter
        name: Store AI review filter
        if: ${{ inputs.action == 'review' }}
        uses: function/workflow.artifact
        with:
          action: write
          format: json
          name: code-review/filters/${{ steps.inventory.outputs.inventoryHash }}.json
          value: ${{ steps.plan_filter.outputs.structured }}
      - id: review_checkout
        name: Acquire review workspace
        if: ${{ inputs.action == 'review' }}
        uses: tool/git_workspace
        with:
          action: acquire
          repository: ${{ inputs.repository }}
          ref: ${{ inputs.ref }}
      - id: review_inventory
        name: Apply AI filter and link review files
        if: ${{ inputs.action == 'review' }}
        uses: function/git.filter
        with:
          workspace: ${{ steps.review_checkout.outputs.workspace }}
          files: ${{ steps.inventory.outputs.files }}
          commit: ${{ steps.inventory.outputs.commit }}
          inventory_hash: ${{ steps.inventory.outputs.inventoryHash }}
          target: ${{ inputs.target }}
          filter: ${{ steps.plan_filter.outputs.structured }}
      - id: release_review
        name: Release review workspace
        if: ${{ inputs.action == 'review' }}
        uses: tool/git_workspace
        with:
          action: release
      - id: review
        name: Review selected files
        if: ${{ inputs.action == 'review' }}
        uses: agent/main
        with:
          managed:
            mode: auto
            strategy: auto
            max_items_per_chunk: 2
            max_parallel_children: 2
            estimated_output_tokens: 1200
            calibration:
              enabled: false
              sample_size: 3
              required_matches: 1
              max_trials: 1
            optimization:
              model:
                enabled: false
              effort:
                enabled: true
          session: key:workflow-code-review
          history: none
          cache: session
          prompt: |
            You are executing a Codex-style code review workflow.

            Review contract:
            - Review only files from the assigned scope.
            - Inspect each file by reading its assigned source.path; path is the repository-relative reporting path.
            - The assigned scope does not embed file content.
            - Use tools only for read-only file inspection and validation.
            - If a file source cannot be read, mention that as residual risk for that file.
            - Do not edit files and do not write review comments into source files.
            - Prioritize actionable bugs, security issues, reliability risks, data loss, concurrency problems, behavioral regressions, and missing tests.
            - Ignore pure style preferences and broad refactors unless they hide a concrete bug.
            - Findings must be concrete, reproducible, and tied to exact file paths and line numbers when possible.
            - Return findings first in priority order by severity.
            - If there are no actionable findings, return "findings": [] and explain residual risk in "residualRisks".

            PicoClaw acquired the repository with the git_workspace tool and released the workspace before this model step.
            Repository: ${{ inputs.repository }}
            Requested ref: ${{ inputs.ref }}
            Base ref: ${{ inputs.base_ref }}
            Review focus: ${{ inputs.review_focus }}
          context: |
            Workspace path: ${{ steps.review_checkout.outputs.workspace.path }}
            Commit: ${{ steps.inventory.outputs.commit }}
            Target: ${{ inputs.target }}
            Inventory hash: ${{ steps.inventory.outputs.inventoryHash }}
            Filter rationale: ${{ steps.plan_filter.outputs.structured.rationale }}
            Selected files: ${{ steps.review_inventory.outputs.counts.totalSelectedFiles }}
          scope: ${{ steps.review_inventory.outputs.selectedFiles }}
          output:
            format: json
            repair_attempts: 1
            schema:
              type: object
              required: [summary, findings, tests, residualRisks]
              properties:
                summary:
                  type: string
                findings:
                  type: array
                  items:
                    type: object
                    required: [severity, title, file, evidence, impact, recommendation]
                    properties:
                      severity:
                        type: string
                        enum: [critical, high, medium, low]
                      title:
                        type: string
                      file:
                        type: string
                      line:
                        type: integer
                      evidence:
                        type: string
                      impact:
                        type: string
                      message:
                        type: string
                      recommendation:
                        type: string
                      validation:
                        type: string
                tests:
                  type: array
                  items:
                    type: string
                residualRisks:
                  type: array
                  items:
                    type: string
      - id: store_review
        name: Store structured review artifact
        if: ${{ inputs.action == 'review' }}
        uses: function/workflow.artifact
        with:
          action: write
          format: json
          name: code-review/reviews/${{ steps.inventory.outputs.inventoryHash }}.json
          value: ${{ steps.review.outputs.structured }}
      - id: save_review_state
        name: Save latest review state
        if: ${{ inputs.action == 'review' }}
        uses: function/workflow.state
        with:
          action: set
          key: code_review:last_review
          value:
            inventory: ${{ steps.selection.outputs }}
            structureInventory: ${{ steps.inventory.outputs }}
            filterJson: ${{ steps.store_filter.outputs.relativePath }}
            filter: ${{ steps.plan_filter.outputs.structured }}
            reviewJson: ${{ steps.store_review.outputs.relativePath }}
            structuredReview: ${{ steps.review.outputs.structured }}
            managed: ${{ steps.review.outputs.managed }}
            rawReview: ${{ steps.review.outputs.text }}
`

type InstalledWorkflowTemplate struct {
	Name        string `json:"name"`
	Ref         string `json:"ref"`
	Path        string `json:"path"`
	Installed   bool   `json:"installed"`
	Overwritten bool   `json:"overwritten,omitempty"`
}

func InstallCodeReviewWorkflow(
	ctx context.Context,
	workspace string,
	overwrite bool,
	opts ...LocalOption,
) (*InstalledWorkflowTemplate, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := validateWorkflowTemplate(CodeReviewWorkflowYAML); err != nil {
		return nil, err
	}
	local := collectLocalOptions(opts...)
	resolved, err := local.resolver(workspace).ResolveLocal(CodeReviewWorkflowRef)
	if err != nil {
		return nil, err
	}
	result := &InstalledWorkflowTemplate{
		Name: CodeReviewWorkflowName,
		Ref:  resolved.Canonical,
		Path: resolved.Path,
	}
	if _, statErr := os.Stat(resolved.Path); statErr == nil && !overwrite {
		return result, nil
	} else if statErr != nil && !os.IsNotExist(statErr) {
		return nil, statErr
	} else if statErr == nil {
		result.Overwritten = true
	}
	if err := os.MkdirAll(filepath.Dir(resolved.Path), 0o755); err != nil {
		return nil, err
	}
	tmp := resolved.Path + ".tmp"
	if err := os.WriteFile(tmp, []byte(CodeReviewWorkflowYAML), 0o644); err != nil {
		return nil, err
	}
	if err := os.Rename(tmp, resolved.Path); err != nil {
		_ = os.Remove(tmp)
		return nil, err
	}
	result.Installed = true
	return result, nil
}

func InstallWorkflowTemplate(
	ctx context.Context,
	workspace string,
	name string,
	overwrite bool,
	opts ...LocalOption,
) (*InstalledWorkflowTemplate, error) {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "", CodeReviewWorkflowName:
		return InstallCodeReviewWorkflow(ctx, workspace, overwrite, opts...)
	default:
		return nil, fmt.Errorf("unknown workflow template %q", name)
	}
}

func validateWorkflowTemplate(raw string) error {
	workflow, err := Parse([]byte(raw))
	if err != nil {
		return err
	}
	return Validate(workflow)
}
