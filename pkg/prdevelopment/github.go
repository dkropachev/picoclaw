package prdevelopment

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	picomcp "github.com/sipeed/picoclaw/pkg/mcp"
	"github.com/sipeed/picoclaw/pkg/workflows"
)

const (
	defaultGitHubMCPServer = "github"
	pullRequestReadTool    = "pull_request_read"

	providerReviewsPerPage = 100
	maxProviderReviewPages = 5
	maxProviderReviewItems = providerReviewsPerPage * maxProviderReviewPages
	// The probe uses perPage=1, so page 501 starts at the first item after the
	// five accepted 100-item pages.
	providerOverflowProbePage = maxProviderReviewItems + 1
	maxProviderJSONBytes      = 16 << 20
	maxProviderTotalBytes     = 32 << 20
	maxProviderTextBytes      = 1 << 20
	maxProviderJSONDepth      = 64
	maxProviderJSONTokens     = 1 << 20
	maxProviderReviewBody     = 64 << 10
	maxProviderRefBytes       = 1024
)

// GitHubVerifier independently re-reads the current PR and exact review via
// the configured, generation-fenced workflow MCP runner.
type GitHubVerifier struct {
	Runner       workflows.ToolRunner
	Server       string
	ArtifactRoot string
}

// VerifiedFeedback is the bounded provider snapshot admitted to durable
// capture. It contains no checkout, credential, or provider-write capability.
type VerifiedFeedback struct {
	Repository         string
	PullNumber         int
	PullURL            string
	PullAuthor         string
	PullState          string
	PullDraft          bool
	PullMerged         bool
	BaseRepository     string
	BaseRef            string
	BaseSHA            string
	HeadRepository     string
	HeadRef            string
	HeadSHA            string
	ReviewID           string
	ReviewAuthor       string
	CurrentReviewState string
	ReviewCommitSHA    string
	ReviewSubmittedAt  time.Time
	ReviewURL          string
	Feedback           string
}

type providerPullRequest struct {
	Number  int                     `json:"number"`
	State   string                  `json:"state"`
	Draft   *bool                   `json:"draft"`
	Merged  *bool                   `json:"merged"`
	HTMLURL string                  `json:"html_url"`
	User    *providerUser           `json:"user"`
	Head    *providerPullRequestRef `json:"head"`
	Base    *providerPullRequestRef `json:"base"`
}

type providerPullRequestRef struct {
	Ref  string                   `json:"ref"`
	SHA  string                   `json:"sha"`
	Repo *providerPullRequestRepo `json:"repo"`
}

type providerPullRequestRepo struct {
	FullName string `json:"full_name"`
}

type providerUser struct {
	Login string `json:"login"`
}

type providerReview struct {
	ID          json.Number   `json:"id"`
	State       string        `json:"state"`
	Body        string        `json:"body"`
	HTMLURL     string        `json:"html_url"`
	User        *providerUser `json:"user"`
	CommitID    string        `json:"commit_id"`
	SubmittedAt string        `json:"submitted_at"`
}

