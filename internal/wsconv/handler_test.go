package wsconv

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"nhooyr.io/websocket"

	"github.com/gastownhall/tmux-adapter/internal/agents"
	"github.com/gastownhall/tmux-adapter/internal/conv"
)

func TestHandleBinaryUnsupportedType(t *testing.T) {
	_, ts := setupTestServer(t, "bin-agent")
	c := dialTestServer(t, ts)

	c.send(t, clientMessage{ID: "h", Type: "hello", Protocol: "tmux-converter.v1"})
	c.recv(t)

	// Build binary frame with unsupported type 0x02 (keyboard input, not handled by converter)
	frame := []byte{0x02}
	frame = append(frame, []byte("bin-agent")...)
	frame = append(frame, 0)
	frame = append(frame, []byte("payload")...)

	if err := c.conn.Write(c.ctx, websocket.MessageBinary, frame); err != nil {
		t.Fatalf("write: %v", err)
	}

	msg := c.recv(t)
	if msg.Type != "error" {
		t.Fatalf("type = %q, want error", msg.Type)
	}
	if msg.Error == "" {
		t.Fatal("expected error for unsupported binary type")
	}
}

func TestHandleBinaryInvalidFrame(t *testing.T) {
	_, ts := setupTestServer(t)
	c := dialTestServer(t, ts)

	c.send(t, clientMessage{ID: "h", Type: "hello", Protocol: "tmux-converter.v1"})
	c.recv(t)

	// Send too-short binary frame (fails ParseBinaryEnvelope)
	if err := c.conn.Write(c.ctx, websocket.MessageBinary, []byte{0x01}); err != nil {
		t.Fatalf("write: %v", err)
	}

	msg := c.recv(t)
	if msg.Type != "error" {
		t.Fatalf("type = %q, want error", msg.Type)
	}
	if msg.Error == "" {
		t.Fatal("expected error for invalid binary frame")
	}
}

func TestStreamLiveViaFollowAgent(t *testing.T) {
	// Follow agent with active conversation, verify live events via streamLiveWithContext.
	srv, ts := setupTestServer(t, "live-agent")

	dir := t.TempDir()
	convPath := filepath.Join(dir, "test.jsonl")
	jsonl := `{"type":"user","uuid":"u1","timestamp":"2026-02-14T01:44:54.253Z","message":{"role":"user","content":[{"type":"text","text":"initial"}]}}` + "\n"
	if err := os.WriteFile(convPath, []byte(jsonl), 0644); err != nil {
		t.Fatal(err)
	}

	convID := "claude:live-agent:test"
	disc := &testDiscoverer{
		files: []conv.ConversationFile{{
			Path:                 convPath,
			NativeConversationID: "test",
			ConversationID:       convID,
			Runtime:              "claude",
		}},
		watchDirs: []string{dir},
	}

	srv.watcher.RegisterRuntime("claude", disc, func(agentName, cID string) conv.Parser {
		return conv.NewClaudeParser(agentName, cID)
	})

	if err := srv.watcher.EnsureTailing("live-agent"); err != nil {
		t.Fatalf("EnsureTailing: %v", err)
	}

	waitForBuffer(t, srv.watcher, convID)

	c := dialTestServer(t, ts)
	c.send(t, clientMessage{ID: "h", Type: "hello", Protocol: "tmux-converter.v1"})
	c.recv(t)

	c.send(t, clientMessage{ID: "1", Type: "follow-agent", Agent: "live-agent"})
	resp := c.recv(t)
	if resp.Type != "follow-agent" || resp.OK == nil || !*resp.OK {
		t.Fatalf("follow failed: %+v", resp)
	}

	// Drain the snapshot chunks before checking for live events
	c.drainSnapshot(t)

	// Append live event to buffer
	buf := srv.watcher.GetBuffer(convID)
	buf.Append(conv.ConversationEvent{
		Type:           "assistant",
		ConversationID: convID,
		AgentName:      "live-agent",
		EventID:        "live-1",
	})

	msg := c.recv(t)
	if msg.Type != "conversation-event" {
		t.Fatalf("type = %q, want conversation-event", msg.Type)
	}
	if msg.Event == nil {
		t.Fatal("expected event")
	}
	if msg.Event.EventID != "live-1" {
		t.Fatalf("eventId = %q, want live-1", msg.Event.EventID)
	}
	if msg.SubscriptionID == "" {
		t.Fatal("expected subscriptionId")
	}
	if msg.Cursor == "" {
		t.Fatal("expected cursor")
	}
}

