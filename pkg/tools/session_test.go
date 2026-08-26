package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func testProcessSessionOwner(agentID, sessionKey string) ProcessSessionOwner {
	return ProcessSessionOwner{AgentID: agentID, SessionKey: sessionKey}
}

func newManagedTestSession(id string, pid int, status string, startedAt int64) *ProcessSession {
	return &ProcessSession{
		ID:           id,
		PID:          pid,
		Command:      "printf managed-session",
		Background:   true,
		StartTime:    startedAt,
		Status:       status,
		stdinWriter:  &bytes.Buffer{},
		outputBuffer: &bytes.Buffer{},
		waitDone:     make(chan struct{}),
	}
}

type blockingProcessSessionWriter struct {
	started chan struct{}
	release chan struct{}
}

func (writer *blockingProcessSessionWriter) Write(data []byte) (int, error) {
	close(writer.started)
	<-writer.release
	return len(data), nil
}

func TestProcessSessionOwnerValidateExact(t *testing.T) {
	tests := []struct {
		name    string
		owner   ProcessSessionOwner
		wantErr bool
	}{
		{name: "exact", owner: testProcessSessionOwner("main", "agent:main:session")},
		{name: "empty", owner: ProcessSessionOwner{}, wantErr: true},
		{name: "missing agent", owner: testProcessSessionOwner("", "session"), wantErr: true},
		{name: "missing session", owner: testProcessSessionOwner("main", ""), wantErr: true},
		{name: "blank agent", owner: testProcessSessionOwner(" ", "session"), wantErr: true},
		{name: "blank session", owner: testProcessSessionOwner("main", "\t"), wantErr: true},
		{name: "agent leading whitespace", owner: testProcessSessionOwner(" main", "session"), wantErr: true},
		{name: "agent trailing whitespace", owner: testProcessSessionOwner("main ", "session"), wantErr: true},
		{name: "session leading whitespace", owner: testProcessSessionOwner("main", " session"), wantErr: true},
		{name: "session trailing whitespace", owner: testProcessSessionOwner("main", "session\n"), wantErr: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := test.owner.Validate()
			if test.wantErr {
				require.ErrorIs(t, err, ErrProcessSessionOwnerInvalid)
				return
			}
			require.NoError(t, err)
		})
	}
}

func TestSessionManagerScopedAddGetListAndImmutableMetadata(t *testing.T) {
	manager := NewSessionManager()
	t.Cleanup(manager.Stop)
	ownerA := testProcessSessionOwner("agent-a", "session-a")
	ownerB := testProcessSessionOwner("agent-b", "session-b")
	ownerSameAgent := testProcessSessionOwner("agent-a", "session-foreign")
	ownerSameSession := testProcessSessionOwner("agent-foreign", "session-a")
	startedAt := time.Now().Unix()
	sessionA := newManagedTestSession("process-a", 101, "running", startedAt)
	sessionB := newManagedTestSession("process-b", 202, "running", startedAt+1)
	require.NoError(t, manager.Add(ownerA, sessionA))
	require.NoError(t, manager.Add(ownerB, sessionB))

	got, err := manager.Get(ownerA, "process-a")
	require.NoError(t, err)
	require.Same(t, sessionA, got)
	_, err = manager.Get(ownerB, "process-a")
	require.ErrorIs(t, err, ErrSessionNotFound)
	_, err = manager.Get(ownerA, "absent")
	require.ErrorIs(t, err, ErrSessionNotFound)
	for _, foreign := range []ProcessSessionOwner{ownerSameAgent, ownerSameSession} {
		_, err = manager.Get(foreign, "process-a")
		require.ErrorIs(t, err, ErrSessionNotFound)
		listed, listErr := manager.List(foreign)
		require.NoError(t, listErr)
		require.Empty(t, listed)
		require.ErrorIs(t, manager.Remove(foreign, "process-a", sessionA), ErrSessionNotFound)
	}

	sessionA.mu.Lock()
	sessionA.ID = "mutated-id"
	sessionA.Command = "mutated command"
	sessionA.PID = 999
	sessionA.StartTime = 1
	sessionA.PTY = true
	sessionA.Background = false
	sessionA.Status = "done"
	sessionA.mu.Unlock()
	manager.mu.RLock()
	entrySnapshot := *manager.sessions["process-a"]
	manager.mu.RUnlock()
	require.False(t, entrySnapshot.pty)
	require.True(t, entrySnapshot.background)

	listedA, err := manager.List(ownerA)
	require.NoError(t, err)
	require.Equal(t, []SessionInfo{{
		ID: "process-a", Command: "printf managed-session", Status: "done", PID: 101, StartedAt: startedAt,
	}}, listedA)
	listedB, err := manager.List(ownerB)
	require.NoError(t, err)
	require.Len(t, listedB, 1)
	require.Equal(t, "process-b", listedB[0].ID)
	encoded, err := json.Marshal(listedA)
	require.NoError(t, err)
	require.NotContains(t, string(encoded), ownerA.AgentID)
	require.NotContains(t, string(encoded), ownerA.SessionKey)

	got, err = manager.Get(ownerA, "process-a")
	require.NoError(t, err)
	require.Same(t, sessionA, got)
	_, err = manager.Get(ownerA, "mutated-id")
	require.ErrorIs(t, err, ErrSessionNotFound)
}

