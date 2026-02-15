package conv

import (
	"bufio"
	"bytes"
	"context"
	"log"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"
	"github.com/gastownhall/tmux-adapter/internal/agents"
)

// WatcherEvent represents a lifecycle or conversation event from the watcher.
type WatcherEvent struct {
	Type      string              // "agent-added", "agent-removed", "agent-updated", "conversation-started", "conversation-switched", "conversation-event"
	Agent     *agents.Agent       // for lifecycle events
	Event     *ConversationEvent  // for conversation events
	OldConvID string              // for conversation-switched events
	NewConvID string              // for conversation-started and conversation-switched events
}

type fileStream struct {
	path   string
	tailer *Tailer
	parser Parser
}

type conversationStream struct {
	conversationID string
	agent          agents.Agent
	files          map[string]*fileStream
	buffer         *ConversationBuffer
	cancel         context.CancelFunc
}

// ConversationWatcher orchestrates discovery, tailing, and parsing for all active agents.
type ConversationWatcher struct {
	registry      *agents.Registry
	discoverers   map[string]Discoverer
	parserFactory map[string]func(agentName, convID string) Parser
	streams       map[string]*conversationStream // keyed by conversation ID
	activeByAgent map[string]string              // agent name → active conversation ID
	events        chan WatcherEvent
	bufferSize    int
	mu            sync.RWMutex
	ctx           context.Context
	cancel        context.CancelFunc

	// Directory watchers for conversation rotation
	dirWatchers map[string]*fsnotify.Watcher // agent name → directory watcher
}

// NewConversationWatcher creates a new watcher.
func NewConversationWatcher(registry *agents.Registry, bufferSize int) *ConversationWatcher {
	if bufferSize <= 0 {
		bufferSize = 1000
	}
	ctx, cancel := context.WithCancel(context.Background())
	return &ConversationWatcher{
		registry:      registry,
		discoverers:   make(map[string]Discoverer),
		parserFactory: make(map[string]func(agentName, convID string) Parser),
		streams:       make(map[string]*conversationStream),
		activeByAgent: make(map[string]string),
		events:        make(chan WatcherEvent, 256),
		bufferSize:    bufferSize,
		ctx:           ctx,
		cancel:        cancel,
		dirWatchers:   make(map[string]*fsnotify.Watcher),
	}
}

// RegisterRuntime registers a discoverer and parser factory for a runtime.
func (w *ConversationWatcher) RegisterRuntime(runtime string, disc Discoverer, factory func(agentName, convID string) Parser) {
	w.discoverers[runtime] = disc
	w.parserFactory[runtime] = factory
}

// Events returns the channel for receiving watcher events.
func (w *ConversationWatcher) Events() <-chan WatcherEvent {
	return w.events
}

// GetBuffer returns the conversation buffer for a given conversation ID.
func (w *ConversationWatcher) GetBuffer(conversationID string) *ConversationBuffer {
	w.mu.RLock()
	defer w.mu.RUnlock()
	if s, ok := w.streams[conversationID]; ok {
		return s.buffer
	}
	return nil
}

// GetActiveConversation returns the active conversation ID for an agent.
func (w *ConversationWatcher) GetActiveConversation(agentName string) string {
	w.mu.RLock()
	defer w.mu.RUnlock()
	return w.activeByAgent[agentName]
}

// ListAgents returns all agents from the registry.
func (w *ConversationWatcher) ListAgents() []agents.Agent {
	return w.registry.GetAgents()
}

// ListConversations returns metadata about all active conversations.
func (w *ConversationWatcher) ListConversations() []ConversationInfo {
	w.mu.RLock()
	defer w.mu.RUnlock()
	var result []ConversationInfo
	for _, s := range w.streams {
		result = append(result, ConversationInfo{
			ConversationID: s.conversationID,
			AgentName:      s.agent.Name,
			Runtime:        s.agent.Runtime,
		})
	}
	return result
}

// ConversationInfo is metadata about an active conversation.
type ConversationInfo struct {
	ConversationID string `json:"conversationId"`
	AgentName      string `json:"agentName"`
	Runtime        string `json:"runtime"`
}

// Start begins watching for agent changes and starts tailing conversations.
func (w *ConversationWatcher) Start() {
	// Process initial agents
	for _, agent := range w.registry.GetAgents() {
		w.emitEvent(WatcherEvent{Type: "agent-added", Agent: &agent})
		w.startWatching(agent)
	}

	go w.watchLoop()
}

// Stop shuts down the watcher and all tailers.
func (w *ConversationWatcher) Stop() {
	w.cancel()

	w.mu.Lock()
	defer w.mu.Unlock()

	for _, s := range w.streams {
		s.cancel()
		for _, fs := range s.files {
			fs.tailer.Stop()
		}
	}
	for name, dw := range w.dirWatchers {
		if err := dw.Close(); err != nil {
			log.Printf("watcher: failed to close dir watcher for %s: %v", name, err)
		}
	}
}

