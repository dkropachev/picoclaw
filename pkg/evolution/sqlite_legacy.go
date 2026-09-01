package evolution

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
	"sort"
	"strings"

	"github.com/sipeed/picoclaw/pkg/sqlitestore"
)

const maximumEvolutionLegacyLineBytes = 16 << 20

func evolutionLegacySources(paths Paths) ([]sqlitestore.LegacySource, error) {
	sources := []sqlitestore.LegacySource{
		{ID: "learning-records", Relative: filepath.Base(paths.LearningRecords)},
		{ID: "task-records", Relative: filepath.Base(paths.TaskRecords)},
		{ID: "pattern-records", Relative: filepath.Base(paths.PatternRecords)},
		{ID: "skill-drafts", Relative: filepath.Base(paths.SkillDrafts)},
	}
	profileRoot := paths.ProfilesDir
	_, statErr := os.Lstat(profileRoot)
	if errors.Is(statErr, os.ErrNotExist) {
		return sources, nil
	}
	if statErr != nil {
		return nil, fmt.Errorf("inspect legacy evolution profiles: %w", statErr)
	}
	if err := validateEvolutionLegacyDirectory(profileRoot); err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(profileRoot)
	if err != nil {
		return nil, fmt.Errorf("enumerate legacy evolution profiles: %w", err)
	}
	for _, entry := range entries {
		path := filepath.Join(profileRoot, entry.Name())
		info, err := os.Lstat(path)
		if err != nil {
			return nil, err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return nil, errors.New("legacy evolution profiles contain a symlink")
		}
		if info.IsDir() {
			if err := validateEvolutionLegacyDirectory(path); err != nil {
				return nil, err
			}
			nested, err := os.ReadDir(path)
			if err != nil {
				return nil, err
			}
			for _, child := range nested {
				childPath := filepath.Join(path, child.Name())
				childInfo, err := os.Lstat(childPath)
				if err != nil {
					return nil, err
				}
				if childInfo.Mode()&os.ModeSymlink != 0 || childInfo.IsDir() {
					return nil, errors.New("legacy evolution profile scope contains an unsafe entry")
				}
				if err := validateEvolutionLegacyFile(childInfo); err != nil {
					return nil, err
				}
				if filepath.Ext(child.Name()) == ".json" {
					source, err := evolutionProfileLegacySource(paths.RootDir, childPath)
					if err != nil {
						return nil, err
					}
					sources = append(sources, source)
				}
			}
			continue
		}
		if err := validateEvolutionLegacyFile(info); err != nil {
			return nil, err
		}
		if filepath.Ext(entry.Name()) == ".json" {
			source, err := evolutionProfileLegacySource(paths.RootDir, path)
			if err != nil {
				return nil, err
			}
			sources = append(sources, source)
		}
	}
	if len(sources) > maximumEvolutionLegacySources {
		return nil, errors.New("legacy evolution source count exceeds its limit")
	}
	sort.Slice(sources, func(left, right int) bool { return sources[left].Relative < sources[right].Relative })
	return sources, nil
}

func validateEvolutionLegacyFile(info os.FileInfo) error {
	if info == nil || !info.Mode().IsRegular() ||
		info.Mode()&(os.ModeSetuid|os.ModeSetgid|os.ModeSticky) != 0 ||
		info.Mode().Perm()&0o022 != 0 {
		return errors.New("legacy evolution profile file is unsafe")
	}
	return nil
}

func validateEvolutionLegacyDirectory(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 ||
		info.Mode()&(os.ModeSetuid|os.ModeSetgid|os.ModeSticky) != 0 ||
		info.Mode().Perm()&0o022 != 0 {
		return errors.New("legacy evolution profile directory is unsafe")
	}
	return nil
}

func evolutionProfileLegacySource(root, path string) (sqlitestore.LegacySource, error) {
	relative, err := filepath.Rel(root, path)
	if err != nil {
		return sqlitestore.LegacySource{}, err
	}
	relative = filepath.ToSlash(relative)
	digest := sha256.Sum256([]byte(relative))
	return sqlitestore.LegacySource{
		ID:       "profile-" + hex.EncodeToString(digest[:]),
		Relative: relative,
	}, nil
}

func importEvolutionLegacySource(
	ctx context.Context,
	conn *sql.Conn,
	input sqlitestore.LegacyInput,
) (sqlitestore.ImportResult, error) {
	switch input.Relative {
	case "learning-records.jsonl":
		return importEvolutionLegacyRecords(ctx, conn, input, "learning-records", "")
	case "task-records.jsonl":
		return importEvolutionLegacyRecords(ctx, conn, input, "task-records", "task")
	case "pattern-records.jsonl":
		return importEvolutionLegacyRecords(ctx, conn, input, "pattern-records", "pattern")
	case "skill-drafts.json":
		return importEvolutionLegacyDrafts(ctx, conn, input)
	default:
		if strings.HasPrefix(input.Relative, "profiles/") && filepath.Ext(input.Relative) == ".json" {
			return importEvolutionLegacyProfile(ctx, conn, input)
		}
	}
	return sqlitestore.ImportResult{}, errors.New("unknown evolution legacy source")
}

