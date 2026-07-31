package model

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sipeed/picoclaw/pkg/config"
)

func TestNewAddCommand_ExposesSeparateAccountAndAliasFlags(t *testing.T) {
	cmd := newAddCommand()

	require.NotNil(t, cmd.Flags().Lookup("account"))
	require.NotNil(t, cmd.Flags().Lookup("name"))
	assert.Contains(t, cmd.Flags().Lookup("account").Usage, "Account name")
	assert.Contains(t, cmd.Flags().Lookup("name").Usage, "Model alias")
}

func TestFetchOpenAIModels_DataEnvelope(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/models", r.URL.Path)
		assert.Equal(t, "Bearer secret", r.Header.Get("Authorization"))
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"id":"gpt-foo","name":"Foo"},{"id":"gpt-bar"}]}`))
	}))
	defer srv.Close()

	entries, err := fetchOpenAIModels(srv.URL, "secret")
	require.NoError(t, err)
	require.Len(t, entries, 2)
	assert.Equal(t, "gpt-foo", entries[0].ID)
	assert.Equal(t, "Foo", entries[0].Name)
	assert.Equal(t, "gpt-bar", entries[1].ID)
}

func TestFetchOpenAIModels_BareArray(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[{"id":"a"},{"id":"b"}]`))
	}))
	defer srv.Close()

	entries, err := fetchOpenAIModels(srv.URL, "secret")
	require.NoError(t, err)
	require.Len(t, entries, 2)
	assert.Equal(t, "a", entries[0].ID)
	assert.Equal(t, "b", entries[1].ID)
}

func TestFetchOpenAIModels_TrimsTrailingSlash(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		_, _ = w.Write([]byte(`{"data":[{"id":"x"}]}`))
	}))
	defer srv.Close()

	_, err := fetchOpenAIModels(srv.URL+"/", "k")
	require.NoError(t, err)
	assert.Equal(t, "/models", gotPath)
}

func TestFetchOpenAIModels_HTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "nope", http.StatusUnauthorized)
	}))
	defer srv.Close()

	_, err := fetchOpenAIModels(srv.URL, "bad")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "HTTP 401")
}

func TestFetchOpenAIModels_HTTPErrorReadFailure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		body := "short"
		w.Header().Set("Content-Length", strconv.Itoa(len(body)+1))
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(body))
	}))
	defer srv.Close()

	_, err := fetchOpenAIModels(srv.URL, "bad")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "read error response")
	assert.Contains(t, err.Error(), "unexpected EOF")
}

func TestFetchOpenAIModels_EmptyDataEnvelope(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"data":[]}`))
	}))
	defer srv.Close()

	entries, err := fetchOpenAIModels(srv.URL, "k")
	require.NoError(t, err)
	assert.Empty(t, entries)
}

func TestFetchOpenAIModels_EmptyBareArray(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`[]`))
	}))
	defer srv.Close()

	entries, err := fetchOpenAIModels(srv.URL, "k")
	require.NoError(t, err)
	assert.Empty(t, entries)
}

func TestFetchOpenAIModels_UnrecognizedShape(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"models":"not-supported"}`))
	}))
	defer srv.Close()

	_, err := fetchOpenAIModels(srv.URL, "k")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unrecognized shape")
}

func TestFetchOpenAIModels_RequiresInputs(t *testing.T) {
	_, err := fetchOpenAIModels("", "k")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "api base")

	_, err = fetchOpenAIModels("https://example.com", "")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "api key")
}

func TestPickModel_ByIndex(t *testing.T) {
	entries := []modelEntry{{ID: "a"}, {ID: "b"}, {ID: "c"}}
	out := &bytes.Buffer{}
	got, err := pickModel(strings.NewReader("2\n"), out, entries)
	require.NoError(t, err)
	assert.Equal(t, "b", got)
	assert.Contains(t, out.String(), "3 model(s) available")
}

func TestPickModel_ByID(t *testing.T) {
	entries := []modelEntry{{ID: "alpha"}, {ID: "beta"}}
	out := &bytes.Buffer{}
	got, err := pickModel(strings.NewReader("beta\n"), out, entries)
	require.NoError(t, err)
	assert.Equal(t, "beta", got)
}

func TestPickModel_RetriesOnInvalid(t *testing.T) {
	entries := []modelEntry{{ID: "x"}}
	out := &bytes.Buffer{}
	got, err := pickModel(strings.NewReader("\n9\nnot-a-model\nx\n"), out, entries)
	require.NoError(t, err)
	assert.Equal(t, "x", got)
	rendered := out.String()
	assert.Contains(t, rendered, "Out of range")
	assert.Contains(t, rendered, "Not a valid number")
}

func TestRunAdd_WithExplicitModel_NoNetwork(t *testing.T) {
	initTest(t)

	out := &bytes.Buffer{}
	err := runAdd(addOptions{
		apiBase:    "https://invalid.invalid/v1",
		apiKey:     "k",
		modelID:    "explicit-model",
		alias:      "myalias",
		accountRef: "my-account",
		modelType:  "openai-compatible",
		stdout:     out,
	})
	require.NoError(t, err)
	assert.Contains(t, out.String(), "Saved account 'my-account' and alias 'myalias' (explicit-model)")

	cfg, err := config.LoadConfig(configPath)
	require.NoError(t, err)
	assert.Equal(t, "my-account", cfg.Agents.Defaults.AccountRef)
	assert.Equal(t, "myalias", cfg.Agents.Defaults.GetModelName())
	added := findModelByName(cfg, "my-account")
	require.NotNil(t, added, "expected account 'my-account' in model_list")
	assert.Equal(t, "explicit-model", added.Model)
	assert.Equal(t, "https://invalid.invalid/v1", added.APIBase)
	assert.True(t, added.Enabled)
	require.Len(t, added.APIKeys, 1)
	assert.Equal(t, "k", added.APIKeys[0].String())
	modelAlias := findModelAlias(cfg, "myalias")
	require.NotNil(t, modelAlias)
	assert.Equal(t, "explicit-model", modelAlias.Model)
}

