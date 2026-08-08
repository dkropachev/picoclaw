package prdevelopment

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/sipeed/picoclaw/pkg/eventing"
)

func TestGitHubVerifierVerifyCaseRefreshesExactMutableHead(t *testing.T) {
	t.Parallel()
	stored := validStoredGitHubCase()
	stored.Repository = "scylladb/picoclaw"
	stored.BaseRepository = "SCYLLADB/PICOCLAW"
	stored.PullAuthor = "review-user"
	stored.TargetUser = "Review-User"
	stored.ReviewAuthor = "independent-reviewer"
	stored.BaseSHA = strings.Repeat("9", 40)

	pull := providerCasePullJSON(t, func(value map[string]any) {
		value["draft"] = true
	})
	runner := &captureToolRunner{responses: []string{
		pull,
		providerReviewsJSON(providerReviewValue(
			"CHANGES_REQUESTED",
			stored.Feedback,
		)),
	}}

	verified, err := (&GitHubVerifier{Runner: runner}).VerifyCase(
		context.Background(),
		stored,
	)
	if err != nil {
		t.Fatalf("VerifyCase() error = %v", err)
	}
	if verified.CaseID != stored.ID ||
		verified.Repository != "ScyllaDB/PicoClaw" ||
		verified.PullNumber != 42 ||
		verified.HeadRepository != "contributor/PicoClaw" ||
		verified.HeadRef != "feat/fix-race" ||
		verified.HeadSHA != testHeadSHA ||
		verified.HeadSHA == stored.HeadSHA ||
		verified.HeadCloneURL != "https://github.com/contributor/PicoClaw.git" ||
		verified.CurrentReviewState != eventing.PRDevelopmentReviewChangesRequested ||
		verified.ReviewDigest != "sha256:8a325e37bfcf34ac026032700822081b777635ce82e706d3efbdc047333a388d" {
		t.Fatalf("VerifyCase() = %#v", verified)
	}
	if len(runner.requests) != 2 {
		t.Fatalf("provider calls = %d, want pull and reviews", len(runner.requests))
	}
	assertReadRequest(t, runner.requests[0], "get", 0)
	assertReadRequest(t, runner.requests[1], "get_reviews", 1)
}

func TestGitHubVerifierVerifyCaseRejectsNonActionableProviderState(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name        string
		mutatePull  func(map[string]any)
		reviewState string
	}{
		{
			name: "closed unmerged pull request",
			mutatePull: func(value map[string]any) {
				value["state"] = "CLOSED"
				value["merged"] = false
			},
			reviewState: "CHANGES_REQUESTED",
		},
		{
			name: "merged pull request",
			mutatePull: func(value map[string]any) {
				value["state"] = "CLOSED"
				value["merged"] = true
			},
			reviewState: "CHANGES_REQUESTED",
		},
		{
			name:        "dismissed review",
			mutatePull:  func(map[string]any) {},
			reviewState: "DISMISSED",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			stored := validStoredGitHubCase()
			runner := &captureToolRunner{responses: []string{
				providerCasePullJSON(t, test.mutatePull),
				providerReviewsJSON(providerReviewValue(
					test.reviewState,
					stored.Feedback,
				)),
			}}
			_, err := (&GitHubVerifier{Runner: runner}).VerifyCase(
				context.Background(),
				stored,
			)
			if !errors.Is(err, ErrGitHubCaseNotActionable) {
				t.Fatalf("VerifyCase() error = %v, want non-actionable", err)
			}
		})
	}
}

