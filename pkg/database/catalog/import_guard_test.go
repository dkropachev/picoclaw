package catalog

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

const logicalCatalogImportPath = "github.com/sipeed/picoclaw/pkg/database/catalog"

func TestLogicalCatalogHasNoProductionConsumers(t *testing.T) {
	t.Parallel()

	repositoryRoot := logicalCatalogRepositoryRoot(t)
	var violations []string
	err := filepath.WalkDir(repositoryRoot, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			if path != repositoryRoot && logicalCatalogGuardSkipsDir(entry.Name()) {
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
			if importPath == logicalCatalogImportPath {
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
		t.Fatalf("dormant logical catalog has production consumers:\n%s", strings.Join(violations, "\n"))
	}
}

func TestLogicalCatalogProductionSurfaceStaysProviderNeutral(t *testing.T) {
	t.Parallel()

	packageRoot := filepath.Join(logicalCatalogRepositoryRoot(t), "pkg", "database", "catalog")
	allowedFiles := map[string]bool{"catalog.go": true}
	allowedExports := map[string]bool{
		"StoreID": true, "Options": true, "Entry": true, "Catalog": true, "New": true,
		"Entries": true, "Lookup": true, "LookupChannel": true, "Contains": true,
	}
	forbiddenImports := map[string]bool{
		"crypto/sha256":       true,
		"database/sql":        true,
		"database/sql/driver": true,
		"encoding/hex":        true,
		"io/fs":               true,
		"net/url":             true,
		"os":                  true,
		"path/filepath":       true,
		"syscall":             true,
		"modernc.org/sqlite":  true,
		"github.com/sipeed/picoclaw/internal/sqliteprovider": true,
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
			violations = append(violations, entry.Name()+": production file outside logical facade")
		}
		fileSet := token.NewFileSet()
		parsed, err := parser.ParseFile(fileSet, path, nil, parser.AllErrors)
		if err != nil {
			return err
		}
		for _, imported := range parsed.Imports {
			importPath, err := strconv.Unquote(imported.Path.Value)
			if err != nil {
				return err
			}
			if forbiddenImports[importPath] || strings.Contains(importPath, "readiness") ||
				strings.Contains(importPath, "migration") {
				violations = append(violations, fmt.Sprintf(
					"%s:%d imports %s", entry.Name(), fileSet.Position(imported.Pos()).Line, importPath,
				))
			}
		}
		for _, declaration := range parsed.Decls {
			switch declaration := declaration.(type) {
			case *ast.GenDecl:
				for _, specification := range declaration.Specs {
					switch specification := specification.(type) {
					case *ast.TypeSpec:
						if ast.IsExported(specification.Name.Name) && !allowedExports[specification.Name.Name] {
							violations = append(violations, entry.Name()+": exports type "+specification.Name.Name)
						}
					case *ast.ValueSpec:
						for _, name := range specification.Names {
							if ast.IsExported(name.Name) && !allowedExports[name.Name] {
								violations = append(violations, entry.Name()+": exports value "+name.Name)
							}
						}
					}
				}
			case *ast.FuncDecl:
				if ast.IsExported(declaration.Name.Name) && !allowedExports[declaration.Name.Name] {
					violations = append(violations, entry.Name()+": exports function "+declaration.Name.Name)
				}
			}
		}
		ast.Inspect(parsed, func(node ast.Node) bool {
			selector, ok := node.(*ast.SelectorExpr)
			if ok {
				switch selector.Sel.Name {
				case "Build", "Open", "OpenStore", "ProbeStatuses", "AcquireCatalogStoreClaims",
					"BrokerAuthorityHeld", "MigrationFenceHeld", "ProviderTestAuthorityHeld",
					"OnlineFenceHeld", "AcquireOnlineFence", "AcquireMigrationFence",
					"InheritedAuthorityEnvironment", "StartServer", "ReadManifest",
					"StoreReadiness", "StoreStatus":
					violations = append(violations, fmt.Sprintf(
						"%s:%d uses forbidden %s binding",
						entry.Name(), fileSet.Position(selector.Pos()).Line, selector.Sel.Name,
					))
				}
			}
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatalf("inspect logical catalog surface: %v", err)
	}
	if len(violations) != 0 {
		sort.Strings(violations)
		t.Fatalf("logical catalog exposed provider or activation surface:\n%s", strings.Join(violations, "\n"))
	}
}

func logicalCatalogRepositoryRoot(t *testing.T) string {
	t.Helper()

	_, currentFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve logical catalog guard source path")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(currentFile), "..", "..", ".."))
}

func logicalCatalogGuardSkipsDir(name string) bool {
	switch strings.ToLower(name) {
	case ".git", ".cache", "cache", "generated", "gen", "node_modules", "testdata", "vendor":
		return true
	default:
		return false
	}
}
