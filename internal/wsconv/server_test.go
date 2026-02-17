package wsconv

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"nhooyr.io/websocket"

	"github.com/gastownhall/tmux-adapter/internal/agents"
	"github.com/gastownhall/tmux-adapter/internal/conv"
)

func TestHelloHandshake(t *testing.T) {
	_, ts := setupTestServer(t)
	c := dialTestServer(t, ts)

	c.send(t, clientMessage{ID: "1", Type: "hello", Protocol: "tmux-converter.v1"})
	resp := c.recv(t)

	if resp.Type != "hello" {
		t.Fatalf("type = %q, want hello", resp.Type)
	}
	if resp.OK == nil || !*resp.OK {
		t.Fatalf("ok = %v, want true", resp.OK)
	}
	if resp.Protocol != "tmux-converter.v1" {
		t.Fatalf("protocol = %q, want tmux-converter.v1", resp.Protocol)
	}
	if resp.ID != "1" {
		t.Fatalf("id = %q, want 1", resp.ID)
	}
}

func TestHelloWrongProtocol(t *testing.T) {
	_, ts := setupTestServer(t)
	c := dialTestServer(t, ts)

	c.send(t, clientMessage{ID: "1", Type: "hello", Protocol: "wrong.v99"})
	resp := c.recv(t)

	if resp.Type != "hello" {
		t.Fatalf("type = %q, want hello", resp.Type)
	}
	if resp.OK == nil || *resp.OK {
		t.Fatalf("ok = %v, want false", resp.OK)
	}
	if resp.Error == "" {
		t.Fatal("expected error message")
	}
}

func TestMessageBeforeHello(t *testing.T) {
	_, ts := setupTestServer(t)
	c := dialTestServer(t, ts)

	// Sending a non-hello message before handshake should be rejected
	c.send(t, clientMessage{ID: "1", Type: "list-agents"})
	resp := c.recv(t)

	if resp.Type != "error" {
		t.Fatalf("type = %q, want error", resp.Type)
	}
	if resp.Error == "" {
		t.Fatal("expected error message about handshake")
	}
}

func TestListAgentsEmpty(t *testing.T) {
	_, ts := setupTestServer(t) // no agents
	c := dialTestServer(t, ts)

	// Handshake first
	c.send(t, clientMessage{ID: "h", Type: "hello", Protocol: "tmux-converter.v1"})
	c.recv(t) // consume hello response

	c.send(t, clientMessage{ID: "1", Type: "list-agents"})
	resp := c.recv(t)

	if resp.Type != "list-agents" {
		t.Fatalf("type = %q, want list-agents", resp.Type)
	}
	if len(resp.Agents) != 0 {
		t.Fatalf("agents len = %d, want 0", len(resp.Agents))
	}
}

func TestListAgentsWithAgents(t *testing.T) {
	_, ts := setupTestServer(t, "agent-alpha", "agent-beta")
	c := dialTestServer(t, ts)

	c.send(t, clientMessage{ID: "h", Type: "hello", Protocol: "tmux-converter.v1"})
	c.recv(t)

	c.send(t, clientMessage{ID: "1", Type: "list-agents"})
	resp := c.recv(t)

	if resp.Type != "list-agents" {
		t.Fatalf("type = %q, want list-agents", resp.Type)
	}
	if len(resp.Agents) != 2 {
		t.Fatalf("agents len = %d, want 2", len(resp.Agents))
	}

	// Verify agent names are present (order may vary)
	names := map[string]bool{}
	for _, a := range resp.Agents {
		names[a.Name] = true
	}
	if !names["agent-alpha"] || !names["agent-beta"] {
		t.Fatalf("expected agent-alpha and agent-beta, got %v", names)
	}
}

func TestSubscribeAgentsWithFilter(t *testing.T) {
	_, ts := setupTestServer(t, "alpha-prod", "beta-dev", "gamma-prod")
	c := dialTestServer(t, ts)

	c.send(t, clientMessage{ID: "h", Type: "hello", Protocol: "tmux-converter.v1"})
	c.recv(t)

	// Subscribe with include filter matching only "*-prod" agents
	c.send(t, clientMessage{
		ID:                   "1",
		Type:                 "subscribe-agents",
		IncludeSessionFilter: ".*-prod$",
	})
	resp := c.recv(t)

	if resp.Type != "subscribe-agents" {
		t.Fatalf("type = %q, want subscribe-agents", resp.Type)
	}
	if resp.OK == nil || !*resp.OK {
		t.Fatalf("ok = %v, want true", resp.OK)
	}
	if len(resp.Agents) != 2 {
		t.Fatalf("filtered agents len = %d, want 2 (alpha-prod, gamma-prod)", len(resp.Agents))
	}
	// Total should reflect all agents, not just filtered
	if resp.TotalAgents == nil || *resp.TotalAgents != 3 {
		t.Fatalf("totalAgents = %v, want 3", resp.TotalAgents)
	}
}

func TestSubscribeConversationSnapshot(t *testing.T) {
	_, ts := setupTestServer(t, "test-agent")
	c := dialTestServer(t, ts)

	c.send(t, clientMessage{ID: "h", Type: "hello", Protocol: "tmux-converter.v1"})
	c.recv(t)

	// Set up a conversation by writing a JSONL file and registering a discoverer
	dir := t.TempDir()
	convPath := filepath.Join(dir, "test.jsonl")
	if err := os.WriteFile(convPath, []byte(
		`{"type":"user","uuid":"u1","timestamp":"2026-02-14T01:44:54.253Z","message":{"role":"user","content":[{"type":"text","text":"hello"}]}}`+"\n",
	), 0644); err != nil {
		t.Fatal(err)
	}

	// We need to register a discoverer on the watcher and trigger EnsureTailing.
	// Access the server's watcher through the test server setup — since we're in
	// the same package, we can access server internals.
	// Unfortunately the watcher is on the test server's Server struct which we
	// can retrieve from setupTestServer. Let's use a different approach:
	// create a buffer directly in the watcher's streams map.
	//
	// For a clean integration test, we'll test subscribe-conversation through
	// the follow-agent path which handles the "no buffer yet" case gracefully.

	// Instead, test the error case when conversation doesn't exist
	c.send(t, clientMessage{
		ID:             "1",
		Type:           "subscribe-conversation",
		ConversationID: "claude:nonexistent:fake",
	})
	resp := c.recv(t)

	if resp.Type != "error" {
		t.Fatalf("type = %q, want error", resp.Type)
	}
}