func TestGitHubVerifierVerifyCaseRejectsCapturedEvidenceDrift(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name         string
		mutatePull   func(map[string]any)
		providerBody string
	}{
		{
			name: "base retargeted",
			mutatePull: func(value map[string]any) {
				providerCasePullBase(value)["ref"] = "release"
			},
			providerBody: "Fix the race.",
		},
		{
			name:         "review body edited",
			mutatePull:   func(map[string]any) {},
			providerBody: "Fix a different problem.",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			stored := validStoredGitHubCase()
			runner := &captureToolRunner{responses: []string{
				providerCasePullJSON(t, test.mutatePull),
				providerReviewsJSON(providerReviewValue(
					"CHANGES_REQUESTED",
					test.providerBody,
				)),
			}}
			_, err := (&GitHubVerifier{Runner: runner}).VerifyCase(
				context.Background(),
				stored,
			)
			if !errors.Is(err, ErrGitHubCaseDrift) {
				t.Fatalf("VerifyCase() error = %v, want drift", err)
			}
		})
	}
}

func TestGitHubVerifierVerifyCaseClassifiesProviderIdentityDrift(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name         string
		mutatePull   func(map[string]any)
		mutateReview func(map[string]any)
		missing      bool
	}{
		{
			name: "base repository",
			mutatePull: func(value map[string]any) {
				repository := providerCasePullBase(value)["repo"].(map[string]any)
				repository["full_name"] = "other/PicoClaw"
			},
			mutateReview: func(map[string]any) {},
		},
		{
			name: "pull author",
			mutatePull: func(value map[string]any) {
				value["user"].(map[string]any)["login"] = "another-user"
			},
			mutateReview: func(map[string]any) {},
		},
		{
			name:         "review state",
			mutatePull:   func(map[string]any) {},
			mutateReview: func(value map[string]any) { value["state"] = "APPROVED" },
		},
		{
			name:       "review commit",
			mutatePull: func(map[string]any) {},
			mutateReview: func(value map[string]any) {
				value["commit_id"] = strings.Repeat("8", 40)
			},
		},
		{
			name:         "missing exact review",
			mutatePull:   func(map[string]any) {},
			mutateReview: func(map[string]any) {},
			missing:      true,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			stored := validStoredGitHubCase()
			review := providerReviewValue("CHANGES_REQUESTED", stored.Feedback)
			test.mutateReview(review)
			reviews := providerReviewsJSON(review)
			if test.missing {
				reviews = "[]"
			}
			runner := &captureToolRunner{responses: []string{
				providerCasePullJSON(t, test.mutatePull),
				reviews,
			}}
			_, err := (&GitHubVerifier{Runner: runner}).VerifyCase(
				context.Background(),
				stored,
			)
			if !errors.Is(err, ErrGitHubCaseDrift) {
				t.Fatalf("VerifyCase() error = %v, want classified drift", err)
			}
		})
	}
}

func TestGitHubVerifierVerifyCaseDoesNotClassifyOperationalFailureAsDrift(t *testing.T) {
	t.Parallel()
	t.Run("provider transport", func(t *testing.T) {
		t.Parallel()
		runner := &captureToolRunner{}
		_, err := (&GitHubVerifier{Runner: runner}).VerifyCase(
			context.Background(),
			validStoredGitHubCase(),
		)
		if err == nil || errors.Is(err, ErrGitHubCaseDrift) {
			t.Fatalf("VerifyCase() error = %v, want non-drift provider failure", err)
		}
	})
	t.Run("cancellation", func(t *testing.T) {
		t.Parallel()
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		runner := &captureToolRunner{}
		_, err := (&GitHubVerifier{Runner: runner}).VerifyCase(
			ctx,
			validStoredGitHubCase(),
		)
		if !errors.Is(err, context.Canceled) || errors.Is(err, ErrGitHubCaseDrift) {
			t.Fatalf("VerifyCase() error = %v, want cancellation only", err)
		}
		if len(runner.requests) != 0 {
			t.Fatalf("canceled verification caused %d provider calls", len(runner.requests))
		}
	})
}

