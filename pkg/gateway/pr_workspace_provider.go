package gateway

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/prworkspace"
	"github.com/sipeed/picoclaw/pkg/reviews"
)

type prWorkspaceGitHubResolver struct {
	provider             *reviews.GitHubProvider
	canReview            bool
	canCreateIssue       bool
	canCreatePullRequest bool
	repositories         map[string]config.PRLifecycleRepositoryDescriptor
}

type prWorkspaceGitHubUser struct {
	ID    json.RawMessage `json:"id"`
	Login string          `json:"login"`
}

type prWorkspaceGitHubPermissions struct {
	Admin    bool `json:"admin"`
	Maintain bool `json:"maintain"`
	Push     bool `json:"push"`
	Triage   bool `json:"triage"`
	Pull     bool `json:"pull"`
}

type prWorkspaceGitHubRepo struct {
	ID            json.RawMessage               `json:"id"`
	Name          string                        `json:"name"`
	FullName      string                        `json:"full_name"`
	HTMLURL       string                        `json:"html_url"`
	HasIssues     *bool                         `json:"has_issues"`
	Owner         *prWorkspaceGitHubUser        `json:"owner"`
	Permissions   *prWorkspaceGitHubPermissions `json:"permissions"`
	DefaultBranch string                        `json:"default_branch"`
}

type prWorkspaceGitHubIssue struct {
	ID        json.RawMessage        `json:"id"`
	Number    int64                  `json:"number"`
	Title     string                 `json:"title"`
	Body      string                 `json:"body"`
	State     string                 `json:"state"`
	HTMLURL   string                 `json:"html_url"`
	UpdatedAt string                 `json:"updated_at"`
	User      *prWorkspaceGitHubUser `json:"user"`
}

type prWorkspaceGitHubCommit struct {
	SHA string `json:"sha"`
}

type prWorkspaceGitHubBranch struct {
	Ref  string                 `json:"ref"`
	SHA  string                 `json:"sha"`
	Repo *prWorkspaceGitHubRepo `json:"repo"`
	User *prWorkspaceGitHubUser `json:"user"`
}

func (resolver *prWorkspaceGitHubResolver) ResolveIssue(
	ctx context.Context,
	request prworkspace.IssueResolveRequest,
) (prworkspace.ProviderSnapshot, error) {
	if resolver == nil || resolver.provider == nil {
		return prworkspace.ProviderSnapshot{}, errors.New("GitHub issue provider is unavailable")
	}
	origin, repository, issueNumber, err := normalizeGitHubIssueURL(request.IssueURL)
	if err != nil {
		return prworkspace.ProviderSnapshot{}, err
	}
	raw, err := resolver.provider.ReadWorkspaceIssueJSON(ctx, repository, issueNumber)
	if err != nil {
		return prworkspace.ProviderSnapshot{}, err
	}
	var issue prWorkspaceGitHubIssue
	if err = json.Unmarshal(raw, &issue); err != nil || issue.User == nil ||
		issue.Number != issueNumber || !sameGitHubIssueURL(issue.HTMLURL, origin, repository, issueNumber) ||
		!strings.EqualFold(strings.TrimSpace(issue.State), "open") {
		return prworkspace.ProviderSnapshot{}, errors.New("GitHub issue identity is invalid")
	}
	issueID := githubPositiveNumericID(issue.ID)
	authorID := githubPositiveNumericID(issue.User.ID)
	if issueID == "" || authorID == "" || strings.TrimSpace(issue.User.Login) == "" {
		return prworkspace.ProviderSnapshot{}, errors.New("GitHub issue authority is incomplete")
	}
	comments, err := resolver.provider.ReadWorkspaceIssueCommentsJSON(ctx, repository, issueNumber)
	if err != nil {
		return prworkspace.ProviderSnapshot{}, err
	}
	body := issue.Body
	if len(comments) > 0 && string(comments) != "[]" {
		body = strings.TrimSpace(body) + "\n\nProvider issue comments (untrusted evidence):\n" + string(comments)
	}
	body = boundedGitHubIssueEvidence(body)
	return resolver.resolveFeatureRepository(ctx, origin, repository, issueID, issueNumber,
		request.IssueURL, issue.Title, body, authorID, issue.User.Login, issue.UpdatedAt)
}

