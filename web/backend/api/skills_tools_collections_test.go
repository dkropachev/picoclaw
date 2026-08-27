package api

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/sipeed/picoclaw/pkg/collectionquery"
	"github.com/sipeed/picoclaw/pkg/config"
)

type skillsCollectionFixture struct {
	configPath string
	workspace  string
	mux        *http.ServeMux
}

func newSkillsCollectionFixture(t *testing.T) skillsCollectionFixture {
	t.Helper()
	configPath, cleanup := setupOAuthTestEnv(t)
	t.Cleanup(cleanup)
	cfg, loadErr := config.LoadConfig(configPath)
	if loadErr != nil {
		t.Fatal(loadErr)
	}
	workspace := filepath.Join(t.TempDir(), "workspace")
	cfg.Agents.Defaults.Workspace = workspace
	if saveErr := config.SaveConfig(configPath, cfg); saveErr != nil {
		t.Fatal(saveErr)
	}
	t.Setenv("PICOCLAW_BUILTIN_SKILLS", filepath.Join(t.TempDir(), "builtin"))
	if err := os.MkdirAll(os.Getenv("PICOCLAW_BUILTIN_SKILLS"), 0o755); err != nil {
		t.Fatal(err)
	}
	handler := NewHandler(configPath)
	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)
	return skillsCollectionFixture{configPath: configPath, workspace: workspace, mux: mux}
}

func writeCollectionSkill(
	t *testing.T,
	root, directory, name, description, body string,
	meta *installedSkillOriginMeta,
) string {
	t.Helper()
	dir := filepath.Join(root, directory)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	content := "---\nname: " + name + "\ndescription: " + description + "\n---\n" + body
	path := filepath.Join(dir, "SKILL.md")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	if meta != nil {
		if err := writeSkillOriginMeta(dir, *meta); err != nil {
			t.Fatal(err)
		}
	}
	return path
}

func requestCollectionAPI(
	t *testing.T,
	mux *http.ServeMux,
	method, target, body, contentType string,
) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(method, target, strings.NewReader(body))
	if contentType != "" {
		request.Header.Set("Content-Type", contentType)
	}
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, request)
	return response
}

func decodeSkillsCollection(t *testing.T, response *httptest.ResponseRecorder) skillSupportResponse {
	t.Helper()
	if response.Code != http.StatusOK {
		t.Fatalf("skills status=%d body=%s", response.Code, response.Body.String())
	}
	var result skillSupportResponse
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	return result
}

