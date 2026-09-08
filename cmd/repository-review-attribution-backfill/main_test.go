package main

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	launcherapi "github.com/sipeed/picoclaw/web/backend/api"
)

func runBackfillForTest(
	args []string,
	stdout io.Writer,
	stderr io.Writer,
	backfill backfillFileAttributionsFunc,
) error {
	if stdout == nil || stderr == nil || backfill == nil {
		return errors.New("command output and backfill operation are required")
	}
	command, err := parseBackfillCommand(args, stderr)
	if err != nil {
		return err
	}
	return executeBackfillCommand(command, stdout, backfill)
}

func TestRunValidatesArgumentsBeforeDatabasePreparation(t *testing.T) {
	home := filepath.Join(t.TempDir(), "must-not-be-created")
	t.Setenv("PICOCLAW_HOME", home)
	t.Setenv("PICOCLAW_EXECUTABLE", filepath.Join(home, "missing-picoclaw"))
	for _, test := range []struct {
		name string
		args []string
		want string
		help bool
	}{
		{name: "unknown flag", args: []string{"--unknown"}, want: "flag"},
		{name: "missing identity", want: "--workspace and --automation are required"},
		{name: "help", args: []string{"--help"}, help: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := run(test.args, io.Discard, io.Discard)
			if test.help {
				if !errors.Is(err, flag.ErrHelp) {
					t.Fatalf("help error = %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("validation error = %v, want %q", err, test.want)
			}
		})
	}
	if _, err := os.Lstat(home); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("argument validation prepared database home: %v", err)
	}
}

func TestRunDryAndApply(t *testing.T) {
	report := launcherapi.RepositoryReviewFileAttributionBackfillReport{
		AutomationID: "rra_test", Repository: "owner/repo",
		ConfiguredRuns: 1, RecoveredRuns: 1, ChildAttempts: 4,
		SuccessfulChildren: 1, FailedChildren: 3, AttributionRecords: 1,
		AcknowledgementOccurrences: 2, UniqueFiles: 2, UniqueFileAssignments: 2,
		Digest: "sha256:digest",
	}
	var calls []launcherapi.RepositoryReviewFileAttributionBackfillOptions
	backfill := func(
		_ context.Context,
		workspace string,
		automationID string,
		options launcherapi.RepositoryReviewFileAttributionBackfillOptions,
	) (launcherapi.RepositoryReviewFileAttributionBackfillReport, error) {
		if workspace != "/workspace" || automationID != "rra_test" {
			t.Fatalf("backfill identity=%q/%q", workspace, automationID)
		}
		calls = append(calls, options)
		result := report
		result.Applied = options.Apply
		return result, nil
	}
	var stdout, stderr bytes.Buffer
	if err := runBackfillForTest(
		[]string{"--workspace", " /workspace ", "--automation", " rra_test "},
		&stdout, &stderr, backfill,
	); err != nil || len(calls) != 1 || calls[0].Apply ||
		!strings.Contains(stdout.String(), `"digest": "sha256:digest"`) || stderr.Len() != 0 {
		t.Fatalf("dry run calls=%#v stdout=%q stderr=%q err=%v", calls, stdout.String(), stderr.String(), err)
	}

	calls = nil
	stdout.Reset()
	args := []string{
		"--workspace=/workspace", "--automation=rra_test", "--apply",
		"--expect-digest=sha256:digest",
		"--expect-configured-runs=1", "--expect-recovered-runs=1",
		"--expect-non-ledger-runs=0", "--expect-child-attempts=4",
		"--expect-successful-children=1", "--expect-failed-children=3",
		"--expect-attribution-records=1", "--expect-acknowledgements=2",
		"--expect-unique-files=2", "--expect-file-assignments=2",
		"--expect-campaign-assignment-credits=0", "--expect-campaign-attributed-files=0",
		"--expect-projected-completed-assignments=0", "--expect-projected-pending-assignments=0",
		"--expect-projected-inspected-files=0", "--expect-projected-completed-files=0",
	}
	if err := runBackfillForTest(args, &stdout, &stderr, backfill); err != nil || len(calls) != 2 ||
		calls[0].Apply || !calls[1].Apply || calls[1].ExpectedDigest != report.Digest ||
		!strings.Contains(stdout.String(), `"applied": true`) {
		t.Fatalf("apply calls=%#v stdout=%q err=%v", calls, stdout.String(), err)
	}
}

