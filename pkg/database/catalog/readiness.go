package catalog

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/sipeed/picoclaw/internal/sqliteprovider"
	"github.com/sipeed/picoclaw/internal/storecatalog"
	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/database"
)

// ProbeStatuses inspects every trusted generation and returns backend-neutral
// readiness. It initializes or migrates nothing.
func ProbeStatuses(ctx context.Context, home string, cfg *config.Config) ([]database.StoreStatus, error) {
	if !database.BrokerAuthorityHeld() && !database.ProviderTestAuthorityHeld() {
		return nil, database.NewError(
			database.CodeUnauthorized,
			"database readiness inspection requires broker authority",
		)
	}
	physical, err := storecatalog.Build(home, cfg)
	if err != nil {
		return nil, err
	}
	statuses := make([]database.StoreStatus, 0, len(physical.Specs))
	for _, spec := range physical.Specs {
		id, parseErr := database.ParseStoreID(spec.ID)
		if parseErr != nil {
			return nil, parseErr
		}
		inspection, inspectErr := sqliteprovider.Inspect(ctx, spec.Path, 5*time.Second)
		status := database.StoreStatus{ID: id, Readiness: database.StoreReady}
		switch {
		case inspectErr != nil && sqliteprovider.IsInspectionIntegrity(inspectErr):
			status.Readiness = database.StoreIntegrityFailed
			status.Error = database.NewError(database.CodeIntegrity, "database store integrity check failed")
		case inspectErr != nil:
			status.Readiness = database.StoreUnavailable
			status.Error = database.NewError(database.CodeUnavailable, "database store is unavailable")
		case !inspection.Exists:
			legacy, legacyErr := legacyInputExists(spec.LegacyRoots)
			if legacyErr != nil {
				status.Readiness = database.StoreUnavailable
				status.Error = database.NewError(database.CodeUnavailable, "database legacy input is unavailable")
			} else if legacy {
				status.Readiness = database.StoreMigrationRequired
				status.Error = database.NewError(database.CodeMigrationRequired, "database migration is required")
			}
		case inspection.Empty:
			// A physically present but otherwise empty endpoint is equivalent to
			// a missing empty store and may be initialized at current schema.
		default:
			if readinessErr := validateUnversionedReadiness(ctx, spec, &status); readinessErr != nil {
				status.Readiness = database.StoreUnavailable
				status.Error = database.NewError(database.CodeUnavailable, "database schema readiness is unavailable")
				break
			}
			expected := expectedDomainVersion(spec.Domain)
			switch {
			case expected > 0 && inspection.Version < expected:
				status.Readiness = database.StoreMigrationRequired
				status.Error = database.NewError(database.CodeMigrationRequired, "database migration is required")
			case expected > 0 && inspection.Version > expected:
				status.Readiness = database.StoreUnavailable
				status.Error = database.NewError(database.CodeUnsupported, "database schema is newer than supported")
			case expected > 0 && domainUsesSharedImportSchema(spec.Domain):
				ready, readinessErr := sqliteprovider.HasSchemaObjects(
					ctx,
					spec.Path,
					5*time.Second,
					"storage_imports",
					"storage_import_issues",
					"storage_import_horizons",
					"storage_imports_archive_status_idx",
				)
				if readinessErr != nil {
					status.Readiness = database.StoreUnavailable
					status.Error = database.NewError(
						database.CodeUnavailable,
						"database schema readiness is unavailable",
					)
				} else if !ready {
					status.Readiness = database.StoreMigrationRequired
					status.Error = database.NewError(
						database.CodeMigrationRequired,
						"database migration is required",
					)
				}
			}
		}
		if (!inspection.Exists || inspection.Empty) &&
			(spec.Domain == "channel-matrix" || spec.Domain == "channel-whatsapp") {
			status.Readiness = database.StoreMigrationRequired
			status.Error = database.NewError(database.CodeMigrationRequired, "database migration is required")
		}
		statuses = append(statuses, status)
	}
	return database.ValidateStoreStatuses(statuses)
}

func domainUsesSharedImportSchema(domain string) bool {
	switch domain {
	case "auth", "launcher-auth", "model-catalogs", "tool-adaptation",
		"workflows", "sessions", "cron", "runtime-state", "account-routing",
		"repository-reviews", "repository-evaluations", "evolution", "local-ci",
		"channel-wecom", "channel-weixin", "git-workspace-inventory",
		"pr-workspace-checkpoints":
		return true
	default:
		return false
	}
}

func validateUnversionedReadiness(
	ctx context.Context,
	spec storecatalog.Spec,
	status *database.StoreStatus,
) error {
	var objects []string
	switch spec.Domain {
	case "channel-matrix":
		objects = []string{"crypto_version", "mx_version"}
	case "channel-whatsapp":
		objects = []string{"whatsmeow_version"}
	case "seahorse":
		objects = []string{
			"conversations", "messages", "message_parts", "summaries",
			"summary_parents", "summary_messages", "context_items",
			"summaries_fts", "messages_fts",
		}
	default:
		return nil
	}
	ready, err := sqliteprovider.HasSchemaObjects(ctx, spec.Path, 5*time.Second, objects...)
	if err != nil {
		return err
	}
	if ready && spec.Domain == "seahorse" {
		ready, err = sqliteprovider.HasTableColumns(
			ctx, spec.Path, 5*time.Second, "messages", "model_name", "reasoning_content",
		)
		if err != nil {
			return err
		}
	}
	if !ready {
		status.Readiness = database.StoreMigrationRequired
		status.Error = database.NewError(database.CodeMigrationRequired, "database migration is required")
	}
	return nil
}

