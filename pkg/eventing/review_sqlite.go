//go:build !mipsle && !netbsd && !(freebsd && arm)

package eventing

import (
	"context"
	"database/sql"
)

const (
	schemaV3ReviewCasesTable = `CREATE TABLE IF NOT EXISTS pr_review_cases (
	id TEXT PRIMARY KEY,
	event_id TEXT NOT NULL REFERENCES event_inbox(id) ON DELETE RESTRICT,
	dispatch_id TEXT NOT NULL UNIQUE REFERENCES event_dispatches(id) ON DELETE RESTRICT,
	run_id TEXT NOT NULL UNIQUE,
	workflow_ref TEXT NOT NULL,
	workflow_revision TEXT NOT NULL,
	connector TEXT NOT NULL,
	repository TEXT NOT NULL COLLATE NOCASE,
	pull_number INTEGER NOT NULL CHECK (pull_number > 0 AND pull_number <= 2147483647),
	pull_url TEXT NOT NULL,
	base_sha TEXT NOT NULL,
	head_sha TEXT NOT NULL,
	draft_schema_version INTEGER NOT NULL CHECK (draft_schema_version = 1),
	summary TEXT NOT NULL,
	tests_json BLOB NOT NULL,
	residual_risks_json BLOB NOT NULL,
	capture_hash TEXT NOT NULL,
	status TEXT NOT NULL CHECK (status IN ('open', 'all_dropped', 'submitting', 'submission_unknown', 'submitted', 'stale')),
	version INTEGER NOT NULL DEFAULT 1 CHECK (version >= 1),
	active_findings INTEGER NOT NULL CHECK (active_findings >= 0),
	total_findings INTEGER NOT NULL CHECK (total_findings >= 0),
	public_error_code TEXT NOT NULL DEFAULT '',
	created_at INTEGER NOT NULL,
	updated_at INTEGER NOT NULL,
	resolved_at INTEGER,
	submitted_at INTEGER,
	CHECK (active_findings <= total_findings)
);`
	schemaV3ReviewCasesListIndex = `CREATE INDEX IF NOT EXISTS pr_review_cases_list
	ON pr_review_cases(updated_at DESC, id DESC);`
	schemaV3ReviewCasesRepositoryIndex = `CREATE INDEX IF NOT EXISTS pr_review_cases_repository
	ON pr_review_cases(repository, pull_number, updated_at DESC, id DESC);`

	schemaV3ReviewFindingsTable = `CREATE TABLE IF NOT EXISTS pr_review_findings (
	id TEXT PRIMARY KEY,
	case_id TEXT NOT NULL REFERENCES pr_review_cases(id) ON DELETE CASCADE,
	ordinal INTEGER NOT NULL CHECK (ordinal >= 0),
	state TEXT NOT NULL CHECK (state IN ('active', 'dropped')),
	severity TEXT NOT NULL CHECK (severity IN ('critical', 'high', 'medium', 'low')),
	title TEXT NOT NULL,
	file TEXT NOT NULL DEFAULT '',
	line INTEGER CHECK (line IS NULL OR (line > 0 AND line <= 2147483647)),
	message TEXT NOT NULL,
	evidence TEXT NOT NULL DEFAULT '',
	impact TEXT NOT NULL DEFAULT '',
	recommendation TEXT NOT NULL DEFAULT '',
	validation TEXT NOT NULL DEFAULT '',
	dropped_reason TEXT NOT NULL DEFAULT '',
	revision INTEGER NOT NULL DEFAULT 1 CHECK (revision >= 1),
	created_at INTEGER NOT NULL,
	updated_at INTEGER NOT NULL,
	dropped_at INTEGER,
	UNIQUE(case_id, ordinal),
	UNIQUE(case_id, id),
	CHECK (line IS NULL OR file <> '')
);`
	schemaV3ReviewFindingsCaseIndex = `CREATE INDEX IF NOT EXISTS pr_review_findings_case
	ON pr_review_findings(case_id, ordinal);`

	schemaV3ReviewMessagesTable = `CREATE TABLE IF NOT EXISTS pr_review_messages (
	id TEXT PRIMARY KEY,
	case_id TEXT NOT NULL REFERENCES pr_review_cases(id) ON DELETE CASCADE,
	ordinal INTEGER NOT NULL CHECK (ordinal >= 0),
	finding_id TEXT,
	kind TEXT NOT NULL CHECK (kind IN ('chat', 'rephrase')),
	role TEXT NOT NULL CHECK (role IN ('user', 'assistant')),
	content TEXT NOT NULL,
	created_at INTEGER NOT NULL,
	FOREIGN KEY(case_id, finding_id)
		REFERENCES pr_review_findings(case_id, id) ON DELETE CASCADE,
	UNIQUE(case_id, ordinal)
);`
	schemaV3ReviewMessagesCaseIndex = `CREATE INDEX IF NOT EXISTS pr_review_messages_case
	ON pr_review_messages(case_id, ordinal);`

	schemaV3ReviewSubmissionsTable = `CREATE TABLE IF NOT EXISTS pr_review_submissions (
	id TEXT PRIMARY KEY,
	case_id TEXT NOT NULL REFERENCES pr_review_cases(id) ON DELETE RESTRICT,
	draft_version INTEGER NOT NULL CHECK (draft_version >= 1),
	marker TEXT NOT NULL UNIQUE,
	status TEXT NOT NULL CHECK (status IN ('pending', 'claimed', 'submitted', 'unknown', 'failed')),
	claim_from TEXT NOT NULL DEFAULT '' CHECK (claim_from IN ('', 'pending')),
	owner TEXT NOT NULL DEFAULT '',
	lease_until INTEGER,
	attempts INTEGER NOT NULL DEFAULT 0 CHECK (attempts >= 0),
	request_json BLOB NOT NULL,
	public_error_code TEXT NOT NULL DEFAULT '',
	internal_error TEXT NOT NULL DEFAULT '',
	external_review_id TEXT NOT NULL DEFAULT '',
	external_url TEXT NOT NULL DEFAULT '',
	created_at INTEGER NOT NULL,
	updated_at INTEGER NOT NULL,
	submitted_at INTEGER,
	UNIQUE(case_id, draft_version)
);`
	schemaV3ReviewSubmissionsClaimIndex = `CREATE INDEX IF NOT EXISTS pr_review_submissions_claim
	ON pr_review_submissions(status, lease_until, created_at, id);`
	schemaV3ReviewSubmissionsCaseIndex = `CREATE INDEX IF NOT EXISTS pr_review_submissions_case
	ON pr_review_submissions(case_id, draft_version DESC, id DESC);`

	schemaV3 = schemaV3ReviewCasesTable + "\n" +
		schemaV3ReviewCasesListIndex + "\n" +
		schemaV3ReviewCasesRepositoryIndex + "\n" +
		schemaV3ReviewFindingsTable + "\n" +
		schemaV3ReviewFindingsCaseIndex + "\n" +
		schemaV3ReviewMessagesTable + "\n" +
		schemaV3ReviewMessagesCaseIndex + "\n" +
		schemaV3ReviewSubmissionsTable + "\n" +
		schemaV3ReviewSubmissionsClaimIndex + "\n" +
		schemaV3ReviewSubmissionsCaseIndex
)

