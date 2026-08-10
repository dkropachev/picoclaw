//go:build !mipsle && !netbsd && !(freebsd && arm)

package eventing

import (
	"context"
	"database/sql"
	"fmt"
)

const (
	schemaV17PRDevelopmentControllersTable = `CREATE TABLE IF NOT EXISTS pr_development_thread_controllers (
	id TEXT PRIMARY KEY,
	thread_id TEXT NOT NULL UNIQUE REFERENCES pr_development_threads(id) ON DELETE RESTRICT,
	owner_session_id TEXT NOT NULL UNIQUE REFERENCES pr_development_repair_sessions(id) ON DELETE RESTRICT,
	agent_id TEXT NOT NULL CHECK (
		length(CAST(agent_id AS BLOB)) >= 1 AND
		length(CAST(agent_id AS BLOB)) <= 64
	),
	revision INTEGER NOT NULL CHECK (revision >= 1 AND revision <= 65536),
	phase TEXT NOT NULL CHECK (phase IN (
		'idle', 'mutation', 'review_pending', 'review', 'ready', 'recovery_required',
		'suspension_pending', 'suspended'
	)),
	line_id TEXT NOT NULL UNIQUE CHECK (
		length(CAST(line_id AS BLOB)) >= 1 AND
		length(CAST(line_id AS BLOB)) <= 256
	),
	workspace_id TEXT NOT NULL DEFAULT '' CHECK (length(CAST(workspace_id AS BLOB)) <= 256),
	source_clone_url TEXT NOT NULL DEFAULT '' CHECK (length(CAST(source_clone_url AS BLOB)) <= 4096),
	source_ref TEXT NOT NULL DEFAULT '' CHECK (length(CAST(source_ref AS BLOB)) <= 1024),
	source_commit TEXT NOT NULL DEFAULT '' CHECK (length(CAST(source_commit AS BLOB)) <= 64),
	source_tree TEXT NOT NULL DEFAULT '' CHECK (length(CAST(source_tree AS BLOB)) <= 64),
	line_version INTEGER NOT NULL DEFAULT 0 CHECK (line_version >= 0 AND line_version <= 8192),
	mutation_epoch INTEGER NOT NULL DEFAULT 0 CHECK (mutation_epoch >= 0 AND mutation_epoch <= 8193),
	tip_commit TEXT NOT NULL DEFAULT '' CHECK (length(CAST(tip_commit AS BLOB)) <= 64),
	tree TEXT NOT NULL DEFAULT '' CHECK (length(CAST(tree AS BLOB)) <= 64),
	current_attempt_id TEXT REFERENCES pr_development_repair_attempts(id) ON DELETE RESTRICT,
	lease_kind TEXT NOT NULL DEFAULT '' CHECK (lease_kind IN ('', 'mutation', 'review')),
	lease_owner TEXT NOT NULL DEFAULT '' CHECK (length(CAST(lease_owner AS BLOB)) <= 256),
	lease_token TEXT NOT NULL DEFAULT '' CHECK (length(CAST(lease_token AS BLOB)) <= 128),
	lease_until INTEGER,
	lease_epoch INTEGER NOT NULL DEFAULT 0 CHECK (lease_epoch >= 0),
	claims INTEGER NOT NULL DEFAULT 0 CHECK (claims >= 0),
	mutation_reservation_key TEXT NOT NULL DEFAULT '' CHECK (
		length(CAST(mutation_reservation_key AS BLOB)) <= 256
	),
	fence_count INTEGER NOT NULL DEFAULT 0 CHECK (fence_count >= 0 AND fence_count <= 8192),
	fences_digest TEXT NOT NULL CHECK (length(fences_digest) = 64),
	created_at INTEGER NOT NULL,
	updated_at INTEGER NOT NULL,
	UNIQUE(id, thread_id, line_id),
	CHECK (
		(workspace_id = '' AND source_clone_url = '' AND source_ref = '' AND
		 source_commit = '' AND source_tree = '' AND line_version = 0 AND
		 mutation_epoch = 0 AND tip_commit = '' AND tree = '' AND fence_count = 0) OR
		(workspace_id <> '' AND source_clone_url <> '' AND source_ref <> '' AND
		 source_commit <> '' AND source_tree <> '' AND tip_commit <> '' AND tree <> '' AND
		 length(source_commit) IN (40, 64) AND length(source_tree) = length(source_commit) AND
		 length(tip_commit) = length(source_commit) AND length(tree) = length(source_commit) AND
		 fence_count = line_version AND
		 mutation_epoch >= line_version AND mutation_epoch <= line_version + 1)
	),
	CHECK (
		(phase = 'idle' AND current_attempt_id IS NULL AND lease_kind = '' AND
			lease_owner = '' AND lease_token = '' AND lease_until IS NULL AND
			mutation_reservation_key = '' AND fence_count = 0 AND workspace_id = '') OR
		(phase = 'mutation' AND current_attempt_id IS NOT NULL AND lease_kind = 'mutation' AND
		 lease_owner <> '' AND lease_token <> '' AND lease_until IS NOT NULL AND
		 mutation_reservation_key <> '') OR
		(phase = 'review_pending' AND current_attempt_id IS NOT NULL AND lease_kind = '' AND
		 lease_owner = '' AND lease_token = '' AND lease_until IS NULL AND
		 mutation_reservation_key = '' AND fence_count >= 1 AND mutation_epoch = line_version) OR
		(phase = 'review' AND current_attempt_id IS NOT NULL AND lease_kind = 'review' AND
		 lease_owner <> '' AND lease_token <> '' AND lease_until IS NOT NULL AND
		 mutation_reservation_key = '' AND fence_count >= 1 AND mutation_epoch = line_version) OR
		(phase = 'ready' AND current_attempt_id IS NOT NULL AND lease_kind = '' AND
		 lease_owner = '' AND lease_token = '' AND lease_until IS NULL AND
		 mutation_reservation_key = '' AND fence_count >= 1 AND mutation_epoch = line_version) OR
		(phase = 'recovery_required' AND current_attempt_id IS NOT NULL AND lease_kind = '' AND
		 lease_owner = '' AND lease_token = '' AND lease_until IS NULL AND
		 mutation_reservation_key <> '') OR
		(phase IN ('suspension_pending', 'suspended') AND
		 current_attempt_id IS NOT NULL AND workspace_id <> '' AND lease_kind = '' AND
		 lease_owner = '' AND lease_token = '' AND lease_until IS NULL AND
		 mutation_reservation_key = '')
	),
	CHECK (phase <> 'mutation' OR line_version < 8192),
	CHECK (updated_at >= created_at)
);`

	schemaV17PRDevelopmentControllerSuspensionsTable = `CREATE TABLE IF NOT EXISTS pr_development_controller_suspensions (
	id TEXT PRIMARY KEY CHECK (
		substr(id, 1, 5) = 'pdsi_' AND
		length(CAST(id AS BLOB)) >= 6 AND length(CAST(id AS BLOB)) <= 256
	),
	controller_id TEXT NOT NULL,
	thread_id TEXT NOT NULL,
	owner_session_id TEXT NOT NULL REFERENCES pr_development_repair_sessions(id) ON DELETE RESTRICT,
	attempt_id TEXT NOT NULL REFERENCES pr_development_repair_attempts(id) ON DELETE RESTRICT,
	ordinal INTEGER NOT NULL CHECK (ordinal >= 0 AND ordinal < 8192),
	source_kind TEXT NOT NULL CHECK (source_kind IN (
		'controller_recovery', 'operation_recovery', 'suspended_resume_recovery'
	)),
	source_recovery_id TEXT NOT NULL CHECK (
		length(CAST(source_recovery_id AS BLOB)) >= 1 AND
		length(CAST(source_recovery_id AS BLOB)) <= 256
	),
	source_operation_id TEXT NOT NULL DEFAULT '' CHECK (
		length(CAST(source_operation_id AS BLOB)) <= 256
	),
	source_operation_kind TEXT NOT NULL DEFAULT '' CHECK (
		source_operation_kind IN ('', 'adopt', 'resume', 'commit')
	),
	source_final_revision INTEGER NOT NULL CHECK (
		source_final_revision >= 1 AND source_final_revision <= 65536
	),
	source_final_hash TEXT NOT NULL CHECK (length(source_final_hash) = 64),
	mode TEXT NOT NULL CHECK (mode IN ('candidate', 'commit_recovery')),
	status TEXT NOT NULL CHECK (status IN (
		'suspend_pending', 'suspend_claimed', 'suspended',
		'resume_pending', 'resume_claimed', 'resumed'
	)),
	agent_id TEXT NOT NULL CHECK (
		length(CAST(agent_id AS BLOB)) >= 1 AND length(CAST(agent_id AS BLOB)) <= 64
	),
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
	line_version INTEGER NOT NULL CHECK (line_version >= 0 AND line_version <= 8192),
	mutation_epoch INTEGER NOT NULL CHECK (
		mutation_epoch >= line_version AND mutation_epoch <= line_version + 1 AND
		mutation_epoch <= 8193
	),
	tip_commit TEXT NOT NULL CHECK (length(tip_commit) IN (40, 64)),
	tree TEXT NOT NULL CHECK (length(tree) IN (40, 64)),
	suspension_reservation_key TEXT NOT NULL DEFAULT '' CHECK (
		length(CAST(suspension_reservation_key AS BLOB)) <= 256
	),
	suspension_reservation_digest TEXT NOT NULL CHECK (
		length(suspension_reservation_digest) = 64
	),
	mutation_lease_epoch INTEGER NOT NULL CHECK (mutation_lease_epoch >= 1),
	mutation_lease_token_digest TEXT NOT NULL CHECK (
		length(mutation_lease_token_digest) = 64
	),
	suspend_intent_id TEXT NOT NULL CHECK (
		length(CAST(suspend_intent_id AS BLOB)) >= 1 AND
		length(CAST(suspend_intent_id AS BLOB)) <= 256
	),
	suspend_request_json BLOB NOT NULL CHECK (
		typeof(suspend_request_json) = 'blob' AND
		length(suspend_request_json) >= 2 AND length(suspend_request_json) <= 32768
	),
	suspend_request_hash TEXT NOT NULL CHECK (length(suspend_request_hash) = 64),
	previous_hash TEXT NOT NULL CHECK (length(previous_hash) = 64),
	intent_hash TEXT NOT NULL CHECK (length(intent_hash) = 64),
	suspend_claim_id TEXT NOT NULL DEFAULT '' CHECK (
		length(CAST(suspend_claim_id AS BLOB)) <= 256
	),
	suspend_claim_owner TEXT NOT NULL DEFAULT '' CHECK (
		length(CAST(suspend_claim_owner AS BLOB)) <= 256
	),
	suspend_claim_token TEXT NOT NULL DEFAULT '' CHECK (
		length(CAST(suspend_claim_token AS BLOB)) <= 128
	),
	suspend_claim_until INTEGER,
	suspend_claim_epoch INTEGER NOT NULL DEFAULT 0 CHECK (suspend_claim_epoch >= 0),
	suspend_claims INTEGER NOT NULL DEFAULT 0 CHECK (suspend_claims >= 0),
	suspend_claimed_at INTEGER,
	suspend_claim_token_digest TEXT NOT NULL DEFAULT '' CHECK (
		length(suspend_claim_token_digest) IN (0, 64)
	),
	suspend_result_json BLOB NOT NULL DEFAULT X'' CHECK (
		typeof(suspend_result_json) = 'blob' AND length(suspend_result_json) <= 32768
	),
	suspend_result_hash TEXT NOT NULL DEFAULT '' CHECK (length(suspend_result_hash) IN (0, 64)),
	candidate_tree TEXT NOT NULL DEFAULT '' CHECK (length(candidate_tree) IN (0, 40, 64)),
	candidate_digest TEXT NOT NULL DEFAULT '' CHECK (length(candidate_digest) IN (0, 64)),
	changed_file_count INTEGER NOT NULL DEFAULT 0 CHECK (
		changed_file_count >= 0 AND changed_file_count <= 10000
	),
	suspension_hash TEXT NOT NULL DEFAULT '' CHECK (length(suspension_hash) IN (0, 64)),
	prepared_commit TEXT NOT NULL DEFAULT '' CHECK (length(prepared_commit) IN (0, 40, 64)),
	prepared_tree TEXT NOT NULL DEFAULT '' CHECK (length(prepared_tree) IN (0, 40, 64)),
	prepared_commit_applied INTEGER NOT NULL DEFAULT 0 CHECK (prepared_commit_applied IN (0, 1)),
	final_suspension_revision INTEGER NOT NULL DEFAULT 0 CHECK (
		final_suspension_revision >= 0 AND final_suspension_revision <= 65536
	),
	suspension_final_hash TEXT NOT NULL DEFAULT '' CHECK (
		length(suspension_final_hash) IN (0, 64)
	),
	suspended_at INTEGER,
	resume_attempt_id TEXT REFERENCES pr_development_repair_attempts(id) ON DELETE RESTRICT,
	resume_intent_id TEXT NOT NULL DEFAULT '' CHECK (
		length(CAST(resume_intent_id AS BLOB)) <= 256
	),
	resume_reservation_key TEXT NOT NULL DEFAULT '' CHECK (
		length(CAST(resume_reservation_key AS BLOB)) <= 256
	),
	resume_reservation_digest TEXT NOT NULL DEFAULT '' CHECK (
		length(resume_reservation_digest) IN (0, 64)
	),
	resume_request_json BLOB NOT NULL DEFAULT X'' CHECK (
		typeof(resume_request_json) = 'blob' AND length(resume_request_json) <= 32768
	),
	resume_request_hash TEXT NOT NULL DEFAULT '' CHECK (length(resume_request_hash) IN (0, 64)),
	resume_intent_hash TEXT NOT NULL DEFAULT '' CHECK (length(resume_intent_hash) IN (0, 64)),
	resume_prepared_at INTEGER,
	resume_claim_id TEXT NOT NULL DEFAULT '' CHECK (
		length(CAST(resume_claim_id AS BLOB)) <= 256
	),
	resume_claim_owner TEXT NOT NULL DEFAULT '' CHECK (
		length(CAST(resume_claim_owner AS BLOB)) <= 256
	),
	resume_claim_token TEXT NOT NULL DEFAULT '' CHECK (
		length(CAST(resume_claim_token AS BLOB)) <= 128
	),
	resume_claim_until INTEGER,
	resume_claim_epoch INTEGER NOT NULL DEFAULT 0 CHECK (resume_claim_epoch >= 0),
	resume_claims INTEGER NOT NULL DEFAULT 0 CHECK (resume_claims >= 0),
	resume_claimed_at INTEGER,
	resume_claim_token_digest TEXT NOT NULL DEFAULT '' CHECK (
		length(resume_claim_token_digest) IN (0, 64)
	),
	resume_result_json BLOB NOT NULL DEFAULT X'' CHECK (
		typeof(resume_result_json) = 'blob' AND length(resume_result_json) <= 32768
	),
	resume_result_hash TEXT NOT NULL DEFAULT '' CHECK (length(resume_result_hash) IN (0, 64)),
	rotation_hash TEXT NOT NULL DEFAULT '' CHECK (length(rotation_hash) IN (0, 64)),
	new_mutation_lease_epoch INTEGER NOT NULL DEFAULT 0 CHECK (new_mutation_lease_epoch >= 0),
	new_mutation_lease_token_digest TEXT NOT NULL DEFAULT '' CHECK (
		length(new_mutation_lease_token_digest) IN (0, 64)
	),
	new_mutation_lease_until INTEGER,
	final_resume_revision INTEGER NOT NULL DEFAULT 0 CHECK (
		final_resume_revision >= 0 AND final_resume_revision <= 65536
	),
	resume_final_hash TEXT NOT NULL DEFAULT '' CHECK (length(resume_final_hash) IN (0, 64)),
	resumed_at INTEGER,
	created_at INTEGER NOT NULL,
	updated_at INTEGER NOT NULL,
	FOREIGN KEY(controller_id, thread_id, line_id)
		REFERENCES pr_development_thread_controllers(id, thread_id, line_id)
		ON DELETE RESTRICT,
	CHECK (
		(source_kind = 'controller_recovery' AND source_operation_id = '' AND
		 source_operation_kind = '' AND mode = 'candidate' AND
		 substr(source_recovery_id, 1, 5) = 'pdri_') OR
		(source_kind = 'operation_recovery' AND source_operation_id <> '' AND
		 substr(source_recovery_id, 1, 5) = 'pdri_' AND
		 substr(source_operation_id, 1, 5) = 'pdop_' AND
		 ((source_operation_kind IN ('adopt', 'resume') AND mode = 'candidate') OR
		  (source_operation_kind = 'commit' AND mode = 'commit_recovery'))) OR
		(source_kind = 'suspended_resume_recovery' AND source_operation_id = '' AND
		 source_operation_kind = '' AND mode = 'candidate' AND
		 substr(source_recovery_id, 1, 5) = 'pdsi_')
	),
	CHECK (suspend_intent_id = id),
	CHECK (
		length(source_tree) = length(source_commit) AND
		length(tip_commit) = length(source_commit) AND
		length(tree) = length(source_commit)
	),
	CHECK (suspend_claims = suspend_claim_epoch),
	CHECK (resume_claims = resume_claim_epoch),
	CHECK (
		(status = 'suspend_pending' AND suspend_claim_id = '' AND suspend_claim_owner = '' AND
		 suspend_claim_token = '' AND suspend_claim_until IS NULL AND suspend_claim_epoch = 0 AND
		 suspend_claimed_at IS NULL AND suspend_claim_token_digest = '') OR
		(status = 'suspend_claimed' AND suspend_claim_id <> '' AND suspend_claim_owner <> '' AND
		 suspend_claim_token <> '' AND suspend_claim_until IS NOT NULL AND suspend_claim_epoch >= 1 AND
		 suspend_claimed_at IS NOT NULL AND suspend_claim_token_digest = '') OR
		(status IN ('suspended', 'resume_pending', 'resume_claimed', 'resumed') AND
		 suspend_claim_id <> '' AND suspend_claim_owner <> '' AND suspend_claim_token = '' AND
		 suspend_claim_until IS NULL AND suspend_claim_epoch >= 1 AND suspend_claimed_at IS NOT NULL AND
		 length(suspend_claim_token_digest) = 64)
	),
	CHECK (
		(status IN ('suspend_pending', 'suspend_claimed') AND suspension_reservation_key <> '') OR
		(status IN ('suspended', 'resume_pending', 'resume_claimed', 'resumed') AND
		 suspension_reservation_key = '')
	),
	CHECK (
		(status IN ('suspend_pending', 'suspend_claimed') AND length(suspend_result_json) = 0 AND
		 suspend_result_hash = '' AND candidate_tree = '' AND candidate_digest = '' AND
		 changed_file_count = 0 AND suspension_hash = '' AND prepared_commit = '' AND
		 prepared_tree = '' AND prepared_commit_applied = 0 AND
		 final_suspension_revision = 0 AND suspension_final_hash = '' AND suspended_at IS NULL) OR
		(status IN ('suspended', 'resume_pending', 'resume_claimed', 'resumed') AND
		 length(suspend_result_json) >= 2 AND length(suspend_result_hash) = 64 AND
		 length(candidate_tree) = length(tree) AND length(candidate_digest) = 64 AND
		 length(suspension_hash) = 64 AND final_suspension_revision > source_final_revision AND
		 length(suspension_final_hash) = 64 AND suspended_at IS NOT NULL AND
		 ((mode = 'candidate' AND prepared_commit = '' AND prepared_tree = '' AND
		   prepared_commit_applied = 0) OR
		  (mode = 'commit_recovery' AND length(prepared_commit) = length(tree) AND
		   length(prepared_tree) = length(tree))))
	),
	CHECK (
		(status IN ('suspend_pending', 'suspend_claimed', 'suspended') AND
		 resume_attempt_id IS NULL AND resume_intent_id = '' AND resume_reservation_key = '' AND
		 resume_reservation_digest = '' AND length(resume_request_json) = 0 AND
		 resume_request_hash = '' AND resume_intent_hash = '' AND resume_prepared_at IS NULL AND
		 resume_claim_id = '' AND resume_claim_owner = '' AND resume_claim_token = '' AND
		 resume_claim_until IS NULL AND resume_claim_epoch = 0 AND resume_claimed_at IS NULL AND
		 resume_claim_token_digest = '' AND length(resume_result_json) = 0 AND
		 resume_result_hash = '' AND rotation_hash = '' AND new_mutation_lease_epoch = 0 AND
		 new_mutation_lease_token_digest = '' AND new_mutation_lease_until IS NULL AND
		 final_resume_revision = 0 AND resume_final_hash = '' AND resumed_at IS NULL) OR
		(status IN ('resume_pending', 'resume_claimed', 'resumed') AND
		 resume_attempt_id IS NOT NULL AND resume_intent_id <> '' AND
		 length(resume_reservation_digest) = 64 AND length(resume_request_json) >= 2 AND
		 length(resume_request_hash) = 64 AND length(resume_intent_hash) = 64 AND
		 resume_prepared_at IS NOT NULL)
	),
	CHECK (
		(status = 'resume_pending' AND resume_reservation_key <> '' AND
		 resume_claim_id = '' AND resume_claim_owner = '' AND resume_claim_token = '' AND
		 resume_claim_until IS NULL AND resume_claim_epoch = 0 AND resume_claimed_at IS NULL AND
		 resume_claim_token_digest = '' AND length(resume_result_json) = 0 AND
		 resume_result_hash = '' AND rotation_hash = '' AND new_mutation_lease_epoch = 0 AND
		 new_mutation_lease_token_digest = '' AND new_mutation_lease_until IS NULL AND
		 final_resume_revision = 0 AND resume_final_hash = '' AND resumed_at IS NULL) OR
		(status = 'resume_claimed' AND resume_reservation_key <> '' AND
		 resume_claim_id <> '' AND resume_claim_owner <> '' AND resume_claim_token <> '' AND
		 resume_claim_until IS NOT NULL AND resume_claim_epoch >= 1 AND resume_claimed_at IS NOT NULL AND
		 resume_claim_token_digest = '' AND length(resume_result_json) = 0 AND
		 resume_result_hash = '' AND rotation_hash = '' AND new_mutation_lease_epoch = 0 AND
		 new_mutation_lease_token_digest = '' AND new_mutation_lease_until IS NULL AND
		 final_resume_revision = 0 AND resume_final_hash = '' AND resumed_at IS NULL) OR
		(status = 'resumed' AND resume_reservation_key = '' AND
		 resume_claim_id <> '' AND resume_claim_owner <> '' AND resume_claim_token = '' AND
		 resume_claim_until IS NULL AND resume_claim_epoch >= 1 AND resume_claimed_at IS NOT NULL AND
		 length(resume_claim_token_digest) = 64 AND length(resume_result_json) >= 2 AND
		 length(resume_result_hash) = 64 AND length(rotation_hash) = 64 AND
		 new_mutation_lease_epoch >= 1 AND length(new_mutation_lease_token_digest) = 64 AND
		 new_mutation_lease_until IS NOT NULL AND final_resume_revision > final_suspension_revision AND
		 length(resume_final_hash) = 64 AND resumed_at IS NOT NULL) OR
		(status IN ('suspend_pending', 'suspend_claimed', 'suspended'))
	),
	CHECK (suspended_at IS NULL OR suspended_at >= created_at),
	CHECK (resume_prepared_at IS NULL OR resume_prepared_at >= suspended_at),
	CHECK (resumed_at IS NULL OR resumed_at >= resume_prepared_at),
	CHECK (updated_at >= created_at)
);`

	schemaV17PRDevelopmentControllerSuspensionsActiveIndex = `CREATE UNIQUE INDEX IF NOT EXISTS pr_development_controller_suspensions_active
	ON pr_development_controller_suspensions(controller_id)
	WHERE status <> 'resumed';`
	schemaV17PRDevelopmentControllerSuspensionsOrdinalIndex = `CREATE UNIQUE INDEX IF NOT EXISTS pr_development_controller_suspensions_ordinal
	ON pr_development_controller_suspensions(controller_id, ordinal);`
	schemaV17PRDevelopmentControllerSuspensionsSourceIndex = `CREATE UNIQUE INDEX IF NOT EXISTS pr_development_controller_suspensions_source
	ON pr_development_controller_suspensions(source_kind, source_recovery_id);`
	schemaV17PRDevelopmentControllerSuspensionsOperationIndex = `CREATE UNIQUE INDEX IF NOT EXISTS pr_development_controller_suspensions_operation
	ON pr_development_controller_suspensions(source_operation_id)
	WHERE source_operation_id <> '';`
	schemaV17PRDevelopmentControllerSuspensionsSuspendIntentIndex = `CREATE UNIQUE INDEX IF NOT EXISTS pr_development_controller_suspensions_suspend_intent
	ON pr_development_controller_suspensions(suspend_intent_id);`
	schemaV17PRDevelopmentControllerSuspensionsIntentHashIndex = `CREATE UNIQUE INDEX IF NOT EXISTS pr_development_controller_suspensions_intent_hash
	ON pr_development_controller_suspensions(intent_hash);`
	schemaV17PRDevelopmentControllerSuspensionsSuspendBearerIndex = `CREATE UNIQUE INDEX IF NOT EXISTS pr_development_controller_suspensions_suspend_bearer
	ON pr_development_controller_suspensions(suspension_reservation_key)
	WHERE suspension_reservation_key <> '';`
	schemaV17PRDevelopmentControllerSuspensionsSuspendBearerDigestIndex = `CREATE UNIQUE INDEX IF NOT EXISTS pr_development_controller_suspensions_suspend_bearer_digest
	ON pr_development_controller_suspensions(suspension_reservation_digest);`
	schemaV17PRDevelopmentControllerSuspensionsSuspendClaimIndex = `CREATE UNIQUE INDEX IF NOT EXISTS pr_development_controller_suspensions_suspend_claim
	ON pr_development_controller_suspensions(suspend_claim_id)
	WHERE suspend_claim_id <> '';`
	schemaV17PRDevelopmentControllerSuspensionsHashIndex = `CREATE UNIQUE INDEX IF NOT EXISTS pr_development_controller_suspensions_hash
	ON pr_development_controller_suspensions(suspension_hash)
	WHERE suspension_hash <> '';`
	schemaV17PRDevelopmentControllerSuspensionsFinalHashIndex = `CREATE UNIQUE INDEX IF NOT EXISTS pr_development_controller_suspensions_final_hash
	ON pr_development_controller_suspensions(suspension_final_hash)
	WHERE suspension_final_hash <> '';`
	schemaV17PRDevelopmentControllerSuspensionsResumeIntentIndex = `CREATE UNIQUE INDEX IF NOT EXISTS pr_development_controller_suspensions_resume_intent
	ON pr_development_controller_suspensions(resume_intent_id)
	WHERE resume_intent_id <> '';`
	schemaV17PRDevelopmentControllerSuspensionsResumeIntentHashIndex = `CREATE UNIQUE INDEX IF NOT EXISTS pr_development_controller_suspensions_resume_intent_hash
	ON pr_development_controller_suspensions(resume_intent_hash)
	WHERE resume_intent_hash <> '';`
	schemaV17PRDevelopmentControllerSuspensionsResumeBearerIndex = `CREATE UNIQUE INDEX IF NOT EXISTS pr_development_controller_suspensions_resume_bearer
	ON pr_development_controller_suspensions(resume_reservation_key)
	WHERE resume_reservation_key <> '';`
	schemaV17PRDevelopmentControllerSuspensionsResumeBearerDigestIndex = `CREATE UNIQUE INDEX IF NOT EXISTS pr_development_controller_suspensions_resume_bearer_digest
	ON pr_development_controller_suspensions(resume_reservation_digest)
	WHERE resume_reservation_digest <> '';`
	schemaV17PRDevelopmentControllerSuspensionsResumeClaimIndex = `CREATE UNIQUE INDEX IF NOT EXISTS pr_development_controller_suspensions_resume_claim
	ON pr_development_controller_suspensions(resume_claim_id)
	WHERE resume_claim_id <> '';`
	schemaV17PRDevelopmentControllerSuspensionsRotationHashIndex = `CREATE UNIQUE INDEX IF NOT EXISTS pr_development_controller_suspensions_rotation_hash
	ON pr_development_controller_suspensions(rotation_hash)
	WHERE rotation_hash <> '';`
	schemaV17PRDevelopmentControllerSuspensionsResumeFinalHashIndex = `CREATE UNIQUE INDEX IF NOT EXISTS pr_development_controller_suspensions_resume_final_hash
	ON pr_development_controller_suspensions(resume_final_hash)
	WHERE resume_final_hash <> '';`
	schemaV17PRDevelopmentControllerSuspensionsSuspendClaimableIndex = `CREATE INDEX IF NOT EXISTS pr_development_controller_suspensions_suspend_claimable
	ON pr_development_controller_suspensions(status, suspend_claim_until, updated_at, id);`
	schemaV17PRDevelopmentControllerSuspensionsResumeClaimableIndex = `CREATE INDEX IF NOT EXISTS pr_development_controller_suspensions_resume_claimable
	ON pr_development_controller_suspensions(status, resume_claim_until, updated_at, id);`

	schemaV17 = schemaV17PRDevelopmentControllerSuspensionsTable + "\n" +
		schemaV17PRDevelopmentControllerSuspensionsActiveIndex + "\n" +
		schemaV17PRDevelopmentControllerSuspensionsOrdinalIndex + "\n" +
		schemaV17PRDevelopmentControllerSuspensionsSourceIndex + "\n" +
		schemaV17PRDevelopmentControllerSuspensionsOperationIndex + "\n" +
		schemaV17PRDevelopmentControllerSuspensionsSuspendIntentIndex + "\n" +
		schemaV17PRDevelopmentControllerSuspensionsIntentHashIndex + "\n" +
		schemaV17PRDevelopmentControllerSuspensionsSuspendBearerIndex + "\n" +
		schemaV17PRDevelopmentControllerSuspensionsSuspendBearerDigestIndex + "\n" +
		schemaV17PRDevelopmentControllerSuspensionsSuspendClaimIndex + "\n" +
		schemaV17PRDevelopmentControllerSuspensionsHashIndex + "\n" +
		schemaV17PRDevelopmentControllerSuspensionsFinalHashIndex + "\n" +
		schemaV17PRDevelopmentControllerSuspensionsResumeIntentIndex + "\n" +
		schemaV17PRDevelopmentControllerSuspensionsResumeIntentHashIndex + "\n" +
		schemaV17PRDevelopmentControllerSuspensionsResumeBearerIndex + "\n" +
		schemaV17PRDevelopmentControllerSuspensionsResumeBearerDigestIndex + "\n" +
		schemaV17PRDevelopmentControllerSuspensionsResumeClaimIndex + "\n" +
		schemaV17PRDevelopmentControllerSuspensionsRotationHashIndex + "\n" +
		schemaV17PRDevelopmentControllerSuspensionsResumeFinalHashIndex + "\n" +
		schemaV17PRDevelopmentControllerSuspensionsSuspendClaimableIndex + "\n" +
		schemaV17PRDevelopmentControllerSuspensionsResumeClaimableIndex
)

