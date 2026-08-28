package agent

import (
	"bytes"
	"fmt"
	"go/ast"
	"go/format"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
	"unicode"
)

type p015HookSinkManifestEntry struct {
	file     string
	function string
	message  string
	fields   string
}

func TestP015HookSafeLoggingASTManifest(t *testing.T) {
	p015ValidateHookProductionOwnership(t)
	hookSink := func(function, message string, fields ...string) p015HookSinkManifestEntry {
		for index := range fields {
			fields[index] = p015CompactGo(fields[index])
		}
		return p015HookSinkManifestEntry{
			file: "hooks.go", function: function, message: message,
			fields: strings.Join(fields, ";"),
		}
	}
	processSink := func(function, message string, fields ...string) p015HookSinkManifestEntry {
		for index := range fields {
			fields[index] = p015CompactGo(fields[index])
		}
		return p015HookSinkManifestEntry{
			file: "hook_process.go", function: function, message: message,
			fields: strings.Join(fields, ";"),
		}
	}
	expected := []p015HookSinkManifestEntry{
		hookSink(
			"NewHookManager", "DiagnosticMessageHookEventSubscribeFailed",
			"safeHookErrorField(logger.ErrorClassInternal, err)",
		),
		hookSink(
			"HookManager.Close", "DiagnosticMessageHookEventSubscriptionCloseFailed",
			"safeHookErrorField(logger.ErrorClassInternal, err)",
		),
		hookSink(
			"HookManager.logUntrustedMutation",
			"DiagnosticMessageHookUntrustedMutationDiscarded",
			"safeHookIdentityField(reg.Name)",
			"safeHookSourceField(reg.Source)",
			"safeHookStageField(stage)",
			"safeHookActionField(action)",
		),
		hookSink(
			"HookManager.BeforeLLM", "DiagnosticMessageHookBeforeLLMRequestInvalid",
			"safeHookErrorField(logger.ErrorClassValidation, err)",
		),
		hookSink(
			"HookManager.BeforeLLM", "DiagnosticMessageHookBeforeLLMInputInvalid",
			"safeHookIdentityField(reg.Name)",
			"safeHookErrorField(logger.ErrorClassValidation, cloneErr)",
		),
		hookSink(
			"HookManager.BeforeLLM", "DiagnosticMessageHookBeforeLLMMutationInvalid",
			"safeHookIdentityField(reg.Name)",
			"safeHookErrorField(logger.ErrorClassValidation, detachErr)",
		),
		hookSink(
			"HookManager.AfterLLM", "DiagnosticMessageHookAfterLLMResponseInvalid",
			"safeHookErrorField(logger.ErrorClassValidation, err)",
		),
		hookSink(
			"HookManager.AfterLLM", "DiagnosticMessageHookAfterLLMInputInvalid",
			"safeHookIdentityField(reg.Name)",
			"safeHookErrorField(logger.ErrorClassValidation, cloneErr)",
		),
		hookSink(
			"HookManager.AfterLLM", "DiagnosticMessageHookAfterLLMMutationInvalid",
			"safeHookIdentityField(reg.Name)",
			"safeHookErrorField(logger.ErrorClassValidation, detachErr)",
		),
		hookSink(
			"HookManager.applyBeforeLLMControls",
			"DiagnosticMessageHookSystemPromptMutationRejected",
			"safeHookIdentityField(hookName)",
		),
		hookSink(
			"HookManager.applyBeforeLLMControls",
			"DiagnosticMessageHookToolDefinitionsMutationRejected",
			"safeHookIdentityField(hookName)",
		),
		hookSink(
			"HookManager.AfterTool", "DiagnosticMessageHookAfterToolResultInvalid",
			"safeHookErrorField(logger.ErrorClassValidation, err)",
		),
		hookSink(
			"HookManager.AfterTool", "DiagnosticMessageHookAfterToolInputInvalid",
			"safeHookIdentityField(reg.Name)",
			"safeHookErrorField(logger.ErrorClassValidation, cloneErr)",
		),
		hookSink(
			"HookManager.AfterTool", "DiagnosticMessageHookAfterToolMutationInvalid",
			"safeHookIdentityField(reg.Name)",
			"safeHookErrorField(logger.ErrorClassValidation, detachErr)",
		),
		hookSink(
			"HookManager.runRuntimeObserver", "DiagnosticMessageHookRuntimeObserverFailed",
			"safeHookIdentityField(name)",
			"safeHookEventKindField(evt.Kind)",
			"safeHookErrorField(logger.ErrorClassUnknown, err)",
		),
		hookSink(
			"HookManager.runRuntimeObserver", "DiagnosticMessageHookRuntimeObserverTimedOut",
			"safeHookIdentityField(name)",
			"safeHookEventKindField(evt.Kind)",
			"safeHookTimeoutField(observerTimeout)",
		),
		hookSink(
			"runInterceptorHook", "DiagnosticMessageHookInterceptorFailed",
			"safeHookIdentityField(name)",
			"safeHookStageField(stage)",
			"safeHookErrorField(logger.ErrorClassUnknown, res.err)",
		),
		hookSink(
			"runInterceptorHook", "DiagnosticMessageHookInterceptorTimedOut",
			"safeHookIdentityField(name)",
			"safeHookStageField(stage)",
			"safeHookTimeoutField(timeout)",
		),
		hookSink(
			"runApprovalHook", "DiagnosticMessageHookApprovalFailed",
			"safeHookIdentityField(name)",
			"safeHookStageField(stage)",
			"safeHookErrorField(logger.ErrorClassUnknown, res.err)",
		),
		hookSink(
			"runApprovalHook", "DiagnosticMessageHookApprovalTimedOut",
			"safeHookIdentityField(name)",
			"safeHookStageField(stage)",
			"safeHookTimeoutField(timeout)",
		),
		hookSink(
			"HookManager.logUnsupportedAction", "DiagnosticMessageHookUnsupportedAction",
			"safeHookIdentityField(name)",
			"safeHookStageField(stage)",
			"safeHookActionField(action)",
		),
		hookSink(
			"closeHookIfPossible", "DiagnosticMessageHookCloseFailed",
			"safeHookErrorField(logger.ErrorClassUnknown, err)",
		),
		processSink(
			"ProcessHook.readLoop", "DiagnosticMessageHookDecodeFailed",
			"safeHookIdentityField(ph.name)",
			"safeHookMessageField(scanner.Bytes())",
			"safeHookErrorField(logger.ErrorClassValidation, err)",
		),
		processSink(
			"ProcessHook.readStderr", "DiagnosticMessageProcessHookStderr",
			"safeHookIdentityField(ph.name)",
			"safeProcessStderrField(scanner.Bytes())",
		),
	}

	want := make(map[string]int, len(expected))
	for _, entry := range expected {
		want[p015HookSinkKey(entry.file, entry.function, entry.message, entry.fields)]++
	}
	got := make(map[string]int, len(expected))

	for _, fileName := range []string{"hooks.go", "hook_process.go"} {
		fileSet := token.NewFileSet()
		parsed, err := parser.ParseFile(fileSet, fileName, nil, parser.AllErrors)
		if err != nil {
			t.Fatalf("parse %s: %v", fileName, err)
		}
		p015ValidateHookLoggerSurface(t, fileSet, parsed, fileName)
		p015ValidateHookFieldHelpers(t, fileSet, parsed, fileName)
		for _, declaration := range parsed.Decls {
			function, ok := declaration.(*ast.FuncDecl)
			if !ok || function.Body == nil {
				continue
			}
			functionName := p015HookFunctionName(function)
			ast.Inspect(function.Body, func(node ast.Node) bool {
				call, ok := node.(*ast.CallExpr)
				if !ok {
					return true
				}
				packageName, callName, ok := p015SelectorCall(call)
				if !ok || packageName != "logger" {
					return true
				}
				position := fileSet.Position(call.Pos())
				if p015ForbiddenHookLoggerCall(callName) {
					t.Errorf("%s: forbidden hook logger call logger.%s", position, callName)
					return true
				}
				if callName != "WarnSafeCF" {
					return true
				}

				message, fields, valid := p015ValidateHookSafeSink(t, fileSet, call)
				if valid {
					got[p015HookSinkKey(fileName, functionName, message, fields)]++
				}
				return true
			})
		}
	}

	if diff := p015HookSinkManifestDiff(want, got); diff != "" {
		t.Fatalf("hook safe sink manifest mismatch (-want +got):\n%s", diff)
	}
}