func TestStreamLiveViaSubscribeConversation(t *testing.T) {
	srv, ts := setupTestServer(t, "sc-agent")

	dir := t.TempDir()
	convPath := filepath.Join(dir, "test.jsonl")
	jsonl := `{"type":"user","uuid":"u1","timestamp":"2026-02-14T01:44:54.253Z","message":{"role":"user","content":[{"type":"text","text":"hello"}]}}` + "\n"
	if err := os.WriteFile(convPath, []byte(jsonl), 0644); err != nil {
		t.Fatal(err)
	}

	convID := "claude:sc-agent:test"
	disc := &testDiscoverer{
		files: []conv.ConversationFile{{
			Path:                 convPath,
			NativeConversationID: "test",
			ConversationID:       convID,
			Runtime:              "claude",
		}},
		watchDirs: []string{dir},
	}

	srv.watcher.RegisterRuntime("claude", disc, func(agentName, cID string) conv.Parser {
		return conv.NewClaudeParser(agentName, cID)
	})

	if err := srv.watcher.EnsureTailing("sc-agent"); err != nil {
		t.Fatalf("EnsureTailing: %v", err)
	}

	waitForBuffer(t, srv.watcher, convID)

	c := dialTestServer(t, ts)
	c.send(t, clientMessage{ID: "h", Type: "hello", Protocol: "tmux-converter.v1"})
	c.recv(t)

	c.send(t, clientMessage{ID: "1", Type: "subscribe-conversation", ConversationID: convID})
	resp := c.recv(t)
	if resp.Type != "conversation-snapshot" {
		t.Fatalf("type = %q, want conversation-snapshot", resp.Type)
	}

	// Drain snapshot chunks before checking for live events
	c.drainSnapshot(t)

	buf := srv.watcher.GetBuffer(convID)
	buf.Append(conv.ConversationEvent{
		Type:           "assistant",
		ConversationID: convID,
		AgentName:      "sc-agent",
		EventID:        "sc-live-1",
	})

	msg := c.recv(t)
	if msg.Type != "conversation-event" {
		t.Fatalf("type = %q, want conversation-event", msg.Type)
	}
	if msg.Event.EventID != "sc-live-1" {
		t.Fatalf("eventId = %q, want sc-live-1", msg.Event.EventID)
	}
}

func TestDeliverConversationEventNoLiveChannel(t *testing.T) {
	// Test deliverConversationEvent for subscriptions without a live channel.
	// A pending follow has no live channel; events go through Broadcast path.
	srv, ts := setupTestServer(t, "nolive-agent")

	c := dialTestServer(t, ts)
	c.send(t, clientMessage{ID: "h", Type: "hello", Protocol: "tmux-converter.v1"})
	c.recv(t)

	c.send(t, clientMessage{ID: "1", Type: "follow-agent", Agent: "nolive-agent"})
	resp := c.recv(t)
	if resp.Type != "follow-agent" || resp.OK == nil || !*resp.OK {
		t.Fatalf("follow failed: %+v", resp)
	}

	convID := "claude:nolive-agent:conv1"

	// Set conversationID on the subscription so broadcast matching works
	srv.mu.Lock()
	for client := range srv.clients {
		client.mu.Lock()
		for _, sub := range client.subs {
			if sub.agentName == "nolive-agent" {
				sub.conversationID = convID
			}
		}
		client.mu.Unlock()
	}
	srv.mu.Unlock()

	srv.Broadcast(conv.WatcherEvent{
		Type: "conversation-event",
		Event: &conv.ConversationEvent{
			ConversationID: convID,
			Type:           "assistant",
			EventID:        "direct-evt",
			AgentName:      "nolive-agent",
		},
	})

	msg := c.recv(t)
	if msg.Type != "conversation-event" {
		t.Fatalf("type = %q, want conversation-event", msg.Type)
	}
	if msg.Event.EventID != "direct-evt" {
		t.Fatalf("eventId = %q, want direct-evt", msg.Event.EventID)
	}
}