func TestSessionManagerRejectsInvalidPublicationAndConflicts(t *testing.T) {
	ownerA := testProcessSessionOwner("agent-a", "session-a")
	ownerB := testProcessSessionOwner("agent-b", "session-b")
	now := time.Now().Unix()
	valid := func() *ProcessSession { return newManagedTestSession("valid-id", 101, "running", now) }

	tests := []struct {
		name    string
		mutate  func(*ProcessSession) *ProcessSession
		wantErr error
	}{
		{name: "nil", mutate: func(*ProcessSession) *ProcessSession { return nil }, wantErr: ErrProcessSessionInvalid},
		{
			name: "missing id",
			mutate: func(s *ProcessSession) *ProcessSession {
				s.ID = ""
				return s
			},
			wantErr: ErrProcessSessionInvalid,
		},
		{
			name: "ambiguous id",
			mutate: func(s *ProcessSession) *ProcessSession {
				s.ID = " valid-id"
				return s
			},
			wantErr: ErrProcessSessionInvalid,
		},
		{
			name: "missing command",
			mutate: func(s *ProcessSession) *ProcessSession {
				s.Command = " "
				return s
			},
			wantErr: ErrProcessSessionInvalid,
		},
		{
			name: "missing pid",
			mutate: func(s *ProcessSession) *ProcessSession {
				s.PID = 0
				return s
			},
			wantErr: ErrProcessSessionInvalid,
		},
		{
			name: "missing start",
			mutate: func(s *ProcessSession) *ProcessSession {
				s.StartTime = 0
				return s
			},
			wantErr: ErrProcessSessionInvalid,
		},
		{
			name: "missing status",
			mutate: func(s *ProcessSession) *ProcessSession {
				s.Status = ""
				return s
			},
			wantErr: ErrProcessSessionInvalid,
		},
		{
			name: "ambiguous status",
			mutate: func(s *ProcessSession) *ProcessSession {
				s.Status = " running"
				return s
			},
			wantErr: ErrProcessSessionInvalid,
		},
		{
			name: "not background",
			mutate: func(s *ProcessSession) *ProcessSession {
				s.Background = false
				return s
			},
			wantErr: ErrProcessSessionInvalid,
		},
		{
			name: "missing output",
			mutate: func(s *ProcessSession) *ProcessSession {
				s.outputBuffer = nil
				return s
			},
			wantErr: ErrProcessSessionInvalid,
		},
		{
			name: "missing wait completion",
			mutate: func(s *ProcessSession) *ProcessSession {
				s.waitDone = nil
				return s
			},
			wantErr: ErrProcessSessionInvalid,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			manager := NewSessionManager()
			t.Cleanup(manager.Stop)
			require.ErrorIs(t, manager.Add(ownerA, test.mutate(valid())), test.wantErr)
			listed, err := manager.List(ownerA)
			require.NoError(t, err)
			require.Empty(t, listed)
		})
	}

	manager := NewSessionManager()
	t.Cleanup(manager.Stop)
	first := valid()
	require.NoError(t, manager.Add(ownerA, first))
	duplicateID := valid()
	require.ErrorIs(t, manager.Add(ownerB, duplicateID), ErrSessionAlreadyExists)

	first.mu.Lock()
	first.ID = "pointer-reuse-id"
	first.mu.Unlock()
	require.ErrorIs(t, manager.Add(ownerB, first), ErrSessionAlreadyExists)

	reservation, err := manager.reserveID(ownerB, "reserved-id")
	require.NoError(t, err)
	reservedSession := newManagedTestSession("reserved-id", 303, "running", now)
	require.ErrorIs(t, manager.Add(ownerB, reservedSession), ErrSessionAlreadyExists)
	require.True(t, manager.releaseReservation(reservation))

	require.ErrorIs(t, manager.Add(ProcessSessionOwner{}, valid()), ErrProcessSessionOwnerInvalid)
}

