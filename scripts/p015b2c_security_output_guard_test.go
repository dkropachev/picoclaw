package main

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
)

const (
	p015B2CConsoleCatalogPath = "pkg/gateway/gateway_console.go"
	// This digest is the comment-free, canonically formatted Go source for the
	// complete private console catalog. There is intentionally no rewrite path:
	// a behavior change must update this reviewed value explicitly.
	p015B2CConsoleCatalogCanonicalSHA256 = "2b9f1fcaf6d84fadeb65e715b07c4637b020d56ab7b5b73b8a1d733ca9250396"
)

func TestP015B2DirectOSHandleChainsCannotBypassInventory(t *testing.T) {
	repoRoot := p015FindRepoRoot(t)
	scan, err := p015ScanLogging(p015LoggingScanOptions{
		RepoRoot:   repoRoot,
		ModulePath: p015ModulePath,
		Roots:      []string{"scripts/testdata/p015b2_logging_gate/bypass"},
	})
	if err != nil {
		t.Fatal(err)
	}

	const directOwner = ".directOSHandleMethods"
	wantReasons := map[string]int{
		"direct os.Stdout output handle method call Write":        1,
		"direct os.Stderr output handle method call WriteString":  1,
		"direct os.Stdout output handle method value Write":       1,
		"direct os.Stderr output handle method value WriteString": 1,
	}
	gotReasons := make(map[string]int)
	for _, escape := range scan.Escapes {
		if strings.HasSuffix(escape.Owner, directOwner) {
			gotReasons[escape.Reason]++
		}
		if strings.HasSuffix(escape.Owner, ".ordinaryWriterMethods") {
			t.Errorf("ordinary writer method produced an escape: %s", escape)
		}
	}
	if issues := p015ExactStringCountIssues("direct-handle escape", gotReasons, wantReasons); len(issues) != 0 {
		t.Fatalf("direct os handle escape census drifted:\n%s", strings.Join(issues, "\n"))
	}

	wantCallees := map[string]int{
		"os.Stdout.Write":       1,
		"os.Stderr.WriteString": 1,
	}
	gotCallees := make(map[string]int)
	for _, site := range scan.Sites {
		if strings.HasSuffix(site.Owner, directOwner) {
			if site.Kind != "forbidden_output" {
				t.Errorf("direct handle site kind = %q, want forbidden_output: %#v", site.Kind, site)
			}
			gotCallees[site.Callee]++
		}
		if strings.HasSuffix(site.Owner, ".ordinaryWriterMethods") {
			t.Errorf("ordinary writer method was inventoried as output: %#v", site)
		}
	}
	if issues := p015ExactStringCountIssues("direct-handle site", gotCallees, wantCallees); len(issues) != 0 {
		t.Fatalf("direct os handle site census drifted:\n%s", strings.Join(issues, "\n"))
	}
}

func TestP015B2CConsoleCatalogSourceIsFrozenAndAmbientFree(t *testing.T) {
	repoRoot := p015FindRepoRoot(t)
	path := filepath.Join(repoRoot, filepath.FromSlash(p015B2CConsoleCatalogPath))
	source, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	canonical, err := p015B2CConsoleCatalogCanonical(source)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(canonical)
	if got := hex.EncodeToString(sum[:]); got != p015B2CConsoleCatalogCanonicalSHA256 {
		t.Fatalf(
			"closed console catalog source changed: canonical sha256=%s, want %s",
			got,
			p015B2CConsoleCatalogCanonicalSHA256,
		)
	}
	if issues := p015B2CConsoleCatalogPurityIssues(source); len(issues) != 0 {
		t.Fatalf("closed console catalog purity drifted:\n%s", strings.Join(issues, "\n"))
	}

	// Model an ambient dependency hidden behind an otherwise valid port that the
	// catalog's fixed output samples do not exercise.
	mutated := strings.Replace(
		string(source),
		`import "strconv"`,
		"import (\n\t\"os\"\n\t\"strconv\"\n)",
		1,
	)
	const caseNeedle = "\tcase gatewayConsoleC001GatewayStarted:\n"
	mutated = strings.Replace(
		mutated,
		caseNeedle,
		caseNeedle+"\t\tif fields.first == 65535 {\n"+
			"\t\t\treturn os.Getenv(\"P015_B2C_CONSOLE_SECRET\")\n\t\t}\n",
		1,
	)
	if mutated == string(source) {
		t.Fatal("ambient console mutation fixture did not change source")
	}
	mutatedCanonical, err := p015B2CConsoleCatalogCanonical([]byte(mutated))
	if err != nil {
		t.Fatal(err)
	}
	mutatedSum := sha256.Sum256(mutatedCanonical)
	if hex.EncodeToString(mutatedSum[:]) == p015B2CConsoleCatalogCanonicalSHA256 {
		t.Fatal("ambient console mutation retained frozen canonical digest")
	}
	issues := p015B2CConsoleCatalogPurityIssues([]byte(mutated))
	if !p015ContainsIssue(issues, `unexpected console catalog import "os"`) ||
		!p015ContainsIssue(issues, "unapproved console catalog call os.Getenv") {
		t.Fatalf("os.Getenv mutation was not rejected exactly: %v", issues)
	}
}

