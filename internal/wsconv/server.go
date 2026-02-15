package wsconv

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"sync"
	"time"

	"nhooyr.io/websocket"

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
	case "agent-added":
		msg := serverMessage{
			Type:  "agent-added",
			Agent: event.Agent,
		}
		for c := range s.clients {
			if c.subscribedAgents {
				c.sendJSON(msg)
			}
		}
	case "agent-removed":
		msg := serverMessage{
			Type: "agent-removed",
		}
		if event.Agent != nil {
			msg.Name = event.Agent.Name
		}
		for c := range s.clients {
			if c.subscribedAgents {
				c.sendJSON(msg)
			}
		}
	case "agent-updated":
		msg := serverMessage{
			Type:  "agent-updated",
			Agent: event.Agent,
		}
		for c := range s.clients {
			if c.subscribedAgents {
				c.sendJSON(msg)
			}
		}
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

// outMsg wraps a WebSocket message with its type (text or binary).
type outMsg struct {
	typ  websocket.MessageType
	data []byte
}

// Client represents a connected WebSocket client.
type Client struct {
	conn     *websocket.Conn
	server   *Server
	send     chan outMsg
	ctx      context.Context
	cancel   context.CancelFunc
	mu       sync.Mutex
	subs     map[string]*subscription // subscriptionId → subscription
	follows  map[string]*subscription // agentName → subscription (follow-agent)
	nextSub  int
	subscribedAgents bool
	handshakeDone    bool
}

type subscription struct {
	id             string
	conversationID string
	agentName      string // non-empty for follow-agent
	filter         conv.EventFilter
	live           <-chan conv.ConversationEvent
	cancel         context.CancelFunc
}

func newClient(conn *websocket.Conn, server *Server) *Client {
	ctx, cancel := context.WithCancel(context.Background())
	return &Client{
		conn:    conn,
		server:  server,
		send:    make(chan outMsg, 256),
		ctx:     ctx,
		cancel:  cancel,
		subs:    make(map[string]*subscription),
		follows: make(map[string]*subscription),
	}
}

func (c *Client) run() {
	go c.writePump()
	c.readPump()
}

func (c *Client) readPump() {
	defer c.cancel()
	for {
		typ, data, err := c.conn.Read(c.ctx)
		if err != nil {
			return
		}
		if typ == websocket.MessageBinary {
			c.handleBinaryMessage(data)
			continue
		}
		c.handleTextMessage(data)
	}
}

func (c *Client) writePump() {
	defer func() { _ = c.conn.Close(websocket.StatusNormalClosure, "") }()
	for {
		select {
		case <-c.ctx.Done():
			return
		case msg, ok := <-c.send:
			if !ok {
				return
			}
			ctx, cancel := context.WithTimeout(c.ctx, 5*time.Second)
			err := c.conn.Write(ctx, msg.typ, msg.data)
			cancel()
			if err != nil {
				return
			}
		}
	}
}

func (c *Client) sendJSON(v any) {
	data, err := json.Marshal(v)
	if err != nil {
		return
	}
	select {
	case c.send <- outMsg{typ: websocket.MessageText, data: data}:
	default:
		// Slow consumer — drop
	}
}

func (c *Client) handleBinaryMessage(data []byte) {
	msgType, agentName, payload, err := agentio.ParseBinaryEnvelope(data)
	if err != nil {
		c.sendJSON(serverMessage{Type: "error", Error: "invalid binary message: " + err.Error()})
		return
	}

	switch msgType {
	case agentio.BinaryFileUpload:
		payloadCopy := append([]byte(nil), payload...)
		go func() {
			lock := c.server.prompter.GetLock(agentName)
			lock.Lock()
			defer lock.Unlock()
			if err := c.server.prompter.HandleFileUpload(agentName, payloadCopy); err != nil {
				log.Printf("file upload %s error: %v", agentName, err)
				c.sendJSON(serverMessage{Type: "error", Error: "file upload " + agentName + ": " + err.Error()})
			}
		}()
	default:
		c.sendJSON(serverMessage{Type: "error", Error: fmt.Sprintf("unsupported binary message type: 0x%02x", msgType)})
	}
}

