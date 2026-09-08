package repoaudit

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestIssueGenerationOwnershipAndSlotsAreWorkspaceWide(t *testing.T) {
	workspace := t.TempDir()
	first := NewStore(workspace)
	second := NewStore(workspace)
	releaseAttempt, acquired, err := first.TryLockIssueGenerationAttempt(
		"owner/repo", "draft", "generation",
	)
	if err != nil || !acquired {
		t.Fatalf("first attempt lock acquired=%v err=%v", acquired, err)
	}
	if release, duplicate, duplicateErr := second.TryLockIssueGenerationAttempt(
		"owner/repo", "draft", "generation",
	); duplicateErr != nil || duplicate || release != nil {
		t.Fatalf(
			"duplicate attempt lock release=%v acquired=%v err=%v",
			release != nil, duplicate, duplicateErr,
		)
	}
	releaseAttempt()
	releaseAttempt, acquired, err = second.TryLockIssueGenerationAttempt(
		"owner/repo", "draft", "generation",
	)
	if err != nil || !acquired {
		t.Fatalf("recovered attempt lock acquired=%v err=%v", acquired, err)
	}
	releaseAttempt()

	slots := make([]func(), 0, 4)
	for range 4 {
		release, slotErr := first.AcquireIssueGenerationSlot(t.Context(), 4)
		if slotErr != nil {
			t.Fatalf("acquire issue-writer slot %d: %v", len(slots), slotErr)
		}
		slots = append(slots, release)
	}
	defer func() {
		for _, release := range slots {
			if release != nil {
				release()
			}
		}
	}()
	blockedContext, cancelBlocked := context.WithTimeout(t.Context(), 75*time.Millisecond)
	defer cancelBlocked()
	if release, slotErr := second.AcquireIssueGenerationSlot(blockedContext, 4); release != nil ||
		!errors.Is(slotErr, context.DeadlineExceeded) {
		t.Fatalf("fifth issue-writer slot release=%v err=%v", release != nil, slotErr)
	}
	slots[0]()
	slots[0] = nil
	recoveredContext, cancelRecovered := context.WithTimeout(t.Context(), time.Second)
	defer cancelRecovered()
	release, err := second.AcquireIssueGenerationSlot(recoveredContext, 4)
	if err != nil {
		t.Fatalf("acquire released issue-writer slot: %v", err)
	}
	release()
}

func TestIssueGenerationReservesOneCanonicalDraftIdempotently(t *testing.T) {
	store, state := repositoryReviewIssueState(t, 1)
	finding := state.Findings[0]
	request := testIssueGenerationRequest(state.Repository, finding.ID, "generation-one")

	const callers = 12
	var created atomic.Int32
	var failures atomic.Int32
	var wait sync.WaitGroup
	for range callers {
		wait.Add(1)
		go func() {
			defer wait.Done()
			_, draft, didCreate, err := store.ReserveIssueGeneration(request)
			if err != nil || draft.State != IssueDraftGenerating ||
				draft.Origin != IssueDraftOriginAIGenerated {
				failures.Add(1)
				return
			}
			if didCreate {
				created.Add(1)
			}
		}()
	}
	wait.Wait()
	if failures.Load() != 0 || created.Load() != 1 {
		t.Fatalf("generation reservation failures=%d created=%d", failures.Load(), created.Load())
	}
	loaded, found, err := store.Get(state.Repository)
	if err != nil || !found || len(loaded.IssueDrafts) != 1 ||
		loaded.Findings[0].IssueDraftID != loaded.IssueDrafts[0].ID ||
		loaded.IssueDrafts[0].GeneratorProfileID != request.GeneratorProfileID ||
		loaded.IssueDrafts[0].GeneratorProfileVersion != request.GeneratorProfileVersion {
		t.Fatalf("reserved state=%#v found=%v err=%v", loaded, found, err)
	}
	if _, _, _, err := store.ReserveIssueGeneration(
		testIssueGenerationRequest(state.Repository, finding.ID, "generation-two"),
	); !errors.Is(err, ErrConflict) {
		t.Fatalf("second generation reservation error=%v", err)
	}
}

