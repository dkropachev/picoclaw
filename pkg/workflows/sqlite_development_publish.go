package workflows

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"time"

	"github.com/sipeed/picoclaw/pkg/fileutil"
	"github.com/sipeed/picoclaw/pkg/sqlitestore"
)

//nolint:govet // Each publish phase keeps errors scoped to its exact boundary.
func publishWorkflowDevelopmentTransaction(
	ctx context.Context,
	workspace string,
	request *WorkflowDevelopmentPublishRequest,
	runtime RuntimeCompatibility,
	gate WorkflowDevelopmentPublishGate,
	hooks *workflowDevelopmentPublishHooks,
	opts ...LocalOption,
) (*WorkflowDevelopmentPublishResult, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	unlock, err := lockWorkflowMutation(workspace)
	if err != nil {
		return nil, err
	}
	defer unlock()
	session, err := requireActiveDevelopment(workspace)
	if err != nil {
		return nil, err
	}
	currentTargetRevision, err := captureWorkflowDevelopmentTargetRevision(
		workspace, session.TargetWorkflowRef, opts...,
	)
	if err != nil {
		return nil, err
	}
	if request == nil {
		if session.BaseTargetRevision == WorkflowTargetRevisionUnknown {
			session.BaseTargetRevision = currentTargetRevision
			session.UpdatedAt = time.Now().UTC()
			if err := writeActiveDevelopment(workspace, session); err != nil {
				return nil, err
			}
		}
		request = &WorkflowDevelopmentPublishRequest{
			SessionID: session.ID, ExpectedSessionRevision: session.SessionRevision,
			ExpectedDraftRevision:      session.DraftRevision,
			ExpectedBaseTargetRevision: session.BaseTargetRevision,
		}
	}
	if err := checkWorkflowDevelopmentPublishRevisions(session, *request, currentTargetRevision); err != nil {
		return nil, err
	}
	draftBytes := []byte(session.YAML)
	workflow, err := Parse(draftBytes)
	if err != nil {
		return nil, fmt.Errorf("%w: parse exact workflow draft: %v", ErrWorkflowDevelopmentDraftNotReady, err)
	}
	if err := Validate(workflow); err != nil {
		return nil, fmt.Errorf("%w: validate exact workflow draft: %v", ErrWorkflowDevelopmentDraftNotReady, err)
	}
	if err := requireCurrentSuccessfulDevelopmentTest(session); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrWorkflowDevelopmentDraftNotReady, err)
	}
	if err := checkWorkflowDevelopmentPublishGate(ctx, *request, session, workflow, gate); err != nil {
		return nil, err
	}
	local := collectLocalOptions(opts...)
	definitionsDir, err := cleanDefinitionsDir(local.DefinitionsDir)
	if err != nil {
		return nil, err
	}
	resolved, err := local.resolver(workspace).ResolveLocal(session.TargetWorkflowRef)
	if err != nil {
		return nil, err
	}
	previousManifest, manifestMissing, err := readCompatibilityManifest(workspace)
	if err != nil {
		return nil, err
	}
	manifest, err := buildCompatibilityManifestLocked(ctx, workspace, runtime,
		&workflowCompatibilityOverlay{ref: resolved.Canonical, data: draftBytes}, opts...)
	if err != nil {
		return nil, err
	}
	if !templateHasValidCompatibilityStamp(manifest, resolved.Canonical) {
		return nil, fmt.Errorf("published workflow did not receive a valid compatibility stamp")
	}
	latest, err := requireActiveDevelopment(workspace)
	if err != nil {
		return nil, err
	}
	latestRevision, err := captureWorkflowDevelopmentTargetRevision(
		workspace, latest.TargetWorkflowRef, opts...,
	)
	if err != nil {
		return nil, err
	}
	if err := checkWorkflowDevelopmentPublishRevisions(latest, *request, latestRevision); err != nil {
		return nil, err
	}
	if err := checkWorkflowDevelopmentPublishGate(ctx, *request, latest, workflow, gate); err != nil {
		return nil, err
	}
	targetSnapshot, err := captureWorkflowTemplateFile(resolved.Path)
	if err != nil {
		return nil, err
	}
	if workflowDevelopmentPublishSnapshotRevision(targetSnapshot) != session.BaseTargetRevision {
		return nil, ErrWorkflowTargetRevisionMismatch
	}
	manifestPre, err := workflowDatabaseSnapshot(previousManifest, !manifestMissing)
	if err != nil {
		return nil, err
	}
	manifestPost, err := workflowDatabaseSnapshot(manifest, true)
	if err != nil {
		return nil, err
	}
	activePre, err := workflowDatabaseSnapshot(session, true)
	if err != nil {
		return nil, err
	}
	archived := *session
	archived.Status = "published"
	archived.UpdatedAt = time.Now().UTC()
	archivePost, err := workflowDatabaseSnapshot(&archived, true)
	if err != nil {
		return nil, err
	}
	targetMode := fs.FileMode(0o644)
	if targetSnapshot.exists {
		targetMode = targetSnapshot.mode
	}
	journal := &workflowDevelopmentPublishJournal{
		Version:        workflowDevelopmentPublishJournalVersion,
		Phase:          workflowDevelopmentPublishPhasePrepared,
		Stage:          workflowDevelopmentPublishStagePrepared,
		DefinitionsDir: definitionsDir, TargetRef: resolved.Canonical, SessionID: session.ID,
		Target: workflowDevelopmentPublishTransition(targetSnapshot, workflowTemplateFileSnapshot{
			path: resolved.Path, exists: true, data: draftBytes, mode: targetMode,
		}),
		Manifest: workflowDevelopmentPublishFileTransition{Preimage: manifestPre, Postimage: manifestPost},
		Active:   workflowDevelopmentPublishFileTransition{Preimage: activePre},
		Archive:  workflowDevelopmentPublishFileTransition{Postimage: archivePost},
	}
	writeJournal := workflowDevelopmentPublishJournalWriter(writeWorkflowDevelopmentPublishJournal)
	if hooks != nil && hooks.writeJournal != nil {
		writeJournal = hooks.writeJournal
	}
	if err := writeJournal(workspace, journal); err != nil {
		return nil, err
	}
	fail := func(cause error) (*WorkflowDevelopmentPublishResult, error) {
		if hooks != nil && hooks.leaveJournalOnError {
			return nil, cause
		}
		if recoveryErr := recoverWorkflowDevelopmentPublishTransaction(workspace); recoveryErr != nil {
			return nil, errors.Join(cause, ErrWorkflowDevelopmentPublishRollbackFailed, recoveryErr)
		}
		return nil, cause
	}
	boundary := func(value workflowDevelopmentPublishBoundary) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if hooks != nil && hooks.afterBoundary != nil {
			return hooks.afterBoundary(value)
		}
		return nil
	}
	if err := boundary(workflowDevelopmentPublishBoundaryPrepared); err != nil {
		return fail(err)
	}
	journal.Stage = workflowDevelopmentPublishStageTargetWriteStarted
	if err := writeJournal(workspace, journal); err != nil {
		return fail(err)
	}
	if err := fileutil.MkdirAllDurable(filepath.Dir(resolved.Path), 0o755); err != nil {
		return fail(err)
	}
	if err := writeWorkflowTemplateAtomic(resolved.Path, draftBytes, targetMode); err != nil {
		return fail(err)
	}
	if err := boundary(workflowDevelopmentPublishBoundaryTargetWritten); err != nil {
		return fail(err)
	}
	journal.Stage = workflowDevelopmentPublishStageManifestWriteStarted
	if err := writeJournal(workspace, journal); err != nil {
		return fail(err)
	}
	if err := applyWorkflowDevelopmentPublishDatabase(ctx, workspace, session, manifest); err != nil {
		return fail(err)
	}
	if err := boundary(workflowDevelopmentPublishBoundaryManifestActivated); err != nil {
		return fail(err)
	}
	journal.Stage = workflowDevelopmentPublishStageArchiveWriteStarted
	if err := writeJournal(workspace, journal); err != nil {
		return fail(err)
	}
	if err := boundary(workflowDevelopmentPublishBoundarySessionArchived); err != nil {
		return fail(err)
	}
	journal.Stage = workflowDevelopmentPublishStageActiveRemoveStarted
	if err := writeJournal(workspace, journal); err != nil {
		return fail(err)
	}
	if err := boundary(workflowDevelopmentPublishBoundaryActiveRemoved); err != nil {
		return fail(err)
	}
	journal.Phase = workflowDevelopmentPublishPhaseCommitted
	journal.Stage = workflowDevelopmentPublishStageCommitted
	if err := writeWorkflowDevelopmentPublishCommitWithRetry(workspace, journal, writeJournal); err != nil {
		return nil, err
	}
	if err := boundary(workflowDevelopmentPublishBoundaryCommitted); err != nil {
		if hooks != nil && hooks.leaveJournalOnError {
			return nil, err
		}
		_ = removeWorkflowDevelopmentPublishJournal(workspace)
		return &WorkflowDevelopmentPublishResult{WorkflowRef: session.TargetWorkflowRef, Session: session}, nil
	}
	_ = removeWorkflowDevelopmentPublishJournal(workspace)
	return &WorkflowDevelopmentPublishResult{WorkflowRef: session.TargetWorkflowRef, Session: session}, nil
}

