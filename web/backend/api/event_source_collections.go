package api

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/sipeed/picoclaw/pkg/collectionquery"
	"github.com/sipeed/picoclaw/pkg/config"
)

const (
	eventSourceCollectionIDNamespace = "event-source"

	eventSourceKindWebhook = "webhook"
	eventSourceKindChannel = "channel"

	eventSourceStatusAvailable    = "available"
	eventSourceStatusDisabled     = "disabled"
	eventSourceStatusInvalid      = "invalid"
	eventSourceStatusUnconfigured = "unconfigured"
	eventSourceStatusUnreachable  = "unreachable"

	eventSourceFormatDeltaChat = "deltachat"

	eventSourceSecretPreserve = "preserve"
	eventSourceSecretReplace  = "replace"
	eventSourceSecretClear    = "clear"

	eventSourceMaximumNameBytes        = 256
	eventSourceMaximumRepositories     = 4096
	eventSourceMaximumRepositoryBytes  = 256
	eventSourceMaximumTargetUserBytes  = 128
	eventSourceMaximumRedactFields     = 256
	eventSourceMaximumRedactFieldBytes = 256
)

var (
	eventSourceWebhookNamePattern = regexp.MustCompile(
		`^[A-Za-z][A-Za-z0-9_-]{0,63}$`,
	)
	eventSourceRepositoryPattern = regexp.MustCompile(
		`^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$`,
	)
	eventSourceTargetUserPattern = regexp.MustCompile(
		`^[A-Za-z0-9](?:[A-Za-z0-9-]{0,126}[A-Za-z0-9])?$`,
	)
	eventSourceSecretStatus = config.EventWebhookSecretStatus
)

type eventSourceMutationRequest struct {
	ExpectedConfigRevision string          `json:"expected_config_revision"`
	EventSource            json.RawMessage `json:"event_source"`
}

type eventSourceRevisionRequest struct {
	ExpectedConfigRevision string `json:"expected_config_revision"`
}

type eventSourceSettingsMutationRequest struct {
	ExpectedConfigRevision string               `json:"expected_config_revision"`
	EventSourceSettings    *eventSourceSettings `json:"event_source_settings"`
}

type eventSourceWebhookInput struct {
	Kind              string   `json:"kind"`
	Name              string   `json:"name"`
	Enabled           bool     `json:"enabled"`
	Format            string   `json:"format"`
	Repositories      []string `json:"repositories"`
	TargetUser        string   `json:"target_user"`
	PollNotifications bool     `json:"poll_notifications"`
	SecretUpdate      string   `json:"secret_update"`
	Secret            string   `json:"secret"`
}

type eventSourceChannelInput struct {
	Kind                 string `json:"kind"`
	Name                 string `json:"name"`
	Enabled              bool   `json:"enabled"`
	Source               string `json:"source"`
	Mode                 string `json:"mode"`
	AllowUnverifiedEmail bool   `json:"allow_unverified_email"`
}

type eventSourceSummary struct {
	ID                string `json:"id"`
	Name              string `json:"name"`
	Kind              string `json:"kind"`
	Enabled           bool   `json:"enabled"`
	Format            string `json:"format"`
	Status            string `json:"status"`
	Repositories      int    `json:"repositories"`
	PollNotifications bool   `json:"poll_notifications"`
}

type eventSourceWebhookDetail struct {
	ID                string   `json:"id"`
	Name              string   `json:"name"`
	Kind              string   `json:"kind"`
	Enabled           bool     `json:"enabled"`
	Format            string   `json:"format"`
	Status            string   `json:"status"`
	Repositories      []string `json:"repositories"`
	TargetUser        string   `json:"target_user"`
	PollNotifications bool     `json:"poll_notifications"`
	SecretConfigured  bool     `json:"secret_configured"`
}

type eventSourceChannelDetail struct {
	ID                   string `json:"id"`
	Name                 string `json:"name"`
	Kind                 string `json:"kind"`
	Enabled              bool   `json:"enabled"`
	Format               string `json:"format"`
	Status               string `json:"status"`
	PollNotifications    bool   `json:"poll_notifications"`
	Source               string `json:"source"`
	Mode                 string `json:"mode"`
	AllowUnverifiedEmail bool   `json:"allow_unverified_email"`
	ChannelAvailable     bool   `json:"channel_available"`
	ChannelEnabled       bool   `json:"channel_enabled"`
	ChannelType          string `json:"channel_type"`
}

type eventSourceSettings struct {
	Enabled         bool     `json:"enabled"`
	DatabasePath    string   `json:"database_path"`
	RetentionDays   int      `json:"retention_days"`
	MaxPayloadBytes int      `json:"max_payload_bytes"`
	RedactFields    []string `json:"redact_fields"`
}

type eligibleEventChannelAdapter struct {
	Name           string `json:"name"`
	Kind           string `json:"kind"`
	Format         string `json:"format"`
	ChannelType    string `json:"channel_type"`
	ChannelEnabled bool   `json:"channel_enabled"`
}

type eventSourceCollectionItem struct {
	ConfigName string
	Summary    eventSourceSummary
	Webhook    *config.GenericWebhookConfig
	Channel    *config.ChannelEventIngressConfig
}

type eventSourceCandidate struct {
	Kind         string
	Name         string
	Webhook      *config.GenericWebhookConfig
	Channel      *config.ChannelEventIngressConfig
	SecretUpdate string
	Secret       string
}

var eventSourceCollectionSchema = mustCollectionQuerySchema(
	[]collectionquery.FieldSchema{
		{Name: "name", Type: collectionquery.TypeString, Sortable: true},
		{
			Name: "kind", Type: collectionquery.TypeEnum, Sortable: true,
			SuggestedValues: []string{eventSourceKindWebhook, eventSourceKindChannel},
		},
		{
			Name: "enabled", Type: collectionquery.TypeBoolean, Sortable: true,
			SuggestedValues: []string{"true", "false"},
		},
		{
			Name: "format", Type: collectionquery.TypeEnum, Sortable: true,
			SuggestedValues: []string{
				config.EventWebhookFormatStandard,
				config.EventWebhookFormatGitHub,
				eventSourceFormatDeltaChat,
			},
		},
		{
			Name: "status", Type: collectionquery.TypeEnum, Sortable: true,
			SuggestedValues: []string{
				eventSourceStatusAvailable,
				eventSourceStatusDisabled,
				eventSourceStatusInvalid,
				eventSourceStatusUnconfigured,
				eventSourceStatusUnreachable,
			},
		},
		{Name: "repositories", Type: collectionquery.TypeNumber, Sortable: true},
		{
			Name: "poll_notifications", Type: collectionquery.TypeBoolean, Sortable: true,
			SuggestedValues: []string{"true", "false"},
		},
	},
	[]collectionquery.SortField{{Field: "name", Direction: collectionquery.Ascending}},
)

func (h *Handler) registerEventSourceCollectionRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/event-sources", h.handleListEventSources)
	mux.HandleFunc(
		"POST /api/event-sources",
		h.requireCollectionMutationOrigin(h.handleCreateEventSource),
	)
	mux.HandleFunc(
		"POST /api/event-sources/bulk-delete",
		h.requireCollectionMutationOrigin(h.handleBulkDeleteEventSources),
	)
	mux.HandleFunc("GET /api/event-sources/{id}", h.handleGetEventSource)
	mux.HandleFunc(
		"PUT /api/event-sources/{id}",
		h.requireCollectionMutationOrigin(h.handleUpdateEventSource),
	)
	mux.HandleFunc(
		"DELETE /api/event-sources/{id}",
		h.requireCollectionMutationOrigin(h.handleDeleteEventSource),
	)
	mux.HandleFunc("GET /api/event-source-settings", h.handleGetEventSourceSettings)
	mux.HandleFunc(
		"PUT /api/event-source-settings",
		h.requireCollectionMutationOrigin(h.handleUpdateEventSourceSettings),
	)
}

func eventSourceResourceID(kind, name string) (string, error) {
	return encodeCollectionResourceID(
		eventSourceCollectionIDNamespace,
		kind+":"+name,
	)
}

func eventSourceWebhookFormat(webhook config.GenericWebhookConfig) string {
	return config.EffectiveEventWebhookFormat(webhook)
}

func cloneEventSourceWebhook(webhook config.GenericWebhookConfig) config.GenericWebhookConfig {
	webhook.Repositories = append([]string(nil), webhook.Repositories...)
	return webhook
}

func eventSourceChannelType(name string, channel *config.Channel) string {
	if channel == nil {
		return ""
	}
	if channel.Type != "" {
		return channel.Type
	}
	return name
}

func validEventSourceWebhookName(name string) bool {
	return name == strings.TrimSpace(name) &&
		len(name) <= 64 && utf8.ValidString(name) &&
		eventSourceWebhookNamePattern.MatchString(name)
}

func validEventSourceRepositoryScope(webhook config.GenericWebhookConfig) bool {
	format := eventSourceWebhookFormat(webhook)
	if format != config.EventWebhookFormatGitHub {
		return len(webhook.Repositories) == 0 && webhook.TargetUser == "" &&
			!webhook.PollNotifications
	}
	if len(webhook.Repositories) > eventSourceMaximumRepositories {
		return false
	}
	seen := make(map[string]struct{}, len(webhook.Repositories))
	for _, repository := range webhook.Repositories {
		folded := strings.ToLower(repository)
		if repository == "" || repository != strings.TrimSpace(repository) ||
			!utf8.ValidString(repository) ||
			len(repository) > eventSourceMaximumRepositoryBytes ||
			!eventSourceRepositoryPattern.MatchString(repository) {
			return false
		}
		if _, duplicate := seen[folded]; duplicate {
			return false
		}
		seen[folded] = struct{}{}
	}
	if webhook.TargetUser == "" {
		return true
	}
	return webhook.TargetUser == strings.TrimSpace(webhook.TargetUser) &&
		utf8.ValidString(webhook.TargetUser) &&
		len(webhook.TargetUser) <= eventSourceMaximumTargetUserBytes &&
		eventSourceTargetUserPattern.MatchString(webhook.TargetUser)
}

func eventSourceWebhookStatus(
	ingressEnabled bool,
	name string,
	webhook config.GenericWebhookConfig,
) string {
	format := eventSourceWebhookFormat(webhook)
	if !validEventSourceWebhookName(name) ||
		(format != config.EventWebhookFormatStandard &&
			format != config.EventWebhookFormatGitHub) ||
		!validEventSourceRepositoryScope(webhook) {
		return eventSourceStatusInvalid
	}
	if !ingressEnabled || !webhook.Enabled {
		return eventSourceStatusDisabled
	}
	switch eventSourceSecretStatus(webhook) {
	case config.EventWebhookSecretInvalid:
		return eventSourceStatusInvalid
	case config.EventWebhookSecretUnreachable:
		return eventSourceStatusUnreachable
	case config.EventWebhookSecretUnconfigured:
		if format == config.EventWebhookFormatGitHub && webhook.PollNotifications {
			return eventSourceStatusAvailable
		}
		return eventSourceStatusUnconfigured
	case config.EventWebhookSecretAvailable:
		return eventSourceStatusAvailable
	default:
		return eventSourceStatusInvalid
	}
}

func validEventSourceChannelBody(adapter config.ChannelEventIngressConfig) bool {
	return adapter.Source == config.EventChannelSourceEmail &&
		(adapter.Mode == config.EventChannelModeMirror ||
			adapter.Mode == config.EventChannelModeEventOnly)
}

func eventSourceChannelStatus(
	cfg *config.Config,
	name string,
	adapter config.ChannelEventIngressConfig,
) string {
	if cfg == nil || name == "" || name != strings.TrimSpace(name) ||
		len(name) > eventSourceMaximumNameBytes || !utf8.ValidString(name) ||
		!validEventSourceChannelBody(adapter) {
		return eventSourceStatusInvalid
	}
	if !cfg.Events.Ingress.Enabled || !adapter.Enabled {
		return eventSourceStatusDisabled
	}
	channel := cfg.Channels.Get(name)
	if channel == nil || eventSourceChannelType(name, channel) != config.ChannelDeltaChat {
		return eventSourceStatusUnconfigured
	}
	if !channel.Enabled {
		return eventSourceStatusUnreachable
	}
	return eventSourceStatusAvailable
}

