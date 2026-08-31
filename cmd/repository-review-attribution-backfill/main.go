package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	launcherapi "github.com/sipeed/picoclaw/web/backend/api"
)

type expectedCounts struct {
	configuredRuns                int
	recoveredRuns                 int
	nonLedgerRuns                 int
	childAttempts                 int
	successfulChildren            int
	failedChildren                int
	attributionRecords            int
	acknowledgements              int
	uniqueFiles                   int
	uniqueFileAssignments         int
	campaignAssignmentCredits     int
	campaignAttributedFiles       int
	projectedCompletedAssignments int
	projectedPendingAssignments   int
	projectedInspectedFiles       int
	projectedCompletedFiles       int
}

func main() {
	if err := run(os.Args[1:], os.Stdout, os.Stderr, backfillFileAttributions); err != nil {
		fatal(err)
	}
}

type backfillFileAttributionsFunc func(
	context.Context,
	string,
	string,
	launcherapi.RepositoryReviewFileAttributionBackfillOptions,
) (launcherapi.RepositoryReviewFileAttributionBackfillReport, error)

var (
	backfillFileAttributions backfillFileAttributionsFunc = launcherapi.BackfillRepositoryReviewFileAttributions
	exitProcess                                           = os.Exit
)

func run(
	args []string,
	stdout io.Writer,
	stderr io.Writer,
	backfill backfillFileAttributionsFunc,
) error {
	if stdout == nil || stderr == nil || backfill == nil {
		return errors.New("command output and backfill operation are required")
	}
	flags := flag.NewFlagSet("repository-review-attribution-backfill", flag.ContinueOnError)
	flags.SetOutput(stderr)
	workspace := flags.String("workspace", "", "PicoClaw workspace containing repository review state")
	automationID := flags.String("automation", "", "repository review automation ID")
	apply := flags.Bool(
		"apply", false,
		"commit prepared attribution records and eligible recovered-campaign credits",
	)
	expectedDigest := flags.String("expect-digest", "", "exact sha256 digest printed by a prior dry run")
	expected := expectedCounts{}
	flags.IntVar(&expected.configuredRuns, "expect-configured-runs", -1, "expected configured workflow runs")
	flags.IntVar(&expected.recoveredRuns, "expect-recovered-runs", -1, "expected retained ledger runs")
	flags.IntVar(&expected.nonLedgerRuns, "expect-non-ledger-runs", -1, "expected allowed pre-review runs")
	flags.IntVar(&expected.childAttempts, "expect-child-attempts", -1, "expected managed child attempts")
	flags.IntVar(&expected.successfulChildren, "expect-successful-children", -1, "expected successful children")
	flags.IntVar(&expected.failedChildren, "expect-failed-children", -1, "expected failed children")
	flags.IntVar(&expected.attributionRecords, "expect-attribution-records", -1, "expected grouped attribution records")
	flags.IntVar(&expected.acknowledgements, "expect-acknowledgements", -1, "expected acknowledged file occurrences")
	flags.IntVar(&expected.uniqueFiles, "expect-unique-files", -1, "expected unique files")
	flags.IntVar(
		&expected.uniqueFileAssignments,
		"expect-file-assignments",
		-1,
		"expected unique file/focus assignments",
	)
	flags.IntVar(
		&expected.campaignAssignmentCredits,
		"expect-campaign-assignment-credits",
		-1,
		"expected exact legacy credits mapped into the current campaign",
	)
	flags.IntVar(
		&expected.campaignAttributedFiles,
		"expect-campaign-attributed-files",
		-1,
		"expected exact files carrying legacy attribution credit",
	)
	flags.IntVar(
		&expected.projectedCompletedAssignments,
		"expect-projected-completed-assignments",
		-1,
		"expected total completed assignments after repair",
	)
	flags.IntVar(
		&expected.projectedPendingAssignments,
		"expect-projected-pending-assignments",
		-1,
		"expected total pending assignments after repair",
	)
	flags.IntVar(
		&expected.projectedInspectedFiles,
		"expect-projected-inspected-files",
		-1,
		"expected total inspected files after repair",
	)
	flags.IntVar(
		&expected.projectedCompletedFiles,
		"expect-projected-completed-files",
		-1,
		"expected total fully reviewed files after repair",
	)
	if err := flags.Parse(args); err != nil {
		return err
	}

	*workspace = strings.TrimSpace(*workspace)
	*automationID = strings.TrimSpace(*automationID)
	*expectedDigest = strings.TrimSpace(*expectedDigest)
	if *workspace == "" || *automationID == "" {
		return errors.New("--workspace and --automation are required")
	}
	report, err := backfill(
		context.Background(),
		*workspace,
		*automationID,
		launcherapi.RepositoryReviewFileAttributionBackfillOptions{},
	)
	if err != nil {
		return err
	}
	if *apply {
		if *expectedDigest == "" {
			return errors.New("--apply requires --expect-digest from a prior dry run")
		}
		if compareErr := compareExpectedCounts(report, expected); compareErr != nil {
			return compareErr
		}
		report, err = backfill(
			context.Background(),
			*workspace,
			*automationID,
			launcherapi.RepositoryReviewFileAttributionBackfillOptions{
				Apply: true, ExpectedDigest: *expectedDigest,
			},
		)
		if err != nil {
			return err
		}
		if compareErr := compareExpectedCounts(report, expected); compareErr != nil {
			return compareErr
		}
	}
	encoder := json.NewEncoder(stdout)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(report); err != nil {
		return err
	}
	return nil
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
		{
			"campaign assignment credits",
			report.CampaignAssignmentCredits,
			expected.campaignAssignmentCredits,
		},
		{
			"campaign attributed files",
			report.CampaignAttributedFiles,
			expected.campaignAttributedFiles,
		},
		{
			"projected completed assignments",
			report.ProjectedCompletedAssignments,
			expected.projectedCompletedAssignments,
		},
		{
			"projected pending assignments",
			report.ProjectedPendingAssignments,
			expected.projectedPendingAssignments,
		},
		{
			"projected inspected files",
			report.ProjectedInspectedFiles,
			expected.projectedInspectedFiles,
		},
		{
			"projected completed files",
			report.ProjectedCompletedFiles,
			expected.projectedCompletedFiles,
		},
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
	exitProcess(1)
}
