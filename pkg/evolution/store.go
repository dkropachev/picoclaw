package evolution

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/sipeed/picoclaw/pkg/sqlitestore"
)

type Store struct {
	paths Paths
}

var evolutionDatabaseWriteLocks sync.Map

// NewSQLiteStore constructs the authoritative evolution SQLite store. Schema
// creation and legacy migration occur on the first operation so construction
// remains source-compatible with the historical no-error constructor.
func NewSQLiteStore(paths Paths) *Store {
	return &Store{paths: normalizedEvolutionPaths(paths)}
}

// NewStore is retained for one compatibility cycle. It is backed exclusively
// by evolution.db and never writes the legacy JSON or JSONL sources.
// Deprecated: use NewSQLiteStore.
func NewStore(paths Paths) *Store { return NewSQLiteStore(paths) }

// Close is provided for constructor symmetry. Store operations use bounded
// database handles and do not retain a process-local connection.
func (s *Store) Close() error { return nil }

func (s *Store) open(ctx context.Context) (*sql.DB, error) {
	if s == nil {
		return nil, errors.New("evolution store is nil")
	}
	paths := normalizedEvolutionPaths(s.paths)
	return sqlitestore.Open(ctx, paths.Database, evolutionStoreOptions(paths))
}

func (s *Store) immediate(ctx context.Context, callback func(*sql.Conn) error) error {
	if s == nil {
		return errors.New("evolution store is nil")
	}
	paths := normalizedEvolutionPaths(s.paths)
	databasePath, err := filepath.Abs(filepath.Clean(paths.Database))
	if err != nil {
		return err
	}
	actual, _ := evolutionDatabaseWriteLocks.LoadOrStore(databasePath, &sync.Mutex{})
	mutex, ok := actual.(*sync.Mutex)
	if !ok || mutex == nil {
		return errors.New("evolution database write lock is invalid")
	}
	mutex.Lock()
	defer mutex.Unlock()
	db, err := s.open(ctx)
	if err != nil {
		return err
	}
	defer db.Close()
	return sqlitestore.Immediate(ctx, db, callback)
}

func (s *Store) AppendLearningRecord(ctx context.Context, record LearningRecord) error {
	return s.AppendTaskOrPatternRecords(ctx, []LearningRecord{record})
}

func (s *Store) AppendLearningRecords(records []LearningRecord) error {
	return s.AppendTaskOrPatternRecords(context.Background(), records)
}

func (s *Store) AppendTaskOrPatternRecords(ctx context.Context, records []LearningRecord) error {
	if len(records) == 0 {
		return nil
	}
	for _, record := range records {
		class := evolutionRecordClass(record)
		if err := validateEvolutionRecord(class, record); err != nil {
			return err
		}
	}
	return s.immediate(ctx, func(conn *sql.Conn) error {
		for _, record := range records {
			if _, err := putEvolutionRecord(
				ctx, conn, evolutionRecordClass(record), record, "", true,
			); err != nil {
				return err
			}
		}
		return nil
	})
}

func (s *Store) AppendTaskRecord(ctx context.Context, record LearningRecord) error {
	return s.AppendTaskRecords(ctx, []LearningRecord{record})
}

func (s *Store) AppendTaskRecords(ctx context.Context, records []LearningRecord) error {
	return s.appendRecords(ctx, "task", records)
}

func (s *Store) AppendPatternRecords(records []LearningRecord) error {
	return s.appendRecords(context.Background(), "pattern", records)
}

func (s *Store) appendRecords(ctx context.Context, class string, records []LearningRecord) error {
	if len(records) == 0 {
		return nil
	}
	for _, record := range records {
		if err := validateEvolutionRecord(class, record); err != nil {
			return err
		}
	}
	return s.immediate(ctx, func(conn *sql.Conn) error {
		for _, record := range records {
			if _, err := putEvolutionRecord(ctx, conn, class, record, "", true); err != nil {
				return err
			}
		}
		return nil
	})
}

func (s *Store) LoadLearningRecords() ([]LearningRecord, error) {
	tasks, err := s.LoadTaskRecords()
	if err != nil {
		return nil, err
	}
	patterns, err := s.LoadPatternRecords()
	if err != nil {
		return nil, err
	}
	return append(tasks, patterns...), nil
}

func (s *Store) LoadTaskRecords() ([]LearningRecord, error) {
	return s.loadRecords(context.Background(), "task")
}

