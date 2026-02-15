package wsadapter

import (
	"context"
	"log"
	"net/http"
	"strings"
	"sync"

	"github.com/gastownhall/tmux-adapter/internal/agentio"
	"github.com/gastownhall/tmux-adapter/internal/agents"
	"github.com/gastownhall/tmux-adapter/internal/tmux"
	"github.com/gastownhall/tmux-adapter/internal/wsbase"
)

// Server is the WebSocket server that manages client connections.
type Server struct {
	registry       *agents.Registry
	pipeMgr        *tmux.PipePaneManager
	ctrl           *tmux.ControlMode
	prompter       *agentio.Prompter
	authToken      string
	originPatterns []string
	clients        map[*Client]struct{}
	mu             sync.Mutex
}

// NewServer creates a new WebSocket server.
func NewServer(registry *agents.Registry, pipeMgr *tmux.PipePaneManager, ctrl *tmux.ControlMode, authToken string, originPatterns []string) *Server {
	return &Server{
		registry:       registry,
		pipeMgr:        pipeMgr,
		ctrl:           ctrl,
		prompter:       agentio.NewPrompter(ctrl, registry),
		authToken:      strings.TrimSpace(authToken),
		originPatterns: originPatterns,
		clients:        make(map[*Client]struct{}),
	}
}

// ServeHTTP handles WebSocket upgrade requests at /ws.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if !wsbase.IsAuthorizedRequest(s.authToken, r) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	conn, err := wsbase.AcceptWebSocket(w, r, s.originPatterns)
	if err != nil {
		return
	}
	conn.SetReadLimit(int64(agentio.MaxFileUploadBytes + 64*1024))

	ctx, cancel := context.WithCancel(r.Context())
	client := NewClient(conn, s, ctx, cancel)

	s.mu.Lock()
	s.clients[client] = struct{}{}
	count := len(s.clients)
	s.mu.Unlock()

	log.Printf("client connected (%d total)", count)

	// Run read/write pumps — blocks until client disconnects
	go client.WritePump()
	client.ReadPump()

	// Cleanup on disconnect
	s.RemoveClient(client)
}

// BroadcastToAgentSubscribers sends a message to all clients subscribed to agent lifecycle events.
func (s *Server) BroadcastToAgentSubscribers(msg []byte) {
	s.mu.Lock()
	defer s.mu.Unlock()

	for client := range s.clients {
		client.mu.Lock()
		subscribed := client.agentSub
		client.mu.Unlock()

		if subscribed {
			client.SendText(msg)
		}
	}
}

// RemoveClient unsubscribes and removes a client from the server.
func (s *Server) RemoveClient(client *Client) {
	s.mu.Lock()
	delete(s.clients, client)
	count := len(s.clients)
	s.mu.Unlock()

	client.Close()
	log.Printf("client disconnected (%d remaining)", count)
}

// CloseAll closes all connected clients.
func (s *Server) CloseAll() {
	s.mu.Lock()
	clients := make([]*Client, 0, len(s.clients))
	for c := range s.clients {
		clients = append(clients, c)
	}
	s.mu.Unlock()

	for _, c := range clients {
		s.RemoveClient(c)
	}
}
