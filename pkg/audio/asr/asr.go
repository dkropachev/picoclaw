package asr

import (
	"context"
	"fmt"

	audiocapabilities "github.com/sipeed/picoclaw/pkg/audio/capabilities"
	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/isolation"
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
	executionPolicy isolation.ExecutionPolicy,
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
		transcriber := NewAudioModelTranscriberWithExecutionPolicy(&resolved, executionPolicy)
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
	if cfg == nil {
		return nil
	}
	executionPolicy := isolation.NewExecutionPolicy(cfg.Isolation)
	return DetectTranscriberWithExecutionPolicy(cfg, executionPolicy)
}

// DetectTranscriberWithExecutionPolicy resolves the configured ASR model while
// retaining the exact immutable subprocess policy owned by the caller's
// runtime generation.
func DetectTranscriberWithExecutionPolicy(
	cfg *config.Config,
	executionPolicy isolation.ExecutionPolicy,
) Transcriber {
	transcriber, _ := detectTranscriberWithExecutionPolicyError(cfg, executionPolicy)
	return transcriber
}

// DetectTranscriberWithError is the diagnostic form of DetectTranscriber. It
// distinguishes intentionally unconfigured ASR from an invalid or unsupported
// account/model selection.
func DetectTranscriberWithError(cfg *config.Config) (Transcriber, error) {
	if cfg == nil {
		return nil, nil
	}
	executionPolicy := isolation.NewExecutionPolicy(cfg.Isolation)
	return detectTranscriberWithExecutionPolicyError(cfg, executionPolicy)
}

func detectTranscriberWithExecutionPolicyError(
	cfg *config.Config,
	executionPolicy isolation.ExecutionPolicy,
) (Transcriber, error) {
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
	return transcriberFromModelConfigWithError(modelCfg, executionPolicy)
}
