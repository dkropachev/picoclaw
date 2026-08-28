package api

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/sipeed/picoclaw/pkg/collectionquery"
	"github.com/sipeed/picoclaw/pkg/config"
)

const collectionMutationMaxBytes = 1 << 20

type collectionBulkDeleteRequest struct {
	IDs                    []string `json:"ids"`
	ConfigRevision         string   `json:"config_revision,omitempty"`
	ExpectedConfigRevision string   `json:"expected_config_revision,omitempty"`
}

type collectionBulkFailure struct {
	ID       string   `json:"id"`
	Code     string   `json:"code"`
	Blockers []string `json:"blockers,omitempty"`
}

type collectionBulkDeleteResponse struct {
	DeletedIDs      []string                `json:"deleted_ids"`
	Failures        []collectionBulkFailure `json:"failures"`
	CleanupFailures []collectionBulkFailure `json:"cleanup_failures,omitempty"`
	ConfigRevision  string                  `json:"config_revision,omitempty"`
	Effects         agentEffects            `json:"effects"`
}

type modelAliasMutationRequest struct {
	ExpectedConfigRevision string                   `json:"expected_config_revision"`
	ModelAlias             *config.ModelAliasConfig `json:"model_alias"`
}

type modelRouterMutationRequest struct {
	ExpectedConfigRevision string                    `json:"expected_config_revision"`
	ModelRouter            *config.ModelRouterConfig `json:"model_router"`
}

type modelAliasSummary struct {
	Name                 string `json:"name"`
	Model                string `json:"model"`
	OverrideCount        int    `json:"override_count"`
	DisabledAccountCount int    `json:"disabled_account_count"`
}

type modelRouterSummary struct {
	Name       string `json:"name"`
	Enabled    bool   `json:"enabled"`
	Entry      string `json:"entry"`
	BlockCount int    `json:"block_count"`
	RuleCount  int    `json:"rule_count"`
}

var modelAliasCollectionSchema = mustCollectionQuerySchema(
	[]collectionquery.FieldSchema{
		{Name: "name", Type: collectionquery.TypeString, Sortable: true},
		{Name: "model", Type: collectionquery.TypeString, Sortable: true},
		{Name: "overrides", Type: collectionquery.TypeNumber, Sortable: true},
		{Name: "disabled_accounts", Type: collectionquery.TypeNumber, Sortable: true},
	},
	[]collectionquery.SortField{{Field: "name", Direction: collectionquery.Ascending}},
)

var modelRouterCollectionSchema = mustCollectionQuerySchema(
	[]collectionquery.FieldSchema{
		{Name: "name", Type: collectionquery.TypeString, Sortable: true},
		{
			Name:            "enabled",
			Type:            collectionquery.TypeBoolean,
			Sortable:        true,
			SuggestedValues: []string{"true", "false"},
		},
		{Name: "blocks", Type: collectionquery.TypeNumber, Sortable: true},
		{Name: "rules", Type: collectionquery.TypeNumber, Sortable: true},
	},
	[]collectionquery.SortField{{Field: "name", Direction: collectionquery.Ascending}},
)

func (h *Handler) registerModelCollectionRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/model-aliases", h.handleListModelAliases)
	mux.HandleFunc("POST /api/model-aliases", h.requireCollectionMutationOrigin(h.handleCreateModelAliasByName))
	mux.HandleFunc(
		"POST /api/model-aliases/bulk-delete",
		h.requireCollectionMutationOrigin(h.handleBulkDeleteModelAliases),
	)
	mux.HandleFunc("GET /api/model-aliases/{name}", h.handleGetModelAliasByName)
	mux.HandleFunc("PUT /api/model-aliases/{name}", h.requireCollectionMutationOrigin(h.handleUpdateModelAliasByName))
	mux.HandleFunc(
		"DELETE /api/model-aliases/{name}",
		h.requireCollectionMutationOrigin(h.handleDeleteModelAliasByName),
	)

	mux.HandleFunc("GET /api/model-routers", h.handleListModelRouters)
	mux.HandleFunc("POST /api/model-routers", h.requireCollectionMutationOrigin(h.handleCreateModelRouterByName))
	mux.HandleFunc(
		"POST /api/model-routers/bulk-delete",
		h.requireCollectionMutationOrigin(h.handleBulkDeleteModelRouters),
	)
	mux.HandleFunc("GET /api/model-routers/{name}", h.handleGetModelRouterByName)
	mux.HandleFunc("PUT /api/model-routers/{name}", h.requireCollectionMutationOrigin(h.handleUpdateModelRouterByName))
	mux.HandleFunc(
		"DELETE /api/model-routers/{name}",
		h.requireCollectionMutationOrigin(h.handleDeleteModelRouterByName),
	)
}

func (h *Handler) requireCollectionMutationOrigin(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		switch strings.ToLower(strings.TrimSpace(r.Header.Get("Sec-Fetch-Site"))) {
		case "", "none", "same-origin":
		default:
			writeCollectionError(
				w,
				http.StatusForbidden,
				"cross_origin_mutation",
				"Cross-origin collection mutations are not allowed",
				0,
				nil,
			)
			return
		}
		if rawOrigin := strings.TrimSpace(r.Header.Get("Origin")); rawOrigin != "" {
			origin, err := url.Parse(rawOrigin)
			if err != nil || origin.Scheme == "" || origin.Host == "" ||
				!strings.EqualFold(origin.Host, mcpRequestHost(h, r)) ||
				!strings.EqualFold(origin.Scheme, mcpRequestScheme(h, r)) {
				writeCollectionError(
					w,
					http.StatusForbidden,
					"cross_origin_mutation",
					"Cross-origin collection mutations are not allowed",
					0,
					nil,
				)
				return
			}
		}
		next(w, r)
	}
}

