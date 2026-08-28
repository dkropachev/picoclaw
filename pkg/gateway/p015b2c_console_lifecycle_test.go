package gateway

import (
	"bytes"
	"fmt"
	"go/ast"
	"go/format"
	"go/parser"
	"go/token"
	"os"
	"sort"
	"strings"
	"testing"
)

type p015B2CConsoleLifecycleExpectation struct {
	owner    string
	controls []string
	anchors  []p015B2CConsoleStatementAnchor
}

type p015B2CConsoleAnchorScope uint8

const (
	p015B2CConsoleAnchorLocal p015B2CConsoleAnchorScope = iota + 1
	p015B2CConsoleAnchorOwner
)

type p015B2CConsoleStatementAnchor struct {
	scope             p015B2CConsoleAnchorScope
	offset            int
	call              string
	consoleSite       string
	condition         string
	anyPrior          bool
	guardedInit       bool
	terminatingGuard  bool
	directExpression  bool
	directAssignment  bool
	trueBodyFinalCall bool
	directConsole     bool
	assignmentLeft    string
	assignmentToken   token.Token
	description       string
}

type p015B2CConsolePairExpectation struct {
	trueSite  string
	falseSite string
	condition string
}

type p015B2CConsoleLifecycleManifest struct {
	placements map[string]p015B2CConsoleLifecycleExpectation
	ownerOrder map[string][]string
	pairs      []p015B2CConsolePairExpectation
	bindReady  bool
}

type p015B2CConsoleControlFrame struct {
	node      ast.Node
	kind      string
	condition string
	branch    string
	signature string
}

type p015B2CConsolePlacement struct {
	site      string
	owner     string
	call      *ast.CallExpr
	statement *ast.ExprStmt
	controls  []p015B2CConsoleControlFrame
}

func TestP015B2CConsoleLifecycleAndCardinalityManifest(t *testing.T) {
	source, err := os.ReadFile("gateway.go")
	if err != nil {
		t.Fatal(err)
	}
	if issues := p015B2CConsoleLifecycleIssues(source, p015B2CFullConsoleLifecycleManifest()); len(issues) != 0 {
		t.Fatalf("Gateway console lifecycle/cardinality drifted:\n%s", strings.Join(issues, "\n"))
	}
}