func TestDeliverConversationEventFilterMismatch(t *testing.T) {
	// deliverConversationEvent respects the subscription filter.
	srv, ts := setupTestServer(t, "filt-agent")

	c := dialTestServer(t, ts)
	c.send(t, clientMessage{ID: "h", Type: "hello", Protocol: "tmux-converter.v1"})
	c.recv(t)

	c.send(t, clientMessage{ID: "1", Type: "follow-agent", Agent: "filt-agent"})
	resp := c.recv(t)
	if resp.Type != "follow-agent" || resp.OK == nil || !*resp.OK {
		t.Fatalf("follow failed: %+v", resp)
	}

	convID := "claude:filt-agent:conv1"

	// Set a type filter ("user" only) and conversationID
	srv.mu.Lock()
	for client := range srv.clients {
		client.mu.Lock()
		for _, sub := range client.subs {
			if sub.agentName == "filt-agent" {
				sub.conversationID = convID
				sub.filter = conv.EventFilter{Types: map[string]bool{"user": true}}
			}
		}
		client.mu.Unlock()
	}
	srv.mu.Unlock()

	// Broadcast "assistant" event (should NOT match)
	srv.Broadcast(conv.WatcherEvent{
		Type: "conversation-event",
		Event: &conv.ConversationEvent{
			ConversationID: convID,
			Type:           "assistant",
			EventID:        "filtered-out",
		},
	})

	// Broadcast "user" event (should match)
	srv.Broadcast(conv.WatcherEvent{
		Type: "conversation-event",
		Event: &conv.ConversationEvent{
			ConversationID: convID,
			Type:           "user",
			EventID:        "allowed-in",
		},
	})

	msg := c.recv(t)
	if msg.Type != "conversation-event" {
		t.Fatalf("type = %q, want conversation-event", msg.Type)
	}
	if msg.Event.EventID != "allowed-in" {
		t.Fatalf("eventId = %q, want allowed-in", msg.Event.EventID)
	}
}

func TestDeliverConversationStartedNilAgent(t *testing.T) {
	srv, ts := setupTestServer(t)
	c := dialTestServer(t, ts)

	c.send(t, clientMessage{ID: "h", Type: "hello", Protocol: "tmux-converter.v1"})
	c.recv(t)

	srv.Broadcast(conv.WatcherEvent{
		Type:      "conversation-started",
		Agent:     nil,
		NewConvID: "test:x:y",
	})

	c.send(t, clientMessage{ID: "1", Type: "list-agents"})
	msg := c.recv(t)
	if msg.Type != "list-agents" {
		t.Fatalf("type = %q, want list-agents", msg.Type)
	}
}

func TestDeliverConversationStartedEmptyConvID(t *testing.T) {
	srv, ts := setupTestServer(t, "test-agent")
	c := dialTestServer(t, ts)

	c.send(t, clientMessage{ID: "h", Type: "hello", Protocol: "tmux-converter.v1"})
	c.recv(t)

	srv.Broadcast(conv.WatcherEvent{
		Type:      "conversation-started",
		Agent:     &agents.Agent{Name: "test-agent"},
		NewConvID: "",
	})

	c.send(t, clientMessage{ID: "1", Type: "list-agents"})
	msg := c.recv(t)
	if msg.Type != "list-agents" {
		t.Fatalf("type = %q, want list-agents", msg.Type)
	}
}