func boundedGitHubIssueEvidence(value string) string {
	const limit = 480 << 10
	if len(value) <= limit {
		return value
	}
	value = value[:limit]
	for !utf8.ValidString(value) {
		value = value[:len(value)-1]
	}
	return strings.TrimSpace(value) + "\n[truncated]"
}

func (resolver *prWorkspaceGitHubResolver) ResolveRepository(
	ctx context.Context,
	request prworkspace.RepositoryResolveRequest,
) (prworkspace.ProviderSnapshot, error) {
	if resolver == nil || resolver.provider == nil || resolver.repositories == nil {
		return prworkspace.ProviderSnapshot{}, errors.New("configured repository provider is unavailable")
	}
	identity := strings.TrimSpace(request.RepositoryIdentity)
	descriptor, ok := resolver.repositories[identity]
	repository := descriptor.Name
	if !ok || repository == "" {
		return prworkspace.ProviderSnapshot{}, prworkspace.ErrInvalid
	}
	origin, repositoryID, ok := strings.Cut(identity, "|")
	if !ok || origin == "" || repositoryID == "" {
		return prworkspace.ProviderSnapshot{}, prworkspace.ErrInvalid
	}
	digest := sha256.Sum256([]byte(strings.Join([]string{
		"development-brief", origin, repositoryID, request.Brief,
	}, "\x00")))
	sourceID := "sha256:" + hex.EncodeToString(digest[:])
	snapshot, err := resolver.resolveFeatureRepository(ctx, origin, repository, sourceID, 0, "",
		"Feature brief", request.Brief, "", "", sourceID)
	if err != nil {
		return prworkspace.ProviderSnapshot{}, err
	}
	if snapshot.ProviderOrigin != origin || snapshot.RepositoryID != repositoryID {
		return prworkspace.ProviderSnapshot{}, errors.New("configured repository identity changed")
	}
	return snapshot, nil
}

func (resolver *prWorkspaceGitHubResolver) ListConfiguredRepositories(
	_ context.Context,
) ([]prworkspace.ConfiguredRepository, error) {
	if resolver == nil || resolver.repositories == nil {
		return nil, errors.New("configured repositories are unavailable")
	}
	identities := make([]string, 0, len(resolver.repositories))
	for identity := range resolver.repositories {
		identities = append(identities, identity)
	}
	sort.Strings(identities)
	result := make([]prworkspace.ConfiguredRepository, 0, len(identities))
	for _, identity := range identities {
		descriptor := resolver.repositories[identity]
		result = append(result, prworkspace.ConfiguredRepository{
			Identity: identity, Name: descriptor.Name, DefaultBranch: descriptor.DefaultBranch,
			CanImplement: resolver.canCreatePullRequest,
		})
	}
	return result, nil
}

func (resolver *prWorkspaceGitHubResolver) VerifyRepository(
	ctx context.Context, raw string,
) (prworkspace.ConfiguredRepository, error) {
	parsed, err := url.ParseRequestURI(raw)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil ||
		parsed.RawQuery != "" || parsed.Fragment != "" {
		return prworkspace.ConfiguredRepository{}, prworkspace.ErrInvalid
	}
	parts := strings.Split(strings.Trim(parsed.Path, "/"), "/")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return prworkspace.ConfiguredRepository{}, prworkspace.ErrInvalid
	}
	repository := parts[0] + "/" + strings.TrimSuffix(parts[1], ".git")
	origin := parsed.Scheme + "://" + strings.ToLower(parsed.Host)
	resolved, err := resolver.resolveRepository(ctx, origin, repository)
	if err != nil || !githubRepositoryCanPush(resolved) || strings.TrimSpace(resolved.DefaultBranch) == "" {
		return prworkspace.ConfiguredRepository{}, errors.New("GitHub repository is not writable")
	}
	id := githubPositiveNumericID(resolved.ID)
	if id == "" {
		return prworkspace.ConfiguredRepository{}, errors.New("GitHub repository identity is invalid")
	}
	return prworkspace.ConfiguredRepository{
		Identity: origin + "|" + id, Name: resolved.FullName,
		DefaultBranch: resolved.DefaultBranch, CanImplement: resolver.canCreatePullRequest,
	}, nil
}

