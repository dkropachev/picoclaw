package sqliteprovider

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

const sqliteProviderImportPath = "github.com/sipeed/picoclaw/internal/sqliteprovider"

const immutableGenerationSourceMinter = "internal/databasemigration/backup_prepare.go"

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
		parsed, err := parser.ParseFile(fileSet, path, nil, parser.AllErrors)
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
			if _, ok := allowed[relative]; !ok && relative != immutableGenerationSourceMinter {
				violations = append(violations, fmt.Sprintf(
					"%s:%d", relative, fileSet.Position(imported.Pos()).Line,
				))
			} else if ok {
				allowed[relative] = true
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("scan production imports: %v", err)
	}
	mintViolations, mintErr := immutableGenerationSourceMintViolations(repositoryRoot)
	if mintErr != nil {
		t.Fatalf("scan immutable source mints: %v", mintErr)
	}
	violations = append(violations, mintViolations...)
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

func TestSQLiteProviderImmutableSourceMintBoundaryRejectsUnauthorizedCallers(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	files := map[string]string{
		immutableGenerationSourceMinter: `package databasemigration
import provider "github.com/sipeed/picoclaw/internal/sqliteprovider"
var _ = provider.NewImmutableGenerationSource
`,
		"internal/databasemigration/migration.go": `package databasemigration
import provider "github.com/sipeed/picoclaw/internal/sqliteprovider"
var _ = provider.NewImmutableGenerationSource
`,
		"internal/databasereadiness/readiness.go": `package databasereadiness
import . "github.com/sipeed/picoclaw/internal/sqliteprovider"
`,
	}
	for relative, source := range files {
		path := filepath.Join(root, filepath.FromSlash(relative))
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(source), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	violations, err := immutableGenerationSourceMintViolations(root)
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(violations, "\n")
	if strings.Contains(joined, immutableGenerationSourceMinter) {
		t.Fatalf("approved immutable-source minter was rejected:\n%s", joined)
	}
	for _, expected := range []string{
		"internal/databasemigration/migration.go:3 cannot mint",
		"internal/databasereadiness/readiness.go:2 cannot use a dot",
	} {
		if !strings.Contains(joined, expected) {
			t.Errorf("mint violations missing %q:\n%s", expected, joined)
		}
	}
}

func immutableGenerationSourceMintViolations(repositoryRoot string) ([]string, error) {
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
		parsed, err := parser.ParseFile(fileSet, path, nil, parser.AllErrors)
		if err != nil {
			return fmt.Errorf("parse production Go file %s: %w", relative, err)
		}
		aliases := make(map[string]struct{})
		for _, imported := range parsed.Imports {
			importPath, unquoteErr := strconv.Unquote(imported.Path.Value)
			if unquoteErr != nil {
				return unquoteErr
			}
			if importPath != sqliteProviderImportPath {
				continue
			}
			alias := "sqliteprovider"
			if imported.Name != nil {
				alias = imported.Name.Name
			}
			if alias == "." {
				violations = append(violations, fmt.Sprintf(
					"%s:%d cannot use a dot SQLite-provider import",
					relative, fileSet.Position(imported.Pos()).Line,
				))
				continue
			}
			if alias != "_" {
				aliases[alias] = struct{}{}
			}
		}
		ast.Inspect(parsed, func(node ast.Node) bool {
			selector, ok := node.(*ast.SelectorExpr)
			if !ok || selector.Sel.Name != "NewImmutableGenerationSource" {
				return true
			}
			identifier, ok := selector.X.(*ast.Ident)
			if !ok {
				return true
			}
			if _, imported := aliases[identifier.Name]; imported &&
				relative != immutableGenerationSourceMinter {
				violations = append(violations, fmt.Sprintf(
					"%s:%d cannot mint an immutable SQLite generation source",
					relative, fileSet.Position(selector.Pos()).Line,
				))
			}
			return true
		})
		return nil
	})
	sort.Strings(violations)
	return violations, err
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
