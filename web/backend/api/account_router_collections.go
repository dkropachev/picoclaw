package api

import (
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/sipeed/picoclaw/pkg/collectionquery"
	"github.com/sipeed/picoclaw/pkg/config"
)

const (
	accountRouterCollectionIDNamespace = "account-router"
	accountRouterOpaqueIDPrefix        = "ar_"

	accountRouterStatusAvailable    = "available"
	accountRouterStatusDisabled     = "disabled"
	accountRouterStatusInvalid      = "invalid"
	accountRouterStatusUnconfigured = "unconfigured"
	accountRouterStatusUnreachable  = "unreachable"
)

var accountRouterReservedDirectIDs = map[string]struct{}{
	"bulk-delete": {},
	"new":         {},
}

type accountRouterMutationRequest struct {
	ExpectedConfigRevision string                      `json:"expected_config_revision"`
	AccountRouter          *config.AccountRouterConfig `json:"account_router"`
}

type accountRouterRevisionRequest struct {
	ExpectedConfigRevision string `json:"expected_config_revision"`
}

type accountRouterSummary struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Enabled   bool   `json:"enabled"`
	IsDefault bool   `json:"is_default"`
	Status    string `json:"status"`
	Entry     string `json:"entry"`
	Accounts  int    `json:"accounts"`
	Blocks    int    `json:"blocks"`
}

type accountRouterResource struct {
	ID                     string                      `json:"id"`
	Name                   string                      `json:"name"`
	Enabled                bool                        `json:"enabled"`
	IsDefault              bool                        `json:"is_default"`
	Status                 string                      `json:"status"`
	Entry                  string                      `json:"entry"`
	Accounts               []string                    `json:"accounts"`
	RefreshIntervalSeconds int                         `json:"refresh_interval_seconds,omitempty"`
	Blocks                 []config.AccountRouterBlock `json:"blocks"`
}

type accountRouterCollectionItem struct {
	Router  config.AccountRouterConfig
	Summary accountRouterSummary
}

type accountRouterItemsProjector func(
	*config.Config,
	time.Time,
) ([]accountRouterCollectionItem, error)

type accountRouterResourceProjector func(
	*config.Config,
	config.AccountRouterConfig,
	time.Time,
) (accountRouterResource, error)

type accountRouterPager func(
	[]accountRouterCollectionItem,
	collectionListRequest,
) (collectionquery.PageResult[accountRouterCollectionItem], error)

type accountRouterCandidateValidator func(*config.Config) error

var accountRouterCollectionSchema = mustCollectionQuerySchema(
	[]collectionquery.FieldSchema{
		{Name: "name", Type: collectionquery.TypeString, Sortable: true},
		{
			Name:            "enabled",
			Type:            collectionquery.TypeBoolean,
			Sortable:        true,
			SuggestedValues: []string{"true", "false"},
		},
		{
			Name:            "is_default",
			Type:            collectionquery.TypeBoolean,
			Sortable:        true,
			SuggestedValues: []string{"true", "false"},
		},
		{
			Name:     "status",
			Type:     collectionquery.TypeEnum,
			Sortable: true,
			SuggestedValues: []string{
				accountRouterStatusAvailable,
				accountRouterStatusDisabled,
				accountRouterStatusInvalid,
				accountRouterStatusUnconfigured,
				accountRouterStatusUnreachable,
			},
		},
		{Name: "entry", Type: collectionquery.TypeString, Sortable: true},
		{Name: "accounts", Type: collectionquery.TypeNumber, Sortable: true},
		{Name: "blocks", Type: collectionquery.TypeNumber, Sortable: true},
	},
	[]collectionquery.SortField{{Field: "name", Direction: collectionquery.Ascending}},
)

func (h *Handler) registerAccountRouterCollectionRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/account-routers", h.handleListAccountRouters)
	mux.HandleFunc(
		"POST /api/account-routers",
		h.requireCollectionMutationOrigin(h.handleCreateAccountRouter),
	)
	mux.HandleFunc(
		"POST /api/account-routers/bulk-delete",
		h.requireCollectionMutationOrigin(h.handleBulkDeleteAccountRouters),
	)
	mux.HandleFunc(
		"POST /api/account-routers/{name}/default",
		h.requireCollectionMutationOrigin(h.handleMakeDefaultAccountRouter),
	)
	mux.HandleFunc("GET /api/account-routers/{name}", h.handleGetAccountRouter)
	mux.HandleFunc(
		"PUT /api/account-routers/{name}",
		h.requireCollectionMutationOrigin(h.handleUpdateAccountRouter),
	)
	mux.HandleFunc(
		"DELETE /api/account-routers/{name}",
		h.requireCollectionMutationOrigin(h.handleDeleteAccountRouter),
	)
}

func cloneAccountRouter(router config.AccountRouterConfig) config.AccountRouterConfig {
	clone := router
	clone.Blocks = make([]config.AccountRouterBlock, len(router.Blocks))
	for index := range router.Blocks {
		clone.Blocks[index] = cloneAccountRouterBlock(router.Blocks[index])
	}
	return clone
}

func cloneAccountRouterBlock(block config.AccountRouterBlock) config.AccountRouterBlock {
	clone := block
	clone.Accounts = append([]string(nil), block.Accounts...)
	if block.Condition != nil {
		condition := *block.Condition
		condition.Left = cloneAccountRouterExpression(block.Condition.Left)
		condition.Right = cloneAccountRouterExpression(block.Condition.Right)
		clone.Condition = &condition
	}
	return clone
}

func cloneAccountRouterExpression(expression config.AccountRouterExpression) config.AccountRouterExpression {
	clone := expression
	if expression.Value != nil {
		value := *expression.Value
		clone.Value = &value
	}
	if expression.Left != nil {
		left := cloneAccountRouterExpression(*expression.Left)
		clone.Left = &left
	}
	if expression.Right != nil {
		right := cloneAccountRouterExpression(*expression.Right)
		clone.Right = &right
	}
	return clone
}

func normalizeAccountRouterForMutation(router config.AccountRouterConfig) config.AccountRouterConfig {
	router = cloneAccountRouter(router)
	router.Name = strings.TrimSpace(router.Name)
	return router
}

func canonicalAccountRouterName(name string) string {
	return strings.TrimSpace(name)
}

func accountRouterOpaqueID(id string) bool {
	if !strings.HasPrefix(id, accountRouterOpaqueIDPrefix) {
		return false
	}
	return validCollectionResourceID(strings.TrimPrefix(id, accountRouterOpaqueIDPrefix))
}

func accountRouterDirectID(name string) bool {
	if !validNameAddressedCollectionID(name) || name == "." || name == ".." ||
		accountRouterOpaqueID(name) {
		return false
	}
	for index := 0; index < len(name); index++ {
		character := name[index]
		if character >= 'a' && character <= 'z' ||
			character >= 'A' && character <= 'Z' ||
			character >= '0' && character <= '9' ||
			character == '-' || character == '.' || character == '_' || character == '~' {
			continue
		}
		return false
	}
	_, reserved := accountRouterReservedDirectIDs[strings.ToLower(name)]
	return !reserved
}

func accountRouterResourceID(name string) (string, error) {
	name = canonicalAccountRouterName(name)
	if accountRouterDirectID(name) {
		return name, nil
	}
	digest, err := encodeCollectionResourceID(accountRouterCollectionIDNamespace, name)
	if err != nil {
		return "", err
	}
	return accountRouterOpaqueIDPrefix + digest, nil
}

func validAccountRouterResourceID(id string) bool {
	return accountRouterOpaqueID(id) || accountRouterDirectID(id)
}

func validAccountRouterMutationName(name string) bool {
	return validCollectionResourceIdentity(name) &&
		strings.IndexFunc(name, unicode.IsControl) < 0
}

