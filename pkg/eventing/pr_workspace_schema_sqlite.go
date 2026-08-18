//go:build !mipsle && !netbsd && !(freebsd && arm)

package eventing

import (
	"context"
	"database/sql"
)

const (
	schemaV19PRWorkspacesTable = `CREATE TABLE IF NOT EXISTS pr_workspaces (
	id TEXT PRIMARY KEY,
	provider TEXT NOT NULL CHECK (provider <> ''),
	provider_origin TEXT NOT NULL CHECK (provider_origin <> ''),
	repository_id TEXT NOT NULL CHECK (repository_id <> ''),
	repository TEXT NOT NULL COLLATE NOCASE CHECK (repository <> ''),
	pull_request_id TEXT NOT NULL CHECK (pull_request_id <> ''),
	pull_number INTEGER NOT NULL CHECK (pull_number > 0 AND pull_number <= 2147483647),
	provider_head_sha TEXT NOT NULL CHECK (provider_head_sha <> ''),
	owned INTEGER NOT NULL CHECK (owned IN (0, 1)),
	head_writable INTEGER NOT NULL CHECK (head_writable IN (0, 1)),
	phase TEXT NOT NULL CHECK (phase IN ('intake', 'charter', 'review', 'triage', 'implementation', 'validation', 'completion_audit', 'publication', 'complete')),
	execution_state TEXT NOT NULL CHECK (execution_state IN ('queued', 'running', 'waiting_gate', 'waiting_user', 'succeeded', 'failed', 'blocked', 'canceled', 'stale', 'unknown')),
	current_provider_ordinal INTEGER NOT NULL CHECK (current_provider_ordinal > 0),
	active_charter_id TEXT NOT NULL DEFAULT '',
	version INTEGER NOT NULL CHECK (version > 0),
	created_at INTEGER NOT NULL,
	updated_at INTEGER NOT NULL,
	UNIQUE (provider, provider_origin, repository_id, pull_request_id),
	CHECK (updated_at >= created_at)
);`
	schemaV19PRWorkspacesListIndex = `CREATE INDEX IF NOT EXISTS pr_workspaces_list
	ON pr_workspaces(updated_at DESC, id DESC);`
	schemaV19PRWorkspacesRepositoryIndex = `CREATE INDEX IF NOT EXISTS pr_workspaces_repository
	ON pr_workspaces(provider_origin, repository_id, updated_at DESC, id DESC);`
	schemaV19PRWorkspacesStateIndex = `CREATE INDEX IF NOT EXISTS pr_workspaces_state
	ON pr_workspaces(phase, execution_state, updated_at DESC, id DESC);`

	schemaV19PRProviderSnapshotsTable = `CREATE TABLE IF NOT EXISTS pr_provider_snapshots (
	id TEXT PRIMARY KEY,
	workspace_id TEXT NOT NULL REFERENCES pr_workspaces(id) ON DELETE CASCADE,
	ordinal INTEGER NOT NULL CHECK (ordinal > 0),
	status TEXT NOT NULL CHECK (status = 'observed'),
	payload_json BLOB NOT NULL CHECK (length(payload_json) >= 2 AND length(payload_json) <= 1048576),
	created_at INTEGER NOT NULL,
	updated_at INTEGER NOT NULL,
	UNIQUE (workspace_id, ordinal),
	CHECK (updated_at >= created_at)
);`
	schemaV19PRProviderSnapshotsListIndex = `CREATE INDEX IF NOT EXISTS pr_provider_snapshots_list
	ON pr_provider_snapshots(workspace_id, status, updated_at DESC, id DESC);`

	schemaV19PRCharterRevisionsTable = `CREATE TABLE IF NOT EXISTS pr_charter_revisions (
	id TEXT PRIMARY KEY,
	workspace_id TEXT NOT NULL REFERENCES pr_workspaces(id) ON DELETE CASCADE,
	ordinal INTEGER NOT NULL CHECK (ordinal > 0),
	status TEXT NOT NULL CHECK (status IN ('draft', 'confirmed', 'superseded')),
	payload_json BLOB NOT NULL CHECK (length(payload_json) >= 2 AND length(payload_json) <= 1048576),
	created_at INTEGER NOT NULL,
	updated_at INTEGER NOT NULL,
	UNIQUE (workspace_id, ordinal),
	CHECK (updated_at >= created_at)
);`
	schemaV19PRCharterRevisionsListIndex = `CREATE INDEX IF NOT EXISTS pr_charter_revisions_list
	ON pr_charter_revisions(workspace_id, status, updated_at DESC, id DESC);`

	schemaV19PRStageRunsTable = `CREATE TABLE IF NOT EXISTS pr_stage_runs (
	id TEXT PRIMARY KEY,
	workspace_id TEXT NOT NULL REFERENCES pr_workspaces(id) ON DELETE CASCADE,
	ordinal INTEGER NOT NULL CHECK (ordinal > 0),
	status TEXT NOT NULL CHECK (status IN ('queued', 'running', 'waiting_gate', 'waiting_user', 'succeeded', 'failed', 'blocked', 'canceled', 'stale', 'unknown')),
	payload_json BLOB NOT NULL CHECK (length(payload_json) >= 2 AND length(payload_json) <= 1048576),
	created_at INTEGER NOT NULL,
	updated_at INTEGER NOT NULL,
	UNIQUE (workspace_id, ordinal),
	CHECK (updated_at >= created_at)
);`
	schemaV19PRStageRunsListIndex = `CREATE INDEX IF NOT EXISTS pr_stage_runs_list
	ON pr_stage_runs(workspace_id, status, updated_at DESC, id DESC);`

	schemaV19PRFindingsTable = `CREATE TABLE IF NOT EXISTS pr_findings (
	id TEXT PRIMARY KEY,
	workspace_id TEXT NOT NULL REFERENCES pr_workspaces(id) ON DELETE CASCADE,
	ordinal INTEGER NOT NULL CHECK (ordinal > 0),
	status TEXT NOT NULL CHECK (status IN ('open', 'in_scope', 'fixed', 'deferred', 'dismissed')),
	payload_json BLOB NOT NULL CHECK (length(payload_json) >= 2 AND length(payload_json) <= 1048576),
	created_at INTEGER NOT NULL,
	updated_at INTEGER NOT NULL,
	UNIQUE (workspace_id, ordinal),
	CHECK (updated_at >= created_at)
);`
	schemaV19PRFindingsListIndex = `CREATE INDEX IF NOT EXISTS pr_findings_list
	ON pr_findings(workspace_id, status, updated_at DESC, id DESC);`

	schemaV19PRFindingEventsTable = `CREATE TABLE IF NOT EXISTS pr_finding_events (
	id TEXT PRIMARY KEY,
	workspace_id TEXT NOT NULL REFERENCES pr_workspaces(id) ON DELETE CASCADE,
	ordinal INTEGER NOT NULL CHECK (ordinal > 0),
	status TEXT NOT NULL CHECK (status <> ''),
	payload_json BLOB NOT NULL CHECK (length(payload_json) >= 2 AND length(payload_json) <= 1048576),
	created_at INTEGER NOT NULL,
	updated_at INTEGER NOT NULL,
	UNIQUE (workspace_id, ordinal),
	CHECK (updated_at >= created_at)
);`
	schemaV19PRFindingEventsListIndex = `CREATE INDEX IF NOT EXISTS pr_finding_events_list
	ON pr_finding_events(workspace_id, status, updated_at DESC, id DESC);`

	schemaV19PRConversationsTable = `CREATE TABLE IF NOT EXISTS pr_conversations (
	id TEXT PRIMARY KEY,
	workspace_id TEXT NOT NULL REFERENCES pr_workspaces(id) ON DELETE CASCADE,
	ordinal INTEGER NOT NULL CHECK (ordinal > 0),
	status TEXT NOT NULL CHECK (status IN ('active', 'resolved', 'superseded')),
	payload_json BLOB NOT NULL CHECK (length(payload_json) >= 2 AND length(payload_json) <= 1048576),
	created_at INTEGER NOT NULL,
	updated_at INTEGER NOT NULL,
	UNIQUE (workspace_id, ordinal),
	CHECK (updated_at >= created_at)
);`
	schemaV19PRConversationsListIndex = `CREATE INDEX IF NOT EXISTS pr_conversations_list
	ON pr_conversations(workspace_id, status, updated_at DESC, id DESC);`

	schemaV19PRMessagesTable = `CREATE TABLE IF NOT EXISTS pr_messages (
	id TEXT PRIMARY KEY,
	workspace_id TEXT NOT NULL REFERENCES pr_workspaces(id) ON DELETE CASCADE,
	ordinal INTEGER NOT NULL CHECK (ordinal > 0),
	status TEXT NOT NULL CHECK (status IN ('user', 'assistant', 'system')),
	payload_json BLOB NOT NULL CHECK (length(payload_json) >= 2 AND length(payload_json) <= 1048576),
	created_at INTEGER NOT NULL,
	updated_at INTEGER NOT NULL,
	UNIQUE (workspace_id, ordinal),
	CHECK (updated_at >= created_at)
);`
	schemaV19PRMessagesListIndex = `CREATE INDEX IF NOT EXISTS pr_messages_list
	ON pr_messages(workspace_id, status, updated_at DESC, id DESC);`

	schemaV19PRCorrectionsTable = `CREATE TABLE IF NOT EXISTS pr_corrections (
	id TEXT PRIMARY KEY,
	workspace_id TEXT NOT NULL REFERENCES pr_workspaces(id) ON DELETE CASCADE,
	ordinal INTEGER NOT NULL CHECK (ordinal > 0),
	status TEXT NOT NULL CHECK (status IN ('active', 'superseded', 'revoked')),
	payload_json BLOB NOT NULL CHECK (length(payload_json) >= 2 AND length(payload_json) <= 1048576),
	created_at INTEGER NOT NULL,
	updated_at INTEGER NOT NULL,
	UNIQUE (workspace_id, ordinal),
	CHECK (updated_at >= created_at)
);`
	schemaV19PRCorrectionsListIndex = `CREATE INDEX IF NOT EXISTS pr_corrections_list
	ON pr_corrections(workspace_id, status, updated_at DESC, id DESC);`

	schemaV19PRRepositoryLessonsTable = `CREATE TABLE IF NOT EXISTS pr_repository_lessons (
	id TEXT PRIMARY KEY,
	workspace_id TEXT NOT NULL REFERENCES pr_workspaces(id) ON DELETE RESTRICT,
	ordinal INTEGER NOT NULL CHECK (ordinal > 0),
	status TEXT NOT NULL CHECK (status IN ('active', 'revoked')),
	payload_json BLOB NOT NULL CHECK (length(payload_json) >= 2 AND length(payload_json) <= 1048576),
	created_at INTEGER NOT NULL,
	updated_at INTEGER NOT NULL,
	UNIQUE (workspace_id, ordinal),
	CHECK (updated_at >= created_at)
);`
	schemaV19PRRepositoryLessonsListIndex = `CREATE INDEX IF NOT EXISTS pr_repository_lessons_list
	ON pr_repository_lessons(workspace_id, status, updated_at DESC, id DESC);`

	schemaV19PRNudgeRoundsTable = `CREATE TABLE IF NOT EXISTS pr_nudge_rounds (
	id TEXT PRIMARY KEY,
	workspace_id TEXT NOT NULL REFERENCES pr_workspaces(id) ON DELETE CASCADE,
	ordinal INTEGER NOT NULL CHECK (ordinal > 0),
	status TEXT NOT NULL CHECK (status IN ('queued', 'running', 'waiting_gate', 'waiting_user', 'succeeded', 'failed', 'blocked', 'canceled', 'stale', 'unknown')),
	payload_json BLOB NOT NULL CHECK (length(payload_json) >= 2 AND length(payload_json) <= 1048576),
	created_at INTEGER NOT NULL,
	updated_at INTEGER NOT NULL,
	UNIQUE (workspace_id, ordinal),
	CHECK (updated_at >= created_at)
);`
	schemaV19PRNudgeRoundsListIndex = `CREATE INDEX IF NOT EXISTS pr_nudge_rounds_list
	ON pr_nudge_rounds(workspace_id, status, updated_at DESC, id DESC);`

	schemaV19PRNudgeRewardsTable = `CREATE TABLE IF NOT EXISTS pr_nudge_rewards (
	id TEXT PRIMARY KEY,
	workspace_id TEXT NOT NULL REFERENCES pr_workspaces(id) ON DELETE CASCADE,
	ordinal INTEGER NOT NULL CHECK (ordinal > 0),
	status TEXT NOT NULL CHECK (status = 'resolved'),
	payload_json BLOB NOT NULL CHECK (length(payload_json) >= 2 AND length(payload_json) <= 1048576),
	created_at INTEGER NOT NULL,
	updated_at INTEGER NOT NULL,
	UNIQUE (workspace_id, ordinal),
	CHECK (updated_at >= created_at)
);`
	schemaV19PRNudgeRewardsListIndex = `CREATE INDEX IF NOT EXISTS pr_nudge_rewards_list
	ON pr_nudge_rewards(workspace_id, status, updated_at DESC, id DESC);`

	schemaV19PRDeferredGroupsTable = `CREATE TABLE IF NOT EXISTS pr_deferred_groups (
	id TEXT PRIMARY KEY,
	workspace_id TEXT NOT NULL REFERENCES pr_workspaces(id) ON DELETE CASCADE,
	ordinal INTEGER NOT NULL CHECK (ordinal > 0),
	status TEXT NOT NULL CHECK (status IN ('draft', 'active', 'resolved', 'dismissed')),
	payload_json BLOB NOT NULL CHECK (length(payload_json) >= 2 AND length(payload_json) <= 1048576),
	created_at INTEGER NOT NULL,
	updated_at INTEGER NOT NULL,
	UNIQUE (workspace_id, ordinal),
	CHECK (updated_at >= created_at)
);`
	schemaV19PRDeferredGroupsListIndex = `CREATE INDEX IF NOT EXISTS pr_deferred_groups_list
	ON pr_deferred_groups(workspace_id, status, updated_at DESC, id DESC);`

	schemaV19PRDeferredGroupItemsTable = `CREATE TABLE IF NOT EXISTS pr_deferred_group_items (
	id TEXT PRIMARY KEY,
	workspace_id TEXT NOT NULL REFERENCES pr_workspaces(id) ON DELETE CASCADE,
	ordinal INTEGER NOT NULL CHECK (ordinal > 0),
	status TEXT NOT NULL CHECK (status IN ('active', 'removed')),
	group_id TEXT NOT NULL,
	finding_id TEXT NOT NULL CHECK (finding_id <> ''),
	ordinal_in_group INTEGER NOT NULL,
	payload_json BLOB NOT NULL CHECK (length(payload_json) >= 2 AND length(payload_json) <= 1048576),
	created_at INTEGER NOT NULL,
	updated_at INTEGER NOT NULL,
	UNIQUE (workspace_id, ordinal),
	UNIQUE (workspace_id, finding_id),
	CHECK ((status = 'active' AND group_id <> '' AND ordinal_in_group >= 0) OR
	       (status = 'removed' AND group_id = '' AND ordinal_in_group = -1)),
	CHECK (updated_at >= created_at)
);`
	schemaV19PRDeferredGroupItemsListIndex = `CREATE INDEX IF NOT EXISTS pr_deferred_group_items_list
	ON pr_deferred_group_items(workspace_id, status, updated_at DESC, id DESC);`
	schemaV19PRDeferredGroupItemsActivePositionIndex = `CREATE UNIQUE INDEX IF NOT EXISTS pr_deferred_group_items_active_position
	ON pr_deferred_group_items(workspace_id, group_id, ordinal_in_group) WHERE status = 'active';`

	schemaV19PRRepairAttemptsTable = `CREATE TABLE IF NOT EXISTS pr_repair_attempts (
	id TEXT PRIMARY KEY,
	workspace_id TEXT NOT NULL REFERENCES pr_workspaces(id) ON DELETE CASCADE,
	ordinal INTEGER NOT NULL CHECK (ordinal > 0),
	status TEXT NOT NULL CHECK (status IN ('queued', 'running', 'waiting_gate', 'waiting_user', 'succeeded', 'failed', 'blocked', 'canceled', 'stale', 'unknown')),
	payload_json BLOB NOT NULL CHECK (length(payload_json) >= 2 AND length(payload_json) <= 1048576),
	created_at INTEGER NOT NULL,
	updated_at INTEGER NOT NULL,
	UNIQUE (workspace_id, ordinal),
	CHECK (updated_at >= created_at)
);`
	schemaV19PRRepairAttemptsListIndex = `CREATE INDEX IF NOT EXISTS pr_repair_attempts_list
	ON pr_repair_attempts(workspace_id, status, updated_at DESC, id DESC);`

	schemaV19PRValidationRunsTable = `CREATE TABLE IF NOT EXISTS pr_validation_runs (
	id TEXT PRIMARY KEY,
	workspace_id TEXT NOT NULL REFERENCES pr_workspaces(id) ON DELETE CASCADE,
	ordinal INTEGER NOT NULL CHECK (ordinal > 0),
	status TEXT NOT NULL CHECK (status IN ('queued', 'running', 'waiting_gate', 'waiting_user', 'succeeded', 'failed', 'blocked', 'canceled', 'stale', 'unknown')),
	payload_json BLOB NOT NULL CHECK (length(payload_json) >= 2 AND length(payload_json) <= 1048576),
	created_at INTEGER NOT NULL,
	updated_at INTEGER NOT NULL,
	UNIQUE (workspace_id, ordinal),
	CHECK (updated_at >= created_at)
);`
	schemaV19PRValidationRunsListIndex = `CREATE INDEX IF NOT EXISTS pr_validation_runs_list
	ON pr_validation_runs(workspace_id, status, updated_at DESC, id DESC);`

	schemaV19PRGateRunsTable = `CREATE TABLE IF NOT EXISTS pr_gate_runs (
	id TEXT PRIMARY KEY,
	workspace_id TEXT NOT NULL REFERENCES pr_workspaces(id) ON DELETE CASCADE,
	ordinal INTEGER NOT NULL CHECK (ordinal > 0),
	status TEXT NOT NULL CHECK (status IN ('queued', 'running', 'waiting_gate', 'waiting_user', 'succeeded', 'failed', 'blocked', 'canceled', 'stale', 'unknown')),
	payload_json BLOB NOT NULL CHECK (length(payload_json) >= 2 AND length(payload_json) <= 1048576),
	created_at INTEGER NOT NULL,
	updated_at INTEGER NOT NULL,
	UNIQUE (workspace_id, ordinal),
	CHECK (updated_at >= created_at)
);`
	schemaV19PRGateRunsListIndex = `CREATE INDEX IF NOT EXISTS pr_gate_runs_list
	ON pr_gate_runs(workspace_id, status, updated_at DESC, id DESC);`

	schemaV19PRPublicationsTable = `CREATE TABLE IF NOT EXISTS pr_publications (
	id TEXT PRIMARY KEY,
	workspace_id TEXT NOT NULL REFERENCES pr_workspaces(id) ON DELETE CASCADE,
	ordinal INTEGER NOT NULL CHECK (ordinal > 0),
	status TEXT NOT NULL CHECK (status IN ('pending', 'claimed', 'published', 'unknown', 'failed')),
	available_at INTEGER NOT NULL,
	lease_owner TEXT NOT NULL DEFAULT '',
	lease_token TEXT NOT NULL DEFAULT '',
	lease_until INTEGER,
	attempts INTEGER NOT NULL CHECK (attempts >= 0),
	payload_json BLOB NOT NULL CHECK (length(payload_json) >= 2 AND length(payload_json) <= 1048576),
	created_at INTEGER NOT NULL,
	updated_at INTEGER NOT NULL,
	UNIQUE (workspace_id, ordinal),
	CHECK (updated_at >= created_at)
);`
	schemaV19PRPublicationsListIndex = `CREATE INDEX IF NOT EXISTS pr_publications_list
	ON pr_publications(workspace_id, status, updated_at DESC, id DESC);`
	schemaV19PRPublicationsClaimIndex = `CREATE INDEX IF NOT EXISTS pr_publications_claim
	ON pr_publications(status, available_at ASC, lease_until ASC, created_at ASC, id ASC);`

	schemaV19PROperationIntentsTable = `CREATE TABLE IF NOT EXISTS pr_operation_intents (
	id TEXT PRIMARY KEY,
	workspace_id TEXT NOT NULL REFERENCES pr_workspaces(id) ON DELETE CASCADE,
	ordinal INTEGER NOT NULL CHECK (ordinal > 0),
	status TEXT NOT NULL CHECK (status IN ('queued', 'running', 'waiting_gate', 'waiting_user', 'succeeded', 'failed', 'blocked', 'canceled', 'stale', 'unknown')),
	available_at INTEGER NOT NULL,
	lease_owner TEXT NOT NULL DEFAULT '',
	lease_token TEXT NOT NULL DEFAULT '',
	lease_until INTEGER,
	attempts INTEGER NOT NULL CHECK (attempts >= 0),
	payload_json BLOB NOT NULL CHECK (length(payload_json) >= 2 AND length(payload_json) <= 1048576),
	created_at INTEGER NOT NULL,
	updated_at INTEGER NOT NULL,
	UNIQUE (workspace_id, ordinal),
	CHECK (updated_at >= created_at)
);`
	schemaV19PROperationIntentsListIndex = `CREATE INDEX IF NOT EXISTS pr_operation_intents_list
	ON pr_operation_intents(workspace_id, status, updated_at DESC, id DESC);`
	schemaV19PROperationIntentsClaimIndex = `CREATE INDEX IF NOT EXISTS pr_operation_intents_claim
	ON pr_operation_intents(status, available_at ASC, lease_until ASC, created_at ASC, id ASC);`

	schemaV19PRIngressWatermarksTable = `CREATE TABLE IF NOT EXISTS pr_ingress_watermarks (
	id TEXT PRIMARY KEY,
	workspace_id TEXT NOT NULL REFERENCES pr_workspaces(id) ON DELETE CASCADE,
	ordinal INTEGER NOT NULL CHECK (ordinal > 0),
	status TEXT NOT NULL CHECK (status = 'observed'),
	payload_json BLOB NOT NULL CHECK (length(payload_json) >= 2 AND length(payload_json) <= 1048576),
	created_at INTEGER NOT NULL,
	updated_at INTEGER NOT NULL,
	UNIQUE (workspace_id, ordinal),
	CHECK (updated_at >= created_at)
);`
	schemaV19PRIngressWatermarksListIndex = `CREATE INDEX IF NOT EXISTS pr_ingress_watermarks_list
	ON pr_ingress_watermarks(workspace_id, status, updated_at DESC, id DESC);`

	schemaV19PRActivityTable = `CREATE TABLE IF NOT EXISTS pr_activity (
	id TEXT PRIMARY KEY,
	workspace_id TEXT NOT NULL REFERENCES pr_workspaces(id) ON DELETE CASCADE,
	ordinal INTEGER NOT NULL CHECK (ordinal > 0),
	status TEXT NOT NULL CHECK (status <> ''),
	payload_json BLOB NOT NULL CHECK (length(payload_json) >= 2 AND length(payload_json) <= 1048576),
	created_at INTEGER NOT NULL,
	updated_at INTEGER NOT NULL,
	UNIQUE (workspace_id, ordinal),
	CHECK (updated_at >= created_at)
);`
	schemaV19PRActivityListIndex = `CREATE INDEX IF NOT EXISTS pr_activity_list
	ON pr_activity(workspace_id, ordinal ASC, id ASC);`

	schemaV19PRIngressCutoverWatermarksTable = `CREATE TABLE IF NOT EXISTS pr_ingress_cutover_watermarks (
	source TEXT NOT NULL CHECK (source <> ''),
	connector TEXT NOT NULL CHECK (connector <> ''),
	inbox_received_at INTEGER NOT NULL,
	inbox_event_id TEXT NOT NULL CHECK (inbox_event_id <> ''),
	created_at INTEGER NOT NULL,
	updated_at INTEGER NOT NULL,
	PRIMARY KEY (source, connector),
	CHECK (updated_at >= created_at)
);`
	schemaV19PRIngressCutoverWatermarksPositionIndex = `CREATE INDEX IF NOT EXISTS pr_ingress_cutover_watermarks_position
	ON pr_ingress_cutover_watermarks(inbox_received_at ASC, inbox_event_id ASC);`

	schemaV19PRWorkspaceRequestsTable = `CREATE TABLE IF NOT EXISTS pr_workspace_requests (
	request_id TEXT PRIMARY KEY,
	workspace_id TEXT NOT NULL REFERENCES pr_workspaces(id) ON DELETE CASCADE,
	kind TEXT NOT NULL CHECK (kind <> ''),
	request_hash TEXT NOT NULL CHECK (length(request_hash) = 64),
	result_json BLOB NOT NULL CHECK (length(result_json) >= 2 AND length(result_json) <= 65536),
	created_at INTEGER NOT NULL
);`
	schemaV19PRWorkspaceRequestsWorkspaceIndex = `CREATE INDEX IF NOT EXISTS pr_workspace_requests_workspace
	ON pr_workspace_requests(workspace_id, created_at DESC, request_id DESC);`

	schemaV19PRWorkspaceHistoryTable = `CREATE TABLE IF NOT EXISTS pr_workspace_history (
	id TEXT PRIMARY KEY,
	workspace_id TEXT NOT NULL REFERENCES pr_workspaces(id) ON DELETE CASCADE,
	version INTEGER NOT NULL CHECK (version > 0),
	sequence INTEGER NOT NULL CHECK (sequence >= 0),
	record_table TEXT NOT NULL CHECK (record_table <> ''),
	record_id TEXT NOT NULL CHECK (record_id <> ''),
	payload_json BLOB NOT NULL CHECK (length(payload_json) >= 2 AND length(payload_json) <= 1048576),
	created_at INTEGER NOT NULL,
	UNIQUE (workspace_id, version, sequence)
);`
	schemaV19PRWorkspaceHistoryReplayIndex = `CREATE INDEX IF NOT EXISTS pr_workspace_history_replay
	ON pr_workspace_history(workspace_id, version ASC, sequence ASC);`

	schemaV19PRWorkspace = schemaV19PRWorkspacesTable + "\n" +
		schemaV19PRWorkspacesListIndex + "\n" +
		schemaV19PRWorkspacesRepositoryIndex + "\n" +
		schemaV19PRWorkspacesStateIndex + "\n" +
		schemaV19PRProviderSnapshotsTable + "\n" + schemaV19PRProviderSnapshotsListIndex + "\n" +
		schemaV19PRCharterRevisionsTable + "\n" + schemaV19PRCharterRevisionsListIndex + "\n" +
		schemaV19PRStageRunsTable + "\n" + schemaV19PRStageRunsListIndex + "\n" +
		schemaV19PRFindingsTable + "\n" + schemaV19PRFindingsListIndex + "\n" +
		schemaV19PRFindingEventsTable + "\n" + schemaV19PRFindingEventsListIndex + "\n" +
		schemaV19PRConversationsTable + "\n" + schemaV19PRConversationsListIndex + "\n" +
		schemaV19PRMessagesTable + "\n" + schemaV19PRMessagesListIndex + "\n" +
		schemaV19PRCorrectionsTable + "\n" + schemaV19PRCorrectionsListIndex + "\n" +
		schemaV19PRRepositoryLessonsTable + "\n" + schemaV19PRRepositoryLessonsListIndex + "\n" +
		schemaV19PRNudgeRoundsTable + "\n" + schemaV19PRNudgeRoundsListIndex + "\n" +
		schemaV19PRNudgeRewardsTable + "\n" + schemaV19PRNudgeRewardsListIndex + "\n" +
		schemaV19PRDeferredGroupsTable + "\n" + schemaV19PRDeferredGroupsListIndex + "\n" +
		schemaV19PRDeferredGroupItemsTable + "\n" + schemaV19PRDeferredGroupItemsListIndex + "\n" + schemaV19PRDeferredGroupItemsActivePositionIndex + "\n" +
		schemaV19PRRepairAttemptsTable + "\n" + schemaV19PRRepairAttemptsListIndex + "\n" +
		schemaV19PRValidationRunsTable + "\n" + schemaV19PRValidationRunsListIndex + "\n" +
		schemaV19PRGateRunsTable + "\n" + schemaV19PRGateRunsListIndex + "\n" +
		schemaV19PRPublicationsTable + "\n" + schemaV19PRPublicationsListIndex + "\n" + schemaV19PRPublicationsClaimIndex + "\n" +
		schemaV19PROperationIntentsTable + "\n" + schemaV19PROperationIntentsListIndex + "\n" + schemaV19PROperationIntentsClaimIndex + "\n" +
		schemaV19PRIngressWatermarksTable + "\n" + schemaV19PRIngressWatermarksListIndex + "\n" +
		schemaV19PRActivityTable + "\n" + schemaV19PRActivityListIndex + "\n" +
		schemaV19PRIngressCutoverWatermarksTable + "\n" + schemaV19PRIngressCutoverWatermarksPositionIndex + "\n" +
		schemaV19PRWorkspaceRequestsTable + "\n" + schemaV19PRWorkspaceRequestsWorkspaceIndex + "\n" +
		schemaV19PRWorkspaceHistoryTable + "\n" + schemaV19PRWorkspaceHistoryReplayIndex
)

