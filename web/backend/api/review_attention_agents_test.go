package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/routing"
)

func reviewAttentionAgentsGET(
	t *testing.T,
	harness *reviewAttentionAPITestHarness,
	path string,
	revision string,
) *httptest.ResponseRecorder {
	t.Helper()
	return harness.request(
		t,
		http.MethodGet,
		path,
		"",
		http.Header{"If-Match": {strconv.Quote(revision)}},
	)
}

func decodeReviewAttentionAgentsPage(
	t *testing.T,
	recorder *httptest.ResponseRecorder,
) reviewAttentionAgentsResponse {
	t.Helper()
	if recorder.Code != http.StatusOK {
		t.Fatalf("GET status = %d, body=%s", recorder.Code, recorder.Body.String())
	}
	var response reviewAttentionAgentsResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("json.Unmarshal() error = %v; body=%s", err, recorder.Body.String())
	}
	return response
}

func reviewAttentionAgentFixtures(count int) []config.AgentConfig {
	agents := make([]config.AgentConfig, 0, count)
	for index := count - 1; index >= 0; index-- {
		agent := config.AgentConfig{
			ID:      fmt.Sprintf("agent-%03d", index),
			Name:    fmt.Sprintf("Agent %03d", index),
			Default: index == count-1,
		}
		if index == 0 {
			agent.Name = "  Agent 000  "
			agent.Workspace = "/private/" +
				strings.Repeat("workspace-sensitive-canary-", 256)
			agent.Model = &config.AgentModelConfig{Primary: "private-model-canary"}
			agent.Skills = []string{
				strings.Repeat("skill-sensitive-canary-", 20),
			}
			agent.Subagents = &config.SubagentsConfig{
				AllowAgents: []string{"*"},
			}
		}
		agents = append(agents, agent)
	}
	return agents
}

func configureReviewAttentionAgentFixtures(
	cfg *config.Config,
	count int,
	withSecret bool,
) {
	if withSecret {
		cfg.ModelList = []*config.ModelConfig{{
			ModelName: "private-account-canary",
			Provider:  "openai",
			Model:     "provider/private-model-canary",
			APIKeys:   config.SimpleSecureStrings("private-api-key-canary"),
			Enabled:   true,
		}}
	}
	cfg.ModelAliases = []config.ModelAliasConfig{{
		Name:  "private-model-canary",
		Model: "provider/private-model-canary",
	}}
	cfg.Agents.List = reviewAttentionAgentFixtures(count)
}

func assertReviewAttentionResponseHeaders(
	t *testing.T,
	recorder *httptest.ResponseRecorder,
) {
	t.Helper()
	if got := recorder.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("Cache-Control = %q, want no-store", got)
	}
	if got := recorder.Header().Get("X-Content-Type-Options"); got != "nosniff" {
		t.Fatalf("X-Content-Type-Options = %q, want nosniff", got)
	}
	if got := recorder.Header().Get("Content-Type"); got != "application/json" {
		t.Fatalf("Content-Type = %q, want application/json", got)
	}
}

func assertJSONKeys(
	t *testing.T,
	document map[string]json.RawMessage,
	want ...string,
) {
	t.Helper()
	got := make([]string, 0, len(document))
	for key := range document {
		got = append(got, key)
	}
	sort.Strings(got)
	sort.Strings(want)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("JSON keys = %v, want %v", got, want)
	}
}

