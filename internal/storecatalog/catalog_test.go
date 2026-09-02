package storecatalog

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"

	"github.com/sipeed/picoclaw/pkg/config"
)

func TestCanonicalPathToleratesDisappearingRegularLeaf(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	path := filepath.Join(root, "live.db-shm")
	stop := make(chan struct{})
	var writer sync.WaitGroup
	writer.Add(1)
	go func() {
		defer writer.Done()
		for {
			select {
			case <-stop:
				return
			default:
				_ = os.WriteFile(path, []byte("sidecar"), 0o600)
				_ = os.Remove(path)
			}
		}
	}()
	defer func() {
		close(stop)
		writer.Wait()
	}()

	for range 1_000 {
		canonical, info, err := canonicalPath(path)
		if err != nil {
			t.Fatalf("canonicalize changing regular leaf: %v", err)
		}
		if canonical != path {
			t.Fatalf("canonical path = %q, want %q", canonical, path)
		}
		if info != nil && !info.Mode().IsRegular() {
			t.Fatalf("changing leaf mode = %v", info.Mode())
		}
	}
}

func TestValidateSpecsRejectsOverlappingAndCaseAliasBoundaries(t *testing.T) {
	root := t.TempDir()
	if err := validateSpecs([]Spec{
		{ID: "first", Path: filepath.Join(root, "store")},
		{ID: "second", Path: filepath.Join(root, "store", "nested.db")},
	}); err == nil || !strings.Contains(err.Error(), "overlapping") {
		t.Fatalf("overlapping store boundaries error = %v", err)
	}
	if runtime.GOOS == "windows" || runtime.GOOS == "darwin" {
		if err := validateSpecs([]Spec{
			{ID: "first", Path: filepath.Join(root, "State.db")},
			{ID: "second", Path: filepath.Join(root, "state.db")},
		}); err == nil {
			t.Fatal("case-alias store boundaries were accepted")
		}
	}
}

func TestBuildRejectsFreshMainToSidecarCollision(t *testing.T) {
	home := t.TempDir()
	workspace := filepath.Join(home, "workspace")
	cfg := &config.Config{
		Agents: config.AgentsConfig{Defaults: config.AgentDefaults{Workspace: workspace}},
		Events: config.EventsConfig{Ingress: config.EventIngressConfig{
			Enabled:      true,
			DatabasePath: filepath.Join(home, "auth.db-wal"),
		}},
	}
	if _, err := Build(home, cfg); err == nil || !strings.Contains(err.Error(), "aliases") {
		t.Fatalf("fresh main-to-sidecar collision error = %v", err)
	}
}

func TestBuildCataloguesDynamicRuntimeDomainsPerConfiguredWorkspace(t *testing.T) {
	home := t.TempDir()
	primary := filepath.Join(home, "workspace")
	agent := filepath.Join(home, "agents", "worker")
	primaryEvents := filepath.Join(home, "event-data", "primary.db")
	cfg := &config.Config{
		Agents: config.AgentsConfig{
			Defaults: config.AgentDefaults{Workspace: primary},
			List: []config.AgentConfig{
				{ID: "worker", Workspace: agent},
				{ID: "shared", Workspace: agent},
			},
		},
		Workflows: config.WorkflowsConfig{Enabled: true},
		Events: config.EventsConfig{Ingress: config.EventIngressConfig{
			Enabled: true, DatabasePath: primaryEvents,
		}},
	}
	catalog, err := Build(home, cfg)
	if err != nil {
		t.Fatal(err)
	}
	agentPrefix := "workspace." + shortPathID(agent)
	wanted := map[string]struct {
		path     string
		required bool
	}{
		"global.git-workspace-inventory": {
			filepath.Join(primary, ".git-workspaces", "inventory.db"), true,
		},
		"global.pr-workspace-checkpoints": {
			filepath.Join(
				primary, ".git-workspaces", ".pr-workspace-implementation", "active", "checkpoints.db",
			), true,
		},
		"workspace.workflows":      {filepath.Join(primary, "state", "workflows.db"), true},
		"workspace.eventing":       {primaryEvents, true},
		"workspace.cron":           {filepath.Join(primary, "cron", "jobs.db"), true},
		agentPrefix + ".workflows": {filepath.Join(agent, "state", "workflows.db"), true},
		agentPrefix + ".eventing":  {filepath.Join(agent, "eventing", "events.db"), true},
		agentPrefix + ".cron":      {filepath.Join(agent, "cron", "jobs.db"), true},
	}
	for id, expected := range wanted {
		spec, ok := catalog.Lookup(id)
		if !ok {
			t.Errorf("catalog is missing %q", id)
			continue
		}
		if spec.Path != expected.path || spec.Required != expected.required {
			t.Errorf("%s = path %q required %t", id, spec.Path, spec.Required)
		}
	}
	for _, domain := range []string{"workflows", "eventing", "cron"} {
		count := 0
		for _, spec := range catalog.Specs {
			if spec.Domain == domain {
				count++
			}
		}
		if count != 2 {
			t.Errorf("%s store count = %d, want 2 distinct workspaces", domain, count)
		}
	}
}

