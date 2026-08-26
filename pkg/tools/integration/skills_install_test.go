package integrationtools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sipeed/picoclaw/pkg/skills"
)

type mockInstallRegistry struct{}

const validSkillMarkdown = "---\nname: pr-review\ndescription: Review pull requests\n---\n# PR Review\n"

func (m *mockInstallRegistry) Name() string { return "clawhub" }

func (m *mockInstallRegistry) ResolveInstallDirName(target string) (string, error) {
	return target, nil
}

func (m *mockInstallRegistry) SkillURL(slug, _ string) string { return slug }

func (m *mockInstallRegistry) Search(context.Context, string, int) ([]skills.SearchResult, error) {
	return nil, nil
}

func (m *mockInstallRegistry) GetSkillMeta(context.Context, string) (*skills.SkillMeta, error) {
	return nil, nil
}

func (m *mockInstallRegistry) DownloadAndInstall(
	_ context.Context,
	_ string,
	_ string,
	targetDir string,
) (*skills.InstallResult, error) {
	if err := os.MkdirAll(targetDir, 0o755); err != nil {
		return nil, err
	}
	if err := os.WriteFile(filepath.Join(targetDir, "SKILL.md"), []byte(validSkillMarkdown), 0o600); err != nil {
		return nil, err
	}
	return &skills.InstallResult{Version: "test"}, nil
}

type mockGitHubInstallRegistry struct{}

func (m *mockGitHubInstallRegistry) Name() string { return "github" }

func (m *mockGitHubInstallRegistry) ResolveInstallDirName(target string) (string, error) {
	return "pr-review", nil
}

func (m *mockGitHubInstallRegistry) SkillURL(slug, _ string) string { return slug }

func (m *mockGitHubInstallRegistry) Search(context.Context, string, int) ([]skills.SearchResult, error) {
	return nil, nil
}

func (m *mockGitHubInstallRegistry) GetSkillMeta(context.Context, string) (*skills.SkillMeta, error) {
	return nil, nil
}

func (m *mockGitHubInstallRegistry) DownloadAndInstall(
	_ context.Context,
	_ string,
	_ string,
	targetDir string,
) (*skills.InstallResult, error) {
	if err := os.MkdirAll(targetDir, 0o755); err != nil {
		return nil, err
	}
	if err := os.WriteFile(filepath.Join(targetDir, "SKILL.md"), []byte(validSkillMarkdown), 0o600); err != nil {
		return nil, err
	}
	return &skills.InstallResult{Version: "main"}, nil
}

type stubGitHubInstallRegistry struct {
	*skills.GitHubRegistry
}

func (m *stubGitHubInstallRegistry) DownloadAndInstall(
	_ context.Context,
	_ string,
	_ string,
	targetDir string,
) (*skills.InstallResult, error) {
	if err := os.MkdirAll(targetDir, 0o755); err != nil {
		return nil, err
	}
	if err := os.WriteFile(filepath.Join(targetDir, "SKILL.md"), []byte(validSkillMarkdown), 0o600); err != nil {
		return nil, err
	}
	return &skills.InstallResult{Version: "main"}, nil
}

type mockInvalidInstallRegistry struct{}

type mockFailingInstallRegistry struct{}

type moderatedInstallRegistry struct {
	result skills.InstallResult
}

func (*moderatedInstallRegistry) Name() string { return "clawhub" }

func (*moderatedInstallRegistry) ResolveInstallDirName(target string) (string, error) {
	return target, nil
}

func (*moderatedInstallRegistry) SkillURL(slug, _ string) string { return slug }

func (*moderatedInstallRegistry) Search(
	context.Context,
	string,
	int,
) ([]skills.SearchResult, error) {
	return nil, nil
}

func (*moderatedInstallRegistry) GetSkillMeta(
	context.Context,
	string,
) (*skills.SkillMeta, error) {
	return nil, nil
}