func TestRunErrors(t *testing.T) {
	report := launcherapi.RepositoryReviewFileAttributionBackfillReport{
		ConfiguredRuns: 1, RecoveredRuns: 1,
	}
	boom := errors.New("backfill failed")
	succeed := func(
		context.Context,
		string,
		string,
		launcherapi.RepositoryReviewFileAttributionBackfillOptions,
	) (launcherapi.RepositoryReviewFileAttributionBackfillReport, error) {
		return report, nil
	}
	tests := []struct {
		name     string
		args     []string
		stdout   io.Writer
		stderr   io.Writer
		backfill backfillFileAttributionsFunc
		want     string
	}{
		{name: "nil stdout", stdout: nil, stderr: io.Discard, backfill: succeed, want: "required"},
		{name: "nil stderr", stdout: io.Discard, stderr: nil, backfill: succeed, want: "required"},
		{name: "nil operation", stdout: io.Discard, stderr: io.Discard, want: "required"},
		{
			name:     "bad flag",
			args:     []string{"--unknown"},
			stdout:   io.Discard,
			stderr:   io.Discard,
			backfill: succeed,
			want:     "flag",
		},
		{
			name:     "missing identity",
			args:     nil,
			stdout:   io.Discard,
			stderr:   io.Discard,
			backfill: succeed,
			want:     "required",
		},
		{
			name:   "dry failure",
			args:   []string{"--workspace=w", "--automation=a"},
			stdout: io.Discard,
			stderr: io.Discard,
			backfill: func(context.Context, string, string, launcherapi.RepositoryReviewFileAttributionBackfillOptions) (launcherapi.RepositoryReviewFileAttributionBackfillReport, error) {
				return report, boom
			},
			want: boom.Error(),
		},
		{
			name:   "apply missing digest",
			args:   []string{"--workspace=w", "--automation=a", "--apply"},
			stdout: io.Discard,
			stderr: io.Discard,
			backfill: func(context.Context, string, string, launcherapi.RepositoryReviewFileAttributionBackfillOptions) (launcherapi.RepositoryReviewFileAttributionBackfillReport, error) {
				return report, nil
			},
			want: "expect-digest",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := runBackfillForTest(test.args, test.stdout, test.stderr, test.backfill)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error=%v want %q", err, test.want)
			}
		})
	}
}

func TestRunApplyFailuresAndEncoding(t *testing.T) {
	base := launcherapi.RepositoryReviewFileAttributionBackfillReport{
		ConfiguredRuns: 1, RecoveredRuns: 1, ChildAttempts: 1,
		SuccessfulChildren: 1, AttributionRecords: 1,
		AcknowledgementOccurrences: 1, UniqueFiles: 1, UniqueFileAssignments: 1,
		Digest: "sha256:digest",
	}
	args := []string{
		"--workspace=w", "--automation=a", "--apply", "--expect-digest=sha256:digest",
		"--expect-configured-runs=1", "--expect-recovered-runs=1", "--expect-non-ledger-runs=0",
		"--expect-child-attempts=1", "--expect-successful-children=1", "--expect-failed-children=0",
		"--expect-attribution-records=1", "--expect-acknowledgements=1", "--expect-unique-files=1",
		"--expect-file-assignments=1", "--expect-campaign-assignment-credits=0",
		"--expect-campaign-attributed-files=0", "--expect-projected-completed-assignments=0",
		"--expect-projected-pending-assignments=0", "--expect-projected-inspected-files=0",
		"--expect-projected-completed-files=0",
	}
	t.Run("missing expectation", func(t *testing.T) {
		err := runBackfillForTest(
			args[:len(args)-1],
			io.Discard,
			io.Discard,
			func(context.Context, string, string, launcherapi.RepositoryReviewFileAttributionBackfillOptions) (launcherapi.RepositoryReviewFileAttributionBackfillReport, error) {
				return base, nil
			},
		)
		if err == nil || !strings.Contains(err.Error(), "expectation for projected completed files") {
			t.Fatalf("error=%v", err)
		}
	})
	t.Run("changed initial count", func(t *testing.T) {
		changed := base
		changed.UniqueFiles = 2
		err := runBackfillForTest(
			args,
			io.Discard,
			io.Discard,
			func(context.Context, string, string, launcherapi.RepositoryReviewFileAttributionBackfillOptions) (launcherapi.RepositoryReviewFileAttributionBackfillReport, error) {
				return changed, nil
			},
		)
		if err == nil || !strings.Contains(err.Error(), "unique files changed") {
			t.Fatalf("error=%v", err)
		}
	})
	t.Run("apply failure", func(t *testing.T) {
		calls := 0
		err := runBackfillForTest(
			args,
			io.Discard,
			io.Discard,
			func(context.Context, string, string, launcherapi.RepositoryReviewFileAttributionBackfillOptions) (launcherapi.RepositoryReviewFileAttributionBackfillReport, error) {
				calls++
				if calls == 2 {
					return base, errors.New("apply failed")
				}
				return base, nil
			},
		)
		if err == nil || !strings.Contains(err.Error(), "apply failed") {
			t.Fatalf("error=%v", err)
		}
	})
	t.Run("changed applied count", func(t *testing.T) {
		calls := 0
		err := runBackfillForTest(
			args,
			io.Discard,
			io.Discard,
			func(context.Context, string, string, launcherapi.RepositoryReviewFileAttributionBackfillOptions) (launcherapi.RepositoryReviewFileAttributionBackfillReport, error) {
				calls++
				result := base
				if calls == 2 {
					result.AttributionRecords++
				}
				return result, nil
			},
		)
		if err == nil || !strings.Contains(err.Error(), "attribution records changed") {
			t.Fatalf("error=%v", err)
		}
	})
	t.Run("encode failure", func(t *testing.T) {
		err := runBackfillForTest(
			[]string{"--workspace=w", "--automation=a"},
			failingWriter{},
			io.Discard,
			func(context.Context, string, string, launcherapi.RepositoryReviewFileAttributionBackfillOptions) (launcherapi.RepositoryReviewFileAttributionBackfillReport, error) {
				return base, nil
			},
		)
		if err == nil || !strings.Contains(err.Error(), "write failed") {
			t.Fatalf("error=%v", err)
		}
	})
}

