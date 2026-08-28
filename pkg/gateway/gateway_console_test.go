package gateway

import (
	"reflect"
	"testing"
)

func TestGatewayConsoleCatalog(t *testing.T) {
	sites := [...]gatewayConsoleSiteID{
		gatewayConsoleC001GatewayStarted,
		gatewayConsoleC002StopHint,
		gatewayConsoleC003DebugEnabled,
		gatewayConsoleC004AgentStatus,
		gatewayConsoleC005ToolsLoaded,
		gatewayConsoleC006SkillsAvailable,
		gatewayConsoleC007NoModelConfigured,
		gatewayConsoleC008HeartbeatRestarted,
		gatewayConsoleC009EventInboxReopened,
		gatewayConsoleC010CronRestarted,
		gatewayConsoleC011ChannelsRestarted,
		gatewayConsoleC012RestartedChannelsEnabled,
		gatewayConsoleC013NoRestartedChannelsEnabled,
		gatewayConsoleC014DeviceServiceRestarted,
		gatewayConsoleC015EventWorkersRestarted,
		gatewayConsoleC016CronStarted,
		gatewayConsoleC017ChannelsEnabled,
		gatewayConsoleC018NoChannelsEnabled,
		gatewayConsoleC019HealthEndpointsAvailable,
		gatewayConsoleC020DeviceServiceStarted,
		gatewayConsoleC021EventWorkersStarted,
		gatewayConsoleC022EventInboxOpened,
		gatewayConsoleC023HeartbeatStarted,
	}
	for index, site := range sites {
		if want := gatewayConsoleSiteID(index + 1); site != want {
			t.Fatalf("site at offset %d = %d, want wire %d", index, site, want)
		}
	}

	tests := [...]struct {
		site   gatewayConsoleSiteID
		fields gatewayConsoleFields
		want   string
	}{
		{
			gatewayConsoleC001GatewayStarted,
			newGatewayConsolePort(18790),
			"✓ Gateway started on port 18790\n",
		},
		{
			gatewayConsoleC002StopHint,
			newGatewayConsoleNoFields(),
			"Press Ctrl+C to stop\n",
		},
		{
			gatewayConsoleC003DebugEnabled,
			newGatewayConsoleNoFields(),
			"🔍 Debug mode enabled\n",
		},
		{
			gatewayConsoleC004AgentStatus,
			newGatewayConsoleNoFields(),
			"\n📦 Agent Status:\n",
		},
		{
			gatewayConsoleC005ToolsLoaded,
			newGatewayConsoleCount(12),
			"  • Tools: 12 loaded\n",
		},
		{
			gatewayConsoleC006SkillsAvailable,
			newGatewayConsoleCountPair(7, 9),
			"  • Skills: 7/9 available\n",
		},
		{
			gatewayConsoleC007NoModelConfigured,
			newGatewayConsoleNoFields(),
			"⚠ Warning: no model configured\n",
		},
		{
			gatewayConsoleC008HeartbeatRestarted,
			newGatewayConsoleNoFields(),
			"  ✓ Heartbeat service restarted\n",
		},
		{
			gatewayConsoleC009EventInboxReopened,
			newGatewayConsoleNoFields(),
			"  ✓ Durable event inbox reopened (workflow dispatch disabled)\n",
		},
		{
			gatewayConsoleC010CronRestarted,
			newGatewayConsoleNoFields(),
			"  ✓ Cron service restarted\n",
		},
		{
			gatewayConsoleC011ChannelsRestarted,
			newGatewayConsoleNoFields(),
			"  ✓ Channels restarted.\n",
		},
		{
			gatewayConsoleC012RestartedChannelsEnabled,
			newGatewayConsoleCount(4),
			"  ✓ Channels enabled: 4\n",
		},
		{
			gatewayConsoleC013NoRestartedChannelsEnabled,
			newGatewayConsoleNoFields(),
			"  ⚠ Warning: No channels enabled\n",
		},
		{
			gatewayConsoleC014DeviceServiceRestarted,
			newGatewayConsoleNoFields(),
			"  ✓ Device event service restarted\n",
		},
		{
			gatewayConsoleC015EventWorkersRestarted,
			newGatewayConsoleNoFields(),
			"  ✓ Durable event inbox and workflow workers restarted\n",
		},
		{
			gatewayConsoleC016CronStarted,
			newGatewayConsoleNoFields(),
			"✓ Cron service started\n",
		},
		{
			gatewayConsoleC017ChannelsEnabled,
			newGatewayConsoleCount(3),
			"✓ Channels enabled: 3\n",
		},
		{
			gatewayConsoleC018NoChannelsEnabled,
			newGatewayConsoleNoFields(),
			"⚠ Warning: No channels enabled\n",
		},
		{
			gatewayConsoleC019HealthEndpointsAvailable,
			newGatewayConsolePort(8080),
			"✓ Health endpoints available on port 8080 at /health, " +
				"/ready and /reload (POST)\n",
		},
		{
			gatewayConsoleC020DeviceServiceStarted,
			newGatewayConsoleNoFields(),
			"✓ Device event service started\n",
		},
		{
			gatewayConsoleC021EventWorkersStarted,
			newGatewayConsoleNoFields(),
			"✓ Durable event inbox and workflow workers started\n",
		},
		{
			gatewayConsoleC022EventInboxOpened,
			newGatewayConsoleNoFields(),
			"✓ Durable event inbox opened (workflow dispatch disabled)\n",
		},
		{
			gatewayConsoleC023HeartbeatStarted,
			newGatewayConsoleNoFields(),
			"✓ Heartbeat service started\n",
		},
	}

	for _, test := range tests {
		if got := renderGatewayConsole(test.site, test.fields); got != test.want {
			t.Errorf("site %d rendered %q, want %q", test.site, got, test.want)
		}
	}
}

