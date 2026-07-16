package tokenizer

import (
	"testing"

	"github.com/sipeed/picoclaw/pkg/providers"
	"github.com/sipeed/picoclaw/pkg/providers/promptir"
)

func TestEstimateMessageTokens_DoesNotDoubleCountContentAndPartsText(t *testing.T) {
	plain := EstimateMessageTokens(providers.Message{
		Role:    "user",
		Content: "hello",
	})
	structured := EstimateMessageTokens(providers.Message{
		Role:    "user",
		Content: "hello",
		Parts: []providers.PromptPart{
			{Type: string(promptir.PartTypeText), Text: "hello"},
		},
	})

	if structured != plain {
		t.Fatalf("structured estimate = %d, want plain estimate %d", structured, plain)
	}
}

func TestEstimateMessageTokens_CountsStructuredImagePart(t *testing.T) {
	plain := EstimateMessageTokens(providers.Message{
		Role:    "user",
		Content: "hello",
	})
	withImage := EstimateMessageTokens(providers.Message{
		Role:    "user",
		Content: "hello",
		Parts: []providers.PromptPart{
			{Type: string(promptir.PartTypeText), Text: "hello"},
			{Type: string(promptir.PartTypeImage), URI: "data:image/png;base64,abc", MIMEType: "image/png"},
		},
	})

	if withImage <= plain {
		t.Fatalf("image estimate = %d, want > plain estimate %d", withImage, plain)
	}
}
