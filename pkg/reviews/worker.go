package reviews

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/sipeed/picoclaw/pkg/eventing"
)

const (
	defaultSubmissionLease = 5 * time.Minute
	defaultFinishTimeout   = 10 * time.Second
	maxDurableRequestBytes = 1 << 20
)

// SubmissionWorker claims immutable outbox requests and executes the GitHub
// protocol once. Ambiguous external outcomes are terminal and never retried.
type SubmissionWorker struct {
	Queue         eventing.ReviewSubmissionQueue
	Submitter     Submitter
	WorkerLabel   string
	LeaseDuration time.Duration
}

func (worker *SubmissionWorker) ProcessOne(ctx context.Context) (bool, error) {
	if worker == nil || worker.Queue == nil || worker.Submitter == nil {
		return false, ErrUnavailable
	}
	label := strings.TrimSpace(worker.WorkerLabel)
	if label == "" {
		label = "gateway-review-submitter"
	}
	lease := worker.LeaseDuration
	if lease <= 0 {
		lease = defaultSubmissionLease
	}
	claimed, err := worker.Queue.ClaimReviewSubmissions(ctx, label, 1, lease)
	if err != nil {
		return false, err
	}
	if len(claimed) == 0 {
		return false, nil
	}
	if len(claimed) != 1 {
		return true, errors.New("review submission claim exceeded requested limit")
	}
	return true, worker.processClaim(ctx, claimed[0], lease)
}

func (worker *SubmissionWorker) processClaim(
	ctx context.Context,
	submission eventing.ReviewSubmission,
	lease time.Duration,
) error {
	request, err := decodeSubmitRequest(submission.Request)
	if err != nil || request.Marker != submission.Marker {
		if err == nil {
			err = errors.New("submission marker does not match durable request")
		}
		return worker.finish(
			ctx,
			submission,
			eventing.ReviewSubmissionFailed,
			"invalid_submission",
			err,
			"",
			"",
			false,
		)
	}

	submitCtx, cancelSubmit := context.WithCancel(ctx)
	defer cancelSubmit()
	heartbeatDone := make(chan struct{})
	heartbeatErr := make(chan error, 1)
	go worker.renewLease(
		submitCtx,
		heartbeatDone,
		heartbeatErr,
		submission,
		lease,
		cancelSubmit,
	)

	result, submitErr := worker.Submitter.Submit(submitCtx, request)
	cancelSubmit()
	<-heartbeatDone
	var renewErr error
	select {
	case renewErr = <-heartbeatErr:
	default:
	}

	if renewErr != nil {
		if submitErr != nil {
			renewErr = fmt.Errorf(
				"renew review submission lease: %w (submission result: %v)",
				renewErr,
				submitErr,
			)
		} else {
			renewErr = fmt.Errorf("renew review submission lease: %w", renewErr)
		}
		return worker.finish(
			ctx,
			submission,
			eventing.ReviewSubmissionUnknown,
			"worker_outcome_unknown",
			renewErr,
			"",
			pullRequestURL(request),
			false,
		)
	}

	if submitErr != nil {
		var headChanged *PullRequestHeadChangedError
		if errors.As(submitErr, &headChanged) {
			return worker.finishStale(ctx, submission, submitErr, request)
		}
		var stageErr *SubmitStageError
		if errors.As(submitErr, &stageErr) {
			switch {
			case stageErr.ExternalStateMayHaveChanged:
				return worker.finishUnknown(ctx, submission, submitErr, request)
			case stageErr.Stage == SubmitStageValidate:
				return worker.finish(
					ctx,
					submission,
					eventing.ReviewSubmissionFailed,
					"invalid_submission",
					submitErr,
					"",
					"",
					false,
				)
			case stageErr.Stage == SubmitStageVerifyHead:
				return worker.finish(
					ctx,
					submission,
					eventing.ReviewSubmissionFailed,
					"github_submission_failed",
					submitErr,
					"",
					"",
					false,
				)
			}
		}
		// A submitter error without an affirmative typed pre-write guarantee is
		// ambiguous: a remote write may have succeeded before its result was
		// lost. Never reopen the case or retry it automatically.
		return worker.finishUnknown(ctx, submission, submitErr, request)
	}

	externalID, externalURL := submitExternalIdentity(result.SubmittedReview)
	if externalURL == "" {
		externalURL = pullRequestURL(request)
	}
	return worker.finish(
		ctx,
		submission,
		eventing.ReviewSubmissionSubmitted,
		"",
		nil,
		externalID,
		externalURL,
		false,
	)
}