func TestP015B2CConsoleLifecycleAnalyzerRejectsCardinalityMutations(t *testing.T) {
	t.Run("C001 moved outside bind-host range", func(t *testing.T) {
		const valid = `package gateway
func Run() {
	runningServices.HealthServer.SetReady(true)
	publishGatewayEvent(agentLoop, runtimeevents.KindGatewayReady, startedAt, nil)
	closeListeners = false
	agentLoop.ReleaseRuntimeStartupBarrier()
	startupResourcesOwned = false
	for range listenResult.BindHosts {
		fmt.Print(renderGatewayConsole(
			gatewayConsoleC001GatewayStarted,
			newGatewayConsolePort(port),
		))
	}
	fmt.Print(renderGatewayConsole(
		gatewayConsoleC002StopHint,
		newGatewayConsoleNoFields(),
	))
}`
		manifest := p015B2CConsoleLifecycleManifest{
			placements: map[string]p015B2CConsoleLifecycleExpectation{
				"gatewayConsoleC001GatewayStarted": {
					owner: "Run", controls: []string{"range listenResult.BindHosts"},
				},
				"gatewayConsoleC002StopHint": {owner: "Run"},
			},
			ownerOrder: map[string][]string{
				"Run": {"gatewayConsoleC001GatewayStarted", "gatewayConsoleC002StopHint"},
			},
			bindReady: true,
		}
		if issues := p015B2CConsoleLifecycleIssues([]byte(valid), manifest); len(issues) != 0 {
			t.Fatalf("valid bind-host fixture rejected: %v", issues)
		}

		const ranged = `	for range listenResult.BindHosts {
		fmt.Print(renderGatewayConsole(
			gatewayConsoleC001GatewayStarted,
			newGatewayConsolePort(port),
		))
	}`
		const unranged = `	for range listenResult.BindHosts {
	}
	fmt.Print(renderGatewayConsole(
		gatewayConsoleC001GatewayStarted,
		newGatewayConsolePort(port),
	))`
		mutated := p015B2CMutateConsoleLifecycleFixture(t, valid, ranged, unranged)
		issues := p015B2CConsoleLifecycleIssues([]byte(mutated), manifest)
		if len(issues) == 0 || !strings.Contains(strings.Join(issues, "\n"), "C001") {
			t.Fatalf("C001 range mutation was accepted: %v", issues)
		}
	})

	t.Run("conditional pair collapsed into one branch", func(t *testing.T) {
		const pair = `	if len(enabledChannels) > 0 {
		fmt.Print(renderGatewayConsole(
			gatewayConsoleC012RestartedChannelsEnabled,
			newGatewayConsoleCount(len(enabledChannels)),
		))
	} else {
		fmt.Print(renderGatewayConsole(
			gatewayConsoleC013NoRestartedChannelsEnabled,
			newGatewayConsoleNoFields(),
		))
	}`
		const collapsed = `	if len(enabledChannels) > 0 {
		fmt.Print(renderGatewayConsole(
			gatewayConsoleC012RestartedChannelsEnabled,
			newGatewayConsoleCount(len(enabledChannels)),
		))
		fmt.Print(renderGatewayConsole(
			gatewayConsoleC013NoRestartedChannelsEnabled,
			newGatewayConsoleNoFields(),
		))
	}`
		valid := "package gateway\nfunc restartServices() {\n" + pair + "\n}"
		manifest := p015B2CConsoleLifecycleManifest{
			placements: map[string]p015B2CConsoleLifecycleExpectation{
				"gatewayConsoleC012RestartedChannelsEnabled": {
					owner: "restartServices", controls: []string{"if len(enabledChannels) > 0 => true"},
				},
				"gatewayConsoleC013NoRestartedChannelsEnabled": {
					owner: "restartServices", controls: []string{"if len(enabledChannels) > 0 => false"},
				},
			},
			ownerOrder: map[string][]string{
				"restartServices": {
					"gatewayConsoleC012RestartedChannelsEnabled",
					"gatewayConsoleC013NoRestartedChannelsEnabled",
				},
			},
			pairs: []p015B2CConsolePairExpectation{{
				trueSite:  "gatewayConsoleC012RestartedChannelsEnabled",
				falseSite: "gatewayConsoleC013NoRestartedChannelsEnabled",
				condition: "len(enabledChannels) > 0",
			}},
		}
		if issues := p015B2CConsoleLifecycleIssues([]byte(valid), manifest); len(issues) != 0 {
			t.Fatalf("valid conditional fixture rejected: %v", issues)
		}

		mutated := p015B2CMutateConsoleLifecycleFixture(t, valid, pair, collapsed)
		issues := p015B2CConsoleLifecycleIssues([]byte(mutated), manifest)
		if len(issues) == 0 || !strings.Contains(strings.Join(issues, "\n"), "C013") {
			t.Fatalf("collapsed conditional pair was accepted: %v", issues)
		}
	})

	t.Run("success record moved before lifecycle operation", func(t *testing.T) {
		const operation = `	if err = runningServices.HeartbeatService.Start(); err != nil {
		return
	}`
		const record = `	fmt.Print(renderGatewayConsole(
		gatewayConsoleC008HeartbeatRestarted,
		newGatewayConsoleNoFields(),
	))`
		valid := "package gateway\nfunc restartServices() {\n" + operation +
			"\n" + record + "\n}"
		manifest := p015B2CConsoleLifecycleManifest{
			placements: map[string]p015B2CConsoleLifecycleExpectation{
				"gatewayConsoleC008HeartbeatRestarted": {
					owner: "restartServices",
					anchors: []p015B2CConsoleStatementAnchor{
						p015B2COwnerGuardedCallAnchor(
							-1,
							"runningServices.HeartbeatService.Start()",
							"err != nil",
							"successful heartbeat restart",
						),
					},
				},
			},
			ownerOrder: map[string][]string{
				"restartServices": {"gatewayConsoleC008HeartbeatRestarted"},
			},
		}
		if issues := p015B2CConsoleLifecycleIssues([]byte(valid), manifest); len(issues) != 0 {
			t.Fatalf("valid operation anchor fixture rejected: %v", issues)
		}

		mutated := p015B2CMutateConsoleLifecycleFixture(
			t,
			valid,
			operation+"\n"+record,
			record+"\n"+operation,
		)
		issues := p015B2CConsoleLifecycleIssues([]byte(mutated), manifest)
		if len(issues) == 0 || !strings.Contains(strings.Join(issues, "\n"), "C008") {
			t.Fatalf("premature success record was accepted: %v", issues)
		}

		const staleGuard = `	if err != nil {
		runningServices.HeartbeatService.Start()
		return
	}`
		movedIntoBody := p015B2CMutateConsoleLifecycleFixture(
			t,
			valid,
			operation,
			staleGuard,
		)
		issues = p015B2CConsoleLifecycleIssues([]byte(movedIntoBody), manifest)
		if len(issues) == 0 || !strings.Contains(strings.Join(issues, "\n"), "C008") {
			t.Fatalf("operation moved into stale error body was accepted: %v", issues)
		}

		const wrongLeftGuard = `	if _ = runningServices.HeartbeatService.Start(); err != nil {
		return
	}`
		wrongLeft := p015B2CMutateConsoleLifecycleFixture(
			t,
			valid,
			operation,
			wrongLeftGuard,
		)
		issues = p015B2CConsoleLifecycleIssues([]byte(wrongLeft), manifest)
		if len(issues) == 0 || !strings.Contains(strings.Join(issues, "\n"), "C008") {
			t.Fatalf("wrong-LHS operation guard was accepted: %v", issues)
		}

		const fallthroughGuard = `	if err = runningServices.HeartbeatService.Start(); err != nil {
		logFailure()
	}`
		fallthroughSource := p015B2CMutateConsoleLifecycleFixture(
			t,
			valid,
			operation,
			fallthroughGuard,
		)
		issues = p015B2CConsoleLifecycleIssues([]byte(fallthroughSource), manifest)
		if len(issues) == 0 || !strings.Contains(strings.Join(issues, "\n"), "C008") {
			t.Fatalf("fallthrough failure guard was accepted: %v", issues)
		}
	})

	t.Run("event automation assignment nested in non-dominating branch", func(t *testing.T) {
		const assignment = `	runningServices.EventAutomation, err = setupEventAutomationService(
		ctx,
		cfg,
		agentLoop,
	)`
		const branch = `	if runningServices.EventAutomation != nil {
		if cfg.Workflows.Enabled {
			fmt.Print(renderGatewayConsole(
				gatewayConsoleC021EventWorkersStarted,
				newGatewayConsoleNoFields(),
			))
		} else {
			fmt.Print(renderGatewayConsole(
				gatewayConsoleC022EventInboxOpened,
				newGatewayConsoleNoFields(),
			))
		}
	}`
		valid := "package gateway\nfunc setupAndStartServices() {\n" + assignment +
			"\n\tif err != nil {\n\t\treturn\n\t}\n" + branch + "\n}"
		manifest := p015B2CConsoleLifecycleManifest{
			placements: map[string]p015B2CConsoleLifecycleExpectation{
				"gatewayConsoleC021EventWorkersStarted": {
					owner: "setupAndStartServices",
					controls: []string{
						"if runningServices.EventAutomation != nil => true",
						"if cfg.Workflows.Enabled => true",
					},
					anchors: p015B2CEventAutomationAnchors("setupAndStartServices"),
				},
				"gatewayConsoleC022EventInboxOpened": {
					owner: "setupAndStartServices",
					controls: []string{
						"if runningServices.EventAutomation != nil => true",
						"if cfg.Workflows.Enabled => false",
					},
					anchors: p015B2CEventAutomationAnchors("setupAndStartServices"),
				},
			},
			ownerOrder: map[string][]string{
				"setupAndStartServices": {
					"gatewayConsoleC021EventWorkersStarted",
					"gatewayConsoleC022EventInboxOpened",
				},
			},
			pairs: []p015B2CConsolePairExpectation{{
				trueSite:  "gatewayConsoleC021EventWorkersStarted",
				falseSite: "gatewayConsoleC022EventInboxOpened",
				condition: "cfg.Workflows.Enabled",
			}},
		}
		if issues := p015B2CConsoleLifecycleIssues([]byte(valid), manifest); len(issues) != 0 {
			t.Fatalf("valid event-automation fixture rejected: %v", issues)
		}

		nested := p015B2CMutateConsoleLifecycleFixture(
			t,
			valid,
			assignment,
			"\tif hidden {\n"+strings.ReplaceAll(assignment, "\t", "\t\t")+"\n\t}",
		)
		issues := p015B2CConsoleLifecycleIssues([]byte(nested), manifest)
		if len(issues) == 0 || !strings.Contains(strings.Join(issues, "\n"), "C02") {
			t.Fatalf("non-dominating event-automation assignment was accepted: %v", issues)
		}

		fallthroughGuard := p015B2CMutateConsoleLifecycleFixture(
			t,
			valid,
			"\tif err != nil {\n\t\treturn\n\t}",
			"\tif err != nil {\n\t\tlogFailure()\n\t}",
		)
		issues = p015B2CConsoleLifecycleIssues([]byte(fallthroughGuard), manifest)
		if len(issues) == 0 || !strings.Contains(strings.Join(issues, "\n"), "C02") {
			t.Fatalf("fallthrough event-automation guard was accepted: %v", issues)
		}
	})
}

