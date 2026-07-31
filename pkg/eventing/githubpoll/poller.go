// Package githubpoll ingests GitHub notifications through the configured
// read-only GitHub MCP tools. It deliberately shares the ordinary durable
// event inbox with webhook and channel ingress.
package githubpoll

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/sipeed/picoclaw/pkg/eventing"
	picomcp "github.com/sipeed/picoclaw/pkg/mcp"
	"github.com/sipeed/picoclaw/pkg/workflows"
)

const (
	// DefaultInterval is the intentionally conservative provider poll cadence.
	DefaultInterval = 60 * time.Second

	DefaultMCPServer      = "github"
	ListNotificationsTool = "list_notifications"
	PullRequestReadTool   = "pull_request_read"

	notificationsPerPage  = 50
	maxNotificationPages  = 5
	maxToolTextBytes      = 16 << 20
	maxNotificationID     = 256
	maxNotificationReason = 64
	maxRepositoryBytes    = 256
	maxEntityFieldBytes   = 2048
)

var repositoryPattern = regexp.MustCompile(
	`^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$`,
)

var notificationIDPattern = regexp.MustCompile(`^[A-Za-z0-9_.:-]+$`)

// Inserter is the narrow durable-inbox capability required by the poller.
type Inserter interface {
	Insert(ctx context.Context, envelope eventing.Envelope) (eventing.InsertResult, error)
}

// Connector is one enabled GitHub event source that opted into polling.
type Connector struct {
	Name         string
	Repositories []string
	TargetUser   string
}

// Config constructs one process-local poller. ToolRunner must be the dynamic
// workflow tool runner so hot-reloaded MCP connections are generation-fenced.
type Config struct {
	Store      Inserter
	ToolRunner workflows.ToolRunner
	Connectors []Connector
	// ArtifactRoot is the MCP artifact directory used when the ordinary tool
	// wrapper moves a large exact JSON text result out of model context.
	ArtifactRoot string
	Now          func() time.Time
}

// Poller reads provider notifications and projects them into durable events.
type Poller struct {
	store        Inserter
	tools        workflows.ToolRunner
	connectors   []connector
	artifactRoot string
	now          func() time.Time
}

type connector struct {
	name         string
	repositories map[string]struct{}
	targetUser   string
}

// PollResult reports one bounded provider scan.
type PollResult struct {
	Notifications int
	Matched       int
	Inserted      int
}

// New validates and freezes a polling configuration.
func New(config Config) (*Poller, error) {
	if config.Store == nil {
		return nil, errors.New("GitHub notification poll store is required")
	}
	if config.ToolRunner == nil {
		return nil, errors.New("GitHub notification poll tool runner is required")
	}
	if len(config.Connectors) == 0 {
		return nil, errors.New("at least one GitHub notification connector is required")
	}

	connectors := make([]connector, 0, len(config.Connectors))
	names := make(map[string]struct{}, len(config.Connectors))
	for _, input := range config.Connectors {
		name := strings.TrimSpace(input.Name)
		if name == "" || !utf8.ValidString(name) || len(name) > maxRepositoryBytes {
			return nil, errors.New("GitHub notification connector name is invalid")
		}
		foldedName := strings.ToLower(name)
		if _, exists := names[foldedName]; exists {
			return nil, fmt.Errorf("duplicate GitHub notification connector %q", name)
		}
		names[foldedName] = struct{}{}

		repositories := make(map[string]struct{}, len(input.Repositories))
		for _, repository := range input.Repositories {
			if !validRepository(repository) {
				return nil, fmt.Errorf(
					"GitHub notification connector %q has invalid repository %q",
					name,
					repository,
				)
			}
			folded := strings.ToLower(repository)
			if _, exists := repositories[folded]; exists {
				return nil, fmt.Errorf(
					"GitHub notification connector %q has duplicate repository %q",
					name,
					repository,
				)
			}
			repositories[folded] = struct{}{}
		}
		connectors = append(connectors, connector{
			name:         name,
			repositories: repositories,
			targetUser:   strings.TrimSpace(input.TargetUser),
		})
	}
	now := config.Now
	if now == nil {
		now = time.Now
	}
	return &Poller{
		store:        config.Store,
		tools:        config.ToolRunner,
		connectors:   connectors,
		artifactRoot: strings.TrimSpace(config.ArtifactRoot),
		now:          now,
	}, nil
}

