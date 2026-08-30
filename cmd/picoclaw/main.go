// PicoClaw - Ultra-lightweight personal AI agent
// Inspired by and based on nanobot: https://github.com/HKUDS/nanobot
// License: MIT
//
// Copyright (c) 2026 PicoClaw contributors

package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"runtime"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/sipeed/picoclaw/cmd/picoclaw/internal"
	"github.com/sipeed/picoclaw/cmd/picoclaw/internal/agent"
	"github.com/sipeed/picoclaw/cmd/picoclaw/internal/auth"
	"github.com/sipeed/picoclaw/cmd/picoclaw/internal/cliui"
	codecmd "github.com/sipeed/picoclaw/cmd/picoclaw/internal/code"
	configcmd "github.com/sipeed/picoclaw/cmd/picoclaw/internal/config"
	"github.com/sipeed/picoclaw/cmd/picoclaw/internal/cron"
	"github.com/sipeed/picoclaw/cmd/picoclaw/internal/events"
	"github.com/sipeed/picoclaw/cmd/picoclaw/internal/gateway"
	"github.com/sipeed/picoclaw/cmd/picoclaw/internal/mcp"
	"github.com/sipeed/picoclaw/cmd/picoclaw/internal/migrate"
	"github.com/sipeed/picoclaw/cmd/picoclaw/internal/model"
	"github.com/sipeed/picoclaw/cmd/picoclaw/internal/onboard"
	"github.com/sipeed/picoclaw/cmd/picoclaw/internal/skills"
	"github.com/sipeed/picoclaw/cmd/picoclaw/internal/status"
	"github.com/sipeed/picoclaw/cmd/picoclaw/internal/version"
	"github.com/sipeed/picoclaw/cmd/picoclaw/internal/workflow"
	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/logger"
	"github.com/sipeed/picoclaw/pkg/updater"
)

var rootNoColor bool

// initTermuxSSL detects Termux environment and sets SSL_CERT_FILE if not already set.
// This fixes X509 certificate errors when running PicoClaw inside Termux or termux-chroot.
// See: https://github.com/sipeed/picoclaw/issues/2944
func initTermuxSSL() {
	// Only applicable on Linux/Android
	if runtime.GOOS != "linux" && runtime.GOOS != "android" {
		return
	}

	// Skip if already set
	if os.Getenv("SSL_CERT_FILE") != "" {
		return
	}

	// Check for Termux prefix in PATH or HOME
	home := os.Getenv("HOME")
	path := os.Getenv("PATH")

	isTermux := strings.Contains(home, "com.termux") ||
		strings.Contains(path, "com.termux") ||
		strings.Contains(home, "/data/data/com.termux")

	if !isTermux {
		return
	}

	// Check common CA bundle locations in Termux
	caPaths := []string{
		"$PREFIX/etc/tls/cert.pem",
		os.Getenv("PREFIX") + "/etc/tls/cert.pem",
		"/data/data/com.termux/files/usr/etc/tls/cert.pem",
		"/usr/etc/tls/cert.pem",
	}

	for _, caPath := range caPaths {
		expanded := os.ExpandEnv(caPath)
		if _, err := os.Stat(expanded); err == nil {
			os.Setenv("SSL_CERT_FILE", expanded)
			return
		}
	}
}

func syncCliUIColor(root *cobra.Command) {
	no, _ := root.PersistentFlags().GetBool("no-color")
	cliui.Init(no || os.Getenv("NO_COLOR") != "" || os.Getenv("TERM") == "dumb")
}

// earlyColorDisabled matches lipgloss/banner behavior from env and argv before Cobra parses flags.
func earlyColorDisabled() bool {
	if os.Getenv("NO_COLOR") != "" || os.Getenv("TERM") == "dumb" {
		return true
	}
	for i := 1; i < len(os.Args); i++ {
		arg := os.Args[i]
		if arg == "--no-color" || arg == "--no-color=true" || arg == "--no-color=1" {
			return true
		}
	}
	return false
}

func NewPicoclawCommand() *cobra.Command {
	short := fmt.Sprintf("%s PicoClaw — personal AI assistant", internal.Logo)
	long := fmt.Sprintf(`%s PicoClaw is a lightweight personal AI assistant.

Version: %s`, internal.Logo, config.FormatVersion())

	cmd := &cobra.Command{
		Use:   "picoclaw",
		Short: short,
		Long:  long,
		Example: `picoclaw version
picoclaw onboard
picoclaw --no-color status`,
		SilenceErrors: true,
		// Avoid plain UsageString() on stderr/stdout when a command fails; cliui
		// renders matching panels on stderr instead.
		SilenceUsage: true,
		PersistentPreRun: func(c *cobra.Command, _ []string) {
			syncCliUIColor(c.Root())
		},
	}

	cmd.PersistentFlags().BoolVar(&rootNoColor, "no-color", false,
		"Disable colors (boxed layout unchanged)")

	cmd.SetHelpFunc(func(c *cobra.Command, _ []string) {
		syncCliUIColor(c.Root())
		fmt.Fprint(c.OutOrStdout(), cliui.RenderCommandHelp(c))
	})

	cmd.AddCommand(
		configcmd.NewConfigCommand(),
		onboard.NewOnboardCommand(),
		agent.NewAgentCommand(),
		auth.NewAuthCommand(),
		gateway.NewGatewayCommand(),
		status.NewStatusCommand(),
		cron.NewCronCommand(),
		codecmd.NewCodeCommand(),
		events.NewEventsCommand(),
		workflow.NewWorkflowCommand(),
		mcp.NewMCPCommand(),
		migrate.NewMigrateCommand(),
		skills.NewSkillsCommand(),
		model.NewModelCommand(),
		updater.NewUpdateCommand("picoclaw"),
		version.NewVersionCommand(),
	)

	return cmd
}