func TestSkillsCollectionQueryPagingDetailAndUTF8Errors(t *testing.T) {
	fixture := newSkillsCollectionFixture(t)
	manualAt := int64(1_725_000_000_000)
	writeCollectionSkill(
		t, filepath.Join(fixture.workspace, "skills"), "alpha-dir", "Alpha", "Alpha skill",
		"# Alpha\nPRIVATE_DETAIL_ONLY\n",
		&installedSkillOriginMeta{Version: 1, OriginKind: "manual", InstalledAt: manualAt},
	)
	writeCollectionSkill(
		t, filepath.Join(fixture.workspace, "skills"), "zeta-dir", "zeta", "Zeta skill",
		"# Zeta\n",
		&installedSkillOriginMeta{
			Version: 1, OriginKind: "third_party", Registry: "github",
			InstalledVersion: "1.2.3", InstalledAt: manualAt + 1,
		},
	)
	writeCollectionSkill(
		t, filepath.Join(globalConfigDir(), "skills"), "gamma-dir", "gamma", "Gamma skill",
		"# Gamma\n", nil,
	)

	firstResponse := requestCollectionAPI(t, fixture.mux, http.MethodGet, "/api/skills?limit=2", "", "")
	first := decodeSkillsCollection(t, firstResponse)
	if first.Total != 3 || len(first.Skills) != 2 || first.NextCursor == "" ||
		first.CanonicalQuery != "ALL ORDER BY name ASC" {
		t.Fatalf("first skills page=%#v", first)
	}
	if first.Skills[0].Name != "Alpha" || first.Skills[1].Name != "gamma" ||
		strings.Contains(firstResponse.Body.String(), "PRIVATE_DETAIL_ONLY") {
		t.Fatalf("default ordering/list projection=%#v body=%s", first.Skills, firstResponse.Body.String())
	}
	for _, item := range first.Skills {
		if !validCollectionResourceID(item.ID) || item.Origin != item.OriginKind ||
			item.Registry != item.RegistryName || item.Version != item.InstalledVersion {
			t.Fatalf("skill aliases/ID=%#v", item)
		}
	}
	second := decodeSkillsCollection(t, requestCollectionAPI(
		t, fixture.mux, http.MethodGet,
		"/api/skills?limit=2&cursor="+url.QueryEscape(first.NextCursor), "", "",
	))
	if second.Total != 3 || len(second.Skills) != 1 || second.Skills[0].Name != "zeta" {
		t.Fatalf("second skills page=%#v", second)
	}

	query := `source = workspace AND origin = manual ORDER BY installed_at DESC`
	filtered := decodeSkillsCollection(t, requestCollectionAPI(
		t, fixture.mux, http.MethodGet,
		"/api/skills?query="+url.QueryEscape(query), "", "",
	))
	if filtered.Total != 1 || len(filtered.Skills) != 1 ||
		filtered.Skills[0].Name != "Alpha" || !filtered.Skills[0].Removable ||
		filtered.Skills[0].InstalledAt != manualAt {
		t.Fatalf("filtered skills=%#v", filtered.Skills)
	}
	wantFields := map[collectionquery.Field]bool{
		"name": true, "source": true, "origin": true, "registry": true,
		"version": true, "installed_at": true,
	}
	for _, field := range filtered.QuerySchema.Fields {
		delete(wantFields, field.Name)
	}
	if len(wantFields) != 0 {
		t.Fatalf("skill query schema omitted fields=%v", wantFields)
	}
	if !containsExactString(schemaFieldSuggestions(filtered.QuerySchema, "name"), "Alpha") ||
		!containsExactString(schemaFieldSuggestions(filtered.QuerySchema, "registry"), "github") ||
		!containsExactString(schemaFieldSuggestions(filtered.QuerySchema, "version"), "1.2.3") {
		t.Fatalf("skill dynamic suggestions=%#v", filtered.QuerySchema.Fields)
	}
	registryQuery := `registry = github AND version = "1.2.3" AND installed_at >= 1`
	registryFiltered := decodeSkillsCollection(t, requestCollectionAPI(
		t, fixture.mux, http.MethodGet,
		"/api/skills?query="+url.QueryEscape(registryQuery), "", "",
	))
	if registryFiltered.Total != 1 || registryFiltered.Skills[0].Name != "zeta" {
		t.Fatalf("registry/version query=%#v", registryFiltered.Skills)
	}

	alpha := filtered.Skills[0]
	wantID, err := encodeCollectionResourceID(skillCollectionIDNamespace, "alpha")
	if err != nil || alpha.ID != wantID || alpha.ID == alpha.Name {
		t.Fatalf("alpha ID=%q want=%q err=%v", alpha.ID, wantID, err)
	}
	if skillCollectionIdentity(" Alpha ") != "alpha" {
		t.Fatalf("skill identity was not case/space canonical")
	}
	detailResponse := requestCollectionAPI(
		t, fixture.mux, http.MethodGet, "/api/skills/"+alpha.ID, "", "",
	)
	if detailResponse.Code != http.StatusOK {
		t.Fatalf("detail status=%d body=%s", detailResponse.Code, detailResponse.Body.String())
	}
	var detail skillDetailResponse
	if err := json.Unmarshal(detailResponse.Body.Bytes(), &detail); err != nil {
		t.Fatal(err)
	}
	if detail.ID != alpha.ID || detail.Content != "# Alpha\nPRIVATE_DETAIL_ONLY\n" {
		t.Fatalf("detail=%#v", detail)
	}
	legacyDetail := requestCollectionAPI(
		t, fixture.mux, http.MethodGet, "/api/skills/Alpha", "", "",
	)
	if legacyDetail.Code != http.StatusOK {
		t.Fatalf("legacy detail status=%d body=%s", legacyDetail.Code, legacyDetail.Body.String())
	}

	mismatch := requestCollectionAPI(
		t, fixture.mux, http.MethodGet,
		"/api/skills?query="+url.QueryEscape("ORDER BY name DESC")+"&cursor="+
			url.QueryEscape(first.NextCursor), "", "",
	)
	if mismatch.Code != http.StatusBadRequest ||
		decodeCollectionErrorCode(t, mismatch.Body.Bytes()) != "invalid_cursor" {
		t.Fatalf("cursor mismatch=%d/%s", mismatch.Code, mismatch.Body.String())
	}
	unicodeQuery := "name = café AND unknown = value"
	invalidQuery := requestCollectionAPI(
		t, fixture.mux, http.MethodGet,
		"/api/skills?query="+url.QueryEscape(unicodeQuery), "", "",
	)
	var structured struct {
		Code     string `json:"code"`
		Message  string `json:"message"`
		Position int    `json:"position"`
	}
	if err := json.Unmarshal(invalidQuery.Body.Bytes(), &structured); err != nil {
		t.Fatal(err)
	}
	if invalidQuery.Code != http.StatusBadRequest || structured.Code != "invalid_query" ||
		structured.Position != strings.Index(unicodeQuery, "unknown") ||
		len(structured.Message) == 0 || len(structured.Message) > collectionquery.MaxQueryErrorMessageLen {
		t.Fatalf("structured UTF-8 error=%#v", structured)
	}
	for _, target := range []string{
		"/api/skills?limit=201",
		"/api/skills?unsupported=true",
		"/api/skills/" + alpha.ID + "?query=ALL",
		"/api/skills/" + toolCollectionResourceID("missing-skill"),
	} {
		response := requestCollectionAPI(t, fixture.mux, http.MethodGet, target, "", "")
		if response.Code != http.StatusBadRequest && response.Code != http.StatusNotFound {
			t.Fatalf("invalid request %q status=%d body=%s", target, response.Code, response.Body.String())
		}
	}
}

