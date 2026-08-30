package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sipeed/picoclaw/cmd/picoclaw/internal"
	codecmd "github.com/sipeed/picoclaw/cmd/picoclaw/internal/code"
	"github.com/sipeed/picoclaw/pkg/config"
)

type rootTestExitError struct {
	code    int
	handled bool
}

func (failure rootTestExitError) Error() string         { return "root test failure" }
func (failure rootTestExitError) ExitCode() int         { return failure.code }
func (failure rootTestExitError) CLIErrorHandled() bool { return failure.handled }

func TestNewPicoclawCommand(t *testing.T) {
	cmd := NewPicoclawCommand()

	require.NotNil(t, cmd)

	short := fmt.Sprintf("%s PicoClaw — personal AI assistant", internal.Logo)
	longHas := strings.Contains(cmd.Long, config.FormatVersion())

	assert.Equal(t, "picoclaw", cmd.Use)
	assert.Equal(t, short, cmd.Short)
	assert.True(t, longHas)

	assert.True(t, cmd.HasSubCommands())
	assert.True(t, cmd.HasAvailableSubCommands())

	assert.True(t, cmd.PersistentFlags().Lookup("no-color") != nil)

	assert.Nil(t, cmd.Run)
	assert.Nil(t, cmd.RunE)

	assert.NotNil(t, cmd.PersistentPreRun)
	assert.Nil(t, cmd.PersistentPostRun)

	allowedCommands := []string{
		"agent",
		"auth",
		"config",
		"code",
		"cron",
		"events",
		"gateway",
		"mcp",
		"migrate",
		"model",
		"onboard",
		"skills",
		"status",
		"update",
		"version",
		"workflow",
	}

	subcommands := cmd.Commands()
	assert.Len(t, subcommands, len(allowedCommands))

	for _, subcmd := range subcommands {
		found := slices.Contains(allowedCommands, subcmd.Name())
		assert.True(t, found, "unexpected subcommand %q", subcmd.Name())

		assert.False(t, subcmd.Hidden)
	}
}

func TestCommandFailureUsesTypedHandledExit(t *testing.T) {
	code, handled := commandFailure(rootTestExitError{code: 2, handled: true})
	assert.Equal(t, 2, code)
	assert.True(t, handled)

	code, handled = commandFailure(fmt.Errorf("ordinary"))
	assert.Equal(t, 1, code)
	assert.False(t, handled)

	code, handled = commandFailure(rootTestExitError{code: 0})
	assert.Equal(t, 1, code)
	assert.False(t, handled)

	code, handled = commandFailure(rootTestExitError{code: 256})
	assert.Equal(t, 1, code)
	assert.False(t, handled)
}

func TestRootColorInitializationAndHelpHooks(t *testing.T) {
	originalArgs := os.Args
	t.Cleanup(func() { os.Args = originalArgs })
	t.Setenv("NO_COLOR", "")
	t.Setenv("TERM", "xterm")

	os.Args = []string{"picoclaw", "version"}
	assert.False(t, earlyColorDisabled())

	t.Setenv("NO_COLOR", "1")
	assert.True(t, earlyColorDisabled())
	t.Setenv("NO_COLOR", "")
	t.Setenv("TERM", "dumb")
	assert.True(t, earlyColorDisabled())
	t.Setenv("TERM", "xterm")

	for _, argument := range []string{"--no-color", "--no-color=true", "--no-color=1"} {
		os.Args = []string{"picoclaw", "version", argument}
		assert.True(t, earlyColorDisabled(), argument)
	}

	cmd := NewPicoclawCommand()
	require.NoError(t, cmd.PersistentFlags().Set("no-color", "true"))
	cmd.PersistentPreRun(cmd, nil)
	var output bytes.Buffer
	cmd.SetOut(&output)
	require.NoError(t, cmd.Help())
	assert.Contains(t, output.String(), "PicoClaw")
}

func TestInitTermuxSSLHonorsExistingValueAndDiscoversPrefix(t *testing.T) {
	if runtime.GOOS != "linux" && runtime.GOOS != "android" {
		t.Skip("Termux detection only runs on Linux and Android")
	}

	t.Setenv("SSL_CERT_FILE", "/already/configured.pem")
	initTermuxSSL()
	assert.Equal(t, "/already/configured.pem", os.Getenv("SSL_CERT_FILE"))

	prefix := t.TempDir()
	certificate := filepath.Join(prefix, "etc", "tls", "cert.pem")
	require.NoError(t, os.MkdirAll(filepath.Dir(certificate), 0o700))
	require.NoError(t, os.WriteFile(certificate, []byte("test certificate"), 0o600))
	t.Setenv("SSL_CERT_FILE", "")
	t.Setenv("PREFIX", prefix)
	t.Setenv("HOME", filepath.Join(prefix, "com.termux", "home"))
	initTermuxSSL()
	assert.Equal(t, certificate, os.Getenv("SSL_CERT_FILE"))
}