func (c *Client) handleTextMessage(data []byte) {
	var msg clientMessage
	if err := json.Unmarshal(data, &msg); err != nil {
		c.sendJSON(serverMessage{Type: "error", Error: "invalid JSON"})
		return
	}

	if !c.handshakeDone {
		if msg.Type != "hello" {
			c.sendJSON(serverMessage{ID: msg.ID, Type: "error", Error: "handshake required: send hello first"})
			return
		}
		c.handleHello(msg)
		return
	}

	switch msg.Type {
	case "hello":
		c.sendJSON(serverMessage{ID: msg.ID, Type: "error", Error: "already handshaked"})
	case "list-agents":
		c.handleListAgents(msg)
	case "subscribe-agents":
		c.handleSubscribeAgents(msg)
	case "list-conversations":
		c.handleListConversations(msg)
	case "subscribe-conversation":
		c.handleSubscribeConversation(msg)
	case "follow-agent":
		c.handleFollowAgent(msg)
	case "unsubscribe":
		c.handleUnsubscribe(msg)
	case "unsubscribe-agent":
		c.handleUnsubscribeAgent(msg)
	case "send-prompt":
		c.handleSendPrompt(msg)
	default:
		c.sendJSON(serverMessage{ID: msg.ID, Type: "error", Error: "unknown message type", UnknownType: msg.Type})
	}
}

func (c *Client) handleHello(msg clientMessage) {
	if msg.Protocol != "tmux-converter.v1" {
		c.sendJSON(serverMessage{ID: msg.ID, Type: "hello", OK: boolPtr(false), Error: "unsupported protocol version"})
		return
	}
	c.handshakeDone = true
	c.sendJSON(serverMessage{ID: msg.ID, Type: "hello", OK: boolPtr(true), Protocol: "tmux-converter.v1", ServerVersion: "0.1.0"})
}

func (c *Client) handleListAgents(msg clientMessage) {
	regAgents := c.buildAgentList()
	c.sendJSON(serverMessage{ID: msg.ID, Type: "list-agents", Agents: regAgents})
}

func (c *Client) handleSubscribeAgents(msg clientMessage) {
	c.subscribedAgents = true
	regAgents := c.buildAgentList()
	c.sendJSON(serverMessage{ID: msg.ID, Type: "subscribe-agents", OK: boolPtr(true), Agents: regAgents})
}

func (c *Client) buildAgentList() []agentInfo {
	agents := c.server.watcher.ListAgents()
	result := make([]agentInfo, 0, len(agents))
	for _, a := range agents {
		info := agentInfo{
			Name:    a.Name,
			Runtime: a.Runtime,
		}
		// Attach active conversation ID if one exists
		if convID := c.server.watcher.GetActiveConversation(a.Name); convID != "" {
			info.ConversationID = convID
		}
		result = append(result, info)
	}
	return result
}

func (c *Client) handleListConversations(msg clientMessage) {
	convs := c.server.watcher.ListConversations()
	c.sendJSON(serverMessage{ID: msg.ID, Type: "list-conversations", Conversations: convs})
}

func (c *Client) handleSubscribeConversation(msg clientMessage) {
	if msg.ConversationID == "" {
		c.sendJSON(serverMessage{ID: msg.ID, Type: "error", Error: "conversationId required"})
		return
	}

	buf := c.server.watcher.GetBuffer(msg.ConversationID)
	if buf == nil {
		c.sendJSON(serverMessage{ID: msg.ID, Type: "error", Error: "conversation not found"})
		return
	}

	filter := buildFilter(msg.Filter)
	snapshot, live := buf.Subscribe(filter)

	c.mu.Lock()
	c.nextSub++
	subID := subID(c.nextSub)
	sub := &subscription{
		id:             subID,
		conversationID: msg.ConversationID,
		filter:         filter,
		live:           live,
	}
	c.subs[subID] = sub
	c.mu.Unlock()

	snapshot = capSnapshot(snapshot)
	cursor := makeCursor(msg.ConversationID, snapshot)

	c.sendJSON(serverMessage{
		ID:             msg.ID,
		Type:           "conversation-snapshot",
		SubscriptionID: subID,
		ConversationID: msg.ConversationID,
		Events:         snapshot,
		Cursor:         cursor,
	})

	go c.streamLive(sub, buf)
}

