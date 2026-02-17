package conv

import (
	"context"
	"fmt"
	"log"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"
	"github.com/gastownhall/tmux-adapter/internal/agents"
)

// WatcherEvent represents a lifecycle or conversation event from the watcher.
type WatcherEvent struct {
	Type      string             // "agent-added", "agent-removed", "agent-updated", "conversation-started", "conversation-switched", "conversation-event"
	Agent     *agents.Agent      // for lifecycle events
	Event     *ConversationEvent // for conversation events
	OldConvID string             // for conversation-switched events
	NewConvID string             // for conversation-started and conversation-switched events
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

// tailingState tracks the ref-counted tailing state for a single agent.
type tailingState struct {
	refCount   int
	cancelFunc context.CancelFunc
	graceTimer *time.Timer
}

// resolveRuntimeSessionIDFunc is replaceable in tests.
var resolveRuntimeSessionIDFunc = resolveRuntimeSessionID

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

	// Lazy tailing: ref-counted per-agent tailing state
	tailing     map[string]*tailingState // agentName → tailing state
	tailingMu   sync.Mutex
	convToAgent map[string]string       // conversationID → agentName
	knownAgents map[string]agents.Agent // agentName → agent info (from registry events)
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
		tailing:       make(map[string]*tailingState),
		convToAgent:   make(map[string]string),
		knownAgents:   make(map[string]agents.Agent),
	}
}

// RegisterRuntime registers a discoverer and parser factory for a runtime.
func (w *ConversationWatcher) RegisterRuntime(runtime string, disc Discoverer, factory func(agentName, convID string) Parser) {
	w.discoverers[runtime] = disc
	w.parserFactory[runtime] = factory
}

// HasDiscoverer returns true if a discoverer is registered for the given runtime.
func (w *ConversationWatcher) HasDiscoverer(runtime string) bool {
	_, ok := w.discoverers[runtime]
	return ok
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

// GetAgentForConversation returns the agent name for a conversation ID,
// or "" if the mapping is not known.
func (w *ConversationWatcher) GetAgentForConversation(convID string) string {
	w.mu.RLock()
	defer w.mu.RUnlock()
	return w.convToAgent[convID]
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

// EnsureTailing starts tailing for an agent if not already active.
// Increments the reference count. Returns error if the agent is not in the registry.
func (w *ConversationWatcher) EnsureTailing(agentName string) error {
	w.tailingMu.Lock()

	if state, ok := w.tailing[agentName]; ok {
		if state.graceTimer != nil {
			state.graceTimer.Stop()
			state.graceTimer = nil
		}
		state.refCount++
		w.tailingMu.Unlock()
		return nil
	}

	// Agent must be known in the registry
	agent, ok := w.registry.GetAgent(agentName)
	if !ok {
		w.tailingMu.Unlock()
		return fmt.Errorf("agent %q not found", agentName)
	}

	ctx, cancelFn := context.WithCancel(w.ctx)
	state := &tailingState{
		refCount:   1,
		cancelFunc: cancelFn,
	}
	w.tailing[agentName] = state
	w.tailingMu.Unlock()

	// Start discovery and tailing in background.
	// discoverAndTail propagates this per-agent ctx to startConversationStream
	// and retryDiscovery, so cancelling tailing cancels all streams for this agent.
	go w.discoverAndTail(ctx, agent)
	return nil
}

// ReleaseTailing decrements the reference count for an agent.
// If count reaches 0, stops tailing after a 30-second grace period.
func (w *ConversationWatcher) ReleaseTailing(agentName string) {
	w.tailingMu.Lock()
	state := w.tailing[agentName]
	if state == nil {
		w.tailingMu.Unlock()
		return
	}

	state.refCount--
	if state.refCount <= 0 {
		state.graceTimer = time.AfterFunc(30*time.Second, func() {
			w.tailingMu.Lock()
			// Re-check refCount: this is the critical safety check.
			// time.Timer.Stop() in EnsureTailing is best-effort; this
			// re-check is the actual correctness guarantee.
			if state.refCount > 0 {
				w.tailingMu.Unlock()
				return
			}
			state.cancelFunc()
			delete(w.tailing, agentName)
			w.tailingMu.Unlock()
			// Clean up streams OUTSIDE tailingMu to respect lock ordering:
			// watcher.mu → tailingMu (never reverse).
			w.cleanupAgent(agentName)
		})
	}
	w.tailingMu.Unlock()
}

// Start begins watching for agent changes. Agents are recorded but NOT
// eagerly tailed — tailing starts on demand via EnsureTailing.
func (w *ConversationWatcher) Start() {
	for _, agent := range w.registry.GetAgents() {
		w.recordAgent(agent)
		w.emitEvent(WatcherEvent{Type: "agent-added", Agent: &agent})
	}

	go w.watchLoop()
}

// Stop shuts down the watcher and all tailers.
func (w *ConversationWatcher) Stop() {
	w.cancel()

	w.mu.Lock()
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
	w.mu.Unlock()

	// Clean up tailing state: stop all grace timers and cancel all per-agent contexts
	w.tailingMu.Lock()
	for _, state := range w.tailing {
		if state.graceTimer != nil {
			state.graceTimer.Stop()
		}
		state.cancelFunc()
	}
	w.tailing = make(map[string]*tailingState)
	w.tailingMu.Unlock()
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
				w.recordAgent(event.Agent)
				w.emitEvent(WatcherEvent{Type: "agent-added", Agent: &event.Agent})
			case "removed":
				w.cleanupAgent(event.Agent.Name)
				w.cancelTailing(event.Agent.Name)
				w.mu.Lock()
				delete(w.knownAgents, event.Agent.Name)
				w.mu.Unlock()
				w.emitEvent(WatcherEvent{Type: "agent-removed", Agent: &event.Agent})
			case "updated":
				w.recordAgent(event.Agent)
				w.emitEvent(WatcherEvent{Type: "agent-updated", Agent: &event.Agent})
			}
		}
	}
}

