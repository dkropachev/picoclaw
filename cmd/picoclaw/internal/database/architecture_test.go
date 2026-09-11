package database

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"testing"
)

const commandDatabaseImportPath = "github.com/sipeed/picoclaw/pkg/database"

const commandLogicalCatalogImportPath = "github.com/sipeed/picoclaw/pkg/database/catalog"

type commandExpectedImport struct {
	path  string
	alias string
}

var commandExpectedImports = []commandExpectedImport{
	{path: "context"},
	{path: "errors"},
	{path: "os"},
	{path: "os/signal"},
	{path: "path/filepath"},
	{path: "strconv"},
	{path: "strings"},
	{path: "syscall"},
	{path: "time"},
	{path: "github.com/spf13/cobra"},
	{path: "github.com/sipeed/picoclaw/cmd/picoclaw/internal"},
	{path: "github.com/sipeed/picoclaw/pkg/config"},
	{path: commandDatabaseImportPath, alias: "dblayer"},
	{path: commandLogicalCatalogImportPath, alias: "dbcatalog"},
}

var commandRequiredBindings = map[string]int{
	"config.ConfigRevision":                   1,
	"config.LoadCurrentConfigSnapshot":        1,
	"config.WithConfigMutationLock":           1,
	"dbcatalog.NewReviewSnapshot":             1,
	"dblayer.CanonicalHome":                   1,
	"dblayer.ConsumeSupervisorBootstrapGuard": 1,
	"dblayer.NewHandlerRegistry":              1,
	"dblayer.StartServer":                     1,
}

var commandAllowedDatabaseBindings = map[string]bool{
	"CanonicalHome": true, "CodeConflict": true, "CodeDeadline": true, "CodeIntegrity": true,
	"CodeInternal": true, "CodeInvalid": true, "CodeMigrationRequired": true, "CodeOf": true,
	"CodeUnavailable":                 true,
	"ConsumeSupervisorBootstrapGuard": true, "Handler": true, "NewError": true,
	"NewHandlerRegistry": true, "ServerOptions": true, "StartServer": true,
	"StoreBinding": true, "StoreID": true,
	"StoreStatus": true, "StoreUnavailable": true,
}

var commandAllowedCatalogBindings = map[string]bool{
	"Entry": true, "NewReviewSnapshot": true, "Options": true,
}

var commandAllowedConfigBindings = map[string]bool{
	"Config": true, "ConfigRevision": true, "ErrConfigMigrationRequired": true,
	"LoadCurrentConfigSnapshot": true, "WithConfigMutationLock": true,
}

var commandAllowedContextBindings = map[string]bool{
	"Background": true, "CancelFunc": true, "Context": true,
}

var commandAllowedErrorsBindings = map[string]bool{
	"Is": true, "Join": true,
}

var commandAllowedOSBindings = map[string]bool{
	"Interrupt": true, "Signal": true, "UserHomeDir": true,
}

var commandAllowedSignalBindings = map[string]bool{
	"NotifyContext": true,
}

var commandAllowedFilepathBindings = map[string]bool{
	"Clean": true, "IsAbs": true,
}

var commandAllowedStrconvBindings = map[string]bool{
	"FormatInt": true, "ParseInt": true,
}

var commandAllowedStringsBindings = map[string]bool{
	"ContainsRune": true, "TrimSpace": true,
}

var commandAllowedSyscallBindings = map[string]bool{
	"SIGTERM": true,
}

var commandAllowedTimeBindings = map[string]bool{
	"Now": true, "Time": true, "Unix": true,
}

var commandAllowedCobraBindings = map[string]bool{
	"Command": true, "NoArgs": true,
}

var commandAllowedInternalBindings = map[string]bool{
	"GetConfigPath": true, "GetPicoclawHome": true,
}

func TestDatabaseCommandProductionArchitecture(t *testing.T) {
	t.Parallel()

	sources, err := commandProductionSources(commandPackageRoot(t))
	if err != nil {
		t.Fatalf("read database command production sources: %v", err)
	}
	violations := commandArchitectureViolations(sources)
	if len(violations) != 0 {
		t.Fatalf("database command architecture violations:\n%s", strings.Join(violations, "\n"))
	}
}

