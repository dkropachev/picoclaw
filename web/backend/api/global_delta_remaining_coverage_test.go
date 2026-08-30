package api

import (
	"testing"

	"github.com/sipeed/picoclaw/pkg/config"
)

func TestGlobalDeltaRemainingModelNormalizationCoverage(t *testing.T) {
	if normalizeStoredModelConfig(nil) {
		t.Fatal("nil stored model reported a change")
	}
	empty := &config.ModelConfig{}
	if normalizeStoredModelConfig(empty) || empty.Provider != "" || empty.Model != "" {
		t.Fatalf("empty stored model normalized unexpectedly: %#v", empty)
	}
	normalizeIncomingModelConfig(nil)

	virtual := &config.ModelConfig{
		ModelName:    " routed-model ",
		Provider:     " MODEL-ROUTER ",
		APIKeys:      config.SimpleSecureStrings("secret"),
		APIBase:      "https://example.invalid/v1",
		Proxy:        "http://proxy.invalid",
		AuthMethod:   "TOKEN",
		CredentialID: "credential",
		ConnectMode:  "local",
		Workspace:    "/workspace",
	}
	normalizeIncomingModelConfig(virtual)
	if virtual.Provider != config.ModelRouterProvider || virtual.Model != "routed-model" ||
		len(virtual.APIKeys) != 0 || virtual.APIBase != "" || virtual.Proxy != "" ||
		virtual.AuthMethod != "" || virtual.CredentialID != "" ||
		virtual.ConnectMode != "" || virtual.Workspace != "" {
		t.Fatalf("incoming virtual model was not normalized: %#v", virtual)
	}

	if normalizeStoredModelProviders(nil) {
		t.Fatal("nil model catalog reported a change")
	}
}

func TestGlobalDeltaRemainingRouterConversionCoverage(t *testing.T) {
	if _, err := modelRouterFromModelConfig(nil); err == nil {
		t.Fatal("nil model router converted")
	}
	if _, err := modelRouterFromModelConfig(&config.ModelConfig{
		ModelRouter: &config.ModelRouterConfig{},
	}); err == nil {
		t.Fatal("nameless model router converted")
	}
	modelRouterInput := &config.ModelConfig{
		ModelName: " routed ", Enabled: true,
		ModelRouter: &config.ModelRouterConfig{
			Entry: "entry",
			Blocks: []config.ModelRouterBlock{{
				ID: "entry", Type: config.ModelRouterBlockTypeModel, Model: "cheap",
			}},
		},
	}
	modelRouter, err := modelRouterFromModelConfig(modelRouterInput)
	if err != nil || modelRouter.Name != "routed" || !modelRouter.Enabled {
		t.Fatalf("model router=%#v err=%v", modelRouter, err)
	}
	if _, conversionErr := modelRouterFromModelConfig(&config.ModelConfig{
		ModelName: "invalid", Enabled: true,
		ModelRouter: &config.ModelRouterConfig{Enabled: true},
	}); conversionErr == nil {
		t.Fatal("structurally invalid model router converted")
	}

	if _, conversionErr := accountRouterFromModelConfig(nil); conversionErr == nil {
		t.Fatal("nil account router converted")
	}
	if _, conversionErr := accountRouterFromModelConfig(&config.ModelConfig{
		Router: &config.AccountRouterConfig{},
	}); conversionErr == nil {
		t.Fatal("nameless account router converted")
	}
	accountRouterInput := &config.ModelConfig{
		ModelName: " accounts ", Enabled: true,
		Router: &config.AccountRouterConfig{
			Entry: "entry",
			Blocks: []config.AccountRouterBlock{{
				ID: "entry", Type: config.AccountRouterBlockTypeAccount, Account: "api",
			}},
		},
	}
	accountRouter, err := accountRouterFromModelConfig(accountRouterInput)
	if err != nil || accountRouter.Name != "accounts" || !accountRouter.Enabled {
		t.Fatalf("account router=%#v err=%v", accountRouter, err)
	}
	if _, err := accountRouterFromModelConfig(&config.ModelConfig{
		ModelName: "invalid", Enabled: true,
		Router: &config.AccountRouterConfig{},
	}); err == nil {
		t.Fatal("structurally invalid account router converted")
	}
}

func TestGlobalDeltaRemainingModelLookupAndValidationCoverage(t *testing.T) {
	if findAccountRouterIndex(nil, "router") != -1 ||
		findAccountRouterIndex(&config.Config{}, " ") != -1 {
		t.Fatal("invalid account router lookup succeeded")
	}
	if findModelRouterIndex(nil, "router") != -1 ||
		findModelRouterIndex(&config.Config{}, " ") != -1 {
		t.Fatal("invalid model router lookup succeeded")
	}
	if err := validateIncomingModelConfig(nil, nil); err == nil {
		t.Fatal("nil incoming model validated")
	}

	handler := NewHandler("")
	handler.SetServerBindHost(" 127.0.0.1 ", false)
	if handler.serverHostInput != "" || handler.serverHostExplicit {
		t.Fatalf("implicit bind host was retained: %#v", handler)
	}
}
