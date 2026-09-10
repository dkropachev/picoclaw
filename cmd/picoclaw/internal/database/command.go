// Package database implements the private database broker process command.
// User-facing lifecycle and maintenance commands belong to later layers.
package database

import (
	"context"
	"errors"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/sipeed/picoclaw/cmd/picoclaw/internal"
	"github.com/sipeed/picoclaw/pkg/config"
	dblayer "github.com/sipeed/picoclaw/pkg/database"
	dbcatalog "github.com/sipeed/picoclaw/pkg/database/catalog"
)

const (
	privateArgumentsInvalidMessage = "database supervisor private arguments are invalid"
	launchGenerationChangedMessage = "database supervisor launch generation changed"
)

// NewDatabaseCommand returns the hidden database process-control tree. It is
// intentionally absent from normal help until public administration lands.
func NewDatabaseCommand() *cobra.Command {
	return newDatabaseCommand(defaultSupervisorServeOps())
}

type supervisorServeGuard interface {
	Validate(home string) error
}

type supervisorServeCatalog interface {
	Entries() []dbcatalog.Entry
	RequiredStores() []dblayer.StoreID
}

type supervisorServeRegistry interface {
	dblayer.Handler
	Close() error
}

type supervisorServeServer interface {
	Done() <-chan struct{}
	Close(ctx context.Context) error
}

type supervisorServeOps struct {
	home               func() string
	consumeGuard       func(string) (supervisorServeGuard, error)
	canonicalHome      func(string) (string, error)
	configPath         func() string
	loadConfigSnapshot func(string) (*config.Config, string, error)
	userHome           func() (string, error)
	newSnapshot        func(dbcatalog.Options, string) (supervisorServeCatalog, string, error)
	configRevision     func(string) (string, error)
	withConfigLock     func(string, func() error) error
	newRegistry        func() supervisorServeRegistry
	startServer        func(context.Context, dblayer.ServerOptions) (supervisorServeServer, error)
	notifyContext      func(context.Context, ...os.Signal) (context.Context, context.CancelFunc)
	now                func() time.Time
}

func defaultSupervisorServeOps() supervisorServeOps {
	return supervisorServeOps{
		home: internal.GetPicoclawHome,
		consumeGuard: func(home string) (supervisorServeGuard, error) {
			return dblayer.ConsumeSupervisorBootstrapGuard(home)
		},
		canonicalHome:      dblayer.CanonicalHome,
		configPath:         internal.GetConfigPath,
		loadConfigSnapshot: config.LoadCurrentConfigSnapshot,
		userHome:           os.UserHomeDir,
		newSnapshot: func(
			options dbcatalog.Options,
			revision string,
		) (supervisorServeCatalog, string, error) {
			return dbcatalog.NewSnapshot(options, revision)
		},
		configRevision: config.ConfigRevision,
		withConfigLock: config.WithConfigMutationLock,
		newRegistry: func() supervisorServeRegistry {
			return dblayer.NewHandlerRegistry()
		},
		startServer: func(
			ctx context.Context,
			options dblayer.ServerOptions,
		) (supervisorServeServer, error) {
			return dblayer.StartServer(ctx, options)
		},
		notifyContext: signal.NotifyContext,
		now:           time.Now,
	}
}

func newDatabaseCommand(ops supervisorServeOps) *cobra.Command {
	command := &cobra.Command{
		Use:    "database",
		Hidden: true,
		Args:   cobra.NoArgs,
	}
	command.AddCommand(newServeCommand(ops))
	return command
}

func newServeCommand(ops supervisorServeOps) *cobra.Command {
	return &cobra.Command{
		Use:                "__serve",
		Hidden:             true,
		DisableFlagParsing: true,
		RunE: func(command *cobra.Command, arguments []string) error {
			return runServeCommand(command.Context(), arguments, ops)
		},
	}
}

type supervisorServeArguments struct {
	home                       string
	expectedCatalogFingerprint string
	startupDeadline            time.Time
}

