package config

import (
	"strings"
	"testing"
)

func TestModelRouterMaterializesVirtualModel(t *testing.T) {
	cfg := &Config{
		ModelList: []*ModelConfig{{
			ModelName: "gpt-main",
			Provider:  "openai",
			Model:     "gpt-5",
			APIKeys:   SimpleSecureStrings("key"),
			Enabled:   true,
		}},
		ModelRouters: []ModelRouterConfig{{
			Name:    "task-router",
			Enabled: true,
			Entry:   "entry",
			Blocks: []ModelRouterBlock{
				{ID: "entry", Type: ModelRouterBlockTypeRules, Fallback: "default", Rules: []ModelRouterRule{{
					Match:  ModelRouterRuleHasCode,
					Target: "default",
				}}},
				{ID: "default", Type: ModelRouterBlockTypeModel, Model: "gpt-main"},
			},
		}},
	}

	cfg.MaterializeModelRouterModels()

	model, err := cfg.GetModelConfig("task-router")
	if err != nil {
		t.Fatalf("GetModelConfig(task-router): %v", err)
	}
	if !model.IsVirtual() || !model.IsModelRouter() {
		t.Fatalf("router model virtual/model-router = %v/%v, want true/true", model.IsVirtual(), model.IsModelRouter())
	}
	if err := cfg.ValidateModelList(); err != nil {
		t.Fatalf("ValidateModelList() error = %v", err)
	}
}

func TestModelRouterValidationRejectsUnknownTarget(t *testing.T) {
	cfg := &Config{
		ModelRouters: []ModelRouterConfig{{
			Name:    "task-router",
			Enabled: true,
			Entry:   "entry",
			Blocks: []ModelRouterBlock{
				{ID: "entry", Type: ModelRouterBlockTypeRules, Fallback: "missing", Rules: []ModelRouterRule{{
					Match:  ModelRouterRuleHasCode,
					Target: "missing",
				}}},
				{ID: "missing", Type: ModelRouterBlockTypeModel, Model: "missing-model"},
			},
		}},
	}
	cfg.MaterializeModelRouterModels()

	err := cfg.ValidateModelList()
	if err == nil {
		t.Fatal("ValidateModelList() error = nil, want error")
	}
	if !strings.Contains(err.Error(), "unknown model") {
		t.Fatalf("ValidateModelList() error = %v, want unknown model", err)
	}
}

func TestModelRouterRejectsAccountRouterReferenceToModelRouter(t *testing.T) {
	cfg := &Config{
		AccountRouters: []AccountRouterConfig{{
			Name:    "account-router",
			Model:   "gpt-5",
			Enabled: true,
			Entry:   "entry",
			Blocks: []AccountRouterBlock{{
				ID:      "entry",
				Type:    AccountRouterBlockTypeAccount,
				Account: "task-router",
			}},
		}},
		ModelRouters: []ModelRouterConfig{{
			Name:    "task-router",
			Enabled: true,
			Entry:   "entry",
			Blocks:  []ModelRouterBlock{{ID: "entry", Type: ModelRouterBlockTypeModel, Model: "account-router"}},
		}},
	}
	cfg.MaterializeAccountRouterModels()
	cfg.MaterializeModelRouterModels()

	err := cfg.ValidateModelList()
	if err == nil {
		t.Fatal("ValidateModelList() error = nil, want error")
	}
	if !strings.Contains(err.Error(), "unknown account") {
		t.Fatalf("ValidateModelList() error = %v, want unknown account", err)
	}
}
