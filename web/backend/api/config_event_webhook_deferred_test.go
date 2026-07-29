package api

import (
	"bytes"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/sipeed/picoclaw/pkg/config"
)

func TestConfigAPIInactiveExplicitEventWebhookReferenceIsNotResolved(t *testing.T) {
	for _, method := range []string{http.MethodPut, http.MethodPatch} {
		t.Run(method, func(t *testing.T) {
			configPath, cleanup, activeSecret := setupEventWebhookAPIConfig(t)
			defer cleanup()

			missingReference := "file://" + activeSecret
			body := eventWebhookAPIUpdateBody(t, configPath, method, map[string]map[string]any{
				"inert": {
					"enabled": false,
					"secret":  missingReference,
				},
			})
			response := performConfigAPIRequest(t, configPath, method, body)
			if response.Code != http.StatusOK {
				t.Fatalf(
					"%s /api/config status = %d, want %d, body=%s",
					method,
					response.Code,
					http.StatusOK,
					response.Body.String(),
				)
			}

			updated, err := config.LoadConfig(configPath)
			if err != nil {
				t.Fatalf("LoadConfig(updated) error = %v", err)
			}
			if got := configuredEventWebhookSecret(updated, "inert"); got != missingReference {
				t.Fatalf("inactive webhook secret = %q, want raw reference", got)
			}
			securityData, err := os.ReadFile(filepath.Join(
				filepath.Dir(configPath),
				config.SecurityConfigFile,
			))
			if err != nil {
				t.Fatalf("ReadFile(.security.yml) error = %v", err)
			}
			if !bytes.Contains(securityData, []byte(missingReference)) {
				t.Fatal("inactive webhook reference was not preserved")
			}
		})
	}
}

func TestConfigAPIRepairsEnabledMissingEventWebhookReference(t *testing.T) {
	for _, method := range []string{http.MethodPut, http.MethodPatch} {
		t.Run(method, func(t *testing.T) {
			configPath, cleanup := setupBrokenEventWebhookAPIConfig(t)
			defer cleanup()

			replacementSecret := eventWebhookAPISecret(0x62)
			const replacementFile = "replacement-webhook-secret.txt"
			if err := os.WriteFile(
				filepath.Join(filepath.Dir(configPath), replacementFile),
				[]byte(replacementSecret+"\n"),
				0o600,
			); err != nil {
				t.Fatalf("WriteFile(replacement secret) error = %v", err)
			}

			body := eventWebhookAPIUpdateBody(t, configPath, method, map[string]map[string]any{
				"deploy": {
					"enabled": true,
					"secret":  "file://" + replacementFile,
				},
			})
			response := performConfigAPIRequest(t, configPath, method, body)
			if response.Code != http.StatusOK {
				t.Fatalf(
					"%s /api/config status = %d, want %d, body=%s",
					method,
					response.Code,
					http.StatusOK,
					response.Body.String(),
				)
			}

			updated, err := config.LoadConfig(configPath)
			if err != nil {
				t.Fatalf("LoadConfig(repaired) error = %v", err)
			}
			if got := configuredEventWebhookSecret(updated, "deploy"); got != replacementSecret {
				t.Fatalf("repaired webhook secret = %q, want replacement", got)
			}
		})
	}
}

func TestConfigAPIRejectsMissingActiveEventWebhookReferenceOpaquely(t *testing.T) {
	for _, method := range []string{http.MethodPut, http.MethodPatch} {
		t.Run(method, func(t *testing.T) {
			configPath, cleanup, activeSecret := setupEventWebhookAPIConfig(t)
			defer cleanup()
			securityPath := filepath.Join(filepath.Dir(configPath), config.SecurityConfigFile)
			configBefore, err := os.ReadFile(configPath)
			if err != nil {
				t.Fatalf("ReadFile(config.json before update) error = %v", err)
			}
			securityBefore, err := os.ReadFile(securityPath)
			if err != nil {
				t.Fatalf("ReadFile(.security.yml before update) error = %v", err)
			}

			missingReference := "file://" + activeSecret
			body := eventWebhookAPIUpdateBody(t, configPath, method, map[string]map[string]any{
				"deploy": {
					"enabled": true,
					"secret":  missingReference,
				},
			})
			response := performConfigAPIRequest(t, configPath, method, body)
			if response.Code != http.StatusBadRequest {
				t.Fatalf(
					"%s /api/config status = %d, want %d, body=%s",
					method,
					response.Code,
					http.StatusBadRequest,
					response.Body.String(),
				)
			}
			if response.Body.String() != "resolve event webhook signing secret\n" {
				t.Fatalf("response body = %q, want static opaque error", response.Body.String())
			}
			if strings.Contains(response.Body.String(), activeSecret) ||
				strings.Contains(response.Body.String(), "file://") {
				t.Fatal("response exposed the missing credential reference")
			}

			configAfter, err := os.ReadFile(configPath)
			if err != nil {
				t.Fatalf("ReadFile(config.json after update) error = %v", err)
			}
			securityAfter, err := os.ReadFile(securityPath)
			if err != nil {
				t.Fatalf("ReadFile(.security.yml after update) error = %v", err)
			}
			if !bytes.Equal(configAfter, configBefore) {
				t.Fatal("rejected update changed config.json")
			}
			if !bytes.Equal(securityAfter, securityBefore) {
				t.Fatal("rejected update changed .security.yml")
			}
		})
	}
}