func TestGitHubVerifierVerifyCaseRejectsAmbiguousCloneEndpoint(t *testing.T) {
	t.Parallel()
	for _, cloneURL := range []string{
		"",
		"http://github.com/contributor/PicoClaw.git",
		"https://user@github.com/contributor/PicoClaw.git",
		"https://git.example.test/contributor/PicoClaw.git",
		"https://github.com:443/contributor/PicoClaw.git",
		"https://github.com./contributor/PicoClaw.git",
		"https://github.com/other/PicoClaw.git",
		"https://github.com/contributor/PicoClaw.git?download=1",
		"https://github.com/contributor/PicoClaw.git#fragment",
		"https://github.com/%63ontributor/PicoClaw.git",
	} {
		name := cloneURL
		if name == "" {
			name = "missing"
		}
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			stored := validStoredGitHubCase()
			runner := &captureToolRunner{responses: []string{
				providerCasePullJSON(t, func(value map[string]any) {
					providerCasePullHeadRepository(value)["clone_url"] = cloneURL
				}),
				providerReviewsJSON(providerReviewValue(
					"CHANGES_REQUESTED",
					stored.Feedback,
				)),
			}}
			_, err := (&GitHubVerifier{Runner: runner}).VerifyCase(
				context.Background(),
				stored,
			)
			if !errors.Is(err, ErrGitHubCaseDrift) {
				t.Fatalf("VerifyCase() error = %v, want clone-endpoint drift", err)
			}
		})
	}
}

func TestGitHubVerifierVerifyCaseBindsCanonicalURLsToWebOrigin(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name   string
		mutate func(*eventing.PRDevelopmentCase)
	}{
		{
			name: "different origin",
			mutate: func(stored *eventing.PRDevelopmentCase) {
				stored.PullURL = "https://ghe.example.test/ScyllaDB/PicoClaw/pull/42"
				stored.ReviewURL = stored.PullURL + "#pullrequestreview-701"
			},
		},
		{
			name: "pull query",
			mutate: func(stored *eventing.PRDevelopmentCase) {
				stored.PullURL += "?view=files"
				stored.ReviewURL = stored.PullURL + "#pullrequestreview-701"
			},
		},
		{
			name: "wrong pull path",
			mutate: func(stored *eventing.PRDevelopmentCase) {
				stored.PullURL = "https://github.com/ScyllaDB/PicoClaw/issues/42"
				stored.ReviewURL = stored.PullURL + "#pullrequestreview-701"
			},
		},
		{
			name: "encoded repository path",
			mutate: func(stored *eventing.PRDevelopmentCase) {
				stored.PullURL = "https://github.com/ScyllaDB/%50icoClaw/pull/42"
				stored.ReviewURL = stored.PullURL + "#pullrequestreview-701"
			},
		},
		{
			name: "wrong review fragment",
			mutate: func(stored *eventing.PRDevelopmentCase) {
				stored.ReviewURL = stored.PullURL + "#discussion_r701"
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			stored := validStoredGitHubCase()
			test.mutate(&stored)
			runner := &captureToolRunner{}
			_, err := (&GitHubVerifier{Runner: runner}).VerifyCase(
				context.Background(),
				stored,
			)
			if !errors.Is(err, ErrGitHubCaseDrift) {
				t.Fatalf("VerifyCase() error = %v, want canonical-URL drift", err)
			}
			if len(runner.requests) != 0 {
				t.Fatalf("invalid provider URL caused %d provider calls", len(runner.requests))
			}
		})
	}
}

func TestGitHubVerifierVerifyCaseSupportsExplicitGitHubEnterpriseOrigin(t *testing.T) {
	t.Parallel()
	const webOrigin = "https://ghe.example.test:8443"
	stored := validStoredGitHubCase()
	stored.PullURL = webOrigin + "/ScyllaDB/PicoClaw/pull/42"
	stored.ReviewURL = stored.PullURL + "#pullrequestreview-701"
	pull := providerCasePullJSON(t, func(value map[string]any) {
		value["html_url"] = stored.PullURL
		headRepository := providerCasePullHeadRepository(value)
		headRepository["clone_url"] = webOrigin + "/contributor/PicoClaw.git"
	})
	review := providerReviewValue("CHANGES_REQUESTED", stored.Feedback)
	review["html_url"] = stored.ReviewURL
	runner := &captureToolRunner{responses: []string{
		pull,
		providerReviewsJSON(review),
	}}
	verified, err := (&GitHubVerifier{
		Runner:    runner,
		WebOrigin: webOrigin,
	}).VerifyCase(context.Background(), stored)
	if err != nil {
		t.Fatalf("VerifyCase() error = %v", err)
	}
	if verified.HeadCloneURL != webOrigin+"/contributor/PicoClaw.git" {
		t.Fatalf("head clone URL = %q", verified.HeadCloneURL)
	}
}

