package gateway

import "strconv"

// gatewayConsoleSiteID selects one fixed gateway progress record. Values are
// append-only because the P015b2 source ledger assigns C001-C023 stable source
// identities.
type gatewayConsoleSiteID uint8

const (
	gatewayConsoleC001GatewayStarted gatewayConsoleSiteID = iota + 1
	gatewayConsoleC002StopHint
	gatewayConsoleC003DebugEnabled
	gatewayConsoleC004AgentStatus
	gatewayConsoleC005ToolsLoaded
	gatewayConsoleC006SkillsAvailable
	gatewayConsoleC007NoModelConfigured
	gatewayConsoleC008HeartbeatRestarted
	gatewayConsoleC009EventInboxReopened
	gatewayConsoleC010CronRestarted
	gatewayConsoleC011ChannelsRestarted
	gatewayConsoleC012RestartedChannelsEnabled
	gatewayConsoleC013NoRestartedChannelsEnabled
	gatewayConsoleC014DeviceServiceRestarted
	gatewayConsoleC015EventWorkersRestarted
	gatewayConsoleC016CronStarted
	gatewayConsoleC017ChannelsEnabled
	gatewayConsoleC018NoChannelsEnabled
	gatewayConsoleC019HealthEndpointsAvailable
	gatewayConsoleC020DeviceServiceStarted
	gatewayConsoleC021EventWorkersStarted
	gatewayConsoleC022EventInboxOpened
	gatewayConsoleC023HeartbeatStarted
)

type gatewayConsoleFieldKind uint8

const (
	gatewayConsoleFieldsInvalid gatewayConsoleFieldKind = iota
	gatewayConsoleFieldsNone
	gatewayConsoleFieldsCount
	gatewayConsoleFieldsCountPair
	gatewayConsoleFieldsPort
)

// gatewayConsoleFields is the sealed input to the pure console catalog. It can
// carry only bounded-shape numeric metadata; arbitrary text, errors, values,
// maps, writers, and formats have no representation.
type gatewayConsoleFields struct {
	kind   gatewayConsoleFieldKind
	first  int
	second int
}

func newGatewayConsoleNoFields() gatewayConsoleFields {
	return gatewayConsoleFields{kind: gatewayConsoleFieldsNone}
}

func newGatewayConsoleCount(value int) gatewayConsoleFields {
	if value < 0 {
		return gatewayConsoleFields{}
	}
	return gatewayConsoleFields{kind: gatewayConsoleFieldsCount, first: value}
}

func newGatewayConsoleCountPair(first, second int) gatewayConsoleFields {
	if first < 0 || second < 0 {
		return gatewayConsoleFields{}
	}
	return gatewayConsoleFields{
		kind: gatewayConsoleFieldsCountPair, first: first, second: second,
	}
}

func newGatewayConsolePort(port int) gatewayConsoleFields {
	if port < 1 || port > 65535 {
		return gatewayConsoleFields{}
	}
	return gatewayConsoleFields{kind: gatewayConsoleFieldsPort, first: port}
}

func (fields gatewayConsoleFields) validFor(kind gatewayConsoleFieldKind) bool {
	if fields.kind != kind {
		return false
	}
	switch kind {
	case gatewayConsoleFieldsNone:
		return fields.first == 0 && fields.second == 0
	case gatewayConsoleFieldsCount:
		return fields.first >= 0 && fields.second == 0
	case gatewayConsoleFieldsCountPair:
		return fields.first >= 0 && fields.second >= 0
	case gatewayConsoleFieldsPort:
		return fields.first >= 1 && fields.first <= 65535 && fields.second == 0
	default:
		return false
	}
}

