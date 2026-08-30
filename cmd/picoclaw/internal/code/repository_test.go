package code

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestRepositoryResolverAcceptsOnlyStrictExplicitIdentities(t *testing.T) {
	resolver := repositoryResolver{
		runGit: func(context.Context, string, ...string) (string, error) {
			t.Fatal("explicit repository must not invoke git")
			return "", nil
		},
		timeout: repositoryGitTimeout,
	}
	for _, test := range []struct {
		input string
		want  string
	}{
		{input: "Owner/Repo", want: "https://github.com/owner/repo"},
		{input: " https://github.com/Owner/Repo ", want: "https://github.com/owner/repo"},
		{input: "https://GITHUB.COM/Owner/Repo.git", want: "https://github.com/owner/repo"},
	} {
		got, err := resolver.resolve(t.Context(), test.input)
		if err != nil || got != test.want {
			t.Errorf("resolve(%q) = %q, %v; want %q", test.input, got, err, test.want)
		}
	}

	for _, input := range []string{
		"owner/repo.git",
		"owner/repo!",
		"HTTPS://github.com/owner/repo",
		"http://github.com/owner/repo",
		"ssh://git@github.com/owner/repo.git",
		"git@github.com:owner/repo.git",
		"github.com:owner/repo.git",
		"https://user@github.com/owner/repo",
		"https://user:secret@github.com/owner/repo",
		"https://github.com:443/owner/repo",
		"https://github.com:/owner/repo",
		"https://example.com/owner/repo",
		"https://github.com/owner/repo/extra",
		"https://github.com/owner/../repo",
		"https://github.com/owner//repo",
		"https://github.com/owner/repo/",
		"https://github.com/owner/repo?ref=main",
		"https://github.com/owner/repo#readme",
		"https://github.com/owner/%72epo",
		"https://github.com\\owner\\repo",
		"owner/repo\nsecret",
	} {
		got, err := resolver.resolve(t.Context(), input)
		if got != "" || !errors.Is(err, errInvalidRepositoryReference) {
			t.Errorf("resolve(%q) = %q, %v; want invalid reference", input, got, err)
		}
	}
}

func TestRepositoryResolverInfersCanonicalIdentityFromLocalGitRemote(t *testing.T) {
	for _, remote := range []string{
		"https://github.com/Owner/Repo.git\n",
		"ssh://git@github.com/Owner/Repo.git\n",
		"git@GitHub.Com:Owner/Repo.git\n",
	} {
		t.Run(strings.TrimSpace(remote), func(t *testing.T) {
			type invocation struct {
				directory string
				arguments []string
			}
			var calls []invocation
			resolver := repositoryResolver{
				runGit: func(_ context.Context, directory string, arguments ...string) (string, error) {
					calls = append(calls, invocation{directory: directory, arguments: slices.Clone(arguments)})
					if len(calls) == 1 {
						return "/workspace/root/../root\n", nil
					}
					return remote, nil
				},
				timeout: repositoryGitTimeout,
			}
			got, err := resolver.resolve(t.Context(), "./checkout")
			if err != nil || got != "https://github.com/owner/repo" {
				t.Fatalf("resolve local = %q, %v", got, err)
			}
			want := []invocation{
				{directory: "./checkout", arguments: []string{"rev-parse", "--show-toplevel"}},
				{directory: "/workspace/root", arguments: []string{"config", "--get", "remote.origin.url"}},
			}
			if !slices.EqualFunc(calls, want, func(left, right invocation) bool {
				return left.directory == right.directory && slices.Equal(left.arguments, right.arguments)
			}) {
				t.Fatalf("git calls = %#v, want %#v", calls, want)
			}
		})
	}
}

