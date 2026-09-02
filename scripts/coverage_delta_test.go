//go:build featuretools

package main

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestCoverageNestedBenchmarkSkipPatternIsExact(t *testing.T) {
	t.Parallel()

	pattern := regexp.MustCompile(coverageNestedBenchmarkSkipPattern)
	for _, name := range []string{
		"TestGraderAcceptsReferenceAndReportsMutationEvidence",
		"TestCodingAgentBenchmarkScriptedGatewayPath",
		"TestWorkflowAdmissionConfigGuardBlocksCrossProcessSaveThroughCreateAndUsesCapturedConfig",
	} {
		if !pattern.MatchString(name) {
			t.Fatalf("coverage skip pattern omitted %q", name)
		}
	}
	for _, name := range []string{
		"TestGraderRejectsOutsideOutput",
		"TestCodingAgentBenchmarkLiveOptIn",
		"PrefixTestCodingAgentBenchmarkScriptedGatewayPath",
		"TestWorkflowAdmissionConfigGuardBlocksCrossProcessSaveThroughCreateAndUsesCapturedConfigExtra",
	} {
		if pattern.MatchString(name) {
			t.Fatalf("coverage skip pattern was too broad for %q", name)
		}
	}
}

func TestCoverageGoTestParallelismIsBounded(t *testing.T) {
	t.Parallel()
	if coverageGoTestParallelism != 1 {
		t.Fatalf("coverage Go test parallelism = %d, want 1", coverageGoTestParallelism)
	}
}