func p015B2CFullConsoleLifecycleManifest() p015B2CConsoleLifecycleManifest {
	return p015B2CConsoleLifecycleManifest{
		placements: map[string]p015B2CConsoleLifecycleExpectation{
			"gatewayConsoleC001GatewayStarted": {
				owner: "Run", controls: []string{"range listenResult.BindHosts"},
			},
			"gatewayConsoleC002StopHint": {owner: "Run"},
			"gatewayConsoleC003DebugEnabled": {
				owner: "Run", controls: []string{"if debug => true"},
				anchors: []p015B2CConsoleStatementAnchor{
					p015B2COwnerGuardedCallAnchor(
						-1,
						"executionPolicy.Validate()",
						"err != nil",
						"execution-policy validation",
					),
				},
			},
			"gatewayConsoleC004AgentStatus": {
				owner: "Run",
				anchors: []p015B2CConsoleStatementAnchor{
					p015B2COwnerDirectExpressionCallAnchor(
						-1,
						"publishGatewayEvent(agentLoop, runtimeevents.KindGatewayStart, startedAt, nil)",
						"Gateway-start publication",
					),
					p015B2COwnerDirectAssignmentCallAnchor(
						1,
						"collectGatewayStartupStatus(agentLoop.GetStartupInfo())",
						"startupStatus",
						token.DEFINE,
						"startup-status collection",
					),
				},
			},
			"gatewayConsoleC005ToolsLoaded": {
				owner: "Run",
				anchors: []p015B2CConsoleStatementAnchor{
					p015B2COwnerDirectAssignmentCallAnchor(
						-1,
						"collectGatewayStartupStatus(agentLoop.GetStartupInfo())",
						"startupStatus",
						token.DEFINE,
						"startup-status collection",
					),
				},
			},
			"gatewayConsoleC006SkillsAvailable": {
				owner: "Run",
				anchors: []p015B2CConsoleStatementAnchor{
					p015B2COwnerConsoleAnchor(-1, "gatewayConsoleC005ToolsLoaded"),
				},
			},
			"gatewayConsoleC007NoModelConfigured": {
				owner: "createStartupProvider",
				controls: []string{
					`if modelName == "" && allowEmptyStartup => true`,
				},
				anchors: []p015B2CConsoleStatementAnchor{
					p015B2CDirectAssignmentCallAnchor(
						p015B2CConsoleAnchorLocal,
						-1,
						"config.ErrNoModelConfigured.Error()",
						"reason",
						token.DEFINE,
						"fixed limited-mode reason",
					),
				},
			},
			"gatewayConsoleC008HeartbeatRestarted": {
				owner: "restartServices",
				anchors: []p015B2CConsoleStatementAnchor{
					p015B2COwnerGuardedCallAnchor(
						-1,
						"runningServices.HeartbeatService.Start()",
						"err != nil",
						"successful heartbeat restart",
					),
				},
			},
			"gatewayConsoleC009EventInboxReopened": {
				owner: "restartServices",
				controls: []string{
					"if runningServices.EventAutomation != nil => true",
					"if cfg.Workflows.Enabled => false",
				},
				anchors: p015B2CEventAutomationAnchors("restartServices"),
			},
			"gatewayConsoleC010CronRestarted": {
				owner: "restartServices",
				anchors: []p015B2CConsoleStatementAnchor{
					p015B2COwnerGuardedCallAnchor(
						-1,
						"runningServices.CronService.Start()",
						"err != nil",
						"successful cron restart",
					),
				},
			},
			"gatewayConsoleC011ChannelsRestarted": {
				owner: "restartServices",
				anchors: []p015B2CConsoleStatementAnchor{
					p015B2COwnerGuardedCallAnchor(
						-1,
						"runningServices.ChannelManager.Reload(context.Background(), cfg)",
						"err != nil",
						"successful channel reload",
					),
				},
			},
			"gatewayConsoleC012RestartedChannelsEnabled": {
				owner:    "restartServices",
				controls: []string{"if len(enabledChannels) > 0 => true"},
				anchors: []p015B2CConsoleStatementAnchor{
					p015B2COwnerDirectAssignmentCallAnchor(
						-1,
						"runningServices.ChannelManager.GetEnabledChannels()",
						"enabledChannels",
						token.DEFINE,
						"enabled-channel retrieval",
					),
				},
			},
			"gatewayConsoleC013NoRestartedChannelsEnabled": {
				owner:    "restartServices",
				controls: []string{"if len(enabledChannels) > 0 => false"},
				anchors: []p015B2CConsoleStatementAnchor{
					p015B2COwnerDirectAssignmentCallAnchor(
						-1,
						"runningServices.ChannelManager.GetEnabledChannels()",
						"enabledChannels",
						token.DEFINE,
						"enabled-channel retrieval",
					),
				},
			},
			"gatewayConsoleC014DeviceServiceRestarted": {
				owner: "restartServices",
				controls: []string{
					"if startErr := runningServices.DeviceService.Start(context.Background()); startErr != nil => false",
					"if cfg.Devices.Enabled => true",
				},
				anchors: []p015B2CConsoleStatementAnchor{
					p015B2COwnerDirectExpressionCallAnchor(
						-1,
						"runningServices.DeviceService.SetBus(msgBus)",
						"device-service bus binding",
					),
				},
			},
			"gatewayConsoleC015EventWorkersRestarted": {
				owner: "restartServices",
				controls: []string{
					"if runningServices.EventAutomation != nil => true",
					"if cfg.Workflows.Enabled => true",
				},
				anchors: p015B2CEventAutomationAnchors("restartServices"),
			},
			"gatewayConsoleC016CronStarted": {
				owner: "setupAndStartServices",
				anchors: []p015B2CConsoleStatementAnchor{
					p015B2COwnerGuardedCallAnchor(
						-1,
						"runningServices.CronService.Start()",
						"err != nil",
						"successful cron start",
					),
				},
			},
			"gatewayConsoleC017ChannelsEnabled": {
				owner:    "setupAndStartServices",
				controls: []string{"if len(enabledChannels) > 0 => true"},
				anchors: []p015B2CConsoleStatementAnchor{
					p015B2COwnerDirectAssignmentCallAnchor(
						-1,
						"runningServices.ChannelManager.GetEnabledChannels()",
						"enabledChannels",
						token.DEFINE,
						"enabled-channel retrieval",
					),
				},
			},
			"gatewayConsoleC018NoChannelsEnabled": {
				owner:    "setupAndStartServices",
				controls: []string{"if len(enabledChannels) > 0 => false"},
				anchors: []p015B2CConsoleStatementAnchor{
					p015B2COwnerDirectAssignmentCallAnchor(
						-1,
						"runningServices.ChannelManager.GetEnabledChannels()",
						"enabledChannels",
						token.DEFINE,
						"enabled-channel retrieval",
					),
				},
			},
			"gatewayConsoleC019HealthEndpointsAvailable": {
				owner: "setupAndStartServices",
				anchors: []p015B2CConsoleStatementAnchor{
					p015B2COwnerTrueBodyFinalCallAnchor(
						-1,
						"voiceAgent.Start(vaCtx)",
						"transcriber != nil",
						"voice-agent start branch",
					),
					p015B2COwnerPriorCallAnchor(
						"runningServices.ChannelManager.SetupHTTPServerListeners(listenResult.Listeners, listenAddr, runningServices.HealthServer)",
						"listener/health route setup",
					),
					p015B2COwnerPriorGuardedCallAnchor(
						"runningServices.ChannelManager.StartAll(context.Background())",
						"err != nil",
						"successful shared channel/HTTP start",
					),
				},
			},
			"gatewayConsoleC020DeviceServiceStarted": {
				owner: "setupAndStartServices",
				controls: []string{
					"if err = runningServices.DeviceService.Start(context.Background()); err != nil => false",
					"if cfg.Devices.Enabled => true",
				},
				anchors: []p015B2CConsoleStatementAnchor{
					p015B2COwnerDirectExpressionCallAnchor(
						-1,
						"runningServices.DeviceService.SetBus(msgBus)",
						"device-service bus binding",
					),
				},
			},
			"gatewayConsoleC021EventWorkersStarted": {
				owner: "setupAndStartServices",
				controls: []string{
					"if runningServices.EventAutomation != nil => true",
					"if cfg.Workflows.Enabled => true",
				},
				anchors: p015B2CEventAutomationAnchors("setupAndStartServices"),
			},
			"gatewayConsoleC022EventInboxOpened": {
				owner: "setupAndStartServices",
				controls: []string{
					"if runningServices.EventAutomation != nil => true",
					"if cfg.Workflows.Enabled => false",
				},
				anchors: p015B2CEventAutomationAnchors("setupAndStartServices"),
			},
			"gatewayConsoleC023HeartbeatStarted": {
				owner: "setupAndStartServices",
				anchors: []p015B2CConsoleStatementAnchor{
					p015B2COwnerGuardedCallAnchor(
						-1,
						"runningServices.HeartbeatService.Start()",
						"err != nil",
						"successful heartbeat start",
					),
				},
			},
		},
		ownerOrder: map[string][]string{
			"Run": {
				"gatewayConsoleC003DebugEnabled",
				"gatewayConsoleC004AgentStatus",
				"gatewayConsoleC005ToolsLoaded",
				"gatewayConsoleC006SkillsAvailable",
				"gatewayConsoleC001GatewayStarted",
				"gatewayConsoleC002StopHint",
			},
			"createStartupProvider": {"gatewayConsoleC007NoModelConfigured"},
			"restartServices": {
				"gatewayConsoleC008HeartbeatRestarted",
				"gatewayConsoleC011ChannelsRestarted",
				"gatewayConsoleC012RestartedChannelsEnabled",
				"gatewayConsoleC013NoRestartedChannelsEnabled",
				"gatewayConsoleC014DeviceServiceRestarted",
				"gatewayConsoleC015EventWorkersRestarted",
				"gatewayConsoleC009EventInboxReopened",
				"gatewayConsoleC010CronRestarted",
			},
			"setupAndStartServices": {
				"gatewayConsoleC017ChannelsEnabled",
				"gatewayConsoleC018NoChannelsEnabled",
				"gatewayConsoleC019HealthEndpointsAvailable",
				"gatewayConsoleC020DeviceServiceStarted",
				"gatewayConsoleC021EventWorkersStarted",
				"gatewayConsoleC022EventInboxOpened",
				"gatewayConsoleC023HeartbeatStarted",
				"gatewayConsoleC016CronStarted",
			},
		},
		pairs: []p015B2CConsolePairExpectation{
			{
				trueSite:  "gatewayConsoleC012RestartedChannelsEnabled",
				falseSite: "gatewayConsoleC013NoRestartedChannelsEnabled",
				condition: "len(enabledChannels) > 0",
			},
			{
				trueSite:  "gatewayConsoleC015EventWorkersRestarted",
				falseSite: "gatewayConsoleC009EventInboxReopened",
				condition: "cfg.Workflows.Enabled",
			},
			{
				trueSite:  "gatewayConsoleC017ChannelsEnabled",
				falseSite: "gatewayConsoleC018NoChannelsEnabled",
				condition: "len(enabledChannels) > 0",
			},
			{
				trueSite:  "gatewayConsoleC021EventWorkersStarted",
				falseSite: "gatewayConsoleC022EventInboxOpened",
				condition: "cfg.Workflows.Enabled",
			},
		},
		bindReady: true,
	}
}

