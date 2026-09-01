package evolution

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

func replaceEvolutionDrafts(ctx context.Context, conn *sql.Conn, drafts []SkillDraft) error {
	if len(drafts) > maximumEvolutionDrafts {
		return errors.New("evolution draft count exceeds its limit")
	}
	if _, err := conn.ExecContext(ctx, `DELETE FROM evolution_skill_drafts`); err != nil {
		return err
	}
	for position, draft := range drafts {
		if err := insertEvolutionDraft(ctx, conn, position, draft); err != nil {
			return err
		}
	}
	return nil
}

func insertEvolutionDraft(ctx context.Context, conn *sql.Conn, position int, draft SkillDraft) error {
	_, err := conn.ExecContext(ctx, `INSERT INTO evolution_skill_drafts (
        workspace_id, draft_id, position, created_at_unix_nano, updated_at_unix_nano,
        source_record_id, target_skill_name, draft_type, change_kind, human_summary,
        body_or_patch, status, version
    ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 1)`, draft.WorkspaceID, draft.ID,
		position, evolutionOptionalTimestamp(draft.CreatedAt), evolutionNullableTimestamp(draft.UpdatedAt),
		draft.SourceRecordID, draft.TargetSkillName, string(draft.DraftType), string(draft.ChangeKind),
		draft.HumanSummary, draft.BodyOrPatch, string(draft.Status))
	if err != nil {
		return err
	}
	return replaceEvolutionDraftChildren(ctx, conn, draft)
}

func putEvolutionDraft(
	ctx context.Context,
	conn *sql.Conn,
	draft SkillDraft,
	importOnly bool,
) (bool, error) {
	var position, version int
	err := conn.QueryRowContext(ctx, `SELECT position, version FROM evolution_skill_drafts
        WHERE workspace_id = ? AND draft_id = ?`, draft.WorkspaceID, draft.ID).
		Scan(&position, &version)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		if err := conn.QueryRowContext(ctx, `SELECT COALESCE(MAX(position), -1) + 1
            FROM evolution_skill_drafts`).Scan(&position); err != nil {
			return false, err
		}
		if position >= maximumEvolutionDrafts {
			return false, errors.New("evolution draft count exceeds its limit")
		}
		return true, insertEvolutionDraft(ctx, conn, position, draft)
	case err != nil:
		return false, err
	case importOnly:
		return false, nil
	}
	result, err := conn.ExecContext(ctx, `UPDATE evolution_skill_drafts SET
        created_at_unix_nano = ?, updated_at_unix_nano = ?, source_record_id = ?,
        target_skill_name = ?, draft_type = ?, change_kind = ?, human_summary = ?,
        body_or_patch = ?, status = ?, version = version + 1
      WHERE workspace_id = ? AND draft_id = ? AND version = ?`,
		evolutionOptionalTimestamp(draft.CreatedAt), evolutionNullableTimestamp(draft.UpdatedAt),
		draft.SourceRecordID, draft.TargetSkillName, string(draft.DraftType), string(draft.ChangeKind),
		draft.HumanSummary, draft.BodyOrPatch, string(draft.Status), draft.WorkspaceID, draft.ID, version)
	if err != nil {
		return false, err
	}
	changed, err := result.RowsAffected()
	if err != nil || changed != 1 {
		return false, errors.New("evolution draft changed during version-fenced update")
	}
	if err := replaceEvolutionDraftChildren(ctx, conn, draft); err != nil {
		return false, err
	}
	return true, nil
}

func replaceEvolutionDraftChildren(ctx context.Context, conn *sql.Conn, draft SkillDraft) error {
	if _, err := conn.ExecContext(ctx, `DELETE FROM evolution_skill_draft_strings
        WHERE workspace_id = ? AND draft_id = ?`, draft.WorkspaceID, draft.ID); err != nil {
		return err
	}
	for _, field := range evolutionDraftStringFields(draft) {
		for valuePosition, value := range field.values {
			if _, err := conn.ExecContext(ctx, `INSERT INTO evolution_skill_draft_strings
                (workspace_id, draft_id, field_name, position, value) VALUES (?, ?, ?, ?, ?)`,
				draft.WorkspaceID, draft.ID, field.name, valuePosition, value); err != nil {
				return err
			}
		}
	}
	return nil
}

