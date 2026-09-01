package evolution

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/sipeed/picoclaw/pkg/sqlitestore"
)

func TestEvolutionSQLiteCodecRejectsInvalidDomainValues(t *testing.T) {
	timestamp := time.Unix(1_700_000_000, 123).UTC()
	invalidTimestamp := time.Unix(0, -1).UTC()
	baseRecord := LearningRecord{
		ID: "record", Kind: RecordKindTask, WorkspaceID: "workspace",
		CreatedAt: timestamp, Status: "new",
	}
	recordCases := []struct {
		name   string
		class  string
		mutate func(*LearningRecord)
	}{
		{name: "class", class: "other"},
		{name: "kind-class", class: "pattern"},
		{name: "workspace-space", class: "task", mutate: func(value *LearningRecord) { value.WorkspaceID = " workspace" }},
		{name: "empty-id", class: "task", mutate: func(value *LearningRecord) { value.ID = "" }},
		{name: "kind", class: "task", mutate: func(value *LearningRecord) { value.Kind = "unknown" }},
		{name: "status", class: "task", mutate: func(value *LearningRecord) { value.Status = "unknown" }},
		{name: "created-zero", class: "task", mutate: func(value *LearningRecord) { value.CreatedAt = time.Time{} }},
		{name: "created-range", class: "task", mutate: func(value *LearningRecord) { value.CreatedAt = invalidTimestamp }},
		{name: "updated-range", class: "task", mutate: func(value *LearningRecord) { value.UpdatedAt = &invalidTimestamp }},
		{name: "text", class: "task", mutate: func(value *LearningRecord) { value.Summary = "bad\x00text" }},
		{name: "event-count", class: "task", mutate: func(value *LearningRecord) { value.EventCount = -1 }},
		{name: "success-rate-nan", class: "task", mutate: func(value *LearningRecord) { value.SuccessRate = math.NaN() }},
		{name: "success-rate-inf", class: "task", mutate: func(value *LearningRecord) { value.SuccessRate = math.Inf(1) }},
		{name: "maturity-nan", class: "task", mutate: func(value *LearningRecord) { value.MaturityScore = math.NaN() }},
		{name: "ordered-count", class: "task", mutate: func(value *LearningRecord) { value.Signals = make([]string, maximumEvolutionChildren+1) }},
		{name: "ordered-value", class: "task", mutate: func(value *LearningRecord) { value.ToolKinds = []string{"bad\x00kind"} }},
		{name: "execution-count", class: "task", mutate: func(value *LearningRecord) {
			value.ToolExecutions = make([]ToolExecutionRecord, maximumEvolutionChildren+1)
		}},
		{name: "execution-name", class: "task", mutate: func(value *LearningRecord) { value.ToolExecutions = []ToolExecutionRecord{{Name: "bad\x00name"}} }},
		{name: "execution-error", class: "task", mutate: func(value *LearningRecord) {
			value.ToolExecutions = []ToolExecutionRecord{{ErrorSummary: "bad\x00error"}}
		}},
		{name: "execution-skills", class: "task", mutate: func(value *LearningRecord) {
			value.ToolExecutions = []ToolExecutionRecord{{SkillNames: []string{"bad\x00skill"}}}
		}},
		{name: "attempted-skills", class: "task", mutate: func(value *LearningRecord) {
			value.AttemptTrail = &AttemptTrail{AttemptedSkills: []string{"bad\x00skill"}}
		}},
		{name: "successful-path", class: "task", mutate: func(value *LearningRecord) {
			value.AttemptTrail = &AttemptTrail{FinalSuccessfulPath: []string{"bad\x00skill"}}
		}},
		{name: "snapshot-count", class: "task", mutate: func(value *LearningRecord) {
			value.AttemptTrail = &AttemptTrail{SkillContextSnapshots: make([]SkillContextSnapshot, maximumEvolutionChildren+1)}
		}},
		{name: "snapshot-trigger", class: "task", mutate: func(value *LearningRecord) {
			value.AttemptTrail = &AttemptTrail{SkillContextSnapshots: []SkillContextSnapshot{{Trigger: "bad\x00trigger"}}}
		}},
		{name: "snapshot-skills", class: "task", mutate: func(value *LearningRecord) {
			value.AttemptTrail = &AttemptTrail{SkillContextSnapshots: []SkillContextSnapshot{{SkillNames: []string{"bad\x00skill"}}}}
		}},
		{name: "source-json", class: "task", mutate: func(value *LearningRecord) { value.Source = map[string]any{"number": math.NaN()} }},
		{name: "source-size", class: "task", mutate: func(value *LearningRecord) {
			value.Source = map[string]any{"text": strings.Repeat("x", maximumEvolutionSourceBytes)}
		}},
	}
	for _, test := range recordCases {
		t.Run("record/"+test.name, func(t *testing.T) {
			value := baseRecord
			if test.mutate != nil {
				test.mutate(&value)
			}
			if err := validateEvolutionRecord(test.class, value); err == nil {
				t.Fatal("invalid record accepted")
			}
		})
	}

	baseDraft := SkillDraft{
		ID: "draft", WorkspaceID: "workspace", CreatedAt: timestamp,
		TargetSkillName: "weather", DraftType: DraftTypeWorkflow,
		ChangeKind: ChangeKindCreate, Status: DraftStatusCandidate,
	}
	draftCases := []struct {
		name   string
		mutate func(*SkillDraft)
	}{
		{name: "identity", mutate: func(value *SkillDraft) { value.ID = " draft" }},
		{name: "target", mutate: func(value *SkillDraft) { value.TargetSkillName = "bad\x00name" }},
		{name: "created", mutate: func(value *SkillDraft) { value.CreatedAt = invalidTimestamp }},
		{name: "updated", mutate: func(value *SkillDraft) { value.UpdatedAt = &invalidTimestamp }},
		{name: "classification", mutate: func(value *SkillDraft) { value.DraftType = DraftType("bad\x00type") }},
		{name: "status", mutate: func(value *SkillDraft) { value.Status = "unknown" }},
		{name: "text", mutate: func(value *SkillDraft) { value.BodyOrPatch = "bad\x00body" }},
		{name: "ordered", mutate: func(value *SkillDraft) { value.ReviewNotes = []string{"bad\x00note"} }},
		{name: "ordered-count", mutate: func(value *SkillDraft) { value.ScanFindings = make([]string, maximumEvolutionChildren+1) }},
	}
	for _, test := range draftCases {
		t.Run("draft/"+test.name, func(t *testing.T) {
			value := baseDraft
			test.mutate(&value)
			if err := validateEvolutionDraft(value); err == nil {
				t.Fatal("invalid draft accepted")
			}
		})
	}

	baseProfile := SkillProfile{
		SkillName: "weather", WorkspaceID: "workspace", Status: SkillStatusActive,
		LastUsedAt: timestamp,
	}
	profileCases := []struct {
		name   string
		mutate func(*SkillProfile)
	}{
		{name: "workspace", mutate: func(value *SkillProfile) { value.WorkspaceID = " workspace" }},
		{name: "skill-empty", mutate: func(value *SkillProfile) { value.SkillName = "" }},
		{name: "skill-format", mutate: func(value *SkillProfile) { value.SkillName = "Weather Helper" }},
		{name: "status", mutate: func(value *SkillProfile) { value.Status = "unknown" }},
		{name: "use-count", mutate: func(value *SkillProfile) { value.UseCount = -1 }},
		{name: "score-nan", mutate: func(value *SkillProfile) { value.RetentionScore = math.NaN() }},
		{name: "score-inf", mutate: func(value *SkillProfile) { value.RetentionScore = math.Inf(1) }},
		{name: "last-used", mutate: func(value *SkillProfile) { value.LastUsedAt = invalidTimestamp }},
		{name: "text", mutate: func(value *SkillProfile) { value.ChangeReason = "bad\x00reason" }},
		{name: "ordered", mutate: func(value *SkillProfile) { value.AvoidPatterns = []string{"bad\x00pattern"} }},
		{name: "history-count", mutate: func(value *SkillProfile) {
			value.VersionHistory = make([]SkillVersionEntry, maximumEvolutionChildren+1)
		}},
		{name: "history-time", mutate: func(value *SkillProfile) { value.VersionHistory = []SkillVersionEntry{{Timestamp: invalidTimestamp}} }},
		{name: "history-text", mutate: func(value *SkillProfile) { value.VersionHistory = []SkillVersionEntry{{Summary: "bad\x00summary"}} }},
	}
	for _, test := range profileCases {
		t.Run("profile/"+test.name, func(t *testing.T) {
			value := baseProfile
			test.mutate(&value)
			if err := validateEvolutionProfile(value); err == nil {
				t.Fatal("invalid profile accepted")
			}
		})
	}

	if validEvolutionText(string([]byte{0xff}), 4, false) {
		t.Fatal("invalid UTF-8 accepted")
	}
	if _, err := evolutionRequiredTimestamp(time.Time{}); err == nil {
		t.Fatal("zero required timestamp accepted")
	}
}

