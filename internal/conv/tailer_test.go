package conv

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestTailerFollowsAppend(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.jsonl")

	// Create file with initial content
	if err := os.WriteFile(path, []byte(`{"line":1}`+"\n"), 0644); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	tailer, err := NewTailer(ctx, path, true)
	if err != nil {
		t.Fatal(err)
	}
	defer tailer.Stop()

	// Should get initial line
	select {
	case line := <-tailer.Lines():
		if string(line) != `{"line":1}` {
			t.Fatalf("first line = %q, want initial content", string(line))
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timeout waiting for initial line")
	}

	// Append a new line
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = f.WriteString(`{"line":2}` + "\n")
	_ = f.Close()

	// Should get the new line
	select {
	case line := <-tailer.Lines():
		if string(line) != `{"line":2}` {
			t.Fatalf("second line = %q, want appended content", string(line))
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timeout waiting for appended line")
	}
}

func TestTailerFromEnd(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.jsonl")

	// Create file with existing content
	if err := os.WriteFile(path, []byte(`{"old":true}`+"\n"), 0644); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	tailer, err := NewTailer(ctx, path, false)
	if err != nil {
		t.Fatal(err)
	}
	defer tailer.Stop()

	// Old content should NOT appear
	select {
	case line := <-tailer.Lines():
		t.Fatalf("should not receive old content, got %q", string(line))
	case <-time.After(500 * time.Millisecond):
		// good — no old data
	}

	// Append new data
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = f.WriteString(`{"new":true}` + "\n")
	_ = f.Close()

	// New content should appear
	select {
	case line := <-tailer.Lines():
		if string(line) != `{"new":true}` {
			t.Fatalf("line = %q, want new content", string(line))
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timeout waiting for new line")
	}
}

func TestTailerDetectsTruncation(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.jsonl")

	if err := os.WriteFile(path, []byte(`{"line":1}`+"\n"+`{"line":2}`+"\n"), 0644); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	tailer, err := NewTailer(ctx, path, true)
	if err != nil {
		t.Fatal(err)
	}
	defer tailer.Stop()

	// Drain initial lines
	for i := 0; i < 2; i++ {
		select {
		case <-tailer.Lines():
		case <-time.After(3 * time.Second):
			t.Fatal("timeout draining initial lines")
		}
	}

	// Truncate the file and write new, shorter content.
	// Content must be shorter than the original (22 bytes) so the
	// size-based truncation detection (info.Size() < offset) triggers.
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = f.WriteString(`{"t":1}` + "\n")
	_ = f.Close()

	// Should detect truncation and read new content
	// May receive partial artifacts during truncation race; look for the expected line
	timeout := time.After(5 * time.Second)
	for {
		select {
		case line := <-tailer.Lines():
			if string(line) == `{"t":1}` {
				return // success
			}
		case <-timeout:
			t.Fatal("timeout waiting for post-truncation line")
		}
	}
}

func TestTailerShutdown(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.jsonl")

	if err := os.WriteFile(path, []byte(`{"line":1}`+"\n"), 0644); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())

	tailer, err := NewTailer(ctx, path, true)
	if err != nil {
		t.Fatal(err)
	}

	// Drain initial
	select {
	case <-tailer.Lines():
	case <-time.After(3 * time.Second):
		t.Fatal("timeout")
	}

	// Cancel context
	cancel()
	tailer.Stop()

	// Channel should close
	select {
	case _, ok := <-tailer.Lines():
		if ok {
			t.Fatal("expected channel to be closed after stop")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timeout waiting for channel close")
	}
}
