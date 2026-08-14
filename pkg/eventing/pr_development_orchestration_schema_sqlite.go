//go:build !mipsle && !netbsd && !(freebsd && arm)

package eventing

import (
	"context"
	"database/sql"
	"fmt"
)

const (
	schemaV14PRDevelopmentRepairOrchestrationCohortsTable = `CREATE TABLE IF NOT EXISTS pr_development_repair_orchestration_cohorts (
	session_id TEXT PRIMARY KEY REFERENCES pr_development_repair_sessions(id) ON DELETE RESTRICT,
	thread_id TEXT NOT NULL UNIQUE REFERENCES pr_development_threads(id) ON DELETE RESTRICT
);`
	schemaV14PRDevelopmentRepairOrchestrationsTable = `CREATE TABLE IF NOT EXISTS pr_development_repair_orchestrations (
	attempt_id TEXT PRIMARY KEY REFERENCES pr_development_repair_attempts(id) ON DELETE RESTRICT,
	session_id TEXT NOT NULL REFERENCES pr_development_repair_sessions(id) ON DELETE RESTRICT,
	case_id TEXT NOT NULL REFERENCES pr_development_cases(id) ON DELETE RESTRICT,
	thread_id TEXT NOT NULL REFERENCES pr_development_threads(id) ON DELETE RESTRICT,
	phase TEXT NOT NULL CHECK (phase IN (
		'bootstrap', 'editing', 'edited', 'validated', 'completed', 'failed', 'recovery_required'
	)),
	claim_owner TEXT NOT NULL DEFAULT '' CHECK (length(CAST(claim_owner AS BLOB)) <= 256),
	claim_token TEXT NOT NULL DEFAULT '' CHECK (length(CAST(claim_token AS BLOB)) <= 128),
	claim_until INTEGER,
	claim_epoch INTEGER NOT NULL DEFAULT 0 CHECK (claim_epoch >= 0),
	claims INTEGER NOT NULL DEFAULT 0 CHECK (claims >= 0 AND claims = claim_epoch),
	head_repository TEXT NOT NULL DEFAULT '' CHECK (length(CAST(head_repository AS BLOB)) <= 256),
	head_ref TEXT NOT NULL DEFAULT '' CHECK (length(CAST(head_ref AS BLOB)) <= 1024),
	head_sha TEXT NOT NULL DEFAULT '' CHECK (length(head_sha) IN (0, 40, 64)),
	clone_url TEXT NOT NULL DEFAULT '' CHECK (length(CAST(clone_url AS BLOB)) <= 4096),
	review_digest TEXT NOT NULL DEFAULT '' CHECK (length(CAST(review_digest AS BLOB)) <= 71),
	workspace_id TEXT NOT NULL DEFAULT '' CHECK (length(CAST(workspace_id AS BLOB)) <= 256),
	source_tree TEXT NOT NULL DEFAULT '' CHECK (length(source_tree) IN (0, 40, 64)),
	controller_id TEXT NOT NULL DEFAULT '' CHECK (length(CAST(controller_id AS BLOB)) <= 256),
	model_controller_revision INTEGER NOT NULL DEFAULT 0 CHECK (
		model_controller_revision >= 0 AND model_controller_revision <= 65536
	),
	model_line_id TEXT NOT NULL DEFAULT '' CHECK (length(CAST(model_line_id AS BLOB)) <= 256),
	model_line_version INTEGER NOT NULL DEFAULT 0 CHECK (
		model_line_version >= 0 AND model_line_version <= 8192
	),
	model_mutation_epoch INTEGER NOT NULL DEFAULT 0 CHECK (
		model_mutation_epoch >= 0 AND model_mutation_epoch <= 8193
	),
	model_lease_epoch INTEGER NOT NULL DEFAULT 0 CHECK (model_lease_epoch >= 0),
	model_lease_token_digest TEXT NOT NULL DEFAULT '' CHECK (
		length(model_lease_token_digest) IN (0, 64)
	),
	model_reservation_digest TEXT NOT NULL DEFAULT '' CHECK (
		length(model_reservation_digest) IN (0, 64)
	),
	context_digest TEXT NOT NULL DEFAULT '' CHECK (length(context_digest) IN (0, 64)),
	prompt_digest TEXT NOT NULL DEFAULT '' CHECK (length(prompt_digest) IN (0, 64)),
	model_result_digest TEXT NOT NULL DEFAULT '' CHECK (length(model_result_digest) IN (0, 64)),
	summary TEXT NOT NULL DEFAULT '' CHECK (length(CAST(summary AS BLOB)) <= 4096),
	iterations INTEGER NOT NULL DEFAULT 0 CHECK (iterations >= 0 AND iterations <= 128),
	validation_controller_revision INTEGER NOT NULL DEFAULT 0 CHECK (
		validation_controller_revision >= 0 AND validation_controller_revision <= 65536
	),
	validation_line_id TEXT NOT NULL DEFAULT '' CHECK (length(CAST(validation_line_id AS BLOB)) <= 256),
	validation_line_version INTEGER NOT NULL DEFAULT 0 CHECK (
		validation_line_version >= 0 AND validation_line_version <= 8192
	),
	validation_mutation_epoch INTEGER NOT NULL DEFAULT 0 CHECK (
		validation_mutation_epoch >= 0 AND validation_mutation_epoch <= 8193
	),
	validation_lease_epoch INTEGER NOT NULL DEFAULT 0 CHECK (validation_lease_epoch >= 0),
	validation_lease_token_digest TEXT NOT NULL DEFAULT '' CHECK (
		length(validation_lease_token_digest) IN (0, 64)
	),
	validation_reservation_digest TEXT NOT NULL DEFAULT '' CHECK (
		length(validation_reservation_digest) IN (0, 64)
	),
	parent_commit TEXT NOT NULL DEFAULT '' CHECK (length(parent_commit) IN (0, 40, 64)),
	parent_tree TEXT NOT NULL DEFAULT '' CHECK (length(parent_tree) IN (0, 40, 64)),
	candidate_tree TEXT NOT NULL DEFAULT '' CHECK (length(candidate_tree) IN (0, 40, 64)),
	candidate_digest TEXT NOT NULL DEFAULT '' CHECK (length(candidate_digest) IN (0, 64)),
	changed_files INTEGER NOT NULL DEFAULT 0 CHECK (changed_files >= 0 AND changed_files <= 10000),
	no_changes INTEGER CHECK (no_changes IS NULL OR no_changes IN (0, 1)),
	ci_status TEXT NOT NULL DEFAULT '' CHECK (
		ci_status IN (
			'', 'passed', 'failed', 'incomplete', 'plan_changed', 'timed_out',
			'canceled', 'output_limit_exceeded', 'environment_unavailable',
			'infrastructure_error'
		)
	),
	ci_attestation_id TEXT NOT NULL DEFAULT '' CHECK (
		length(CAST(ci_attestation_id AS BLOB)) <= 256
	),
	ci_attestation_digest TEXT NOT NULL DEFAULT '' CHECK (
		length(ci_attestation_digest) IN (0, 64)
	),
	ci_result_key TEXT NOT NULL DEFAULT '' CHECK (length(ci_result_key) IN (0, 64)),
	ci_effective_plan_digest TEXT NOT NULL DEFAULT '' CHECK (
		length(ci_effective_plan_digest) IN (0, 64)
	),
	ci_execution_digest TEXT NOT NULL DEFAULT '' CHECK (
		length(ci_execution_digest) IN (0, 64)
	),
	receipt_hash TEXT NOT NULL DEFAULT '' CHECK (length(receipt_hash) IN (0, 64)),
	park_operation_id TEXT NOT NULL DEFAULT '' CHECK (
		length(CAST(park_operation_id AS BLOB)) <= 256
	),
	ledger_entry_id TEXT NOT NULL DEFAULT '' CHECK (
		length(CAST(ledger_entry_id AS BLOB)) <= 256
	),
	fence_hash TEXT NOT NULL DEFAULT '' CHECK (length(fence_hash) IN (0, 64)),
	failed_claim_token_digest TEXT NOT NULL DEFAULT '' CHECK (
		length(failed_claim_token_digest) IN (0, 64)
	),
	created_at INTEGER NOT NULL,
	model_started_at INTEGER,
	model_completed_at INTEGER,
	validated_at INTEGER,
	completed_at INTEGER,
	failed_at INTEGER,
	recovery_required_at INTEGER,
	updated_at INTEGER NOT NULL,
	CHECK (
		(phase IN ('bootstrap', 'editing', 'edited', 'validated') AND
		 claim_owner <> '' AND claim_token <> '' AND claim_until IS NOT NULL AND
		 claim_epoch >= 1) OR
		(phase IN ('completed', 'failed', 'recovery_required') AND
		 claim_owner = '' AND claim_token = '' AND claim_until IS NULL)
	),
	CHECK (
		(head_repository = '' AND head_ref = '' AND head_sha = '' AND clone_url = '' AND
		 review_digest = '' AND workspace_id = '' AND source_tree = '') OR
		(head_repository <> '' AND head_ref <> '' AND head_sha <> '' AND clone_url <> '' AND
		 review_digest <> '' AND workspace_id <> '' AND source_tree <> '')
	),
	CHECK (controller_id = '' OR workspace_id <> ''),
	CHECK (
		(phase IN ('bootstrap', 'failed') AND model_controller_revision = 0 AND
		 model_line_id = '' AND model_line_version = 0 AND model_mutation_epoch = 0 AND
		 model_lease_epoch = 0 AND model_lease_token_digest = '' AND
		 model_reservation_digest = '' AND context_digest = '' AND prompt_digest = '' AND
		 model_result_digest = '' AND summary = '' AND iterations = 0 AND
		 model_started_at IS NULL AND model_completed_at IS NULL) OR
		(phase IN ('editing', 'recovery_required') AND model_controller_revision >= 1 AND
		 model_line_id <> '' AND model_mutation_epoch = model_line_version + 1 AND
		 model_lease_epoch >= 1 AND length(model_lease_token_digest) = 64 AND
		 length(model_reservation_digest) = 64 AND length(context_digest) = 64 AND
		 length(prompt_digest) = 64 AND
		 model_result_digest = '' AND summary = '' AND iterations = 0 AND
		 model_started_at IS NOT NULL AND model_completed_at IS NULL) OR
		(phase IN ('edited', 'validated', 'completed', 'recovery_required') AND
		 model_controller_revision >= 1 AND model_line_id <> '' AND
		 model_mutation_epoch = model_line_version + 1 AND model_lease_epoch >= 1 AND
		 length(model_lease_token_digest) = 64 AND length(model_reservation_digest) = 64 AND
		 length(context_digest) = 64 AND length(prompt_digest) = 64 AND
		 length(model_result_digest) = 64 AND summary <> '' AND iterations >= 1 AND
		 model_started_at IS NOT NULL AND model_completed_at IS NOT NULL)
	),
	CHECK (
		(phase NOT IN ('validated', 'completed') AND receipt_hash = '' AND
		 validation_controller_revision = 0 AND validation_line_id = '' AND
		 validation_line_version = 0 AND validation_mutation_epoch = 0 AND
		 validation_lease_epoch = 0 AND validation_lease_token_digest = '' AND
		 validation_reservation_digest = '' AND parent_commit = '' AND parent_tree = '' AND
		 candidate_tree = '' AND candidate_digest = '' AND changed_files = 0 AND
		 no_changes IS NULL AND ci_status = '' AND ci_attestation_id = '' AND
		 ci_attestation_digest = '' AND ci_result_key = '' AND
		 ci_effective_plan_digest = '' AND ci_execution_digest = '' AND validated_at IS NULL) OR
		(phase IN ('validated', 'completed', 'recovery_required') AND
		 length(receipt_hash) = 64 AND
		 validation_controller_revision >= 1 AND validation_line_id <> '' AND
		 validation_mutation_epoch = validation_line_version + 1 AND
		 validation_lease_epoch >= 1 AND length(validation_lease_token_digest) = 64 AND
		 length(validation_reservation_digest) = 64 AND parent_commit <> '' AND
		 parent_tree <> '' AND candidate_tree <> '' AND length(candidate_digest) = 64 AND
		 no_changes IS NOT NULL AND ci_status <> '' AND ci_attestation_id <> '' AND
		 length(ci_attestation_digest) = 64 AND length(ci_result_key) = 64 AND
		 length(ci_effective_plan_digest) = 64 AND length(ci_execution_digest) = 64 AND
		 validated_at IS NOT NULL AND
		 ((no_changes = 1 AND changed_files = 0 AND candidate_tree = parent_tree) OR
		  (no_changes = 0 AND changed_files >= 1 AND candidate_tree <> parent_tree)))
	),
	CHECK (
		(phase <> 'completed' AND park_operation_id = '' AND ledger_entry_id = '' AND
		 fence_hash = '' AND completed_at IS NULL) OR
		(phase = 'completed' AND park_operation_id <> '' AND ledger_entry_id <> '' AND
		 length(fence_hash) = 64 AND completed_at IS NOT NULL)
	),
	CHECK (
		(phase <> 'failed' AND failed_claim_token_digest = '' AND failed_at IS NULL) OR
		(phase = 'failed' AND length(failed_claim_token_digest) = 64 AND failed_at IS NOT NULL)
	),
	CHECK ((phase = 'recovery_required') = (recovery_required_at IS NOT NULL)),
	CHECK (updated_at >= created_at),
	CHECK (model_started_at IS NULL OR model_started_at >= created_at),
	CHECK (model_completed_at IS NULL OR model_completed_at >= model_started_at),
	CHECK (validated_at IS NULL OR validated_at >= model_completed_at),
	CHECK (completed_at IS NULL OR completed_at >= validated_at),
	CHECK (failed_at IS NULL OR failed_at >= created_at),
	CHECK (recovery_required_at IS NULL OR recovery_required_at >= created_at),
	CHECK (claim_until IS NULL OR claim_until > updated_at)
);`
	schemaV14PRDevelopmentRepairOrchestrationClaimableIndex = `CREATE INDEX IF NOT EXISTS pr_development_repair_orchestration_claimable
	ON pr_development_repair_orchestrations(phase, claim_until, created_at, attempt_id);`
	schemaV14PRDevelopmentRepairOrchestrationReceiptIndex = `CREATE UNIQUE INDEX IF NOT EXISTS pr_development_repair_orchestration_receipt
	ON pr_development_repair_orchestrations(receipt_hash) WHERE receipt_hash <> '';`
	schemaV14PRDevelopmentRepairOrchestrationParkIndex = `CREATE UNIQUE INDEX IF NOT EXISTS pr_development_repair_orchestration_park
	ON pr_development_repair_orchestrations(park_operation_id) WHERE park_operation_id <> '';`
	schemaV14PRDevelopmentRepairOrchestrationLedgerIndex = `CREATE UNIQUE INDEX IF NOT EXISTS pr_development_repair_orchestration_ledger
	ON pr_development_repair_orchestrations(ledger_entry_id) WHERE ledger_entry_id <> '';`
	schemaV14 = schemaV14PRDevelopmentRepairOrchestrationCohortsTable + "\n" +
		schemaV14PRDevelopmentRepairOrchestrationsTable + "\n" +
		schemaV14PRDevelopmentRepairOrchestrationClaimableIndex + "\n" +
		schemaV14PRDevelopmentRepairOrchestrationReceiptIndex + "\n" +
		schemaV14PRDevelopmentRepairOrchestrationParkIndex + "\n" +
		schemaV14PRDevelopmentRepairOrchestrationLedgerIndex
)

