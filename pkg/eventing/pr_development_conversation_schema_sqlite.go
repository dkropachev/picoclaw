//go:build !mipsle && !netbsd && !(freebsd && arm)

package eventing

import (
	"context"
	"database/sql"
)

const (
	schemaV7PRDevelopmentConversationsTable = `CREATE TABLE IF NOT EXISTS pr_development_conversations (
	case_id TEXT PRIMARY KEY REFERENCES pr_development_cases(id) ON DELETE CASCADE,
	version INTEGER NOT NULL CHECK (version >= 0 AND version <= 256),
	content_bytes INTEGER NOT NULL CHECK (content_bytes >= 0 AND content_bytes <= 4194304),
	transcript_digest TEXT NOT NULL CHECK (length(transcript_digest) = 64),
	CHECK (
		(version = 0 AND content_bytes = 0) OR
		(version > 0 AND content_bytes > 0)
	)
);`
	schemaV7PRDevelopmentMessagesTable = `CREATE TABLE IF NOT EXISTS pr_development_messages (
	id TEXT PRIMARY KEY,
	case_id TEXT NOT NULL REFERENCES pr_development_conversations(case_id) ON DELETE CASCADE,
	ordinal INTEGER NOT NULL CHECK (ordinal >= 0),
	role TEXT NOT NULL CHECK (role IN ('user', 'assistant')),
	content TEXT NOT NULL CHECK (
		length(CAST(content AS BLOB)) > 0 AND
		length(CAST(content AS BLOB)) <= 65536
	),
	created_at INTEGER NOT NULL,
	UNIQUE(case_id, ordinal)
);`
	schemaV7 = schemaV7PRDevelopmentConversationsTable + "\n" +
		schemaV7PRDevelopmentMessagesTable
)

func validateSchemaV7(ctx context.Context, conn *sql.Conn) error {
	binary := func(name string) schemaIndexColumn {
		return schemaIndexColumn{name: name, collation: "BINARY"}
	}
	if err := validateSchemaTable(ctx, conn, schemaTableSpec{
		name:      "pr_development_conversations",
		createSQL: schemaV7PRDevelopmentConversationsTable,
		uniqueIndexes: []schemaUniqueIndexSpec{
			{origin: "pk", columns: []schemaIndexColumn{binary("case_id")}},
		},
	}); err != nil {
		return err
	}
	return validateSchemaTable(ctx, conn, schemaTableSpec{
		name:      "pr_development_messages",
		createSQL: schemaV7PRDevelopmentMessagesTable,
		uniqueIndexes: []schemaUniqueIndexSpec{
			{origin: "pk", columns: []schemaIndexColumn{binary("id")}},
			{
				origin: "u",
				columns: []schemaIndexColumn{
					binary("case_id"),
					binary("ordinal"),
				},
			},
		},
	})
}
