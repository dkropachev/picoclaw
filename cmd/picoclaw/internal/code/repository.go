package code

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/sipeed/picoclaw/pkg/repoaudit"
)

const (
	repositoryGitTimeout  = 2 * time.Second
	repositoryGitMaxBytes = 64 << 10
)

var (
	errInvalidRepositoryReference  = errors.New("invalid repository reference")
	errLocalRepositoryUnavailable  = errors.New("local repository is unavailable")
	errUnsupportedRepositoryOrigin = errors.New("repository origin is not a supported GitHub remote")
)

type repositoryGitRunner func(context.Context, string, ...string) (string, error)

type repositoryResolver struct {
	runGit  repositoryGitRunner
	timeout time.Duration
}

func newRepositoryResolver() repositoryResolver {
	return repositoryResolver{
		runGit:  runRepositoryGitCommand,
		timeout: repositoryGitTimeout,
	}
}

func resolveRepository(ctx context.Context, input string) (string, error) {
	return newRepositoryResolver().resolve(ctx, input)
}

func (r repositoryResolver) resolve(ctx context.Context, input string) (string, error) {
	input = strings.TrimSpace(input)
	if containsRepositoryControl(input) {
		return "", errInvalidRepositoryReference
	}
	if identity, explicit, err := explicitRepositoryIdentity(input); explicit {
		return identity, err
	}

	if ctx == nil {
		ctx = context.Background()
	}
	if r.runGit == nil {
		return "", errLocalRepositoryUnavailable
	}
	if r.timeout <= 0 || r.timeout > repositoryGitTimeout {
		r.timeout = repositoryGitTimeout
	}
	directory := input
	if directory == "" {
		directory = "."
	}

	root, err := r.runBounded(ctx, directory, "rev-parse", "--show-toplevel")
	if err != nil {
		return "", errLocalRepositoryUnavailable
	}
	root, ok := repositoryCommandLine(root)
	if !ok || !filepath.IsAbs(root) {
		return "", errLocalRepositoryUnavailable
	}

	remote, err := r.runBounded(ctx, filepath.Clean(root), "config", "--get", "remote.origin.url")
	if err != nil {
		return "", errUnsupportedRepositoryOrigin
	}
	remote, ok = repositoryCommandLine(remote)
	if !ok {
		return "", errUnsupportedRepositoryOrigin
	}
	identity, ok := inferredRepositoryIdentity(remote)
	if !ok {
		return "", errUnsupportedRepositoryOrigin
	}
	return canonicalGitHubRepositoryURL(identity), nil
}

func (r repositoryResolver) runBounded(
	ctx context.Context,
	directory string,
	arguments ...string,
) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	commandCtx, cancel := context.WithTimeout(ctx, r.timeout)
	defer cancel()
	output, err := r.runGit(commandCtx, directory, arguments...)
	if err != nil || commandCtx.Err() != nil || len(output) > repositoryGitMaxBytes {
		return "", errors.New("repository inspection failed")
	}
	return output, nil
}

func explicitRepositoryIdentity(value string) (string, bool, error) {
	if value == "" {
		return "", false, nil
	}
	if strings.Count(value, "/") == 1 && !filepath.IsAbs(value) && !looksLikeWindowsPath(value) &&
		!strings.HasPrefix(value, "./") && !strings.HasPrefix(value, "../") {
		identity := repoaudit.GitHubRepositoryIdentity(value)
		if identity == "" || !strings.EqualFold(identity, value) {
			return "", true, errInvalidRepositoryReference
		}
		return canonicalGitHubRepositoryURL(identity), true, nil
	}
	if strings.HasPrefix(value, "https://") {
		identity, ok := strictHTTPSGitHubIdentity(value)
		if !ok {
			return "", true, errInvalidRepositoryReference
		}
		return canonicalGitHubRepositoryURL(identity), true, nil
	}
	if looksLikeRemoteReference(value) {
		return "", true, errInvalidRepositoryReference
	}
	return "", false, nil
}

func strictHTTPSGitHubIdentity(value string) (string, bool) {
	parsed, err := url.Parse(value)
	if err != nil || parsed.Scheme != "https" || parsed.Opaque != "" || parsed.User != nil ||
		!strings.EqualFold(parsed.Host, "github.com") || parsed.Port() != "" ||
		parsed.RawPath != "" || strings.ContainsAny(value, "%?#") ||
		strings.Contains(parsed.Path, "\\") {
		return "", false
	}
	return strictGitHubPathIdentity(value, parsed.Path)
}

func strictSSHGitHubIdentity(value string) (string, bool) {
	parsed, err := url.Parse(value)
	if err != nil || parsed.Scheme != "ssh" || parsed.Opaque != "" || parsed.User == nil ||
		parsed.User.Username() != "git" || !strings.EqualFold(parsed.Host, "github.com") ||
		parsed.Port() != "" || parsed.RawPath != "" || strings.ContainsAny(value, "%?#") ||
		strings.Contains(parsed.Path, "\\") {
		return "", false
	}
	if _, hasPassword := parsed.User.Password(); hasPassword {
		return "", false
	}
	return strictGitHubPathIdentity(value, parsed.Path)
}

