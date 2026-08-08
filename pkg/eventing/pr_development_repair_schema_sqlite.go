//go:build !mipsle && !netbsd && !(freebsd && arm)

package eventing

import (
	"context"
	"database/sql"
)

const (
	schemaV8PRDevelopmentRepairSessionsTable = `CREATE TABLE IF NOT EXISTS pr_development_repair_sessions (
	id TEXT PRIMARY KEY,
	case_id TEXT NOT NULL UNIQUE REFERENCES pr_development_cases(id) ON DELETE CASCADE,
	version INTEGER NOT NULL CHECK (version >= 1 AND version <= 1024),
	agent_id TEXT NOT NULL CHECK (
		length(CAST(agent_id AS BLOB)) >= 1 AND
		length(CAST(agent_id AS BLOB)) <= 64
	),
	head_repository TEXT NOT NULL DEFAULT '' CHECK (length(CAST(head_repository AS BLOB)) <= 256),
	head_ref TEXT NOT NULL DEFAULT '' CHECK (length(CAST(head_ref AS BLOB)) <= 1024),
	head_sha TEXT NOT NULL DEFAULT '' CHECK (length(CAST(head_sha AS BLOB)) <= 64),
	clone_url TEXT NOT NULL DEFAULT '' CHECK (length(CAST(clone_url AS BLOB)) <= 4096),
	review_digest TEXT NOT NULL DEFAULT '' CHECK (length(CAST(review_digest AS BLOB)) <= 71),
	reservation_key TEXT NOT NULL CHECK (
		length(CAST(reservation_key AS BLOB)) >= 1 AND
		length(CAST(reservation_key AS BLOB)) <= 1024
	),
	workspace_id TEXT NOT NULL DEFAULT '' CHECK (length(CAST(workspace_id AS BLOB)) <= 256),
	claim_suppressed INTEGER NOT NULL DEFAULT 0 CHECK (claim_suppressed IN (0, 1)),
	created_at INTEGER NOT NULL,
	updated_at INTEGER NOT NULL,
	CHECK (
		(head_repository = '' AND head_ref = '' AND head_sha = '' AND
		 clone_url = '' AND review_digest = '') OR
		(head_repository <> '' AND head_ref <> '' AND head_sha <> '' AND
		 clone_url <> '' AND review_digest <> '')
	),
	CHECK (workspace_id = '' OR head_repository <> ''),
	CHECK (updated_at >= created_at)
);`
	schemaV8PRDevelopmentRepairAttemptsTable = `CREATE TABLE IF NOT EXISTS pr_development_repair_attempts (
	id TEXT PRIMARY KEY,
	session_id TEXT NOT NULL REFERENCES pr_development_repair_sessions(id) ON DELETE CASCADE,
	ordinal INTEGER NOT NULL CHECK (ordinal >= 0 AND ordinal < 64),
	expected_repair_version INTEGER NOT NULL CHECK (
		expected_repair_version >= 0 AND expected_repair_version <= 1024
	),
	conversation_version INTEGER NOT NULL CHECK (conversation_version >= 0 AND conversation_version <= 256),
	idempotency_key TEXT NOT NULL CHECK (
		length(CAST(idempotency_key AS BLOB)) >= 1 AND
		length(CAST(idempotency_key AS BLOB)) <= 256
	),
	instruction TEXT NOT NULL CHECK (
		length(CAST(instruction AS BLOB)) >= 1 AND
		length(CAST(instruction AS BLOB)) <= 4096
	),
	status TEXT NOT NULL CHECK (status IN (
		'queued', 'preparing', 'running', 'completed', 'failed', 'recovery_required'
	)),
	lease_owner TEXT NOT NULL DEFAULT '' CHECK (length(CAST(lease_owner AS BLOB)) <= 256),
	lease_token TEXT NOT NULL DEFAULT '' CHECK (length(CAST(lease_token AS BLOB)) <= 128),
	lease_until INTEGER,
	claims INTEGER NOT NULL DEFAULT 0 CHECK (claims >= 0),
	summary TEXT NOT NULL DEFAULT '' CHECK (length(CAST(summary AS BLOB)) <= 4096),
	error_code TEXT NOT NULL DEFAULT '' CHECK (error_code IN (
		'', 'provider_changed', 'not_actionable', 'runtime_unavailable',
		'workspace_unavailable', 'repair_failed', 'recovery_required', 'internal_error'
	)),
	internal_error TEXT NOT NULL DEFAULT '' CHECK (length(CAST(internal_error AS BLOB)) <= 16384),
	iterations INTEGER NOT NULL DEFAULT 0 CHECK (iterations >= 0 AND iterations <= 128),
	created_at INTEGER NOT NULL,
	updated_at INTEGER NOT NULL,
	UNIQUE(session_id, ordinal),
	UNIQUE(session_id, idempotency_key),
	CHECK (
		(status IN ('preparing', 'running') AND lease_owner <> '' AND
		 lease_token <> '' AND lease_until IS NOT NULL) OR
		(status NOT IN ('preparing', 'running') AND lease_owner = '' AND
		 lease_token = '' AND lease_until IS NULL)
	),
	CHECK ((status = 'queued' AND claims = 0) OR (status <> 'queued' AND claims >= 1)),
	CHECK (
		(status IN ('queued', 'preparing', 'running') AND summary = '' AND
		 error_code = '' AND internal_error = '' AND iterations = 0) OR
		(status = 'completed' AND summary <> '' AND error_code = '' AND
		 internal_error = '' AND iterations >= 1) OR
		(status = 'failed' AND summary <> '' AND error_code IN (
			'provider_changed', 'not_actionable', 'runtime_unavailable',
			'workspace_unavailable', 'repair_failed', 'internal_error'
		)) OR
		(status = 'recovery_required' AND summary <> '' AND
		 error_code = 'recovery_required')
	),
	CHECK (updated_at >= created_at)
);`
	schemaV8PRDevelopmentRepairActiveIndex = `CREATE UNIQUE INDEX IF NOT EXISTS pr_development_repair_attempts_active
	ON pr_development_repair_attempts(session_id)
	WHERE status IN ('queued', 'preparing', 'running');`
	schemaV8PRDevelopmentRepairClaimIndex = `CREATE INDEX IF NOT EXISTS pr_development_repair_attempts_claim
	ON pr_development_repair_attempts(status, lease_until, created_at, id);`
	schemaV8 = schemaV8PRDevelopmentRepairSessionsTable + "\n" +
		schemaV8PRDevelopmentRepairAttemptsTable + "\n" +
		schemaV8PRDevelopmentRepairActiveIndex + "\n" +
		schemaV8PRDevelopmentRepairClaimIndex
)

