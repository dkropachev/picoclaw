package mqtt

import (
	"context"
	"testing"

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

type fakeMessage struct {
	topic   string
	payload []byte
}

func (message *fakeMessage) Duplicate() bool   { return false }
func (message *fakeMessage) Qos() byte         { return 1 }
func (message *fakeMessage) Retained() bool    { return false }
func (message *fakeMessage) Topic() string     { return message.topic }
func (message *fakeMessage) MessageID() uint16 { return 7 }
func (message *fakeMessage) Payload() []byte   { return message.payload }
func (message *fakeMessage) Ack()              {}

func TestHandleInboundUsesPayloadMessageIDForEventAdmission(t *testing.T) {
	messageBus := bus.NewMessageBus()
	defer messageBus.Close()
	recorder := &admissionRecorder{}
	messageBus.SetInboundAdmission(recorder)

	channel, err := NewMQTTChannel(
		&config.Channel{Enabled: true, Type: config.ChannelMQTT},
		&config.MQTTSettings{
			Broker:      "tcp://broker.example:1883",
			AgentID:     "agent",
			TopicPrefix: "/picoclaw",
		},
		messageBus,
	)
	if err != nil {
		t.Fatalf("NewMQTTChannel() error = %v", err)
	}

	message := &fakeMessage{
		topic:   "/picoclaw/agent/client/request",
		payload: []byte(`{"text":"hello","message_id":" request-42 "}`),
	}
	channel.handleInbound(message)

	if recorder.message.Context.MessageID != "request-42" {
		t.Fatalf("message_id = %q, want request-42", recorder.message.Context.MessageID)
	}
	if recorder.message.Context.ChatID != "mqtt:client" {
		t.Fatalf("chat_id = %q, want mqtt:client", recorder.message.Context.ChatID)
	}
}
