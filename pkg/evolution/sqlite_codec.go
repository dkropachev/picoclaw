package evolution

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/sipeed/picoclaw/pkg/skills"
)

type evolutionQueryer interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

func consumeEvolutionRows(rows *sql.Rows, next func() error) (resultErr error) {
	defer func() {
		resultErr = errors.Join(resultErr, rows.Close())
	}()
	for rows.Next() {
		if nextErr := next(); nextErr != nil {
			return nextErr
		}
	}
	return rows.Err()
}

func validateEvolutionRecord(class string, record LearningRecord) error {
	if class != "task" && class != "pattern" || evolutionRecordClass(record) != class {
		return errors.New("evolution record class is invalid")
	}
	if record.WorkspaceID != strings.TrimSpace(record.WorkspaceID) ||
		record.ID != strings.TrimSpace(record.ID) ||
		!validEvolutionText(record.WorkspaceID, 4096, true) ||
		!validEvolutionText(record.ID, 1024, true) {
		return errors.New("evolution record identity is invalid")
	}
	switch record.Kind {
	case RecordKindTask, RecordKindPattern, legacyRecordKindCase, legacyRecordKindRule:
	default:
		return errors.New("evolution record kind is invalid")
	}
	switch record.Status {
	case "", "new", "clustered", "ready":
	default:
		return errors.New("evolution record status is invalid")
	}
	if _, err := evolutionRequiredTimestamp(record.CreatedAt); err != nil {
		return err
	}
	if record.UpdatedAt != nil {
		if _, err := evolutionRequiredTimestamp(*record.UpdatedAt); err != nil {
			return err
		}
	}
	for _, value := range []string{
		record.SessionKey, record.TaskHash, record.Summary, record.UserGoal,
		record.FinalOutput, record.Label, record.ClusterReason, record.FinalSnapshotTrigger,
	} {
		if !validEvolutionText(value, maximumEvolutionTextBytes, false) {
			return errors.New("evolution record text is invalid")
		}
	}
	if record.EventCount < 0 || math.IsNaN(record.SuccessRate) || math.IsInf(record.SuccessRate, 0) ||
		math.IsNaN(record.MaturityScore) || math.IsInf(record.MaturityScore, 0) {
		return errors.New("evolution record metrics are invalid")
	}
	for _, values := range evolutionRecordStringFields(record) {
		if err := validateEvolutionStrings(values.values); err != nil {
			return err
		}
	}
	if len(record.ToolExecutions) > maximumEvolutionChildren {
		return errors.New("evolution tool execution count exceeds its limit")
	}
	for _, execution := range record.ToolExecutions {
		if !validEvolutionText(execution.Name, 4096, false) ||
			!validEvolutionText(execution.ErrorSummary, maximumEvolutionTextBytes, false) {
			return errors.New("evolution tool execution is invalid")
		}
		if err := validateEvolutionStrings(execution.SkillNames); err != nil {
			return err
		}
	}
	if record.AttemptTrail != nil {
		if err := validateEvolutionStrings(record.AttemptTrail.AttemptedSkills); err != nil {
			return err
		}
		if err := validateEvolutionStrings(record.AttemptTrail.FinalSuccessfulPath); err != nil {
			return err
		}
		if len(record.AttemptTrail.SkillContextSnapshots) > maximumEvolutionChildren {
			return errors.New("evolution skill context snapshot count exceeds its limit")
		}
		for _, snapshot := range record.AttemptTrail.SkillContextSnapshots {
			if !validEvolutionText(snapshot.Trigger, maximumEvolutionTextBytes, false) {
				return errors.New("evolution skill context trigger is invalid")
			}
			if err := validateEvolutionStrings(snapshot.SkillNames); err != nil {
				return err
			}
		}
	}
	if record.Source != nil {
		encoded, err := json.Marshal(record.Source)
		if err != nil || len(encoded) > maximumEvolutionSourceBytes {
			return errors.New("evolution record source payload is invalid")
		}
	}
	return nil
}