// Verify binds the signed routing identity to current provider facts. The
// review list is paginated under fixed limits and selected by its canonical
// database ID before any body is admitted.
func (v *GitHubVerifier) Verify(
	ctx context.Context,
	evidence RoutingEvidence,
) (VerifiedFeedback, error) {
	if v == nil || v.Runner == nil {
		return VerifiedFeedback{}, errors.New("GitHub verifier runner is required")
	}
	if err := ctx.Err(); err != nil {
		return VerifiedFeedback{}, err
	}
	owner, repo, ok := strings.Cut(evidence.Repository, "/")
	if !ok || owner == "" || repo == "" {
		return VerifiedFeedback{}, errors.New("GitHub verifier repository is invalid")
	}
	server := strings.TrimSpace(v.Server)
	if server == "" {
		server = defaultGitHubMCPServer
	}

	pullRaw, err := v.read(
		ctx,
		server,
		map[string]any{
			"method":     "get",
			"owner":      owner,
			"repo":       repo,
			"pullNumber": evidence.PullNumber,
		},
		maxProviderTextBytes,
	)
	if err != nil {
		return VerifiedFeedback{}, fmt.Errorf("read pull request: %w", err)
	}
	var pull providerPullRequest
	if decodeErr := decodeProviderJSON(pullRaw, &pull); decodeErr != nil {
		return VerifiedFeedback{}, fmt.Errorf("decode pull request: %w", decodeErr)
	}
	verified, err := verifyProviderPullRequest(evidence, pull)
	if err != nil {
		return VerifiedFeedback{}, err
	}

	var (
		matched       *providerReview
		totalItems    int
		totalBytes    = len(pullRaw)
		scanCompleted bool
	)
	for page := 1; page <= maxProviderReviewPages; page++ {
		if err := ctx.Err(); err != nil {
			return VerifiedFeedback{}, err
		}
		pageLimit, limitErr := remainingProviderReadLimit(totalBytes)
		if limitErr != nil {
			return VerifiedFeedback{}, limitErr
		}
		raw, readErr := v.read(
			ctx,
			server,
			map[string]any{
				"method":     "get_reviews",
				"owner":      owner,
				"repo":       repo,
				"pullNumber": evidence.PullNumber,
				"page":       page,
				"perPage":    providerReviewsPerPage,
			},
			pageLimit,
		)
		if readErr != nil {
			return VerifiedFeedback{}, fmt.Errorf("read reviews page %d: %w", page, readErr)
		}
		totalBytes += len(raw)
		if totalBytes > maxProviderTotalBytes {
			return VerifiedFeedback{}, errors.New("GitHub provider snapshot exceeds the total limit")
		}
		var reviews []providerReview
		if decodeErr := decodeProviderJSON(raw, &reviews); decodeErr != nil {
			return VerifiedFeedback{}, fmt.Errorf("decode reviews page %d: %w", page, decodeErr)
		}
		if reviews == nil || len(reviews) > providerReviewsPerPage {
			return VerifiedFeedback{}, fmt.Errorf("reviews page %d has an invalid item count", page)
		}
		totalItems += len(reviews)
		if totalItems > maxProviderReviewItems {
			return VerifiedFeedback{}, errors.New("GitHub provider review count exceeds the limit")
		}
		for index := range reviews {
			id, idErr := canonicalProviderDatabaseID(reviews[index].ID)
			if idErr != nil {
				return VerifiedFeedback{}, fmt.Errorf(
					"reviews page %d item %d has an invalid ID",
					page,
					index,
				)
			}
			if id != evidence.ReviewID {
				continue
			}
			if matched != nil {
				return VerifiedFeedback{}, errors.New("GitHub provider returned the review more than once")
			}
			copyReview := reviews[index]
			matched = &copyReview
		}
		if len(reviews) < providerReviewsPerPage {
			scanCompleted = true
			break
		}
	}
	if !scanCompleted {
		probeLimit, limitErr := remainingProviderReadLimit(totalBytes)
		if limitErr != nil {
			return VerifiedFeedback{}, limitErr
		}
		raw, readErr := v.read(
			ctx,
			server,
			map[string]any{
				"method":     "get_reviews",
				"owner":      owner,
				"repo":       repo,
				"pullNumber": evidence.PullNumber,
				"page":       providerOverflowProbePage,
				"perPage":    1,
			},
			probeLimit,
		)
		if readErr != nil {
			return VerifiedFeedback{}, fmt.Errorf("probe review scan overflow: %w", readErr)
		}
		totalBytes += len(raw)
		if totalBytes > maxProviderTotalBytes {
			return VerifiedFeedback{}, errors.New("GitHub provider snapshot exceeds the total limit")
		}
		var overflow []providerReview
		if decodeErr := decodeProviderJSON(raw, &overflow); decodeErr != nil {
			return VerifiedFeedback{}, fmt.Errorf("decode review scan overflow probe: %w", decodeErr)
		}
		if overflow == nil || len(overflow) > 1 {
			return VerifiedFeedback{}, errors.New("review scan overflow probe has an invalid item count")
		}
		if len(overflow) != 0 {
			return VerifiedFeedback{}, errors.New("GitHub provider review scan exceeds five complete pages")
		}
	}
	if matched == nil {
		return VerifiedFeedback{}, errors.New("exact GitHub review was not found within the bounded scan")
	}
	if err := verifyProviderReview(evidence, *matched); err != nil {
		return VerifiedFeedback{}, err
	}
	verified.ReviewID = evidence.ReviewID
	verified.ReviewAuthor = matched.User.Login
	verified.CurrentReviewState = strings.ToLower(matched.State)
	verified.ReviewCommitSHA = matched.CommitID
	verified.ReviewSubmittedAt = evidence.ReviewSubmittedAt
	verified.ReviewURL = matched.HTMLURL
	verified.Feedback = matched.Body
	return verified, nil
}