func schemaFieldSuggestions(schema collectionquery.Schema, name collectionquery.Field) []string {
	for _, field := range schema.Fields {
		if field.Name == name {
			return field.SuggestedValues
		}
	}
	return nil
}

func containsExactString(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}

func TestSkillsCollectionDeletionStableIdentityAndPartialBulkFailures(t *testing.T) {
	fixture := newSkillsCollectionFixture(t)
	workspaceSkills := filepath.Join(fixture.workspace, "skills")
	sharedWorkspace := writeCollectionSkill(
		t, workspaceSkills, "shared-workspace", "shared", "Workspace shared", "# Workspace\n", nil,
	)
	deletePath := writeCollectionSkill(
		t, workspaceSkills, "delete-dir", "delete-me", "Delete me", "# Delete\n", nil,
	)
	failDeletePath := writeCollectionSkill(
		t, workspaceSkills, "fail-delete-dir", "fail-delete", "Fail delete", "# Fail\n", nil,
	)
	globalShared := writeCollectionSkill(
		t, filepath.Join(globalConfigDir(), "skills"), "shared-global", "shared", "Global shared",
		"# Global\n", nil,
	)

	before := decodeSkillsCollection(t, requestCollectionAPI(
		t, fixture.mux, http.MethodGet, "/api/skills", "", "",
	))
	byName := make(map[string]skillSupportItem, len(before.Skills))
	for _, item := range before.Skills {
		byName[item.Name] = item
	}
	shared := byName["shared"]
	deleteMe := byName["delete-me"]
	failDelete := byName["fail-delete"]
	if shared.Source != "workspace" || !shared.Removable || deleteMe.ID == "" || failDelete.ID == "" {
		t.Fatalf("initial skills=%#v", before.Skills)
	}

	deletedShared := requestCollectionAPI(
		t, fixture.mux, http.MethodDelete, "/api/skills/"+shared.ID, "", "",
	)
	if deletedShared.Code != http.StatusOK {
		t.Fatalf("delete shared=%d/%s", deletedShared.Code, deletedShared.Body.String())
	}
	if _, err := os.Stat(filepath.Dir(sharedWorkspace)); !os.IsNotExist(err) {
		t.Fatalf("workspace skill remains: %v", err)
	}
	after := decodeSkillsCollection(t, requestCollectionAPI(
		t, fixture.mux, http.MethodGet,
		"/api/skills?query="+url.QueryEscape(`name = shared`), "", "",
	))
	if after.Total != 1 || after.Skills[0].ID != shared.ID ||
		after.Skills[0].Source != "global" || after.Skills[0].Removable {
		t.Fatalf("revealed lower-precedence skill=%#v; original=%#v", after.Skills, shared)
	}
	if _, err := os.Stat(globalShared); err != nil {
		t.Fatalf("global fallback changed: %v", err)
	}

	missingID, encodeErr := encodeCollectionResourceID(skillCollectionIDNamespace, "missing")
	if encodeErr != nil {
		t.Fatal(encodeErr)
	}
	requestBody, marshalErr := json.Marshal(collectionBulkDeleteRequest{IDs: []string{
		deleteMe.ID, shared.ID, "not-an-id", missingID,
	}})
	if marshalErr != nil {
		t.Fatal(marshalErr)
	}
	bulk := requestCollectionAPI(
		t, fixture.mux, http.MethodPost, "/api/skills/bulk-delete",
		string(requestBody), "application/json; charset=utf-8",
	)
	if bulk.Code != http.StatusOK {
		t.Fatalf("bulk delete=%d/%s", bulk.Code, bulk.Body.String())
	}
	var result skillBulkDeleteResponse
	if err := json.Unmarshal(bulk.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if len(result.DeletedIDs) != 1 || result.DeletedIDs[0] != deleteMe.ID ||
		len(result.Failures) != 3 {
		t.Fatalf("bulk result=%#v", result)
	}
	codes := make(map[string]string, len(result.Failures))
	for _, failure := range result.Failures {
		codes[failure.ID] = failure.Code
	}
	if codes[shared.ID] != "read_only_origin" || codes["not-an-id"] != "invalid_id" ||
		codes[missingID] != "not_found" {
		t.Fatalf("bulk failures=%#v", result.Failures)
	}
	if _, err := os.Stat(filepath.Dir(deletePath)); !os.IsNotExist(err) {
		t.Fatalf("bulk-deleted workspace skill remains: %v", err)
	}
	previousRemove := removeWorkspaceSkillDir
	removeWorkspaceSkillDir = func(path string) error {
		if path == filepath.Dir(failDeletePath) {
			return errors.New("private filesystem failure")
		}
		return previousRemove(path)
	}
	failedDeleteBody := `{"ids":["` + failDelete.ID + `"]}`
	failedDelete := requestCollectionAPI(
		t, fixture.mux, http.MethodPost, "/api/skills/bulk-delete",
		failedDeleteBody, "application/json",
	)
	removeWorkspaceSkillDir = previousRemove
	if failedDelete.Code != http.StatusOK ||
		!strings.Contains(failedDelete.Body.String(), `"code":"delete_failed"`) ||
		strings.Contains(failedDelete.Body.String(), "private filesystem") {
		t.Fatalf("failed bulk delete=%d/%s", failedDelete.Code, failedDelete.Body.String())
	}
	if _, err := os.Stat(failDeletePath); err != nil {
		t.Fatalf("failed delete removed skill: %v", err)
	}

	readOnly := requestCollectionAPI(
		t, fixture.mux, http.MethodDelete, "/api/skills/"+shared.ID, "", "",
	)
	if readOnly.Code != http.StatusConflict ||
		decodeCollectionErrorCode(t, readOnly.Body.Bytes()) != "read_only_origin" {
		t.Fatalf("read-only delete=%d/%s", readOnly.Code, readOnly.Body.String())
	}
	duplicateBody := `{"ids":["` + shared.ID + `","` + shared.ID + `"]}`
	duplicate := requestCollectionAPI(
		t, fixture.mux, http.MethodPost, "/api/skills/bulk-delete",
		duplicateBody, "application/json",
	)
	if duplicate.Code != http.StatusOK || !strings.Contains(duplicate.Body.String(), "duplicate_id") {
		t.Fatalf("duplicate bulk=%d/%s", duplicate.Code, duplicate.Body.String())
	}
	for _, body := range []string{`{"ids":[]}`, `{"ids":`} {
		invalid := requestCollectionAPI(
			t, fixture.mux, http.MethodPost, "/api/skills/bulk-delete",
			body, "application/json",
		)
		if invalid.Code != http.StatusBadRequest {
			t.Fatalf("invalid bulk body=%q status=%d body=%s", body, invalid.Code, invalid.Body.String())
		}
	}
	tooManyIDs := make([]string, collectionquery.MaxPageSize+1)
	for index := range tooManyIDs {
		tooManyIDs[index] = toolCollectionResourceID("bulk-" + string(rune(index+1)))
	}
	tooManyBody, marshalErr := json.Marshal(collectionBulkDeleteRequest{IDs: tooManyIDs})
	if marshalErr != nil {
		t.Fatal(marshalErr)
	}
	tooMany := requestCollectionAPI(
		t, fixture.mux, http.MethodPost, "/api/skills/bulk-delete",
		string(tooManyBody), "application/json",
	)
	if tooMany.Code != http.StatusBadRequest ||
		decodeCollectionErrorCode(t, tooMany.Body.Bytes()) != "invalid_bulk_delete" {
		t.Fatalf("oversize bulk=%d/%s", tooMany.Code, tooMany.Body.String())
	}
}

func TestToolsCollectionQueryPagingDetailAndStateByID(t *testing.T) {
	configPath, cleanup := setupOAuthTestEnv(t)
	defer cleanup()
	cfg, loadErr := config.LoadConfig(configPath)
	if loadErr != nil {
		t.Fatal(loadErr)
	}
	cfg.Tools.ReadFile.Enabled = true
	cfg.Tools.WriteFile.Enabled = false
	cfg.Tools.Spawn.Enabled = true
	cfg.Tools.Subagent.Enabled = false
	if saveErr := config.SaveConfig(configPath, cfg); saveErr != nil {
		t.Fatal(saveErr)
	}
	handler := NewHandler(configPath)
	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)

	first := requestCollectionAPI(t, mux, http.MethodGet, "/api/tools?limit=5", "", "")
	if first.Code != http.StatusOK {
		t.Fatalf("tools status=%d body=%s", first.Code, first.Body.String())
	}
	var firstPage toolSupportResponse
	if decodeErr := json.Unmarshal(first.Body.Bytes(), &firstPage); decodeErr != nil {
		t.Fatal(decodeErr)
	}
	if firstPage.Total != len(toolCatalog) || len(firstPage.Tools) != 5 ||
		firstPage.NextCursor == "" ||
		firstPage.CanonicalQuery != "ALL ORDER BY category ASC, name ASC" {
		t.Fatalf("first tools page=%#v", firstPage)
	}
	for index, item := range firstPage.Tools {
		if !validCollectionResourceID(item.ID) || item.Reason != item.ReasonCode {
			t.Fatalf("tool ID/aliases=%#v", item)
		}
		if index > 0 {
			previous := firstPage.Tools[index-1]
			if previous.Category > item.Category ||
				(previous.Category == item.Category && previous.Name > item.Name) {
				t.Fatalf("tools not category/name ordered: %#v", firstPage.Tools)
			}
		}
	}
	second := requestCollectionAPI(
		t, mux, http.MethodGet,
		"/api/tools?limit=5&cursor="+url.QueryEscape(firstPage.NextCursor), "", "",
	)
	if second.Code != http.StatusOK {
		t.Fatalf("second tools page=%d/%s", second.Code, second.Body.String())
	}

	query := `category = filesystem ORDER BY name DESC`
	filteredResponse := requestCollectionAPI(
		t, mux, http.MethodGet, "/api/tools?query="+url.QueryEscape(query), "", "",
	)
	if filteredResponse.Code != http.StatusOK {
		t.Fatalf("filtered tools=%d/%s", filteredResponse.Code, filteredResponse.Body.String())
	}
	var filtered toolSupportResponse
	if decodeErr := json.Unmarshal(filteredResponse.Body.Bytes(), &filtered); decodeErr != nil {
		t.Fatal(decodeErr)
	}
	if filtered.Total < 2 {
		t.Fatalf("filtered tools=%#v", filtered.Tools)
	}
	if !sort.SliceIsSorted(filtered.Tools, func(left, right int) bool {
		return filtered.Tools[left].Name > filtered.Tools[right].Name
	}) {
		t.Fatalf("filtered tool order=%#v", filtered.Tools)
	}
	fields := make(map[collectionquery.Field]collectionquery.FieldType)
	for _, field := range filtered.QuerySchema.Fields {
		fields[field.Name] = field.Type
	}
	for _, name := range []collectionquery.Field{"name", "category", "status", "reason", "config_key"} {
		if _, found := fields[name]; !found {
			t.Fatalf("tool schema omitted %q: %#v", name, fields)
		}
	}
	if fields["status"] != collectionquery.TypeEnum {
		t.Fatalf("tool status schema=%q", fields["status"])
	}
	if fields["category"] != collectionquery.TypeEnum ||
		!containsExactString(schemaFieldSuggestions(filtered.QuerySchema, "category"), "filesystem") ||
		!containsExactString(schemaFieldSuggestions(filtered.QuerySchema, "name"), "read_file") ||
		!containsExactString(schemaFieldSuggestions(filtered.QuerySchema, "config_key"), "read_file") {
		t.Fatalf("tool schema/suggestions=%#v", filtered.QuerySchema.Fields)
	}
	blockedQuery := `status = blocked AND reason = requires_subagent AND config_key = spawn`
	blockedResponse := requestCollectionAPI(
		t, mux, http.MethodGet, "/api/tools?query="+url.QueryEscape(blockedQuery), "", "",
	)
	if blockedResponse.Code != http.StatusOK {
		t.Fatalf("blocked tool query=%d/%s", blockedResponse.Code, blockedResponse.Body.String())
	}
	var blocked toolSupportResponse
	if err := json.Unmarshal(blockedResponse.Body.Bytes(), &blocked); err != nil {
		t.Fatal(err)
	}
	if blocked.Total != 1 || blocked.Tools[0].Reason != "requires_subagent" {
		t.Fatalf("status/reason/config_key query=%#v", blocked.Tools)
	}

	readFileID := toolCollectionResourceID("read_file")
	detail := requestCollectionAPI(t, mux, http.MethodGet, "/api/tools/"+readFileID, "", "")
	if detail.Code != http.StatusOK {
		t.Fatalf("tool detail=%d/%s", detail.Code, detail.Body.String())
	}
	var detailBody struct {
		Tool toolSupportItem `json:"tool"`
	}
	if decodeErr := json.Unmarshal(detail.Body.Bytes(), &detailBody); decodeErr != nil {
		t.Fatal(decodeErr)
	}
	if detailBody.Tool.ID != readFileID || detailBody.Tool.Name != "read_file" ||
		detailBody.Tool.Status != "enabled" {
		t.Fatalf("tool detail=%#v", detailBody.Tool)
	}
	legacy := requestCollectionAPI(t, mux, http.MethodGet, "/api/tools/read_file", "", "")
	if legacy.Code != http.StatusOK {
		t.Fatalf("name-addressed detail=%d/%s", legacy.Code, legacy.Body.String())
	}

	state := requestCollectionAPI(
		t, mux, http.MethodPut, "/api/tools/"+readFileID+"/state",
		`{"enabled":false}`, "application/json",
	)
	if state.Code != http.StatusOK {
		t.Fatalf("opaque state update=%d/%s", state.Code, state.Body.String())
	}
	updated, updateErr := config.LoadConfig(configPath)
	if updateErr != nil || updated.Tools.ReadFile.Enabled {
		t.Fatalf("state update config=%#v err=%v", updated.Tools.ReadFile, updateErr)
	}

	mismatch := requestCollectionAPI(
		t, mux, http.MethodGet,
		"/api/tools?query="+url.QueryEscape("ORDER BY name DESC")+"&cursor="+
			url.QueryEscape(firstPage.NextCursor), "", "",
	)
	if mismatch.Code != http.StatusBadRequest ||
		decodeCollectionErrorCode(t, mismatch.Body.Bytes()) != "invalid_cursor" {
		t.Fatalf("tool cursor mismatch=%d/%s", mismatch.Code, mismatch.Body.String())
	}
	unicodeQuery := "name = café AND unknown = value"
	unicodeError := requestCollectionAPI(
		t, mux, http.MethodGet, "/api/tools?query="+url.QueryEscape(unicodeQuery), "", "",
	)
	var structured struct {
		Code     string `json:"code"`
		Position int    `json:"position"`
	}
	if err := json.Unmarshal(unicodeError.Body.Bytes(), &structured); err != nil {
		t.Fatal(err)
	}
	if unicodeError.Code != http.StatusBadRequest || structured.Code != "invalid_query" ||
		structured.Position != strings.Index(unicodeQuery, "unknown") {
		t.Fatalf("tool UTF-8 query error=%#v body=%s", structured, unicodeError.Body.String())
	}
	for _, target := range []string{
		"/api/tools?limit=0",
		"/api/tools?other=value",
		"/api/tools/" + toolCollectionResourceID("missing"),
		"/api/tools/" + readFileID + "?query=ALL",
	} {
		response := requestCollectionAPI(t, mux, http.MethodGet, target, "", "")
		if response.Code != http.StatusBadRequest && response.Code != http.StatusNotFound {
			t.Fatalf("invalid tool request %q=%d/%s", target, response.Code, response.Body.String())
		}
	}
}