// renderGatewayConsole renders one complete progress record without emitting
// it. Unknown sites and invalid or mismatched field shapes fail closed.
func renderGatewayConsole(site gatewayConsoleSiteID, fields gatewayConsoleFields) string {
	switch site {
	case gatewayConsoleC001GatewayStarted:
		if !fields.validFor(gatewayConsoleFieldsPort) {
			return ""
		}
		return "✓ Gateway started on port " + strconv.Itoa(fields.first) + "\n"
	case gatewayConsoleC002StopHint:
		if !fields.validFor(gatewayConsoleFieldsNone) {
			return ""
		}
		return "Press Ctrl+C to stop\n"
	case gatewayConsoleC003DebugEnabled:
		if !fields.validFor(gatewayConsoleFieldsNone) {
			return ""
		}
		return "🔍 Debug mode enabled\n"
	case gatewayConsoleC004AgentStatus:
		if !fields.validFor(gatewayConsoleFieldsNone) {
			return ""
		}
		return "\n📦 Agent Status:\n"
	case gatewayConsoleC005ToolsLoaded:
		if !fields.validFor(gatewayConsoleFieldsCount) {
			return ""
		}
		return "  • Tools: " + strconv.Itoa(fields.first) + " loaded\n"
	case gatewayConsoleC006SkillsAvailable:
		if !fields.validFor(gatewayConsoleFieldsCountPair) {
			return ""
		}
		return "  • Skills: " + strconv.Itoa(fields.first) + "/" +
			strconv.Itoa(fields.second) + " available\n"
	case gatewayConsoleC007NoModelConfigured:
		if !fields.validFor(gatewayConsoleFieldsNone) {
			return ""
		}
		return "⚠ Warning: no model configured\n"
	case gatewayConsoleC008HeartbeatRestarted:
		if !fields.validFor(gatewayConsoleFieldsNone) {
			return ""
		}
		return "  ✓ Heartbeat service restarted\n"
	case gatewayConsoleC009EventInboxReopened:
		if !fields.validFor(gatewayConsoleFieldsNone) {
			return ""
		}
		return "  ✓ Durable event inbox reopened (workflow dispatch disabled)\n"
	case gatewayConsoleC010CronRestarted:
		if !fields.validFor(gatewayConsoleFieldsNone) {
			return ""
		}
		return "  ✓ Cron service restarted\n"
	case gatewayConsoleC011ChannelsRestarted:
		if !fields.validFor(gatewayConsoleFieldsNone) {
			return ""
		}
		return "  ✓ Channels restarted.\n"
	case gatewayConsoleC012RestartedChannelsEnabled:
		if !fields.validFor(gatewayConsoleFieldsCount) {
			return ""
		}
		return "  ✓ Channels enabled: " + strconv.Itoa(fields.first) + "\n"
	case gatewayConsoleC013NoRestartedChannelsEnabled:
		if !fields.validFor(gatewayConsoleFieldsNone) {
			return ""
		}
		return "  ⚠ Warning: No channels enabled\n"
	case gatewayConsoleC014DeviceServiceRestarted:
		if !fields.validFor(gatewayConsoleFieldsNone) {
			return ""
		}
		return "  ✓ Device event service restarted\n"
	case gatewayConsoleC015EventWorkersRestarted:
		if !fields.validFor(gatewayConsoleFieldsNone) {
			return ""
		}
		return "  ✓ Durable event inbox and workflow workers restarted\n"
	case gatewayConsoleC016CronStarted:
		if !fields.validFor(gatewayConsoleFieldsNone) {
			return ""
		}
		return "✓ Cron service started\n"
	case gatewayConsoleC017ChannelsEnabled:
		if !fields.validFor(gatewayConsoleFieldsCount) {
			return ""
		}
		return "✓ Channels enabled: " + strconv.Itoa(fields.first) + "\n"
	case gatewayConsoleC018NoChannelsEnabled:
		if !fields.validFor(gatewayConsoleFieldsNone) {
			return ""
		}
		return "⚠ Warning: No channels enabled\n"
	case gatewayConsoleC019HealthEndpointsAvailable:
		if !fields.validFor(gatewayConsoleFieldsPort) {
			return ""
		}
		return "✓ Health endpoints available on port " + strconv.Itoa(fields.first) +
			" at /health, /ready and /reload (POST)\n"
	case gatewayConsoleC020DeviceServiceStarted:
		if !fields.validFor(gatewayConsoleFieldsNone) {
			return ""
		}
		return "✓ Device event service started\n"
	case gatewayConsoleC021EventWorkersStarted:
		if !fields.validFor(gatewayConsoleFieldsNone) {
			return ""
		}
		return "✓ Durable event inbox and workflow workers started\n"
	case gatewayConsoleC022EventInboxOpened:
		if !fields.validFor(gatewayConsoleFieldsNone) {
			return ""
		}
		return "✓ Durable event inbox opened (workflow dispatch disabled)\n"
	case gatewayConsoleC023HeartbeatStarted:
		if !fields.validFor(gatewayConsoleFieldsNone) {
			return ""
		}
		return "✓ Heartbeat service started\n"
	default:
		return ""
	}
}