func writeCollectionError(w http.ResponseWriter, status int, code, message string, position int, blockers []string) {
	message = boundedCollectionMessage(message, 512)
	body := map[string]any{"code": code, "message": message}
	if position >= 0 && code == "invalid_query" {
		body["position"] = position
	}
	if len(blockers) > 0 {
		body["blockers"] = blockers
	}
	writeCollectionJSON(w, status, body)
}

func decodeCollectionJSON(w http.ResponseWriter, r *http.Request, target any) bool {
	return decodeCollectionJSONWithLimit(w, r, target, collectionMutationMaxBytes)
}

func decodeCollectionJSONWithLimit(
	w http.ResponseWriter,
	r *http.Request,
	target any,
	maximumBytes int64,
) bool {
	if r == nil || r.Body == nil {
		writeCollectionError(
			w,
			http.StatusBadRequest,
			"invalid_collection_request",
			"A JSON request body is required",
			-1,
			nil,
		)
		return false
	}
	contentTypes := r.Header.Values("Content-Type")
	if len(contentTypes) != 1 {
		writeCollectionError(
			w,
			http.StatusUnsupportedMediaType,
			"json_content_type_required",
			"Content-Type must be application/json",
			-1,
			nil,
		)
		return false
	}
	mediaType, parameters, err := mime.ParseMediaType(contentTypes[0])
	if err != nil || !strings.EqualFold(mediaType, "application/json") {
		writeCollectionError(
			w,
			http.StatusUnsupportedMediaType,
			"json_content_type_required",
			"Content-Type must be application/json",
			-1,
			nil,
		)
		return false
	}
	for name, value := range parameters {
		if !strings.EqualFold(name, "charset") || !strings.EqualFold(value, "utf-8") {
			writeCollectionError(
				w,
				http.StatusUnsupportedMediaType,
				"json_content_type_required",
				"Content-Type must be application/json",
				-1,
				nil,
			)
			return false
		}
	}
	defer r.Body.Close()
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maximumBytes))
	if err != nil {
		var maximum *http.MaxBytesError
		if errors.As(err, &maximum) {
			writeCollectionError(
				w,
				http.StatusRequestEntityTooLarge,
				"collection_request_too_large",
				"Collection request exceeds its size limit",
				-1,
				nil,
			)
			return false
		}
		writeCollectionError(
			w,
			http.StatusBadRequest,
			"invalid_collection_request",
			"Invalid JSON request body",
			-1,
			nil,
		)
		return false
	}
	if !utf8.Valid(raw) || rejectDuplicateJSONKeys(raw, 32, collectionExactJSONKeySubtree) != nil {
		writeCollectionError(
			w,
			http.StatusBadRequest,
			"invalid_collection_request",
			"Invalid JSON request body",
			-1,
			nil,
		)
		return false
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		writeCollectionError(
			w,
			http.StatusBadRequest,
			"invalid_collection_request",
			"Invalid JSON request body",
			-1,
			nil,
		)
		return false
	}
	var trailing json.RawMessage
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		writeCollectionError(
			w,
			http.StatusBadRequest,
			"invalid_collection_request",
			"Request body must contain one JSON object",
			-1,
			nil,
		)
		return false
	}
	return true
}

func collectionExactJSONKeySubtree(_ []string, foldedKey string) bool {
	return foldedKey == foldAgentJSONKey("account_overrides")
}

func boundedCollectionMessage(message string, maximum int) string {
	message = strings.TrimSpace(strings.ToValidUTF8(message, ""))
	if message == "" {
		message = "Collection request failed"
	}
	if len(message) <= maximum {
		return message
	}
	message = message[:maximum]
	for !utf8.ValidString(message) {
		message = message[:len(message)-1]
	}
	return strings.TrimSpace(message)
}

func resolveCollectionRevision(
	w http.ResponseWriter,
	r *http.Request,
	bodyRevision string,
) (string, bool) {
	if r == nil || r.URL == nil {
		writeCollectionError(
			w,
			http.StatusBadRequest,
			"invalid_collection_request",
			"Invalid collection request",
			-1,
			nil,
		)
		return "", false
	}
	if len(r.Header.Values("If-Match")) > 1 {
		writeCollectionError(
			w,
			http.StatusBadRequest,
			"conflicting_config_revision",
			"Config revision fences conflict",
			-1,
			nil,
		)
		return "", false
	}
	query, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		writeCollectionError(
			w,
			http.StatusBadRequest,
			"invalid_collection_request",
			"Collection query parameters are malformed",
			-1,
			nil,
		)
		return "", false
	}
	candidates := []string{
		strings.TrimSpace(bodyRevision),
		strings.Trim(strings.TrimSpace(r.Header.Get("If-Match")), `"`),
		strings.TrimSpace(query.Get("revision")),
	}
	resolved := ""
	for _, candidate := range candidates {
		if candidate == "" {
			continue
		}
		if resolved != "" && candidate != resolved {
			writeCollectionError(
				w,
				http.StatusBadRequest,
				"conflicting_config_revision",
				"Config revision fences conflict",
				-1,
				nil,
			)
			return "", false
		}
		resolved = candidate
	}
	return resolved, true
}

