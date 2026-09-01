package workflows

import (
	"bufio"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/sipeed/picoclaw/pkg/sqlitestore"
)

const (
	workflowLegacyRunOrder         = 10
	workflowLegacyPrivateOrder     = 15
	workflowLegacyEventOrder       = 20
	workflowLegacyNativeOrder      = 30
	workflowLegacyManifestOrder    = 40
	workflowLegacyDevelopmentOrder = 50
)

func workflowLegacyID(kind, relative string) string {
	digest := sha256.Sum256([]byte(filepath.ToSlash(relative)))
	return kind + "-" + hex.EncodeToString(digest[:12])
}

func enumerateWorkflowLegacySources(workspace string) ([]sqlitestore.LegacySource, error) {
	var sources []sqlitestore.LegacySource
	add := func(kind, relative string, order int, maximum int64) {
		sources = append(sources, sqlitestore.LegacySource{
			ID: workflowLegacyID(kind, relative), Relative: filepath.ToSlash(relative),
			Order: order, MaxBytes: maximum,
		})
	}
	runRoot := filepath.Join(workspace, "workflow_runs")
	entries, err := readSafeLegacyDirectory(runRoot)
	if err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	for _, entry := range entries {
		if !entry.IsDir() || entry.Type()&os.ModeSymlink != 0 {
			return nil, fmt.Errorf("workflow legacy run root contains an unsafe entry")
		}
		runEntries, err := readSafeLegacyDirectory(filepath.Join(runRoot, entry.Name()))
		if err != nil {
			return nil, err
		}
		for _, runEntry := range runEntries {
			if runEntry.Type()&os.ModeSymlink != 0 || runEntry.IsDir() {
				return nil, fmt.Errorf("workflow legacy run contains an unsafe entry")
			}
			switch runEntry.Name() {
			case "run.json", "events.jsonl", privateRunMarkerFilename:
			default:
				return nil, fmt.Errorf("workflow legacy run contains an unexpected entry")
			}
		}
		runRelative := filepath.Join("workflow_runs", entry.Name(), "run.json")
		markerRelative := filepath.Join("workflow_runs", entry.Name(), privateRunMarkerFilename)
		eventRelative := filepath.Join("workflow_runs", entry.Name(), "events.jsonl")
		for _, candidate := range []struct {
			kind     string
			relative string
			order    int
			maximum  int64
		}{
			{"run", runRelative, workflowLegacyRunOrder, maximumWorkflowLegacySourceBytes},
			{"private", markerRelative, workflowLegacyPrivateOrder, 4096},
			{"events", eventRelative, workflowLegacyEventOrder, maximumWorkflowLegacySourceBytes},
		} {
			if _, statErr := os.Lstat(filepath.Join(workspace, candidate.relative)); statErr == nil {
				add(candidate.kind, candidate.relative, candidate.order, candidate.maximum)
			} else if !os.IsNotExist(statErr) {
				return nil, statErr
			}
		}
	}

	if err := enumerateWorkflowJSONTree(workspace, workflowStateDir, 2, func(relative string) {
		base := filepath.Base(relative)
		if base == workflowDevelopmentPublishJournalFile || base == workflowTemplateInstallJournalFile {
			return
		}
		add("native", relative, workflowLegacyNativeOrder, maximumWorkflowNativeValueBytes)
	}); err != nil {
		return nil, err
	}
	manifest := filepath.Join(compatibilityManifestDir, compatibilityManifest)
	if _, err := os.Lstat(filepath.Join(workspace, manifest)); err == nil {
		add("manifest", manifest, workflowLegacyManifestOrder, maximumWorkflowManifestBytes)
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	active := filepath.Join(workflowDevelopmentDir, workflowDevelopmentActive)
	if _, err := os.Lstat(filepath.Join(workspace, active)); err == nil {
		add("development", active, workflowLegacyDevelopmentOrder, maximumWorkflowDevelopmentBytes)
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	if err := enumerateWorkflowJSONTree(workspace, filepath.Join(workflowDevelopmentDir, "archive"), 1,
		func(relative string) {
			add("development", relative, workflowLegacyDevelopmentOrder+1, maximumWorkflowDevelopmentBytes)
		}); err != nil {
		return nil, err
	}
	sort.Slice(sources, func(i, j int) bool {
		if sources[i].Order != sources[j].Order {
			return sources[i].Order < sources[j].Order
		}
		return sources[i].Relative < sources[j].Relative
	})
	return sources, nil
}

func readSafeLegacyDirectory(path string) ([]os.DirEntry, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0o022 != 0 {
		return nil, fmt.Errorf("workflow legacy directory is unsafe")
	}
	return os.ReadDir(path)
}

func enumerateWorkflowJSONTree(
	workspace, relativeRoot string,
	maximumDepth int,
	add func(string),
) error {
	root := filepath.Join(workspace, relativeRoot)
	if _, err := os.Lstat(root); os.IsNotExist(err) {
		return nil
	} else if err != nil {
		return err
	}
	return filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("workflow legacy tree contains a symlink")
		}
		relative, err := filepath.Rel(workspace, path)
		if err != nil {
			return err
		}
		depth := len(strings.Split(filepath.ToSlash(relative), "/")) -
			len(strings.Split(filepath.ToSlash(relativeRoot), "/"))
		if entry.IsDir() {
			info, err := entry.Info()
			if err != nil {
				return err
			}
			if info.Mode().Perm()&0o022 != 0 {
				return fmt.Errorf("workflow legacy directory is writable")
			}
			if depth > maximumDepth {
				return filepath.SkipDir
			}
			return nil
		}
		if depth > maximumDepth || !strings.HasSuffix(strings.ToLower(entry.Name()), ".json") {
			return nil
		}
		add(relative)
		return nil
	})
}

