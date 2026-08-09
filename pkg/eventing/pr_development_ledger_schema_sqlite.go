//go:build !mipsle && !netbsd && !(freebsd && arm)

package eventing

import (
	"context"
	"database/sql"
)

const (
	schemaV11PRDevelopmentLedgerEntriesTable = `CREATE TABLE IF NOT EXISTS pr_development_ledger_entries (
	id TEXT PRIMARY KEY,
	thread_id TEXT NOT NULL REFERENCES pr_development_threads(id) ON DELETE RESTRICT,
	ordinal INTEGER NOT NULL CHECK (ordinal >= 0 AND ordinal < 16384),
	kind TEXT NOT NULL CHECK (kind IN ('attempt', 'review')),
	attempt_id TEXT NOT NULL REFERENCES pr_development_attempt_review_fences(attempt_id) ON DELETE RESTRICT,
	fence_ordinal INTEGER NOT NULL CHECK (fence_ordinal >= 0 AND fence_ordinal < 8192),
	case_id TEXT NOT NULL REFERENCES pr_development_cases(id) ON DELETE RESTRICT,
	case_ordinal INTEGER NOT NULL CHECK (case_ordinal >= 0 AND case_ordinal < 8192),
	commit_oid TEXT NOT NULL DEFAULT '' CHECK (length(commit_oid) IN (0, 40, 64)),
	tree_oid TEXT NOT NULL DEFAULT '' CHECK (length(tree_oid) IN (0, 40, 64)),
	no_changes INTEGER CHECK (no_changes IS NULL OR no_changes IN (0, 1)),
	summary TEXT NOT NULL CHECK (
		length(CAST(summary AS BLOB)) >= 1 AND
		length(CAST(summary AS BLOB)) <= 4096
	),
	ci_plan_digest TEXT NOT NULL DEFAULT '' CHECK (length(ci_plan_digest) IN (0, 64)),
	ci_result_digest TEXT NOT NULL DEFAULT '' CHECK (length(ci_result_digest) IN (0, 64)),
	review_outcome TEXT NOT NULL DEFAULT '' CHECK (
		review_outcome IN ('', 'passed', 'changes_required', 'attention_required')
	),
	finding_count INTEGER NOT NULL DEFAULT 0 CHECK (finding_count >= 0 AND finding_count <= 128),
	fence_hash TEXT NOT NULL CHECK (length(fence_hash) = 64),
	previous_hash TEXT NOT NULL CHECK (length(previous_hash) = 64),
	entry_hash TEXT NOT NULL CHECK (length(entry_hash) = 64),
	created_at INTEGER NOT NULL,
	UNIQUE(thread_id, ordinal),
	UNIQUE(attempt_id, kind),
	UNIQUE(id, kind),
	CHECK (
		(kind = 'attempt' AND ordinal = 2 * fence_ordinal AND
		 length(commit_oid) IN (40, 64) AND length(tree_oid) = length(commit_oid) AND
		 no_changes IS NOT NULL AND length(ci_plan_digest) = 64 AND
		 length(ci_result_digest) = 64 AND review_outcome = '' AND finding_count = 0) OR
		(kind = 'review' AND ordinal = 2 * fence_ordinal + 1 AND
		 commit_oid = '' AND tree_oid = '' AND no_changes IS NULL AND
		 ci_plan_digest = '' AND ci_result_digest = '' AND review_outcome <> '' AND
		 (review_outcome <> 'passed' OR finding_count = 0) AND
		 (review_outcome <> 'changes_required' OR finding_count >= 1))
	)
);`
	schemaV11PRDevelopmentLedgerFindingsTable = `CREATE TABLE IF NOT EXISTS pr_development_ledger_review_findings (
	entry_id TEXT NOT NULL,
	entry_kind TEXT NOT NULL DEFAULT 'review' CHECK (entry_kind = 'review'),
	ordinal INTEGER NOT NULL CHECK (ordinal >= 0 AND ordinal < 128),
	severity TEXT NOT NULL CHECK (severity IN ('critical', 'high', 'medium', 'low')),
	title TEXT NOT NULL CHECK (
		length(CAST(title AS BLOB)) >= 1 AND length(CAST(title AS BLOB)) <= 512
	),
	file TEXT NOT NULL DEFAULT '' CHECK (length(CAST(file AS BLOB)) <= 4096),
	line INTEGER CHECK (line IS NULL OR (line >= 1 AND line <= 2147483647)),
	message TEXT NOT NULL CHECK (
		length(CAST(message AS BLOB)) >= 1 AND length(CAST(message AS BLOB)) <= 8192
	),
	evidence TEXT NOT NULL DEFAULT '' CHECK (length(CAST(evidence AS BLOB)) <= 8192),
	impact TEXT NOT NULL DEFAULT '' CHECK (length(CAST(impact AS BLOB)) <= 4096),
	recommendation TEXT NOT NULL DEFAULT '' CHECK (
		length(CAST(recommendation AS BLOB)) <= 8192
	),
	validation TEXT NOT NULL DEFAULT '' CHECK (length(CAST(validation AS BLOB)) <= 4096),
	PRIMARY KEY(entry_id, ordinal),
	FOREIGN KEY(entry_id, entry_kind)
		REFERENCES pr_development_ledger_entries(id, kind) ON DELETE RESTRICT
);`
	schemaV11PRDevelopmentLedgerCheckpointsTable = `CREATE TABLE IF NOT EXISTS pr_development_ledger_checkpoints (
	id TEXT PRIMARY KEY,
	thread_id TEXT NOT NULL REFERENCES pr_development_threads(id) ON DELETE RESTRICT,
	generation INTEGER NOT NULL CHECK (generation >= 0 AND generation < 8192),
	through_ordinal INTEGER NOT NULL CHECK (
		through_ordinal >= 1 AND through_ordinal < 16384 AND through_ordinal % 2 = 1
	),
	source_digest TEXT NOT NULL CHECK (length(source_digest) = 64),
	summary TEXT NOT NULL CHECK (
		length(CAST(summary AS BLOB)) >= 1 AND
		length(CAST(summary AS BLOB)) <= 32768
	),
	compactor_id TEXT NOT NULL CHECK (
		length(CAST(compactor_id AS BLOB)) >= 1 AND
		length(CAST(compactor_id AS BLOB)) <= 256
	),
	prompt_digest TEXT NOT NULL CHECK (length(prompt_digest) = 64),
	previous_hash TEXT NOT NULL CHECK (length(previous_hash) = 64),
	checkpoint_hash TEXT NOT NULL CHECK (length(checkpoint_hash) = 64),
	created_at INTEGER NOT NULL,
	UNIQUE(thread_id, generation),
	UNIQUE(thread_id, through_ordinal)
);`
	schemaV11 = schemaV11PRDevelopmentLedgerEntriesTable + "\n" +
		schemaV11PRDevelopmentLedgerFindingsTable + "\n" +
		schemaV11PRDevelopmentLedgerCheckpointsTable
)

