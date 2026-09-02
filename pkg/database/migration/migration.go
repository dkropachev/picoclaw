// Package migration coordinates exclusive, backed-up, offline database
// maintenance over the trusted logical store catalog.
package migration

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/sipeed/picoclaw/internal/sqliteprovider"
	"github.com/sipeed/picoclaw/internal/storecatalog"
	"github.com/sipeed/picoclaw/pkg/accountrouter"
	"github.com/sipeed/picoclaw/pkg/auth"
	"github.com/sipeed/picoclaw/pkg/channels/wecom"
	"github.com/sipeed/picoclaw/pkg/channels/weixin"
	whatsapp "github.com/sipeed/picoclaw/pkg/channels/whatsapp_native"
	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/cron"
	"github.com/sipeed/picoclaw/pkg/database"
	"github.com/sipeed/picoclaw/pkg/eventing"
	"github.com/sipeed/picoclaw/pkg/evolution"
	"github.com/sipeed/picoclaw/pkg/gateway"
	"github.com/sipeed/picoclaw/pkg/gitworkspace"
	"github.com/sipeed/picoclaw/pkg/memory"
	"github.com/sipeed/picoclaw/pkg/prworkspace/localci"
	"github.com/sipeed/picoclaw/pkg/repoaudit"
	"github.com/sipeed/picoclaw/pkg/repoeval"
	"github.com/sipeed/picoclaw/pkg/seahorse"
	"github.com/sipeed/picoclaw/pkg/state"
	"github.com/sipeed/picoclaw/pkg/tools"
	"github.com/sipeed/picoclaw/pkg/workflows"
	backendapi "github.com/sipeed/picoclaw/web/backend/api"
	"github.com/sipeed/picoclaw/web/backend/dashboardauth"
)

var (
	// ErrStorageActive means the launcher, supervisor, runtime, or another
	// migrator still owns the canonical storage root.
	ErrStorageActive = errors.New("database storage is active")
	// ErrUnknownStore means a requested logical ID is absent from the trusted
	// catalog. Physical paths are never accepted as a fallback.
	ErrUnknownStore = errors.New("unknown database store ID")
	// ErrIntegrity means SQLite or foreign-key integrity validation failed.
	ErrIntegrity = errors.New("database integrity check failed")
	// ErrMigrationRequired means recovery succeeded but a broker-side domain
	// adapter must apply the store's schema/import plan. The generic provider
	// deliberately cannot invent or execute application schema changes.
	ErrMigrationRequired = errors.New("database domain migration adapter required")
	// ErrSchemaTooNew means the generation was written by a newer domain adapter.
	ErrSchemaTooNew = errors.New("database schema is newer than supported")
)

// Options controls one offline migration. BackupDir is a parent directory; a
// new database-migrate-<UTC> generation is always created below it. An empty
// BackupDir uses <canonical-home>/backups.
type Options struct {
	Stores    []database.StoreID
	BackupDir string
	DryRun    bool
}

// StoreResult is provider-neutral maintenance output for one logical store.
type StoreResult struct {
	ID              database.StoreID `json:"id"`
	Exists          bool             `json:"exists"`
	BeforeVersion   int              `json:"before_version"`
	AfterVersion    int              `json:"after_version"`
	Migrated        bool             `json:"migrated"`
	AdapterRequired bool             `json:"adapter_required,omitempty"`
}

// Result reports the mandatory durable backup and each selected store. Dry
// runs still snapshot selected generations but perform no database mutation.
type Result struct {
	DryRun    bool          `json:"dry_run"`
	BackupDir string        `json:"backup_dir,omitempty"`
	Stores    []StoreResult `json:"stores"`
}

// Engine is scoped to one canonical home. The trusted catalog is deliberately
// constructed only while Run owns the exclusive migration fence.
type Engine struct {
	home   string
	config *config.Config
	now    func() time.Time
}

// New canonicalizes migration inputs without enumerating or inspecting any
// physical store. Run performs catalog construction only after fencing home.
func New(home string, cfg *config.Config) (*Engine, error) {
	canonicalHome, err := database.CanonicalHome(home)
	if err != nil {
		return nil, err
	}
	if cfg == nil {
		cfg = &config.Config{}
	}
	return &Engine{home: canonicalHome, config: cfg, now: time.Now}, nil
}

