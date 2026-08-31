package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/repoaudit"
	"github.com/sipeed/picoclaw/pkg/workflows"
)

type repositoryReviewValidationGitFixture struct {
	directory string
	base      string
	fix       string
	rename    string
}

func newRepositoryReviewValidationGitFixture(t *testing.T) repositoryReviewValidationGitFixture {
	t.Helper()
	directory := t.TempDir()
	git := func(arguments ...string) string {
		t.Helper()
		command := exec.Command("git", arguments...)
		command.Dir = directory
		command.Env = append(os.Environ(),
			"GIT_AUTHOR_DATE=2026-08-20T12:00:00Z",
			"GIT_COMMITTER_DATE=2026-08-20T12:00:00Z",
		)
		output, err := command.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", arguments, err, output)
		}
		return strings.TrimSpace(string(output))
	}
	git("init", "-b", "main")
	git("config", "user.email", "review-validation@example.test")
	git("config", "user.name", "Repository Validation")
	if err := os.MkdirAll(filepath.Join(directory, "src"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		filepath.Join(directory, "src", "waiter.go"),
		[]byte("package src\n\nfunc add_waiter() bool { return false }\n"), 0o600,
	); err != nil {
		t.Fatal(err)
	}
	git("add", "src/waiter.go")
	git("commit", "-m", "add moved waiter predicate")
	base := git("rev-parse", "HEAD")

	if err := os.WriteFile(
		filepath.Join(directory, "src", "waiter.go"),
		[]byte("package src\n\nfunc add_waiter() bool { return true }\n"), 0o600,
	); err != nil {
		t.Fatal(err)
	}
	git("add", "src/waiter.go")
	git("commit", "-m", "fix waiter owner after condition variable move", "-m", "preserve requeue invariant")
	fix := git("rev-parse", "HEAD")
	git("tag", "nightly")
	git("tag", "v1.2.3")

	git("mv", "src/waiter.go", "src/predicate_waiter.go")
	git("commit", "-m", "rename predicate waiter implementation")
	rename := git("rev-parse", "HEAD")
	return repositoryReviewValidationGitFixture{
		directory: directory, base: base, fix: fix, rename: rename,
	}
}

func repositoryReviewValidationFindingForTest(
	fixture repositoryReviewValidationGitFixture,
) repoaudit.RepositoryFinding {
	return repoaudit.RepositoryFinding{
		ID: "rrf_validation", CanonicalTitle: "waiter remains blocked after move",
		MatchHints: repoaudit.MatchHints{
			Component: "core scheduling", Operation: "requeue waiter after move",
			FailureMode: "waiter remains on moved owner", Trigger: "move then predicate wakeup",
			ViolatedInvariant: "waiter requeues on current owner",
			ObservableOutcome: "coroutine blocked indefinitely",
			RelatedSymbols:    []string{"add_waiter", "predicate_awaiter::signal"},
			SourceAnchors:     []string{"add_waiter", "_waiters"},
		},
		PathSymbolHistory: []repoaudit.RepositoryFindingPathSymbol{
			{
				Path: "src/waiter.go", Symbol: "add_waiter", CommitSHA: fixture.base,
				ObservedAt: time.Date(2026, 8, 20, 12, 0, 0, 0, time.UTC), DefaultBranchVerified: true,
			},
			{
				Path: "src/predicate_waiter.go", Symbol: "add_waiter", CommitSHA: fixture.rename,
				ObservedAt: time.Date(2026, 8, 21, 12, 0, 0, 0, time.UTC),
			},
		},
	}
}

func recordRepositoryReviewControllerFinding(
	t *testing.T,
	store repoaudit.Store,
	fixture repositoryReviewValidationGitFixture,
) (repoaudit.RepositoryState, repoaudit.Finding) {
	t.Helper()
	file := repoaudit.FileRef{
		Path: "src/waiter.go", BlobSHA: strings.Repeat("a", 40), SizeBytes: 64,
		Category: "code", Mode: "100644",
	}
	plan, err := store.Plan(
		t.Context(), fixture.directory, fixture.base, "controller-inventory",
		[]repoaudit.FileRef{file}, false,
	)
	if err != nil {
		t.Fatal(err)
	}
	plan, err = repoaudit.BindPlanBranch(plan, "main", "main", true)
	if err != nil {
		t.Fatal(err)
	}
	line := 3
	result, err := store.Record(t.Context(), repoaudit.RecordRequest{
		Plan: plan, RunID: "controller-run", TargetBranch: "main",
		AdvertisedDefaultBranch: "main", TargetIsDefault: true,
		Observations: []repoaudit.Observation{{
			Model: "cheap", Reviewer: "cheap", ScopeFiles: []repoaudit.FileRef{file},
			Findings: []repoaudit.FindingCandidate{{
				Severity: "high", Title: "waiter remains blocked after move",
				Symbol: "add_waiter", File: file.Path, Line: &line,
				Message:  "The failed wake path retains the old owner.",
				Evidence: "The moved waiter is requeued on the stale queue.",
				Impact:   "The coroutine remains blocked indefinitely.",
				Validation: repoaudit.Validation{
					Status: "confirmed", Summary: "traced owner", Checks: []string{"wake path"},
				},
				MatchHints: repoaudit.MatchHints{
					Component: "core scheduling", Operation: "requeue waiter after move",
					FailureMode: "waiter remains on moved owner", Trigger: "move then failed wake",
					ViolatedInvariant: "waiter requeues on current owner",
					ObservableOutcome: "coroutine remains blocked indefinitely",
					RelatedSymbols:    []string{"add_waiter"}, SourceAnchors: []string{"add_waiter"},
					DistinguishingFacts: []string{"requires a moved owner"},
				},
				FixEffort: repoaudit.FixEffort{
					Quick: repoaudit.FixEffortEstimate{
						LOCMin: 5, LOCMax: 20, Class: "small", Rationale: "Localized containment.",
					},
					Quality: repoaudit.FixEffortEstimate{
						LOCMin: 30, LOCMax: 100, Class: "medium", Rationale: "Ownership spans units.",
					},
				},
			}},
		}},
	})
	if err != nil || len(result.State.Findings) != 1 {
		t.Fatalf("record state=%#v err=%v", result.State, err)
	}
	return result.State, result.State.Findings[0]
}

func TestRepositoryValidationGitEvidenceHelpers(t *testing.T) {
	fixture := newRepositoryReviewValidationGitFixture(t)
	finding := repositoryReviewValidationFindingForTest(fixture)

	paths := repositoryValidationPaths(finding)
	if !slices.Equal(paths, []string{"src/predicate_waiter.go", "src/waiter.go"}) {
		t.Fatalf("paths = %#v", paths)
	}
	baseline, err := repositoryValidationBaseline(t.Context(), fixture.directory, finding)
	if err != nil || baseline != fixture.base {
		t.Fatalf("baseline=%q err=%v", baseline, err)
	}
	records, err := repositoryValidationCommitLog(
		t.Context(), fixture.directory, finding, paths, baseline,
	)
	if err != nil || len(records) < 2 {
		t.Fatalf("records=%#v err=%v", records, err)
	}
	ranked := repositoryRankValidationCommits(finding, records, 1)
	if len(ranked) != 1 || ranked[0].SHA != fixture.fix {
		t.Fatalf("ranked=%#v", ranked)
	}
	if all := repositoryRankValidationCommits(finding, records, 99); len(all) != len(records) {
		t.Fatalf("default-limit rank count=%d records=%d", len(all), len(records))
	}

	source := repositoryValidationCurrentSource(
		t.Context(), fixture.directory, []string{"missing.go", "src/predicate_waiter.go"},
	)
	if !strings.Contains(source, "predicate_waiter.go") || !strings.Contains(source, "add_waiter") {
		t.Fatalf("current source=%q", source)
	}
	if tag := repositoryFirstSemanticTag(t.Context(), fixture.directory, fixture.fix); tag != "v1.2.3" {
		t.Fatalf("semantic tag=%q", tag)
	}
	if tag := repositoryFirstSemanticTag(t.Context(), fixture.directory, strings.Repeat("f", 40)); tag != "" {
		t.Fatalf("missing semantic tag=%q", tag)
	}

	frozen, err := repositoryValidationFrozenCommits(
		t.Context(), fixture.directory, []string{strings.ToUpper(fixture.fix)},
	)
	if err != nil || len(frozen) != 1 || frozen[0].SHA != fixture.fix || frozen[0].Time.IsZero() {
		t.Fatalf("frozen=%#v err=%v", frozen, err)
	}
	if _, err := repositoryValidationFrozenCommits(
		t.Context(), fixture.directory, []string{"not-a-commit"},
	); err == nil {
		t.Fatal("invalid frozen commit was accepted")
	}
	if _, err := repositoryValidationFrozenCommits(
		t.Context(), fixture.directory, []string{strings.Repeat("f", 40)},
	); err == nil {
		t.Fatal("missing frozen commit was accepted")
	}

	if ancestor, err := repositoryReviewCommitIsAncestor(
		t.Context(), fixture.directory, fixture.base, fixture.rename,
	); err != nil || !ancestor {
		t.Fatalf("base ancestor=%v err=%v", ancestor, err)
	}
	if ancestor, err := repositoryReviewCommitIsAncestor(
		t.Context(), fixture.directory, fixture.rename, fixture.base,
	); err != nil || ancestor {
		t.Fatalf("reverse ancestor=%v err=%v", ancestor, err)
	}
	if _, err := repositoryReviewCommitIsAncestor(
		t.Context(), fixture.directory, "invalid", fixture.base,
	); err == nil {
		t.Fatal("invalid ancestry command succeeded")
	}
}

func TestRepositoryValidationEvidenceProviderBuildsAndReplaysBoundedEvidence(t *testing.T) {
	fixture := newRepositoryReviewValidationGitFixture(t)
	finding := repositoryReviewValidationFindingForTest(fixture)
	handler, _, _ := newRepositoryReviewAutomationTestHandler(t)
	t.Cleanup(handler.Shutdown)
	cfg, err := config.LoadConfig(handler.configPath)
	if err != nil {
		t.Fatal(err)
	}
	cfg.GitWorkspaces.RootDir = t.TempDir()
	controller := newRepositoryReviewController(handler)
	controller.leasedConfig = cfg
	metadata := &sync.Map{}
	provider := controller.repositoryValidationEvidenceProvider(
		repoaudit.RepositoryReviewAutomation{Repository: fixture.directory}, metadata,
	)
	evidence, err := provider(t.Context(), finding, nil)
	if err != nil || len(evidence) < 2 || len(evidence) > 8 {
		t.Fatalf("evidence count=%d evidence=%#v err=%v", len(evidence), evidence, err)
	}
	seenFix := false
	for _, record := range evidence {
		if record.CommitSHA == fixture.fix {
			seenFix = true
			if !strings.Contains(record.Diff, "return true") || record.CurrentSource == "" {
				t.Fatalf("fix evidence=%#v", record)
			}
		}
		value, ok := metadata.Load(record.CommitSHA)
		if !ok || !value.(repositoryValidationGitMetadata).reachable {
			t.Fatalf("metadata for %q=%#v ok=%v", record.CommitSHA, value, ok)
		}
	}
	if !seenFix {
		t.Fatalf("fix commit absent from evidence: %#v", evidence)
	}

	replayed, err := provider(t.Context(), finding, []string{fixture.fix})
	if err != nil || len(replayed) != 1 || replayed[0].CommitSHA != fixture.fix {
		t.Fatalf("replayed=%#v err=%v", replayed, err)
	}
	if _, err := provider(t.Context(), finding, []string{"invalid"}); err == nil {
		t.Fatal("provider accepted invalid frozen commit")
	}
}

