//go:build !mipsle && !netbsd && !(freebsd && arm)

package eventing

import (
	"context"
	"database/sql"
)

const (
	schemaV9PRDevelopmentThreadsTable = `CREATE TABLE IF NOT EXISTS pr_development_threads (
	id TEXT PRIMARY KEY,
	identity_kind TEXT NOT NULL CHECK (identity_kind IN ('provider', 'legacy')),
	provider TEXT CHECK (provider IS NULL OR provider = 'github'),
	provider_origin TEXT CHECK (
		provider_origin IS NULL OR (
			length(CAST(provider_origin AS BLOB)) >= 1 AND
			length(CAST(provider_origin AS BLOB)) <= 4096
		)
	),
	pull_author_id TEXT CHECK (
		pull_author_id IS NULL OR (
			length(CAST(pull_author_id AS BLOB)) >= 1 AND
			length(CAST(pull_author_id AS BLOB)) <= 19
		)
	),
	repository_id TEXT CHECK (
		repository_id IS NULL OR (
			length(CAST(repository_id AS BLOB)) >= 1 AND
			length(CAST(repository_id AS BLOB)) <= 19
		)
	),
	pull_request_id TEXT CHECK (
		pull_request_id IS NULL OR (
			length(CAST(pull_request_id AS BLOB)) >= 1 AND
			length(CAST(pull_request_id AS BLOB)) <= 19
		)
	),
	pull_number INTEGER CHECK (
		pull_number IS NULL OR
		(pull_number > 0 AND pull_number <= 2147483647)
	),
	legacy_case_id TEXT UNIQUE REFERENCES pr_development_cases(id) ON DELETE RESTRICT,
	case_count INTEGER NOT NULL CHECK (case_count >= 1 AND case_count <= 8192),
	identity_hash TEXT NOT NULL CHECK (length(identity_hash) = 64),
	cases_digest TEXT NOT NULL CHECK (length(cases_digest) = 64),
	created_at INTEGER NOT NULL,
	updated_at INTEGER NOT NULL,
	UNIQUE(provider, provider_origin, pull_request_id),
	UNIQUE(provider, provider_origin, repository_id, pull_number),
	CHECK (
		(identity_kind = 'provider' AND provider = 'github' AND
		 provider_origin IS NOT NULL AND pull_author_id IS NOT NULL AND
		 repository_id IS NOT NULL AND pull_request_id IS NOT NULL AND
		 pull_number IS NOT NULL AND legacy_case_id IS NULL) OR
		(identity_kind = 'legacy' AND provider IS NULL AND
		 provider_origin IS NULL AND pull_author_id IS NULL AND
		 repository_id IS NULL AND pull_request_id IS NULL AND
		 pull_number IS NULL AND legacy_case_id IS NOT NULL AND
		 case_count = 1)
	),
	CHECK (updated_at >= created_at)
);`
	schemaV9PRDevelopmentThreadCasesTable = `CREATE TABLE IF NOT EXISTS pr_development_thread_cases (
	case_id TEXT PRIMARY KEY REFERENCES pr_development_cases(id) ON DELETE RESTRICT,
	thread_id TEXT NOT NULL REFERENCES pr_development_threads(id) ON DELETE RESTRICT,
	ordinal INTEGER NOT NULL CHECK (ordinal >= 0 AND ordinal < 8192),
	linked_at INTEGER NOT NULL,
	previous_hash TEXT NOT NULL CHECK (length(previous_hash) = 64),
	link_hash TEXT NOT NULL CHECK (length(link_hash) = 64),
	UNIQUE(thread_id, ordinal)
);`
	schemaV9 = schemaV9PRDevelopmentThreadsTable + "\n" +
		schemaV9PRDevelopmentThreadCasesTable
)

func validateSchemaV9(ctx context.Context, conn *sql.Conn) error {
	binary := func(name string) schemaIndexColumn {
		return schemaIndexColumn{name: name, collation: "BINARY"}
	}
	if err := validateSchemaTable(ctx, conn, schemaTableSpec{
		name:      "pr_development_threads",
		createSQL: schemaV9PRDevelopmentThreadsTable,
		uniqueIndexes: []schemaUniqueIndexSpec{
			{origin: "pk", columns: []schemaIndexColumn{binary("id")}},
			{origin: "u", columns: []schemaIndexColumn{binary("legacy_case_id")}},
			{
				origin: "u",
				columns: []schemaIndexColumn{
					binary("provider"), binary("provider_origin"),
					binary("pull_request_id"),
				},
			},
			{
				origin: "u",
				columns: []schemaIndexColumn{
					binary("provider"), binary("provider_origin"),
					binary("repository_id"), binary("pull_number"),
				},
			},
		},
	}); err != nil {
		return err
	}
	return validateSchemaTable(ctx, conn, schemaTableSpec{
		name:      "pr_development_thread_cases",
		createSQL: schemaV9PRDevelopmentThreadCasesTable,
		uniqueIndexes: []schemaUniqueIndexSpec{
			{origin: "pk", columns: []schemaIndexColumn{binary("case_id")}},
			{
				origin:  "u",
				columns: []schemaIndexColumn{binary("thread_id"), binary("ordinal")},
			},
		},
	})
}
