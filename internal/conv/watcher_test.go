package conv

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/gastownhall/tmux-adapter/internal/agents"
	"github.com/gastownhall/tmux-adapter/internal/tmux"
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

// mockCtrl implements agents.ControlModeInterface for testing.
type mockCtrl struct {
	sessions []tmux.SessionInfo
	panes    map[string]tmux.PaneInfo
	notifCh  chan tmux.Notification
}

func (m *mockCtrl) ListSessions() ([]tmux.SessionInfo, error) { return m.sessions, nil }
func (m *mockCtrl) GetPaneInfo(session string) (tmux.PaneInfo, error) {
	return m.panes[session], nil
}
func (m *mockCtrl) Notifications() <-chan tmux.Notification { return m.notifCh }

// newTestRegistry creates a real registry with one agent, starts it, drains
// the initial "added" event, and registers cleanup.
func newTestRegistry(t *testing.T, agentName, runtime, workDir string) (*agents.Registry, *mockCtrl) {
	t.Helper()
	ctrl := &mockCtrl{
		sessions: []tmux.SessionInfo{{Name: agentName, Attached: true}},
		panes:    map[string]tmux.PaneInfo{agentName: {Command: runtime, WorkDir: workDir}},
		notifCh:  make(chan tmux.Notification, 10),
	}
	registry := agents.NewRegistry(ctrl, "", nil)
	if err := registry.Start(); err != nil {
		t.Fatalf("registry.Start() error: %v", err)
	}
	t.Cleanup(registry.Stop)
	// Drain the initial "added" event from the registry
	select {
	case <-registry.Events():
	case <-time.After(time.Second):
		t.Fatal("timeout draining initial registry event")
	}
	return registry, ctrl
}

func TestWatcherStartStop(t *testing.T) {
	registry, _ := newTestRegistry(t, "agent-ss", "claude", "/tmp/test")
	w := NewConversationWatcher(registry, 100)
	w.Start()
	// Drain any agent-added events that Start emits
	drainWatcherEvents(w)
	w.Stop()
}

func TestWatcherRecordAndGetBuffer(t *testing.T) {
	registry, _ := newTestRegistry(t, "agent-rb", "claude", "/tmp/test")
	w := NewConversationWatcher(registry, 100)

	agent := agents.Agent{Name: "agent-rb", Runtime: "claude", WorkDir: "/tmp/test"}
	w.recordAgent(agent)

	w.mu.RLock()
	_, ok := w.knownAgents["agent-rb"]
	w.mu.RUnlock()
	if !ok {
		t.Fatal("recordAgent did not populate knownAgents")
	}

	// Manually create a stream and verify GetBuffer returns it
	convID := "claude:agent-rb:test123"
	buf := NewConversationBuffer(convID, "agent-rb", 100)
	w.mu.Lock()
	w.streams[convID] = &conversationStream{
		conversationID: convID,
		agent:          agent,
		buffer:         buf,
		files:          make(map[string]*fileStream),
		cancel:         func() {},
	}
	w.mu.Unlock()

	got := w.GetBuffer(convID)
	if got != buf {
		t.Fatal("GetBuffer did not return the expected buffer")
	}

	// Unknown conversation returns nil
	if w.GetBuffer("unknown-conv") != nil {
		t.Fatal("GetBuffer returned non-nil for unknown conversation")
	}

	w.Stop()
}

func TestWatcherGetActiveConversation(t *testing.T) {
	registry, _ := newTestRegistry(t, "agent-ac", "claude", "/tmp/test")
	w := NewConversationWatcher(registry, 100)

	w.mu.Lock()
	w.activeByAgent["agent-ac"] = "claude:agent-ac:conv1"
	w.mu.Unlock()

	got := w.GetActiveConversation("agent-ac")
	if got != "claude:agent-ac:conv1" {
		t.Fatalf("GetActiveConversation = %q, want %q", got, "claude:agent-ac:conv1")
	}

	// Unknown agent returns ""
	if w.GetActiveConversation("nope") != "" {
		t.Fatal("GetActiveConversation returned non-empty for unknown agent")
	}

	w.Stop()
}

