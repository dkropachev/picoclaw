// Package database implements database broker lifecycle and offline
// maintenance commands. It never accepts a physical database path.
package database

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/sipeed/picoclaw/cmd/picoclaw/internal"
	"github.com/sipeed/picoclaw/internal/sqlbridge"
	"github.com/sipeed/picoclaw/internal/sqlitestore"
	"github.com/sipeed/picoclaw/pkg/accountrouter"
	authstore "github.com/sipeed/picoclaw/pkg/auth"
	"github.com/sipeed/picoclaw/pkg/channels/wecom"
	"github.com/sipeed/picoclaw/pkg/channels/weixin"
	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/cron"
	dblayer "github.com/sipeed/picoclaw/pkg/database"
	dbcatalog "github.com/sipeed/picoclaw/pkg/database/catalog"
	"github.com/sipeed/picoclaw/pkg/database/migration"
	"github.com/sipeed/picoclaw/pkg/eventing"
	"github.com/sipeed/picoclaw/pkg/evolution"
	"github.com/sipeed/picoclaw/pkg/gateway"
	"github.com/sipeed/picoclaw/pkg/gitworkspace"
	"github.com/sipeed/picoclaw/pkg/prworkspace/localci"
	"github.com/sipeed/picoclaw/pkg/repoaudit"
	"github.com/sipeed/picoclaw/pkg/repoeval"
	"github.com/sipeed/picoclaw/pkg/seahorse"
	"github.com/sipeed/picoclaw/pkg/state"
	"github.com/sipeed/picoclaw/pkg/threads"
	"github.com/sipeed/picoclaw/pkg/tools"
	"github.com/sipeed/picoclaw/pkg/workflows"
	backendapi "github.com/sipeed/picoclaw/web/backend/api"
	"github.com/sipeed/picoclaw/web/backend/dashboardauth"
	"github.com/sipeed/picoclaw/web/backend/launcherconfig"
)

var ensureSupervisor = dblayer.EnsureSupervisor

// NewDatabaseCommand returns the provider-neutral database maintenance tree.
func NewDatabaseCommand() *cobra.Command {
	command := &cobra.Command{
		Use:   "database",
		Short: "Inspect and maintain PicoClaw database stores",
		Args:  cobra.NoArgs,
	}
	command.AddCommand(
		newStatusCommand(),
		newMigrateCommand(),
		newShutdownCommand(),
		newServeCommand(),
	)
	return command
}

func newStatusCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Show broker and logical-store readiness",
		Args:  cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			client, err := ensureForCommand(command.Context())
			if err != nil {
				return err
			}
			status, err := client.Status(command.Context())
			if err != nil {
				if refreshErr := client.Refresh(); refreshErr == nil {
					status, err = client.Status(command.Context())
				}
			}
			if err != nil {
				return err
			}
			return writeJSON(command, status)
		},
	}
}

func newShutdownCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "shutdown",
		Short: "Shut down the canonical-home database supervisor",
		Args:  cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			client, err := ensureForCommand(command.Context())
			if err != nil {
				return err
			}
			epoch := client.Epoch()
			if err := client.Shutdown(command.Context()); err != nil &&
				dblayer.CodeOf(err) != dblayer.CodeOutcomeUnknown {
				return err
			}
			return writeJSON(command, map[string]any{
				"epoch": epoch, "status": "shutdown_requested",
			})
		},
	}
}

