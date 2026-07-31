package oauthprovider

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"
	"github.com/openai/openai-go/v3/responses"
	"github.com/openai/openai-go/v3/shared"

	"github.com/sipeed/picoclaw/pkg/auth"
	"github.com/sipeed/picoclaw/pkg/logger"
	"github.com/sipeed/picoclaw/pkg/providers/common"
	orc "github.com/sipeed/picoclaw/pkg/providers/openai_responses_common"
	"github.com/sipeed/picoclaw/pkg/providers/protocoltypes"
)

const (
	codexDefaultInstructions = "You are Codex, a coding assistant."
)

type CodexProvider struct {
	client          *openai.Client
	token           string
	accountID       string
	tokenSource     func() (string, string, error)
	enableWebSearch bool
	rateLimitReset  *codexRateLimitResetter
}

const defaultCodexInstructions = "You are Codex, a coding assistant."

type codexStreamError struct {
	errorType string
	code      string
	message   string
	param     string
}

func (e *codexStreamError) Error() string {
	if e.message != "" {
		return e.message
	}
	if e.code != "" {
		return "codex response error: " + e.code
	}
	return "codex response failed"
}

func NewCodexProvider(token, accountID string) *CodexProvider {
	client := newCodexOpenAIClient(token, accountID)
	return &CodexProvider{
		client:          client,
		token:           token,
		accountID:       accountID,
		enableWebSearch: true,
		rateLimitReset:  newCodexRateLimitResetter(),
	}
}

func newCodexOpenAIClient(
	token string,
	accountID string,
	additionalOptions ...option.RequestOption,
) *openai.Client {
	opts := []option.RequestOption{
		option.WithBaseURL("https://chatgpt.com/backend-api/codex"),
		option.WithAPIKey(token),
		option.WithHeader("originator", "codex_cli_rs"),
		option.WithHeader("OpenAI-Beta", "responses=experimental"),
		option.WithMiddleware(codexUsageLimitNoRetryMiddleware),
	}
	if accountID != "" {
		opts = append(opts, option.WithHeader("Chatgpt-Account-Id", accountID))
	}
	opts = append(opts, additionalOptions...)
	client := openai.NewClient(opts...)
	return &client
}

func NewCodexProviderWithTokenSource(
	token, accountID string, tokenSource func() (string, string, error),
) *CodexProvider {
	p := NewCodexProvider(token, accountID)
	p.tokenSource = tokenSource
	return p
}

func (p *CodexProvider) Chat(
	ctx context.Context, messages []Message, tools []ToolDefinition, model string, options map[string]any,
) (*LLMResponse, error) {
	resolvedModel, err := protocoltypes.RequireModel(model)
	if err != nil {
		return nil, err
	}

	var opts []option.RequestOption
	token := p.token
	accountID := p.accountID
	if p.tokenSource != nil {
		tok, accID, tokenErr := p.tokenSource()
		if tokenErr != nil {
			return nil, fmt.Errorf("refreshing token: %w", tokenErr)
		}
		token = tok
		opts = append(opts, option.WithAPIKey(tok))
		if accID != "" {
			accountID = accID
		}
	}
	if accountID != "" {
		opts = append(opts, option.WithHeader("Chatgpt-Account-Id", accountID))
	} else {
		logger.WarnCF(
			"provider.codex",
			"No account id found for Codex request; backend may reject with 400",
			map[string]any{
				"requested_model": model,
				"resolved_model":  resolvedModel,
			},
		)
	}

	// Respect tools.web.prefer_native: only inject native search when the agent
	// loop passes options["native_search"]=true, so prefer_native=false means no injection.
	useNativeSearch := p.enableWebSearch && (options["native_search"] == true)
	params := buildCodexParams(messages, tools, resolvedModel, options, useNativeSearch)

	parsed, err := p.chatOnce(ctx, params, opts...)
	if isCodexUsageLimitReachedError(err) && p.rateLimitReset != nil {
		shouldRetry, resetErr := p.rateLimitReset.tryReset(ctx, token, accountID)
		if resetErr != nil {
			logger.WarnCF(
				"provider.codex",
				"Codex rate-limit auto-reset failed",
				map[string]any{
					"account_id_present": accountID != "",
					"reason":             codexRateLimitResetFailureReason(resetErr),
				},
			)
		}
		if errors.Is(resetErr, context.Canceled) ||
			errors.Is(resetErr, context.DeadlineExceeded) {
			err = resetErr
		} else if shouldRetry {
			parsed, err = p.chatOnce(ctx, params, opts...)
		}
	}
	if err == nil {
		return parsed, nil
	}

	fields := map[string]any{
		"requested_model":    model,
		"resolved_model":     resolvedModel,
		"messages_count":     len(messages),
		"tools_count":        len(tools),
		"account_id_present": accountID != "",
		"error":              err.Error(),
	}
	var apiErr *openai.Error
	if errors.As(err, &apiErr) {
		fields["status_code"] = apiErr.StatusCode
		fields["api_type"] = apiErr.Type
		fields["api_code"] = apiErr.Code
		fields["api_param"] = apiErr.Param
		fields["api_message"] = apiErr.Message
		if apiErr.StatusCode == 400 {
			fields["hint"] = "verify account id header and model compatibility for codex backend"
		}
		if apiErr.Response != nil {
			fields["request_id"] = apiErr.Response.Header.Get("x-request-id")
		}
	}
	logger.ErrorCF("provider.codex", "Codex API call failed", fields)
	return nil, fmt.Errorf("codex API call: %w", err)
}