func p015B2COwnerDirectExpressionCallAnchor(
	offset int,
	call string,
	description string,
) p015B2CConsoleStatementAnchor {
	return p015B2CConsoleStatementAnchor{
		scope: p015B2CConsoleAnchorOwner, offset: offset,
		call: call, directExpression: true, description: description,
	}
}

func p015B2COwnerGuardedCallAnchor(
	offset int,
	call string,
	condition string,
	description string,
) p015B2CConsoleStatementAnchor {
	return p015B2CConsoleStatementAnchor{
		scope: p015B2CConsoleAnchorOwner, offset: offset,
		call: call, condition: condition, guardedInit: true,
		terminatingGuard: true, description: description,
	}
}

func p015B2CDirectAssignmentCallAnchor(
	scope p015B2CConsoleAnchorScope,
	offset int,
	call string,
	left string,
	assignmentToken token.Token,
	description string,
) p015B2CConsoleStatementAnchor {
	return p015B2CConsoleStatementAnchor{
		scope: scope, offset: offset, call: call,
		directAssignment: true, assignmentLeft: left,
		assignmentToken: assignmentToken, description: description,
	}
}

func p015B2COwnerConsoleAnchor(
	offset int,
	site string,
) p015B2CConsoleStatementAnchor {
	return p015B2CConsoleStatementAnchor{
		scope: p015B2CConsoleAnchorOwner, offset: offset,
		consoleSite: site, directConsole: true,
		description: p015B2CShortConsoleSite(site) + " record",
	}
}