func newMigrateCommand() *cobra.Command {
	var storeNames []string
	var backupDir string
	var dryRun bool
	command := &cobra.Command{
		Use:   "migrate",
		Short: "Run exclusive backed-up offline database migrations",
		Args:  cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			home, err := dblayer.PrepareHome(internal.GetPicoclawHome())
			if err != nil {
				return err
			}
			if client, connectErr := dblayer.Connect(home); connectErr == nil {
				pingCtx, cancel := context.WithTimeout(command.Context(), time.Second)
				_, pingErr := client.Ping(pingCtx)
				cancel()
				if pingErr == nil {
					return fmt.Errorf("%w; run `picoclaw database shutdown` first", migration.ErrStorageActive)
				}
			}
			cfg, err := loadDatabaseConfig(internal.GetConfigPath())
			if err != nil {
				return err
			}
			workspace, err := trustedWorkspace(home, cfg.Agents.Defaults.Workspace)
			if err != nil {
				return err
			}
			cfg.Agents.Defaults.Workspace = workspace
			stores := make([]dbcatalog.StoreID, 0, len(storeNames))
			for _, name := range storeNames {
				id, parseErr := dblayer.ParseStoreID(name)
				if parseErr != nil {
					return fmt.Errorf("invalid --store %q: %w", name, parseErr)
				}
				stores = append(stores, id)
			}
			engine, err := migration.New(home, cfg)
			if err != nil {
				return err
			}
			result, err := engine.Run(command.Context(), migration.Options{
				Stores: stores, BackupDir: backupDir, DryRun: dryRun,
			})
			if err != nil {
				return err
			}
			return writeJSON(command, result)
		},
	}
	command.Flags().StringArrayVar(
		&storeNames,
		"store",
		nil,
		"Logical store ID to migrate (repeatable; defaults to all stores)",
	)
	command.Flags().StringVar(
		&backupDir,
		"backup-dir",
		"",
		"Parent directory for the mandatory timestamped backup",
	)
	command.Flags().BoolVar(
		&dryRun,
		"dry-run",
		false,
		"Create the mandatory backup and validate the migration plan without changing stores",
	)
	return command
}