func TestIssueGenerationFailureRetryRegenerationAndDeletion(t *testing.T) {
	store, state := repositoryReviewIssueState(t, 1)
	finding := state.Findings[0]
	request := testIssueGenerationRequest(state.Repository, finding.ID, "generation-one")
	_, reserved, _, err := store.ReserveIssueGeneration(request)
	if err != nil {
		t.Fatal(err)
	}
	_, failed, err := store.CompleteIssueGeneration(
		state.Repository, reserved.ID, request.GenerationID, "", "", nil,
		"provider response included unsafe details",
	)
	if err != nil || failed.State != IssueDraftFailed ||
		failed.GenerationError != "Issue preview generation failed." {
		t.Fatalf("failed generation=%#v err=%v", failed, err)
	}
	request.ExpectedDraftVersion = failed.Version
	_, retrying, began, err := store.BeginIssueRegeneration(
		state.Repository, failed.ID, request,
	)
	if err != nil || !began || retrying.State != IssueDraftGenerating {
		t.Fatalf("retry reservation=%#v began=%v err=%v", retrying, began, err)
	}
	_, retryReplay, began, err := store.BeginIssueRegeneration(
		state.Repository, failed.ID, request,
	)
	if err != nil || began || retryReplay.State != IssueDraftGenerating {
		t.Fatalf("retry replay=%#v began=%v err=%v", retryReplay, began, err)
	}
	_, good, err := store.CompleteIssueGeneration(
		state.Repository, failed.ID, request.GenerationID,
		"Concurrent cache reads can return stale values",
		"## Evidence\n\nThe version check races with replacement.",
		[]string{"bug"}, "",
	)
	if err != nil || good.State != IssueDraftEditing || good.Title == "" || good.Body == "" {
		t.Fatalf("completed generation=%#v err=%v", good, err)
	}

	regeneration := testIssueGenerationRequest(state.Repository, finding.ID, "generation-two")
	regeneration.ResolvedInstructions = "Use a compact presentation for the new attempt."
	regeneration.InstructionsMode = IssueDraftInstructionsCustom
	regeneration.GeneratorModel = "replacement-writer"
	regeneration.GeneratorAccount = "replacement-account"
	regeneration.GeneratorProfileID = "rrpf_replacement"
	regeneration.GeneratorProfileVersion = 4
	regeneration.ExpectedDraftVersion = good.Version
	staleRegeneration := regeneration
	staleRegeneration.ExpectedDraftVersion++
	if _, _, _, staleErr := store.BeginIssueRegeneration(
		state.Repository, good.ID, staleRegeneration,
	); !errors.Is(staleErr, ErrConflict) {
		t.Fatalf("stale regeneration error=%v", staleErr)
	}
	_, regenerating, began, err := store.BeginIssueRegeneration(
		state.Repository, good.ID, regeneration,
	)
	if err != nil || !began || regenerating.Title != good.Title || regenerating.Body != good.Body ||
		regenerating.GenerationID != good.GenerationID ||
		regenerating.GeneratorModel != good.GeneratorModel ||
		regenerating.ResolvedInstructions != good.ResolvedInstructions ||
		regenerating.AttemptGenerationID != regeneration.GenerationID ||
		regenerating.AttemptGeneratorModel != regeneration.GeneratorModel ||
		regenerating.AttemptGeneratorProfileID != regeneration.GeneratorProfileID ||
		regenerating.AttemptGeneratorProfileVersion != regeneration.GeneratorProfileVersion ||
		regenerating.AttemptResolvedInstructions != regeneration.ResolvedInstructions {
		t.Fatalf("regeneration reservation=%#v began=%v err=%v", regenerating, began, err)
	}
	if _, replay, reserved, reserveErr := store.ReserveIssueGeneration(regeneration); reserveErr != nil ||
		reserved || replay.ID != regenerating.ID ||
		replay.AttemptGenerationID != regeneration.GenerationID {
		t.Fatalf("active regeneration replay=%#v reserved=%v err=%v", replay, reserved, reserveErr)
	}
	_, preserved, err := store.CompleteIssueGeneration(
		state.Repository, good.ID, regeneration.GenerationID, "", "", nil, "unsafe provider failure",
	)
	if err != nil || preserved.State != IssueDraftEditing || preserved.Title != good.Title ||
		preserved.Body != good.Body || preserved.GenerationError == "" ||
		preserved.GenerationID != good.GenerationID ||
		preserved.GeneratorModel != good.GeneratorModel ||
		preserved.GeneratorAccount != good.GeneratorAccount ||
		preserved.ResolvedInstructions != good.ResolvedInstructions ||
		preserved.InstructionsMode != good.InstructionsMode ||
		preserved.AttemptGenerationID != regeneration.GenerationID ||
		preserved.AttemptGeneratorModel != regeneration.GeneratorModel ||
		preserved.AttemptGeneratorAccount != regeneration.GeneratorAccount ||
		preserved.AttemptGeneratorProfileID != regeneration.GeneratorProfileID ||
		preserved.AttemptGeneratorProfileVersion != regeneration.GeneratorProfileVersion ||
		preserved.AttemptResolvedInstructions != regeneration.ResolvedInstructions ||
		preserved.AttemptInstructionsMode != regeneration.InstructionsMode {
		t.Fatalf("preserved preview=%#v err=%v", preserved, err)
	}
	_, replayedFailure, err := store.CompleteIssueGeneration(
		state.Repository, good.ID, regeneration.GenerationID, "", "", nil,
		"replayed provider failure",
	)
	if err != nil || replayedFailure.Version != preserved.Version ||
		replayedFailure.GenerationID != good.GenerationID {
		t.Fatalf("replayed failed regeneration=%#v err=%v", replayedFailure, err)
	}
	successfulRegeneration := testIssueGenerationRequest(
		state.Repository, finding.ID, "generation-three",
	)
	successfulRegeneration.ResolvedInstructions = "Use the successful compact presentation."
	successfulRegeneration.InstructionsMode = IssueDraftInstructionsCustom
	successfulRegeneration.GeneratorModel = "successful-writer"
	successfulRegeneration.GeneratorAccount = "successful-account"
	successfulRegeneration.GeneratorProfileID = "rrpf_successful"
	successfulRegeneration.GeneratorProfileVersion = 5
	successfulRegeneration.ExpectedDraftVersion = preserved.Version
	_, regenerating, began, err = store.BeginIssueRegeneration(
		state.Repository, good.ID, successfulRegeneration,
	)
	if err != nil || !began {
		t.Fatalf("successful regeneration reservation=%#v began=%v err=%v", regenerating, began, err)
	}
	_, promoted, err := store.CompleteIssueGeneration(
		state.Repository, good.ID, successfulRegeneration.GenerationID,
		"Updated preview", "Updated grounded body", []string{"bug"}, "",
	)
	if err != nil || promoted.GenerationID != successfulRegeneration.GenerationID ||
		promoted.GeneratorModel != successfulRegeneration.GeneratorModel ||
		promoted.GeneratorAccount != successfulRegeneration.GeneratorAccount ||
		promoted.GeneratorProfileID != successfulRegeneration.GeneratorProfileID ||
		promoted.GeneratorProfileVersion != successfulRegeneration.GeneratorProfileVersion ||
		promoted.ResolvedInstructions != successfulRegeneration.ResolvedInstructions ||
		promoted.InstructionsMode != successfulRegeneration.InstructionsMode ||
		promoted.AttemptGenerationID != "" || promoted.GenerationError != "" {
		t.Fatalf("promoted regeneration=%#v err=%v", promoted, err)
	}
	deleted, err := store.DeleteIssueDraft(state.Repository, promoted.ID, promoted.Version)
	if err != nil || len(deleted.IssueDrafts) != 0 || deleted.Findings[0].IssueDraftID != "" {
		t.Fatalf("deleted preview state=%#v err=%v", deleted, err)
	}
}

