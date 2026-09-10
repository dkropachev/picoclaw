package database

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

const (
	supervisorDatabaseImportPath = "github.com/sipeed/picoclaw/pkg/database"
	supervisorCommandImportPath  = "github.com/sipeed/picoclaw/cmd/picoclaw/internal/database"
)

type supervisorSymbolPolicy struct {
	importPath      string
	declarationPath string
	allowedRefs     map[string]bool
}

func TestSupervisorProductionFilesAreExactAndProviderNeutral(t *testing.T) {
	t.Parallel()

	repositoryRoot := supervisorArchitectureRepositoryRoot(t)
	packageRoot := filepath.Join(repositoryRoot, "pkg", "database")
	allowedImports := supervisorProductionImportAllowlist()
	required := make(map[string]bool, len(allowedImports))
	for name := range allowedImports {
		required[name] = false
	}

	entries, err := os.ReadDir(packageRoot)
	if err != nil {
		t.Fatal(err)
	}
	var violations []string
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasPrefix(name, "supervisor_") ||
			!strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		if _, expected := required[name]; expected {
			required[name] = true
		} else {
			violations = append(violations, name+": unreviewed supervisor production file")
		}
		if entry.Type()&os.ModeSymlink != 0 {
			violations = append(violations, name+": supervisor source must not be a symlink")
		}

		path := filepath.Join(packageRoot, name)
		fileSet := token.NewFileSet()
		parsed, parseErr := parser.ParseFile(fileSet, path, nil, parser.ParseComments|parser.AllErrors)
		if parseErr != nil {
			violations = append(violations, fmt.Sprintf("%s: parse: %v", name, parseErr))
			continue
		}
		violations = append(
			violations,
			scanSupervisorProductionFile(name, parsed, fileSet, allowedImports[name])...,
		)
	}
	for name, found := range required {
		if !found {
			violations = append(violations, name+": required supervisor production file is missing")
		}
	}
	if len(violations) != 0 {
		sort.Strings(violations)
		t.Fatalf("database supervisor production boundary changed:\n%s", strings.Join(violations, "\n"))
	}
}

func scanSupervisorProductionFile(
	name string,
	parsed *ast.File,
	fileSet *token.FileSet,
	allowed map[string]bool,
) []string {
	var violations []string
	if parsed == nil || parsed.Name == nil || parsed.Name.Name != "database" {
		return []string{name + ": production supervisor source must use package database"}
	}
	found := make(map[string]bool, len(allowed))
	for _, imported := range parsed.Imports {
		importPath, err := strconv.Unquote(imported.Path.Value)
		if err != nil {
			violations = append(violations, fmt.Sprintf(
				"%s:%d has an invalid import", name, fileSet.Position(imported.Pos()).Line,
			))
			continue
		}
		if !allowed[importPath] {
			violations = append(violations, fmt.Sprintf(
				"%s:%d imports unapproved package %s",
				name, fileSet.Position(imported.Pos()).Line, importPath,
			))
		} else {
			found[importPath] = true
		}
		if imported.Name != nil {
			violations = append(violations, fmt.Sprintf(
				"%s:%d aliases production supervisor import %s",
				name, fileSet.Position(imported.Pos()).Line, importPath,
			))
		}
	}
	for importPath := range allowed {
		if !found[importPath] {
			violations = append(violations, name+": required import is missing "+importPath)
		}
	}
	for _, comments := range parsed.Comments {
		for _, comment := range comments.List {
			if strings.Contains(comment.Text, "go:linkname") {
				violations = append(violations, fmt.Sprintf(
					"%s:%d uses forbidden go:linkname",
					name, fileSet.Position(comment.Pos()).Line,
				))
			}
		}
	}
	return violations
}

