package main

import (
	"crypto/sha256"
	_ "embed"
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
	p015LoggingLedgerPath       = "scripts/testdata/p015b2_logging_inventory.tsv"
	p015TombstoneGoldenPath     = "scripts/testdata/p015b2_logging_tombstones.tsv"
	p015B2BSignaturePath        = "scripts/testdata/p015b2b_safe_signatures.tsv"
	p015B2CLoggerSignaturePath  = "scripts/testdata/p015b2c_logger_safe_signatures.tsv"
	p015B2CConsoleSignaturePath = "scripts/testdata/p015b2c_console_safe_signatures.tsv"
	p015B3ATransitionPath       = "scripts/testdata/p015b3a_logging_transitions.tsv"
	p015ModulePath              = "github.com/sipeed/picoclaw"
)

//go:embed testdata/p015b3a_logging_transitions.tsv
var p015B3AReviewedTransitionData []byte

var p015B3AExpectedTransitionIDs = []string{
	"A032", "A033", "A034", "A035",
	"A110", "A111", "A112", "A113", "A114", "A115", "A116",
	"B057", "B058", "B059", "B092", "B093",
}

type p015ReviewedLoggingTransition struct {
	ID       string
	FromHash string
	ToHash   string
}

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
		"func:ObservationFields", "func:ObserveBytes", "func:ObserveConfigPath",
		"func:ObserveErrorType", "func:ObserveHomePath",
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
	"F": 24,
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
	"F": 16,
}

var p015ExpectedPreB2CStageCounts = map[string]int{
	"A|b2a_safe|pico_safe":              112,
	"A|b2a_retired|retired":             4,
	"B|b2b_safe|pico_safe":              132,
	"H|b1_certified_safe|pico_safe":     24,
	"G|b2c_logger_deferred|pico_legacy": 54,
	"C|b2c_console_deferred|console":    23,
	"R|b4_excluded|pico_legacy":         7,
	"X|crash_artifact|panic_artifact":   1,
	"F|functional_allow|functional_fmt": 13,
	"F|functional_allow|functional_io":  3,
	"F|functional_retired|retired":      8,
}

var p015ExpectedFinalStageCounts = map[string]int{
	"A|b2a_safe|pico_safe":              112,
	"A|b2a_retired|retired":             4,
	"B|b2b_safe|pico_safe":              132,
	"H|b1_certified_safe|pico_safe":     24,
	"G|b2c_logger_safe|pico_safe":       54,
	"C|b2c_console_safe|console_safe":   23,
	"R|b4_excluded|pico_legacy":         7,
	"X|crash_artifact|panic_artifact":   1,
	"F|functional_allow|functional_fmt": 13,
	"F|functional_allow|functional_io":  3,
	"F|functional_retired|retired":      8,
}

type p015B2CGroupExpectation struct {
	canary string
}

const (
	p015B2CAutomationCanary = "pkg/gateway/p015b2c_automation_logging_test.go#" +
		"TestP015B2CAutomationLoggingASTManifest"
	p015B2CStartupCanary = "pkg/gateway/p015b2c_startup_logging_test.go#" +
		"TestP015B2CStartupLoggingASTManifest"
	p015B2CReloadCanary = "pkg/gateway/p015b2c_reload_logging_test.go#" +
		"TestP015B2CReloadLoggingASTManifest"
	p015B2CShutdownCanary = "pkg/gateway/p015b2c_shutdown_logging_test.go#" +
		"TestP015B2CShutdownLoggingASTManifest"
	p015B2CConsoleCatalogCanary = "pkg/gateway/gateway_console_test.go#" +
		"TestGatewayConsoleCatalogHasNoRawInputSurface"
	p015B2CConsoleLifecycleCanary = "pkg/gateway/p015b2c_console_lifecycle_test.go#" +
		"TestP015B2CConsoleLifecycleAndCardinalityManifest"
)

func p015B2CCompleteConsoleCanary(groupCanary string) string {
	return groupCanary + "," + p015B2CConsoleCatalogCanary + "," +
		p015B2CConsoleLifecycleCanary
}

var p015B2CGroupByID = func() map[string]p015B2CGroupExpectation {
	result := make(map[string]p015B2CGroupExpectation, 77)
	add := func(canary string, ids ...string) {
		for _, id := range ids {
			if _, duplicate := result[id]; duplicate {
				panic("duplicate P015b2c group ID " + id)
			}
			result[id] = p015B2CGroupExpectation{canary: canary}
		}
	}
	add(p015B2CAutomationCanary,
		"G001", "G002", "G003", "G004", "G005", "G006", "G007", "G008", "G054")
	add(p015B2CStartupCanary,
		"G009", "G017", "G019", "G020", "G021", "G022", "G023", "G035", "G039", "G040",
		"C001", "C002", "C003", "C004", "C005", "C006", "C007",
		"C016", "C017", "C018", "C019", "C020", "C021", "C022", "C023")
	add(p015B2CReloadCanary,
		"G011", "G012", "G013", "G014", "G015", "G016", "G018",
		"G024", "G025", "G026", "G027", "G028", "G029", "G030", "G031", "G032", "G033", "G034",
		"G036", "G037", "G038", "G041", "G042", "G043", "G044", "G045", "G046", "G047",
		"C008", "C009", "C010", "C011", "C012", "C013", "C014", "C015")
	add(p015B2CShutdownCanary,
		"G010", "G048", "G049", "G050", "G051", "G052", "G053")
	return result
}()

var p015B2CLoggerLevelByID = func() map[string]string {
	result := make(map[string]string, 54)
	add := func(level string, ids ...string) {
		for _, id := range ids {
			if _, duplicate := result[id]; duplicate {
				panic("duplicate P015b2c logger level ID " + id)
			}
			result[id] = level
		}
	}
	add("Debug", "G006", "G008", "G041")
	add("Info",
		"G009", "G010", "G013", "G018", "G019", "G021", "G024", "G025", "G026",
		"G027", "G029", "G033", "G035", "G037", "G038", "G039", "G046", "G053")
	add("Warn",
		"G001", "G002", "G003", "G004", "G005", "G007", "G011", "G020", "G023",
		"G031", "G036", "G043", "G045", "G047")
	add("Error",
		"G012", "G014", "G015", "G016", "G022", "G028", "G030", "G032", "G034",
		"G040", "G042", "G044", "G048", "G049", "G050", "G051", "G052", "G054")
	add("Fatal", "G017")
	return result
}()

var p015B2CLoggerFileCounts = map[string]int{
	"pkg/gateway/event_automation.go":            8,
	"pkg/gateway/gateway.go":                     45,
	"pkg/gateway/pr_workspace_implementation.go": 1,
}