func TestRepositoryResolverUsesCurrentDirectoryByDefault(t *testing.T) {
	var directories []string
	resolver := repositoryResolver{
		runGit: func(_ context.Context, directory string, arguments ...string) (string, error) {
			directories = append(directories, directory)
			if arguments[0] == "rev-parse" {
				return "/workspace/root", nil
			}
			return "git@github.com:owner/repo.git", nil
		},
		timeout: repositoryGitTimeout,
	}
	got, err := resolver.resolve(nil, "")
	if err != nil || got != "https://github.com/owner/repo" {
		t.Fatalf("resolve default = %q, %v", got, err)
	}
	if !slices.Equal(directories, []string{".", "/workspace/root"}) {
		t.Fatalf("directories = %#v", directories)
	}
}

func TestRepositoryResolverRejectsUnsupportedInferredRemotes(t *testing.T) {
	for _, remote := range []string{
		"owner/repo",
		"http://github.com/owner/repo.git",
		"git://github.com/owner/repo.git",
		"https://user@github.com/owner/repo.git",
		"https://github.com/owner/repo.git?token=secret",
		"https://example.com/owner/repo.git",
		"ssh://github.com/owner/repo.git",
		"ssh://other@github.com/owner/repo.git",
		"ssh://git:secret@github.com/owner/repo.git",
		"ssh://git@github.com:22/owner/repo.git",
		"git@github.com:/owner/repo.git",
		"other@github.com:owner/repo.git",
		"git@example.com:owner/repo.git",
		"github.com:owner/repo.git",
		"git@github.com:owner/repo/extra.git",
		"git@github.com:owner/../repo.git",
		"git@github.com:owner/repo.git\nsecond",
	} {
		t.Run(strings.ReplaceAll(remote, "/", "_"), func(t *testing.T) {
			calls := 0
			resolver := repositoryResolver{
				runGit: func(_ context.Context, _ string, arguments ...string) (string, error) {
					calls++
					if arguments[0] == "rev-parse" {
						return "/workspace/root", nil
					}
					return remote, nil
				},
				timeout: repositoryGitTimeout,
			}
			got, err := resolver.resolve(t.Context(), "./checkout")
			if got != "" || !errors.Is(err, errUnsupportedRepositoryOrigin) || calls != 2 {
				t.Fatalf("resolve remote = %q, %v, calls=%d", got, err, calls)
			}
		})
	}
}

func TestRepositoryResolverBoundsAndSanitizesFailures(t *testing.T) {
	secret := "secret-path-and-remote-canary"
	resolver := repositoryResolver{
		runGit: func(context.Context, string, ...string) (string, error) {
			return "", errors.New(secret)
		},
		timeout: repositoryGitTimeout,
	}
	_, err := resolver.resolve(t.Context(), "/local/"+secret)
	if !errors.Is(err, errLocalRepositoryUnavailable) || strings.Contains(err.Error(), secret) {
		t.Fatalf("first-command error = %v", err)
	}

	calls := 0
	resolver.runGit = func(context.Context, string, ...string) (string, error) {
		calls++
		if calls == 1 {
			return "/workspace/root", nil
		}
		return "", errors.New(secret)
	}
	_, err = resolver.resolve(t.Context(), "/local/"+secret)
	if !errors.Is(err, errUnsupportedRepositoryOrigin) || strings.Contains(err.Error(), secret) {
		t.Fatalf("second-command error = %v", err)
	}

	calls = 0
	resolver.runGit = func(context.Context, string, ...string) (string, error) {
		calls++
		return strings.Repeat("x", repositoryGitMaxBytes+1), nil
	}
	_, err = resolver.resolve(t.Context(), "./checkout")
	if !errors.Is(err, errLocalRepositoryUnavailable) || calls != 1 {
		t.Fatalf("oversized root error = %v, calls=%d", err, calls)
	}

	resolver.runGit = nil
	if _, err = resolver.resolve(t.Context(), "./checkout"); !errors.Is(err, errLocalRepositoryUnavailable) {
		t.Fatalf("nil runner error = %v", err)
	}
}

