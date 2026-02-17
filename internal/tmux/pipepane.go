package tmux

import (
	"context"
	"fmt"
	"io"
	"log"
	"os"
	"strings"
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
	subscribers map[int]chan []byte
	nextSubID   int
	mu          sync.Mutex
}

// NewPipePaneManager creates a new pipe-pane manager.
func NewPipePaneManager(ctrl *ControlMode) *PipePaneManager {
	return &PipePaneManager{
		ctrl:    ctrl,
		streams: make(map[string]*pipeStream),
	}
}

// Subscribe starts streaming output for a session and returns a subscriber ID
// and channel for receiving raw bytes. If this is the first subscriber, pipe-pane is activated.
func (pm *PipePaneManager) Subscribe(session string) (int, <-chan []byte, error) {
	pm.mu.Lock()
	defer pm.mu.Unlock()

	ch := make(chan []byte, 256)

	stream, exists := pm.streams[session]
	if exists {
		stream.mu.Lock()
		stream.nextSubID++
		id := stream.nextSubID
		stream.subscribers[id] = ch
		stream.mu.Unlock()
		return id, ch, nil
	}

	// First subscriber — activate pipe-pane
	// Sanitize target for use in file path (pane targets contain : and .)
	safeName := strings.NewReplacer(":", "-", ".", "-", "/", "-").Replace(session)
	filePath := fmt.Sprintf("/tmp/adapter-%s.pipe", safeName)

	// Create the file if it doesn't exist
	f, err := os.OpenFile(filePath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		return 0, nil, fmt.Errorf("create pipe file: %w", err)
	}
	if err := f.Close(); err != nil {
		return 0, nil, fmt.Errorf("close pipe file: %w", err)
	}

	// Activate pipe-pane
	if err := pm.ctrl.PipePaneStart(session, fmt.Sprintf("cat >> %s", filePath)); err != nil {
		if rmErr := os.Remove(filePath); rmErr != nil {
			log.Printf("pipe-pane cleanup %s: %v", filePath, rmErr)
		}
		return 0, nil, fmt.Errorf("activate pipe-pane: %w", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	stream = &pipeStream{
		session:     session,
		filePath:    filePath,
		cancel:      cancel,
		subscribers: map[int]chan []byte{1: ch},
		nextSubID:   1,
	}
	pm.streams[session] = stream

	go pm.tailFile(ctx, stream)

	return 1, ch, nil
}

// Unsubscribe removes a subscriber by ID. If it was the last one, pipe-pane is deactivated.
func (pm *PipePaneManager) Unsubscribe(session string, id int) {
	pm.mu.Lock()
	defer pm.mu.Unlock()

	stream, exists := pm.streams[session]
	if !exists {
		return
	}

	stream.mu.Lock()
	if ch, ok := stream.subscribers[id]; ok {
		delete(stream.subscribers, id)
		close(ch)
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
	if err := pm.ctrl.PipePaneStop(stream.session); err != nil {
		log.Printf("pipe-pane stop %s: %v", stream.session, err)
	}

	stream.mu.Lock()
	for _, ch := range stream.subscribers {
		close(ch)
	}
	stream.subscribers = nil
	stream.mu.Unlock()

	if err := os.Remove(stream.filePath); err != nil {
		log.Printf("pipe file cleanup %s: %v", stream.filePath, err)
	}
}

// tailFile reads new bytes from the pipe file and fans out raw bytes to subscribers at ~30fps.
func (pm *PipePaneManager) tailFile(ctx context.Context, stream *pipeStream) {
	f, err := os.Open(stream.filePath)
	if err != nil {
		log.Printf("open pipe file %s: %v", stream.filePath, err)
		return
	}
	defer func() {
		if closeErr := f.Close(); closeErr != nil {
			log.Printf("close pipe file %s: %v", stream.filePath, closeErr)
		}
	}()

	// Seek to end — we only want new output
	if _, err := f.Seek(0, io.SeekEnd); err != nil {
		log.Printf("seek pipe file %s: %v", stream.filePath, err)
		return
	}

	// Pending buffer accumulates raw bytes across multiple reads.
	var pending []byte
	var pendingMu sync.Mutex

	// Read goroutine: continuously reads raw bytes into buffer
	readDone := make(chan struct{})
	go func() {
		defer close(readDone)
		buf := make([]byte, 4096)
		for {
			select {
			case <-ctx.Done():
				return
			default:
			}

			n, err := f.Read(buf)
			if n > 0 {
				pendingMu.Lock()
				pending = append(pending, buf[:n]...)
				pendingMu.Unlock()
			}

			if err != nil || n == 0 {
				select {
				case <-ctx.Done():
					return
				case <-time.After(50 * time.Millisecond):
				}
			}
		}
	}()

	// Send loop: flush accumulated bytes to subscribers at ~30fps
	ticker := time.NewTicker(33 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-readDone:
			return
		case <-ticker.C:
			pendingMu.Lock()
			if len(pending) == 0 {
				pendingMu.Unlock()
				continue
			}
			data := pending
			pending = nil
			pendingMu.Unlock()

			stream.mu.Lock()
			for _, ch := range stream.subscribers {
				select {
				case ch <- data:
				default:
					// Subscriber is slow — drop this update
				}
			}
			stream.mu.Unlock()
		}
	}
}
