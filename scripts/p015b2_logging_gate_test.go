package main

import (
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
)

const (
	p015LoggingLedgerPath   = "scripts/testdata/p015b2_logging_inventory.tsv"
	p015TombstoneGoldenPath = "scripts/testdata/p015b2_logging_tombstones.tsv"
	p015B2BSignaturePath    = "scripts/testdata/p015b2b_safe_signatures.tsv"
	p015ModulePath          = "github.com/sipeed/picoclaw"
)

var p015ExpectedFrozenTombstoneIDs = map[string]struct{}{
	"A004": {}, "A019": {}, "A103": {}, "A106": {},
	"F002": {}, "F003": {}, "F004": {}, "F005": {},
	"F006": {}, "F007": {}, "F008": {}, "F009": {},
}

type p015LoggerSurfaceClass string

const (
	p015LoggerSurfaceEmitter    p015LoggerSurfaceClass = "emitter"
	p015LoggerSurfaceNonEmitter p015LoggerSurfaceClass = "non-emitting"
	p015LoggerSurfaceForbidden  p015LoggerSurfaceClass = "forbidden"
)

var p015PicoLoggerExportSurface = func() map[string]p015LoggerSurfaceClass {
	surface := make(map[string]p015LoggerSurfaceClass)
	add := func(class p015LoggerSurfaceClass, keys ...string) {
		for _, key := range keys {
			surface[key] = class
		}
	}
	add(
		p015LoggerSurfaceEmitter,
		"func:Debug", "func:DebugC", "func:DebugCF", "func:DebugF", "func:Debugf",
		"func:DebugSafeCF", "func:DebugSensitiveCF",
		"func:Info", "func:InfoC", "func:InfoCF", "func:InfoF", "func:Infof",
		"func:InfoSafeCF",
		"func:Warn", "func:WarnC", "func:WarnCF", "func:WarnF", "func:Warnf",
		"func:WarnSafeCF",
		"func:Error", "func:ErrorC", "func:ErrorCF", "func:ErrorF", "func:Errorf",
		"func:ErrorSafeCF",
		"func:Fatal", "func:FatalC", "func:FatalCF", "func:FatalF", "func:Fatalf",
		"func:FatalSafeCF",
		"func:InitPanic", "func:RecoverPanicNoExit",
	)
	add(
		p015LoggerSurfaceNonEmitter,
		"type:ComponentID", "type:DiagnosticMessageID", "type:DiagnosticPolicy",
		"type:ErrorClass", "type:FieldKey", "type:LogLevel", "type:Observation",
		"type:ObservationDomain", "type:ObservationFieldPrefix", "type:PresenceClass",
		"type:SafeEnumValue", "type:SafeField", "type:SafeFields", "type:SensitivityClass",
		"func:BindRootDiagnosticPolicy", "func:ConfigureFromEnv",
		"func:DiagnosticPolicyFromContext", "func:DisableConsole", "func:DisableFileLogging",
		"func:EnableConsole", "func:EnableFileLogging", "func:GetLevel",
		"func:NarrowDiagnosticPolicy", "func:NewDiagnosticPolicy", "func:NewSafeFields",
		"func:ObservationFields", "func:ObserveBytes", "func:ObserveErrorType",
		"func:ObserveIdentity", "func:ObserveJSONValue", "func:ObservePanic",
		"func:ObservePath", "func:ObservePresence", "func:ObserveText", "func:ObserveURL",
		"func:ParseLevel", "func:RebindDiagnosticPolicy", "func:SafeBool",
		"func:SafeEnum", "func:SafeFloat64", "func:SafeInt", "func:SafeInt64",
		"func:SafeObservation", "func:SetConsoleLevel", "func:SetLevel",
		"func:SetLevelFromString", "method:DiagnosticPolicy.Meet",
	)
	add(
		p015LoggerSurfaceForbidden,
		"type:Logger", "func:NewLogger",
		"method:*Logger.Debug", "method:*Logger.Debugf", "method:*Logger.Error",
		"method:*Logger.Errorf", "method:*Logger.Fatalf", "method:*Logger.Info",
		"method:*Logger.Infof", "method:*Logger.Log", "method:*Logger.Sync",
		"method:*Logger.Warn", "method:*Logger.Warnf", "method:*Logger.Warningf",
		"method:*Logger.WithLevels",
	)
	return surface
}()

var p015CohortSizes = map[string]int{
	"A": 116,
	"B": 132,
	"H": 24,
	"G": 54,
	"C": 23,
	"R": 7,
	"X": 1,
	"F": 22,
}

// Active rows describe current source calls. Stable rows additionally retain
// tombstones for collapsed legacy emitters so a later migration cannot recycle
// or silently erase their identities.
var p015ActiveCohortSizes = map[string]int{
	"A": 112,
	"B": 132,
	"H": 24,
	"G": 54,
	"C": 23,
	"R": 7,
	"X": 1,
	"F": 14,
}

var p015ExpectedStageCounts = map[string]int{
	"A|b2a_safe|pico_safe":              112,
	"A|b2a_retired|retired":             4,
	"B|b2b_safe|pico_safe":              132,
	"H|b1_certified_safe|pico_safe":     24,
	"G|b2c_logger_deferred|pico_legacy": 54,
	"C|b2c_console_deferred|console":    23,
	"R|b4_excluded|pico_legacy":         7,
	"X|crash_artifact|panic_artifact":   1,
	"F|functional_allow|functional_fmt": 13,
	"F|functional_allow|functional_io":  1,
	"F|functional_retired|retired":      8,
}

var p015CoreAgentFiles = map[string]struct{}{
	"pkg/agent/agent.go":                    {},
	"pkg/agent/agent_message.go":            {},
	"pkg/agent/context.go":                  {},
	"pkg/agent/context_legacy.go":           {},
	"pkg/agent/context_seahorse_catalog.go": {},
	"pkg/agent/agent_utils.go":              {},
	"pkg/agent/pipeline_execute.go":         {},
	"pkg/agent/pipeline_llm.go":             {},
	"pkg/agent/subagent_result_mailbox.go":  {},
	"pkg/agent/subturn.go":                  {},
	"pkg/agent/turn_state.go":               {},
}

var p015B2AApplicationFiles = map[string]struct{}{
	"pkg/agent/agent_message.go":    {},
	"pkg/agent/agent_utils.go":      {},
	"pkg/agent/context.go":          {},
	"pkg/agent/context_legacy.go":   {},
	"pkg/agent/pipeline_execute.go": {},
	"pkg/agent/pipeline_llm.go":     {},
}

type p015B2BFileExpectation struct {
	count  int
	canary string
}

var p015B2BFiles = map[string]p015B2BFileExpectation{
	"pkg/agent/account_alias_resolution.go": {
		count: 2, canary: "pkg/agent/p015b2b_catalog_logging_test.go#TestP015B2BCatalogLoggingASTManifest",
	},
	"pkg/agent/agent_command.go": {
		count: 2, canary: "pkg/agent/p015b2b_catalog_logging_test.go#TestP015B2BCatalogLoggingASTManifest",
	},
	"pkg/agent/agent_init.go": {
		count: 9, canary: "pkg/agent/p015b2b_catalog_logging_test.go#TestP015B2BCatalogLoggingASTManifest",
	},
	"pkg/agent/agent_mcp.go": {
		count: 9, canary: "pkg/agent/p015b2b_catalog_logging_test.go#TestP015B2BCatalogLoggingASTManifest",
	},
	"pkg/agent/definition.go": {
		count: 1, canary: "pkg/agent/p015b2b_catalog_logging_test.go#TestP015B2BCatalogLoggingASTManifest",
	},
	"pkg/agent/instance.go": {
		count: 8, canary: "pkg/agent/p015b2b_catalog_logging_test.go#TestP015B2BCatalogLoggingASTManifest",
	},
	"pkg/agent/prompt.go": {
		count: 2, canary: "pkg/agent/p015b2b_catalog_logging_test.go#TestP015B2BCatalogLoggingASTManifest",
	},
	"pkg/agent/recursion_tool_factory_catalog.go": {
		count: 1, canary: "pkg/agent/p015b2b_catalog_logging_test.go#TestP015B2BCatalogLoggingASTManifest",
	},
	"pkg/agent/registry.go": {
		count: 3, canary: "pkg/agent/p015b2b_catalog_logging_test.go#TestP015B2BCatalogLoggingASTManifest",
	},
	"pkg/agent/thinking.go": {
		count: 1, canary: "pkg/agent/p015b2b_catalog_logging_test.go#TestP015B2BCatalogLoggingASTManifest",
	},
	"pkg/agent/tool_allowlist.go": {
		count: 2, canary: "pkg/agent/p015b2b_catalog_logging_test.go#TestP015B2BCatalogLoggingASTManifest",
	},
	"pkg/agent/agent_media.go": {
		count: 7, canary: "pkg/agent/p015b2b_transport_logging_test.go#TestP015B2BTransportLoggingASTManifest",
	},
	"pkg/agent/agent_outbound.go": {
		count: 10, canary: "pkg/agent/p015b2b_transport_logging_test.go#TestP015B2BTransportLoggingASTManifest",
	},
	"pkg/agent/agent_steering.go": {
		count: 3, canary: "pkg/agent/p015b2b_transport_logging_test.go#TestP015B2BTransportLoggingASTManifest",
	},
	"pkg/agent/agent_transcribe.go": {
		count: 3, canary: "pkg/agent/p015b2b_transport_logging_test.go#TestP015B2BTransportLoggingASTManifest",
	},
	"pkg/agent/llm_media.go": {
		count: 1, canary: "pkg/agent/p015b2b_transport_logging_test.go#TestP015B2BTransportLoggingASTManifest",
	},
	"pkg/agent/pipeline_setup.go": {
		count: 4, canary: "pkg/agent/p015b2b_transport_logging_test.go#TestP015B2BTransportLoggingASTManifest",
	},
	"pkg/agent/pipeline_streaming.go": {
		count: 18, canary: "pkg/agent/p015b2b_transport_logging_test.go#TestP015B2BTransportLoggingASTManifest",
	},
	"pkg/agent/steering.go": {
		count: 3, canary: "pkg/agent/p015b2b_transport_logging_test.go#TestP015B2BTransportLoggingASTManifest",
	},
	"pkg/agent/turn_coord.go": {
		count: 9, canary: "pkg/agent/p015b2b_transport_logging_test.go#TestP015B2BTransportLoggingASTManifest",
	},
	"pkg/agent/context_seahorse.go": {
		count: 2, canary: "pkg/agent/p015b2b_lifecycle_logging_test.go#TestP015B2BLifecycleLoggingASTManifest",
	},
	"pkg/agent/evolution_bridge.go": {
		count: 4, canary: "pkg/agent/p015b2b_lifecycle_logging_test.go#TestP015B2BLifecycleLoggingASTManifest",
	},
	"pkg/agent/git_workspace.go": {
		count: 3, canary: "pkg/agent/p015b2b_lifecycle_logging_test.go#TestP015B2BLifecycleLoggingASTManifest",
	},
	"pkg/agent/legacy_events.go": {
		count: 1, canary: "pkg/agent/p015b2b_lifecycle_logging_test.go#TestP015B2BLifecycleLoggingASTManifest",
	},
	"pkg/agent/workflow_automations.go": {
		count: 17, canary: "pkg/agent/p015b2b_workflow_logging_test.go#TestP015B2BWorkflowLoggingASTManifest",
	},
	"pkg/agent/workflow_runtime.go": {
		count: 1, canary: "pkg/agent/p015b2b_workflow_logging_test.go#TestP015B2BWorkflowLoggingASTManifest",
	},
	"pkg/agent/workflow_triggers.go": {
		count: 6, canary: "pkg/agent/p015b2b_workflow_logging_test.go#TestP015B2BWorkflowLoggingASTManifest",
	},
}

