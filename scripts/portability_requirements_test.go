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
		"GOOS=darwin GOARCH=arm64",
		"GOOS=windows GOARCH=amd64",
	}
	for _, snippet := range requiredSnippets {
		if !strings.Contains(buildAll, snippet) {
			t.Fatalf("build-all target missing %q", snippet)
		}
	}
}

func TestRetiredTargetsAreAbsentFromBuildAndReleaseMatrices(t *testing.T) {
	for _, relPath := range []string{"Makefile", "web/Makefile"} {
		contents := readRepoFile(t, relPath)
		for _, retired := range []string{"mipsle", "netbsd"} {
			if strings.Contains(strings.ToLower(contents), retired) {
				t.Errorf("%s still contains retired target %q", relPath, retired)
			}
		}
	}

	releaseConfig := readRepoFile(t, ".goreleaser.yaml")
	for _, retired := range []string{"- netbsd", "- mipsle", "gomips:"} {
		if strings.Contains(releaseConfig, retired) {
			t.Errorf("GoReleaser config still contains retired target setting %q", retired)
		}
	}
}

func TestReleaseMatrixRetainsSupportedTargets(t *testing.T) {
	releaseConfig := readRepoFile(t, ".goreleaser.yaml")
	builds := []struct {
		id        string
		endMarker string
	}{
		{id: "picoclaw", endMarker: "\n  - id: picoclaw-launcher"},
		{id: "picoclaw-launcher", endMarker: "\ndockers_v2:"},
	}

	for _, build := range builds {
		buildConfig := targetBlock(t, releaseConfig, "  - id: "+build.id, build.endMarker)
		goos := targetBlock(t, buildConfig, "    goos:", "    goarch:")
		for _, supported := range []string{"linux", "windows", "darwin", "freebsd"} {
			if !strings.Contains(goos, "      - "+supported+"\n") {
				t.Errorf("GoReleaser build %q is missing supported OS %q", build.id, supported)
			}
		}

		goarch := targetBlock(t, buildConfig, "    goarch:", "    goarm:")
		for _, supported := range []string{"amd64", "arm64"} {
			if !strings.Contains(goarch, "      - "+supported+"\n") {
				t.Errorf("GoReleaser build %q is missing supported architecture %q", build.id, supported)
			}
		}

		if !strings.Contains(buildConfig, "      - goos: freebsd\n        goarch: arm\n") {
			t.Errorf("GoReleaser build %q does not exclude retired freebsd/arm", build.id)
		}
		if !strings.Contains(buildConfig, "      - goos: freebsd\n        goarch: riscv64\n") {
			t.Errorf("GoReleaser build %q does not exclude unsupported freebsd/riscv64", build.id)
		}
	}
}

func TestAndroidARM64BuildsRemainAvailable(t *testing.T) {
	for _, relPath := range []string{"Makefile", "web/Makefile"} {
		contents := readRepoFile(t, relPath)
		for _, snippet := range []string{"build-android-arm64", "GOOS=android GOARCH=arm64"} {
			if !strings.Contains(contents, snippet) {
				t.Errorf("%s is missing Android ARM64 build setting %q", relPath, snippet)
			}
		}
	}
}

func TestPRRunsBuildAllBeforeMerge(t *testing.T) {
	workflow := readRepoFile(t, ".github/workflows/pr.yml")
	if !strings.Contains(
		workflow,
		"- name: Cross-compile core binaries\n        run: make build-all",
	) {
		t.Fatal("PR workflow does not run the complete core cross-build matrix")
	}
}

func TestPRCancelsSupersededRuns(t *testing.T) {
	workflow := readRepoFile(t, ".github/workflows/pr.yml")
	for _, snippet := range []string{
		"concurrency:\n  group: ${{ github.workflow }}-${{ github.event.pull_request.number || github.run_id }}",
		"  cancel-in-progress: true",
	} {
		if !strings.Contains(workflow, snippet) {
			t.Errorf("PR workflow is missing superseded-run cancellation setting %q", snippet)
		}
	}
}

func TestPRScopesValidationBehindStableRequiredCheck(t *testing.T) {
	workflow := readRepoFile(t, ".github/workflows/pr.yml")
	for _, snippet := range []string{
		"classify:\n    name: Classify changes",
		"git diff --name-status -z --find-renames --find-copies",
		"if: ${{ needs.classify.outputs.frontend_ui != 'false' }}",
		"required:\n    name: PR Required\n    if: ${{ always() }}",
		`- classify
      - lint
      - frontend
      - frontend_ui
      - vuln_check
      - test
      - cross_compile
      - coverage
      - integration`,
	} {
		if !strings.Contains(workflow, snippet) {
			t.Errorf("PR workflow is missing scoped-validation setting %q", snippet)
		}
	}
}

func TestPRGoTestsBoundPackageParallelism(t *testing.T) {
	workflow := readRepoFile(t, ".github/workflows/pr.yml")
	testJob := targetBlock(t, workflow, "  test:\n", "  cross_compile:\n")
	for _, snippet := range []string{
		"name: Tests (${{ matrix.shard }})",
		"fail-fast: false",
		"shard: [slow, remaining]",
		"if: matrix.shard == 'remaining'",
		"uses: actions/cache/restore@55cc8345863c7cc4c66a329aec7e433d2d1c52a9",
		`go run ./scripts/hermetic-go-test -- bash ./scripts/run-go-test-shard.sh "${{ matrix.shard }}"`,
	} {
		if !strings.Contains(testJob, snippet) {
			t.Errorf("PR workflow is missing Go test sharding setting %q", snippet)
		}
	}
	if strings.Count(testJob, "run: go generate ./...") != 1 {
		t.Fatal("PR test matrix must run Go generation exactly once")
	}
	helper := readRepoFile(t, "scripts/run-go-test-shard.sh")
	if !strings.Contains(helper, `exec go test -p 4 -tags goolm,stdjson -timeout 20m "${selected[@]}"`) {
		t.Fatal("PR Go test shard helper does not bound package parallelism")
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
