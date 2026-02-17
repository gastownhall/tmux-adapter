package wsconv

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"regexp"
	"time"

	"github.com/gastownhall/tmux-adapter/internal/agentio"
	"github.com/gastownhall/tmux-adapter/internal/conv"
	"github.com/gastownhall/tmux-adapter/internal/wsbase"
)

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
	// Ephemeral filter — does NOT update stored broadcast filter
	include, exclude, err := wsbase.CompileSessionFilters(msg.IncludeSessionFilter, msg.ExcludeSessionFilter)
	if err != nil {
		ok := false
		c.sendJSON(serverMessage{ID: msg.ID, Type: "list-agents", OK: &ok, Error: err.Error()})
		return
	}
	pathInclude, pathExclude, err := wsbase.CompilePathFilters(msg.IncludePathFilter, msg.ExcludePathFilter)
	if err != nil {
		ok := false
		c.sendJSON(serverMessage{ID: msg.ID, Type: "list-agents", OK: &ok, Error: err.Error()})
		return
	}

	regAgents := c.buildAgentList(include, exclude, pathInclude, pathExclude)
	c.sendJSON(serverMessage{ID: msg.ID, Type: "list-agents", Agents: regAgents})
}

func (c *Client) handleSubscribeAgents(msg clientMessage) {
	// Persistent filter — stored on client, applied to all future broadcasts
	include, exclude, err := wsbase.CompileSessionFilters(msg.IncludeSessionFilter, msg.ExcludeSessionFilter)
	if err != nil {
		ok := false
		c.sendJSON(serverMessage{ID: msg.ID, Type: "subscribe-agents", OK: &ok, Error: err.Error()})
		return
	}
	pathInclude, pathExclude, err := wsbase.CompilePathFilters(msg.IncludePathFilter, msg.ExcludePathFilter)
	if err != nil {
		ok := false
		c.sendJSON(serverMessage{ID: msg.ID, Type: "subscribe-agents", OK: &ok, Error: err.Error()})
		return
	}

	c.mu.Lock()
	c.subscribedAgents = true
	c.includeSessionFilter = include
	c.excludeSessionFilter = exclude
	c.includePathFilter = pathInclude
	c.excludePathFilter = pathExclude
	c.mu.Unlock()

	regAgents := c.buildAgentList(include, exclude, pathInclude, pathExclude)
	total := c.server.registry.Count()
	c.sendJSON(serverMessage{
		ID:          msg.ID,
		Type:        "subscribe-agents",
		OK:          boolPtr(true),
		Agents:      regAgents,
		TotalAgents: &total,
	})
}