type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) { return 0, errors.New("write failed") }

func TestCompareExpectedCountsAllFields(t *testing.T) {
	report := launcherapi.RepositoryReviewFileAttributionBackfillReport{
		ConfiguredRuns: 1, RecoveredRuns: 2, AllowedNonLedgerRuns: 3,
		ChildAttempts: 4, SuccessfulChildren: 5, FailedChildren: 6,
		AttributionRecords: 7, AcknowledgementOccurrences: 8,
		UniqueFiles: 9, UniqueFileAssignments: 10,
		CampaignAssignmentCredits: 11, CampaignAttributedFiles: 12,
		ProjectedCompletedAssignments: 13, ProjectedPendingAssignments: 14,
		ProjectedInspectedFiles: 15, ProjectedCompletedFiles: 16,
	}
	expected := expectedCounts{
		configuredRuns: 1, recoveredRuns: 2, nonLedgerRuns: 3,
		childAttempts: 4, successfulChildren: 5, failedChildren: 6,
		attributionRecords: 7, acknowledgements: 8, uniqueFiles: 9,
		uniqueFileAssignments: 10, campaignAssignmentCredits: 11,
		campaignAttributedFiles: 12, projectedCompletedAssignments: 13,
		projectedPendingAssignments: 14, projectedInspectedFiles: 15,
		projectedCompletedFiles: 16,
	}
	if err := compareExpectedCounts(report, expected); err != nil {
		t.Fatal(err)
	}
	for index := range 16 {
		candidate := expected
		fields := []*int{
			&candidate.configuredRuns,
			&candidate.recoveredRuns,
			&candidate.nonLedgerRuns,
			&candidate.childAttempts,
			&candidate.successfulChildren,
			&candidate.failedChildren,
			&candidate.attributionRecords,
			&candidate.acknowledgements,
			&candidate.uniqueFiles,
			&candidate.uniqueFileAssignments,
			&candidate.campaignAssignmentCredits,
			&candidate.campaignAttributedFiles,
			&candidate.projectedCompletedAssignments,
			&candidate.projectedPendingAssignments,
			&candidate.projectedInspectedFiles,
			&candidate.projectedCompletedFiles,
		}
		*fields[index]++
		if err := compareExpectedCounts(report, candidate); err == nil {
			t.Fatalf("field %d mismatch accepted", index)
		}
	}
}

func TestFatalWrapper(t *testing.T) {
	originalStderr, originalExit := os.Stderr, exitProcess
	t.Cleanup(func() {
		os.Stderr, exitProcess = originalStderr, originalExit
	})
	stderr, err := os.CreateTemp(t.TempDir(), "stderr")
	if err != nil {
		t.Fatal(err)
	}
	os.Stderr = stderr
	exitCode := 0
	exitProcess = func(code int) { exitCode = code }
	fatal(errors.New("fatal test"))
	if closeErr := stderr.Close(); closeErr != nil {
		t.Fatal(closeErr)
	}
	encoded, err := os.ReadFile(stderr.Name())
	if err != nil || exitCode != 1 || !strings.Contains(string(encoded), "fatal test") {
		t.Fatalf("stderr=%q exit=%d err=%v", encoded, exitCode, err)
	}
}