func TestReviewAttentionAgentsPaginateStableIdentityOnlyProjection(
	t *testing.T,
) {
	resetGatewayTestState(t)
	harness := newReviewAttentionAPITestHarness(t, func(cfg *config.Config) {
		configureReviewAttentionAgentFixtures(cfg, 300, true)
	})
	directory := filepath.Dir(harness.configPath)
	before := reviewAttentionDirectorySnapshot(t, directory)
	revision, err := config.ConfigRevision(harness.configPath)
	if err != nil {
		t.Fatalf("ConfigRevision() error = %v", err)
	}

	firstRecorder := reviewAttentionAgentsGET(
		t,
		harness,
		reviewAttentionAgentsPath,
		revision,
	)
	assertReviewAttentionResponseHeaders(t, firstRecorder)
	first := decodeReviewAttentionAgentsPage(t, firstRecorder)
	if len(first.Agents) != 256 || first.NextCursor != "256" {
		t.Fatalf(
			"first page = %d agents, next_cursor=%q; want 256/256",
			len(first.Agents),
			first.NextCursor,
		)
	}
	if first.ConfigRevision != revision || first.DefaultAgentID != "agent-299" {
		t.Fatalf("first-page generation/default = %#v", first)
	}
	if first.Agents[0].ID != "agent-000" ||
		first.Agents[len(first.Agents)-1].ID != "agent-255" ||
		first.Agents[0].Name != "Agent 000" {
		t.Fatalf("first-page boundaries = %#v / %#v", first.Agents[0], first.Agents[255])
	}

	secondRecorder := reviewAttentionAgentsGET(
		t,
		harness,
		reviewAttentionAgentsPath+"?cursor="+first.NextCursor,
		revision,
	)
	assertReviewAttentionResponseHeaders(t, secondRecorder)
	second := decodeReviewAttentionAgentsPage(t, secondRecorder)
	if len(second.Agents) != 44 || second.NextCursor != "" {
		t.Fatalf(
			"second page = %d agents, next_cursor=%q; want 44/empty",
			len(second.Agents),
			second.NextCursor,
		)
	}
	if second.ConfigRevision != revision || second.DefaultAgentID != "agent-299" ||
		second.Agents[0].ID != "agent-256" || second.Agents[43].ID != "agent-299" {
		t.Fatalf("second-page generation/boundaries = %#v", second)
	}
	defaultOnSecondPage := false
	allIDs := make([]string, 0, 300)
	seen := make(map[string]struct{}, 300)
	for _, page := range [][]reviewAttentionAgentIdentity{first.Agents, second.Agents} {
		for _, agent := range page {
			if _, duplicate := seen[agent.ID]; duplicate {
				t.Fatalf("duplicate paginated identity %q", agent.ID)
			}
			seen[agent.ID] = struct{}{}
			allIDs = append(allIDs, agent.ID)
			if agent.ID == second.DefaultAgentID {
				defaultOnSecondPage = true
			}
		}
	}
	if !sort.StringsAreSorted(allIDs) || len(allIDs) != 300 || !defaultOnSecondPage {
		t.Fatalf(
			"pagination order/count/default = sorted:%v count:%d default-on-page-two:%v",
			sort.StringsAreSorted(allIDs),
			len(allIDs),
			defaultOnSecondPage,
		)
	}
	for index, id := range allIDs {
		want := fmt.Sprintf("agent-%03d", index)
		if id != want {
			t.Fatalf("identity %d = %q, want %q", index, id, want)
		}
	}

	for pageIndex, recorder := range []*httptest.ResponseRecorder{
		firstRecorder,
		secondRecorder,
	} {
		body := recorder.Body.String()
		for _, forbidden := range []string{
			`"workspace"`,
			`"account_ref"`,
			`"model"`,
			`"skills"`,
			`"subagents"`,
			`"effects"`,
			"workspace-sensitive-canary",
			"skill-sensitive-canary",
			"private-model-canary",
			"private-account-canary",
			"private-api-key-canary",
		} {
			if strings.Contains(body, forbidden) {
				t.Fatalf("page %d leaked %q: %s", pageIndex+1, forbidden, body)
			}
		}
		var raw map[string]json.RawMessage
		if err = json.Unmarshal(recorder.Body.Bytes(), &raw); err != nil {
			t.Fatalf("page %d raw decode error = %v", pageIndex+1, err)
		}
		if pageIndex == 0 {
			assertJSONKeys(
				t,
				raw,
				"agents",
				"config_revision",
				"default_agent_id",
				"next_cursor",
			)
		} else {
			assertJSONKeys(t, raw, "agents", "config_revision", "default_agent_id")
		}
		var rawAgents []map[string]json.RawMessage
		if err = json.Unmarshal(raw["agents"], &rawAgents); err != nil {
			t.Fatalf("page %d agents raw decode error = %v", pageIndex+1, err)
		}
		for _, rawAgent := range rawAgents {
			assertJSONKeys(t, rawAgent, "id", "name")
		}
	}

	after := reviewAttentionDirectorySnapshot(t, directory)
	if !reflect.DeepEqual(after, before) {
		t.Fatalf("successful paginated reads mutated config: before=%v after=%v", before, after)
	}
}

