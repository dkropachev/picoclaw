package capabilities

import (
	"fmt"
	"strings"

	"github.com/sipeed/picoclaw/pkg/providers/protocoltypes"
)

// ASRRoute identifies the wire protocol used for a configured transcription
// model.
type ASRRoute string

const (
	ASRRouteElevenLabs ASRRoute = "elevenlabs"
	ASRRouteWhisper    ASRRoute = "whisper"
	ASRRouteAudioModel ASRRoute = "audio-model"
)

// TTSRoute identifies the wire protocol used for a configured speech model.
type TTSRoute string

const (
	TTSRouteOpenAI TTSRoute = "openai-speech"
	TTSRouteMimo   TTSRoute = "mimo"
)

const ElevenLabsASRModelID = "scribe_v1"

var audioModelASRProviders = map[string]struct{}{
	"openai":         {},
	"azure":          {},
	"litellm":        {},
	"openrouter":     {},
	"groq":           {},
	"zhipu":          {},
	"gemini":         {},
	"nvidia":         {},
	"ollama":         {},
	"moonshot":       {},
	"shengsuanyun":   {},
	"deepseek":       {},
	"cerebras":       {},
	"vivgrid":        {},
	"volcengine":     {},
	"vllm":           {},
	"qwen-portal":    {},
	"qwen-intl":      {},
	"qwen-us":        {},
	"mistral":        {},
	"avian":          {},
	"minimax":        {},
	"longcat":        {},
	"modelscope":     {},
	"novita":         {},
	"alibaba-coding": {},
	"zai":            {},
}

var whisperASRProviders = map[string]struct{}{
	"openai":         {},
	"litellm":        {},
	"openrouter":     {},
	"groq":           {},
	"zhipu":          {},
	"nvidia":         {},
	"ollama":         {},
	"moonshot":       {},
	"shengsuanyun":   {},
	"deepseek":       {},
	"cerebras":       {},
	"vivgrid":        {},
	"volcengine":     {},
	"vllm":           {},
	"qwen-portal":    {},
	"qwen-intl":      {},
	"qwen-us":        {},
	"mistral":        {},
	"avian":          {},
	"minimax":        {},
	"longcat":        {},
	"modelscope":     {},
	"novita":         {},
	"alibaba-coding": {},
	"zai":            {},
	"mimo":           {},
}

// ResolveASRRoute validates a provider/model pair against the transcription
// transports implemented by pkg/audio/asr.
func ResolveASRRoute(provider, modelID string) (ASRRoute, error) {
	provider = protocoltypes.NormalizeProvider(provider)
	modelID = strings.TrimSpace(modelID)
	if provider == "" {
		return "", fmt.Errorf("no account provider configured")
	}
	if modelID == "" {
		return "", protocoltypes.ErrNoModelConfigured
	}

	if provider == "elevenlabs" {
		if modelID != ElevenLabsASRModelID {
			return "", fmt.Errorf(
				"provider %q only supports ASR model %q, got %q",
				provider,
				ElevenLabsASRModelID,
				modelID,
			)
		}
		return ASRRouteElevenLabs, nil
	}

	if strings.Contains(strings.ToLower(modelID), "whisper") {
		if _, ok := whisperASRProviders[provider]; ok {
			return ASRRouteWhisper, nil
		}
	}
	if _, ok := audioModelASRProviders[provider]; ok {
		return ASRRouteAudioModel, nil
	}
	return "", fmt.Errorf(
		"provider %q does not support the configured ASR transport for model %q",
		provider,
		modelID,
	)
}

// ResolveTTSRoute validates a provider/model pair against the synthesis
// transports implemented by pkg/audio/tts. In particular, an arbitrary chat
// provider must never be treated as OpenAI-compatible speech synthesis.
func ResolveTTSRoute(provider, modelID string) (TTSRoute, error) {
	provider = protocoltypes.NormalizeProvider(provider)
	modelID = strings.TrimSpace(modelID)
	if provider == "" {
		return "", fmt.Errorf("no account provider configured")
	}
	if modelID == "" {
		return "", protocoltypes.ErrNoModelConfigured
	}

	if provider == "mimo" {
		return TTSRouteMimo, nil
	}
	if protocoltypes.UsesOpenAICompatibleHTTPTransport(provider) {
		return TTSRouteOpenAI, nil
	}
	return "", fmt.Errorf(
		"provider %q does not support speech synthesis via the OpenAI-compatible /audio/speech transport",
		provider,
	)
}
