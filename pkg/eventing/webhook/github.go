package webhook

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/sipeed/picoclaw/pkg/eventing"
)

const (
	githubSignatureHeader = "X-Hub-Signature-256"
	githubEventHeader     = "X-Github-Event"
	githubDeliveryHeader  = "X-Github-Delivery"

	githubSignaturePrefix          = "sha256="
	maxGitHubEventHeaderBytes      = 128
	maxGitHubActionBytes           = 127
	maxGitHubEntityFieldBytes      = 1024
	maxGitHubAttributeValueBytes   = 2048
	maxGitHubNormalizedEventBytes  = 256
	githubSignatureAlgorithm       = "hmac-sha256"
	githubAuthenticatedBodyValue   = "true"
	githubUnauthenticatedHeadValue = "false"
)

type githubSenderPayload struct {
	Login   string          `json:"login"`
	ID      json.RawMessage `json:"id"`
	NodeID  string          `json:"node_id"`
	Type    string          `json:"type"`
	HTMLURL string          `json:"html_url"`
}

type githubRepositoryPayload struct {
	ID            json.RawMessage `json:"id"`
	NodeID        string          `json:"node_id"`
	Name          string          `json:"name"`
	FullName      string          `json:"full_name"`
	HTMLURL       string          `json:"html_url"`
	DefaultBranch string          `json:"default_branch"`
	Visibility    string          `json:"visibility"`
	Private       *bool           `json:"private"`
	Fork          *bool           `json:"fork"`
	Owner         struct {
		Login string `json:"login"`
	} `json:"owner"`
}

type githubUserPayload struct {
	Login string          `json:"login"`
	ID    json.RawMessage `json:"id"`
}

type githubTeamPayload struct {
	Slug string `json:"slug"`
	Name string `json:"name"`
}

type githubRefPayload struct {
	Ref        string                     `json:"ref"`
	SHA        string                     `json:"sha"`
	Repository githubRefRepositoryPayload `json:"repo"`
}

type githubRefRepositoryPayload struct {
	FullName string `json:"full_name"`
}

type githubPullRequestPayload struct {
	ID                 json.RawMessage     `json:"id"`
	Number             json.RawMessage     `json:"number"`
	HTMLURL            string              `json:"html_url"`
	Title              string              `json:"title"`
	Body               string              `json:"body"`
	Draft              *bool               `json:"draft"`
	User               githubUserPayload   `json:"user"`
	Head               githubRefPayload    `json:"head"`
	Base               githubRefPayload    `json:"base"`
	RequestedReviewers []githubUserPayload `json:"requested_reviewers"`
}

type githubIssuePayload struct {
	Number    json.RawMessage     `json:"number"`
	HTMLURL   string              `json:"html_url"`
	Title     string              `json:"title"`
	Body      string              `json:"body"`
	User      githubUserPayload   `json:"user"`
	Assignees []githubUserPayload `json:"assignees"`
}

type githubCommentPayload struct {
	HTMLURL string            `json:"html_url"`
	Body    string            `json:"body"`
	User    githubUserPayload `json:"user"`
}

type githubReviewPayload struct {
	ID          json.RawMessage   `json:"id"`
	NodeID      string            `json:"node_id"`
	HTMLURL     string            `json:"html_url"`
	Body        string            `json:"body"`
	User        githubUserPayload `json:"user"`
	State       string            `json:"state"`
	CommitID    string            `json:"commit_id"`
	SubmittedAt string            `json:"submitted_at"`
}

func githubAuthenticationHeaders(
	headers http.Header,
) (admissionAuthentication, bool) {
	signature, signatureOK := exactlyOneHeader(headers, githubSignatureHeader)
	event, eventOK := exactlyOneHeader(headers, githubEventHeader)
	delivery, deliveryOK := exactlyOneHeader(headers, githubDeliveryHeader)
	digest, digestOK := canonicalGitHubDigest(signature)
	if !signatureOK ||
		!eventOK ||
		!deliveryOK ||
		!digestOK ||
		!validGitHubName(event, maxGitHubEventHeaderBytes) ||
		!validGitHubDelivery(delivery) {
		return admissionAuthentication{}, false
	}
	return admissionAuthentication{
		dedupeKey:    delivery,
		githubEvent:  event,
		githubDigest: digest,
	}, true
}