func (registry *moderatedInstallRegistry) DownloadAndInstall(
	_ context.Context,
	_ string,
	_ string,
	targetDir string,
) (*skills.InstallResult, error) {
	if err := os.MkdirAll(targetDir, 0o755); err != nil {
		return nil, err
	}
	if err := os.WriteFile(
		filepath.Join(targetDir, "SKILL.md"),
		[]byte(validSkillMarkdown),
		0o600,
	); err != nil {
		return nil, err
	}
	result := registry.result
	return &result, nil
}

type concurrentInstallRegistry struct {
	calls          atomic.Int64
	secondDownload chan struct{}
	secondOnce     sync.Once
}

func (*concurrentInstallRegistry) Name() string { return "clawhub" }

func (*concurrentInstallRegistry) ResolveInstallDirName(target string) (string, error) {
	return target, nil
}

func (*concurrentInstallRegistry) SkillURL(slug, _ string) string { return slug }

func (*concurrentInstallRegistry) Search(
	context.Context,
	string,
	int,
) ([]skills.SearchResult, error) {
	return nil, nil
}

func (*concurrentInstallRegistry) GetSkillMeta(
	context.Context,
	string,
) (*skills.SkillMeta, error) {
	return nil, nil
}

func (registry *concurrentInstallRegistry) DownloadAndInstall(
	_ context.Context,
	slug string,
	_ string,
	targetDir string,
) (*skills.InstallResult, error) {
	if registry.calls.Add(1) == 2 {
		registry.secondOnce.Do(func() { close(registry.secondDownload) })
	}
	if err := os.MkdirAll(targetDir, 0o755); err != nil {
		return nil, err
	}
	if err := os.WriteFile(
		filepath.Join(targetDir, "SKILL.md"),
		[]byte(fmt.Sprintf(
			"---\nname: %s\ndescription: Concurrent install\n---\n# Concurrent\n",
			slug,
		)),
		0o600,
	); err != nil {
		return nil, err
	}
	return &skills.InstallResult{Version: "concurrent"}, nil
}

func (m *mockInvalidInstallRegistry) Name() string { return "clawhub" }

func (m *mockInvalidInstallRegistry) ResolveInstallDirName(target string) (string, error) {
	return target, nil
}

func (m *mockInvalidInstallRegistry) SkillURL(slug, _ string) string { return slug }

func (m *mockInvalidInstallRegistry) Search(context.Context, string, int) ([]skills.SearchResult, error) {
	return nil, nil
}

func (m *mockInvalidInstallRegistry) GetSkillMeta(context.Context, string) (*skills.SkillMeta, error) {
	return nil, nil
}

func (m *mockInvalidInstallRegistry) DownloadAndInstall(
	_ context.Context,
	_ string,
	_ string,
	targetDir string,
) (*skills.InstallResult, error) {
	if err := os.MkdirAll(targetDir, 0o755); err != nil {
		return nil, err
	}
	if err := os.WriteFile(
		filepath.Join(targetDir, "SKILL.md"),
		[]byte("---\nname: bad_skill\ndescription: invalid name\n---\n# Invalid\n"),
		0o600,
	); err != nil {
		return nil, err
	}
	return &skills.InstallResult{Version: "test"}, nil
}

func (m *mockFailingInstallRegistry) Name() string { return "clawhub" }

func (m *mockFailingInstallRegistry) ResolveInstallDirName(target string) (string, error) {
	return target, nil
}

func (m *mockFailingInstallRegistry) SkillURL(slug, _ string) string { return slug }

func (m *mockFailingInstallRegistry) Search(context.Context, string, int) ([]skills.SearchResult, error) {
	return nil, nil
}

func (m *mockFailingInstallRegistry) GetSkillMeta(context.Context, string) (*skills.SkillMeta, error) {
	return nil, nil
}

func (m *mockFailingInstallRegistry) DownloadAndInstall(
	_ context.Context,
	_ string,
	_ string,
	_ string,
) (*skills.InstallResult, error) {
	return nil, assert.AnError
}

