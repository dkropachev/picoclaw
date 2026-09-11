package catalog

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

const logicalCatalogImportPath = "github.com/sipeed/picoclaw/pkg/database/catalog"

const internalStoreCatalogImportPath = "github.com/sipeed/picoclaw/internal/storecatalog"

const logicalCatalogProductionConsumer = "cmd/picoclaw/internal/database/command.go"

func TestLogicalCatalogHasOneExactProductionConsumer(t *testing.T) {
	t.Parallel()

	repositoryRoot := logicalCatalogRepositoryRoot(t)
	sources := make(map[string]string)
	err := filepath.WalkDir(repositoryRoot, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if path != repositoryRoot && entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("repository entry is a symlink: %s", path)
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
		raw, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("read production Go file %s: %w", filepath.ToSlash(relative), err)
		}
		sources[filepath.ToSlash(relative)] = string(raw)
		return nil
	})
	if err != nil {
		t.Fatalf("scan production imports: %v", err)
	}
	violations := logicalCatalogConsumerViolations(sources)
	if len(violations) != 0 {
		t.Fatalf("logical catalog consumer boundary violations:\n%s", strings.Join(violations, "\n"))
	}
}

func TestLogicalCatalogConsumerGuardRejectsEscapeFixtures(t *testing.T) {
	t.Parallel()

	valid := `package database
import dbcatalog "github.com/sipeed/picoclaw/pkg/database/catalog"
func NewDatabaseCommand() { _ = dbcatalog.NewSnapshot }
`
	fixtures := []struct {
		name     string
		sources  map[string]string
		contains string
	}{
		{
			name:     "missing consumer",
			sources:  map[string]string{"main.go": "package main\n"},
			contains: "exact consumer import count = 0, want 1",
		},
		{
			name: "additional consumer",
			sources: map[string]string{
				logicalCatalogProductionConsumer: valid,
				"pkg/escape/escape.go": `package escape
import dbcatalog "github.com/sipeed/picoclaw/pkg/database/catalog"
var _ = dbcatalog.NewSnapshot
`,
			},
			contains: "additional production consumer",
		},
		{
			name: "exported function value",
			sources: map[string]string{
				logicalCatalogProductionConsumer: `package database
import dbcatalog "github.com/sipeed/picoclaw/pkg/database/catalog"
var ExportedSnapshot = dbcatalog.NewSnapshot
`,
			},
			contains: "re-exports the logical catalog",
		},
		{
			name: "exported type alias",
			sources: map[string]string{
				logicalCatalogProductionConsumer: `package database
import dbcatalog "github.com/sipeed/picoclaw/pkg/database/catalog"
type ExportedCatalog = dbcatalog.Catalog
`,
			},
			contains: "re-exports the logical catalog",
		},
		{
			name: "dot import",
			sources: map[string]string{
				logicalCatalogProductionConsumer: `package database
import . "github.com/sipeed/picoclaw/pkg/database/catalog"
var _ = NewSnapshot
`,
			},
			contains: "alias = \".\", want \"dbcatalog\"",
		},
		{
			name: "linkname consumer",
			sources: map[string]string{
				logicalCatalogProductionConsumer: valid,
				"pkg/escape/escape.go": `package escape
import _ "unsafe"
//go:linkname snapshot github.com/sipeed/picoclaw/pkg/database/catalog.NewSnapshot
func snapshot()
`,
			},
			contains: "linknames the logical catalog",
		},
	}
	for _, fixture := range fixtures {
		t.Run(fixture.name, func(t *testing.T) {
			t.Parallel()
			violations := logicalCatalogConsumerViolations(fixture.sources)
			if !logicalCatalogViolationContains(violations, fixture.contains) {
				t.Fatalf("violations = %q, want one containing %q", violations, fixture.contains)
			}
		})
	}
}

