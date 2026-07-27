package modelrouter

import (
	"testing"

	"github.com/sipeed/picoclaw/pkg/config"
)

func TestRouterSelectsFirstMatchingRule(t *testing.T) {
	router := New("task-router", &config.ModelRouterConfig{
		Enabled: true,
		Entry:   "entry",
		Blocks: []config.ModelRouterBlock{
			{
				ID:   "entry",
				Type: config.ModelRouterBlockTypeRules,
				Rules: []config.ModelRouterRule{
					{Match: config.ModelRouterRuleHasCode, Target: "code"},
					{Match: config.ModelRouterRuleContains, Value: "translate", Target: "translation"},
				},
				Fallback: "default",
			},
			{ID: "code", Type: config.ModelRouterBlockTypeModel, Model: "code-model"},
			{ID: "translation", Type: config.ModelRouterBlockTypeModel, Model: "translation-model"},
			{ID: "default", Type: config.ModelRouterBlockTypeModel, Model: "default-model"},
		},
	})

	selection := router.Select(Input{UserMessage: "please translate this"})
	if selection.Target != "translation-model" {
		t.Fatalf("Target = %q, want translation-model", selection.Target)
	}

	selection = router.Select(Input{UserMessage: "```go\nfmt.Println()\n``` translate"})
	if selection.Target != "code-model" {
		t.Fatalf("Target = %q, want code-model", selection.Target)
	}
}

func TestRouterFallsBackWhenNoRuleMatches(t *testing.T) {
	router := New("task-router", &config.ModelRouterConfig{
		Enabled: true,
		Entry:   "entry",
		Blocks: []config.ModelRouterBlock{
			{
				ID:       "entry",
				Type:     config.ModelRouterBlockTypeRules,
				Rules:    []config.ModelRouterRule{{Match: config.ModelRouterRuleHasMedia, Target: "media"}},
				Fallback: "default",
			},
			{ID: "media", Type: config.ModelRouterBlockTypeModel, Model: "vision-model"},
			{ID: "default", Type: config.ModelRouterBlockTypeModel, Model: "default-model"},
		},
	})

	selection := router.Select(Input{UserMessage: "hello"})
	if selection.Target != "default-model" {
		t.Fatalf("Target = %q, want default-model", selection.Target)
	}
}
