//go:build unix

package repoaudit

import (
	"errors"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func TestMappingWorkerClaimRaceOffsets(t *testing.T) {
	t.Run("claim error after initial snapshot", func(t *testing.T) {
		store := newRepositoryAuditTestStore(t)
		state := recordMappingWorkerFinding(
			t,
			store,
			"claim-error-offset",
			strings.Repeat("2", 40),
			"wait.go",
			"wait.signal",
		)
		original := repositoryReviewFlock
		exclusiveCalls := 0
		repositoryReviewFlock = func(fd int, how int) error {
			if how == unix.LOCK_EX {
				exclusiveCalls++
				if exclusiveCalls == 2 {
					return errors.New("injected claim lock failure")
				}
			}
			return original(fd, how)
		}
		t.Cleanup(func() { repositoryReviewFlock = original })
		if _, err := store.ProcessPendingMappingJobs(
			t.Context(),
			state.Repository,
			RepositoryMappingProcessOptions{},
		); err == nil {
			t.Fatal("mapping worker ignored claim error")
		}
	})

	t.Run("concurrent claimant wins reservation", func(t *testing.T) {
		store := newRepositoryAuditTestStore(t)
		state := recordMappingWorkerFinding(
			t,
			store,
			"claim-race-offset",
			strings.Repeat("3", 40),
			"wait.go",
			"wait.signal",
		)
		original := repositoryReviewFlock
		exclusiveCalls := 0
		injected := false
		repositoryReviewFlock = func(fd int, how int) error {
			if how != unix.LOCK_EX {
				return original(fd, how)
			}
			exclusiveCalls++
			if err := original(fd, how); err != nil {
				return err
			}
			if exclusiveCalls == 2 && !injected {
				injected = true
				current, loadErr := store.load(state.Repository)
				if loadErr != nil {
					return loadErr
				}
				now := repositoryAuditTestNow.Add(time.Minute)
				current.MappingJobs[0].State = RepositoryMappingRunning
				current.MappingJobs[0].Attempts++
				current.MappingJobs[0].ReservedAt = now
				current.MappingJobs[0].UpdatedAt = now
				current.Version++
				current.UpdatedAt = now
				if saveErr := store.save(&current); saveErr != nil {
					return saveErr
				}
			}
			return nil
		}
		t.Cleanup(func() { repositoryReviewFlock = original })
		result, err := store.ProcessPendingMappingJobs(t.Context(), state.Repository, RepositoryMappingProcessOptions{})
		if err != nil || !injected || result != (RepositoryMappingProcessResult{}) {
			t.Fatalf("claim race result=%#v injected=%v err=%v", result, injected, err)
		}
	})
}