func TestInstallSkillToolName(t *testing.T) {
	tool := NewInstallSkillTool(skills.NewRegistryManager(), t.TempDir())
	assert.Equal(t, "install_skill", tool.Name())
	assert.Contains(t, tool.Description(), "Install a skill")
}

func TestInstallSkillToolLockConstructors(t *testing.T) {
	registryMgr := skills.NewRegistryManager()
	first := NewInstallSkillTool(registryMgr, t.TempDir())
	second := NewInstallSkillTool(registryMgr, t.TempDir())
	if first.installMu == nil || second.installMu == nil || first.installMu == second.installMu {
		t.Fatalf("legacy locks = %p / %p, want distinct non-nil locks", first.installMu, second.installMu)
	}

	shared := &sync.Mutex{}
	owner := NewInstallSkillToolWithLock(registryMgr, t.TempDir(), shared)
	sibling := NewInstallSkillToolWithLock(registryMgr, t.TempDir(), shared)
	if owner.installMu != shared || sibling.installMu != shared {
		t.Fatalf("borrowed locks = %p / %p, want %p", owner.installMu, sibling.installMu, shared)
	}

	nilLock := NewInstallSkillToolWithLock(registryMgr, t.TempDir(), nil)
	if nilLock == nil || nilLock.installMu == nil {
		t.Fatal("nil borrowed lock did not fall back to a safe private lock")
	}
	result := nilLock.Execute(context.Background(), map[string]any{})
	if result == nil || !result.IsError || !strings.Contains(result.ForLLM, "identifier is required") {
		t.Fatalf("nil-lock wrapper execution = %#v", result)
	}
	zeroValueResult := (&InstallSkillTool{}).Execute(context.Background(), map[string]any{})
	if zeroValueResult == nil || !zeroValueResult.IsError ||
		!strings.Contains(zeroValueResult.ForLLM, "identifier is required") {
		t.Fatalf("zero-value wrapper execution = %#v", zeroValueResult)
	}
	zeroValueConfiguredResult := (&InstallSkillTool{}).Execute(
		context.Background(),
		map[string]any{"slug": "example"},
	)
	if zeroValueConfiguredResult == nil || !zeroValueConfiguredResult.IsError ||
		!strings.Contains(zeroValueConfiguredResult.ForLLM, "registry manager is not configured") {
		t.Fatalf("configured zero-value wrapper execution = %#v", zeroValueConfiguredResult)
	}
	var nilTool *InstallSkillTool
	if nilResult := nilTool.Execute(context.Background(), map[string]any{}); nilResult == nil ||
		!nilResult.IsError || !strings.Contains(nilResult.ForLLM, "not configured") {
		t.Fatalf("nil wrapper execution = %#v", nilResult)
	}
}