func TestFollowAgentNoConversation(t *testing.T) {
	_, ts := setupTestServer(t, "test-agent")
	c := dialTestServer(t, ts)

	c.send(t, clientMessage{ID: "h", Type: "hello", Protocol: "tmux-converter.v1"})
	c.recv(t)

	// Follow an agent that has no active conversation — should succeed
	// with pending follow (no conversation data yet)
	c.send(t, clientMessage{ID: "1", Type: "follow-agent", Agent: "test-agent"})
	resp := c.recv(t)

	if resp.Type != "follow-agent" {
		t.Fatalf("type = %q, want follow-agent", resp.Type)
	}
	if resp.OK == nil || !*resp.OK {
		t.Fatalf("ok = %v, want true", resp.OK)
	}
	if resp.SubscriptionID == "" {
		t.Fatal("expected non-empty subscriptionId")
	}
	// No conversation data since agent has no active conversation
	if len(resp.Events) != 0 {
		t.Fatalf("events len = %d, want 0 (no conversation)", len(resp.Events))
	}
}

func TestFollowAgentWithConversation(t *testing.T) {
	// Create a server where the watcher has a pre-populated conversation buffer.
	// We need to inject a buffer manually since we can't easily set up the full
	// discovery pipeline in a unit test.
	srv, ts := setupTestServer(t, "conv-agent")

	// Inject a conversation buffer directly into the watcher's internal state.
	// Since we're in the same package (wsconv), we access the server's watcher.
	// We need to manipulate the watcher's internal maps — but those are in the
	// conv package (unexported). Instead, we'll use a different strategy:
	// Directly build a ConversationBuffer, populate it, then use the watcher's
	// exported EnsureTailing path.

	// Actually, the simplest way: create a buffer and wire it through the
	// watcher's exported API via RegisterRuntime + EnsureTailing.
	dir := t.TempDir()
	convPath := filepath.Join(dir, "test.jsonl")
	jsonl := `{"type":"user","uuid":"u1","timestamp":"2026-02-14T01:44:54.253Z","message":{"role":"user","content":[{"type":"text","text":"hello world"}]}}` + "\n"
	if err := os.WriteFile(convPath, []byte(jsonl), 0644); err != nil {
		t.Fatal(err)
	}

	convID := "claude:conv-agent:test"
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

	// EnsureTailing triggers discovery and buffer creation
	if err := srv.watcher.EnsureTailing("conv-agent"); err != nil {
		t.Fatalf("EnsureTailing: %v", err)
	}

	// Poll until buffer is created (async operation)
	deadline := time.After(5 * time.Second)
	for srv.watcher.GetBuffer(convID) == nil {
		select {
		case <-deadline:
			t.Fatal("timeout waiting for conversation buffer")
		case <-time.After(50 * time.Millisecond):
		}
	}

	// Wait for the tailer to process the file
	deadline = time.After(5 * time.Second)
	for {
		buf := srv.watcher.GetBuffer(convID)
		if buf != nil {
			snap := buf.Snapshot(conv.EventFilter{})
			if len(snap) > 0 {
				break
			}
		}
		select {
		case <-deadline:
			t.Fatal("timeout waiting for events in buffer")
		case <-time.After(50 * time.Millisecond):
		}
	}

	c := dialTestServer(t, ts)

	c.send(t, clientMessage{ID: "h", Type: "hello", Protocol: "tmux-converter.v1"})
	c.recv(t)

	c.send(t, clientMessage{ID: "1", Type: "follow-agent", Agent: "conv-agent"})
	resp := c.recv(t)

	if resp.Type != "follow-agent" {
		t.Fatalf("type = %q, want follow-agent", resp.Type)
	}
	if resp.OK == nil || !*resp.OK {
		t.Fatalf("ok = %v, want true", resp.OK)
	}
	if resp.ConversationID != convID {
		t.Fatalf("conversationId = %q, want %q", resp.ConversationID, convID)
	}

	// Drain chunked snapshot
	events := c.drainSnapshot(t)
	if len(events) != 1 {
		t.Fatalf("events len = %d, want 1", len(events))
	}
	if events[0].Type != "user" {
		t.Fatalf("event type = %q, want user", events[0].Type)
	}
}

func TestUnsubscribeAgent(t *testing.T) {
	_, ts := setupTestServer(t, "test-agent")
	c := dialTestServer(t, ts)

	c.send(t, clientMessage{ID: "h", Type: "hello", Protocol: "tmux-converter.v1"})
	c.recv(t)

	// Follow agent first
	c.send(t, clientMessage{ID: "1", Type: "follow-agent", Agent: "test-agent"})
	resp := c.recv(t)
	if resp.Type != "follow-agent" || resp.OK == nil || !*resp.OK {
		t.Fatalf("follow-agent failed: %+v", resp)
	}

	// Unsubscribe
	c.send(t, clientMessage{ID: "2", Type: "unsubscribe-agent", Agent: "test-agent"})
	resp = c.recv(t)

	if resp.Type != "unsubscribe-agent" {
		t.Fatalf("type = %q, want unsubscribe-agent", resp.Type)
	}
	if resp.OK == nil || !*resp.OK {
		t.Fatalf("ok = %v, want true", resp.OK)
	}
}

