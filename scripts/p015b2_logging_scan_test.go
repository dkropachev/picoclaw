package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
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
)

const p015PicoLoggerImport = "github.com/sipeed/picoclaw/pkg/logger"

type p015LoggingSite struct {
	ID          string
	Disposition string
	File        string
	Owner       string
	Ordinal     int
	Kind        string
	Callee      string
	Call        string
	Canary      string
}

func (site p015LoggingSite) sourceKey() string {
	return strings.Join([]string{
		site.File,
		site.Owner,
		strconv.Itoa(site.Ordinal),
		site.Kind,
		site.Callee,
		site.Call,
	}, "\x00")
}

func (site p015LoggingSite) immutableSourceKey() string {
	return strings.Join([]string{
		site.File,
		site.Owner,
		strconv.Itoa(site.Ordinal),
		site.Callee,
		site.Call,
	}, "\x00")
}

func (site p015LoggingSite) tombstoneProvenanceHash() string {
	sum := sha256.Sum256([]byte(site.immutableSourceKey()))
	return hex.EncodeToString(sum[:])
}

func (site p015LoggingSite) shortSourceKey() string {
	sum := sha256.Sum256([]byte(site.sourceKey()))
	return hex.EncodeToString(sum[:8])
}

type p015LoggingEscape struct {
	File   string
	Line   int
	Owner  string
	Reason string
}

func (escape p015LoggingEscape) String() string {
	return fmt.Sprintf("%s:%d: %s: %s", escape.File, escape.Line, escape.Owner, escape.Reason)
}

type p015LoggingScan struct {
	Sites                []p015LoggingSite
	Escapes              []p015LoggingEscape
	ReviewedMethodValues map[string]int
}

type p015LoggingScanOptions struct {
	RepoRoot   string
	ModulePath string
	Roots      []string
}

func p015ScanLogging(options p015LoggingScanOptions) (p015LoggingScan, error) {
	var result p015LoggingScan
	for _, relativeRoot := range options.Roots {
		root := filepath.Join(options.RepoRoot, filepath.FromSlash(relativeRoot))
		err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if entry.IsDir() {
				return nil
			}
			name := entry.Name()
			if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
				return nil
			}

			relative, err := filepath.Rel(options.RepoRoot, path)
			if err != nil {
				return err
			}
			relative = filepath.ToSlash(relative)
			fileScan, err := p015ScanLoggingFile(options, relative)
			if err != nil {
				return err
			}
			result.Sites = append(result.Sites, fileScan.Sites...)
			result.Escapes = append(result.Escapes, fileScan.Escapes...)
			for key, count := range fileScan.ReviewedMethodValues {
				if result.ReviewedMethodValues == nil {
					result.ReviewedMethodValues = make(map[string]int)
				}
				result.ReviewedMethodValues[key] += count
			}
			return nil
		})
		if err != nil {
			return p015LoggingScan{}, fmt.Errorf("scan %s: %w", relativeRoot, err)
		}
	}

	sort.Slice(result.Sites, func(left, right int) bool {
		return result.Sites[left].sourceKey() < result.Sites[right].sourceKey()
	})
	sort.Slice(result.Escapes, func(left, right int) bool {
		return result.Escapes[left].String() < result.Escapes[right].String()
	})
	return result, nil
}

type p015ParsedLoggingFile struct {
	options              p015LoggingScanOptions
	fileSet              *token.FileSet
	file                 *ast.File
	relative             string
	packagePath          string
	imports              map[string]string
	watchedImport        map[string]string
	sites                []p015LoggingSite
	escapes              []p015LoggingEscape
	reviewedMethodValues map[string]int
}

func p015ScanLoggingFile(
	options p015LoggingScanOptions,
	relative string,
) (p015LoggingScan, error) {
	fileSet := token.NewFileSet()
	path := filepath.Join(options.RepoRoot, filepath.FromSlash(relative))
	parsed, err := parser.ParseFile(fileSet, path, nil, parser.AllErrors|parser.ParseComments)
	if err != nil {
		return p015LoggingScan{}, fmt.Errorf("parse %s: %w", relative, err)
	}

	directory := filepath.ToSlash(filepath.Dir(relative))
	packagePath := options.ModulePath
	if directory != "." {
		packagePath += "/" + directory
	}
	scanner := &p015ParsedLoggingFile{
		options:              options,
		fileSet:              fileSet,
		file:                 parsed,
		relative:             relative,
		packagePath:          packagePath,
		imports:              make(map[string]string),
		watchedImport:        make(map[string]string),
		reviewedMethodValues: make(map[string]int),
	}
	if err := scanner.resolveImports(); err != nil {
		return p015LoggingScan{}, err
	}
	scanner.findImportShadows()
	scanner.findForbiddenPicoFacadeReferences()
	scanner.scanDeclarations()
	return p015LoggingScan{
		Sites: scanner.sites, Escapes: scanner.escapes,
		ReviewedMethodValues: scanner.reviewedMethodValues,
	}, nil
}