func supervisorProductionImportAllowlist() map[string]map[string]bool {
	imports := func(values ...string) map[string]bool {
		result := make(map[string]bool, len(values))
		for _, value := range values {
			result[value] = true
		}
		return result
	}
	return map[string]map[string]bool{
		"supervisor_bootstrap_guard.go": imports("os", "path/filepath"),
		"supervisor_bootstrap_other.go": imports(),
		"supervisor_bootstrap_unix.go": imports(
			"os", "path/filepath", "golang.org/x/sys/unix",
		),
		"supervisor_bootstrap_windows.go": imports(
			"errors", "os", "path/filepath", "strings", "golang.org/x/sys/windows",
		),
		"supervisor_environment_nonwindows.go": imports(),
		"supervisor_environment_windows.go":    imports("strings"),
		"supervisor_file_identity.go": imports(
			"os", "strings", "github.com/sipeed/picoclaw/internal/fileidentity",
		),
		"supervisor_files_other.go": imports("os"),
		"supervisor_files_unix.go": imports(
			"errors", "os", "syscall", "golang.org/x/sys/unix",
		),
		"supervisor_files_windows.go": imports(
			"os", "unsafe", "golang.org/x/sys/windows",
		),
		"supervisor_owner_other.go": imports("errors", "os", "os/exec", "sync"),
		"supervisor_owner_unix.go": imports(
			"errors", "os", "os/exec", "sync", "syscall", "time",
		),
		"supervisor_owner_windows.go": imports(
			"errors", "fmt", "os", "sync", "time", "golang.org/x/sys/windows",
		),
		"supervisor_process.go": imports(
			"context", "errors", "os", "os/exec", "path/filepath", "strconv", "strings", "sync", "time",
		),
		"supervisor_process_other.go": imports("errors", "fmt", "os/exec"),
		"supervisor_process_unix.go":  imports("errors", "fmt", "os", "os/exec", "syscall"),
		"supervisor_process_windows.go": imports(
			"errors", "os", "os/exec", "runtime", "strings", "unicode/utf16", "unsafe",
			"golang.org/x/sys/windows",
		),
		"supervisor_test_executable.go": imports("errors", "os", "testing"),
	}
}