func validateSchemaV14ForVersion(
	ctx context.Context,
	conn *sql.Conn,
	publicationV18 bool,
) error {
	binary := func(name string) schemaIndexColumn {
		return schemaIndexColumn{name: name, collation: "BINARY"}
	}
	if err := validateSchemaTable(ctx, conn, schemaTableSpec{
		name:      "pr_development_repair_orchestration_cohorts",
		createSQL: schemaV14PRDevelopmentRepairOrchestrationCohortsTable,
		uniqueIndexes: []schemaUniqueIndexSpec{
			{origin: "pk", columns: []schemaIndexColumn{binary("session_id")}},
			{origin: "u", columns: []schemaIndexColumn{binary("thread_id")}},
		},
	}); err != nil {
		return err
	}
	orchestrationIndexes := []schemaUniqueIndexSpec{
		{origin: "pk", columns: []schemaIndexColumn{binary("attempt_id")}},
		{
			name: "pr_development_repair_orchestration_receipt", origin: "c", partial: true,
			columns: []schemaIndexColumn{binary("receipt_hash")},
		},
		{
			name: "pr_development_repair_orchestration_park", origin: "c", partial: true,
			columns: []schemaIndexColumn{binary("park_operation_id")},
		},
		{
			name: "pr_development_repair_orchestration_ledger", origin: "c", partial: true,
			columns: []schemaIndexColumn{binary("ledger_entry_id")},
		},
	}
	if publicationV18 {
		orchestrationIndexes = append(orchestrationIndexes, schemaUniqueIndexSpec{
			name:   "pr_development_repair_orchestration_publication",
			origin: "c",
			columns: []schemaIndexColumn{
				binary("attempt_id"), binary("receipt_hash"),
			},
		})
	}
	if err := validateSchemaTable(ctx, conn, schemaTableSpec{
		name:          "pr_development_repair_orchestrations",
		createSQL:     schemaV14PRDevelopmentRepairOrchestrationsTable,
		uniqueIndexes: orchestrationIndexes,
	}); err != nil {
		return err
	}
	for _, index := range []schemaIndexSpec{
		{name: "pr_development_repair_orchestration_claimable", createSQL: schemaV14PRDevelopmentRepairOrchestrationClaimableIndex},
		{name: "pr_development_repair_orchestration_receipt", createSQL: schemaV14PRDevelopmentRepairOrchestrationReceiptIndex},
		{name: "pr_development_repair_orchestration_park", createSQL: schemaV14PRDevelopmentRepairOrchestrationParkIndex},
		{name: "pr_development_repair_orchestration_ledger", createSQL: schemaV14PRDevelopmentRepairOrchestrationLedgerIndex},
	} {
		if err := validateSchemaIndex(ctx, conn, index); err != nil {
			return err
		}
	}
	return validatePRDevelopmentRepairOrchestrationCohorts(ctx, conn)
}

func validatePRDevelopmentRepairOrchestrationCohorts(
	ctx context.Context,
	conn *sql.Conn,
) error {
	var invalid int
	if err := conn.QueryRowContext(ctx, `
		SELECT COUNT(*)
		FROM pr_development_repair_orchestration_cohorts AS cohort
		JOIN pr_development_repair_sessions AS session ON session.id = cohort.session_id
		LEFT JOIN pr_development_thread_cases AS membership
		  ON membership.case_id = session.case_id AND membership.thread_id = cohort.thread_id
		JOIN pr_development_threads AS thread ON thread.id = cohort.thread_id
		WHERE membership.case_id IS NULL OR thread.identity_kind <> 'provider'`).Scan(&invalid); err != nil {
		return err
	}
	if invalid != 0 {
		return fmt.Errorf(
			"%w: %d orchestration cohort bindings are not provider-thread session owners",
			ErrSchemaInvalid,
			invalid,
		)
	}
	return nil
}
