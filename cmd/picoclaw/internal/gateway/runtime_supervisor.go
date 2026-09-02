package gateway

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/sipeed/picoclaw/cmd/picoclaw/internal"
	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/database"
	coregateway "github.com/sipeed/picoclaw/pkg/gateway"
)

const gatewayRuntimeChildEnvironment = "PICOCLAW_GATEWAY_RUNTIME_CHILD"

var preparedRuntimeAuthority struct {
	sync.Mutex
	client *database.Client
	home   string
}

// PrepareGatewayRuntimeInvocation consumes and validates inherited child
// authority before main may acquire an online fence or ensure a supervisor.
// A spoofed child marker therefore fails without creating broker state.
func PrepareGatewayRuntimeInvocation(ctx context.Context) (bool, error) {
	if os.Getenv(gatewayRuntimeChildEnvironment) == "" {
		return false, nil
	}
	client, inheritedHome, err := database.ConnectInherited(ctx)
	if err != nil {
		return true, fmt.Errorf("private gateway runtime rejected: %w", err)
	}
	home, err := database.CanonicalHome(internal.GetPicoclawHome())
	if err != nil || inheritedHome != home {
		return true, database.NewError(database.CodeUnauthorized, "gateway runtime home authority does not match")
	}
	preparedRuntimeAuthority.Lock()
	preparedRuntimeAuthority.client = client
	preparedRuntimeAuthority.home = home
	preparedRuntimeAuthority.Unlock()
	return true, nil
}

func takePreparedRuntimeAuthority() (*database.Client, string, bool) {
	preparedRuntimeAuthority.Lock()
	defer preparedRuntimeAuthority.Unlock()
	client, home := preparedRuntimeAuthority.client, preparedRuntimeAuthority.home
	preparedRuntimeAuthority.client = nil
	preparedRuntimeAuthority.home = ""
	return client, home, client != nil && home != ""
}

func runAuthenticatedGatewayRuntime(ctx context.Context, debug, allowEmpty bool) error {
	client, home, prepared := takePreparedRuntimeAuthority()
	if !prepared {
		var inheritedHome string
		var err error
		client, inheritedHome, err = database.ConnectInherited(ctx)
		if err != nil {
			return fmt.Errorf("private gateway runtime rejected: %w", err)
		}
		home, err = database.CanonicalHome(internal.GetPicoclawHome())
		if err != nil || inheritedHome != home {
			return database.NewError(database.CodeUnauthorized, "gateway runtime home authority does not match")
		}
	}
	if readinessErr := requireGatewayDatabaseReadiness(ctx, client); readinessErr != nil {
		return readinessErr
	}
	startGatewayParentWatcher()
	fence, err := database.AcquireOnlineFence(home)
	if err != nil {
		return err
	}
	defer fence.Close()
	stopMonitor := make(chan struct{})
	defer close(stopMonitor)
	go monitorRuntimeBroker(client, stopMonitor)
	return coregateway.Run(debug, home, internal.GetConfigPath(), allowEmpty)
}