func TestGatewayConsoleCatalogRejectsMismatchedFieldShapes(t *testing.T) {
	tests := [...]struct {
		site gatewayConsoleSiteID
		kind gatewayConsoleFieldKind
	}{
		{gatewayConsoleC001GatewayStarted, gatewayConsoleFieldsPort},
		{gatewayConsoleC002StopHint, gatewayConsoleFieldsNone},
		{gatewayConsoleC003DebugEnabled, gatewayConsoleFieldsNone},
		{gatewayConsoleC004AgentStatus, gatewayConsoleFieldsNone},
		{gatewayConsoleC005ToolsLoaded, gatewayConsoleFieldsCount},
		{gatewayConsoleC006SkillsAvailable, gatewayConsoleFieldsCountPair},
		{gatewayConsoleC007NoModelConfigured, gatewayConsoleFieldsNone},
		{gatewayConsoleC008HeartbeatRestarted, gatewayConsoleFieldsNone},
		{gatewayConsoleC009EventInboxReopened, gatewayConsoleFieldsNone},
		{gatewayConsoleC010CronRestarted, gatewayConsoleFieldsNone},
		{gatewayConsoleC011ChannelsRestarted, gatewayConsoleFieldsNone},
		{gatewayConsoleC012RestartedChannelsEnabled, gatewayConsoleFieldsCount},
		{gatewayConsoleC013NoRestartedChannelsEnabled, gatewayConsoleFieldsNone},
		{gatewayConsoleC014DeviceServiceRestarted, gatewayConsoleFieldsNone},
		{gatewayConsoleC015EventWorkersRestarted, gatewayConsoleFieldsNone},
		{gatewayConsoleC016CronStarted, gatewayConsoleFieldsNone},
		{gatewayConsoleC017ChannelsEnabled, gatewayConsoleFieldsCount},
		{gatewayConsoleC018NoChannelsEnabled, gatewayConsoleFieldsNone},
		{gatewayConsoleC019HealthEndpointsAvailable, gatewayConsoleFieldsPort},
		{gatewayConsoleC020DeviceServiceStarted, gatewayConsoleFieldsNone},
		{gatewayConsoleC021EventWorkersStarted, gatewayConsoleFieldsNone},
		{gatewayConsoleC022EventInboxOpened, gatewayConsoleFieldsNone},
		{gatewayConsoleC023HeartbeatStarted, gatewayConsoleFieldsNone},
	}
	fields := map[gatewayConsoleFieldKind]gatewayConsoleFields{
		gatewayConsoleFieldsNone:      newGatewayConsoleNoFields(),
		gatewayConsoleFieldsCount:     newGatewayConsoleCount(2),
		gatewayConsoleFieldsCountPair: newGatewayConsoleCountPair(2, 3),
		gatewayConsoleFieldsPort:      newGatewayConsolePort(8080),
	}
	for _, test := range tests {
		for kind, candidate := range fields {
			if kind == test.kind {
				continue
			}
			if got := renderGatewayConsole(test.site, candidate); got != "" {
				t.Errorf("site %d accepted field kind %d: %q", test.site, kind, got)
			}
		}
	}
}

