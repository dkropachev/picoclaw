package localci

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"unicode/utf8"
)

const (
	maximumDiscoveryFiles     = 20_000
	maximumDiscoveryDepth     = 8
	maximumDefinitionFileSize = 1 << 20
	maximumDependencyFileSize = 8 << 20
	maximumDefinitionBytes    = 32 << 20
	maximumDependencyBytes    = 64 << 20
	maximumDefinitionFiles    = 256
	maximumDependencyFiles    = 512
	defaultStepTimeoutSeconds = 300
)

var makeTargetPattern = regexp.MustCompile(`(?m)^([A-Za-z0-9][A-Za-z0-9_.-]{0,127})[ \t]*:(?:[^=]|$)`)

type discoveryFragment struct {
	Path    string
	Payload []byte
}

type discoveryAccumulator struct {
	steps           []Step
	diagnostics     []Diagnostic
	definitions     []discoveryFragment
	dependencies    []discoveryFragment
	definitionBytes int64
	dependencyBytes int64
	err             error
}

type discoveredFile struct {
	path string
	name string
	dir  string
}

// DiscoverPair discovers both the immutable pre-attempt parent and candidate.
// A changed gate definition is deliberately not accepted as a runnable plan.
func DiscoverPair(ctx context.Context, baselineRoot, candidateRoot string) (ResolvedPlan, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	baseline, err := Discover(ctx, baselineRoot)
	if err != nil {
		return ResolvedPlan{}, err
	}
	candidate, err := Discover(ctx, candidateRoot)
	if err != nil {
		return ResolvedPlan{}, err
	}
	return resolveDiscoveredPlans(baseline, candidate)
}

func resolveDiscoveredPlans(baseline, candidate Plan) (ResolvedPlan, error) {
	var err error
	baseline, err = normalizePlan(baseline)
	if err != nil {
		return ResolvedPlan{}, err
	}
	candidate, err = normalizePlan(candidate)
	if err != nil {
		return ResolvedPlan{}, err
	}
	baselineSemantic, err := planSemanticDigest(baseline)
	if err != nil {
		return ResolvedPlan{}, err
	}
	candidateSemantic, err := planSemanticDigest(candidate)
	if err != nil {
		return ResolvedPlan{}, err
	}
	resolved := ResolvedPlan{
		Baseline:  baseline,
		Candidate: candidate,
		Effective: clonePlan(candidate),
		Changed: baseline.DefinitionDigest != candidate.DefinitionDigest ||
			baselineSemantic != candidateSemantic,
	}
	if resolved.Changed {
		resolved.Effective.Complete = false
		resolved.Effective.Diagnostics = append(
			resolved.Effective.Diagnostics,
			Diagnostic{Code: "plan_changed", Detail: "validation definitions changed from the pre-attempt parent"},
		)
		resolved.Effective, err = normalizePlan(resolved.Effective)
		if err != nil {
			return ResolvedPlan{}, err
		}
	}
	return resolved, nil
}

func clonePlan(plan Plan) Plan {
	plan.Steps = append([]Step(nil), plan.Steps...)
	for index := range plan.Steps {
		plan.Steps[index].Argv = append([]string(nil), plan.Steps[index].Argv...)
		plan.Steps[index].Environment = append(
			[]EnvironmentVariable(nil),
			plan.Steps[index].Environment...,
		)
	}
	plan.Diagnostics = append([]Diagnostic(nil), plan.Diagnostics...)
	return plan
}

