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

const internalStoreCatalogImportPath = "github.com/sipeed/picoclaw/internal/storecatalog"

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
	allowedFiles := map[string]bool{"catalog.go": true, "snapshot.go": true}
	allowedImports := map[string]map[string]bool{
		"catalog.go": {
			"sort": true,
			"github.com/sipeed/picoclaw/internal/storecatalog": true,
			"github.com/sipeed/picoclaw/pkg/config":            true,
			"github.com/sipeed/picoclaw/pkg/database":          true,
		},
		"snapshot.go": {
			"github.com/sipeed/picoclaw/pkg/database": true,
		},
	}
	allowedExports := map[string]bool{
		"StoreID": true, "Options": true, "Entry": true, "Catalog": true, "New": true,
		"NewSnapshot": true, "Entries": true, "Lookup": true, "LookupChannel": true,
		"Contains": true,
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
	newSnapshotDeclarations := 0
	storeCatalogImports := 0
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
			if !allowedImports[entry.Name()][importPath] {
				violations = append(violations, fmt.Sprintf(
					"%s:%d imports unapproved package %s",
					entry.Name(), fileSet.Position(imported.Pos()).Line, importPath,
				))
			}
			if imported.Name != nil && imported.Name.Name == "." {
				violations = append(violations, fmt.Sprintf(
					"%s:%d dot-imports %s", entry.Name(), fileSet.Position(imported.Pos()).Line, importPath,
				))
			}
			if importPath == internalStoreCatalogImportPath {
				storeCatalogImports++
				if entry.Name() != "catalog.go" {
					violations = append(violations, fmt.Sprintf(
						"%s:%d imports the physical catalog outside catalog.go",
						entry.Name(), fileSet.Position(imported.Pos()).Line,
					))
				}
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
				if declaration.Name.Name == "NewSnapshot" {
					newSnapshotDeclarations++
					if entry.Name() != "snapshot.go" || !validNewSnapshotDeclaration(declaration) {
						violations = append(violations, entry.Name()+": NewSnapshot has an invalid surface")
					}
					projectCalls, fingerprintCalls, snapshotCalls := newSnapshotCallCounts(declaration)
					if projectCalls != 1 || fingerprintCalls != 1 || snapshotCalls != 1 {
						violations = append(violations, fmt.Sprintf(
							"%s: NewSnapshot calls project/Fingerprint/newCatalogSnapshot %d/%d/%d times",
							entry.Name(), projectCalls, fingerprintCalls, snapshotCalls,
						))
					}
				}
			}
		}
		ast.Inspect(parsed, func(node ast.Node) bool {
			selector, ok := node.(*ast.SelectorExpr)
			if ok {
				if logicalCatalogForbiddenSelector(selector.Sel.Name) {
					violations = append(violations, fmt.Sprintf(
						"%s:%d uses forbidden %s binding",
						entry.Name(), fileSet.Position(selector.Pos()).Line, selector.Sel.Name,
					))
				}
				switch selector.Sel.Name {
				case "Build", "Open", "OpenStore", "ProbeStatuses", "AcquireCatalogStoreClaims",
					"BrokerAuthorityHeld", "MigrationFenceHeld", "ProviderTestAuthorityHeld",
					"OnlineFenceHeld", "AcquireOnlineFence", "AcquireMigrationFence",
					"InheritedAuthorityEnvironment", "StartServer", "ReadManifest",
					"ServerOptions", "StoreReadiness", "StoreStatus", "PhysicalStoreClaims",
					"RequireBrokerReady", "ValidateStoreStatuses", "EnsureSupervisor",
					"MonitorSupervisor", "ConsumeSupervisorBootstrap", "ConnectInherited",
					"ConnectWithManifest", "InstallProcessClient", "DriverName", "DSN",
					"ProtectedRoots", "ProtectedRootsForDomains":
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
	if newSnapshotDeclarations != 1 {
		violations = append(violations, fmt.Sprintf(
			"NewSnapshot declaration count = %d, want 1", newSnapshotDeclarations,
		))
	}
	if storeCatalogImports != 1 {
		violations = append(violations, fmt.Sprintf(
			"internal store catalog import count = %d, want exact catalog.go import", storeCatalogImports,
		))
	}
	if len(violations) != 0 {
		sort.Strings(violations)
		t.Fatalf("logical catalog exposed provider or activation surface:\n%s", strings.Join(violations, "\n"))
	}
}

func logicalCatalogForbiddenSelector(name string) bool {
	return strings.Contains(name, "ConfigRevision") ||
		strings.Contains(name, "ConfigMutation") ||
		strings.HasPrefix(name, "LoadConfig") ||
		strings.HasPrefix(name, "LoadCurrentConfig") ||
		strings.HasPrefix(name, "SaveConfig")
}

func validNewSnapshotDeclaration(declaration *ast.FuncDecl) bool {
	if declaration == nil || declaration.Recv != nil || declaration.Type.TypeParams != nil ||
		declaration.Type.Params == nil || declaration.Type.Results == nil {
		return false
	}
	parameters := fieldListTypes(declaration.Type.Params)
	if len(parameters) != 2 || !identType(parameters[0], "Options") ||
		!identType(parameters[1], "string") {
		return false
	}
	results := fieldListTypes(declaration.Type.Results)
	if len(results) != 3 {
		return false
	}
	catalogPointer, ok := results[0].(*ast.StarExpr)
	return ok && identType(catalogPointer.X, "Catalog") && identType(results[1], "string") &&
		identType(results[2], "error")
}

func newSnapshotCallCounts(declaration *ast.FuncDecl) (project, fingerprint, snapshot int) {
	if declaration == nil {
		return 0, 0, 0
	}
	ast.Inspect(declaration.Body, func(node ast.Node) bool {
		call, ok := node.(*ast.CallExpr)
		if !ok {
			return true
		}
		switch function := call.Fun.(type) {
		case *ast.Ident:
			switch function.Name {
			case "project":
				project++
			case "newCatalogSnapshot":
				snapshot++
			}
		case *ast.SelectorExpr:
			if function.Sel.Name == "Fingerprint" {
				fingerprint++
			}
		}
		return true
	})
	return project, fingerprint, snapshot
}

func fieldListTypes(fields *ast.FieldList) []ast.Expr {
	if fields == nil {
		return nil
	}
	var result []ast.Expr
	for _, field := range fields.List {
		count := len(field.Names)
		if count == 0 {
			count = 1
		}
		for range count {
			result = append(result, field.Type)
		}
	}
	return result
}

func identType(expression ast.Expr, name string) bool {
	identifier, ok := expression.(*ast.Ident)
	return ok && identifier.Name == name
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