// testDiscoverer returns pre-configured discovery results for testing.
type testDiscoverer struct {
	files     []conv.ConversationFile
	watchDirs []string
}

func (d *testDiscoverer) FindConversations(_, _ string) (conv.DiscoveryResult, error) {
	return conv.DiscoveryResult{
		Files:     d.files,
		WatchDirs: d.watchDirs,
	}, nil
}

// blockingDiscoverer blocks on FindConversations until ready is closed.
// This lets tests guarantee the pending-follow path (no race with discovery).
type blockingDiscoverer struct {
	files     []conv.ConversationFile
	watchDirs []string
	ready     chan struct{}
}

func (d *blockingDiscoverer) FindConversations(_, _ string) (conv.DiscoveryResult, error) {
	<-d.ready
	return conv.DiscoveryResult{
		Files:     d.files,
		WatchDirs: d.watchDirs,
	}, nil
}

// switchDiscoverer returns firstFiles on the first call, secondFiles on subsequent calls.
// Simulates conversation rotation for testing conversation-switched delivery.
type switchDiscoverer struct {
	firstFiles  []conv.ConversationFile
	secondFiles []conv.ConversationFile
	watchDirs   []string
	calls       int
	mu          sync.Mutex
}

func (d *switchDiscoverer) FindConversations(_, _ string) (conv.DiscoveryResult, error) {
	d.mu.Lock()
	d.calls++
	files := d.firstFiles
	if d.calls > 1 {
		files = d.secondFiles
	}
	d.mu.Unlock()
	return conv.DiscoveryResult{
		Files:     files,
		WatchDirs: d.watchDirs,
	}, nil
}

func TestListConversations(t *testing.T) {
	_, ts := setupTestServer(t)
	c := dialTestServer(t, ts)

	c.send(t, clientMessage{ID: "h", Type: "hello", Protocol: "tmux-converter.v1"})
	c.recv(t)

	c.send(t, clientMessage{ID: "1", Type: "list-conversations"})
	resp := c.recv(t)

	if resp.Type != "list-conversations" {
		t.Fatalf("type = %q, want list-conversations", resp.Type)
	}
	if resp.ID != "1" {
		t.Fatalf("id = %q, want 1", resp.ID)
	}
}

func TestUnsubscribe(t *testing.T) {
	_, ts := setupTestServer(t, "test-agent")
	c := dialTestServer(t, ts)

	c.send(t, clientMessage{ID: "h", Type: "hello", Protocol: "tmux-converter.v1"})
	c.recv(t)

	// Follow agent to get a subscription ID
	c.send(t, clientMessage{ID: "1", Type: "follow-agent", Agent: "test-agent"})
	resp := c.recv(t)
	if resp.Type != "follow-agent" || resp.OK == nil || !*resp.OK {
		t.Fatalf("follow failed: %+v", resp)
	}
	subID := resp.SubscriptionID

	// Unsubscribe by subscription ID
	c.send(t, clientMessage{ID: "2", Type: "unsubscribe", SubscriptionID: subID})
	resp = c.recv(t)

	if resp.Type != "unsubscribe" {
		t.Fatalf("type = %q, want unsubscribe", resp.Type)
	}
	if resp.OK == nil || !*resp.OK {
		t.Fatalf("ok = %v, want true", resp.OK)
	}
}

func TestBroadcastAgentAdded(t *testing.T) {
	srv, ts := setupTestServer(t, "existing-agent")
	c := dialTestServer(t, ts)

	c.send(t, clientMessage{ID: "h", Type: "hello", Protocol: "tmux-converter.v1"})
	c.recv(t)

	c.send(t, clientMessage{ID: "1", Type: "subscribe-agents"})
	resp := c.recv(t)
	if resp.Type != "subscribe-agents" || resp.OK == nil || !*resp.OK {
		t.Fatalf("subscribe-agents failed: %+v", resp)
	}

	srv.Broadcast(conv.WatcherEvent{
		Type:  "agent-added",
		Agent: &agents.Agent{Name: "new-agent", Runtime: "claude"},
	})

	// Should receive agents-count first
	msg := c.recv(t)
	if msg.Type != "agents-count" {
		t.Fatalf("type = %q, want agents-count", msg.Type)
	}
	if msg.TotalAgents == nil {
		t.Fatal("expected totalAgents to be set")
	}

	// Then agent-added
	msg = c.recv(t)
	if msg.Type != "agent-added" {
		t.Fatalf("type = %q, want agent-added", msg.Type)
	}
	if msg.Agent == nil {
		t.Fatal("expected agent to be set")
	}
}

func TestBroadcastAgentRemoved(t *testing.T) {
	srv, ts := setupTestServer(t, "existing-agent")
	c := dialTestServer(t, ts)

	c.send(t, clientMessage{ID: "h", Type: "hello", Protocol: "tmux-converter.v1"})
	c.recv(t)

	c.send(t, clientMessage{ID: "1", Type: "subscribe-agents"})
	c.recv(t)

	srv.Broadcast(conv.WatcherEvent{
		Type:  "agent-removed",
		Agent: &agents.Agent{Name: "existing-agent", Runtime: "claude"},
	})

	msg := c.recv(t)
	if msg.Type != "agents-count" {
		t.Fatalf("type = %q, want agents-count", msg.Type)
	}

	msg = c.recv(t)
	if msg.Type != "agent-removed" {
		t.Fatalf("type = %q, want agent-removed", msg.Type)
	}
	if msg.Name != "existing-agent" {
		t.Fatalf("name = %q, want existing-agent", msg.Name)
	}
}