func p015B2COwnerPriorCallAnchor(
	call string,
	description string,
) p015B2CConsoleStatementAnchor {
	return p015B2CConsoleStatementAnchor{
		scope: p015B2CConsoleAnchorOwner, call: call,
		anyPrior: true, directExpression: true, description: description,
	}
}

func p015B2COwnerPriorGuardedCallAnchor(
	call string,
	condition string,
	description string,
) p015B2CConsoleStatementAnchor {
	return p015B2CConsoleStatementAnchor{
		scope: p015B2CConsoleAnchorOwner, call: call, condition: condition,
		anyPrior: true, guardedInit: true, terminatingGuard: true,
		description: description,
	}
}

func p015B2COwnerTrueBodyFinalCallAnchor(
	offset int,
	call string,
	condition string,
	description string,
) p015B2CConsoleStatementAnchor {
	return p015B2CConsoleStatementAnchor{
		scope: p015B2CConsoleAnchorOwner, offset: offset,
		call: call, condition: condition, trueBodyFinalCall: true,
		description: description,
	}
}

func p015B2COwnerDirectAssignmentCallAnchor(
	offset int,
	call string,
	left string,
	assignmentToken token.Token,
	description string,
) p015B2CConsoleStatementAnchor {
	return p015B2CDirectAssignmentCallAnchor(
		p015B2CConsoleAnchorOwner,
		offset,
		call,
		left,
		assignmentToken,
		description,
	)
}

func p015B2CEventAutomationAnchors(owner string) []p015B2CConsoleStatementAnchor {
	agent := "al"
	operation := "restart"
	if owner == "setupAndStartServices" {
		agent = "agentLoop"
		operation = "start"
	}
	return []p015B2CConsoleStatementAnchor{
		{
			scope: p015B2CConsoleAnchorOwner, offset: -1,
			condition: "err != nil", terminatingGuard: true,
			description: "terminating event-automation error guard",
		},
		p015B2COwnerDirectAssignmentCallAnchor(
			-2,
			"setupEventAutomationService(ctx, cfg, "+agent+")",
			"runningServices.EventAutomation, err",
			token.ASSIGN,
			"event-automation "+operation,
		),
	}
}

func p015B2CConsoleLifecycleIssues(
	source []byte,
	manifest p015B2CConsoleLifecycleManifest,
) []string {
	fileSet := token.NewFileSet()
	parsed, err := parser.ParseFile(fileSet, "gateway.go", source, parser.AllErrors)
	if err != nil {
		return []string{"parse lifecycle source: " + err.Error()}
	}
	parents := p015B2CConsoleParentMap(parsed)
	placements := make(map[string][]p015B2CConsolePlacement, len(manifest.placements))
	var issues []string
	ast.Inspect(parsed, func(node ast.Node) bool {
		call, ok := node.(*ast.CallExpr)
		if !ok || !p015B2CFmtOutputCall(call) {
			return true
		}
		site, shapeIssues := p015B2CConsoleSinkShapeIssues(call)
		for _, issue := range shapeIssues {
			issues = append(issues, fmt.Sprintf("console at %s: %s", fileSet.Position(call.Pos()), issue))
		}
		if site == "" {
			return true
		}
		statement, _ := parents[call].(*ast.ExprStmt)
		if statement == nil || statement.X != call {
			issues = append(
				issues,
				fmt.Sprintf(
					"%s is not a direct expression statement",
					p015B2CShortConsoleSite(site),
				),
			)
		}
		placements[site] = append(placements[site], p015B2CConsolePlacement{
			site:      site,
			owner:     p015B2CConsoleOwner(call, parents),
			call:      call,
			statement: statement,
			controls:  p015B2CConsoleControlPath(fileSet, call, parents),
		})
		return true
	})

	for site, expectation := range manifest.placements {
		actual := placements[site]
		if len(actual) != 1 {
			issues = append(
				issues,
				fmt.Sprintf(
					"%s occurrence count = %d; want 1",
					p015B2CShortConsoleSite(site),
					len(actual),
				),
			)
			continue
		}
		if actual[0].owner != expectation.owner {
			issues = append(
				issues,
				fmt.Sprintf(
					"%s owner = %q; want %q",
					p015B2CShortConsoleSite(site),
					actual[0].owner,
					expectation.owner,
				),
			)
		}
		gotControls := p015B2CConsoleControlSignatures(actual[0].controls)
		if !p015B2CStringSlicesEqual(gotControls, expectation.controls) {
			issues = append(
				issues,
				fmt.Sprintf(
					"%s controls = %q; want %q",
					p015B2CShortConsoleSite(site),
					gotControls,
					expectation.controls,
				),
			)
		}
		for _, anchor := range expectation.anchors {
			issues = append(
				issues,
				p015B2CConsoleStatementAnchorIssues(
					fileSet,
					parsed,
					parents,
					actual[0],
					anchor,
				)...,
			)
		}
	}
	for site := range placements {
		if _, expected := manifest.placements[site]; !expected {
			issues = append(
				issues,
				fmt.Sprintf(
					"unexpected console lifecycle site %s",
					p015B2CShortConsoleSite(site),
				),
			)
		}
	}

	for owner, expected := range manifest.ownerOrder {
		var ownerPlacements []p015B2CConsolePlacement
		for _, occurrences := range placements {
			for _, placement := range occurrences {
				if placement.owner == owner {
					ownerPlacements = append(ownerPlacements, placement)
				}
			}
		}
		sort.Slice(ownerPlacements, func(left, right int) bool {
			return ownerPlacements[left].call.Pos() < ownerPlacements[right].call.Pos()
		})
		actual := make([]string, 0, len(ownerPlacements))
		for _, placement := range ownerPlacements {
			actual = append(actual, placement.site)
		}
		if !p015B2CStringSlicesEqual(actual, expected) {
			issues = append(
				issues,
				fmt.Sprintf(
					"%s console source order = %q; want %q",
					owner,
					p015B2CShortConsoleSites(actual),
					p015B2CShortConsoleSites(expected),
				),
			)
		}
	}

	for _, pair := range manifest.pairs {
		issues = append(issues, p015B2CConsolePairIssues(placements, pair)...)
	}
	if manifest.bindReady {
		issues = append(
			issues,
			p015B2CBindReadyConsoleIssues(fileSet, parsed, parents, placements)...,
		)
	}
	sort.Strings(issues)
	return issues
}