func (scanner *p015ParsedLoggingFile) resolveImports() error {
	for _, spec := range scanner.file.Imports {
		importPath, err := strconv.Unquote(spec.Path.Value)
		if err != nil {
			return fmt.Errorf("parse import in %s: %w", scanner.relative, err)
		}
		alias := filepath.Base(importPath)
		if spec.Name != nil {
			alias = spec.Name.Name
		}
		if alias == "_" {
			if p015WatchedLoggingImport(importPath) {
				scanner.addEscape(spec.Pos(), scanner.packagePath+".<imports>", "blank logging import "+importPath)
			}
			continue
		}
		if alias == "." {
			if p015WatchedLoggingImport(importPath) {
				scanner.addEscape(spec.Pos(), scanner.packagePath+".<imports>", "dot logging import "+importPath)
			}
			continue
		}
		scanner.imports[alias] = importPath
		if p015WatchedLoggingImport(importPath) {
			scanner.watchedImport[alias] = importPath
		}
		if p015ForbiddenLoggingImport(importPath) {
			scanner.addEscape(
				spec.Pos(),
				scanner.packagePath+".<imports>",
				"forbidden logging adapter import "+importPath,
			)
		}
		if importPath == "log" || importPath == "log/slog" {
			scanner.addEscape(
				spec.Pos(),
				scanner.packagePath+".<imports>",
				"forbidden standard logging import "+importPath,
			)
		}
	}
	return nil
}

func (scanner *p015ParsedLoggingFile) findForbiddenPicoFacadeReferences() {
	ast.Inspect(scanner.file, func(node ast.Node) bool {
		selector, ok := node.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		importPath, imported := scanner.importedSelector(selector)
		if !imported || importPath != p015PicoLoggerImport {
			return true
		}
		switch selector.Sel.Name {
		case "Logger":
			scanner.addEscape(
				selector.Pos(),
				scanner.packagePath+".<facade>",
				"forbidden Pico logger facade type reference logger.Logger",
			)
		case "NewLogger":
			scanner.addEscape(
				selector.Pos(),
				scanner.packagePath+".<facade>",
				"forbidden Pico logger facade constructor reference logger.NewLogger",
			)
		}
		return true
	})
}

func p015WatchedLoggingImport(importPath string) bool {
	switch importPath {
	case p015PicoLoggerImport, "fmt", "io", "log", "log/slog", "os", "runtime/debug":
		return true
	default:
		return p015ForbiddenLoggingImport(importPath)
	}
}

func p015ForbiddenLoggingImport(importPath string) bool {
	return importPath == "github.com/rs/zerolog" ||
		strings.HasPrefix(importPath, "github.com/rs/zerolog/") ||
		importPath == "github.com/go-logr/logr" ||
		strings.HasPrefix(importPath, "github.com/go-logr/logr/")
}

func (scanner *p015ParsedLoggingFile) findImportShadows() {
	ast.Inspect(scanner.file, func(node ast.Node) bool {
		identifier, ok := node.(*ast.Ident)
		if !ok || identifier.Obj == nil || identifier.Obj.Pos() != identifier.Pos() {
			return true
		}
		importPath, watched := scanner.watchedImport[identifier.Name]
		if !watched {
			return true
		}
		scanner.addEscape(
			identifier.Pos(),
			scanner.packagePath+".<scope>",
			fmt.Sprintf("logging import alias %q for %s is shadowed", identifier.Name, importPath),
		)
		return true
	})
}

func (scanner *p015ParsedLoggingFile) scanDeclarations() {
	initOrdinals := make(map[string]int)
	for _, declaration := range scanner.file.Decls {
		switch declaration := declaration.(type) {
		case *ast.FuncDecl:
			if declaration.Body == nil {
				continue
			}
			owner := scanner.functionOwner(declaration)
			if declaration.Recv == nil && declaration.Name.Name == "init" {
				initOrdinals[owner]++
				owner += "#" + strconv.Itoa(initOrdinals[owner])
			}
			scanner.scanOwner(declaration.Body, owner)
		case *ast.GenDecl:
			scanner.scanInitializerDeclaration(declaration)
		}
	}
}