func runServeCommand(
	commandContext context.Context,
	arguments []string,
	ops supervisorServeOps,
) error {
	if !validSupervisorServeOps(ops) {
		return dblayer.NewError(dblayer.CodeInternal, "database supervisor command is unavailable")
	}
	// The launcher's private home environment is authenticated by the one-use
	// artifact. Do not parse any private argv field before that consumption.
	guard, err := ops.consumeGuard(ops.home())
	if err != nil {
		return err
	}
	if guard == nil {
		return dblayer.NewError(dblayer.CodeInternal, "database supervisor guard is unavailable")
	}
	parsed, err := parseSupervisorServeArguments(arguments)
	if err != nil {
		return err
	}
	canonicalHome, err := ops.canonicalHome(parsed.home)
	if err != nil {
		return supervisorServeFailure(err, "database supervisor home is unavailable")
	}
	if err = guard.Validate(canonicalHome); err != nil {
		return err
	}
	configPath := ops.configPath()
	if !validPrivateConfigPath(configPath) {
		return dblayer.NewError(dblayer.CodeInvalid, "database supervisor config path is invalid")
	}
	if err = requireSupervisorStartupWindow(
		commandContext,
		parsed.startupDeadline,
		ops.now,
	); err != nil {
		return err
	}
	serverContext, stop := ops.notifyContext(commandContext, os.Interrupt, syscall.SIGTERM)
	if serverContext == nil || stop == nil {
		return dblayer.NewError(dblayer.CodeInternal, "database supervisor signal context is unavailable")
	}
	defer stop()
	if err = requireSupervisorStartupWindow(
		serverContext,
		parsed.startupDeadline,
		ops.now,
	); err != nil {
		return err
	}
	cfg, configRevision, err := ops.loadConfigSnapshot(configPath)
	if err != nil {
		if errors.Is(err, config.ErrConfigMigrationRequired) {
			return dblayer.NewError(
				dblayer.CodeMigrationRequired,
				"database supervisor configuration requires migration",
			)
		}
		return dblayer.NewError(
			dblayer.CodeUnavailable,
			"database supervisor configuration is unavailable",
		)
	}
	userHome, err := ops.userHome()
	if err != nil {
		return dblayer.NewError(
			dblayer.CodeUnavailable,
			"database catalog user home is unavailable",
		)
	}
	logicalCatalog, catalogFingerprint, err := ops.newSnapshot(dbcatalog.Options{
		Home: canonicalHome, Config: cfg, ConfigPath: configPath, UserHome: userHome,
	}, configRevision)
	if err != nil {
		return err
	}
	if logicalCatalog == nil {
		return dblayer.NewError(dblayer.CodeInternal, "database supervisor catalog is unavailable")
	}

	generation := supervisorLaunchGeneration{
		guard: guard, home: canonicalHome, configPath: configPath,
		configRevision: configRevision, catalogFingerprint: catalogFingerprint,
		expectedCatalogFingerprint: parsed.expectedCatalogFingerprint,
		startupDeadline:            parsed.startupDeadline,
	}
	if err = generation.validate(serverContext, ops); err != nil {
		return err
	}

	entries := logicalCatalog.Entries()
	registry := ops.newRegistry()
	if registry == nil {
		return dblayer.NewError(dblayer.CodeInternal, "database supervisor registry is unavailable")
	}
	var server supervisorServeServer
	callbackRan := false
	var callbackErr error
	startErr := ops.withConfigLock(configPath, func() error {
		callbackRan = true
		if validationErr := generation.validate(serverContext, ops); validationErr != nil {
			callbackErr = validationErr
			return validationErr
		}
		server, callbackErr = ops.startServer(serverContext, dblayer.ServerOptions{
			Home:               canonicalHome,
			CatalogFingerprint: catalogFingerprint,
			RequiredStores:     logicalCatalog.RequiredStores(),
			StatusProvider: func(context.Context) ([]dblayer.StoreStatus, error) {
				return unavailableStatuses(entries), nil
			},
			Handler: registry,
			StartupGuard: func() error {
				return generation.validate(serverContext, ops)
			},
			CloseHandler: registry.Close,
		})
		return callbackErr
	})
	if startErr != nil && (!callbackRan || callbackErr == nil) {
		startErr = dblayer.NewError(
			dblayer.CodeUnavailable,
			"database supervisor configuration lock is unavailable",
		)
	} else if callbackErr != nil {
		startErr = supervisorServeFailure(
			callbackErr,
			"database supervisor server startup failed",
		)
	}
	if startErr == nil && server == nil {
		startErr = dblayer.NewError(dblayer.CodeInternal, "database supervisor server is unavailable")
	}
	if startErr != nil {
		return errors.Join(startErr, registry.Close())
	}
	<-server.Done()
	return server.Close(context.Background())
}