func (resolver *prWorkspaceGitHubResolver) resolveFeatureRepository(
	ctx context.Context,
	origin, repository, sourceID string,
	sourceNumber int64,
	sourceURL, title, body, authorID, authorLogin, sourceRevision string,
) (prworkspace.ProviderSnapshot, error) {
	repo, err := resolver.resolveRepository(ctx, origin, repository)
	if err != nil {
		return prworkspace.ProviderSnapshot{}, err
	}
	repositoryID := githubPositiveNumericID(repo.ID)
	defaultBranch := strings.TrimSpace(repo.DefaultBranch)
	if repositoryID == "" || defaultBranch == "" || !githubRepositoryCanPush(repo) {
		return prworkspace.ProviderSnapshot{}, errors.New("GitHub repository is not writable")
	}
	commitRaw, err := resolver.provider.ListWorkspaceCommitsJSON(ctx, repository, defaultBranch)
	if err != nil {
		return prworkspace.ProviderSnapshot{}, err
	}
	commitSHA, err := decodeSingleGitHubCommitSHA(commitRaw)
	if err != nil {
		return prworkspace.ProviderSnapshot{}, err
	}
	viewerRaw, err := resolver.provider.ReadWorkspaceViewerJSON(ctx)
	if err != nil {
		return prworkspace.ProviderSnapshot{}, err
	}
	var viewer prWorkspaceGitHubUser
	if err = json.Unmarshal(viewerRaw, &viewer); err != nil {
		return prworkspace.ProviderSnapshot{}, errors.New("GitHub viewer response is invalid")
	}
	viewerID := githubPositiveNumericID(viewer.ID)
	if viewerID == "" || strings.TrimSpace(viewer.Login) == "" {
		return prworkspace.ProviderSnapshot{}, errors.New("GitHub viewer authority is incomplete")
	}
	if authorID == "" {
		authorID, authorLogin = viewerID, viewer.Login
	}
	revision := sha256.Sum256([]byte(strings.Join([]string{
		origin, repositoryID, sourceID, sourceRevision, defaultBranch, commitSHA,
	}, "\x00")))
	return prworkspace.ProviderSnapshot{
		SourceID: sourceID, SourceNumber: sourceNumber, SourceURL: sourceURL,
		Provider: "github", ProviderOrigin: origin, RepositoryID: repositoryID,
		Repository: repository, Title: strings.TrimSpace(title), Body: body,
		AuthorID: authorID, AuthorLogin: authorLogin, AuthenticatedUserID: viewerID,
		BaseRef: defaultBranch, BaseSHA: commitSHA,
		HeadRepositoryID: repositoryID, HeadRepository: repository,
		HeadRef: defaultBranch, HeadSHA: commitSHA, State: "open", Owned: true,
		HeadWritable: true, CanCreateIssue: resolver.canCreateIssue,
		CanCreatePullRequest: resolver.canCreatePullRequest,
		ProviderRevision:     "sha256:" + hex.EncodeToString(revision[:]), ObservedAt: time.Now().UTC(),
	}, nil
}

func decodeSingleGitHubCommitSHA(raw []byte) (string, error) {
	var commits []prWorkspaceGitHubCommit
	if err := json.Unmarshal(raw, &commits); err != nil || len(commits) != 1 {
		return "", errors.New("GitHub commit response is invalid")
	}
	sha := strings.ToLower(strings.TrimSpace(commits[0].SHA))
	if len(sha) != 40 && len(sha) != 64 {
		return "", errors.New("GitHub commit identity is invalid")
	}
	if _, err := hex.DecodeString(sha); err != nil {
		return "", errors.New("GitHub commit identity is invalid")
	}
	return sha, nil
}

type prWorkspaceGitHubPull struct {
	ID                  json.RawMessage          `json:"id"`
	Number              int64                    `json:"number"`
	Title               string                   `json:"title"`
	Body                string                   `json:"body"`
	State               string                   `json:"state"`
	Merged              bool                     `json:"merged"`
	Draft               bool                     `json:"draft"`
	HTMLURL             string                   `json:"html_url"`
	User                *prWorkspaceGitHubUser   `json:"user"`
	Base                *prWorkspaceGitHubBranch `json:"base"`
	Head                *prWorkspaceGitHubBranch `json:"head"`
	MaintainerCanModify *bool                    `json:"maintainer_can_modify"`
	UpdatedAt           string                   `json:"updated_at"`
}

