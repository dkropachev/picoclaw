package main

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"go/ast"
	"go/format"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
	"unicode"
)

const (
	p015B3RuntimeAdmissionManifestPath = "scripts/testdata/p015b3_runtime_admissions.tsv"
	p015B3RuntimeRootManifestPath      = "scripts/testdata/p015b3_runtime_roots.tsv"
	p015B3PausedCallbackManifestPath   = "scripts/testdata/p015b3_paused_callbacks.tsv"
	p015B3SensitivePolicyManifestPath  = "scripts/testdata/p015b3_sensitive_policy_sources.tsv"
	p015B3ATestManifestPath            = "scripts/testdata/p015b3a_test_manifest.tsv"
)

type p015B3ParsedFile struct {
	path    string
	fileSet *token.FileSet
	file    *ast.File
	source  []byte
}

type p015B3CallSite struct {
	File       string
	Function   string
	Ordinal    int
	Callee     string
	Mode       string
	Expression string
}

func (site p015B3CallSite) identity() string {
	return strings.Join([]string{
		site.File,
		site.Function,
		strconv.Itoa(site.Ordinal),
		site.Callee,
		site.Mode,
		site.Expression,
	}, "\x00")
}

type p015B3RootSite struct {
	File     string
	Function string
	Callee   string
}

func (site p015B3RootSite) identity() string {
	return strings.Join([]string{site.File, site.Function, site.Callee}, "\x00")
}

type p015B3PausedCallbackSite struct {
	File     string
	Function string
	Ordinal  int
	Callee   string
}

func (site p015B3PausedCallbackSite) identity() string {
	return strings.Join([]string{
		site.File,
		site.Function,
		strconv.Itoa(site.Ordinal),
		site.Callee,
	}, "\x00")
}

type p015B3SensitivePolicySite struct {
	File     string
	Function string
	Ordinal  int
	Callee   string
	Policy   string
}

type p015B3ATestSite struct {
	File string
	Name string
}

func (site p015B3ATestSite) identity() string {
	return site.File + "\x00" + site.Name
}

func (site p015B3SensitivePolicySite) identity() string {
	return strings.Join([]string{
		site.File,
		site.Function,
		strconv.Itoa(site.Ordinal),
		site.Callee,
		site.Policy,
	}, "\x00")
}

func TestP015B3ARuntimePolicyStructureIsClosed(t *testing.T) {
	repoRoot := p015B3FindRepoRoot(t)
	parsed, err := p015B3ParseProductionGo(repoRoot, nil)
	if err != nil {
		t.Fatal(err)
	}

	admissionManifest := p015B3ReadAdmissionManifest(
		t,
		filepath.Join(repoRoot, filepath.FromSlash(p015B3RuntimeAdmissionManifestPath)),
	)
	rootManifest := p015B3ReadRootManifest(
		t,
		filepath.Join(repoRoot, filepath.FromSlash(p015B3RuntimeRootManifestPath)),
	)
	pausedManifest := p015B3ReadPausedCallbackManifest(
		t,
		filepath.Join(repoRoot, filepath.FromSlash(p015B3PausedCallbackManifestPath)),
	)
	sensitiveManifest := p015B3ReadSensitivePolicyManifest(
		t,
		filepath.Join(repoRoot, filepath.FromSlash(p015B3SensitivePolicyManifestPath)),
	)

	issues := make([]string, 0, 16)
	issues = append(issues, p015B3AdmissionIssues(parsed, admissionManifest)...)
	issues = append(issues, p015B3RootIssues(parsed, rootManifest)...)
	issues = append(issues, p015B3PausedCallbackIssues(parsed, pausedManifest)...)
	issues = append(issues, p015B3SensitivePolicyIssues(parsed, sensitiveManifest)...)
	issues = append(issues, p015B3NoProductionDiagnosticPolicyConstructorIssues(parsed)...)
	issues = append(issues, p015B3ForwardPublicationIssues(parsed)...)
	issues = append(issues, p015B3RetainedRollbackIssues(parsed)...)
	issues = append(issues, p015B3ProviderGenerationIssues(parsed)...)
	issues = append(issues, p015B3RuntimeLeaseCoreIssues(parsed)...)
	issues = append(issues, p015B3LateWorkProvenanceIssues(parsed)...)
	issues = append(issues, p015B3StrictConstructorIssues(parsed)...)
	issues = append(issues, p015B3PromptAuthorityIssues(parsed)...)
	if len(issues) != 0 {
		sort.Strings(issues)
		t.Fatalf("P015b3 runtime-policy structure drifted:\n%s", strings.Join(issues, "\n"))
	}
}

func TestP015B3ARuntimePolicyGateRejectsSyntheticMutations(t *testing.T) {
	repoRoot := p015B3FindRepoRoot(t)
	baseline, err := p015B3ParseProductionGo(repoRoot, nil)
	if err != nil {
		t.Fatal(err)
	}
	admissionManifest := p015B3ReadAdmissionManifest(
		t,
		filepath.Join(repoRoot, filepath.FromSlash(p015B3RuntimeAdmissionManifestPath)),
	)
	rootManifest := p015B3ReadRootManifest(
		t,
		filepath.Join(repoRoot, filepath.FromSlash(p015B3RuntimeRootManifestPath)),
	)
	pausedManifest := p015B3ReadPausedCallbackManifest(
		t,
		filepath.Join(repoRoot, filepath.FromSlash(p015B3PausedCallbackManifestPath)),
	)
	sensitiveManifest := p015B3ReadSensitivePolicyManifest(
		t,
		filepath.Join(repoRoot, filepath.FromSlash(p015B3SensitivePolicyManifestPath)),
	)

	tests := []struct {
		name        string
		path        string
		old         string
		replacement string
		issues      func(map[string]*p015B3ParsedFile) []string
	}{
		{
			name:        "tracked route stores raw context",
			path:        "pkg/agent/subagent_result_mailbox.go",
			old:         "type trackedSubagentResultRoute struct {\n",
			replacement: "type trackedSubagentResultRoute struct {\n\tparentContext context.Context\n",
			issues:      p015B3LateWorkProvenanceIssues,
		},
		{
			name: "tracked route clone drops origin",
			path: "pkg/agent/subagent_result_mailbox.go",
			old: "func cloneTrackedSubagentResultRoute(\n" +
				"\troute trackedSubagentResultRoute,\n" +
				") trackedSubagentResultRoute {",
			replacement: "func cloneTrackedSubagentResultRoute(\n" +
				"\troute trackedSubagentResultRoute,\n" +
				") trackedSubagentResultRoute {\n\troute.diagnosticOrigin = runtimeDiagnosticOrigin{}",
			issues: p015B3LateWorkProvenanceIssues,
		},
		{
			name:        "tracked pump drops origin",
			path:        "pkg/agent/subagent_result_mailbox.go",
			old:         "route.diagnosticOrigin,\n\t)",
			replacement: "runtimeDiagnosticOrigin{},\n\t)",
			issues:      p015B3LateWorkProvenanceIssues,
		},
		{
			name:        "steering rescue drops origin",
			path:        "pkg/agent/turn_state.go",
			old:         "diagnosticOrigin: origin,",
			replacement: "diagnosticOrigin: runtimeDiagnosticOrigin{},",
			issues:      p015B3LateWorkProvenanceIssues,
		},
		{
			name: "tuple snapshot before counted admission",
			path: "pkg/agent/runtime_gate.go",
			old: "if err := al.incrementRuntimeAdmission(ctx); err != nil {\n" +
				"\t\treturn ctx, func() {}, err\n" +
				"\t}\n" +
				"\tgeneration, err := al.snapshotRuntimeGeneration()",
			replacement: "generation, err := al.snapshotRuntimeGeneration()\n" +
				"\tif err := al.incrementRuntimeAdmission(ctx); err != nil {\n" +
				"\t\treturn ctx, func() {}, err\n" +
				"\t}",
			issues: p015B3RuntimeLeaseCoreIssues,
		},
		{
			name: "release boundary before diagnostic revoke",
			path: "pkg/agent/runtime_gate.go",
			old: "if revokeDiagnostic != nil {\n" +
				"\t\t\t\trevokeDiagnostic()\n" +
				"\t\t\t}\n" +
				"\t\t\tif boundary != nil {\n" +
				"\t\t\t\tboundary.active.Store(false)\n" +
				"\t\t\t}",
			replacement: "if boundary != nil {\n" +
				"\t\t\t\tboundary.active.Store(false)\n" +
				"\t\t\t}\n" +
				"\t\t\tif revokeDiagnostic != nil {\n" +
				"\t\t\t\trevokeDiagnostic()\n" +
				"\t\t\t}",
			issues: p015B3RuntimeLeaseCoreIssues,
		},
		{
			name: "release decrement before diagnostic revoke",
			path: "pkg/agent/runtime_gate.go",
			old: "if revokeDiagnostic != nil {\n" +
				"\t\t\t\trevokeDiagnostic()\n" +
				"\t\t\t}\n" +
				"\t\t\tif boundary != nil {\n" +
				"\t\t\t\tboundary.active.Store(false)\n" +
				"\t\t\t}\n" +
				"\t\t\tif counted && al.runtimeGateActive > 0 {\n" +
				"\t\t\t\tal.runtimeGateActive--\n" +
				"\t\t\t}",
			replacement: "if counted && al.runtimeGateActive > 0 {\n" +
				"\t\t\t\tal.runtimeGateActive--\n" +
				"\t\t\t}\n" +
				"\t\t\tif revokeDiagnostic != nil {\n" +
				"\t\t\t\trevokeDiagnostic()\n" +
				"\t\t\t}\n" +
				"\t\t\tif boundary != nil {\n" +
				"\t\t\t\tboundary.active.Store(false)\n" +
				"\t\t\t}",
			issues: p015B3RuntimeLeaseCoreIssues,
		},
		{
			name:        "retain bind root",
			path:        "pkg/agent/runtime_gate.go",
			old:         "logger.RebindDiagnosticPolicy(\n",
			replacement: "logger.BindRootDiagnosticPolicy(\n",
			issues:      p015B3RuntimeLeaseCoreIssues,
		},
		{
			name: "retain unlock before active publication",
			path: "pkg/agent/runtime_gate.go",
			old: "value.generation.diagnosticPolicy,\n" +
				"\t)\n" +
				"\tal.runtimeGateActive++\n" +
				"\tal.signalRuntimeGateChangedLocked()\n" +
				"\tal.runtimeGateMu.Unlock()",
			replacement: "value.generation.diagnosticPolicy,\n" +
				"\t)\n" +
				"\tal.runtimeGateMu.Unlock()\n" +
				"\tal.runtimeGateActive++\n" +
				"\tal.signalRuntimeGateChangedLocked()",
			issues: p015B3RuntimeLeaseCoreIssues,
		},
		{
			name: "detached generation public rebind",
			path: "pkg/agent/runtime_gate.go",
			old: "return logger.BindRootDiagnosticPolicy(ctx, logger.DiagnosticPolicy{})\n" +
				"}\n\nfunc bindDetachedRuntimeDiagnostic",
			replacement: "return logger.RebindDiagnosticPolicy(ctx, ctx, logger.NewDiagnosticPolicy(true, logger.DEBUG))\n" +
				"}\n\nfunc bindDetachedRuntimeDiagnostic",
			issues: p015B3RuntimeLeaseCoreIssues,
		},
		{
			name:        "pause child loses parent boundary",
			path:        "pkg/agent/runtime_gate.go",
			old:         "boundary := newRuntimeLeaseBoundary(parent.boundary)",
			replacement: "boundary := newRuntimeLeaseBoundary(nil)",
			issues:      p015B3RuntimeLeaseCoreIssues,
		},
		{
			name:        "pause child increments active count",
			path:        "pkg/agent/runtime_gate.go",
			old:         "al.newRuntimeLeaseRelease(boundary, revokeDiagnostic, false)",
			replacement: "al.newRuntimeLeaseRelease(boundary, revokeDiagnostic, true)",
			issues:      p015B3RuntimeLeaseCoreIssues,
		},
		{
			name: "local repair validates after effects",
			path: "pkg/agent/local_repair.go",
			old:  "if runner.strictRuntime {",
			replacement: "_ = runner.workspaces.WithPinnedOperation(ctx, request.Pin, func(context.Context) error { return nil })\n" +
				"\tif runner.strictRuntime {",
			issues: p015B3RuntimeLeaseCoreIssues,
		},
		{
			name: "root diagnostic authority",
			path: "cmd/picoclaw/internal/agent/helpers.go",
			old:  "logger.DiagnosticPolicy{},",
			replacement: "logger.NewDiagnosticPolicy(" +
				"true, logger.DebugLevel),",
			issues: func(files map[string]*p015B3ParsedFile) []string {
				return append(
					p015B3RootIssues(files, rootManifest),
					p015B3NoProductionDiagnosticPolicyConstructorIssues(files)...,
				)
			},
		},
		{
			name:        "forward publication authority",
			path:        "pkg/gateway/gateway.go",
			old:         "logger.DiagnosticPolicy{},\n\t)",
			replacement: "logger.DiagnosticPolicyFromContext(reloadCtx),\n\t)",
			issues:      p015B3ForwardPublicationIssues,
		},
		{
			name:        "rollback diagnostic bundle",
			path:        "pkg/agent/runtime_reload_transaction.go",
			old:         "retained.diagnosticPolicy,",
			replacement: "logger.DiagnosticPolicy{},",
			issues:      p015B3RetainedRollbackIssues,
		},
		{
			name:        "complete provider generation snapshot",
			path:        "pkg/agent/agent.go",
			old:         "oldProviderGeneration = snapshotAgentRegistryProviderGeneration(oldRegistry)",
			replacement: "oldProviderGeneration = nil",
			issues:      p015B3ProviderGenerationIssues,
		},
		{
			name:        "provider generation drops light provider",
			path:        "pkg/agent/runtime_provider_generation.go",
			old:         "appendProvider(instance.LightProvider)",
			replacement: "appendProvider(instance.Provider)",
			issues:      p015B3ProviderGenerationIssues,
		},
		{
			name:        "provider generation drops direct light binding",
			path:        "pkg/agent/runtime_provider_generation.go",
			old:         "light:   instance.LightProvider,",
			replacement: "light:   nil,",
			issues:      p015B3ProviderGenerationIssues,
		},
		{
			name:        "provider generation loses alias deduplication",
			path:        "pkg/agent/runtime_provider_generation.go",
			old:         "if sameLLMProvider(existing, provider) {",
			replacement: "if false {",
			issues:      p015B3ProviderGenerationIssues,
		},
		{
			name: "provider close outside reload locks",
			path: "pkg/agent/runtime_reload_transaction.go",
			old: "transaction.mu.Unlock()\n" +
				"\towner.reloadMu.Unlock()\n" +
				"\tproviderGeneration.closeAll()",
			replacement: "providerGeneration.closeAll()\n" +
				"\ttransaction.mu.Unlock()\n" +
				"\towner.reloadMu.Unlock()",
			issues: p015B3ProviderGenerationIssues,
		},
		{
			name:        "strict registry construction",
			path:        "pkg/agent/agent_init.go",
			old:         "NewAgentRegistryWithRuntimePolicies(\n",
			replacement: "NewAgentRegistryWithExecutionPolicy(\n",
			issues:      p015B3StrictConstructorIssues,
		},
		{
			name:        "tool registry diagnostic cap",
			path:        "pkg/agent/instance.go",
			old:         "tools.NewToolRegistryWithDiagnosticPolicy(diagnosticPolicy)",
			replacement: "tools.NewToolRegistry()",
			issues:      p015B3StrictConstructorIssues,
		},
		{
			name:        "admission mode",
			path:        "pkg/agent/agent_message.go",
			old:         "al.acquireTrustedRuntimeRoot(ctx)",
			replacement: "al.acquireDetachedRuntimeUse(ctx)",
			issues: func(files map[string]*p015B3ParsedFile) []string {
				return p015B3AdmissionIssues(files, admissionManifest)
			},
		},
		{
			name:        "paused callback allowlist",
			path:        "pkg/agent/agent.go",
			old:         "al.withPausedRuntimeGeneration(\n",
			replacement: "al.WithPausedRuntimeGeneration(\n",
			issues: func(files map[string]*p015B3ParsedFile) []string {
				return p015B3PausedCallbackIssues(files, pausedManifest)
			},
		},
		{
			name: "sensitive sink policy source",
			path: "pkg/agent/pipeline_llm.go",
			old: "logger.DebugSensitiveCF(\n" +
				"\t\tts.diagnosticPolicy,\n",
			replacement: "logger.DebugSensitiveCF(\n" +
				"\t\tlogger.DiagnosticPolicy{},\n",
			issues: func(files map[string]*p015B3ParsedFile) []string {
				return p015B3SensitivePolicyIssues(files, sensitiveManifest)
			},
		},
		{
			name:        "prompt request authority field",
			path:        "pkg/agent/prompt.go",
			old:         "type PromptBuildRequest struct {\n",
			replacement: "type PromptBuildRequest struct {\n\tDiagnosticPolicy logger.DiagnosticPolicy\n",
			issues:      p015B3PromptAuthorityIssues,
		},
		{
			name:        "public prompt authority",
			path:        "pkg/agent/context.go",
			old:         "\t\tlogger.DiagnosticPolicy{},\n",
			replacement: "\t\tlogger.DiagnosticPolicyFromContext(context.Background()),\n",
			issues:      p015B3PromptAuthorityIssues,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			original := baseline[test.path]
			if original == nil {
				t.Fatalf("fixture source %q is unavailable", test.path)
			}
			mutated := strings.Replace(string(original.source), test.old, test.replacement, 1)
			if mutated == string(original.source) {
				t.Fatalf("mutation needle was not found in %s", test.path)
			}
			parsedMutation, parseErr := p015B3ParseFile(test.path, []byte(mutated))
			if parseErr != nil {
				t.Fatalf("parse mutation: %v", parseErr)
			}
			candidate := make(map[string]*p015B3ParsedFile, len(baseline))
			for path, file := range baseline {
				candidate[path] = file
			}
			candidate[test.path] = parsedMutation
			if issues := test.issues(candidate); len(issues) == 0 {
				t.Fatal("synthetic policy mutation was accepted")
			}
		})
	}
	p015B3AssertManifestIdentityMutations(t)
}

