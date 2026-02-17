package wsconv

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"nhooyr.io/websocket"

	"github.com/gastownhall/tmux-adapter/internal/conv"
)

func recvQueuedServerMessage(t *testing.T, ch <-chan outMsg) serverMessage {
	t.Helper()

	select {
	case msg := <-ch:
		var sm serverMessage
		if err := json.Unmarshal(msg.data, &sm); err != nil {
			t.Fatalf("unmarshal queued message: %v", err)
		}
		return sm
	case <-time.After(time.Second):
		t.Fatal("timeout waiting for queued message")
		return serverMessage{}
	}
}

func TestStreamSubscriptionCriticalQueueWhenNormalQueueFull(t *testing.T) {
	c := &Client{
		send:         make(chan outMsg, 1),
		sendCritical: make(chan outMsg, 8),
	}

	// Saturate the normal queue. Snapshot traffic must still flow via sendCritical.
	c.send <- outMsg{typ: websocket.MessageText, data: []byte(`{"type":"filler"}`)}

	ctx, cancel := context.WithCancel(context.Background())
	live := make(chan conv.ConversationEvent)
	done := make(chan struct{})

	snapshot := []conv.ConversationEvent{
		{
			Type:           "user",
			ConversationID: "conv-1",
			AgentName:      "agent-1",
			EventID:        "evt-1",
		},
	}

	go func() {
		c.streamSubscription("sub-1", "conv-1", snapshot, live, nil, true, ctx)
		close(done)
	}()

	msg := recvQueuedServerMessage(t, c.sendCritical)
	if msg.Type != "conversation-snapshot-chunk" {
		t.Fatalf("type = %q, want conversation-snapshot-chunk", msg.Type)
	}
	if msg.SubscriptionID != "sub-1" {
		t.Fatalf("subscriptionId = %q, want sub-1", msg.SubscriptionID)
	}
	if len(msg.Events) != 1 || msg.Events[0].EventID != "evt-1" {
		t.Fatalf("events = %+v, want single evt-1 event", msg.Events)
	}

	msg = recvQueuedServerMessage(t, c.sendCritical)
	if msg.Type != "conversation-snapshot-end" {
		t.Fatalf("type = %q, want conversation-snapshot-end", msg.Type)
	}
	if msg.SubscriptionID != "sub-1" {
		t.Fatalf("subscriptionId = %q, want sub-1", msg.SubscriptionID)
	}

	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("streamSubscription did not exit after context cancel")
	}
}

func TestStreamSubscriptionEndWithEmptySnapshotWhenNormalQueueFull(t *testing.T) {
	c := &Client{
		send:         make(chan outMsg, 1),
		sendCritical: make(chan outMsg, 4),
	}

	// Saturate normal queue. With no snapshot events, end marker is the only signal
	// that unblocks client-side loading state.
	c.send <- outMsg{typ: websocket.MessageText, data: []byte(`{"type":"filler"}`)}

	ctx, cancel := context.WithCancel(context.Background())
	live := make(chan conv.ConversationEvent)
	done := make(chan struct{})

	go func() {
		c.streamSubscription("sub-2", "conv-2", nil, live, nil, true, ctx)
		close(done)
	}()

	msg := recvQueuedServerMessage(t, c.sendCritical)
	if msg.Type != "conversation-snapshot-end" {
		t.Fatalf("type = %q, want conversation-snapshot-end", msg.Type)
	}
	if msg.SubscriptionID != "sub-2" {
		t.Fatalf("subscriptionId = %q, want sub-2", msg.SubscriptionID)
	}

	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("streamSubscription did not exit after context cancel")
	}
}