func TestCodeJSONProcessOutputIsOnePureObject(t *testing.T) {
	command := exec.Command(os.Args[0], "-test.run=^TestPicoclawMainProcessHelper$")
	command.Env = append(
		os.Environ(),
		"PICOCLAW_MAIN_PROCESS_HELPER=1",
		"TZ=America/Santo_Domingo",
		"ZONEINFO=/private/zoneinfo",
	)
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	command.Stdout = &stdout
	command.Stderr = &stderr
	err := command.Run()
	var exitErr *exec.ExitError
	require.ErrorAs(t, err, &exitErr)
	assert.Equal(t, 1, exitErr.ExitCode())
	assert.Empty(t, stderr.String())
	assert.Equal(t, 1, strings.Count(stdout.String(), "\n"))
	assert.NotContains(t, stdout.String(), "PicoClaw")
	assert.NotContains(t, stdout.String(), "TZ environment")

	decoder := json.NewDecoder(bytes.NewReader(stdout.Bytes()))
	var result codecmd.Result
	require.NoError(t, decoder.Decode(&result))
	assert.Equal(t, codecmd.ResultSchemaVersion, result.Version)
	assert.Equal(t, "invalid_request", result.ErrorCode)
	var trailing any
	assert.ErrorIs(t, decoder.Decode(&trailing), io.EOF)
}

func TestPicoclawMainPlainProcessModes(t *testing.T) {
	for _, test := range []struct {
		name          string
		mode          string
		timezone      string
		wantExit      int
		wantStdout    []string
		wantStderr    bool
		wantColorCode bool
	}{
		{
			name: "plain banner and valid timezone", mode: "plain-valid-timezone", timezone: "UTC",
			wantStdout: []string{"TZ environment: UTC", "Time zone loaded successfully:"},
		},
		{
			name: "color banner and invalid timezone", mode: "plain-invalid-timezone",
			timezone:   "Invalid/Zone/For/PicoClaw/Test",
			wantStdout: []string{"Error loading time zone:"}, wantColorCode: true,
		},
		{
			name: "unhandled command error", mode: "plain-error", wantExit: 1,
			wantStderr: true, wantColorCode: true,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			command := exec.Command(os.Args[0], "-test.run=^TestPicoclawMainProcessHelper$")
			command.Env = append(
				os.Environ(),
				"PICOCLAW_MAIN_PROCESS_HELPER="+test.mode,
				"TZ="+test.timezone,
				"ZONEINFO=",
				"NO_COLOR=",
				"TERM=xterm",
				"SSL_CERT_FILE=/already/configured.pem",
			)
			var stdout bytes.Buffer
			var stderr bytes.Buffer
			command.Stdout = &stdout
			command.Stderr = &stderr
			err := command.Run()
			if test.wantExit == 0 {
				require.NoError(t, err)
			} else {
				var exitErr *exec.ExitError
				require.ErrorAs(t, err, &exitErr)
				assert.Equal(t, test.wantExit, exitErr.ExitCode())
			}
			for _, wanted := range test.wantStdout {
				assert.Contains(t, stdout.String(), wanted)
			}
			assert.Equal(t, test.wantStderr, stderr.Len() > 0)
			assert.Equal(t, test.wantColorCode, strings.Contains(stdout.String(), "\x1b["))
		})
	}
}

func TestPicoclawMainPlainSuccessBranchesInProcess(t *testing.T) {
	originalArgs := os.Args
	originalStdout := os.Stdout
	originalLocal := time.Local
	t.Cleanup(func() {
		os.Args = originalArgs
		os.Stdout = originalStdout
		time.Local = originalLocal
	})
	t.Setenv("NO_COLOR", "")
	t.Setenv("TERM", "xterm")
	t.Setenv("SSL_CERT_FILE", "/already/configured.pem")

	for _, test := range []struct {
		name     string
		args     []string
		timezone string
		want     string
	}{
		{
			name: "plain banner valid timezone", args: []string{"picoclaw", "--no-color", "version"},
			timezone: "UTC", want: "Time zone loaded successfully:",
		},
		{
			name: "color banner invalid timezone", args: []string{"picoclaw", "version"},
			timezone: "Invalid/Zone/For/PicoClaw/Test", want: "Error loading time zone:",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Setenv("TZ", test.timezone)
			os.Args = test.args
			reader, writer, err := os.Pipe()
			require.NoError(t, err)
			os.Stdout = writer
			main()
			require.NoError(t, writer.Close())
			os.Stdout = originalStdout
			output, err := io.ReadAll(reader)
			require.NoError(t, err)
			require.NoError(t, reader.Close())
			assert.Contains(t, string(output), test.want)
		})
	}
}

func TestPicoclawMainProcessHelper(t *testing.T) {
	switch os.Getenv("PICOCLAW_MAIN_PROCESS_HELPER") {
	case "1":
		os.Args = []string{"picoclaw", "code", "--json=TRUE", "--definitely-invalid"}
	case "plain-valid-timezone":
		os.Args = []string{"picoclaw", "--no-color", "version"}
	case "plain-invalid-timezone":
		os.Args = []string{"picoclaw", "version"}
	case "plain-error":
		os.Args = []string{"picoclaw", "--definitely-invalid"}
	default:
		return
	}
	main()
}
