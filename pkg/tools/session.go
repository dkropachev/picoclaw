package tools

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
)

const maxOutputBufferSize = 1 * 1024 * 1024 // 1MB

const outputTruncateMarker = "\n... [output truncated, exceeded 1MB]\n"

// PtyKeyMode represents arrow key encoding mode for PTY sessions.
// Programs send smkx/rmkx sequences to switch between CSI and SS3 modes.
type PtyKeyMode uint8

const (
	PtyKeyModeCSI PtyKeyMode = iota // triggered by rmkx (\x1b[?1l)
	PtyKeyModeSS3                   // triggered by smkx (\x1b[?1h)
)

const PtyKeyModeNotFound PtyKeyMode = 255

var (
	ErrSessionNotFound            = errors.New("session not found")
	ErrSessionDone                = errors.New("session already completed")
	ErrPTYNotSupported            = errors.New("PTY is not supported on this platform")
	ErrNoStdin                    = errors.New("no stdin available")
	ErrProcessSessionOwnerInvalid = errors.New("process session owner is invalid")
	ErrProcessSessionInvalid      = errors.New("process session is invalid")
	ErrSessionAlreadyExists       = errors.New("process session already exists")
	ErrSessionStale               = errors.New("process session entry is stale")
	ErrSessionReservationInvalid  = errors.New("process session reservation is invalid")
)

// ProcessSessionOwner is the exact runtime identity authorized to manage one
// background process session. Both values are opaque and must already be in
// canonical form; validation rejects whitespace instead of normalizing it.
type ProcessSessionOwner struct {
	AgentID    string
	SessionKey string
}

// Validate checks that both owner fields are exact and nonblank.
func (owner ProcessSessionOwner) Validate() error {
	if owner.AgentID == "" || owner.SessionKey == "" ||
		owner.AgentID != strings.TrimSpace(owner.AgentID) ||
		owner.SessionKey != strings.TrimSpace(owner.SessionKey) {
		return ErrProcessSessionOwnerInvalid
	}
	return nil
}

type ProcessSession struct {
	mu              sync.Mutex
	inputMu         sync.Mutex
	ID              string
	PID             int
	Command         string
	PTY             bool
	Background      bool
	StartTime       int64
	ExitCode        int
	Status          string
	stdinWriter     io.Writer
	stdoutPipe      io.Reader
	outputBuffer    *bytes.Buffer
	outputTruncated bool
	ptyMaster       *os.File
	waitDone        chan struct{}
	waitDoneOnce    sync.Once
	killProcessFn   func(int) error
	processExited   bool

	// ptyKeyMode tracks arrow key encoding mode (CSI vs SS3)
	ptyKeyMode PtyKeyMode
}

func (s *ProcessSession) IsDone() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.processExited || s.Status == "done" || s.Status == "exited"
}

func (s *ProcessSession) GetPtyKeyMode() PtyKeyMode {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.ptyKeyMode
}

func (s *ProcessSession) SetPtyKeyMode(mode PtyKeyMode) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ptyKeyMode = mode
}

func (s *ProcessSession) GetStatus() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.Status
}

func (s *ProcessSession) SetStatus(status string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.Status = status
}

func (s *ProcessSession) GetExitCode() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.ExitCode
}

func (s *ProcessSession) SetExitCode(code int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ExitCode = code
}

// signalWaitDone records completion by the sole goroutine that owns cmd.Wait.
// The once guard makes defensive repeated cleanup harmless.
func (s *ProcessSession) signalWaitDone() {
	if s == nil || s.waitDone == nil {
		return
	}
	s.waitDoneOnce.Do(func() { close(s.waitDone) })
}