func TestExistingIssueMayBeReusedAndReversibleLinksCanBeUnlinked(t *testing.T) {
	store, state := repositoryReviewIssueState(t, 2)
	issueURL := "https://github.com/owner/repo/issues/42"
	for index := range state.Findings {
		current, found, err := store.Get(state.Repository)
		if err != nil || !found {
			t.Fatalf("load state found=%v err=%v", found, err)
		}
		finding := current.Findings[index]
		_, linked, err := store.LinkExistingIssue(ExistingIssueLink{
			Repository: state.Repository, FindingID: finding.ID,
			ExpectedFindingVersion: finding.Version,
			ExternalID:             "42", ExternalURL: issueURL,
			Title: "Existing issue", Body: "Existing diagnosis", Confirmed: true,
		})
		if err != nil || linked.Origin != IssueDraftOriginLinked || linked.State != IssueDraftPosted {
			t.Fatalf("linked issue=%#v err=%v", linked, err)
		}
	}
	linkedState, _, err := store.Get(state.Repository)
	if err != nil || len(linkedState.IssueDrafts) != 2 ||
		linkedState.IssueDrafts[0].ExternalURL != linkedState.IssueDrafts[1].ExternalURL {
		t.Fatalf("reused linked issue state=%#v err=%v", linkedState, err)
	}
	first := linkedState.Findings[0]
	replacedState, replacement, err := store.LinkExistingIssue(ExistingIssueLink{
		Repository: state.Repository, FindingID: first.ID,
		ExpectedFindingVersion: first.Version,
		ExternalID:             "43", ExternalURL: "https://github.com/owner/repo/issues/43",
		Title: "Replacement issue", Confirmed: true, Replace: true,
	})
	if err != nil || replacement.ExternalID != "43" || len(replacedState.IssueDrafts) != 2 ||
		replacedState.Findings[0].IssueDraftID != replacement.ID {
		t.Fatalf("replacement=%#v state=%#v err=%v", replacement, replacedState, err)
	}
	first = replacedState.Findings[0]
	unlinked, err := store.UnlinkExistingIssue(
		state.Repository, first.ID, first.Version, true,
	)
	if err != nil || unlinked.Findings[0].Status != FindingOpen ||
		unlinked.Findings[0].IssueDraftID != "" || len(unlinked.IssueDrafts) != 1 {
		t.Fatalf("unlinked state=%#v err=%v", unlinked, err)
	}

	second := unlinked.Findings[1]
	if _, err := store.UnlinkExistingIssue(
		state.Repository, second.ID, second.Version+1, true,
	); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale unlink error=%v", err)
	}
}