func verifyProviderPullRequest(
	evidence RoutingEvidence,
	pull providerPullRequest,
) (VerifiedFeedback, error) {
	if pull.Number != evidence.PullNumber || pull.HTMLURL != evidence.PullURL ||
		!validHTTPSURL(pull.HTMLURL) {
		return VerifiedFeedback{}, errors.New("GitHub pull request identity does not match the event")
	}
	if pull.Draft == nil || pull.Merged == nil {
		return VerifiedFeedback{}, errors.New("GitHub pull request draft or merged state is incomplete")
	}
	if pull.User == nil ||
		!validGitHubUser(pull.User.Login, false) ||
		!strings.EqualFold(pull.User.Login, evidence.PullAuthor) ||
		!strings.EqualFold(pull.User.Login, evidence.TargetUser) {
		return VerifiedFeedback{}, errors.New("GitHub pull request author does not match the target")
	}
	state := strings.ToLower(pull.State)
	if state != "open" && state != "closed" {
		return VerifiedFeedback{}, errors.New("GitHub pull request state is invalid")
	}
	if pull.Head == nil || pull.Base == nil || pull.Head.Repo == nil || pull.Base.Repo == nil ||
		!validProviderRef(*pull.Head) || !validProviderRef(*pull.Base) {
		return VerifiedFeedback{}, errors.New("GitHub pull request branch identity is incomplete")
	}
	if !repositoryPattern.MatchString(pull.Base.Repo.FullName) ||
		!strings.EqualFold(pull.Base.Repo.FullName, evidence.Repository) ||
		!repositoryPattern.MatchString(pull.Head.Repo.FullName) {
		return VerifiedFeedback{}, errors.New("GitHub pull request repository identity is invalid")
	}
	return VerifiedFeedback{
		Repository:     pull.Base.Repo.FullName,
		PullNumber:     pull.Number,
		PullURL:        pull.HTMLURL,
		PullAuthor:     pull.User.Login,
		PullState:      state,
		PullDraft:      *pull.Draft,
		PullMerged:     *pull.Merged,
		BaseRepository: pull.Base.Repo.FullName,
		BaseRef:        pull.Base.Ref,
		BaseSHA:        pull.Base.SHA,
		HeadRepository: pull.Head.Repo.FullName,
		HeadRef:        pull.Head.Ref,
		HeadSHA:        pull.Head.SHA,
	}, nil
}

func verifyProviderReview(evidence RoutingEvidence, review providerReview) error {
	id, err := canonicalProviderDatabaseID(review.ID)
	if err != nil || id != evidence.ReviewID {
		return errors.New("GitHub review ID does not match the event")
	}
	if review.User == nil ||
		!validGitHubUser(review.User.Login, true) ||
		!strings.EqualFold(review.User.Login, evidence.ReviewAuthor) ||
		strings.EqualFold(review.User.Login, evidence.TargetUser) {
		return errors.New("GitHub review author does not match the event")
	}
	state := strings.ToLower(review.State)
	if state != evidence.ReviewState && state != "dismissed" {
		return errors.New("GitHub review state does not match the event")
	}
	if !validObjectID(review.CommitID) || review.CommitID != evidence.ReviewCommitSHA {
		return errors.New("GitHub review commit does not match the event")
	}
	submittedAt, err := time.Parse(time.RFC3339Nano, review.SubmittedAt)
	if err != nil || !submittedAt.Equal(evidence.ReviewSubmittedAt) {
		return errors.New("GitHub review submission time does not match the event")
	}
	if !validHTTPSURLWithFragment(review.HTMLURL) || review.HTMLURL != evidence.ReviewURL {
		return errors.New("GitHub review URL does not match the event")
	}
	if len(review.Body) > maxProviderReviewBody || !utf8.ValidString(review.Body) {
		return errors.New("GitHub review body is invalid or too large")
	}
	return nil
}

func validProviderRef(ref providerPullRequestRef) bool {
	if ref.Ref == "" || len(ref.Ref) > maxProviderRefBytes ||
		ref.Ref != strings.TrimSpace(ref.Ref) || !utf8.ValidString(ref.Ref) ||
		!validObjectID(ref.SHA) {
		return false
	}
	for _, char := range ref.Ref {
		if unicode.IsControl(char) || unicode.Is(unicode.Cf, char) {
			return false
		}
	}
	return true
}

func canonicalProviderDatabaseID(value json.Number) (string, error) {
	raw := string(value)
	if !databaseIDPattern.MatchString(raw) {
		return "", errors.New("database ID is not canonical")
	}
	parsed, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || parsed <= 0 || strconv.FormatInt(parsed, 10) != raw {
		return "", errors.New("database ID is out of range")
	}
	return raw, nil
}