func (scanner *p015ParsedLoggingFile) scanInitializerDeclaration(declaration *ast.GenDecl) {
	if declaration.Tok != token.VAR && declaration.Tok != token.CONST {
		return
	}
	for _, rawSpec := range declaration.Specs {
		spec, ok := rawSpec.(*ast.ValueSpec)
		if !ok {
			continue
		}
		for index, value := range spec.Values {
			name := "unnamed"
			if index < len(spec.Names) {
				name = spec.Names[index].Name
			} else if len(spec.Names) > 0 {
				name = spec.Names[len(spec.Names)-1].Name + "+"
			}
			owner := fmt.Sprintf("%s.<init:%s:%s>", scanner.packagePath, declaration.Tok, name)
			scanner.scanOwner(value, owner)
		}
	}
}

func (scanner *p015ParsedLoggingFile) functionOwner(function *ast.FuncDecl) string {
	if function.Recv == nil || len(function.Recv.List) == 0 {
		return scanner.packagePath + "." + function.Name.Name
	}
	receiver := p015RenderNode(scanner.fileSet, function.Recv.List[0].Type)
	return fmt.Sprintf("%s.(%s).%s", scanner.packagePath, receiver, function.Name.Name)
}

func (scanner *p015ParsedLoggingFile) scanOwner(node ast.Node, owner string) {
	var calls []*ast.CallExpr
	var literals []*ast.FuncLit
	ast.Inspect(node, func(current ast.Node) bool {
		if literal, ok := current.(*ast.FuncLit); ok && current != node {
			literals = append(literals, literal)
			return false
		}
		if call, ok := current.(*ast.CallExpr); ok {
			calls = append(calls, call)
		}
		return true
	})
	sort.Slice(calls, func(left, right int) bool { return calls[left].Pos() < calls[right].Pos() })
	ordinal := 0
	for _, call := range calls {
		site, found := scanner.classifyCall(call, owner)
		if !found {
			continue
		}
		ordinal++
		site.Ordinal = ordinal
		scanner.sites = append(scanner.sites, site)
	}
	scanner.findFunctionValues(node, owner)
	for index, literal := range literals {
		scanner.scanOwner(literal.Body, fmt.Sprintf("%s.$lit%d", owner, index+1))
	}
}

func (scanner *p015ParsedLoggingFile) classifyCall(
	call *ast.CallExpr,
	owner string,
) (p015LoggingSite, bool) {
	function := p015UnwrapParens(call.Fun)
	if identifier, ok := function.(*ast.Ident); ok && identifier.Obj == nil {
		if identifier.Name == "print" || identifier.Name == "println" {
			return scanner.site(call, owner, "forbidden_output", "builtin."+identifier.Name), true
		}
	}
	selector, ok := function.(*ast.SelectorExpr)
	if !ok {
		return p015LoggingSite{}, false
	}
	importPath, imported := scanner.importedSelector(selector)
	if !imported {
		if p015EmitterLikeMethod(selector.Sel.Name, len(call.Args)) {
			if scanner.allowedUnresolvedEmitterCall(owner, call) {
				return p015LoggingSite{}, false
			}
			scanner.addEscape(
				call.Pos(),
				owner,
				"unresolved emitter-like receiver call "+selector.Sel.Name,
			)
			return scanner.site(
				call,
				owner,
				"forbidden_output",
				"unresolved."+selector.Sel.Name,
			), true
		}
		return p015LoggingSite{}, false
	}
	name := selector.Sel.Name
	switch importPath {
	case p015PicoLoggerImport:
		switch {
		case p015PicoLegacyEmitter(name):
			return scanner.site(call, owner, "pico_legacy", "pico."+name), true
		case p015PicoSafeEmitter(name):
			return scanner.site(call, owner, "pico_safe", "pico."+name), true
		case name == "InitPanic":
			return scanner.site(call, owner, "panic_artifact", "pico.InitPanic"), true
		}
	case "fmt":
		if p015NameIn(name, "Print", "Printf", "Println") {
			return scanner.site(call, owner, "console", "fmt."+name), true
		}
		if p015NameIn(name, "Fprint", "Fprintf", "Fprintln") {
			return scanner.site(call, owner, "functional_fmt", "fmt."+name), true
		}
	case "io":
		if p015NameIn(name, "Copy", "CopyN", "WriteString") {
			return scanner.site(call, owner, "functional_io", "io."+name), true
		}
	case "log", "log/slog":
		if p015StandardLoggingEmitter(importPath, name) {
			scanner.addEscape(call.Pos(), owner, "forbidden "+importPath+" output call "+name)
			return scanner.site(call, owner, "forbidden_output", importPath+"."+name), true
		}
	case "runtime/debug":
		if name == "PrintStack" || name == "Stack" {
			scanner.addEscape(call.Pos(), owner, "runtime/debug stack output or capture")
			return scanner.site(call, owner, "forbidden_output", "runtime/debug."+name), true
		}
	}
	if p015ForbiddenLoggingImport(importPath) {
		scanner.addEscape(call.Pos(), owner, "forbidden logging adapter call "+importPath+"."+name)
		return scanner.site(call, owner, "forbidden_output", importPath+"."+name), true
	}
	if !p015WatchedLoggingImport(importPath) &&
		p015EmitterLikeMethod(name, len(call.Args)) &&
		!p015ReviewedNonLoggingImportedCall(importPath, name) {
		scanner.addEscape(
			call.Pos(),
			owner,
			"unreviewed emitter-like imported call "+importPath+"."+name,
		)
		return scanner.site(
			call,
			owner,
			"forbidden_output",
			"foreign."+name,
		), true
	}
	return p015LoggingSite{}, false
}