func findAccountRouterByResourceID(
	cfg *config.Config,
	id string,
) (int, string) {
	if cfg == nil || !validAccountRouterResourceID(id) {
		return -1, ""
	}
	for index := range cfg.AccountRouters {
		name := canonicalAccountRouterName(cfg.AccountRouters[index].Name)
		candidateID, err := accountRouterResourceID(name)
		if err == nil && candidateID == id {
			return index, name
		}
	}
	return -1, ""
}

func accountRouterQuerySuggestions(
	items []accountRouterCollectionItem,
) map[collectionquery.Field][]string {
	names := make([]string, 0, len(items))
	entries := make([]string, 0, len(items))
	for _, item := range items {
		names = append(names, item.Summary.Name)
		entries = append(entries, item.Summary.Entry)
	}
	sort.Strings(names)
	sort.Strings(entries)
	return map[collectionquery.Field][]string{"name": names, "entry": entries}
}

func accountRouterStatus(
	cfg *config.Config,
	router *config.AccountRouterConfig,
	evaluatedAt time.Time,
) string {
	if cfg == nil || router == nil || evaluatedAt.IsZero() || router.Validate() != nil {
		return accountRouterStatusInvalid
	}
	if !router.Enabled {
		return accountRouterStatusDisabled
	}
	// Validate guarantees every reachable terminal is an account or a
	// non-empty load-balancing block, so a valid graph has at least one ref.
	accounts := reachableAccountRouterRefs(router)

	hasUnreachable := false
	for _, accountRef := range accounts {
		if _, credential := config.AccountRouterCredentialAccountID(accountRef); credential {
			if _, supported := config.AccountRouterCredentialAccountProvider(accountRef); !supported {
				return accountRouterStatusInvalid
			}
			if credentialAccountAvailableAt(accountRef, evaluatedAt) {
				return accountRouterStatusAvailable
			}
			continue
		}
		account, err := resolveAccountModelConfig(cfg, accountRef)
		if err != nil {
			return accountRouterStatusInvalid
		}
		status := modelConfigurationStatus(account)
		switch status.Status {
		case modelStatusAvailable:
			return accountRouterStatusAvailable
		case modelStatusUnreachable:
			hasUnreachable = true
		}
	}
	if hasUnreachable {
		return accountRouterStatusUnreachable
	}
	return accountRouterStatusUnconfigured
}

func accountRouterSummaryForConfig(
	cfg *config.Config,
	router config.AccountRouterConfig,
	evaluatedAt time.Time,
) (accountRouterSummary, error) {
	name := canonicalAccountRouterName(router.Name)
	id, err := accountRouterResourceID(name)
	if err != nil {
		return accountRouterSummary{}, err
	}
	return accountRouterSummary{
		ID:        id,
		Name:      name,
		Enabled:   router.Enabled,
		IsDefault: cfg != nil && strings.TrimSpace(cfg.Agents.Defaults.AccountRef) == name,
		Status:    accountRouterStatus(cfg, &router, evaluatedAt),
		Entry:     strings.TrimSpace(router.Entry),
		Accounts:  len(reachableAccountRouterRefs(&router)),
		Blocks:    len(router.Blocks),
	}, nil
}

func accountRouterResourceForConfig(
	cfg *config.Config,
	router config.AccountRouterConfig,
	evaluatedAt time.Time,
) (accountRouterResource, error) {
	router = cloneAccountRouter(router)
	summary, err := accountRouterSummaryForConfig(cfg, router, evaluatedAt)
	if err != nil {
		return accountRouterResource{}, err
	}
	return accountRouterResource{
		ID:                     summary.ID,
		Name:                   summary.Name,
		Enabled:                summary.Enabled,
		IsDefault:              summary.IsDefault,
		Status:                 summary.Status,
		Entry:                  summary.Entry,
		Accounts:               reachableAccountRouterRefs(&router),
		RefreshIntervalSeconds: router.RefreshIntervalSeconds,
		Blocks:                 router.Blocks,
	}, nil
}