func canonicalGitHubDigest(signature string) ([]byte, bool) {
	if len(signature) != len(githubSignaturePrefix)+sha256.Size*2 ||
		!strings.HasPrefix(signature, githubSignaturePrefix) {
		return nil, false
	}
	encoded := strings.TrimPrefix(signature, githubSignaturePrefix)
	digest, err := hex.DecodeString(encoded)
	if err != nil || hex.EncodeToString(digest) != encoded {
		return nil, false
	}
	return digest, true
}

func verifyGitHubSignature(secret, body, digest []byte) bool {
	if len(secret) == 0 || len(digest) != sha256.Size {
		return false
	}
	mac := hmac.New(sha256.New, secret)
	_, _ = mac.Write(body)
	return hmac.Equal(mac.Sum(nil), digest)
}

func decodeGitHubAdmissionRequest(
	body []byte,
	event string,
	targetUser string,
) (admissionRequest, error) {
	fields, payload, err := decodeGitHubObject(body)
	if err != nil {
		return admissionRequest{}, err
	}

	eventType := event
	if raw, exists := fields["action"]; exists {
		action, decodeErr := decodeString(raw)
		if decodeErr != nil ||
			!validGitHubName(action, maxGitHubActionBytes) {
			return admissionRequest{}, errors.New("GitHub action must be a canonical string")
		}
		eventType += "." + action
	}
	if len(eventType) > maxGitHubNormalizedEventBytes {
		return admissionRequest{}, errors.New("GitHub event type is too long")
	}

	actor := githubActor(fields["sender"])
	subject, repositoryAttributes := githubSubject(fields["repository"])
	repositoryDatabaseID, _ := githubRepositoryDatabaseID(fields["repository"])
	attributeValues := map[string]string{
		"body_authenticated":     githubAuthenticatedBodyValue,
		"source_authenticated":   githubAuthenticatedBodyValue,
		"headers_authenticated":  githubUnauthenticatedHeadValue,
		"signature_algorithm":    githubSignatureAlgorithm,
		"repository_id":          repositoryAttributes["id"],
		"repository_database_id": repositoryDatabaseID,
		"repository_full_name":   repositoryAttributes["full_name"],
		"repository_url":         repositoryAttributes["url"],
		"repository_owner":       repositoryAttributes["owner"],
		"repository_visibility":  repositoryAttributes["visibility"],
		"repository_private":     repositoryAttributes["private"],
		"repository_branch":      repositoryAttributes["default_branch"],
	}
	for key, value := range githubResourceAttributes(fields, eventType, targetUser) {
		attributeValues[key] = value
	}
	attributes := githubAttributes(attributeValues)

	return admissionRequest{
		eventType:  eventType,
		actor:      actor,
		subject:    subject,
		payload:    payload,
		attributes: attributes,
	}, nil
}

func decodeGitHubObject(
	body []byte,
) (map[string]json.RawMessage, json.RawMessage, error) {
	if !utf8.Valid(body) {
		return nil, nil, errors.New("GitHub payload is not valid UTF-8")
	}
	trimmed := bytes.TrimSpace(body)
	decoder := json.NewDecoder(bytes.NewReader(trimmed))
	token, err := decoder.Token()
	if err != nil || token != json.Delim('{') {
		return nil, nil, errors.New("GitHub payload must be a JSON object")
	}

	fields := make(map[string]json.RawMessage)
	seen := make(map[string]struct{})
	for decoder.More() {
		nameToken, tokenErr := decoder.Token()
		name, nameOK := nameToken.(string)
		if tokenErr != nil || !nameOK {
			return nil, nil, errors.New("GitHub payload has an invalid field name")
		}
		if _, duplicate := seen[name]; duplicate {
			return nil, nil, errors.New("GitHub payload has a duplicate field")
		}
		seen[name] = struct{}{}

		var raw json.RawMessage
		if decodeErr := decoder.Decode(&raw); decodeErr != nil {
			return nil, nil, errors.New("GitHub payload has an invalid field value")
		}
		switch name {
		case "action",
			"sender",
			"repository",
			"pull_request",
			"issue",
			"comment",
			"review",
			"requested_reviewer",
			"requested_team",
			"assignee":
			fields[name] = raw
		}
	}
	if _, err = decoder.Token(); err != nil {
		return nil, nil, errors.New("GitHub payload is incomplete")
	}
	var trailing any
	if err = decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return nil, nil, errors.New("GitHub payload contains trailing data")
	}
	return fields, append(json.RawMessage(nil), trimmed...), nil
}

