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

func TestRunCoverageCommandRetriesAnyBaseFailureOnce(t *testing.T) {
	wantErr := errors.New("exit status 1")
	attempts := 0
	out, err, retried := runCoverageCommandWithBaselineRetry(
		"base",
		func() ([]byte, error) {
			attempts++
			if attempts == 1 {
				return []byte("unclassified baseline failure"), wantErr
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
}

func TestRunCoverageCommandDoesNotRetryHeadFailure(t *testing.T) {
	wantErr := errors.New("exit status 1")
	attempts := 0
	out, err, retried := runCoverageCommandWithBaselineRetry(
		"head",
		func() ([]byte, error) {
			attempts++
			return []byte("head failure"), wantErr
		},
	)
	if !errors.Is(err, wantErr) || string(out) != "head failure" || retried || attempts != 1 {
		t.Fatalf(
			"runCoverageCommandWithBaselineRetry() = (%q, %v, %t), attempts = %d",
			out,
			err,
			retried,
			attempts,
		)
	}
}

func TestRunCoverageCommandDoesNotRetrySuccessfulBase(t *testing.T) {
	attempts := 0
	out, err, retried := runCoverageCommandWithBaselineRetry(
		"base",
		func() ([]byte, error) {
			attempts++
			return []byte("ok"), nil
		},
	)
	if err != nil || string(out) != "ok" || retried || attempts != 1 {
		t.Fatalf(
			"runCoverageCommandWithBaselineRetry() = (%q, %v, %t), attempts = %d",
			out,
			err,
			retried,
			attempts,
		)
	}
}

func TestRunCoverageCommandFailsDeterministicBaseFailureAfterOneRetry(t *testing.T) {
	firstErr := errors.New("first exit status 1")
	secondErr := errors.New("second exit status 1")
	attempts := 0
	out, err, retried := runCoverageCommandWithBaselineRetry(
		"base",
		func() ([]byte, error) {
			attempts++
			if attempts == 1 {
				return []byte("first baseline failure"), firstErr
			}
			return []byte("second baseline failure"), secondErr
		},
	)
	if !errors.Is(err, secondErr) || string(out) != "second baseline failure" ||
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