func TestSupervisorProductionActivationReferencesAreExact(t *testing.T) {
	t.Parallel()

	repositoryRoot := supervisorArchitectureRepositoryRoot(t)
	policies := supervisorArchitectureSymbolPolicies()
	declarations := make(map[string]int, len(policies))
	references := make(map[string]map[string]int, len(policies))
	var violations []string
	err := filepath.WalkDir(
		repositoryRoot,
		func(path string, entry fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if entry.IsDir() {
				if path != repositoryRoot &&
					supervisorArchitectureSkipsDirectory(entry.Name()) {
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
			relative = filepath.ToSlash(relative)
			fileSet := token.NewFileSet()
			parsed, err := parser.ParseFile(
				fileSet, path, nil, parser.ParseComments|parser.AllErrors,
			)
			if err != nil {
				return fmt.Errorf("parse production Go file %s: %w", relative, err)
			}
			fileViolations, fileDeclarations, fileReferences := scanSupervisorArchitectureReferences(
				relative, parsed, fileSet, policies,
			)
			if relative == "cmd/picoclaw/main.go" {
				fileViolations = append(
					fileViolations,
					supervisorRootCommandHookViolations(relative, parsed, fileSet)...,
				)
			}
			violations = append(violations, fileViolations...)
			for symbol, count := range fileDeclarations {
				declarations[symbol] += count
			}
			for symbol, count := range fileReferences {
				if references[symbol] == nil {
					references[symbol] = make(map[string]int)
				}
				references[symbol][relative] += count
			}
			return nil
		},
	)
	if err != nil {
		t.Fatalf("scan supervisor activation references: %v", err)
	}
	for symbol := range policies {
		if declarations[symbol] != 1 {
			violations = append(violations, fmt.Sprintf(
				"%s declaration count = %d, want 1", symbol, declarations[symbol],
			))
		}
	}
	for _, required := range []struct {
		symbol string
		path   string
		count  int
	}{
		{symbol: "EnsureSupervisor", path: "pkg/database/supervisor_process.go"},
		{symbol: "ensureSupervisor", path: "pkg/database/supervisor_process.go"},
		{symbol: "startSupervisorProcessUntil", path: "pkg/database/supervisor_process.go", count: 2},
		{symbol: "startSupervisorProcessUntilWith", path: "pkg/database/supervisor_process.go"},
		{symbol: "monitorSupervisor", path: "pkg/database/supervisor_process.go"},
		{symbol: "ConsumeSupervisorBootstrapGuard", path: "cmd/picoclaw/internal/database/command.go"},
		{symbol: "NewDatabaseCommand", path: "cmd/picoclaw/main.go"},
	} {
		want := required.count
		if want == 0 {
			want = 1
		}
		if references[required.symbol][required.path] != want {
			violations = append(violations, fmt.Sprintf(
				"%s reference count in %s = %d, want %d",
				required.symbol, required.path, references[required.symbol][required.path], want,
			))
		}
	}
	if len(violations) != 0 {
		sort.Strings(violations)
		t.Fatalf("database supervisor activation boundary changed:\n%s", strings.Join(violations, "\n"))
	}
}

func supervisorArchitectureSymbolPolicies() map[string]supervisorSymbolPolicy {
	return map[string]supervisorSymbolPolicy{
		"EnsureSupervisor": {
			importPath: supervisorDatabaseImportPath, declarationPath: "pkg/database/supervisor_process.go",
			allowedRefs: map[string]bool{
				"pkg/database/supervisor_process.go": true,
			},
		},
		"ensureSupervisor": {
			declarationPath: "pkg/database/supervisor_process.go",
			allowedRefs: map[string]bool{
				"pkg/database/supervisor_process.go": true,
			},
		},
		"startSupervisorProcess": {
			declarationPath: "pkg/database/supervisor_process.go",
		},
		"startSupervisorProcessUntil": {
			declarationPath: "pkg/database/supervisor_process.go",
			allowedRefs: map[string]bool{
				"pkg/database/supervisor_process.go": true,
			},
		},
		"startSupervisorProcessUntilWith": {
			declarationPath: "pkg/database/supervisor_process.go",
			allowedRefs: map[string]bool{
				"pkg/database/supervisor_process.go": true,
			},
		},
		"MonitorSupervisor": {
			importPath: supervisorDatabaseImportPath, declarationPath: "pkg/database/supervisor_process.go",
		},
		"monitorSupervisor": {
			declarationPath: "pkg/database/supervisor_process.go",
			allowedRefs: map[string]bool{
				"pkg/database/supervisor_process.go": true,
			},
		},
		"ConsumeSupervisorBootstrap": {
			importPath: supervisorDatabaseImportPath, declarationPath: "pkg/database/supervisor_process.go",
		},
		"ConsumeSupervisorBootstrapGuard": {
			importPath:      supervisorDatabaseImportPath,
			declarationPath: "pkg/database/supervisor_bootstrap_guard.go",
			allowedRefs: map[string]bool{
				"cmd/picoclaw/internal/database/command.go": true,
			},
		},
		"NewDatabaseCommand": {
			importPath:      supervisorCommandImportPath,
			declarationPath: "cmd/picoclaw/internal/database/command.go",
			allowedRefs: map[string]bool{
				"cmd/picoclaw/main.go": true,
			},
		},
	}
}

func scanSupervisorArchitectureReferences(
	relative string,
	parsed *ast.File,
	fileSet *token.FileSet,
	policies map[string]supervisorSymbolPolicy,
) ([]string, map[string]int, map[string]int) {
	var violations []string
	declarations := make(map[string]int)
	references := make(map[string]int)
	parents := supervisorArchitectureParents(parsed)
	declarationIdentifiers := make(map[*ast.Ident]bool)
	aliases := make(map[string]string)
	dotImports := make(map[string]bool)
	for _, group := range parsed.Comments {
		for _, comment := range group.List {
			if supervisorLinknameTargetsGuardedSymbol(comment.Text, policies) {
				violations = append(violations, fmt.Sprintf(
					"%s:%d linknames a guarded supervisor symbol",
					relative, fileSet.Position(comment.Pos()).Line,
				))
			}
		}
	}
	for _, imported := range parsed.Imports {
		importPath, err := strconv.Unquote(imported.Path.Value)
		if err != nil {
			continue
		}
		guarded := importPath == supervisorDatabaseImportPath || importPath == supervisorCommandImportPath
		if !guarded {
			continue
		}
		name := filepath.Base(importPath)
		if imported.Name != nil {
			name = imported.Name.Name
		}
		switch name {
		case ".":
			dotImports[importPath] = true
			violations = append(violations, fmt.Sprintf(
				"%s:%d dot-imports guarded supervisor package %s",
				relative, fileSet.Position(imported.Pos()).Line, importPath,
			))
		case "_":
		default:
			aliases[name] = importPath
		}
	}

	for _, declaration := range parsed.Decls {
		function, ok := declaration.(*ast.FuncDecl)
		if !ok || function.Recv != nil {
			continue
		}
		policy, guarded := policies[function.Name.Name]
		if !guarded || filepath.ToSlash(filepath.Dir(relative)) !=
			filepath.ToSlash(filepath.Dir(policy.declarationPath)) {
			continue
		}
		declarationIdentifiers[function.Name] = true
		declarations[function.Name.Name]++
		if relative != policy.declarationPath {
			violations = append(violations, fmt.Sprintf(
				"%s:%d declares guarded symbol %s",
				relative, fileSet.Position(function.Name.Pos()).Line, function.Name.Name,
			))
		}
	}

	ast.Inspect(parsed, func(node ast.Node) bool {
		selector, ok := node.(*ast.SelectorExpr)
		if ok {
			qualifier, qualified := selector.X.(*ast.Ident)
			policy, guarded := policies[selector.Sel.Name]
			if qualified && guarded && policy.importPath != "" &&
				aliases[qualifier.Name] == policy.importPath {
				references[selector.Sel.Name]++
				if !policy.allowedRefs[relative] {
					violations = append(violations, fmt.Sprintf(
						"%s:%d references guarded supervisor symbol %s",
						relative, fileSet.Position(selector.Pos()).Line, selector.Sel.Name,
					))
				} else if !supervisorArchitectureReferenceHasExactShape(
					relative, selector.Sel.Name, selector, parents,
				) {
					violations = append(violations, fmt.Sprintf(
						"%s:%d uses guarded supervisor symbol %s outside its exact composition shape",
						relative, fileSet.Position(selector.Pos()).Line, selector.Sel.Name,
					))
				}
			}
		}
		identifier, ok := node.(*ast.Ident)
		if !ok || declarationIdentifiers[identifier] ||
			supervisorArchitectureSelectorMember(identifier, parents) {
			return true
		}
		policy, guarded := policies[identifier.Name]
		if !guarded {
			return true
		}
		samePackage := filepath.ToSlash(filepath.Dir(relative)) ==
			filepath.ToSlash(filepath.Dir(policy.declarationPath))
		if dotImports[policy.importPath] {
			references[identifier.Name]++
			if !policy.allowedRefs[relative] {
				violations = append(violations, fmt.Sprintf(
					"%s:%d references dot-imported guarded supervisor symbol %s",
					relative, fileSet.Position(identifier.Pos()).Line, identifier.Name,
				))
			} else if !supervisorArchitectureReferenceHasExactShape(
				relative, identifier.Name, identifier, parents,
			) {
				violations = append(violations, fmt.Sprintf(
					"%s:%d uses dot-imported guarded supervisor symbol %s outside its exact composition shape",
					relative, fileSet.Position(identifier.Pos()).Line, identifier.Name,
				))
			}
		} else if samePackage {
			references[identifier.Name]++
			if !policy.allowedRefs[relative] {
				violations = append(violations, fmt.Sprintf(
					"%s:%d references guarded same-package symbol %s",
					relative, fileSet.Position(identifier.Pos()).Line, identifier.Name,
				))
			} else if !supervisorArchitectureReferenceHasExactShape(
				relative, identifier.Name, identifier, parents,
			) {
				violations = append(violations, fmt.Sprintf(
					"%s:%d uses guarded same-package symbol %s outside its exact composition shape",
					relative, fileSet.Position(identifier.Pos()).Line, identifier.Name,
				))
			}
		}
		return true
	})
	return violations, declarations, references
}

func supervisorArchitectureParents(root ast.Node) map[ast.Node]ast.Node {
	parents := make(map[ast.Node]ast.Node)
	var stack []ast.Node
	ast.Inspect(root, func(node ast.Node) bool {
		if node == nil {
			stack = stack[:len(stack)-1]
			return false
		}
		if len(stack) != 0 {
			parents[node] = stack[len(stack)-1]
		}
		stack = append(stack, node)
		return true
	})
	return parents
}

func supervisorLinknameTargetsGuardedSymbol(
	comment string,
	policies map[string]supervisorSymbolPolicy,
) bool {
	if !strings.Contains(comment, "go:linkname") {
		return false
	}
	if strings.Contains(comment, supervisorDatabaseImportPath+".") ||
		strings.Contains(comment, supervisorCommandImportPath+".") {
		return true
	}
	for symbol := range policies {
		if strings.Contains(comment, symbol) {
			return true
		}
	}
	return false
}

func supervisorArchitectureSelectorMember(
	identifier *ast.Ident,
	parents map[ast.Node]ast.Node,
) bool {
	selector, ok := parents[identifier].(*ast.SelectorExpr)
	return ok && selector.Sel == identifier
}

func supervisorArchitectureReferenceHasExactShape(
	relative string,
	symbol string,
	reference ast.Expr,
	parents map[ast.Node]ast.Node,
) bool {
	switch symbol {
	case "NewDatabaseCommand":
		call, ok := parents[reference].(*ast.CallExpr)
		if !ok || call.Fun != reference || len(call.Args) != 0 {
			return false
		}
		registration, ok := parents[call].(*ast.CallExpr)
		if !ok || !supervisorArchitectureDirectArgument(registration, call) {
			return false
		}
		selector, ok := registration.Fun.(*ast.SelectorExpr)
		if !ok {
			return false
		}
		receiver, receiverOK := selector.X.(*ast.Ident)
		return relative == "cmd/picoclaw/main.go" && receiverOK &&
			receiver.Name == "cmd" && selector.Sel.Name == "AddCommand" &&
			supervisorArchitectureEnclosingFunction(reference, parents) == "NewPicoclawCommand"
	case "ConsumeSupervisorBootstrapGuard":
		call, ok := parents[reference].(*ast.CallExpr)
		return relative == "cmd/picoclaw/internal/database/command.go" && ok &&
			call.Fun == reference && len(call.Args) == 1 &&
			supervisorArchitectureEnclosingFunction(reference, parents) ==
				"defaultSupervisorServeOps"
	case "EnsureSupervisor":
		keyValue, ok := parents[reference].(*ast.KeyValueExpr)
		if !ok {
			return false
		}
		key, keyOK := keyValue.Key.(*ast.Ident)
		if !keyOK || keyValue.Value != reference || key.Name != "ensure" {
			return false
		}
		literal, ok := parents[keyValue].(*ast.CompositeLit)
		literalType, typeOK := literal.Type.(*ast.Ident)
		if !ok || !typeOK || literalType.Name != "supervisorMonitorOps" {
			return false
		}
		call, ok := parents[literal].(*ast.CallExpr)
		if !ok {
			return false
		}
		callee, calleeOK := call.Fun.(*ast.Ident)
		return relative == "pkg/database/supervisor_process.go" && calleeOK &&
			supervisorArchitectureDirectArgument(call, literal) &&
			callee.Name == "monitorSupervisor" &&
			supervisorArchitectureEnclosingFunction(reference, parents) == "MonitorSupervisor"
	case "ensureSupervisor":
		return supervisorArchitectureDirectCallInFunction(
			reference, parents, "EnsureSupervisor", 3,
		)
	case "startSupervisorProcessUntil":
		if supervisorArchitectureDirectCallInFunction(
			reference, parents, "startSupervisorProcess", 3,
		) {
			return true
		}
		keyValue, ok := parents[reference].(*ast.KeyValueExpr)
		if !ok {
			return false
		}
		key, keyOK := keyValue.Key.(*ast.Ident)
		literal, literalOK := parents[keyValue].(*ast.CompositeLit)
		if !keyOK || !literalOK || keyValue.Value != reference || key.Name != "start" {
			return false
		}
		literalType, typeOK := literal.Type.(*ast.Ident)
		return typeOK && literalType.Name == "supervisorEnsureOps" &&
			supervisorArchitectureEnclosingFunction(reference, parents) == "EnsureSupervisor"
	case "startSupervisorProcessUntilWith":
		return supervisorArchitectureDirectCallInFunction(
			reference, parents, "startSupervisorProcessUntil", 4,
		)
	case "monitorSupervisor":
		return supervisorArchitectureDirectCallInFunction(
			reference, parents, "MonitorSupervisor", 3,
		)
	default:
		return false
	}
}

func supervisorArchitectureDirectCallInFunction(
	reference ast.Expr,
	parents map[ast.Node]ast.Node,
	function string,
	argumentCount int,
) bool {
	call, ok := parents[reference].(*ast.CallExpr)
	return ok && call.Fun == reference && len(call.Args) == argumentCount &&
		supervisorArchitectureEnclosingFunction(reference, parents) == function
}

func supervisorArchitectureDirectArgument(call *ast.CallExpr, expression ast.Expr) bool {
	if call == nil {
		return false
	}
	for _, argument := range call.Args {
		if argument == expression {
			return true
		}
	}
	return false
}

func supervisorArchitectureEnclosingFunction(
	node ast.Node,
	parents map[ast.Node]ast.Node,
) string {
	for current := parents[node]; current != nil; current = parents[current] {
		if function, ok := current.(*ast.FuncDecl); ok {
			return function.Name.Name
		}
	}
	return ""
}

func supervisorRootCommandHookViolations(
	relative string,
	parsed *ast.File,
	fileSet *token.FileSet,
) []string {
	rootCount := 0
	var violations []string
	ast.Inspect(parsed, func(node ast.Node) bool {
		literal, ok := node.(*ast.CompositeLit)
		if !ok || !supervisorQualifiedType(literal.Type, "cobra", "Command") {
			return true
		}
		fields := make(map[string][]ast.Expr)
		for _, element := range literal.Elts {
			keyValue, ok := element.(*ast.KeyValueExpr)
			if !ok {
				continue
			}
			key, ok := keyValue.Key.(*ast.Ident)
			if ok {
				fields[key.Name] = append(fields[key.Name], keyValue.Value)
			}
		}
		if len(fields["Use"]) != 1 || supervisorStringLiteral(fields["Use"][0]) != "picoclaw" {
			return true
		}
		rootCount++
		if len(fields["PersistentPreRunE"]) != 0 || len(fields["PersistentPreRun"]) != 1 ||
			!supervisorRootPreRunIsColorOnly(fields["PersistentPreRun"][0]) {
			violations = append(violations, fmt.Sprintf(
				"%s:%d root persistent pre-run must perform only CLI color synchronization",
				relative, fileSet.Position(literal.Pos()).Line,
			))
		}
		return true
	})
	if rootCount != 1 {
		violations = append(violations, fmt.Sprintf(
			"%s root cobra command count = %d, want 1", relative, rootCount,
		))
	}
	return violations
}

func supervisorRootPreRunIsColorOnly(expression ast.Expr) bool {
	function, ok := expression.(*ast.FuncLit)
	if !ok || function.Body == nil || len(function.Body.List) != 1 {
		return false
	}
	statement, ok := function.Body.List[0].(*ast.ExprStmt)
	if !ok {
		return false
	}
	call, ok := statement.X.(*ast.CallExpr)
	if !ok {
		return false
	}
	callee, calleeOK := call.Fun.(*ast.Ident)
	if !calleeOK || callee.Name != "syncCliUIColor" || len(call.Args) != 1 {
		return false
	}
	rootCall, ok := call.Args[0].(*ast.CallExpr)
	if !ok || len(rootCall.Args) != 0 {
		return false
	}
	selector, ok := rootCall.Fun.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	receiver, receiverOK := selector.X.(*ast.Ident)
	return receiverOK && receiver.Name == "c" && selector.Sel.Name == "Root"
}

func supervisorQualifiedType(expression ast.Expr, qualifier, name string) bool {
	selector, ok := expression.(*ast.SelectorExpr)
	if !ok || selector.Sel.Name != name {
		return false
	}
	prefix, ok := selector.X.(*ast.Ident)
	return ok && prefix.Name == qualifier
}

func supervisorStringLiteral(expression ast.Expr) string {
	literal, ok := expression.(*ast.BasicLit)
	if !ok || literal.Kind != token.STRING {
		return ""
	}
	value, err := strconv.Unquote(literal.Value)
	if err != nil {
		return ""
	}
	return value
}

func TestSupervisorRootHookScannerRejectsPreAuthorizationWork(t *testing.T) {
	t.Parallel()

	fileSet := token.NewFileSet()
	parsed, err := parser.ParseFile(fileSet, "main.go", `package main
import "github.com/spf13/cobra"
func command() *cobra.Command {
	return &cobra.Command{
		Use: "picoclaw",
		PersistentPreRun: func(c *cobra.Command, _ []string) {
			syncCliUIColor(c.Root())
			loadConfig()
		},
	}
}
`, parser.AllErrors)
	if err != nil {
		t.Fatal(err)
	}
	violations := supervisorRootCommandHookViolations("cmd/picoclaw/main.go", parsed, fileSet)
	if !strings.Contains(strings.Join(violations, "\n"), "color synchronization") {
		t.Fatalf("root-hook violations = %v, want pre-authorization-work rejection", violations)
	}
}

func TestSupervisorReferenceScannerDetectsIndirectAliases(t *testing.T) {
	t.Parallel()

	policies := supervisorArchitectureSymbolPolicies()
	for _, test := range []struct {
		name   string
		path   string
		source string
		want   string
	}{
		{
			name: "function value alias",
			source: `package sample
import db "github.com/sipeed/picoclaw/pkg/database"
var start = db.EnsureSupervisor
`,
			want: "EnsureSupervisor",
		},
		{
			name: "dot import",
			source: `package sample
import . "github.com/sipeed/picoclaw/pkg/database"
var monitor = MonitorSupervisor
`,
			want: "dot-imports guarded supervisor package",
		},
		{
			name: "aliased hidden constructor",
			source: `package sample
import hidden "github.com/sipeed/picoclaw/cmd/picoclaw/internal/database"
var command = hidden.NewDatabaseCommand
`,
			want: "NewDatabaseCommand",
		},
		{
			name: "hidden constructor is only referenced",
			path: "cmd/picoclaw/main.go",
			source: `package main
import databasecmd "github.com/sipeed/picoclaw/cmd/picoclaw/internal/database"
func NewPicoclawCommand() { _ = databasecmd.NewDatabaseCommand }
`,
			want: "outside its exact composition shape",
		},
		{
			name: "exported activation inside declaration file",
			path: "pkg/database/supervisor_process.go",
			source: `package database
func EnsureSupervisor() {}
func init() { EnsureSupervisor() }
`,
			want: "outside its exact composition shape",
		},
		{
			name: "unexported activation from another file",
			path: "pkg/database/auto.go",
			source: `package database
func activate() { monitorSupervisor(nil, nil, supervisorMonitorOps{}) }
`,
			want: "references guarded same-package symbol monitorSupervisor",
		},
		{
			name: "linkname activation",
			source: `package sample
import _ "unsafe"
//go:linkname activate github.com/sipeed/picoclaw/pkg/database.EnsureSupervisor
func activate()
`,
			want: "linknames a guarded supervisor symbol",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			fileSet := token.NewFileSet()
			parsed, err := parser.ParseFile(
				fileSet, "fixture.go", test.source, parser.ParseComments|parser.AllErrors,
			)
			if err != nil {
				t.Fatal(err)
			}
			path := test.path
			if path == "" {
				path = "fixture/sample.go"
			}
			violations, _, _ := scanSupervisorArchitectureReferences(path, parsed, fileSet, policies)
			if !strings.Contains(strings.Join(violations, "\n"), test.want) {
				t.Fatalf("scanner violations = %v, want %q", violations, test.want)
			}
		})
	}

	fileSet := token.NewFileSet()
	parsed, err := parser.ParseFile(fileSet, "fixture.go", `package sample
type runner struct{}
func (runner) monitorSupervisor() {}
func use(value runner) { value.monitorSupervisor() }
`, parser.AllErrors)
	if err != nil {
		t.Fatal(err)
	}
	violations, _, _ := scanSupervisorArchitectureReferences(
		"fixture/sample.go", parsed, fileSet, policies,
	)
	if len(violations) != 0 {
		t.Fatalf("benign method-name violations = %v, want none", violations)
	}
}

func supervisorArchitectureRepositoryRoot(t *testing.T) string {
	t.Helper()
	_, currentFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve supervisor architecture source path")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(currentFile), "..", ".."))
}

func supervisorArchitectureSkipsDirectory(name string) bool {
	switch strings.ToLower(name) {
	case ".git", ".cache", "node_modules", "testdata", "vendor":
		return true
	default:
		return false
	}
}