func newServeCommand() *cobra.Command {
	var home string
	command := &cobra.Command{
		Use:    "__serve",
		Hidden: true,
		Args:   cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			if !dblayer.ConsumeSupervisorBootstrap(home) {
				return dblayer.NewError(
					dblayer.CodeUnauthorized,
					"direct database supervisor invocation is not allowed",
				)
			}
			canonicalHome, err := dblayer.PrepareHome(home)
			if err != nil {
				return err
			}
			cfg, catalogFingerprint, err := dblayer.LoadCatalogConfiguration(internal.GetConfigPath())
			if err != nil {
				return err
			}
			workspace, err := trustedWorkspace(canonicalHome, cfg.Agents.Defaults.Workspace)
			if err != nil {
				return err
			}
			cfg.Agents.Defaults.Workspace = workspace
			startupFence, err := dblayer.AcquireOnlineFence(canonicalHome)
			if err != nil {
				return err
			}
			defer startupFence.Close()
			physicalClaims, err := dblayer.AcquireCatalogStoreClaims(canonicalHome, cfg)
			if err != nil {
				return err
			}
			defer physicalClaims.Close()
			initialStatuses, err := dbcatalog.ProbeStatuses(command.Context(), canonicalHome, cfg)
			if err != nil {
				return err
			}
			defer dbcatalog.CloseProbePools(canonicalHome)
			serverContext, stop := signal.NotifyContext(
				command.Context(), os.Interrupt, syscall.SIGTERM,
			)
			defer stop()
			launcherPath := launcherconfig.PathForAppConfig(internal.GetConfigPath())
			authHandler := dashboardauth.NewBrokerHandler(canonicalHome, launcherPath)
			credentialHandler := authstore.NewBrokerHandler(canonicalHome)
			modelCatalogHandler := backendapi.NewModelCatalogBrokerHandler(canonicalHome)
			workflowHandler := newLazyDomainHandler(func() (dblayer.Handler, func() error, error) {
				handler, openErr := workflows.NewBrokerHandler(canonicalHome, cfg)
				if openErr != nil {
					return nil, nil, openErr
				}
				return handler, handler.Close, nil
			})
			cronHandler := newLazyDomainHandler(func() (dblayer.Handler, func() error, error) {
				handler, openErr := cron.NewBrokerHandler(canonicalHome, cfg)
				if openErr != nil {
					return nil, nil, openErr
				}
				return handler, handler.Close, nil
			})
			accountRoutingHandler := newLazyDomainHandler(func() (dblayer.Handler, func() error, error) {
				handler, openErr := accountrouter.NewBrokerHandler(canonicalHome, cfg)
				if openErr != nil {
					return nil, nil, openErr
				}
				return handler, handler.Close, nil
			})
			sessionHandler := newLazyDomainHandler(func() (dblayer.Handler, func() error, error) {
				handler, openErr := threads.NewBrokerHandler(canonicalHome, cfg)
				if openErr != nil {
					return nil, nil, openErr
				}
				return handler, handler.Close, nil
			})
			eventingHandler := newLazyDomainHandler(func() (dblayer.Handler, func() error, error) {
				handler, openErr := eventing.NewBrokerHandler(canonicalHome, cfg)
				if openErr != nil {
					return nil, nil, openErr
				}
				return handler, handler.Close, nil
			})
			evolutionHandler := newLazyDomainHandler(func() (dblayer.Handler, func() error, error) {
				handler, openErr := evolution.NewBrokerHandler(canonicalHome, cfg)
				if openErr != nil {
					return nil, nil, openErr
				}
				return handler, handler.Close, nil
			})
			reviewHandler, err := repoaudit.NewBrokerHandler(canonicalHome, cfg)
			if err != nil {
				return err
			}
			evaluationHandler, err := repoeval.NewBrokerHandler(canonicalHome, cfg)
			if err != nil {
				_ = reviewHandler.Close()
				return err
			}
			runtimeStateHandler := newLazyDomainHandler(func() (dblayer.Handler, func() error, error) {
				handler, openErr := state.NewBrokerHandler(canonicalHome, cfg)
				if openErr != nil {
					return nil, nil, openErr
				}
				return handler, handler.Close, nil
			})
			seahorseHandler := newLazyDomainHandler(func() (dblayer.Handler, func() error, error) {
				handler, openErr := seahorse.NewBrokerHandler(canonicalHome, cfg)
				if openErr != nil {
					return nil, nil, openErr
				}
				return handler, handler.Close, nil
			})
			localCIHandler := newLazyDomainHandler(func() (dblayer.Handler, func() error, error) {
				handler, openErr := localci.NewBrokerHandler(canonicalHome, cfg)
				if openErr != nil {
					return nil, nil, openErr
				}
				return handler, handler.Close, nil
			})
			wecomHandler := wecom.NewBrokerHandler(canonicalHome)
			weixinHandler := weixin.NewBrokerHandler(canonicalHome)
			adaptationHandler := tools.NewAdaptationBrokerHandler(canonicalHome)
			bridgeHandler, err := sqlbridge.NewBrokerHandler(canonicalHome, cfg)
			if err != nil {
				return err
			}
			gitInventoryHandler := newLazyDomainHandler(func() (dblayer.Handler, func() error, error) {
				handler, openErr := gitworkspace.NewBrokerHandler(canonicalHome, cfg)
				if openErr != nil {
					return nil, nil, openErr
				}
				return handler, handler.Close, nil
			})
			checkpointHandler := newLazyDomainHandler(func() (dblayer.Handler, func() error, error) {
				handler, openErr := gateway.NewPRWorkspaceCheckpointBrokerHandler(canonicalHome, cfg)
				if openErr != nil {
					return nil, nil, openErr
				}
				return handler, handler.Close, nil
			})
			var closeOnce sync.Once
			var handlersCloseErr error
			closeHandlers := func() error {
				closeOnce.Do(func() {
					handlersCloseErr = errors.Join(
						authHandler.Close(), credentialHandler.Close(), modelCatalogHandler.Close(),
						workflowHandler.Close(),
						cronHandler.Close(), accountRoutingHandler.Close(), runtimeStateHandler.Close(),
						sessionHandler.Close(), eventingHandler.Close(), evolutionHandler.Close(),
						reviewHandler.Close(), evaluationHandler.Close(), seahorseHandler.Close(),
						localCIHandler.Close(),
						wecomHandler.Close(), weixinHandler.Close(),
						adaptationHandler.Close(), bridgeHandler.Close(),
						gitInventoryHandler.Close(), checkpointHandler.Close(),
						dbcatalog.CloseProbePools(canonicalHome),
					)
				})
				return handlersCloseErr
			}
			compositeHandler := dblayer.HandlerFunc(func(
				ctx context.Context,
				request dblayer.Request,
			) (any, error) {
				switch request.Domain {
				case "launcher-auth":
					return authHandler.Handle(ctx, request)
				case "auth":
					return credentialHandler.Handle(ctx, request)
				case "model-catalogs":
					return modelCatalogHandler.Handle(ctx, request)
				case workflows.BrokerDomain:
					return workflowHandler.Handle(ctx, request)
				case cron.BrokerDomain:
					return cronHandler.Handle(ctx, request)
				case accountrouter.BrokerDomain:
					return accountRoutingHandler.Handle(ctx, request)
				case "sessions":
					return sessionHandler.Handle(ctx, request)
				case eventing.BrokerDomain:
					return eventingHandler.Handle(ctx, request)
				case evolution.BrokerDomain:
					return evolutionHandler.Handle(ctx, request)
				case "repository-reviews":
					return reviewHandler.Handle(ctx, request)
				case "repository-evaluations":
					return evaluationHandler.Handle(ctx, request)
				case "runtime-state":
					return runtimeStateHandler.Handle(ctx, request)
				case seahorse.BrokerDomain:
					return seahorseHandler.Handle(ctx, request)
				case localci.CacheBrokerDomain:
					return localCIHandler.Handle(ctx, request)
				case "channel-wecom":
					return wecomHandler.Handle(ctx, request)
				case "channel-weixin":
					return weixinHandler.Handle(ctx, request)
				case "tool-adaptation":
					return adaptationHandler.Handle(ctx, request)
				case sqlbridge.RPCDomain:
					return bridgeHandler.Handle(ctx, request)
				case gitworkspace.BrokerDomain:
					return gitInventoryHandler.Handle(ctx, request)
				case gateway.PRWorkspaceCheckpointBrokerDomain:
					return checkpointHandler.Handle(ctx, request)
				default:
					return nil, dblayer.NewError(
						dblayer.CodeUnsupported,
						"database domain is unsupported",
					)
				}
			})
			logicalCatalog, err := dbcatalog.New(canonicalHome, cfg)
			if err != nil {
				_ = closeHandlers()
				return err
			}
			initialStatuses, err = dbcatalog.InitializeRequired(
				command.Context(), logicalCatalog, initialStatuses,
				func(ctx context.Context, entry dbcatalog.Entry) error {
					switch entry.Domain {
					case "auth":
						return preflightBrokerTarget(ctx, credentialHandler, "auth", "load-page", entry.ID)
					case "launcher-auth":
						return preflightBrokerTarget(
							ctx, authHandler, "launcher-auth", "is-initialized", entry.ID,
						)
					case "model-catalogs":
						return preflightBrokerTarget(
							ctx, modelCatalogHandler, "model-catalogs", "preflight", entry.ID,
						)
					case "tool-adaptation":
						return preflightBrokerTarget(
							ctx, adaptationHandler, "tool-adaptation", "preflight", entry.ID,
						)
					case "workflows":
						return preflightBrokerTarget(
							ctx,
							workflowHandler,
							workflows.BrokerDomain,
							"preflight",
							entry.ID,
						)
					case "cron":
						return preflightBrokerTarget(
							ctx, cronHandler, cron.BrokerDomain, "preflight", entry.ID,
						)
					case "account-routing":
						return accountRoutingHandler.ensureOpen()
					case "sessions":
						return preflightBrokerTarget(
							ctx, sessionHandler, "sessions", threads.BrokerPreflightOperation, entry.ID,
						)
					case "eventing":
						return preflightBrokerTarget(
							ctx, eventingHandler, eventing.BrokerDomain,
							eventing.BrokerPreflightOperation, entry.ID,
						)
					case "evolution":
						return preflightBrokerTarget(
							ctx, evolutionHandler, evolution.BrokerDomain,
							evolution.BrokerPreflightOperation, entry.ID,
						)
					case "runtime-state":
						return preflightBrokerTarget(
							ctx, runtimeStateHandler, "runtime-state", "preflight", entry.ID,
						)
					case "repository-reviews":
						return preflightBrokerTarget(
							ctx, reviewHandler, "repository-reviews", "preflight", entry.ID,
						)
					case "repository-evaluations":
						return preflightBrokerTarget(
							ctx, evaluationHandler, "repository-evaluations", "preflight", entry.ID,
						)
					case "local-ci":
						return preflightBrokerTarget(
							ctx, localCIHandler, localci.CacheBrokerDomain, "preflight", entry.ID,
						)
					case "seahorse":
						return preflightBrokerTarget(
							ctx, seahorseHandler, seahorse.BrokerDomain, "preflight", entry.ID,
						)
					case "channel-wecom":
						return preflightBrokerTarget(
							ctx, wecomHandler, "channel-wecom", "preflight", entry.ID,
						)
					case "channel-weixin":
						return preflightBrokerTarget(
							ctx, weixinHandler, "channel-weixin", "preflight", entry.ID,
						)
					case "channel-matrix", "channel-whatsapp":
						return preflightSQLBridge(ctx, bridgeHandler, entry.ID)
					case gitworkspace.BrokerDomain:
						return preflightBrokerTarget(
							ctx, gitInventoryHandler, gitworkspace.BrokerDomain, "preflight", entry.ID,
						)
					case gateway.PRWorkspaceCheckpointBrokerDomain:
						return checkpointHandler.ensureOpen()
					default:
						return dblayer.NewError(
							dblayer.CodeUnsupported,
							"required database domain has no readiness initializer",
						)
					}
				},
			)
			if err != nil {
				_ = closeHandlers()
				return err
			}
			requiredStores := make([]dblayer.StoreID, 0)
			for _, entry := range logicalCatalog.Entries() {
				if entry.Required {
					requiredStores = append(requiredStores, entry.ID)
				}
			}
			server, err := dblayer.StartServer(serverContext, dblayer.ServerOptions{
				Home: canonicalHome, CatalogFingerprint: catalogFingerprint,
				RequiredStores: requiredStores,
				StatusProvider: func(context.Context) ([]dblayer.StoreStatus, error) {
					return append([]dblayer.StoreStatus(nil), initialStatuses...), nil
				},
				Handler:      compositeHandler,
				CloseHandler: closeHandlers,
			})
			if err != nil {
				_ = closeHandlers()
				return err
			}
			if closeErr := startupFence.Close(); closeErr != nil {
				_ = server.Close(context.Background())
				return closeErr
			}
			<-server.Done()
			return nil
		},
	}
	command.Flags().StringVar(&home, "home", "", "private canonical PicoClaw home")
	_ = command.MarkFlagRequired("home")
	return command
}