type prWorkspaceGitHubRepositorySearch struct {
	TotalCount        int64                   `json:"total_count"`
	IncompleteResults bool                    `json:"incomplete_results"`
	Items             []prWorkspaceGitHubRepo `json:"items"`
}

func (resolver *prWorkspaceGitHubResolver) ResolvePullRequest(
	ctx context.Context,
	request prworkspace.ResolveRequest,
) (prworkspace.ProviderSnapshot, error) {
	if resolver == nil || resolver.provider == nil {
		return prworkspace.ProviderSnapshot{}, errors.New("GitHub PR provider is unavailable")
	}
	origin, repository, pullNumber, err := normalizePRWorkspaceResolveRequest(request)
	if err != nil {
		return prworkspace.ProviderSnapshot{}, err
	}
	raw, err := resolver.provider.ReadWorkspacePullJSON(ctx, repository, pullNumber)
	if err != nil {
		return prworkspace.ProviderSnapshot{}, err
	}
	var pull prWorkspaceGitHubPull
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.UseNumber()
	if err = decoder.Decode(&pull); err != nil {
		return prworkspace.ProviderSnapshot{}, errors.New("GitHub pull response is invalid")
	}
	if pull.Number != pullNumber || pull.Base == nil || pull.Base.Repo == nil ||
		pull.Head == nil || pull.Head.Repo == nil || pull.User == nil ||
		!strings.EqualFold(pull.Base.Repo.FullName, repository) ||
		!samePRWorkspacePullURL(pull.HTMLURL, origin, repository, pullNumber) {
		return prworkspace.ProviderSnapshot{}, errors.New("GitHub pull identity mismatch")
	}
	authorID := githubPositiveNumericID(pull.User.ID)
	if authorID == "" || strings.TrimSpace(pull.User.Login) == "" ||
		pull.Base.SHA == "" || pull.Head.SHA == "" || pull.Base.Ref == "" || pull.Head.Ref == "" {
		return prworkspace.ProviderSnapshot{}, errors.New("GitHub pull authority is incomplete")
	}
	baseRepo, err := resolver.resolveRepository(ctx, origin, pull.Base.Repo.FullName)
	if err != nil {
		return prworkspace.ProviderSnapshot{}, err
	}
	headRepo := baseRepo
	if !strings.EqualFold(pull.Head.Repo.FullName, pull.Base.Repo.FullName) {
		headRepo, err = resolver.resolveRepository(ctx, origin, pull.Head.Repo.FullName)
		if err != nil {
			return prworkspace.ProviderSnapshot{}, err
		}
	}
	baseRepoID := githubPositiveNumericID(baseRepo.ID)
	headRepoID := githubPositiveNumericID(headRepo.ID)
	pullID := stableGitHubPullID(origin, baseRepoID, pull.Number)
	viewerRaw, err := resolver.provider.ReadWorkspaceViewerJSON(ctx)
	if err != nil {
		return prworkspace.ProviderSnapshot{}, err
	}
	var viewer prWorkspaceGitHubUser
	if err = json.Unmarshal(viewerRaw, &viewer); err != nil {
		return prworkspace.ProviderSnapshot{}, errors.New("GitHub viewer response is invalid")
	}
	viewerID := githubPositiveNumericID(viewer.ID)
	if viewerID == "" || strings.TrimSpace(viewer.Login) == "" {
		return prworkspace.ProviderSnapshot{}, errors.New("GitHub viewer authority is incomplete")
	}
	owned := viewerID == authorID && strings.EqualFold(viewer.Login, pull.User.Login)
	headPush := githubRepositoryCanPush(headRepo)
	basePush := githubRepositoryCanPush(baseRepo)
	maintainerCanModify := pull.MaintainerCanModify != nil && *pull.MaintainerCanModify
	headWritable := headPush || maintainerCanModify && basePush
	canCreateIssue := resolver.canCreateIssue && baseRepo.HasIssues != nil && *baseRepo.HasIssues &&
		(basePush || baseRepo.Permissions.Triage)
	state := strings.ToLower(strings.TrimSpace(pull.State))
	if pull.Merged {
		state = "merged"
	}
	if state != "open" && state != "closed" && state != "merged" {
		return prworkspace.ProviderSnapshot{}, errors.New("GitHub pull state is invalid")
	}
	if state != "open" || !headWritable {
		return prworkspace.ProviderSnapshot{}, errors.New("GitHub pull request must be open and writable")
	}
	observed := time.Now().UTC()
	revisionDigest := sha256.Sum256([]byte(strings.Join([]string{
		origin, baseRepoID, pullID, pull.Base.SHA, pull.Head.SHA, pull.UpdatedAt,
	}, "\x00")))
	return prworkspace.ProviderSnapshot{
		Provider: "github", ProviderOrigin: origin, RepositoryID: baseRepoID,
		Repository: pull.Base.Repo.FullName, PullRequestID: pullID, PullNumber: pull.Number,
		Title: pull.Title, Body: pull.Body, AuthorID: authorID, AuthorLogin: pull.User.Login,
		AuthenticatedUserID: viewerID, BaseRef: pull.Base.Ref, BaseSHA: pull.Base.SHA,
		HeadRepositoryID: headRepoID, HeadRepository: pull.Head.Repo.FullName,
		HeadRef: pull.Head.Ref, HeadSHA: pull.Head.SHA,
		State: state, Owned: owned, HeadWritable: headWritable, CanReview: resolver.canReview,
		CanCreateIssue:   canCreateIssue,
		ProviderRevision: "sha256:" + hex.EncodeToString(revisionDigest[:]), ObservedAt: observed,
	}, nil
}