func validateEvolutionDraft(draft SkillDraft) error {
	if draft.WorkspaceID != strings.TrimSpace(draft.WorkspaceID) ||
		draft.ID != strings.TrimSpace(draft.ID) ||
		!validEvolutionText(draft.WorkspaceID, 4096, true) ||
		!validEvolutionText(draft.ID, 1024, true) {
		return errors.New("evolution draft identity is invalid")
	}
	if !validEvolutionText(draft.TargetSkillName, skills.MaxNameLength, false) {
		return errors.New("evolution draft target skill is invalid")
	}
	if !draft.CreatedAt.IsZero() {
		if _, err := evolutionRequiredTimestamp(draft.CreatedAt); err != nil {
			return err
		}
	}
	if draft.UpdatedAt != nil && !draft.UpdatedAt.IsZero() {
		if _, err := evolutionRequiredTimestamp(*draft.UpdatedAt); err != nil {
			return err
		}
	}
	if !validEvolutionText(string(draft.DraftType), 64, false) ||
		!validEvolutionText(string(draft.ChangeKind), 64, false) {
		return errors.New("evolution draft classification is invalid")
	}
	switch draft.Status {
	case DraftStatusCandidate, DraftStatusQuarantined, DraftStatusAccepted:
	default:
		return errors.New("evolution draft status is invalid")
	}
	for _, value := range []string{draft.SourceRecordID, draft.HumanSummary, draft.BodyOrPatch} {
		if !validEvolutionText(value, maximumEvolutionTextBytes, false) {
			return errors.New("evolution draft text is invalid")
		}
	}
	for _, values := range evolutionDraftStringFields(draft) {
		if err := validateEvolutionStrings(values.values); err != nil {
			return err
		}
	}
	return nil
}

func validateEvolutionProfile(profile SkillProfile) error {
	if profile.WorkspaceID != strings.TrimSpace(profile.WorkspaceID) ||
		!validEvolutionText(profile.WorkspaceID, 4096, false) {
		return errors.New("evolution profile workspace identity is invalid")
	}
	if err := validateEvolutionSkillName(profile.SkillName); err != nil {
		return err
	}
	switch profile.Status {
	case "", SkillStatusActive, SkillStatusCold, SkillStatusArchived, SkillStatusDeleted:
	default:
		return errors.New("evolution profile status is invalid")
	}
	if profile.UseCount < 0 || math.IsNaN(profile.RetentionScore) || math.IsInf(profile.RetentionScore, 0) {
		return errors.New("evolution profile metrics are invalid")
	}
	if !profile.LastUsedAt.IsZero() {
		if _, err := evolutionRequiredTimestamp(profile.LastUsedAt); err != nil {
			return err
		}
	}
	for _, value := range []string{
		profile.CurrentVersion, profile.Origin, profile.HumanSummary, profile.ChangeReason,
	} {
		if !validEvolutionText(value, maximumEvolutionTextBytes, false) {
			return errors.New("evolution profile text is invalid")
		}
	}
	for _, values := range evolutionProfileStringFields(profile) {
		if err := validateEvolutionStrings(values.values); err != nil {
			return err
		}
	}
	if len(profile.VersionHistory) > maximumEvolutionChildren {
		return errors.New("evolution profile version history exceeds its limit")
	}
	for _, entry := range profile.VersionHistory {
		if !entry.Timestamp.IsZero() {
			if _, err := evolutionRequiredTimestamp(entry.Timestamp); err != nil {
				return err
			}
		}
		for _, value := range []string{
			entry.Version, entry.Action, entry.DraftID, entry.Summary, entry.RollbackReason,
		} {
			if !validEvolutionText(value, maximumEvolutionTextBytes, false) {
				return errors.New("evolution profile version entry is invalid")
			}
		}
	}
	return nil
}

func validateEvolutionSkillName(value string) error {
	if !validEvolutionText(value, skills.MaxNameLength, true) {
		return errors.New("evolution skill name is invalid")
	}
	return skills.ValidateSkillName(value)
}