func p015B3AssertManifestIdentityMutations(t *testing.T) {
	t.Helper()
	repoRoot := p015B3FindRepoRoot(t)
	admissions := p015B3ReadAdmissionManifest(
		t,
		filepath.Join(repoRoot, filepath.FromSlash(p015B3RuntimeAdmissionManifestPath)),
	)
	roots := p015B3ReadRootManifest(
		t,
		filepath.Join(repoRoot, filepath.FromSlash(p015B3RuntimeRootManifestPath)),
	)
	paused := p015B3ReadPausedCallbackManifest(
		t,
		filepath.Join(repoRoot, filepath.FromSlash(p015B3PausedCallbackManifestPath)),
	)
	sensitive := p015B3ReadSensitivePolicyManifest(
		t,
		filepath.Join(repoRoot, filepath.FromSlash(p015B3SensitivePolicyManifestPath)),
	)

	admissionMutations := map[string]func(*p015B3CallSite){
		"file":       func(site *p015B3CallSite) { site.File += ".mutated" },
		"function":   func(site *p015B3CallSite) { site.Function += ".mutated" },
		"ordinal":    func(site *p015B3CallSite) { site.Ordinal++ },
		"callee":     func(site *p015B3CallSite) { site.Callee += "Mutated" },
		"mode":       func(site *p015B3CallSite) { site.Mode += "_mutated" },
		"expression": func(site *p015B3CallSite) { site.Expression += " /* mutation */" },
	}
	for name, mutate := range admissionMutations {
		t.Run("admission "+name, func(t *testing.T) {
			candidate := append([]p015B3CallSite(nil), admissions...)
			mutate(&candidate[0])
			if issues := p015B3ExactIdentities(
				"runtime admission",
				p015B3CallSiteIdentities(candidate),
				p015B3CallSiteIdentities(admissions),
			); len(issues) == 0 {
				t.Fatal("admission identity mutation was accepted")
			}
		})
	}

	rootMutations := map[string]func(*p015B3RootSite){
		"file":     func(site *p015B3RootSite) { site.File += ".mutated" },
		"function": func(site *p015B3RootSite) { site.Function += ".mutated" },
		"callee":   func(site *p015B3RootSite) { site.Callee += "Mutated" },
	}
	for name, mutate := range rootMutations {
		t.Run("root "+name, func(t *testing.T) {
			candidate := roots[0]
			mutate(&candidate)
			if issues := p015B3ExactIdentities(
				"runtime root",
				[]string{candidate.identity()},
				[]string{roots[0].identity()},
			); len(issues) == 0 {
				t.Fatal("root identity mutation was accepted")
			}
		})
	}

	pausedMutations := map[string]func(*p015B3PausedCallbackSite){
		"file":     func(site *p015B3PausedCallbackSite) { site.File += ".mutated" },
		"function": func(site *p015B3PausedCallbackSite) { site.Function += ".mutated" },
		"ordinal":  func(site *p015B3PausedCallbackSite) { site.Ordinal++ },
		"callee":   func(site *p015B3PausedCallbackSite) { site.Callee += "Mutated" },
	}
	for name, mutate := range pausedMutations {
		t.Run("paused "+name, func(t *testing.T) {
			candidate := paused[0]
			mutate(&candidate)
			if issues := p015B3ExactIdentities(
				"paused callback",
				[]string{candidate.identity()},
				[]string{paused[0].identity()},
			); len(issues) == 0 {
				t.Fatal("paused callback identity mutation was accepted")
			}
		})
	}

	sensitiveMutations := map[string]func(*p015B3SensitivePolicySite){
		"file":     func(site *p015B3SensitivePolicySite) { site.File += ".mutated" },
		"function": func(site *p015B3SensitivePolicySite) { site.Function += ".mutated" },
		"ordinal":  func(site *p015B3SensitivePolicySite) { site.Ordinal++ },
		"callee":   func(site *p015B3SensitivePolicySite) { site.Callee += "Mutated" },
		"policy":   func(site *p015B3SensitivePolicySite) { site.Policy += " /* mutation */" },
	}
	for name, mutate := range sensitiveMutations {
		t.Run("sensitive "+name, func(t *testing.T) {
			candidate := sensitive[0]
			mutate(&candidate)
			if issues := p015B3ExactIdentities(
				"sensitive policy source",
				[]string{candidate.identity()},
				[]string{sensitive[0].identity()},
			); len(issues) == 0 {
				t.Fatal("sensitive policy identity mutation was accepted")
			}
		})
	}
}