func runSupervisedGateway(
	command *cobra.Command,
	debug,
	noTruncate,
	allowEmpty bool,
) error {
	home, err := database.PrepareHome(internal.GetPicoclawHome())
	if err != nil {
		return err
	}
	executable, err := os.Executable()
	if err != nil {
		return fmt.Errorf("resolve gateway runtime executable: %w", err)
	}
	client, err := database.EnsureSupervisor(command.Context(), database.EnsureOptions{
		Home: home, Executable: executable, ConfigPath: internal.GetConfigPath(),
	})
	if err != nil {
		return fmt.Errorf("start database supervisor: %w", err)
	}
	if readinessErr := requireGatewayDatabaseReadiness(command.Context(), client); readinessErr != nil {
		return readinessErr
	}
	authority, err := database.InheritedAuthorityEnvironment(home)
	if err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(command.Context(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	backoff := 100 * time.Millisecond
	for attempt := 0; ; attempt++ {
		startedAt := time.Now()
		runtimeEpoch := client.Epoch()
		err = runGatewayRuntimeChild(
			ctx, command, client, executable, authority, debug, noTruncate, allowEmpty,
		)
		if ctx.Err() != nil {
			return nil
		}
		if err == nil {
			return nil
		}
		pingCtx, cancel := context.WithTimeout(ctx, time.Second)
		_, pingErr := client.Ping(pingCtx)
		cancel()
		if pingErr != nil || client.Epoch() != runtimeEpoch {
			client, err = recoverGatewayDatabaseSupervisor(ctx, home, executable)
			if err != nil {
				return fmt.Errorf("recover database supervisor after runtime loss: %w", err)
			}
			authority, err = database.InheritedAuthorityEnvironment(home)
			if err != nil {
				return err
			}
			attempt = -1
			backoff = 100 * time.Millisecond
			continue
		}
		if time.Since(startedAt) >= 10*time.Second {
			attempt = 0
			backoff = 100 * time.Millisecond
		}
		if attempt >= 4 {
			return fmt.Errorf("gateway runtime repeatedly exited: %w", err)
		}
		timer := time.NewTimer(backoff)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil
		case <-timer.C:
		}
		if backoff < 2*time.Second {
			backoff *= 2
		}
	}
}

func recoverGatewayDatabaseSupervisor(
	ctx context.Context,
	home,
	executable string,
) (*database.Client, error) {
	backoff := 100 * time.Millisecond
	var lastErr error
	for attempt := 0; attempt < 6; attempt++ {
		client, err := database.EnsureSupervisor(ctx, database.EnsureOptions{
			Home: home, Executable: executable, ConfigPath: internal.GetConfigPath(),
		})
		if err == nil {
			if readinessErr := requireGatewayDatabaseReadiness(ctx, client); readinessErr == nil {
				return client, nil
			} else {
				lastErr = readinessErr
			}
		} else {
			lastErr = err
		}
		timer := time.NewTimer(backoff)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, ctx.Err()
		case <-timer.C:
		}
		if backoff < 2*time.Second {
			backoff *= 2
		}
	}
	return nil, lastErr
}

func requireGatewayDatabaseReadiness(
	ctx context.Context,
	client *database.Client,
) error {
	status, err := client.Status(ctx)
	if err != nil {
		return err
	}
	if err := database.RequireBrokerReady(status); err != nil {
		return fmt.Errorf("gateway database readiness failed: %w", err)
	}
	return nil
}

func runGatewayRuntimeChild(
	ctx context.Context,
	command *cobra.Command,
	client *database.Client,
	executable,
	authority string,
	debug,
	noTruncate,
	allowEmpty bool,
) error {
	arguments := []string{"gateway"}
	if debug {
		arguments = append(arguments, "--debug")
	}
	if noTruncate {
		arguments = append(arguments, "--no-truncate")
	}
	if allowEmpty {
		arguments = append(arguments, "--allow-empty")
	}
	child := exec.Command(executable, arguments...)
	child.Stdin = command.InOrStdin()
	child.Stdout = command.OutOrStdout()
	child.Stderr = command.ErrOrStderr()
	environment := os.Environ()
	environment = replaceEnvironment(environment, gatewayRuntimeChildEnvironment, "1")
	environment = replaceEnvironment(
		environment,
		config.EnvGatewaySupervisorPID,
		strconv.Itoa(os.Getpid()),
	)
	if separator := strings.IndexByte(authority, '='); separator > 0 {
		environment = replaceEnvironment(environment, authority[:separator], authority[separator+1:])
	}
	child.Env = environment
	configureGatewayRuntimeChild(child)
	if err := child.Start(); err != nil {
		return err
	}
	wait := make(chan error, 1)
	go func() { wait <- child.Wait() }()
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case err := <-wait:
			return err
		case <-ctx.Done():
			terminateGatewayRuntimeChild(child)
			select {
			case <-wait:
			case <-time.After(5 * time.Second):
				_ = child.Process.Kill()
				<-wait
			}
			return ctx.Err()
		case <-ticker.C:
			pingCtx, cancel := context.WithTimeout(ctx, 750*time.Millisecond)
			_, err := client.Ping(pingCtx)
			cancel()
			if err != nil {
				terminateGatewayRuntimeChild(child)
				select {
				case <-wait:
				case <-time.After(2 * time.Second):
					_ = child.Process.Kill()
					<-wait
				}
				return err
			}
		}
	}
}

func monitorRuntimeBroker(client *database.Client, stop <-chan struct{}) {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-stop:
			return
		case <-ticker.C:
		}
		ctx, cancel := context.WithTimeout(context.Background(), 750*time.Millisecond)
		_, err := client.Ping(ctx)
		cancel()
		if err != nil {
			terminateCurrentGatewayRuntime()
			return
		}
	}
}

func replaceEnvironment(environment []string, key, value string) []string {
	prefix := key + "="
	result := make([]string, 0, len(environment)+1)
	for _, entry := range environment {
		if !strings.HasPrefix(entry, prefix) {
			result = append(result, entry)
		}
	}
	return append(result, prefix+value)
}