func p015B2CConsoleParentMap(root ast.Node) map[ast.Node]ast.Node {
	parents := make(map[ast.Node]ast.Node)
	stack := make([]ast.Node, 0, 32)
	ast.Inspect(root, func(node ast.Node) bool {
		if node == nil {
			if len(stack) > 0 {
				stack = stack[:len(stack)-1]
			}
			return true
		}
		if len(stack) > 0 {
			parents[node] = stack[len(stack)-1]
		}
		stack = append(stack, node)
		return true
	})
	return parents
}

func p015B2CConsoleOwner(node ast.Node, parents map[ast.Node]ast.Node) string {
	for current := node; current != nil; current = parents[current] {
		if function, ok := current.(*ast.FuncDecl); ok {
			return function.Name.Name
		}
	}
	return ""
}

func p015B2CConsoleControlPath(
	fileSet *token.FileSet,
	node ast.Node,
	parents map[ast.Node]ast.Node,
) []p015B2CConsoleControlFrame {
	var reversed []p015B2CConsoleControlFrame
	for current := node; current != nil; current = parents[current] {
		parent := parents[current]
		switch control := parent.(type) {
		case *ast.IfStmt:
			branch := "outside"
			if p015B2CNodeContains(control.Body, node.Pos()) {
				branch = "true"
			} else if control.Else != nil && p015B2CNodeContains(control.Else, node.Pos()) {
				branch = "false"
			}
			condition := p015B2CConsoleNodeText(fileSet, control.Cond)
			header := "if "
			if control.Init != nil {
				header += p015B2CConsoleNodeText(fileSet, control.Init) + "; "
			}
			header += condition
			reversed = append(reversed, p015B2CConsoleControlFrame{
				node: control, kind: "if", condition: condition, branch: branch,
				signature: header + " => " + branch,
			})
		case *ast.RangeStmt:
			reversed = append(reversed, p015B2CConsoleControlFrame{
				node: control, kind: "range",
				signature: "range " + p015B2CConsoleNodeText(fileSet, control.X),
			})
		case *ast.ForStmt:
			reversed = append(reversed, p015B2CConsoleControlFrame{
				node: control, kind: "for", signature: "for control",
			})
		case *ast.SwitchStmt:
			reversed = append(reversed, p015B2CConsoleControlFrame{
				node: control, kind: "switch", signature: "switch control",
			})
		case *ast.TypeSwitchStmt:
			reversed = append(reversed, p015B2CConsoleControlFrame{
				node: control, kind: "type-switch", signature: "type-switch control",
			})
		case *ast.SelectStmt:
			reversed = append(reversed, p015B2CConsoleControlFrame{
				node: control, kind: "select", signature: "select control",
			})
		case *ast.CaseClause:
			reversed = append(reversed, p015B2CConsoleControlFrame{
				node: control, kind: "case", signature: "case control",
			})
		case *ast.CommClause:
			reversed = append(reversed, p015B2CConsoleControlFrame{
				node: control, kind: "comm", signature: "comm control",
			})
		case *ast.FuncLit:
			reversed = append(reversed, p015B2CConsoleControlFrame{
				node: control, kind: "func-literal", signature: "func literal",
			})
		case *ast.GoStmt:
			reversed = append(reversed, p015B2CConsoleControlFrame{
				node: control, kind: "go", signature: "go statement",
			})
		case *ast.DeferStmt:
			reversed = append(reversed, p015B2CConsoleControlFrame{
				node: control, kind: "defer", signature: "defer statement",
			})
		}
	}
	for left, right := 0, len(reversed)-1; left < right; left, right = left+1, right-1 {
		reversed[left], reversed[right] = reversed[right], reversed[left]
	}
	return reversed
}

func p015B2CConsoleStatementAnchorIssues(
	fileSet *token.FileSet,
	parsed *ast.File,
	parents map[ast.Node]ast.Node,
	placement p015B2CConsolePlacement,
	anchor p015B2CConsoleStatementAnchor,
) []string {
	statement, block := p015B2CConsoleAnchorRoot(parsed, parents, placement, anchor.scope)
	site := p015B2CShortConsoleSite(placement.site)
	if statement == nil || block == nil {
		return []string{fmt.Sprintf("%s cannot resolve its %s anchor block", site, anchor.description)}
	}
	index := p015B2CStatementIndex(block.List, statement)
	if index < 0 {
		return []string{fmt.Sprintf("%s cannot locate its %s anchor root", site, anchor.description)}
	}
	if anchor.anyPrior {
		for candidate := 0; candidate < index; candidate++ {
			if p015B2CConsoleAnchorMatches(fileSet, block.List[candidate], anchor) {
				return nil
			}
		}
		return []string{fmt.Sprintf(
			"%s does not follow %s in the same lifecycle block",
			site,
			anchor.description,
		)}
	}
	target := index + anchor.offset
	if target < 0 || target >= len(block.List) {
		return []string{fmt.Sprintf(
			"%s lacks %s at sibling offset %+d",
			site,
			anchor.description,
			anchor.offset,
		)}
	}
	if !p015B2CConsoleAnchorMatches(fileSet, block.List[target], anchor) {
		return []string{fmt.Sprintf(
			"%s sibling offset %+d is not %s",
			site,
			anchor.offset,
			anchor.description,
		)}
	}
	return nil
}

