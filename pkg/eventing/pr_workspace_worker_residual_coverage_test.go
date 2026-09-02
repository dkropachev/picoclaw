//go:build !mipsle && !netbsd && !(freebsd && arm)

package eventing

import (
	"encoding/json"
	"errors"
	"testing"
	"time"
)

func TestPRWorkspaceWorkerValidationResidualMatrix(t *testing.T) {
	now := time.Date(2026, 9, 2, 12, 0, 0, 0, time.UTC)
	store := openPRWorkspaceTestStore(t, newMutableClock(now))
	claimCases := []PRWorkspaceClaimRequest{
		{},
		{WorkerID: "worker", Limit: 0, LeaseDuration: time.Minute},
		{WorkerID: "worker", Limit: maxPRWorkspaceClaimItems + 1, LeaseDuration: time.Minute},
		{WorkerID: "worker", Limit: 1},
		{WorkerID: "worker", Limit: 1, LeaseDuration: maxPRWorkspaceLease + time.Second},
	}
	for _, input := range claimCases {
		if _, err := store.ClaimPRWorkspaceOperations(t.Context(), input); !errors.Is(err, ErrInvalidPRWorkspace) {
			t.Errorf("invalid operation claim %#v error = %v", input, err)
		}
		if _, err := store.ClaimPRWorkspacePublications(t.Context(), input); !errors.Is(err, ErrInvalidPRWorkspace) {
			t.Errorf("invalid publication claim %#v error = %v", input, err)
		}
	}
	if claimed, err := store.ClaimPRWorkspaceOperations(t.Context(), PRWorkspaceClaimRequest{
		WorkerID: " worker ", Limit: 1, LeaseDuration: time.Minute,
	}); err != nil || len(claimed) != 0 {
		t.Fatalf("normalized empty operation claim = %#v, %v", claimed, err)
	}
	operationFinishes := []PRWorkspaceOperationFinish{
		{},
		{IntentID: "poi_00000000000000000000000000000001", LeaseToken: "bad", State: PRExecutionSucceeded},
		{
			IntentID:   "poi_00000000000000000000000000000001",
			LeaseToken: "plt_00000000000000000000000000000001",
			State:      PRExecutionRunning,
		},
		{
			IntentID:   "poi_00000000000000000000000000000001",
			LeaseToken: "plt_00000000000000000000000000000001",
			State:      PRExecutionSucceeded,
			Result:     json.RawMessage(`{`),
		},
	}
	for _, input := range operationFinishes {
		if _, err := store.FinishPRWorkspaceOperation(t.Context(), input); !errors.Is(err, ErrInvalidPRWorkspace) {
			t.Errorf("invalid operation finish %#v error = %v", input, err)
		}
	}
	publicationFinishes := []PRWorkspacePublicationFinish{
		{},
		{PublicationID: "ppb_00000000000000000000000000000001", LeaseToken: "bad", Status: PRPublicationFailed},
		{
			PublicationID: "ppb_00000000000000000000000000000001",
			LeaseToken:    "plt_00000000000000000000000000000001",
			Status:        PRPublicationPending,
		},
	}
	for _, input := range publicationFinishes {
		if _, err := store.FinishPRWorkspacePublication(t.Context(), input); !errors.Is(err, ErrInvalidPRWorkspace) {
			t.Errorf("invalid publication finish %#v error = %v", input, err)
		}
	}
	for _, state := range []PRExecutionState{
		PRExecutionSucceeded, PRExecutionFailed, PRExecutionBlocked, PRExecutionCanceled,
		PRExecutionStale, PRExecutionUnknown,
	} {
		if !validPRWorkspaceOperationFinishState(state) {
			t.Errorf("terminal operation state rejected: %q", state)
		}
	}
	if validPRWorkspaceOperationFinishState(PRExecutionRunning) {
		t.Fatal("running operation state accepted as terminal")
	}
}