// recordAgent stores agent info for bookkeeping. Does NOT start tailing.
func (w *ConversationWatcher) recordAgent(agent agents.Agent) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.knownAgents[agent.Name] = agent
}

// cleanupAgent stops ALL conversation streams and directory watchers for an agent.
// This iterates all streams (not just the active one), fixing the pre-existing
// subagent stream leak where only activeByAgent was checked.
func (w *ConversationWatcher) cleanupAgent(agentName string) {
	w.mu.Lock()

	// Find all streams belonging to this agent (including subagent streams)
	var toClean []*conversationStream
	var convIDs []string
	for convID, s := range w.streams {
		if s.agent.Name == agentName {
			toClean = append(toClean, s)
			convIDs = append(convIDs, convID)
		}
	}

	for _, convID := range convIDs {
		delete(w.streams, convID)
		delete(w.convToAgent, convID)
	}
	delete(w.activeByAgent, agentName)

	// Clean up directory watcher
	if dw, ok := w.dirWatchers[agentName]; ok {
		if err := dw.Close(); err != nil {
			log.Printf("watcher: failed to close dir watcher for %s: %v", agentName, err)
		}
		delete(w.dirWatchers, agentName)
	}
	w.mu.Unlock()

	// Stop streams outside the lock
	for _, s := range toClean {
		s.cancel()
		for _, fs := range s.files {
			fs.tailer.Stop()
		}
	}
}

// cancelTailing clears tailing state for an agent regardless of refCount.
// Used when an agent is removed from the registry.
func (w *ConversationWatcher) cancelTailing(agentName string) {
	w.tailingMu.Lock()
	state := w.tailing[agentName]
	if state == nil {
		w.tailingMu.Unlock()
		return
	}
	if state.graceTimer != nil {
		state.graceTimer.Stop()
	}
	state.cancelFunc()
	delete(w.tailing, agentName)
	w.tailingMu.Unlock()
}

func (w *ConversationWatcher) discoverAndTail(ctx context.Context, agent agents.Agent) {
	disc, ok := w.discoverers[agent.Runtime]
	if !ok {
		log.Printf("watcher: no conversation parser for runtime %q, agent %q — lifecycle events only", agent.Runtime, agent.Name)
		return
	}

	result, err := disc.FindConversations(agent.Name, agent.WorkDir)
	if err != nil {
		log.Printf("watcher: discovery error for %s: %v", agent.Name, err)
		return
	}

	// Watch directories for conversation rotation (uses per-agent ctx)
	w.watchDirectories(ctx, agent.Name, result.WatchDirs)

	if len(result.Files) == 0 {
		log.Printf("watcher: no conversation files found for %s, watching directories", agent.Name)
		go w.retryDiscovery(ctx, agent)
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
		// Prefer an explicit runtime session hint (e.g. --resume ID) when available.
		// Otherwise deterministically assign unique files across agents sharing the
		// same runtime + workdir so they don't collapse onto the newest file.
		currentFile, _ := w.selectMainConversationFile(agent, mainFiles)
		w.startConversationStream(ctx, agent, currentFile)
	}

	// Also start subagent streams
	for _, f := range result.Files {
		if f.IsSubagent {
			w.startConversationStream(ctx, agent, f)
		}
	}
}