func p015EmitterLikeMethod(name string, argumentCount int) bool {
	for _, prefix := range []string{
		"Debug",
		"Info",
		"Warn",
		"Fatal",
		"Panic",
		"Print",
		"Log",
		"Output",
		"Trace",
		"Notice",
		"Crit",
		"Critical",
		"DPanic",
		"Alert",
		"Emergency",
	} {
		if strings.HasPrefix(name, prefix) {
			return true
		}
	}
	return argumentCount > 0 && strings.HasPrefix(name, "Error")
}

func (scanner *p015ParsedLoggingFile) allowedUnresolvedEmitterCall(
	owner string,
	call *ast.CallExpr,
) bool {
	key := strings.Join([]string{
		scanner.relative,
		owner,
		p015RenderNode(scanner.fileSet, call),
	}, "\x00")
	_, allowed := p015ReviewedNonLoggingCalls[key]
	return allowed
}

// No Agent or Gateway emitter-shaped domain call currently needs an exception.
// Additions must use the exact file/owner/canonical-call key and receive review.
var p015ReviewedNonLoggingCalls = map[string]struct{}{}

var p015ReviewedNonLoggingImportedCalls = map[string]struct{}{
	"github.com/sipeed/picoclaw/pkg/tools\x00ErrorResult": {},
}

func p015ReviewedNonLoggingImportedCall(importPath, name string) bool {
	_, reviewed := p015ReviewedNonLoggingImportedCalls[importPath+"\x00"+name]
	return reviewed
}

func (scanner *p015ParsedLoggingFile) site(
	call *ast.CallExpr,
	owner string,
	kind string,
	callee string,
) p015LoggingSite {
	return p015LoggingSite{
		File:   scanner.relative,
		Owner:  owner,
		Kind:   kind,
		Callee: callee,
		Call:   p015RenderNode(scanner.fileSet, call),
	}
}

func (scanner *p015ParsedLoggingFile) findFunctionValues(node ast.Node, owner string) {
	directFunctions := make(map[token.Pos]struct{})
	ast.Inspect(node, func(current ast.Node) bool {
		if current != node {
			if _, ok := current.(*ast.FuncLit); ok {
				return false
			}
		}
		call, ok := current.(*ast.CallExpr)
		if !ok {
			return true
		}
		function := p015UnwrapParens(call.Fun)
		if selector, ok := function.(*ast.SelectorExpr); ok {
			directFunctions[selector.Pos()] = struct{}{}
		}
		return true
	})
	parents := p015ParentNodes(node)
	methodValueOrdinal := 0
	ast.Inspect(node, func(current ast.Node) bool {
		if current != node {
			if _, ok := current.(*ast.FuncLit); ok {
				return false
			}
		}
		selector, ok := current.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		if _, direct := directFunctions[selector.Pos()]; direct {
			return true
		}
		if p015SelectorIsNonValuePosition(selector, parents[selector]) {
			return true
		}
		importPath, imported := scanner.importedSelector(selector)
		if !imported {
			if p015EmitterLikeMethod(selector.Sel.Name, 1) {
				methodValueOrdinal++
				if scanner.allowedUnresolvedMethodValue(owner, methodValueOrdinal, selector) {
					return true
				}
				scanner.addEscape(
					selector.Pos(),
					owner,
					fmt.Sprintf(
						"unresolved emitter-like receiver method value #%d %s",
						methodValueOrdinal,
						p015RenderNode(scanner.fileSet, selector),
					),
				)
			}
			return true
		}
		name := selector.Sel.Name
		if p015OutputFunction(importPath, name) {
			scanner.addEscape(selector.Pos(), owner, "output function value "+importPath+"."+name)
		} else if !p015WatchedLoggingImport(importPath) &&
			p015EmitterLikeMethod(name, 1) &&
			!p015ReviewedNonLoggingImportedCall(importPath, name) {
			scanner.addEscape(
				selector.Pos(),
				owner,
				"unreviewed emitter-like imported method value "+importPath+"."+name,
			)
		}
		if importPath == "os" && (name == "Stdout" || name == "Stderr") {
			scanner.addEscape(selector.Pos(), owner, "direct os."+name+" output handle")
		}
		return true
	})
}