// Poll performs one bounded, read-only provider scan. It never acknowledges,
// marks, dismisses, or otherwise mutates a GitHub notification.
func (p *Poller) Poll(ctx context.Context) (PollResult, error) {
	if p == nil || p.store == nil || p.tools == nil {
		return PollResult{}, errors.New("GitHub notification poller is not configured")
	}
	var result PollResult
	for page := 1; page <= maxNotificationPages; page++ {
		notifications, err := p.listNotifications(ctx, page)
		if err != nil {
			return result, err
		}
		result.Notifications += len(notifications)
		for _, notification := range notifications {
			matched, inserted, pollErr := p.ingestNotification(ctx, notification)
			result.Matched += matched
			result.Inserted += inserted
			if pollErr != nil {
				return result, pollErr
			}
		}
		if len(notifications) < notificationsPerPage {
			break
		}
	}
	return result, nil
}

func (p *Poller) listNotifications(
	ctx context.Context,
	page int,
) ([]notification, error) {
	outputs, err := p.tools.RunTool(ctx, workflows.ToolRequest{
		Name: picomcp.CanonicalToolName(DefaultMCPServer, ListNotificationsTool),
		Args: map[string]any{
			"filter":  "include_read_notifications",
			"page":    page,
			"perPage": notificationsPerPage,
		},
		MCP:       true,
		MCPServer: DefaultMCPServer,
		MCPTool:   ListNotificationsTool,
	})
	if err != nil {
		return nil, fmt.Errorf("list GitHub notifications page %d: %w", page, err)
	}
	text, err := p.exactToolText(outputs)
	if err != nil {
		return nil, fmt.Errorf("decode GitHub notifications page %d: %w", page, err)
	}
	var notifications []notification
	if err := decodeExactJSON(text, &notifications); err != nil {
		return nil, fmt.Errorf("decode GitHub notifications page %d: %w", page, err)
	}
	if notifications == nil {
		notifications = []notification{}
	}
	if len(notifications) > notificationsPerPage {
		return nil, fmt.Errorf(
			"decode GitHub notifications page %d: provider returned %d items; maximum is %d",
			page,
			len(notifications),
			notificationsPerPage,
		)
	}
	return notifications, nil
}

func (p *Poller) ingestNotification(
	ctx context.Context,
	item notification,
) (int, int, error) {
	if err := item.validate(); err != nil {
		return 0, 0, err
	}
	repository := item.Repository.FullName
	connectors := p.matchingConnectors(repository)
	if len(connectors) == 0 {
		return 0, 0, nil
	}

	var pullRequest *pullRequest
	if strings.EqualFold(item.Subject.Type, "PullRequest") {
		owner, repo, number, parseErr := pullRequestIdentity(item)
		if parseErr != nil {
			return 0, 0, parseErr
		}
		resolvedPullRequest, readErr := p.readPullRequest(ctx, owner, repo, number)
		if readErr != nil {
			return 0, 0, readErr
		}
		pullRequest = resolvedPullRequest
		if validationErr := validatePullRequestEnrichment(*pullRequest, repository, number); validationErr != nil {
			return 0, 0, fmt.Errorf(
				"GitHub notification %q pull-request enrichment is incomplete: %w",
				item.ID,
				validationErr,
			)
		}
	}

	matched := len(connectors)
	inserted := 0
	for _, connector := range connectors {
		envelope, buildErr := p.envelope(item, pullRequest, connector)
		if buildErr != nil {
			return matched, inserted, buildErr
		}
		stored, insertErr := p.store.Insert(ctx, envelope)
		if insertErr != nil {
			return matched, inserted, fmt.Errorf(
				"store GitHub notification %q for connector %q: %w",
				item.ID,
				connector.name,
				insertErr,
			)
		}
		if stored.Inserted {
			inserted++
		}
	}
	return matched, inserted, nil
}

