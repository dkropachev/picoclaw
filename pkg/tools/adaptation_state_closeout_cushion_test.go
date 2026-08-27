package tools

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/sipeed/picoclaw/pkg/providers"
)

func TestAdaptationStateCloseoutCushionLoadFailuresAndDefaults(t *testing.T) {
	if hash := ToolSchemaHash([]providers.ToolDefinition{{
		Function: providers.ToolFunctionDefinition{
			Name:       "bad",
			Parameters: map[string]any{"unsupported": make(chan int)},
		},
	}}); hash != "" {
		t.Fatalf("unsupported schema hash = %q", hash)
	}
	directory := t.TempDir()
	for _, test := range []struct {
		name string
		path string
		data []byte
	}{
		{"directory read", directory, nil},
		{"malformed", filepath.Join(directory, "malformed.json"), []byte("{")},
		{"nil maps", filepath.Join(directory, "nil.json"), []byte(`{"version":1}`)},
	} {
		t.Run(test.name, func(t *testing.T) {
			if test.data != nil {
				if err := os.WriteFile(test.path, test.data, 0o600); err != nil {
					t.Fatal(err)
				}
			}
			store := &toolAdaptationStateStore{
				observations: map[string]ToolAdaptationObservation{},
				outcomes:     map[string]ToolAdaptationToolOutcome{},
				pathOverride: test.path,
			}
			store.loadLocked()
			store.loadLocked()
			if !store.loaded {
				t.Fatal("adaptation state was not marked loaded")
			}
		})
	}
}

func TestAdaptationStateCloseoutCushionCanonicalMerge(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	earlier := time.Now().UTC().Add(-time.Hour)
	later := earlier.Add(time.Minute)
	data := `{
  "version": 1,
  "observations": {
    "a": {"profile":{"provider":"OPENAI","model":" Model "},"observed_at":"` + earlier.Format(time.RFC3339Nano) + `"},
    "b": {"profile":{"provider":"openai","model":"model"},"observed_at":"` + later.Format(time.RFC3339Nano) + `"},
    "ignored": {"profile":{}}
  },
  "outcomes": {
    "a": {"profile":{"provider":"OPENAI","model":" Model "},"visible_tool_surface":" native ","tool_name":" tool ","successes":1,"updated_at":"` + earlier.Format(time.RFC3339Nano) + `"},
    "b": {"profile":{"provider":"openai","model":"model"},"visible_tool_surface":"native","tool_name":"tool","failures":2,"updated_at":"` + later.Format(time.RFC3339Nano) + `"},
    "ignored": {"profile":{},"tool_name":""}
  }
}`
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
	store := &toolAdaptationStateStore{
		observations: map[string]ToolAdaptationObservation{},
		outcomes:     map[string]ToolAdaptationToolOutcome{},
		pathOverride: path,
	}
	store.loadLocked()
	if len(store.observations) != 1 || len(store.outcomes) != 1 {
		t.Fatalf("merged adaptation state = %#v / %#v", store.observations, store.outcomes)
	}
	for _, outcome := range store.outcomes {
		if outcome.Successes != 1 || outcome.Failures != 2 || outcome.ToolName != "tool" {
			t.Fatalf("merged outcome = %#v", outcome)
		}
	}
}

func TestAdaptationStateCloseoutCushionPersistenceFailures(t *testing.T) {
	store := &toolAdaptationStateStore{
		observations: map[string]ToolAdaptationObservation{},
		outcomes:     map[string]ToolAdaptationToolOutcome{},
		loaded:       true,
		pathOverride: t.TempDir(),
	}
	profile := ToolAdaptationProfile{Provider: "openai", Model: "model"}
	if _, ok := store.observe(
		profile,
		"native",
		nil,
		&providers.UsageInfo{PromptTokens: minCacheSniffPromptTokens, CachedTokens: 1},
	); !ok {
		t.Fatal("observation with persistence failure was dropped")
	}
	if _, ok := store.observeToolOutcome(
		profile, "native", "tool", false, " error ", time.Second,
	); !ok {
		t.Fatal("outcome with persistence failure was dropped")
	}
}
