package main

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestMakefileBuildAllCoversRequiredTargets(t *testing.T) {
	makefile := readRepoFile(t, "Makefile")
	buildAll := targetBlock(t, makefile, "## build-all:", "## install:")

	requiredSnippets := []string{
		"GOOS=linux GOARCH=amd64",
		"GOOS=linux GOARCH=arm GOARM=7",
		"GOOS=linux GOARCH=arm64",
		"GOOS=linux GOARCH=loong64",
		"GOOS=linux GOARCH=riscv64",
		"GOOS=linux GOARCH=mipsle",
		"GOOS=darwin GOARCH=arm64",
		"GOOS=windows GOARCH=amd64",
	}
	for _, snippet := range requiredSnippets {
		if !strings.Contains(buildAll, snippet) {
			t.Fatalf("build-all target missing %q", snippet)
		}
	}
}

func TestLauncherBuildIncludesFrontendAndBackendPackaging(t *testing.T) {
	rootMakefile := readRepoFile(t, "Makefile")
	rootLauncher := targetBlock(t, rootMakefile, "## build-launcher:", "build-launcher-frontend:")
	for _, snippet := range []string{
		"$(MAKE) -C web build",
		"picoclaw-launcher-$(PLATFORM)-$(ARCH)$(EXT)",
	} {
		if !strings.Contains(rootLauncher, snippet) {
			t.Fatalf("root build-launcher target missing %q", snippet)
		}
	}

	webMakefile := readRepoFile(t, "web/Makefile")
	webLauncher := targetBlock(t, webMakefile, "build: build-frontend", "# Build launcher for Android ARM64")
	for _, snippet := range []string{
		"build: build-frontend",
		"${WEB_GO} build",
		"-o \"$(OUTPUT)\" ./$(BACKEND_DIR)/",
	} {
		if !strings.Contains(webLauncher, snippet) {
			t.Fatalf("web launcher build target missing %q", snippet)
		}
	}
	if !strings.Contains(webMakefile, "pnpm build:backend") {
		t.Fatal("web Makefile does not build frontend assets into backend dist")
	}
}

func readRepoFile(t *testing.T, relPath string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(repoRootForTest(t), filepath.FromSlash(relPath)))
	if err != nil {
		t.Fatalf("read %s: %v", relPath, err)
	}
	return string(data)
}

func targetBlock(t *testing.T, text, startMarker, endMarker string) string {
	t.Helper()
	start := strings.Index(text, startMarker)
	if start < 0 {
		t.Fatalf("missing marker %q", startMarker)
	}
	end := strings.Index(text[start:], endMarker)
	if end < 0 {
		t.Fatalf("missing marker %q after %q", endMarker, startMarker)
	}
	return text[start : start+end]
}

func repoRootForTest(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("locate test file")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(file), ".."))
}