func (p *Poller) matchingConnectors(repository string) []connector {
	folded := strings.ToLower(repository)
	out := make([]connector, 0, len(p.connectors))
	for _, connector := range p.connectors {
		if len(connector.repositories) == 0 {
			out = append(out, connector)
			continue
		}
		if _, exists := connector.repositories[folded]; exists {
			out = append(out, connector)
		}
	}
	return out
}

func (p *Poller) readPullRequest(
	ctx context.Context,
	owner, repo string,
	number int,
) (*pullRequest, error) {
	outputs, err := p.tools.RunTool(ctx, workflows.ToolRequest{
		Name: picomcp.CanonicalToolName(DefaultMCPServer, PullRequestReadTool),
		Args: map[string]any{
			"method":     "get",
			"owner":      owner,
			"repo":       repo,
			"pullNumber": number,
		},
		MCP:       true,
		MCPServer: DefaultMCPServer,
		MCPTool:   PullRequestReadTool,
	})
	if err != nil {
		return nil, fmt.Errorf(
			"read GitHub pull request %s/%s#%d: %w",
			owner,
			repo,
			number,
			err,
		)
	}
	text, err := p.exactToolText(outputs)
	if err != nil {
		return nil, fmt.Errorf(
			"decode GitHub pull request %s/%s#%d: %w",
			owner,
			repo,
			number,
			err,
		)
	}
	var value pullRequest
	if err := decodeExactJSON(text, &value); err != nil {
		return nil, fmt.Errorf(
			"decode GitHub pull request %s/%s#%d: %w",
			owner,
			repo,
			number,
			err,
		)
	}
	return &value, nil
}

func (p *Poller) exactToolText(outputs map[string]any) ([]byte, error) {
	if outputs == nil {
		return nil, errors.New("MCP result is missing")
	}
	text, ok := outputs["text"].(string)
	if !ok {
		return nil, errors.New("MCP result text is missing")
	}
	text = strings.TrimSpace(text)
	if text == "" {
		return nil, errors.New("MCP result text is empty")
	}
	if len(text) > maxToolTextBytes || !utf8.ValidString(text) {
		return nil, errors.New("MCP result text is invalid or too large")
	}
	if rawTags, exists := outputs["artifact_tags"]; exists {
		tags, valid := rawTags.([]string)
		if !valid {
			return nil, errors.New("MCP exact text artifact tags are invalid")
		}
		if len(tags) > 0 {
			if p == nil || p.artifactRoot == "" {
				return nil, errors.New("MCP exact text artifact root is not configured")
			}
			artifact, artifactErr := p.exactArtifactText(outputs)
			if artifactErr != nil {
				return nil, fmt.Errorf("read MCP exact text artifact: %w", artifactErr)
			}
			return artifact, nil
		}
	}
	if strings.HasPrefix(text, "{") || strings.HasPrefix(text, "[") {
		candidate := []byte(text)
		if json.Valid(candidate) {
			return candidate, nil
		}
	}
	return nil, errors.New("MCP result is neither exact JSON nor a configured exact text artifact")
}