func TestRepositoryResolverNormalizesTimeoutAndRejectsMalformedRoot(t *testing.T) {
	for _, timeout := range []time.Duration{0, repositoryGitTimeout + time.Second} {
		calls := 0
		resolver := repositoryResolver{
			runGit: func(_ context.Context, _ string, arguments ...string) (string, error) {
				calls++
				if arguments[0] == "rev-parse" {
					return "/workspace/root", nil
				}
				return "git@github.com:owner/repo.git", nil
			},
			timeout: timeout,
		}
		got, err := resolver.resolve(t.Context(), "./checkout")
		if err != nil || got != "https://github.com/owner/repo" || calls != 2 {
			t.Fatalf("timeout %v resolve = %q, %v, calls=%d", timeout, got, err, calls)
		}
	}

	resolver := repositoryResolver{
		runGit: func(context.Context, string, ...string) (string, error) {
			return "relative/root", nil
		},
		timeout: repositoryGitTimeout,
	}
	if _, err := resolver.resolve(t.Context(), "./checkout"); !errors.Is(err, errLocalRepositoryUnavailable) {
		t.Fatalf("relative root error = %v", err)
	}
}

func TestRepositoryParsingHelperEdgeCases(t *testing.T) {
	for _, test := range []struct {
		name string
		got  bool
	}{
		{
			name: "dot owner",
			got: func() bool {
				_, ok := strictGitHubPathIdentity("https://github.com/./repo", "/./repo")
				return ok
			}(),
		},
		{
			name: "reference path mismatch",
			got: func() bool {
				_, ok := strictGitHubPathIdentity("https://github.com/owner/repo", "/other/repo")
				return ok
			}(),
		},
		{
			name: "inferred control",
			got: func() bool {
				_, ok := inferredRepositoryIdentity("git@github.com:owner/repo\n")
				return ok
			}(),
		},
		{
			name: "scp canonical mismatch",
			got: func() bool {
				_, ok := strictSCPGitHubIdentity("git@github.com:owner/repo.git ")
				return ok
			}(),
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			if test.got {
				t.Fatal("malformed repository helper input was accepted")
			}
		})
	}

	if looksLikeRemoteReference(`C:\\checkout`) {
		t.Fatal("Windows path was classified as a remote")
	}
	if !looksLikeWindowsPath(`C:\\checkout`) || !looksLikeWindowsPath(`c:/checkout`) {
		t.Fatal("Windows drive paths were not recognized")
	}
	for _, output := range []string{"", "root\x00tail", strings.Repeat("x", repositoryGitMaxBytes+1)} {
		if value, ok := repositoryCommandLine(output); ok || value != "" {
			t.Fatalf("repositoryCommandLine accepted malformed output of length %d", len(output))
		}
	}
}

func TestRepositoryResolverAppliesPerCommandDeadline(t *testing.T) {
	var calls int
	resolver := repositoryResolver{
		runGit: func(ctx context.Context, _ string, arguments ...string) (string, error) {
			calls++
			deadline, ok := ctx.Deadline()
			if !ok || time.Until(deadline) > 50*time.Millisecond {
				t.Errorf("command %q deadline = %v, %v", arguments[0], deadline, ok)
			}
			<-ctx.Done()
			return "", ctx.Err()
		},
		timeout: 10 * time.Millisecond,
	}
	started := time.Now()
	_, err := resolver.resolve(t.Context(), "./checkout")
	if !errors.Is(err, errLocalRepositoryUnavailable) || calls != 1 || time.Since(started) > time.Second {
		t.Fatalf("timeout resolve err=%v calls=%d elapsed=%v", err, calls, time.Since(started))
	}

	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	calls = 0
	_, err = resolver.resolve(canceled, "./checkout")
	if !errors.Is(err, errLocalRepositoryUnavailable) || calls != 0 {
		t.Fatalf("canceled resolve err=%v calls=%d", err, calls)
	}
}