func TestP015B2LoggingInventoryClosed(t *testing.T) {
	repoRoot := p015FindRepoRoot(t)
	ledger := p015ReadLoggingLedger(t, filepath.Join(repoRoot, filepath.FromSlash(p015LoggingLedgerPath)))
	p015ValidateLedger(t, repoRoot, ledger)
	scanRoot := repoRoot
	if override := os.Getenv("P015B2_SCAN_REPO_ROOT"); override != "" {
		scanRoot = override
	}

	scan, err := p015ScanLogging(p015LoggingScanOptions{
		RepoRoot:   scanRoot,
		ModulePath: p015ModulePath,
		Roots:      []string{"pkg/agent", "pkg/gateway"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(scan.Escapes) != 0 {
		messages := make([]string, 0, len(scan.Escapes))
		for _, escape := range scan.Escapes {
			messages = append(messages, escape.String())
		}
		t.Fatalf("logging escape(s) bypass the closed inventory:\n%s", strings.Join(messages, "\n"))
	}
	if issues := p015ReviewedMethodValueIssues(scan.ReviewedMethodValues); len(issues) != 0 {
		t.Fatalf("reviewed method-value allowance use drifted:\n%s", strings.Join(issues, "\n"))
	}

	expected := make(map[string]p015LoggingSite)
	for _, row := range ledger {
		if p015RetiredDisposition(row.Disposition) {
			continue
		}
		key := row.sourceKey()
		if previous, duplicate := expected[key]; duplicate {
			t.Fatalf("ledger rows %s and %s own the same source tuple", previous.ID, row.ID)
		}
		expected[key] = row
	}

	var unexpected []string
	for _, site := range scan.Sites {
		key := site.sourceKey()
		if _, found := expected[key]; found {
			delete(expected, key)
			continue
		}
		unexpected = append(unexpected, p015DescribeSite(site))
	}
	if len(expected) == 0 && len(unexpected) == 0 {
		return
	}

	missing := make([]string, 0, len(expected))
	for _, row := range expected {
		missing = append(missing, row.ID+" "+p015DescribeSite(row))
	}
	sort.Strings(missing)
	sort.Strings(unexpected)
	t.Fatalf(
		"closed logging inventory drifted\nmissing ledger tuples:\n%s\nunexpected source tuples:\n%s",
		strings.Join(missing, "\n"),
		strings.Join(unexpected, "\n"),
	)
}

func TestP015B2LoggingInventoryDetectors(t *testing.T) {
	repoRoot := p015FindRepoRoot(t)
	if len(p015ReviewedNonLoggingCalls) != 0 {
		t.Fatalf("unexpected unresolved-receiver allowances: %#v", p015ReviewedNonLoggingCalls)
	}
	if len(p015ReviewedNonLoggingMethodValues) != 56 {
		t.Fatalf(
			"reviewed unresolved method-value allowances = %d, want exact 56: %#v",
			len(p015ReviewedNonLoggingMethodValues),
			p015ReviewedNonLoggingMethodValues,
		)
	}
	exactMethodValueUse := make(map[string]int, len(p015ReviewedNonLoggingMethodValues))
	for key := range p015ReviewedNonLoggingMethodValues {
		exactMethodValueUse[key] = 1
	}
	if issues := p015ReviewedMethodValueIssues(exactMethodValueUse); len(issues) != 0 {
		t.Fatalf("exact reviewed method-value set was rejected: %v", issues)
	}
	for key := range exactMethodValueUse {
		missing := make(map[string]int, len(exactMethodValueUse)-1)
		for candidate, count := range exactMethodValueUse {
			if candidate != key {
				missing[candidate] = count
			}
		}
		if issues := p015ReviewedMethodValueIssues(missing); len(issues) == 0 {
			t.Fatal("missing reviewed method-value use was accepted")
		}
		duplicate := make(map[string]int, len(exactMethodValueUse))
		for candidate, count := range exactMethodValueUse {
			duplicate[candidate] = count
		}
		duplicate[key] = 2
		if issues := p015ReviewedMethodValueIssues(duplicate); len(issues) == 0 {
			t.Fatal("duplicate reviewed method-value use was accepted")
		}
		break
	}
	unknown := make(map[string]int, len(exactMethodValueUse)+1)
	for key, count := range exactMethodValueUse {
		unknown[key] = count
	}
	unknown["unknown\x00method\x00value"] = 1
	if issues := p015ReviewedMethodValueIssues(unknown); len(issues) == 0 {
		t.Fatal("unknown reviewed method-value use was accepted")
	}
	if len(p015ReviewedNonLoggingImportedCalls) != 1 ||
		!p015ReviewedNonLoggingImportedCall(
			"github.com/sipeed/picoclaw/pkg/tools",
			"ErrorResult",
		) {
		t.Fatalf(
			"reviewed imported non-logging allowances drifted: %#v",
			p015ReviewedNonLoggingImportedCalls,
		)
	}
	valid, err := p015ScanLogging(p015LoggingScanOptions{
		RepoRoot:   repoRoot,
		ModulePath: p015ModulePath,
		Roots:      []string{"scripts/testdata/p015b2_logging_gate/valid"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(valid.Escapes) != 0 {
		t.Fatalf("valid fixture produced escapes: %#v", valid.Escapes)
	}
	p015RequireFixtureSite(t, valid.Sites, ".<init:var:initialized>.$lit1", "pico.Info")
	p015RequireFixtureSite(t, valid.Sites, ".aliasAndLiteral", "pico.Warn")
	p015RequireFixtureSite(t, valid.Sites, ".aliasAndLiteral.$lit1", "pico.Error")
	p015RequireFixtureSite(t, valid.Sites, ".aliasAndLiteral", "pico.Debug")
	p015RequireFixtureSite(t, valid.Sites, ".functionalFormatting", "fmt.Fprintf")
	p015RequireFixtureSite(t, valid.Sites, ".functionalFormatting", "io.WriteString")
	literalCalls := p015FixtureCalls(valid.Sites, ".literalWhitespace", "pico.Info")
	if len(literalCalls) != 3 {
		t.Fatalf("literal-whitespace fixture calls = %#v, want exact three", literalCalls)
	}
	wantLiteralFragments := []string{`"a  b"`, `"a b"`, "`first line\n\t  second line`"}
	for _, fragment := range wantLiteralFragments {
		found := false
		for _, call := range literalCalls {
			if strings.Contains(call, fragment) {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("canonical literal calls do not preserve %q: %#v", fragment, literalCalls)
		}
	}
	uniqueLiteralCalls := make(map[string]struct{}, len(literalCalls))
	for _, call := range literalCalls {
		uniqueLiteralCalls[call] = struct{}{}
	}
	if len(uniqueLiteralCalls) != len(literalCalls) {
		t.Fatalf("literal whitespace collapsed distinct calls: %#v", literalCalls)
	}

	bypass, err := p015ScanLogging(p015LoggingScanOptions{
		RepoRoot:   repoRoot,
		ModulePath: p015ModulePath,
		Roots:      []string{"scripts/testdata/p015b2_logging_gate/bypass"},
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, fragment := range []string{
		"dot logging import",
		"forbidden standard logging import log",
		"forbidden standard logging import log/slog",
		"forbidden logging adapter import github.com/go-logr/logr",
		"forbidden logging adapter import github.com/rs/zerolog",
		"forbidden Pico logger facade type reference logger.Logger",
		"forbidden Pico logger facade constructor reference logger.NewLogger",
		"logging import alias \"pico\"",
		"output function value fmt.Println",
		"output function value github.com/sipeed/picoclaw/pkg/logger.Error",
		"output function value github.com/sipeed/picoclaw/pkg/logger.InfoSafeCF",
		"output function value github.com/sipeed/picoclaw/pkg/logger.InitPanic",
		"output function value github.com/sipeed/picoclaw/pkg/logger.RecoverPanicNoExit",
		"direct os.Stderr output handle",
		"direct os.Stdout output handle",
		"forbidden log output call Print",
		"forbidden log/slog output call Info",
		"runtime/debug stack output or capture",
		"unresolved emitter-like receiver call Debug",
		"unresolved emitter-like receiver call Error",
		"unresolved emitter-like receiver call Info",
		"unresolved emitter-like receiver call Log",
		"unresolved emitter-like receiver call Print",
		"unresolved emitter-like receiver call Warn",
		"unreviewed emitter-like imported call example.invalid/foreign.Info",
		"unresolved emitter-like receiver call Trace",
		"unresolved emitter-like receiver call Notice",
		"unresolved emitter-like receiver call Crit",
		"unresolved emitter-like receiver call Critical",
		"unresolved emitter-like receiver call DPanic",
		"unresolved emitter-like receiver call Alert",
		"unresolved emitter-like receiver call Emergency",
		"unreviewed emitter-like imported call example.invalid/foreign.Trace",
		"unreviewed emitter-like imported call example.invalid/foreign.Notice",
		"unreviewed emitter-like imported call example.invalid/foreign.Crit",
		"unreviewed emitter-like imported call example.invalid/foreign.Critical",
		"unreviewed emitter-like imported call example.invalid/foreign.DPanic",
		"unreviewed emitter-like imported call example.invalid/foreign.Alert",
		"unreviewed emitter-like imported call example.invalid/foreign.Emergency",
		"unresolved emitter-like receiver method value #1 sink.Info",
		"unresolved emitter-like receiver method value #2 sink.Error",
		"unresolved emitter-like receiver method value #3 sink.ErrorResult",
		"unreviewed emitter-like imported method value example.invalid/foreign.Info",
		"unreviewed emitter-like imported method value example.invalid/foreign.Error",
	} {
		p015RequireEscape(t, bypass.Escapes, fragment)
	}
	for _, expectation := range []struct {
		reason      string
		ownerSuffix string
		want        int
	}{
		{"forbidden Pico logger facade type reference logger.Logger", ".<facade>", 3},
		{"forbidden Pico logger facade constructor reference logger.NewLogger", ".<facade>", 3},
		{"unresolved emitter-like receiver call", ".injectedFacade", 9},
		{"unresolved emitter-like receiver call", ".picoFacadeBypasses", 4},
		{"unresolved emitter-like receiver call", ".standardFacadeBypasses", 4},
		{"unreviewed emitter-like imported call", ".bypasses", 8},
		{"unresolved emitter-like receiver method value", ".injectedFacade", 3},
		{"unreviewed emitter-like imported method value", ".foreignMethodValues", 2},
		{"unresolved emitter-like receiver method value", ".<init:var:injectedGlobalEmitter>", 1},
		{"unreviewed emitter-like imported method value", ".<init:var:foreignGlobalEmitter>", 1},
		{"unresolved emitter-like receiver method value", ".passEmitterValues", 1},
		{"unreviewed emitter-like imported method value", ".passEmitterValues", 1},
		{"unresolved emitter-like receiver method value", ".returnInjectedEmitter", 1},
		{"unreviewed emitter-like imported method value", ".returnForeignEmitter", 1},
	} {
		got := p015CountEscapes(
			bypass.Escapes,
			expectation.reason,
			expectation.ownerSuffix,
		)
		if got != expectation.want {
			t.Errorf(
				"escape count for %q owner %q = %d, want %d",
				expectation.reason,
				expectation.ownerSuffix,
				got,
				expectation.want,
			)
		}
	}
	for _, reason := range []string{
		"forbidden standard logging import log",
		"forbidden standard logging import log/slog",
	} {
		if got := p015CountExactEscapes(bypass.Escapes, reason, ".<imports>"); got != 1 {
			t.Errorf("exact escape count for %q = %d, want 1", reason, got)
		}
	}
	p015RequireFixtureSite(t, bypass.Sites, ".facade", "pico.Warn")
	p015RequireFixtureSite(t, bypass.Sites, ".bypasses", "fmt.Fprintf")
	p015RequireFixtureSite(t, bypass.Sites, ".bypasses", "builtin.print")

	for _, disposition := range []string{
		"b1_certified_safe",
		"b2a_safe",
		"b2a_retired",
		"functional_allow",
		"functional_retired",
		"b4_excluded",
		"crash_artifact",
	} {
		if !p015HistorySourceImmutable(disposition) {
			t.Errorf("history disposition %s unexpectedly permits source-tuple mutation", disposition)
		}
	}
	for _, disposition := range []string{"b2b_deferred", "b2c_logger_deferred", "b2c_console_deferred"} {
		if p015HistorySourceImmutable(disposition) {
			t.Errorf("pending disposition %s is immutable before migration", disposition)
		}
	}
}

func TestP015B2PicoLoggerExportSurfaceClosed(t *testing.T) {
	repoRoot := p015FindRepoRoot(t)
	actual, err := p015DiscoverPicoLoggerExportSurface(repoRoot)
	if err != nil {
		t.Fatal(err)
	}
	if issues := p015ComparePicoLoggerExportSurface(actual); len(issues) != 0 {
		t.Fatalf("Pico logger export surface drifted:\n%s", strings.Join(issues, "\n"))
	}

	withUnknown := append(
		append([]p015LoggerSurface(nil), actual...),
		p015LoggerSurface{Key: "func:FutureUnclassifiedLoggerAPI", File: "future.go"},
	)
	if issues := p015ComparePicoLoggerExportSurface(withUnknown); len(issues) == 0 {
		t.Fatal("synthetic future logger export did not fail the closed surface")
	}
}

func TestP015B2FrozenTombstoneGoldenRejectsEveryMutation(t *testing.T) {
	repoRoot := p015FindRepoRoot(t)
	ledger := p015ReadLoggingLedger(
		t,
		filepath.Join(repoRoot, filepath.FromSlash(p015LoggingLedgerPath)),
	)
	golden, err := p015ReadFrozenTombstones(
		filepath.Join(repoRoot, filepath.FromSlash(p015TombstoneGoldenPath)),
	)
	if err != nil {
		t.Fatal(err)
	}
	if issues := p015FrozenTombstoneIssues(ledger, golden); len(issues) != 0 {
		t.Fatalf("frozen tombstone manifest drifted:\n%s", strings.Join(issues, "\n"))
	}

	for index, row := range ledger {
		if _, frozen := p015ExpectedFrozenTombstoneIDs[row.ID]; !frozen {
			continue
		}
		t.Run(row.ID+"_call", func(t *testing.T) {
			mutated := append([]p015LoggingSite(nil), ledger...)
			mutated[index].Call += " /* mutation */"
			if issues := p015FrozenTombstoneIssues(mutated, golden); len(issues) == 0 {
				t.Fatal("tombstone call mutation was accepted")
			}
		})
		t.Run(row.ID+"_id", func(t *testing.T) {
			mutated := append([]p015LoggingSite(nil), ledger...)
			mutated[index].ID = "Z" + row.ID[1:]
			if issues := p015FrozenTombstoneIssues(mutated, golden); len(issues) == 0 {
				t.Fatal("tombstone ID mutation was accepted")
			}
		})
	}
}

func TestP015B2PrintFrozenTombstoneGolden(t *testing.T) {
	if os.Getenv("P015B2_PRINT_TOMBSTONE_GOLDEN") != "1" {
		t.Skip("set P015B2_PRINT_TOMBSTONE_GOLDEN=1 to print the frozen manifest")
	}
	repoRoot := p015FindRepoRoot(t)
	ledger := p015ReadLoggingLedger(
		t,
		filepath.Join(repoRoot, filepath.FromSlash(p015LoggingLedgerPath)),
	)
	rows := make([]p015LoggingSite, 0, len(p015ExpectedFrozenTombstoneIDs))
	for _, row := range ledger {
		if _, frozen := p015ExpectedFrozenTombstoneIDs[row.ID]; frozen {
			rows = append(rows, row)
		}
	}
	sort.Slice(rows, func(left, right int) bool { return rows[left].ID < rows[right].ID })
	fmt.Println("# p015b2-frozen-d08-tombstones-v1")
	fmt.Println("# id\tfile\towner\tordinal\tcallee\tsha256(file\\0owner\\0ordinal\\0callee\\0canonical_call)")
	for _, row := range rows {
		fmt.Printf(
			"%s\t%s\t%s\t%d\t%s\t%s\n",
			row.ID,
			row.File,
			row.Owner,
			row.Ordinal,
			row.Callee,
			row.tombstoneProvenanceHash(),
		)
	}
}

func TestP015B2LoggingInventoryMonotonicHistory(t *testing.T) {
	repoRoot := p015FindRepoRoot(t)
	currentPath := filepath.Join(repoRoot, filepath.FromSlash(p015LoggingLedgerPath))
	revisions, err := p015LoggingLedgerRevisions(repoRoot, currentPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(revisions) == 0 {
		t.Fatal("logging ledger history has no tracked or working-tree revision")
	}
	for _, issue := range p015LedgerRevisionHistoryIssues(revisions) {
		t.Error(issue)
	}
}

func TestP015B2PendingCohortMigrationMappingClosed(t *testing.T) {
	repoRoot := p015FindRepoRoot(t)
	ledgerPath := filepath.Join(repoRoot, filepath.FromSlash(p015LoggingLedgerPath))
	ledger := p015ReadLoggingLedger(t, ledgerPath)
	scan, err := p015ScanLogging(p015LoggingScanOptions{
		RepoRoot:   repoRoot,
		ModulePath: p015ModulePath,
		Roots:      []string{"pkg/agent", "pkg/gateway"},
	})
	if err != nil {
		t.Fatal(err)
	}
	migrated, err := p015MigrateB2BRows(ledger, scan.Sites)
	if err != nil {
		t.Fatal(err)
	}
	for index, row := range ledger {
		if p015SiteCohort(row.ID) != "B" {
			continue
		}
		got := migrated[index]
		if got.ID != row.ID || got.File != row.File || got.Owner != row.Owner ||
			got.Ordinal != row.Ordinal {
			t.Errorf("%s migration did not preserve ID/file/owner/ordinal", row.ID)
		}
		if got.Disposition != "b2b_safe" || got.Kind != "pico_safe" ||
			!strings.HasSuffix(got.Callee, "SafeCF") || got.Callee == "pico.DebugSensitiveCF" {
			t.Errorf("%s migration target is not direct safe-only: %#v", row.ID, got)
		}
	}
}

// TestP015B2BReviewedSafeSignatures is deliberately independent of the
// migration ledger rewrite path. The ledger preserves stable source identity;
// this reviewed golden freezes the exact safe call selected for every B ID so
// regenerating the ledger cannot bless a swapped message, helper, field, or
// error class during the first pending-to-safe transition.
func TestP015B2BReviewedSafeSignatures(t *testing.T) {
	repoRoot := p015FindRepoRoot(t)
	ledger := p015ReadLoggingLedger(
		t,
		filepath.Join(repoRoot, filepath.FromSlash(p015LoggingLedgerPath)),
	)
	signatures := p015ReadB2BReviewedSignatures(
		t,
		filepath.Join(repoRoot, filepath.FromSlash(p015B2BSignaturePath)),
	)
	scan, err := p015ScanLogging(p015LoggingScanOptions{
		RepoRoot:   repoRoot,
		ModulePath: p015ModulePath,
		Roots:      []string{"pkg/agent", "pkg/gateway"},
	})
	if err != nil {
		t.Fatal(err)
	}
	current := make(map[string]struct{}, len(scan.Sites))
	for _, site := range scan.Sites {
		current[site.sourceKey()] = struct{}{}
	}

	seen := make(map[string]struct{}, len(signatures))
	for _, row := range ledger {
		if p015SiteCohort(row.ID) != "B" {
			continue
		}
		signature, ok := signatures[row.ID]
		if !ok {
			t.Errorf("reviewed B signature is missing %s", row.ID)
			continue
		}
		seen[row.ID] = struct{}{}
		if row.Callee != signature.Callee || row.Call != signature.Call {
			t.Errorf(
				"%s safe call differs from the reviewed signature\ncallee: %s\nwant:   %s\ncall:\n%s\nwant:\n%s",
				row.ID,
				row.Callee,
				signature.Callee,
				row.Call,
				signature.Call,
			)
		}
		if _, exists := current[row.sourceKey()]; !exists {
			t.Errorf("%s reviewed safe signature is absent from current source", row.ID)
		}
	}
	for index := 1; index <= p015CohortSizes["B"]; index++ {
		id := fmt.Sprintf("B%03d", index)
		if _, ok := signatures[id]; !ok {
			t.Errorf("reviewed signature golden is missing %s", id)
		}
		if _, ok := seen[id]; !ok {
			t.Errorf("current B ledger did not consume reviewed signature %s", id)
		}
	}
	if got, want := len(signatures), p015CohortSizes["B"]; got != want {
		t.Errorf("reviewed signature count = %d, want %d", got, want)
	}
}

type p015B2BReviewedSignature struct {
	Callee string
	Call   string
}

func p015ReadB2BReviewedSignatures(
	t *testing.T,
	path string,
) map[string]p015B2BReviewedSignature {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	signatures := make(map[string]p015B2BReviewedSignature)
	previousID := ""
	for index, rawLine := range strings.Split(string(data), "\n") {
		line := strings.TrimSpace(rawLine)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.Split(rawLine, "\t")
		if len(fields) != 3 {
			t.Fatalf("reviewed signature line %d has %d fields, want 3", index+1, len(fields))
		}
		id := fields[0]
		if id <= previousID {
			t.Fatalf("reviewed signature IDs are not strictly sorted: %s then %s", previousID, id)
		}
		previousID = id
		if _, duplicate := signatures[id]; duplicate {
			t.Fatalf("duplicate reviewed signature %s", id)
		}
		call, decodeErr := base64.RawURLEncoding.DecodeString(fields[2])
		if decodeErr != nil {
			t.Fatalf("reviewed signature line %d has invalid call encoding: %v", index+1, decodeErr)
		}
		signatures[id] = p015B2BReviewedSignature{Callee: fields[1], Call: string(call)}
	}
	return signatures
}

func TestP015B2RewriteB2BLoggingInventory(t *testing.T) {
	if os.Getenv("P015B2_REWRITE_B2B_INVENTORY") != "1" {
		t.Skip("set P015B2_REWRITE_B2B_INVENTORY=1 to apply the closed B migration")
	}
	repoRoot := p015FindRepoRoot(t)
	ledgerPath := filepath.Join(repoRoot, filepath.FromSlash(p015LoggingLedgerPath))
	data, err := os.ReadFile(ledgerPath)
	if err != nil {
		t.Fatal(err)
	}
	ledger, err := p015ParseLoggingLedger(data)
	if err != nil {
		t.Fatal(err)
	}
	scan, err := p015ScanLogging(p015LoggingScanOptions{
		RepoRoot:   repoRoot,
		ModulePath: p015ModulePath,
		Roots:      []string{"pkg/agent", "pkg/gateway"},
	})
	if err != nil {
		t.Fatal(err)
	}
	migrated, err := p015MigrateB2BRows(ledger, scan.Sites)
	if err != nil {
		t.Fatal(err)
	}

	var output strings.Builder
	for _, line := range strings.Split(string(data), "\n") {
		if !strings.HasPrefix(line, "#") {
			break
		}
		output.WriteString(line)
		output.WriteByte('\n')
	}
	for _, row := range migrated {
		output.WriteString(p015FormatLedgerRow(row))
		output.WriteByte('\n')
	}
	if err := os.WriteFile(ledgerPath, []byte(output.String()), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestP015B2CollapsedPendingRowsPreserveHistory(t *testing.T) {
	testCases := []struct {
		id                 string
		pendingDisposition string
		retiredDisposition string
		safeDisposition    string
	}{
		{"B001", "b2b_deferred", "b2b_retired", "b2b_safe"},
		{"G001", "b2c_logger_deferred", "b2c_logger_retired", "b2c_logger_safe"},
		{"C001", "b2c_console_deferred", "b2c_console_retired", "b2c_console_safe"},
	}
	for _, testCase := range testCases {
		t.Run(testCase.id, func(t *testing.T) {
			old := p015LoggingSite{
				ID:          testCase.id,
				Disposition: testCase.pendingDisposition,
				File:        "pkg/fixture.go",
				Owner:       p015ModulePath + "/pkg/fixture.owner",
				Ordinal:     1,
				Kind:        "pico_legacy",
				Callee:      "pico.WarnCF",
				Call:        "logger.WarnCF(\"component\", \"message\", nil)",
			}
			retired := old
			retired.Disposition = testCase.retiredDisposition
			retired.Kind = "retired"
			if issues := p015LedgerHistoryIssues(
				[]p015LoggingSite{old},
				[]p015LoggingSite{retired},
			); len(issues) != 0 {
				t.Fatalf("exact pending-to-retired tuple rejected: %v", issues)
			}

			mutated := retired
			mutated.Call += " /* mutated */"
			if issues := p015LedgerHistoryIssues(
				[]p015LoggingSite{old},
				[]p015LoggingSite{mutated},
			); len(issues) == 0 {
				t.Fatal("pending-to-retired call mutation was accepted")
			}

			mutatedID := retired
			mutatedID.ID = "Z999"
			if issues := p015LedgerHistoryIssues(
				[]p015LoggingSite{old},
				[]p015LoggingSite{mutatedID},
			); len(issues) == 0 {
				t.Fatal("pending-to-retired ID mutation was accepted")
			}

			safe := old
			safe.Disposition = testCase.safeDisposition
			safe.Kind = "pico_safe"
			safe.Callee = "pico.WarnSafeCF"
			safe.Call = "logger.WarnSafeCF(component, message, fields)"
			if issues := p015LedgerHistoryIssues(
				[]p015LoggingSite{old},
				[]p015LoggingSite{safe},
			); len(issues) != 0 {
				t.Fatalf("pending-to-safe source migration rejected: %v", issues)
			}

			for name, mutate := range map[string]func(*p015LoggingSite){
				"file":    func(row *p015LoggingSite) { row.File = "pkg/other.go" },
				"owner":   func(row *p015LoggingSite) { row.Owner += ".other" },
				"ordinal": func(row *p015LoggingSite) { row.Ordinal++ },
			} {
				t.Run("safe_rejects_"+name, func(t *testing.T) {
					mutated := safe
					mutate(&mutated)
					if issues := p015LedgerHistoryIssues(
						[]p015LoggingSite{old},
						[]p015LoggingSite{mutated},
					); len(issues) == 0 {
						t.Fatalf("pending-to-safe %s mutation was accepted", name)
					}
				})
			}

			for name, mutate := range map[string]func(*p015LoggingSite){
				"file":    func(row *p015LoggingSite) { row.File = "pkg/other.go" },
				"owner":   func(row *p015LoggingSite) { row.Owner += ".other" },
				"ordinal": func(row *p015LoggingSite) { row.Ordinal++ },
				"callee":  func(row *p015LoggingSite) { row.Callee = "pico.ErrorCF" },
				"call":    func(row *p015LoggingSite) { row.Call += " /* mutation */" },
			} {
				t.Run("retired_rejects_"+name, func(t *testing.T) {
					mutated := retired
					mutate(&mutated)
					if issues := p015LedgerHistoryIssues(
						[]p015LoggingSite{old},
						[]p015LoggingSite{mutated},
					); len(issues) == 0 {
						t.Fatalf("pending-to-retired %s mutation was accepted", name)
					}
				})
			}
		})
	}
}

func TestP015B2LedgerHistoryReplaysEveryAdjacentRevision(t *testing.T) {
	old := p015LoggingSite{
		ID:          "B001",
		Disposition: "b2b_deferred",
		File:        "pkg/agent/old.go",
		Owner:       p015ModulePath + "/pkg/agent.old",
		Ordinal:     1,
		Kind:        "pico_legacy",
		Callee:      "pico.WarnCF",
		Call:        `logger.WarnCF("agent", "old", nil)`,
	}
	badFirstTransition := old
	badFirstTransition.Disposition = "b2b_safe"
	badFirstTransition.Kind = "pico_safe"
	badFirstTransition.Callee = "pico.WarnSafeCF"
	badFirstTransition.Call = "logger.WarnSafeCF(component, message, fields)"
	badFirstTransition.File = "pkg/agent/moved.go"
	unchangedLater := badFirstTransition

	revisions := []p015LoggingLedgerRevision{
		{label: "revision-1", rows: []p015LoggingSite{old}},
		{label: "revision-2", rows: []p015LoggingSite{badFirstTransition}},
		{label: "revision-3", rows: []p015LoggingSite{unchangedLater}},
	}
	issues := p015LedgerRevisionHistoryIssues(revisions)
	if len(issues) == 0 || !strings.Contains(strings.Join(issues, "\n"), "revision-1 -> revision-2") {
		t.Fatalf("bad first transition hidden by later revision: %v", issues)
	}

	goodFirstTransition := badFirstTransition
	goodFirstTransition.File = old.File
	revisions[1].rows = []p015LoggingSite{goodFirstTransition}
	revisions[2].rows = []p015LoggingSite{goodFirstTransition}
	if issues := p015LedgerRevisionHistoryIssues(revisions); len(issues) != 0 {
		t.Fatalf("valid adjacent transition history rejected: %v", issues)
	}
}

func TestP015B2PrintLoggingInventory(t *testing.T) {
	if os.Getenv("P015B2_PRINT_LOGGING_INVENTORY") != "1" {
		t.Skip("set P015B2_PRINT_LOGGING_INVENTORY=1 to print a reviewed baseline candidate")
	}
	repoRoot := p015FindRepoRoot(t)
	scanRoot := repoRoot
	if override := os.Getenv("P015B2_SCAN_REPO_ROOT"); override != "" {
		scanRoot = override
	}
	scan, err := p015ScanLogging(p015LoggingScanOptions{
		RepoRoot:   scanRoot,
		ModulePath: p015ModulePath,
		Roots:      []string{"pkg/agent", "pkg/gateway"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(scan.Escapes) != 0 {
		t.Fatalf("refusing to generate around escapes: %#v", scan.Escapes)
	}
	rows, err := p015ClassifyBaseline(scan.Sites)
	if err != nil {
		t.Fatal(err)
	}
	fmt.Println("# p015b2-logging-inventory-v1")
	fmt.Println("# site_id\tdisposition\tfile\towner\tordinal\tkind\tcallee\tcall_base64url\tcanary_tests")
	for _, row := range rows {
		fmt.Println(p015FormatLedgerRow(row))
	}
}

func p015FindRepoRoot(t *testing.T) string {
	t.Helper()
	directory, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(directory, "go.mod")); err == nil {
			return directory
		}
		parent := filepath.Dir(directory)
		if parent == directory {
			t.Fatal("repository root not found")
		}
		directory = parent
	}
}

type p015LoggerSurface struct {
	Key  string
	File string
}

func p015DiscoverPicoLoggerExportSurface(repoRoot string) ([]p015LoggerSurface, error) {
	root := filepath.Join(repoRoot, "pkg", "logger")
	fileSet := token.NewFileSet()
	seen := make(map[string]string)
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".go") ||
			strings.HasSuffix(entry.Name(), "_test.go") {
			return nil
		}
		relative, err := filepath.Rel(repoRoot, path)
		if err != nil {
			return err
		}
		relative = filepath.ToSlash(relative)
		parsed, err := parser.ParseFile(fileSet, path, nil, parser.AllErrors)
		if err != nil {
			return fmt.Errorf("parse Pico logger surface %s: %w", relative, err)
		}
		for _, declaration := range parsed.Decls {
			switch declaration := declaration.(type) {
			case *ast.FuncDecl:
				if !ast.IsExported(declaration.Name.Name) {
					continue
				}
				key := "func:" + declaration.Name.Name
				if declaration.Recv != nil && len(declaration.Recv.List) != 0 {
					key = "method:" +
						p015RenderNode(fileSet, declaration.Recv.List[0].Type) +
						"." + declaration.Name.Name
				}
				if previous, duplicate := seen[key]; duplicate {
					return fmt.Errorf("duplicate Pico logger surface %s in %s and %s", key, previous, relative)
				}
				seen[key] = relative
			case *ast.GenDecl:
				for _, rawSpec := range declaration.Specs {
					switch spec := rawSpec.(type) {
					case *ast.TypeSpec:
						if ast.IsExported(spec.Name.Name) {
							seen["type:"+spec.Name.Name] = relative
						}
					case *ast.ValueSpec:
						if declaration.Tok != token.VAR {
							continue
						}
						for _, name := range spec.Names {
							if ast.IsExported(name.Name) {
								seen["var:"+name.Name] = relative
							}
						}
					}
				}
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	result := make([]p015LoggerSurface, 0, len(seen))
	for key, file := range seen {
		result = append(result, p015LoggerSurface{Key: key, File: file})
	}
	sort.Slice(result, func(left, right int) bool { return result[left].Key < result[right].Key })
	return result, nil
}

func p015ComparePicoLoggerExportSurface(actual []p015LoggerSurface) []string {
	seen := make(map[string]string, len(actual))
	var issues []string
	for _, item := range actual {
		if previous, duplicate := seen[item.Key]; duplicate {
			issues = append(issues, fmt.Sprintf("duplicate export %s in %s and %s", item.Key, previous, item.File))
			continue
		}
		seen[item.Key] = item.File
		class, classified := p015PicoLoggerExportSurface[item.Key]
		if !classified {
			issues = append(issues, fmt.Sprintf("unclassified export %s in %s", item.Key, item.File))
			continue
		}
		if class != p015LoggerSurfaceEmitter &&
			class != p015LoggerSurfaceNonEmitter &&
			class != p015LoggerSurfaceForbidden {
			issues = append(issues, fmt.Sprintf("export %s has invalid classification %q", item.Key, class))
		}
	}
	for key := range p015PicoLoggerExportSurface {
		if _, exists := seen[key]; !exists {
			issues = append(issues, "classified Pico logger export no longer exists: "+key)
		}
	}
	sort.Strings(issues)
	return issues
}

type p015FrozenTombstone struct {
	ID      string
	File    string
	Owner   string
	Ordinal int
	Callee  string
	Hash    string
}

func p015ReadFrozenTombstones(path string) ([]p015FrozenTombstone, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var result []p015FrozenTombstone
	for lineIndex, rawLine := range strings.Split(string(data), "\n") {
		line := strings.TrimSpace(rawLine)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.Split(rawLine, "\t")
		if len(fields) != 6 {
			return nil, fmt.Errorf(
				"frozen tombstone line %d has %d fields, want 6",
				lineIndex+1,
				len(fields),
			)
		}
		ordinal, err := strconv.Atoi(fields[3])
		if err != nil || ordinal < 1 {
			return nil, fmt.Errorf(
				"frozen tombstone line %d has invalid ordinal %q",
				lineIndex+1,
				fields[3],
			)
		}
		if decoded, decodeErr := hex.DecodeString(fields[5]); decodeErr != nil || len(decoded) != 32 {
			return nil, fmt.Errorf(
				"frozen tombstone line %d has invalid SHA-256 %q",
				lineIndex+1,
				fields[5],
			)
		}
		result = append(result, p015FrozenTombstone{
			ID: fields[0], File: fields[1], Owner: fields[2],
			Ordinal: ordinal, Callee: fields[4], Hash: fields[5],
		})
	}
	return result, nil
}

func p015FrozenTombstoneIssues(
	ledger []p015LoggingSite,
	golden []p015FrozenTombstone,
) []string {
	ledgerByID := make(map[string]p015LoggingSite)
	for _, row := range ledger {
		if _, frozen := p015ExpectedFrozenTombstoneIDs[row.ID]; frozen {
			ledgerByID[row.ID] = row
		}
	}
	goldenByID := make(map[string]p015FrozenTombstone)
	var issues []string
	for _, row := range golden {
		if _, expected := p015ExpectedFrozenTombstoneIDs[row.ID]; !expected {
			issues = append(issues, "unexpected frozen tombstone ID "+row.ID)
			continue
		}
		if _, duplicate := goldenByID[row.ID]; duplicate {
			issues = append(issues, "duplicate frozen tombstone ID "+row.ID)
			continue
		}
		goldenByID[row.ID] = row
	}
	for id := range p015ExpectedFrozenTombstoneIDs {
		ledgerRow, ledgerExists := ledgerByID[id]
		goldenRow, goldenExists := goldenByID[id]
		if !ledgerExists {
			issues = append(issues, "ledger is missing frozen tombstone "+id)
			continue
		}
		if !goldenExists {
			issues = append(issues, "golden is missing frozen tombstone "+id)
			continue
		}
		if ledgerRow.File != goldenRow.File ||
			ledgerRow.Owner != goldenRow.Owner ||
			ledgerRow.Ordinal != goldenRow.Ordinal ||
			ledgerRow.Callee != goldenRow.Callee {
			issues = append(issues, "frozen tombstone provenance changed for "+id)
		}
		if got := ledgerRow.tombstoneProvenanceHash(); got != goldenRow.Hash {
			issues = append(
				issues,
				fmt.Sprintf("frozen tombstone hash changed for %s: %s != %s", id, got, goldenRow.Hash),
			)
		}
	}
	sort.Strings(issues)
	return issues
}

func p015ReadLoggingLedger(t *testing.T, path string) []p015LoggingSite {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	rows, err := p015ParseLoggingLedger(data)
	if err != nil {
		t.Fatal(err)
	}
	return rows
}

func p015ParseLoggingLedger(data []byte) ([]p015LoggingSite, error) {
	var rows []p015LoggingSite
	for index, rawLine := range strings.Split(string(data), "\n") {
		line := strings.TrimSpace(rawLine)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.Split(rawLine, "\t")
		if len(fields) != 9 {
			return nil, fmt.Errorf("ledger line %d has %d fields, want 9", index+1, len(fields))
		}
		ordinal, err := strconv.Atoi(fields[4])
		if err != nil || ordinal < 1 {
			return nil, fmt.Errorf("ledger line %d has invalid ordinal %q", index+1, fields[4])
		}
		call, err := base64.RawURLEncoding.DecodeString(fields[7])
		if err != nil {
			return nil, fmt.Errorf("ledger line %d has invalid call encoding: %w", index+1, err)
		}
		rows = append(rows, p015LoggingSite{
			ID:          fields[0],
			Disposition: fields[1],
			File:        fields[2],
			Owner:       fields[3],
			Ordinal:     ordinal,
			Kind:        fields[5],
			Callee:      fields[6],
			Call:        string(call),
			Canary:      fields[8],
		})
	}
	return rows, nil
}

func p015FormatLedgerRow(row p015LoggingSite) string {
	return strings.Join([]string{
		row.ID,
		row.Disposition,
		row.File,
		row.Owner,
		strconv.Itoa(row.Ordinal),
		row.Kind,
		row.Callee,
		base64.RawURLEncoding.EncodeToString([]byte(row.Call)),
		row.Canary,
	}, "\t")
}

func p015ValidateLedger(t *testing.T, repoRoot string, rows []p015LoggingSite) {
	t.Helper()
	golden, err := p015ReadFrozenTombstones(
		filepath.Join(repoRoot, filepath.FromSlash(p015TombstoneGoldenPath)),
	)
	if err != nil {
		t.Errorf("read frozen tombstone golden: %v", err)
	} else {
		for _, issue := range p015FrozenTombstoneIssues(rows, golden) {
			t.Error(issue)
		}
	}
	counts := make(map[string]int)
	activeCounts := make(map[string]int)
	retiredCounts := make(map[string]int)
	stageCounts := make(map[string]int)
	b2bFileCounts := make(map[string]int)
	seenIDs := make(map[string]struct{}, len(rows))
	previousID := ""
	for _, row := range rows {
		if _, duplicate := seenIDs[row.ID]; duplicate {
			t.Errorf("duplicate ledger ID %s", row.ID)
		}
		seenIDs[row.ID] = struct{}{}
		if previousID != "" && row.ID <= previousID {
			t.Errorf("ledger IDs are not strictly sorted: %s then %s", previousID, row.ID)
		}
		previousID = row.ID
		cohort := p015SiteCohort(row.ID)
		counts[cohort]++
		stageCounts[p015StageCountKey(cohort, row.Disposition, row.Kind)]++
		if p015RetiredDisposition(row.Disposition) {
			retiredCounts[cohort]++
		} else {
			activeCounts[cohort]++
		}
		p015ValidateDisposition(t, row, cohort)
		if cohort == "A" {
			p015ValidateAOwnerCanary(t, row)
		}
		if cohort == "B" {
			b2bFileCounts[row.File]++
			p015ValidateB2BOwnerCanary(t, row)
		}
		if cohort == "F" && row.Disposition == "functional_retired" {
			p015ValidateFunctionalTombstone(t, row)
		}
		if p015SafeDisposition(row.Disposition) || p015RetiredDisposition(row.Disposition) {
			p015ValidateCanaries(t, repoRoot, row)
		} else if row.Canary != "-" {
			t.Errorf("%s pending/fixed row unexpectedly owns canary %q", row.ID, row.Canary)
		}
	}
	for cohort, want := range p015CohortSizes {
		if got := counts[cohort]; got != want {
			t.Errorf("cohort %s has %d rows, want %d", cohort, got, want)
		}
		for index := 1; index <= want; index++ {
			id := fmt.Sprintf("%s%03d", cohort, index)
			if _, exists := seenIDs[id]; !exists {
				t.Errorf("closed cohort %s is missing ID %s", cohort, id)
			}
		}
		if got, wantActive := activeCounts[cohort], p015ActiveCohortSizes[cohort]; got != wantActive {
			t.Errorf("cohort %s has %d active rows, want %d", cohort, got, wantActive)
		}
	}
	for stage, want := range p015ExpectedStageCounts {
		if got := stageCounts[stage]; got != want {
			t.Errorf("ledger stage %s has %d rows, want exact %d", stage, got, want)
		}
	}
	for stage, got := range stageCounts {
		if _, expected := p015ExpectedStageCounts[stage]; !expected {
			t.Errorf("ledger has unexpected stage %s with %d rows", stage, got)
		}
	}
	if len(p015B2BFiles) != 27 {
		t.Errorf("closed P015b2b file map has %d files, want exact 27", len(p015B2BFiles))
	}
	for file, expectation := range p015B2BFiles {
		if got := b2bFileCounts[file]; got != expectation.count {
			t.Errorf("P015b2b file %s has %d stable rows, want exact %d", file, got, expectation.count)
		}
	}
	for file, got := range b2bFileCounts {
		if _, expected := p015B2BFiles[file]; !expected {
			t.Errorf("P015b2b has unexpected file %s with %d stable rows", file, got)
		}
	}
	if len(rows) != 379 {
		t.Errorf("ledger has %d stable rows, want exact 379", len(rows))
	}
	if got := activeCounts["A"] + activeCounts["B"] + activeCounts["H"] + activeCounts["G"]; got != 322 {
		t.Errorf("P015b2 active scoped Pico cohort has %d rows, want 322", got)
	}
	if got := counts["A"] + counts["B"] + counts["H"] + counts["G"]; got != 326 {
		t.Errorf("P015b2 stable scoped Pico cohort has %d rows, want 326", got)
	}
	if got := retiredCounts["A"]; got != 4 {
		t.Errorf("P015b2a has %d retired rows, want four collapsed recovery tombstones", got)
	}
	if got := retiredCounts["B"]; got != 0 {
		t.Errorf("P015b2b has %d retired rows, want exact zero", got)
	}
	if got := retiredCounts["F"]; got != 8 {
		t.Errorf("functional census has %d retired rows, want eight formatter tombstones", got)
	}
	const originalAIdentities = 109
	if got := originalAIdentities + counts["B"] + counts["H"]; got != 265 {
		t.Errorf("original Agent census has %d identities, want exact 265", got)
	}
	if got := counts["A"] - originalAIdentities; got != 7 {
		t.Errorf("current source has %d A projection extensions, want exact seven", got)
	}
	if got := counts["A"] + counts["B"] + counts["H"]; got != 272 {
		t.Errorf("stable Agent ledger has %d rows, want exact 272", got)
	}
	if got := activeCounts["A"] + activeCounts["B"] + activeCounts["H"]; got != 268 {
		t.Errorf("active Agent safe census has %d rows, want exact 268", got)
	}
	agentLegacy := 0
	for _, row := range rows {
		if strings.HasPrefix(row.File, "pkg/agent/") && row.Kind == "pico_legacy" {
			agentLegacy++
			if p015SiteCohort(row.ID) != "R" {
				t.Errorf("Agent legacy row %s remains outside the reserved R cohort", row.ID)
			}
		}
	}
	if agentLegacy != 7 {
		t.Errorf("Agent legacy census has %d rows, want only the seven reserved R rows", agentLegacy)
	}
	activeTotal := 0
	for _, count := range activeCounts {
		activeTotal += count
	}
	if activeTotal != 367 {
		t.Errorf("ledger has %d active source tuples, want exact 367", activeTotal)
	}
}

func p015StageCountKey(cohort, disposition, kind string) string {
	return strings.Join([]string{cohort, disposition, kind}, "|")
}

func p015ValidateDisposition(t *testing.T, row p015LoggingSite, cohort string) {
	t.Helper()
	valid := false
	switch cohort {
	case "A":
		_, coreFile := p015CoreAgentFiles[row.File]
		valid = coreFile && p015DispositionKind(
			row,
			"b2a_legacy", "pico_legacy",
			"b2a_safe", "pico_safe",
			"b2a_retired", "retired",
		)
	case "B":
		_, coreFile := p015CoreAgentFiles[row.File]
		valid = strings.HasPrefix(row.File, "pkg/agent/") && !coreFile &&
			row.File != "pkg/agent/hooks.go" && row.File != "pkg/agent/hook_process.go" &&
			row.File != "pkg/agent/runtime_event_logger.go" &&
			p015DispositionKind(row, "b2b_deferred", "pico_legacy", "b2b_safe", "pico_safe", "b2b_retired", "retired")
	case "H":
		valid = (row.File == "pkg/agent/hooks.go" || row.File == "pkg/agent/hook_process.go") &&
			p015DispositionKind(row, "b1_certified_safe", "pico_safe", "b1_retired", "retired")
	case "G":
		valid = strings.HasPrefix(row.File, "pkg/gateway/") &&
			p015DispositionKind(
				row,
				"b2c_logger_deferred", "pico_legacy",
				"b2c_logger_safe", "pico_safe",
				"b2c_logger_retired", "retired",
			)
	case "C":
		valid = strings.HasPrefix(row.File, "pkg/gateway/") &&
			p015DispositionKind(
				row,
				"b2c_console_deferred", "console",
				"b2c_console_safe", "pico_safe",
				"b2c_console_retired", "retired",
			)
	case "R":
		valid = row.File == "pkg/agent/runtime_event_logger.go" &&
			row.Disposition == "b4_excluded" &&
			row.Kind == "pico_legacy"
	case "X":
		valid = row.File == "pkg/gateway/gateway.go" &&
			row.Disposition == "crash_artifact" &&
			row.Kind == "panic_artifact" &&
			row.Callee == "pico.InitPanic"
	case "F":
		valid = p015DispositionKind(
			row,
			"functional_allow", "functional_fmt",
			"functional_allow", "functional_io",
			"functional_retired", "retired",
		)
	}
	if !valid {
		t.Errorf(
			"%s has invalid cohort/disposition tuple: %s %s %s %s",
			row.ID,
			row.File,
			row.Disposition,
			row.Kind,
			row.Callee,
		)
	}
}

func p015DispositionKind(row p015LoggingSite, pairs ...string) bool {
	for index := 0; index+1 < len(pairs); index += 2 {
		if row.Disposition == pairs[index] && row.Kind == pairs[index+1] {
			return true
		}
	}
	return false
}

func p015ValidateAOwnerCanary(t *testing.T, row p015LoggingSite) {
	t.Helper()
	const (
		application = "pkg/agent/p015b2a_application_logging_test.go#" +
			"TestP015B2AApplicationLoggingASTManifest"
		core = "pkg/agent/p015b2a_core_logging_test.go#" +
			"TestP015B2ACoreLoggingASTManifest"
		finalResponse = "pkg/agent/p015b2a_core_logging_test.go#" +
			"TestP015B2ACoreFinalResponseZeroPreviewAndParity"
	)
	_, applicationOwned := p015B2AApplicationFiles[row.File]
	required := core
	if applicationOwned {
		required = application
	}
	if !p015CanaryIncludes(row.Canary, required) {
		t.Errorf("%s lacks owning source canary %q", row.ID, required)
	}
	if (row.ID == "A024" || row.ID == "A110") &&
		!p015CanaryIncludes(row.Canary, finalResponse) {
		t.Errorf("%s lacks final-response runtime canary %q", row.ID, finalResponse)
	}
}

func p015ValidateB2BOwnerCanary(t *testing.T, row p015LoggingSite) {
	t.Helper()
	expectation, exists := p015B2BFiles[row.File]
	if !exists {
		t.Errorf("%s is outside the closed P015b2b file map", row.ID)
		return
	}
	if row.Disposition == "b2b_deferred" {
		if row.Canary != "-" {
			t.Errorf("%s deferred row unexpectedly owns canary %q", row.ID, row.Canary)
		}
		return
	}
	if !p015CanaryIncludes(row.Canary, expectation.canary) {
		t.Errorf("%s lacks exact owning P015b2b source canary %q", row.ID, expectation.canary)
	}
}

func p015CanaryIncludes(canaries, target string) bool {
	for _, canary := range strings.Split(canaries, ",") {
		if canary == target {
			return true
		}
	}
	return false
}

func p015ValidateFunctionalTombstone(t *testing.T, row p015LoggingSite) {
	t.Helper()
	const application = "pkg/agent/p015b2a_application_logging_test.go#" +
		"TestP015B2AApplicationLoggingASTManifest"
	expected := map[string]struct {
		owner   string
		ordinal int
	}{
		"F002": {owner: p015ModulePath + "/pkg/agent.formatMessagesForLog", ordinal: 1},
		"F003": {owner: p015ModulePath + "/pkg/agent.formatMessagesForLog", ordinal: 2},
		"F004": {owner: p015ModulePath + "/pkg/agent.formatMessagesForLog", ordinal: 3},
		"F005": {owner: p015ModulePath + "/pkg/agent.formatMessagesForLog", ordinal: 4},
		"F006": {owner: p015ModulePath + "/pkg/agent.formatMessagesForLog", ordinal: 5},
		"F007": {owner: p015ModulePath + "/pkg/agent.formatToolsForLog", ordinal: 1},
		"F008": {owner: p015ModulePath + "/pkg/agent.formatToolsForLog", ordinal: 2},
		"F009": {owner: p015ModulePath + "/pkg/agent.formatToolsForLog", ordinal: 3},
	}
	want, exists := expected[row.ID]
	if !exists || row.File != "pkg/agent/agent_utils.go" ||
		row.Owner != want.owner || row.Ordinal != want.ordinal ||
		row.Callee != "fmt.Fprintf" || !p015CanaryIncludes(row.Canary, application) {
		t.Errorf("%s is not an exact owned d08 functional tombstone", row.ID)
	}
}

func p015ValidateCanaries(t *testing.T, repoRoot string, row p015LoggingSite) {
	t.Helper()
	if row.Canary == "" || row.Canary == "-" {
		t.Errorf("%s safe/retired row has no owning canary", row.ID)
		return
	}
	for _, reference := range strings.Split(row.Canary, ",") {
		parts := strings.Split(reference, "#")
		if len(parts) != 2 || !strings.HasSuffix(parts[0], "_test.go") || !strings.HasPrefix(parts[1], "Test") {
			t.Errorf("%s has invalid canary reference %q", row.ID, reference)
			continue
		}
		path := filepath.Clean(filepath.Join(repoRoot, filepath.FromSlash(parts[0])))
		if !strings.HasPrefix(path, filepath.Clean(repoRoot)+string(filepath.Separator)) {
			t.Errorf("%s canary escapes repository: %q", row.ID, reference)
			continue
		}
		file, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.SkipObjectResolution)
		if err != nil {
			t.Errorf("%s parse canary %q: %v", row.ID, reference, err)
			continue
		}
		found := false
		for _, declaration := range file.Decls {
			function, ok := declaration.(*ast.FuncDecl)
			if ok && function.Recv == nil && function.Name.Name == parts[1] && function.Body != nil {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("%s owning canary %q does not exist", row.ID, reference)
		}
	}
}

func p015SiteCohort(id string) string {
	if len(id) != 4 {
		return "?"
	}
	if _, err := strconv.Atoi(id[1:]); err != nil {
		return "?"
	}
	return id[:1]
}

func p015SafeDisposition(disposition string) bool {
	return strings.Contains(disposition, "_safe")
}

func p015RetiredDisposition(disposition string) bool {
	return strings.HasSuffix(disposition, "_retired") || disposition == "b1_retired"
}

func p015DispositionRank(disposition string) (int, bool) {
	switch disposition {
	case "b2a_legacy",
		"b2b_deferred",
		"b2c_logger_deferred",
		"b2c_console_deferred",
		"b4_excluded",
		"crash_artifact",
		"functional_allow":
		return 0, true
	case "b1_certified_safe",
		"b2a_safe",
		"b2b_safe",
		"b2c_logger_safe",
		"b2c_console_safe",
		"functional_retired":
		return 1, true
	case "b1_retired", "b2a_retired", "b2b_retired", "b2c_logger_retired", "b2c_console_retired":
		return 2, true
	default:
		return 0, false
	}
}

func p015HistorySourceImmutable(disposition string) bool {
	return p015SafeDisposition(disposition) ||
		p015RetiredDisposition(disposition) ||
		disposition == "b4_excluded" ||
		disposition == "crash_artifact" ||
		disposition == "functional_allow"
}

func p015PendingDisposition(disposition string) bool {
	return disposition == "b2a_legacy" ||
		disposition == "b2b_deferred" ||
		disposition == "b2c_logger_deferred" ||
		disposition == "b2c_console_deferred"
}

func p015LedgerHistoryIssues(
	previous []p015LoggingSite,
	current []p015LoggingSite,
) []string {
	currentByID := make(map[string]p015LoggingSite, len(current))
	for _, row := range current {
		currentByID[row.ID] = row
	}
	var issues []string
	for _, old := range previous {
		row, exists := currentByID[old.ID]
		if !exists {
			issues = append(
				issues,
				fmt.Sprintf("ledger row %s was deleted; retain it as a tombstone", old.ID),
			)
			continue
		}
		oldCohort, newCohort := p015SiteCohort(old.ID), p015SiteCohort(row.ID)
		if oldCohort != newCohort {
			issues = append(
				issues,
				fmt.Sprintf(
					"ledger row %s changed cohort from %s to %s",
					old.ID,
					oldCohort,
					newCohort,
				),
			)
		}
		oldRank, oldKnown := p015DispositionRank(old.Disposition)
		newRank, newKnown := p015DispositionRank(row.Disposition)
		if !oldKnown || !newKnown || newRank < oldRank {
			issues = append(
				issues,
				fmt.Sprintf(
					"ledger row %s disposition regressed from %s to %s",
					old.ID,
					old.Disposition,
					row.Disposition,
				),
			)
		}
		pendingToSafe := p015PendingDisposition(old.Disposition) &&
			p015SafeDisposition(row.Disposition)
		if pendingToSafe &&
			(old.File != row.File || old.Owner != row.Owner || old.Ordinal != row.Ordinal) {
			issues = append(
				issues,
				fmt.Sprintf(
					"ledger row %s pending-to-safe migration changed file/owner/ordinal",
					old.ID,
				),
			)
		}
		if !pendingToSafe && old.immutableSourceKey() != row.immutableSourceKey() {
			issues = append(
				issues,
				fmt.Sprintf("ledger row %s changed its immutable source tuple", old.ID),
			)
		}
	}
	return issues
}

type p015LoggingLedgerRevision struct {
	label string
	rows  []p015LoggingSite
}

func p015LedgerRevisionHistoryIssues(revisions []p015LoggingLedgerRevision) []string {
	var issues []string
	for index := 1; index < len(revisions); index++ {
		previous, current := revisions[index-1], revisions[index]
		for _, issue := range p015LedgerHistoryIssues(previous.rows, current.rows) {
			issues = append(
				issues,
				fmt.Sprintf("ledger history %s -> %s: %s", previous.label, current.label, issue),
			)
		}
	}
	return issues
}

func p015MigrateB2BRows(
	ledger []p015LoggingSite,
	sites []p015LoggingSite,
) ([]p015LoggingSite, error) {
	currentByIdentity := make(map[string]p015LoggingSite, len(p015B2BFiles))
	for _, site := range sites {
		if _, owned := p015B2BFiles[site.File]; !owned {
			continue
		}
		if site.Kind == "functional_fmt" || site.Kind == "functional_io" {
			continue
		}
		if site.Kind != "pico_safe" || !strings.HasSuffix(site.Callee, "SafeCF") ||
			site.Callee == "pico.DebugSensitiveCF" {
			return nil, fmt.Errorf(
				"P015b2b current source is not safe-only: %s",
				p015DescribeSite(site),
			)
		}
		key := p015MigrationIdentityKey(site)
		if previous, duplicate := currentByIdentity[key]; duplicate {
			return nil, fmt.Errorf(
				"P015b2b current source has duplicate migration identity: %s and %s",
				p015DescribeSite(previous),
				p015DescribeSite(site),
			)
		}
		currentByIdentity[key] = site
	}
	if len(currentByIdentity) != 132 {
		return nil, fmt.Errorf(
			"P015b2b current safe source has %d identities, want exact 132",
			len(currentByIdentity),
		)
	}

	migrated := append([]p015LoggingSite(nil), ledger...)
	matched := make(map[string]struct{}, len(currentByIdentity))
	bRows := 0
	for index, old := range migrated {
		if p015SiteCohort(old.ID) != "B" {
			continue
		}
		bRows++
		expectation, owned := p015B2BFiles[old.File]
		if !owned {
			return nil, fmt.Errorf("%s is outside the closed P015b2b file map", old.ID)
		}
		key := p015MigrationIdentityKey(old)
		current, exists := currentByIdentity[key]
		if !exists {
			return nil, fmt.Errorf(
				"%s has no current source with the same file/owner/ordinal",
				old.ID,
			)
		}
		if _, duplicate := matched[key]; duplicate {
			return nil, fmt.Errorf("current P015b2b source matched more than once: %s", old.ID)
		}
		matched[key] = struct{}{}
		migrated[index].Disposition = "b2b_safe"
		migrated[index].Kind = current.Kind
		migrated[index].Callee = current.Callee
		migrated[index].Call = current.Call
		migrated[index].Canary = expectation.canary
	}
	if bRows != 132 || len(matched) != len(currentByIdentity) {
		return nil, fmt.Errorf(
			"P015b2b migration matched %d of %d current identities from %d stable rows",
			len(matched),
			len(currentByIdentity),
			bRows,
		)
	}
	return migrated, nil
}

func p015MigrationIdentityKey(site p015LoggingSite) string {
	return strings.Join(
		[]string{site.File, site.Owner, strconv.Itoa(site.Ordinal)},
		"\x00",
	)
}

func p015LoggingLedgerRevisions(
	repoRoot string,
	currentPath string,
) ([]p015LoggingLedgerRevision, error) {
	relative, err := filepath.Rel(repoRoot, currentPath)
	if err != nil {
		return nil, err
	}
	relative = filepath.ToSlash(relative)
	logCommand := exec.Command("git", "log", "--format=%H", "--", relative)
	logCommand.Dir = repoRoot
	output, err := logCommand.Output()
	if err != nil {
		return nil, fmt.Errorf("read logging ledger git history: %w", err)
	}
	commits := strings.Fields(string(output))
	var revisions []p015LoggingLedgerRevision
	for index := len(commits) - 1; index >= 0; index-- {
		data, found := p015GitFile(repoRoot, commits[index], relative)
		if !found {
			return nil, fmt.Errorf("read logging ledger at revision %s", commits[index])
		}
		rows, parseErr := p015ParseLoggingLedger(data)
		if parseErr != nil {
			return nil, fmt.Errorf("parse logging ledger at revision %s: %w", commits[index], parseErr)
		}
		revisions = append(revisions, p015LoggingLedgerRevision{
			label: commits[index],
			rows:  rows,
		})
	}

	workingData, err := os.ReadFile(currentPath)
	if err != nil {
		return nil, fmt.Errorf("read working logging ledger: %w", err)
	}
	workingRows, err := p015ParseLoggingLedger(workingData)
	if err != nil {
		return nil, fmt.Errorf("parse working logging ledger: %w", err)
	}
	latestData := []byte(nil)
	if len(commits) != 0 {
		latestData, _ = p015GitFile(repoRoot, commits[0], relative)
	}
	if len(revisions) == 0 || string(latestData) != string(workingData) {
		revisions = append(revisions, p015LoggingLedgerRevision{
			label: "working-tree",
			rows:  workingRows,
		})
	}
	return revisions, nil
}

func p015GitFile(repoRoot, revision, relative string) ([]byte, bool) {
	command := exec.Command("git", "show", revision+":"+relative)
	command.Dir = repoRoot
	output, err := command.Output()
	return output, err == nil
}

func p015DescribeSite(site p015LoggingSite) string {
	return fmt.Sprintf(
		"%s %s owner=%s ordinal=%d kind=%s callee=%s call=%s",
		site.shortSourceKey(), site.File, site.Owner, site.Ordinal, site.Kind, site.Callee, site.Call,
	)
}

func p015RequireFixtureSite(t *testing.T, sites []p015LoggingSite, ownerSuffix, callee string) {
	t.Helper()
	for _, site := range sites {
		if strings.HasSuffix(site.Owner, ownerSuffix) && site.Callee == callee {
			return
		}
	}
	t.Errorf("fixture site owner suffix %q callee %q not found in %#v", ownerSuffix, callee, sites)
}

func p015FixtureCalls(
	sites []p015LoggingSite,
	ownerSuffix string,
	callee string,
) []string {
	var calls []string
	for _, site := range sites {
		if strings.HasSuffix(site.Owner, ownerSuffix) && site.Callee == callee {
			calls = append(calls, site.Call)
		}
	}
	sort.Strings(calls)
	return calls
}

func p015RequireEscape(t *testing.T, escapes []p015LoggingEscape, fragment string) {
	t.Helper()
	for _, escape := range escapes {
		if strings.Contains(escape.Reason, fragment) {
			return
		}
	}
	t.Errorf("escape containing %q not found in %#v", fragment, escapes)
}

func p015CountEscapes(
	escapes []p015LoggingEscape,
	reasonFragment string,
	ownerSuffix string,
) int {
	count := 0
	for _, escape := range escapes {
		if strings.Contains(escape.Reason, reasonFragment) &&
			strings.HasSuffix(escape.Owner, ownerSuffix) {
			count++
		}
	}
	return count
}

func p015CountExactEscapes(
	escapes []p015LoggingEscape,
	reason string,
	ownerSuffix string,
) int {
	count := 0
	for _, escape := range escapes {
		if escape.Reason == reason && strings.HasSuffix(escape.Owner, ownerSuffix) {
			count++
		}
	}
	return count
}

func p015ReviewedMethodValueIssues(used map[string]int) []string {
	var issues []string
	for key := range p015ReviewedNonLoggingMethodValues {
		if count := used[key]; count != 1 {
			issues = append(
				issues,
				fmt.Sprintf("reviewed method-value allowance %q used %d times, want 1", key, count),
			)
		}
	}
	for key, count := range used {
		if _, reviewed := p015ReviewedNonLoggingMethodValues[key]; !reviewed {
			issues = append(
				issues,
				fmt.Sprintf("unreviewed method-value key %q recorded %d uses", key, count),
			)
		}
	}
	sort.Strings(issues)
	return issues
}

func p015ClassifyBaseline(sites []p015LoggingSite) ([]p015LoggingSite, error) {
	rows := append([]p015LoggingSite(nil), sites...)
	sort.Slice(rows, func(left, right int) bool { return rows[left].sourceKey() < rows[right].sourceKey() })
	next := make(map[string]int)
	for index := range rows {
		cohort, disposition, canary, err := p015BaselineDisposition(rows[index])
		if err != nil {
			return nil, err
		}
		next[cohort]++
		rows[index].ID = fmt.Sprintf("%s%03d", cohort, next[cohort])
		rows[index].Disposition = disposition
		rows[index].Canary = canary
	}
	sort.Slice(rows, func(left, right int) bool { return rows[left].ID < rows[right].ID })
	return rows, nil
}

func p015BaselineDisposition(site p015LoggingSite) (string, string, string, error) {
	if site.Kind == "functional_fmt" || site.Kind == "functional_io" {
		return "F", "functional_allow", "-", nil
	}
	if site.Kind == "panic_artifact" {
		return "X", "crash_artifact", "-", nil
	}
	if site.File == "pkg/agent/runtime_event_logger.go" && site.Kind == "pico_legacy" {
		return "R", "b4_excluded", "-", nil
	}
	if site.File == "pkg/agent/hooks.go" || site.File == "pkg/agent/hook_process.go" {
		if site.Kind != "pico_safe" {
			return "", "", "", fmt.Errorf("hook site is not safe: %s", p015DescribeSite(site))
		}
		canary := "pkg/agent/p015_hook_safe_logging_test.go#TestP015HookDynamicFieldsAreObservedAndPolicyIndependent," +
			"pkg/agent/p015_hook_logging_structure_test.go#TestP015HookSafeLoggingASTManifest"
		if site.File == "pkg/agent/hook_process.go" {
			canary = "pkg/agent/p015_hook_safe_logging_test.go#TestP015HookProcessBytesNeverPreviewAcrossPolicies," +
				"pkg/agent/p015_hook_logging_structure_test.go#TestP015HookSafeLoggingASTManifest"
		}
		return "H", "b1_certified_safe", canary, nil
	}
	if _, core := p015CoreAgentFiles[site.File]; core {
		switch site.Kind {
		case "pico_legacy":
			return "A", "b2a_legacy", "-", nil
		case "pico_safe":
			return "A", "b2a_safe", p015B2ACanaryForFile(site.File), nil
		}
	}
	if strings.HasPrefix(site.File, "pkg/agent/") && site.Kind == "pico_legacy" {
		return "B", "b2b_deferred", "-", nil
	}
	if strings.HasPrefix(site.File, "pkg/gateway/") && site.Kind == "pico_legacy" {
		return "G", "b2c_logger_deferred", "-", nil
	}
	if strings.HasPrefix(site.File, "pkg/gateway/") && site.Kind == "console" {
		return "C", "b2c_console_deferred", "-", nil
	}
	return "", "", "", fmt.Errorf("unclassified site: %s", p015DescribeSite(site))
}

func p015B2ACanaryForFile(file string) string {
	if _, applicationOwned := p015B2AApplicationFiles[file]; applicationOwned {
		return "pkg/agent/p015b2a_application_logging_test.go#TestP015B2AApplicationLoggingASTManifest"
	}
	return "pkg/agent/p015b2a_core_logging_test.go#TestP015B2ACoreLoggingASTManifest," +
		"pkg/agent/diagnostic_fields_test.go#TestAgentDiagnosticIdentityHelpersAreSealedAndDistinct," +
		"pkg/agent/diagnostic_fields_test.go#TestAgentDiagnosticErrorPanicAndPathHelpersInvokeNoMethods"
}
