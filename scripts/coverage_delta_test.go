//go:build featuretools

package main

import (
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"
)

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
	assertEnvironmentValue(t, baseEnvironment, "PICOCLAW_HOME", "")
	assertEnvironmentValue(t, headEnvironment, "PICOCLAW_HOME", "")
	assertEnvironmentValue(t, baseEnvironment, "GOCACHE", "/cache/build")
	assertEnvironmentValue(t, baseEnvironment, "GOMODCACHE", "/cache/modules")
	assertEnvironmentValue(t, baseEnvironment, "GOTOOLCHAIN", "auto")
	assertEnvironmentValue(t, baseEnvironment, "PATH", "/bin")
	assertEnvironmentValue(t, baseEnvironment, "VALUE", "with=equals")
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