func TestWatcherGetAgentForConversation(t *testing.T) {
	registry, _ := newTestRegistry(t, "agent-gac", "claude", "/tmp/test")
	w := NewConversationWatcher(registry, 100)

	w.mu.Lock()
	w.convToAgent["claude:agent-gac:conv1"] = "agent-gac"
	w.mu.Unlock()

	got := w.GetAgentForConversation("claude:agent-gac:conv1")
	if got != "agent-gac" {
		t.Fatalf("GetAgentForConversation = %q, want %q", got, "agent-gac")
	}

	if w.GetAgentForConversation("unknown") != "" {
		t.Fatal("GetAgentForConversation returned non-empty for unknown conv")
	}

	w.Stop()
}

func TestWatcherHasDiscoverer(t *testing.T) {
	registry, _ := newTestRegistry(t, "agent-hd", "claude", "/tmp/test")
	w := NewConversationWatcher(registry, 100)

	disc := &mockDiscoverer{}
	w.RegisterRuntime("claude", disc, func(agentName, convID string) Parser {
		return NewClaudeParser(agentName, convID)
	})

	if !w.HasDiscoverer("claude") {
		t.Fatal("HasDiscoverer(\"claude\") = false, want true")
	}
	if w.HasDiscoverer("gemini") {
		t.Fatal("HasDiscoverer(\"gemini\") = true, want false")
	}

	w.Stop()
}

func TestWatcherListConversations(t *testing.T) {
	registry, _ := newTestRegistry(t, "agent-lc", "claude", "/tmp/test")
	w := NewConversationWatcher(registry, 100)

	agent := agents.Agent{Name: "agent-lc", Runtime: "claude", WorkDir: "/tmp/test"}
	convID := "claude:agent-lc:conv1"

	w.mu.Lock()
	w.streams[convID] = &conversationStream{
		conversationID: convID,
		agent:          agent,
		buffer:         NewConversationBuffer(convID, "agent-lc", 100),
		files:          make(map[string]*fileStream),
		cancel:         func() {},
	}
	w.mu.Unlock()

	convs := w.ListConversations()
	if len(convs) != 1 {
		t.Fatalf("ListConversations returned %d items, want 1", len(convs))
	}
	if convs[0].ConversationID != convID {
		t.Fatalf("ConversationID = %q, want %q", convs[0].ConversationID, convID)
	}
	if convs[0].AgentName != "agent-lc" {
		t.Fatalf("AgentName = %q, want %q", convs[0].AgentName, "agent-lc")
	}
	if convs[0].Runtime != "claude" {
		t.Fatalf("Runtime = %q, want %q", convs[0].Runtime, "claude")
	}

	w.Stop()
}

func TestWatcherListAgents(t *testing.T) {
	registry, _ := newTestRegistry(t, "agent-la", "claude", "/tmp/test")
	w := NewConversationWatcher(registry, 100)

	agentList := w.ListAgents()
	if len(agentList) != 1 {
		t.Fatalf("ListAgents returned %d agents, want 1", len(agentList))
	}
	if agentList[0].Name != "agent-la" {
		t.Fatalf("agent name = %q, want %q", agentList[0].Name, "agent-la")
	}

	w.Stop()
}

func TestWatcherEnsureTailingUnknownAgent(t *testing.T) {
	// Create a registry with NO agents
	ctrl := &mockCtrl{
		sessions: nil,
		panes:    map[string]tmux.PaneInfo{},
		notifCh:  make(chan tmux.Notification, 10),
	}
	registry := agents.NewRegistry(ctrl, "", nil)
	if err := registry.Start(); err != nil {
		t.Fatalf("registry.Start() error: %v", err)
	}
	t.Cleanup(registry.Stop)

	w := NewConversationWatcher(registry, 100)

	err := w.EnsureTailing("nonexistent-agent")
	if err == nil {
		t.Fatal("EnsureTailing for unknown agent should return error")
	}

	w.Stop()
}