func validateEvolutionStrings(values []string) error {
	if len(values) > maximumEvolutionChildren {
		return errors.New("evolution ordered values exceed their limit")
	}
	for _, value := range values {
		if !validEvolutionText(value, maximumEvolutionTextBytes, false) {
			return errors.New("evolution ordered value is invalid")
		}
	}
	return nil
}

func validEvolutionText(value string, maximum int, required bool) bool {
	return utf8.ValidString(value) && len(value) <= maximum && !strings.ContainsRune(value, '\x00') &&
		(!required || value != "")
}

func evolutionRequiredTimestamp(value time.Time) (int64, error) {
	if value.IsZero() {
		return 0, errors.New("evolution timestamp is required")
	}
	nanoseconds := value.UnixNano()
	if nanoseconds <= 0 || !time.Unix(0, nanoseconds).Equal(value) {
		return 0, errors.New("evolution timestamp is outside the supported range")
	}
	return nanoseconds, nil
}

func evolutionOptionalTimestamp(value time.Time) int64 {
	if value.IsZero() {
		return 0
	}
	return value.UnixNano()
}

func evolutionNullableTimestamp(value *time.Time) any {
	if value == nil {
		return nil
	}
	return evolutionOptionalTimestamp(*value)
}

func evolutionBool(value bool) int {
	if value {
		return 1
	}
	return 0
}

func evolutionNullableBool(value *bool) any {
	if value == nil {
		return nil
	}
	return evolutionBool(*value)
}

type evolutionStringField struct {
	name   string
	values []string
}

func evolutionRecordStringFields(record LearningRecord) []evolutionStringField {
	return []evolutionStringField{
		{"tool_kinds", record.ToolKinds},
		{"initial_skill_names", record.InitialSkillNames},
		{"added_skill_names", record.AddedSkillNames},
		{"used_skill_names", record.UsedSkillNames},
		{"all_loaded_skill_names", record.AllLoadedSkillNames},
		{"active_skill_names", record.ActiveSkillNames},
		{"signals", record.Signals},
		{"source_record_ids", record.SourceRecordIDs},
		{"task_record_ids", record.TaskRecordIDs},
		{"winning_path", record.WinningPath},
		{"late_added_skills", record.LateAddedSkills},
		{"matched_skill_names", record.MatchedSkillNames},
	}
}

func evolutionDraftStringFields(draft SkillDraft) []evolutionStringField {
	return []evolutionStringField{
		{"matched_skill_refs", draft.MatchedSkillRefs},
		{"intended_use_cases", draft.IntendedUseCases},
		{"preferred_entry_path", draft.PreferredEntryPath},
		{"avoid_patterns", draft.AvoidPatterns},
		{"review_notes", draft.ReviewNotes},
		{"scan_findings", draft.ScanFindings},
	}
}

func evolutionProfileStringFields(profile SkillProfile) []evolutionStringField {
	return []evolutionStringField{
		{"intended_use_cases", profile.IntendedUseCases},
		{"preferred_entry_path", profile.PreferredEntryPath},
		{"avoid_patterns", profile.AvoidPatterns},
	}
}

func putEvolutionRecord(
	ctx context.Context,
	conn *sql.Conn,
	class string,
	record LearningRecord,
	importSource string,
	runtimeUpdate bool,
) (bool, error) {
	var position, version int64
	var existingSource sql.NullString
	queryErr := conn.QueryRowContext(ctx, `SELECT position, version, import_source
        FROM evolution_records WHERE record_class = ? AND workspace_id = ? AND record_id = ?`,
		class, record.WorkspaceID, record.ID).Scan(&position, &version, &existingSource)
	switch {
	case queryErr == nil:
		if importSource != "" {
			if !existingSource.Valid ||
				evolutionImportPriority(existingSource.String) >= evolutionImportPriority(importSource) {
				return false, nil
			}
		} else if !runtimeUpdate {
			return false, nil
		}
		if err := updateEvolutionRecord(
			ctx,
			conn,
			class,
			position,
			version,
			record,
			nullableImportSource(importSource),
		); err != nil {
			return false, err
		}
	case errors.Is(queryErr, sql.ErrNoRows):
		if err := conn.QueryRowContext(ctx, `SELECT COALESCE(MAX(position), -1) + 1
            FROM evolution_records WHERE record_class = ?`, class).Scan(&position); err != nil {
			return false, err
		}
		if position >= maximumEvolutionRecords {
			return false, errors.New("evolution record count exceeds its limit")
		}
		if err := insertEvolutionRecord(
			ctx,
			conn,
			class,
			position,
			record,
			nullableImportSource(importSource),
		); err != nil {
			return false, err
		}
	default:
		return false, queryErr
	}
	if err := replaceEvolutionRecordChildren(ctx, conn, class, record); err != nil {
		return false, err
	}
	return true, nil
}

