package conv

import (
	"bytes"
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

	// Drain nil sentinel (signals end of initial read)
	select {
	case line := <-tailer.Lines():
		if line != nil {
			t.Fatalf("expected nil sentinel, got %q", string(line))
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timeout waiting for nil sentinel")
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

	// Drain nil sentinel (signals end of initial read, even for fromEnd)
	select {
	case line := <-tailer.Lines():
		if line != nil {
			t.Fatalf("expected nil sentinel, got old content %q", string(line))
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timeout waiting for nil sentinel")
	}

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

	// Drain initial lines + nil sentinel
	for i := 0; i < 3; i++ {
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

func TestTailerDetectsReplaceWithLargerFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.json")

	// Initial file (small)
	if err := os.WriteFile(path, []byte("a\n"), 0644); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	tailer, err := NewTailer(ctx, path, true)
	if err != nil {
		t.Fatal(err)
	}
	defer tailer.Stop()

	// Initial line
	select {
	case line := <-tailer.Lines():
		if string(line) != "a" {
			t.Fatalf("initial line = %q, want %q", string(line), "a")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timeout waiting for initial line")
	}

	// Nil sentinel (initial read complete)
	select {
	case line := <-tailer.Lines():
		if line != nil {
			t.Fatalf("expected nil sentinel, got %q", string(line))
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timeout waiting for nil sentinel")
	}

	// Replace file atomically with a larger file.
	tmp := filepath.Join(dir, "new.json")
	if err := os.WriteFile(tmp, []byte("b\nc\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(tmp, path); err != nil {
		t.Fatal(err)
	}

	// Must re-read from start of replacement file; expect "b" then "c".
	var got []string
	timeout := time.After(5 * time.Second)
	for len(got) < 2 {
		select {
		case line := <-tailer.Lines():
			if line == nil {
				continue
			}
			got = append(got, string(line))
		case <-timeout:
			t.Fatalf("timeout waiting for replacement lines, got %v", got)
		}
	}
	if got[0] != "b" || got[1] != "c" {
		t.Fatalf("replacement lines = %v, want [b c]", got)
	}
}

func TestTailerHandlesLargeJSONLLine(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "large.jsonl")

	// 3MB single line + newline (exceeds prior 2MB scanner token size).
	large := bytes.Repeat([]byte("x"), 3*1024*1024)
	data := append(append([]byte(nil), large...), '\n')
	if err := os.WriteFile(path, data, 0644); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	tailer, err := NewTailer(ctx, path, true)
	if err != nil {
		t.Fatal(err)
	}
	defer tailer.Stop()

	select {
	case line := <-tailer.Lines():
		if len(line) != len(large) {
			t.Fatalf("line length = %d, want %d", len(line), len(large))
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timeout waiting for large line")
	}

	select {
	case line := <-tailer.Lines():
		if line != nil {
			t.Fatalf("expected nil sentinel, got line len %d", len(line))
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timeout waiting for nil sentinel")
	}
}

func TestTailerFullDocRereadsFromStartOnChange(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "session.json")

	if err := os.WriteFile(path, []byte("a\n"), 0644); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	tailer, err := NewTailer(ctx, path, true)
	if err != nil {
		t.Fatal(err)
	}
	defer tailer.Stop()

	// Initial line + nil sentinel
	select {
	case line := <-tailer.Lines():
		if string(line) != "a" {
			t.Fatalf("initial line = %q, want %q", string(line), "a")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timeout waiting for initial line")
	}
	select {
	case line := <-tailer.Lines():
		if line != nil {
			t.Fatalf("expected nil sentinel, got %q", string(line))
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timeout waiting for nil sentinel")
	}

	// Grow file without truncation.
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString("b\n"); err != nil {
		_ = f.Close()
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}

	// Full-doc mode should re-read from start and emit both lines.
	var got []string
	timeout := time.After(5 * time.Second)
	for len(got) < 2 {
		select {
		case line := <-tailer.Lines():
			if line == nil {
				continue
			}
			got = append(got, string(line))
		case <-timeout:
			t.Fatalf("timeout waiting for reread lines, got %v", got)
		}
	}
	if got[0] != "a" || got[1] != "b" {
		t.Fatalf("reread lines = %v, want [a b]", got)
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

	// Drain initial line + nil sentinel
	for i := 0; i < 2; i++ {
		select {
		case <-tailer.Lines():
		case <-time.After(3 * time.Second):
			t.Fatal("timeout draining initial data")
		}
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
