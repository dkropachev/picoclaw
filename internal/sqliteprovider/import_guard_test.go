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

func TestSQLiteProviderBoundaryVersionIsStable(t *testing.T) {
	if providerBoundaryVersion != "picoclaw/sqlite-provider-boundary/v1" {
		t.Fatalf("provider boundary version = %q", providerBoundaryVersion)
	}
}

func TestSQLiteProviderProductionImportersAreExplicit(t *testing.T) {
	t.Parallel()

	repositoryRoot := sqliteProviderRepositoryRoot(t)
	allowed := map[string]bool{
		"internal/databasemigration/backup.go":    true,
		"internal/databasemigration/migration.go": true,
		"internal/databasereadiness/readiness.go": true,
		"internal/sqlitestore/open.go":            false,
		"internal/sqlitestore/schema.go":          false,
	}
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
		relative = filepath.ToSlash(relative)
		fileSet := token.NewFileSet()
		parsed, err := parser.ParseFile(fileSet, path, nil, parser.ImportsOnly)
		if err != nil {
			return fmt.Errorf("parse production Go file %s: %w", relative, err)
		}
		for _, imported := range parsed.Imports {
			importPath, err := strconv.Unquote(imported.Path.Value)
			if err != nil {
				return err
			}
			if importPath != sqliteProviderImportPath {
				continue
			}
			if _, ok := allowed[relative]; !ok {
				violations = append(violations, fmt.Sprintf(
					"%s:%d", relative, fileSet.Position(imported.Pos()).Line,
				))
			} else {
				allowed[relative] = true
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("scan production imports: %v", err)
	}
	for path, found := range allowed {
		if !found {
			violations = append(violations, path+": expected provider importer is missing")
		}
	}
	if len(violations) != 0 {
		sort.Strings(violations)
		t.Fatalf("SQLite provider importer allowlist mismatch:\n%s", strings.Join(violations, "\n"))
	}
}

func TestSQLiteProviderOwnsDriverOpen(t *testing.T) {
	t.Parallel()

	root := filepath.Join(sqliteProviderRepositoryRoot(t), "internal", "sqliteprovider")
	moderncUsers := map[string]bool{
		"maintenance.go":      true,
		"provider.go":         true,
		"staged_migration.go": true,
	}
	var violations []string
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() || filepath.Ext(path) != ".go" || strings.HasSuffix(path, "_test.go") {
			return nil
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
			if importPath == "modernc.org/sqlite" && !moderncUsers[entry.Name()] {
				violations = append(violations, entry.Name()+": unexpected SQLite driver import")
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
			if ok && sqlNames[identifier.Name] && entry.Name() != "provider.go" {
				violations = append(violations, fmt.Sprintf(
					"%s:%d calls database/sql.Open", entry.Name(), fileSet.Position(call.Pos()).Line,
				))
			}
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatalf("inspect SQLite provider: %v", err)
	}
	if len(violations) != 0 {
		sort.Strings(violations)
		t.Fatalf("SQLite provider driver boundary mismatch:\n%s", strings.Join(violations, "\n"))
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
