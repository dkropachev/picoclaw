package repoaudit

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/sipeed/picoclaw/pkg/sqlitestore"
)

//nolint:govet // Boundary tests intentionally keep setup and assertion errors in local scopes.
func TestRepositoryReviewSQLiteSchemaConfigurationAndReopen(t *testing.T) {
	store := newRepositoryAuditTestStore(t)
	file := repositoryAuditTestFile("pkg/sqlite.go", "a", 32)
	recorded := recordRepositoryAuditCoverage(
		t, store, "owner/sqlite", "commit-a", "inventory-a", []FileRef{file}, "sqlite-run",
	)
	profileInput := validProfileForTest("rrpf_sqlite", "SQLite")
	profileInput.ScopePolicy.IncludeFolders = []string{"pkg"}
	profileInput.ScopePolicy.ExcludeFolders = []string{"vendor"}
	profile, err := store.CreateProfile(t.Context(), profileInput)
	if err != nil {
		t.Fatal(err)
	}
	automationInput, err := MaterializeRepositoryReviewAutomation(
		profile,
		validAutomationForTest("rra_sqlite", "SQLite automation"),
	)
	if err != nil {
		t.Fatal(err)
	}
	automation, err := store.CreateAutomation(t.Context(), automationInput)
	if err != nil {
		t.Fatal(err)
	}
	database, err := store.openDatabase(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	var version, foreignKeys, synchronous int
	var journal string
	if err := database.QueryRow("PRAGMA user_version").Scan(&version); err != nil {
		t.Fatal(err)
	}
	if err := database.QueryRow("PRAGMA foreign_keys").Scan(&foreignKeys); err != nil {
		t.Fatal(err)
	}
	if err := database.QueryRow("PRAGMA synchronous").Scan(&synchronous); err != nil {
		t.Fatal(err)
	}
	if err := database.QueryRow("PRAGMA journal_mode").Scan(&journal); err != nil {
		t.Fatal(err)
	}
	if version != 1 || foreignKeys != 1 || synchronous != 2 || journal != "wal" {
		t.Fatalf("SQLite configuration version=%d fk=%d sync=%d journal=%q", version, foreignKeys, synchronous, journal)
	}
	info, err := os.Stat(filepath.Join(store.root, repositoryReviewDatabaseFilename))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("database mode=%o", info.Mode().Perm())
	}
	reopened := NewSQLiteStore(store.workspace)
	state, found, err := reopened.GetByID(recorded.State.ID)
	if err != nil || !found || !reflect.DeepEqual(state, recorded.State) {
		t.Fatalf("reopened state=%#v found=%v err=%v", state, found, err)
	}
	loadedProfile, found, err := reopened.GetProfile(t.Context(), profile.ID)
	if err != nil || !found || loadedProfile.ID != profile.ID ||
		loadedProfile.Version != profile.Version || loadedProfile.Name != profile.Name {
		t.Fatalf("reopened profile=%#v want=%#v found=%v err=%v", loadedProfile, profile, found, err)
	}
	loadedAutomation, found, err := reopened.GetAutomation(t.Context(), automation.ID)
	if err != nil || !found || !reflect.DeepEqual(loadedAutomation, automation) {
		t.Fatalf("reopened automation=%#v found=%v err=%v", loadedAutomation, found, err)
	}
}