// Run fences every cooperative database owner before inspecting or mutating a
// generation. The fence is nonblocking so an online process produces a prompt,
// deterministic maintenance error.
func (e *Engine) Run(ctx context.Context, options Options) (result Result, returnErr error) {
	result.DryRun = options.DryRun
	if e == nil || e.home == "" || e.config == nil {
		return result, errors.New("database migration engine is unavailable")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return result, err
	}
	fence, err := database.AcquireMigrationFence(e.home)
	if err != nil {
		if database.CodeOf(err) == database.CodeConflict {
			return result, ErrStorageActive
		}
		return result, err
	}
	defer func() {
		if releaseErr := fence.Close(); releaseErr != nil {
			returnErr = errors.Join(returnErr, fmt.Errorf("release database storage fence: %w", releaseErr))
		}
	}()

	physicalClaims, err := database.AcquireCatalogStoreClaims(e.home, e.config)
	if err != nil {
		if database.CodeOf(err) == database.CodeConflict {
			return result, ErrStorageActive
		}
		return result, err
	}
	defer func() {
		if releaseErr := physicalClaims.Close(); releaseErr != nil {
			returnErr = errors.Join(returnErr, fmt.Errorf("release physical database claims: %w", releaseErr))
		}
	}()
	physical, err := storecatalog.Build(e.home, e.config)
	if err != nil {
		return result, err
	}
	specs, ids, err := selectStores(physical, options.Stores)
	if err != nil {
		return result, err
	}

	result.Stores = make([]StoreResult, len(specs))
	for index := range specs {
		result.Stores[index] = StoreResult{ID: ids[index]}
		info, statErr := os.Lstat(specs[index].Path)
		switch {
		case statErr == nil:
			if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
				return result, fmt.Errorf("store %s generation is unsafe", specs[index].ID)
			}
			result.Stores[index].Exists = true
		case errors.Is(statErr, os.ErrNotExist):
			// Missing stores are initialized by ordinary broker startup, not by
			// the offline migrator.
		default:
			return result, fmt.Errorf("inspect store %s: %w", specs[index].ID, statErr)
		}
	}
	backup, err := e.snapshot(ctx, physical, specs, options.BackupDir)
	if backup != nil {
		result.BackupDir = backup.root
	}
	if err != nil {
		return result, err
	}
	if options.DryRun {
		if err := backup.finish("dry_run", nil); err != nil {
			return result, fmt.Errorf("finalize dry-run database backup: %w", err)
		}
		return result, nil
	}
	defer func() {
		outcome := "complete"
		if returnErr != nil {
			outcome = "failed"
		}
		if manifestErr := backup.finish(outcome, returnErr); manifestErr != nil {
			returnErr = errors.Join(returnErr, fmt.Errorf("finalize database migration backup: %w", manifestErr))
		}
		if returnErr != nil {
			returnErr = fmt.Errorf("%w; backup preserved at %s", returnErr, backup.root)
		}
	}()

	for index := range specs {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		if !result.Stores[index].Exists {
			legacyExists, legacyErr := migrationLegacyInputExists(specs[index].LegacyRoots)
			if legacyErr != nil {
				return result, fmt.Errorf("inspect store %s legacy input: %w", specs[index].ID, legacyErr)
			}
			if domainUsesUnversionedAdapter(specs[index].Domain) || legacyExists {
				if !domainHasMigrationAdapter(specs[index].Domain) {
					result.Stores[index].AdapterRequired = true
					return result, fmt.Errorf(
						"migrate store %s: %w",
						specs[index].ID,
						ErrMigrationRequired,
					)
				}
				maintenance, adapterErr := migrateUnversionedStore(ctx, specs[index])
				if adapterErr != nil {
					result.Stores[index].AdapterRequired = true
					return result, adapterErr
				}
				result.Stores[index].AfterVersion = maintenance.AfterVersion
				result.Stores[index].Migrated = true
			}
			continue
		}
		maintenance, migrateErr := sqliteprovider.MaintainOffline(
			ctx,
			specs[index].Path,
			5*time.Second,
		)
		result.Stores[index].BeforeVersion = maintenance.BeforeVersion
		result.Stores[index].AfterVersion = maintenance.AfterVersion
		if migrateErr != nil {
			if sqliteprovider.IsMaintenanceIntegrity(migrateErr) {
				migrateErr = fmt.Errorf("%w: %v", ErrIntegrity, migrateErr)
			}
			return result, fmt.Errorf("migrate store %s: %w", specs[index].ID, migrateErr)
		}
		if domainUsesUnversionedAdapter(specs[index].Domain) {
			updatedMaintenance, adapterErr := migrateUnversionedStore(ctx, specs[index])
			if adapterErr != nil {
				result.Stores[index].AdapterRequired = true
				return result, adapterErr
			}
			result.Stores[index].AfterVersion = updatedMaintenance.AfterVersion
			result.Stores[index].Migrated = true
			continue
		}
		if expected := expectedSchemaVersion(specs[index].Domain); expected > 0 {
			switch {
			case maintenance.BeforeVersion > expected:
				return result, fmt.Errorf(
					"migrate store %s: %w (database=%d supported=%d)",
					specs[index].ID,
					ErrSchemaTooNew,
					maintenance.BeforeVersion,
					expected,
				)
			case maintenance.BeforeVersion < expected || domainHasMigrationAdapter(specs[index].Domain):
				if adapterErr := applyDomainMigrationAdapter(ctx, specs[index]); adapterErr != nil {
					result.Stores[index].AdapterRequired = true
					return result, fmt.Errorf(
						"migrate store %s: %w (database=%d supported=%d): %v",
						specs[index].ID,
						ErrMigrationRequired,
						maintenance.BeforeVersion,
						expected,
						adapterErr,
					)
				}
				maintenance, migrateErr = sqliteprovider.MaintainOffline(
					ctx,
					specs[index].Path,
					5*time.Second,
				)
				if migrateErr != nil {
					return result, fmt.Errorf(
						"revalidate migrated store %s: %w",
						specs[index].ID,
						migrateErr,
					)
				}
				if maintenance.AfterVersion != expected {
					return result, fmt.Errorf(
						"revalidate migrated store %s: schema version %d, want %d",
						specs[index].ID,
						maintenance.AfterVersion,
						expected,
					)
				}
				result.Stores[index].AfterVersion = maintenance.AfterVersion
			}
		}
		result.Stores[index].Migrated = true
	}
	return result, nil
}