func (p *Poller) exactArtifactText(outputs map[string]any) ([]byte, error) {
	tags, ok := outputs["artifact_tags"].([]string)
	if !ok || len(tags) != 1 {
		return nil, errors.New("MCP exact text artifact is missing")
	}
	path, ok := strings.CutPrefix(tags[0], "[file:")
	if !ok || !strings.HasSuffix(path, "]") {
		return nil, errors.New("MCP exact text artifact tag is invalid")
	}
	path = strings.TrimSuffix(path, "]")
	root, err := filepath.Abs(p.artifactRoot)
	if err != nil {
		return nil, err
	}
	path, err = filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	tagInfo, err := os.Lstat(path)
	if err != nil || !tagInfo.Mode().IsRegular() {
		return nil, errors.New("MCP exact text artifact is not a regular file")
	}
	resolvedRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return nil, err
	}
	resolvedPath, err := filepath.EvalSymlinks(path)
	if err != nil {
		return nil, err
	}
	relative, err := filepath.Rel(resolvedRoot, resolvedPath)
	if err != nil ||
		relative == "." ||
		relative == ".." ||
		strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return nil, errors.New("MCP exact text artifact is outside the configured root")
	}
	file, err := os.Open(resolvedPath)
	if err != nil {
		return nil, err
	}
	info, err := file.Stat()
	if err != nil ||
		!info.Mode().IsRegular() ||
		!os.SameFile(tagInfo, info) ||
		info.Size() > maxToolTextBytes {
		_ = file.Close()
		return nil, errors.New("MCP exact text artifact is invalid or too large")
	}
	data, err := io.ReadAll(io.LimitReader(file, maxToolTextBytes+1))
	if err != nil || len(data) > maxToolTextBytes || !utf8.Valid(data) {
		_ = file.Close()
		return nil, errors.New("MCP exact text artifact is invalid or too large")
	}
	if err := file.Close(); err != nil {
		return nil, err
	}
	// The MCP wrapper created this unique file for this call. Removing it
	// prevents a one-file-per-minute leak.
	if err := os.Remove(resolvedPath); err != nil {
		return nil, fmt.Errorf("remove consumed MCP exact text artifact: %w", err)
	}
	return bytes.TrimSpace(data), nil
}

func decodeExactJSON(data []byte, destination any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	if err := decoder.Decode(destination); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); errors.Is(err, io.EOF) {
		return nil
	} else if err != nil {
		return err
	}
	return errors.New("MCP result contains trailing JSON")
}

type notification struct {
	ID         string                 `json:"id"`
	Repository notificationRepository `json:"repository"`
	Subject    notificationSubject    `json:"subject"`
	Reason     string                 `json:"reason"`
	Unread     bool                   `json:"unread"`
	UpdatedAt  string                 `json:"updated_at"`
	LastReadAt *string                `json:"last_read_at"`
	URL        string                 `json:"url"`
}

type notificationRepository struct {
	ID            json.RawMessage             `json:"id"`
	NodeID        string                      `json:"node_id"`
	Name          string                      `json:"name"`
	FullName      string                      `json:"full_name"`
	Private       bool                        `json:"private"`
	HTMLURL       string                      `json:"html_url"`
	URL           string                      `json:"url"`
	DefaultBranch string                      `json:"default_branch"`
	Owner         notificationRepositoryOwner `json:"owner"`
}

type notificationRepositoryOwner struct {
	Login string `json:"login"`
}

type notificationSubject struct {
	Title            string `json:"title"`
	URL              string `json:"url"`
	LatestCommentURL string `json:"latest_comment_url"`
	Type             string `json:"type"`
}

type pullRequest struct {
	Number  int                `json:"number"`
	Title   string             `json:"title"`
	Body    string             `json:"body"`
	Draft   bool               `json:"draft"`
	HTMLURL string             `json:"html_url"`
	User    *pullRequestUser   `json:"user"`
	Head    *pullRequestBranch `json:"head"`
	Base    *pullRequestBranch `json:"base"`
}

type pullRequestUser struct {
	Login string `json:"login"`
}

type pullRequestBranch struct {
	Ref  string                 `json:"ref"`
	SHA  string                 `json:"sha"`
	Repo *pullRequestBranchRepo `json:"repo"`
}

type pullRequestBranchRepo struct {
	FullName string `json:"full_name"`
}