func TestConfigAPINewConnectorDoesNotInheritStaleSecuritySecret(t *testing.T) {
	for _, method := range []string{http.MethodPut, http.MethodPatch} {
		t.Run(method, func(t *testing.T) {
			configPath, cleanup, _ := setupEventWebhookAPIConfig(t)
			defer cleanup()

			staleSecret := eventWebhookAPISecret(0x73)
			addStaleEventWebhookSecurityEntry(t, configPath, "audit", staleSecret)
			fields := map[string]any{"enabled": false}
			if method == http.MethodPut {
				fields["secret"] = "[NOT_HERE]"
			}
			body := eventWebhookAPIUpdateBody(t, configPath, method, map[string]map[string]any{
				"audit": fields,
			})
			response := performConfigAPIRequest(t, configPath, method, body)
			if response.Code != http.StatusOK {
				t.Fatalf(
					"%s /api/config status = %d, want %d, body=%s",
					method,
					response.Code,
					http.StatusOK,
					response.Body.String(),
				)
			}

			updated, err := config.LoadConfig(configPath)
			if err != nil {
				t.Fatalf("LoadConfig(updated) error = %v", err)
			}
			if got := configuredEventWebhookSecret(updated, "audit"); got != "" {
				t.Fatalf("new connector inherited stale secret %q", got)
			}
			securityData, err := os.ReadFile(filepath.Join(
				filepath.Dir(configPath),
				config.SecurityConfigFile,
			))
			if err != nil {
				t.Fatalf("ReadFile(.security.yml) error = %v", err)
			}
			if bytes.Contains(securityData, []byte(staleSecret)) {
				t.Fatal("stale security-only webhook secret survived the update")
			}
		})
	}
}

func setupBrokenEventWebhookAPIConfig(t *testing.T) (string, func()) {
	t.Helper()
	configPath, cleanup, _ := setupEventWebhookAPIConfig(t)
	cfg, err := config.LoadConfig(configPath)
	if err != nil {
		cleanup()
		t.Fatalf("LoadConfig() error = %v", err)
	}

	const oldFile = "removed-webhook-secret.txt"
	oldSecret := eventWebhookAPISecret(0x61)
	oldPath := filepath.Join(filepath.Dir(configPath), oldFile)
	if err = os.WriteFile(oldPath, []byte(oldSecret+"\n"), 0o600); err != nil {
		cleanup()
		t.Fatalf("WriteFile(old secret) error = %v", err)
	}
	webhook := cfg.Events.Ingress.Webhooks["deploy"]
	webhook.Secret = *config.NewSecureString("file://" + oldFile)
	cfg.Events.Ingress.Webhooks["deploy"] = webhook
	if err = config.SaveConfig(configPath, cfg); err != nil {
		cleanup()
		t.Fatalf("SaveConfig(file reference) error = %v", err)
	}
	if err = os.Remove(oldPath); err != nil {
		cleanup()
		t.Fatalf("Remove(old secret) error = %v", err)
	}
	return configPath, cleanup
}

func addStaleEventWebhookSecurityEntry(
	t *testing.T,
	configPath string,
	name string,
	secret string,
) {
	t.Helper()
	securityPath := filepath.Join(filepath.Dir(configPath), config.SecurityConfigFile)
	data, err := os.ReadFile(securityPath)
	if err != nil {
		t.Fatalf("ReadFile(.security.yml) error = %v", err)
	}
	var document map[string]any
	if err = yaml.Unmarshal(data, &document); err != nil {
		t.Fatalf("Unmarshal(.security.yml) error = %v", err)
	}
	events, ok := document["events"].(map[string]any)
	if !ok {
		events = make(map[string]any)
		document["events"] = events
	}
	ingress, ok := events["ingress"].(map[string]any)
	if !ok {
		ingress = make(map[string]any)
		events["ingress"] = ingress
	}
	webhooks, ok := ingress["webhooks"].(map[string]any)
	if !ok {
		webhooks = make(map[string]any)
		ingress["webhooks"] = webhooks
	}
	webhooks[name] = map[string]any{"secret": secret}
	updated, err := yaml.Marshal(document)
	if err != nil {
		t.Fatalf("Marshal(.security.yml) error = %v", err)
	}
	if err = os.WriteFile(securityPath, updated, 0o600); err != nil {
		t.Fatalf("WriteFile(.security.yml) error = %v", err)
	}
}