func TestGitHubVerifierVerifyCaseRejectsNoncanonicalWebOriginAsConfiguration(t *testing.T) {
	t.Parallel()
	for _, webOrigin := range []string{
		"https://GHE.example.test",
		"https://ghe.example.test/",
		"https://ghe.example.test/path",
		"https://ghe.example.test:443",
		"https://user@ghe.example.test",
		"https://ghe.example.test?api=v3",
	} {
		t.Run(webOrigin, func(t *testing.T) {
			t.Parallel()
			runner := &captureToolRunner{}
			_, err := (&GitHubVerifier{
				Runner:    runner,
				WebOrigin: webOrigin,
			}).VerifyCase(context.Background(), validStoredGitHubCase())
			if err == nil || errors.Is(err, ErrGitHubCaseDrift) {
				t.Fatalf("VerifyCase() error = %v, want configuration failure", err)
			}
			if len(runner.requests) != 0 {
				t.Fatalf("invalid web origin caused %d provider calls", len(runner.requests))
			}
		})
	}
}

func TestGitHubVerifierWebOriginDoesNotChangeCaptureVerification(t *testing.T) {
	t.Parallel()
	runner := &captureToolRunner{responses: []string{
		providerPullJSON("OPEN", testHeadSHA),
		providerReviewsJSON(providerReviewValue("CHANGES_REQUESTED", "feedback")),
	}}
	verified, err := (&GitHubVerifier{
		Runner:    runner,
		WebOrigin: "not a development web origin",
	}).Verify(context.Background(), validRoutingEvidence())
	if err != nil {
		t.Fatalf("Verify() error = %v", err)
	}
	if verified.Feedback != "feedback" {
		t.Fatalf("Verify() feedback = %q", verified.Feedback)
	}
}

func TestGitHubVerifierVerifyCaseRejectsInvalidStoredCaseBeforeProviderRead(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name   string
		mutate func(*eventing.PRDevelopmentCase)
		want   error
	}{
		{
			name: "case identity",
			mutate: func(stored *eventing.PRDevelopmentCase) {
				stored.ID = "pdc_invalid"
			},
			want: ErrGitHubCaseDrift,
		},
		{
			name: "pull number",
			mutate: func(stored *eventing.PRDevelopmentCase) {
				stored.PullNumber = 0
			},
			want: ErrGitHubCaseDrift,
		},
		{
			name: "pull state",
			mutate: func(stored *eventing.PRDevelopmentCase) {
				stored.PullState = eventing.PRDevelopmentPullState("reopened")
			},
			want: ErrGitHubCaseDrift,
		},
		{
			name: "incoherent merged pull state",
			mutate: func(stored *eventing.PRDevelopmentCase) {
				stored.PullMerged = true
			},
			want: ErrGitHubCaseDrift,
		},
		{
			name: "repository path alias",
			mutate: func(stored *eventing.PRDevelopmentCase) {
				stored.HeadRepository = "../PicoClaw"
			},
			want: ErrGitHubCaseDrift,
		},
		{
			name: "trigger review node",
			mutate: func(stored *eventing.PRDevelopmentCase) {
				stored.TriggerReviewNodeID = ""
			},
			want: ErrGitHubCaseDrift,
		},
		{
			name: "base ref traversal",
			mutate: func(stored *eventing.PRDevelopmentCase) {
				stored.BaseRef = "release/../main"
			},
			want: ErrGitHubCaseDrift,
		},
		{
			name: "head ref control alias",
			mutate: func(stored *eventing.PRDevelopmentCase) {
				stored.HeadRef = "refs/heads/fix.lock"
			},
			want: ErrGitHubCaseDrift,
		},
		{
			name: "captured dismissed review",
			mutate: func(stored *eventing.PRDevelopmentCase) {
				stored.CurrentReviewState = eventing.PRDevelopmentReviewDismissed
			},
			want: ErrGitHubCaseNotActionable,
		},
		{
			name: "captured merged pull request",
			mutate: func(stored *eventing.PRDevelopmentCase) {
				stored.PullState = eventing.PRDevelopmentPullClosed
				stored.PullMerged = true
			},
			want: ErrGitHubCaseNotActionable,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			stored := validStoredGitHubCase()
			test.mutate(&stored)
			runner := &captureToolRunner{}
			_, err := (&GitHubVerifier{Runner: runner}).VerifyCase(
				context.Background(),
				stored,
			)
			if !errors.Is(err, test.want) {
				t.Fatalf("VerifyCase() error = %v, want %v", err, test.want)
			}
			if len(runner.requests) != 0 {
				t.Fatalf("invalid case caused %d provider calls", len(runner.requests))
			}
		})
	}
}