func (w *ConversationWatcher) watchLoop() {
	for {
		select {
		case <-w.ctx.Done():
			return
		case event, ok := <-w.registry.Events():
			if !ok {
				return
			}
			switch event.Type {
			case "added":
				w.emitEvent(WatcherEvent{Type: "agent-added", Agent: &event.Agent})
				w.startWatching(event.Agent)
			case "removed":
				w.stopWatching(event.Agent.Name)
				w.emitEvent(WatcherEvent{Type: "agent-removed", Agent: &event.Agent})
			case "updated":
				w.emitEvent(WatcherEvent{Type: "agent-updated", Agent: &event.Agent})
			}
		}
	}
}

func (w *ConversationWatcher) startWatching(agent agents.Agent) {
	disc, ok := w.discoverers[agent.Runtime]
	if !ok {
		log.Printf("watcher: no conversation parser for runtime %q, agent %q — lifecycle events only", agent.Runtime, agent.Name)
		return
	}

	// Non-blocking: spawn goroutine for discovery
	go w.discoverAndTail(agent, disc)
}

func (w *ConversationWatcher) discoverAndTail(agent agents.Agent, disc Discoverer) {
	result, err := disc.FindConversations(agent.Name, agent.WorkDir)
	if err != nil {
		log.Printf("watcher: discovery error for %s: %v", agent.Name, err)
		return
	}

	// Watch directories for conversation rotation
	w.watchDirectories(agent.Name, result.WatchDirs)

	if len(result.Files) == 0 {
		log.Printf("watcher: no conversation files found for %s, watching directories", agent.Name)
		go w.retryDiscovery(agent, disc)
		return
	}

	// Separate non-subagent and subagent files.
	// Discovery returns files sorted by mtime descending (most recent first).
	var mainFiles []ConversationFile
	for _, f := range result.Files {
		if !f.IsSubagent {
			mainFiles = append(mainFiles, f)
		}
	}

	if len(mainFiles) > 0 {
		// Most recent file is first — it becomes the active conversation
		currentFile := mainFiles[0]

		// Load historical events from ALL older files (oldest-first for chronological order).
		// All events get tagged with the current file's ConversationID so the buffer is unified.
		var history []ConversationEvent
		for i := len(mainFiles) - 1; i >= 1; i-- {
			events := w.loadHistoricalFile(agent, mainFiles[i], currentFile.ConversationID)
			history = append(history, events...)
		}

		w.startConversationStream(agent, currentFile, history)
	}

	// Also start subagent streams
	for _, f := range result.Files {
		if f.IsSubagent {
			w.startConversationStream(agent, f, nil)
		}
	}
}

func (w *ConversationWatcher) startConversationStream(agent agents.Agent, file ConversationFile, history []ConversationEvent) {
	factory, ok := w.parserFactory[file.Runtime]
	if !ok {
		return
	}

	streamCtx, streamCancel := context.WithCancel(w.ctx)

	tailer, err := NewTailer(streamCtx, file.Path, true)
	if err != nil {
		log.Printf("watcher: tailer error for %s: %v", file.Path, err)
		streamCancel()
		return
	}

	parser := factory(agent.Name, file.ConversationID)
	buffer := NewConversationBuffer(file.ConversationID, agent.Name, w.bufferSize)

	// Pre-load historical events from older conversation files
	for _, event := range history {
		buffer.Append(event)
	}
	if len(history) > 0 {
		log.Printf("watcher: loaded %d historical events for %s", len(history), agent.Name)
	}

	fs := &fileStream{
		path:   file.Path,
		tailer: tailer,
		parser: parser,
	}

	stream := &conversationStream{
		conversationID: file.ConversationID,
		agent:          agent,
		files:          map[string]*fileStream{file.Path: fs},
		buffer:         buffer,
		cancel:         streamCancel,
	}

	w.mu.Lock()
	// Clean up any existing stream for this conversation ID (prevents goroutine/FD leaks on re-discovery)
	if existing, ok := w.streams[file.ConversationID]; ok {
		existing.cancel()
		for _, efs := range existing.files {
			efs.tailer.Stop()
		}
	}
	w.streams[file.ConversationID] = stream
	if !file.IsSubagent {
		oldConvID := w.activeByAgent[agent.Name]
		w.activeByAgent[agent.Name] = file.ConversationID
		w.mu.Unlock()

		if oldConvID != "" && oldConvID != file.ConversationID {
			w.emitEvent(WatcherEvent{
				Type:      "conversation-switched",
				Agent:     &agent,
				OldConvID: oldConvID,
				NewConvID: file.ConversationID,
			})
		} else {
			w.emitEvent(WatcherEvent{
				Type:      "conversation-started",
				Agent:     &agent,
				NewConvID: file.ConversationID,
			})
		}
	} else {
		w.mu.Unlock()
	}

	// Start parsing goroutine
	go w.pumpFileStream(stream, fs)
}

