package tools

import (
	"bytes"
	"errors"
	"testing"
	"time"
)

func TestSessionCloseoutCushionKillAndCleaner(t *testing.T) {
	injected := errors.New("kill failed")
	session := &ProcessSession{
		PID: 1, Status: "running",
		killProcessFn: func(int) error { return injected },
	}
	if err := session.killProcess(); !errors.Is(err, injected) {
		t.Fatalf("kill failure = %v", err)
	}
	manager := NewSessionManager()
	manager.Stop()
	manager.Stop()
}

func TestSessionCloseoutCushionMapReservationAndPromotionBranches(t *testing.T) {
	owner := ProcessSessionOwner{AgentID: "agent", SessionKey: "session"}
	manager := &SessionManager{}
	manager.initMapsLocked()
	if manager.sessions == nil || manager.reservations == nil {
		t.Fatal("session maps were not initialized")
	}
	if _, err := manager.reserveID(ProcessSessionOwner{}, "id"); err == nil {
		t.Fatal("invalid owner reserved ID")
	}
	if _, err := manager.reserveID(owner, " "); err == nil {
		t.Fatal("blank ID was reserved")
	}
	reservation, err := manager.reserveID(owner, "id")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.reserveID(owner, "id"); !errors.Is(err, ErrSessionAlreadyExists) {
		t.Fatalf("duplicate reservation = %v", err)
	}
	if err := manager.promoteReservation(nil, nil); !errors.Is(err, ErrSessionReservationInvalid) {
		t.Fatalf("nil promotion = %v", err)
	}
	if err := manager.promoteReservation(reservation, nil); !errors.Is(err, ErrProcessSessionInvalid) {
		t.Fatalf("invalid session promotion = %v", err)
	}
	valid := validCloseoutProcessSession("other")
	if err := manager.promoteReservation(reservation, valid); !errors.Is(err, ErrSessionReservationInvalid) {
		t.Fatalf("mismatched session promotion = %v", err)
	}
	valid.ID = "id"
	stale := &processSessionReservation{owner: owner, id: "id"}
	if err := manager.promoteReservation(stale, valid); !errors.Is(err, ErrSessionReservationInvalid) {
		t.Fatalf("stale token promotion = %v", err)
	}
	if err := manager.promoteReservation(reservation, valid); err != nil {
		t.Fatalf("valid promotion = %v", err)
	}
	if _, err := manager.reserveID(owner, "id"); !errors.Is(err, ErrSessionAlreadyExists) {
		t.Fatalf("visible session ID reservation = %v", err)
	}
	if err := manager.Remove(ProcessSessionOwner{}, "id", valid); err == nil {
		t.Fatal("invalid owner removed session")
	}
}

func TestSessionCloseoutCushionPointerReuse(t *testing.T) {
	owner := ProcessSessionOwner{AgentID: "agent", SessionKey: "session"}
	manager := &SessionManager{
		sessions:     map[string]*managedProcessSession{},
		reservations: map[string]*processSessionReservation{},
	}
	session := validCloseoutProcessSession("first")
	manager.sessions["existing"] = &managedProcessSession{owner: owner, session: session}
	reservation := &processSessionReservation{owner: owner, id: "second"}
	manager.reservations["second"] = reservation
	session.ID = "second"
	if err := manager.promoteReservation(reservation, session); !errors.Is(err, ErrSessionAlreadyExists) {
		t.Fatalf("pointer reuse promotion = %v", err)
	}
}

func validCloseoutProcessSession(id string) *ProcessSession {
	return &ProcessSession{
		ID: id, PID: 1, Command: "command", Status: "running",
		Background: true, StartTime: time.Now().UnixMilli(),
		outputBuffer: &bytes.Buffer{}, waitDone: make(chan struct{}),
	}
}