func bulkCollectionRevision(
	w http.ResponseWriter,
	request collectionBulkDeleteRequest,
) (string, bool) {
	configRevision := strings.TrimSpace(request.ConfigRevision)
	expectedRevision := strings.TrimSpace(request.ExpectedConfigRevision)
	if configRevision != "" && expectedRevision != "" && configRevision != expectedRevision {
		writeCollectionError(
			w,
			http.StatusBadRequest,
			"conflicting_config_revision",
			"Config revision fences conflict",
			-1,
			nil,
		)
		return "", false
	}
	if configRevision != "" {
		return configRevision, true
	}
	return expectedRevision, true
}

func requireCollectionRevision(w http.ResponseWriter, expected, current string) bool {
	if expected == "" {
		writeCollectionError(
			w,
			http.StatusPreconditionRequired,
			"config_revision_required",
			"The current config revision is required",
			-1,
			nil,
		)
		return false
	}
	if expected != current {
		writeCollectionError(
			w,
			http.StatusConflict,
			"config_revision_mismatch",
			"Configuration changed; reload and try again",
			-1,
			nil,
		)
		return false
	}
	return true
}

func findModelAliasIndexByName(cfg *config.Config, name string) int {
	name = strings.TrimSpace(name)
	for index := range cfg.ModelAliases {
		if cfg.ModelAliases[index].Name == name {
			return index
		}
	}
	return -1
}

func cloneModelAlias(alias config.ModelAliasConfig) config.ModelAliasConfig {
	clone := alias
	if alias.AccountOverrides != nil {
		clone.AccountOverrides = make(map[string]string, len(alias.AccountOverrides))
		for key, value := range alias.AccountOverrides {
			clone.AccountOverrides[key] = value
		}
	}
	clone.DisabledAccounts = append([]string(nil), alias.DisabledAccounts...)
	return clone
}

func cloneModelRouter(router config.ModelRouterConfig) config.ModelRouterConfig {
	clone := router
	clone.Blocks = append([]config.ModelRouterBlock(nil), router.Blocks...)
	for index := range clone.Blocks {
		clone.Blocks[index].Rules = append([]config.ModelRouterRule(nil), router.Blocks[index].Rules...)
	}
	return clone
}

func modelAliasQuerySuggestions(aliases []config.ModelAliasConfig) map[collectionquery.Field][]string {
	names := make([]string, 0, len(aliases))
	models := make([]string, 0, len(aliases))
	for _, alias := range aliases {
		names = append(names, alias.Name)
		models = append(models, alias.Model)
	}
	sort.Strings(names)
	sort.Strings(models)
	return map[collectionquery.Field][]string{"name": names, "model": models}
}

func modelRouterQuerySuggestions(routers []config.ModelRouterConfig) map[collectionquery.Field][]string {
	names := make([]string, 0, len(routers))
	for _, router := range routers {
		names = append(names, router.Name)
	}
	sort.Strings(names)
	return map[collectionquery.Field][]string{"name": names}
}

func projectModelAliasSummaries(aliases []config.ModelAliasConfig) []modelAliasSummary {
	summaries := make([]modelAliasSummary, len(aliases))
	for index, alias := range aliases {
		summaries[index] = modelAliasSummary{
			Name: alias.Name, Model: alias.Model,
			OverrideCount:        len(alias.AccountOverrides),
			DisabledAccountCount: len(alias.DisabledAccounts),
		}
	}
	return summaries
}

func projectModelRouterSummaries(routers []config.ModelRouterConfig) []modelRouterSummary {
	summaries := make([]modelRouterSummary, len(routers))
	for index, router := range routers {
		rules := 0
		for _, block := range router.Blocks {
			rules += len(block.Rules)
		}
		summaries[index] = modelRouterSummary{
			Name: router.Name, Enabled: router.Enabled, Entry: router.Entry,
			BlockCount: len(router.Blocks), RuleCount: rules,
		}
	}
	return summaries
}

func validNameAddressedCollectionID(value string) bool {
	return value != "" && value == strings.TrimSpace(value) && len(value) <= 1024 &&
		utf8.ValidString(value) && !strings.ContainsRune(value, '/') &&
		strings.IndexFunc(value, unicode.IsControl) < 0
}