func TestReviewAttentionAgentsImplicitMainAndMissingConfigAreReadOnly(
	t *testing.T,
) {
	for _, test := range []struct {
		name    string
		missing bool
	}{
		{name: "current empty agent list"},
		{name: "missing public and security config", missing: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			resetGatewayTestState(t)
			harness := newReviewAttentionAPITestHarness(t, nil)
			if test.missing {
				for _, path := range []string{
					harness.configPath,
					filepath.Join(filepath.Dir(harness.configPath), config.SecurityConfigFile),
				} {
					if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
						t.Fatalf("Remove(%q) error = %v", path, err)
					}
				}
			}
			directory := filepath.Dir(harness.configPath)
			before := reviewAttentionDirectorySnapshot(t, directory)
			revision, err := config.ConfigRevision(harness.configPath)
			if err != nil {
				t.Fatalf("ConfigRevision() error = %v", err)
			}
			if test.missing && revision != "missing" {
				t.Fatalf("missing revision = %q, want missing", revision)
			}

			recorder := reviewAttentionAgentsGET(
				t,
				harness,
				reviewAttentionAgentsPath,
				revision,
			)
			assertReviewAttentionResponseHeaders(t, recorder)
			response := decodeReviewAttentionAgentsPage(t, recorder)
			if len(response.Agents) != 1 ||
				response.Agents[0] != (reviewAttentionAgentIdentity{ID: routing.DefaultAgentID, Name: "Main"}) ||
				response.DefaultAgentID != routing.DefaultAgentID ||
				response.ConfigRevision != revision || response.NextCursor != "" {
				t.Fatalf("implicit main response = %#v", response)
			}
			after := reviewAttentionDirectorySnapshot(t, directory)
			if !reflect.DeepEqual(after, before) {
				t.Fatalf("implicit/missing read mutated config: before=%v after=%v", before, after)
			}
		})
	}
}