// Discover creates one deterministic bounded plan without executing repository
// content or invoking a task runner.
func Discover(ctx context.Context, root string) (Plan, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	canonicalRoot, err := validateDiscoveryRoot(root)
	if err != nil {
		return Plan{}, err
	}
	files, err := enumerateDiscoveryFiles(ctx, canonicalRoot)
	if err != nil {
		return Plan{}, err
	}
	accumulator := discoveryAccumulator{}
	explicitFiles := make([]discoveredFile, 0, 1)
	for _, file := range files {
		if isExplicitPlan(file.path) {
			explicitFiles = append(explicitFiles, file)
		}
	}
	if len(explicitFiles) > 1 {
		return Plan{}, fmt.Errorf("%w: multiple explicit local CI plans", ErrInvalid)
	}
	if len(explicitFiles) == 1 {
		if err = accumulator.discoverExplicit(canonicalRoot, explicitFiles[0]); err != nil {
			return Plan{}, err
		}
		if accumulator.err != nil {
			return Plan{}, accumulator.err
		}
		for _, file := range files {
			if isDependencyInput(file.name) {
				if err = accumulator.addDependency(canonicalRoot, file.path); err != nil {
					return Plan{}, err
				}
			}
		}
		return finalizeDiscoveredPlan(accumulator)
	}
	for _, file := range files {
		if err = ctx.Err(); err != nil {
			return Plan{}, err
		}
		switch {
		case isMakefile(file.name):
			err = accumulator.discoverMakefile(canonicalRoot, file)
		case file.name == "package.json":
			err = accumulator.discoverPackageJSON(canonicalRoot, file, files)
		case file.name == "go.mod":
			err = accumulator.discoverGoModule(canonicalRoot, file)
		}
		if err != nil {
			return Plan{}, err
		}
		if accumulator.err != nil {
			return Plan{}, accumulator.err
		}
		if isDependencyInput(file.name) {
			if err = accumulator.addDependency(canonicalRoot, file.path); err != nil {
				return Plan{}, err
			}
		}
	}
	if len(accumulator.steps) == 0 {
		for _, file := range files {
			if !isGitHubWorkflow(file.path) {
				continue
			}
			if err = accumulator.discoverGitHubWorkflow(canonicalRoot, file); err != nil {
				return Plan{}, err
			}
			if accumulator.err != nil {
				return Plan{}, accumulator.err
			}
		}
	} else {
		for _, file := range files {
			if isGitHubWorkflow(file.path) {
				accumulator.addDiagnostic(Diagnostic{
					Code: "ignored_workflow", Source: file.path,
					Detail: "workflow omitted because a bounded repository-native quick profile was discovered",
				})
			}
		}
	}
	return finalizeDiscoveredPlan(accumulator)
}

func finalizeDiscoveredPlan(accumulator discoveryAccumulator) (Plan, error) {
	if accumulator.err != nil {
		return Plan{}, accumulator.err
	}
	if len(accumulator.steps) == 0 {
		accumulator.addDiagnostic(Diagnostic{
			Code:   "no_required_steps",
			Detail: "no supported local validation steps were discovered",
		})
	}
	if accumulator.err != nil {
		return Plan{}, accumulator.err
	}
	definitionDigest := digestFragments("picoclaw-local-ci-definitions-v1", accumulator.definitions)
	dependencyDigest := digestFragments("picoclaw-local-ci-dependencies-v1", accumulator.dependencies)
	return normalizePlan(Plan{
		DefinitionDigest: definitionDigest,
		DependencyDigest: dependencyDigest,
		Complete:         len(accumulator.steps) > 0 && !hasBlockingDiagnostic(accumulator.diagnostics),
		Steps:            accumulator.steps,
		Diagnostics:      accumulator.diagnostics,
	})
}

func validateDiscoveryRoot(root string) (string, error) {
	if strings.TrimSpace(root) != root || root == "" {
		return "", fmt.Errorf("%w: discovery root is required", ErrInvalid)
	}
	absolute, err := filepath.Abs(root)
	if err != nil {
		return "", fmt.Errorf("%w: resolve discovery root: %v", ErrInvalid, err)
	}
	info, err := os.Lstat(absolute)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return "", fmt.Errorf("%w: discovery root must be a real directory", ErrInvalid)
	}
	resolved, err := filepath.EvalSymlinks(absolute)
	if err != nil || resolved != absolute {
		return "", fmt.Errorf("%w: discovery root must be canonical", ErrInvalid)
	}
	return absolute, nil
}