func ensureForCommand(ctx context.Context) (*dblayer.Client, error) {
	executable, err := os.Executable()
	if err != nil {
		return nil, err
	}
	return ensureSupervisor(ctx, dblayer.EnsureOptions{
		Home: internal.GetPicoclawHome(), Executable: executable,
		ConfigPath: internal.GetConfigPath(),
	})
}

func loadDatabaseConfig(path string) (*config.Config, error) {
	cfg, err := config.LoadConfig(path)
	if err == nil {
		return cfg, nil
	}
	if errors.Is(err, os.ErrNotExist) || strings.Contains(strings.ToLower(err.Error()), "no such file") {
		return &config.Config{}, nil
	}
	return nil, err
}

func trustedWorkspace(home, configured string) (string, error) {
	path := strings.TrimSpace(configured)
	if path == "" {
		path = filepath.Join(home, "workspace")
	} else if path == "~" || strings.HasPrefix(path, "~/") {
		userHome, err := os.UserHomeDir()
		if err != nil {
			return "", dblayer.NewError(dblayer.CodeInvalid, "database workspace is invalid")
		}
		if path == "~" {
			path = userHome
		} else {
			path = filepath.Join(userHome, path[2:])
		}
	} else if !filepath.IsAbs(path) {
		path = filepath.Join(home, path)
	}
	path, err := filepath.Abs(filepath.Clean(path))
	if err != nil || strings.ContainsRune(path, 0) {
		return "", dblayer.NewError(dblayer.CodeInvalid, "database workspace is invalid")
	}
	return path, nil
}

