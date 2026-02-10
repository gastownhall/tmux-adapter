package tmux

import (
	"context"
	"fmt"
	"io"
	"log"
	"os"
	"sync"
	"time"
)

// PipePaneManager manages pipe-pane output streaming per agent session.
type PipePaneManager struct {
	ctrl    *ControlMode
	mu      sync.Mutex
	streams map[string]*pipeStream
}

type pipeStream struct {
	session     string
	filePath    string
	cancel      context.CancelFunc
	subscribers map[chan []byte]struct{}
	mu          sync.Mutex
}

// NewPipePaneManager creates a new pipe-pane manager.
func NewPipePaneManager(ctrl *ControlMode) *PipePaneManager {
	return &PipePaneManager{
		ctrl:    ctrl,
		streams: make(map[string]*pipeStream),
	}
}

// Subscribe starts streaming output for a session and returns a channel for receiving bytes.
// If this is the first subscriber, pipe-pane is activated.
func (pm *PipePaneManager) Subscribe(session string) (<-chan []byte, error) {
	pm.mu.Lock()
	defer pm.mu.Unlock()

	ch := make(chan []byte, 256)

	stream, exists := pm.streams[session]
	if exists {
		stream.mu.Lock()
		stream.subscribers[ch] = struct{}{}
		stream.mu.Unlock()
		return ch, nil
	}

	// First subscriber — activate pipe-pane
	filePath := fmt.Sprintf("/tmp/adapter-%s.pipe", session)

	// Create the file if it doesn't exist
	f, err := os.OpenFile(filePath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		return nil, fmt.Errorf("create pipe file: %w", err)
	}
	f.Close()

	// Activate pipe-pane
	if err := pm.ctrl.PipePaneStart(session, fmt.Sprintf("cat >> %s", filePath)); err != nil {
		os.Remove(filePath)
		return nil, fmt.Errorf("activate pipe-pane: %w", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	stream = &pipeStream{
		session:     session,
		filePath:    filePath,
		cancel:      cancel,
		subscribers: map[chan []byte]struct{}{ch: {}},
	}
	pm.streams[session] = stream

	go pm.tailFile(ctx, stream)

	return ch, nil
}

// Unsubscribe removes a subscriber. If it was the last one, pipe-pane is deactivated.
func (pm *PipePaneManager) Unsubscribe(session string, ch <-chan []byte) {
	pm.mu.Lock()
	defer pm.mu.Unlock()

	stream, exists := pm.streams[session]
	if !exists {
		return
	}

	// Find and remove the channel (convert back to the send-capable type for map lookup)
	stream.mu.Lock()
	for sub := range stream.subscribers {
		// Compare by checking if they're the same channel
		if (<-chan []byte)(sub) == ch {
			delete(stream.subscribers, sub)
			close(sub)
			break
		}
	}
	remaining := len(stream.subscribers)
	stream.mu.Unlock()

	if remaining == 0 {
		pm.stopStream(stream)
		delete(pm.streams, session)
	}
}

// StopAll deactivates all pipe-panes and cleans up.
func (pm *PipePaneManager) StopAll() {
	pm.mu.Lock()
	defer pm.mu.Unlock()

	for name, stream := range pm.streams {
		pm.stopStream(stream)
		delete(pm.streams, name)
	}
}

func (pm *PipePaneManager) stopStream(stream *pipeStream) {
	stream.cancel()
	pm.ctrl.PipePaneStop(stream.session)

	stream.mu.Lock()
	for ch := range stream.subscribers {
		close(ch)
	}
	stream.subscribers = nil
	stream.mu.Unlock()

	os.Remove(stream.filePath)
}

// tailFile reads new bytes from the pipe file and fans them out to subscribers.
func (pm *PipePaneManager) tailFile(ctx context.Context, stream *pipeStream) {
	f, err := os.Open(stream.filePath)
	if err != nil {
		log.Printf("open pipe file %s: %v", stream.filePath, err)
		return
	}
	defer f.Close()

	// Seek to end — we only want new output
	f.Seek(0, io.SeekEnd)

	buf := make([]byte, 4096)
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		n, err := f.Read(buf)
		if n > 0 {
			data := make([]byte, n)
			copy(data, buf[:n])

			stream.mu.Lock()
			for ch := range stream.subscribers {
				select {
				case ch <- data:
				default:
					// Subscriber is slow — drop this chunk
				}
			}
			stream.mu.Unlock()
		}

		if err != nil || n == 0 {
			// No new data — wait before trying again
			select {
			case <-ctx.Done():
				return
			case <-time.After(50 * time.Millisecond):
			}
		}
	}
}
