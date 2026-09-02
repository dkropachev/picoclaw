//go:build integration

package gateway

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/sipeed/picoclaw/pkg/accountrouter"
	"github.com/sipeed/picoclaw/pkg/auth"
	"github.com/sipeed/picoclaw/pkg/cron"
	"github.com/sipeed/picoclaw/pkg/evolution"
	"github.com/sipeed/picoclaw/pkg/gitworkspace"
	"github.com/sipeed/picoclaw/pkg/memory"
	"github.com/sipeed/picoclaw/pkg/providers"
	"github.com/sipeed/picoclaw/pkg/prworkspace/localci"
	"github.com/sipeed/picoclaw/pkg/repoaudit"
	"github.com/sipeed/picoclaw/pkg/repoeval"
	"github.com/sipeed/picoclaw/pkg/sqlitestore"
	"github.com/sipeed/picoclaw/pkg/state"
	"github.com/sipeed/picoclaw/pkg/tools"
	"github.com/sipeed/picoclaw/pkg/workflows"
	"github.com/sipeed/picoclaw/web/backend/api"
	"github.com/sipeed/picoclaw/web/backend/dashboardauth"
	"github.com/sipeed/picoclaw/web/backend/launcherconfig"
)

const legacyMigrationSecretCanary = "legacy-invalid-secret-canary-do-not-store"

type runtimeLegacySource struct {
	source  string
	archive string
	data    []byte
	mode    os.FileMode
}

type runtimeLegacyMigrationFixture struct {
	*runtimeStorageIntegrationFixture
	sources                  []runtimeLegacySource
	launcherOriginal         []byte
	launcherPassword         string
	legacySessionCreated     time.Time
	legacyReviewState        repoaudit.RepositoryState
	legacyReviewProfile      repoaudit.RepositoryReviewProfile
	legacyReviewAutomation   repoaudit.RepositoryReviewAutomation
	legacyEvaluation         repoeval.Evaluation
	legacyEvolutionTask      evolution.LearningRecord
	legacyEvolutionPattern   evolution.LearningRecord
	legacyEvolutionDraft     evolution.SkillDraft
	legacyEvolutionProfile   evolution.SkillProfile
	legacyGitRepositoryID    string
	legacyCheckpoint         prWorkspaceCandidateCheckpoint
	legacyLocalCIResultKey   string
	legacyLocalCIExecutionID string
	expectedIssueDigests     map[string][]runtimeLegacyImportIssue
}

// TestIntegrationRuntimeOwnedJSONLegacyMigration is the aggregate upgrade
// fixture. It places every supported mutable legacy store below one disposable
// PicoClaw home/workspace, opens only the public SQLite owners, validates
// representative typed rows and relationships, verifies safe skip accounting
// and exact retained bytes, then repeats startup without duplicate imports.
func TestIntegrationRuntimeOwnedJSONLegacyMigration(t *testing.T) {
	if os.Getenv("PICOCLAW_STORAGE_JSON_ALLOWLIST_SUITE") == "" {
		t.Skip("runtime storage integration suite is not enabled")
	}
	fixture := newRuntimeLegacyMigrationFixture(t)
	seedRuntimeLegacyMigrationFixture(t, fixture)
	exerciseRuntimeLegacyMigrationFirstStartup(t, fixture)
	assertRuntimeLegacyMigrationRows(t, fixture)
	assertRuntimeLegacyMigrationArchives(t, fixture)
	assertRuntimeLegacyMigrationAuditSafety(t, fixture)
	before := runtimeLegacyMigrationInventory(t, fixture)
	archiveIdentities := runtimeLegacyArchiveIdentities(t, fixture)
	exerciseRuntimeLegacyMigrationSecondStartup(t, fixture)
	assertRuntimeLegacyMigrationRows(t, fixture)
	assertRuntimeLegacyMigrationArchives(t, fixture)
	assertRuntimeLegacyArchiveIdentities(t, archiveIdentities)
	after := runtimeLegacyMigrationInventory(t, fixture)
	if !reflect.DeepEqual(before, after) {
		t.Fatalf("legacy migration inventory changed on reopen:\nbefore=%#v\nafter=%#v", before, after)
	}

	t.Run("shared transaction and archive crash composition", func(t *testing.T) {
		testRuntimeLegacyCrashBeforeCommit(t)
		testRuntimeLegacyInterruptedArchiveRecovery(t)
		testRuntimeLegacyLateSourcesStayNonAuthoritative(t)
	})
}

func newRuntimeLegacyMigrationFixture(t *testing.T) *runtimeLegacyMigrationFixture {
	t.Helper()
	base := newRuntimeStorageIntegrationFixture(t)
	return &runtimeLegacyMigrationFixture{
		runtimeStorageIntegrationFixture: base,
		launcherPassword:                 "legacy-launcher-token-password",
		legacySessionCreated: time.Date(
			2025, time.March, 4, 5, 6, 7, 123456789, time.UTC,
		),
	}
}

func seedRuntimeLegacyMigrationFixture(t *testing.T, fixture *runtimeLegacyMigrationFixture) {
	t.Helper()
	seedLegacyLauncherAuth(t, fixture)
	seedLegacyAuth(t, fixture)
	seedLegacyModelCatalog(t, fixture)
	seedLegacyToolAdaptation(t, fixture)
	seedLegacyChannels(t, fixture)
	seedLegacySessions(t, fixture)
	seedLegacyCron(t, fixture)
	seedLegacyRuntimeState(t, fixture)
	seedLegacyAccountRouter(t, fixture)
	seedLegacyWorkflows(t, fixture)
	seedLegacyRepositoryReview(t, fixture)
	seedLegacyRepositoryEvaluation(t, fixture)
	seedLegacyEvolution(t, fixture)
	seedLegacyGitInventory(t, fixture)
	seedLegacyCheckpoint(t, fixture)
	seedLegacyLocalCI(t, fixture)
}

func (fixture *runtimeLegacyMigrationFixture) writeLegacy(
	t *testing.T,
	source,
	archive string,
	data []byte,
	mode os.FileMode,
) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(source), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Dir(source), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(source, data, mode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(source, mode); err != nil {
		t.Fatal(err)
	}
	fixture.sources = append(fixture.sources, runtimeLegacySource{
		source: source, archive: archive, data: append([]byte(nil), data...), mode: mode,
	})
}

func (fixture *runtimeLegacyMigrationFixture) expectIssue(
	component,
	relative,
	code string,
	raw []byte,
) {
	if fixture.expectedIssueDigests == nil {
		fixture.expectedIssueDigests = make(map[string][]runtimeLegacyImportIssue)
	}
	fixture.expectedIssueDigests[component] = append(
		fixture.expectedIssueDigests[component],
		runtimeLegacyImportIssue{
			relative: filepath.ToSlash(relative), code: code, digest: runtimeLegacyDigestHex(raw),
		},
	)
}

func seedLegacyLauncherAuth(t *testing.T, fixture *runtimeLegacyMigrationFixture) {
	t.Helper()
	body := []byte(fmt.Sprintf(`{
  "port": 18800,
  "public": false,
  "dashboard_password_hash": "not-a-bcrypt-hash-%s",
  "launcher_token": %q
}
`, legacyMigrationSecretCanary, fixture.launcherPassword))
	fixture.launcherOriginal = append([]byte(nil), body...)
	if err := os.WriteFile(fixture.launcherPath, body, 0o640); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(fixture.launcherPath, 0o640); err != nil {
		t.Fatal(err)
	}
}

func seedLegacyAuth(t *testing.T, fixture *runtimeLegacyMigrationFixture) {
	t.Helper()
	data := []byte(`{"credentials":{
  "openai:legacy":{"access_token":"auth-valid-secret","provider":"openai","auth_method":"token","expires_at":"2026-08-31T10:00:00.123456789Z"},
  "openai:bad/name":{"access_token":"` + legacyMigrationSecretCanary + `","provider":"openai"},
  "anthropic:invalid":"not-an-object"
}}`)
	fixture.expectIssue("auth", "auth.json", "invalid-credential",
		runtimeLegacyObjectValue(t, data, "credentials", "anthropic:invalid"))
	fixture.expectIssue("auth", "auth.json", "invalid-identity",
		runtimeLegacyObjectValue(t, data, "credentials", "openai:bad/name"))
	fixture.writeLegacy(t,
		filepath.Join(fixture.home, "auth.json"),
		filepath.Join(fixture.home, "legacy-json", "auth-v1", "auth.json"),
		data, 0o600,
	)
}

func seedLegacyModelCatalog(t *testing.T, fixture *runtimeLegacyMigrationFixture) {
	t.Helper()
	data := []byte(`{"entries":{
  "legacy-catalog":{"id":"legacy-catalog","provider":"openai","api_base":"https://legacy.example.invalid/v1","api_key_mask":"****","models":[{"id":"model-b","owned_by":"fixture","extra":{"exact":9007199254740993}},{"id":"model-a"}],"fetched_at":"2025-03-04T05:06:07.123456789Z"},
  "invalid-catalog":{"id":"invalid-catalog","provider":"openai","models":[{"id":""}],"fetched_at":"2025-03-04T05:06:07Z"}
}}`)
	fixture.expectIssue("model-catalogs", "model_catalogs.json", "invalid-catalog",
		runtimeLegacyObjectValue(t, data, "entries", "invalid-catalog"))
	fixture.writeLegacy(t,
		filepath.Join(fixture.home, "model_catalogs.json"),
		filepath.Join(fixture.home, "legacy-json", "model-catalogs-v1", "model_catalogs.json"),
		data, 0o640,
	)
}

func seedLegacyToolAdaptation(t *testing.T, fixture *runtimeLegacyMigrationFixture) {
	t.Helper()
	data := []byte(`{
  "version":1,
  "observations":{
    "valid":{"profile":{"provider":"github-copilot","model":"gpt-legacy"},"visible_tool_surface":"codex","tool_schema_hash":"schema-legacy","prompt_tokens":1000,"cached_tokens":500,"cache_hit_ratio":0.5,"cache_sensitive":true,"sniffed":true,"observed_at":"2026-01-02T04:04:05.000000006Z"},
    "invalid":{"profile":{},"tool_schema_hash":"` + legacyMigrationSecretCanary + `","observed_at":"2026-01-02T04:04:05Z"}
  },
  "outcomes":{
    "valid":{"profile":{"provider":"github-copilot","model":"gpt-legacy"},"visible_tool_surface":"codex","tool_name":"exec_command","successes":3,"failures":4,"last_error":"bounded legacy error","last_duration_ms":20,"updated_at":"2026-01-02T04:04:05.000000006Z"},
    "invalid":{"profile":{"provider":"openai","model":"gpt"},"tool_name":"","last_error":"` + legacyMigrationSecretCanary + `","updated_at":"2026-01-02T04:04:05Z"}
  }
}`)
	fixture.expectIssue("tool-adaptation", "tool_adaptation_state.json", "invalid-observation",
		runtimeLegacyObjectValue(t, data, "observations", "invalid"))
	fixture.expectIssue("tool-adaptation", "tool_adaptation_state.json", "invalid-outcome",
		runtimeLegacyObjectValue(t, data, "outcomes", "invalid"))
	fixture.writeLegacy(t,
		filepath.Join(fixture.home, "tool_adaptation_state.json"),
		filepath.Join(fixture.home, "legacy-json", "tool-adaptation-v1", "tool_adaptation_state.json"),
		data, 0o600,
	)
}