func TestDiscoveredIssueAssociationIsReversible(t *testing.T) {
	store, state := repositoryReviewIssueState(t, 1)
	finding := state.Findings[0]
	linkedState, discovered, err := store.LinkExistingIssue(ExistingIssueLink{
		Repository: state.Repository, FindingID: finding.ID,
		ExpectedFindingVersion: finding.Version,
		ExternalID:             "42",
		ExternalURL:            "https://github.com/owner/repo/issues/42",
		Title:                  "Discovered issue",
		Origin:                 IssueDraftOriginDiscovered,
		Confirmed:              true,
	})
	if err != nil || discovered.Origin != IssueDraftOriginDiscovered ||
		linkedState.Findings[0].IssueDraftID != discovered.ID {
		t.Fatalf("discovered link=%#v state=%#v err=%v", discovered, linkedState, err)
	}
	finding = linkedState.Findings[0]
	linkedState, discovered, err = store.LinkExistingIssue(ExistingIssueLink{
		Repository: state.Repository, FindingID: finding.ID,
		ExpectedFindingVersion: finding.Version,
		ExternalID:             "43",
		ExternalURL:            "https://github.com/owner/repo/issues/43",
		Title:                  "Replacement discovered issue",
		Origin:                 IssueDraftOriginDiscovered,
		Confirmed:              true,
		Replace:                true,
	})
	if err != nil || discovered.ExternalID != "43" || len(linkedState.IssueDrafts) != 1 {
		t.Fatalf("replacement discovered link=%#v state=%#v err=%v", discovered, linkedState, err)
	}
	finding = linkedState.Findings[0]
	unlinked, err := store.UnlinkExistingIssue(
		state.Repository, finding.ID, finding.Version, true,
	)
	if err != nil || unlinked.Findings[0].Status != FindingOpen ||
		unlinked.Findings[0].IssueDraftID != "" || len(unlinked.IssueDrafts) != 0 {
		t.Fatalf("unlinked discovered state=%#v err=%v", unlinked, err)
	}
}

func TestCreatedIssueAssociationIsPermanent(t *testing.T) {
	store, state := repositoryReviewIssueState(t, 1)
	finding := state.Findings[0]
	request := testIssueGenerationRequest(state.Repository, finding.ID, "generation")
	_, draft, _, err := store.ReserveIssueGeneration(request)
	if err != nil {
		t.Fatal(err)
	}
	_, draft, err = store.CompleteIssueGeneration(
		state.Repository, draft.ID, request.GenerationID, "Bug title", "Bug body", []string{"bug"}, "",
	)
	if err != nil {
		t.Fatal(err)
	}
	_, draft, claimed, err := store.ClaimIssueDraftPublication(state.Repository, draft.ID, draft.Version)
	if err != nil || !claimed {
		t.Fatalf("publication claim=%#v claimed=%v err=%v", draft, claimed, err)
	}
	postedState, draft, err := store.SetIssueDraftPublication(
		state.Repository, draft.ID, draft.Version, IssueDraftPosted,
		"77", "https://github.com/owner/repo/issues/77",
	)
	if err != nil {
		t.Fatal(err)
	}
	postedFinding := postedState.Findings[0]
	if _, err := store.UnlinkExistingIssue(
		state.Repository, postedFinding.ID, postedFinding.Version, true,
	); !errors.Is(err, ErrConflict) {
		t.Fatalf("created issue unlink error=%v", err)
	}
	if _, err := store.DeleteIssueDraft(state.Repository, draft.ID, draft.Version); !errors.Is(err, ErrConflict) {
		t.Fatalf("posted preview delete error=%v", err)
	}
}

