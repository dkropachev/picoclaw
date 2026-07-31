// Package reviews turns opt-in workflow output into durable, human-editable
// pull-request review cases.
package reviews

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/sipeed/picoclaw/pkg/eventing"
	"github.com/sipeed/picoclaw/pkg/workflows"
)

const (
	// WorkflowDraftOutput is the reserved top-level workflow output captured
	// after a successful external-event run.
	WorkflowDraftOutput = "picoclawReviewDraft"

	maxCapturedDraftBytes = 1 << 20
)

var captureRepositoryPattern = regexp.MustCompile(
	`^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$`,
)

// CaptureSink implements workflows.EventReviewSink over the durable event
// store. A workflow without the reserved output is deliberately ignored.
type ReviewCapturer interface {
	CaptureReview(
		context.Context,
		eventing.ReviewCaptureInput,
	) (eventing.ReviewCase, bool, error)
}

type CaptureSink struct {
	Store ReviewCapturer
}

func (s *CaptureSink) CaptureSucceededEventRun(
	ctx context.Context,
	envelope eventing.Envelope,
	dispatch eventing.Dispatch,
	run *workflows.Run,
) error {
	if s == nil || s.Store == nil {
		return errors.New("review capture store is required")
	}
	if run == nil {
		return errors.New("review capture run is required")
	}
	raw, present := run.Outputs[WorkflowDraftOutput]
	if !present {
		return nil
	}
	draft, err := decodeWorkflowReviewDraft(raw)
	if err != nil {
		return fmt.Errorf("decode %s output: %w", WorkflowDraftOutput, err)
	}
	input, err := reviewCaptureInput(envelope, dispatch, run, draft)
	if err != nil {
		return err
	}
	_, _, err = s.Store.CaptureReview(ctx, input)
	if err != nil {
		return fmt.Errorf("persist pull request review: %w", err)
	}
	return nil
}

func decodeWorkflowReviewDraft(value any) (eventing.ReviewDraft, error) {
	data, err := json.Marshal(value)
	if err != nil {
		return eventing.ReviewDraft{}, err
	}
	if len(data) == 0 || len(data) > maxCapturedDraftBytes {
		return eventing.ReviewDraft{}, errors.New("review draft exceeds the capture limit")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var draft eventing.ReviewDraft
	if err := decoder.Decode(&draft); err != nil {
		return eventing.ReviewDraft{}, err
	}
	if err := ensureReviewDraftEOF(decoder); err != nil {
		return eventing.ReviewDraft{}, err
	}
	if draft.SchemaVersion != eventing.ReviewDraftSchemaVersion {
		return eventing.ReviewDraft{}, fmt.Errorf(
			"unsupported review draft schema version %d",
			draft.SchemaVersion,
		)
	}
	return draft, nil
}

func ensureReviewDraftEOF(decoder *json.Decoder) error {
	var extra any
	if err := decoder.Decode(&extra); errors.Is(err, io.EOF) {
		return nil
	} else if err != nil {
		return err
	}
	return errors.New("review draft contains trailing JSON")
}

func reviewCaptureInput(
	envelope eventing.Envelope,
	dispatch eventing.Dispatch,
	run *workflows.Run,
	draft eventing.ReviewDraft,
) (eventing.ReviewCaptureInput, error) {
	authenticatedWebhook := envelope.Attributes["body_authenticated"] == "true" &&
		envelope.Attributes["provider_authenticated"] != "true"
	authenticatedProviderPoll := envelope.Attributes["provider_authenticated"] == "true" &&
		envelope.Attributes["body_authenticated"] != "true" &&
		envelope.Attributes["notification_id"] != "" &&
		envelope.Attributes["notification_reason"] == "review_requested"
	if envelope.Source != "github" ||
		envelope.Type != "pull_request.review_requested" ||
		envelope.Attributes["source_authenticated"] != "true" ||
		envelope.Attributes["targets_user"] != "true" ||
		(!authenticatedWebhook && !authenticatedProviderPoll) {
		return eventing.ReviewCaptureInput{}, errors.New(
			"review capture requires an authenticated GitHub review-request event",
		)
	}
	if envelope.ID == "" ||
		dispatch.ID == "" ||
		dispatch.EventID != envelope.ID ||
		dispatch.RunID != run.ID ||
		dispatch.WorkflowRef != run.WorkflowRef {
		return eventing.ReviewCaptureInput{}, errors.New(
			"review capture event, dispatch, and run identity do not match",
		)
	}
	repository := envelope.Attributes["repository_full_name"]
	if repository == "" ||
		len(repository) > 256 ||
		repository != strings.TrimSpace(repository) ||
		!utf8.ValidString(repository) ||
		!captureRepositoryPattern.MatchString(repository) {
		return eventing.ReviewCaptureInput{}, errors.New(
			"review capture repository identity is invalid",
		)
	}
	pullNumber, err := strconv.ParseInt(
		envelope.Attributes["pull_request_number"],
		10,
		64,
	)
	if err != nil || pullNumber <= 0 {
		return eventing.ReviewCaptureInput{}, errors.New(
			"review capture pull request number is invalid",
		)
	}
	pullURL := envelope.Attributes["pull_request_url"]
	if !validPullRequestURL(pullURL) {
		return eventing.ReviewCaptureInput{}, errors.New(
			"review capture pull request URL is invalid",
		)
	}
	baseSHA := envelope.Attributes["pull_request_base_sha"]
	headSHA := envelope.Attributes["pull_request_head_sha"]
	if !validGitObjectID(baseSHA) || !validGitObjectID(headSHA) {
		return eventing.ReviewCaptureInput{}, errors.New(
			"review capture pull request revision is invalid",
		)
	}
	return eventing.ReviewCaptureInput{
		EventID:          envelope.ID,
		DispatchID:       dispatch.ID,
		RunID:            run.ID,
		WorkflowRef:      run.WorkflowRef,
		WorkflowRevision: dispatch.WorkflowRevision,
		Connector:        envelope.Connector,
		Repository:       repository,
		PullNumber:       pullNumber,
		PullURL:          pullURL,
		BaseSHA:          baseSHA,
		HeadSHA:          headSHA,
		Draft:            draft,
	}, nil
}

func validPullRequestURL(value string) bool {
	if value == "" || len(value) > 2048 || value != strings.TrimSpace(value) {
		return false
	}
	parsed, err := url.Parse(value)
	return err == nil &&
		parsed.Scheme == "https" &&
		parsed.Host != "" &&
		parsed.User == nil &&
		parsed.Fragment == ""
}

func validGitObjectID(value string) bool {
	if len(value) != 40 && len(value) != 64 {
		return false
	}
	for _, char := range []byte(value) {
		if char >= '0' && char <= '9' || char >= 'a' && char <= 'f' {
			continue
		}
		return false
	}
	return true
}

var _ workflows.EventReviewSink = (*CaptureSink)(nil)