func evolutionImportPriority(source string) int {
	switch source {
	case "learning-records":
		return 0
	case "task-records", "pattern-records":
		return 1
	default:
		return 2
	}
}

func nullableImportSource(source string) any {
	if source == "" {
		return nil
	}
	return source
}

func evolutionSourceJSON(source map[string]any) ([]byte, error) {
	if source == nil {
		return nil, nil
	}
	encoded, err := json.Marshal(source)
	if err != nil || len(encoded) > maximumEvolutionSourceBytes {
		return nil, errors.New("evolution record source payload is invalid")
	}
	return encoded, nil
}

func insertEvolutionRecord(
	ctx context.Context,
	conn *sql.Conn,
	class string,
	position int64,
	record LearningRecord,
	importSource any,
) error {
	created, _ := evolutionRequiredTimestamp(record.CreatedAt)
	source, err := evolutionSourceJSON(record.Source)
	if err != nil {
		return err
	}
	_, err = conn.ExecContext(ctx, `INSERT INTO evolution_records (
        record_class, workspace_id, record_id, position, kind, created_at_unix_nano,
        updated_at_unix_nano, session_key, task_hash, summary, user_goal, final_output,
        source_json, status, success, label, cluster_reason, event_count, success_rate,
        maturity_score, final_snapshot_trigger, import_source, version
    ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 1)`,
		class, record.WorkspaceID, record.ID, position, string(record.Kind), created,
		evolutionNullableTimestamp(record.UpdatedAt), record.SessionKey, record.TaskHash,
		record.Summary, record.UserGoal, record.FinalOutput, source, string(record.Status),
		evolutionNullableBool(record.Success), record.Label, record.ClusterReason,
		record.EventCount, record.SuccessRate, record.MaturityScore,
		record.FinalSnapshotTrigger, importSource)
	return err
}

func updateEvolutionRecord(
	ctx context.Context,
	conn *sql.Conn,
	class string,
	position, version int64,
	record LearningRecord,
	importSource any,
) error {
	created, _ := evolutionRequiredTimestamp(record.CreatedAt)
	source, err := evolutionSourceJSON(record.Source)
	if err != nil {
		return err
	}
	result, err := conn.ExecContext(ctx, `UPDATE evolution_records SET
        position = ?, kind = ?, created_at_unix_nano = ?, updated_at_unix_nano = ?,
        session_key = ?, task_hash = ?, summary = ?, user_goal = ?, final_output = ?,
        source_json = ?, status = ?, success = ?, label = ?, cluster_reason = ?,
        event_count = ?, success_rate = ?, maturity_score = ?, final_snapshot_trigger = ?,
        import_source = ?, version = version + 1
      WHERE record_class = ? AND workspace_id = ? AND record_id = ? AND version = ?`,
		position, string(record.Kind), created, evolutionNullableTimestamp(record.UpdatedAt),
		record.SessionKey, record.TaskHash, record.Summary, record.UserGoal, record.FinalOutput,
		source, string(record.Status), evolutionNullableBool(record.Success), record.Label,
		record.ClusterReason, record.EventCount, record.SuccessRate, record.MaturityScore,
		record.FinalSnapshotTrigger, importSource, class, record.WorkspaceID, record.ID, version)
	if err != nil {
		return err
	}
	changed, err := result.RowsAffected()
	if err != nil || changed != 1 {
		return errors.New("evolution record changed during version-fenced update")
	}
	return nil
}

