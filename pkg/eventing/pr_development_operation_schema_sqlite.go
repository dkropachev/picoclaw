//go:build !mipsle && !netbsd && !(freebsd && arm)

package eventing

import (
	"context"
	"database/sql"
)

const (
	schemaV13PRDevelopmentControllerOperationIntentsTable = `CREATE TABLE IF NOT EXISTS pr_development_controller_operation_intents (
	id TEXT PRIMARY KEY,
	controller_id TEXT NOT NULL REFERENCES pr_development_thread_controllers(id) ON DELETE RESTRICT,
	attempt_id TEXT NOT NULL REFERENCES pr_development_repair_attempts(id) ON DELETE RESTRICT,
	ordinal INTEGER NOT NULL CHECK (ordinal >= 0 AND ordinal < 24576),
	kind TEXT NOT NULL CHECK (kind IN ('adopt', 'resume', 'commit', 'park')),
	status TEXT NOT NULL CHECK (status IN ('pending', 'recovery_pending', 'recovery_claimed', 'finalized')),
	prepared_controller_revision INTEGER NOT NULL CHECK (
		prepared_controller_revision >= 1 AND prepared_controller_revision <= 65536
	),
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
	source_tree TEXT NOT NULL DEFAULT '' CHECK (length(source_tree) IN (0, 40, 64)),
	line_version INTEGER NOT NULL CHECK (line_version >= 0 AND line_version <= 8192),
	mutation_epoch INTEGER NOT NULL CHECK (mutation_epoch >= 0 AND mutation_epoch <= 8193),
	tip_commit TEXT NOT NULL DEFAULT '' CHECK (length(tip_commit) IN (0, 40, 64)),
	tree TEXT NOT NULL DEFAULT '' CHECK (length(tree) IN (0, 40, 64)),
	mutation_reservation_digest TEXT NOT NULL CHECK (
		length(mutation_reservation_digest) = 64
	),
	mutation_lease_epoch INTEGER NOT NULL CHECK (mutation_lease_epoch >= 1),
	mutation_lease_token_digest TEXT NOT NULL CHECK (
		length(mutation_lease_token_digest) = 64
	),
	effect_intent_id TEXT NOT NULL DEFAULT '' CHECK (
		length(CAST(effect_intent_id AS BLOB)) <= 256
	),
	request_json BLOB NOT NULL CHECK (
		typeof(request_json) = 'blob' AND length(request_json) >= 2 AND
		length(request_json) <= 32768
	),
	request_hash TEXT NOT NULL CHECK (length(request_hash) = 64),
	previous_hash TEXT NOT NULL CHECK (length(previous_hash) = 64),
	intent_hash TEXT NOT NULL CHECK (length(intent_hash) = 64),
	recovery_id TEXT NOT NULL DEFAULT '' CHECK (
		length(CAST(recovery_id AS BLOB)) <= 256
	),
	replacement_reservation_key TEXT NOT NULL DEFAULT '' CHECK (
		length(CAST(replacement_reservation_key AS BLOB)) <= 256
	),
	replacement_reservation_digest TEXT NOT NULL DEFAULT '' CHECK (
		length(replacement_reservation_digest) IN (0, 64)
	),
	recovery_revision INTEGER NOT NULL DEFAULT 0 CHECK (
		recovery_revision >= 0 AND recovery_revision <= 65536
	),
	expired_controller_revision INTEGER NOT NULL DEFAULT 0 CHECK (
		expired_controller_revision >= 0 AND expired_controller_revision <= 65536
	),
	expired_lease_epoch INTEGER NOT NULL DEFAULT 0 CHECK (expired_lease_epoch >= 0),
	expired_lease_token_digest TEXT NOT NULL DEFAULT '' CHECK (
		length(expired_lease_token_digest) IN (0, 64)
	),
	recovery_lease_until INTEGER,
	recovery_staged_at INTEGER,
	recovery_hash TEXT NOT NULL DEFAULT '' CHECK (
		length(CAST(recovery_hash AS BLOB)) IN (0, 64)
	),
	claim_id TEXT NOT NULL DEFAULT '' CHECK (length(CAST(claim_id AS BLOB)) <= 256),
	claim_owner TEXT NOT NULL DEFAULT '' CHECK (length(CAST(claim_owner AS BLOB)) <= 256),
	claim_token TEXT NOT NULL DEFAULT '' CHECK (length(CAST(claim_token AS BLOB)) <= 128),
	claim_until INTEGER,
	claim_epoch INTEGER NOT NULL DEFAULT 0 CHECK (claim_epoch >= 0),
	claims INTEGER NOT NULL DEFAULT 0 CHECK (claims >= 0),
	claimed_at INTEGER,
	rotation_result_hash TEXT NOT NULL DEFAULT '' CHECK (
		length(rotation_result_hash) IN (0, 64)
	),
	recovery_claim_token_digest TEXT NOT NULL DEFAULT '' CHECK (
		length(recovery_claim_token_digest) IN (0, 64)
	),
	new_mutation_lease_epoch INTEGER NOT NULL DEFAULT 0 CHECK (
		new_mutation_lease_epoch >= 0
	),
	new_mutation_lease_token_digest TEXT NOT NULL DEFAULT '' CHECK (
		length(new_mutation_lease_token_digest) IN (0, 64)
	),
	new_mutation_lease_until INTEGER,
	result_json BLOB NOT NULL DEFAULT X'' CHECK (
		typeof(result_json) = 'blob' AND length(result_json) <= 32768
	),
	result_hash TEXT NOT NULL DEFAULT '' CHECK (length(result_hash) IN (0, 64)),
	stage_authorization_digest TEXT NOT NULL CHECK (
		length(stage_authorization_digest) = 64
	),
	final_controller_revision INTEGER NOT NULL DEFAULT 0 CHECK (
		final_controller_revision >= 0 AND final_controller_revision <= 65536
	),
	final_controller_phase TEXT NOT NULL DEFAULT '' CHECK (
		final_controller_phase IN ('', 'mutation', 'review_pending')
	),
	final_fence_hash TEXT NOT NULL DEFAULT '' CHECK (length(final_fence_hash) IN (0, 64)),
	final_hash TEXT NOT NULL DEFAULT '' CHECK (length(final_hash) IN (0, 64)),
	created_at INTEGER NOT NULL,
	finalized_at INTEGER,
	updated_at INTEGER NOT NULL,
	UNIQUE(controller_id, ordinal),
	UNIQUE(attempt_id, kind),
	CHECK (
		(kind IN ('adopt', 'resume') AND effect_intent_id = '') OR
		(kind IN ('commit', 'park') AND effect_intent_id <> '')
	),
	CHECK (stage_authorization_digest = mutation_lease_token_digest),
	CHECK (claims = claim_epoch),
	CHECK (updated_at >= created_at),
	CHECK (
		(recovery_staged_at IS NULL AND recovery_id = '' AND
		 replacement_reservation_key = '' AND replacement_reservation_digest = '' AND
		 recovery_revision = 0 AND expired_controller_revision = 0 AND
		 expired_lease_epoch = 0 AND expired_lease_token_digest = '' AND
		 recovery_lease_until IS NULL AND recovery_hash = '') OR
		(recovery_staged_at IS NOT NULL AND recovery_revision >= 2 AND
		 recovery_revision = expired_controller_revision + 1 AND
		 expired_controller_revision = prepared_controller_revision AND
		 expired_lease_epoch = mutation_lease_epoch AND
		 expired_lease_token_digest = mutation_lease_token_digest AND
		 recovery_lease_until IS NOT NULL AND recovery_lease_until <= recovery_staged_at AND
		 length(recovery_hash) = 64 AND (
			(kind = 'park' AND recovery_id = '' AND replacement_reservation_key = '' AND
			 replacement_reservation_digest = '') OR
			(kind <> 'park' AND recovery_id <> '' AND
			 length(replacement_reservation_digest) = 64 AND
			 ((status IN ('recovery_pending', 'recovery_claimed') AND
			   replacement_reservation_key <> '') OR
			  (status = 'finalized' AND replacement_reservation_key = '')))
		 ))
	),
	CHECK (
		(status IN ('pending', 'recovery_pending') AND claim_id = '' AND claim_owner = '' AND
		 claim_token = '' AND claim_until IS NULL AND claim_epoch = 0 AND claims = 0 AND
		 claimed_at IS NULL AND recovery_claim_token_digest = '') OR
		(status = 'recovery_claimed' AND claim_id <> '' AND claim_owner <> '' AND
		 claim_token <> '' AND claim_until IS NOT NULL AND claim_epoch >= 1 AND claims >= 1 AND
		 claimed_at IS NOT NULL AND recovery_claim_token_digest = '') OR
		(status = 'finalized' AND recovery_staged_at IS NULL AND claim_id = '' AND
		 claim_owner = '' AND claim_token = '' AND claim_until IS NULL AND claim_epoch = 0 AND
		 claims = 0 AND claimed_at IS NULL AND recovery_claim_token_digest = '') OR
		(status = 'finalized' AND recovery_staged_at IS NOT NULL AND claim_id <> '' AND
		 claim_owner <> '' AND claim_token = '' AND claim_until IS NULL AND claim_epoch >= 1 AND
		 claims >= 1 AND claimed_at IS NOT NULL AND length(recovery_claim_token_digest) = 64)
	),
	CHECK (
		(status <> 'finalized' AND length(result_json) = 0 AND result_hash = '' AND
		 final_controller_revision = 0 AND final_controller_phase = '' AND
		 final_fence_hash = '' AND final_hash = '' AND finalized_at IS NULL) OR
		(status = 'finalized' AND length(result_json) >= 2 AND length(result_hash) = 64 AND
		 final_controller_revision >= prepared_controller_revision AND
		 ((kind = 'park' AND final_controller_phase = 'review_pending' AND
		   length(final_fence_hash) = 64) OR
		  (kind <> 'park' AND final_controller_phase = 'mutation' AND final_fence_hash = '')) AND
		 length(final_hash) = 64 AND finalized_at IS NOT NULL)
	),
	CHECK (
		(recovery_staged_at IS NULL AND rotation_result_hash = '' AND
		 new_mutation_lease_epoch = 0 AND new_mutation_lease_token_digest = '' AND
		 new_mutation_lease_until IS NULL) OR
		(recovery_staged_at IS NOT NULL AND status <> 'finalized' AND
		 rotation_result_hash = '' AND new_mutation_lease_epoch = 0 AND
		 new_mutation_lease_token_digest = '' AND new_mutation_lease_until IS NULL) OR
		(recovery_staged_at IS NOT NULL AND status = 'finalized' AND kind = 'park' AND
		 rotation_result_hash = '' AND new_mutation_lease_epoch = 0 AND
		 new_mutation_lease_token_digest = '' AND new_mutation_lease_until IS NULL) OR
		(recovery_staged_at IS NOT NULL AND status = 'finalized' AND kind <> 'park' AND
		 length(rotation_result_hash) = 64 AND new_mutation_lease_epoch >= 2 AND
		 length(new_mutation_lease_token_digest) = 64 AND
		 new_mutation_lease_until IS NOT NULL AND new_mutation_lease_until > finalized_at)
	),
	CHECK (status = 'pending' OR recovery_staged_at IS NOT NULL OR status = 'finalized'),
	CHECK (status <> 'pending' OR recovery_staged_at IS NULL),
	CHECK (
		status <> 'finalized' OR recovery_staged_at IS NOT NULL OR
		(kind = 'commit' AND
		 final_controller_revision = prepared_controller_revision) OR
		(kind IN ('adopt', 'resume', 'park') AND
		 final_controller_revision = prepared_controller_revision + 1)
	),
	CHECK (
		status <> 'finalized' OR recovery_staged_at IS NULL OR
		(kind = 'commit' AND final_controller_revision = recovery_revision + 1) OR
		(kind IN ('adopt', 'resume') AND
		 final_controller_revision = recovery_revision + 2) OR
		(kind = 'park' AND final_controller_revision = recovery_revision)
	),
	CHECK (claim_until IS NULL OR claim_until > updated_at),
	CHECK (claimed_at IS NULL OR (recovery_staged_at IS NOT NULL AND
	       claimed_at >= recovery_staged_at)),
	CHECK (finalized_at IS NULL OR finalized_at >= COALESCE(claimed_at, created_at)),
	CHECK (updated_at >= COALESCE(finalized_at, claimed_at, recovery_staged_at, created_at))
);`
	schemaV13PRDevelopmentControllerOperationActiveIndex = `CREATE UNIQUE INDEX IF NOT EXISTS pr_development_controller_operation_active
	ON pr_development_controller_operation_intents(controller_id)
	WHERE status <> 'finalized';`
	schemaV13PRDevelopmentControllerOperationEffectIndex = `CREATE UNIQUE INDEX IF NOT EXISTS pr_development_controller_operation_effect
	ON pr_development_controller_operation_intents(effect_intent_id)
	WHERE effect_intent_id <> '';`
	schemaV13PRDevelopmentControllerOperationRecoveryIndex = `CREATE UNIQUE INDEX IF NOT EXISTS pr_development_controller_operation_recovery
	ON pr_development_controller_operation_intents(recovery_id)
	WHERE recovery_id <> '';`
	schemaV13PRDevelopmentControllerOperationReplacementKeyIndex = `CREATE UNIQUE INDEX IF NOT EXISTS pr_development_controller_operation_replacement_key
	ON pr_development_controller_operation_intents(replacement_reservation_key)
	WHERE replacement_reservation_key <> '';`
	schemaV13PRDevelopmentControllerOperationReplacementDigestIndex = `CREATE UNIQUE INDEX IF NOT EXISTS pr_development_controller_operation_replacement_digest
	ON pr_development_controller_operation_intents(replacement_reservation_digest)
	WHERE replacement_reservation_digest <> '';`
	schemaV13PRDevelopmentControllerOperationClaimIndex = `CREATE UNIQUE INDEX IF NOT EXISTS pr_development_controller_operation_claim
	ON pr_development_controller_operation_intents(claim_id)
	WHERE claim_id <> '';`
	schemaV13PRDevelopmentControllerOperationClaimableIndex = `CREATE INDEX IF NOT EXISTS pr_development_controller_operation_claimable
	ON pr_development_controller_operation_intents(status, claim_until, created_at, id);`
	schemaV13 = schemaV13PRDevelopmentControllerOperationIntentsTable + "\n" +
		schemaV13PRDevelopmentControllerOperationActiveIndex + "\n" +
		schemaV13PRDevelopmentControllerOperationEffectIndex + "\n" +
		schemaV13PRDevelopmentControllerOperationRecoveryIndex + "\n" +
		schemaV13PRDevelopmentControllerOperationReplacementKeyIndex + "\n" +
		schemaV13PRDevelopmentControllerOperationReplacementDigestIndex + "\n" +
		schemaV13PRDevelopmentControllerOperationClaimIndex + "\n" +
		schemaV13PRDevelopmentControllerOperationClaimableIndex
)