func workflowDatabaseSnapshot(value any, exists bool) (workflowDevelopmentPublishFileSnapshot, error) {
	if !exists {
		return workflowDevelopmentPublishFileSnapshot{}, nil
	}
	data, err := json.Marshal(value)
	if err != nil {
		return workflowDevelopmentPublishFileSnapshot{}, err
	}
	if len(data) > int(maximumWorkflowManifestBytes) {
		return workflowDevelopmentPublishFileSnapshot{}, fmt.Errorf(
			"workflow database journal snapshot exceeds its limit",
		)
	}
	return workflowDevelopmentPublishFileSnapshot{Exists: true, Data: data, Mode: 0o600}, nil
}

func applyWorkflowDevelopmentPublishDatabase(
	ctx context.Context,
	workspace string,
	session *WorkflowDevelopmentSession,
	manifest *WorkflowCompatibilityManifest,
) error {
	db, release, err := borrowWorkflowDatabase(ctx, workspace)
	if err != nil {
		return err
	}
	defer release()
	return sqlitestore.Immediate(ctx, db, func(conn *sql.Conn) error {
		current, err := loadWorkflowDevelopmentSession(ctx, conn, "active")
		if err != nil {
			return err
		}
		if current == nil || current.ID != session.ID ||
			current.SessionRevision != session.SessionRevision ||
			current.DraftRevision != session.DraftRevision {
			return ErrWorkflowSessionRevisionMismatch
		}
		if err := writeCompatibilityManifestConn(ctx, conn, manifest); err != nil {
			return err
		}
		var version int64
		if err := conn.QueryRowContext(ctx, `SELECT version FROM workflow_development_sessions
			WHERE session_id=? AND lifecycle='active'`, session.ID).Scan(&version); err != nil {
			return err
		}
		archived := *session
		archived.Status = "published"
		archived.UpdatedAt = time.Now().UTC()
		return updateWorkflowDevelopmentSession(ctx, conn, &archived, "published", version)
	})
}