func projectEventSourceItems(cfg *config.Config) ([]eventSourceCollectionItem, error) {
	if cfg == nil {
		return nil, errors.New("config is required")
	}
	items := make([]eventSourceCollectionItem, 0,
		len(cfg.Events.Ingress.Webhooks)+len(cfg.Events.Ingress.Channels))
	for configuredName, configured := range cfg.Events.Ingress.Webhooks {
		name := configuredName
		webhook := cloneEventSourceWebhook(configured)
		id, err := eventSourceResourceID(eventSourceKindWebhook, name)
		if err != nil {
			return nil, err
		}
		items = append(items, eventSourceCollectionItem{
			ConfigName: configuredName,
			Summary: eventSourceSummary{
				ID: id, Name: name, Kind: eventSourceKindWebhook,
				Enabled: webhook.Enabled, Format: eventSourceWebhookFormat(webhook),
				Status:            eventSourceWebhookStatus(cfg.Events.Ingress.Enabled, name, webhook),
				Repositories:      len(webhook.Repositories),
				PollNotifications: webhook.PollNotifications,
			},
			Webhook: &webhook,
		})
	}
	for configuredName, configured := range cfg.Events.Ingress.Channels {
		name := configuredName
		adapter := effectiveEventSourceChannelAdapter(configured)
		id, err := eventSourceResourceID(eventSourceKindChannel, name)
		if err != nil {
			return nil, err
		}
		items = append(items, eventSourceCollectionItem{
			ConfigName: configuredName,
			Summary: eventSourceSummary{
				ID: id, Name: name, Kind: eventSourceKindChannel,
				Enabled: adapter.Enabled, Format: eventSourceFormatDeltaChat,
				Status: eventSourceChannelStatus(cfg, name, adapter),
			},
			Channel: &adapter,
		})
	}
	return items, nil
}

func eventSourceQuerySchema(items []eventSourceCollectionItem) collectionquery.Schema {
	names := make([]string, 0, len(items))
	for _, item := range items {
		names = append(names, item.Summary.Name)
	}
	sort.Strings(names)
	return collectionSchemaWithSuggestions(
		eventSourceCollectionSchema,
		map[collectionquery.Field][]string{"name": names},
	)
}

func pageEventSources(
	items []eventSourceCollectionItem,
	request collectionListRequest,
) (collectionquery.PageResult[eventSourceCollectionItem], error) {
	return collectionquery.Paginate(
		items,
		request.Query,
		request.Cursor,
		request.Limit,
		request.Now,
		eventSourcePageOptions(),
	)
}

func eventSourcePageOptions() collectionquery.PageOptions[eventSourceCollectionItem] {
	return collectionquery.PageOptions[eventSourceCollectionItem]{
		ID: func(item eventSourceCollectionItem) (string, error) {
			return item.Summary.ID, nil
		},
		ValidateID: validCollectionResourceID,
		Clone: func(item eventSourceCollectionItem) eventSourceCollectionItem {
			if item.Webhook != nil {
				clone := cloneEventSourceWebhook(*item.Webhook)
				item.Webhook = &clone
			}
			if item.Channel != nil {
				clone := *item.Channel
				item.Channel = &clone
			}
			return item
		},
		Resolve: func(
			item eventSourceCollectionItem,
			field collectionquery.Field,
			_ time.Time,
		) (collectionquery.FieldValue, bool) {
			summary := item.Summary
			switch field {
			case "name":
				return collectionquery.StringValue(summary.Name), true
			case "kind":
				return collectionquery.EnumValue(summary.Kind), true
			case "enabled":
				return collectionquery.BooleanValue(summary.Enabled), true
			case "format":
				return collectionquery.EnumValue(summary.Format), true
			case "status":
				return collectionquery.EnumValue(summary.Status), true
			case "repositories":
				return collectionquery.NumberValue(float64(summary.Repositories)), true
			case "poll_notifications":
				return collectionquery.BooleanValue(summary.PollNotifications), true
			default:
				return collectionquery.FieldValue{}, false
			}
		},
	}
}

func loadEventSourceManagementConfig(
	path string,
) (*config.Config, string, error) {
	cfg, revision, err := config.LoadConfigForUpdateSnapshot(path)
	if err != nil {
		return nil, "", err
	}
	if err = cfg.Events.Ingress.ValidatePublicIdentities(
		cfg.SensitiveDataValues()...,
	); err != nil {
		return nil, "", err
	}
	return cfg, revision, nil
}

func writeEventSourceConfigLoadError(w http.ResponseWriter) {
	writeCollectionError(
		w,
		http.StatusInternalServerError,
		"config_load_failed",
		"Failed to load configuration",
		-1,
		nil,
	)
}

func findEventSourceByID(
	items []eventSourceCollectionItem,
	id string,
) (eventSourceCollectionItem, bool) {
	if !validCollectionResourceID(id) {
		return eventSourceCollectionItem{}, false
	}
	for _, item := range items {
		if item.Summary.ID == id {
			return item, true
		}
	}
	return eventSourceCollectionItem{}, false
}

func eventSourceIDFromPath(
	w http.ResponseWriter,
	r *http.Request,
) (string, bool) {
	if r == nil || !validCollectionResourceID(r.PathValue("id")) {
		writeCollectionError(
			w,
			http.StatusBadRequest,
			"invalid_event_source_id",
			"Event source ID is invalid",
			-1,
			nil,
		)
		return "", false
	}
	return r.PathValue("id"), true
}

func projectEventSourceDetail(
	cfg *config.Config,
	item eventSourceCollectionItem,
) any {
	summary := item.Summary
	if item.Webhook != nil {
		webhook := item.Webhook
		return eventSourceWebhookDetail{
			ID: summary.ID, Name: summary.Name, Kind: summary.Kind,
			Enabled: summary.Enabled, Format: summary.Format, Status: summary.Status,
			Repositories: append([]string(nil), webhook.Repositories...),
			TargetUser:   webhook.TargetUser, PollNotifications: webhook.PollNotifications,
			SecretConfigured: webhook.Secret.String() != "",
		}
	}
	adapter := item.Channel
	channel := cfg.Channels.Get(summary.Name)
	return eventSourceChannelDetail{
		ID: summary.ID, Name: summary.Name, Kind: summary.Kind,
		Enabled: summary.Enabled, Format: summary.Format, Status: summary.Status,
		Source: adapter.Source, Mode: adapter.Mode,
		AllowUnverifiedEmail: adapter.AllowUnverifiedEmail,
		ChannelAvailable:     channel != nil,
		ChannelEnabled:       channel != nil && channel.Enabled,
		ChannelType:          eventSourceChannelType(summary.Name, channel),
	}
}