func TestCoverageIntegrationSuitesAllowHeadOnlyAddition(t *testing.T) {
	root := t.TempDir()
	base := filepath.Join(root, "base")
	head := filepath.Join(root, "head")
	for _, path := range []string{
		filepath.Join(base, "integration", "suites", "existing"),
		filepath.Join(head, "integration", "suites", "existing"),
		filepath.Join(head, "integration", "suites", "storage-json"),
	} {
		if err := os.MkdirAll(path, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	planned := []string{"existing", "storage-json"}

	baseSuites, err := coverageIntegrationSuitesForRef(base, "base", "base-ref", planned)
	if err != nil || !reflect.DeepEqual(baseSuites, []string{"existing"}) {
		t.Fatalf("base suites = %#v, %v", baseSuites, err)
	}
	headSuites, err := coverageIntegrationSuitesForRef(head, "head", "head-ref", planned)
	if err != nil || !reflect.DeepEqual(headSuites, planned) {
		t.Fatalf("head suites = %#v, %v", headSuites, err)
	}

	if err := os.Remove(filepath.Join(head, "integration", "suites", "storage-json")); err != nil {
		t.Fatal(err)
	}
	if suites, err := coverageIntegrationSuitesForRef(head, "head", "head-ref", planned); err == nil ||
		suites != nil || !strings.Contains(err.Error(), "planned suite storage-json is missing") {
		t.Fatalf("missing head suite = %#v, %v", suites, err)
	}

	unsafe := filepath.Join(base, "integration", "suites", "storage-json")
	if err := os.WriteFile(unsafe, []byte("not a suite"), 0o600); err != nil {
		t.Fatal(err)
	}
	if suites, err := coverageIntegrationSuitesForRef(base, "base", "base-ref", planned); err == nil ||
		suites != nil || !strings.Contains(err.Error(), "is not a real directory") {
		t.Fatalf("unsafe base suite = %#v, %v", suites, err)
	}

	for _, invalid := range []string{"", ".", "..", "../escape", `child\escape`, " bad"} {
		if suites, err := coverageIntegrationSuitesForRef(
			base,
			"base",
			"base-ref",
			[]string{invalid},
		); err == nil || suites != nil || !strings.Contains(err.Error(), "identity is invalid") {
			t.Fatalf("invalid suite identity %q = %#v, %v", invalid, suites, err)
		}
	}
}

func TestCoverageRegressionTracksUncoveredStatementDebt(t *testing.T) {
	tests := []struct {
		name string
		base coverageSummary
		head coverageSummary
		want bool
	}{
		{
			name: "covered legacy deletion reduces debt",
			base: coverageSummary{CoveredStatements: 22045, TotalStatements: 27866},
			head: coverageSummary{CoveredStatements: 14524, TotalStatements: 19782},
			want: false,
		},
		{
			name: "new uncovered statement increases debt",
			base: coverageSummary{CoveredStatements: 80, TotalStatements: 100},
			head: coverageSummary{CoveredStatements: 80, TotalStatements: 101},
			want: true,
		},
		{
			name: "additional coverage reduces debt",
			base: coverageSummary{CoveredStatements: 80, TotalStatements: 100},
			head: coverageSummary{CoveredStatements: 81, TotalStatements: 100},
			want: false,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := summaryRegressed(test.base, test.head); got != test.want {
				t.Fatalf("summaryRegressed(%+v, %+v) = %t, want %t", test.base, test.head, got, test.want)
			}
		})
	}
}

func TestFeatureCoverageRegressionAllowsTenUncoveredStatements(t *testing.T) {
	base := coverageSummary{CoveredStatements: 80, TotalStatements: 100}
	withinTolerance := coverageSummary{CoveredStatements: 80, TotalStatements: 110}
	regressed := coverageSummary{CoveredStatements: 80, TotalStatements: 111}

	if featureSummaryRegressed(base, withinTolerance) {
		t.Fatal("featureSummaryRegressed() rejected the documented ten-statement tolerance")
	}
	if !featureSummaryRegressed(base, regressed) {
		t.Fatal("featureSummaryRegressed() accepted an eleven-statement debt increase")
	}
}

func TestCompareCoverageReportsUncoveredDebtAndCoverage(t *testing.T) {
	spec := featureSpecMetadata{
		RelPath: "docs/features/example.md",
		Ownerships: []featureOwnership{
			{Kind: "CODE", Pattern: "pkg/example/**"},
		},
	}
	plan := coveragePlan{ImpactedFeature: map[string]bool{spec.RelPath: true}}
	base := coverageProfile{
		Global: coverageSummary{CoveredStatements: 80, TotalStatements: 100},
		Files: map[string]coverageSummary{
			"pkg/example/example.go": {CoveredStatements: 80, TotalStatements: 100},
		},
	}
	head := coverageProfile{
		Global: coverageSummary{CoveredStatements: 69, TotalStatements: 100},
		Files: map[string]coverageSummary{
			"pkg/example/example.go": {CoveredStatements: 69, TotalStatements: 100},
		},
	}

	want := []string{
		"scoped Go uncovered statement debt increased: 20 -> 31 (coverage 80.00% (80/100) -> 69.00% (69/100))",
		"docs/features/example.md Go uncovered statement debt increased: 20 -> 31 (coverage 80.00% (80/100) -> 69.00% (69/100))",
	}
	if got := compareCoverage([]featureSpecMetadata{spec}, plan, base, head); !reflect.DeepEqual(got, want) {
		t.Fatalf("compareCoverage() = %#v, want %#v", got, want)
	}
}

func TestCoverageEnvironmentIsolatesRefState(t *testing.T) {
	base := []string{
		"PATH=/bin",
		"HOME=/shared/user-home",
		"PICOCLAW_HOME=/shared/home",
		"picoclaw_home=/duplicate/home",
		"GOCACHE=/shared/build-cache",
		"GOMODCACHE=/shared/module-cache",
		"GOTOOLCHAIN=local",
		"AWS_ACCESS_KEY_ID=operator-access-key",
		"AWS_SECRET_ACCESS_KEY=operator-secret-key",
		"AWS_PROFILE=operator-profile",
		"AWS_SHARED_CREDENTIALS_FILE=/operator/aws-credentials",
		"OPENAI_API_KEY=operator-openai-key",
		"ANTHROPIC_API_KEY=operator-anthropic-key",
		"GITHUB_TOKEN=operator-github-token",
		"GITHUB_PAT=operator-github-pat",
		"GH_TOKEN=operator-gh-token",
		"GH_PAT=operator-gh-pat",
		"SSH_AUTH_SOCK=/operator/ssh-agent.sock",
		"GOOGLE_APPLICATION_CREDENTIALS=/operator/google.json",
		"AZURE_CLIENT_SECRET=operator-azure-secret",
		"KUBECONFIG=/operator/kubeconfig",
		"DOCKER_HOST=unix:///operator/docker.sock",
		"GIT_ASKPASS=/operator/askpass",
		"GIT_TERMINAL_PROMPT=1",
		"GCM_INTERACTIVE=always",
		"NETRC=/operator/netrc",
		"SERVICE_API_KEY=operator-generic-key",
		"NOTIFY_SOCKET=/operator/notify.sock",
		"LISTEN_FDS=3",
		"GOAUTH=/operator/goauth-helper",
		"GOENV=/operator/goenv",
		"VALUE=with=equals",
	}
	original := append([]string(nil), base...)

	caches := goCachePaths{Build: "/cache/build", Modules: "/cache/modules"}
	baseEnvironment := coverageEnvironment(base, "/isolated/base", caches)
	headEnvironment := coverageEnvironment(base, "/isolated/head", caches)

	if !reflect.DeepEqual(base, original) {
		t.Fatalf("coverageEnvironment() mutated its input: got %#v, want %#v", base, original)
	}
	assertEnvironmentValue(t, baseEnvironment, "HOME", "/isolated/base")
	assertEnvironmentValue(t, headEnvironment, "HOME", "/isolated/head")
	assertEnvironmentValue(t, baseEnvironment, "PICOCLAW_HOME", "/isolated/base/.picoclaw")
	assertEnvironmentValue(t, headEnvironment, "PICOCLAW_HOME", "/isolated/head/.picoclaw")
	assertEnvironmentValue(t, baseEnvironment, "PICOCLAW_CONFIG", "/isolated/base/.picoclaw/config.json")
	assertEnvironmentValue(t, baseEnvironment, "PICOCLAW_BINARY", "/isolated/base/bin/picoclaw")
	assertEnvironmentValue(t, baseEnvironment, "XDG_RUNTIME_DIR", "/isolated/base/.xdg/runtime")
	assertEnvironmentValue(t, baseEnvironment, "TMPDIR", "/isolated/base/.tmp")
	assertEnvironmentValue(t, baseEnvironment, "GNUPGHOME", "/isolated/base/.gnupg")
	assertEnvironmentValue(t, baseEnvironment, "GIT_CONFIG_NOSYSTEM", "1")
	assertEnvironmentValue(
		t,
		baseEnvironment,
		"DBUS_SESSION_BUS_ADDRESS",
		"unix:path=/isolated/base/.no-systemd-bus",
	)
	assertEnvironmentValue(t, baseEnvironment, "GOCACHE", "/cache/build")
	assertEnvironmentValue(t, baseEnvironment, "GOMODCACHE", "/cache/modules")
	assertEnvironmentValue(t, baseEnvironment, "GOTOOLCHAIN", "auto")
	assertEnvironmentValue(t, baseEnvironment, "AWS_EC2_METADATA_DISABLED", "true")
	assertEnvironmentValue(t, baseEnvironment, "GIT_TERMINAL_PROMPT", "0")
	assertEnvironmentValue(t, baseEnvironment, "GCM_INTERACTIVE", "never")
	assertEnvironmentValue(t, baseEnvironment, "GOAUTH", "off")
	assertEnvironmentValue(t, baseEnvironment, "GOENV", "off")
	assertEnvironmentValue(t, baseEnvironment, "PATH", "/bin")
	assertEnvironmentValue(t, baseEnvironment, "VALUE", "with=equals")
	for _, name := range []string{
		"ANTHROPIC_API_KEY",
		"AWS_ACCESS_KEY_ID",
		"AWS_PROFILE",
		"AWS_SECRET_ACCESS_KEY",
		"AWS_SHARED_CREDENTIALS_FILE",
		"AZURE_CLIENT_SECRET",
		"DOCKER_HOST",
		"GH_TOKEN",
		"GH_PAT",
		"GITHUB_TOKEN",
		"GITHUB_PAT",
		"GIT_ASKPASS",
		"GOOGLE_APPLICATION_CREDENTIALS",
		"KUBECONFIG",
		"NETRC",
		"NOTIFY_SOCKET",
		"LISTEN_FDS",
		"OPENAI_API_KEY",
		"SERVICE_API_KEY",
		"SSH_AUTH_SOCK",
	} {
		assertEnvironmentMissing(t, baseEnvironment, name)
	}
	fallbackEnvironment := coverageFallbackHomeEnvironment(baseEnvironment)
	assertEnvironmentValue(t, fallbackEnvironment, "HOME", "/isolated/base")
	assertEnvironmentValue(t, fallbackEnvironment, "PICOCLAW_HOME", "")
	assertEnvironmentValue(t, fallbackEnvironment, "PICOCLAW_CONFIG", "")
	assertEnvironmentValue(t, fallbackEnvironment, "PICOCLAW_BINARY", "/isolated/base/bin/picoclaw")
	assertEnvironmentValue(t, baseEnvironment, "PICOCLAW_HOME", "/isolated/base/.picoclaw")
}

func TestPrepareCoverageStorageDefersConfigUntilAfterUnitCoverage(t *testing.T) {
	home := filepath.Join(t.TempDir(), "coverage-home")
	if err := prepareCoverageStorage(home); err != nil {
		t.Fatalf("prepareCoverageStorage() error = %v", err)
	}
	configPath := filepath.Join(home, ".picoclaw", "config.json")
	if _, err := os.Stat(configPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("coverage config existed during fallback-home unit tests: %v", err)
	}
	if err := writeCoverageConfig(home); err != nil {
		t.Fatalf("writeCoverageConfig() error = %v", err)
	}
	if info, err := os.Stat(configPath); err != nil || !info.Mode().IsRegular() {
		t.Fatalf("coverage config = (%v, %v), want regular file", info, err)
	}
}

func TestPrepareCoverageHomeCreatesIsolatedRuntimeState(t *testing.T) {
	home := filepath.Join(t.TempDir(), "coverage-home")
	if err := prepareCoverageHome(home); err != nil {
		t.Fatalf("prepareCoverageHome() error = %v", err)
	}
	configPath := filepath.Join(home, ".picoclaw", "config.json")
	raw, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("read coverage config: %v", err)
	}
	var configFile struct {
		Agents struct {
			Defaults struct {
				Workspace string `json:"workspace"`
			} `json:"defaults"`
		} `json:"agents"`
		Gateway struct {
			Host string `json:"host"`
			Port int    `json:"port"`
		} `json:"gateway"`
		Events struct {
			Ingress struct {
				DatabasePath string `json:"database_path"`
			} `json:"ingress"`
		} `json:"events"`
	}
	if err = json.Unmarshal(raw, &configFile); err != nil {
		t.Fatalf("decode coverage config: %v", err)
	}
	wantWorkspace := filepath.Join(home, ".picoclaw", "workspace")
	wantDB := filepath.Join(wantWorkspace, "eventing", "events.db")
	if configFile.Agents.Defaults.Workspace != wantWorkspace ||
		configFile.Events.Ingress.DatabasePath != wantDB ||
		configFile.Gateway.Host != "127.0.0.1" || configFile.Gateway.Port <= 0 {
		t.Fatalf("coverage config escaped runtime: %#v", configFile)
	}
	if info, statErr := os.Stat(wantDB); statErr != nil || !info.Mode().IsRegular() {
		t.Fatalf("coverage event database = (%v, %v), want regular file", info, statErr)
	}
}

