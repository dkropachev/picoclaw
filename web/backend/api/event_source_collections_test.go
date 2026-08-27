package api

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sipeed/picoclaw/pkg/collectionquery"
	"github.com/sipeed/picoclaw/pkg/config"
)

func eventSourceTestStandardSecret(fill byte) string {
	return "whsec_" + base64.StdEncoding.EncodeToString(
		bytesOf(fill, 32),
	)
}

func bytesOf(value byte, length int) []byte {
	result := make([]byte, length)
	for index := range result {
		result[index] = value
	}
	return result
}

func eventSourceWebhookRequest(
	revision, name string,
	enabled bool,
	format, secretUpdate, secret string,
) map[string]any {
	return map[string]any{
		"expected_config_revision": revision,
		"event_source": map[string]any{
			"kind":               eventSourceKindWebhook,
			"name":               name,
			"enabled":            enabled,
			"format":             format,
			"repositories":       []string{},
			"target_user":        "",
			"poll_notifications": false,
			"secret_update":      secretUpdate,
			"secret":             secret,
		},
	}
}

func eventSourceChannelRequest(
	revision, name string,
	enabled bool,
) map[string]any {
	return map[string]any{
		"expected_config_revision": revision,
		"event_source": map[string]any{
			"kind":                   eventSourceKindChannel,
			"name":                   name,
			"enabled":                enabled,
			"source":                 config.EventChannelSourceEmail,
			"mode":                   config.EventChannelModeMirror,
			"allow_unverified_email": false,
		},
	}
}

func configureEventSourceCollectionFixture(cfg *config.Config) {
	standardSecret := eventSourceTestStandardSecret('a')
	githubSecret := strings.Repeat("g", 32)
	cfg.Events.Ingress = config.EventIngressConfig{
		Enabled: true,
		Webhooks: map[string]config.GenericWebhookConfig{
			"alpha": {
				Enabled: true,
				Format:  config.EventWebhookFormatStandard,
				Secret:  *config.NewSecureString(standardSecret),
			},
			"disabled": {
				Enabled: false,
				Format:  config.EventWebhookFormatStandard,
			},
			"github": {
				Enabled:           true,
				Format:            config.EventWebhookFormatGitHub,
				Repositories:      []string{"openai/codex"},
				TargetUser:        "octocat",
				PollNotifications: true,
				Secret:            *config.NewSecureString(githubSecret),
			},
		},
		Channels: map[string]config.ChannelEventIngressConfig{
			"deltachat": {
				Enabled: true,
				Source:  config.EventChannelSourceEmail,
				Mode:    config.EventChannelModeMirror,
			},
		},
	}
	delta := cfg.Channels.Get(config.ChannelDeltaChat)
	delta.Enabled = true
	mailbox := *delta
	mailbox.Enabled = false
	mailbox.Type = config.ChannelDeltaChat
	mailbox.SetName("mailbox")
	cfg.Channels["mailbox"] = &mailbox
}

func decodeEventSourceList(t *testing.T, responseBody []byte) struct {
	EventSources   []eventSourceSummary `json:"event_sources"`
	Total          int                  `json:"total"`
	NextCursor     string               `json:"next_cursor"`
	CanonicalQuery string               `json:"canonical_query"`
	QuerySchema    json.RawMessage      `json:"query_schema"`
	ConfigRevision string               `json:"config_revision"`
} {
	t.Helper()
	var response struct {
		EventSources   []eventSourceSummary `json:"event_sources"`
		Total          int                  `json:"total"`
		NextCursor     string               `json:"next_cursor"`
		CanonicalQuery string               `json:"canonical_query"`
		QuerySchema    json.RawMessage      `json:"query_schema"`
		ConfigRevision string               `json:"config_revision"`
	}
	if err := json.Unmarshal(responseBody, &response); err != nil {
		t.Fatalf("json.Unmarshal(list) error = %v; body=%s", err, responseBody)
	}
	return response
}

func TestEventSourceCollectionQueryPagingDetailAndSettings(t *testing.T) {
	resetGatewayTestState(t)
	harness := newAgentAPITestHarness(t, configureEventSourceCollectionFixture)
	query := `enabled = true ORDER BY name ASC`
	first := harness.request(
		t, http.MethodGet,
		"/api/event-sources?query="+url.QueryEscape(query)+"&limit=2", nil,
	)
	if first.Code != http.StatusOK {
		t.Fatalf("list status = %d, body=%s", first.Code, first.Body.String())
	}
	page := decodeEventSourceList(t, first.Body.Bytes())
	if len(page.EventSources) != 2 || page.Total != 3 || page.NextCursor == "" ||
		page.CanonicalQuery == "" || page.ConfigRevision == "" ||
		!strings.Contains(string(page.QuerySchema), `"alpha"`) {
		t.Fatalf("first page = %#v schema=%s", page, page.QuerySchema)
	}
	for _, source := range page.EventSources {
		if !validCollectionResourceID(source.ID) ||
			source.Status != eventSourceStatusAvailable {
			t.Fatalf("summary = %#v", source)
		}
	}
	second := harness.request(
		t, http.MethodGet,
		"/api/event-sources?query="+url.QueryEscape(query)+"&limit=2&cursor="+
			url.QueryEscape(page.NextCursor), nil,
	)
	if second.Code != http.StatusOK ||
		len(decodeEventSourceList(t, second.Body.Bytes()).EventSources) != 1 {
		t.Fatalf("second page status = %d, body=%s", second.Code, second.Body.String())
	}
	mismatch := harness.request(
		t, http.MethodGet,
		"/api/event-sources?query="+url.QueryEscape(`enabled = false`)+
			"&limit=2&cursor="+url.QueryEscape(page.NextCursor), nil,
	)
	if mismatch.Code != http.StatusBadRequest ||
		decodeCollectionErrorCode(t, mismatch.Body.Bytes()) != "invalid_cursor" {
		t.Fatalf("cursor mismatch = %d, body=%s", mismatch.Code, mismatch.Body.String())
	}

	alphaID, err := eventSourceResourceID(eventSourceKindWebhook, "alpha")
	if err != nil {
		t.Fatal(err)
	}
	detail := harness.request(
		t, http.MethodGet, "/api/event-sources/"+url.PathEscape(alphaID), nil,
	)
	if detail.Code != http.StatusOK {
		t.Fatalf("detail status = %d, body=%s", detail.Code, detail.Body.String())
	}
	var detailResponse struct {
		EventSource eventSourceWebhookDetail `json:"event_source"`
		Revision    string                   `json:"config_revision"`
	}
	if err = json.Unmarshal(detail.Body.Bytes(), &detailResponse); err != nil {
		t.Fatal(err)
	}
	if detailResponse.EventSource.ID != alphaID ||
		!detailResponse.EventSource.SecretConfigured ||
		detailResponse.Revision != page.ConfigRevision ||
		strings.Contains(detail.Body.String(), eventSourceTestStandardSecret('a')) ||
		strings.Contains(detail.Body.String(), "[NOT_HERE]") {
		t.Fatalf("unsafe detail = %s", detail.Body.String())
	}

	settings := harness.request(t, http.MethodGet, "/api/event-source-settings", nil)
	if settings.Code != http.StatusOK {
		t.Fatalf("settings status = %d, body=%s", settings.Code, settings.Body.String())
	}
	var settingsResponse struct {
		Settings eventSourceSettings           `json:"event_source_settings"`
		Eligible []eligibleEventChannelAdapter `json:"eligible_channel_adapters"`
		Revision string                        `json:"config_revision"`
	}
	if err = json.Unmarshal(settings.Body.Bytes(), &settingsResponse); err != nil {
		t.Fatal(err)
	}
	if !settingsResponse.Settings.Enabled || len(settingsResponse.Eligible) != 1 ||
		settingsResponse.Eligible[0].Name != "mailbox" ||
		settingsResponse.Eligible[0].ChannelEnabled ||
		settingsResponse.Revision != page.ConfigRevision {
		t.Fatalf("settings response = %#v", settingsResponse)
	}

	invalidQuery := `name = "é" AND unknown = value`
	invalid := harness.request(
		t, http.MethodGet,
		"/api/event-sources?query="+url.QueryEscape(invalidQuery), nil,
	)
	var queryError struct {
		Code     string `json:"code"`
		Message  string `json:"message"`
		Position int    `json:"position"`
	}
	_ = json.Unmarshal(invalid.Body.Bytes(), &queryError)
	if invalid.Code != http.StatusBadRequest || queryError.Code != "invalid_query" ||
		queryError.Position != strings.Index(invalidQuery, "unknown") ||
		len(queryError.Message) == 0 || len(queryError.Message) > 512 {
		t.Fatalf("query error = %#v, status=%d", queryError, invalid.Code)
	}
}

