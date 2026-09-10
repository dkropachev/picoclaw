package databasemigration

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

const (
	backupStatusHelperMarker  = "PICOCLAW_BACKUP_STATUS_HELPER"
	backupStatusHelperRoot    = "PICOCLAW_BACKUP_STATUS_ROOT"
	backupStatusHelperReady   = "PICOCLAW_BACKUP_STATUS_READY"
	backupStatusHelperGate    = "PICOCLAW_BACKUP_STATUS_GATE"
	backupStatusHelperOutcome = "PICOCLAW_BACKUP_STATUS_OUTCOME"
)

func TestBackupStatusSubprocessHelper(t *testing.T) {
	if os.Getenv(backupStatusHelperMarker) != "1" {
		return
	}
	root := os.Getenv(backupStatusHelperRoot)
	ready := os.Getenv(backupStatusHelperReady)
	gate := os.Getenv(backupStatusHelperGate)
	outcome := os.Getenv(backupStatusHelperOutcome)
	if root == "" || ready == "" || gate == "" || outcome == "" {
		t.Fatal("backup status helper environment is incomplete")
	}
	session, err := loadBackupSession(t.Context(), root)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(ready, []byte("ready"), 0o600); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		if _, err := os.Lstat(gate); err == nil {
			break
		} else if !errors.Is(err, os.ErrNotExist) {
			t.Fatal(err)
		}
		if time.Now().After(deadline) {
			t.Fatal("backup status helper gate timed out")
		}
		time.Sleep(5 * time.Millisecond)
	}
	var cause error
	if outcome == "failed" {
		cause = errors.New("migration failed")
	}
	if err := session.finish(outcome, cause); err != nil {
		t.Fatal(err)
	}
}

func TestBackupStatusSubprocessRacesCommitOneWriter(t *testing.T) {
	if os.Getenv(backupStatusHelperMarker) == "1" {
		t.Skip("helper process")
	}
	t.Run("revision one no-replace", func(t *testing.T) {
		session := newStatusStateSession(t)
		runBackupStatusProcessRace(
			t, session.root, []string{"migration_in_progress", "migration_in_progress"},
		)
		loaded, err := loadBackupSession(t.Context(), session.root)
		if err != nil {
			t.Fatal(err)
		}
		status, exists, err := loaded.readMigrationStatus()
		if err != nil || !exists || status.Revision != 1 ||
			status.Outcome != "migration_in_progress" {
			t.Fatalf("revision-one race status = %#v, %t, %v", status, exists, err)
		}
	})

	t.Run("terminal exchange", func(t *testing.T) {
		session := newStatusStateSession(t)
		if err := session.finish("migration_in_progress", nil); err != nil {
			t.Fatal(err)
		}
		runBackupStatusProcessRace(t, session.root, []string{"complete", "dry_run"})
		loaded, err := loadBackupSession(t.Context(), session.root)
		if err != nil {
			t.Fatal(err)
		}
		status, exists, err := loaded.readMigrationStatus()
		if err != nil || !exists || status.Revision != 2 ||
			status.Outcome != "complete" && status.Outcome != "dry_run" {
			t.Fatalf("terminal race status = %#v, %t, %v", status, exists, err)
		}
	})
}

func runBackupStatusProcessRace(t *testing.T, root string, outcomes []string) {
	t.Helper()
	if len(outcomes) != 2 {
		t.Fatal("backup status process race requires two outcomes")
	}
	barrier := t.TempDir()
	gate := filepath.Join(barrier, "gate")
	processCtx, cancelProcesses := context.WithTimeout(t.Context(), 20*time.Second)
	commands := make([]*exec.Cmd, len(outcomes))
	waited := make([]bool, len(outcomes))
	t.Cleanup(func() {
		cancelProcesses()
		for index, command := range commands {
			if command == nil || command.Process == nil || waited[index] {
				continue
			}
			_ = command.Process.Kill()
			_ = command.Wait()
		}
	})
	readyPaths := make([]string, len(outcomes))
	for index, outcome := range outcomes {
		readyPaths[index] = filepath.Join(barrier, "ready-"+string(rune('a'+index)))
		command := exec.CommandContext(
			processCtx,
			os.Args[0], "-test.run=^TestBackupStatusSubprocessHelper$",
		)
		command.Env = append(
			os.Environ(),
			backupStatusHelperMarker+"=1",
			backupStatusHelperRoot+"="+root,
			backupStatusHelperReady+"="+readyPaths[index],
			backupStatusHelperGate+"="+gate,
			backupStatusHelperOutcome+"="+outcome,
		)
		commands[index] = command
		if err := command.Start(); err != nil {
			t.Fatal(err)
		}
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		ready := 0
		for _, path := range readyPaths {
			if _, err := os.Lstat(path); err == nil {
				ready++
			}
		}
		if ready == len(readyPaths) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("backup status process race did not reach the barrier")
		}
		time.Sleep(5 * time.Millisecond)
	}
	if err := os.WriteFile(gate, []byte("go"), 0o600); err != nil {
		t.Fatal(err)
	}
	successes := 0
	for index, command := range commands {
		if err := command.Wait(); err == nil {
			successes++
		}
		waited[index] = true
	}
	if processCtx.Err() != nil {
		t.Fatalf("backup status process race timed out: %v", processCtx.Err())
	}
	if successes != 1 {
		t.Fatalf("backup status process-race successes = %d, want 1", successes)
	}
}
