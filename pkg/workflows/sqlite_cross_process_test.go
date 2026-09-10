package workflows

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

const workflowSQLiteSidecarHelperWorkspace = "PICOCLAW_WORKFLOW_SQLITE_SIDECAR_HELPER_WORKSPACE"

func TestWorkflowSQLiteSidecarsSurviveCrossProcessPoolReopen(t *testing.T) {
	if workspace := os.Getenv(workflowSQLiteSidecarHelperWorkspace); workspace != "" {
		runWorkflowSQLiteSidecarHelper(t, workspace)
		return
	}

	workspace := privateWorkflowTestWorkspace(t)
	store := NewFileRunStore(workspace)
	createWorkflowSQLiteSidecarTestRun(t, store, 0)

	// Retain one physical parent connection while the child attaches. This
	// establishes the exact cross-process WAL generation whose identities must
	// survive every later parent close/reopen cycle.
	parentDB, releaseParent, borrowErr := borrowWorkflowDatabase(t.Context(), workspace)
	if borrowErr != nil {
		t.Fatal(borrowErr)
	}
	parentReleased := false
	t.Cleanup(func() {
		if !parentReleased {
			releaseParent()
		}
		if closeErr := workflowDatabasePoolFor(workspace).closeIdle(); closeErr != nil {
			t.Errorf("close parent workflow pool: %v", closeErr)
		}
	})
	var initialCount int
	if queryErr := parentDB.QueryRowContext(
		t.Context(), `SELECT COUNT(*) FROM workflow_runs`,
	).Scan(&initialCount); queryErr != nil {
		t.Fatal(queryErr)
	}
	if initialCount != 1 {
		t.Fatalf("initial workflow run count = %d, want 1", initialCount)
	}

	command := exec.Command(
		os.Args[0],
		"-test.run=^TestWorkflowSQLiteSidecarsSurviveCrossProcessPoolReopen$",
	)
	command.Env = append(
		os.Environ(),
		workflowSQLiteSidecarHelperWorkspace+"="+workspace,
	)
	stdin, err := command.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := command.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	var stderr bytes.Buffer
	command.Stderr = &stderr
	if startErr := command.Start(); startErr != nil {
		t.Fatal(startErr)
	}
	waited := false
	t.Cleanup(func() {
		_ = stdin.Close()
		if !waited {
			_ = command.Process.Kill()
			_ = command.Wait()
		}
	})
	reader := bufio.NewReader(stdout)
	if line := readWorkflowSQLiteSidecarProtocolLine(t, reader, &stderr); line != "ready 1" {
		t.Fatalf("sidecar helper readiness = %q, want %q", line, "ready 1")
	}

	databasePath, err := workflowDatabasePath(workspace)
	if err != nil {
		t.Fatal(err)
	}
	walHandle, walIdentity := retainWorkflowSQLiteSidecarIdentity(t, databasePath+"-wal")
	shmHandle, shmIdentity := retainWorkflowSQLiteSidecarIdentity(t, databasePath+"-shm")
	t.Cleanup(func() {
		if err := walHandle.Close(); err != nil {
			t.Errorf("close retained WAL identity: %v", err)
		}
		if err := shmHandle.Close(); err != nil {
			t.Errorf("close retained SHM identity: %v", err)
		}
	})

	releaseParent()
	parentReleased = true
	if err := workflowDatabasePoolFor(workspace).closeIdle(); err != nil {
		t.Fatal(err)
	}
	assertWorkflowSQLiteSidecarIdentity(t, databasePath+"-wal", walIdentity)
	assertWorkflowSQLiteSidecarIdentity(t, databasePath+"-shm", shmIdentity)

	const cycles = 12
	for cycle := 1; cycle <= cycles; cycle++ {
		createWorkflowSQLiteSidecarTestRun(t, store, cycle)
		if err := workflowDatabasePoolFor(workspace).closeIdle(); err != nil {
			t.Fatalf("close parent workflow pool after cycle %d: %v", cycle, err)
		}
		assertWorkflowSQLiteSidecarIdentity(t, databasePath+"-wal", walIdentity)
		assertWorkflowSQLiteSidecarIdentity(t, databasePath+"-shm", shmIdentity)

		if _, err := fmt.Fprintln(stdin, "count"); err != nil {
			t.Fatalf("request child count after cycle %d: %v", cycle, err)
		}
		want := "count " + strconv.Itoa(cycle+1)
		if line := readWorkflowSQLiteSidecarProtocolLine(t, reader, &stderr); line != want {
			t.Fatalf("child count after cycle %d = %q, want %q", cycle, line, want)
		}
	}

	if _, err := fmt.Fprintln(stdin, "stop"); err != nil {
		t.Fatal(err)
	}
	if line := readWorkflowSQLiteSidecarProtocolLine(t, reader, &stderr); line != "stopped" {
		t.Fatalf("sidecar helper stop = %q, want stopped", line)
	}
	if err := stdin.Close(); err != nil {
		t.Fatal(err)
	}
	if err := command.Wait(); err != nil {
		t.Fatalf("sidecar helper exit = %v\n%s", err, stderr.String())
	}
	waited = true
}