func migrateUnversionedStore(
	ctx context.Context,
	spec storecatalog.Spec,
) (sqliteprovider.MaintenanceResult, error) {
	if adapterErr := applyDomainMigrationAdapter(ctx, spec); adapterErr != nil {
		return sqliteprovider.MaintenanceResult{}, fmt.Errorf(
			"migrate store %s: %w: %v",
			spec.ID,
			ErrMigrationRequired,
			adapterErr,
		)
	}
	maintenance, err := sqliteprovider.MaintainOffline(ctx, spec.Path, 5*time.Second)
	if err != nil {
		return maintenance, fmt.Errorf("revalidate migrated store %s: %w", spec.ID, err)
	}
	return maintenance, nil
}

func applyDomainMigrationAdapter(ctx context.Context, spec storecatalog.Spec) error {
	switch spec.Domain {
	case "auth":
		return auth.RunOfflineDatabaseMigration(ctx, filepath.Dir(spec.Path))
	case "launcher-auth":
		home := filepath.Dir(spec.Path)
		launcherPath := filepath.Join(home, "launcher-config.json")
		if len(spec.LegacyRoots) > 0 {
			launcherPath = spec.LegacyRoots[0]
		}
		return dashboardauth.RunOfflineDatabaseMigration(home, launcherPath)
	case "model-catalogs":
		return backendapi.RunOfflineModelCatalogMigration(ctx, filepath.Dir(spec.Path))
	case "tool-adaptation":
		return tools.RunOfflineDatabaseMigration(ctx, filepath.Dir(spec.Path))
	case "workflows":
		workspace := filepath.Dir(filepath.Dir(spec.Path))
		return workflows.RunOfflineDatabaseMigration(ctx, workspace)
	case "sessions":
		return memory.RunOfflineDatabaseMigration(ctx, filepath.Dir(spec.Path))
	case "eventing":
		return eventing.RunOfflineDatabaseMigration(ctx, spec.Path)
	case "cron":
		service, err := cron.NewOfflineService(spec.Path, nil)
		if err != nil {
			return err
		}
		return service.Close()
	case "runtime-state":
		workspace := filepath.Dir(filepath.Dir(spec.Path))
		return state.RunOfflineDatabaseMigration(workspace)
	case "account-routing":
		return accountrouter.RunOfflineDatabaseMigration(filepath.Dir(filepath.Dir(spec.Path)))
	case "repository-reviews":
		return repoaudit.RunOfflineDatabaseMigration(
			ctx, filepath.Dir(filepath.Dir(spec.Path)),
		)
	case "repository-evaluations":
		return repoeval.RunOfflineDatabaseMigration(
			ctx, filepath.Dir(filepath.Dir(spec.Path)),
		)
	case "evolution":
		return evolution.RunOfflineDatabaseMigration(ctx, spec.Path)
	case "local-ci":
		return localci.RunOfflineDatabaseMigration(filepath.Dir(spec.Path))
	case "seahorse":
		return seahorse.RunOfflineDatabaseMigration(spec.Path)
	case "channel-wecom":
		return wecom.RunOfflineDatabaseMigration(filepath.Dir(filepath.Dir(filepath.Dir(spec.Path))))
	case "channel-weixin":
		return weixin.RunOfflineDatabaseMigration(filepath.Dir(filepath.Dir(filepath.Dir(spec.Path))))
	case "channel-matrix":
		return migrateMatrixDatabase(ctx, spec.Path)
	case "channel-whatsapp":
		return whatsapp.MigrateDatabase(ctx, spec.Path)
	case "git-workspace-inventory":
		return gitworkspace.RunOfflineDatabaseMigration(ctx, filepath.Dir(spec.Path))
	case "pr-workspace-checkpoints":
		return gateway.RunOfflinePRWorkspaceCheckpointMigration(ctx, filepath.Dir(spec.Path))
	default:
		return errors.New("domain migration adapter is unavailable")
	}
}