func knownAgentTempDirCleanupRaceOutput() string {
	return strings.Join([]string{
		"--- FAIL: TestRunWorkerPanicReleasesSessionTurnState (0.07s)",
		"    testing.go:1369: TempDir RemoveAll cleanup: unlinkat /tmp/TestRun/001/sessions: directory not empty",
		"FAIL",
		"FAIL\tgithub.com/sipeed/picoclaw/pkg/agent\t87.764s",
	}, "\n")
}

func knownWorkflowTempDirCleanupRaceOutput() string {
	return strings.Join([]string{
		"--- FAIL: TestHandleRunWorkflowStartsAsyncRun (0.08s)",
		"    testing.go:1369: TempDir RemoveAll cleanup: unlinkat /tmp/TestHandleRunWorkflowStartsAsyncRun123/001/workflow_runs/wr_abc123: directory not empty",
		"FAIL",
		"FAIL\tgithub.com/sipeed/picoclaw/web/backend/api\t109.578s",
	}, "\n")
}

func knownRepositoryModelEvaluationBatchCancellationRaceOutput() string {
	return strings.Join([]string{
		"--- FAIL: TestRepositoryModelEvaluationControllerBatchFailureAndRunningCancellation (0.11s)",
		"    --- FAIL: TestRepositoryModelEvaluationControllerBatchFailureAndRunningCancellation/running_cancellation (0.06s)",
		"        repository_model_evaluation_controller_test.go:1304: cancel status=409 body={\"code\":\"stale_repository_model_evaluation\",\"message\":\"invalid repository evaluation status transition\"}",
		"FAIL",
		"FAIL\tgithub.com/sipeed/picoclaw/web/backend/api\t203.005s",
	}, "\n")
}