func TestEventSourceStatusPrecedenceAndInvalidConfigReads(t *testing.T) {
	resetGatewayTestState(t)
	harness := newAgentAPITestHarness(t, func(cfg *config.Config) {
		cfg.Events.Ingress.Enabled = false
		cfg.Events.Ingress.Webhooks = map[string]config.GenericWebhookConfig{
			"bad_name": {Enabled: true, Format: "unsupported"},
			"disabled": {Enabled: false, Format: config.EventWebhookFormatStandard},
		}
		cfg.Events.Ingress.Channels = map[string]config.ChannelEventIngressConfig{
			"missing": {
				Enabled: true, Source: config.EventChannelSourceEmail,
				Mode: config.EventChannelModeMirror,
			},
		}
	})
	response := harness.request(t, http.MethodGet, "/api/event-sources", nil)
	if response.Code != http.StatusOK {
		t.Fatalf("invalid config read status = %d, body=%s", response.Code, response.Body.String())
	}
	page := decodeEventSourceList(t, response.Body.Bytes())
	statuses := map[string]string{}
	for _, source := range page.EventSources {
		statuses[source.Name] = source.Status
	}
	if statuses["bad_name"] != eventSourceStatusInvalid ||
		statuses["disabled"] != eventSourceStatusDisabled ||
		statuses["missing"] != eventSourceStatusDisabled {
		t.Fatalf("statuses = %#v", statuses)
	}
}

func TestEventSourceProjectionRejectsSecretBearingPublicIdentity(t *testing.T) {
	resetGatewayTestState(t)
	secret := eventSourceTestStandardSecret('i')
	harness := newAgentAPITestHarness(t, func(cfg *config.Config) {
		cfg.Events.Ingress.Enabled = false
		cfg.Events.Ingress.Webhooks = map[string]config.GenericWebhookConfig{
			"anchor": {
				Format: config.EventWebhookFormatStandard,
				Secret: *config.NewSecureString(secret),
			},
			"public": {Format: config.EventWebhookFormatStandard},
		}
	})
	publicConfig, err := os.ReadFile(harness.configPath)
	if err != nil {
		t.Fatal(err)
	}
	corrupt := strings.Replace(
		string(publicConfig),
		`"public": {`,
		`"prefix-`+secret+`": {`,
		1,
	)
	if corrupt == string(publicConfig) {
		t.Fatal("test fixture did not replace public webhook identity")
	}
	if err = os.WriteFile(harness.configPath, []byte(corrupt), 0o600); err != nil {
		t.Fatal(err)
	}
	unsafeID, err := eventSourceResourceID(eventSourceKindWebhook, "prefix-"+secret)
	if err != nil {
		t.Fatal(err)
	}
	paths := []string{
		"/api/event-sources",
		"/api/event-sources/" + unsafeID,
		"/api/event-source-settings",
	}
	for _, path := range paths {
		response := harness.request(t, http.MethodGet, path, nil)
		if response.Code != http.StatusInternalServerError ||
			decodeCollectionErrorCode(t, response.Body.Bytes()) != "config_load_failed" ||
			strings.Contains(response.Body.String(), secret) ||
			len(response.Body.String()) > 600 {
			t.Fatalf("unsafe projection %s = %d, body=%s", path, response.Code, response.Body.String())
		}
	}
}

func TestEventSourcePersistedKeysRemainExactAndMutationNamesMustBeCanonical(t *testing.T) {
	resetGatewayTestState(t)
	harness := newAgentAPITestHarness(t, func(cfg *config.Config) {
		cfg.Events.Ingress.Enabled = false
		cfg.Events.Ingress.Webhooks = map[string]config.GenericWebhookConfig{
			"alpha":   {Format: config.EventWebhookFormatStandard},
			" alpha ": {Format: config.EventWebhookFormatStandard},
		}
		cfg.Channels.Get(config.ChannelDeltaChat).Enabled = true
	})
	list := harness.request(t, http.MethodGet, "/api/event-sources", nil)
	if list.Code != http.StatusOK {
		t.Fatalf("list status = %d, body=%s", list.Code, list.Body.String())
	}
	page := decodeEventSourceList(t, list.Body.Bytes())
	if len(page.EventSources) != 2 {
		t.Fatalf("event sources = %#v", page.EventSources)
	}
	byName := make(map[string]eventSourceSummary, len(page.EventSources))
	for _, source := range page.EventSources {
		byName[source.Name] = source
	}
	if byName["alpha"].ID == "" || byName[" alpha "].ID == "" ||
		byName["alpha"].ID == byName[" alpha "].ID ||
		byName["alpha"].Status != eventSourceStatusDisabled ||
		byName[" alpha "].Status != eventSourceStatusInvalid {
		t.Fatalf("exact-key projection = %#v", byName)
	}
	for _, name := range []string{"alpha", " alpha "} {
		wantID, err := eventSourceResourceID(eventSourceKindWebhook, name)
		if err != nil || byName[name].ID != wantID {
			t.Fatalf("ID for %q = %q, want %q; err=%v", name, byName[name].ID, wantID, err)
		}
		detail := harness.request(
			t, http.MethodGet, "/api/event-sources/"+url.PathEscape(wantID), nil,
		)
		if detail.Code != http.StatusOK || !strings.Contains(detail.Body.String(), `"name":"`+name+`"`) {
			t.Fatalf("detail %q = %d, body=%s", name, detail.Code, detail.Body.String())
		}
	}

	whitespaceWebhook := harness.request(
		t, http.MethodPost, "/api/event-sources",
		eventSourceWebhookRequest(
			page.ConfigRevision, " spaced ", false,
			config.EventWebhookFormatStandard, eventSourceSecretPreserve, "",
		),
	)
	if whitespaceWebhook.Code != http.StatusUnprocessableEntity ||
		decodeCollectionErrorCode(t, whitespaceWebhook.Body.Bytes()) != "invalid_event_source" {
		t.Fatalf(
			"whitespace webhook = %d, body=%s",
			whitespaceWebhook.Code,
			whitespaceWebhook.Body.String(),
		)
	}
	whitespaceChannel := harness.request(
		t, http.MethodPost, "/api/event-sources",
		eventSourceChannelRequest(page.ConfigRevision, " deltachat ", false),
	)
	if whitespaceChannel.Code != http.StatusUnprocessableEntity ||
		decodeCollectionErrorCode(t, whitespaceChannel.Body.Bytes()) != "invalid_event_source" {
		t.Fatalf(
			"whitespace channel = %d, body=%s",
			whitespaceChannel.Code,
			whitespaceChannel.Body.String(),
		)
	}
}