func TestWatcherEnsureTailingRefCounting(t *testing.T) {
	dir := t.TempDir()
	registry, _ := newTestRegistry(t, "agent-rc", "claude", dir)
	w := NewConversationWatcher(registry, 100)

	// Register a discoverer that returns no files (empty discovery)
	disc := &mockDiscoverer{
		files:     nil,
		watchDirs: []string{dir},
	}
	w.RegisterRuntime("claude", disc, func(agentName, convID string) Parser {
		return NewClaudeParser(agentName, convID)
	})

	// Record the agent so EnsureTailing can find it
	agent := agents.Agent{Name: "agent-rc", Runtime: "claude", WorkDir: dir}
	w.recordAgent(agent)

	// First EnsureTailing — starts tailing
	if err := w.EnsureTailing("agent-rc"); err != nil {
		t.Fatalf("first EnsureTailing error: %v", err)
	}

	// Second EnsureTailing — increments refCount
	if err := w.EnsureTailing("agent-rc"); err != nil {
		t.Fatalf("second EnsureTailing error: %v", err)
	}

	w.tailingMu.Lock()
	state := w.tailing["agent-rc"]
	if state == nil {
		w.tailingMu.Unlock()
		t.Fatal("tailing state is nil after two EnsureTailing calls")
	}
	if state.refCount != 2 {
		w.tailingMu.Unlock()
		t.Fatalf("refCount = %d, want 2", state.refCount)
	}
	w.tailingMu.Unlock()

	// Release twice
	w.ReleaseTailing("agent-rc")
	w.tailingMu.Lock()
	if state.refCount != 1 {
		w.tailingMu.Unlock()
		t.Fatalf("refCount after first release = %d, want 1", state.refCount)
	}
	w.tailingMu.Unlock()

	w.ReleaseTailing("agent-rc")
	// After second release, refCount drops to 0 and a grace timer is set.
	// The tailing entry is still present until the timer fires.
	w.tailingMu.Lock()
	state2 := w.tailing["agent-rc"]
	if state2 != nil && state2.graceTimer == nil {
		w.tailingMu.Unlock()
		t.Fatal("expected grace timer to be set after refCount hit 0")
	}
	w.tailingMu.Unlock()

	w.Stop()
}

func TestWatcherDiscoverAndTail(t *testing.T) {
	dir := t.TempDir()
	convPath := filepath.Join(dir, "test-conv.jsonl")

	// Write a JSONL line
	line := `{"type":"user","uuid":"u1","timestamp":"2026-02-14T01:44:54.253Z","message":{"role":"user","content":[{"type":"text","text":"hello from discover"}]}}`
	if err := os.WriteFile(convPath, []byte(line+"\n"), 0644); err != nil {
		t.Fatal(err)
	}

	convID := "claude:agent-dt:test-conv"
	disc := &mockDiscoverer{
		files: []ConversationFile{{
			Path:                 convPath,
			NativeConversationID: "test-conv",
			ConversationID:       convID,
			Runtime:              "claude",
		}},
		watchDirs: []string{dir},
	}

	registry, _ := newTestRegistry(t, "agent-dt", "claude", dir)
	w := NewConversationWatcher(registry, 100)
	w.RegisterRuntime("claude", disc, func(agentName, cID string) Parser {
		return NewClaudeParser(agentName, cID)
	})

	agent := agents.Agent{Name: "agent-dt", Runtime: "claude", WorkDir: dir}
	w.recordAgent(agent)

	if err := w.EnsureTailing("agent-dt"); err != nil {
		t.Fatalf("EnsureTailing error: %v", err)
	}

	// Wait for the buffer to be populated (tailing reads file asynchronously)
	deadline := time.Now().Add(5 * time.Second)
	for {
		buf := w.GetBuffer(convID)
		if buf != nil {
			snap := buf.Snapshot(EventFilter{})
			if len(snap) > 0 {
				if snap[0].Type != EventUser {
					t.Fatalf("event type = %q, want %q", snap[0].Type, EventUser)
				}
				break
			}
		}
		if time.Now().After(deadline) {
			t.Fatal("timeout waiting for events to appear in buffer")
		}
		time.Sleep(50 * time.Millisecond)
	}

	w.Stop()
}