var p015B2CLoggerOwnerCounts = map[string]int{
	p015ModulePath + "/pkg/gateway.newEventAutomationServiceWithRuntime":             2,
	p015ModulePath + "/pkg/gateway.runEventAutomationWorker":                         1,
	p015ModulePath + "/pkg/gateway.runEventRetentionWorker.$lit1":                    3,
	p015ModulePath + "/pkg/gateway.runGitHubNotificationPollWorker.$lit1":            2,
	p015ModulePath + "/pkg/gateway.Run":                                              13,
	p015ModulePath + "/pkg/gateway.Run.$lit1":                                        1,
	p015ModulePath + "/pkg/gateway.createStartupProvider":                            1,
	p015ModulePath + "/pkg/gateway.handleConfigReloadWithServiceOps":                 11,
	p015ModulePath + "/pkg/gateway.logChannelVoiceCapabilities":                      1,
	p015ModulePath + "/pkg/gateway.restartServices":                                  3,
	p015ModulePath + "/pkg/gateway.setupAndStartServices":                            2,
	p015ModulePath + "/pkg/gateway.setupConfigWatcherPolling.$lit1":                  7,
	p015ModulePath + "/pkg/gateway.shutdownGateway":                                  6,
	p015ModulePath + "/pkg/gateway.(*prWorkspaceImplementationRuntime).Repair.$lit1": 1,
}