func replaceEvolutionRecordChildren(
	ctx context.Context,
	conn *sql.Conn,
	class string,
	record LearningRecord,
) error {
	arguments := []any{class, record.WorkspaceID, record.ID}
	for _, statement := range []string{
		`DELETE FROM evolution_record_strings WHERE record_class = ? AND workspace_id = ? AND record_id = ?`,
		`DELETE FROM evolution_record_tool_executions WHERE record_class = ? AND workspace_id = ? AND record_id = ?`,
		`DELETE FROM evolution_record_attempt_trails WHERE record_class = ? AND workspace_id = ? AND record_id = ?`,
	} {
		if _, err := conn.ExecContext(ctx, statement, arguments...); err != nil {
			return err
		}
	}
	for _, field := range evolutionRecordStringFields(record) {
		for position, value := range field.values {
			if _, err := conn.ExecContext(ctx, `INSERT INTO evolution_record_strings
                (record_class, workspace_id, record_id, field_name, position, value)
                VALUES (?, ?, ?, ?, ?, ?)`, class, record.WorkspaceID, record.ID,
				field.name, position, value); err != nil {
				return err
			}
		}
	}
	for position, execution := range record.ToolExecutions {
		if _, err := conn.ExecContext(ctx, `INSERT INTO evolution_record_tool_executions
            (record_class, workspace_id, record_id, position, name, success, error_summary)
            VALUES (?, ?, ?, ?, ?, ?, ?)`, class, record.WorkspaceID, record.ID,
			position, execution.Name, evolutionBool(execution.Success), execution.ErrorSummary); err != nil {
			return err
		}
		for skillPosition, skillName := range execution.SkillNames {
			if _, err := conn.ExecContext(ctx, `INSERT INTO evolution_record_tool_execution_skills
                (record_class, workspace_id, record_id, execution_position, position, skill_name)
                VALUES (?, ?, ?, ?, ?, ?)`, class, record.WorkspaceID, record.ID,
				position, skillPosition, skillName); err != nil {
				return err
			}
		}
	}
	if record.AttemptTrail == nil {
		return nil
	}
	if _, err := conn.ExecContext(ctx, `INSERT INTO evolution_record_attempt_trails
        (record_class, workspace_id, record_id) VALUES (?, ?, ?)`, arguments...); err != nil {
		return err
	}
	for _, field := range []evolutionStringField{
		{"attempted_skills", record.AttemptTrail.AttemptedSkills},
		{"final_successful_path", record.AttemptTrail.FinalSuccessfulPath},
	} {
		for position, value := range field.values {
			if _, err := conn.ExecContext(ctx, `INSERT INTO evolution_record_attempt_strings
                (record_class, workspace_id, record_id, field_name, position, value)
                VALUES (?, ?, ?, ?, ?, ?)`, class, record.WorkspaceID, record.ID,
				field.name, position, value); err != nil {
				return err
			}
		}
	}
	for position, snapshot := range record.AttemptTrail.SkillContextSnapshots {
		if _, err := conn.ExecContext(ctx, `INSERT INTO evolution_record_attempt_snapshots
            (record_class, workspace_id, record_id, position, sequence, trigger_name)
            VALUES (?, ?, ?, ?, ?, ?)`, class, record.WorkspaceID, record.ID,
			position, snapshot.Sequence, snapshot.Trigger); err != nil {
			return err
		}
		for skillPosition, skillName := range snapshot.SkillNames {
			if _, err := conn.ExecContext(ctx, `INSERT INTO evolution_record_attempt_snapshot_skills
                (record_class, workspace_id, record_id, snapshot_position, position, skill_name)
                VALUES (?, ?, ?, ?, ?, ?)`, class, record.WorkspaceID, record.ID,
				position, skillPosition, skillName); err != nil {
				return err
			}
		}
	}
	return nil
}