func TestCanonicalIssueMutationBoundaryFailures(t *testing.T) {
	t.Run("generation reservation", func(t *testing.T) {
		store, state := repositoryReviewIssueState(t, 1)
		if _, _, _, err := store.ReserveIssueGeneration(IssueGenerationRequest{}); err == nil {
			t.Fatal("invalid generation reservation was accepted")
		}
		request := testIssueGenerationRequest(state.Repository, "missing", "generation")
		if _, _, _, err := store.ReserveIssueGeneration(request); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("missing finding reservation error=%v", err)
		}
	})

	t.Run("regeneration", func(t *testing.T) {
		store, state := repositoryReviewIssueState(t, 1)
		request := testIssueGenerationRequest(state.Repository, state.Findings[0].ID, "generation")
		if _, _, _, err := store.BeginIssueRegeneration(
			state.Repository, "missing", IssueGenerationRequest{},
		); err == nil {
			t.Fatal("invalid regeneration was accepted")
		}
		request.ExpectedDraftVersion = 1
		if _, _, _, err := store.BeginIssueRegeneration(
			state.Repository, "missing", request,
		); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("missing regeneration draft error=%v", err)
		}
		_, generating, _, err := store.ReserveIssueGeneration(request)
		if err != nil {
			t.Fatal(err)
		}
		request.GenerationID = "second"
		request.ExpectedDraftVersion = generating.Version
		if _, _, _, err := store.BeginIssueRegeneration(
			state.Repository, generating.ID, request,
		); !errors.Is(err, ErrConflict) {
			t.Fatalf("overlapping regeneration error=%v", err)
		}
	})

	t.Run("generation completion", func(t *testing.T) {
		store, state := repositoryReviewIssueState(t, 1)
		request := testIssueGenerationRequest(state.Repository, state.Findings[0].ID, "generation")
		_, draft, _, err := store.ReserveIssueGeneration(request)
		if err != nil {
			t.Fatal(err)
		}
		if _, _, completionErr := store.CompleteIssueGeneration(
			state.Repository, "missing", request.GenerationID, "title", "body", nil, "",
		); !errors.Is(completionErr, os.ErrNotExist) {
			t.Fatalf("missing completion draft error=%v", completionErr)
		}
		if _, _, completionErr := store.CompleteIssueGeneration(
			state.Repository, draft.ID, "wrong", "title", "body", nil, "",
		); !errors.Is(completionErr, ErrConflict) {
			t.Fatalf("wrong generation completion error=%v", completionErr)
		}
		if _, _, completionErr := store.CompleteIssueGeneration(
			state.Repository, draft.ID, request.GenerationID, "", "", nil, "",
		); completionErr == nil {
			t.Fatal("empty generated preview was accepted")
		}
		_, draft, err = store.CompleteIssueGeneration(
			state.Repository, draft.ID, request.GenerationID, "title", "body", nil, "",
		)
		if err != nil || len(draft.Labels) != 1 || draft.Labels[0] != "bug" {
			t.Fatalf("default labels draft=%#v err=%v", draft, err)
		}
		_, draft, _, err = store.ClaimIssueDraftPublication(state.Repository, draft.ID, draft.Version)
		if err != nil {
			t.Fatal(err)
		}
		if _, _, err := store.CompleteIssueGeneration(
			state.Repository, draft.ID, request.GenerationID, "title", "body", nil, "",
		); !errors.Is(err, ErrConflict) {
			t.Fatalf("publishing completion error=%v", err)
		}
	})

	t.Run("delete", func(t *testing.T) {
		store, state := repositoryReviewIssueState(t, 1)
		if _, err := store.DeleteIssueDraft(state.Repository, "missing", 1); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("missing delete error=%v", err)
		}
		request := testIssueGenerationRequest(state.Repository, state.Findings[0].ID, "generation")
		_, draft, _, err := store.ReserveIssueGeneration(request)
		if err != nil {
			t.Fatal(err)
		}
		_, draft, err = store.CompleteIssueGeneration(
			state.Repository, draft.ID, request.GenerationID, "title", "body", nil, "",
		)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := store.DeleteIssueDraft(
			state.Repository, draft.ID, draft.Version+1,
		); !errors.Is(err, ErrConflict) {
			t.Fatalf("stale delete error=%v", err)
		}
		if _, err := store.DeleteIssueDraft(state.Repository, draft.ID, draft.Version); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("existing issue link", func(t *testing.T) {
		store, state := repositoryReviewIssueState(t, 2)
		if _, _, err := store.LinkExistingIssue(ExistingIssueLink{}); err == nil {
			t.Fatal("invalid existing issue link was accepted")
		}
		request := ExistingIssueLink{
			Repository: state.Repository, FindingID: "missing", ExpectedFindingVersion: 1,
			ExternalID: "1", ExternalURL: "https://github.com/owner/repo/issues/1",
			Title: "title", Confirmed: true,
		}
		if _, _, err := store.LinkExistingIssue(request); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("missing link finding error=%v", err)
		}
		request.FindingID = state.Findings[0].ID
		request.ExpectedFindingVersion = state.Findings[0].Version + 1
		if _, _, err := store.LinkExistingIssue(request); !errors.Is(err, ErrConflict) {
			t.Fatalf("stale link error=%v", err)
		}
		request.ExpectedFindingVersion = state.Findings[0].Version
		linkedState, linked, err := store.LinkExistingIssue(request)
		if err != nil {
			t.Fatal(err)
		}
		request.ExpectedFindingVersion = linkedState.Findings[0].Version
		if _, _, err := store.LinkExistingIssue(request); !errors.Is(err, ErrConflict) {
			t.Fatalf("unconfirmed replacement error=%v", err)
		}
		request.Replace = true
		if _, replay, err := store.LinkExistingIssue(request); err != nil || replay.ID != linked.ID {
			t.Fatalf("idempotent replacement=%#v err=%v", replay, err)
		}
		if _, err := store.UnlinkExistingIssue(
			state.Repository, state.Findings[1].ID, state.Findings[1].Version, false,
		); err == nil {
			t.Fatal("unconfirmed unlink was accepted")
		}
		if _, err := store.UnlinkExistingIssue(state.Repository, "missing", 1, true); !errors.Is(
			err, os.ErrNotExist,
		) {
			t.Fatalf("missing unlink finding error=%v", err)
		}
		if _, err := store.UnlinkExistingIssue(
			state.Repository, state.Findings[1].ID, state.Findings[1].Version, true,
		); !errors.Is(err, ErrConflict) {
			t.Fatalf("unassociated unlink error=%v", err)
		}
	})
}