func TestValidateSpecsRejectsFreshGenerationMemberAliases(t *testing.T) {
	root := t.TempDir()
	tests := []struct {
		name   string
		first  string
		second string
	}{
		{
			name:   "main aliases wal",
			first:  filepath.Join(root, "first.db"),
			second: filepath.Join(root, "first.db-wal"),
		},
		{
			name:   "main aliases shm",
			first:  filepath.Join(root, "first.db"),
			second: filepath.Join(root, "first.db-shm"),
		},
		{
			name:   "main aliases rollback journal",
			first:  filepath.Join(root, "first.db"),
			second: filepath.Join(root, "first.db-journal"),
		},
		{
			name:   "sidecar ancestor",
			first:  filepath.Join(root, "first.db"),
			second: filepath.Join(root, "first.db-wal", "nested.db"),
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := validateSpecs([]Spec{
				{ID: "first", Path: test.first},
				{ID: "second", Path: test.second},
			})
			if err == nil {
				t.Fatal("generation alias was accepted")
			}
		})
	}
}

func TestValidateSpecsRejectsExistingGenerationMemberAlias(t *testing.T) {
	root := t.TempDir()
	first := filepath.Join(root, "first.db")
	shared := first + "-wal"
	if err := os.WriteFile(shared, []byte("generation"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := validateSpecs([]Spec{
		{ID: "first", Path: first},
		{ID: "second", Path: shared},
	}); err == nil {
		t.Fatal("existing main-to-sidecar alias was accepted")
	}
}

func TestValidateSpecsRejectsGenerationMemberHardlinks(t *testing.T) {
	root := t.TempDir()
	first := filepath.Join(root, "first.db")
	second := filepath.Join(root, "second.db")
	if err := os.WriteFile(first+"-wal", []byte("generation"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(first+"-wal", second); err != nil {
		t.Skipf("hardlinks unavailable: %v", err)
	}
	err := validateSpecs([]Spec{
		{ID: "first", Path: first},
		{ID: "second", Path: second},
	})
	if err == nil || !strings.Contains(err.Error(), "physical file") {
		t.Fatalf("generation hardlink error = %v", err)
	}
}

func TestValidateSpecsRejectsCaseFoldedGenerationMemberAliases(t *testing.T) {
	root := t.TempDir()
	err := validateSpecsWithPathKey([]Spec{
		{ID: "first", Path: filepath.Join(root, "State.db")},
		{ID: "second", Path: filepath.Join(root, "state.db-WAL")},
	}, func(path string) string {
		return strings.ToLower(filepath.Clean(path))
	})
	if err == nil {
		t.Fatal("case-folded main-to-sidecar alias was accepted")
	}
}

func TestValidateSpecsAcceptsDistinctGenerationBoundaries(t *testing.T) {
	root := t.TempDir()
	if err := validateSpecs([]Spec{
		{ID: "first", Path: filepath.Join(root, "first.db")},
		{ID: "second", Path: filepath.Join(root, "second.db")},
	}); err != nil {
		t.Fatalf("distinct generations rejected: %v", err)
	}
}
