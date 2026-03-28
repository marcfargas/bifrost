package protocol_test

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/marcfargas/bifrost/pkg/protocol"
)

func TestEventDataRoundtrip(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	ev := &protocol.Event{
		ID:             "ev001",
		ConversationID: "conv001",
		Type:           protocol.EventTypeMessage,
		FromAgent:      "agent-a",
		Data: protocol.EventData{
			Body:        "hello",
			MessageType: protocol.MessageTypeAnswer,
			Priority:    protocol.PriorityNormal,
		},
		Timestamp: now,
	}

	b, err := json.Marshal(ev)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var got protocol.Event
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.ID != ev.ID {
		t.Errorf("ID: want %q got %q", ev.ID, got.ID)
	}
	if got.Data.Body != "hello" {
		t.Errorf("Body: want hello got %q", got.Data.Body)
	}
}

func TestFileEventSizeConstant(t *testing.T) {
	if protocol.MaxFileSizeBytes != 256*1024 {
		t.Errorf("MaxFileSizeBytes: want 262144 got %d", protocol.MaxFileSizeBytes)
	}
}