func (h *Handler) handleListModelAliases(w http.ResponseWriter, r *http.Request) {
	listRequest, ok := parseCollectionListRequest(w, r, modelAliasCollectionSchema)
	if !ok {
		return
	}
	cfg, revision, err := config.LoadConfigSnapshot(h.configPath)
	if err != nil {
		writeCollectionError(
			w,
			http.StatusInternalServerError,
			"config_load_failed",
			"Failed to load configuration",
			-1,
			nil,
		)
		return
	}
	aliases := make([]config.ModelAliasConfig, len(cfg.ModelAliases))
	for index := range cfg.ModelAliases {
		aliases[index] = cloneModelAlias(cfg.ModelAliases[index])
	}
	page, err := collectionquery.Paginate(
		aliases, listRequest.Query, listRequest.Cursor, listRequest.Limit, listRequest.Now,
		collectionquery.PageOptions[config.ModelAliasConfig]{
			ID:         func(alias config.ModelAliasConfig) (string, error) { return alias.Name, nil },
			ValidateID: validNameAddressedCollectionID,
			Clone:      cloneModelAlias,
			Resolve: func(alias config.ModelAliasConfig, field collectionquery.Field, _ time.Time) (collectionquery.FieldValue, bool) {
				switch field {
				case "name":
					return collectionquery.StringValue(alias.Name), true
				case "model":
					return collectionquery.StringValue(alias.Model), true
				case "overrides":
					return collectionquery.NumberValue(float64(len(alias.AccountOverrides))), true
				case "disabled_accounts":
					return collectionquery.NumberValue(float64(len(alias.DisabledAccounts))), true
				default:
					return collectionquery.FieldValue{}, false
				}
			},
		},
	)
	if err != nil {
		writeCollectionPageError(w, err)
		return
	}
	writeCollectionJSON(w, http.StatusOK, map[string]any{
		"model_aliases": projectModelAliasSummaries(page.Items),
		"total":         page.Total, "next_cursor": page.NextCursor,
		"canonical_query": listRequest.Query.Canonical(),
		"query_schema": collectionSchemaWithSuggestions(
			modelAliasCollectionSchema,
			modelAliasQuerySuggestions(aliases),
		),
		"config_revision": revision,
	})
}

func (h *Handler) handleGetModelAliasByName(w http.ResponseWriter, r *http.Request) {
	if !validateCollectionQueryParameters(w, r) {
		return
	}
	cfg, revision, err := config.LoadConfigSnapshot(h.configPath)
	if err != nil {
		writeCollectionError(
			w,
			http.StatusInternalServerError,
			"config_load_failed",
			"Failed to load configuration",
			-1,
			nil,
		)
		return
	}
	index := findModelAliasIndexByName(cfg, r.PathValue("name"))
	if index < 0 {
		writeCollectionError(w, http.StatusNotFound, "model_alias_not_found", "Model alias not found", -1, nil)
		return
	}
	writeCollectionJSON(
		w,
		http.StatusOK,
		map[string]any{"model_alias": cloneModelAlias(cfg.ModelAliases[index]), "config_revision": revision},
	)
}

func (h *Handler) handleCreateModelAliasByName(w http.ResponseWriter, r *http.Request) {
	if !validateCollectionQueryParameters(w, r, "revision") {
		return
	}
	var request modelAliasMutationRequest
	if !decodeCollectionJSON(w, r, &request) {
		return
	}
	if request.ModelAlias == nil {
		writeCollectionError(
			w,
			http.StatusBadRequest,
			"invalid_model_alias",
			"A model_alias object is required",
			-1,
			nil,
		)
		return
	}
	alias := cloneModelAlias(*request.ModelAlias)
	if err := normalizeModelAlias(&alias); err != nil {
		writeCollectionError(w, http.StatusUnprocessableEntity, "invalid_model_alias", err.Error(), -1, nil)
		return
	}
	if !validNameAddressedCollectionID(alias.Name) {
		writeCollectionError(
			w,
			http.StatusUnprocessableEntity,
			"invalid_model_alias",
			"Model alias name is not a valid stable identity",
			-1,
			nil,
		)
		return
	}
	h.configMutationMu.Lock()
	releaseConfigMutation := sync.OnceFunc(h.configMutationMu.Unlock)
	defer releaseConfigMutation()
	cfg, revision, err := config.LoadConfigForUpdateSnapshot(h.configPath)
	if err != nil {
		writeCollectionError(
			w,
			http.StatusInternalServerError,
			"config_load_failed",
			"Failed to load configuration",
			-1,
			nil,
		)
		return
	}
	expectedRevision, ok := resolveCollectionRevision(w, r, request.ExpectedConfigRevision)
	if !ok || !requireCollectionRevision(w, expectedRevision, revision) {
		return
	}
	if findModelAliasIndexByName(cfg, alias.Name) >= 0 {
		writeCollectionError(
			w,
			http.StatusConflict,
			"model_alias_exists",
			"A model alias with this name already exists",
			-1,
			nil,
		)
		return
	}
	cfg.ModelAliases = append(cfg.ModelAliases, alias)
	if validationErr := validateAPIModelConfiguration(cfg); validationErr != nil {
		writeCollectionError(
			w,
			http.StatusUnprocessableEntity,
			"invalid_model_configuration",
			validationErr.Error(),
			-1,
			nil,
		)
		return
	}
	nextRevision, err := h.saveConfigIfRevision(h.configPath, cfg, revision)
	if err != nil {
		writeCollectionConfigSaveError(w, err)
		return
	}
	w.Header().Set("Location", "/api/model-aliases/"+url.PathEscape(alias.Name))
	releaseConfigMutation()
	writeCollectionJSON(w, http.StatusCreated, map[string]any{
		"model_alias": alias, "config_revision": nextRevision, "effects": agentEffectsForConfig(cfg),
	})
}