func TestDatabaseCommandArchitectureRejectsEscapeFixtures(t *testing.T) {
	t.Parallel()

	fixtures := []struct {
		name     string
		source   string
		contains string
	}{
		{
			name: "aliased auto start function value",
			source: `package database
import broker "github.com/sipeed/picoclaw/pkg/database"
var start = broker.EnsureSupervisor
`,
			contains: "forbidden EnsureSupervisor binding",
		},
		{
			name: "dot imported direct auto start",
			source: `package database
import . "github.com/sipeed/picoclaw/pkg/database"
var _ = MonitorSupervisor
`,
			contains: "dot-imports",
		},
		{
			name: "boolean bootstrap compatibility",
			source: `package database
import db "github.com/sipeed/picoclaw/pkg/database"
var consume = db.ConsumeSupervisorBootstrap
`,
			contains: "forbidden ConsumeSupervisorBootstrap binding",
		},
		{
			name: "registry registration alias",
			source: `package database
func retain(registry interface{ Register(string, any) error }) { use := registry.Register; _ = use }
`,
			contains: "forbidden Register binding",
		},
		{
			name: "raw sql import",
			source: `package database
import sql "database/sql"
var open = sql.Open
`,
			contains: "forbidden storage import database/sql",
		},
		{
			name: "raw os open",
			source: `package database
import "os"
var open = os.OpenFile
`,
			contains: "forbidden OpenFile binding",
		},
		{
			name: "second config loader",
			source: `package database
import "github.com/sipeed/picoclaw/cmd/picoclaw/internal"
var load = internal.LoadConfig
`,
			contains: "unapproved internal.LoadConfig binding",
		},
	}
	for _, fixture := range fixtures {
		t.Run(fixture.name, func(t *testing.T) {
			t.Parallel()
			violations := commandSourceViolations("command.go", fixture.source, false)
			if !containsCommandViolation(violations, fixture.contains) {
				t.Fatalf("violations = %q, want one containing %q", violations, fixture.contains)
			}
		})
	}

	violations := commandArchitectureViolations(map[string]string{
		"command.go": "package database\n",
		"escape.go":  "package database\n",
	})
	if !containsCommandViolation(violations, "production files") {
		t.Fatalf("additional-file violations = %q, want production inventory failure", violations)
	}
}

func commandArchitectureViolations(sources map[string]string) []string {
	var violations []string
	if len(sources) != 1 || sources["command.go"] == "" {
		names := make([]string, 0, len(sources))
		for name := range sources {
			names = append(names, name)
		}
		sort.Strings(names)
		violations = append(violations, fmt.Sprintf(
			"production files = %q, want exact [command.go]", names,
		))
	}
	for name, source := range sources {
		violations = append(violations, commandSourceViolations(name, source, name == "command.go")...)
	}
	sort.Strings(violations)
	return violations
}

func commandSourceViolations(name, source string, requireExactShape bool) []string {
	fileSet := token.NewFileSet()
	parsed, err := parser.ParseFile(fileSet, name, source, parser.AllErrors)
	if err != nil {
		return []string{fmt.Sprintf("%s: parse production source: %v", name, err)}
	}
	violations := commandForbiddenViolations(name, fileSet, parsed)
	if !requireExactShape {
		return violations
	}
	violations = append(violations, commandImportViolations(name, fileSet, parsed)...)
	violations = append(violations, commandExportViolations(name, parsed)...)
	violations = append(violations, commandBindingViolations(name, parsed)...)
	violations = append(violations, commandTreeViolations(name, parsed)...)
	violations = append(violations, commandServerOptionsViolations(name, parsed)...)
	return violations
}