func githubResourceAttributes(
	fields map[string]json.RawMessage,
	eventType string,
	targetUser string,
) map[string]string {
	values := make(map[string]string)
	var mentionText []string
	var targetReasons []string
	var pullRequestAuthor string
	var pullRequestAuthorCanonical bool
	var reviewAuthor string
	var reviewAuthorCanonical bool
	var reviewFeedbackMetadataCanonical bool
	_, repositoryIdentityCanonical := githubRepositoryDatabaseID(fields["repository"])
	var pullRequestIdentityCanonical bool

	var pullRequest githubPullRequestPayload
	if decodeGitHubProjection(fields["pull_request"], &pullRequest) {
		pullRequestID, pullRequestIDCanonical := canonicalGitHubDatabaseID(
			pullRequest.ID,
		)
		pullRequestAuthorID, pullRequestAuthorIDCanonical := canonicalGitHubDatabaseID(
			pullRequest.User.ID,
		)
		pullRequestIdentityCanonical = repositoryIdentityCanonical &&
			pullRequestIDCanonical && pullRequestAuthorIDCanonical
		pullRequestAuthor = pullRequest.User.Login
		pullRequestAuthorCanonical = validGitHubTargetUser(pullRequestAuthor)
		values["pull_request_id"] = pullRequestID
		values["pull_request_author_id"] = pullRequestAuthorID
		values["pull_request_number"] = githubDatabaseID(pullRequest.Number)
		values["pull_request_url"] = pullRequest.HTMLURL
		values["pull_request_author"] = pullRequestAuthor
		values["pull_request_head_ref"] = pullRequest.Head.Ref
		values["pull_request_head_sha"] = pullRequest.Head.SHA
		values["pull_request_head_repository"] = pullRequest.Head.Repository.FullName
		values["pull_request_base_ref"] = pullRequest.Base.Ref
		values["pull_request_base_sha"] = pullRequest.Base.SHA
		values["pull_request_base_repository"] = pullRequest.Base.Repository.FullName
		values["pull_request_draft"] = githubBool(pullRequest.Draft)
		mentionText = append(mentionText, pullRequest.Title, pullRequest.Body)
		if githubUsersContain(pullRequest.RequestedReviewers, targetUser) {
			targetReasons = append(targetReasons, "requested_reviewer")
		}
	}

	var issue githubIssuePayload
	if decodeGitHubProjection(fields["issue"], &issue) {
		values["issue_number"] = githubDatabaseID(issue.Number)
		values["issue_url"] = issue.HTMLURL
		values["issue_author"] = issue.User.Login
		mentionText = append(mentionText, issue.Title, issue.Body)
		if githubUsersContain(issue.Assignees, targetUser) {
			targetReasons = append(targetReasons, "assignee")
		}
	}

	var comment githubCommentPayload
	if decodeGitHubProjection(fields["comment"], &comment) {
		values["comment_url"] = comment.HTMLURL
		values["comment_author"] = comment.User.Login
		mentionText = append(mentionText, comment.Body)
	}

	var review githubReviewPayload
	if decodeGitHubProjection(fields["review"], &review) {
		reviewAuthor = review.User.Login
		reviewAuthorCanonical = validGitHubReviewAuthor(reviewAuthor)
		reviewID, reviewIDCanonical := canonicalGitHubDatabaseID(review.ID)
		reviewNodeID, reviewNodeIDCanonical := canonicalGitHubNodeID(review.NodeID)
		_, reviewStateCanonical := canonicalGitHubReviewState(review.State)
		reviewCommitSHA, reviewCommitCanonical := canonicalGitHubCommitSHA(review.CommitID)
		reviewSubmittedAt, reviewTimestampCanonical := canonicalGitHubTimestamp(
			review.SubmittedAt,
		)
		reviewFeedbackMetadataCanonical = reviewIDCanonical &&
			reviewNodeIDCanonical &&
			reviewStateCanonical &&
			reviewCommitCanonical &&
			reviewTimestampCanonical

		values["review_id"] = reviewID
		values["review_node_id"] = reviewNodeID
		values["review_url"] = review.HTMLURL
		values["review_author"] = reviewAuthor
		values["review_state"] = strings.ToLower(review.State)
		values["review_commit_sha"] = reviewCommitSHA
		values["review_submitted_at"] = reviewSubmittedAt
		mentionText = append(mentionText, review.Body)
	}

	var requestedReviewer githubUserPayload
	if decodeGitHubProjection(fields["requested_reviewer"], &requestedReviewer) {
		values["requested_reviewer"] = requestedReviewer.Login
		if eventType != "pull_request.review_request_removed" &&
			githubLoginMatches(requestedReviewer.Login, targetUser) {
			targetReasons = append(targetReasons, "requested_reviewer")
		}
	}

	var requestedTeam githubTeamPayload
	if decodeGitHubProjection(fields["requested_team"], &requestedTeam) {
		values["requested_team"] = requestedTeam.Slug
		if values["requested_team"] == "" {
			values["requested_team"] = requestedTeam.Name
		}
	}

	var assignee githubUserPayload
	if decodeGitHubProjection(fields["assignee"], &assignee) {
		values["assignee"] = assignee.Login
		if eventType != "issues.unassigned" &&
			githubLoginMatches(assignee.Login, targetUser) {
			targetReasons = append(targetReasons, "assignee")
		}
	}

	targetUser = strings.TrimSpace(targetUser)
	if targetUser != "" {
		pullRequestAuthorIsTarget := pullRequestAuthorCanonical &&
			strings.EqualFold(pullRequestAuthor, targetUser)
		reviewAuthorIsTarget := reviewAuthorCanonical &&
			strings.EqualFold(reviewAuthor, targetUser)
		values["pull_request_author_is_target"] = githubBoolean(
			pullRequestAuthorIsTarget,
		)
		values["review_author_is_target"] = githubBoolean(reviewAuthorIsTarget)
		if eventType == "pull_request_review.submitted" &&
			pullRequestAuthorIsTarget &&
			reviewAuthorCanonical &&
			!strings.EqualFold(reviewAuthor, pullRequestAuthor) &&
			reviewFeedbackMetadataCanonical &&
			pullRequestIdentityCanonical {
			targetReasons = append(targetReasons, "review_feedback")
		}
		for _, text := range mentionText {
			if githubTextMentionsUser(text, targetUser) {
				targetReasons = append(targetReasons, "mention")
				break
			}
		}
		targetReasons = uniqueGitHubStrings(targetReasons)
		values["target_user"] = targetUser
		values["targets_user"] = "false"
		if len(targetReasons) > 0 {
			values["targets_user"] = "true"
			values["target_reason"] = strings.Join(targetReasons, ",")
		}
	}
	return values
}