//nolint:govet // Boundary tests intentionally keep setup and assertion errors in local scopes.
func TestRepositoryReviewSQLiteRewriteAndImporterBoundaries(t *testing.T) {
	store := newRepositoryAuditTestStore(t)
	file := repositoryAuditTestFile("pkg/rewrite.go", "d", 80)
	recorded := recordRepositoryAuditCoverage(
		t, store, "owner/rewrite", "commit-d", "inventory-d", []FileRef{file}, "rewrite-run",
	)
	profile, err := store.CreateProfile(t.Context(), validProfileForTest("rrpf_rewrite", "Rewrite"))
	if err != nil {
		t.Fatal(err)
	}
	automationInput, err := MaterializeRepositoryReviewAutomation(
		profile, validAutomationForTest("rra_rewrite", "Rewrite automation"),
	)
	if err != nil {
		t.Fatal(err)
	}
	automation, err := store.CreateAutomation(t.Context(), automationInput)
	if err != nil {
		t.Fatal(err)
	}

	state := recorded.State
	state.LastExcludedFiles = 7
	if rewritten, err := store.RewriteStateForMigration(nil, state); err != nil ||
		rewritten.LastExcludedFiles != 7 {
		t.Fatalf("state rewrite=%#v err=%v", rewritten, err)
	}
	profile.Name = "Rewritten"
	if rewritten, err := store.RewriteProfileForMigration(nil, profile); err != nil ||
		rewritten.Name != "Rewritten" {
		t.Fatalf("profile rewrite=%#v err=%v", rewritten, err)
	}
	automation.Ref = "release"
	if rewritten, err := store.RewriteAutomationForMigration(nil, automation); err != nil ||
		rewritten.Ref != "release" {
		t.Fatalf("automation rewrite=%#v err=%v", rewritten, err)
	}

	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := store.RewriteStateForMigration(canceled, state); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled state rewrite=%v", err)
	}
	if _, err := store.RewriteProfileForMigration(canceled, profile); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled profile rewrite=%v", err)
	}
	if _, err := store.RewriteAutomationForMigration(canceled, automation); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled automation rewrite=%v", err)
	}
	if _, err := store.RewriteStateForMigration(t.Context(), RepositoryState{}); err == nil {
		t.Fatal("invalid state rewrite succeeded")
	}
	invalidProfile := profile
	invalidProfile.Name = ""
	if _, err := store.RewriteProfileForMigration(t.Context(), invalidProfile); err == nil {
		t.Fatal("invalid profile rewrite succeeded")
	}
	invalidAutomation := automation
	invalidAutomation.Name = ""
	if _, err := store.RewriteAutomationForMigration(t.Context(), invalidAutomation); err == nil {
		t.Fatal("invalid automation rewrite succeeded")
	}
	missingState := state
	missingState.ID = RepositoryID("owner/missing-rewrite")
	missingState.Repository = "owner/missing-rewrite"
	if _, err := store.RewriteStateForMigration(t.Context(), missingState); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing state rewrite=%v", err)
	}
	missingProfile := profile
	missingProfile.ID = "rrpf_missing_rewrite"
	if _, err := store.RewriteProfileForMigration(t.Context(), missingProfile); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing profile rewrite=%v", err)
	}
	missingAutomation := automation
	missingAutomation.ID = "rra_missing_rewrite"
	if _, err := store.RewriteAutomationForMigration(t.Context(), missingAutomation); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing automation rewrite=%v", err)
	}
	staleRewriteState := state
	staleRewriteState.Version += 20
	if _, err := store.RewriteStateForMigration(t.Context(), staleRewriteState); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale state rewrite=%v", err)
	}
	staleRewriteProfile := profile
	staleRewriteProfile.Version += 20
	if _, err := store.RewriteProfileForMigration(t.Context(), staleRewriteProfile); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale profile rewrite=%v", err)
	}
	staleRewriteAutomation := automation
	staleRewriteAutomation.Version += 20
	if _, err := store.RewriteAutomationForMigration(
		t.Context(),
		staleRewriteAutomation,
	); !errors.Is(
		err,
		ErrConflict,
	) {
		t.Fatalf("stale automation rewrite=%v", err)
	}
	if err := prepareRepositoryStateForMigrationRewrite(nil); err == nil {
		t.Fatal("nil migration state prepared")
	}

	database, err := store.openDatabase(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	conn, err := database.Conn(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	legacyInput := func(relative string, value any) sqlitestore.LegacyInput {
		data, marshalErr := json.Marshal(value)
		if marshalErr != nil {
			t.Fatal(marshalErr)
		}
		return sqlitestore.LegacyInput{
			Relative: relative, Data: data, Digest: sha256.Sum256(data),
		}
	}
	validSummary := Summarize(state)
	if result, err := importLegacyRepositoryReviewSource(
		t.Context(), conn, legacyInput("repo_summary.summary.json", validSummary),
	); err != nil || result.Imported != 0 || result.Skipped != 0 || len(result.Issues) != 0 {
		t.Fatalf("summary import=%#v err=%v", result, err)
	}
	invalidSummary := validSummary
	invalidSummary.ID = "rrp_wrong"
	if result, err := importLegacyRepositoryReviewSource(
		t.Context(), conn, legacyInput("repo_invalid.summary.json", invalidSummary),
	); err != nil || result.Skipped != 1 || result.Issues[0].Code != "invalid_summary" {
		t.Fatalf("invalid summary=%#v err=%v", result, err)
	}
	if result, err := importLegacyRepositoryReviewSource(
		t.Context(), conn, legacyInput("repo_wrong.json", state),
	); err != nil || result.Issues[0].Code != "invalid_identity" {
		t.Fatalf("state identity=%#v err=%v", result, err)
	}
	malformedState := sqlitestore.LegacyInput{
		Relative: "repo_malformed.json", Data: []byte("{"), Digest: sha256.Sum256([]byte("{")),
	}
	if result, err := importLegacyRepositoryReviewSource(t.Context(), conn, malformedState); err != nil ||
		result.Issues[0].Code != "malformed_json" {
		t.Fatalf("malformed state=%#v err=%v", result, err)
	}
	futureState := state
	futureState.SchemaVersion = SchemaVersion + 1
	futureName := "repo_" + strings.TrimPrefix(futureState.ID, "rrp_") + ".json"
	if result, err := importLegacyRepositoryReviewSource(
		t.Context(), conn, legacyInput(futureName, futureState),
	); err != nil || result.Issues[0].Code != "invalid_record" {
		t.Fatalf("future state=%#v err=%v", result, err)
	}
	stateName := "repo_" + strings.TrimPrefix(state.ID, "rrp_") + ".json"
	if result, err := importLegacyRepositoryReviewSource(
		t.Context(), conn, legacyInput(stateName, state),
	); err != nil || result.Issues[0].Code != "duplicate_identity" {
		t.Fatalf("duplicate state=%#v err=%v", result, err)
	}
	if result, err := importLegacyRepositoryReviewSource(
		t.Context(), conn, legacyInput(profileFilename(profile.ID), profile),
	); err != nil || result.Issues[0].Code != "duplicate_identity" {
		t.Fatalf("duplicate profile=%#v err=%v", result, err)
	}
	invalidLegacyProfile := profile
	invalidLegacyProfile.ID = "rrpf_invalid_legacy"
	invalidLegacyProfile.Name = ""
	if result, err := importLegacyRepositoryReviewSource(
		t.Context(), conn, legacyInput(profileFilename(invalidLegacyProfile.ID), invalidLegacyProfile),
	); err != nil || result.Issues[0].Code != "invalid_profile" {
		t.Fatalf("invalid profile=%#v err=%v", result, err)
	}
	brokenAutomation := automation
	brokenAutomation.ID = "rra_broken_profile"
	brokenAutomation.ProfileID = "rrpf_absent"
	if result, err := importLegacyRepositoryReviewSource(
		t.Context(), conn, legacyInput(automationFilename(brokenAutomation.ID), brokenAutomation),
	); err != nil || result.Issues[0].Code != "broken_profile_reference" {
		t.Fatalf("broken automation=%#v err=%v", result, err)
	}
	if result, err := importLegacyRepositoryReviewSource(
		t.Context(), conn, legacyInput(automationFilename(automation.ID), automation),
	); err != nil || result.Issues[0].Code != "duplicate_identity" {
		t.Fatalf("duplicate automation=%#v err=%v", result, err)
	}
	invalidLegacyAutomation := automation
	invalidLegacyAutomation.ID = "rra_invalid_legacy"
	invalidLegacyAutomation.Name = ""
	if result, err := importLegacyRepositoryReviewSource(
		t.Context(), conn, legacyInput(automationFilename(invalidLegacyAutomation.ID), invalidLegacyAutomation),
	); err != nil || result.Issues[0].Code != "invalid_automation" {
		t.Fatalf("invalid automation=%#v err=%v", result, err)
	}
	if result, err := importLegacyRepositoryReviewSource(
		t.Context(), conn, legacyInput("unknown.json", map[string]any{}),
	); err != nil || result.Issues[0].Code != "unknown_source" {
		t.Fatalf("unknown source=%#v err=%v", result, err)
	}
	if err := conn.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := importLegacyRepositoryReviewSource(
		t.Context(), conn, legacyInput(stateName, state),
	); err == nil {
		t.Fatal("closed import connection succeeded")
	}
	closedProfile := profileCoverageFixture("rrpf_closed_import")
	if _, err := importLegacyRepositoryReviewSource(
		t.Context(), conn, legacyInput(profileFilename(closedProfile.ID), closedProfile),
	); err == nil {
		t.Fatal("closed profile import succeeded")
	}
	closedAutomation := validAutomationForTest("rra_closed_import", "Closed import")
	closedAutomation.SchemaVersion = RepositoryReviewAutomationSchemaVersion
	closedAutomation.Version = 1
	closedAutomation.Status = RepositoryReviewAutomationIdle
	closedAutomation.CreatedAt = automationTestNow
	closedAutomation.UpdatedAt = automationTestNow
	if err := normalizeAutomation(&closedAutomation); err != nil {
		t.Fatal(err)
	}
	withProfile := automation
	withProfile.ID = "rra_closed_profile_query"
	if _, err := importLegacyRepositoryReviewSource(
		t.Context(), conn, legacyInput(automationFilename(withProfile.ID), withProfile),
	); err == nil {
		t.Fatal("closed profile query import succeeded")
	}
	if _, err := importLegacyRepositoryReviewSource(
		t.Context(), conn, legacyInput(automationFilename(closedAutomation.ID), closedAutomation),
	); err == nil {
		t.Fatal("closed automation import succeeded")
	}
	_ = database.Close()
}

//nolint:govet // Boundary tests intentionally keep setup and assertion errors in local scopes.
func TestRepositoryReviewSQLiteLegacyDecoderDefaults(t *testing.T) {
	profile := profileCoverageFixture("rrpf_legacy_decoder")
	profile.SchemaVersion = 1
	profile.IssuePrompt = ""
	profile.DeduplicationSimilarityThreshold = 0
	profile.DeduplicationCandidateLimit = 0
	encodedProfile, err := json.Marshal(profile)
	if err != nil {
		t.Fatal(err)
	}
	var profileFields map[string]json.RawMessage
	_ = json.Unmarshal(encodedProfile, &profileFields)
	delete(profileFields, "deduplication_similarity_threshold")
	delete(profileFields, "deduplication_candidate_limit")
	encodedProfile, _ = json.Marshal(profileFields)
	decodedProfile, err := decodeLegacyRepositoryReviewProfile(profile.ID, encodedProfile)
	if err != nil || decodedProfile.SchemaVersion != RepositoryReviewProfileSchemaVersion ||
		decodedProfile.IssuePrompt != DefaultRepositoryReviewIssuePrompt ||
		decodedProfile.DeduplicationSimilarityThreshold != DeduplicationDefaultThreshold ||
		decodedProfile.DeduplicationCandidateLimit != DeduplicationDefaultCandidateLimit {
		t.Fatalf("decoded profile=%#v err=%v", decodedProfile, err)
	}
	if _, err := decodeLegacyRepositoryReviewProfile("rrpf_other", encodedProfile); err == nil {
		t.Fatal("profile identity mismatch decoded")
	}
	if _, err := decodeLegacyRepositoryReviewProfile(profile.ID, []byte("{")); err == nil {
		t.Fatal("malformed profile decoded")
	}

	automation := validAutomationForTest("rra_legacy_decoder", "Legacy")
	automation.SchemaVersion = RepositoryReviewAutomationSchemaVersion
	automation.Version = 1
	automation.Status = RepositoryReviewAutomationIdle
	automation.CreatedAt = automationTestNow
	automation.UpdatedAt = automationTestNow
	automation.Progress.Findings = 3
	if err := normalizeAutomation(&automation); err != nil {
		t.Fatal(err)
	}
	automation.SchemaVersion = 1
	encodedAutomation, _ := json.Marshal(automation)
	var automationFields map[string]json.RawMessage
	_ = json.Unmarshal(encodedAutomation, &automationFields)
	delete(automationFields, "deduplication_similarity_threshold")
	delete(automationFields, "deduplication_candidate_limit")
	var progressFields map[string]json.RawMessage
	_ = json.Unmarshal(automationFields["progress"], &progressFields)
	delete(progressFields, "deduplicated_findings")
	automationFields["progress"], _ = json.Marshal(progressFields)
	encodedAutomation, _ = json.Marshal(automationFields)
	decodedAutomation, err := decodeLegacyRepositoryReviewAutomation(automation.ID, encodedAutomation)
	if err != nil || decodedAutomation.SchemaVersion != RepositoryReviewAutomationSchemaVersion ||
		decodedAutomation.DeduplicationSimilarityThreshold != DeduplicationDefaultThreshold ||
		decodedAutomation.DeduplicationCandidateLimit != DeduplicationDefaultCandidateLimit ||
		decodedAutomation.Progress.DeduplicatedFindings != 3 {
		t.Fatalf("decoded automation=%#v err=%v", decodedAutomation, err)
	}
	if _, err := decodeLegacyRepositoryReviewAutomation("rra_other", encodedAutomation); err == nil {
		t.Fatal("automation identity mismatch decoded")
	}
	if _, err := decodeLegacyRepositoryReviewAutomation(automation.ID, []byte("{")); err == nil {
		t.Fatal("malformed automation decoded")
	}
	automationFields["name"] = json.RawMessage(`""`)
	invalidAutomation, _ := json.Marshal(automationFields)
	if _, err := decodeLegacyRepositoryReviewAutomation(automation.ID, invalidAutomation); err == nil {
		t.Fatal("invalid automation decoded")
	}
}

//nolint:govet // Boundary tests intentionally keep setup and assertion errors in local scopes.
func TestRepositoryReviewSQLiteEnumerationAndClosedConnectionBoundaries(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "missing")
	if sources, err := legacyRepositoryReviewSources(missing); err != nil || sources != nil {
		t.Fatalf("missing sources=%#v err=%v", sources, err)
	}
	fileRoot := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(fileRoot, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := legacyRepositoryReviewSources(fileRoot); err == nil {
		t.Fatal("file root enumerated")
	}
	root := t.TempDir()
	name := profileFilename("rrpf_directory")
	if err := os.Mkdir(filepath.Join(root, name), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := legacyRepositoryReviewSources(root); err == nil {
		t.Fatal("matching directory enumerated")
	}
	if err := os.Remove(filepath.Join(root, name)); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "target"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(root, "target"), filepath.Join(root, name)); err == nil {
		if _, err := legacyRepositoryReviewSources(root); err == nil {
			t.Fatal("matching symlink enumerated")
		}
	}

	store := newRepositoryAuditTestStore(t)
	if _, err := store.List(); err != nil {
		t.Fatal(err)
	}
	database, err := store.openDatabase(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	conn, err := database.Conn(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	_ = conn.Close()
	if err := validateRepositoryReviewDatabaseSchema(t.Context(), conn); err == nil {
		t.Fatal("closed schema connection validated")
	}
	if err := validateRepositoryReviewAggregateRows(t.Context(), conn); err == nil {
		t.Fatal("closed aggregate query validated")
	}
	if _, err := insertRepositoryStateConn(
		t.Context(), conn, repositoryReviewCoverageState("owner/closed"), true, 0,
	); err == nil {
		t.Fatal("closed state insert succeeded")
	}
	if _, err := insertRepositoryReviewProfileConn(
		t.Context(), conn, profileCoverageFixture("rrpf_closed"), true, 0,
	); err == nil {
		t.Fatal("closed profile insert succeeded")
	}
	if _, err := insertRepositoryReviewAutomationConn(
		t.Context(), conn, validAutomationForTest("rra_closed", "Closed"), true, 0,
	); err == nil {
		t.Fatal("closed automation insert succeeded")
	}
	if _, err := loadRepositoryStateRow(t.Context(), database, RepositoryID("owner/closed")); err == nil {
		t.Fatal("closed state load succeeded")
	}
	if _, err := loadRepositoryReviewProfileRow(t.Context(), database, "rrpf_closed"); err == nil {
		t.Fatal("closed profile load succeeded")
	}
	if _, err := loadRepositoryReviewAutomationRow(t.Context(), database, "rra_closed"); err == nil {
		t.Fatal("closed automation load succeeded")
	}
	_ = database.Close()
	loose, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := loose.Exec(
		`CREATE TABLE values_for_scan (owner, value); INSERT INTO values_for_scan VALUES ('owner', NULL)`,
	); err != nil {
		loose.Close()
		t.Fatal(err)
	}
	if _, err := loadReviewOrderedStrings(
		t.Context(), loose, `SELECT value FROM values_for_scan WHERE owner = ?`, "owner",
	); err == nil {
		t.Fatal("invalid ordered string row scanned")
	}
	_ = loose.Close()
}

//nolint:govet // Boundary tests intentionally keep setup and assertion errors in local scopes.
func TestRepositoryReviewSQLitePathAndUniqueIndexFailures(t *testing.T) {
	if runtime.GOOS != "windows" {
		original, err := os.Getwd()
		if err != nil {
			t.Fatal(err)
		}
		removed := t.TempDir()
		if err := os.Chdir(removed); err != nil {
			t.Fatal(err)
		}
		if err := os.Remove(removed); err != nil {
			t.Fatal(err)
		}
		_, openErr := (Store{root: "relative"}).openDatabase(t.Context())
		if err := os.Chdir(original); err != nil {
			t.Fatal(err)
		}
		if openErr == nil {
			t.Fatal("unresolvable database path opened")
		}
	}
	store := newRepositoryAuditTestStore(t)
	if _, err := store.List(); err != nil {
		t.Fatal(err)
	}
	database, err := store.openDatabase(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(
		`CREATE UNIQUE INDEX unexpected_state_unique ON repository_review_states(last_commit_sha)`,
	); err != nil {
		database.Close()
		t.Fatal(err)
	}
	conn, err := database.Conn(t.Context())
	if err != nil {
		database.Close()
		t.Fatal(err)
	}
	if err := validateRepositoryReviewDatabaseSchema(t.Context(), conn); err == nil {
		t.Fatal("unexpected unique index validated")
	}
	_ = conn.Close()
	_ = database.Close()
}

//nolint:govet // Boundary tests intentionally keep setup and assertion errors in local scopes.
func TestRepositoryReviewSQLiteCASAndProjectionFailureBoundaries(t *testing.T) {
	store := newRepositoryAuditTestStore(t)
	file := repositoryAuditTestFile("pkg/tamper.go", "e", 96)
	recorded := recordRepositoryAuditCoverage(
		t, store, "owner/tamper", "commit-e", "inventory-e", []FileRef{file}, "tamper-run",
	)
	profile, err := store.CreateProfile(t.Context(), validProfileForTest("rrpf_tamper", "Tamper"))
	if err != nil {
		t.Fatal(err)
	}
	automationInput, err := MaterializeRepositoryReviewAutomation(
		profile, validAutomationForTest("rra_tamper", "Tamper automation"),
	)
	if err != nil {
		t.Fatal(err)
	}
	automationInput.RunIDs = []string{"run-tamper"}
	automation, err := store.CreateAutomation(t.Context(), automationInput)
	if err != nil {
		t.Fatal(err)
	}
	database, err := store.openDatabase(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	migrationState := recorded.State
	migrationState.SchemaVersion = SchemaVersion + 1
	migrationPayload, _ := json.Marshal(migrationState)
	invalidState := recorded.State
	invalidState.Runs[0].InspectedFiles = -1
	invalidPayload, _ := json.Marshal(invalidState)

	staleState := recorded.State
	staleState.Version += 10
	if err := saveRepositoryStateDatabase(t.Context(), database, &staleState); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale state save=%v", err)
	}
	if err := saveRepositoryStateDatabase(t.Context(), database, nil); err == nil {
		t.Fatal("nil state save succeeded")
	}
	staleProfile := profile
	staleProfile.Version += 10
	if err := saveRepositoryReviewProfileDatabase(t.Context(), database, staleProfile); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale profile save=%v", err)
	}
	staleAutomation := automation
	staleAutomation.Version += 10
	if err := saveRepositoryReviewAutomationDatabase(
		t.Context(),
		database,
		staleAutomation,
	); !errors.Is(
		err,
		ErrConflict,
	) {
		t.Fatalf("stale automation save=%v", err)
	}

	for name, statement := range map[string]string{
		"state payload":      `UPDATE repository_review_states SET payload_json = X'7B'`,
		"state migration":    fmt.Sprintf(`UPDATE repository_review_states SET payload_json = X'%x'`, migrationPayload),
		"state invalid":      fmt.Sprintf(`UPDATE repository_review_states SET payload_json = X'%x'`, invalidPayload),
		"state typed":        `UPDATE repository_review_states SET finding_count = finding_count + 1`,
		"state records":      `DELETE FROM repository_review_records WHERE record_kind = 'run'`,
		"state record table": `DROP TABLE repository_review_records`,
		"state record extra": fmt.Sprintf(`INSERT INTO repository_review_records (
			state_id, record_kind, position, record_id, status, version, created_at_unix_nano, updated_at_unix_nano
		) VALUES (%q, 'run', 1, 'extra', '', 0, 0, 0)`, recorded.State.ID),
		"state record scan": fmt.Sprintf(`DROP TABLE repository_review_records;
			CREATE TABLE repository_review_records (
				state_id, record_kind, position, record_id, status, version, created_at_unix_nano, updated_at_unix_nano
			);
			INSERT INTO repository_review_records VALUES (%q, 'run', 'bad', 'rewrite-run', '', 0, 0, 0)`, recorded.State.ID),
		"profile typed":       `UPDATE repository_review_profiles SET name = ''`,
		"profile scope":       `UPDATE repository_review_profile_scope SET scope_value = 'invalid' WHERE scope_kind = 'code_type'`,
		"profile scope table": `DROP TABLE repository_review_profile_scope`,
		"profile scope scan": fmt.Sprintf(`DROP TABLE repository_review_profile_scope;
			CREATE TABLE repository_review_profile_scope (profile_id, scope_kind, position, scope_value);
			INSERT INTO repository_review_profile_scope VALUES (%q, 'code_type', 0, NULL)`, profile.ID),
		"automation payload":     `UPDATE repository_review_automations SET payload_json = X'7B'`,
		"automation typed":       `UPDATE repository_review_automations SET name = 'typed-drift'`,
		"automation models":      `DELETE FROM repository_review_automation_models`,
		"automation model table": `DROP TABLE repository_review_automation_models`,
		"automation model drift": `UPDATE repository_review_automation_models SET model_alias = 'different' WHERE position = 0`,
		"automation runs":        `UPDATE repository_review_automation_runs SET position = 3 WHERE position = 0`,
		"automation run table":   `DROP TABLE repository_review_automation_runs`,
	} {
		t.Run(name, func(t *testing.T) {
			clonePath := filepath.Join(t.TempDir(), "copy.db")
			if _, err := database.Exec("PRAGMA wal_checkpoint(TRUNCATE)"); err != nil {
				t.Fatal(err)
			}
			source, err := os.ReadFile(filepath.Join(store.root, repositoryReviewDatabaseFilename))
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(clonePath, source, 0o600); err != nil {
				t.Fatal(err)
			}
			clone, err := sql.Open("sqlite", clonePath)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := clone.Exec("PRAGMA foreign_keys = ON"); err != nil {
				clone.Close()
				t.Fatal(err)
			}
			if _, err := clone.Exec(statement); err != nil {
				clone.Close()
				t.Fatal(err)
			}
			var loadErr error
			switch {
			case strings.HasPrefix(name, "state"):
				_, loadErr = loadRepositoryStateRow(t.Context(), clone, recorded.State.ID)
			case strings.HasPrefix(name, "profile"):
				_, loadErr = loadRepositoryReviewProfileRow(t.Context(), clone, profile.ID)
			default:
				if name == "automation runs" {
					conn, connErr := clone.Conn(t.Context())
					if connErr != nil {
						clone.Close()
						t.Fatal(connErr)
					}
					loadErr = validateRepositoryReviewDatabaseSchema(t.Context(), conn)
					_ = conn.Close()
				} else {
					_, loadErr = loadRepositoryReviewAutomationRow(t.Context(), clone, automation.ID)
				}
			}
			_ = clone.Close()
			if loadErr == nil {
				t.Fatalf("%s tamper loaded", name)
			}
		})
	}
	var automationPayload []byte
	if err := database.QueryRow(
		`SELECT payload_json FROM repository_review_automations WHERE automation_id = ?`, automation.ID,
	).Scan(&automationPayload); err != nil {
		t.Fatal(err)
	}
	var invalidStoredAutomation RepositoryReviewAutomation
	if err := json.Unmarshal(automationPayload, &invalidStoredAutomation); err != nil {
		t.Fatal(err)
	}
	invalidStoredAutomation.MaxFilesPerRun = -1
	automationPayload, _ = json.Marshal(invalidStoredAutomation)
	if _, err := database.Exec(
		`UPDATE repository_review_automations SET payload_json = ? WHERE automation_id = ?`,
		automationPayload, automation.ID,
	); err != nil {
		t.Fatal(err)
	}
	if _, err := loadRepositoryReviewAutomationRow(t.Context(), database, automation.ID); err == nil {
		t.Fatal("invalid stored automation loaded")
	}
	_ = database.Close()
}

func TestRepositoryReviewSQLiteRelationshipWriteFailures(t *testing.T) {
	t.Run("state payload", func(t *testing.T) {
		state := repositoryReviewCoverageState("owner/bad-time")
		state.UpdatedAt = time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC)
		if _, err := insertRepositoryStateConn(t.Context(), nil, state, true, 0); err == nil {
			t.Fatal("unencodable state inserted")
		}
	})
	t.Run("automation payload", func(t *testing.T) {
		automation := validAutomationForTest("rra_bad_time", "Bad time")
		automation.SchemaVersion = RepositoryReviewAutomationSchemaVersion
		automation.Version = 1
		automation.Status = RepositoryReviewAutomationIdle
		automation.CreatedAt = time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC)
		automation.UpdatedAt = automation.CreatedAt
		if _, err := insertRepositoryReviewAutomationConn(
			t.Context(), nil, automation, true, 0,
		); err == nil {
			t.Fatal("unencodable automation inserted")
		}
	})

	for name, setup := range map[string]func(*testing.T, Store, *sql.DB, *sql.Conn){
		"state delete": func(t *testing.T, store Store, database *sql.DB, conn *sql.Conn) {
			file := repositoryAuditTestFile("pkg/delete.go", "f", 1)
			state := recordRepositoryAuditCoverage(t, store, "owner/delete", "c", "i", []FileRef{file}, "r").State
			if _, err := database.Exec("DROP TABLE repository_review_records"); err != nil {
				t.Fatal(err)
			}
			if _, err := insertRepositoryStateConn(t.Context(), conn, state, false, state.Version); err == nil {
				t.Fatal("state child delete failure ignored")
			}
		},
		"state insert": func(t *testing.T, store Store, database *sql.DB, conn *sql.Conn) {
			file := repositoryAuditTestFile("pkg/insert.go", "a", 1)
			state := recordRepositoryAuditCoverage(t, store, "owner/insert", "c", "i", []FileRef{file}, "r").State
			if _, err := database.Exec(`CREATE TRIGGER reject_records BEFORE INSERT ON repository_review_records BEGIN SELECT RAISE(FAIL, 'reject'); END`); err != nil {
				t.Fatal(err)
			}
			if _, err := insertRepositoryStateConn(t.Context(), conn, state, false, state.Version); err == nil {
				t.Fatal("state child insert failure ignored")
			}
		},
		"profile delete": func(t *testing.T, store Store, database *sql.DB, conn *sql.Conn) {
			profile, err := store.CreateProfile(t.Context(), validProfileForTest("rrpf_delete_child", "Delete"))
			if err != nil {
				t.Fatal(err)
			}
			if _, err := database.Exec("DROP TABLE repository_review_profile_scope"); err != nil {
				t.Fatal(err)
			}
			if _, err := insertRepositoryReviewProfileConn(t.Context(), conn, profile, false, profile.Version); err == nil {
				t.Fatal("profile child delete failure ignored")
			}
		},
		"profile insert": func(t *testing.T, store Store, database *sql.DB, conn *sql.Conn) {
			profile, err := store.CreateProfile(t.Context(), validProfileForTest("rrpf_insert_child", "Insert"))
			if err != nil {
				t.Fatal(err)
			}
			if _, err := database.Exec(`CREATE TRIGGER reject_scope BEFORE INSERT ON repository_review_profile_scope BEGIN SELECT RAISE(FAIL, 'reject'); END`); err != nil {
				t.Fatal(err)
			}
			if _, err := insertRepositoryReviewProfileConn(t.Context(), conn, profile, false, profile.Version); err == nil {
				t.Fatal("profile child insert failure ignored")
			}
		},
		"automation models delete": func(t *testing.T, store Store, database *sql.DB, conn *sql.Conn) {
			automation, err := store.CreateAutomation(t.Context(), validAutomationForTest("rra_models_delete", "Delete"))
			if err != nil {
				t.Fatal(err)
			}
			if _, err := database.Exec("DROP TABLE repository_review_automation_models"); err != nil {
				t.Fatal(err)
			}
			if _, err := insertRepositoryReviewAutomationConn(t.Context(), conn, automation, false, automation.Version); err == nil {
				t.Fatal("automation model delete failure ignored")
			}
		},
		"automation models insert": func(t *testing.T, store Store, database *sql.DB, conn *sql.Conn) {
			automation, err := store.CreateAutomation(t.Context(), validAutomationForTest("rra_models_insert", "Insert"))
			if err != nil {
				t.Fatal(err)
			}
			if _, err := database.Exec(`CREATE TRIGGER reject_models BEFORE INSERT ON repository_review_automation_models BEGIN SELECT RAISE(FAIL, 'reject'); END`); err != nil {
				t.Fatal(err)
			}
			if _, err := insertRepositoryReviewAutomationConn(t.Context(), conn, automation, false, automation.Version); err == nil {
				t.Fatal("automation model insert failure ignored")
			}
		},
		"automation runs delete": func(t *testing.T, store Store, database *sql.DB, conn *sql.Conn) {
			automation := validAutomationForTest("rra_runs_delete", "Delete")
			automation.RunIDs = []string{"run"}
			created, err := store.CreateAutomation(t.Context(), automation)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := database.Exec("DROP TABLE repository_review_automation_runs"); err != nil {
				t.Fatal(err)
			}
			if _, err := insertRepositoryReviewAutomationConn(t.Context(), conn, created, false, created.Version); err == nil {
				t.Fatal("automation run delete failure ignored")
			}
		},
		"automation runs insert": func(t *testing.T, store Store, database *sql.DB, conn *sql.Conn) {
			automation := validAutomationForTest("rra_runs_insert", "Insert")
			automation.RunIDs = []string{"run"}
			created, err := store.CreateAutomation(t.Context(), automation)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := database.Exec(`CREATE TRIGGER reject_runs BEFORE INSERT ON repository_review_automation_runs BEGIN SELECT RAISE(FAIL, 'reject'); END`); err != nil {
				t.Fatal(err)
			}
			if _, err := insertRepositoryReviewAutomationConn(t.Context(), conn, created, false, created.Version); err == nil {
				t.Fatal("automation run insert failure ignored")
			}
		},
	} {
		t.Run(name, func(t *testing.T) {
			store := newRepositoryAuditTestStore(t)
			database, err := store.openDatabase(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			conn, err := database.Conn(t.Context())
			if err != nil {
				database.Close()
				t.Fatal(err)
			}
			setup(t, store, database, conn)
			_ = conn.Close()
			_ = database.Close()
		})
	}
}

func TestRepositoryReviewSQLiteMigrationRewriteFailureBoundaries(t *testing.T) {
	store := newRepositoryAuditTestStore(t)
	file := repositoryAuditTestFile("pkg/generic.go", "1", 1)
	state := recordRepositoryAuditCoverage(
		t, store, "owner/generic", "commit", "inventory", []FileRef{file}, "run",
	).State
	query := `SELECT version FROM repository_review_states WHERE state_id = ?`
	if err := store.rewriteMigrationRow(t.Context(), "generic", "", state.ID, state.Version, nil); err == nil {
		t.Fatal("invalid migration rewrite succeeded")
	}
	want := errors.New("write failed")
	if err := store.rewriteMigrationRow(
		t.Context(), "generic", query, state.ID, state.Version,
		func(context.Context, *sql.Conn, int64) (bool, error) { return false, want },
	); !errors.Is(err, want) {
		t.Fatalf("write error=%v", err)
	}
	if err := store.rewriteMigrationRow(
		t.Context(), "generic", query, state.ID, state.Version,
		func(context.Context, *sql.Conn, int64) (bool, error) { return false, nil },
	); !errors.Is(err, ErrConflict) {
		t.Fatalf("ignored rewrite error=%v", err)
	}
	if err := store.rewriteMigrationRow(
		t.Context(), "generic", `SELECT missing FROM absent`, state.ID, state.Version,
		func(context.Context, *sql.Conn, int64) (bool, error) { return true, nil },
	); err == nil {
		t.Fatal("rewrite query error ignored")
	}

	lockStore := NewSQLiteStore(t.TempDir())
	if err := os.Mkdir(lockStore.root+".lock", 0o700); err != nil {
		t.Fatal(err)
	}
	if err := lockStore.rewriteMigrationRow(
		t.Context(), "generic", query, state.ID, state.Version,
		func(context.Context, *sql.Conn, int64) (bool, error) { return true, nil },
	); err == nil {
		t.Fatal("rewrite lock error ignored")
	}
	openStore := NewSQLiteStore(t.TempDir())
	if err := os.WriteFile(openStore.root, []byte("not-a-directory"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := openStore.rewriteMigrationRow(
		t.Context(), "generic", query, state.ID, state.Version,
		func(context.Context, *sql.Conn, int64) (bool, error) { return true, nil },
	); err == nil {
		t.Fatal("rewrite open error ignored")
	}
}

func TestRepositoryReviewSQLiteSaveConflictAndQueryFailures(t *testing.T) {
	for name, run := range map[string]func(*testing.T, Store, *sql.DB){
		"state query": func(t *testing.T, store Store, database *sql.DB) {
			state := repositoryReviewCoverageState("owner/query")
			if _, err := database.Exec("DROP TABLE repository_review_states"); err != nil {
				t.Fatal(err)
			}
			if err := saveRepositoryStateDatabase(t.Context(), database, &state); err == nil {
				t.Fatal("state query failure ignored")
			}
		},
		"state insert error": func(t *testing.T, store Store, database *sql.DB) {
			state := repositoryReviewCoverageState("owner/insert-error")
			if _, err := database.Exec(`CREATE TRIGGER reject_state_insert BEFORE INSERT ON repository_review_states BEGIN SELECT RAISE(FAIL, 'reject'); END`); err != nil {
				t.Fatal(err)
			}
			if err := saveRepositoryStateDatabase(t.Context(), database, &state); err == nil {
				t.Fatal("state insert error ignored")
			}
		},
		"state insert ignored": func(t *testing.T, store Store, database *sql.DB) {
			state := repositoryReviewCoverageState("owner/insert-ignore")
			if _, err := database.Exec(`CREATE TRIGGER ignore_state_insert BEFORE INSERT ON repository_review_states BEGIN SELECT RAISE(IGNORE); END`); err != nil {
				t.Fatal(err)
			}
			if err := saveRepositoryStateDatabase(t.Context(), database, &state); !errors.Is(err, ErrConflict) {
				t.Fatalf("ignored state insert=%v", err)
			}
		},
		"state update error": func(t *testing.T, store Store, database *sql.DB) {
			file := repositoryAuditTestFile("pkg/state.go", "2", 2)
			state := recordRepositoryAuditCoverage(t, store, "owner/update-error", "c", "i", []FileRef{file}, "r").State
			if _, err := database.Exec(`CREATE TRIGGER reject_state_update BEFORE UPDATE ON repository_review_states BEGIN SELECT RAISE(FAIL, 'reject'); END`); err != nil {
				t.Fatal(err)
			}
			if err := saveRepositoryStateDatabase(t.Context(), database, &state); err == nil {
				t.Fatal("state update error ignored")
			}
		},
		"state update ignored": func(t *testing.T, store Store, database *sql.DB) {
			file := repositoryAuditTestFile("pkg/state.go", "3", 3)
			state := recordRepositoryAuditCoverage(t, store, "owner/update-ignore", "c", "i", []FileRef{file}, "r").State
			if _, err := database.Exec(`CREATE TRIGGER ignore_state_update BEFORE UPDATE ON repository_review_states BEGIN SELECT RAISE(IGNORE); END`); err != nil {
				t.Fatal(err)
			}
			if err := saveRepositoryStateDatabase(t.Context(), database, &state); !errors.Is(err, ErrConflict) {
				t.Fatalf("ignored state update=%v", err)
			}
		},
		"profile query": func(t *testing.T, store Store, database *sql.DB) {
			if _, err := database.Exec("DROP TABLE repository_review_profiles"); err != nil {
				t.Fatal(err)
			}
			if err := saveRepositoryReviewProfileDatabase(t.Context(), database, profileCoverageFixture("rrpf_query")); err == nil {
				t.Fatal("profile query failure ignored")
			}
		},
		"profile ignored": func(t *testing.T, store Store, database *sql.DB) {
			profile, err := store.CreateProfile(t.Context(), validProfileForTest("rrpf_ignore", "Ignore"))
			if err != nil {
				t.Fatal(err)
			}
			if _, err := database.Exec(`CREATE TRIGGER ignore_profile_update BEFORE UPDATE ON repository_review_profiles BEGIN SELECT RAISE(IGNORE); END`); err != nil {
				t.Fatal(err)
			}
			if err := saveRepositoryReviewProfileDatabase(t.Context(), database, profile); !errors.Is(err, ErrConflict) {
				t.Fatalf("ignored profile update=%v", err)
			}
		},
		"profile insert error": func(t *testing.T, store Store, database *sql.DB) {
			if _, err := database.Exec(`CREATE TRIGGER reject_profile_insert BEFORE INSERT ON repository_review_profiles BEGIN SELECT RAISE(FAIL, 'reject'); END`); err != nil {
				t.Fatal(err)
			}
			if err := saveRepositoryReviewProfileDatabase(
				t.Context(), database, profileCoverageFixture("rrpf_insert_error"),
			); err == nil {
				t.Fatal("profile insert error ignored")
			}
		},
		"automation query": func(t *testing.T, store Store, database *sql.DB) {
			if _, err := database.Exec("DROP TABLE repository_review_automations"); err != nil {
				t.Fatal(err)
			}
			automation := validAutomationForTest("rra_query", "Query")
			automation.SchemaVersion = RepositoryReviewAutomationSchemaVersion
			automation.Version = 1
			automation.Status = RepositoryReviewAutomationIdle
			automation.CreatedAt = automationTestNow
			automation.UpdatedAt = automationTestNow
			if err := saveRepositoryReviewAutomationDatabase(t.Context(), database, automation); err == nil {
				t.Fatal("automation query failure ignored")
			}
		},
		"automation ignored": func(t *testing.T, store Store, database *sql.DB) {
			automation, err := store.CreateAutomation(t.Context(), validAutomationForTest("rra_ignore", "Ignore"))
			if err != nil {
				t.Fatal(err)
			}
			if _, err := database.Exec(`CREATE TRIGGER ignore_automation_update BEFORE UPDATE ON repository_review_automations BEGIN SELECT RAISE(IGNORE); END`); err != nil {
				t.Fatal(err)
			}
			if err := saveRepositoryReviewAutomationDatabase(t.Context(), database, automation); !errors.Is(err, ErrConflict) {
				t.Fatalf("ignored automation update=%v", err)
			}
		},
	} {
		t.Run(name, func(t *testing.T) {
			store := newRepositoryAuditTestStore(t)
			database, err := store.openDatabase(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			run(t, store, database)
			_ = database.Close()
		})
	}
}

func TestRepositoryReviewSQLiteFacadesPropagateDatabaseFailure(t *testing.T) {
	type fixture struct {
		store      Store
		state      RepositoryState
		profile    RepositoryReviewProfile
		automation RepositoryReviewAutomation
	}
	newFixture := func(t *testing.T) fixture {
		t.Helper()
		store := newRepositoryAuditTestStore(t)
		file := repositoryAuditTestFile("pkg/failure.go", "6", 6)
		state := recordRepositoryAuditCoverage(
			t, store, "owner/failure", "commit", "inventory", []FileRef{file}, "run",
		).State
		profile, err := store.CreateProfile(t.Context(), validProfileForTest("rrpf_failure", "Failure"))
		if err != nil {
			t.Fatal(err)
		}
		automationInput, err := MaterializeRepositoryReviewAutomation(
			profile, validAutomationForTest("rra_failure", "Failure"),
		)
		if err != nil {
			t.Fatal(err)
		}
		automation, err := store.CreateAutomation(t.Context(), automationInput)
		if err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(store.root, repositoryReviewDatabaseFilename)
		if err := os.Remove(path); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("not-sqlite"), 0o600); err != nil {
			t.Fatal(err)
		}
		return fixture{store: store, state: state, profile: profile, automation: automation}
	}
	tests := map[string]func(*testing.T, fixture){
		"list states":    func(t *testing.T, f fixture) { _, err := f.store.List(); requireSQLiteFailure(t, err) },
		"list summaries": func(t *testing.T, f fixture) { _, err := f.store.ListSummaries(); requireSQLiteFailure(t, err) },
		"get id":         func(t *testing.T, f fixture) { _, _, err := f.store.GetByID(f.state.ID); requireSQLiteFailure(t, err) },
		"get repository": func(t *testing.T, f fixture) {
			_, _, err := f.store.Get(f.state.Repository)
			requireSQLiteFailure(t, err)
		},
		"save state": func(t *testing.T, f fixture) { requireSQLiteFailure(t, f.store.save(&f.state)) },
		"list profiles": func(t *testing.T, f fixture) {
			_, err := f.store.listProfilesUnlocked(maxProfileCount)
			requireSQLiteFailure(t, err)
		},
		"load profile": func(t *testing.T, f fixture) {
			_, _, err := f.store.loadProfile(f.profile.ID)
			requireSQLiteFailure(t, err)
		},
		"save profile": func(t *testing.T, f fixture) { requireSQLiteFailure(t, f.store.saveProfile(f.profile)) },
		"profile assigned": func(t *testing.T, f fixture) {
			_, err := f.store.profileAssignedUnlocked(f.profile.ID)
			requireSQLiteFailure(t, err)
		},
		"profile active": func(t *testing.T, f fixture) {
			_, err := f.store.profileActiveUnlocked(f.profile.ID)
			requireSQLiteFailure(t, err)
		},
		"create profile": func(t *testing.T, f fixture) {
			_, err := f.store.CreateProfile(t.Context(), validProfileForTest("rrpf_new_failure", "New"))
			requireSQLiteFailure(t, err)
		},
		"update profile": func(t *testing.T, f fixture) {
			_, err := f.store.UpdateProfile(
				t.Context(),
				f.profile.ID,
				f.profile.Version,
				func(*RepositoryReviewProfile) error { return nil },
			)
			requireSQLiteFailure(t, err)
		},
		"delete profile": func(t *testing.T, f fixture) {
			requireSQLiteFailure(t, f.store.DeleteProfile(t.Context(), f.profile.ID, f.profile.Version))
		},
		"list automations": func(t *testing.T, f fixture) {
			_, err := f.store.listAutomationsUnlocked(maxAutomationCount)
			requireSQLiteFailure(t, err)
		},
		"load automation": func(t *testing.T, f fixture) {
			_, _, err := f.store.loadAutomation(f.automation.ID)
			requireSQLiteFailure(t, err)
		},
		"save automation": func(t *testing.T, f fixture) { requireSQLiteFailure(t, f.store.saveAutomation(f.automation)) },
		"automation unique": func(t *testing.T, f fixture) {
			requireSQLiteFailure(t, f.store.ensureRepositoryAutomationUniqueUnlocked("rra_other", "owner/other"))
		},
		"profile snapshot": func(t *testing.T, f fixture) {
			requireSQLiteFailure(t, f.store.validateAutomationProfileSnapshotUnlocked(f.automation))
		},
		"create automation": func(t *testing.T, f fixture) {
			_, err := f.store.CreateAutomation(t.Context(), validAutomationForTest("rra_new_failure", "New"))
			requireSQLiteFailure(t, err)
		},
		"update automation": func(t *testing.T, f fixture) {
			_, err := f.store.UpdateAutomation(
				t.Context(),
				f.automation.ID,
				f.automation.Version,
				func(*RepositoryReviewAutomation) error { return nil },
			)
			requireSQLiteFailure(t, err)
		},
		"delete automation": func(t *testing.T, f fixture) {
			requireSQLiteFailure(t, f.store.DeleteAutomation(t.Context(), f.automation.ID, f.automation.Version))
		},
	}
	for name, run := range tests {
		t.Run(name, func(t *testing.T) { run(t, newFixture(t)) })
	}
}

func requireSQLiteFailure(t *testing.T, err error) {
	t.Helper()
	if err == nil {
		t.Fatal("corrupt SQLite database operation succeeded")
	}
}

func TestRepositoryReviewSQLiteCatalogQueryAndScanFailures(t *testing.T) {
	for name, test := range map[string]struct {
		setup string
		call  func(Store) error
	}{
		"automation query": {call: func(store Store) error { _, err := store.listAutomationsUnlocked(maxAutomationCount); return err }},
		"automation scan": {
			setup: `CREATE TABLE repository_review_automations (automation_id, updated_at_unix_nano); INSERT INTO repository_review_automations VALUES (NULL, 0)`,
			call:  func(store Store) error { _, err := store.listAutomationsUnlocked(maxAutomationCount); return err },
		},
		"profile query": {call: func(store Store) error { _, err := store.listProfilesUnlocked(maxProfileCount); return err }},
		"profile scan": {
			setup: `CREATE TABLE repository_review_profiles (profile_id, updated_at_unix_nano); INSERT INTO repository_review_profiles VALUES (NULL, 0)`,
			call:  func(store Store) error { _, err := store.listProfilesUnlocked(maxProfileCount); return err },
		},
		"state query": {call: func(store Store) error { _, err := store.List(); return err }},
		"state scan": {
			setup: `CREATE TABLE repository_review_states (state_id, updated_at_unix_nano); INSERT INTO repository_review_states VALUES (NULL, 0)`,
			call:  func(store Store) error { _, err := store.List(); return err },
		},
		"summary query": {call: func(store Store) error { _, err := store.ListSummaries(); return err }},
		"summary scan": {
			setup: `CREATE TABLE repository_review_states (
				schema_version, state_id, repository, version, review_version, last_commit_sha,
				finding_count, repository_finding_count, open_finding_count, issue_draft_count,
				unsupported_count, reviewed_file_count, excluded_file_count, updated_at_unix_nano
			); INSERT INTO repository_review_states VALUES (NULL,NULL,NULL,NULL,NULL,NULL,NULL,NULL,NULL,NULL,NULL,NULL,NULL,NULL)`,
			call: func(store Store) error { _, err := store.ListSummaries(); return err },
		},
	} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "raw.db")
			if test.setup != "" {
				database, err := sql.Open("sqlite", path)
				if err != nil {
					t.Fatal(err)
				}
				if _, err := database.Exec(test.setup); err != nil {
					database.Close()
					t.Fatal(err)
				}
				_ = database.Close()
			}
			store := NewSQLiteStore(t.TempDir())
			store.openForTest = func(context.Context) (*sql.DB, error) { return sql.Open("sqlite", path) }
			if err := test.call(store); err == nil {
				t.Fatalf("%s succeeded", name)
			}
		})
	}
}

