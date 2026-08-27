package api

import (
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/sipeed/picoclaw/pkg/collectionquery"
	"github.com/sipeed/picoclaw/pkg/config"
)

const toolCollectionIDNamespace = "tool"

const toolCollectionRouteIDMaxBytes = 128

var toolCollectionSchema = mustCollectionQuerySchema(
	[]collectionquery.FieldSchema{
		{Name: "name", Type: collectionquery.TypeString, Sortable: true},
		{
			Name: "category", Type: collectionquery.TypeEnum, Sortable: true,
			SuggestedValues: []string{
				"agents", "automation", "communication", "discovery",
				"filesystem", "hardware", "skills", "web",
			},
		},
		{
			Name: "status", Type: collectionquery.TypeEnum, Sortable: true,
			SuggestedValues: []string{"enabled", "disabled", "blocked"},
		},
		{Name: "reason", Type: collectionquery.TypeString, Sortable: true},
		{Name: "config_key", Type: collectionquery.TypeString, Sortable: true},
	},
	[]collectionquery.SortField{
		{Field: "category", Direction: collectionquery.Ascending},
		{Field: "name", Direction: collectionquery.Ascending},
	},
)

func toolCollectionResourceID(name string) string {
	id, _ := encodeCollectionResourceID(
		toolCollectionIDNamespace,
		strings.ToLower(strings.TrimSpace(name)),
	)
	return id
}

func resolveToolStateTarget(idOrName string) (string, int, string) {
	if idOrName == "" || idOrName != strings.TrimSpace(idOrName) ||
		!utf8.ValidString(idOrName) || len(idOrName) > toolCollectionRouteIDMaxBytes {
		return "", http.StatusBadRequest, "invalid_tool_id"
	}
	for _, entry := range toolCatalog {
		if entry.Name == idOrName || toolCollectionResourceID(entry.Name) == idOrName {
			return entry.Name, http.StatusOK, ""
		}
	}
	if len(idOrName) == collectionResourceIDEncodedBytes &&
		!validCollectionResourceID(idOrName) {
		return "", http.StatusBadRequest, "invalid_tool_id"
	}
	return "", http.StatusNotFound, "tool_not_found"
}

func pageToolSupportItems(
	items []toolSupportItem,
	request collectionListRequest,
) (collectionquery.PageResult[toolSupportItem], error) {
	return collectionquery.Paginate(
		items,
		request.Query,
		request.Cursor,
		request.Limit,
		request.Now,
		collectionquery.PageOptions[toolSupportItem]{
			ID:         func(item toolSupportItem) (string, error) { return item.ID, nil },
			ValidateID: validCollectionResourceID,
			Clone:      func(item toolSupportItem) toolSupportItem { return item },
			Resolve: func(
				item toolSupportItem,
				field collectionquery.Field,
				_ time.Time,
			) (collectionquery.FieldValue, bool) {
				switch field {
				case "name":
					return collectionquery.StringValue(item.Name), true
				case "category":
					return collectionquery.EnumValue(item.Category), true
				case "status":
					return collectionquery.EnumValue(item.Status), true
				case "reason":
					return collectionquery.StringValue(item.Reason), true
				case "config_key":
					return collectionquery.StringValue(item.ConfigKey), true
				default:
					return collectionquery.FieldValue{}, false
				}
			},
		},
	)
}

func toolCollectionSchemaWithSuggestions(items []toolSupportItem) collectionquery.Schema {
	names := make([]string, 0, len(items))
	categories := make([]string, 0, len(items))
	reasons := make([]string, 0, len(items))
	configKeys := make([]string, 0, len(items))
	for _, item := range items {
		names = append(names, item.Name)
		categories = append(categories, item.Category)
		reasons = append(reasons, item.Reason)
		configKeys = append(configKeys, item.ConfigKey)
	}
	return collectionSchemaWithSuggestions(
		toolCollectionSchema,
		map[collectionquery.Field][]string{
			"name": names, "category": categories, "reason": reasons,
			"config_key": configKeys,
		},
	)
}

func resolveToolSupportItem(items []toolSupportItem, idOrName string) (toolSupportItem, bool) {
	for _, item := range items {
		if item.ID == idOrName {
			return item, true
		}
	}
	// Name lookup keeps state callers and diagnostic clients compatible with
	// the pre-collection API. Routed details use only the issued ID.
	for _, item := range items {
		if item.Name == idOrName {
			return item, true
		}
	}
	return toolSupportItem{}, false
}

func (h *Handler) handleGetTool(w http.ResponseWriter, r *http.Request) {
	if !validateCollectionQueryParameters(w, r) {
		return
	}
	cfg, err := config.LoadConfig(h.configPath)
	if err != nil {
		writeCollectionError(
			w, http.StatusInternalServerError, "config_load_failed",
			"Failed to load configuration", -1, nil,
		)
		return
	}
	item, found := resolveToolSupportItem(buildToolSupport(cfg), r.PathValue("id"))
	if !found {
		writeCollectionError(w, http.StatusNotFound, "tool_not_found", "Tool not found", -1, nil)
		return
	}
	writeCollectionJSON(w, http.StatusOK, map[string]toolSupportItem{"tool": item})
}
