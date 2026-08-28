package agent

import "testing"

func TestP015B2BTransportLoggingASTManifest(t *testing.T) {
	p015B2BValidateLoggingPartition(t, "transport/turn", map[string]p015B2BFileManifest{
		"agent_media.go": {sinks: []p015B2BSinkCount{
			{"DebugSafeCF", "ComponentAgent", 1},
			{"WarnSafeCF", "ComponentAgent", 6},
		}},
		"agent_outbound.go": {sinks: []p015B2BSinkCount{
			{"DebugSafeCF", "ComponentAgent", 3},
			{"InfoSafeCF", "ComponentAgent", 1},
			{"WarnSafeCF", "ComponentAgent", 6},
		}},
		"agent_steering.go": {sinks: []p015B2BSinkCount{
			{"InfoSafeCF", "ComponentAgent", 1},
			{"WarnSafeCF", "ComponentAgent", 2},
		}},
		"agent_transcribe.go": {sinks: []p015B2BSinkCount{{"WarnSafeCF", "ComponentVoice", 3}}},
		"llm_media.go":        {sinks: []p015B2BSinkCount{{"InfoSafeCF", "ComponentAgent", 1}}},
		"pipeline_setup.go":   {sinks: []p015B2BSinkCount{{"WarnSafeCF", "ComponentAgent", 4}}},
		"pipeline_streaming.go": {sinks: []p015B2BSinkCount{
			{"DebugSafeCF", "ComponentAgent", 10},
			{"WarnSafeCF", "ComponentAgent", 8},
		}},
		"steering.go": {sinks: []p015B2BSinkCount{
			{"DebugSafeCF", "ComponentAgent", 1},
			{"InfoSafeCF", "ComponentAgent", 1},
			{"WarnSafeCF", "ComponentAgent", 1},
		}},
		"turn_coord.go": {sinks: []p015B2BSinkCount{
			{"DebugSafeCF", "ComponentAgent", 2},
			{"InfoSafeCF", "ComponentAgent", 5},
			{"WarnSafeCF", "ComponentAgent", 2},
		}},
	}, 58)
}
