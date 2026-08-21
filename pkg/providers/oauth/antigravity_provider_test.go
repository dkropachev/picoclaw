package oauthprovider

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/sipeed/picoclaw/pkg/auth"
	"github.com/sipeed/picoclaw/pkg/config"
)

func TestFetchAntigravityModelsContextHonorsCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := FetchAntigravityModelsContext(ctx, "token", "project")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("FetchAntigravityModelsContext() error = %v, want context canceled", err)
	}
}

func TestAntigravityModelOrderingUsesID(t *testing.T) {
	models := []AntigravityModelInfo{
		{ID: "zeta"},
		{ID: "gemini-3-flash"},
		{ID: "Alpha"},
		{ID: "beta"},
	}
	sortAntigravityModels(models)

	got := []string{models[0].ID, models[1].ID, models[2].ID, models[3].ID}
	want := []string{"Alpha", "beta", "gemini-3-flash", "zeta"}
	if !slices.Equal(got, want) {
		t.Fatalf("model order = %v, want %v", got, want)
	}
}

func TestAntigravityProviderRejectsMissingModelBeforeAuth(t *testing.T) {
	authCalls := 0
	provider := &AntigravityProvider{
		tokenSource: func() (string, string, error) {
			authCalls++
			return "token", "project", nil
		},
	}

	_, err := provider.Chat(context.Background(), nil, nil, "  ", nil)
	if err == nil || err.Error() != "no model configured" {
		t.Fatalf("Chat() error = %v, want no model configured", err)
	}
	if authCalls != 0 {
		t.Fatalf("auth calls = %d, want 0", authCalls)
	}
}

func TestCreateAntigravityTokenSourceForCredentialUsesNamedCredential(t *testing.T) {
	t.Setenv(config.EnvHome, t.TempDir())

	if err := auth.SetCredential("google-antigravity", &auth.AuthCredential{
		AccessToken: "default-token",
		ProjectID:   "default-project",
		Provider:    "google-antigravity",
		AuthMethod:  "oauth",
	}); err != nil {
		t.Fatalf("SetCredential(default) error = %v", err)
	}
	if err := auth.SetCredential("google-antigravity:work", &auth.AuthCredential{
		AccessToken: "work-token",
		ProjectID:   "work-project",
		Provider:    "antigravity",
		AuthMethod:  "oauth",
	}); err != nil {
		t.Fatalf("SetCredential(work) error = %v", err)
	}

	token, projectID, err := CreateAntigravityTokenSourceForCredential("google-antigravity:work")()
	if err != nil {
		t.Fatalf("token source error = %v", err)
	}
	if token != "work-token" {
		t.Fatalf("token = %q, want named credential token", token)
	}
	if projectID != "work-project" {
		t.Fatalf("projectID = %q, want named credential project", projectID)
	}
}

func TestCreateAntigravityTokenSourceForCredentialRejectsProviderMismatch(t *testing.T) {
	t.Setenv(config.EnvHome, t.TempDir())

	if err := auth.SetCredential("google-antigravity:work", &auth.AuthCredential{
		AccessToken: "anthropic-secret",
		ProjectID:   "wrong-project",
		Provider:    "anthropic",
		AuthMethod:  "oauth",
	}); err != nil {
		t.Fatalf("SetCredential() error = %v", err)
	}

	_, _, err := CreateAntigravityTokenSourceForCredential("google-antigravity:work")()
	if err == nil {
		t.Fatal("token source error = nil, want provider mismatch")
	}
}

