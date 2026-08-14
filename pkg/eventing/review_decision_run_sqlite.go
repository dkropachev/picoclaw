//go:build !mipsle && !netbsd && !(freebsd && arm)

package eventing

import (
	"context"
	"database/sql"
)

const (
	schemaV4ReviewDecisionRunsTable = `CREATE TABLE IF NOT EXISTS pr_review_decision_runs (
	case_id TEXT NOT NULL REFERENCES pr_review_cases(id) ON DELETE RESTRICT,
	case_version INTEGER NOT NULL CHECK (case_version >= 1),
	decision_point TEXT NOT NULL CHECK (decision_point <> ''),
	policy_revision TEXT NOT NULL CHECK (policy_revision <> ''),
	run_id TEXT NOT NULL UNIQUE,
	created_at INTEGER NOT NULL,
	PRIMARY KEY(case_id, case_version, decision_point, policy_revision)
);`
	schemaV4 = schemaV4ReviewDecisionRunsTable
)

func validateSchemaV4(ctx context.Context, conn *sql.Conn) error {
	binary := func(name string) schemaIndexColumn {
		return schemaIndexColumn{name: name, collation: "BINARY"}
	}
	return validateSchemaTable(ctx, conn, schemaTableSpec{
		name:      "pr_review_decision_runs",
		createSQL: schemaV4ReviewDecisionRunsTable,
		uniqueIndexes: []schemaUniqueIndexSpec{
			{
				origin: "pk",
				columns: []schemaIndexColumn{
					binary("case_id"),
					binary("case_version"),
					binary("decision_point"),
					binary("policy_revision"),
				},
			},
			{origin: "u", columns: []schemaIndexColumn{binary("run_id")}},
		},
	})
}