func TestDeliverConversationSwitchNilAgent(t *testing.T) {
	srv, ts := setupTestServer(t)
	c := dialTestServer(t, ts)

	c.send(t, clientMessage{ID: "h", Type: "hello", Protocol: "tmux-converter.v1"})
	c.recv(t)

	srv.Broadcast(conv.WatcherEvent{
		Type:      "conversation-switched",
		Agent:     nil,
		OldConvID: "old",
		NewConvID: "new",
	})

	c.send(t, clientMessage{ID: "1", Type: "list-agents"})
	msg := c.recv(t)
	if msg.Type != "list-agents" {
		t.Fatalf("type = %q, want list-agents", msg.Type)
	}
}

func TestDeliverConversationSwitchNoFollow(t *testing.T) {
	srv, ts := setupTestServer(t, "other-agent")
	c := dialTestServer(t, ts)

	c.send(t, clientMessage{ID: "h", Type: "hello", Protocol: "tmux-converter.v1"})
	c.recv(t)

	srv.Broadcast(conv.WatcherEvent{
		Type:      "conversation-switched",
		Agent:     &agents.Agent{Name: "other-agent", Runtime: "claude"},
		OldConvID: "claude:other-agent:old",
		NewConvID: "claude:other-agent:new",
	})

	c.send(t, clientMessage{ID: "1", Type: "list-agents"})
	msg := c.recv(t)
	if msg.Type != "list-agents" {
		t.Fatalf("type = %q, want list-agents", msg.Type)
	}
}

func TestFollowAgentRefollowWithConversation(t *testing.T) {
	// Re-follow agent with active conversation. Tests cleanup of old subscription
	// with live channel and buffer subscription.
	srv, ts := setupTestServer(t, "rf2-agent")

	dir := t.TempDir()
	convPath := filepath.Join(dir, "test.jsonl")
	jsonl := `{"type":"user","uuid":"u1","timestamp":"2026-02-14T01:44:54.253Z","message":{"role":"user","content":[{"type":"text","text":"refollow live"}]}}` + "\n"
	if err := os.WriteFile(convPath, []byte(jsonl), 0644); err != nil {
		t.Fatal(err)
	}

	convID := "claude:rf2-agent:test"
	disc := &testDiscoverer{
		files: []conv.ConversationFile{{
			Path:                 convPath,
			NativeConversationID: "test",
			ConversationID:       convID,
			Runtime:              "claude",
		}},
		watchDirs: []string{dir},
	}

	srv.watcher.RegisterRuntime("claude", disc, func(agentName, cID string) conv.Parser {
		return conv.NewClaudeParser(agentName, cID)
	})

	if err := srv.watcher.EnsureTailing("rf2-agent"); err != nil {
		t.Fatalf("EnsureTailing: %v", err)
	}

	waitForBuffer(t, srv.watcher, convID)

	c := dialTestServer(t, ts)
	c.send(t, clientMessage{ID: "h", Type: "hello", Protocol: "tmux-converter.v1"})
	c.recv(t)

	c.send(t, clientMessage{ID: "1", Type: "follow-agent", Agent: "rf2-agent"})
	resp := c.recv(t)
	if resp.Type != "follow-agent" || resp.OK == nil || !*resp.OK {
		t.Fatalf("first follow failed: %+v", resp)
	}
	if resp.ConversationID != convID {
		t.Fatalf("convID = %q, want %q", resp.ConversationID, convID)
	}

	// Drain snapshot chunks from first follow
	c.drainSnapshot(t)

	// Re-follow same agent (triggers cleanup of old sub with live channel)
	c.send(t, clientMessage{ID: "2", Type: "follow-agent", Agent: "rf2-agent"})
	resp = c.recv(t)
	if resp.Type != "follow-agent" || resp.OK == nil || !*resp.OK {
		t.Fatalf("re-follow failed: %+v", resp)
	}

	// Drain snapshot chunks from re-follow
	c.drainSnapshot(t)

	// Verify live streaming works on new subscription
	buf := srv.watcher.GetBuffer(convID)
	buf.Append(conv.ConversationEvent{
		Type:           "assistant",
		ConversationID: convID,
		AgentName:      "rf2-agent",
		EventID:        "rf-live",
	})

	msg := c.recv(t)
	if msg.Type != "conversation-event" {
		t.Fatalf("type = %q, want conversation-event", msg.Type)
	}
	if msg.Event.EventID != "rf-live" {
		t.Fatalf("eventId = %q, want rf-live", msg.Event.EventID)
	}
}

