package storecatalog

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"testing"
)

const storeCatalogImportPath = "github.com/sipeed/picoclaw/internal/storecatalog"

var storeCatalogAllowedImporters = map[string]bool{
	"internal/databaseclaims/claims.go":            true,
	"internal/databaseclaims/scoped_claims.go":     true,
	"internal/databasemigration/backup_archive.go": true,
	"internal/databasemigration/backup_parent.go":  true,
	"internal/databasemigration/backup_prepare.go": true,
	"internal/databasemigration/migration.go":      true,
	"internal/databasereadiness/readiness.go":      true,
	"pkg/database/catalog/catalog.go":              true,
	"pkg/database/catalog/review_scope.go":         true,
}

func TestStoreCatalogHasOnlyExactProductionImportersAndReviewBridges(t *testing.T) {
	t.Parallel()

	repositoryRoot := storeCatalogRepositoryRoot(t)
	sources := make(map[string]string)
	var violations []string
	err := filepath.WalkDir(repositoryRoot, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if path != repositoryRoot && entry.Type()&os.ModeSymlink != 0 {
			relative, err := filepath.Rel(repositoryRoot, path)
			if err != nil {
				return err
			}
			violations = append(violations, filepath.ToSlash(relative)+": repository entry is a symlink")
			return nil
		}
		if entry.IsDir() {
			if path != repositoryRoot && storeCatalogImportGuardSkipsDir(entry.Name()) {
				return filepath.SkipDir
			}
			return nil
		}
		if filepath.Ext(path) != ".go" || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		relative, err := filepath.Rel(repositoryRoot, path)
		if err != nil {
			return err
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		sources[filepath.ToSlash(relative)] = string(raw)
		return nil
	})
	if err != nil {
		t.Fatalf("scan production imports: %v", err)
	}
	violations = append(violations, storeCatalogImportViolations(sources)...)
	if len(violations) != 0 {
		sort.Strings(violations)
		t.Fatalf("store catalog production boundary changed:\n%s", strings.Join(violations, "\n"))
	}
}