func TestSkillsAndToolsMutationsRejectCrossOrigin(t *testing.T) {
	fixture := newSkillsCollectionFixture(t)
	tests := []struct {
		method      string
		target      string
		contentType string
		body        string
	}{
		{http.MethodPost, "/api/skills/install", "application/json", `{}`},
		{http.MethodPost, "/api/skills/import", "multipart/form-data; boundary=x", "--x--\r\n"},
		{http.MethodPost, "/api/skills/bulk-delete", "application/json", `{"ids":["x"]}`},
		{http.MethodDelete, "/api/skills/missing", "", ""},
		{http.MethodPut, "/api/tools/read_file/state", "application/json", `{"enabled":false}`},
		{http.MethodPut, "/api/tools/web-search-config", "application/json", `{}`},
		{http.MethodPut, "/api/tools/thread-policy", "application/json", `{}`},
		{http.MethodPut, "/api/tools/adaptation", "application/json", `{}`},
		{http.MethodPost, "/api/tools/adaptation/probe", "application/json", `{}`},
	}
	for _, test := range tests {
		request := httptest.NewRequest(test.method, test.target, bytes.NewBufferString(test.body))
		if test.contentType != "" {
			request.Header.Set("Content-Type", test.contentType)
		}
		request.Header.Set("Origin", "https://attacker.invalid")
		request.Header.Set("Sec-Fetch-Site", "cross-site")
		response := httptest.NewRecorder()
		fixture.mux.ServeHTTP(response, request)
		if response.Code != http.StatusForbidden ||
			decodeCollectionErrorCode(t, response.Body.Bytes()) != "cross_origin_mutation" {
			t.Fatalf("cross-origin %s %s=%d/%s", test.method, test.target, response.Code, response.Body.String())
		}
	}
}