func knownRepositoryModelEvaluationCancellationRestartRaceOutput() string {
	return strings.Join([]string{
		"--- FAIL: TestRepositoryModelEvaluationControllerCancellationAndRestart (0.06s)",
		"    repository_model_evaluations_test.go:859: cancel status=409 body={\"code\":\"stale_repository_model_evaluation\",\"message\":\"repository evaluation state changed\"}",
		"FAIL",
		"FAIL\tgithub.com/sipeed/picoclaw/web/backend/api\t203.005s",
	}, "\n")
}

func knownEvolutionDraftPersistenceTimeoutOutput() string {
	path := filepath.Join(
		os.TempDir(),
		"picoclaw-coverage-delta-123456789",
		"base-picoclaw-home",
		".tmp",
		"TestEvolutionBridge_DraftModeUsesProviderBackedDraftGenerator123",
		"001",
		"state",
		"evolution",
		"skill-drafts.json",
	)
	return strings.Join([]string{
		"--- FAIL: TestEvolutionBridge_DraftModeUsesProviderBackedDraftGenerator (1.00s)",
		"    evolution_bridge_test.go:596: timed out waiting for 1 drafts at " + path,
		"FAIL",
		"FAIL\tgithub.com/sipeed/picoclaw/pkg/agent\t88.000s",
	}, "\n")
}

func knownRepositoryReviewAutoContinueCompletionTimeoutOutput() string {
	return strings.Join([]string{
		"--- FAIL: TestRepositoryReviewAutomationAutoContinueReusesResolvedCommit (1.84s)",
		"    repository_review_automations_test.go:1897: automation rra_heceemhmds56guifrx3ifu2tnf did not reach completed",
		"FAIL",
		"FAIL\tgithub.com/sipeed/picoclaw/web/backend/api\t323.656s",
	}, "\n")
}

func TestKnownRepositoryReviewAutoContinueCompletionTimeout(t *testing.T) {
	valid := knownRepositoryReviewAutoContinueCompletionTimeoutOutput()
	if !isKnownRepositoryReviewAutoContinueCompletionTimeout([]byte(valid)) ||
		!isKnownCoverageBaselineFlake([]byte(valid)) {
		t.Fatal("exact repository review auto-continuation timeout was rejected")
	}
	tests := map[string]string{
		"wrong package": strings.Replace(
			valid,
			"github.com/sipeed/picoclaw/web/backend/api",
			"github.com/sipeed/picoclaw/web/backend",
			1,
		),
		"wrong test": strings.Replace(
			valid,
			"TestRepositoryReviewAutomationAutoContinueReusesResolvedCommit",
			"TestRepositoryReviewAutomationAutoContinueUsesLatestCommit",
			1,
		),
		"wrong file": strings.Replace(
			valid,
			"repository_review_automations_test.go",
			"repository_review_automation_test.go",
			1,
		),
		"wrong line": strings.Replace(
			valid,
			"repository_review_automations_test.go:1897:",
			"repository_review_automations_test.go:1898:",
			1,
		),
		"wrong status": strings.Replace(valid, "did not reach completed", "did not reach paused", 1),
		"wrong id prefix": strings.Replace(
			valid,
			"rra_heceemhmds56guifrx3ifu2tnf",
			"rrb_heceemhmds56guifrx3ifu2tnf",
			1,
		),
		"uppercase id": strings.Replace(
			valid,
			"rra_heceemhmds56guifrx3ifu2tnf",
			"rra_Heceemhmds56guifrx3ifu2tnf",
			1,
		),
		"invalid id alphabet": strings.Replace(
			valid,
			"rra_heceemhmds56guifrx3ifu2tnf",
			"rra_heceemhmds56guifrx3ifu2tn0",
			1,
		),
		"short id": strings.Replace(
			valid,
			"rra_heceemhmds56guifrx3ifu2tnf",
			"rra_heceemhmds56guifrx3ifu2tn",
			1,
		),
		"long id": strings.Replace(
			valid,
			"rra_heceemhmds56guifrx3ifu2tnf",
			"rra_heceemhmds56guifrx3ifu2tnfa",
			1,
		),
		"additional failure": valid +
			"\n--- FAIL: TestRepositoryReviewAutomationPause (0.01s)",
		"additional diagnostic": valid +
			"\n    repository_review_automations_test.go:1900: unrelated assertion",
		"additional package": valid +
			"\nFAIL\tgithub.com/sipeed/picoclaw/pkg/repoaudit\t0.01s",
		"panic": valid + "\npanic: concurrent test failure",
		"fatal": valid + "\nfatal error: concurrent map writes",
		"build failure": valid +
			"\nFAIL\tgithub.com/sipeed/picoclaw/web/backend/api [build failed]",
		"setup failure": valid +
			"\nFAIL\tgithub.com/sipeed/picoclaw/web/backend/api [setup failed]",
	}
	for name, output := range tests {
		t.Run(name, func(t *testing.T) {
			if isKnownRepositoryReviewAutoContinueCompletionTimeout([]byte(output)) ||
				isKnownCoverageBaselineFlake([]byte(output)) {
				t.Fatal("repository review auto-continuation timeout classifier accepted a near miss")
			}
		})
	}
}