func p015B2CConsoleAnchorRoot(
	parsed *ast.File,
	parents map[ast.Node]ast.Node,
	placement p015B2CConsolePlacement,
	scope p015B2CConsoleAnchorScope,
) (ast.Stmt, *ast.BlockStmt) {
	if placement.statement == nil {
		return nil, nil
	}
	if scope == p015B2CConsoleAnchorLocal {
		block, _ := parents[placement.statement].(*ast.BlockStmt)
		return placement.statement, block
	}
	if scope != p015B2CConsoleAnchorOwner {
		return nil, nil
	}
	function := p015B2CFindConsoleFunction(parsed, placement.owner)
	if function == nil || function.Body == nil {
		return nil, nil
	}
	for current := ast.Node(placement.statement); current != nil; current = parents[current] {
		statement, ok := current.(ast.Stmt)
		if ok && parents[statement] == function.Body {
			return statement, function.Body
		}
	}
	return nil, nil
}

func p015B2CConsoleAnchorMatches(
	fileSet *token.FileSet,
	statement ast.Stmt,
	anchor p015B2CConsoleStatementAnchor,
) bool {
	conditional, isConditional := statement.(*ast.IfStmt)
	if anchor.condition != "" {
		if !isConditional || p015B2CConsoleNodeText(fileSet, conditional.Cond) != anchor.condition {
			return false
		}
	}
	if anchor.guardedInit {
		if !isConditional || conditional.Init == nil ||
			!p015B2CNodeIsExactAssignmentCall(
				conditional.Init,
				anchor.call,
				"err",
				token.ASSIGN,
			) {
			return false
		}
	} else if anchor.trueBodyFinalCall {
		if !isConditional || conditional.Else != nil || len(conditional.Body.List) == 0 ||
			!p015B2CStatementIsDirectExpressionCall(
				conditional.Body.List[len(conditional.Body.List)-1],
				anchor.call,
			) {
			return false
		}
	} else if anchor.directExpression {
		if !p015B2CStatementIsDirectExpressionCall(statement, anchor.call) {
			return false
		}
	} else if anchor.directAssignment {
		if !p015B2CNodeIsExactAssignmentCall(
			statement,
			anchor.call,
			anchor.assignmentLeft,
			anchor.assignmentToken,
		) {
			return false
		}
	} else if anchor.call != "" {
		// Call anchors must select one exact dominating AST shape above. An
		// unclassified recursive call search would accept nested/non-dominating
		// operations and is deliberately closed.
		return false
	}
	if anchor.terminatingGuard {
		if !isConditional || conditional.Else != nil ||
			(!anchor.guardedInit && conditional.Init != nil) ||
			!p015B2CBlockEndsWithDirectReturn(conditional.Body) {
			return false
		}
	}
	if anchor.consoleSite != "" {
		if !anchor.directConsole ||
			!p015B2CStatementIsDirectConsoleSite(statement, anchor.consoleSite) {
			return false
		}
	}
	return anchor.call != "" || anchor.consoleSite != "" || anchor.condition != ""
}

func p015B2CNodeIsExactAssignmentCall(
	node ast.Node,
	wantCall string,
	wantLeft string,
	wantToken token.Token,
) bool {
	assignment, ok := node.(*ast.AssignStmt)
	if !ok || len(assignment.Rhs) != 1 || assignment.Tok != wantToken ||
		p015B2CAssignmentLeftText(assignment) != wantLeft {
		return false
	}
	call, ok := assignment.Rhs[0].(*ast.CallExpr)
	return ok && p015B2CConsoleSemanticNodeText(call) == wantCall
}

func p015B2CAssignmentLeftText(assignment *ast.AssignStmt) string {
	if assignment == nil || len(assignment.Lhs) == 0 {
		return ""
	}
	parts := make([]string, 0, len(assignment.Lhs))
	for _, expression := range assignment.Lhs {
		parts = append(parts, p015B2CConsoleSemanticNodeText(expression))
	}
	return strings.Join(parts, ", ")
}

func p015B2CStatementIsDirectExpressionCall(statement ast.Stmt, want string) bool {
	expression, ok := statement.(*ast.ExprStmt)
	if !ok {
		return false
	}
	call, ok := expression.X.(*ast.CallExpr)
	return ok && p015B2CConsoleSemanticNodeText(call) == want
}

func p015B2CStatementIsDirectConsoleSite(statement ast.Stmt, want string) bool {
	expression, ok := statement.(*ast.ExprStmt)
	if !ok {
		return false
	}
	call, ok := expression.X.(*ast.CallExpr)
	if !ok || !p015B2CFmtOutputCall(call) {
		return false
	}
	site, issues := p015B2CConsoleSinkShapeIssues(call)
	return len(issues) == 0 && site == want
}

func p015B2CBlockEndsWithDirectReturn(block *ast.BlockStmt) bool {
	if block == nil || len(block.List) == 0 {
		return false
	}
	_, ok := block.List[len(block.List)-1].(*ast.ReturnStmt)
	return ok
}

func p015B2CConsoleSemanticNodeText(node ast.Node) string {
	if node == nil {
		return ""
	}
	var rendered bytes.Buffer
	if err := format.Node(&rendered, token.NewFileSet(), node); err != nil {
		return "<format-error>"
	}
	return strings.Join(strings.Fields(rendered.String()), " ")
}

