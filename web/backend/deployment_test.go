package main

import (
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

type launcherComposeDocument struct {
	Name     string                            `yaml:"name"`
	Services map[string]launcherComposeService `yaml:"services"`
}

type launcherComposeService struct {
	Profiles    []string          `yaml:"profiles"`
	Environment map[string]string `yaml:"environment"`
	Ports       []string          `yaml:"ports"`
	Volumes     []string          `yaml:"volumes"`
}

func launcherRepositoryRoot(t *testing.T) string {
	t.Helper()
	_, sourceFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve test source path")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(sourceFile), "..", ".."))
}

func readLauncherComposeDocument(t *testing.T, relative string) launcherComposeDocument {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(launcherRepositoryRoot(t), relative))
	if err != nil {
		t.Fatalf("ReadFile(%q) error = %v", relative, err)
	}
	var document launcherComposeDocument
	if err = yaml.Unmarshal(raw, &document); err != nil {
		t.Fatalf("Unmarshal(%q) error = %v", relative, err)
	}
	return document
}

func composeServiceNames(document launcherComposeDocument) []string {
	names := make([]string, 0, len(document.Services))
	for name := range document.Services {
		names = append(names, name)
	}
	slices.Sort(names)
	return names
}

func TestDefaultComposeIsSingleNodeLauncherOnly(t *testing.T) {
	document := readLauncherComposeDocument(t, "docker/docker-compose.yml")
	if document.Name != "picoclaw" {
		t.Fatalf("default project name = %q, want picoclaw", document.Name)
	}
	if names := composeServiceNames(document); !slices.Equal(names, []string{"picoclaw-launcher"}) {
		t.Fatalf("default services = %v, want launcher only", names)
	}
	launcher := document.Services["picoclaw-launcher"]
	if len(launcher.Profiles) != 0 {
		t.Fatalf("launcher profiles = %v, want default service", launcher.Profiles)
	}
	if got := launcher.Environment["PICOCLAW_GATEWAY_HOST"]; got != "127.0.0.1" {
		t.Fatalf("gateway host = %q, want container loopback", got)
	}
	if !slices.Equal(launcher.Ports, []string{"${PICOCLAW_LAUNCHER_BIND:-127.0.0.1}:18800:18800"}) {
		t.Fatalf("launcher ports = %v, want host-loopback 18800 only", launcher.Ports)
	}
	if !slices.Equal(launcher.Volumes, []string{"./data:/root/.picoclaw"}) {
		t.Fatalf("launcher volumes = %v, want complete PicoClaw home", launcher.Volumes)
	}
}

func TestHeadlessComposeKeepsOptionalProcessesOutOfDefault(t *testing.T) {
	document := readLauncherComposeDocument(t, "docker/docker-compose.headless.yml")
	if document.Name != "picoclaw" {
		t.Fatalf("headless project name = %q, want picoclaw", document.Name)
	}
	want := []string{"picoclaw-agent", "picoclaw-gateway"}
	if names := composeServiceNames(document); !slices.Equal(names, want) {
		t.Fatalf("headless services = %v, want %v", names, want)
	}
	for _, name := range want {
		service := document.Services[name]
		if len(service.Profiles) == 0 {
			t.Fatalf("headless service %q has no opt-in profile", name)
		}
		if !slices.Equal(service.Volumes, []string{"./data:/root/.picoclaw"}) {
			t.Fatalf("headless service %q volumes = %v", name, service.Volumes)
		}
	}
}

func TestGatewayPublicComposeIsExplicitLauncherOverride(t *testing.T) {
	document := readLauncherComposeDocument(t, "docker/docker-compose.gateway-public.yml")
	if names := composeServiceNames(document); !slices.Equal(names, []string{"picoclaw-launcher"}) {
		t.Fatalf("public override services = %v, want launcher only", names)
	}
	launcher := document.Services["picoclaw-launcher"]
	if got := launcher.Environment["PICOCLAW_GATEWAY_HOST"]; got != "0.0.0.0" {
		t.Fatalf("public gateway host = %q, want wildcard", got)
	}
	if !slices.Equal(launcher.Ports, []string{"18790:18790"}) {
		t.Fatalf("public gateway ports = %v, want direct 18790 only", launcher.Ports)
	}
}

func TestLauncherDockerfilesProbeLauncherHealth(t *testing.T) {
	for _, relative := range []string{
		"docker/Dockerfile.launcher",
		"docker/Dockerfile.goreleaser.launcher",
	} {
		t.Run(filepath.Base(relative), func(t *testing.T) {
			raw, err := os.ReadFile(filepath.Join(launcherRepositoryRoot(t), relative))
			if err != nil {
				t.Fatalf("ReadFile(%q) error = %v", relative, err)
			}
			contents := string(raw)
			if !strings.Contains(contents, "HEALTHCHECK ") {
				t.Fatalf("%s has no HEALTHCHECK", relative)
			}
			if !strings.Contains(contents, "http://127.0.0.1:18800/health") {
				t.Fatalf("%s does not probe launcher liveness", relative)
			}
			if !strings.Contains(contents, "ENV PICOCLAW_GATEWAY_HOST=127.0.0.1") {
				t.Fatalf("%s does not keep the managed Gateway on loopback", relative)
			}
			if strings.Contains(contents, "http://localhost:18790/health") {
				t.Fatalf("%s still couples health to Gateway", relative)
			}
		})
	}
}
