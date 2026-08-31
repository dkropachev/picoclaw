package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"

	launcherapi "github.com/sipeed/picoclaw/web/backend/api"
)

type expectedCounts struct {
	configuredRuns        int
	recoveredRuns         int
	nonLedgerRuns         int
	childAttempts         int
	successfulChildren    int
	failedChildren        int
	attributionRecords    int
	acknowledgements      int
	uniqueFiles           int
	uniqueFileAssignments int
}

func main() {
	workspace := flag.String("workspace", "", "PicoClaw workspace containing repository review state")
	automationID := flag.String("automation", "", "repository review automation ID")
	apply := flag.Bool("apply", false, "commit the prepared attribution records")
	expectedDigest := flag.String("expect-digest", "", "exact sha256 digest printed by a prior dry run")
	expected := expectedCounts{}
	flag.IntVar(&expected.configuredRuns, "expect-configured-runs", -1, "expected configured workflow runs")
	flag.IntVar(&expected.recoveredRuns, "expect-recovered-runs", -1, "expected retained ledger runs")
	flag.IntVar(&expected.nonLedgerRuns, "expect-non-ledger-runs", -1, "expected allowed pre-review runs")
	flag.IntVar(&expected.childAttempts, "expect-child-attempts", -1, "expected managed child attempts")
	flag.IntVar(&expected.successfulChildren, "expect-successful-children", -1, "expected successful children")
	flag.IntVar(&expected.failedChildren, "expect-failed-children", -1, "expected failed children")
	flag.IntVar(&expected.attributionRecords, "expect-attribution-records", -1, "expected grouped attribution records")
	flag.IntVar(&expected.acknowledgements, "expect-acknowledgements", -1, "expected acknowledged file occurrences")
	flag.IntVar(&expected.uniqueFiles, "expect-unique-files", -1, "expected unique files")
	flag.IntVar(
		&expected.uniqueFileAssignments,
		"expect-file-assignments",
		-1,
		"expected unique file/focus assignments",
	)
	flag.Parse()

	*workspace = strings.TrimSpace(*workspace)
	*automationID = strings.TrimSpace(*automationID)
	*expectedDigest = strings.TrimSpace(*expectedDigest)
	if *workspace == "" || *automationID == "" {
		fatal(errors.New("--workspace and --automation are required"))
	}
	report, err := launcherapi.BackfillRepositoryReviewFileAttributions(
		context.Background(),
		*workspace,
		*automationID,
		launcherapi.RepositoryReviewFileAttributionBackfillOptions{},
	)
	if err != nil {
		fatal(err)
	}
	if *apply {
		if *expectedDigest == "" {
			fatal(errors.New("--apply requires --expect-digest from a prior dry run"))
		}
		if compareErr := compareExpectedCounts(report, expected); compareErr != nil {
			fatal(compareErr)
		}
		report, err = launcherapi.BackfillRepositoryReviewFileAttributions(
			context.Background(),
			*workspace,
			*automationID,
			launcherapi.RepositoryReviewFileAttributionBackfillOptions{
				Apply: true, ExpectedDigest: *expectedDigest,
			},
		)
		if err != nil {
			fatal(err)
		}
		if compareErr := compareExpectedCounts(report, expected); compareErr != nil {
			fatal(compareErr)
		}
	}
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(report); err != nil {
		fatal(err)
	}
}

func compareExpectedCounts(
	report launcherapi.RepositoryReviewFileAttributionBackfillReport,
	expected expectedCounts,
) error {
	checks := []struct {
		name string
		got  int
		want int
	}{
		{"configured runs", report.ConfiguredRuns, expected.configuredRuns},
		{"recovered runs", report.RecoveredRuns, expected.recoveredRuns},
		{"non-ledger runs", report.AllowedNonLedgerRuns, expected.nonLedgerRuns},
		{"child attempts", report.ChildAttempts, expected.childAttempts},
		{"successful children", report.SuccessfulChildren, expected.successfulChildren},
		{"failed children", report.FailedChildren, expected.failedChildren},
		{"attribution records", report.AttributionRecords, expected.attributionRecords},
		{"acknowledgements", report.AcknowledgementOccurrences, expected.acknowledgements},
		{"unique files", report.UniqueFiles, expected.uniqueFiles},
		{"file assignments", report.UniqueFileAssignments, expected.uniqueFileAssignments},
	}
	for _, check := range checks {
		if check.want < 0 {
			return fmt.Errorf("--apply requires an expectation for %s", check.name)
		}
		if check.got != check.want {
			return fmt.Errorf("%s changed: got %d, expected %d", check.name, check.got, check.want)
		}
	}
	return nil
}

func fatal(err error) {
	_, _ = fmt.Fprintln(os.Stderr, err)
	os.Exit(1)
}