func TestWatcherCleanupAgent(t *testing.T) {
	registry, _ := newTestRegistry(t, "agent-ca", "claude", "/tmp/test")
	w := NewConversationWatcher(registry, 100)

	agent := agents.Agent{Name: "agent-ca", Runtime: "claude", WorkDir: "/tmp/test"}
	convID := "claude:agent-ca:conv1"

	// Set up a conversationStream with a cancel func we can track
	cancelled := false
	ctx, cancel := context.WithCancel(context.Background())
	_ = ctx

	w.mu.Lock()
	w.streams[convID] = &conversationStream{
		conversationID: convID,
		agent:          agent,
		buffer:         NewConversationBuffer(convID, "agent-ca", 100),
		files:          make(map[string]*fileStream),
		cancel: func() {
			cancelled = true
			cancel()
		},
	}
	w.activeByAgent["agent-ca"] = convID
	w.convToAgent[convID] = "agent-ca"
	w.mu.Unlock()

	w.cleanupAgent("agent-ca")

	w.mu.RLock()
	_, streamExists := w.streams[convID]
	_, activeExists := w.activeByAgent["agent-ca"]
	_, convAgentExists := w.convToAgent[convID]
	w.mu.RUnlock()

	if streamExists {
		t.Fatal("stream still exists after cleanupAgent")
	}
	if activeExists {
		t.Fatal("activeByAgent still exists after cleanupAgent")
	}
	if convAgentExists {
		t.Fatal("convToAgent still exists after cleanupAgent")
	}
	if !cancelled {
		t.Fatal("stream cancel was not called")
	}

	w.Stop()
}

func TestEmitConversationEventNonBlocking(t *testing.T) {
	registry, _ := newTestRegistry(t, "agent-nb", "claude", "/tmp/test")
	w := NewConversationWatcher(registry, 100)

	// Fill the events channel completely
	for range cap(w.events) {
		w.events <- WatcherEvent{Type: "agent-added"}
	}

	// emitEvent with conversation-event should NOT block (non-blocking send)
	done := make(chan struct{})
	go func() {
		w.emitEvent(WatcherEvent{
			Type:  "conversation-event",
			Event: &ConversationEvent{Type: EventUser},
		})
		close(done)
	}()

	select {
	case <-done:
		// Good — it didn't block
	case <-time.After(time.Second):
		t.Fatal("emitEvent with conversation-event blocked on full channel")
	}

	w.Stop()
}

func TestWatcherCancelTailing(t *testing.T) {
	dir := t.TempDir()
	registry, _ := newTestRegistry(t, "agent-ct", "claude", dir)
	w := NewConversationWatcher(registry, 100)

	disc := &mockDiscoverer{
		files:     nil,
		watchDirs: []string{dir},
	}
	w.RegisterRuntime("claude", disc, func(agentName, convID string) Parser {
		return NewClaudeParser(agentName, convID)
	})

	agent := agents.Agent{Name: "agent-ct", Runtime: "claude", WorkDir: dir}
	w.recordAgent(agent)

	// Start tailing
	if err := w.EnsureTailing("agent-ct"); err != nil {
		t.Fatalf("EnsureTailing error: %v", err)
	}

	// Verify tailing state exists
	w.tailingMu.Lock()
	if _, ok := w.tailing["agent-ct"]; !ok {
		w.tailingMu.Unlock()
		t.Fatal("tailing state missing before cancelTailing")
	}
	w.tailingMu.Unlock()

	// Cancel tailing (as happens when agent is removed)
	w.cancelTailing("agent-ct")

	w.tailingMu.Lock()
	_, exists := w.tailing["agent-ct"]
	w.tailingMu.Unlock()
	if exists {
		t.Fatal("tailing state still exists after cancelTailing")
	}

	// Cancelling nonexistent agent should not panic
	w.cancelTailing("nonexistent")

	w.Stop()
}

