package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/sipeed/picoclaw/pkg/prworkspace/lifecycleflow"
)

func TestCheckedInArtifactsMatchEmbeddedManifest(t *testing.T) {
	graph, revision := lifecycleflow.Default()
	formats, err := defaultGateFormats(graph)
	if err != nil {
		t.Fatal(err)
	}
	wantSVG, err := lifecycleflow.RenderSVG(graph, revision, formats)
	if err != nil {
		t.Fatal(err)
	}
	wantFixture, err := json.MarshalIndent(struct {
		Flow         lifecycleflow.Graph `json:"flow"`
		FlowRevision string              `json:"flow_revision"`
	}{Flow: graph, FlowRevision: revision}, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	wantFixture = append(wantFixture, '\n')

	repositoryRoot := repositoryRootForTest(t)
	assertGeneratedArtifact(t,
		filepath.Join(repositoryRoot, "docs", "architecture", "pr-lifecycle-gates.svg"),
		wantSVG,
	)
	assertGeneratedArtifact(t,
		filepath.Join(repositoryRoot, "web", "frontend", "tests", "fixtures", "pr-lifecycle-flow.json"),
		wantFixture,
	)
}

func repositoryRootForTest(t *testing.T) string {
	t.Helper()
	_, filename, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve generated-artifact test path")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(filename), "../../../../.."))
}

func assertGeneratedArtifact(t *testing.T, path string, want []byte) {
	t.Helper()
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("%s is stale; run go generate ./pkg/prworkspace/lifecycleflow", path)
	}
}