func importWorkflowLegacySource(
	ctx context.Context,
	conn *sql.Conn,
	input sqlitestore.LegacyInput,
) (sqlitestore.ImportResult, error) {
	relative := filepath.ToSlash(input.Relative)
	switch {
	case strings.HasPrefix(relative, "workflow_runs/") && strings.HasSuffix(relative, "/run.json"):
		return importWorkflowLegacyRun(ctx, conn, input)
	case strings.HasPrefix(relative, "workflow_runs/") && strings.HasSuffix(relative, "/events.jsonl"):
		return importWorkflowLegacyEvents(ctx, conn, input)
	case strings.HasPrefix(relative, "workflow_runs/") && strings.HasSuffix(relative, "/"+privateRunMarkerFilename):
		return importWorkflowLegacyMarker(ctx, conn, input)
	case strings.HasPrefix(relative, workflowStateDir+"/"):
		return importWorkflowLegacyNativeState(ctx, conn, input)
	case relative == compatibilityManifestDir+"/"+compatibilityManifest:
		return importWorkflowLegacyManifest(ctx, conn, input)
	case relative == workflowDevelopmentDir+"/"+workflowDevelopmentActive ||
		strings.HasPrefix(relative, workflowDevelopmentDir+"/archive/"):
		return importWorkflowLegacyDevelopment(ctx, conn, input)
	default:
		return sqlitestore.ImportResult{}, errors.New("unknown workflow legacy source")
	}
}

func workflowImportIssue(code string, data []byte) sqlitestore.ImportIssue {
	return sqlitestore.ImportIssue{Code: code, RecordDigest: sha256.Sum256(data)}
}

func skippedWorkflowImport(code string, data []byte) sqlitestore.ImportResult {
	return sqlitestore.ImportResult{Skipped: 1, Issues: []sqlitestore.ImportIssue{workflowImportIssue(code, data)}}
}

//nolint:nilerr // Malformed legacy records are audited skips, not subsystem failures.
func importWorkflowLegacyRun(
	ctx context.Context,
	conn *sql.Conn,
	input sqlitestore.LegacyInput,
) (sqlitestore.ImportResult, error) {
	run, _, err := decodeRunWithExactEventFields(input.Data)
	if err != nil || run == nil {
		return skippedWorkflowImport("invalid_run_json", input.Data), nil
	}
	parts := strings.Split(filepath.ToSlash(input.Relative), "/")
	if len(parts) != 3 || safeID(run.ID) != parts[1] {
		return skippedWorkflowImport("invalid_run_identity", input.Data), nil
	}
	if err := prepareWorkflowRun(run); err != nil {
		return skippedWorkflowImport("invalid_run_record", input.Data), nil
	}
	if _, _, err := getWorkflowRunConn(ctx, conn, run.ID); err == nil {
		return skippedWorkflowImport("duplicate_run_identity", input.Data), nil
	} else if !os.IsNotExist(err) {
		return sqlitestore.ImportResult{}, err
	}
	if err := insertWorkflowRunConn(ctx, conn, run); err != nil {
		return skippedWorkflowImport("invalid_run_record", input.Data), nil
	}
	return sqlitestore.ImportResult{Imported: 1}, nil
}

