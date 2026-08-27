package api

import (
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/sipeed/picoclaw/pkg/collectionquery"
	"github.com/sipeed/picoclaw/pkg/config"
)

const skillCollectionIDNamespace = "skill"

var skillCollectionSchema = mustCollectionQuerySchema(
	[]collectionquery.FieldSchema{
		{Name: "name", Type: collectionquery.TypeString, Sortable: true},
		{
			Name: "source", Type: collectionquery.TypeEnum, Sortable: true,
			SuggestedValues: []string{"workspace", "global", "builtin"},
		},
		{
			Name: "origin", Type: collectionquery.TypeEnum, Sortable: true,
			SuggestedValues: []string{"builtin", "manual", "third_party"},
		},
		{Name: "registry", Type: collectionquery.TypeString, Sortable: true},
		{Name: "version", Type: collectionquery.TypeString, Sortable: true},
		// The compatibility wire contract stores installation time as Unix
		// milliseconds, so the collection schema keeps the field numeric.
		{Name: "installed_at", Type: collectionquery.TypeNumber, Sortable: true},
	},
	[]collectionquery.SortField{{Field: "name", Direction: collectionquery.Ascending}},
)

var removeWorkspaceSkillDir = os.RemoveAll

type skillBulkDeleteResponse struct {
	DeletedIDs []string                `json:"deleted_ids"`
	Failures   []collectionBulkFailure `json:"failures"`
}

func finalizeSkillSupportItem(item skillSupportItem) (skillSupportItem, error) {
	item.Name = strings.TrimSpace(item.Name)
	item.Source = strings.ToLower(strings.TrimSpace(item.Source))
	item.OriginKind = strings.ToLower(strings.TrimSpace(item.OriginKind))
	if item.OriginKind == "" {
		item.OriginKind = "builtin"
	}
	item.Origin = item.OriginKind

	item.RegistryName = strings.TrimSpace(item.RegistryName)
	if item.RegistryName == "" {
		item.RegistryName = strings.TrimSpace(item.Registry)
	}
	item.Registry = item.RegistryName

	item.InstalledVersion = strings.TrimSpace(item.InstalledVersion)
	if item.InstalledVersion == "" {
		item.InstalledVersion = strings.TrimSpace(item.Version)
	}
	item.Version = item.InstalledVersion
	item.Removable = item.Source == "workspace"

	id, err := encodeCollectionResourceID(
		skillCollectionIDNamespace,
		skillCollectionIdentity(item.Name),
	)
	if err != nil {
		return skillSupportItem{}, err
	}
	item.ID = id
	return item, nil
}

func skillCollectionIdentity(name string) string {
	return strings.ToLower(strings.TrimSpace(name))
}

func pageSkillSupportItems(
	items []skillSupportItem,
	request collectionListRequest,
) (collectionquery.PageResult[skillSupportItem], error) {
	return collectionquery.Paginate(
		items,
		request.Query,
		request.Cursor,
		request.Limit,
		request.Now,
		collectionquery.PageOptions[skillSupportItem]{
			ID:         func(item skillSupportItem) (string, error) { return item.ID, nil },
			ValidateID: validCollectionResourceID,
			Clone:      func(item skillSupportItem) skillSupportItem { return item },
			Resolve: func(
				item skillSupportItem,
				field collectionquery.Field,
				_ time.Time,
			) (collectionquery.FieldValue, bool) {
				switch field {
				case "name":
					return collectionquery.StringValue(item.Name), true
				case "source":
					return collectionquery.EnumValue(item.Source), true
				case "origin":
					return collectionquery.EnumValue(item.Origin), true
				case "registry":
					return collectionquery.StringValue(item.Registry), true
				case "version":
					return collectionquery.StringValue(item.Version), true
				case "installed_at":
					return collectionquery.NumberValue(float64(item.InstalledAt)), true
				default:
					return collectionquery.FieldValue{}, false
				}
			},
		},
	)
}

func skillCollectionSchemaWithSuggestions(items []skillSupportItem) collectionquery.Schema {
	names := make([]string, 0, len(items))
	registries := make([]string, 0, len(items))
	versions := make([]string, 0, len(items))
	for _, item := range items {
		names = append(names, item.Name)
		registries = append(registries, item.Registry)
		versions = append(versions, item.Version)
	}
	return collectionSchemaWithSuggestions(
		skillCollectionSchema,
		map[collectionquery.Field][]string{
			"name": names, "registry": registries, "version": versions,
		},
	)
}

func resolveSkillSupportItem(items []skillSupportItem, idOrName string) (skillSupportItem, bool) {
	for _, item := range items {
		if item.ID == idOrName {
			return item, true
		}
	}
	// Compatibility: callers of the pre-collection API addressed details and
	// deletions by the metadata name. New clients use only the issued ID.
	for _, item := range items {
		if item.Name == idOrName {
			return item, true
		}
	}
	return skillSupportItem{}, false
}

func removeWorkspaceSkill(cfg *config.Config, item skillSupportItem) error {
	if cfg == nil || !item.Removable || item.Source != "workspace" ||
		filepath.Base(item.Path) != "SKILL.md" {
		return errors.New("invalid workspace skill deletion target")
	}
	root := filepath.Clean(filepath.Join(cfg.WorkspacePath(), "skills"))
	target := filepath.Clean(filepath.Dir(item.Path))
	relative, err := filepath.Rel(root, target)
	if err != nil || relative == "." || relative == ".." || filepath.IsAbs(relative) ||
		strings.HasPrefix(relative, ".."+string(filepath.Separator)) ||
		strings.ContainsRune(relative, filepath.Separator) {
		return errors.New("workspace skill deletion target escapes skills root")
	}
	return removeWorkspaceSkillDir(target)
}

func (h *Handler) handleBulkDeleteSkills(w http.ResponseWriter, r *http.Request) {
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
	cfg, err := config.LoadConfig(h.configPath)
	if err != nil {
		writeCollectionError(
			w, http.StatusInternalServerError, "config_load_failed",
			"Failed to load configuration", -1, nil,
		)
		return
	}

	workspaceSkillWriteMu.Lock()
	defer workspaceSkillWriteMu.Unlock()
	items, err := buildSkillSupportItems(cfg)
	if err != nil {
		writeCollectionError(
			w, http.StatusInternalServerError, "skill_projection_failed",
			"Failed to project installed skills", -1, nil,
		)
		return
	}
	byID := make(map[string]skillSupportItem, len(items))
	for _, item := range items {
		byID[item.ID] = item
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
		if !item.Removable {
			failures = append(failures, collectionBulkFailure{ID: id, Code: "read_only_origin"})
			continue
		}
		if err := removeWorkspaceSkill(cfg, item); err != nil {
			failures = append(failures, collectionBulkFailure{ID: id, Code: "delete_failed"})
			continue
		}
		deleted = append(deleted, id)
	}
	if failures == nil {
		failures = []collectionBulkFailure{}
	}
	sort.Strings(deleted)
	sortCollectionFailures(failures)
	writeCollectionJSON(w, http.StatusOK, skillBulkDeleteResponse{
		DeletedIDs: deleted,
		Failures:   failures,
	})
}
