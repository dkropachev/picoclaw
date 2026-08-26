package tools

import (
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"
)

func TestSessionManagerConcurrentOwnerIsolation(t *testing.T) {
	manager := NewSessionManager()
	t.Cleanup(manager.Stop)
	ownerA := testProcessSessionOwner("agent-a", "session-a")
	ownerB := testProcessSessionOwner("agent-b", "session-b")
	now := time.Now().Unix()
	stableA := newManagedTestSession("stable-a", 101, "running", now)
	stableB := newManagedTestSession("stable-b", 201, "running", now)
	mutableA := newManagedTestSession("mutable-a", 102, "running", now)
	if err := manager.Add(ownerA, stableA); err != nil {
		t.Fatal(err)
	}
	if err := manager.Add(ownerB, stableB); err != nil {
		t.Fatal(err)
	}
	if err := manager.Add(ownerA, mutableA); err != nil {
		t.Fatal(err)
	}

	const iterations = 250
	// Keep every possible failure nonblocking so a regression is reported
	// instead of deadlocking before the channel is drained.
	errorsCh := make(chan error, iterations*6)
	var workers sync.WaitGroup
	workers.Add(6)

	go func() {
		defer workers.Done()
		for index := range iterations {
			if index%2 == 0 {
				stableA.SetStatus("running")
			} else {
				stableA.SetStatus("done")
			}
		}
	}()

	go func() {
		defer workers.Done()
		for range iterations {
			got, err := manager.Get(ownerA, "stable-a")
			if err != nil || got != stableA {
				errorsCh <- fmt.Errorf("exact get = %#v, %w", got, err)
			}
			listed, err := manager.List(ownerA)
			if err != nil {
				errorsCh <- fmt.Errorf("owner A list: %w", err)
				continue
			}
			for _, info := range listed {
				if info.ID == "stable-b" {
					errorsCh <- errors.New("owner A listed owner B session")
				}
			}
		}
	}()

	go func() {
		defer workers.Done()
		for range iterations {
			if _, err := manager.Get(ownerB, "stable-a"); !errors.Is(err, ErrSessionNotFound) {
				errorsCh <- fmt.Errorf("foreign get error = %v", err)
			}
			if err := manager.Remove(ownerB, "mutable-a", mutableA); !errors.Is(err, ErrSessionNotFound) {
				errorsCh <- fmt.Errorf("foreign remove error = %v", err)
			}
		}
	}()

	go func() {
		defer workers.Done()
		for index := range iterations {
			id := fmt.Sprintf("reserved-%03d", index)
			token, err := manager.reserveID(ownerA, id)
			if err != nil {
				errorsCh <- fmt.Errorf("reserve %s: %w", id, err)
				continue
			}
			if !manager.releaseReservation(token) {
				errorsCh <- fmt.Errorf("release %s failed", id)
			}
		}
	}()

	go func() {
		defer workers.Done()
		for range iterations {
			if err := manager.Remove(ownerA, "mutable-a", mutableA); err != nil {
				errorsCh <- fmt.Errorf("exact remove: %w", err)
				continue
			}
			if err := manager.Add(ownerA, mutableA); err != nil {
				errorsCh <- fmt.Errorf("exact re-add: %w", err)
			}
		}
	}()

	go func() {
		defer workers.Done()
		for range iterations {
			manager.cleanupOldSessions()
		}
	}()

	workers.Wait()
	close(errorsCh)
	for err := range errorsCh {
		if err != nil {
			t.Error(err)
		}
	}

	if got, err := manager.Get(ownerA, "stable-a"); err != nil || got != stableA {
		t.Fatalf("owner A stable session lost: %#v, %v", got, err)
	}
	if got, err := manager.Get(ownerB, "stable-b"); err != nil || got != stableB {
		t.Fatalf("owner B stable session lost: %#v, %v", got, err)
	}
}