func TestUnsubscribeAgentWithActiveConversation(t *testing.T) {
	srv, ts := setupTestServer(t, "ua2-agent")

	dir := t.TempDir()
	convPath := filepath.Join(dir, "test.jsonl")
	jsonl := `{"type":"user","uuid":"u1","timestamp":"2026-02-14T01:44:54.253Z","message":{"role":"user","content":[{"type":"text","text":"unsub"}]}}` + "\n"
	if err := os.WriteFile(convPath, []byte(jsonl), 0644); err != nil {
		t.Fatal(err)
	}

	convID := "claude:ua2-agent:test"
	disc := &testDiscoverer{
		files: []conv.ConversationFile{{
			Path:                 convPath,
			NativeConversationID: "test",
			ConversationID:       convID,
			Runtime:              "claude",
		}},
		watchDirs: []string{dir},
	}

	srv.watcher.RegisterRuntime("claude", disc, func(agentName, cID string) conv.Parser {
		return conv.NewClaudeParser(agentName, cID)
	})

	if err := srv.watcher.EnsureTailing("ua2-agent"); err != nil {
		t.Fatalf("EnsureTailing: %v", err)
	}

	waitForBuffer(t, srv.watcher, convID)

	c := dialTestServer(t, ts)
	c.send(t, clientMessage{ID: "h", Type: "hello", Protocol: "tmux-converter.v1"})
	c.recv(t)

	c.send(t, clientMessage{ID: "1", Type: "follow-agent", Agent: "ua2-agent"})
	resp := c.recv(t)
	if resp.Type != "follow-agent" || resp.OK == nil || !*resp.OK {
		t.Fatalf("follow failed: %+v", resp)
	}

	c.send(t, clientMessage{ID: "2", Type: "unsubscribe-agent", Agent: "ua2-agent"})
	resp = c.recvAfterSnapshot(t)
	if resp.Type != "unsubscribe-agent" {
		t.Fatalf("type = %q, want unsubscribe-agent", resp.Type)
	}
	if resp.OK == nil || !*resp.OK {
		t.Fatalf("ok = %v, want true", resp.OK)
	}
}

func TestUnsubscribeWithActiveConversation(t *testing.T) {
	srv, ts := setupTestServer(t, "us2-agent")

	dir := t.TempDir()
	convPath := filepath.Join(dir, "test.jsonl")
	jsonl := `{"type":"user","uuid":"u1","timestamp":"2026-02-14T01:44:54.253Z","message":{"role":"user","content":[{"type":"text","text":"unsub id"}]}}` + "\n"
	if err := os.WriteFile(convPath, []byte(jsonl), 0644); err != nil {
		t.Fatal(err)
	}

	convID := "claude:us2-agent:test"
	disc := &testDiscoverer{
		files: []conv.ConversationFile{{
			Path:                 convPath,
			NativeConversationID: "test",
			ConversationID:       convID,
			Runtime:              "claude",
		}},
		watchDirs: []string{dir},
	}

	srv.watcher.RegisterRuntime("claude", disc, func(agentName, cID string) conv.Parser {
		return conv.NewClaudeParser(agentName, cID)
	})

	if err := srv.watcher.EnsureTailing("us2-agent"); err != nil {
		t.Fatalf("EnsureTailing: %v", err)
	}

	waitForBuffer(t, srv.watcher, convID)

	c := dialTestServer(t, ts)
	c.send(t, clientMessage{ID: "h", Type: "hello", Protocol: "tmux-converter.v1"})
	c.recv(t)

	c.send(t, clientMessage{ID: "1", Type: "follow-agent", Agent: "us2-agent"})
	resp := c.recv(t)
	if resp.Type != "follow-agent" || resp.OK == nil || !*resp.OK {
		t.Fatalf("follow failed: %+v", resp)
	}

	c.send(t, clientMessage{ID: "2", Type: "unsubscribe", SubscriptionID: resp.SubscriptionID})
	resp = c.recvAfterSnapshot(t)
	if resp.Type != "unsubscribe" {
		t.Fatalf("type = %q, want unsubscribe", resp.Type)
	}
	if resp.OK == nil || !*resp.OK {
		t.Fatalf("ok = %v, want true", resp.OK)
	}
}

