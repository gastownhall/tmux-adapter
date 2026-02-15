package wsadapter

import (
	"encoding/json"
	"fmt"
	"log"
	"strconv"
	"strings"
	"time"

	"github.com/gastownhall/tmux-adapter/internal/agentio"
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

// handleMessage routes a text request to the appropriate handler.
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

// handleBinaryMessage routes binary WebSocket frames.
// Format: msgType(1 byte) + agentName + \0 + payload
func handleBinaryMessage(c *Client, data []byte) {
	msgType, agentName, payload, err := agentio.ParseBinaryEnvelope(data)
	if err != nil {
		c.sendError("", "invalid binary message: "+err.Error())
		return
	}

	switch msgType {
	case agentio.BinaryKeyboardInput:
		if err := sendKeyboardPayload(c, agentName, payload); err != nil {
			log.Printf("keyboard input %s error: %v", agentName, err)
			c.sendError("", "keyboard input "+agentName+": "+err.Error())
		}
	case agentio.BinaryResize:
		parts := strings.SplitN(string(payload), ":", 2)
		if len(parts) != 2 {
			c.sendError("", "invalid resize payload for "+agentName+": expected cols:rows")
			return
		}
		cols, err1 := strconv.Atoi(parts[0])
		rows, err2 := strconv.Atoi(parts[1])
		if err1 != nil || err2 != nil {
			c.sendError("", "invalid resize payload for "+agentName+": non-numeric cols/rows")
			return
		}
		if cols < 2 || rows < 1 {
			c.sendError("", fmt.Sprintf("invalid resize payload for %s: %dx%d out of range", agentName, cols, rows))
			return
		}
		log.Printf("binary resize %s -> %dx%d", agentName, cols, rows)
		if err := c.server.ctrl.ResizePaneTo(agentName, cols, rows); err != nil {
			log.Printf("resize %s error: %v", agentName, err)
			c.sendError("", "resize "+agentName+": "+err.Error())
			return
		}
		// No snapshot needed — pipe-pane captures the app's SIGWINCH redraw naturally.
	case agentio.BinaryFileUpload:
		payloadCopy := append([]byte(nil), payload...)
		go func() {
			lock := c.server.prompter.GetLock(agentName)
			lock.Lock()
			defer lock.Unlock()

			if err := c.server.prompter.HandleFileUpload(agentName, payloadCopy); err != nil {
				log.Printf("file upload %s error: %v", agentName, err)
				c.sendError("", "file upload "+agentName+": "+err.Error())
			}
		}()
	default:
		log.Printf("unknown binary message type: 0x%02x", msgType)
		c.sendError("", fmt.Sprintf("unknown binary message type: 0x%02x", msgType))
	}
}

func sendKeyboardPayload(c *Client, agentName string, payload []byte) error {
	// Prefer tmux key names for known VT special-key sequences (e.g. Shift+Tab).
	// Fall back to byte-exact injection for everything else.
	if keyName, ok := tmuxKeyNameFromVT(payload); ok {
		return c.server.ctrl.SendKeysRaw(agentName, keyName)
	}
	return c.server.ctrl.SendKeysBytes(agentName, payload)
}

func tmuxKeyNameFromVT(payload []byte) (string, bool) {
	switch string(payload) {
	case "\x1b[Z":
		return "BTab", true
	case "\x1b[A", "\x1bOA":
		return "Up", true
	case "\x1b[B", "\x1bOB":
		return "Down", true
	case "\x1b[C", "\x1bOC":
		return "Right", true
	case "\x1b[D", "\x1bOD":
		return "Left", true
	case "\x1b[H", "\x1bOH":
		return "Home", true
	case "\x1b[F", "\x1bOF":
		return "End", true
	case "\x1b[5~":
		return "PgUp", true
	case "\x1b[6~":
		return "PgDn", true
	case "\x1b[2~":
		return "IC", true
	case "\x1b[3~":
		return "DC", true
	case "\x1bOP":
		return "F1", true
	case "\x1bOQ":
		return "F2", true
	case "\x1bOR":
		return "F3", true
	case "\x1bOS":
		return "F4", true
	case "\x1b[15~":
		return "F5", true
	case "\x1b[17~":
		return "F6", true
	case "\x1b[18~":
		return "F7", true
	case "\x1b[19~":
		return "F8", true
	case "\x1b[20~":
		return "F9", true
	case "\x1b[21~":
		return "F10", true
	case "\x1b[23~":
		return "F11", true
	case "\x1b[24~":
		return "F12", true
	case "\x1b":
		return "Escape", true
	case "\x7f":
		return "BSpace", true
	}
	return "", false
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

	// Verify agent exists before acquiring lock
	if _, ok := c.server.registry.GetAgent(req.Agent); !ok {
		ok := false
		c.sendJSON(Response{ID: req.ID, Type: "send-prompt", OK: &ok, Error: "agent not found"})
		return
	}

	lock := c.server.prompter.GetLock(req.Agent)

	go func() {
		lock.Lock()
		defer lock.Unlock()

		if err := c.server.prompter.SendPrompt(req.Agent, req.Prompt); err != nil {
			ok := false
			c.sendJSON(Response{ID: req.ID, Type: "send-prompt", OK: &ok, Error: err.Error()})
			return
		}

		ok := true
		c.sendJSON(Response{ID: req.ID, Type: "send-prompt", OK: &ok})
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

	// Check if streaming is requested (default: true)
	wantStream := req.Stream == nil || *req.Stream

	if wantStream {
		// Clean up any existing subscription for this agent before creating a new one.
		// This prevents leaking the old channel and its forwarding goroutine.
		c.mu.Lock()
		oldCh, hadOld := c.outputSubs[req.Agent]
		if hadOld {
			delete(c.outputSubs, req.Agent)
		}
		c.mu.Unlock()
		if hadOld {
			log.Printf("subscribe-output(%s): replacing existing subscription", req.Agent)
			c.server.pipeMgr.Unsubscribe(req.Agent, oldCh)
		}

		// Subscribe to pipe-pane first so it's ready for ongoing streaming.
		log.Printf("subscribe-output(%s): starting pipe-pane", req.Agent)
		ch, err := c.server.pipeMgr.Subscribe(req.Agent)
		if err != nil {
			log.Printf("subscribe-output(%s): pipe-pane error: %v", req.Agent, err)
			okVal := false
			c.sendJSON(Response{ID: req.ID, Type: "subscribe-output", OK: &okVal, Error: err.Error()})
			return
		}
		log.Printf("subscribe-output(%s): pipe-pane active", req.Agent)

		c.mu.Lock()
		c.outputSubs[req.Agent] = ch
		c.mu.Unlock()

		okVal := true
		c.sendJSON(Response{
			ID:   req.ID,
			Type: "subscribe-output",
			OK:   &okVal,
		})

		// Drain any output the agent was already producing — we only want
		// the controlled redraw.
		drained := 0
	drain:
		for {
			select {
			case _, ok := <-ch:
				if !ok {
					break drain
				}
				drained++
			default:
				break drain
			}
		}
		if drained > 0 {
			log.Printf("subscribe-output(%s): drained %d pre-redraw chunks", req.Agent, drained)
		}

		// Force a clean redraw. The resize dance triggers SIGWINCH, causing
		// the app to repaint. pipe-pane captures all output in real-time.
		log.Printf("subscribe-output(%s): forcing redraw", req.Agent)
		c.server.ctrl.ForceRedraw(req.Agent)

		// Let the app finish redrawing; pipe-pane buffers all output in ch.
		time.Sleep(200 * time.Millisecond)

		// Send a minimal 0x05 (clear screen) to trigger the client's reset+reveal.
		// The actual content comes from pipe-pane data buffered in ch.
		log.Printf("subscribe-output(%s): sending 0x05 clear-screen trigger", req.Agent)
		c.SendBinary(agentio.MakeBinaryFrame(agentio.BinaryTerminalSnapshot, req.Agent, []byte("\x1b[2J\x1b[H")))

		// Stream raw bytes in background — immediately flushes buffered pipe-pane data.
		go func() {
			for rawBytes := range ch {
				c.SendBinary(agentio.MakeBinaryFrame(agentio.BinaryTerminalOutput, req.Agent, rawBytes))
			}
		}()
	} else {
		// Non-streaming: return full capture in JSON
		fullHistory, _ := c.server.ctrl.CapturePaneAll(req.Agent)
		okVal := true
		c.sendJSON(Response{
			ID:      req.ID,
			Type:    "subscribe-output",
			OK:      &okVal,
			History: fullHistory,
		})
	}
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