func logicalCatalogConsumerViolations(sources map[string]string) []string {
	paths := make([]string, 0, len(sources))
	for path := range sources {
		paths = append(paths, path)
	}
	sort.Strings(paths)

	exactImports := 0
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
		for _, group := range parsed.Comments {
			for _, comment := range group.List {
				if strings.Contains(comment.Text, "go:linkname") &&
					strings.Contains(comment.Text, logicalCatalogImportPath+".") {
					violations = append(violations, fmt.Sprintf(
						"%s:%d linknames the logical catalog", path,
						fileSet.Position(comment.Pos()).Line,
					))
				}
			}
		}
		for _, imported := range parsed.Imports {
			importPath, err := strconv.Unquote(imported.Path.Value)
			if err != nil || importPath != logicalCatalogImportPath {
				continue
			}
			alias := "catalog"
			if imported.Name != nil {
				alias = imported.Name.Name
			}
			aliases[alias] = true
			if path != logicalCatalogProductionConsumer {
				violations = append(violations, fmt.Sprintf(
					"%s:%d is an additional production consumer", path,
					fileSet.Position(imported.Pos()).Line,
				))
				continue
			}
			exactImports++
			if alias != "dbcatalog" {
				violations = append(violations, fmt.Sprintf(
					"%s:%d logical catalog alias = %q, want %q", path,
					fileSet.Position(imported.Pos()).Line, alias, "dbcatalog",
				))
			}
		}
		if len(aliases) != 0 {
			violations = append(violations, logicalCatalogReexportViolations(path, parsed, aliases)...)
		}
	}
	if exactImports != 1 {
		violations = append(violations, fmt.Sprintf(
			"logical catalog exact consumer import count = %d, want 1", exactImports,
		))
	}
	sort.Strings(violations)
	return violations
}

func logicalCatalogReexportViolations(
	path string,
	parsed *ast.File,
	aliases map[string]bool,
) []string {
	var violations []string
	for _, declaration := range parsed.Decls {
		if logicalCatalogDeclarationReexports(declaration, aliases) {
			violations = append(violations, path+": re-exports the logical catalog")
		}
	}
	return violations
}

func logicalCatalogDeclarationReexports(declaration ast.Decl, aliases map[string]bool) bool {
	switch declaration := declaration.(type) {
	case *ast.FuncDecl:
		if !ast.IsExported(declaration.Name.Name) {
			return false
		}
		return logicalCatalogExpressionUsesAlias(declaration.Type, aliases) ||
			(declaration.Recv != nil && logicalCatalogExpressionUsesAlias(declaration.Recv, aliases))
	case *ast.GenDecl:
		for _, specification := range declaration.Specs {
			switch specification := specification.(type) {
			case *ast.TypeSpec:
				if ast.IsExported(specification.Name.Name) &&
					logicalCatalogExpressionUsesAlias(specification, aliases) {
					return true
				}
			case *ast.ValueSpec:
				if logicalCatalogExportedValueUsesAlias(specification, aliases) {
					return true
				}
			}
		}
	}
	return false
}

func logicalCatalogExportedValueUsesAlias(
	specification *ast.ValueSpec,
	aliases map[string]bool,
) bool {
	exported := false
	for _, name := range specification.Names {
		exported = exported || ast.IsExported(name.Name)
	}
	return exported && logicalCatalogExpressionUsesAlias(specification, aliases)
}

func logicalCatalogExpressionUsesAlias(node ast.Node, aliases map[string]bool) bool {
	uses := false
	ast.Inspect(node, func(child ast.Node) bool {
		selector, ok := child.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		qualifier, ok := selector.X.(*ast.Ident)
		if ok && aliases[qualifier.Name] {
			uses = true
			return false
		}
		return true
	})
	return uses
}

func logicalCatalogViolationContains(violations []string, fragment string) bool {
	for _, violation := range violations {
		if strings.Contains(violation, fragment) {
			return true
		}
	}
	return false
}

