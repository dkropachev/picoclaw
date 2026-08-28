package tools

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"maps"
	"os"
	"sort"
	"strings"
	"testing"
)

type registryDiagnosticSink struct {
	function  string
	emitter   string
	component string
	message   string
}

func TestRegistryDiagnosticSinkManifest(t *testing.T) {
	files := parseRegistryDiagnosticFiles(t, "registry.go", "registry_invocation.go")
	want := []registryDiagnosticSink{
		{"registerLegacy", "DebugSafeCF", "ComponentTools", "DiagnosticMessageToolRegistrationSkipped"},
		{"registerLegacy", "WarnSafeCF", "ComponentTools", "DiagnosticMessageToolRegistrationCollision"},
		{"registerLegacy", "WarnSafeCF", "ComponentTools", "DiagnosticMessageToolRegistrationOverwritten"},
		{"registerLegacy", "DebugSafeCF", "ComponentTools", "DiagnosticMessageToolRegistered"},
		{"Unregister", "DebugSafeCF", "ComponentTools", "DiagnosticMessageToolUnregistered"},
		{"PromoteTools", "DebugSafeCF", "ComponentTools", "DiagnosticMessageToolPromotionCompleted"},
		{"executeWithContext", "ErrorSafeCF", "ComponentTool", "DiagnosticMessageToolNotFound"},
		{"executeWithContext", "WarnSafeCF", "ComponentTool", "DiagnosticMessageToolArgumentValidationFailed"},
		{"logToolExecutionStart", "InfoSafeCF", "ComponentTool", "DiagnosticMessageToolExecutionStarted"},
		{"logToolExecutionStart", "DebugSensitiveCF", "ComponentTool", "DiagnosticMessageToolArguments"},
		{"executeResolvedToolWithContext", "ErrorSafeCF", "ComponentTool", "DiagnosticMessageToolExecutionPanic"},
		{"executeResolvedToolWithContext", "ErrorSafeCF", "ComponentTool", "DiagnosticMessageToolExecutionFailed"},
		{"executeResolvedToolWithContext", "InfoSafeCF", "ComponentTool", "DiagnosticMessageToolAsyncStarted"},
		{"executeResolvedToolWithContext", "InfoSafeCF", "ComponentTool", "DiagnosticMessageToolExecutionCompleted"},
	}

	var got []registryDiagnosticSink
	for fileName, file := range files {
		for _, declaration := range file.Decls {
			function, ok := declaration.(*ast.FuncDecl)
			if !ok || function.Body == nil {
				continue
			}
			ast.Inspect(function.Body, func(node ast.Node) bool {
				call, ok := node.(*ast.CallExpr)
				if !ok {
					return true
				}
				selector, ok := call.Fun.(*ast.SelectorExpr)
				if !ok || identifierName(selector.X) != "logger" {
					return true
				}
				if isLegacyRegistryLoggerCall(selector.Sel.Name) {
					t.Errorf(
						"%s:%s contains forbidden logger.%s",
						fileName,
						function.Name.Name,
						selector.Sel.Name,
					)
					return true
				}
				componentIndex, messageIndex, sink := registrySafeSinkIndexes(selector.Sel.Name)
				if !sink {
					if strings.HasSuffix(selector.Sel.Name, "SafeCF") ||
						strings.HasSuffix(selector.Sel.Name, "SensitiveCF") {
						t.Errorf(
							"%s:%s uses unmanifested safe sink logger.%s",
							fileName,
							function.Name.Name,
							selector.Sel.Name,
						)
					}
					return true
				}
				if len(call.Args) <= messageIndex {
					t.Errorf(
						"%s:%s logger.%s has incomplete fixed envelope",
						fileName,
						function.Name.Name,
						selector.Sel.Name,
					)
					return true
				}
				component, componentOK := loggerConstantName(call.Args[componentIndex], "Component")
				message, messageOK := loggerConstantName(call.Args[messageIndex], "DiagnosticMessage")
				if !componentOK || !messageOK {
					t.Errorf(
						"%s:%s logger.%s has a dynamic component/message",
						fileName,
						function.Name.Name,
						selector.Sel.Name,
					)
					return true
				}
				for _, argument := range call.Args {
					assertRegistrySinkArgumentSafe(t, fileName, function.Name.Name, argument)
				}
				got = append(got, registryDiagnosticSink{
					function: function.Name.Name, emitter: selector.Sel.Name,
					component: component, message: message,
				})
				return true
			})
		}
	}

	sortRegistryDiagnosticSinks(got)
	sortRegistryDiagnosticSinks(want)
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("registry diagnostic sink manifest changed\n got: %#v\nwant: %#v", got, want)
	}
}

