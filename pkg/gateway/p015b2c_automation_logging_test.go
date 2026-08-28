package gateway

import (
	"bytes"
	"go/ast"
	"go/format"
	"go/parser"
	"go/token"
	"path/filepath"
	"reflect"
	"runtime"
	"strconv"
	"strings"
	"testing"
)

type p015B2CAutomationSink struct {
	file      string
	callee    string
	component string
	message   string
	fields    []string
}

func TestP015B2CAutomationLoggingASTManifest(t *testing.T) {
	want := []p015B2CAutomationSink{
		{
			file: "event_automation.go", callee: "WarnSafeCF",
			component: "ComponentEventing",
			message:   "DiagnosticMessageEventingPRWorkspaceLocalCIIsUnavailable",
			fields: []string{
				"gatewayDiagnosticErrorField(logger.ErrorClassInternal, err)",
			},
		},
		{
			file: "event_automation.go", callee: "WarnSafeCF",
			component: "ComponentEventing",
			message:   "DiagnosticMessageEventingPRWorkspaceImplementationIsUnavailable",
			fields: []string{
				"gatewayDiagnosticErrorField(logger.ErrorClassInternal, err)",
			},
		},
		{
			file: "event_automation.go", callee: "WarnSafeCF",
			component: "ComponentEventing",
			message:   "DiagnosticMessageEventingEventRetentionMaintenanceFailed",
			fields: []string{
				"gatewayDiagnosticErrorField(logger.ErrorClassInternal, err)",
			},
		},
		{
			file: "event_automation.go", callee: "WarnSafeCF",
			component: "ComponentEventing",
			message:   "DiagnosticMessageEventingEventRetentionMaintenanceFailed",
			fields: []string{
				"gatewayDiagnosticErrorField(logger.ErrorClassInternal, err)",
			},
		},
		{
			file: "event_automation.go", callee: "DebugSafeCF",
			component: "ComponentEventing",
			message:   "DiagnosticMessageEventingPrunedExpiredDurableEvents",
			fields: []string{
				"logger.SafeInt(logger.FieldCount, int(pruned))",
			},
		},
		{
			file: "event_automation.go", callee: "WarnSafeCF",
			component: "ComponentEventing",
			message:   "DiagnosticMessageEventingGitHubNotificationPollingFailed",
			fields: []string{
				"gatewayDiagnosticErrorField(logger.ErrorClassInternal, err)",
			},
		},
		{
			file: "event_automation.go", callee: "DebugSafeCF",
			component: "ComponentEventing",
			message:   "DiagnosticMessageEventingStoredGitHubNotifications",
			fields: []string{
				"logger.SafeInt(logger.FieldNotificationCount, result.Notifications)",
				"logger.SafeInt(logger.FieldMatchedCount, result.Matched)",
				"logger.SafeInt(logger.FieldInsertedCount, result.Inserted)",
			},
		},
		{
			file: "event_automation.go", callee: "WarnSafeCF",
			component: "ComponentEventing",
			message:   "DiagnosticMessageEventingEventWorkflowWorkerIterationFailed",
			fields: []string{
				"gatewayDiagnosticWorkerField(name)",
				"gatewayDiagnosticErrorField(logger.ErrorClassInternal, err)",
			},
		},
		{
			file: "pr_workspace_implementation.go", callee: "ErrorSafeCF",
			component: "ComponentPRWorkspace",
			message:   "DiagnosticMessagePRWorkspaceRepairFailed",
			fields: []string{
				"gatewayDiagnosticWorkspaceField(request.Context.WorkspaceID)",
				"logger.SafeInt(logger.FieldAttempt, request.Attempt)",
				"gatewayDiagnosticErrorField(logger.ErrorClassInternal, repairErr)",
			},
		},
	}

	got := make([]p015B2CAutomationSink, 0, len(want))
	for _, file := range []string{"event_automation.go", "pr_workspace_implementation.go"} {
		got = append(got, p015B2CReadAutomationSinks(t, file)...)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("automation safe-sink manifest = %#v, want %#v", got, want)
	}
}

