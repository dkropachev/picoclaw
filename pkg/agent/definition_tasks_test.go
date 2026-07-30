package agent

import (
	"reflect"
	"testing"
)

func TestAgentDefinitionTasksExcludesFrontmatterAndMatchesRuntimeBody(t *testing.T) {
	data := []byte(`---
description: |
  # Tasks
  - not a runtime task
---
Prompt prose.

# Tasks
- inspect the event
2. dispatch the workflow

# Later
- ignored
`)
	want := []string{"inspect the event", "dispatch the workflow"}
	if got := AgentDefinitionTasks(data); !reflect.DeepEqual(got, want) {
		t.Fatalf("AgentDefinitionTasks() = %#v, want %#v", got, want)
	}
}

func TestAgentDefinitionTasksRejectsUnterminatedFrontmatter(t *testing.T) {
	data := []byte(`---
description: unterminated

# Tasks
- must not be activated
`)
	if got := AgentDefinitionTasks(data); len(got) != 0 {
		t.Fatalf("AgentDefinitionTasks() = %#v, want none", got)
	}
}

func TestAgentDefinitionTasksRejectsMalformedClosedFrontmatter(t *testing.T) {
	tests := map[string][]byte{
		"syntax error": []byte(`---
tools: [
---
# Tasks
- must not be activated
`),
		"typed decode error": []byte(`---
tools: exec
---
# Tasks
- must not be activated
`),
	}
	for name, data := range tests {
		t.Run(name, func(t *testing.T) {
			if got := AgentDefinitionTasks(data); len(got) != 0 {
				t.Fatalf("AgentDefinitionTasks() = %#v, want none", got)
			}
		})
	}
}
