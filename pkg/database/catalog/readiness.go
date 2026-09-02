package catalog

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/sipeed/picoclaw/internal/sqliteprovider"
	"github.com/sipeed/picoclaw/internal/storecatalog"
	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/database"
)

var readinessPoolPaths sync.Map

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
	if closeErr := CloseProbePools(physical.Home); closeErr != nil {
		return nil, database.NewError(
			database.CodeUnavailable,
			"previous database readiness pools could not be closed",
		)
	}
	paths := make([]string, 0, len(physical.Specs))
	for _, spec := range physical.Specs {
		paths = append(paths, spec.Path)
	}
	retained := false
	defer func() {
		if !retained {
			_ = sqliteprovider.CloseInspectedPools(paths)
		}
	}()
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
			if readinessErr := validateUnversionedReadiness(
				ctx, inspection, spec, &status,
			); readinessErr != nil {
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
			case expected > 0 && importHorizonComponent(spec.Domain) != "":
				ready, readinessErr := inspection.HasSchemaObjects(
					ctx,
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
				} else {
					closed, horizonErr := inspection.HasImportHorizon(
						ctx, importHorizonComponent(spec.Domain),
					)
					if horizonErr != nil {
						status.Readiness = database.StoreIntegrityFailed
						status.Error = database.NewError(
							database.CodeIntegrity,
							"database import readiness is invalid",
						)
					} else if !closed {
						status.Readiness = database.StoreMigrationRequired
						status.Error = database.NewError(
							database.CodeMigrationRequired,
							"database migration is required",
						)
					}
				}
			}
		}
		if (!inspection.Exists || inspection.Empty) &&
			(spec.Domain == "channel-matrix" || spec.Domain == "channel-whatsapp") {
			status.Readiness = database.StoreMigrationRequired
			status.Error = database.NewError(database.CodeMigrationRequired, "database migration is required")
		}
		if status.Readiness != database.StoreReady {
			if releaseErr := inspection.Release(); releaseErr != nil {
				status.Readiness = database.StoreUnavailable
				status.Error = database.NewError(
					database.CodeUnavailable,
					"database readiness pool could not be closed",
				)
			}
		}
		statuses = append(statuses, status)
	}
	validated, err := database.ValidateStoreStatuses(statuses)
	if err != nil {
		return nil, err
	}
	readinessPoolPaths.Store(readinessHomeKey(physical.Home), append([]string(nil), paths...))
	retained = true
	return validated, nil
}

func importHorizonComponent(domain string) string {
	switch domain {
	case "auth":
		return "auth"
	case "model-catalogs":
		return "model-catalogs"
	case "tool-adaptation":
		return "tool-adaptation"
	case "workflows":
		return "workflows"
	case "sessions":
		return "sessions"
	case "cron":
		return "cron-jobs"
	case "runtime-state":
		return "runtime-state"
	case "account-routing":
		return "account-router"
	case "repository-reviews":
		return "repository-reviews"
	case "repository-evaluations":
		return "repository-evaluations"
	case "evolution":
		return "evolution"
	case "local-ci":
		return "local_ci_cache"
	case "channel-wecom":
		return "wecom-reqid"
	case "channel-weixin":
		return "weixin-state"
	case "git-workspace-inventory":
		return "git-workspace-inventory"
	case "pr-workspace-checkpoints":
		return "pr-workspace-checkpoints"
	default:
		return ""
	}
}

func validateUnversionedReadiness(
	ctx context.Context,
	inspection sqliteprovider.Inspection,
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
	ready, err := inspection.HasSchemaObjects(ctx, objects...)
	if err != nil {
		return err
	}
	if ready && spec.Domain == "seahorse" {
		ready, err = inspection.HasTableColumns(
			ctx, "messages", "model_name", "reasoning_content",
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

// CloseProbePools closes readiness pools that no typed domain handler adopted.
// It exposes no physical identity and is used only by broker lifecycle code.
func CloseProbePools(home string) error {
	key := readinessHomeKey(home)
	value, ok := readinessPoolPaths.LoadAndDelete(key)
	if !ok {
		return nil
	}
	paths, _ := value.([]string)
	return sqliteprovider.CloseInspectedPools(paths)
}

func readinessHomeKey(home string) string {
	absolute, err := filepath.Abs(filepath.Clean(home))
	if err != nil {
		absolute = filepath.Clean(home)
	}
	if runtime.GOOS == "windows" || runtime.GOOS == "darwin" {
		absolute = strings.ToLower(absolute)
	}
	return absolute
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

// InitializeRequired synchronously proves every catalog store already
// classified ready through its typed domain adapter. Non-ready stores are never
// opened, so legacy and outdated generations remain migration-required.
// Optional-store failures remain visible in status without blocking admission;
// required-store failures still prevent runtime startup.
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
		index, found := byID[entry.ID]
		if !found {
			return nil, database.NewError(
				database.CodeIntegrity,
				"database store has no readiness status",
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
