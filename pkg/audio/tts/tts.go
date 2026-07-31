package tts

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	audiocapabilities "github.com/sipeed/picoclaw/pkg/audio/capabilities"
	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/media"
	"github.com/sipeed/picoclaw/pkg/providers"
)

type TTSProvider interface {
	Name() string
	Synthesize(ctx context.Context, text string) (io.ReadCloser, error)
}

type ttsAudioMetaProvider interface {
	AudioFileMeta() (fileExt string, contentType string)
}

func providerFromModelConfigWithError(mc *config.ModelConfig) (TTSProvider, error) {
	if mc == nil {
		return nil, config.ErrNoModelConfigured
	}
	protocol, configuredModel := providers.ExtractProtocol(mc)
	modelID, err := providers.ResolveModelForProvider(protocol, configuredModel)
	if err != nil {
		return nil, err
	}
	route, err := audiocapabilities.ResolveTTSRoute(protocol, modelID)
	if err != nil {
		return nil, err
	}
	if mc.APIKey() == "" {
		return nil, fmt.Errorf(
			"voice TTS provider %q has no API key configured",
			protocol,
		)
	}

	resolved := *mc
	resolved.Provider = protocol
	resolved.Model = modelID
	switch route {
	case audiocapabilities.TTSRouteMimo:
		return NewMimoTTSProvider(
			resolved.APIKey(),
			providers.ResolveAPIBase(&resolved),
			modelID,
			resolved.Proxy,
		), nil
	case audiocapabilities.TTSRouteOpenAI:
		return NewOpenAITTSProviderWithOptions(
			resolved.APIKey(),
			providers.ResolveAPIBase(&resolved),
			resolved.Proxy,
			modelID,
			openAITTSOptionsFromModelConfig(&resolved),
		), nil
	default:
		return nil, fmt.Errorf(
			"voice TTS provider %q has no supported synthesis route",
			protocol,
		)
	}
}

func openAITTSOptionsFromModelConfig(mc *config.ModelConfig) OpenAITTSOptions {
	options := OpenAITTSOptions{}
	if mc == nil || mc.ExtraBody == nil {
		return options
	}

	if voice, ok := mc.ExtraBody["voice"].(string); ok {
		options.Voice = strings.TrimSpace(voice)
	}
	if responseFormat, ok := mc.ExtraBody["response_format"].(string); ok {
		options.ResponseFormat = strings.TrimSpace(responseFormat)
	}
	return options
}

func DetectTTS(cfg *config.Config) TTSProvider {
	provider, _ := DetectTTSWithError(cfg)
	return provider
}

// DetectTTSWithError is the diagnostic form of DetectTTS. It reports invalid
// and unsupported account/model selections instead of silently constructing an
// OpenAI speech request for an unrelated provider.
func DetectTTSWithError(cfg *config.Config) (TTSProvider, error) {
	if cfg == nil {
		return nil, nil
	}

	modelCfg, err := cfg.ResolveVoiceTTSModelConfig()
	if err != nil {
		return nil, err
	}
	if modelCfg == nil {
		return nil, nil
	}
	return providerFromModelConfigWithError(modelCfg)
}

// SynthesizeAndStore synthesizes text to speech and registers it in the media store, returning the media reference.
func SynthesizeAndStore(
	ctx context.Context,
	provider TTSProvider,
	store media.MediaStore,
	text string,
	filename string,
	channel string,
	chatID string,
) (string, error) {
	if provider == nil {
		return "", config.ErrNoModelConfigured
	}
	if store == nil {
		return "", fmt.Errorf("media store not configured")
	}
	if channel == "" || chatID == "" {
		return "", fmt.Errorf("no target channel/chat available")
	}
	if strings.TrimSpace(text) == "" {
		return "", fmt.Errorf("text is required")
	}

	stream, err := provider.Synthesize(ctx, text)
	if err != nil {
		return "", fmt.Errorf("tts synthesize failed: %w", err)
	}
	defer stream.Close()

	err = os.MkdirAll(media.TempDir(), 0o700)
	if err != nil {
		return "", fmt.Errorf("failed to create media temp dir: %w", err)
	}

	fileExt := ".ogg"
	contentType := "audio/ogg"
	if provider.Name() == "mimo-tts" {
		fileExt = ".mp3"
		contentType = "audio/mpeg"
	}
	if metaProvider, ok := stream.(ttsAudioMetaProvider); ok {
		if ext, ct := metaProvider.AudioFileMeta(); ext != "" && ct != "" {
			fileExt = ext
			contentType = ct
		}
	}

	file, err := os.CreateTemp(media.TempDir(), "tts-*"+fileExt)
	if err != nil {
		return "", fmt.Errorf("failed to create temp file: %w", err)
	}

	removeTemp := true
	defer func() {
		if removeTemp {
			_ = os.Remove(file.Name())
		}
	}()

	_, err = io.Copy(file, stream)
	if err != nil {
		_ = file.Close()
		return "", fmt.Errorf("failed to write tts audio: %w", err)
	}

	err = file.Close()
	if err != nil {
		return "", fmt.Errorf("failed to close tts audio file: %w", err)
	}

	filename = strings.TrimSpace(filename)
	if filename == "" {
		filename = fmt.Sprintf("tts-%d%s", time.Now().Unix(), fileExt)
	}

	ext := strings.ToLower(filepath.Ext(filename))
	if ext == "" {
		filename += fileExt
	} else if ext != fileExt {
		filename = strings.TrimSuffix(filename, filepath.Ext(filename)) + fileExt
	}

	scope := fmt.Sprintf("tool:send_tts:%s:%s:%d", channel, chatID, time.Now().UnixNano())
	ref, err := store.Store(file.Name(), media.MediaMeta{
		Filename:    filename,
		ContentType: contentType,
		Source:      "tool:send_tts",
	}, scope)
	if err != nil {
		return "", fmt.Errorf("failed to register audio: %w", err)
	}
	removeTemp = false

	return ref, nil
}