//nolint:govet // Sequential Git probes intentionally reuse short-lived error names.
func TestRepositoryMappingRenameAndDefaultBranchVerification(t *testing.T) {
	fixture := newRepositoryReviewValidationGitFixture(t)
	cfg := config.DefaultConfig()
	cfg.Agents.Defaults.Workspace = t.TempDir()
	cfg.GitWorkspaces.RootDir = t.TempDir()
	automation := repoaudit.RepositoryReviewAutomation{
		ID: "rra_git_mapping", Repository: fixture.directory,
	}
	state := repoaudit.RepositoryState{
		LastCommitSHA: fixture.rename,
		RepositoryFindings: []repoaudit.RepositoryFinding{{
			FoundCommits: []string{fixture.fix, fixture.rename},
		}},
	}
	equivalent := repositoryMappingRenameEquivalent(t.Context(), cfg, automation, state)
	if !equivalent("src/waiter.go", "src/predicate_waiter.go") ||
		!equivalent("src/predicate_waiter.go", "src/waiter.go") ||
		equivalent("src/unrelated.go", "src/predicate_waiter.go") {
		t.Fatal("git rename equivalence did not preserve the detected pair")
	}

	state.Findings = []repoaudit.Finding{{
		CommitSHA: fixture.base, TargetIsDefault: true,
	}}
	verify, verifyRegression, release := repositoryMappingDefaultVerifier(
		t.Context(), cfg, automation, state,
	)
	defer release()
	ok, err := verify(t.Context(), repoaudit.Finding{
		CommitSHA: fixture.base, TargetIsDefault: true,
	})
	if err != nil || !ok {
		t.Fatalf("default verification=%v err=%v", ok, err)
	}
	ok, err = verify(t.Context(), repoaudit.Finding{
		CommitSHA: fixture.base, TargetBranch: "feature", AdvertisedDefaultBranch: "main",
	})
	if err != nil || ok {
		t.Fatalf("non-default verification=%v err=%v", ok, err)
	}
	if _, err := verify(t.Context(), repoaudit.Finding{CommitSHA: "invalid", TargetIsDefault: true}); err == nil {
		t.Fatal("invalid default commit was accepted")
	}
	ok, err = verifyRegression(t.Context(),
		repoaudit.Finding{CommitSHA: fixture.rename},
		repoaudit.RepositoryFinding{FixCommitSHA: fixture.fix},
	)
	if err != nil || !ok {
		t.Fatalf("regression verification=%v err=%v", ok, err)
	}
	ok, err = verifyRegression(t.Context(),
		repoaudit.Finding{CommitSHA: "invalid"}, repoaudit.RepositoryFinding{FixCommitSHA: fixture.fix},
	)
	if err != nil || ok {
		t.Fatalf("invalid regression verification=%v err=%v", ok, err)
	}
}

func TestRepositoryValidationParsingRankingAndBounds(t *testing.T) {
	validSHA := strings.Repeat("a", 40)
	output := []byte("garbage\x1e" + validSHA + "\x1f2026-08-22T14:30:00Z\x1ffix waiter\x1fbody" +
		"\x1e" + strings.Repeat("b", 40) + "\x1fnot-a-time\x1fbad\x1fbody" +
		"\x1eshort\x1f2026-08-22T14:30:00Z\x1fbad\x1fbody")
	records := repositoryParseValidationLog(output)
	if len(records) != 1 || records[0].SHA != validSHA || records[0].Time.Location() != time.UTC ||
		!strings.Contains(records[0].Message, "body") {
		t.Fatalf("parsed records=%#v", records)
	}

	unique := repositoryValidationUniqueStrings(
		[]string{"", " waiter ", "waiter", "owner", "third"}, 2,
	)
	if !slices.Equal(unique, []string{"waiter", "owner"}) {
		t.Fatalf("unique=%#v", unique)
	}
	finding := repoaudit.RepositoryFinding{CanonicalTitle: "specific fix token"}
	ranked := repositoryRankValidationCommits(finding, []repositoryValidationCommitRecord{
		{SHA: strings.Repeat("c", 40), Message: "unrelated cleanup"},
		{SHA: strings.Repeat("d", 40), Message: "specific fix token"},
	}, 1)
	if len(ranked) != 1 || ranked[0].SHA != strings.Repeat("d", 40) {
		t.Fatalf("ranked=%#v", ranked)
	}
	if ranked := repositoryRankValidationCommits(finding, nil, -1); len(ranked) != 0 {
		t.Fatalf("empty ranked=%#v", ranked)
	}

	pathHistory := make([]repoaudit.RepositoryFindingPathSymbol, 42)
	for index := range 40 {
		pathHistory[index].Path = "path/" + string(rune('a'+index%26)) + string(rune('0'+index%10))
	}
	pathHistory[40] = repoaudit.RepositoryFindingPathSymbol{Path: ""}
	pathHistory[41] = repoaudit.RepositoryFindingPathSymbol{Path: pathHistory[39].Path}
	if paths := repositoryValidationPaths(
		repoaudit.RepositoryFinding{PathSymbolHistory: pathHistory},
	); len(
		paths,
	) > 32 {
		t.Fatalf("paths exceeded bound: %d", len(paths))
	}

	noBaseline := repoaudit.RepositoryFinding{PathSymbolHistory: []repoaudit.RepositoryFindingPathSymbol{
		{CommitSHA: validSHA},
		{CommitSHA: "invalid", DefaultBranchVerified: true},
	}}
	if _, err := repositoryValidationBaseline(t.Context(), t.TempDir(), noBaseline); err == nil {
		t.Fatal("unverified baseline was accepted")
	}
}

func TestRepositoryValidationCurrentSourceEnforcesAggregateBound(t *testing.T) {
	fixture := newRepositoryReviewValidationGitFixture(t)
	large := bytes.Repeat([]byte("z"), 40<<10)
	paths := make([]string, 0, 5)
	for index := 0; index < 5; index++ {
		name := "large" + string(rune('a'+index)) + ".txt"
		if err := os.WriteFile(filepath.Join(fixture.directory, name), large, 0o600); err != nil {
			t.Fatal(err)
		}
		paths = append(paths, name)
	}
	command := exec.Command("git", "add", ".")
	command.Dir = fixture.directory
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("git add: %v\n%s", err, output)
	}
	command = exec.Command("git", "commit", "-m", "add bounded current source")
	command.Dir = fixture.directory
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("git commit: %v\n%s", err, output)
	}
	source := repositoryValidationCurrentSource(t.Context(), fixture.directory, paths)
	if len(source) > 128<<10 || len(source) < 100<<10 {
		t.Fatalf("current-source length=%d", len(source))
	}
}

func TestRepositoryMappingHelpersAndSnapshots(t *testing.T) {
	if repositoryReviewMappingPromptRevision != "repository-finding-matcher-v2" {
		t.Fatalf("mapping prompt revision=%q", repositoryReviewMappingPromptRevision)
	}
	if repositoryStateHasPendingMapping(repoaudit.RepositoryState{}) {
		t.Fatal("empty state reported pending mapping")
	}
	if !repositoryStateHasPendingMapping(repoaudit.RepositoryState{MappingJobs: []repoaudit.RepositoryMappingJob{
		{State: repoaudit.RepositoryMappingCompleted},
		{State: repoaudit.RepositoryMappingPending},
	}}) {
		t.Fatal("pending mapping was not detected")
	}
	if repositoryStateHasPendingMapping(repoaudit.RepositoryState{MappingJobs: []repoaudit.RepositoryMappingJob{{
		State:    repoaudit.RepositoryMappingPending,
		Attempts: repoaudit.RepositoryRunFindingStatusAttemptLimit,
	}}}) {
		t.Fatal("capped run finding status was reported as processable")
	}
	if repositoryStateHasPendingValidation(repoaudit.RepositoryState{}) {
		t.Fatal("empty state reported pending validation")
	}
	if !repositoryStateHasPendingValidation(
		repoaudit.RepositoryState{ValidationJobs: []repoaudit.RepositoryValidationJob{
			{State: repoaudit.RepositoryValidationPending},
		}},
	) {
		t.Fatal("pending validation was not detected")
	}

	pairs := map[string]struct{}{"old.go\x00new.go": {}}
	if !hasRenamePair(pairs, "old.go", "new.go") || hasRenamePair(pairs, "new.go", "old.go") {
		t.Fatalf("rename pair lookup failed: %#v", pairs)
	}
	exact := repositoryMappingRenameEquivalent(
		t.Context(), nil, repoaudit.RepositoryReviewAutomation{}, repoaudit.RepositoryState{},
	)
	if !exact(" same.go ", "same.go") || exact("", "") || exact("old.go", "new.go") {
		t.Fatal("nil-config path equivalence was not exact-only")
	}

	cfg := config.DefaultConfig()
	cfg.Agents.Defaults.AccountRef = "default-account"
	cfg.Agents.Defaults.ModelName = "default-model"
	fallback := repositoryFallbackAutomation(cfg, repoaudit.RepositoryState{
		ID: "rrp_state", Repository: "owner/repository",
	})
	if fallback.ID != "legacy_state" || fallback.AccountRef != "default-account" ||
		!slices.Equal(fallback.ReviewerModels, []string{"default-model"}) ||
		fallback.IssueWriterModel != "default-model" {
		t.Fatalf("fallback=%#v", fallback)
	}
	if fallback := repositoryFallbackAutomation(
		nil,
		repoaudit.RepositoryState{ID: "rrp_x"},
	); fallback.ID != "legacy_x" ||
		fallback.AccountRef != "" {
		t.Fatalf("nil-config fallback=%#v", fallback)
	}

	handler, _, _ := newRepositoryReviewAutomationTestHandler(t)
	t.Cleanup(handler.Shutdown)
	store, err := handler.repositoryReviewStore()
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := config.LoadConfig(handler.configPath)
	if err != nil {
		t.Fatal(err)
	}
	direct, err := repositoryMappingSnapshot(t.Context(), store, loaded, repoaudit.RepositoryReviewAutomation{
		ReviewerModels: []string{"reviewer"}, AccountRef: "account",
	})
	if err != nil || direct.Model != "reviewer" || direct.Account == "" ||
		direct.Prompt != repositoryReviewMappingPromptRevision {
		t.Fatalf("direct snapshot=%#v err=%v", direct, err)
	}
	missing, err := repositoryMappingSnapshot(t.Context(), store, loaded, repoaudit.RepositoryReviewAutomation{
		ProfileID: "rrpf_missing",
	})
	if err == nil || missing != (repoaudit.RepositoryMappingModelSnapshot{}) {
		t.Fatalf("missing profile snapshot=%#v err=%v", missing, err)
	}

	if _, err := runRepositoryMappingAdjudication(
		t.Context(), nil, repoaudit.RepositoryMappingModelSnapshot{}, repoaudit.RepositoryMappingAIRequest{},
	); err == nil {
		t.Fatal("nil mapping adjudicator was accepted")
	}
	if _, err := runRepositoryValidationAdjudication(
		t.Context(), nil, repoaudit.RepositoryMappingModelSnapshot{}, repoaudit.RepositoryFinding{}, nil,
	); err == nil {
		t.Fatal("nil validation adjudicator was accepted")
	}

	if schema := repositoryReviewMappingSchema(); schema["type"] != "object" {
		t.Fatalf("mapping schema=%#v", schema)
	}
	if schema := repositoryReviewValidationSchema(); schema["type"] != "object" {
		t.Fatalf("validation schema=%#v", schema)
	}
}

