package gateway

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"sort"
	"strconv"
	"strings"
	"testing"
)

type p015B2CSinkKind uint8

const (
	p015B2CLoggerSink p015B2CSinkKind = iota + 1
	p015B2CConsoleSink
)

type p015B2CSinkDescriptor struct {
	ID          string
	File        string
	Owner       string
	Kind        p015B2CSinkKind
	Occurrence  int
	Level       string
	Component   string
	Message     string
	ConsoleSite string
}

type p015B2CDescriptorGroup struct {
	name           string
	descriptors    []p015B2CSinkDescriptor
	loggerTotal    int
	consoleTotal   int
	levelCounts    map[string]int
	componentCount map[string]int
}

type p015B2CActualSink struct {
	file        string
	owner       string
	kind        p015B2CSinkKind
	position    token.Pos
	level       string
	component   string
	message     string
	consoleSite string
}

var p015B2CClosedGatewayFieldHelpers = map[string]int{
	"gatewayDiagnosticErrorField":      2,
	"gatewayDiagnosticWorkerField":     1,
	"gatewayDiagnosticChannelField":    1,
	"gatewayDiagnosticModelField":      1,
	"gatewayDiagnosticProviderField":   1,
	"gatewayDiagnosticWorkspaceField":  1,
	"gatewayDiagnosticConfigPathField": 1,
	"gatewayDiagnosticHomePathField":   1,
	"gatewayDiagnosticLogLevelField":   1,
}

var p015B2CConsoleFieldConstructors = map[string]int{
	"newGatewayConsoleNoFields":  0,
	"newGatewayConsoleCount":     1,
	"newGatewayConsoleCountPair": 2,
	"newGatewayConsolePort":      1,
}

var p015B2CConsoleSites = map[string]string{
	"gatewayConsoleC001GatewayStarted":             "newGatewayConsolePort",
	"gatewayConsoleC002StopHint":                   "newGatewayConsoleNoFields",
	"gatewayConsoleC003DebugEnabled":               "newGatewayConsoleNoFields",
	"gatewayConsoleC004AgentStatus":                "newGatewayConsoleNoFields",
	"gatewayConsoleC005ToolsLoaded":                "newGatewayConsoleCount",
	"gatewayConsoleC006SkillsAvailable":            "newGatewayConsoleCountPair",
	"gatewayConsoleC007NoModelConfigured":          "newGatewayConsoleNoFields",
	"gatewayConsoleC008HeartbeatRestarted":         "newGatewayConsoleNoFields",
	"gatewayConsoleC009EventInboxReopened":         "newGatewayConsoleNoFields",
	"gatewayConsoleC010CronRestarted":              "newGatewayConsoleNoFields",
	"gatewayConsoleC011ChannelsRestarted":          "newGatewayConsoleNoFields",
	"gatewayConsoleC012RestartedChannelsEnabled":   "newGatewayConsoleCount",
	"gatewayConsoleC013NoRestartedChannelsEnabled": "newGatewayConsoleNoFields",
	"gatewayConsoleC014DeviceServiceRestarted":     "newGatewayConsoleNoFields",
	"gatewayConsoleC015EventWorkersRestarted":      "newGatewayConsoleNoFields",
	"gatewayConsoleC016CronStarted":                "newGatewayConsoleNoFields",
	"gatewayConsoleC017ChannelsEnabled":            "newGatewayConsoleCount",
	"gatewayConsoleC018NoChannelsEnabled":          "newGatewayConsoleNoFields",
	"gatewayConsoleC019HealthEndpointsAvailable":   "newGatewayConsolePort",
	"gatewayConsoleC020DeviceServiceStarted":       "newGatewayConsoleNoFields",
	"gatewayConsoleC021EventWorkersStarted":        "newGatewayConsoleNoFields",
	"gatewayConsoleC022EventInboxOpened":           "newGatewayConsoleNoFields",
	"gatewayConsoleC023HeartbeatStarted":           "newGatewayConsoleNoFields",
}