// selectMainConversationFile picks the active main conversation file for an agent.
// Order of preference:
// 1) Explicit runtime session hint (e.g. claude --resume <id>)
// 2) Deterministic unique assignment among peers with same runtime+workdir
// 3) Newest file fallback
func (w *ConversationWatcher) selectMainConversationFile(agent agents.Agent, mainFiles []ConversationFile) (ConversationFile, bool) {
	if len(mainFiles) == 0 {
		return ConversationFile{}, false
	}

	filesByNativeID := make(map[string]ConversationFile, len(mainFiles))
	for _, f := range mainFiles {
		filesByNativeID[f.NativeConversationID] = f
	}

	// First choice: explicit runtime resume hint from process args.
	if sessionID := resolveRuntimeSessionIDFunc(agent.Runtime, agent.PanePID); sessionID != "" {
		if f, ok := filesByNativeID[sessionID]; ok {
			return f, true
		}
	}

	// Second choice: distribute files deterministically among peers sharing
	// runtime+workdir so each session gets a distinct conversation when possible.
	allAgents := w.registry.GetAgents()
	peers := make([]agents.Agent, 0, len(allAgents))
	for _, a := range allAgents {
		if a.Runtime == agent.Runtime && a.WorkDir == agent.WorkDir {
			peers = append(peers, a)
		}
	}
	if len(peers) <= 1 {
		return mainFiles[0], true
	}

	claimed := make(map[string]bool)
	unresolved := make([]agents.Agent, 0, len(peers))

	for _, peer := range peers {
		sessionID := resolveRuntimeSessionIDFunc(peer.Runtime, peer.PanePID)
		if sessionID != "" {
			if f, ok := filesByNativeID[sessionID]; ok {
				if peer.Name == agent.Name {
					return f, true
				}
				claimed[f.NativeConversationID] = true
				continue
			}
		}
		unresolved = append(unresolved, peer)
	}

	available := make([]ConversationFile, 0, len(mainFiles))
	for _, f := range mainFiles {
		if !claimed[f.NativeConversationID] {
			available = append(available, f)
		}
	}
	if len(available) == 0 {
		return mainFiles[0], true
	}

	sort.Slice(unresolved, func(i, j int) bool {
		if unresolved[i].Attached != unresolved[j].Attached {
			return unresolved[i].Attached // attached first
		}
		return unresolved[i].Name < unresolved[j].Name
	})

	agentIndex := -1
	for i, peer := range unresolved {
		if peer.Name == agent.Name {
			agentIndex = i
			break
		}
	}
	if agentIndex >= 0 && agentIndex < len(available) {
		return available[agentIndex], true
	}

	return available[0], true
}

func (w *ConversationWatcher) startConversationStream(ctx context.Context, agent agents.Agent, file ConversationFile) {
	factory, ok := w.parserFactory[file.Runtime]
	if !ok {
		return
	}

	// Derive stream context from per-agent ctx (NOT w.ctx) so cancelling
	// the agent's tailing cancels all its streams.
	streamCtx, streamCancel := context.WithCancel(ctx)

	tailer, err := NewTailer(streamCtx, file.Path, true)
	if err != nil {
		log.Printf("watcher: tailer error for %s: %v", file.Path, err)
		streamCancel()
		return
	}

	parser := factory(agent.Name, file.ConversationID)
	buffer := NewConversationBuffer(file.ConversationID, agent.Name, w.bufferSize)

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
	w.convToAgent[file.ConversationID] = agent.Name

	if !file.IsSubagent {
		oldConvID := w.activeByAgent[agent.Name]
		w.activeByAgent[agent.Name] = file.ConversationID

		// Clean up orphaned stream and convToAgent entry from the previous active conversation
		if oldConvID != "" && oldConvID != file.ConversationID {
			if oldStream, ok := w.streams[oldConvID]; ok {
				oldStream.cancel()
				for _, efs := range oldStream.files {
					efs.tailer.Stop()
				}
				delete(w.streams, oldConvID)
			}
			delete(w.convToAgent, oldConvID)
		}
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

	// Start parsing goroutine for live updates only (file history already in buffer)
	go w.pumpFileStream(stream, fs)
}

func (w *ConversationWatcher) pumpFileStream(stream *conversationStream, fs *fileStream) {
	for line := range fs.tailer.Lines() {
		if line == nil {
			// Sentinel from tailer: initial file read is complete.
			stream.buffer.MarkHistoryDone()
			continue
		}
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

func (w *ConversationWatcher) watchDirectories(ctx context.Context, agentName string, dirs []string) {
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		log.Printf("watcher: dir watcher error for %s: %v", agentName, err)
		return
	}

	for _, dir := range dirs {
		if err := os.MkdirAll(dir, 0755); err != nil {
			log.Printf("watcher: failed to create watch directory %s: %v", dir, err)
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

	go w.watchDirectoryLoop(ctx, agentName, watcher)
}

func (w *ConversationWatcher) watchDirectoryLoop(ctx context.Context, agentName string, watcher *fsnotify.Watcher) {
	for {
		select {
		case <-ctx.Done():
			return
		case event, ok := <-watcher.Events:
			if !ok {
				return
			}
			if event.Has(fsnotify.Create) && strings.HasSuffix(event.Name, ".jsonl") {
				// New conversation file detected — re-discover using latest agent info
				agent, agentOk := w.registry.GetAgent(agentName)
				if agentOk {
					go w.discoverAndTail(ctx, agent)
				}
			}
		case err, ok := <-watcher.Errors:
			if !ok {
				return
			}
			log.Printf("watcher: fsnotify error for %s: %v", agentName, err)
		}
	}
}

func (w *ConversationWatcher) retryDiscovery(ctx context.Context, agent agents.Agent) {
	timer := time.NewTimer(5 * time.Second)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return
	case <-timer.C:
		w.discoverAndTail(ctx, agent)
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