func TestWakeRepositoryRunFindingStatusCoverage(t *testing.T) {
	var nilController *repositoryReviewController
	nilController.wakeRepositoryRunFindingStatus()

	canceledContext, cancelCanceled := context.WithCancel(context.Background())
	cancelCanceled()
	(&repositoryReviewController{
		ctx:          canceledContext,
		leasedConfig: &config.Config{},
	}).wakeRepositoryRunFindingStatus()
	(&repositoryReviewController{
		ctx: context.Background(),
	}).wakeRepositoryRunFindingStatus()

	blockedRoot := filepath.Join(t.TempDir(), "blocked-store")
	if err := os.WriteFile(blockedRoot, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	(&repositoryReviewController{
		ctx:          context.Background(),
		leasedConfig: &config.Config{},
		leasedStore:  repoaudit.NewStore(blockedRoot),
	}).wakeRepositoryRunFindingStatus()

	controllerContext, cancelController := context.WithCancel(context.Background())
	controller := &repositoryReviewController{
		ctx:          controllerContext,
		cancel:       cancelController,
		leasedConfig: &config.Config{},
		leasedStore:  repoaudit.NewStore(t.TempDir()),
		active:       make(map[string]*repositoryReviewActiveRun),
	}
	controller.wakeRepositoryRunFindingStatus()
	controller.wg.Wait()
	cancelController()
}

func TestRepositoryMappingAndValidationControllersUnavailableAndCanceled(t *testing.T) {
	var nilController *repositoryReviewController
	if err := nilController.processRepositoryFindingMappings(t.Context(), nil); err == nil {
		t.Fatal("nil mapping controller succeeded")
	}
	if err := nilController.processRepositoryFindingValidations(t.Context(), nil); err == nil {
		t.Fatal("nil validation controller succeeded")
	}
	nilController.startRepositoryFindingMapping(nil)
	nilController.startRepositoryFindingValidation(nil)

	handler, _, _ := newRepositoryReviewAutomationTestHandler(t)
	t.Cleanup(handler.Shutdown)
	store, err := handler.repositoryReviewStore()
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := config.LoadConfig(handler.configPath)
	if err != nil {
		t.Fatal(err)
	}
	controller := newRepositoryReviewController(handler)
	controller.leasedConfig = cfg
	controller.leasedStore = store
	if err := controller.processRepositoryFindingMappings(t.Context(), nil); err != nil {
		t.Fatalf("empty mapping controller: %v", err)
	}
	if err := controller.processRepositoryFindingValidations(t.Context(), nil); err != nil {
		t.Fatalf("empty validation controller: %v", err)
	}
	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	if err := controller.processRepositoryFindingMappings(
		canceled,
		nil,
	); !errors.Is(err, context.Canceled) &&
		err != nil {
		t.Fatalf("canceled mapping controller=%v", err)
	}
	if err := controller.processRepositoryFindingValidations(
		canceled,
		nil,
	); !errors.Is(err, context.Canceled) &&
		err != nil {
		t.Fatalf("canceled validation controller=%v", err)
	}
}

//nolint:govet // Sequential controller probes intentionally reuse short-lived error names.
func TestRepositoryMappingAndValidationControllersProcessRealQueues(t *testing.T) {
	fixture := newRepositoryReviewValidationGitFixture(t)
	workspace := t.TempDir()
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer r.Body.Close()
		var request struct {
			Messages []struct {
				Content string `json:"content"`
			} `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Errorf("decode provider request: %v", err)
		}
		content := `{"outcome":"confirmed","selected_commit_sha":"` + fixture.fix +
			`","summary":"The supplied fix restores the waiter owner invariant."}`
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{{
				"message": map[string]any{"content": content}, "finish_reason": "stop",
			}},
		})
	}))
	t.Cleanup(provider.Close)

	configPath := filepath.Join(t.TempDir(), "config.json")
	cfg := config.DefaultConfig()
	cfg.Agents.Defaults.Workspace = workspace
	cfg.Agents.Defaults.ModelName = "cheap"
	cfg.Agents.Defaults.AccountRef = "api"
	cfg.GitWorkspaces.RootDir = t.TempDir()
	cfg.ModelAliases = []config.ModelAliasConfig{{Name: "cheap", Model: "openai/test"}}
	cfg.ModelList = []*config.ModelConfig{{
		ModelName: "api", Provider: "openai", Model: "openai/test", Enabled: true,
		APIBase: provider.URL, APIKeys: config.SimpleSecureStrings("test-key"),
	}}
	if err := config.SaveConfig(configPath, cfg); err != nil {
		t.Fatal(err)
	}
	handler := NewHandler(configPath)
	t.Cleanup(handler.Shutdown)
	store := repoaudit.NewStore(workspace)
	state, _ := recordRepositoryReviewControllerFinding(t, store, fixture)
	automation := repoaudit.RepositoryReviewAutomation{
		ID: "rra_controller_queue", Repository: fixture.directory,
		ReviewerModels: []string{"cheap"}, IssueWriterModel: "cheap", AccountRef: "api",
		RunIDs: []string{"controller-run"},
	}
	controller := newRepositoryReviewController(handler)
	controller.leasedConfig = cfg
	controller.leasedStore = store
	if selected, found := repositoryAutomationForLedger(
		store, []repoaudit.RepositoryReviewAutomation{automation}, state,
	); !found || selected.ID != automation.ID {
		t.Fatalf("automation selection=%#v found=%v", selected, found)
	}
	if _, found := repositoryAutomationForLedger(
		store, []repoaudit.RepositoryReviewAutomation{{Repository: "other/repo"}}, state,
	); found {
		t.Fatal("unrelated automation selected")
	}
	if err := controller.processRepositoryFindingMappings(
		t.Context(), []repoaudit.RepositoryReviewAutomation{automation},
	); err != nil {
		t.Fatalf("process mappings: %v", err)
	}
	state, found, err := store.Get(fixture.directory)
	if err != nil || !found || len(state.RepositoryFindings) != 1 ||
		state.MappingJobs[0].State != repoaudit.RepositoryMappingCompleted {
		t.Fatalf("mapped state=%#v found=%v err=%v", state, found, err)
	}
	aggregate := state.RepositoryFindings[0]
	snapshot := repoaudit.RepositoryMappingModelSnapshot{
		Model: "cheap", Account: "api", Prompt: repositoryReviewMappingPromptRevision,
	}
	_, jobs, err := store.ReserveValidationJobs(
		state.Repository, []string{aggregate.ID}, snapshot,
	)
	if err != nil || len(jobs) != 1 {
		t.Fatalf("reserve jobs=%#v err=%v", jobs, err)
	}
	if err := controller.processRepositoryFindingValidations(
		t.Context(), []repoaudit.RepositoryReviewAutomation{automation},
	); err != nil {
		t.Fatalf("process validations: %v", err)
	}
	state, found, err = store.Get(fixture.directory)
	if err != nil || !found || state.ValidationJobs[0].State != repoaudit.RepositoryValidationConfirmed ||
		state.RepositoryFindings[0].FixCommitSHA != fixture.fix ||
		state.RepositoryFindings[0].FirstContainingTag != "v1.2.3" {
		t.Fatalf("validated state=%#v found=%v err=%v", state, found, err)
	}
}

func newRepositoryReviewAIAdjudicationHandler(
	t *testing.T,
	status int,
	content string,
) *Handler {
	t.Helper()
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer r.Body.Close()
		if status != http.StatusOK {
			http.Error(w, "provider unavailable", status)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{{
				"message": map[string]any{"content": content}, "finish_reason": "stop",
			}},
		}); err != nil {
			t.Errorf("encode provider response: %v", err)
		}
	}))
	t.Cleanup(provider.Close)
	configPath := filepath.Join(t.TempDir(), "config.json")
	cfg := config.DefaultConfig()
	cfg.Agents.Defaults.Workspace = t.TempDir()
	cfg.Agents.Defaults.ModelName = "cheap"
	cfg.Agents.Defaults.AccountRef = "api"
	cfg.ModelAliases = []config.ModelAliasConfig{{Name: "cheap", Model: "openai/test"}}
	cfg.ModelList = []*config.ModelConfig{{
		ModelName: "api", Provider: "openai", Model: "openai/test", Enabled: true,
		APIBase: provider.URL, APIKeys: config.SimpleSecureStrings("test-key"),
	}}
	if err := config.SaveConfig(configPath, cfg); err != nil {
		t.Fatal(err)
	}
	handler := NewHandler(configPath)
	t.Cleanup(handler.Shutdown)
	return handler
}

func TestRepositoryMappingAdjudicationRunnerSuccessErrorsAndBounds(t *testing.T) {
	snapshot := repoaudit.RepositoryMappingModelSnapshot{Model: "cheap", Account: "api"}
	request := repoaudit.RepositoryMappingAIRequest{
		Finding: repoaudit.Finding{Title: "waiter remains blocked"},
		Candidates: []repoaudit.RepositoryMappingAICandidate{{
			ID: "opaque_1", Finding: repoaudit.RepositoryFinding{CanonicalTitle: "waiter remains blocked"},
		}},
	}
	valid := newRepositoryReviewAIAdjudicationHandler(t, http.StatusOK,
		`{"decision":"same","candidate_id":"opaque_1","confidence":0.96,`+
			`"matching_anchors":["trigger","invariant"],"conflicting_anchors":`+
			`[{"field":"severity","text":"The occurrence is high while the candidate is medium."},`+
			`{"field":"causal_identity","text":"The trigger descriptions use different wake paths."}],`+
			`"explanation":"The supplied causal identity is the same."}`,
	)
	result, err := runRepositoryMappingAdjudication(t.Context(), valid, snapshot, request)
	if err != nil || result.Decision != "same" || result.CandidateID != "opaque_1" ||
		result.Confidence != 0.96 ||
		!slices.Equal(result.ConflictingAnchors, []string{
			"The occurrence is high while the candidate is medium.",
			"The trigger descriptions use different wake paths.",
		}) || !slices.Equal(result.ConflictFields, []string{
		repoaudit.RepositoryMappingConflictFieldSeverity,
		repoaudit.RepositoryMappingConflictFieldCausalIdentity,
	}) {
		t.Fatalf("mapping result=%#v err=%v", result, err)
	}

	providerFailure := newRepositoryReviewAIAdjudicationHandler(
		t, http.StatusServiceUnavailable, "",
	)
	if _, err := runRepositoryMappingAdjudication(
		t.Context(), providerFailure, snapshot, request,
	); err == nil {
		t.Fatal("mapping provider failure was accepted")
	}
	invalidStructured := newRepositoryReviewAIAdjudicationHandler(t, http.StatusOK, `{}`)
	if _, err := runRepositoryMappingAdjudication(
		t.Context(), invalidStructured, snapshot, request,
	); err == nil {
		t.Fatal("invalid mapping structured output was accepted")
	}
	if _, err := runRepositoryMappingAdjudication(
		t.Context(), NewHandler(t.TempDir()), snapshot, request,
	); err == nil {
		t.Fatal("missing mapping configuration was accepted")
	}
	if _, err := runRepositoryMappingAdjudication(
		t.Context(), valid,
		repoaudit.RepositoryMappingModelSnapshot{Model: "missing", Account: "missing"}, request,
	); err == nil {
		t.Fatal("unknown mapping model snapshot was accepted")
	}
	oversized := request
	oversized.Finding.Title = strings.Repeat("x", (1<<20)+1)
	if _, err := runRepositoryMappingAdjudication(
		t.Context(), valid, snapshot, oversized,
	); err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("oversized mapping error=%v", err)
	}

	for _, test := range []struct {
		name      string
		conflicts string
	}{
		{name: "legacy string", conflicts: `["severity differs"]`},
		{name: "missing field", conflicts: `[{"text":"severity differs"}]`},
		{name: "missing text", conflicts: `[{"field":"severity"}]`},
		{name: "extra property", conflicts: `[{"field":"severity","text":"severity differs","label":"severity"}]`},
		{name: "unknown classification", conflicts: `[{"field":"priority","text":"severity differs"}]`},
	} {
		t.Run(test.name, func(t *testing.T) {
			invalid := newRepositoryReviewAIAdjudicationHandler(t, http.StatusOK,
				`{"decision":"same","candidate_id":"opaque_1","confidence":0.96,`+
					`"matching_anchors":[],"conflicting_anchors":`+test.conflicts+`,`+
					`"explanation":"The supplied records match."}`,
			)
			if _, err := runRepositoryMappingAdjudication(
				t.Context(), invalid, snapshot, request,
			); err == nil {
				t.Fatalf("conflicts %s were accepted", test.conflicts)
			}
		})
	}
}