func TestP015HookASTEscapeClassifiers(t *testing.T) {
	for expression, want := range map[string]bool{
		`logger.WarnSafeCF(component, message, fields)`: true,
		`new(logger.Logger).Warn(secret)`:               true,
		`(&logger.Logger{}).Warn(secret)`:               true,
		`other.Warn(secret)`:                            false,
	} {
		parsed, err := parser.ParseExpr(expression)
		if err != nil {
			t.Fatalf("parse %q: %v", expression, err)
		}
		call, ok := parsed.(*ast.CallExpr)
		if !ok {
			t.Fatalf("%q parsed as %T", expression, parsed)
		}
		if got := p015ExpressionRootedAtLogger(call.Fun); got != want {
			t.Fatalf("logger root for %q = %v, want %v", expression, got, want)
		}
	}
	if !p015ForbiddenHookLoggerSelector("Logger") {
		t.Fatal("logger.Logger facade type is not forbidden")
	}

	file, err := parser.ParseFile(
		token.NewFileSet(),
		"receiver.go",
		"package agent; func (hm (*HookManager)) escape() {}",
		parser.AllErrors,
	)
	if err != nil {
		t.Fatalf("parse parenthesized receiver: %v", err)
	}
	function := file.Decls[0].(*ast.FuncDecl)
	if got := p015HookReceiverName(function); got != "HookManager" {
		t.Fatalf("parenthesized receiver = %q, want HookManager", got)
	}
}

