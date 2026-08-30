package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"slices"
	"strings"
	"testing"

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

func TestPicoclawMainProcessHelper(t *testing.T) {
	if os.Getenv("PICOCLAW_MAIN_PROCESS_HELPER") != "1" {
		return
	}
	os.Args = []string{"picoclaw", "code", "--json=TRUE", "--definitely-invalid"}
	main()
}