func TestGatewayConsoleCatalogFailsClosed(t *testing.T) {
	if (gatewayConsoleFields{kind: gatewayConsoleFieldKind(255)}).
		validFor(gatewayConsoleFieldKind(255)) {
		t.Fatal("unknown console field kind validated itself")
	}
	invalidFields := [...]gatewayConsoleFields{
		{},
		newGatewayConsoleCount(-1),
		newGatewayConsoleCountPair(-1, 0),
		newGatewayConsoleCountPair(0, -1),
		newGatewayConsolePort(-1),
		newGatewayConsolePort(0),
		newGatewayConsolePort(65536),
		{kind: gatewayConsoleFieldsNone, first: 1},
		{kind: gatewayConsoleFieldsCount, first: -1},
		{kind: gatewayConsoleFieldsCount, first: 1, second: 1},
		{kind: gatewayConsoleFieldsCountPair, first: -1},
		{kind: gatewayConsoleFieldsCountPair, second: -1},
		{kind: gatewayConsoleFieldsPort, first: 65536},
		{kind: gatewayConsoleFieldsPort, first: 8080, second: 1},
		{kind: gatewayConsoleFieldKind(255)},
	}
	for index, fields := range invalidFields {
		for site := gatewayConsoleC001GatewayStarted; site <= gatewayConsoleC023HeartbeatStarted; site++ {
			if got := renderGatewayConsole(site, fields); got != "" {
				t.Errorf("invalid fields %d rendered site %d: %q", index, site, got)
			}
		}
	}

	for _, site := range []gatewayConsoleSiteID{0, gatewayConsoleC023HeartbeatStarted + 1, 255} {
		if got := renderGatewayConsole(site, newGatewayConsoleNoFields()); got != "" {
			t.Errorf("invalid site %d rendered %q", site, got)
		}
	}
}

func TestGatewayConsoleCatalogHasNoRawInputSurface(t *testing.T) {
	functions := [...]any{
		newGatewayConsoleNoFields,
		newGatewayConsoleCount,
		newGatewayConsoleCountPair,
		newGatewayConsolePort,
		renderGatewayConsole,
	}
	for _, function := range functions {
		functionType := reflect.TypeOf(function)
		if functionType.IsVariadic() {
			t.Fatalf("%s is variadic", functionType)
		}
		for index := 0; index < functionType.NumIn(); index++ {
			assertGatewayConsoleInputType(t, functionType.In(index))
		}
	}

	fieldsType := reflect.TypeOf(gatewayConsoleFields{})
	for index := 0; index < fieldsType.NumField(); index++ {
		assertGatewayConsoleInputType(t, fieldsType.Field(index).Type)
	}
}

func assertGatewayConsoleInputType(t *testing.T, inputType reflect.Type) {
	t.Helper()
	switch inputType.Kind() {
	case reflect.Int, reflect.Uint8:
		return
	case reflect.Struct:
		if inputType != reflect.TypeOf(gatewayConsoleFields{}) {
			t.Fatalf("unexpected console input struct %s", inputType)
		}
		return
	default:
		t.Fatalf("raw-capable console input type %s", inputType)
	}
}