//nolint:govet // Recovery boundary errors remain locally scoped.
func recoverWorkflowDevelopmentPublishTransaction(workspace string) error {
	journal, missing, err := readWorkflowDevelopmentPublishJournal(workspace)
	if err != nil {
		return errors.Join(ErrWorkflowDevelopmentPublishRecoveryFailed, err)
	}
	if missing {
		return nil
	}
	if journal.Version == workflowDevelopmentPublishLegacyJournalVersion {
		return recoverWorkflowDevelopmentPublishFileTransactionLegacy(workspace)
	}
	if journal.Phase == workflowDevelopmentPublishPhaseCommitted {
		if err := removeWorkflowDevelopmentPublishJournal(workspace); err != nil {
			return errors.Join(ErrWorkflowDevelopmentPublishRecoveryFailed, err)
		}
		return nil
	}
	resolver := Resolver{
		WorkspaceDir:   workspace,
		DefinitionsDir: journal.DefinitionsDir,
	}
	resolved, err := resolver.ResolveLocal(journal.TargetRef)
	if err != nil {
		return errors.Join(ErrWorkflowDevelopmentPublishRecoveryFailed, err)
	}
	if err := recoverWorkflowPublishDatabasePreimage(workspace, journal); err != nil {
		return errors.Join(ErrWorkflowDevelopmentPublishRecoveryFailed, err)
	}
	if err := recoverWorkflowFileTransitions(workflowFileRecoveryTransition{
		label: "published workflow target", path: resolved.Path,
		preimage:  journal.Target.Preimage.fileSnapshot(resolved.Path),
		postimage: journal.Target.Postimage.fileSnapshot(resolved.Path),
	}); err != nil {
		return errors.Join(ErrWorkflowDevelopmentPublishRecoveryFailed, err)
	}
	if err := removeWorkflowDevelopmentPublishJournal(workspace); err != nil {
		return errors.Join(ErrWorkflowDevelopmentPublishRecoveryFailed, err)
	}
	return nil
}