func canonicalGitHubDatabaseID(raw json.RawMessage) (string, bool) {
	value := githubDatabaseID(raw)
	parsed, err := strconv.ParseInt(value, 10, 64)
	if err != nil || parsed <= 0 || strconv.FormatInt(parsed, 10) != value {
		return "", false
	}
	return value, true
}

func githubRepositoryDatabaseID(raw json.RawMessage) (string, bool) {
	var repository githubRepositoryPayload
	if !decodeGitHubProjection(raw, &repository) {
		return "", false
	}
	return canonicalGitHubDatabaseID(repository.ID)
}

func canonicalGitHubReviewState(value string) (string, bool) {
	switch value {
	case "approved", "changes_requested", "commented":
		return value, true
	default:
		return "", false
	}
}

func canonicalGitHubNodeID(value string) (string, bool) {
	value = githubStableID(value)
	if value == "" {
		return "", false
	}
	for _, char := range []byte(value) {
		if char >= 'a' && char <= 'z' ||
			char >= 'A' && char <= 'Z' ||
			char >= '0' && char <= '9' ||
			char == '_' ||
			char == '-' ||
			char == '+' ||
			char == '/' ||
			char == '=' {
			continue
		}
		return "", false
	}
	return value, true
}

func validGitHubReviewAuthor(value string) bool {
	if validGitHubTargetUser(value) {
		return true
	}
	base, bot := strings.CutSuffix(value, "[bot]")
	return bot && validGitHubTargetUser(base)
}

