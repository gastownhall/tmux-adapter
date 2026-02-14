package conv

import (
	"sync"
	"testing"
	"time"
)

func makeEvent(typ string) ConversationEvent {
	return ConversationEvent{
		Type:           typ,
		AgentName:      "test-agent",
		ConversationID: "test-conv",
		Timestamp:      time.Now(),
		Runtime:        "claude",
	}
}

func TestBufferAppendAndSnapshot(t *testing.T) {
	buf := NewConversationBuffer("test-conv", "test-agent", 100)

	buf.Append(makeEvent(EventUser))
	buf.Append(makeEvent(EventAssistant))
	buf.Append(makeEvent(EventToolUse))

	snap := buf.Snapshot(EventFilter{})
	if len(snap) != 3 {
		t.Fatalf("snapshot len = %d, want 3", len(snap))
	}
	if snap[0].Seq != 0 || snap[1].Seq != 1 || snap[2].Seq != 2 {
		t.Fatalf("seq values = %d,%d,%d; want 0,1,2", snap[0].Seq, snap[1].Seq, snap[2].Seq)
	}
}

func TestBufferEviction(t *testing.T) {
	buf := NewConversationBuffer("test-conv", "test-agent", 3)

	for i := 0; i < 5; i++ {
		buf.Append(makeEvent(EventUser))
	}

	snap := buf.Snapshot(EventFilter{})
	if len(snap) != 3 {
		t.Fatalf("snapshot len = %d, want 3 (maxSize)", len(snap))
	}
	// Oldest events should be evicted
	if snap[0].Seq != 2 {
		t.Fatalf("first event seq = %d, want 2", snap[0].Seq)
	}
}

func TestBufferSubscribeNoGap(t *testing.T) {
	buf := NewConversationBuffer("test-conv", "test-agent", 100)

	// Add some initial events
	buf.Append(makeEvent(EventUser))
	buf.Append(makeEvent(EventAssistant))

	// Subscribe — should get snapshot + live channel
	snap, live := buf.Subscribe(EventFilter{})
	if len(snap) != 2 {
		t.Fatalf("snapshot len = %d, want 2", len(snap))
	}

	// Append while subscribed
	buf.Append(makeEvent(EventToolUse))

	select {
	case e := <-live:
		if e.Type != EventToolUse {
			t.Fatalf("live event type = %q, want %q", e.Type, EventToolUse)
		}
		if e.Seq != 2 {
			t.Fatalf("live event seq = %d, want 2", e.Seq)
		}
	case <-time.After(time.Second):
		t.Fatal("timeout waiting for live event")
	}

	buf.Unsubscribe(live)
}

func TestBufferSubscribeConcurrent(t *testing.T) {
	buf := NewConversationBuffer("test-conv", "test-agent", 1000)

	const total = 100

	// Subscribe first, then write concurrently
	snap, live := buf.Subscribe(EventFilter{})
	received := len(snap)

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < total; i++ {
			buf.Append(makeEvent(EventAssistant))
		}
	}()

	// Collect events until we have all of them
	timeout := time.After(5 * time.Second)
	for received < total {
		select {
		case <-live:
			received++
		case <-timeout:
			t.Fatalf("timeout: received %d events total (snap+live), want %d", received, total)
		}
	}

	wg.Wait()
	buf.Unsubscribe(live)
}

func TestBufferFilterTypes(t *testing.T) {
	buf := NewConversationBuffer("test-conv", "test-agent", 100)

	buf.Append(makeEvent(EventUser))
	buf.Append(makeEvent(EventAssistant))
	buf.Append(makeEvent(EventThinking))
	buf.Append(makeEvent(EventProgress))

	// Filter to only user+assistant
	filter := EventFilter{Types: map[string]bool{EventUser: true, EventAssistant: true}}
	snap := buf.Snapshot(filter)
	if len(snap) != 2 {
		t.Fatalf("filtered snapshot len = %d, want 2", len(snap))
	}
}

func TestBufferFilterExcludeThinking(t *testing.T) {
	buf := NewConversationBuffer("test-conv", "test-agent", 100)

	buf.Append(makeEvent(EventUser))
	buf.Append(makeEvent(EventThinking))
	buf.Append(makeEvent(EventAssistant))

	filter := EventFilter{ExcludeThinking: true}
	snap := buf.Snapshot(filter)
	if len(snap) != 2 {
		t.Fatalf("filtered snapshot len = %d, want 2 (thinking excluded)", len(snap))
	}
}

func TestBufferFilterExcludeProgress(t *testing.T) {
	buf := NewConversationBuffer("test-conv", "test-agent", 100)

	buf.Append(makeEvent(EventUser))
	buf.Append(makeEvent(EventProgress))
	buf.Append(makeEvent(EventAssistant))

	filter := EventFilter{ExcludeProgress: true}
	snap := buf.Snapshot(filter)
	if len(snap) != 2 {
		t.Fatalf("filtered snapshot len = %d, want 2 (progress excluded)", len(snap))
	}
}

func TestBufferEventsSince(t *testing.T) {
	buf := NewConversationBuffer("test-conv", "test-agent", 100)

	for i := 0; i < 5; i++ {
		buf.Append(makeEvent(EventUser))
	}

	events, ok := buf.EventsSince(2, EventFilter{})
	if !ok {
		t.Fatal("EventsSince returned not ok")
	}
	if len(events) != 2 {
		t.Fatalf("EventsSince len = %d, want 2 (seq 3 and 4)", len(events))
	}
	if events[0].Seq != 3 {
		t.Fatalf("first event seq = %d, want 3", events[0].Seq)
	}
}

func TestBufferEventsSinceGap(t *testing.T) {
	buf := NewConversationBuffer("test-conv", "test-agent", 3)

	for i := 0; i < 10; i++ {
		buf.Append(makeEvent(EventUser))
	}

	_, ok := buf.EventsSince(0, EventFilter{})
	if ok {
		t.Fatal("EventsSince should return not ok when events have been evicted")
	}
}

func TestBufferMinSeq(t *testing.T) {
	buf := NewConversationBuffer("test-conv", "test-agent", 3)

	if buf.MinSeq() != -1 {
		t.Fatalf("MinSeq on empty buffer = %d, want -1", buf.MinSeq())
	}

	for i := 0; i < 5; i++ {
		buf.Append(makeEvent(EventUser))
	}

	if buf.MinSeq() != 2 {
		t.Fatalf("MinSeq = %d, want 2", buf.MinSeq())
	}
}