func TestBroadcastAgentUpdated(t *testing.T) {
	srv, ts := setupTestServer(t, "existing-agent")
	c := dialTestServer(t, ts)

	c.send(t, clientMessage{ID: "h", Type: "hello", Protocol: "tmux-converter.v1"})
	c.recv(t)

	c.send(t, clientMessage{ID: "1", Type: "subscribe-agents"})
	c.recv(t)

	srv.Broadcast(conv.WatcherEvent{
		Type:  "agent-updated",
		Agent: &agents.Agent{Name: "existing-agent", Runtime: "claude", Attached: true},
	})

	// agent-updated should NOT send agents-count — the lifecycle msg comes first
	msg := c.recv(t)
	if msg.Type != "agent-updated" {
		t.Fatalf("type = %q, want agent-updated (no agents-count for updates)", msg.Type)
	}
	if msg.Agent == nil {
		t.Fatal("expected agent to be set")
	}
}

func TestBroadcastWithFilter(t *testing.T) {
	srv, ts := setupTestServer(t, "alpha-prod", "beta-dev")
	c := dialTestServer(t, ts)

	c.send(t, clientMessage{ID: "h", Type: "hello", Protocol: "tmux-converter.v1"})
	c.recv(t)

	// Subscribe with filter matching only *-prod agents
	c.send(t, clientMessage{
		ID:                   "1",
		Type:                 "subscribe-agents",
		IncludeSessionFilter: ".*-prod$",
	})
	c.recv(t)

	// Broadcast matching agent — should get agents-count + agent-added
	srv.Broadcast(conv.WatcherEvent{
		Type:  "agent-added",
		Agent: &agents.Agent{Name: "new-prod", Runtime: "claude"},
	})

	msg := c.recv(t)
	if msg.Type != "agents-count" {
		t.Fatalf("type = %q, want agents-count", msg.Type)
	}
	msg = c.recv(t)
	if msg.Type != "agent-added" {
		t.Fatalf("type = %q, want agent-added for matching agent", msg.Type)
	}

	// Broadcast non-matching agent — should get agents-count only, no agent-added
	srv.Broadcast(conv.WatcherEvent{
		Type:  "agent-added",
		Agent: &agents.Agent{Name: "new-dev", Runtime: "claude"},
	})

	msg = c.recv(t)
	if msg.Type != "agents-count" {
		t.Fatalf("type = %q, want agents-count", msg.Type)
	}

	// Use list-agents as a fence: if agent-added was sent it would appear before this
	c.send(t, clientMessage{ID: "fence", Type: "list-agents"})
	msg = c.recv(t)
	if msg.Type != "list-agents" {
		t.Fatalf("type = %q, want list-agents fence (non-matching agent should be filtered)", msg.Type)
	}
}

func TestDoubleHello(t *testing.T) {
	_, ts := setupTestServer(t)
	c := dialTestServer(t, ts)

	// First hello — succeeds
	c.send(t, clientMessage{ID: "1", Type: "hello", Protocol: "tmux-converter.v1"})
	resp := c.recv(t)
	if resp.OK == nil || !*resp.OK {
		t.Fatalf("first hello failed: %+v", resp)
	}

	// Second hello — should error
	c.send(t, clientMessage{ID: "2", Type: "hello", Protocol: "tmux-converter.v1"})
	resp = c.recv(t)
	if resp.Type != "error" {
		t.Fatalf("type = %q, want error for double hello", resp.Type)
	}
	if resp.Error == "" {
		t.Fatal("expected error message for double hello")
	}
}

func TestUnknownMessageType(t *testing.T) {
	_, ts := setupTestServer(t)
	c := dialTestServer(t, ts)

	c.send(t, clientMessage{ID: "h", Type: "hello", Protocol: "tmux-converter.v1"})
	c.recv(t)

	c.send(t, clientMessage{ID: "1", Type: "totally-unknown"})
	resp := c.recv(t)

	if resp.Type != "error" {
		t.Fatalf("type = %q, want error", resp.Type)
	}
	if resp.UnknownType != "totally-unknown" {
		t.Fatalf("unknownType = %q, want totally-unknown", resp.UnknownType)
	}
}

func TestFollowAgentNotFound(t *testing.T) {
	_, ts := setupTestServer(t) // no agents
	c := dialTestServer(t, ts)

	c.send(t, clientMessage{ID: "h", Type: "hello", Protocol: "tmux-converter.v1"})
	c.recv(t)

	c.send(t, clientMessage{ID: "1", Type: "follow-agent", Agent: "nonexistent"})
	resp := c.recv(t)

	if resp.Type != "error" {
		t.Fatalf("type = %q, want error", resp.Type)
	}
	if resp.Error == "" {
		t.Fatal("expected error message for nonexistent agent")
	}
}

func TestFollowAgentMissingName(t *testing.T) {
	_, ts := setupTestServer(t)
	c := dialTestServer(t, ts)

	c.send(t, clientMessage{ID: "h", Type: "hello", Protocol: "tmux-converter.v1"})
	c.recv(t)

	c.send(t, clientMessage{ID: "1", Type: "follow-agent"})
	resp := c.recv(t)

	if resp.Type != "error" {
		t.Fatalf("type = %q, want error", resp.Type)
	}
	if resp.Error != "agent required" {
		t.Fatalf("error = %q, want 'agent required'", resp.Error)
	}
}

func TestSendPromptMissingAgent(t *testing.T) {
	_, ts := setupTestServer(t)
	c := dialTestServer(t, ts)

	c.send(t, clientMessage{ID: "h", Type: "hello", Protocol: "tmux-converter.v1"})
	c.recv(t)

	c.send(t, clientMessage{ID: "1", Type: "send-prompt", Prompt: "hello"})
	resp := c.recv(t)

	if resp.Type != "error" {
		t.Fatalf("type = %q, want error", resp.Type)
	}
	if resp.Error != "agent field required" {
		t.Fatalf("error = %q, want 'agent field required'", resp.Error)
	}
}