func p015ValidateHookSafeSink(
	t *testing.T,
	fileSet *token.FileSet,
	call *ast.CallExpr,
) (string, string, bool) {
	t.Helper()
	position := fileSet.Position(call.Pos())
	if len(call.Args) != 3 {
		t.Errorf("%s: WarnSafeCF argument count = %d; want 3", position, len(call.Args))
		return "", "", false
	}
	if !p015LoggerSelector(call.Args[0], "ComponentHooks") {
		t.Errorf("%s: hook sink component is not fixed logger.ComponentHooks", position)
		return "", "", false
	}
	message, ok := p015LoggerSelectorName(call.Args[1])
	if !ok || !strings.HasPrefix(message, "DiagnosticMessageHook") &&
		message != "DiagnosticMessageProcessHookStderr" {
		t.Errorf("%s: hook sink message is not a fixed hook diagnostic ID", position)
		return "", "", false
	}

	fields, ok := call.Args[2].(*ast.CallExpr)
	if !ok || !p015IsLoggerCall(fields, "NewSafeFields") {
		t.Errorf("%s: hook sink fields are not direct logger.NewSafeFields", position)
		return "", "", false
	}
	allowedFields := map[string]bool{
		"safeHookActionField":    true,
		"safeHookErrorField":     true,
		"safeHookEventKindField": true,
		"safeHookIdentityField":  true,
		"safeHookMessageField":   true,
		"safeHookSourceField":    true,
		"safeHookStageField":     true,
		"safeHookTimeoutField":   true,
		"safeProcessStderrField": true,
	}
	fieldExpressions := make([]string, 0, len(fields.Args))
	for _, argument := range fields.Args {
		fieldCall, ok := argument.(*ast.CallExpr)
		if !ok {
			t.Errorf("%s: hook sink contains a non-constructor field", position)
			return "", "", false
		}
		identifier, ok := fieldCall.Fun.(*ast.Ident)
		if !ok || !allowedFields[identifier.Name] {
			t.Errorf("%s: hook sink contains unapproved field constructor", position)
			return "", "", false
		}
		var rendered bytes.Buffer
		if err := format.Node(&rendered, fileSet, argument); err != nil {
			t.Errorf("%s: format hook field: %v", position, err)
			return "", "", false
		}
		fieldExpressions = append(fieldExpressions, p015CompactGo(rendered.String()))
	}

	unsafe := false
	ast.Inspect(call.Args[2], func(node ast.Node) bool {
		switch value := node.(type) {
		case *ast.MapType:
			t.Errorf("%s: hook sink constructs a dynamic map", fileSet.Position(value.Pos()))
			unsafe = true
		case *ast.BasicLit:
			if value.Kind == token.STRING {
				t.Errorf("%s: hook sink contains a raw string literal", fileSet.Position(value.Pos()))
				unsafe = true
			}
		case *ast.CallExpr:
			packageName, method, selector := p015SelectorCall(value)
			if selector && (method == "Error" || method == "Unwrap" ||
				method == "Is" || method == "As") {
				t.Errorf("%s: hook sink invokes arbitrary error method %s", fileSet.Position(value.Pos()), method)
				unsafe = true
			}
			if selector && (packageName == "fmt" || packageName == "debug" && method == "Stack") {
				t.Errorf("%s: hook sink formats a dynamic value or captures a stack", fileSet.Position(value.Pos()))
				unsafe = true
			}
		}
		return true
	})
	return message, strings.Join(fieldExpressions, ";"), !unsafe
}

