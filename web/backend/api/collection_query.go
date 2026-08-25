package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/sipeed/picoclaw/pkg/collectionquery"
)

type collectionListRequest struct {
	Query  collectionquery.Query
	Cursor string
	Limit  int
	Now    time.Time
}

func mustCollectionQuerySchema(
	fields []collectionquery.FieldSchema,
	defaultOrder []collectionquery.SortField,
) collectionquery.Schema {
	schema, err := collectionquery.NewSchema(fields, defaultOrder)
	if err != nil {
		panic(err)
	}
	return schema
}

func collectionSchemaWithSuggestions(
	schema collectionquery.Schema,
	suggestions map[collectionquery.Field][]string,
) collectionquery.Schema {
	projected := schema.Clone()
	for index := range projected.Fields {
		values, ok := suggestions[projected.Fields[index].Name]
		if !ok || projected.Fields[index].Type == collectionquery.TypeEnum {
			continue
		}
		seen := make(map[string]struct{}, len(values))
		bounded := make([]string, 0, min(len(values), collectionquery.MaxSuggestedValues))
		for _, value := range values {
			value = strings.TrimSpace(value)
			key := strings.ToLower(value)
			if value == "" || len(value) > collectionquery.MaxSuggestedValueBytes {
				continue
			}
			if _, duplicate := seen[key]; duplicate {
				continue
			}
			seen[key] = struct{}{}
			bounded = append(bounded, value)
			if len(bounded) == collectionquery.MaxSuggestedValues {
				break
			}
		}
		projected.Fields[index].SuggestedValues = bounded
	}
	return projected
}

func parseCollectionListRequest(
	w http.ResponseWriter,
	r *http.Request,
	schema collectionquery.Schema,
) (collectionListRequest, bool) {
	if r == nil || r.URL == nil {
		writeCollectionError(w, http.StatusBadRequest, "invalid_collection_request", "Invalid collection request", -1, nil)
		return collectionListRequest{}, false
	}
	values, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		writeCollectionError(w, http.StatusBadRequest, "invalid_collection_request", "Collection query parameters are malformed", -1, nil)
		return collectionListRequest{}, false
	}
	for key, entries := range values {
		if key != "query" && key != "cursor" && key != "limit" || len(entries) != 1 {
			writeCollectionError(w, http.StatusBadRequest, "invalid_collection_request", "Only query, cursor, and limit are supported", -1, nil)
			return collectionListRequest{}, false
		}
	}
	limit := 0
	if rawLimit := strings.TrimSpace(values.Get("limit")); rawLimit != "" {
		parsed, err := strconv.Atoi(rawLimit)
		if err != nil || parsed < 1 || parsed > collectionquery.MaxPageSize {
			writeCollectionError(w, http.StatusBadRequest, "invalid_page_limit", "Limit must be between 1 and 200", -1, nil)
			return collectionListRequest{}, false
		}
		limit = parsed
	}
	query, err := collectionquery.Parse(values.Get("query"), schema)
	if err != nil {
		var queryError *collectionquery.QueryError
		position := 0
		message := "Invalid collection query"
		if errors.As(err, &queryError) {
			position = queryError.Position
			message = queryError.Message
		}
		writeCollectionError(w, http.StatusBadRequest, "invalid_query", message, position, nil)
		return collectionListRequest{}, false
	}
	return collectionListRequest{
		Query: query, Cursor: values.Get("cursor"), Limit: limit, Now: time.Now().UTC(),
	}, true
}

func validateCollectionQueryParameters(
	w http.ResponseWriter,
	r *http.Request,
	allowed ...string,
) bool {
	if r == nil || r.URL == nil {
		writeCollectionError(w, http.StatusBadRequest, "invalid_collection_request", "Invalid collection request", -1, nil)
		return false
	}
	values, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		writeCollectionError(w, http.StatusBadRequest, "invalid_collection_request", "Collection query parameters are malformed", -1, nil)
		return false
	}
	allowlist := make(map[string]struct{}, len(allowed))
	for _, key := range allowed {
		allowlist[key] = struct{}{}
	}
	for key, entries := range values {
		if _, ok := allowlist[key]; !ok || len(entries) != 1 {
			writeCollectionError(w, http.StatusBadRequest, "invalid_collection_request", "Unsupported collection query parameter", -1, nil)
			return false
		}
	}
	return true
}

func writeCollectionJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func writeCollectionPageError(w http.ResponseWriter, err error) {
	if errors.Is(err, collectionquery.ErrInvalidCursor) {
		writeCollectionError(w, http.StatusBadRequest, "invalid_cursor", "The cursor does not match this query", -1, nil)
		return
	}
	writeCollectionError(w, http.StatusInternalServerError, "collection_page_failed", "Failed to page collection results", -1, nil)
}