//nolint:govet // Boundary tests intentionally keep setup and assertion errors in local scopes.
func TestRepositoryReviewSQLiteDeleteFailureBoundaries(t *testing.T) {
	for _, kind := range []string{"profile", "automation"} {
		for _, mode := range []string{"open", "reject", "ignore"} {
			t.Run(kind+" "+mode, func(t *testing.T) {
				store := newRepositoryAuditTestStore(t)
				profile, err := store.CreateProfile(t.Context(), validProfileForTest("rrpf_delete_boundary", "Delete"))
				if err != nil {
					t.Fatal(err)
				}
				var automation RepositoryReviewAutomation
				if kind == "automation" {
					automation, err = store.CreateAutomation(
						t.Context(),
						validAutomationForTest("rra_delete_boundary", "Delete"),
					)
					if err != nil {
						t.Fatal(err)
					}
				}
				path := filepath.Join(store.root, repositoryReviewDatabaseFilename)
				if mode != "open" {
					database, err := sql.Open("sqlite", path)
					if err != nil {
						t.Fatal(err)
					}
					table := "repository_review_profiles"
					if kind == "automation" {
						table = "repository_review_automations"
					}
					action := "FAIL, 'reject'"
					if mode == "ignore" {
						action = "IGNORE"
					}
					if _, err := database.Exec(fmt.Sprintf(
						`CREATE TRIGGER delete_boundary BEFORE DELETE ON %s BEGIN SELECT RAISE(%s); END`, table, action,
					)); err != nil {
						database.Close()
						t.Fatal(err)
					}
					_ = database.Close()
				}
				calls := 0
				store.openForTest = func(context.Context) (*sql.DB, error) {
					calls++
					openFailureCall := 2
					if kind == "profile" {
						openFailureCall = 3
					}
					if mode == "open" && calls == openFailureCall {
						return nil, errors.New("open failed")
					}
					return sql.Open("sqlite", path)
				}
				if kind == "profile" {
					err = store.DeleteProfile(t.Context(), profile.ID, profile.Version)
				} else {
					err = store.DeleteAutomation(t.Context(), automation.ID, automation.Version)
				}
				if err == nil {
					t.Fatal("delete boundary succeeded")
				}
			})
		}
	}
}