func TestRepositoryMappingSchemaUsesStrictClassifiedConflictObjects(t *testing.T) {
	schema := repositoryReviewMappingSchema()
	properties, ok := schema["properties"].(map[string]any)
	if !ok {
		t.Fatalf("mapping schema properties=%#v", schema["properties"])
	}
	conflicts, ok := properties["conflicting_anchors"].(map[string]any)
	if !ok {
		t.Fatalf("conflicting anchors schema=%#v", properties["conflicting_anchors"])
	}
	items, ok := conflicts["items"].(map[string]any)
	if !ok || items["type"] != "object" || items["additionalProperties"] != false {
		t.Fatalf("conflicting anchor items=%#v", conflicts["items"])
	}
	required, ok := items["required"].([]any)
	if !ok || !slices.Equal(required, []any{"field", "text"}) {
		t.Fatalf("conflicting anchor required=%#v", items["required"])
	}
	itemProperties, ok := items["properties"].(map[string]any)
	if !ok {
		t.Fatalf("conflicting anchor properties=%#v", items["properties"])
	}
	field, ok := itemProperties["field"].(map[string]any)
	if !ok || field["type"] != "string" {
		t.Fatalf("conflicting anchor field schema=%#v", itemProperties["field"])
	}
	fieldEnum, ok := field["enum"].([]any)
	wantEnum := []any{
		repoaudit.RepositoryMappingConflictFieldSeverity,
		repoaudit.RepositoryMappingConflictFieldTitleWording,
		repoaudit.RepositoryMappingConflictFieldFixEffort,
		repoaudit.RepositoryMappingConflictFieldLifecycleStatus,
		repoaudit.RepositoryMappingConflictFieldCausalIdentity,
		repoaudit.RepositoryMappingConflictFieldLocation,
		repoaudit.RepositoryMappingConflictFieldSymbol,
		repoaudit.RepositoryMappingConflictFieldEvidence,
		repoaudit.RepositoryMappingConflictFieldImpact,
		repoaudit.RepositoryMappingConflictFieldValidationContent,
		repoaudit.RepositoryMappingConflictFieldOther,
	}
	if !ok || !slices.Equal(fieldEnum, wantEnum) {
		t.Fatalf("conflicting anchor field enum=%#v", field["enum"])
	}
	textSchema, ok := itemProperties["text"].(map[string]any)
	if !ok || textSchema["type"] != "string" {
		t.Fatalf("conflicting anchor text schema=%#v", itemProperties["text"])
	}
}

func TestRepositoryValidationAdjudicationRunnerErrorsAndBounds(t *testing.T) {
	snapshot := repoaudit.RepositoryMappingModelSnapshot{Model: "cheap", Account: "api"}
	finding := repoaudit.RepositoryFinding{CanonicalTitle: "waiter remains blocked"}
	evidence := []repoaudit.RepositoryValidationEvidence{{
		CommitSHA: strings.Repeat("a", 40), Summary: "fix waiter", Diff: "- false\n+ true",
	}}
	valid := newRepositoryReviewAIAdjudicationHandler(t, http.StatusOK,
		`{"outcome":"not_fixed","selected_commit_sha":"","summary":"Defect remains."}`,
	)
	result, err := runRepositoryValidationAdjudication(
		t.Context(), valid, snapshot, finding, evidence,
	)
	if err != nil || result.Outcome != repoaudit.RepositoryValidationNotFixed {
		t.Fatalf("validation result=%#v err=%v", result, err)
	}
	assertFailureCode := func(
		t *testing.T,
		err error,
		want repoaudit.RepositoryValidationFailureCode,
	) {
		t.Helper()
		got, ok := repoaudit.RepositoryValidationFailureCodeFromError(err)
		if err == nil || !ok || got != want {
			t.Fatalf("validation failure code=%q found=%v err=%v; want %q", got, ok, err, want)
		}
	}
	providerFailure := newRepositoryReviewAIAdjudicationHandler(
		t, http.StatusServiceUnavailable, "",
	)
	_, err = runRepositoryValidationAdjudication(
		t.Context(), providerFailure, snapshot, finding, evidence,
	)
	assertFailureCode(t, err, repoaudit.RepositoryValidationFailureCodeModelRequest)
	invalidStructured := newRepositoryReviewAIAdjudicationHandler(t, http.StatusOK, `{}`)
	_, err = runRepositoryValidationAdjudication(
		t.Context(), invalidStructured, snapshot, finding, evidence,
	)
	assertFailureCode(t, err, repoaudit.RepositoryValidationFailureCodeModelOutputInvalid)
	_, err = runRepositoryValidationAdjudication(
		t.Context(), NewHandler(t.TempDir()), snapshot, finding, evidence,
	)
	assertFailureCode(t, err, repoaudit.RepositoryValidationFailureCodeModelUnavailable)
	_, err = runRepositoryValidationAdjudication(
		t.Context(), valid,
		repoaudit.RepositoryMappingModelSnapshot{Model: "missing", Account: "missing"},
		finding, evidence,
	)
	assertFailureCode(t, err, repoaudit.RepositoryValidationFailureCodeModelUnavailable)
	expired, cancelExpired := context.WithDeadline(t.Context(), time.Now().Add(-time.Second))
	defer cancelExpired()
	_, err = runRepositoryValidationAdjudication(
		expired, valid, snapshot, finding, evidence,
	)
	assertFailureCode(t, err, repoaudit.RepositoryValidationFailureCodeModelTimeout)
	oversized := []repoaudit.RepositoryValidationEvidence{{
		CommitSHA: strings.Repeat("b", 40), Diff: strings.Repeat("x", (2<<20)+1),
	}}
	_, err = runRepositoryValidationAdjudication(
		t.Context(), valid, snapshot, finding, oversized,
	)
	assertFailureCode(t, err, repoaudit.RepositoryValidationFailureCodeEvidenceInvalid)
}

func TestRepositoryAdjudicationOutputBoundaryFailures(t *testing.T) {
	handler := newRepositoryReviewAIAdjudicationHandler(t, http.StatusOK, `{}`)
	snapshot := repoaudit.RepositoryMappingModelSnapshot{Model: "cheap", Account: "api"}
	mappingRequest := repoaudit.RepositoryMappingAIRequest{Finding: repoaudit.Finding{Title: "finding"}}
	originalMapping := runRepositoryMappingAgent
	originalValidation := runRepositoryValidationAgent
	t.Cleanup(func() {
		runRepositoryMappingAgent = originalMapping
		runRepositoryValidationAgent = originalValidation
	})

	for _, outputs := range []map[string]any{
		{"structured_valid": false},
		{"structured_valid": true, "structured": make(chan int)},
		{"structured_valid": true, "structured": map[string]any{
			"decision": "same", "candidate_id": "unknown", "confidence": .9,
			"matching_anchors": []any{}, "conflicting_anchors": []any{},
			"explanation": "same",
		}},
		{"structured_valid": true, "structured": map[string]any{
			"decision": "same", "candidate_id": "opaque", "confidence": .9,
			"matching_anchors": []any{}, "conflicting_anchors": []any{},
			"explanation": "same", "unexpected": true,
		}},
	} {
		runRepositoryMappingAgent = func(
			context.Context, *webWorkflowRuntimeRunner, workflows.AgentRequest,
		) (map[string]any, error) {
			return outputs, nil
		}
		if _, err := runRepositoryMappingAdjudication(
			t.Context(), handler, snapshot, mappingRequest,
		); err == nil {
			t.Fatalf("mapping outputs %#v were accepted", outputs)
		}
	}

	validationFinding := repoaudit.RepositoryFinding{CanonicalTitle: "finding"}
	for _, outputs := range []map[string]any{
		{"structured_valid": false},
		{"structured_valid": true, "structured": map[string]any{
			"outcome": "not_fixed", "selected_commit_sha": "", "summary": "summary",
			"unexpected": true,
		}},
		{"structured_valid": true, "structured": map[string]any{
			"outcome": "unexpected", "selected_commit_sha": "", "summary": "summary",
		}},
	} {
		runRepositoryValidationAgent = func(
			context.Context, *webWorkflowRuntimeRunner, workflows.AgentRequest,
		) (map[string]any, error) {
			return outputs, nil
		}
		if _, err := runRepositoryValidationAdjudication(
			t.Context(), handler, snapshot, validationFinding, nil,
		); err == nil {
			t.Fatalf("validation outputs %#v were accepted", outputs)
		} else if code, ok := repoaudit.RepositoryValidationFailureCodeFromError(err); !ok || code != repoaudit.RepositoryValidationFailureCodeModelOutputInvalid {
			t.Fatalf("validation outputs %#v failure code=%q found=%v err=%v", outputs, code, ok, err)
		}
	}
	runRepositoryValidationAgent = func(
		context.Context, *webWorkflowRuntimeRunner, workflows.AgentRequest,
	) (map[string]any, error) {
		return nil, context.DeadlineExceeded
	}
	if _, err := runRepositoryValidationAdjudication(
		t.Context(), handler, snapshot, validationFinding, nil,
	); err == nil {
		t.Fatal("timed out validation request was accepted")
	} else if code, ok := repoaudit.RepositoryValidationFailureCodeFromError(err); !ok || code != repoaudit.RepositoryValidationFailureCodeModelTimeout {
		t.Fatalf("timed out validation failure code=%q found=%v err=%v", code, ok, err)
	}
}