func TestReviewAttentionAgentsFencePublicAndSecurityGenerationChanges(
	t *testing.T,
) {
	for _, test := range []struct {
		name   string
		mutate func(*testing.T, *reviewAttentionAPITestHarness)
	}{
		{
			name: "public config mutation",
			mutate: func(t *testing.T, harness *reviewAttentionAPITestHarness) {
				t.Helper()
				raw, err := os.ReadFile(harness.configPath)
				if err != nil {
					t.Fatalf("ReadFile(public) error = %v", err)
				}
				raw = append(raw, []byte("\n \n")...)
				if err = os.WriteFile(harness.configPath, raw, 0o600); err != nil {
					t.Fatalf("WriteFile(public winner) error = %v", err)
				}
			},
		},
		{
			name: "security config mutation",
			mutate: func(t *testing.T, harness *reviewAttentionAPITestHarness) {
				t.Helper()
				path := filepath.Join(filepath.Dir(harness.configPath), config.SecurityConfigFile)
				raw, err := os.ReadFile(path)
				if err != nil {
					t.Fatalf("ReadFile(security) error = %v", err)
				}
				raw = append(raw, []byte("\n# concurrent security winner\n")...)
				if err = os.WriteFile(path, raw, 0o600); err != nil {
					t.Fatalf("WriteFile(security winner) error = %v", err)
				}
			},
		},
		{
			name: "malformed public config winner",
			mutate: func(t *testing.T, harness *reviewAttentionAPITestHarness) {
				t.Helper()
				if err := os.WriteFile(harness.configPath, []byte("{"), 0o600); err != nil {
					t.Fatalf("WriteFile(malformed public winner) error = %v", err)
				}
			},
		},
		{
			name: "malformed security config winner",
			mutate: func(t *testing.T, harness *reviewAttentionAPITestHarness) {
				t.Helper()
				path := filepath.Join(filepath.Dir(harness.configPath), config.SecurityConfigFile)
				if err := os.WriteFile(path, []byte(":\n"), 0o600); err != nil {
					t.Fatalf("WriteFile(malformed security winner) error = %v", err)
				}
			},
		},
		{
			name: "orphaned security config winner",
			mutate: func(t *testing.T, harness *reviewAttentionAPITestHarness) {
				t.Helper()
				if err := os.Remove(harness.configPath); err != nil {
					t.Fatalf("Remove(public winner) error = %v", err)
				}
			},
		},
		{
			name: "legacy public config winner",
			mutate: func(t *testing.T, harness *reviewAttentionAPITestHarness) {
				t.Helper()
				raw, err := os.ReadFile(harness.configPath)
				if err != nil {
					t.Fatalf("ReadFile(public winner) error = %v", err)
				}
				var document map[string]json.RawMessage
				if err = json.Unmarshal(raw, &document); err != nil {
					t.Fatalf("json.Unmarshal(public winner) error = %v", err)
				}
				document["version"] = json.RawMessage(strconv.Itoa(config.CurrentVersion - 1))
				raw, err = json.Marshal(document)
				if err != nil {
					t.Fatalf("json.Marshal(legacy winner) error = %v", err)
				}
				if err = os.WriteFile(harness.configPath, raw, 0o600); err != nil {
					t.Fatalf("WriteFile(legacy winner) error = %v", err)
				}
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			resetGatewayTestState(t)
			harness := newReviewAttentionAPITestHarness(t, func(cfg *config.Config) {
				configureReviewAttentionAgentFixtures(cfg, 257, true)
			})
			staleRevision, err := config.ConfigRevision(harness.configPath)
			if err != nil {
				t.Fatalf("ConfigRevision() error = %v", err)
			}
			first := decodeReviewAttentionAgentsPage(
				t,
				reviewAttentionAgentsGET(
					t,
					harness,
					reviewAttentionAgentsPath,
					staleRevision,
				),
			)
			if first.NextCursor != "256" {
				t.Fatalf("first next_cursor = %q, want 256", first.NextCursor)
			}

			test.mutate(t, harness)
			directory := filepath.Dir(harness.configPath)
			winner := reviewAttentionDirectorySnapshot(t, directory)
			currentRevision, err := config.ConfigRevision(harness.configPath)
			if err != nil {
				t.Fatalf("ConfigRevision(winner) error = %v", err)
			}
			if currentRevision == staleRevision {
				t.Fatal("concurrent mutation did not change the combined revision")
			}

			recorder := reviewAttentionAgentsGET(
				t,
				harness,
				reviewAttentionAgentsPath+"?cursor="+first.NextCursor,
				staleRevision,
			)
			if recorder.Code != http.StatusConflict ||
				decodeReviewAttentionError(t, recorder) != reviewAttentionConfigRevisionMismatch {
				t.Fatalf("stale page = %d body=%s", recorder.Code, recorder.Body.String())
			}
			assertReviewAttentionResponseHeaders(t, recorder)
			after := reviewAttentionDirectorySnapshot(t, directory)
			if !reflect.DeepEqual(after, winner) {
				t.Fatalf("stale page changed winner: before=%v after=%v", winner, after)
			}
		})
	}
}