func importWorkflowLegacyMarker(
	ctx context.Context,
	conn *sql.Conn,
	input sqlitestore.LegacyInput,
) (sqlitestore.ImportResult, error) {
	if string(input.Data) != privateRunMarkerContents {
		return skippedWorkflowImport("invalid_private_marker", input.Data), nil
	}
	parts := strings.Split(filepath.ToSlash(input.Relative), "/")
	if len(parts) != 3 {
		return skippedWorkflowImport("invalid_private_marker_identity", input.Data), nil
	}
	var private int
	err := conn.QueryRowContext(ctx, `SELECT is_private FROM workflow_runs WHERE run_id=?`, parts[1]).Scan(&private)
	if errors.Is(err, sql.ErrNoRows) {
		return sqlitestore.ImportResult{}, fmt.Errorf("legacy private marker has no matching private run")
	}
	if err != nil {
		return sqlitestore.ImportResult{}, err
	}
	if private != 1 {
		return sqlitestore.ImportResult{}, fmt.Errorf("legacy private marker has no matching private run")
	}
	return sqlitestore.ImportResult{Imported: 1}, nil
}

func importWorkflowLegacyEvents(
	ctx context.Context,
	conn *sql.Conn,
	input sqlitestore.LegacyInput,
) (sqlitestore.ImportResult, error) {
	parts := strings.Split(filepath.ToSlash(input.Relative), "/")
	if len(parts) != 3 {
		return skippedWorkflowImport("invalid_event_source", input.Data), nil
	}
	scanner := bufio.NewScanner(strings.NewReader(string(input.Data)))
	buffer := make([]byte, 64<<10)
	scanner.Buffer(buffer, int(maximumWorkflowLegacySourceBytes))
	result := sqlitestore.ImportResult{}
	for scanner.Scan() {
		line := append([]byte(nil), scanner.Bytes()...)
		if len(strings.TrimSpace(string(line))) == 0 {
			continue
		}
		var event RunEvent
		if err := decodeWorkflowJSON(line, &event); err != nil || event.RunID == "" || safeID(event.RunID) != parts[1] {
			result.Skipped++
			if len(result.Issues) < 512 {
				result.Issues = append(result.Issues, workflowImportIssue("invalid_event_line", line))
			}
			continue
		}
		if err := appendWorkflowEventConn(ctx, conn, event); err != nil {
			result.Skipped++
			if len(result.Issues) < 512 {
				result.Issues = append(result.Issues, workflowImportIssue("orphan_event_line", line))
			}
			continue
		}
		result.Imported++
	}
	if err := scanner.Err(); err != nil {
		return sqlitestore.ImportResult{}, err
	}
	return result, nil
}

//nolint:nilerr // Malformed legacy records are audited skips, not subsystem failures.
func importWorkflowLegacyNativeState(
	ctx context.Context,
	conn *sql.Conn,
	input sqlitestore.LegacyInput,
) (sqlitestore.ImportResult, error) {
	var envelope nativeStateEnvelope
	if err := decodeWorkflowJSON(input.Data, &envelope); err != nil || strings.TrimSpace(envelope.Key) == "" {
		return skippedWorkflowImport("invalid_native_state", input.Data), nil
	}
	parts := strings.Split(filepath.ToSlash(input.Relative), "/")
	if len(parts) != 3 {
		return skippedWorkflowImport("invalid_native_identity", input.Data), nil
	}
	namespaceID := parts[1]
	keyID := strings.TrimSuffix(parts[2], filepath.Ext(parts[2]))
	if safeStorageSegment(envelope.Key) != keyID {
		return skippedWorkflowImport("invalid_native_identity", input.Data), nil
	}
	valueJSON, err := encodeWorkflowJSON(envelope.Value, maximumWorkflowNativeValueBytes)
	if err != nil {
		return skippedWorkflowImport("invalid_native_value", input.Data), nil
	}
	if valueJSON == nil {
		valueJSON = []byte("null")
	}
	seconds, nanos, err := workflowTimestamp(envelope.UpdatedAt)
	if err != nil {
		return skippedWorkflowImport("invalid_native_timestamp", input.Data), nil
	}
	result, err := conn.ExecContext(ctx, `INSERT OR IGNORE INTO workflow_native_state
		(namespace_id,key_id,key_text,value_json,updated_at_seconds,updated_at_nanosecond,version)
		VALUES(?,?,?,?,?,?,1)`, namespaceID, keyID, envelope.Key, valueJSON, seconds, nanos)
	if err != nil {
		return sqlitestore.ImportResult{}, err
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return sqlitestore.ImportResult{}, err
	}
	if changed == 0 {
		return skippedWorkflowImport("duplicate_native_identity", input.Data), nil
	}
	return sqlitestore.ImportResult{Imported: 1}, nil
}