func TestKnownEvolutionDraftPersistenceTimeout(t *testing.T) {
	valid := knownEvolutionDraftPersistenceTimeoutOutput()
	if !isKnownEvolutionDraftPersistenceTimeout([]byte(valid)) ||
		!isKnownCoverageBaselineFlake([]byte(valid)) {
		t.Fatal("exact evolution draft timeout was rejected")
	}
	tests := map[string]string{
		"wrong package": strings.Replace(
			valid,
			"github.com/sipeed/picoclaw/pkg/agent",
			"github.com/sipeed/picoclaw/pkg/agents",
			1,
		),
		"wrong test": strings.Replace(
			valid,
			"TestEvolutionBridge_DraftModeUsesProviderBackedDraftGenerator",
			"TestEvolutionBridge_DraftModeUsesFallbackGenerator",
			1,
		),
		"wrong line": strings.Replace(valid, "evolution_bridge_test.go:596:",
			"evolution_bridge_test.go:597:", 1),
		"wrong message":      strings.Replace(valid, "waiting for 1 drafts", "waiting for 2 drafts", 1),
		"additional failure": valid + "\n--- FAIL: TestEvolutionBridgeOther (0.01s)",
		"additional diagnostic": valid +
			"\n    evolution_bridge_test.go:600: unexpected persisted draft",
		"panic": valid + "\npanic: concurrent test failure",
		"relative path": strings.Replace(
			valid,
			filepath.Join(
				os.TempDir(), "picoclaw-coverage-delta-123456789", "base-picoclaw-home", ".tmp",
				"TestEvolutionBridge_DraftModeUsesProviderBackedDraftGenerator123", "001",
			),
			"relative",
			1,
		),
		"unclean path": strings.Replace(
			valid,
			filepath.Join(
				os.TempDir(), "picoclaw-coverage-delta-123456789", "base-picoclaw-home", ".tmp",
				"TestEvolutionBridge_DraftModeUsesProviderBackedDraftGenerator123", "001",
			),
			filepath.Join(os.TempDir(), "fixture")+string(filepath.Separator)+".."+
				string(filepath.Separator)+"escape",
			1,
		),
		"outside temp": strings.Replace(
			valid,
			filepath.Join(
				os.TempDir(), "picoclaw-coverage-delta-123456789", "base-picoclaw-home", ".tmp",
				"TestEvolutionBridge_DraftModeUsesProviderBackedDraftGenerator123", "001",
			),
			filepath.Join(filepath.Dir(os.TempDir()), "outside-evolution-fixture"),
			1,
		),
		"head coverage home": strings.Replace(
			valid,
			"base-picoclaw-home",
			"head-picoclaw-home",
			1,
		),
		"wrong test temp directory": strings.Replace(
			valid,
			string(filepath.Separator)+"TestEvolutionBridge_DraftModeUsesProviderBackedDraftGenerator123"+
				string(filepath.Separator),
			string(filepath.Separator)+"TestEvolutionBridge_DraftModeUsesProviderBackedDraftGeneratorOther123"+
				string(filepath.Separator),
			1,
		),
		"nonnumeric coverage sandbox": strings.Replace(
			valid,
			"picoclaw-coverage-delta-123456789",
			"picoclaw-coverage-delta-not-numeric",
			1,
		),
	}
	for name, output := range tests {
		t.Run(name, func(t *testing.T) {
			if isKnownEvolutionDraftPersistenceTimeout([]byte(output)) ||
				isKnownCoverageBaselineFlake([]byte(output)) {
				t.Fatal("evolution draft timeout classifier accepted a near miss")
			}
		})
	}
}

func TestKnownCoverageTempDirCleanupRace(t *testing.T) {
	agentCleanupRace := knownAgentTempDirCleanupRaceOutput()
	workflowCleanupRace := knownWorkflowTempDirCleanupRaceOutput()
	for _, test := range []struct {
		name   string
		output string
	}{
		{name: "agent", output: agentCleanupRace},
		{name: "workflow", output: workflowCleanupRace},
	} {
		t.Run(test.name, func(t *testing.T) {
			if !isKnownCoverageTempDirCleanupRace([]byte(test.output)) {
				t.Fatal("exact cleanup-only failure was rejected")
			}
		})
	}

	for _, test := range []struct {
		name   string
		output string
	}{
		{
			name: "different failing test",
			output: agentCleanupRace + "\n" +
				"--- FAIL: TestLeaseLoss (0.01s)",
		},
		{
			name: "agent plus another failed package",
			output: agentCleanupRace + "\n" +
				"FAIL\tgithub.com/sipeed/picoclaw/pkg/reviews\t0.01s",
		},
		{
			name: "workflow plus another failed package",
			output: workflowCleanupRace + "\n" +
				"FAIL\tgithub.com/sipeed/picoclaw/pkg/workflows\t0.01s",
		},
		{
			name: "extra cleanup failure",
			output: workflowCleanupRace + "\n" +
				"    testing.go:1369: TempDir RemoveAll cleanup: unlinkat /tmp/TestOther/001/cache: directory not empty",
		},
		{
			name: "agent functional assertion",
			output: agentCleanupRace + "\n" +
				"    agent_test.go:7683: second message did not start a new turn after panic cleanup",
		},
		{
			name: "workflow functional assertion",
			output: workflowCleanupRace + "\n" +
				"    workflow_ai_test.go:755: run status = running, want succeeded",
		},
		{
			name: "helper functional assertion",
			output: agentCleanupRace + "\n" +
				"    rescue_helper_test.go:42: cleanup invariant failed",
		},
		{
			name: "different agent temp directory",
			output: strings.Replace(
				agentCleanupRace,
				"sessions: directory not empty",
				"cache: directory not empty",
				1,
			),
		},
		{
			name: "agent path prefix collision",
			output: strings.Replace(
				agentCleanupRace,
				"/sessions: directory not empty",
				"/not-sessions: directory not empty",
				1,
			),
		},
		{
			name: "different workflow run directory",
			output: strings.Replace(
				workflowCleanupRace,
				"workflow_runs/wr_abc123",
				"workflow_runs/run_abc123",
				1,
			),
		},
		{
			name: "workflow path prefix collision",
			output: strings.Replace(
				workflowCleanupRace,
				"/workflow_runs/wr_abc123",
				"/other_workflow_runs/wr_abc123",
				1,
			),
		},
		{
			name: "nested workflow artifact",
			output: strings.Replace(
				workflowCleanupRace,
				"workflow_runs/wr_abc123: directory not empty",
				"workflow_runs/wr_abc123/artifacts: directory not empty",
				1,
			),
		},
		{
			name: "wrong agent package",
			output: strings.Replace(
				agentCleanupRace,
				"github.com/sipeed/picoclaw/pkg/agent",
				"github.com/sipeed/picoclaw/pkg/agents",
				1,
			),
		},
		{
			name: "wrong workflow package",
			output: strings.Replace(
				workflowCleanupRace,
				"github.com/sipeed/picoclaw/web/backend/api",
				"github.com/sipeed/picoclaw/pkg/workflows",
				1,
			),
		},
		{
			name:   "test timeout",
			output: agentCleanupRace + "\npanic: test timed out after 10m0s",
		},
		{
			name:   "fatal runtime error",
			output: agentCleanupRace + "\nfatal error: concurrent map writes",
		},
		{
			name:   "setup failure",
			output: agentCleanupRace + "\nFAIL\tother/package [setup failed]",
		},
		{
			name:   "build failure",
			output: agentCleanupRace + "\nFAIL\tother/package [build failed]",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			if isKnownCoverageTempDirCleanupRace([]byte(test.output)) {
				t.Fatal("cleanup classifier accepted an unrelated failure")
			}
		})
	}
}