func p015B2CConsolePairIssues(
	placements map[string][]p015B2CConsolePlacement,
	expectation p015B2CConsolePairExpectation,
) []string {
	left := placements[expectation.trueSite]
	right := placements[expectation.falseSite]
	if len(left) != 1 || len(right) != 1 {
		return nil
	}
	leftFrame := p015B2CFindConsoleIfFrame(left[0].controls, expectation.condition)
	rightFrame := p015B2CFindConsoleIfFrame(right[0].controls, expectation.condition)
	pair := p015B2CShortConsoleSite(expectation.trueSite) + "/" + p015B2CShortConsoleSite(expectation.falseSite)
	if leftFrame == nil || rightFrame == nil {
		return []string{pair + " lacks its exact conditional ancestor"}
	}
	var issues []string
	if leftFrame.node != rightFrame.node {
		issues = append(issues, pair+" does not share one conditional node")
	}
	if leftFrame.branch != "true" || rightFrame.branch != "false" {
		issues = append(issues, pair+" is not in opposite true/false branches")
	}
	if len(left[0].controls) != len(right[0].controls) {
		issues = append(issues, pair+" does not share the same enclosing control depth")
	} else {
		for index := range left[0].controls {
			if left[0].controls[index].node != right[0].controls[index].node {
				issues = append(issues, pair+" does not share its complete enclosing control path")
				break
			}
		}
	}
	return issues
}

func p015B2CFindConsoleIfFrame(
	controls []p015B2CConsoleControlFrame,
	condition string,
) *p015B2CConsoleControlFrame {
	for index := range controls {
		if controls[index].kind == "if" && controls[index].condition == condition {
			return &controls[index]
		}
	}
	return nil
}

func p015B2CBindReadyConsoleIssues(
	fileSet *token.FileSet,
	parsed *ast.File,
	parents map[ast.Node]ast.Node,
	placements map[string][]p015B2CConsolePlacement,
) []string {
	const (
		started = "gatewayConsoleC001GatewayStarted"
		hint    = "gatewayConsoleC002StopHint"
	)
	if len(placements[started]) != 1 || len(placements[hint]) != 1 {
		return nil
	}
	run := p015B2CFindConsoleFunction(parsed, "Run")
	if run == nil || run.Body == nil {
		return []string{"C001/C002 owner Run is missing"}
	}
	start := placements[started][0]
	stopHint := placements[hint][0]
	var ranged *ast.RangeStmt
	for _, frame := range start.controls {
		if candidate, ok := frame.node.(*ast.RangeStmt); ok {
			ranged = candidate
		}
	}
	var issues []string
	if ranged == nil {
		return []string{"C001 is outside the bind-host range"}
	}
	if ranged.Key != nil || ranged.Value != nil || ranged.Tok != token.ILLEGAL ||
		p015B2CConsoleNodeText(fileSet, ranged.X) != "listenResult.BindHosts" {
		issues = append(issues, "C001 range is not exact `for range listenResult.BindHosts`")
	}
	if parents[ranged] != run.Body {
		issues = append(issues, "C001 bind-host range is not top-level in Run")
	}
	if start.statement == nil || len(ranged.Body.List) != 1 || ranged.Body.List[0] != start.statement {
		issues = append(issues, "C001 is not the sole direct record emitted once per bind host")
	}
	rangeIndex := p015B2CStatementIndex(run.Body.List, ranged)
	hintIndex := p015B2CStatementIndex(run.Body.List, stopHint.statement)
	if rangeIndex < 0 || hintIndex != rangeIndex+1 {
		issues = append(issues, "C002 is not the immediate top-level record after the C001 bind-host range")
	}
	anchors := []string{
		"runningServices.HealthServer.SetReady(true)",
		"publishGatewayEvent(agentLoop, runtimeevents.KindGatewayReady, startedAt, nil)",
		"closeListeners = false",
		"agentLoop.ReleaseRuntimeStartupBarrier()",
		"startupResourcesOwned = false",
	}
	if rangeIndex < len(anchors) {
		issues = append(issues, "C001 bind-host range precedes readiness/startup-barrier anchors")
	} else {
		for offset, want := range anchors {
			index := rangeIndex - len(anchors) + offset
			if got := p015B2CConsoleNodeText(fileSet, run.Body.List[index]); got != want {
				issues = append(issues, fmt.Sprintf("C001 readiness anchor %d = %q; want %q", offset+1, got, want))
			}
		}
	}
	return issues
}

func p015B2CFindConsoleFunction(parsed *ast.File, name string) *ast.FuncDecl {
	for _, declaration := range parsed.Decls {
		function, ok := declaration.(*ast.FuncDecl)
		if ok && function.Recv == nil && function.Name.Name == name {
			return function
		}
	}
	return nil
}

func p015B2CStatementIndex(statements []ast.Stmt, target ast.Stmt) int {
	for index, statement := range statements {
		if statement == target {
			return index
		}
	}
	return -1
}

func p015B2CConsoleNodeText(fileSet *token.FileSet, node ast.Node) string {
	if node == nil {
		return ""
	}
	var rendered bytes.Buffer
	if err := format.Node(&rendered, fileSet, node); err != nil {
		return "<format-error>"
	}
	return strings.Join(strings.Fields(rendered.String()), " ")
}

func p015B2CNodeContains(node ast.Node, position token.Pos) bool {
	return node != nil && node.Pos() <= position && position < node.End()
}

func p015B2CConsoleControlSignatures(controls []p015B2CConsoleControlFrame) []string {
	result := make([]string, 0, len(controls))
	for _, control := range controls {
		result = append(result, control.signature)
	}
	return result
}

func p015B2CStringSlicesEqual(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func p015B2CShortConsoleSite(site string) string {
	const prefix = "gatewayConsole"
	if strings.HasPrefix(site, prefix) && len(site) >= len(prefix)+4 {
		return site[len(prefix) : len(prefix)+4]
	}
	return site
}

func p015B2CShortConsoleSites(sites []string) []string {
	result := make([]string, 0, len(sites))
	for _, site := range sites {
		result = append(result, p015B2CShortConsoleSite(site))
	}
	return result
}

func p015B2CMutateConsoleLifecycleFixture(t *testing.T, source, old, replacement string) string {
	t.Helper()
	if count := strings.Count(source, old); count != 1 {
		t.Fatalf("mutation target count = %d; want 1", count)
	}
	return strings.Replace(source, old, replacement, 1)
}