func projectAccountRouterItems(
	cfg *config.Config,
	evaluatedAt time.Time,
) ([]accountRouterCollectionItem, error) {
	items := make([]accountRouterCollectionItem, len(cfg.AccountRouters))
	for index := range cfg.AccountRouters {
		router := cloneAccountRouter(cfg.AccountRouters[index])
		summary, err := accountRouterSummaryForConfig(cfg, router, evaluatedAt)
		if err != nil {
			return nil, err
		}
		items[index] = accountRouterCollectionItem{
			Router:  router,
			Summary: summary,
		}
	}
	return items, nil
}

func accountRouterIDFromPath(
	w http.ResponseWriter,
	r *http.Request,
) (string, bool) {
	if r == nil {
		writeCollectionError(
			w,
			http.StatusBadRequest,
			"invalid_account_router_id",
			"Account router ID is invalid",
			-1,
			nil,
		)
		return "", false
	}
	id := r.PathValue("name")
	if !validAccountRouterResourceID(id) {
		writeCollectionError(
			w,
			http.StatusBadRequest,
			"invalid_account_router_id",
			"Account router ID is invalid",
			-1,
			nil,
		)
		return "", false
	}
	return id, true
}

func pageAccountRouters(
	items []accountRouterCollectionItem,
	request collectionListRequest,
) (collectionquery.PageResult[accountRouterCollectionItem], error) {
	return collectionquery.Paginate(
		items,
		request.Query,
		request.Cursor,
		request.Limit,
		request.Now,
		accountRouterPageOptions(),
	)
}

func accountRouterPageOptions() collectionquery.PageOptions[accountRouterCollectionItem] {
	return collectionquery.PageOptions[accountRouterCollectionItem]{
		ID: func(item accountRouterCollectionItem) (string, error) {
			return item.Summary.ID, nil
		},
		ValidateID: validAccountRouterResourceID,
		Clone: func(item accountRouterCollectionItem) accountRouterCollectionItem {
			item.Router = cloneAccountRouter(item.Router)
			return item
		},
		Resolve: func(
			item accountRouterCollectionItem,
			field collectionquery.Field,
			_ time.Time,
		) (collectionquery.FieldValue, bool) {
			switch field {
			case "name":
				return collectionquery.StringValue(item.Summary.Name), true
			case "enabled":
				return collectionquery.BooleanValue(item.Summary.Enabled), true
			case "is_default":
				return collectionquery.BooleanValue(item.Summary.IsDefault), true
			case "status":
				return collectionquery.EnumValue(item.Summary.Status), true
			case "entry":
				return collectionquery.StringValue(item.Summary.Entry), true
			case "accounts":
				return collectionquery.NumberValue(float64(item.Summary.Accounts)), true
			case "blocks":
				return collectionquery.NumberValue(float64(item.Summary.Blocks)), true
			default:
				return collectionquery.FieldValue{}, false
			}
		},
	}
}

