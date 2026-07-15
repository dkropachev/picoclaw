//go:build featuretools

package main

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strings"

	"github.com/sipeed/picoclaw/pkg/config"
)

type featureSurface struct {
	Kind   string
	ID     string
	Source string
}

type featureSpecMetadata struct {
	Path       string
	RelPath    string
	FeatureID  string
	Text       string
	Ownerships []featureOwnership
}

type featureOwnership struct {
	Kind        string
	Pattern     string
	SpecPath    string
	SpecRelPath string
}

func repoRoot() (string, error) {
	wd, err := os.Getwd()
	if err != nil {
		return "", err
	}
	cur, err := filepath.Abs(wd)
	if err != nil {
		return "", err
	}
	for {
		if _, err := os.Stat(filepath.Join(cur, "go.mod")); err == nil {
			return cur, nil
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			return "", fmt.Errorf("could not find repository root from %s", wd)
		}
		cur = parent
	}
}

func defaultBaseRef() string {
	if ref := strings.TrimSpace(os.Getenv("BASE_REF")); ref != "" {
		return ref
	}
	if ref := strings.TrimSpace(os.Getenv("GITHUB_BASE_REF")); ref != "" {
		return "origin/" + ref
	}
	return "origin/main"
}

func gitOutput(root string, args ...string) (string, error) {
	cmd := exec.Command("git", append([]string{"-C", root}, args...)...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg != "" {
			return "", fmt.Errorf("%w: %s", err, msg)
		}
		return "", err
	}
	return string(out), nil
}

func changedFiles(root, base, head string) ([]string, error) {
	out, err := gitOutput(root, "diff", "--name-only", "--diff-filter=ACMRTD", base+"..."+head)
	if err != nil {
		return nil, fmt.Errorf("git diff %s...%s: %w", base, head, err)
	}
	var files []string
	for _, line := range strings.Split(out, "\n") {
		line = normalizeRepoPath(line)
		if line != "" {
			files = append(files, line)
		}
	}
	sort.Strings(files)
	return files, nil
}

func loadFeatureSpecs(root string) ([]featureSpecMetadata, error) {
	featuresDir := filepath.Join(root, "docs", "features")
	entries, err := os.ReadDir(featuresDir)
	if err != nil {
		return nil, fmt.Errorf("read docs/features: %w", err)
	}
	var specs []featureSpecMetadata
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".md") {
			continue
		}
		switch entry.Name() {
		case "README.md", "template.md":
			continue
		}
		path := filepath.Join(featuresDir, entry.Name())
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", rel(root, path), err)
		}
		text := string(data)
		relPath := rel(root, path)
		specs = append(specs, featureSpecMetadata{
			Path:       path,
			RelPath:    relPath,
			FeatureID:  featureIDFromText(text),
			Text:       text,
			Ownerships: parseFeatureOwnerships(relPath, text),
		})
	}
	sort.Slice(specs, func(i, j int) bool {
		return specs[i].RelPath < specs[j].RelPath
	})
	return specs, nil
}

func featureIDFromText(text string) string {
	re := regexp.MustCompile(`FR-[A-Z0-9-]+`)
	if id := re.FindString(text); id != "" {
		return id
	}
	return ""
}

func parseFeatureOwnerships(specRelPath, text string) []featureOwnership {
	re := regexp.MustCompile(`(?m)^Owns:\s+([A-Z][A-Z0-9_-]*)\s+(.+)$`)
	var ownerships []featureOwnership
	for _, match := range re.FindAllStringSubmatch(text, -1) {
		kind := strings.TrimSpace(match[1])
		pattern := strings.TrimSpace(match[2])
		pattern = strings.Trim(pattern, "`")
		if kind == "" || pattern == "" {
			continue
		}
		ownerships = append(ownerships, featureOwnership{
			Kind:        kind,
			Pattern:     normalizeRepoPathPattern(pattern),
			SpecRelPath: specRelPath,
		})
	}
	return ownerships
}

