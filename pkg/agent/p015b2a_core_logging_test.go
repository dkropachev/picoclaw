package agent

import (
	"context"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"strings"
	"testing"

	"github.com/sipeed/picoclaw/pkg/bus"
	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/logger"
)

func TestP015B2ACoreFinalResponseZeroPreviewAndParity(t *testing.T) {
	const responseCanary = "P015B2A_FINAL_RESPONSE_3f8a0ad7d8c74c70"
	cfg := &config.Config{Agents: config.AgentsConfig{Defaults: config.AgentDefaults{
		Workspace: t.TempDir(), ModelName: "test-model", MaxTokens: 4096, MaxToolIterations: 2,
	}}}
	loop := newTestAgentLoopWithStrictModels(
		cfg,
		bus.NewMessageBus(),
		&simpleMockProvider{response: responseCanary},
	)

	var response string
	var processErr error
	records, raw := captureP015HookRecords(t, func() {
		response, processErr = loop.processMessage(
			context.Background(),
			testInboundMessage(bus.InboundMessage{Channel: "cli", Content: "request"}),
		)
	})
	if processErr != nil {
		t.Fatalf("processMessage() error = %v", processErr)
	}
	if response != responseCanary {
		t.Fatalf("processMessage() response = %q, want byte-exact canary", response)
	}
	assertP015CanariesAbsent(t, raw, responseCanary)

	var safeRecord, sensitiveRecord map[string]any
	safeCount, sensitiveCount := 0, 0
	for _, record := range records {
		if record["message"] != "Model response diagnostics" ||
			!p015B2ANonemptyRecordString(record, "identity_session_digest") {
			continue
		}
		if _, invalid := record["safe_fields_state"]; invalid {
			t.Fatalf("final-response safe fields rejected: %#v", record)
		}
		if !p015B2ANonemptyRecordString(record, "identity_agent_digest") ||
			!p015B2ANonemptyRecordString(record, "model_response_digest") ||
			record["content_bytes"] == nil || record["iteration"] == nil {
			t.Fatalf("final-response observation lacks safe metadata: %#v", record)
		}
		switch record["level"] {
		case "info":
			safeCount++
			safeRecord = record
		case "debug":
			sensitiveCount++
			sensitiveRecord = record
		}
	}
	if safeCount != 1 || sensitiveCount != 1 {
		t.Fatalf(
			"session-scoped final-response pair = %d info/%d debug, want exact 1/1; records=%#v",
			safeCount,
			sensitiveCount,
			records,
		)
	}
	wantObservation := logger.ObserveText(
		logger.ObservationDomainModelResponse,
		responseCanary,
	)
	p015B2AAssertRuntimeObservation(
		t,
		safeRecord,
		logger.ObservationPrefixModelResponse,
		wantObservation,
	)
	p015B2AAssertRuntimeObservation(
		t,
		sensitiveRecord,
		logger.ObservationPrefixModelResponse,
		wantObservation,
	)
	if safeRecord["model_response_digest"] != sensitiveRecord["model_response_digest"] ||
		safeRecord["model_response_bytes"] != sensitiveRecord["model_response_bytes"] ||
		safeRecord["content_bytes"] != sensitiveRecord["content_bytes"] {
		t.Fatalf(
			"final-response safe/sensitive observations diverged: info=%#v debug=%#v",
			safeRecord,
			sensitiveRecord,
		)
	}
}