type lazyDomainHandler struct {
	once sync.Once
	mu   sync.RWMutex

	open    func() (dblayer.Handler, func() error, error)
	handler dblayer.Handler
	close   func() error
	err     error
	closed  bool
}

func preflightBrokerTarget(
	ctx context.Context,
	handler dblayer.Handler,
	domain,
	operation string,
	storeID dblayer.StoreID,
) error {
	payload, err := dblayer.MarshalCanonical(struct {
		StoreID dblayer.StoreID `json:"store_id"`
	}{StoreID: storeID})
	if err != nil {
		return dblayer.NewError(dblayer.CodeInternal, "database readiness payload failed")
	}
	_, err = handler.Handle(ctx, dblayer.Request{
		Domain: domain, Version: 1, Operation: operation, Payload: payload,
	})
	return err
}

func preflightSQLBridge(
	ctx context.Context,
	handler dblayer.Handler,
	storeID dblayer.StoreID,
) error {
	payload, err := dblayer.MarshalCanonical(sqlbridge.PingRequest{Target: sqlbridge.Target{
		StoreID: storeID,
		Mode:    sqlbridge.ModeRuntime,
	}})
	if err != nil {
		return dblayer.NewError(dblayer.CodeInternal, "database readiness payload failed")
	}
	_, err = handler.Handle(ctx, dblayer.Request{
		Domain: sqlbridge.RPCDomain, Version: sqlbridge.RPCVersion,
		Operation: sqlbridge.RPCOperationPing, Payload: payload,
	})
	return err
}

