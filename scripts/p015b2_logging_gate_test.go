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
	"B|b2b_deferred|pico_legacy":        132,
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
	current := p015ReadLoggingLedger(t, currentPath)
	previousData, found := p015PreviousLedger(repoRoot, currentPath)
	if !found {
		t.Skip("no earlier tracked logging ledger revision")
	}
	previous, err := p015ParseLoggingLedger(previousData)
	if err != nil {
		t.Fatalf("parse previous logging ledger: %v", err)
	}
	for _, issue := range p015LedgerHistoryIssues(previous, current) {
		t.Error(issue)
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
		})
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
	if got := retiredCounts["F"]; got != 8 {
		t.Errorf("functional census has %d retired rows, want eight formatter tombstones", got)
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

func p015HistoryMayChangeSource(oldDisposition, newDisposition string) bool {
	return p015PendingDisposition(oldDisposition) && p015SafeDisposition(newDisposition)
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
		if !p015HistoryMayChangeSource(old.Disposition, row.Disposition) &&
			old.immutableSourceKey() != row.immutableSourceKey() {
			issues = append(
				issues,
				fmt.Sprintf("ledger row %s changed its immutable source tuple", old.ID),
			)
		}
	}
	return issues
}

func p015PreviousLedger(repoRoot, currentPath string) ([]byte, bool) {
	relative, err := filepath.Rel(repoRoot, currentPath)
	if err != nil {
		return nil, false
	}
	relative = filepath.ToSlash(relative)
	logCommand := exec.Command("git", "log", "--format=%H", "--", relative)
	logCommand.Dir = repoRoot
	output, err := logCommand.Output()
	if err != nil {
		return nil, false
	}
	commits := strings.Fields(string(output))
	if len(commits) == 0 {
		return nil, false
	}
	current, err := os.ReadFile(currentPath)
	if err != nil {
		return nil, false
	}
	index := 0
	headData, headFound := p015GitFile(repoRoot, "HEAD", relative)
	if headFound && string(headData) == string(current) {
		index = 1
	}
	if index >= len(commits) {
		return nil, false
	}
	return p015GitFile(repoRoot, commits[index], relative)
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
