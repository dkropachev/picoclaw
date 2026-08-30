package migrate

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type migrationCoverageOperation struct {
	sourceHome      string
	workspace       string
	configPath      string
	configErr       error
	workspaceErr    error
	configMigration error
}

func (operation *migrationCoverageOperation) GetSourceName() string { return "coverage" }
func (operation *migrationCoverageOperation) GetSourceHome() (string, error) {
	return operation.sourceHome, nil
}

func (operation *migrationCoverageOperation) GetSourceWorkspace() (string, error) {
	return operation.workspace, operation.workspaceErr
}

func (operation *migrationCoverageOperation) GetSourceConfigFile() (string, error) {
	return operation.configPath, operation.configErr
}

func (operation *migrationCoverageOperation) ExecuteConfigMigration(string, string) error {
	return operation.configMigration
}
func (*migrationCoverageOperation) GetMigrateableFiles() []string { return []string{"memory.md"} }
func (*migrationCoverageOperation) GetMigrateableDirs() []string  { return []string{"skills"} }

func TestMigrationGlobalShortfallPlanAndExecuteErrors(t *testing.T) {
	root := t.TempDir()
	sourceHome := filepath.Join(root, "source")
	targetHome := filepath.Join(root, "target")
	if err := os.MkdirAll(sourceHome, 0o755); err != nil {
		t.Fatal(err)
	}
	operation := &migrationCoverageOperation{
		sourceHome: sourceHome,
		workspace:  filepath.Join(sourceHome, "missing-workspace"),
		configErr:  errors.New("configuration unavailable"),
	}
	instance := &MigrateInstance{
		options: Options{Source: "coverage"}, handlers: map[string]Operation{"coverage": operation},
	}
	actions, warnings, err := instance.Plan(Options{}, sourceHome, targetHome)
	if err != nil || len(actions) != 0 || len(warnings) != 2 {
		t.Fatalf("plan actions=%#v warnings=%#v err=%v", actions, warnings, err)
	}
	if _, _, err := instance.Plan(Options{ConfigOnly: true}, sourceHome, targetHome); err == nil {
		t.Fatal("config-only plan hid its configuration error")
	}
	operation.configErr = nil
	operation.workspaceErr = errors.New("workspace unavailable")
	if _, _, err := instance.Plan(Options{}, sourceHome, targetHome); err == nil ||
		!strings.Contains(err.Error(), "getting source workspace") {
		t.Fatalf("workspace plan error=%v", err)
	}

	operation.workspaceErr = nil
	operation.configMigration = errors.New("conversion failed")
	blockingFile := filepath.Join(root, "blocking-file")
	if err := os.WriteFile(blockingFile, []byte("block"), 0o600); err != nil {
		t.Fatal(err)
	}
	backupTarget := filepath.Join(root, "backup-target")
	if err := os.WriteFile(backupTarget, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	result := instance.Execute([]Action{
		{Type: ActionConvertConfig, Source: "source-config", Target: "target-config"},
		{Type: ActionCreateDir, Target: filepath.Join(blockingFile, "child")},
		{Type: ActionBackup, Source: filepath.Join(root, "missing-source"), Target: backupTarget},
		{Type: ActionBackup, Source: "source", Target: filepath.Join(root, "missing-target")},
		{Type: ActionCopy, Source: "source", Target: filepath.Join(blockingFile, "copy")},
		{Type: ActionSkip},
	}, sourceHome, targetHome)
	if result.ConfigMigrated || result.FilesCopied != 0 || result.BackupsCreated != 1 ||
		result.FilesSkipped != 1 || len(result.Errors) < 5 {
		t.Fatalf("execute result=%#v", result)
	}
}

func TestMigrationGlobalShortfallRunDryAndForced(t *testing.T) {
	root := t.TempDir()
	sourceHome := filepath.Join(root, "source")
	workspace := filepath.Join(sourceHome, "workspace")
	targetHome := filepath.Join(root, "target")
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(sourceHome, "config.json")
	if err := os.WriteFile(configPath, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	operation := &migrationCoverageOperation{
		sourceHome: sourceHome, workspace: workspace, configPath: configPath,
	}
	instance := &MigrateInstance{
		options: Options{Source: "coverage"}, handlers: map[string]Operation{"coverage": operation},
	}
	dry, err := instance.Run(Options{DryRun: true, Force: true, TargetHome: targetHome})
	if err != nil || dry == nil {
		t.Fatalf("dry run=%#v err=%v", dry, err)
	}
	forced, err := instance.Run(Options{Force: true, ConfigOnly: true, TargetHome: targetHome})
	if err != nil || !forced.ConfigMigrated {
		t.Fatalf("forced run=%#v err=%v", forced, err)
	}
	instance.PrintSummary(&Result{
		FilesCopied: 1, ConfigMigrated: true, BackupsCreated: 1, FilesSkipped: 1,
		Errors: []error{errors.New("reported")},
	})
}
