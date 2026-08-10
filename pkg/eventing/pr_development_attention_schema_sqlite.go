//go:build !mipsle && !netbsd && !(freebsd && arm)

package eventing

import (
	"context"
	"database/sql"
)

const (
	schemaV15PRDevelopmentLedgerEntryAttentionIndex = `CREATE UNIQUE INDEX IF NOT EXISTS pr_development_ledger_entries_attention
	ON pr_development_ledger_entries(id, kind, entry_hash);`
	schemaV15PRDevelopmentAttentionDecisionRunsTable = `CREATE TABLE IF NOT EXISTS pr_development_attention_decision_runs (
	case_id TEXT NOT NULL REFERENCES pr_development_cases(id) ON DELETE RESTRICT,
	review_entry_id TEXT NOT NULL,
	review_entry_kind TEXT NOT NULL DEFAULT 'review' CHECK (review_entry_kind = 'review'),
	review_entry_hash TEXT NOT NULL CHECK (length(review_entry_hash) = 64),
	conversation_version INTEGER NOT NULL CHECK (conversation_version >= 0 AND conversation_version <= 256),
	subject_revision TEXT NOT NULL CHECK (length(subject_revision) = 71),
	decision_point TEXT NOT NULL CHECK (
		length(CAST(decision_point AS BLOB)) >= 1 AND
		length(CAST(decision_point AS BLOB)) <= 128
	),
	policy_revision TEXT NOT NULL CHECK (length(policy_revision) = 71),
	run_id TEXT NOT NULL UNIQUE,
	selected_ordinal INTEGER NOT NULL CHECK (selected_ordinal >= 0 AND selected_ordinal < 8192),
	transcript_digest TEXT NOT NULL CHECK (length(transcript_digest) = 64),
	thread_id TEXT NOT NULL REFERENCES pr_development_threads(id) ON DELETE RESTRICT,
	thread_case_count INTEGER NOT NULL CHECK (thread_case_count >= 1 AND thread_case_count <= 8192),
	thread_cases_digest TEXT NOT NULL CHECK (length(thread_cases_digest) = 64),
	ledger_entry_count INTEGER NOT NULL CHECK (ledger_entry_count >= 2 AND ledger_entry_count <= 16384),
	ledger_entries_digest TEXT NOT NULL CHECK (length(ledger_entries_digest) = 64),
	ledger_checkpoint_count INTEGER NOT NULL CHECK (ledger_checkpoint_count >= 0 AND ledger_checkpoint_count <= 8192),
	ledger_checkpoints_digest TEXT NOT NULL CHECK (length(ledger_checkpoints_digest) = 64),
	review_entry_ordinal INTEGER NOT NULL CHECK (
		review_entry_ordinal >= 1 AND review_entry_ordinal < 16384 AND
		review_entry_ordinal % 2 = 1
	),
	attempt_id TEXT NOT NULL REFERENCES pr_development_repair_attempts(id) ON DELETE RESTRICT,
	attempt_ordinal INTEGER NOT NULL CHECK (attempt_ordinal >= 0 AND attempt_ordinal < 64),
	fence_ordinal INTEGER NOT NULL CHECK (fence_ordinal >= 0 AND fence_ordinal < 8192),
	fence_hash TEXT NOT NULL CHECK (length(fence_hash) = 64),
	controller_id TEXT NOT NULL REFERENCES pr_development_thread_controllers(id) ON DELETE RESTRICT,
	controller_revision INTEGER NOT NULL CHECK (controller_revision >= 1 AND controller_revision <= 65536),
	controller_line_version INTEGER NOT NULL CHECK (controller_line_version >= 1 AND controller_line_version <= 8192),
	controller_fence_count INTEGER NOT NULL CHECK (controller_fence_count >= 1 AND controller_fence_count <= 8192),
	controller_fences_digest TEXT NOT NULL CHECK (length(controller_fences_digest) = 64),
	owner_session_id TEXT NOT NULL REFERENCES pr_development_repair_sessions(id) ON DELETE RESTRICT,
	owner_session_version INTEGER NOT NULL CHECK (owner_session_version >= 1 AND owner_session_version <= 1024),
	owner_attempt_count INTEGER NOT NULL CHECK (owner_attempt_count >= 1 AND owner_attempt_count <= 64),
	created_at INTEGER NOT NULL,
	PRIMARY KEY (
		case_id, review_entry_id, review_entry_hash, conversation_version,
		subject_revision, decision_point, policy_revision
	),
	FOREIGN KEY (review_entry_id, review_entry_kind, review_entry_hash)
		REFERENCES pr_development_ledger_entries(id, kind, entry_hash) ON DELETE RESTRICT,
	CHECK (selected_ordinal < thread_case_count),
	CHECK (review_entry_ordinal = 2 * fence_ordinal + 1),
	CHECK (fence_ordinal + 1 = controller_fence_count),
	CHECK (attempt_ordinal + 1 = owner_attempt_count)
);`
	schemaV15 = schemaV15PRDevelopmentLedgerEntryAttentionIndex + "\n" +
		schemaV15PRDevelopmentAttentionDecisionRunsTable
)

func validateSchemaV15(ctx context.Context, conn *sql.Conn) error {
	binary := func(name string) schemaIndexColumn {
		return schemaIndexColumn{name: name, collation: "BINARY"}
	}
	if err := validateSchemaTable(ctx, conn, schemaTableSpec{
		name:      "pr_development_attention_decision_runs",
		createSQL: schemaV15PRDevelopmentAttentionDecisionRunsTable,
		uniqueIndexes: []schemaUniqueIndexSpec{
			{
				origin: "pk",
				columns: []schemaIndexColumn{
					binary("case_id"),
					binary("review_entry_id"),
					binary("review_entry_hash"),
					binary("conversation_version"),
					binary("subject_revision"),
					binary("decision_point"),
					binary("policy_revision"),
				},
			},
			{origin: "u", columns: []schemaIndexColumn{binary("run_id")}},
		},
	}); err != nil {
		return err
	}
	return validateSchemaIndex(ctx, conn, schemaIndexSpec{
		name:      "pr_development_ledger_entries_attention",
		createSQL: schemaV15PRDevelopmentLedgerEntryAttentionIndex,
	})
}