func p015ParentNodes(node ast.Node) map[ast.Node]ast.Node {
	parents := make(map[ast.Node]ast.Node)
	var stack []ast.Node
	ast.Inspect(node, func(current ast.Node) bool {
		if current == nil {
			stack = stack[:len(stack)-1]
			return true
		}
		if len(stack) != 0 {
			parents[current] = stack[len(stack)-1]
		}
		stack = append(stack, current)
		return true
	})
	return parents
}

func p015SelectorIsNonValuePosition(selector *ast.SelectorExpr, parent ast.Node) bool {
	switch parent := parent.(type) {
	case *ast.SelectorExpr:
		return parent.X == selector
	case *ast.AssignStmt:
		for _, left := range parent.Lhs {
			if left == selector {
				return true
			}
		}
	case *ast.IncDecStmt:
		return parent.X == selector
	case *ast.RangeStmt:
		return parent.Key == selector || parent.Value == selector
	case *ast.KeyValueExpr:
		return parent.Key == selector
	case *ast.Field:
		return parent.Type == selector
	case *ast.TypeSpec:
		return parent.Type == selector
	case *ast.CompositeLit:
		return parent.Type == selector
	case *ast.TypeAssertExpr:
		return parent.Type == selector
	}
	return false
}

func (scanner *p015ParsedLoggingFile) allowedUnresolvedMethodValue(
	owner string,
	ordinal int,
	selector *ast.SelectorExpr,
) bool {
	key := p015MethodValueKey(
		scanner.relative,
		owner,
		ordinal,
		p015RenderNode(scanner.fileSet, selector),
	)
	_, allowed := p015ReviewedNonLoggingMethodValues[key]
	if allowed {
		scanner.reviewedMethodValues[key]++
	}
	return allowed
}

func p015MethodValueKey(file, owner string, ordinal int, selector string) string {
	return strings.Join([]string{file, owner, strconv.Itoa(ordinal), selector}, "\x00")
}