func (h *Handler) handleUpdateModelAliasByName(w http.ResponseWriter, r *http.Request) {
	if !validateCollectionQueryParameters(w, r, "revision") {
		return
	}
	var request modelAliasMutationRequest
	if !decodeCollectionJSON(w, r, &request) {
		return
	}
	if request.ModelAlias == nil {
		writeCollectionError(
			w,
			http.StatusBadRequest,
			"invalid_model_alias",
			"A model_alias object is required",
			-1,
			nil,
		)
		return
	}
	alias := cloneModelAlias(*request.ModelAlias)
	if err := normalizeModelAlias(&alias); err != nil {
		writeCollectionError(w, http.StatusUnprocessableEntity, "invalid_model_alias", err.Error(), -1, nil)
		return
	}
	if !validNameAddressedCollectionID(alias.Name) {
		writeCollectionError(
			w,
			http.StatusUnprocessableEntity,
			"invalid_model_alias",
			"Model alias name is not a valid stable identity",
			-1,
			nil,
		)
		return
	}
	name := strings.TrimSpace(r.PathValue("name"))
	if alias.Name != name {
		writeCollectionError(
			w,
			http.StatusConflict,
			"model_alias_name_immutable",
			"Model alias names are immutable",
			-1,
			nil,
		)
		return
	}
	h.configMutationMu.Lock()
	releaseConfigMutation := sync.OnceFunc(h.configMutationMu.Unlock)
	defer releaseConfigMutation()
	cfg, revision, err := config.LoadConfigForUpdateSnapshot(h.configPath)
	if err != nil {
		writeCollectionError(
			w,
			http.StatusInternalServerError,
			"config_load_failed",
			"Failed to load configuration",
			-1,
			nil,
		)
		return
	}
	expectedRevision, ok := resolveCollectionRevision(w, r, request.ExpectedConfigRevision)
	if !ok || !requireCollectionRevision(w, expectedRevision, revision) {
		return
	}
	index := findModelAliasIndexByName(cfg, name)
	if index < 0 {
		writeCollectionError(w, http.StatusNotFound, "model_alias_not_found", "Model alias not found", -1, nil)
		return
	}
	cfg.ModelAliases[index] = alias
	if validationErr := validateAPIModelConfiguration(cfg); validationErr != nil {
		writeCollectionError(
			w,
			http.StatusUnprocessableEntity,
			"invalid_model_configuration",
			validationErr.Error(),
			-1,
			nil,
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
		"model_alias": alias, "config_revision": nextRevision, "effects": agentEffectsForConfig(cfg),
	})
}

func (h *Handler) handleDeleteModelAliasByName(w http.ResponseWriter, r *http.Request) {
	if !validateCollectionQueryParameters(w, r, "revision") {
		return
	}
	h.configMutationMu.Lock()
	releaseConfigMutation := sync.OnceFunc(h.configMutationMu.Unlock)
	defer releaseConfigMutation()
	cfg, revision, err := config.LoadConfigForUpdateSnapshot(h.configPath)
	if err != nil {
		writeCollectionError(
			w,
			http.StatusInternalServerError,
			"config_load_failed",
			"Failed to load configuration",
			-1,
			nil,
		)
		return
	}
	expectedRevision, ok := resolveCollectionRevision(w, r, "")
	if !ok || !requireCollectionRevision(w, expectedRevision, revision) {
		return
	}
	name := strings.TrimSpace(r.PathValue("name"))
	index := findModelAliasIndexByName(cfg, name)
	if index < 0 {
		writeCollectionError(w, http.StatusNotFound, "model_alias_not_found", "Model alias not found", -1, nil)
		return
	}
	if blockers := modelAliasReferences(cfg, name); len(blockers) > 0 {
		writeCollectionError(
			w,
			http.StatusConflict,
			"model_alias_referenced",
			"Model alias is still referenced",
			-1,
			blockers,
		)
		return
	}
	cfg.ModelAliases = append(cfg.ModelAliases[:index], cfg.ModelAliases[index+1:]...)
	if validationErr := validateAPIModelConfiguration(cfg); validationErr != nil {
		writeCollectionError(
			w,
			http.StatusUnprocessableEntity,
			"invalid_model_configuration",
			validationErr.Error(),
			-1,
			nil,
		)
		return
	}
	nextRevision, err := h.saveConfigIfRevision(h.configPath, cfg, revision)
	if err != nil {
		writeCollectionConfigSaveError(w, err)
		return
	}
	releaseConfigMutation()
	writeCollectionJSON(
		w,
		http.StatusOK,
		collectionBulkDeleteResponse{
			DeletedIDs:     []string{name},
			Failures:       []collectionBulkFailure{},
			ConfigRevision: nextRevision,
			Effects:        agentEffectsForConfig(cfg),
		},
	)
}