func TestReviewAttentionAgentsStrictRequestContract(t *testing.T) {
	resetGatewayTestState(t)
	harness := newReviewAttentionAPITestHarness(t, nil)
	directory := filepath.Dir(harness.configPath)
	before := reviewAttentionDirectorySnapshot(t, directory)
	revision, err := config.ConfigRevision(harness.configPath)
	if err != nil {
		t.Fatalf("ConfigRevision() error = %v", err)
	}
	validIfMatch := strconv.Quote(revision)

	tests := []struct {
		name      string
		method    string
		path      string
		headers   http.Header
		want      int
		wantError string
		wantAllow string
	}{
		{
			name: "missing if-match", method: http.MethodGet,
			path: reviewAttentionAgentsPath, want: http.StatusBadRequest,
			wantError: reviewAttentionAgentsInvalidRequest,
		},
		{
			name: "empty quoted if-match", method: http.MethodGet,
			path:    reviewAttentionAgentsPath,
			headers: http.Header{"If-Match": {`""`}},
			want:    http.StatusBadRequest, wantError: reviewAttentionAgentsInvalidRequest,
		},
		{
			name: "unquoted if-match", method: http.MethodGet,
			path:    reviewAttentionAgentsPath,
			headers: http.Header{"If-Match": {revision}},
			want:    http.StatusBadRequest, wantError: reviewAttentionAgentsInvalidRequest,
		},
		{
			name: "weak if-match", method: http.MethodGet,
			path:    reviewAttentionAgentsPath,
			headers: http.Header{"If-Match": {"W/" + validIfMatch}},
			want:    http.StatusBadRequest, wantError: reviewAttentionAgentsInvalidRequest,
		},
		{
			name: "wildcard if-match", method: http.MethodGet,
			path:    reviewAttentionAgentsPath,
			headers: http.Header{"If-Match": {"*"}},
			want:    http.StatusBadRequest, wantError: reviewAttentionAgentsInvalidRequest,
		},
		{
			name: "comma-list if-match", method: http.MethodGet,
			path:    reviewAttentionAgentsPath,
			headers: http.Header{"If-Match": {validIfMatch + ", " + validIfMatch}},
			want:    http.StatusBadRequest, wantError: reviewAttentionAgentsInvalidRequest,
		},
		{
			name: "repeated if-match", method: http.MethodGet,
			path:    reviewAttentionAgentsPath,
			headers: http.Header{"If-Match": {validIfMatch, validIfMatch}},
			want:    http.StatusBadRequest, wantError: reviewAttentionAgentsInvalidRequest,
		},
		{
			name: "case-colliding if-match", method: http.MethodGet,
			path: reviewAttentionAgentsPath,
			headers: http.Header{
				"If-Match": {validIfMatch},
				"if-match": {validIfMatch},
			},
			want: http.StatusBadRequest, wantError: reviewAttentionAgentsInvalidRequest,
		},
		{
			name: "oversized if-match", method: http.MethodGet,
			path:    reviewAttentionAgentsPath,
			headers: http.Header{"If-Match": {`"` + strings.Repeat("x", 4096) + `"`}},
			want:    http.StatusBadRequest, wantError: reviewAttentionAgentsInvalidRequest,
		},
		{
			name: "stale strong if-match", method: http.MethodGet,
			path:    reviewAttentionAgentsPath,
			headers: http.Header{"If-Match": {`"sha256:stale-generation"`}},
			want:    http.StatusConflict, wantError: reviewAttentionConfigRevisionMismatch,
		},
		{
			name: "bare query delimiter", method: http.MethodGet,
			path:    reviewAttentionAgentsPath + "?",
			headers: http.Header{"If-Match": {validIfMatch}},
			want:    http.StatusBadRequest, wantError: reviewAttentionAgentsInvalidRequest,
		},
		{
			name: "unknown query", method: http.MethodGet,
			path:    reviewAttentionAgentsPath + "?limit=1",
			headers: http.Header{"If-Match": {validIfMatch}},
			want:    http.StatusBadRequest, wantError: reviewAttentionAgentsInvalidRequest,
		},
		{
			name: "repeated cursor", method: http.MethodGet,
			path:    reviewAttentionAgentsPath + "?cursor=0&cursor=0",
			headers: http.Header{"If-Match": {validIfMatch}},
			want:    http.StatusBadRequest, wantError: reviewAttentionAgentsInvalidRequest,
		},
		{
			name: "blank cursor", method: http.MethodGet,
			path:    reviewAttentionAgentsPath + "?cursor=",
			headers: http.Header{"If-Match": {validIfMatch}},
			want:    http.StatusBadRequest, wantError: reviewAttentionAgentsInvalidRequest,
		},
		{
			name: "unaligned cursor", method: http.MethodGet,
			path:    reviewAttentionAgentsPath + "?cursor=1",
			headers: http.Header{"If-Match": {validIfMatch}},
			want:    http.StatusBadRequest, wantError: reviewAttentionAgentsInvalidRequest,
		},
		{
			name: "aligned cursor beyond catalog", method: http.MethodGet,
			path:    reviewAttentionAgentsPath + "?cursor=256",
			headers: http.Header{"If-Match": {validIfMatch}},
			want:    http.StatusBadRequest, wantError: reviewAttentionAgentsInvalidRequest,
		},
		{
			name: "leading zero cursor", method: http.MethodGet,
			path:    reviewAttentionAgentsPath + "?cursor=0256",
			headers: http.Header{"If-Match": {validIfMatch}},
			want:    http.StatusBadRequest, wantError: reviewAttentionAgentsInvalidRequest,
		},
		{
			name: "signed cursor", method: http.MethodGet,
			path:    reviewAttentionAgentsPath + "?cursor=%2B1",
			headers: http.Header{"If-Match": {validIfMatch}},
			want:    http.StatusBadRequest, wantError: reviewAttentionAgentsInvalidRequest,
		},
		{
			name: "negative cursor", method: http.MethodGet,
			path:    reviewAttentionAgentsPath + "?cursor=-1",
			headers: http.Header{"If-Match": {validIfMatch}},
			want:    http.StatusBadRequest, wantError: reviewAttentionAgentsInvalidRequest,
		},
		{
			name: "oversized cursor", method: http.MethodGet,
			path:    reviewAttentionAgentsPath + "?cursor=" + strings.Repeat("9", 64),
			headers: http.Header{"If-Match": {validIfMatch}},
			want:    http.StatusBadRequest, wantError: reviewAttentionAgentsInvalidRequest,
		},
		{
			name: "escaped path", method: http.MethodGet,
			path:    "/api/reviews/attention%2Dagents",
			headers: http.Header{"If-Match": {validIfMatch}},
			want:    http.StatusBadRequest, wantError: reviewAttentionAgentsInvalidRequest,
		},
		{
			name: "trailing slash", method: http.MethodGet,
			path:    reviewAttentionAgentsPath + "/",
			headers: http.Header{"If-Match": {validIfMatch}},
			want:    http.StatusBadRequest,
		},
		{
			name: "head unsupported", method: http.MethodHead,
			path:    reviewAttentionAgentsPath,
			headers: http.Header{"If-Match": {validIfMatch}},
			want:    http.StatusMethodNotAllowed, wantError: "method not allowed", wantAllow: http.MethodGet,
		},
		{
			name: "post unsupported", method: http.MethodPost,
			path:    reviewAttentionAgentsPath,
			headers: http.Header{"If-Match": {validIfMatch}},
			want:    http.StatusMethodNotAllowed, wantError: "method not allowed", wantAllow: http.MethodGet,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			recorder := harness.request(
				t,
				test.method,
				test.path,
				"",
				test.headers,
			)
			if recorder.Code != test.want {
				t.Fatalf(
					"status = %d, want %d; body=%s",
					recorder.Code,
					test.want,
					recorder.Body.String(),
				)
			}
			if test.wantError != "" &&
				decodeReviewAttentionError(t, recorder) != test.wantError {
				t.Fatalf("body = %s, want error %q", recorder.Body.String(), test.wantError)
			}
			if got := recorder.Header().Get("Allow"); got != test.wantAllow {
				t.Fatalf("Allow = %q, want %q", got, test.wantAllow)
			}
			assertReviewAttentionResponseHeaders(t, recorder)
		})
	}
	after := reviewAttentionDirectorySnapshot(t, directory)
	if !reflect.DeepEqual(after, before) {
		t.Fatalf("invalid requests mutated config: before=%v after=%v", before, after)
	}
}