func strictGitHubPathIdentity(reference, pathValue string) (string, bool) {
	if !strings.HasPrefix(pathValue, "/") || strings.HasSuffix(pathValue, "/") ||
		strings.Contains(pathValue, "//") {
		return "", false
	}
	pathValue = strings.TrimPrefix(pathValue, "/")
	pathIdentity := strings.TrimSuffix(pathValue, ".git")
	if strings.Count(pathIdentity, "/") != 1 {
		return "", false
	}
	owner, name, _ := strings.Cut(pathIdentity, "/")
	if owner == "." || owner == ".." || name == "." || name == ".." {
		return "", false
	}
	identity := repoaudit.GitHubRepositoryIdentity(reference)
	if identity == "" || !strings.EqualFold(identity, pathIdentity) {
		return "", false
	}
	return identity, true
}

func inferredRepositoryIdentity(remote string) (string, bool) {
	if containsRepositoryControl(remote) {
		return "", false
	}
	switch {
	case strings.HasPrefix(remote, "https://"):
		return strictHTTPSGitHubIdentity(remote)
	case strings.HasPrefix(remote, "ssh://"):
		return strictSSHGitHubIdentity(remote)
	default:
		return strictSCPGitHubIdentity(remote)
	}
}

func strictSCPGitHubIdentity(value string) (string, bool) {
	identityPart, pathValue, ok := strings.Cut(value, ":")
	if !ok || strings.Contains(pathValue, ":") || strings.HasPrefix(pathValue, "/") ||
		strings.ContainsAny(value, "%?#\\") {
		return "", false
	}
	user, host, ok := strings.Cut(identityPart, "@")
	if !ok || user != "git" || !strings.EqualFold(host, "github.com") {
		return "", false
	}
	pathIdentity := strings.TrimSuffix(pathValue, ".git")
	if strings.Count(pathIdentity, "/") != 1 {
		return "", false
	}
	identity := repoaudit.GitHubRepositoryIdentity(value)
	if identity == "" || !strings.EqualFold(identity, pathIdentity) {
		return "", false
	}
	return identity, true
}

func canonicalGitHubRepositoryURL(identity string) string {
	return "https://github.com/" + identity
}

func containsRepositoryControl(value string) bool {
	return strings.ContainsAny(value, "\x00\r\n")
}

func looksLikeRemoteReference(value string) bool {
	if looksLikeWindowsPath(value) {
		return false
	}
	if strings.Contains(value, "://") {
		return true
	}
	colon := strings.IndexByte(value, ':')
	separator := strings.IndexAny(value, "/\\")
	return colon > 0 && (separator < 0 || colon < separator)
}

func looksLikeWindowsPath(value string) bool {
	if len(value) < 3 || value[1] != ':' || value[2] != '\\' && value[2] != '/' {
		return false
	}
	return value[0] >= 'a' && value[0] <= 'z' || value[0] >= 'A' && value[0] <= 'Z'
}

func repositoryCommandLine(output string) (string, bool) {
	if len(output) == 0 || len(output) > repositoryGitMaxBytes || strings.ContainsRune(output, '\x00') {
		return "", false
	}
	output = strings.TrimSuffix(output, "\n")
	output = strings.TrimSuffix(output, "\r")
	if strings.ContainsAny(output, "\r\n") {
		return "", false
	}
	output = strings.TrimSpace(output)
	return output, output != ""
}

func runRepositoryGitCommand(
	ctx context.Context,
	directory string,
	arguments ...string,
) (string, error) {
	commandArguments := append([]string{"-C", directory}, arguments...)
	command := exec.CommandContext(ctx, "git", commandArguments...)
	command.Env = repositoryGitEnvironment(os.Environ())
	command.Stdin = nil
	command.Stderr = io.Discard
	var output boundedRepositoryOutput
	command.Stdout = &output
	if err := command.Run(); err != nil || output.overflow {
		return "", errors.New("repository inspection failed")
	}
	return output.buffer.String(), nil
}

func repositoryGitEnvironment(environment []string) []string {
	filtered := make([]string, 0, len(environment)+1)
	for _, variable := range environment {
		name, _, _ := strings.Cut(variable, "=")
		if strings.HasPrefix(strings.ToUpper(name), "GIT_") {
			continue
		}
		filtered = append(filtered, variable)
	}
	return append(filtered, "GIT_OPTIONAL_LOCKS=0")
}

type boundedRepositoryOutput struct {
	buffer   bytes.Buffer
	overflow bool
}

func (w *boundedRepositoryOutput) Write(data []byte) (int, error) {
	written := len(data)
	remaining := repositoryGitMaxBytes + 1 - w.buffer.Len()
	if remaining > 0 {
		if len(data) > remaining {
			data = data[:remaining]
		}
		_, _ = w.buffer.Write(data)
	}
	if w.buffer.Len() > repositoryGitMaxBytes || len(data) < written {
		w.overflow = true
	}
	return written, nil
}