func TestSendPromptMissingPrompt(t *testing.T) {
	_, ts := setupTestServer(t)
	c := dialTestServer(t, ts)

	c.send(t, clientMessage{ID: "h", Type: "hello", Protocol: "tmux-converter.v1"})
	c.recv(t)

	c.send(t, clientMessage{ID: "1", Type: "send-prompt", Agent: "test-agent"})
	resp := c.recv(t)

	if resp.Type != "error" {
		t.Fatalf("type = %q, want error", resp.Type)
	}
	if resp.Error != "prompt field required" {
		t.Fatalf("error = %q, want 'prompt field required'", resp.Error)
	}
}

func TestSubscribeConversationMissingID(t *testing.T) {
	_, ts := setupTestServer(t)
	c := dialTestServer(t, ts)

	c.send(t, clientMessage{ID: "h", Type: "hello", Protocol: "tmux-converter.v1"})
	c.recv(t)

	c.send(t, clientMessage{ID: "1", Type: "subscribe-conversation"})
	resp := c.recv(t)

	if resp.Type != "error" {
		t.Fatalf("type = %q, want error", resp.Type)
	}
	if resp.Error != "conversationId required" {
		t.Fatalf("error = %q, want 'conversationId required'", resp.Error)
	}
}

func TestListAgentsFilterError(t *testing.T) {
	_, ts := setupTestServer(t, "test-agent")
	c := dialTestServer(t, ts)

	c.send(t, clientMessage{ID: "h", Type: "hello", Protocol: "tmux-converter.v1"})
	c.recv(t)

	c.send(t, clientMessage{ID: "1", Type: "list-agents", IncludeSessionFilter: "[invalid"})
	resp := c.recv(t)

	if resp.Type != "list-agents" {
		t.Fatalf("type = %q, want list-agents", resp.Type)
	}
	if resp.OK == nil || *resp.OK {
		t.Fatalf("ok = %v, want false", resp.OK)
	}
	if resp.Error == "" {
		t.Fatal("expected error for invalid regex")
	}
}

func TestSubscribeAgentsFilterError(t *testing.T) {
	_, ts := setupTestServer(t)
	c := dialTestServer(t, ts)

	c.send(t, clientMessage{ID: "h", Type: "hello", Protocol: "tmux-converter.v1"})
	c.recv(t)

	c.send(t, clientMessage{ID: "1", Type: "subscribe-agents", IncludeSessionFilter: "[bad"})
	resp := c.recv(t)

	if resp.Type != "subscribe-agents" {
		t.Fatalf("type = %q, want subscribe-agents", resp.Type)
	}
	if resp.OK == nil || *resp.OK {
		t.Fatalf("ok = %v, want false", resp.OK)
	}
}

func TestBroadcastConversationEvent(t *testing.T) {
	srv, ts := setupTestServer(t)
	c := dialTestServer(t, ts)

	c.send(t, clientMessage{ID: "h", Type: "hello", Protocol: "tmux-converter.v1"})
	c.recv(t)

	// Broadcast a conversation-event with nil event — should be a no-op
	srv.Broadcast(conv.WatcherEvent{Type: "conversation-event", Event: nil})

	// Broadcast a conversation-event with a valid event — client has no matching
	// subscription so no message delivered, but covers the Broadcast switch branch
	srv.Broadcast(conv.WatcherEvent{
		Type: "conversation-event",
		Event: &conv.ConversationEvent{
			ConversationID: "test-conv",
			Type:           "user",
		},
	})

	// Verify client still works by sending a request
	c.send(t, clientMessage{ID: "1", Type: "list-agents"})
	resp := c.recv(t)
	if resp.Type != "list-agents" {
		t.Fatalf("type = %q, want list-agents", resp.Type)
	}
}

func TestBroadcastConversationStarted(t *testing.T) {
	srv, ts := setupTestServer(t, "test-agent")
	c := dialTestServer(t, ts)

	c.send(t, clientMessage{ID: "h", Type: "hello", Protocol: "tmux-converter.v1"})
	c.recv(t)

	// Broadcast conversation-started — client has no follow, so no snapshot delivered
	srv.Broadcast(conv.WatcherEvent{
		Type:      "conversation-started",
		Agent:     &agents.Agent{Name: "test-agent", Runtime: "claude"},
		NewConvID: "claude:test-agent:conv1",
	})

	// Verify client still works
	c.send(t, clientMessage{ID: "1", Type: "list-agents"})
	resp := c.recv(t)
	if resp.Type != "list-agents" {
		t.Fatalf("type = %q, want list-agents", resp.Type)
	}
}

func TestBroadcastConversationSwitched(t *testing.T) {
	srv, ts := setupTestServer(t, "test-agent")
	c := dialTestServer(t, ts)

	c.send(t, clientMessage{ID: "h", Type: "hello", Protocol: "tmux-converter.v1"})
	c.recv(t)

	// Broadcast conversation-switched — client has no follow, so no message
	srv.Broadcast(conv.WatcherEvent{
		Type:      "conversation-switched",
		Agent:     &agents.Agent{Name: "test-agent", Runtime: "claude"},
		OldConvID: "claude:test-agent:old",
		NewConvID: "claude:test-agent:new",
	})

	// Verify client still works
	c.send(t, clientMessage{ID: "1", Type: "list-agents"})
	resp := c.recv(t)
	if resp.Type != "list-agents" {
		t.Fatalf("type = %q, want list-agents", resp.Type)
	}
}