func TestRegistryDiagnosticCapabilityConstructionManifest(t *testing.T) {
	files := parseRegistryDiagnosticFiles(
		t,
		"registry.go",
		"registry_factory.go",
		"registry_selection.go",
	)
	for _, fileName := range []string{"registry_factory.go", "registry_selection.go"} {
		file := files[fileName]
		strictCalls := 0
		ast.Inspect(file, func(node ast.Node) bool {
			call, ok := node.(*ast.CallExpr)
			if !ok {
				return true
			}
			name := calledFunctionName(call.Fun)
			if name == "NewOwnedToolRegistry" {
				t.Errorf("%s uses compatibility owner constructor", fileName)
			}
			if name != "NewOwnedToolRegistryWithDiagnosticPolicy" {
				return true
			}
			strictCalls++
			if len(call.Args) != 2 || !isSelectorNamed(call.Args[1], "diagnosticOwnerCap") {
				t.Errorf("%s owner construction does not copy exact diagnosticOwnerCap", fileName)
			}
			return true
		})
		if strictCalls != 1 {
			t.Errorf("%s strict owner constructor calls = %d, want 1", fileName, strictCalls)
		}
	}

	// The capability is immutable after construction. Keyed composite literals
	// initialize it, but no package peer may assign through a selector or add a
	// getter/use outside the exact meet and propagation paths.
	productionFiles := parseAllProductionToolsFiles(t)
	uses := make(map[string]int)
	for fileName, file := range productionFiles {
		currentFunction := ""
		for _, declaration := range file.Decls {
			function, ok := declaration.(*ast.FuncDecl)
			if !ok || function.Body == nil {
				continue
			}
			currentFunction = function.Name.Name
			ast.Inspect(function.Body, func(node ast.Node) bool {
				selector, ok := node.(*ast.SelectorExpr)
				if ok && selector.Sel.Name == "diagnosticOwnerCap" {
					uses[currentFunction]++
				}
				return true
			})
		}
		ast.Inspect(file, func(node ast.Node) bool {
			assignment, ok := node.(*ast.AssignStmt)
			if !ok {
				return true
			}
			for _, left := range assignment.Lhs {
				if isSelectorNamed(left, "diagnosticOwnerCap") {
					t.Errorf("%s mutates diagnosticOwnerCap after construction", fileName)
				}
			}
			return true
		})
	}
	wantUses := map[string]int{
		"diagnosticPolicyForContext":   1,
		"Clone":                        1,
		"InstantiateForOwner":          1,
		"InstantiateForOwnerSelection": 1,
	}
	if !maps.Equal(uses, wantUses) {
		t.Fatalf("diagnosticOwnerCap use manifest changed: got %v, want %v", uses, wantUses)
	}
}