func (c *Client) handleFollowAgent(msg clientMessage) {
	if msg.Agent == "" {
		c.sendJSON(serverMessage{ID: msg.ID, Type: "error", Error: "agent required"})
		return
	}

	// Remove existing follow for this agent
	c.mu.Lock()
	if existing, ok := c.follows[msg.Agent]; ok {
		delete(c.subs, existing.id)
		if existing.cancel != nil {
			existing.cancel()
		}
		if existing.live != nil {
			oldBuf := c.server.watcher.GetBuffer(existing.conversationID)
			if oldBuf != nil {
				oldBuf.Unsubscribe(existing.live)
			}
		}
	}

	filter := buildFilter(msg.Filter)
	c.nextSub++
	subID := subID(c.nextSub)

	convID := c.server.watcher.GetActiveConversation(msg.Agent)

	if convID == "" {
		// No active conversation yet — register a pending follow
		sub := &subscription{
			id:        subID,
			agentName: msg.Agent,
			filter:    filter,
		}
		c.subs[subID] = sub
		c.follows[msg.Agent] = sub
		c.mu.Unlock()

		c.sendJSON(serverMessage{
			ID:             msg.ID,
			Type:           "follow-agent",
			OK:             boolPtr(true),
			SubscriptionID: subID,
		})
		return
	}

	buf := c.server.watcher.GetBuffer(convID)
	if buf == nil {
		// Conversation ID exists but buffer doesn't yet — pending follow
		sub := &subscription{
			id:        subID,
			agentName: msg.Agent,
			filter:    filter,
		}
		c.subs[subID] = sub
		c.follows[msg.Agent] = sub
		c.mu.Unlock()

		c.sendJSON(serverMessage{
			ID:             msg.ID,
			Type:           "follow-agent",
			OK:             boolPtr(true),
			SubscriptionID: subID,
		})
		return
	}

	snapshot, live := buf.Subscribe(filter)
	subCtx, subCancel := context.WithCancel(c.ctx)
	sub := &subscription{
		id:             subID,
		conversationID: convID,
		agentName:      msg.Agent,
		filter:         filter,
		live:           live,
		cancel:         subCancel,
	}
	c.subs[subID] = sub
	c.follows[msg.Agent] = sub
	c.mu.Unlock()

	snapshot = capSnapshot(snapshot)
	cursor := makeCursor(convID, snapshot)

	c.sendJSON(serverMessage{
		ID:             msg.ID,
		Type:           "follow-agent",
		OK:             boolPtr(true),
		SubscriptionID: subID,
		ConversationID: convID,
		Events:         snapshot,
		Cursor:         cursor,
	})

	go c.streamLiveWithContext(sub, buf, subCtx)
}

func (c *Client) handleUnsubscribe(msg clientMessage) {
	c.mu.Lock()
	sub, ok := c.subs[msg.SubscriptionID]
	if ok {
		delete(c.subs, msg.SubscriptionID)
		if sub.agentName != "" {
			delete(c.follows, sub.agentName)
		}
		if sub.cancel != nil {
			sub.cancel()
		}
	}
	c.mu.Unlock()

	if ok {
		buf := c.server.watcher.GetBuffer(sub.conversationID)
		if buf != nil {
			buf.Unsubscribe(sub.live)
		}
	}

	c.sendJSON(serverMessage{ID: msg.ID, Type: "unsubscribe", OK: boolPtr(true)})
}

func (c *Client) handleUnsubscribeAgent(msg clientMessage) {
	c.mu.Lock()
	sub, ok := c.follows[msg.Agent]
	if ok {
		delete(c.follows, msg.Agent)
		delete(c.subs, sub.id)
		if sub.cancel != nil {
			sub.cancel()
		}
	}
	c.mu.Unlock()

	if ok {
		buf := c.server.watcher.GetBuffer(sub.conversationID)
		if buf != nil {
			buf.Unsubscribe(sub.live)
		}
	}

	c.sendJSON(serverMessage{ID: msg.ID, Type: "unsubscribe-agent", OK: boolPtr(true)})
}

