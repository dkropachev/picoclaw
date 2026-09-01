package repoaudit

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestRepositoryValidationFailureWrapperIsAllowlisted(t *testing.T) {
	cause := errors.New("provider credential and private source text")
	wrapped := WrapRepositoryValidationFailure(RepositoryValidationFailureCodeModelTimeout, cause)
	code, ok := RepositoryValidationFailureCodeFromError(fmt.Errorf("request: %w", wrapped))
	if !ok || code != RepositoryValidationFailureCodeModelTimeout || !errors.Is(wrapped, cause) {
		t.Fatalf("wrapped code=%q found=%v error=%v", code, ok, wrapped)
	}
	if WrapRepositoryValidationFailure(RepositoryValidationFailureCodeModelTimeout, nil) != nil {
		t.Fatal("nil cause produced a failure wrapper")
	}

	unknown := WrapRepositoryValidationFailure(RepositoryValidationFailureCode("provider_secret"), cause)
	code, ok = RepositoryValidationFailureCodeFromError(unknown)
	if !ok || code != RepositoryValidationFailureCodeProcessing {
		t.Fatalf("unknown wrapped code=%q found=%v", code, ok)
	}
	invalidWrapped := &repositoryValidationFailureError{
		code: RepositoryValidationFailureCode("provider_secret"), cause: cause,
	}
	if code, ok = RepositoryValidationFailureCodeFromError(invalidWrapped); ok || code != "" {
		t.Fatalf("invalid internal wrapped code=%q found=%v", code, ok)
	}

	now := time.Date(2026, 8, 31, 12, 0, 0, 0, time.UTC)
	failure := safeRepositoryValidationFailure(RepositoryValidationFailureCode("provider_secret"), now)
	if failure.Code != RepositoryValidationFailureCodeProcessing ||
		failure.Message != "Fix-check processing failed." || !failure.Retryable ||
		!failure.At.Equal(now) || !validRepositoryValidationFailure(failure) {
		t.Fatalf("sanitized failure=%#v", failure)
	}
	failure.Message = cause.Error()
	if validRepositoryValidationFailure(failure) {
		t.Fatal("non-allowlisted failure message was accepted")
	}
}

func TestCompleteValidationJobPersistsSafeFailureAndAcceptsHistoricalNil(t *testing.T) {
	store, state, pending := newLifecycleCoverageValidationStore(t, "safe-failure-persistence")
	now := state.UpdatedAt.Add(time.Minute)
	store.now = func() time.Time { return now }
	_, running, _, claimed, err := store.ClaimValidationJob(state.Repository, pending.ID)
	if err != nil || !claimed {
		t.Fatalf("claim=%v err=%v", claimed, err)
	}
	secret := "provider token=secret private/path.go:42"
	completedState, finding, completed, err := store.CompleteValidationJob(
		state.Repository,
		RepositoryValidationCompletion{
			JobID: running.ID, Outcome: RepositoryValidationFailed, Error: secret,
			Summary: "The fix check could not finish.",
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if completed.Failure == nil || completed.Failure.Code != RepositoryValidationFailureCodeProcessing ||
		completed.Failure.Message != "Fix-check processing failed." || !completed.Failure.Retryable ||
		!completed.Failure.At.Equal(now) || completed.Error != "Validation failed." {
		t.Fatalf("completed failure=%#v error=%q", completed.Failure, completed.Error)
	}
	if len(finding.ResolutionHistory) != 1 || finding.ResolutionHistory[0].Failure == nil ||
		*finding.ResolutionHistory[0].Failure != *completed.Failure {
		t.Fatalf("resolution history=%#v", finding.ResolutionHistory)
	}
	encoded, err := json.Marshal(completedState)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), secret) || strings.Contains(string(encoded), "private/path.go") {
		t.Fatalf("raw cause leaked into state: %s", encoded)
	}

	var rehomed RepositoryState
	if err := json.Unmarshal(encoded, &rehomed); err != nil {
		t.Fatal(err)
	}
	rehomed.ValidationJobs[0].UpdatedAt = now.Add(time.Minute)
	if err := validateRepositoryLifecycleState(rehomed); err != nil {
		t.Fatalf("later lifecycle update invalidated the attempt failure timestamp: %v", err)
	}

	var historical RepositoryState
	if err := json.Unmarshal(encoded, &historical); err != nil {
		t.Fatal(err)
	}
	historical.ValidationJobs[0].Failure = nil
	historical.RepositoryFindings[0].ResolutionHistory[0].Failure = nil
	if err := validateRepositoryLifecycleState(historical); err != nil {
		t.Fatalf("historical failed state without diagnostics was rejected: %v", err)
	}
	if _, _, _, err := store.CompleteValidationJob(state.Repository, RepositoryValidationCompletion{
		JobID: pending.ID, Outcome: RepositoryValidationNotFixed,
		FailureCode: RepositoryValidationFailureCodeModelRequest,
	}); err == nil {
		t.Fatal("non-failed completion accepted a failure code")
	}
	if _, _, _, err := store.CompleteValidationJob(state.Repository, RepositoryValidationCompletion{
		JobID: pending.ID, Outcome: RepositoryValidationNotFixed,
		FirstContainingTag: strings.Repeat("x", 257),
	}); err == nil {
		t.Fatal("oversized completion metadata was accepted")
	}

	var malformed RepositoryState
	if err := json.Unmarshal(encoded, &malformed); err != nil {
		t.Fatal(err)
	}
	malformed.ValidationJobs[0].Failure.Message = secret
	if err := validateRepositoryLifecycleState(malformed); err == nil {
		t.Fatal("failure with non-allowlisted message was accepted")
	}
}

