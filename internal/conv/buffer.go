package conv

import (
	"sync"
)

// ConversationBuffer is a per-conversation event ring buffer with snapshot + live streaming.
type ConversationBuffer struct {
	conversationID string
	agentName      string
	events         []ConversationEvent
	maxSize        int
	nextSeq        int64
	mu             sync.Mutex // Must be full Lock (not RLock) for gap-free snapshot+subscribe
	subs           map[chan ConversationEvent]EventFilter
}

// NewConversationBuffer creates a buffer for a specific conversation.
func NewConversationBuffer(conversationID, agentName string, maxSize int) *ConversationBuffer {
	return &ConversationBuffer{
		conversationID: conversationID,
		agentName:      agentName,
		events:         make([]ConversationEvent, 0, 256),
		maxSize:        maxSize,
		subs:           make(map[chan ConversationEvent]EventFilter),
	}
}

// Append adds an event to the buffer and broadcasts to subscribers.
func (b *ConversationBuffer) Append(event ConversationEvent) {
	b.mu.Lock()
	defer b.mu.Unlock()

	event.Seq = b.nextSeq
	b.nextSeq++

	// Evict oldest if at capacity
	if len(b.events) >= b.maxSize {
		// Copy to new backing array so old references don't pin memory
		newEvents := make([]ConversationEvent, len(b.events)-1, b.maxSize)
		copy(newEvents, b.events[1:])
		b.events = newEvents
	}
	b.events = append(b.events, event)

	// Broadcast to subscribers (non-blocking)
	for ch, filter := range b.subs {
		if filter.Matches(event) {
			select {
			case ch <- event:
			default:
				// Slow consumer — drop event
			}
		}
	}
}

// Snapshot returns all buffered events, optionally filtered.
func (b *ConversationBuffer) Snapshot(filter EventFilter) []ConversationEvent {
	b.mu.Lock()
	defer b.mu.Unlock()

	return b.snapshotLocked(filter)
}

func (b *ConversationBuffer) snapshotLocked(filter EventFilter) []ConversationEvent {
	result := make([]ConversationEvent, 0, len(b.events))
	for _, e := range b.events {
		if filter.Matches(e) {
			result = append(result, e)
		}
	}
	return result
}

// Subscribe returns a snapshot of current events and a live channel for new events.
// The snapshot and channel registration are atomic — no events are missed between them.
func (b *ConversationBuffer) Subscribe(filter EventFilter) (snapshot []ConversationEvent, live <-chan ConversationEvent) {
	b.mu.Lock()
	defer b.mu.Unlock()

	snapshot = b.snapshotLocked(filter)

	ch := make(chan ConversationEvent, 256)
	b.subs[ch] = filter
	return snapshot, ch
}

// Unsubscribe removes a subscriber and closes its channel.
func (b *ConversationBuffer) Unsubscribe(ch <-chan ConversationEvent) {
	b.mu.Lock()
	defer b.mu.Unlock()

	for sub := range b.subs {
		if (<-chan ConversationEvent)(sub) == ch {
			delete(b.subs, sub)
			close(sub)
			return
		}
	}
}

// MinSeq returns the lowest sequence number still in the buffer, or -1 if empty.
func (b *ConversationBuffer) MinSeq() int64 {
	b.mu.Lock()
	defer b.mu.Unlock()
	if len(b.events) == 0 {
		return -1
	}
	return b.events[0].Seq
}

// EventsSince returns events with seq > afterSeq, or nil if afterSeq is no longer in buffer.
func (b *ConversationBuffer) EventsSince(afterSeq int64, filter EventFilter) ([]ConversationEvent, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()

	if len(b.events) == 0 {
		return nil, true
	}

	minSeq := b.events[0].Seq
	if afterSeq < minSeq-1 {
		return nil, false // gap: events have been evicted
	}

	var result []ConversationEvent
	for _, e := range b.events {
		if e.Seq > afterSeq && filter.Matches(e) {
			result = append(result, e)
		}
	}
	return result, true
}