func TestInstallSkillToolBorrowedWorkspaceLockConcurrency(t *testing.T) {
	for _, test := range []struct {
		name       string
		sharedLock bool
	}{
		{name: "same lock serializes whole install", sharedLock: true},
		{name: "different locks may overlap", sharedLock: false},
	} {
		t.Run(test.name, func(t *testing.T) {
			workspace := t.TempDir()
			registry := &concurrentInstallRegistry{secondDownload: make(chan struct{})}
			registryMgr := skills.NewRegistryManager()
			registryMgr.AddRegistry(registry)
			firstLock := &sync.Mutex{}
			secondLock := firstLock
			if !test.sharedLock {
				secondLock = &sync.Mutex{}
			}
			first := NewInstallSkillToolWithLock(registryMgr, workspace, firstLock)
			second := NewInstallSkillToolWithLock(registryMgr, workspace, secondLock)

			firstPersistEntered := make(chan struct{})
			releaseFirstPersist := make(chan struct{})
			previousPersist := persistInstalledSkillOriginMeta
			var persistCalls atomic.Int64
			persistInstalledSkillOriginMeta = func(
				targetDir string,
				registry skills.SkillRegistry,
				slug, version string,
			) error {
				if persistCalls.Add(1) == 1 {
					close(firstPersistEntered)
					<-releaseFirstPersist
				}
				return previousPersist(targetDir, registry, slug, version)
			}
			defer func() {
				persistInstalledSkillOriginMeta = previousPersist
			}()

			results := make(chan *ToolResult, 2)
			go func() {
				results <- first.Execute(context.Background(), map[string]any{
					"slug": "first-skill", "registry": "clawhub",
				})
			}()
			select {
			case <-firstPersistEntered:
			case <-time.After(3 * time.Second):
				t.Fatal("first install did not reach its final metadata stage")
			}

			secondStarted := make(chan struct{})
			go func() {
				close(secondStarted)
				results <- second.Execute(context.Background(), map[string]any{
					"slug": "second-skill", "registry": "clawhub",
				})
			}()
			<-secondStarted

			if test.sharedLock {
				select {
				case <-registry.secondDownload:
					t.Fatal("second wrapper entered installation while the shared lock was held")
				case <-time.After(100 * time.Millisecond):
				}
			} else {
				select {
				case <-registry.secondDownload:
				case <-time.After(3 * time.Second):
					t.Fatal("different workspace locks did not permit overlapping installs")
				}
			}

			close(releaseFirstPersist)
			for index := 0; index < 2; index++ {
				select {
				case result := <-results:
					if result == nil || result.IsError {
						t.Fatalf("install %d result = %#v", index, result)
					}
				case <-time.After(3 * time.Second):
					t.Fatalf("install %d did not finish", index)
				}
			}
			if registry.calls.Load() != 2 || persistCalls.Load() != 2 {
				t.Fatalf(
					"install stages = download:%d persist:%d",
					registry.calls.Load(),
					persistCalls.Load(),
				)
			}
		})
	}
}

func TestInstallSkillToolRejectsInvalidRegistryIdentifier(t *testing.T) {
	tool := NewInstallSkillTool(skills.NewRegistryManager(), t.TempDir())
	result := tool.Execute(context.Background(), map[string]any{
		"slug": "safe-skill", "registry": "../clawhub",
	})
	if result == nil || !result.IsError ||
		!strings.Contains(result.ForLLM, "invalid registry") {
		t.Fatalf("invalid registry result = %#v", result)
	}
}