func TestP015B2CLoggingDescriptorUnionIsExact(t *testing.T) {
	descriptors := append(
		p015B2CStartupLoggingDescriptors(),
		p015B2CReloadLoggingDescriptors()...,
	)
	descriptors = append(descriptors, p015B2CShutdownLoggingDescriptors()...)
	for _, id := range []string{
		"G001", "G002", "G003", "G004", "G005",
		"G006", "G007", "G008", "G054",
	} {
		descriptors = append(descriptors, p015B2CSinkDescriptor{
			ID: id, Kind: p015B2CLoggerSink,
		})
	}
	p015B2CRequireDescriptorUnion(t, descriptors)
	if len(descriptors) != 77 {
		t.Fatalf("G/C descriptor union = %d, want 77", len(descriptors))
	}
	seen := make(map[string]struct{}, len(descriptors))
	for _, descriptor := range descriptors {
		seen[descriptor.ID] = struct{}{}
	}
	for _, cohort := range []struct {
		prefix string
		count  int
	}{{"G", 54}, {"C", 23}} {
		for index := 1; index <= cohort.count; index++ {
			id := fmt.Sprintf("%s%03d", cohort.prefix, index)
			if _, ok := seen[id]; !ok {
				t.Errorf("descriptor union is missing %s", id)
			}
		}
	}
}