func (c *Client) handleSendPrompt(msg clientMessage) {
	if msg.Agent == "" {
		c.sendJSON(serverMessage{ID: msg.ID, Type: "error", Error: "agent field required"})
		return
	}
	if msg.Prompt == "" {
		c.sendJSON(serverMessage{ID: msg.ID, Type: "error", Error: "prompt field required"})
		return
	}

	lock := c.server.prompter.GetLock(msg.Agent)
	go func() {
		lock.Lock()
		defer lock.Unlock()

		if err := c.server.prompter.SendPrompt(msg.Agent, msg.Prompt); err != nil {
			c.sendJSON(serverMessage{ID: msg.ID, Type: "send-prompt", OK: boolPtr(false), Error: err.Error()})
			return
		}
		c.sendJSON(serverMessage{ID: msg.ID, Type: "send-prompt", OK: boolPtr(true)})
	}()
}

func (c *Client) deliverConversationEvent(event *conv.ConversationEvent) {
	c.mu.Lock()
	defer c.mu.Unlock()

	for _, sub := range c.subs {
		if sub.live != nil {
			continue // already delivered via streamLiveWithContext
		}
		if sub.conversationID == event.ConversationID && sub.filter.Matches(*event) {
			cursor := conv.Cursor{
				ConversationID: event.ConversationID,
				Seq:            event.Seq,
				EventID:        event.EventID,
			}
			c.sendJSON(serverMessage{
				Type:           "conversation-event",
				SubscriptionID: sub.id,
				ConversationID: event.ConversationID,
				Event:          event,
				Cursor:         encodeCursor(cursor),
			})
		}
	}
}

func (c *Client) deliverConversationStarted(we conv.WatcherEvent) {
	if we.Agent == nil || we.NewConvID == "" {
		return
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	sub, ok := c.follows[we.Agent.Name]
	if !ok || sub.conversationID != "" {
		// No pending follow, or already subscribed to a conversation
		return
	}

	buf := c.server.watcher.GetBuffer(we.NewConvID)
	if buf == nil {
		return
	}

	snapshot, live := buf.Subscribe(sub.filter)
	subCtx, subCancel := context.WithCancel(c.ctx)

	sub.conversationID = we.NewConvID
	sub.live = live
	sub.cancel = subCancel

	snapshot = capSnapshot(snapshot)
	cursor := makeCursor(we.NewConvID, snapshot)

	c.sendJSON(serverMessage{
		Type:           "conversation-snapshot",
		SubscriptionID: sub.id,
		ConversationID: we.NewConvID,
		Events:         snapshot,
		Cursor:         cursor,
	})

	go c.streamLiveWithContext(sub, buf, subCtx)
}

func (c *Client) deliverConversationSwitch(we conv.WatcherEvent) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if we.Agent == nil {
		return
	}

	sub, ok := c.follows[we.Agent.Name]
	if !ok {
		return
	}

	// Unsubscribe from old buffer
	oldBuf := c.server.watcher.GetBuffer(sub.conversationID)
	if oldBuf != nil {
		oldBuf.Unsubscribe(sub.live)
	}
	if sub.cancel != nil {
		sub.cancel()
	}

	// Send switch message
	c.sendJSON(serverMessage{
		Type:           "conversation-switched",
		SubscriptionID: sub.id,
		Agent:          we.Agent,
		From:           we.OldConvID,
		To:             we.NewConvID,
	})

	// Subscribe to new buffer
	newBuf := c.server.watcher.GetBuffer(we.NewConvID)
	if newBuf == nil {
		return
	}

	snapshot, live := newBuf.Subscribe(sub.filter)
	subCtx, subCancel := context.WithCancel(c.ctx)

	sub.conversationID = we.NewConvID
	sub.live = live
	sub.cancel = subCancel

	snapshot = capSnapshot(snapshot)
	cursor := makeCursor(we.NewConvID, snapshot)

	c.sendJSON(serverMessage{
		Type:           "conversation-snapshot",
		SubscriptionID: sub.id,
		ConversationID: we.NewConvID,
		Events:         snapshot,
		Cursor:         cursor,
		Reason:         "switch",
	})

	go c.streamLiveWithContext(sub, newBuf, subCtx)
}

func (c *Client) streamLive(sub *subscription, buf *conv.ConversationBuffer) {
	c.streamLiveWithContext(sub, buf, c.ctx)
}

