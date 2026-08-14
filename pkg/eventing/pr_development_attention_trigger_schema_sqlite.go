//go:build !mipsle && !netbsd && !(freebsd && arm)

package eventing

import (
	"context"
	"database/sql"
)

const (
	schemaV16PRDevelopmentAttentionTriggersTable = `CREATE TABLE IF NOT EXISTS pr_development_attention_triggers (
	review_entry_id TEXT PRIMARY KEY,
	review_entry_kind TEXT NOT NULL DEFAULT 'review' CHECK (review_entry_kind = 'review'),
	review_entry_hash TEXT NOT NULL CHECK (length(review_entry_hash) = 64),
	case_id TEXT NOT NULL REFERENCES pr_development_cases(id) ON DELETE RESTRICT,
	conversation_version INTEGER NOT NULL CHECK (conversation_version >= 0 AND conversation_version <= 256),
	transcript_digest TEXT NOT NULL CHECK (length(transcript_digest) = 64),
	decision_point TEXT NOT NULL CHECK (decision_point = 'pr_development.review_attention_required'),
	status TEXT NOT NULL CHECK (status IN (
		'pending', 'claimed', 'delivered', 'noop', 'superseded',
		'recovery_required', 'failed'
	)),
	owner TEXT NOT NULL DEFAULT '',
	lease_until INTEGER,
	attempts INTEGER NOT NULL DEFAULT 0 CHECK (attempts >= 0),
	available_at INTEGER NOT NULL,
	policy_revision TEXT NOT NULL DEFAULT '',
	pinned_policy_json BLOB NOT NULL DEFAULT X'',
	subject_revision TEXT NOT NULL DEFAULT '',
	run_id TEXT NOT NULL DEFAULT '',
	last_error TEXT NOT NULL DEFAULT '',
	created_at INTEGER NOT NULL,
	updated_at INTEGER NOT NULL,
	completed_at INTEGER,
	FOREIGN KEY (review_entry_id, review_entry_kind, review_entry_hash)
		REFERENCES pr_development_ledger_entries(id, kind, entry_hash) ON DELETE RESTRICT,
	UNIQUE (
		case_id, review_entry_id, review_entry_hash, conversation_version,
		decision_point
	),
	CHECK ((status = 'claimed' AND owner <> '' AND lease_until IS NOT NULL) OR
	       (status <> 'claimed' AND owner = '' AND lease_until IS NULL)),
	CHECK ((status IN ('pending', 'claimed') AND completed_at IS NULL) OR
	       (status NOT IN ('pending', 'claimed') AND completed_at IS NOT NULL)),
	CHECK ((policy_revision = '' AND length(pinned_policy_json) = 0 AND subject_revision = '') OR
	       (length(policy_revision) = 71 AND
	        length(pinned_policy_json) > 0 AND
	        length(pinned_policy_json) <= 3145728 AND
	        (subject_revision = '' OR length(subject_revision) = 71))),
	CHECK ((status = 'delivered' AND run_id <> '' AND subject_revision <> '') OR
	       (status <> 'delivered' AND run_id = '')),
	CHECK (status <> 'noop' OR
	       (policy_revision <> '' AND subject_revision = '' AND run_id = '')),
	CHECK (status <> 'recovery_required' OR
	       (policy_revision <> '' AND subject_revision <> '' AND run_id = '')),
	CHECK (length(CAST(last_error AS BLOB)) <= 16384)
);`
	schemaV16PRDevelopmentAttentionTriggersClaimIndex = `CREATE INDEX IF NOT EXISTS pr_development_attention_triggers_claim
	ON pr_development_attention_triggers(status, available_at, lease_until, created_at, review_entry_id);`
	schemaV16 = schemaV16PRDevelopmentAttentionTriggersTable + "\n" +
		schemaV16PRDevelopmentAttentionTriggersClaimIndex
)

func validateSchemaV16(ctx context.Context, conn *sql.Conn) error {
	binary := func(name string) schemaIndexColumn {
		return schemaIndexColumn{name: name, collation: "BINARY"}
	}
	if err := validateSchemaTable(ctx, conn, schemaTableSpec{
		name:      "pr_development_attention_triggers",
		createSQL: schemaV16PRDevelopmentAttentionTriggersTable,
		uniqueIndexes: []schemaUniqueIndexSpec{
			{origin: "pk", columns: []schemaIndexColumn{binary("review_entry_id")}},
			{
				origin: "u",
				columns: []schemaIndexColumn{
					binary("case_id"),
					binary("review_entry_id"),
					binary("review_entry_hash"),
					binary("conversation_version"),
					binary("decision_point"),
				},
			},
		},
	}); err != nil {
		return err
	}
	return validateSchemaIndex(ctx, conn, schemaIndexSpec{
		name:      "pr_development_attention_triggers_claim",
		createSQL: schemaV16PRDevelopmentAttentionTriggersClaimIndex,
	})
}
