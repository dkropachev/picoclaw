package sqliteprovider

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"testing"
)

const sqliteProviderImportPath = "github.com/sipeed/picoclaw/internal/sqliteprovider"

func TestSQLiteProviderHasNoProductionImporters(t *testing.T) {
	t.Parallel()

	repositoryRoot := sqliteProviderRepositoryRoot(t)
	var violations []string
	err := filepath.WalkDir(repositoryRoot, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			if path != repositoryRoot && sqliteProviderImportGuardSkipsDir(entry.Name()) {
				return filepath.SkipDir
			}
			return nil
		}
		if filepath.Ext(path) != ".go" || strings.HasSuffix(path, "_test.go") ||
			filepath.Clean(filepath.Dir(path)) == filepath.Join(repositoryRoot, "internal", "sqliteprovider") {
			return nil
		}

		relative, err := filepath.Rel(repositoryRoot, path)
		if err != nil {
			return err
		}
		fileSet := token.NewFileSet()
		parsed, err := parser.ParseFile(fileSet, path, nil, parser.ImportsOnly)
		if err != nil {
			return fmt.Errorf("parse production Go file %s: %w", filepath.ToSlash(relative), err)
		}
		for _, imported := range parsed.Imports {
			importPath, err := strconv.Unquote(imported.Path.Value)
			if err != nil {
				return err
			}
			if importPath == sqliteProviderImportPath {
				violations = append(violations, fmt.Sprintf(
					"%s:%d", filepath.ToSlash(relative), fileSet.Position(imported.Pos()).Line,
				))
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("scan production imports: %v", err)
	}
	if len(violations) != 0 {
		sort.Strings(violations)
		t.Fatalf("dormant SQLite provider imported by production code:\n%s", strings.Join(violations, "\n"))
	}
}

func TestSQLiteProviderSliceHasNoDriverOpenOrFilesystemBinding(t *testing.T) {
	t.Parallel()

	packageRoot := filepath.Join(sqliteProviderRepositoryRoot(t), "internal", "sqliteprovider")
	allowedFiles := map[string]bool{"control.go": true, "schema.go": true}
	forbiddenImports := map[string]bool{
		"database/sql/driver": true,
		"io/fs":               true,
		"net/url":             true,
		"os":                  true,
		"path/filepath":       true,
		"syscall":             true,
		"modernc.org/sqlite":  true,
	}
	var violations []string
	err := filepath.WalkDir(packageRoot, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() || filepath.Ext(path) != ".go" || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		if !allowedFiles[entry.Name()] {
			violations = append(violations, filepath.ToSlash(entry.Name())+": production file is outside control slice")
		}

		fileSet := token.NewFileSet()
		parsed, err := parser.ParseFile(fileSet, path, nil, parser.AllErrors)
		if err != nil {
			return fmt.Errorf("parse %s: %w", path, err)
		}
		sqlNames := map[string]bool{}
		for _, imported := range parsed.Imports {
			importPath, err := strconv.Unquote(imported.Path.Value)
			if err != nil {
				return err
			}
			if forbiddenImports[importPath] {
				violations = append(violations, fmt.Sprintf(
					"%s:%d imports %s", entry.Name(), fileSet.Position(imported.Pos()).Line, importPath,
				))
			}
			if importPath == "database/sql" {
				name := "sql"
				if imported.Name != nil {
					name = imported.Name.Name
				}
				sqlNames[name] = true
			}
		}
		ast.Inspect(parsed, func(node ast.Node) bool {
			call, ok := node.(*ast.CallExpr)
			if !ok {
				return true
			}
			selector, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || selector.Sel.Name != "Open" {
				return true
			}
			identifier, ok := selector.X.(*ast.Ident)
			if ok && sqlNames[identifier.Name] {
				violations = append(violations, fmt.Sprintf(
					"%s:%d calls database/sql.Open", entry.Name(), fileSet.Position(call.Pos()).Line,
				))
			}
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatalf("inspect SQLite provider slice: %v", err)
	}
	if len(violations) != 0 {
		sort.Strings(violations)
		t.Fatalf("dormant SQLite control slice grew a driver/open/path binding:\n%s", strings.Join(violations, "\n"))
	}
}

func sqliteProviderRepositoryRoot(t *testing.T) string {
	t.Helper()

	_, currentFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve SQLite provider guard source path")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(currentFile), "..", ".."))
}

func sqliteProviderImportGuardSkipsDir(name string) bool {
	switch strings.ToLower(name) {
	case ".git", ".cache", "cache", "generated", "gen", "node_modules", "testdata", "vendor":
		return true
	default:
		return false
	}
}