func TestRegistryDiagnosticExecutionContextManifest(t *testing.T) {
	files := parseRegistryDiagnosticFiles(t, "registry.go", "registry_invocation.go")
	resolved := findRegistryDiagnosticFunction(t, files["registry.go"], "executeResolvedToolWithContext")
	narrowIndex := -1
	narrowCount := 0
	totalNarrowCalls := 0
	ast.Inspect(resolved.Body, func(node ast.Node) bool {
		call, ok := node.(*ast.CallExpr)
		if !ok {
			return true
		}
		selector, selected := call.Fun.(*ast.SelectorExpr)
		if selected && identifierName(selector.X) == "logger" &&
			selector.Sel.Name == "NarrowDiagnosticPolicy" {
			totalNarrowCalls++
		}
		return true
	})
	for index, statement := range resolved.Body.List {
		assignment, ok := statement.(*ast.AssignStmt)
		if !ok || assignment.Tok != token.DEFINE || len(assignment.Lhs) != 2 ||
			len(assignment.Rhs) != 1 || identifierName(assignment.Lhs[0]) != "ctx" ||
			identifierName(assignment.Lhs[1]) != "revokeDiagnosticPolicy" {
			continue
		}
		call, ok := assignment.Rhs[0].(*ast.CallExpr)
		if ok && exactRegistryDiagnosticCall(
			call,
			"logger",
			"NarrowDiagnosticPolicy",
			"ctx",
			"diagnosticCap",
		) {
			narrowIndex = index
			narrowCount++
		}
	}
	if totalNarrowCalls != 1 || narrowCount != 1 || narrowIndex < 0 ||
		narrowIndex+2 >= len(resolved.Body.List) {
		t.Fatal("executeResolvedToolWithContext lacks exact diagnostic Narrow assignment")
	}
	deferred, ok := resolved.Body.List[narrowIndex+1].(*ast.DeferStmt)
	if !ok || !exactRegistryDiagnosticCall(
		deferred.Call,
		"",
		"revokeDiagnosticPolicy",
	) {
		t.Fatal("diagnostic Narrow revoke is not the immediate deferred statement")
	}
	revokeCalls := 0
	ast.Inspect(resolved.Body, func(node ast.Node) bool {
		call, callOK := node.(*ast.CallExpr)
		if callOK && exactRegistryDiagnosticCall(call, "", "revokeDiagnosticPolicy") {
			revokeCalls++
		}
		return true
	})
	if revokeCalls != 1 {
		t.Fatalf("diagnostic Narrow revoke calls = %d, want 1", revokeCalls)
	}
	capAssignment, ok := resolved.Body.List[narrowIndex+2].(*ast.AssignStmt)
	if !ok || capAssignment.Tok != token.ASSIGN || len(capAssignment.Lhs) != 1 ||
		len(capAssignment.Rhs) != 1 || identifierName(capAssignment.Lhs[0]) != "ctx" {
		t.Fatal("private registry cap is not bound immediately after Narrow revoke")
	}
	capCall, ok := capAssignment.Rhs[0].(*ast.CallExpr)
	if !ok || !exactRegistryDiagnosticCall(
		capCall,
		"",
		"withToolRegistryDiagnosticCap",
		"ctx",
		"diagnosticCap",
	) {
		t.Fatal("private registry cap binding changed")
	}

	for _, spec := range []struct {
		file     string
		function string
	}{
		{file: "registry.go", function: "executeWithContext"},
		{file: "registry_invocation.go", function: "DispatchClaimed"},
	} {
		function := findRegistryDiagnosticFunction(t, files[spec.file], spec.function)
		computed := 0
		passed := 0
		ast.Inspect(function.Body, func(node ast.Node) bool {
			switch value := node.(type) {
			case *ast.AssignStmt:
				if value.Tok != token.DEFINE || len(value.Lhs) != 1 ||
					len(value.Rhs) != 1 || identifierName(value.Lhs[0]) != "diagnosticCap" {
					return true
				}
				call, callOK := value.Rhs[0].(*ast.CallExpr)
				if callOK && exactRegistryDiagnosticCall(
					call,
					"r",
					"diagnosticPolicyForContext",
					"ctx",
					"effectiveSuppressed",
				) {
					computed++
				}
			case *ast.CallExpr:
				if calledFunctionName(value.Fun) != "executeResolvedToolWithContext" {
					return true
				}
				if len(value.Args) == 10 && identifierName(value.Args[8]) == "diagnosticCap" &&
					identifierName(value.Args[9]) == "effectiveSuppressed" {
					passed++
				}
			}
			return true
		})
		if computed != 1 || passed != 1 {
			t.Errorf(
				"%s:%s diagnostic cap compute/pass = %d/%d, want 1/1",
				spec.file,
				spec.function,
				computed,
				passed,
			)
		}
	}

	policyFunction := findRegistryDiagnosticFunction(
		t,
		files["registry.go"],
		"diagnosticPolicyForContext",
	)
	inheritedLookup := 0
	inheritedMeet := 0
	ast.Inspect(policyFunction.Body, func(node ast.Node) bool {
		call, ok := node.(*ast.CallExpr)
		if !ok {
			return true
		}
		if exactRegistryDiagnosticCall(
			call,
			"",
			"toolRegistryDiagnosticCapFromContext",
			"ctx",
		) {
			inheritedLookup++
		}
		if exactRegistryDiagnosticCall(call, "effective", "Meet", "inherited") {
			inheritedMeet++
		}
		return true
	})
	if inheritedLookup != 1 || inheritedMeet != 1 {
		t.Fatalf(
			"inherited registry cap lookup/meet = %d/%d, want 1/1",
			inheritedLookup,
			inheritedMeet,
		)
	}
}

func findRegistryDiagnosticFunction(
	t *testing.T,
	file *ast.File,
	name string,
) *ast.FuncDecl {
	t.Helper()
	for _, declaration := range file.Decls {
		function, ok := declaration.(*ast.FuncDecl)
		if ok && function.Name.Name == name {
			return function
		}
	}
	t.Fatalf("function %s is missing", name)
	return nil
}