func TestRepositoryReviewSQLiteProfileAndStateResidualBoundaries(t *testing.T) {
	profile := profileCoverageFixture("rrpf_bad_timestamp")
	profile.CreatedAt = time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC)
	profile.UpdatedAt = profile.CreatedAt
	if err := NewSQLiteStore(t.TempDir()).saveProfile(profile); err == nil {
		t.Fatal("unencodable profile saved")
	}
	if err := NewSQLiteStore(
		t.TempDir(),
	).ensureRepositoryAutomationUniqueUnlocked("rra_blank", ""); !errors.Is(
		err,
		ErrInvalidAutomation,
	) {
		t.Fatalf("blank repository uniqueness=%v", err)
	}
	if err := prepareRepositoryStateForPersistence(nil); err == nil {
		t.Fatal("nil repository state prepared")
	}
	blocked := NewSQLiteStore(t.TempDir())
	if err := os.WriteFile(blocked.root, []byte("not-a-directory"), 0o600); err != nil {
		t.Fatal(err)
	}
	state := repositoryReviewCoverageState("owner/blocked-save")
	if err := blocked.save(&state); err == nil {
		t.Fatal("blocked state save succeeded")
	}
	store := newRepositoryAuditTestStore(t)
	file := repositoryAuditTestFile("pkg/residual.go", "8", 8)
	_ = recordRepositoryAuditCoverage(
		t, store, "owner/residual", "commit", "inventory", []FileRef{file}, "run",
	)
	if _, found, err := store.GetByID("invalid"); err != nil || found {
		t.Fatalf("invalid GetByID found=%v err=%v", found, err)
	}
	if _, found, err := store.GetByID(RepositoryID("owner/absent")); err != nil || found {
		t.Fatalf("missing GetByID found=%v err=%v", found, err)
	}
	if _, err := store.listSummaries(0); err == nil {
		t.Fatal("zero-bound summary list succeeded")
	}
	_, invalidAssociation := repositoryReviewIssueState(t, 1)
	invalidAssociation.Findings[0].IssueDraftID = "missing"
	if err := validateState(invalidAssociation); err == nil {
		t.Fatal("invalid issue association validated")
	}
	if legacyIssueDraftPriority(IssueDraftPublishing) != 2 {
		t.Fatal("publishing legacy priority changed")
	}
	priorityState := invalidAssociation
	priorityState.IssueDrafts = []IssueDraft{
		{
			ID:         "editing",
			Origin:     IssueDraftOriginLegacy,
			State:      IssueDraftEditing,
			FindingIDs: []string{priorityState.Findings[0].ID},
		},
		{
			ID:         "posted",
			Origin:     IssueDraftOriginLegacy,
			State:      IssueDraftPosted,
			FindingIDs: []string{priorityState.Findings[0].ID},
		},
	}
	backfillCanonicalIssueAssociations(&priorityState)
	if _, err := repositoryReviewFileAttributionCreditCandidates(
		make([]RepositoryReviewFileAttribution, maxRepositoryReviewFileAttributions+1),
	); !errors.Is(err, ErrInvalidPlan) {
		t.Fatalf("oversized attribution candidates=%v", err)
	}
	creditFixture := newRepositoryReviewAttributionCreditFixture(t)
	firstAssignment := creditFixture.state.CurrentCampaign.AssignmentCatalog[0]
	duplicateAssignment, err := NewRepositoryReviewAssignment(
		firstAssignment.FocusID, firstAssignment.Reviewer,
		firstAssignment.PromptRevision+"-duplicate", firstAssignment.ProfileHash,
		firstAssignment.Required,
	)
	if err != nil {
		t.Fatal(err)
	}
	creditFixture.state.CurrentCampaign.AssignmentCatalog = append(
		creditFixture.state.CurrentCampaign.AssignmentCatalog,
		duplicateAssignment,
	)
	if _, err := PreviewRepositoryReviewFileAttributionCredits(
		creditFixture.state, creditFixture.fence, creditFixture.attributions,
	); !errors.Is(err, ErrConflict) {
		t.Fatalf("ambiguous assignment credit=%v", err)
	}
	creditFixture = newRepositoryReviewAttributionCreditFixture(t)
	targetPath := creditFixture.attributions[0].AcknowledgedFiles[0].Path
	invalidCoverage := creditFixture.state.CurrentCampaign.Paths[targetPath]
	invalidCoverage.AssignmentBits = "***"
	creditFixture.state.CurrentCampaign.Paths[targetPath] = invalidCoverage
	if _, err := PreviewRepositoryReviewFileAttributionCredits(
		creditFixture.state, creditFixture.fence, creditFixture.attributions,
	); err == nil {
		t.Fatal("invalid assignment bits received attribution credit")
	}
	conflictingAttribution := creditFixture.attributions[0]
	conflictingAttribution.UsageModel = "different-model"
	if _, err := repositoryReviewFileAttributionCreditCandidates([]RepositoryReviewFileAttribution{
		creditFixture.attributions[0], conflictingAttribution,
	}); !errors.Is(err, ErrConflict) {
		t.Fatalf("conflicting attribution candidates=%v", err)
	}
	creditFixture = newRepositoryReviewAttributionCreditFixture(t)
	wrongFence := creditFixture.fence
	wrongFence.CampaignID = NewRepositoryReviewCampaignID()
	if _, err := creditFixture.store.MergeRepositoryReviewFileAttributions(
		t.Context(), MergeRepositoryReviewFileAttributionsRequest{
			Repository: creditFixture.repository, ExpectedVersion: creditFixture.state.Version,
			Attributions: creditFixture.attributions, CampaignCredit: &wrongFence,
		},
	); !errors.Is(err, ErrConflict) {
		t.Fatalf("mismatched attribution credit fence=%v", err)
	}
	dedupFixture := dedupDeepPendingFixture(t, 1)
	dedupState := dedupDeepState(t, dedupFixture)
	if len(dedupState.DeduplicatedFindings) == 0 {
		dedupState.DeduplicatedFindings = []DeduplicatedReviewFinding{{
			ID: "rdf_invalid", Version: 1, CampaignID: dedupFixture.campaignID,
			AdmissionBucket: "bucket", CreationOrdinal: 1,
			DiagnosisDigest: "sha256:" + strings.Repeat("a", 64),
			Repository:      dedupFixture.repository, CommitSHA: strings.Repeat("a", 40),
			File: dedupFixture.files[0], RawSourceIDs: []string{"missing"},
			Status: FindingOpen, RepositoryFindingID: "missing",
			RepositoryMatchState: RepositoryMatchKnown,
			CreatedAt:            repositoryAuditTestNow, UpdatedAt: repositoryAuditTestNow,
		}}
	} else {
		dedupState.DeduplicatedFindings[0].RepositoryFindingID = "missing"
		dedupState.DeduplicatedFindings[0].RepositoryMatchState = RepositoryMatchKnown
	}
	if err := validateDeduplicationState(dedupState); err == nil {
		t.Fatal("missing repository finding deduplication reference validated")
	}
}

