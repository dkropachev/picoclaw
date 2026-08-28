package agent

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"reflect"
	"strconv"
	"strings"
	"testing"
)

type p015B2BSinkCount struct {
	level     string
	component string
	count     int
}

type p015B2BFileManifest struct {
	sinks []p015B2BSinkCount
}

var p015B2BClosedDiagnosticFieldHelpers = map[string]struct{}{
	"agentDiagnosticAccountField":        {},
	"agentDiagnosticAgentField":          {},
	"agentDiagnosticChannelField":        {},
	"agentDiagnosticChatField":           {},
	"agentDiagnosticContextManagerField": {},
	"agentDiagnosticErrorField":          {},
	"agentDiagnosticLightModelField":     {},
	"agentDiagnosticMCPServerField":      {},
	"agentDiagnosticMediaRefField":       {},
	"agentDiagnosticModelField":          {},
	"agentDiagnosticPanicField":          {},
	"agentDiagnosticPathField":           {},
	"agentDiagnosticPromptLayerField":    {},
	"agentDiagnosticPromptPartField":     {},
	"agentDiagnosticPromptSlotField":     {},
	"agentDiagnosticPromptSourceField":   {},
	"agentDiagnosticProviderField":       {},
	"agentDiagnosticProviderModelField":  {},
	"agentDiagnosticReasonField":         {},
	"agentDiagnosticRegexField":          {},
	"agentDiagnosticRouteField":          {},
	"agentDiagnosticScopeField":          {},
	"agentDiagnosticSessionField":        {},
	"agentDiagnosticToolField":           {},
	"agentDiagnosticToolSurfaceField":    {},
	"agentDiagnosticTurnField":           {},
	"agentDiagnosticWorkflowField":       {},
	"agentDiagnosticWorkspaceField":      {},
}

func TestP015B2BClosedSafeSinkShapeRejectsEveryRawAdapterClass(t *testing.T) {
	valid := p015B2BParseCallExpression(t, `logger.WarnSafeCF(
		logger.ComponentAgent,
		logger.DiagnosticMessageEvent,
		logger.NewSafeFields(
			agentDiagnosticAgentField(agentID),
			logger.SafeInt(logger.FieldCount, len(items)),
		),
	)`)
	if _, issues := p015B2BSafeSinkShapeIssues(valid); len(issues) != 0 {
		t.Fatalf("closed direct SafeCF fixture rejected: %v", issues)
	}

	invalid := map[string]string{
		"component_adapter": `logger.WarnSafeCF(component(), logger.DiagnosticMessageEvent, logger.NewSafeFields())`,
		"message_adapter":   `logger.WarnSafeCF(logger.ComponentAgent, message(), logger.NewSafeFields())`,
		"fields_adapter": `logger.WarnSafeCF(
			logger.ComponentAgent, logger.DiagnosticMessageEvent, buildSafeFields(secret),
		)`,
		"unknown_helper": `logger.WarnSafeCF(
			logger.ComponentAgent, logger.DiagnosticMessageEvent,
			logger.NewSafeFields(agentDiagnosticFutureField(secret)),
		)`,
		"raw_format": `logger.WarnSafeCF(
			logger.ComponentAgent, logger.DiagnosticMessageEvent,
			logger.NewSafeFields(agentDiagnosticReasonField(fmt.Sprintf("%v", secret))),
		)`,
		"raw_error": `logger.WarnSafeCF(
			logger.ComponentAgent, logger.DiagnosticMessageEvent,
			logger.NewSafeFields(agentDiagnosticErrorField(logger.ErrorClassInternal, fmt.Errorf("%w", err))),
		)`,
		"raw_marshal": `logger.WarnSafeCF(
			logger.ComponentAgent, logger.DiagnosticMessageEvent,
			logger.NewSafeFields(agentDiagnosticReasonField(string(json.Marshal(secret)))),
		)`,
		"raw_map": `logger.WarnSafeCF(
			logger.ComponentAgent, logger.DiagnosticMessageEvent,
			logger.NewSafeFields(agentDiagnosticReasonField(map[string]any{"secret": secret})),
		)`,
		"raw_any": `logger.WarnSafeCF(
			logger.ComponentAgent, logger.DiagnosticMessageEvent,
			logger.NewSafeFields(agentDiagnosticPanicField(any(secret))),
		)`,
	}
	for name, source := range invalid {
		t.Run(name, func(t *testing.T) {
			call := p015B2BParseCallExpression(t, source)
			if _, issues := p015B2BSafeSinkShapeIssues(call); len(issues) == 0 {
				t.Fatal("raw/adapter sink shape was accepted")
			}
		})
	}
}

