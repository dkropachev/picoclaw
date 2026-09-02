package cron

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"

	"github.com/sipeed/picoclaw/pkg/config"
	corecron "github.com/sipeed/picoclaw/pkg/cron"
	"github.com/sipeed/picoclaw/pkg/database"
)

func TestCronCommandHelpersAndSubcommandsAgainstTypedBroker(t *testing.T) {
	home := t.TempDir()
	workspace := filepath.Join(home, "workspace")
	cfg := config.DefaultConfig()
	cfg.Agents.Defaults.Workspace = workspace
	handler, err := corecron.NewBrokerHandler(home, cfg)
	if err != nil {
		t.Fatal(err)
	}
	server, err := database.StartServer(t.Context(), database.ServerOptions{
		Home: home, Handler: handler, CloseHandler: handler.Close,
	})
	if err != nil {
		t.Fatal(err)
	}
	client, err := database.Connect(home)
	if err != nil {
		t.Fatal(err)
	}
	previousClient := database.RuntimeClient()
	database.InstallProcessClient(client)
	t.Cleanup(func() {
		database.InstallProcessClient(previousClient)
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = server.Close(ctx)
	})
	locator := filepath.Join(workspace, "cron")

	empty := captureCronStdout(t, func() { cronListCmd(locator) })
	if !strings.Contains(empty, "No scheduled jobs") {
		t.Fatalf("empty list output = %q", empty)
	}
	service := corecron.NewForWorkspace(locator, nil)
	if service.Status()["jobs"] != 0 {
		t.Fatalf("initial service status = %#v", service.Status())
	}
	every := int64(time.Minute / time.Millisecond)
	everyJob, err := service.AddJob(
		"every-job", corecron.CronSchedule{Kind: "every", EveryMS: &every}, "message", "channel", "target",
	)
	if err != nil {
		t.Fatal(err)
	}
	cronJob, err := service.AddJob(
		"cron-job", corecron.CronSchedule{Kind: "cron", Expr: "0 9 * * *"}, "message", "", "",
	)
	if err != nil {
		t.Fatal(err)
	}
	at := time.Now().Add(time.Hour).UnixMilli()
	atJob, err := service.AddJob(
		"at-job", corecron.CronSchedule{Kind: "at", AtMS: &at}, "message", "", "",
	)
	if err != nil {
		t.Fatal(err)
	}
	if service.EnableJob(cronJob.ID, false) == nil {
		t.Fatal("failed to disable cron job")
	}
	listed := captureCronStdout(t, func() { cronListCmd(locator) })
	for _, text := range []string{"Scheduled Jobs", "every 60s", "0 9 * * *", "one-time", "enabled", "disabled"} {
		if !strings.Contains(listed, text) {
			t.Errorf("list output missing %q: %s", text, listed)
		}
	}
	enabled := captureCronStdout(t, func() { cronSetJobEnabled(locator, cronJob.ID, true) })
	if !strings.Contains(enabled, "cron-job") {
		t.Fatalf("enable output = %q", enabled)
	}
	missingEnable := captureCronStdout(t, func() { cronSetJobEnabled(locator, "missing", true) })
	if !strings.Contains(missingEnable, "not found") {
		t.Fatalf("missing enable output = %q", missingEnable)
	}
	removed := captureCronStdout(t, func() { cronRemoveCmd(locator, everyJob.ID) })
	if !strings.Contains(removed, "Removed") {
		t.Fatalf("remove output = %q", removed)
	}
	missingRemove := captureCronStdout(t, func() { cronRemoveCmd(locator, "missing") })
	if !strings.Contains(missingRemove, "not found") {
		t.Fatalf("missing remove output = %q", missingRemove)
	}
	if service.RemoveJob(atJob.ID) != true {
		t.Fatal("failed to remove at job")
	}

	invalidAdd := newAddCommand(func() string { return locator })
	invalidAdd.SetArgs([]string{"--name", "missing-schedule", "--message", "message"})
	if err := invalidAdd.Execute(); err == nil || !strings.Contains(err.Error(), "either --every or --cron") {
		t.Fatalf("invalid add error = %v", err)
	}
	everyAdd := newAddCommand(func() string { return locator })
	everyAdd.SetOut(io.Discard)
	everyAdd.SetErr(io.Discard)
	everyAdd.SetArgs([]string{"--name", "command-every", "--message", "message", "--every", "30"})
	if err := everyAdd.Execute(); err != nil {
		t.Fatalf("every add error = %v", err)
	}
	cronAdd := newAddCommand(func() string { return locator })
	cronAdd.SetOut(io.Discard)
	cronAdd.SetErr(io.Discard)
	cronAdd.SetArgs([]string{"--name", "command-cron", "--message", "message", "--cron", "0 1 * * *"})
	if err := cronAdd.Execute(); err != nil {
		t.Fatalf("cron add error = %v", err)
	}
	for _, command := range []struct {
		cmd  *cobra.Command
		args []string
	}{
		{newListCommand(func() string { return locator }), nil},
		{newRemoveCommand(func() string { return locator }), []string{"missing"}},
		{newEnableCommand(func() string { return locator }), []string{"missing"}},
		{newDisableCommand(func() string { return locator }), []string{"missing"}},
	} {
		command.cmd.SetArgs(command.args)
		if err := command.cmd.Execute(); err != nil {
			t.Errorf("subcommand error = %v", err)
		}
	}
}

func TestCronCommandUnavailableBrokerErrors(t *testing.T) {
	previousClient := database.RuntimeClient()
	database.InstallProcessClient(nil)
	t.Cleanup(func() { database.InstallProcessClient(previousClient) })
	restoreAuthority := database.SuspendProviderTestAuthority()
	t.Cleanup(restoreAuthority)
	command := newAddCommand(func() string { return filepath.Join(t.TempDir(), "cron") })
	command.SetArgs([]string{"--name", "offline", "--message", "message", "--every", "10"})
	if err := command.Execute(); err == nil || !strings.Contains(err.Error(), "error adding job") {
		t.Fatalf("offline add error = %v", err)
	}
}

func captureCronStdout(t *testing.T, run func()) string {
	t.Helper()
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	previous := os.Stdout
	os.Stdout = writer
	run()
	os.Stdout = previous
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	if _, err := io.Copy(&output, reader); err != nil {
		t.Fatal(err)
	}
	if err := reader.Close(); err != nil {
		t.Fatal(err)
	}
	return output.String()
}