func TestValidationWorkerPersistsSafeStageFailureCodes(t *testing.T) {
	type testCase struct {
		name string
		code RepositoryValidationFailureCode
	}
	cases := []testCase{
		{name: "evidence unavailable", code: RepositoryValidationFailureCodeEvidenceUnavailable},
		{name: "evidence invalid", code: RepositoryValidationFailureCodeEvidenceInvalid},
		{name: "evidence changed", code: RepositoryValidationFailureCodeEvidenceChanged},
		{name: "model request", code: RepositoryValidationFailureCodeModelRequest},
		{name: "model unavailable override", code: RepositoryValidationFailureCodeModelUnavailable},
		{name: "model timeout override", code: RepositoryValidationFailureCodeModelTimeout},
		{name: "model output override", code: RepositoryValidationFailureCodeModelOutputInvalid},
		{name: "invalid result", code: RepositoryValidationFailureCodeResultInvalid},
		{name: "selected evidence missing commit time", code: RepositoryValidationFailureCodeEvidenceInvalid},
		{name: "default branch verification", code: RepositoryValidationFailureCodeDefaultBranchVerification},
		{name: "release tag", code: RepositoryValidationFailureCodeReleaseTag},
		{name: "invalid release tag", code: RepositoryValidationFailureCodeReleaseTag},
		{name: "processing override", code: RepositoryValidationFailureCodeProcessing},
	}
	for index, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			store, state, pending := newLifecycleCoverageValidationStore(
				t, fmt.Sprintf("safe-stage-%02d", index),
			)
			commit := strings.Repeat("d", 40)
			secret := "provider-secret private/repository/path.go"
			options := RepositoryValidationProcessOptions{
				Evidence: func(context.Context, RepositoryFinding, []string) ([]RepositoryValidationEvidence, error) {
					return []RepositoryValidationEvidence{{CommitSHA: commit, CommitTime: time.Now().UTC()}}, nil
				},
				Adjudicate: func(context.Context, RepositoryMappingModelSnapshot, RepositoryFinding, []RepositoryValidationEvidence) (RepositoryValidationDecision, error) {
					return RepositoryValidationDecision{Outcome: RepositoryValidationNotFixed}, nil
				},
				VerifyAncestry: func(context.Context, string) (bool, error) { return true, nil },
			}
			switch test.code {
			case RepositoryValidationFailureCodeEvidenceUnavailable:
				options.Evidence = func(context.Context, RepositoryFinding, []string) ([]RepositoryValidationEvidence, error) {
					return nil, errors.New(secret)
				}
			case RepositoryValidationFailureCodeEvidenceInvalid:
				if test.name == "selected evidence missing commit time" {
					options.Evidence = func(context.Context, RepositoryFinding, []string) ([]RepositoryValidationEvidence, error) {
						return []RepositoryValidationEvidence{{CommitSHA: commit}}, nil
					}
					options.Adjudicate = confirmedValidationDecision(commit)
				} else {
					options.Evidence = func(context.Context, RepositoryFinding, []string) ([]RepositoryValidationEvidence, error) {
						return []RepositoryValidationEvidence{{CommitSHA: "invalid"}}, nil
					}
				}
			case RepositoryValidationFailureCodeEvidenceChanged:
				_, running, _, claimed, err := store.ClaimValidationJob(state.Repository, pending.ID)
				if err != nil || !claimed {
					t.Fatalf("claim=%v err=%v", claimed, err)
				}
				if _, _, err := store.SetValidationJobCandidates(
					state.Repository, running.ID, []string{strings.Repeat("c", 40)},
				); err != nil {
					t.Fatal(err)
				}
				if err := store.releaseValidationJob(state.Repository, running.ID); err != nil {
					t.Fatal(err)
				}
			case RepositoryValidationFailureCodeModelRequest:
				options.Adjudicate = func(context.Context, RepositoryMappingModelSnapshot, RepositoryFinding, []RepositoryValidationEvidence) (RepositoryValidationDecision, error) {
					return RepositoryValidationDecision{}, errors.New(secret)
				}
			case RepositoryValidationFailureCodeModelTimeout,
				RepositoryValidationFailureCodeModelUnavailable,
				RepositoryValidationFailureCodeModelOutputInvalid,
				RepositoryValidationFailureCodeProcessing:
				options.Adjudicate = func(context.Context, RepositoryMappingModelSnapshot, RepositoryFinding, []RepositoryValidationEvidence) (RepositoryValidationDecision, error) {
					return RepositoryValidationDecision{}, WrapRepositoryValidationFailure(
						test.code, errors.New(secret),
					)
				}
			case RepositoryValidationFailureCodeResultInvalid:
				options.Adjudicate = func(context.Context, RepositoryMappingModelSnapshot, RepositoryFinding, []RepositoryValidationEvidence) (RepositoryValidationDecision, error) {
					return RepositoryValidationDecision{}, nil
				}
			case RepositoryValidationFailureCodeDefaultBranchVerification:
				options.Adjudicate = confirmedValidationDecision(commit)
				options.VerifyAncestry = func(context.Context, string) (bool, error) {
					return false, errors.New(secret)
				}
			case RepositoryValidationFailureCodeReleaseTag:
				options.Adjudicate = confirmedValidationDecision(commit)
				if test.name == "invalid release tag" {
					options.FirstSemanticTag = func(context.Context, string) (string, error) {
						return "not-semver", nil
					}
				} else {
					options.FirstSemanticTag = func(context.Context, string) (string, error) {
						return "", errors.New(secret)
					}
				}
			}

			result, err := store.ProcessPendingValidationJobs(t.Context(), state.Repository, options)
			if err != nil || result.Failed != 1 || result.Completed != 1 {
				t.Fatalf("result=%#v err=%v", result, err)
			}
			current, found, err := store.Get(state.Repository)
			if err != nil || !found {
				t.Fatalf("get found=%v err=%v", found, err)
			}
			job := lifecycleValidationJobByID(t, current, pending.ID)
			findingIndex := repositoryFindingIndexByID(current.RepositoryFindings, job.RepositoryFindingID)
			resolutions := current.RepositoryFindings[findingIndex].ResolutionHistory
			if job.Failure == nil || job.Failure.Code != test.code || !job.Failure.Retryable ||
				len(resolutions) == 0 || resolutions[len(resolutions)-1].Failure == nil ||
				resolutions[len(resolutions)-1].Failure.Code != test.code {
				t.Fatalf("job=%#v resolutions=%#v", job, resolutions)
			}
			encoded, err := json.Marshal(current)
			if err != nil || strings.Contains(string(encoded), secret) {
				t.Fatalf("raw cause leaked: err=%v state=%s", err, encoded)
			}
		})
	}

	t.Run("completion persistence failure is returned safely", func(t *testing.T) {
		store, state, pending := newLifecycleCoverageValidationStore(t, "safe-completion-failure")
		commit := strings.Repeat("e", 40)
		const privateFailure = "provider token=secret private/path.go"
		database, err := sql.Open("sqlite", store.database)
		if err != nil {
			t.Fatal(err)
		}
		database.SetMaxOpenConns(1)
		if _, err = database.Exec(`PRAGMA busy_timeout = 5000`); err != nil {
			_ = database.Close()
			t.Fatal(err)
		}
		_, triggerErr := database.Exec(`CREATE TRIGGER reject_nonfailed_validation_completion
			BEFORE UPDATE OF payload_json ON repository_review_states
			WHEN json_extract(CAST(NEW.payload_json AS TEXT), '$.validation_jobs[0].state') = 'not_fixed'
			BEGIN SELECT RAISE(ABORT, 'provider token=secret private/path.go'); END`)
		closeErr := database.Close()
		if triggerErr != nil || closeErr != nil {
			t.Fatalf("install validation completion fault: trigger=%v close=%v", triggerErr, closeErr)
		}
		result, err := store.ProcessPendingValidationJobs(
			t.Context(), state.Repository, RepositoryValidationProcessOptions{
				Evidence: func(context.Context, RepositoryFinding, []string) ([]RepositoryValidationEvidence, error) {
					return []RepositoryValidationEvidence{{CommitSHA: commit, CommitTime: time.Now().UTC()}}, nil
				},
				Adjudicate: func(context.Context, RepositoryMappingModelSnapshot, RepositoryFinding, []RepositoryValidationEvidence) (RepositoryValidationDecision, error) {
					return RepositoryValidationDecision{
						Outcome: RepositoryValidationNotFixed, Summary: "No fix found.",
					}, nil
				},
				VerifyAncestry: func(context.Context, string) (bool, error) { return true, nil },
			},
		)
		if err != nil || result.Completed != 1 || result.Failed != 1 {
			t.Fatalf("completion persistence result=%#v err=%v", result, err)
		}
		current, found, loadErr := store.Get(state.Repository)
		if loadErr != nil || !found {
			t.Fatalf("load failed validation state found=%v err=%v", found, loadErr)
		}
		job := lifecycleValidationJobByID(t, current, pending.ID)
		if job.State != RepositoryValidationFailed || job.Failure == nil ||
			job.Failure.Code != RepositoryValidationFailureCodeProcessing ||
			job.Failure.Message != "Fix-check processing failed." {
			t.Fatalf("safe processing failure=%#v", job)
		}
		encoded, encodeErr := json.Marshal(current)
		if encodeErr != nil || strings.Contains(string(encoded), privateFailure) {
			t.Fatalf("private completion failure leaked: err=%v state=%s", encodeErr, encoded)
		}
	})
}

func confirmedValidationDecision(commit string) RepositoryValidationAdjudicator {
	return func(
		context.Context,
		RepositoryMappingModelSnapshot,
		RepositoryFinding,
		[]RepositoryValidationEvidence,
	) (RepositoryValidationDecision, error) {
		return RepositoryValidationDecision{
			Outcome: RepositoryValidationConfirmed, SelectedCommitSHA: commit,
		}, nil
	}
}