func TestWatcherWatchLoop(t *testing.T) {
	registry, ctrl := newTestRegistry(t, "agent-wl", "claude", "/tmp/test")
	w := NewConversationWatcher(registry, 100)
	w.Start()
	drainWatcherEvents(w)

	// Simulate agent removal via registry notification
	ctrl.sessions = nil
	ctrl.notifCh <- tmux.Notification{Type: "sessions-changed"}

	// Wait for the "agent-removed" event to propagate through the watcher
	deadline := time.Now().Add(2 * time.Second)
	gotRemoved := false
	for time.Now().Before(deadline) {
		select {
		case e := <-w.Events():
			if e.Type == "agent-removed" && e.Agent.Name == "agent-wl" {
				gotRemoved = true
			}
		case <-time.After(100 * time.Millisecond):
		}
		if gotRemoved {
			break
		}
	}
	if !gotRemoved {
		t.Fatal("did not receive agent-removed event from watchLoop")
	}

	w.Stop()
}

func TestWatcherWatchLoopUpdated(t *testing.T) {
	registry, ctrl := newTestRegistry(t, "agent-wlu", "claude", "/tmp/test")
	w := NewConversationWatcher(registry, 100)
	w.Start()
	drainWatcherEvents(w)

	// Simulate agent update (attached state change)
	ctrl.sessions = []tmux.SessionInfo{{Name: "agent-wlu", Attached: false}}
	ctrl.notifCh <- tmux.Notification{Type: "sessions-changed"}

	deadline := time.Now().Add(2 * time.Second)
	gotUpdated := false
	for time.Now().Before(deadline) {
		select {
		case e := <-w.Events():
			if e.Type == "agent-updated" && e.Agent.Name == "agent-wlu" {
				gotUpdated = true
			}
		case <-time.After(100 * time.Millisecond):
		}
		if gotUpdated {
			break
		}
	}
	if !gotUpdated {
		t.Fatal("did not receive agent-updated event from watchLoop")
	}

	w.Stop()
}

func TestWatcherReleaseTailingNonExistent(t *testing.T) {
	registry, _ := newTestRegistry(t, "agent-rn", "claude", "/tmp/test")
	w := NewConversationWatcher(registry, 100)

	// Releasing an agent that was never tailed should not panic
	w.ReleaseTailing("never-tailed")

	w.Stop()
}

func TestSelectMainConversationFile_DistributesSameWorkdirAgents(t *testing.T) {
	oldResolver := resolveRuntimeSessionIDFunc
	resolveRuntimeSessionIDFunc = func(_, _ string) string { return "" }
	defer func() { resolveRuntimeSessionIDFunc = oldResolver }()

	ctrl := &mockCtrl{
		sessions: []tmux.SessionInfo{
			{Name: "271", Attached: false},
			{Name: "273", Attached: true},
		},
		panes: map[string]tmux.PaneInfo{
			"271": {Command: "claude", PID: "pid-271", WorkDir: "/tmp/shared"},
			"273": {Command: "claude", PID: "pid-273", WorkDir: "/tmp/shared"},
		},
		notifCh: make(chan tmux.Notification, 10),
	}
	registry := agents.NewRegistry(ctrl, "", nil)
	if err := registry.Start(); err != nil {
		t.Fatalf("registry.Start() error: %v", err)
	}
	t.Cleanup(registry.Stop)
	drainRegistryEvents(registry) // initial added events

	w := NewConversationWatcher(registry, 100)
	defer w.Stop()

	files := []ConversationFile{
		{NativeConversationID: "newest", ConversationID: "claude:agent:newest"},
		{NativeConversationID: "older", ConversationID: "claude:agent:older"},
	}

	agent273, ok := registry.GetAgent("273")
	if !ok {
		t.Fatal("missing agent 273")
	}
	selected273, _ := w.selectMainConversationFile(agent273, files)
	if selected273.NativeConversationID != "newest" {
		t.Fatalf("agent 273 selected %q, want newest", selected273.NativeConversationID)
	}

	agent271, ok := registry.GetAgent("271")
	if !ok {
		t.Fatal("missing agent 271")
	}
	selected271, _ := w.selectMainConversationFile(agent271, files)
	if selected271.NativeConversationID != "older" {
		t.Fatalf("agent 271 selected %q, want older", selected271.NativeConversationID)
	}
}