func (item notification) validate() error {
	switch {
	case item.ID == "" ||
		item.ID != strings.TrimSpace(item.ID) ||
		len(item.ID) > maxNotificationID ||
		!notificationIDPattern.MatchString(item.ID):
		return errors.New("GitHub notification ID is invalid")
	case !validRepository(item.Repository.FullName):
		return fmt.Errorf("GitHub notification %q repository is invalid", item.ID)
	case !validHTTPSURL(item.Repository.HTMLURL):
		return fmt.Errorf("GitHub notification %q repository URL is invalid", item.ID)
	case item.Reason == "" ||
		item.Reason != strings.TrimSpace(item.Reason) ||
		len(item.Reason) > maxNotificationReason:
		return fmt.Errorf("GitHub notification %q reason is invalid", item.ID)
	case item.Subject.Type == "" || len(item.Subject.Type) > maxNotificationReason:
		return fmt.Errorf("GitHub notification %q subject type is invalid", item.ID)
	}
	if _, err := time.Parse(time.RFC3339, item.UpdatedAt); err != nil {
		return fmt.Errorf("GitHub notification %q updated_at is invalid", item.ID)
	}
	return nil
}

func pullRequestIdentity(item notification) (string, string, int, error) {
	owner, repo, ok := strings.Cut(item.Repository.FullName, "/")
	if !ok {
		return "", "", 0, errors.New("GitHub pull-request repository is invalid")
	}
	number, err := resourceNumber(item.Subject.URL, owner, repo, "pulls")
	if err != nil {
		return "", "", 0, fmt.Errorf(
			"GitHub notification %q pull-request identity is invalid: %w",
			item.ID,
			err,
		)
	}
	return owner, repo, number, nil
}

func resourceNumber(rawURL, owner, repo, collection string) (int, error) {
	parsed, err := url.Parse(rawURL)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" {
		return 0, errors.New("resource URL is invalid")
	}
	parts := strings.Split(strings.Trim(parsed.Path, "/"), "/")
	if len(parts) < 5 ||
		!strings.EqualFold(parts[len(parts)-5], "repos") ||
		!strings.EqualFold(parts[len(parts)-4], owner) ||
		!strings.EqualFold(parts[len(parts)-3], repo) ||
		!strings.EqualFold(parts[len(parts)-2], collection) {
		return 0, errors.New("resource URL does not match the notification repository")
	}
	number, err := strconv.Atoi(parts[len(parts)-1])
	if err != nil || number <= 0 {
		return 0, errors.New("resource number is invalid")
	}
	return number, nil
}

func validatePullRequestEnrichment(
	value pullRequest,
	repository string,
	number int,
) error {
	switch {
	case value.Number != number:
		return errors.New("pull request number does not match")
	case !validHTTPSURL(value.HTMLURL):
		return errors.New("pull request URL is missing")
	case value.User == nil || strings.TrimSpace(value.User.Login) == "":
		return errors.New("pull request author is missing")
	case value.Head == nil || !validGitObjectID(value.Head.SHA):
		return errors.New("pull request head revision is missing")
	case value.Base == nil || !validGitObjectID(value.Base.SHA):
		return errors.New("pull request base revision is missing")
	case value.Base.Repo == nil ||
		!validRepository(value.Base.Repo.FullName) ||
		!strings.EqualFold(value.Base.Repo.FullName, repository):
		return errors.New("pull request base repository does not match the notification")
	case value.Head.Repo != nil &&
		value.Head.Repo.FullName != "" &&
		!validRepository(value.Head.Repo.FullName):
		return errors.New("pull request head repository is invalid")
	case !validRepository(repository):
		return errors.New("repository identity is invalid")
	}
	return nil
}

