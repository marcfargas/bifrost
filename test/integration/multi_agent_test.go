package integration_test

import (
	"context"
	"testing"

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

	// Frontend must have received the answer.
	frontendMsgs := notifier.MessagesFor(frontend.AgentID)
	if len(frontendMsgs) == 0 {
		t.Fatalf("frontend received 0 messages, want at least 1")
	}
	found := false
	for _, m := range frontendMsgs {
		if m.Body == answer.Body {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("frontend did not receive the answer message")
	}

	// Mobile must NOT have received any message from this conversation.
	mobileMsgs := notifier.MessagesFor(mobile.AgentID)
	for _, m := range mobileMsgs {
		if m.Body == question.Body || m.Body == answer.Body {
			t.Errorf("mobile unexpectedly received a conversation message: %q", m.Body)
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