//nolint:govet // Recovery statement errors stay scoped to their exact boundary.
func recoverWorkflowPublishDatabasePreimage(
	workspace string,
	journal *workflowDevelopmentPublishJournal,
) error {
	var preManifest, postManifest WorkflowCompatibilityManifest
	if journal.Manifest.Preimage.Exists {
		if err := json.Unmarshal(journal.Manifest.Preimage.Data, &preManifest); err != nil {
			return err
		}
	}
	if journal.Manifest.Postimage.Exists {
		if err := json.Unmarshal(journal.Manifest.Postimage.Data, &postManifest); err != nil {
			return err
		}
	}
	var active WorkflowDevelopmentSession
	if !journal.Active.Preimage.Exists || json.Unmarshal(journal.Active.Preimage.Data, &active) != nil {
		return fmt.Errorf("workflow publish journal active snapshot is invalid")
	}
	ctx := context.Background()
	db, release, err := borrowWorkflowDatabase(ctx, workspace)
	if err != nil {
		return err
	}
	defer release()
	return sqlitestore.Immediate(ctx, db, func(conn *sql.Conn) error {
		var lifecycle string
		var version int64
		err := conn.QueryRowContext(ctx, `SELECT lifecycle,version FROM workflow_development_sessions
			WHERE session_id=?`, journal.SessionID).Scan(&lifecycle, &version)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if err == nil && lifecycle != "active" && lifecycle != "published" {
			return fmt.Errorf("workflow development database changed after interrupted publish")
		}
		if errors.Is(err, sql.ErrNoRows) {
			if err := insertWorkflowDevelopmentSession(ctx, conn, &active, "active", false); err != nil {
				return err
			}
		} else if lifecycle == "published" {
			if err := updateWorkflowDevelopmentSession(ctx, conn, &active, "active", version); err != nil {
				return err
			}
		}
		if journal.Manifest.Preimage.Exists {
			return writeCompatibilityManifestConn(ctx, conn, &preManifest)
		}
		if _, err := conn.ExecContext(ctx, `DELETE FROM workflow_compatibility_runtime`); err != nil {
			return err
		}
		_, err = conn.ExecContext(ctx, `DELETE FROM workflow_validation_stamps`)
		return err
	})
}