func TestKnownCoverageTempDirCleanupRaceRejectsExtraTestingDiagnostic(t *testing.T) {
	for name, output := range map[string]string{
		"agent":    knownAgentTempDirCleanupRaceOutput(),
		"workflow": knownWorkflowTempDirCleanupRaceOutput(),
	} {
		t.Run(name, func(t *testing.T) {
			output += "\n    testing.go:1400: unrelated framework failure"
			if isKnownCoverageTempDirCleanupRace([]byte(output)) {
				t.Fatal("cleanup race with an extra testing diagnostic was accepted")
			}
			if isKnownCoverageBaselineFlake([]byte(output)) {
				t.Fatal("baseline classifier accepted an extra testing diagnostic")
			}
		})
	}
}

func TestKnownRepositoryModelEvaluationCancellationRace(t *testing.T) {
	batchCancellation := knownRepositoryModelEvaluationBatchCancellationRaceOutput()
	cancellationRestart := knownRepositoryModelEvaluationCancellationRestartRaceOutput()
	tests := []struct {
		name   string
		output string
		want   bool
	}{
		{
			name:   "batch running cancellation",
			output: batchCancellation,
			want:   true,
		},
		{
			name:   "cancellation and restart",
			output: cancellationRestart,
			want:   true,
		},
		{
			name: "additional failing test",
			output: batchCancellation + "\n" +
				"--- FAIL: TestRepositoryModelEvaluationControllerLeaseLoss (0.01s)",
		},
		{
			name: "additional test diagnostic",
			output: cancellationRestart + "\n" +
				"    repository_model_evaluations_test.go:900: unrelated assertion",
		},
		{
			name: "batch additional testing diagnostic",
			output: batchCancellation + "\n" +
				"    testing.go:1369: TempDir RemoveAll cleanup: directory not empty",
		},
		{
			name: "restart additional testing diagnostic",
			output: cancellationRestart + "\n" +
				"    testing.go:1369: TempDir RemoveAll cleanup: directory not empty",
		},
		{
			name: "additional failed package",
			output: cancellationRestart + "\n" +
				"FAIL\tgithub.com/sipeed/picoclaw/pkg/workflows\t0.01s",
		},
		{
			name: "wrong stale transition message",
			output: strings.Replace(
				batchCancellation,
				"invalid repository evaluation status transition",
				"repository evaluation state changed",
				1,
			),
		},
		{
			name: "wrong stale state message",
			output: strings.Replace(
				cancellationRestart,
				"repository evaluation state changed",
				"invalid repository evaluation status transition",
				1,
			),
		},
		{
			name: "wrong package",
			output: strings.Replace(
				batchCancellation,
				"github.com/sipeed/picoclaw/web/backend/api",
				"github.com/sipeed/picoclaw/pkg/repoeval",
				1,
			),
		},
		{
			name: "wrong subtest marker",
			output: strings.Replace(
				batchCancellation,
				"/running_cancellation",
				"/batch_failure",
				1,
			),
		},
		{
			name: "wrong diagnostic file",
			output: strings.Replace(
				cancellationRestart,
				"repository_model_evaluations_test.go",
				"repository_model_evaluation_controller_test.go",
				1,
			),
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := isKnownRepositoryModelEvaluationCancellationRace([]byte(test.output))
			if got != test.want {
				t.Fatalf(
					"isKnownRepositoryModelEvaluationCancellationRace() = %t, want %t",
					got,
					test.want,
				)
			}
			if got && !isKnownCoverageBaselineFlake([]byte(test.output)) {
				t.Fatal("recognized cancellation race was rejected by baseline classifier")
			}
		})
	}
}