func canonicalGitHubCommitSHA(value string) (string, bool) {
	if len(value) != 40 && len(value) != 64 {
		return "", false
	}
	for _, char := range []byte(value) {
		if char >= '0' && char <= '9' || char >= 'a' && char <= 'f' {
			continue
		}
		return "", false
	}
	return value, true
}

func canonicalGitHubTimestamp(value string) (string, bool) {
	if value == "" ||
		len(value) > maxGitHubAttributeValueBytes ||
		value != strings.TrimSpace(value) {
		return "", false
	}
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return "", false
	}
	return parsed.UTC().Format(time.RFC3339Nano), true
}

func githubBoolean(value bool) string {
	if value {
		return "true"
	}
	return "false"
}

func decodeGitHubProjection(raw json.RawMessage, destination any) bool {
	if len(raw) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return false
	}
	return json.Unmarshal(raw, destination) == nil
}

func githubUsersContain(users []githubUserPayload, targetUser string) bool {
	for _, user := range users {
		if githubLoginMatches(user.Login, targetUser) {
			return true
		}
	}
	return false
}

func githubLoginMatches(login, targetUser string) bool {
	return targetUser != "" && strings.EqualFold(strings.TrimSpace(login), targetUser)
}

func githubTextMentionsUser(text, targetUser string) bool {
	text = strings.ToLower(text)
	targetUser = strings.ToLower(strings.TrimSpace(targetUser))
	if text == "" || targetUser == "" {
		return false
	}
	needle := "@" + targetUser
	for offset := 0; offset <= len(text)-len(needle); {
		index := strings.Index(text[offset:], needle)
		if index < 0 {
			return false
		}
		index += offset
		after := index + len(needle)
		beforeBoundary := index == 0 || !githubLoginByte(text[index-1])
		afterBoundary := after == len(text) || !githubLoginByte(text[after])
		if beforeBoundary && afterBoundary {
			return true
		}
		offset = index + len(needle)
	}
	return false
}

func githubLoginByte(value byte) bool {
	return value >= 'a' && value <= 'z' ||
		value >= '0' && value <= '9' ||
		value == '-'
}

func uniqueGitHubStrings(values []string) []string {
	out := make([]string, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	return out
}

func githubActor(raw json.RawMessage) *eventing.Actor {
	if len(raw) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return nil
	}
	var sender githubSenderPayload
	if err := json.Unmarshal(raw, &sender); err != nil {
		return nil
	}

	databaseID := githubDatabaseID(sender.ID)
	id := githubStableID(sender.NodeID)
	if id == "" {
		id = databaseID
	}
	actorType := boundedGitHubString(
		strings.ToLower(sender.Type),
		maxGitHubEntityFieldBytes,
	)
	login := boundedGitHubString(sender.Login, maxGitHubEntityFieldBytes)
	url := boundedGitHubString(sender.HTMLURL, maxGitHubAttributeValueBytes)
	if id == "" && actorType == "" && login == "" && url == "" {
		return nil
	}
	return &eventing.Actor{
		ID:          id,
		Type:        actorType,
		DisplayName: login,
		Attributes: githubAttributes(map[string]string{
			"database_id": databaseID,
			"login":       login,
			"url":         url,
		}),
	}
}