func (worker *SubmissionWorker) finishUnknown(
	ctx context.Context,
	submission eventing.ReviewSubmission,
	err error,
	request SubmitRequest,
) error {
	return worker.finish(
		ctx,
		submission,
		eventing.ReviewSubmissionUnknown,
		"github_outcome_unknown",
		err,
		"",
		pullRequestURL(request),
		false,
	)
}

func (worker *SubmissionWorker) finishStale(
	ctx context.Context,
	submission eventing.ReviewSubmission,
	err error,
	request SubmitRequest,
) error {
	return worker.finish(
		ctx,
		submission,
		eventing.ReviewSubmissionFailed,
		"pull_request_head_changed",
		err,
		"",
		pullRequestURL(request),
		true,
	)
}

func (worker *SubmissionWorker) renewLease(
	ctx context.Context,
	done chan<- struct{},
	errs chan<- error,
	submission eventing.ReviewSubmission,
	lease time.Duration,
	cancel context.CancelFunc,
) {
	defer close(done)
	interval := lease / 3
	if interval < time.Second {
		interval = time.Second
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := worker.Queue.RenewReviewSubmissionLease(
				ctx,
				submission.ID,
				submission.LeaseToken,
				lease,
			); err != nil {
				select {
				case errs <- err:
				default:
				}
				cancel()
				return
			}
		}
	}
}

func (worker *SubmissionWorker) finish(
	ctx context.Context,
	submission eventing.ReviewSubmission,
	status eventing.ReviewSubmissionStatus,
	publicCode string,
	internalErr error,
	externalID, externalURL string,
	stale bool,
) error {
	finishCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), defaultFinishTimeout)
	defer cancel()
	internal := ""
	if internalErr != nil {
		internal = internalErr.Error()
	}
	_, err := worker.Queue.FinishReviewSubmission(
		finishCtx,
		eventing.ReviewSubmissionOutcome{
			SubmissionID:     submission.ID,
			LeaseToken:       submission.LeaseToken,
			Status:           status,
			Stale:            stale,
			PublicErrorCode:  publicCode,
			InternalError:    internal,
			ExternalReviewID: externalID,
			ExternalURL:      externalURL,
		},
	)
	return err
}

func decodeSubmitRequest(raw json.RawMessage) (SubmitRequest, error) {
	if len(raw) == 0 || len(raw) > maxDurableRequestBytes {
		return SubmitRequest{}, errors.New("durable submission request is empty or too large")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var request SubmitRequest
	if err := decoder.Decode(&request); err != nil {
		return SubmitRequest{}, err
	}
	var extra json.RawMessage
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return SubmitRequest{}, errors.New("durable submission request has trailing JSON")
		}
		return SubmitRequest{}, err
	}
	return request, nil
}

func submitExternalIdentity(value map[string]any) (string, string) {
	id := findStringValue(value, 0, "id", "review_id", "node_id")
	externalURL := findStringValue(value, 0, "html_url", "url")
	if parsed, err := url.Parse(externalURL); err != nil ||
		parsed.Scheme != "https" ||
		parsed.Host == "" ||
		parsed.User != nil {
		externalURL = ""
	}
	if len(id) > 1024 {
		id = ""
	}
	if len(externalURL) > 4096 {
		externalURL = ""
	}
	return id, externalURL
}

func findStringValue(value any, depth int, names ...string) string {
	if depth > 5 {
		return ""
	}
	switch typed := value.(type) {
	case map[string]any:
		for _, name := range names {
			if candidate, ok := typed[name]; ok {
				switch scalar := candidate.(type) {
				case string:
					if trimmed := strings.TrimSpace(scalar); trimmed != "" {
						return trimmed
					}
				case json.Number:
					return scalar.String()
				case float64:
					if scalar >= 0 && scalar == float64(int64(scalar)) {
						return strconv.FormatInt(int64(scalar), 10)
					}
				}
			}
		}
		for _, candidate := range typed {
			if found := findStringValue(candidate, depth+1, names...); found != "" {
				return found
			}
		}
	case []any:
		for _, candidate := range typed {
			if found := findStringValue(candidate, depth+1, names...); found != "" {
				return found
			}
		}
	}
	return ""
}

func pullRequestURL(request SubmitRequest) string {
	return fmt.Sprintf(
		"https://github.com/%s/%s/pull/%d",
		url.PathEscape(request.Owner),
		url.PathEscape(request.Repo),
		request.PullNumber,
	)
}