func TestP015B2CClosedSinkShapesRejectRawAndAdapterInputs(t *testing.T) {
	validLogger := p015B2CParseCallExpression(t, `logger.WarnSafeCF(
		logger.ComponentGateway,
		logger.DiagnosticMessageGatewayStartupFailed,
		logger.NewSafeFields(
			gatewayDiagnosticErrorField(logger.ErrorClassInternal, err),
			logger.SafeBool(logger.FieldAllowEmpty, allowEmpty),
		),
	)`)
	if _, _, issues := p015B2CSafeSinkShapeIssues(validLogger); len(issues) != 0 {
		t.Fatalf("closed logger fixture rejected: %v", issues)
	}

	invalidLogger := map[string]string{
		"component adapter": `logger.WarnSafeCF(
			component(), logger.DiagnosticMessageGatewayStartupFailed,
			logger.NewSafeFields(),
		)`,
		"message adapter": `logger.WarnSafeCF(
			logger.ComponentGateway, message(), logger.NewSafeFields(),
		)`,
		"fields adapter": `logger.WarnSafeCF(
			logger.ComponentGateway, logger.DiagnosticMessageGatewayStartupFailed,
			buildFields(secret),
		)`,
		"unknown helper": `logger.WarnSafeCF(
			logger.ComponentGateway, logger.DiagnosticMessageGatewayStartupFailed,
			logger.NewSafeFields(gatewayDiagnosticFutureField(secret)),
		)`,
		"formatted error": `logger.WarnSafeCF(
			logger.ComponentGateway, logger.DiagnosticMessageGatewayStartupFailed,
			logger.NewSafeFields(
				gatewayDiagnosticErrorField(logger.ErrorClassInternal, fmt.Errorf("%w", err)),
			),
		)`,
		"Error method": `logger.WarnSafeCF(
			logger.ComponentGateway, logger.DiagnosticMessageGatewayStartupFailed,
			logger.NewSafeFields(gatewayDiagnosticModelField(err.Error())),
		)`,
		"String method": `logger.WarnSafeCF(
			logger.ComponentGateway, logger.DiagnosticMessageGatewayStartupFailed,
			logger.NewSafeFields(gatewayDiagnosticModelField(value.String())),
		)`,
		"map any": `logger.WarnSafeCF(
			logger.ComponentGateway, logger.DiagnosticMessageGatewayStartupFailed,
			logger.NewSafeFields(
				gatewayDiagnosticModelField(map[string]any{"secret": secret}),
			),
		)`,
		"any conversion": `logger.WarnSafeCF(
			logger.ComponentGateway, logger.DiagnosticMessageGatewayStartupFailed,
			logger.NewSafeFields(gatewayDiagnosticModelField(any(secret))),
		)`,
		"value adapter": `logger.WarnSafeCF(
			logger.ComponentGateway, logger.DiagnosticMessageGatewayStartupFailed,
			logger.NewSafeFields(gatewayDiagnosticModelField(normalize(secret))),
		)`,
		"scalar key adapter": `logger.WarnSafeCF(
			logger.ComponentGateway, logger.DiagnosticMessageGatewayStartupFailed,
			logger.NewSafeFields(logger.SafeInt(field(), count)),
		)`,
	}
	for name, source := range invalidLogger {
		t.Run(name, func(t *testing.T) {
			call := p015B2CParseCallExpression(t, source)
			if _, _, issues := p015B2CSafeSinkShapeIssues(call); len(issues) == 0 {
				t.Fatal("raw or adapter logger shape was accepted")
			}
		})
	}

	validConsole := p015B2CParseCallExpression(t, `fmt.Print(renderGatewayConsole(
		gatewayConsoleC017ChannelsEnabled,
		newGatewayConsoleCount(len(enabledChannels)),
	))`)
	if _, issues := p015B2CConsoleSinkShapeIssues(validConsole); len(issues) != 0 {
		t.Fatalf("closed console fixture rejected: %v", issues)
	}
	invalidConsole := map[string]string{
		"direct printf":  `fmt.Printf("%s", secret)`,
		"render adapter": `fmt.Print(render(secret))`,
		"site adapter": `fmt.Print(renderGatewayConsole(
			site(), newGatewayConsoleNoFields(),
		))`,
		"field adapter": `fmt.Print(renderGatewayConsole(
			gatewayConsoleC017ChannelsEnabled, fields(secret),
		))`,
		"raw count": `fmt.Print(renderGatewayConsole(
			gatewayConsoleC017ChannelsEnabled,
			newGatewayConsoleCount(fmt.Sprint(secret)),
		))`,
		"mismatched constructor": `fmt.Print(renderGatewayConsole(
			gatewayConsoleC017ChannelsEnabled,
			newGatewayConsolePort(port),
		))`,
	}
	for name, source := range invalidConsole {
		t.Run(name, func(t *testing.T) {
			call := p015B2CParseCallExpression(t, source)
			if _, issues := p015B2CConsoleSinkShapeIssues(call); len(issues) == 0 {
				t.Fatal("raw or adapter console shape was accepted")
			}
		})
	}
}

func p015B2CParseCallExpression(t *testing.T, source string) *ast.CallExpr {
	t.Helper()
	expression, err := parser.ParseExpr(source)
	if err != nil {
		t.Fatal(err)
	}
	call, ok := expression.(*ast.CallExpr)
	if !ok {
		t.Fatalf("fixture expression = %T; want call", expression)
	}
	return call
}