func TestRepositoryReviewSQLiteReconcilePropagatesSemanticStateFailure(t *testing.T) {
	store, request, _ := repositoryReviewCampaignReconcileFixture(t, "owner/sqlite-reconcile")
	want := errors.New("semantic state failure")
	store.loadForTest = func(string) (RepositoryState, error) {
		return RepositoryState{}, want
	}
	if _, err := store.ReconcileCampaign(t.Context(), request); !errors.Is(err, want) {
		t.Fatalf("semantic campaign reconciliation error=%v", err)
	}
}

func TestRepositoryReviewSQLiteRetryStatusSaveFailure(t *testing.T) {
	store := newRepositoryAuditTestStore(t)
	state := recordMappingWorkerFinding(
		t, store, "sqlite-status-save", strings.Repeat("9", 40), "status.go", "status.retry",
	)
	findingID := state.Findings[len(state.Findings)-1].ID
	for index := range state.MappingJobs {
		if state.MappingJobs[index].ReviewFindingID == findingID {
			state.MappingJobs[index].Attempts = RepositoryRunFindingStatusAttemptLimit
			state.MappingJobs[index].Error = "failed"
		}
	}
	failure := store
	failure.loadForTest = func(string) (RepositoryState, error) { return state, nil }
	failure.openForTest = func(context.Context) (*sql.DB, error) {
		return nil, errors.New("save failed")
	}
	if _, _, err := failure.RetryRunFindingStatus(
		state.Repository, []string{findingID},
	); err == nil {
		t.Fatal("retry status ignored SQLite save failure")
	}
}