// These exact scalar/boolean DTO selections are data, not callable methods.
// File, qualified owner, owner-local ordinal, and selector are all pinned.
var p015ReviewedNonLoggingMethodValues = map[string]struct{}{
	p015MethodValueKey("pkg/agent/events_runtime.go", "github.com/sipeed/picoclaw/pkg/agent.runtimeCorrelationFromHookMeta", 1, "meta.TracePath"):                                 {},
	p015MethodValueKey("pkg/agent/evolution_bridge.go", "github.com/sipeed/picoclaw/pkg/agent.toEvolutionToolExecutions", 1, "record.ErrorSummary"):                               {},
	p015MethodValueKey("pkg/agent/hook_process.go", "github.com/sipeed/picoclaw/pkg/agent.(*ProcessHook).call", 1, "resp.Error"):                                                  {},
	p015MethodValueKey("pkg/agent/legacy_events.go", "github.com/sipeed/picoclaw/pkg/agent.hookMetaFromRuntimeEvent", 1, "evt.Correlation.TraceID"):                               {},
	p015MethodValueKey("pkg/agent/runtime_event_logger.go", "github.com/sipeed/picoclaw/pkg/agent.appendRuntimeEventCorrelationFields", 1, "correlation.TraceID"):                 {},
	p015MethodValueKey("pkg/agent/runtime_event_logger.go", "github.com/sipeed/picoclaw/pkg/agent.appendRuntimeEventPayloadSummary", 1, "payload.Error"):                          {},
	p015MethodValueKey("pkg/agent/subturn.go", "github.com/sipeed/picoclaw/pkg/agent.(*AgentLoopSpawner).SpawnSubTurn", 1, "cfg.Critical"):                                        {},
	p015MethodValueKey("pkg/agent/subturn.go", "github.com/sipeed/picoclaw/pkg/agent.spawnSubTurn", 1, "cfg.Critical"):                                                            {},
	p015MethodValueKey("pkg/agent/subturn.go", "github.com/sipeed/picoclaw/pkg/agent.spawnSubTurn", 2, "cfg.Critical"):                                                            {},
	p015MethodValueKey("pkg/agent/turn_state.go", "github.com/sipeed/picoclaw/pkg/agent.(*turnState).toolExecutionsSnapshot", 1, "exec.ErrorSummary"):                             {},
	p015MethodValueKey("pkg/agent/workflow_automations.go", "github.com/sipeed/picoclaw/pkg/agent.(*AgentLoop).loadScheduledWorkflowRuns", 1, "def.Error"):                        {},
	p015MethodValueKey("pkg/agent/workflow_automations.go", "github.com/sipeed/picoclaw/pkg/agent.(*AgentLoop).handleWorkflowRuntimeEventForGeneration", 1, "def.Error"):          {},
	p015MethodValueKey("pkg/agent/workflow_managed.go", "github.com/sipeed/picoclaw/pkg/agent.workflowManagedCalibrationSimilarityScore", 1, "entry.OutputSchemaHash"):            {},
	p015MethodValueKey("pkg/agent/workflow_managed.go", "github.com/sipeed/picoclaw/pkg/agent.workflowRunManagedChildren.$lit1", 1, "req.Output"):                                 {},
	p015MethodValueKey("pkg/agent/workflow_managed.go", "github.com/sipeed/picoclaw/pkg/agent.workflowStructuredAgentOutputs", 1, "structured.Error"):                             {},
	p015MethodValueKey("pkg/agent/workflow_managed.go", "github.com/sipeed/picoclaw/pkg/agent.workflowStructuredAgentOutputs", 2, "structured.Error"):                             {},
	p015MethodValueKey("pkg/agent/workflow_managed.go", "github.com/sipeed/picoclaw/pkg/agent.workflowManagedChildOutput", 1, "result.structured.Error"):                          {},
	p015MethodValueKey("pkg/agent/workflow_managed.go", "github.com/sipeed/picoclaw/pkg/agent.workflowManagedChildOutput", 2, "result.structured.Error"):                          {},
	p015MethodValueKey("pkg/agent/workflow_managed.go", "github.com/sipeed/picoclaw/pkg/agent.(*workflowAgentRunner).runManagedSplit", 1, "req.Output"):                           {},
	p015MethodValueKey("pkg/agent/workflow_managed.go", "github.com/sipeed/picoclaw/pkg/agent.workflowManagedSplitStrategy", 1, "req.Output"):                                     {},
	p015MethodValueKey("pkg/agent/workflow_managed.go", "github.com/sipeed/picoclaw/pkg/agent.(*workflowAgentRunner).runManagedSplit", 2, "req.Output"):                           {},
	p015MethodValueKey("pkg/agent/workflow_managed.go", "github.com/sipeed/picoclaw/pkg/agent.workflowModelCandidateProfileGuarded", 1, "metadata.OutputPricePerMTok"):            {},
	p015MethodValueKey("pkg/agent/workflow_managed.go", "github.com/sipeed/picoclaw/pkg/agent.(*workflowAgentRunner).runManagedSplit", 3, "req.Output"):                           {},
	p015MethodValueKey("pkg/agent/workflow_managed.go", "github.com/sipeed/picoclaw/pkg/agent.(*workflowAgentRunner).runManagedSplit", 4, "validation.Error"):                     {},
	p015MethodValueKey("pkg/agent/workflow_managed.go", "github.com/sipeed/picoclaw/pkg/agent.(*workflowAgentRunner).runManagedSplit", 5, "validation.Error"):                     {},
	p015MethodValueKey("pkg/agent/workflow_managed.go", "github.com/sipeed/picoclaw/pkg/agent.(*workflowAgentRunner).runManagedSplit", 6, "validation.Error"):                     {},
	p015MethodValueKey("pkg/agent/workflow_managed.go", "github.com/sipeed/picoclaw/pkg/agent.(*workflowAgentRunner).runManagedSplitCalibration", 1, "req.Output"):                {},
	p015MethodValueKey("pkg/agent/workflow_managed.go", "github.com/sipeed/picoclaw/pkg/agent.workflowManagedCalibrationCacheKey", 1, "req.Output"):                               {},
	p015MethodValueKey("pkg/agent/workflow_managed.go", "github.com/sipeed/picoclaw/pkg/agent.workflowManagedCalibrationTargetIdentity", 1, "req.Output"):                         {},
	p015MethodValueKey("pkg/agent/workflow_runtime.go", "github.com/sipeed/picoclaw/pkg/agent.(*workflowAgentRunner).RunAgent", 2, "req.Output"):                                  {},
	p015MethodValueKey("pkg/agent/workflow_runtime.go", "github.com/sipeed/picoclaw/pkg/agent.(*workflowAgentRunner).RunAgent", 3, "req.Output"):                                  {},
	p015MethodValueKey("pkg/agent/workflow_runtime.go", "github.com/sipeed/picoclaw/pkg/agent.(*workflowAgentRunner).RunAgent", 4, "structured.Error"):                            {},
	p015MethodValueKey("pkg/agent/workflow_runtime.go", "github.com/sipeed/picoclaw/pkg/agent.(*workflowAgentRunner).RunAgent", 5, "structured.Error"):                            {},
	p015MethodValueKey("pkg/agent/workflow_runtime.go", "github.com/sipeed/picoclaw/pkg/agent.(*workflowAgentRunner).RunAgent", 6, "req.Output"):                                  {},
	p015MethodValueKey("pkg/agent/workflow_runtime.go", "github.com/sipeed/picoclaw/pkg/agent.(*workflowAgentRunner).RunAgent", 7, "req.Output"):                                  {},
	p015MethodValueKey("pkg/agent/workflow_runtime.go", "github.com/sipeed/picoclaw/pkg/agent.(*workflowAgentRunner).RunAgent", 8, "structured.Error"):                            {},
	p015MethodValueKey("pkg/agent/workflow_runtime.go", "github.com/sipeed/picoclaw/pkg/agent.(*workflowAgentRunner).RunAgent", 9, "structured.Error"):                            {},
	p015MethodValueKey("pkg/agent/workflow_runtime.go", "github.com/sipeed/picoclaw/pkg/agent.(*workflowAgentRunner).RunAgent", 10, "structured.Error"):                           {},
	p015MethodValueKey("pkg/agent/workflow_runtime.go", "github.com/sipeed/picoclaw/pkg/agent.workflowRunStructuredAgentWithOptions", 1, "structured.Error"):                      {},
	p015MethodValueKey("pkg/agent/workflow_runtime.go", "github.com/sipeed/picoclaw/pkg/agent.workflowRunStructuredAgentWithOptions", 2, "structured.Error"):                      {},
	p015MethodValueKey("pkg/agent/workflow_runtime.go", "github.com/sipeed/picoclaw/pkg/agent.workflowAgentMessage", 1, "req.Output"):                                             {},
	p015MethodValueKey("pkg/agent/workflow_runtime.go", "github.com/sipeed/picoclaw/pkg/agent.(*workflowAgentRunner).RunAgent", 1, "req.Output"):                                  {},
	p015MethodValueKey("pkg/agent/workflow_triggers.go", "github.com/sipeed/picoclaw/pkg/agent.(*AgentLoop).handleWorkflowTriggers", 1, "def.Error"):                              {},
	p015MethodValueKey("pkg/gateway/events.go", "github.com/sipeed/picoclaw/pkg/gateway.gatewayEventAttrs", 1, "payload.Error"):                                                   {},
	p015MethodValueKey("pkg/gateway/events.go", "github.com/sipeed/picoclaw/pkg/gateway.gatewayEventAttrs", 2, "payload.Error"):                                                   {},
	p015MethodValueKey("pkg/gateway/pr_workspace_implementation.go", "github.com/sipeed/picoclaw/pkg/gateway.(*prWorkspaceImplementationRuntime).Validate", 1, "step.Output"):     {},
	p015MethodValueKey("pkg/gateway/pr_workspace_provider.go", "github.com/sipeed/picoclaw/pkg/gateway.(*prWorkspaceGitHubResolver).ResolveIssue", 2, "issue.User.Login"):         {},
	p015MethodValueKey("pkg/gateway/pr_workspace_provider.go", "github.com/sipeed/picoclaw/pkg/gateway.(*prWorkspaceGitHubResolver).resolveFeatureRepository", 1, "viewer.Login"): {},
	p015MethodValueKey("pkg/gateway/pr_workspace_provider.go", "github.com/sipeed/picoclaw/pkg/gateway.(*prWorkspaceGitHubResolver).resolveFeatureRepository", 2, "viewer.Login"): {},
	p015MethodValueKey("pkg/gateway/pr_workspace_provider.go", "github.com/sipeed/picoclaw/pkg/gateway.(*prWorkspaceGitHubResolver).ResolvePullRequest", 1, "pull.User.Login"):    {},
	p015MethodValueKey("pkg/gateway/pr_workspace_provider.go", "github.com/sipeed/picoclaw/pkg/gateway.(*prWorkspaceGitHubResolver).ResolvePullRequest", 2, "viewer.Login"):       {},
	p015MethodValueKey("pkg/gateway/pr_workspace_provider.go", "github.com/sipeed/picoclaw/pkg/gateway.(*prWorkspaceGitHubResolver).ResolvePullRequest", 3, "viewer.Login"):       {},
	p015MethodValueKey("pkg/gateway/pr_workspace_provider.go", "github.com/sipeed/picoclaw/pkg/gateway.(*prWorkspaceGitHubResolver).ResolvePullRequest", 4, "pull.User.Login"):    {},
	p015MethodValueKey("pkg/gateway/pr_workspace_provider.go", "github.com/sipeed/picoclaw/pkg/gateway.(*prWorkspaceGitHubResolver).ResolvePullRequest", 5, "pull.User.Login"):    {},
	p015MethodValueKey("pkg/gateway/pr_workspace_provider.go", "github.com/sipeed/picoclaw/pkg/gateway.exactPRWorkspaceRepository", 1, "candidate.Owner.Login"):                   {},
	p015MethodValueKey("pkg/gateway/pr_workspace_provider.go", "github.com/sipeed/picoclaw/pkg/gateway.(*prWorkspaceGitHubResolver).ResolveIssue", 1, "issue.User.Login"):         {},
}