func replaceEvolutionRecords(
	ctx context.Context,
	conn *sql.Conn,
	class string,
	records []LearningRecord,
) error {
	records = mergeLearningRecordsByID(nil, records)
	if len(records) > maximumEvolutionRecords {
		return errors.New("evolution record count exceeds its limit")
	}
	if _, err := conn.ExecContext(ctx, `DELETE FROM evolution_records WHERE record_class = ?`, class); err != nil {
		return err
	}
	for position, record := range records {
		if err := insertEvolutionRecord(ctx, conn, class, int64(position), record, nil); err != nil {
			return err
		}
		if err := replaceEvolutionRecordChildren(ctx, conn, class, record); err != nil {
			return err
		}
	}
	return nil
}

func loadEvolutionRecords(
	ctx context.Context,
	queryer evolutionQueryer,
	class string,
) ([]LearningRecord, error) {
	rows, err := queryer.QueryContext(ctx, `SELECT workspace_id, record_id, kind,
        created_at_unix_nano, updated_at_unix_nano, session_key, task_hash, summary,
        user_goal, final_output, source_json, status, success, label, cluster_reason,
        event_count, success_rate, maturity_score, final_snapshot_trigger
      FROM evolution_records WHERE record_class = ? ORDER BY position`, class)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	records := make([]LearningRecord, 0)
	indexes := make(map[string]int)
	for rows.Next() {
		var record LearningRecord
		var kind, status string
		var created int64
		var updated, success sql.NullInt64
		var source []byte
		if err := rows.Scan(&record.WorkspaceID, &record.ID, &kind, &created, &updated,
			&record.SessionKey, &record.TaskHash, &record.Summary, &record.UserGoal,
			&record.FinalOutput, &source, &status, &success, &record.Label,
			&record.ClusterReason, &record.EventCount, &record.SuccessRate,
			&record.MaturityScore, &record.FinalSnapshotTrigger); err != nil {
			return nil, err
		}
		record.Kind = RecordKind(kind)
		record.Status = RecordStatus(status)
		record.CreatedAt = time.Unix(0, created).UTC()
		if updated.Valid {
			value := time.Unix(0, updated.Int64).UTC()
			record.UpdatedAt = &value
		}
		if success.Valid {
			value := success.Int64 == 1
			record.Success = &value
		}
		if source != nil {
			if err := json.Unmarshal(source, &record.Source); err != nil {
				return nil, err
			}
		}
		indexes[evolutionRecordIdentity(class, record.WorkspaceID, record.ID)] = len(records)
		records = append(records, record)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if err := loadEvolutionRecordStrings(ctx, queryer, class, records, indexes); err != nil {
		return nil, err
	}
	if err := loadEvolutionToolExecutions(ctx, queryer, class, records, indexes); err != nil {
		return nil, err
	}
	if err := loadEvolutionAttemptTrails(ctx, queryer, class, records, indexes); err != nil {
		return nil, err
	}
	return records, nil
}

func evolutionRecordIdentity(class, workspace, id string) string {
	return class + "\x00" + workspace + "\x00" + id
}

func loadEvolutionRecordStrings(
	ctx context.Context,
	queryer evolutionQueryer,
	class string,
	records []LearningRecord,
	indexes map[string]int,
) error {
	rows, err := queryer.QueryContext(ctx, `SELECT s.workspace_id, s.record_id, s.field_name, s.value
        FROM evolution_record_strings s
        JOIN evolution_records r USING(record_class, workspace_id, record_id)
       WHERE s.record_class = ? ORDER BY r.position, s.field_name, s.position`, class)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var workspace, id, field, value string
		if err := rows.Scan(&workspace, &id, &field, &value); err != nil {
			return err
		}
		record := &records[indexes[evolutionRecordIdentity(class, workspace, id)]]
		switch field {
		case "tool_kinds":
			record.ToolKinds = append(record.ToolKinds, value)
		case "initial_skill_names":
			record.InitialSkillNames = append(record.InitialSkillNames, value)
		case "added_skill_names":
			record.AddedSkillNames = append(record.AddedSkillNames, value)
		case "used_skill_names":
			record.UsedSkillNames = append(record.UsedSkillNames, value)
		case "all_loaded_skill_names":
			record.AllLoadedSkillNames = append(record.AllLoadedSkillNames, value)
		case "active_skill_names":
			record.ActiveSkillNames = append(record.ActiveSkillNames, value)
		case "signals":
			record.Signals = append(record.Signals, value)
		case "source_record_ids":
			record.SourceRecordIDs = append(record.SourceRecordIDs, value)
		case "task_record_ids":
			record.TaskRecordIDs = append(record.TaskRecordIDs, value)
		case "winning_path":
			record.WinningPath = append(record.WinningPath, value)
		case "late_added_skills":
			record.LateAddedSkills = append(record.LateAddedSkills, value)
		case "matched_skill_names":
			record.MatchedSkillNames = append(record.MatchedSkillNames, value)
		default:
			return errors.New("evolution record has an unknown ordered field")
		}
	}
	return rows.Err()
}