func p015B2BParseCallExpression(t *testing.T, source string) *ast.CallExpr {
	t.Helper()
	expression, err := parser.ParseExpr(source)
	if err != nil {
		t.Fatal(err)
	}
	call, ok := expression.(*ast.CallExpr)
	if !ok {
		t.Fatalf("fixture expression = %T, want call", expression)
	}
	return call
}

func p015B2BValidateLoggingPartition(
	t *testing.T,
	partition string,
	expected map[string]p015B2BFileManifest,
	wantTotal int,
) {
	t.Helper()
	gotTotal := 0
	for name, want := range expected {
		parsed := p015B2BParseLoggingFile(t, name)
		p015B2BRequireDirectLoggerImport(t, name, parsed)
		wantCounts := make(map[string]int, len(want.sinks))
		for _, sink := range want.sinks {
			key := p015B2BSinkKey(sink.level, sink.component)
			wantCounts[key] += sink.count
			gotTotal += sink.count
		}
		gotCounts := make(map[string]int, len(wantCounts))
		ast.Inspect(parsed, func(node ast.Node) bool {
			call, ok := node.(*ast.CallExpr)
			if !ok {
				return true
			}
			selector, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || !p015B2BIdent(selector.X, "logger") {
				return true
			}
			callee := selector.Sel.Name
			if p015B2BLegacyEmitter(callee) {
				t.Errorf("%s retains legacy logger.%s at byte %d", name, callee, call.Pos())
				return true
			}
			if callee == "DebugSensitiveCF" {
				t.Errorf("%s contains unplanned sensitive-preview sink at byte %d", name, call.Pos())
				return true
			}
			if !p015B2BSafeEmitter(callee) {
				return true
			}
			component := p015B2BValidateSafeSink(t, name, call)
			gotCounts[p015B2BSinkKey(callee, component)]++
			return true
		})
		if !reflect.DeepEqual(gotCounts, wantCounts) {
			t.Errorf("%s sink level/component manifest = %#v, want %#v", name, gotCounts, wantCounts)
		}
	}
	if gotTotal != wantTotal {
		t.Fatalf("%s declared sink total = %d, want exact %d", partition, gotTotal, wantTotal)
	}
}

func p015B2BParseLoggingFile(t *testing.T, name string) *ast.File {
	t.Helper()
	parsed, err := parser.ParseFile(token.NewFileSet(), name, nil, parser.AllErrors)
	if err != nil {
		t.Fatal(err)
	}
	return parsed
}

func p015B2BRequireDirectLoggerImport(t *testing.T, name string, parsed *ast.File) {
	t.Helper()
	count := 0
	for _, imported := range parsed.Imports {
		path, err := strconv.Unquote(imported.Path.Value)
		if err != nil {
			t.Fatalf("%s has invalid import: %v", name, err)
		}
		if path != "github.com/sipeed/picoclaw/pkg/logger" {
			continue
		}
		count++
		if imported.Name != nil {
			t.Errorf("%s aliases the closed logger import as %q", name, imported.Name.Name)
		}
	}
	if count != 1 {
		t.Errorf("%s direct logger import count = %d, want exactly 1", name, count)
	}
}

func p015B2BValidateSafeSink(t *testing.T, name string, call *ast.CallExpr) string {
	t.Helper()
	component, issues := p015B2BSafeSinkShapeIssues(call)
	for _, issue := range issues {
		t.Errorf("%s SafeCF %s at byte %d", name, issue, call.Pos())
	}
	return component
}