func seedLegacyChannels(t *testing.T, fixture *runtimeLegacyMigrationFixture) {
	t.Helper()
	future := "2500-08-31T12:00:00.000000123Z"
	wecom := []byte(`{
  "legacy-chat":{"req_id":"legacy-request","chat_id":"legacy-chat","chat_type":7,"expires_at":"` + future + `"},
  "invalid-chat":{"req_id":"","chat_id":"invalid-chat","chat_type":1,"private":"` + legacyMigrationSecretCanary + `"}
}`)
	fixture.expectIssue("wecom-reqid", "wecom/reqid-store.json", "invalid-route",
		runtimeLegacyObjectValue(t, wecom, "invalid-chat"))
	fixture.writeLegacy(t,
		filepath.Join(fixture.home, "wecom", "reqid-store.json"),
		filepath.Join(fixture.home, "legacy-json", "wecom-reqid-v1", "wecom", "reqid-store.json"),
		wecom, 0o600,
	)
	account := "0123456789abcdef"
	weixinRoot := filepath.Join(fixture.home, "channels", "weixin")
	cursor := []byte(`{"get_updates_buf":"legacy-cursor"}`)
	tokens := []byte(`{"tokens":{"user-a":"token-a","":"` + legacyMigrationSecretCanary + `","user-b":4}}`)
	fixture.expectIssue("weixin-state", "context-tokens/"+account+".json", "invalid-token",
		runtimeLegacyObjectValue(t, tokens, "tokens", ""))
	fixture.expectIssue("weixin-state", "context-tokens/"+account+".json", "invalid-token",
		runtimeLegacyObjectValue(t, tokens, "tokens", "user-b"))
	fixture.writeLegacy(t,
		filepath.Join(weixinRoot, "sync", account+".json"),
		filepath.Join(weixinRoot, "legacy-json", "weixin-state-v1", "sync", account+".json"),
		cursor, 0o600,
	)
	fixture.writeLegacy(t,
		filepath.Join(weixinRoot, "context-tokens", account+".json"),
		filepath.Join(weixinRoot, "legacy-json", "weixin-state-v1", "context-tokens", account+".json"),
		tokens, 0o640,
	)
}

func seedLegacySessions(t *testing.T, fixture *runtimeLegacyMigrationFixture) {
	t.Helper()
	scope := json.RawMessage(
		`{"version":1,"agent_id":"main","channel":"pico","account":"default","dimensions":["chat"],"values":{"chat":"pico:legacy"}}`,
	)
	meta := memory.SessionMeta{
		Key: "legacy-session", Summary: "legacy summary", Count: 3,
		CreatedAt: fixture.legacySessionCreated, UpdatedAt: fixture.legacySessionCreated,
		Scope: scope, Aliases: []string{"legacy-alias"}, HistorySlot: "a",
		ThreadID: "thread-from-meta", ThreadTitle: "Legacy thread", ThreadType: "coding",
		ThreadAttachedAt: fixture.legacySessionCreated,
	}
	metaData := runtimeLegacyJSON(t, meta)
	history := []byte("{\"role\":\"user\",\"content\":\"first\"}\n" +
		"{\"role\":\"assistant\",\"content\":\"second\"}\n" +
		"{\"role\":\"user\",\"content\":\"" + legacyMigrationSecretCanary + "\"\n")
	other := runtimeLegacyJSON(t, map[string]any{
		"key": "other", "messages": []map[string]any{{"role": "user", "content": "other history"}},
		"summary": "other", "created": fixture.legacySessionCreated,
		"updated": fixture.legacySessionCreated,
	})
	thread := runtimeLegacyJSON(t, map[string]any{
		"id": "thread-two", "ui_session_id": "ui-two", "primary_session_key": "other",
		"agent_id": "main", "owner_identity": "integration", "title": "Other thread",
		"type": "general", "context": map[string]string{"a": "first", "z": "last"},
		"session_keys": []string{"other"}, "registration": "migrated",
		"created_at": fixture.legacySessionCreated, "updated_at": fixture.legacySessionCreated,
	})
	handoff := runtimeLegacyJSON(t, map[string]any{
		"id": "handoff-one", "origin_session_key": "legacy-session",
		"target_thread_id": "thread-two", "target_session_id": "ui-two",
		"agent_id": "main", "summary": "continue", "created_at": fixture.legacySessionCreated,
	})
	root := fixture.workspace
	archiveRoot := filepath.Join(root, "legacy-json", "sessions-v1")
	brokenSession := []byte(`{"key":"` + legacyMigrationSecretCanary)
	invalidHistoryLine := bytes.Split(history, []byte{'\n'})[2]
	fixture.expectIssue("sessions", "sessions/broken.json", "invalid-session-json", brokenSession)
	fixture.expectIssue("sessions", "sessions/legacy.history-a", "invalid-message-json", invalidHistoryLine)
	for _, source := range []struct {
		relative string
		data     []byte
	}{
		{"sessions/legacy.meta.json", metaData},
		{"sessions/legacy.history-a", history},
		{"sessions/other.json", other},
		{"sessions/broken.json", brokenSession},
		{"threads/thread-two.json", thread},
		{"threads/handoffs/handoff-one.json", handoff},
	} {
		fixture.writeLegacy(t, filepath.Join(root, filepath.FromSlash(source.relative)),
			filepath.Join(archiveRoot, filepath.FromSlash(source.relative)), source.data, 0o600)
	}
}

func seedLegacyCron(t *testing.T, fixture *runtimeLegacyMigrationFixture) {
	t.Helper()
	data := []byte(`{"version":1,"jobs":[
  {"id":"legacy-first","name":"first valid","enabled":true,"schedule":{"kind":"every","everyMs":60000},"payload":{"kind":"agent_turn","message":"one"},"state":{"nextRunAtMs":1900000000000,"lastRunAtMs":1800000000000,"lastStatus":"ok","lastError":"legacy bounded error"},"createdAtMs":1001,"updatedAtMs":1002,"deleteAfterRun":false},
  {"id":"","name":"` + legacyMigrationSecretCanary + `","enabled":true,"schedule":{"kind":"every","everyMs":60000},"payload":{"kind":"agent_turn"},"state":{},"createdAtMs":1,"updatedAtMs":2,"deleteAfterRun":false},
  {"id":"legacy-second","name":"second valid","enabled":false,"schedule":{"kind":"at","atMs":2000000000000},"payload":{"kind":"agent_turn","message":"two"},"state":{},"createdAtMs":1003,"updatedAtMs":1004,"deleteAfterRun":true}
]}`)
	fixture.expectIssue("cron-jobs", "jobs.json", "invalid-job",
		runtimeLegacyArrayValue(t, data, "jobs", 1))
	root := filepath.Join(fixture.workspace, "cron")
	fixture.writeLegacy(t, filepath.Join(root, "jobs.json"),
		filepath.Join(root, "legacy-json", "cron-jobs-v1", "jobs.json"), data, 0o600)
}

func seedLegacyRuntimeState(t *testing.T, fixture *runtimeLegacyMigrationFixture) {
	t.Helper()
	old := runtimeLegacyJSON(t, state.State{
		LastChannel: "legacy-old:user", LastChatID: "old-chat",
		Timestamp: time.Date(2026, 8, 30, 10, 0, 0, 0, time.UTC),
	})
	latest := runtimeLegacyJSON(t, state.State{
		LastChannel: "legacy-new:user", LastChatID: "new-chat",
		Timestamp: time.Date(2026, 8, 31, 10, 0, 0, 123, time.UTC),
	})
	fixture.expectIssue("runtime-state", "state/state.json", "source-conflict", latest)
	archiveRoot := filepath.Join(fixture.workspace, "state", "legacy-json", "runtime-state-v1")
	fixture.writeLegacy(t, filepath.Join(fixture.workspace, "state.json"),
		filepath.Join(archiveRoot, "state.json"), old, 0o600)
	fixture.writeLegacy(t, filepath.Join(fixture.workspace, "state", "state.json"),
		filepath.Join(archiveRoot, "state", "state.json"), latest, 0o640)
}

func seedLegacyAccountRouter(t *testing.T, fixture *runtimeLegacyMigrationFixture) {
	t.Helper()
	now := time.Date(2026, 8, 31, 12, 0, 0, 123, time.UTC)
	valid := accountrouter.RouterState{
		ConfigHash: "0123456789abcdef",
		Accounts: map[string]*accountrouter.AccountState{
			"credential:openai:work": {
				State: "unavailable", Reason: providers.FailoverAuth, FailureCount: 2,
				Requests: 3, LastFailureAt: now, UnavailableUntil: now.Add(time.Hour),
				LastError: "authentication failed",
			},
		},
		Sessions: map[string]*accountrouter.SessionState{
			"legacy-router-session": {
				ConfigHash: "0123456789abcdef", UpdatedAt: now,
				Blocks: map[string]accountrouter.BlockAffinity{
					"pool": {
						Account: "credential:openai:work", Reason: accountrouter.SelectReasonInitial,
						SelectedAt: now,
					},
				},
			},
		},
		Blocks:    map[string]*accountrouter.BlockRunState{"pool": {Cursor: 3, UpdatedAt: now}},
		UpdatedAt: now,
	}
	validRaw := runtimeLegacyJSON(t, valid)
	data := []byte(fmt.Sprintf(
		`{"version":1,"routers":{"legacy-router":%s,"bad":null}}`, validRaw,
	))
	fixture.expectIssue("account-router", "account_router_state.json", "invalid-router",
		runtimeLegacyObjectValue(t, data, "routers", "bad"))
	source := filepath.Join(fixture.workspace, "account_router_state.json")
	archive := filepath.Join(
		fixture.workspace, "state", "legacy-json", "account-router-v1", "account_router_state.json",
	)
	fixture.writeLegacy(t, source, archive, data, 0o600)
}

func seedLegacyWorkflows(t *testing.T, fixture *runtimeLegacyMigrationFixture) {
	t.Helper()
	created := time.Date(2025, 4, 5, 6, 7, 8, 9, time.UTC)
	run := map[string]any{
		"id": "wr_legacy_fixture", "workflow_ref": "workflows/legacy.yml", "status": "succeeded",
		"event":      map[string]any{"large": json.Number("9007199254740993")},
		"created_at": created, "updated_at": created.Add(time.Hour),
	}
	runData := runtimeLegacyJSON(t, run)
	eventData := append(runtimeLegacyJSON(t, map[string]any{
		"time": created, "kind": "legacy.first", "run_id": "wr_legacy_fixture",
		"payload": map[string]any{"huge": json.Number("1e400")},
	}), '\n')
	secondEventTime := created.Add(987654321 * time.Nanosecond)
	eventData = append(eventData, runtimeLegacyJSON(t, map[string]any{
		"time": secondEventTime, "kind": "legacy.second", "run_id": "wr_legacy_fixture",
		"payload": map[string]any{"order": json.Number("2")},
	})...)
	eventData = append(eventData, '\n')
	invalidEventLine := []byte(`{"private":"` + legacyMigrationSecretCanary + `"`)
	eventData = append(eventData, invalidEventLine...)
	eventData = append(eventData, '\n')
	fixture.expectIssue(
		"workflows", "workflow_runs/wr_legacy_fixture/events.jsonl", "invalid_event_line",
		invalidEventLine,
	)
	namespace, key := "legacy-space", "legacy-key"
	nativeData := runtimeLegacyJSON(t, map[string]any{
		"key": key, "value": map[string]any{"n": json.Number("42")},
		"updated_at": created.Add(time.Hour),
	})
	nativeRelative := filepath.Join(
		"workflow_state",
		runtimeLegacySafeStorageSegment(namespace),
		runtimeLegacySafeStorageSegment(key)+".json",
	)
	development := runtimeLegacyJSON(t, map[string]any{
		"id": "dev_legacy_fixture", "session_revision": "session", "draft_revision": "draft",
		"base_target_revision": "missing", "reason": "new", "status": "editing",
		"target_workflow_ref": "workflows/draft.yml",
		"yaml":                "name: Draft\non:\n  manual: {}\njobs: {}\n",
		"created_at":          created, "updated_at": created.Add(time.Hour),
	})
	sources := []struct {
		relative string
		data     []byte
	}{
		{filepath.Join("workflow_runs", "wr_legacy_fixture", "run.json"), runData},
		{filepath.Join("workflow_runs", "wr_legacy_fixture", "events.jsonl"), eventData},
		{nativeRelative, nativeData},
		{filepath.Join("workflow_dev", "active.json"), development},
	}
	for _, item := range sources {
		fixture.writeLegacy(t, filepath.Join(fixture.workspace, item.relative),
			filepath.Join(fixture.workspace, "legacy-json", "workflows-v1", item.relative),
			item.data, 0o600)
	}
}

func runtimeLegacySafeStorageSegment(value string) string {
	clean := strings.Trim(strings.Map(func(r rune) rune {
		if r >= 'A' && r <= 'Z' || r >= 'a' && r <= 'z' || r >= '0' && r <= '9' ||
			r == '.' || r == '_' || r == '-' {
			return r
		}
		return '-'
	}, strings.TrimSpace(value)), ".-_")
	if clean == "" {
		clean = "value"
	}
	digest := sha256.Sum256([]byte(strings.TrimSpace(value)))
	return clean + "-" + hex.EncodeToString(digest[:])[:12]
}