func p015B2CReadAutomationSinks(t *testing.T, file string) []p015B2CAutomationSink {
	t.Helper()
	_, current, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve automation manifest source path")
	}
	path := filepath.Join(filepath.Dir(current), file)
	fileSet := token.NewFileSet()
	parsed, err := parser.ParseFile(fileSet, path, nil, parser.AllErrors)
	if err != nil {
		t.Fatal(err)
	}
	p015B2CAutomationRequireDirectLoggerImport(t, file, parsed)

	var sinks []p015B2CAutomationSink
	ast.Inspect(parsed, func(node ast.Node) bool {
		call, ok := node.(*ast.CallExpr)
		if !ok {
			return true
		}
		selector, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || !p015B2CAutomationIdent(selector.X, "logger") {
			return true
		}
		callee := selector.Sel.Name
		if p015B2CAutomationLegacyEmitter(callee) {
			t.Errorf("%s retains legacy logger.%s at %s", file, callee, fileSet.Position(call.Pos()))
			return true
		}
		if !p015B2CAutomationSafeEmitter(callee) {
			return true
		}
		if call.Ellipsis.IsValid() || len(call.Args) != 3 {
			t.Errorf("%s logger.%s does not have exact direct arity", file, callee)
			return true
		}
		component := p015B2CAutomationLoggerSelector(call.Args[0], "Component")
		message := p015B2CAutomationLoggerSelector(call.Args[1], "DiagnosticMessage")
		fieldsCall, fieldsOK := call.Args[2].(*ast.CallExpr)
		if component == "" || message == "" || !fieldsOK || fieldsCall.Ellipsis.IsValid() ||
			!p015B2CAutomationSelector(fieldsCall.Fun, "logger", "NewSafeFields") {
			t.Errorf("%s logger.%s has an open component/message/fields shape", file, callee)
			return true
		}
		fields := make([]string, 0, len(fieldsCall.Args))
		for _, expression := range fieldsCall.Args {
			fields = append(fields, p015B2CAutomationRender(t, fileSet, expression))
		}
		sinks = append(sinks, p015B2CAutomationSink{
			file: file, callee: callee, component: component, message: message, fields: fields,
		})
		return true
	})
	return sinks
}

func p015B2CAutomationRequireDirectLoggerImport(t *testing.T, file string, parsed *ast.File) {
	t.Helper()
	count := 0
	for _, imported := range parsed.Imports {
		path, err := strconv.Unquote(imported.Path.Value)
		if err != nil {
			t.Fatalf("%s import: %v", file, err)
		}
		if path != "github.com/sipeed/picoclaw/pkg/logger" {
			continue
		}
		count++
		if imported.Name != nil {
			t.Errorf("%s aliases logger import as %q", file, imported.Name.Name)
		}
	}
	if count != 1 {
		t.Errorf("%s logger import count = %d, want 1", file, count)
	}
}

func p015B2CAutomationRender(t *testing.T, fileSet *token.FileSet, node ast.Node) string {
	t.Helper()
	var rendered bytes.Buffer
	if err := format.Node(&rendered, fileSet, node); err != nil {
		t.Fatal(err)
	}
	return rendered.String()
}

func p015B2CAutomationLoggerSelector(expression ast.Expr, prefix string) string {
	selector, ok := expression.(*ast.SelectorExpr)
	if !ok || !p015B2CAutomationIdent(selector.X, "logger") ||
		!strings.HasPrefix(selector.Sel.Name, prefix) {
		return ""
	}
	return selector.Sel.Name
}

func p015B2CAutomationSelector(expression ast.Expr, pkg, name string) bool {
	selector, ok := expression.(*ast.SelectorExpr)
	return ok && p015B2CAutomationIdent(selector.X, pkg) && selector.Sel.Name == name
}

func p015B2CAutomationIdent(expression ast.Expr, name string) bool {
	identifier, ok := expression.(*ast.Ident)
	return ok && identifier.Name == name
}

func p015B2CAutomationSafeEmitter(name string) bool {
	switch name {
	case "DebugSafeCF", "InfoSafeCF", "WarnSafeCF", "ErrorSafeCF", "FatalSafeCF":
		return true
	default:
		return false
	}
}

func p015B2CAutomationLegacyEmitter(name string) bool {
	for _, prefix := range []string{"Debug", "Info", "Warn", "Error", "Fatal", "RecoverPanic"} {
		if strings.HasPrefix(name, prefix) && !p015B2CAutomationSafeEmitter(name) {
			return true
		}
	}
	return false
}