func (h *Handler) handleListEventSources(w http.ResponseWriter, r *http.Request) {
	request, ok := parseCollectionListRequest(w, r, eventSourceCollectionSchema)
	if !ok {
		return
	}
	cfg, revision, err := loadEventSourceManagementConfig(h.configPath)
	if err != nil {
		writeEventSourceConfigLoadError(w)
		return
	}
	items, err := projectEventSourceItems(cfg)
	if err != nil {
		writeCollectionError(
			w, http.StatusInternalServerError, "event_source_projection_failed",
			"Failed to project event sources", -1, nil,
		)
		return
	}
	page, err := pageEventSources(items, request)
	if err != nil {
		writeCollectionPageError(w, err)
		return
	}
	summaries := make([]eventSourceSummary, len(page.Items))
	for index := range page.Items {
		summaries[index] = page.Items[index].Summary
	}
	writeCollectionJSON(w, http.StatusOK, map[string]any{
		"event_sources":   summaries,
		"total":           page.Total,
		"next_cursor":     page.NextCursor,
		"canonical_query": request.Query.Canonical(),
		"query_schema":    eventSourceQuerySchema(items),
		"config_revision": revision,
	})
}

func (h *Handler) handleGetEventSource(w http.ResponseWriter, r *http.Request) {
	if !validateCollectionQueryParameters(w, r) {
		return
	}
	id, ok := eventSourceIDFromPath(w, r)
	if !ok {
		return
	}
	cfg, revision, err := loadEventSourceManagementConfig(h.configPath)
	if err != nil {
		writeEventSourceConfigLoadError(w)
		return
	}
	items, err := projectEventSourceItems(cfg)
	if err != nil {
		writeCollectionError(
			w, http.StatusInternalServerError, "event_source_projection_failed",
			"Failed to project event source", -1, nil,
		)
		return
	}
	item, found := findEventSourceByID(items, id)
	if !found {
		writeCollectionError(
			w, http.StatusNotFound, "event_source_not_found",
			"Event source not found", -1, nil,
		)
		return
	}
	writeCollectionJSON(w, http.StatusOK, map[string]any{
		"event_source":    projectEventSourceDetail(cfg, item),
		"config_revision": revision,
	})
}

func eventSourceSettingsFromConfig(cfg *config.Config) eventSourceSettings {
	ingress := cfg.Events.Ingress
	return eventSourceSettings{
		Enabled:         ingress.Enabled,
		DatabasePath:    ingress.DatabasePath,
		RetentionDays:   ingress.RetentionDays,
		MaxPayloadBytes: ingress.MaxPayloadBytes,
		RedactFields:    append([]string(nil), ingress.RedactFields...),
	}
}

func eligibleEventChannelAdapters(cfg *config.Config) []eligibleEventChannelAdapter {
	eligible := make([]eligibleEventChannelAdapter, 0)
	for name, channel := range cfg.Channels {
		if channel == nil ||
			eventSourceChannelType(name, channel) != config.ChannelDeltaChat {
			continue
		}
		if _, configured := cfg.Events.Ingress.Channels[name]; configured {
			continue
		}
		eligible = append(eligible, eligibleEventChannelAdapter{
			Name: name, Kind: eventSourceKindChannel,
			Format:         eventSourceFormatDeltaChat,
			ChannelType:    config.ChannelDeltaChat,
			ChannelEnabled: channel.Enabled,
		})
	}
	sort.Slice(eligible, func(i, j int) bool {
		return eligible[i].Name < eligible[j].Name
	})
	return eligible
}

func (h *Handler) handleGetEventSourceSettings(w http.ResponseWriter, r *http.Request) {
	if !validateCollectionQueryParameters(w, r) {
		return
	}
	cfg, revision, err := loadEventSourceManagementConfig(h.configPath)
	if err != nil {
		writeEventSourceConfigLoadError(w)
		return
	}
	writeCollectionJSON(w, http.StatusOK, map[string]any{
		"event_source_settings":     eventSourceSettingsFromConfig(cfg),
		"eligible_channel_adapters": eligibleEventChannelAdapters(cfg),
		"config_revision":           revision,
	})
}

func decodeEventSourceRawInput(
	w http.ResponseWriter,
	raw json.RawMessage,
) (eventSourceCandidate, bool) {
	if len(raw) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		writeCollectionError(
			w, http.StatusBadRequest, "invalid_event_source",
			"An event_source object is required", -1, nil,
		)
		return eventSourceCandidate{}, false
	}
	var discriminator struct {
		Kind string `json:"kind"`
	}
	if err := json.Unmarshal(raw, &discriminator); err != nil {
		writeCollectionError(
			w, http.StatusBadRequest, "invalid_event_source",
			"Event source body is invalid", -1, nil,
		)
		return eventSourceCandidate{}, false
	}
	kind := strings.ToLower(strings.TrimSpace(discriminator.Kind))
	switch kind {
	case eventSourceKindWebhook:
		var input eventSourceWebhookInput
		if !decodeEventSourceDiscriminatedBody(raw, &input) {
			writeCollectionError(
				w, http.StatusBadRequest, "invalid_event_source",
				"Webhook event source body is invalid", -1, nil,
			)
			return eventSourceCandidate{}, false
		}
		name := input.Name
		format := strings.ToLower(strings.TrimSpace(input.Format))
		if format == "" {
			format = config.EventWebhookFormatStandard
		}
		secretUpdate := strings.ToLower(strings.TrimSpace(input.SecretUpdate))
		if secretUpdate == "" {
			secretUpdate = eventSourceSecretPreserve
		}
		webhook := config.GenericWebhookConfig{
			Enabled: input.Enabled, Format: format,
			Repositories:      append([]string(nil), input.Repositories...),
			TargetUser:        input.TargetUser,
			PollNotifications: input.PollNotifications,
		}
		return eventSourceCandidate{
			Kind: kind, Name: name, Webhook: &webhook,
			SecretUpdate: secretUpdate, Secret: input.Secret,
		}, validateEventSourceSecretInput(w, input, secretUpdate)
	case eventSourceKindChannel:
		var input eventSourceChannelInput
		if !decodeEventSourceDiscriminatedBody(raw, &input) {
			writeCollectionError(
				w, http.StatusBadRequest, "invalid_event_source",
				"Channel event source body is invalid", -1, nil,
			)
			return eventSourceCandidate{}, false
		}
		adapter := effectiveEventSourceChannelAdapter(config.ChannelEventIngressConfig{
			Enabled:              input.Enabled,
			Source:               strings.ToLower(strings.TrimSpace(input.Source)),
			Mode:                 strings.ToLower(strings.TrimSpace(input.Mode)),
			AllowUnverifiedEmail: input.AllowUnverifiedEmail,
		})
		return eventSourceCandidate{
			Kind: kind, Name: input.Name, Channel: &adapter,
		}, true
	default:
		writeCollectionError(
			w, http.StatusUnprocessableEntity, "invalid_event_source_kind",
			"Event source kind must be webhook or channel", -1, nil,
		)
		return eventSourceCandidate{}, false
	}
}