func (scanner *p015ParsedLoggingFile) importedSelector(selector *ast.SelectorExpr) (string, bool) {
	identifier, ok := p015UnwrapParens(selector.X).(*ast.Ident)
	if !ok || identifier.Obj != nil {
		return "", false
	}
	importPath, exists := scanner.imports[identifier.Name]
	return importPath, exists
}

func (scanner *p015ParsedLoggingFile) addEscape(position token.Pos, owner, reason string) {
	scanner.escapes = append(scanner.escapes, p015LoggingEscape{
		File:   scanner.relative,
		Line:   scanner.fileSet.Position(position).Line,
		Owner:  owner,
		Reason: reason,
	})
}

func p015PicoLegacyEmitter(name string) bool {
	return p015NameIn(
		name,
		"Debug", "DebugC", "DebugCF", "DebugF", "Debugf",
		"Info", "InfoC", "InfoCF", "InfoF", "Infof",
		"Warn", "WarnC", "WarnCF", "WarnF", "Warnf",
		"Error", "ErrorC", "ErrorCF", "ErrorF", "Errorf",
		"Fatal", "FatalC", "FatalCF", "FatalF", "Fatalf",
		"RecoverPanic", "RecoverPanicNoExit",
	)
}

func p015PicoSafeEmitter(name string) bool {
	return p015NameIn(
		name,
		"DebugSafeCF",
		"DebugSensitiveCF",
		"InfoSafeCF",
		"WarnSafeCF",
		"ErrorSafeCF",
		"FatalSafeCF",
	)
}

