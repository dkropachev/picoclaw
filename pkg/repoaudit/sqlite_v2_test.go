package repoaudit

import (
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

func TestLegacyRepositoryReviewAutomationImportDropsExecutionState(t *testing.T) {
	automation := validAutomationForTest("rra_legacy_config_only", "Legacy configuration")
	automation.SchemaVersion = 1
	automation.Version = 1
	automation.CreatedAt = automationTestNow
	automation.UpdatedAt = automationTestNow
	automation.Status = RepositoryReviewAutomationCompleted
	automation.CampaignID = NewRepositoryReviewCampaignID()
	automation.RunIDs = []string{"wfr_legacy"}
	automation.ResolvedCommitSHA = strings.Repeat("a", 40)
	automation.Progress = RepositoryReviewProgress{ReviewedFiles: 3, DeduplicatedFindings: 2}
	automation.CompletedAt = automationTestNow
	encoded, err := json.Marshal(automation)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := decodeLegacyRepositoryReviewAutomation(automation.ID, encoded)
	if err != nil {
		t.Fatal(err)
	}
	if !repositoryReviewAutomationHistoryReset(decoded) {
		t.Fatalf("legacy execution state survived import: %#v", decoded)
	}
	if decoded.ID != automation.ID || decoded.Name != automation.Name ||
		decoded.Repository != automation.Repository || decoded.ReviewFocus != automation.ReviewFocus {
		t.Fatalf("legacy configuration changed during import: %#v", decoded)
	}
}

func TestRepositoryReviewSQLiteV2RemovesFindingRecordKind(t *testing.T) {
	workspace := t.TempDir()
	root := filepath.Join(workspace, storeDirectory)
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	databasePath := filepath.Join(root, repositoryReviewDatabaseFilename)
	database, err := sql.Open("sqlite", databasePath)
	if err != nil {
		t.Fatal(err)
	}
	options := repositoryReviewStoreOptions(root)
	for _, statement := range options.Migrations[0].Statements {
		if _, err = database.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	repository := "owner/retired"
	configuredRepository := "https://github.com/Owner/Retired.git"
	fallbackRepository := "legacy/run-fallback-ledger"
	removedRepository := "owner/already-removed"
	removedConfiguredRepository := "https://github.com/Owner/Already-Removed.git"
	for _, ledgerRepository := range []string{
		repository, configuredRepository, fallbackRepository,
		removedRepository, removedConfiguredRepository,
	} {
		if _, err = database.Exec(`INSERT INTO repository_review_states (
	    state_id, repository, schema_version, version, review_version, last_commit_sha,
	    finding_count, repository_finding_count, open_finding_count, issue_draft_count,
	    unsupported_count, reviewed_file_count, excluded_file_count, updated_at_unix_nano,
	    payload_json
	) VALUES (?, ?, 5, 1, 1, '', 0, 0, 0, 0, 0, 0, 0, 1, ?)`,
			RepositoryID(ledgerRepository), ledgerRepository, []byte("{}"),
		); err != nil {
			t.Fatal(err)
		}
	}
	if _, err = database.Exec(`INSERT INTO repository_review_records (
	    state_id, record_kind, position, record_id, status, version,
	    created_at_unix_nano, updated_at_unix_nano
	) VALUES (?, 'run', 0, 'run-fallback', '', 0, 1, 1)`,
		RepositoryID(fallbackRepository),
	); err != nil {
		t.Fatal(err)
	}
	connection, err := database.Conn(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	profile := validProfileForTest("rrpf_v2_kept", "V2 kept profile")
	profile.SchemaVersion = RepositoryReviewProfileSchemaVersion
	profile.Version = 1
	profile.CreatedAt = automationTestNow
	profile.UpdatedAt = automationTestNow
	if err = normalizeProfile(&profile); err != nil {
		t.Fatal(err)
	}
	if inserted, insertErr := insertRepositoryReviewProfileConn(
		t.Context(), connection, profile, true, 0,
	); insertErr != nil || !inserted {
		t.Fatalf("insert profile=%v err=%v", inserted, insertErr)
	}
	retiredAutomation := validAutomationForTest("rra_v2_retired", "V2 retired automation")
	retiredAutomation.Repository = configuredRepository
	fallbackAutomation := validAutomationForTest("rra_v2_run_fallback", "V2 run fallback")
	fallbackAutomation.Repository = "owner/unmatched-configuration"
	fallbackAutomation.RunIDs = []string{"run-fallback"}
	keptAutomation := validAutomationForTest("rra_v2_kept", "V2 kept automation")
	keptAutomation.Repository = "owner/kept"
	for _, automation := range []*RepositoryReviewAutomation{
		&retiredAutomation, &fallbackAutomation, &keptAutomation,
	} {
		automation.SchemaVersion = RepositoryReviewAutomationSchemaVersion
		automation.Version = 1
		automation.Status = RepositoryReviewAutomationIdle
		automation.CreatedAt = automationTestNow
		automation.UpdatedAt = automationTestNow
		if err = normalizeAutomation(automation); err != nil {
			t.Fatal(err)
		}
		if inserted, insertErr := insertRepositoryReviewAutomationConn(
			t.Context(), connection, *automation, true, 0,
		); insertErr != nil || !inserted {
			t.Fatalf("insert automation %q=%v err=%v", automation.ID, inserted, insertErr)
		}
	}
	if err = connection.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err = database.Exec(`PRAGMA user_version = 1`); err != nil {
		t.Fatal(err)
	}
	if err = database.Close(); err != nil {
		t.Fatal(err)
	}
	store := NewSQLiteStore(workspace)
	ledgerTargets := []repositoryReviewPurgeLedgerTarget{
		{Repository: repository, Version: 1},
		{Repository: configuredRepository, Version: 1},
	}
	sort.Slice(ledgerTargets, func(i, j int) bool {
		return ledgerTargets[i].Repository < ledgerTargets[j].Repository
	})
	intent := repositoryReviewPurgeIntent{
		SchemaVersion:             repositoryReviewPurgeIntentSchemaVersion,
		Mode:                      repositoryReviewPurgeReset,
		Phase:                     repositoryReviewPurgeAutomationCommitting,
		AutomationID:              retiredAutomation.ID,
		ConfiguredRepository:      retiredAutomation.Repository,
		Repository:                repository,
		LedgerTargets:             ledgerTargets,
		ExpectedAutomationVersion: retiredAutomation.Version,
		ExpectedRepositoryVersion: 1,
		CreatedAt:                 automationTestNow,
	}
	removedTargets := []repositoryReviewPurgeLedgerTarget{
		{Repository: removedRepository, Version: 1},
		{Repository: removedConfiguredRepository, Version: 1},
	}
	sort.Slice(removedTargets, func(i, j int) bool {
		return removedTargets[i].Repository < removedTargets[j].Repository
	})
	removedIntent := repositoryReviewPurgeIntent{
		SchemaVersion:             repositoryReviewPurgeIntentSchemaVersion,
		Mode:                      repositoryReviewPurgeRemove,
		Phase:                     repositoryReviewPurgeAutomationApplied,
		AutomationID:              "rra_v2_already_removed",
		ConfiguredRepository:      removedConfiguredRepository,
		Repository:                removedRepository,
		LedgerTargets:             removedTargets,
		ExpectedAutomationVersion: 1,
		ExpectedRepositoryVersion: 1,
		CreatedAt:                 automationTestNow,
	}
	for _, purgeIntent := range []repositoryReviewPurgeIntent{intent, removedIntent} {
		intentData, marshalErr := json.Marshal(purgeIntent)
		if marshalErr != nil {
			t.Fatal(marshalErr)
		}
		for _, path := range store.purgeIntentPaths(purgeIntent) {
			if err = os.WriteFile(path, intentData, 0o600); err != nil {
				t.Fatal(err)
			}
		}
	}

	opened, err := store.openDatabase(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer opened.Close()
	var version int
	var ddl string
	if err = opened.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil {
		t.Fatal(err)
	}
	if err = opened.QueryRow(
		`SELECT sql FROM sqlite_schema WHERE type = 'table' AND name = 'repository_review_records'`,
	).Scan(&ddl); err != nil {
		t.Fatal(err)
	}
	if version != 2 || strings.Contains(ddl, "'finding'") {
		t.Fatalf("version=%d records DDL=%s", version, ddl)
	}
	var retired int
	if err = opened.QueryRow(
		`SELECT COUNT(*) FROM repository_review_states WHERE schema_version <> ?`, SchemaVersion,
	).Scan(&retired); err != nil || retired != 0 {
		t.Fatalf("retired state count=%d err=%v", retired, err)
	}
	var retiredAutomations, fallbackAutomations, keptAutomations, keptProfiles int
	if err = opened.QueryRow(
		`SELECT COUNT(*) FROM repository_review_automations WHERE automation_id = ?`,
		retiredAutomation.ID,
	).Scan(&retiredAutomations); err != nil || retiredAutomations != 0 {
		t.Fatalf("retired automation count=%d err=%v", retiredAutomations, err)
	}
	if err = opened.QueryRow(
		`SELECT COUNT(*) FROM repository_review_automations WHERE automation_id = ?`,
		fallbackAutomation.ID,
	).Scan(&fallbackAutomations); err != nil || fallbackAutomations != 0 {
		t.Fatalf("run-fallback automation count=%d err=%v", fallbackAutomations, err)
	}
	if err = opened.QueryRow(
		`SELECT COUNT(*) FROM repository_review_automations WHERE automation_id = ?`,
		keptAutomation.ID,
	).Scan(&keptAutomations); err != nil || keptAutomations != 1 {
		t.Fatalf("kept automation count=%d err=%v", keptAutomations, err)
	}
	if err = opened.QueryRow(
		`SELECT COUNT(*) FROM repository_review_profiles WHERE profile_id = ?`, profile.ID,
	).Scan(&keptProfiles); err != nil || keptProfiles != 1 {
		t.Fatalf("kept profile count=%d err=%v", keptProfiles, err)
	}
	if reconciled, reconcileErr := store.ReconcilePurgeIntents(t.Context()); reconcileErr != nil || reconciled != 2 {
		t.Fatalf("retired purge intents reconciled=%d err=%v", reconciled, reconcileErr)
	}
	for _, purgeIntent := range []repositoryReviewPurgeIntent{intent, removedIntent} {
		for _, path := range store.purgeIntentCleanupPaths(purgeIntent) {
			if _, statErr := os.Stat(path); !os.IsNotExist(statErr) {
				t.Fatalf("retired purge marker remains at %q: %v", path, statErr)
			}
		}
	}
	var retirements int
	if err = opened.QueryRow(
		`SELECT COUNT(*) FROM repository_review_retired_ledgers WHERE automation_id = ?`,
		retiredAutomation.ID,
	).Scan(&retirements); err != nil || retirements != 0 {
		t.Fatalf("retirement markers=%d err=%v", retirements, err)
	}
	state, found, err := store.Get(repository)
	if err != nil || found || state.SchemaVersion != SchemaVersion {
		t.Fatalf("fresh state=%#v found=%v err=%v", state, found, err)
	}
}
