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
	snap, subID, live, _, _ := buf.Subscribe(EventFilter{})
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

	buf.Unsubscribe(subID)
}

func TestBufferSubscribeConcurrent(t *testing.T) {
	buf := NewConversationBuffer("test-conv", "test-agent", 1000)

	const total = 100

	// Subscribe first, then write concurrently
	snap, subID, live, _, _ := buf.Subscribe(EventFilter{})
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
	buf.Unsubscribe(subID)
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

func TestBufferHistoryDoneChannelReliable(t *testing.T) {
	// Regression test: MarkHistoryDone must be reliably detected even when
	// the subscriber's live channel is full. Previously, MarkHistoryDone sent
	// a sentinel through the subscriber channel (non-blocking), which was
	// silently dropped when the channel was full, causing streamSubscription
	// to block forever ("Loading conversation..." bug).
	buf := NewConversationBuffer("test-conv", "test-agent", 100000)

	_, subID, live, historyDoneCh, complete := buf.Subscribe(EventFilter{})
	if complete {
		t.Fatal("complete should be false before MarkHistoryDone")
	}

	// Fill the subscriber channel to capacity (256) so old sentinel would have been dropped
	for i := 0; i < 300; i++ {
		buf.Append(makeEvent(EventUser))
	}

	// Mark history done — with the old code, the sentinel would be dropped
	// because the channel is full. With the new code, historyDoneCh closes reliably.
	buf.MarkHistoryDone()

	// historyDoneCh should fire immediately
	select {
	case <-historyDoneCh:
		// success — history done signal received reliably
	case <-time.After(time.Second):
		t.Fatal("timeout waiting for historyDoneCh — signal was lost (the old bug)")
	}

	// Drain the live channel to verify events are still there
	drained := 0
	for {
		select {
		case <-live:
			drained++
		default:
			goto done
		}
	}
done:
	// We should have received up to the channel capacity (256), the rest were dropped
	if drained == 0 {
		t.Fatal("expected some events in live channel")
	}

	buf.Unsubscribe(subID)
}

func TestBufferHistoryDoneAlreadyComplete(t *testing.T) {
	// When history is already done at subscribe time, historyDoneCh should be
	// immediately readable (closed before subscribe).
	buf := NewConversationBuffer("test-conv", "test-agent", 100)

	buf.Append(makeEvent(EventUser))
	buf.MarkHistoryDone()

	_, subID, _, historyDoneCh, complete := buf.Subscribe(EventFilter{})
	if !complete {
		t.Fatal("complete should be true after MarkHistoryDone")
	}

	// historyDoneCh should be immediately readable (already closed)
	select {
	case <-historyDoneCh:
		// success
	default:
		t.Fatal("historyDoneCh should be immediately readable when history is already done")
	}

	buf.Unsubscribe(subID)
}

func TestBufferMarkHistoryDoneIdempotent(t *testing.T) {
	buf := NewConversationBuffer("test-conv", "test-agent", 100)
	buf.MarkHistoryDone()
	buf.MarkHistoryDone() // should not panic (double close)
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