func p015B2CValidateDescriptorGroup(t *testing.T, group p015B2CDescriptorGroup) {
	t.Helper()
	p015B2CRequireDescriptorUnion(t, group.descriptors)
	loggerCount := 0
	consoleCount := 0
	levels := make(map[string]int)
	components := make(map[string]int)
	files := make(map[string]struct{})
	for _, descriptor := range group.descriptors {
		files[descriptor.File] = struct{}{}
		switch descriptor.Kind {
		case p015B2CLoggerSink:
			loggerCount++
			levels[descriptor.Level]++
			components[descriptor.Component]++
		case p015B2CConsoleSink:
			consoleCount++
		default:
			t.Errorf("%s has unknown sink kind %d", descriptor.ID, descriptor.Kind)
		}
	}
	if loggerCount != group.loggerTotal || consoleCount != group.consoleTotal {
		t.Fatalf(
			"%s descriptor totals logger/console = %d/%d; want %d/%d",
			group.name,
			loggerCount,
			consoleCount,
			group.loggerTotal,
			group.consoleTotal,
		)
	}
	p015B2CCompareCounts(t, group.name+" levels", levels, group.levelCounts)
	p015B2CCompareCounts(t, group.name+" components", components, group.componentCount)

	actualByFile := make(map[string][]p015B2CActualSink, len(files))
	for file := range files {
		actualByFile[file] = p015B2CScanLoggingFile(t, file)
	}
	used := make(map[string]struct{}, len(group.descriptors))
	for _, descriptor := range group.descriptors {
		actual, key, ok := p015B2CFindDescriptorSink(actualByFile[descriptor.File], descriptor)
		if !ok {
			t.Errorf("%s did not match current source", descriptor.ID)
			continue
		}
		if _, duplicate := used[key]; duplicate {
			t.Errorf("%s reused current source identity %s", descriptor.ID, key)
			continue
		}
		used[key] = struct{}{}
		if descriptor.Owner != "" && actual.owner != descriptor.Owner {
			t.Errorf("%s owner = %s; want %s", descriptor.ID, actual.owner, descriptor.Owner)
		}
		if descriptor.Kind == p015B2CLoggerSink &&
			(actual.level != descriptor.Level ||
				actual.component != descriptor.Component ||
				actual.message != descriptor.Message) {
			t.Errorf("%s logger shape = %#v; want %#v", descriptor.ID, actual, descriptor)
		}
	}
}

func p015B2CRequireDescriptorUnion(t *testing.T, groups ...[]p015B2CSinkDescriptor) {
	t.Helper()
	ids := make(map[string]struct{})
	for _, group := range groups {
		for _, descriptor := range group {
			if len(descriptor.ID) != 4 ||
				(descriptor.ID[0] != 'G' && descriptor.ID[0] != 'C') {
				t.Errorf("invalid stable descriptor ID %q", descriptor.ID)
				continue
			}
			if _, err := strconv.Atoi(descriptor.ID[1:]); err != nil {
				t.Errorf("invalid stable descriptor ID %q", descriptor.ID)
				continue
			}
			if _, duplicate := ids[descriptor.ID]; duplicate {
				t.Errorf("stable descriptor ID %s appears twice", descriptor.ID)
			}
			ids[descriptor.ID] = struct{}{}
		}
	}
}

func p015B2CCompareCounts(t *testing.T, name string, got, want map[string]int) {
	t.Helper()
	if len(got) != len(want) {
		t.Errorf("%s key count = %d; want %d: got=%v want=%v", name, len(got), len(want), got, want)
	}
	for key, wantCount := range want {
		if got[key] != wantCount {
			t.Errorf("%s %s = %d; want %d", name, key, got[key], wantCount)
		}
	}
}

func p015B2CFindDescriptorSink(
	actual []p015B2CActualSink,
	descriptor p015B2CSinkDescriptor,
) (p015B2CActualSink, string, bool) {
	occurrence := descriptor.Occurrence
	if occurrence == 0 {
		occurrence = 1
	}
	seen := 0
	for index, sink := range actual {
		if sink.kind != descriptor.Kind {
			continue
		}
		if sink.kind == p015B2CLoggerSink && sink.message != descriptor.Message {
			continue
		}
		if sink.kind == p015B2CConsoleSink && sink.consoleSite != descriptor.ConsoleSite {
			continue
		}
		seen++
		if seen == occurrence {
			key := fmt.Sprintf("%s:%d:%d", descriptor.File, sink.position, index)
			return sink, key, true
		}
	}
	return p015B2CActualSink{}, "", false
}

