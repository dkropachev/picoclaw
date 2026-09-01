//go:build integration

package gateway

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/sipeed/picoclaw/pkg/accountrouter"
	"github.com/sipeed/picoclaw/pkg/auth"
	"github.com/sipeed/picoclaw/pkg/bus"
	"github.com/sipeed/picoclaw/pkg/channels/wecom"
	"github.com/sipeed/picoclaw/pkg/channels/weixin"
	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/cron"
	"github.com/sipeed/picoclaw/pkg/evolution"
	"github.com/sipeed/picoclaw/pkg/memory"
	"github.com/sipeed/picoclaw/pkg/pid"
	"github.com/sipeed/picoclaw/pkg/providers"
	"github.com/sipeed/picoclaw/pkg/prworkspace/localci"
	"github.com/sipeed/picoclaw/pkg/state"
	"github.com/sipeed/picoclaw/pkg/threads"
	"github.com/sipeed/picoclaw/pkg/tools"
	"github.com/sipeed/picoclaw/pkg/workflows"
	"github.com/sipeed/picoclaw/web/backend/api"
	"github.com/sipeed/picoclaw/web/backend/dashboardauth"
	"github.com/sipeed/picoclaw/web/backend/launcherconfig"
)

const runtimeStoragePassword = "runtime-storage-integration-password"

type runtimeStorageIntegrationFixture struct {
	root          string
	home          string
	workspace     string
	evolutionRoot string
	evidenceRoot  string
	configPath    string
	launcherPath  string
	pidPath       string
	threadID      string
	handoffID     string
	cronJobID     string
	planDigest    string
	resultKey     string
	executionID   string
}

type runtimeStorageScan struct {
	databases []string
	jsonFiles []string
}

