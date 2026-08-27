package api

import (
	"errors"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/sipeed/picoclaw/pkg/auth"
	"github.com/sipeed/picoclaw/pkg/collectionquery"
)

const accountCollectionIDNamespace = "account"

var accountCollectionSchema = mustCollectionQuerySchema(
	[]collectionquery.FieldSchema{
		{Name: "id", Type: collectionquery.TypeString, Sortable: true},
		{Name: "provider", Type: collectionquery.TypeString, Sortable: true},
		{Name: "account", Type: collectionquery.TypeString, Sortable: true},
		{
			Name: "status", Type: collectionquery.TypeEnum, Sortable: true,
			SuggestedValues: []string{"connected", "needs_refresh", "expired", "not_logged_in"},
		},
		{Name: "auth_method", Type: collectionquery.TypeString, Sortable: true},
		// Account credentials may be non-expiring. Collection queries do not
		// have a nullable timestamp type, so this field is an RFC3339 string or
		// the empty string rather than a fabricated timestamp.
		{Name: "expires_at", Type: collectionquery.TypeString, Sortable: true},
	},
	[]collectionquery.SortField{
		{Field: "provider", Direction: collectionquery.Ascending},
		{Field: "id", Direction: collectionquery.Ascending},
	},
)

// accountCollectionResource is deliberately narrower than AuthCredential and
// oauthProviderStatus. In particular, credential material, account metadata,
// email addresses, and project identifiers never enter this projection.
type accountCollectionResource struct {
	ID         string `json:"id"`
	Provider   string `json:"provider"`
	Account    string `json:"account"`
	Status     string `json:"status"`
	AuthMethod string `json:"auth_method"`
	ExpiresAt  string `json:"expires_at"`
}

type accountsCollectionResponse struct {
	Accounts       []accountCollectionResource `json:"accounts"`
	Total          int                         `json:"total"`
	NextCursor     string                      `json:"next_cursor,omitempty"`
	CanonicalQuery string                      `json:"canonical_query,omitempty"`
	QuerySchema    collectionquery.Schema      `json:"query_schema"`
}

type accountItemResponse struct {
	Account accountCollectionResource `json:"account"`
}

func (h *Handler) registerAccountRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/accounts", h.handleListAccounts)
	mux.HandleFunc("GET /api/accounts/{id}", h.handleGetAccount)
}

func (h *Handler) handleListAccounts(w http.ResponseWriter, r *http.Request) {
	listRequest, ok := parseCollectionListRequest(w, r, accountCollectionSchema)
	if !ok {
		return
	}
	evaluatedAt := listRequest.Now
	if listRequest.Cursor != "" {
		cursor, err := collectionquery.DecodeCursor(
			listRequest.Cursor,
			listRequest.Query,
			validCollectionResourceID,
		)
		if err != nil {
			writeCollectionPageError(w, err)
			return
		}
		evaluatedAt = cursor.EvaluatedAt
	}
	resources, ok := h.loadAccountCollectionResources(w, evaluatedAt)
	if !ok {
		return
	}
	page, err := collectionquery.Paginate(
		resources,
		listRequest.Query,
		listRequest.Cursor,
		listRequest.Limit,
		listRequest.Now,
		accountCollectionPageOptions(),
	)
	if err != nil {
		writeCollectionPageError(w, err)
		return
	}

	providers := make([]string, 0, len(resources))
	accounts := make([]string, 0, len(resources))
	authMethods := make([]string, 0, len(resources))
	for _, resource := range resources {
		providers = append(providers, resource.Provider)
		accounts = append(accounts, resource.Account)
		authMethods = append(authMethods, resource.AuthMethod)
	}
	writeCollectionJSON(w, http.StatusOK, accountsCollectionResponse{
		Accounts:       page.Items,
		Total:          page.Total,
		NextCursor:     page.NextCursor,
		CanonicalQuery: listRequest.Query.Canonical(),
		QuerySchema: collectionSchemaWithSuggestions(
			accountCollectionSchema,
			map[collectionquery.Field][]string{
				"provider": providers, "account": accounts, "auth_method": authMethods,
			},
		),
	})
}

