package repoaudit

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"math"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type repositoryReviewCancelAfterFirstContext struct {
	context.Context
	first    chan struct{}
	calls    atomic.Int32
	canceled atomic.Bool
}

type repositoryReviewCoverageDirEntry struct {
	name    string
	info    os.FileInfo
	infoErr error
}

func (entry repositoryReviewCoverageDirEntry) Name() string { return entry.name }
func (entry repositoryReviewCoverageDirEntry) IsDir() bool {
	return entry.info != nil && entry.info.IsDir()
}
func (entry repositoryReviewCoverageDirEntry) Type() os.FileMode { return 0 }
func (entry repositoryReviewCoverageDirEntry) Info() (os.FileInfo, error) {
	return entry.info, entry.infoErr
}

func (ctx *repositoryReviewCancelAfterFirstContext) Err() error {
	if ctx.calls.Add(1) == 1 {
		close(ctx.first)
		return nil
	}
	if ctx.canceled.Load() {
		return context.Canceled
	}
	return nil
}

func repositoryReviewCoverageState(repository string) RepositoryState {
	now := repositoryAuditTestNow
	return RepositoryState{
		SchemaVersion:           SchemaVersion,
		ID:                      RepositoryID(repository),
		Repository:              repository,
		Version:                 1,
		ReviewVersion:           1,
		Files:                   map[string]ReviewedFile{},
		Unsupported:             map[string]UnsupportedFile{},
		ReviewAttempts:          map[string]int{},
		ReviewAttemptIdentities: map[string]string{},
		Findings:                []Finding{},
		Contexts:                []FindingContext{},
		Runs:                    []ReviewRun{},
		IssueDrafts:             []IssueDraft{},
		UpdatedAt:               now,
	}
}

func repositoryReviewCoverageStore(t *testing.T, repository string) (Store, RepositoryState) {
	t.Helper()
	store := newRepositoryAuditTestStore(t)
	state := repositoryReviewCoverageState(repository)
	if saveErr := store.save(&state); saveErr != nil {
		t.Fatal(saveErr)
	}
	return store, state
}

func repositoryReviewDenyPermissions(t *testing.T, path string, restore os.FileMode) {
	t.Helper()
	if err := os.Chmod(path, 0); err != nil {
		t.Skipf("cannot restrict %s: %v", path, err)
	}
	t.Cleanup(func() {
		if err := os.Chmod(path, restore); err != nil && !os.IsNotExist(err) {
			t.Errorf("restore permissions for %s: %v", path, err)
		}
	})
}

func TestRepositoryReviewIssueDraftMutationBoundaries(t *testing.T) {
	repository := "owner/repo"
	store, state := repositoryReviewCoverageStore(t, repository)
	now := repositoryAuditTestNow
	state.Findings = []Finding{{
		ID: "finding-1", Repository: repository, Status: FindingOpen,
		Version: 1, CreatedAt: now, UpdatedAt: now,
	}}
	state.IssueDrafts = []IssueDraft{{
		ID: "draft-1", Repository: repository, FindingIDs: []string{"finding-1"},
		Title: "Original", Body: "Original body", Labels: []string{"bug"},
		State: IssueDraftEditing, Version: 1, CreatedAt: now, UpdatedAt: now,
	}}
	if err := store.save(&state); err != nil {
		t.Fatal(err)
	}

	if _, _, missingDraftErr := store.UpdateIssueDraft(
		repository,
		"missing",
		"title",
		"body",
		nil,
		1,
	); !errors.Is(
		missingDraftErr,
		os.ErrNotExist,
	) {
		t.Fatalf("missing draft error = %v", missingDraftErr)
	}
	unchanged, unchangedDraft, err := store.UpdateIssueDraft(
		repository, "draft-1", " Original ", " Original body ", []string{" bug "}, 99,
	)
	if err != nil || unchangedDraft.Version != 1 || unchanged.Version != state.Version {
		t.Fatalf("no-op update state=%#v draft=%#v err=%v", unchanged, unchangedDraft, err)
	}
	if _, _, staleUpdateErr := store.UpdateIssueDraft(
		repository,
		"draft-1",
		"Changed",
		"body",
		nil,
		2,
	); !errors.Is(
		staleUpdateErr,
		ErrConflict,
	) {
		t.Fatalf("stale draft update error = %v", staleUpdateErr)
	}
	if _, _, invalidTitleErr := store.UpdateIssueDraft(
		repository,
		"draft-1",
		"",
		"body",
		nil,
		1,
	); invalidTitleErr == nil {
		t.Fatal("empty issue title was accepted")
	}
	updated, draft, err := store.UpdateIssueDraft(
		repository, "draft-1", " Updated ", " Updated body ", []string{" bug ", "bug", "triage"}, 1,
	)
	if err != nil || draft.Version != 2 || draft.Title != "Updated" ||
		len(draft.Labels) != 2 || updated.Version != state.Version+1 {
		t.Fatalf("updated state=%#v draft=%#v err=%v", updated, draft, err)
	}

	if _, _, invalidStateErr := store.SetIssueDraftPublication(
		repository,
		draft.ID,
		draft.Version,
		"invalid",
		"",
		"",
	); invalidStateErr == nil {
		t.Fatal("invalid publication state was accepted")
	}
	if _, _, missingPublicationErr := store.SetIssueDraftPublication(
		repository,
		"missing",
		1,
		IssueDraftUnknown,
		"",
		"",
	); !errors.Is(
		missingPublicationErr,
		os.ErrNotExist,
	) {
		t.Fatalf("missing publication draft error = %v", missingPublicationErr)
	}
	if _, _, editingPublicationErr := store.SetIssueDraftPublication(
		repository,
		draft.ID,
		draft.Version,
		IssueDraftUnknown,
		"",
		"",
	); !errors.Is(
		editingPublicationErr,
		ErrConflict,
	) {
		t.Fatalf("editing draft publication error = %v", editingPublicationErr)
	}
	if _, _, _, missingClaimErr := store.ClaimIssueDraftPublication(
		repository,
		"missing",
		1,
	); !errors.Is(
		missingClaimErr,
		os.ErrNotExist,
	) {
		t.Fatalf("missing claim error = %v", missingClaimErr)
	}
	if _, _, _, staleClaimErr := store.ClaimIssueDraftPublication(
		repository,
		draft.ID,
		draft.Version+1,
	); !errors.Is(
		staleClaimErr,
		ErrConflict,
	) {
		t.Fatalf("stale claim error = %v", staleClaimErr)
	}
	_, publishing, claimed, err := store.ClaimIssueDraftPublication(repository, draft.ID, draft.Version)
	if err != nil || !claimed || publishing.State != IssueDraftPublishing {
		t.Fatalf("claim draft=%#v claimed=%v err=%v", publishing, claimed, err)
	}
	if _, _, repeatClaimed, repeatClaimErr := store.ClaimIssueDraftPublication(
		repository,
		draft.ID,
		publishing.Version,
	); repeatClaimErr != nil ||
		repeatClaimed {
		t.Fatalf("repeat claim claimed=%v err=%v", repeatClaimed, repeatClaimErr)
	}
	if _, _, publishingEditErr := store.UpdateIssueDraft(
		repository,
		draft.ID,
		"after claim",
		"body",
		nil,
		publishing.Version,
	); !errors.Is(
		publishingEditErr,
		ErrConflict,
	) {
		t.Fatalf("publishing draft edit error = %v", publishingEditErr)
	}
	if _, _, invalidPostedErr := store.SetIssueDraftPublication(
		repository, draft.ID, publishing.Version, IssueDraftPosted, "", "http://invalid",
	); invalidPostedErr == nil {
		t.Fatal("posted draft without HTTPS identity was accepted")
	}
	_, unknown, err := store.SetIssueDraftPublication(
		repository, draft.ID, publishing.Version, IssueDraftUnknown, " ignored ", " ignored ",
	)
	if err != nil || unknown.State != IssueDraftUnknown || unknown.ExternalID != "ignored" {
		t.Fatalf("unknown draft=%#v err=%v", unknown, err)
	}
	if _, _, unknownEditingErr := store.SetIssueDraftPublication(
		repository, draft.ID, unknown.Version, IssueDraftEditing, "", "",
	); !errors.Is(unknownEditingErr, ErrConflict) {
		t.Fatalf("unknown-to-editing error = %v", unknownEditingErr)
	}
	_, posted, err := store.SetIssueDraftPublication(
		repository, draft.ID, unknown.Version, IssueDraftPosted,
		" 42 ", " https://github.com/owner/repo/issues/42 ",
	)
	if err != nil || posted.State != IssueDraftPosted || posted.ExternalID != "42" {
		t.Fatalf("posted draft=%#v err=%v", posted, err)
	}
}

func TestRepositoryReviewFindingAndIssueSelectionBoundaries(t *testing.T) {
	repository := "owner/repo"
	store, state := repositoryReviewCoverageStore(t, repository)
	now := repositoryAuditTestNow
	state.Findings = []Finding{{
		ID: "finding-1", Repository: repository, Status: FindingOpen,
		Title: "One", Version: 1, CreatedAt: now, UpdatedAt: now,
	}}
	if err := store.save(&state); err != nil {
		t.Fatal(err)
	}

	if _, err := store.SetFindingStatus(repository, "finding-1", "invalid", state.Version); err == nil {
		t.Fatal("invalid finding status was accepted")
	}
	if _, missingStatusErr := store.SetFindingStatus(
		repository,
		"missing",
		FindingDismissed,
		state.Version,
	); !errors.Is(
		missingStatusErr,
		os.ErrNotExist,
	) {
		t.Fatalf("missing finding error = %v", missingStatusErr)
	}
	unchanged, err := store.SetFindingStatus(repository, "finding-1", FindingOpen, 99)
	if err != nil || unchanged.Version != state.Version {
		t.Fatalf("no-op finding state=%#v err=%v", unchanged, err)
	}
	if _, staleStatusErr := store.SetFindingStatus(
		repository,
		"finding-1",
		FindingDismissed,
		state.Version+1,
	); !errors.Is(
		staleStatusErr,
		ErrConflict,
	) {
		t.Fatalf("stale finding mutation error = %v", staleStatusErr)
	}
	updated, err := store.SetFindingStatus(repository, "finding-1", FindingDismissed, state.Version)
	if err != nil || updated.Findings[0].Status != FindingDismissed {
		t.Fatalf("updated finding state=%#v err=%v", updated, err)
	}

	for _, request := range []IssueDraftRequest{
		{Repository: repository, ExpectedVersion: updated.Version},
		{Repository: repository, FindingIDs: []string{"missing"}, ExpectedVersion: updated.Version},
		{Repository: repository, FindingIDs: []string{"finding-1", "finding-1"}, ExpectedVersion: updated.Version},
	} {
		if _, _, selectionErr := store.PrepareIssue(request); selectionErr == nil {
			t.Fatalf("invalid issue selection %#v was accepted", request)
		}
	}
	if _, _, stalePrepareErr := store.PrepareIssue(IssueDraftRequest{
		Repository: repository, FindingIDs: []string{"finding-1"}, Title: "title", Body: "body",
		ExpectedVersion: updated.Version + 1,
	}); !errors.Is(stalePrepareErr, ErrConflict) {
		t.Fatalf("stale issue preparation error = %v", stalePrepareErr)
	}
	if _, _, oversizedTitleErr := store.PrepareIssue(IssueDraftRequest{
		Repository: repository, FindingIDs: []string{"finding-1"},
		Title: strings.Repeat("x", 257), Body: "body", ExpectedVersion: updated.Version,
	}); oversizedTitleErr == nil {
		t.Fatal("oversized issue title was accepted")
	}
	withDraft, draft, err := store.PrepareIssue(IssueDraftRequest{
		Repository: repository, FindingIDs: []string{"finding-1"},
		Title: " Explicit title ", Body: " Explicit body ",
		Labels: []string{"", strings.Repeat("x", 51), "bug", "bug"}, ExpectedVersion: updated.Version,
	})
	if err != nil || draft.Title != "Explicit title" || len(draft.Labels) != 1 {
		t.Fatalf("draft=%#v state=%#v err=%v", draft, withDraft, err)
	}
}