func (resolver *prWorkspaceGitHubResolver) resolveRepository(
	ctx context.Context,
	origin string,
	repository string,
) (prWorkspaceGitHubRepo, error) {
	for _, ownerKind := range []string{"user", "org"} {
		raw, err := resolver.provider.SearchWorkspaceRepositoriesJSON(ctx, repository, ownerKind)
		if err != nil {
			return prWorkspaceGitHubRepo{}, err
		}
		matched, found, err := exactPRWorkspaceRepository(raw, origin, repository)
		if err != nil {
			return prWorkspaceGitHubRepo{}, err
		}
		if found {
			return matched, nil
		}
	}
	return prWorkspaceGitHubRepo{}, errors.New("GitHub repository authority is unavailable")
}

func exactPRWorkspaceRepository(
	raw []byte,
	origin string,
	repository string,
) (prWorkspaceGitHubRepo, bool, error) {
	var search prWorkspaceGitHubRepositorySearch
	if err := json.Unmarshal(raw, &search); err != nil || search.TotalCount < 0 ||
		search.IncompleteResults || len(search.Items) > 100 || search.TotalCount < int64(len(search.Items)) {
		return prWorkspaceGitHubRepo{}, false, errors.New("GitHub repository search response is invalid")
	}
	var matches []prWorkspaceGitHubRepo
	for _, candidate := range search.Items {
		if strings.EqualFold(candidate.FullName, repository) {
			matches = append(matches, candidate)
		}
	}
	if len(matches) == 0 {
		return prWorkspaceGitHubRepo{}, false, nil
	}
	if len(matches) != 1 {
		return prWorkspaceGitHubRepo{}, false, errors.New("GitHub repository identity is ambiguous")
	}
	candidate := matches[0]
	owner, name, ok := strings.Cut(repository, "/")
	if !ok || candidate.Owner == nil || candidate.Permissions == nil || candidate.HasIssues == nil ||
		githubPositiveNumericID(candidate.ID) == "" || githubPositiveNumericID(candidate.Owner.ID) == "" ||
		!strings.EqualFold(candidate.Owner.Login, owner) || !strings.EqualFold(candidate.Name, name) ||
		!samePRWorkspaceRepositoryURL(candidate.HTMLURL, origin, repository) {
		return prWorkspaceGitHubRepo{}, false, errors.New("GitHub repository authority is incomplete")
	}
	return candidate, true, nil
}

func githubRepositoryCanPush(repository prWorkspaceGitHubRepo) bool {
	return repository.Permissions != nil &&
		(repository.Permissions.Push || repository.Permissions.Maintain || repository.Permissions.Admin)
}