func TestEventSourceCRUDSecretsSettingsAndBulkDelete(t *testing.T) {
	resetGatewayTestState(t)
	harness := newAgentAPITestHarness(t, func(cfg *config.Config) {
		cfg.Events.Ingress.Enabled = false
		cfg.Channels.Get(config.ChannelDeltaChat).Enabled = true
	})
	revision, err := config.ConfigRevision(harness.configPath)
	if err != nil {
		t.Fatal(err)
	}
	invalid := eventSourceWebhookRequest(
		revision, "invalid", false, config.EventWebhookFormatStandard,
		eventSourceSecretPreserve, "",
	)
	invalidSource := invalid["event_source"].(map[string]any)
	invalidSource["repositories"] = []string{"not-a-repository"}
	invalidCreate := harness.request(t, http.MethodPost, "/api/event-sources", invalid)
	if invalidCreate.Code != http.StatusUnprocessableEntity ||
		decodeCollectionErrorCode(t, invalidCreate.Body.Bytes()) != "invalid_event_source" {
		t.Fatalf("invalid create = %d, body=%s", invalidCreate.Code, invalidCreate.Body.String())
	}

	create := harness.request(
		t, http.MethodPost, "/api/event-sources",
		eventSourceWebhookRequest(
			revision, "created", false, config.EventWebhookFormatStandard,
			eventSourceSecretPreserve, "",
		),
	)
	if create.Code != http.StatusCreated {
		t.Fatalf("create status = %d, body=%s", create.Code, create.Body.String())
	}
	var created struct {
		EventSource eventSourceWebhookDetail `json:"event_source"`
		Revision    string                   `json:"config_revision"`
	}
	if err = json.Unmarshal(create.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	if created.EventSource.Name != "created" || created.EventSource.SecretConfigured ||
		created.EventSource.Status != eventSourceStatusDisabled ||
		created.Revision == revision || create.Header().Get("Location") == "" {
		t.Fatalf("create response = %#v", created)
	}

	secret := eventSourceTestStandardSecret('r')
	update := harness.request(
		t, http.MethodPut, "/api/event-sources/"+created.EventSource.ID,
		eventSourceWebhookRequest(
			created.Revision, "created", true, config.EventWebhookFormatStandard,
			eventSourceSecretReplace, secret,
		),
	)
	if update.Code != http.StatusOK || strings.Contains(update.Body.String(), secret) {
		t.Fatalf("update status = %d, body=%s", update.Code, update.Body.String())
	}
	var updated struct {
		EventSource eventSourceWebhookDetail `json:"event_source"`
		Revision    string                   `json:"config_revision"`
	}
	if err = json.Unmarshal(update.Body.Bytes(), &updated); err != nil {
		t.Fatal(err)
	}
	if !updated.EventSource.SecretConfigured || updated.Revision == created.Revision {
		t.Fatalf("updated = %#v", updated)
	}

	rename := harness.request(
		t, http.MethodPut, "/api/event-sources/"+created.EventSource.ID,
		eventSourceWebhookRequest(
			updated.Revision, "renamed", true, config.EventWebhookFormatStandard,
			eventSourceSecretPreserve, "",
		),
	)
	if rename.Code != http.StatusConflict ||
		decodeCollectionErrorCode(t, rename.Body.Bytes()) != "event_source_name_immutable" {
		t.Fatalf("rename status = %d, body=%s", rename.Code, rename.Body.String())
	}

	formatChange := harness.request(
		t, http.MethodPut, "/api/event-sources/"+created.EventSource.ID,
		eventSourceWebhookRequest(
			updated.Revision, "created", true, config.EventWebhookFormatGitHub,
			eventSourceSecretPreserve, "",
		),
	)
	if formatChange.Code != http.StatusUnprocessableEntity ||
		decodeCollectionErrorCode(t, formatChange.Body.Bytes()) != "secret_replacement_required" {
		t.Fatalf("format change = %d, body=%s", formatChange.Code, formatChange.Body.String())
	}

	clearEnabled := harness.request(
		t, http.MethodPut, "/api/event-sources/"+created.EventSource.ID,
		eventSourceWebhookRequest(
			updated.Revision, "created", true, config.EventWebhookFormatStandard,
			eventSourceSecretClear, "",
		),
	)
	if clearEnabled.Code != http.StatusUnprocessableEntity ||
		decodeCollectionErrorCode(t, clearEnabled.Body.Bytes()) != "invalid_secret_update" {
		t.Fatalf("clear enabled = %d, body=%s", clearEnabled.Code, clearEnabled.Body.String())
	}

	clearResponse := harness.request(
		t, http.MethodPut, "/api/event-sources/"+created.EventSource.ID,
		eventSourceWebhookRequest(
			updated.Revision, "created", false, config.EventWebhookFormatStandard,
			eventSourceSecretClear, "",
		),
	)
	if clearResponse.Code != http.StatusOK {
		t.Fatalf(
			"clear status = %d, body=%s",
			clearResponse.Code,
			clearResponse.Body.String(),
		)
	}
	var cleared struct {
		EventSource eventSourceWebhookDetail `json:"event_source"`
		Revision    string                   `json:"config_revision"`
	}
	_ = json.Unmarshal(clearResponse.Body.Bytes(), &cleared)
	if cleared.EventSource.SecretConfigured {
		t.Fatalf("cleared response = %#v", cleared)
	}

	channel := harness.request(
		t, http.MethodPost, "/api/event-sources",
		eventSourceChannelRequest(cleared.Revision, config.ChannelDeltaChat, true),
	)
	if channel.Code != http.StatusCreated {
		t.Fatalf("channel create = %d, body=%s", channel.Code, channel.Body.String())
	}
	var channelCreated struct {
		EventSource eventSourceChannelDetail `json:"event_source"`
		Revision    string                   `json:"config_revision"`
	}
	_ = json.Unmarshal(channel.Body.Bytes(), &channelCreated)
	if channelCreated.EventSource.Status != eventSourceStatusDisabled ||
		channelCreated.EventSource.ChannelType != config.ChannelDeltaChat {
		t.Fatalf("channel response = %#v", channelCreated)
	}

	settings := eventSourceSettings{
		Enabled:         true,
		DatabasePath:    " state/events.db ",
		RetentionDays:   7,
		MaxPayloadBytes: 2048,
		RedactFields:    []string{" Custom_Token ", "custom_token"},
	}
	settingsUpdate := harness.request(
		t, http.MethodPut, "/api/event-source-settings",
		eventSourceSettingsMutationRequest{
			ExpectedConfigRevision: channelCreated.Revision,
			EventSourceSettings:    &settings,
		},
	)
	if settingsUpdate.Code != http.StatusOK {
		t.Fatalf("settings update = %d, body=%s", settingsUpdate.Code, settingsUpdate.Body.String())
	}
	var settingsResult struct {
		Settings eventSourceSettings `json:"event_source_settings"`
		Revision string              `json:"config_revision"`
	}
	_ = json.Unmarshal(settingsUpdate.Body.Bytes(), &settingsResult)
	if settingsResult.Settings.DatabasePath != "state/events.db" ||
		len(settingsResult.Settings.RedactFields) != 1 ||
		settingsResult.Revision == channelCreated.Revision {
		t.Fatalf("settings result = %#v", settingsResult)
	}

	unknownID, _ := eventSourceResourceID(eventSourceKindWebhook, "missing")
	bulk := harness.request(
		t, http.MethodPost, "/api/event-sources/bulk-delete",
		collectionBulkDeleteRequest{
			IDs: []string{
				created.EventSource.ID,
				channelCreated.EventSource.ID,
				unknownID,
				"bad",
				created.EventSource.ID,
			},
			ConfigRevision: settingsResult.Revision,
		},
	)
	if bulk.Code != http.StatusOK {
		t.Fatalf("bulk status = %d, body=%s", bulk.Code, bulk.Body.String())
	}
	var bulkResult collectionBulkDeleteResponse
	if err = json.Unmarshal(bulk.Body.Bytes(), &bulkResult); err != nil {
		t.Fatal(err)
	}
	if len(bulkResult.DeletedIDs) != 1 ||
		bulkResult.DeletedIDs[0] != channelCreated.EventSource.ID ||
		len(bulkResult.Failures) != 3 {
		t.Fatalf("bulk result = %#v", bulkResult)
	}
	codes := map[string]string{}
	for _, failure := range bulkResult.Failures {
		codes[failure.ID] = failure.Code
	}
	if codes[created.EventSource.ID] != "duplicate_id" ||
		codes[unknownID] != "not_found" || codes["bad"] != "invalid_id" {
		t.Fatalf("bulk failure codes = %#v", codes)
	}

	deleteResponse := harness.request(
		t, http.MethodDelete, "/api/event-sources/"+created.EventSource.ID,
		eventSourceRevisionRequest{ExpectedConfigRevision: bulkResult.ConfigRevision},
	)
	if deleteResponse.Code != http.StatusOK {
		t.Fatalf("delete status = %d, body=%s", deleteResponse.Code, deleteResponse.Body.String())
	}
	loaded, _, err := config.LoadConfigForUpdateSnapshot(harness.configPath)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Events.Ingress.DatabasePath != "state/events.db" ||
		len(loaded.Events.Ingress.Webhooks) != 0 ||
		len(loaded.Events.Ingress.Channels) != 0 {
		t.Fatalf("persisted ingress = %#v", loaded.Events.Ingress)
	}
}

func TestEventSourceBrokenReferenceReadAndRepair(t *testing.T) {
	resetGatewayTestState(t)
	harness := newAgentAPITestHarness(t, func(cfg *config.Config) {
		cfg.Events.Ingress.Enabled = true
		cfg.Events.Ingress.Webhooks = map[string]config.GenericWebhookConfig{
			"broken": {
				Enabled: true,
				Format:  config.EventWebhookFormatStandard,
				Secret:  *config.NewSecureString(eventSourceTestStandardSecret('o')),
			},
		}
	})
	secretPath := filepath.Join(filepath.Dir(harness.configPath), "event-secret")
	if err := os.WriteFile(
		secretPath, []byte(eventSourceTestStandardSecret('o')), 0o600,
	); err != nil {
		t.Fatal(err)
	}
	cfg, _, err := config.LoadConfigForUpdateSnapshot(harness.configPath)
	if err != nil {
		t.Fatal(err)
	}
	broken := cfg.Events.Ingress.Webhooks["broken"]
	broken.Secret = *config.NewSecureString("file://event-secret")
	cfg.Events.Ingress.Webhooks["broken"] = broken
	if err = config.SaveConfig(harness.configPath, cfg); err != nil {
		t.Fatal(err)
	}
	if err = os.Remove(secretPath); err != nil {
		t.Fatal(err)
	}
	list := harness.request(t, http.MethodGet, "/api/event-sources", nil)
	if list.Code != http.StatusOK {
		t.Fatalf("broken list = %d, body=%s", list.Code, list.Body.String())
	}
	page := decodeEventSourceList(t, list.Body.Bytes())
	if len(page.EventSources) != 1 ||
		page.EventSources[0].Status != eventSourceStatusUnreachable ||
		strings.Contains(list.Body.String(), "event-secret") {
		t.Fatalf("broken projection = %s", list.Body.String())
	}
	repaired := harness.request(
		t, http.MethodPut, "/api/event-sources/"+page.EventSources[0].ID,
		eventSourceWebhookRequest(
			page.ConfigRevision, "broken", true,
			config.EventWebhookFormatStandard, eventSourceSecretReplace,
			eventSourceTestStandardSecret('b'),
		),
	)
	if repaired.Code != http.StatusOK || strings.Contains(repaired.Body.String(), "event-secret") {
		t.Fatalf("repair status = %d, body=%s", repaired.Code, repaired.Body.String())
	}
	after := harness.request(t, http.MethodGet, "/api/event-sources", nil)
	if after.Code != http.StatusOK ||
		decodeEventSourceList(t, after.Body.Bytes()).EventSources[0].Status !=
			eventSourceStatusAvailable {
		t.Fatalf("after repair = %d, body=%s", after.Code, after.Body.String())
	}
}

func TestEventSourceMutationBoundariesAndCAS(t *testing.T) {
	resetGatewayTestState(t)
	harness := newAgentAPITestHarness(t, nil)
	revision, err := config.ConfigRevision(harness.configPath)
	if err != nil {
		t.Fatal(err)
	}
	unknownNested := `{"expected_config_revision":"` + revision +
		`","event_source":{"kind":"webhook","name":"one","enabled":false,` +
		`"format":"standard","repositories":[],"target_user":"",` +
		`"poll_notifications":false,"secret_update":"preserve","secret":"",` +
		`"unknown":true}}`
	strict := serveCollectionRaw(
		t, harness.configPath, http.MethodPost, "/api/event-sources",
		unknownNested, "application/json", nil,
	)
	if strict.Code != http.StatusBadRequest ||
		decodeCollectionErrorCode(t, strict.Body.Bytes()) != "invalid_event_source" {
		t.Fatalf("strict status = %d, body=%s", strict.Code, strict.Body.String())
	}
	body, _ := json.Marshal(eventSourceWebhookRequest(
		revision, "one", false, config.EventWebhookFormatStandard,
		eventSourceSecretPreserve, "",
	))
	crossOrigin := serveCollectionRaw(
		t, harness.configPath, http.MethodPost, "/api/event-sources",
		string(body), "application/json",
		map[string]string{
			"Origin":         "https://attacker.invalid",
			"Sec-Fetch-Site": "cross-site",
		},
	)
	if crossOrigin.Code != http.StatusForbidden ||
		decodeCollectionErrorCode(t, crossOrigin.Body.Bytes()) != "cross_origin_mutation" {
		t.Fatalf("cross origin = %d, body=%s", crossOrigin.Code, crossOrigin.Body.String())
	}

	saveCalls := 0
	harness.handler.saveConfigIfRevision = func(
		_ string, _ *config.Config, _ string,
	) (string, error) {
		saveCalls++
		return "", config.ErrConfigRevisionMismatch
	}
	cas := harness.request(
		t, http.MethodPost, "/api/event-sources",
		eventSourceWebhookRequest(
			revision, "cas", false, config.EventWebhookFormatStandard,
			eventSourceSecretPreserve, "",
		),
	)
	if cas.Code != http.StatusConflict || saveCalls != 1 ||
		decodeCollectionErrorCode(t, cas.Body.Bytes()) != "config_revision_mismatch" {
		t.Fatalf("CAS status=%d calls=%d body=%s", cas.Code, saveCalls, cas.Body.String())
	}
	loaded, _, err := config.LoadConfigForUpdateSnapshot(harness.configPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, persisted := loaded.Events.Ingress.Webhooks["cas"]; persisted {
		t.Fatal("failed CAS persisted candidate")
	}
}

func TestEventSourceConfigSaveFailureIsBounded(t *testing.T) {
	resetGatewayTestState(t)
	harness := newAgentAPITestHarness(t, nil)
	revision, _ := config.ConfigRevision(harness.configPath)
	harness.handler.saveConfigIfRevision = func(
		_ string, _ *config.Config, _ string,
	) (string, error) {
		return "", errors.New(strings.Repeat("credential diagnostic ", 100))
	}
	response := harness.request(
		t, http.MethodPost, "/api/event-sources",
		eventSourceWebhookRequest(
			revision, "failure", false, config.EventWebhookFormatStandard,
			eventSourceSecretPreserve, "",
		),
	)
	if response.Code != http.StatusInternalServerError ||
		decodeCollectionErrorCode(t, response.Body.Bytes()) != "config_save_failed" ||
		len(response.Body.String()) > 600 || strings.Contains(response.Body.String(), "credential diagnostic") {
		t.Fatalf("save failure response = %d, body=%s", response.Code, response.Body.String())
	}
}

func TestEventSourceCollectionPureFunctionCoverage(t *testing.T) {
	if got := eventSourceChannelType("missing", nil); got != "" {
		t.Fatalf("nil channel type = %q", got)
	}
	if got := eventSourceChannelType("fallback", &config.Channel{}); got != "fallback" {
		t.Fatalf("fallback channel type = %q", got)
	}

	github := config.GenericWebhookConfig{Format: config.EventWebhookFormatGitHub}
	github.Repositories = make([]string, eventSourceMaximumRepositories+1)
	if validEventSourceRepositoryScope(github) {
		t.Fatal("oversized repository scope was valid")
	}
	github.Repositories = []string{"not-a-repository"}
	if validEventSourceRepositoryScope(github) {
		t.Fatal("malformed repository scope was valid")
	}
	github.Repositories = []string{"OpenAI/Codex", "openai/codex"}
	if validEventSourceRepositoryScope(github) {
		t.Fatal("case-folded duplicate repository scope was valid")
	}
	github.Repositories = nil
	if !validEventSourceRepositoryScope(github) {
		t.Fatal("empty GitHub repository scope was invalid")
	}

	standard := config.GenericWebhookConfig{
		Enabled: true,
		Format:  config.EventWebhookFormatStandard,
	}
	if got := eventSourceWebhookStatus(true, "standard", standard); got != eventSourceStatusUnconfigured {
		t.Fatalf("unconfigured webhook status = %q", got)
	}
	standard.Secret = *config.NewSecureString("not-a-standard-secret")
	if got := eventSourceWebhookStatus(true, "standard", standard); got != eventSourceStatusInvalid {
		t.Fatalf("invalid-secret webhook status = %q", got)
	}
	github = config.GenericWebhookConfig{
		Enabled:           true,
		Format:            config.EventWebhookFormatGitHub,
		PollNotifications: true,
	}
	if got := eventSourceWebhookStatus(true, "github", github); got != eventSourceStatusAvailable {
		t.Fatalf("poll-only webhook status = %q", got)
	}
	originalSecretStatus := eventSourceSecretStatus
	eventSourceSecretStatus = func(config.GenericWebhookConfig) config.WebhookSecretStatus {
		return config.WebhookSecretStatus("unexpected")
	}
	if got := eventSourceWebhookStatus(true, "unknown", standard); got != eventSourceStatusInvalid {
		t.Fatalf("unknown secret status = %q", got)
	}
	eventSourceSecretStatus = originalSecretStatus
	t.Cleanup(func() { eventSourceSecretStatus = originalSecretStatus })

	adapter := config.ChannelEventIngressConfig{
		Enabled: true,
		Source:  config.EventChannelSourceEmail,
		Mode:    config.EventChannelModeMirror,
	}
	if got := eventSourceChannelStatus(nil, "mailbox", adapter); got != eventSourceStatusInvalid {
		t.Fatalf("nil config channel status = %q", got)
	}
	invalidAdapter := adapter
	invalidAdapter.Mode = "invalid"
	if got := eventSourceChannelStatus(
		config.DefaultConfig(),
		"mailbox",
		invalidAdapter,
	); got != eventSourceStatusInvalid {
		t.Fatalf("invalid adapter status = %q", got)
	}
	cfg := config.DefaultConfig()
	cfg.Events.Ingress.Enabled = true
	if got := eventSourceChannelStatus(cfg, "mailbox", adapter); got != eventSourceStatusUnconfigured {
		t.Fatalf("missing channel status = %q", got)
	}
	mailbox := *cfg.Channels.Get(config.ChannelDeltaChat)
	mailbox.Enabled = false
	mailbox.SetName("mailbox")
	cfg.Channels["mailbox"] = &mailbox
	if got := eventSourceChannelStatus(cfg, "mailbox", adapter); got != eventSourceStatusUnreachable {
		t.Fatalf("disabled channel status = %q", got)
	}

	if _, err := projectEventSourceItems(nil); err == nil {
		t.Fatal("nil event-source config projected")
	}
	tooLong := strings.Repeat("x", collectionResourceIDIdentityMaxBytes)
	if _, err := projectEventSourceItems(&config.Config{Events: config.EventsConfig{
		Ingress: config.EventIngressConfig{Webhooks: map[string]config.GenericWebhookConfig{
			tooLong: {},
		}},
	}}); err == nil {
		t.Fatal("oversized webhook identity projected")
	}
	if _, err := projectEventSourceItems(&config.Config{Events: config.EventsConfig{
		Ingress: config.EventIngressConfig{Channels: map[string]config.ChannelEventIngressConfig{
			tooLong: {},
		}},
	}}); err == nil {
		t.Fatal("oversized channel identity projected")
	}

	item := eventSourceCollectionItem{
		Summary: eventSourceSummary{
			ID: "id", Name: "name", Kind: eventSourceKindWebhook, Enabled: true,
			Format: config.EventWebhookFormatGitHub, Status: eventSourceStatusAvailable,
			Repositories: 2, PollNotifications: true,
		},
		Webhook: &config.GenericWebhookConfig{Repositories: []string{"openai/codex"}},
		Channel: &adapter,
	}
	options := eventSourcePageOptions()
	for _, field := range []collectionquery.Field{
		"name", "kind", "enabled", "format", "status", "repositories", "poll_notifications",
	} {
		if _, ok := options.Resolve(item, field, time.Now()); !ok {
			t.Fatalf("field %q did not resolve", field)
		}
	}
	if _, ok := options.Resolve(item, "unknown", time.Now()); ok {
		t.Fatal("unknown event-source field resolved")
	}
	clone := options.Clone(item)
	clone.Webhook.Repositories[0] = "changed/repository"
	clone.Channel.Mode = config.EventChannelModeEventOnly
	if item.Webhook.Repositories[0] != "openai/codex" || item.Channel.Mode != config.EventChannelModeMirror {
		t.Fatal("page clone mutated source item")
	}

	invalidConfigPath := filepath.Join(t.TempDir(), "invalid.json")
	if err := os.WriteFile(invalidConfigPath, []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := loadEventSourceManagementConfig(invalidConfigPath); err == nil {
		t.Fatal("invalid management config loaded")
	}
	id, err := eventSourceResourceID(eventSourceKindWebhook, "found")
	if err != nil {
		t.Fatal(err)
	}
	items := []eventSourceCollectionItem{{Summary: eventSourceSummary{ID: id}}}
	if _, found := findEventSourceByID(items, "bad"); found {
		t.Fatal("invalid resource ID found")
	}
	if _, found := findEventSourceByID(items, id); !found {
		t.Fatal("existing resource ID not found")
	}
	missingID, _ := eventSourceResourceID(eventSourceKindWebhook, "missing")
	if _, found := findEventSourceByID(nil, missingID); found {
		t.Fatal("missing resource ID found")
	}
	recorder := httptest.NewRecorder()
	got, ok := eventSourceIDFromPath(recorder, nil)
	if ok || got != "" || recorder.Code != http.StatusBadRequest {
		t.Fatalf("nil path request = (%q, %t), status=%d", got, ok, recorder.Code)
	}

	eligibleCfg := config.DefaultConfig()
	second := *eligibleCfg.Channels.Get(config.ChannelDeltaChat)
	second.Type = config.ChannelDeltaChat
	second.SetName("aaa-mailbox")
	eligibleCfg.Channels["aaa-mailbox"] = &second
	eligible := eligibleEventChannelAdapters(eligibleCfg)
	if len(eligible) < 2 || eligible[0].Name != "aaa-mailbox" {
		t.Fatalf("eligible adapters not sorted: %#v", eligible)
	}
}

func TestEventSourceInputAndValidationCoverage(t *testing.T) {
	decode := func(raw string) (eventSourceCandidate, *httptest.ResponseRecorder, bool) {
		recorder := httptest.NewRecorder()
		candidate, ok := decodeEventSourceRawInput(recorder, json.RawMessage(raw))
		return candidate, recorder, ok
	}
	for name, raw := range map[string]string{
		"empty":       "",
		"null":        "null",
		"malformed":   "{",
		"unknown":     `{"kind":"unsupported"}`,
		"bad channel": `{"kind":"channel","name":"mailbox","unknown":true}`,
	} {
		t.Run(name, func(t *testing.T) {
			if _, response, ok := decode(raw); ok || response.Code < 400 {
				t.Fatalf("decode %q = ok=%t status=%d body=%s", name, ok, response.Code, response.Body.String())
			}
		})
	}
	defaultWebhook := `{"kind":"webhook","name":"defaulted","enabled":false,` +
		`"format":"","repositories":[],"target_user":"","poll_notifications":false,` +
		`"secret_update":"","secret":""}`
	candidate, response, ok := decode(defaultWebhook)
	if !ok || response.Code != http.StatusOK || candidate.Webhook == nil ||
		candidate.Webhook.Format != config.EventWebhookFormatStandard ||
		candidate.SecretUpdate != eventSourceSecretPreserve {
		t.Fatalf(
			"default webhook = %#v ok=%t status=%d body=%s",
			candidate,
			ok,
			response.Code,
			response.Body.String(),
		)
	}

	invalidUTF8 := string([]byte{0xff})
	secretTests := []struct {
		name   string
		input  eventSourceWebhookInput
		update string
	}{
		{
			name:   "preserve with secret",
			input:  eventSourceWebhookInput{Secret: "secret"},
			update: eventSourceSecretPreserve,
		},
		{name: "replace empty", update: eventSourceSecretReplace},
		{
			name:   "replace invalid utf8",
			input:  eventSourceWebhookInput{Secret: invalidUTF8},
			update: eventSourceSecretReplace,
		},
		{name: "unknown action", update: "unknown"},
	}
	for _, test := range secretTests {
		t.Run(test.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			if validateEventSourceSecretInput(recorder, test.input, test.update) || recorder.Code < 400 {
				t.Fatalf("secret input accepted: status=%d body=%s", recorder.Code, recorder.Body.String())
			}
		})
	}

	defaults := effectiveEventSourceChannelAdapter(config.ChannelEventIngressConfig{})
	if defaults.Source != config.EventChannelSourceEmail || defaults.Mode != config.EventChannelModeMirror {
		t.Fatalf("channel defaults = %#v", defaults)
	}
	if len(dummyEventSourceWebhookSecret(config.EventWebhookFormatGitHub)) < 32 {
		t.Fatal("GitHub dummy secret too short")
	}
	if !eventSourceOpaqueCredentialReference("file://secret") ||
		!eventSourceOpaqueCredentialReference("enc://secret") ||
		eventSourceOpaqueCredentialReference("literal") {
		t.Fatal("opaque credential reference classification failed")
	}

	webhook := config.GenericWebhookConfig{Enabled: true, Format: "unsupported"}
	if validateEventSourceWebhookCandidate("webhook", webhook, false) {
		t.Fatal("unsupported webhook candidate was valid")
	}
	webhook.Format = config.EventWebhookFormatStandard
	if validateEventSourceWebhookCandidate("webhook", webhook, true) {
		t.Fatal("enabled unconfigured webhook candidate was valid")
	}
	webhook.Enabled = false
	webhook.Secret = *config.NewSecureString("file://missing-secret")
	if !validateEventSourceWebhookCandidate("webhook", webhook, false) {
		t.Fatal("disabled opaque webhook candidate was invalid")
	}

	adapter := config.ChannelEventIngressConfig{
		Enabled: true,
		Source:  config.EventChannelSourceEmail,
		Mode:    config.EventChannelModeMirror,
	}
	if validateEventSourceChannelCandidate(config.DefaultConfig(), "missing", adapter) {
		t.Fatal("candidate with missing channel was valid")
	}
	cfg := config.DefaultConfig()
	mailbox := *cfg.Channels.Get(config.ChannelDeltaChat)
	mailbox.Enabled = false
	mailbox.SetName("mailbox")
	cfg.Channels["mailbox"] = &mailbox
	if validateEventSourceChannelCandidate(cfg, "mailbox", adapter) {
		t.Fatal("enabled candidate with disabled channel was valid")
	}
	adapter.Mode = "invalid"
	if validateEventSourceChannelCandidate(cfg, "mailbox", adapter) {
		t.Fatal("invalid channel body candidate was valid")
	}

	if validateEventSourceWebhookNameSet(map[string]config.GenericWebhookConfig{" bad ": {}}) {
		t.Fatal("invalid webhook name set was valid")
	}
	if validateEventSourceWebhookNameSet(map[string]config.GenericWebhookConfig{"Alpha": {}, "alpha": {}}) {
		t.Fatal("case-folded duplicate webhook names were valid")
	}
	if validateEventSourceCandidateConfig(nil) {
		t.Fatal("nil candidate config was valid")
	}
	invalidNames := config.DefaultConfig()
	invalidNames.Events.Ingress.Webhooks = map[string]config.GenericWebhookConfig{" bad ": {}}
	if validateEventSourceCandidateConfig(invalidNames) {
		t.Fatal("candidate config with invalid names was valid")
	}
	if _, found := findEventSourceItemForMutation(&config.Config{Events: config.EventsConfig{
		Ingress: config.EventIngressConfig{Webhooks: map[string]config.GenericWebhookConfig{
			strings.Repeat("x", collectionResourceIDIdentityMaxBytes): {},
		}},
	}}, "missing"); found {
		t.Fatal("mutation lookup found unprojectable source")
	}
}

func TestEventSourceMutationHelperCoverage(t *testing.T) {
	webhookCandidate := func(name string) eventSourceCandidate {
		return eventSourceCandidate{
			Kind: eventSourceKindWebhook,
			Name: name,
			Webhook: &config.GenericWebhookConfig{
				Enabled: false,
				Format:  config.EventWebhookFormatStandard,
			},
			SecretUpdate: eventSourceSecretPreserve,
		}
	}
	channelCandidate := func(name string) eventSourceCandidate {
		return eventSourceCandidate{
			Kind: eventSourceKindChannel,
			Name: name,
			Channel: &config.ChannelEventIngressConfig{
				Enabled: false,
				Source:  config.EventChannelSourceEmail,
				Mode:    config.EventChannelModeMirror,
			},
		}
	}
	applyFails := func(
		cfg *config.Config,
		candidate eventSourceCandidate,
		existing *eventSourceCollectionItem,
		code string,
	) {
		t.Helper()
		recorder := httptest.NewRecorder()
		if id, ok := applyEventSourceCandidate(recorder, cfg, candidate, existing); ok || id != "" {
			t.Fatalf("candidate unexpectedly applied: id=%q", id)
		}
		if got := decodeCollectionErrorCode(t, recorder.Body.Bytes()); got != code {
			t.Fatalf("candidate error = %q, want %q; body=%s", got, code, recorder.Body.String())
		}
	}

	kindMismatch := eventSourceCollectionItem{Summary: eventSourceSummary{Kind: eventSourceKindChannel}}
	applyFails(
		config.DefaultConfig(), webhookCandidate("kind-mismatch"), &kindMismatch,
		"event_source_kind_mismatch",
	)

	preserveCfg := config.DefaultConfig()
	preserved := config.GenericWebhookConfig{
		Enabled: false,
		Format:  config.EventWebhookFormatStandard,
		Secret:  *config.NewSecureString(eventSourceTestStandardSecret('p')),
	}
	preserveCfg.Events.Ingress.Webhooks = map[string]config.GenericWebhookConfig{"preserve": preserved}
	preserveExisting := eventSourceCollectionItem{
		ConfigName: "preserve",
		Summary:    eventSourceSummary{Kind: eventSourceKindWebhook},
		Webhook:    &preserved,
	}
	recorder := httptest.NewRecorder()
	if _, ok := applyEventSourceCandidate(
		recorder, preserveCfg, webhookCandidate("preserve"), &preserveExisting,
	); !ok {
		t.Fatalf("preserve candidate failed: status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	preservedResult := preserveCfg.Events.Ingress.Webhooks["preserve"]
	if preservedResult.Secret.String() == "" {
		t.Fatal("preserve candidate cleared secret")
	}

	caseFoldCfg := config.DefaultConfig()
	caseFoldCfg.Events.Ingress.Webhooks = map[string]config.GenericWebhookConfig{"Alpha": {}}
	applyFails(caseFoldCfg, webhookCandidate("alpha"), nil, "event_source_exists")
	exactCfg := config.DefaultConfig()
	exactCfg.Events.Ingress.Webhooks = map[string]config.GenericWebhookConfig{"exact": {}}
	applyFails(exactCfg, webhookCandidate("exact"), nil, "event_source_exists")
	updateCollisionCfg := config.DefaultConfig()
	updateCollisionCfg.Events.Ingress.Webhooks = map[string]config.GenericWebhookConfig{
		"old": {}, "new": {},
	}
	oldWebhook := updateCollisionCfg.Events.Ingress.Webhooks["old"]
	updateCollision := eventSourceCollectionItem{
		ConfigName: "old", Summary: eventSourceSummary{Kind: eventSourceKindWebhook},
		Webhook: &oldWebhook,
	}
	applyFails(
		updateCollisionCfg, webhookCandidate("new"), &updateCollision,
		"event_source_exists",
	)

	applyFails(
		config.DefaultConfig(), channelCandidate("missing"), nil,
		"invalid_event_source",
	)
	channelCfg := config.DefaultConfig()
	channelCfg.Events.Ingress.Channels = map[string]config.ChannelEventIngressConfig{
		config.ChannelDeltaChat: {},
	}
	applyFails(
		channelCfg, channelCandidate(config.ChannelDeltaChat), nil,
		"event_source_exists",
	)
	channelUpdateCfg := config.DefaultConfig()
	channelUpdateCfg.Events.Ingress.Channels = map[string]config.ChannelEventIngressConfig{
		"old": {}, config.ChannelDeltaChat: {},
	}
	oldChannel := channelUpdateCfg.Events.Ingress.Channels["old"]
	channelExisting := eventSourceCollectionItem{
		ConfigName: "old", Summary: eventSourceSummary{Kind: eventSourceKindChannel},
		Channel: &oldChannel,
	}
	applyFails(
		channelUpdateCfg, channelCandidate(config.ChannelDeltaChat), &channelExisting,
		"event_source_exists",
	)

	invalidAggregate := config.DefaultConfig()
	invalidAggregate.Events.Ingress.Webhooks = map[string]config.GenericWebhookConfig{" bad ": {}}
	applyFails(
		invalidAggregate, webhookCandidate("valid"), nil,
		"invalid_event_source_configuration",
	)
	oversizedKind := webhookCandidate("valid")
	oversizedKind.Kind = strings.Repeat("k", collectionResourceIDIdentityMaxBytes)
	applyFails(
		config.DefaultConfig(), oversizedKind, nil,
		"invalid_event_source",
	)

	missingID, _ := eventSourceResourceID(eventSourceKindWebhook, "missing-detail")
	if detail, found := eventSourceMutationDetail(config.DefaultConfig(), missingID); found || detail != nil {
		t.Fatalf("missing mutation detail = %#v, found=%t", detail, found)
	}
}

func TestEventSourceCandidateCredentialValidationCoverage(t *testing.T) {
	literalSecret := strings.Repeat("g", 32)
	identityConflict := config.DefaultConfig()
	identityConflict.Events.Ingress.Webhooks = map[string]config.GenericWebhookConfig{
		"conflict": {
			Format:     config.EventWebhookFormatGitHub,
			TargetUser: literalSecret,
			Secret:     *config.NewSecureString(literalSecret),
		},
	}
	if validateEventSourceCandidateConfig(identityConflict) {
		t.Fatal("secret-bearing public identity passed candidate validation")
	}

	newReferencedConfig := func(t *testing.T, removeSecret bool) *config.Config {
		t.Helper()
		harness := newAgentAPITestHarness(t, nil)
		secretPath := filepath.Join(filepath.Dir(harness.configPath), "candidate-secret")
		if err := os.WriteFile(secretPath, []byte(literalSecret), 0o600); err != nil {
			t.Fatal(err)
		}
		cfg, _, err := config.LoadConfigForUpdateSnapshot(harness.configPath)
		if err != nil {
			t.Fatal(err)
		}
		cfg.Events.Ingress.Enabled = true
		cfg.Events.Ingress.Webhooks = map[string]config.GenericWebhookConfig{
			"referenced": {
				Enabled: true,
				Format:  config.EventWebhookFormatGitHub,
				Secret:  *config.NewSecureString("file://candidate-secret"),
			},
		}
		if err = config.SaveConfig(harness.configPath, cfg); err != nil {
			t.Fatal(err)
		}
		if removeSecret {
			if err = os.Remove(secretPath); err != nil {
				t.Fatal(err)
			}
		}
		loaded, _, err := config.LoadConfigForUpdateSnapshot(harness.configPath)
		if err != nil {
			t.Fatal(err)
		}
		webhook := loaded.Events.Ingress.Webhooks["referenced"]
		webhook.TargetUser = literalSecret
		loaded.Events.Ingress.Webhooks["referenced"] = webhook
		return loaded
	}

	if validateEventSourceCandidateConfig(newReferencedConfig(t, true)) {
		t.Fatal("unreachable referenced secret passed candidate validation")
	}
	if validateEventSourceCandidateConfig(newReferencedConfig(t, false)) {
		t.Fatal("resolved secret-bearing identity passed candidate validation")
	}
}

func TestEventSourceHandlerFailureAndBulkCoverage(t *testing.T) {
	requireCode := func(response *httptest.ResponseRecorder, status int, code string) {
		t.Helper()
		if response.Code != status || decodeCollectionErrorCode(t, response.Body.Bytes()) != code {
			t.Fatalf(
				"response = %d/%q, want %d/%q; body=%s",
				response.Code,
				decodeCollectionErrorCode(t, response.Body.Bytes()),
				status,
				code,
				response.Body.String(),
			)
		}
	}
	rawRequest := func(
		harness *agentAPITestHarness,
		method string,
		path string,
		body string,
	) *httptest.ResponseRecorder {
		t.Helper()
		request := httptest.NewRequest(method, path, strings.NewReader(body))
		request.Header.Set("Content-Type", "application/json")
		response := httptest.NewRecorder()
		harness.mux.ServeHTTP(response, request)
		return response
	}
	fixture := func(cfg *config.Config) {
		cfg.Events.Ingress.Enabled = false
		cfg.Events.Ingress.Webhooks = map[string]config.GenericWebhookConfig{
			"source": {Enabled: false, Format: config.EventWebhookFormatStandard},
		}
	}

	base := newAgentAPITestHarness(t, fixture)
	revision, err := config.ConfigRevision(base.configPath)
	if err != nil {
		t.Fatal(err)
	}
	sourceID, _ := eventSourceResourceID(eventSourceKindWebhook, "source")
	missingID, _ := eventSourceResourceID(eventSourceKindWebhook, "missing")
	validUpdate := eventSourceWebhookRequest(
		revision,
		"source",
		false,
		config.EventWebhookFormatStandard,
		eventSourceSecretPreserve,
		"",
	)

	requireCode(
		base.request(t, http.MethodGet, "/api/event-sources/"+sourceID+"?unexpected=1", nil),
		http.StatusBadRequest,
		"invalid_collection_request",
	)
	requireCode(
		base.request(t, http.MethodGet, "/api/event-sources/bad", nil),
		http.StatusBadRequest,
		"invalid_event_source_id",
	)
	requireCode(
		base.request(t, http.MethodGet, "/api/event-sources/"+missingID, nil),
		http.StatusNotFound,
		"event_source_not_found",
	)
	requireCode(
		base.request(t, http.MethodGet, "/api/event-source-settings?unexpected=1", nil),
		http.StatusBadRequest,
		"invalid_collection_request",
	)

	projection := newAgentAPITestHarness(t, func(cfg *config.Config) {
		cfg.Events.Ingress.Webhooks = map[string]config.GenericWebhookConfig{
			strings.Repeat("x", collectionResourceIDIdentityMaxBytes): {},
		}
	})
	requireCode(
		projection.request(t, http.MethodGet, "/api/event-sources", nil),
		http.StatusInternalServerError,
		"event_source_projection_failed",
	)
	requireCode(
		projection.request(t, http.MethodGet, "/api/event-sources/"+missingID, nil),
		http.StatusInternalServerError,
		"event_source_projection_failed",
	)

	malformed := rawRequest(base, http.MethodPost, "/api/event-sources", "{")
	requireCode(malformed, http.StatusBadRequest, "invalid_collection_request")
	requireCode(
		base.request(
			t,
			http.MethodPost,
			"/api/event-sources?unexpected=1",
			eventSourceWebhookRequest(
				revision,
				"new",
				false,
				config.EventWebhookFormatStandard,
				eventSourceSecretPreserve,
				"",
			),
		),
		http.StatusBadRequest,
		"invalid_collection_request",
	)
	requireCode(
		base.request(
			t,
			http.MethodPost,
			"/api/event-sources",
			eventSourceWebhookRequest(
				"",
				"new",
				false,
				config.EventWebhookFormatStandard,
				eventSourceSecretPreserve,
				"",
			),
		),
		http.StatusPreconditionRequired,
		"config_revision_required",
	)

	broken := newAgentAPITestHarness(t, nil)
	if err = os.WriteFile(broken.configPath, []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	brokenCreate := eventSourceWebhookRequest(
		"revision",
		"new",
		false,
		config.EventWebhookFormatStandard,
		eventSourceSecretPreserve,
		"",
	)
	for _, request := range []struct {
		method  string
		path    string
		payload any
	}{
		{method: http.MethodPost, path: "/api/event-sources", payload: brokenCreate},
		{method: http.MethodPut, path: "/api/event-sources/" + sourceID, payload: validUpdate},
		{
			method: http.MethodDelete,
			path:   "/api/event-sources/" + sourceID,
			payload: eventSourceRevisionRequest{
				ExpectedConfigRevision: "revision",
			},
		},
		{
			method: http.MethodPost,
			path:   "/api/event-sources/bulk-delete",
			payload: collectionBulkDeleteRequest{
				IDs:            []string{sourceID},
				ConfigRevision: "revision",
			},
		},
	} {
		requireCode(
			broken.request(t, request.method, request.path, request.payload),
			http.StatusInternalServerError,
			"config_load_failed",
		)
	}

	requireCode(
		base.request(t, http.MethodPut, "/api/event-sources/"+sourceID+"?unexpected=1", validUpdate),
		http.StatusBadRequest,
		"invalid_collection_request",
	)
	requireCode(
		base.request(t, http.MethodPut, "/api/event-sources/bad", validUpdate),
		http.StatusBadRequest,
		"invalid_event_source_id",
	)
	missingRevisionUpdate := eventSourceWebhookRequest(
		"",
		"source",
		false,
		config.EventWebhookFormatStandard,
		eventSourceSecretPreserve,
		"",
	)
	requireCode(
		base.request(t, http.MethodPut, "/api/event-sources/"+sourceID, missingRevisionUpdate),
		http.StatusPreconditionRequired,
		"config_revision_required",
	)
	missingUpdate := eventSourceWebhookRequest(
		revision,
		"missing",
		false,
		config.EventWebhookFormatStandard,
		eventSourceSecretPreserve,
		"",
	)
	requireCode(
		base.request(t, http.MethodPut, "/api/event-sources/"+missingID, missingUpdate),
		http.StatusNotFound,
		"event_source_not_found",
	)

	updateSaveFailure := newAgentAPITestHarness(t, fixture)
	updateRevision, _ := config.ConfigRevision(updateSaveFailure.configPath)
	updateSaveFailure.handler.saveConfigIfRevision = func(
		_ string,
		_ *config.Config,
		_ string,
	) (string, error) {
		return "", errors.New("save failed")
	}
	requireCode(
		updateSaveFailure.request(
			t,
			http.MethodPut,
			"/api/event-sources/"+sourceID,
			eventSourceWebhookRequest(
				updateRevision,
				"source",
				false,
				config.EventWebhookFormatStandard,
				eventSourceSecretPreserve,
				"",
			),
		),
		http.StatusInternalServerError,
		"config_save_failed",
	)

	updateProjectionFailure := newAgentAPITestHarness(t, fixture)
	updateProjectionRevision, _ := config.ConfigRevision(updateProjectionFailure.configPath)
	updateProjectionFailure.handler.saveConfigIfRevision = func(
		_ string,
		cfg *config.Config,
		_ string,
	) (string, error) {
		delete(cfg.Events.Ingress.Webhooks, "source")
		return "next", nil
	}
	requireCode(
		updateProjectionFailure.request(
			t,
			http.MethodPut,
			"/api/event-sources/"+sourceID,
			eventSourceWebhookRequest(
				updateProjectionRevision,
				"source",
				false,
				config.EventWebhookFormatStandard,
				eventSourceSecretPreserve,
				"",
			),
		),
		http.StatusInternalServerError,
		"event_source_projection_failed",
	)

	createProjectionFailure := newAgentAPITestHarness(t, nil)
	createRevision, _ := config.ConfigRevision(createProjectionFailure.configPath)
	createProjectionFailure.handler.saveConfigIfRevision = func(
		_ string,
		cfg *config.Config,
		_ string,
	) (string, error) {
		delete(cfg.Events.Ingress.Webhooks, "created")
		return "next", nil
	}
	requireCode(
		createProjectionFailure.request(
			t,
			http.MethodPost,
			"/api/event-sources",
			eventSourceWebhookRequest(
				createRevision,
				"created",
				false,
				config.EventWebhookFormatStandard,
				eventSourceSecretPreserve,
				"",
			),
		),
		http.StatusInternalServerError,
		"event_source_projection_failed",
	)

	requireCode(
		base.request(t, http.MethodDelete, "/api/event-sources/"+sourceID+"?unexpected=1", nil),
		http.StatusBadRequest,
		"invalid_collection_request",
	)
	requireCode(
		base.request(t, http.MethodDelete, "/api/event-sources/bad", nil),
		http.StatusBadRequest,
		"invalid_event_source_id",
	)
	requireCode(
		rawRequest(base, http.MethodDelete, "/api/event-sources/"+sourceID, "{"),
		http.StatusBadRequest,
		"invalid_collection_request",
	)
	requireCode(
		base.request(
			t,
			http.MethodDelete,
			"/api/event-sources/"+sourceID,
			eventSourceRevisionRequest{},
		),
		http.StatusPreconditionRequired,
		"config_revision_required",
	)
	requireCode(
		base.request(
			t,
			http.MethodDelete,
			"/api/event-sources/"+missingID,
			eventSourceRevisionRequest{ExpectedConfigRevision: revision},
		),
		http.StatusNotFound,
		"event_source_not_found",
	)

	deleteInvalid := newAgentAPITestHarness(t, func(cfg *config.Config) {
		fixture(cfg)
		cfg.Events.Ingress.Webhooks[" bad "] = config.GenericWebhookConfig{}
	})
	deleteInvalidRevision, _ := config.ConfigRevision(deleteInvalid.configPath)
	requireCode(
		deleteInvalid.request(
			t,
			http.MethodDelete,
			"/api/event-sources/"+sourceID,
			eventSourceRevisionRequest{ExpectedConfigRevision: deleteInvalidRevision},
		),
		http.StatusUnprocessableEntity,
		"invalid_event_source_configuration",
	)

	deleteSaveFailure := newAgentAPITestHarness(t, fixture)
	deleteSaveRevision, _ := config.ConfigRevision(deleteSaveFailure.configPath)
	deleteSaveFailure.handler.saveConfigIfRevision = func(
		_ string,
		_ *config.Config,
		_ string,
	) (string, error) {
		return "", errors.New("save failed")
	}
	requireCode(
		deleteSaveFailure.request(
			t,
			http.MethodDelete,
			"/api/event-sources/"+sourceID,
			eventSourceRevisionRequest{ExpectedConfigRevision: deleteSaveRevision},
		),
		http.StatusInternalServerError,
		"config_save_failed",
	)

	requireCode(
		base.request(t, http.MethodPost, "/api/event-sources/bulk-delete?unexpected=1", nil),
		http.StatusBadRequest,
		"invalid_collection_request",
	)
	requireCode(
		rawRequest(base, http.MethodPost, "/api/event-sources/bulk-delete", "{"),
		http.StatusBadRequest,
		"invalid_collection_request",
	)
	requireCode(
		base.request(
			t,
			http.MethodPost,
			"/api/event-sources/bulk-delete",
			collectionBulkDeleteRequest{},
		),
		http.StatusBadRequest,
		"invalid_bulk_delete",
	)
	requireCode(
		base.request(
			t,
			http.MethodPost,
			"/api/event-sources/bulk-delete",
			collectionBulkDeleteRequest{
				IDs:                    []string{sourceID},
				ConfigRevision:         revision,
				ExpectedConfigRevision: "different",
			},
		),
		http.StatusBadRequest,
		"conflicting_config_revision",
	)
	requireCode(
		base.request(
			t,
			http.MethodPost,
			"/api/event-sources/bulk-delete",
			collectionBulkDeleteRequest{IDs: []string{sourceID}},
		),
		http.StatusPreconditionRequired,
		"config_revision_required",
	)
	projectionRevision, err := config.ConfigRevision(projection.configPath)
	if err != nil {
		t.Fatal(err)
	}
	requireCode(
		projection.request(
			t,
			http.MethodPost,
			"/api/event-sources/bulk-delete",
			collectionBulkDeleteRequest{
				IDs:            []string{missingID},
				ConfigRevision: projectionRevision,
			},
		),
		http.StatusInternalServerError,
		"event_source_projection_failed",
	)

	bulkInvalid := newAgentAPITestHarness(t, func(cfg *config.Config) {
		fixture(cfg)
		cfg.Events.Ingress.Webhooks[" bad "] = config.GenericWebhookConfig{}
	})
	bulkInvalidRevision, _ := config.ConfigRevision(bulkInvalid.configPath)
	requireCode(
		bulkInvalid.request(
			t,
			http.MethodPost,
			"/api/event-sources/bulk-delete",
			collectionBulkDeleteRequest{
				IDs:            []string{sourceID},
				ConfigRevision: bulkInvalidRevision,
			},
		),
		http.StatusUnprocessableEntity,
		"invalid_event_source_configuration",
	)

	bulkSaveFailure := newAgentAPITestHarness(t, fixture)
	bulkSaveRevision, _ := config.ConfigRevision(bulkSaveFailure.configPath)
	bulkSaveFailure.handler.saveConfigIfRevision = func(
		_ string,
		_ *config.Config,
		_ string,
	) (string, error) {
		return "", errors.New("save failed")
	}
	requireCode(
		bulkSaveFailure.request(
			t,
			http.MethodPost,
			"/api/event-sources/bulk-delete",
			collectionBulkDeleteRequest{
				IDs:            []string{sourceID},
				ConfigRevision: bulkSaveRevision,
			},
		),
		http.StatusInternalServerError,
		"config_save_failed",
	)

	bulkSuccess := newAgentAPITestHarness(t, fixture)
	bulkSuccessRevision, _ := config.ConfigRevision(bulkSuccess.configPath)
	response := bulkSuccess.request(
		t,
		http.MethodPost,
		"/api/event-sources/bulk-delete",
		collectionBulkDeleteRequest{
			IDs:            []string{sourceID},
			ConfigRevision: bulkSuccessRevision,
		},
	)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"failures":[]`) {
		t.Fatalf("all-success bulk = %d, body=%s", response.Code, response.Body.String())
	}
}

func TestEventSourceSettingsHandlerFailureCoverage(t *testing.T) {
	requireCode := func(response *httptest.ResponseRecorder, status int, code string) {
		t.Helper()
		if response.Code != status || decodeCollectionErrorCode(t, response.Body.Bytes()) != code {
			t.Fatalf(
				"response = %d/%q, want %d/%q; body=%s",
				response.Code,
				decodeCollectionErrorCode(t, response.Body.Bytes()),
				status,
				code,
				response.Body.String(),
			)
		}
	}
	rawRequest := func(
		harness *agentAPITestHarness,
		path string,
		body string,
	) *httptest.ResponseRecorder {
		t.Helper()
		request := httptest.NewRequest(http.MethodPut, path, strings.NewReader(body))
		request.Header.Set("Content-Type", "application/json")
		response := httptest.NewRecorder()
		harness.mux.ServeHTTP(response, request)
		return response
	}

	harness := newAgentAPITestHarness(t, nil)
	revision, err := config.ConfigRevision(harness.configPath)
	if err != nil {
		t.Fatal(err)
	}
	settings := eventSourceSettings{
		Enabled:         false,
		DatabasePath:    "events.db",
		RetentionDays:   1,
		MaxPayloadBytes: 1024,
	}
	validRequest := eventSourceSettingsMutationRequest{
		ExpectedConfigRevision: revision,
		EventSourceSettings:    &settings,
	}
	requireCode(
		harness.request(
			t,
			http.MethodPut,
			"/api/event-source-settings?unexpected=1",
			validRequest,
		),
		http.StatusBadRequest,
		"invalid_collection_request",
	)
	requireCode(
		rawRequest(harness, "/api/event-source-settings", "{"),
		http.StatusBadRequest,
		"invalid_collection_request",
	)
	requireCode(
		harness.request(
			t,
			http.MethodPut,
			"/api/event-source-settings",
			eventSourceSettingsMutationRequest{ExpectedConfigRevision: revision},
		),
		http.StatusBadRequest,
		"invalid_event_source_settings",
	)
	tooManyFields := settings
	tooManyFields.RedactFields = make([]string, eventSourceMaximumRedactFields+1)
	requireCode(
		harness.request(
			t,
			http.MethodPut,
			"/api/event-source-settings",
			eventSourceSettingsMutationRequest{
				ExpectedConfigRevision: revision,
				EventSourceSettings:    &tooManyFields,
			},
		),
		http.StatusUnprocessableEntity,
		"invalid_event_source_settings",
	)
	nulField := settings
	nulField.RedactFields = []string{"bad\x00field"}
	if _, ok := normalizeEventSourceSettings(nulField); ok {
		t.Fatal("NUL redact field normalized")
	}

	broken := newAgentAPITestHarness(t, nil)
	if err = os.WriteFile(broken.configPath, []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	requireCode(
		broken.request(
			t,
			http.MethodPut,
			"/api/event-source-settings",
			eventSourceSettingsMutationRequest{
				ExpectedConfigRevision: "revision",
				EventSourceSettings:    &settings,
			},
		),
		http.StatusInternalServerError,
		"config_load_failed",
	)
	requireCode(
		harness.request(
			t,
			http.MethodPut,
			"/api/event-source-settings",
			eventSourceSettingsMutationRequest{EventSourceSettings: &settings},
		),
		http.StatusPreconditionRequired,
		"config_revision_required",
	)

	invalidCandidate := newAgentAPITestHarness(t, func(cfg *config.Config) {
		cfg.Events.Ingress.Enabled = false
		cfg.Events.Ingress.Webhooks = map[string]config.GenericWebhookConfig{
			"invalid": {Enabled: true, Format: "unsupported"},
		}
	})
	invalidRevision, _ := config.ConfigRevision(invalidCandidate.configPath)
	enabledSettings := settings
	enabledSettings.Enabled = true
	requireCode(
		invalidCandidate.request(
			t,
			http.MethodPut,
			"/api/event-source-settings",
			eventSourceSettingsMutationRequest{
				ExpectedConfigRevision: invalidRevision,
				EventSourceSettings:    &enabledSettings,
			},
		),
		http.StatusUnprocessableEntity,
		"invalid_event_source_configuration",
	)

	saveFailure := newAgentAPITestHarness(t, nil)
	saveRevision, _ := config.ConfigRevision(saveFailure.configPath)
	saveFailure.handler.saveConfigIfRevision = func(
		_ string,
		_ *config.Config,
		_ string,
	) (string, error) {
		return "", errors.New("save failed")
	}
	requireCode(
		saveFailure.request(
			t,
			http.MethodPut,
			"/api/event-source-settings",
			eventSourceSettingsMutationRequest{
				ExpectedConfigRevision: saveRevision,
				EventSourceSettings:    &settings,
			},
		),
		http.StatusInternalServerError,
		"config_save_failed",
	)
}