func TestRepositoryGitEnvironmentStripsInheritedGitVariables(t *testing.T) {
	got := repositoryGitEnvironment([]string{
		"PATH=/bin",
		"GIT_DIR=/secret",
		"git_work_tree=/secret",
		"GITHUB_TOKEN=kept",
		"GIT_OPTIONAL_LOCKS=1",
	})
	want := []string{"PATH=/bin", "GITHUB_TOKEN=kept", "GIT_OPTIONAL_LOCKS=0"}
	if !slices.Equal(got, want) {
		t.Fatalf("environment = %#v, want %#v", got, want)
	}
}

func TestRunRepositoryGitCommandUsesHermeticGitEnvironmentAndDiscardsStderr(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake executable uses a POSIX shell")
	}
	directory := t.TempDir()
	gitPath := filepath.Join(directory, "git")
	script := `#!/bin/sh
printf '%s\n' 'stderr-secret-canary' >&2
if [ "${GIT_DIR-unset}" != "unset" ] || [ "${GIT_WORK_TREE-unset}" != "unset" ]; then
  exit 20
fi
if [ "$GIT_OPTIONAL_LOCKS" != "0" ]; then
  exit 21
fi
if [ "${FAKE_GIT_FAIL-}" = "1" ]; then
  exit 22
fi
case "$3" in
  rev-parse) printf '%s\n' '/tmp/fake-root' ;;
  config) printf '%s\n' 'git@github.com:Owner/Repo.git' ;;
  *) exit 23 ;;
esac
`
	if err := os.WriteFile(gitPath, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", directory+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("GIT_DIR", "/secret")
	t.Setenv("GIT_WORK_TREE", "/secret")
	t.Setenv("GIT_OPTIONAL_LOCKS", "1")

	got, err := resolveRepository(t.Context(), "./checkout")
	if err != nil || got != "https://github.com/owner/repo" {
		t.Fatalf("fake git resolve = %q, %v", got, err)
	}

	t.Setenv("FAKE_GIT_FAIL", "1")
	_, err = runRepositoryGitCommand(t.Context(), ".", "rev-parse", "--show-toplevel")
	if err == nil || strings.Contains(err.Error(), "stderr-secret-canary") {
		t.Fatalf("failed git error = %v", err)
	}
}

func TestRepositoryResolverDoesNotMutateCallerCheckout(t *testing.T) {
	repository := t.TempDir()
	if _, err := runRepositoryGitCommand(t.Context(), repository, "init"); err != nil {
		t.Fatal(err)
	}
	if _, err := runRepositoryGitCommand(
		t.Context(),
		repository,
		"remote",
		"add",
		"origin",
		"git@github.com:Owner/Repo.git",
	); err != nil {
		t.Fatal(err)
	}
	sentinelPath := filepath.Join(repository, "caller-worktree-sentinel")
	if err := os.WriteFile(sentinelPath, []byte("unchanged"), 0o600); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(repository, ".git", "config")
	configBefore, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}

	got, err := resolveRepository(t.Context(), repository)
	if err != nil || got != "https://github.com/owner/repo" {
		t.Fatalf("resolve = %q, %v", got, err)
	}
	configAfter, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	sentinelAfter, err := os.ReadFile(sentinelPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(configAfter, configBefore) || string(sentinelAfter) != "unchanged" {
		t.Fatal("repository resolver mutated caller checkout")
	}
	if _, err := os.Stat(filepath.Join(repository, ".git", "index")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("resolver created Git index: %v", err)
	}
}

func TestBoundedRepositoryOutputRetainsOnlyTheLimit(t *testing.T) {
	var output boundedRepositoryOutput
	data := bytes.Repeat([]byte("x"), repositoryGitMaxBytes+100)
	written, err := output.Write(data)
	if err != nil || written != len(data) || !output.overflow ||
		output.buffer.Len() != repositoryGitMaxBytes+1 {
		t.Fatalf(
			"write = %d, %v; overflow=%v bytes=%d",
			written,
			err,
			output.overflow,
			output.buffer.Len(),
		)
	}
}