func newLazyDomainHandler(
	open func() (dblayer.Handler, func() error, error),
) *lazyDomainHandler {
	return &lazyDomainHandler{open: open}
}

func (handler *lazyDomainHandler) Handle(
	ctx context.Context,
	request dblayer.Request,
) (any, error) {
	if handler == nil {
		return nil, dblayer.NewError(dblayer.CodeUnavailable, "database domain is unavailable")
	}
	if err := handler.ensureOpen(); err != nil {
		return nil, err
	}
	handler.mu.RLock()
	defer handler.mu.RUnlock()
	if handler.closed || handler.handler == nil {
		return nil, dblayer.NewError(dblayer.CodeUnavailable, "database domain is unavailable")
	}
	return handler.handler.Handle(ctx, request)
}

func (handler *lazyDomainHandler) ensureOpen() error {
	if handler == nil {
		return dblayer.NewError(dblayer.CodeUnavailable, "database domain is unavailable")
	}
	handler.once.Do(func() {
		handler.mu.Lock()
		defer handler.mu.Unlock()
		if handler.closed || handler.open == nil {
			handler.err = dblayer.NewError(dblayer.CodeUnavailable, "database domain is unavailable")
			return
		}
		handler.handler, handler.close, handler.err = handler.open()
		handler.open = nil
	})
	handler.mu.RLock()
	defer handler.mu.RUnlock()
	if handler.closed {
		return dblayer.NewError(dblayer.CodeUnavailable, "database domain is unavailable")
	}
	if handler.err != nil {
		if errors.Is(handler.err, sqlitestore.ErrInvalidSchema) ||
			errors.Is(handler.err, sqlitestore.ErrIntegrity) {
			return dblayer.NewError(dblayer.CodeIntegrity, "database domain integrity validation failed")
		}
		if errors.Is(handler.err, sqlitestore.ErrTooNew) {
			return dblayer.NewError(dblayer.CodeUnsupported, "database domain schema is newer than supported")
		}
		if code := dblayer.CodeOf(handler.err); code != dblayer.CodeInternal {
			return dblayer.NewError(code, "database domain initialization failed")
		}
		return dblayer.NewError(dblayer.CodeInternal, "database domain initialization failed")
	}
	if handler.handler == nil {
		return dblayer.NewError(dblayer.CodeUnavailable, "database domain is unavailable")
	}
	return nil
}

func (handler *lazyDomainHandler) Close() error {
	if handler == nil {
		return nil
	}
	handler.mu.Lock()
	defer handler.mu.Unlock()
	if handler.closed {
		return nil
	}
	handler.closed = true
	if handler.close == nil {
		return nil
	}
	return handler.close()
}

func writeJSON(command *cobra.Command, value any) error {
	encoder := json.NewEncoder(command.OutOrStdout())
	encoder.SetEscapeHTML(false)
	return encoder.Encode(value)
}