func TestSubscribeConversationValid(t *testing.T) {
	srv, ts := setupTestServer(t, "conv-agent")

	dir := t.TempDir()
	convPath := filepath.Join(dir, "test.jsonl")
	jsonl := `{"type":"user","uuid":"u1","timestamp":"2026-02-14T01:44:54.253Z","message":{"role":"user","content":[{"type":"text","text":"hello"}]}}` + "\n"
	if err := os.WriteFile(convPath, []byte(jsonl), 0644); err != nil {
		t.Fatal(err)
	}

	convID := "claude:conv-agent:test"
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

	if err := srv.watcher.EnsureTailing("conv-agent"); err != nil {
		t.Fatalf("EnsureTailing: %v", err)
	}

	// Wait for buffer + events
	deadline := time.After(5 * time.Second)
	for {
		buf := srv.watcher.GetBuffer(convID)
		if buf != nil && len(buf.Snapshot(conv.EventFilter{})) > 0 {
			break
		}
		select {
		case <-deadline:
			t.Fatal("timeout waiting for conversation buffer")
		case <-time.After(50 * time.Millisecond):
		}
	}

	c := dialTestServer(t, ts)
	c.send(t, clientMessage{ID: "h", Type: "hello", Protocol: "tmux-converter.v1"})
	c.recv(t)

	c.send(t, clientMessage{
		ID:             "1",
		Type:           "subscribe-conversation",
		ConversationID: convID,
	})
	resp := c.recv(t)

	if resp.Type != "conversation-snapshot" {
		t.Fatalf("type = %q, want conversation-snapshot", resp.Type)
	}
	if resp.SubscriptionID == "" {
		t.Fatal("expected non-empty subscriptionId")
	}
	if resp.ConversationID != convID {
		t.Fatalf("conversationId = %q, want %q", resp.ConversationID, convID)
	}

	// Drain chunked snapshot
	events := c.drainSnapshot(t)
	if len(events) != 1 {
		t.Fatalf("events len = %d, want 1", len(events))
	}
}

func TestSubscribeConversationNotFound(t *testing.T) {
	_, ts := setupTestServer(t)
	c := dialTestServer(t, ts)

	c.send(t, clientMessage{ID: "h", Type: "hello", Protocol: "tmux-converter.v1"})
	c.recv(t)

	// Subscribe to a conversation with unknown format (no agent extraction)
	c.send(t, clientMessage{
		ID:             "1",
		Type:           "subscribe-conversation",
		ConversationID: "unknown-format-id",
	})
	resp := c.recv(t)

	if resp.Type != "error" {
		t.Fatalf("type = %q, want error", resp.Type)
	}
	if resp.Error != "conversation not found" {
		t.Fatalf("error = %q, want 'conversation not found'", resp.Error)
	}
}

func TestFollowAgentRefollow(t *testing.T) {
	_, ts := setupTestServer(t, "test-agent")
	c := dialTestServer(t, ts)

	c.send(t, clientMessage{ID: "h", Type: "hello", Protocol: "tmux-converter.v1"})
	c.recv(t)

	// First follow
	c.send(t, clientMessage{ID: "1", Type: "follow-agent", Agent: "test-agent"})
	resp := c.recv(t)
	if resp.Type != "follow-agent" || resp.OK == nil || !*resp.OK {
		t.Fatalf("first follow failed: %+v", resp)
	}
	firstSubID := resp.SubscriptionID

	// Re-follow the same agent — should succeed with new subscription ID
	c.send(t, clientMessage{ID: "2", Type: "follow-agent", Agent: "test-agent"})
	resp = c.recv(t)
	if resp.Type != "follow-agent" || resp.OK == nil || !*resp.OK {
		t.Fatalf("re-follow failed: %+v", resp)
	}
	if resp.SubscriptionID == firstSubID {
		t.Fatalf("re-follow should give new subscriptionId, got same %q", resp.SubscriptionID)
	}
}

func TestUnsubscribeNonexistent(t *testing.T) {
	_, ts := setupTestServer(t)
	c := dialTestServer(t, ts)

	c.send(t, clientMessage{ID: "h", Type: "hello", Protocol: "tmux-converter.v1"})
	c.recv(t)

	// Unsubscribe with an ID that doesn't exist — should still return ok
	c.send(t, clientMessage{ID: "1", Type: "unsubscribe", SubscriptionID: "sub-999"})
	resp := c.recv(t)

	if resp.Type != "unsubscribe" {
		t.Fatalf("type = %q, want unsubscribe", resp.Type)
	}
	if resp.OK == nil || !*resp.OK {
		t.Fatalf("ok = %v, want true", resp.OK)
	}
}

func TestDeliverConversationStartedViaFollow(t *testing.T) {
	// Tests the full follow-agent → pending → conversation-started → snapshot delivery path.
	// Uses a blocking discoverer to guarantee the pending follow (no race with discovery).
	srv, ts := setupTestServer(t, "conv-agent")

	dir := t.TempDir()
	convPath := filepath.Join(dir, "test.jsonl")
	jsonl := `{"type":"user","uuid":"u1","timestamp":"2026-02-14T01:44:54.253Z","message":{"role":"user","content":[{"type":"text","text":"hello from follow"}]}}` + "\n"
	if err := os.WriteFile(convPath, []byte(jsonl), 0644); err != nil {
		t.Fatal(err)
	}

	convID := "claude:conv-agent:test"
	ready := make(chan struct{})
	disc := &blockingDiscoverer{
		files: []conv.ConversationFile{{
			Path:                 convPath,
			NativeConversationID: "test",
			ConversationID:       convID,
			Runtime:              "claude",
		}},
		watchDirs: []string{dir},
		ready:     ready,
	}

	srv.watcher.RegisterRuntime("claude", disc, func(agentName, cID string) conv.Parser {
		return conv.NewClaudeParser(agentName, cID)
	})

	c := dialTestServer(t, ts)
	c.send(t, clientMessage{ID: "h", Type: "hello", Protocol: "tmux-converter.v1"})
	c.recv(t)

	// Follow agent — EnsureTailing starts but discovery blocks.
	// This guarantees the pending follow (no active conversation yet).
	c.send(t, clientMessage{ID: "1", Type: "follow-agent", Agent: "conv-agent"})
	resp := c.recv(t)

	if resp.Type != "follow-agent" {
		t.Fatalf("type = %q, want follow-agent", resp.Type)
	}
	if resp.OK == nil || !*resp.OK {
		t.Fatalf("ok = %v, want true", resp.OK)
	}
	if resp.ConversationID != "" {
		t.Fatalf("conversationId = %q, want empty (pending follow)", resp.ConversationID)
	}

	// Unblock discovery → buffer gets created
	close(ready)

	// Wait for buffer + events
	deadline := time.After(5 * time.Second)
	for {
		buf := srv.watcher.GetBuffer(convID)
		if buf != nil && len(buf.Snapshot(conv.EventFilter{})) > 0 {
			break
		}
		select {
		case <-deadline:
			t.Fatal("timeout waiting for conversation buffer")
		case <-time.After(50 * time.Millisecond):
		}
	}

	// Broadcast conversation-started → deliverConversationStarted binds the pending follow
	srv.Broadcast(conv.WatcherEvent{
		Type:      "conversation-started",
		Agent:     &agents.Agent{Name: "conv-agent", Runtime: "claude"},
		NewConvID: convID,
	})

	// Client should receive conversation-snapshot (the pending follow resolved)
	msg := c.recv(t)
	if msg.Type != "conversation-snapshot" {
		t.Fatalf("type = %q, want conversation-snapshot", msg.Type)
	}
	if msg.ConversationID != convID {
		t.Fatalf("conversationId = %q, want %q", msg.ConversationID, convID)
	}
	if msg.SubscriptionID == "" {
		t.Fatal("expected non-empty subscriptionId")
	}

	// Drain chunked snapshot
	events := c.drainSnapshot(t)
	if len(events) != 1 {
		t.Fatalf("events len = %d, want 1", len(events))
	}
}

