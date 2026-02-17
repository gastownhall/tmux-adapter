package conv

import (
	"log"
	"sync"
)

// bufferSub holds a subscriber's channel and filter.
type bufferSub struct {
	ch     chan ConversationEvent
	filter EventFilter
}

// ConversationBuffer is a per-conversation event ring buffer with snapshot + live streaming.
type ConversationBuffer struct {
	conversationID string
	agentName      string
	events         []ConversationEvent
	maxSize        int
	nextSeq        int64
	mu             sync.Mutex // Must be full Lock (not RLock) for gap-free snapshot+subscribe
	subs           map[int]bufferSub
	nextSubID      int
	historyDone    bool           // true once the initial file read is complete
	historyDoneCh  chan struct{}   // closed when historyDone becomes true; never blocks
}

// NewConversationBuffer creates a buffer for a specific conversation.
func NewConversationBuffer(conversationID, agentName string, maxSize int) *ConversationBuffer {
	return &ConversationBuffer{
		conversationID: conversationID,
		agentName:      agentName,
		events:         make([]ConversationEvent, 0, 256),
		maxSize:        maxSize,
		subs:           make(map[int]bufferSub),
		historyDoneCh:  make(chan struct{}),
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
	for _, sub := range b.subs {
		if sub.filter.Matches(event) {
			select {
			case sub.ch <- event:
			default:
				log.Printf("buffer %s: dropped event seq=%d type=%s to slow subscriber", b.conversationID[:min(8, len(b.conversationID))], event.Seq, event.Type)
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

// Subscribe returns a snapshot of current events, a subscriber ID, a live channel
// for new events, a channel that closes when the initial file read is complete,
// and whether the initial file read is already complete. When complete is false,
// callers should select on historyDoneCh to detect completion reliably (the channel
// is closed by MarkHistoryDone and never blocks, unlike the old sentinel approach).
func (b *ConversationBuffer) Subscribe(filter EventFilter) (snapshot []ConversationEvent, subID int, live <-chan ConversationEvent, historyDoneCh <-chan struct{}, complete bool) {
	b.mu.Lock()
	defer b.mu.Unlock()

	snapshot = b.snapshotLocked(filter)
	complete = b.historyDone

	ch := make(chan ConversationEvent, 256)
	b.nextSubID++
	subID = b.nextSubID
	b.subs[subID] = bufferSub{ch: ch, filter: filter}
	return snapshot, subID, ch, b.historyDoneCh, complete
}

// MarkHistoryDone signals that the initial file read is complete. All goroutines
// selecting on the historyDoneCh (returned by Subscribe) are woken reliably —
// closing a channel never blocks and never drops.
func (b *ConversationBuffer) MarkHistoryDone() {
	b.mu.Lock()
	defer b.mu.Unlock()

	if b.historyDone {
		return // already done
	}
	b.historyDone = true
	close(b.historyDoneCh)
}

// Unsubscribe removes a subscriber by ID and closes its channel.
func (b *ConversationBuffer) Unsubscribe(subID int) {
	b.mu.Lock()
	defer b.mu.Unlock()

	if sub, ok := b.subs[subID]; ok {
		delete(b.subs, subID)
		close(sub.ch)
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