func TestRepositoryReviewStateValidationBoundaries(t *testing.T) {
	valid := repositoryReviewCoverageState("owner/repo")
	invalidStates := []RepositoryState{
		func() RepositoryState { value := valid; value.ActiveForceCampaignID = "campaign"; return value }(),
		func() RepositoryState {
			value := valid
			value.ActiveForceCampaignID = strings.Repeat("x", 257)
			value.ActiveForceProfileHash = "profile"
			value.ActiveForceCommitSHA = "commit"
			return value
		}(),
		func() RepositoryState {
			value := valid
			value.ReviewAttempts = map[string]int{"path": -1}
			return value
		}(),
		func() RepositoryState {
			value := valid
			value.ReviewAttemptIdentities = map[string]string{"path": "identity"}
			return value
		}(),
		func() RepositoryState {
			value := valid
			value.Unsupported = map[string]UnsupportedFile{
				"path": {FileRef: FileRef{Path: "other", BlobSHA: strings.Repeat("a", 40)}, Reason: "reason"},
			}
			return value
		}(),
		func() RepositoryState {
			value := valid
			value.Findings = []Finding{{Observations: make([]FindingObservation, 65)}}
			return value
		}(),
	}
	for index, state := range invalidStates {
		if err := validateState(state); err == nil {
			t.Fatalf("invalid state %d was accepted", index)
		}
	}

	if validHexDigest("xyz") || validHexDigest("") || !validHexDigest("0123abcdef") {
		t.Fatal("hex digest validation mismatch")
	}
	if validBlobSHA(strings.Repeat("g", 40)) || validBlobSHA("abc") || !validBlobSHA(strings.Repeat("a", 64)) {
		t.Fatal("blob digest validation mismatch")
	}
	if repositoryReviewStateFilename("repo_x.summary.json") || repositoryReviewStateFilename("other.json") ||
		!repositoryReviewStateFilename("repo_x.json") {
		t.Fatal("state filename validation mismatch")
	}
}

func TestRepositoryReviewCandidateAndSimilarityBoundaries(t *testing.T) {
	line := 2
	valid := FindingCandidate{
		Severity: "high", Title: "title", File: "service.go", Message: "message",
		Symbol: "Save", Evidence: "evidence", Impact: "impact",
		Validation: Validation{Status: "confirmed", Summary: "summary", Checks: []string{"check"}},
		Line:       &line,
	}
	invalid := []FindingCandidate{
		func() FindingCandidate { value := valid; value.Severity = "unknown"; return value }(),
		func() FindingCandidate { value := valid; value.Title = ""; return value }(),
		func() FindingCandidate { value := valid; value.Title = string([]byte{0xff}); return value }(),
		func() FindingCandidate { value := valid; value.Message = string([]byte{0xff}); return value }(),
		func() FindingCandidate { value := valid; value.Symbol = strings.Repeat("x", 4097); return value }(),
		func() FindingCandidate { value := valid; value.Validation.Checks = make([]string, 129); return value }(),
		func() FindingCandidate { value := valid; value.Validation.Checks = []string{""}; return value }(),
		func() FindingCandidate { value := valid; zero := 0; value.Line = &zero; return value }(),
	}
	for index, candidate := range invalid {
		if err := validateCandidate(candidate); err == nil {
			t.Fatalf("invalid candidate %d was accepted", index)
		}
	}
	if normalized := normalizeCandidate(FindingCandidate{
		Severity: " HIGH ", Title: " title ", File: `dir\\file.go`,
		Validation: Validation{Status: " CONFIRMED ", Summary: " yes "},
	}); normalized.Severity != "high" || normalized.Title != "title" || normalized.Validation.Status != "confirmed" {
		t.Fatalf("normalized candidate = %#v", normalized)
	}

	left, right, far := 10, 5, 30
	if !nearbyLines(nil, nil) || nearbyLines(&left, nil) || !nearbyLines(&left, &right) || nearbyLines(&left, &far) {
		t.Fatal("nearby line comparison mismatch")
	}
	if tokenDice(nil, findingTokens("useful token")) != 0 ||
		tokenDice(findingTokens("alpha beta"), findingTokens("alpha gamma")) <= 0 {
		t.Fatal("token similarity mismatch")
	}
	if got := findingTokens("the workers running services"); len(got) != 3 {
		t.Fatalf("finding tokens = %#v", got)
	}
	if moreSevere("high", "low") != "high" || moreSevere("low", "critical") != "critical" {
		t.Fatal("severity merge mismatch")
	}
	if got := appendUnique([]string{"one"}, " one "); len(got) != 1 {
		t.Fatalf("duplicate append = %#v", got)
	}
	if got := appendUnique([]string{"one"}, ""); len(got) != 1 {
		t.Fatalf("empty append = %#v", got)
	}
}

func TestRepositoryReviewAutomationNormalizationBoundaries(t *testing.T) {
	if err := normalizeAutomation(nil); !errors.Is(err, ErrInvalidAutomation) {
		t.Fatalf("nil automation error = %v", err)
	}
	base := validAutomationForTest("rra_coverage", "Coverage")
	base.SchemaVersion = RepositoryReviewAutomationSchemaVersion
	base.Version = 1
	base.CreatedAt = automationTestNow
	base.UpdatedAt = automationTestNow

	tests := []func(*RepositoryReviewAutomation){
		func(value *RepositoryReviewAutomation) { value.Repository = "https://user@example.com/repo" },
		func(value *RepositoryReviewAutomation) { value.Repository = "https://example.com/repo?q=1" },
		func(value *RepositoryReviewAutomation) {
			value.Status = RepositoryReviewAutomationStopping
			value.ActiveRunID = "run"
			value.RunIDs = []string{"run"}
		},
		func(value *RepositoryReviewAutomation) {
			value.Status = RepositoryReviewAutomationRunning
			value.ActiveRunID = "missing"
		},
		func(value *RepositoryReviewAutomation) {
			value.Status = RepositoryReviewAutomationPaused
			value.PauseReason = RepositoryReviewPauseManual
			value.RequestedPauseReason = RepositoryReviewPauseManual
		},
		func(value *RepositoryReviewAutomation) {
			value.Status = RepositoryReviewAutomationFailed
			value.PauseReason = RepositoryReviewPauseRunFailed
			value.ActiveRunID = "run"
			value.RunIDs = []string{"run"}
		},
	}
	for index, mutate := range tests {
		value := cloneAutomation(base)
		mutate(&value)
		if err := normalizeAutomation(&value); err == nil {
			t.Fatalf("invalid automation %d was accepted: %#v", index, value)
		}
	}

	tooManyPrices := cloneAutomation(base)
	tooManyPrices.ModelPrices = make(map[string]RepositoryReviewModelPrice, maxAutomationReviewers+1)
	for index := 0; index <= maxAutomationReviewers; index++ {
		tooManyPrices.ModelPrices["alias-"+automationTestIndex(index)] = RepositoryReviewModelPrice{}
	}
	if err := normalizeModelPrices(&tooManyPrices); err == nil {
		t.Fatal("too many model prices were accepted")
	}
	duplicatePrice := cloneAutomation(base)
	duplicatePrice.ModelPrices = map[string]RepositoryReviewModelPrice{
		"review-a":  {InputPricePer1M: 1},
		" review-a": {InputPricePer1M: 1},
	}
	for index := 0; index < 100; index++ {
		candidate := cloneAutomation(duplicatePrice)
		if err := normalizeModelPrices(&candidate); err == nil {
			t.Fatal("duplicate normalized model price was accepted")
		}
	}

	tooManyStats := cloneAutomation(base)
	tooManyStats.ModelStats = make(map[string]RepositoryReviewModelStats, maxAutomationReviewers+1)
	for index := 0; index <= maxAutomationReviewers; index++ {
		tooManyStats.ModelStats["alias-"+automationTestIndex(index)] = RepositoryReviewModelStats{}
	}
	if err := normalizeModelStats(&tooManyStats); err == nil {
		t.Fatal("too many model statistics were accepted")
	}
	for _, stats := range []RepositoryReviewModelStats{
		{Tokens: RepositoryReviewTokenUsage{PromptTokens: -1}},
		{Requests: 1, Failures: 2},
	} {
		value := cloneAutomation(base)
		value.ModelStats = map[string]RepositoryReviewModelStats{"review-a": stats}
		if err := normalizeModelStats(&value); err == nil {
			t.Fatalf("invalid model statistics %#v were accepted", stats)
		}
	}
	duplicateStats := cloneAutomation(base)
	duplicateStats.ModelStats = map[string]RepositoryReviewModelStats{
		"review-a":  {},
		" review-a": {},
	}
	if err := normalizeModelStats(&duplicateStats); err == nil {
		t.Fatal("duplicate normalized model statistics were accepted")
	}

	for _, sketches := range []map[string]string{
		{"unknown": base64.RawStdEncoding.EncodeToString(make([]byte, automationModelCoverageSketchBytes))},
		{"review-a": "not-base64"},
	} {
		value := cloneAutomation(base)
		value.ModelCoverageSketches = sketches
		if err := normalizeModelCoverageSketches(&value); err == nil {
			t.Fatalf("invalid coverage sketches %#v were accepted", sketches)
		}
	}
	validSketch := cloneAutomation(base)
	validSketch.ModelCoverageSketches = map[string]string{
		"review-a": base64.RawStdEncoding.EncodeToString(make([]byte, automationModelCoverageSketchBytes)),
	}
	if err := normalizeModelCoverageSketches(&validSketch); err != nil {
		t.Fatal(err)
	}

	remaining := 10.0
	for _, snapshots := range [][]RepositoryReviewAccountLimitSnapshot{
		{{AccountID: "account-a", Window: "daily"}},
		{{AccountID: "account-a", Window: "daily", RemainingPercent: func() *float64 { value := 101.0; return &value }(), CheckedAt: automationTestNow}},
		{{AccountID: "account-a", Window: "daily", RemainingPercent: &remaining, CheckedAt: automationTestNow}, {AccountID: "account-a", Window: "daily", CheckedAt: automationTestNow}},
	} {
		value := cloneAutomation(base)
		value.AccountLimitSnapshots = snapshots
		if err := normalizeAccountSnapshots(&value); err == nil {
			t.Fatalf("invalid snapshots %#v were accepted", snapshots)
		}
	}

	if _, err := normalizeUniqueAutomationStrings([]string{"a", "b"}, 1, 10, "value"); err == nil {
		t.Fatal("overlong string set was accepted")
	}
	if _, err := normalizeUniqueAutomationStrings([]string{""}, 1, 10, "value"); err == nil {
		t.Fatal("empty normalized string was accepted")
	}
	if _, err := normalizeUniqueAutomationStrings([]string{"a", " a "}, 2, 10, "value"); err == nil {
		t.Fatal("duplicate normalized string was accepted")
	}
	if validAutomationPauseReason("unknown") || containsAutomationString([]string{"one"}, "two") {
		t.Fatal("automation enum/string boundary mismatch")
	}
	if finiteNonnegative(math.NaN(), 1) || finiteNonnegative(math.Inf(1), 1) {
		t.Fatal("non-finite value was accepted")
	}
}

