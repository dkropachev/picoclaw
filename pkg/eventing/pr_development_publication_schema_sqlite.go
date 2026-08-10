//go:build !mipsle && !netbsd && !(freebsd && arm)

package eventing

import (
	"context"
	"database/sql"
)

const (
	schemaV18PRDevelopmentReviewFencePublicationIndex = `CREATE UNIQUE INDEX IF NOT EXISTS pr_development_attempt_review_fences_publication
	ON pr_development_attempt_review_fences(attempt_id, controller_id, ordinal, fence_hash);`
	schemaV18PRDevelopmentOrchestrationPublicationIndex = `CREATE UNIQUE INDEX IF NOT EXISTS pr_development_repair_orchestration_publication
	ON pr_development_repair_orchestrations(attempt_id, receipt_hash);`
	schemaV18PRDevelopmentPublicationsTable = `CREATE TABLE IF NOT EXISTS pr_development_publications (
	id TEXT PRIMARY KEY CHECK (
		substr(id, 1, 6) = 'pdpub_' AND
		length(CAST(id AS BLOB)) >= 7 AND length(CAST(id AS BLOB)) <= 256
	),
	case_id TEXT NOT NULL REFERENCES pr_development_cases(id) ON DELETE RESTRICT,
	thread_id TEXT NOT NULL REFERENCES pr_development_threads(id) ON DELETE RESTRICT,
	controller_id TEXT NOT NULL,
	controller_revision INTEGER NOT NULL CHECK (
		controller_revision >= 1 AND controller_revision <= 65536
	),
	owner_session_id TEXT NOT NULL REFERENCES pr_development_repair_sessions(id) ON DELETE RESTRICT,
	attempt_id TEXT NOT NULL REFERENCES pr_development_repair_attempts(id) ON DELETE RESTRICT,
	fence_ordinal INTEGER NOT NULL CHECK (fence_ordinal >= 0 AND fence_ordinal < 8192),
	fence_hash TEXT NOT NULL CHECK (length(fence_hash) = 64),
	attempt_ledger_entry_id TEXT NOT NULL,
	attempt_ledger_entry_kind TEXT NOT NULL DEFAULT 'attempt' CHECK (
		attempt_ledger_entry_kind = 'attempt'
	),
	attempt_ledger_entry_hash TEXT NOT NULL CHECK (length(attempt_ledger_entry_hash) = 64),
	review_ledger_entry_id TEXT NOT NULL,
	review_ledger_entry_kind TEXT NOT NULL DEFAULT 'review' CHECK (
		review_ledger_entry_kind = 'review'
	),
	review_ledger_entry_hash TEXT NOT NULL CHECK (length(review_ledger_entry_hash) = 64),
	review_outcome TEXT NOT NULL DEFAULT 'passed' CHECK (review_outcome = 'passed'),
	orchestration_phase TEXT NOT NULL DEFAULT 'completed' CHECK (
		orchestration_phase = 'completed'
	),
	orchestration_receipt_hash TEXT NOT NULL CHECK (length(orchestration_receipt_hash) = 64),
	ci_status TEXT NOT NULL DEFAULT 'passed' CHECK (ci_status = 'passed'),
	ci_plan_digest TEXT NOT NULL CHECK (length(ci_plan_digest) = 64),
	ci_result_digest TEXT NOT NULL CHECK (length(ci_result_digest) = 64),
	workspace_id TEXT NOT NULL CHECK (
		length(CAST(workspace_id AS BLOB)) >= 1 AND
		length(CAST(workspace_id AS BLOB)) <= 256
	),
	line_id TEXT NOT NULL CHECK (
		length(CAST(line_id AS BLOB)) >= 1 AND length(CAST(line_id AS BLOB)) <= 256
	),
	source_clone_url TEXT NOT NULL CHECK (
		length(CAST(source_clone_url AS BLOB)) >= 1 AND
		length(CAST(source_clone_url AS BLOB)) <= 4096
	),
	source_ref TEXT NOT NULL CHECK (
		length(CAST(source_ref AS BLOB)) >= 1 AND length(CAST(source_ref AS BLOB)) <= 1024
	),
	source_commit TEXT NOT NULL CHECK (length(source_commit) IN (40, 64)),
	source_tree TEXT NOT NULL CHECK (length(source_tree) IN (40, 64)),
	line_version INTEGER NOT NULL CHECK (line_version >= 1 AND line_version <= 8192),
	mutation_epoch INTEGER NOT NULL CHECK (mutation_epoch >= 1 AND mutation_epoch <= 8192),
	park_intent_id TEXT NOT NULL CHECK (
		length(CAST(park_intent_id AS BLOB)) >= 1 AND
		length(CAST(park_intent_id AS BLOB)) <= 256
	),
	base_commit TEXT NOT NULL CHECK (length(base_commit) IN (40, 64)),
	tip_commit TEXT NOT NULL CHECK (length(tip_commit) IN (40, 64)),
	tree TEXT NOT NULL CHECK (length(tree) IN (40, 64)),
	no_changes INTEGER NOT NULL CHECK (no_changes IN (0, 1)),
	status TEXT NOT NULL CHECK (status IN (
		'pending', 'claimed', 'gate_waiting', 'push_ready', 'push_started',
		'published', 'conflict', 'superseded', 'failed',
		'recovery_required', 'outcome_unknown'
	)),
	claim_from TEXT NOT NULL DEFAULT '' CHECK (
		claim_from IN ('', 'pending', 'gate_waiting', 'push_ready')
	),
	claim_owner TEXT NOT NULL DEFAULT '' CHECK (length(CAST(claim_owner AS BLOB)) <= 256),
	claim_token TEXT NOT NULL DEFAULT '' CHECK (length(CAST(claim_token AS BLOB)) <= 128),
	claim_until INTEGER,
	claim_epoch INTEGER NOT NULL DEFAULT 0 CHECK (claim_epoch >= 0),
	claims INTEGER NOT NULL DEFAULT 0 CHECK (claims >= 0 AND claims = claim_epoch),
	claimed_at INTEGER,
	attempts INTEGER NOT NULL DEFAULT 0 CHECK (attempts IN (0, 1)),
	available_at INTEGER NOT NULL,
	policy_revision TEXT NOT NULL DEFAULT '' CHECK (length(policy_revision) IN (0, 71)),
	pinned_policy_json BLOB NOT NULL DEFAULT X'' CHECK (
		typeof(pinned_policy_json) = 'blob' AND
		length(pinned_policy_json) <= 4194304
	),
	pinned_policy_hash TEXT NOT NULL DEFAULT '' CHECK (length(pinned_policy_hash) IN (0, 64)),
	subject_revision TEXT NOT NULL DEFAULT '' CHECK (length(subject_revision) IN (0, 71)),
	pinned_subject_json BLOB NOT NULL DEFAULT X'' CHECK (
		typeof(pinned_subject_json) = 'blob' AND
		length(pinned_subject_json) <= 2097152
	),
	pinned_subject_hash TEXT NOT NULL DEFAULT '' CHECK (length(pinned_subject_hash) IN (0, 64)),
	provider_observation_json BLOB NOT NULL DEFAULT X'' CHECK (
		typeof(provider_observation_json) = 'blob' AND
		length(provider_observation_json) <= 32768
	),
	provider_observation_hash TEXT NOT NULL DEFAULT '' CHECK (
		length(provider_observation_hash) IN (0, 64)
	),
	provider_pinned_at INTEGER,
	provider_observed_at INTEGER,
	reconciliation_observation_json BLOB NOT NULL DEFAULT X'' CHECK (
		typeof(reconciliation_observation_json) = 'blob' AND
		length(reconciliation_observation_json) <= 32768
	),
	reconciliation_observation_hash TEXT NOT NULL DEFAULT '' CHECK (
		length(reconciliation_observation_hash) IN (0, 64)
	),
	reconciliation_observed_at INTEGER,
	decision_run_id TEXT NOT NULL DEFAULT '' CHECK (
		length(CAST(decision_run_id AS BLOB)) <= 256
	),
	expected_remote_tip TEXT NOT NULL DEFAULT '' CHECK (
		length(expected_remote_tip) IN (0, 40, 64)
	),
	push_request_json BLOB NOT NULL DEFAULT X'' CHECK (
		typeof(push_request_json) = 'blob' AND length(push_request_json) <= 32768
	),
	push_request_hash TEXT NOT NULL DEFAULT '' CHECK (length(push_request_hash) IN (0, 64)),
	push_result_json BLOB NOT NULL DEFAULT X'' CHECK (
		typeof(push_result_json) = 'blob' AND length(push_result_json) <= 32768
	),
	push_result_hash TEXT NOT NULL DEFAULT '' CHECK (length(push_result_hash) IN (0, 64)),
	push_disposition TEXT NOT NULL DEFAULT '' CHECK (
		push_disposition IN ('', 'applied', 'already_current', 'reconciled')
	),
	workspace_clean INTEGER NOT NULL DEFAULT 0 CHECK (workspace_clean IN (0, 1)),
	local_drift INTEGER NOT NULL DEFAULT 0 CHECK (local_drift IN (0, 1)),
	last_error_code TEXT NOT NULL DEFAULT '' CHECK (
		length(CAST(last_error_code AS BLOB)) <= 128
	),
	last_error_detail TEXT NOT NULL DEFAULT '' CHECK (
		length(CAST(last_error_detail AS BLOB)) <= 16384
	),
	created_at INTEGER NOT NULL,
	updated_at INTEGER NOT NULL,
	effect_started_at INTEGER,
	completed_at INTEGER,
	FOREIGN KEY(controller_id, thread_id, line_id)
		REFERENCES pr_development_thread_controllers(id, thread_id, line_id)
		ON DELETE RESTRICT,
	FOREIGN KEY(attempt_id, controller_id, fence_ordinal, fence_hash)
		REFERENCES pr_development_attempt_review_fences(
			attempt_id, controller_id, ordinal, fence_hash
		) ON DELETE RESTRICT,
	FOREIGN KEY(attempt_id, orchestration_receipt_hash)
		REFERENCES pr_development_repair_orchestrations(attempt_id, receipt_hash)
		ON DELETE RESTRICT,
	FOREIGN KEY(attempt_ledger_entry_id, attempt_ledger_entry_kind, attempt_ledger_entry_hash)
		REFERENCES pr_development_ledger_entries(id, kind, entry_hash)
		ON DELETE RESTRICT,
	FOREIGN KEY(review_ledger_entry_id, review_ledger_entry_kind, review_ledger_entry_hash)
		REFERENCES pr_development_ledger_entries(id, kind, entry_hash)
		ON DELETE RESTRICT,
	CHECK (attempt_ledger_entry_id <> review_ledger_entry_id),
	CHECK (line_version = mutation_epoch AND line_version = fence_ordinal + 1),
	CHECK (
		length(source_commit) = length(source_tree) AND
		length(source_commit) = length(base_commit) AND
		length(source_commit) = length(tip_commit) AND
		length(source_commit) = length(tree)
	),
	CHECK ((no_changes = 1 AND base_commit = tip_commit) OR
	       (no_changes = 0 AND base_commit <> tip_commit)),
	CHECK (
		(policy_revision = '' AND length(pinned_policy_json) = 0 AND
		 pinned_policy_hash = '' AND subject_revision = '' AND
		 length(pinned_subject_json) = 0 AND pinned_subject_hash = '') OR
		(length(policy_revision) = 71 AND length(pinned_policy_json) >= 2 AND
		 length(pinned_policy_hash) = 64 AND subject_revision = '' AND
		 length(pinned_subject_json) = 0 AND pinned_subject_hash = '') OR
		(length(policy_revision) = 71 AND length(pinned_policy_json) >= 2 AND
		 length(pinned_policy_hash) = 64 AND length(subject_revision) = 71 AND
		 length(pinned_subject_json) >= 2 AND length(pinned_subject_hash) = 64)
	),
	CHECK (
		(length(provider_observation_json) = 0 AND provider_observation_hash = '' AND
		 provider_pinned_at IS NULL AND provider_observed_at IS NULL) OR
		(length(provider_observation_json) >= 2 AND
		 length(provider_observation_hash) = 64 AND provider_pinned_at IS NOT NULL AND
		 provider_observed_at IS NOT NULL)
	),
	CHECK (
		(length(reconciliation_observation_json) = 0 AND
		 reconciliation_observation_hash = '' AND reconciliation_observed_at IS NULL) OR
		(length(reconciliation_observation_json) >= 2 AND
		 length(reconciliation_observation_hash) = 64 AND
		 reconciliation_observed_at IS NOT NULL AND status = 'published' AND
		 push_disposition = 'reconciled')
	),
	CHECK (
		(expected_remote_tip = '' AND length(push_request_json) = 0 AND
		 push_request_hash = '') OR
		(length(expected_remote_tip) = length(tip_commit) AND
		 length(push_request_json) >= 2 AND length(push_request_hash) = 64)
	),
	CHECK (
		(status <> 'published' AND length(push_result_json) = 0 AND
		 push_result_hash = '' AND push_disposition = '' AND
		 workspace_clean = 0 AND local_drift = 0) OR
		(status = 'published' AND length(push_result_json) >= 2 AND
		 length(push_result_hash) = 64 AND push_disposition <> '' AND
		 NOT (workspace_clean = 1 AND local_drift = 1))
	),
	CHECK (
		status NOT IN ('gate_waiting', 'push_ready', 'push_started', 'published',
		               'outcome_unknown') OR
		(length(subject_revision) = 71 AND length(provider_observation_json) >= 2)
	),
	CHECK (status <> 'claimed' OR claim_from NOT IN ('gate_waiting', 'push_ready') OR
	       (length(subject_revision) = 71 AND length(provider_observation_json) >= 2)),
	CHECK (status <> 'gate_waiting' OR decision_run_id <> ''),
	CHECK (decision_run_id = '' OR
	       (length(subject_revision) = 71 AND length(provider_observation_json) >= 2)),
	CHECK (
		status NOT IN ('push_started', 'published', 'outcome_unknown') OR
		(length(provider_observation_json) >= 2 AND length(push_request_json) >= 2)
	),
	CHECK (length(push_request_json) = 0 OR
	       (length(subject_revision) = 71 AND length(provider_observation_json) >= 2)),
	CHECK (status <> 'push_started' OR claim_from = 'push_ready'),
	CHECK (
		(status IN ('pending', 'claimed', 'gate_waiting', 'push_ready',
		            'push_started', 'published') AND last_error_code = '' AND
		 last_error_detail = '') OR
		(status = 'conflict' AND last_error_code IN (
			'provider_changed', 'local_evidence_changed', 'push_conflict'
		)) OR
		(status = 'superseded' AND last_error_code = 'superseded') OR
		(status = 'failed' AND (
			(effect_started_at IS NULL AND last_error_code IN (
				'provider_changed', 'local_evidence_changed', 'gate_failed',
				'runtime_unavailable', 'push_conflict', 'push_failed', 'internal'
			)) OR
			(effect_started_at IS NOT NULL AND last_error_code IN (
				'runtime_unavailable', 'push_failed', 'internal'
			))
		)) OR
		(status = 'recovery_required' AND last_error_code = 'recovery_required') OR
		(status = 'outcome_unknown' AND last_error_code = 'outcome_unknown')
	),
	CHECK (
		(status IN ('claimed', 'push_started') AND claim_from <> '' AND
		 claim_owner <> '' AND claim_token <> '' AND claim_until IS NOT NULL AND
		 claim_epoch >= 1 AND claimed_at IS NOT NULL) OR
		(status NOT IN ('claimed', 'push_started') AND claim_from = '' AND
		 claim_owner = '' AND claim_token = '' AND claim_until IS NULL)
	),
	CHECK ((claim_epoch = 0 AND claimed_at IS NULL) OR
	       (claim_epoch >= 1 AND claimed_at IS NOT NULL)),
	CHECK (
		(status IN ('pending', 'claimed', 'gate_waiting', 'push_ready', 'push_started') AND
		 completed_at IS NULL) OR
		(status IN ('published', 'conflict', 'superseded', 'failed',
		            'recovery_required', 'outcome_unknown') AND completed_at IS NOT NULL)
	),
	CHECK ((length(push_request_json) = 0 AND effect_started_at IS NULL AND attempts = 0) OR
	       (length(push_request_json) >= 2 AND effect_started_at IS NOT NULL AND attempts = 1)),
	CHECK (status NOT IN ('pending', 'claimed', 'gate_waiting', 'push_ready', 'superseded') OR
	       effect_started_at IS NULL),
	CHECK (status NOT IN ('push_started', 'published', 'outcome_unknown') OR
	       effect_started_at IS NOT NULL),
	CHECK (updated_at >= created_at),
	CHECK (available_at >= created_at),
	CHECK (claimed_at IS NULL OR claimed_at >= created_at),
	CHECK (claimed_at IS NULL OR claimed_at <= updated_at),
	CHECK (claim_until IS NULL OR claim_until > updated_at),
	CHECK (provider_pinned_at IS NULL OR provider_pinned_at >= created_at),
	CHECK (provider_pinned_at IS NULL OR provider_pinned_at <= updated_at),
	CHECK (provider_observed_at IS NULL OR provider_observed_at >= provider_pinned_at),
	CHECK (provider_observed_at IS NULL OR provider_observed_at <= updated_at),
	CHECK (length(push_request_json) >= 2 OR provider_observed_at = provider_pinned_at),
	CHECK (effect_started_at IS NULL OR provider_observed_at <= effect_started_at),
	CHECK (effect_started_at IS NULL OR effect_started_at <= updated_at),
	CHECK (reconciliation_observed_at IS NULL OR
	       reconciliation_observed_at >= effect_started_at),
	CHECK (reconciliation_observed_at IS NULL OR
	       (completed_at IS NOT NULL AND reconciliation_observed_at > completed_at)),
	CHECK (reconciliation_observed_at IS NULL OR
	       reconciliation_observed_at <= updated_at),
	CHECK (effect_started_at IS NULL OR effect_started_at >= created_at),
	CHECK (completed_at IS NULL OR completed_at >= created_at),
	CHECK (completed_at IS NULL OR completed_at <= updated_at),
	CHECK (completed_at IS NULL OR effect_started_at IS NULL OR completed_at >= effect_started_at)
);`
	schemaV18PRDevelopmentPublicationOccurrenceIndex = `CREATE UNIQUE INDEX IF NOT EXISTS pr_development_publications_occurrence
	ON pr_development_publications(review_ledger_entry_id);`
	schemaV18PRDevelopmentPublicationDecisionRunIndex = `CREATE UNIQUE INDEX IF NOT EXISTS pr_development_publications_decision_run
	ON pr_development_publications(decision_run_id) WHERE decision_run_id <> '';`
	schemaV18PRDevelopmentPublicationPushStartedIndex = `CREATE UNIQUE INDEX IF NOT EXISTS pr_development_publications_push_started
	ON pr_development_publications(controller_id) WHERE status = 'push_started';`
	schemaV18PRDevelopmentPublicationClaimableIndex = `CREATE INDEX IF NOT EXISTS pr_development_publications_claimable
	ON pr_development_publications(status, available_at, claim_until, created_at, id);`
	schemaV18 = schemaV18PRDevelopmentReviewFencePublicationIndex + "\n" +
		schemaV18PRDevelopmentOrchestrationPublicationIndex + "\n" +
		schemaV18PRDevelopmentPublicationsTable + "\n" +
		schemaV18PRDevelopmentPublicationOccurrenceIndex + "\n" +
		schemaV18PRDevelopmentPublicationDecisionRunIndex + "\n" +
		schemaV18PRDevelopmentPublicationPushStartedIndex + "\n" +
		schemaV18PRDevelopmentPublicationClaimableIndex
)

