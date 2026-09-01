package repoaudit

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Legacy JSON helpers live only in tests for compatibility fixtures. Runtime
// persistence never reads or writes these paths after SQLite migration.
func repositoryReviewSummaryFromEntry(root string, entry os.DirEntry) (RepositorySummary, error) {
	statePath := filepath.Join(root, entry.Name())
	stateInfo, infoErr := entry.Info()
	if infoErr != nil || stateInfo.Size() > maxStateFileBytes {
		return RepositorySummary{}, errors.Join(infoErr, errors.New("repository review state exceeds its size limit"))
	}
	summaryPath := strings.TrimSuffix(statePath, ".json") + ".summary.json"
	readPath := statePath
	if summaryInfo, summaryErr := os.Lstat(summaryPath); summaryErr == nil {
		if summaryInfo.Mode()&os.ModeSymlink != 0 || !summaryInfo.Mode().IsRegular() {
			return RepositorySummary{}, errors.New("repository review summary must be a regular file")
		}
		if !summaryInfo.ModTime().Before(stateInfo.ModTime()) {
			readPath = summaryPath
		}
	} else if !os.IsNotExist(summaryErr) {
		return RepositorySummary{}, summaryErr
	}
	file, openErr := os.Open(readPath)
	if openErr != nil {
		return RepositorySummary{}, openErr
	}
	var summary RepositorySummary
	decodeErr := json.NewDecoder(file).Decode(&summary)
	closeErr := file.Close()
	if decodeErr != nil || closeErr != nil {
		return RepositorySummary{}, errors.Join(decodeErr, closeErr)
	}
	if (summary.SchemaVersion < 1 || summary.SchemaVersion > SchemaVersion) ||
		summary.ID != RepositoryID(summary.Repository) {
		return RepositorySummary{}, errors.New("invalid repository review summary")
	}
	return summary, nil
}

func repositoryReviewStateFromEntry(root string, entry os.DirEntry) (RepositoryState, error) {
	info, infoErr := entry.Info()
	if infoErr != nil {
		return RepositoryState{}, infoErr
	}
	if info.Size() > maxStateFileBytes {
		return RepositoryState{}, errors.New("repository review state exceeds its size limit")
	}
	data, readErr := os.ReadFile(filepath.Join(root, entry.Name()))
	if readErr != nil {
		return RepositoryState{}, readErr
	}
	var state RepositoryState
	if jsonErr := json.Unmarshal(data, &state); jsonErr != nil {
		return RepositoryState{}, jsonErr
	}
	if _, migrationErr := migrateRepositoryState(&state); migrationErr != nil {
		return RepositoryState{}, migrationErr
	}
	backfillCanonicalIssueAssociations(&state)
	if err := validateState(state); err != nil {
		return RepositoryState{}, err
	}
	return state, nil
}

func (s Store) requireSafeRoot(allowMissing bool) error {
	info, err := os.Lstat(s.root)
	if os.IsNotExist(err) && allowMissing {
		return nil
	}
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return errors.New("repository review storage root must be a real directory")
	}
	return nil
}

func (s Store) ensureSafeRoot(mkdir func(string, os.FileMode) error) error {
	if err := s.requireSafeRoot(true); err != nil {
		return err
	}
	if err := mkdir(s.root, 0o700); err != nil {
		return err
	}
	return s.requireSafeRoot(false)
}

func (s Store) path(repository string) string {
	return filepath.Join(s.root, stableID("repo_", strings.TrimSpace(repository))+".json")
}

func repositoryReviewStateFilename(name string) bool {
	return strings.HasPrefix(name, "repo_") && strings.HasSuffix(name, ".json") &&
		!strings.HasSuffix(name, ".summary.json")
}

type legacyAutomationPriceMetadata struct {
	ModelPrices map[string]map[string]json.RawMessage `json:"model_prices"`
}

func decodeLegacyAutomationPriceMetadata(data []byte) (legacyAutomationPriceMetadata, error) {
	var legacy legacyAutomationPriceMetadata
	err := json.Unmarshal(data, &legacy)
	return legacy, err
}

func (s Store) automationPath(id string) string {
	return filepath.Join(s.root, automationFilename(id))
}

func (s Store) profilePath(id string) string {
	return filepath.Join(s.root, profileFilename(id))
}