type prWorkspaceSchemaEntry struct {
	table      string
	tableSQL   string
	index      string
	indexSQL   string
	uniquePair bool
}

var prWorkspaceSchemaEntries = []prWorkspaceSchemaEntry{
	{"pr_provider_snapshots", schemaV19PRProviderSnapshotsTable, "pr_provider_snapshots_list", schemaV19PRProviderSnapshotsListIndex, true},
	{"pr_charter_revisions", schemaV19PRCharterRevisionsTable, "pr_charter_revisions_list", schemaV19PRCharterRevisionsListIndex, true},
	{"pr_stage_runs", schemaV19PRStageRunsTable, "pr_stage_runs_list", schemaV19PRStageRunsListIndex, true},
	{"pr_findings", schemaV19PRFindingsTable, "pr_findings_list", schemaV19PRFindingsListIndex, true},
	{"pr_finding_events", schemaV19PRFindingEventsTable, "pr_finding_events_list", schemaV19PRFindingEventsListIndex, true},
	{"pr_conversations", schemaV19PRConversationsTable, "pr_conversations_list", schemaV19PRConversationsListIndex, true},
	{"pr_messages", schemaV19PRMessagesTable, "pr_messages_list", schemaV19PRMessagesListIndex, true},
	{"pr_corrections", schemaV19PRCorrectionsTable, "pr_corrections_list", schemaV19PRCorrectionsListIndex, true},
	{"pr_repository_lessons", schemaV19PRRepositoryLessonsTable, "pr_repository_lessons_list", schemaV19PRRepositoryLessonsListIndex, true},
	{"pr_nudge_rounds", schemaV19PRNudgeRoundsTable, "pr_nudge_rounds_list", schemaV19PRNudgeRoundsListIndex, true},
	{"pr_nudge_rewards", schemaV19PRNudgeRewardsTable, "pr_nudge_rewards_list", schemaV19PRNudgeRewardsListIndex, true},
	{"pr_deferred_groups", schemaV19PRDeferredGroupsTable, "pr_deferred_groups_list", schemaV19PRDeferredGroupsListIndex, true},
	{"pr_deferred_group_items", schemaV19PRDeferredGroupItemsTable, "pr_deferred_group_items_list", schemaV19PRDeferredGroupItemsListIndex, true},
	{"pr_repair_attempts", schemaV19PRRepairAttemptsTable, "pr_repair_attempts_list", schemaV19PRRepairAttemptsListIndex, true},
	{"pr_validation_runs", schemaV19PRValidationRunsTable, "pr_validation_runs_list", schemaV19PRValidationRunsListIndex, true},
	{"pr_gate_runs", schemaV19PRGateRunsTable, "pr_gate_runs_list", schemaV19PRGateRunsListIndex, true},
	{"pr_publications", schemaV19PRPublicationsTable, "pr_publications_list", schemaV19PRPublicationsListIndex, true},
	{"pr_operation_intents", schemaV19PROperationIntentsTable, "pr_operation_intents_list", schemaV19PROperationIntentsListIndex, true},
	{"pr_ingress_watermarks", schemaV19PRIngressWatermarksTable, "pr_ingress_watermarks_list", schemaV19PRIngressWatermarksListIndex, true},
	{"pr_activity", schemaV19PRActivityTable, "pr_activity_list", schemaV19PRActivityListIndex, true},
}

