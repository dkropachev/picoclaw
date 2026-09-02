package database_test

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
)

const sqliteProviderDirectory = "internal/sqliteprovider"

var knownDatabaseArchitectureDebt = map[string]string{}

func TestSQLiteDriverAndOpenAreProviderOwned(t *testing.T) {
	t.Parallel()

	root := repositoryRoot(t)
	violations := make([]string, 0)
	seenDebt := make(map[string]struct{})
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		if entry.IsDir() {
			if relative != "." && shouldSkipArchitectureDirectory(relative, entry.Name()) {
				return filepath.SkipDir
			}
			return nil
		}
		if filepath.Ext(path) != ".go" || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		relativeSlash := filepath.ToSlash(relative)
		policy := databaseBoundaryPolicyFor(relativeSlash)
		source, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for _, violation := range databaseBoundaryViolationsWithPolicy(source, policy) {
			full := filepath.ToSlash(relative) + ": " + violation
			debtKey := filepath.ToSlash(relative) + ": " + architectureViolationMessage(violation)
			if _, allowed := knownDatabaseArchitectureDebt[debtKey]; allowed {
				seenDebt[debtKey] = struct{}{}
				continue
			}
			violations = append(violations, full)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(violations)
	staleDebt := make([]string, 0)
	for allowance := range knownDatabaseArchitectureDebt {
		if _, stillPresent := seenDebt[allowance]; !stillPresent {
			staleDebt = append(staleDebt, allowance)
		}
	}
	sort.Strings(staleDebt)
	if len(violations) != 0 || len(staleDebt) != 0 {
		var sections []string
		if len(violations) != 0 {
			sections = append(sections, "SQLite provider boundary violations:\n"+strings.Join(violations, "\n"))
		}
		if len(staleDebt) != 0 {
			sections = append(
				sections,
				"stale architecture debt allowances (remove them):\n"+strings.Join(staleDebt, "\n"),
			)
		}
		t.Fatal(strings.Join(sections, "\n"))
	}
}

func TestDatabaseBoundaryDetectorRejectsMutations(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		source string
	}{
		{
			name:   "driver import",
			source: `package bad; import _ "modernc.org/sqlite"`,
		},
		{
			name:   "aliased open",
			source: `package bad; import storage "database/sql"; func f(){ storage.Open("sqlite", "x") }`,
		},
		{
			name:   "open function value",
			source: `package bad; import storage "database/sql"; var open = storage.Open`,
		},
		{
			name:   "open db",
			source: `package bad; import "database/sql"; func f(c driver.Connector){ sql.OpenDB(c) }`,
		},
		{
			name:   "driver interface import",
			source: `package bad; import "database/sql/driver"; var _ driver.Driver`,
		},
		{
			name:   "provider control",
			source: `package bad; const q = "PRAGMA journal_mode = WAL"`,
		},
		{
			name: "aliased generation removal",
			source: `package bad
import disk "os"
func remove(dbPath string) { _ = disk.Remove(dbPath + "-wal") }`,
		},
		{
			name: "generation operation function value",
			source: `package bad
import disk "os"
var remove = disk.Remove
func cleanup(dbPath string) { _ = remove(dbPath + "-shm") }`,
		},
		{
			name: "literal generation inspection",
			source: `package bad
import ("os"; "path/filepath")
func inspect(root string) { _, _ = os.Lstat(filepath.Join(root, "state.db")) }`,
		},
		{
			name: "local generation alias",
			source: `package bad
import ("os"; "path/filepath")
func remove(root string) {
	path := filepath.Join(root, "state.db")
	_ = os.Remove(path)
}`,
		},
		{
			name:   "sqlite dsn control",
			source: `package bad; const dsn = "file:state.db?mode=rwc"`,
		},
		{
			name: "provider dsn escape",
			source: `package bad
import ("time"; provider "github.com/sipeed/picoclaw/internal/sqliteprovider")
func dsn(path string) { _, _ = provider.DSN(path, time.Second) }`,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if violations := databaseBoundaryViolations([]byte(test.source), false, false); len(violations) == 0 {
				t.Fatal("mutated source passed the provider boundary")
			}
		})
	}
	providerSource := []byte(`package sqliteprovider; import (
		"database/sql"; _ "modernc.org/sqlite"
	); func f(){ sql.Open("sqlite", "x") }`)
	if violations := databaseBoundaryViolations(providerSource, true, false); len(violations) != 0 {
		t.Fatalf("provider source rejected: %v", violations)
	}
}

