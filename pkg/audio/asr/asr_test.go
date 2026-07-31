package asr

import (
	"strings"
	"testing"

	"github.com/sipeed/picoclaw/pkg/config"
)

func TestDetectTranscriber(t *testing.T) {
	tests := []struct {
		name     string
		cfg      *config.Config
		wantNil  bool
		wantName string
	}{
		{
			name:    "no config",
			cfg:     &config.Config{},
			wantNil: true,
		},
		{
			name: "voice model name selects audio model transcriber",
			cfg: &config.Config{
				Voice: config.VoiceConfig{ModelName: "voice-gemini"},
				ModelList: []*config.ModelConfig{
					{
						ModelName: "voice-gemini",
						Model:     "gemini/gemini-2.5-flash",
						APIKeys:   config.SimpleSecureStrings("sk-gemini-model"),
					},
				},
			},
			wantName: "audio-model",
		},
		{
			name: "voice model name alias selects elevenlabs transcriber",
			cfg: &config.Config{
				Voice: config.VoiceConfig{ModelName: "my-asr-model"},
				ModelList: []*config.ModelConfig{
					{
						ModelName: "my-asr-model",
						Model:     "elevenlabs/scribe_v1",
						APIKeys:   config.SimpleSecureStrings("sk_elevenlabs_test"),
					},
				},
			},
			wantName: "elevenlabs",
		},
		{
			name: "explicit elevenlabs provider selects elevenlabs transcriber",
			cfg: &config.Config{
				Voice: config.VoiceConfig{ModelName: "my-asr-model"},
				ModelList: []*config.ModelConfig{
					{
						ModelName: "my-asr-model",
						Provider:  "elevenlabs",
						Model:     "scribe_v1",
						APIKeys:   config.SimpleSecureStrings("sk_elevenlabs_test"),
					},
				},
			},
			wantName: "elevenlabs",
		},
		{
			name: "voice model name alias selects whisper transcriber for groq",
			cfg: &config.Config{
				Voice: config.VoiceConfig{ModelName: "my-asr-model"},
				ModelList: []*config.ModelConfig{
					{
						ModelName: "my-asr-model",
						Model:     "groq/whisper-large-v3",
						APIKeys:   config.SimpleSecureStrings("sk-groq-model"),
					},
				},
			},
			wantName: "whisper",
		},
		{
			name: "openai whisper alias selects whisper transcriber",
			cfg: &config.Config{
				Voice: config.VoiceConfig{ModelName: "my-asr-model"},
				ModelList: []*config.ModelConfig{
					{
						ModelName: "my-asr-model",
						Model:     "openai/whisper-1",
						APIKeys:   config.SimpleSecureStrings("sk-openai-model"),
					},
				},
			},
			wantName: "whisper",
		},
		{
			name: "model list is not scanned without an explicit alias",
			cfg: &config.Config{
				ModelList: []*config.ModelConfig{
					{ModelName: "openai", Model: "openai/gpt-4o", APIKeys: config.SimpleSecureStrings("sk-openai")},
					{
						ModelName: "groq",
						Model:     "groq/whisper-large-v3-turbo",
						APIKeys:   config.SimpleSecureStrings("sk-groq-model"),
					},
				},
			},
			wantNil: true,
		},
		{
			name: "voice model name alias selects non-gemini audio model transcriber",
			cfg: &config.Config{
				Voice: config.VoiceConfig{ModelName: "my-asr-model"},
				ModelList: []*config.ModelConfig{
					{
						ModelName: "my-asr-model",
						Model:     "openai/gpt-4o-audio-preview",
						APIKeys:   config.SimpleSecureStrings("sk-openai"),
					},
				},
			},
			wantName: "audio-model",
		},
		{
			name: "voice model name selects azure audio model transcriber",
			cfg: &config.Config{
				Voice: config.VoiceConfig{ModelName: "voice-azure-audio"},
				ModelList: []*config.ModelConfig{
					{
						ModelName: "voice-azure-audio",
						Model:     "azure/my-audio-deployment", APIKeys: config.SimpleSecureStrings("sk-azure"),
						APIBase: "https://example.openai.azure.com",
					},
				},
			},
			wantName: "audio-model",
		},
		{
			name: "voice model name with non openai compatible protocol does not select audio model transcriber",
			cfg: &config.Config{
				Voice: config.VoiceConfig{ModelName: "voice-anthropic"},
				ModelList: []*config.ModelConfig{
					{
						ModelName: "voice-anthropic",
						Model:     "anthropic/claude-sonnet-4.6",
						APIKeys:   config.SimpleSecureStrings("sk-anthropic"),
					},
				},
			},
			wantNil: true,
		},
		{
			name: "groq model list entry without key is skipped",
			cfg: &config.Config{
				ModelList: []*config.ModelConfig{
					{Model: "groq/whisper-large-v3"},
				},
			},
			wantNil: true,
		},
		{
			name: "configured account without voice alias is ignored",
			cfg: &config.Config{
				ModelList: []*config.ModelConfig{
					{
						ModelName: "groq",
						Model:     "groq/whisper-large-v3",
						APIKeys:   config.SimpleSecureStrings("sk-groq-model"),
					},
				},
			},
			wantNil: true,
		},
		{
			name: "missing voice model name config returns nil",
			cfg: &config.Config{
				Voice: config.VoiceConfig{ModelName: "missing"},
				ModelList: []*config.ModelConfig{
					{
						ModelName: "other",
						Model:     "gemini/gemini-2.5-flash",
						APIKeys:   config.SimpleSecureStrings("sk-other-model"),
					},
				},
			},
			wantNil: true,
		},
		{
			name: "elevenlabs account without voice alias is ignored",
			cfg: &config.Config{
				ModelList: []*config.ModelConfig{
					{Model: "elevenlabs/scribe_v1", APIKeys: config.SimpleSecureStrings("sk_elevenlabs_test")},
				},
			},
			wantNil: true,
		},
		{
			name: "multiple accounts without a voice alias are ignored",
			cfg: &config.Config{
				ModelList: []*config.ModelConfig{
					{Model: "elevenlabs/scribe_v1", APIKeys: config.SimpleSecureStrings("sk_elevenlabs_test")},
					{
						ModelName: "groq",
						Model:     "groq/llama-3.3-70b",
						APIKeys:   config.SimpleSecureStrings("sk-groq-model"),
					},
				},
			},
			wantNil: true,
		},
		{
			name: "voice model name takes priority over elevenlabs",
			cfg: &config.Config{
				Voice: config.VoiceConfig{
					ModelName: "voice-gemini",
				},
				ModelList: []*config.ModelConfig{
					{Model: "elevenlabs", APIKeys: config.SimpleSecureStrings("sk_elevenlabs_test")},
					{
						ModelName: "voice-gemini",
						Model:     "gemini/gemini-2.5-flash",
						APIKeys:   config.SimpleSecureStrings("sk-gemini-model"),
					},
				},
			},
			wantName: "audio-model",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			prepareStrictVoiceTestConfig(tc.cfg)
			tr := DetectTranscriber(tc.cfg)
			if tc.wantNil {
				if tr != nil {
					t.Errorf("DetectTranscriber() = %v, want nil", tr)
				}
				return
			}
			if tr == nil {
				t.Fatal("DetectTranscriber() = nil, want non-nil")
			}
			if got := tr.Name(); got != tc.wantName {
				t.Errorf("Name() = %q, want %q", got, tc.wantName)
			}
		})
	}
}