func TestRepositoryReviewSQLiteSnapshotMappingSaveFailure(t *testing.T) {
	store := newRepositoryAuditTestStore(t)
	state, finding := recordLifecycleFinding(
		t, store, strings.Repeat("a", 40), strings.Repeat("b", 40), "sqlite-snapshot-run",
		"main", "main", true, "snapshot failure",
	)
	failure := store
	failure.loadForTest = func(string) (RepositoryState, error) { return state, nil }
	failure.openForTest = func(context.Context) (*sql.DB, error) {
		return nil, errors.New("save failed")
	}
	if _, err := failure.SnapshotMappingJobs(
		state.Repository, []string{finding.ID}, RepositoryMappingModelSnapshot{Model: "reviewer"},
	); err == nil {
		t.Fatal("mapping snapshot ignored SQLite save failure")
	}
}

func TestRepositoryReviewSQLiteLegacyRawIdentityMigration(t *testing.T) {
	store := newRepositoryAuditTestStore(t)
	state, _ := recordLifecycleFinding(
		t, store, strings.Repeat("a", 40), strings.Repeat("b", 40), "legacy-sqlite-run",
		"main", "main", true, "legacy sqlite identity",
	)
	oldParentID := "rfn_sqlite_compatibility"
	newRawID := state.RawFindings[0].ID
	oldRawID := "rrl_" + strings.TrimPrefix(newRawID, "rrw_")
	originalParentID := state.DeduplicatedFindings[0].ID
	raw := &state.RawFindings[0]
	raw.ID = oldRawID
	raw.LegacyFindingID = ""
	raw.DeduplicatedFindingID = oldParentID
	for index := range raw.History {
		raw.History[index].DeduplicatedFindingID = oldParentID
	}
	raw.DiagnosisDigest = RawReviewFindingDiagnosisDigest(*raw)
	deduplicated := &state.DeduplicatedFindings[0]
	deduplicated.ID = oldParentID
	deduplicated.DiagnosisDigest = raw.DiagnosisDigest
	deduplicated.RawSourceIDs[0] = oldRawID
	for index := range deduplicated.History {
		deduplicated.History[index].RawFindingID = oldRawID
	}
	for index := range state.Findings {
		if state.Findings[index].ID == originalParentID {
			state.Findings[index].ID = oldParentID
		}
	}
	for index := range state.DeduplicationJobs {
		state.DeduplicationJobs[index].RawFindingID = oldRawID
	}
	for index := range state.MappingJobs {
		state.MappingJobs[index].ID = mappingJobID(oldParentID)
		state.MappingJobs[index].ReviewFindingID = oldParentID
	}
	for runIndex := range state.Runs {
		for findingIndex := range state.Runs[runIndex].FindingIDs {
			if state.Runs[runIndex].FindingIDs[findingIndex] == originalParentID {
				state.Runs[runIndex].FindingIDs[findingIndex] = oldParentID
			}
		}
	}
	migrated, err := migrateRepositoryState(&state)
	if err != nil || !migrated || state.RawFindings[0].ID != newRawID {
		t.Fatalf("legacy raw migration migrated=%v state=%#v err=%v", migrated, state.RawFindings, err)
	}
}