func codeOwnersForPath(specs []featureSpecMetadata, path string) []featureOwnership {
	path = normalizeRepoPath(path)
	var owners []featureOwnership
	for _, spec := range specs {
		for _, owner := range spec.Ownerships {
			if owner.Kind != "CODE" {
				continue
			}
			if codePatternMatches(owner.Pattern, path) {
				owner.SpecPath = spec.Path
				owners = append(owners, owner)
			}
		}
	}
	sort.Slice(owners, func(i, j int) bool {
		if owners[i].SpecRelPath != owners[j].SpecRelPath {
			return owners[i].SpecRelPath < owners[j].SpecRelPath
		}
		return owners[i].Pattern < owners[j].Pattern
	})
	return owners
}

func codePatternMatches(pattern, path string) bool {
	pattern = normalizeRepoPathPattern(pattern)
	path = normalizeRepoPath(path)
	if pattern == "" || path == "" {
		return false
	}
	if strings.HasSuffix(pattern, "/**") {
		prefix := strings.TrimSuffix(pattern, "/**")
		return path == prefix || strings.HasPrefix(path, prefix+"/")
	}
	return globMatch(pattern, path)
}

func normalizeRepoPathPattern(pattern string) string {
	pattern = strings.TrimSpace(strings.Trim(pattern, "`"))
	pattern = strings.TrimPrefix(pattern, "./")
	pattern = strings.TrimPrefix(pattern, "/")
	return filepath.ToSlash(pattern)
}

func normalizeRepoPath(path string) string {
	path = strings.TrimSpace(path)
	path = strings.TrimPrefix(path, "./")
	path = strings.TrimPrefix(path, "/")
	return filepath.ToSlash(path)
}

func isIgnoredProductionPath(path string) bool {
	if strings.Contains(path, "/testdata/") ||
		strings.Contains(path, "/dist/") ||
		strings.HasSuffix(path, ".gen.ts") ||
		strings.HasSuffix(path, ".generated.go") ||
		strings.HasSuffix(path, "_generated.go") {
		return true
	}
	return false
}

func isProductionCodePath(path string) bool {
	path = normalizeRepoPath(path)
	if path == "" {
		return false
	}
	if isIgnoredProductionPath(path) {
		return false
	}
	if strings.HasPrefix(path, "cmd/") || strings.HasPrefix(path, "pkg/") || strings.HasPrefix(path, "web/backend/") {
		return strings.HasSuffix(path, ".go") && !strings.HasSuffix(path, "_test.go")
	}
	if strings.HasPrefix(path, "web/frontend/src/") {
		return isFrontendSourcePath(path)
	}
	if strings.HasPrefix(path, "integration/") {
		return strings.HasSuffix(path, ".go") && !strings.HasSuffix(path, "_test.go") ||
			strings.HasSuffix(path, ".sh") ||
			strings.HasSuffix(path, "suite.env")
	}
	return false
}

func isFrontendSourcePath(path string) bool {
	switch filepath.Ext(path) {
	case ".ts", ".tsx", ".js", ".jsx", ".css":
		return !strings.Contains(path, ".test.") && !strings.Contains(path, ".spec.")
	default:
		return false
	}
}

func globMatch(pattern, value string) bool {
	quoted := regexp.QuoteMeta(pattern)
	quoted = strings.ReplaceAll(quoted, `\*`, `.*`)
	quoted = strings.ReplaceAll(quoted, `\?`, `.`)
	re, err := regexp.Compile("^" + quoted + "$")
	if err != nil {
		return false
	}
	return re.MatchString(value)
}