func decodeEventSourceDiscriminatedBody(raw []byte, target any) bool {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return false
	}
	var trailing json.RawMessage
	return errors.Is(decoder.Decode(&trailing), io.EOF)
}

func validateEventSourceSecretInput(
	w http.ResponseWriter,
	input eventSourceWebhookInput,
	secretUpdate string,
) bool {
	switch secretUpdate {
	case eventSourceSecretPreserve:
		if input.Secret != "" {
			writeCollectionError(
				w, http.StatusUnprocessableEntity, "invalid_secret_update",
				"A secret is accepted only when secret_update is replace", -1, nil,
			)
			return false
		}
	case eventSourceSecretReplace:
		if input.Secret == "" || !utf8.ValidString(input.Secret) {
			writeCollectionError(
				w, http.StatusUnprocessableEntity, "invalid_secret_update",
				"A valid non-empty replacement secret is required", -1, nil,
			)
			return false
		}
	case eventSourceSecretClear:
		if input.Enabled || input.Secret != "" {
			writeCollectionError(
				w, http.StatusUnprocessableEntity, "invalid_secret_update",
				"A signing secret can be cleared only while the source is disabled",
				-1, nil,
			)
			return false
		}
	default:
		writeCollectionError(
			w, http.StatusUnprocessableEntity, "invalid_secret_update",
			"secret_update must be preserve, replace, or clear", -1, nil,
		)
		return false
	}
	return true
}

func effectiveEventSourceChannelAdapter(
	adapter config.ChannelEventIngressConfig,
) config.ChannelEventIngressConfig {
	if adapter.Source == "" {
		adapter.Source = config.EventChannelSourceEmail
	}
	if adapter.Mode == "" {
		adapter.Mode = config.EventChannelModeMirror
	}
	return adapter
}

func dummyEventSourceWebhookSecret(format string) string {
	if format == config.EventWebhookFormatGitHub {
		return strings.Repeat("g", 32)
	}
	return "whsec_" + base64.StdEncoding.EncodeToString(make([]byte, 32))
}

func eventSourceOpaqueCredentialReference(value string) bool {
	return strings.HasPrefix(value, "file://") || strings.HasPrefix(value, "enc://")
}

func validateEventSourceWebhookCandidate(
	name string,
	webhook config.GenericWebhookConfig,
	validateReplacement bool,
) bool {
	if !validEventSourceWebhookName(name) || !validEventSourceRepositoryScope(webhook) {
		return false
	}
	format := eventSourceWebhookFormat(webhook)
	if format != config.EventWebhookFormatStandard &&
		format != config.EventWebhookFormatGitHub {
		return false
	}
	secret := webhook.Secret.String()
	if webhook.Enabled && secret == "" &&
		(format != config.EventWebhookFormatGitHub || !webhook.PollNotifications) {
		return false
	}
	check := cloneEventSourceWebhook(webhook)
	check.Enabled = true
	if !validateReplacement &&
		(!webhook.Enabled || eventSourceOpaqueCredentialReference(secret)) {
		check.Secret = *config.NewSecureString(dummyEventSourceWebhookSecret(format))
	}
	ingress := config.EventIngressConfig{
		Enabled:  true,
		Webhooks: map[string]config.GenericWebhookConfig{name: check},
	}
	return ingress.Validate() == nil
}

func validateEventSourceChannelCandidate(
	cfg *config.Config,
	name string,
	adapter config.ChannelEventIngressConfig,
) bool {
	if cfg == nil || name == "" || name != strings.TrimSpace(name) ||
		len(name) > eventSourceMaximumNameBytes || !utf8.ValidString(name) ||
		!validEventSourceChannelBody(adapter) {
		return false
	}
	channel := cfg.Channels.Get(name)
	if channel == nil || eventSourceChannelType(name, channel) != config.ChannelDeltaChat {
		return false
	}
	if adapter.Enabled && !channel.Enabled {
		return false
	}
	test := config.EventIngressConfig{
		Enabled:  true,
		Channels: map[string]config.ChannelEventIngressConfig{name: adapter},
	}
	return test.ValidateEventChannelAdapters(cfg.Channels) == nil
}

func validateEventSourceWebhookNameSet(webhooks map[string]config.GenericWebhookConfig) bool {
	seen := make(map[string]struct{}, len(webhooks))
	for name := range webhooks {
		if !validEventSourceWebhookName(name) {
			return false
		}
		folded := strings.ToLower(name)
		if _, duplicate := seen[folded]; duplicate {
			return false
		}
		seen[folded] = struct{}{}
	}
	return true
}

func validateEventSourceCandidateConfig(cfg *config.Config) bool {
	if cfg == nil || !validateEventSourceWebhookNameSet(cfg.Events.Ingress.Webhooks) {
		return false
	}
	if err := cfg.Events.Ingress.ValidatePublicIdentities(
		cfg.SensitiveDataValues()...,
	); err != nil {
		return false
	}
	if !cfg.Events.Ingress.Enabled {
		return true
	}
	if err := cfg.Events.Ingress.ApplyWebhookSecrets(nil); err != nil {
		return false
	}
	if err := cfg.Events.Ingress.ValidatePublicIdentities(
		cfg.SensitiveDataValues()...,
	); err != nil {
		return false
	}
	if err := cfg.Events.Ingress.Validate(); err != nil {
		return false
	}
	return cfg.Events.Ingress.ValidateEventChannelAdapters(
		cfg.Channels,
		cfg.SensitiveDataValues()...,
	) == nil
}