func p015B2CScanLoggingFile(t *testing.T, name string) []p015B2CActualSink {
	t.Helper()
	parsed, err := parser.ParseFile(token.NewFileSet(), name, nil, parser.AllErrors)
	if err != nil {
		t.Fatal(err)
	}
	p015B2CRequireDirectLoggerImport(t, name, parsed)
	var sinks []p015B2CActualSink
	for _, declaration := range parsed.Decls {
		function, ok := declaration.(*ast.FuncDecl)
		if !ok || function.Body == nil {
			continue
		}
		owner := function.Name.Name
		ast.Inspect(function.Body, func(node ast.Node) bool {
			call, ok := node.(*ast.CallExpr)
			if !ok {
				return true
			}
			if level, safe := p015B2CSafeEmitterCall(call); safe {
				component, message, issues := p015B2CSafeSinkShapeIssues(call)
				for _, issue := range issues {
					t.Errorf("%s %s SafeCF at byte %d: %s", name, owner, call.Pos(), issue)
				}
				sinks = append(sinks, p015B2CActualSink{
					file: name, owner: owner, kind: p015B2CLoggerSink,
					position: call.Pos(), level: level,
					component: component, message: message,
				})
				return true
			}
			if p015B2CLegacyLoggerCall(call) {
				t.Errorf("%s %s retains legacy logger call at byte %d", name, owner, call.Pos())
				return true
			}
			if p015B2CFmtOutputCall(call) {
				site, issues := p015B2CConsoleSinkShapeIssues(call)
				for _, issue := range issues {
					t.Errorf("%s %s console at byte %d: %s", name, owner, call.Pos(), issue)
				}
				if site != "" {
					sinks = append(sinks, p015B2CActualSink{
						file: name, owner: owner, kind: p015B2CConsoleSink,
						position: call.Pos(), consoleSite: site,
					})
				}
			}
			return true
		})
	}
	sort.Slice(sinks, func(left, right int) bool {
		return sinks[left].position < sinks[right].position
	})
	return sinks
}

func p015B2CRequireDirectLoggerImport(t *testing.T, name string, parsed *ast.File) {
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
			t.Errorf("%s aliases logger import as %q", name, imported.Name.Name)
		}
	}
	if count != 1 {
		t.Errorf("%s direct logger import count = %d; want 1", name, count)
	}
	ast.Inspect(parsed, func(node ast.Node) bool {
		identifier, ok := node.(*ast.Ident)
		if ok && identifier.Name == "logger" && identifier.Obj != nil {
			t.Errorf("%s shadows direct logger import at byte %d", name, identifier.Pos())
		}
		return true
	})
}

func p015B2CSafeSinkShapeIssues(call *ast.CallExpr) (string, string, []string) {
	var issues []string
	if call.Ellipsis.IsValid() || len(call.Args) != 3 {
		return "<invalid>", "<invalid>", []string{"has non-direct arity"}
	}
	component := p015B2CLoggerSelectorName(call.Args[0], "Component")
	if component == "" {
		component = "<invalid>"
		issues = append(issues, "component is not a direct logger.Component constant")
	}
	message := p015B2CLoggerSelectorName(call.Args[1], "DiagnosticMessage")
	if message == "" {
		message = "<invalid>"
		issues = append(issues, "message is not a direct logger.DiagnosticMessage constant")
	}
	fields, ok := call.Args[2].(*ast.CallExpr)
	if !ok || !p015B2CSelector(fields.Fun, "logger", "NewSafeFields") ||
		fields.Ellipsis.IsValid() {
		issues = append(issues, "fields are not a direct logger.NewSafeFields call")
		return component, message, issues
	}
	for _, field := range fields.Args {
		issues = append(issues, p015B2CClosedFieldIssues(field)...)
	}
	issues = append(issues, p015B2CRawExpressionIssues(fields)...)
	return component, message, issues
}