func loadEvolutionToolExecutions(
	ctx context.Context,
	queryer evolutionQueryer,
	class string,
	records []LearningRecord,
	indexes map[string]int,
) error {
	rows, queryErr := queryer.QueryContext(ctx, `SELECT e.workspace_id, e.record_id, e.position,
        e.name, e.success, e.error_summary
      FROM evolution_record_tool_executions e
      JOIN evolution_records r USING(record_class, workspace_id, record_id)
     WHERE e.record_class = ? ORDER BY r.position, e.position`, class)
	if queryErr != nil {
		return queryErr
	}
	executionIndexes := make(map[string]int)
	consumeErr := consumeEvolutionRows(rows, func() error {
		var workspace, id, name, errorSummary string
		var position, success int
		if scanErr := rows.Scan(
			&workspace, &id, &position, &name, &success, &errorSummary,
		); scanErr != nil {
			return scanErr
		}
		recordIndex := indexes[evolutionRecordIdentity(class, workspace, id)]
		executionIndexes[evolutionExecutionIdentity(class, workspace, id, position)] = len(
			records[recordIndex].ToolExecutions,
		)
		records[recordIndex].ToolExecutions = append(records[recordIndex].ToolExecutions,
			ToolExecutionRecord{Name: name, Success: success == 1, ErrorSummary: errorSummary})
		return nil
	})
	if consumeErr != nil {
		return consumeErr
	}
	rows, queryErr = queryer.QueryContext(ctx, `SELECT s.workspace_id, s.record_id,
        s.execution_position, s.skill_name
      FROM evolution_record_tool_execution_skills s
      JOIN evolution_records r USING(record_class, workspace_id, record_id)
     WHERE s.record_class = ? ORDER BY r.position, s.execution_position, s.position`, class)
	if queryErr != nil {
		return queryErr
	}
	return consumeEvolutionRows(rows, func() error {
		var workspace, id, skillName string
		var executionPosition int
		if scanErr := rows.Scan(&workspace, &id, &executionPosition, &skillName); scanErr != nil {
			return scanErr
		}
		recordIndex := indexes[evolutionRecordIdentity(class, workspace, id)]
		executionIndex := executionIndexes[evolutionExecutionIdentity(class, workspace, id, executionPosition)]
		records[recordIndex].ToolExecutions[executionIndex].SkillNames = append(
			records[recordIndex].ToolExecutions[executionIndex].SkillNames, skillName)
		return nil
	})
}

func evolutionExecutionIdentity(class, workspace, id string, position int) string {
	return fmt.Sprintf("%s\x00%s\x00%s\x00%d", class, workspace, id, position)
}

