package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"strings"
	"testing"

	launcherapi "github.com/sipeed/picoclaw/web/backend/api"
)

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
	if err := run(
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
	if err := run(args, &stdout, &stderr, backfill); err != nil || len(calls) != 2 ||
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
			err := run(test.args, test.stdout, test.stderr, test.backfill)
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
		err := run(
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
		err := run(
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
		err := run(
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
		err := run(
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
		err := run(
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

func TestMainAndFatalWrappers(t *testing.T) {
	originalArgs, originalStdout, originalStderr := os.Args, os.Stdout, os.Stderr
	originalBackfill, originalPrepare, originalExit := backfillFileAttributions, prepareBackfillRuntime, exitProcess
	t.Cleanup(func() {
		os.Args, os.Stdout, os.Stderr = originalArgs, originalStdout, originalStderr
		backfillFileAttributions, exitProcess = originalBackfill, originalExit
		prepareBackfillRuntime = originalPrepare
	})
	stdout, err := os.CreateTemp(t.TempDir(), "stdout")
	if err != nil {
		t.Fatal(err)
	}
	os.Args = []string{"repository-review-attribution-backfill", "--workspace=w", "--automation=a"}
	os.Stdout = stdout
	backfillFileAttributions = func(
		context.Context,
		string,
		string,
		launcherapi.RepositoryReviewFileAttributionBackfillOptions,
	) (launcherapi.RepositoryReviewFileAttributionBackfillReport, error) {
		return launcherapi.RepositoryReviewFileAttributionBackfillReport{Digest: "sha256:main"}, nil
	}
	prepareBackfillRuntime = func(context.Context, string) (func(), error) {
		return func() {}, nil
	}
	main()
	if closeErr := stdout.Close(); closeErr != nil {
		t.Fatal(closeErr)
	}
	encoded, err := os.ReadFile(stdout.Name())
	if err != nil || !strings.Contains(string(encoded), "sha256:main") {
		t.Fatalf("stdout=%q err=%v", encoded, err)
	}

	stderr, err := os.CreateTemp(t.TempDir(), "stderr")
	if err != nil {
		t.Fatal(err)
	}
	os.Stderr = stderr
	exitCode := 0
	exitProcess = func(code int) { exitCode = code }
	backfillFileAttributions = func(
		context.Context,
		string,
		string,
		launcherapi.RepositoryReviewFileAttributionBackfillOptions,
	) (launcherapi.RepositoryReviewFileAttributionBackfillReport, error) {
		return launcherapi.RepositoryReviewFileAttributionBackfillReport{}, errors.New("main failed")
	}
	main()
	if exitCode != 1 {
		t.Fatalf("main failure exit=%d", exitCode)
	}
	exitCode = 0
	fatal(errors.New("fatal test"))
	if closeErr := stderr.Close(); closeErr != nil {
		t.Fatal(closeErr)
	}
	encoded, err = os.ReadFile(stderr.Name())
	if err != nil || exitCode != 1 || !strings.Contains(string(encoded), "main failed") ||
		!strings.Contains(string(encoded), "fatal test") {
		t.Fatalf("stderr=%q exit=%d err=%v", encoded, exitCode, err)
	}
}