func TestDatabaseBoundaryDetectorAvoidsProseAndUnrelatedFiles(t *testing.T) {
	t.Parallel()

	source := []byte(`package good
import ("os"; "path/filepath")
const documentation = "operators can inspect PRAGMA settings in provider diagnostics"
func database(root string) { path := filepath.Join(root, "state.db"); _ = path }
func remove(path string) { _ = os.Remove(path) }`)
	if violations := databaseBoundaryViolations(source, false, false); len(violations) != 0 {
		t.Fatalf("unrelated source rejected: %v", violations)
	}
}

type databaseBoundaryPolicy struct {
	providerOwned         bool
	bridgeOwned           bool
	generationOwned       bool
	providerIdentityOwned bool
}

func databaseBoundaryPolicyFor(relative string) databaseBoundaryPolicy {
	directory := filepath.ToSlash(filepath.Dir(relative))
	providerOwned := directory == sqliteProviderDirectory ||
		strings.HasPrefix(directory, sqliteProviderDirectory+"/")
	bridgeOwned := relative == "pkg/channels/matrix/matrix.go" ||
		relative == "pkg/channels/whatsapp_native/whatsapp_native.go" ||
		strings.HasPrefix(relative, "internal/sqlbridge/") ||
		strings.HasPrefix(relative, "internal/sqlitestore/")

	// These packages project or maintain physical generations on behalf of the
	// provider. They are deliberately narrower than a general database-package
	// exemption: ordinary domain stores must use a logical StoreID and broker
	// operations instead of inspecting files themselves.
	generationOwned := providerOwned || bridgeOwned ||
		strings.HasPrefix(relative, "pkg/database/migration/") ||
		strings.HasPrefix(relative, "pkg/database/artifacts/") ||
		relative == "pkg/database/catalog/readiness.go" ||
		relative == "internal/storecatalog/catalog.go" ||
		relative == "pkg/agent/file_mutation_policy.go" ||
		strings.HasSuffix(relative, "/database_migration.go") ||
		// The coverage harness manufactures an intentionally empty fixture; it
		// is not linked into an application or used against a live store.
		relative == "scripts/coverage_delta.go"

	maintenanceOwned := strings.HasPrefix(relative, "pkg/database/migration/") ||
		strings.HasSuffix(relative, "/database_migration.go")
	return databaseBoundaryPolicy{
		providerOwned:         providerOwned,
		bridgeOwned:           bridgeOwned,
		generationOwned:       generationOwned,
		providerIdentityOwned: providerOwned || bridgeOwned || maintenanceOwned,
	}
}

func databaseBoundaryViolations(source []byte, providerOwned, bridgeOwned bool) []string {
	return databaseBoundaryViolationsWithPolicy(source, databaseBoundaryPolicy{
		providerOwned:         providerOwned,
		bridgeOwned:           bridgeOwned,
		generationOwned:       providerOwned || bridgeOwned,
		providerIdentityOwned: providerOwned || bridgeOwned,
	})
}

