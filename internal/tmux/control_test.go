package tmux

import (
	"strings"
	"testing"
	"time"
)

type writeCloserStub struct {
	writeFn func([]byte) (int, error)
	closeFn func() error
}

func (w writeCloserStub) Write(p []byte) (int, error) {
	if w.writeFn != nil {
		return w.writeFn(p)
	}
	return len(p), nil
}

func (w writeCloserStub) Close() error {
	if w.closeFn != nil {
		return w.closeFn()
	}
	return nil
}

func TestExecuteTimeout(t *testing.T) {
	cm := &ControlMode{
		stdin:          writeCloserStub{},
		responseCh:     make(chan commandResponse),
		done:           make(chan struct{}),
		executeTimeout: 20 * time.Millisecond,
	}

	start := time.Now()
	_, err := cm.Execute("list-sessions")
	if err == nil {
		t.Fatal("expected timeout error, got nil")
	}
	if !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("error = %q, expected timeout message", err)
	}
	if elapsed := time.Since(start); elapsed < cm.executeTimeout {
		t.Fatalf("elapsed = %v, expected at least %v", elapsed, cm.executeTimeout)
	}
}

func TestExecuteReturnsResponse(t *testing.T) {
	cm := &ControlMode{
		stdin:          writeCloserStub{},
		responseCh:     make(chan commandResponse, 1),
		done:           make(chan struct{}),
		executeTimeout: 200 * time.Millisecond,
	}

	cm.stdin = writeCloserStub{
		writeFn: func(p []byte) (int, error) {
			go func() {
				cm.responseCh <- commandResponse{output: "ok"}
			}()
			return len(p), nil
		},
	}

	out, err := cm.Execute("display-message")
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if out != "ok" {
		t.Fatalf("output = %q, want %q", out, "ok")
	}
}