func TestP015B3ATestManifest(t *testing.T) {
	repoRoot := p015B3FindRepoRoot(t)
	want := p015B3ReadTestManifest(
		t,
		filepath.Join(repoRoot, filepath.FromSlash(p015B3ATestManifestPath)),
	)
	got, executableIssues, err := p015B3ScanTests(repoRoot, nil)
	if err != nil {
		t.Fatal(err)
	}
	if issues := p015B3TestManifestIssues(got, want, executableIssues); len(issues) != 0 {
		t.Fatalf("P015b3a executable test manifest drifted:\n%s", strings.Join(issues, "\n"))
	}

	tests := []struct {
		name      string
		candidate []p015B3ATestSite
	}{
		{name: "missing", candidate: append([]p015B3ATestSite(nil), want[:len(want)-1]...)},
		{name: "extra", candidate: append(
			append([]p015B3ATestSite(nil), want...),
			p015B3ATestSite{File: "pkg/agent/missing_test.go", Name: "TestP015B3AMissing"},
		)},
		{name: "duplicate", candidate: append(
			append([]p015B3ATestSite(nil), want...),
			want[0],
		)},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if issues := p015B3TestManifestIssues(got, test.candidate, nil); len(issues) == 0 {
				t.Fatal("mutated P015b3a test manifest was accepted")
			}
		})
	}

	const testPath = "scripts/p015b3_runtime_policy_gate_test.go"
	source, err := os.ReadFile(filepath.Join(repoRoot, filepath.FromSlash(testPath)))
	if err != nil {
		t.Fatal(err)
	}
	mutated := strings.Replace(
		string(source),
		"func TestP015B3ATestManifest(t *testing.T) {",
		"func TestP015B3ATestManifest() {",
		1,
	)
	if mutated == string(source) {
		t.Fatal("non-executable P015b3a test mutation did not change source")
	}
	_, mutatedExecutableIssues, err := p015B3ScanTests(
		repoRoot,
		map[string][]byte{testPath: []byte(mutated)},
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(mutatedExecutableIssues) == 0 {
		t.Fatal("non-executable P015b3a test mutation was accepted")
	}
	constrained := "//go:build p015b3_never\n\n" + string(source)
	_, constrainedIssues, err := p015B3ScanTests(
		repoRoot,
		map[string][]byte{testPath: []byte(constrained)},
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(constrainedIssues) == 0 {
		t.Fatal("build-constrained P015b3a test mutation was accepted")
	}
}

func p015B3ParseProductionGo(
	repoRoot string,
	overrides map[string][]byte,
) (map[string]*p015B3ParsedFile, error) {
	parsed := make(map[string]*p015B3ParsedFile)
	err := filepath.WalkDir(repoRoot, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		relative, relErr := filepath.Rel(repoRoot, path)
		if relErr != nil {
			return relErr
		}
		relative = filepath.ToSlash(relative)
		if entry.IsDir() {
			if (relative != "." && strings.HasPrefix(filepath.Base(relative), ".")) ||
				relative == "vendor" ||
				strings.HasPrefix(relative, "web/frontend/node_modules") ||
				strings.HasPrefix(relative, "scripts/testdata") {
				return filepath.SkipDir
			}
			return nil
		}
		if filepath.Ext(path) != ".go" || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		source, overridden := overrides[relative]
		if !overridden {
			var readErr error
			source, readErr = os.ReadFile(path)
			if readErr != nil {
				return readErr
			}
		}
		file, parseErr := p015B3ParseFile(relative, source)
		if parseErr != nil {
			return parseErr
		}
		parsed[relative] = file
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("scan production Go source: %w", err)
	}
	return parsed, nil
}

func p015B3ScanTests(
	repoRoot string,
	overrides map[string][]byte,
) ([]p015B3ATestSite, []string, error) {
	var sites []p015B3ATestSite
	var issues []string
	for _, root := range []string{"pkg/agent", "pkg/gateway", "scripts"} {
		absoluteRoot := filepath.Join(repoRoot, filepath.FromSlash(root))
		err := filepath.WalkDir(
			absoluteRoot,
			func(path string, entry fs.DirEntry, walkErr error) error {
				if walkErr != nil {
					return walkErr
				}
				relative, relErr := filepath.Rel(repoRoot, path)
				if relErr != nil {
					return relErr
				}
				relative = filepath.ToSlash(relative)
				if entry.IsDir() {
					if strings.HasPrefix(relative, "scripts/testdata") ||
						strings.HasPrefix(filepath.Base(relative), ".") {
						return filepath.SkipDir
					}
					return nil
				}
				if !strings.HasSuffix(relative, "_test.go") {
					return nil
				}
				source, overridden := overrides[relative]
				if !overridden {
					var readErr error
					source, readErr = os.ReadFile(path)
					if readErr != nil {
						return readErr
					}
				}
				parsed, parseErr := p015B3ParseFile(relative, source)
				if parseErr != nil {
					return parseErr
				}
				constraintIssue := p015B3TestFileConstraintIssue(relative, source)
				for _, declaration := range parsed.file.Decls {
					function, ok := declaration.(*ast.FuncDecl)
					if !ok || !strings.HasPrefix(function.Name.Name, "TestP015B3A") {
						continue
					}
					site := p015B3ATestSite{File: relative, Name: function.Name.Name}
					sites = append(sites, site)
					if issue := p015B3ExecutableTestIssue(parsed, function); issue != "" {
						issues = append(issues, site.File+" "+site.Name+": "+issue)
					}
					if constraintIssue != "" {
						issues = append(issues, site.File+" "+site.Name+": "+constraintIssue)
					}
				}
				return nil
			},
		)
		if err != nil {
			return nil, nil, fmt.Errorf("scan P015b3a tests under %s: %w", root, err)
		}
	}
	sort.Slice(sites, func(left, right int) bool {
		return sites[left].identity() < sites[right].identity()
	})
	sort.Strings(issues)
	return sites, issues, nil
}

func p015B3TestFileConstraintIssue(relative string, source []byte) string {
	for _, line := range strings.Split(string(source), "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "package ") {
			break
		}
		if strings.HasPrefix(trimmed, "//go:build") ||
			strings.HasPrefix(trimmed, "// +build") {
			return "test file has a build constraint"
		}
	}
	base := strings.TrimSuffix(filepath.Base(relative), "_test.go")
	for _, suffix := range []string{
		"_aix", "_android", "_darwin", "_dragonfly", "_freebsd", "_illumos",
		"_ios", "_js", "_linux", "_netbsd", "_openbsd", "_plan9", "_solaris",
		"_wasip1", "_windows", "_386", "_amd64", "_arm", "_arm64", "_loong64",
		"_mips", "_mips64", "_mips64le", "_mipsle", "_ppc64", "_ppc64le",
		"_riscv64", "_s390x", "_wasm",
	} {
		if strings.HasSuffix(base, suffix) {
			return "test file is platform constrained by suffix " + suffix
		}
	}
	return ""
}

func p015B3ExecutableTestIssue(
	file *p015B3ParsedFile,
	function *ast.FuncDecl,
) string {
	if function.Recv != nil {
		return "test has a receiver"
	}
	if function.Body == nil {
		return "test has no body"
	}
	if function.Type.TypeParams != nil && len(function.Type.TypeParams.List) != 0 {
		return "test has type parameters"
	}
	if function.Type.Params == nil || len(function.Type.Params.List) != 1 ||
		len(function.Type.Params.List[0].Names) != 1 ||
		p015B3Render(file.fileSet, function.Type.Params.List[0].Type) != "*testing.T" {
		return "test does not have one *testing.T parameter"
	}
	if function.Type.Results != nil && len(function.Type.Results.List) != 0 {
		return "test returns values"
	}
	return ""
}

func p015B3TestManifestIssues(
	got []p015B3ATestSite,
	want []p015B3ATestSite,
	executableIssues []string,
) []string {
	issues := append([]string(nil), executableIssues...)
	gotIdentities := make([]string, 0, len(got))
	wantIdentities := make([]string, 0, len(want))
	for _, site := range got {
		gotIdentities = append(gotIdentities, site.identity())
	}
	for _, site := range want {
		wantIdentities = append(wantIdentities, site.identity())
	}
	issues = append(
		issues,
		p015B3ExactIdentities("P015b3a test", gotIdentities, wantIdentities)...,
	)
	sort.Strings(issues)
	return issues
}

func p015B3FindRepoRoot(t *testing.T) string {
	t.Helper()
	current, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		moduleData, readErr := os.ReadFile(filepath.Join(current, "go.mod"))
		if readErr == nil && strings.Contains(
			string(moduleData),
			"module github.com/sipeed/picoclaw",
		) {
			return current
		}
		parent := filepath.Dir(current)
		if parent == current {
			t.Fatal("repository root is unavailable")
		}
		current = parent
	}
}

func p015B3ParseFile(path string, source []byte) (*p015B3ParsedFile, error) {
	fileSet := token.NewFileSet()
	file, err := parser.ParseFile(fileSet, path, source, parser.SkipObjectResolution)
	if err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	return &p015B3ParsedFile{path: path, fileSet: fileSet, file: file, source: source}, nil
}

func p015B3Functions(file *p015B3ParsedFile) map[string]*ast.FuncDecl {
	functions := make(map[string]*ast.FuncDecl)
	for _, declaration := range file.file.Decls {
		function, ok := declaration.(*ast.FuncDecl)
		if !ok {
			continue
		}
		functions[p015B3FunctionName(file.fileSet, function)] = function
	}
	return functions
}

func p015B3FunctionName(fileSet *token.FileSet, function *ast.FuncDecl) string {
	if function.Recv == nil || len(function.Recv.List) != 1 {
		return function.Name.Name
	}
	receiver := p015B3Render(fileSet, function.Recv.List[0].Type)
	if strings.HasPrefix(receiver, "*") {
		receiver = "(" + receiver + ")"
	}
	return receiver + "." + function.Name.Name
}

func p015B3Calls(file *p015B3ParsedFile) []struct {
	function string
	call     *ast.CallExpr
} {
	var calls []struct {
		function string
		call     *ast.CallExpr
	}
	for _, declaration := range file.file.Decls {
		switch declaration := declaration.(type) {
		case *ast.FuncDecl:
			if declaration.Body == nil {
				continue
			}
			function := p015B3FunctionName(file.fileSet, declaration)
			ast.Inspect(declaration.Body, func(node ast.Node) bool {
				if call, ok := node.(*ast.CallExpr); ok {
					calls = append(calls, struct {
						function string
						call     *ast.CallExpr
					}{function: function, call: call})
				}
				return true
			})
		case *ast.GenDecl:
			ast.Inspect(declaration, func(node ast.Node) bool {
				if call, ok := node.(*ast.CallExpr); ok {
					calls = append(calls, struct {
						function string
						call     *ast.CallExpr
					}{function: "<package>", call: call})
				}
				return true
			})
		}
	}
	sort.Slice(calls, func(left, right int) bool {
		return calls[left].call.Pos() < calls[right].call.Pos()
	})
	return calls
}

func p015B3Callee(call *ast.CallExpr) string {
	switch function := call.Fun.(type) {
	case *ast.Ident:
		return function.Name
	case *ast.SelectorExpr:
		return function.Sel.Name
	default:
		return ""
	}
}

func p015B3Render(fileSet *token.FileSet, node any) string {
	var buffer bytes.Buffer
	if err := format.Node(&buffer, fileSet, node); err != nil {
		return fmt.Sprintf("<render %T: %v>", node, err)
	}
	return strings.TrimSpace(buffer.String())
}

func p015B3IsExported(name string) bool {
	for _, character := range name {
		return unicode.IsUpper(character)
	}
	return false
}

func p015B3ReadManifestLines(t *testing.T, path, header string) [][]string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) == 0 || strings.TrimSpace(lines[0]) != header {
		t.Fatalf("%s has invalid header, want %q", path, header)
	}
	var rows [][]string
	for lineNumber, line := range lines[1:] {
		if strings.TrimSpace(line) == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.Split(line, "\t")
		for index := range fields {
			fields[index] = strings.TrimSpace(fields[index])
		}
		if len(fields) == 0 {
			t.Fatalf("%s:%d has no fields", path, lineNumber+2)
		}
		rows = append(rows, fields)
	}
	return rows
}