func githubSubject(
	raw json.RawMessage,
) (*eventing.Subject, map[string]string) {
	if len(raw) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return nil, nil
	}
	var repository githubRepositoryPayload
	if err := json.Unmarshal(raw, &repository); err != nil {
		return nil, nil
	}

	databaseID := githubDatabaseID(repository.ID)
	nodeID := githubStableID(repository.NodeID)
	id := nodeID
	if id == "" {
		id = databaseID
	}
	name := boundedGitHubString(repository.FullName, maxGitHubEntityFieldBytes)
	if name == "" {
		name = boundedGitHubString(repository.Name, maxGitHubEntityFieldBytes)
	}
	url := boundedGitHubString(repository.HTMLURL, maxGitHubEntityFieldBytes)
	repositoryAttributes := githubAttributes(map[string]string{
		"id":             id,
		"database_id":    databaseID,
		"node_id":        nodeID,
		"full_name":      name,
		"url":            url,
		"owner":          repository.Owner.Login,
		"default_branch": repository.DefaultBranch,
		"visibility":     repository.Visibility,
		"private":        githubBool(repository.Private),
		"fork":           githubBool(repository.Fork),
	})
	if len(repositoryAttributes) == 0 {
		return nil, nil
	}
	return &eventing.Subject{
		ID:   id,
		Type: "repository",
		Name: name,
		URL:  url,
		Attributes: githubAttributes(map[string]string{
			"database_id":    databaseID,
			"node_id":        nodeID,
			"owner":          repositoryAttributes["owner"],
			"default_branch": repositoryAttributes["default_branch"],
			"visibility":     repositoryAttributes["visibility"],
			"private":        repositoryAttributes["private"],
			"fork":           repositoryAttributes["fork"],
		}),
	}, repositoryAttributes
}

func githubDatabaseID(raw json.RawMessage) string {
	value := strings.TrimSpace(string(raw))
	if value == "" || len(value) > maxGitHubEntityFieldBytes {
		return ""
	}
	for _, char := range []byte(value) {
		if char < '0' || char > '9' {
			return ""
		}
	}
	return value
}

func githubBool(value *bool) string {
	switch {
	case value == nil:
		return ""
	case *value:
		return "true"
	default:
		return "false"
	}
}

func githubAttributes(values map[string]string) map[string]string {
	attributes := make(map[string]string, len(values))
	for key, value := range values {
		value = boundedGitHubString(value, maxGitHubAttributeValueBytes)
		if value != "" {
			attributes[key] = value
		}
	}
	if len(attributes) == 0 {
		return nil
	}
	return attributes
}

func boundedGitHubString(value string, maximum int) string {
	value = strings.TrimSpace(value)
	if len(value) <= maximum {
		return value
	}
	value = value[:maximum]
	for !utf8.ValidString(value) {
		value = value[:len(value)-1]
	}
	return value
}

func validGitHubName(value string, maximum int) bool {
	if value == "" || len(value) > maximum {
		return false
	}
	for _, char := range []byte(value) {
		if char >= 'a' && char <= 'z' ||
			char >= '0' && char <= '9' ||
			char == '_' {
			continue
		}
		return false
	}
	return true
}

func validGitHubDelivery(value string) bool {
	if value == "" ||
		len(value) > maxWebhookIDBytes ||
		strings.TrimSpace(value) != value {
		return false
	}
	for _, char := range []byte(value) {
		if char < '!' || char > '~' || char == ',' {
			return false
		}
	}
	return true
}

func githubStableID(value string) string {
	if value == "" ||
		len(value) > maxGitHubEntityFieldBytes ||
		strings.TrimSpace(value) != value {
		return ""
	}
	return value
}