func loadEvolutionAttemptTrails(
	ctx context.Context,
	queryer evolutionQueryer,
	class string,
	records []LearningRecord,
	indexes map[string]int,
) error {
	rows, queryErr := queryer.QueryContext(ctx, `SELECT a.workspace_id, a.record_id
        FROM evolution_record_attempt_trails a
        JOIN evolution_records r USING(record_class, workspace_id, record_id)
       WHERE a.record_class = ? ORDER BY r.position`, class)
	if queryErr != nil {
		return queryErr
	}
	consumeErr := consumeEvolutionRows(rows, func() error {
		var workspace, id string
		if scanErr := rows.Scan(&workspace, &id); scanErr != nil {
			return scanErr
		}
		records[indexes[evolutionRecordIdentity(class, workspace, id)]].AttemptTrail = &AttemptTrail{}
		return nil
	})
	if consumeErr != nil {
		return consumeErr
	}
	rows, queryErr = queryer.QueryContext(ctx, `SELECT s.workspace_id, s.record_id, s.field_name, s.value
        FROM evolution_record_attempt_strings s
        JOIN evolution_records r USING(record_class, workspace_id, record_id)
       WHERE s.record_class = ? ORDER BY r.position, s.field_name, s.position`, class)
	if queryErr != nil {
		return queryErr
	}
	consumeErr = consumeEvolutionRows(rows, func() error {
		var workspace, id, field, value string
		if scanErr := rows.Scan(&workspace, &id, &field, &value); scanErr != nil {
			return scanErr
		}
		trail := records[indexes[evolutionRecordIdentity(class, workspace, id)]].AttemptTrail
		if field == "attempted_skills" {
			trail.AttemptedSkills = append(trail.AttemptedSkills, value)
		} else {
			trail.FinalSuccessfulPath = append(trail.FinalSuccessfulPath, value)
		}
		return nil
	})
	if consumeErr != nil {
		return consumeErr
	}
	rows, queryErr = queryer.QueryContext(ctx, `SELECT s.workspace_id, s.record_id, s.position,
        s.sequence, s.trigger_name
      FROM evolution_record_attempt_snapshots s
      JOIN evolution_records r USING(record_class, workspace_id, record_id)
     WHERE s.record_class = ? ORDER BY r.position, s.position`, class)
	if queryErr != nil {
		return queryErr
	}
	snapshotIndexes := make(map[string]int)
	consumeErr = consumeEvolutionRows(rows, func() error {
		var workspace, id, trigger string
		var position, sequence int
		if scanErr := rows.Scan(&workspace, &id, &position, &sequence, &trigger); scanErr != nil {
			return scanErr
		}
		recordIndex := indexes[evolutionRecordIdentity(class, workspace, id)]
		snapshotIndexes[evolutionExecutionIdentity(class, workspace, id, position)] = len(
			records[recordIndex].AttemptTrail.SkillContextSnapshots,
		)
		records[recordIndex].AttemptTrail.SkillContextSnapshots = append(
			records[recordIndex].AttemptTrail.SkillContextSnapshots,
			SkillContextSnapshot{Sequence: sequence, Trigger: trigger})
		return nil
	})
	if consumeErr != nil {
		return consumeErr
	}
	rows, queryErr = queryer.QueryContext(ctx, `SELECT s.workspace_id, s.record_id,
        s.snapshot_position, s.skill_name
      FROM evolution_record_attempt_snapshot_skills s
      JOIN evolution_records r USING(record_class, workspace_id, record_id)
     WHERE s.record_class = ? ORDER BY r.position, s.snapshot_position, s.position`, class)
	if queryErr != nil {
		return queryErr
	}
	return consumeEvolutionRows(rows, func() error {
		var workspace, id, skillName string
		var snapshotPosition int
		if scanErr := rows.Scan(&workspace, &id, &snapshotPosition, &skillName); scanErr != nil {
			return scanErr
		}
		recordIndex := indexes[evolutionRecordIdentity(class, workspace, id)]
		snapshotIndex := snapshotIndexes[evolutionExecutionIdentity(class, workspace, id, snapshotPosition)]
		trail := records[recordIndex].AttemptTrail
		trail.SkillContextSnapshots[snapshotIndex].SkillNames = append(
			trail.SkillContextSnapshots[snapshotIndex].SkillNames, skillName)
		return nil
	})
}