func TestEvolutionSQLiteTransactionsReplaceChildrenAndFenceVersions(t *testing.T) {
	ctx := context.Background()
	paths := NewPaths(t.TempDir(), "")
	store := NewSQLiteStore(paths)
	timestamp := time.Unix(1_700_000_000, 7).UTC()
	record := LearningRecord{
		ID: "record", Kind: RecordKindTask, WorkspaceID: paths.Workspace,
		CreatedAt: timestamp, Status: "new", ToolKinds: []string{"read", "write"},
		ToolExecutions: []ToolExecutionRecord{{Name: "read", Success: true, SkillNames: []string{"one"}}},
		AttemptTrail: &AttemptTrail{
			AttemptedSkills: []string{"one"}, FinalSuccessfulPath: []string{"two"},
			SkillContextSnapshots: []SkillContextSnapshot{{Sequence: 1, Trigger: "retry", SkillNames: []string{"two"}}},
		},
	}
	if err := store.AppendLearningRecord(ctx, record); err != nil {
		t.Fatal(err)
	}
	record.Status = "clustered"
	record.ToolKinds = []string{"exec"}
	record.ToolExecutions = nil
	record.AttemptTrail = nil
	if err := store.AppendLearningRecord(ctx, record); err != nil {
		t.Fatal(err)
	}
	loaded, err := store.LoadTaskRecords()
	if err != nil || len(loaded) != 1 || !reflect.DeepEqual(loaded[0], record) {
		t.Fatalf("updated record = %#v, %v", loaded, err)
	}

	draftTimestamp := timestamp.Add(time.Second)
	drafts := []SkillDraft{
		{
			ID: "one", WorkspaceID: paths.Workspace, CreatedAt: timestamp,
			TargetSkillName: "weather", DraftType: DraftTypeWorkflow,
			ChangeKind: ChangeKindCreate, Status: DraftStatusCandidate,
			MatchedSkillRefs: []string{"ref"}, IntendedUseCases: []string{"forecast"},
			PreferredEntryPath: []string{"native"}, AvoidPatterns: []string{"shell"},
			ReviewNotes: []string{"review"}, ScanFindings: []string{"clean"},
		},
		{
			ID: "two", WorkspaceID: paths.Workspace, CreatedAt: timestamp,
			UpdatedAt: &draftTimestamp, TargetSkillName: "climate",
			DraftType: DraftTypeShortcut, ChangeKind: ChangeKindAppend,
			Status: DraftStatusQuarantined,
		},
	}
	if err := store.immediate(ctx, func(conn *sql.Conn) error {
		return replaceEvolutionDrafts(ctx, conn, drafts)
	}); err != nil {
		t.Fatal(err)
	}
	loadedDrafts, err := store.LoadDrafts()
	if err != nil || !reflect.DeepEqual(loadedDrafts, drafts) {
		t.Fatalf("replaced drafts = %#v, %v", loadedDrafts, err)
	}

	profile := SkillProfile{
		SkillName: "weather", WorkspaceID: paths.Workspace, CurrentVersion: "v1",
		Status: SkillStatusActive, LastUsedAt: timestamp, UseCount: 1, RetentionScore: .5,
		IntendedUseCases: []string{"forecast"}, PreferredEntryPath: []string{"native"},
		AvoidPatterns: []string{"shell"}, VersionHistory: []SkillVersionEntry{{
			Version: "v1", Action: "create", Timestamp: timestamp, DraftID: "one",
			Summary: "created", Rollback: true, RollbackReason: "test",
		}},
	}
	if err := store.SaveProfile(profile); err != nil {
		t.Fatal(err)
	}
	profile.CurrentVersion = "v2"
	profile.UseCount = 2
	profile.VersionHistory = append(profile.VersionHistory, SkillVersionEntry{
		Version: "v2", Action: "update", Timestamp: draftTimestamp,
	})
	if err := store.SaveProfile(profile); err != nil {
		t.Fatal(err)
	}
	profiles, err := store.LoadProfiles()
	if err != nil || len(profiles) != 1 || !reflect.DeepEqual(profiles[0], profile) {
		t.Fatalf("updated profiles = %#v, %v", profiles, err)
	}

	db, conn := openEvolutionCoverageConnection(t, store)
	defer db.Close()
	defer conn.Close()
	if err := updateEvolutionRecord(ctx, conn, "task", 0, 1, record, nil); err == nil ||
		!strings.Contains(err.Error(), "version-fenced") {
		t.Fatalf("stale record version update = %v", err)
	}
	if err := updateEvolutionProfile(ctx, conn, profile, 1); err == nil ||
		!strings.Contains(err.Error(), "version-fenced") {
		t.Fatalf("stale profile version update = %v", err)
	}
}