func (s *Store) LoadPatternRecords() ([]LearningRecord, error) {
	return s.loadRecords(context.Background(), "pattern")
}

func (s *Store) loadRecords(ctx context.Context, class string) ([]LearningRecord, error) {
	db, err := s.open(ctx)
	if err != nil {
		return nil, err
	}
	defer db.Close()
	return loadEvolutionRecords(ctx, db, class)
}

func (s *Store) SaveTaskRecords(records []LearningRecord) error {
	return s.saveRecords(context.Background(), "task", records)
}

func (s *Store) SavePatternRecords(records []LearningRecord) error {
	return s.saveRecords(context.Background(), "pattern", records)
}

func (s *Store) saveRecords(ctx context.Context, class string, records []LearningRecord) error {
	if len(records) > maximumEvolutionRecords {
		return errors.New("evolution record count exceeds its limit")
	}
	for _, record := range records {
		if err := validateEvolutionRecord(class, record); err != nil {
			return err
		}
	}
	return s.immediate(ctx, func(conn *sql.Conn) error {
		return replaceEvolutionRecords(ctx, conn, class, records)
	})
}

func (s *Store) MarkTaskRecordsClustered(ids []string) error {
	target := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		id = strings.TrimSpace(id)
		if id != "" {
			target[id] = struct{}{}
		}
	}
	if len(target) == 0 {
		return nil
	}
	workspace := strings.TrimSpace(s.paths.Workspace)
	return s.immediate(context.Background(), func(conn *sql.Conn) error {
		for id := range target {
			rows, err := conn.QueryContext(context.Background(), `SELECT workspace_id, version
                    FROM evolution_records
                   WHERE record_class = 'task' AND record_id = ?
                   ORDER BY position`, id)
			if err != nil {
				return err
			}
			type candidate struct {
				workspace string
				version   int64
			}
			var candidates []candidate
			hasWorkspace := false
			for rows.Next() {
				var value candidate
				if err := rows.Scan(&value.workspace, &value.version); err != nil {
					rows.Close()
					return err
				}
				hasWorkspace = hasWorkspace || workspace != "" && value.workspace == workspace
				candidates = append(candidates, value)
			}
			if err := rows.Err(); err != nil {
				rows.Close()
				return err
			}
			if err := rows.Close(); err != nil {
				return err
			}
			for _, candidate := range candidates {
				if hasWorkspace && candidate.workspace != workspace {
					continue
				}
				result, err := conn.ExecContext(context.Background(), `UPDATE evolution_records
                        SET status = 'clustered', version = version + 1, import_source = NULL
                      WHERE record_class = 'task' AND workspace_id = ? AND record_id = ? AND version = ?`,
					candidate.workspace, id, candidate.version)
				if err != nil {
					return err
				}
				changed, err := result.RowsAffected()
				if err != nil || changed != 1 {
					return errors.New("evolution task record changed during clustered update")
				}
			}
		}
		return nil
	})
}

func (s *Store) MergePatternRecords(records []LearningRecord) error {
	if len(records) == 0 {
		return nil
	}
	for _, record := range records {
		if err := validateEvolutionRecord("pattern", record); err != nil {
			return err
		}
	}
	return s.immediate(context.Background(), func(conn *sql.Conn) error {
		current, err := loadEvolutionRecords(context.Background(), conn, "pattern")
		if err != nil {
			return err
		}
		return replaceEvolutionRecords(
			context.Background(), conn, "pattern", mergeLearningRecordsByID(current, records),
		)
	})
}

func mergeLearningRecordsByID(base, updates []LearningRecord) []LearningRecord {
	out := append([]LearningRecord(nil), base...)
	indexByID := make(map[string]int, len(out)+len(updates))
	for index, record := range out {
		indexByID[learningRecordMergeKey(record)] = index
	}
	for _, record := range updates {
		key := learningRecordMergeKey(record)
		if index, found := indexByID[key]; found {
			out[index] = record
			continue
		}
		indexByID[key] = len(out)
		out = append(out, record)
	}
	return out
}

func learningRecordMergeKey(record LearningRecord) string {
	return strings.TrimSpace(record.WorkspaceID) + "\x00" + strings.TrimSpace(record.ID)
}