func (h *Handler) handleGetAccount(w http.ResponseWriter, r *http.Request) {
	if !validateCollectionQueryParameters(w, r) {
		return
	}
	id := r.PathValue("id")
	if !validCollectionResourceID(id) {
		writeCollectionError(
			w,
			http.StatusBadRequest,
			"invalid_account_id",
			"Account ID is invalid",
			-1,
			nil,
		)
		return
	}
	resources, ok := h.loadAccountCollectionResources(w, time.Now().UTC())
	if !ok {
		return
	}
	for _, resource := range resources {
		if collectionResourceIDMatches(accountCollectionIDNamespace, id, resource.Account) {
			writeCollectionJSON(w, http.StatusOK, accountItemResponse{Account: resource})
			return
		}
	}
	writeCollectionError(
		w,
		http.StatusNotFound,
		"account_not_found",
		"Account not found",
		-1,
		nil,
	)
}

func (h *Handler) loadAccountCollectionResources(
	w http.ResponseWriter,
	evaluatedAt time.Time,
) ([]accountCollectionResource, bool) {
	store, err := oauthLoadStore()
	if err != nil || store == nil {
		writeCollectionError(
			w,
			http.StatusInternalServerError,
			"account_store_failed",
			"Failed to load account credentials",
			-1,
			nil,
		)
		return nil, false
	}
	resources, err := projectAccountCollectionResources(store, evaluatedAt)
	if err != nil {
		writeCollectionError(
			w,
			http.StatusInternalServerError,
			"account_projection_failed",
			"Failed to project account credentials",
			-1,
			nil,
		)
		return nil, false
	}
	return resources, true
}

func projectAccountCollectionResources(
	store *auth.AuthStore,
	evaluatedAt time.Time,
) ([]accountCollectionResource, error) {
	if store == nil {
		return nil, errors.New("account credential store is nil")
	}
	if evaluatedAt.IsZero() {
		return nil, errors.New("account evaluation time is required")
	}
	credentialIDs := make([]string, 0, len(store.Credentials))
	for credentialID, credential := range store.Credentials {
		if credential == nil {
			continue
		}
		credentialIDs = append(credentialIDs, strings.ToLower(strings.TrimSpace(credentialID)))
	}
	sort.Strings(credentialIDs)

	resources := make([]accountCollectionResource, 0, len(credentialIDs))
	for _, provider := range accountProviderIDs() {
		for _, credentialID := range credentialIDs {
			if !credentialIDBelongsToProvider(provider, credentialID) {
				continue
			}
			credential := store.Credentials[credentialID]
			if credential == nil {
				continue
			}
			id, err := encodeCollectionResourceID(accountCollectionIDNamespace, credentialID)
			if err != nil {
				return nil, err
			}
			// Keep status calculation and expiry formatting aligned with the
			// established OAuth provider response.
			status := newOAuthProviderCredentialStatusAt(
				provider,
				credentialID,
				credential,
				evaluatedAt,
			)
			resources = append(resources, accountCollectionResource{
				ID:         id,
				Provider:   provider,
				Account:    credentialID,
				Status:     status.Status,
				AuthMethod: strings.ToLower(strings.TrimSpace(status.AuthMethod)),
				ExpiresAt:  status.ExpiresAt,
			})
		}
	}
	return resources, nil
}

func accountCollectionPageOptions() collectionquery.PageOptions[accountCollectionResource] {
	return collectionquery.PageOptions[accountCollectionResource]{
		ID: func(account accountCollectionResource) (string, error) {
			return account.ID, nil
		},
		ValidateID: validCollectionResourceID,
		Resolve: func(
			account accountCollectionResource,
			field collectionquery.Field,
			_ time.Time,
		) (collectionquery.FieldValue, bool) {
			return accountCollectionField(account, field)
		},
	}
}

func accountCollectionField(
	account accountCollectionResource,
	field collectionquery.Field,
) (collectionquery.FieldValue, bool) {
	switch field {
	case "id":
		return collectionquery.StringValue(account.ID), true
	case "provider":
		return collectionquery.StringValue(account.Provider), true
	case "account":
		return collectionquery.StringValue(account.Account), true
	case "status":
		return collectionquery.EnumValue(account.Status), true
	case "auth_method":
		return collectionquery.StringValue(account.AuthMethod), true
	case "expires_at":
		return collectionquery.StringValue(account.ExpiresAt), true
	default:
		return collectionquery.FieldValue{}, false
	}
}