func validateSchemaV8(ctx context.Context, conn *sql.Conn) error {
	binary := func(name string) schemaIndexColumn {
		return schemaIndexColumn{name: name, collation: "BINARY"}
	}
	if err := validateSchemaTable(ctx, conn, schemaTableSpec{
		name:      "pr_development_repair_sessions",
		createSQL: schemaV8PRDevelopmentRepairSessionsTable,
		uniqueIndexes: []schemaUniqueIndexSpec{
			{origin: "pk", columns: []schemaIndexColumn{binary("id")}},
			{origin: "u", columns: []schemaIndexColumn{binary("case_id")}},
		},
	}); err != nil {
		return err
	}
	if err := validateSchemaTable(ctx, conn, schemaTableSpec{
		name:      "pr_development_repair_attempts",
		createSQL: schemaV8PRDevelopmentRepairAttemptsTable,
		uniqueIndexes: []schemaUniqueIndexSpec{
			{origin: "pk", columns: []schemaIndexColumn{binary("id")}},
			{
				origin:  "u",
				columns: []schemaIndexColumn{binary("session_id"), binary("ordinal")},
			},
			{
				origin:  "u",
				columns: []schemaIndexColumn{binary("session_id"), binary("idempotency_key")},
			},
			{
				name:    "pr_development_repair_attempts_active",
				origin:  "c",
				partial: true,
				columns: []schemaIndexColumn{binary("session_id")},
			},
		},
	}); err != nil {
		return err
	}
	for _, index := range []schemaIndexSpec{
		{
			name:      "pr_development_repair_attempts_active",
			createSQL: schemaV8PRDevelopmentRepairActiveIndex,
		},
		{
			name:      "pr_development_repair_attempts_claim",
			createSQL: schemaV8PRDevelopmentRepairClaimIndex,
		},
	} {
		if err := validateSchemaIndex(ctx, conn, index); err != nil {
			return err
		}
	}
	return nil
}