func p015StandardLoggingEmitter(importPath, name string) bool {
	if importPath == "log" {
		return p015NameIn(
			name,
			"Print", "Printf", "Println",
			"Fatal", "Fatalf", "Fatalln",
			"Panic", "Panicf", "Panicln", "Output",
		)
	}
	return p015NameIn(
		name,
		"Debug", "DebugContext",
		"Info", "InfoContext",
		"Warn", "WarnContext",
		"Error", "ErrorContext",
		"Log", "LogAttrs",
	)
}

func p015OutputFunction(importPath, name string) bool {
	switch importPath {
	case p015PicoLoggerImport:
		return p015PicoLegacyEmitter(name) || p015PicoSafeEmitter(name) || name == "InitPanic"
	case "fmt":
		return p015NameIn(name, "Print", "Printf", "Println", "Fprint", "Fprintf", "Fprintln")
	case "io":
		return p015NameIn(name, "Copy", "CopyN", "WriteString")
	case "log", "log/slog":
		return p015StandardLoggingEmitter(importPath, name)
	case "runtime/debug":
		return name == "PrintStack" || name == "Stack"
	default:
		return p015ForbiddenLoggingImport(importPath)
	}
}

func p015NameIn(name string, values ...string) bool {
	for _, value := range values {
		if name == value {
			return true
		}
	}
	return false
}

func p015UnwrapParens(expression ast.Expr) ast.Expr {
	for {
		parenthesized, ok := expression.(*ast.ParenExpr)
		if !ok {
			return expression
		}
		expression = parenthesized.X
	}
}

func p015RenderNode(fileSet *token.FileSet, node any) string {
	var output bytes.Buffer
	if err := format.Node(&output, fileSet, node); err != nil {
		panic(err)
	}
	return strings.TrimSpace(output.String())
}