func (w *ConversationWatcher) pumpFileStream(stream *conversationStream, fs *fileStream) {
	for line := range fs.tailer.Lines() {
		events, err := fs.parser.Parse(line)
		if err != nil {
			log.Printf("watcher: parse error for %s: %v", fs.path, err)
			continue
		}
		for _, event := range events {
			stream.buffer.Append(event)
			w.emitEvent(WatcherEvent{
				Type:  "conversation-event",
				Event: &event,
			})
		}
	}
}

// loadHistoricalFile reads an entire conversation file and parses all events.
// Events are tagged with activeConvID so they merge cleanly into the active buffer.
func (w *ConversationWatcher) loadHistoricalFile(agent agents.Agent, file ConversationFile, activeConvID string) []ConversationEvent {
	factory, ok := w.parserFactory[file.Runtime]
	if !ok {
		return nil
	}

	data, err := os.ReadFile(file.Path)
	if err != nil {
		log.Printf("watcher: failed to read historical file %s: %v", file.Path, err)
		return nil
	}

	parser := factory(agent.Name, activeConvID)
	var events []ConversationEvent
	scanner := bufio.NewScanner(bytes.NewReader(data))
	scanner.Buffer(make([]byte, 2*1024*1024), 2*1024*1024)
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		parsed, parseErr := parser.Parse(line)
		if parseErr != nil {
			continue
		}
		events = append(events, parsed...)
	}
	return events
}

func (w *ConversationWatcher) stopWatching(agentName string) {
	w.mu.Lock()
	convID, ok := w.activeByAgent[agentName]
	if !ok {
		w.mu.Unlock()
		return
	}
	delete(w.activeByAgent, agentName)

	stream, streamOk := w.streams[convID]
	if streamOk {
		delete(w.streams, convID)
	}
	w.mu.Unlock()

	if streamOk {
		stream.cancel()
		for _, fs := range stream.files {
			fs.tailer.Stop()
		}
	}

	// Clean up directory watcher
	w.mu.Lock()
	if dw, ok := w.dirWatchers[agentName]; ok {
		if err := dw.Close(); err != nil {
			log.Printf("watcher: failed to close dir watcher for %s: %v", agentName, err)
		}
		delete(w.dirWatchers, agentName)
	}
	w.mu.Unlock()
}

func (w *ConversationWatcher) watchDirectories(agentName string, dirs []string) {
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		log.Printf("watcher: dir watcher error for %s: %v", agentName, err)
		return
	}

	for _, dir := range dirs {
		if err := os.MkdirAll(dir, 0755); err != nil {
			continue
		}
		if err := watcher.Add(dir); err != nil {
			log.Printf("watcher: failed to watch dir %s for %s: %v", dir, agentName, err)
		}
	}

	w.mu.Lock()
	if old, ok := w.dirWatchers[agentName]; ok {
		if err := old.Close(); err != nil {
			log.Printf("watcher: failed to close old dir watcher for %s: %v", agentName, err)
		}
	}
	w.dirWatchers[agentName] = watcher
	w.mu.Unlock()

	go w.watchDirectoryLoop(agentName, watcher)
}

func (w *ConversationWatcher) watchDirectoryLoop(agentName string, watcher *fsnotify.Watcher) {
	for {
		select {
		case <-w.ctx.Done():
			return
		case event, ok := <-watcher.Events:
			if !ok {
				return
			}
			if event.Has(fsnotify.Create) && strings.HasSuffix(event.Name, ".jsonl") {
				// New conversation file detected — re-discover
				w.mu.RLock()
				agent, agentOk := w.findAgentByName(agentName)
				w.mu.RUnlock()
				if agentOk {
					disc, discOk := w.discoverers[agent.Runtime]
					if discOk {
						go w.discoverAndTail(agent, disc)
					}
				}
			}
		case _, ok := <-watcher.Errors:
			if !ok {
				return
			}
		}
	}
}

func (w *ConversationWatcher) findAgentByName(name string) (agents.Agent, bool) {
	for _, a := range w.registry.GetAgents() {
		if a.Name == name {
			return a, true
		}
	}
	return agents.Agent{}, false
}

func (w *ConversationWatcher) retryDiscovery(agent agents.Agent, disc Discoverer) {
	timer := time.NewTimer(5 * time.Second)
	defer timer.Stop()
	select {
	case <-w.ctx.Done():
		return
	case <-timer.C:
		w.discoverAndTail(agent, disc)
	}
}

func (w *ConversationWatcher) emitEvent(event WatcherEvent) {
	switch event.Type {
	case "conversation-event":
		// High-volume — non-blocking send, OK to drop (buffer retains events)
		select {
		case w.events <- event:
		default:
			log.Printf("watcher: dropped conversation-event (channel full)")
		}
	default:
		// Lifecycle events (agent-added/removed/updated, conversation-started/switched)
		// are rare and critical — block until delivered or context cancelled
		select {
		case w.events <- event:
		case <-w.ctx.Done():
		}
	}
}