var p015B2CConsoleOwnerCounts = map[string]int{
	p015ModulePath + "/pkg/gateway.Run":                   6,
	p015ModulePath + "/pkg/gateway.createStartupProvider": 1,
	p015ModulePath + "/pkg/gateway.restartServices":       8,
	p015ModulePath + "/pkg/gateway.setupAndStartServices": 8,
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
	if len(p015ReviewedNonLoggingMethodValues) != 57 {
		t.Fatalf(
			"reviewed unresolved method-value allowances = %d, want exact 57: %#v",
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
}

func TestP015B2ClosedGatewayConsoleScannerShapeRejectsMutations(t *testing.T) {
	if got := len(p015GatewayConsoleCallShapes); got != 23 {
		t.Fatalf("closed Gateway console scanner has %d site shapes, want exact 23", got)
	}
	repoRoot := t.TempDir()
	gatewayDir := filepath.Join(repoRoot, "pkg", "gateway")
	if err := os.MkdirAll(gatewayDir, 0o755); err != nil {
		t.Fatal(err)
	}
	source := `package gateway

import "fmt"

func valid() {
	fmt.Print(renderGatewayConsole(gatewayConsoleC001GatewayStarted, newGatewayConsolePort(8080)))
}

func rawLiteral() { fmt.Print("raw") }
func printFormat() { fmt.Printf("%s", renderGatewayConsole(gatewayConsoleC002StopHint, newGatewayConsoleNoFields())) }
func printLine() { fmt.Println(renderGatewayConsole(gatewayConsoleC002StopHint, newGatewayConsoleNoFields())) }
func outerMultiple() { fmt.Print(renderGatewayConsole(gatewayConsoleC002StopHint, newGatewayConsoleNoFields()), "raw") }
func outerEllipsis(values []any) { fmt.Print(values...) }
func dynamicSite(site gatewayConsoleSiteID) { fmt.Print(renderGatewayConsole(site, newGatewayConsoleNoFields())) }
func unknownSite() { fmt.Print(renderGatewayConsole(gatewayConsoleUnknown, newGatewayConsoleNoFields())) }
func wrongConstructor() { fmt.Print(renderGatewayConsole(gatewayConsoleC001GatewayStarted, newGatewayConsoleCount(8080))) }
func fieldsVariable() {
	fields := newGatewayConsolePort(8080)
	fmt.Print(renderGatewayConsole(gatewayConsoleC001GatewayStarted, fields))
}
func unknownFields() { fmt.Print(renderGatewayConsole(gatewayConsoleC001GatewayStarted, futureConsoleFields(8080))) }
func rendererAlias() {
	render := renderGatewayConsole
	fmt.Print(render(gatewayConsoleC001GatewayStarted, newGatewayConsolePort(8080)))
}
func rendererMethod(console any) { fmt.Print(console.render(gatewayConsoleC001GatewayStarted, newGatewayConsolePort(8080))) }
func rendererWrongArity() { fmt.Print(renderGatewayConsole(gatewayConsoleC001GatewayStarted)) }
func rendererEllipsis(values []any) { fmt.Print(renderGatewayConsole(values...)) }
func localShadow() {
	renderGatewayConsole := func(any, any) string { return "raw" }
	fmt.Print(renderGatewayConsole(gatewayConsoleC001GatewayStarted, newGatewayConsolePort(8080)))
}
`
	if err := os.WriteFile(filepath.Join(gatewayDir, "gateway.go"), []byte(source), 0o644); err != nil {
		t.Fatal(err)
	}
	other := `package gateway

import "fmt"

func wrongFile() {
	fmt.Print(renderGatewayConsole(gatewayConsoleC001GatewayStarted, newGatewayConsolePort(8080)))
}
`
	if err := os.WriteFile(filepath.Join(gatewayDir, "other.go"), []byte(other), 0o644); err != nil {
		t.Fatal(err)
	}

	scan, err := p015ScanLogging(p015LoggingScanOptions{
		RepoRoot: repoRoot, ModulePath: p015ModulePath, Roots: []string{"pkg/gateway"},
	})
	if err != nil {
		t.Fatal(err)
	}
	wantKinds := map[string]string{
		"valid":      "console_safe",
		"rawLiteral": "console", "printFormat": "console", "printLine": "console",
		"outerMultiple": "console", "outerEllipsis": "console", "dynamicSite": "console",
		"unknownSite": "console", "wrongConstructor": "console", "fieldsVariable": "console",
		"unknownFields": "console", "rendererAlias": "console", "rendererMethod": "console",
		"rendererWrongArity": "console", "rendererEllipsis": "console", "localShadow": "console",
		"wrongFile": "console",
	}
	seen := make(map[string]struct{}, len(wantKinds))
	for _, site := range scan.Sites {
		owner := site.Owner[strings.LastIndex(site.Owner, ".")+1:]
		want, expected := wantKinds[owner]
		if !expected {
			continue
		}
		seen[owner] = struct{}{}
		if site.Kind != want {
			t.Errorf("%s classified as %s, want %s: %s", owner, site.Kind, want, site.Call)
		}
		if want == "console_safe" && site.Callee != "fmt.Print" {
			t.Errorf("valid closed console callee = %s, want fmt.Print", site.Callee)
		}
	}
	for owner := range wantKinds {
		if _, found := seen[owner]; !found {
			t.Errorf("console shape fixture %s was not inventoried", owner)
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

func TestP015B3AReviewedLoggingTransitionsClosed(t *testing.T) {
	transitions, err := p015ParseReviewedLoggingTransitions(p015B3AReviewedTransitionData)
	if err != nil {
		t.Fatal(err)
	}
	repoRoot := p015FindRepoRoot(t)
	diskData, err := os.ReadFile(
		filepath.Join(repoRoot, filepath.FromSlash(p015B3ATransitionPath)),
	)
	if err != nil {
		t.Fatal(err)
	}
	if string(diskData) != string(p015B3AReviewedTransitionData) {
		t.Fatal("embedded P015b3a transition manifest differs from its repository file")
	}
	ledger := p015ReadLoggingLedger(
		t,
		filepath.Join(repoRoot, filepath.FromSlash(p015LoggingLedgerPath)),
	)
	ledgerByID := make(map[string]p015LoggingSite, len(ledger))
	for _, row := range ledger {
		ledgerByID[row.ID] = row
	}
	for _, transition := range transitions {
		row, exists := ledgerByID[transition.ID]
		if !exists {
			t.Errorf("reviewed transition target %s is missing from the ledger", transition.ID)
			continue
		}
		if got := p015LoggingTransitionHash(row); got != transition.ToHash {
			t.Errorf(
				"reviewed transition target %s hash = %s, want %s",
				transition.ID,
				got,
				transition.ToHash,
			)
		}
	}
}

func TestP015B3AReviewedLoggingTransitionManifestRejectsMutations(t *testing.T) {
	lines := strings.Split(strings.TrimSuffix(string(p015B3AReviewedTransitionData), "\n"), "\n")
	firstDataLine := 3
	if len(lines) != firstDataLine+len(p015B3AExpectedTransitionIDs) {
		t.Fatalf("unexpected reviewed transition fixture shape: %d lines", len(lines))
	}
	mutations := map[string]func([]string) []string{
		"missing": func(candidate []string) []string {
			return candidate[:len(candidate)-1]
		},
		"duplicate": func(candidate []string) []string {
			return append(candidate, candidate[len(candidate)-1])
		},
		"unsorted": func(candidate []string) []string {
			first := candidate[firstDataLine]
			candidate[firstDataLine] = candidate[firstDataLine+1]
			candidate[firstDataLine+1] = first
			return candidate
		},
		"unknown_id": func(candidate []string) []string {
			candidate[firstDataLine] = strings.Replace(
				candidate[firstDataLine], "A032\t", "A031\t", 1,
			)
			return candidate
		},
		"uppercase_hash": func(candidate []string) []string {
			fields := strings.Split(candidate[firstDataLine], "\t")
			fields[1] = strings.ToUpper(fields[1])
			candidate[firstDataLine] = strings.Join(fields, "\t")
			return candidate
		},
		"short_hash": func(candidate []string) []string {
			fields := strings.Split(candidate[firstDataLine], "\t")
			fields[1] = fields[1][:len(fields[1])-1]
			candidate[firstDataLine] = strings.Join(fields, "\t")
			return candidate
		},
		"same_hash": func(candidate []string) []string {
			fields := strings.Split(candidate[firstDataLine], "\t")
			fields[2] = fields[1]
			candidate[firstDataLine] = strings.Join(fields, "\t")
			return candidate
		},
		"extra_field": func(candidate []string) []string {
			candidate[firstDataLine] += "\textra"
			return candidate
		},
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			candidate := append([]string(nil), lines...)
			candidate = mutate(candidate)
			if _, err := p015ParseReviewedLoggingTransitions(
				[]byte(strings.Join(candidate, "\n") + "\n"),
			); err == nil {
				t.Fatal("mutated reviewed transition manifest was accepted")
			}
		})
	}
}

func TestP015B3AReviewedLoggingTransitionRejectsUnreviewedChanges(t *testing.T) {
	old := p015LoggingSite{
		ID:          "A032",
		Disposition: "b2a_safe",
		File:        "pkg/agent/context.go",
		Owner:       p015ModulePath + "/pkg/agent.(*ContextBuilder).BuildMessagesFromPrompt",
		Ordinal:     1,
		Kind:        "pico_safe",
		Callee:      "pico.WarnSafeCF",
		Call:        `logger.WarnSafeCF(component, message, fields)`,
		Canary:      "pkg/agent/p015b2a_application_logging_test.go#TestP015B2AApplicationLoggingASTManifest",
	}
	target := old
	target.Owner = p015ModulePath +
		"/pkg/agent.(*ContextBuilder).buildMessagesFromPromptWithDiagnosticPolicy"
	transition := p015ReviewedLoggingTransition{
		ID:       old.ID,
		FromHash: p015LoggingTransitionHash(old),
		ToHash:   p015LoggingTransitionHash(target),
	}
	transitions := map[string]p015ReviewedLoggingTransition{old.ID: transition}
	if issues := p015LedgerHistoryIssuesWithTransitions(
		[]p015LoggingSite{old},
		[]p015LoggingSite{target},
		transitions,
	); len(issues) != 0 {
		t.Fatalf("exact reviewed transition rejected: %v", issues)
	}

	mutations := map[string]func(*p015LoggingSite){
		"id":          func(row *p015LoggingSite) { row.ID = "A033" },
		"disposition": func(row *p015LoggingSite) { row.Disposition = "b2a_retired" },
		"file":        func(row *p015LoggingSite) { row.File = "pkg/agent/other.go" },
		"owner":       func(row *p015LoggingSite) { row.Owner += ".other" },
		"ordinal":     func(row *p015LoggingSite) { row.Ordinal++ },
		"kind":        func(row *p015LoggingSite) { row.Kind = "retired" },
		"callee":      func(row *p015LoggingSite) { row.Callee = "pico.ErrorSafeCF" },
		"call":        func(row *p015LoggingSite) { row.Call += " /* mutation */" },
		"canary":      func(row *p015LoggingSite) { row.Canary += ",other_test.go#TestOther" },
	}
	for name, mutate := range mutations {
		t.Run("target_"+name, func(t *testing.T) {
			candidate := target
			mutate(&candidate)
			if issues := p015LedgerHistoryIssuesWithTransitions(
				[]p015LoggingSite{old},
				[]p015LoggingSite{candidate},
				transitions,
			); len(issues) == 0 {
				t.Fatal("unreviewed target mutation was accepted")
			}
		})
		t.Run("preimage_"+name, func(t *testing.T) {
			candidate := old
			mutate(&candidate)
			if issues := p015LedgerHistoryIssuesWithTransitions(
				[]p015LoggingSite{candidate},
				[]p015LoggingSite{target},
				transitions,
			); len(issues) == 0 {
				t.Fatal("unreviewed preimage mutation was accepted")
			}
		})
		t.Run("after_target_"+name, func(t *testing.T) {
			candidate := target
			mutate(&candidate)
			if issues := p015LedgerHistoryIssuesWithTransitions(
				[]p015LoggingSite{target},
				[]p015LoggingSite{candidate},
				transitions,
			); len(issues) == 0 {
				t.Fatal("post-transition mutation was accepted")
			}
		})
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

func TestP015B2CFinalMigrationMappingClosed(t *testing.T) {
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
	if !p015B2CCurrentSourceConverged(scan.Sites) {
		t.Skip("P015b2c production source has not converged")
	}
	migrated, err := p015MigrateB2CRows(ledger, scan.Sites)
	if err != nil {
		t.Fatal(err)
	}
	for index, old := range ledger {
		got := migrated[index]
		switch p015SiteCohort(old.ID) {
		case "G":
			if got.ID != old.ID || got.File != old.File || got.Owner != old.Owner ||
				got.Ordinal != old.Ordinal {
				t.Errorf("%s logger migration did not preserve ID/file/owner/ordinal", old.ID)
			}
			if got.Disposition != "b2c_logger_safe" || got.Kind != "pico_safe" ||
				!strings.HasSuffix(got.Callee, "SafeCF") || got.Callee == "pico.DebugSensitiveCF" {
				t.Errorf("%s logger migration target is not direct safe-only: %#v", old.ID, got)
			}
		case "C":
			if got.ID != old.ID || got.File != old.File || got.Owner != old.Owner ||
				got.Ordinal != old.Ordinal {
				t.Errorf("%s console migration did not preserve ID/file/owner/ordinal", old.ID)
			}
			if got.Disposition != "b2c_console_safe" || got.Kind != "console_safe" ||
				got.Callee != "fmt.Print" {
				t.Errorf("%s console migration target is not closed console-only: %#v", old.ID, got)
			}
		default:
			if got != old {
				t.Errorf("migration changed unrelated row %s", old.ID)
			}
		}
	}
}

func TestP015B2CFinalMigrationRejectsIdentityAndShapeDrift(t *testing.T) {
	repoRoot := p015FindRepoRoot(t)
	ledger := p015ReadLoggingLedger(
		t,
		filepath.Join(repoRoot, filepath.FromSlash(p015LoggingLedgerPath)),
	)
	scan, err := p015ScanLogging(p015LoggingScanOptions{
		RepoRoot: repoRoot, ModulePath: p015ModulePath, Roots: []string{"pkg/agent", "pkg/gateway"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !p015B2CCurrentSourceConverged(scan.Sites) {
		t.Skip("P015b2c production source has not converged")
	}
	loggerIndex, consoleIndex := -1, -1
	for index, site := range scan.Sites {
		if loggerIndex < 0 && site.Kind == "pico_safe" {
			if _, owned := p015B2CLoggerFileCounts[site.File]; owned {
				loggerIndex = index
			}
		}
		if consoleIndex < 0 && site.Kind == "console_safe" {
			consoleIndex = index
		}
	}
	if loggerIndex < 0 || consoleIndex < 0 {
		t.Fatalf("converged source lacks mutation fixtures: logger=%d console=%d", loggerIndex, consoleIndex)
	}

	testCases := map[string]func([]p015LoggingSite) []p015LoggingSite{
		"missing_logger": func(sites []p015LoggingSite) []p015LoggingSite {
			return append(sites[:loggerIndex:loggerIndex], sites[loggerIndex+1:]...)
		},
		"duplicate_logger_identity": func(sites []p015LoggingSite) []p015LoggingSite {
			return append(sites, sites[loggerIndex])
		},
		"sensitive_logger": func(sites []p015LoggingSite) []p015LoggingSite {
			sites[loggerIndex].Callee = "pico.DebugSensitiveCF"
			return sites
		},
		"wrong_logger_level": func(sites []p015LoggingSite) []p015LoggingSite {
			if level, _ := p015LoggerLevel(sites[loggerIndex].Callee); level == "Fatal" {
				sites[loggerIndex].Callee = "pico.DebugSafeCF"
			} else {
				sites[loggerIndex].Callee = "pico.FatalSafeCF"
			}
			return sites
		},
		"moved_logger_owner": func(sites []p015LoggingSite) []p015LoggingSite {
			sites[loggerIndex].Owner += ".moved"
			return sites
		},
		"missing_console": func(sites []p015LoggingSite) []p015LoggingSite {
			return append(sites[:consoleIndex:consoleIndex], sites[consoleIndex+1:]...)
		},
		"raw_console": func(sites []p015LoggingSite) []p015LoggingSite {
			sites[consoleIndex].Kind = "console"
			return sites
		},
		"printf_console": func(sites []p015LoggingSite) []p015LoggingSite {
			sites[consoleIndex].Callee = "fmt.Printf"
			return sites
		},
		"moved_console_owner": func(sites []p015LoggingSite) []p015LoggingSite {
			sites[consoleIndex].Owner += ".moved"
			return sites
		},
	}
	for name, mutate := range testCases {
		t.Run(name, func(t *testing.T) {
			candidate := append([]p015LoggingSite(nil), scan.Sites...)
			candidate = mutate(candidate)
			if _, migrateErr := p015MigrateB2CRows(ledger, candidate); migrateErr == nil {
				t.Fatal("migration drift was accepted")
			}
		})
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

func TestP015B2CReviewedLoggerSafeSignatures(t *testing.T) {
	p015RequireB2CReviewedSignatures(
		t,
		"G",
		p015B2CLoggerSignaturePath,
		54,
		func(row p015LoggingSite) bool {
			return row.Disposition == "b2c_logger_safe" && row.Kind == "pico_safe" &&
				strings.HasSuffix(row.Callee, "SafeCF") && row.Callee != "pico.DebugSensitiveCF"
		},
	)
}

func TestP015B2CReviewedConsoleSafeSignatures(t *testing.T) {
	p015RequireB2CReviewedSignatures(
		t,
		"C",
		p015B2CConsoleSignaturePath,
		23,
		func(row p015LoggingSite) bool {
			return row.Disposition == "b2c_console_safe" && row.Kind == "console_safe" &&
				row.Callee == "fmt.Print"
		},
	)
}

func p015RequireB2CReviewedSignatures(
	t *testing.T,
	cohort string,
	path string,
	wantCount int,
	validTarget func(p015LoggingSite) bool,
) {
	t.Helper()
	repoRoot := p015FindRepoRoot(t)
	ledger := p015ReadLoggingLedger(
		t,
		filepath.Join(repoRoot, filepath.FromSlash(p015LoggingLedgerPath)),
	)
	if !p015B2CFinalized(ledger) {
		t.Skip("P015b2c production source and ledger have not converged")
	}
	signatures := p015ReadB2BReviewedSignatures(
		t,
		filepath.Join(repoRoot, filepath.FromSlash(path)),
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

	seen := make(map[string]struct{}, wantCount)
	for _, row := range ledger {
		if p015SiteCohort(row.ID) != cohort {
			continue
		}
		if !validTarget(row) {
			t.Errorf("%s reviewed target has invalid final shape: %#v", row.ID, row)
		}
		signature, ok := signatures[row.ID]
		if !ok {
			t.Errorf("reviewed %s signature is missing %s", cohort, row.ID)
			continue
		}
		seen[row.ID] = struct{}{}
		if row.Callee != signature.Callee || row.Call != signature.Call {
			t.Errorf(
				"%s safe call differs from the reviewed signature\ncallee: %s\nwant:   %s\ncall:\n%s\nwant:\n%s",
				row.ID, row.Callee, signature.Callee, row.Call, signature.Call,
			)
		}
		if _, exists := current[row.sourceKey()]; !exists {
			t.Errorf("%s reviewed safe signature is absent from current source", row.ID)
		}
	}
	for index := 1; index <= wantCount; index++ {
		id := fmt.Sprintf("%s%03d", cohort, index)
		if _, ok := signatures[id]; !ok {
			t.Errorf("reviewed signature golden is missing %s", id)
		}
		if _, ok := seen[id]; !ok {
			t.Errorf("current ledger did not consume reviewed signature %s", id)
		}
	}
	for id := range signatures {
		if p015SiteCohort(id) != cohort {
			t.Errorf("%s signature golden contains cross-cohort ID %s", cohort, id)
		}
	}
	if got := len(signatures); got != wantCount {
		t.Errorf("reviewed %s signature count = %d, want %d", cohort, got, wantCount)
	}
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

func TestP015B2RewriteB2CLoggingInventory(t *testing.T) {
	if os.Getenv("P015B2_REWRITE_B2C_INVENTORY") != "1" {
		t.Skip("set P015B2_REWRITE_B2C_INVENTORY=1 to apply the closed G/C migration")
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
	if !p015B2CCurrentSourceConverged(scan.Sites) {
		t.Fatal("refusing to rewrite before all 54 logger and 23 console source identities converge")
	}
	migrated, err := p015MigrateB2CRows(ledger, scan.Sites)
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

func TestP015B2CExactPendingTransitionsAndCertifiedRowsAreImmutable(t *testing.T) {
	testCases := []struct {
		name string
		old  p015LoggingSite
		safe p015LoggingSite
	}{
		{
			name: "logger",
			old: p015LoggingSite{
				ID:          "G001",
				Disposition: "b2c_logger_deferred",
				File:        "pkg/gateway/event_automation.go",
				Owner:       p015ModulePath + "/pkg/gateway.newEventAutomationServiceWithRuntime",
				Ordinal:     1,
				Kind:        "pico_legacy",
				Callee:      "pico.WarnCF",
				Call:        `logger.WarnCF("eventing", "PR workspace local CI is unavailable", fields)`,
				Canary:      "-",
			},
			safe: p015LoggingSite{
				ID:          "G001",
				Disposition: "b2c_logger_safe",
				File:        "pkg/gateway/event_automation.go",
				Owner:       p015ModulePath + "/pkg/gateway.newEventAutomationServiceWithRuntime",
				Ordinal:     1,
				Kind:        "pico_safe",
				Callee:      "pico.WarnSafeCF",
				Call: `logger.WarnSafeCF(logger.ComponentEventing, ` +
					`logger.DiagnosticMessageEvent, logger.NewSafeFields())`,
				Canary: p015B2CAutomationCanary,
			},
		},
		{
			name: "console",
			old: p015LoggingSite{
				ID:          "C001",
				Disposition: "b2c_console_deferred",
				File:        "pkg/gateway/gateway.go",
				Owner:       p015ModulePath + "/pkg/gateway.Run",
				Ordinal:     10,
				Kind:        "console",
				Callee:      "fmt.Printf",
				Call:        `fmt.Printf("✓ Gateway started on %s\n", address)`,
				Canary:      "-",
			},
			safe: p015LoggingSite{
				ID:          "C001",
				Disposition: "b2c_console_safe",
				File:        "pkg/gateway/gateway.go",
				Owner:       p015ModulePath + "/pkg/gateway.Run",
				Ordinal:     10,
				Kind:        "console_safe",
				Callee:      "fmt.Print",
				Call: `fmt.Print(renderGatewayConsole(` +
					`gatewayConsoleC001GatewayStarted, newGatewayConsolePort(port)))`,
				Canary: p015B2CCompleteConsoleCanary(p015B2CStartupCanary),
			},
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			if issues := p015LedgerHistoryIssues(
				[]p015LoggingSite{testCase.old}, []p015LoggingSite{testCase.safe},
			); len(issues) != 0 {
				t.Fatalf("exact pending-to-safe transition rejected: %v", issues)
			}

			mutations := map[string]func(*p015LoggingSite){
				"file":        func(row *p015LoggingSite) { row.File = "pkg/gateway/other.go" },
				"owner":       func(row *p015LoggingSite) { row.Owner += ".other" },
				"ordinal":     func(row *p015LoggingSite) { row.Ordinal++ },
				"disposition": func(row *p015LoggingSite) { row.Disposition = "b2c_logger_retired" },
				"kind":        func(row *p015LoggingSite) { row.Kind = "retired" },
				"callee":      func(row *p015LoggingSite) { row.Callee = "pico.ErrorSafeCF" },
				"canary":      func(row *p015LoggingSite) { row.Canary += ",other_test.go#TestOther" },
			}
			for name, mutate := range mutations {
				t.Run("transition_rejects_"+name, func(t *testing.T) {
					candidate := testCase.safe
					mutate(&candidate)
					if issues := p015LedgerHistoryIssues(
						[]p015LoggingSite{testCase.old}, []p015LoggingSite{candidate},
					); len(issues) == 0 {
						t.Fatalf("pending transition %s mutation was accepted", name)
					}
				})
			}

			for name, mutate := range map[string]func(*p015LoggingSite){
				"kind":   func(row *p015LoggingSite) { row.Kind += "_changed" },
				"call":   func(row *p015LoggingSite) { row.Call += " /* changed */" },
				"canary": func(row *p015LoggingSite) { row.Canary += ",other_test.go#TestOther" },
			} {
				t.Run("certified_rejects_"+name, func(t *testing.T) {
					candidate := testCase.safe
					mutate(&candidate)
					if issues := p015LedgerHistoryIssues(
						[]p015LoggingSite{testCase.safe}, []p015LoggingSite{candidate},
					); len(issues) == 0 {
						t.Fatalf("certified row %s mutation was accepted", name)
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

func p015LoggingTransitionHash(row p015LoggingSite) string {
	sum := sha256.Sum256([]byte(p015FormatLedgerRow(row)))
	return hex.EncodeToString(sum[:])
}

func p015ParseReviewedLoggingTransitions(
	data []byte,
) ([]p015ReviewedLoggingTransition, error) {
	var transitions []p015ReviewedLoggingTransition
	for index, rawLine := range strings.Split(string(data), "\n") {
		line := strings.TrimSpace(rawLine)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.Split(rawLine, "\t")
		if len(fields) != 3 {
			return nil, fmt.Errorf(
				"reviewed transition line %d has %d fields, want 3",
				index+1,
				len(fields),
			)
		}
		transition := p015ReviewedLoggingTransition{
			ID:       fields[0],
			FromHash: fields[1],
			ToHash:   fields[2],
		}
		for label, hash := range map[string]string{
			"from": transition.FromHash,
			"to":   transition.ToHash,
		} {
			decoded, err := hex.DecodeString(hash)
			if err != nil || len(decoded) != sha256.Size || strings.ToLower(hash) != hash {
				return nil, fmt.Errorf(
					"reviewed transition line %d has invalid lowercase SHA-256 %s hash %q",
					index+1,
					label,
					hash,
				)
			}
		}
		if transition.FromHash == transition.ToHash {
			return nil, fmt.Errorf(
				"reviewed transition line %d has identical from/to hashes",
				index+1,
			)
		}
		transitions = append(transitions, transition)
	}
	if len(transitions) != len(p015B3AExpectedTransitionIDs) {
		return nil, fmt.Errorf(
			"reviewed transition count = %d, want exact %d",
			len(transitions),
			len(p015B3AExpectedTransitionIDs),
		)
	}
	for index, wantID := range p015B3AExpectedTransitionIDs {
		if gotID := transitions[index].ID; gotID != wantID {
			return nil, fmt.Errorf(
				"reviewed transition ID %d = %q, want exact sorted ID %q",
				index+1,
				gotID,
				wantID,
			)
		}
	}
	return transitions, nil
}

func p015ReviewedLoggingTransitionMap(
	transitions []p015ReviewedLoggingTransition,
) map[string]p015ReviewedLoggingTransition {
	result := make(map[string]p015ReviewedLoggingTransition, len(transitions))
	for _, transition := range transitions {
		result[transition.ID] = transition
	}
	return result
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
	b2cLoggerFileCounts := make(map[string]int)
	b2cLoggerOwnerCounts := make(map[string]int)
	b2cConsoleOwnerCounts := make(map[string]int)
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
		if cohort == "G" {
			b2cLoggerFileCounts[row.File]++
			b2cLoggerOwnerCounts[row.Owner]++
			p015ValidateB2COwnerCanary(t, row)
		}
		if cohort == "C" {
			b2cConsoleOwnerCounts[row.Owner]++
			p015ValidateB2COwnerCanary(t, row)
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
	expectedStages := p015ExpectedPreB2CStageCounts
	if p015B2CFinalized(rows) {
		expectedStages = p015ExpectedFinalStageCounts
	}
	for stage, want := range expectedStages {
		if got := stageCounts[stage]; got != want {
			t.Errorf("ledger stage %s has %d rows, want exact %d", stage, got, want)
		}
	}
	for stage, got := range stageCounts {
		if _, expected := expectedStages[stage]; !expected {
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
	p015RequireExactCounts(t, "P015b2c logger file", b2cLoggerFileCounts, p015B2CLoggerFileCounts)
	p015RequireExactCounts(t, "P015b2c logger owner", b2cLoggerOwnerCounts, p015B2CLoggerOwnerCounts)
	p015RequireExactCounts(t, "P015b2c console owner", b2cConsoleOwnerCounts, p015B2CConsoleOwnerCounts)
	if len(rows) != 381 {
		t.Errorf("ledger has %d stable rows, want exact 381", len(rows))
	}
	const originalAIdentities = 109
	if got := activeCounts["A"] + activeCounts["B"] + activeCounts["H"] + activeCounts["G"]; got != 322 {
		t.Errorf("P015b2 active scoped Pico cohort has %d rows, want 322", got)
	}
	if got := counts["A"] + counts["B"] + counts["H"] + counts["G"]; got != 326 {
		t.Errorf("P015b2 stable scoped Pico cohort has %d rows, want 326", got)
	}
	if got := originalAIdentities + counts["B"] + counts["H"] + counts["G"]; got != 319 {
		t.Errorf("original P015b2 logger census has %d identities, want exact 319", got)
	}
	if got := originalAIdentities + counts["B"] + counts["H"] + counts["G"] + counts["C"]; got != 342 {
		t.Errorf("original P015b2 logger plus console census has %d identities, want exact 342", got)
	}
	if got := counts["A"] + counts["B"] + counts["H"] + counts["G"] + counts["C"]; got != 349 {
		t.Errorf("stable P015b2 logger plus console ledger has %d rows, want exact 349", got)
	}
	if got := activeCounts["A"] + activeCounts["B"] + activeCounts["H"] + activeCounts["G"] + activeCounts["C"]; got != 345 {
		t.Errorf("active P015b2 logger plus console census has %d rows, want exact 345", got)
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
	if p015B2CFinalized(rows) {
		gatewayLegacy, rawConsole, safeConsole := 0, 0, 0
		for _, row := range rows {
			if !strings.HasPrefix(row.File, "pkg/gateway/") {
				continue
			}
			switch row.Kind {
			case "pico_legacy":
				gatewayLegacy++
			case "console":
				rawConsole++
			case "console_safe":
				safeConsole++
			}
		}
		if gatewayLegacy != 0 || rawConsole != 0 || safeConsole != 23 {
			t.Errorf(
				"final Gateway census = legacy %d, raw console %d, safe console %d; want 0, 0, 23",
				gatewayLegacy, rawConsole, safeConsole,
			)
		}
	}
	activeTotal := 0
	for _, count := range activeCounts {
		activeTotal += count
	}
	if activeTotal != 369 {
		t.Errorf("ledger has %d active source tuples, want exact 369", activeTotal)
	}
}

func p015RequireExactCounts(
	t *testing.T,
	label string,
	got map[string]int,
	want map[string]int,
) {
	t.Helper()
	for key, count := range want {
		if gotCount := got[key]; gotCount != count {
			t.Errorf("%s %s has %d rows, want exact %d", label, key, gotCount, count)
		}
	}
	for key, count := range got {
		if _, expected := want[key]; !expected {
			t.Errorf("%s has unexpected %s with %d rows", label, key, count)
		}
	}
}

func p015B2CFinalized(rows []p015LoggingSite) bool {
	loggerSafe, consoleSafe := 0, 0
	for _, row := range rows {
		switch p015SiteCohort(row.ID) {
		case "G":
			if row.Disposition != "b2c_logger_safe" || row.Kind != "pico_safe" {
				return false
			}
			loggerSafe++
		case "C":
			if row.Disposition != "b2c_console_safe" || row.Kind != "console_safe" {
				return false
			}
			consoleSafe++
		}
	}
	return loggerSafe == 54 && consoleSafe == 23
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
			)
	case "C":
		valid = strings.HasPrefix(row.File, "pkg/gateway/") &&
			p015DispositionKind(
				row,
				"b2c_console_deferred", "console",
				"b2c_console_safe", "console_safe",
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

func p015ValidateB2COwnerCanary(t *testing.T, row p015LoggingSite) {
	t.Helper()
	if len(p015B2CGroupByID) != 77 {
		t.Errorf("closed P015b2c group map has %d IDs, want exact 77", len(p015B2CGroupByID))
		return
	}
	expectation, exists := p015B2CGroupByID[row.ID]
	if !exists {
		t.Errorf("%s is outside the closed P015b2c group map", row.ID)
		return
	}
	switch p015SiteCohort(row.ID) {
	case "G":
		if _, expected := p015B2CLoggerFileCounts[row.File]; !expected {
			t.Errorf("%s is outside the closed P015b2c logger file map", row.ID)
		}
		wantLevel, expected := p015B2CLoggerLevelByID[row.ID]
		if !expected {
			t.Errorf("%s has no closed legacy level", row.ID)
		} else if gotLevel, ok := p015LoggerLevel(row.Callee); !ok || gotLevel != wantLevel {
			t.Errorf("%s logger level = %q, %v; want %q", row.ID, gotLevel, ok, wantLevel)
		}
	case "C":
		if row.File != "pkg/gateway/gateway.go" {
			t.Errorf("%s console source file = %s, want pkg/gateway/gateway.go", row.ID, row.File)
		}
	}
	if row.Disposition == "b2c_logger_deferred" || row.Disposition == "b2c_console_deferred" {
		if row.Canary != "-" {
			t.Errorf("%s deferred row unexpectedly owns canary %q", row.ID, row.Canary)
		}
		return
	}
	if !p015CanaryIncludes(row.Canary, expectation.canary) {
		t.Errorf("%s lacks exact owning P015b2c source canary %q", row.ID, expectation.canary)
	}
	if p015SiteCohort(row.ID) == "C" &&
		!p015CanaryIncludes(row.Canary, p015B2CConsoleCatalogCanary) {
		t.Errorf("%s lacks exact closed console catalog canary %q", row.ID, p015B2CConsoleCatalogCanary)
	}
	if p015SiteCohort(row.ID) == "C" &&
		!p015CanaryIncludes(row.Canary, p015B2CConsoleLifecycleCanary) {
		t.Errorf("%s lacks exact console lifecycle/cardinality canary %q", row.ID, p015B2CConsoleLifecycleCanary)
	}
}

func p015LoggerLevel(callee string) (string, bool) {
	name := strings.TrimPrefix(callee, "pico.")
	for _, level := range []string{"Debug", "Info", "Warn", "Error", "Fatal"} {
		if strings.HasPrefix(name, level) {
			return level, true
		}
	}
	return "", false
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
	transitions, err := p015ParseReviewedLoggingTransitions(p015B3AReviewedTransitionData)
	if err != nil {
		return []string{"invalid P015b3a reviewed transition manifest: " + err.Error()}
	}
	return p015LedgerHistoryIssuesWithTransitions(
		previous,
		current,
		p015ReviewedLoggingTransitionMap(transitions),
	)
}

func p015LedgerHistoryIssuesWithTransitions(
	previous []p015LoggingSite,
	current []p015LoggingSite,
	transitions map[string]p015ReviewedLoggingTransition,
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
		if oldCohort == "G" || oldCohort == "C" {
			issues = append(issues, p015B2CLedgerHistoryIssues(old, row)...)
			continue
		}
		if old == row {
			continue
		}
		if transition, reviewed := transitions[old.ID]; reviewed &&
			p015LoggingTransitionHash(old) == transition.FromHash {
			if got := p015LoggingTransitionHash(row); got != transition.ToHash {
				issues = append(
					issues,
					fmt.Sprintf(
						"ledger row %s did not use its exact reviewed P015b3a transition: got %s, want %s",
						old.ID,
						got,
						transition.ToHash,
					),
				)
			}
			continue
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
		pendingToRetired := p015PendingDisposition(old.Disposition) &&
			p015RetiredDisposition(row.Disposition)
		if p015SafeDisposition(old.Disposition) && p015RetiredDisposition(row.Disposition) {
			issues = append(issues, fmt.Sprintf("ledger row %s cannot retire after safety certification", old.ID))
		}
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
		if pendingToRetired && old.immutableSourceKey() != row.immutableSourceKey() {
			issues = append(
				issues,
				fmt.Sprintf("ledger row %s pending-to-retired transition changed its source tuple", old.ID),
			)
		}
		if !pendingToSafe && !pendingToRetired && old != row {
			issues = append(issues, fmt.Sprintf("ledger row %s changed after its source tuple was frozen", old.ID))
		}
	}
	return issues
}

func p015B2CLedgerHistoryIssues(old, row p015LoggingSite) []string {
	cohort := p015SiteCohort(old.ID)
	expectation, owned := p015B2CGroupByID[old.ID]
	if !owned {
		return []string{fmt.Sprintf("ledger row %s is outside the closed P015b2c group map", old.ID)}
	}
	if old == row {
		return nil
	}
	pendingDisposition := "b2c_logger_deferred"
	safeDisposition := "b2c_logger_safe"
	safeKind := "pico_safe"
	safeCallee := func(candidate p015LoggingSite) bool {
		level, ok := p015LoggerLevel(candidate.Callee)
		return ok && level == p015B2CLoggerLevelByID[candidate.ID] &&
			strings.HasSuffix(candidate.Callee, "SafeCF") &&
			candidate.Callee != "pico.DebugSensitiveCF"
	}
	wantCanary := expectation.canary
	oldShapeValid := old.Kind == "pico_legacy"
	if level, ok := p015LoggerLevel(old.Callee); !ok || level != p015B2CLoggerLevelByID[old.ID] {
		oldShapeValid = false
	}
	if cohort == "C" {
		pendingDisposition = "b2c_console_deferred"
		safeDisposition = "b2c_console_safe"
		safeKind = "console_safe"
		safeCallee = func(candidate p015LoggingSite) bool { return candidate.Callee == "fmt.Print" }
		wantCanary = p015B2CCompleteConsoleCanary(wantCanary)
		oldShapeValid = old.Kind == "console" &&
			(old.Callee == "fmt.Print" || old.Callee == "fmt.Printf" || old.Callee == "fmt.Println")
	}
	if old.Disposition != pendingDisposition {
		return []string{fmt.Sprintf("ledger row %s changed after P015b2c safety certification", old.ID)}
	}
	var issues []string
	if !oldShapeValid {
		issues = append(issues, fmt.Sprintf("ledger row %s pending source has an invalid original kind/callee", old.ID))
	}
	if old.Canary != "-" {
		issues = append(issues, fmt.Sprintf("ledger row %s pending source did not retain the empty canary", old.ID))
	}
	if row.Disposition != safeDisposition || row.Kind != safeKind || !safeCallee(row) {
		issues = append(
			issues,
			fmt.Sprintf(
				"ledger row %s used a forbidden P015b2c transition from %s to %s/%s/%s",
				old.ID,
				old.Disposition,
				row.Disposition,
				row.Kind,
				row.Callee,
			),
		)
	}
	if old.File != row.File || old.Owner != row.Owner || old.Ordinal != row.Ordinal {
		issues = append(issues, fmt.Sprintf("ledger row %s P015b2c migration changed file/owner/ordinal", old.ID))
	}
	if row.Canary != wantCanary {
		issues = append(issues, fmt.Sprintf("ledger row %s P015b2c migration changed its exact owning canary", old.ID))
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

func p015B2CCurrentSourceConverged(sites []p015LoggingSite) bool {
	loggerSafe, consoleSafe := 0, 0
	for _, site := range sites {
		if _, owned := p015B2CLoggerFileCounts[site.File]; owned {
			switch site.Kind {
			case "pico_safe":
				loggerSafe++
			case "pico_legacy":
				return false
			}
		}
		if site.File == "pkg/gateway/gateway.go" {
			switch site.Kind {
			case "console_safe":
				consoleSafe++
			case "console":
				return false
			}
		}
	}
	return loggerSafe == 54 && consoleSafe == 23
}

func p015MigrateB2CRows(
	ledger []p015LoggingSite,
	sites []p015LoggingSite,
) ([]p015LoggingSite, error) {
	loggerMigrated, err := p015MigrateB2CLoggerRows(ledger, sites)
	if err != nil {
		return nil, err
	}
	return p015MigrateB2CConsoleRows(loggerMigrated, sites)
}

func p015MigrateB2CLoggerRows(
	ledger []p015LoggingSite,
	sites []p015LoggingSite,
) ([]p015LoggingSite, error) {
	currentByIdentity := make(map[string]p015LoggingSite, 54)
	fileCounts := make(map[string]int)
	ownerCounts := make(map[string]int)
	for _, site := range sites {
		if _, owned := p015B2CLoggerFileCounts[site.File]; !owned {
			continue
		}
		if site.Kind != "pico_safe" && site.Kind != "pico_legacy" {
			continue
		}
		if site.Kind != "pico_safe" || !strings.HasSuffix(site.Callee, "SafeCF") ||
			site.Callee == "pico.DebugSensitiveCF" {
			return nil, fmt.Errorf(
				"P015b2c current Gateway logger is not safe-only: %s",
				p015DescribeSite(site),
			)
		}
		key := p015MigrationIdentityKey(site)
		if previous, duplicate := currentByIdentity[key]; duplicate {
			return nil, fmt.Errorf(
				"P015b2c current Gateway logger has duplicate migration identity: %s and %s",
				p015DescribeSite(previous), p015DescribeSite(site),
			)
		}
		currentByIdentity[key] = site
		fileCounts[site.File]++
		ownerCounts[site.Owner]++
	}
	if len(currentByIdentity) != 54 {
		return nil, fmt.Errorf(
			"P015b2c current safe Gateway logger source has %d identities, want exact 54",
			len(currentByIdentity),
		)
	}
	if issues := p015ExactCountIssues(
		"logger file",
		fileCounts,
		p015B2CLoggerFileCounts,
	); len(issues) != 0 {
		return nil, fmt.Errorf("P015b2c %s", strings.Join(issues, "; "))
	}
	if issues := p015ExactCountIssues(
		"logger owner",
		ownerCounts,
		p015B2CLoggerOwnerCounts,
	); len(issues) != 0 {
		return nil, fmt.Errorf("P015b2c %s", strings.Join(issues, "; "))
	}

	migrated := append([]p015LoggingSite(nil), ledger...)
	matched := make(map[string]struct{}, len(currentByIdentity))
	rows := 0
	for index, old := range migrated {
		if p015SiteCohort(old.ID) != "G" {
			continue
		}
		rows++
		key := p015MigrationIdentityKey(old)
		current, exists := currentByIdentity[key]
		if !exists {
			return nil, fmt.Errorf(
				"%s has no current logger source with the same file/owner/ordinal",
				old.ID,
			)
		}
		if _, duplicate := matched[key]; duplicate {
			return nil, fmt.Errorf("current P015b2c logger source matched more than once: %s", old.ID)
		}
		wantLevel := p015B2CLoggerLevelByID[old.ID]
		gotLevel, levelOK := p015LoggerLevel(current.Callee)
		if !levelOK || gotLevel != wantLevel {
			return nil, fmt.Errorf(
				"%s migrated logger level = %q, %v; want %q",
				old.ID,
				gotLevel,
				levelOK,
				wantLevel,
			)
		}
		expectation, owned := p015B2CGroupByID[old.ID]
		if !owned {
			return nil, fmt.Errorf("%s is outside the closed P015b2c group map", old.ID)
		}
		matched[key] = struct{}{}
		migrated[index].Disposition = "b2c_logger_safe"
		migrated[index].Kind = current.Kind
		migrated[index].Callee = current.Callee
		migrated[index].Call = current.Call
		migrated[index].Canary = expectation.canary
	}
	if rows != 54 || len(matched) != len(currentByIdentity) {
		return nil, fmt.Errorf(
			"P015b2c logger migration matched %d of %d current identities from %d stable rows",
			len(matched), len(currentByIdentity), rows,
		)
	}
	return migrated, nil
}

func p015MigrateB2CConsoleRows(
	ledger []p015LoggingSite,
	sites []p015LoggingSite,
) ([]p015LoggingSite, error) {
	currentByIdentity := make(map[string]p015LoggingSite, 23)
	ownerCounts := make(map[string]int)
	for _, site := range sites {
		if site.File != "pkg/gateway/gateway.go" ||
			(site.Kind != "console_safe" && site.Kind != "console") {
			continue
		}
		if site.Kind != "console_safe" || site.Callee != "fmt.Print" {
			return nil, fmt.Errorf(
				"P015b2c current Gateway console is not closed-only: %s",
				p015DescribeSite(site),
			)
		}
		key := p015MigrationIdentityKey(site)
		if previous, duplicate := currentByIdentity[key]; duplicate {
			return nil, fmt.Errorf(
				"P015b2c current Gateway console has duplicate migration identity: %s and %s",
				p015DescribeSite(previous), p015DescribeSite(site),
			)
		}
		currentByIdentity[key] = site
		ownerCounts[site.Owner]++
	}
	if len(currentByIdentity) != 23 {
		return nil, fmt.Errorf(
			"P015b2c current closed Gateway console has %d identities, want exact 23",
			len(currentByIdentity),
		)
	}
	if issues := p015ExactCountIssues(
		"console owner",
		ownerCounts,
		p015B2CConsoleOwnerCounts,
	); len(issues) != 0 {
		return nil, fmt.Errorf("P015b2c %s", strings.Join(issues, "; "))
	}

	migrated := append([]p015LoggingSite(nil), ledger...)
	matched := make(map[string]struct{}, len(currentByIdentity))
	rows := 0
	for index, old := range migrated {
		if p015SiteCohort(old.ID) != "C" {
			continue
		}
		rows++
		key := p015MigrationIdentityKey(old)
		current, exists := currentByIdentity[key]
		if !exists {
			return nil, fmt.Errorf(
				"%s has no current console source with the same file/owner/ordinal",
				old.ID,
			)
		}
		if _, duplicate := matched[key]; duplicate {
			return nil, fmt.Errorf("current P015b2c console source matched more than once: %s", old.ID)
		}
		expectation, owned := p015B2CGroupByID[old.ID]
		if !owned {
			return nil, fmt.Errorf("%s is outside the closed P015b2c group map", old.ID)
		}
		matched[key] = struct{}{}
		migrated[index].Disposition = "b2c_console_safe"
		migrated[index].Kind = current.Kind
		migrated[index].Callee = current.Callee
		migrated[index].Call = current.Call
		migrated[index].Canary = p015B2CCompleteConsoleCanary(expectation.canary)
	}
	if rows != 23 || len(matched) != len(currentByIdentity) {
		return nil, fmt.Errorf(
			"P015b2c console migration matched %d of %d current identities from %d stable rows",
			len(matched), len(currentByIdentity), rows,
		)
	}
	return migrated, nil
}

func p015ExactCountIssues(label string, got, want map[string]int) []string {
	var issues []string
	for key, count := range want {
		if gotCount := got[key]; gotCount != count {
			issues = append(
				issues,
				fmt.Sprintf("%s %s has %d, want %d", label, key, gotCount, count),
			)
		}
	}
	for key, count := range got {
		if _, expected := want[key]; !expected {
			issues = append(issues, fmt.Sprintf("%s has unexpected %s with %d", label, key, count))
		}
	}
	sort.Strings(issues)
	return issues
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
