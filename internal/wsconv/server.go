package wsconv

import (
	"net/http"
	"sync"

	"github.com/gastownhall/tmux-adapter/internal/agentio"
	"github.com/gastownhall/tmux-adapter/internal/agents"
	"github.com/gastownhall/tmux-adapter/internal/conv"
	"github.com/gastownhall/tmux-adapter/internal/tmux"
	"github.com/gastownhall/tmux-adapter/internal/wsbase"
)

// maxSnapshotEvents caps the number of events in a single snapshot message.
const maxSnapshotEvents = 20000

// Server manages WebSocket connections for the converter service.
type Server struct {
	watcher        *conv.ConversationWatcher
	ctrl           *tmux.ControlMode
	registry       *agents.Registry
	prompter       *agentio.Prompter
	authToken      string
	originPatterns []string
	clients        map[*Client]struct{}
	mu             sync.Mutex
}

// NewServer creates a new converter WebSocket server.
func NewServer(watcher *conv.ConversationWatcher, authToken string, originPatterns []string, ctrl *tmux.ControlMode, registry *agents.Registry) *Server {
	return &Server{
		watcher:        watcher,
		ctrl:           ctrl,
		registry:       registry,
		prompter:       agentio.NewPrompter(ctrl, registry),
		authToken:      authToken,
		originPatterns: originPatterns,
		clients:        make(map[*Client]struct{}),
	}
}

// HandleWebSocket is the HTTP handler for /ws.
func (s *Server) HandleWebSocket(w http.ResponseWriter, r *http.Request) {
	if !wsbase.IsAuthorizedRequest(s.authToken, r) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	conn, err := wsbase.AcceptWebSocket(w, r, s.originPatterns)
	if err != nil {
		return
	}
	conn.SetReadLimit(int64(agentio.MaxFileUploadBytes + 64*1024))

	client := newClient(conn, s)
	s.addClient(client)
	defer s.removeClient(client)

	client.run()
}

// Broadcast sends a watcher event to all connected clients.
func (s *Server) Broadcast(event conv.WatcherEvent) {
	s.mu.Lock()
	defer s.mu.Unlock()

	switch event.Type {
	case "agent-added", "agent-removed", "agent-updated":
		s.broadcastAgentLifecycle(event)
	case "conversation-started":
		for c := range s.clients {
			c.deliverConversationStarted(event)
		}
	case "conversation-event":
		if event.Event == nil {
			return
		}
		for c := range s.clients {
			c.deliverConversationEvent(event.Event)
		}
	case "conversation-switched":
		for c := range s.clients {
			c.deliverConversationSwitch(event)
		}
	}
}

// broadcastAgentLifecycle sends agent lifecycle events to subscribed clients
// with per-client session filtering. Also sends agents-count on added/removed.
// Must be called with s.mu held.
func (s *Server) broadcastAgentLifecycle(event conv.WatcherEvent) {
	agentName := ""
	agentWorkDir := ""
	if event.Agent != nil {
		agentName = event.Agent.Name
		agentWorkDir = event.Agent.WorkDir
	}

	var msg serverMessage
	switch event.Type {
	case "agent-added":
		msg = serverMessage{Type: "agent-added", Agent: event.Agent}
	case "agent-removed":
		msg = serverMessage{Type: "agent-removed"}
		if event.Agent != nil {
			msg.Name = event.Agent.Name
		}
	case "agent-updated":
		msg = serverMessage{Type: "agent-updated", Agent: event.Agent}
	}

	// agents-count event on added/removed (total changed)
	sendCount := event.Type != "agent-updated"
	var countMsg serverMessage
	if sendCount {
		total := s.registry.Count()
		countMsg = serverMessage{Type: "agents-count", TotalAgents: &total}
	}

	for c := range s.clients {
		c.mu.Lock()
		subscribed := c.subscribedAgents
		include := c.includeSessionFilter
		exclude := c.excludeSessionFilter
		pathInclude := c.includePathFilter
		pathExclude := c.excludePathFilter
		c.mu.Unlock()

		if !subscribed {
			continue
		}

		if sendCount {
			c.sendJSON(countMsg)
		}
		if wsbase.PassesFilter(agentName, include, exclude) && wsbase.PassesFilter(agentWorkDir, pathInclude, pathExclude) {
			c.sendJSON(msg)
		}
	}
}

func (s *Server) addClient(c *Client) {
	s.mu.Lock()
	s.clients[c] = struct{}{}
	s.mu.Unlock()
}

func (s *Server) removeClient(c *Client) {
	s.mu.Lock()
	delete(s.clients, c)
	s.mu.Unlock()
	c.cleanup()
}
