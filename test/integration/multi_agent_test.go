package integration_test

import (
	"context"
	"testing"
	"time"

	"github.com/marcfargas/bifrost/pkg/protocol"
	"github.com/marcfargas/bifrost/test/testutil"
)

// TestThreeAgentConversation verifies that a reply in a conversation is
// delivered only to the conversation participants and not to uninvolved agents.
func TestThreeAgentConversation(t *testing.T) {
	ctx := context.Background()
	hub := testutil.TestHub(t)

	notifier := testutil.NewCollectingNotifier()
	hub.AddNotifier(notifier)

	frontend := testutil.TestAgent("frontend")
	backend := testutil.TestAgent("backend")
	mobile := testutil.TestAgent("mobile")

	for _, a := range []*protocol.Agent{frontend, backend, mobile} {
		if err := hub.Agents().Register(ctx, a); err != nil {
			t.Fatalf("register %s: %v", a.AgentID, err)
		}
	}

	// Frontend asks backend a question.
	question := &protocol.Message{
		From: frontend.AgentID,
		To:   backend.AgentID,
		Type: protocol.MessageTypeQuestion,
		Body: "How do you do?",
	}
	if err := hub.Messages().Send(ctx, question); err != nil {
		t.Fatalf("send question: %v", err)
	}

	// Backend replies in the same conversation.
	answer := &protocol.Message{
		From:           backend.AgentID,
		To:             frontend.AgentID,
		Type:           protocol.MessageTypeAnswer,
		Body:           "I am doing well.",
		ConversationID: question.ConversationID,
		InReplyTo:      question.ID,
	}
	if err := hub.Messages().Send(ctx, answer); err != nil {
		t.Fatalf("send answer: %v", err)
	}

	// Give the async SyncEngine a moment to deliver.
	time.Sleep(50 * time.Millisecond)

	// Frontend must have received the answer event.
	frontendEvents := notifier.NotificationsOfType(frontend.AgentID, "event.new")
	if len(frontendEvents) == 0 {
		t.Fatalf("frontend received 0 event.new notifications, want at least 1")
	}
	found := false
	for _, n := range frontendEvents {
		if ev, ok := n.Payload.(*protocol.Event); ok {
			if ev.Data.Body == answer.Body {
				found = true
				break
			}
		}
	}
	if !found {
		t.Errorf("frontend did not receive the answer event")
	}

	// Mobile must NOT have received any event from this conversation.
	mobileEvents := notifier.NotificationsOfType(mobile.AgentID, "event.new")
	for _, n := range mobileEvents {
		if ev, ok := n.Payload.(*protocol.Event); ok {
			if ev.Data.Body == question.Body || ev.Data.Body == answer.Body {
				t.Errorf("mobile unexpectedly received a conversation event: %q", ev.Data.Body)
			}
		}
	}
}

// TestBroadcastMessage registers three agents, has one broadcast with To="*",
// and verifies the other two received it while the sender did not.
func TestBroadcastMessage(t *testing.T) {
	ctx := context.Background()
	hub := testutil.TestHub(t)

	notifier := testutil.NewCollectingNotifier()
	hub.AddNotifier(notifier)

	alpha := testutil.TestAgent("alpha")
	beta := testutil.TestAgent("beta")
	gamma := testutil.TestAgent("gamma")

	for _, a := range []*protocol.Agent{alpha, beta, gamma} {
		if err := hub.Agents().Register(ctx, a); err != nil {
			t.Fatalf("register %s: %v", a.AgentID, err)
		}
	}

	broadcast := &protocol.Message{
		From: alpha.AgentID,
		To:   "*",
		Type: protocol.MessageTypeContext,
		Body: "Attention all agents: system maintenance at midnight.",
	}
	if err := hub.Messages().Send(ctx, broadcast); err != nil {
		t.Fatalf("send broadcast: %v", err)
	}

	// Broadcasts go through the direct notification path (not SyncEngine),
	// so check "message.new" notifications.
	// Beta and Gamma must each have received the broadcast.
	for _, agentID := range []string{beta.AgentID, gamma.AgentID} {
		msgs := notifier.MessagesFor(agentID)
		found := false
		for _, m := range msgs {
			if m.Body == broadcast.Body {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("agent %s did not receive the broadcast message", agentID)
		}
	}

	// Alpha (sender) must NOT have received its own broadcast.
	alphaMsgs := notifier.MessagesFor(alpha.AgentID)
	for _, m := range alphaMsgs {
		if m.Body == broadcast.Body {
			t.Errorf("sender alpha unexpectedly received its own broadcast")
		}
	}
}