const schemaV17PRDevelopmentControllerColumns = `id, thread_id, owner_session_id, agent_id,
	revision, phase, line_id, workspace_id, source_clone_url, source_ref, source_commit,
	source_tree, line_version, mutation_epoch, tip_commit, tree, current_attempt_id,
	lease_kind, lease_owner, lease_token, lease_until, lease_epoch, claims,
	mutation_reservation_key, fence_count, fences_digest, created_at, updated_at`

func migrateSchemaV17(ctx context.Context, conn *sql.Conn) error {
	if _, err := conn.ExecContext(ctx, "PRAGMA defer_foreign_keys = ON"); err != nil {
		return fmt.Errorf("defer controller foreign keys: %w", err)
	}
	backupSQL := `CREATE TEMP TABLE pr_development_thread_controllers_v17_backup AS SELECT ` +
		schemaV17PRDevelopmentControllerColumns + ` FROM pr_development_thread_controllers`
	if _, err := conn.ExecContext(ctx, backupSQL); err != nil {
		return fmt.Errorf("snapshot v16 controllers: %w", err)
	}
	if _, err := conn.ExecContext(ctx, `DROP TABLE pr_development_thread_controllers`); err != nil {
		return fmt.Errorf("replace v16 controllers: %w", err)
	}
	if _, err := conn.ExecContext(ctx, schemaV17PRDevelopmentControllersTable); err != nil {
		return fmt.Errorf("create v17 controllers: %w", err)
	}
	copySQL := `INSERT INTO pr_development_thread_controllers (` +
		schemaV17PRDevelopmentControllerColumns + `) SELECT ` +
		schemaV17PRDevelopmentControllerColumns +
		` FROM pr_development_thread_controllers_v17_backup`
	if _, err := conn.ExecContext(ctx, copySQL); err != nil {
		return fmt.Errorf("restore v16 controllers: %w", err)
	}
	for _, query := range []string{
		`SELECT count(*) FROM (
			SELECT ` + schemaV17PRDevelopmentControllerColumns + `
			FROM pr_development_thread_controllers_v17_backup
			EXCEPT
			SELECT ` + schemaV17PRDevelopmentControllerColumns + `
			FROM pr_development_thread_controllers
		)`,
		`SELECT count(*) FROM (
			SELECT ` + schemaV17PRDevelopmentControllerColumns + `
			FROM pr_development_thread_controllers
			EXCEPT
			SELECT ` + schemaV17PRDevelopmentControllerColumns + `
			FROM pr_development_thread_controllers_v17_backup
		)`,
	} {
		var differences int
		if err := conn.QueryRowContext(ctx, query).Scan(&differences); err != nil {
			return fmt.Errorf("verify preserved v16 controllers: %w", err)
		}
		if differences != 0 {
			return fmt.Errorf("verify preserved v16 controllers: %d rows differ", differences)
		}
	}
	if _, err := conn.ExecContext(ctx, `DROP TABLE pr_development_thread_controllers_v17_backup`); err != nil {
		return fmt.Errorf("drop v16 controller snapshot: %w", err)
	}
	if _, err := conn.ExecContext(ctx,
		schemaV10PRDevelopmentControllerWorkspaceIndex+"\n"+
			schemaV10PRDevelopmentControllerReservationIndex+"\n"+
			schemaV10PRDevelopmentControllerLeaseIndex+"\n"+schemaV17,
	); err != nil {
		return fmt.Errorf("create eventing schema v17: %w", err)
	}
	rows, err := conn.QueryContext(ctx, `PRAGMA foreign_key_check`)
	if err != nil {
		return fmt.Errorf("check v17 foreign keys: %w", err)
	}
	defer rows.Close()
	if rows.Next() {
		var table string
		var rowID sql.NullInt64
		var parent string
		var foreignKey int
		if err := rows.Scan(&table, &rowID, &parent, &foreignKey); err != nil {
			return fmt.Errorf("scan v17 foreign-key violation: %w", err)
		}
		return fmt.Errorf("v17 foreign-key violation: table=%s rowid=%v parent=%s key=%d",
			table, rowID, parent, foreignKey)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate v17 foreign-key check: %w", err)
	}
	return nil
}