func prepareStrictVoiceTestConfig(cfg *config.Config) {
	if cfg == nil || cfg.Voice.ModelName == "" || cfg.Voice.AccountRef != "" {
		return
	}
	for _, account := range cfg.ModelList {
		if account == nil || account.ModelName != cfg.Voice.ModelName {
			continue
		}
		account.Enabled = true
		cfg.Voice.AccountRef = account.ModelName
		cfg.ModelAliases = append(cfg.ModelAliases, config.ModelAliasConfig{
			Name:  cfg.Voice.ModelName,
			Model: account.Model,
		})
		return
	}
}

func TestDetectTranscriberWithErrorRejectsUnsupportedProvider(t *testing.T) {
	cfg := &config.Config{
		Voice: config.VoiceConfig{
			AccountRef: "anthropic-account",
			ModelName:  "asr",
		},
		ModelAliases: []config.ModelAliasConfig{{
			Name:  "asr",
			Model: "claude-sonnet-4-6",
		}},
		ModelList: []*config.ModelConfig{{
			ModelName: "anthropic-account",
			Provider:  "anthropic",
			APIKeys:   config.SimpleSecureStrings("sk-anthropic"),
			Enabled:   true,
		}},
	}

	transcriber, err := DetectTranscriberWithError(cfg)
	if transcriber != nil {
		t.Fatalf("DetectTranscriberWithError() = %T, want nil", transcriber)
	}
	if err == nil || !strings.Contains(err.Error(), `provider "anthropic"`) {
		t.Fatalf("DetectTranscriberWithError() error = %v, want unsupported anthropic provider", err)
	}
	if transcriber := DetectTranscriber(cfg); transcriber != nil {
		t.Fatalf("DetectTranscriber() = %T, want nil", transcriber)
	}
}