func TestSessionManagerRemoveIsNonProbingAndExactPointerFenced(t *testing.T) {
	manager := NewSessionManager()
	t.Cleanup(manager.Stop)
	owner := testProcessSessionOwner("agent-a", "session-a")
	foreign := testProcessSessionOwner("agent-b", "session-b")
	session := newManagedTestSession("process-a", 101, "running", time.Now().Unix())
	other := newManagedTestSession("other", 202, "running", time.Now().Unix())
	require.NoError(t, manager.Add(owner, session))

	for name, expected := range map[string]*ProcessSession{
		"nil":       nil,
		"actual":    session,
		"arbitrary": other,
	} {
		t.Run("foreign "+name, func(t *testing.T) {
			require.ErrorIs(t, manager.Remove(foreign, "process-a", expected), ErrSessionNotFound)
			got, err := manager.Get(owner, "process-a")
			require.NoError(t, err)
			require.Same(t, session, got)
		})
		t.Run("absent "+name, func(t *testing.T) {
			require.ErrorIs(t, manager.Remove(foreign, "absent", expected), ErrSessionNotFound)
		})
	}

	require.ErrorIs(t, manager.Remove(owner, "process-a", nil), ErrSessionStale)
	require.ErrorIs(t, manager.Remove(owner, "process-a", other), ErrSessionStale)
	require.NoError(t, manager.Remove(owner, "process-a", session))

	replacement := newManagedTestSession("process-a", 303, "running", time.Now().Unix())
	require.NoError(t, manager.Add(owner, replacement))
	require.ErrorIs(t, manager.Remove(owner, "process-a", session), ErrSessionStale)
	got, err := manager.Get(owner, "process-a")
	require.NoError(t, err)
	require.Same(t, replacement, got)
	require.NoError(t, manager.Remove(owner, "process-a", replacement))
	require.ErrorIs(t, manager.Remove(owner, "process-a", replacement), ErrSessionNotFound)
}

func TestSessionManagerReservationTokenABAFences(t *testing.T) {
	manager := NewSessionManager()
	t.Cleanup(manager.Stop)
	owner := testProcessSessionOwner("agent-a", "session-a")
	now := time.Now().Unix()

	first, err := manager.reserveID(owner, "reserved-id")
	require.NoError(t, err)
	_, err = manager.reserveID(owner, "reserved-id")
	require.ErrorIs(t, err, ErrSessionAlreadyExists)
	require.True(t, manager.releaseReservation(first))
	require.False(t, manager.releaseReservation(first))

	second, err := manager.reserveID(owner, "reserved-id")
	require.NoError(t, err)
	require.False(t, manager.releaseReservation(first), "stale token released replacement reservation")
	staleSession := newManagedTestSession("reserved-id", 100, "running", now)
	require.ErrorIs(t, manager.promoteReservation(first, staleSession), ErrSessionReservationInvalid)
	wrongID := newManagedTestSession("wrong-id", 101, "running", now)
	require.ErrorIs(t, manager.promoteReservation(second, wrongID), ErrSessionReservationInvalid)

	session := newManagedTestSession("reserved-id", 101, "running", now)
	require.NoError(t, manager.promoteReservation(second, session))
	require.False(t, manager.releaseReservation(second))
	got, err := manager.Get(owner, "reserved-id")
	require.NoError(t, err)
	require.Same(t, session, got)
	require.ErrorIs(t, manager.promoteReservation(second, session), ErrSessionReservationInvalid)

	_, err = manager.reserveID(ProcessSessionOwner{}, "invalid-owner")
	require.ErrorIs(t, err, ErrProcessSessionOwnerInvalid)
	_, err = manager.reserveID(owner, " reserved")
	require.ErrorIs(t, err, ErrSessionReservationInvalid)
	require.False(t, manager.releaseReservation(nil))
}