// TestIntegrationRuntimeOwnedJSONAllowlist exercises every SQLite migration
// already present on main, restarts its public owners, and scans the complete
// disposable PicoClaw home/workspace/custom roots. A new mutable JSON store is
// therefore a merge-blocking failure unless it is one of the narrowly defined
// recovery, configuration, archive, or immutable-artifact exceptions below.
func TestIntegrationRuntimeOwnedJSONAllowlist(t *testing.T) {
	fixture := newRuntimeStorageIntegrationFixture(t)
	allowlist := runtimeStorageJSONAllowlist{fixture: fixture}
	assertRuntimeStorageAllowlistExamples(t, allowlist)

	exerciseRuntimeStorageFirstStartup(t, fixture)
	first := scanRuntimeStorage(t, allowlist)
	assertRuntimeStorageDatabases(t, fixture, first.databases)
	assertRuntimeStorageJSONFiles(t, fixture, first.jsonFiles)

	exerciseRuntimeStorageSecondStartup(t, fixture)
	second := scanRuntimeStorage(t, allowlist)
	if !slices.Equal(first.databases, second.databases) {
		t.Fatalf(
			"database set changed after restart:\nfirst=%v\nsecond=%v",
			first.databases,
			second.databases,
		)
	}
	if !slices.Equal(first.jsonFiles, second.jsonFiles) {
		t.Fatalf(
			"JSON set changed after restart:\nfirst=%v\nsecond=%v",
			first.jsonFiles,
			second.jsonFiles,
		)
	}

	canaryPath := filepath.Join(fixture.workspace, "state", "runtime-owned-canary.json")
	if err := os.WriteFile(canaryPath, []byte("private-canary-payload"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := scanRuntimeStorageTree(allowlist)
	if err == nil || !strings.Contains(err.Error(), "workspace/state/runtime-owned-canary.json") ||
		strings.Contains(err.Error(), "private-canary-payload") {
		t.Fatalf("unexpected canary scan result: %v", err)
	}
	if err := os.Remove(canaryPath); err != nil {
		t.Fatal(err)
	}
	final := scanRuntimeStorage(t, allowlist)
	databasesChanged := !slices.Equal(second.databases, final.databases)
	jsonChanged := !slices.Equal(second.jsonFiles, final.jsonFiles)
	if databasesChanged || jsonChanged {
		t.Fatalf("storage set changed after canary removal: %#v", final)
	}
}

func newRuntimeStorageIntegrationFixture(t *testing.T) *runtimeStorageIntegrationFixture {
	t.Helper()
	root := t.TempDir()
	fixture := &runtimeStorageIntegrationFixture{
		root:          root,
		home:          filepath.Join(root, "home"),
		workspace:     filepath.Join(root, "workspace"),
		evolutionRoot: filepath.Join(root, "custom-evolution"),
		evidenceRoot:  filepath.Join(root, "local-ci-evidence"),
	}
	fixture.configPath = filepath.Join(fixture.home, "config.json")
	fixture.launcherPath = filepath.Join(fixture.home, launcherconfig.FileName)
	fixture.pidPath = filepath.Join(fixture.home, ".picoclaw.pid")
	for _, directory := range []string{fixture.home, fixture.workspace} {
		if err := os.MkdirAll(directory, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv(config.EnvHome, fixture.home)
	t.Setenv(config.EnvConfig, fixture.configPath)
	operatorHome := filepath.Join(root, "operator-home")
	if err := os.MkdirAll(operatorHome, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", operatorHome)

	cfg := config.DefaultConfig()
	cfg.Agents.Defaults.Workspace = fixture.workspace
	if err := config.SaveConfig(fixture.configPath, cfg); err != nil {
		t.Fatal(err)
	}
	if err := launcherconfig.Save(fixture.launcherPath, launcherconfig.Default()); err != nil {
		t.Fatal(err)
	}
	metadata, err := pid.WritePidFile(fixture.home, "127.0.0.1", 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pid.RemovePidFileIfPID(fixture.home, metadata.PID) })
	return fixture
}

//nolint:govet // Integration assertions intentionally keep independent errors in narrow scopes.
func exerciseRuntimeStorageFirstStartup(t *testing.T, fixture *runtimeStorageIntegrationFixture) {
	t.Helper()
	ctx := context.Background()

	credential := &auth.AuthCredential{
		AccessToken: "integration-token", Provider: "openai", AuthMethod: "token",
		ExpiresAt: time.Now().UTC().Add(time.Hour),
	}
	if err := auth.SetCredential("openai", credential); err != nil {
		t.Fatal(err)
	}
	loadedCredential, loadCredentialErr := auth.GetCredential("openai")
	if loadCredentialErr != nil || loadedCredential == nil ||
		loadedCredential.AccessToken != credential.AccessToken {
		t.Fatalf("auth credential = %#v, %v", loadedCredential, loadCredentialErr)
	}

	dashboard, err := dashboardauth.New(fixture.home)
	if err != nil {
		t.Fatal(err)
	}
	if err := dashboard.SetPassword(ctx, runtimeStoragePassword); err != nil {
		_ = dashboard.Close()
		t.Fatal(err)
	}
	if err := dashboard.Close(); err != nil {
		t.Fatal(err)
	}

	if err := api.SaveCatalog(
		"openai",
		"https://api.example.invalid/v1",
		"catalog-key",
		[]api.CatalogModel{{
			ID: "runtime-model", OwnedBy: "integration", Extra: map[string]any{"nested": []any{1, "two"}},
		}},
	); err != nil {
		t.Fatal(err)
	}

	adaptationProfile := tools.ToolAdaptationProfile{Provider: "integration", Model: "runtime-model"}
	if _, ok := tools.ObserveToolAdaptationToolOutcome(
		adaptationProfile, config.ToolSurfacePicoClaw, "read_file", true, "", time.Millisecond,
	); !ok {
		t.Fatal("tool-adaptation outcome was not admitted")
	}

	exerciseChannelStorage(t, ctx)

	sessionsDir := filepath.Join(fixture.workspace, "sessions")
	sessionStore, err := memory.NewSQLiteStore(sessionsDir)
	if err != nil {
		t.Fatal(err)
	}
	if err := sessionStore.AddMessage(ctx, "origin-session", "user", "origin message"); err != nil {
		_ = sessionStore.Close()
		t.Fatal(err)
	}
	if err := sessionStore.Close(); err != nil {
		t.Fatal(err)
	}
	threadStore := threads.NewStoreFromWorkspace(fixture.workspace)
	thread, err := threadStore.CreateThread(ctx, threads.CreateRequest{
		PrimarySessionKey: "thread-target-session", Title: "Runtime storage thread",
		AgentID: "main", OwnerIdentity: "integration", Registration: "manual",
	})
	if err != nil {
		t.Fatal(err)
	}
	_, handoff, err := threadStore.AttachCurrent(ctx, threads.AttachRequest{
		ThreadID: thread.ID, SessionKey: "origin-session", AgentID: "main",
		OwnerIdentity: "integration", OriginSessionID: "origin-ui", Summary: "continue safely",
	})
	if err != nil {
		t.Fatal(err)
	}
	fixture.threadID, fixture.handoffID = thread.ID, handoff.ID

	cronPath := filepath.Join(fixture.workspace, "cron", "jobs.db")
	cronService, err := cron.NewSQLiteCronService(cronPath, nil)
	if err != nil {
		t.Fatal(err)
	}
	every := int64(time.Hour / time.Millisecond)
	job, err := cronService.AddJob(
		"runtime storage", cron.CronSchedule{Kind: "every", EveryMS: &every},
		"check runtime storage", "cli", "integration",
	)
	if err != nil {
		_ = cronService.Close()
		t.Fatal(err)
	}
	fixture.cronJobID = job.ID
	if err := cronService.Close(); err != nil {
		t.Fatal(err)
	}

	runtimeState, err := state.NewSQLiteManager(fixture.workspace)
	if err != nil {
		t.Fatal(err)
	}
	if err := runtimeState.SetLastChannel("cli"); err != nil {
		t.Fatal(err)
	}
	if err := runtimeState.SetLastChatID("integration-chat"); err != nil {
		t.Fatal(err)
	}

	exerciseAccountRouterStorage(t, fixture)

	workflowStore, err := workflows.NewSQLiteRunStore(fixture.workspace)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	run := &workflows.Run{
		ID: "wr_runtime_storage", WorkflowRef: "workflows/runtime-storage.yml",
		Status: workflows.RunStatusRunning, Inputs: map[string]any{"exact": 1},
		CreatedAt: now, UpdatedAt: now,
	}
	if err := workflowStore.CreateRun(ctx, run); err != nil {
		t.Fatal(err)
	}
	if err := workflowStore.AppendEvent(ctx, workflows.RunEvent{
		Time: now, Kind: "workflow.runtime-storage", RunID: run.ID,
		Payload: map[string]any{"ordered": []any{1, 2, 3}},
	}); err != nil {
		t.Fatal(err)
	}
	if err := workflowStore.Close(); err != nil {
		t.Fatal(err)
	}

	evolutionPaths := evolution.NewPaths(fixture.workspace, fixture.evolutionRoot)
	evolutionStore := evolution.NewSQLiteStore(evolutionPaths)
	if err := evolutionStore.SaveTaskRecords(nil); err != nil {
		t.Fatal(err)
	}
	if err := evolutionStore.Close(); err != nil {
		t.Fatal(err)
	}

	fixture.planDigest, fixture.resultKey, fixture.executionID = writeLocalCIPassingEvidence(
		t,
		fixture.evidenceRoot,
	)

	// TODO(runtime-json-allowlist): after the remaining held migration branches
	// merge, extend this same fixture with gitworkspace.NewManager inventory
	// mutation + checkpoint Save and repoaudit/repoeval create/reopen
	// assertions. Calling those mutations on this base would intentionally
	// create the legacy mutable JSON stores that the held branches remove.
	time.Sleep(150 * time.Millisecond) // release workflow/channel idle handles before scanning
}

func exerciseChannelStorage(t *testing.T, ctx context.Context) {
	t.Helper()
	messageBus := bus.NewMessageBus()
	defer messageBus.Close()

	wecomChannelConfig := &config.Channel{}
	wecomChannelConfig.SetName(config.ChannelWeCom)
	wecomSettings := &config.WeComSettings{BotID: "runtime-storage-bot"}
	wecomSettings.SetSecret("runtime-storage-secret")
	if _, err := wecom.NewChannel(wecomChannelConfig, wecomSettings, messageBus); err != nil {
		t.Fatal(err)
	}

	weixinChannelConfig := &config.Channel{}
	weixinChannelConfig.SetName(config.ChannelWeixin)
	weixinSettings := &config.WeixinSettings{BaseURL: "https://weixin.example.invalid/"}
	weixinSettings.SetToken("runtime-storage-token")
	channel, err := weixin.NewWeixinChannel(weixinChannelConfig, weixinSettings, messageBus)
	if err != nil {
		t.Fatal(err)
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if err := channel.Start(canceled); err != nil {
		t.Fatal(err)
	}
	if err := channel.Stop(ctx); err != nil {
		t.Fatal(err)
	}
}

func exerciseAccountRouterStorage(t *testing.T, fixture *runtimeStorageIntegrationFixture) {
	t.Helper()
	routerConfig := &config.AccountRouterConfig{
		Name: "runtime-storage", Enabled: true, Entry: "primary",
		Blocks: []config.AccountRouterBlock{{
			ID: "primary", Type: config.AccountRouterBlockTypeAccount, Account: "primary-account",
		}},
	}
	accounts := map[string]accountrouter.Account{
		"primary-account": {
			Candidates: []providers.FallbackCandidate{{Provider: "openai", Model: "runtime-model"}},
		},
	}
	router, err := accountrouter.NewSQLite(
		routerConfig.Name, routerConfig, accounts,
		filepath.Join(fixture.workspace, "state", "account-router.db"),
	)
	if err != nil {
		t.Fatal(err)
	}
	selection := router.Select("runtime-storage-session", accountrouter.SelectReasonInitial)
	if len(selection.Candidates) != 1 {
		t.Fatalf("router selection = %#v", selection)
	}
}

//nolint:govet // Integration assertions intentionally keep independent errors in narrow scopes.
func writeLocalCIPassingEvidence(t *testing.T, evidenceRoot string) (string, string, string) {
	t.Helper()
	store, err := localci.OpenFileEvidenceStore(evidenceRoot)
	if err != nil {
		t.Fatal(err)
	}
	plan := localci.Plan{
		DefinitionDigest: strings.Repeat("1", 64), DependencyDigest: strings.Repeat("2", 64),
		Complete: true,
		Steps: []localci.Step{{
			ID: "runtime_storage", Name: "runtime storage", Kind: localci.StepTest,
			Origin: localci.OriginExplicit, Source: ".picoclaw/ci.yml",
			Argv: []string{"true"}, TimeoutSeconds: 30, Required: true,
		}},
	}
	if err := store.PutPlan(context.Background(), plan); err != nil {
		_ = store.Close()
		t.Fatal(err)
	}
	planDigest := runtimeStorageSingleEvidenceDigest(t, evidenceRoot, "plans")
	normalizedPlan, found, err := store.GetPlan(context.Background(), planDigest)
	if err != nil || !found {
		_ = store.Close()
		t.Fatalf("normalized local-CI plan = %#v, %v, %v", normalizedPlan, found, err)
	}
	evidence := localci.CandidateEvidence{
		Repository:              "github.com/example/runtime-storage",
		ParentCommit:            strings.Repeat("3", 40),
		Tree:                    strings.Repeat("4", 40),
		CandidateDigest:         strings.Repeat("5", 64),
		ParentManifestDigest:    strings.Repeat("6", 64),
		CandidateManifestDigest: strings.Repeat("7", 64),
		DependencyDigest:        normalizedPlan.DependencyDigest,
		PlanDigest:              normalizedPlan.Digest,
		EnvironmentDigest:       strings.Repeat("8", 64),
		Limits: localci.LimitEvidence{
			StepTimeoutMillis: 30_000, TotalTimeoutMillis: 60_000,
			OutputBytes: 4096, ResourcePolicy: "aggregate-resource-policy-v1",
		},
	}
	resultKey := runtimeStorageDigestJSON(t, "picoclaw-local-ci-result-key-v1", evidence)
	now := time.Now().UTC()
	execution := localci.Execution{
		ResultKey: resultKey, Evidence: evidence, Status: localci.StatusPassed,
		StartedAt: now, CompletedAt: now.Add(time.Millisecond),
		Steps: []localci.StepResult{{
			StepID: normalizedPlan.Steps[0].ID, Status: localci.StatusPassed, ExitCode: 0,
			OutputDigest:   runtimeStorageDigestParts("picoclaw-local-ci-output-v1", nil),
			DurationMillis: 1,
		}},
	}
	if err := store.PutExecution(context.Background(), execution); err != nil {
		_ = store.Close()
		t.Fatal(err)
	}
	executionDigest := runtimeStorageSingleEvidenceDigest(t, evidenceRoot, "executions")
	if err := store.PromotePassing(context.Background(), resultKey, executionDigest); err != nil {
		_ = store.Close()
		t.Fatal(err)
	}
	cached, cacheFound, cacheErr := store.LookupPassing(context.Background(), resultKey)
	if cacheErr != nil || !cacheFound || cached.Digest != executionDigest {
		_ = store.Close()
		t.Fatalf("promoted local-CI cache = %#v, %v, %v", cached, cacheFound, cacheErr)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	return planDigest, resultKey, executionDigest
}

func runtimeStorageSingleEvidenceDigest(t *testing.T, root, kind string) string {
	t.Helper()
	matches, err := filepath.Glob(filepath.Join(root, kind, "*", "*.json"))
	if err != nil || len(matches) != 1 {
		t.Fatalf("local-CI %s files = %v, %v", kind, matches, err)
	}
	return strings.TrimSuffix(filepath.Base(matches[0]), ".json")
}

func runtimeStorageDigestJSON(t *testing.T, domain string, value any) string {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return runtimeStorageDigestParts(domain, encoded)
}

func runtimeStorageDigestParts(domain string, parts ...[]byte) string {
	hash := sha256.New()
	writePart := func(value []byte) {
		var size [8]byte
		binary.BigEndian.PutUint64(size[:], uint64(len(value)))
		_, _ = hash.Write(size[:])
		_, _ = hash.Write(value)
	}
	writePart([]byte(domain))
	for _, part := range parts {
		writePart(part)
	}
	return hex.EncodeToString(hash.Sum(nil))
}

//nolint:govet // Integration assertions intentionally keep independent errors in narrow scopes.
func exerciseRuntimeStorageSecondStartup(t *testing.T, fixture *runtimeStorageIntegrationFixture) {
	t.Helper()
	ctx := context.Background()

	loadedCredential, loadCredentialErr := auth.GetCredential("openai")
	if loadCredentialErr != nil || loadedCredential == nil ||
		loadedCredential.AccessToken != "integration-token" {
		t.Fatalf("reopened auth credential = %#v, %v", loadedCredential, loadCredentialErr)
	}
	dashboard, err := dashboardauth.New(fixture.home)
	if err != nil {
		t.Fatal(err)
	}
	if initialized, err := dashboard.IsInitialized(ctx); err != nil || !initialized {
		_ = dashboard.Close()
		t.Fatalf("reopened dashboard initialized = %v, %v", initialized, err)
	}
	if valid, err := dashboard.VerifyPassword(ctx, runtimeStoragePassword); err != nil || !valid {
		_ = dashboard.Close()
		t.Fatalf("reopened dashboard password = %v, %v", valid, err)
	}
	if err := dashboard.Close(); err != nil {
		t.Fatal(err)
	}
	if err := api.SaveCatalog(
		"openai",
		"https://api.example.invalid/v1",
		"catalog-key",
		[]api.CatalogModel{{
			ID: "runtime-model", OwnedBy: "integration", Extra: map[string]any{"nested": []any{1, "two"}},
		}},
	); err != nil {
		t.Fatal(err)
	}
	if outcomes := tools.LatestToolAdaptationToolOutcomes(
		tools.ToolAdaptationProfile{Provider: "integration", Model: "runtime-model"},
	); len(outcomes) != 1 || outcomes[0].Successes != 1 {
		t.Fatalf("reopened adaptation outcomes = %#v", outcomes)
	}
	exerciseChannelStorage(t, ctx)

	sessionStore, err := memory.NewSQLiteStore(filepath.Join(fixture.workspace, "sessions"))
	if err != nil {
		t.Fatal(err)
	}
	history, err := sessionStore.GetHistory(ctx, "origin-session")
	if err != nil || len(history) != 1 || history[0].Content != "origin message" {
		_ = sessionStore.Close()
		t.Fatalf("reopened session history = %#v, %v", history, err)
	}
	if err := sessionStore.Close(); err != nil {
		t.Fatal(err)
	}
	threadStore := threads.NewStoreFromWorkspace(fixture.workspace)
	thread, threadFound, threadErr := threadStore.Get(fixture.threadID)
	if threadErr != nil || !threadFound || thread.ID != fixture.threadID {
		t.Fatalf("reopened thread = %#v, %v, %v", thread, threadFound, threadErr)
	}
	handoff, handoffFound, handoffErr := threadStore.ReturnToOrigin(fixture.handoffID)
	if handoffErr != nil || !handoffFound || handoff.ID != fixture.handoffID {
		t.Fatalf("reopened handoff = %#v, %v, %v", handoff, handoffFound, handoffErr)
	}

	cronPath := filepath.Join(fixture.workspace, "cron", "jobs.db")
	cronService, err := cron.NewSQLiteCronService(cronPath, nil)
	if err != nil {
		t.Fatal(err)
	}
	job, jobFound := cronService.GetJob(fixture.cronJobID)
	if !jobFound || job == nil || job.ID != fixture.cronJobID {
		_ = cronService.Close()
		t.Fatalf("reopened cron job = %#v, %v", job, jobFound)
	}
	if err := cronService.Close(); err != nil {
		t.Fatal(err)
	}

	runtimeState, err := state.NewSQLiteManager(fixture.workspace)
	if err != nil {
		t.Fatal(err)
	}
	if runtimeState.GetLastChannel() != "cli" || runtimeState.GetLastChatID() != "integration-chat" {
		t.Fatalf(
			"reopened runtime state = %q/%q",
			runtimeState.GetLastChannel(),
			runtimeState.GetLastChatID(),
		)
	}
	exerciseAccountRouterStorage(t, fixture)
	keys, err := accountrouter.SessionKeys(
		filepath.Join(fixture.workspace, "state", "account-router.db"), "runtime-storage",
	)
	if err != nil || !slices.Contains(keys, "runtime-storage-session") {
		t.Fatalf("reopened router session keys = %v, %v", keys, err)
	}

	workflowStore, err := workflows.NewSQLiteRunStore(fixture.workspace)
	if err != nil {
		t.Fatal(err)
	}
	run, loadRunErr := workflowStore.GetRun(ctx, "wr_runtime_storage")
	if loadRunErr != nil || run.ID != "wr_runtime_storage" {
		t.Fatalf("reopened workflow run = %#v, %v", run, loadRunErr)
	}
	if events, err := workflowStore.Events(ctx, "wr_runtime_storage"); err != nil || len(events) != 1 {
		t.Fatalf("reopened workflow events = %#v, %v", events, err)
	}
	if err := workflowStore.Close(); err != nil {
		t.Fatal(err)
	}

	evolutionPaths := evolution.NewPaths(fixture.workspace, fixture.evolutionRoot)
	evolutionStore := evolution.NewSQLiteStore(evolutionPaths)
	if records, err := evolutionStore.LoadTaskRecords(); err != nil || len(records) != 0 {
		t.Fatalf("reopened evolution records = %#v, %v", records, err)
	}
	if err := evolutionStore.Close(); err != nil {
		t.Fatal(err)
	}

	evidenceStore, err := localci.OpenFileEvidenceStore(fixture.evidenceRoot)
	if err != nil {
		t.Fatal(err)
	}
	plan, planFound, planErr := evidenceStore.GetPlan(ctx, fixture.planDigest)
	if planErr != nil || !planFound || plan.Digest != fixture.planDigest {
		_ = evidenceStore.Close()
		t.Fatalf("reopened local-CI plan = %#v, %v, %v", plan, planFound, planErr)
	}
	cached, cacheFound, cacheErr := evidenceStore.LookupPassing(ctx, fixture.resultKey)
	if cacheErr != nil || !cacheFound || cached.Digest != fixture.executionID {
		_ = evidenceStore.Close()
		t.Fatalf("reopened local-CI cache = %#v, %v, %v", cached, cacheFound, cacheErr)
	}
	if err := evidenceStore.Close(); err != nil {
		t.Fatal(err)
	}

	cfg, loadConfigErr := config.LoadConfig(fixture.configPath)
	if loadConfigErr != nil || cfg.WorkspacePath() != fixture.workspace {
		t.Fatalf("reopened config workspace = %#v, %v", cfg, loadConfigErr)
	}
	if _, err := launcherconfig.Load(fixture.launcherPath, launcherconfig.Default()); err != nil {
		t.Fatal(err)
	}
	if metadata := pid.PeekPidFile(fixture.home); metadata == nil || metadata.PID != os.Getpid() {
		t.Fatalf("reopened PID metadata = %#v", metadata)
	}
	time.Sleep(150 * time.Millisecond)
}

type runtimeStorageJSONAllowlist struct {
	fixture *runtimeStorageIntegrationFixture
}

func (allowlist runtimeStorageJSONAllowlist) allows(path string) bool {
	path = filepath.Clean(path)
	fixture := allowlist.fixture
	for _, exact := range []string{fixture.configPath, fixture.launcherPath, fixture.pidPath} {
		if path == filepath.Clean(exact) {
			return true
		}
	}
	relative, ok := runtimeStorageRelative(fixture.root, path)
	if !ok {
		return false
	}
	parts := strings.Split(relative, "/")
	if slices.Contains(parts, "legacy-json") {
		return true
	}
	if relative == runtimeStorageSlashRelative(fixture.root, filepath.Join(
		fixture.workspace, "workflow_state", "publish-transaction.json",
	)) || relative == runtimeStorageSlashRelative(fixture.root, filepath.Join(
		fixture.workspace, "workflow_state", "template-transaction.json",
	)) {
		return true
	}
	if runtimeStorageHasPrefix(path, filepath.Join(fixture.workspace, "workflow_artifacts")) &&
		runtimeStorageJSONExtension(path) {
		return true
	}
	workspaceSkills := filepath.Join(fixture.workspace, "skills")
	homeSkills := filepath.Join(fixture.home, "skills")
	if filepath.Base(path) == ".skill-origin.json" &&
		(runtimeStorageHasPrefix(path, workspaceSkills) || runtimeStorageHasPrefix(path, homeSkills)) {
		return true
	}
	if runtimeStorageApplyPatchJournal(fixture.home, path) {
		return true
	}
	return runtimeStorageImmutableEvidenceJSON(fixture.evidenceRoot, path)
}

func runtimeStorageApplyPatchJournal(home, path string) bool {
	relative, ok := runtimeStorageRelative(filepath.Join(home, "apply_patch_transactions"), path)
	if !ok {
		return false
	}
	parts := strings.Split(relative, "/")
	if len(parts) != 4 || parts[0] != "workspaces" || !runtimeStorageLowerHex(parts[1], 64) ||
		parts[3] != "journal.json" {
		return false
	}
	directory := parts[2]
	for _, prefix := range []string{"active-", "committed-"} {
		if strings.HasPrefix(directory, prefix) {
			return runtimeStorageLowerHex(strings.TrimPrefix(directory, prefix), 32)
		}
	}
	return false
}

func runtimeStorageImmutableEvidenceJSON(root, path string) bool {
	relative, ok := runtimeStorageRelative(root, path)
	if !ok {
		return false
	}
	parts := strings.Split(relative, "/")
	immutableKinds := []string{"plans", "executions", "attestations", "discovery"}
	if len(parts) != 3 || !slices.Contains(immutableKinds, parts[0]) ||
		!runtimeStorageLowerHex(parts[1], 2) || !strings.HasSuffix(parts[2], ".json") {
		return false
	}
	digest := strings.TrimSuffix(parts[2], ".json")
	return runtimeStorageLowerHex(digest, 64) && parts[1] == digest[:2]
}

func runtimeStorageLowerHex(value string, length int) bool {
	if len(value) != length {
		return false
	}
	for _, character := range value {
		if character < '0' || character > '9' && character < 'a' || character > 'f' {
			return false
		}
	}
	return true
}

func runtimeStorageJSONExtension(path string) bool {
	extension := strings.ToLower(filepath.Ext(path))
	return extension == ".json" || extension == ".jsonl"
}

func runtimeStorageHasPrefix(path, root string) bool {
	_, ok := runtimeStorageRelative(root, path)
	return ok
}

func runtimeStorageRelative(root, path string) (string, bool) {
	relative, err := filepath.Rel(filepath.Clean(root), filepath.Clean(path))
	escapes := strings.HasPrefix(relative, ".."+string(filepath.Separator))
	if err != nil || relative == "." || relative == ".." || escapes {
		return "", false
	}
	return filepath.ToSlash(relative), true
}

func runtimeStorageSlashRelative(root, path string) string {
	relative, _ := filepath.Rel(filepath.Clean(root), filepath.Clean(path))
	return filepath.ToSlash(relative)
}

func assertRuntimeStorageAllowlistExamples(t *testing.T, allowlist runtimeStorageJSONAllowlist) {
	t.Helper()
	fixture := allowlist.fixture
	digest := strings.Repeat("a", 64)
	transaction := strings.Repeat("b", 32)
	tests := []struct {
		name    string
		path    string
		allowed bool
	}{
		{"config", fixture.configPath, true},
		{"launcher settings", fixture.launcherPath, true},
		{"PID metadata", fixture.pidPath, true},
		{
			"legacy archive",
			filepath.Join(fixture.workspace, "state", "legacy-json", "component-v1", "record.jsonl"),
			true,
		},
		{
			"workflow publish journal",
			filepath.Join(fixture.workspace, "workflow_state", "publish-transaction.json"),
			true,
		},
		{
			"workflow template journal",
			filepath.Join(fixture.workspace, "workflow_state", "template-transaction.json"),
			true,
		},
		{
			"explicit workflow artifact",
			filepath.Join(fixture.workspace, "workflow_artifacts", "namespace", "run", "report.json"),
			true,
		},
		{
			"workspace skill origin",
			filepath.Join(fixture.workspace, "skills", "review", ".skill-origin.json"),
			true,
		},
		{
			"global skill origin",
			filepath.Join(fixture.home, "skills", "review", ".skill-origin.json"),
			true,
		},
		{
			"active apply patch journal",
			filepath.Join(
				fixture.home, "apply_patch_transactions", "workspaces", digest,
				"active-"+transaction, "journal.json",
			),
			true,
		},
		{
			"committed apply patch journal",
			filepath.Join(
				fixture.home, "apply_patch_transactions", "workspaces", digest,
				"committed-"+transaction, "journal.json",
			),
			true,
		},
		{
			"immutable plan",
			filepath.Join(fixture.evidenceRoot, "plans", digest[:2], digest+".json"),
			true,
		},
		{"active auth JSON", filepath.Join(fixture.home, "auth.json"), false},
		{"active model catalog JSON", filepath.Join(fixture.home, "model_catalogs.json"), false},
		{"active runtime JSON", filepath.Join(fixture.workspace, "state.json"), false},
		{"session JSONL", filepath.Join(fixture.workspace, "sessions", "session.jsonl"), false},
		{"cron JSON", filepath.Join(fixture.workspace, "cron", "jobs.json"), false},
		{
			"review JSON",
			filepath.Join(fixture.workspace, "repository_reviews", "repo_record.json"),
			false,
		},
		{
			"evaluation JSON",
			filepath.Join(fixture.workspace, "repository_evaluations", "evaluation_record.json"),
			false,
		},
		{"other workflow state", filepath.Join(fixture.workspace, "workflow_state", "other.json"), false},
		{
			"mutable evidence cache",
			filepath.Join(fixture.evidenceRoot, "cache", digest[:2], digest+".json"),
			false,
		},
		{
			"uppercase evidence digest",
			filepath.Join(fixture.evidenceRoot, "plans", "AA", strings.Repeat("A", 64)+".json"),
			false,
		},
		{
			"wrong evidence prefix",
			filepath.Join(fixture.evidenceRoot, "plans", "bb", digest+".json"),
			false,
		},
		{"origin outside skills", filepath.Join(fixture.workspace, ".skill-origin.json"), false},
		{"nested config impostor", filepath.Join(fixture.workspace, "config.json"), false},
		{
			"malformed apply patch digest",
			filepath.Join(
				fixture.home, "apply_patch_transactions", "workspaces", "short",
				"active-"+transaction, "journal.json",
			),
			false,
		},
		{
			"malformed apply patch directory",
			filepath.Join(
				fixture.home, "apply_patch_transactions", "workspaces", digest,
				"pending-"+transaction, "journal.json",
			),
			false,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := allowlist.allows(test.path); got != test.allowed {
				t.Fatalf("allowlist(%s) = %v, want %v", test.path, got, test.allowed)
			}
		})
	}
}

func scanRuntimeStorage(t *testing.T, allowlist runtimeStorageJSONAllowlist) runtimeStorageScan {
	t.Helper()
	scan, err := scanRuntimeStorageTree(allowlist)
	if err != nil {
		t.Fatal(err)
	}
	return scan
}

func scanRuntimeStorageTree(allowlist runtimeStorageJSONAllowlist) (runtimeStorageScan, error) {
	var scan runtimeStorageScan
	issues := make([]string, 0)
	err := filepath.WalkDir(allowlist.fixture.root, func(
		path string,
		entry fs.DirEntry,
		walkErr error,
	) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		isPID := filepath.Clean(path) == filepath.Clean(allowlist.fixture.pidPath)
		isJSON := runtimeStorageJSONExtension(path) || isPID
		isDatabase := strings.EqualFold(filepath.Ext(path), ".db")
		if !isJSON && !isDatabase {
			return nil
		}
		relative := runtimeStorageSlashRelative(allowlist.fixture.root, path)
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if entry.Type()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
			issues = append(issues, relative+": unsafe file type")
			return nil
		}
		if isDatabase {
			if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
				issues = append(issues, fmt.Sprintf("%s: database mode %04o", relative, info.Mode().Perm()))
			}
			scan.databases = append(scan.databases, relative)
		}
		if isJSON {
			if !allowlist.allows(path) {
				issues = append(issues, relative+": JSON path is not allowlisted")
			} else {
				scan.jsonFiles = append(scan.jsonFiles, relative)
			}
		}
		return nil
	})
	if err != nil {
		return runtimeStorageScan{}, fmt.Errorf("walk runtime storage: %w", err)
	}
	sort.Strings(scan.databases)
	sort.Strings(scan.jsonFiles)
	sort.Strings(issues)
	if len(issues) != 0 {
		return runtimeStorageScan{}, fmt.Errorf(
			"runtime storage scan rejected: %s",
			strings.Join(issues, "; "),
		)
	}
	return scan, nil
}

func assertRuntimeStorageDatabases(
	t *testing.T,
	fixture *runtimeStorageIntegrationFixture,
	got []string,
) {
	t.Helper()
	wantPaths := []string{
		filepath.Join(fixture.home, "auth.db"),
		filepath.Join(fixture.home, "launcher-auth.db"),
		filepath.Join(fixture.home, "model-catalogs.db"),
		filepath.Join(fixture.home, "tool-adaptation.db"),
		filepath.Join(fixture.home, "channels", "wecom", "reqid-store.db"),
		filepath.Join(fixture.home, "channels", "weixin", "state.db"),
		filepath.Join(fixture.workspace, "sessions", "sessions.db"),
		filepath.Join(fixture.workspace, "cron", "jobs.db"),
		filepath.Join(fixture.workspace, "state", "runtime.db"),
		filepath.Join(fixture.workspace, "state", "account-router.db"),
		filepath.Join(fixture.workspace, "state", "workflows.db"),
		filepath.Join(fixture.evolutionRoot, "evolution.db"),
		filepath.Join(fixture.evidenceRoot, "cache.db"),
	}
	want := make([]string, 0, len(wantPaths))
	for _, path := range wantPaths {
		want = append(want, runtimeStorageSlashRelative(fixture.root, path))
	}
	sort.Strings(want)
	if !slices.Equal(got, want) {
		t.Fatalf("database inventory = %v, want %v", got, want)
	}
}

func assertRuntimeStorageJSONFiles(
	t *testing.T,
	fixture *runtimeStorageIntegrationFixture,
	got []string,
) {
	t.Helper()
	want := []string{
		runtimeStorageSlashRelative(fixture.root, fixture.configPath),
		runtimeStorageSlashRelative(fixture.root, fixture.launcherPath),
		runtimeStorageSlashRelative(fixture.root, fixture.pidPath),
		runtimeStorageSlashRelative(fixture.root, filepath.Join(
			fixture.evidenceRoot, "plans", fixture.planDigest[:2], fixture.planDigest+".json",
		)),
		runtimeStorageSlashRelative(fixture.root, filepath.Join(
			fixture.evidenceRoot, "executions", fixture.executionID[:2], fixture.executionID+".json",
		)),
	}
	sort.Strings(want)
	if !slices.Equal(got, want) {
		t.Fatalf("intentional JSON inventory = %v, want %v", got, want)
	}
}