func (s *Store) SaveDrafts(drafts []SkillDraft) error {
	if len(drafts) == 0 {
		return nil
	}
	for _, draft := range drafts {
		if err := validateEvolutionDraft(draft); err != nil {
			return err
		}
	}
	return s.immediate(context.Background(), func(conn *sql.Conn) error {
		for _, draft := range drafts {
			if _, err := putEvolutionDraft(context.Background(), conn, draft, false); err != nil {
				return err
			}
		}
		return nil
	})
}

func (s *Store) LoadDrafts() ([]SkillDraft, error) {
	db, err := s.open(context.Background())
	if err != nil {
		return nil, err
	}
	defer db.Close()
	return loadEvolutionDrafts(context.Background(), db)
}

func (s *Store) SaveProfile(profile SkillProfile) error {
	if err := validateEvolutionProfile(profile); err != nil {
		return err
	}
	return s.immediate(context.Background(), func(conn *sql.Conn) error {
		_, err := putEvolutionProfile(context.Background(), conn, profile, false)
		return err
	})
}

func (s *Store) LoadProfile(skillName string) (SkillProfile, error) {
	if err := validateEvolutionSkillName(skillName); err != nil {
		return SkillProfile{}, err
	}
	db, err := s.open(context.Background())
	if err != nil {
		return SkillProfile{}, err
	}
	defer db.Close()
	workspace := strings.TrimSpace(s.paths.Workspace)
	profile, found, err := loadEvolutionProfile(context.Background(), db, workspace, skillName)
	if err != nil {
		return SkillProfile{}, err
	}
	if found {
		return profile, nil
	}
	if workspace != "" && usesDefaultWorkspaceState(s.paths, workspace) {
		profile, found, err = loadEvolutionProfile(context.Background(), db, "", skillName)
		if err != nil {
			return SkillProfile{}, err
		}
		if found {
			return profile, nil
		}
	}
	return SkillProfile{}, os.ErrNotExist
}

func (s *Store) UpdateProfile(
	workspaceID, skillName string,
	update func(profile *SkillProfile, exists bool) error,
) error {
	workspaceID = strings.TrimSpace(workspaceID)
	if update == nil {
		return errors.New("evolution profile update is nil")
	}
	if err := validateEvolutionSkillName(skillName); err != nil {
		return err
	}
	return s.immediate(context.Background(), func(conn *sql.Conn) error {
		profile, exists, err := loadEvolutionProfile(
			context.Background(), conn, workspaceID, skillName,
		)
		if err != nil {
			return err
		}
		loadedFallback := false
		if !exists && workspaceID != "" && usesDefaultWorkspaceState(s.paths, workspaceID) {
			profile, exists, err = loadEvolutionProfile(
				context.Background(), conn, "", skillName,
			)
			if err != nil {
				return err
			}
			loadedFallback = exists
		}
		if err := update(&profile, exists); err != nil {
			return err
		}
		if !exists && isZeroSkillProfile(profile) {
			return nil
		}
		if loadedFallback {
			profile.WorkspaceID = workspaceID
			profile.SkillName = skillName
		}
		if profile.WorkspaceID != workspaceID || profile.SkillName != skillName {
			return errors.New("evolution profile update changed its identity")
		}
		if err := validateEvolutionProfile(profile); err != nil {
			return err
		}
		_, err = putEvolutionProfile(context.Background(), conn, profile, false)
		return err
	})
}

func (s *Store) LoadProfiles() ([]SkillProfile, error) {
	db, err := s.open(context.Background())
	if err != nil {
		return nil, err
	}
	defer db.Close()
	return loadEvolutionProfiles(context.Background(), db)
}

func isZeroSkillProfile(profile SkillProfile) bool {
	return profile.SkillName == "" && profile.WorkspaceID == "" && profile.CurrentVersion == "" &&
		profile.Status == "" && profile.Origin == "" && profile.HumanSummary == "" &&
		profile.ChangeReason == "" && len(profile.IntendedUseCases) == 0 &&
		len(profile.PreferredEntryPath) == 0 && len(profile.AvoidPatterns) == 0 &&
		profile.LastUsedAt.IsZero() && profile.UseCount == 0 && profile.RetentionScore == 0 &&
		len(profile.VersionHistory) == 0
}

func draftKey(workspaceID, id string) string { return workspaceID + "\x00" + id }

func evolutionRecordClass(record LearningRecord) string {
	switch record.Kind {
	case RecordKindPattern, legacyRecordKindRule:
		return "pattern"
	default:
		return "task"
	}
}