func TestReviewAttentionAgentsInvalidConfigurationFailsClosed(t *testing.T) {
	tests := []struct {
		name      string
		configure func(*config.Config)
	}{
		{
			name: "duplicate IDs",
			configure: func(cfg *config.Config) {
				cfg.Agents.List = []config.AgentConfig{{ID: "main"}, {ID: "main"}}
			},
		},
		{
			name: "noncanonical ID",
			configure: func(cfg *config.Config) {
				cfg.Agents.List = []config.AgentConfig{{ID: "Not-Canonical"}}
			},
		},
		{
			name: "multiple defaults",
			configure: func(cfg *config.Config) {
				cfg.Agents.List = []config.AgentConfig{
					{ID: "main", Default: true},
					{ID: "worker", Default: true},
				}
			},
		},
		{
			name: "invalid name",
			configure: func(cfg *config.Config) {
				cfg.Agents.List = []config.AgentConfig{{ID: "main", Name: "bad\x00name"}}
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			resetGatewayTestState(t)
			harness := newReviewAttentionAPITestHarness(t, test.configure)
			directory := filepath.Dir(harness.configPath)
			before := reviewAttentionDirectorySnapshot(t, directory)
			revision, err := config.ConfigRevision(harness.configPath)
			if err != nil {
				t.Fatalf("ConfigRevision() error = %v", err)
			}
			recorder := reviewAttentionAgentsGET(
				t,
				harness,
				reviewAttentionAgentsPath,
				revision,
			)
			if recorder.Code != http.StatusInternalServerError ||
				decodeReviewAttentionError(t, recorder) != reviewAttentionAgentsUnavailable {
				t.Fatalf("invalid config response = %d body=%s", recorder.Code, recorder.Body.String())
			}
			assertReviewAttentionResponseHeaders(t, recorder)
			after := reviewAttentionDirectorySnapshot(t, directory)
			if !reflect.DeepEqual(after, before) {
				t.Fatalf("invalid config read mutated files: before=%v after=%v", before, after)
			}
		})
	}
}