func (c *Client) buildAgentList(include, exclude, pathInclude, pathExclude *regexp.Regexp) []agentInfo {
	allAgents := c.server.watcher.ListAgents()
	result := make([]agentInfo, 0, len(allAgents))
	for _, a := range allAgents {
		if !wsbase.PassesFilter(a.Name, include, exclude) {
			continue
		}
		if !wsbase.PassesFilter(a.WorkDir, pathInclude, pathExclude) {
			continue
		}
		info := agentInfo{
			Name:     a.Name,
			Runtime:  a.Runtime,
			WorkDir:  a.WorkDir,
			Attached: a.Attached,
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

	// Extract agent name from conversationID format "runtime:agentName:nativeId"
	agentName := extractAgentFromConvID(msg.ConversationID)
	if agentName == "" {
		// Fallback: check watcher's convToAgent map
		agentName = c.server.watcher.GetAgentForConversation(msg.ConversationID)
	}

	if agentName != "" {
		if err := c.server.watcher.EnsureTailing(agentName); err != nil {
			c.sendJSON(serverMessage{ID: msg.ID, Type: "error", Error: err.Error()})
			return
		}
	}

	buf := c.server.watcher.GetBuffer(msg.ConversationID)
	if buf == nil && agentName != "" {
		// Buffer not ready yet (EnsureTailing is async) — create pending subscription
		c.mu.Lock()
		if _, exists := c.pendingConvSubs[msg.ConversationID]; exists {
			c.mu.Unlock()
			c.sendJSON(serverMessage{ID: msg.ID, Type: "error", Error: "already pending subscription for this conversation"})
			return
		}
		pending := &pendingConvSub{
			msgID:     msg.ID,
			agentName: agentName,
			filter:    msg.Filter,
		}
		pending.timer = time.AfterFunc(30*time.Second, func() {
			c.mu.Lock()
			p, ok := c.pendingConvSubs[msg.ConversationID]
			if !ok {
				c.mu.Unlock()
				return // already resolved
			}
			delete(c.pendingConvSubs, msg.ConversationID)
			c.mu.Unlock()
			c.server.watcher.ReleaseTailing(p.agentName)
			c.sendJSON(serverMessage{ID: p.msgID, Type: "error", Error: "conversation not found within timeout"})
		})
		c.pendingConvSubs[msg.ConversationID] = pending
		c.mu.Unlock()
		return // response sent when subscription binds (or times out)
	}

	if buf == nil {
		c.sendJSON(serverMessage{ID: msg.ID, Type: "error", Error: "conversation not found"})
		return
	}

	filter := buildFilter(msg.Filter)
	snapshot, bufSubID, live, historyDoneCh, complete := buf.Subscribe(filter)

	c.mu.Lock()
	c.nextSub++
	sID := subID(c.nextSub)
	sub := &subscription{
		id:             sID,
		conversationID: msg.ConversationID,
		agentName:      agentName,
		bufSubID:       bufSubID,
		filter:         filter,
		live:           live,
	}
	c.subs[sID] = sub
	c.mu.Unlock()

	snapshot = capSnapshot(snapshot)
	c.sendJSON(serverMessage{
		ID:             msg.ID,
		Type:           "conversation-snapshot",
		SubscriptionID: sID,
		ConversationID: msg.ConversationID,
	})

	go c.streamSubscription(sID, msg.ConversationID, snapshot, live, historyDoneCh, complete, c.ctx)
}

func (c *Client) handleFollowAgent(msg clientMessage) {
	if msg.Agent == "" {
		c.sendJSON(serverMessage{ID: msg.ID, Type: "error", Error: "agent required"})
		return
	}

	// Remove existing follow for this agent (same-agent re-follow: release+reacquire)
	c.mu.Lock()
	if existing, ok := c.follows[msg.Agent]; ok {
		// Release tailing ref for the old follow before reacquiring
		c.server.watcher.ReleaseTailing(msg.Agent)
		delete(c.subs, existing.id)
		if existing.cancel != nil {
			existing.cancel()
		}
		if existing.live != nil {
			oldBuf := c.server.watcher.GetBuffer(existing.conversationID)
			if oldBuf != nil {
				oldBuf.Unsubscribe(existing.bufSubID)
			}
		}
		delete(c.follows, msg.Agent)
	}

	// Start tailing for this agent (ref-counted, idempotent if already active)
	if err := c.server.watcher.EnsureTailing(msg.Agent); err != nil {
		c.mu.Unlock()
		c.sendJSON(serverMessage{ID: msg.ID, Type: "error", Error: err.Error()})
		return
	}

	// Check if this agent's runtime supports conversation streaming
	var convSupported *bool
	if agent, ok := c.server.registry.GetAgent(msg.Agent); ok {
		convSupported = boolPtr(c.server.watcher.HasDiscoverer(agent.Runtime))
	}

	filter := buildFilter(msg.Filter)
	c.nextSub++
	sID := subID(c.nextSub)

	convID := c.server.watcher.GetActiveConversation(msg.Agent)

	if convID == "" {
		// No active conversation yet — register a pending follow
		sub := &subscription{
			id:        sID,
			agentName: msg.Agent,
			filter:    filter,
		}
		c.subs[sID] = sub
		c.follows[msg.Agent] = sub
		c.mu.Unlock()

		c.sendJSON(serverMessage{
			ID:                    msg.ID,
			Type:                  "follow-agent",
			OK:                    boolPtr(true),
			SubscriptionID:        sID,
			ConversationSupported: convSupported,
		})
		return
	}

	buf := c.server.watcher.GetBuffer(convID)
	if buf == nil {
		// Conversation ID exists but buffer doesn't yet — pending follow
		sub := &subscription{
			id:        sID,
			agentName: msg.Agent,
			filter:    filter,
		}
		c.subs[sID] = sub
		c.follows[msg.Agent] = sub
		c.mu.Unlock()

		c.sendJSON(serverMessage{
			ID:                    msg.ID,
			Type:                  "follow-agent",
			OK:                    boolPtr(true),
			SubscriptionID:        sID,
			ConversationSupported: convSupported,
		})
		return
	}

	snapshot, bufSubID, live, historyDoneCh, complete := buf.Subscribe(filter)
	subCtx, subCancel := context.WithCancel(c.ctx)
	sub := &subscription{
		id:             sID,
		conversationID: convID,
		agentName:      msg.Agent,
		bufSubID:       bufSubID,
		filter:         filter,
		live:           live,
		cancel:         subCancel,
	}
	c.subs[sID] = sub
	c.follows[msg.Agent] = sub
	c.mu.Unlock()

	snapshot = capSnapshot(snapshot)
	c.sendJSON(serverMessage{
		ID:                    msg.ID,
		Type:                  "follow-agent",
		OK:                    boolPtr(true),
		SubscriptionID:        sID,
		ConversationID:        convID,
		ConversationSupported: convSupported,
	})

	go c.streamSubscription(sID, convID, snapshot, live, historyDoneCh, complete, subCtx)
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

	if ok && sub.bufSubID != 0 {
		buf := c.server.watcher.GetBuffer(sub.conversationID)
		if buf != nil {
			buf.Unsubscribe(sub.bufSubID)
		}
	}

	// Release tailing ref for unsubscribed agent
	if ok && sub.agentName != "" {
		c.server.watcher.ReleaseTailing(sub.agentName)
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

	// Clean up any pending subscribe-conversation requests for this agent
	var pendingToRelease []string
	for convID, pending := range c.pendingConvSubs {
		if pending.agentName == msg.Agent {
			pending.timer.Stop()
			delete(c.pendingConvSubs, convID)
			pendingToRelease = append(pendingToRelease, pending.agentName)
		}
	}
	c.mu.Unlock()

	if ok && sub.bufSubID != 0 {
		buf := c.server.watcher.GetBuffer(sub.conversationID)
		if buf != nil {
			buf.Unsubscribe(sub.bufSubID)
		}
	}

	// Release tailing ref for unfollowed agent
	if ok && sub.agentName != "" {
		c.server.watcher.ReleaseTailing(sub.agentName)
	}

	// Release tailing refs for cleaned-up pending subscriptions
	for _, name := range pendingToRelease {
		c.server.watcher.ReleaseTailing(name)
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
			continue // already delivered via streamSubscription
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

	// 1. Check pending follow-agent subscriptions
	if sub, ok := c.follows[we.Agent.Name]; ok && sub.conversationID == "" {
		buf := c.server.watcher.GetBuffer(we.NewConvID)
		if buf != nil {
			snapshot, bufSubID, live, historyDoneCh, complete := buf.Subscribe(sub.filter)
			subCtx, subCancel := context.WithCancel(c.ctx)

			sub.conversationID = we.NewConvID
			sub.bufSubID = bufSubID
			sub.live = live
			sub.cancel = subCancel

			snapshot = capSnapshot(snapshot)
			c.sendJSON(serverMessage{
				Type:           "conversation-snapshot",
				SubscriptionID: sub.id,
				ConversationID: we.NewConvID,
			})

			go c.streamSubscription(sub.id, we.NewConvID, snapshot, live, historyDoneCh, complete, subCtx)
		}
	}

	// 2. Check pending subscribe-conversation requests
	if pending, hasPending := c.pendingConvSubs[we.NewConvID]; hasPending {
		pending.timer.Stop()
		delete(c.pendingConvSubs, we.NewConvID)

		buf := c.server.watcher.GetBuffer(we.NewConvID)
		if buf == nil {
			c.sendJSON(serverMessage{ID: pending.msgID, Type: "error", Error: "conversation buffer not available"})
			c.server.watcher.ReleaseTailing(pending.agentName)
			return
		}

		filter := buildFilter(pending.filter)
		snapshot, bufSubID, live, historyDoneCh, complete := buf.Subscribe(filter)
		c.nextSub++
		sID := subID(c.nextSub)
		subCtx, subCancel := context.WithCancel(c.ctx)
		pendingSub := &subscription{
			id:             sID,
			conversationID: we.NewConvID,
			agentName:      pending.agentName,
			bufSubID:       bufSubID,
			filter:         filter,
			live:           live,
			cancel:         subCancel,
		}
		c.subs[sID] = pendingSub

		snapshot = capSnapshot(snapshot)
		c.sendJSON(serverMessage{
			ID:             pending.msgID,
			Type:           "conversation-snapshot",
			SubscriptionID: sID,
			ConversationID: we.NewConvID,
		})

		go c.streamSubscription(sID, we.NewConvID, snapshot, live, historyDoneCh, complete, subCtx)
	}
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
	if sub.bufSubID != 0 {
		oldBuf := c.server.watcher.GetBuffer(sub.conversationID)
		if oldBuf != nil {
			oldBuf.Unsubscribe(sub.bufSubID)
		}
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

	snapshot, bufSubID, live, historyDoneCh, complete := newBuf.Subscribe(sub.filter)
	subCtx, subCancel := context.WithCancel(c.ctx)

	sub.conversationID = we.NewConvID
	sub.bufSubID = bufSubID
	sub.live = live
	sub.cancel = subCancel

	snapshot = capSnapshot(snapshot)
	c.sendJSON(serverMessage{
		Type:           "conversation-snapshot",
		SubscriptionID: sub.id,
		ConversationID: we.NewConvID,
		Reason:         "switch",
	})

	go c.streamSubscription(sub.id, we.NewConvID, snapshot, live, historyDoneCh, complete, subCtx)
}

// snapshotChunkSize is the batch size for streaming snapshot events to clients.
const snapshotChunkSize = 500

// streamSubscription sends buffered snapshot events as chunks, waits for the
// history-done signal if the initial file read hasn't completed, sends an
// end-of-history marker, then streams live events. All history is sent as
// conversation-snapshot-chunk messages; all live events after end-of-history
// are sent as conversation-event messages.
//
// The historyDoneCh is closed by ConversationBuffer.MarkHistoryDone() and is
// used instead of an in-channel sentinel — closing a channel never blocks and
// never drops, fixing the bug where a non-blocking sentinel send could be lost
// when the subscriber channel was full.
func (c *Client) streamSubscription(subID, convID string, snapshot []conv.ConversationEvent, live <-chan conv.ConversationEvent, historyDoneCh <-chan struct{}, historyComplete bool, ctx context.Context) {
	// Phase 1: Send buffered snapshot as chunks.
	// When history is already complete, we know the total and can show a real progress bar.
	total := 0
	if historyComplete {
		total = len(snapshot)
	}
	loaded := 0
	for i := 0; i < len(snapshot); i += snapshotChunkSize {
		end := min(i+snapshotChunkSize, len(snapshot))
		loaded = end
		if !c.sendJSONCritical(serverMessage{
			Type:           "conversation-snapshot-chunk",
			SubscriptionID: subID,
			ConversationID: convID,
			Events:         snapshot[i:end],
			Progress:       &snapshotProgress{Loaded: loaded, Total: total},
		}) {
			return
		}
	}

	// Phase 2: If history not complete, stream from live channel until historyDoneCh closes.
	if !historyComplete {
		for {
			select {
			case <-ctx.Done():
				return
			case event, ok := <-live:
				if !ok {
					return
				}
				loaded++
				if !c.sendJSONCritical(serverMessage{
					Type:           "conversation-snapshot-chunk",
					SubscriptionID: subID,
					ConversationID: convID,
					Events:         []conv.ConversationEvent{event},
					Progress:       &snapshotProgress{Loaded: loaded},
				}) {
					return
				}
			case <-historyDoneCh:
				historyComplete = true
			}
			if historyComplete {
				break
			}
		}
	}

	// End of history
	if !c.sendJSONCritical(serverMessage{
		Type:           "conversation-snapshot-end",
		SubscriptionID: subID,
		ConversationID: convID,
	}) {
		return
	}

	// Phase 3: Stream live events
	for {
		select {
		case <-ctx.Done():
			return
		case event, ok := <-live:
			if !ok {
				return
			}
			cursor := conv.Cursor{
				ConversationID: convID,
				Seq:            event.Seq,
				EventID:        event.EventID,
			}
			c.sendJSON(serverMessage{
				Type:           "conversation-event",
				SubscriptionID: subID,
				ConversationID: convID,
				Event:          &event,
				Cursor:         encodeCursor(cursor),
			})
		}
	}
}