func findModelByName(cfg *config.Config, name string) *config.ModelConfig {
	for _, m := range cfg.ModelList {
		if m != nil && m.ModelName == name {
			return m
		}
	}
	return nil
}

func findModelAlias(cfg *config.Config, name string) *config.ModelAliasConfig {
	for i := range cfg.ModelAliases {
		if cfg.ModelAliases[i].Name == name {
			return &cfg.ModelAliases[i]
		}
	}
	return nil
}

func TestRunAdd_FetchAndPick(t *testing.T) {
	initTest(t)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "Bearer my-key", r.Header.Get("Authorization"))
		_, _ = w.Write([]byte(`{"data":[{"id":"m1"},{"id":"m2"}]}`))
	}))
	defer srv.Close()

	out := &bytes.Buffer{}
	err := runAdd(addOptions{
		apiBase:   srv.URL,
		apiKey:    "my-key",
		alias:     defaultAliasName,
		modelType: "openai-compatible",
		stdin:     strings.NewReader("2\n"),
		stdout:    out,
	})
	require.NoError(t, err)

	cfg, err := config.LoadConfig(configPath)
	require.NoError(t, err)
	assert.Equal(t, defaultAliasName, cfg.Agents.Defaults.AccountRef)
	assert.Equal(t, defaultAliasName, cfg.Agents.Defaults.GetModelName())
	added := findModelByName(cfg, defaultAliasName)
	require.NotNil(t, added)
	assert.Equal(t, "m2", added.Model)
	modelAlias := findModelAlias(cfg, defaultAliasName)
	require.NotNil(t, modelAlias)
	assert.Equal(t, "m2", modelAlias.Model)
}

func TestRunAdd_UpsertsExistingAlias(t *testing.T) {
	initTest(t)

	first := &bytes.Buffer{}
	require.NoError(t, runAdd(addOptions{
		apiBase: "https://a.example/v1",
		apiKey:  "k1",
		modelID: "m1",
		alias:   "shared",
		stdout:  first,
	}))

	second := &bytes.Buffer{}
	require.NoError(t, runAdd(addOptions{
		apiBase: "https://b.example/v1",
		apiKey:  "k2",
		modelID: "m2",
		alias:   "shared",
		stdout:  second,
	}))

	cfg, err := config.LoadConfig(configPath)
	require.NoError(t, err)
	matches := 0
	for _, m := range cfg.ModelList {
		if m != nil && m.ModelName == "shared" {
			matches++
		}
	}
	assert.Equal(t, 1, matches, "alias should be updated, not duplicated")
	aliasMatches := 0
	for i := range cfg.ModelAliases {
		if cfg.ModelAliases[i].Name == "shared" {
			aliasMatches++
		}
	}
	assert.Equal(t, 1, aliasMatches, "model alias should be updated, not duplicated")

	updated := findModelByName(cfg, "shared")
	require.NotNil(t, updated)
	assert.Equal(t, "m2", updated.Model)
	assert.Equal(t, "https://b.example/v1", updated.APIBase)
	assert.Equal(t, "k2", updated.APIKeys[0].String())
	updatedAlias := findModelAlias(cfg, "shared")
	require.NotNil(t, updatedAlias)
	assert.Equal(t, "m2", updatedAlias.Model)
}

func TestRunAdd_RejectsUnsupportedType(t *testing.T) {
	initTest(t)

	err := runAdd(addOptions{
		apiBase:   "https://x/v1",
		apiKey:    "k",
		modelID:   "m",
		alias:     "a",
		modelType: "anthropic",
		stdout:    &bytes.Buffer{},
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unsupported --type")
}

func TestRunAdd_RejectsReservedCredentialAccountName(t *testing.T) {
	initTest(t)

	err := runAdd(addOptions{
		apiBase:    "https://x/v1",
		apiKey:     "k",
		modelID:    "m",
		alias:      "coding",
		accountRef: "credential:openai:work",
		stdout:     &bytes.Buffer{},
	})

	require.EqualError(
		t,
		err,
		`--account "credential:openai:work" uses the reserved credential-account namespace`,
	)
}

func TestRunAdd_RejectsExistingNonOpenAICompatibleAccount(t *testing.T) {
	initTest(t)

	cfg := config.DefaultConfig()
	cfg.ModelList = []*config.ModelConfig{{
		ModelName: "work",
		Provider:  "anthropic",
		Model:     "claude-sonnet-4-6",
		Enabled:   true,
	}}
	require.NoError(t, config.SaveConfig(configPath, cfg))

	err := runAdd(addOptions{
		apiBase:    "https://openai.example/v1",
		apiKey:     "k",
		modelID:    "gpt-5.4",
		alias:      "coding",
		accountRef: "work",
		stdout:     &bytes.Buffer{},
	})

	require.EqualError(
		t,
		err,
		`account "work" uses provider "anthropic"; --type openai-compatible requires an OpenAI-compatible account`,
	)

	unchanged, loadErr := config.LoadConfig(configPath)
	require.NoError(t, loadErr)
	require.Equal(t, "anthropic", findModelByName(unchanged, "work").Provider)
	require.Equal(t, "claude-sonnet-4-6", findModelByName(unchanged, "work").Model)
}
