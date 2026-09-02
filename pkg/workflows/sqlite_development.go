package workflows

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/sipeed/picoclaw/internal/sqlitestore"
)

func getWorkflowDevelopmentSessionLocked(workspace string) (*WorkflowDevelopmentSession, error) {
	ctx := context.Background()
	db, release, err := borrowWorkflowDatabase(ctx, workspace)
	if err != nil {
		return nil, err
	}
	defer release()
	return loadWorkflowDevelopmentSession(ctx, db, "active")
}

func loadWorkflowDevelopmentSession(
	ctx context.Context,
	query interface {
		QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
	},
	lifecycle string,
) (*WorkflowDevelopmentSession, error) {
	session := &WorkflowDevelopmentSession{}
	var validationJSON, lastTestJSON []byte
	var createdSeconds, createdNanos, updatedSeconds, updatedNanos int64
	err := query.QueryRowContext(ctx, `SELECT session_id,session_revision,draft_revision,
		base_target_revision,reason,status,prompt_text,source_workflow_ref,target_workflow_ref,
		target_picoclaw_version,target_git_commit,yaml_text,validation_json,last_test_json,
		created_at_seconds,created_at_nanosecond,updated_at_seconds,updated_at_nanosecond
		FROM workflow_development_sessions WHERE lifecycle=? ORDER BY updated_at_seconds DESC,
		updated_at_nanosecond DESC,session_id LIMIT 1`, lifecycle).Scan(&session.ID,
		&session.SessionRevision, &session.DraftRevision, &session.BaseTargetRevision,
		&session.Reason, &session.Status, &session.Prompt, &session.SourceWorkflowRef,
		&session.TargetWorkflowRef, &session.TargetPicoclawVersion, &session.TargetGitCommit,
		&session.YAML, &validationJSON, &lastTestJSON, &createdSeconds, &createdNanos,
		&updatedSeconds, &updatedNanos)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	session.CreatedAt = workflowTime(createdSeconds, createdNanos)
	session.UpdatedAt = workflowTime(updatedSeconds, updatedNanos)
	if len(validationJSON) != 0 {
		session.Validation = &WorkflowDevelopmentValidation{}
		if err := decodeWorkflowJSON(validationJSON, session.Validation); err != nil {
			return nil, err
		}
	}
	if len(lastTestJSON) != 0 {
		session.LastTest = &WorkflowDevelopmentTest{}
		if err := decodeWorkflowJSON(lastTestJSON, session.LastTest); err != nil {
			return nil, err
		}
	}
	if session.BaseTargetRevision == "" {
		session.BaseTargetRevision = WorkflowTargetRevisionUnknown
	}
	if session.DraftRevision == "" || session.SessionRevision == "" {
		if err := refreshWorkflowDevelopmentRevisions(session); err != nil {
			return nil, err
		}
	}
	return session, nil
}

//nolint:govet // Transaction-local errors stay scoped to their exact statement.
func writeNewActiveDevelopment(workspace string, session *WorkflowDevelopmentSession) error {
	if session == nil {
		return ErrNoActiveDevelopment
	}
	if err := refreshWorkflowDevelopmentRevisions(session); err != nil {
		return err
	}
	ctx := context.Background()
	db, release, err := borrowWorkflowDatabase(ctx, workspace)
	if err != nil {
		return err
	}
	defer release()
	err = sqlitestore.Immediate(ctx, db, func(conn *sql.Conn) error {
		var active int
		if err := conn.QueryRowContext(ctx, `SELECT COUNT(*) FROM workflow_development_sessions
			WHERE lifecycle='active'`).Scan(&active); err != nil {
			return err
		}
		if active != 0 {
			return ErrActiveDevelopmentExists
		}
		return insertWorkflowDevelopmentSession(ctx, conn, session, "active", false)
	})
	if strings.Contains(strings.ToLower(fmt.Sprint(err)), "unique") {
		return ErrActiveDevelopmentExists
	}
	return err
}

func writeActiveDevelopment(workspace string, session *WorkflowDevelopmentSession) error {
	if session == nil {
		return ErrNoActiveDevelopment
	}
	if err := refreshWorkflowDevelopmentRevisions(session); err != nil {
		return err
	}
	ctx := context.Background()
	db, release, err := borrowWorkflowDatabase(ctx, workspace)
	if err != nil {
		return err
	}
	defer release()
	return sqlitestore.Immediate(ctx, db, func(conn *sql.Conn) error {
		var version int64
		if err := conn.QueryRowContext(ctx, `SELECT version FROM workflow_development_sessions
			WHERE session_id=? AND lifecycle='active'`, session.ID).Scan(&version); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return ErrNoActiveDevelopment
			}
			return err
		}
		return updateWorkflowDevelopmentSession(ctx, conn, session, "active", version)
	})
}