//nolint:nilerr // Malformed legacy records are audited skips, not subsystem failures.
func importWorkflowLegacyManifest(
	ctx context.Context,
	conn *sql.Conn,
	input sqlitestore.LegacyInput,
) (sqlitestore.ImportResult, error) {
	var manifest WorkflowCompatibilityManifest
	if err := decodeWorkflowJSON(input.Data, &manifest); err != nil || manifest.Workflows == nil {
		return skippedWorkflowImport("invalid_validation_manifest", input.Data), nil
	}
	seconds, nanos, err := workflowTimestamp(manifest.UpdatedAt)
	if err != nil {
		return skippedWorkflowImport("invalid_validation_timestamp", input.Data), nil
	}
	if _, err := conn.ExecContext(ctx, `INSERT INTO workflow_compatibility_runtime
		(singleton,picoclaw_version,git_commit,workflow_engine,workflow_schema,
		validator_fingerprint,updated_at_seconds,updated_at_nanosecond,version)
		VALUES(1,?,?,?,?,?,?,?,1) ON CONFLICT(singleton) DO NOTHING`, manifest.PicoclawVersion,
		manifest.GitCommit, manifest.WorkflowEngine, manifest.WorkflowSchema,
		manifest.ValidatorFingerprint, seconds, nanos); err != nil {
		return sqlitestore.ImportResult{}, err
	}
	result := sqlitestore.ImportResult{Imported: 1}
	refs := sortedStringKeys(manifest.Workflows)
	for _, ref := range refs {
		stamp := manifest.Workflows[ref]
		if stamp.WorkflowRef == "" {
			stamp.WorkflowRef = ref
		}
		if stamp.WorkflowRef != ref {
			result.Skipped++
			result.Issues = append(result.Issues, workflowImportIssue("invalid_validation_identity", []byte(ref)))
			continue
		}
		if err := insertWorkflowValidationStamp(ctx, conn, stamp); err != nil {
			result.Skipped++
			result.Issues = append(result.Issues, workflowImportIssue("invalid_validation_stamp", []byte(ref)))
			continue
		}
		result.Imported++
	}
	return result, nil
}

func insertWorkflowValidationStamp(ctx context.Context, conn *sql.Conn, stamp WorkflowValidationStamp) error {
	if len(stamp.Errors) > maximumWorkflowIssuesPerStamp ||
		len(stamp.Warnings) > maximumWorkflowIssuesPerStamp-len(stamp.Errors) {
		return errors.New("workflow validation issue collection exceeds its limit")
	}
	seconds, nanos, err := workflowTimestamp(stamp.ValidatedAt)
	if err != nil {
		return err
	}
	if _, err := conn.ExecContext(ctx, `INSERT OR IGNORE INTO workflow_validation_stamps
		(workflow_ref,workflow_hash,picoclaw_version,git_commit,workflow_engine,workflow_schema,
		validator_fingerprint,status,validated_at_seconds,validated_at_nanosecond)
		VALUES(?,?,?,?,?,?,?,?,?,?)`, stamp.WorkflowRef, stamp.WorkflowHash, stamp.PicoclawVersion,
		stamp.GitCommit, stamp.WorkflowEngine, stamp.WorkflowSchema, stamp.ValidatorFingerprint,
		stamp.Status, seconds, nanos); err != nil {
		return err
	}
	for kind, issues := range map[string][]WorkflowValidationIssue{"error": stamp.Errors, "warning": stamp.Warnings} {
		for position, issue := range issues {
			if _, err := conn.ExecContext(ctx, `INSERT INTO workflow_validation_issues
				(workflow_ref,issue_kind,position,path_text,message) VALUES(?,?,?,?,?)`,
				stamp.WorkflowRef, kind, position, issue.Path, issue.Message); err != nil {
				return err
			}
		}
	}
	return nil
}