func TestCanonicalIssueDraftUpdateBoundaries(t *testing.T) {
	store, state := repositoryReviewIssueState(t, 1)
	request := testIssueGenerationRequest(state.Repository, state.Findings[0].ID, "draft-update")
	state, draft, _, err := store.ReserveIssueGeneration(request)
	if err != nil {
		t.Fatal(err)
	}
	state, draft, err = store.CompleteIssueGeneration(
		state.Repository, draft.ID, request.GenerationID,
		"Original", "Original body", []string{"bug"}, "",
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, updateErr := store.UpdateIssueDraft(
		state.Repository, "missing", "title", "body", nil, 1,
	); !errors.Is(updateErr, os.ErrNotExist) {
		t.Fatalf("missing draft error=%v", updateErr)
	}
	unchanged, unchangedDraft, err := store.UpdateIssueDraft(
		state.Repository, draft.ID, " Original ", " Original body ", []string{" bug "}, 99,
	)
	if err != nil || unchangedDraft.Version != draft.Version || unchanged.Version != state.Version {
		t.Fatalf("no-op update state=%#v draft=%#v err=%v", unchanged, unchangedDraft, err)
	}
	if _, _, updateErr := store.UpdateIssueDraft(
		state.Repository, draft.ID, "Changed", "body", nil, draft.Version+1,
	); !errors.Is(updateErr, ErrConflict) {
		t.Fatalf("stale draft update error=%v", updateErr)
	}
	if _, _, updateErr := store.UpdateIssueDraft(
		state.Repository, draft.ID, "", "body", nil, draft.Version,
	); updateErr == nil {
		t.Fatal("empty issue title was accepted")
	}
	updated, updatedDraft, err := store.UpdateIssueDraft(
		state.Repository, draft.ID, " Updated ", " Updated body ",
		[]string{" bug ", "bug", "triage"}, draft.Version,
	)
	if err != nil || updatedDraft.Version != draft.Version+1 || updatedDraft.Title != "Updated" ||
		len(updatedDraft.Labels) != 2 || updated.Version != state.Version+1 {
		t.Fatalf("updated state=%#v draft=%#v err=%v", updated, updatedDraft, err)
	}
	_, publishing, _, err := store.ClaimIssueDraftPublication(
		state.Repository, updatedDraft.ID, updatedDraft.Version,
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.UpdateIssueDraft(
		state.Repository, publishing.ID, "after claim", "body", nil, publishing.Version,
	); !errors.Is(err, ErrConflict) {
		t.Fatalf("publishing draft edit error=%v", err)
	}
}

func TestCanonicalIssueMutationSaveFailures(t *testing.T) {
	assertFailure := func(t *testing.T, err error) {
		t.Helper()
		if err == nil {
			t.Fatal("mutation unexpectedly persisted through an unsafe root replacement")
		}
	}

	t.Run("generation reservation", func(t *testing.T) {
		store, state := repositoryReviewIssueState(t, 1)
		failNextCanonicalIssueSave(t, &store)
		_, _, _, err := store.ReserveIssueGeneration(testIssueGenerationRequest(
			state.Repository, state.Findings[0].ID, "generation",
		))
		assertFailure(t, err)
	})
	t.Run("draft update", func(t *testing.T) {
		store, state := repositoryReviewIssueState(t, 1)
		request := testIssueGenerationRequest(state.Repository, state.Findings[0].ID, "update")
		_, draft, _, err := store.ReserveIssueGeneration(request)
		if err != nil {
			t.Fatal(err)
		}
		_, draft, err = store.CompleteIssueGeneration(
			state.Repository, draft.ID, request.GenerationID, "title", "body", nil, "",
		)
		if err != nil {
			t.Fatal(err)
		}
		failNextCanonicalIssueSave(t, &store)
		_, _, err = store.UpdateIssueDraft(
			state.Repository, draft.ID, "changed", "changed body", nil, draft.Version,
		)
		assertFailure(t, err)
	})
	t.Run("regeneration", func(t *testing.T) {
		store, state := repositoryReviewIssueState(t, 1)
		request := testIssueGenerationRequest(state.Repository, state.Findings[0].ID, "first")
		_, draft, _, err := store.ReserveIssueGeneration(request)
		if err != nil {
			t.Fatal(err)
		}
		_, draft, err = store.CompleteIssueGeneration(
			state.Repository, draft.ID, request.GenerationID, "title", "body", nil, "",
		)
		if err != nil {
			t.Fatal(err)
		}
		request.GenerationID = "second"
		request.ExpectedDraftVersion = draft.Version
		failNextCanonicalIssueSave(t, &store)
		_, _, _, err = store.BeginIssueRegeneration(state.Repository, draft.ID, request)
		assertFailure(t, err)
	})
	t.Run("completion", func(t *testing.T) {
		store, state := repositoryReviewIssueState(t, 1)
		request := testIssueGenerationRequest(state.Repository, state.Findings[0].ID, "generation")
		_, draft, _, err := store.ReserveIssueGeneration(request)
		if err != nil {
			t.Fatal(err)
		}
		failNextCanonicalIssueSave(t, &store)
		_, _, err = store.CompleteIssueGeneration(
			state.Repository, draft.ID, request.GenerationID, "title", "body", nil, "",
		)
		assertFailure(t, err)
	})
	t.Run("delete", func(t *testing.T) {
		store, state := repositoryReviewIssueState(t, 1)
		request := testIssueGenerationRequest(state.Repository, state.Findings[0].ID, "generation")
		_, draft, _, err := store.ReserveIssueGeneration(request)
		if err != nil {
			t.Fatal(err)
		}
		_, draft, err = store.CompleteIssueGeneration(
			state.Repository, draft.ID, request.GenerationID, "title", "body", nil, "",
		)
		if err != nil {
			t.Fatal(err)
		}
		failNextCanonicalIssueSave(t, &store)
		_, err = store.DeleteIssueDraft(state.Repository, draft.ID, draft.Version)
		assertFailure(t, err)
	})
	t.Run("link", func(t *testing.T) {
		store, state := repositoryReviewIssueState(t, 1)
		failNextCanonicalIssueSave(t, &store)
		_, _, err := store.LinkExistingIssue(ExistingIssueLink{
			Repository: state.Repository, FindingID: state.Findings[0].ID,
			ExpectedFindingVersion: state.Findings[0].Version,
			ExternalID:             "1", ExternalURL: "https://github.com/owner/repo/issues/1",
			Title: "title", Confirmed: true,
		})
		assertFailure(t, err)
	})
	t.Run("unlink", func(t *testing.T) {
		store, state := repositoryReviewIssueState(t, 1)
		linkedState, _, err := store.LinkExistingIssue(ExistingIssueLink{
			Repository: state.Repository, FindingID: state.Findings[0].ID,
			ExpectedFindingVersion: state.Findings[0].Version,
			ExternalID:             "1", ExternalURL: "https://github.com/owner/repo/issues/1",
			Title: "title", Confirmed: true,
		})
		if err != nil {
			t.Fatal(err)
		}
		failNextCanonicalIssueSave(t, &store)
		_, err = store.UnlinkExistingIssue(
			state.Repository, linkedState.Findings[0].ID, linkedState.Findings[0].Version, true,
		)
		assertFailure(t, err)
	})
}

func TestCanonicalIssueAssociationValidationBoundaries(t *testing.T) {
	store, state := repositoryReviewIssueState(t, 1)
	request := testIssueGenerationRequest(state.Repository, state.Findings[0].ID, "validation")
	valid, _, _, err := store.ReserveIssueGeneration(request)
	if err != nil {
		t.Fatal(err)
	}
	clone := func() RepositoryState {
		encoded, err := json.Marshal(valid)
		if err != nil {
			t.Fatal(err)
		}
		var copied RepositoryState
		if err := json.Unmarshal(encoded, &copied); err != nil {
			t.Fatal(err)
		}
		return copied
	}
	cases := map[string]func(*RepositoryState){
		"invalid finding": func(state *RepositoryState) { state.Findings[0].ID = "" },
		"duplicate finding": func(state *RepositoryState) {
			state.Findings = append(state.Findings, state.Findings[0])
		},
		"invalid finding association": func(state *RepositoryState) {
			state.Findings[0].IssueDraftID = strings.Repeat("x", 257)
		},
		"invalid draft": func(state *RepositoryState) { state.IssueDrafts[0].ID = "" },
		"duplicate draft": func(state *RepositoryState) {
			state.IssueDrafts = append(state.IssueDrafts, state.IssueDrafts[0])
		},
		"invalid origin": func(state *RepositoryState) { state.IssueDrafts[0].Origin = "invalid" },
		"invalid state":  func(state *RepositoryState) { state.IssueDrafts[0].State = "invalid" },
		"invalid content": func(state *RepositoryState) {
			state.IssueDrafts[0].State = IssueDraftEditing
		},
		"invalid metadata": func(state *RepositoryState) {
			state.IssueDrafts[0].GenerationError = strings.Repeat("x", maxIssueGenerationErrorBytes+1)
		},
		"invalid instructions mode": func(state *RepositoryState) {
			state.IssueDrafts[0].InstructionsMode = "invalid"
		},
		"invalid attempt mode": func(state *RepositoryState) {
			state.IssueDrafts[0].AttemptInstructionsMode = "invalid"
		},
		"partial attempt": func(state *RepositoryState) {
			state.IssueDrafts[0].AttemptGeneratorAccount = ""
		},
		"attempt on linked": func(state *RepositoryState) {
			state.IssueDrafts[0].Origin = IssueDraftOriginLinked
		},
		"settled attempt without error": func(state *RepositoryState) {
			state.IssueDrafts[0].State = IssueDraftEditing
			state.IssueDrafts[0].Title = "title"
			state.IssueDrafts[0].Body = "body"
		},
		"invalid generated preview": func(state *RepositoryState) {
			state.IssueDrafts[0].GenerationID = ""
		},
		"invalid linked preview": func(state *RepositoryState) {
			draft := &state.IssueDrafts[0]
			draft.Origin = IssueDraftOriginLinked
			draft.AttemptGenerationID = ""
			draft.AttemptResolvedInstructions = ""
			draft.AttemptInstructionsMode = ""
			draft.AttemptGeneratorModel = ""
			draft.AttemptGeneratorAccount = ""
			draft.State = IssueDraftPosted
		},
		"missing finding reference": func(state *RepositoryState) {
			state.IssueDrafts[0].FindingIDs[0] = "missing"
		},
		"invalid label": func(state *RepositoryState) {
			state.IssueDrafts[0].Labels = []string{strings.Repeat("x", 51)}
		},
		"missing canonical draft": func(state *RepositoryState) {
			state.Findings[0].IssueDraftID = "missing"
		},
		"incomplete canonical references": func(state *RepositoryState) {
			state.Findings[0].IssueDraftID = ""
		},
		"invalid profile provenance": func(state *RepositoryState) {
			state.IssueDrafts[0].GeneratorProfileID = ""
		},
	}
	if err := validateIssueAssociations(valid); err != nil {
		t.Fatalf("valid association error=%v", err)
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			state := clone()
			mutate(&state)
			if err := validateIssueAssociations(state); err == nil {
				t.Fatalf("invalid association was accepted: %#v", state)
			}
		})
	}
	promoteIssueDraftAttempt(nil)
	promoteIssueDraftAttempt(&IssueDraft{})
	clearIssueDraftAttempt(nil)
	if issueDraftContainsFinding(IssueDraft{FindingIDs: []string{"one"}}, "two") {
		t.Fatal("missing issue finding was reported present")
	}
}