func findEventSourceItemForMutation(
	cfg *config.Config,
	id string,
) (eventSourceCollectionItem, bool) {
	items, err := projectEventSourceItems(cfg)
	if err != nil {
		return eventSourceCollectionItem{}, false
	}
	return findEventSourceByID(items, id)
}

func applyEventSourceCandidate(
	w http.ResponseWriter,
	cfg *config.Config,
	candidate eventSourceCandidate,
	existing *eventSourceCollectionItem,
) (string, bool) {
	if existing != nil && existing.Summary.Kind != candidate.Kind {
		writeCollectionError(
			w, http.StatusConflict, "event_source_kind_mismatch",
			"Event source kind cannot be changed", -1, nil,
		)
		return "", false
	}
	if candidate.Webhook != nil {
		webhook := cloneEventSourceWebhook(*candidate.Webhook)
		var previous config.GenericWebhookConfig
		previousConfigured := false
		if existing != nil && existing.Webhook != nil {
			previous = cloneEventSourceWebhook(*existing.Webhook)
			previousConfigured = previous.Secret.String() != ""
		}
		if existing != nil && previousConfigured &&
			eventSourceWebhookFormat(previous) != eventSourceWebhookFormat(webhook) &&
			candidate.SecretUpdate != eventSourceSecretReplace {
			writeCollectionError(
				w, http.StatusUnprocessableEntity, "secret_replacement_required",
				"Changing webhook format requires a replacement signing secret",
				-1, nil,
			)
			return "", false
		}
		switch candidate.SecretUpdate {
		case eventSourceSecretPreserve:
			if existing != nil {
				webhook.Secret = previous.Secret
			}
		case eventSourceSecretReplace:
			webhook.Secret = *config.NewSecureString(candidate.Secret)
		case eventSourceSecretClear:
			webhook.Secret = config.SecureString{}
		}
		if !validateEventSourceWebhookCandidate(
			candidate.Name,
			webhook,
			candidate.SecretUpdate == eventSourceSecretReplace,
		) {
			writeCollectionError(
				w, http.StatusUnprocessableEntity, "invalid_event_source",
				"Webhook event source is invalid", -1, nil,
			)
			return "", false
		}
		if cfg.Events.Ingress.Webhooks == nil {
			cfg.Events.Ingress.Webhooks = make(map[string]config.GenericWebhookConfig)
		}
		if existing != nil && existing.ConfigName != candidate.Name {
			delete(cfg.Events.Ingress.Webhooks, existing.ConfigName)
		}
		for name := range cfg.Events.Ingress.Webhooks {
			if strings.EqualFold(name, candidate.Name) && name != candidate.Name {
				writeCollectionError(
					w, http.StatusConflict, "event_source_exists",
					"A webhook event source with this name already exists", -1, nil,
				)
				return "", false
			}
		}
		if existing == nil {
			if _, duplicate := cfg.Events.Ingress.Webhooks[candidate.Name]; duplicate {
				writeCollectionError(
					w, http.StatusConflict, "event_source_exists",
					"A webhook event source with this name already exists", -1, nil,
				)
				return "", false
			}
		} else if _, duplicate := cfg.Events.Ingress.Webhooks[candidate.Name]; duplicate &&
			existing.ConfigName != candidate.Name {
			writeCollectionError(
				w, http.StatusConflict, "event_source_exists",
				"A webhook event source with this name already exists", -1, nil,
			)
			return "", false
		}
		cfg.Events.Ingress.Webhooks[candidate.Name] = webhook
	} else {
		adapter := effectiveEventSourceChannelAdapter(*candidate.Channel)
		if !validateEventSourceChannelCandidate(cfg, candidate.Name, adapter) {
			writeCollectionError(
				w, http.StatusUnprocessableEntity, "invalid_event_source",
				"Channel event source is invalid", -1, nil,
			)
			return "", false
		}
		if cfg.Events.Ingress.Channels == nil {
			cfg.Events.Ingress.Channels = make(map[string]config.ChannelEventIngressConfig)
		}
		if existing != nil && existing.ConfigName != candidate.Name {
			delete(cfg.Events.Ingress.Channels, existing.ConfigName)
		}
		if existing == nil {
			if _, duplicate := cfg.Events.Ingress.Channels[candidate.Name]; duplicate {
				writeCollectionError(
					w, http.StatusConflict, "event_source_exists",
					"A channel event source with this name already exists", -1, nil,
				)
				return "", false
			}
		} else if _, duplicate := cfg.Events.Ingress.Channels[candidate.Name]; duplicate &&
			existing.ConfigName != candidate.Name {
			writeCollectionError(
				w, http.StatusConflict, "event_source_exists",
				"A channel event source with this name already exists", -1, nil,
			)
			return "", false
		}
		cfg.Events.Ingress.Channels[candidate.Name] = adapter
	}
	if !validateEventSourceCandidateConfig(cfg) {
		writeCollectionError(
			w, http.StatusUnprocessableEntity, "invalid_event_source_configuration",
			"Event source configuration is invalid", -1, nil,
		)
		return "", false
	}
	id, err := eventSourceResourceID(candidate.Kind, candidate.Name)
	if err != nil {
		writeCollectionError(
			w, http.StatusUnprocessableEntity, "invalid_event_source",
			"Event source identity is invalid", -1, nil,
		)
		return "", false
	}
	return id, true
}

func decodeEventSourceMutationRequest(
	w http.ResponseWriter,
	r *http.Request,
) (eventSourceMutationRequest, eventSourceCandidate, bool) {
	var request eventSourceMutationRequest
	if !decodeCollectionJSON(w, r, &request) {
		return eventSourceMutationRequest{}, eventSourceCandidate{}, false
	}
	candidate, ok := decodeEventSourceRawInput(w, request.EventSource)
	return request, candidate, ok
}

func eventSourceMutationDetail(
	cfg *config.Config,
	id string,
) (any, bool) {
	item, found := findEventSourceItemForMutation(cfg, id)
	if !found {
		return nil, false
	}
	return projectEventSourceDetail(cfg, item), true
}