func p015ValidateHookLoggerSurface(
	t *testing.T,
	fileSet *token.FileSet,
	file *ast.File,
	fileName string,
) {
	t.Helper()
	loggerImports := 0
	for _, imported := range file.Imports {
		importPath, err := strconv.Unquote(imported.Path.Value)
		if err != nil {
			t.Errorf("%s: invalid import path %s", fileName, imported.Path.Value)
			continue
		}
		localName := filepath.Base(importPath)
		if imported.Name != nil {
			localName = imported.Name.Name
		}
		if imported.Name != nil && imported.Name.Name == "." {
			t.Errorf("%s: dot import %s is forbidden", fileName, imported.Path.Value)
		}
		if importPath == "github.com/rs/zerolog" ||
			strings.HasPrefix(importPath, "github.com/rs/zerolog/") {
			t.Errorf("%s: alternate logging import %s is forbidden", fileName, imported.Path.Value)
			continue
		}
		switch importPath {
		case "github.com/sipeed/picoclaw/pkg/logger":
			loggerImports++
			if imported.Name != nil {
				t.Errorf("%s: logger import must be direct and unaliased", fileName)
			}
		case "fmt", "io", "os", "runtime":
			if imported.Name != nil {
				t.Errorf("%s: monitored import %q must be direct and unaliased", fileName, importPath)
			}
		case "runtime/debug", "log", "log/slog":
			t.Errorf("%s: alternate logging import %s is forbidden", fileName, imported.Path.Value)
		default:
			if p015ProtectedHookName(localName) {
				t.Errorf(
					"%s: import %q shadows monitored name %q",
					fileName,
					importPath,
					localName,
				)
			}
		}
	}
	if loggerImports != 1 {
		t.Errorf("%s: direct logger imports = %d, want 1", fileName, loggerImports)
	}
	p015ValidateHookProtectedNames(t, fileSet, file, fileName)
	p015ValidateHookOutputSurface(t, fileSet, file)

	warnSafeSelectors := 0
	ast.Inspect(file, func(node ast.Node) bool {
		switch value := node.(type) {
		case *ast.SelectorExpr:
			identifier, direct := value.X.(*ast.Ident)
			if !direct || identifier.Name != "logger" {
				return true
			}
			if value.Sel.Name == "WarnSafeCF" {
				warnSafeSelectors++
			}
			if p015ForbiddenHookLoggerSelector(value.Sel.Name) {
				t.Errorf(
					"%s: forbidden logger selector logger.%s",
					fileSet.Position(value.Pos()),
					value.Sel.Name,
				)
			}
		case *ast.CallExpr:
			if !p015ExpressionRootedAtLogger(value.Fun) {
				return true
			}
			directLogger := false
			selector, direct := value.Fun.(*ast.SelectorExpr)
			if direct {
				name, ok := selector.X.(*ast.Ident)
				directLogger = ok && name.Name == "logger"
			}
			if !directLogger {
				t.Errorf(
					"%s: chained logger facade call is forbidden",
					fileSet.Position(value.Pos()),
				)
			}
		}
		return true
	})
	wantWarnSafe := 22
	if fileName == "hook_process.go" {
		wantWarnSafe = 2
	}
	if warnSafeSelectors != wantWarnSafe {
		t.Errorf(
			"%s: logger.WarnSafeCF selector count = %d, want %d direct sinks",
			fileName,
			warnSafeSelectors,
			wantWarnSafe,
		)
	}
}

