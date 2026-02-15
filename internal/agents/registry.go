package agents

import (
	"log"
	"slices"
	"strings"
	"sync"

	"github.com/gastownhall/tmux-adapter/internal/tmux"
)

// RegistryEvent represents a change in agent state.
type RegistryEvent struct {
	Type  string // "added", "removed", "updated"
	Agent Agent
}

// Registry tracks live agents and emits lifecycle events.
type Registry struct {
	ctrl         *tmux.ControlMode
	mu           sync.RWMutex
	agents       map[string]Agent // name -> agent
	events       chan RegistryEvent
	gtDir        string
	skipSessions []string
	stopCh       chan struct{}
}

// NewRegistry creates a new agent registry.
// skipSessions lists tmux session names to ignore during scanning (e.g., monitor sessions).
func NewRegistry(ctrl *tmux.ControlMode, gtDir string, skipSessions []string) *Registry {
	return &Registry{
		ctrl:         ctrl,
		agents:       make(map[string]Agent),
		events:       make(chan RegistryEvent, 100),
		gtDir:        gtDir,
		skipSessions: skipSessions,
		stopCh:       make(chan struct{}),
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

// Stop halts the registry watcher.
func (r *Registry) Stop() {
	close(r.stopCh)
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

func (r *Registry) shouldSkip(sessionName string) bool {
	return slices.Contains(r.skipSessions, sessionName)
}

func (r *Registry) watchLoop() {
	for {
		select {
		case <-r.stopCh:
			return
		case notif := <-r.ctrl.Notifications():
			switch notif.Type {
			case "sessions-changed", "window-renamed":
				// sessions-changed: session created/destroyed
				// window-renamed: agent set terminal title (e.g., Claude Code → "2.1.42")
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
		if !IsGastownSession(sess.Name) {
			continue
		}

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

		// Read agent environment variables
		agentName, _ := r.ctrl.ShowEnvironment(sess.Name, "GT_AGENT")
		agentRole, _ := r.ctrl.ShowEnvironment(sess.Name, "GT_ROLE")
		agentRig, _ := r.ctrl.ShowEnvironment(sess.Name, "GT_RIG")

		// Determine process names to check
		processNames := GetProcessNames(agentName)

		// Check if agent is alive — the agent is the CLI app, not the session.
		// Detection priority (from gastown spec):
		// 1. Direct pane command match
		// 2. Shell wrapping agent → check descendants
		// 3. Unrecognized command (version-as-argv[0]) → check binary, then descendants
		alive := false
		if IsAgentProcess(pane.Command, processNames) {
			alive = true
		} else if IsShell(pane.Command) && pane.PID != "" {
			alive = CheckDescendants(pane.PID, processNames)
		} else if pane.PID != "" {
			alive = CheckProcessBinary(pane.PID, processNames) || CheckDescendants(pane.PID, processNames)
		}

		if !alive {
			continue
		}

		// Validate workDir against gtDir if set
		if r.gtDir != "" && !strings.HasPrefix(pane.WorkDir, r.gtDir) {
			// This session's working directory doesn't belong to our gastown instance
			continue
		}

		// Determine role and rig from session name (env vars override if available)
		role, rig := ParseSessionName(sess.Name)
		if agentRole != "" {
			role = agentRole
		}
		if agentRig != "" {
			rig = agentRig
		}

		// Runtime is the agent preset name; infer from binary if not set
		runtime := agentName
		if runtime == "" {
			runtime = InferRuntime(pane.Command, pane.PID)
		}

		var rigPtr *string
		if rig != "" {
			rigPtr = &rig
		}

		discovered[sess.Name] = Agent{
			Name:     sess.Name,
			Role:     role,
			Runtime:  runtime,
			Rig:      rigPtr,
			WorkDir:  pane.WorkDir,
			Attached: sess.Attached,
		}
	}

	// Diff against known agents
	r.mu.Lock()
	defer r.mu.Unlock()

	// Find removed agents
	for name, oldAgent := range r.agents {
		if _, exists := discovered[name]; !exists {
			delete(r.agents, name)
			r.events <- RegistryEvent{Type: "removed", Agent: oldAgent}
		}
	}

	// Find added and updated agents
	for name, newAgent := range discovered {
		oldAgent, existed := r.agents[name]
		if !existed {
			r.agents[name] = newAgent
			r.events <- RegistryEvent{Type: "added", Agent: newAgent}
		} else if oldAgent.Attached != newAgent.Attached {
			r.agents[name] = newAgent
			r.events <- RegistryEvent{Type: "updated", Agent: newAgent}
		}
	}

	return nil
}