func commandImportViolations(name string, fileSet *token.FileSet, parsed *ast.File) []string {
	expected := make(map[string]string, len(commandExpectedImports))
	for _, imported := range commandExpectedImports {
		expected[imported.path] = imported.alias
	}
	seen := make(map[string]int, len(parsed.Imports))
	var violations []string
	for _, imported := range parsed.Imports {
		path, err := strconv.Unquote(imported.Path.Value)
		if err != nil {
			violations = append(violations, name+": malformed import path")
			continue
		}
		seen[path]++
		wantAlias, allowed := expected[path]
		gotAlias := ""
		if imported.Name != nil {
			gotAlias = imported.Name.Name
		}
		if !allowed {
			violations = append(violations, fmt.Sprintf(
				"%s:%d imports unapproved package %s", name,
				fileSet.Position(imported.Pos()).Line, path,
			))
			continue
		}
		if gotAlias != wantAlias {
			violations = append(violations, fmt.Sprintf(
				"%s:%d import %s alias = %q, want %q", name,
				fileSet.Position(imported.Pos()).Line, path, gotAlias, wantAlias,
			))
		}
	}
	for path := range expected {
		if seen[path] != 1 {
			violations = append(violations, fmt.Sprintf(
				"%s: import %s count = %d, want 1", name, path, seen[path],
			))
		}
	}
	return violations
}

func commandExportViolations(name string, parsed *ast.File) []string {
	newCommandCount := 0
	var violations []string
	for _, declaration := range parsed.Decls {
		switch declaration := declaration.(type) {
		case *ast.FuncDecl:
			if !ast.IsExported(declaration.Name.Name) {
				continue
			}
			if declaration.Name.Name != "NewDatabaseCommand" {
				violations = append(violations, name+": exports function "+declaration.Name.Name)
				continue
			}
			newCommandCount++
			if !validNewDatabaseCommand(declaration) {
				violations = append(violations, name+": NewDatabaseCommand has an invalid surface")
			}
		case *ast.GenDecl:
			violations = append(violations, commandExportedGenDeclViolations(name, declaration)...)
		}
	}
	if newCommandCount != 1 {
		violations = append(violations, fmt.Sprintf(
			"%s: NewDatabaseCommand declaration count = %d, want 1", name, newCommandCount,
		))
	}
	return violations
}

func commandExportedGenDeclViolations(name string, declaration *ast.GenDecl) []string {
	var violations []string
	for _, specification := range declaration.Specs {
		switch specification := specification.(type) {
		case *ast.TypeSpec:
			if ast.IsExported(specification.Name.Name) {
				violations = append(violations, name+": exports type "+specification.Name.Name)
			}
		case *ast.ValueSpec:
			for _, valueName := range specification.Names {
				if ast.IsExported(valueName.Name) {
					violations = append(violations, name+": exports value "+valueName.Name)
				}
			}
		}
	}
	return violations
}

func validNewDatabaseCommand(declaration *ast.FuncDecl) bool {
	if declaration == nil || declaration.Recv != nil || declaration.Type.TypeParams != nil ||
		declaration.Type.Params == nil || len(declaration.Type.Params.List) != 0 ||
		declaration.Type.Results == nil || len(declaration.Type.Results.List) != 1 ||
		len(declaration.Type.Results.List[0].Names) != 0 {
		return false
	}
	pointer, ok := declaration.Type.Results.List[0].Type.(*ast.StarExpr)
	if !ok {
		return false
	}
	selector, ok := pointer.X.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	qualifier, qualifierOK := selector.X.(*ast.Ident)
	return qualifierOK && qualifier.Name == "cobra" && selector.Sel.Name == "Command"
}