func validateSchemaV11(ctx context.Context, conn *sql.Conn) error {
	binary := func(name string) schemaIndexColumn {
		return schemaIndexColumn{name: name, collation: "BINARY"}
	}
	for _, table := range []schemaTableSpec{
		{
			name:      "pr_development_ledger_entries",
			createSQL: schemaV11PRDevelopmentLedgerEntriesTable,
			uniqueIndexes: []schemaUniqueIndexSpec{
				{origin: "pk", columns: []schemaIndexColumn{binary("id")}},
				{origin: "u", columns: []schemaIndexColumn{binary("thread_id"), binary("ordinal")}},
				{origin: "u", columns: []schemaIndexColumn{binary("attempt_id"), binary("kind")}},
				{origin: "u", columns: []schemaIndexColumn{binary("id"), binary("kind")}},
			},
		},
		{
			name:      "pr_development_ledger_review_findings",
			createSQL: schemaV11PRDevelopmentLedgerFindingsTable,
			uniqueIndexes: []schemaUniqueIndexSpec{
				{origin: "pk", columns: []schemaIndexColumn{binary("entry_id"), binary("ordinal")}},
			},
		},
		{
			name:      "pr_development_ledger_checkpoints",
			createSQL: schemaV11PRDevelopmentLedgerCheckpointsTable,
			uniqueIndexes: []schemaUniqueIndexSpec{
				{origin: "pk", columns: []schemaIndexColumn{binary("id")}},
				{origin: "u", columns: []schemaIndexColumn{binary("thread_id"), binary("generation")}},
				{origin: "u", columns: []schemaIndexColumn{binary("thread_id"), binary("through_ordinal")}},
			},
		},
	} {
		if err := validateSchemaTable(ctx, conn, table); err != nil {
			return err
		}
	}
	return nil
}