func remainingProviderReadLimit(totalBytes int) (int, error) {
	if totalBytes < 0 || totalBytes >= maxProviderTotalBytes {
		return 0, errors.New("GitHub provider snapshot exceeds the total limit")
	}
	remaining := maxProviderTotalBytes - totalBytes
	if remaining > maxProviderJSONBytes {
		remaining = maxProviderJSONBytes
	}
	return remaining, nil
}

func (v *GitHubVerifier) read(
	ctx context.Context,
	server string,
	args map[string]any,
	limit int,
) ([]byte, error) {
	outputs, err := v.Runner.RunTool(ctx, workflows.ToolRequest{
		Name:      picomcp.CanonicalToolName(server, pullRequestReadTool),
		Args:      args,
		MCP:       true,
		MCPServer: server,
		MCPTool:   pullRequestReadTool,
	})
	if err != nil {
		return nil, err
	}
	return v.exactJSON(outputs, limit)
}

func (v *GitHubVerifier) exactJSON(outputs map[string]any, limit int) ([]byte, error) {
	if outputs == nil {
		return nil, errors.New("MCP result is missing")
	}
	if limit <= 0 || limit > maxProviderJSONBytes {
		limit = maxProviderJSONBytes
	}
	if rawTags, present := outputs["artifact_tags"]; present {
		tags, ok := rawTags.([]string)
		if !ok {
			return nil, errors.New("MCP artifact tags are invalid")
		}
		if len(tags) > 0 {
			return v.exactArtifactJSON(tags, limit)
		}
	}
	value, ok := outputs["text"].(string)
	if !ok {
		return nil, errors.New("MCP result text is missing")
	}
	value = strings.TrimSpace(value)
	if value == "" || len(value) > limit || !utf8.ValidString(value) {
		return nil, errors.New("MCP result text is invalid or too large")
	}
	raw := []byte(value)
	if !json.Valid(raw) {
		return nil, errors.New("MCP result is not exact JSON")
	}
	return raw, nil
}

func (v *GitHubVerifier) exactArtifactJSON(tags []string, limit int) ([]byte, error) {
	if len(tags) != 1 || strings.TrimSpace(v.ArtifactRoot) == "" {
		return nil, errors.New("MCP exact JSON artifact is unavailable")
	}
	artifactPath, ok := strings.CutPrefix(tags[0], "[file:")
	if !ok || !strings.HasSuffix(artifactPath, "]") {
		return nil, errors.New("MCP exact JSON artifact tag is invalid")
	}
	artifactPath = strings.TrimSuffix(artifactPath, "]")
	root, err := filepath.Abs(strings.TrimSpace(v.ArtifactRoot))
	if err != nil {
		return nil, err
	}
	artifactPath, err = filepath.Abs(artifactPath)
	if err != nil {
		return nil, err
	}
	before, err := os.Lstat(artifactPath)
	if err != nil || !before.Mode().IsRegular() {
		return nil, errors.New("MCP exact JSON artifact is not a regular file")
	}
	resolvedRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return nil, err
	}
	resolvedPath, err := filepath.EvalSymlinks(artifactPath)
	if err != nil {
		return nil, err
	}
	relative, err := filepath.Rel(resolvedRoot, resolvedPath)
	if err != nil || relative == "." || relative == ".." ||
		strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return nil, errors.New("MCP exact JSON artifact is outside the configured root")
	}
	file, err := os.Open(resolvedPath)
	if err != nil {
		return nil, err
	}
	after, statErr := file.Stat()
	if statErr != nil || !after.Mode().IsRegular() || !os.SameFile(before, after) ||
		after.Size() > int64(limit) {
		_ = file.Close()
		return nil, errors.New("MCP exact JSON artifact is invalid or too large")
	}
	raw, readErr := io.ReadAll(io.LimitReader(file, int64(limit)+1))
	closeErr := file.Close()
	if readErr != nil || closeErr != nil || len(raw) > limit || !utf8.Valid(raw) {
		return nil, errors.New("MCP exact JSON artifact cannot be read safely")
	}
	current, currentErr := os.Lstat(resolvedPath)
	if currentErr != nil || !current.Mode().IsRegular() || !os.SameFile(after, current) {
		return nil, errors.New("MCP exact JSON artifact changed while it was read")
	}
	if err := os.Remove(resolvedPath); err != nil {
		return nil, fmt.Errorf("remove consumed MCP exact JSON artifact: %w", err)
	}
	raw = bytes.TrimSpace(raw)
	if !json.Valid(raw) {
		return nil, errors.New("MCP exact JSON artifact is not JSON")
	}
	return raw, nil
}