func TestRunCoverageCommandRetriesOnlyKnownBaselineFlakes(t *testing.T) {
	batchCancellation := knownRepositoryModelEvaluationBatchCancellationRaceOutput()
	cancellationRestart := knownRepositoryModelEvaluationCancellationRestartRaceOutput()
	evolutionDraftTimeout := knownEvolutionDraftPersistenceTimeoutOutput()
	repositoryReviewAutoContinueTimeout := knownRepositoryReviewAutoContinueCompletionTimeoutOutput()
	tests := []struct {
		name         string
		label        string
		output       string
		wantRetried  bool
		wantAttempts int
	}{
		{
			name:         "base batch running cancellation",
			label:        "base",
			output:       batchCancellation,
			wantRetried:  true,
			wantAttempts: 2,
		},
		{
			name:         "base cancellation and restart",
			label:        "base",
			output:       cancellationRestart,
			wantRetried:  true,
			wantAttempts: 2,
		},
		{
			name:         "base evolution draft persistence timeout",
			label:        "base",
			output:       evolutionDraftTimeout,
			wantRetried:  true,
			wantAttempts: 2,
		},
		{
			name:         "base repository review auto-continuation completion timeout",
			label:        "base",
			output:       repositoryReviewAutoContinueTimeout,
			wantRetried:  true,
			wantAttempts: 2,
		},
		{
			name:  "base additional failure",
			label: "base",
			output: cancellationRestart + "\n" +
				"--- FAIL: TestRepositoryModelEvaluationControllerLeaseLoss (0.01s)",
			wantAttempts: 1,
		},
		{
			name:  "base wrong message",
			label: "base",
			output: strings.Replace(
				batchCancellation,
				"invalid repository evaluation status transition",
				"repository evaluation state changed",
				1,
			),
			wantAttempts: 1,
		},
		{
			name:  "base wrong package",
			label: "base",
			output: strings.Replace(
				cancellationRestart,
				"github.com/sipeed/picoclaw/web/backend/api",
				"github.com/sipeed/picoclaw/web/backend",
				1,
			),
			wantAttempts: 1,
		},
		{
			name:         "head batch running cancellation",
			label:        "head",
			output:       batchCancellation,
			wantAttempts: 1,
		},
		{
			name:         "head cancellation and restart",
			label:        "head",
			output:       cancellationRestart,
			wantAttempts: 1,
		},
		{
			name:         "head evolution draft persistence timeout",
			label:        "head",
			output:       evolutionDraftTimeout,
			wantAttempts: 1,
		},
		{
			name:         "head repository review auto-continuation completion timeout",
			label:        "head",
			output:       repositoryReviewAutoContinueTimeout,
			wantAttempts: 1,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			wantErr := errors.New("exit status 1")
			attempts := 0
			out, err, retried := runCoverageCommandWithBaselineRetry(
				test.label,
				func() ([]byte, error) {
					attempts++
					if attempts == 1 {
						return []byte(test.output), wantErr
					}
					return []byte("ok"), nil
				},
			)
			if retried != test.wantRetried || attempts != test.wantAttempts {
				t.Fatalf(
					"runCoverageCommandWithBaselineRetry() retried=%t attempts=%d, want retried=%t attempts=%d",
					retried,
					attempts,
					test.wantRetried,
					test.wantAttempts,
				)
			}
			if test.wantRetried {
				if err != nil || string(out) != "ok" {
					t.Fatalf("retry result = (%q, %v), want (ok, nil)", out, err)
				}
				return
			}
			if !errors.Is(err, wantErr) || string(out) != test.output {
				t.Fatalf("non-retry result = (%q, %v), want original failure", out, err)
			}
		})
	}
}

func TestRunCoverageCommandRetriesKnownCleanupRaceOnce(t *testing.T) {
	for _, test := range []struct {
		name   string
		output string
	}{
		{name: "agent", output: knownAgentTempDirCleanupRaceOutput()},
		{name: "workflow", output: knownWorkflowTempDirCleanupRaceOutput()},
	} {
		t.Run(test.name, func(t *testing.T) {
			attempts := 0
			out, err, retried := runCoverageCommandWithBaselineRetry(
				"base",
				func() ([]byte, error) {
					attempts++
					if attempts == 1 {
						return []byte(test.output), errors.New("exit status 1")
					}
					return []byte("ok"), nil
				},
			)
			if err != nil || string(out) != "ok" || !retried || attempts != 2 {
				t.Fatalf(
					"runCoverageCommandWithBaselineRetry() = (%q, %v, %t), attempts = %d",
					out,
					err,
					retried,
					attempts,
				)
			}
		})
	}
}

func TestRunCoverageCommandDoesNotRetryHeadCleanupRace(t *testing.T) {
	for _, test := range []struct {
		name   string
		output string
	}{
		{name: "agent", output: knownAgentTempDirCleanupRaceOutput()},
		{name: "workflow", output: knownWorkflowTempDirCleanupRaceOutput()},
	} {
		t.Run(test.name, func(t *testing.T) {
			wantErr := errors.New("exit status 1")
			attempts := 0
			out, err, retried := runCoverageCommandWithBaselineRetry(
				"head",
				func() ([]byte, error) {
					attempts++
					return []byte(test.output), wantErr
				},
			)
			if !errors.Is(err, wantErr) || string(out) != test.output ||
				retried || attempts != 1 {
				t.Fatalf(
					"runCoverageCommandWithBaselineRetry() = (%q, %v, %t), attempts = %d",
					out,
					err,
					retried,
					attempts,
				)
			}
		})
	}
}

func TestRunCoverageCommandPreservesFunctionalSecondAttempt(t *testing.T) {
	wantErr := errors.New("exit status 1")
	wantOutput := "--- FAIL: TestLeaseLoss (0.01s)"
	attempts := 0
	out, err, retried := runCoverageCommandWithBaselineRetry(
		"base",
		func() ([]byte, error) {
			attempts++
			if attempts == 1 {
				return []byte(knownAgentTempDirCleanupRaceOutput()), wantErr
			}
			return []byte(wantOutput), wantErr
		},
	)
	if !errors.Is(err, wantErr) || string(out) != wantOutput ||
		!retried || attempts != 2 {
		t.Fatalf(
			"runCoverageCommandWithBaselineRetry() = (%q, %v, %t), attempts = %d",
			out,
			err,
			retried,
			attempts,
		)
	}
}