func p015ForbiddenHookLoggerSelector(name string) bool {
	return name == "Logger" || name == "NewLogger" || p015ForbiddenHookLoggerCall(name)
}

func p015ValidateHookOutputSurface(
	t *testing.T,
	fileSet *token.FileSet,
	file *ast.File,
) {
	t.Helper()
	directCalls := make(map[ast.Expr]struct{})
	ast.Inspect(file, func(node ast.Node) bool {
		if call, ok := node.(*ast.CallExpr); ok {
			directCalls[call.Fun] = struct{}{}
		}
		return true
	})
	ast.Inspect(file, func(node ast.Node) bool {
		switch value := node.(type) {
		case *ast.CallExpr:
			if identifier, ok := value.Fun.(*ast.Ident); ok &&
				(identifier.Name == "print" || identifier.Name == "println") {
				t.Errorf(
					"%s: builtin %s output is forbidden",
					fileSet.Position(value.Pos()),
					identifier.Name,
				)
			}
		case *ast.SelectorExpr:
			owner, direct := value.X.(*ast.Ident)
			if !direct {
				return true
			}
			forbidden := false
			switch owner.Name {
			case "fmt":
				forbidden = p015FmtOutputFunction(value.Sel.Name)
			case "os":
				forbidden = value.Sel.Name == "Stdout" || value.Sel.Name == "Stderr"
			case "io":
				forbidden = value.Sel.Name == "WriteString"
			case "runtime", "debug":
				forbidden = value.Sel.Name == "Stack" || value.Sel.Name == "PrintStack"
			}
			if forbidden {
				t.Errorf(
					"%s: output bypass %s.%s is forbidden",
					fileSet.Position(value.Pos()),
					owner.Name,
					value.Sel.Name,
				)
			}
			if p015OutputFunctionSelector(owner.Name, value.Sel.Name) {
				if _, calledDirectly := directCalls[value]; !calledDirectly {
					t.Errorf(
						"%s: output function %s.%s cannot be used indirectly",
						fileSet.Position(value.Pos()),
						owner.Name,
						value.Sel.Name,
					)
				}
			}
		}
		return true
	})
}

func p015FmtOutputFunction(name string) bool {
	switch name {
	case "Print", "Printf", "Println", "Fprint", "Fprintf", "Fprintln":
		return true
	default:
		return false
	}
}

func p015OutputFunctionSelector(owner, name string) bool {
	if owner == "fmt" {
		return p015FmtOutputFunction(name)
	}
	if owner == "io" && name == "WriteString" ||
		(owner == "runtime" || owner == "debug") &&
			(name == "Stack" || name == "PrintStack") {
		return true
	}
	if owner != "logger" {
		return false
	}
	switch name {
	case "SafeObservation", "ObserveIdentity", "SafeEnum", "ObserveErrorType",
		"SafeInt64", "ObserveBytes", "NewSafeFields", "WarnSafeCF", "NewLogger":
		return true
	default:
		return p015ForbiddenHookLoggerCall(name)
	}
}