func discoverFeatureSurfaces(root string) ([]featureSurface, error) {
	seen := make(map[string]featureSurface)
	add := func(kind, id, source string) {
		id = strings.TrimSpace(id)
		if id == "" {
			return
		}
		key := kind + "\x00" + id
		if old, ok := seen[key]; ok {
			if old.Source == "" && source != "" {
				old.Source = source
				seen[key] = old
			}
			return
		}
		seen[key] = featureSurface{Kind: kind, ID: id, Source: source}
	}

	if err := discoverHTTPRoutes(root, add); err != nil {
		return nil, err
	}
	if err := discoverCLICommands(root, add); err != nil {
		return nil, err
	}
	discoverConfigFields(add)
	if err := discoverChannels(root, add); err != nil {
		return nil, err
	}
	if err := discoverTools(root, add); err != nil {
		return nil, err
	}
	if err := discoverRuntimeEvents(root, add); err != nil {
		return nil, err
	}
	if err := discoverTests(root, add); err != nil {
		return nil, err
	}
	if err := discoverIntegrationSuites(root, add); err != nil {
		return nil, err
	}

	surfaces := make([]featureSurface, 0, len(seen))
	for _, surface := range seen {
		surfaces = append(surfaces, surface)
	}
	sort.Slice(surfaces, func(i, j int) bool {
		if surfaces[i].Kind != surfaces[j].Kind {
			return surfaces[i].Kind < surfaces[j].Kind
		}
		return surfaces[i].ID < surfaces[j].ID
	})
	return surfaces, nil
}

func discoverHTTPRoutes(root string, add func(kind, id, source string)) error {
	re := regexp.MustCompile(`mux\.HandleFunc\("([^"]+)"`)
	return walkGoFiles(filepath.Join(root, "web", "backend", "api"), func(path string, data string) error {
		for _, match := range re.FindAllStringSubmatch(data, -1) {
			add("HTTP", "HTTP "+match[1], rel(root, path))
		}
		return nil
	})
}

func discoverCLICommands(root string, add func(kind, id, source string)) error {
	re := regexp.MustCompile(`Use:\s+"([^"]+)"`)
	for _, dir := range []string{filepath.Join(root, "cmd", "picoclaw")} {
		if err := walkGoFiles(dir, func(path string, data string) error {
			for _, match := range re.FindAllStringSubmatch(data, -1) {
				use := strings.TrimSpace(strings.Split(match[1], "\n")[0])
				add("CLI", fmt.Sprintf("CLI %s %s", rel(root, path), use), rel(root, path))
			}
			return nil
		}); err != nil {
			return err
		}
	}
	return nil
}

func discoverConfigFields(add func(kind, id, source string)) {
	visited := make(map[reflect.Type]bool)
	collectConfigType(reflect.TypeOf(config.Config{}), "CONFIG", add, visited)
}

func collectConfigType(t reflect.Type, prefix string, add func(kind, id, source string), visited map[reflect.Type]bool) {
	t = derefType(t)
	if t.Kind() != reflect.Struct || visited[t] {
		return
	}
	visited[t] = true
	defer delete(visited, t)

	for i := 0; i < t.NumField(); i++ {
		field := t.Field(i)
		if field.PkgPath != "" {
			continue
		}
		name := jsonFieldName(field)
		if name == "" {
			continue
		}
		path := prefix + "." + name
		add("CONFIG", path, "pkg/config")
		collectNestedConfig(field.Type, path, add, visited)
	}
}

func collectNestedConfig(t reflect.Type, prefix string, add func(kind, id, source string), visited map[reflect.Type]bool) {
	t = derefType(t)
	switch t.Kind() {
	case reflect.Struct:
		if shouldRecurseConfigStruct(t) {
			collectConfigType(t, prefix, add, visited)
		}
	case reflect.Slice, reflect.Array:
		elem := derefType(t.Elem())
		if elem.Kind() == reflect.Struct && shouldRecurseConfigStruct(elem) {
			collectConfigType(elem, prefix+".*", add, visited)
		}
	case reflect.Map:
		elem := derefType(t.Elem())
		if elem.Kind() == reflect.Struct && shouldRecurseConfigStruct(elem) {
			collectConfigType(elem, prefix+".*", add, visited)
		}
	}
}