func commandBindingViolations(name string, parsed *ast.File) []string {
	references := make(map[string]int)
	calls := make(map[string]int)
	selectorCalls := make(map[token.Pos]bool)
	ast.Inspect(parsed, func(node ast.Node) bool {
		call, ok := node.(*ast.CallExpr)
		if !ok {
			return true
		}
		if selector, ok := call.Fun.(*ast.SelectorExpr); ok {
			selectorCalls[selector.Pos()] = true
		}
		return true
	})
	ast.Inspect(parsed, func(node ast.Node) bool {
		selector, ok := node.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		qualifier, ok := selector.X.(*ast.Ident)
		if !ok {
			return true
		}
		key := qualifier.Name + "." + selector.Sel.Name
		references[key]++
		if selectorCalls[selector.Pos()] {
			calls[key]++
		}
		return true
	})
	var violations []string
	for binding, want := range commandRequiredBindings {
		if references[binding] != want {
			violations = append(violations, fmt.Sprintf(
				"%s: %s reference count = %d, want %d", name, binding, references[binding], want,
			))
		}
	}
	for _, binding := range []string{
		"dblayer.ConsumeSupervisorBootstrapGuard", "dbcatalog.NewReviewSnapshot",
		"dblayer.NewHandlerRegistry", "dblayer.StartServer",
	} {
		if calls[binding] != 1 {
			violations = append(violations, fmt.Sprintf(
				"%s: %s direct call count = %d, want 1", name, binding, calls[binding],
			))
		}
	}
	if references["dblayer.NewError"] == 0 ||
		references["dblayer.NewError"] != calls["dblayer.NewError"] {
		violations = append(violations, fmt.Sprintf(
			"%s: dblayer.NewError references/direct calls = %d/%d, want equal nonzero counts",
			name, references["dblayer.NewError"], calls["dblayer.NewError"],
		))
	}
	for binding, want := range map[string]int{
		"ops.canonicalHome":      1,
		"ops.configRevision":     1,
		"ops.consumeGuard":       1,
		"ops.loadConfigSnapshot": 1,
		"ops.newRegistry":        1,
		"ops.newReviewSnapshot":  1,
		"ops.startServer":        1,
		"ops.withConfigLock":     1,
	} {
		if calls[binding] != want {
			violations = append(violations, fmt.Sprintf(
				"%s: %s direct call count = %d, want %d", name, binding, calls[binding], want,
			))
		}
	}
	for _, method := range []struct {
		name string
		want int
	}{
		{name: "AddCommand", want: 1},
		{name: "Bindings", want: 1},
		{name: "Entries", want: 1},
		{name: "RequiredStores", want: 1},
		{name: "Validate", want: 2},
		{name: "validate", want: 3},
	} {
		got := countSelectorCalls(parsed, method.name)
		if got != method.want {
			violations = append(violations, fmt.Sprintf(
				"%s: %s call count = %d, want %d", name, method.name,
				got, method.want,
			))
		}
	}
	return violations
}

func commandForbiddenViolations(
	name string,
	fileSet *token.FileSet,
	parsed *ast.File,
) []string {
	var violations []string
	for _, imported := range parsed.Imports {
		path, err := strconv.Unquote(imported.Path.Value)
		if err != nil {
			continue
		}
		if commandForbiddenStorageImport(path) {
			violations = append(violations, fmt.Sprintf(
				"%s:%d forbidden storage import %s", name,
				fileSet.Position(imported.Pos()).Line, path,
			))
		}
		if path == commandDatabaseImportPath && imported.Name != nil && imported.Name.Name == "." {
			violations = append(violations, fmt.Sprintf(
				"%s:%d dot-imports %s", name, fileSet.Position(imported.Pos()).Line, path,
			))
		}
	}
	ast.Inspect(parsed, func(node ast.Node) bool {
		selector, ok := node.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		if commandForbiddenBinding(selector.Sel.Name) {
			violations = append(violations, fmt.Sprintf(
				"%s:%d forbidden %s binding", name,
				fileSet.Position(selector.Sel.Pos()).Line, selector.Sel.Name,
			))
		}
		qualifier, qualifierOK := selector.X.(*ast.Ident)
		if qualifierOK && !commandBindingAllowed(qualifier.Name, selector.Sel.Name) {
			violations = append(violations, fmt.Sprintf(
				"%s:%d unapproved %s.%s binding", name,
				fileSet.Position(selector.Sel.Pos()).Line, qualifier.Name, selector.Sel.Name,
			))
		}
		return true
	})
	return violations
}