func seedLegacyRepositoryReview(t *testing.T, fixture *runtimeLegacyMigrationFixture) {
	t.Helper()
	seedWorkspace := t.TempDir()
	store := repoaudit.NewSQLiteStore(seedWorkspace)
	profile, err := store.CreateProfile(t.Context(), repoaudit.RepositoryReviewProfile{
		ID: "rrpf_legacy_fixture", Name: "Legacy fixture", ReviewFocus: "Find concrete bugs.",
		ScopePolicy: repoaudit.RepositoryReviewScopePolicy{
			CodeTypes: []repoaudit.RepositoryReviewCodeType{repoaudit.RepositoryReviewCodeTypeCode},
		},
		ReviewerModel: "review-a", AutoContinue: true, MaxFilesPerRun: 12,
		MaxContentBytes: 64 << 10, MaxParallelChildren: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	profile.ScopePolicy.IncludeFolders = []string{}
	profile.ScopePolicy.ExcludeFolders = []string{}
	automationInput, err := repoaudit.MaterializeRepositoryReviewAutomation(profile,
		repoaudit.RepositoryReviewAutomation{
			ID: "rra_legacy_fixture", Name: "Legacy automation", Repository: "owner/legacy",
			Ref: "main", Target: "all", ReviewerModels: []string{"review-a"},
		})
	if err != nil {
		t.Fatal(err)
	}
	automation, err := store.CreateAutomation(t.Context(), automationInput)
	if err != nil {
		t.Fatal(err)
	}
	reviewID := writeRepositoryReviewSentinel(t, seedWorkspace)
	review, found, err := store.GetByID(reviewID)
	if err != nil || !found {
		t.Fatalf("seed review found=%v err=%v", found, err)
	}
	fixture.legacyReviewState = review
	fixture.legacyReviewProfile = profile
	fixture.legacyReviewAutomation = automation
	root := filepath.Join(fixture.workspace, "repository_reviews")
	stateName := "repo_" + strings.TrimPrefix(review.ID, "rrp_") + ".json"
	sources := map[string][]byte{
		stateName: runtimeLegacyJSON(t, review),
		strings.TrimSuffix(stateName, ".json") + ".summary.json": runtimeLegacyJSON(t, repoaudit.Summarize(review)),
		"profile_" + profile.ID + ".json":                        runtimeLegacyJSON(t, profile),
		"automation_" + automation.ID + ".json":                  runtimeLegacyJSON(t, automation),
		"profile_rrpf_malformed.json":                            []byte(`{"secret":"` + legacyMigrationSecretCanary),
	}
	for name, data := range sources {
		if name == "profile_rrpf_malformed.json" {
			fixture.expectIssue("repository-reviews", name, "invalid_profile", data)
		}
		fixture.writeLegacy(t, filepath.Join(root, name),
			filepath.Join(root, "legacy-json", "repository-reviews-v1", name), data, 0o600)
	}
}

func seedLegacyRepositoryEvaluation(t *testing.T, fixture *runtimeLegacyMigrationFixture) {
	t.Helper()
	seed := repoeval.NewSQLiteStore(t.TempDir())
	evaluation, err := seed.Create(t.Context(), repoeval.CreateRequest{
		Repository: "owner/legacy", Ref: "main", CandidateModels: []string{"model-a", "model-b"},
		SelectorModelAlias: "selector", JudgeModelAlias: "judge",
		Focus: repoeval.Focus{
			CodeTypes: []repoeval.CodeType{repoeval.CodeTypeCode}, IncludeFolders: []string{"pkg"},
		},
		FilesPerLanguage: map[string]int{"Go": 2},
	})
	if err != nil {
		t.Fatal(err)
	}
	fixture.legacyEvaluation = evaluation
	root := filepath.Join(fixture.workspace, "repository_evaluations")
	name := "evaluation_" + evaluation.ID + ".json"
	fixture.writeLegacy(t, filepath.Join(root, name),
		filepath.Join(root, "legacy-json", "repository-evaluations-v1", name),
		runtimeLegacyJSON(t, evaluation), 0o600)
	badName := "evaluation_rme_ffffffffffffffffffffffffffffffff.json"
	badData := []byte(`{"secret":"` + legacyMigrationSecretCanary)
	fixture.expectIssue("repository-evaluations", badName, "malformed_json", badData)
	fixture.writeLegacy(t, filepath.Join(root, badName),
		filepath.Join(root, "legacy-json", "repository-evaluations-v1", badName),
		badData, 0o600)
}

func seedLegacyEvolution(t *testing.T, fixture *runtimeLegacyMigrationFixture) {
	t.Helper()
	workspaceID := fixture.workspace
	task := evolution.LearningRecord{
		ID: "task-legacy", Kind: evolution.RecordKindTask, WorkspaceID: workspaceID,
		CreatedAt: time.Unix(1700000000, 123).UTC(), Summary: "legacy task", Status: "new",
	}
	pattern := evolution.LearningRecord{
		ID: "pattern-legacy", Kind: evolution.RecordKindPattern, WorkspaceID: workspaceID,
		CreatedAt: time.Unix(1700000001, 456).UTC(), Summary: "legacy pattern", Status: "ready",
	}
	draft := evolution.SkillDraft{
		ID: "draft-legacy", WorkspaceID: workspaceID, CreatedAt: time.Unix(1700000002, 789).UTC(),
		SourceRecordID: pattern.ID, TargetSkillName: "weather", DraftType: evolution.DraftTypeShortcut,
		ChangeKind: evolution.ChangeKindAppend, HumanSummary: "legacy draft", BodyOrPatch: "patch",
		Status: evolution.DraftStatusCandidate,
	}
	profile := evolution.SkillProfile{
		SkillName: "weather", WorkspaceID: workspaceID, Status: evolution.SkillStatusActive,
		Origin: "evolved", HumanSummary: "legacy profile", LastUsedAt: time.Unix(1700000003, 12).UTC(),
	}
	fixture.legacyEvolutionTask = task
	fixture.legacyEvolutionPattern = pattern
	fixture.legacyEvolutionDraft = draft
	fixture.legacyEvolutionProfile = profile
	learning := append(runtimeLegacyJSON(t, task), '\n')
	learning = append(learning, runtimeLegacyJSON(t, pattern)...)
	learning = append(learning, '\n')
	tasks := append(runtimeLegacyJSON(t, task), '\n')
	invalidTaskLine := []byte(`{"secret":"` + legacyMigrationSecretCanary + `"`)
	tasks = append(tasks, invalidTaskLine...)
	tasks = append(tasks, '\n')
	draftsData := runtimeLegacyJSON(t, []evolution.SkillDraft{draft, {}})
	fixture.expectIssue("evolution", "skill-drafts.json", "invalid-draft",
		runtimeLegacyArrayValue(t, draftsData, "", 1))
	fixture.expectIssue("evolution", "task-records.jsonl", "malformed-record", invalidTaskLine)
	root := fixture.evolutionRoot
	sources := map[string][]byte{
		"learning-records.jsonl": learning,
		"task-records.jsonl":     tasks,
		"skill-drafts.json":      draftsData,
		"profiles/weather.json":  runtimeLegacyJSON(t, profile),
	}
	for relative, data := range sources {
		fixture.writeLegacy(t, filepath.Join(root, filepath.FromSlash(relative)),
			filepath.Join(root, "legacy-json", "evolution-v1", filepath.FromSlash(relative)), data, 0o600)
	}
}

func seedLegacyGitInventory(t *testing.T, fixture *runtimeLegacyMigrationFixture) {
	t.Helper()
	remote := "https://example.invalid/owner/legacy.git"
	digest := sha256.Sum256([]byte(strings.ToLower(remote)))
	repositoryID := "gw-" + hex.EncodeToString(digest[:])[:12]
	fixture.legacyGitRepositoryID = repositoryID
	first := time.Date(2026, 8, 31, 12, 34, 56, 987654321, time.UTC)
	data := runtimeLegacyJSON(t, map[string]any{
		"version": "4",
		"repositories": map[string]any{repositoryID: map[string]any{
			"id": repositoryID, "remote_url": remote, "first_seen_at": first,
			"last_seen_at": first.Add(time.Second), "last_work_at": first.Add(2 * time.Second),
		}},
		"workspaces": map[string]any{}, "development_lines": map[string]any{},
		"pinned_reservation_rotations": map[string]any{},
	})
	fixture.writeLegacy(t, filepath.Join(fixture.gitRoot, "inventory.json"),
		filepath.Join(fixture.gitRoot, "legacy-json", "git-workspaces-v1", "inventory.json"),
		data, 0o600)
}

func seedLegacyCheckpoint(t *testing.T, fixture *runtimeLegacyMigrationFixture) {
	t.Helper()
	checkpoint := runtimeStorageCheckpointFixture("devw_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	fixture.legacyCheckpoint = checkpoint
	name := legacyPRWorkspaceCheckpointFilename(checkpoint.WorkspaceID)
	fixture.writeLegacy(t, filepath.Join(fixture.checkpointRoot, name),
		filepath.Join(fixture.checkpointRoot, "legacy-json", prWorkspaceCheckpointArchiveLabel, name),
		runtimeLegacyJSON(t, checkpoint), 0o600)
	malformed := checkpoint
	malformed.WorkspaceID = "devw_88888888888888888888888888888888"
	badName := legacyPRWorkspaceCheckpointFilename(malformed.WorkspaceID)
	badData := []byte(`{"secret":"` + legacyMigrationSecretCanary)
	fixture.expectIssue("pr-workspace-checkpoints", badName, "malformed-checkpoint", badData)
	fixture.writeLegacy(t, filepath.Join(fixture.checkpointRoot, badName),
		filepath.Join(fixture.checkpointRoot, "legacy-json", prWorkspaceCheckpointArchiveLabel, badName),
		badData, 0o600)
}

func seedLegacyLocalCI(t *testing.T, fixture *runtimeLegacyMigrationFixture) {
	t.Helper()
	seedRoot := filepath.Join(t.TempDir(), "seed-evidence")
	planDigest, resultKey, executionID := writeLocalCIPassingEvidence(t, seedRoot)
	fixture.legacyLocalCIResultKey = resultKey
	fixture.legacyLocalCIExecutionID = executionID
	for _, item := range []struct{ kind, digest string }{
		{"plans", planDigest}, {"executions", executionID},
	} {
		source := filepath.Join(seedRoot, item.kind, item.digest[:2], item.digest+".json")
		data, err := os.ReadFile(source)
		if err != nil {
			t.Fatal(err)
		}
		target := filepath.Join(fixture.evidenceRoot, item.kind, item.digest[:2], item.digest+".json")
		if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(target, data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	now := time.Date(2099, time.January, 2, 3, 4, 5, 123456789, time.UTC)
	record := runtimeLegacyCacheIndex{
		Version: localci.EvidenceVersion, ResultKey: resultKey, ExecutionDigest: executionID,
		CreatedAt: now, ExpiresAt: now.Add(24 * time.Hour),
	}
	record.Digest = runtimeStorageDigestJSON(t, "picoclaw-local-ci-cache-index-v1", record)
	relative := filepath.Join("cache", resultKey[:2], resultKey+".json")
	fixture.writeLegacy(t, filepath.Join(fixture.evidenceRoot, relative),
		filepath.Join(fixture.evidenceRoot, "legacy-json", "local-ci-cache-v1", relative),
		append(runtimeLegacyJSON(t, record), '\n'), 0o600)
	badKey := strings.Repeat("a", 64)
	badRelative := filepath.Join("cache", badKey[:2], badKey+".json")
	badData := []byte(`{"secret":"` + legacyMigrationSecretCanary)
	fixture.expectIssue("local_ci_cache", badRelative, "malformed-index", badData)
	fixture.writeLegacy(t, filepath.Join(fixture.evidenceRoot, badRelative),
		filepath.Join(fixture.evidenceRoot, "legacy-json", "local-ci-cache-v1", badRelative),
		badData, 0o600)
}

type runtimeLegacyCacheIndex struct {
	Version         int       `json:"version"`
	ResultKey       string    `json:"result_key"`
	ExecutionDigest string    `json:"execution_digest"`
	CreatedAt       time.Time `json:"created_at"`
	ExpiresAt       time.Time `json:"expires_at"`
	Digest          string    `json:"digest"`
}

func runtimeLegacyJSON(t *testing.T, value any) []byte {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func runtimeLegacyObjectValue(t *testing.T, data []byte, keys ...string) []byte {
	t.Helper()
	current := append([]byte(nil), data...)
	for _, key := range keys {
		var object map[string]json.RawMessage
		if err := json.Unmarshal(current, &object); err != nil {
			t.Fatal(err)
		}
		value, found := object[key]
		if !found {
			t.Fatalf("legacy JSON key %q is missing", key)
		}
		current = append([]byte(nil), value...)
	}
	return current
}

func runtimeLegacyArrayValue(t *testing.T, data []byte, key string, index int) []byte {
	t.Helper()
	arrayData := data
	if key != "" {
		arrayData = runtimeLegacyObjectValue(t, data, key)
	}
	var values []json.RawMessage
	if err := json.Unmarshal(arrayData, &values); err != nil {
		t.Fatal(err)
	}
	if index < 0 || index >= len(values) {
		t.Fatalf("legacy JSON array index %d is out of bounds", index)
	}
	return append([]byte(nil), values[index]...)
}

//nolint:govet // Integration setup keeps independent subsystem errors locally scoped.
func exerciseRuntimeLegacyMigrationFirstStartup(
	t *testing.T,
	fixture *runtimeLegacyMigrationFixture,
) {
	t.Helper()
	ctx := t.Context()

	loadedAuth, err := auth.LoadStore()
	if err != nil {
		t.Fatal(err)
	}
	credential := loadedAuth.Credentials["openai:legacy"]
	if credential == nil || credential.AccessToken != "auth-valid-secret" ||
		credential.Provider != "openai" || credential.AuthMethod != "token" ||
		credential.RefreshToken != "" || credential.TokenType != "" || credential.OAuthTokenURL != "" ||
		credential.OAuthClientID != "" || credential.OAuthClientSecret != "" ||
		credential.OAuthAuthStyle != "" || credential.AccountID != "" || credential.Email != "" ||
		credential.ProjectID != "" ||
		credential.ExpiresAt.Format(time.RFC3339Nano) != "2026-08-31T10:00:00.123456789Z" {
		t.Fatalf("legacy auth credential = %#v", credential)
	}

	dashboard, err := dashboardauth.NewWithLauncherConfig(fixture.home, fixture.launcherPath)
	if err != nil {
		t.Fatal(err)
	}
	if valid, verifyErr := dashboard.VerifyPassword(ctx, fixture.launcherPassword); verifyErr != nil || !valid {
		_ = dashboard.Close()
		t.Fatalf("legacy launcher password valid=%v err=%v", valid, verifyErr)
	}
	if err := dashboard.Close(); err != nil {
		t.Fatal(err)
	}

	// SaveCatalog is the exported owner boundary. Opening imports legacy rows
	// before this unrelated live catalog is transactionally replaced.
	if err := api.SaveCatalog("fixture-live", "https://live.example.invalid/v1", "key", []api.CatalogModel{{
		ID: "live-model",
	}}); err != nil {
		t.Fatal(err)
	}
	profile := tools.ToolAdaptationProfile{Provider: "github-copilot", Model: "gpt-legacy"}
	observation, found := tools.LatestToolAdaptationObservation(profile)
	if !found || observation.ToolSchemaHash != "schema-legacy" ||
		observation.PromptTokens != 1000 || observation.CachedTokens != 500 ||
		observation.VisibleToolSurface != "codex" || observation.CacheHitRatio != 0.5 ||
		!observation.CacheSensitive || !observation.Sniffed ||
		observation.ObservedAt.Format(time.RFC3339Nano) != "2026-01-02T04:04:05.000000006Z" {
		t.Fatalf("legacy tool adaptation observation = %#v found=%v", observation, found)
	}
	if outcomes := tools.LatestToolAdaptationToolOutcomes(profile); len(outcomes) != 1 ||
		outcomes[0].Successes != 3 || outcomes[0].Failures != 4 || outcomes[0].LastDurationMS != 20 {
		t.Fatalf("legacy tool adaptation outcomes = %#v", outcomes)
	}
	outcome := tools.LatestToolAdaptationToolOutcomes(profile)[0]
	if outcome.VisibleToolSurface != "codex" || outcome.ToolName != "exec_command" ||
		outcome.LastError != "bounded legacy error" ||
		outcome.UpdatedAt.Format(time.RFC3339Nano) != "2026-01-02T04:04:05.000000006Z" {
		t.Fatalf("legacy tool adaptation outcome detail = %#v", outcome)
	}

	exerciseChannelStorage(t, ctx)

	sessions, err := memory.NewSQLiteStore(filepath.Join(fixture.workspace, "sessions"))
	if err != nil {
		t.Fatal(err)
	}
	canonical, history, meta, found, err := sessions.ReadSessionSnapshot(ctx, "legacy-alias")
	if err != nil || !found || canonical != "legacy-session" || len(history) != 2 ||
		history[0].Content != "first" || history[1].Content != "second" ||
		meta.Summary != "legacy summary" || !meta.CreatedAt.Equal(fixture.legacySessionCreated) {
		_ = sessions.Close()
		t.Fatalf("legacy session = key:%q history:%#v meta:%#v found=%v err=%v",
			canonical, history, meta, found, err)
	}
	if err := sessions.Close(); err != nil {
		t.Fatal(err)
	}

	cronService, err := cron.NewSQLiteCronService(filepath.Join(fixture.workspace, "cron", "jobs.db"), nil)
	if err != nil {
		t.Fatal(err)
	}
	jobs := cronService.ListJobs(true)
	if len(jobs) != 2 || jobs[0].ID != "legacy-first" || jobs[1].ID != "legacy-second" ||
		!jobs[0].Enabled || jobs[1].Enabled || jobs[0].Schedule.Kind != "every" ||
		jobs[0].Schedule.EveryMS == nil || *jobs[0].Schedule.EveryMS != 60000 ||
		jobs[1].Schedule.Kind != "at" || jobs[1].Schedule.AtMS == nil ||
		*jobs[1].Schedule.AtMS != 2000000000000 || jobs[0].Payload.Kind != "agent_turn" ||
		jobs[0].Payload.Message != "one" || jobs[1].Payload.Message != "two" ||
		jobs[0].State.NextRunAtMS == nil || *jobs[0].State.NextRunAtMS != 1900000000000 ||
		jobs[0].State.LastRunAtMS == nil || *jobs[0].State.LastRunAtMS != 1800000000000 ||
		jobs[0].State.LastStatus != "ok" || jobs[0].State.LastError != "legacy bounded error" ||
		jobs[0].DeleteAfterRun || !jobs[1].DeleteAfterRun || jobs[0].CreatedAtMS != 1001 ||
		jobs[0].UpdatedAtMS != 1002 || jobs[1].CreatedAtMS != 1003 || jobs[1].UpdatedAtMS != 1004 {
		_ = cronService.Close()
		t.Fatalf("legacy cron jobs = %#v", jobs)
	}
	if err := cronService.Close(); err != nil {
		t.Fatal(err)
	}

	runtimeState, err := state.NewSQLiteManager(fixture.workspace)
	if err != nil {
		t.Fatal(err)
	}
	if runtimeState.GetLastChannel() != "legacy-new:user" || runtimeState.GetLastChatID() != "new-chat" {
		t.Fatalf("legacy runtime state = %q/%q", runtimeState.GetLastChannel(), runtimeState.GetLastChatID())
	}

	exerciseAccountRouterStorage(t, fixture.runtimeStorageIntegrationFixture)
	workflowStore, err := workflows.NewSQLiteRunStore(fixture.workspace)
	if err != nil {
		t.Fatal(err)
	}
	run, err := workflowStore.GetRun(ctx, "wr_legacy_fixture")
	if err != nil || run.Status != workflows.RunStatusSucceeded {
		_ = workflowStore.Close()
		t.Fatalf("legacy workflow run = %#v err=%v", run, err)
	}
	events, err := workflowStore.Events(ctx, run.ID)
	if err != nil || len(events) != 2 || events[0].Kind != "legacy.first" ||
		events[1].Kind != "legacy.second" ||
		!events[0].Time.Equal(time.Date(2025, 4, 5, 6, 7, 8, 9, time.UTC)) ||
		!events[1].Time.Equal(time.Date(2025, 4, 5, 6, 7, 8, 987654330, time.UTC)) {
		_ = workflowStore.Close()
		t.Fatalf("legacy workflow events = %#v err=%v", events, err)
	}
	if number, ok := events[0].Payload["huge"].(json.Number); !ok || number.String() != "1e400" {
		_ = workflowStore.Close()
		t.Fatalf("legacy workflow event exact number = %#v", events[0].Payload["huge"])
	}
	if number, ok := events[1].Payload["order"].(float64); !ok || number != 2 {
		_ = workflowStore.Close()
		t.Fatalf("legacy workflow second event number = %#v", events[1].Payload["order"])
	}
	if err := workflowStore.Close(); err != nil {
		t.Fatal(err)
	}
	development, err := workflows.GetWorkflowDevelopmentSession(fixture.workspace)
	if err != nil || development == nil || development.ID != "dev_legacy_fixture" {
		t.Fatalf("legacy workflow development = %#v err=%v", development, err)
	}

	reviewStore := repoaudit.NewSQLiteStore(fixture.workspace)
	if review, found, err := reviewStore.GetByID(fixture.legacyReviewState.ID); err != nil || !found ||
		!reflect.DeepEqual(review, fixture.legacyReviewState) {
		t.Fatalf("legacy repository review = %#v found=%v err=%v", review, found, err)
	}
	if profile, found, err := reviewStore.GetProfile(ctx, fixture.legacyReviewProfile.ID); err != nil || !found ||
		!reflect.DeepEqual(profile, fixture.legacyReviewProfile) {
		t.Fatalf("legacy repository review profile = %#v found=%v err=%v", profile, found, err)
	}
	if automation, found, err := reviewStore.GetAutomation(ctx, fixture.legacyReviewAutomation.ID); err != nil ||
		!found || !reflect.DeepEqual(automation, fixture.legacyReviewAutomation) {
		t.Fatalf("legacy repository review automation = %#v found=%v err=%v", automation, found, err)
	}

	evaluationStore := repoeval.NewSQLiteStore(fixture.workspace)
	evaluation, found, err := evaluationStore.Get(ctx, fixture.legacyEvaluation.ID)
	if err != nil || !found || !reflect.DeepEqual(evaluation, fixture.legacyEvaluation) {
		t.Fatalf("legacy repository evaluation = %#v found=%v err=%v", evaluation, found, err)
	}

	evolutionStore := evolution.NewSQLiteStore(evolution.NewPaths(fixture.workspace, fixture.evolutionRoot))
	tasks, err := evolutionStore.LoadTaskRecords()
	if err != nil || len(tasks) != 1 || !reflect.DeepEqual(tasks[0], fixture.legacyEvolutionTask) {
		_ = evolutionStore.Close()
		t.Fatalf("legacy evolution tasks = %#v err=%v", tasks, err)
	}
	patterns, err := evolutionStore.LoadPatternRecords()
	if err != nil || len(patterns) != 1 ||
		!reflect.DeepEqual(patterns[0], fixture.legacyEvolutionPattern) {
		_ = evolutionStore.Close()
		t.Fatalf("legacy evolution patterns = %#v err=%v", patterns, err)
	}
	drafts, err := evolutionStore.LoadDrafts()
	if err != nil || len(drafts) != 1 || !reflect.DeepEqual(drafts[0], fixture.legacyEvolutionDraft) {
		_ = evolutionStore.Close()
		t.Fatalf("legacy evolution drafts = %#v err=%v", drafts, err)
	}
	evolutionProfile, err := evolutionStore.LoadProfile("weather")
	if err != nil || !reflect.DeepEqual(evolutionProfile, fixture.legacyEvolutionProfile) {
		_ = evolutionStore.Close()
		t.Fatalf("legacy evolution profile = %#v err=%v", evolutionProfile, err)
	}
	if err := evolutionStore.Close(); err != nil {
		t.Fatal(err)
	}

	manager, err := gitworkspace.NewManager(gitworkspace.Options{RootDir: fixture.gitRoot})
	if err != nil {
		t.Fatal(err)
	}
	stats, err := manager.Stats(ctx)
	// Public Stats projects repositories through workspaces. This legacy fixture
	// intentionally imports a repository without inventing a checkout; the raw
	// typed repository row is asserted below.
	if err != nil || stats.WorkspaceCount != 0 {
		t.Fatalf("legacy Git inventory stats = %#v err=%v", stats, err)
	}
	checkpointStore, err := newPRWorkspaceCandidateCheckpointStore(fixture.checkpointRoot)
	if err != nil {
		t.Fatal(err)
	}
	checkpoint, _, found, err := checkpointStore.Load(fixture.legacyCheckpoint.WorkspaceID)
	if err != nil || !found || checkpoint != fixture.legacyCheckpoint {
		t.Fatalf("legacy checkpoint = %#v found=%v err=%v", checkpoint, found, err)
	}

	evidence, err := localci.OpenFileEvidenceStore(fixture.evidenceRoot)
	if err != nil {
		t.Fatal(err)
	}
	passing, found, err := evidence.LookupPassing(ctx, fixture.legacyLocalCIResultKey)
	if err != nil || !found || passing.Digest != fixture.legacyLocalCIExecutionID {
		_ = evidence.Close()
		t.Fatalf("legacy local-CI cache = %#v found=%v err=%v", passing, found, err)
	}
	if err := evidence.Close(); err != nil {
		t.Fatal(err)
	}
}

func assertRuntimeLegacyMigrationRows(t *testing.T, fixture *runtimeLegacyMigrationFixture) {
	t.Helper()
	assertLegacySQLRow(t, filepath.Join(fixture.home, "model-catalogs.db"),
		`SELECT group_concat(model_id, ',') FROM (
		 SELECT model_id FROM model_catalog_models WHERE catalog_id='legacy-catalog' ORDER BY position
		)`,
		"model-b,model-a")
	assertLegacySQLRow(t, filepath.Join(fixture.home, "model-catalogs.db"),
		`SELECT CAST(extra_json AS TEXT) FROM model_catalog_models WHERE catalog_id='legacy-catalog' AND position=0`,
		`{"exact":9007199254740993}`)
	assertLegacySQLRow(t, filepath.Join(fixture.home, "channels", "wecom", "reqid-store.db"),
		`SELECT request_id || '/' || chat_type FROM wecom_request_routes WHERE chat_id='legacy-chat'`,
		"legacy-request/7")
	assertLegacySQLRow(t, filepath.Join(fixture.home, "channels", "weixin", "state.db"),
		`SELECT cursor_value FROM weixin_cursors WHERE account_key='0123456789abcdef'`, "legacy-cursor")
	assertLegacySQLRow(t, filepath.Join(fixture.home, "channels", "weixin", "state.db"),
		`SELECT context_token FROM weixin_context_tokens WHERE account_key='0123456789abcdef' AND user_id='user-a'`,
		"token-a")
	assertLegacySQLInt(t, filepath.Join(fixture.workspace, "sessions", "sessions.db"),
		`SELECT COUNT(*) FROM threads WHERE thread_id IN ('thread-from-meta','thread-two')`, 2)
	assertLegacySQLInt(t, filepath.Join(fixture.workspace, "sessions", "sessions.db"),
		`SELECT COUNT(*) FROM thread_handoffs WHERE handoff_id='handoff-one'
		 AND origin_session_key='legacy-session' AND target_thread_id='thread-two'
		 AND target_session_id='ui-two'`, 1)
	assertLegacySQLRow(t, filepath.Join(fixture.workspace, "sessions", "sessions.db"),
		`SELECT group_concat(thread_id || '/' || session_key || '/' || is_primary, ',') FROM (
		 SELECT thread_id, session_key, is_primary FROM thread_sessions
		 WHERE thread_id IN ('thread-from-meta','thread-two') ORDER BY thread_id, sequence
		)`, "thread-from-meta/legacy-session/1,thread-two/other/1")
	assertLegacySQLRow(t, filepath.Join(fixture.workspace, "sessions", "sessions.db"),
		`SELECT group_concat(session_key || '/' || thread_id, ',') FROM (
		 SELECT session_key, thread_id FROM session_thread_links
		 WHERE session_key IN ('legacy-session','other') ORDER BY session_key
		)`, "legacy-session/thread-from-meta,other/thread-two")
	assertLegacySQLRow(t, filepath.Join(fixture.workspace, "state", "account-router.db"),
		`SELECT failure_count || '/' || requests FROM account_router_accounts
		 WHERE router_name='legacy-router' AND account_ref='credential:openai:work'`, "2/3")
	routerTime := time.Date(2026, 8, 31, 12, 0, 0, 123, time.UTC)
	assertLegacySQLInt(t, filepath.Join(fixture.workspace, "state", "account-router.db"),
		`SELECT COUNT(*) FROM account_router_accounts WHERE router_name='legacy-router'
		 AND account_ref='credential:openai:work' AND health_state='unavailable'
		 AND failure_reason='auth' AND failure_count=2 AND requests=3
		 AND unavailable_until_unix_seconds=? AND unavailable_until_nanosecond=?
		 AND last_failure_at_unix_seconds=? AND last_failure_at_nanosecond=?
		 AND last_error='authentication failed'`, 1,
		routerTime.Add(time.Hour).Unix(), routerTime.Add(time.Hour).Nanosecond(),
		routerTime.Unix(), routerTime.Nanosecond())
	assertLegacySQLInt(t, filepath.Join(fixture.workspace, "state", "account-router.db"),
		`SELECT COUNT(*) FROM account_router_session_affinities
		 WHERE router_name='legacy-router' AND session_key='legacy-router-session'
		 AND block_id='pool' AND account_ref='credential:openai:work'
		 AND select_reason='initial' AND selected_at_unix_seconds=?
		 AND selected_at_nanosecond=?`, 1, routerTime.Unix(), routerTime.Nanosecond())
	assertLegacySQLInt(t, filepath.Join(fixture.workspace, "state", "account-router.db"),
		`SELECT COUNT(*) FROM account_router_block_cursors
		 WHERE router_name='legacy-router' AND block_id='pool' AND cursor=3
		 AND updated_at_unix_seconds=? AND updated_at_nanosecond=?`,
		1, routerTime.Unix(), routerTime.Nanosecond())
	assertLegacySQLRow(t, filepath.Join(fixture.workspace, "state", "workflows.db"),
		`SELECT CAST(value_json AS TEXT) FROM workflow_native_state WHERE key_text='legacy-key'`, `{"n":42}`)
	assertLegacySQLRow(t, filepath.Join(fixture.workspace, "state", "workflows.db"),
		`SELECT CAST(event_json AS TEXT) FROM workflow_run_payloads WHERE run_id='wr_legacy_fixture'`,
		`{"large":9007199254740993}`)
	assertLegacySQLRow(t, filepath.Join(fixture.workspace, "state", "workflows.db"),
		`SELECT group_concat(sequence || '/' || kind || '/' || occurred_at_seconds || '/' ||
		 occurred_nanosecond, ',') FROM (
		 SELECT sequence, kind, occurred_at_seconds, occurred_nanosecond FROM workflow_run_events
		 WHERE run_id='wr_legacy_fixture' ORDER BY sequence
		)`, "0/legacy.first/1743833228/9,1/legacy.second/1743833228/987654330")
	assertLegacySQLInt(t, filepath.Join(fixture.workspace, "repository_reviews", "repository-reviews.db"),
		`SELECT COUNT(*) FROM repository_review_profiles WHERE profile_id='rrpf_legacy_fixture'`, 1)
	assertLegacySQLInt(t, filepath.Join(fixture.workspace, "repository_reviews", "repository-reviews.db"),
		`SELECT COUNT(*) FROM repository_review_automations WHERE automation_id='rra_legacy_fixture'`, 1)
	assertLegacySQLRow(t, filepath.Join(fixture.workspace, "repository_evaluations", "evaluations.db"),
		`SELECT group_concat(model_alias, ',') FROM (
		 SELECT model_alias FROM repository_evaluation_models WHERE evaluation_id=? ORDER BY position
		)`, "model-a,model-b", fixture.legacyEvaluation.ID)
	assertLegacySQLInt(t, filepath.Join(fixture.evolutionRoot, "evolution.db"),
		`SELECT COUNT(*) FROM evolution_records WHERE record_id IN ('task-legacy','pattern-legacy')`, 2)
	assertLegacySQLRow(t, filepath.Join(fixture.gitRoot, "inventory.db"),
		`SELECT remote_url FROM inventory_repositories WHERE repository_id=?`,
		"https://example.invalid/owner/legacy.git", fixture.legacyGitRepositoryID)
	gitFirst := time.Date(2026, 8, 31, 12, 34, 56, 987654321, time.UTC)
	assertLegacySQLInt(t, filepath.Join(fixture.gitRoot, "inventory.db"),
		`SELECT COUNT(*) FROM inventory_repositories WHERE repository_id=?
		 AND first_seen_unix_seconds=? AND first_seen_nanosecond=?
		 AND last_seen_unix_seconds=? AND last_seen_nanosecond=?
		 AND last_work_unix_seconds=? AND last_work_nanosecond=?`, 1,
		fixture.legacyGitRepositoryID,
		gitFirst.Unix(), gitFirst.Nanosecond(),
		gitFirst.Add(time.Second).Unix(), gitFirst.Add(time.Second).Nanosecond(),
		gitFirst.Add(2*time.Second).Unix(), gitFirst.Add(2*time.Second).Nanosecond())
}

func assertLegacySQLRow(t *testing.T, path, query, want string, args ...any) {
	t.Helper()
	database := openRuntimeLegacyDatabase(t, path)
	defer database.Close()
	var got string
	if err := database.QueryRow(query, args...).Scan(&got); err != nil || got != want {
		t.Fatalf("%s query result = %q err=%v; want %q", filepath.Base(path), got, err, want)
	}
}

func assertLegacySQLInt(t *testing.T, path, query string, want int, args ...any) {
	t.Helper()
	database := openRuntimeLegacyDatabase(t, path)
	defer database.Close()
	var got int
	if err := database.QueryRow(query, args...).Scan(&got); err != nil || got != want {
		t.Fatalf("%s query result = %d err=%v; want %d", filepath.Base(path), got, err, want)
	}
}

func assertRuntimeLegacyMigrationArchives(t *testing.T, fixture *runtimeLegacyMigrationFixture) {
	t.Helper()
	for _, source := range fixture.sources {
		if _, err := os.Lstat(source.source); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("legacy source remains: %s: %v", source.source, err)
		}
		archived, err := os.ReadFile(source.archive)
		if err != nil || !bytes.Equal(archived, source.data) {
			t.Fatalf("legacy archive %s = %d bytes err=%v", source.archive, len(archived), err)
		}
		if info, statErr := os.Stat(source.archive); statErr != nil ||
			(runtimeStoragePOSIXModes() && info.Mode().Perm() != source.mode.Perm()) {
			t.Fatalf("legacy archive mode %s = %#v err=%v", source.archive, info, statErr)
		}
	}
	launcherArchive := filepath.Join(fixture.home, "legacy-json", "launcher-auth-v1", launcherconfig.FileName)
	if archived, err := os.ReadFile(launcherArchive); err != nil || !bytes.Equal(archived, fixture.launcherOriginal) {
		t.Fatalf("launcher archive = %d bytes err=%v", len(archived), err)
	}
	if info, err := os.Stat(launcherArchive); err != nil ||
		(runtimeStoragePOSIXModes() && info.Mode().Perm() != 0o640) {
		t.Fatalf("launcher archive mode = %#v err=%v", info, err)
	}
	clean, err := os.ReadFile(fixture.launcherPath)
	if err != nil || bytes.Contains(clean, []byte("dashboard_password_hash")) ||
		bytes.Contains(clean, []byte("launcher_token")) || bytes.Contains(clean, []byte(fixture.launcherPassword)) {
		t.Fatalf("launcher settings were not stripped safely: %q err=%v", clean, err)
	}
	settings, err := launcherconfig.Load(fixture.launcherPath, launcherconfig.Default())
	if err != nil || settings.Port != 18800 || settings.Public {
		t.Fatalf("launcher settings changed during auth migration: %#v err=%v", settings, err)
	}
}

func runtimeLegacyArchiveIdentities(
	t *testing.T,
	fixture *runtimeLegacyMigrationFixture,
) map[string]os.FileInfo {
	t.Helper()
	paths := make([]string, 0, len(fixture.sources)+1)
	for _, source := range fixture.sources {
		paths = append(paths, source.archive)
	}
	paths = append(paths,
		filepath.Join(fixture.home, "legacy-json", "launcher-auth-v1", launcherconfig.FileName))
	identities := make(map[string]os.FileInfo, len(paths))
	for _, path := range paths {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		identities[path] = info
	}
	return identities
}

func assertRuntimeLegacyArchiveIdentities(t *testing.T, before map[string]os.FileInfo) {
	t.Helper()
	for path, prior := range before {
		current, err := os.Stat(path)
		if err != nil || !os.SameFile(prior, current) {
			t.Fatalf("legacy archive identity changed on reopen: %s: %#v err=%v", path, current, err)
		}
	}
}

func runtimeStoragePOSIXModes() bool {
	return os.PathSeparator == '/'
}

func assertRuntimeLegacyMigrationAuditSafety(t *testing.T, fixture *runtimeLegacyMigrationFixture) {
	t.Helper()
	expectations := runtimeLegacyAuditExpectations(fixture)
	databasePaths := runtimeStorageExpectedDatabasePaths(fixture.runtimeStorageIntegrationFixture)
	for _, path := range databasePaths {
		database := openRuntimeLegacyDatabase(t, path)
		if filepath.Base(path) == dashboardauth.DBFilename {
			var ledgerRows int
			if err := database.QueryRow(`SELECT COUNT(*) FROM launcher_auth_legacy_imports`).Scan(
				&ledgerRows,
			); err != nil || ledgerRows != 1 {
				_ = database.Close()
				t.Fatalf("launcher legacy ledger rows = %d err=%v", ledgerRows, err)
			}
			var relative, digest, issue, archiveStatus string
			var imported, skipped int
			if err := database.QueryRow(`SELECT source_relative, hex(source_digest), imported_count,
				skipped_count, COALESCE(issue_code,''), archive_status
				FROM launcher_auth_legacy_imports`).Scan(
				&relative, &digest, &imported, &skipped, &issue, &archiveStatus,
			); err != nil || relative != launcherconfig.FileName ||
				digest != runtimeLegacyDigestHex(fixture.launcherOriginal) ||
				imported != 1 || skipped != 1 || issue != "invalid-bcrypt-hash" ||
				archiveStatus != "complete" {
				_ = database.Close()
				t.Fatalf("launcher legacy audit = %q/%s/%d/%d/%q/%q err=%v",
					relative, digest, imported, skipped, issue, archiveStatus, err)
			}
		} else {
			expected, ok := expectations[filepath.Clean(path)]
			if !ok {
				_ = database.Close()
				t.Fatalf("missing legacy audit expectation for %s", path)
			}
			component, records, issues, horizons, err := runtimeLegacyImportAudit(database)
			if err != nil {
				_ = database.Close()
				t.Fatal(err)
			}
			if component != expected.component || horizons != 1 ||
				!reflect.DeepEqual(records, expected.sources) ||
				!runtimeLegacyIssuesMatch(issues, expected.issues) {
				_ = database.Close()
				t.Fatalf(
					"legacy audit mismatch in %s: component=%q horizons=%d\nrecords=%#v\nissues=%#v\nwant=%#v/%#v/%#v",
					path, component, horizons, records, issues, expected.component,
					expected.sources, expected.issues,
				)
			}
		}
		if err := database.Close(); err != nil {
			t.Fatal(err)
		}
		for _, candidate := range []string{path, path + "-wal", path + "-shm"} {
			raw, err := os.ReadFile(candidate)
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			if err != nil {
				t.Fatal(err)
			}
			if bytes.Contains(raw, []byte(legacyMigrationSecretCanary)) {
				t.Fatalf("rejected legacy payload leaked into %s", candidate)
			}
		}
	}
}

func runtimeLegacyIssuesMatch(got, want []runtimeLegacyImportIssue) bool {
	if len(got) != len(want) {
		return false
	}
	for index := range want {
		if got[index] != want[index] {
			return false
		}
	}
	return true
}

type runtimeLegacySourceAudit struct {
	imported int
	skipped  int
	digest   string
}

type runtimeLegacyImportIssue struct {
	relative string
	code     string
	digest   string
}

type runtimeLegacyAuditExpectation struct {
	component string
	sources   map[string]runtimeLegacySourceAudit
	issues    []runtimeLegacyImportIssue
}

func runtimeLegacyAuditExpectations(
	fixture *runtimeLegacyMigrationFixture,
) map[string]runtimeLegacyAuditExpectation {
	nativeRelative := filepath.ToSlash(filepath.Join(
		"workflow_state",
		runtimeLegacySafeStorageSegment("legacy-space"),
		runtimeLegacySafeStorageSegment("legacy-key")+".json",
	))
	reviewStateName := "repo_" + strings.TrimPrefix(fixture.legacyReviewState.ID, "rrp_") + ".json"
	evaluationName := "evaluation_" + fixture.legacyEvaluation.ID + ".json"
	checkpointName := legacyPRWorkspaceCheckpointFilename(fixture.legacyCheckpoint.WorkspaceID)
	malformedCheckpointName := legacyPRWorkspaceCheckpointFilename(
		"devw_88888888888888888888888888888888",
	)
	localCIValid := filepath.ToSlash(filepath.Join(
		"cache", fixture.legacyLocalCIResultKey[:2], fixture.legacyLocalCIResultKey+".json",
	))
	localCIBadKey := strings.Repeat("a", 64)
	localCIBad := filepath.ToSlash(filepath.Join("cache", localCIBadKey[:2], localCIBadKey+".json"))
	expectations := map[string]runtimeLegacyAuditExpectation{
		filepath.Clean(filepath.Join(fixture.home, "auth.db")): {
			component: "auth",
			sources:   map[string]runtimeLegacySourceAudit{"auth.json": {imported: 1, skipped: 2}},
			issues: []runtimeLegacyImportIssue{
				{relative: "auth.json", code: "invalid-credential"},
				{relative: "auth.json", code: "invalid-identity"},
			},
		},
		filepath.Clean(filepath.Join(fixture.home, "model-catalogs.db")): {
			component: "model-catalogs",
			sources: map[string]runtimeLegacySourceAudit{
				"model_catalogs.json": {imported: 1, skipped: 1},
			},
			issues: []runtimeLegacyImportIssue{{relative: "model_catalogs.json", code: "invalid-catalog"}},
		},
		filepath.Clean(filepath.Join(fixture.home, "tool-adaptation.db")): {
			component: "tool-adaptation",
			sources: map[string]runtimeLegacySourceAudit{
				"tool_adaptation_state.json": {imported: 2, skipped: 2},
			},
			issues: []runtimeLegacyImportIssue{
				{relative: "tool_adaptation_state.json", code: "invalid-observation"},
				{relative: "tool_adaptation_state.json", code: "invalid-outcome"},
			},
		},
		filepath.Clean(filepath.Join(fixture.home, "channels", "wecom", "reqid-store.db")): {
			component: "wecom-reqid",
			sources: map[string]runtimeLegacySourceAudit{
				"wecom/reqid-store.json": {imported: 1, skipped: 1},
			},
			issues: []runtimeLegacyImportIssue{{relative: "wecom/reqid-store.json", code: "invalid-route"}},
		},
		filepath.Clean(filepath.Join(fixture.home, "channels", "weixin", "state.db")): {
			component: "weixin-state",
			sources: map[string]runtimeLegacySourceAudit{
				"context-tokens/0123456789abcdef.json": {imported: 1, skipped: 2},
				"sync/0123456789abcdef.json":           {imported: 1, skipped: 0},
			},
			issues: []runtimeLegacyImportIssue{
				{relative: "context-tokens/0123456789abcdef.json", code: "invalid-token"},
				{relative: "context-tokens/0123456789abcdef.json", code: "invalid-token"},
			},
		},
		filepath.Clean(filepath.Join(fixture.workspace, "sessions", "sessions.db")): {
			component: "sessions",
			sources: map[string]runtimeLegacySourceAudit{
				"sessions/broken.json":              {imported: 0, skipped: 1},
				"sessions/legacy.history-a":         {imported: 2, skipped: 1},
				"sessions/legacy.meta.json":         {imported: 1, skipped: 0},
				"sessions/other.json":               {imported: 1, skipped: 0},
				"threads/handoffs/handoff-one.json": {imported: 1, skipped: 0},
				"threads/thread-two.json":           {imported: 1, skipped: 0},
			},
			issues: []runtimeLegacyImportIssue{
				{relative: "sessions/broken.json", code: "invalid-session-json"},
				{relative: "sessions/legacy.history-a", code: "invalid-message-json"},
			},
		},
		filepath.Clean(filepath.Join(fixture.workspace, "cron", "jobs.db")): {
			component: "cron-jobs",
			sources:   map[string]runtimeLegacySourceAudit{"jobs.json": {imported: 2, skipped: 1}},
			issues:    []runtimeLegacyImportIssue{{relative: "jobs.json", code: "invalid-job"}},
		},
		filepath.Clean(filepath.Join(fixture.workspace, "state", "runtime.db")): {
			component: "runtime-state",
			sources: map[string]runtimeLegacySourceAudit{
				"state.json":       {imported: 1, skipped: 0},
				"state/state.json": {imported: 1, skipped: 1},
			},
			issues: []runtimeLegacyImportIssue{{relative: "state/state.json", code: "source-conflict"}},
		},
		filepath.Clean(filepath.Join(fixture.workspace, "state", "account-router.db")): {
			component: "account-router",
			sources: map[string]runtimeLegacySourceAudit{
				"account_router_state.json": {imported: 1, skipped: 1},
			},
			issues: []runtimeLegacyImportIssue{
				{relative: "account_router_state.json", code: "invalid-router"},
			},
		},
		filepath.Clean(filepath.Join(fixture.workspace, "state", "workflows.db")): {
			component: "workflows",
			sources: map[string]runtimeLegacySourceAudit{
				"workflow_dev/active.json":                     {imported: 1, skipped: 0},
				"workflow_runs/wr_legacy_fixture/events.jsonl": {imported: 2, skipped: 1},
				"workflow_runs/wr_legacy_fixture/run.json":     {imported: 1, skipped: 0},
				nativeRelative: {imported: 1, skipped: 0},
			},
			issues: []runtimeLegacyImportIssue{
				{relative: "workflow_runs/wr_legacy_fixture/events.jsonl", code: "invalid_event_line"},
			},
		},
		filepath.Clean(filepath.Join(
			fixture.workspace, "repository_reviews", "repository-reviews.db",
		)): {
			component: "repository-reviews",
			sources: map[string]runtimeLegacySourceAudit{
				"automation_rra_legacy_fixture.json": {imported: 1, skipped: 0},
				"profile_rrpf_legacy_fixture.json":   {imported: 1, skipped: 0},
				"profile_rrpf_malformed.json":        {imported: 0, skipped: 1},
				reviewStateName:                      {imported: 1, skipped: 0},
				strings.TrimSuffix(reviewStateName, ".json") + ".summary.json": {
					imported: 0, skipped: 0,
				},
			},
			issues: []runtimeLegacyImportIssue{
				{relative: "profile_rrpf_malformed.json", code: "invalid_profile"},
			},
		},
		filepath.Clean(filepath.Join(fixture.workspace, "repository_evaluations", "evaluations.db")): {
			component: "repository-evaluations",
			sources: map[string]runtimeLegacySourceAudit{
				evaluationName: {imported: 1, skipped: 0},
				"evaluation_rme_ffffffffffffffffffffffffffffffff.json": {imported: 0, skipped: 1},
			},
			issues: []runtimeLegacyImportIssue{{
				relative: "evaluation_rme_ffffffffffffffffffffffffffffffff.json", code: "malformed_json",
			}},
		},
		filepath.Clean(filepath.Join(fixture.evolutionRoot, "evolution.db")): {
			component: "evolution",
			sources: map[string]runtimeLegacySourceAudit{
				"learning-records.jsonl": {imported: 2, skipped: 0},
				"profiles/weather.json":  {imported: 1, skipped: 0},
				"skill-drafts.json":      {imported: 1, skipped: 1},
				"task-records.jsonl":     {imported: 1, skipped: 1},
			},
			issues: []runtimeLegacyImportIssue{
				{relative: "skill-drafts.json", code: "invalid-draft"},
				{relative: "task-records.jsonl", code: "malformed-record"},
			},
		},
		filepath.Clean(filepath.Join(fixture.evidenceRoot, "cache.db")): {
			component: "local_ci_cache",
			sources: map[string]runtimeLegacySourceAudit{
				localCIValid: {imported: 1, skipped: 0}, localCIBad: {imported: 0, skipped: 1},
			},
			issues: []runtimeLegacyImportIssue{{relative: localCIBad, code: "malformed-index"}},
		},
		filepath.Clean(filepath.Join(fixture.gitRoot, "inventory.db")): {
			component: "git-workspace-inventory",
			sources: map[string]runtimeLegacySourceAudit{
				"inventory.json": {imported: 1, skipped: 0},
			},
		},
		filepath.Clean(filepath.Join(fixture.checkpointRoot, "checkpoints.db")): {
			component: "pr-workspace-checkpoints",
			sources: map[string]runtimeLegacySourceAudit{
				checkpointName:          {imported: 1, skipped: 0},
				malformedCheckpointName: {imported: 0, skipped: 1},
			},
			issues: []runtimeLegacyImportIssue{{
				relative: malformedCheckpointName, code: "malformed-checkpoint",
			}},
		},
	}
	for path, expectation := range expectations {
		root := runtimeLegacySourceRoot(fixture, expectation.component)
		for relative, sourceAudit := range expectation.sources {
			sourcePath := filepath.Clean(filepath.Join(root, filepath.FromSlash(relative)))
			found := false
			for _, source := range fixture.sources {
				if filepath.Clean(source.source) != sourcePath {
					continue
				}
				sourceAudit.digest = runtimeLegacyDigestHex(source.data)
				expectation.sources[relative] = sourceAudit
				found = true
				break
			}
			if !found {
				panic("legacy audit expectation has no seeded source: " + relative)
			}
		}
		seededIssues := fixture.expectedIssueDigests[expectation.component]
		if len(seededIssues) != len(expectation.issues) {
			panic("legacy audit expectation has incomplete issue digests: " + expectation.component)
		}
		for index := range expectation.issues {
			if expectation.issues[index].relative != seededIssues[index].relative ||
				expectation.issues[index].code != seededIssues[index].code {
				panic("legacy audit issue order differs from seeded records: " + expectation.component)
			}
			expectation.issues[index].digest = seededIssues[index].digest
		}
		expectations[path] = expectation
	}
	return expectations
}

func runtimeLegacySourceRoot(fixture *runtimeLegacyMigrationFixture, component string) string {
	switch component {
	case "auth", "model-catalogs", "tool-adaptation", "wecom-reqid":
		return fixture.home
	case "weixin-state":
		return filepath.Join(fixture.home, "channels", "weixin")
	case "cron-jobs":
		return filepath.Join(fixture.workspace, "cron")
	case "repository-reviews":
		return filepath.Join(fixture.workspace, "repository_reviews")
	case "repository-evaluations":
		return filepath.Join(fixture.workspace, "repository_evaluations")
	case "evolution":
		return fixture.evolutionRoot
	case "local_ci_cache":
		return fixture.evidenceRoot
	case "git-workspace-inventory":
		return fixture.gitRoot
	case "pr-workspace-checkpoints":
		return fixture.checkpointRoot
	default:
		return fixture.workspace
	}
}

func runtimeLegacyDigestHex(data []byte) string {
	digest := sha256.Sum256(data)
	return strings.ToUpper(hex.EncodeToString(digest[:]))
}

//nolint:govet,sqlclosecheck // Exact row/close failures are returned at each sequential audit boundary.
func runtimeLegacyImportAudit(
	database *sql.DB,
) (string, map[string]runtimeLegacySourceAudit, []runtimeLegacyImportIssue, int, error) {
	rows, err := database.Query(`SELECT component, source_relative, hex(source_digest),
		imported_count, skipped_count, archive_status FROM storage_imports ORDER BY source_relative`)
	if err != nil {
		return "", nil, nil, 0, err
	}
	records := make(map[string]runtimeLegacySourceAudit)
	component := ""
	for rows.Next() {
		var rowComponent, relative, digest, status string
		var imported, skipped int
		if err := rows.Scan(
			&rowComponent, &relative, &digest, &imported, &skipped, &status,
		); err != nil {
			_ = rows.Close()
			return "", nil, nil, 0, err
		}
		if len(digest) != sha256.Size*2 || status != "complete" ||
			(component != "" && component != rowComponent) {
			_ = rows.Close()
			return "", nil, nil, 0, errors.New("legacy import record is unsafe or incomplete")
		}
		component = rowComponent
		records[relative] = runtimeLegacySourceAudit{
			imported: imported, skipped: skipped, digest: digest,
		}
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return "", nil, nil, 0, err
	}
	if err := rows.Close(); err != nil {
		return "", nil, nil, 0, err
	}
	issueRows, err := database.Query(`SELECT imports.source_relative, issues.issue_code,
		hex(issues.record_digest) FROM storage_import_issues AS issues
		JOIN storage_imports AS imports
		  ON imports.component=issues.component AND imports.source_id=issues.source_id
		ORDER BY imports.source_relative, issues.sequence`)
	if err != nil {
		return "", nil, nil, 0, err
	}
	issues := make([]runtimeLegacyImportIssue, 0)
	for issueRows.Next() {
		var issue runtimeLegacyImportIssue
		if err := issueRows.Scan(&issue.relative, &issue.code, &issue.digest); err != nil {
			_ = issueRows.Close()
			return "", nil, nil, 0, err
		}
		if len(issue.digest) != sha256.Size*2 || strings.Trim(issue.digest, "0") == "" ||
			strings.Contains(issue.code, legacyMigrationSecretCanary) || len(issue.code) > 80 {
			_ = issueRows.Close()
			return "", nil, nil, 0, errors.New("legacy import issue is unsafe")
		}
		issues = append(issues, issue)
	}
	if err := issueRows.Err(); err != nil {
		_ = issueRows.Close()
		return "", nil, nil, 0, err
	}
	if err := issueRows.Close(); err != nil {
		return "", nil, nil, 0, err
	}
	if len(issues) == 0 {
		issues = nil
	}
	var horizons, matchingHorizons int
	if err := database.QueryRow(`SELECT COUNT(*),
		COUNT(*) FILTER (WHERE component=?) FROM storage_import_horizons`, component).Scan(
		&horizons, &matchingHorizons,
	); err != nil {
		return "", nil, nil, 0, err
	}
	if horizons != 1 || matchingHorizons != 1 {
		return "", nil, nil, 0, errors.New("legacy import horizon set is invalid")
	}
	return component, records, issues, horizons, nil
}

func exerciseRuntimeLegacyMigrationSecondStartup(
	t *testing.T,
	fixture *runtimeLegacyMigrationFixture,
) {
	t.Helper()
	// Repeat the same exported owner boundaries. Their unrelated idempotent live
	// writes retain row cardinality while every legacy ledger must remain fixed.
	exerciseRuntimeLegacyMigrationFirstStartup(t, fixture)
}

type runtimeLegacyDatabaseInventory struct {
	UserVersion int
	Tables      []string
	Imports     []string
	Issues      []string
	Horizons    []string
}

//nolint:govet // Inventory errors intentionally remain beside their exact query boundary.
func runtimeLegacyMigrationInventory(
	t *testing.T,
	fixture *runtimeLegacyMigrationFixture,
) map[string]runtimeLegacyDatabaseInventory {
	t.Helper()
	inventory := make(map[string]runtimeLegacyDatabaseInventory)
	for _, path := range runtimeStorageExpectedDatabasePaths(fixture.runtimeStorageIntegrationFixture) {
		database := openRuntimeLegacyDatabase(t, path)
		entry := runtimeLegacyDatabaseInventory{}
		if err := database.QueryRow(`PRAGMA user_version`).Scan(&entry.UserVersion); err != nil {
			_ = database.Close()
			t.Fatal(err)
		}
		names, err := runtimeLegacyDatabaseTableNames(database)
		if err != nil {
			_ = database.Close()
			t.Fatal(err)
		}
		for _, name := range names {
			var count int
			quoted := `"` + strings.ReplaceAll(name, `"`, `""`) + `"`
			if err := database.QueryRow(`SELECT COUNT(*) FROM ` + quoted).Scan(&count); err != nil {
				_ = database.Close()
				t.Fatal(err)
			}
			entry.Tables = append(entry.Tables, fmt.Sprintf("%s=%d", name, count))
		}
		if filepath.Base(path) == dashboardauth.DBFilename {
			entry.Imports, err = runtimeLegacyLauncherImportInventory(database)
		} else {
			entry.Imports, err = runtimeLegacyImportInventory(database)
			if err == nil {
				entry.Issues, err = runtimeLegacyIssueInventory(database)
			}
			if err == nil {
				entry.Horizons, err = runtimeLegacyHorizonInventory(database)
			}
		}
		if err != nil {
			_ = database.Close()
			t.Fatal(err)
		}
		if err := database.Close(); err != nil {
			t.Fatal(err)
		}
		inventory[runtimeStorageSlashRelative(fixture.root, path)] = entry
	}
	return inventory
}

func runtimeLegacyImportInventory(database *sql.DB) ([]string, error) {
	rows, err := database.Query(`SELECT component, source_id, source_relative,
		hex(source_digest), source_size, source_limit, source_mode, imported_count,
		skipped_count, archive_status, imported_at, COALESCE(archived_at, -1)
		FROM storage_imports ORDER BY component, source_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	values := make([]string, 0)
	for rows.Next() {
		var component, id, relative, digest, status string
		var size, limit, mode, imported, skipped, importedAt, archivedAt int64
		if err := rows.Scan(
			&component, &id, &relative, &digest, &size, &limit, &mode, &imported, &skipped,
			&status, &importedAt, &archivedAt,
		); err != nil {
			return nil, err
		}
		values = append(values, fmt.Sprintf(
			"%s|%s|%s|%s|%d|%d|%d|%d|%d|%s|%d|%d",
			component, id, relative, digest, size, limit, mode, imported, skipped,
			status, importedAt, archivedAt,
		))
	}
	return values, rows.Err()
}

func runtimeLegacyIssueInventory(database *sql.DB) ([]string, error) {
	rows, err := database.Query(`SELECT component, source_id, sequence, issue_code,
		hex(record_digest) FROM storage_import_issues ORDER BY component, source_id, sequence`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	values := make([]string, 0)
	for rows.Next() {
		var component, sourceID, code, digest string
		var sequence int
		if err := rows.Scan(&component, &sourceID, &sequence, &code, &digest); err != nil {
			return nil, err
		}
		values = append(values, fmt.Sprintf("%s|%s|%d|%s|%s",
			component, sourceID, sequence, code, digest))
	}
	return values, rows.Err()
}

func runtimeLegacyHorizonInventory(database *sql.DB) ([]string, error) {
	rows, err := database.Query(`SELECT component, completed_at
		FROM storage_import_horizons ORDER BY component`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	values := make([]string, 0)
	for rows.Next() {
		var component string
		var completedAt int64
		if err := rows.Scan(&component, &completedAt); err != nil {
			return nil, err
		}
		values = append(values, fmt.Sprintf("%s|%d", component, completedAt))
	}
	return values, rows.Err()
}

func runtimeLegacyLauncherImportInventory(database *sql.DB) ([]string, error) {
	rows, err := database.Query(`SELECT source_id, source_relative, hex(source_digest),
		source_size, source_limit, source_mode, credential_source, imported_count,
		skipped_count, COALESCE(issue_code,''), archive_status, imported_at,
		COALESCE(archived_at,'') FROM launcher_auth_legacy_imports ORDER BY source_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	values := make([]string, 0)
	for rows.Next() {
		var id, relative, digest, credentialSource, issue, status, importedAt, archivedAt string
		var size, limit, mode, imported, skipped int64
		if err := rows.Scan(
			&id, &relative, &digest, &size, &limit, &mode, &credentialSource,
			&imported, &skipped, &issue, &status, &importedAt, &archivedAt,
		); err != nil {
			return nil, err
		}
		values = append(values, fmt.Sprintf(
			"%s|%s|%s|%d|%d|%d|%s|%d|%d|%s|%s|%s|%s",
			id, relative, digest, size, limit, mode, credentialSource, imported, skipped,
			issue, status, importedAt, archivedAt,
		))
	}
	return values, rows.Err()
}

func runtimeLegacyDatabaseTableNames(database *sql.DB) ([]string, error) {
	rows, err := database.Query(`SELECT name FROM sqlite_schema
		WHERE type='table' AND name NOT LIKE 'sqlite_%' ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	names := make([]string, 0)
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		names = append(names, name)
	}
	return names, rows.Err()
}

func openRuntimeLegacyDatabase(t *testing.T, path string) *sql.DB {
	t.Helper()
	database, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if err := database.PingContext(t.Context()); err != nil {
		_ = database.Close()
		t.Fatalf("open SQLite database %s: %v", path, err)
	}
	return database
}

func testRuntimeLegacyCrashBeforeCommit(t *testing.T) {
	t.Helper()
	root := filepath.Join(t.TempDir(), "crash-before-commit")
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(root, "legacy.json")
	data := []byte(`{"id":"would-have-committed"}`)
	if err := os.WriteFile(source, data, 0o600); err != nil {
		t.Fatal(err)
	}
	databasePath := filepath.Join(root, "fixture.db")
	archiveRoot := filepath.Join(root, "legacy-json", "crash-fixture-v1")
	sentinel := errors.New("injected crash before commit")
	_, err := sqlitestore.Open(t.Context(), databasePath, sqlitestore.Options{
		Component: "integration-crash-before-commit",
		Migrations: []sqlitestore.Migration{{
			Version: 1,
			Statements: []string{`CREATE TABLE fixture_rows (
				id TEXT PRIMARY KEY, value_text TEXT NOT NULL
			) STRICT`},
		}},
		Legacy: &sqlitestore.LegacyOptions{
			SourceRoot:  root,
			ArchiveRoot: archiveRoot,
			Sources: func() ([]sqlitestore.LegacySource, error) {
				return []sqlitestore.LegacySource{{
					ID: "fixture-source", Relative: "legacy.json", MaxBytes: 4096,
				}}, nil
			},
			Import: func(
				ctx context.Context,
				conn *sql.Conn,
				_ sqlitestore.LegacyInput,
			) (sqlitestore.ImportResult, error) {
				if _, insertErr := conn.ExecContext(ctx,
					`INSERT INTO fixture_rows(id,value_text) VALUES('row','value')`); insertErr != nil {
					return sqlitestore.ImportResult{}, insertErr
				}
				return sqlitestore.ImportResult{}, sentinel
			},
			MaxBytes: 4096, MaxSources: 1, MaxTotalBytes: 4096,
		},
	})
	if !errors.Is(err, sentinel) {
		t.Fatalf("crash-before-commit error = %v", err)
	}
	database := openRuntimeLegacyDatabase(t, databasePath)
	defer database.Close()
	var userVersion, retainedObjects int
	if err := database.QueryRow(`PRAGMA user_version`).Scan(&userVersion); err != nil {
		t.Fatal(err)
	}
	if err := database.QueryRow(`SELECT COUNT(*) FROM sqlite_schema
		WHERE name NOT LIKE 'sqlite_%'`).Scan(&retainedObjects); err != nil {
		t.Fatal(err)
	}
	if userVersion != 0 || retainedObjects != 0 {
		t.Fatalf("failed migration committed version=%d objects=%d", userVersion, retainedObjects)
	}
	if current, readErr := os.ReadFile(source); readErr != nil || !bytes.Equal(current, data) {
		t.Fatalf("failed migration source = %q err=%v", current, readErr)
	}
	if _, statErr := os.Lstat(filepath.Join(archiveRoot, "legacy.json")); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("failed migration created archive: %v", statErr)
	}
}

//nolint:govet // Recovery assertions intentionally keep sequential error boundaries explicit.
func testRuntimeLegacyInterruptedArchiveRecovery(t *testing.T) {
	t.Helper()
	root := filepath.Join(t.TempDir(), "archive-recovery")
	archiveRoot := filepath.Join(root, "legacy-json", "archive-fixture-v1")
	if err := os.MkdirAll(archiveRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	dataByRelative := map[string][]byte{
		"a.json": []byte(`{"id":"first-committed-once"}`),
		"b.json": []byte(`{"id":"second-committed-once"}`),
	}
	for relative, data := range dataByRelative {
		if err := os.WriteFile(filepath.Join(root, relative), data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	conflictingArchive := filepath.Join(archiveRoot, "b.json")
	if err := os.WriteFile(conflictingArchive, []byte("conflicting partial publication"), 0o600); err != nil {
		t.Fatal(err)
	}
	databasePath := filepath.Join(root, "fixture.db")
	importCalls := make(map[string]int)
	options := sqlitestore.Options{
		Component: "integration-archive-recovery",
		Migrations: []sqlitestore.Migration{{
			Version: 1,
			Statements: []string{`CREATE TABLE fixture_rows (
				id TEXT PRIMARY KEY, value_text TEXT NOT NULL
			) STRICT`},
		}},
		Legacy: &sqlitestore.LegacyOptions{
			SourceRoot:  root,
			ArchiveRoot: archiveRoot,
			Sources: func() ([]sqlitestore.LegacySource, error) {
				return []sqlitestore.LegacySource{
					{ID: "fixture-source-a", Relative: "a.json", MaxBytes: 4096},
					{ID: "fixture-source-b", Relative: "b.json", MaxBytes: 4096},
				}, nil
			},
			Import: func(
				ctx context.Context,
				conn *sql.Conn,
				input sqlitestore.LegacyInput,
			) (sqlitestore.ImportResult, error) {
				importCalls[input.ID]++
				_, err := conn.ExecContext(ctx,
					`INSERT INTO fixture_rows(id,value_text) VALUES(?,?)`, input.ID, input.Relative)
				return sqlitestore.ImportResult{Imported: 1}, err
			},
			MaxBytes: 4096, MaxSources: 2, MaxTotalBytes: 8192,
		},
	}
	opened, err := sqlitestore.Open(t.Context(), databasePath, options)
	if opened != nil {
		_ = opened.Close()
	}
	if err == nil || !strings.Contains(err.Error(), "destination already exists") {
		t.Fatalf("conflicting partial archive error = %v", err)
	}
	database := openRuntimeLegacyDatabase(t, databasePath)
	var rows, pending int
	if err := database.QueryRow(`SELECT COUNT(*) FROM fixture_rows`).Scan(&rows); err != nil {
		_ = database.Close()
		t.Fatal(err)
	}
	if err := database.QueryRow(`SELECT COUNT(*) FROM storage_imports
		WHERE component='integration-archive-recovery' AND archive_status='pending'`).Scan(&pending); err != nil {
		_ = database.Close()
		t.Fatal(err)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}
	if rows != 2 || pending != 2 || importCalls["fixture-source-a"] != 1 ||
		importCalls["fixture-source-b"] != 1 {
		t.Fatalf("post-commit partial archive rows=%d pending=%d calls=%v", rows, pending, importCalls)
	}
	firstSource := filepath.Join(root, "a.json")
	firstArchive := filepath.Join(archiveRoot, "a.json")
	if _, err := os.Lstat(firstSource); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("first partial archive source remains: %v", err)
	}
	if first, err := os.ReadFile(firstArchive); err != nil || !bytes.Equal(first, dataByRelative["a.json"]) {
		t.Fatalf("first partial archive = %q err=%v", first, err)
	}
	if conflict, err := os.ReadFile(conflictingArchive); err != nil ||
		string(conflict) != "conflicting partial publication" {
		t.Fatalf("conflicting destination changed after failure: %q err=%v", conflict, err)
	}
	if info, err := os.Stat(conflictingArchive); err != nil ||
		(runtimeStoragePOSIXModes() && info.Mode().Perm() != 0o600) {
		t.Fatalf("conflicting destination mode changed after failure: %#v err=%v", info, err)
	}
	if err := os.Remove(conflictingArchive); err != nil {
		t.Fatal(err)
	}
	reopened, err := sqlitestore.Open(t.Context(), databasePath, options)
	if err != nil {
		t.Fatalf("archive recovery reopen: %v", err)
	}
	if err := reopened.Close(); err != nil {
		t.Fatal(err)
	}
	if importCalls["fixture-source-a"] != 1 || importCalls["fixture-source-b"] != 1 {
		t.Fatalf("archive recovery re-imported source: calls=%v", importCalls)
	}
	for relative, data := range dataByRelative {
		if _, err := os.Lstat(filepath.Join(root, relative)); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("archive recovery retained %s source: %v", relative, err)
		}
		if archived, err := os.ReadFile(filepath.Join(archiveRoot, relative)); err != nil ||
			!bytes.Equal(archived, data) {
			t.Fatalf("archive recovery %s bytes = %q err=%v", relative, archived, err)
		}
	}
	database = openRuntimeLegacyDatabase(t, databasePath)
	defer database.Close()
	var complete int
	if err := database.QueryRow(`SELECT COUNT(*) FROM storage_imports
		WHERE component='integration-archive-recovery' AND archive_status='complete'`).Scan(&complete); err != nil {
		t.Fatal(err)
	}
	if complete != 2 {
		t.Fatalf("archive recovery complete rows = %d", complete)
	}
}

//nolint:govet // Late-source assertions intentionally keep sequential error boundaries explicit.
func testRuntimeLegacyLateSourcesStayNonAuthoritative(t *testing.T) {
	t.Helper()
	workspace := filepath.Join(t.TempDir(), "late-session-authority")
	sessionsDir := filepath.Join(workspace, "sessions")
	store, err := memory.NewSQLiteStore(sessionsDir)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.AddMessage(t.Context(), "authoritative-session", "user", "keep me"); err != nil {
		_ = store.Close()
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	lateSession := filepath.Join(sessionsDir, "late.json")
	lateDelete := filepath.Join(sessionsDir, ".session-delete-v1-late.json")
	if err := os.WriteFile(lateSession, runtimeLegacyJSON(t, map[string]any{
		"key": "late-session", "messages": []map[string]any{{"role": "user", "content": "must not import"}},
		"created": time.Now().UTC(), "updated": time.Now().UTC(),
	}), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(lateDelete, runtimeLegacyJSON(t, map[string]any{
		"version": 1, "keys": []string{"authoritative-session"},
	}), 0o600); err != nil {
		t.Fatal(err)
	}
	reopened, err := memory.NewSQLiteStore(sessionsDir)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	history, err := reopened.GetHistory(t.Context(), "authoritative-session")
	if err != nil || len(history) != 1 || history[0].Content != "keep me" {
		t.Fatalf("late delete manifest changed SQLite authority: %#v err=%v", history, err)
	}
	lateHistory, err := reopened.GetHistory(t.Context(), "late-session")
	if err != nil || len(lateHistory) != 0 {
		t.Fatalf("late session became authoritative: %#v err=%v", lateHistory, err)
	}
	for _, path := range []string{lateSession, lateDelete} {
		if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("late legacy source remains: %s: %v", path, err)
		}
		relative, err := filepath.Rel(workspace, path)
		if err != nil {
			t.Fatal(err)
		}
		archive := filepath.Join(workspace, "legacy-json", "sessions-v1", relative)
		if _, err := os.Stat(archive); err != nil {
			t.Fatalf("late source archive missing: %s: %v", archive, err)
		}
	}
	var imported, skipped, lateIssues int
	if err := reopened.SQLDB().QueryRow(`SELECT COALESCE(SUM(imported_count),0),
		COALESCE(SUM(skipped_count),0) FROM storage_imports WHERE component='sessions'`).Scan(
		&imported, &skipped,
	); err != nil {
		t.Fatal(err)
	}
	if err := reopened.SQLDB().QueryRow(`SELECT COUNT(*) FROM storage_import_issues
		WHERE component='sessions' AND issue_code='late-source'`).Scan(&lateIssues); err != nil {
		t.Fatal(err)
	}
	if imported != 0 || skipped != 2 || lateIssues != 2 {
		t.Fatalf("late-source audit = imported:%d skipped:%d issues:%d", imported, skipped, lateIssues)
	}
}