// waitForProcessExit waits for the sole cmd.Wait owner to finish without ever
// calling Wait itself. Callers provide the bound through ctx.
func (s *ProcessSession) waitForProcessExit(ctx context.Context) error {
	if s == nil || s.waitDone == nil {
		return ErrProcessSessionInvalid
	}
	if ctx == nil {
		ctx = context.Background()
	}
	select {
	case <-s.waitDone:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (s *ProcessSession) killProcess() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.Status != "running" || s.processExited {
		return ErrSessionDone
	}

	pid := s.PID
	if pid <= 0 {
		return ErrSessionNotFound
	}

	kill := s.killProcessFn
	if kill == nil {
		kill = killProcessGroup
	}
	if err := kill(pid); err != nil {
		return err
	}

	s.Status = "done"
	s.ExitCode = -1
	return nil
}

func (s *ProcessSession) Kill() error {
	return s.killProcess()
}

func (s *ProcessSession) Write(data string) error {
	s.inputMu.Lock()
	defer s.inputMu.Unlock()

	s.mu.Lock()
	if s.Status != "running" || s.processExited {
		s.mu.Unlock()
		return ErrSessionDone
	}

	var writer io.Writer
	if s.PTY && s.ptyMaster != nil {
		writer = s.ptyMaster
	} else if s.stdinWriter != nil {
		writer = s.stdinWriter
	} else {
		s.mu.Unlock()
		return ErrNoStdin
	}
	s.mu.Unlock()

	_, err := writer.Write([]byte(data))
	return err
}

func (s *ProcessSession) Read() string {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.outputBuffer.Len() == 0 {
		return ""
	}

	data := s.outputBuffer.String()
	s.outputBuffer.Reset()
	return data
}

func (s *ProcessSession) ToSessionInfo() SessionInfo {
	s.mu.Lock()
	defer s.mu.Unlock()

	return SessionInfo{
		ID:        s.ID,
		Command:   s.Command,
		Status:    s.Status,
		PID:       s.PID,
		StartedAt: s.StartTime,
	}
}

type managedProcessSession struct {
	owner      ProcessSessionOwner
	session    *ProcessSession
	id         string
	command    string
	pid        int
	startedAt  int64
	pty        bool
	background bool
}

// processSessionReservation is an opaque pointer-identity token. Promotion and
// release compare the exact pointer stored by SessionManager, so a stale token
// cannot affect a later reservation that reused the same ID.
type processSessionReservation struct {
	owner ProcessSessionOwner
	id    string
}

type SessionManager struct {
	mu           sync.RWMutex
	sessions     map[string]*managedProcessSession
	reservations map[string]*processSessionReservation
	stopCh       chan struct{}
	stopOnce     sync.Once
}

func NewSessionManager() *SessionManager {
	sm := &SessionManager{
		sessions:     make(map[string]*managedProcessSession),
		reservations: make(map[string]*processSessionReservation),
		stopCh:       make(chan struct{}),
	}

	// Start cleaner goroutine - runs every 5 minutes, cleans up sessions done for >30 minutes
	go func() {
		ticker := time.NewTicker(5 * time.Minute)
		defer ticker.Stop()
		for {
			select {
			case <-sm.stopCh:
				return
			case <-ticker.C:
				sm.cleanupOldSessions()
			}
		}
	}()

	return sm
}

// Stop shuts down the background cleanup goroutine. Safe to call multiple
// times from concurrent goroutines. After Stop returns, the SessionManager
// is still usable — only the cleanup goroutine is terminated.
func (sm *SessionManager) Stop() {
	sm.stopOnce.Do(func() {
		close(sm.stopCh)
	})
}

// ActiveCount returns the number of published processes that have not reached
// terminal state. It snapshots pointers without holding the manager lock while
// inspecting per-session state.
func (sm *SessionManager) ActiveCount() int {
	if sm == nil {
		return 0
	}
	sm.mu.RLock()
	sessions := make([]*ProcessSession, 0, len(sm.sessions))
	for _, entry := range sm.sessions {
		if entry != nil && entry.session != nil {
			sessions = append(sessions, entry.session)
		}
	}
	sm.mu.RUnlock()
	active := 0
	for _, session := range sessions {
		if !session.IsDone() {
			active++
		}
	}
	return active
}

// cleanupOldSessions removes sessions that are done and older than 30 minutes
func (sm *SessionManager) cleanupOldSessions() {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	cutoff := time.Now().Add(-30 * time.Minute)
	for id, entry := range sm.sessions {
		if entry.session.IsDone() && entry.startedAt < cutoff.Unix() {
			delete(sm.sessions, id)
		}
	}
}

// Add publishes a fully initialized background process for one exact owner.
// Duplicate IDs, outstanding reservations, and pointer reuse all fail without
// changing the manager.
func (sm *SessionManager) Add(owner ProcessSessionOwner, session *ProcessSession) error {
	if err := owner.Validate(); err != nil {
		return err
	}
	snapshot, err := snapshotManagedProcessSession(session)
	if err != nil {
		return err
	}

	sm.mu.Lock()
	defer sm.mu.Unlock()
	sm.initMapsLocked()
	if _, exists := sm.sessions[snapshot.id]; exists {
		return ErrSessionAlreadyExists
	}
	if _, reserved := sm.reservations[snapshot.id]; reserved {
		return ErrSessionAlreadyExists
	}
	if sm.hasSessionPointerLocked(session) {
		return ErrSessionAlreadyExists
	}
	sm.sessions[snapshot.id] = &managedProcessSession{
		owner:      owner,
		session:    session,
		id:         snapshot.id,
		command:    snapshot.command,
		pid:        snapshot.pid,
		startedAt:  snapshot.startedAt,
		pty:        snapshot.pty,
		background: snapshot.background,
	}
	return nil
}

// Get returns a live process capability only to its exact owner. Foreign and
// absent IDs intentionally share ErrSessionNotFound.
func (sm *SessionManager) Get(owner ProcessSessionOwner, sessionID string) (*ProcessSession, error) {
	if err := owner.Validate(); err != nil {
		return nil, err
	}
	sm.mu.RLock()
	defer sm.mu.RUnlock()

	entry, ok := sm.sessions[sessionID]
	if !ok || entry.owner != owner {
		return nil, ErrSessionNotFound
	}

	return entry.session, nil
}

// Remove deletes only the exact owner and exact process pointer observed by the
// caller. Owner mismatch is checked before pointer identity to avoid ID probes.
func (sm *SessionManager) Remove(
	owner ProcessSessionOwner,
	sessionID string,
	expected *ProcessSession,
) error {
	if err := owner.Validate(); err != nil {
		return err
	}
	sm.mu.Lock()
	defer sm.mu.Unlock()
	entry, ok := sm.sessions[sessionID]
	if !ok || entry.owner != owner {
		return ErrSessionNotFound
	}
	if expected == nil || entry.session != expected {
		return ErrSessionStale
	}
	delete(sm.sessions, sessionID)
	return nil
}

// List returns immutable process metadata plus synchronized live status for one
// exact owner. Owner identity is never projected.
func (sm *SessionManager) List(owner ProcessSessionOwner) ([]SessionInfo, error) {
	if err := owner.Validate(); err != nil {
		return nil, err
	}
	sm.mu.RLock()
	defer sm.mu.RUnlock()

	result := make([]SessionInfo, 0, len(sm.sessions))
	for _, entry := range sm.sessions {
		if entry.owner != owner {
			continue
		}
		result = append(result, entry.toSessionInfo())
	}

	return result, nil
}

type managedProcessSessionSnapshot struct {
	id         string
	command    string
	pid        int
	startedAt  int64
	pty        bool
	background bool
}

func snapshotManagedProcessSession(session *ProcessSession) (managedProcessSessionSnapshot, error) {
	if session == nil {
		return managedProcessSessionSnapshot{}, ErrProcessSessionInvalid
	}
	session.mu.Lock()
	defer session.mu.Unlock()

	if session.ID == "" || session.ID != strings.TrimSpace(session.ID) ||
		strings.TrimSpace(session.Command) == "" || session.PID <= 0 ||
		session.StartTime <= 0 || session.Status == "" ||
		session.Status != strings.TrimSpace(session.Status) ||
		!session.Background || session.outputBuffer == nil || session.waitDone == nil {
		return managedProcessSessionSnapshot{}, ErrProcessSessionInvalid
	}
	return managedProcessSessionSnapshot{
		id:         session.ID,
		command:    session.Command,
		pid:        session.PID,
		startedAt:  session.StartTime,
		pty:        session.PTY,
		background: session.Background,
	}, nil
}

func (entry *managedProcessSession) toSessionInfo() SessionInfo {
	entry.session.mu.Lock()
	status := entry.session.Status
	entry.session.mu.Unlock()
	return SessionInfo{
		ID:        entry.id,
		Command:   entry.command,
		Status:    status,
		PID:       entry.pid,
		StartedAt: entry.startedAt,
	}
}

func (sm *SessionManager) initMapsLocked() {
	if sm.sessions == nil {
		sm.sessions = make(map[string]*managedProcessSession)
	}
	if sm.reservations == nil {
		sm.reservations = make(map[string]*processSessionReservation)
	}
}

func (sm *SessionManager) hasSessionPointerLocked(session *ProcessSession) bool {
	for _, entry := range sm.sessions {
		if entry.session == session {
			return true
		}
	}
	return false
}

// reserveID atomically reserves one non-listable ID for a future background
// process. The returned pointer is the authorization token for promotion or
// release.
func (sm *SessionManager) reserveID(
	owner ProcessSessionOwner,
	sessionID string,
) (*processSessionReservation, error) {
	if err := owner.Validate(); err != nil {
		return nil, err
	}
	if sessionID == "" || sessionID != strings.TrimSpace(sessionID) {
		return nil, ErrSessionReservationInvalid
	}

	sm.mu.Lock()
	defer sm.mu.Unlock()
	sm.initMapsLocked()
	if _, exists := sm.sessions[sessionID]; exists {
		return nil, ErrSessionAlreadyExists
	}
	if _, exists := sm.reservations[sessionID]; exists {
		return nil, ErrSessionAlreadyExists
	}
	token := &processSessionReservation{owner: owner, id: sessionID}
	sm.reservations[sessionID] = token
	return token, nil
}

// promoteReservation atomically replaces one exact reservation with its fully
// initialized visible process entry.
func (sm *SessionManager) promoteReservation(
	token *processSessionReservation,
	session *ProcessSession,
) error {
	if token == nil {
		return ErrSessionReservationInvalid
	}
	snapshot, err := snapshotManagedProcessSession(session)
	if err != nil {
		return err
	}

	sm.mu.Lock()
	defer sm.mu.Unlock()
	sm.initMapsLocked()
	current, ok := sm.reservations[token.id]
	if !ok || current != token {
		return ErrSessionReservationInvalid
	}
	if snapshot.id != token.id {
		return ErrSessionReservationInvalid
	}
	if _, exists := sm.sessions[snapshot.id]; exists || sm.hasSessionPointerLocked(session) {
		return ErrSessionAlreadyExists
	}
	sm.sessions[snapshot.id] = &managedProcessSession{
		owner:      token.owner,
		session:    session,
		id:         snapshot.id,
		command:    snapshot.command,
		pid:        snapshot.pid,
		startedAt:  snapshot.startedAt,
		pty:        snapshot.pty,
		background: snapshot.background,
	}
	delete(sm.reservations, token.id)
	return nil
}

// releaseReservation releases only the exact pointer token currently stored
// for its ID. Stale tokens are harmless.
func (sm *SessionManager) releaseReservation(token *processSessionReservation) bool {
	if token == nil {
		return false
	}
	sm.mu.Lock()
	defer sm.mu.Unlock()
	current, ok := sm.reservations[token.id]
	if !ok || current != token {
		return false
	}
	delete(sm.reservations, token.id)
	return true
}

func generateSessionID() string {
	return uuid.New().String()[:8]
}
