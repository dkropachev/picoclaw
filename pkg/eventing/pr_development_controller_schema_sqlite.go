//go:build !mipsle && !netbsd && !(freebsd && arm)

package eventing

import (
	"context"
	"database/sql"
)

const (
	schemaV10PRDevelopmentControllersTable = `CREATE TABLE IF NOT EXISTS pr_development_thread_controllers (
	id TEXT PRIMARY KEY,
	thread_id TEXT NOT NULL UNIQUE REFERENCES pr_development_threads(id) ON DELETE RESTRICT,
	owner_session_id TEXT NOT NULL UNIQUE REFERENCES pr_development_repair_sessions(id) ON DELETE RESTRICT,
	agent_id TEXT NOT NULL CHECK (
		length(CAST(agent_id AS BLOB)) >= 1 AND
		length(CAST(agent_id AS BLOB)) <= 64
	),
	revision INTEGER NOT NULL CHECK (revision >= 1 AND revision <= 65536),
	phase TEXT NOT NULL CHECK (phase IN (
		'idle', 'mutation', 'review_pending', 'review', 'ready', 'recovery_required'
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
			mutation_reservation_key <> '')
	),
	CHECK (phase <> 'mutation' OR line_version < 8192),
	CHECK (updated_at >= created_at)
);`
	schemaV10PRDevelopmentControllerWorkspaceIndex = `CREATE UNIQUE INDEX IF NOT EXISTS pr_development_thread_controllers_workspace
	ON pr_development_thread_controllers(workspace_id)
	WHERE workspace_id <> '';`
	schemaV10PRDevelopmentControllerReservationIndex = `CREATE UNIQUE INDEX IF NOT EXISTS pr_development_thread_controllers_reservation
	ON pr_development_thread_controllers(mutation_reservation_key)
	WHERE mutation_reservation_key <> '';`
	schemaV10PRDevelopmentControllerLeaseIndex = `CREATE INDEX IF NOT EXISTS pr_development_thread_controllers_lease
	ON pr_development_thread_controllers(phase, lease_until, updated_at, id);`
	schemaV10PRDevelopmentReviewFencesTable = `CREATE TABLE IF NOT EXISTS pr_development_attempt_review_fences (
	attempt_id TEXT PRIMARY KEY REFERENCES pr_development_repair_attempts(id) ON DELETE RESTRICT,
	controller_id TEXT NOT NULL,
	thread_id TEXT NOT NULL,
	line_id TEXT NOT NULL,
	ordinal INTEGER NOT NULL CHECK (ordinal >= 0 AND ordinal < 8192),
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
	line_review_digest TEXT NOT NULL CHECK (length(line_review_digest) = 64),
	mutation_reservation_digest TEXT NOT NULL CHECK (
		length(mutation_reservation_digest) = 64
	),
	mutation_lease_epoch INTEGER NOT NULL CHECK (mutation_lease_epoch >= 1),
	mutation_lease_token_digest TEXT NOT NULL CHECK (
		length(mutation_lease_token_digest) = 64
	),
	mutation_controller_revision INTEGER NOT NULL CHECK (
		mutation_controller_revision >= 1 AND mutation_controller_revision <= 65536
	),
	review_lease_epoch INTEGER NOT NULL DEFAULT 0 CHECK (review_lease_epoch >= 0),
	review_lease_token_digest TEXT NOT NULL DEFAULT '' CHECK (
		length(review_lease_token_digest) IN (0, 64)
	),
	review_controller_revision INTEGER NOT NULL DEFAULT 0 CHECK (
		review_controller_revision >= 0 AND review_controller_revision <= 65536
	),
	previous_hash TEXT NOT NULL CHECK (length(previous_hash) = 64),
	fence_hash TEXT NOT NULL CHECK (length(fence_hash) = 64),
	created_at INTEGER NOT NULL,
	reviewed_at INTEGER,
	UNIQUE(controller_id, ordinal),
	UNIQUE(thread_id, line_version),
	UNIQUE(controller_id, park_intent_id),
	UNIQUE(mutation_reservation_digest),
	FOREIGN KEY(controller_id, thread_id, line_id)
		REFERENCES pr_development_thread_controllers(id, thread_id, line_id)
		ON DELETE RESTRICT,
	CHECK (mutation_epoch = line_version),
	CHECK (length(base_commit) = length(tip_commit) AND length(tip_commit) = length(tree)),
	CHECK ((no_changes = 1 AND base_commit = tip_commit) OR
	       (no_changes = 0 AND base_commit <> tip_commit)),
	CHECK (
		(reviewed_at IS NULL AND review_lease_epoch = 0 AND
		 review_lease_token_digest = '' AND review_controller_revision = 0) OR
		(reviewed_at IS NOT NULL AND reviewed_at >= created_at AND
		 review_lease_epoch >= 1 AND length(review_lease_token_digest) = 64 AND
		 review_controller_revision >= 1)
	)
);`
	schemaV10 = schemaV10PRDevelopmentControllersTable + "\n" +
		schemaV10PRDevelopmentControllerWorkspaceIndex + "\n" +
		schemaV10PRDevelopmentControllerReservationIndex + "\n" +
		schemaV10PRDevelopmentControllerLeaseIndex + "\n" +
		schemaV10PRDevelopmentReviewFencesTable
)