func validateSchemaV17(ctx context.Context, conn *sql.Conn) error {
	if err := validateSchemaV10ForVersion(ctx, conn, true); err != nil {
		return err
	}
	binary := func(name string) schemaIndexColumn {
		return schemaIndexColumn{name: name, collation: "BINARY"}
	}
	unique := []schemaUniqueIndexSpec{
		{origin: "pk", columns: []schemaIndexColumn{binary("id")}},
		{
			name:    "pr_development_controller_suspensions_active",
			origin:  "c",
			partial: true,
			columns: []schemaIndexColumn{binary("controller_id")},
		},
		{
			name:    "pr_development_controller_suspensions_ordinal",
			origin:  "c",
			columns: []schemaIndexColumn{binary("controller_id"), binary("ordinal")},
		},
		{
			name:    "pr_development_controller_suspensions_source",
			origin:  "c",
			columns: []schemaIndexColumn{binary("source_kind"), binary("source_recovery_id")},
		},
		{
			name:    "pr_development_controller_suspensions_operation",
			origin:  "c",
			partial: true,
			columns: []schemaIndexColumn{binary("source_operation_id")},
		},
		{
			name:    "pr_development_controller_suspensions_suspend_intent",
			origin:  "c",
			columns: []schemaIndexColumn{binary("suspend_intent_id")},
		},
		{
			name:    "pr_development_controller_suspensions_intent_hash",
			origin:  "c",
			columns: []schemaIndexColumn{binary("intent_hash")},
		},
		{
			name:    "pr_development_controller_suspensions_suspend_bearer",
			origin:  "c",
			partial: true,
			columns: []schemaIndexColumn{binary("suspension_reservation_key")},
		},
		{
			name:    "pr_development_controller_suspensions_suspend_bearer_digest",
			origin:  "c",
			columns: []schemaIndexColumn{binary("suspension_reservation_digest")},
		},
		{
			name:    "pr_development_controller_suspensions_suspend_claim",
			origin:  "c",
			partial: true,
			columns: []schemaIndexColumn{binary("suspend_claim_id")},
		},
		{
			name:    "pr_development_controller_suspensions_hash",
			origin:  "c",
			partial: true,
			columns: []schemaIndexColumn{binary("suspension_hash")},
		},
		{
			name:    "pr_development_controller_suspensions_final_hash",
			origin:  "c",
			partial: true,
			columns: []schemaIndexColumn{binary("suspension_final_hash")},
		},
		{
			name:    "pr_development_controller_suspensions_resume_intent",
			origin:  "c",
			partial: true,
			columns: []schemaIndexColumn{binary("resume_intent_id")},
		},
		{
			name:    "pr_development_controller_suspensions_resume_intent_hash",
			origin:  "c",
			partial: true,
			columns: []schemaIndexColumn{binary("resume_intent_hash")},
		},
		{
			name:    "pr_development_controller_suspensions_resume_bearer",
			origin:  "c",
			partial: true,
			columns: []schemaIndexColumn{binary("resume_reservation_key")},
		},
		{
			name:    "pr_development_controller_suspensions_resume_bearer_digest",
			origin:  "c",
			partial: true,
			columns: []schemaIndexColumn{binary("resume_reservation_digest")},
		},
		{
			name:    "pr_development_controller_suspensions_resume_claim",
			origin:  "c",
			partial: true,
			columns: []schemaIndexColumn{binary("resume_claim_id")},
		},
		{
			name:    "pr_development_controller_suspensions_rotation_hash",
			origin:  "c",
			partial: true,
			columns: []schemaIndexColumn{binary("rotation_hash")},
		},
		{
			name:    "pr_development_controller_suspensions_resume_final_hash",
			origin:  "c",
			partial: true,
			columns: []schemaIndexColumn{binary("resume_final_hash")},
		},
	}
	if err := validateSchemaTable(ctx, conn, schemaTableSpec{
		name: "pr_development_controller_suspensions", createSQL: schemaV17PRDevelopmentControllerSuspensionsTable,
		uniqueIndexes: unique,
	}); err != nil {
		return err
	}
	for _, index := range []schemaIndexSpec{
		{name: "pr_development_controller_suspensions_active", createSQL: schemaV17PRDevelopmentControllerSuspensionsActiveIndex},
		{name: "pr_development_controller_suspensions_ordinal", createSQL: schemaV17PRDevelopmentControllerSuspensionsOrdinalIndex},
		{name: "pr_development_controller_suspensions_source", createSQL: schemaV17PRDevelopmentControllerSuspensionsSourceIndex},
		{name: "pr_development_controller_suspensions_operation", createSQL: schemaV17PRDevelopmentControllerSuspensionsOperationIndex},
		{name: "pr_development_controller_suspensions_suspend_intent", createSQL: schemaV17PRDevelopmentControllerSuspensionsSuspendIntentIndex},
		{name: "pr_development_controller_suspensions_intent_hash", createSQL: schemaV17PRDevelopmentControllerSuspensionsIntentHashIndex},
		{name: "pr_development_controller_suspensions_suspend_bearer", createSQL: schemaV17PRDevelopmentControllerSuspensionsSuspendBearerIndex},
		{name: "pr_development_controller_suspensions_suspend_bearer_digest", createSQL: schemaV17PRDevelopmentControllerSuspensionsSuspendBearerDigestIndex},
		{name: "pr_development_controller_suspensions_suspend_claim", createSQL: schemaV17PRDevelopmentControllerSuspensionsSuspendClaimIndex},
		{name: "pr_development_controller_suspensions_hash", createSQL: schemaV17PRDevelopmentControllerSuspensionsHashIndex},
		{name: "pr_development_controller_suspensions_final_hash", createSQL: schemaV17PRDevelopmentControllerSuspensionsFinalHashIndex},
		{name: "pr_development_controller_suspensions_resume_intent", createSQL: schemaV17PRDevelopmentControllerSuspensionsResumeIntentIndex},
		{name: "pr_development_controller_suspensions_resume_intent_hash", createSQL: schemaV17PRDevelopmentControllerSuspensionsResumeIntentHashIndex},
		{name: "pr_development_controller_suspensions_resume_bearer", createSQL: schemaV17PRDevelopmentControllerSuspensionsResumeBearerIndex},
		{name: "pr_development_controller_suspensions_resume_bearer_digest", createSQL: schemaV17PRDevelopmentControllerSuspensionsResumeBearerDigestIndex},
		{name: "pr_development_controller_suspensions_resume_claim", createSQL: schemaV17PRDevelopmentControllerSuspensionsResumeClaimIndex},
		{name: "pr_development_controller_suspensions_rotation_hash", createSQL: schemaV17PRDevelopmentControllerSuspensionsRotationHashIndex},
		{name: "pr_development_controller_suspensions_resume_final_hash", createSQL: schemaV17PRDevelopmentControllerSuspensionsResumeFinalHashIndex},
		{name: "pr_development_controller_suspensions_suspend_claimable", createSQL: schemaV17PRDevelopmentControllerSuspensionsSuspendClaimableIndex},
		{name: "pr_development_controller_suspensions_resume_claimable", createSQL: schemaV17PRDevelopmentControllerSuspensionsResumeClaimableIndex},
	} {
		if err := validateSchemaIndex(ctx, conn, index); err != nil {
			return err
		}
	}
	return nil
}