func validSupervisorServeOps(ops supervisorServeOps) bool {
	return ops.home != nil && ops.consumeGuard != nil && ops.canonicalHome != nil &&
		ops.configPath != nil && ops.loadConfigSnapshot != nil && ops.userHome != nil &&
		ops.newSnapshot != nil && ops.configRevision != nil && ops.withConfigLock != nil &&
		ops.newRegistry != nil && ops.startServer != nil && ops.notifyContext != nil &&
		ops.now != nil
}

func parseSupervisorServeArguments(arguments []string) (supervisorServeArguments, error) {
	if len(arguments) != 6 || arguments[0] != "--home" ||
		arguments[2] != "--expected-catalog-fingerprint" ||
		arguments[4] != "--startup-deadline-unix-ns" ||
		arguments[1] == "" || arguments[3] == "" || arguments[5] == "" {
		return supervisorServeArguments{}, privateArgumentsInvalid()
	}
	deadlineUnixNS, err := strconv.ParseInt(arguments[5], 10, 64)
	if err != nil || deadlineUnixNS <= 0 ||
		strconv.FormatInt(deadlineUnixNS, 10) != arguments[5] {
		return supervisorServeArguments{}, privateArgumentsInvalid()
	}
	return supervisorServeArguments{
		home:                       arguments[1],
		expectedCatalogFingerprint: arguments[3],
		startupDeadline:            time.Unix(0, deadlineUnixNS),
	}, nil
}

func privateArgumentsInvalid() error {
	return dblayer.NewError(dblayer.CodeInvalid, privateArgumentsInvalidMessage)
}

func validPrivateConfigPath(path string) bool {
	return path != "" && path == strings.TrimSpace(path) &&
		!strings.ContainsRune(path, 0) && filepath.IsAbs(path) && filepath.Clean(path) == path
}

func supervisorServeFailure(err error, message string) error {
	code := dblayer.CodeOf(err)
	if code == "" {
		code = dblayer.CodeInternal
	}
	return dblayer.NewError(code, message)
}

func requireSupervisorStartupWindow(
	ctx context.Context,
	deadline time.Time,
	now func() time.Time,
) error {
	if ctx == nil || ctx.Err() != nil {
		return dblayer.NewError(dblayer.CodeDeadline, "database supervisor startup was canceled")
	}
	if deadline.IsZero() || !deadline.After(now()) {
		return dblayer.NewError(dblayer.CodeDeadline, "database supervisor startup deadline expired")
	}
	return nil
}

type supervisorLaunchGeneration struct {
	guard                      supervisorServeGuard
	home                       string
	configPath                 string
	configRevision             string
	catalogFingerprint         string
	expectedCatalogFingerprint string
	startupDeadline            time.Time
}

func (generation supervisorLaunchGeneration) validate(
	ctx context.Context,
	ops supervisorServeOps,
) error {
	if err := requireSupervisorStartupWindow(ctx, generation.startupDeadline, ops.now); err != nil {
		return err
	}
	if err := generation.guard.Validate(generation.home); err != nil {
		return err
	}
	currentRevision, err := ops.configRevision(generation.configPath)
	if err != nil || currentRevision != generation.configRevision {
		return dblayer.NewError(dblayer.CodeConflict, launchGenerationChangedMessage)
	}
	if generation.catalogFingerprint == "" ||
		generation.catalogFingerprint != generation.expectedCatalogFingerprint {
		return dblayer.NewError(dblayer.CodeConflict, launchGenerationChangedMessage)
	}
	return nil
}

func unavailableStatuses(entries []dbcatalog.Entry) []dblayer.StoreStatus {
	statuses := make([]dblayer.StoreStatus, 0, len(entries))
	for _, entry := range entries {
		statuses = append(statuses, dblayer.StoreStatus{
			ID:        entry.ID,
			Readiness: dblayer.StoreUnavailable,
			Error: dblayer.NewError(
				dblayer.CodeUnavailable,
				"database domain handler is not installed",
			),
		})
	}
	return statuses
}