func p015ValidateHookProtectedNames(
	t *testing.T,
	fileSet *token.FileSet,
	file *ast.File,
	fileName string,
) {
	t.Helper()
	check := func(identifier *ast.Ident, canonicalHelper bool) {
		if identifier == nil || !p015ProtectedHookName(identifier.Name) || canonicalHelper {
			return
		}
		t.Errorf(
			"%s: protected logger/import/helper name %q is shadowed",
			fileSet.Position(identifier.Pos()),
			identifier.Name,
		)
	}
	for _, imported := range file.Imports {
		if imported.Name != nil {
			check(imported.Name, false)
		}
	}
	ast.Inspect(file, func(node ast.Node) bool {
		switch value := node.(type) {
		case *ast.FuncDecl:
			_, canonical := p015HookFieldHelperBodies(fileName)[value.Name.Name]
			check(value.Name, canonical)
			p015CheckProtectedFieldNames(value.Recv, check)
			p015CheckProtectedFieldNames(value.Type.Params, check)
			p015CheckProtectedFieldNames(value.Type.Results, check)
		case *ast.FuncLit:
			p015CheckProtectedFieldNames(value.Type.Params, check)
			p015CheckProtectedFieldNames(value.Type.Results, check)
		case *ast.AssignStmt:
			if value.Tok == token.DEFINE {
				for _, left := range value.Lhs {
					identifier, _ := left.(*ast.Ident)
					check(identifier, false)
				}
			}
		case *ast.RangeStmt:
			if value.Tok == token.DEFINE {
				identifier, _ := value.Key.(*ast.Ident)
				check(identifier, false)
				identifier, _ = value.Value.(*ast.Ident)
				check(identifier, false)
			}
		case *ast.ValueSpec:
			for _, identifier := range value.Names {
				check(identifier, false)
			}
		case *ast.TypeSpec:
			check(value.Name, false)
		}
		return true
	})
}

func p015ProtectedHookName(name string) bool {
	switch name {
	case "logger", "fmt", "os", "io", "runtime", "debug", "log", "zerolog",
		"print", "println",
		"safeHookActionField", "safeHookErrorField", "safeHookEventKindField",
		"safeHookIdentityField", "safeHookMessageField", "safeHookSourceField",
		"safeHookStageField", "safeHookTimeoutField", "safeProcessStderrField":
		return true
	default:
		return false
	}
}

func p015CheckProtectedFieldNames(
	fields *ast.FieldList,
	check func(*ast.Ident, bool),
) {
	if fields == nil {
		return
	}
	for _, field := range fields.List {
		for _, identifier := range field.Names {
			check(identifier, false)
		}
	}
}

func p015ExpressionRootedAtLogger(expression ast.Expr) bool {
	switch value := expression.(type) {
	case *ast.Ident:
		return value.Name == "logger"
	case *ast.SelectorExpr:
		return p015ExpressionRootedAtLogger(value.X)
	case *ast.CallExpr:
		if p015ExpressionRootedAtLogger(value.Fun) {
			return true
		}
		for _, argument := range value.Args {
			if p015ExpressionRootedAtLogger(argument) {
				return true
			}
		}
		return false
	case *ast.ParenExpr:
		return p015ExpressionRootedAtLogger(value.X)
	case *ast.UnaryExpr:
		return p015ExpressionRootedAtLogger(value.X)
	case *ast.StarExpr:
		return p015ExpressionRootedAtLogger(value.X)
	case *ast.CompositeLit:
		if p015ExpressionRootedAtLogger(value.Type) {
			return true
		}
		for _, element := range value.Elts {
			if p015ExpressionRootedAtLogger(element) {
				return true
			}
		}
		return false
	case *ast.KeyValueExpr:
		return p015ExpressionRootedAtLogger(value.Key) ||
			p015ExpressionRootedAtLogger(value.Value)
	case *ast.IndexExpr:
		return p015ExpressionRootedAtLogger(value.X)
	case *ast.IndexListExpr:
		return p015ExpressionRootedAtLogger(value.X)
	default:
		return false
	}
}