func failNextCanonicalIssueSave(t *testing.T, store *Store) {
	t.Helper()
	root := store.root
	moved := root + ".before-failed-save"
	target := filepath.Join(t.TempDir(), "replacement")
	if err := os.MkdirAll(target, 0o700); err != nil {
		t.Fatal(err)
	}
	var once sync.Once
	store.now = func() time.Time {
		once.Do(func() {
			if err := os.Rename(root, moved); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(target, root); err != nil {
				t.Fatal(err)
			}
		})
		return repositoryAuditTestNow.Add(time.Hour)
	}
}

func TestProfileIssueWriterDefaultsAtAutomationSnapshot(t *testing.T) {
	profile := validProfileForTest("rrpf_writer", "Writer")
	profile.SchemaVersion = RepositoryReviewProfileSchemaVersion
	profile.Version = 1
	profile.CreatedAt = repositoryAuditTestNow
	profile.UpdatedAt = repositoryAuditTestNow
	profile.IssueWriterModel = ""
	automation, err := MaterializeRepositoryReviewAutomation(profile, RepositoryReviewAutomation{
		Repository: "owner/repo", Ref: "main",
	})
	if err != nil || automation.IssueWriterModel != profile.ReviewerModel {
		t.Fatalf("default writer automation=%#v err=%v", automation, err)
	}
	profile.IssueWriterModel = "writer-model"
	automation, err = MaterializeRepositoryReviewAutomation(profile, RepositoryReviewAutomation{
		Repository: "owner/repo", Ref: "main",
	})
	if err != nil || automation.IssueWriterModel != "writer-model" {
		t.Fatalf("explicit writer automation=%#v err=%v", automation, err)
	}
}