//nolint:nilerr // Malformed legacy records are audited skips, not subsystem failures.
func importWorkflowLegacyDevelopment(
	ctx context.Context,
	conn *sql.Conn,
	input sqlitestore.LegacyInput,
) (sqlitestore.ImportResult, error) {
	var session WorkflowDevelopmentSession
	if err := decodeWorkflowJSON(input.Data, &session); err != nil || session.ID == "" {
		return skippedWorkflowImport("invalid_development_session", input.Data), nil
	}
	lifecycle := "active"
	if strings.Contains(filepath.ToSlash(input.Relative), "/archive/") {
		lifecycle = strings.ToLower(strings.TrimSpace(session.Status))
		if lifecycle != "published" && lifecycle != "discarded" {
			return skippedWorkflowImport("invalid_development_lifecycle", input.Data), nil
		}
	}
	if err := insertWorkflowDevelopmentSession(ctx, conn, &session, lifecycle, false); err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "unique") {
			return skippedWorkflowImport("duplicate_development_identity", input.Data), nil
		}
		return skippedWorkflowImport("invalid_development_session", input.Data), nil
	}
	return sqlitestore.ImportResult{Imported: 1}, nil
}

//nolint:govet // Import query errors stay scoped to their exact statement.
func insertWorkflowDevelopmentSession(
	ctx context.Context,
	conn *sql.Conn,
	session *WorkflowDevelopmentSession,
	lifecycle string,
	replace bool,
) error {
	validation, err := encodeWorkflowJSON(session.Validation, maximumWorkflowDevelopmentBytes)
	if err != nil {
		return err
	}
	lastTest, err := encodeWorkflowJSON(session.LastTest, maximumWorkflowDevelopmentBytes)
	if err != nil {
		return err
	}
	createdSec, createdNano, err := workflowTimestamp(session.CreatedAt)
	if err != nil {
		return err
	}
	updatedSec, updatedNano, err := workflowTimestamp(session.UpdatedAt)
	if err != nil {
		return err
	}
	var recordCount int
	var totalBytes int64
	if err := conn.QueryRowContext(ctx, `SELECT COUNT(*),COALESCE(SUM(
		length(CAST(prompt_text AS BLOB))+length(CAST(yaml_text AS BLOB))+
		COALESCE(length(validation_json),0)+COALESCE(length(last_test_json),0)),0)
		FROM workflow_development_sessions`).Scan(&recordCount, &totalBytes); err != nil {
		return err
	}
	newBytes := int64(len(session.Prompt) + len(session.YAML) + len(validation) + len(lastTest))
	if recordCount >= maximumWorkflowDevelopmentRecords ||
		newBytes > int64(maximumWorkflowDevelopmentRecords)*maximumWorkflowDevelopmentBytes-totalBytes {
		return fmt.Errorf("workflow development storage exceeds its aggregate limit")
	}
	verb := "INSERT"
	if replace {
		verb = "INSERT OR REPLACE"
	}
	_, err = conn.ExecContext(ctx, verb+` INTO workflow_development_sessions
		(session_id,lifecycle,session_revision,draft_revision,base_target_revision,reason,status,
		prompt_text,source_workflow_ref,target_workflow_ref,target_picoclaw_version,target_git_commit,
		yaml_text,validation_json,last_test_json,created_at_seconds,created_at_nanosecond,
		updated_at_seconds,updated_at_nanosecond,version) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,1)`,
		session.ID, lifecycle, session.SessionRevision, session.DraftRevision,
		session.BaseTargetRevision, session.Reason, session.Status, session.Prompt,
		session.SourceWorkflowRef, session.TargetWorkflowRef, session.TargetPicoclawVersion,
		session.TargetGitCommit, session.YAML, validation, lastTest, createdSec, createdNano,
		updatedSec, updatedNano)
	return err
}