func (h *Handler) handleCreateEventSource(w http.ResponseWriter, r *http.Request) {
	if !validateCollectionQueryParameters(w, r) {
		return
	}
	request, candidate, ok := decodeEventSourceMutationRequest(w, r)
	if !ok {
		return
	}
	h.configMutationMu.Lock()
	releaseConfigMutation := sync.OnceFunc(h.configMutationMu.Unlock)
	defer releaseConfigMutation()
	cfg, revision, err := config.LoadConfigForUpdateSnapshot(h.configPath)
	if err != nil {
		writeEventSourceConfigLoadError(w)
		return
	}
	expectedRevision, ok := resolveCollectionRevision(
		w, r, request.ExpectedConfigRevision,
	)
	if !ok || !requireCollectionRevision(w, expectedRevision, revision) {
		return
	}
	id, ok := applyEventSourceCandidate(w, cfg, candidate, nil)
	if !ok {
		return
	}
	nextRevision, err := h.saveConfigIfRevision(h.configPath, cfg, revision)
	if err != nil {
		writeCollectionConfigSaveError(w, err)
		return
	}
	detail, found := eventSourceMutationDetail(cfg, id)
	if !found {
		writeCollectionError(
			w, http.StatusInternalServerError, "event_source_projection_failed",
			"Failed to project event source", -1, nil,
		)
		return
	}
	w.Header().Set("Location", "/api/event-sources/"+url.PathEscape(id))
	releaseConfigMutation()
	writeCollectionJSON(w, http.StatusCreated, map[string]any{
		"event_source":    detail,
		"config_revision": nextRevision,
		"effects":         agentEffectsForConfig(cfg),
	})
}

func (h *Handler) handleUpdateEventSource(w http.ResponseWriter, r *http.Request) {
	if !validateCollectionQueryParameters(w, r) {
		return
	}
	id, ok := eventSourceIDFromPath(w, r)
	if !ok {
		return
	}
	request, candidate, ok := decodeEventSourceMutationRequest(w, r)
	if !ok {
		return
	}
	h.configMutationMu.Lock()
	releaseConfigMutation := sync.OnceFunc(h.configMutationMu.Unlock)
	defer releaseConfigMutation()
	cfg, revision, err := config.LoadConfigForUpdateSnapshot(h.configPath)
	if err != nil {
		writeEventSourceConfigLoadError(w)
		return
	}
	expectedRevision, ok := resolveCollectionRevision(
		w, r, request.ExpectedConfigRevision,
	)
	if !ok || !requireCollectionRevision(w, expectedRevision, revision) {
		return
	}
	existing, found := findEventSourceItemForMutation(cfg, id)
	if !found {
		writeCollectionError(
			w, http.StatusNotFound, "event_source_not_found",
			"Event source not found", -1, nil,
		)
		return
	}
	if candidate.Name != existing.ConfigName {
		writeCollectionError(
			w, http.StatusConflict, "event_source_name_immutable",
			"Event source name cannot be changed", -1, nil,
		)
		return
	}
	nextID, ok := applyEventSourceCandidate(w, cfg, candidate, &existing)
	if !ok {
		return
	}
	nextRevision, err := h.saveConfigIfRevision(h.configPath, cfg, revision)
	if err != nil {
		writeCollectionConfigSaveError(w, err)
		return
	}
	detail, found := eventSourceMutationDetail(cfg, nextID)
	if !found {
		writeCollectionError(
			w, http.StatusInternalServerError, "event_source_projection_failed",
			"Failed to project event source", -1, nil,
		)
		return
	}
	releaseConfigMutation()
	writeCollectionJSON(w, http.StatusOK, map[string]any{
		"event_source":    detail,
		"config_revision": nextRevision,
		"effects":         agentEffectsForConfig(cfg),
	})
}

func deleteEventSourceItem(cfg *config.Config, item eventSourceCollectionItem) {
	if item.Summary.Kind == eventSourceKindWebhook {
		delete(cfg.Events.Ingress.Webhooks, item.ConfigName)
		return
	}
	delete(cfg.Events.Ingress.Channels, item.ConfigName)
}

func (h *Handler) handleDeleteEventSource(w http.ResponseWriter, r *http.Request) {
	if !validateCollectionQueryParameters(w, r) {
		return
	}
	id, ok := eventSourceIDFromPath(w, r)
	if !ok {
		return
	}
	var request eventSourceRevisionRequest
	if !decodeCollectionJSON(w, r, &request) {
		return
	}
	h.configMutationMu.Lock()
	releaseConfigMutation := sync.OnceFunc(h.configMutationMu.Unlock)
	defer releaseConfigMutation()
	cfg, revision, err := config.LoadConfigForUpdateSnapshot(h.configPath)
	if err != nil {
		writeEventSourceConfigLoadError(w)
		return
	}
	expectedRevision, ok := resolveCollectionRevision(
		w, r, request.ExpectedConfigRevision,
	)
	if !ok || !requireCollectionRevision(w, expectedRevision, revision) {
		return
	}
	item, found := findEventSourceItemForMutation(cfg, id)
	if !found {
		writeCollectionError(
			w, http.StatusNotFound, "event_source_not_found",
			"Event source not found", -1, nil,
		)
		return
	}
	deleteEventSourceItem(cfg, item)
	if !validateEventSourceCandidateConfig(cfg) {
		writeCollectionError(
			w, http.StatusUnprocessableEntity, "invalid_event_source_configuration",
			"Event source configuration is invalid", -1, nil,
		)
		return
	}
	nextRevision, err := h.saveConfigIfRevision(h.configPath, cfg, revision)
	if err != nil {
		writeCollectionConfigSaveError(w, err)
		return
	}
	releaseConfigMutation()
	writeCollectionJSON(w, http.StatusOK, collectionBulkDeleteResponse{
		DeletedIDs:     []string{id},
		Failures:       []collectionBulkFailure{},
		ConfigRevision: nextRevision,
		Effects:        agentEffectsForConfig(cfg),
	})
}