func importEvolutionLegacyRecords(
	ctx context.Context,
	conn *sql.Conn,
	input sqlitestore.LegacyInput,
	importSource, fixedClass string,
) (sqlitestore.ImportResult, error) {
	lines := bytes.Split(input.Data, []byte{'\n'})
	if len(lines) > maximumEvolutionRecords*2+1 {
		return sqlitestore.ImportResult{}, errors.New("legacy evolution record count exceeds its limit")
	}
	result := sqlitestore.ImportResult{}
	for _, line := range lines {
		line = bytes.TrimSpace(line)
		if len(line) == 0 {
			continue
		}
		if len(line) > maximumEvolutionLegacyLineBytes {
			return sqlitestore.ImportResult{}, errors.New("legacy evolution record exceeds its size limit")
		}
		digest := sha256.Sum256(line)
		var record LearningRecord
		if err := json.Unmarshal(line, &record); err != nil || bytes.Equal(line, []byte("null")) {
			addEvolutionImportIssue(&result, "malformed-record", digest)
			continue
		}
		class := fixedClass
		if class == "" {
			class = evolutionRecordClass(record)
		}
		if err := validateEvolutionRecord(class, record); err != nil {
			addEvolutionImportIssue(&result, "invalid-record", digest)
			continue
		}
		written, err := putEvolutionRecord(ctx, conn, class, record, importSource, false)
		if err != nil {
			return sqlitestore.ImportResult{}, err
		}
		if written {
			result.Imported++
		} else {
			addEvolutionImportIssue(&result, "identity-conflict", digest)
		}
	}
	return result, nil
}

func importEvolutionLegacyDrafts(
	ctx context.Context,
	conn *sql.Conn,
	input sqlitestore.LegacyInput,
) (sqlitestore.ImportResult, error) {
	trimmed := bytes.TrimSpace(input.Data)
	if len(trimmed) == 0 {
		return sqlitestore.ImportResult{}, nil
	}
	var rawDrafts []json.RawMessage
	if err := json.Unmarshal(trimmed, &rawDrafts); err != nil {
		// Malformed legacy payloads are audited skips, not store-open failures.
		//nolint:nilerr
		return malformedEvolutionImport(input.Digest, "malformed-drafts"), nil
	}
	if len(rawDrafts) > maximumEvolutionDrafts {
		return sqlitestore.ImportResult{}, errors.New("legacy evolution draft count exceeds its limit")
	}
	result := sqlitestore.ImportResult{}
	for _, raw := range rawDrafts {
		digest := sha256.Sum256(raw)
		var draft SkillDraft
		if err := json.Unmarshal(raw, &draft); err != nil || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) ||
			validateEvolutionDraft(draft) != nil {
			addEvolutionImportIssue(&result, "invalid-draft", digest)
			continue
		}
		written, err := putEvolutionDraftImport(ctx, conn, draft)
		if err != nil {
			return sqlitestore.ImportResult{}, err
		}
		if written {
			result.Imported++
		} else {
			addEvolutionImportIssue(&result, "identity-conflict", digest)
		}
	}
	return result, nil
}

func putEvolutionDraftImport(ctx context.Context, conn *sql.Conn, draft SkillDraft) (bool, error) {
	return putEvolutionDraft(ctx, conn, draft, true)
}

func importEvolutionLegacyProfile(
	ctx context.Context,
	conn *sql.Conn,
	input sqlitestore.LegacyInput,
) (sqlitestore.ImportResult, error) {
	trimmed := bytes.TrimSpace(input.Data)
	digest := sha256.Sum256(trimmed)
	var profile SkillProfile
	if len(trimmed) == 0 || json.Unmarshal(trimmed, &profile) != nil ||
		bytes.Equal(trimmed, []byte("null")) || validateEvolutionProfile(profile) != nil ||
		!evolutionProfilePathConsistent(input.Relative, profile) {
		// Invalid legacy profiles are audited skips, not store-open failures.
		//nolint:nilerr
		return malformedEvolutionImport(digest, "invalid-profile"), nil
	}
	written, err := putEvolutionProfile(ctx, conn, profile, true)
	if err != nil {
		return sqlitestore.ImportResult{}, err
	}
	if !written {
		return malformedEvolutionImport(digest, "identity-conflict"), nil
	}
	return sqlitestore.ImportResult{Imported: 1}, nil
}

func evolutionProfilePathConsistent(relative string, profile SkillProfile) bool {
	parts := strings.Split(filepath.ToSlash(relative), "/")
	if len(parts) != 2 && len(parts) != 3 {
		return false
	}
	if strings.TrimSuffix(parts[len(parts)-1], ".json") != profile.SkillName {
		return false
	}
	if len(parts) == 3 {
		return profile.WorkspaceID != "" && parts[1] == workspaceScopeDir(profile.WorkspaceID)
	}
	return true
}

func addEvolutionImportIssue(
	result *sqlitestore.ImportResult,
	code string,
	digest [sha256.Size]byte,
) {
	result.Skipped++
	if len(result.Issues) < maximumEvolutionAuditIssues {
		result.Issues = append(result.Issues, sqlitestore.ImportIssue{
			Code:         code,
			RecordDigest: digest,
		})
	}
}

func malformedEvolutionImport(
	digest [sha256.Size]byte,
	code string,
) sqlitestore.ImportResult {
	return sqlitestore.ImportResult{
		Skipped: 1,
		Issues: []sqlitestore.ImportIssue{{
			Code:         code,
			RecordDigest: digest,
		}},
	}
}