func validateSchemaV18(ctx context.Context, conn *sql.Conn) error {
	binary := func(name string) schemaIndexColumn {
		return schemaIndexColumn{name: name, collation: "BINARY"}
	}
	if err := validateSchemaTable(ctx, conn, schemaTableSpec{
		name:      "pr_development_publications",
		createSQL: schemaV18PRDevelopmentPublicationsTable,
		uniqueIndexes: []schemaUniqueIndexSpec{
			{origin: "pk", columns: []schemaIndexColumn{binary("id")}},
			{
				name: "pr_development_publications_occurrence", origin: "c",
				columns: []schemaIndexColumn{binary("review_ledger_entry_id")},
			},
			{
				name: "pr_development_publications_decision_run", origin: "c", partial: true,
				columns: []schemaIndexColumn{binary("decision_run_id")},
			},
			{
				name: "pr_development_publications_push_started", origin: "c", partial: true,
				columns: []schemaIndexColumn{binary("controller_id")},
			},
		},
	}); err != nil {
		return err
	}
	for _, index := range []schemaIndexSpec{
		{name: "pr_development_attempt_review_fences_publication", createSQL: schemaV18PRDevelopmentReviewFencePublicationIndex},
		{name: "pr_development_repair_orchestration_publication", createSQL: schemaV18PRDevelopmentOrchestrationPublicationIndex},
		{name: "pr_development_publications_occurrence", createSQL: schemaV18PRDevelopmentPublicationOccurrenceIndex},
		{name: "pr_development_publications_decision_run", createSQL: schemaV18PRDevelopmentPublicationDecisionRunIndex},
		{name: "pr_development_publications_push_started", createSQL: schemaV18PRDevelopmentPublicationPushStartedIndex},
		{name: "pr_development_publications_claimable", createSQL: schemaV18PRDevelopmentPublicationClaimableIndex},
	} {
		if err := validateSchemaIndex(ctx, conn, index); err != nil {
			return err
		}
	}
	return nil
}