func TestReviewAttentionAgentsRejectLegacyWithoutMigration(t *testing.T) {
	resetGatewayTestState(t)
	harness := newReviewAttentionAPITestHarness(t, nil)
	raw, err := os.ReadFile(harness.configPath)
	if err != nil {
		t.Fatalf("ReadFile(config) error = %v", err)
	}
	var document map[string]json.RawMessage
	if err = json.Unmarshal(raw, &document); err != nil {
		t.Fatalf("json.Unmarshal(config) error = %v", err)
	}
	document["version"] = json.RawMessage(strconv.Itoa(config.CurrentVersion - 1))
	raw, err = json.MarshalIndent(document, "", "  ")
	if err != nil {
		t.Fatalf("json.MarshalIndent(legacy) error = %v", err)
	}
	if err = os.WriteFile(harness.configPath, raw, 0o600); err != nil {
		t.Fatalf("WriteFile(legacy) error = %v", err)
	}
	directory := filepath.Dir(harness.configPath)
	before := reviewAttentionDirectorySnapshot(t, directory)
	revision, err := config.ConfigRevision(harness.configPath)
	if err != nil {
		t.Fatalf("ConfigRevision(legacy) error = %v", err)
	}

	recorder := reviewAttentionAgentsGET(
		t,
		harness,
		reviewAttentionAgentsPath,
		revision,
	)
	if recorder.Code != http.StatusInternalServerError ||
		decodeReviewAttentionError(t, recorder) != reviewAttentionAgentsUnavailable {
		t.Fatalf("legacy response = %d body=%s", recorder.Code, recorder.Body.String())
	}
	assertReviewAttentionResponseHeaders(t, recorder)
	after := reviewAttentionDirectorySnapshot(t, directory)
	if !reflect.DeepEqual(after, before) {
		t.Fatalf("legacy read migrated or wrote files: before=%v after=%v", before, after)
	}
}

func TestReviewAttentionAgentsOrphanedSecurityFailsClosedWithoutWrite(
	t *testing.T,
) {
	resetGatewayTestState(t)
	harness := newReviewAttentionAPITestHarness(t, func(cfg *config.Config) {
		configureReviewAttentionAgentFixtures(cfg, 1, true)
	})
	if err := os.Remove(harness.configPath); err != nil {
		t.Fatalf("Remove(public config) error = %v", err)
	}
	directory := filepath.Dir(harness.configPath)
	before := reviewAttentionDirectorySnapshot(t, directory)
	revision, err := config.ConfigRevision(harness.configPath)
	if err != nil {
		t.Fatalf("ConfigRevision(orphaned security) error = %v", err)
	}

	recorder := reviewAttentionAgentsGET(
		t,
		harness,
		reviewAttentionAgentsPath,
		revision,
	)
	if recorder.Code != http.StatusInternalServerError ||
		decodeReviewAttentionError(t, recorder) != reviewAttentionAgentsUnavailable {
		t.Fatalf("orphaned response = %d body=%s", recorder.Code, recorder.Body.String())
	}
	assertReviewAttentionResponseHeaders(t, recorder)
	after := reviewAttentionDirectorySnapshot(t, directory)
	if !reflect.DeepEqual(after, before) {
		t.Fatalf("orphaned read wrote files: before=%v after=%v", before, after)
	}
}