func (h *Handler) handleListAccountRouters(w http.ResponseWriter, r *http.Request) {
	listRequest, ok := parseCollectionListRequest(w, r, accountRouterCollectionSchema)
	if !ok {
		return
	}
	evaluatedAt := listRequest.Now
	if listRequest.Cursor != "" {
		cursor, err := collectionquery.DecodeCursor(
			listRequest.Cursor,
			listRequest.Query,
			validAccountRouterResourceID,
		)
		if err != nil {
			writeCollectionPageError(w, err)
			return
		}
		evaluatedAt = cursor.EvaluatedAt
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
	items, err := h.projectAccountRouterItems(cfg, evaluatedAt)
	if err != nil {
		writeCollectionError(
			w,
			http.StatusInternalServerError,
			"account_router_projection_failed",
			"Failed to project account routers",
			-1,
			nil,
		)
		return
	}
	page, err := h.pageAccountRouters(items, listRequest)
	if err != nil {
		writeCollectionPageError(w, err)
		return
	}
	summaries := make([]accountRouterSummary, len(page.Items))
	for index := range page.Items {
		summaries[index] = page.Items[index].Summary
	}
	writeCollectionJSON(w, http.StatusOK, map[string]any{
		"account_routers": summaries,
		"total":           page.Total,
		"next_cursor":     page.NextCursor,
		"canonical_query": listRequest.Query.Canonical(),
		"query_schema": collectionSchemaWithSuggestions(
			accountRouterCollectionSchema,
			accountRouterQuerySuggestions(items),
		),
		"config_revision": revision,
	})
}

func (h *Handler) handleGetAccountRouter(w http.ResponseWriter, r *http.Request) {
	if !validateCollectionQueryParameters(w, r) {
		return
	}
	id, ok := accountRouterIDFromPath(w, r)
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
	index, _ := findAccountRouterByResourceID(cfg, id)
	if index < 0 {
		writeCollectionError(
			w,
			http.StatusNotFound,
			"account_router_not_found",
			"Account router not found",
			-1,
			nil,
		)
		return
	}
	resource, err := h.projectAccountRouterResource(
		cfg,
		cfg.AccountRouters[index],
		time.Now().UTC(),
	)
	if err != nil {
		writeCollectionError(
			w,
			http.StatusInternalServerError,
			"account_router_projection_failed",
			"Failed to project account router",
			-1,
			nil,
		)
		return
	}
	writeCollectionJSON(w, http.StatusOK, map[string]any{
		"account_router":  resource,
		"config_revision": revision,
	})
}

func validateAccountRouterMutation(
	w http.ResponseWriter,
	request accountRouterMutationRequest,
) (config.AccountRouterConfig, bool) {
	if request.AccountRouter == nil {
		writeCollectionError(
			w,
			http.StatusBadRequest,
			"invalid_account_router",
			"An account_router object is required",
			-1,
			nil,
		)
		return config.AccountRouterConfig{}, false
	}
	router := normalizeAccountRouterForMutation(*request.AccountRouter)
	if !validAccountRouterMutationName(router.Name) {
		writeCollectionError(
			w,
			http.StatusUnprocessableEntity,
			"invalid_account_router",
			"Account router name is not a valid stable identity",
			-1,
			nil,
		)
		return config.AccountRouterConfig{}, false
	}
	if err := router.Validate(); err != nil {
		writeCollectionError(
			w,
			http.StatusUnprocessableEntity,
			"invalid_account_router",
			err.Error(),
			-1,
			nil,
		)
		return config.AccountRouterConfig{}, false
	}
	return router, true
}

func materializeAndValidateAccountRouterCandidate(cfg *config.Config) error {
	cfg.MaterializeAccountRouterModels()
	cfg.MaterializeModelRouterModels()
	return validateAPIModelConfiguration(cfg)
}

func (h *Handler) handleCreateAccountRouter(w http.ResponseWriter, r *http.Request) {
	if !validateCollectionQueryParameters(w, r, "revision") {
		return
	}
	var request accountRouterMutationRequest
	if !decodeCollectionJSON(w, r, &request) {
		return
	}
	router, ok := validateAccountRouterMutation(w, request)
	if !ok {
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
	if findAccountRouterIndex(cfg, router.Name) >= 0 {
		writeCollectionError(
			w,
			http.StatusConflict,
			"account_router_exists",
			"An account router with this name already exists",
			-1,
			nil,
		)
		return
	}
	cfg.AccountRouters = append(cfg.AccountRouters, router)
	if validationErr := h.validateAccountRouterCandidate(cfg); validationErr != nil {
		writeCollectionError(
			w,
			http.StatusUnprocessableEntity,
			"invalid_account_router_configuration",
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
	resource, err := h.projectAccountRouterResource(cfg, router, time.Now().UTC())
	if err != nil {
		writeCollectionError(
			w,
			http.StatusInternalServerError,
			"account_router_projection_failed",
			"Failed to project account router",
			-1,
			nil,
		)
		return
	}
	w.Header().Set("Location", "/api/account-routers/"+url.PathEscape(resource.ID))
	releaseConfigMutation()
	writeCollectionJSON(w, http.StatusCreated, map[string]any{
		"account_router":  resource,
		"config_revision": nextRevision,
		"effects":         agentEffectsForConfig(cfg),
	})
}

func (h *Handler) handleUpdateAccountRouter(w http.ResponseWriter, r *http.Request) {
	if !validateCollectionQueryParameters(w, r, "revision") {
		return
	}
	id, ok := accountRouterIDFromPath(w, r)
	if !ok {
		return
	}
	var request accountRouterMutationRequest
	if !decodeCollectionJSON(w, r, &request) {
		return
	}
	router, ok := validateAccountRouterMutation(w, request)
	if !ok {
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
	index, name := findAccountRouterByResourceID(cfg, id)
	if index < 0 {
		writeCollectionError(
			w,
			http.StatusNotFound,
			"account_router_not_found",
			"Account router not found",
			-1,
			nil,
		)
		return
	}
	if router.Name != name {
		writeCollectionError(
			w,
			http.StatusConflict,
			"account_router_name_immutable",
			"Account router names are immutable",
			-1,
			nil,
		)
		return
	}
	cfg.AccountRouters[index] = router
	if validationErr := h.validateAccountRouterCandidate(cfg); validationErr != nil {
		writeCollectionError(
			w,
			http.StatusUnprocessableEntity,
			"invalid_account_router_configuration",
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
	resource, err := h.projectAccountRouterResource(cfg, router, time.Now().UTC())
	if err != nil {
		writeCollectionError(
			w,
			http.StatusInternalServerError,
			"account_router_projection_failed",
			"Failed to project account router",
			-1,
			nil,
		)
		return
	}
	releaseConfigMutation()
	writeCollectionJSON(w, http.StatusOK, map[string]any{
		"account_router":  resource,
		"config_revision": nextRevision,
		"effects":         agentEffectsForConfig(cfg),
	})
}

func accountRouterDeleteBlockers(
	cfg *config.Config,
	name string,
) (string, []string) {
	references := modelAccountReferences(cfg, name)
	sort.Strings(references)
	if cfg != nil && strings.TrimSpace(cfg.Agents.Defaults.AccountRef) == strings.TrimSpace(name) {
		return "default", references
	}
	if len(references) > 0 {
		return "referenced", references
	}
	return "", nil
}

func (h *Handler) handleMakeDefaultAccountRouter(w http.ResponseWriter, r *http.Request) {
	if !validateCollectionQueryParameters(w, r, "revision") {
		return
	}
	id, ok := accountRouterIDFromPath(w, r)
	if !ok {
		return
	}
	var request accountRouterRevisionRequest
	if !decodeCollectionJSON(w, r, &request) {
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
	index, name := findAccountRouterByResourceID(cfg, id)
	if index < 0 {
		writeCollectionError(
			w,
			http.StatusNotFound,
			"account_router_not_found",
			"Account router not found",
			-1,
			nil,
		)
		return
	}
	router := &cfg.AccountRouters[index]
	if !router.Enabled {
		writeCollectionError(
			w,
			http.StatusConflict,
			"account_router_disabled",
			"A disabled account router cannot be made default",
			-1,
			nil,
		)
		return
	}
	now := time.Now().UTC()
	if accountRouterStatus(cfg, router, now) != accountRouterStatusAvailable {
		writeCollectionError(
			w,
			http.StatusConflict,
			"account_router_unavailable",
			"An unavailable account router cannot be made default",
			-1,
			nil,
		)
		return
	}
	cfg.Agents.Defaults.AccountRef = name
	if validationErr := h.validateAccountRouterCandidate(cfg); validationErr != nil {
		writeCollectionError(
			w,
			http.StatusUnprocessableEntity,
			"invalid_account_router_configuration",
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
	resource, err := h.projectAccountRouterResource(cfg, cfg.AccountRouters[index], now)
	if err != nil {
		writeCollectionError(
			w,
			http.StatusInternalServerError,
			"account_router_projection_failed",
			"Failed to project account router",
			-1,
			nil,
		)
		return
	}
	releaseConfigMutation()
	writeCollectionJSON(w, http.StatusOK, map[string]any{
		"account_router":  resource,
		"config_revision": nextRevision,
		"effects":         agentEffectsForConfig(cfg),
	})
}

func (h *Handler) handleDeleteAccountRouter(w http.ResponseWriter, r *http.Request) {
	if !validateCollectionQueryParameters(w, r, "revision") {
		return
	}
	id, ok := accountRouterIDFromPath(w, r)
	if !ok {
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
	index, name := findAccountRouterByResourceID(cfg, id)
	if index < 0 {
		writeCollectionError(
			w,
			http.StatusNotFound,
			"account_router_not_found",
			"Account router not found",
			-1,
			nil,
		)
		return
	}
	if code, blockers := accountRouterDeleteBlockers(cfg, name); code != "" {
		writeCollectionError(
			w,
			http.StatusConflict,
			code,
			"Account router cannot be deleted while it is "+code,
			-1,
			blockers,
		)
		return
	}
	cfg.AccountRouters = append(cfg.AccountRouters[:index], cfg.AccountRouters[index+1:]...)
	if validationErr := h.validateAccountRouterCandidate(cfg); validationErr != nil {
		writeCollectionError(
			w,
			http.StatusUnprocessableEntity,
			"invalid_account_router_configuration",
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
	writeCollectionJSON(w, http.StatusOK, collectionBulkDeleteResponse{
		DeletedIDs:     []string{id},
		Failures:       []collectionBulkFailure{},
		ConfigRevision: nextRevision,
		Effects:        agentEffectsForConfig(cfg),
	})
}

func (h *Handler) handleBulkDeleteAccountRouters(w http.ResponseWriter, r *http.Request) {
	if !validateCollectionQueryParameters(w, r, "revision") {
		return
	}
	var request collectionBulkDeleteRequest
	if !decodeCollectionJSON(w, r, &request) {
		return
	}
	if len(request.IDs) == 0 || len(request.IDs) > collectionquery.MaxPageSize {
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
	deleteIDsByName := make(map[string]string, len(requested))
	for _, id := range requested {
		if !validAccountRouterResourceID(id) {
			failures = append(failures, collectionBulkFailure{ID: id, Code: "invalid_id"})
			continue
		}
		_, name := findAccountRouterByResourceID(cfg, id)
		if name == "" {
			failures = append(failures, collectionBulkFailure{ID: id, Code: "not_found"})
			continue
		}
		if code, blockers := accountRouterDeleteBlockers(cfg, name); code != "" {
			failures = append(failures, collectionBulkFailure{
				ID: id, Code: code, Blockers: blockers,
			})
			continue
		}
		deleteIDsByName[name] = id
	}

	deleted := make([]string, 0, len(deleteIDsByName))
	kept := make(config.AccountRouterList, 0, len(cfg.AccountRouters)-len(deleteIDsByName))
	for _, router := range cfg.AccountRouters {
		name := canonicalAccountRouterName(router.Name)
		if id, deleting := deleteIDsByName[name]; deleting {
			deleted = append(deleted, id)
			continue
		}
		kept = append(kept, router)
	}
	cfg.AccountRouters = kept
	nextRevision := revision
	if len(deleted) > 0 {
		if validationErr := h.validateAccountRouterCandidate(cfg); validationErr != nil {
			writeCollectionError(
				w,
				http.StatusUnprocessableEntity,
				"invalid_account_router_configuration",
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
	writeCollectionJSON(w, http.StatusOK, collectionBulkDeleteResponse{
		DeletedIDs:     deleted,
		Failures:       failures,
		ConfigRevision: nextRevision,
		Effects:        agentEffectsForConfig(cfg),
	})
}