func TestGitHubVerifierVerifyCaseRequiresContextWithoutProviderRead(t *testing.T) {
	t.Parallel()
	runner := &captureToolRunner{}
	_, err := (&GitHubVerifier{Runner: runner}).VerifyCase(nil, validStoredGitHubCase())
	if err == nil || !strings.Contains(err.Error(), "context") {
		t.Fatalf("VerifyCase(nil) error = %v", err)
	}
	if len(runner.requests) != 0 {
		t.Fatalf("nil context caused %d provider calls", len(runner.requests))
	}
}

func validStoredGitHubCase() eventing.PRDevelopmentCase {
	evidence := validRoutingEvidence()
	return eventing.PRDevelopmentCase{
		ID: "pdc_" + strings.Repeat("1", 32),
		PRDevelopmentCaptureInput: eventing.PRDevelopmentCaptureInput{
			Repository:           evidence.Repository,
			PullNumber:           int64(evidence.PullNumber),
			PullURL:              evidence.PullURL,
			PullAuthor:           evidence.PullAuthor,
			TargetUser:           evidence.TargetUser,
			PullState:            eventing.PRDevelopmentPullOpen,
			BaseRepository:       evidence.Repository,
			BaseRef:              "main",
			BaseSHA:              testBaseSHA,
			HeadRepository:       "old-fork/PicoClaw",
			HeadRef:              "old/fix-race",
			HeadSHA:              testEventHead,
			ReviewID:             evidence.ReviewID,
			TriggerReviewNodeID:  evidence.ReviewNodeID,
			ReviewAuthor:         evidence.ReviewAuthor,
			SubmittedReviewState: eventing.PRDevelopmentReviewChangesRequested,
			CurrentReviewState:   eventing.PRDevelopmentReviewChangesRequested,
			ReviewCommitSHA:      evidence.ReviewCommitSHA,
			ReviewSubmittedAt:    evidence.ReviewSubmittedAt,
			ReviewURL:            evidence.ReviewURL,
			Feedback:             "Fix the race.",
		},
	}
}

func providerCasePullJSON(t *testing.T, mutate func(map[string]any)) string {
	t.Helper()
	var value map[string]any
	if err := json.Unmarshal([]byte(providerPullJSON("OPEN", testHeadSHA)), &value); err != nil {
		t.Fatal(err)
	}
	providerCasePullHeadRepository(value)["clone_url"] = "https://github.com/contributor/PicoClaw.git"
	mutate(value)
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func providerCasePullHeadRepository(value map[string]any) map[string]any {
	return value["head"].(map[string]any)["repo"].(map[string]any)
}

func providerCasePullBase(value map[string]any) map[string]any {
	return value["base"].(map[string]any)
}