func databaseBoundaryViolationsWithPolicy(source []byte, policy databaseBoundaryPolicy) []string {
	fileSet := token.NewFileSet()
	file, err := parser.ParseFile(fileSet, "source.go", source, parser.SkipObjectResolution)
	if err != nil {
		return []string{fmt.Sprintf("parse source: %v", err)}
	}
	sqlAliases := make(map[string]struct{})
	osAliases := make(map[string]struct{})
	filepathAliases := make(map[string]struct{})
	sqliteProviderAliases := make(map[string]struct{})
	violations := make([]string, 0)
	seenViolations := make(map[string]struct{})
	addViolation := func(node ast.Node, message string) {
		position := fileSet.Position(node.Pos())
		violation := fmt.Sprintf("line %d: %s", position.Line, message)
		if _, duplicate := seenViolations[violation]; duplicate {
			return
		}
		seenViolations[violation] = struct{}{}
		violations = append(violations, violation)
	}
	for _, declaration := range file.Imports {
		path, unquoteErr := strconv.Unquote(declaration.Path.Value)
		if unquoteErr != nil {
			violations = append(violations, "invalid import path")
			continue
		}
		if path == "modernc.org/sqlite" && !policy.providerOwned {
			addViolation(declaration, "SQLite driver import outside provider")
		}
		if path == "database/sql/driver" && !policy.providerOwned && !policy.bridgeOwned {
			addViolation(declaration, "database/sql/driver import outside private bridge")
		}
		name := filepath.Base(path)
		if declaration.Name != nil {
			name = declaration.Name.Name
		}
		switch path {
		case "database/sql":
			if name == "." {
				addViolation(declaration, "dot-imported database/sql")
			} else if name != "_" {
				sqlAliases[name] = struct{}{}
			}
		case "os":
			if name != "_" && name != "." {
				osAliases[name] = struct{}{}
			}
		case "path/filepath":
			if name != "_" && name != "." {
				filepathAliases[name] = struct{}{}
			}
		case "github.com/sipeed/picoclaw/internal/sqliteprovider":
			if name != "_" && name != "." {
				sqliteProviderAliases[name] = struct{}{}
			}
		}
	}
	ast.Inspect(file, func(node ast.Node) bool {
		literal, isLiteral := node.(*ast.BasicLit)
		if isLiteral && literal.Kind == token.STRING && !policy.providerOwned && !policy.bridgeOwned {
			value, unquoteErr := strconv.Unquote(literal.Value)
			if unquoteErr == nil && isSQLiteControlLiteral(value) {
				addViolation(literal, "SQLite provider control outside provider")
			}
		}
		selector, ok := node.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		identifier, ok := selector.X.(*ast.Ident)
		if !ok {
			return true
		}
		if _, imported := sqlAliases[identifier.Name]; imported &&
			(selector.Sel.Name == "Open" || selector.Sel.Name == "OpenDB") {
			allowedBridgeOpen := policy.bridgeOwned && selector.Sel.Name == "OpenDB"
			if !policy.providerOwned && !allowedBridgeOpen {
				addViolation(selector, "database/sql "+selector.Sel.Name+" outside provider")
			}
		}
		if _, imported := sqliteProviderAliases[identifier.Name]; imported &&
			(selector.Sel.Name == "DSN" || selector.Sel.Name == "DriverName") &&
			!policy.providerIdentityOwned {
			addViolation(selector, "raw SQLite provider "+selector.Sel.Name+" outside provider boundary")
		}
		return true
	})
	if !policy.generationOwned {
		globalNames := collectGlobalDatabaseGenerationNames(file, filepathAliases)
		globalOperations := collectGlobalFilesystemOperationAliases(file, osAliases, filepathAliases)
		for _, declaration := range file.Decls {
			names := cloneStringSet(globalNames)
			operations := cloneStringSet(globalOperations)
			if function, ok := declaration.(*ast.FuncDecl); ok {
				mergeStringSets(names, collectDatabaseGenerationNames(function.Body, filepathAliases, names))
				mergeStringSets(operations, collectFilesystemOperationAliases(
					function.Body, osAliases, filepathAliases, operations,
				))
			}
			ast.Inspect(declaration, func(node ast.Node) bool {
				call, ok := node.(*ast.CallExpr)
				if !ok {
					return true
				}
				if isDatabaseGenerationFileCall(
					call,
					osAliases,
					filepathAliases,
					names,
					operations,
				) {
					addViolation(call, "database generation file operation outside provider boundary")
				}
				return true
			})
		}
	}
	sort.Strings(violations)
	return violations
}