func validateSchemaV3(ctx context.Context, conn *sql.Conn) error {
	for _, table := range schemaV3TableSpecs() {
		if err := validateSchemaTable(ctx, conn, table); err != nil {
			return err
		}
	}
	for _, index := range schemaV3IndexSpecs() {
		if err := validateSchemaIndex(ctx, conn, index); err != nil {
			return err
		}
	}
	return nil
}

func schemaV3TableSpecs() []schemaTableSpec {
	binary := func(name string) schemaIndexColumn {
		return schemaIndexColumn{name: name, collation: "BINARY"}
	}
	return []schemaTableSpec{
		{
			name:      "pr_review_cases",
			createSQL: schemaV3ReviewCasesTable,
			uniqueIndexes: []schemaUniqueIndexSpec{
				{origin: "pk", columns: []schemaIndexColumn{binary("id")}},
				{origin: "u", columns: []schemaIndexColumn{binary("dispatch_id")}},
				{origin: "u", columns: []schemaIndexColumn{binary("run_id")}},
			},
		},
		{
			name:      "pr_review_findings",
			createSQL: schemaV3ReviewFindingsTable,
			uniqueIndexes: []schemaUniqueIndexSpec{
				{origin: "pk", columns: []schemaIndexColumn{binary("id")}},
				{
					origin:  "u",
					columns: []schemaIndexColumn{binary("case_id"), binary("ordinal")},
				},
				{
					origin:  "u",
					columns: []schemaIndexColumn{binary("case_id"), binary("id")},
				},
			},
		},
		{
			name:      "pr_review_messages",
			createSQL: schemaV3ReviewMessagesTable,
			uniqueIndexes: []schemaUniqueIndexSpec{
				{origin: "pk", columns: []schemaIndexColumn{binary("id")}},
				{
					origin:  "u",
					columns: []schemaIndexColumn{binary("case_id"), binary("ordinal")},
				},
			},
		},
		{
			name:      "pr_review_submissions",
			createSQL: schemaV3ReviewSubmissionsTable,
			uniqueIndexes: []schemaUniqueIndexSpec{
				{origin: "pk", columns: []schemaIndexColumn{binary("id")}},
				{origin: "u", columns: []schemaIndexColumn{binary("marker")}},
				{
					origin:  "u",
					columns: []schemaIndexColumn{binary("case_id"), binary("draft_version")},
				},
			},
		},
	}
}

func schemaV3IndexSpecs() []schemaIndexSpec {
	return []schemaIndexSpec{
		{name: "pr_review_cases_list", createSQL: schemaV3ReviewCasesListIndex},
		{name: "pr_review_cases_repository", createSQL: schemaV3ReviewCasesRepositoryIndex},
		{name: "pr_review_findings_case", createSQL: schemaV3ReviewFindingsCaseIndex},
		{name: "pr_review_messages_case", createSQL: schemaV3ReviewMessagesCaseIndex},
		{name: "pr_review_submissions_claim", createSQL: schemaV3ReviewSubmissionsClaimIndex},
		{name: "pr_review_submissions_case", createSQL: schemaV3ReviewSubmissionsCaseIndex},
	}
}
