package maixcam

import (
	"context"
	"testing"
	"time"

	"github.com/sipeed/picoclaw/pkg/bus"
	"github.com/sipeed/picoclaw/pkg/config"
)

type admissionRecorder struct {
	message bus.InboundMessage
}

func (recorder *admissionRecorder) AdmitInbound(
	_ context.Context,
	message bus.InboundMessage,
) (bool, error) {
	recorder.message = message
	return false, nil
}

func TestPersonDetectionUsesProtocolIdentityForEventAdmission(t *testing.T) {
	messageBus := bus.NewMessageBus()
	defer messageBus.Close()
	recorder := &admissionRecorder{}
	messageBus.SetInboundAdmission(recorder)

	channel, err := NewMaixCamChannel(
		&config.Channel{Enabled: true, Type: config.ChannelMaixCam},
		&config.MaixCamSettings{},
		messageBus,
	)
	if err != nil {
		t.Fatalf("NewMaixCamChannel() error = %v", err)
	}
	channel.ctx = context.Background()
	channel.handlePersonDetection(MaixCamMessage{
		MessageID: " detection-42 ",
		Type:      "person_detected",
		Timestamp: 1_700_000_000.25,
		Data: map[string]any{
			"class_name": "person",
			"score":      0.99,
		},
	})

	if recorder.message.Context.MessageID != "detection-42" {
		t.Fatalf("message_id = %q, want detection-42", recorder.message.Context.MessageID)
	}
	wantOccurredAt := time.Unix(1_700_000_000, 250_000_000).UTC()
	if recorder.message.OccurredAt == nil ||
		!recorder.message.OccurredAt.Equal(wantOccurredAt) {
		t.Fatalf("occurred_at = %v, want %v", recorder.message.OccurredAt, wantOccurredAt)
	}
}