func TestRepositoryReviewSQLiteProfileAutomationCatalogFailuresReachCallers(t *testing.T) {
	store := newRepositoryAuditTestStore(t)
	profile, err := store.CreateProfile(t.Context(), validProfileForTest("rrpf_catalog_failure", "Catalog"))
	if err != nil {
		t.Fatal(err)
	}
	automationInput, err := MaterializeRepositoryReviewAutomation(
		profile, validAutomationForTest("rra_catalog_failure", "Catalog"),
	)
	if err != nil {
		t.Fatal(err)
	}
	automation, err := store.CreateAutomation(t.Context(), automationInput)
	if err != nil {
		t.Fatal(err)
	}
	database, err := store.openDatabase(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	var payload []byte
	if err := database.QueryRow(
		`SELECT payload_json FROM repository_review_automations WHERE automation_id = ?`, automation.ID,
	).Scan(&payload); err != nil {
		t.Fatal(err)
	}
	var invalid RepositoryReviewAutomation
	_ = json.Unmarshal(payload, &invalid)
	invalid.MaxFilesPerRun = -1
	payload, _ = json.Marshal(invalid)
	if _, err := database.Exec(
		`UPDATE repository_review_automations SET payload_json = ? WHERE automation_id = ?`,
		payload, automation.ID,
	); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec("PRAGMA wal_checkpoint(TRUNCATE)"); err != nil {
		t.Fatal(err)
	}
	_ = database.Close()
	path := filepath.Join(store.root, repositoryReviewDatabaseFilename)
	store.openForTest = func(context.Context) (*sql.DB, error) { return sql.Open("sqlite", path) }
	if _, err := store.UpdateProfile(
		t.Context(), profile.ID, profile.Version, func(*RepositoryReviewProfile) error { return nil },
	); err == nil {
		t.Fatal("catalog-error profile update succeeded")
	}
	if err := store.DeleteProfile(t.Context(), profile.ID, profile.Version); err == nil {
		t.Fatal("catalog-error profile delete succeeded")
	}
}

func TestRepositoryReviewSQLiteFacadesPropagateSemanticRowFailure(t *testing.T) {
	store := newRepositoryAuditTestStore(t)
	file := repositoryAuditTestFile("pkg/semantic.go", "7", 7)
	recorded := recordRepositoryAuditCoverage(
		t, store, "owner/semantic", "commit", "inventory", []FileRef{file}, "run",
	)
	profile, err := store.CreateProfile(t.Context(), validProfileForTest("rrpf_semantic", "Semantic"))
	if err != nil {
		t.Fatal(err)
	}
	automationInput, err := MaterializeRepositoryReviewAutomation(
		profile, validAutomationForTest("rra_semantic", "Semantic"),
	)
	if err != nil {
		t.Fatal(err)
	}
	automation, err := store.CreateAutomation(t.Context(), automationInput)
	if err != nil {
		t.Fatal(err)
	}
	changedFile := file
	changedFile.BlobSHA = strings.Repeat("8", 40)
	recordPlan, err := store.Plan(
		t.Context(), recorded.State.Repository, "commit-next", "inventory-next",
		[]FileRef{changedFile}, false,
	)
	if err != nil {
		t.Fatal(err)
	}
	noopPlan, err := store.PlanWithProfileLimitAuthoritative(
		t.Context(), recorded.State.Repository, "commit", "inventory",
		"repository-bug-finder-v1", []FileRef{file}, false, maxReviewFiles, true,
	)
	if err != nil {
		t.Fatal(err)
	}
	database, err := store.openDatabase(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	badState := recorded.State
	badState.Runs[0].InspectedFiles = -1
	statePayload, _ := json.Marshal(badState)
	if _, err := database.Exec(
		`UPDATE repository_review_states SET payload_json = ? WHERE state_id = ?`,
		statePayload, badState.ID,
	); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(
		`UPDATE repository_review_profiles SET name = '' WHERE profile_id = ?`, profile.ID,
	); err != nil {
		t.Fatal(err)
	}
	var automationPayload []byte
	if err := database.QueryRow(
		`SELECT payload_json FROM repository_review_automations WHERE automation_id = ?`, automation.ID,
	).Scan(&automationPayload); err != nil {
		t.Fatal(err)
	}
	var badAutomation RepositoryReviewAutomation
	_ = json.Unmarshal(automationPayload, &badAutomation)
	badAutomation.MaxFilesPerRun = -1
	automationPayload, _ = json.Marshal(badAutomation)
	if _, err := database.Exec(
		`UPDATE repository_review_automations SET payload_json = ? WHERE automation_id = ?`,
		automationPayload, automation.ID,
	); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec("PRAGMA wal_checkpoint(TRUNCATE)"); err != nil {
		t.Fatal(err)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}
	databasePath := filepath.Join(store.root, repositoryReviewDatabaseFilename)
	store.openForTest = func(context.Context) (*sql.DB, error) {
		return sql.Open("sqlite", databasePath)
	}

	if _, _, err := store.loadProfile(profile.ID); err == nil {
		t.Fatal("semantic profile loaded")
	}
	if _, err := store.listProfilesUnlocked(maxProfileCount); err == nil {
		t.Fatal("semantic profile listed")
	}
	if _, err := store.CreateProfile(t.Context(), profile); err == nil {
		t.Fatal("semantic profile duplicate created")
	}
	if _, err := store.UpdateProfile(
		t.Context(), profile.ID, profile.Version, func(*RepositoryReviewProfile) error { return nil },
	); err == nil {
		t.Fatal("semantic profile updated")
	}
	if err := store.DeleteProfile(t.Context(), profile.ID, profile.Version); err == nil {
		t.Fatal("semantic profile deleted")
	}
	if err := store.validateAutomationProfileSnapshotUnlocked(automation); err == nil {
		t.Fatal("semantic profile snapshot validated")
	}

	if _, _, err := store.loadAutomation(automation.ID); err == nil {
		t.Fatal("semantic automation loaded")
	}
	if _, err := store.listAutomationsUnlocked(maxAutomationCount); err == nil {
		t.Fatal("semantic automation listed")
	}
	if _, err := store.CreateAutomation(t.Context(), automation); err == nil {
		t.Fatal("semantic automation duplicate created")
	}
	if _, err := store.UpdateAutomation(
		t.Context(), automation.ID, automation.Version, func(*RepositoryReviewAutomation) error { return nil },
	); err == nil {
		t.Fatal("semantic automation updated")
	}
	if err := store.DeleteAutomation(t.Context(), automation.ID, automation.Version); err == nil {
		t.Fatal("semantic automation deleted")
	}
	if err := store.ensureRepositoryAutomationUniqueUnlocked("rra_other", "owner/other"); err == nil {
		t.Fatal("semantic automation uniqueness succeeded")
	}
	if _, err := store.profileAssignedUnlocked(profile.ID); err == nil {
		t.Fatal("semantic assignment lookup succeeded")
	}
	if _, err := store.profileActiveUnlocked(profile.ID); err == nil {
		t.Fatal("semantic active lookup succeeded")
	}

	if _, _, err := store.Get(recorded.State.Repository); err == nil {
		t.Fatal("semantic state loaded")
	}
	if _, _, err := store.GetByID(recorded.State.ID); err == nil {
		t.Fatal("semantic state ID loaded")
	}
	if _, err := store.List(); err == nil {
		t.Fatal("semantic state listed")
	}
	if _, err := store.SetFindingStatus(recorded.State.Repository, "missing", FindingOpen, 1); err == nil {
		t.Fatal("semantic finding status succeeded")
	}
	if _, _, err := store.PrepareIssue(IssueDraftRequest{
		Repository: recorded.State.Repository, FindingIDs: []string{"missing"},
	}); err == nil {
		t.Fatal("semantic issue preparation succeeded")
	}
	if _, _, err := store.UpdateIssueDraft(
		recorded.State.Repository, "missing", "title", "body", nil, 1,
	); err == nil {
		t.Fatal("semantic issue update succeeded")
	}
	if _, _, _, err := store.ClaimIssueDraftPublication(
		recorded.State.Repository, "missing", 1,
	); err == nil {
		t.Fatal("semantic issue claim succeeded")
	}
	if _, _, err := store.SetIssueDraftPublication(
		recorded.State.Repository, "missing", 1, IssueDraftUnknown, "", "",
	); err == nil {
		t.Fatal("semantic issue publication succeeded")
	}
	if _, err := store.Record(t.Context(), RecordRequest{Plan: recordPlan, RunID: "semantic-record"}); err == nil {
		t.Fatal("semantic review record succeeded")
	}
	if _, err := store.FinalizeNoopPlan(noopPlan); err == nil {
		t.Fatal("semantic no-op finalization succeeded")
	}
	if _, _, err := store.SetFindingStatusByVersion(
		recorded.State.Repository, "missing", FindingOpen, 1,
	); err == nil {
		t.Fatal("semantic versioned finding status succeeded")
	}
	generation := testIssueGenerationRequest(recorded.State.Repository, "missing", "generation-semantic")
	if _, _, _, err := store.ReserveIssueGeneration(generation); err == nil {
		t.Fatal("semantic issue reservation succeeded")
	}
	if _, _, _, err := store.BeginIssueRegeneration(
		recorded.State.Repository, "missing", generation,
	); err == nil {
		t.Fatal("semantic issue regeneration succeeded")
	}
	if _, _, err := store.CompleteIssueGeneration(
		recorded.State.Repository, "missing", generation.GenerationID,
		"title", "body", nil, "",
	); err == nil {
		t.Fatal("semantic issue completion succeeded")
	}
	if _, err := store.DeleteIssueDraft(recorded.State.Repository, "missing", 1); err == nil {
		t.Fatal("semantic issue deletion succeeded")
	}
	if _, _, err := store.LinkExistingIssue(ExistingIssueLink{
		Repository: recorded.State.Repository, FindingID: "missing",
		ExpectedFindingVersion: 1, ExternalID: "1",
		ExternalURL: "https://github.com/owner/semantic/issues/1",
		Title:       "Existing", State: "open", Confirmed: true,
	}); err == nil {
		t.Fatal("semantic existing issue link succeeded")
	}
	if _, err := store.UnlinkExistingIssue(
		recorded.State.Repository, "missing", 1, true,
	); err == nil {
		t.Fatal("semantic existing issue unlink succeeded")
	}

	if _, _, _, err := store.reconcileRepositoryJobs(recorded.State.Repository); err == nil {
		t.Fatal("semantic repository reconciliation succeeded")
	}
	if _, _, err := store.RetryRunFindingStatus(
		recorded.State.Repository, []string{"missing"},
	); err == nil {
		t.Fatal("semantic run finding retry succeeded")
	}
	if _, _, _, _, err := store.ClaimMappingJob(
		recorded.State.Repository, "missing", RepositoryMappingModelSnapshot{},
	); err == nil {
		t.Fatal("semantic mapping claim succeeded")
	}
	if _, _, err := store.ResolvePossibleDuplicate(
		recorded.State.Repository,
		RepositoryDuplicateResolution{
			ProvisionalID: "provisional", CandidateID: "candidate", Decision: "distinct",
			ExpectedProvisionalVersion: 1,
		},
	); err == nil {
		t.Fatal("semantic duplicate resolution succeeded")
	}
	if _, _, err := store.ReserveValidationJobs(
		recorded.State.Repository, []string{"missing"}, RepositoryMappingModelSnapshot{},
	); err == nil {
		t.Fatal("semantic validation reservation succeeded")
	}
	if _, _, _, _, err := store.ClaimValidationJob(
		recorded.State.Repository, "missing",
	); err == nil {
		t.Fatal("semantic validation claim succeeded")
	}
	if _, _, err := store.SetValidationJobCandidates(
		recorded.State.Repository, "missing", []string{strings.Repeat("a", 40)},
	); err == nil {
		t.Fatal("semantic validation candidates succeeded")
	}
	if _, _, _, err := store.CompleteValidationJob(
		recorded.State.Repository,
		RepositoryValidationCompletion{JobID: "missing", Outcome: RepositoryValidationNotFixed},
	); err == nil {
		t.Fatal("semantic validation completion succeeded")
	}
	if _, _, err := store.UpdateRepositoryFindingIssueSnapshot(
		recorded.State.Repository,
		RepositoryIssueSnapshotUpdate{
			RepositoryFindingID: "missing", ExpectedVersion: 1,
			State: RepositoryFindingIssueNone,
		},
	); err == nil {
		t.Fatal("semantic issue snapshot succeeded")
	}
	if _, _, err := store.SetRepositoryFindingLifecycle(
		recorded.State.Repository, "missing", RepositoryFindingOpen, 1,
	); err == nil {
		t.Fatal("semantic lifecycle mutation succeeded")
	}
	if _, err := store.BeginCampaign(t.Context(), BeginCampaignRequest{
		Repository: recorded.State.Repository, CampaignID: NewRepositoryReviewCampaignID(),
		CommitSHA: strings.Repeat("a", 40), ExpectedReviewVersion: recorded.State.ReviewVersion,
	}); err == nil {
		t.Fatal("semantic campaign begin succeeded")
	}
	if _, _, err := store.RetryDeduplications(
		recorded.State.Repository, []string{"missing"},
	); err == nil {
		t.Fatal("semantic deduplication retry succeeded")
	}
	if err := store.releaseValidationJob(recorded.State.Repository, "missing"); err == nil {
		t.Fatal("semantic validation release succeeded")
	}
	if _, _, err := store.ResolveRepositoryState(
		"owner/missing-semantic", []string{"run"},
	); err == nil {
		t.Fatal("semantic repository resolution succeeded")
	}
}

//nolint:govet // Boundary tests intentionally keep setup and assertion errors in local scopes.
func TestRepositoryReviewSQLiteMigratesArchivesAndAuditsLegacySources(t *testing.T) {
	seed := newRepositoryAuditTestStore(t)
	profile, err := seed.CreateProfile(t.Context(), validProfileForTest("rrpf_migrate", "Migration"))
	if err != nil {
		t.Fatal(err)
	}
	automationInput, err := MaterializeRepositoryReviewAutomation(
		profile,
		validAutomationForTest("rra_migrate", "Migration automation"),
	)
	if err != nil {
		t.Fatal(err)
	}
	automation, err := seed.CreateAutomation(t.Context(), automationInput)
	if err != nil {
		t.Fatal(err)
	}
	file := repositoryAuditTestFile("pkg/migrate.go", "b", 48)
	recorded := recordRepositoryAuditCoverage(
		t, seed, "owner/migrate", "commit-b", "inventory-b", []FileRef{file}, "migration-run",
	)

	workspace := t.TempDir()
	root := filepath.Join(workspace, storeDirectory)
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	stateName := "repo_" + strings.TrimPrefix(recorded.State.ID, "rrp_") + ".json"
	sources := map[string]any{
		stateName: recorded.State,
		strings.TrimSuffix(stateName, ".json") + ".summary.json": Summarize(recorded.State),
		profileFilename(profile.ID):                              profile,
		automationFilename(automation.ID):                        automation,
	}
	for name, value := range sources {
		encoded, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, name), encoded, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	badName := "profile_rrpf_malformed.json"
	if err := os.WriteFile(filepath.Join(root, badName), []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}

	store := NewSQLiteStore(workspace)
	states, err := store.List()
	if err != nil || len(states) != 1 || states[0].ID != recorded.State.ID {
		t.Fatalf("migrated states=%#v err=%v", states, err)
	}
	if _, found, err := store.GetProfile(t.Context(), profile.ID); err != nil || !found {
		t.Fatalf("migrated profile found=%v err=%v", found, err)
	}
	if _, found, err := store.GetAutomation(t.Context(), automation.ID); err != nil || !found {
		t.Fatalf("migrated automation found=%v err=%v", found, err)
	}
	for name := range sources {
		requireReviewLegacyArchived(t, root, name)
	}
	requireReviewLegacyArchived(t, root, badName)
	database, err := store.openDatabase(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	var imported, skipped, complete, issues int
	if err := database.QueryRow(`
		SELECT COALESCE(SUM(imported_count), 0), COALESCE(SUM(skipped_count), 0),
		       COUNT(*) FILTER (WHERE archive_status = 'complete')
		  FROM storage_imports WHERE component = ?`, repositoryReviewDatabaseComponent,
	).Scan(&imported, &skipped, &complete); err != nil {
		t.Fatal(err)
	}
	if err := database.QueryRow(`
		SELECT COUNT(*) FROM storage_import_issues WHERE component = ?`,
		repositoryReviewDatabaseComponent,
	).Scan(&issues); err != nil {
		t.Fatal(err)
	}
	if imported != 3 || skipped != 1 || complete != 5 || issues != 1 {
		t.Fatalf("accounting imported=%d skipped=%d complete=%d issues=%d", imported, skipped, complete, issues)
	}
	if summaries, err := NewSQLiteStore(workspace).ListSummaries(); err != nil || len(summaries) != 1 {
		t.Fatalf("idempotent reopen summaries=%#v err=%v", summaries, err)
	}
}

//nolint:govet // Boundary tests intentionally keep setup and assertion errors in local scopes.
func TestRepositoryReviewSQLiteRejectsTooNewAndTamperedSchema(t *testing.T) {
	for name, mutate := range map[string]func(*testing.T, *sql.DB){
		"too new": func(t *testing.T, database *sql.DB) {
			if _, err := database.Exec("PRAGMA user_version = 2"); err != nil {
				t.Fatal(err)
			}
		},
		"schema": func(t *testing.T, database *sql.DB) {
			if _, err := database.Exec("DROP INDEX repository_review_states_updated_idx"); err != nil {
				t.Fatal(err)
			}
		},
		"profile scope": func(t *testing.T, database *sql.DB) {
			if _, err := database.Exec(`
				INSERT INTO repository_review_profiles (
					profile_id, schema_version, version, name, review_focus, scope_free_text,
					reviewer_model, deduplication_model, deduplication_similarity_threshold,
					deduplication_candidate_limit, issue_writer_model, issue_prompt, account_ref,
					force_enabled, auto_continue, max_files_per_run, max_content_bytes,
					max_parallel_children, assignment_timeout_seconds, guard_expression,
					created_at_unix_nano, updated_at_unix_nano
				) VALUES ('rrpf_tampered', 4, 1, 'tampered', 'focus', '', 'reviewer', '', 90, 4,
				          '', 'prompt', '', 0, 0, 1, 1, 1, 60, '', 1, 1)`); err != nil {
				t.Fatal(err)
			}
		},
	} {
		t.Run(name, func(t *testing.T) {
			workspace := t.TempDir()
			store := NewSQLiteStore(workspace)
			if _, err := store.List(); err != nil {
				t.Fatal(err)
			}
			database, err := sql.Open("sqlite", filepath.Join(store.root, repositoryReviewDatabaseFilename))
			if err != nil {
				t.Fatal(err)
			}
			mutate(t, database)
			if err := database.Close(); err != nil {
				t.Fatal(err)
			}
			_, err = store.List()
			if name == "too new" && !errors.Is(err, sqlitestore.ErrTooNew) {
				t.Fatalf("too-new error=%v", err)
			}
			if name != "too new" && !errors.Is(err, sqlitestore.ErrInvalidSchema) {
				t.Fatalf("schema error=%v", err)
			}
		})
	}
}

func TestRepositoryReviewSQLiteDetectsRelationshipProjectionTamper(t *testing.T) {
	store := newRepositoryAuditTestStore(t)
	file := repositoryAuditTestFile("pkg/relationship.go", "c", 64)
	recorded := recordRepositoryAuditCoverage(
		t, store, "owner/relationship", "commit-c", "inventory-c", []FileRef{file}, "relationship-run",
	)
	database, err := store.openDatabase(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`
		UPDATE repository_review_records SET status = 'tampered'
		 WHERE state_id = ? AND record_kind = 'run'`, recorded.State.ID); err != nil {
		database.Close()
		t.Fatal(err)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.GetByID(recorded.State.ID); err == nil ||
		!strings.Contains(err.Error(), "relationship projection mismatch") {
		t.Fatalf("relationship tamper error=%v", err)
	}
}

func TestRepositoryReviewSQLitePrimitiveEncodings(t *testing.T) {
	codeTypes := []RepositoryReviewCodeType{
		RepositoryReviewCodeTypeCode,
		RepositoryReviewCodeTypeTest,
	}
	if got := repositoryReviewCodeTypeStrings(codeTypes); !reflect.DeepEqual(
		got, []string{"code", "test"},
	) {
		t.Fatalf("code type strings=%#v", got)
	}
	if reviewBoolInteger(true) != 1 || reviewBoolInteger(false) != 0 {
		t.Fatal("boolean encoding mismatch")
	}
	if id, version := nullableReviewProfile("", 7); id != nil || version != nil {
		t.Fatalf("nil profile encoding=(%#v, %#v)", id, version)
	}
	if id, version := nullableReviewProfile("rrpf_encoding", 7); id != "rrpf_encoding" || version != int64(7) {
		t.Fatalf("profile encoding=(%#v, %#v)", id, version)
	}

	now := time.Date(2026, time.August, 31, 12, 34, 56, 789, time.FixedZone("offset", 3600))
	if nullableReviewTime(time.Time{}) != nil || nullableReviewTime(now) != now.UTC().UnixNano() {
		t.Fatal("time encoding mismatch")
	}
	if !reviewNullTimeMatches(time.Time{}, sql.NullInt64{}) ||
		reviewNullTimeMatches(time.Time{}, sql.NullInt64{Valid: true}) ||
		!reviewNullTimeMatches(now, sql.NullInt64{Int64: now.UTC().UnixNano(), Valid: true}) ||
		reviewNullTimeMatches(now, sql.NullInt64{Int64: now.UTC().UnixNano() + 1, Valid: true}) {
		t.Fatal("nullable time comparison mismatch")
	}
	if !equalReviewStrings([]string{"a", "b"}, []string{"a", "b"}) ||
		equalReviewStrings([]string{"a"}, []string{"a", "b"}) ||
		equalReviewStrings([]string{"a", "b"}, []string{"a", "c"}) {
		t.Fatal("ordered string comparison mismatch")
	}

	database, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	database.SetMaxOpenConns(1)
	defer database.Close()
	if _, err = database.Exec(`CREATE TABLE ordered_values (
		owner TEXT NOT NULL, position INTEGER NOT NULL, value TEXT
	)`); err != nil {
		t.Fatal(err)
	}
	if _, err = database.Exec(`INSERT INTO ordered_values VALUES
		('owner', 1, 'second'), ('owner', 0, 'first')`); err != nil {
		t.Fatal(err)
	}
	values, err := loadReviewOrderedStrings(
		t.Context(), database,
		`SELECT value FROM ordered_values WHERE owner = ? ORDER BY position`, "owner",
	)
	if err != nil || !reflect.DeepEqual(values, []string{"first", "second"}) {
		t.Fatalf("ordered values=%#v err=%v", values, err)
	}
	if _, err = loadReviewOrderedStrings(
		t.Context(), database, `SELECT value FROM missing_values WHERE owner = ?`, "owner",
	); err == nil {
		t.Fatal("missing ordered-value table was accepted")
	}
	if _, err = database.Exec(`INSERT INTO ordered_values VALUES ('null', 0, NULL)`); err != nil {
		t.Fatal(err)
	}
	if _, err = loadReviewOrderedStrings(
		t.Context(), database,
		`SELECT value FROM ordered_values WHERE owner = ? ORDER BY position`, "null",
	); err == nil {
		t.Fatal("NULL ordered value was accepted")
	}
}

func requireReviewLegacyArchived(t *testing.T, root, name string) {
	t.Helper()
	if _, err := os.Stat(filepath.Join(root, name)); !os.IsNotExist(err) {
		t.Fatalf("legacy source %s remains: %v", name, err)
	}
	archive := filepath.Join(root, "legacy-json", repositoryReviewLegacyArchiveLabel, name)
	if _, err := os.Stat(archive); err != nil {
		t.Fatalf("archive %s: %v", archive, err)
	}
}