func domainUsesUnversionedAdapter(domain string) bool {
	return domain == "channel-matrix" || domain == "channel-whatsapp" || domain == "seahorse"
}

func domainHasMigrationAdapter(domain string) bool {
	switch domain {
	case "auth", "launcher-auth", "model-catalogs", "tool-adaptation", "workflows",
		"sessions", "eventing", "cron", "runtime-state", "account-routing",
		"repository-reviews", "repository-evaluations", "evolution", "local-ci", "seahorse", "channel-wecom",
		"channel-weixin", "channel-matrix", "channel-whatsapp",
		"git-workspace-inventory", "pr-workspace-checkpoints":
		return true
	default:
		return false
	}
}

func migrationLegacyInputExists(paths []string) (bool, error) {
	for _, path := range paths {
		info, err := os.Lstat(path)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return false, err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return false, errors.New("legacy migration input is unsafe")
		}
		return true, nil
	}
	return false, nil
}

// expectedSchemaVersion is intentionally provider-neutral catalog metadata.
// Zero identifies upstream/unversioned stores whose typed adapter owns its own
// schema contract (currently Matrix, WhatsApp, and Seahorse).
func expectedSchemaVersion(domain string) int {
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

func selectStores(
	physical *storecatalog.Catalog,
	requested []database.StoreID,
) ([]storecatalog.Spec, []database.StoreID, error) {
	if len(requested) == 0 {
		specs := physical.All()
		ids := make([]database.StoreID, len(specs))
		for index := range specs {
			id, err := database.ParseStoreID(specs[index].ID)
			if err != nil {
				return nil, nil, err
			}
			ids[index] = id
		}
		return specs, ids, nil
	}

	type selectedStore struct {
		spec storecatalog.Spec
		id   database.StoreID
	}
	selected := make([]selectedStore, 0, len(requested))
	seen := make(map[string]struct{}, len(requested))
	for _, requestedID := range requested {
		name := string(requestedID)
		if !requestedID.Valid() {
			return nil, nil, fmt.Errorf("%w: %q", ErrUnknownStore, name)
		}
		if _, duplicate := seen[name]; duplicate {
			return nil, nil, fmt.Errorf("duplicate database store ID %q", name)
		}
		seen[name] = struct{}{}
		spec, ok := physical.Lookup(name)
		if !ok {
			return nil, nil, fmt.Errorf("%w: %q", ErrUnknownStore, name)
		}
		selected = append(selected, selectedStore{spec: spec, id: requestedID})
	}
	sort.Slice(selected, func(i, j int) bool { return selected[i].spec.ID < selected[j].spec.ID })
	specs := make([]storecatalog.Spec, len(selected))
	ids := make([]database.StoreID, len(selected))
	for index := range selected {
		specs[index], ids[index] = selected[index].spec, selected[index].id
	}
	return specs, ids, nil
}

func validateBackupParent(value, canonicalHome string) (string, error) {
	if strings.TrimSpace(value) == "" {
		value = filepath.Join(canonicalHome, "backups")
	}
	if value != strings.TrimSpace(value) || strings.ContainsRune(value, 0) {
		return "", errors.New("database backup directory is invalid")
	}
	absolute, err := filepath.Abs(filepath.Clean(value))
	if err != nil {
		return "", err
	}
	return absolute, nil
}