func validateSchemaV10(ctx context.Context, conn *sql.Conn) error {
	binary := func(name string) schemaIndexColumn {
		return schemaIndexColumn{name: name, collation: "BINARY"}
	}
	if err := validateSchemaTable(ctx, conn, schemaTableSpec{
		name:      "pr_development_thread_controllers",
		createSQL: schemaV10PRDevelopmentControllersTable,
		uniqueIndexes: []schemaUniqueIndexSpec{
			{origin: "pk", columns: []schemaIndexColumn{binary("id")}},
			{origin: "u", columns: []schemaIndexColumn{binary("thread_id")}},
			{origin: "u", columns: []schemaIndexColumn{binary("owner_session_id")}},
			{origin: "u", columns: []schemaIndexColumn{binary("line_id")}},
			{
				origin: "u",
				columns: []schemaIndexColumn{
					binary("id"), binary("thread_id"), binary("line_id"),
				},
			},
			{
				name:    "pr_development_thread_controllers_workspace",
				origin:  "c",
				partial: true,
				columns: []schemaIndexColumn{binary("workspace_id")},
			},
			{
				name:    "pr_development_thread_controllers_reservation",
				origin:  "c",
				partial: true,
				columns: []schemaIndexColumn{binary("mutation_reservation_key")},
			},
		},
	}); err != nil {
		return err
	}
	if err := validateSchemaTable(ctx, conn, schemaTableSpec{
		name:      "pr_development_attempt_review_fences",
		createSQL: schemaV10PRDevelopmentReviewFencesTable,
		uniqueIndexes: []schemaUniqueIndexSpec{
			{origin: "pk", columns: []schemaIndexColumn{binary("attempt_id")}},
			{
				origin:  "u",
				columns: []schemaIndexColumn{binary("controller_id"), binary("ordinal")},
			},
			{
				origin:  "u",
				columns: []schemaIndexColumn{binary("thread_id"), binary("line_version")},
			},
			{
				origin:  "u",
				columns: []schemaIndexColumn{binary("controller_id"), binary("park_intent_id")},
			},
			{
				origin:  "u",
				columns: []schemaIndexColumn{binary("mutation_reservation_digest")},
			},
		},
	}); err != nil {
		return err
	}
	for _, index := range []schemaIndexSpec{
		{
			name:      "pr_development_thread_controllers_workspace",
			createSQL: schemaV10PRDevelopmentControllerWorkspaceIndex,
		},
		{
			name:      "pr_development_thread_controllers_reservation",
			createSQL: schemaV10PRDevelopmentControllerReservationIndex,
		},
		{
			name:      "pr_development_thread_controllers_lease",
			createSQL: schemaV10PRDevelopmentControllerLeaseIndex,
		},
	} {
		if err := validateSchemaIndex(ctx, conn, index); err != nil {
			return err
		}
	}
	return nil
}