func TestToolStateMutationStructuredItemErrors(t *testing.T) {
	configPath, cleanup := setupOAuthTestEnv(t)
	defer cleanup()
	handler := NewHandler(configPath)
	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)

	staleID := toolCollectionResourceID("removed_tool")
	tests := []struct {
		name       string
		target     string
		wantStatus int
		wantCode   string
	}{
		{
			name: "unsupported query parameter", target: "/api/tools/cron/state?query=ALL",
			wantStatus: http.StatusBadRequest, wantCode: "invalid_collection_request",
		},
		{
			name: "unknown compatibility name", target: "/api/tools/not_a_tool/state",
			wantStatus: http.StatusNotFound, wantCode: "tool_not_found",
		},
		{
			name: "stale issued ID", target: "/api/tools/" + staleID + "/state",
			wantStatus: http.StatusNotFound, wantCode: "tool_not_found",
		},
		{
			name:       "malformed opaque ID",
			target:     "/api/tools/" + strings.Repeat("!", collectionResourceIDEncodedBytes) + "/state",
			wantStatus: http.StatusBadRequest, wantCode: "invalid_tool_id",
		},
		{
			name:       "oversized ID",
			target:     "/api/tools/" + strings.Repeat("x", toolCollectionRouteIDMaxBytes+1) + "/state",
			wantStatus: http.StatusBadRequest, wantCode: "invalid_tool_id",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			response := requestCollectionAPI(
				t, mux, http.MethodPut, test.target,
				`{"enabled":true}`, "application/json",
			)
			assertToolStateCollectionError(t, response, test.wantStatus, test.wantCode)
		})
	}

	malformedConfig := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(malformedConfig, []byte("{private-load-failure"), 0o600); err != nil {
		t.Fatal(err)
	}
	loadHandler := NewHandler(malformedConfig)
	loadMux := http.NewServeMux()
	loadHandler.RegisterRoutes(loadMux)
	loadFailure := requestCollectionAPI(
		t, loadMux, http.MethodPut, "/api/tools/cron/state",
		`{"enabled":true}`, "application/json",
	)
	assertToolStateCollectionError(
		t, loadFailure, http.StatusInternalServerError, "config_load_failed",
	)
	if strings.Contains(loadFailure.Body.String(), "private-load-failure") {
		t.Fatalf("config load detail leaked: %s", loadFailure.Body.String())
	}

	saveHandler := NewHandler(configPath)
	saveHandler.saveToolStateConfig = func(string, *config.Config, string) (string, error) {
		return "", errors.New("private-save-failure")
	}
	saveMux := http.NewServeMux()
	saveHandler.RegisterRoutes(saveMux)
	saveFailure := requestCollectionAPI(
		t, saveMux, http.MethodPut, "/api/tools/cron/state",
		`{"enabled":true}`, "application/json",
	)
	assertToolStateCollectionError(
		t, saveFailure, http.StatusInternalServerError, "config_save_failed",
	)
	if strings.Contains(saveFailure.Body.String(), "private-save-failure") {
		t.Fatalf("config save detail leaked: %s", saveFailure.Body.String())
	}
}