func TestAntigravityNamedCredentialRefreshPersistsToNamedCredential(t *testing.T) {
	t.Setenv(config.EnvHome, t.TempDir())
	credentialID := "google-antigravity:work"
	expired := &auth.AuthCredential{
		AccessToken:  "expired-token",
		RefreshToken: "refresh-token",
		ExpiresAt:    time.Now().Add(-time.Hour),
		Provider:     "google-antigravity",
		AuthMethod:   "oauth",
		Email:        "work@example.com",
		ProjectID:    "work-project",
	}
	if err := auth.SetCredential(credentialID, expired); err != nil {
		t.Fatalf("SetCredential() error = %v", err)
	}

	var fetchedToken string
	source := createAntigravityTokenSourceForCredential(
		credentialID,
		antigravityTokenSourceDependencies{
			getCredential:     auth.GetCredential,
			refreshCredential: auth.RefreshCredential,
			refreshToken: func(
				credential *auth.AuthCredential,
				_ auth.OAuthProviderConfig,
			) (*auth.AuthCredential, error) {
				if credential.AccessToken != expired.AccessToken {
					t.Fatalf("refresh received %+v, want expired credential", credential)
				}
				return &auth.AuthCredential{
					AccessToken:  "refreshed-token",
					RefreshToken: "refresh-token",
					ExpiresAt:    time.Now().Add(time.Hour),
					Provider:     "antigravity",
					AuthMethod:   "oauth",
				}, nil
			},
			fetchProjectID: func(accessToken string) (string, error) {
				fetchedToken = accessToken
				return "refreshed-project", nil
			},
		},
	)

	token, projectID, err := source()
	if err != nil {
		t.Fatalf("token source error = %v", err)
	}
	if token != "refreshed-token" {
		t.Fatalf("token = %q, want refreshed token", token)
	}
	if projectID != "refreshed-project" || fetchedToken != "refreshed-token" {
		t.Fatalf("project resolution = (%q, %q), want refreshed token/project pair", fetchedToken, projectID)
	}
	savedCredential, err := auth.GetCredential(credentialID)
	if err != nil {
		t.Fatalf("GetCredential() error = %v", err)
	}
	if savedCredential == nil ||
		savedCredential.AccessToken != "refreshed-token" ||
		savedCredential.Email != "work@example.com" ||
		savedCredential.ProjectID != "refreshed-project" {
		t.Fatalf("saved credential = %+v, want refreshed token/project bundle", savedCredential)
	}
}

func TestAntigravityTokenSourceKeepsConcurrentRenewal(t *testing.T) {
	t.Setenv(config.EnvHome, t.TempDir())
	credentialID := "google-antigravity:work"
	if err := auth.SetCredential(credentialID, &auth.AuthCredential{
		AccessToken:  "expired-token",
		RefreshToken: "old-refresh-token",
		ExpiresAt:    time.Now().Add(-time.Hour),
		Provider:     "google-antigravity",
		AuthMethod:   "oauth",
		ProjectID:    "old-project",
	}); err != nil {
		t.Fatalf("SetCredential(expired) error = %v", err)
	}
	renewed := &auth.AuthCredential{
		AccessToken:  "renewed-token",
		RefreshToken: "new-refresh-token",
		ExpiresAt:    time.Now().Add(2 * time.Hour),
		Provider:     "google-antigravity",
		AuthMethod:   "oauth",
		ProjectID:    "renewed-project",
	}
	source := createAntigravityTokenSourceForCredential(
		credentialID,
		antigravityTokenSourceDependencies{
			getCredential:     auth.GetCredential,
			refreshCredential: auth.RefreshCredential,
			refreshToken: func(
				credential *auth.AuthCredential,
				_ auth.OAuthProviderConfig,
			) (*auth.AuthCredential, error) {
				if credential.AccessToken != "expired-token" {
					t.Fatalf("refresh credential = %+v, want expired credential", credential)
				}
				if err := auth.SetCredential(credentialID, renewed); err != nil {
					t.Fatalf("SetCredential(renewed) error = %v", err)
				}
				return &auth.AuthCredential{
					AccessToken:  "stale-refreshed-token",
					RefreshToken: "old-refresh-token",
					ExpiresAt:    time.Now().Add(time.Hour),
					Provider:     "google-antigravity",
					AuthMethod:   "oauth",
				}, nil
			},
			fetchProjectID: func(string) (string, error) {
				t.Fatal("project ID fetch should not run for the concurrent renewal")
				return "", nil
			},
		},
	)

	token, projectID, err := source()
	if err != nil {
		t.Fatalf("token source error = %v", err)
	}
	if token != "renewed-token" || projectID != "renewed-project" {
		t.Fatalf("token source = (%q, %q), want concurrent renewal", token, projectID)
	}
	stored, err := auth.GetCredential(credentialID)
	if err != nil {
		t.Fatalf("GetCredential() error = %v", err)
	}
	if stored == nil ||
		stored.AccessToken != "renewed-token" ||
		stored.RefreshToken != "new-refresh-token" {
		t.Fatalf("stored credential = %+v, want concurrent renewal", stored)
	}
}