func validateSchemaV13(ctx context.Context, conn *sql.Conn) error {
	binary := func(name string) schemaIndexColumn {
		return schemaIndexColumn{name: name, collation: "BINARY"}
	}
	if err := validateSchemaTable(ctx, conn, schemaTableSpec{
		name:      "pr_development_controller_operation_intents",
		createSQL: schemaV13PRDevelopmentControllerOperationIntentsTable,
		uniqueIndexes: []schemaUniqueIndexSpec{
			{origin: "pk", columns: []schemaIndexColumn{binary("id")}},
			{origin: "u", columns: []schemaIndexColumn{binary("controller_id"), binary("ordinal")}},
			{origin: "u", columns: []schemaIndexColumn{binary("attempt_id"), binary("kind")}},
			{
				name:    "pr_development_controller_operation_active",
				origin:  "c",
				partial: true,
				columns: []schemaIndexColumn{binary("controller_id")},
			},
			{
				name:    "pr_development_controller_operation_effect",
				origin:  "c",
				partial: true,
				columns: []schemaIndexColumn{binary("effect_intent_id")},
			},
			{
				name:    "pr_development_controller_operation_recovery",
				origin:  "c",
				partial: true,
				columns: []schemaIndexColumn{binary("recovery_id")},
			},
			{
				name:    "pr_development_controller_operation_replacement_key",
				origin:  "c",
				partial: true,
				columns: []schemaIndexColumn{binary("replacement_reservation_key")},
			},
			{
				name:    "pr_development_controller_operation_replacement_digest",
				origin:  "c",
				partial: true,
				columns: []schemaIndexColumn{binary("replacement_reservation_digest")},
			},
			{
				name:    "pr_development_controller_operation_claim",
				origin:  "c",
				partial: true,
				columns: []schemaIndexColumn{binary("claim_id")},
			},
		},
	}); err != nil {
		return err
	}
	for _, index := range []schemaIndexSpec{
		{name: "pr_development_controller_operation_active", createSQL: schemaV13PRDevelopmentControllerOperationActiveIndex},
		{name: "pr_development_controller_operation_effect", createSQL: schemaV13PRDevelopmentControllerOperationEffectIndex},
		{name: "pr_development_controller_operation_recovery", createSQL: schemaV13PRDevelopmentControllerOperationRecoveryIndex},
		{name: "pr_development_controller_operation_replacement_key", createSQL: schemaV13PRDevelopmentControllerOperationReplacementKeyIndex},
		{name: "pr_development_controller_operation_replacement_digest", createSQL: schemaV13PRDevelopmentControllerOperationReplacementDigestIndex},
		{name: "pr_development_controller_operation_claim", createSQL: schemaV13PRDevelopmentControllerOperationClaimIndex},
		{name: "pr_development_controller_operation_claimable", createSQL: schemaV13PRDevelopmentControllerOperationClaimableIndex},
	} {
		if err := validateSchemaIndex(ctx, conn, index); err != nil {
			return err
		}
	}
	return nil
}
