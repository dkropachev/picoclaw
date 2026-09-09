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
		"shard: [slow, workspace, remaining]",
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

func TestGoCacheIsMainOwnedAndPRReadOnly(t *testing.T) {
	const cacheAction = "55cc8345863c7cc4c66a329aec7e433d2d1c52a9"
	const sharedPaths = "path: |\n            .cache/go-build\n            .cache/go-mod"
	const keyBase = "${{ runner.os }}-${{ runner.arch }}-go-v3-"
	const dependencyHash = "${{ hashFiles('go.mod', 'go.sum') }}"
	const consumerEpoch = "${{ needs.classify.outputs.go_cache_epoch }}"
	prWorkflow := readRepoFile(t, ".github/workflows/pr.yml")
	for _, snippet := range []string{
		"go_cache_epoch: ${{ steps.go-cache-key.outputs.epoch }}",
		`echo "epoch=$(date -u +%Y-%m-%d)" >> "$GITHUB_OUTPUT"`,
	} {
		if !strings.Contains(prWorkflow, snippet) {
			t.Errorf("PR workflow is missing Go cache generation setting %q", snippet)
		}
	}
	if got := strings.Count(prWorkflow, "uses: actions/cache/restore@"+cacheAction); got != 6 {
		t.Errorf("PR workflow shared Go cache restore count = %d, want 6", got)
	}
	if got := strings.Count(prWorkflow, sharedPaths); got != 6 {
		t.Errorf("PR workflow shared Go cache path count = %d, want 6", got)
	}
	for _, cache := range []struct {
		name string
		want int
	}{
		{name: "host", want: 5},
		{name: "cross", want: 1},
	} {
		consumerKey := "key: " + keyBase + cache.name + "-" + dependencyHash + "-" + consumerEpoch
		if got := strings.Count(prWorkflow, consumerKey+"\n          restore-keys: |"); got != cache.want {
			t.Errorf("PR workflow %s Go cache key count = %d, want %d", cache.name, got, cache.want)
		}
		dependencyPrefix := keyBase + cache.name + "-" + dependencyHash + "-"
		broadPrefix := keyBase + cache.name + "-"
		orderedFallback := dependencyPrefix + "\n            " + broadPrefix
		if got := strings.Count(prWorkflow, orderedFallback); got != cache.want {
			t.Errorf(
				"PR workflow dependency-first %s Go cache fallback count = %d, want %d",
				cache.name,
				got,
				cache.want,
			)
		}
	}
	if strings.Contains(prWorkflow, "uses: actions/cache/save@") {
		t.Error("PR workflow must not save the shared Go cache")
	}
	if strings.Contains(prWorkflow, "go-v2-") {
		t.Error("PR workflow still has a legacy v2 Go cache fallback")
	}
	hasV2JobKey := strings.Contains(prWorkflow, "go-v2-${{ github.job }}")
	hasV3JobKey := strings.Contains(prWorkflow, "go-v3-${{ github.job }}")
	if hasV2JobKey || hasV3JobKey {
		t.Error("PR workflow still uses job-specific Go cache keys")
	}
	if got, want := strings.Count(prWorkflow, "go-version-file: go.mod"), strings.Count(
		prWorkflow,
		"go-version-file: go.mod\n          cache: false",
	); got != want {
		t.Errorf("PR setup-go steps with cache disabled = %d, want %d", want, got)
	}

	buildWorkflow := readRepoFile(t, ".github/workflows/build.yml")
	cacheOwners := []struct {
		name  string
		block string
	}{
		{name: "cross", block: targetBlock(t, buildWorkflow, "  build:\n", "  launcher:\n")},
		{name: "host", block: targetBlock(t, buildWorkflow, "  launcher:\n", "  frontend_tests:\n")},
	}
	for _, owner := range cacheOwners {
		for _, snippet := range []string{
			"group: go-cache-" + owner.name + "-${{ github.ref }}\n      cancel-in-progress: false",
			`echo "epoch=$(date -u +%Y-%m-%d)" >> "$GITHUB_OUTPUT"`,
			"id: go-cache\n        uses: actions/cache/restore@" + cacheAction,
			"github.ref == format('refs/heads/{0}', github.event.repository.default_branch)",
			"key: ${{ steps.go-cache.outputs.cache-primary-key }}",
		} {
			if !strings.Contains(owner.block, snippet) {
				t.Errorf("main %s cache owner is missing setting %q", owner.name, snippet)
			}
		}
		if got := strings.Count(owner.block, "uses: actions/cache/save@"+cacheAction); got != 1 {
			t.Errorf("main %s cache save count = %d, want 1", owner.name, got)
		}
		if got := strings.Count(owner.block, "uses: actions/cache/restore@"+cacheAction); got != 1 {
			t.Errorf("main %s cache restore count = %d, want 1", owner.name, got)
		}
		if got := strings.Count(owner.block, sharedPaths); got != 2 {
			t.Errorf("main %s cache shared path count = %d, want 2", owner.name, got)
		}
		producerKey := "key: " + keyBase + owner.name + "-" + dependencyHash +
			"-${{ steps.go-cache-key.outputs.epoch }}"
		if !strings.Contains(owner.block, producerKey) {
			t.Errorf("main %s cache owner is missing daily key %q", owner.name, producerKey)
		}
	}

	workflowPaths, err := filepath.Glob(filepath.Join(repoRootForTest(t), ".github", "workflows", "*.yml"))
	if err != nil {
		t.Fatalf("list workflows: %v", err)
	}
	saveOwners := 0
	for _, path := range workflowPaths {
		data, readErr := os.ReadFile(path)
		if readErr != nil {
			t.Fatalf("read workflow %s: %v", path, readErr)
		}
		saveOwners += strings.Count(string(data), "uses: actions/cache/save@"+cacheAction)
	}
	if saveOwners != 2 {
		t.Errorf("repository shared cache save owners = %d, want 2", saveOwners)
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