func assertToolStateCollectionError(
	t *testing.T,
	response *httptest.ResponseRecorder,
	wantStatus int,
	wantCode string,
) {
	t.Helper()
	if response.Code != wantStatus {
		t.Fatalf("status=%d want=%d body=%s", response.Code, wantStatus, response.Body.String())
	}
	var body map[string]json.RawMessage
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode structured tool-state error: %v; body=%s", err, response.Body.String())
	}
	if len(body) != 2 || body["code"] == nil || body["message"] == nil {
		t.Fatalf("tool-state error schema=%v", body)
	}
	var code, message string
	if err := json.Unmarshal(body["code"], &code); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(body["message"], &message); err != nil {
		t.Fatal(err)
	}
	if code != wantCode || message == "" || len(message) > 512 {
		t.Fatalf("tool-state error code=%q message=%q", code, message)
	}
	if response.Header().Get("Content-Type") != "application/json; charset=utf-8" ||
		response.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("tool-state error headers=%v", response.Header())
	}
}

func TestSkillToolCollectionBoundaryFailuresStayStructured(t *testing.T) {
	projected, projectionErr := finalizeSkillSupportItem(skillSupportItem{
		Name: " Mixed-Case ", Source: "WORKSPACE", Registry: "registry-alias",
		Version: "version-alias",
	})
	if projectionErr != nil {
		t.Fatal(projectionErr)
	}
	if projected.Name != "Mixed-Case" || projected.Source != "workspace" ||
		projected.Origin != "builtin" || projected.RegistryName != "registry-alias" ||
		projected.InstalledVersion != "version-alias" || !projected.Removable {
		t.Fatalf("finalized compatibility aliases=%#v", projected)
	}
	if _, err := finalizeSkillSupportItem(skillSupportItem{Source: "workspace"}); err == nil {
		t.Fatal("empty skill identity was accepted")
	}
	query, parseErr := collectionquery.Parse("", skillCollectionSchema)
	if parseErr != nil {
		t.Fatal(parseErr)
	}
	request := collectionListRequest{Query: query, Now: testCollectionTime()}
	invalidSkill := projected
	invalidSkill.ID = "invalid"
	if _, err := pageSkillSupportItems([]skillSupportItem{invalidSkill}, request); err == nil {
		t.Fatal("skill pager accepted invalid stable ID")
	}
	if _, found := resolveSkillSupportItem([]skillSupportItem{projected}, "missing"); found {
		t.Fatal("missing skill resolved")
	}
	toolQuery, parseErr := collectionquery.Parse("", toolCollectionSchema)
	if parseErr != nil {
		t.Fatal(parseErr)
	}
	invalidTool := toolSupportItem{
		ID: "invalid", Name: "invalid", Category: "filesystem", Status: "enabled",
	}
	if _, err := pageToolSupportItems(
		[]toolSupportItem{invalidTool},
		collectionListRequest{Query: toolQuery, Now: testCollectionTime()},
	); err == nil {
		t.Fatal("tool pager accepted invalid stable ID")
	}
	if toolCollectionResourceID("") != "" {
		t.Fatal("empty tool identity received an ID")
	}
	if _, found := resolveToolSupportItem([]toolSupportItem{invalidTool}, "missing"); found {
		t.Fatal("missing tool resolved")
	}

	cfg := config.DefaultConfig()
	cfg.Agents.Defaults.Workspace = t.TempDir()
	if err := removeWorkspaceSkill(nil, projected); err == nil {
		t.Fatal("nil config deletion target accepted")
	}
	nonRemovable := projected
	nonRemovable.Removable = false
	if err := removeWorkspaceSkill(cfg, nonRemovable); err == nil {
		t.Fatal("read-only deletion target accepted")
	}
	wrongFile := projected
	wrongFile.Path = filepath.Join(cfg.WorkspacePath(), "skills", "one", "README.md")
	if err := removeWorkspaceSkill(cfg, wrongFile); err == nil {
		t.Fatal("non-SKILL.md deletion target accepted")
	}
	rootTarget := projected
	rootTarget.Path = filepath.Join(cfg.WorkspacePath(), "skills", "SKILL.md")
	if err := removeWorkspaceSkill(cfg, rootTarget); err == nil {
		t.Fatal("skills root deletion target accepted")
	}
	escapeTarget := projected
	escapeTarget.Path = filepath.Join(cfg.WorkspacePath(), "outside", "SKILL.md")
	if err := removeWorkspaceSkill(cfg, escapeTarget); err == nil {
		t.Fatal("escaping deletion target accepted")
	}

	badConfigPath := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(badConfigPath, []byte("{private-config-syntax"), 0o600); err != nil {
		t.Fatal(err)
	}
	handler := NewHandler(badConfigPath)
	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)
	failureRequests := []struct {
		method      string
		target      string
		body        string
		contentType string
	}{
		{http.MethodGet, "/api/skills", "", ""},
		{http.MethodGet, "/api/skills/missing", "", ""},
		{http.MethodDelete, "/api/skills/missing", "", ""},
		{http.MethodPost, "/api/skills/bulk-delete", `{"ids":["missing"]}`, "application/json"},
		{http.MethodGet, "/api/tools", "", ""},
		{http.MethodGet, "/api/tools/missing", "", ""},
	}
	for _, failureRequest := range failureRequests {
		response := requestCollectionAPI(
			t, mux, failureRequest.method, failureRequest.target,
			failureRequest.body, failureRequest.contentType,
		)
		if response.Code != http.StatusInternalServerError ||
			decodeCollectionErrorCode(t, response.Body.Bytes()) != "config_load_failed" ||
			strings.Contains(response.Body.String(), "private-config-syntax") {
			t.Fatalf(
				"config failure %s %s=%d/%s",
				failureRequest.method, failureRequest.target, response.Code, response.Body.String(),
			)
		}
	}
}

func testCollectionTime() time.Time {
	return time.Date(2026, time.August, 27, 12, 0, 0, 0, time.UTC)
}