func (c *Client) streamLiveWithContext(sub *subscription, _ *conv.ConversationBuffer, ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case event, ok := <-sub.live:
			if !ok {
				return
			}
			cursor := conv.Cursor{
				ConversationID: sub.conversationID,
				Seq:            event.Seq,
				EventID:        event.EventID,
			}
			c.sendJSON(serverMessage{
				Type:           "conversation-event",
				SubscriptionID: sub.id,
				ConversationID: sub.conversationID,
				Event:          &event,
				Cursor:         encodeCursor(cursor),
			})
		}
	}
}

func (c *Client) cleanup() {
	c.mu.Lock()
	defer c.mu.Unlock()

	for _, sub := range c.subs {
		buf := c.server.watcher.GetBuffer(sub.conversationID)
		if buf != nil {
			buf.Unsubscribe(sub.live)
		}
		if sub.cancel != nil {
			sub.cancel()
		}
	}
	c.subs = nil
	c.follows = nil
}

// Helper types and functions

type clientMessage struct {
	ID             string           `json:"id"`
	Type           string           `json:"type"`
	Protocol       string           `json:"protocol,omitempty"`
	ConversationID string           `json:"conversationId,omitempty"`
	Agent          string           `json:"agent,omitempty"`
	Prompt         string           `json:"prompt,omitempty"`
	SubscriptionID string           `json:"subscriptionId,omitempty"`
	Filter         *clientFilter    `json:"filter,omitempty"`
	Cursor         string           `json:"cursor,omitempty"`
}

type clientFilter struct {
	Types           []string `json:"types,omitempty"`
	ExcludeThinking *bool    `json:"excludeThinking,omitempty"`
	ExcludeProgress *bool    `json:"excludeProgress,omitempty"`
}

type serverMessage struct {
	ID             string                    `json:"id,omitempty"`
	Type           string                    `json:"type"`
	OK             *bool                     `json:"ok,omitempty"`
	Error          string                    `json:"error,omitempty"`
	Protocol       string                    `json:"protocol,omitempty"`
	ServerVersion  string                    `json:"serverVersion,omitempty"`
	UnknownType    string                    `json:"unknownType,omitempty"`
	Agents         []agentInfo               `json:"agents,omitempty"`
	Conversations  []conv.ConversationInfo   `json:"conversations,omitempty"`
	SubscriptionID string                    `json:"subscriptionId,omitempty"`
	ConversationID string                    `json:"conversationId,omitempty"`
	Events         []conv.ConversationEvent  `json:"events,omitempty"`
	Event          *conv.ConversationEvent   `json:"event,omitempty"`
	Cursor         string                    `json:"cursor,omitempty"`
	Agent          any                       `json:"agent,omitempty"`
	Name           string                    `json:"name,omitempty"`
	From           string                    `json:"from,omitempty"`
	To             string                    `json:"to,omitempty"`
	Reason         string                    `json:"reason,omitempty"`
}

type agentInfo struct {
	Name           string `json:"name"`
	Runtime        string `json:"runtime"`
	ConversationID string `json:"conversationId,omitempty"`
}

func buildFilter(cf *clientFilter) conv.EventFilter {
	if cf == nil {
		return conv.EventFilter{}
	}
	filter := conv.EventFilter{}
	if len(cf.Types) > 0 {
		filter.Types = make(map[string]bool)
		for _, t := range cf.Types {
			filter.Types[t] = true
		}
	}
	if cf.ExcludeThinking != nil {
		filter.ExcludeThinking = *cf.ExcludeThinking
	}
	if cf.ExcludeProgress != nil {
		filter.ExcludeProgress = *cf.ExcludeProgress
	}
	return filter
}

func subID(n int) string {
	return "sub-" + itoa(n)
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var buf [20]byte
	pos := len(buf)
	for n > 0 {
		pos--
		buf[pos] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[pos:])
}

func capSnapshot(events []conv.ConversationEvent) []conv.ConversationEvent {
	if len(events) > maxSnapshotEvents {
		return events[len(events)-maxSnapshotEvents:]
	}
	return events
}

func makeCursor(convID string, events []conv.ConversationEvent) string {
	if len(events) == 0 {
		return ""
	}
	last := events[len(events)-1]
	c := conv.Cursor{
		ConversationID: convID,
		Seq:            last.Seq,
		EventID:        last.EventID,
	}
	return encodeCursor(c)
}

func encodeCursor(c conv.Cursor) string {
	data, _ := json.Marshal(c)
	return string(data)
}

func boolPtr(b bool) *bool {
	return &b
}