func architectureViolationMessage(violation string) string {
	if strings.HasPrefix(violation, "line ") {
		if separator := strings.Index(violation, ": "); separator >= 0 {
			return violation[separator+2:]
		}
	}
	return violation
}

func isSQLiteControlLiteral(value string) bool {
	trimmed := strings.TrimSpace(value)
	lower := strings.ToLower(trimmed)
	if strings.Contains(lower, "_pragma=") || isSQLiteFileDSN(lower) {
		return true
	}
	for {
		switch {
		case strings.HasPrefix(trimmed, "--"):
			newline := strings.IndexByte(trimmed, '\n')
			if newline < 0 {
				return false
			}
			trimmed = strings.TrimSpace(trimmed[newline+1:])
		case strings.HasPrefix(trimmed, "/*"):
			end := strings.Index(trimmed[2:], "*/")
			if end < 0 {
				return false
			}
			trimmed = strings.TrimSpace(trimmed[end+4:])
		default:
			end := 0
			for end < len(trimmed) && ((trimmed[end] >= 'A' && trimmed[end] <= 'Z') ||
				(trimmed[end] >= 'a' && trimmed[end] <= 'z') || trimmed[end] == '_') {
				end++
			}
			return strings.EqualFold(trimmed[:end], "PRAGMA") && end < len(trimmed)
		}
	}
}

func isSQLiteFileDSN(lower string) bool {
	if !strings.HasPrefix(lower, "file:") {
		return false
	}
	remainder := strings.TrimPrefix(lower, "file:")
	path, query, hasQuery := strings.Cut(remainder, "?")
	if hasQuery {
		for _, parameter := range strings.Split(query, "&") {
			key, _, _ := strings.Cut(parameter, "=")
			switch key {
			case "mode", "cache", "immutable", "vfs", "nolock", "psow", "_pragma":
				return true
			}
		}
	}
	return isDatabaseGenerationLiteral(path)
}

func collectGlobalDatabaseGenerationNames(file *ast.File, filepathAliases map[string]struct{}) map[string]struct{} {
	names := make(map[string]struct{})
	for changed := true; changed; {
		changed = false
		for _, declaration := range file.Decls {
			general, ok := declaration.(*ast.GenDecl)
			if !ok || (general.Tok != token.CONST && general.Tok != token.VAR) {
				continue
			}
			for _, rawSpec := range general.Specs {
				statement, ok := rawSpec.(*ast.ValueSpec)
				if !ok {
					continue
				}
				for index, name := range statement.Names {
					if index < len(statement.Values) &&
						isDatabaseGenerationExpression(statement.Values[index], filepathAliases, names) {
						if _, exists := names[name.Name]; !exists {
							names[name.Name] = struct{}{}
							changed = true
						}
					}
				}
			}
		}
	}
	return names
}

func collectDatabaseGenerationNames(
	node ast.Node,
	filepathAliases map[string]struct{},
	seed map[string]struct{},
) map[string]struct{} {
	names := cloneStringSet(seed)
	for changed := true; changed; {
		changed = false
		ast.Inspect(node, func(node ast.Node) bool {
			switch statement := node.(type) {
			case *ast.AssignStmt:
				for index, left := range statement.Lhs {
					if index >= len(statement.Rhs) ||
						!isDatabaseGenerationExpression(statement.Rhs[index], filepathAliases, names) {
						continue
					}
					identifier, ok := left.(*ast.Ident)
					if ok {
						if _, exists := names[identifier.Name]; !exists {
							names[identifier.Name] = struct{}{}
							changed = true
						}
					}
				}
			case *ast.ValueSpec:
				for index, name := range statement.Names {
					if index < len(statement.Values) &&
						isDatabaseGenerationExpression(statement.Values[index], filepathAliases, names) {
						if _, exists := names[name.Name]; !exists {
							names[name.Name] = struct{}{}
							changed = true
						}
					}
				}
			}
			return true
		})
	}
	return names
}