func decodeProviderJSON(raw []byte, destination any) error {
	if len(raw) == 0 || len(raw) > maxProviderJSONBytes || !utf8.Valid(raw) {
		return errors.New("provider JSON is invalid or too large")
	}
	if err := validateProviderJSONStringEncoding(raw); err != nil {
		return err
	}
	if err := rejectDuplicateProviderJSONKeys(raw); err != nil {
		return err
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(destination); err != nil {
		return err
	}
	var trailing json.RawMessage
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("provider JSON contains trailing data")
		}
		return err
	}
	return nil
}

func rejectDuplicateProviderJSONKeys(raw []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	tokens := 0
	var decodeValue func(int) error
	decodeValue = func(depth int) error {
		if depth > maxProviderJSONDepth {
			return errors.New("provider JSON exceeds the depth limit")
		}
		tokens++
		if tokens > maxProviderJSONTokens {
			return errors.New("provider JSON exceeds the token limit")
		}
		token, err := decoder.Token()
		if err != nil {
			return err
		}
		delim, compound := token.(json.Delim)
		if !compound {
			return nil
		}
		switch delim {
		case '{':
			seen := make(map[string]struct{})
			for decoder.More() {
				tokens++
				if tokens > maxProviderJSONTokens {
					return errors.New("provider JSON exceeds the token limit")
				}
				nameToken, tokenErr := decoder.Token()
				name, ok := nameToken.(string)
				if tokenErr != nil || !ok {
					return errors.New("provider JSON object name is invalid")
				}
				if _, duplicate := seen[name]; duplicate {
					return errors.New("provider JSON contains a duplicate object name")
				}
				seen[name] = struct{}{}
				if err := decodeValue(depth + 1); err != nil {
					return err
				}
			}
			closing, closeErr := decoder.Token()
			if closeErr != nil || closing != json.Delim('}') {
				return errors.New("provider JSON object is incomplete")
			}
		case '[':
			for decoder.More() {
				if err := decodeValue(depth + 1); err != nil {
					return err
				}
			}
			closing, closeErr := decoder.Token()
			if closeErr != nil || closing != json.Delim(']') {
				return errors.New("provider JSON array is incomplete")
			}
		default:
			return errors.New("provider JSON delimiter is invalid")
		}
		return nil
	}
	if err := decodeValue(0); err != nil {
		return err
	}
	var trailing json.RawMessage
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("provider JSON contains trailing data")
		}
		return err
	}
	return nil
}

func validateProviderJSONStringEncoding(raw []byte) error {
	for index := 0; index < len(raw); index++ {
		if raw[index] != '"' {
			continue
		}
		index++
		for {
			if index >= len(raw) {
				return errors.New("provider JSON string is incomplete")
			}
			switch raw[index] {
			case '"':
				goto stringClosed
			case '\\':
				index++
				if index >= len(raw) {
					return errors.New("provider JSON escape is incomplete")
				}
				if raw[index] != 'u' {
					if !strings.ContainsRune(`"\\/bfnrt`, rune(raw[index])) {
						return errors.New("provider JSON escape is invalid")
					}
					index++
					continue
				}
				code, ok := providerJSONHexQuad(raw, index+1)
				if !ok {
					return errors.New("provider JSON Unicode escape is invalid")
				}
				index += 5
				switch {
				case code >= 0xd800 && code <= 0xdbff:
					if index+6 > len(raw) || raw[index] != '\\' || raw[index+1] != 'u' {
						return errors.New("provider JSON contains an unpaired surrogate")
					}
					low, lowOK := providerJSONHexQuad(raw, index+2)
					if !lowOK || low < 0xdc00 || low > 0xdfff {
						return errors.New("provider JSON contains an unpaired surrogate")
					}
					index += 6
				case code >= 0xdc00 && code <= 0xdfff:
					return errors.New("provider JSON contains an unpaired surrogate")
				}
				continue
			default:
				if raw[index] < 0x20 {
					return errors.New("provider JSON string contains a control byte")
				}
				_, size := utf8.DecodeRune(raw[index:])
				index += size
			}
		}
	stringClosed:
	}
	return nil
}

func providerJSONHexQuad(raw []byte, offset int) (uint16, bool) {
	if offset < 0 || offset+4 > len(raw) {
		return 0, false
	}
	var value uint16
	for _, character := range raw[offset : offset+4] {
		value <<= 4
		switch {
		case character >= '0' && character <= '9':
			value |= uint16(character - '0')
		case character >= 'a' && character <= 'f':
			value |= uint16(character-'a') + 10
		case character >= 'A' && character <= 'F':
			value |= uint16(character-'A') + 10
		default:
			return 0, false
		}
	}
	return value, true
}