func shouldRecurseConfigStruct(t reflect.Type) bool {
	if t.PkgPath() != "github.com/sipeed/picoclaw/pkg/config" {
		return false
	}
	switch t.Name() {
	case "SecureString", "BuildInfo":
		return false
	default:
		return true
	}
}

func derefType(t reflect.Type) reflect.Type {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	return t
}

func jsonFieldName(field reflect.StructField) string {
	tag := field.Tag.Get("json")
	if tag == "" || tag == "-" {
		return ""
	}
	name := strings.Split(tag, ",")[0]
	if name == "" || name == "-" {
		return ""
	}
	return name
}

func discoverChannels(root string, add func(kind, id, source string)) error {
	base := filepath.Join(root, "pkg", "channels")
	entries, err := os.ReadDir(base)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		name := entry.Name()
		if hasGoFile(filepath.Join(base, name)) {
			add("CHANNEL", "CHANNEL "+name, rel(root, filepath.Join(base, name)))
		}
	}
	return nil
}

func discoverTools(root string, add func(kind, id, source string)) error {
	re := regexp.MustCompile(`func \([^)]*\) Name\(\) string \{\s*return "([^"]+)"`)
	return walkGoFiles(filepath.Join(root, "pkg", "tools"), func(path string, data string) error {
		for _, match := range re.FindAllStringSubmatch(data, -1) {
			add("TOOL", "TOOL "+match[1], rel(root, path))
		}
		return nil
	})
}

func discoverRuntimeEvents(root string, add func(kind, id, source string)) error {
	path := filepath.Join(root, "pkg", "events", "kind.go")
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	re := regexp.MustCompile(`Kind = "([^"]+)"`)
	for _, match := range re.FindAllStringSubmatch(string(data), -1) {
		add("EVENT", "EVENT "+match[1], rel(root, path))
	}
	return nil
}

func discoverTests(root string, add func(kind, id, source string)) error {
	re := regexp.MustCompile(`func (Test|Benchmark)[A-Za-z0-9_]*\(`)
	return filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			switch entry.Name() {
			case ".git", ".cache", "build", "dist", "node_modules", "vendor":
				if path != root {
					return filepath.SkipDir
				}
			}
			return nil
		}
		if !strings.HasSuffix(entry.Name(), "_test.go") {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for _, match := range re.FindAllString(string(data), -1) {
			name := strings.TrimSuffix(strings.TrimPrefix(match, "func "), "(")
			add("TEST", fmt.Sprintf("TEST %s %s", rel(root, path), name), rel(root, path))
		}
		return nil
	})
}

func discoverIntegrationSuites(root string, add func(kind, id, source string)) error {
	base := filepath.Join(root, "integration", "suites")
	entries, err := os.ReadDir(base)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		suiteEnv := filepath.Join(base, entry.Name(), "suite.env")
		if _, err := os.Stat(suiteEnv); err == nil {
			add("INTEGRATION", "INTEGRATION "+entry.Name(), rel(root, suiteEnv))
		}
	}
	return nil
}

func walkGoFiles(root string, visit func(path string, data string) error) error {
	return filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			switch entry.Name() {
			case ".git", ".cache", "node_modules", "vendor":
				if path != root {
					return filepath.SkipDir
				}
			}
			return nil
		}
		if !strings.HasSuffix(entry.Name(), ".go") || strings.HasSuffix(entry.Name(), "_test.go") {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return visit(path, string(data))
	})
}

func hasGoFile(dir string) bool {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return false
	}
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		if strings.HasSuffix(entry.Name(), ".go") && !strings.HasSuffix(entry.Name(), "_test.go") {
			return true
		}
	}
	return false
}

func rel(root, path string) string {
	relative, err := filepath.Rel(root, path)
	if err != nil {
		return filepath.ToSlash(path)
	}
	return filepath.ToSlash(relative)
}
