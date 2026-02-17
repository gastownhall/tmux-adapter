package conv

import (
	"bufio"
	"context"
	"io"
	"log"
	"os"
	"path/filepath"
	"time"

	"github.com/fsnotify/fsnotify"
)

// MaxReReadFileSize is the safety valve for full-file reads (Gemini strategy).
const MaxReReadFileSize = 8 * 1024 * 1024

// Tailer watches a conversation file and emits complete lines as they are appended.
type Tailer struct {
	path    string
	offset  int64
	partial []byte
	watcher *fsnotify.Watcher
	lines   chan []byte
	ctx     context.Context
	cancel  context.CancelFunc
}

// NewTailer creates a JSONL tailer for the given file.
// If fromStart is true, reads from the beginning (history replay).
// If false, seeks to end (live-only).
func NewTailer(ctx context.Context, path string, fromStart bool) (*Tailer, error) {
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, err
	}

	// Watch the directory containing the file
	dir := filepath.Dir(path)
	if err := watcher.Add(dir); err != nil {
		_ = watcher.Close()
		return nil, err
	}

	tCtx, cancel := context.WithCancel(ctx)

	t := &Tailer{
		path:    path,
		watcher: watcher,
		lines:   make(chan []byte, 256),
		ctx:     tCtx,
		cancel:  cancel,
	}

	if !fromStart {
		if info, err := os.Stat(path); err == nil {
			t.offset = info.Size()
		}
	}

	go t.tailLoop()

	return t, nil
}

// Lines returns a channel of complete JSONL lines.
func (t *Tailer) Lines() <-chan []byte {
	return t.lines
}

// Stop shuts down the tailer.
func (t *Tailer) Stop() {
	t.cancel()
	_ = t.watcher.Close()
}

func (t *Tailer) tailLoop() {
	defer close(t.lines)

	// Initial read — all existing file content
	t.readNewData()

	// Sentinel: nil line signals initial read is complete. Consumers use this
	// to know when all historical data has been delivered through the channel.
	select {
	case t.lines <- nil:
	case <-t.ctx.Done():
		return
	}

	// Poll fallback timer (1s with jitter)
	pollTicker := time.NewTicker(time.Second)
	defer pollTicker.Stop()

	for {
		select {
		case <-t.ctx.Done():
			return
		case event, ok := <-t.watcher.Events:
			if !ok {
				return
			}
			if event.Has(fsnotify.Write) || event.Has(fsnotify.Create) {
				t.readNewData()
			}
		case _, ok := <-t.watcher.Errors:
			if !ok {
				return
			}
		case <-pollTicker.C:
			t.readNewData()
		}
	}
}

func (t *Tailer) readNewData() {
	f, err := os.Open(t.path)
	if err != nil {
		return // file doesn't exist yet
	}
	defer func() { _ = f.Close() }()

	// Check for truncation or rotation
	info, err := f.Stat()
	if err != nil {
		return
	}
	if info.Size() < t.offset {
		// File was truncated — reset
		t.offset = 0
		t.partial = nil
	}

	if info.Size() == t.offset {
		return // no new data
	}

	if _, err := f.Seek(t.offset, io.SeekStart); err != nil {
		return
	}

	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 2*1024*1024), 2*1024*1024) // 2MB buffer

	for scanner.Scan() {
		line := scanner.Bytes()
		if len(t.partial) > 0 {
			line = append(t.partial, line...)
			t.partial = nil
		}
		if len(line) == 0 {
			continue
		}
		// Make a copy since scanner reuses the buffer
		lineCopy := make([]byte, len(line))
		copy(lineCopy, line)

		select {
		case t.lines <- lineCopy:
		case <-t.ctx.Done():
			return
		}
	}

	if err := scanner.Err(); err != nil {
		log.Printf("tailer read %s: %v", t.path, err)
	}

	// Track current position
	newOffset, err := f.Seek(0, io.SeekCurrent)
	if err == nil {
		t.offset = newOffset
	}
}