func TestInstallSkillToolReportsSkillsDirectoryCreationFailure(t *testing.T) {
	workspaceFile := filepath.Join(t.TempDir(), "workspace-file")
	if err := os.WriteFile(workspaceFile, []byte("not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}
	registryManager := skills.NewRegistryManager()
	registryManager.AddRegistry(&mockInstallRegistry{})
	tool := NewInstallSkillTool(registryManager, workspaceFile)
	result := tool.Execute(context.Background(), map[string]any{
		"slug": "pr-review", "registry": "clawhub",
	})
	if result == nil || !result.IsError ||
		!strings.Contains(result.ForLLM, "failed to create skills directory") {
		t.Fatalf("skills directory failure result = %#v", result)
	}
}

func TestInstallSkillToolRemovesMalwareBlockedInstall(t *testing.T) {
	workspace := t.TempDir()
	registryManager := skills.NewRegistryManager()
	registryManager.AddRegistry(&moderatedInstallRegistry{
		result: skills.InstallResult{Version: "blocked", IsMalwareBlocked: true},
	})
	tool := NewInstallSkillTool(registryManager, workspace)
	result := tool.Execute(context.Background(), map[string]any{
		"slug": "pr-review", "registry": "clawhub",
	})
	if result == nil || !result.IsError ||
		!strings.Contains(result.ForLLM, "flagged as malicious") {
		t.Fatalf("malware result = %#v", result)
	}
	if _, err := os.Stat(filepath.Join(workspace, "skills", "pr-review")); !os.IsNotExist(err) {
		t.Fatalf("malware-blocked target stat error = %v", err)
	}
}

func TestInstallSkillToolSuccessfulReinstallReportsModerationAndCleansBackup(t *testing.T) {
	workspace := t.TempDir()
	targetDir := filepath.Join(workspace, "skills", "pr-review")
	if err := os.MkdirAll(targetDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(targetDir, "old.txt"), []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	registryManager := skills.NewRegistryManager()
	registryManager.AddRegistry(&moderatedInstallRegistry{
		result: skills.InstallResult{
			Version: "reviewed", IsSuspicious: true, Summary: "review summary",
		},
	})
	tool := NewInstallSkillTool(registryManager, workspace)
	result := tool.Execute(context.Background(), map[string]any{
		"slug": "pr-review", "registry": "clawhub", "force": true,
	})
	if result == nil || result.IsError ||
		!strings.Contains(result.ForLLM, "flagged as suspicious") ||
		!strings.Contains(result.ForLLM, "Description: review summary") {
		t.Fatalf("successful moderated reinstall result = %#v", result)
	}
	if _, err := os.Stat(filepath.Join(targetDir, "old.txt")); !os.IsNotExist(err) {
		t.Fatalf("old install marker stat error = %v", err)
	}
	backups, err := filepath.Glob(filepath.Join(workspace, "skills", ".pr-review.picoclaw-backup-*"))
	if err != nil || len(backups) != 0 {
		t.Fatalf("reinstall backups = %v, %v", backups, err)
	}
}

func TestInstallSkillToolMissingSlug(t *testing.T) {
	tool := NewInstallSkillTool(skills.NewRegistryManager(), t.TempDir())
	result := tool.Execute(context.Background(), map[string]any{})
	assert.True(t, result.IsError)
	assert.Contains(t, result.ForLLM, "identifier is required and must be a non-empty string")
}

func TestInstallSkillToolEmptySlug(t *testing.T) {
	tool := NewInstallSkillTool(skills.NewRegistryManager(), t.TempDir())
	result := tool.Execute(context.Background(), map[string]any{
		"slug": "   ",
	})
	assert.True(t, result.IsError)
	assert.Contains(t, result.ForLLM, "identifier is required and must be a non-empty string")
}

func TestInstallSkillToolUnsafeSlug(t *testing.T) {
	registryMgr := skills.NewRegistryManager()
	registryMgr.AddRegistry(skills.NewClawHubRegistry(skills.ClawHubConfig{Enabled: true}))
	tool := NewInstallSkillTool(registryMgr, t.TempDir())

	cases := []string{
		"../etc/passwd",
		"path/traversal",
		"path\\traversal",
	}

	for _, slug := range cases {
		result := tool.Execute(context.Background(), map[string]any{
			"slug":     slug,
			"registry": "clawhub",
		})
		assert.True(t, result.IsError, "slug %q should be rejected", slug)
		assert.Contains(t, result.ForLLM, "invalid slug")
	}
}

func TestInstallSkillToolAlreadyExists(t *testing.T) {
	workspace := t.TempDir()
	skillDir := filepath.Join(workspace, "skills", "existing-skill")
	require.NoError(t, os.MkdirAll(skillDir, 0o755))

	registryMgr := skills.NewRegistryManager()
	registryMgr.AddRegistry(&mockInstallRegistry{})
	tool := NewInstallSkillTool(registryMgr, workspace)
	result := tool.Execute(context.Background(), map[string]any{
		"slug":     "existing-skill",
		"registry": "clawhub",
	})
	assert.True(t, result.IsError)
	assert.Contains(t, result.ForLLM, "already installed")
}

func TestInstallSkillToolRegistryNotFound(t *testing.T) {
	workspace := t.TempDir()
	tool := NewInstallSkillTool(skills.NewRegistryManager(), workspace)
	result := tool.Execute(context.Background(), map[string]any{
		"slug":     "some-skill",
		"registry": "nonexistent",
	})
	assert.True(t, result.IsError)
	assert.Contains(t, result.ForLLM, "registry")
	assert.Contains(t, result.ForLLM, "not found")
}

func TestInstallSkillToolParameters(t *testing.T) {
	tool := NewInstallSkillTool(skills.NewRegistryManager(), t.TempDir())
	params := tool.Parameters()

	props, ok := params["properties"].(map[string]any)
	assert.True(t, ok)
	assert.Contains(t, props, "slug")
	assert.Contains(t, props, "version")
	assert.Contains(t, props, "registry")
	assert.Contains(t, props, "force")

	required, ok := params["required"].([]string)
	assert.True(t, ok)
	assert.Contains(t, required, "slug")
	assert.NotContains(t, required, "registry")
}

func TestInstallSkillToolMissingRegistry(t *testing.T) {
	registryMgr := skills.NewRegistryManager()
	registryMgr.AddRegistry(&mockGitHubInstallRegistry{})
	tool := NewInstallSkillTool(registryMgr, t.TempDir())
	result := tool.Execute(context.Background(), map[string]any{
		"slug": "some-skill",
	})
	assert.False(t, result.IsError)
	assert.Contains(t, result.ForLLM, `Successfully installed skill`)
}

func TestInstallSkillToolAllowsGitHubURLSlug(t *testing.T) {
	registry := skills.GitHubRegistryConfig{Enabled: true, BaseURL: "https://github.com"}.BuildRegistry()
	githubRegistry, ok := registry.(*skills.GitHubRegistry)
	require.True(t, ok)

	registryMgr := skills.NewRegistryManager()
	registryMgr.AddRegistry(&stubGitHubInstallRegistry{GitHubRegistry: githubRegistry})
	workspace := t.TempDir()
	tool := NewInstallSkillTool(registryMgr, workspace)

	slug := "https://github.com/synthetic-lab/octofriend/tree/main/.agents/skills/pr-review"
	result := tool.Execute(context.Background(), map[string]any{
		"slug":     slug,
		"registry": "github",
	})

	assert.False(t, result.IsError)
	assert.Contains(t, result.ForLLM, `Successfully installed skill`)

	data, err := os.ReadFile(filepath.Join(workspace, "skills", "pr-review", ".skill-origin.json"))
	require.NoError(t, err)

	var meta originMeta
	require.NoError(t, json.Unmarshal(data, &meta))
	assert.Equal(t, "third_party", meta.OriginKind)
	assert.Equal(t, "github", meta.Registry)
	assert.Equal(t, "synthetic-lab/octofriend/.agents/skills/pr-review", meta.Slug)
	assert.Equal(t, slug, meta.RegistryURL)
	assert.Equal(t, "main", meta.InstalledVersion)
	assert.NotZero(t, meta.InstalledAt)
}

func TestInstallSkillToolPreservesGitHubSourceURLWithEnterpriseRegistry(t *testing.T) {
	registry := skills.GitHubRegistryConfig{Enabled: true, BaseURL: "https://ghe.example.com/git"}.BuildRegistry()
	githubRegistry, ok := registry.(*skills.GitHubRegistry)
	require.True(t, ok)

	registryMgr := skills.NewRegistryManager()
	registryMgr.AddRegistry(&stubGitHubInstallRegistry{GitHubRegistry: githubRegistry})
	workspace := t.TempDir()
	tool := NewInstallSkillTool(registryMgr, workspace)

	slug := "https://github.com/synthetic-lab/octofriend/tree/main/.agents/skills/pr-review"
	result := tool.Execute(context.Background(), map[string]any{
		"slug":     slug,
		"registry": "github",
	})

	assert.False(t, result.IsError)

	data, err := os.ReadFile(filepath.Join(workspace, "skills", "pr-review", ".skill-origin.json"))
	require.NoError(t, err)

	var meta originMeta
	require.NoError(t, json.Unmarshal(data, &meta))
	assert.Equal(t, "synthetic-lab/octofriend/.agents/skills/pr-review", meta.Slug)
	assert.Equal(t, slug, meta.RegistryURL)
	assert.Equal(t, "main", meta.InstalledVersion)
}

func TestInstallSkillToolRejectsInvalidInstalledSkill(t *testing.T) {
	workspace := t.TempDir()
	registryMgr := skills.NewRegistryManager()
	registryMgr.AddRegistry(&mockInvalidInstallRegistry{})
	tool := NewInstallSkillTool(registryMgr, workspace)

	result := tool.Execute(context.Background(), map[string]any{
		"slug":     "broken-skill",
		"registry": "clawhub",
	})

	assert.True(t, result.IsError)
	assert.Contains(t, result.ForLLM, "not a valid skill")
	_, err := os.Stat(filepath.Join(workspace, "skills", "broken-skill"))
	assert.True(t, os.IsNotExist(err))
}

func TestInstallSkillToolRollsBackOnOriginMetadataWriteFailure(t *testing.T) {
	workspace := t.TempDir()
	registryMgr := skills.NewRegistryManager()
	registryMgr.AddRegistry(&mockInstallRegistry{})
	tool := NewInstallSkillTool(registryMgr, workspace)

	previousPersist := persistInstalledSkillOriginMeta
	persistInstalledSkillOriginMeta = func(string, skills.SkillRegistry, string, string) error {
		return assert.AnError
	}
	defer func() {
		persistInstalledSkillOriginMeta = previousPersist
	}()

	result := tool.Execute(context.Background(), map[string]any{
		"slug":     "rollback-skill",
		"registry": "clawhub",
	})

	assert.True(t, result.IsError)
	assert.Contains(t, result.ForLLM, "failed to persist skill metadata")
	_, err := os.Stat(filepath.Join(workspace, "skills", "rollback-skill"))
	assert.True(t, os.IsNotExist(err))
}

func TestInstallSkillToolForceReinstallRestoresPreviousSkillAfterDownloadFailure(t *testing.T) {
	workspace := t.TempDir()
	skillDir := filepath.Join(workspace, "skills", "existing-skill")
	require.NoError(t, os.MkdirAll(skillDir, 0o755))
	oldContent := []byte("---\nname: existing-skill\ndescription: Existing skill\n---\n# Existing\n")
	require.NoError(t, os.WriteFile(filepath.Join(skillDir, "SKILL.md"), oldContent, 0o600))

	registryMgr := skills.NewRegistryManager()
	registryMgr.AddRegistry(&mockFailingInstallRegistry{})
	tool := NewInstallSkillTool(registryMgr, workspace)

	result := tool.Execute(context.Background(), map[string]any{
		"slug":     "existing-skill",
		"registry": "clawhub",
		"force":    true,
	})

	assert.True(t, result.IsError)
	assert.Contains(t, result.ForLLM, "failed to install")

	gotContent, err := os.ReadFile(filepath.Join(skillDir, "SKILL.md"))
	require.NoError(t, err)
	assert.Equal(t, oldContent, gotContent)
}

func TestInstallSkillToolForceReinstallRestoresPreviousSkillAfterMetadataFailure(t *testing.T) {
	workspace := t.TempDir()
	skillDir := filepath.Join(workspace, "skills", "existing-skill")
	require.NoError(t, os.MkdirAll(skillDir, 0o755))
	oldContent := []byte("---\nname: existing-skill\ndescription: Existing skill\n---\n# Existing\n")
	require.NoError(t, os.WriteFile(filepath.Join(skillDir, "SKILL.md"), oldContent, 0o600))

	registryMgr := skills.NewRegistryManager()
	registryMgr.AddRegistry(&mockInstallRegistry{})
	tool := NewInstallSkillTool(registryMgr, workspace)

	previousPersist := persistInstalledSkillOriginMeta
	persistInstalledSkillOriginMeta = func(string, skills.SkillRegistry, string, string) error {
		return assert.AnError
	}
	defer func() {
		persistInstalledSkillOriginMeta = previousPersist
	}()

	result := tool.Execute(context.Background(), map[string]any{
		"slug":     "existing-skill",
		"registry": "clawhub",
		"force":    true,
	})

	assert.True(t, result.IsError)
	assert.Contains(t, result.ForLLM, "failed to persist skill metadata")

	gotContent, err := os.ReadFile(filepath.Join(skillDir, "SKILL.md"))
	require.NoError(t, err)
	assert.Equal(t, oldContent, gotContent)
}
