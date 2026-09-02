package evolution

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/database"
)

func TestEvolutionBrokerClientFailClosedAndProfileBoundaries(t *testing.T) {
	sentinel := database.NewError(database.CodeUnavailable, "broker unavailable")
	if err := (*Store)(
		nil,
	).brokerCall("read", evolutionBrokerRequest{}, nil, false); !errors.Is(err, sentinel) &&
		err == nil {
		t.Fatalf("nil store broker call = %v", err)
	}
	failed := &Store{brokerErr: sentinel}
	if err := failed.brokerCall("read", evolutionBrokerRequest{}, nil, false); !errors.Is(err, sentinel) {
		t.Fatalf("failed broker call = %v", err)
	}
	if err := failed.brokerRecords(t.Context(), evolutionOpAppendRecords, "task", nil); !errors.Is(err, sentinel) {
		t.Fatalf("failed record call = %v", err)
	}
	if err := failed.brokerIDs(evolutionOpMarkClustered, nil); !errors.Is(err, sentinel) {
		t.Fatalf("failed ID call = %v", err)
	}
	if err := failed.brokerDrafts(evolutionOpSaveDrafts, nil); !errors.Is(err, sentinel) {
		t.Fatalf("failed draft call = %v", err)
	}
	if err := failed.brokerSaveProfile(SkillProfile{}, "", false); !errors.Is(err, sentinel) {
		t.Fatalf("failed profile save = %v", err)
	}
	if _, err := failed.brokerLoadRecords("task"); !errors.Is(err, sentinel) {
		t.Fatalf("failed record load = %v", err)
	}
	if _, err := failed.brokerLoadDrafts(); !errors.Is(err, sentinel) {
		t.Fatalf("failed draft load = %v", err)
	}
	if _, err := failed.brokerLoadProfiles(); !errors.Is(err, sentinel) {
		t.Fatalf("failed profile list = %v", err)
	}
	if _, err := failed.brokerLoadProfile("skill"); !errors.Is(err, sentinel) {
		t.Fatalf("failed profile load = %v", err)
	}
	if err := failed.brokerUpdateProfile("workspace", "skill", nil); err == nil {
		t.Fatal("nil profile update succeeded")
	}

	fixture := newEvolutionBrokerFixture(t)
	if err := fixture.primaryStore.brokerUpdateProfile(
		fixture.primary, "missing",
		func(*SkillProfile, bool) error { return errors.New("callback stopped") },
	); err == nil || err.Error() != "callback stopped" {
		t.Fatalf("profile callback = %v", err)
	}
	if err := fixture.primaryStore.brokerUpdateProfile(
		fixture.primary, "missing", func(*SkillProfile, bool) error { return nil },
	); err != nil {
		t.Fatalf("zero absent profile update = %v", err)
	}
	if _, err := fixture.primaryStore.brokerLoadProfile("missing"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing profile = %v", err)
	}
	if digest, err := evolutionProfileDigest(SkillProfile{}, false); err != nil || digest != "absent" {
		t.Fatalf("absent digest = %q, %v", digest, err)
	}
	values := make([]int, evolutionBrokerPageSize+1)
	if page, more := paginateEvolution(values, -1); len(page) != evolutionBrokerPageSize || !more {
		t.Fatalf("first page = %d, %v", len(page), more)
	}
	if page, more := paginateEvolution(values, len(values)+10); len(page) != 0 || more {
		t.Fatalf("past-end page = %d, %v", len(page), more)
	}
}