func p015B2CClosedFieldIssues(expression ast.Expr) []string {
	call, ok := expression.(*ast.CallExpr)
	if !ok || call.Ellipsis.IsValid() {
		return []string{"field is not a direct non-variadic constructor"}
	}
	if identifier, identifierOK := call.Fun.(*ast.Ident); identifierOK {
		arity, allowed := p015B2CClosedGatewayFieldHelpers[identifier.Name]
		if !allowed || len(call.Args) != arity {
			return []string{"field is not a closed gateway helper"}
		}
		if identifier.Name == "gatewayDiagnosticErrorField" &&
			p015B2CLoggerSelectorName(call.Args[0], "ErrorClass") == "" {
			return []string{"error class is not a direct logger.ErrorClass constant"}
		}
		start := 0
		if identifier.Name == "gatewayDiagnosticErrorField" {
			start = 1
		}
		for _, argument := range call.Args[start:] {
			if !p015B2CClosedValueExpression(argument) {
				return []string{"gateway helper receives an adapter or raw value"}
			}
		}
		return nil
	}
	selector, ok := call.Fun.(*ast.SelectorExpr)
	if !ok || !p015B2CIdent(selector.X, "logger") {
		return []string{"field is not a direct logger scalar constructor"}
	}
	switch selector.Sel.Name {
	case "SafeBool", "SafeEnum", "SafeFloat64", "SafeInt", "SafeInt64":
	default:
		return []string{"field uses an unreviewed logger constructor"}
	}
	if len(call.Args) != 2 || p015B2CLoggerSelectorName(call.Args[0], "Field") == "" {
		return []string{"logger scalar field has non-literal key or arity"}
	}
	if !p015B2CClosedValueExpression(call.Args[1]) {
		return []string{"logger scalar field receives an adapter or raw value"}
	}
	return nil
}

func p015B2CClosedValueExpression(expression ast.Expr) bool {
	switch value := expression.(type) {
	case *ast.BasicLit, *ast.Ident:
		return true
	case *ast.ParenExpr:
		return p015B2CClosedValueExpression(value.X)
	case *ast.SelectorExpr:
		return p015B2CClosedValueExpression(value.X)
	case *ast.CallExpr:
		if value.Ellipsis.IsValid() {
			return false
		}
		identifier, ok := value.Fun.(*ast.Ident)
		if !ok {
			return false
		}
		switch identifier.Name {
		case "len", "gatewayConfiguredVoiceProvider":
			return len(value.Args) == 1 && p015B2CClosedValueExpression(value.Args[0])
		default:
			return false
		}
	default:
		return false
	}
}

func p015B2CRawExpressionIssues(root ast.Node) []string {
	const rawMethodNames = "|Error|Errorf|Format|Marshal|MarshalJSON|Sprint|Sprintf|Sprintln|String|"
	var issues []string
	ast.Inspect(root, func(node ast.Node) bool {
		switch value := node.(type) {
		case *ast.MapType, *ast.InterfaceType:
			issues = append(issues, fmt.Sprintf("contains raw map/interface at byte %d", value.Pos()))
		case *ast.CallExpr:
			if identifier, ok := value.Fun.(*ast.Ident); ok && identifier.Name == "any" {
				issues = append(issues, fmt.Sprintf("contains raw any conversion at byte %d", value.Pos()))
				return true
			}
			selector, ok := value.Fun.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			if p015B2CIdent(selector.X, "fmt") ||
				strings.Contains(rawMethodNames, "|"+selector.Sel.Name+"|") {
				issues = append(issues, fmt.Sprintf("invokes raw method %s at byte %d", selector.Sel.Name, value.Pos()))
			}
		}
		return true
	})
	return issues
}