// installSchemaV19PRWorkspace is deliberately not called by Store.migrate yet.
// It lets the destructive v19 cutover own installation order as one explicit
// transaction while this additive store can be tested independently.
func installSchemaV19PRWorkspace(ctx context.Context, conn *sql.Conn) error {
	if _, err := conn.ExecContext(ctx, schemaV19PRWorkspace); err != nil {
		return err
	}
	return validateSchemaV19PRWorkspace(ctx, conn)
}

func validateSchemaV19PRWorkspace(ctx context.Context, conn *sql.Conn) error {
	binary := func(name string) schemaIndexColumn {
		return schemaIndexColumn{name: name, collation: "BINARY"}
	}
	if err := validateSchemaTable(ctx, conn, schemaTableSpec{
		name:      "pr_workspaces",
		createSQL: schemaV19PRWorkspacesTable,
		uniqueIndexes: []schemaUniqueIndexSpec{
			{origin: "pk", columns: []schemaIndexColumn{binary("id")}},
			{origin: "u", columns: []schemaIndexColumn{binary("provider"), binary("provider_origin"), binary("repository_id"), binary("pull_request_id")}},
		},
	}); err != nil {
		return err
	}
	for _, index := range []schemaIndexSpec{
		{name: "pr_workspaces_list", createSQL: schemaV19PRWorkspacesListIndex},
		{name: "pr_workspaces_repository", createSQL: schemaV19PRWorkspacesRepositoryIndex},
		{name: "pr_workspaces_state", createSQL: schemaV19PRWorkspacesStateIndex},
	} {
		if err := validateSchemaIndex(ctx, conn, index); err != nil {
			return err
		}
	}
	for _, entry := range prWorkspaceSchemaEntries {
		uniques := []schemaUniqueIndexSpec{{origin: "pk", columns: []schemaIndexColumn{binary("id")}}}
		if entry.uniquePair {
			uniques = append(uniques, schemaUniqueIndexSpec{origin: "u", columns: []schemaIndexColumn{binary("workspace_id"), binary("ordinal")}})
		}
		if entry.table == "pr_deferred_group_items" {
			uniques = append(uniques,
				schemaUniqueIndexSpec{origin: "u", columns: []schemaIndexColumn{binary("workspace_id"), binary("finding_id")}},
				schemaUniqueIndexSpec{name: "pr_deferred_group_items_active_position", origin: "c", partial: true, columns: []schemaIndexColumn{binary("workspace_id"), binary("group_id"), binary("ordinal_in_group")}},
			)
		}
		if err := validateSchemaTable(ctx, conn, schemaTableSpec{name: entry.table, createSQL: entry.tableSQL, uniqueIndexes: uniques}); err != nil {
			return err
		}
		if err := validateSchemaIndex(ctx, conn, schemaIndexSpec{name: entry.index, createSQL: entry.indexSQL}); err != nil {
			return err
		}
	}
	for _, index := range []schemaIndexSpec{
		{name: "pr_deferred_group_items_active_position", createSQL: schemaV19PRDeferredGroupItemsActivePositionIndex},
		{name: "pr_publications_claim", createSQL: schemaV19PRPublicationsClaimIndex},
		{name: "pr_operation_intents_claim", createSQL: schemaV19PROperationIntentsClaimIndex},
	} {
		if err := validateSchemaIndex(ctx, conn, index); err != nil {
			return err
		}
	}
	if err := validateSchemaTable(ctx, conn, schemaTableSpec{
		name:      "pr_ingress_cutover_watermarks",
		createSQL: schemaV19PRIngressCutoverWatermarksTable,
		uniqueIndexes: []schemaUniqueIndexSpec{
			{origin: "pk", columns: []schemaIndexColumn{binary("source"), binary("connector")}},
		},
	}); err != nil {
		return err
	}
	if err := validateSchemaIndex(ctx, conn, schemaIndexSpec{
		name:      "pr_ingress_cutover_watermarks_position",
		createSQL: schemaV19PRIngressCutoverWatermarksPositionIndex,
	}); err != nil {
		return err
	}
	if err := validateSchemaTable(ctx, conn, schemaTableSpec{
		name:      "pr_workspace_requests",
		createSQL: schemaV19PRWorkspaceRequestsTable,
		uniqueIndexes: []schemaUniqueIndexSpec{
			{origin: "pk", columns: []schemaIndexColumn{binary("request_id")}},
		},
	}); err != nil {
		return err
	}
	if err := validateSchemaIndex(ctx, conn, schemaIndexSpec{name: "pr_workspace_requests_workspace", createSQL: schemaV19PRWorkspaceRequestsWorkspaceIndex}); err != nil {
		return err
	}
	if err := validateSchemaTable(ctx, conn, schemaTableSpec{
		name: "pr_workspace_history", createSQL: schemaV19PRWorkspaceHistoryTable,
		uniqueIndexes: []schemaUniqueIndexSpec{
			{origin: "pk", columns: []schemaIndexColumn{binary("id")}},
			{origin: "u", columns: []schemaIndexColumn{binary("workspace_id"), binary("version"), binary("sequence")}},
		},
	}); err != nil {
		return err
	}
	return validateSchemaIndex(ctx, conn, schemaIndexSpec{name: "pr_workspace_history_replay", createSQL: schemaV19PRWorkspaceHistoryReplayIndex})
}
