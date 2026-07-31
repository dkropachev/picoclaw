package asr

import (
	"context"
	"fmt"

	audiocapabilities "github.com/sipeed/picoclaw/pkg/audio/capabilities"
	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/providers"
)

func ElevenLabsSupportedModelID() string {
	return audiocapabilities.ElevenLabsASRModelID
}

type Transcriber interface {
	Name() string
	Transcribe(ctx context.Context, audioFilePath string) (*TranscriptionResponse, error)
}

type TranscriptionResponse struct {
	Text     string  `json:"text"`
	Language string  `json:"language,omitempty"`
	Duration float64 `json:"duration,omitempty"`
}

func transcriberFromModelConfigWithError(
	modelCfg *config.ModelConfig,
) (Transcriber, error) {
	route, provider, modelID, err := asrRouteFromModelConfig(modelCfg)
	if err != nil {
		return nil, err
	}
	if (route == audiocapabilities.ASRRouteElevenLabs ||
		route == audiocapabilities.ASRRouteWhisper) &&
		modelCfg.APIKey() == "" {
		return nil, fmt.Errorf(
			"voice transcription provider %q has no API key configured",
			provider,
		)
	}

	resolved := *modelCfg
	resolved.Provider = provider
	resolved.Model = modelID
	switch route {
	case audiocapabilities.ASRRouteElevenLabs:
		return NewElevenLabsTranscriber(
			resolved.APIKey(),
			resolved.APIBase,
			modelID,
		), nil
	case audiocapabilities.ASRRouteWhisper:
		transcriber := NewWhisperTranscriber(&resolved)
		if transcriber == nil {
			return nil, fmt.Errorf(
				"failed to initialize Whisper transcription provider %q",
				provider,
			)
		}
		return transcriber, nil
	case audiocapabilities.ASRRouteAudioModel:
		transcriber := NewAudioModelTranscriber(&resolved)
		if transcriber == nil {
			return nil, fmt.Errorf(
				"failed to initialize audio-model transcription provider %q",
				provider,
			)
		}
		return transcriber, nil
	default:
		return nil, fmt.Errorf(
			"voice transcription provider %q has no supported ASR route",
			provider,
		)
	}
}

func asrRouteFromModelConfig(
	modelCfg *config.ModelConfig,
) (audiocapabilities.ASRRoute, string, string, error) {
	if modelCfg == nil {
		return "", "", "", config.ErrNoModelConfigured
	}
	provider, configuredModel := providers.ExtractProtocol(modelCfg)
	modelID, err := providers.ResolveModelForProvider(provider, configuredModel)
	if err != nil {
		return "", "", "", err
	}
	route, err := audiocapabilities.ResolveASRRoute(provider, modelID)
	if err != nil {
		return "", provider, modelID, err
	}
	return route, provider, modelID, nil
}

// DetectTranscriber resolves the explicitly configured ASR account and model
// alias. It never scans model_list or invents a provider model.
func DetectTranscriber(cfg *config.Config) Transcriber {
	transcriber, _ := DetectTranscriberWithError(cfg)
	return transcriber
}

// DetectTranscriberWithError is the diagnostic form of DetectTranscriber. It
// distinguishes intentionally unconfigured ASR from an invalid or unsupported
// account/model selection.
func DetectTranscriberWithError(cfg *config.Config) (Transcriber, error) {
	if cfg == nil {
		return nil, nil
	}

	modelCfg, err := cfg.ResolveVoiceASRModelConfig()
	if err != nil {
		return nil, err
	}
	if modelCfg == nil {
		return nil, nil
	}
	return transcriberFromModelConfigWithError(modelCfg)
}
