package tmux

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestCapturePaneVisibleFallsBackWhenNoAlternateScreen(t *testing.T) {
	var executed []string

	cm := &ControlMode{
		responseCh:     make(chan commandResponse, 1),
		done:           make(chan struct{}),
		executeTimeout: 200 * time.Millisecond,
	}
	cm.stdin = writeCloserStub{
		writeFn: func(p []byte) (int, error) {
			cmd := strings.TrimSpace(string(p))
			executed = append(executed, cmd)

			go func(command string) {
				if strings.Contains(command, "capture-pane -p -e -a ") {
					cm.responseCh <- commandResponse{err: fmt.Errorf("tmux: no alternate screen")}
					return
				}
				cm.responseCh <- commandResponse{output: "visible-screen"}
			}(cmd)

			return len(p), nil
		},
	}

	out, err := cm.CapturePaneVisible("hq-mayor")
	if err != nil {
		t.Fatalf("CapturePaneVisible() error = %v", err)
	}
	if out != "visible-screen" {
		t.Fatalf("output = %q, want %q", out, "visible-screen")
	}
	if len(executed) != 2 {
		t.Fatalf("executed command count = %d, want 2", len(executed))
	}
	if !strings.Contains(executed[0], "capture-pane -p -e -a ") {
		t.Fatalf("first command = %q, expected alternate-screen capture", executed[0])
	}
	if strings.Contains(executed[1], "capture-pane -p -e -a ") {
		t.Fatalf("second command = %q, expected non-alternate fallback", executed[1])
	}
}
