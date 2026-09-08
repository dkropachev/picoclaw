package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/database"
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

type backfillCommand struct {
	workspace      string
	automationID   string
	apply          bool
	expectedDigest string
	expected       expectedCounts
}

func main() {
	if err := run(os.Args[1:], os.Stdout, os.Stderr); err != nil {
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
) error {
	if stdout == nil || stderr == nil || backfillFileAttributions == nil {
		return errors.New("command output and backfill operation are required")
	}
	command, err := parseBackfillCommand(args, stderr)
	if err != nil {
		return err
	}
	cleanup, err := prepareBackfillDatabase(context.Background())
	if err != nil {
		return err
	}
	if cleanup != nil {
		defer cleanup()
	}
	return executeBackfillCommand(command, stdout, backfillFileAttributions)
}

func parseBackfillCommand(args []string, stderr io.Writer) (backfillCommand, error) {
	if stderr == nil {
		return backfillCommand{}, errors.New("command error output is required")
	}
	command := backfillCommand{}
	flags := flag.NewFlagSet("repository-review-attribution-backfill", flag.ContinueOnError)
	flags.SetOutput(stderr)
	flags.StringVar(&command.workspace, "workspace", "", "PicoClaw workspace containing repository review state")
	flags.StringVar(&command.automationID, "automation", "", "repository review automation ID")
	flags.BoolVar(
		&command.apply,
		"apply", false,
		"commit prepared attribution records and eligible recovered-campaign credits",
	)
	flags.StringVar(
		&command.expectedDigest,
		"expect-digest",
		"",
		"exact sha256 digest printed by a prior dry run",
	)
	flags.IntVar(&command.expected.configuredRuns, "expect-configured-runs", -1, "expected configured workflow runs")
	flags.IntVar(&command.expected.recoveredRuns, "expect-recovered-runs", -1, "expected retained ledger runs")
	flags.IntVar(&command.expected.nonLedgerRuns, "expect-non-ledger-runs", -1, "expected allowed pre-review runs")
	flags.IntVar(&command.expected.childAttempts, "expect-child-attempts", -1, "expected managed child attempts")
	flags.IntVar(&command.expected.successfulChildren, "expect-successful-children", -1, "expected successful children")
	flags.IntVar(&command.expected.failedChildren, "expect-failed-children", -1, "expected failed children")
	flags.IntVar(&command.expected.attributionRecords, "expect-attribution-records", -1, "expected grouped attribution records")
	flags.IntVar(&command.expected.acknowledgements, "expect-acknowledgements", -1, "expected acknowledged file occurrences")
	flags.IntVar(&command.expected.uniqueFiles, "expect-unique-files", -1, "expected unique files")
	flags.IntVar(
		&command.expected.uniqueFileAssignments,
		"expect-file-assignments",
		-1,
		"expected unique file/focus assignments",
	)
	flags.IntVar(
		&command.expected.campaignAssignmentCredits,
		"expect-campaign-assignment-credits",
		-1,
		"expected exact legacy credits mapped into the current campaign",
	)
	flags.IntVar(
		&command.expected.campaignAttributedFiles,
		"expect-campaign-attributed-files",
		-1,
		"expected exact files carrying legacy attribution credit",
	)
	flags.IntVar(
		&command.expected.projectedCompletedAssignments,
		"expect-projected-completed-assignments",
		-1,
		"expected total completed assignments after repair",
	)
	flags.IntVar(
		&command.expected.projectedPendingAssignments,
		"expect-projected-pending-assignments",
		-1,
		"expected total pending assignments after repair",
	)
	flags.IntVar(
		&command.expected.projectedInspectedFiles,
		"expect-projected-inspected-files",
		-1,
		"expected total inspected files after repair",
	)
	flags.IntVar(
		&command.expected.projectedCompletedFiles,
		"expect-projected-completed-files",
		-1,
		"expected total fully reviewed files after repair",
	)
	if err := flags.Parse(args); err != nil {
		return backfillCommand{}, err
	}

	command.workspace = strings.TrimSpace(command.workspace)
	command.automationID = strings.TrimSpace(command.automationID)
	command.expectedDigest = strings.TrimSpace(command.expectedDigest)
	if command.workspace == "" || command.automationID == "" {
		return backfillCommand{}, errors.New("--workspace and --automation are required")
	}
	return command, nil
}

func executeBackfillCommand(
	command backfillCommand,
	stdout io.Writer,
	backfill backfillFileAttributionsFunc,
) error {
	if stdout == nil || backfill == nil {
		return errors.New("command output and backfill operation are required")
	}
	report, err := backfill(
		context.Background(),
		command.workspace,
		command.automationID,
		launcherapi.RepositoryReviewFileAttributionBackfillOptions{},
	)
	if err != nil {
		return err
	}
	if command.apply {
		if command.expectedDigest == "" {
			return errors.New("--apply requires --expect-digest from a prior dry run")
		}
		if compareErr := compareExpectedCounts(report, command.expected); compareErr != nil {
			return compareErr
		}
		report, err = backfill(
			context.Background(),
			command.workspace,
			command.automationID,
			launcherapi.RepositoryReviewFileAttributionBackfillOptions{
				Apply: true, ExpectedDigest: command.expectedDigest,
			},
		)
		if err != nil {
			return err
		}
		if compareErr := compareExpectedCounts(report, command.expected); compareErr != nil {
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

func prepareBackfillDatabase(ctx context.Context) (func(), error) {
	home, err := database.PrepareHome(config.GetHome())
	if err != nil {
		return nil, err
	}
	executable := strings.TrimSpace(os.Getenv("PICOCLAW_EXECUTABLE"))
	if executable == "" {
		executable, err = exec.LookPath("picoclaw")
		if err != nil {
			return nil, errors.New("picoclaw executable is required to start the database supervisor")
		}
	}
	configPath := strings.TrimSpace(os.Getenv(config.EnvConfig))
	if configPath == "" {
		configPath = filepath.Join(home, "config.json")
	}
	client, err := database.EnsureSupervisor(ctx, database.EnsureOptions{
		Home: home, Executable: executable, ConfigPath: configPath,
	})
	if err != nil {
		return nil, err
	}
	fence, err := database.AcquireOnlineFence(home)
	if err != nil {
		return nil, err
	}
	database.InstallProcessClient(client)
	return func() {
		database.InstallProcessClient(nil)
		_ = fence.Close()
	}, nil
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
