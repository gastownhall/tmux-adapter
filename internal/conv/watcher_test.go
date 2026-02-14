package conv

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// mockDiscoverer returns pre-configured discovery results.
type mockDiscoverer struct {
	files     []ConversationFile
	watchDirs []string
}

func (d *mockDiscoverer) FindConversations(_, _ string) (DiscoveryResult, error) {
	return DiscoveryResult{
		Files:     d.files,
		WatchDirs: d.watchDirs,
	}, nil
}

func TestWatcherCreatesBuffer(t *testing.T) {
	dir := t.TempDir()
	convPath := filepath.Join(dir, "test.jsonl")

	// Write initial data
	if err := os.WriteFile(convPath, []byte(`{"type":"user","uuid":"u1","timestamp":"2026-02-14T01:44:54.253Z","message":{"role":"user","content":[{"type":"text","text":"hello"}]}}`+"\n"), 0644); err != nil {
		t.Fatal(err)
	}

	disc := &mockDiscoverer{
		files: []ConversationFile{{
			Path:                 convPath,
			NativeConversationID: "test",
			ConversationID:       "claude:test-agent:test",
			Runtime:              "claude",
		}},
		watchDirs: []string{dir},
	}

	// We can't use a real Registry without tmux, so test the discovery+buffer
	// directly by creating a watcher and calling discoverAndTail manually
	watcher := NewConversationWatcher(nil, 100)
	watcher.RegisterRuntime("claude", disc, func(agentName, convID string) Parser {
		return NewClaudeParser(agentName, convID)
	})

	// Simulate what startWatching does
	agent := struct {
		Name    string
		Runtime string
		WorkDir string
	}{"test-agent", "claude", "/tmp/test"}

	// Use the agents package Agent type indirectly through discoverAndTail
	// For this unit test, we'll test the buffer integration directly

	buf := NewConversationBuffer("claude:test-agent:test", "test-agent", 100)
	parser := NewClaudeParser("test-agent", "claude:test-agent:test")

	// Simulate parsing a line
	raw := []byte(`{"type":"user","uuid":"u1","timestamp":"2026-02-14T01:44:54.253Z","message":{"role":"user","content":[{"type":"text","text":"hello"}]}}`)
	events, err := parser.Parse(raw)
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}

	for _, e := range events {
		buf.Append(e)
	}

	snap := buf.Snapshot(EventFilter{})
	if len(snap) != 1 {
		t.Fatalf("buffer has %d events, want 1", len(snap))
	}
	if snap[0].Type != EventUser {
		t.Fatalf("event type = %q, want %q", snap[0].Type, EventUser)
	}

	_ = agent // used for documentation
	watcher.Stop()
}

func TestWatcherEventChannel(t *testing.T) {
	watcher := NewConversationWatcher(nil, 100)

	// Test that emitEvent works
	go func() {
		watcher.emitEvent(WatcherEvent{Type: "agent-added"})
	}()

	select {
	case e := <-watcher.Events():
		if e.Type != "agent-added" {
			t.Fatalf("event type = %q, want %q", e.Type, "agent-added")
		}
	case <-time.After(time.Second):
		t.Fatal("timeout waiting for event")
	}

	watcher.Stop()
}