//nolint:govet // Transaction-local errors stay scoped to their exact statement.
func updateWorkflowDevelopmentSession(
	ctx context.Context,
	conn *sql.Conn,
	session *WorkflowDevelopmentSession,
	lifecycle string,
	version int64,
) error {
	validation, err := encodeWorkflowJSON(session.Validation, maximumWorkflowDevelopmentBytes)
	if err != nil {
		return err
	}
	lastTest, err := encodeWorkflowJSON(session.LastTest, maximumWorkflowDevelopmentBytes)
	if err != nil {
		return err
	}
	createdSeconds, createdNanos, err := workflowTimestamp(session.CreatedAt)
	if err != nil {
		return err
	}
	updatedSeconds, updatedNanos, err := workflowTimestamp(session.UpdatedAt)
	if err != nil {
		return err
	}
	var totalBytes, previousBytes int64
	if err := conn.QueryRowContext(ctx, `SELECT
		(SELECT COALESCE(SUM(length(CAST(prompt_text AS BLOB))+length(CAST(yaml_text AS BLOB))+
		 COALESCE(length(validation_json),0)+COALESCE(length(last_test_json),0)),0)
		 FROM workflow_development_sessions),
		length(CAST(prompt_text AS BLOB))+length(CAST(yaml_text AS BLOB))+
		COALESCE(length(validation_json),0)+COALESCE(length(last_test_json),0)
		FROM workflow_development_sessions WHERE session_id=?`, session.ID).Scan(&totalBytes, &previousBytes); err != nil {
		return err
	}
	newBytes := int64(len(session.Prompt) + len(session.YAML) + len(validation) + len(lastTest))
	if newBytes > int64(maximumWorkflowDevelopmentRecords)*maximumWorkflowDevelopmentBytes-
		(totalBytes-previousBytes) {
		return fmt.Errorf("workflow development storage exceeds its aggregate limit")
	}
	result, err := conn.ExecContext(ctx, `UPDATE workflow_development_sessions SET
		lifecycle=?,session_revision=?,draft_revision=?,base_target_revision=?,reason=?,status=?,
		prompt_text=?,source_workflow_ref=?,target_workflow_ref=?,target_picoclaw_version=?,
		target_git_commit=?,yaml_text=?,validation_json=?,last_test_json=?,created_at_seconds=?,
		created_at_nanosecond=?,updated_at_seconds=?,updated_at_nanosecond=?,version=version+1
		WHERE session_id=? AND version=?`, lifecycle, session.SessionRevision,
		session.DraftRevision, session.BaseTargetRevision, session.Reason, session.Status,
		session.Prompt, session.SourceWorkflowRef, session.TargetWorkflowRef,
		session.TargetPicoclawVersion, session.TargetGitCommit, session.YAML, validation,
		lastTest, createdSeconds, createdNanos, updatedSeconds, updatedNanos, session.ID, version)
	if err != nil {
		return err
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if changed != 1 {
		return ErrWorkflowSessionRevisionMismatch
	}
	return nil
}

func archiveDevelopmentSession(workspace string, session *WorkflowDevelopmentSession, state string) error {
	state = strings.ToLower(strings.TrimSpace(state))
	if session == nil {
		return ErrNoActiveDevelopment
	}
	if state != "published" && state != "discarded" {
		return fmt.Errorf("invalid workflow development archive state")
	}
	archived := *session
	archived.Status = state
	archived.UpdatedAt = time.Now().UTC()
	ctx := context.Background()
	db, release, err := borrowWorkflowDatabase(ctx, workspace)
	if err != nil {
		return err
	}
	defer release()
	return sqlitestore.Immediate(ctx, db, func(conn *sql.Conn) error {
		var version int64
		if err := conn.QueryRowContext(ctx, `SELECT version FROM workflow_development_sessions
			WHERE session_id=? AND lifecycle='active'`, archived.ID).Scan(&version); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return ErrNoActiveDevelopment
			}
			return err
		}
		return updateWorkflowDevelopmentSession(ctx, conn, &archived, state, version)
	})
}