func loadEvolutionDrafts(ctx context.Context, queryer evolutionQueryer) ([]SkillDraft, error) {
	rows, err := queryer.QueryContext(ctx, `SELECT workspace_id, draft_id, created_at_unix_nano,
        updated_at_unix_nano, source_record_id, target_skill_name, draft_type, change_kind,
        human_summary, body_or_patch, status
      FROM evolution_skill_drafts ORDER BY position`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	drafts := make([]SkillDraft, 0)
	indexes := make(map[string]int)
	for rows.Next() {
		var draft SkillDraft
		var created int64
		var updated sql.NullInt64
		var draftType, changeKind, status string
		if err := rows.Scan(&draft.WorkspaceID, &draft.ID, &created, &updated,
			&draft.SourceRecordID, &draft.TargetSkillName, &draftType, &changeKind,
			&draft.HumanSummary, &draft.BodyOrPatch, &status); err != nil {
			return nil, err
		}
		draft.CreatedAt = evolutionStoredTime(created)
		if updated.Valid {
			value := evolutionStoredTime(updated.Int64)
			draft.UpdatedAt = &value
		}
		draft.DraftType = DraftType(draftType)
		draft.ChangeKind = ChangeKind(changeKind)
		draft.Status = DraftStatus(status)
		indexes[draftKey(draft.WorkspaceID, draft.ID)] = len(drafts)
		drafts = append(drafts, draft)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	rows, err = queryer.QueryContext(ctx, `SELECT s.workspace_id, s.draft_id, s.field_name, s.value
        FROM evolution_skill_draft_strings s
        JOIN evolution_skill_drafts d USING(workspace_id, draft_id)
       ORDER BY d.position, s.field_name, s.position`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var workspace, id, field, value string
		if err := rows.Scan(&workspace, &id, &field, &value); err != nil {
			return nil, err
		}
		draft := &drafts[indexes[draftKey(workspace, id)]]
		switch field {
		case "matched_skill_refs":
			draft.MatchedSkillRefs = append(draft.MatchedSkillRefs, value)
		case "intended_use_cases":
			draft.IntendedUseCases = append(draft.IntendedUseCases, value)
		case "preferred_entry_path":
			draft.PreferredEntryPath = append(draft.PreferredEntryPath, value)
		case "avoid_patterns":
			draft.AvoidPatterns = append(draft.AvoidPatterns, value)
		case "review_notes":
			draft.ReviewNotes = append(draft.ReviewNotes, value)
		case "scan_findings":
			draft.ScanFindings = append(draft.ScanFindings, value)
		default:
			return nil, errors.New("evolution draft has an unknown ordered field")
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return drafts, nil
}

func putEvolutionProfile(
	ctx context.Context,
	conn *sql.Conn,
	profile SkillProfile,
	importOnly bool,
) (bool, error) {
	var version int64
	err := conn.QueryRowContext(ctx, `SELECT version FROM evolution_skill_profiles
        WHERE workspace_id = ? AND skill_name = ?`, profile.WorkspaceID, profile.SkillName).Scan(&version)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		if err := insertEvolutionProfile(ctx, conn, profile); err != nil {
			return false, err
		}
	case err != nil:
		return false, err
	case importOnly:
		return false, nil
	default:
		if err := updateEvolutionProfile(ctx, conn, profile, version); err != nil {
			return false, err
		}
	}
	if err := replaceEvolutionProfileChildren(ctx, conn, profile); err != nil {
		return false, err
	}
	return true, nil
}

func insertEvolutionProfile(ctx context.Context, conn *sql.Conn, profile SkillProfile) error {
	_, err := conn.ExecContext(ctx, `INSERT INTO evolution_skill_profiles (
        workspace_id, skill_name, current_version, status, origin, human_summary,
        change_reason, last_used_at_unix_nano, use_count, retention_score, version
    ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 1)`, profile.WorkspaceID, profile.SkillName,
		profile.CurrentVersion, string(profile.Status), profile.Origin, profile.HumanSummary,
		profile.ChangeReason, evolutionOptionalTimestamp(profile.LastUsedAt), profile.UseCount,
		profile.RetentionScore)
	return err
}

func updateEvolutionProfile(
	ctx context.Context,
	conn *sql.Conn,
	profile SkillProfile,
	version int64,
) error {
	result, err := conn.ExecContext(ctx, `UPDATE evolution_skill_profiles SET
        current_version = ?, status = ?, origin = ?, human_summary = ?, change_reason = ?,
        last_used_at_unix_nano = ?, use_count = ?, retention_score = ?, version = version + 1
      WHERE workspace_id = ? AND skill_name = ? AND version = ?`, profile.CurrentVersion,
		string(profile.Status), profile.Origin, profile.HumanSummary, profile.ChangeReason,
		evolutionOptionalTimestamp(profile.LastUsedAt), profile.UseCount, profile.RetentionScore,
		profile.WorkspaceID, profile.SkillName, version)
	if err != nil {
		return err
	}
	changed, err := result.RowsAffected()
	if err != nil || changed != 1 {
		return errors.New("evolution profile changed during version-fenced update")
	}
	return nil
}

func replaceEvolutionProfileChildren(ctx context.Context, conn *sql.Conn, profile SkillProfile) error {
	for _, statement := range []string{
		`DELETE FROM evolution_skill_profile_strings WHERE workspace_id = ? AND skill_name = ?`,
		`DELETE FROM evolution_skill_profile_versions WHERE workspace_id = ? AND skill_name = ?`,
	} {
		if _, err := conn.ExecContext(ctx, statement, profile.WorkspaceID, profile.SkillName); err != nil {
			return err
		}
	}
	for _, field := range evolutionProfileStringFields(profile) {
		for position, value := range field.values {
			if _, err := conn.ExecContext(ctx, `INSERT INTO evolution_skill_profile_strings
                (workspace_id, skill_name, field_name, position, value) VALUES (?, ?, ?, ?, ?)`,
				profile.WorkspaceID, profile.SkillName, field.name, position, value); err != nil {
				return err
			}
		}
	}
	for position, entry := range profile.VersionHistory {
		if _, err := conn.ExecContext(ctx, `INSERT INTO evolution_skill_profile_versions (
            workspace_id, skill_name, position, version_name, action_name, timestamp_unix_nano,
            draft_id, summary, rollback, rollback_reason
        ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, profile.WorkspaceID, profile.SkillName,
			position, entry.Version, entry.Action, evolutionOptionalTimestamp(entry.Timestamp),
			entry.DraftID, entry.Summary, evolutionBool(entry.Rollback), entry.RollbackReason); err != nil {
			return err
		}
	}
	return nil
}

func loadEvolutionProfile(
	ctx context.Context,
	queryer evolutionQueryer,
	workspace, skillName string,
) (SkillProfile, bool, error) {
	var profile SkillProfile
	var status string
	var lastUsed int64
	err := queryer.QueryRowContext(ctx, `SELECT workspace_id, skill_name, current_version,
        status, origin, human_summary, change_reason, last_used_at_unix_nano,
        use_count, retention_score
      FROM evolution_skill_profiles WHERE workspace_id = ? AND skill_name = ?`, workspace, skillName).
		Scan(&profile.WorkspaceID, &profile.SkillName, &profile.CurrentVersion, &status,
			&profile.Origin, &profile.HumanSummary, &profile.ChangeReason, &lastUsed,
			&profile.UseCount, &profile.RetentionScore)
	if errors.Is(err, sql.ErrNoRows) {
		return SkillProfile{}, false, nil
	}
	if err != nil {
		return SkillProfile{}, false, err
	}
	profile.Status = SkillStatus(status)
	profile.LastUsedAt = evolutionStoredTime(lastUsed)
	if err := loadOneEvolutionProfileChildren(ctx, queryer, &profile); err != nil {
		return SkillProfile{}, false, err
	}
	return profile, true, nil
}

func loadOneEvolutionProfileChildren(
	ctx context.Context,
	queryer evolutionQueryer,
	profile *SkillProfile,
) error {
	rows, err := queryer.QueryContext(ctx, `SELECT field_name, value
        FROM evolution_skill_profile_strings
       WHERE workspace_id = ? AND skill_name = ? ORDER BY field_name, position`,
		profile.WorkspaceID, profile.SkillName)
	if err != nil {
		return err
	}
	for rows.Next() {
		var field, value string
		if err := rows.Scan(&field, &value); err != nil {
			rows.Close()
			return err
		}
		appendEvolutionProfileString(profile, field, value)
	}
	if err := rows.Close(); err != nil {
		return err
	}
	rows, err = queryer.QueryContext(ctx, `SELECT version_name, action_name, timestamp_unix_nano,
        draft_id, summary, rollback, rollback_reason
      FROM evolution_skill_profile_versions
     WHERE workspace_id = ? AND skill_name = ? ORDER BY position`,
		profile.WorkspaceID, profile.SkillName)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var entry SkillVersionEntry
		var timestamp int64
		var rollback int
		if err := rows.Scan(&entry.Version, &entry.Action, &timestamp, &entry.DraftID,
			&entry.Summary, &rollback, &entry.RollbackReason); err != nil {
			return err
		}
		entry.Timestamp = evolutionStoredTime(timestamp)
		entry.Rollback = rollback == 1
		profile.VersionHistory = append(profile.VersionHistory, entry)
	}
	return rows.Err()
}

func loadEvolutionProfiles(ctx context.Context, queryer evolutionQueryer) ([]SkillProfile, error) {
	rows, err := queryer.QueryContext(ctx, `SELECT workspace_id, skill_name, current_version,
        status, origin, human_summary, change_reason, last_used_at_unix_nano,
        use_count, retention_score
      FROM evolution_skill_profiles ORDER BY skill_name, workspace_id`)
	if err != nil {
		return nil, err
	}
	profiles := make([]SkillProfile, 0)
	indexes := make(map[string]int)
	for rows.Next() {
		var profile SkillProfile
		var status string
		var lastUsed int64
		if err := rows.Scan(&profile.WorkspaceID, &profile.SkillName, &profile.CurrentVersion,
			&status, &profile.Origin, &profile.HumanSummary, &profile.ChangeReason, &lastUsed,
			&profile.UseCount, &profile.RetentionScore); err != nil {
			rows.Close()
			return nil, err
		}
		profile.Status = SkillStatus(status)
		profile.LastUsedAt = evolutionStoredTime(lastUsed)
		indexes[draftKey(profile.WorkspaceID, profile.SkillName)] = len(profiles)
		profiles = append(profiles, profile)
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	rows, err = queryer.QueryContext(ctx, `SELECT s.workspace_id, s.skill_name,
        s.field_name, s.value
      FROM evolution_skill_profile_strings s
      JOIN evolution_skill_profiles p USING(workspace_id, skill_name)
     ORDER BY p.skill_name, p.workspace_id, s.field_name, s.position`)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var workspace, skillName, field, value string
		if err := rows.Scan(&workspace, &skillName, &field, &value); err != nil {
			rows.Close()
			return nil, err
		}
		appendEvolutionProfileString(&profiles[indexes[draftKey(workspace, skillName)]], field, value)
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	rows, err = queryer.QueryContext(ctx, `SELECT v.workspace_id, v.skill_name,
        v.version_name, v.action_name, v.timestamp_unix_nano, v.draft_id,
        v.summary, v.rollback, v.rollback_reason
      FROM evolution_skill_profile_versions v
      JOIN evolution_skill_profiles p USING(workspace_id, skill_name)
     ORDER BY p.skill_name, p.workspace_id, v.position`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var workspace, skillName string
		var entry SkillVersionEntry
		var timestamp int64
		var rollback int
		if err := rows.Scan(&workspace, &skillName, &entry.Version, &entry.Action, &timestamp,
			&entry.DraftID, &entry.Summary, &rollback, &entry.RollbackReason); err != nil {
			return nil, err
		}
		entry.Timestamp = evolutionStoredTime(timestamp)
		entry.Rollback = rollback == 1
		index := indexes[draftKey(workspace, skillName)]
		profiles[index].VersionHistory = append(profiles[index].VersionHistory, entry)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return profiles, nil
}

func appendEvolutionProfileString(profile *SkillProfile, field, value string) {
	switch field {
	case "intended_use_cases":
		profile.IntendedUseCases = append(profile.IntendedUseCases, value)
	case "preferred_entry_path":
		profile.PreferredEntryPath = append(profile.PreferredEntryPath, value)
	case "avoid_patterns":
		profile.AvoidPatterns = append(profile.AvoidPatterns, value)
	}
}

func evolutionStoredTime(nanoseconds int64) time.Time {
	if nanoseconds == 0 {
		return time.Time{}
	}
	return time.Unix(0, nanoseconds).UTC()
}