func TestDeliverConversationSwitchedViaFollow(t *testing.T) {
	// Tests conversation switch: follow agent with active conv, then switch to new conv.
	// Uses a switchDiscoverer that returns different files on each call to simulate rotation.
	srv, ts := setupTestServer(t, "conv-agent")

	dir := t.TempDir()
	convPath1 := filepath.Join(dir, "test1.jsonl")
	jsonl1 := `{"type":"user","uuid":"u1","timestamp":"2026-02-14T01:44:54.253Z","message":{"role":"user","content":[{"type":"text","text":"original"}]}}` + "\n"
	if err := os.WriteFile(convPath1, []byte(jsonl1), 0644); err != nil {
		t.Fatal(err)
	}

	convPath2 := filepath.Join(dir, "test2.jsonl")
	jsonl2 := `{"type":"user","uuid":"u2","timestamp":"2026-02-14T02:00:00.000Z","message":{"role":"user","content":[{"type":"text","text":"switched"}]}}` + "\n"
	if err := os.WriteFile(convPath2, []byte(jsonl2), 0644); err != nil {
		t.Fatal(err)
	}

	convID1 := "claude:conv-agent:test1"
	convID2 := "claude:conv-agent:test2"

	// Use a switchDiscoverer that returns file1 first, then file2
	disc := &switchDiscoverer{
		firstFiles: []conv.ConversationFile{{
			Path:                 convPath1,
			NativeConversationID: "test1",
			ConversationID:       convID1,
			Runtime:              "claude",
		}},
		secondFiles: []conv.ConversationFile{{
			Path:                 convPath2,
			NativeConversationID: "test2",
			ConversationID:       convID2,
			Runtime:              "claude",
		}},
		watchDirs: []string{dir},
	}

	srv.watcher.RegisterRuntime("claude", disc, func(agentName, cID string) conv.Parser {
		return conv.NewClaudeParser(agentName, cID)
	})

	// First EnsureTailing → discovers file1
	if err := srv.watcher.EnsureTailing("conv-agent"); err != nil {
		t.Fatalf("EnsureTailing: %v", err)
	}

	// Wait for first buffer
	deadline := time.After(5 * time.Second)
	for {
		buf := srv.watcher.GetBuffer(convID1)
		if buf != nil && len(buf.Snapshot(conv.EventFilter{})) > 0 {
			break
		}
		select {
		case <-deadline:
			t.Fatal("timeout waiting for first conversation buffer")
		case <-time.After(50 * time.Millisecond):
		}
	}

	c := dialTestServer(t, ts)
	c.send(t, clientMessage{ID: "h", Type: "hello", Protocol: "tmux-converter.v1"})
	c.recv(t)

	// Follow agent with active conversation
	c.send(t, clientMessage{ID: "1", Type: "follow-agent", Agent: "conv-agent"})
	resp := c.recv(t)
	if resp.Type != "follow-agent" || resp.OK == nil || !*resp.OK {
		t.Fatalf("follow failed: %+v", resp)
	}

	// Drain the initial snapshot chunks
	c.drainSnapshot(t)

	// Create a new .jsonl file in the watched directory to trigger fsnotify → rediscovery.
	// The switchDiscoverer returns file2 on the second FindConversations call.
	triggerPath := filepath.Join(dir, "trigger.jsonl")
	if err := os.WriteFile(triggerPath, []byte(""), 0644); err != nil {
		t.Fatal(err)
	}

	// Wait for second buffer to be created by the watcher's fsnotify → rediscovery
	deadline = time.After(5 * time.Second)
	for {
		buf := srv.watcher.GetBuffer(convID2)
		if buf != nil && len(buf.Snapshot(conv.EventFilter{})) > 0 {
			break
		}
		select {
		case <-deadline:
			t.Fatal("timeout waiting for second conversation buffer")
		case <-time.After(50 * time.Millisecond):
		}
	}

	// Broadcast conversation-switched
	srv.Broadcast(conv.WatcherEvent{
		Type:      "conversation-switched",
		Agent:     &agents.Agent{Name: "conv-agent", Runtime: "claude"},
		OldConvID: convID1,
		NewConvID: convID2,
	})

	// Client should receive conversation-switched + conversation-snapshot
	msg := c.recvAfterSnapshot(t)
	if msg.Type != "conversation-switched" {
		t.Fatalf("type = %q, want conversation-switched", msg.Type)
	}
	if msg.From != convID1 {
		t.Fatalf("from = %q, want %q", msg.From, convID1)
	}
	if msg.To != convID2 {
		t.Fatalf("to = %q, want %q", msg.To, convID2)
	}

	msg = c.recv(t)
	if msg.Type != "conversation-snapshot" {
		t.Fatalf("type = %q, want conversation-snapshot", msg.Type)
	}
	if msg.ConversationID != convID2 {
		t.Fatalf("conversationId = %q, want %q", msg.ConversationID, convID2)
	}
	if msg.Reason != "switch" {
		t.Fatalf("reason = %q, want switch", msg.Reason)
	}
}