func TestRepositoryReviewLifecycleRouteFences(t *testing.T) {
	handler, mux, workspace := newRepositoryReviewAutomationTestHandler(t)
	t.Cleanup(handler.Shutdown)
	missingAutomation := "rra_missing"
	missingFinding := "rrf_missing"
	base := "/api/repository-reviews/automations/" + missingAutomation +
		"/repository-findings/" + missingFinding

	for _, test := range []struct {
		name, method, path, body string
		mutate                   bool
		want                     int
	}{
		{
			name: "lifecycle csrf", method: http.MethodPatch, path: base,
			body: `{"lifecycle":"dismissed","expected_version":1}`, want: http.StatusBadRequest,
		},
		{
			name: "lifecycle malformed", method: http.MethodPatch, path: base,
			body: `{`, mutate: true, want: http.StatusBadRequest,
		},
		{
			name: "lifecycle missing", method: http.MethodPatch, path: base,
			body: `{"lifecycle":"dismissed","expected_version":1}`, mutate: true, want: http.StatusNotFound,
		},
		{
			name: "duplicate malformed", method: http.MethodPost, path: base + "/duplicates",
			body: `{"unknown":true}`, mutate: true, want: http.StatusBadRequest,
		},
		{
			name: "duplicate csrf", method: http.MethodPost, path: base + "/duplicates",
			body: `{"candidate_id":"rrf_candidate","decision":"distinct","expected_provisional_version":1}`,
			want: http.StatusBadRequest,
		},
		{
			name: "duplicate missing", method: http.MethodPost, path: base + "/duplicates",
			body:   `{"candidate_id":"rrf_candidate","decision":"distinct","expected_provisional_version":1}`,
			mutate: true, want: http.StatusNotFound,
		},
		{
			name: "validation malformed", method: http.MethodPost,
			path: "/api/repository-reviews/automations/" + missingAutomation + "/repository-findings/validations",
			body: `{`, mutate: true, want: http.StatusBadRequest,
		},
		{
			name: "validation csrf", method: http.MethodPost,
			path: "/api/repository-reviews/automations/" + missingAutomation + "/repository-findings/validations",
			body: `{"repository_finding_ids":["rrf_missing"]}`, want: http.StatusBadRequest,
		},
		{
			name: "validation missing", method: http.MethodPost,
			path: "/api/repository-reviews/automations/" + missingAutomation + "/repository-findings/validations",
			body: `{"repository_finding_ids":["rrf_missing"]}`, mutate: true, want: http.StatusNotFound,
		},
		{
			name: "sync query", method: http.MethodPost, path: base + "/sync?unexpected=1",
			body: `{}`, mutate: true, want: http.StatusBadRequest,
		},
		{
			name: "sync body", method: http.MethodPost, path: base + "/sync",
			body: `{"unknown":true}`, mutate: true, want: http.StatusBadRequest,
		},
		{
			name: "sync missing", method: http.MethodPost, path: base + "/sync",
			body: `{}`, mutate: true, want: http.StatusNotFound,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(test.method, test.path, strings.NewReader(test.body))
			if test.mutate {
				setRepositoryReviewMutationHeaders(request)
			}
			response := httptest.NewRecorder()
			mux.ServeHTTP(response, request)
			if response.Code != test.want {
				t.Fatalf("status=%d want=%d body=%s", response.Code, test.want, response.Body.String())
			}
		})
	}

	state := seedRepositoryReviewAPIState(t, workspace)
	state = completeRepositoryReviewAPIMappingJobs(t, workspace, state)
	automation := seedRepositoryReviewDetailAutomation(t, handler, state.Repository, state.Runs[0].ID)
	missingAggregatePath := "/api/repository-reviews/automations/" + automation.ID +
		"/repository-findings/rrf_missing/sync"
	response := repositoryReviewAutomationMutation(t, mux, http.MethodPost, missingAggregatePath, map[string]any{})
	if response.Code != http.StatusNotFound {
		t.Fatalf("known-ledger missing sync=%d %s", response.Code, response.Body.String())
	}
}