func stableGitHubPullID(origin, repositoryID string, pullNumber int64) string {
	digest := sha256.Sum256([]byte(strings.Join([]string{
		"github-pull", origin, repositoryID, strconv.FormatInt(pullNumber, 10),
	}, "\x00")))
	return "sha256:" + hex.EncodeToString(digest[:])
}

func (resolver *prWorkspaceGitHubResolver) LoadReviewEvidence(
	ctx context.Context,
	expected prworkspace.ProviderSnapshot,
) (prworkspace.ReviewEvidence, error) {
	if resolver == nil || resolver.provider == nil || expected.Repository == "" || expected.PullNumber < 1 {
		return prworkspace.ReviewEvidence{}, errors.New("GitHub PR review evidence is unavailable")
	}
	resolve := prworkspace.ResolveRequest{
		ProviderOrigin: expected.ProviderOrigin,
		Repository:     expected.Repository,
		PullNumber:     expected.PullNumber,
	}
	before, err := resolver.ResolvePullRequest(ctx, resolve)
	if err != nil {
		return prworkspace.ReviewEvidence{}, err
	}
	if !samePRWorkspaceEvidenceRevision(expected, before) {
		return prworkspace.ReviewEvidence{}, prworkspace.ErrConflict
	}
	diff, err := resolver.provider.ReadWorkspaceDiff(ctx, expected.Repository, expected.PullNumber)
	if err != nil {
		return prworkspace.ReviewEvidence{}, err
	}
	after, err := resolver.ResolvePullRequest(ctx, resolve)
	if err != nil {
		return prworkspace.ReviewEvidence{}, err
	}
	if !samePRWorkspaceEvidenceRevision(before, after) {
		return prworkspace.ReviewEvidence{}, prworkspace.ErrConflict
	}
	return prworkspace.ReviewEvidence{
		ProviderRevision: after.ProviderRevision,
		BaseSHA:          after.BaseSHA,
		HeadSHA:          after.HeadSHA,
		UnifiedDiff:      string(diff),
	}, nil
}

func samePRWorkspaceEvidenceRevision(left, right prworkspace.ProviderSnapshot) bool {
	return left.Provider == right.Provider && left.ProviderOrigin == right.ProviderOrigin &&
		left.RepositoryID == right.RepositoryID && left.PullRequestID == right.PullRequestID &&
		left.BaseSHA == right.BaseSHA && left.HeadRepositoryID == right.HeadRepositoryID &&
		left.HeadRef == right.HeadRef && left.HeadSHA == right.HeadSHA &&
		left.ProviderRevision != "" && left.ProviderRevision == right.ProviderRevision
}

func normalizePRWorkspaceResolveRequest(request prworkspace.ResolveRequest) (string, string, int64, error) {
	if request.PullRequestURL != "" {
		parsed, err := url.ParseRequestURI(request.PullRequestURL)
		if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil ||
			parsed.RawQuery != "" || parsed.Fragment != "" {
			return "", "", 0, prworkspace.ErrInvalid
		}
		segments := strings.Split(strings.Trim(parsed.EscapedPath(), "/"), "/")
		if len(segments) != 4 || segments[0] == "" || segments[1] == "" || segments[2] != "pull" {
			return "", "", 0, prworkspace.ErrInvalid
		}
		owner, ownerErr := url.PathUnescape(segments[0])
		repo, repoErr := url.PathUnescape(segments[1])
		number, numberErr := strconv.ParseInt(segments[3], 10, 64)
		if ownerErr != nil || repoErr != nil || numberErr != nil || number < 1 ||
			owner == "" || repo == "" || strings.ContainsAny(owner+repo, "/\\") {
			return "", "", 0, prworkspace.ErrInvalid
		}
		return parsed.Scheme + "://" + strings.ToLower(parsed.Host), owner + "/" + repo, number, nil
	}
	origin := strings.TrimSuffix(strings.TrimSpace(request.ProviderOrigin), "/")
	parsed, err := url.ParseRequestURI(origin)
	parts := strings.Split(request.Repository, "/")
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil ||
		parsed.Path != "" || parsed.RawQuery != "" || parsed.Fragment != "" || len(parts) != 2 ||
		parts[0] == "" || parts[1] == "" || request.PullNumber < 1 {
		return "", "", 0, prworkspace.ErrInvalid
	}
	return parsed.Scheme + "://" + strings.ToLower(parsed.Host), request.Repository, request.PullNumber, nil
}