func TestEvolutionSQLitePublicBoundariesAndProfileFallback(t *testing.T) {
	ctx := context.Background()
	var nilStore *Store
	if err := nilStore.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := nilStore.LoadTaskRecords(); err == nil {
		t.Fatal("nil store load succeeded")
	}
	if _, err := nilStore.LoadLearningRecords(); err == nil {
		t.Fatal("nil store aggregate load succeeded")
	}
	if err := nilStore.immediate(ctx, func(*sql.Conn) error { return nil }); err == nil {
		t.Fatal("nil store transaction succeeded")
	}
	if err := nilStore.AppendLearningRecord(ctx, LearningRecord{}); err == nil {
		t.Fatal("nil store invalid record succeeded")
	}

	workspace := t.TempDir()
	store := NewSQLiteStore(NewPaths(workspace, ""))
	if err := store.AppendTaskOrPatternRecords(ctx, nil); err != nil {
		t.Fatal(err)
	}
	if err := store.AppendTaskRecords(ctx, nil); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveDrafts(nil); err != nil {
		t.Fatal(err)
	}
	if err := store.MarkTaskRecordsClustered([]string{"", "  "}); err != nil {
		t.Fatal(err)
	}
	if err := store.MergePatternRecords(nil); err != nil {
		t.Fatal(err)
	}
	invalidRecord := LearningRecord{ID: "invalid", Kind: RecordKindTask, WorkspaceID: workspace}
	if err := store.AppendTaskOrPatternRecords(ctx, []LearningRecord{invalidRecord}); err == nil {
		t.Fatal("invalid mixed record append succeeded")
	}
	if err := store.AppendTaskRecords(ctx, []LearningRecord{invalidRecord}); err == nil {
		t.Fatal("invalid task append succeeded")
	}
	if err := store.SaveTaskRecords([]LearningRecord{invalidRecord}); err == nil {
		t.Fatal("invalid task replacement succeeded")
	}
	if err := store.MergePatternRecords([]LearningRecord{invalidRecord}); err == nil {
		t.Fatal("invalid pattern merge succeeded")
	}
	if err := store.SaveDrafts([]SkillDraft{{}}); err == nil {
		t.Fatal("invalid draft save succeeded")
	}
	if err := store.UpdateProfile(workspace, "bad name", func(*SkillProfile, bool) error { return nil }); err == nil {
		t.Fatal("invalid profile update name succeeded")
	}
	if err := store.saveRecords(ctx, "task", make([]LearningRecord, maximumEvolutionRecords+1)); err == nil {
		t.Fatal("oversized record replacement succeeded")
	}
	if err := store.UpdateProfile(workspace, "weather", nil); err == nil {
		t.Fatal("nil profile update accepted")
	}
	sentinel := errors.New("stop")
	if err := store.UpdateProfile(workspace, "weather", func(*SkillProfile, bool) error {
		return sentinel
	}); !errors.Is(err, sentinel) {
		t.Fatalf("callback error = %v", err)
	}
	if err := store.UpdateProfile(workspace, "weather", func(*SkillProfile, bool) error {
		return nil
	}); err != nil {
		t.Fatalf("zero missing profile update = %v", err)
	}
	if err := store.UpdateProfile(workspace, "weather", func(profile *SkillProfile, _ bool) error {
		*profile = SkillProfile{SkillName: "other", WorkspaceID: workspace}
		return nil
	}); err == nil || !strings.Contains(err.Error(), "identity") {
		t.Fatalf("identity-changing profile update = %v", err)
	}
	if _, err := store.LoadProfile("bad name"); err == nil {
		t.Fatal("invalid profile name loaded")
	}

	fallback := SkillProfile{SkillName: "weather", Status: SkillStatusActive}
	if err := store.SaveProfile(fallback); err != nil {
		t.Fatal(err)
	}
	loaded, err := store.LoadProfile("weather")
	if err != nil || !reflect.DeepEqual(loaded, fallback) {
		t.Fatalf("legacy workspace fallback = %#v, %v", loaded, err)
	}
	if err := store.UpdateProfile(workspace, "weather", func(profile *SkillProfile, exists bool) error {
		if !exists {
			t.Fatal("fallback profile not visible")
		}
		profile.UseCount++
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	localized, err := store.LoadProfile("weather")
	if err != nil || localized.WorkspaceID != workspace || localized.UseCount != 1 {
		t.Fatalf("localized fallback = %#v, %v", localized, err)
	}

	broken := NewSQLiteStore(Paths{RootDir: t.TempDir(), Database: t.TempDir()})
	if _, err := broken.LoadDrafts(); err == nil {
		t.Fatal("directory database loaded drafts")
	}
	if _, err := broken.LoadProfile("weather"); err == nil {
		t.Fatal("directory database loaded profile")
	}
	if _, err := broken.LoadProfiles(); err == nil {
		t.Fatal("directory database loaded profiles")
	}
	valid := LearningRecord{
		ID: "valid", Kind: RecordKindTask, WorkspaceID: workspace,
		CreatedAt: time.Unix(1_700_000_000, 0).UTC(),
	}
	if err := broken.AppendTaskRecord(ctx, valid); err == nil {
		t.Fatal("directory database accepted a transaction")
	}

	lockPaths := NewPaths(t.TempDir(), "")
	lockStore := NewSQLiteStore(lockPaths)
	lockPath, err := filepath.Abs(filepath.Clean(lockPaths.Database))
	if err != nil {
		t.Fatal(err)
	}
	evolutionDatabaseWriteLocks.Store(lockPath, "invalid")
	t.Cleanup(func() { evolutionDatabaseWriteLocks.Delete(lockPath) })
	if err := lockStore.immediate(ctx, func(*sql.Conn) error { return nil }); err == nil ||
		!strings.Contains(err.Error(), "lock is invalid") {
		t.Fatalf("invalid write lock = %v", err)
	}
}

func TestEvolutionSQLitePathNormalizationAndMergeIdentity(t *testing.T) {
	workspace := t.TempDir()
	paths := normalizedEvolutionPaths(Paths{Workspace: workspace})
	want := NewPaths(workspace, "")
	if !reflect.DeepEqual(paths, want) {
		t.Fatalf("normalized paths = %#v, want %#v", paths, want)
	}
	if got := workspaceScopeDir(string(filepath.Separator)); !strings.HasPrefix(got, "workspace-") {
		t.Fatalf("root workspace scope = %q", got)
	}
	if got := sanitizeWorkspaceComponent("@name/value!"); got != "name-value" {
		t.Fatalf("sanitized workspace component = %q", got)
	}
	base := LearningRecord{ID: "same", WorkspaceID: "workspace", Summary: "old"}
	update := base
	update.Summary = "new"
	merged := mergeLearningRecordsByID([]LearningRecord{base}, []LearningRecord{update})
	if len(merged) != 1 || merged[0].Summary != "new" {
		t.Fatalf("identity merge = %#v", merged)
	}
	if got := evolutionImportPriority("future-source"); got != 2 {
		t.Fatalf("unknown import priority = %d", got)
	}
	if _, err := evolutionSourceJSON(map[string]any{"number": math.NaN()}); err == nil {
		t.Fatal("invalid source JSON encoded")
	}
}

func TestEvolutionSQLiteLegacyEnumerationAndMalformedInputs(t *testing.T) {
	ctx := context.Background()
	workspace := t.TempDir()
	paths := NewPaths(workspace, "")
	if err := os.MkdirAll(paths.ProfilesDir, 0o700); err != nil {
		t.Fatal(err)
	}
	scope := workspaceScopeDir(workspace)
	scopeDir := filepath.Join(paths.ProfilesDir, scope)
	if err := os.Mkdir(scopeDir, 0o700); err != nil {
		t.Fatal(err)
	}
	direct := SkillProfile{SkillName: "weather", Status: SkillStatusActive}
	nested := SkillProfile{SkillName: "climate", WorkspaceID: workspace, Status: SkillStatusCold}
	writeEvolutionCoverageJSON(t, filepath.Join(paths.ProfilesDir, "weather.json"), direct)
	writeEvolutionCoverageJSON(t, filepath.Join(scopeDir, "climate.json"), nested)
	if err := os.WriteFile(filepath.Join(paths.ProfilesDir, "ignored.txt"), []byte("ignored"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(scopeDir, "ignored.txt"), []byte("ignored"), 0o600); err != nil {
		t.Fatal(err)
	}
	sources, err := evolutionLegacySources(paths)
	if err != nil {
		t.Fatal(err)
	}
	var relatives []string
	for _, source := range sources {
		relatives = append(relatives, source.Relative)
	}
	if !containsEvolutionCoverageString(relatives, "profiles/weather.json") ||
		!containsEvolutionCoverageString(relatives, filepath.ToSlash(filepath.Join("profiles", scope, "climate.json"))) {
		t.Fatalf("legacy profile sources = %#v", relatives)
	}

	store := NewSQLiteStore(paths)
	db, conn := openEvolutionCoverageConnection(t, store)
	defer db.Close()
	defer conn.Close()
	malformed, err := importEvolutionLegacyDrafts(ctx, conn, sqlitestore.LegacyInput{
		Relative: "skill-drafts.json", Data: []byte("{"),
	})
	if err != nil || malformed.Skipped != 1 || malformed.Issues[0].Code != "malformed-drafts" {
		t.Fatalf("malformed draft import = %#v, %v", malformed, err)
	}
	empty, err := importEvolutionLegacyDrafts(ctx, conn, sqlitestore.LegacyInput{
		Relative: "skill-drafts.json", Data: []byte("  \n"),
	})
	if err != nil || empty.Imported != 0 || empty.Skipped != 0 {
		t.Fatalf("empty draft import = %#v, %v", empty, err)
	}
	invalidProfile, err := importEvolutionLegacyProfile(ctx, conn, sqlitestore.LegacyInput{
		Relative: "profiles/wrong.json", Data: []byte(`{"skill_name":"weather"}`),
	})
	if err != nil || invalidProfile.Skipped != 1 || invalidProfile.Issues[0].Code != "invalid-profile" {
		t.Fatalf("invalid profile import = %#v, %v", invalidProfile, err)
	}
	if _, err := importEvolutionLegacySource(ctx, conn, sqlitestore.LegacyInput{Relative: "unknown.json"}); err == nil || !strings.Contains(err.Error(), "unknown") {
		t.Fatalf("unknown source = %v", err)
	}
	if evolutionProfilePathConsistent("profiles/extra/wrong/weather.json", direct) ||
		evolutionProfilePathConsistent("profiles/climate.json", direct) ||
		evolutionProfilePathConsistent("profiles/wrong/climate.json", nested) {
		t.Fatal("inconsistent profile path accepted")
	}

	record := LearningRecord{
		ID: "legacy", Kind: RecordKindTask, WorkspaceID: workspace,
		CreatedAt: time.Unix(1_700_000_000, 0).UTC(),
	}
	recordJSON, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := importEvolutionLegacyRecords(ctx, conn, sqlitestore.LegacyInput{
		Data: bytesRepeatForEvolutionCoverage('x', maximumEvolutionLegacyLineBytes+1),
	}, "task-records", "task"); err == nil || !strings.Contains(err.Error(), "size limit") {
		t.Fatalf("oversized record import = %v", err)
	}
	tooManyLines := bytesRepeatForEvolutionCoverage('\n', maximumEvolutionRecords*2+1)
	if _, err := importEvolutionLegacyRecords(ctx, conn, sqlitestore.LegacyInput{
		Data: tooManyLines,
	}, "task-records", "task"); err == nil || !strings.Contains(err.Error(), "count") {
		t.Fatalf("oversized record count import = %v", err)
	}
	validDraft := SkillDraft{
		ID: "legacy-draft", WorkspaceID: workspace, TargetSkillName: "weather",
		DraftType: DraftTypeWorkflow, ChangeKind: ChangeKindCreate, Status: DraftStatusCandidate,
	}
	draftJSON, err := json.Marshal([]SkillDraft{validDraft, validDraft})
	if err != nil {
		t.Fatal(err)
	}
	duplicateDrafts, err := importEvolutionLegacyDrafts(ctx, conn, sqlitestore.LegacyInput{Data: draftJSON})
	if err != nil || duplicateDrafts.Imported != 1 || duplicateDrafts.Skipped != 1 {
		t.Fatalf("duplicate draft import = %#v, %v", duplicateDrafts, err)
	}
	tooManyDrafts := "[" + strings.Repeat("{},", maximumEvolutionDrafts) + "{}]"
	if _, err := importEvolutionLegacyDrafts(ctx, conn, sqlitestore.LegacyInput{Data: []byte(tooManyDrafts)}); err == nil || !strings.Contains(err.Error(), "count") {
		t.Fatalf("oversized draft count import = %v", err)
	}
	importedProfile := SkillProfile{SkillName: "newskill", Status: SkillStatusActive}
	directJSON, err := json.Marshal(importedProfile)
	if err != nil {
		t.Fatal(err)
	}
	profileInput := sqlitestore.LegacyInput{Relative: "profiles/newskill.json", Data: directJSON}
	firstProfile, err := importEvolutionLegacyProfile(ctx, conn, profileInput)
	if err != nil || firstProfile.Imported != 1 {
		t.Fatalf("first profile import = %#v, %v", firstProfile, err)
	}
	secondProfile, err := importEvolutionLegacyProfile(ctx, conn, profileInput)
	if err != nil || secondProfile.Skipped != 1 || secondProfile.Issues[0].Code != "identity-conflict" {
		t.Fatalf("duplicate profile import = %#v, %v", secondProfile, err)
	}

	if err := conn.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := importEvolutionLegacyRecords(ctx, conn, sqlitestore.LegacyInput{
		Data: append(recordJSON, '\n'),
	}, "task-records", "task"); err == nil {
		t.Fatal("record import ignored closed connection")
	}
	oneDraftJSON, err := json.Marshal([]SkillDraft{validDraft})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := importEvolutionLegacyDrafts(ctx, conn, sqlitestore.LegacyInput{Data: oneDraftJSON}); err == nil {
		t.Fatal("draft import ignored closed connection")
	}
	if _, err := importEvolutionLegacyProfile(ctx, conn, profileInput); err == nil {
		t.Fatal("profile import ignored closed connection")
	}
}

func TestEvolutionSQLiteRejectsUnsafeLegacyProfileTrees(t *testing.T) {
	if os.PathSeparator == '\\' {
		t.Skip("Unix permission and symlink coverage")
	}
	if err := validateEvolutionLegacyFile(nil); err == nil {
		t.Fatal("nil legacy file accepted")
	}
	for _, test := range []struct {
		name  string
		setup func(*testing.T, Paths)
	}{
		{
			name: "profiles-is-file",
			setup: func(t *testing.T, paths Paths) {
				if err := os.MkdirAll(paths.RootDir, 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(paths.ProfilesDir, []byte("file"), 0o600); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "writable-profiles-directory",
			setup: func(t *testing.T, paths Paths) {
				if err := os.MkdirAll(paths.ProfilesDir, 0o777); err != nil {
					t.Fatal(err)
				}
				if err := os.Chmod(paths.ProfilesDir, 0o777); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "direct-symlink",
			setup: func(t *testing.T, paths Paths) {
				if err := os.MkdirAll(paths.ProfilesDir, 0o700); err != nil {
					t.Fatal(err)
				}
				target := filepath.Join(t.TempDir(), "profile.json")
				if err := os.WriteFile(target, []byte("{}"), 0o600); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(target, filepath.Join(paths.ProfilesDir, "profile.json")); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "writable-file",
			setup: func(t *testing.T, paths Paths) {
				if err := os.MkdirAll(paths.ProfilesDir, 0o700); err != nil {
					t.Fatal(err)
				}
				path := filepath.Join(paths.ProfilesDir, "profile.json")
				if err := os.WriteFile(path, []byte("{}"), 0o666); err != nil {
					t.Fatal(err)
				}
				if err := os.Chmod(path, 0o666); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "nested-symlink",
			setup: func(t *testing.T, paths Paths) {
				scope := filepath.Join(paths.ProfilesDir, "scope")
				if err := os.MkdirAll(scope, 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(filepath.Join(t.TempDir(), "missing"), filepath.Join(scope, "profile.json")); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "nested-directory",
			setup: func(t *testing.T, paths Paths) {
				if err := os.MkdirAll(filepath.Join(paths.ProfilesDir, "scope", "child"), 0o700); err != nil {
					t.Fatal(err)
				}
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			paths := NewPaths(t.TempDir(), "")
			test.setup(t, paths)
			if _, err := evolutionLegacySources(paths); err == nil {
				t.Fatal("unsafe legacy tree accepted")
			}
		})
	}
}

func TestEvolutionSQLitePropagatesConnectionFailures(t *testing.T) {
	ctx := context.Background()
	paths := NewPaths(t.TempDir(), "")
	store := NewSQLiteStore(paths)
	db, conn := openEvolutionCoverageConnection(t, store)
	if err := conn.Close(); err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	timestamp := time.Unix(1_700_000_000, 0).UTC()
	record := LearningRecord{ID: "record", Kind: RecordKindTask, WorkspaceID: paths.Workspace, CreatedAt: timestamp}
	draft := SkillDraft{
		ID: "draft", WorkspaceID: paths.Workspace, TargetSkillName: "weather",
		DraftType: DraftTypeWorkflow, ChangeKind: ChangeKindCreate, Status: DraftStatusCandidate,
	}
	profile := SkillProfile{SkillName: "weather", WorkspaceID: paths.Workspace}

	checks := []struct {
		name string
		call func() error
	}{
		{name: "put-record", call: func() error { _, err := putEvolutionRecord(ctx, conn, "task", record, "", true); return err }},
		{name: "insert-record", call: func() error { return insertEvolutionRecord(ctx, conn, "task", 0, record, nil) }},
		{name: "update-record", call: func() error { return updateEvolutionRecord(ctx, conn, "task", 0, 1, record, nil) }},
		{name: "record-children", call: func() error { return replaceEvolutionRecordChildren(ctx, conn, "task", record) }},
		{name: "replace-records", call: func() error { return replaceEvolutionRecords(ctx, conn, "task", []LearningRecord{record}) }},
		{name: "load-records", call: func() error { _, err := loadEvolutionRecords(ctx, conn, "task"); return err }},
		{name: "load-record-strings", call: func() error { return loadEvolutionRecordStrings(ctx, conn, "task", nil, nil) }},
		{name: "load-executions", call: func() error { return loadEvolutionToolExecutions(ctx, conn, "task", nil, nil) }},
		{name: "load-attempts", call: func() error { return loadEvolutionAttemptTrails(ctx, conn, "task", nil, nil) }},
		{name: "replace-drafts", call: func() error { return replaceEvolutionDrafts(ctx, conn, []SkillDraft{draft}) }},
		{name: "insert-draft", call: func() error { return insertEvolutionDraft(ctx, conn, 0, draft) }},
		{name: "put-draft", call: func() error { _, err := putEvolutionDraft(ctx, conn, draft, false); return err }},
		{name: "draft-children", call: func() error { return replaceEvolutionDraftChildren(ctx, conn, draft) }},
		{name: "load-drafts", call: func() error { _, err := loadEvolutionDrafts(ctx, conn); return err }},
		{name: "put-profile", call: func() error { _, err := putEvolutionProfile(ctx, conn, profile, false); return err }},
		{name: "insert-profile", call: func() error { return insertEvolutionProfile(ctx, conn, profile) }},
		{name: "update-profile", call: func() error { return updateEvolutionProfile(ctx, conn, profile, 1) }},
		{name: "profile-children", call: func() error { return replaceEvolutionProfileChildren(ctx, conn, profile) }},
		{name: "load-profile", call: func() error { _, _, err := loadEvolutionProfile(ctx, conn, paths.Workspace, "weather"); return err }},
		{name: "load-profile-children", call: func() error { return loadOneEvolutionProfileChildren(ctx, conn, &profile) }},
		{name: "load-profiles", call: func() error { _, err := loadEvolutionProfiles(ctx, conn); return err }},
		{name: "validate-schema", call: func() error { return validateEvolutionSchema(ctx, conn) }},
		{name: "validate-positions", call: func() error { return validateEvolutionPositions(ctx, conn) }},
	}
	for _, check := range checks {
		t.Run(check.name, func(t *testing.T) {
			if err := check.call(); err == nil {
				t.Fatal("closed SQLite connection error was not propagated")
			}
		})
	}
}

func TestEvolutionSQLiteRelationalFailureBoundaries(t *testing.T) {
	ctx := context.Background()
	t.Run("record-import-and-cap-boundaries", func(t *testing.T) {
		withEvolutionCoverageConnection(t, func(paths Paths, conn *sql.Conn) {
			record := evolutionCoverageRecord(paths)
			if err := insertEvolutionRecord(ctx, conn, "task", maximumEvolutionRecords-1, record, "task-records"); err != nil {
				t.Fatal(err)
			}
			if written, err := putEvolutionRecord(ctx, conn, "task", record, "", false); err != nil || written {
				t.Fatalf("runtime-disabled existing record = %t, %v", written, err)
			}
			newRecord := record
			newRecord.ID = "new-record"
			if _, err := putEvolutionRecord(ctx, conn, "task", newRecord, "", true); err == nil ||
				!strings.Contains(err.Error(), "count") {
				t.Fatalf("record position cap = %v", err)
			}
		})
	})
	t.Run("record-insert-trigger", func(t *testing.T) {
		withEvolutionCoverageConnection(t, func(paths Paths, conn *sql.Conn) {
			mustEvolutionCoverageSQL(t, conn, `CREATE TEMP TRIGGER fail_put_record_insert
				BEFORE INSERT ON evolution_records BEGIN SELECT RAISE(ABORT, 'forced'); END`)
			if _, err := putEvolutionRecord(ctx, conn, "task", evolutionCoverageRecord(paths), "", true); err == nil {
				t.Fatal("put record insert trigger failure was ignored")
			}
		})
	})
	t.Run("record-source-errors", func(t *testing.T) {
		withEvolutionCoverageConnection(t, func(paths Paths, conn *sql.Conn) {
			record := evolutionCoverageRecord(paths)
			record.Source = map[string]any{"number": math.NaN()}
			if err := insertEvolutionRecord(ctx, conn, "task", 0, record, nil); err == nil {
				t.Fatal("insert encoded invalid source")
			}
			if err := updateEvolutionRecord(ctx, conn, "task", 0, 1, record, nil); err == nil {
				t.Fatal("update encoded invalid source")
			}
		})
	})
	t.Run("draft-caps", func(t *testing.T) {
		withEvolutionCoverageConnection(t, func(paths Paths, conn *sql.Conn) {
			if err := replaceEvolutionDrafts(ctx, conn, make([]SkillDraft, maximumEvolutionDrafts+1)); err == nil {
				t.Fatal("oversized draft replacement succeeded")
			}
			draft := evolutionCoverageDraft(paths)
			if err := insertEvolutionDraft(ctx, conn, maximumEvolutionDrafts-1, draft); err != nil {
				t.Fatal(err)
			}
			newDraft := draft
			newDraft.ID = "new-draft"
			if _, err := putEvolutionDraft(ctx, conn, newDraft, false); err == nil ||
				!strings.Contains(err.Error(), "count") {
				t.Fatalf("draft position cap = %v", err)
			}
		})
	})
	t.Run("record-update", func(t *testing.T) {
		withEvolutionCoverageConnection(t, func(paths Paths, conn *sql.Conn) {
			record := evolutionCoverageRecord(paths)
			mustEvolutionCoverageSQL(t, conn, `CREATE TEMP TRIGGER fail_record_update
				BEFORE UPDATE ON evolution_records BEGIN SELECT RAISE(ABORT, 'forced'); END`)
			if err := insertEvolutionRecord(ctx, conn, "task", 0, record, nil); err != nil {
				t.Fatal(err)
			}
			if _, err := putEvolutionRecord(ctx, conn, "task", record, "", true); err == nil {
				t.Fatal("record update trigger failure was ignored")
			}
		})
	})
	t.Run("record-child-put", func(t *testing.T) {
		withEvolutionCoverageConnection(t, func(paths Paths, conn *sql.Conn) {
			record := evolutionCoverageRecord(paths)
			record.ToolKinds = []string{"read"}
			mustEvolutionCoverageSQL(t, conn, `CREATE TEMP TRIGGER fail_record_string
				BEFORE INSERT ON evolution_record_strings BEGIN SELECT RAISE(ABORT, 'forced'); END`)
			if _, err := putEvolutionRecord(ctx, conn, "task", record, "", true); err == nil {
				t.Fatal("record child trigger failure was ignored")
			}
		})
	})
	for _, test := range []struct {
		name    string
		trigger string
		mutate  func(*LearningRecord)
	}{
		{
			name: "execution",
			trigger: `CREATE TEMP TRIGGER fail_child BEFORE INSERT ON evolution_record_tool_executions
				BEGIN SELECT RAISE(ABORT, 'forced'); END`,
			mutate: func(record *LearningRecord) {
				record.ToolExecutions = []ToolExecutionRecord{{Name: "read"}}
			},
		},
		{
			name: "execution-skill",
			trigger: `CREATE TEMP TRIGGER fail_child BEFORE INSERT ON evolution_record_tool_execution_skills
				BEGIN SELECT RAISE(ABORT, 'forced'); END`,
			mutate: func(record *LearningRecord) {
				record.ToolExecutions = []ToolExecutionRecord{{Name: "read", SkillNames: []string{"one"}}}
			},
		},
		{
			name: "attempt-trail",
			trigger: `CREATE TEMP TRIGGER fail_child BEFORE INSERT ON evolution_record_attempt_trails
				BEGIN SELECT RAISE(ABORT, 'forced'); END`,
			mutate: func(record *LearningRecord) { record.AttemptTrail = &AttemptTrail{} },
		},
		{
			name: "attempt-string",
			trigger: `CREATE TEMP TRIGGER fail_child BEFORE INSERT ON evolution_record_attempt_strings
				BEGIN SELECT RAISE(ABORT, 'forced'); END`,
			mutate: func(record *LearningRecord) {
				record.AttemptTrail = &AttemptTrail{AttemptedSkills: []string{"one"}}
			},
		},
		{
			name: "attempt-snapshot",
			trigger: `CREATE TEMP TRIGGER fail_child BEFORE INSERT ON evolution_record_attempt_snapshots
				BEGIN SELECT RAISE(ABORT, 'forced'); END`,
			mutate: func(record *LearningRecord) {
				record.AttemptTrail = &AttemptTrail{SkillContextSnapshots: []SkillContextSnapshot{{}}}
			},
		},
		{
			name: "attempt-snapshot-skill",
			trigger: `CREATE TEMP TRIGGER fail_child BEFORE INSERT ON evolution_record_attempt_snapshot_skills
				BEGIN SELECT RAISE(ABORT, 'forced'); END`,
			mutate: func(record *LearningRecord) {
				record.AttemptTrail = &AttemptTrail{SkillContextSnapshots: []SkillContextSnapshot{{SkillNames: []string{"one"}}}}
			},
		},
	} {
		t.Run("record-child-"+test.name, func(t *testing.T) {
			withEvolutionCoverageConnection(t, func(paths Paths, conn *sql.Conn) {
				record := evolutionCoverageRecord(paths)
				if err := insertEvolutionRecord(ctx, conn, "task", 0, record, nil); err != nil {
					t.Fatal(err)
				}
				test.mutate(&record)
				mustEvolutionCoverageSQL(t, conn, test.trigger)
				if err := replaceEvolutionRecordChildren(ctx, conn, "task", record); err == nil {
					t.Fatal("record child trigger failure was ignored")
				}
			})
		})
	}
	for _, table := range []string{
		"evolution_record_tool_executions",
		"evolution_record_attempt_trails",
	} {
		t.Run("record-delete-"+table, func(t *testing.T) {
			withEvolutionCoverageConnection(t, func(paths Paths, conn *sql.Conn) {
				record := evolutionCoverageRecord(paths)
				if err := insertEvolutionRecord(ctx, conn, "task", 0, record, nil); err != nil {
					t.Fatal(err)
				}
				dropEvolutionCoverageTable(t, conn, table)
				if err := replaceEvolutionRecordChildren(ctx, conn, "task", record); err == nil {
					t.Fatal("missing record child table was ignored")
				}
			})
		})
	}
	t.Run("replace-record-insert", func(t *testing.T) {
		withEvolutionCoverageConnection(t, func(paths Paths, conn *sql.Conn) {
			mustEvolutionCoverageSQL(t, conn, `CREATE TEMP TRIGGER fail_record_insert
				BEFORE INSERT ON evolution_records BEGIN SELECT RAISE(ABORT, 'forced'); END`)
			if err := replaceEvolutionRecords(ctx, conn, "task", []LearningRecord{evolutionCoverageRecord(paths)}); err == nil {
				t.Fatal("record replacement insert failure was ignored")
			}
		})
	})
	t.Run("replace-record-child", func(t *testing.T) {
		withEvolutionCoverageConnection(t, func(paths Paths, conn *sql.Conn) {
			record := evolutionCoverageRecord(paths)
			record.ToolKinds = []string{"read"}
			mustEvolutionCoverageSQL(t, conn, `CREATE TEMP TRIGGER fail_record_child
				BEFORE INSERT ON evolution_record_strings BEGIN SELECT RAISE(ABORT, 'forced'); END`)
			if err := replaceEvolutionRecords(ctx, conn, "task", []LearningRecord{record}); err == nil {
				t.Fatal("record replacement child failure was ignored")
			}
		})
	})

	for _, table := range []string{
		"evolution_record_strings",
		"evolution_record_tool_executions",
		"evolution_record_attempt_trails",
	} {
		t.Run("load-records-"+table, func(t *testing.T) {
			withEvolutionCoverageConnection(t, func(paths Paths, conn *sql.Conn) {
				record := evolutionCoverageRecord(paths)
				if err := insertEvolutionRecord(ctx, conn, "task", 0, record, nil); err != nil {
					t.Fatal(err)
				}
				dropEvolutionCoverageTable(t, conn, table)
				if _, err := loadEvolutionRecords(ctx, conn, "task"); err == nil {
					t.Fatal("missing record relationship table was ignored")
				}
			})
		})
	}
	t.Run("load-execution-skills", func(t *testing.T) {
		withEvolutionCoverageConnection(t, func(paths Paths, conn *sql.Conn) {
			record := evolutionCoverageRecord(paths)
			record.ToolExecutions = []ToolExecutionRecord{{Name: "read"}}
			seedEvolutionCoverageRecord(t, conn, record)
			dropEvolutionCoverageTable(t, conn, "evolution_record_tool_execution_skills")
			loaded := []LearningRecord{{WorkspaceID: record.WorkspaceID, ID: record.ID}}
			indexes := map[string]int{evolutionRecordIdentity("task", record.WorkspaceID, record.ID): 0}
			if err := loadEvolutionToolExecutions(ctx, conn, "task", loaded, indexes); err == nil {
				t.Fatal("missing execution skills table was ignored")
			}
		})
	})
	for _, table := range []string{
		"evolution_record_attempt_strings",
		"evolution_record_attempt_snapshots",
		"evolution_record_attempt_snapshot_skills",
	} {
		t.Run("load-attempts-"+table, func(t *testing.T) {
			withEvolutionCoverageConnection(t, func(paths Paths, conn *sql.Conn) {
				record := evolutionCoverageRecord(paths)
				record.AttemptTrail = &AttemptTrail{}
				seedEvolutionCoverageRecord(t, conn, record)
				dropEvolutionCoverageTable(t, conn, table)
				loaded := []LearningRecord{{WorkspaceID: record.WorkspaceID, ID: record.ID}}
				indexes := map[string]int{evolutionRecordIdentity("task", record.WorkspaceID, record.ID): 0}
				if err := loadEvolutionAttemptTrails(ctx, conn, "task", loaded, indexes); err == nil {
					t.Fatal("missing attempt relationship table was ignored")
				}
			})
		})
	}

	t.Run("draft-update", func(t *testing.T) {
		withEvolutionCoverageConnection(t, func(paths Paths, conn *sql.Conn) {
			draft := evolutionCoverageDraft(paths)
			if err := insertEvolutionDraft(ctx, conn, 0, draft); err != nil {
				t.Fatal(err)
			}
			mustEvolutionCoverageSQL(t, conn, `CREATE TEMP TRIGGER fail_draft_update
				BEFORE UPDATE ON evolution_skill_drafts BEGIN SELECT RAISE(ABORT, 'forced'); END`)
			if _, err := putEvolutionDraft(ctx, conn, draft, false); err == nil {
				t.Fatal("draft update trigger failure was ignored")
			}
		})
	})
	t.Run("draft-child", func(t *testing.T) {
		withEvolutionCoverageConnection(t, func(paths Paths, conn *sql.Conn) {
			draft := evolutionCoverageDraft(paths)
			if err := insertEvolutionDraft(ctx, conn, 0, draft); err != nil {
				t.Fatal(err)
			}
			draft.ReviewNotes = []string{"review"}
			mustEvolutionCoverageSQL(t, conn, `CREATE TEMP TRIGGER fail_draft_child
				BEFORE INSERT ON evolution_skill_draft_strings BEGIN SELECT RAISE(ABORT, 'forced'); END`)
			if _, err := putEvolutionDraft(ctx, conn, draft, false); err == nil {
				t.Fatal("draft child trigger failure was ignored")
			}
		})
	})
	t.Run("load-draft-children", func(t *testing.T) {
		withEvolutionCoverageConnection(t, func(paths Paths, conn *sql.Conn) {
			if err := insertEvolutionDraft(ctx, conn, 0, evolutionCoverageDraft(paths)); err != nil {
				t.Fatal(err)
			}
			dropEvolutionCoverageTable(t, conn, "evolution_skill_draft_strings")
			if _, err := loadEvolutionDrafts(ctx, conn); err == nil {
				t.Fatal("missing draft child table was ignored")
			}
		})
	})

	t.Run("profile-update", func(t *testing.T) {
		withEvolutionCoverageConnection(t, func(paths Paths, conn *sql.Conn) {
			profile := evolutionCoverageProfile(paths)
			if err := insertEvolutionProfile(ctx, conn, profile); err != nil {
				t.Fatal(err)
			}
			mustEvolutionCoverageSQL(t, conn, `CREATE TEMP TRIGGER fail_profile_update
				BEFORE UPDATE ON evolution_skill_profiles BEGIN SELECT RAISE(ABORT, 'forced'); END`)
			if _, err := putEvolutionProfile(ctx, conn, profile, false); err == nil {
				t.Fatal("profile update trigger failure was ignored")
			}
		})
	})
	t.Run("profile-child", func(t *testing.T) {
		withEvolutionCoverageConnection(t, func(paths Paths, conn *sql.Conn) {
			profile := evolutionCoverageProfile(paths)
			profile.IntendedUseCases = []string{"forecast"}
			mustEvolutionCoverageSQL(t, conn, `CREATE TEMP TRIGGER fail_profile_child
				BEFORE INSERT ON evolution_skill_profile_strings BEGIN SELECT RAISE(ABORT, 'forced'); END`)
			if _, err := putEvolutionProfile(ctx, conn, profile, false); err == nil {
				t.Fatal("profile child trigger failure was ignored")
			}
		})
	})
	t.Run("load-profile-versions", func(t *testing.T) {
		withEvolutionCoverageConnection(t, func(paths Paths, conn *sql.Conn) {
			profile := evolutionCoverageProfile(paths)
			if _, err := putEvolutionProfile(ctx, conn, profile, false); err != nil {
				t.Fatal(err)
			}
			dropEvolutionCoverageTable(t, conn, "evolution_skill_profile_versions")
			if err := loadOneEvolutionProfileChildren(ctx, conn, &profile); err == nil {
				t.Fatal("missing profile versions table was ignored")
			}
		})
	})
	for _, table := range []string{"evolution_skill_profile_strings", "evolution_skill_profile_versions"} {
		t.Run("load-profiles-"+table, func(t *testing.T) {
			withEvolutionCoverageConnection(t, func(paths Paths, conn *sql.Conn) {
				profile := evolutionCoverageProfile(paths)
				if _, err := putEvolutionProfile(ctx, conn, profile, false); err != nil {
					t.Fatal(err)
				}
				dropEvolutionCoverageTable(t, conn, table)
				if _, err := loadEvolutionProfiles(ctx, conn); err == nil {
					t.Fatal("missing profile relationship table was ignored")
				}
			})
		})
	}
}

func TestEvolutionSQLiteSchemaRejectsUnexpectedObjects(t *testing.T) {
	for _, statement := range []string{
		`CREATE TABLE unexpected_evolution_state (id INTEGER)`,
		`CREATE INDEX unexpected_evolution_index ON storage_imports(component)`,
	} {
		t.Run(strings.Fields(statement)[1], func(t *testing.T) {
			withEvolutionCoverageConnection(t, func(_ Paths, conn *sql.Conn) {
				mustEvolutionCoverageSQL(t, conn, statement)
				if err := validateEvolutionSchema(context.Background(), conn); err == nil {
					t.Fatal("unexpected SQLite schema object accepted")
				}
			})
		})
	}
}

func TestEvolutionSQLiteRejectsMalformedRelationalRows(t *testing.T) {
	ctx := context.Background()
	t.Run("record-string-field", func(t *testing.T) {
		withEvolutionCoverageConnection(t, func(paths Paths, conn *sql.Conn) {
			record := evolutionCoverageRecord(paths)
			if err := insertEvolutionRecord(ctx, conn, "task", 0, record, nil); err != nil {
				t.Fatal(err)
			}
			rebuildEvolutionCoverageTable(t, conn, "evolution_record_strings", `(
				record_class TEXT, workspace_id TEXT, record_id TEXT,
				field_name TEXT, position INTEGER, value TEXT)`)
			mustEvolutionCoverageSQLArgs(t, conn, `INSERT INTO evolution_record_strings
				VALUES ('task', ?, ?, 'unknown', 0, 'value')`, record.WorkspaceID, record.ID)
			loaded := []LearningRecord{{WorkspaceID: record.WorkspaceID, ID: record.ID}}
			indexes := map[string]int{evolutionRecordIdentity("task", record.WorkspaceID, record.ID): 0}
			if err := loadEvolutionRecordStrings(ctx, conn, "task", loaded, indexes); err == nil {
				t.Fatal("unknown record string field accepted")
			}
		})
	})
	t.Run("execution", func(t *testing.T) {
		withEvolutionCoverageConnection(t, func(paths Paths, conn *sql.Conn) {
			record := evolutionCoverageRecord(paths)
			if err := insertEvolutionRecord(ctx, conn, "task", 0, record, nil); err != nil {
				t.Fatal(err)
			}
			rebuildEvolutionCoverageTable(t, conn, "evolution_record_tool_executions", `(
				record_class TEXT, workspace_id TEXT, record_id TEXT, position INTEGER,
				name TEXT, success INTEGER, error_summary TEXT)`)
			mustEvolutionCoverageSQLArgs(t, conn, `INSERT INTO evolution_record_tool_executions
				VALUES ('task', ?, ?, 0, NULL, 1, '')`, record.WorkspaceID, record.ID)
			loaded := []LearningRecord{{WorkspaceID: record.WorkspaceID, ID: record.ID}}
			indexes := map[string]int{evolutionRecordIdentity("task", record.WorkspaceID, record.ID): 0}
			if err := loadEvolutionToolExecutions(ctx, conn, "task", loaded, indexes); err == nil {
				t.Fatal("malformed execution row accepted")
			}
		})
	})
	t.Run("execution-skill", func(t *testing.T) {
		withEvolutionCoverageConnection(t, func(paths Paths, conn *sql.Conn) {
			record := evolutionCoverageRecord(paths)
			record.ToolExecutions = []ToolExecutionRecord{{Name: "read"}}
			seedEvolutionCoverageRecord(t, conn, record)
			rebuildEvolutionCoverageTable(t, conn, "evolution_record_tool_execution_skills", `(
				record_class TEXT, workspace_id TEXT, record_id TEXT,
				execution_position INTEGER, position INTEGER, skill_name TEXT)`)
			mustEvolutionCoverageSQLArgs(t, conn, `INSERT INTO evolution_record_tool_execution_skills
				VALUES ('task', ?, ?, 0, 0, NULL)`, record.WorkspaceID, record.ID)
			loaded := []LearningRecord{{WorkspaceID: record.WorkspaceID, ID: record.ID}}
			indexes := map[string]int{evolutionRecordIdentity("task", record.WorkspaceID, record.ID): 0}
			if err := loadEvolutionToolExecutions(ctx, conn, "task", loaded, indexes); err == nil {
				t.Fatal("malformed execution skill row accepted")
			}
		})
	})
	for _, test := range []struct {
		name, table, schema, insert string
	}{
		{
			name: "trail-string", table: "evolution_record_attempt_strings",
			schema: `(record_class TEXT, workspace_id TEXT, record_id TEXT,
				field_name TEXT, position INTEGER, value TEXT)`,
			insert: `INSERT INTO evolution_record_attempt_strings
				VALUES ('task', ?, 'record', 'attempted_skills', 0, NULL)`,
		},
		{
			name: "snapshot", table: "evolution_record_attempt_snapshots",
			schema: `(record_class TEXT, workspace_id TEXT, record_id TEXT,
				position INTEGER, sequence INTEGER, trigger_name TEXT)`,
			insert: `INSERT INTO evolution_record_attempt_snapshots
				VALUES ('task', ?, 'record', 0, 1, NULL)`,
		},
		{
			name: "snapshot-skill", table: "evolution_record_attempt_snapshot_skills",
			schema: `(record_class TEXT, workspace_id TEXT, record_id TEXT,
				snapshot_position INTEGER, position INTEGER, skill_name TEXT)`,
			insert: `INSERT INTO evolution_record_attempt_snapshot_skills
				VALUES ('task', ?, 'record', 0, 0, NULL)`,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			withEvolutionCoverageConnection(t, func(paths Paths, conn *sql.Conn) {
				record := evolutionCoverageRecord(paths)
				record.AttemptTrail = &AttemptTrail{
					SkillContextSnapshots: []SkillContextSnapshot{{}},
				}
				seedEvolutionCoverageRecord(t, conn, record)
				rebuildEvolutionCoverageTable(t, conn, test.table, test.schema)
				if strings.Contains(test.insert, "?") {
					mustEvolutionCoverageSQLArgs(t, conn, test.insert, record.WorkspaceID)
				} else {
					mustEvolutionCoverageSQL(t, conn, test.insert)
				}
				loaded := []LearningRecord{{WorkspaceID: record.WorkspaceID, ID: record.ID}}
				indexes := map[string]int{evolutionRecordIdentity("task", record.WorkspaceID, record.ID): 0}
				if err := loadEvolutionAttemptTrails(ctx, conn, "task", loaded, indexes); err == nil {
					t.Fatal("malformed attempt relationship row accepted")
				}
			})
		})
	}

	t.Run("draft", func(t *testing.T) {
		withEvolutionCoverageConnection(t, func(_ Paths, conn *sql.Conn) {
			dropEvolutionCoverageTable(t, conn, "evolution_skill_drafts")
			mustEvolutionCoverageSQL(t, conn, `CREATE TABLE evolution_skill_drafts (
				workspace_id TEXT, draft_id TEXT, position INTEGER, created_at_unix_nano INTEGER,
				updated_at_unix_nano INTEGER, source_record_id TEXT, target_skill_name TEXT,
				draft_type TEXT, change_kind TEXT, human_summary TEXT, body_or_patch TEXT,
				status TEXT)`)
			mustEvolutionCoverageSQL(t, conn, `INSERT INTO evolution_skill_drafts VALUES
				(NULL, 'draft', 0, 0, NULL, '', 'weather', 'workflow', 'create', '', '', 'candidate')`)
			if _, err := loadEvolutionDrafts(ctx, conn); err == nil {
				t.Fatal("malformed draft row accepted")
			}
		})
	})
	t.Run("draft-field", func(t *testing.T) {
		withEvolutionCoverageConnection(t, func(paths Paths, conn *sql.Conn) {
			draft := evolutionCoverageDraft(paths)
			if err := insertEvolutionDraft(ctx, conn, 0, draft); err != nil {
				t.Fatal(err)
			}
			rebuildEvolutionCoverageTable(t, conn, "evolution_skill_draft_strings", `(
				workspace_id TEXT, draft_id TEXT, field_name TEXT, position INTEGER, value TEXT)`)
			mustEvolutionCoverageSQLArgs(t, conn, `INSERT INTO evolution_skill_draft_strings
				VALUES (?, 'draft', 'unknown', 0, 'value')`, draft.WorkspaceID)
			if _, err := loadEvolutionDrafts(ctx, conn); err == nil {
				t.Fatal("unknown draft field accepted")
			}
		})
	})
	for _, test := range []struct {
		name, table, schema, insert string
		loadAll                     bool
	}{
		{
			name: "profile-string", table: "evolution_skill_profile_strings",
			schema: `(workspace_id TEXT, skill_name TEXT, field_name TEXT, position INTEGER, value TEXT)`,
			insert: `INSERT INTO evolution_skill_profile_strings
				VALUES (?, 'weather', 'intended_use_cases', 0, NULL)`,
		},
		{
			name: "profile-version", table: "evolution_skill_profile_versions",
			schema: `(workspace_id TEXT, skill_name TEXT, position INTEGER, version_name TEXT,
				action_name TEXT, timestamp_unix_nano INTEGER, draft_id TEXT, summary TEXT,
				rollback INTEGER, rollback_reason TEXT)`,
			insert: `INSERT INTO evolution_skill_profile_versions
				VALUES (?, 'weather', 0, NULL, '', 0, '', '', 0, '')`,
		},
		{
			name: "profiles-string", table: "evolution_skill_profile_strings",
			schema: `(workspace_id TEXT, skill_name TEXT, field_name TEXT, position INTEGER, value TEXT)`,
			insert: `INSERT INTO evolution_skill_profile_strings
				VALUES (?, 'weather', 'intended_use_cases', 0, NULL)`, loadAll: true,
		},
		{
			name: "profiles-version", table: "evolution_skill_profile_versions",
			schema: `(workspace_id TEXT, skill_name TEXT, position INTEGER, version_name TEXT,
				action_name TEXT, timestamp_unix_nano INTEGER, draft_id TEXT, summary TEXT,
				rollback INTEGER, rollback_reason TEXT)`,
			insert: `INSERT INTO evolution_skill_profile_versions
				VALUES (?, 'weather', 0, NULL, '', 0, '', '', 0, '')`, loadAll: true,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			withEvolutionCoverageConnection(t, func(paths Paths, conn *sql.Conn) {
				profile := evolutionCoverageProfile(paths)
				if _, err := putEvolutionProfile(ctx, conn, profile, false); err != nil {
					t.Fatal(err)
				}
				rebuildEvolutionCoverageTable(t, conn, test.table, test.schema)
				mustEvolutionCoverageSQLArgs(t, conn, test.insert, profile.WorkspaceID)
				if test.loadAll {
					if _, err := loadEvolutionProfiles(ctx, conn); err == nil {
						t.Fatal("malformed profile relationship row accepted")
					}
					return
				}
				if err := loadOneEvolutionProfileChildren(ctx, conn, &profile); err == nil {
					t.Fatal("malformed profile child row accepted")
				}
			})
		})
	}
}

func openEvolutionCoverageConnection(t *testing.T, store *Store) (*sql.DB, *sql.Conn) {
	t.Helper()
	db, err := store.open(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	conn, err := db.Conn(context.Background())
	if err != nil {
		db.Close()
		t.Fatal(err)
	}
	return db, conn
}

func withEvolutionCoverageConnection(
	t *testing.T,
	callback func(Paths, *sql.Conn),
) {
	t.Helper()
	paths := NewPaths(t.TempDir(), "")
	store := NewSQLiteStore(paths)
	db, conn := openEvolutionCoverageConnection(t, store)
	defer db.Close()
	defer conn.Close()
	callback(paths, conn)
}

func evolutionCoverageRecord(paths Paths) LearningRecord {
	return LearningRecord{
		ID: "record", Kind: RecordKindTask, WorkspaceID: paths.Workspace,
		CreatedAt: time.Unix(1_700_000_000, 0).UTC(), Status: "new",
	}
}

func evolutionCoverageDraft(paths Paths) SkillDraft {
	return SkillDraft{
		ID: "draft", WorkspaceID: paths.Workspace,
		TargetSkillName: "weather", DraftType: DraftTypeWorkflow,
		ChangeKind: ChangeKindCreate, Status: DraftStatusCandidate,
	}
}

func evolutionCoverageProfile(paths Paths) SkillProfile {
	return SkillProfile{
		SkillName: "weather", WorkspaceID: paths.Workspace, Status: SkillStatusActive,
	}
}

func seedEvolutionCoverageRecord(t *testing.T, conn *sql.Conn, record LearningRecord) {
	t.Helper()
	if err := insertEvolutionRecord(context.Background(), conn, "task", 0, record, nil); err != nil {
		t.Fatal(err)
	}
	if err := replaceEvolutionRecordChildren(context.Background(), conn, "task", record); err != nil {
		t.Fatal(err)
	}
}

func mustEvolutionCoverageSQL(t *testing.T, conn *sql.Conn, statement string) {
	t.Helper()
	mustEvolutionCoverageSQLArgs(t, conn, statement)
}

func mustEvolutionCoverageSQLArgs(
	t *testing.T,
	conn *sql.Conn,
	statement string,
	arguments ...any,
) {
	t.Helper()
	if _, err := conn.ExecContext(context.Background(), statement, arguments...); err != nil {
		t.Fatal(err)
	}
}

func dropEvolutionCoverageTable(t *testing.T, conn *sql.Conn, table string) {
	t.Helper()
	mustEvolutionCoverageSQL(t, conn, `PRAGMA foreign_keys = OFF`)
	mustEvolutionCoverageSQL(t, conn, `DROP TABLE `+table)
}

func rebuildEvolutionCoverageTable(
	t *testing.T,
	conn *sql.Conn,
	table, schema string,
) {
	t.Helper()
	dropEvolutionCoverageTable(t, conn, table)
	mustEvolutionCoverageSQL(t, conn, `CREATE TABLE `+table+` `+schema)
}

func writeEvolutionCoverageJSON(t *testing.T, path string, value any) {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
}

func containsEvolutionCoverageString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func bytesRepeatForEvolutionCoverage(value byte, count int) []byte {
	return []byte(strings.Repeat(string(value), count))
}