func TestAntigravityProjectIDPersistenceKeepsConcurrentRenewal(t *testing.T) {
	t.Setenv(config.EnvHome, t.TempDir())
	credentialID := "google-antigravity:work"
	if err := auth.SetCredential(credentialID, &auth.AuthCredential{
		AccessToken: "old-token",
		Provider:    "google-antigravity",
		AuthMethod:  "oauth",
	}); err != nil {
		t.Fatalf("SetCredential(old) error = %v", err)
	}
	renewed := &auth.AuthCredential{
		AccessToken:  "renewed-token",
		RefreshToken: "new-refresh-token",
		ExpiresAt:    time.Now().Add(2 * time.Hour),
		Provider:     "google-antigravity",
		AuthMethod:   "oauth",
	}
	var projectFetchCalls int
	source := createAntigravityTokenSourceForCredential(
		credentialID,
		antigravityTokenSourceDependencies{
			getCredential:     auth.GetCredential,
			refreshCredential: auth.RefreshCredential,
			refreshToken: func(
				*auth.AuthCredential,
				auth.OAuthProviderConfig,
			) (*auth.AuthCredential, error) {
				t.Fatal("refresh should not run for an unexpired credential")
				return nil, nil
			},
			fetchProjectID: func(accessToken string) (string, error) {
				projectFetchCalls++
				switch projectFetchCalls {
				case 1:
					if accessToken != "old-token" {
						t.Fatalf("first project fetch token = %q, want old token", accessToken)
					}
					if err := auth.SetCredential(credentialID, renewed); err != nil {
						t.Fatalf("SetCredential(renewed) error = %v", err)
					}
					return "stale-project", nil
				case 2:
					if accessToken != "renewed-token" {
						t.Fatalf("second project fetch token = %q, want renewed token", accessToken)
					}
					return "renewed-project", nil
				default:
					t.Fatalf("unexpected project fetch call %d", projectFetchCalls)
					return "", nil
				}
			},
		},
	)

	token, projectID, err := source()
	if err != nil {
		t.Fatalf("token source error = %v", err)
	}
	if token != "renewed-token" || projectID != "renewed-project" {
		t.Fatalf("token source = (%q, %q), want concurrent renewal", token, projectID)
	}
	if projectFetchCalls != 2 {
		t.Fatalf("project fetch calls = %d, want 2", projectFetchCalls)
	}
	stored, err := auth.GetCredential(credentialID)
	if err != nil {
		t.Fatalf("GetCredential() error = %v", err)
	}
	if stored == nil ||
		stored.AccessToken != "renewed-token" ||
		stored.ProjectID != "renewed-project" {
		t.Fatalf("stored credential = %+v, want concurrent renewal", stored)
	}
}

func TestAntigravityProjectFallbackRejectsConcurrentProviderReplacement(t *testing.T) {
	credentialID := "google-antigravity:work"
	reads := 0
	source := createAntigravityTokenSourceForCredential(
		credentialID,
		antigravityTokenSourceDependencies{
			getCredential: func(string) (*auth.AuthCredential, error) {
				reads++
				if reads == 1 {
					return &auth.AuthCredential{
						AccessToken: "antigravity-token",
						Provider:    "google-antigravity",
						AuthMethod:  "oauth",
					}, nil
				}
				return &auth.AuthCredential{
					AccessToken: "other-provider-token",
					ProjectID:   "other-provider-project",
					Provider:    "openai",
					AuthMethod:  "oauth",
				}, nil
			},
			refreshCredential: func(
				string,
				func(*auth.AuthCredential) bool,
				auth.RefreshCredentialFunc,
			) (*auth.AuthCredential, error) {
				return nil, errors.New("project lookup failed")
			},
			refreshToken: func(
				*auth.AuthCredential,
				auth.OAuthProviderConfig,
			) (*auth.AuthCredential, error) {
				t.Fatal("token refresh should not run")
				return nil, nil
			},
			fetchProjectID: func(string) (string, error) {
				t.Fatal("project fetch is handled by the refresh dependency")
				return "", nil
			},
		},
	)

	_, _, err := source()
	if err == nil || !strings.Contains(err.Error(), "belongs to provider") {
		t.Fatalf("token source error = %v, want provider mismatch", err)
	}
}