func exactRegistryDiagnosticCall(
	call *ast.CallExpr,
	receiver string,
	name string,
	arguments ...string,
) bool {
	if call == nil || len(call.Args) != len(arguments) {
		return false
	}
	if receiver == "" {
		if identifierName(call.Fun) != name {
			return false
		}
	} else {
		selector, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || identifierName(selector.X) != receiver || selector.Sel.Name != name {
			return false
		}
	}
	for index, argument := range arguments {
		if identifierName(call.Args[index]) != argument {
			return false
		}
	}
	return true
}

func parseRegistryDiagnosticFiles(t *testing.T, names ...string) map[string]*ast.File {
	t.Helper()
	files := make(map[string]*ast.File, len(names))
	for _, name := range names {
		file, err := parser.ParseFile(token.NewFileSet(), name, nil, parser.SkipObjectResolution)
		if err != nil {
			t.Fatalf("ParseFile(%s) error = %v", name, err)
		}
		files[name] = file
	}
	return files
}

func parseAllProductionToolsFiles(t *testing.T) map[string]*ast.File {
	t.Helper()
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("ReadDir(.) error = %v", err)
	}
	var names []string
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") ||
			strings.HasSuffix(name, "_test.go") {
			continue
		}
		names = append(names, name)
	}
	return parseRegistryDiagnosticFiles(t, names...)
}

func registrySafeSinkIndexes(name string) (component, message int, ok bool) {
	switch name {
	case "DebugSafeCF", "InfoSafeCF", "WarnSafeCF", "ErrorSafeCF":
		return 0, 1, true
	case "DebugSensitiveCF":
		return 1, 2, true
	default:
		return 0, 0, false
	}
}

func isLegacyRegistryLoggerCall(name string) bool {
	if strings.HasPrefix(name, "Recover") {
		return true
	}
	for _, prefix := range []string{"Debug", "Info", "Warn", "Error", "Fatal"} {
		if name == prefix || name == prefix+"C" || name == prefix+"f" ||
			name == prefix+"F" || name == prefix+"CF" {
			return true
		}
	}
	return false
}

func loggerConstantName(expression ast.Expr, prefix string) (string, bool) {
	selector, ok := expression.(*ast.SelectorExpr)
	if !ok || identifierName(selector.X) != "logger" ||
		!strings.HasPrefix(selector.Sel.Name, prefix) {
		return "", false
	}
	return selector.Sel.Name, true
}

func assertRegistrySinkArgumentSafe(
	t *testing.T,
	fileName string,
	functionName string,
	expression ast.Expr,
) {
	t.Helper()
	ast.Inspect(expression, func(node ast.Node) bool {
		switch typed := node.(type) {
		case *ast.CompositeLit:
			if _, dynamicMap := typed.Type.(*ast.MapType); dynamicMap {
				t.Errorf(
					"%s:%s constructs a dynamic map in logger sink arguments",
					fileName,
					functionName,
				)
			}
		case *ast.CallExpr:
			selector, ok := typed.Fun.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			owner := identifierName(selector.X)
			if owner == "fmt" || owner == "debug" {
				t.Errorf(
					"%s:%s invokes %s.%s in logger sink arguments",
					fileName,
					functionName,
					owner,
					selector.Sel.Name,
				)
			}
			switch selector.Sel.Name {
			case "Error", "String", "Format", "ContentForLLM":
				t.Errorf(
					"%s:%s invokes .%s in logger sink arguments",
					fileName,
					functionName,
					selector.Sel.Name,
				)
			}
		}
		return true
	})
}

func sortRegistryDiagnosticSinks(sinks []registryDiagnosticSink) {
	sort.Slice(sinks, func(left, right int) bool {
		return fmt.Sprint(sinks[left]) < fmt.Sprint(sinks[right])
	})
}

func identifierName(expression ast.Expr) string {
	identifier, _ := expression.(*ast.Ident)
	if identifier == nil {
		return ""
	}
	return identifier.Name
}

func calledFunctionName(expression ast.Expr) string {
	switch typed := expression.(type) {
	case *ast.Ident:
		return typed.Name
	case *ast.SelectorExpr:
		return typed.Sel.Name
	default:
		return ""
	}
}

func isSelectorNamed(expression ast.Expr, name string) bool {
	selector, ok := expression.(*ast.SelectorExpr)
	return ok && selector.Sel.Name == name
}
