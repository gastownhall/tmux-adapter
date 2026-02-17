package agents

import (
	"log"
	"slices"
	"strings"
	"sync"
)

// RegistryEvent represents a change in agent state.
type RegistryEvent struct {
	Type  string // "added", "removed", "updated"
	Agent Agent
}

// Registry tracks live agents and emits lifecycle events.
type Registry struct {
	ctrl          ControlModeInterface
	mu            sync.RWMutex
	agents        map[string]Agent // name -> agent
	events        chan RegistryEvent
	workDirFilter string
	skipSessions  []string
	stopCh        chan struct{}
	stopOnce      sync.Once
}

// NewRegistry creates a new agent registry.
// workDirFilter restricts detection to agents with matching workdir prefix (empty = all).
// skipSessions lists tmux session names to ignore during scanning (e.g., monitor sessions).
func NewRegistry(ctrl ControlModeInterface, workDirFilter string, skipSessions []string) *Registry {
	return &Registry{
		ctrl:          ctrl,
		agents:        make(map[string]Agent),
		events:        make(chan RegistryEvent, 100),
		workDirFilter: workDirFilter,
		skipSessions:  skipSessions,
		stopCh:        make(chan struct{}),
	}
}

// Start begins watching for agent changes.
func (r *Registry) Start() error {
	// Initial scan
	if err := r.scan(); err != nil {
		return err
	}

	// Watch for tmux notifications
	go r.watchLoop()
	return nil
}

// Stop halts the registry watcher. Safe to call multiple times.
func (r *Registry) Stop() {
	r.stopOnce.Do(func() {
		close(r.stopCh)
	})
}

// Events returns the channel for receiving lifecycle events.
func (r *Registry) Events() <-chan RegistryEvent {
	return r.events
}

// GetAgents returns a snapshot of all currently known agents.
func (r *Registry) GetAgents() []Agent {
	r.mu.RLock()
	defer r.mu.RUnlock()

	result := make([]Agent, 0, len(r.agents))
	for _, a := range r.agents {
		result = append(result, a)
	}
	return result
}

// GetAgent looks up a single agent by name.
func (r *Registry) GetAgent(name string) (Agent, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	a, ok := r.agents[name]
	return a, ok
}

// Count returns the number of currently tracked agents.
func (r *Registry) Count() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.agents)
}

func (r *Registry) shouldSkip(sessionName string) bool {
	return slices.Contains(r.skipSessions, sessionName)
}

func (r *Registry) watchLoop() {
	for {
		select {
		case <-r.stopCh:
			return
		case notif, ok := <-r.ctrl.Notifications():
			if !ok {
				return // notifications channel closed
			}
			switch notif.Type {
			case "sessions-changed", "window-renamed":
				// sessions-changed: session created/destroyed
				// window-renamed: agent set terminal title (e.g., Claude Code -> "2.1.42")
				if err := r.scan(); err != nil {
					log.Printf("agent scan error: %v", err)
				}
			}
		}
	}
}

func (r *Registry) scan() error {
	sessions, err := r.ctrl.ListSessions()
	if err != nil {
		return err
	}

	// Build new agent map from current tmux state
	discovered := make(map[string]Agent)

	for _, sess := range sessions {
		// Skip monitor sessions (e.g., adapter-monitor, converter-monitor)
		if r.shouldSkip(sess.Name) {
			continue
		}

		// Get pane info for process detection and workDir
		pane, err := r.ctrl.GetPaneInfo(sess.Name)
		if err != nil {
			log.Printf("pane info for %s: %v", sess.Name, err)
			continue
		}

		// Full 3-tier detection across all known runtimes
		runtime := DetectRuntime(pane.Command, pane.PID)
		if runtime == "" {
			continue // no known agent process found
		}

		// Optional workdir filter
		if r.workDirFilter != "" && pane.WorkDir != r.workDirFilter && !strings.HasPrefix(pane.WorkDir, r.workDirFilter+"/") {
			continue
		}

		discovered[sess.Name] = Agent{
			Name:        sess.Name,
			Runtime:     runtime,
			WorkDir:     pane.WorkDir,
			Attached:    sess.Attached,
			PanePID:     pane.PID,
			PaneCommand: pane.Command,
		}
	}

	// Diff against known agents
	r.mu.Lock()
	var pendingEvents []RegistryEvent

	// Find removed agents
	for name, oldAgent := range r.agents {
		if _, exists := discovered[name]; !exists {
			delete(r.agents, name)
			pendingEvents = append(pendingEvents, RegistryEvent{Type: "removed", Agent: oldAgent})
		}
	}

	// Find added and updated agents
	for name, newAgent := range discovered {
		oldAgent, existed := r.agents[name]
		if !existed {
			r.agents[name] = newAgent
			pendingEvents = append(pendingEvents, RegistryEvent{Type: "added", Agent: newAgent})
		} else if oldAgent.Attached != newAgent.Attached ||
			oldAgent.Runtime != newAgent.Runtime ||
			oldAgent.WorkDir != newAgent.WorkDir ||
			oldAgent.PanePID != newAgent.PanePID ||
			oldAgent.PaneCommand != newAgent.PaneCommand {
			r.agents[name] = newAgent
			pendingEvents = append(pendingEvents, RegistryEvent{Type: "updated", Agent: newAgent})
		}
	}
	r.mu.Unlock()

	// Send events outside the lock to avoid deadlocking GetAgents() callers
	for _, event := range pendingEvents {
		select {
		case r.events <- event:
		default:
			log.Printf("registry: dropping event %s for agent %s (channel full)", event.Type, event.Agent.Name)
		}
	}

	return nil
}