func repositoryReviewIssueState(t *testing.T, findingCount int) (Store, RepositoryState) {
	t.Helper()
	store := NewStore(t.TempDir())
	var state RepositoryState
	for index := range findingCount {
		hints := MatchHints{
			Component: fmt.Sprintf("component-%d", index),
			Operation: fmt.Sprintf("operation-%d", index), FailureMode: "state is lost",
			Trigger: "overlapping operations", ViolatedInvariant: "accepted state remains durable",
			ObservableOutcome: "data disappears", RelatedSymbols: []string{"Save"},
			SourceAnchors:       []string{fmt.Sprintf("anchor-%d", index)},
			DistinguishingFacts: []string{fmt.Sprintf("case-%d", index)},
		}
		var finding Finding
		state, finding = recordLifecycleFinding(
			t,
			store,
			strings.Repeat(fmt.Sprintf("%x", index+1), 40),
			strings.Repeat(fmt.Sprintf("%x", index+3), 40),
			fmt.Sprintf("issue-run-%d", index),
			"main",
			"main",
			true,
			fmt.Sprintf("issue finding %d", index),
			hints,
		)
		job := lifecycleJobForFinding(t, state, finding.ID)
		job = claimLifecycleMappingJob(t, store, state.Repository, job, RepositoryMappingModelSnapshot{})
		var err error
		state, _, err = store.CompleteMappingJob(state.Repository, RepositoryMappingCompletion{
			JobID: job.ID, CreateMatchState: RepositoryMatchNew, DefaultBranchVerified: true,
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	return store, state
}

func testIssueGenerationRequest(repository, findingID, generationID string) IssueGenerationRequest {
	return IssueGenerationRequest{
		Repository: repository, FindingID: findingID, GenerationID: generationID,
		ResolvedInstructions: "Write a concise grounded GitHub issue without proposing a fix.",
		InstructionsMode:     IssueDraftInstructionsDefault,
		GeneratorModel:       "writer-model", GeneratorAccount: "default-account",
		GeneratorProfileID: "rrpf_writer", GeneratorProfileVersion: 3,
	}
}