func normalizeGitHubIssueURL(raw string) (string, string, int64, error) {
	parsed, err := url.ParseRequestURI(raw)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil ||
		parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", "", 0, prworkspace.ErrInvalid
	}
	segments := strings.Split(strings.Trim(parsed.EscapedPath(), "/"), "/")
	if len(segments) != 4 || segments[0] == "" || segments[1] == "" || segments[2] != "issues" {
		return "", "", 0, prworkspace.ErrInvalid
	}
	owner, ownerErr := url.PathUnescape(segments[0])
	repo, repoErr := url.PathUnescape(segments[1])
	number, numberErr := strconv.ParseInt(segments[3], 10, 64)
	if ownerErr != nil || repoErr != nil || numberErr != nil || number < 1 ||
		owner == "" || repo == "" || strings.ContainsAny(owner+repo, "/\\") {
		return "", "", 0, prworkspace.ErrInvalid
	}
	return parsed.Scheme + "://" + strings.ToLower(parsed.Host), owner + "/" + repo, number, nil
}

func sameGitHubIssueURL(raw, origin, repository string, number int64) bool {
	parsed, err := url.ParseRequestURI(raw)
	if err != nil || parsed.Scheme+"://"+strings.ToLower(parsed.Host) != origin {
		return false
	}
	want := "/" + repository + "/issues/" + strconv.FormatInt(number, 10)
	return strings.EqualFold(strings.TrimSuffix(parsed.Path, "/"), want) &&
		parsed.RawQuery == "" && parsed.Fragment == ""
}

func samePRWorkspacePullURL(raw, origin, repository string, number int64) bool {
	parsed, err := url.ParseRequestURI(raw)
	if err != nil || parsed.Scheme+"://"+strings.ToLower(parsed.Host) != origin {
		return false
	}
	want := "/" + repository + "/pull/" + strconv.FormatInt(number, 10)
	return strings.EqualFold(strings.TrimSuffix(parsed.Path, "/"), want) && parsed.RawQuery == "" &&
		parsed.Fragment == ""
}

func samePRWorkspaceRepositoryURL(raw, origin, repository string) bool {
	parsed, err := url.ParseRequestURI(raw)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil ||
		parsed.Scheme+"://"+strings.ToLower(parsed.Host) != origin || parsed.RawQuery != "" ||
		parsed.Fragment != "" {
		return false
	}
	want := "/" + repository
	return strings.EqualFold(strings.TrimSuffix(parsed.Path, "/"), want)
}

func githubPositiveNumericID(raw json.RawMessage) string {
	value := githubScalarID(raw)
	number, err := strconv.ParseUint(value, 10, 64)
	if err != nil || number == 0 {
		return ""
	}
	return value
}

func githubScalarID(raw json.RawMessage) string {
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" || trimmed == "null" {
		return ""
	}
	if strings.HasPrefix(trimmed, `"`) {
		var value string
		if json.Unmarshal(raw, &value) == nil {
			return strings.TrimSpace(value)
		}
		return ""
	}
	var number json.Number
	if json.Unmarshal(raw, &number) != nil {
		return ""
	}
	if _, err := strconv.ParseInt(number.String(), 10, 64); err != nil {
		return ""
	}
	return number.String()
}

var (
	_ prworkspace.ProviderResolver             = (*prWorkspaceGitHubResolver)(nil)
	_ prworkspace.IssueProviderResolver        = (*prWorkspaceGitHubResolver)(nil)
	_ prworkspace.RepositoryProviderResolver   = (*prWorkspaceGitHubResolver)(nil)
	_ prworkspace.ConfiguredRepositoryLister   = (*prWorkspaceGitHubResolver)(nil)
	_ prworkspace.ConfiguredRepositoryVerifier = (*prWorkspaceGitHubResolver)(nil)
	_ prworkspace.ReviewEvidenceLoader         = (*prWorkspaceGitHubResolver)(nil)
)