func enumerateDiscoveryFiles(ctx context.Context, root string) ([]discoveredFile, error) {
	files := make([]discoveredFile, 0, 32)
	visited := 0
	err := filepath.WalkDir(root, func(current string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		visited++
		if visited > maximumDiscoveryFiles {
			return fmt.Errorf("%w: discovery file limit exceeded", ErrInvalid)
		}
		relative, err := filepath.Rel(root, current)
		if err != nil || !filepath.IsLocal(relative) {
			return fmt.Errorf("%w: discovery path escaped root", ErrInvalid)
		}
		relative = filepath.ToSlash(relative)
		if relative == "." {
			return nil
		}
		depth := strings.Count(relative, "/") + 1
		if entry.Type()&os.ModeSymlink != 0 {
			if discoveryRelevantPath(relative) {
				return fmt.Errorf("%w: discovery input cannot be a symlink", ErrInvalid)
			}
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if entry.IsDir() {
			if depth > maximumDiscoveryDepth || ignoredDiscoveryDirectory(entry.Name()) {
				return filepath.SkipDir
			}
			return nil
		}
		if !entry.Type().IsRegular() {
			if discoveryRelevantPath(relative) {
				return fmt.Errorf("%w: discovery input must be a regular file", ErrInvalid)
			}
			return nil
		}
		if discoveryRelevantPath(relative) || isDependencyInput(entry.Name()) {
			files = append(files, discoveredFile{
				path: relative,
				name: entry.Name(),
				dir:  filepath.ToSlash(filepath.Dir(relative)),
			})
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("discover local CI inputs: %w", err)
	}
	slices.SortFunc(files, func(left, right discoveredFile) int {
		return strings.Compare(left.path, right.path)
	})
	return files, nil
}

func ignoredDiscoveryDirectory(name string) bool {
	return name == ".git" || name == "node_modules" || name == "vendor" ||
		name == "dist" || name == "build" || name == ".cache"
}

func discoveryRelevantPath(value string) bool {
	base := filepath.Base(value)
	return isExplicitPlan(value) || isGitHubWorkflow(value) || isMakefile(base) ||
		base == "package.json" || base == "go.mod"
}

func isExplicitPlan(value string) bool {
	return value == ".picoclaw/ci.yml" || value == ".picoclaw/ci.yaml"
}

func isGitHubWorkflow(value string) bool {
	return strings.HasPrefix(value, ".github/workflows/") &&
		(strings.HasSuffix(value, ".yml") || strings.HasSuffix(value, ".yaml"))
}

func isMakefile(name string) bool {
	return name == "Makefile" || name == "makefile" || name == "GNUmakefile"
}

func isDependencyInput(name string) bool {
	switch name {
	case "go.mod", "go.sum", "go.work", "go.work.sum", "package.json", "package-lock.json",
		"pnpm-lock.yaml", "yarn.lock", "Cargo.toml", "Cargo.lock", "pyproject.toml",
		"poetry.lock", "uv.lock", "requirements.txt", "pom.xml", "build.gradle",
		"build.gradle.kts", "gradle.lockfile":
		return true
	default:
		return false
	}
}

func (accumulator *discoveryAccumulator) discoverMakefile(root string, file discoveredFile) error {
	raw, err := readDiscoveryFile(root, file.path, maximumDefinitionFileSize)
	if err != nil {
		return err
	}
	accumulator.addDefinition(file.path, raw)
	targets := make(map[string]struct{})
	for _, match := range makeTargetPattern.FindAllSubmatch(raw, -1) {
		targets[string(match[1])] = struct{}{}
	}
	selected := []struct {
		kind    StepKind
		targets []string
	}{
		{kind: StepLint, targets: []string{"lint", "lint-all"}},
		{kind: StepTest, targets: []string{"test-unit", "unit-test", "test"}},
		{kind: StepBuild, targets: []string{"build"}},
	}
	for _, group := range selected {
		for _, target := range group.targets {
			if _, exists := targets[target]; !exists {
				continue
			}
			accumulator.addStep(Step{
				ID:               stepID(OriginMake, file.path, target),
				Name:             "make " + target,
				Kind:             group.kind,
				Origin:           OriginMake,
				Source:           file.path,
				WorkingDirectory: normalizeWorkingDirectory(file.dir),
				Argv:             []string{"make", "-f", file.name, target},
				TimeoutSeconds:   defaultStepTimeoutSeconds,
				Required:         true,
			})
			break
		}
	}
	return nil
}

func (accumulator *discoveryAccumulator) discoverGoModule(root string, file discoveredFile) error {
	raw, err := readDiscoveryFile(root, file.path, maximumDependencyFileSize)
	if err != nil {
		return err
	}
	accumulator.addDefinition(file.path+"#presence", []byte("go-module"))
	if !utf8.Valid(raw) || !bytesContainGoModule(raw) {
		return fmt.Errorf("%w: malformed go.mod", ErrInvalid)
	}
	workingDirectory := normalizeWorkingDirectory(file.dir)
	for _, item := range []struct {
		id   string
		name string
		kind StepKind
		argv []string
	}{
		{id: "vet", name: "Go vet", kind: StepLint, argv: []string{"go", "vet", "./..."}},
		{id: "test", name: "Go tests", kind: StepTest, argv: []string{"go", "test", "./..."}},
		{id: "build", name: "Go build", kind: StepBuild, argv: []string{"go", "build", "./..."}},
	} {
		accumulator.addStep(Step{
			ID:               stepID(OriginLanguage, file.path, item.id),
			Name:             item.name,
			Kind:             item.kind,
			Origin:           OriginLanguage,
			Source:           file.path,
			WorkingDirectory: workingDirectory,
			Argv:             item.argv,
			TimeoutSeconds:   defaultStepTimeoutSeconds,
			Required:         true,
		})
	}
	return nil
}

func bytesContainGoModule(raw []byte) bool {
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "module ") && len(strings.TrimSpace(strings.TrimPrefix(line, "module "))) > 0 {
			return true
		}
	}
	return false
}

func (accumulator *discoveryAccumulator) discoverPackageJSON(
	root string,
	file discoveredFile,
	files []discoveredFile,
) error {
	raw, err := readDiscoveryFile(root, file.path, maximumDependencyFileSize)
	if err != nil {
		return err
	}
	var document struct {
		Scripts map[string]string `json:"scripts"`
	}
	if err = decodeStrictJSON(raw, &document); err != nil {
		return fmt.Errorf("%w: parse %s: %v", ErrInvalid, file.path, err)
	}
	definition, err := json.Marshal(document.Scripts)
	if err != nil {
		return fmt.Errorf("encode package scripts: %w", err)
	}
	accumulator.addDefinition(file.path+"#scripts", definition)
	manager := packageManagerForDirectory(file.dir, files)
	selected := []struct {
		kind  StepKind
		names []string
	}{
		{kind: StepLint, names: []string{"lint", "typecheck", "check"}},
		{kind: StepTest, names: []string{"test:unit", "test-unit", "test"}},
		{kind: StepBuild, names: []string{"build"}},
	}
	for _, group := range selected {
		for _, name := range group.names {
			script, exists := document.Scripts[name]
			if !exists || strings.TrimSpace(script) == "" {
				continue
			}
			accumulator.addStep(Step{
				ID:               stepID(OriginPackage, file.path, name),
				Name:             manager + " " + name,
				Kind:             group.kind,
				Origin:           OriginPackage,
				Source:           file.path,
				WorkingDirectory: normalizeWorkingDirectory(file.dir),
				Argv:             packageManagerArguments(manager, name),
				TimeoutSeconds:   defaultStepTimeoutSeconds,
				Required:         true,
			})
			break
		}
	}
	return nil
}

func packageManagerForDirectory(directory string, files []discoveredFile) string {
	for _, file := range files {
		if file.dir != directory {
			continue
		}
		switch file.name {
		case "pnpm-lock.yaml":
			return "pnpm"
		case "yarn.lock":
			return "yarn"
		case "package-lock.json":
			return "npm"
		}
	}
	return "npm"
}

func packageManagerArguments(manager, script string) []string {
	if manager == "pnpm" || manager == "yarn" {
		return []string{"corepack", manager, "run", script}
	}
	return []string{"npm", "run", script, "--"}
}

func (accumulator *discoveryAccumulator) addDefinition(source string, payload []byte) {
	if accumulator.err != nil {
		return
	}
	if len(accumulator.definitions) >= maximumDefinitionFiles ||
		accumulator.definitionBytes+int64(len(payload)) > maximumDefinitionBytes {
		accumulator.err = fmt.Errorf("%w: local CI definitions exceed aggregate bounds", ErrInvalid)
		return
	}
	accumulator.definitionBytes += int64(len(payload))
	accumulator.definitions = append(accumulator.definitions, discoveryFragment{
		Path:    source,
		Payload: append([]byte(nil), payload...),
	})
}

func (accumulator *discoveryAccumulator) addDependency(root, source string) error {
	raw, err := readDiscoveryFile(root, source, maximumDependencyFileSize)
	if err != nil {
		return err
	}
	if len(accumulator.dependencies) >= maximumDependencyFiles ||
		accumulator.dependencyBytes+int64(len(raw)) > maximumDependencyBytes {
		return fmt.Errorf("%w: local CI dependencies exceed aggregate bounds", ErrInvalid)
	}
	accumulator.dependencyBytes += int64(len(raw))
	accumulator.dependencies = append(accumulator.dependencies, discoveryFragment{
		Path:    source,
		Payload: raw,
	})
	return nil
}

func (accumulator *discoveryAccumulator) addStep(step Step) {
	if accumulator.err != nil {
		return
	}
	if len(accumulator.steps) >= maximumPlanSteps {
		accumulator.err = fmt.Errorf("%w: local CI plan contains too many required steps", ErrInvalid)
		return
	}
	accumulator.steps = append(accumulator.steps, step)
}

func (accumulator *discoveryAccumulator) addDiagnostic(diagnostic Diagnostic) {
	if accumulator.err != nil {
		return
	}
	if len(accumulator.diagnostics) >= maximumPlanDiagnostics {
		accumulator.err = fmt.Errorf("%w: local CI plan contains too many diagnostics", ErrInvalid)
		return
	}
	accumulator.diagnostics = append(accumulator.diagnostics, diagnostic)
}

func digestFragments(domain string, fragments []discoveryFragment) string {
	slices.SortFunc(fragments, func(left, right discoveryFragment) int {
		return strings.Compare(left.Path, right.Path)
	})
	parts := make([][]byte, 0, len(fragments)*2)
	for _, fragment := range fragments {
		parts = append(parts, []byte(fragment.Path), fragment.Payload)
	}
	return digestParts(domain, parts...)
}

func stepID(origin StepOrigin, source, name string) string {
	digest := digestParts("picoclaw-local-ci-step-id-v1", []byte(origin), []byte(source), []byte(name))
	return "ci_" + digest[:24]
}

func normalizeWorkingDirectory(directory string) string {
	if directory == "" || directory == "." {
		return ""
	}
	return directory
}

func hasBlockingDiagnostic(diagnostics []Diagnostic) bool {
	for _, diagnostic := range diagnostics {
		switch diagnostic.Code {
		case "passive_action", "ignored_workflow":
			continue
		default:
			return true
		}
	}
	return false
}

func planSemanticDigest(plan Plan) (string, error) {
	return digestJSON("picoclaw-local-ci-plan-semantics-v1", struct {
		Complete    bool
		Steps       []Step
		Diagnostics []Diagnostic
	}{
		Complete:    plan.Complete,
		Steps:       plan.Steps,
		Diagnostics: plan.Diagnostics,
	})
}

func decodeStrictJSON(raw []byte, target any) error {
	if !utf8.Valid(raw) {
		return errors.New("JSON is not valid UTF-8")
	}
	if err := validateJSONDocument(raw); err != nil {
		return err
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err != nil {
			return err
		}
		return errors.New("JSON contains trailing data")
	}
	return nil
}

func validateJSONDocument(raw []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	nodes := 0
	if err := validateJSONValue(decoder, 0, &nodes); err != nil {
		return err
	}
	if token, err := decoder.Token(); !errors.Is(err, io.EOF) {
		if err != nil {
			return err
		}
		return fmt.Errorf("JSON contains trailing token %v", token)
	}
	return nil
}

func validateJSONValue(decoder *json.Decoder, depth int, nodes *int) error {
	if depth > 64 || *nodes > 100_000 {
		return errors.New("JSON complexity limit exceeded")
	}
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	(*nodes)++
	delimiter, compound := token.(json.Delim)
	if !compound {
		return nil
	}
	switch delimiter {
	case '{':
		seen := make(map[string]struct{})
		for decoder.More() {
			keyToken, keyErr := decoder.Token()
			if keyErr != nil {
				return keyErr
			}
			key, ok := keyToken.(string)
			if !ok {
				return errors.New("JSON object key is not a string")
			}
			if _, exists := seen[key]; exists {
				return fmt.Errorf("JSON contains duplicate key %q", key)
			}
			seen[key] = struct{}{}
			if err = validateJSONValue(decoder, depth+1, nodes); err != nil {
				return err
			}
		}
		closing, closeErr := decoder.Token()
		if closeErr != nil || closing != json.Delim('}') {
			return errors.New("JSON object is not closed")
		}
	case '[':
		for decoder.More() {
			if err = validateJSONValue(decoder, depth+1, nodes); err != nil {
				return err
			}
		}
		closing, closeErr := decoder.Token()
		if closeErr != nil || closing != json.Delim(']') {
			return errors.New("JSON array is not closed")
		}
	default:
		return errors.New("JSON contains an invalid delimiter")
	}
	return nil
}