func TestRepositoryReviewLifecycleDuplicateAndValidationStoreFences(t *testing.T) {
	handler, mux, workspace := newRepositoryReviewAutomationTestHandler(t)
	t.Cleanup(handler.Shutdown)
	fixture := newRepositoryReviewValidationGitFixture(t)
	store := repoaudit.NewStore(workspace)
	state, firstOccurrence := recordRepositoryReviewControllerFinding(t, store, fixture)
	firstJob := state.MappingJobs[0]
	_, claimedFirst, _, claimed, err := store.ClaimMappingJob(
		state.Repository, firstJob.ID, repoaudit.RepositoryMappingModelSnapshot{},
	)
	if err != nil || !claimed {
		t.Fatalf("claim first=%v job=%#v err=%v", claimed, claimedFirst, err)
	}
	_, candidate, err := store.CompleteMappingJob(
		state.Repository, repoaudit.RepositoryMappingCompletion{
			JobID: claimedFirst.ID, CreateMatchState: repoaudit.RepositoryMatchNew,
			DefaultBranchVerified: true,
		},
	)
	if err != nil {
		t.Fatal(err)
	}

	file := repoaudit.FileRef{
		Path: "src/predicate_waiter.go", BlobSHA: strings.Repeat("b", 40), SizeBytes: 64,
		Category: "code", Mode: "100644",
	}
	plan, err := store.Plan(
		t.Context(), fixture.directory, fixture.rename, "duplicate-inventory",
		[]repoaudit.FileRef{file}, false,
	)
	if err != nil {
		t.Fatal(err)
	}
	plan, err = repoaudit.BindPlanBranch(plan, "main", "main", true)
	if err != nil {
		t.Fatal(err)
	}
	line := 3
	secondResult, err := store.Record(t.Context(), repoaudit.RecordRequest{
		Plan: plan, RunID: "duplicate-run", TargetBranch: "main",
		AdvertisedDefaultBranch: "main", TargetIsDefault: true,
		Observations: []repoaudit.Observation{{
			Model: "cheap", Reviewer: "cheap", ScopeFiles: []repoaudit.FileRef{file},
			Findings: []repoaudit.FindingCandidate{{
				Severity: "high", Title: "possibly identical waiter failure",
				Symbol: "predicate_waiter", File: file.Path, Line: &line,
				Message:  "The moved waiter may retain the stale owner.",
				Evidence: "The causal identity needs adjudication.",
				Impact:   "The coroutine remains blocked.", Validation: firstOccurrence.Validation,
				MatchHints: repoaudit.MatchHints{
					Component: "core scheduling", Operation: "requeue moved waiter",
					FailureMode: "waiter remains on stale owner", Trigger: "move then failed wake",
					ViolatedInvariant: "waiter requeues on current owner",
					ObservableOutcome: "coroutine remains blocked indefinitely",
					RelatedSymbols:    []string{"predicate_waiter"}, SourceAnchors: []string{"add_waiter"},
					DistinguishingFacts: []string{"identity is uncertain after rename"},
				},
				FixEffort: firstOccurrence.FixEffort,
			}},
		}},
	})
	if err != nil || len(secondResult.AcceptedFindingIDs) != 1 {
		t.Fatalf("second result=%#v err=%v", secondResult, err)
	}
	state = secondResult.State
	secondID := secondResult.AcceptedFindingIDs[0]
	var secondJob repoaudit.RepositoryMappingJob
	for _, job := range state.MappingJobs {
		if job.ReviewFindingID == secondID {
			secondJob = job
			break
		}
	}
	_, claimedSecond, _, claimed, err := store.ClaimMappingJob(
		state.Repository, secondJob.ID, repoaudit.RepositoryMappingModelSnapshot{},
	)
	if err != nil || !claimed {
		t.Fatalf("claim second=%v job=%#v err=%v", claimed, claimedSecond, err)
	}
	state, provisional, err := store.CompleteMappingJob(
		state.Repository, repoaudit.RepositoryMappingCompletion{
			JobID: claimedSecond.ID, CreateMatchState: repoaudit.RepositoryMatchProvisional,
			DefaultBranchVerified: true,
			PossibleDuplicates: []repoaudit.RepositoryFindingPossibleDuplicate{{
				CandidateID: candidate.ID, Relation: "uncertain", Confidence: .74,
				MatchingAnchors: []string{"trigger", "invariant"},
				Explanation:     "The rename leaves causal identity uncertain.",
			}},
		},
	)
	if err != nil || provisional.MatchState != repoaudit.RepositoryMatchProvisional {
		t.Fatalf("provisional=%#v err=%v", provisional, err)
	}
	automation := seedRepositoryReviewDetailAutomation(
		t, handler, state.Repository, state.Runs[0].ID,
	)
	path := "/api/repository-reviews/automations/" + automation.ID +
		"/repository-findings/" + provisional.ID + "/duplicates"
	stale := repositoryReviewAutomationMutation(t, mux, http.MethodPost, path, map[string]any{
		"candidate_id": candidate.ID, "decision": "distinct",
		"expected_provisional_version": provisional.Version + 1,
	})
	if stale.Code != http.StatusConflict {
		t.Fatalf("stale duplicate=%d %s", stale.Code, stale.Body.String())
	}
	provisionalValidation := repositoryReviewAutomationMutation(
		t, mux, http.MethodPost,
		"/api/repository-reviews/automations/"+automation.ID+"/repository-findings/validations",
		map[string]any{"repository_finding_ids": []string{provisional.ID}},
	)
	if provisionalValidation.Code != http.StatusConflict {
		t.Fatalf(
			"provisional validation=%d %s",
			provisionalValidation.Code, provisionalValidation.Body.String(),
		)
	}
	distinct := repositoryReviewAutomationMutation(t, mux, http.MethodPost, path, map[string]any{
		"candidate_id": candidate.ID, "decision": "distinct",
		"expected_provisional_version": provisional.Version,
	})
	if distinct.Code != http.StatusOK ||
		!strings.Contains(distinct.Body.String(), `"match_state":"new"`) {
		t.Fatalf("distinct duplicate=%d %s", distinct.Code, distinct.Body.String())
	}

	base := "/api/repository-reviews/automations/" + automation.ID
	missingValidation := repositoryReviewAutomationMutation(
		t, mux, http.MethodPost, base+"/repository-findings/validations",
		map[string]any{"repository_finding_ids": []string{"rrf_missing"}},
	)
	if missingValidation.Code != http.StatusNotFound {
		t.Fatalf("missing validation=%d %s", missingValidation.Code, missingValidation.Body.String())
	}
	staleLifecycle := repositoryReviewAutomationMutation(
		t, mux, http.MethodPatch, base+"/repository-findings/"+candidate.ID,
		map[string]any{"lifecycle": "dismissed", "expected_version": candidate.Version + 100},
	)
	if staleLifecycle.Code != http.StatusConflict {
		t.Fatalf("stale lifecycle=%d %s", staleLifecycle.Code, staleLifecycle.Body.String())
	}
	syncResponse := repositoryReviewAutomationMutation(
		t, mux, http.MethodPost, base+"/repository-findings/"+candidate.ID+"/sync",
		map[string]any{},
	)
	if syncResponse.Code == http.StatusNotFound || syncResponse.Code == http.StatusBadRequest {
		t.Fatalf("known sync did not reach gateway proxy: %d %s", syncResponse.Code, syncResponse.Body.String())
	}

	profileStore := repoaudit.NewStore(workspace)
	profile, err := profileStore.CreateProfile(t.Context(), repoaudit.RepositoryReviewProfile{
		ID: "rrpf_kttutlpoaklekkcrod5fqpz3qw", Name: "Deleted validation profile",
		ReviewFocus: "Find concrete bugs.", ScopePolicy: repoaudit.RepositoryReviewScopePolicy{
			CodeTypes: []repoaudit.RepositoryReviewCodeType{repoaudit.RepositoryReviewCodeTypeCode},
		},
		ReviewerModel: "cheap", AutoContinue: true, MaxFilesPerRun: 12,
		MaxContentBytes: 64 << 10, MaxParallelChildren: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	materialized, err := repoaudit.MaterializeRepositoryReviewAutomation(profile, automation)
	if err != nil {
		t.Fatal(err)
	}
	automationStore, err := handler.repositoryReviewStore()
	if err != nil {
		t.Fatal(err)
	}
	automation, err = automationStore.UpdateAutomation(
		t.Context(), automation.ID, automation.Version,
		func(value *repoaudit.RepositoryReviewAutomation) error {
			*value = materialized
			return nil
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	profilePath := filepath.Join(workspace, "repository_reviews", "profile_"+profile.ID+".json")
	if err := os.Remove(profilePath); err != nil {
		t.Fatal(err)
	}
	missingProfileValidation := repositoryReviewAutomationMutation(
		t, mux, http.MethodPost, base+"/repository-findings/validations",
		map[string]any{"repository_finding_ids": []string{candidate.ID}},
	)
	if missingProfileValidation.Code != http.StatusNotFound {
		t.Fatalf(
			"missing-profile validation=%d %s automation=%#v",
			missingProfileValidation.Code, missingProfileValidation.Body.String(), automation,
		)
	}
}

//nolint:govet // Sequential route probes intentionally reuse short-lived error names.
func TestRepositoryReviewLifecycleMissingLedgerModelAndIssueTTLBranches(t *testing.T) {
	handler, mux, workspace := newRepositoryReviewAutomationTestHandler(t)
	t.Cleanup(handler.Shutdown)
	store, err := handler.repositoryReviewStore()
	if err != nil {
		t.Fatal(err)
	}
	empty := testRepositoryReviewAutomation()
	empty.ID = "rra_empty_lifecycle_ledger"
	empty.Repository = "owner/no-ledger"
	empty.RunIDs = []string{"missing-run"}
	empty, err = store.CreateAutomation(t.Context(), empty)
	if err != nil {
		t.Fatal(err)
	}
	base := "/api/repository-reviews/automations/" + empty.ID
	for _, request := range []struct {
		method, path string
		body         map[string]any
	}{
		{
			http.MethodPatch, base + "/repository-findings/rrf_missing",
			map[string]any{"lifecycle": "dismissed", "expected_version": 1},
		},
		{
			http.MethodPost, base + "/repository-findings/rrf_missing/duplicates",
			map[string]any{"candidate_id": "rrf_other", "decision": "distinct", "expected_provisional_version": 1},
		},
		{
			http.MethodPost, base + "/repository-findings/validations",
			map[string]any{"repository_finding_ids": []string{"rrf_missing"}},
		},
		{http.MethodPost, base + "/repository-findings/rrf_missing/sync", map[string]any{}},
	} {
		response := repositoryReviewAutomationMutation(
			t, mux, request.method, request.path, request.body,
		)
		if response.Code != http.StatusNotFound {
			t.Fatalf("missing ledger %s=%d %s", request.path, response.Code, response.Body.String())
		}
	}

	state := seedRepositoryReviewAPIState(t, workspace)
	state = completeRepositoryReviewAPIMappingJobs(t, workspace, state)
	automation := seedRepositoryReviewDetailAutomation(
		t, handler, state.Repository, state.Runs[0].ID,
	)
	aggregate := state.RepositoryFindings[0]
	cfg, err := config.LoadConfig(handler.configPath)
	if err != nil {
		t.Fatal(err)
	}
	withoutAccount := *cfg
	withoutAccount.Agents.Defaults.AccountRef = ""
	if err := config.SaveConfig(handler.configPath, &withoutAccount); err != nil {
		t.Fatal(err)
	}
	validationPath := "/api/repository-reviews/automations/" + automation.ID +
		"/repository-findings/validations"
	originalLoad := loadRepositoryReviewLifecycleConfig
	loadRepositoryReviewLifecycleConfig = func(string) (*config.Config, error) {
		return nil, errors.New("injected lifecycle config failure")
	}
	configFailure := repositoryReviewAutomationMutation(
		t, mux, http.MethodPost, validationPath,
		map[string]any{"repository_finding_ids": []string{aggregate.ID}},
	)
	loadRepositoryReviewLifecycleConfig = originalLoad
	if configFailure.Code != http.StatusInternalServerError {
		t.Fatalf("validation config failure=%d %s", configFailure.Code, configFailure.Body.String())
	}
	unavailable := repositoryReviewAutomationMutation(
		t, mux, http.MethodPost, validationPath,
		map[string]any{"repository_finding_ids": []string{aggregate.ID}},
	)
	if unavailable.Code != http.StatusInternalServerError {
		t.Fatalf("unavailable validation model=%d %s", unavailable.Code, unavailable.Body.String())
	}
	if err := config.SaveConfig(handler.configPath, cfg); err != nil {
		t.Fatal(err)
	}

	state, _, err = store.LinkExistingIssue(repoaudit.ExistingIssueLink{
		Repository: state.Repository, FindingID: state.Findings[0].ID,
		ExpectedFindingVersion: state.Findings[0].Version,
		ExternalID:             "12", ExternalURL: "https://github.com/owner/repo/issues/12",
		Title: "Existing issue", State: "open", Origin: repoaudit.IssueDraftOriginLinked,
		Confirmed: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	state.RepositoryFindings[0].Issue.SnapshotAt = time.Now().UTC().Add(-time.Hour)
	data, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	statePath := filepath.Join(
		workspace, "repository_reviews", "repo_"+strings.TrimPrefix(state.ID, "rrp_")+".json",
	)
	if err := os.WriteFile(statePath, data, 0o600); err != nil {
		t.Fatal(err)
	}
	staleIssue := repositoryReviewAutomationMutation(
		t, mux, http.MethodPost, validationPath,
		map[string]any{"repository_finding_ids": []string{state.RepositoryFindings[0].ID}},
	)
	if staleIssue.Code != http.StatusBadRequest {
		t.Fatalf("stale issue validation=%d %s", staleIssue.Code, staleIssue.Body.String())
	}
}

func TestRepositoryReviewLegacyAndAutomationOffsetBranches(t *testing.T) {
	handler, mux, workspace := newRepositoryReviewAutomationTestHandler(t)
	t.Cleanup(handler.Shutdown)

	for _, action := range []string{"issue-drafts//publish", "other/id/publish"} {
		request := httptest.NewRequest(http.MethodPost, "/", nil)
		request.SetPathValue("legacy_action", action)
		response := httptest.NewRecorder()
		handler.handleLegacyRepositoryReviewAction(response, request)
		if response.Code != http.StatusNotFound {
			t.Fatalf("legacy action %q=%d", action, response.Code)
		}
	}

	state := seedRepositoryReviewAPIState(t, workspace)
	for _, status := range []repoaudit.FindingStatus{
		repoaudit.FindingOpen, repoaudit.FindingDismissed, repoaudit.FindingStatus("invalid"),
	} {
		request := httptest.NewRequest(
			http.MethodPatch,
			"/api/repository-reviews/"+state.ID+"/findings/"+state.Findings[0].ID,
			strings.NewReader(`{"status":"`+string(status)+`","expected_version":1}`),
		)
		request.SetPathValue("repository_id", state.ID)
		request.SetPathValue("finding_id", state.Findings[0].ID)
		setRepositoryReviewMutationHeaders(request)
		response := httptest.NewRecorder()
		handler.handleUpdateRepositoryReviewFinding(response, request)
		want := http.StatusConflict
		if status == "invalid" {
			want = http.StatusBadRequest
		}
		if response.Code != want {
			t.Fatalf("immutable status %q=%d want=%d %s", status, response.Code, want, response.Body.String())
		}
	}

	automation := seedRepositoryReviewDetailAutomation(t, handler, state.Repository, state.Runs[0].ID)
	list := httptest.NewRecorder()
	mux.ServeHTTP(list, httptest.NewRequest(http.MethodGet, "/api/repository-reviews/automations", nil))
	if list.Code != http.StatusOK || !strings.Contains(list.Body.String(), automation.ID) ||
		!strings.Contains(list.Body.String(), `"findings":1`) {
		t.Fatalf("automation list=%d %s", list.Code, list.Body.String())
	}
	statePath := filepath.Join(
		workspace, "repository_reviews", "repo_"+strings.TrimPrefix(state.ID, "rrp_")+".json",
	)
	if err := os.WriteFile(statePath, []byte(`{`), 0o600); err != nil {
		t.Fatal(err)
	}
	corruptList := httptest.NewRecorder()
	mux.ServeHTTP(
		corruptList, httptest.NewRequest(http.MethodGet, "/api/repository-reviews/automations", nil),
	)
	if corruptList.Code != http.StatusBadRequest {
		t.Fatalf("corrupt automation ledger=%d %s", corruptList.Code, corruptList.Body.String())
	}

	cfg := config.DefaultConfig()
	cfg.ModelAliases = []config.ModelAliasConfig{
		{Name: "subscription", Model: "openai/subscription"},
		{Name: "metered", Model: "openai/metered"},
	}
	cfg.ModelList = []*config.ModelConfig{{
		ModelName: "api", Provider: "openai", Model: "openai/subscription", Enabled: true,
		Subscription: true, SubscriptionEquivalentModel: "metered",
		InputPricePerMTok: 0, OutputPricePerMTok: 0,
	}}
	price, ok := repositoryReviewAliasPriceForAccount(
		cfg, "subscription", "api", make(map[string]bool),
	)
	if ok || price != nil {
		// The equivalent is intentionally unresolved; this also exercises the
		// subscription inheritance path without inventing a price.
		t.Fatalf("unresolved inherited price=%#v ok=%v", price, ok)
	}
}

func TestRepositoryValidationAdjudicationProjectionDropsEmptyEvidence(t *testing.T) {
	finding, evidence, source := repositoryValidationAdjudicationProjection(
		repoaudit.RepositoryFinding{
			Issue:              repoaudit.RepositoryFindingIssueAssociation{URL: "secret"},
			PossibleDuplicates: []repoaudit.RepositoryFindingPossibleDuplicate{{CandidateID: "other"}},
			ResolutionHistory:  []repoaudit.RepositoryFindingResolution{{Summary: "secret"}},
		},
		[]repoaudit.RepositoryValidationEvidence{
			{CurrentSource: "current"},
			{CommitSHA: strings.Repeat("a", 40), Summary: "candidate", CurrentSource: "later"},
		},
	)
	if source != "current" || len(evidence) != 1 || evidence[0].CurrentSource != "" ||
		finding.Issue.URL != "" || finding.PossibleDuplicates != nil || finding.ResolutionHistory != nil {
		t.Fatalf("finding=%#v evidence=%#v source=%q", finding, evidence, source)
	}
	if got := repositoryMappingTailStrings([]string{"a", "b"}, 0); got != nil {
		t.Fatalf("zero-limit tail=%#v", got)
	}
	if got := repositoryMappingTailStrings(nil, 2); got != nil {
		t.Fatalf("nil tail=%#v", got)
	}
	if got := repositoryMappingTailPathSymbolHistory(nil, 2); got != nil {
		t.Fatalf("nil history tail=%#v", got)
	}

	encoded, err := json.Marshal(repositoryReviewMappingSchema())
	if err != nil || !bytes.Contains(encoded, []byte(`"uncertain"`)) {
		t.Fatalf("mapping schema JSON=%s err=%v", encoded, err)
	}
}

func TestRepositoryMappingDefaultVerifierNoWorkAndInvalidRoot(t *testing.T) {
	verify, regression, release := repositoryMappingDefaultVerifier(
		t.Context(), nil, repoaudit.RepositoryReviewAutomation{}, repoaudit.RepositoryState{},
	)
	defer release()
	if ok, err := verify(t.Context(), repoaudit.Finding{}); err != nil || ok {
		t.Fatalf("no-work verify=%v err=%v", ok, err)
	}
	if ok, err := regression(t.Context(), repoaudit.Finding{}, repoaudit.RepositoryFinding{}); err != nil || ok {
		t.Fatalf("no-work regression=%v err=%v", ok, err)
	}

	invalidRoot := filepath.Join(t.TempDir(), "root-file")
	if err := os.WriteFile(invalidRoot, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := config.DefaultConfig()
	cfg.GitWorkspaces.RootDir = invalidRoot
	verify, regression, release = repositoryMappingDefaultVerifier(
		t.Context(), cfg,
		repoaudit.RepositoryReviewAutomation{ID: "rra_invalid", Repository: "owner/repo"},
		repoaudit.RepositoryState{Findings: []repoaudit.Finding{{TargetIsDefault: true}}},
	)
	defer release()
	if _, err := verify(t.Context(), repoaudit.Finding{}); err == nil {
		t.Fatal("invalid workspace root verifier had no error")
	}
	if _, err := regression(t.Context(), repoaudit.Finding{}, repoaudit.RepositoryFinding{}); err == nil {
		t.Fatal("invalid workspace root regression verifier had no error")
	}
}

//nolint:govet // Sequential controller probes intentionally reuse short-lived error names.
func TestRepositoryMappingControllerAndGitFailureBranches(t *testing.T) {
	handler, _, workspace := newRepositoryReviewAutomationTestHandler(t)
	t.Cleanup(handler.Shutdown)
	cfg, err := config.LoadConfig(handler.configPath)
	if err != nil {
		t.Fatal(err)
	}

	poisonedWorkspace := t.TempDir()
	if err := os.WriteFile(
		filepath.Join(poisonedWorkspace, "repository_reviews"), []byte("not a directory"), 0o600,
	); err != nil {
		t.Fatal(err)
	}
	poisonedController := newRepositoryReviewController(handler)
	poisonedController.leasedConfig = cfg
	poisonedController.leasedStore = repoaudit.NewStore(poisonedWorkspace)
	if err := poisonedController.processRepositoryFindingMappings(t.Context(), nil); err == nil {
		t.Fatal("poisoned mapping catalog was accepted")
	}
	if err := poisonedController.processRepositoryFindingValidations(t.Context(), nil); err == nil {
		t.Fatal("poisoned validation catalog was accepted")
	}

	fixture := newRepositoryReviewValidationGitFixture(t)
	store := repoaudit.NewStore(workspace)
	state, _ := recordRepositoryReviewControllerFinding(t, store, fixture)
	controller := newRepositoryReviewController(handler)
	controller.leasedConfig = cfg
	controller.leasedStore = store
	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	if err := controller.processRepositoryFindingMappings(canceled, nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled mapping error=%v", err)
	}
	if err := controller.processRepositoryFindingValidations(canceled, nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled validation error=%v", err)
	}
	if err := controller.processRepositoryFindingMappings(t.Context(), nil); err != nil {
		t.Fatalf("fallback mapping: %v", err)
	}
	if err := controller.processRepositoryFindingMappings(t.Context(), nil); err != nil {
		t.Fatalf("completed mapping replay: %v", err)
	}
	state, found, err := store.Get(state.Repository)
	if err != nil || !found || len(state.RepositoryFindings) != 1 {
		t.Fatalf("fallback mapped state=%#v found=%v err=%v", state, found, err)
	}

	missingProfileWorkspace := t.TempDir()
	missingProfileStore := repoaudit.NewStore(missingProfileWorkspace)
	missingState, _ := recordRepositoryReviewControllerFinding(t, missingProfileStore, fixture)
	missingProfileController := newRepositoryReviewController(handler)
	missingProfileController.leasedConfig = cfg
	missingProfileController.leasedStore = missingProfileStore
	if err := missingProfileController.processRepositoryFindingMappings(
		t.Context(), []repoaudit.RepositoryReviewAutomation{{
			ID: "rra_missing_profile_snapshot", Repository: missingState.Repository,
			RunIDs: []string{"controller-run"}, ProfileID: "rrpf_kttutlpoaklekkcrod5fqpz3qw",
		}},
	); err != nil {
		t.Fatalf("missing mapping profile should defer without controller failure: %v", err)
	}

	unavailable := repoaudit.RepositoryReviewAutomation{
		ID: "rra_unavailable", Repository: "https://127.0.0.1:1/unavailable.git",
	}
	reachabilityState := repoaudit.RepositoryState{Findings: []repoaudit.Finding{{
		CommitSHA: strings.Repeat("a", 40), TargetIsDefault: true,
	}}}
	verify, regression, release := repositoryMappingDefaultVerifier(
		t.Context(), cfg, unavailable, reachabilityState,
	)
	defer release()
	if _, err := verify(t.Context(), reachabilityState.Findings[0]); err == nil {
		t.Fatal("unavailable default checkout did not report its acquisition error")
	}
	if _, err := regression(
		t.Context(), reachabilityState.Findings[0], repoaudit.RepositoryFinding{},
	); err == nil {
		t.Fatal("unavailable regression checkout did not report its acquisition error")
	}

	invalidRoot := filepath.Join(t.TempDir(), "rename-root-file")
	if err := os.WriteFile(invalidRoot, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	invalidCfg := *cfg
	invalidCfg.GitWorkspaces.RootDir = invalidRoot
	if equivalent := repositoryMappingRenameEquivalent(
		t.Context(), &invalidCfg, unavailable,
		repoaudit.RepositoryState{
			LastCommitSHA:      strings.Repeat("a", 40),
			RepositoryFindings: []repoaudit.RepositoryFinding{{FoundCommits: []string{strings.Repeat("b", 40)}}},
		},
	); equivalent("old.go", "new.go") {
		t.Fatal("invalid rename workspace invented equivalence")
	}
	if equivalent := repositoryMappingRenameEquivalent(
		t.Context(), cfg, unavailable,
		repoaudit.RepositoryState{
			LastCommitSHA:      strings.Repeat("a", 40),
			RepositoryFindings: []repoaudit.RepositoryFinding{{FoundCommits: []string{strings.Repeat("b", 40)}}},
		},
	); equivalent("old.go", "new.go") {
		t.Fatal("unavailable rename checkout invented equivalence")
	}
	if equivalent := repositoryMappingRenameEquivalent(
		t.Context(), cfg,
		repoaudit.RepositoryReviewAutomation{ID: "rra_bad_diff", Repository: fixture.directory},
		repoaudit.RepositoryState{
			LastCommitSHA:      fixture.rename,
			RepositoryFindings: []repoaudit.RepositoryFinding{{FoundCommits: []string{strings.Repeat("f", 40)}}},
		},
	); equivalent("old.go", "new.go") {
		t.Fatal("failed rename diff invented equivalence")
	}
	manyCommits := make([]string, 201)
	for index := range manyCommits {
		manyCommits[index] = fmt.Sprintf("%040x", index+1000)
	}
	_ = repositoryMappingRenameEquivalent(
		t.Context(), cfg,
		repoaudit.RepositoryReviewAutomation{ID: "rra_rename_cap", Repository: fixture.directory},
		repoaudit.RepositoryState{
			LastCommitSHA:      fixture.rename,
			RepositoryFindings: []repoaudit.RepositoryFinding{{FoundCommits: manyCommits}},
		},
	)
}

func TestRepositoryMappingSnapshotProfileAndUnavailableBranches(t *testing.T) {
	handler, _, workspace := newRepositoryReviewAutomationTestHandler(t)
	t.Cleanup(handler.Shutdown)
	store := repoaudit.NewStore(workspace)
	cfg, err := config.LoadConfig(handler.configPath)
	if err != nil {
		t.Fatal(err)
	}
	profile, err := store.CreateProfile(t.Context(), repoaudit.RepositoryReviewProfile{
		ID: "rrpf_kttutlpoaklekkcrod5fqpz3qw", Name: "Mapping profile",
		ReviewFocus: "Find concrete bugs.", ScopePolicy: repoaudit.RepositoryReviewScopePolicy{
			CodeTypes: []repoaudit.RepositoryReviewCodeType{repoaudit.RepositoryReviewCodeTypeCode},
		},
		ReviewerModel: "cheap", AccountRef: "api", AutoContinue: true,
		MaxFilesPerRun: 12, MaxContentBytes: 64 << 10, MaxParallelChildren: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := repositoryMappingSnapshot(t.Context(), store, cfg,
		repoaudit.RepositoryReviewAutomation{ProfileID: profile.ID})
	if err != nil || snapshot.ProfileID != profile.ID || snapshot.ProfileVersion != profile.Version ||
		snapshot.Model != "cheap" || snapshot.Account == "" {
		t.Fatalf("profile snapshot=%#v err=%v", snapshot, err)
	}
	emptyCfg := config.DefaultConfig()
	emptyCfg.Agents.Defaults.AccountRef = ""
	emptyCfg.Agents.Defaults.ModelName = ""
	snapshot, err = repositoryMappingSnapshot(
		t.Context(), store, emptyCfg, repoaudit.RepositoryReviewAutomation{},
	)
	if err != nil || snapshot != (repoaudit.RepositoryMappingModelSnapshot{}) {
		t.Fatalf("unavailable snapshot=%#v err=%v", snapshot, err)
	}

	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := repositoryMappingSnapshot(
		canceled, store, cfg, repoaudit.RepositoryReviewAutomation{ProfileID: profile.ID},
	); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled profile snapshot error=%v", err)
	}
}

//nolint:govet // Independent controller subtests intentionally reuse short-lived error names.
func TestRepositoryControllersFallbackAndJoinedErrors(t *testing.T) {
	handler, _, _ := newRepositoryReviewAutomationTestHandler(t)
	t.Cleanup(handler.Shutdown)
	cfg, err := config.LoadConfig(handler.configPath)
	if err != nil {
		t.Fatal(err)
	}
	cfg.GitWorkspaces.RootDir = t.TempDir()

	t.Run("mapping checkout error", func(t *testing.T) {
		fixture := newRepositoryReviewValidationGitFixture(t)
		store := repoaudit.NewStore(t.TempDir())
		_, _ = recordRepositoryReviewControllerFinding(t, store, fixture)
		moved := fixture.directory + "-moved"
		if err := os.Rename(fixture.directory, moved); err != nil {
			t.Fatal(err)
		}
		controller := newRepositoryReviewController(handler)
		controller.leasedConfig = cfg
		controller.leasedStore = store
		if err := controller.processRepositoryFindingMappings(t.Context(), nil); err == nil {
			t.Fatal("mapping checkout failure was not joined")
		}
	})

	t.Run("validation no pending and checkout error", func(t *testing.T) {
		fixture := newRepositoryReviewValidationGitFixture(t)
		store := repoaudit.NewStore(t.TempDir())
		state, _ := recordRepositoryReviewControllerFinding(t, store, fixture)
		_, job, _, claimed, err := store.ClaimMappingJob(
			state.Repository, state.MappingJobs[0].ID, repoaudit.RepositoryMappingModelSnapshot{},
		)
		if err != nil || !claimed {
			t.Fatalf("claim mapping=%v err=%v", claimed, err)
		}
		state, aggregate, err := store.CompleteMappingJob(
			state.Repository, repoaudit.RepositoryMappingCompletion{
				JobID: job.ID, CreateMatchState: repoaudit.RepositoryMatchNew,
				DefaultBranchVerified: true,
			},
		)
		if err != nil {
			t.Fatal(err)
		}
		controller := newRepositoryReviewController(handler)
		controller.leasedConfig = cfg
		controller.leasedStore = store
		if err := controller.processRepositoryFindingValidations(t.Context(), nil); err != nil {
			t.Fatalf("no-pending validation: %v", err)
		}
		state, jobs, err := store.ReserveValidationJobs(
			state.Repository, []string{aggregate.ID},
			repoaudit.RepositoryMappingModelSnapshot{Model: "cheap", Account: "api"},
		)
		if err != nil || len(jobs) != 1 {
			t.Fatalf("reserve=%#v err=%v", jobs, err)
		}
		moved := fixture.directory + "-moved"
		if err := os.Rename(fixture.directory, moved); err != nil {
			t.Fatal(err)
		}
		if err := controller.processRepositoryFindingValidations(t.Context(), nil); err != nil {
			t.Fatalf("validation checkout failure was not safely recorded: %v", err)
		}
		_ = state
	})
}

//nolint:govet // Sequential seam probes intentionally reuse short-lived error names.
func TestRepositoryControllerProcessSeamsCoverCallbacksAndJoinedErrors(t *testing.T) {
	handler := newRepositoryReviewAIAdjudicationHandler(t, http.StatusOK, `{}`)
	cfg, err := config.LoadConfig(handler.configPath)
	if err != nil {
		t.Fatal(err)
	}
	originalMappingProcess := processRepositoryMappingJobs
	originalValidationProcess := processRepositoryValidationJobs
	originalMappingAgent := runRepositoryMappingAgent
	originalValidationAgent := runRepositoryValidationAgent
	t.Cleanup(func() {
		processRepositoryMappingJobs = originalMappingProcess
		processRepositoryValidationJobs = originalValidationProcess
		runRepositoryMappingAgent = originalMappingAgent
		runRepositoryValidationAgent = originalValidationAgent
	})

	fixture := newRepositoryReviewValidationGitFixture(t)
	mappingStore := repoaudit.NewStore(t.TempDir())
	state, _ := recordRepositoryReviewControllerFinding(t, mappingStore, fixture)
	mappingController := newRepositoryReviewController(handler)
	mappingController.leasedConfig = cfg
	mappingController.leasedStore = mappingStore
	runRepositoryMappingAgent = func(
		context.Context, *webWorkflowRuntimeRunner, workflows.AgentRequest,
	) (map[string]any, error) {
		return map[string]any{
			"structured_valid": true,
			"structured": map[string]any{
				"decision": "distinct", "candidate_id": "", "confidence": .9,
				"matching_anchors": []string{}, "conflicting_anchors": []string{},
				"explanation": "The supplied records are distinct.",
			},
		}, nil
	}
	processRepositoryMappingJobs = func(
		_ repoaudit.Store, ctx context.Context, _ string,
		options repoaudit.RepositoryMappingProcessOptions,
	) (repoaudit.RepositoryMappingProcessResult, error) {
		if _, err := options.Adjudicate(
			ctx, options.ModelSnapshot, repoaudit.RepositoryMappingAIRequest{},
		); err != nil {
			t.Errorf("mapping callback: %v", err)
		}
		return repoaudit.RepositoryMappingProcessResult{}, errors.New("injected mapping processor failure")
	}
	if err := mappingController.processRepositoryFindingMappings(t.Context(), nil); err == nil {
		t.Fatal("injected mapping processor failure was not joined")
	}
	_ = state

	validationStore := repoaudit.NewStore(t.TempDir())
	validationState, _ := recordRepositoryReviewControllerFinding(t, validationStore, fixture)
	_, mappingJob, _, claimed, err := validationStore.ClaimMappingJob(
		validationState.Repository, validationState.MappingJobs[0].ID,
		repoaudit.RepositoryMappingModelSnapshot{},
	)
	if err != nil || !claimed {
		t.Fatalf("claim validation fixture mapping=%v err=%v", claimed, err)
	}
	validationState, aggregate, err := validationStore.CompleteMappingJob(
		validationState.Repository, repoaudit.RepositoryMappingCompletion{
			JobID: mappingJob.ID, CreateMatchState: repoaudit.RepositoryMatchNew,
			DefaultBranchVerified: true,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = validationStore.ReserveValidationJobs(
		validationState.Repository, []string{aggregate.ID},
		repoaudit.RepositoryMappingModelSnapshot{Model: "cheap", Account: "api"},
	)
	if err != nil {
		t.Fatal(err)
	}
	validationController := newRepositoryReviewController(handler)
	validationController.leasedConfig = cfg
	validationController.leasedStore = validationStore
	processRepositoryValidationJobs = func(
		_ repoaudit.Store, ctx context.Context, _ string,
		options repoaudit.RepositoryValidationProcessOptions,
	) (repoaudit.RepositoryValidationProcessResult, error) {
		if _, err := options.FirstSemanticTag(ctx, strings.Repeat("f", 40)); err == nil {
			t.Error("missing validation metadata returned a tag")
		}
		return repoaudit.RepositoryValidationProcessResult{}, errors.New("injected validation processor failure")
	}
	if err := validationController.processRepositoryFindingValidations(t.Context(), nil); err == nil {
		t.Fatal("injected validation processor failure was not joined")
	}
}

func TestRepositoryValidationMetadataClosures(t *testing.T) {
	metadata := sync.Map{}
	metadata.Store(strings.Repeat("a", 40), repositoryValidationGitMetadata{reachable: true, tag: "v2.0.0"})
	value, ok := metadata.Load(strings.Repeat("a", 40))
	if !ok || !value.(repositoryValidationGitMetadata).reachable ||
		value.(repositoryValidationGitMetadata).tag != "v2.0.0" {
		t.Fatalf("metadata=%#v ok=%v", value, ok)
	}
}

//nolint:govet // Sequential Git probes intentionally reuse short-lived error names.
func TestRepositoryValidationRemainingGitAndEvidenceBranches(t *testing.T) {
	fixture := newRepositoryReviewValidationGitFixture(t)
	handler, _, _ := newRepositoryReviewAutomationTestHandler(t)
	t.Cleanup(handler.Shutdown)
	cfg, err := config.LoadConfig(handler.configPath)
	if err != nil {
		t.Fatal(err)
	}

	invalidRoot := filepath.Join(t.TempDir(), "validation-root-file")
	if err := os.WriteFile(invalidRoot, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	invalidCfg := *cfg
	invalidCfg.GitWorkspaces.RootDir = invalidRoot
	controller := newRepositoryReviewController(handler)
	controller.leasedConfig = &invalidCfg
	provider := controller.repositoryValidationEvidenceProvider(
		repoaudit.RepositoryReviewAutomation{Repository: fixture.directory}, &sync.Map{},
	)
	if _, err := provider(t.Context(), repositoryReviewValidationFindingForTest(fixture), nil); err == nil {
		t.Fatal("invalid validation manager root was accepted")
	}

	validCfg := *cfg
	validCfg.GitWorkspaces.RootDir = t.TempDir()
	controller.leasedConfig = &validCfg
	provider = controller.repositoryValidationEvidenceProvider(
		repoaudit.RepositoryReviewAutomation{Repository: "https://127.0.0.1:1/unavailable.git"},
		&sync.Map{},
	)
	if _, err := provider(t.Context(), repositoryReviewValidationFindingForTest(fixture), nil); err == nil {
		t.Fatal("unavailable validation checkout was accepted")
	}

	provider = controller.repositoryValidationEvidenceProvider(
		repoaudit.RepositoryReviewAutomation{Repository: fixture.directory}, &sync.Map{},
	)
	noBaseline := repositoryReviewValidationFindingForTest(fixture)
	for index := range noBaseline.PathSymbolHistory {
		noBaseline.PathSymbolHistory[index].DefaultBranchVerified = false
	}
	if _, err := provider(t.Context(), noBaseline, nil); err == nil {
		t.Fatal("validation evidence without a baseline was accepted")
	}
	sourceOnly := repositoryReviewValidationFindingForTest(fixture)
	sourceOnly.PathSymbolHistory = []repoaudit.RepositoryFindingPathSymbol{{
		Path: "src/predicate_waiter.go", Symbol: "add_waiter", CommitSHA: fixture.rename,
		DefaultBranchVerified: true, ObservedAt: time.Now().UTC(),
	}}
	evidence, err := provider(t.Context(), sourceOnly, nil)
	if err != nil || len(evidence) != 1 || evidence[0].CommitSHA != "" || evidence[0].CurrentSource == "" {
		t.Fatalf("source-only evidence=%#v err=%v", evidence, err)
	}

	if _, err := repositoryValidationCommitLog(
		t.Context(), t.TempDir(), repoaudit.RepositoryFinding{}, nil, fixture.base,
	); err == nil {
		t.Fatal("validation commit log outside a repository succeeded")
	}
	duplicateBaseline := repositoryReviewValidationFindingForTest(fixture)
	missing := strings.Repeat("f", 40)
	duplicateBaseline.PathSymbolHistory = []repoaudit.RepositoryFindingPathSymbol{
		{CommitSHA: missing, DefaultBranchVerified: true, ObservedAt: time.Now().UTC().Add(time.Minute)},
		{CommitSHA: missing, DefaultBranchVerified: true, ObservedAt: time.Now().UTC()},
		{CommitSHA: fixture.base, DefaultBranchVerified: true, ObservedAt: time.Now().UTC().Add(-time.Minute)},
	}
	if baseline, err := repositoryValidationBaseline(
		t.Context(), fixture.directory, duplicateBaseline,
	); err != nil || baseline != fixture.base {
		t.Fatalf("duplicate baseline=%q err=%v", baseline, err)
	}

	emptyRanked := repositoryRankValidationCommits(
		repoaudit.RepositoryFinding{},
		[]repositoryValidationCommitRecord{{SHA: "one"}, {SHA: "two"}}, 1,
	)
	if len(emptyRanked) != 1 {
		t.Fatalf("fallback ranked=%#v", emptyRanked)
	}

	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	wrapperRoot := t.TempDir()
	wrapper := filepath.Join(wrapperRoot, "git")
	baseLogPath := filepath.Join(wrapperRoot, "base.log")
	symbolLogPath := filepath.Join(wrapperRoot, "symbol.log")
	script := "#!/bin/sh\nfor arg in \"$@\"; do case \"$arg\" in -S*) " +
		"if [ \"$FAIL_SYMBOL_LOG\" = 1 ]; then exit 9; fi; cat \"$SYMBOL_LOG_PATH\"; exit 0;; esac; done\n" +
		"if [ \"$1\" = show ] && [ -n \"$FROZEN_OUTPUT\" ]; then printf '%s' \"$FROZEN_OUTPUT\"; exit 0; fi\n" +
		"if [ -n \"$BASE_LOG_PATH\" ]; then cat \"$BASE_LOG_PATH\"; exit 0; fi\n" +
		"exec \"" + realGit + "\" \"$@\"\n"
	if err := os.WriteFile(wrapper, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", wrapperRoot+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("BASE_LOG_PATH", "")
	t.Setenv("SYMBOL_LOG_PATH", symbolLogPath)
	t.Setenv("FROZEN_OUTPUT", "malformed")
	if _, err := repositoryValidationFrozenCommits(
		t.Context(), fixture.directory, []string{fixture.fix},
	); err == nil {
		t.Fatal("malformed frozen metadata was accepted")
	}
	t.Setenv("FROZEN_OUTPUT", fixture.fix+"\x1fnot-a-time\x1fsubject\x1fbody")
	if _, err := repositoryValidationFrozenCommits(
		t.Context(), fixture.directory, []string{fixture.fix},
	); err == nil {
		t.Fatal("invalid frozen metadata time was accepted")
	}
	t.Setenv("FROZEN_OUTPUT", "")

	logRecord := func(index int) string {
		return "\x1e" + fmt.Sprintf("%040x", index) +
			"\x1f2026-08-22T14:30:00Z\x1fsubject\x1fbody"
	}
	if err := os.WriteFile(baseLogPath, []byte(logRecord(1)), 0o600); err != nil {
		t.Fatal(err)
	}
	var symbolLog strings.Builder
	symbolLog.WriteString(logRecord(1))
	for index := 2; index <= 200; index++ {
		symbolLog.WriteString(logRecord(index))
	}
	if err := os.WriteFile(symbolLogPath, []byte(symbolLog.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("BASE_LOG_PATH", baseLogPath)
	records, err := repositoryValidationCommitLog(
		t.Context(), t.TempDir(), repoaudit.RepositoryFinding{
			MatchHints: repoaudit.MatchHints{RelatedSymbols: []string{"waiter", "owner"}},
		}, nil, fixture.base,
	)
	if err != nil || len(records) != 200 {
		t.Fatalf("synthetic commit records=%d err=%v", len(records), err)
	}
	t.Setenv("FAIL_SYMBOL_LOG", "1")
	if _, err := repositoryValidationCommitLog(
		t.Context(), t.TempDir(), repoaudit.RepositoryFinding{
			MatchHints: repoaudit.MatchHints{RelatedSymbols: []string{"waiter"}},
		}, nil, fixture.base,
	); err != nil {
		t.Fatalf("symbol-log failure should be bounded: %v", err)
	}
}