// RequireReady fails when any enabled required catalog store is not ready.
func RequireReady(logical *Catalog, statuses []database.StoreStatus) error {
	if logical == nil {
		return database.NewError(database.CodeUnavailable, "database catalog is unavailable")
	}
	byID := make(map[database.StoreID]database.StoreStatus, len(statuses))
	for _, status := range statuses {
		byID[status.ID] = status
	}
	for _, entry := range logical.Entries() {
		if !entry.Required {
			continue
		}
		status, ok := byID[entry.ID]
		if !ok {
			return database.NewError(database.CodeUnavailable, "required database store has no readiness status")
		}
		if status.Readiness == database.StoreReady {
			continue
		}
		if status.Error != nil {
			return database.NewError(status.Error.Code, status.Error.Message)
		}
		return database.NewError(database.CodeUnavailable, "required database store is not ready")
	}
	return nil
}

// InitializeRequired synchronously initializes or proves every required store
// already classified ready. Non-ready stores are never opened, so legacy and
// outdated generations remain migration-required. Optional stores remain lazy.
// Initialization failures are reflected in the returned status snapshot so the
// broker remains available for maintenance while runtime admission fails.
func InitializeRequired(
	ctx context.Context,
	logical *Catalog,
	statuses []database.StoreStatus,
	initialize func(context.Context, Entry) error,
) ([]database.StoreStatus, error) {
	if logical == nil || initialize == nil {
		return nil, database.NewError(database.CodeInvalid, "database readiness initializer is invalid")
	}
	validated, err := database.ValidateStoreStatuses(statuses)
	if err != nil {
		return nil, err
	}
	byID := make(map[database.StoreID]int, len(validated))
	for index := range validated {
		byID[validated[index].ID] = index
	}
	for _, entry := range logical.Entries() {
		if !entry.Required {
			continue
		}
		index, found := byID[entry.ID]
		if !found {
			return nil, database.NewError(
				database.CodeIntegrity,
				"required database store has no readiness status",
			)
		}
		if validated[index].Readiness != database.StoreReady {
			continue
		}
		if err := initialize(ctx, entry); err != nil {
			code := database.CodeOf(err)
			switch code {
			case database.CodeMigrationRequired:
				validated[index].Readiness = database.StoreMigrationRequired
			case database.CodeIntegrity:
				validated[index].Readiness = database.StoreIntegrityFailed
			case database.CodeUnsupported, database.CodeUnavailable:
				validated[index].Readiness = database.StoreUnavailable
			default:
				code = database.CodeUnavailable
				validated[index].Readiness = database.StoreUnavailable
			}
			validated[index].Error = database.NewError(code, "database store readiness initialization failed")
		}
	}
	return database.ValidateStoreStatuses(validated)
}

func expectedDomainVersion(domain string) int {
	switch domain {
	case "eventing":
		return 20
	case "git-workspace-inventory":
		return 2
	case "pr-workspace-checkpoints":
		return 3
	case "auth", "launcher-auth", "model-catalogs", "tool-adaptation",
		"workflows", "sessions", "cron", "runtime-state", "account-routing",
		"repository-reviews", "repository-evaluations", "evolution", "local-ci",
		"channel-wecom", "channel-weixin", "channel-matrix", "channel-whatsapp", "seahorse":
		return 1
	default:
		return 0
	}
}

func legacyInputExists(paths []string) (bool, error) {
	for _, path := range paths {
		info, err := os.Lstat(path)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return false, err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return false, errors.New("legacy input is a symlink")
		}
		if info.Mode().IsRegular() {
			if !databaseGenerationLike(path) {
				return true, nil
			}
			continue
		}
		if !info.IsDir() {
			return false, errors.New("legacy input is not a regular file or directory")
		}
		found := errors.New("legacy input found")
		walkErr := filepath.WalkDir(path, func(candidate string, entry fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if candidate == path {
				return nil
			}
			if entry.Type()&os.ModeSymlink != 0 {
				return errors.New("legacy input tree contains a symlink")
			}
			if entry.IsDir() {
				switch entry.Name() {
				case "legacy-json", "backups", ".database":
					return filepath.SkipDir
				}
				return nil
			}
			if !databaseGenerationLike(candidate) {
				return found
			}
			return nil
		})
		if errors.Is(walkErr, found) {
			return true, nil
		}
		if walkErr != nil {
			return false, walkErr
		}
	}
	return false, nil
}

func databaseGenerationLike(path string) bool {
	name := strings.ToLower(filepath.Base(path))
	return strings.HasSuffix(name, ".db") || strings.HasSuffix(name, ".db-wal") ||
		strings.HasSuffix(name, ".db-shm") || strings.HasSuffix(name, ".lock") || name == "store"
}