func TestSelectMainConversationFile_UsesResumeHint(t *testing.T) {
	oldResolver := resolveRuntimeSessionIDFunc
	resolveRuntimeSessionIDFunc = func(runtime, pid string) string {
		if runtime == "claude" && pid == "pid-57" {
			return "26a96967-588d-4c9b-a1b2-5b4eb8af29fd"
		}
		return ""
	}
	defer func() { resolveRuntimeSessionIDFunc = oldResolver }()

	ctrl := &mockCtrl{
		sessions: []tmux.SessionInfo{{Name: "57", Attached: false}},
		panes: map[string]tmux.PaneInfo{
			"57": {Command: "claude", PID: "pid-57", WorkDir: "/tmp/web3"},
		},
		notifCh: make(chan tmux.Notification, 10),
	}
	registry := agents.NewRegistry(ctrl, "", nil)
	if err := registry.Start(); err != nil {
		t.Fatalf("registry.Start() error: %v", err)
	}
	t.Cleanup(registry.Stop)
	drainRegistryEvents(registry) // initial added event

	w := NewConversationWatcher(registry, 100)
	defer w.Stop()

	files := []ConversationFile{
		{NativeConversationID: "c1b3f9cf-c93f-4865-833e-6c7b1aea1e78", ConversationID: "claude:57:c1b3"},
		{NativeConversationID: "26a96967-588d-4c9b-a1b2-5b4eb8af29fd", ConversationID: "claude:57:26a9"},
	}

	agent57, ok := registry.GetAgent("57")
	if !ok {
		t.Fatal("missing agent 57")
	}
	selected, _ := w.selectMainConversationFile(agent57, files)
	if selected.NativeConversationID != "26a96967-588d-4c9b-a1b2-5b4eb8af29fd" {
		t.Fatalf("selected %q, want resume-matched ID", selected.NativeConversationID)
	}
}

func TestShouldRediscoverForCreate(t *testing.T) {
	tests := []struct {
		name    string
		runtime string
		path    string
		want    bool
	}{
		{
			name:    "claude jsonl",
			runtime: "claude",
			path:    "/tmp/claude/abc.jsonl",
			want:    true,
		},
		{
			name:    "codex jsonl",
			runtime: "codex",
			path:    "/tmp/codex/rollout-123.jsonl",
			want:    true,
		},
		{
			name:    "gemini session json",
			runtime: "gemini",
			path:    "/tmp/gemini/chats/session-2026-02-17T04-27-1234.json",
			want:    true,
		},
		{
			name:    "gemini non session json",
			runtime: "gemini",
			path:    "/tmp/gemini/logs.json",
			want:    false,
		},
		{
			name:    "gemini jsonl",
			runtime: "gemini",
			path:    "/tmp/gemini/chats/session-foo.jsonl",
			want:    false,
		},
		{
			name:    "unknown json",
			runtime: "other",
			path:    "/tmp/other/conv.json",
			want:    true,
		},
		{
			name:    "unknown jsonl",
			runtime: "other",
			path:    "/tmp/other/conv.jsonl",
			want:    true,
		},
		{
			name:    "unknown txt",
			runtime: "other",
			path:    "/tmp/other/conv.txt",
			want:    false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := shouldRediscoverForCreate(tt.runtime, tt.path)
			if got != tt.want {
				t.Fatalf("shouldRediscoverForCreate(%q, %q) = %v, want %v", tt.runtime, tt.path, got, tt.want)
			}
		})
	}
}

// drainWatcherEvents drains all buffered events from a watcher's event channel.
func drainWatcherEvents(w *ConversationWatcher) {
	for {
		select {
		case <-w.Events():
		default:
			return
		}
	}
}

func drainRegistryEvents(r *agents.Registry) {
	for {
		select {
		case <-r.Events():
		default:
			return
		}
	}
}