func (p *Poller) envelope(
	item notification,
	pr *pullRequest,
	connector connector,
) (eventing.Envelope, error) {
	updatedAt, _ := time.Parse(time.RFC3339, item.UpdatedAt)
	eventType := notificationEventType(item.Subject.Type, item.Reason)
	attributes := map[string]string{
		"body_authenticated":     "false",
		"headers_authenticated":  "false",
		"provider_authenticated": "true",
		"source_authenticated":   "true",
		"notification_id":        item.ID,
		"notification_reason":    strings.ToLower(item.Reason),
		"repository_full_name":   item.Repository.FullName,
		"repository_url":         item.Repository.HTMLURL,
		"repository_owner":       item.Repository.Owner.Login,
		"repository_private":     strconv.FormatBool(item.Repository.Private),
		"repository_branch":      item.Repository.DefaultBranch,
	}
	if len(item.Repository.ID) > 0 {
		attributes["repository_id"] = strings.Trim(string(item.Repository.ID), `"`)
	}
	if connector.targetUser != "" {
		attributes["target_user"] = connector.targetUser
	}
	if targetReason := notificationTargetReason(item.Reason); targetReason != "" {
		attributes["targets_user"] = "true"
		attributes["target_reason"] = targetReason
	} else {
		attributes["targets_user"] = "false"
	}

	var actor *eventing.Actor
	payload := map[string]any{
		"notification": item,
		"repository":   webhookRepositoryPayload(item.Repository),
	}
	if pr != nil {
		attributes["pull_request_number"] = strconv.Itoa(pr.Number)
		attributes["pull_request_url"] = pr.HTMLURL
		attributes["pull_request_author"] = pr.User.Login
		attributes["pull_request_head_ref"] = pr.Head.Ref
		attributes["pull_request_head_sha"] = pr.Head.SHA
		attributes["pull_request_base_ref"] = pr.Base.Ref
		attributes["pull_request_base_sha"] = pr.Base.SHA
		attributes["pull_request_draft"] = strconv.FormatBool(pr.Draft)
		actor = &eventing.Actor{
			ID:          pr.User.Login,
			Type:        "user",
			DisplayName: pr.User.Login,
		}
		payload["pull_request"] = webhookPullRequestPayload(*pr, item.Repository.FullName)
	} else if strings.EqualFold(item.Subject.Type, "Issue") {
		owner, repo, _ := strings.Cut(item.Repository.FullName, "/")
		number, issueErr := resourceNumber(
			item.Subject.URL,
			owner,
			repo,
			"issues",
		)
		if issueErr != nil {
			return eventing.Envelope{}, fmt.Errorf(
				"GitHub notification %q issue identity is invalid: %w",
				item.ID,
				issueErr,
			)
		}
		attributes["issue_number"] = strconv.Itoa(number)
		attributes["issue_url"] = htmlResourceURL(
			item.Repository.HTMLURL,
			"issues",
			number,
		)
	}
	payloadJSON, err := json.Marshal(payload)
	if err != nil {
		return eventing.Envelope{}, err
	}
	return eventing.Envelope{
		Source:     "github",
		Connector:  connector.name,
		Type:       eventType,
		DedupeKey:  "notification/" + item.ID + "/" + updatedAt.UTC().Format(time.RFC3339Nano),
		Actor:      actor,
		Subject:    repositorySubject(item.Repository),
		OccurredAt: &updatedAt,
		ReceivedAt: p.now().UTC(),
		Payload:    payloadJSON,
		Attributes: boundedAttributes(attributes),
	}, nil
}

func webhookRepositoryPayload(repository notificationRepository) map[string]any {
	owner, _, _ := strings.Cut(repository.FullName, "/")
	if strings.TrimSpace(repository.Owner.Login) != "" {
		owner = repository.Owner.Login
	}
	return map[string]any{
		"id":             repository.ID,
		"node_id":        repository.NodeID,
		"name":           repository.Name,
		"full_name":      repository.FullName,
		"html_url":       repository.HTMLURL,
		"default_branch": repository.DefaultBranch,
		"private":        repository.Private,
		"owner": map[string]any{
			"login": owner,
		},
	}
}