func runWorkflowSQLiteSidecarHelper(t *testing.T, workspace string) {
	if !filepath.IsAbs(workspace) || filepath.Clean(workspace) != workspace {
		t.Fatal("sidecar helper workspace is not an absolute clean path")
	}
	db, release, err := borrowWorkflowDatabase(t.Context(), workspace)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		release()
		if err := workflowDatabasePoolFor(workspace).closeIdle(); err != nil {
			t.Errorf("close child workflow pool: %v", err)
		}
	}()
	queryCount := func() int {
		var count int
		if err := db.QueryRowContext(
			context.Background(), `SELECT COUNT(*) FROM workflow_runs`,
		).Scan(&count); err != nil {
			t.Fatal(err)
		}
		return count
	}
	if _, err := fmt.Fprintf(os.Stdout, "ready %d\n", queryCount()); err != nil {
		t.Fatal(err)
	}

	scanner := bufio.NewScanner(os.Stdin)
	for scanner.Scan() {
		switch scanner.Text() {
		case "count":
			if _, err := fmt.Fprintf(os.Stdout, "count %d\n", queryCount()); err != nil {
				t.Fatal(err)
			}
		case "stop":
			if _, err := fmt.Fprintln(os.Stdout, "stopped"); err != nil {
				t.Fatal(err)
			}
			return
		default:
			t.Fatalf("unknown sidecar helper command %q", scanner.Text())
		}
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	t.Fatal("sidecar helper input closed before stop")
}

func createWorkflowSQLiteSidecarTestRun(t *testing.T, store *FileRunStore, index int) {
	t.Helper()
	now := time.Unix(int64(index+1), int64(index+1)).UTC()
	if err := store.CreateRun(t.Context(), &Run{
		ID:          fmt.Sprintf("wr_sidecar_%02d", index),
		WorkflowRef: "workflows/sidecar.yml",
		Status:      RunStatusSucceeded,
		CreatedAt:   now,
		UpdatedAt:   now,
	}); err != nil {
		t.Fatalf("create workflow run %d: %v", index, err)
	}
}

func workflowSQLiteSidecarIdentity(t *testing.T, path string) os.FileInfo {
	t.Helper()
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatalf("inspect SQLite sidecar %s: %v", filepath.Base(path), err)
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		t.Fatalf("SQLite sidecar %s is unsafe", filepath.Base(path))
	}
	return info
}

func assertWorkflowSQLiteSidecarIdentity(t *testing.T, path string, want os.FileInfo) {
	t.Helper()
	got := workflowSQLiteSidecarIdentity(t, path)
	if !os.SameFile(want, got) {
		t.Fatalf("SQLite sidecar %s identity changed", filepath.Base(path))
	}
}

func readWorkflowSQLiteSidecarProtocolLine(
	t *testing.T,
	reader *bufio.Reader,
	stderr *bytes.Buffer,
) string {
	t.Helper()
	type result struct {
		line string
		err  error
	}
	resultCh := make(chan result, 1)
	go func() {
		line, err := reader.ReadString('\n')
		resultCh <- result{line: line, err: err}
	}()
	select {
	case got := <-resultCh:
		if got.err != nil {
			t.Fatalf("read sidecar helper protocol: %v\n%s", got.err, stderr.String())
		}
		return strings.TrimSuffix(got.line, "\n")
	case <-time.After(10 * time.Second):
		t.Fatalf("timed out reading sidecar helper protocol\n%s", stderr.String())
		return ""
	}
}