func TestSessionManagerCleanupAllOwnersPreservesReservations(t *testing.T) {
	manager := NewSessionManager()
	t.Cleanup(manager.Stop)
	ownerA := testProcessSessionOwner("agent-a", "session-a")
	ownerB := testProcessSessionOwner("agent-b", "session-b")
	now := time.Now()
	old := now.Add(-31 * time.Minute).Unix()
	recent := now.Add(-29 * time.Minute).Unix()

	entries := []struct {
		owner   ProcessSessionOwner
		session *ProcessSession
	}{
		{ownerA, newManagedTestSession("a-old-done", 101, "done", old)},
		{ownerA, newManagedTestSession("a-old-running", 102, "running", old)},
		{ownerA, newManagedTestSession("a-recent-done", 103, "done", recent)},
		{ownerB, newManagedTestSession("b-old-exited", 201, "exited", old)},
		{ownerB, newManagedTestSession("b-recent-done", 202, "done", recent)},
	}
	for _, entry := range entries {
		require.NoError(t, manager.Add(entry.owner, entry.session))
	}
	reservation, err := manager.reserveID(ownerB, "reserved-cleanup")
	require.NoError(t, err)

	manager.cleanupOldSessions()
	for _, removed := range []struct {
		owner ProcessSessionOwner
		id    string
	}{{ownerA, "a-old-done"}, {ownerB, "b-old-exited"}} {
		_, err = manager.Get(removed.owner, removed.id)
		require.ErrorIs(t, err, ErrSessionNotFound)
	}
	for _, retained := range []struct {
		owner ProcessSessionOwner
		id    string
	}{{ownerA, "a-old-running"}, {ownerA, "a-recent-done"}, {ownerB, "b-recent-done"}} {
		_, err = manager.Get(retained.owner, retained.id)
		require.NoError(t, err)
	}
	require.True(t, manager.releaseReservation(reservation))
}

func TestProcessSessionStateAndInfo(t *testing.T) {
	session := newManagedTestSession("test-1", 12345, "running", 1000)
	require.False(t, session.IsDone())
	session.SetStatus("done")
	require.True(t, session.IsDone())
	session.SetStatus("exited")
	require.True(t, session.IsDone())

	info := session.ToSessionInfo()
	require.Equal(t, "test-1", info.ID)
	require.Equal(t, "printf managed-session", info.Command)
	require.Equal(t, "exited", info.Status)
	require.Equal(t, 12345, info.PID)
	require.Equal(t, int64(1000), info.StartedAt)
}

func TestProcessSessionWriteDoesNotHoldStateLockAcrossIO(t *testing.T) {
	writer := &blockingProcessSessionWriter{
		started: make(chan struct{}),
		release: make(chan struct{}),
	}
	session := newManagedTestSession("blocking-write", 12345, "running", time.Now().Unix())
	session.stdinWriter = writer
	writeDone := make(chan error, 1)
	go func() { writeDone <- session.Write("blocked") }()
	<-writer.started

	exitRecorded := make(chan struct{})
	go func() {
		session.mu.Lock()
		session.processExited = true
		session.mu.Unlock()
		close(exitRecorded)
	}()
	select {
	case <-exitRecorded:
	case <-time.After(time.Second):
		t.Fatal("blocked OS write retained the process-session state lock")
	}
	close(writer.release)
	require.NoError(t, <-writeDone)
	require.ErrorIs(t, session.Write("after-exit"), ErrSessionDone)
}

func TestProcessSessionSignalWaitDoneOnce(t *testing.T) {
	session := newManagedTestSession("wait-session", 123, "running", time.Now().Unix())
	session.signalWaitDone()
	session.signalWaitDone()
	require.NoError(t, session.waitForProcessExit(nil))
	select {
	case <-session.waitDone:
	default:
		t.Fatal("wait completion was not signaled")
	}
	var nilSession *ProcessSession
	nilSession.signalWaitDone()
	(&ProcessSession{}).signalWaitDone()
	require.ErrorIs(t, nilSession.waitForProcessExit(context.Background()), ErrProcessSessionInvalid)
	require.ErrorIs(t, (&ProcessSession{}).waitForProcessExit(context.Background()), ErrProcessSessionInvalid)

	pending := newManagedTestSession("pending-wait", 124, "running", time.Now().Unix())
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	require.ErrorIs(t, pending.waitForProcessExit(canceled), context.Canceled)
}

func TestSessionOwnerErrorIsRedacted(t *testing.T) {
	require.NotContains(t, ErrProcessSessionOwnerInvalid.Error(), "agent")
	require.NotContains(t, ErrProcessSessionOwnerInvalid.Error(), "session-a")
	require.False(t, strings.Contains(ErrSessionNotFound.Error(), "owner"))
}
