package ws

import (
	"encoding/json"
	"sync"
	"time"

	"github.com/gastownhall/tmux-adapter/internal/agents"
)

// Request is a message from a WebSocket client.
type Request struct {
	ID     string `json:"id"`
	Type   string `json:"type"`
	Agent  string `json:"agent,omitempty"`
	Prompt string `json:"prompt,omitempty"`
	Stream *bool  `json:"stream,omitempty"`
}

// Response is a message sent to a WebSocket client.
type Response struct {
	ID      string         `json:"id,omitempty"`
	Type    string         `json:"type"`
	OK      *bool          `json:"ok,omitempty"`
	Error   string         `json:"error,omitempty"`
	Agents  []agents.Agent `json:"agents,omitempty"`
	History string         `json:"history,omitempty"`
	Agent   *agents.Agent  `json:"agent,omitempty"`
	Name    string         `json:"name,omitempty"`
	Data    string         `json:"data,omitempty"`
}

// Per-agent mutexes for send-prompt serialization.
var (
	nudgeLocks   = make(map[string]*sync.Mutex)
	nudgeLocksMu sync.Mutex
)

func getNudgeLock(agent string) *sync.Mutex {
	nudgeLocksMu.Lock()
	defer nudgeLocksMu.Unlock()
	if _, ok := nudgeLocks[agent]; !ok {
		nudgeLocks[agent] = &sync.Mutex{}
	}
	return nudgeLocks[agent]
}

// handleMessage routes a request to the appropriate handler.
func handleMessage(c *Client, req Request) {
	switch req.Type {
	case "list-agents":
		handleListAgents(c, req)
	case "send-prompt":
		handleSendPrompt(c, req)
	case "subscribe-output":
		handleSubscribeOutput(c, req)
	case "unsubscribe-output":
		handleUnsubscribeOutput(c, req)
	case "subscribe-agents":
		handleSubscribeAgents(c, req)
	case "unsubscribe-agents":
		handleUnsubscribeAgents(c, req)
	default:
		c.sendError(req.ID, "unknown message type: "+req.Type)
	}
}

func handleListAgents(c *Client, req Request) {
	agentList := c.server.registry.GetAgents()
	c.sendJSON(Response{
		ID:     req.ID,
		Type:   "list-agents",
		Agents: agentList,
	})
}

func handleSendPrompt(c *Client, req Request) {
	if req.Agent == "" {
		c.sendError(req.ID, "agent field required")
		return
	}
	if req.Prompt == "" {
		c.sendError(req.ID, "prompt field required")
		return
	}

	// Verify agent exists
	agent, ok := c.server.registry.GetAgent(req.Agent)
	if !ok {
		ok := false
		c.sendJSON(Response{ID: req.ID, Type: "send-prompt", OK: &ok, Error: "agent not found"})
		return
	}

	// Serialize sends to this agent
	lock := getNudgeLock(req.Agent)

	go func() {
		lock.Lock()
		defer lock.Unlock()

		session := agent.Name

		// 1. Send text in literal mode
		if err := c.server.ctrl.SendKeysLiteral(session, req.Prompt); err != nil {
			ok := false
			c.sendJSON(Response{ID: req.ID, Type: "send-prompt", OK: &ok, Error: err.Error()})
			return
		}

		// 2. Wait 500ms for paste to complete
		time.Sleep(500 * time.Millisecond)

		// 3. Send Escape (for vim mode)
		c.server.ctrl.SendKeysRaw(session, "Escape")
		time.Sleep(100 * time.Millisecond)

		// 4. Send Enter with 3x retry, 200ms backoff
		var lastErr error
		for attempt := 0; attempt < 3; attempt++ {
			if attempt > 0 {
				time.Sleep(200 * time.Millisecond)
			}
			if err := c.server.ctrl.SendKeysRaw(session, "Enter"); err != nil {
				lastErr = err
				continue
			}

			// 5. Wake detached sessions via SIGWINCH resize dance
			if !agent.Attached {
				c.server.ctrl.ResizePane(session, "-1")
				time.Sleep(50 * time.Millisecond)
				c.server.ctrl.ResizePane(session, "+1")
			}

			ok := true
			c.sendJSON(Response{ID: req.ID, Type: "send-prompt", OK: &ok})
			return
		}

		ok := false
		errMsg := "failed to send Enter after 3 attempts"
		if lastErr != nil {
			errMsg += ": " + lastErr.Error()
		}
		c.sendJSON(Response{ID: req.ID, Type: "send-prompt", OK: &ok, Error: errMsg})
	}()
}