func TestDeliverConversationSwitchNoNewBuffer(t *testing.T) {
	// Conversation-switched where new buffer doesn't exist.
	// The switch message is sent but no snapshot follows.
	srv, ts := setupTestServer(t, "nobufsw-agent")

	dir := t.TempDir()
	convPath := filepath.Join(dir, "test.jsonl")
	jsonl := `{"type":"user","uuid":"u1","timestamp":"2026-02-14T01:44:54.253Z","message":{"role":"user","content":[{"type":"text","text":"old conv"}]}}` + "\n"
	if err := os.WriteFile(convPath, []byte(jsonl), 0644); err != nil {
		t.Fatal(err)
	}

	oldConvID := "claude:nobufsw-agent:old"
	disc := &testDiscoverer{
		files: []conv.ConversationFile{{
			Path:                 convPath,
			NativeConversationID: "old",
			ConversationID:       oldConvID,
			Runtime:              "claude",
		}},
		watchDirs: []string{dir},
	}

	srv.watcher.RegisterRuntime("claude", disc, func(agentName, cID string) conv.Parser {
		return conv.NewClaudeParser(agentName, cID)
	})

	if err := srv.watcher.EnsureTailing("nobufsw-agent"); err != nil {
		t.Fatalf("EnsureTailing: %v", err)
	}

	waitForBuffer(t, srv.watcher, oldConvID)

	c := dialTestServer(t, ts)
	c.send(t, clientMessage{ID: "h", Type: "hello", Protocol: "tmux-converter.v1"})
	c.recv(t)

	c.send(t, clientMessage{ID: "1", Type: "follow-agent", Agent: "nobufsw-agent"})
	resp := c.recv(t)
	if resp.Type != "follow-agent" || resp.OK == nil || !*resp.OK {
		t.Fatalf("follow failed: %+v", resp)
	}

	newConvID := "claude:nobufsw-agent:new"

	// Switch to a conversation that has no buffer
	srv.Broadcast(conv.WatcherEvent{
		Type:      "conversation-switched",
		Agent:     &agents.Agent{Name: "nobufsw-agent", Runtime: "claude"},
		OldConvID: oldConvID,
		NewConvID: newConvID,
	})

	// Drain snapshot chunks from the initial follow, then get the switch message
	msg := c.recvAfterSnapshot(t)
	if msg.Type != "conversation-switched" {
		t.Fatalf("type = %q, want conversation-switched", msg.Type)
	}

	// No snapshot follows — verify with a fence
	c.send(t, clientMessage{ID: "fence", Type: "list-agents"})
	msg = c.recv(t)
	if msg.Type != "list-agents" {
		t.Fatalf("type = %q, want list-agents (no snapshot for missing buffer)", msg.Type)
	}
}

// waitForBuffer polls until the buffer for convID exists and has events.
func waitForBuffer(t *testing.T, watcher *conv.ConversationWatcher, convID string) {
	t.Helper()
	deadline := time.After(5 * time.Second)
	for {
		buf := watcher.GetBuffer(convID)
		if buf != nil && len(buf.Snapshot(conv.EventFilter{})) > 0 {
			return
		}
		select {
		case <-deadline:
			t.Fatalf("timeout waiting for buffer %q", convID)
		case <-time.After(50 * time.Millisecond):
		}
	}
}