func p015ValidateHookFieldHelpers(
	t *testing.T,
	fileSet *token.FileSet,
	file *ast.File,
	fileName string,
) {
	t.Helper()
	expected := p015HookFieldHelperBodies(fileName)
	seen := make(map[string]bool, len(expected))
	for _, declaration := range file.Decls {
		function, ok := declaration.(*ast.FuncDecl)
		if !ok || function.Body == nil {
			continue
		}
		name := function.Name.Name
		want, manifested := expected[name]
		isHookFieldHelper := strings.HasPrefix(name, "safeHook") ||
			name == "safeProcessStderrField"
		if !manifested {
			if isHookFieldHelper {
				t.Errorf("%s: unmanifested hook logging helper %s", fileName, name)
			}
			continue
		}
		seen[name] = true
		var rendered bytes.Buffer
		if err := format.Node(&rendered, fileSet, function.Body); err != nil {
			t.Errorf("%s:%s: format helper body: %v", fileName, name, err)
			continue
		}
		if p015CompactGo(rendered.String()) != p015CompactGo(want) {
			t.Errorf(
				"%s:%s helper body changed\n got: %s\nwant: %s",
				fileName,
				name,
				rendered.String(),
				want,
			)
		}
	}
	for name := range expected {
		if !seen[name] {
			t.Errorf("%s: manifested hook logging helper %s is missing", fileName, name)
		}
	}
}

func p015CompactGo(value string) string {
	return strings.Map(func(character rune) rune {
		if unicode.IsSpace(character) {
			return -1
		}
		return character
	}, value)
}

func p015HookFieldHelperBodies(fileName string) map[string]string {
	if fileName == "hook_process.go" {
		return map[string]string{
			"safeHookMessageField": `{
	return logger.SafeObservation(
		logger.ObservationPrefixHookMessage,
		logger.ObserveBytes(logger.ObservationDomainHookMessage, message),
	)
}`,
			"safeProcessStderrField": `{
	return logger.SafeObservation(
		logger.ObservationPrefixProcessStderr,
		logger.ObserveBytes(logger.ObservationDomainProcessStderr, stderr),
	)
}`,
		}
	}
	return map[string]string{
		"safeHookIdentityField": `{
	return logger.SafeObservation(
		logger.ObservationPrefixIdentityHook,
		logger.ObserveIdentity(logger.ObservationDomainIdentityHook, name),
	)
}`,
		"safeHookStageField": `{
	return logger.SafeObservation(
		logger.ObservationPrefixIdentityHookStage,
		logger.ObserveIdentity(logger.ObservationDomainIdentityHookStage, stage),
	)
}`,
		"safeHookActionField": `{
	return logger.SafeObservation(
		logger.ObservationPrefixIdentityHookAction,
		logger.ObserveIdentity(logger.ObservationDomainIdentityHookAction, string(action)),
	)
}`,
		"safeHookEventKindField": `{
	return logger.SafeObservation(
		logger.ObservationPrefixIdentityRuntimeEventKind,
		logger.ObserveIdentity(logger.ObservationDomainIdentityRuntimeEventKind, string(kind)),
	)
}`,
		"safeHookSourceField": `{
	value := logger.SafeEnumUnknown
	switch source {
	case HookSourceInProcess:
		value = logger.SafeEnumInProcess
	case HookSourceProcess:
		value = logger.SafeEnumProcess
	}
	return logger.SafeEnum(logger.FieldSource, value)
}`,
		"safeHookErrorField": `{
	return logger.SafeObservation(
		logger.ObservationPrefixError,
		logger.ObserveErrorType(class, err),
	)
}`,
		"safeHookTimeoutField": `{
	return logger.SafeInt64(logger.FieldTimeoutMilliseconds, int64(timeout/time.Millisecond))
}`,
	}
}

func p015HookFunctionName(function *ast.FuncDecl) string {
	if function.Recv == nil || len(function.Recv.List) == 0 {
		return function.Name.Name
	}
	receiver := function.Recv.List[0].Type
	if pointer, ok := receiver.(*ast.StarExpr); ok {
		receiver = pointer.X
	}
	identifier, ok := receiver.(*ast.Ident)
	if !ok {
		return "<unknown>." + function.Name.Name
	}
	return identifier.Name + "." + function.Name.Name
}