func p015B3ReadAdmissionManifest(t *testing.T, path string) []p015B3CallSite {
	t.Helper()
	rows := p015B3ReadManifestLines(t, path, "# p015b3-runtime-admissions-v1")
	sites := make([]p015B3CallSite, 0, len(rows))
	seen := make(map[string]struct{}, len(rows))
	for index, row := range rows {
		if len(row) != 6 {
			t.Fatalf("%s row %d has %d fields, want 6", path, index+1, len(row))
		}
		ordinal, err := strconv.Atoi(row[2])
		if err != nil || ordinal <= 0 {
			t.Fatalf("%s row %d has invalid ordinal %q", path, index+1, row[2])
		}
		expression, err := base64.RawURLEncoding.DecodeString(row[5])
		if err != nil {
			t.Fatalf("%s row %d has invalid expression: %v", path, index+1, err)
		}
		site := p015B3CallSite{
			File:       row[0],
			Function:   row[1],
			Ordinal:    ordinal,
			Callee:     row[3],
			Mode:       row[4],
			Expression: string(expression),
		}
		if _, duplicate := seen[site.identity()]; duplicate {
			t.Fatalf("%s row %d duplicates a prior admission", path, index+1)
		}
		seen[site.identity()] = struct{}{}
		sites = append(sites, site)
	}
	if len(sites) != 37 {
		t.Fatalf("%s has %d admissions, want exact 37", path, len(sites))
	}
	return sites
}

func p015B3ReadRootManifest(t *testing.T, path string) []p015B3RootSite {
	t.Helper()
	rows := p015B3ReadManifestLines(t, path, "# p015b3-runtime-roots-v1")
	sites := make([]p015B3RootSite, 0, len(rows))
	seen := make(map[string]struct{}, len(rows))
	for index, row := range rows {
		if len(row) != 3 {
			t.Fatalf("%s row %d has %d fields, want 3", path, index+1, len(row))
		}
		site := p015B3RootSite{File: row[0], Function: row[1], Callee: row[2]}
		if _, duplicate := seen[site.identity()]; duplicate {
			t.Fatalf("%s row %d duplicates a prior root", path, index+1)
		}
		seen[site.identity()] = struct{}{}
		sites = append(sites, site)
	}
	if len(sites) != 5 {
		t.Fatalf("%s has %d roots, want exact 5", path, len(sites))
	}
	return sites
}

func p015B3ReadPausedCallbackManifest(
	t *testing.T,
	path string,
) []p015B3PausedCallbackSite {
	t.Helper()
	rows := p015B3ReadManifestLines(t, path, "# p015b3-paused-callbacks-v1")
	sites := make([]p015B3PausedCallbackSite, 0, len(rows))
	seen := make(map[string]struct{}, len(rows))
	for index, row := range rows {
		if len(row) != 4 {
			t.Fatalf("%s row %d has %d fields, want 4", path, index+1, len(row))
		}
		ordinal, err := strconv.Atoi(row[2])
		if err != nil || ordinal <= 0 {
			t.Fatalf("%s row %d has invalid ordinal %q", path, index+1, row[2])
		}
		site := p015B3PausedCallbackSite{
			File: row[0], Function: row[1], Ordinal: ordinal, Callee: row[3],
		}
		if _, duplicate := seen[site.identity()]; duplicate {
			t.Fatalf("%s row %d duplicates a prior callback", path, index+1)
		}
		seen[site.identity()] = struct{}{}
		sites = append(sites, site)
	}
	if len(sites) != 5 {
		t.Fatalf("%s has %d callbacks, want exact 5", path, len(sites))
	}
	return sites
}

func p015B3ReadSensitivePolicyManifest(
	t *testing.T,
	path string,
) []p015B3SensitivePolicySite {
	t.Helper()
	rows := p015B3ReadManifestLines(t, path, "# p015b3-sensitive-policy-sources-v1")
	sites := make([]p015B3SensitivePolicySite, 0, len(rows))
	seen := make(map[string]struct{}, len(rows))
	for index, row := range rows {
		if len(row) != 5 {
			t.Fatalf("%s row %d has %d fields, want 5", path, index+1, len(row))
		}
		ordinal, err := strconv.Atoi(row[2])
		if err != nil || ordinal <= 0 {
			t.Fatalf("%s row %d has invalid ordinal %q", path, index+1, row[2])
		}
		policy, err := base64.RawURLEncoding.DecodeString(row[4])
		if err != nil {
			t.Fatalf("%s row %d has invalid policy expression: %v", path, index+1, err)
		}
		site := p015B3SensitivePolicySite{
			File: row[0], Function: row[1], Ordinal: ordinal,
			Callee: row[3], Policy: string(policy),
		}
		if _, duplicate := seen[site.identity()]; duplicate {
			t.Fatalf("%s row %d duplicates a prior sensitive policy source", path, index+1)
		}
		seen[site.identity()] = struct{}{}
		sites = append(sites, site)
	}
	if len(sites) != 7 {
		t.Fatalf("%s has %d sensitive policy sources, want exact 7", path, len(sites))
	}
	return sites
}

func p015B3ReadTestManifest(t *testing.T, path string) []p015B3ATestSite {
	t.Helper()
	rows := p015B3ReadManifestLines(t, path, "# p015b3a-test-manifest-v1")
	sites := make([]p015B3ATestSite, 0, len(rows))
	seen := make(map[string]struct{}, len(rows))
	previous := ""
	for index, row := range rows {
		if len(row) != 2 {
			t.Fatalf("%s row %d has %d fields, want 2", path, index+1, len(row))
		}
		site := p015B3ATestSite{File: row[0], Name: row[1]}
		identity := site.identity()
		if site.File == "" || !strings.HasSuffix(site.File, "_test.go") ||
			!strings.HasPrefix(site.Name, "TestP015B3A") {
			t.Fatalf("%s row %d is not an exact P015b3a test identity", path, index+1)
		}
		if _, duplicate := seen[identity]; duplicate {
			t.Fatalf("%s row %d duplicates %s", path, index+1, p015B3ReadableIdentity(identity))
		}
		if previous != "" && identity <= previous {
			t.Fatalf("%s row %d is not strictly sorted", path, index+1)
		}
		seen[identity] = struct{}{}
		previous = identity
		sites = append(sites, site)
	}
	if len(sites) == 0 {
		t.Fatalf("%s contains no P015b3a tests", path)
	}
	return sites
}

var p015B3AdmissionModes = map[string]string{
	"acquireTrustedRuntimeRoot":        "trusted",
	"acquireSteeringRuntimeUse":        "steering",
	"acquireRuntimeUseFromOrigin":      "origin_current",
	"acquireInheritedRuntimeUse":       "inherited",
	"retainRuntimeUse":                 "retain",
	"AcquireRuntimeGeneration":         "exact",
	"AcquireRuntimeStartupUse":         "startup",
	"PauseRuntimeForReload":            "pause",
	"PauseRuntimeForReloadWithContext": "pause_context",
}

var p015B3ExpectedAdmissionModeCounts = map[string]int{
	"trusted":        14,
	"steering":       1,
	"origin_current": 3,
	"inherited":      1,
	"retain":         5,
	"exact":          10,
	"startup":        1,
	"pause":          1,
	"pause_context":  1,
}

func p015B3ScanAdmissions(files map[string]*p015B3ParsedFile) []p015B3CallSite {
	var sites []p015B3CallSite
	ordinals := make(map[string]int)
	for _, path := range p015B3SortedPaths(files) {
		if path == "pkg/agent/runtime_gate.go" {
			continue
		}
		file := files[path]
		for _, ownedCall := range p015B3Calls(file) {
			callee := p015B3Callee(ownedCall.call)
			mode, admission := p015B3AdmissionModes[callee]
			if !admission {
				continue
			}
			ordinalKey := path + "\x00" + ownedCall.function + "\x00" + callee
			ordinals[ordinalKey]++
			sites = append(sites, p015B3CallSite{
				File:       path,
				Function:   ownedCall.function,
				Ordinal:    ordinals[ordinalKey],
				Callee:     callee,
				Mode:       mode,
				Expression: p015B3Render(file.fileSet, ownedCall.call),
			})
		}
	}
	return sites
}

func p015B3AdmissionIssues(
	files map[string]*p015B3ParsedFile,
	want []p015B3CallSite,
) []string {
	got := p015B3ScanAdmissions(files)
	var issues []string
	if len(got) != 37 {
		issues = append(issues, fmt.Sprintf("external runtime admissions = %d, want exact 37", len(got)))
	}
	modeCounts := make(map[string]int)
	for _, site := range got {
		modeCounts[site.Mode]++
	}
	issues = append(
		issues,
		p015B3ExactCounts(
			"runtime admission mode",
			modeCounts,
			p015B3ExpectedAdmissionModeCounts,
		)...,
	)
	issues = append(issues, p015B3ExactIdentities(
		"runtime admission",
		p015B3CallSiteIdentities(got),
		p015B3CallSiteIdentities(want),
	)...)

	for _, path := range p015B3SortedPaths(files) {
		if path == "pkg/agent/runtime_gate.go" {
			continue
		}
		for _, ownedCall := range p015B3Calls(files[path]) {
			switch p015B3Callee(ownedCall.call) {
			case "acquireRuntimeUse":
				issues = append(issues, fmt.Sprintf(
					"%s %s calls compatibility runtime admission", path, ownedCall.function,
				))
			case "acquireDetachedRuntimeUse":
				issues = append(issues, fmt.Sprintf(
					"%s %s calls detached runtime admission outside the gate core", path, ownedCall.function,
				))
			}
		}
	}
	return issues
}

func p015B3CallSiteIdentities(sites []p015B3CallSite) []string {
	identities := make([]string, 0, len(sites))
	for _, site := range sites {
		identities = append(identities, site.identity())
	}
	return identities
}

