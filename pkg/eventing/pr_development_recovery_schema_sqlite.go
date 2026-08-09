//go:build !mipsle && !netbsd && !(freebsd && arm)

package eventing

import (
	"context"
	"database/sql"
)

const (
	schemaV12PRDevelopmentControllerRecoveryIntentsTable = `CREATE TABLE IF NOT EXISTS pr_development_controller_recovery_intents (
	id TEXT PRIMARY KEY,
	controller_id TEXT NOT NULL REFERENCES pr_development_thread_controllers(id) ON DELETE RESTRICT,
	attempt_id TEXT NOT NULL REFERENCES pr_development_repair_attempts(id) ON DELETE RESTRICT,
	ordinal INTEGER NOT NULL CHECK (ordinal >= 0 AND ordinal < 8192),
	recovery_revision INTEGER NOT NULL CHECK (recovery_revision >= 2 AND recovery_revision <= 65536),
	mode TEXT NOT NULL CHECK (mode IN ('unbound', 'bound')),
	status TEXT NOT NULL CHECK (status IN ('pending', 'claimed', 'finalized')),
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
	line_version INTEGER NOT NULL DEFAULT 0 CHECK (line_version >= 0 AND line_version <= 8192),
	mutation_epoch INTEGER NOT NULL DEFAULT 0 CHECK (mutation_epoch >= 0 AND mutation_epoch <= 8193),
	tip_commit TEXT NOT NULL DEFAULT '' CHECK (length(tip_commit) IN (0, 40, 64)),
	tree TEXT NOT NULL DEFAULT '' CHECK (length(tree) IN (0, 40, 64)),
	previous_reservation_key TEXT NOT NULL DEFAULT '' CHECK (
		length(CAST(previous_reservation_key AS BLOB)) <= 256
	),
	replacement_reservation_key TEXT NOT NULL DEFAULT '' CHECK (
		length(CAST(replacement_reservation_key AS BLOB)) <= 256
	),
	previous_reservation_digest TEXT NOT NULL CHECK (length(previous_reservation_digest) = 64),
	replacement_reservation_digest TEXT NOT NULL CHECK (length(replacement_reservation_digest) = 64),
	expired_controller_revision INTEGER NOT NULL CHECK (
		expired_controller_revision >= 1 AND expired_controller_revision < 65536
	),
	expired_lease_epoch INTEGER NOT NULL CHECK (expired_lease_epoch >= 1),
	expired_lease_token_digest TEXT NOT NULL CHECK (length(expired_lease_token_digest) = 64),
	previous_hash TEXT NOT NULL CHECK (length(previous_hash) = 64),
	intent_hash TEXT NOT NULL CHECK (length(intent_hash) = 64),
	claim_id TEXT NOT NULL DEFAULT '' CHECK (length(CAST(claim_id AS BLOB)) <= 256),
	claim_owner TEXT NOT NULL DEFAULT '' CHECK (length(CAST(claim_owner AS BLOB)) <= 256),
	claim_token TEXT NOT NULL DEFAULT '' CHECK (length(CAST(claim_token AS BLOB)) <= 128),
	claim_until INTEGER,
	claim_epoch INTEGER NOT NULL DEFAULT 0 CHECK (claim_epoch >= 0),
	claims INTEGER NOT NULL DEFAULT 0 CHECK (claims >= 0),
	rotation_result_hash TEXT NOT NULL DEFAULT '' CHECK (length(rotation_result_hash) IN (0, 64)),
	recovery_claim_token_digest TEXT NOT NULL DEFAULT '' CHECK (
		length(recovery_claim_token_digest) IN (0, 64)
	),
	new_mutation_lease_epoch INTEGER NOT NULL DEFAULT 0 CHECK (new_mutation_lease_epoch >= 0),
	new_mutation_lease_token_digest TEXT NOT NULL DEFAULT '' CHECK (
		length(new_mutation_lease_token_digest) IN (0, 64)
	),
	new_mutation_lease_until INTEGER,
	final_revision INTEGER NOT NULL DEFAULT 0 CHECK (
		final_revision >= 0 AND final_revision <= 65536
	),
	final_hash TEXT NOT NULL DEFAULT '' CHECK (length(final_hash) IN (0, 64)),
	created_at INTEGER NOT NULL,
	claimed_at INTEGER,
	finalized_at INTEGER,
	updated_at INTEGER NOT NULL,
	UNIQUE(controller_id, ordinal),
	UNIQUE(controller_id, recovery_revision),
	UNIQUE(previous_reservation_digest),
	UNIQUE(replacement_reservation_digest),
	CHECK (recovery_revision = expired_controller_revision + 1),
	CHECK (previous_reservation_digest <> replacement_reservation_digest),
	CHECK (claims = claim_epoch),
	CHECK (updated_at >= created_at),
	CHECK (
		(mode = 'unbound' AND source_tree = '' AND line_version = 0 AND
		 mutation_epoch = 0 AND tip_commit = '' AND tree = '') OR
		(mode = 'bound' AND length(source_tree) = length(source_commit) AND
		 length(tip_commit) = length(source_commit) AND
		 length(tree) = length(source_commit) AND mutation_epoch = line_version + 1)
	),
	CHECK (
		(status = 'pending' AND previous_reservation_key <> '' AND
		 replacement_reservation_key <> '' AND claim_id = '' AND claim_owner = '' AND
		 claim_token = '' AND claim_until IS NULL AND claim_epoch = 0 AND claims = 0 AND
		 claimed_at IS NULL AND rotation_result_hash = '' AND
		 recovery_claim_token_digest = '' AND new_mutation_lease_epoch = 0 AND
		 new_mutation_lease_token_digest = '' AND new_mutation_lease_until IS NULL AND
		 final_revision = 0 AND
		 final_hash = '' AND finalized_at IS NULL) OR
		(status = 'claimed' AND previous_reservation_key <> '' AND
		 replacement_reservation_key <> '' AND claim_id <> '' AND claim_owner <> '' AND
		 claim_token <> '' AND claim_until IS NOT NULL AND claim_epoch >= 1 AND claims >= 1 AND
		 claimed_at IS NOT NULL AND rotation_result_hash = '' AND
		 recovery_claim_token_digest = '' AND new_mutation_lease_epoch = 0 AND
		 new_mutation_lease_token_digest = '' AND new_mutation_lease_until IS NULL AND
		 final_revision = 0 AND
		 final_hash = '' AND finalized_at IS NULL) OR
		(status = 'finalized' AND previous_reservation_key = '' AND
		 replacement_reservation_key = '' AND claim_id <> '' AND claim_owner <> '' AND
		 claim_token = '' AND claim_until IS NULL AND claim_epoch >= 1 AND claims >= 1 AND
		 claimed_at IS NOT NULL AND length(rotation_result_hash) = 64 AND
		 length(recovery_claim_token_digest) = 64 AND new_mutation_lease_epoch >= 2 AND
		 length(new_mutation_lease_token_digest) = 64 AND new_mutation_lease_until IS NOT NULL AND
		 final_revision = recovery_revision + 1 AND length(final_hash) = 64 AND
		 finalized_at IS NOT NULL)
	),
	CHECK (claim_until IS NULL OR claim_until > updated_at),
	CHECK (claimed_at IS NULL OR claimed_at >= created_at),
	CHECK (finalized_at IS NULL OR (claimed_at IS NOT NULL AND finalized_at >= claimed_at AND
		new_mutation_lease_until > finalized_at)),
	CHECK (updated_at >= COALESCE(finalized_at, claimed_at, created_at))
);`
	schemaV12PRDevelopmentControllerRecoveryActiveIndex = `CREATE UNIQUE INDEX IF NOT EXISTS pr_development_controller_recovery_active
	ON pr_development_controller_recovery_intents(controller_id)
	WHERE status <> 'finalized';`
	schemaV12PRDevelopmentControllerRecoveryClaimIndex = `CREATE UNIQUE INDEX IF NOT EXISTS pr_development_controller_recovery_claim
	ON pr_development_controller_recovery_intents(claim_id)
	WHERE claim_id <> '';`
	schemaV12PRDevelopmentControllerRecoveryPreviousKeyIndex = `CREATE UNIQUE INDEX IF NOT EXISTS pr_development_controller_recovery_previous_key
	ON pr_development_controller_recovery_intents(previous_reservation_key)
	WHERE previous_reservation_key <> '';`
	schemaV12PRDevelopmentControllerRecoveryReplacementKeyIndex = `CREATE UNIQUE INDEX IF NOT EXISTS pr_development_controller_recovery_replacement_key
	ON pr_development_controller_recovery_intents(replacement_reservation_key)
	WHERE replacement_reservation_key <> '';`
	schemaV12PRDevelopmentControllerRecoveryClaimableIndex = `CREATE INDEX IF NOT EXISTS pr_development_controller_recovery_claimable
	ON pr_development_controller_recovery_intents(status, claim_until, created_at, id);`
	schemaV12 = schemaV12PRDevelopmentControllerRecoveryIntentsTable + "\n" +
		schemaV12PRDevelopmentControllerRecoveryActiveIndex + "\n" +
		schemaV12PRDevelopmentControllerRecoveryClaimIndex + "\n" +
		schemaV12PRDevelopmentControllerRecoveryPreviousKeyIndex + "\n" +
		schemaV12PRDevelopmentControllerRecoveryReplacementKeyIndex + "\n" +
		schemaV12PRDevelopmentControllerRecoveryClaimableIndex
)