func p015B2CConsoleSinkShapeIssues(call *ast.CallExpr) (string, []string) {
	var issues []string
	if !p015B2CSelector(call.Fun, "fmt", "Print") ||
		call.Ellipsis.IsValid() || len(call.Args) != 1 {
		return "", []string{"console sink is not direct single-argument fmt.Print"}
	}
	render, ok := call.Args[0].(*ast.CallExpr)
	if !ok || !p015B2CIdent(render.Fun, "renderGatewayConsole") ||
		render.Ellipsis.IsValid() || len(render.Args) != 2 {
		return "", []string{"fmt.Print argument is not direct renderGatewayConsole"}
	}
	siteIdentifier, ok := render.Args[0].(*ast.Ident)
	if !ok {
		return "", []string{"console site is not a private literal identifier"}
	}
	expectedConstructor, known := p015B2CConsoleSites[siteIdentifier.Name]
	if !known {
		issues = append(issues, "console site is outside the closed C001-C023 catalog")
	}
	fields, ok := render.Args[1].(*ast.CallExpr)
	if !ok || fields.Ellipsis.IsValid() {
		issues = append(issues, "console fields are not a direct sealed constructor")
		return siteIdentifier.Name, issues
	}
	constructor, ok := fields.Fun.(*ast.Ident)
	if !ok {
		issues = append(issues, "console fields use a non-private constructor")
		return siteIdentifier.Name, issues
	}
	wantArity, allowed := p015B2CConsoleFieldConstructors[constructor.Name]
	if !allowed || len(fields.Args) != wantArity {
		issues = append(issues, "console field constructor or arity is not closed")
	}
	if known && constructor.Name != expectedConstructor {
		issues = append(issues, "console site uses the wrong sealed field shape")
	}
	for _, argument := range fields.Args {
		if !p015B2CClosedValueExpression(argument) {
			issues = append(issues, "console fields receive an adapter or raw value")
		}
	}
	issues = append(issues, p015B2CRawExpressionIssues(render)...)
	return siteIdentifier.Name, issues
}

func p015B2CSafeEmitterCall(call *ast.CallExpr) (string, bool) {
	selector, ok := call.Fun.(*ast.SelectorExpr)
	if !ok || !p015B2CIdent(selector.X, "logger") {
		return "", false
	}
	switch selector.Sel.Name {
	case "DebugSafeCF", "InfoSafeCF", "WarnSafeCF", "ErrorSafeCF", "FatalSafeCF":
		return selector.Sel.Name, true
	default:
		return "", false
	}
}

func p015B2CLegacyLoggerCall(call *ast.CallExpr) bool {
	selector, ok := call.Fun.(*ast.SelectorExpr)
	if !ok || !p015B2CIdent(selector.X, "logger") {
		return false
	}
	name := selector.Sel.Name
	if name == "RecoverPanic" || name == "RecoverPanicNoExit" ||
		name == "DebugSensitiveCF" {
		return true
	}
	for _, prefix := range []string{"Debug", "Info", "Warn", "Error", "Fatal"} {
		if strings.HasPrefix(name, prefix) && !strings.HasSuffix(name, "SafeCF") {
			return true
		}
	}
	return false
}

func p015B2CFmtOutputCall(call *ast.CallExpr) bool {
	selector, ok := call.Fun.(*ast.SelectorExpr)
	if !ok || !p015B2CIdent(selector.X, "fmt") {
		return false
	}
	for _, name := range []string{
		"Print", "Printf", "Println", "Fprint", "Fprintf", "Fprintln",
	} {
		if selector.Sel.Name == name {
			return true
		}
	}
	return false
}

func p015B2CLoggerSelectorName(expression ast.Expr, prefix string) string {
	selector, ok := expression.(*ast.SelectorExpr)
	if !ok || !p015B2CIdent(selector.X, "logger") ||
		!strings.HasPrefix(selector.Sel.Name, prefix) {
		return ""
	}
	return selector.Sel.Name
}

func p015B2CSelector(expression ast.Expr, pkg, name string) bool {
	selector, ok := expression.(*ast.SelectorExpr)
	return ok && p015B2CIdent(selector.X, pkg) && selector.Sel.Name == name
}

func p015B2CIdent(expression ast.Expr, name string) bool {
	identifier, ok := expression.(*ast.Ident)
	return ok && identifier.Name == name
}