func p015B2CConsoleCatalogCanonical(source []byte) ([]byte, error) {
	fileSet := token.NewFileSet()
	file, err := parser.ParseFile(
		fileSet,
		p015B2CConsoleCatalogPath,
		source,
		parser.SkipObjectResolution,
	)
	if err != nil {
		return nil, fmt.Errorf("parse closed console catalog: %w", err)
	}
	return []byte(p015RenderNode(fileSet, file)), nil
}

func p015B2CConsoleCatalogPurityIssues(source []byte) []string {
	fileSet := token.NewFileSet()
	file, err := parser.ParseFile(
		fileSet,
		p015B2CConsoleCatalogPath,
		source,
		parser.SkipObjectResolution,
	)
	if err != nil {
		return []string{"parse closed console catalog: " + err.Error()}
	}

	var issues []string
	if file.Name.Name != "gateway" {
		issues = append(issues, "console catalog package is not gateway")
	}
	for _, spec := range file.Imports {
		path, unquoteErr := strconv.Unquote(spec.Path.Value)
		if unquoteErr != nil {
			issues = append(issues, "invalid console catalog import "+spec.Path.Value)
			continue
		}
		if path != "strconv" || spec.Name != nil {
			issues = append(issues, fmt.Sprintf("unexpected console catalog import %q", path))
		}
	}
	if len(file.Imports) != 1 {
		issues = append(issues, fmt.Sprintf("console catalog has %d imports, want exact 1", len(file.Imports)))
	}

	wantDeclarations := p015B2CConsoleCatalogDeclarations()
	gotDeclarations := make(map[string]int, len(wantDeclarations))
	for _, declaration := range file.Decls {
		switch declaration := declaration.(type) {
		case *ast.GenDecl:
			if declaration.Tok == token.IMPORT {
				continue
			}
			for _, rawSpec := range declaration.Specs {
				switch spec := rawSpec.(type) {
				case *ast.TypeSpec:
					gotDeclarations["type:"+spec.Name.Name]++
				case *ast.ValueSpec:
					if declaration.Tok != token.CONST {
						issues = append(issues, "console catalog contains non-constant package data")
					}
					for _, name := range spec.Names {
						gotDeclarations["const:"+name.Name]++
					}
				default:
					issues = append(issues, fmt.Sprintf("console catalog contains declaration %T", rawSpec))
				}
			}
		case *ast.FuncDecl:
			key := "func:" + declaration.Name.Name
			if declaration.Recv != nil {
				key = "method:" + declaration.Name.Name
			}
			gotDeclarations[key]++
			issues = append(issues, p015B2CConsoleFunctionPurityIssues(fileSet, declaration)...)
		default:
			issues = append(issues, fmt.Sprintf("console catalog contains declaration %T", declaration))
		}
	}
	issues = append(
		issues,
		p015ExactStringCountIssues(
			"console catalog declaration",
			gotDeclarations,
			wantDeclarations,
		)...,
	)
	sort.Strings(issues)
	return issues
}

func p015B2CConsoleFunctionPurityIssues(
	fileSet *token.FileSet,
	function *ast.FuncDecl,
) []string {
	if function.Body == nil {
		return []string{"console catalog function " + function.Name.Name + " has no body"}
	}
	allowedIdentifiers := p015B2CConsoleCatalogIdentifiers()
	var issues []string
	ast.Inspect(function.Body, func(node ast.Node) bool {
		switch node := node.(type) {
		case *ast.CallExpr:
			if !p015B2CAllowedConsoleCatalogCall(node) {
				issues = append(
					issues,
					"unapproved console catalog call "+p015RenderNode(fileSet, node.Fun),
				)
			}
		case *ast.Ident:
			if _, allowed := allowedIdentifiers[node.Name]; !allowed {
				issues = append(
					issues,
					fmt.Sprintf(
						"unapproved console catalog identifier %s in %s",
						node.Name,
						function.Name.Name,
					),
				)
			}
		}
		return true
	})
	return issues
}