func TestRepositoryReviewStoreLoadAndListBoundaries(t *testing.T) {
	t.Run("empty", func(t *testing.T) {
		store := NewStore(t.TempDir())
		if states, err := store.List(); err != nil || len(states) != 0 {
			t.Fatalf("empty list=%#v err=%v", states, err)
		}
		if summaries, err := store.ListSummaries(); err != nil || len(summaries) != 0 {
			t.Fatalf("empty summaries=%#v err=%v", summaries, err)
		}
	})

	store, first := repositoryReviewCoverageStore(t, "owner/first")
	store.now = func() time.Time { return repositoryAuditTestNow.Add(time.Minute) }
	second := repositoryReviewCoverageState("owner/second")
	second.UpdatedAt = repositoryAuditTestNow.Add(time.Minute)
	if err := store.save(&second); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(store.root, "ignored-dir"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(store.root, "ignored.txt"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	states, err := store.List()
	if err != nil || len(states) != 2 || states[0].Repository != second.Repository ||
		states[1].Repository != first.Repository {
		t.Fatalf("states=%#v err=%v", states, err)
	}

	loaded, err := store.load(first.Repository)
	if err != nil || loaded.Repository != first.Repository {
		t.Fatalf("loaded=%#v err=%v", loaded, err)
	}
	legacy := first
	legacy.Files = nil
	legacy.Unsupported = nil
	legacy.ReviewAttempts = nil
	legacy.ReviewAttemptIdentities = nil
	legacy.Findings = nil
	legacy.Contexts = nil
	legacy.Runs = nil
	legacy.IssueDrafts = nil
	data, err := json.Marshal(legacy)
	if err != nil {
		t.Fatal(err)
	}
	if writeErr := os.WriteFile(store.path(first.Repository), data, 0o600); writeErr != nil {
		t.Fatal(writeErr)
	}
	loaded, err = store.load(first.Repository)
	if err != nil || loaded.ID != RepositoryID(first.Repository) || loaded.Files == nil || loaded.IssueDrafts == nil {
		t.Fatalf("legacy load=%#v err=%v", loaded, err)
	}

	t.Run("unsafe list root", func(t *testing.T) {
		unsafe := NewStore(t.TempDir())
		if err := os.WriteFile(unsafe.root, nil, 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := unsafe.List(); err == nil {
			t.Fatal("unsafe root was listed")
		}
	})
	t.Run("unreadable list root", func(t *testing.T) {
		unreadable := NewStore(t.TempDir())
		if err := os.MkdirAll(unreadable.root, 0o700); err != nil {
			t.Fatal(err)
		}
		repositoryReviewDenyPermissions(t, unreadable.root, 0o700)
		if _, err := unreadable.List(); err == nil {
			t.Skip("filesystem user can bypass directory permissions")
		}
	})
	t.Run("unreadable list state", func(t *testing.T) {
		unreadable, state := repositoryReviewCoverageStore(t, "owner/unreadable-list")
		path := unreadable.path(state.Repository)
		repositoryReviewDenyPermissions(t, path, 0o600)
		if _, err := unreadable.List(); err == nil {
			t.Skip("filesystem user can bypass file permissions")
		}
	})
	t.Run("inaccessible load path", func(t *testing.T) {
		unreadable, state := repositoryReviewCoverageStore(t, "owner/inaccessible-load")
		repositoryReviewDenyPermissions(t, unreadable.root, 0o700)
		if _, err := unreadable.load(state.Repository); err == nil {
			t.Skip("filesystem user can bypass directory permissions")
		}
	})
	t.Run("unreadable load state", func(t *testing.T) {
		unreadable, state := repositoryReviewCoverageStore(t, "owner/unreadable-load")
		path := unreadable.path(state.Repository)
		repositoryReviewDenyPermissions(t, path, 0o600)
		if _, err := unreadable.load(state.Repository); err == nil {
			t.Skip("filesystem user can bypass file permissions")
		}
	})
}

func TestRepositoryReviewStorePlanningAndRecordValidationBoundaries(t *testing.T) {
	store := newRepositoryAuditTestStore(t)
	file := repositoryAuditTestFile("service.go", "a", 10)
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := store.Plan(
		canceled,
		"owner/repo",
		"commit",
		"inventory",
		[]FileRef{file},
		false,
	); !errors.Is(
		err,
		context.Canceled,
	) {
		t.Fatalf("canceled plan error = %v", err)
	}
	if _, err := store.Record(canceled, RecordRequest{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled record error = %v", err)
	}

	for _, call := range []func() error{
		func() error {
			_, err := store.Plan(context.Background(), "", "commit", "inventory", []FileRef{file}, false)
			return err
		},
		func() error {
			_, err := store.PlanWithProfileLimit(context.Background(), "owner/repo", "commit", "inventory", "profile", []FileRef{file}, false, 0)
			return err
		},
		func() error {
			_, err := store.Plan(context.Background(), "owner/repo", "commit", "inventory", []FileRef{file, file}, false)
			return err
		},
	} {
		if err := call(); !errors.Is(err, ErrInvalidPlan) {
			t.Fatalf("invalid plan error = %v", err)
		}
	}

	plan, err := store.Plan(context.Background(), "owner/repo", "commit", "inventory", []FileRef{file}, false)
	if err != nil {
		t.Fatal(err)
	}
	badPlan := plan
	badPlan.ID = "bad"
	for _, request := range []RecordRequest{
		{},
		{Plan: badPlan, RunID: "run"},
		{Plan: func() Plan {
			value := plan
			value.ForceCampaignID = strings.Repeat("x", 257)
			value.ID = planDigest(value)
			return value
		}(), RunID: "run"},
		{Plan: plan, RunID: strings.Repeat("x", 1025)},
		{Plan: plan, RunID: "run", ExcludedFiles: -1},
	} {
		if _, err := store.Record(context.Background(), request); !errors.Is(err, ErrInvalidPlan) {
			t.Fatalf("invalid record %#v error = %v", request, err)
		}
	}

	duplicatePlan := plan
	duplicatePlan.DeferredFiles = []FileRef{file}
	duplicatePlan.ID = planDigest(duplicatePlan)
	if _, err := store.Record(
		context.Background(),
		RecordRequest{Plan: duplicatePlan, RunID: "duplicate"},
	); !errors.Is(
		err,
		ErrInvalidPlan,
	) {
		t.Fatalf("duplicate pending/deferred error = %v", err)
	}

	tests := []struct {
		name    string
		request RecordRequest
	}{
		{
			name: "unsupported outside plan",
			request: RecordRequest{Plan: plan, RunID: "unsupported", UnsupportedFiles: []UnsupportedFile{{
				FileRef: repositoryAuditTestFile("other.go", "b", 10), Reason: "unsupported",
			}}},
		},
		{
			name: "observation without model",
			request: RecordRequest{
				Plan:         plan,
				RunID:        "model",
				Observations: []Observation{{ScopeFiles: []FileRef{file}}},
			},
		},
		{
			name:    "empty observation scope",
			request: RecordRequest{Plan: plan, RunID: "scope", Observations: []Observation{{Model: "review-a"}}},
		},
		{
			name: "finding outside scope",
			request: RecordRequest{Plan: plan, RunID: "finding", Observations: []Observation{{
				Model: "review-a", ScopeFiles: []FileRef{file}, Findings: []FindingCandidate{{
					Severity: "high", Title: "bug", File: "other.go", Evidence: "e", Impact: "i",
					Validation: Validation{Status: "confirmed", Summary: "v"},
				}},
			}}},
		},
		{
			name: "completed outside plan",
			request: RecordRequest{
				Plan:           plan,
				RunID:          "completed",
				CompletedFiles: []FileRef{repositoryAuditTestFile("other.go", "b", 10)},
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := store.Record(context.Background(), test.request); err == nil {
				t.Fatalf("invalid record request %#v was accepted", test.request)
			}
		})
	}
}

func TestRepositoryReviewFinalizeAndHelperBoundaries(t *testing.T) {
	store := newRepositoryAuditTestStore(t)
	if _, err := store.FinalizeNoopPlan(Plan{}); !errors.Is(err, ErrInvalidPlan) {
		t.Fatalf("invalid no-op plan error = %v", err)
	}
	plan, err := store.PlanWithProfileLimitAuthoritative(
		context.Background(), "owner/repo", "commit", "inventory", "profile", nil, false, 10, true,
	)
	if err != nil {
		t.Fatal(err)
	}
	stale := plan
	stale.StateVersion++
	stale.ID = planDigest(stale)
	if _, staleFinalizeErr := store.FinalizeNoopPlan(stale); !errors.Is(staleFinalizeErr, ErrConflict) {
		t.Fatalf("stale no-op plan error = %v", staleFinalizeErr)
	}
	if _, invalidExcludedErr := store.FinalizeNoopPlan(plan, -1); !errors.Is(invalidExcludedErr, ErrInvalidPlan) {
		t.Fatalf("invalid excluded count error = %v", invalidExcludedErr)
	}
	finalized, err := store.FinalizeNoopPlan(plan, 2)
	if err != nil || finalized.LastCommitSHA != "commit" || finalized.LastExcludedFiles != 2 {
		t.Fatalf("finalized state=%#v err=%v", finalized, err)
	}
	next, err := store.PlanWithProfileLimitAuthoritative(
		context.Background(), "owner/repo", "commit", "inventory-2", "profile", nil, false, 10, true,
	)
	if err != nil {
		t.Fatal(err)
	}
	unchanged, err := store.FinalizeNoopPlan(next, 2)
	if err != nil || unchanged.Version != finalized.Version {
		t.Fatalf("unchanged no-op state=%#v err=%v", unchanged, err)
	}

	state := repositoryReviewCoverageState("owner/repo")
	if pruneCheckpointMetadata(nil, plan, nil) || pruneCheckpointMetadata(&state, Plan{}, nil) {
		t.Fatal("invalid checkpoint pruning changed state")
	}
	state.Files["gone.go"] = ReviewedFile{FileRef: repositoryAuditTestFile("gone.go", "a", 1)}
	state.Unsupported["unsupported.go"] = UnsupportedFile{
		FileRef: repositoryAuditTestFile("unsupported.go", "b", 1),
		Reason:  "reason",
	}
	state.ReviewAttempts["attempt.go"] = 1
	state.ReviewAttemptIdentities["attempt.go"] = "identity"
	keptUnsupported := repositoryAuditTestFile("keep.go", "c", 1)
	state.Unsupported[keptUnsupported.Path] = UnsupportedFile{FileRef: keptUnsupported, Reason: "reason"}
	if !pruneCheckpointMetadata(&state, Plan{
		Authoritative:    true,
		UnsupportedFiles: []UnsupportedFile{{FileRef: keptUnsupported, Reason: "reason"}},
	}, nil) || len(state.Files) != 0 ||
		len(state.Unsupported) != 1 || state.Unsupported[keptUnsupported.Path].Path == "" ||
		len(state.ReviewAttempts) != 0 {
		t.Fatalf("pruned state = %#v", state)
	}

	trusted := repositoryAuditTestFile("service.go", "a", 10)
	if _, err := bindScopeFiles(nil, map[string]FileRef{trusted.Path: trusted}); err == nil {
		t.Fatal("empty scope was accepted")
	}
	changed := trusted
	changed.SizeBytes++
	if _, err := bindScopeFiles([]FileRef{changed}, map[string]FileRef{trusted.Path: trusted}); err == nil {
		t.Fatal("changed scope file was accepted")
	}
	if _, found := fileInScope("missing.go", []FileRef{trusted}); found {
		t.Fatal("missing file was found in scope")
	}
	state.Contexts = []FindingContext{{ID: "keep"}, {ID: "drop"}}
	state.Findings = []Finding{{ContextIDs: []string{"keep"}}}
	pruneUnreferencedFindingContexts(&state)
	if len(state.Contexts) != 1 || state.Contexts[0].ID != "keep" {
		t.Fatalf("contexts after prune = %#v", state.Contexts)
	}
	pruneUnreferencedFindingContexts(nil)
	if truncateUTF8Bytes("value", 0) != "value" || truncateUTF8Bytes("value", 10) != "value" {
		t.Fatal("short truncation changed value")
	}
	line := 12
	body := defaultIssueBody(RepositoryState{Repository: "owner/repo"}, []Finding{{
		ID: "finding", Severity: "high", Title: "bug", File: trusted, Line: &line,
		Evidence: "e", Impact: "i", Validation: Validation{Summary: "v"},
	}})
	if !strings.Contains(body, ":12") {
		t.Fatalf("issue body = %q", body)
	}
	labels := make([]string, 25)
	for index := range labels {
		labels[index] = "label-" + automationTestIndex(index)
	}
	if got := normalizeLabels(labels); len(got) != 20 {
		t.Fatalf("bounded labels = %d", len(got))
	}
}

func TestRepositoryReviewAutomationStoreErrorBoundaries(t *testing.T) {
	store := newAutomationTestStore(t)
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := store.ListAutomations(canceled); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled list error = %v", err)
	}
	if _, _, err := store.GetAutomation(canceled, "rra_test"); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled get error = %v", err)
	}
	if _, err := store.CreateAutomation(
		canceled,
		validAutomationForTest("rra_test", "test"),
	); !errors.Is(
		err,
		context.Canceled,
	) {
		t.Fatalf("canceled create error = %v", err)
	}
	if _, err := store.UpdateAutomation(
		canceled,
		"rra_test",
		1,
		func(*RepositoryReviewAutomation) error { return nil },
	); !errors.Is(
		err,
		context.Canceled,
	) {
		t.Fatalf("canceled update error = %v", err)
	}
	if err := store.DeleteAutomation(canceled, "rra_test", 1); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled delete error = %v", err)
	}

	if listed, err := store.ListAutomations(context.Background()); err != nil || len(listed) != 0 {
		t.Fatalf("empty automation list=%#v err=%v", listed, err)
	}
	if _, _, err := store.GetAutomation(context.Background(), "invalid"); !errors.Is(err, ErrInvalidAutomation) {
		t.Fatalf("invalid automation ID error = %v", err)
	}
	if _, err := store.UpdateAutomation(
		context.Background(),
		"rra_missing",
		1,
		nil,
	); !errors.Is(
		err,
		ErrInvalidAutomation,
	) {
		t.Fatalf("nil automation mutation error = %v", err)
	}
	if _, err := store.UpdateAutomation(
		context.Background(),
		"rra_missing",
		1,
		func(*RepositoryReviewAutomation) error { return nil },
	); !errors.Is(
		err,
		os.ErrNotExist,
	) {
		t.Fatalf("missing automation update error = %v", err)
	}
	if err := store.DeleteAutomation(context.Background(), "invalid", 1); !errors.Is(err, ErrInvalidAutomation) {
		t.Fatalf("invalid automation delete error = %v", err)
	}

	created := createAutomationForTest(t, store, "rra_duplicate", "Duplicate")
	if _, err := store.CreateAutomation(
		context.Background(),
		validAutomationForTest(created.ID, "Duplicate"),
	); !errors.Is(
		err,
		ErrConflict,
	) {
		t.Fatalf("duplicate automation create error = %v", err)
	}
	if _, err := store.UpdateAutomation(
		context.Background(),
		created.ID,
		created.Version,
		func(value *RepositoryReviewAutomation) error {
			value.Version++
			return nil
		},
	); !errors.Is(
		err,
		ErrInvalidAutomation,
	) {
		t.Fatalf("immutable version update error = %v", err)
	}
	if _, err := store.UpdateAutomation(
		context.Background(),
		created.ID,
		created.Version,
		func(value *RepositoryReviewAutomation) error {
			value.Name = ""
			return nil
		},
	); !errors.Is(err, ErrInvalidAutomation) {
		t.Fatalf("invalid candidate update error = %v", err)
	}
	if err := store.saveAutomation(RepositoryReviewAutomation{}); !errors.Is(err, ErrInvalidAutomation) {
		t.Fatalf("invalid direct save error = %v", err)
	}
	if _, _, err := store.loadAutomation("invalid"); !errors.Is(err, ErrInvalidAutomation) {
		t.Fatalf("invalid direct load error = %v", err)
	}
}

func TestRepositoryReviewCorruptStorageBoundaries(t *testing.T) {
	t.Run("automation unreadable catalog", func(t *testing.T) {
		store := newAutomationTestStore(t)
		if err := os.MkdirAll(store.root, 0o700); err != nil {
			t.Fatal(err)
		}
		repositoryReviewDenyPermissions(t, store.root, 0o700)
		if _, err := store.ListAutomations(context.Background()); err == nil {
			t.Skip("filesystem user can bypass directory permissions")
		}
	})
	t.Run("automation inaccessible entry", func(t *testing.T) {
		store := newAutomationTestStore(t)
		if err := os.MkdirAll(store.root, 0o700); err != nil {
			t.Fatal(err)
		}
		repositoryReviewDenyPermissions(t, store.root, 0o700)
		if _, _, err := store.loadAutomation("rra_hidden"); err == nil {
			t.Skip("filesystem user can bypass directory permissions")
		}
	})
	t.Run("automation unreadable file", func(t *testing.T) {
		store := newAutomationTestStore(t)
		created := createAutomationForTest(t, store, "rra_unreadable", "Unreadable")
		path := store.automationPath(created.ID)
		repositoryReviewDenyPermissions(t, path, 0o600)
		if _, _, err := store.loadAutomation(created.ID); err == nil {
			t.Skip("filesystem user can bypass file permissions")
		}
	})
	t.Run("automation semantically invalid", func(t *testing.T) {
		store := newAutomationTestStore(t)
		if err := os.MkdirAll(store.root, 0o700); err != nil {
			t.Fatal(err)
		}
		fixture := validAutomationForTest("rra_invalid_state", "Invalid")
		fixture.SchemaVersion = RepositoryReviewAutomationSchemaVersion + 1
		fixture.Version = 1
		fixture.Status = RepositoryReviewAutomationIdle
		fixture.CreatedAt = automationTestNow
		fixture.UpdatedAt = automationTestNow
		data, err := json.Marshal(fixture)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(store.automationPath(fixture.ID), data, 0o600); err != nil {
			t.Fatal(err)
		}
		if _, _, err := store.loadAutomation(fixture.ID); !errors.Is(err, ErrInvalidAutomation) {
			t.Fatalf("invalid automation error = %v", err)
		}
	})
	t.Run("automation invalid filename", func(t *testing.T) {
		store := newAutomationTestStore(t)
		if err := os.MkdirAll(store.root, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(store.root, "automation_bad!.json"), []byte(`{}`), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := store.ListAutomations(context.Background()); !errors.Is(err, ErrInvalidAutomation) {
			t.Fatalf("invalid filename list error = %v", err)
		}
	})
	t.Run("automation malformed", func(t *testing.T) {
		store := newAutomationTestStore(t)
		if err := os.MkdirAll(store.root, 0o700); err != nil {
			t.Fatal(err)
		}
		id := "rra_malformed"
		if err := os.WriteFile(store.automationPath(id), []byte(`{`), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, _, err := store.GetAutomation(context.Background(), id); err == nil {
			t.Fatal("malformed automation was loaded")
		}
	})
	t.Run("automation identity", func(t *testing.T) {
		store := newAutomationTestStore(t)
		created := createAutomationForTest(t, store, "rra_identity", "Identity")
		created.ID = "rra_other"
		data, err := json.Marshal(created)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(store.automationPath("rra_identity"), data, 0o600); err != nil {
			t.Fatal(err)
		}
		if _, _, err := store.GetAutomation(context.Background(), "rra_identity"); err == nil {
			t.Fatal("automation identity mismatch was loaded")
		}
	})
	t.Run("automation oversized", func(t *testing.T) {
		store := newAutomationTestStore(t)
		if err := os.MkdirAll(store.root, 0o700); err != nil {
			t.Fatal(err)
		}
		path := store.automationPath("rra_large")
		file, err := os.Create(path)
		if err != nil {
			t.Fatal(err)
		}
		if err := file.Truncate(maxAutomationFileBytes + 1); err != nil {
			t.Fatal(err)
		}
		if err := file.Close(); err != nil {
			t.Fatal(err)
		}
		if _, _, err := store.GetAutomation(context.Background(), "rra_large"); err == nil {
			t.Fatal("oversized automation was loaded")
		}
	})
	t.Run("state malformed", func(t *testing.T) {
		store, state := repositoryReviewCoverageStore(t, "owner/repo")
		if err := os.WriteFile(store.path(state.Repository), []byte(`{`), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, _, err := store.Get(state.Repository); err == nil {
			t.Fatal("malformed state was loaded")
		}
		if _, err := store.List(); err == nil {
			t.Fatal("malformed state was listed")
		}
	})
	t.Run("state semantically invalid", func(t *testing.T) {
		store, state := repositoryReviewCoverageStore(t, "owner/repo")
		state.SchemaVersion++
		data, err := json.Marshal(state)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(store.path(state.Repository), data, 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := store.load(state.Repository); err == nil {
			t.Fatal("semantically invalid state was loaded")
		}
	})
	t.Run("state identity", func(t *testing.T) {
		store, state := repositoryReviewCoverageStore(t, "owner/repo")
		state.Repository = "owner/other"
		state.ID = RepositoryID(state.Repository)
		data, err := json.Marshal(state)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(store.path("owner/repo"), data, 0o600); err != nil {
			t.Fatal(err)
		}
		if _, _, err := store.Get("owner/repo"); err == nil {
			t.Fatal("state identity mismatch was loaded")
		}
	})
	t.Run("state oversized", func(t *testing.T) {
		store := NewStore(t.TempDir())
		if err := os.MkdirAll(store.root, 0o700); err != nil {
			t.Fatal(err)
		}
		path := store.path("owner/repo")
		file, err := os.Create(path)
		if err != nil {
			t.Fatal(err)
		}
		if err := file.Truncate(maxStateFileBytes + 1); err != nil {
			t.Fatal(err)
		}
		if err := file.Close(); err != nil {
			t.Fatal(err)
		}
		if _, _, err := store.Get("owner/repo"); err == nil {
			t.Fatal("oversized state was loaded")
		}
		if _, err := store.List(); err == nil {
			t.Fatal("oversized state was listed")
		}
	})
}

func TestRepositoryReviewGetByIDBoundaries(t *testing.T) {
	store := NewStore(t.TempDir())
	if _, found, err := store.GetByID("invalid"); err != nil || found {
		t.Fatalf("invalid ID found=%v err=%v", found, err)
	}
	missingID := RepositoryID("owner/missing")
	if _, found, err := store.GetByID(missingID); err != nil || found {
		t.Fatalf("missing ID found=%v err=%v", found, err)
	}

	t.Run("unsafe root", func(t *testing.T) {
		workspace := t.TempDir()
		unsafeStore := NewStore(workspace)
		if err := os.WriteFile(unsafeStore.root, []byte("file"), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, _, err := unsafeStore.GetByID(missingID); err == nil {
			t.Fatal("unsafe store root was accepted")
		}
	})
	t.Run("inaccessible state path", func(t *testing.T) {
		store, state := repositoryReviewCoverageStore(t, "owner/repo")
		repositoryReviewDenyPermissions(t, store.root, 0o700)
		if _, _, err := store.GetByID(state.ID); err == nil {
			t.Skip("filesystem user can bypass directory permissions")
		}
	})
	t.Run("unreadable state", func(t *testing.T) {
		store, state := repositoryReviewCoverageStore(t, "owner/repo")
		path := store.path(state.Repository)
		repositoryReviewDenyPermissions(t, path, 0o600)
		if _, _, err := store.GetByID(state.ID); err == nil {
			t.Skip("filesystem user can bypass file permissions")
		}
	})
	t.Run("irregular state", func(t *testing.T) {
		irregular := NewStore(t.TempDir())
		if err := os.MkdirAll(irregular.root, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.Mkdir(irregular.path("owner/repo"), 0o700); err != nil {
			t.Fatal(err)
		}
		if _, _, err := irregular.GetByID(RepositoryID("owner/repo")); err == nil {
			t.Fatal("directory state was accepted")
		}
	})
	t.Run("malformed summary", func(t *testing.T) {
		malformed := NewStore(t.TempDir())
		if err := os.MkdirAll(malformed.root, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(malformed.path("owner/repo"), []byte(`{`), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, _, err := malformed.GetByID(RepositoryID("owner/repo")); err == nil {
			t.Fatal("malformed direct state summary was accepted")
		}
	})
	t.Run("mismatched summary", func(t *testing.T) {
		mismatch := NewStore(t.TempDir())
		if err := os.MkdirAll(mismatch.root, 0o700); err != nil {
			t.Fatal(err)
		}
		requested := RepositoryID("owner/repo")
		data, err := json.Marshal(RepositorySummary{
			SchemaVersion: SchemaVersion, ID: RepositoryID("owner/other"), Repository: "owner/other",
		})
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(mismatch.path("owner/repo"), data, 0o600); err != nil {
			t.Fatal(err)
		}
		if _, _, err := mismatch.GetByID(requested); err == nil {
			t.Fatal("mismatched direct summary was accepted")
		}
	})
}

func TestRepositoryReviewListSummariesBoundaries(t *testing.T) {
	store, first := repositoryReviewCoverageStore(t, "owner/first")
	second := repositoryReviewCoverageState("owner/second")
	second.UpdatedAt = first.UpdatedAt.Add(time.Minute)
	if err := store.save(&second); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(store.root, "ignored"), 0o700); err != nil {
		t.Fatal(err)
	}
	summaries, err := store.ListSummaries()
	if err != nil || len(summaries) != 2 || summaries[0].Repository != second.Repository {
		t.Fatalf("summaries=%#v err=%v", summaries, err)
	}
	if _, err := store.listSummaries(1); err == nil {
		t.Fatal("bounded summary catalog accepted two repositories")
	}

	t.Run("unsafe root", func(t *testing.T) {
		unsafe := NewStore(t.TempDir())
		if err := os.WriteFile(unsafe.root, nil, 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := unsafe.ListSummaries(); err == nil {
			t.Fatal("unsafe summary root was listed")
		}
	})
	t.Run("unreadable catalog", func(t *testing.T) {
		unreadable := NewStore(t.TempDir())
		if err := os.MkdirAll(unreadable.root, 0o700); err != nil {
			t.Fatal(err)
		}
		repositoryReviewDenyPermissions(t, unreadable.root, 0o700)
		if _, err := unreadable.ListSummaries(); err == nil {
			t.Skip("filesystem user can bypass directory permissions")
		}
	})
	t.Run("unreadable summary", func(t *testing.T) {
		unreadable, state := repositoryReviewCoverageStore(t, "owner/repo")
		summaryPath := strings.TrimSuffix(unreadable.path(state.Repository), ".json") + ".summary.json"
		future := time.Now().Add(time.Hour)
		if err := os.Chtimes(summaryPath, future, future); err != nil {
			t.Fatal(err)
		}
		repositoryReviewDenyPermissions(t, summaryPath, 0o600)
		if _, err := unreadable.ListSummaries(); err == nil {
			t.Skip("filesystem user can bypass file permissions")
		}
	})

	t.Run("summary symlink", func(t *testing.T) {
		symlinkStore, state := repositoryReviewCoverageStore(t, "owner/repo")
		summaryPath := strings.TrimSuffix(symlinkStore.path(state.Repository), ".json") + ".summary.json"
		if err := os.Remove(summaryPath); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(symlinkStore.path(state.Repository), summaryPath); err != nil {
			t.Skipf("symlink unavailable: %v", err)
		}
		if _, err := symlinkStore.ListSummaries(); err == nil {
			t.Fatal("summary symlink was accepted")
		}
	})
	t.Run("malformed summary", func(t *testing.T) {
		malformed, state := repositoryReviewCoverageStore(t, "owner/repo")
		summaryPath := strings.TrimSuffix(malformed.path(state.Repository), ".json") + ".summary.json"
		if err := os.WriteFile(summaryPath, []byte(`{`), 0o600); err != nil {
			t.Fatal(err)
		}
		future := time.Now().Add(time.Hour)
		if err := os.Chtimes(summaryPath, future, future); err != nil {
			t.Fatal(err)
		}
		if _, err := malformed.ListSummaries(); err == nil {
			t.Fatal("malformed summary was accepted")
		}
	})
	t.Run("invalid summary identity", func(t *testing.T) {
		invalid, state := repositoryReviewCoverageStore(t, "owner/repo")
		summaryPath := strings.TrimSuffix(invalid.path(state.Repository), ".json") + ".summary.json"
		data, err := json.Marshal(
			RepositorySummary{SchemaVersion: SchemaVersion, ID: "bad", Repository: state.Repository},
		)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(summaryPath, data, 0o600); err != nil {
			t.Fatal(err)
		}
		future := time.Now().Add(time.Hour)
		if err := os.Chtimes(summaryPath, future, future); err != nil {
			t.Fatal(err)
		}
		if _, err := invalid.ListSummaries(); err == nil {
			t.Fatal("invalid summary identity was accepted")
		}
	})
	t.Run("state symlink", func(t *testing.T) {
		symlinkStore := NewStore(t.TempDir())
		if err := os.MkdirAll(symlinkStore.root, 0o700); err != nil {
			t.Fatal(err)
		}
		target := filepath.Join(t.TempDir(), "state")
		if err := os.WriteFile(target, []byte(`{}`), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(target, symlinkStore.path("owner/repo")); err != nil {
			t.Skipf("symlink unavailable: %v", err)
		}
		if _, err := symlinkStore.ListSummaries(); err == nil {
			t.Fatal("state symlink was accepted")
		}
	})
}

func TestRepositoryReviewListEntryMetadataFailures(t *testing.T) {
	sentinel := errors.New("injected directory metadata failure")
	entry := repositoryReviewCoverageDirEntry{name: "repo_failure.json", infoErr: sentinel}
	if _, err := repositoryReviewSummaryFromEntry(t.TempDir(), entry); !errors.Is(err, sentinel) {
		t.Fatalf("summary metadata error = %v", err)
	}
	if _, err := repositoryReviewStateFromEntry(t.TempDir(), entry); !errors.Is(err, sentinel) {
		t.Fatalf("state metadata error = %v", err)
	}

	fixturePath := filepath.Join(t.TempDir(), "fixture")
	if err := os.WriteFile(fixturePath, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(fixturePath)
	if err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(t.TempDir(), "inaccessible")
	if err := os.Mkdir(root, 0o700); err != nil {
		t.Fatal(err)
	}
	repositoryReviewDenyPermissions(t, root, 0o700)
	entry = repositoryReviewCoverageDirEntry{name: "repo_failure.json", info: info}
	if _, err := repositoryReviewSummaryFromEntry(root, entry); err == nil {
		t.Skip("filesystem user can bypass directory permissions")
	}
}

func TestRepositoryReviewSaveRejectsUnsafeTargets(t *testing.T) {
	store := NewStore(t.TempDir())
	if err := store.save(nil); err == nil {
		t.Fatal("nil state was saved")
	}
	invalid := RepositoryState{}
	if err := store.save(&invalid); err == nil {
		t.Fatal("invalid state was saved")
	}
	invalidTime := repositoryReviewCoverageState("owner/invalid-time")
	invalidTime.UpdatedAt = time.Date(10_000, time.January, 1, 0, 0, 0, 0, time.UTC)
	if err := NewStore(t.TempDir()).save(&invalidTime); err == nil {
		t.Fatal("state with an unencodable timestamp was saved")
	}
	oversized := repositoryReviewCoverageState("owner/oversized")
	oversized.Findings = []Finding{{Evidence: strings.Repeat("x", int(maxStateFileBytes)+1)}}
	if err := NewStore(t.TempDir()).save(&oversized); err == nil {
		t.Fatal("oversized state was saved")
	}

	t.Run("root file", func(t *testing.T) {
		unsafe := NewStore(t.TempDir())
		if err := os.WriteFile(unsafe.root, []byte("file"), 0o600); err != nil {
			t.Fatal(err)
		}
		state := repositoryReviewCoverageState("owner/repo")
		if err := unsafe.save(&state); err == nil {
			t.Fatal("state was saved through a root file")
		}
	})
	t.Run("uncreatable root", func(t *testing.T) {
		workspace := t.TempDir()
		if err := os.Chmod(workspace, 0o500); err != nil {
			t.Skipf("cannot restrict workspace: %v", err)
		}
		t.Cleanup(func() { _ = os.Chmod(workspace, 0o700) })
		state := repositoryReviewCoverageState("owner/repo")
		if err := NewStore(workspace).save(&state); err == nil {
			t.Skip("filesystem user can bypass directory permissions")
		}
	})
	t.Run("unwritable root", func(t *testing.T) {
		store := NewStore(t.TempDir())
		if err := os.MkdirAll(store.root, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(store.root, 0o500); err != nil {
			t.Skipf("cannot restrict store root: %v", err)
		}
		t.Cleanup(func() { _ = os.Chmod(store.root, 0o700) })
		state := repositoryReviewCoverageState("owner/repo")
		if err := store.save(&state); err == nil {
			t.Skip("filesystem user can bypass directory permissions")
		}
	})
	t.Run("state directory", func(t *testing.T) {
		unsafe := NewStore(t.TempDir())
		if err := os.MkdirAll(unsafe.path("owner/repo"), 0o700); err != nil {
			t.Fatal(err)
		}
		state := repositoryReviewCoverageState("owner/repo")
		if err := unsafe.save(&state); err == nil {
			t.Fatal("state replaced an existing directory")
		}
	})
	t.Run("inaccessible state target", func(t *testing.T) {
		unsafe := NewStore(t.TempDir())
		if err := os.MkdirAll(unsafe.root, 0o700); err != nil {
			t.Fatal(err)
		}
		repositoryReviewDenyPermissions(t, unsafe.root, 0o700)
		state := repositoryReviewCoverageState("owner/repo")
		if err := unsafe.save(&state); err == nil {
			t.Skip("filesystem user can bypass directory permissions")
		}
	})
	t.Run("nonempty summary directory", func(t *testing.T) {
		unsafe := NewStore(t.TempDir())
		if err := os.MkdirAll(unsafe.root, 0o700); err != nil {
			t.Fatal(err)
		}
		summaryPath := strings.TrimSuffix(unsafe.path("owner/repo"), ".json") + ".summary.json"
		if err := os.Mkdir(summaryPath, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(summaryPath, "child"), nil, 0o600); err != nil {
			t.Fatal(err)
		}
		state := repositoryReviewCoverageState("owner/repo")
		if err := unsafe.save(&state); err == nil {
			t.Fatal("state save ignored a non-removable summary path")
		}
	})
}

func TestRepositoryReviewEnsureSafeRootBoundaries(t *testing.T) {
	sentinel := errors.New("injected durable mkdir failure")
	store := NewStore(t.TempDir())
	if err := store.ensureSafeRoot(func(string, os.FileMode) error { return sentinel }); !errors.Is(err, sentinel) {
		t.Fatalf("durable mkdir error = %v", err)
	}

	store = NewStore(t.TempDir())
	target := t.TempDir()
	err := store.ensureSafeRoot(func(root string, _ os.FileMode) error {
		return os.Symlink(target, root)
	})
	if err == nil {
		t.Fatal("root replaced by a symlink after mkdir was accepted")
	}
}

func TestRepositoryReviewPublicMutationsRejectUnsafeStore(t *testing.T) {
	unsafe := NewStore(t.TempDir())
	target := filepath.Join(t.TempDir(), "lock")
	if err := os.WriteFile(target, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, unsafe.root+".lock"); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	state := repositoryReviewCoverageState("owner/repo")
	plan := Plan{
		Repository: state.Repository, CommitSHA: "commit", InventoryHash: "inventory",
		ProfileHash: "profile", CreatedAt: repositoryAuditTestNow,
	}
	plan.ID = planDigest(plan)
	calls := []func() error{
		func() error { _, _, err := unsafe.Get(state.Repository); return err },
		func() error {
			_, err := unsafe.Plan(context.Background(), state.Repository, "commit", "inventory", nil, false)
			return err
		},
		func() error {
			_, err := unsafe.Record(context.Background(), RecordRequest{Plan: plan, RunID: "run"})
			return err
		},
		func() error { _, err := unsafe.FinalizeNoopPlan(plan); return err },
		func() error {
			_, err := unsafe.SetFindingStatus(state.Repository, "finding", FindingOpen, 1)
			return err
		},
		func() error {
			_, _, err := unsafe.PrepareIssue(IssueDraftRequest{
				Repository: state.Repository,
				FindingIDs: []string{"finding"},
			})
			return err
		},
		func() error {
			_, _, err := unsafe.UpdateIssueDraft(state.Repository, "draft", "title", "body", nil, 1)
			return err
		},
		func() error {
			_, _, err := unsafe.SetIssueDraftPublication(state.Repository, "draft", 1, IssueDraftUnknown, "", "")
			return err
		},
		func() error {
			_, _, _, err := unsafe.ClaimIssueDraftPublication(state.Repository, "draft", 1)
			return err
		},
		func() error {
			_, _, err := unsafe.SetFindingStatusByVersion(
				state.Repository, "finding", FindingOpen, 1,
			)
			return err
		},
		func() error {
			request := testIssueGenerationRequest(state.Repository, "finding", "generation")
			_, _, _, err := unsafe.ReserveIssueGeneration(request)
			return err
		},
		func() error {
			request := testIssueGenerationRequest(state.Repository, "finding", "generation")
			request.ExpectedDraftVersion = 1
			_, _, _, err := unsafe.BeginIssueRegeneration(state.Repository, "draft", request)
			return err
		},
		func() error {
			_, _, err := unsafe.CompleteIssueGeneration(
				state.Repository, "draft", "generation", "title", "body", nil, "",
			)
			return err
		},
		func() error { _, err := unsafe.DeleteIssueDraft(state.Repository, "draft", 1); return err },
		func() error {
			_, _, err := unsafe.LinkExistingIssue(ExistingIssueLink{
				Repository: state.Repository, FindingID: "finding", ExpectedFindingVersion: 1,
				ExternalID: "1", ExternalURL: "https://github.com/owner/repo/issues/1",
				Title: "title", Confirmed: true,
			})
			return err
		},
		func() error {
			_, err := unsafe.UnlinkExistingIssue(state.Repository, "finding", 1, true)
			return err
		},
	}
	for index, call := range calls {
		if err := call(); err == nil {
			t.Fatalf("unsafe public mutation %d succeeded", index)
		}
	}
}

func TestRepositoryReviewLockFileBoundaries(t *testing.T) {
	t.Run("controller lifecycle and contention", func(t *testing.T) {
		store := NewStore(t.TempDir())
		unlock, err := store.LockAutomationController()
		if err != nil {
			t.Fatal(err)
		}
		if _, secondErr := store.LockAutomationController(); !errors.Is(secondErr, ErrAutomationControllerLocked) {
			t.Fatalf("second controller lock error = %v", secondErr)
		}
		unlock()
		unlock, err = store.LockAutomationController()
		if err != nil {
			t.Fatalf("controller lock after release: %v", err)
		}
		unlock()
	})
	t.Run("controller irregular lock", func(t *testing.T) {
		store := NewStore(t.TempDir())
		if err := os.MkdirAll(store.root+".controller.lock", 0o700); err != nil {
			t.Fatal(err)
		}
		if _, err := store.LockAutomationController(); err == nil {
			t.Fatal("controller directory lock was accepted")
		}
	})
	t.Run("store irregular lock", func(t *testing.T) {
		root := filepath.Join(t.TempDir(), "reviews")
		if err := os.MkdirAll(root+".lock", 0o700); err != nil {
			t.Fatal(err)
		}
		if _, err := lockRepositoryReviewStore(root); err == nil {
			t.Fatal("store directory lock was accepted")
		}
	})
	t.Run("not a directory", func(t *testing.T) {
		parent := filepath.Join(t.TempDir(), "file")
		if err := os.WriteFile(parent, nil, 0o600); err != nil {
			t.Fatal(err)
		}
		store := NewStore(filepath.Join(parent, "child"))
		if _, err := store.LockAutomationController(); err == nil {
			t.Fatal("controller lock below file succeeded")
		}
		if _, err := lockRepositoryReviewStore(store.root); err == nil {
			t.Fatal("store lock below file succeeded")
		}
	})
}

func TestRepositoryReviewAutomationPublicMutationsRejectUnsafeStore(t *testing.T) {
	store := NewStore(t.TempDir())
	target := filepath.Join(t.TempDir(), "lock")
	if err := os.WriteFile(target, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, store.root+".lock"); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	fixture := validAutomationForTest("rra_unsafe", "Unsafe")
	calls := []func() error{
		func() error { _, err := store.ListAutomations(context.Background()); return err },
		func() error { _, _, err := store.GetAutomation(context.Background(), fixture.ID); return err },
		func() error { _, err := store.CreateAutomation(context.Background(), fixture); return err },
		func() error {
			_, err := store.UpdateAutomation(
				context.Background(),
				fixture.ID,
				1,
				func(*RepositoryReviewAutomation) error { return nil },
			)
			return err
		},
		func() error { return store.DeleteAutomation(context.Background(), fixture.ID, 1) },
	}
	for index, call := range calls {
		if err := call(); err == nil {
			t.Fatalf("unsafe automation operation %d succeeded", index)
		}
	}
}

func TestRepositoryReviewAutomationCorruptStatePropagates(t *testing.T) {
	store := newAutomationTestStore(t)
	if err := os.MkdirAll(store.root, 0o700); err != nil {
		t.Fatal(err)
	}
	id := "rra_corrupt"
	if err := os.WriteFile(store.automationPath(id), []byte(`{`), 0o600); err != nil {
		t.Fatal(err)
	}
	fixture := validAutomationForTest(id, "Corrupt")
	calls := []func() error{
		func() error { _, err := store.CreateAutomation(context.Background(), fixture); return err },
		func() error {
			_, err := store.UpdateAutomation(
				context.Background(),
				id,
				1,
				func(*RepositoryReviewAutomation) error { return nil },
			)
			return err
		},
		func() error { return store.DeleteAutomation(context.Background(), id, 1) },
	}
	for index, call := range calls {
		if err := call(); err == nil {
			t.Fatalf("corrupt automation operation %d succeeded", index)
		}
	}
}

func TestRepositoryReviewAutomationSaveRejectsUnsafeTargets(t *testing.T) {
	fixture := validAutomationForTest("rra_save", "Save")
	fixture.SchemaVersion = RepositoryReviewAutomationSchemaVersion
	fixture.Version = 1
	fixture.Status = RepositoryReviewAutomationIdle
	fixture.CreatedAt = automationTestNow
	fixture.UpdatedAt = automationTestNow
	unencodable := fixture
	unencodable.UpdatedAt = time.Date(10_000, time.January, 1, 0, 0, 0, 0, time.UTC)
	if err := NewStore(t.TempDir()).saveAutomation(unencodable); err == nil {
		t.Fatal("automation with an unencodable timestamp was saved")
	}

	t.Run("root file", func(t *testing.T) {
		store := NewStore(t.TempDir())
		if err := os.WriteFile(store.root, []byte("file"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := store.saveAutomation(fixture); err == nil {
			t.Fatal("automation was saved through a root file")
		}
	})
	t.Run("uncreatable root", func(t *testing.T) {
		workspace := t.TempDir()
		if err := os.Chmod(workspace, 0o500); err != nil {
			t.Skipf("cannot restrict workspace: %v", err)
		}
		t.Cleanup(func() { _ = os.Chmod(workspace, 0o700) })
		if err := NewStore(workspace).saveAutomation(fixture); err == nil {
			t.Skip("filesystem user can bypass directory permissions")
		}
	})
	t.Run("target directory", func(t *testing.T) {
		store := NewStore(t.TempDir())
		if err := os.MkdirAll(store.automationPath(fixture.ID), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := store.saveAutomation(fixture); err == nil {
			t.Fatal("automation replaced a directory")
		}
	})
	t.Run("inaccessible target", func(t *testing.T) {
		store := NewStore(t.TempDir())
		if err := os.MkdirAll(store.root, 0o700); err != nil {
			t.Fatal(err)
		}
		repositoryReviewDenyPermissions(t, store.root, 0o700)
		if err := store.saveAutomation(fixture); err == nil {
			t.Skip("filesystem user can bypass directory permissions")
		}
	})
}

func TestRepositoryReviewAutomationAdditionalNormalizationBranches(t *testing.T) {
	base := validAutomationForTest("rra_more", "More")
	base.SchemaVersion = RepositoryReviewAutomationSchemaVersion
	base.Version = 1
	base.Status = RepositoryReviewAutomationIdle
	base.CreatedAt = automationTestNow
	base.UpdatedAt = automationTestNow

	for _, mutate := range []func(*RepositoryReviewAutomation){
		func(value *RepositoryReviewAutomation) { value.BudgetPolicy.GuardExpression = "spent.tokens.* > 0" },
		func(value *RepositoryReviewAutomation) { value.RunIDs = []string{"run", " run "} },
		func(value *RepositoryReviewAutomation) {
			value.ModelStats = map[string]RepositoryReviewModelStats{"review-a": {Requests: -1}}
		},
		func(value *RepositoryReviewAutomation) {
			value.ModelCoverageSketches = map[string]string{"review-a": "bad"}
		},
	} {
		value := cloneAutomation(base)
		mutate(&value)
		if err := normalizeAutomation(&value); err == nil {
			t.Fatalf("invalid normalized automation was accepted: %#v", value)
		}
	}

	cost := cloneAutomation(base)
	cost.CompareModels = false
	cost.BudgetPolicy.GuardExpression = "spend.total.usd < 1"
	cost.ModelPrices = map[string]RepositoryReviewModelPrice{"review-a": {InputPricePer1M: 1}}
	if err := normalizeAutomation(&cost); err != nil {
		t.Fatalf("single-model cost automation: %v", err)
	}

	tooManySnapshots := cloneAutomation(base)
	tooManySnapshots.AccountLimitSnapshots = make(
		[]RepositoryReviewAccountLimitSnapshot,
		maxAutomationAccountSnapshots+1,
	)
	if err := normalizeAccountSnapshots(&tooManySnapshots); err == nil {
		t.Fatal("too many snapshots were accepted")
	}

	sorted := cloneAutomation(base)
	sorted.AccountLimitSnapshots = []RepositoryReviewAccountLimitSnapshot{
		{AccountID: "b", Name: "a", Window: "weekly", CheckedAt: automationTestNow},
		{AccountID: "a", Name: "b", Window: "weekly", CheckedAt: automationTestNow},
		{AccountID: "a", Name: "a", Window: "weekly", CheckedAt: automationTestNow},
		{AccountID: "a", Name: "a", Window: "daily", CheckedAt: automationTestNow},
	}
	if err := normalizeAccountSnapshots(&sorted); err != nil {
		t.Fatal(err)
	}
	if sorted.AccountLimitSnapshots[0].Window != "daily" || sorted.AccountLimitSnapshots[3].AccountID != "b" {
		t.Fatalf("sorted snapshots = %#v", sorted.AccountLimitSnapshots)
	}

	nilMaps := RepositoryReviewAutomation{}
	cloned := cloneAutomation(nilMaps)
	if cloned.ModelPrices != nil || cloned.ModelStats != nil || cloned.ModelCoverageSketches != nil {
		t.Fatalf("nil maps became non-nil: %#v", cloned)
	}
}

func TestRepositoryReviewCorruptStatePropagatesAcrossMutations(t *testing.T) {
	store := NewStore(t.TempDir())
	if err := os.MkdirAll(store.root, 0o700); err != nil {
		t.Fatal(err)
	}
	repository := "owner/repo"
	if err := os.WriteFile(store.path(repository), []byte(`{`), 0o600); err != nil {
		t.Fatal(err)
	}
	plan := Plan{
		Repository: repository, CommitSHA: "commit", InventoryHash: "inventory",
		ProfileHash: "profile", Authoritative: true, CreatedAt: repositoryAuditTestNow,
	}
	plan.ID = planDigest(plan)
	calls := []func() error{
		func() error {
			_, err := store.Plan(context.Background(), repository, "commit", "inventory", nil, false)
			return err
		},
		func() error {
			_, err := store.Record(context.Background(), RecordRequest{Plan: plan, RunID: "run"})
			return err
		},
		func() error { _, err := store.FinalizeNoopPlan(plan); return err },
		func() error { _, err := store.SetFindingStatus(repository, "finding", FindingOpen, 1); return err },
		func() error {
			_, _, err := store.PrepareIssue(IssueDraftRequest{Repository: repository, FindingIDs: []string{"finding"}})
			return err
		},
		func() error {
			_, _, err := store.UpdateIssueDraft(repository, "draft", "title", "body", nil, 1)
			return err
		},
		func() error {
			_, _, err := store.SetIssueDraftPublication(repository, "draft", 1, IssueDraftUnknown, "", "")
			return err
		},
		func() error { _, _, _, err := store.ClaimIssueDraftPublication(repository, "draft", 1); return err },
		func() error {
			_, _, err := store.SetFindingStatusByVersion(repository, "finding", FindingOpen, 1)
			return err
		},
		func() error {
			request := testIssueGenerationRequest(repository, "finding", "generation")
			_, _, _, err := store.ReserveIssueGeneration(request)
			return err
		},
		func() error {
			request := testIssueGenerationRequest(repository, "finding", "generation")
			request.ExpectedDraftVersion = 1
			_, _, _, err := store.BeginIssueRegeneration(repository, "draft", request)
			return err
		},
		func() error {
			_, _, err := store.CompleteIssueGeneration(
				repository, "draft", "generation", "title", "body", nil, "",
			)
			return err
		},
		func() error { _, err := store.DeleteIssueDraft(repository, "draft", 1); return err },
		func() error {
			_, _, err := store.LinkExistingIssue(ExistingIssueLink{
				Repository: repository, FindingID: "finding", ExpectedFindingVersion: 1,
				ExternalID: "1", ExternalURL: "https://github.com/owner/repo/issues/1",
				Title: "title", Confirmed: true,
			})
			return err
		},
		func() error { _, err := store.UnlinkExistingIssue(repository, "finding", 1, true); return err },
	}
	for index, call := range calls {
		if err := call(); err == nil {
			t.Fatalf("corrupt state mutation %d succeeded", index)
		}
	}
}

func TestRepositoryReviewRemainingUtilityBranches(t *testing.T) {
	if (Store{}).clock().IsZero() || NewStore(t.TempDir()).clock().IsZero() {
		t.Fatal("default store clock returned zero")
	}
	files := make([]FileRef, 0, 4200)
	pathPrefix := strings.Repeat("x", 4080)
	for index := 0; index < 4200; index++ {
		files = append(files, FileRef{
			Path: pathPrefix + automationTestIndex(index), BlobSHA: strings.Repeat("a", 40), SizeBytes: 1,
		})
	}
	if _, err := normalizeFiles(files); !errors.Is(err, ErrInvalidPlan) {
		t.Fatalf("oversized inventory metadata error = %v", err)
	}

	state := repositoryReviewCoverageState("owner/repo")
	state.Findings = []Finding{{ContextIDs: make([]string, 65)}}
	if err := validateState(state); err == nil {
		t.Fatal("too many finding contexts were accepted")
	}
	if got := defaultIssueTitle([]Finding{{Title: "one"}, {Title: "two"}}); !strings.Contains(got, "2") {
		t.Fatalf("multi-finding title = %q", got)
	}
	if got := truncateUTF8Bytes("é", 1); got != "" {
		t.Fatalf("partial UTF-8 truncation = %q", got)
	}

	parent := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(parent, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := NewStore(filepath.Join(parent, "child")).requireSafeRoot(false); err == nil {
		t.Fatal("ENOTDIR root was accepted")
	}
}

func TestRepositoryReviewAutomationCatalogAndCloneBranches(t *testing.T) {
	store := newAutomationTestStore(t)
	if err := os.MkdirAll(store.root, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(store.root, "ignored.txt"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	disappearedPath := store.automationPath("rra_disappeared")
	if err := os.WriteFile(disappearedPath, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.listAutomationsWithLoader(
		context.Background(), maxAutomationCount,
		func(string) (RepositoryReviewAutomation, bool, error) {
			return RepositoryReviewAutomation{}, false, nil
		},
	); err == nil {
		t.Fatal("automation disappearing during a locked catalog read was accepted")
	}
	if err := os.Remove(disappearedPath); err != nil {
		t.Fatal(err)
	}
	first := createAutomationForTest(t, store, "rra_equal_a", "A")
	second := createAutomationForTest(t, store, "rra_equal_b", "B")
	if _, err := store.listAutomations(context.Background(), 1); !errors.Is(err, ErrInvalidAutomation) {
		t.Fatalf("bounded automation catalog error = %v", err)
	}
	listed, err := store.ListAutomations(context.Background())
	if err != nil || len(listed) != 2 || listed[0].ID != first.ID || listed[1].ID != second.ID {
		t.Fatalf("equal-time automations=%#v err=%v", listed, err)
	}

	if err := os.WriteFile(store.automationPath(first.ID), []byte(`{`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ListAutomations(context.Background()); err == nil {
		t.Fatal("catalog accepted malformed automation")
	}

	base := validAutomationForTest("rra_branch", "Branch")
	base.ModelStats = map[string]RepositoryReviewModelStats{"unknown": {}}
	if err := normalizeModelStats(&base); err == nil {
		t.Fatal("unknown model statistics alias was accepted")
	}
	base = validAutomationForTest("rra_clone", "Clone")
	base.ModelCoverageSketches = map[string]string{"review-a": "encoded"}
	cloned := cloneAutomation(base)
	cloned.ModelCoverageSketches["review-a"] = "changed"
	if base.ModelCoverageSketches["review-a"] != "encoded" {
		t.Fatal("coverage sketch map was not detached")
	}
}

func TestRepositoryReviewAutomationUnsafeRootPropagates(t *testing.T) {
	store := NewStore(t.TempDir())
	if err := os.WriteFile(store.root, []byte("file"), 0o600); err != nil {
		t.Fatal(err)
	}
	fixture := validAutomationForTest("rra_root", "Root")
	if _, err := store.ListAutomations(context.Background()); err == nil {
		t.Fatal("unsafe root was listed")
	}
	if _, _, err := store.GetAutomation(context.Background(), fixture.ID); err == nil {
		t.Fatal("unsafe root automation was loaded")
	}
	fixture.SchemaVersion = RepositoryReviewAutomationSchemaVersion
	fixture.Version = 1
	fixture.Status = RepositoryReviewAutomationIdle
	fixture.CreatedAt = automationTestNow
	fixture.UpdatedAt = automationTestNow
	if err := store.saveAutomation(fixture); err == nil {
		t.Fatal("unsafe root automation was saved")
	}
}

func TestRepositoryReviewRecordCoversDuplicateContextsAndCompletion(t *testing.T) {
	store := newRepositoryAuditTestStore(t)
	file := repositoryAuditTestFile("service.go", "a", 10)
	plan, err := store.Plan(context.Background(), "owner/repo", "commit", "inventory", []FileRef{file}, false)
	if err != nil {
		t.Fatal(err)
	}
	invalid := FindingCandidate{
		Severity: "high", Title: "", File: file.Path, Evidence: "e", Impact: "i",
		Validation: Validation{Status: "confirmed", Summary: "v"},
	}
	result, err := store.Record(context.Background(), RecordRequest{
		Plan:  plan,
		RunID: "invalid-candidate",
		Observations: []Observation{
			{Model: "review-a", ScopeFiles: []FileRef{file}, Findings: []FindingCandidate{invalid}},
		},
	})
	if err != nil || result.Run.RejectedFindings != 1 {
		t.Fatalf("invalid candidate result=%#v err=%v", result, err)
	}

	plan, err = store.Plan(context.Background(), "owner/repo", "commit-2", "inventory-2", []FileRef{file}, true)
	if err != nil {
		t.Fatal(err)
	}
	candidate := FindingCandidate{
		Severity: "high", Title: "bug", File: file.Path, Symbol: "Save",
		Evidence: "unfenced write", Impact: "loss",
		Validation: Validation{Status: "confirmed", Summary: "reproduced"},
	}
	observation := Observation{Model: "review-a", ScopeFiles: []FileRef{file}, Findings: []FindingCandidate{candidate}}
	result, err = store.Record(context.Background(), RecordRequest{
		Plan: plan, RunID: "duplicate-context", Observations: []Observation{observation, observation},
		CompletedFiles: []FileRef{file},
	})
	if err != nil || len(result.State.Contexts) == 0 || result.Run.ReviewedFiles != 1 {
		t.Fatalf("duplicate context result=%#v err=%v", result, err)
	}
}

func TestRepositoryReviewRecordBoundsRunHistory(t *testing.T) {
	store, state := repositoryReviewCoverageStore(t, "owner/repo")
	state.Runs = make([]ReviewRun, 1000)
	for index := range state.Runs {
		state.Runs[index] = ReviewRun{ID: "old-" + automationTestIndex(index), PlanID: "plan"}
	}
	if err := store.save(&state); err != nil {
		t.Fatal(err)
	}
	plan, err := store.Plan(context.Background(), state.Repository, "commit", "inventory", nil, false)
	if err != nil {
		t.Fatal(err)
	}
	result, err := store.Record(context.Background(), RecordRequest{Plan: plan, RunID: "new-run"})
	if err != nil || len(result.State.Runs) != 1000 {
		t.Fatalf("bounded runs=%d err=%v", len(result.State.Runs), err)
	}
	if result.State.Runs[999].ID != "new-run" {
		t.Fatalf("last bounded run=%#v", result.State.Runs[999])
	}
}

func TestRepositoryReviewPublicationMutationAdditionalBranches(t *testing.T) {
	store, state := repositoryReviewCoverageStore(t, "owner/repo")
	now := repositoryAuditTestNow
	state.Findings = []Finding{
		{ID: "selected", Repository: state.Repository, Status: FindingOpen, Version: 1, CreatedAt: now, UpdatedAt: now},
		{ID: "other", Repository: state.Repository, Status: FindingOpen, Version: 1, CreatedAt: now, UpdatedAt: now},
	}
	state.IssueDrafts = []IssueDraft{{
		ID: "draft", Repository: state.Repository, FindingIDs: []string{"selected"},
		Title: "title", Body: "body", State: IssueDraftPublishing, Version: 1,
		CreatedAt: now, UpdatedAt: now,
	}}
	if err := store.save(&state); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.SetIssueDraftPublication(
		state.Repository,
		"draft",
		2,
		IssueDraftUnknown,
		"",
		"",
	); !errors.Is(
		err,
		ErrConflict,
	) {
		t.Fatalf("stale publication error = %v", err)
	}
	postedState, _, err := store.SetIssueDraftPublication(
		state.Repository, "draft", 1, IssueDraftPosted, "42", "https://github.com/owner/repo/issues/42",
	)
	if err != nil || postedState.Findings[0].Status != FindingPosted || postedState.Findings[1].Status != FindingOpen {
		t.Fatalf("selective publication state=%#v err=%v", postedState, err)
	}
}

func TestRepositoryReviewAdditionalPlanAndStateValidationBranches(t *testing.T) {
	store := newRepositoryAuditTestStore(t)
	files := make([]FileRef, maxReviewFiles+1)
	for index := range files {
		files[index] = FileRef{
			Path: "f/" + automationTestIndex(index), BlobSHA: strings.Repeat("a", 40), SizeBytes: 1,
		}
	}
	if _, err := store.PlanWithProfileLimit(
		context.Background(), "owner/repo", "commit", "inventory", "profile", files, false, maxReviewFiles,
	); !errors.Is(err, ErrInvalidPlan) {
		t.Fatalf("too many files error = %v", err)
	}

	for _, plan := range []Plan{
		{
			Repository: "owner/repo", CommitSHA: "commit", InventoryHash: "inventory", ProfileHash: "profile",
			PendingFiles: []FileRef{{Path: "../bad", BlobSHA: strings.Repeat("a", 40)}},
			CreatedAt:    repositoryAuditTestNow,
		},
		{
			Repository: "owner/repo", CommitSHA: "commit", InventoryHash: "inventory", ProfileHash: "profile",
			DeferredFiles: []FileRef{{Path: "../bad", BlobSHA: strings.Repeat("a", 40)}},
			CreatedAt:     repositoryAuditTestNow,
		},
	} {
		plan.ID = planDigest(plan)
		if _, err := store.Record(
			context.Background(),
			RecordRequest{Plan: plan, RunID: "invalid-files"},
		); !errors.Is(
			err,
			ErrInvalidPlan,
		) {
			t.Fatalf("invalid plan file record error = %v", err)
		}
	}

	state := repositoryReviewCoverageState("owner/repo")
	state.ReviewAttempts = map[string]int{"path": 1}
	state.ReviewAttemptIdentities = map[string]string{"path": ""}
	if err := validateState(state); err == nil {
		t.Fatal("empty review attempt identity was accepted")
	}
}

func TestRepositoryReviewListRejectsInvalidState(t *testing.T) {
	store := NewStore(t.TempDir())
	if err := os.MkdirAll(store.root, 0o700); err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(
		RepositoryState{SchemaVersion: 99, ID: RepositoryID("owner/repo"), Repository: "owner/repo"},
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(store.path("owner/repo"), data, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.List(); err == nil {
		t.Fatal("invalid state was listed")
	}
}

func TestRepositoryReviewRecordUpdatesExistingContext(t *testing.T) {
	store, state := repositoryReviewCoverageStore(t, "owner/repo")
	file := repositoryAuditTestFile("service.go", "a", 10)
	plan, err := store.Plan(context.Background(), state.Repository, "commit", "inventory", []FileRef{file}, false)
	if err != nil {
		t.Fatal(err)
	}
	candidate := FindingCandidate{
		Severity: "high", Title: "bug", File: file.Path, Symbol: "Save",
		Evidence: "unfenced", Impact: "loss",
		Validation: Validation{Status: "confirmed", Summary: "reproduced"},
	}
	contextRecord := FindingContext{
		Repository: state.Repository, CommitSHA: plan.CommitSHA, InventoryHash: plan.InventoryHash,
		ProfileHash: plan.ProfileHash, RunID: "existing-context", Model: "review-a",
		Files: []FileRef{file}, CreatedAt: repositoryAuditTestNow,
	}
	contextRecord.ID = stableID("rctx_", contextBindingDigest(contextRecord))
	state.Contexts = []FindingContext{contextRecord}
	if saveErr := store.save(&state); saveErr != nil {
		t.Fatal(saveErr)
	}
	result, err := store.Record(context.Background(), RecordRequest{
		Plan:  plan,
		RunID: "existing-context",
		Observations: []Observation{
			{Model: "review-a", ScopeFiles: []FileRef{file}, Findings: []FindingCandidate{candidate}},
		},
		CompletedAt: repositoryAuditTestNow,
	})
	if err != nil || len(result.State.Contexts) != 1 || result.State.Contexts[0].ID != contextRecord.ID {
		t.Fatalf("existing context result=%#v err=%v", result, err)
	}
}

func TestRepositoryReviewAutomationMethodsHonorCancellationAfterLock(t *testing.T) {
	store := newAutomationTestStore(t)
	fixture := createAutomationForTest(t, store, "rra_cancel_after_lock", "Cancel")
	tests := []struct {
		name string
		key  string
		call func(context.Context) error
	}{
		{
			name: "list", key: "repository-review-automations",
			call: func(ctx context.Context) error {
				_, err := store.ListAutomations(ctx)
				return err
			},
		},
		{
			name: "get", key: "automation:" + fixture.ID,
			call: func(ctx context.Context) error {
				_, _, err := store.GetAutomation(ctx, fixture.ID)
				return err
			},
		},
		{
			name: "create", key: "automation:rra_cancel_create",
			call: func(ctx context.Context) error {
				_, err := store.CreateAutomation(ctx, validAutomationForTest("rra_cancel_create", "Cancel create"))
				return err
			},
		},
		{
			name: "update", key: "automation:" + fixture.ID,
			call: func(ctx context.Context) error {
				_, err := store.UpdateAutomation(
					ctx,
					fixture.ID,
					fixture.Version,
					func(*RepositoryReviewAutomation) error {
						return nil
					},
				)
				return err
			},
		},
		{
			name: "delete", key: "automation:" + fixture.ID,
			call: func(ctx context.Context) error {
				return store.DeleteAutomation(ctx, fixture.ID, fixture.Version)
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			key := store.root + "\x00" + test.key
			value, _ := storeLocks.LoadOrStore(key, &sync.Mutex{})
			mutex := value.(*sync.Mutex)
			mutex.Lock()
			ctx := &repositoryReviewCancelAfterFirstContext{
				Context: context.Background(),
				first:   make(chan struct{}),
			}
			done := make(chan error, 1)
			go func() { done <- test.call(ctx) }()
			<-ctx.first
			ctx.canceled.Store(true)
			mutex.Unlock()
			if callErr := <-done; !errors.Is(callErr, context.Canceled) {
				t.Fatalf("operation error = %v, want context.Canceled", callErr)
			}
		})
	}
}

func poisonRepositoryReviewStoreOnClock(t *testing.T, store *Store) {
	t.Helper()
	var poisoned atomic.Bool
	store.now = func() time.Time {
		if poisoned.CompareAndSwap(false, true) {
			if removeErr := os.RemoveAll(store.root); removeErr != nil {
				t.Errorf("remove store root: %v", removeErr)
			} else if writeErr := os.WriteFile(store.root, []byte("not a directory"), 0o600); writeErr != nil {
				t.Errorf("replace store root: %v", writeErr)
			}
		}
		// Repository state fixtures are created at repositoryAuditTestNow while
		// automation fixtures are created three hours later. Returning a later
		// instant ensures the mutation itself is valid and the assertion exercises
		// the intended durable-write failure rather than timestamp validation.
		return automationTestNow.Add(time.Hour)
	}
}

func TestRepositoryReviewMutationsReportPersistenceFailures(t *testing.T) {
	newState := func(t *testing.T) (Store, RepositoryState) {
		t.Helper()
		store, state := repositoryReviewCoverageStore(t, "owner/repo")
		now := repositoryAuditTestNow
		state.Findings = []Finding{{
			ID: "finding", Repository: state.Repository, Status: FindingOpen,
			Version: 1, CreatedAt: now, UpdatedAt: now,
		}}
		state.IssueDrafts = []IssueDraft{{
			ID: "draft", Repository: state.Repository, FindingIDs: []string{"finding"},
			Title: "title", Body: "body", State: IssueDraftEditing, Version: 1,
			CreatedAt: now, UpdatedAt: now,
		}}
		if saveErr := store.save(&state); saveErr != nil {
			t.Fatal(saveErr)
		}
		return store, state
	}

	t.Run("finding status", func(t *testing.T) {
		store, state := newState(t)
		state.Findings[0].IssueDraftID = ""
		state.IssueDrafts = nil
		if saveErr := store.save(&state); saveErr != nil {
			t.Fatal(saveErr)
		}
		poisonRepositoryReviewStoreOnClock(t, &store)
		if _, mutationErr := store.SetFindingStatus(
			state.Repository,
			"finding",
			FindingDismissed,
			state.Version,
		); mutationErr == nil {
			t.Fatal("finding mutation ignored persistence failure")
		}
	})
	t.Run("prepare issue", func(t *testing.T) {
		store, state := newState(t)
		poisonRepositoryReviewStoreOnClock(t, &store)
		if _, _, mutationErr := store.PrepareIssue(IssueDraftRequest{
			Repository: state.Repository, FindingIDs: []string{"finding"},
			Title: "issue", Body: "body", ExpectedVersion: state.Version,
		}); mutationErr == nil {
			t.Fatal("issue preparation ignored persistence failure")
		}
	})
	t.Run("update issue", func(t *testing.T) {
		store, state := newState(t)
		poisonRepositoryReviewStoreOnClock(t, &store)
		if _, _, mutationErr := store.UpdateIssueDraft(
			state.Repository,
			"draft",
			"updated",
			"body",
			nil,
			1,
		); mutationErr == nil {
			t.Fatal("issue update ignored persistence failure")
		}
	})
	t.Run("claim issue", func(t *testing.T) {
		store, state := newState(t)
		poisonRepositoryReviewStoreOnClock(t, &store)
		if _, _, _, mutationErr := store.ClaimIssueDraftPublication(state.Repository, "draft", 1); mutationErr == nil {
			t.Fatal("issue claim ignored persistence failure")
		}
	})
	t.Run("publish issue", func(t *testing.T) {
		store, state := newState(t)
		state.IssueDrafts[0].State = IssueDraftPublishing
		if saveErr := store.save(&state); saveErr != nil {
			t.Fatal(saveErr)
		}
		poisonRepositoryReviewStoreOnClock(t, &store)
		if _, _, mutationErr := store.SetIssueDraftPublication(
			state.Repository, "draft", 1, IssueDraftUnknown, "", "",
		); mutationErr == nil {
			t.Fatal("issue publication ignored persistence failure")
		}
	})
	t.Run("record", func(t *testing.T) {
		store := newRepositoryAuditTestStore(t)
		plan, planErr := store.Plan(context.Background(), "owner/repo", "commit", "inventory", nil, false)
		if planErr != nil {
			t.Fatal(planErr)
		}
		poisonRepositoryReviewStoreOnClock(t, &store)
		if _, recordErr := store.Record(
			context.Background(),
			RecordRequest{Plan: plan, RunID: "run"},
		); recordErr == nil {
			t.Fatal("record ignored persistence failure")
		}
	})
	t.Run("finalize", func(t *testing.T) {
		store := newRepositoryAuditTestStore(t)
		plan, planErr := store.PlanWithProfileLimitAuthoritative(
			context.Background(), "owner/repo", "commit", "inventory", "profile", nil, false, 1, true,
		)
		if planErr != nil {
			t.Fatal(planErr)
		}
		poisonRepositoryReviewStoreOnClock(t, &store)
		if _, finalizeErr := store.FinalizeNoopPlan(plan); finalizeErr == nil {
			t.Fatal("finalize ignored persistence failure")
		}
	})
}

func TestRepositoryReviewAutomationMutationsReportPersistenceFailures(t *testing.T) {
	t.Run("create", func(t *testing.T) {
		store := newAutomationTestStore(t)
		poisonRepositoryReviewStoreOnClock(t, &store)
		if _, createErr := store.CreateAutomation(
			context.Background(), validAutomationForTest("rra_poison_create", "Create"),
		); createErr == nil {
			t.Fatal("automation create ignored persistence failure")
		}
	})
	t.Run("update", func(t *testing.T) {
		store := newAutomationTestStore(t)
		created := createAutomationForTest(t, store, "rra_poison_update", "Update")
		poisonRepositoryReviewStoreOnClock(t, &store)
		if _, updateErr := store.UpdateAutomation(
			context.Background(), created.ID, created.Version,
			func(value *RepositoryReviewAutomation) error { value.Name = "changed"; return nil },
		); updateErr == nil {
			t.Fatal("automation update ignored persistence failure")
		}
	})
}

func TestRepositoryReviewLoadNormalizesExplicitNullMaps(t *testing.T) {
	store, state := repositoryReviewCoverageStore(t, "owner/repo")
	wire := map[string]any{}
	data, marshalErr := json.Marshal(state)
	if marshalErr != nil {
		t.Fatal(marshalErr)
	}
	if decodeErr := json.Unmarshal(data, &wire); decodeErr != nil {
		t.Fatal(decodeErr)
	}
	wire["unsupported"] = nil
	wire["review_attempts"] = nil
	wire["review_attempt_identities"] = nil
	data, marshalErr = json.Marshal(wire)
	if marshalErr != nil {
		t.Fatal(marshalErr)
	}
	if writeErr := os.WriteFile(store.path(state.Repository), data, 0o600); writeErr != nil {
		t.Fatal(writeErr)
	}
	loaded, loadErr := store.load(state.Repository)
	if loadErr != nil || loaded.Unsupported == nil || loaded.ReviewAttempts == nil ||
		loaded.ReviewAttemptIdentities == nil {
		t.Fatalf("normalized state=%#v err=%v", loaded, loadErr)
	}
}

func TestRepositoryReviewRemainingPersistenceAndAssociationBoundaries(t *testing.T) {
	t.Run("noncanonical publication", func(t *testing.T) {
		store, state := repositoryReviewCoverageStore(t, "owner/noncanonical")
		now := repositoryAuditTestNow
		state.Findings = []Finding{{
			ID: "finding", Repository: state.Repository, Status: FindingOpen,
			IssueDraftID: "canonical", Version: 1, CreatedAt: now, UpdatedAt: now,
		}}
		state.IssueDrafts = []IssueDraft{
			{
				ID: "canonical", Repository: state.Repository, FindingIDs: []string{"finding"},
				Title: "canonical", Body: "canonical", Origin: IssueDraftOriginLegacy,
				State: IssueDraftEditing, Canonical: true, Version: 1, CreatedAt: now, UpdatedAt: now,
			},
			{
				ID: "legacy-conflict", Repository: state.Repository, FindingIDs: []string{"finding"},
				Title: "legacy", Body: "legacy", Origin: IssueDraftOriginLegacy,
				State: IssueDraftEditing, Version: 1, CreatedAt: now, UpdatedAt: now,
			},
		}
		if saveErr := store.save(&state); saveErr != nil {
			t.Fatal(saveErr)
		}
		if _, statusErr := store.SetFindingStatus(
			state.Repository, "finding", FindingDismissed, state.Version,
		); !errors.Is(statusErr, ErrConflict) {
			t.Fatalf("associated finding status error = %v", statusErr)
		}
		if _, _, publicationErr := store.SetIssueDraftPublication(
			state.Repository, "legacy-conflict", 1, IssueDraftUnknown, "", "",
		); !errors.Is(publicationErr, ErrConflict) {
			t.Fatalf("noncanonical publication error = %v", publicationErr)
		}
		if _, _, _, claimErr := store.ClaimIssueDraftPublication(
			state.Repository, "legacy-conflict", 1,
		); !errors.Is(claimErr, ErrConflict) {
			t.Fatalf("noncanonical claim error = %v", claimErr)
		}
	})

	t.Run("legacy rewrite failure", func(t *testing.T) {
		store := NewStore(t.TempDir())
		state := repositoryReviewCoverageState("owner/legacy-rewrite")
		now := repositoryAuditTestNow
		state.Findings = []Finding{{
			ID: "finding", Repository: state.Repository, Status: FindingOpen,
			Version: 1, CreatedAt: now, UpdatedAt: now,
		}}
		state.IssueDrafts = []IssueDraft{{
			ID: "legacy", Repository: state.Repository, FindingIDs: []string{"finding"},
			Title: "legacy", Body: "legacy", State: IssueDraftEditing,
			Version: 1, CreatedAt: now, UpdatedAt: now,
		}}
		data, marshalErr := json.Marshal(state)
		if marshalErr != nil {
			t.Fatal(marshalErr)
		}
		if mkdirErr := os.MkdirAll(store.root, 0o700); mkdirErr != nil {
			t.Fatal(mkdirErr)
		}
		statePath := store.path(state.Repository)
		if writeErr := os.WriteFile(statePath, data, 0o600); writeErr != nil {
			t.Fatal(writeErr)
		}
		summaryPath := strings.TrimSuffix(statePath, ".json") + ".summary.json"
		if mkdirErr := os.Mkdir(summaryPath, 0o700); mkdirErr != nil {
			t.Fatal(mkdirErr)
		}
		if writeErr := os.WriteFile(filepath.Join(summaryPath, "keep"), []byte("keep"), 0o600); writeErr != nil {
			t.Fatal(writeErr)
		}
		if _, loadErr := store.load(state.Repository); loadErr == nil {
			t.Fatal("legacy rewrite ignored an unremovable summary projection")
		}
	})

	t.Run("state size limit", func(t *testing.T) {
		store := NewStore(t.TempDir())
		state := repositoryReviewCoverageState("owner/oversized")
		state.Findings = []Finding{{
			ID: "finding", Repository: state.Repository, Status: FindingOpen,
			Evidence: strings.Repeat("x", int(maxStateFileBytes)),
		}}
		if saveErr := store.save(&state); saveErr == nil ||
			!strings.Contains(saveErr.Error(), "exceeds its size limit") {
			t.Fatalf("oversized state error = %v", saveErr)
		}
	})

	t.Run("finding selection cardinality", func(t *testing.T) {
		findings := []Finding{{ID: "finding"}}
		if _, _, selectionErr := selectedFindings(findings, nil); selectionErr == nil {
			t.Fatal("empty finding selection was accepted")
		}
		if _, _, selectionErr := selectedFindings(
			findings, []string{"finding", "finding"},
		); selectionErr == nil || !strings.Contains(selectionErr.Error(), "duplicate") {
			t.Fatalf("duplicate finding selection error = %v", selectionErr)
		}
	})
}