func p015B2BSafeSinkShapeIssues(call *ast.CallExpr) (string, []string) {
	var issues []string
	if call.Ellipsis.IsValid() || len(call.Args) != 3 {
		return "<invalid>", []string{"has non-direct arity"}
	}
	component := p015B2BLoggerSelectorName(call.Args[0], "Component")
	if component == "" {
		issues = append(issues, "component is not a direct logger.Component constant")
		component = "<invalid>"
	}
	if p015B2BLoggerSelectorName(call.Args[1], "DiagnosticMessage") == "" {
		issues = append(issues, "message is not a direct logger.DiagnosticMessage constant")
	}
	fields, ok := call.Args[2].(*ast.CallExpr)
	if !ok || !p015B2BSelector(fields.Fun, "logger", "NewSafeFields") || fields.Ellipsis.IsValid() {
		issues = append(issues, "fields are not a direct non-variadic logger.NewSafeFields call")
		return component, issues
	}
	for _, field := range fields.Args {
		if !p015B2BClosedFieldExpression(field) {
			issues = append(issues, fmt.Sprintf("uses a raw or adapter field expression at byte %d", field.Pos()))
		}
	}
	issues = append(issues, p015B2BRawSinkShapeIssues(fields)...)
	return component, issues
}

func p015B2BClosedFieldExpression(expression ast.Expr) bool {
	call, ok := expression.(*ast.CallExpr)
	if !ok || call.Ellipsis.IsValid() {
		return false
	}
	if identifier, identifierOK := call.Fun.(*ast.Ident); identifierOK {
		_, allowed := p015B2BClosedDiagnosticFieldHelpers[identifier.Name]
		return allowed
	}
	selector, ok := call.Fun.(*ast.SelectorExpr)
	if !ok || !p015B2BIdent(selector.X, "logger") {
		return false
	}
	switch selector.Sel.Name {
	case "SafeBool", "SafeEnum", "SafeFloat64", "SafeInt", "SafeInt64":
		return true
	default:
		return false
	}
}

func p015B2BRawSinkShapeIssues(root ast.Node) []string {
	var issues []string
	ast.Inspect(root, func(node ast.Node) bool {
		switch value := node.(type) {
		case *ast.MapType:
			issues = append(issues, fmt.Sprintf("contains raw map shape at byte %d", value.Pos()))
		case *ast.InterfaceType:
			issues = append(issues, fmt.Sprintf("contains raw interface shape at byte %d", value.Pos()))
		case *ast.CallExpr:
			if identifier, ok := value.Fun.(*ast.Ident); ok && identifier.Name == "any" {
				issues = append(issues, fmt.Sprintf("contains raw any conversion at byte %d", value.Pos()))
				return true
			}
			selector, ok := value.Fun.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			switch selector.Sel.Name {
			case "Error", "Errorf", "Format", "Marshal", "MarshalJSON", "Sprint", "Sprintf", "Sprintln", "String":
				issues = append(issues, fmt.Sprintf(
					"invokes raw formatting/error/marshal method %s at byte %d",
					selector.Sel.Name,
					value.Pos(),
				))
			}
		}
		return true
	})
	return issues
}

func p015B2BSinkKey(level, component string) string {
	return level + "\x00" + component
}

func p015B2BLoggerSelectorName(expression ast.Expr, prefix string) string {
	selector, ok := expression.(*ast.SelectorExpr)
	if !ok || !p015B2BIdent(selector.X, "logger") || !strings.HasPrefix(selector.Sel.Name, prefix) {
		return ""
	}
	return selector.Sel.Name
}

func p015B2BSelector(expression ast.Expr, pkg, name string) bool {
	selector, ok := expression.(*ast.SelectorExpr)
	return ok && p015B2BIdent(selector.X, pkg) && selector.Sel.Name == name
}

func p015B2BIdent(expression ast.Expr, name string) bool {
	identifier, ok := expression.(*ast.Ident)
	return ok && identifier.Name == name
}

func p015B2BSafeEmitter(name string) bool {
	switch name {
	case "DebugSafeCF", "InfoSafeCF", "WarnSafeCF", "ErrorSafeCF", "FatalSafeCF":
		return true
	default:
		return false
	}
}

func p015B2BLegacyEmitter(name string) bool {
	if name == "RecoverPanic" || name == "RecoverPanicNoExit" {
		return true
	}
	for _, prefix := range []string{"Debug", "Info", "Warn", "Error", "Fatal"} {
		if strings.HasPrefix(name, prefix) && !strings.HasSuffix(name, "SafeCF") &&
			name != "DebugSensitiveCF" {
			return true
		}
	}
	return false
}
