//go:build !mipsle && !netbsd && !(freebsd && arm)

package eventing

import (
	"context"
	"database/sql"
)

const (
	schemaV6PRDevelopmentCasesTable = `CREATE TABLE IF NOT EXISTS pr_development_cases (
	id TEXT PRIMARY KEY,
	event_id TEXT NOT NULL REFERENCES event_inbox(id) ON DELETE RESTRICT,
	dispatch_id TEXT NOT NULL UNIQUE REFERENCES event_dispatches(id) ON DELETE RESTRICT,
	run_id TEXT NOT NULL UNIQUE,
	workflow_ref TEXT NOT NULL CHECK (workflow_ref <> ''),
	workflow_revision TEXT NOT NULL CHECK (workflow_revision <> ''),
	connector TEXT NOT NULL CHECK (connector <> ''),
	repository TEXT NOT NULL COLLATE NOCASE,
	pull_number INTEGER NOT NULL CHECK (pull_number > 0 AND pull_number <= 2147483647),
	pull_url TEXT NOT NULL,
	pull_author TEXT NOT NULL,
	target_user TEXT NOT NULL,
	pull_state TEXT NOT NULL CHECK (pull_state IN ('open', 'closed')),
	pull_draft INTEGER NOT NULL CHECK (pull_draft IN (0, 1)),
	pull_merged INTEGER NOT NULL CHECK (pull_merged IN (0, 1)),
	base_repository TEXT NOT NULL COLLATE NOCASE,
	base_ref TEXT NOT NULL,
	base_sha TEXT NOT NULL,
	head_repository TEXT NOT NULL COLLATE NOCASE,
	head_ref TEXT NOT NULL,
	head_sha TEXT NOT NULL,
	review_id TEXT NOT NULL CHECK (review_id <> ''),
	trigger_review_node_id TEXT NOT NULL CHECK (trigger_review_node_id <> ''),
	review_author TEXT NOT NULL,
	submitted_review_state TEXT NOT NULL CHECK (submitted_review_state IN ('approved', 'changes_requested', 'commented')),
	current_review_state TEXT NOT NULL CHECK (current_review_state IN ('approved', 'changes_requested', 'commented', 'dismissed')),
	review_commit_sha TEXT NOT NULL,
	review_submitted_at INTEGER NOT NULL,
	review_url TEXT NOT NULL,
	feedback TEXT NOT NULL,
	capture_hash TEXT NOT NULL CHECK (length(capture_hash) = 64),
	created_at INTEGER NOT NULL,
	updated_at INTEGER NOT NULL,
	CHECK (lower(repository) = lower(base_repository)),
	CHECK (lower(pull_author) = lower(target_user)),
	CHECK (lower(review_author) <> lower(target_user)),
	CHECK (pull_merged = 0 OR (pull_state = 'closed' AND pull_draft = 0)),
	CHECK (updated_at >= created_at)
);`
	schemaV6PRDevelopmentCasesListIndex = `CREATE INDEX IF NOT EXISTS pr_development_cases_list
	ON pr_development_cases(updated_at DESC, id DESC);`
	schemaV6PRDevelopmentCasesRepositoryIndex = `CREATE INDEX IF NOT EXISTS pr_development_cases_repository
	ON pr_development_cases(repository, pull_number, updated_at DESC, id DESC);`
	schemaV6 = schemaV6PRDevelopmentCasesTable + "\n" +
		schemaV6PRDevelopmentCasesListIndex + "\n" +
		schemaV6PRDevelopmentCasesRepositoryIndex
)

func validateSchemaV6(ctx context.Context, conn *sql.Conn) error {
	binary := func(name string) schemaIndexColumn {
		return schemaIndexColumn{name: name, collation: "BINARY"}
	}
	if err := validateSchemaTable(ctx, conn, schemaTableSpec{
		name:      "pr_development_cases",
		createSQL: schemaV6PRDevelopmentCasesTable,
		uniqueIndexes: []schemaUniqueIndexSpec{
			{origin: "pk", columns: []schemaIndexColumn{binary("id")}},
			{origin: "u", columns: []schemaIndexColumn{binary("dispatch_id")}},
			{origin: "u", columns: []schemaIndexColumn{binary("run_id")}},
		},
	}); err != nil {
		return err
	}
	for _, index := range []schemaIndexSpec{
		{
			name:      "pr_development_cases_list",
			createSQL: schemaV6PRDevelopmentCasesListIndex,
		},
		{
			name:      "pr_development_cases_repository",
			createSQL: schemaV6PRDevelopmentCasesRepositoryIndex,
		},
	} {
		if err := validateSchemaIndex(ctx, conn, index); err != nil {
			return err
		}
	}
	return nil
}