func (p *CodexProvider) chatOnce(
	ctx context.Context,
	params responses.ResponseNewParams,
	opts ...option.RequestOption,
) (*LLMResponse, error) {
	stream := p.client.Responses.NewStreaming(ctx, params, opts...)
	defer stream.Close()

	var resp *responses.Response
	var terminalErr error
	var streamedText strings.Builder
	streamedOutputItems := make([]responses.ResponseOutputItemUnion, 0)
	for stream.Next() {
		evt := stream.Current()
		if evt.Type == "response.output_text.delta" {
			streamedText.WriteString(evt.Delta)
		}
		if evt.Type == "response.output_item.done" {
			itemEvt := evt.AsResponseOutputItemDone()
			if itemEvt.Item.Type != "" {
				streamedOutputItems = append(streamedOutputItems, itemEvt.Item)
			}
		}
		if evt.Type == "error" {
			terminalErr = &codexStreamError{
				code:    evt.Code,
				message: evt.Message,
				param:   evt.Param,
			}
		}
		if evt.Type == "response.completed" || evt.Type == "response.failed" || evt.Type == "response.incomplete" {
			evtResp := evt.Response
			if evtResp.ID != "" {
				evtRespCopy := evtResp
				resp = &evtRespCopy
			}
			if evt.Type == "response.failed" {
				terminalErr = codexFailedResponseError(evtResp)
			}
		}
	}
	err := stream.Err()
	if err != nil {
		if terminalErr != nil {
			return nil, terminalErr
		}
		return nil, err
	}
	if terminalErr != nil {
		return nil, terminalErr
	}
	if resp == nil {
		return nil, errors.New("stream ended without completed response")
	}
	if len(resp.Output) == 0 && len(streamedOutputItems) > 0 {
		resp.Output = streamedOutputItems
	}

	parsed := orc.ParseResponseFromStruct(resp)
	if parsed.Content == "" && streamedText.Len() > 0 {
		parsed.Content = streamedText.String()
	}
	return parsed, nil
}

func codexFailedResponseError(resp responses.Response) error {
	details := codexStreamError{
		code:    string(resp.Error.Code),
		message: resp.Error.Message,
	}
	if raw := resp.Error.RawJSON(); raw != "" {
		var payload struct {
			Type    string `json:"type"`
			Code    string `json:"code"`
			Message string `json:"message"`
			Param   string `json:"param"`
		}
		if json.Unmarshal([]byte(raw), &payload) == nil {
			details.errorType = payload.Type
			if payload.Code != "" {
				details.code = payload.Code
			}
			if payload.Message != "" {
				details.message = payload.Message
			}
			details.param = payload.Param
		}
	}
	return &details
}

func (p *CodexProvider) SupportsNativeSearch() bool {
	return p.enableWebSearch
}

func buildCodexParams(
	messages []Message, tools []ToolDefinition, model string, options map[string]any, enableWebSearch bool,
) responses.ResponseNewParams {
	inputItems, instructions := orc.TranslateMessages(messages)

	params := responses.ResponseNewParams{
		Model: model,
		Input: responses.ResponseNewParamsInputUnion{
			OfInputItemList: inputItems,
		},
		Store: openai.Opt(false),
	}

	if instructions != "" {
		params.Instructions = openai.Opt(instructions)
	} else {
		// ChatGPT Codex backend requires instructions to be present.
		params.Instructions = openai.Opt(defaultCodexInstructions)
	}

	// Prompt caching: pass a stable cache key so OpenAI can bucket requests
	// and reuse prefix KV cache across calls with the same key.
	// See: https://platform.openai.com/docs/guides/prompt-caching
	if cacheKey, ok := options["prompt_cache_key"].(string); ok && cacheKey != "" {
		params.PromptCacheKey = openai.Opt(cacheKey)
	}

	if len(tools) > 0 || enableWebSearch {
		params.Tools = orc.TranslateTools(tools, enableWebSearch)
	}

	if rawEffort, ok := options["reasoning_effort"].(string); ok {
		if effort, err := common.NormalizeReasoningEffort(rawEffort); err == nil && effort != "" {
			params.Reasoning = shared.ReasoningParam{
				Effort: shared.ReasoningEffort(effort),
			}
		}
	}

	return params
}

func CreateCodexTokenSource() func() (string, string, error) {
	return CreateCodexTokenSourceForCredential("openai")
}

func CreateCodexTokenSourceForCredential(credentialID string) func() (string, string, error) {
	return func() (string, string, error) {
		cred, err := auth.GetCredential(credentialID)
		if err != nil {
			return "", "", fmt.Errorf("loading auth credentials: %w", err)
		}
		if cred == nil {
			return "", "", fmt.Errorf(
				"no credentials for %s. Run: picoclaw auth login --provider openai --credential-id %s",
				credentialID,
				credentialID,
			)
		}

		if cred.AuthMethod == "oauth" && cred.NeedsRefresh() && cred.RefreshToken != "" {
			oauthCfg := auth.OpenAIOAuthConfig()
			refreshed, err := auth.RefreshAccessToken(cred, oauthCfg)
			if err != nil {
				return "", "", fmt.Errorf("refreshing token: %w", err)
			}
			if refreshed.AccountID == "" {
				refreshed.AccountID = cred.AccountID
			}
			if err := auth.SetCredential(credentialID, refreshed); err != nil {
				return "", "", fmt.Errorf("saving refreshed token: %w", err)
			}
			return refreshed.AccessToken, refreshed.AccountID, nil
		}

		return cred.AccessToken, cred.AccountID, nil
	}
}