func TestLogicalCatalogProductionSurfaceStaysProviderNeutral(t *testing.T) {
	t.Parallel()

	packageRoot := filepath.Join(logicalCatalogRepositoryRoot(t), "pkg", "database", "catalog")
	allowedFiles := map[string]bool{
		"catalog.go": true, "review_scope.go": true, "snapshot.go": true,
	}
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
		"review_scope.go": {
			"github.com/sipeed/picoclaw/internal/storecatalog": true,
			"github.com/sipeed/picoclaw/pkg/database":          true,
		},
	}
	allowedExports := map[string]bool{
		"StoreID": true, "Options": true, "Entry": true, "Catalog": true, "New": true,
		"NewSnapshot": true, "Entries": true, "Lookup": true, "LookupChannel": true,
		"Contains": true, "RequiredStores": true, "Bindings": true,
		"NewReviewSnapshot": true,
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
	newReviewSnapshotDeclarations := 0
	requiredStoresDeclarations := 0
	bindingsDeclarations := 0
	storeCatalogImports := make(map[string]int)
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
				storeCatalogImports[entry.Name()]++
				if entry.Name() != "catalog.go" && entry.Name() != "review_scope.go" {
					violations = append(violations, fmt.Sprintf(
						"%s:%d imports the physical catalog outside reviewed facade files",
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
				if declaration.Name.Name == "NewReviewSnapshot" {
					newReviewSnapshotDeclarations++
					if entry.Name() != "review_scope.go" || !validNewSnapshotDeclaration(declaration) {
						violations = append(violations, entry.Name()+": NewReviewSnapshot has an invalid surface")
					}
					newScopeCalls, bindingsCalls, fingerprintCalls, snapshotCalls := newReviewSnapshotCallCounts(
						declaration,
					)
					if newScopeCalls != 1 || bindingsCalls != 1 || fingerprintCalls != 1 || snapshotCalls != 1 {
						violations = append(violations, fmt.Sprintf(
							"%s: NewReviewSnapshot calls NewReviewScope/Bindings/Fingerprint/newReviewCatalogSnapshot %d/%d/%d/%d times",
							entry.Name(),
							newScopeCalls,
							bindingsCalls,
							fingerprintCalls,
							snapshotCalls,
						))
					}
				}
				if declaration.Name.Name == "RequiredStores" {
					requiredStoresDeclarations++
					if entry.Name() != "catalog.go" || !validRequiredStoresDeclaration(declaration) {
						violations = append(violations, entry.Name()+": RequiredStores has an invalid surface")
					}
				}
				if declaration.Name.Name == "Bindings" {
					bindingsDeclarations++
					if entry.Name() != "catalog.go" || !validBindingsDeclaration(declaration) {
						violations = append(violations, entry.Name()+": Bindings has an invalid surface")
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
					"ConnectWithManifest", "InstallProcessClient", "BrokerStatus", "Handler",
					"StatusProvider", "Manifest", "ControlOperationStatus", "DriverName", "DSN",
					"ProtectedRoots", "ProtectedRootsForDomains", "ReviewScopeCatalogs",
					"FullFingerprint":
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
	if newReviewSnapshotDeclarations != 1 {
		violations = append(violations, fmt.Sprintf(
			"NewReviewSnapshot declaration count = %d, want 1", newReviewSnapshotDeclarations,
		))
	}
	if requiredStoresDeclarations != 1 {
		violations = append(violations, fmt.Sprintf(
			"RequiredStores declaration count = %d, want 1", requiredStoresDeclarations,
		))
	}
	if bindingsDeclarations != 1 {
		violations = append(violations, fmt.Sprintf(
			"Bindings declaration count = %d, want 1", bindingsDeclarations,
		))
	}
	if storeCatalogImports["catalog.go"] != 1 || storeCatalogImports["review_scope.go"] != 1 ||
		len(storeCatalogImports) != 2 {
		violations = append(violations, fmt.Sprintf(
			"internal store catalog imports = %#v, want exact catalog.go/review_scope.go imports",
			storeCatalogImports,
		))
	}
	if len(violations) != 0 {
		sort.Strings(violations)
		t.Fatalf("logical catalog exposed provider or activation surface:\n%s", strings.Join(violations, "\n"))
	}
}

func validBindingsDeclaration(declaration *ast.FuncDecl) bool {
	if declaration == nil || declaration.Type.TypeParams != nil || declaration.Recv == nil ||
		declaration.Type.Params == nil || declaration.Type.Results == nil ||
		len(fieldListTypes(declaration.Type.Params)) != 0 {
		return false
	}
	receivers := fieldListTypes(declaration.Recv)
	if len(receivers) != 1 {
		return false
	}
	receiver, ok := receivers[0].(*ast.StarExpr)
	if !ok || !identType(receiver.X, "Catalog") {
		return false
	}
	results := fieldListTypes(declaration.Type.Results)
	if len(results) != 1 {
		return false
	}
	slice, ok := results[0].(*ast.ArrayType)
	return ok && slice.Len == nil && qualifiedIdentType(slice.Elt, "database", "StoreBinding")
}

func qualifiedIdentType(expression ast.Expr, qualifier, name string) bool {
	selector, ok := expression.(*ast.SelectorExpr)
	if !ok || selector.Sel.Name != name {
		return false
	}
	prefix, ok := selector.X.(*ast.Ident)
	return ok && prefix.Name == qualifier
}

func validRequiredStoresDeclaration(declaration *ast.FuncDecl) bool {
	if declaration == nil || declaration.Type.TypeParams != nil || declaration.Recv == nil ||
		declaration.Type.Params == nil || declaration.Type.Results == nil ||
		len(fieldListTypes(declaration.Type.Params)) != 0 {
		return false
	}
	receivers := fieldListTypes(declaration.Recv)
	if len(receivers) != 1 {
		return false
	}
	receiver, ok := receivers[0].(*ast.StarExpr)
	if !ok || !identType(receiver.X, "Catalog") {
		return false
	}
	results := fieldListTypes(declaration.Type.Results)
	if len(results) != 1 {
		return false
	}
	slice, ok := results[0].(*ast.ArrayType)
	return ok && slice.Len == nil && identType(slice.Elt, "StoreID")
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

func newReviewSnapshotCallCounts(
	declaration *ast.FuncDecl,
) (newScope, bindings, fingerprint, snapshot int) {
	if declaration == nil {
		return 0, 0, 0, 0
	}
	ast.Inspect(declaration.Body, func(node ast.Node) bool {
		call, ok := node.(*ast.CallExpr)
		if !ok {
			return true
		}
		switch function := call.Fun.(type) {
		case *ast.Ident:
			if function.Name == "newReviewCatalogSnapshot" {
				snapshot++
			}
		case *ast.SelectorExpr:
			switch function.Sel.Name {
			case "NewReviewScope":
				newScope++
			case "Bindings":
				bindings++
			case "Fingerprint":
				fingerprint++
			}
		}
		return true
	})
	return newScope, bindings, fingerprint, snapshot
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
	switch name {
	case ".git", ".cache", "node_modules", "testdata", "vendor":
		return true
	default:
		return false
	}
}

func TestLogicalCatalogGuardSkipsOnlyExactNonProductionDirectories(t *testing.T) {
	t.Parallel()
	for _, name := range []string{".git", ".cache", "node_modules", "testdata", "vendor"} {
		if !logicalCatalogGuardSkipsDir(name) {
			t.Errorf("guard did not skip exact non-production directory %q", name)
		}
	}
	for _, name := range []string{
		".Cache", "Node_Modules", "TestData", "Vendor", "cache", "gen", "generated",
	} {
		if logicalCatalogGuardSkipsDir(name) {
			t.Errorf("guard skipped buildable lookalike directory %q", name)
		}
	}
}