func p015B2CAllowedConsoleCatalogCall(call *ast.CallExpr) bool {
	if call.Ellipsis != token.NoPos {
		return false
	}
	selector, ok := p015UnwrapParens(call.Fun).(*ast.SelectorExpr)
	if !ok || len(call.Args) != 1 {
		return false
	}
	receiver, ok := p015UnwrapParens(selector.X).(*ast.Ident)
	if !ok {
		return false
	}
	switch {
	case receiver.Name == "fields" && selector.Sel.Name == "validFor":
		kind, ok := p015UnwrapParens(call.Args[0]).(*ast.Ident)
		return ok && p015B2CConsoleFieldKindNames()[kind.Name]
	case receiver.Name == "strconv" && selector.Sel.Name == "Itoa":
		field, ok := p015UnwrapParens(call.Args[0]).(*ast.SelectorExpr)
		if !ok {
			return false
		}
		fields, ok := p015UnwrapParens(field.X).(*ast.Ident)
		return ok && fields.Name == "fields" &&
			(field.Sel.Name == "first" || field.Sel.Name == "second")
	default:
		return false
	}
}

func p015B2CConsoleCatalogDeclarations() map[string]int {
	result := map[string]int{
		"type:gatewayConsoleSiteID":       1,
		"type:gatewayConsoleFieldKind":    1,
		"type:gatewayConsoleFields":       1,
		"func:newGatewayConsoleNoFields":  1,
		"func:newGatewayConsoleCount":     1,
		"func:newGatewayConsoleCountPair": 1,
		"func:newGatewayConsolePort":      1,
		"method:validFor":                 1,
		"func:renderGatewayConsole":       1,
	}
	for name := range p015B2CConsoleSiteNames() {
		result["const:"+name] = 1
	}
	for name := range p015B2CConsoleFieldKindNames() {
		result["const:"+name] = 1
	}
	return result
}

func p015B2CConsoleCatalogIdentifiers() map[string]struct{} {
	result := map[string]struct{}{
		"false": {}, "fields": {}, "first": {}, "gatewayConsoleFields": {},
		"Itoa": {}, "kind": {}, "port": {}, "second": {}, "site": {}, "strconv": {},
		"validFor": {}, "value": {},
	}
	for name := range p015B2CConsoleSiteNames() {
		result[name] = struct{}{}
	}
	for name := range p015B2CConsoleFieldKindNames() {
		result[name] = struct{}{}
	}
	return result
}

func p015B2CConsoleSiteNames() map[string]bool {
	return map[string]bool{
		"gatewayConsoleC001GatewayStarted":             true,
		"gatewayConsoleC002StopHint":                   true,
		"gatewayConsoleC003DebugEnabled":               true,
		"gatewayConsoleC004AgentStatus":                true,
		"gatewayConsoleC005ToolsLoaded":                true,
		"gatewayConsoleC006SkillsAvailable":            true,
		"gatewayConsoleC007NoModelConfigured":          true,
		"gatewayConsoleC008HeartbeatRestarted":         true,
		"gatewayConsoleC009EventInboxReopened":         true,
		"gatewayConsoleC010CronRestarted":              true,
		"gatewayConsoleC011ChannelsRestarted":          true,
		"gatewayConsoleC012RestartedChannelsEnabled":   true,
		"gatewayConsoleC013NoRestartedChannelsEnabled": true,
		"gatewayConsoleC014DeviceServiceRestarted":     true,
		"gatewayConsoleC015EventWorkersRestarted":      true,
		"gatewayConsoleC016CronStarted":                true,
		"gatewayConsoleC017ChannelsEnabled":            true,
		"gatewayConsoleC018NoChannelsEnabled":          true,
		"gatewayConsoleC019HealthEndpointsAvailable":   true,
		"gatewayConsoleC020DeviceServiceStarted":       true,
		"gatewayConsoleC021EventWorkersStarted":        true,
		"gatewayConsoleC022EventInboxOpened":           true,
		"gatewayConsoleC023HeartbeatStarted":           true,
	}
}

func p015B2CConsoleFieldKindNames() map[string]bool {
	return map[string]bool{
		"gatewayConsoleFieldsInvalid":   true,
		"gatewayConsoleFieldsNone":      true,
		"gatewayConsoleFieldsCount":     true,
		"gatewayConsoleFieldsCountPair": true,
		"gatewayConsoleFieldsPort":      true,
	}
}

func p015ExactStringCountIssues(label string, got, want map[string]int) []string {
	var issues []string
	for key, count := range want {
		if got[key] != count {
			issues = append(issues, fmt.Sprintf("%s %q count = %d, want %d", label, key, got[key], count))
		}
	}
	for key, count := range got {
		if _, expected := want[key]; !expected {
			issues = append(issues, fmt.Sprintf("%s has unexpected %q count %d", label, key, count))
		}
	}
	sort.Strings(issues)
	return issues
}

func p015ContainsIssue(issues []string, fragment string) bool {
	for _, issue := range issues {
		if strings.Contains(issue, fragment) {
			return true
		}
	}
	return false
}
