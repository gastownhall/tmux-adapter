package ws

import (
	"context"
	"encoding/json"
	"log"
	"sync"

	"nhooyr.io/websocket"
)

// Client represents a single WebSocket connection.
type Client struct {
	conn       *websocket.Conn
	server     *Server
	send       chan []byte
	agentSub   bool                   // subscribed to agent lifecycle
	outputSubs map[string]<-chan []byte // agent name -> pipe-pane channel
	mu         sync.Mutex
	ctx        context.Context
	cancel     context.CancelFunc
}

// NewClient creates a new WebSocket client.
func NewClient(conn *websocket.Conn, server *Server, ctx context.Context, cancel context.CancelFunc) *Client {
	return &Client{
		conn:       conn,
		server:     server,
		send:       make(chan []byte, 256),
		outputSubs: make(map[string]<-chan []byte),
		ctx:        ctx,
		cancel:     cancel,
	}
}

// ReadPump reads messages from the WebSocket and routes them to handlers.
func (c *Client) ReadPump() {
	defer c.cancel()

	for {
		_, data, err := c.conn.Read(c.ctx)
		if err != nil {
			return
		}

		var req Request
		if err := json.Unmarshal(data, &req); err != nil {
			c.sendError("", "invalid JSON: "+err.Error())
			continue
		}

		handleMessage(c, req)
	}
}

// WritePump writes queued messages to the WebSocket and streams output subscriptions.
func (c *Client) WritePump() {
	defer c.cancel()

	for {
		select {
		case <-c.ctx.Done():
			return
		case msg, ok := <-c.send:
			if !ok {
				return
			}
			if err := c.conn.Write(c.ctx, websocket.MessageText, msg); err != nil {
				return
			}
		}
	}
}

// Send queues a message for sending to this client.
func (c *Client) Send(msg []byte) {
	select {
	case c.send <- msg:
	default:
		// Client is slow — drop message
		log.Printf("dropping message for slow client")
	}
}

// sendJSON marshals and sends a response.
func (c *Client) sendJSON(v any) {
	data, err := json.Marshal(v)
	if err != nil {
		log.Printf("marshal error: %v", err)
		return
	}
	c.Send(data)
}

// sendError sends an error response.
func (c *Client) sendError(id, errMsg string) {
	ok := false
	resp := Response{
		Type:  "error",
		OK:    &ok,
		Error: errMsg,
	}
	if id != "" {
		resp.ID = id
	}
	c.sendJSON(resp)
}

// Close cleans up all subscriptions and closes the connection.
func (c *Client) Close() {
	c.mu.Lock()
	defer c.mu.Unlock()

	// Unsubscribe from all output streams
	for session, ch := range c.outputSubs {
		c.server.pipeMgr.Unsubscribe(session, ch)
		delete(c.outputSubs, session)
	}

	c.agentSub = false
	c.conn.Close(websocket.StatusNormalClosure, "")
}
