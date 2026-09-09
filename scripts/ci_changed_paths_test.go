package main

import (
	"bytes"
	"os/exec"
	"reflect"
	"strings"
	"testing"
)

var ciOutputNames = []string{
	"lint",
	"frontend",
	"frontend_ui",
	"vuln_check",
	"test",
	"cross_compile",
	"coverage",
	"integration",
}

func TestCIChangedPathsClassification(t *testing.T) {
	tests := []struct {
		name    string
		fields  []string
		enabled []string
	}{
		{name: "empty diff"},
		{name: "ordinary docs", fields: []string{"M", "docs/README.md"}},
		{name: "feature spec", fields: []string{"M", "docs/features/storage.md"}, enabled: []string{"lint"}},
		{name: "deleted feature spec", fields: []string{"D", "docs/features/storage.md"}, enabled: []string{"lint", "frontend"}},
		{name: "repository review API contract", fields: []string{"M", "docs/reference/repository-reviews-api.md"}, enabled: []string{"test"}},
		{name: "frontend", fields: []string{"M", "web/frontend/src/app.tsx"}, enabled: []string{"lint", "frontend", "frontend_ui"}},
		{name: "Go", fields: []string{"M", "pkg/agent/agent.go"}, enabled: []string{"lint", "vuln_check", "test", "cross_compile", "coverage", "integration"}},
		{name: "integration Go", fields: []string{"M", "integration/fixtures/server/main.go"}, enabled: []string{"lint", "vuln_check", "test", "cross_compile", "coverage", "integration"}},
		{name: "launcher backend", fields: []string{"M", "web/backend/api/server.go"}, enabled: []string{"lint", "frontend", "vuln_check", "test", "cross_compile", "coverage", "integration"}},
		{name: "integration manifest", fields: []string{"M", "integration/suites/storage-json/suite.env"}, enabled: []string{"lint", "test", "integration"}},
		{name: "Docker", fields: []string{"M", "docker/Dockerfile"}, enabled: ciOutputNames},
		{name: "embedded workspace", fields: []string{"M", "workspace/AGENTS.md"}, enabled: []string{"test", "cross_compile"}},
		{name: "golangci", fields: []string{"M", ".golangci.yml"}, enabled: []string{"lint"}},
		{name: "goreleaser", fields: []string{"M", ".goreleaser.yaml"}, enabled: []string{"test", "cross_compile"}},
		{name: "unknown", fields: []string{"A", "src/lib.rs"}, enabled: ciOutputNames},
		{name: "workflow", fields: []string{"M", ".github/workflows/pr.yml"}, enabled: ciOutputNames},
		{name: "newline path", fields: []string{"M", "docs/odd\nname.md"}},
		{
			name:    "renamed feature spec",
			fields:  []string{"R100", "docs/features/storage.md", "docs/storage.md"},
			enabled: []string{"lint", "frontend"},
		},
		{
			name:    "renamed frontend source",
			fields:  []string{"R095", "web/frontend/src/old.ts", "docs/old.ts"},
			enabled: []string{"lint", "frontend", "frontend_ui"},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := runCIChangedPaths(t, encodeNULFields(test.fields...), false)
			want := expectedCIOutputs(test.enabled...)
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("outputs = %#v, want %#v", got, want)
			}
		})
	}
}

func TestCIChangedPathsForceAll(t *testing.T) {
	got := runCIChangedPaths(t, nil, true)
	want := expectedCIOutputs(ciOutputNames...)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("outputs = %#v, want %#v", got, want)
	}
}

func TestCIChangedPathsRejectsMalformedInput(t *testing.T) {
	tests := []struct {
		name  string
		input []byte
	}{
		{name: "missing path", input: encodeNULFields("M")},
		{name: "missing rename destination", input: encodeNULFields("R100", "old.go")},
		{name: "unknown status", input: encodeNULFields("Q", "file.go")},
		{name: "malformed rename score", input: encodeNULFields("R1junk", "old.go", "new.go")},
		{name: "out of range copy score", input: encodeNULFields("C999", "old.go", "new.go")},
		{name: "unterminated field", input: []byte("M\x00file.go")},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			cmd := exec.Command("bash", "./scripts/ci-changed-paths.sh")
			cmd.Dir = repoRootForTest(t)
			cmd.Stdin = bytes.NewReader(test.input)
			if output, err := cmd.CombinedOutput(); err == nil {
				t.Fatalf("command succeeded with output %q", output)
			}
		})
	}
}

func runCIChangedPaths(t *testing.T, input []byte, forceAll bool) map[string]string {
	t.Helper()
	args := []string{"./scripts/ci-changed-paths.sh"}
	if forceAll {
		args = append(args, "--force-all")
	}
	cmd := exec.Command("bash", args...)
	cmd.Dir = repoRootForTest(t)
	cmd.Stdin = bytes.NewReader(input)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("run classifier: %v\n%s", err, output)
	}

	result := make(map[string]string)
	for _, line := range strings.Split(strings.TrimSpace(string(output)), "\n") {
		name, value, ok := strings.Cut(line, "=")
		if !ok {
			t.Fatalf("malformed classifier output %q", line)
		}
		result[name] = value
	}
	return result
}

func encodeNULFields(fields ...string) []byte {
	var result []byte
	for _, field := range fields {
		result = append(result, field...)
		result = append(result, 0)
	}
	return result
}

func expectedCIOutputs(enabled ...string) map[string]string {
	result := make(map[string]string, len(ciOutputNames))
	for _, name := range ciOutputNames {
		result[name] = "false"
	}
	for _, name := range enabled {
		result[name] = "true"
	}
	return result
}