func TestP015B2ACoreLoggingASTManifest(t *testing.T) {
	type fileExpectation struct {
		safe      int
		sensitive int
		component string
	}
	expected := map[string]fileExpectation{
		"agent.go":                    {safe: 22, sensitive: 1, component: "ComponentAgent"},
		"context_seahorse_catalog.go": {safe: 2, component: "ComponentSeahorse"},
		"subagent_result_mailbox.go":  {safe: 10, component: "ComponentAgent"},
		"subturn.go":                  {safe: 2, component: "ComponentSubturn"},
		"turn_state.go":               {safe: 4, component: "ComponentAgent"},
	}
	legacyEmitters := map[string]struct{}{
		"Debug": {}, "DebugC": {}, "DebugCF": {}, "DebugF": {}, "Debugf": {},
		"Info": {}, "InfoC": {}, "InfoCF": {}, "InfoF": {}, "Infof": {},
		"Warn": {}, "WarnC": {}, "WarnCF": {}, "WarnF": {}, "Warnf": {},
		"Error": {}, "ErrorC": {}, "ErrorCF": {}, "ErrorF": {}, "Errorf": {},
		"Fatal": {}, "FatalC": {}, "FatalCF": {}, "FatalF": {}, "Fatalf": {},
		"RecoverPanic": {}, "RecoverPanicNoExit": {},
	}

	totalSafe, totalSensitive := 0, 0
	for name, want := range expected {
		data, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		parsed, err := parser.ParseFile(token.NewFileSet(), name, data, parser.AllErrors)
		if err != nil {
			t.Fatal(err)
		}
		safeCount, sensitiveCount := 0, 0
		ast.Inspect(parsed, func(node ast.Node) bool {
			call, ok := node.(*ast.CallExpr)
			if !ok {
				return true
			}
			selector, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || !p015B2AIdent(selector.X, "logger") {
				return true
			}
			callee := selector.Sel.Name
			if _, legacy := legacyEmitters[callee]; legacy {
				t.Errorf("%s retains legacy logger.%s at byte %d", name, callee, call.Pos())
			}
			if strings.HasSuffix(callee, "SafeCF") {
				safeCount++
				p015B2AValidateCoreSafeSink(t, name, want.component, call)
			}
			if callee == "DebugSensitiveCF" {
				sensitiveCount++
				p015B2AValidateCoreSensitiveSink(t, name, call)
			}
			return true
		})
		if safeCount != want.safe || sensitiveCount != want.sensitive {
			t.Errorf(
				"%s sinks = %d safe/%d sensitive, want %d/%d",
				name,
				safeCount,
				sensitiveCount,
				want.safe,
				want.sensitive,
			)
		}
		totalSafe += safeCount
		totalSensitive += sensitiveCount
	}
	if totalSafe != 40 || totalSensitive != 1 {
		t.Fatalf(
			"core sink manifest = %d safe/%d sensitive, want exact 40/1",
			totalSafe,
			totalSensitive,
		)
	}
}

func p015B2AValidateCoreSafeSink(
	t *testing.T,
	file string,
	component string,
	call *ast.CallExpr,
) {
	t.Helper()
	if len(call.Args) != 3 ||
		!p015B2ASelector(call.Args[0], "logger", component) ||
		!p015B2ASelectorPrefix(call.Args[1], "logger", "DiagnosticMessage") ||
		!p015B2ACall(call.Args[2], "logger", "NewSafeFields") {
		t.Errorf("%s has non-direct/non-closed SafeCF call at byte %d", file, call.Pos())
	}
	p015B2ARejectHostileFormatting(t, file, call)
}

func p015B2AValidateCoreSensitiveSink(t *testing.T, file string, call *ast.CallExpr) {
	t.Helper()
	if len(call.Args) != 7 ||
		!p015B2AEmptyPolicy(call.Args[0]) ||
		!p015B2ASelector(call.Args[1], "logger", "ComponentAgent") ||
		!p015B2ASelector(call.Args[2], "logger", "DiagnosticMessageModelResponse") ||
		!p015B2ACall(call.Args[3], "logger", "NewSafeFields") ||
		!p015B2ASelector(call.Args[4], "logger", "SensitivityModelResponse") ||
		!p015B2ASelector(call.Args[5], "logger", "ObservationDomainModelResponse") {
		t.Errorf("%s has non-zero/non-direct final-response preview at byte %d", file, call.Pos())
	}
	p015B2ARejectHostileFormatting(t, file, call)
}