func TestBuildRequestUsesFunctionFieldsWhenToolCallNameMissing(t *testing.T) {
	p := &AntigravityProvider{}

	messages := []Message{
		{
			Role: "assistant",
			ToolCalls: []ToolCall{{
				ID: "call_read_file_123",
				Function: &FunctionCall{
					Name:      "read_file",
					Arguments: `{"path":"README.md"}`,
				},
			}},
		},
		{
			Role:       "tool",
			ToolCallID: "call_read_file_123",
			Content:    "ok",
		},
	}

	req := p.buildRequest(messages, nil, "", nil)
	if len(req.Contents) != 2 {
		t.Fatalf("expected 2 contents, got %d", len(req.Contents))
	}

	modelPart := req.Contents[0].Parts[0]
	if modelPart.FunctionCall == nil {
		t.Fatal("expected functionCall in assistant message")
	}
	if modelPart.FunctionCall.Name != "read_file" {
		t.Fatalf("expected functionCall name read_file, got %q", modelPart.FunctionCall.Name)
	}
	if got := modelPart.FunctionCall.Args["path"]; got != "README.md" {
		t.Fatalf("expected functionCall args[path] to be README.md, got %v", got)
	}

	toolPart := req.Contents[1].Parts[0]
	if toolPart.FunctionResponse == nil {
		t.Fatal("expected functionResponse in tool message")
	}
	if toolPart.FunctionResponse.Name != "read_file" {
		t.Fatalf("expected functionResponse name read_file, got %q", toolPart.FunctionResponse.Name)
	}
}

func TestParseSSEResponse_SplitsThoughtAndVisibleContent(t *testing.T) {
	p := &AntigravityProvider{}
	body := "data: {\"response\":{\"candidates\":[{\"content\":{\"parts\":[{\"text\":\"hidden reasoning\",\"thought\":true},{\"text\":\"visible answer\"}],\"role\":\"model\"},\"finishReason\":\"STOP\"}],\"usageMetadata\":{\"promptTokenCount\":8,\"candidatesTokenCount\":17,\"thoughtsTokenCount\":191,\"totalTokenCount\":216}}}\n" +
		"data: [DONE]\n"

	resp, err := p.parseSSEResponse(body)
	if err != nil {
		t.Fatalf("parseSSEResponse() error = %v", err)
	}

	if resp.Content != "visible answer" {
		t.Fatalf("Content = %q, want %q", resp.Content, "visible answer")
	}
	if resp.ReasoningContent != "hidden reasoning" {
		t.Fatalf("ReasoningContent = %q, want %q", resp.ReasoningContent, "hidden reasoning")
	}
	if resp.FinishReason != "stop" {
		t.Fatalf("FinishReason = %q, want %q", resp.FinishReason, "stop")
	}
	if resp.Usage == nil || resp.Usage.TotalTokens != 216 || resp.Usage.CompletionTokens != 208 ||
		resp.Usage.ReasoningTokens != 191 {
		t.Fatalf("Usage = %v, want total=216 reasoning=191", resp.Usage)
	}
}

func TestBuildRequest_PreservesComplexToolSchemasByDefault(t *testing.T) {
	p := &AntigravityProvider{}
	schema := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"parent": map[string]any{
				"anyOf": []any{
					map[string]any{"$ref": "#/$defs/pageParent"},
					map[string]any{"$ref": "#/$defs/databaseParent"},
				},
			},
			"icon": map[string]any{
				"anyOf": []any{
					map[string]any{"type": "null"},
					map[string]any{"$ref": "#/$defs/emoji"},
				},
			},
		},
		"$defs": map[string]any{
			"pageParent": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"page_id": map[string]any{"type": "string"},
				},
				"required": []any{"page_id"},
			},
			"databaseParent": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"database_id": map[string]any{"type": "string"},
				},
				"required": []any{"database_id"},
			},
			"emoji": map[string]any{
				"type":    "string",
				"pattern": "^:[a-z_]+:$",
			},
		},
	}

	req := p.buildRequest(
		[]Message{{Role: "user", Content: "hello"}},
		[]ToolDefinition{{
			Type: "function",
			Function: ToolFunctionDefinition{
				Name:        "mcp_notion_create",
				Description: "Create a Notion object",
				Parameters:  schema,
			},
		}},
		"gemini-3-flash",
		nil,
	)

	if len(req.Tools) != 1 || len(req.Tools[0].FunctionDeclarations) != 1 {
		t.Fatalf("request tools = %#v, want one function declaration", req.Tools)
	}

	got, ok := req.Tools[0].FunctionDeclarations[0].Parameters.(map[string]any)
	if !ok {
		t.Fatalf("parameters = %#v, want map", req.Tools[0].FunctionDeclarations[0].Parameters)
	}
	if got["$defs"] == nil {
		t.Fatalf("parameters = %#v, want raw schema with $defs preserved by default", got)
	}
}