func storeCatalogImportViolations(sources map[string]string) []string {
	paths := make([]string, 0, len(sources))
	for path := range sources {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	imports := make(map[string]int)
	references := map[string]map[string]int{
		"NewReviewScope":        {},
		"RevalidateReviewScope": {},
		"ReviewScopeCatalogs":   {},
	}
	directCalls := map[string]map[string]int{
		"NewReviewScope":        {},
		"RevalidateReviewScope": {},
		"ReviewScopeCatalogs":   {},
	}
	var violations []string
	for _, path := range paths {
		fileSet := token.NewFileSet()
		parsed, err := parser.ParseFile(
			fileSet, path, sources[path], parser.ParseComments|parser.AllErrors,
		)
		if err != nil {
			violations = append(violations, fmt.Sprintf("%s: parse production source: %v", path, err))
			continue
		}
		aliases := make(map[string]bool)
		for _, imported := range parsed.Imports {
			importPath, err := strconv.Unquote(imported.Path.Value)
			if err != nil || importPath != storeCatalogImportPath {
				continue
			}
			alias := "storecatalog"
			if imported.Name != nil {
				alias = imported.Name.Name
			}
			aliases[alias] = true
			imports[path]++
			if !storeCatalogAllowedImporters[path] {
				violations = append(violations, fmt.Sprintf(
					"%s:%d imports the physical store catalog", path,
					fileSet.Position(imported.Pos()).Line,
				))
			}
			if alias == "." || alias == "_" {
				violations = append(violations, fmt.Sprintf(
					"%s:%d imports the store catalog with forbidden alias %q", path,
					fileSet.Position(imported.Pos()).Line, alias,
				))
			}
		}
		for _, group := range parsed.Comments {
			for _, comment := range group.List {
				if strings.Contains(comment.Text, "go:linkname") &&
					strings.Contains(comment.Text, storeCatalogImportPath) {
					violations = append(violations, fmt.Sprintf(
						"%s:%d linknames the physical store catalog", path,
						fileSet.Position(comment.Pos()).Line,
					))
				}
			}
		}
		ast.Inspect(parsed, func(node ast.Node) bool {
			selector, ok := node.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			qualifier, ok := selector.X.(*ast.Ident)
			if !ok || !aliases[qualifier.Name] {
				return true
			}
			switch selector.Sel.Name {
			case "NewReviewScope", "RevalidateReviewScope", "ReviewScopeCatalogs":
				references[selector.Sel.Name][path]++
				if call, ok := storeCatalogParentCall(parsed, selector); ok && call.Fun == selector {
					directCalls[selector.Sel.Name][path]++
				}
			}
			return true
		})
	}
	for path := range storeCatalogAllowedImporters {
		if imports[path] != 1 {
			violations = append(violations, fmt.Sprintf(
				"%s store catalog import count = %d, want 1", path, imports[path],
			))
		}
	}
	for _, bridge := range []struct {
		symbol      string
		path        string
		directCalls int
	}{
		{symbol: "NewReviewScope", path: "pkg/database/catalog/review_scope.go", directCalls: 1},
		{symbol: "RevalidateReviewScope", path: "internal/databaseclaims/scoped_claims.go"},
		{symbol: "ReviewScopeCatalogs", path: "internal/databaseclaims/scoped_claims.go"},
	} {
		for path, count := range references[bridge.symbol] {
			if path != bridge.path && count != 0 {
				violations = append(violations, fmt.Sprintf(
					"%s references privileged bridge %s", path, bridge.symbol,
				))
			}
		}
		if references[bridge.symbol][bridge.path] != 1 ||
			directCalls[bridge.symbol][bridge.path] != bridge.directCalls {
			violations = append(violations, fmt.Sprintf(
				"%s %s references/direct calls = %d/%d, want 1/%d",
				bridge.path, bridge.symbol, references[bridge.symbol][bridge.path],
				directCalls[bridge.symbol][bridge.path], bridge.directCalls,
			))
		}
	}
	violations = append(
		violations,
		storeCatalogReviewScopeConstructorViolations(
			sources["internal/storecatalog/review_scope.go"],
		)...,
	)
	sort.Strings(violations)
	return violations
}

func storeCatalogReviewScopeConstructorViolations(source string) []string {
	const path = "internal/storecatalog/review_scope.go"
	fileSet := token.NewFileSet()
	parsed, err := parser.ParseFile(fileSet, path, source, parser.AllErrors)
	if err != nil {
		return []string{fmt.Sprintf("%s: parse review scope constructor: %v", path, err)}
	}
	declarations, projectCalls, deriveCalls := 0, 0, 0
	for _, declaration := range parsed.Decls {
		function, ok := declaration.(*ast.FuncDecl)
		if !ok || function.Name.Name != "NewReviewScope" {
			continue
		}
		declarations++
		ast.Inspect(function.Body, func(node ast.Node) bool {
			call, ok := node.(*ast.CallExpr)
			if !ok {
				return true
			}
			identifier, ok := call.Fun.(*ast.Ident)
			if !ok {
				return true
			}
			switch identifier.Name {
			case "Project":
				projectCalls++
			case "newReviewScope":
				deriveCalls++
			}
			return true
		})
	}
	var violations []string
	if declarations != 1 {
		violations = append(violations, fmt.Sprintf(
			"%s NewReviewScope declaration count = %d, want 1", path, declarations,
		))
	}
	if projectCalls != 1 || deriveCalls != 1 {
		violations = append(violations, fmt.Sprintf(
			"%s NewReviewScope Project/newReviewScope calls = %d/%d, want 1/1",
			path, projectCalls, deriveCalls,
		))
	}
	return violations
}

func storeCatalogParentCall(file *ast.File, target ast.Node) (*ast.CallExpr, bool) {
	var parent *ast.CallExpr
	ast.Inspect(file, func(node ast.Node) bool {
		call, ok := node.(*ast.CallExpr)
		if !ok {
			return true
		}
		if call.Fun == target {
			parent = call
			return false
		}
		return true
	})
	return parent, parent != nil
}

func TestStoreCatalogGuardRejectsImportAndBridgeEscapes(t *testing.T) {
	t.Parallel()
	validFacade := `package catalog
import "github.com/sipeed/picoclaw/internal/storecatalog"
func use() { _, _ = storecatalog.NewReviewScope(storecatalog.Options{}, "missing") }
`
	validClaims := `package databaseclaims
import "github.com/sipeed/picoclaw/internal/storecatalog"
var revalidate = storecatalog.RevalidateReviewScope
var catalogs = storecatalog.ReviewScopeCatalogs
`
	validConstructor := `package storecatalog
func NewReviewScope(options Options, revision string) (*ReviewScope, error) {
	full, err := Project(options)
	if err != nil { return nil, err }
	return newReviewScope(full, revision)
}
`
	base := map[string]string{
		"pkg/database/catalog/review_scope.go":     validFacade,
		"internal/databaseclaims/scoped_claims.go": validClaims,
		"internal/storecatalog/review_scope.go":    validConstructor,
	}
	for _, fixture := range []struct {
		name     string
		path     string
		source   string
		contains string
	}{
		{
			name: "buildable gen importer", path: "pkg/gen/escape.go",
			source: `package escape
import "github.com/sipeed/picoclaw/internal/storecatalog"
var _ = storecatalog.Project
`, contains: "imports the physical store catalog",
		},
		{
			name: "dot import", path: "pkg/database/catalog/catalog.go",
			source: `package catalog
import . "github.com/sipeed/picoclaw/internal/storecatalog"
var _ = Project
`, contains: "forbidden alias",
		},
		{
			name: "linkname", path: "pkg/escape/escape.go",
			source: `package escape
import _ "unsafe"
//go:linkname scope github.com/sipeed/picoclaw/internal/storecatalog.NewReviewScope
func scope()
`, contains: "linknames the physical store catalog",
		},
		{
			name: "bridge function value", path: "pkg/database/catalog/review_scope.go",
			source: `package catalog
import "github.com/sipeed/picoclaw/internal/storecatalog"
var scope = storecatalog.NewReviewScope
`, contains: "references/direct calls = 1/0",
		},
		{
			name: "additional bridge", path: "internal/databasereadiness/readiness.go",
			source: `package databasereadiness
import "github.com/sipeed/picoclaw/internal/storecatalog"
func use(scope *storecatalog.ReviewScope) { _, _, _ = storecatalog.ReviewScopeCatalogs(scope) }
`, contains: "references privileged bridge",
		},
		{
			name: "multiple complete projections", path: "internal/storecatalog/review_scope.go",
			source: `package storecatalog
func NewReviewScope(options Options, revision string) (*ReviewScope, error) {
	first, _ := Project(options)
	_, _ = Project(options)
	return newReviewScope(first, revision)
}
`, contains: "Project/newReviewScope calls = 2/1, want 1/1",
		},
	} {
		t.Run(fixture.name, func(t *testing.T) {
			t.Parallel()
			sources := make(map[string]string, len(base)+1)
			for path, source := range base {
				sources[path] = source
			}
			sources[fixture.path] = fixture.source
			violations := strings.Join(storeCatalogImportViolations(sources), "\n")
			if !strings.Contains(violations, fixture.contains) {
				t.Fatalf("guard violations = %q, want %q", violations, fixture.contains)
			}
		})
	}
}

func storeCatalogRepositoryRoot(t *testing.T) string {
	t.Helper()
	_, currentFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve import-guard source path")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(currentFile), "..", ".."))
}

func storeCatalogImportGuardSkipsDir(name string) bool {
	switch name {
	case ".git", ".cache", "node_modules", "testdata", "vendor":
		return true
	default:
		return false
	}
}

func TestStoreCatalogImportGuardSkipsOnlyExactNonProductionDirectories(t *testing.T) {
	t.Parallel()
	for _, name := range []string{".git", ".cache", "node_modules", "testdata", "vendor"} {
		if !storeCatalogImportGuardSkipsDir(name) {
			t.Errorf("guard did not skip exact non-production directory %q", name)
		}
	}
	for _, name := range []string{
		".Cache", "Node_Modules", "TestData", "Vendor", "cache", "gen", "generated",
	} {
		if storeCatalogImportGuardSkipsDir(name) {
			t.Errorf("guard skipped buildable lookalike directory %q", name)
		}
	}
}