func validateSchemaV12(ctx context.Context, conn *sql.Conn) error {
	binary := func(name string) schemaIndexColumn {
		return schemaIndexColumn{name: name, collation: "BINARY"}
	}
	if err := validateSchemaTable(ctx, conn, schemaTableSpec{
		name:      "pr_development_controller_recovery_intents",
		createSQL: schemaV12PRDevelopmentControllerRecoveryIntentsTable,
		uniqueIndexes: []schemaUniqueIndexSpec{
			{origin: "pk", columns: []schemaIndexColumn{binary("id")}},
			{origin: "u", columns: []schemaIndexColumn{binary("controller_id"), binary("ordinal")}},
			{origin: "u", columns: []schemaIndexColumn{binary("controller_id"), binary("recovery_revision")}},
			{origin: "u", columns: []schemaIndexColumn{binary("previous_reservation_digest")}},
			{origin: "u", columns: []schemaIndexColumn{binary("replacement_reservation_digest")}},
			{
				name:    "pr_development_controller_recovery_active",
				origin:  "c",
				partial: true,
				columns: []schemaIndexColumn{binary("controller_id")},
			},
			{
				name:    "pr_development_controller_recovery_claim",
				origin:  "c",
				partial: true,
				columns: []schemaIndexColumn{binary("claim_id")},
			},
			{
				name:    "pr_development_controller_recovery_previous_key",
				origin:  "c",
				partial: true,
				columns: []schemaIndexColumn{binary("previous_reservation_key")},
			},
			{
				name:    "pr_development_controller_recovery_replacement_key",
				origin:  "c",
				partial: true,
				columns: []schemaIndexColumn{binary("replacement_reservation_key")},
			},
		},
	}); err != nil {
		return err
	}
	for _, index := range []schemaIndexSpec{
		{name: "pr_development_controller_recovery_active", createSQL: schemaV12PRDevelopmentControllerRecoveryActiveIndex},
		{name: "pr_development_controller_recovery_claim", createSQL: schemaV12PRDevelopmentControllerRecoveryClaimIndex},
		{name: "pr_development_controller_recovery_previous_key", createSQL: schemaV12PRDevelopmentControllerRecoveryPreviousKeyIndex},
		{name: "pr_development_controller_recovery_replacement_key", createSQL: schemaV12PRDevelopmentControllerRecoveryReplacementKeyIndex},
		{name: "pr_development_controller_recovery_claimable", createSQL: schemaV12PRDevelopmentControllerRecoveryClaimableIndex},
	} {
		if err := validateSchemaIndex(ctx, conn, index); err != nil {
			return err
		}
	}
	return nil
}