func TestRunCoverageCommandDoesNotRetryKnownCleanupRaceTwice(t *testing.T) {
	cleanupRace := []byte(knownAgentTempDirCleanupRaceOutput())
	wantErr := errors.New("exit status 1")
	attempts := 0
	out, err, retried := runCoverageCommandWithBaselineRetry(
		"base",
		func() ([]byte, error) {
			attempts++
			return cleanupRace, wantErr
		},
	)
	if !errors.Is(err, wantErr) || string(out) != string(cleanupRace) ||
		!retried || attempts != 2 {
		t.Fatalf(
			"runCoverageCommandWithBaselineRetry() = (%q, %v, %t), attempts = %d",
			out,
			err,
			retried,
			attempts,
		)
	}
}

func TestRunCoverageCommandDoesNotRetryUnrelatedFailure(t *testing.T) {
	wantErr := errors.New("exit status 1")
	attempts := 0
	out, err, retried := runCoverageCommandWithBaselineRetry(
		"base",
		func() ([]byte, error) {
			attempts++
			return []byte("--- FAIL: TestLeaseLoss (0.01s)"), wantErr
		},
	)
	if !errors.Is(err, wantErr) || string(out) != "--- FAIL: TestLeaseLoss (0.01s)" ||
		retried || attempts != 1 {
		t.Fatalf(
			"runCoverageCommandWithBaselineRetry() = (%q, %v, %t), attempts = %d",
			out,
			err,
			retried,
			attempts,
		)
	}
}

func TestTrimCommandOutputPreservesFailureContextAndTail(t *testing.T) {
	failure := "--- FAIL: TestLeaseLoss (0.01s)\n" +
		"    worker_test.go:42: lease expired before admission\n" +
		"FAIL\nFAIL\tgithub.com/sipeed/picoclaw/pkg/reviews\t0.01s\n"
	largeContextLine := strings.Repeat("successful package with verbose coverage ", 60) + "\n"
	output := strings.Repeat("earlier output\n", 500) + strings.Repeat(largeContextLine, 6) + failure +
		strings.Repeat("later successful package with verbose coverage\n", 500) + "final output line"

	trimmed := trimCommandOutput([]byte(output))
	if len(trimmed) > 12000 {
		t.Fatalf("trimmed output length = %d, want at most 12000", len(trimmed))
	}
	for _, want := range []string{
		"failure markers:",
		"--- FAIL: TestLeaseLoss",
		"worker_test.go:42: lease expired before admission",
		"FAIL\tgithub.com/sipeed/picoclaw/pkg/reviews",
		"command output truncated; failure context preserved above",
		"final output line",
	} {
		if !strings.Contains(trimmed, want) {
			t.Fatalf("trimmed output does not contain %q:\n%s", want, trimmed)
		}
	}
	if !utf8.ValidString(trimmed) {
		t.Fatal("trimmed output is not valid UTF-8")
	}
}

func TestTrimCommandOutputPreservesBuildAndPanicFailures(t *testing.T) {
	tests := []struct {
		name   string
		output string
		want   []string
	}{
		{
			name: "build failure",
			output: "# github.com/sipeed/picoclaw/pkg/example\n" +
				"pkg/example/example.go:42:9: undefined: missingSymbol\n" +
				"FAIL\tgithub.com/sipeed/picoclaw/pkg/example [build failed]\n",
			want: []string{"undefined: missingSymbol", "FAIL\tgithub.com/sipeed/picoclaw/pkg/example [build failed]"},
		},
		{
			name: "test timeout",
			output: "panic: test timed out after 10m0s\n" +
				"running tests:\n\tTestBlocked (10m0s)\n" +
				"FAIL\tgithub.com/sipeed/picoclaw/pkg/example\t600.00s\n",
			want: []string{
				"panic: test timed out after 10m0s",
				"TestBlocked",
				"FAIL\tgithub.com/sipeed/picoclaw/pkg/example",
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			largeLine := strings.Repeat("coverage context π ", 150) + "\n"
			output := strings.Repeat("earlier output\n", 500) +
				strings.Repeat(largeLine, 6) + test.output +
				strings.Repeat("later output\n", 1000) + "final output line"
			trimmed := trimCommandOutput([]byte(output))
			if len(trimmed) > 12000 {
				t.Fatalf("trimmed output length = %d, want at most 12000", len(trimmed))
			}
			for _, want := range append(test.want, "final output line") {
				if !strings.Contains(trimmed, want) {
					t.Fatalf("trimmed output does not contain %q:\n%s", want, trimmed)
				}
			}
			if !utf8.ValidString(trimmed) {
				t.Fatal("trimmed output is not valid UTF-8")
			}
		})
	}
}

func TestCommandOutputClippingPreservesUTF8Boundaries(t *testing.T) {
	line := strings.Repeat("a", 252) + "π" + strings.Repeat("b", 600)
	clipped := clipCommandFailureLine(line)
	if !utf8.ValidString(clipped) {
		t.Fatalf("clipped failure line is not valid UTF-8: %q", clipped)
	}
	if tail := commandOutputTail("aπbc", 3); tail != "bc" || !utf8.ValidString(tail) {
		t.Fatalf("commandOutputTail() = %q, want valid UTF-8 %q", tail, "bc")
	}
}

func assertEnvironmentValue(t *testing.T, environment []string, name, want string) {
	t.Helper()
	var values []string
	for _, entry := range environment {
		entryName, value, ok := strings.Cut(entry, "=")
		if ok && strings.EqualFold(entryName, name) {
			values = append(values, value)
		}
	}
	if len(values) != 1 || values[0] != want {
		t.Fatalf("environment %s values = %#v, want [%q]", name, values, want)
	}
}

func assertEnvironmentMissing(t *testing.T, environment []string, name string) {
	t.Helper()
	for _, entry := range environment {
		entryName, _, ok := strings.Cut(entry, "=")
		if ok && strings.EqualFold(entryName, name) {
			t.Fatalf("environment unexpectedly contains %s", name)
		}
	}
}