func p015ValidateHookProductionOwnership(t *testing.T) {
	t.Helper()
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("read agent package: %v", err)
	}
	for _, entry := range entries {
		fileName := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(fileName, ".go") ||
			strings.HasSuffix(fileName, "_test.go") {
			continue
		}
		fileSet := token.NewFileSet()
		file, parseErr := parser.ParseFile(fileSet, fileName, nil, parser.AllErrors)
		if parseErr != nil {
			t.Errorf("parse production agent file %s: %v", fileName, parseErr)
			continue
		}
		for _, declaration := range file.Decls {
			function, ok := declaration.(*ast.FuncDecl)
			if !ok {
				continue
			}
			receiver := p015HookReceiverName(function)
			if receiver == "HookManager" && fileName != "hooks.go" {
				t.Errorf("%s: HookManager method %s is outside hooks.go", fileName, function.Name.Name)
			}
			if receiver == "ProcessHook" && fileName != "hook_process.go" {
				t.Errorf("%s: ProcessHook method %s is outside hook_process.go", fileName, function.Name.Name)
			}
			name := function.Name.Name
			if !strings.HasPrefix(name, "safeHook") && name != "safeProcessStderrField" {
				continue
			}
			_, canonical := p015HookFieldHelperBodies(fileName)[name]
			if !canonical {
				t.Errorf("%s: hook logging helper %s is outside its canonical file", fileName, name)
			}
		}
	}
}

func p015HookReceiverName(function *ast.FuncDecl) string {
	if function.Recv == nil || len(function.Recv.List) != 1 {
		return ""
	}
	return p015HookReceiverTypeName(function.Recv.List[0].Type)
}

func p015HookReceiverTypeName(receiver ast.Expr) string {
	switch value := receiver.(type) {
	case *ast.Ident:
		return value.Name
	case *ast.StarExpr:
		return p015HookReceiverTypeName(value.X)
	case *ast.ParenExpr:
		return p015HookReceiverTypeName(value.X)
	default:
		return ""
	}
}

func p015SelectorCall(call *ast.CallExpr) (string, string, bool) {
	selector, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return "", "", false
	}
	identifier, ok := selector.X.(*ast.Ident)
	if !ok {
		return "", selector.Sel.Name, true
	}
	return identifier.Name, selector.Sel.Name, true
}

func p015IsLoggerCall(call *ast.CallExpr, name string) bool {
	packageName, callName, ok := p015SelectorCall(call)
	return ok && packageName == "logger" && callName == name
}

func p015LoggerSelector(expression ast.Expr, name string) bool {
	got, ok := p015LoggerSelectorName(expression)
	return ok && got == name
}

func p015LoggerSelectorName(expression ast.Expr) (string, bool) {
	selector, ok := expression.(*ast.SelectorExpr)
	if !ok {
		return "", false
	}
	identifier, ok := selector.X.(*ast.Ident)
	return selector.Sel.Name, ok && identifier.Name == "logger"
}

func p015ForbiddenHookLoggerCall(name string) bool {
	if name == "DebugSensitiveCF" || name == "RecoverPanic" || name == "RecoverPanicNoExit" {
		return true
	}
	for _, prefix := range []string{"Debug", "Info", "Warn", "Error", "Fatal"} {
		if name == prefix || name == prefix+"C" || name == prefix+"F" ||
			name == prefix+"CF" || name == prefix+"f" {
			return name != "WarnSafeCF"
		}
	}
	if strings.HasSuffix(name, "SafeCF") {
		return name != "WarnSafeCF"
	}
	return false
}

func p015HookSinkKey(file, function, message, fields string) string {
	return file + "|" + function + "|" + message + "|" + fields
}

func p015HookSinkManifestDiff(want, got map[string]int) string {
	keys := make(map[string]struct{}, len(want)+len(got))
	for key := range want {
		keys[key] = struct{}{}
	}
	for key := range got {
		keys[key] = struct{}{}
	}
	ordered := make([]string, 0, len(keys))
	for key := range keys {
		ordered = append(ordered, key)
	}
	sort.Strings(ordered)

	var diff strings.Builder
	for _, key := range ordered {
		if want[key] == got[key] {
			continue
		}
		if want[key] != 0 {
			fmt.Fprintf(&diff, "-%d %s\n", want[key], key)
		}
		if got[key] != 0 {
			fmt.Fprintf(&diff, "+%d %s\n", got[key], key)
		}
	}
	return diff.String()
}