func handleSubscribeOutput(c *Client, req Request) {
	if req.Agent == "" {
		c.sendError(req.ID, "agent field required")
		return
	}

	_, ok := c.server.registry.GetAgent(req.Agent)
	if !ok {
		okVal := false
		c.sendJSON(Response{ID: req.ID, Type: "subscribe-output", OK: &okVal, Error: "agent not found"})
		return
	}

	// Capture full scrollback
	history, err := c.server.ctrl.CapturePaneAll(req.Agent)
	if err != nil {
		okVal := false
		c.sendJSON(Response{ID: req.ID, Type: "subscribe-output", OK: &okVal, Error: err.Error()})
		return
	}

	// Check if streaming is requested (default: true)
	wantStream := req.Stream == nil || *req.Stream

	if wantStream {
		// Subscribe to pipe-pane output
		ch, err := c.server.pipeMgr.Subscribe(req.Agent)
		if err != nil {
			okVal := false
			c.sendJSON(Response{ID: req.ID, Type: "subscribe-output", OK: &okVal, Error: err.Error()})
			return
		}

		c.mu.Lock()
		c.outputSubs[req.Agent] = ch
		c.mu.Unlock()

		// Stream output events in background
		go func() {
			for data := range ch {
				event, _ := json.Marshal(Response{
					Type:  "output",
					Name:  req.Agent,
					Data:  string(data),
				})
				c.Send(event)
			}
		}()
	}

	okVal := true
	c.sendJSON(Response{
		ID:      req.ID,
		Type:    "subscribe-output",
		OK:      &okVal,
		History: history,
	})
}

func handleUnsubscribeOutput(c *Client, req Request) {
	if req.Agent == "" {
		c.sendError(req.ID, "agent field required")
		return
	}

	c.mu.Lock()
	ch, exists := c.outputSubs[req.Agent]
	if exists {
		delete(c.outputSubs, req.Agent)
	}
	c.mu.Unlock()

	if exists {
		c.server.pipeMgr.Unsubscribe(req.Agent, ch)
	}

	okVal := true
	c.sendJSON(Response{ID: req.ID, Type: "unsubscribe-output", OK: &okVal})
}

func handleSubscribeAgents(c *Client, req Request) {
	c.mu.Lock()
	c.agentSub = true
	c.mu.Unlock()

	agentList := c.server.registry.GetAgents()
	okVal := true
	c.sendJSON(Response{
		ID:     req.ID,
		Type:   "subscribe-agents",
		OK:     &okVal,
		Agents: agentList,
	})
}

func handleUnsubscribeAgents(c *Client, req Request) {
	c.mu.Lock()
	c.agentSub = false
	c.mu.Unlock()

	okVal := true
	c.sendJSON(Response{ID: req.ID, Type: "unsubscribe-agents", OK: &okVal})
}

// MakeAgentEvent creates a JSON event message for agent lifecycle changes.
func MakeAgentEvent(eventType string, agent agents.Agent) []byte {
	var resp Response
	switch eventType {
	case "added":
		resp = Response{Type: "agent-added", Agent: &agent}
	case "removed":
		resp = Response{Type: "agent-removed", Name: agent.Name}
	case "updated":
		resp = Response{Type: "agent-updated", Agent: &agent}
	}
	data, _ := json.Marshal(resp)
	return data
}