func TestPendingSubscribeConversationResolved(t *testing.T) {
	// Tests the pending subscribe-conversation path: buffer doesn't exist yet when
	// subscribe-conversation is sent, but resolves when conversation-started is broadcast.
	srv, ts := setupTestServer(t, "conv-agent")

	dir := t.TempDir()
	convPath := filepath.Join(dir, "test.jsonl")
	jsonl := `{"type":"user","uuid":"u1","timestamp":"2026-02-14T01:44:54.253Z","message":{"role":"user","content":[{"type":"text","text":"pending resolved"}]}}` + "\n"
	if err := os.WriteFile(convPath, []byte(jsonl), 0644); err != nil {
		t.Fatal(err)
	}

	convID := "claude:conv-agent:test"
	ready := make(chan struct{})
	disc := &blockingDiscoverer{
		files: []conv.ConversationFile{{
			Path:                 convPath,
			NativeConversationID: "test",
			ConversationID:       convID,
			Runtime:              "claude",
		}},
		watchDirs: []string{dir},
		ready:     ready,
	}

	srv.watcher.RegisterRuntime("claude", disc, func(agentName, cID string) conv.Parser {
		return conv.NewClaudeParser(agentName, cID)
	})

	c := dialTestServer(t, ts)
	c.send(t, clientMessage{ID: "h", Type: "hello", Protocol: "tmux-converter.v1"})
	c.recv(t)

	// Subscribe to conversation — EnsureTailing starts but discovery blocks.
	// Buffer is nil, agent exists → creates pending subscription (no response yet).
	c.send(t, clientMessage{
		ID:             "1",
		Type:           "subscribe-conversation",
		ConversationID: convID,
	})

	// Unblock discovery → buffer gets created
	close(ready)

	// Wait for buffer
	deadline := time.After(5 * time.Second)
	for {
		buf := srv.watcher.GetBuffer(convID)
		if buf != nil && len(buf.Snapshot(conv.EventFilter{})) > 0 {
			break
		}
		select {
		case <-deadline:
			t.Fatal("timeout waiting for conversation buffer")
		case <-time.After(50 * time.Millisecond):
		}
	}

	// Broadcast conversation-started → resolves the pending subscribe-conversation
	srv.Broadcast(conv.WatcherEvent{
		Type:      "conversation-started",
		Agent:     &agents.Agent{Name: "conv-agent", Runtime: "claude"},
		NewConvID: convID,
	})

	// Client should receive conversation-snapshot as the pending sub resolution
	msg := c.recv(t)
	if msg.Type != "conversation-snapshot" {
		t.Fatalf("type = %q, want conversation-snapshot", msg.Type)
	}
	if msg.ConversationID != convID {
		t.Fatalf("conversationId = %q, want %q", msg.ConversationID, convID)
	}
	if msg.ID != "1" {
		t.Fatalf("id = %q, want 1 (original request ID)", msg.ID)
	}
	if msg.SubscriptionID == "" {
		t.Fatal("expected non-empty subscriptionId")
	}

	// Drain chunked snapshot
	events := c.drainSnapshot(t)
	if len(events) != 1 {
		t.Fatalf("events len = %d, want 1", len(events))
	}
}

func TestInvalidJSON(t *testing.T) {
	_, ts := setupTestServer(t)
	c := dialTestServer(t, ts)

	// Send malformed JSON — should get error before handshake check
	if err := c.conn.Write(c.ctx, websocket.MessageText, []byte("{bad json")); err != nil {
		t.Fatalf("write: %v", err)
	}
	resp := c.recv(t)

	if resp.Type != "error" {
		t.Fatalf("type = %q, want error", resp.Type)
	}
	if resp.Error != "invalid JSON" {
		t.Fatalf("error = %q, want 'invalid JSON'", resp.Error)
	}
}

func TestBinaryMessageInvalid(t *testing.T) {
	_, ts := setupTestServer(t)
	c := dialTestServer(t, ts)

	// Send a too-short binary frame — triggers handleBinaryMessage error path
	if err := c.conn.Write(c.ctx, websocket.MessageBinary, []byte{0x01}); err != nil {
		t.Fatalf("write: %v", err)
	}
	resp := c.recv(t)

	if resp.Type != "error" {
		t.Fatalf("type = %q, want error", resp.Type)
	}
}

func TestBinaryMessageUnsupportedType(t *testing.T) {
	_, ts := setupTestServer(t)
	c := dialTestServer(t, ts)

	// Send binary with valid envelope but unsupported message type (0xFF)
	// Format: msgType(1) + agentName(utf8) + \0 + payload
	frame := []byte{0xFF}
	frame = append(frame, []byte("agent")...)
	frame = append(frame, 0x00)
	frame = append(frame, []byte("payload")...)

	if err := c.conn.Write(c.ctx, websocket.MessageBinary, frame); err != nil {
		t.Fatalf("write: %v", err)
	}
	resp := c.recv(t)

	if resp.Type != "error" {
		t.Fatalf("type = %q, want error", resp.Type)
	}
}