func (h *Handler) handleBulkDeleteModelAliases(w http.ResponseWriter, r *http.Request) {
	if !validateCollectionQueryParameters(w, r, "revision") {
		return
	}
	var request collectionBulkDeleteRequest
	if !decodeCollectionJSON(w, r, &request) {
		return
	}
	if len(request.IDs) == 0 || len(request.IDs) > 200 {
		writeCollectionError(
			w,
			http.StatusBadRequest,
			"invalid_bulk_delete",
			"Bulk deletion requires between 1 and 200 IDs",
			-1,
			nil,
		)
		return
	}
	h.configMutationMu.Lock()
	releaseConfigMutation := sync.OnceFunc(h.configMutationMu.Unlock)
	defer releaseConfigMutation()
	cfg, revision, err := config.LoadConfigForUpdateSnapshot(h.configPath)
	if err != nil {
		writeCollectionError(
			w,
			http.StatusInternalServerError,
			"config_load_failed",
			"Failed to load configuration",
			-1,
			nil,
		)
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
	requested, failures := normalizeBulkIDs(request.IDs)
	deleteNames := make(map[string]bool, len(requested))
	for _, name := range requested {
		if findModelAliasIndexByName(cfg, name) < 0 {
			failures = append(failures, collectionBulkFailure{ID: name, Code: "not_found"})
			continue
		}
		if blockers := modelAliasReferences(cfg, name); len(blockers) > 0 {
			failures = append(failures, collectionBulkFailure{ID: name, Code: "referenced", Blockers: blockers})
			continue
		}
		deleteNames[name] = true
	}
	deleted := make([]string, 0, len(deleteNames))
	kept := make([]config.ModelAliasConfig, 0, len(cfg.ModelAliases)-len(deleteNames))
	for _, alias := range cfg.ModelAliases {
		if deleteNames[alias.Name] {
			deleted = append(deleted, alias.Name)
			continue
		}
		kept = append(kept, alias)
	}
	cfg.ModelAliases = kept
	nextRevision := revision
	if len(deleted) > 0 {
		if validationErr := validateAPIModelConfiguration(cfg); validationErr != nil {
			writeCollectionError(
				w,
				http.StatusUnprocessableEntity,
				"invalid_model_configuration",
				validationErr.Error(),
				-1,
				nil,
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
	writeCollectionJSON(
		w,
		http.StatusOK,
		collectionBulkDeleteResponse{
			DeletedIDs: deleted, Failures: failures, ConfigRevision: nextRevision,
			Effects: agentEffectsForConfig(cfg),
		},
	)
}

func (h *Handler) handleListModelRouters(w http.ResponseWriter, r *http.Request) {
	listRequest, ok := parseCollectionListRequest(w, r, modelRouterCollectionSchema)
	if !ok {
		return
	}
	cfg, revision, err := config.LoadConfigSnapshot(h.configPath)
	if err != nil {
		writeCollectionError(
			w,
			http.StatusInternalServerError,
			"config_load_failed",
			"Failed to load configuration",
			-1,
			nil,
		)
		return
	}
	routers := make([]config.ModelRouterConfig, len(cfg.ModelRouters))
	for index := range cfg.ModelRouters {
		routers[index] = cloneModelRouter(cfg.ModelRouters[index])
	}
	page, err := collectionquery.Paginate(
		routers, listRequest.Query, listRequest.Cursor, listRequest.Limit, listRequest.Now,
		collectionquery.PageOptions[config.ModelRouterConfig]{
			ID:         func(router config.ModelRouterConfig) (string, error) { return router.Name, nil },
			ValidateID: validNameAddressedCollectionID,
			Clone:      cloneModelRouter,
			Resolve: func(router config.ModelRouterConfig, field collectionquery.Field, _ time.Time) (collectionquery.FieldValue, bool) {
				switch field {
				case "name":
					return collectionquery.StringValue(router.Name), true
				case "enabled":
					return collectionquery.BooleanValue(router.Enabled), true
				case "blocks":
					return collectionquery.NumberValue(float64(len(router.Blocks))), true
				case "rules":
					count := 0
					for _, block := range router.Blocks {
						count += len(block.Rules)
					}
					return collectionquery.NumberValue(float64(count)), true
				default:
					return collectionquery.FieldValue{}, false
				}
			},
		},
	)
	if err != nil {
		writeCollectionPageError(w, err)
		return
	}
	writeCollectionJSON(w, http.StatusOK, map[string]any{
		"model_routers": projectModelRouterSummaries(page.Items),
		"total":         page.Total, "next_cursor": page.NextCursor,
		"canonical_query": listRequest.Query.Canonical(),
		"query_schema": collectionSchemaWithSuggestions(
			modelRouterCollectionSchema,
			modelRouterQuerySuggestions(routers),
		),
		"config_revision": revision,
	})
}

func (h *Handler) handleGetModelRouterByName(w http.ResponseWriter, r *http.Request) {
	if !validateCollectionQueryParameters(w, r) {
		return
	}
	cfg, revision, err := config.LoadConfigSnapshot(h.configPath)
	if err != nil {
		writeCollectionError(
			w,
			http.StatusInternalServerError,
			"config_load_failed",
			"Failed to load configuration",
			-1,
			nil,
		)
		return
	}
	index := findModelRouterIndex(cfg, r.PathValue("name"))
	if index < 0 {
		writeCollectionError(w, http.StatusNotFound, "model_router_not_found", "Model router not found", -1, nil)
		return
	}
	writeCollectionJSON(
		w,
		http.StatusOK,
		map[string]any{"model_router": cloneModelRouter(cfg.ModelRouters[index]), "config_revision": revision},
	)
}

func (h *Handler) handleCreateModelRouterByName(w http.ResponseWriter, r *http.Request) {
	if !validateCollectionQueryParameters(w, r, "revision") {
		return
	}
	var request modelRouterMutationRequest
	if !decodeCollectionJSON(w, r, &request) {
		return
	}
	if request.ModelRouter == nil {
		writeCollectionError(
			w,
			http.StatusBadRequest,
			"invalid_model_router",
			"A model_router object is required",
			-1,
			nil,
		)
		return
	}
	router := cloneModelRouter(*request.ModelRouter)
	router.Name = strings.TrimSpace(router.Name)
	if !validNameAddressedCollectionID(router.Name) {
		writeCollectionError(
			w,
			http.StatusUnprocessableEntity,
			"invalid_model_router",
			"Model router name is not a valid stable identity",
			-1,
			nil,
		)
		return
	}
	h.configMutationMu.Lock()
	releaseConfigMutation := sync.OnceFunc(h.configMutationMu.Unlock)
	defer releaseConfigMutation()
	cfg, revision, err := config.LoadConfigForUpdateSnapshot(h.configPath)
	if err != nil {
		writeCollectionError(
			w,
			http.StatusInternalServerError,
			"config_load_failed",
			"Failed to load configuration",
			-1,
			nil,
		)
		return
	}
	expectedRevision, ok := resolveCollectionRevision(w, r, request.ExpectedConfigRevision)
	if !ok || !requireCollectionRevision(w, expectedRevision, revision) {
		return
	}
	if findModelRouterIndex(cfg, router.Name) >= 0 {
		writeCollectionError(
			w,
			http.StatusConflict,
			"model_router_exists",
			"A model router with this name already exists",
			-1,
			nil,
		)
		return
	}
	cfg.ModelRouters = append(cfg.ModelRouters, router)
	cfg.MaterializeAccountRouterModels()
	cfg.MaterializeModelRouterModels()
	if validationErr := validateAPIModelConfiguration(cfg); validationErr != nil {
		writeCollectionError(
			w,
			http.StatusUnprocessableEntity,
			"invalid_model_configuration",
			validationErr.Error(),
			-1,
			nil,
		)
		return
	}
	nextRevision, err := h.saveConfigIfRevision(h.configPath, cfg, revision)
	if err != nil {
		writeCollectionConfigSaveError(w, err)
		return
	}
	w.Header().Set("Location", "/api/model-routers/"+url.PathEscape(router.Name))
	releaseConfigMutation()
	writeCollectionJSON(w, http.StatusCreated, map[string]any{
		"model_router": router, "config_revision": nextRevision, "effects": agentEffectsForConfig(cfg),
	})
}

func (h *Handler) handleUpdateModelRouterByName(w http.ResponseWriter, r *http.Request) {
	if !validateCollectionQueryParameters(w, r, "revision") {
		return
	}
	var request modelRouterMutationRequest
	if !decodeCollectionJSON(w, r, &request) {
		return
	}
	if request.ModelRouter == nil {
		writeCollectionError(
			w,
			http.StatusBadRequest,
			"invalid_model_router",
			"A model_router object is required",
			-1,
			nil,
		)
		return
	}
	router := cloneModelRouter(*request.ModelRouter)
	router.Name = strings.TrimSpace(router.Name)
	if !validNameAddressedCollectionID(router.Name) {
		writeCollectionError(
			w,
			http.StatusUnprocessableEntity,
			"invalid_model_router",
			"Model router name is not a valid stable identity",
			-1,
			nil,
		)
		return
	}
	name := strings.TrimSpace(r.PathValue("name"))
	if router.Name != name {
		writeCollectionError(
			w,
			http.StatusConflict,
			"model_router_name_immutable",
			"Model router names are immutable",
			-1,
			nil,
		)
		return
	}
	h.configMutationMu.Lock()
	releaseConfigMutation := sync.OnceFunc(h.configMutationMu.Unlock)
	defer releaseConfigMutation()
	cfg, revision, err := config.LoadConfigForUpdateSnapshot(h.configPath)
	if err != nil {
		writeCollectionError(
			w,
			http.StatusInternalServerError,
			"config_load_failed",
			"Failed to load configuration",
			-1,
			nil,
		)
		return
	}
	expectedRevision, ok := resolveCollectionRevision(w, r, request.ExpectedConfigRevision)
	if !ok || !requireCollectionRevision(w, expectedRevision, revision) {
		return
	}
	index := findModelRouterIndex(cfg, name)
	if index < 0 {
		writeCollectionError(w, http.StatusNotFound, "model_router_not_found", "Model router not found", -1, nil)
		return
	}
	cfg.ModelRouters[index] = router
	cfg.MaterializeAccountRouterModels()
	cfg.MaterializeModelRouterModels()
	if validationErr := validateAPIModelConfiguration(cfg); validationErr != nil {
		writeCollectionError(
			w,
			http.StatusUnprocessableEntity,
			"invalid_model_configuration",
			validationErr.Error(),
			-1,
			nil,
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
		"model_router": router, "config_revision": nextRevision, "effects": agentEffectsForConfig(cfg),
	})
}

func (h *Handler) handleDeleteModelRouterByName(w http.ResponseWriter, r *http.Request) {
	if !validateCollectionQueryParameters(w, r, "revision") {
		return
	}
	h.configMutationMu.Lock()
	releaseConfigMutation := sync.OnceFunc(h.configMutationMu.Unlock)
	defer releaseConfigMutation()
	cfg, revision, err := config.LoadConfigForUpdateSnapshot(h.configPath)
	if err != nil {
		writeCollectionError(
			w,
			http.StatusInternalServerError,
			"config_load_failed",
			"Failed to load configuration",
			-1,
			nil,
		)
		return
	}
	expectedRevision, ok := resolveCollectionRevision(w, r, "")
	if !ok || !requireCollectionRevision(w, expectedRevision, revision) {
		return
	}
	name := strings.TrimSpace(r.PathValue("name"))
	index := findModelRouterIndex(cfg, name)
	if index < 0 {
		writeCollectionError(w, http.StatusNotFound, "model_router_not_found", "Model router not found", -1, nil)
		return
	}
	if blockers := modelAliasReferences(cfg, name); len(blockers) > 0 {
		writeCollectionError(
			w,
			http.StatusConflict,
			"model_router_referenced",
			"Model router is still referenced",
			-1,
			blockers,
		)
		return
	}
	cfg.ModelRouters = append(cfg.ModelRouters[:index], cfg.ModelRouters[index+1:]...)
	cfg.MaterializeAccountRouterModels()
	cfg.MaterializeModelRouterModels()
	if validationErr := validateAPIModelConfiguration(cfg); validationErr != nil {
		writeCollectionError(
			w,
			http.StatusUnprocessableEntity,
			"invalid_model_configuration",
			validationErr.Error(),
			-1,
			nil,
		)
		return
	}
	nextRevision, err := h.saveConfigIfRevision(h.configPath, cfg, revision)
	if err != nil {
		writeCollectionConfigSaveError(w, err)
		return
	}
	releaseConfigMutation()
	writeCollectionJSON(
		w,
		http.StatusOK,
		collectionBulkDeleteResponse{
			DeletedIDs:     []string{name},
			Failures:       []collectionBulkFailure{},
			ConfigRevision: nextRevision,
			Effects:        agentEffectsForConfig(cfg),
		},
	)
}

func (h *Handler) handleBulkDeleteModelRouters(w http.ResponseWriter, r *http.Request) {
	if !validateCollectionQueryParameters(w, r, "revision") {
		return
	}
	var request collectionBulkDeleteRequest
	if !decodeCollectionJSON(w, r, &request) {
		return
	}
	if len(request.IDs) == 0 || len(request.IDs) > 200 {
		writeCollectionError(
			w,
			http.StatusBadRequest,
			"invalid_bulk_delete",
			"Bulk deletion requires between 1 and 200 IDs",
			-1,
			nil,
		)
		return
	}
	h.configMutationMu.Lock()
	releaseConfigMutation := sync.OnceFunc(h.configMutationMu.Unlock)
	defer releaseConfigMutation()
	cfg, revision, err := config.LoadConfigForUpdateSnapshot(h.configPath)
	if err != nil {
		writeCollectionError(
			w,
			http.StatusInternalServerError,
			"config_load_failed",
			"Failed to load configuration",
			-1,
			nil,
		)
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
	requested, failures := normalizeBulkIDs(request.IDs)
	deleteNames := make(map[string]bool, len(requested))
	for _, name := range requested {
		if findModelRouterIndex(cfg, name) < 0 {
			failures = append(failures, collectionBulkFailure{ID: name, Code: "not_found"})
			continue
		}
		if blockers := modelAliasReferences(cfg, name); len(blockers) > 0 {
			failures = append(failures, collectionBulkFailure{ID: name, Code: "referenced", Blockers: blockers})
			continue
		}
		deleteNames[name] = true
	}
	deleted := make([]string, 0, len(deleteNames))
	kept := make(config.ModelRouterList, 0, len(cfg.ModelRouters)-len(deleteNames))
	for _, router := range cfg.ModelRouters {
		if deleteNames[router.Name] {
			deleted = append(deleted, router.Name)
			continue
		}
		kept = append(kept, router)
	}
	cfg.ModelRouters = kept
	nextRevision := revision
	if len(deleted) > 0 {
		cfg.MaterializeAccountRouterModels()
		cfg.MaterializeModelRouterModels()
		if validationErr := validateAPIModelConfiguration(cfg); validationErr != nil {
			writeCollectionError(
				w,
				http.StatusUnprocessableEntity,
				"invalid_model_configuration",
				validationErr.Error(),
				-1,
				nil,
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
	writeCollectionJSON(
		w,
		http.StatusOK,
		collectionBulkDeleteResponse{
			DeletedIDs: deleted, Failures: failures, ConfigRevision: nextRevision,
			Effects: agentEffectsForConfig(cfg),
		},
	)
}

func normalizeBulkIDs(ids []string) ([]string, []collectionBulkFailure) {
	counts := make(map[string]int, len(ids))
	for _, raw := range ids {
		counts[strings.TrimSpace(raw)]++
	}
	unique := make([]string, 0, len(counts))
	failures := make([]collectionBulkFailure, 0)
	for id, count := range counts {
		if id == "" {
			failures = append(failures, collectionBulkFailure{ID: id, Code: "invalid_id"})
			continue
		}
		if count > 1 {
			failures = append(failures, collectionBulkFailure{ID: id, Code: "duplicate_id"})
			continue
		}
		unique = append(unique, id)
	}
	sort.Strings(unique)
	sortCollectionFailures(failures)
	return unique, failures
}

func sortCollectionFailures(failures []collectionBulkFailure) {
	sort.SliceStable(failures, func(i, j int) bool {
		if failures[i].ID == failures[j].ID {
			return failures[i].Code < failures[j].Code
		}
		return failures[i].ID < failures[j].ID
	})
}

func writeCollectionConfigSaveError(w http.ResponseWriter, err error) {
	if errors.Is(err, config.ErrConfigRevisionMismatch) {
		writeCollectionError(
			w,
			http.StatusConflict,
			"config_revision_mismatch",
			"Configuration changed; reload and try again",
			-1,
			nil,
		)
		return
	}
	writeCollectionError(
		w,
		http.StatusInternalServerError,
		"config_save_failed",
		"Failed to save configuration",
		-1,
		nil,
	)
}