func cloneStringSet(source map[string]struct{}) map[string]struct{} {
	clone := make(map[string]struct{}, len(source))
	mergeStringSets(clone, source)
	return clone
}

func mergeStringSets(target, source map[string]struct{}) {
	for item := range source {
		target[item] = struct{}{}
	}
}

func isDatabaseGenerationFileCall(
	call *ast.CallExpr,
	osAliases map[string]struct{},
	filepathAliases map[string]struct{},
	generationNames map[string]struct{},
	operationAliases map[string]struct{},
) bool {
	relevant := false
	switch function := call.Fun.(type) {
	case *ast.SelectorExpr:
		identifier, ok := function.X.(*ast.Ident)
		if ok {
			relevant = isFilesystemOperation(identifier.Name, function.Sel.Name, osAliases, filepathAliases)
		}
	case *ast.Ident:
		_, relevant = operationAliases[function.Name]
	}
	if !relevant {
		return false
	}
	for _, argument := range call.Args {
		if isDatabaseGenerationExpression(argument, filepathAliases, generationNames) {
			return true
		}
	}
	return false
}

func collectGlobalFilesystemOperationAliases(
	file *ast.File,
	osAliases map[string]struct{},
	filepathAliases map[string]struct{},
) map[string]struct{} {
	aliases := make(map[string]struct{})
	for changed := true; changed; {
		changed = false
		for _, declaration := range file.Decls {
			general, ok := declaration.(*ast.GenDecl)
			if !ok || general.Tok != token.VAR {
				continue
			}
			for _, rawSpec := range general.Specs {
				spec, ok := rawSpec.(*ast.ValueSpec)
				if !ok {
					continue
				}
				for index, name := range spec.Names {
					if index < len(spec.Values) && isFilesystemOperationExpression(
						spec.Values[index], osAliases, filepathAliases, aliases,
					) {
						if _, exists := aliases[name.Name]; !exists {
							aliases[name.Name] = struct{}{}
							changed = true
						}
					}
				}
			}
		}
	}
	return aliases
}

func collectFilesystemOperationAliases(
	node ast.Node,
	osAliases map[string]struct{},
	filepathAliases map[string]struct{},
	seed map[string]struct{},
) map[string]struct{} {
	aliases := cloneStringSet(seed)
	for changed := true; changed; {
		changed = false
		ast.Inspect(node, func(node ast.Node) bool {
			var names []*ast.Ident
			var values []ast.Expr
			switch declaration := node.(type) {
			case *ast.AssignStmt:
				for _, rawName := range declaration.Lhs {
					name, _ := rawName.(*ast.Ident)
					names = append(names, name)
				}
				values = declaration.Rhs
			case *ast.ValueSpec:
				names = declaration.Names
				values = declaration.Values
			default:
				return true
			}
			for index, name := range names {
				if name == nil || index >= len(values) || !isFilesystemOperationExpression(
					values[index], osAliases, filepathAliases, aliases,
				) {
					continue
				}
				if _, exists := aliases[name.Name]; !exists {
					aliases[name.Name] = struct{}{}
					changed = true
				}
			}
			return true
		})
	}
	return aliases
}