func commandBindingAllowed(qualifier, name string) bool {
	switch qualifier {
	case "context":
		return commandAllowedContextBindings[name]
	case "errors":
		return commandAllowedErrorsBindings[name]
	case "os":
		return commandAllowedOSBindings[name]
	case "signal":
		return commandAllowedSignalBindings[name]
	case "filepath":
		return commandAllowedFilepathBindings[name]
	case "strconv":
		return commandAllowedStrconvBindings[name]
	case "strings":
		return commandAllowedStringsBindings[name]
	case "syscall":
		return commandAllowedSyscallBindings[name]
	case "time":
		return commandAllowedTimeBindings[name]
	case "cobra":
		return commandAllowedCobraBindings[name]
	case "internal":
		return commandAllowedInternalBindings[name]
	case "dblayer":
		return commandAllowedDatabaseBindings[name]
	case "dbcatalog":
		return commandAllowedCatalogBindings[name]
	case "config":
		return commandAllowedConfigBindings[name]
	default:
		return true
	}
}

func commandForbiddenStorageImport(path string) bool {
	lower := strings.ToLower(path)
	return path == "database/sql" || path == "database/sql/driver" ||
		path == "github.com/sipeed/picoclaw/internal/storecatalog" ||
		strings.Contains(lower, "claims") || strings.Contains(lower, "sqlite") ||
		strings.Contains(lower, "readiness") || strings.Contains(lower, "migration") ||
		strings.Contains(lower, "provider")
}

func commandForbiddenBinding(name string) bool {
	switch name {
	case "Acquire", "AcquireCatalogStoreClaims", "AcquireMigrationFence", "AcquireOnlineFence",
		"AcquireProjected", "Build", "ConsumeSupervisorBootstrap", "EnsureSupervisor",
		"Create", "InstallProcessClient", "MonitorSupervisor", "Open", "OpenDB", "OpenFile", "OpenStore",
		"ProbeStatuses", "Register", "RequireBrokerReady", "RunMigration",
		"ReadFile", "ValidateStoreStatuses":
		return true
	default:
		return strings.Contains(strings.ToLower(name), "sqlite")
	}
}

func commandTreeViolations(name string, parsed *ast.File) []string {
	literals := make(map[string]map[string]ast.Expr)
	duplicates := make(map[string][]string)
	ast.Inspect(parsed, func(node ast.Node) bool {
		literal, ok := node.(*ast.CompositeLit)
		if !ok || !isCobraCommandType(literal.Type) {
			return true
		}
		fields := make(map[string]ast.Expr)
		use := ""
		for _, element := range literal.Elts {
			keyValue, ok := element.(*ast.KeyValueExpr)
			if !ok {
				continue
			}
			key, ok := keyValue.Key.(*ast.Ident)
			if !ok {
				continue
			}
			fields[key.Name] = keyValue.Value
			if key.Name == "Use" {
				use, _ = stringLiteral(keyValue.Value)
			}
		}
		if _, exists := literals[use]; exists {
			duplicates[use] = append(duplicates[use], "duplicate")
		}
		literals[use] = fields
		return true
	})
	var violations []string
	if len(literals) != 2 || len(duplicates) != 0 {
		violations = append(violations, fmt.Sprintf(
			"%s: cobra command literals = %d with %d duplicate keys, want exact database/__serve pair",
			name, len(literals), len(duplicates),
		))
	}
	violations = append(violations, exactCommandFields(
		name, "database", literals["database"], map[string]string{
			"Use": "database", "Hidden": "true", "Args": "cobra.NoArgs",
		},
	)...)
	violations = append(violations, exactCommandFields(
		name, "__serve", literals["__serve"], map[string]string{
			"Use": "__serve", "Hidden": "true", "DisableFlagParsing": "true", "RunE": "func",
		},
	)...)
	return violations
}

