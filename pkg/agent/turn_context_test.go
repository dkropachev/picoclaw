package agent

import (
	"testing"
	"time"

	"github.com/sipeed/picoclaw/pkg/bus"
)

func TestCloneInboundContextDetachesEventMetadata(t *testing.T) {
	t.Parallel()

	occurredAt := time.Date(2026, time.July, 29, 12, 34, 56, 789, time.UTC)
	original := &bus.InboundContext{
		ReplyHandles: map[string]string{"thread": "original"},
		Raw:          map[string]string{"transport": "original"},
		OccurredAt:   &occurredAt,
		Attachments: []bus.InboundAttachment{{
			Filename:    "original.txt",
			ContentType: "text/plain",
			Kind:        "document",
			SizeBytes:   42,
		}},
	}

	cloned := cloneInboundContext(original)
	if cloned == nil {
		t.Fatal("cloneInboundContext returned nil")
	}
	if cloned.OccurredAt == original.OccurredAt {
		t.Fatal("OccurredAt pointer was not detached")
	}
	if &cloned.Attachments[0] == &original.Attachments[0] {
		t.Fatal("Attachments backing storage was not detached")
	}

	cloned.ReplyHandles["thread"] = "clone"
	cloned.Raw["transport"] = "clone"
	*cloned.OccurredAt = cloned.OccurredAt.Add(time.Hour)
	cloned.Attachments[0].Filename = "clone.txt"

	if got := original.ReplyHandles["thread"]; got != "original" {
		t.Fatalf("original reply handle = %q, want original", got)
	}
	if got := original.Raw["transport"]; got != "original" {
		t.Fatalf("original raw value = %q, want original", got)
	}
	if got := *original.OccurredAt; !got.Equal(occurredAt) {
		t.Fatalf("original OccurredAt = %v, want %v", got, occurredAt)
	}
	if got := original.Attachments[0].Filename; got != "original.txt" {
		t.Fatalf("original attachment filename = %q, want original.txt", got)
	}
}