func webhookPullRequestPayload(
	pr pullRequest,
	repository string,
) map[string]any {
	headRepository := repository
	if pr.Head.Repo != nil && pr.Head.Repo.FullName != "" {
		headRepository = pr.Head.Repo.FullName
	}
	return map[string]any{
		"number":   pr.Number,
		"html_url": pr.HTMLURL,
		"title":    pr.Title,
		"body":     pr.Body,
		"draft":    pr.Draft,
		"user": map[string]any{
			"login": pr.User.Login,
		},
		"head": map[string]any{
			"ref": pr.Head.Ref,
			"sha": pr.Head.SHA,
			"repo": map[string]any{
				"full_name": headRepository,
				"clone_url": "https://github.com/" + headRepository + ".git",
			},
		},
		"base": map[string]any{
			"ref": pr.Base.Ref,
			"sha": pr.Base.SHA,
			"repo": map[string]any{
				"full_name": pr.Base.Repo.FullName,
				"clone_url": "https://github.com/" + pr.Base.Repo.FullName + ".git",
			},
		},
	}
}

func repositorySubject(repository notificationRepository) *eventing.Subject {
	id := strings.Trim(string(repository.ID), `"`)
	return &eventing.Subject{
		ID:   boundedString(id, maxEntityFieldBytes),
		Type: "repository",
		Name: boundedString(repository.FullName, maxEntityFieldBytes),
		URL:  boundedString(repository.HTMLURL, maxEntityFieldBytes),
	}
}

func notificationEventType(subjectType, reason string) string {
	kind := strings.ToLower(strings.TrimSpace(subjectType))
	reason = normalizedTypePart(reason)
	switch kind {
	case "pullrequest":
		switch reason {
		case "review_requested":
			return "pull_request.review_requested"
		case "mention":
			return "pull_request.mention"
		case "assign":
			return "pull_request.assign"
		}
	case "issue":
		switch reason {
		case "mention":
			return "issues.mention"
		case "assign":
			return "issues.assign"
		}
	}
	return "notification." + reason
}

func notificationTargetReason(reason string) string {
	switch strings.ToLower(strings.TrimSpace(reason)) {
	case "review_requested":
		return "requested_reviewer"
	case "mention":
		return "mention"
	case "assign":
		return "assignee"
	default:
		return ""
	}
}

func normalizedTypePart(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	var out strings.Builder
	for _, char := range value {
		if unicode.IsLetter(char) || unicode.IsDigit(char) || char == '_' || char == '-' {
			out.WriteRune(char)
		} else if out.Len() > 0 {
			out.WriteByte('_')
		}
		if out.Len() >= maxNotificationReason {
			break
		}
	}
	normalized := strings.Trim(out.String(), "_-")
	if normalized == "" {
		return "other"
	}
	return normalized
}

func validRepository(value string) bool {
	return value != "" &&
		value == strings.TrimSpace(value) &&
		utf8.ValidString(value) &&
		len(value) <= maxRepositoryBytes &&
		repositoryPattern.MatchString(value)
}

func validHTTPSURL(value string) bool {
	if value == "" || len(value) > maxEntityFieldBytes || value != strings.TrimSpace(value) {
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

func htmlResourceURL(repositoryURL, collection string, number int) string {
	repositoryURL = strings.TrimSuffix(repositoryURL, "/")
	if !validHTTPSURL(repositoryURL) {
		return ""
	}
	return repositoryURL + "/" + collection + "/" + strconv.Itoa(number)
}

func boundedAttributes(values map[string]string) map[string]string {
	out := make(map[string]string, len(values))
	for key, value := range values {
		value = boundedString(value, maxEntityFieldBytes)
		if value != "" {
			out[key] = value
		}
	}
	return out
}

func boundedString(value string, limit int) string {
	value = strings.TrimSpace(value)
	if !utf8.ValidString(value) {
		return ""
	}
	if len(value) > limit {
		cut := limit
		for cut > 0 && !utf8.ValidString(value[:cut]) {
			cut--
		}
		return value[:cut]
	}
	return value
}