func commandServerOptionsViolations(name string, parsed *ast.File) []string {
	want := map[string]bool{
		"Home": true, "CatalogFingerprint": true, "RequiredStores": true,
		"ServedStores": true, "StatusProvider": true, "Handler": true,
		"StartupGuard": true, "CloseHandler": true,
	}
	count := 0
	var violations []string
	ast.Inspect(parsed, func(node ast.Node) bool {
		literal, ok := node.(*ast.CompositeLit)
		if !ok || !qualifiedType(literal.Type, "dblayer", "ServerOptions") {
			return true
		}
		count++
		seen := make(map[string]int)
		for _, element := range literal.Elts {
			keyValue, ok := element.(*ast.KeyValueExpr)
			if !ok {
				violations = append(violations, name+": ServerOptions uses an unkeyed field")
				continue
			}
			key, ok := keyValue.Key.(*ast.Ident)
			if !ok {
				continue
			}
			seen[key.Name]++
			if !want[key.Name] {
				violations = append(violations, name+": ServerOptions sets unapproved field "+key.Name)
			}
		}
		for field := range want {
			if seen[field] != 1 {
				violations = append(violations, fmt.Sprintf(
					"%s: ServerOptions field %s count = %d, want 1", name, field, seen[field],
				))
			}
		}
		return true
	})
	if count != 1 {
		violations = append(violations, fmt.Sprintf(
			"%s: dblayer.ServerOptions literal count = %d, want 1", name, count,
		))
	}
	return violations
}

func qualifiedType(expression ast.Expr, qualifierName, typeName string) bool {
	selector, ok := expression.(*ast.SelectorExpr)
	if !ok || selector.Sel.Name != typeName {
		return false
	}
	qualifier, ok := selector.X.(*ast.Ident)
	return ok && qualifier.Name == qualifierName
}

func exactCommandFields(
	name string,
	use string,
	fields map[string]ast.Expr,
	want map[string]string,
) []string {
	if fields == nil {
		return []string{fmt.Sprintf("%s: missing cobra command %q", name, use)}
	}
	var violations []string
	if len(fields) != len(want) {
		violations = append(violations, fmt.Sprintf(
			"%s: command %q field count = %d, want %d", name, use, len(fields), len(want),
		))
	}
	for field, expected := range want {
		if !commandFieldMatches(fields[field], expected) {
			violations = append(violations, fmt.Sprintf(
				"%s: command %q field %s is not exact %s", name, use, field, expected,
			))
		}
	}
	return violations
}

func commandFieldMatches(expression ast.Expr, expected string) bool {
	if expected == "func" {
		_, ok := expression.(*ast.FuncLit)
		return ok
	}
	if value, ok := stringLiteral(expression); ok {
		return value == expected
	}
	identifier, ok := expression.(*ast.Ident)
	if ok {
		return identifier.Name == expected
	}
	selector, ok := expression.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	qualifier, qualifierOK := selector.X.(*ast.Ident)
	return qualifierOK && qualifier.Name+"."+selector.Sel.Name == expected
}

func stringLiteral(expression ast.Expr) (string, bool) {
	literal, ok := expression.(*ast.BasicLit)
	if !ok || literal.Kind != token.STRING {
		return "", false
	}
	value, err := strconv.Unquote(literal.Value)
	return value, err == nil
}

func isCobraCommandType(expression ast.Expr) bool {
	selector, ok := expression.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	qualifier, qualifierOK := selector.X.(*ast.Ident)
	return qualifierOK && qualifier.Name == "cobra" && selector.Sel.Name == "Command"
}

func countSelectorCalls(parsed *ast.File, name string) int {
	count := 0
	ast.Inspect(parsed, func(node ast.Node) bool {
		call, ok := node.(*ast.CallExpr)
		if !ok {
			return true
		}
		selector, ok := call.Fun.(*ast.SelectorExpr)
		if ok && selector.Sel.Name == name {
			count++
		}
		return true
	})
	return count
}

func commandProductionSources(root string) (map[string]string, error) {
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil, err
	}
	sources := make(map[string]string)
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".go" ||
			strings.HasSuffix(entry.Name(), "_test.go") {
			continue
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return nil, fmt.Errorf(
				"database command production source %s is a symlink",
				entry.Name(),
			)
		}
		raw, err := os.ReadFile(filepath.Join(root, entry.Name()))
		if err != nil {
			return nil, err
		}
		sources[entry.Name()] = string(raw)
	}
	return sources, nil
}

func commandPackageRoot(t *testing.T) string {
	t.Helper()
	_, currentFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve database command architecture source path")
	}
	return filepath.Dir(currentFile)
}

func containsCommandViolation(violations []string, fragment string) bool {
	for _, violation := range violations {
		if strings.Contains(violation, fragment) {
			return true
		}
	}
	return false
}