func (h *Handler) handleBulkDeleteEventSources(w http.ResponseWriter, r *http.Request) {
	if !validateCollectionQueryParameters(w, r) {
		return
	}
	var request collectionBulkDeleteRequest
	if !decodeCollectionJSON(w, r, &request) {
		return
	}
	if len(request.IDs) == 0 || len(request.IDs) > collectionquery.MaxPageSize {
		writeCollectionError(
			w, http.StatusBadRequest, "invalid_bulk_delete",
			"Bulk deletion requires between 1 and 200 IDs", -1, nil,
		)
		return
	}
	h.configMutationMu.Lock()
	releaseConfigMutation := sync.OnceFunc(h.configMutationMu.Unlock)
	defer releaseConfigMutation()
	cfg, revision, err := config.LoadConfigForUpdateSnapshot(h.configPath)
	if err != nil {
		writeEventSourceConfigLoadError(w)
		return
	}
	bodyRevision, ok := bulkCollectionRevision(w, request)
	if !ok {
		return
	}
	expectedRevision, ok := resolveCollectionRevision(w, r, bodyRevision)
	if !ok || !requireCollectionRevision(w, expectedRevision, revision) {
		return
	}
	items, err := projectEventSourceItems(cfg)
	if err != nil {
		writeCollectionError(
			w, http.StatusInternalServerError, "event_source_projection_failed",
			"Failed to project event sources", -1, nil,
		)
		return
	}
	byID := make(map[string]eventSourceCollectionItem, len(items))
	for _, item := range items {
		byID[item.Summary.ID] = item
	}
	requested, failures := normalizeBulkIDs(request.IDs)
	deleted := make([]string, 0, len(requested))
	for _, id := range requested {
		if !validCollectionResourceID(id) {
			failures = append(failures, collectionBulkFailure{ID: id, Code: "invalid_id"})
			continue
		}
		item, found := byID[id]
		if !found {
			failures = append(failures, collectionBulkFailure{ID: id, Code: "not_found"})
			continue
		}
		deleteEventSourceItem(cfg, item)
		deleted = append(deleted, id)
	}
	nextRevision := revision
	if len(deleted) > 0 {
		if !validateEventSourceCandidateConfig(cfg) {
			writeCollectionError(
				w, http.StatusUnprocessableEntity, "invalid_event_source_configuration",
				"Event source configuration is invalid", -1, nil,
			)
			return
		}
		nextRevision, err = h.saveConfigIfRevision(h.configPath, cfg, revision)
		if err != nil {
			writeCollectionConfigSaveError(w, err)
			return
		}
	}
	sort.Strings(deleted)
	sortCollectionFailures(failures)
	releaseConfigMutation()
	writeCollectionJSON(w, http.StatusOK, collectionBulkDeleteResponse{
		DeletedIDs:     deleted,
		Failures:       failures,
		ConfigRevision: nextRevision,
		Effects:        agentEffectsForConfig(cfg),
	})
}

func normalizeEventSourceSettings(
	settings eventSourceSettings,
) (eventSourceSettings, bool) {
	settings.DatabasePath = strings.TrimSpace(settings.DatabasePath)
	if !utf8.ValidString(settings.DatabasePath) ||
		len(settings.DatabasePath) > collectionResourceIDIdentityMaxBytes ||
		strings.ContainsRune(settings.DatabasePath, 0) ||
		settings.RetentionDays < 0 || settings.MaxPayloadBytes < 0 ||
		len(settings.RedactFields) > eventSourceMaximumRedactFields {
		return eventSourceSettings{}, false
	}
	redactFields := make([]string, 0, len(settings.RedactFields))
	seen := make(map[string]struct{}, len(settings.RedactFields))
	for _, raw := range settings.RedactFields {
		field := strings.TrimSpace(raw)
		if field == "" || !utf8.ValidString(field) ||
			len(field) > eventSourceMaximumRedactFieldBytes ||
			strings.ContainsRune(field, 0) {
			return eventSourceSettings{}, false
		}
		folded := strings.ToLower(field)
		if _, duplicate := seen[folded]; duplicate {
			continue
		}
		seen[folded] = struct{}{}
		redactFields = append(redactFields, field)
	}
	settings.RedactFields = redactFields
	return settings, true
}

func (h *Handler) handleUpdateEventSourceSettings(
	w http.ResponseWriter,
	r *http.Request,
) {
	if !validateCollectionQueryParameters(w, r) {
		return
	}
	var request eventSourceSettingsMutationRequest
	if !decodeCollectionJSON(w, r, &request) {
		return
	}
	if request.EventSourceSettings == nil {
		writeCollectionError(
			w, http.StatusBadRequest, "invalid_event_source_settings",
			"An event_source_settings object is required", -1, nil,
		)
		return
	}
	settings, ok := normalizeEventSourceSettings(*request.EventSourceSettings)
	if !ok {
		writeCollectionError(
			w, http.StatusUnprocessableEntity, "invalid_event_source_settings",
			"Event source settings are invalid", -1, nil,
		)
		return
	}
	h.configMutationMu.Lock()
	releaseConfigMutation := sync.OnceFunc(h.configMutationMu.Unlock)
	defer releaseConfigMutation()
	cfg, revision, err := config.LoadConfigForUpdateSnapshot(h.configPath)
	if err != nil {
		writeEventSourceConfigLoadError(w)
		return
	}
	expectedRevision, ok := resolveCollectionRevision(
		w, r, request.ExpectedConfigRevision,
	)
	if !ok || !requireCollectionRevision(w, expectedRevision, revision) {
		return
	}
	cfg.Events.Ingress.Enabled = settings.Enabled
	cfg.Events.Ingress.DatabasePath = settings.DatabasePath
	cfg.Events.Ingress.RetentionDays = settings.RetentionDays
	cfg.Events.Ingress.MaxPayloadBytes = settings.MaxPayloadBytes
	cfg.Events.Ingress.RedactFields = append([]string(nil), settings.RedactFields...)
	if !validateEventSourceCandidateConfig(cfg) {
		writeCollectionError(
			w, http.StatusUnprocessableEntity, "invalid_event_source_configuration",
			"Event source configuration is invalid", -1, nil,
		)
		return
	}
	nextRevision, err := h.saveConfigIfRevision(h.configPath, cfg, revision)
	if err != nil {
		writeCollectionConfigSaveError(w, err)
		return
	}
	releaseConfigMutation()
	writeCollectionJSON(w, http.StatusOK, map[string]any{
		"event_source_settings":     eventSourceSettingsFromConfig(cfg),
		"eligible_channel_adapters": eligibleEventChannelAdapters(cfg),
		"config_revision":           nextRevision,
		"effects":                   agentEffectsForConfig(cfg),
	})
}