const (
	colorBlue = "\033[1;38;2;62;93;185m"
	colorRed  = "\033[1;38;2;213;70;70m"
	banner    = "\r\n" +
		colorBlue + "██████╗ ██╗ ██████╗ ██████╗ " + colorRed + " ██████╗██╗      █████╗ ██╗    ██╗\n" +
		colorBlue + "██╔══██╗██║██╔════╝██╔═══██╗" + colorRed + "██╔════╝██║     ██╔══██╗██║    ██║\n" +
		colorBlue + "██████╔╝██║██║     ██║   ██║" + colorRed + "██║     ██║     ███████║██║ █╗ ██║\n" +
		colorBlue + "██╔═══╝ ██║██║     ██║   ██║" + colorRed + "██║     ██║     ██╔══██║██║███╗██║\n" +
		colorBlue + "██║     ██║╚██████╗╚██████╔╝" + colorRed + "╚██████╗███████╗██║  ██║╚███╔███╔╝\n" +
		colorBlue + "╚═╝     ╚═╝ ╚═════╝ ╚═════╝ " + colorRed + " ╚═════╝╚══════╝╚═╝  ╚═╝ ╚══╝╚══╝\n " +
		"\033[0m\r\n"
	plainBanner = "\r\n" +
		"██████╗ ██╗ ██████╗ ██████╗  ██████╗██╗      █████╗ ██╗    ██╗\n" +
		"██╔══██╗██║██╔════╝██╔═══██╗██╔════╝██║     ██╔══██╗██║    ██║\n" +
		"██████╔╝██║██║     ██║   ██║██║     ██║     ███████║██║ █╗ ██║\n" +
		"██╔═══╝ ██║██║     ██║   ██║██║     ██║     ██╔══██║██║███╗██║\n" +
		"██║     ██║╚██████╗╚██████╔╝╚██████╗███████╗██║  ██║╚███╔███╔╝\n" +
		"╚═╝     ╚═╝ ╚═════╝ ╚═════╝  ╚═════╝╚══════╝╚═╝  ╚═╝ ╚══╝╚══╝\n " +
		"\r\n"
)

func main() {
	// Initialize Termux SSL certificate detection before anything else
	initTermuxSSL()

	machineJSON := codecmd.IsJSONInvocation(os.Args)
	cliui.Init(machineJSON || earlyColorDisabled())
	if machineJSON {
		logger.DisableConsole()
	}

	if !machineJSON {
		if earlyColorDisabled() {
			fmt.Print(plainBanner)
		} else {
			fmt.Printf("%s", banner)
		}

		tzEnv := os.Getenv("TZ")
		if tzEnv != "" {
			fmt.Println("TZ environment:", tzEnv)
			zoneinfoEnv := os.Getenv("ZONEINFO")
			fmt.Println("ZONEINFO environment:", zoneinfoEnv)
			loc, err := time.LoadLocation(tzEnv)
			if err != nil {
				fmt.Println("Error loading time zone:", err)
			} else {
				fmt.Println("Time zone loaded successfully:", loc)
				time.Local = loc //nolint:gosmopolitan // We intentionally set local timezone from TZ env
			}
		}
	}

	cmd := NewPicoclawCommand()
	if codecmd.IsCodeInvocation(os.Args) {
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
		defer stop()
		cmd.SetContext(ctx)
	}
	last, err := cmd.ExecuteC()
	if err != nil {
		exitCode, handled := commandFailure(err)
		if machineJSON && !handled {
			_ = json.NewEncoder(os.Stdout).Encode(codecmd.Result{
				Version: codecmd.ResultSchemaVersion, ErrorCode: "invalid_request",
				Usage: codecmd.ImplementationUsage{Scope: codecmd.ImplementationUsageScope},
			})
			handled = true
		}
		if !handled {
			syncCliUIColor(cmd)
			fmt.Fprint(os.Stderr, cliui.FormatCLIError(err.Error(), last))
		}
		os.Exit(exitCode)
	}
}

func commandFailure(err error) (int, bool) {
	exitCode := 1
	var coded interface{ ExitCode() int }
	if errors.As(err, &coded) {
		if candidate := coded.ExitCode(); candidate > 0 && candidate <= 255 {
			exitCode = candidate
		}
	}
	var rendered interface{ CLIErrorHandled() bool }
	handled := errors.As(err, &rendered) && rendered.CLIErrorHandled()
	return exitCode, handled
}
