//go:build !mipsle && !netbsd && !(freebsd && arm)

package eventing

import (
	"context"
	"database/sql"
)

const (
	schemaV5ReviewAttentionTriggersTable = `CREATE TABLE IF NOT EXISTS pr_review_attention_triggers (
	submission_id TEXT PRIMARY KEY REFERENCES pr_review_submissions(id) ON DELETE RESTRICT,
	case_id TEXT NOT NULL REFERENCES pr_review_cases(id) ON DELETE RESTRICT,
	case_version INTEGER NOT NULL CHECK (case_version >= 1),
	decision_point TEXT NOT NULL CHECK (decision_point <> ''),
	status TEXT NOT NULL CHECK (status IN ('pending', 'claimed', 'delivered', 'noop')),
	owner TEXT NOT NULL DEFAULT '',
	lease_until INTEGER,
	attempts INTEGER NOT NULL DEFAULT 0 CHECK (attempts >= 0),
	available_at INTEGER NOT NULL,
	policy_revision TEXT NOT NULL DEFAULT '',
	pinned_policy_json BLOB NOT NULL DEFAULT X'',
	run_id TEXT NOT NULL DEFAULT '',
	last_error TEXT NOT NULL DEFAULT '',
	created_at INTEGER NOT NULL,
	updated_at INTEGER NOT NULL,
	completed_at INTEGER,
	UNIQUE(case_id, case_version, decision_point),
	CHECK ((policy_revision = '' AND length(pinned_policy_json) = 0) OR
	       (policy_revision <> '' AND length(pinned_policy_json) > 0)),
	CHECK (typeof(pinned_policy_json) = 'blob' AND length(pinned_policy_json) <= 3145728),
	CHECK (typeof(last_error) = 'text' AND length(CAST(last_error AS BLOB)) <= 16384),
	CHECK ((status = 'claimed' AND owner <> '' AND lease_until IS NOT NULL) OR
	       (status <> 'claimed' AND owner = '' AND lease_until IS NULL)),
	CHECK ((status IN ('delivered', 'noop') AND completed_at IS NOT NULL) OR
	       (status IN ('pending', 'claimed') AND completed_at IS NULL)),
	CHECK (status IN ('pending', 'claimed') OR policy_revision <> ''),
	CHECK ((status = 'delivered' AND run_id <> '') OR
	       (status <> 'delivered' AND run_id = ''))
);`
	schemaV5ReviewAttentionTriggersClaimIndex = `CREATE INDEX IF NOT EXISTS pr_review_attention_triggers_claim
	ON pr_review_attention_triggers(status, available_at, lease_until, created_at, submission_id);`
	schemaV5 = schemaV5ReviewAttentionTriggersTable + "\n" +
		schemaV5ReviewAttentionTriggersClaimIndex
)

func validateSchemaV5(ctx context.Context, conn *sql.Conn) error {
	binary := func(name string) schemaIndexColumn {
		return schemaIndexColumn{name: name, collation: "BINARY"}
	}
	if err := validateSchemaTable(ctx, conn, schemaTableSpec{
		name:      "pr_review_attention_triggers",
		createSQL: schemaV5ReviewAttentionTriggersTable,
		uniqueIndexes: []schemaUniqueIndexSpec{
			{origin: "pk", columns: []schemaIndexColumn{binary("submission_id")}},
			{
				origin: "u",
				columns: []schemaIndexColumn{
					binary("case_id"),
					binary("case_version"),
					binary("decision_point"),
				},
			},
		},
	}); err != nil {
		return err
	}
	return validateSchemaIndex(ctx, conn, schemaIndexSpec{
		name:      "pr_review_attention_triggers_claim",
		createSQL: schemaV5ReviewAttentionTriggersClaimIndex,
	})
}