func TestEvolutionBrokerHandlerValidationDispatchAndClose(t *testing.T) {
	fixture := newEvolutionBrokerFixture(t)
	handler := fixture.handler
	if _, err := (*BrokerHandler)(
		nil,
	).Handle(t.Context(), database.Request{}); database.CodeOf(
		err,
	) != database.CodeUnsupported {
		t.Fatalf("nil handler = %v", err)
	}
	if _, err := handler.Handle(
		t.Context(),
		database.Request{Domain: "wrong"},
	); database.CodeOf(
		err,
	) != database.CodeUnsupported {
		t.Fatalf("wrong domain = %v", err)
	}
	for _, operation := range []string{
		BrokerPreflightOperation, evolutionOpAppendRecords, evolutionOpSaveRecords,
		evolutionOpLoadRecords, evolutionOpMarkClustered, evolutionOpMergePatterns,
		evolutionOpSaveDrafts, evolutionOpLoadDrafts, evolutionOpSaveProfile,
		evolutionOpLoadProfile, evolutionOpLoadProfiles,
	} {
		if _, err := handler.Handle(t.Context(), database.Request{
			Domain: BrokerDomain, Version: BrokerVersion, Operation: operation,
			Payload: json.RawMessage(`{`),
		}); database.CodeOf(err) != database.CodeInvalid {
			t.Errorf("%s malformed request = %v", operation, err)
		}
	}
	if _, err := handler.Handle(t.Context(), evolutionRequest(
		t, BrokerPreflightOperation, evolutionBrokerTargetRequest{StoreID: "workspace.unknown"},
	)); database.CodeOf(err) != database.CodeUnauthorized {
		t.Fatalf("unknown preflight store = %v", err)
	}
	if _, err := handler.Handle(t.Context(), evolutionRequest(
		t, evolutionOpLoadRecords, evolutionBrokerRequest{StoreID: "workspace.unknown"},
	)); database.CodeOf(err) != database.CodeUnauthorized {
		t.Fatalf("unknown operation store = %v", err)
	}
	if _, err := handler.Handle(t.Context(), evolutionRequest(
		t, "unknown", evolutionBrokerRequest{StoreID: fixture.primaryStore.storeID},
	)); database.CodeOf(err) != database.CodeUnsupported {
		t.Fatalf("unknown operation = %v", err)
	}
	if _, err := handler.Handle(t.Context(), evolutionRequest(
		t, evolutionOpAppendRecords,
		evolutionBrokerRequest{
			StoreID: fixture.primaryStore.storeID, Class: "invalid", Records: []LearningRecord{{}},
		},
	)); database.CodeOf(err) != database.CodeInternal {
		t.Fatalf("invalid append = %v", err)
	}
	if _, err := handler.Handle(t.Context(), evolutionRequest(
		t, evolutionOpSaveProfile,
		evolutionBrokerRequest{StoreID: fixture.primaryStore.storeID, Profile: SkillProfile{}},
	)); database.CodeOf(err) != database.CodeInternal {
		t.Fatalf("invalid profile save = %v", err)
	}

	if _, err := (*evolutionBrokerStore)(nil).open(); database.CodeOf(err) != database.CodeUnavailable {
		t.Fatalf("nil target open = %v", err)
	}
	if err := (*evolutionBrokerStore)(nil).close(); err != nil {
		t.Fatalf("nil target close = %v", err)
	}
	poisoned := &evolutionBrokerStore{}
	poisoned.once.Do(func() { poisoned.err = errors.New("provider failed") })
	poisonedHandler := &BrokerHandler{
		stores: map[database.StoreID]*evolutionBrokerStore{"workspace.evolution": poisoned},
	}
	if _, err := poisonedHandler.Handle(t.Context(), evolutionRequest(
		t, BrokerPreflightOperation, evolutionBrokerTargetRequest{StoreID: "workspace.evolution"},
	)); database.CodeOf(err) != database.CodeInternal {
		t.Fatalf("poisoned preflight = %v", err)
	}
	if _, err := poisonedHandler.Handle(t.Context(), evolutionRequest(
		t, evolutionOpLoadRecords, evolutionBrokerRequest{StoreID: "workspace.evolution"},
	)); database.CodeOf(err) != database.CodeInternal {
		t.Fatalf("poisoned operation = %v", err)
	}
	closed := &BrokerHandler{stores: map[database.StoreID]*evolutionBrokerStore{}, closed: true}
	if _, err := closed.Handle(t.Context(), evolutionRequest(
		t, evolutionOpLoadRecords, evolutionBrokerRequest{},
	)); database.CodeOf(err) != database.CodeUnavailable {
		t.Fatalf("closed handler = %v", err)
	}
	if err := closed.Close(); err != nil {
		t.Fatal(err)
	}
	if err := (*BrokerHandler)(nil).Close(); err != nil {
		t.Fatalf("nil handler close = %v", err)
	}
}