func p015B3SortedPaths(files map[string]*p015B3ParsedFile) []string {
	paths := make([]string, 0, len(files))
	for path := range files {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	return paths
}

func p015B3ExactCounts(label string, got, want map[string]int) []string {
	var issues []string
	for key, wantCount := range want {
		if got[key] != wantCount {
			issues = append(issues, fmt.Sprintf(
				"%s %q count = %d, want %d", label, key, got[key], wantCount,
			))
		}
	}
	for key, gotCount := range got {
		if _, expected := want[key]; !expected {
			issues = append(issues, fmt.Sprintf(
				"unexpected %s %q count = %d", label, key, gotCount,
			))
		}
	}
	return issues
}

func p015B3ExactIdentities(label string, got, want []string) []string {
	gotCounts := make(map[string]int, len(got))
	wantCounts := make(map[string]int, len(want))
	for _, identity := range got {
		gotCounts[identity]++
	}
	for _, identity := range want {
		wantCounts[identity]++
	}
	var issues []string
	for identity, wantCount := range wantCounts {
		if gotCounts[identity] != wantCount {
			issues = append(issues, fmt.Sprintf(
				"missing/drifted %s %q (got %d, want %d)",
				label, p015B3ReadableIdentity(identity), gotCounts[identity], wantCount,
			))
		}
	}
	for identity, gotCount := range gotCounts {
		if wantCounts[identity] != gotCount {
			issues = append(issues, fmt.Sprintf(
				"unexpected/drifted %s %q (got %d, want %d)",
				label, p015B3ReadableIdentity(identity), gotCount, wantCounts[identity],
			))
		}
	}
	return issues
}

func p015B3ReadableIdentity(identity string) string {
	return strings.ReplaceAll(identity, "\x00", " | ")
}

func p015B3ScanRoots(files map[string]*p015B3ParsedFile) (
	[]p015B3RootSite,
	[]string,
) {
	var sites []p015B3RootSite
	var issues []string
	for _, path := range p015B3SortedPaths(files) {
		file := files[path]
		for _, ownedCall := range p015B3Calls(file) {
			if p015B3Callee(ownedCall.call) != "NewAgentLoopWithRuntimePolicies" {
				continue
			}
			sites = append(sites, p015B3RootSite{
				File: path, Function: ownedCall.function,
				Callee: "NewAgentLoopWithRuntimePolicies",
			})
			if len(ownedCall.call.Args) < 5 ||
				!p015B3LiteralZeroDiagnosticPolicy(file, ownedCall.call.Args[4]) {
				issues = append(issues, fmt.Sprintf(
					"%s %s runtime root does not pass a literal zero diagnostic policy",
					path,
					ownedCall.function,
				))
			}
		}
	}
	return sites, issues
}

func p015B3RootIssues(
	files map[string]*p015B3ParsedFile,
	want []p015B3RootSite,
) []string {
	got, issues := p015B3ScanRoots(files)
	if len(got) != 5 {
		issues = append(issues, fmt.Sprintf("production runtime roots = %d, want exact 5", len(got)))
	}
	gotIdentities := make([]string, 0, len(got))
	wantIdentities := make([]string, 0, len(want))
	for _, site := range got {
		gotIdentities = append(gotIdentities, site.identity())
	}
	for _, site := range want {
		wantIdentities = append(wantIdentities, site.identity())
	}
	return append(issues, p015B3ExactIdentities("runtime root", gotIdentities, wantIdentities)...)
}

func p015B3LiteralZeroDiagnosticPolicy(file *p015B3ParsedFile, expression ast.Expr) bool {
	literal, ok := expression.(*ast.CompositeLit)
	if !ok || len(literal.Elts) != 0 || literal.Type == nil {
		return false
	}
	return p015B3Render(file.fileSet, literal.Type) == "logger.DiagnosticPolicy"
}

func p015B3ScanPausedCallbacks(
	files map[string]*p015B3ParsedFile,
) []p015B3PausedCallbackSite {
	var sites []p015B3PausedCallbackSite
	ordinals := make(map[string]int)
	for _, path := range p015B3SortedPaths(files) {
		if path == "pkg/agent/runtime_gate.go" {
			continue
		}
		file := files[path]
		for _, ownedCall := range p015B3Calls(file) {
			callee := p015B3Callee(ownedCall.call)
			if callee != "withPausedRuntimeGeneration" &&
				callee != "WithPausedRuntimeGeneration" {
				continue
			}
			ordinalKey := path + "\x00" + ownedCall.function + "\x00" + callee
			ordinals[ordinalKey]++
			sites = append(sites, p015B3PausedCallbackSite{
				File: path, Function: ownedCall.function,
				Ordinal: ordinals[ordinalKey], Callee: callee,
			})
		}
	}
	return sites
}

func p015B3PausedCallbackIssues(
	files map[string]*p015B3ParsedFile,
	want []p015B3PausedCallbackSite,
) []string {
	got := p015B3ScanPausedCallbacks(files)
	var issues []string
	if len(got) != 5 {
		issues = append(issues, fmt.Sprintf("subordinate paused callbacks = %d, want exact 5", len(got)))
	}
	gotIdentities := make([]string, 0, len(got))
	wantIdentities := make([]string, 0, len(want))
	for _, site := range got {
		gotIdentities = append(gotIdentities, site.identity())
	}
	for _, site := range want {
		wantIdentities = append(wantIdentities, site.identity())
	}
	return append(
		issues,
		p015B3ExactIdentities("paused callback", gotIdentities, wantIdentities)...,
	)
}

func p015B3ScanSensitivePolicies(
	files map[string]*p015B3ParsedFile,
) []p015B3SensitivePolicySite {
	var sites []p015B3SensitivePolicySite
	ordinals := make(map[string]int)
	for _, path := range p015B3SortedPaths(files) {
		if !strings.HasPrefix(path, "pkg/agent/") {
			continue
		}
		file := files[path]
		for _, ownedCall := range p015B3Calls(file) {
			callee := p015B3Callee(ownedCall.call)
			if !strings.HasSuffix(callee, "SensitiveCF") {
				continue
			}
			policy := "<missing>"
			if len(ownedCall.call.Args) != 0 {
				policy = p015B3Render(file.fileSet, ownedCall.call.Args[0])
			}
			ordinalKey := path + "\x00" + ownedCall.function + "\x00" + callee
			ordinals[ordinalKey]++
			sites = append(sites, p015B3SensitivePolicySite{
				File: path, Function: ownedCall.function,
				Ordinal: ordinals[ordinalKey], Callee: callee, Policy: policy,
			})
		}
	}
	return sites
}

func p015B3SensitivePolicyIssues(
	files map[string]*p015B3ParsedFile,
	want []p015B3SensitivePolicySite,
) []string {
	got := p015B3ScanSensitivePolicies(files)
	var issues []string
	if len(got) != 7 {
		issues = append(issues, fmt.Sprintf("Agent sensitive policy sources = %d, want exact 7", len(got)))
	}
	gotIdentities := make([]string, 0, len(got))
	wantIdentities := make([]string, 0, len(want))
	for _, site := range got {
		gotIdentities = append(gotIdentities, site.identity())
	}
	for _, site := range want {
		wantIdentities = append(wantIdentities, site.identity())
	}
	return append(
		issues,
		p015B3ExactIdentities("sensitive policy source", gotIdentities, wantIdentities)...,
	)
}

func p015B3NoProductionDiagnosticPolicyConstructorIssues(
	files map[string]*p015B3ParsedFile,
) []string {
	var issues []string
	for _, path := range p015B3SortedPaths(files) {
		file := files[path]
		for _, ownedCall := range p015B3Calls(file) {
			if p015B3Callee(ownedCall.call) == "NewDiagnosticPolicy" {
				issues = append(issues, fmt.Sprintf(
					"%s %s constructs production diagnostic authority with NewDiagnosticPolicy",
					path,
					ownedCall.function,
				))
			}
		}
	}
	return issues
}

func p015B3ForwardPublicationIssues(files map[string]*p015B3ParsedFile) []string {
	var issues []string
	var calls []struct {
		file     *p015B3ParsedFile
		function string
		call     *ast.CallExpr
	}
	for _, path := range p015B3SortedPaths(files) {
		file := files[path]
		for _, ownedCall := range p015B3Calls(file) {
			if p015B3Callee(ownedCall.call) == "PublishRetainingPrevious" {
				calls = append(calls, struct {
					file     *p015B3ParsedFile
					function string
					call     *ast.CallExpr
				}{file: file, function: ownedCall.function, call: ownedCall.call})
			}
		}
	}
	if len(calls) != 1 {
		return []string{fmt.Sprintf("forward retained publication calls = %d, want exact 1", len(calls))}
	}
	call := calls[0]
	if call.file.path != "pkg/gateway/gateway.go" ||
		call.function != "handleConfigReloadWithServiceOps" {
		issues = append(issues, fmt.Sprintf(
			"forward retained publication moved to %s %s",
			call.file.path,
			call.function,
		))
	}
	if len(call.call.Args) != 5 ||
		!p015B3LiteralZeroDiagnosticPolicy(call.file, call.call.Args[4]) {
		issues = append(issues, "forward retained publication does not pass an exact literal zero diagnostic policy")
	}
	return issues
}

func p015B3RetainedRollbackIssues(files map[string]*p015B3ParsedFile) []string {
	const path = "pkg/agent/runtime_reload_transaction.go"
	file := files[path]
	if file == nil {
		return []string{"retained runtime implementation is missing"}
	}
	functions := p015B3Functions(file)
	rollback := functions["(*RuntimeReloadTransaction).Rollback"]
	constructor := functions["newRetainedRuntimeGeneration"]
	var issues []string
	if rollback == nil {
		issues = append(issues, "RuntimeReloadTransaction.Rollback is missing")
	} else {
		calls := p015B3NamedCallsInNode(rollback.Body, "reloadProviderAndConfig")
		if len(calls) != 1 {
			issues = append(issues, fmt.Sprintf(
				"rollback reload calls = %d, want exact 1", len(calls),
			))
		} else {
			wantArguments := []string{
				"ctx",
				"retained.providers.constructorProvider()",
				"retained.cfg",
				"retained.executionPolicy",
				"retained.diagnosticPolicy",
				"false",
				"true",
				"&transaction.current",
				"&failed",
				"retained.providers",
			}
			issues = append(
				issues,
				p015B3ExactCallArgumentIssues(
					file,
					"rollback exact retained bundle",
					calls[0],
					wantArguments,
				)...,
			)
		}
	}

	wantFields := map[string]string{
		"mu":               "sync.Mutex",
		"owner":            "*AgentLoop",
		"transaction":      "*RuntimeReloadTransaction",
		"generationID":     "uint64",
		"cfg":              "*config.Config",
		"executionPolicy":  "isolation.ExecutionPolicy",
		"diagnosticPolicy": "logger.DiagnosticPolicy",
		"providers":        "*agentRegistryProviderGeneration",
		"successorID":      "uint64",
		"state":            "retainedRuntimeGenerationState",
	}
	gotFields, fieldIssues := p015B3NamedStructFields(file, "RetainedRuntimeGeneration")
	issues = append(issues, fieldIssues...)
	issues = append(issues, p015B3ExactStringMapIssues(
		"retained runtime field", gotFields, wantFields,
	)...)

	if constructor == nil {
		issues = append(issues, "newRetainedRuntimeGeneration is missing")
	} else {
		literals := p015B3NamedCompositeLiterals(
			file,
			constructor.Body,
			"RetainedRuntimeGeneration",
		)
		if len(literals) != 1 {
			issues = append(issues, fmt.Sprintf(
				"retained runtime constructor literals = %d, want exact 1", len(literals),
			))
		} else {
			gotValues, valueIssues := p015B3CompositeLiteralValues(file, literals[0])
			issues = append(issues, valueIssues...)
			wantValues := map[string]string{
				"owner":            "owner",
				"generationID":     "generation.id",
				"cfg":              "generation.cfg",
				"executionPolicy":  "generation.executionPolicy",
				"diagnosticPolicy": "generation.diagnosticPolicy",
				"providers":        "providerGeneration",
				"successorID":      "successorID",
				"state":            "retainedRuntimeGenerationAvailable",
			}
			issues = append(issues, p015B3ExactStringMapIssues(
				"retained runtime constructor field", gotValues, wantValues,
			)...)
		}
	}
	return issues
}

func p015B3LateWorkProvenanceIssues(files map[string]*p015B3ParsedFile) []string {
	mailboxFile := files["pkg/agent/subagent_result_mailbox.go"]
	turnFile := files["pkg/agent/turn_state.go"]
	if mailboxFile == nil || turnFile == nil {
		return []string{"late-work provenance sources are missing"}
	}
	var issues []string
	routeFields, routeFieldIssues := p015B3NamedStructFields(
		mailboxFile,
		"trackedSubagentResultRoute",
	)
	issues = append(issues, routeFieldIssues...)
	if routeFields["diagnosticOrigin"] != "runtimeDiagnosticOrigin" {
		issues = append(issues, fmt.Sprintf(
			"tracked route diagnosticOrigin = %q, want runtimeDiagnosticOrigin",
			routeFields["diagnosticOrigin"],
		))
	}
	for name, fieldType := range routeFields {
		lower := strings.ToLower(name + " " + fieldType)
		if strings.Contains(lower, "context.context") ||
			strings.Contains(lower, "parentcontext") ||
			strings.Contains(lower, "runtimeleas") {
			issues = append(issues, fmt.Sprintf(
				"tracked route retains raw authority field %s %s", name, fieldType,
			))
		}
	}

	mailboxFunctions := p015B3Functions(mailboxFile)
	snapshot := mailboxFunctions["snapshotTrackedSubagentResultRoute"]
	if snapshot == nil {
		issues = append(issues, "tracked route snapshot is missing")
	} else {
		origins := p015B3NamedCompositeLiterals(
			mailboxFile,
			snapshot.Body,
			"runtimeDiagnosticOrigin",
		)
		if len(origins) != 1 {
			issues = append(issues, fmt.Sprintf(
				"tracked route origin literals = %d, want 1", len(origins),
			))
		} else {
			values, literalIssues := p015B3CompositeLiteralValues(mailboxFile, origins[0])
			issues = append(issues, literalIssues...)
			issues = append(issues, p015B3ExactStringMapIssues(
				"tracked route origin field",
				values,
				map[string]string{"policy": "diagnosticPolicy", "valid": "true"},
			)...)
		}
	}
	clone := mailboxFunctions["cloneTrackedSubagentResultRoute"]
	if clone == nil {
		issues = append(issues, "tracked route clone is missing")
	} else if body := p015B3Render(mailboxFile.fileSet, clone.Body); strings.Contains(
		body,
		"diagnosticOrigin",
	) {
		issues = append(issues, "tracked route clone mutates diagnostic origin")
	}
	if got := len(p015B3NamedCallsInNode(
		mailboxFile.file,
		"cloneTrackedSubagentResultRoute",
	)); got != 5 {
		issues = append(issues, fmt.Sprintf(
			"tracked route clone calls = %d, want exact 5", got,
		))
	}
	for _, requirement := range []struct {
		function string
		callee   string
		args     []string
	}{
		{
			function: "(*AgentLoop).runTrackedSubagentResultPump",
			callee:   "acquireRuntimeUseFromOrigin",
			args:     []string{"al.trackedSubagentWorkerContext()", "route.diagnosticOrigin"},
		},
		{
			function: "(*AgentLoop).runTrackedSubagentSteeringRescue",
			callee:   "acquireRuntimeUseFromOrigin",
			args:     []string{"al.trackedSubagentWorkerContext()", "route.diagnosticOrigin"},
		},
	} {
		function := mailboxFunctions[requirement.function]
		if function == nil {
			issues = append(issues, requirement.function+" is missing")
			continue
		}
		calls := p015B3NamedCallsInNode(function.Body, requirement.callee)
		if len(calls) != 1 {
			issues = append(issues, fmt.Sprintf(
				"%s %s calls = %d, want 1", requirement.function, requirement.callee, len(calls),
			))
			continue
		}
		issues = append(issues, p015B3ExactCallArgumentIssues(
			mailboxFile,
			requirement.function+" origin admission",
			calls[0],
			requirement.args,
		)...)
		if len(p015B3NamedCallsInNode(function.Body, "WithoutCancel")) != 0 {
			issues = append(issues, requirement.function+" retains a raw context")
		}
	}

	requestFields, requestFieldIssues := p015B3NamedStructFields(
		turnFile,
		"steeringRescueRequest",
	)
	issues = append(issues, requestFieldIssues...)
	if requestFields["diagnosticOrigin"] != "runtimeDiagnosticOrigin" {
		issues = append(issues, fmt.Sprintf(
			"steering rescue diagnosticOrigin = %q, want runtimeDiagnosticOrigin",
			requestFields["diagnosticOrigin"],
		))
	}
	for name, fieldType := range requestFields {
		lower := strings.ToLower(name + " " + fieldType)
		if strings.Contains(lower, "context.context") || strings.Contains(lower, "parentcontext") {
			issues = append(issues, fmt.Sprintf(
				"steering rescue retains raw authority field %s %s", name, fieldType,
			))
		}
	}
	turnFunctions := p015B3Functions(turnFile)
	rescue := turnFunctions["(*AgentLoop).rescueOrClearOrphanedSteering"]
	if rescue == nil {
		issues = append(issues, "steering rescue snapshot is missing")
	} else {
		originCalls := p015B3NamedCallsInNode(rescue.Body, "runtimeDiagnosticOriginFromLease")
		if len(originCalls) != 1 {
			issues = append(issues, fmt.Sprintf(
				"steering rescue origin snapshots = %d, want 1", len(originCalls),
			))
		}
		requests := p015B3NamedCompositeLiterals(turnFile, rescue.Body, "steeringRescueRequest")
		if len(requests) != 1 {
			issues = append(issues, fmt.Sprintf(
				"steering rescue request literals = %d, want 1", len(requests),
			))
		} else {
			values, literalIssues := p015B3CompositeLiteralValues(turnFile, requests[0])
			issues = append(issues, literalIssues...)
			if values["diagnosticOrigin"] != "origin" {
				issues = append(issues, fmt.Sprintf(
					"steering rescue request origin = %q, want origin",
					values["diagnosticOrigin"],
				))
			}
		}
	}
	runRescue := turnFunctions["(*AgentLoop).runSteeringRescue"]
	if runRescue == nil {
		issues = append(issues, "steering rescue runner is missing")
	} else {
		for _, requirement := range []struct {
			callee string
			args   []string
		}{
			{
				callee: "continueWithInboundContext",
				args: []string{
					"resumeCtx", "sessionKey", "request.channel", "request.chatID",
					"request.inboundContext", "&request.diagnosticOrigin",
				},
			},
			{
				callee: "drainQueuedSteeringContinuations",
				args:   []string{"resumeCtx", "target", "&request.diagnosticOrigin"},
			},
		} {
			calls := p015B3NamedCallsInNode(runRescue.Body, requirement.callee)
			if len(calls) != 1 {
				issues = append(issues, fmt.Sprintf(
					"steering rescue %s calls = %d, want 1", requirement.callee, len(calls),
				))
				continue
			}
			issues = append(issues, p015B3ExactCallArgumentIssues(
				turnFile,
				"steering rescue "+requirement.callee,
				calls[0],
				requirement.args,
			)...)
		}
	}
	return issues
}

func p015B3RuntimeLeaseCoreIssues(files map[string]*p015B3ParsedFile) []string {
	runtimeFile := files["pkg/agent/runtime_gate.go"]
	repairFile := files["pkg/agent/local_repair.go"]
	if runtimeFile == nil || repairFile == nil {
		return []string{"runtime lease core sources are missing"}
	}
	functions := p015B3Functions(runtimeFile)
	var issues []string

	release := functions["(*AgentLoop).newRuntimeLeaseRelease"]
	if release == nil {
		issues = append(issues, "runtime lease release is missing")
	} else {
		body := p015B3Render(runtimeFile.fileSet, release.Body)
		issues = append(issues, p015B3OrderedFragmentsIssues(
			"runtime lease release",
			body,
			[]string{
				"al.runtimeGateMu.Lock()",
				"revokeDiagnostic()",
				"boundary.active.Store(false)",
				"al.runtimeGateActive--",
				"al.signalRuntimeGateChangedLocked()",
				"al.runtimeGateMu.Unlock()",
			},
		)...)
		for fragment, want := range map[string]int{
			"al.runtimeGateMu.Lock()":             1,
			"revokeDiagnostic()":                  1,
			"boundary.active.Store(false)":        1,
			"al.runtimeGateActive--":              1,
			"al.signalRuntimeGateChangedLocked()": 1,
			"al.runtimeGateMu.Unlock()":           1,
		} {
			if got := strings.Count(body, fragment); got != want {
				issues = append(issues, fmt.Sprintf(
					"runtime lease release %q count = %d, want %d",
					fragment,
					got,
					want,
				))
			}
		}
	}

	counted := functions["(*AgentLoop).acquireCountedRuntimeGeneration"]
	if counted == nil {
		issues = append(issues, "counted runtime admission is missing")
	} else {
		body := p015B3Render(runtimeFile.fileSet, counted.Body)
		issues = append(issues, p015B3OrderedFragmentsIssues(
			"counted runtime admission",
			body,
			[]string{
				"al.rejectLiveForeignRuntime(ctx)",
				"al.incrementRuntimeAdmission(ctx)",
				"al.snapshotRuntimeGeneration()",
				"newRuntimeLeaseBoundary(nil)",
				"binder(ctx, generation)",
				"context.WithValue(",
				"al.newRuntimeLeaseRelease(boundary, revokeDiagnostic, true)",
			},
		)...)
		literals := p015B3NamedCompositeLiterals(
			runtimeFile,
			counted.Body,
			"runtimeLeaseContextValue",
		)
		if len(literals) != 1 {
			issues = append(issues, fmt.Sprintf(
				"counted runtime lease literals = %d, want 1", len(literals),
			))
		} else {
			values, literalIssues := p015B3CompositeLiteralValues(runtimeFile, literals[0])
			issues = append(issues, literalIssues...)
			issues = append(issues, p015B3ExactStringMapIssues(
				"counted runtime lease field",
				values,
				map[string]string{
					"owner": "al", "generation": "generation", "boundary": "boundary", "kind": "kind",
				},
			)...)
		}
	}

	retain := functions["(*AgentLoop).retainRuntimeUse"]
	if retain == nil {
		issues = append(issues, "retained runtime admission is missing")
	} else {
		body := p015B3Render(runtimeFile.fileSet, retain.Body)
		issues = append(issues, p015B3OrderedFragmentsIssues(
			"retained runtime admission",
			body,
			[]string{
				"al.runtimeGateMu.Lock()",
				"runtimeLeaseContextFrom(ctx)",
				"newRuntimeLeaseBoundary(nil)",
				"logger.RebindDiagnosticPolicy(",
				"al.runtimeGateActive++",
				"al.signalRuntimeGateChangedLocked()",
				"al.runtimeGateMu.Unlock()",
				"context.WithValue(",
				"al.newRuntimeLeaseRelease(boundary, revokeDiagnostic, true)",
			},
		)...)
		calls := p015B3NamedCallsInNode(retain.Body, "RebindDiagnosticPolicy")
		if len(calls) != 1 {
			issues = append(issues, fmt.Sprintf(
				"retained runtime Rebind calls = %d, want 1", len(calls),
			))
		} else {
			issues = append(issues, p015B3ExactCallArgumentIssues(
				runtimeFile,
				"retained runtime rebind",
				calls[0],
				[]string{"ctx", "ctx", "value.generation.diagnosticPolicy"},
			)...)
		}
		for _, forbidden := range []string{"BindRootDiagnosticPolicy", "NarrowDiagnosticPolicy"} {
			if got := len(p015B3NamedCallsInNode(retain.Body, forbidden)); got != 0 {
				issues = append(issues, fmt.Sprintf(
					"retained runtime uses forbidden %s %d time(s)", forbidden, got,
				))
			}
		}
	}

	binder := functions["bindOriginCurrentRuntimeDiagnostic"]
	if binder == nil {
		issues = append(issues, "detached exact-generation diagnostic binder is missing")
	} else {
		calls := p015B3NamedCallsInNode(binder.Body, "BindRootDiagnosticPolicy")
		if len(calls) != 1 {
			issues = append(issues, fmt.Sprintf(
				"detached exact-generation root binds = %d, want 1", len(calls),
			))
		} else {
			issues = append(issues, p015B3ExactCallArgumentIssues(
				runtimeFile,
				"detached exact-generation safe-only bind",
				calls[0],
				[]string{"ctx", "logger.DiagnosticPolicy{}"},
			)...)
		}
		if got := len(p015B3NamedCallsInNode(binder.Body, "RebindDiagnosticPolicy")); got != 0 {
			issues = append(issues,
				"detached exact-generation binder trusts a public diagnostic rebind",
			)
		}
	}

	paused := functions["(*AgentLoop).withPausedRuntimeGeneration"]
	if paused == nil {
		issues = append(issues, "paused-current runtime callback is missing")
	} else {
		body := p015B3Render(runtimeFile.fileSet, paused.Body)
		issues = append(issues, p015B3OrderedFragmentsIssues(
			"paused-current runtime callback",
			body,
			[]string{
				"al.runtimeGateMu.Lock()",
				"runtimeLeaseContextFrom(ctx)",
				"newRuntimeLeaseBoundary(parent.boundary)",
				"al.runtimeGateMu.Unlock()",
				"al.snapshotRuntimeGeneration()",
				"logger.NarrowDiagnosticPolicy(",
				"context.WithValue(",
				"al.newRuntimeLeaseRelease(boundary, revokeDiagnostic, false)",
				"defer release()",
				"if !boundary.live()",
				"run(childCtx)",
			},
		)...)
		if strings.Contains(body, "runtimeGateActive++") ||
			strings.Contains(body, "runtimeGateActive--") {
			issues = append(issues, "paused-current callback changes runtime active count")
		}
	}

	pauseOwner := functions["(*AgentLoop).pauseRuntimeUsesWithContext"]
	if pauseOwner == nil {
		issues = append(issues, "generationless pause owner is missing")
	} else {
		body := p015B3Render(runtimeFile.fileSet, pauseOwner.Body)
		issues = append(issues, p015B3OrderedFragmentsIssues(
			"generationless pause owner",
			body,
			[]string{
				"newRuntimeLeaseBoundary(nil)",
				"logger.BindRootDiagnosticPolicy(",
				"context.WithValue(",
				"al.newRuntimeLeaseRelease(boundary, revokeDiagnostic, false)",
				"releaseOwner()",
				"resumeRuntime()",
			},
		)...)
		literals := p015B3NamedCompositeLiterals(
			runtimeFile,
			pauseOwner.Body,
			"runtimeLeaseContextValue",
		)
		if len(literals) != 1 {
			issues = append(issues, fmt.Sprintf(
				"pause-owner runtime lease literals = %d, want 1", len(literals),
			))
		} else {
			values, literalIssues := p015B3CompositeLiteralValues(runtimeFile, literals[0])
			issues = append(issues, literalIssues...)
			issues = append(issues, p015B3ExactStringMapIssues(
				"pause-owner runtime lease field",
				values,
				map[string]string{
					"owner": "al", "boundary": "boundary", "kind": "runtimeLeaseKindPauseOwner",
				},
			)...)
		}
	}

	repairRun := p015B3Functions(repairFile)["(*LocalRepairRunner).Run"]
	if repairRun == nil {
		issues = append(issues, "local repair runtime guard is missing")
	} else {
		body := p015B3Render(repairFile.fileSet, repairRun.Body)
		issues = append(issues, p015B3OrderedFragmentsIssues(
			"local repair runtime guard",
			body,
			[]string{
				"if runner.strictRuntime",
				"runner.runtimeLoop.runtimeGenerationFromLease(ctx)",
				"validateLocalRepairPin(request.Pin)",
				"runner.workspaces.WithPinnedOperation(",
			},
		)...)
		if got := len(p015B3NamedCallsInNode(repairRun.Body, "WithPinnedOperation")); got != 1 {
			issues = append(issues, fmt.Sprintf(
				"local repair WithPinnedOperation calls = %d, want 1", got,
			))
		}
	}
	repairPinned := p015B3Functions(repairFile)["(*LocalRepairRunner).runPinned"]
	if repairPinned == nil {
		issues = append(issues, "local repair pinned loop is missing")
	} else {
		configs := p015B3NamedCompositeLiterals(
			repairFile,
			repairPinned.Body,
			"tools.ToolLoopConfig",
		)
		if len(configs) != 1 {
			issues = append(issues, fmt.Sprintf(
				"local repair tool-loop configs = %d, want 1", len(configs),
			))
		} else {
			values, literalIssues := p015B3CompositeLiteralValues(repairFile, configs[0])
			issues = append(issues, literalIssues...)
			if values["SuppressToolArguments"] != "true" {
				issues = append(issues, fmt.Sprintf(
					"local repair SuppressToolArguments = %q, want true",
					values["SuppressToolArguments"],
				))
			}
		}
	}
	return issues
}

func p015B3OrderedFragmentsIssues(label, body string, fragments []string) []string {
	previous := -1
	for _, fragment := range fragments {
		start := previous + 1
		relative := strings.Index(body[start:], fragment)
		if relative < 0 {
			return []string{fmt.Sprintf("%s is missing %q", label, fragment)}
		}
		index := start + relative
		previous = index
	}
	return nil
}

func p015B3ProviderGenerationIssues(files map[string]*p015B3ParsedFile) []string {
	agentFile := files["pkg/agent/agent.go"]
	transactionFile := files["pkg/agent/runtime_reload_transaction.go"]
	providerFile := files["pkg/agent/runtime_provider_generation.go"]
	if agentFile == nil || transactionFile == nil || providerFile == nil {
		return []string{"complete retained provider generation sources are missing"}
	}
	var issues []string
	generationFields, generationFieldIssues := p015B3NamedStructFields(
		providerFile,
		"agentRegistryProviderGeneration",
	)
	issues = append(issues, generationFieldIssues...)
	issues = append(issues, p015B3ExactStringMapIssues(
		"provider generation field",
		generationFields,
		map[string]string{
			"bootstrap":        "providers.LLMProvider",
			"defaultProvider":  "providers.LLMProvider",
			"agentBindings":    "map[string]map[string]providers.LLMProvider",
			"agentDirect":      "map[string]agentDirectProviderBindings",
			"orderedProviders": "[]providers.LLMProvider",
		},
	)...)
	directFields, directFieldIssues := p015B3NamedStructFields(
		providerFile,
		"agentDirectProviderBindings",
	)
	issues = append(issues, directFieldIssues...)
	issues = append(issues, p015B3ExactStringMapIssues(
		"provider direct-binding field",
		directFields,
		map[string]string{
			"primary": "providers.LLMProvider",
			"light":   "providers.LLMProvider",
		},
	)...)
	snapshot := p015B3Functions(providerFile)["snapshotAgentRegistryProviderGeneration"]
	if snapshot == nil {
		issues = append(issues, "complete provider generation snapshot is missing")
	} else {
		body := p015B3Render(providerFile.fileSet, snapshot.Body)
		issues = append(issues, p015B3OrderedFragmentsIssues(
			"complete provider generation snapshot",
			body,
			[]string{
				"registry.mu.RLock()",
				"for agentID, instance := range registry.agents",
				"registry.mu.RUnlock()",
				"agentCandidateProvidersMu.RLock()",
				"for _, agentID := range agentIDs",
				"for key := range instance.CandidateProviders",
				"sort.Strings(keys)",
				"generation.agentBindings[agentID] = bindings",
				"generation.agentDirect[agentID] = agentDirectProviderBindings",
				"primary: instance.Provider",
				"light:   instance.LightProvider",
				"appendProvider(instance.Provider)",
				"appendProvider(instance.LightProvider)",
				"agentCandidateProvidersMu.RUnlock()",
				"appendProvider(bootstrap)",
			},
		)...)
		if got := len(p015B3NamedCallsInNode(snapshot.Body, "appendProvider")); got != 4 {
			issues = append(issues, fmt.Sprintf(
				"complete provider append calls = %d, want 4", got,
			))
		}
		if got := len(p015B3NamedCallsInNode(snapshot.Body, "sameLLMProvider")); got != 1 {
			issues = append(issues, fmt.Sprintf(
				"complete provider deduplication calls = %d, want 1", got,
			))
		}
	}
	reload := p015B3Functions(agentFile)["(*AgentLoop).reloadProviderAndConfig"]
	if reload == nil {
		issues = append(issues, "provider generation reload owner is missing")
	} else {
		for callee, want := range map[string]int{
			"snapshotAgentRegistryProviderGeneration": 1,
			"newRetainedRuntimeGeneration":            1,
			"closeAll":                                1,
			"closeAllExcept":                          1,
		} {
			if got := len(p015B3NamedCallsInNode(reload.Body, callee)); got != want {
				issues = append(issues, fmt.Sprintf(
					"reload provider-generation %s calls = %d, want %d",
					callee,
					got,
					want,
				))
			}
		}
	}

	commit := p015B3Functions(transactionFile)["(*RuntimeReloadTransaction).CommitRetained"]
	if commit == nil {
		issues = append(issues, "RuntimeReloadTransaction.CommitRetained is missing")
	} else {
		body := p015B3Render(transactionFile.fileSet, commit.Body)
		unlockTransaction := strings.Index(body, "transaction.mu.Unlock()")
		unlockReload := strings.Index(body, "owner.reloadMu.Unlock()")
		closeProviders := strings.Index(body, "providerGeneration.closeAll()")
		if unlockTransaction < 0 || unlockReload < 0 || closeProviders < 0 ||
			!(unlockTransaction < unlockReload && unlockReload < closeProviders) {
			issues = append(issues,
				"retained provider close is not ordered after transaction and reload unlock",
			)
		}
	}
	closeTransaction := p015B3Functions(transactionFile)["(*RuntimeReloadTransaction).Close"]
	if closeTransaction == nil {
		issues = append(issues, "RuntimeReloadTransaction.Close is missing")
	} else {
		body := p015B3Render(transactionFile.fileSet, closeTransaction.Body)
		unlockReload := strings.LastIndex(body, "owner.reloadMu.Unlock()")
		closeProviders := strings.LastIndex(body, "providerGeneration.closeAll()")
		if unlockReload < 0 || closeProviders < 0 || unlockReload >= closeProviders {
			issues = append(issues,
				"transaction Close does not release reload lock before pending provider close",
			)
		}
	}

	if p015B3Functions(providerFile)["(*agentRegistryProviderGeneration).bindingsForAgent"] == nil ||
		p015B3Functions(providerFile)["(*agentRegistryProviderGeneration).closeAll"] == nil {
		issues = append(issues, "complete provider generation snapshot/binding/close API drifted")
	}
	return issues
}

func p015B3StrictConstructorIssues(files map[string]*p015B3ParsedFile) []string {
	var issues []string
	constructorCounts := make(map[string]int)
	requireCalls := []struct {
		path      string
		function  string
		callee    string
		arguments [][]string
	}{
		{
			path: "pkg/agent/registry.go", function: "NewAgentRegistry",
			callee: "NewAgentRegistryWithRuntimePolicies",
			arguments: [][]string{{
				"cfg",
				"provider",
				"isolation.NewExecutionPolicy(isolationCfg)",
				"logger.DiagnosticPolicy{}",
			}},
		},
		{
			path: "pkg/agent/registry.go", function: "NewAgentRegistryWithExecutionPolicy",
			callee:    "NewAgentRegistryWithRuntimePolicies",
			arguments: [][]string{{"cfg", "provider", "policy", "logger.DiagnosticPolicy{}"}},
		},
		{
			path: "pkg/agent/instance.go", function: "NewAgentInstance",
			callee: "NewAgentInstanceWithRuntimePolicies",
			arguments: [][]string{{
				"agentCfg",
				"defaults",
				"cfg",
				"provider",
				"isolation.NewExecutionPolicy(isolationCfg)",
				"logger.DiagnosticPolicy{}",
			}},
		},
		{
			path: "pkg/agent/instance.go", function: "NewAgentInstanceWithExecutionPolicy",
			callee: "NewAgentInstanceWithRuntimePolicies",
			arguments: [][]string{{
				"agentCfg",
				"defaults",
				"cfg",
				"provider",
				"policy",
				"logger.DiagnosticPolicy{}",
			}},
		},
		{
			path: "pkg/agent/registry.go", function: "NewAgentRegistryWithRuntimePolicies",
			callee: "newAgentRegistryWithRuntimePolicies",
			arguments: [][]string{{
				"cfg", "provider", "executionPolicy", "diagnosticPolicy", "nil",
			}},
		},
		{
			path: "pkg/agent/agent_init.go", function: "newAgentLoop",
			callee:    "NewAgentRegistryWithRuntimePolicies",
			arguments: [][]string{{"cfg", "provider", "al.executionPolicy", "al.diagnosticPolicy"}},
		},
		{
			path: "pkg/agent/agent.go", function: "(*AgentLoop).reloadProviderAndConfig",
			callee: "newAgentRegistryWithRuntimePolicies",
			arguments: [][]string{{
				"cfg", "provider", "executionPolicy", "diagnosticPolicy", "providerGeneration",
			}},
		},
		{
			path: "pkg/agent/agent.go", function: "(*AgentLoop).reloadProviderAndConfig",
			callee:    "NewAgentRegistryWithRuntimePolicies",
			arguments: [][]string{{"cfg", "provider", "executionPolicy", "diagnosticPolicy"}},
		},
		{
			path: "pkg/agent/registry.go", function: "newAgentRegistryWithRuntimePolicies",
			callee: "newAgentInstanceWithRuntimePolicies",
			arguments: [][]string{
				{
					"implicitAgent",
					"&cfg.Agents.Defaults",
					"cfg",
					"provider",
					"executionPolicy",
					"diagnosticPolicy",
					"providerGeneration.bindingsForAgent(\"main\")",
				},
				{
					"ac",
					"&cfg.Agents.Defaults",
					"cfg",
					"provider",
					"executionPolicy",
					"diagnosticPolicy",
					"providerGeneration.bindingsForAgent(id)",
				},
			},
		},
		{
			path: "pkg/agent/instance.go", function: "NewAgentInstanceWithRuntimePolicies",
			callee: "newAgentInstanceWithRuntimePolicies",
			arguments: [][]string{{
				"agentCfg", "defaults", "cfg", "provider", "executionPolicy",
				"diagnosticPolicy", "nil",
			}},
		},
		{
			path: "pkg/agent/instance.go", function: "newAgentInstanceWithRuntimePolicies",
			callee:    "NewToolRegistryWithDiagnosticPolicy",
			arguments: [][]string{{"diagnosticPolicy"}},
		},
		{
			path: "pkg/agent/local_repair.go", function: "newLocalRepairToolRegistryWithDiagnosticPolicy",
			callee:    "NewToolRegistryWithDiagnosticPolicy",
			arguments: [][]string{{"diagnosticPolicy"}},
		},
	}
	for _, requirement := range requireCalls {
		file := files[requirement.path]
		if file == nil {
			issues = append(issues, requirement.path+" is missing")
			continue
		}
		function := p015B3Functions(file)[requirement.function]
		if function == nil {
			issues = append(issues, fmt.Sprintf(
				"%s %s is missing", requirement.path, requirement.function,
			))
			continue
		}
		calls := p015B3NamedCallsInNode(function.Body, requirement.callee)
		if len(calls) != len(requirement.arguments) {
			issues = append(issues, fmt.Sprintf(
				"%s %s %s calls = %d, want %d",
				requirement.path,
				requirement.function,
				requirement.callee,
				len(calls),
				len(requirement.arguments),
			))
			continue
		}
		for index, call := range calls {
			issues = append(issues, p015B3ExactCallArgumentIssues(
				file,
				requirement.path+" "+requirement.function+" "+requirement.callee,
				call,
				requirement.arguments[index],
			)...)
		}
	}

	for _, path := range p015B3SortedPaths(files) {
		if !strings.HasPrefix(path, "pkg/agent/") {
			continue
		}
		for _, ownedCall := range p015B3Calls(files[path]) {
			callee := p015B3Callee(ownedCall.call)
			switch callee {
			case "NewAgentRegistry",
				"NewAgentRegistryWithExecutionPolicy",
				"NewAgentRegistryWithRuntimePolicies",
				"newAgentRegistryWithRuntimePolicies",
				"NewAgentInstance",
				"NewAgentInstanceWithExecutionPolicy",
				"NewAgentInstanceWithRuntimePolicies",
				"newAgentInstanceWithRuntimePolicies",
				"NewToolRegistry",
				"NewToolRegistryWithDiagnosticPolicy":
				constructorCounts[callee]++
			}
			switch callee {
			case "NewAgentRegistry", "NewAgentInstance", "NewToolRegistry":
				issues = append(issues, fmt.Sprintf(
					"%s %s uses compatibility constructor %s",
					path,
					ownedCall.function,
					p015B3Callee(ownedCall.call),
				))
			}
		}
	}
	wantConstructorCounts := map[string]int{
		"NewAgentRegistryWithRuntimePolicies": 4,
		"newAgentRegistryWithRuntimePolicies": 2,
		"NewAgentInstanceWithRuntimePolicies": 2,
		"newAgentInstanceWithRuntimePolicies": 3,
		"NewToolRegistryWithDiagnosticPolicy": 2,
	}
	issues = append(
		issues,
		p015B3ExactCounts(
			"internal policy constructor",
			constructorCounts,
			wantConstructorCounts,
		)...,
	)
	return issues
}

func p015B3PromptAuthorityIssues(files map[string]*p015B3ParsedFile) []string {
	promptFile := files["pkg/agent/prompt.go"]
	contextFile := files["pkg/agent/context.go"]
	if promptFile == nil || contextFile == nil {
		return []string{"prompt authority source files are missing"}
	}
	wantFields := map[string]string{
		"History":                     "[]providers.Message",
		"Summary":                     "string",
		"CurrentMessage":              "string",
		"Media":                       "[]string",
		"Channel":                     "string",
		"ChatID":                      "string",
		"SenderID":                    "string",
		"SenderDisplayName":           "string",
		"ActiveSkills":                "[]string",
		"Overlays":                    "[]PromptPart",
		"SuppressDefaultSystemPrompt": "bool",
		"SuppressSkillContext":        "bool",
		"SuppressToolUseRule":         "bool",
		"AllowedSkills":               "[]string",
		"AllowedTools":                "[]string",
		"ToolUseFallback":             "bool",
	}
	fields, issues := p015B3NamedStructFields(promptFile, "PromptBuildRequest")
	issues = append(issues, p015B3ExactStringMapIssues(
		"PromptBuildRequest field", fields, wantFields,
	)...)
	for name, fieldType := range fields {
		lower := strings.ToLower(name + " " + fieldType)
		if strings.Contains(lower, "diagnostic") ||
			strings.Contains(lower, "authority") ||
			strings.Contains(lower, "logger.") ||
			strings.Contains(lower, "context.context") {
			issues = append(issues, fmt.Sprintf(
				"PromptBuildRequest field %s retains authority type %s", name, fieldType,
			))
		}
	}

	functions := p015B3Functions(contextFile)
	public := functions["(*ContextBuilder).BuildMessagesFromPrompt"]
	private := functions["(*ContextBuilder).buildMessagesFromPromptWithDiagnosticPolicy"]
	if public == nil {
		issues = append(issues, "public BuildMessagesFromPrompt is missing")
	} else {
		calls := p015B3NamedCallsInNode(public.Body, "buildMessagesFromPromptWithDiagnosticPolicy")
		if len(calls) != 1 {
			issues = append(issues, fmt.Sprintf(
				"public BuildMessagesFromPrompt private calls = %d, want exact 1", len(calls),
			))
		} else {
			issues = append(issues, p015B3ExactCallArgumentIssues(
				contextFile,
				"public BuildMessagesFromPrompt safe-only projection",
				calls[0],
				[]string{"req", "logger.DiagnosticPolicy{}"},
			)...)
		}
	}
	if private == nil {
		issues = append(issues, "private buildMessagesFromPromptWithDiagnosticPolicy is missing")
	} else {
		if p015B3IsExported(private.Name.Name) {
			issues = append(issues, "diagnostic prompt method became exported")
		}
		gotParameters := p015B3FieldListSignatures(contextFile, private.Type.Params)
		wantParameters := []string{
			"req PromptBuildRequest",
			"diagnosticPolicy logger.DiagnosticPolicy",
		}
		if strings.Join(gotParameters, "\x00") != strings.Join(wantParameters, "\x00") {
			issues = append(issues, fmt.Sprintf(
				"private diagnostic prompt parameters = %q, want %q",
				gotParameters,
				wantParameters,
			))
		}
		gotResults := p015B3FieldListSignatures(contextFile, private.Type.Results)
		if len(gotResults) != 1 || gotResults[0] != "[]providers.Message" {
			issues = append(issues, fmt.Sprintf(
				"private diagnostic prompt results = %q, want []providers.Message",
				gotResults,
			))
		}
	}
	return issues
}

func p015B3NamedCallsInNode(node ast.Node, callee string) []*ast.CallExpr {
	var calls []*ast.CallExpr
	ast.Inspect(node, func(node ast.Node) bool {
		call, ok := node.(*ast.CallExpr)
		if ok && p015B3Callee(call) == callee {
			calls = append(calls, call)
		}
		return true
	})
	return calls
}

func p015B3ExactCallArgumentIssues(
	file *p015B3ParsedFile,
	label string,
	call *ast.CallExpr,
	want []string,
) []string {
	if len(call.Args) != len(want) {
		return []string{fmt.Sprintf(
			"%s argument count = %d, want %d", label, len(call.Args), len(want),
		)}
	}
	var issues []string
	for index, expression := range call.Args {
		got := p015B3Render(file.fileSet, expression)
		if got != want[index] {
			issues = append(issues, fmt.Sprintf(
				"%s argument %d = %q, want %q", label, index+1, got, want[index],
			))
		}
	}
	return issues
}

func p015B3NamedStructFields(
	file *p015B3ParsedFile,
	typeName string,
) (map[string]string, []string) {
	for _, declaration := range file.file.Decls {
		general, ok := declaration.(*ast.GenDecl)
		if !ok || general.Tok != token.TYPE {
			continue
		}
		for _, rawSpec := range general.Specs {
			spec, ok := rawSpec.(*ast.TypeSpec)
			if !ok || spec.Name.Name != typeName {
				continue
			}
			structure, ok := spec.Type.(*ast.StructType)
			if !ok {
				return nil, []string{typeName + " is not a struct"}
			}
			fields := make(map[string]string)
			var issues []string
			for _, field := range structure.Fields.List {
				if len(field.Names) == 0 {
					issues = append(issues, typeName+" contains an embedded field")
					continue
				}
				fieldType := p015B3Render(file.fileSet, field.Type)
				for _, name := range field.Names {
					if _, duplicate := fields[name.Name]; duplicate {
						issues = append(issues, typeName+" duplicates field "+name.Name)
					}
					fields[name.Name] = fieldType
				}
			}
			return fields, issues
		}
	}
	return nil, []string{typeName + " is missing"}
}

func p015B3NamedCompositeLiterals(
	file *p015B3ParsedFile,
	node ast.Node,
	typeName string,
) []*ast.CompositeLit {
	var literals []*ast.CompositeLit
	ast.Inspect(node, func(node ast.Node) bool {
		literal, ok := node.(*ast.CompositeLit)
		if ok && p015B3Render(file.fileSet, literal.Type) == typeName {
			literals = append(literals, literal)
		}
		return true
	})
	return literals
}

func p015B3CompositeLiteralValues(
	file *p015B3ParsedFile,
	literal *ast.CompositeLit,
) (map[string]string, []string) {
	values := make(map[string]string)
	var issues []string
	for _, element := range literal.Elts {
		keyValue, ok := element.(*ast.KeyValueExpr)
		if !ok {
			issues = append(issues, "retained runtime constructor contains an unkeyed field")
			continue
		}
		key, ok := keyValue.Key.(*ast.Ident)
		if !ok {
			issues = append(issues, "retained runtime constructor contains a non-identifier key")
			continue
		}
		if _, duplicate := values[key.Name]; duplicate {
			issues = append(issues, "retained runtime constructor duplicates field "+key.Name)
		}
		values[key.Name] = p015B3Render(file.fileSet, keyValue.Value)
	}
	return values, issues
}

func p015B3ExactStringMapIssues(label string, got, want map[string]string) []string {
	var issues []string
	for key, wantValue := range want {
		if gotValue, found := got[key]; !found || gotValue != wantValue {
			issues = append(issues, fmt.Sprintf(
				"%s %s = %q, want %q", label, key, gotValue, wantValue,
			))
		}
	}
	for key, gotValue := range got {
		if _, found := want[key]; !found {
			issues = append(issues, fmt.Sprintf(
				"unexpected %s %s = %q", label, key, gotValue,
			))
		}
	}
	return issues
}

func p015B3FieldListSignatures(
	file *p015B3ParsedFile,
	fields *ast.FieldList,
) []string {
	if fields == nil {
		return nil
	}
	var signatures []string
	for _, field := range fields.List {
		fieldType := p015B3Render(file.fileSet, field.Type)
		if len(field.Names) == 0 {
			signatures = append(signatures, fieldType)
			continue
		}
		for _, name := range field.Names {
			signatures = append(signatures, name.Name+" "+fieldType)
		}
	}
	return signatures
}