func isFilesystemOperationExpression(
	expression ast.Expr,
	osAliases map[string]struct{},
	filepathAliases map[string]struct{},
	operationAliases map[string]struct{},
) bool {
	switch expression := expression.(type) {
	case *ast.SelectorExpr:
		identifier, ok := expression.X.(*ast.Ident)
		return ok && isFilesystemOperation(identifier.Name, expression.Sel.Name, osAliases, filepathAliases)
	case *ast.Ident:
		_, known := operationAliases[expression.Name]
		return known
	case *ast.ParenExpr:
		return isFilesystemOperationExpression(expression.X, osAliases, filepathAliases, operationAliases)
	default:
		return false
	}
}

func isFilesystemOperation(
	qualifier string,
	operation string,
	osAliases map[string]struct{},
	filepathAliases map[string]struct{},
) bool {
	if _, imported := osAliases[qualifier]; imported {
		switch operation {
		case "Open", "OpenFile", "Create", "ReadFile", "WriteFile", "Remove", "RemoveAll",
			"Rename", "Chmod", "Chown", "Lstat", "Stat", "Truncate", "Chtimes", "Readlink":
			return true
		}
	}
	if _, imported := filepathAliases[qualifier]; imported {
		return operation == "Glob" || operation == "Walk" || operation == "WalkDir" || operation == "EvalSymlinks"
	}
	return false
}

func isDatabaseGenerationExpression(
	expression ast.Expr,
	filepathAliases map[string]struct{},
	generationNames map[string]struct{},
) bool {
	switch expression := expression.(type) {
	case *ast.BasicLit:
		if expression.Kind != token.STRING {
			return false
		}
		value, err := strconv.Unquote(expression.Value)
		return err == nil && isDatabaseGenerationLiteral(value)
	case *ast.Ident:
		if _, known := generationNames[expression.Name]; known {
			return true
		}
		return isPhysicalDatabaseIdentifier(expression.Name)
	case *ast.SelectorExpr:
		return isPhysicalDatabaseIdentifier(expression.Sel.Name)
	case *ast.BinaryExpr:
		return isDatabaseGenerationExpression(expression.X, filepathAliases, generationNames) ||
			isDatabaseGenerationExpression(expression.Y, filepathAliases, generationNames)
	case *ast.ParenExpr:
		return isDatabaseGenerationExpression(expression.X, filepathAliases, generationNames)
	case *ast.IndexExpr:
		return isDatabaseGenerationExpression(expression.X, filepathAliases, generationNames)
	case *ast.CallExpr:
		if selector, ok := expression.Fun.(*ast.SelectorExpr); ok {
			if identifier, ok := selector.X.(*ast.Ident); ok {
				if _, imported := filepathAliases[identifier.Name]; imported && selector.Sel.Name == "Join" {
					for _, argument := range expression.Args {
						if isDatabaseGenerationExpression(argument, filepathAliases, generationNames) {
							return true
						}
					}
				}
			}
		}
	}
	return false
}

func isDatabaseGenerationLiteral(value string) bool {
	if value == "" || strings.ContainsAny(value, " \t\r\n") {
		return false
	}
	base := strings.ToLower(filepath.Base(filepath.Clean(value)))
	return strings.HasSuffix(base, ".db") || strings.HasSuffix(base, ".sqlite") ||
		strings.HasSuffix(base, ".sqlite3") || strings.HasSuffix(base, "-wal") ||
		strings.HasSuffix(base, "-journal") ||
		strings.HasSuffix(base, "-shm") || strings.HasSuffix(base, "-journal")
}

func shouldSkipArchitectureDirectory(relative, name string) bool {
	if strings.HasPrefix(name, ".") || name == "vendor" || name == "node_modules" {
		return true
	}
	return filepath.ToSlash(relative) == "scripts/testdata"
}

func repositoryRoot(t *testing.T) string {
	t.Helper()
	directory, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(directory, "go.mod")); err == nil {
			return directory
		}
		parent := filepath.Dir(directory)
		if parent == directory {
			t.Fatal("repository root not found")
		}
		directory = parent
	}
}