func TestEvolutionBrokerErrorPathAndMigrationContracts(t *testing.T) {
	for _, item := range []struct {
		err  error
		code database.ErrorCode
	}{
		{nil, ""},
		{database.NewError(database.CodeUnauthorized, "denied"), database.CodeUnauthorized},
		{context.Canceled, database.CodeDeadline},
		{os.ErrNotExist, database.CodeNotFound},
		{errors.New("changed during update"), database.CodeConflict},
		{errors.New("opaque"), database.CodeInternal},
	} {
		mapped := mapEvolutionBrokerError(item.err)
		if item.err == nil {
			if mapped != nil {
				t.Errorf("nil mapping = %v", mapped)
			}
			continue
		}
		if database.CodeOf(mapped) != item.code {
			t.Errorf("map(%v) = %v", item.err, mapped)
		}
	}
	if decodeEvolutionBrokerError(nil) != nil {
		t.Fatal("nil decode changed nil")
	}
	for _, item := range []struct {
		err  error
		want error
	}{
		{database.NewError(database.CodeOutcomeUnknown, "unknown"), nil},
		{database.NewError(database.CodeNotFound, "evolution_not_found"), os.ErrNotExist},
		{database.NewError(database.CodeConflict, "evolution_conflict"), errors.New("changed during update")},
		{database.NewError(database.CodeDeadline, "deadline"), context.DeadlineExceeded},
	} {
		got := decodeEvolutionBrokerError(item.err)
		if item.want != nil && !errors.Is(got, item.want) && !strings.Contains(got.Error(), item.want.Error()) {
			t.Errorf("decode(%v) = %v", item.err, got)
		}
	}

	if _, err := NewBrokerHandler(t.TempDir(), nil); database.CodeOf(err) != database.CodeInvalid {
		t.Fatalf("nil broker config = %v", err)
	}
	restoreAuthority := database.SuspendProviderTestAuthority()
	allowUnfencedEvolutionProviderForTests.Store(false)
	_, unauthorizedErr := NewBrokerHandler(t.TempDir(), config.DefaultConfig())
	allowUnfencedEvolutionProviderForTests.Store(true)
	restoreAuthority()
	if database.CodeOf(unauthorizedErr) != database.CodeUnauthorized {
		t.Fatalf("unfenced broker = %v", unauthorizedErr)
	}
	if _, err := canonicalEvolutionPath(""); err == nil {
		t.Fatal("empty evolution path was accepted")
	}
	home := t.TempDir()
	if resolved, err := resolveEvolutionWorkspace(home, "relative"); err != nil || !filepath.IsAbs(resolved) {
		t.Fatalf("relative workspace = %q, %v", resolved, err)
	}
	if resolved, err := resolveEvolutionWorkspace(home, "~/workspace"); err != nil || !filepath.IsAbs(resolved) {
		t.Fatalf("home workspace = %q, %v", resolved, err)
	}
	t.Setenv("HOME", "")
	if _, err := resolveEvolutionWorkspace(home, "~"); err == nil {
		t.Fatal("home-less workspace was accepted")
	}

	databasePath := filepath.Join(home, "evolution.db")
	if err := RunOfflineDatabaseMigration(t.Context(), databasePath); database.CodeOf(err) != database.CodeConflict {
		t.Fatalf("unfenced migration = %v", err)
	}
	fence, err := database.AcquireMigrationFence(home)
	if err != nil {
		t.Fatal(err)
	}
	if err := RunOfflineDatabaseMigration(t.Context(), databasePath); err != nil {
		t.Fatal(err)
	}
	if err := fence.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestEvolutionBrokerStoreResolutionContracts(t *testing.T) {
	fixture := newEvolutionBrokerFixture(t)
	previous := database.RuntimeClient()
	database.InstallProcessClient(fixture.client)
	t.Cleanup(func() { database.InstallProcessClient(previous) })
	t.Setenv(config.EnvHome, fixture.home)
	t.Setenv(config.EnvConfig, "")
	if storeID, err := resolveEvolutionBrokerStoreID(
		NewPaths(fixture.primary, ""),
	); err != nil ||
		storeID != "workspace.evolution" {
		t.Fatalf("primary broker StoreID = %q, %v", storeID, err)
	}
	if _, err := resolveEvolutionBrokerStoreID(
		NewPaths(t.TempDir(), ""),
	); database.CodeOf(
		err,
	) != database.CodeUnsupported {
		t.Fatalf("unknown broker store = %v", err)
	}
	if _, err := resolveEvolutionBrokerStoreID(
		Paths{Workspace: "bad\x00workspace"},
	); database.CodeOf(
		err,
	) != database.CodeInvalid {
		t.Fatalf("invalid broker workspace = %v", err)
	}
	malformed := filepath.Join(fixture.home, "malformed.json")
	if err := os.WriteFile(malformed, []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(config.EnvConfig, malformed)
	if _, err := resolveEvolutionBrokerStoreID(
		NewPaths(fixture.primary, ""),
	); database.CodeOf(
		err,
	) != database.CodeUnavailable {
		t.Fatalf("malformed broker config = %v", err)
	}

	profile := SkillProfile{
		SkillName: "weather", WorkspaceID: fixture.primary,
		Status: SkillStatusActive, Origin: "manual", HumanSummary: "Weather",
	}
	if err := fixture.primaryStore.SaveProfile(profile); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.handler.Handle(t.Context(), evolutionRequest(
		t, evolutionOpSaveProfile,
		evolutionBrokerRequest{
			StoreID: fixture.primaryStore.storeID, Profile: profile,
			WorkspaceID: fixture.primary, SkillName: profile.SkillName,
			ExpectedDigest: "absent", ExpectedExists: false, CAS: true,
		},
	)); database.CodeOf(err) != database.CodeConflict {
		t.Fatalf("stale profile CAS = %v", err)
	}
	if _, err := fixture.handler.Handle(t.Context(), evolutionRequest(
		t, evolutionOpSaveRecords,
		evolutionBrokerRequest{
			StoreID: fixture.primaryStore.storeID, Class: "invalid", Records: []LearningRecord{{}},
		},
	)); database.CodeOf(err) != database.CodeInternal {
		t.Fatalf("invalid record replacement = %v", err)
	}
}

func TestEvolutionHeuristicLabelJudgeAndReadableBoundaryContracts(t *testing.T) {
	if label := heuristicClusterLabel(LearningRecord{Summary: "Fix API timeout 123"}); label != "fix-api-timeout" {
		t.Fatalf("ASCII label = %q", label)
	}
	unicodeRecord := LearningRecord{Summary: "天气 查询"}
	unicodeLabel := heuristicClusterLabel(unicodeRecord)
	if !strings.HasPrefix(unicodeLabel, "task-") || heuristicClusterKey(unicodeRecord) == "" {
		t.Fatalf("Unicode label/key = %q/%q", unicodeLabel, heuristicClusterKey(unicodeRecord))
	}
	if heuristicClusterLabel(LearningRecord{}) != "" || heuristicClusterKey(LearningRecord{}) != "" {
		t.Fatal("empty task produced a cluster identity")
	}
	if label := heuristicClusterLabelForGroup("ascii:weather", nil); label != "weather" {
		t.Fatalf("key label = %q", label)
	}
	if label := heuristicClusterLabelForGroup("", []LearningRecord{{Summary: "Review code"}}); label != "review-code" {
		t.Fatalf("group label = %q", label)
	}
	if summary := heuristicClusterSummary(
		"fallback",
		[]LearningRecord{{}, {Summary: " Concrete "}},
	); summary != "Concrete" {
		t.Fatalf("cluster summary = %q", summary)
	}
	if labelSummary("") != "Learned task pattern." || labelSummary("weather-check") != "Weather check." {
		t.Fatal("label summary changed")
	}

	judge := &HeuristicSuccessJudge{}
	success := true
	for _, record := range []LearningRecord{
		{},
		{Success: &success},
		{Success: &success, Summary: "done", SessionKey: "heartbeat", FinalOutput: "done"},
		{Success: &success, Summary: "done", FinalOutput: "HEARTBEAT_OK"},
		{Success: &success, Summary: "done"},
	} {
		decision, err := judge.JudgeTaskRecord(t.Context(), record)
		if err != nil || decision.Success {
			t.Fatalf("rejected decision = %#v, %v", decision, err)
		}
	}
	decision, err := judge.JudgeTaskRecord(t.Context(), LearningRecord{
		Success: &success, Summary: "done", FinalOutput: "completed",
	})
	if err != nil || !decision.Success {
		t.Fatalf("successful decision = %#v, %v", decision, err)
	}
	if decision, err := (*LLMTaskSuccessJudge)(
		nil,
	).JudgeTaskRecord(t.Context(), LearningRecord{}); err != nil ||
		decision.Success {
		t.Fatalf("nil LLM judge = %#v, %v", decision, err)
	}
	llmJudge := NewLLMTaskSuccessJudge(nil, "", nil)
	if decision, err := llmJudge.JudgeTaskRecord(t.Context(), LearningRecord{}); err != nil || decision.Success {
		t.Fatalf("fallback LLM judge = %#v, %v", decision, err)
	}
	prompt := buildTaskSuccessJudgePrompt(LearningRecord{Summary: "done", FinalOutput: "result"})
	if !strings.Contains(prompt, "Summary: done") || !strings.Contains(prompt, "Final output: result") {
		t.Fatalf("judge prompt = %q", prompt)
	}

	if sentenceFragment("") != "complete the documented workflow" || sentenceFragment("Review code") != "review code" {
		t.Fatal("sentence fragment changed")
	}
	if got := trimAtReadableBoundary("first sentence. second sentence", 16); !strings.HasSuffix(got, "...") {
		t.Fatalf("readable trim = %q", got)
	}
	if got := trimAtReadableBoundary("short", 20); got != "short" {
		t.Fatalf("short trim = %q", got)
	}
}

//nolint:govet // Preview setup assertions intentionally keep filesystem errors local.
func TestEvolutionDraftPreviewCreateReplaceAndDiffContracts(t *testing.T) {
	workspace := t.TempDir()
	created, err := BuildDraftPreview(workspace, SkillDraft{
		TargetSkillName: "weather", ChangeKind: ChangeKindCreate,
		BodyOrPatch: "# Weather\n\nUse forecasts.",
	})
	if err != nil || created.CurrentBody != "" || !strings.Contains(created.DiffPreview, "+# Weather") {
		t.Fatalf("create preview = %#v, %v", created, err)
	}
	skillDir := filepath.Join(workspace, "skills", "weather")
	if err := os.MkdirAll(skillDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte("# Weather\n\nOld."), 0o600); err != nil {
		t.Fatal(err)
	}
	replaced, err := BuildDraftPreview(workspace, SkillDraft{
		TargetSkillName: "weather", ChangeKind: ChangeKindReplace,
		BodyOrPatch: "# Weather\n\nNew.",
	})
	if err != nil || !strings.Contains(replaced.DiffPreview, "-Old.") ||
		!strings.Contains(replaced.DiffPreview, "+New.") {
		t.Fatalf("replace preview = %#v, %v", replaced, err)
	}
	if noChange := buildLineDiffPreview("same\n", "same\n"); noChange != "(no content change)" {
		t.Fatalf("no-change preview = %q", noChange)
	}
	if previewMinInt(1, 2) != 1 || previewMinInt(2, 1) != 1 ||
		previewMaxInt(1, 2) != 2 || previewMaxInt(2, 1) != 2 {
		t.Fatal("preview bounds changed")
	}
}

func evolutionRequest(t *testing.T, operation string, payload any) database.Request {
	t.Helper()
	raw, err := database.MarshalCanonical(payload)
	if err != nil {
		t.Fatal(err)
	}
	return database.Request{
		Domain: BrokerDomain, Version: BrokerVersion, Operation: operation,
		Payload: json.RawMessage(raw),
	}
}
